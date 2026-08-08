package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

type Handler struct {
	db             *sql.DB
	singBoxService *services.SingBoxService
	loginLimiter   *loginRateLimiter
	nodeWriteMu    sync.Mutex
	checkProxyIP   func(proxyAddr string, username string, password string) (*services.IPInfo, error)
}

type nodeUpsertRequest struct {
	Name            string `json:"name"`
	Remark          string `json:"remark"`
	Type            string `json:"type"`
	Config          string `json:"config"`
	InboundPort     int    `json:"inbound_port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	Enabled         bool   `json:"enabled"`
	TCPReuseEnabled *bool  `json:"tcp_reuse_enabled"`
}

func NewHandler(db *sql.DB, singBoxService *services.SingBoxService) *Handler {
	return &Handler{
		db:             db,
		singBoxService: singBoxService,
		loginLimiter:   newLoginRateLimiterFromEnv(),
		checkProxyIP:   services.CheckProxyIP,
	}
}

func hasRouteSeparatorInUsername(username string) bool {
	return strings.Contains(username, "+")
}

func validateInboundUsername(username string) error {
	if hasRouteSeparatorInUsername(strings.TrimSpace(username)) {
		return fmt.Errorf("username must not contain '+'")
	}
	return nil
}

func validateInboundPort(port int) error {
	if port <= 0 || port > 65535 {
		return fmt.Errorf("inbound port out of range")
	}
	return nil
}

func shouldSkipPortAvailabilityCheck() bool {
	raw := strings.TrimSpace(os.Getenv("SBPM_SKIP_PORT_AVAILABILITY_CHECK"))
	if raw == "" {
		return false
	}
	raw = strings.ToLower(raw)
	return raw != "0" && raw != "false"
}

func validateInboundPortAvailable(port int) error {
	if shouldSkipPortAvailabilityCheck() {
		return nil
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("inbound port not available: %w", err)
	}
	_ = ln.Close()
	return nil
}

func validateStartPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("start port out of range")
	}
	return nil
}

type sqlQueryRower interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}

func getPortSettings(rower sqlQueryRower) (int, bool, error) {
	var startPort int
	var preserveInboundPorts bool
	if err := rower.QueryRow("SELECT start_port, preserve_inbound_ports FROM settings LIMIT 1").Scan(&startPort, &preserveInboundPorts); err != nil {
		return 0, false, err
	}
	return startPort, preserveInboundPorts, nil
}

func collectUsedInboundPortsTx(tx *sql.Tx, excludeNodeID int) (map[int]struct{}, error) {
	query := "SELECT inbound_port FROM proxy_nodes"
	args := []interface{}{}
	if excludeNodeID > 0 {
		query += " WHERE id <> ?"
		args = append(args, excludeNodeID)
	}

	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	usedPorts := make(map[int]struct{})
	for rows.Next() {
		var inboundPort int
		if err := rows.Scan(&inboundPort); err != nil {
			return nil, err
		}
		usedPorts[inboundPort] = struct{}{}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return usedPorts, nil
}

func nextAvailableInboundPort(startPort int, usedPorts map[int]struct{}) (int, error) {
	for inboundPort := startPort; inboundPort <= 65535; inboundPort++ {
		if _, exists := usedPorts[inboundPort]; exists {
			continue
		}
		return inboundPort, nil
	}
	return 0, fmt.Errorf("no available inbound port")
}

func nodeFromUpsertRequest(req nodeUpsertRequest) models.ProxyNode {
	node := models.ProxyNode{
		Name:        req.Name,
		Remark:      req.Remark,
		Type:        req.Type,
		Config:      req.Config,
		InboundPort: req.InboundPort,
		Username:    req.Username,
		Password:    req.Password,
		Enabled:     req.Enabled,
	}
	if req.TCPReuseEnabled != nil {
		node.TCPReuseEnabled = *req.TCPReuseEnabled
	} else {
		node.TCPReuseEnabled = true
	}
	return node
}

// loadAllNodes reads every proxy node from the database ordered by sort_order.
func (h *Handler) loadAllNodes() ([]models.ProxyNode, error) {
	rows, err := h.db.Query(`
		SELECT id, name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodes []models.ProxyNode
	for rows.Next() {
		var node models.ProxyNode
		err := rows.Scan(
			&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
			&node.Username, &node.Password, &node.TCPReuseEnabled, &node.SortOrder, &node.NodeIP, &node.Location,
			&node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

// regenerateAndRestart rebuilds the unified config from the database, validates
// it with `sing-box check`, and only then swaps it into the running process.
// A config the kernel would reject therefore never takes down working nodes:
// on validation failure the running process and the on-disk config are left
// untouched and the kernel's own error message is returned.
func (h *Handler) regenerateAndRestart() error {
	nodes, err := h.loadAllNodes()
	if err != nil {
		return err
	}

	configJSON, err := h.singBoxService.BuildGlobalConfig(nodes)
	if err != nil {
		return err
	}

	if err := h.singBoxService.ValidateConfig(configJSON); err != nil {
		return err
	}

	return h.singBoxService.ApplyConfig(configJSON)
}

// regenerateAndRestartWithRevert applies the new config; when that fails,
// revert is invoked to undo the database change that produced it, so a bad
// node cannot stay persisted and wedge every subsequent operation. The revert
// only restores database state: on validation failure the running process and
// config file were never touched, and on a start failure ApplyConfig has
// already rolled the process back to the last-good config, which matches the
// reverted database contents.
func (h *Handler) regenerateAndRestartWithRevert(revert func() error) error {
	err := h.regenerateAndRestart()
	if err == nil {
		return nil
	}
	if revert != nil {
		if revertErr := revert(); revertErr != nil {
			log.Printf("Failed to revert database change after sing-box config error: %v", revertErr)
		}
	}
	return err
}

// singboxUpdateError builds the error payload for a failed config update,
// surfacing the underlying kernel/validator message to the frontend.
func singboxUpdateError(err error) gin.H {
	return gin.H{"error": fmt.Sprintf("failed to update sing-box config: %v", err)}
}

// reorderRemainingNodes reorders all remaining nodes and reassigns ports to fill gaps
func (h *Handler) reorderRemainingNodes() error {
	startPort, preserveInboundPorts, err := getPortSettings(h.db)
	if err != nil {
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

	// Update each node with new sort_order and inbound_port when needed
	for newSortOrder, nodeID := range nodeIDs {
		if preserveInboundPorts {
			_, err := tx.Exec(`
				UPDATE proxy_nodes
				SET sort_order = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, newSortOrder, nodeID)
			if err != nil {
				return err
			}
			continue
		}

		newPort := startPort + newSortOrder
		if err := validateInboundPort(newPort); err != nil {
			return err
		}
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
		token := normalizeAuthToken(c.GetHeader("Authorization"))
		ok, err := h.isValidAdminSession(token)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			c.Abort()
			return
		}
		if !ok {
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

	ip := c.ClientIP()
	now := time.Now()
	if h.loginLimiter != nil {
		if ok, retryAfter := h.loginLimiter.Allow(ip, now); !ok {
			c.Header("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many login attempts, please try again later"})
			return
		}
	}

	envPassword := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD"))
	if envPassword != "" {
		if !constantTimeEqual(envPassword, req.Password) {
			if h.loginLimiter != nil {
				h.loginLimiter.OnFailure(ip, now)
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
			return
		}
	} else {
		var settings models.Settings
		err := h.db.QueryRow("SELECT id, admin_password, admin_password_set FROM settings LIMIT 1").Scan(&settings.ID, &settings.AdminPassword, &settings.AdminPasswordSet)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		if settings.AdminPasswordSet == 0 || strings.TrimSpace(settings.AdminPassword) == "" {
			c.JSON(http.StatusPreconditionRequired, gin.H{"error": "admin password not set", "setup_required": true})
			return
		}

		// Compare password using bcrypt only (no plaintext fallback for security)
		if err := bcrypt.CompareHashAndPassword([]byte(settings.AdminPassword), []byte(req.Password)); err != nil {
			if h.loginLimiter != nil {
				h.loginLimiter.OnFailure(ip, now)
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
			return
		}
	}

	if h.loginLimiter != nil {
		h.loginLimiter.OnSuccess(ip)
	}

	token, expiry, err := h.createAdminSession(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"token":  token,
		"expiry": expiry.Unix(),
	})
}

// AuthStatus returns whether setup is required and whether admin password is locked by env.
func (h *Handler) AuthStatus(c *gin.Context) {
	locked := strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")) != ""
	setupRequired := false

	if !locked {
		var set int
		var hash string
		err := h.db.QueryRow("SELECT admin_password, admin_password_set FROM settings LIMIT 1").Scan(&hash, &set)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		setupRequired = set == 0 || strings.TrimSpace(hash) == ""
	}

	c.JSON(http.StatusOK, gin.H{
		"admin_password_locked": locked,
		"setup_required":        setupRequired,
	})
}

// SetupAdminPassword sets the initial admin password when ADMIN_PASSWORD is not set.
func (h *Handler) SetupAdminPassword(c *gin.Context) {
	if strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")) != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "admin password is managed by ADMIN_PASSWORD"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if strings.TrimSpace(req.Password) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password is required"})
		return
	}
	if len([]rune(req.Password)) < 8 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 8 characters"})
		return
	}

	var settingsID int
	var hash string
	var set int
	if err := h.db.QueryRow("SELECT id, admin_password, admin_password_set FROM settings LIMIT 1").Scan(&settingsID, &hash, &set); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if set != 0 && strings.TrimSpace(hash) != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "admin password already set"})
		return
	}

	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}
	if _, err := h.db.Exec("UPDATE settings SET admin_password = ?, admin_password_set = 1, updated_at = CURRENT_TIMESTAMP WHERE id = ?", string(hashedPassword), settingsID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
		return
	}

	token, expiry, err := h.createAdminSession(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token":  token,
		"expiry": expiry.Unix(),
	})
}

// GetNodes returns all proxy nodes
func (h *Handler) GetNodes(c *gin.Context) {
	rows, err := h.db.Query(`
		SELECT id, name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled,
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
			&node.Username, &node.Password, &node.TCPReuseEnabled, &node.SortOrder, &node.NodeIP,
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
		SELECT id, name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(
		&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
		&node.Username, &node.Password, &node.TCPReuseEnabled, &node.SortOrder, &node.NodeIP,
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
	var payload nodeUpsertRequest
	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req := nodeFromUpsertRequest(payload)

	// Validate config JSON
	if _, err := req.ParseConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config format"})
		return
	}
	if err := validateInboundUsername(req.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Ensure inbound auth is enabled by default (generate missing credentials).
	if strings.TrimSpace(req.Username) == "" {
		username, err := generateRandomUsername(12)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate username"})
			return
		}
		req.Username = username
	}
	if strings.TrimSpace(req.Password) == "" {
		password, err := generateRandomString(24)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate password"})
			return
		}
		req.Password = password
	}
	if err := validateInboundUsername(req.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	// Allocate sort_order / inbound_port in one critical section to avoid races.
	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	var maxOrder int
	if err := tx.QueryRow("SELECT COALESCE(MAX(sort_order), -1) FROM proxy_nodes").Scan(&maxOrder); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	req.SortOrder = maxOrder + 1

	startPort, preserveInboundPorts, err := getPortSettings(tx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	usedInboundPorts, err := collectUsedInboundPortsTx(tx, 0)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if preserveInboundPorts {
		if req.InboundPort == 0 {
			req.InboundPort, err = nextAvailableInboundPort(startPort, usedInboundPorts)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
				return
			}
		}
	} else {
		req.InboundPort = startPort + req.SortOrder
	}

	if err := validateInboundPort(req.InboundPort); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, exists := usedInboundPorts[req.InboundPort]; exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "inbound port already in use"})
		return
	}
	if err := validateInboundPortAvailable(req.InboundPort); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Insert node
	result, err := tx.Exec(`
		INSERT INTO proxy_nodes (name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled, sort_order, latency, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, req.Name, req.Remark, req.Type, req.Config, req.InboundPort, req.Username, req.Password, req.TCPReuseEnabled, req.SortOrder, 0, req.Enabled)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create node"})
		return
	}

	id, err := appdb.LastInsertID(c.Request.Context(), tx, result, appdb.DialectFor(h.db))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read created node id"})
		return
	}
	req.ID = int(id)

	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	// Regenerate global config and restart sing-box; if the new node produces a
	// config the kernel rejects, remove it again so the panel stays healthy.
	if err := h.regenerateAndRestartWithRevert(func() error {
		_, revertErr := h.db.Exec("DELETE FROM proxy_nodes WHERE id = ?", req.ID)
		return revertErr
	}); err != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
		return
	}

	c.JSON(http.StatusCreated, req)
}

type batchImportCandidate struct {
	node   models.ProxyNode
	result map[string]interface{}
}

// BatchImportNodes imports multiple nodes from share links, subscriptions, or
// structured payloads. Candidate nodes are kernel-validated before any row is
// inserted, so a rejected candidate cannot roll back unrelated valid nodes.
func (h *Handler) BatchImportNodes(c *gin.Context) {
	var req struct {
		Links      []string `json:"links"`
		Content    string   `json:"content"`
		SourceType string   `json:"source_type"`
		Enabled    bool     `json:"enabled"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	sourceType, err := services.ParseBatchImportSourceType(req.SourceType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	var sources []string
	if strings.TrimSpace(req.Content) != "" {
		sources = []string{req.Content}
	} else {
		sources = req.Links
	}
	if len(sources) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no input provided"})
		return
	}

	items, expandFailures, err := services.ExpandBatchImportSourcesWithType(c.Request.Context(), sources, sourceType)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	results := make([]map[string]interface{}, 0, len(items)+len(expandFailures))
	for _, failure := range expandFailures {
		results = append(results, map[string]interface{}{
			"success": false,
			"source":  failure.Source,
			"error":   failure.Error,
		})
	}
	if len(items) == 0 {
		if len(expandFailures) > 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": expandFailures[0].Error})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "input contains no proxy nodes"})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	existingNodes, err := h.loadAllNodes()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if err := h.validateNodeSet(existingNodes); err != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(fmt.Errorf("existing node set is invalid: %w", err)))
		return
	}

	startPort, preserveInboundPorts, err := getPortSettings(h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	maxOrder, maxID, usedInboundPorts := batchImportExistingState(existingNodes)
	nextOrder := maxOrder + 1

	candidates := make([]*batchImportCandidate, 0, len(items))
	for _, item := range items {
		result := map[string]interface{}{"source": item.Source}
		var parsedConfig any
		var proxyType, name string
		if item.Link != "" {
			result["link"] = item.Link
			var parseErr error
			parsedConfig, proxyType, name, parseErr = services.ParseShareLink(item.Link)
			if parseErr != nil {
				result["success"] = false
				result["error"] = parseErr.Error()
				results = append(results, result)
				continue
			}
		} else {
			parsedConfig, proxyType, name = item.Config, item.Type, item.Name
		}

		configJSON, marshalErr := json.Marshal(parsedConfig)
		if marshalErr != nil {
			result["success"] = false
			result["error"] = "failed to marshal config"
			results = append(results, result)
			continue
		}
		username, usernameErr := generateRandomUsername(12)
		if usernameErr != nil {
			result["success"] = false
			result["error"] = "failed to generate username"
			results = append(results, result)
			continue
		}
		password, passwordErr := generateRandomString(24)
		if passwordErr != nil {
			result["success"] = false
			result["error"] = "failed to generate password"
			results = append(results, result)
			continue
		}

		inboundPort := startPort + nextOrder
		if preserveInboundPorts {
			inboundPort, err = nextAvailableInboundPort(startPort, usedInboundPorts)
		}
		if err == nil {
			err = validateInboundPort(inboundPort)
		}
		if err == nil {
			if _, exists := usedInboundPorts[inboundPort]; exists {
				err = fmt.Errorf("inbound port already in use")
			}
		}
		if err != nil {
			result["success"] = false
			result["error"] = err.Error()
			results = append(results, result)
			err = nil
			continue
		}

		candidate := &batchImportCandidate{
			node: models.ProxyNode{
				ID:              maxID + len(candidates) + 1,
				Name:            name,
				Type:            proxyType,
				Config:          string(configJSON),
				InboundPort:     inboundPort,
				Username:        username,
				Password:        password,
				TCPReuseEnabled: true,
				SortOrder:       nextOrder,
				Enabled:         true,
			},
			result: result,
		}
		candidates = append(candidates, candidate)
		nextOrder++
		usedInboundPorts[inboundPort] = struct{}{}
	}

	validCandidates, rejectedCandidates := h.selectValidBatchCandidates(existingNodes, candidates)
	for candidate, validationErr := range rejectedCandidates {
		candidate.result["success"] = false
		candidate.result["error"] = fmt.Sprintf("sing-box validation failed: %v", validationErr)
	}

	// Reassign ports and order after isolation so rejected candidates leave no
	// artificial gaps. A final grouped validation covers the exact persisted set.
	maxOrder, maxID, usedInboundPorts = batchImportExistingState(existingNodes)
	nextOrder = maxOrder + 1
	portValidCandidates := make([]*batchImportCandidate, 0, len(validCandidates))
	for _, candidate := range validCandidates {
		inboundPort := startPort + nextOrder
		if preserveInboundPorts {
			inboundPort, err = nextAvailableInboundPort(startPort, usedInboundPorts)
		}
		if err == nil {
			err = validateInboundPort(inboundPort)
		}
		if err == nil {
			if _, exists := usedInboundPorts[inboundPort]; exists {
				err = fmt.Errorf("inbound port already in use")
			}
		}
		if err == nil {
			err = validateInboundPortAvailable(inboundPort)
		}
		if err != nil {
			candidate.result["success"] = false
			candidate.result["error"] = err.Error()
			err = nil
			continue
		}
		candidate.node.ID = maxID + len(portValidCandidates) + 1
		candidate.node.InboundPort = inboundPort
		candidate.node.SortOrder = nextOrder
		portValidCandidates = append(portValidCandidates, candidate)
		nextOrder++
		usedInboundPorts[inboundPort] = struct{}{}
	}
	validCandidates, finalRejected := h.selectValidBatchCandidates(existingNodes, portValidCandidates)
	for candidate, validationErr := range finalRejected {
		candidate.result["success"] = false
		candidate.result["error"] = fmt.Sprintf("sing-box validation failed: %v", validationErr)
	}

	accepted := make(map[*batchImportCandidate]struct{}, len(validCandidates))
	for _, candidate := range validCandidates {
		accepted[candidate] = struct{}{}
	}
	for _, candidate := range candidates {
		if _, ok := accepted[candidate]; !ok {
			results = append(results, candidate.result)
		}
	}

	if len(validCandidates) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"total": len(items) + len(expandFailures), "success": 0,
			"failed": len(items) + len(expandFailures), "results": results,
		})
		return
	}

	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`
		INSERT INTO proxy_nodes (name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled, sort_order, latency, enabled)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer stmt.Close()

	insertedIDs := make([]int64, 0, len(validCandidates))
	for _, candidate := range validCandidates {
		node := &candidate.node
		node.Enabled = req.Enabled
		dbResult, execErr := stmt.Exec(
			node.Name, "", node.Type, node.Config, node.InboundPort, node.Username,
			node.Password, node.TCPReuseEnabled, node.SortOrder, 0, node.Enabled,
		)
		if execErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create node"})
			return
		}
		id, idErr := appdb.LastInsertID(c.Request.Context(), tx, dbResult, appdb.DialectFor(h.db))
		if idErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read created node id"})
			return
		}
		node.ID = int(id)
		insertedIDs = append(insertedIDs, id)
	}

	finalNodes := appendBatchCandidateNodes(existingNodes, validCandidates)
	configJSON, err := h.singBoxService.BuildGlobalConfig(finalNodes)
	if err == nil {
		err = h.singBoxService.ValidateConfig(configJSON)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}
	if err := h.singBoxService.ApplyConfig(configJSON); err != nil {
		if revertErr := h.deleteImportedNodes(insertedIDs); revertErr != nil {
			log.Printf("Failed to revert batch import after sing-box apply error: %v", revertErr)
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
		return
	}

	for _, candidate := range validCandidates {
		candidate.result["success"] = true
		candidate.result["id"] = candidate.node.ID
		candidate.result["name"] = candidate.node.Name
		candidate.result["port"] = candidate.node.InboundPort
		candidate.result["username"] = candidate.node.Username
		candidate.result["password"] = candidate.node.Password
		results = append(results, candidate.result)
	}
	c.JSON(http.StatusOK, gin.H{
		"total": len(items) + len(expandFailures), "success": len(validCandidates),
		"failed": len(items) + len(expandFailures) - len(validCandidates), "results": results,
	})
}

func batchImportExistingState(nodes []models.ProxyNode) (int, int, map[int]struct{}) {
	maxOrder, maxID := -1, 0
	usedPorts := make(map[int]struct{}, len(nodes))
	for _, node := range nodes {
		if node.SortOrder > maxOrder {
			maxOrder = node.SortOrder
		}
		if node.ID > maxID {
			maxID = node.ID
		}
		usedPorts[node.InboundPort] = struct{}{}
	}
	return maxOrder, maxID, usedPorts
}

func appendBatchCandidateNodes(existing []models.ProxyNode, candidates []*batchImportCandidate) []models.ProxyNode {
	nodes := make([]models.ProxyNode, 0, len(existing)+len(candidates))
	nodes = append(nodes, existing...)
	for _, candidate := range candidates {
		nodes = append(nodes, candidate.node)
	}
	return nodes
}

func (h *Handler) validateNodeSet(nodes []models.ProxyNode) error {
	configJSON, err := h.singBoxService.BuildGlobalConfig(nodes)
	if err != nil {
		return err
	}
	return h.singBoxService.ValidateConfig(configJSON)
}

func (h *Handler) selectValidBatchCandidates(
	existing []models.ProxyNode,
	candidates []*batchImportCandidate,
) ([]*batchImportCandidate, map[*batchImportCandidate]error) {
	rejected := make(map[*batchImportCandidate]error)
	if len(candidates) == 0 {
		return nil, rejected
	}
	groups := batchImportDependencyGroups(candidates)
	accepted := make([]*batchImportCandidate, 0, len(candidates))
	acceptedSet := make(map[*batchImportCandidate]struct{}, len(candidates))

	var isolate func([][]*batchImportCandidate)
	isolate = func(subset [][]*batchImportCandidate) {
		if len(subset) == 0 {
			return
		}
		trial := make([]*batchImportCandidate, 0, len(accepted)+len(candidates))
		trial = append(trial, accepted...)
		for _, group := range subset {
			trial = append(trial, group...)
		}
		err := h.validateNodeSet(appendBatchCandidateNodes(existing, trial))
		if err == nil {
			for _, group := range subset {
				for _, candidate := range group {
					accepted = append(accepted, candidate)
					acceptedSet[candidate] = struct{}{}
				}
			}
			return
		}
		if len(subset) == 1 {
			groupAccepted, groupRejected := h.selectValidDependencyGroup(existing, accepted, subset[0], err)
			for _, candidate := range groupAccepted {
				accepted = append(accepted, candidate)
				acceptedSet[candidate] = struct{}{}
			}
			for candidate, validationErr := range groupRejected {
				rejected[candidate] = validationErr
			}
			return
		}
		middle := len(subset) / 2
		isolate(subset[:middle])
		isolate(subset[middle:])
	}
	isolate(groups)

	// Dependency targets may be validated before their dependants. Preserve the
	// user's original import order for port allocation and response ordering.
	orderedAccepted := make([]*batchImportCandidate, 0, len(acceptedSet))
	for _, candidate := range candidates {
		if _, ok := acceptedSet[candidate]; ok {
			orderedAccepted = append(orderedAccepted, candidate)
		}
	}
	return orderedAccepted, rejected
}

func (h *Handler) selectValidDependencyGroup(
	existing []models.ProxyNode,
	accepted []*batchImportCandidate,
	group []*batchImportCandidate,
	groupErr error,
) ([]*batchImportCandidate, map[*batchImportCandidate]error) {
	groupByName := make(map[string][]*batchImportCandidate, len(group))
	for _, candidate := range group {
		name := strings.TrimSpace(candidate.node.Name)
		if name != "" {
			groupByName[name] = append(groupByName[name], candidate)
		}
	}

	processed := make(map[*batchImportCandidate]struct{}, len(group))
	selected := make([]*batchImportCandidate, 0, len(group))
	rejected := make(map[*batchImportCandidate]error)
	for len(processed) < len(group) {
		ready := make([]*batchImportCandidate, 0, len(group)-len(processed))
		for _, candidate := range group {
			if _, done := processed[candidate]; done {
				continue
			}
			dependenciesReady := true
			detour := batchImportCandidateDetour(candidate)
			if detour != "" && detour != "direct" {
				for _, dependency := range groupByName[detour] {
					if _, done := processed[dependency]; !done {
						dependenciesReady = false
						break
					}
				}
			}
			if dependenciesReady {
				ready = append(ready, candidate)
			}
		}

		if len(ready) == 0 {
			for _, candidate := range group {
				if _, done := processed[candidate]; done {
					continue
				}
				processed[candidate] = struct{}{}
				rejected[candidate] = groupErr
			}
			break
		}

		for _, candidate := range ready {
			processed[candidate] = struct{}{}
			trial := make([]*batchImportCandidate, 0, len(accepted)+len(selected)+1)
			trial = append(trial, accepted...)
			trial = append(trial, selected...)
			trial = append(trial, candidate)
			if err := h.validateNodeSet(appendBatchCandidateNodes(existing, trial)); err != nil {
				rejected[candidate] = err
				continue
			}
			selected = append(selected, candidate)
		}
	}
	return selected, rejected
}

func batchImportCandidateDetour(candidate *batchImportCandidate) string {
	if candidate == nil {
		return ""
	}
	var config struct {
		Detour string `json:"detour"`
	}
	if json.Unmarshal([]byte(candidate.node.Config), &config) != nil {
		return ""
	}
	return strings.TrimSpace(config.Detour)
}

func batchImportDependencyGroups(candidates []*batchImportCandidate) [][]*batchImportCandidate {
	nameToIndexes := make(map[string][]int, len(candidates))
	for index, candidate := range candidates {
		name := strings.TrimSpace(candidate.node.Name)
		if name != "" {
			nameToIndexes[name] = append(nameToIndexes[name], index)
		}
	}
	adjacency := make([][]int, len(candidates))
	for index, candidate := range candidates {
		detour := batchImportCandidateDetour(candidate)
		if detour == "" || detour == "direct" {
			continue
		}
		for _, target := range nameToIndexes[detour] {
			if target == index {
				continue
			}
			adjacency[index] = append(adjacency[index], target)
			adjacency[target] = append(adjacency[target], index)
		}
	}

	visited := make([]bool, len(candidates))
	groups := make([][]*batchImportCandidate, 0, len(candidates))
	for start := range candidates {
		if visited[start] {
			continue
		}
		visited[start] = true
		queue := []int{start}
		group := make([]*batchImportCandidate, 0, 1)
		for len(queue) > 0 {
			index := queue[0]
			queue = queue[1:]
			group = append(group, candidates[index])
			for _, adjacent := range adjacency[index] {
				if !visited[adjacent] {
					visited[adjacent] = true
					queue = append(queue, adjacent)
				}
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func (h *Handler) deleteImportedNodes(ids []int64) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.Exec("DELETE FROM proxy_nodes WHERE id = ?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UpdateNode updates a proxy node
func (h *Handler) UpdateNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var payload nodeUpsertRequest
	if err := c.BindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	req := nodeFromUpsertRequest(payload)

	// Validate config JSON
	if _, err := req.ParseConfig(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid config format"})
		return
	}
	if err := validateInboundUsername(req.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.ID = id

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	// Snapshot the full previous row so the update can be reverted if the new
	// config is rejected by the kernel.
	var prev models.ProxyNode
	if err := tx.QueryRow(`
		SELECT name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled, sort_order, enabled
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(
		&prev.Name, &prev.Remark, &prev.Type, &prev.Config, &prev.InboundPort,
		&prev.Username, &prev.Password, &prev.TCPReuseEnabled, &prev.SortOrder, &prev.Enabled,
	); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	currentInboundPort := prev.InboundPort
	if payload.TCPReuseEnabled == nil {
		req.TCPReuseEnabled = prev.TCPReuseEnabled
	}
	req.SortOrder = prev.SortOrder

	startPort, preserveInboundPorts, err := getPortSettings(tx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	usedInboundPorts, err := collectUsedInboundPortsTx(tx, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if preserveInboundPorts {
		if req.InboundPort == 0 {
			req.InboundPort, err = nextAvailableInboundPort(startPort, usedInboundPorts)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
				return
			}
		}
	} else {
		req.InboundPort = startPort + req.SortOrder
	}

	if err := validateInboundPort(req.InboundPort); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, exists := usedInboundPorts[req.InboundPort]; exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "inbound port already in use"})
		return
	}
	if req.InboundPort != currentInboundPort {
		if err := validateInboundPortAvailable(req.InboundPort); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	// Update node
	_, err = tx.Exec(`
		UPDATE proxy_nodes 
		SET name = ?, remark = ?, type = ?, config = ?, inbound_port = ?, username = ?, password = ?, tcp_reuse_enabled = ?,
		    enabled = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, req.Name, req.Remark, req.Type, req.Config, req.InboundPort, req.Username, req.Password, req.TCPReuseEnabled, req.Enabled, id)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update node"})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit transaction"})
		return
	}

	// Regenerate global config and restart sing-box; restore the previous row
	// if the updated node produces a config the kernel rejects.
	if err := h.regenerateAndRestartWithRevert(func() error {
		_, revertErr := h.db.Exec(`
			UPDATE proxy_nodes
			SET name = ?, remark = ?, type = ?, config = ?, inbound_port = ?, username = ?, password = ?,
			    tcp_reuse_enabled = ?, enabled = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, prev.Name, prev.Remark, prev.Type, prev.Config, prev.InboundPort, prev.Username, prev.Password,
			prev.TCPReuseEnabled, prev.Enabled, id)
		return revertErr
	}); err != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *Handler) UpdateNodeRemark(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req struct {
		Remark string `json:"remark"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	res, err := h.db.Exec(`
		UPDATE proxy_nodes
		SET remark = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, req.Remark, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update remark"})
		return
	}

	affected, _ := res.RowsAffected()
	if affected == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"id": id, "remark": req.Remark})
}

// DeleteNode deletes a proxy node
func (h *Handler) DeleteNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

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
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
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

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

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
			c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
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

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	startPort, preserveInboundPorts, err := getPortSettings(h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	// Begin transaction
	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	// Update each node
	for _, order := range req.Nodes {
		if preserveInboundPorts {
			_, err := tx.Exec(`
				UPDATE proxy_nodes
				SET sort_order = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, order.SortOrder, order.ID)

			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update order"})
				return
			}
			continue
		}

		newPort := startPort + order.SortOrder
		if err := validateInboundPort(newPort); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
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
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
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
	proxyAddr := fmt.Sprintf("localhost:%d", node.InboundPort)
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
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
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
	if err := validateInboundUsername(req.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	tx, err := h.db.Begin()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	// Snapshot previous credentials so the change can be reverted if the new
	// config fails to apply.
	type nodeAuthSnapshot struct {
		id       int
		username string
		password string
	}
	prevAuth := make([]nodeAuthSnapshot, 0, len(req.NodeIDs))
	for _, nodeID := range req.NodeIDs {
		var snapshot nodeAuthSnapshot
		snapshot.id = nodeID
		if err := tx.QueryRow(
			"SELECT username, password FROM proxy_nodes WHERE id = ?", nodeID,
		).Scan(&snapshot.username, &snapshot.password); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		prevAuth = append(prevAuth, snapshot)
	}

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
	if err := h.regenerateAndRestartWithRevert(func() error {
		for _, snapshot := range prevAuth {
			if _, revertErr := h.db.Exec(`
				UPDATE proxy_nodes
				SET username = ?, password = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, snapshot.username, snapshot.password, snapshot.id); revertErr != nil {
				return revertErr
			}
		}
		return nil
	}); err != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "authentication updated"})
}

// GetSettings returns current settings
func (h *Handler) GetSettings(c *gin.Context) {
	var settings models.Settings
	err := h.db.QueryRow("SELECT id, start_port, preserve_inbound_ports FROM settings LIMIT 1").Scan(&settings.ID, &settings.StartPort, &settings.PreserveInboundPorts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"start_port":             settings.StartPort,
		"preserve_inbound_ports": settings.PreserveInboundPorts,
		"admin_password_locked":  strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")) != "",
	})
}

// UpdateSettings updates settings
func (h *Handler) UpdateSettings(c *gin.Context) {
	var req struct {
		StartPort            *int    `json:"start_port,omitempty"`
		AdminPassword        *string `json:"admin_password,omitempty"`
		PreserveInboundPorts *bool   `json:"preserve_inbound_ports,omitempty"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	if req.AdminPassword != nil {
		if strings.TrimSpace(os.Getenv("ADMIN_PASSWORD")) != "" {
			c.JSON(http.StatusConflict, gin.H{"error": "admin password is managed by ADMIN_PASSWORD"})
			return
		}
		if strings.TrimSpace(*req.AdminPassword) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password is required"})
			return
		}
		if len([]rune(*req.AdminPassword)) < 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 8 characters"})
			return
		}

		// Hash password
		hashedPassword, err := bcrypt.GenerateFromPassword([]byte(*req.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
			return
		}

		_, err = h.db.Exec("UPDATE settings SET admin_password = ?, admin_password_set = 1, updated_at = CURRENT_TIMESTAMP", string(hashedPassword))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
			return
		}
		// Revoke all sessions after password change.
		_, _ = h.db.Exec("DELETE FROM admin_sessions")
	}

	portsChanged := false

	if req.StartPort != nil || req.PreserveInboundPorts != nil {
		if req.StartPort != nil {
			if err := validateStartPort(*req.StartPort); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}

		h.nodeWriteMu.Lock()
		defer h.nodeWriteMu.Unlock()

		tx, err := h.db.Begin()
		if err != nil {
			log.Printf("Failed to begin transaction: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to start transaction"})
			return
		}
		defer tx.Rollback()

		currentStartPort, currentPreserveInboundPorts, err := getPortSettings(tx)
		if err != nil {
			log.Printf("Failed to query settings: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query settings"})
			return
		}

		nextStartPort := currentStartPort
		if req.StartPort != nil {
			nextStartPort = *req.StartPort
		}

		nextPreserveInboundPorts := currentPreserveInboundPorts
		if req.PreserveInboundPorts != nil {
			nextPreserveInboundPorts = *req.PreserveInboundPorts
		}

		_, err = tx.Exec("UPDATE settings SET start_port = ?, preserve_inbound_ports = ?, updated_at = CURRENT_TIMESTAMP", nextStartPort, nextPreserveInboundPorts)
		if err != nil {
			log.Printf("Failed to update settings: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update settings"})
			return
		}

		shouldReassignInboundPorts := !nextPreserveInboundPorts && (req.StartPort != nil || currentPreserveInboundPorts != nextPreserveInboundPorts)
		if shouldReassignInboundPorts {
			rows, err := tx.Query("SELECT id, sort_order FROM proxy_nodes ORDER BY sort_order")
			if err != nil {
				log.Printf("Failed to query nodes: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query nodes"})
				return
			}

			var nodes []struct {
				ID        int
				SortOrder int
			}

			for rows.Next() {
				var node struct {
					ID        int
					SortOrder int
				}
				if err := rows.Scan(&node.ID, &node.SortOrder); err != nil {
					rows.Close()
					log.Printf("Failed to scan node: %v", err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read node data"})
					return
				}
				nodes = append(nodes, node)
			}
			rows.Close()

			if err := rows.Err(); err != nil {
				log.Printf("Error iterating nodes: %v", err)
				c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read nodes"})
				return
			}

			for _, node := range nodes {
				newPort := nextStartPort + node.SortOrder
				if err := validateInboundPort(newPort); err != nil {
					c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
					return
				}
				_, err := tx.Exec("UPDATE proxy_nodes SET inbound_port = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?", newPort, node.ID)
				if err != nil {
					log.Printf("Failed to update port for node %d: %v", node.ID, err)
					c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update node port"})
					return
				}
			}

			portsChanged = true
		}

		if err := tx.Commit(); err != nil {
			log.Printf("Failed to commit transaction: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit changes"})
			return
		}

	}

	if portsChanged {
		if err := h.regenerateAndRestart(); err != nil {
			c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "settings updated"})
}

// Logout revokes the current admin session token.
func (h *Handler) Logout(c *gin.Context) {
	token := normalizeAuthToken(c.GetHeader("Authorization"))
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}

	if _, err := h.db.Exec("DELETE FROM admin_sessions WHERE token_hash = ?", hashSessionToken(token)); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "logged out"})
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

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	// Snapshot the fields being replaced so the change can be reverted if the
	// new config is rejected by the kernel.
	var prevName, prevType, prevConfig, prevNodeIP, prevLocation, prevCountryCode string
	var prevLatency int
	if err := h.db.QueryRow(`
		SELECT name, type, config, node_ip, location, country_code, latency
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(&prevName, &prevType, &prevConfig, &prevNodeIP, &prevLocation, &prevCountryCode, &prevLatency); err != nil {
		if err == sql.ErrNoRows {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if updateName {
		_, err = h.db.Exec(`
			UPDATE proxy_nodes
			SET name = ?, type = ?, config = ?,
			    node_ip = '', location = '', country_code = '', latency = 0,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, name, proxyType, string(configJSON), id)
	} else {
		_, err = h.db.Exec(`
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

	if err := h.regenerateAndRestartWithRevert(func() error {
		_, revertErr := h.db.Exec(`
			UPDATE proxy_nodes
			SET name = ?, type = ?, config = ?,
			    node_ip = ?, location = ?, country_code = ?, latency = ?,
			    updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, prevName, prevType, prevConfig, prevNodeIP, prevLocation, prevCountryCode, prevLatency, id)
		return revertErr
	}); err != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(err))
		return
	}

	var node models.ProxyNode
	err = h.db.QueryRow(`
		SELECT id, name, remark, type, config, inbound_port, username, password, tcp_reuse_enabled,
		       sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(
		&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
		&node.Username, &node.Password, &node.TCPReuseEnabled, &node.SortOrder, &node.NodeIP,
		&node.Location, &node.CountryCode, &node.Latency, &node.Enabled, &node.CreatedAt, &node.UpdatedAt,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, node)
}
