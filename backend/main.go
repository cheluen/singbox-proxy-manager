package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/textproto"
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
	"sb-proxy/internal/version"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
)

const (
	defaultConfigDir            = "./config"
	defaultPort                 = "30000"
	defaultBindAddress          = "0.0.0.0"
	defaultTZ                   = "UTC+8"
	defaultTimezoneLocationName = "Asia/Shanghai"
)

func openDatabase(configDir string) (*sql.DB, error) {
	return appdb.Open(configDir)
}

func loadRuntimeEnvironment() {
	loadedPaths, err := loadDotEnvFiles(discoverDotEnvPaths())
	if err != nil {
		log.Printf("Failed to load .env files: %v", err)
	} else if len(loadedPaths) > 0 {
		log.Printf("Loaded environment from: %s", strings.Join(loadedPaths, ", "))
	}

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

func loadDotEnvFiles(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}

	if err := godotenv.Load(paths...); err != nil {
		return nil, err
	}

	return paths, nil
}

func applyTimezoneFromEnv() string {
	rawTZ := strings.TrimSpace(os.Getenv("TZ"))
	if rawTZ == "" {
		rawTZ = defaultTZ
		if err := os.Setenv("TZ", rawTZ); err != nil {
			log.Printf("Failed to set default TZ=%s: %v", rawTZ, err)
		}
	}

	loc, canonicalName, err := resolveTimezone(rawTZ)
	if err != nil {
		log.Printf("Invalid TZ=%q (%v), fallback to %s", rawTZ, err, defaultTimezoneLocationName)
		fallbackLoc, fallbackErr := time.LoadLocation(defaultTimezoneLocationName)
		if fallbackErr != nil {
			log.Printf("Failed to load fallback timezone %s: %v", defaultTimezoneLocationName, fallbackErr)
			fallbackLoc = time.FixedZone("UTC+08:00", 8*3600)
			canonicalName = "UTC+08:00"
		} else {
			canonicalName = defaultTimezoneLocationName
		}
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
	switch upper {
	case "UTC+8", "UTC+08", "UTC+08:00", "GMT+8", "GMT+08", "GMT+08:00":
		loc, err := time.LoadLocation(defaultTimezoneLocationName)
		if err != nil {
			return nil, "", err
		}
		return loc, defaultTimezoneLocationName, nil
	}

	if strings.HasPrefix(upper, "UTC") || strings.HasPrefix(upper, "GMT") {
		if loc, canonicalName, ok := parseUTCOffsetLocation(upper); ok {
			return loc, canonicalName, nil
		}
	}

	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, "", err
	}
	return loc, tz, nil
}

func parseUTCOffsetLocation(raw string) (*time.Location, string, bool) {
	offset := strings.TrimPrefix(strings.TrimPrefix(raw, "UTC"), "GMT")
	if offset == "" {
		return time.UTC, "UTC", true
	}
	if len(offset) < 2 {
		return nil, "", false
	}

	sign := offset[0]
	if sign != '+' && sign != '-' {
		return nil, "", false
	}
	number := offset[1:]
	parts := strings.Split(number, ":")
	if len(parts) > 2 {
		return nil, "", false
	}

	hourPart := parts[0]
	minutePart := "0"
	if len(parts) == 2 {
		minutePart = parts[1]
	} else if len(number) == 4 {
		hourPart = number[:2]
		minutePart = number[2:]
	}

	hours, err := strconv.Atoi(hourPart)
	if err != nil || hours < 0 || hours > 14 {
		return nil, "", false
	}
	minutes, err := strconv.Atoi(minutePart)
	if err != nil || minutes < 0 || minutes >= 60 {
		return nil, "", false
	}

	totalSeconds := hours*3600 + minutes*60
	if sign == '-' {
		totalSeconds = -totalSeconds
	}

	canonical := fmt.Sprintf("UTC%c%02d:%02d", sign, hours, minutes)
	return time.FixedZone(canonical, totalSeconds), canonical, true
}

func main() {
	loadRuntimeEnvironment()

	bindAddress := configuredBindAddress()
	if err := validateAdminPasswordForBindAddress(bindAddress); err != nil {
		log.Fatalf("Unsafe management listener configuration: %v", err)
	}

	// Get config directory from environment or use default
	configDir := os.Getenv("CONFIG_DIR")
	if configDir == "" {
		configDir = defaultConfigDir
	}

	// Create config directory if not exists
	if err := ensureSecureConfigDir(configDir); err != nil {
		log.Fatalf("Failed to secure config directory permissions: %v", err)
	}

	// Initialize database
	db, err := openDatabase(configDir)
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

	// Initialize sing-box service
	singBoxService := services.NewSingBoxService(configDir)

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

	apiGroup := r.Group("/api")
	updateChecker := version.NewUpdateCheckerFromEnv()

	// Public routes
	apiGroup.GET("/version", func(c *gin.Context) {
		currentVersion := version.Version()
		response := gin.H{"version": currentVersion}
		if updateInfo, err := updateChecker.Check(c.Request.Context(), currentVersion); err == nil {
			response["update"] = updateInfo
		} else {
			response["update"] = version.UpdateInfo{
				CurrentVersion: currentVersion,
				Available:      false,
				Error:          err.Error(),
			}
		}
		c.JSON(http.StatusOK, response)
	})
	apiGroup.GET("/auth/status", handler.AuthStatus)
	apiGroup.POST("/setup/admin-password", handler.SetupAdminPassword)
	apiGroup.POST("/login", handler.Login)

	// Protected routes
	authorized := apiGroup.Group("")
	authorized.Use(handler.AuthMiddleware())
	{
		// Node management
		authorized.GET("/nodes", handler.GetNodes)
		authorized.GET("/nodes/:id", handler.GetNode)
		authorized.POST("/nodes", handler.CreateNode)
		authorized.POST("/nodes/batch-import", handler.BatchImportNodes)
		authorized.POST("/nodes/batch-delete", handler.BatchDeleteNodes)
		authorized.POST("/nodes/batch-export", handler.BatchExportNodes)
		authorized.PUT("/nodes/:id", handler.UpdateNode)
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

	// Get port from environment or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	listenAddress := net.JoinHostPort(bindAddress, port)
	log.Printf("Server starting on %s", listenAddress)
	srv := &http.Server{
		Addr:              listenAddress,
		Handler:           r,
		ReadHeaderTimeout: readDurationEnv("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       readDurationEnv("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      readDurationEnv("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       readDurationEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		MaxHeaderBytes:    readIntEnv("HTTP_MAX_HEADER_BYTES", 1<<20),
	}

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)

	exitCode := 0
	select {
	case err := <-serverErr:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("Failed to start server: %v", err)
			exitCode = 1
		}
	case sig := <-quit:
		log.Printf("Received signal %s, shutting down gracefully", sig)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
		cancel()
	case leaseErr, ok := <-leaseErrors:
		if !ok {
			leaseErr = errors.New("database lease monitor stopped unexpectedly")
		}
		log.Printf("Lost single-instance database lease, shutting down: %v", leaseErr)
		exitCode = 1
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
		cancel()
	}

	// Stop the sing-box child after the HTTP server has drained: in-flight API
	// requests may restart sing-box, so stopping it first could resurrect it.
	if err := singBoxService.Stop(); err != nil {
		log.Printf("Failed to stop sing-box: %v", err)
	} else {
		log.Println("Sing-box stopped")
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

func validateAdminPasswordForBindAddress(bindAddress string) error {
	if strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")) != "" {
		return nil
	}

	normalized := strings.TrimSpace(strings.Trim(bindAddress, "[]"))
	if strings.EqualFold(normalized, "localhost") {
		return nil
	}
	if ip := net.ParseIP(normalized); ip != nil && ip.IsLoopback() {
		return nil
	}

	return fmt.Errorf(
		"ADMIN_PASSWORD must be set when BIND_ADDRESS=%q is not an explicit loopback address",
		bindAddress,
	)
}

// startSingBoxFromDatabase generates the unified config from the current
// database contents and launches the sing-box child process. Failures are
// logged but not fatal so the management UI stays reachable for recovery.
func startSingBoxFromDatabase(db *sql.DB, singBoxService *services.SingBoxService, configDir string) {
	rows, err := db.Query(`
		SELECT id, name, remark, type, config, inbound_port, inbound_port_pinned, username, password, tcp_reuse_enabled,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
	if err != nil {
		startPreservedConfigAfterDatabaseFailure(singBoxService, fmt.Errorf("failed to query proxy nodes: %w", err), configDir)
		return
	}
	defer rows.Close()

	var nodes []models.ProxyNode
	for rows.Next() {
		var node models.ProxyNode
		if err := rows.Scan(
			&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
			&node.InboundPortPinned, &node.Username, &node.Password, &node.TCPReuseEnabled,
			&node.SortOrder, &node.NodeIP, &node.Location, &node.CountryCode, &node.Latency,
			&node.Enabled, &node.CreatedAt, &node.UpdatedAt,
		); err != nil {
			startPreservedConfigAfterDatabaseFailure(singBoxService, fmt.Errorf("failed to scan proxy node: %w", err), configDir)
			return
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		startPreservedConfigAfterDatabaseFailure(singBoxService, fmt.Errorf("failed while reading proxy nodes: %w", err), configDir)
		return
	}

	runtimeApplier := services.NewRuntimeApplier(singBoxService)
	if err := runtimeApplier.Apply(nodes); err != nil {
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
	log.Printf("Database read failed; preserving existing sing-box configuration: %v", databaseErr)
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

	nodesVirtualThreshold := readIntEnv("SBPM_NODES_VIRTUAL_THRESHOLD", 50)
	if nodesVirtualThreshold < 0 {
		log.Printf("Invalid SBPM_NODES_VIRTUAL_THRESHOLD=%d, using default 50", nodesVirtualThreshold)
		nodesVirtualThreshold = 50
	}
	nodesVirtualThresholdMeta := fmt.Sprintf(
		`    <meta name="sbpm-nodes-virtual-threshold" content="%d" />`,
		nodesVirtualThreshold,
	)
	indexContent = upsertIndexMetaTag(indexContent, "sbpm-nodes-virtual-threshold", nodesVirtualThresholdMeta)

	batchCheckIPConcurrency := readIntEnv("SBPM_BATCH_CHECK_IP_CONCURRENCY", 10)
	if batchCheckIPConcurrency < 1 {
		log.Printf("Invalid SBPM_BATCH_CHECK_IP_CONCURRENCY=%d, using default 10", batchCheckIPConcurrency)
		batchCheckIPConcurrency = 10
	}
	batchCheckIPConcurrencyMeta := fmt.Sprintf(
		`    <meta name="sbpm-batch-check-ip-concurrency" content="%d" />`,
		batchCheckIPConcurrency,
	)
	indexContent = upsertIndexMetaTag(indexContent, "sbpm-batch-check-ip-concurrency", batchCheckIPConcurrencyMeta)

	indexFingerprint := calcContentFingerprint(indexContent)
	indexETag := fmt.Sprintf("\"sbpm-%s\"", indexFingerprint)
	log.Printf("Serving frontend assets from %s", sourceName)

	serveIndex := func(c *gin.Context) {
		c.Header("Cache-Control", indexCacheControlHeader)
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Header("ETag", indexETag)
		c.Header("X-App-Version", appVersion)
		c.Header("X-Frontend-Fingerprint", indexFingerprint)

		if ifNoneMatchMatchesCurrentETag(c.GetHeader("If-None-Match"), indexETag) {
			c.Status(http.StatusNotModified)
			c.Abort()
			return
		}

		http.ServeContent(c.Writer, c.Request, "index.html", time.Time{}, bytes.NewReader(indexContent))
		c.Abort()
	}

	assetsDirFS, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("read assets dir: %w", err)
	}
	assetsFileServer := http.StripPrefix("/assets/", http.FileServer(http.FS(assetsDirFS)))
	withAssetsHeaders := func(c *gin.Context) {
		c.Header("Cache-Control", assetsCacheControlHeader)
		c.Header("X-App-Version", appVersion)
		c.Header("X-Frontend-Fingerprint", indexFingerprint)
		c.Next()
	}

	assetsHandler := gin.WrapH(assetsFileServer)
	r.GET("/assets/*filepath", withAssetsHeaders, assetsHandler)
	r.HEAD("/assets/*filepath", withAssetsHeaders, assetsHandler)

	serveStaticFile := func(route string, fileName string) {
		handler := func(c *gin.Context) {
			content, readErr := fs.ReadFile(assetsFS, fileName)
			if readErr != nil {
				c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
				return
			}

			contentType := mime.TypeByExtension(filepath.Ext(fileName))
			if contentType == "" {
				contentType = "application/octet-stream"
			}

			c.Header("Cache-Control", assetsCacheControlHeader)
			c.Header("X-App-Version", appVersion)
			c.Header("X-Frontend-Fingerprint", indexFingerprint)
			c.Data(http.StatusOK, contentType, content)
		}
		r.GET(route, handler)
		r.HEAD(route, handler)
	}

	serveStaticFile("/logo.svg", "logo.svg")
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
	if info, err := os.Stat(frontendDistDir); err == nil && info.IsDir() {
		return os.DirFS(frontendDistDir), frontendDistDir, nil
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

func ifNoneMatchMatchesCurrentETag(ifNoneMatch string, currentETag string) bool {
	if ifNoneMatch == "" || currentETag == "" {
		return false
	}

	buf := ifNoneMatch
	for {
		buf = textproto.TrimString(buf)
		if len(buf) == 0 {
			break
		}
		if buf[0] == ',' {
			buf = buf[1:]
			continue
		}
		if buf[0] == '*' {
			rest := textproto.TrimString(buf[1:])
			if rest == "" || rest[0] == ',' {
				return true
			}
		}

		etag, remain := scanETagToken(buf)
		if etag == "" {
			buf = skipToNextETagToken(buf)
			continue
		}
		if etagWeakMatch(etag, currentETag) {
			return true
		}
		buf = remain
	}

	return false
}

func scanETagToken(s string) (etag string, remain string) {
	s = textproto.TrimString(s)
	start := 0
	if strings.HasPrefix(s, "W/") {
		start = 2
	}
	if len(s[start:]) < 2 || s[start] != '"' {
		return "", ""
	}

	for i := start + 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 0x21 || c >= 0x23 && c <= 0x7E || c >= 0x80:
		case c == '"':
			return s[:i+1], s[i+1:]
		default:
			return "", ""
		}
	}
	return "", ""
}

func etagWeakMatch(a string, b string) bool {
	return strings.TrimPrefix(a, "W/") == strings.TrimPrefix(b, "W/")
}

func skipToNextETagToken(s string) string {
	if idx := strings.IndexByte(s, ','); idx >= 0 {
		return s[idx+1:]
	}
	return ""
}

func apiCorsMiddlewareFromEnv() gin.HandlerFunc {
	allowed := parseCommaListEnv("CORS_ALLOWED_ORIGINS")
	if len(allowed) == 0 {
		return func(c *gin.Context) {
			if isAPIRequest(c.Request.URL.Path) && c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusNoContent)
				return
			}
			c.Next()
		}
	}

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
				c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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
