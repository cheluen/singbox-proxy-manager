package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sb-proxy/backend/api"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
	"sb-proxy/internal/version"

	"github.com/gin-gonic/gin"
	"github.com/tursodatabase/libsql-client-go/libsql"
	sqlite "modernc.org/sqlite"
)

func openDatabase(configDir string) (*sql.DB, error) {
	tursoURL := os.Getenv("TURSO_DATABASE_URL")
	tursoToken := os.Getenv("TURSO_AUTH_TOKEN")
	if tursoURL != "" && tursoToken != "" {
		connector, err := libsql.NewConnector(tursoURL, libsql.WithAuthToken(tursoToken))
		if err != nil {
			return nil, err
		}

		db := sql.OpenDB(connector)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, err
		}

		log.Printf("Using Turso database: %s", tursoURL)
		return db, nil
	}
	if tursoURL != "" || tursoToken != "" {
		log.Printf("Turso config incomplete (need both TURSO_DATABASE_URL and TURSO_AUTH_TOKEN), falling back to local sqlite")
	}

	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON", nil); err != nil {
			return fmt.Errorf("sqlite init failed (foreign_keys): %w", err)
		}
		if _, err := conn.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000", nil); err != nil {
			return fmt.Errorf("sqlite init failed (busy_timeout): %w", err)
		}
		if _, err := conn.ExecContext(context.Background(), "PRAGMA journal_mode = WAL", nil); err != nil {
			return fmt.Errorf("sqlite init failed (journal_mode): %w", err)
		}
		return nil
	})

	dbPath := filepath.Join(configDir, "proxy.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	log.Printf("Using local sqlite database: %s", dbPath)
	return db, nil
}

func main() {
	// Get config directory from environment or use default
	configDir := os.Getenv("CONFIG_DIR")
	if configDir == "" {
		configDir = "./config"
	}

	// Create config directory if not exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		log.Fatalf("Failed to create config directory: %v", err)
	}

	// Initialize database
	db, err := openDatabase(configDir)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Initialize database schema
	if err := models.InitDB(db); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize sing-box service
	singBoxService := services.NewSingBoxService(configDir)

	// Generate global config for all nodes and start sing-box
	rows, err := db.Query(`
		SELECT id, name, remark, type, config, inbound_port, username, password, 
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
	if err != nil {
		log.Printf("Failed to query proxy nodes: %v", err)
	}

	var nodes []models.ProxyNode
	if rows != nil {
		for rows.Next() {
			var node models.ProxyNode
			if err := rows.Scan(
				&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
				&node.Username, &node.Password, &node.SortOrder, &node.NodeIP, &node.Location,
				&node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
			); err != nil {
				log.Printf("Failed to scan proxy node: %v", err)
				continue
			}
			nodes = append(nodes, node)
		}
		rows.Close()
	}

	if err := singBoxService.GenerateGlobalConfig(nodes); err != nil {
		log.Printf("Failed to generate global config: %v", err)
	} else {
		if err := singBoxService.Start(); err != nil {
			log.Printf("Failed to start sing-box: %v", err)
		} else {
			log.Println("Sing-box started successfully")
		}
	}

	// Initialize Gin
	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	r.Use(apiCorsMiddlewareFromEnv())

	// Initialize handler
	handler := api.NewHandler(db, singBoxService)

	apiGroup := r.Group("/api")

	// Public routes
	apiGroup.GET("/version", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"version": version.Version()})
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
		authorized.PUT("/nodes/:id/replace", handler.ReplaceNode)
		authorized.DELETE("/nodes/:id", handler.DeleteNode)
		authorized.GET("/nodes/:id/export", handler.ExportNode)
		authorized.POST("/nodes/reorder", handler.ReorderNodes)
		authorized.GET("/nodes/:id/check-ip", handler.CheckNodeIP)
		authorized.POST("/nodes/batch-auth", handler.BatchSetAuth)

		// Share link parsing
		authorized.POST("/parse-link", handler.ParseShareLink)

		// Settings
		authorized.GET("/settings", handler.GetSettings)
		authorized.PUT("/settings", handler.UpdateSettings)
	}

	// Serve frontend static files
	r.Static("/assets", "./frontend/dist/assets")
	r.StaticFile("/", "./frontend/dist/index.html")
	r.NoRoute(func(c *gin.Context) {
		c.File("./frontend/dist/index.html")
	})

	// Get port from environment or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = "30000"
	}

	log.Printf("Server starting on port %s", port)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           r,
		ReadHeaderTimeout: readDurationEnv("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       readDurationEnv("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      readDurationEnv("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       readDurationEnv("HTTP_IDLE_TIMEOUT", 60*time.Second),
		MaxHeaderBytes:    readIntEnv("HTTP_MAX_HEADER_BYTES", 1<<20),
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Failed to start server: %v", err)
	}
}

func apiCorsMiddlewareFromEnv() gin.HandlerFunc {
	allowed := parseCommaListEnv("CORS_ALLOWED_ORIGINS")
	if len(allowed) == 0 {
		return func(c *gin.Context) {
			if strings.HasPrefix(c.Request.URL.Path, "/api") && c.Request.Method == http.MethodOptions {
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
		if !strings.HasPrefix(c.Request.URL.Path, "/api") {
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
