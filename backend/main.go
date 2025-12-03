package main

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"

	"sb-proxy/backend/api"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

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
	dbPath := filepath.Join(configDir, "proxy.db")
	db, err := sql.Open("sqlite", dbPath)
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
		SELECT id, name, type, config, inbound_port, username, password, 
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
				&node.ID, &node.Name, &node.Type, &node.Config, &node.InboundPort,
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

	// CORS middleware
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// Initialize handler
	handler := api.NewHandler(db, singBoxService)

	// Public routes
	r.POST("/api/login", handler.Login)

	// Protected routes
	authorized := r.Group("/api")
	authorized.Use(handler.AuthMiddleware())
	{
		// Node management
		authorized.GET("/nodes", handler.GetNodes)
		authorized.GET("/nodes/:id", handler.GetNode)
		authorized.POST("/nodes", handler.CreateNode)
		authorized.POST("/nodes/batch-import", handler.BatchImportNodes)
		authorized.POST("/nodes/batch-delete", handler.BatchDeleteNodes)
		authorized.PUT("/nodes/:id", handler.UpdateNode)
		authorized.DELETE("/nodes/:id", handler.DeleteNode)
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
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
