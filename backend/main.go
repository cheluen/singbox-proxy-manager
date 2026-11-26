package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sb-proxy/backend/api"
	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
	frontendassets "sb-proxy/frontend"
	"sb-proxy/internal/agreement"
	"sb-proxy/internal/kernel"
	"sb-proxy/internal/version"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

const (
	defaultConfigDir            = "./config"
	defaultPort                 = "30000"
	defaultBindAddress          = "0.0.0.0"
	defaultTimezoneLocationName = "Asia/Singapore"
)

func loadDotEnv() {
	if paths := discoverDotEnvPaths(); len(paths) > 0 {
		if err := godotenv.Load(paths...); err != nil {
			log.Printf("Failed to load .env files: %v", err)
		} else {
			log.Printf("Loaded environment from: %s", strings.Join(paths, ", "))
		}
	}
}

func loadRuntimeEnvironment() {
	loadDotEnv()
	locationName := applyTimezoneFromEnv()
	log.Printf("Using service timezone: %s", locationName)
}

func discoverDotEnvPaths() []string {
	candidates := make([]string, 0, 2)
	seen := map[string]struct{}{}
	addIfExists := func(path string) {
		if path == "" {
			return
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return
		}
		if _, ok := seen[absPath]; ok {
			return
		}
		if info, err := os.Stat(absPath); err == nil && !info.IsDir() {
			seen[absPath] = struct{}{}
			candidates = append(candidates, absPath)
		}
	}

	if executablePath, err := os.Executable(); err == nil {
		addIfExists(filepath.Join(filepath.Dir(executablePath), ".env"))
	}
	if cwd, err := os.Getwd(); err == nil {
		addIfExists(filepath.Join(cwd, ".env"))
	}

	return candidates
}

func applyTimezoneFromEnv() string {
	rawTZ := strings.TrimSpace(os.Getenv("TZ"))
	if rawTZ == "" {
		rawTZ = defaultTimezoneLocationName
	}

	loc, canonicalName, err := resolveTimezone(rawTZ)
	if err != nil {
		log.Printf("Invalid TZ=%q (%v), fallback to %s", rawTZ, err, defaultTimezoneLocationName)
		fallbackLoc, fallbackErr := time.LoadLocation(defaultTimezoneLocationName)
		if fallbackErr != nil {
			log.Printf("Failed to load fallback timezone %s: %v", defaultTimezoneLocationName, fallbackErr)
			fallbackLoc = time.FixedZone(defaultTimezoneLocationName, 8*3600)
		}
		canonicalName = defaultTimezoneLocationName
		loc = fallbackLoc
	}

	time.Local = loc
	if err := os.Setenv("TZ", canonicalName); err != nil {
		log.Printf("Failed to apply canonical TZ=%s: %v", canonicalName, err)
	}

	return canonicalName
}

func resolveTimezone(raw string) (*time.Location, string, error) {
	tz := strings.TrimSpace(raw)
	if tz == "" {
		return nil, "", fmt.Errorf("empty timezone")
	}

	upper := strings.ToUpper(tz)
	if upper == "LOCAL" || strings.HasPrefix(upper, "UTC+") || strings.HasPrefix(upper, "UTC-") ||
		strings.HasPrefix(upper, "GMT+") || strings.HasPrefix(upper, "GMT-") {
		return nil, "", fmt.Errorf("timezone must use a standard IANA name")
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, "", err
	}
	return loc, tz, nil
}

func main() {
	if isHealthcheck(os.Args[1:]) {
		loadDotEnv()
		if err := probeHealth(configuredBindAddress(), configuredPort()); err != nil {
			log.Printf("Health check failed: %v", err)
			os.Exit(1)
		}
		return
	}
	loadRuntimeEnvironment()

	bindAddress := configuredBindAddress()

	// Get config directory from environment or use default
	configDir := os.Getenv("CONFIG_DIR")
	if configDir == "" {
		configDir = defaultConfigDir
	}
	terminal := agreement.Terminal{
		In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr, IsTTY: agreement.IsTTY(os.Stdin),
	}
	if err := agreement.Require(configDir, os.Getenv(agreement.EnvKey), terminal, ensureSecureConfigDir); err != nil {
		if !errors.Is(err, agreement.ErrNotAccepted) {
			log.Printf("User agreement error: %v", err)
		}
		os.Exit(1)
	}

	// Create config directory if not exists
	if err := ensureSecureConfigDir(configDir); err != nil {
		log.Fatalf("Failed to secure config directory permissions: %v", err)
	}

	// Initialize database
	db, err := appdb.Open(configDir)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()
	leaseAcquireCtx, cancelLeaseAcquire := context.WithTimeout(context.Background(), 15*time.Second)
	instanceLease, err := appdb.AcquireInstanceLease(leaseAcquireCtx, db)
	cancelLeaseAcquire()
	if err != nil {
		log.Fatalf("Failed to acquire the single-instance database lease: %v", err)
	}
	leaseMonitorCtx, cancelLeaseMonitor := context.WithCancel(context.Background())
	leaseErrors := instanceLease.Monitor(leaseMonitorCtx)

	// Initialize database schema
	if err := models.InitDB(db); err != nil {
		fatalWithInstanceLease(instanceLease, cancelLeaseMonitor, db, "Failed to initialize database: %v", err)
	}
	if err := models.ReconcileAdminPassword(db, os.Getenv("ADMIN_PASSWORD")); err != nil {
		fatalWithInstanceLease(instanceLease, cancelLeaseMonitor, db, "Failed to initialize administrator authentication: %v", err)
	}

	// Initialize sing-box service
	kernelPath, err := kernel.Prepare(configDir)
	if err != nil {
		fatalWithInstanceLease(instanceLease, cancelLeaseMonitor, db, "Failed to prepare bundled sing-box: %v", err)
	}
	singBoxService := services.NewSingBoxService(configDir, services.WithBinaryPath(kernelPath))

	// Initialize Gin. The whole HTTP stack (routes, middleware, frontend
	// assets) is built before the sing-box child process is launched, so any
	// fatal error in this section cannot leave an orphaned proxy behind.
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	trustedProxies := parseCommaListEnv("TRUSTED_PROXIES")
	if err := r.SetTrustedProxies(trustedProxies); err != nil {
		fatalWithInstanceLease(instanceLease, cancelLeaseMonitor, db, "Invalid TRUSTED_PROXIES: %v", err)
	}
	r.Use(apiSecurityHeadersMiddleware())
	r.Use(apiRequestBodyLimitMiddleware(int64(readIntEnv("API_MAX_BODY_BYTES", 1<<20))))
	r.Use(apiCorsMiddlewareFromEnv())

	// Initialize handler
	handler := api.NewHandler(db, singBoxService)
	observability := services.NewObservability(db, singBoxService)
	panelOutput := log.Writer()
	log.SetOutput(io.MultiWriter(panelOutput, observability.Writer("panel")))
	if err := observability.Restore(context.Background()); err != nil {
		log.Printf("Failed to activate saved log storage: %v", err)
	}

	apiGroup := r.Group("/api")

	// Public routes
	registerPublicRoutes(apiGroup, handler, version.NewUpdateCheckerFromEnv())

	// Protected routes
	authorized := apiGroup.Group("")
	authorized.Use(handler.AuthMiddleware())
	handler.RegisterObservabilityRoutes(authorized, observability)
	{
		// Node management
		authorized.GET("/nodes", handler.GetNodes)
		authorized.GET("/node-groups", handler.GetGroups)
		authorized.GET("/node-groups/:id", handler.GetGroup)
		authorized.POST("/node-groups", handler.CreateGroup)
		authorized.PATCH("/node-groups/:id", handler.UpdateGroup)
		authorized.DELETE("/node-groups/:id", handler.DeleteGroup)
		authorized.POST("/node-groups/reorder", handler.ReorderGroups)
		authorized.POST("/node-groups/:id/subscription/refresh", handler.RefreshSubscription)
		authorized.POST("/node-groups/:id/upstream/check-ip", handler.CheckGroupUpstreamIP)
		authorized.POST("/nodes/batch-move", handler.BatchMoveNodes)
		authorized.GET("/nodes/:id", handler.GetNode)
		authorized.POST("/nodes", handler.CreateNode)
		authorized.POST("/nodes/batch-import", handler.BatchImportNodes)
		authorized.POST("/nodes/batch-delete", handler.BatchDeleteNodes)
		authorized.POST("/nodes/batch-export", handler.BatchExportNodes)
		authorized.PUT("/nodes/:id", handler.UpdateNode)
		authorized.PATCH("/nodes/:id", handler.UpdateNodeFlags)
		authorized.PUT("/nodes/:id/upstream", handler.UpdateNodeUpstream)
		authorized.PUT("/nodes/:id/remark", handler.UpdateNodeRemark)
		authorized.PUT("/nodes/:id/port-pin", handler.SetNodeInboundPortPinned)
		authorized.PUT("/nodes/:id/replace", handler.ReplaceNode)
		authorized.DELETE("/nodes/:id", handler.DeleteNode)
		authorized.GET("/nodes/:id/export", handler.ExportNode)
		authorized.POST("/nodes/reorder", handler.ReorderNodes)
		registerNodeIPCheckRoute(authorized, handler)
		authorized.POST("/nodes/batch-auth", handler.BatchSetAuth)

		// Share link parsing
		authorized.POST("/parse-link", handler.ParseShareLink)

		// Settings
		authorized.GET("/settings", handler.GetSettings)
		authorized.PUT("/settings", handler.UpdateSettings)
		authorized.POST("/settings/upstream/check-ip", handler.CheckGlobalUpstreamIP)
		authorized.GET("/runtime/status", handler.GetRuntimeStatus)
		authorized.POST("/runtime/restart", handler.RestartRuntime)
		authorized.POST("/logout", handler.Logout)
	}

	// Serve frontend static files
	if err := registerFrontendRoutes(r, "./frontend/dist", version.Version()); err != nil {
		fatalWithInstanceLease(instanceLease, cancelLeaseMonitor, db, "Failed to register frontend routes: %v", err)
	}

	// Start sing-box only after the web stack is fully prepared.
	startSingBoxFromDatabase(db, singBoxService, configDir)

	workCtx, stopWork := context.WithCancel(leaseMonitorCtx)
	schedulerCtx, stopScheduler := context.WithCancel(workCtx)
	schedulerDone := handler.StartSubscriptionScheduler(schedulerCtx)
	listenAddress := net.JoinHostPort(bindAddress, configuredPort())
	log.Printf("Server starting on %s", listenAddress)
	srv := &http.Server{
		Addr:              listenAddress,
		Handler:           r,
		BaseContext:       func(net.Listener) context.Context { return workCtx },
		ReadHeaderTimeout: readDurationEnv("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       readDurationEnv("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      readDurationEnv("HTTP_WRITE_TIMEOUT", 2*time.Minute),
		IdleTimeout:       readDurationEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		MaxHeaderBytes:    readIntEnv("HTTP_MAX_HEADER_BYTES", 1<<20),
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(quit)

	exitCode := 0
	shutdownHTTP := true
	select {
	case err := <-serverErr:
		stopWork()
		shutdownHTTP = false
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Failed to start server: %v", err)
			exitCode = 1
		}
	case sig := <-quit:
		log.Printf("Received signal %s, shutting down gracefully", sig)
	case leaseErr, ok := <-leaseErrors:
		stopWork()
		if !ok {
			leaseErr = errors.New("database lease monitor stopped unexpectedly")
		}
		log.Printf("Lost single-instance database lease, shutting down: %v", leaseErr)
		exitCode = 1
	}
	stopScheduler()
	stopWork()
	if shutdownHTTP {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
		cancel()
	}
	<-schedulerDone

	// Stop the sing-box child after the HTTP server has drained: in-flight API
	// requests may restart sing-box, so stopping it first could resurrect it.
	if err := singBoxService.Stop(); err != nil {
		log.Printf("Failed to stop sing-box: %v", err)
	} else {
		log.Println("Sing-box stopped")
	}
	log.SetOutput(panelOutput)
	if err := observability.Close(); err != nil {
		log.Printf("Failed to flush log storage: %v", err)
		exitCode = 1
	}
	cancelLeaseMonitor()
	leaseReleaseCtx, cancelLeaseRelease := context.WithTimeout(context.Background(), 5*time.Second)
	if err := instanceLease.Release(leaseReleaseCtx); err != nil {
		log.Printf("Failed to release single-instance database lease: %v", err)
		exitCode = 1
	}
	cancelLeaseRelease()

	if exitCode != 0 {
		db.Close()
		os.Exit(exitCode)
	}
}

func registerNodeIPCheckRoute(routes *gin.RouterGroup, handler *api.Handler) {
	routes.POST("/nodes/:id/check-ip", handler.CheckNodeIP)
}

func registerPublicRoutes(routes *gin.RouterGroup, handler *api.Handler, updateChecker *version.UpdateChecker) {
	routes.GET("/health", handler.GetHealth)
	routes.GET("/version", func(c *gin.Context) {
		updateInfo, _ := updateChecker.Check(c.Request.Context())
		c.JSON(http.StatusOK, gin.H{"version": updateInfo.CurrentVersion, "update": updateInfo})
	})
	routes.POST("/login", handler.Login)
}

func fatalWithInstanceLease(
	lease *appdb.InstanceLease,
	cancelMonitor context.CancelFunc,
	db *sql.DB,
	format string,
	args ...interface{},
) {
	if cancelMonitor != nil {
		cancelMonitor()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if lease != nil {
		_ = lease.Release(ctx)
	}
	cancel()
	if db != nil {
		_ = db.Close()
	}
	log.Fatalf(format, args...)
}

func configuredBindAddress() string {
	address := strings.TrimSpace(os.Getenv("BIND_ADDRESS"))
	if address == "" {
		return defaultBindAddress
	}
	return strings.Trim(address, "[]")
}

func configuredPort() string {
	if port := os.Getenv("PORT"); port != "" {
		return port
	}
	return defaultPort
}

// 启动前按当前分组顺序收敛已关联的策略成员，再生成统一配置并启动内核。
// 数据库不可用时保留已有配置，管理界面仍可用于恢复。
func startSingBoxFromDatabase(db *sql.DB, singBoxService *services.SingBoxService, configDir string) {
	ctx := context.Background()
	nodes, err := api.ReconcileLinkedPolicyMembers(ctx, db)
	if err != nil {
		startPreservedConfigAfterDatabaseFailure(singBoxService, fmt.Errorf("failed to load or reconcile proxy nodes: %w", err), configDir)
		return
	}
	settings, err := models.LoadRuntimeSettings(ctx, db)
	if err != nil {
		startPreservedConfigAfterDatabaseFailure(singBoxService, fmt.Errorf("failed to load runtime settings: %w", err), configDir)
		return
	}

	runtimeApplier := services.NewRuntimeApplier(singBoxService)
	if err := runtimeApplier.Apply(nodes, settings); err != nil {
		applyErr := err
		if !singBoxService.RuntimeStatus().Running {
			if recoveryErr := singBoxService.StartPreservedConfig(); recoveryErr != nil {
				applyErr = fmt.Errorf("%v; preserved runtime recovery failed: %w", err, recoveryErr)
			}
		}
		log.Printf("Failed to apply database state to sing-box: %v", applyErr)
		singBoxService.MarkDegraded(applyErr)
		logSingBoxDependencyGuideIfNeeded(applyErr, configDir)
		return
	}
	log.Println("Sing-box started successfully")
}

func ensureSecureConfigDir(configDir string) error {
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return err
	}
	return os.Chmod(configDir, 0o700)
}

func startPreservedConfigAfterDatabaseFailure(
	singBoxService *services.SingBoxService,
	databaseErr error,
	configDir string,
) {
	log.Printf("Database load or reconciliation failed; preserving existing sing-box configuration: %v", databaseErr)
	startErr := singBoxService.StartPreservedConfig()
	if startErr != nil {
		log.Printf("Failed to start preserved sing-box configuration: %v", startErr)
		logSingBoxDependencyGuideIfNeeded(startErr, configDir)
		singBoxService.MarkDegraded(fmt.Errorf("%v; preserved config start failed: %w", databaseErr, startErr))
		return
	}
	singBoxService.MarkDegraded(databaseErr)
}

const (
	assetsCacheControlHeader = "public, max-age=31536000, immutable"
	indexCacheControlHeader  = "no-cache, max-age=0, must-revalidate"
)

func upsertIndexMetaTag(indexContent []byte, metaName string, metaTag string) []byte {
	metaMarker := []byte(fmt.Sprintf(`name="%s"`, metaName))
	updated := false

	if idx := bytes.Index(indexContent, metaMarker); idx >= 0 {
		start := bytes.LastIndex(indexContent[:idx], []byte("<meta"))
		if start >= 0 {
			endRel := bytes.IndexByte(indexContent[idx:], '>')
			if endRel >= 0 {
				end := idx + endRel + 1
				indexContent = append(indexContent[:start], append([]byte(metaTag), indexContent[end:]...)...)
				updated = true
			}
		}
	}

	if !updated && bytes.Contains(indexContent, []byte("</head>")) {
		indexContent = bytes.Replace(indexContent, []byte("</head>"), []byte(metaTag+"\n  </head>"), 1)
	}

	return indexContent
}

func registerFrontendRoutes(r *gin.Engine, frontendDistDir string, appVersion string) error {
	assetsFS, sourceName, err := frontendAssetFS(frontendDistDir)
	if err != nil {
		return err
	}

	indexContent, err := fs.ReadFile(assetsFS, "index.html")
	if err != nil {
		return fmt.Errorf("read index.html: %w", err)
	}
	rawIndexContent := bytes.Clone(indexContent)

	nodesVirtualThreshold := readIntEnv("SBPM_NODES_VIRTUAL_THRESHOLD", 120)
	if nodesVirtualThreshold < 0 {
		log.Printf("Invalid SBPM_NODES_VIRTUAL_THRESHOLD=%d, using default 120", nodesVirtualThreshold)
		nodesVirtualThreshold = 120
	}
	nodesVirtualThresholdMeta := fmt.Sprintf(
		`    <meta name="sbpm-nodes-virtual-threshold" content="%d" />`,
		nodesVirtualThreshold,
	)
	indexContent = upsertIndexMetaTag(indexContent, "sbpm-nodes-virtual-threshold", nodesVirtualThresholdMeta)

	batchCheckIPConcurrency := readIntEnv("SBPM_BATCH_CHECK_IP_CONCURRENCY", 3)
	if batchCheckIPConcurrency < 1 {
		log.Printf("Invalid SBPM_BATCH_CHECK_IP_CONCURRENCY=%d, using default 3", batchCheckIPConcurrency)
		batchCheckIPConcurrency = 3
	}
	batchCheckIPConcurrencyMeta := fmt.Sprintf(
		`    <meta name="sbpm-batch-check-ip-concurrency" content="%d" />`,
		batchCheckIPConcurrency,
	)
	indexContent = upsertIndexMetaTag(indexContent, "sbpm-batch-check-ip-concurrency", batchCheckIPConcurrencyMeta)

	indexFingerprint := calcContentFingerprint(indexContent)
	indexETag := fmt.Sprintf("\"sbpm-%s\"", indexFingerprint)
	indexContentType := mime.TypeByExtension(".html")
	if indexContentType == "" {
		indexContentType = http.DetectContentType(indexContent)
	}
	indexVariants, err := prepareIndexVariants(assetsFS, rawIndexContent, indexContent)
	if err != nil {
		return fmt.Errorf("prepare index compression: %w", err)
	}
	indexETags := map[string]string{"": indexETag}
	for encoding, content := range indexVariants {
		indexETags[encoding] = fmt.Sprintf("\"sbpm-%s-%s\"", calcContentFingerprint(content), encoding)
	}
	log.Printf("Serving frontend assets from %s", sourceName)

	serveIndex := func(c *gin.Context) {
		c.Header("Cache-Control", indexCacheControlHeader)
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Header("X-App-Version", appVersion)
		c.Header("X-Frontend-Fingerprint", indexFingerprint)
		representation := chooseStaticRepresentation(c.Writer, c.Request, indexContent, indexVariants)
		c.Header("ETag", indexETags[representation.encoding])

		serveStaticRepresentation(c.Writer, c.Request, "index.html", indexContentType, time.Time{}, representation)
		c.Abort()
	}

	withAssetsHeaders := func(c *gin.Context) {
		c.Header("Cache-Control", assetsCacheControlHeader)
		c.Header("X-App-Version", appVersion)
		c.Header("X-Frontend-Fingerprint", indexFingerprint)
		c.Next()
	}

	assetsHandler := func(c *gin.Context) {
		fileName := strings.TrimPrefix(c.Param("filepath"), "/")
		if !validStaticPath(fileName) || servePrecompressedStaticFile(c.Writer, c.Request, assetsFS, "assets/"+fileName) != nil {
			c.Writer.Header().Del("Cache-Control")
			if c.Request.Method == http.MethodHead {
				c.Header("Content-Type", "text/plain; charset=utf-8")
				c.Status(http.StatusNotFound)
			} else {
				http.NotFound(c.Writer, c.Request)
			}
		}
		c.Abort()
	}
	r.GET("/assets/*filepath", withAssetsHeaders, assetsHandler)
	r.HEAD("/assets/*filepath", withAssetsHeaders, assetsHandler)

	serveStaticFile := func(route string, fileName string) {
		handler := func(c *gin.Context) {
			c.Header("Cache-Control", assetsCacheControlHeader)
			c.Header("X-App-Version", appVersion)
			c.Header("X-Frontend-Fingerprint", indexFingerprint)
			if err := servePrecompressedStaticFile(c.Writer, c.Request, assetsFS, fileName); err != nil {
				c.Writer.Header().Del("Cache-Control")
				if c.Request.Method == http.MethodHead {
					c.Header("Content-Type", "application/json; charset=utf-8")
					c.Status(http.StatusNotFound)
				} else {
					c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				}
			}
		}
		r.GET(route, handler)
		r.HEAD(route, handler)
	}

	serveStaticFile("/logo.svg", "logo.svg")
	serveStaticFile("/brand-mark.svg", "brand-mark.svg")
	r.GET("/favicon.ico", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/logo.svg")
	})
	r.HEAD("/favicon.ico", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/logo.svg")
	})

	r.GET("/", serveIndex)
	r.HEAD("/", serveIndex)
	r.NoRoute(func(c *gin.Context) {
		if c.Request.URL.Path == "/api" || strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		serveIndex(c)
	})

	return nil
}

func frontendAssetFS(frontendDistDir string) (fs.FS, string, error) {
	if !kernel.Bundled {
		if info, err := os.Stat(frontendDistDir); err == nil && info.IsDir() {
			return os.DirFS(frontendDistDir), frontendDistDir, nil
		}
	}

	if !frontendassets.HasEmbeddedAssets {
		return nil, "", fmt.Errorf("frontend dist directory %q not found and embedded assets are disabled", frontendDistDir)
	}

	embeddedDist, err := fs.Sub(frontendassets.DistFS, "dist")
	if err != nil {
		return nil, "", fmt.Errorf("frontend dist directory %q not found and embedded assets unavailable: %w", frontendDistDir, err)
	}
	return embeddedDist, "embedded:frontend/dist", nil
}

func calcContentFingerprint(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:8])
}

func logSingBoxDependencyGuideIfNeeded(err error, configDir string) {
	if !errors.Is(err, services.ErrSingBoxBinaryNotFound) && !errors.Is(err, services.ErrSingBoxBinaryNotExecutable) {
		return
	}

	log.Printf("Binary deploy guide: ensure sing-box is installed for your OS/arch and executable.")
	log.Printf("Binary deploy guide: set SINGBOX_BINARY=/absolute/path/to/sing-box in .env (recommended).")
	log.Printf("Binary deploy guide: fallback search order is PATH -> %s/sing-box -> executable directory.", configDir)
}

func apiCorsMiddlewareFromEnv() gin.HandlerFunc {
	allowed := parseCommaListEnv("CORS_ALLOWED_ORIGINS")
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		if o == "*" {
			log.Printf("CORS_ALLOWED_ORIGINS contains '*', ignoring for safety")
			continue
		}
		allowedSet[o] = struct{}{}
	}

	return func(c *gin.Context) {
		if !isAPIRequest(c.Request.URL.Path) {
			c.Next()
			return
		}

		origin := c.GetHeader("Origin")
		if origin != "" {
			if _, ok := allowedSet[origin]; ok {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
				c.Writer.Header().Add("Vary", "Origin")
				c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			}
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

func apiSecurityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Frame-Options", "DENY")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Header("X-XSS-Protection", "0")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Cross-Origin-Opener-Policy", "same-origin")
		c.Header("Cross-Origin-Resource-Policy", "same-origin")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")

		if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}

		c.Next()
	}
}

func apiRequestBodyLimitMiddleware(maxBytes int64) gin.HandlerFunc {
	if maxBytes <= 0 {
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		if isAPIRequest(c.Request.URL.Path) {
			if c.Request.ContentLength > maxBytes {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
				return
			}
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

func isAPIRequest(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

func parseCommaListEnv(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, p)
	}
	return out
}

func readDurationEnv(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("Invalid %s=%q, using default %s", key, raw, def)
		return def
	}
	return d
}

func readIntEnv(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("Invalid %s=%q, using default %d", key, raw, def)
		return def
	}
	return n
}
