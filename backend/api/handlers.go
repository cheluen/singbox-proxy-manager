package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	db              *sql.DB
	singBoxService  *services.SingBoxService
	loginLimiter    *loginRateLimiter
	nodeWriteMu     sync.Mutex
	passwordSetupMu sync.Mutex
	nodeMutations   *NodeMutationCoordinator
	checkProxyIP    func(context.Context, string, string, string) (*services.IPInfo, error)
	hashPassword    func([]byte, int) ([]byte, error)
}

type nodeUpsertRequest struct {
	Name            string `json:"name"`
	Remark          string `json:"remark"`
	Type            string `json:"type"`
	Config          string `json:"config"`
	InboundPort     int    `json:"inbound_port"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	AuthEnabled     *bool  `json:"auth_enabled,omitempty"`
	Enabled         bool   `json:"enabled"`
	TCPReuseEnabled *bool  `json:"tcp_reuse_enabled"`
}

func NewHandler(db *sql.DB, singBoxService *services.SingBoxService) *Handler {
	return &Handler{
		db:             db,
		singBoxService: singBoxService,
		loginLimiter:   newLoginRateLimiterFromEnv(),
		nodeMutations:  NewNodeMutationCoordinator(db, singBoxService),
		checkProxyIP:   services.CheckProxyIPContext,
		hashPassword:   bcrypt.GenerateFromPassword,
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

func validateInboundCredentials(username string, password string, authEnabled *bool) error {
	usernameSet := username != ""
	passwordSet := password != ""
	if usernameSet != passwordSet {
		return fmt.Errorf("username and password must be provided together")
	}
	if authEnabled == nil {
		return nil
	}
	if *authEnabled && !usernameSet {
		return fmt.Errorf("username and password are required when authentication is enabled")
	}
	if !*authEnabled && usernameSet {
		return fmt.Errorf("username and password must be empty when authentication is disabled")
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
	if err := rower.QueryRow("SELECT start_port, preserve_inbound_ports FROM settings WHERE singleton_key = 1").Scan(&startPort, &preserveInboundPorts); err != nil {
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
	return loadAllNodesFrom(context.Background(), h.db)
}

// singboxUpdateError builds the error payload for a failed config update,
// surfacing the underlying kernel/validator message to the frontend.
func singboxUpdateError(err error) gin.H {
	return gin.H{"error": fmt.Sprintf("failed to update sing-box config: %v", err)}
}

// Auth middleware
func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := normalizeAuthToken(c.GetHeader("Authorization"))
		ok, err := h.isValidAdminSessionContext(c.Request.Context(), token)
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
	var authGeneration int64
	if envPassword != "" {
		if !constantTimeEqual(envPassword, req.Password) {
			if h.loginLimiter != nil {
				h.loginLimiter.OnFailure(ip, now)
			}
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
			return
		}
		if err := h.db.QueryRowContext(c.Request.Context(), "SELECT auth_generation FROM settings WHERE singleton_key = 1").Scan(&authGeneration); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
	} else {
		var settings models.Settings
		err := h.db.QueryRowContext(c.Request.Context(), `
			SELECT id, admin_password, admin_password_set, auth_generation
			FROM settings
			WHERE singleton_key = 1
		`).Scan(&settings.ID, &settings.AdminPassword, &settings.AdminPasswordSet, &settings.AuthGeneration)
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
		authGeneration = settings.AuthGeneration
	}

	if h.loginLimiter != nil {
		h.loginLimiter.OnSuccess(ip)
	}

	token, expiry, err := createAdminSessionWithGeneration(
		c.Request.Context(),
		c,
		h.db,
		authGeneration,
	)
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
		err := h.db.QueryRowContext(c.Request.Context(), "SELECT admin_password, admin_password_set FROM settings WHERE singleton_key = 1").Scan(&hash, &set)
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

	h.passwordSetupMu.Lock()
	defer h.passwordSetupMu.Unlock()

	var passwordSet int
	var existingHash string
	if err := h.db.QueryRowContext(
		c.Request.Context(),
		"SELECT admin_password, admin_password_set FROM settings WHERE singleton_key = 1",
	).Scan(&existingHash, &passwordSet); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if passwordSet != 0 || strings.TrimSpace(existingHash) != "" {
		c.JSON(http.StatusConflict, gin.H{"error": "admin password already set"})
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

	hashPassword := h.hashPassword
	if hashPassword == nil {
		hashPassword = bcrypt.GenerateFromPassword
	}
	hashedPassword, err := hashPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
		return
	}
	tx, err := h.db.BeginTx(c.Request.Context(), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(c.Request.Context(), `
		UPDATE settings
		SET admin_password = ?,
		    admin_password_set = 1,
		    auth_generation = auth_generation + 1,
		    updated_at = CURRENT_TIMESTAMP
		WHERE singleton_key = 1 AND admin_password_set = 0 AND admin_password = ''
	`, string(hashedPassword))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update password"})
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify password update"})
		return
	}
	if affected != 1 {
		c.JSON(http.StatusConflict, gin.H{"error": "admin password already set"})
		return
	}

	var authGeneration int64
	if err := tx.QueryRowContext(c.Request.Context(), "SELECT auth_generation FROM settings WHERE singleton_key = 1").Scan(&authGeneration); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	token, expiry, err := createAdminSessionWithGeneration(
		c.Request.Context(),
		c,
		tx,
		authGeneration,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create session"})
		return
	}
	if err := tx.Commit(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to commit password setup"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"token":  token,
		"expiry": expiry.Unix(),
	})
}

// GetNodes returns all proxy nodes
func (h *Handler) GetNodes(c *gin.Context) {
	nodes, err := loadAllNodesFrom(c.Request.Context(), h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
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

	node, err := loadNodeByIDFrom(c.Request.Context(), h.db, id)
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
	if err := validateInboundCredentials(req.Username, req.Password, payload.AuthEnabled); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	var requestErr error
	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			var maxOrder int
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sort_order), -1) FROM proxy_nodes").Scan(&maxOrder); err != nil {
				return err
			}
			req.SortOrder = maxOrder + 1

			startPort, preserveInboundPorts, err := getPortSettings(tx)
			if err != nil {
				return err
			}
			usedInboundPorts, err := collectUsedInboundPortsTx(tx, 0)
			if err != nil {
				return err
			}
			if !preserveInboundPorts || req.InboundPort == 0 {
				req.InboundPort, err = nextAvailableInboundPort(startPort, usedInboundPorts)
				if err != nil {
					return err
				}
			}
			if err := validateInboundPort(req.InboundPort); err != nil {
				requestErr = err
				return err
			}
			if _, exists := usedInboundPorts[req.InboundPort]; exists {
				requestErr = fmt.Errorf("inbound port already in use")
				return requestErr
			}
			if err := validateInboundPortAvailable(req.InboundPort); err != nil {
				requestErr = err
				return err
			}

			result, err := tx.ExecContext(ctx, `
				INSERT INTO proxy_nodes (
					name, remark, type, config, inbound_port, inbound_port_pinned,
					username, password, tcp_reuse_enabled, sort_order, latency, enabled
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, req.Name, req.Remark, req.Type, req.Config, req.InboundPort, false,
				req.Username, req.Password, req.TCPReuseEnabled, req.SortOrder, 0, req.Enabled)
			if err != nil {
				return err
			}
			id, err := appdb.LastInsertID(ctx, tx, result, appdb.DialectFor(h.db))
			if err != nil {
				return err
			}
			req.ID = int(id)
			return nil
		},
	})
	if mutationErr != nil {
		if requestErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": requestErr.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
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

	startPort, _, err := getPortSettings(h.db)
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

		inboundPort, err := nextAvailableInboundPort(startPort, usedInboundPorts)
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
		inboundPort, err := nextAvailableInboundPort(startPort, usedInboundPorts)
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

	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(ctx, `
				INSERT INTO proxy_nodes (
					name, remark, type, config, inbound_port, inbound_port_pinned,
					username, password, tcp_reuse_enabled, sort_order, latency, enabled
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`)
			if err != nil {
				return err
			}
			defer stmt.Close()

			for _, candidate := range validCandidates {
				node := &candidate.node
				node.Enabled = req.Enabled
				dbResult, err := stmt.ExecContext(
					ctx,
					node.Name,
					"",
					node.Type,
					node.Config,
					node.InboundPort,
					false,
					node.Username,
					node.Password,
					node.TCPReuseEnabled,
					node.SortOrder,
					0,
					node.Enabled,
				)
				if err != nil {
					return err
				}
				id, err := appdb.LastInsertID(ctx, tx, dbResult, appdb.DialectFor(h.db))
				if err != nil {
					return err
				}
				node.ID = int(id)
			}
			return nil
		},
	})
	if mutationErr != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
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
	if err := validateInboundCredentials(req.Username, req.Password, payload.AuthEnabled); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	req.ID = id

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	var prev models.ProxyNode
	var requestErr error
	var notFound bool
	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			var err error
			prev, err = loadNodeByIDFrom(ctx, tx, id)
			if err != nil {
				if err == sql.ErrNoRows {
					notFound = true
				}
				return err
			}
			if payload.TCPReuseEnabled == nil {
				req.TCPReuseEnabled = prev.TCPReuseEnabled
			}
			req.SortOrder = prev.SortOrder
			req.InboundPortPinned = prev.InboundPortPinned

			startPort, preserveInboundPorts, err := getPortSettings(tx)
			if err != nil {
				return err
			}
			usedInboundPorts, err := collectUsedInboundPortsTx(tx, id)
			if err != nil {
				return err
			}

			switch {
			case preserveInboundPorts && req.InboundPort == 0:
				req.InboundPort, err = nextAvailableInboundPort(startPort, usedInboundPorts)
				if err != nil {
					return err
				}
			case !preserveInboundPorts && !prev.InboundPortPinned:
				req.InboundPort = prev.InboundPort
			case !preserveInboundPorts && prev.InboundPortPinned && req.InboundPort == 0:
				req.InboundPort = prev.InboundPort
			}

			if err := validateInboundPort(req.InboundPort); err != nil {
				requestErr = err
				return err
			}
			if _, exists := usedInboundPorts[req.InboundPort]; exists {
				requestErr = fmt.Errorf("inbound port already in use")
				return requestErr
			}
			if req.InboundPort != prev.InboundPort {
				if err := validateInboundPortAvailable(req.InboundPort); err != nil {
					requestErr = err
					return err
				}
			}

			result, err := tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET name = ?, remark = ?, type = ?, config = ?, inbound_port = ?,
				    username = ?, password = ?, tcp_reuse_enabled = ?, enabled = ?,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, req.Name, req.Remark, req.Type, req.Config, req.InboundPort, req.Username,
				req.Password, req.TCPReuseEnabled, req.Enabled, id)
			if err != nil {
				return err
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if affected != 1 {
				notFound = true
				return sql.ErrNoRows
			}
			if !preserveInboundPorts && !prev.InboundPortPinned {
				if err := reassignAutomaticPortsTx(ctx, tx, startPort); err != nil {
					return err
				}
				if err := tx.QueryRowContext(ctx, "SELECT inbound_port FROM proxy_nodes WHERE id = ?", id).Scan(&req.InboundPort); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if mutationErr != nil {
		if notFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		if requestErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": requestErr.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
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

func (h *Handler) SetNodeInboundPortPinned(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req struct {
		Pinned *bool `json:"pinned"`
	}
	if err := c.BindJSON(&req); err != nil || req.Pinned == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pinned must be a boolean"})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, preserveInboundPorts, err := getPortSettings(h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if preserveInboundPorts {
		c.JSON(http.StatusConflict, gin.H{"error": "port pinning is only available in automatic sorting mode"})
		return
	}

	result, err := h.db.ExecContext(c.Request.Context(), `
		UPDATE proxy_nodes
		SET inbound_port_pinned = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, *req.Pinned, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update port pin"})
		return
	}
	affected, err := result.RowsAffected()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify port pin update"})
		return
	}
	if affected != 1 {
		c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"id":                  id,
		"inbound_port_pinned": *req.Pinned,
	})
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

	var notFound bool
	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			_, err := loadNodeByIDFrom(ctx, tx, id)
			if err != nil {
				if err == sql.ErrNoRows {
					notFound = true
				}
				return err
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_nodes WHERE id = ?", id); err != nil {
				return err
			}
			if err := normalizeNodeSortOrderTx(ctx, tx); err != nil {
				return err
			}
			startPort, preserveInboundPorts, err := getPortSettings(tx)
			if err != nil {
				return err
			}
			if !preserveInboundPorts {
				return reassignAutomaticPortsTx(ctx, tx, startPort)
			}
			return nil
		},
	})
	if mutationErr != nil {
		if notFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
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

	seenIDs := make(map[int]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid node id"})
			return
		}
		if _, duplicate := seenIDs[id]; duplicate {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duplicate node id"})
			return
		}
		seenIDs[id] = struct{}{}
	}

	var missingID int
	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			for _, id := range req.IDs {
				_, err := loadNodeByIDFrom(ctx, tx, id)
				if err != nil {
					if err == sql.ErrNoRows {
						missingID = id
					}
					return err
				}
			}
			for _, id := range req.IDs {
				if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_nodes WHERE id = ?", id); err != nil {
					return err
				}
			}
			if err := normalizeNodeSortOrderTx(ctx, tx); err != nil {
				return err
			}
			startPort, preserveInboundPorts, err := getPortSettings(tx)
			if err != nil {
				return err
			}
			if !preserveInboundPorts {
				return reassignAutomaticPortsTx(ctx, tx, startPort)
			}
			return nil
		},
	})
	if mutationErr != nil {
		if missingID != 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("node %d not found", missingID)})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       "nodes deleted",
		"deleted_count": len(req.IDs),
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

	var snapshots []nodeOrderPortSnapshot
	var requestErr error
	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			var err error
			snapshots, err = snapshotNodeOrderPorts(ctx, tx)
			if err != nil {
				return err
			}
			if len(req.Nodes) != len(snapshots) {
				requestErr = fmt.Errorf("reorder request must contain every node exactly once")
				return requestErr
			}

			currentIDs := make(map[int]struct{}, len(snapshots))
			for _, snapshot := range snapshots {
				currentIDs[snapshot.ID] = struct{}{}
			}
			requestIDs := make(map[int]struct{}, len(req.Nodes))
			sortOrders := make(map[int]struct{}, len(req.Nodes))
			for _, order := range req.Nodes {
				if _, exists := currentIDs[order.ID]; !exists {
					requestErr = fmt.Errorf("reorder request contains unknown node id %d", order.ID)
					return requestErr
				}
				if _, duplicate := requestIDs[order.ID]; duplicate {
					requestErr = fmt.Errorf("reorder request contains duplicate node id %d", order.ID)
					return requestErr
				}
				if order.SortOrder < 0 || order.SortOrder >= len(req.Nodes) {
					requestErr = fmt.Errorf("sort_order must be a complete zero-based permutation")
					return requestErr
				}
				if _, duplicate := sortOrders[order.SortOrder]; duplicate {
					requestErr = fmt.Errorf("sort_order must be unique")
					return requestErr
				}
				requestIDs[order.ID] = struct{}{}
				sortOrders[order.SortOrder] = struct{}{}
			}

			for _, order := range req.Nodes {
				result, err := tx.ExecContext(ctx, `
					UPDATE proxy_nodes
					SET sort_order = ?, updated_at = CURRENT_TIMESTAMP
					WHERE id = ?
				`, order.SortOrder, order.ID)
				if err != nil {
					return err
				}
				affected, err := result.RowsAffected()
				if err != nil {
					return err
				}
				if affected != 1 {
					return fmt.Errorf("node %d disappeared during reorder", order.ID)
				}
			}

			startPort, preserveInboundPorts, err := getPortSettings(tx)
			if err != nil {
				return err
			}
			if !preserveInboundPorts {
				return reassignAutomaticPortsTx(ctx, tx, startPort)
			}
			return nil
		},
	})
	if mutationErr != nil {
		if requestErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": requestErr.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
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
	err = h.db.QueryRowContext(c.Request.Context(), `
		SELECT id, name, inbound_port, username, password, enabled FROM proxy_nodes WHERE id = ?
	`, id).Scan(&node.ID, &nodeName, &node.InboundPort, &node.Username, &node.Password, &node.Enabled)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		fmt.Printf("[API] Failed to load node %d: %v\n", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
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
	ipInfo, err := h.checkProxyIP(c.Request.Context(), proxyAddr, node.Username, node.Password)
	if err != nil {
		fmt.Printf("[API] Failed to check IP for node %d: %v\n", id, err)
		if c.Request.Context().Err() != nil {
			return
		}
		// Clear stale status on failure so UI can show the node as invalid
		if _, clearErr := h.db.ExecContext(c.Request.Context(), `
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
	if c.Request.Context().Err() != nil {
		return
	}

	// Update node with IP info, location, country code, and latency
	_, err = h.db.ExecContext(c.Request.Context(), `
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
		NodeIDs     []int  `json:"node_ids"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		AuthEnabled *bool  `json:"auth_enabled,omitempty"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := validateInboundUsername(req.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateInboundCredentials(req.Username, req.Password, req.AuthEnabled); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.NodeIDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no nodes selected"})
		return
	}
	seen := make(map[int]struct{}, len(req.NodeIDs))
	for _, nodeID := range req.NodeIDs {
		if nodeID <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid node id"})
			return
		}
		if _, duplicate := seen[nodeID]; duplicate {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duplicate node id"})
			return
		}
		seen[nodeID] = struct{}{}
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	var missingID int
	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			for _, nodeID := range req.NodeIDs {
				var existingID int
				if err := tx.QueryRowContext(
					ctx,
					"SELECT id FROM proxy_nodes WHERE id = ?",
					nodeID,
				).Scan(&existingID); err != nil {
					if err == sql.ErrNoRows {
						missingID = nodeID
					}
					return err
				}
			}
			for _, nodeID := range req.NodeIDs {
				if _, err := tx.ExecContext(ctx, `
					UPDATE proxy_nodes
					SET username = ?, password = ?, updated_at = CURRENT_TIMESTAMP
					WHERE id = ?
				`, req.Username, req.Password, nodeID); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if mutationErr != nil {
		if missingID != 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("node %d not found", missingID)})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "authentication updated"})
}

// GetSettings returns current settings
func (h *Handler) GetSettings(c *gin.Context) {
	var settings models.Settings
	err := h.db.QueryRowContext(c.Request.Context(), `
		SELECT id, start_port, preserve_inbound_ports
		FROM settings
		WHERE singleton_key = 1
	`).Scan(&settings.ID, &settings.StartPort, &settings.PreserveInboundPorts)
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

func (h *Handler) GetRuntimeStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.singBoxService.RuntimeStatus())
}

func (h *Handler) RestartRuntime(c *gin.Context) {
	if h.singBoxService == nil || h.db == nil || h.nodeMutations == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sing-box runtime is unavailable"})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	_, err := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(context.Context, *sql.Tx) error {
			return nil
		},
	})
	if err != nil {
		h.singBoxService.MarkDegraded(err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":  err.Error(),
			"status": h.singBoxService.RuntimeStatus(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "sing-box runtime reapplied from database",
		"status":  h.singBoxService.RuntimeStatus(),
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

	var hashedPassword string
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

		hashed, err := bcrypt.GenerateFromPassword([]byte(*req.AdminPassword), bcrypt.DefaultCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to hash password"})
			return
		}
		hashedPassword = string(hashed)
	}

	if req.StartPort != nil {
		if err := validateStartPort(*req.StartPort); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	var previous models.Settings
	if err := h.db.QueryRowContext(c.Request.Context(), `
		SELECT id, singleton_key, admin_password, admin_password_set, auth_generation,
		       start_port, preserve_inbound_ports
		FROM settings
		WHERE singleton_key = 1
	`).Scan(
		&previous.ID,
		&previous.SingletonKey,
		&previous.AdminPassword,
		&previous.AdminPasswordSet,
		&previous.AuthGeneration,
		&previous.StartPort,
		&previous.PreserveInboundPorts,
	); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query settings"})
		return
	}

	nextStartPort := previous.StartPort
	if req.StartPort != nil {
		nextStartPort = *req.StartPort
	}
	nextPreserveInboundPorts := previous.PreserveInboundPorts
	if req.PreserveInboundPorts != nil {
		nextPreserveInboundPorts = *req.PreserveInboundPorts
	}
	portSettingsChanged := nextStartPort != previous.StartPort ||
		nextPreserveInboundPorts != previous.PreserveInboundPorts
	passwordChanged := req.AdminPassword != nil
	if !portSettingsChanged && !passwordChanged {
		c.JSON(http.StatusOK, gin.H{"message": "settings unchanged", "changed": false})
		return
	}
	shouldReassignInboundPorts := !nextPreserveInboundPorts && portSettingsChanged

	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: shouldReassignInboundPorts,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			if passwordChanged {
				if _, err := tx.ExecContext(ctx, `
					UPDATE settings
					SET admin_password = ?, admin_password_set = 1,
					    auth_generation = auth_generation + 1,
					    start_port = ?, preserve_inbound_ports = ?,
					    updated_at = CURRENT_TIMESTAMP
					WHERE singleton_key = 1
				`, hashedPassword, nextStartPort, nextPreserveInboundPorts); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx, `
				UPDATE settings
				SET start_port = ?, preserve_inbound_ports = ?, updated_at = CURRENT_TIMESTAMP
				WHERE singleton_key = 1
			`, nextStartPort, nextPreserveInboundPorts); err != nil {
				return err
			}

			if shouldReassignInboundPorts {
				return reassignAutomaticPortsTx(ctx, tx, nextStartPort)
			}
			return nil
		},
	})
	if mutationErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("failed to update settings consistently: %v", mutationErr),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":          "settings updated",
		"changed":          true,
		"password_changed": passwordChanged,
	})
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

	var previous models.ProxyNode
	var notFound bool
	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			var err error
			previous, err = loadNodeByIDFrom(ctx, tx, id)
			if err != nil {
				if err == sql.ErrNoRows {
					notFound = true
				}
				return err
			}
			nextName := previous.Name
			if updateName {
				nextName = name
			}
			_, err = tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET name = ?, type = ?, config = ?,
				    node_ip = '', location = '', country_code = '', latency = 0,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, nextName, proxyType, string(configJSON), id)
			return err
		},
	})
	if mutationErr != nil {
		if notFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
		return
	}

	node, err := loadNodeByIDFrom(c.Request.Context(), h.db, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, node)
}
