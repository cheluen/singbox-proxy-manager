package api

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

type Handler struct {
	db             *sql.DB
	singBoxService *services.SingBoxService
	sessionToken   string
	sessionExpiry  time.Time
	sessionMu      sync.RWMutex
	checkProxyIP   func(proxyAddr string, username string, password string) (*services.IPInfo, error)
}

func NewHandler(db *sql.DB, singBoxService *services.SingBoxService) *Handler {
	return &Handler{
		db:             db,
		singBoxService: singBoxService,
		checkProxyIP:   services.CheckProxyIP,
	}
}

// regenerateAndRestart is a helper function to regenerate sing-box config and restart the service
func (h *Handler) regenerateAndRestart() error {
	// Get all nodes from database
	rows, err := h.db.Query(`
		SELECT id, name, remark, type, config, inbound_port, username, password,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var nodes []models.ProxyNode
	for rows.Next() {
		var node models.ProxyNode
		err := rows.Scan(
			&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
			&node.Username, &node.Password, &node.SortOrder, &node.NodeIP, &node.Location,
			&node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
		)
		if err != nil {
			return err
		}
		nodes = append(nodes, node)
	}

	// Generate global config
	if err := h.singBoxService.GenerateGlobalConfig(nodes); err != nil {
		return err
	}

	// Restart sing-box
	return h.singBoxService.Restart()
}

// reorderRemainingNodes reorders all remaining nodes and reassigns ports to fill gaps
func (h *Handler) reorderRemainingNodes() error {
	// Get start port from settings
	var startPort int
	if err := h.db.QueryRow("SELECT start_port FROM settings LIMIT 1").Scan(&startPort); err != nil {
		return err
	}

	// Get all remaining nodes ordered by current sort_order
	rows, err := h.db.Query("SELECT id FROM proxy_nodes ORDER BY sort_order ASC")
	if err != nil {
		return err
	}
	defer rows.Close()

	var nodeIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return err
		}
		nodeIDs = append(nodeIDs, id)
	}

	// Begin transaction to update all nodes
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Update each node with new sort_order and inbound_port
	for newSortOrder, nodeID := range nodeIDs {
		newPort := startPort + newSortOrder
		_, err := tx.Exec(`
			UPDATE proxy_nodes
			SET sort_order = ?, inbound_port = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, newSortOrder, newPort, nodeID)

		if err != nil {
			return err
		}
	}

	// Commit transaction
	return tx.Commit()
}

// Auth middleware
func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := c.GetHeader("Authorization")
		if token == "" {
			token = c.Query("token")
		}

		h.sessionMu.RLock()
		validToken := h.sessionToken
		expiry := h.sessionExpiry
		h.sessionMu.RUnlock()

		if token != validToken || time.Now().After(expiry) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		c.Next()
	}
}

// Login handles admin login
func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	var settings models.Settings
	err := h.db.QueryRow("SELECT id, admin_password FROM settings LIMIT 1").Scan(&settings.ID, &settings.AdminPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	// Compare password using bcrypt only (no plaintext fallback for security)
	if err := bcrypt.CompareHashAndPassword([]byte(settings.AdminPassword), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
		return
	}

	// Generate cryptographically secure session token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate token"})
		return
	}

	h.sessionMu.Lock()
	h.sessionToken = base64.URLEncoding.EncodeToString(tokenBytes)
	h.sessionExpiry = time.Now().Add(24 * time.Hour)
	token := h.sessionToken
	expiry := h.sessionExpiry
	h.sessionMu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"token":  token,
		"expiry": expiry.Unix(),
	})
}

// GetNodes returns all proxy nodes
func (h *Handler) GetNodes(c *gin.Context) {
	rows, err := h.db.Query(`
		SELECT id, name, remark, type, config, inbound_port, username, password, 
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer rows.Close()

	var nodes []models.ProxyNode
	for rows.Next() {
		var node models.ProxyNode
		err := rows.Scan(
			&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
			&node.Username, &node.Password, &node.SortOrder, &node.NodeIP,
			&node.Location, &node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
		)
		if err != nil {
			continue
		}
		nodes = append(nodes, node)
	}

	c.JSON(http.StatusOK, nodes)
}

// GetNode returns a single proxy node
func (h *Handler) GetNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var node models.ProxyNode
	err = h.db.QueryRow(`
		SELECT id, name, remark, type, config, inbound_port, username, password,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(
		&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
		&node.Username, &node.Password, &node.SortOrder, &node.NodeIP,
		&node.Location, &node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		}
		return
	}

	c.JSON(http.StatusOK, node)
}

// CreateNode creates a new proxy node
func (h *Handler) CreateNode(c *gin.Context) {
	var req models.ProxyNode
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Validate config JSON
	if _, err := req.ParseConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config format"})
		return
	}

	// Get max sort order
	var maxOrder int
	h.db.QueryRow("SELECT COALESCE(MAX(sort_order), -1) FROM proxy_nodes").Scan(&maxOrder)
	req.SortOrder = maxOrder + 1

	// Handle inbound port
	if req.InboundPort == 0 {
		// Auto-assign based on first node's port
		var firstNodePort int
		err := h.db.QueryRow("SELECT inbound_port FROM proxy_nodes ORDER BY sort_order ASC LIMIT 1").Scan(&firstNodePort)

		if err == sql.ErrNoRows {
			// This is the first node, use start_port
			var startPort int
			h.db.QueryRow("SELECT start_port FROM settings LIMIT 1").Scan(&startPort)
			req.InboundPort = startPort
		} else if err == nil {
			// Calculate based on first node's port and sort order
			req.InboundPort = firstNodePort + req.SortOrder
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to determine port"})
			return
		}
	}

	// Insert node
	result, err := h.db.Exec(`
		INSERT INTO proxy_nodes (name, remark, type, config, inbound_port, username, password, sort_order, latency, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, req.Name, req.Remark, req.Type, req.Config, req.InboundPort, req.Username, req.Password, req.SortOrder, 0, req.Enabled)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create node"})
		return
	}

	id, _ := result.LastInsertId()
	req.ID = int(id)

	// Regenerate global config and restart sing-box
	if err := h.regenerateAndRestart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
		return
	}

	c.JSON(http.StatusCreated, req)
}

// BatchImportNodes imports multiple nodes from share links
func (h *Handler) BatchImportNodes(c *gin.Context) {
	var req struct {
		Links   []string `json:"links"`
		Enabled bool     `json:"enabled"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if len(req.Links) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no links provided"})
		return
	}

	results := []map[string]interface{}{}
	successCount := 0

	for _, link := range req.Links {
		result := map[string]interface{}{
			"link": link,
		}

		// Parse share link
		parsedConfig, proxyType, name, err := services.ParseShareLink(link)
		if err != nil {
			result["success"] = false
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}

		// Convert to JSON
		configJSON, err := json.Marshal(parsedConfig)
		if err != nil {
			result["success"] = false
			result["error"] = "failed to marshal config"
			results = append(results, result)
			continue
		}

		// Get max sort order
		var maxOrder int
		h.db.QueryRow("SELECT COALESCE(MAX(sort_order), -1) FROM proxy_nodes").Scan(&maxOrder)
		sortOrder := maxOrder + 1

		// Auto-assign inbound port
		var inboundPort int
		var firstNodePort int
		err = h.db.QueryRow("SELECT inbound_port FROM proxy_nodes ORDER BY sort_order ASC LIMIT 1").Scan(&firstNodePort)

		if err == sql.ErrNoRows {
			// This is the first node, use start_port
			var startPort int
			h.db.QueryRow("SELECT start_port FROM settings LIMIT 1").Scan(&startPort)
			inboundPort = startPort
		} else if err == nil {
			// Calculate based on first node's port and sort order
			inboundPort = firstNodePort + sortOrder
		} else {
			result["success"] = false
			result["error"] = "failed to determine port"
			results = append(results, result)
			continue
		}

		// Insert node
		dbResult, err := h.db.Exec(`
			INSERT INTO proxy_nodes (name, remark, type, config, inbound_port, username, password, sort_order, latency, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, name, "", proxyType, string(configJSON), inboundPort, "", "", sortOrder, 0, req.Enabled)

		if err != nil {
			result["success"] = false
			result["error"] = "failed to create node"
			results = append(results, result)
			continue
		}

		id, _ := dbResult.LastInsertId()

		result["success"] = true
		result["id"] = id
		result["name"] = name
		result["port"] = inboundPort
		results = append(results, result)
		successCount++
	}

	// Regenerate global config and restart sing-box after batch import
	if successCount > 0 {
		if err := h.regenerateAndRestart(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"total":   len(req.Links),
		"success": successCount,
		"failed":  len(req.Links) - successCount,
		"results": results,
	})
}

// UpdateNode updates a proxy node
func (h *Handler) UpdateNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req models.ProxyNode
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Validate config JSON
	if _, err := req.ParseConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config format"})
		return
	}

	req.ID = id

	// Update node
	_, err = h.db.Exec(`
		UPDATE proxy_nodes 
		SET name = ?, remark = ?, type = ?, config = ?, username = ?, password = ?, 
		    enabled = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, req.Name, req.Remark, req.Type, req.Config, req.Username, req.Password, req.Enabled, id)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update node"})
		return
	}

	// Regenerate global config and restart sing-box
	if err := h.regenerateAndRestart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
		return
	}

	c.JSON(http.StatusOK, req)
}

// DeleteNode deletes a proxy node
func (h *Handler) DeleteNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	// Delete from database
	_, err = h.db.Exec("DELETE FROM proxy_nodes WHERE id = ?", id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to delete node"})
		return
	}

	// Reorder remaining nodes to fill the gap
	if err := h.reorderRemainingNodes(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reorder nodes"})
		return
	}

	// Regenerate global config and restart sing-box
	if err := h.regenerateAndRestart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "node deleted"})
}

// BatchDeleteNodes deletes multiple proxy nodes at once (only restarts sing-box once)
func (h *Handler) BatchDeleteNodes(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no nodes to delete"})
		return
	}

	// Begin transaction
	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	// Delete all nodes in one transaction
	deletedCount := 0
	for _, id := range req.IDs {
		result, err := tx.Exec("DELETE FROM proxy_nodes WHERE id = ?", id)
		if err != nil {
			continue
		}
		if affected, _ := result.RowsAffected(); affected > 0 {
			deletedCount++
		}
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	// Reorder remaining nodes to fill the gaps
	if deletedCount > 0 {
		if err := h.reorderRemainingNodes(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to reorder nodes"})
			return
		}

		// Only regenerate and restart once after all deletions and reordering
		if err := h.regenerateAndRestart(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       "nodes deleted",
		"deleted_count": deletedCount,
	})
}

// ReorderNodes reorders proxy nodes and updates inbound ports
func (h *Handler) ReorderNodes(c *gin.Context) {
	var req struct {
		Nodes []struct {
			ID        int `json:"id"`
			SortOrder int `json:"sort_order"`
		} `json:"nodes"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Get start port
	var startPort int
	h.db.QueryRow("SELECT start_port FROM settings LIMIT 1").Scan(&startPort)

	// Begin transaction
	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	// Update each node
	for _, order := range req.Nodes {
		newPort := startPort + order.SortOrder
		_, err := tx.Exec(`
			UPDATE proxy_nodes 
			SET sort_order = ?, inbound_port = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, order.SortOrder, newPort, order.ID)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	// Regenerate global config and restart sing-box
	if err := h.regenerateAndRestart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "nodes reordered"})
}

// CheckNodeIP checks the IP and location of a proxy node
func (h *Handler) CheckNodeIP(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	// Get node with full info including auth
	var node models.ProxyNode
	var nodeName string
	err = h.db.QueryRow(`
		SELECT id, name, inbound_port, username, password, enabled FROM proxy_nodes WHERE id = ?
	`, id).Scan(&node.ID, &nodeName, &node.InboundPort, &node.Username, &node.Password, &node.Enabled)

	if err != nil {
		fmt.Printf("[API] Node %d not found in database: %v\n", id, err)
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	// Check if node is enabled
	if !node.Enabled {
		fmt.Printf("[API] Node %d (%s) is disabled, cannot check IP\n", id, nodeName)
		c.JSON(http.StatusBadRequest, gin.H{"error": "node is disabled"})
		return
	}

	fmt.Printf("[API] Checking IP for node %d (%s) on port %d (auth: %v)\n", id, nodeName, node.InboundPort, node.Username != "")

	// Check IP through the proxy with authentication
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", node.InboundPort)
	ipInfo, err := h.checkProxyIP(proxyAddr, node.Username, node.Password)
	if err != nil {
		fmt.Printf("[API] Failed to check IP for node %d: %v\n", id, err)
		// Clear stale status on failure so UI can show the node as invalid
		if _, clearErr := h.db.Exec(`
			UPDATE proxy_nodes 
			SET node_ip = '', location = '', country_code = '', latency = 0, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, id); clearErr != nil {
			fmt.Printf("[API] Failed to clear node %d status after error: %v\n", id, clearErr)
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to check IP: %v", err)})
		return
	}

	// If HTTP path failed but SOCKS5 succeeded, treat it as an error because mixed inbound should serve both.
	if ipInfo.Transport != "" && ipInfo.Transport != "http" {
		msg := "http proxy failed while socks5 succeeded"
		if ipInfo.HTTPError != "" {
			msg = ipInfo.HTTPError
		}
		_, _ = h.db.Exec(`
			UPDATE proxy_nodes 
			SET node_ip = '', location = '', country_code = '', latency = 0, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, id)
		c.JSON(http.StatusBadGateway, gin.H{"error": msg})
		return
	}

	fmt.Printf("[API] Successfully checked IP for node %d: %s (%s), latency: %dms\n",
		id, ipInfo.IP, ipInfo.Location, ipInfo.Latency)

	// Update node with IP info, location, country code, and latency
	_, err = h.db.Exec(`
		UPDATE proxy_nodes 
		SET node_ip = ?, location = ?, country_code = ?, latency = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, ipInfo.IP, ipInfo.Location, ipInfo.CountryCode, ipInfo.Latency, id)

	if err != nil {
		fmt.Printf("[API] Failed to update node %d in database: %v\n", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update node"})
		return
	}

	c.JSON(http.StatusOK, ipInfo)
}

// BatchSetAuth sets authentication for multiple nodes
func (h *Handler) BatchSetAuth(c *gin.Context) {
	var req struct {
		NodeIDs  []int  `json:"node_ids"`
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	for _, nodeID := range req.NodeIDs {
		_, err := tx.Exec(`
			UPDATE proxy_nodes 
			SET username = ?, password = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, req.Username, req.Password, nodeID)

		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update auth"})
			return
		}
	}

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	// Regenerate global config and restart sing-box
	if err := h.regenerateAndRestart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "authentication updated"})
}

// GetSettings returns current settings
func (h *Handler) GetSettings(c *gin.Context) {
	var settings models.Settings
	err := h.db.QueryRow("SELECT id, start_port FROM settings LIMIT 1").Scan(&settings.ID, &settings.StartPort)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"start_port": settings.StartPort,
	})
}

// UpdateSettings updates settings
func (h *Handler) UpdateSettings(c *gin.Context) {
	var req struct {
		StartPort     *int    `json:"start_port,omitempty"`
		AdminPassword *string `json:"admin_password,omitempty"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if req.AdminPassword != nil {
		// Hash password
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(*req.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
			return
		}

		_, err = h.db.Exec("UPDATE settings SET admin_password = ?, updated_at = CURRENT_TIMESTAMP", string(hashedPassword))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
			return
		}
	}

	if req.StartPort != nil {
		// Use transaction to ensure atomic update of all ports
		tx, err := h.db.Begin()
		if err != nil {
			log.Printf("Failed to begin transaction: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction"})
			return
		}

		// Update settings
		_, err = tx.Exec("UPDATE settings SET start_port = ?, updated_at = CURRENT_TIMESTAMP", *req.StartPort)
		if err != nil {
			tx.Rollback()
			log.Printf("Failed to update start_port: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update start port"})
			return
		}

		// Query all node ports
		rows, err := tx.Query("SELECT id, sort_order, enabled FROM proxy_nodes ORDER BY sort_order")
		if err != nil {
			tx.Rollback()
			log.Printf("Failed to query nodes: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query nodes"})
			return
		}

		var nodes []struct {
			ID        int
			SortOrder int
			Enabled   bool
		}

		for rows.Next() {
			var node struct {
				ID        int
				SortOrder int
				Enabled   bool
			}
			if err := rows.Scan(&node.ID, &node.SortOrder, &node.Enabled); err != nil {
				rows.Close()
				tx.Rollback()
				log.Printf("Failed to scan node: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read node data"})
				return
			}
			nodes = append(nodes, node)
		}
		rows.Close()

		// Check for iteration errors
		if err := rows.Err(); err != nil {
			tx.Rollback()
			log.Printf("Error iterating nodes: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read nodes"})
			return
		}

		// Update all node ports within transaction
		for _, node := range nodes {
			newPort := *req.StartPort + node.SortOrder
			_, err := tx.Exec("UPDATE proxy_nodes SET inbound_port = ? WHERE id = ?", newPort, node.ID)
			if err != nil {
				tx.Rollback()
				log.Printf("Failed to update port for node %d: %v", node.ID, err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update node port"})
				return
			}
		}

		// Commit transaction
		if err := tx.Commit(); err != nil {
			log.Printf("Failed to commit transaction: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit changes"})
			return
		}

		// Regenerate global config and restart sing-box
		if err := h.regenerateAndRestart(); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "settings updated"})
}

// ParseShareLink parses a share link and returns the config
func (h *Handler) ParseShareLink(c *gin.Context) {
	var req struct {
		Link string `json:"link"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	// Parse the share link using the service
	config, proxyType, name, err := services.ParseShareLink(req.Link)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse link: %v", err)})
		return
	}

	configJSON, _ := json.Marshal(config)

	c.JSON(http.StatusOK, gin.H{
		"type":   proxyType,
		"name":   name,
		"config": string(configJSON),
	})
}

// ExportNode exports a node as its share link format.
func (h *Handler) ExportNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var node models.ProxyNode
	if err := h.db.QueryRow(`
		SELECT id, name, remark, type, config
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	link, err := services.BuildShareLink(node)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"link": link})
}

// BatchExportNodes exports multiple nodes as share links.
func (h *Handler) BatchExportNodes(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no nodes selected"})
		return
	}

	results := []map[string]interface{}{}
	successCount := 0

	for _, id := range req.IDs {
		result := map[string]interface{}{
			"id": id,
		}

		var node models.ProxyNode
		if err := h.db.QueryRow(`
			SELECT id, name, remark, type, config
			FROM proxy_nodes WHERE id = ?
		`, id).Scan(&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config); err != nil {
			result["success"] = false
			if err == sql.ErrNoRows {
				result["error"] = "node not found"
			} else {
				result["error"] = "database error"
			}
			results = append(results, result)
			continue
		}

		link, err := services.BuildShareLink(node)
		if err != nil {
			result["success"] = false
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}

		result["success"] = true
		result["name"] = node.Name
		result["type"] = node.Type
		result["link"] = link
		results = append(results, result)
		successCount++
	}

	c.JSON(http.StatusOK, gin.H{
		"total":   len(req.IDs),
		"success": successCount,
		"failed":  len(req.IDs) - successCount,
		"results": results,
	})
}

// ReplaceNode replaces a node's config using a share link.
func (h *Handler) ReplaceNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req struct {
		Link       string `json:"link" binding:"required"`
		UpdateName *bool  `json:"update_name,omitempty"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	parsedConfig, proxyType, name, err := services.ParseShareLink(req.Link)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse link: %v", err)})
		return
	}

	configJSON, err := json.Marshal(parsedConfig)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed to marshal config"})
		return
	}

	updateName := true
	if req.UpdateName != nil {
		updateName = *req.UpdateName
	}

	var res sql.Result
	if updateName {
		res, err = h.db.Exec(`
			UPDATE proxy_nodes
			SET name = ?, type = ?, config = ?,
			    node_ip = '', location = '', country_code = '', latency = 0,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, name, proxyType, string(configJSON), id)
	} else {
		res, err = h.db.Exec(`
			UPDATE proxy_nodes
			SET type = ?, config = ?,
			    node_ip = '', location = '', country_code = '', latency = 0,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, proxyType, string(configJSON), id)
	}

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update node"})
		return
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	if err := h.regenerateAndRestart(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update sing-box config"})
		return
	}

	var node models.ProxyNode
	err = h.db.QueryRow(`
		SELECT id, name, remark, type, config, inbound_port, username, password,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(
		&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
		&node.Username, &node.Password, &node.SortOrder, &node.NodeIP,
		&node.Location, &node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, node)
}
