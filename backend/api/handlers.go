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
	"unicode/utf8"

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
	nodeMutations   *NodeMutationCoordinator
	checkProxyIP    func(context.Context, string, string, string) (*services.IPInfo, error)
	checkUpstreamIP func(context.Context, models.ProxyDefinition) (*services.IPInfo, error)
	comparePassword func([]byte, []byte) error
}

type nodeUpsertRequest struct {
	Name            string  `json:"name"`
	Remark          string  `json:"remark"`
	Type            string  `json:"type"`
	Config          string  `json:"config"`
	InboundPort     int     `json:"inbound_port"`
	Username        string  `json:"username"`
	Password        string  `json:"password"`
	AuthEnabled     *bool   `json:"auth_enabled,omitempty"`
	Enabled         bool    `json:"enabled"`
	TCPReuseEnabled *bool   `json:"tcp_reuse_enabled"`
	UpstreamMode    *string `json:"upstream_mode,omitempty"`
	UpstreamType    *string `json:"upstream_type,omitempty"`
	UpstreamConfig  *string `json:"upstream_config,omitempty"`
}

const (
	maxNodeNameCharacters               = 255
	maxNodeRemarkCharacters             = 1024
	maxNodeTypeCharacters               = 64
	maxInboundCredentialCharacters      = 255
	maxUpstreamModeCharacters           = 16
	maxUpstreamTypeCharacters           = 64
	maxStoredIPCharacters               = 255
	maxStoredLocationCharacters         = 255
	maxStoredCountryCodeCharacters      = 32
	defaultBatchImportValidationTimeout = 45 * time.Second
	batchImportValidationConcurrency    = 4
)

func batchImportValidationTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("SBPM_BATCH_IMPORT_VALIDATION_TIMEOUT"))
	if raw == "" {
		return defaultBatchImportValidationTimeout
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout < 100*time.Millisecond || timeout > 2*time.Minute {
		return defaultBatchImportValidationTimeout
	}
	return timeout
}

func validateCharacterLimit(field, value string, maximum int) error {
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s must not exceed %d characters", field, maximum)
	}
	return nil
}

func validateNodeStorageFields(node models.ProxyNode) error {
	checks := []struct {
		field   string
		value   string
		maximum int
	}{
		{field: "name", value: node.Name, maximum: maxNodeNameCharacters},
		{field: "remark", value: node.Remark, maximum: maxNodeRemarkCharacters},
		{field: "type", value: node.Type, maximum: maxNodeTypeCharacters},
		{field: "username", value: node.Username, maximum: maxInboundCredentialCharacters},
		{field: "password", value: node.Password, maximum: maxInboundCredentialCharacters},
		{field: "upstream_mode", value: node.UpstreamMode, maximum: maxUpstreamModeCharacters},
		{field: "upstream_type", value: node.UpstreamType, maximum: maxUpstreamTypeCharacters},
	}
	for _, check := range checks {
		if err := validateCharacterLimit(check.field, check.value, check.maximum); err != nil {
			return err
		}
	}
	return nil
}

func truncateCharacters(value string, maximum int) string {
	if maximum <= 0 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func normalizeIPInfoForStorage(info *services.IPInfo) {
	if info == nil {
		return
	}
	info.IP = truncateCharacters(info.IP, maxStoredIPCharacters)
	info.Location = truncateCharacters(info.Location, maxStoredLocationCharacters)
	info.CountryCode = truncateCharacters(info.CountryCode, maxStoredCountryCodeCharacters)
}

func NewHandler(db *sql.DB, singBoxService *services.SingBoxService) *Handler {
	return &Handler{
		db:              db,
		singBoxService:  singBoxService,
		loginLimiter:    newLoginRateLimiterFromEnv(),
		nodeMutations:   NewNodeMutationCoordinator(db, singBoxService),
		checkProxyIP:    services.CheckProxyIPContext,
		checkUpstreamIP: singBoxService.CheckUpstreamIPContext,
		comparePassword: bcrypt.CompareHashAndPassword,
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
		Name:         req.Name,
		Remark:       req.Remark,
		Type:         strings.ToLower(strings.TrimSpace(req.Type)),
		Config:       strings.TrimSpace(req.Config),
		InboundPort:  req.InboundPort,
		Username:     req.Username,
		Password:     req.Password,
		Enabled:      req.Enabled,
		UpstreamMode: models.UpstreamModeNone,
	}
	if req.TCPReuseEnabled != nil {
		node.TCPReuseEnabled = *req.TCPReuseEnabled
	} else {
		node.TCPReuseEnabled = true
	}
	if req.UpstreamMode != nil {
		node.UpstreamMode = *req.UpstreamMode
	}
	if req.UpstreamType != nil {
		node.UpstreamType = *req.UpstreamType
	}
	if req.UpstreamConfig != nil {
		node.UpstreamConfig = *req.UpstreamConfig
	}
	return node
}

func (h *Handler) validateNodeUpstream(node *models.ProxyNode, allowLegacy bool) error {
	if node == nil {
		return fmt.Errorf("proxy node is required")
	}
	mode, err := models.NormalizeUpstreamMode(node.UpstreamMode)
	if err != nil {
		return err
	}
	if mode == models.UpstreamModeLegacy && !allowLegacy {
		return fmt.Errorf("legacy upstream mode is reserved for migrated or imported nodes")
	}
	node.UpstreamMode = mode
	node.UpstreamType = strings.ToLower(strings.TrimSpace(node.UpstreamType))
	node.UpstreamConfig = strings.TrimSpace(node.UpstreamConfig)
	hasType := strings.TrimSpace(node.UpstreamType) != ""
	hasConfig := strings.TrimSpace(node.UpstreamConfig) != ""
	if hasType != hasConfig {
		return fmt.Errorf("upstream type and config must be provided together")
	}
	if mode == models.UpstreamModeCustom && !hasType {
		return fmt.Errorf("custom upstream proxy is required")
	}
	if hasType {
		if h.singBoxService == nil {
			return fmt.Errorf("sing-box service is unavailable")
		}
		return h.singBoxService.ValidateUpstreamDefinition(models.ProxyDefinition{
			Type:   node.UpstreamType,
			Config: node.UpstreamConfig,
		})
	}
	return nil
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
	var loginPermit *loginAttemptPermit
	if h.loginLimiter != nil {
		var retryAfter time.Duration
		var limitErr error
		loginPermit, retryAfter, limitErr = h.loginLimiter.Begin(c.Request.Context(), ip, now)
		if limitErr != nil {
			if errors.Is(limitErr, context.Canceled) || errors.Is(limitErr, context.DeadlineExceeded) {
				return
			}
			if retryAfter > 0 {
				retrySeconds := int((retryAfter + time.Second - 1) / time.Second)
				c.Header("Retry-After", strconv.Itoa(retrySeconds))
			}
			message := "too many login attempts, please try again later"
			if errors.Is(limitErr, ErrLoginCheckCapacity) {
				message = "login verification is busy, please try again shortly"
			}
			c.JSON(http.StatusTooManyRequests, gin.H{"error": message})
			return
		}
		defer loginPermit.Cancel()
	}

	var adminPassword string
	var authGeneration int64
	err := h.db.QueryRowContext(c.Request.Context(), `
			SELECT admin_password, auth_generation
			FROM settings
			WHERE singleton_key = 1
		`).Scan(&adminPassword, &authGeneration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if c.Request.Context().Err() != nil {
		return
	}

	// Compare password using bcrypt only (no plaintext fallback for security)
	comparePassword := h.comparePassword
	if comparePassword == nil {
		comparePassword = bcrypt.CompareHashAndPassword
	}
	if err := comparePassword([]byte(adminPassword), []byte(req.Password)); err != nil {
		if loginPermit != nil {
			loginPermit.Failure(time.Now())
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
		return
	}
	if loginPermit != nil {
		loginPermit.Success()
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
	if err := validateNodeStorageFields(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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
	if err := h.validateNodeUpstream(&req, false); err != nil {
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
					username, password, tcp_reuse_enabled, upstream_mode, upstream_type,
					upstream_config, sort_order, latency, enabled
				)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, req.Name, req.Remark, req.Type, req.Config, req.InboundPort, false,
				req.Username, req.Password, req.TCPReuseEnabled, req.UpstreamMode,
				req.UpstreamType, req.UpstreamConfig, req.SortOrder, 0, req.Enabled)
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
		if services.IsUpstreamValidationError(mutationErr) {
			c.JSON(http.StatusBadRequest, gin.H{"error": mutationErr.Error()})
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

	if c.Request.Context().Err() != nil {
		return
	}
	validationCtx, cancelValidation := context.WithTimeout(
		c.Request.Context(),
		batchImportValidationTimeout(),
	)
	defer cancelValidation()

	h.nodeWriteMu.Lock()
	existingNodes, snapshotErr := loadAllNodesFrom(validationCtx, h.db)
	var runtimeSettings models.Settings
	var startPort int
	if snapshotErr == nil {
		runtimeSettings, snapshotErr = loadRuntimeSettingsFrom(validationCtx, h.db)
	}
	if snapshotErr == nil {
		startPort, _, snapshotErr = getPortSettings(h.db)
	}
	h.nodeWriteMu.Unlock()
	if snapshotErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if err := h.validateNodeSet(validationCtx, existingNodes, runtimeSettings); err != nil {
		c.JSON(http.StatusInternalServerError, singboxUpdateError(fmt.Errorf("existing node set is invalid: %w", err)))
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
				UpstreamMode:    importedUpstreamMode(configJSON),
				SortOrder:       nextOrder,
				Enabled:         true,
			},
			result: result,
		}
		if err := validateNodeStorageFields(candidate.node); err != nil {
			result["success"] = false
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}
		candidates = append(candidates, candidate)
		nextOrder++
		usedInboundPorts[inboundPort] = struct{}{}
	}

	validCandidates, rejectedCandidates := h.selectValidBatchCandidates(validationCtx, existingNodes, candidates, runtimeSettings)
	for candidate, validationErr := range rejectedCandidates {
		candidate.result["success"] = false
		candidate.result["error"] = fmt.Sprintf("sing-box validation failed: %v", validationErr)
	}

	// Reassign ports and order after isolation so rejected candidates leave no
	// artificial gaps. A final grouped validation covers the exact persisted set.
	maxOrder, maxID, usedInboundPorts = batchImportExistingState(existingNodes)
	nextOrder = maxOrder + 1
	portValidCandidates := make([]*batchImportCandidate, 0, len(validCandidates))
	assignmentsChanged := false
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
			assignmentsChanged = true
			err = nil
			continue
		}
		nextID := maxID + len(portValidCandidates) + 1
		if candidate.node.ID != nextID || candidate.node.InboundPort != inboundPort || candidate.node.SortOrder != nextOrder {
			assignmentsChanged = true
		}
		candidate.node.ID = nextID
		candidate.node.InboundPort = inboundPort
		candidate.node.SortOrder = nextOrder
		portValidCandidates = append(portValidCandidates, candidate)
		nextOrder++
		usedInboundPorts[inboundPort] = struct{}{}
	}
	finalRejected := make(map[*batchImportCandidate]error)
	if assignmentsChanged {
		validCandidates, finalRejected = h.selectValidBatchCandidates(validationCtx, existingNodes, portValidCandidates, runtimeSettings)
	} else {
		validCandidates = portValidCandidates
	}
	for candidate, validationErr := range finalRejected {
		candidate.result["success"] = false
		candidate.result["error"] = fmt.Sprintf("sing-box validation failed: %v", validationErr)
	}

	if validationCtx.Err() != nil {
		for _, candidate := range validCandidates {
			candidate.result["success"] = false
			candidate.result["error"] = fmt.Sprintf("sing-box validation failed: %v", validationCtx.Err())
		}
		validCandidates = nil
	}

	if len(validCandidates) > 0 {
		h.nodeWriteMu.Lock()
		defer h.nodeWriteMu.Unlock()

		currentNodes, currentErr := loadAllNodesFrom(validationCtx, h.db)
		var currentStartPort int
		if currentErr == nil {
			currentStartPort, _, currentErr = getPortSettings(h.db)
		}
		if currentErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		maxOrder, maxID, usedInboundPorts = batchImportExistingState(currentNodes)
		nextOrder = maxOrder + 1
		currentPortValid := make([]*batchImportCandidate, 0, len(validCandidates))
		for _, candidate := range validCandidates {
			inboundPort, portErr := nextAvailableInboundPort(currentStartPort, usedInboundPorts)
			if portErr == nil {
				portErr = validateInboundPort(inboundPort)
			}
			if portErr == nil {
				if _, exists := usedInboundPorts[inboundPort]; exists {
					portErr = fmt.Errorf("inbound port already in use")
				}
			}
			if portErr == nil {
				portErr = validateInboundPortAvailable(inboundPort)
			}
			if portErr != nil {
				candidate.result["success"] = false
				candidate.result["error"] = portErr.Error()
				continue
			}
			candidate.node.ID = maxID + len(currentPortValid) + 1
			candidate.node.InboundPort = inboundPort
			candidate.node.SortOrder = nextOrder
			currentPortValid = append(currentPortValid, candidate)
			nextOrder++
			usedInboundPorts[inboundPort] = struct{}{}
		}
		validCandidates = currentPortValid
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

	_, mutationErr := h.nodeMutations.Execute(validationCtx, NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(ctx, `
					INSERT INTO proxy_nodes (
						name, remark, type, config, inbound_port, inbound_port_pinned,
						username, password, tcp_reuse_enabled, upstream_mode, upstream_type,
						upstream_config, sort_order, latency, enabled
					)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
					node.UpstreamMode,
					node.UpstreamType,
					node.UpstreamConfig,
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

func (h *Handler) validateNodeSet(
	ctx context.Context,
	nodes []models.ProxyNode,
	settings models.Settings,
) error {
	configJSON, err := h.singBoxService.BuildGlobalConfigContext(ctx, nodes, settings)
	if err != nil {
		return err
	}
	return h.singBoxService.ValidateConfigContext(ctx, configJSON)
}

func (h *Handler) selectValidBatchCandidates(
	ctx context.Context,
	existing []models.ProxyNode,
	candidates []*batchImportCandidate,
	settings models.Settings,
) ([]*batchImportCandidate, map[*batchImportCandidate]error) {
	rejected := make(map[*batchImportCandidate]error)
	if len(candidates) == 0 {
		return nil, rejected
	}
	if ctx == nil {
		ctx = context.Background()
	}
	allErr := h.validateNodeSet(ctx, appendBatchCandidateNodes(existing, candidates), settings)
	if allErr == nil {
		return append([]*batchImportCandidate(nil), candidates...), rejected
	}
	if len(candidates) == 1 || ctx.Err() != nil {
		for _, candidate := range candidates {
			rejected[candidate] = allErr
		}
		return nil, rejected
	}

	groups := batchImportDependencyGroups(candidates)
	type groupValidationResult struct {
		accepted []*batchImportCandidate
		rejected map[*batchImportCandidate]error
	}
	groupResults := make([]groupValidationResult, len(groups))
	jobs := make(chan int, len(groups))
	for index := range groups {
		jobs <- index
	}
	close(jobs)
	workerCount := min(len(groups), batchImportValidationConcurrency)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				group := groups[index]
				groupErr := h.validateNodeSet(ctx, appendBatchCandidateNodes(existing, group), settings)
				if groupErr == nil {
					groupResults[index].accepted = append([]*batchImportCandidate(nil), group...)
					continue
				}
				if len(group) == 1 {
					groupResults[index].rejected = map[*batchImportCandidate]error{group[0]: groupErr}
					continue
				}
				groupResults[index].accepted, groupResults[index].rejected = h.selectValidDependencyGroup(
					ctx,
					existing,
					nil,
					group,
					settings,
					groupErr,
				)
			}
		}()
	}
	workers.Wait()

	acceptedSet := make(map[*batchImportCandidate]struct{}, len(candidates))
	for _, result := range groupResults {
		for _, candidate := range result.accepted {
			acceptedSet[candidate] = struct{}{}
		}
		for candidate, validationErr := range result.rejected {
			rejected[candidate] = validationErr
		}
	}

	orderedAccepted := orderedBatchCandidates(candidates, acceptedSet)
	if len(orderedAccepted) > 0 && h.validateNodeSet(
		ctx,
		appendBatchCandidateNodes(existing, orderedAccepted),
		settings,
	) != nil {
		acceptedSet = make(map[*batchImportCandidate]struct{}, len(orderedAccepted))
		selected := make([]*batchImportCandidate, 0, len(orderedAccepted))
		for _, result := range groupResults {
			group := result.accepted
			if len(group) == 0 {
				continue
			}
			trial := append(append([]*batchImportCandidate(nil), selected...), group...)
			groupErr := h.validateNodeSet(ctx, appendBatchCandidateNodes(existing, trial), settings)
			if groupErr == nil {
				selected = trial
				for _, candidate := range group {
					acceptedSet[candidate] = struct{}{}
				}
				continue
			}
			groupAccepted, groupRejected := h.selectValidDependencyGroup(
				ctx,
				existing,
				selected,
				group,
				settings,
				groupErr,
			)
			selected = append(selected, groupAccepted...)
			for _, candidate := range groupAccepted {
				acceptedSet[candidate] = struct{}{}
			}
			for candidate, validationErr := range groupRejected {
				rejected[candidate] = validationErr
			}
		}
		orderedAccepted = orderedBatchCandidates(candidates, acceptedSet)
	}

	for _, candidate := range candidates {
		if _, accepted := acceptedSet[candidate]; !accepted {
			if _, alreadyRejected := rejected[candidate]; !alreadyRejected {
				rejected[candidate] = allErr
			}
		}
	}
	return orderedAccepted, rejected
}

func orderedBatchCandidates(
	candidates []*batchImportCandidate,
	accepted map[*batchImportCandidate]struct{},
) []*batchImportCandidate {
	ordered := make([]*batchImportCandidate, 0, len(accepted))
	for _, candidate := range candidates {
		if _, ok := accepted[candidate]; ok {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}

func (h *Handler) selectValidDependencyGroup(
	ctx context.Context,
	existing []models.ProxyNode,
	accepted []*batchImportCandidate,
	group []*batchImportCandidate,
	settings models.Settings,
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
			if err := h.validateNodeSet(ctx, appendBatchCandidateNodes(existing, trial), settings); err != nil {
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

func importedUpstreamMode(configJSON []byte) string {
	var config struct {
		Detour string `json:"detour"`
	}
	if json.Unmarshal(configJSON, &config) == nil && strings.TrimSpace(config.Detour) != "" {
		return models.UpstreamModeLegacy
	}
	return models.UpstreamModeNone
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
	if err := validateNodeStorageFields(req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

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
	var upstreamChanged bool
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
			if payload.UpstreamMode == nil {
				req.UpstreamMode = prev.UpstreamMode
			}
			if payload.UpstreamType == nil {
				req.UpstreamType = prev.UpstreamType
			}
			if payload.UpstreamConfig == nil {
				req.UpstreamConfig = prev.UpstreamConfig
			}
			if err := validateNodeStorageFields(req); err != nil {
				requestErr = err
				return err
			}
			previousMode, _ := models.NormalizeUpstreamMode(prev.UpstreamMode)
			nextMode, _ := models.NormalizeUpstreamMode(req.UpstreamMode)
			allowLegacy := previousMode == models.UpstreamModeLegacy && nextMode == models.UpstreamModeLegacy
			if err := h.validateNodeUpstream(&req, allowLegacy); err != nil {
				requestErr = err
				return err
			}
			upstreamChanged = req.UpstreamMode != prev.UpstreamMode ||
				req.UpstreamType != prev.UpstreamType ||
				req.UpstreamConfig != prev.UpstreamConfig
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
				    username = ?, password = ?, tcp_reuse_enabled = ?, upstream_mode = ?,
				    upstream_type = ?, upstream_config = ?, enabled = ?,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, req.Name, req.Remark, req.Type, req.Config, req.InboundPort, req.Username,
				req.Password, req.TCPReuseEnabled, req.UpstreamMode, req.UpstreamType,
				req.UpstreamConfig, req.Enabled, id)
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
			if upstreamChanged {
				if _, err := tx.ExecContext(ctx, `
					UPDATE proxy_nodes
					SET upstream_ip = '', upstream_location = '', upstream_country_code = '',
					    upstream_latency = 0, upstream_error = ''
					WHERE id = ?
				`, id); err != nil {
					return err
				}
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
		if services.IsUpstreamValidationError(mutationErr) {
			c.JSON(http.StatusBadRequest, gin.H{"error": mutationErr.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
		return
	}

	c.JSON(http.StatusOK, req)
}

// UpdateNodeUpstream updates only a node's managed upstream policy and definition.
func (h *Handler) UpdateNodeUpstream(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var req struct {
		Mode   string `json:"upstream_mode"`
		Type   string `json:"upstream_type"`
		Config string `json:"upstream_config"`
	}
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	var requestErr error
	var notFound bool
	candidateNodes, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			node, err := loadNodeByIDFrom(ctx, tx, id)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					notFound = true
				}
				return err
			}
			previousMode, _ := models.NormalizeUpstreamMode(node.UpstreamMode)
			previousType := node.UpstreamType
			previousConfig := node.UpstreamConfig
			node.UpstreamMode = req.Mode
			node.UpstreamType = req.Type
			node.UpstreamConfig = req.Config
			if err := validateNodeStorageFields(node); err != nil {
				requestErr = err
				return err
			}
			nextMode, _ := models.NormalizeUpstreamMode(node.UpstreamMode)
			allowLegacy := previousMode == models.UpstreamModeLegacy && nextMode == models.UpstreamModeLegacy
			if err := h.validateNodeUpstream(&node, allowLegacy); err != nil {
				requestErr = err
				return err
			}

			result, err := tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET upstream_mode = ?, upstream_type = ?, upstream_config = ?,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, node.UpstreamMode, node.UpstreamType, node.UpstreamConfig, id)
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
			if node.UpstreamMode != previousMode || node.UpstreamType != previousType || node.UpstreamConfig != previousConfig {
				if _, err := tx.ExecContext(ctx, `
					UPDATE proxy_nodes
					SET upstream_ip = '', upstream_location = '', upstream_country_code = '',
					    upstream_latency = 0, upstream_error = ''
					WHERE id = ?
				`, id); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if mutationErr != nil {
		switch {
		case notFound:
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
		case requestErr != nil || services.IsUpstreamValidationError(mutationErr):
			c.JSON(http.StatusBadRequest, gin.H{"error": mutationErr.Error()})
		default:
			c.JSON(http.StatusInternalServerError, singboxUpdateError(mutationErr))
		}
		return
	}

	for _, node := range candidateNodes {
		if node.ID == id {
			c.JSON(http.StatusOK, node)
			return
		}
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "updated node missing from runtime snapshot"})
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
	if err := validateCharacterLimit("remark", req.Remark, maxNodeRemarkCharacters); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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

type upstreamCheckOutcome struct {
	info *services.IPInfo
	err  error
}

var errUpstreamDefinitionChanged = errors.New("upstream proxy changed during IP check")

func verifyConditionalCheckUpdate(result sql.Result, targetStillMatches func() (bool, error)) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	if affected > 1 {
		return fmt.Errorf("upstream check update affected %d rows", affected)
	}
	matches, err := targetStillMatches()
	if err != nil {
		return err
	}
	if matches {
		// MySQL reports changed rows rather than matched rows by default. An
		// identical repeated result can therefore produce zero affected rows.
		return nil
	}
	return errUpstreamDefinitionChanged
}

func ipInfoPayload(info *services.IPInfo) gin.H {
	if info == nil {
		return gin.H{}
	}
	return gin.H{
		"ip":           info.IP,
		"country":      info.Country,
		"country_code": info.CountryCode,
		"city":         info.City,
		"region":       info.Region,
		"location":     info.Location,
		"latency":      info.Latency,
		"transport":    info.Transport,
		"http_error":   info.HTTPError,
	}
}

func boundedCheckError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToValidUTF8(err.Error(), "?")
	const maxBytes = 2000
	for len(message) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(message)
		if size <= 0 {
			return message[:maxBytes]
		}
		message = message[:len(message)-size]
	}
	return message
}

func ipCheckFailureStatus(c *gin.Context, err error) int {
	if !services.IsIPCheckRateLimited(err) {
		return http.StatusBadGateway
	}
	retryAfter := services.IPCheckRetryAfter(err)
	if retryAfter != "" && !strings.ContainsAny(retryAfter, "\r\n") {
		c.Header("Retry-After", retryAfter)
	}
	return http.StatusTooManyRequests
}

func (h *Handler) persistNodeUpstreamCheck(
	ctx context.Context,
	id int,
	definition models.ProxyDefinition,
	outcome upstreamCheckOutcome,
) error {
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	var currentMode, currentType, currentConfig string
	err := h.db.QueryRowContext(ctx, `
		SELECT upstream_mode, upstream_type, upstream_config
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(&currentMode, &currentType, &currentConfig)
	if errors.Is(err, sql.ErrNoRows) {
		return errUpstreamDefinitionChanged
	}
	if err != nil {
		return err
	}
	if currentMode != models.UpstreamModeCustom ||
		currentType != definition.Type || currentConfig != definition.Config {
		return errUpstreamDefinitionChanged
	}

	var result sql.Result
	if outcome.err != nil {
		result, err = h.db.ExecContext(ctx, `
			UPDATE proxy_nodes
			SET upstream_ip = '', upstream_location = '', upstream_country_code = '',
			    upstream_latency = 0, upstream_error = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND upstream_mode = ? AND upstream_type = ? AND upstream_config = ?
		`, boundedCheckError(outcome.err), id, models.UpstreamModeCustom, definition.Type, definition.Config)
	} else if outcome.info == nil {
		return fmt.Errorf("upstream IP check returned no result")
	} else {
		result, err = h.db.ExecContext(ctx, `
			UPDATE proxy_nodes
			SET upstream_ip = ?, upstream_location = ?, upstream_country_code = ?,
			    upstream_latency = ?, upstream_error = '', updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND upstream_mode = ? AND upstream_type = ? AND upstream_config = ?
		`, outcome.info.IP, outcome.info.Location, outcome.info.CountryCode, outcome.info.Latency,
			id, models.UpstreamModeCustom, definition.Type, definition.Config)
	}
	if err != nil {
		return err
	}
	return verifyConditionalCheckUpdate(result, func() (bool, error) {
		var currentMode, currentType, currentConfig string
		err := h.db.QueryRowContext(ctx, `
			SELECT upstream_mode, upstream_type, upstream_config
			FROM proxy_nodes WHERE id = ?
		`, id).Scan(&currentMode, &currentType, &currentConfig)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return currentMode == models.UpstreamModeCustom &&
			currentType == definition.Type && currentConfig == definition.Config, nil
	})
}

// CheckNodeIP checks the final node exit and its custom upstream, when present.
func (h *Handler) CheckNodeIP(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}

	var node models.ProxyNode
	err = h.db.QueryRowContext(c.Request.Context(), `
		SELECT id, name, inbound_port, username, password, enabled,
		       upstream_mode, upstream_type, upstream_config
		FROM proxy_nodes WHERE id = ?
	`, id).Scan(
		&node.ID,
		&node.Name,
		&node.InboundPort,
		&node.Username,
		&node.Password,
		&node.Enabled,
		&node.UpstreamMode,
		&node.UpstreamType,
		&node.UpstreamConfig,
	)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			c.JSON(http.StatusNotFound, gin.H{"error": "node not found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if !node.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node is disabled"})
		return
	}

	ctx := c.Request.Context()
	customUpstream := node.UpstreamMode == models.UpstreamModeCustom
	upstreamDefinition := models.ProxyDefinition{Type: node.UpstreamType, Config: node.UpstreamConfig}
	var upstreamResult <-chan upstreamCheckOutcome
	if customUpstream {
		result := make(chan upstreamCheckOutcome, 1)
		upstreamResult = result
		checker := h.checkUpstreamIP
		go func() {
			if checker == nil {
				result <- upstreamCheckOutcome{err: fmt.Errorf("upstream IP checker is unavailable")}
				return
			}
			info, checkErr := checker(ctx, upstreamDefinition)
			if checkErr == nil && info == nil {
				checkErr = fmt.Errorf("upstream IP check returned no result")
			}
			result <- upstreamCheckOutcome{info: info, err: checkErr}
		}()
	}

	proxyAddr := fmt.Sprintf("localhost:%d", node.InboundPort)
	ipInfo, nodeCheckErr := h.checkProxyIP(ctx, proxyAddr, node.Username, node.Password)
	normalizeIPInfoForStorage(ipInfo)
	if nodeCheckErr == nil && ipInfo == nil {
		nodeCheckErr = fmt.Errorf("node IP check returned no result")
	}
	upstreamOutcome := upstreamCheckOutcome{}
	if customUpstream {
		select {
		case upstreamOutcome = <-upstreamResult:
		case <-ctx.Done():
			return
		}
		normalizeIPInfoForStorage(upstreamOutcome.info)
		if err := h.persistNodeUpstreamCheck(ctx, id, upstreamDefinition, upstreamOutcome); err != nil {
			if errors.Is(err, errUpstreamDefinitionChanged) {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update upstream check result"})
			return
		}
	}

	if ctx.Err() != nil {
		return
	}
	if nodeCheckErr != nil {
		if _, clearErr := h.db.ExecContext(ctx, `
			UPDATE proxy_nodes
			SET node_ip = '', location = '', country_code = '', latency = 0, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, id); clearErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to clear node check result"})
			return
		}
		payload := gin.H{"error": boundedCheckError(nodeCheckErr)}
		if customUpstream {
			if upstreamOutcome.err != nil {
				payload["upstream"] = gin.H{"error": boundedCheckError(upstreamOutcome.err)}
			} else {
				payload["upstream"] = ipInfoPayload(upstreamOutcome.info)
			}
		}
		c.JSON(ipCheckFailureStatus(c, nodeCheckErr), payload)
		return
	}

	if _, err := h.db.ExecContext(ctx, `
		UPDATE proxy_nodes
		SET node_ip = ?, location = ?, country_code = ?, latency = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, ipInfo.IP, ipInfo.Location, ipInfo.CountryCode, ipInfo.Latency, id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update node"})
		return
	}

	payload := ipInfoPayload(ipInfo)
	if customUpstream {
		if upstreamOutcome.err != nil {
			payload["upstream"] = gin.H{"error": boundedCheckError(upstreamOutcome.err)}
		} else {
			payload["upstream"] = ipInfoPayload(upstreamOutcome.info)
		}
	}
	c.JSON(http.StatusOK, payload)
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
	if err := validateCharacterLimit("username", req.Username, maxInboundCredentialCharacters); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := validateCharacterLimit("password", req.Password, maxInboundCredentialCharacters); err != nil {
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
	settings, err := loadRuntimeSettingsFrom(c.Request.Context(), h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"start_port":                   settings.StartPort,
		"preserve_inbound_ports":       settings.PreserveInboundPorts,
		"global_upstream_enabled":      settings.GlobalUpstreamEnabled,
		"global_upstream_type":         settings.GlobalUpstreamType,
		"global_upstream_config":       settings.GlobalUpstreamConfig,
		"global_upstream_ip":           settings.GlobalUpstreamIP,
		"global_upstream_location":     settings.GlobalUpstreamLocation,
		"global_upstream_country_code": settings.GlobalUpstreamCountryCode,
		"global_upstream_latency":      settings.GlobalUpstreamLatency,
		"global_upstream_error":        settings.GlobalUpstreamError,
	})
}

// CheckGlobalUpstreamIP checks the configured global upstream without changing
// whether it is currently enabled for nodes.
func (h *Handler) CheckGlobalUpstreamIP(c *gin.Context) {
	settings, err := loadRuntimeSettingsFrom(c.Request.Context(), h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if strings.TrimSpace(settings.GlobalUpstreamType) == "" || strings.TrimSpace(settings.GlobalUpstreamConfig) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "global upstream proxy is not configured"})
		return
	}
	if h.checkUpstreamIP == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "upstream IP checker is unavailable"})
		return
	}

	definition := models.ProxyDefinition{
		Type:   settings.GlobalUpstreamType,
		Config: settings.GlobalUpstreamConfig,
	}
	info, checkErr := h.checkUpstreamIP(c.Request.Context(), definition)
	normalizeIPInfoForStorage(info)
	if c.Request.Context().Err() != nil {
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	var currentType, currentConfig string
	if err := h.db.QueryRowContext(c.Request.Context(), `
		SELECT global_upstream_type, global_upstream_config
		FROM settings WHERE singleton_key = 1
	`).Scan(&currentType, &currentConfig); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify global upstream configuration"})
		return
	}
	if currentType != definition.Type || currentConfig != definition.Config {
		c.JSON(http.StatusConflict, gin.H{"error": errUpstreamDefinitionChanged.Error()})
		return
	}
	if checkErr == nil && info == nil {
		checkErr = fmt.Errorf("upstream IP check returned no result")
	}
	if checkErr != nil {
		message := boundedCheckError(checkErr)
		result, err := h.db.ExecContext(c.Request.Context(), `
			UPDATE settings
			SET global_upstream_ip = '', global_upstream_location = '',
			    global_upstream_country_code = '', global_upstream_latency = 0,
			    global_upstream_error = ?, updated_at = CURRENT_TIMESTAMP
			WHERE singleton_key = 1 AND global_upstream_type = ? AND global_upstream_config = ?
		`, message, definition.Type, definition.Config)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update global upstream check result"})
			return
		}
		if err := h.verifyGlobalUpstreamCheckUpdate(c.Request.Context(), result, definition); err != nil {
			if errors.Is(err, errUpstreamDefinitionChanged) {
				c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify global upstream check result"})
			return
		}
		c.JSON(ipCheckFailureStatus(c, checkErr), gin.H{"error": message})
		return
	}
	result, err := h.db.ExecContext(c.Request.Context(), `
		UPDATE settings
		SET global_upstream_ip = ?, global_upstream_location = ?,
		    global_upstream_country_code = ?, global_upstream_latency = ?,
		    global_upstream_error = '', updated_at = CURRENT_TIMESTAMP
		WHERE singleton_key = 1 AND global_upstream_type = ? AND global_upstream_config = ?
	`, info.IP, info.Location, info.CountryCode, info.Latency, definition.Type, definition.Config)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update global upstream check result"})
		return
	}
	if err := h.verifyGlobalUpstreamCheckUpdate(c.Request.Context(), result, definition); err != nil {
		if errors.Is(err, errUpstreamDefinitionChanged) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify global upstream check result"})
		return
	}
	c.JSON(http.StatusOK, info)
}

func (h *Handler) verifyGlobalUpstreamCheckUpdate(
	ctx context.Context,
	result sql.Result,
	definition models.ProxyDefinition,
) error {
	return verifyConditionalCheckUpdate(result, func() (bool, error) {
		var currentType, currentConfig string
		err := h.db.QueryRowContext(ctx, `
			SELECT global_upstream_type, global_upstream_config
			FROM settings WHERE singleton_key = 1
		`).Scan(&currentType, &currentConfig)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return currentType == definition.Type && currentConfig == definition.Config, nil
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
		StartPort             *int    `json:"start_port,omitempty"`
		AdminPassword         *string `json:"admin_password,omitempty"`
		PreserveInboundPorts  *bool   `json:"preserve_inbound_ports,omitempty"`
		GlobalUpstreamEnabled *bool   `json:"global_upstream_enabled,omitempty"`
		GlobalUpstreamType    *string `json:"global_upstream_type,omitempty"`
		GlobalUpstreamConfig  *string `json:"global_upstream_config,omitempty"`
	}

	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	var hashedPassword string
	if req.AdminPassword != nil {
		if strings.TrimSpace(*req.AdminPassword) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password is required"})
			return
		}
		if len([]rune(*req.AdminPassword)) < 8 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "password must be at least 8 characters"})
			return
		}
		if len([]byte(*req.AdminPassword)) > models.BcryptMaxPasswordBytes {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("password must not exceed %d bytes", models.BcryptMaxPasswordBytes)})
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

	previous, err := loadRuntimeSettingsFrom(c.Request.Context(), h.db)
	if err != nil {
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
	nextGlobalUpstreamEnabled := previous.GlobalUpstreamEnabled
	if req.GlobalUpstreamEnabled != nil {
		nextGlobalUpstreamEnabled = *req.GlobalUpstreamEnabled
	}
	nextGlobalUpstreamType := previous.GlobalUpstreamType
	if req.GlobalUpstreamType != nil {
		nextGlobalUpstreamType = strings.ToLower(strings.TrimSpace(*req.GlobalUpstreamType))
	}
	nextGlobalUpstreamConfig := previous.GlobalUpstreamConfig
	if req.GlobalUpstreamConfig != nil {
		nextGlobalUpstreamConfig = strings.TrimSpace(*req.GlobalUpstreamConfig)
	}
	if err := validateCharacterLimit("global_upstream_type", nextGlobalUpstreamType, maxUpstreamTypeCharacters); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	globalDefinitionRequested := req.GlobalUpstreamType != nil || req.GlobalUpstreamConfig != nil
	if nextGlobalUpstreamEnabled || globalDefinitionRequested {
		hasType := nextGlobalUpstreamType != ""
		hasConfig := nextGlobalUpstreamConfig != ""
		if hasType != hasConfig {
			c.JSON(http.StatusBadRequest, gin.H{"error": "global upstream type and config must be provided together"})
			return
		}
		if nextGlobalUpstreamEnabled && !hasType {
			c.JSON(http.StatusBadRequest, gin.H{"error": "global upstream proxy is required when enabled"})
			return
		}
		if hasType {
			if err := h.singBoxService.ValidateUpstreamDefinition(models.ProxyDefinition{
				Type: nextGlobalUpstreamType, Config: nextGlobalUpstreamConfig,
			}); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
	}
	portSettingsChanged := nextStartPort != previous.StartPort ||
		nextPreserveInboundPorts != previous.PreserveInboundPorts
	globalSettingsChanged := nextGlobalUpstreamEnabled != previous.GlobalUpstreamEnabled ||
		nextGlobalUpstreamType != previous.GlobalUpstreamType ||
		nextGlobalUpstreamConfig != previous.GlobalUpstreamConfig
	globalDefinitionChanged := nextGlobalUpstreamType != previous.GlobalUpstreamType ||
		nextGlobalUpstreamConfig != previous.GlobalUpstreamConfig
	passwordChanged := req.AdminPassword != nil
	if !portSettingsChanged && !globalSettingsChanged && !passwordChanged {
		c.JSON(http.StatusOK, gin.H{"message": "settings unchanged", "changed": false})
		return
	}
	shouldReassignInboundPorts := !nextPreserveInboundPorts && portSettingsChanged
	globalRuntimeChanged := nextGlobalUpstreamEnabled != previous.GlobalUpstreamEnabled ||
		(nextGlobalUpstreamEnabled && (nextGlobalUpstreamType != previous.GlobalUpstreamType ||
			nextGlobalUpstreamConfig != previous.GlobalUpstreamConfig))

	_, mutationErr := h.nodeMutations.Execute(c.Request.Context(), NodeMutationOperation{
		ApplyRuntime: shouldReassignInboundPorts || globalRuntimeChanged,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			if passwordChanged {
				if _, err := tx.ExecContext(ctx, `
					UPDATE settings
					SET admin_password = ?,
					    auth_generation = auth_generation + 1,
					    start_port = ?, preserve_inbound_ports = ?,
					    global_upstream_enabled = ?, global_upstream_type = ?,
					    global_upstream_config = ?,
					    updated_at = CURRENT_TIMESTAMP
					WHERE singleton_key = 1
				`, hashedPassword, nextStartPort, nextPreserveInboundPorts,
					nextGlobalUpstreamEnabled, nextGlobalUpstreamType, nextGlobalUpstreamConfig); err != nil {
					return err
				}
			} else if _, err := tx.ExecContext(ctx, `
				UPDATE settings
				SET start_port = ?, preserve_inbound_ports = ?,
				    global_upstream_enabled = ?, global_upstream_type = ?,
				    global_upstream_config = ?, updated_at = CURRENT_TIMESTAMP
				WHERE singleton_key = 1
			`, nextStartPort, nextPreserveInboundPorts, nextGlobalUpstreamEnabled,
				nextGlobalUpstreamType, nextGlobalUpstreamConfig); err != nil {
				return err
			}

			if globalDefinitionChanged {
				if _, err := tx.ExecContext(ctx, `
					UPDATE settings
					SET global_upstream_ip = '', global_upstream_location = '',
					    global_upstream_country_code = '', global_upstream_latency = 0,
					    global_upstream_error = ''
					WHERE singleton_key = 1
				`); err != nil {
					return err
				}
			}

			if shouldReassignInboundPorts {
				return reassignAutomaticPortsTx(ctx, tx, nextStartPort)
			}
			return nil
		},
	})
	if mutationErr != nil {
		if services.IsUpstreamValidationError(mutationErr) {
			c.JSON(http.StatusBadRequest, gin.H{"error": mutationErr.Error()})
			return
		}
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
	var requestErr error
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
			nextUpstreamMode := previous.UpstreamMode
			previousMode, normalizeErr := models.NormalizeUpstreamMode(previous.UpstreamMode)
			if normalizeErr != nil {
				return normalizeErr
			}
			if previousMode == models.UpstreamModeLegacy {
				nextUpstreamMode = importedUpstreamMode(configJSON)
			}
			candidate := previous
			candidate.Name = nextName
			candidate.Type = proxyType
			candidate.Config = string(configJSON)
			candidate.UpstreamMode = nextUpstreamMode
			if err := validateNodeStorageFields(candidate); err != nil {
				requestErr = err
				return err
			}
			_, err = tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET name = ?, type = ?, config = ?, upstream_mode = ?,
				    node_ip = '', location = '', country_code = '', latency = 0,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, nextName, proxyType, string(configJSON), nextUpstreamMode, id)
			return err
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

	node, err := loadNodeByIDFrom(c.Request.Context(), h.db, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, node)
}
