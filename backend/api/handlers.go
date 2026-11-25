package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"sort"
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
	refreshMu                 sync.Mutex
	refreshing                map[int]bool
	fetchSubscription         func(context.Context, []string, services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error)
	db                        *sql.DB
	singBoxService            *services.SingBoxService
	loginLimiter              *loginRateLimiter
	nodeWriteMu               sync.Mutex
	nodeMutations             *nodeMutationCoordinator
	skipPortAvailabilityCheck bool
	checkProxyIP              func(context.Context, string, string, string) (*services.IPInfo, error)
	checkUpstreamIP           func(context.Context, models.ProxyDefinition) (*services.IPInfo, error)
	comparePassword           func([]byte, []byte) error
}

type nodeUpsertRequest struct {
	GroupID         *int    `json:"group_id"`
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
		refreshing: make(map[int]bool), fetchSubscription: services.ExpandBatchImportSourcesWithType,
		db:             db,
		singBoxService: singBoxService,
		loginLimiter:   newLoginRateLimiterFromEnv(),
		nodeMutations:  newNodeMutationCoordinator(db, singBoxService),
		checkProxyIP:   services.CheckProxyIPContext,
		checkUpstreamIP: func(ctx context.Context, definition models.ProxyDefinition) (*services.IPInfo, error) {
			return singBoxService.CheckUpstreamIPContext(ctx, definition, func(ctx context.Context) ([]models.ProxyNode, error) {
				return models.LoadActiveInbounds(ctx, db)
			})
		},
		comparePassword: bcrypt.CompareHashAndPassword,
	}
}

func validateInboundCredentials(username string, password string, authEnabled *bool) error {
	if strings.ContainsAny(username, "\r\n") || strings.ContainsAny(password, "\r\n") {
		return errors.New("inbound credentials must not contain line breaks")
	}
	if strings.Contains(username, "+") {
		return fmt.Errorf("username must not contain '+'")
	}
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

func validateStartPort(port int) error {
	if port < 1024 || port > 65535 {
		return fmt.Errorf("start port out of range")
	}
	return nil
}

type sqlQueryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func getPreservePorts(ctx context.Context, rower sqlQueryRower) (bool, error) {
	var preserveInboundPorts bool
	err := rower.QueryRowContext(ctx, "SELECT preserve_inbound_ports FROM settings WHERE singleton_key=1").Scan(&preserveInboundPorts)
	return preserveInboundPorts, err
}

func collectUsedInboundPortsTx(ctx context.Context, tx *sql.Tx, excludeNodeID int) (map[int]struct{}, error) {
	query := "SELECT inbound_port FROM proxy_nodes WHERE inbound_port IS NOT NULL"
	args := []interface{}{}
	if excludeNodeID > 0 {
		query += " AND id <> ?"
		args = append(args, excludeNodeID)
	}

	rows, err := tx.QueryContext(ctx, query, args...)
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

func (h *Handler) nextBindableInboundPort(startPort int, usedPorts map[int]struct{}) (int, error) {
	return h.nextBindablePort(context.Background(), startPort, usedPorts)
}

func (h *Handler) nextBindablePort(ctx context.Context, startPort int, usedPorts map[int]struct{}) (int, error) {
	if err := validateInboundPort(startPort); err != nil {
		return 0, err
	}
	for startPort <= 65535 {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		port, err := nextAvailableInboundPort(startPort, usedPorts)
		if err != nil {
			return 0, err
		}
		if h.checkPort(ctx, port) == nil {
			return port, nil
		}
		if usedPorts != nil {
			usedPorts[port] = struct{}{}
		}
		startPort = port + 1
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
	if models.IsPolicy(node.Type) {
		if node.UpstreamType != "" || node.UpstreamConfig != "" || (node.UpstreamMode != "" && node.UpstreamMode != "none") {
			return errors.New("policy nodes cannot use an upstream proxy")
		}
		node.UpstreamMode = "none"
		return nil
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
	hasType := node.UpstreamType != ""
	hasConfig := node.UpstreamConfig != ""
	if hasType != hasConfig {
		return fmt.Errorf("upstream type and config must be provided together")
	}
	if mode == models.UpstreamModeCustom && !hasType {
		return fmt.Errorf("custom upstream proxy is required")
	}
	if hasType {
		return h.singBoxService.ValidateUpstreamDefinition(models.ProxyDefinition{
			Type:   node.UpstreamType,
			Config: node.UpstreamConfig,
		})
	}
	return nil
}

func (h *Handler) validateUpdatedNodeUpstream(node *models.ProxyNode, previousMode string) error {
	mode, _ := models.NormalizeUpstreamMode(previousMode)
	return h.validateNodeUpstream(node, mode == models.UpstreamModeLegacy)
}

func clearChangedNodeUpstreamCheck(ctx context.Context, tx *sql.Tx, previous, next models.ProxyNode) error {
	if next.UpstreamMode == previous.UpstreamMode && next.UpstreamType == previous.UpstreamType &&
		next.UpstreamConfig == previous.UpstreamConfig {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE proxy_nodes
		SET upstream_ip = '', upstream_location = '', upstream_country_code = '',
		    upstream_latency = 0, upstream_error = ''
		WHERE id = ?
	`, next.ID)
	return err
}

// singboxUpdateError builds the error payload for a failed config update,
// surfacing the underlying kernel/validator message to the frontend.
func singboxUpdateError(err error) gin.H {
	return gin.H{"error": "failed to update sing-box config: " + services.SafeDiagnostic(err.Error())}
}

// Auth middleware
func (h *Handler) AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestContext := c.Request.Context()
		if requestContext.Err() != nil {
			c.Abort()
			return
		}
		token := normalizeAuthToken(c.GetHeader("Authorization"))
		tokenHash, err := h.verifiedAdminSessionHash(requestContext, token)
		// 页面切换取消的查询不是数据库故障，也不能继续执行受保护操作。
		if requestContext.Err() != nil {
			c.Abort()
			return
		}
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			c.Abort()
			return
		}
		if tokenHash == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			c.Abort()
			return
		}

		c.Set(adminSessionHashKey, tokenHash)
		c.Next()
	}
}

// Login handles admin login
func (h *Handler) Login(c *gin.Context) {
	var req struct {
		Password string `json:"password"`
	}

	if !bindRequestJSON(c, &req) {
		return
	}

	ctx := c.Request.Context()
	ip := c.ClientIP()
	loginPermit, retryAfter, limitErr := h.loginLimiter.Begin(ctx, ip, time.Now())
	if limitErr != nil {
		if errors.Is(limitErr, context.Canceled) || errors.Is(limitErr, context.DeadlineExceeded) {
			return
		}
		if retryAfter > 0 {
			retrySeconds := int((retryAfter + time.Second - 1) / time.Second)
			c.Header("Retry-After", strconv.Itoa(retrySeconds))
		}
		message := "too many login attempts, please try again later"
		if errors.Is(limitErr, errLoginCheckCapacity) {
			message = "login verification is busy, please try again shortly"
		}
		c.JSON(http.StatusTooManyRequests, gin.H{"error": message})
		return
	}
	defer loginPermit.Cancel()

	var adminPassword string
	var authGeneration int64
	err := h.db.QueryRowContext(ctx, `
			SELECT admin_password, auth_generation
			FROM settings
			WHERE singleton_key = 1
		`).Scan(&adminPassword, &authGeneration)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if ctx.Err() != nil {
		return
	}

	if err := h.comparePassword([]byte(adminPassword), []byte(req.Password)); err != nil {
		loginPermit.Failure(time.Now())
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
		return
	}
	loginPermit.Success()

	token, expiry, err := createAdminSessionWithGeneration(ctx, h.db, authGeneration, c.Request.UserAgent(), ip)
	if ctx.Err() != nil {
		return
	}
	if errors.Is(err, errAuthGenerationChanged) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid password"})
		return
	}
	if err != nil {
		_ = c.Error(err)
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
	groupID := 0
	if raw, ok := c.GetQuery("group_id"); ok {
		var err error
		groupID, err = strconv.Atoi(raw)
		if err != nil || groupID <= 0 {
			c.JSON(400, gin.H{"error": "invalid group_id"})
			return
		}
	}
	nodes, err := models.LoadGroupNodes(c.Request.Context(), h.db, groupID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	for index := range nodes {
		nodes[index] = h.singBoxService.WithPolicyStatus(nodes[index])
	}
	c.JSON(http.StatusOK, nodes)
}

// GetNode returns a single proxy node
func (h *Handler) GetNode(c *gin.Context) {
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	node, err := models.LoadProxyNode(c.Request.Context(), h.db, id)
	if err != nil {
		writeNodeReadError(c, err)
		return
	}

	c.JSON(http.StatusOK, h.singBoxService.WithPolicyStatus(node))
}

// CreateNode creates a new proxy node
func (h *Handler) CreateNode(c *gin.Context) {
	req, payload, ok := parseNodeUpsert(c)
	if !ok {
		return
	}
	if payload.UpstreamMode == nil {
		req.UpstreamMode = importedUpstreamMode([]byte(req.Config))
	}
	if err := h.validateNodeUpstream(&req, payload.UpstreamMode == nil); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			group, err := requestGroup(ctx, tx, payload.GroupID)
			if err != nil {
				return err
			}
			req.GroupID = group.ID
			if err := validatePolicyMembers(ctx, tx, &req); err != nil {
				return err
			}
			var maxOrder int
			if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sort_order), -1) FROM proxy_nodes WHERE group_id=?", group.ID).Scan(&maxOrder); err != nil {
				return err
			}
			req.SortOrder = maxOrder + 1

			preserveInboundPorts, err := getPreservePorts(ctx, tx)
			if err != nil {
				return err
			}
			usedInboundPorts, err := collectUsedInboundPortsTx(ctx, tx, 0)
			if err != nil {
				return err
			}
			if !group.AllocatePort {
				req.InboundPort = 0
			} else if !preserveInboundPorts || req.InboundPort == 0 {
				req.InboundPort, err = h.nextBindablePort(ctx, group.StartPort, usedInboundPorts)
				if err != nil {
					return err
				}
			} else {
				if err := validateInboundPort(req.InboundPort); err != nil {
					return invalidNodeRequest(err)
				}
				if _, exists := usedInboundPorts[req.InboundPort]; exists {
					return invalidNodeRequest(errors.New("inbound port already in use"))
				}
				if err := h.checkPort(ctx, req.InboundPort); err != nil {
					return invalidNodeRequest(err)
				}
			}

			result, err := tx.ExecContext(ctx, insertNodeSQL, nodeInsertArgs(req)...)
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
		writeNodeMutationError(c, mutationErr, true)
		return
	}

	h.respondNode(c, req.ID, http.StatusCreated)
}

type batchImportCandidate struct {
	node   models.ProxyNode
	result map[string]interface{}
	link   string
}

// BatchImportNodes imports multiple nodes from share links, subscriptions, or
// structured payloads. Candidate nodes are kernel-validated before any row is
// inserted, so a rejected candidate cannot roll back unrelated valid nodes.
func (h *Handler) BatchImportNodes(c *gin.Context) {
	var req struct {
		GroupID    *int     `json:"group_id"`
		Links      []string `json:"links"`
		Content    string   `json:"content"`
		SourceType string   `json:"source_type"`
		Enabled    bool     `json:"enabled"`
	}

	if !bindRequestJSON(c, &req) {
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
			c.JSON(http.StatusBadRequest, gin.H{
				"error": expandFailures[0].Error, "total": len(results),
				"success": 0, "failed": len(results), "results": results,
			})
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
	group, groupErr := requestGroup(validationCtx, h.db, req.GroupID)
	if groupErr != nil {
		h.nodeWriteMu.Unlock()
		writeNodeMutationError(c, groupErr, false)
		return
	}
	existingNodes, snapshotErr := models.LoadProxyNodes(validationCtx, h.db)
	var runtimeSettings models.Settings
	if snapshotErr == nil {
		runtimeSettings, snapshotErr = models.LoadRuntimeSettings(validationCtx, h.db)
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
	allocator := newBatchImportAllocator(existingNodes, group, true)

	candidates := make([]*batchImportCandidate, 0, len(items))
	for _, item := range items {
		result := map[string]interface{}{"source": item.Source}
		if item.Link != "" {
			result["source"] = item.Type + " share link"
		}
		parsedConfig, proxyType, name := item.Config, item.Type, item.Name

		configJSON, marshalErr := json.Marshal(parsedConfig)
		if marshalErr != nil {
			result["success"] = false
			result["error"] = "failed to marshal config"
			results = append(results, result)
			continue
		}
		username, usernameErr := generateRandomString(12)
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

		inboundPort, err := h.batchPort(group, &allocator)
		if err != nil {
			result["success"] = false
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}

		candidate := &batchImportCandidate{
			node: models.ProxyNode{
				GroupID:         group.ID,
				Name:            name,
				Type:            proxyType,
				Config:          string(configJSON),
				InboundPort:     inboundPort,
				Username:        username,
				Password:        password,
				TCPReuseEnabled: true,
				UpstreamMode:    importedUpstreamMode(configJSON),
				Enabled:         true,
			},
			result: result, link: item.Link,
		}
		if err := validateNodeStorageFields(candidate.node); err != nil {
			result["success"] = false
			result["error"] = err.Error()
			results = append(results, result)
			continue
		}
		allocator.assign(&candidate.node, inboundPort)
		candidates = append(candidates, candidate)
	}

	validCandidates, rejectedCandidates := h.selectValidBatchCandidates(validationCtx, existingNodes, candidates, runtimeSettings)
	for candidate, validationErr := range rejectedCandidates {
		candidate.result["success"] = false
		candidate.result["error"] = fmt.Sprintf("sing-box validation failed: %v", validationErr)
	}

	// Reassign ports and order after isolation so rejected candidates leave no
	// artificial gaps. A final grouped validation covers the exact persisted set.
	portValidCandidates, assignmentsChanged := h.assignBatchImportCandidates(existingNodes, validCandidates, group, true)
	if assignmentsChanged {
		var finalRejected map[*batchImportCandidate]error
		validCandidates, finalRejected = h.selectValidBatchCandidates(validationCtx, existingNodes, portValidCandidates, runtimeSettings)
		for candidate, validationErr := range finalRejected {
			candidate.result["success"] = false
			candidate.result["error"] = fmt.Sprintf("sing-box validation failed: %v", validationErr)
		}
	} else {
		validCandidates = portValidCandidates
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

		currentNodes, currentErr := models.LoadProxyNodes(validationCtx, h.db)
		if currentErr == nil {
			group, currentErr = requestGroup(validationCtx, h.db, &group.ID)
		}
		if currentErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
			return
		}
		validCandidates, _ = h.assignBatchImportCandidates(currentNodes, validCandidates, group, false)
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

	_, mutationErr := h.nodeMutations.execute(validationCtx, nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			stmt, err := tx.PrepareContext(ctx, insertNodeSQL)
			if err != nil {
				return err
			}
			defer stmt.Close()

			for _, candidate := range validCandidates {
				node := &candidate.node
				node.Enabled = req.Enabled
				dbResult, err := stmt.ExecContext(ctx, nodeInsertArgs(*node)...)
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
		if candidate.link != "" {
			candidate.result["source"], candidate.result["link"] = candidate.link, candidate.link
		}
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

type batchImportAllocator struct {
	startPort     int
	nextSortOrder int
	nextID        int
	usedPorts     map[int]struct{}
}

func newBatchImportAllocator(nodes []models.ProxyNode, group models.NodeGroup, assignIDs bool) batchImportAllocator {
	allocator := batchImportAllocator{startPort: group.StartPort, usedPorts: make(map[int]struct{}, len(nodes))}
	if assignIDs {
		allocator.nextID = 1
	}
	for _, node := range nodes {
		if node.GroupID == group.ID && node.SortOrder >= allocator.nextSortOrder {
			allocator.nextSortOrder = node.SortOrder + 1
		}
		if assignIDs && node.ID >= allocator.nextID {
			allocator.nextID = node.ID + 1
		}
		allocator.usedPorts[node.InboundPort] = struct{}{}
	}
	return allocator
}

func (allocator *batchImportAllocator) assign(node *models.ProxyNode, port int) bool {
	changed := node.InboundPort != port || node.SortOrder != allocator.nextSortOrder
	if allocator.nextID != 0 {
		changed = changed || node.ID != allocator.nextID
		node.ID = allocator.nextID
		allocator.nextID++
	}
	node.InboundPort = port
	node.SortOrder = allocator.nextSortOrder
	allocator.nextSortOrder++
	allocator.usedPorts[port] = struct{}{}
	return changed
}

func (h *Handler) batchPort(group models.NodeGroup, allocator *batchImportAllocator) (int, error) {
	if !group.AllocatePort {
		return 0, nil
	}
	port, err := h.nextBindableInboundPort(allocator.startPort, allocator.usedPorts)
	if err == nil {
		allocator.startPort = port + 1
	}
	return port, err
}

func (h *Handler) assignBatchImportCandidates(existingNodes []models.ProxyNode, candidates []*batchImportCandidate, group models.NodeGroup, assignIDs bool) ([]*batchImportCandidate, bool) {
	allocator := newBatchImportAllocator(existingNodes, group, assignIDs)
	accepted := make([]*batchImportCandidate, 0, len(candidates))
	changed := false
	for _, candidate := range candidates {
		port, err := h.batchPort(group, &allocator)
		if err != nil {
			candidate.result["success"] = false
			candidate.result["error"] = err.Error()
			changed = true
			continue
		}
		changed = allocator.assign(&candidate.node, port) || changed
		accepted = append(accepted, candidate)
	}
	return accepted, changed
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
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	req, payload, ok := parseNodeUpsert(c)
	if !ok {
		return
	}

	req.ID = id

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			prev, err := loadNodeForMutation(ctx, tx, id)
			if err != nil {
				return err
			}
			if payload.TCPReuseEnabled == nil {
				req.TCPReuseEnabled = prev.TCPReuseEnabled
			}
			if payload.UpstreamMode == nil && !models.IsPolicy(req.Type) {
				req.UpstreamMode = prev.UpstreamMode
				if err := validateCharacterLimit("upstream_mode", req.UpstreamMode, maxUpstreamModeCharacters); err != nil {
					return invalidNodeRequest(err)
				}
			}
			if payload.UpstreamType == nil && !models.IsPolicy(req.Type) {
				req.UpstreamType = prev.UpstreamType
				if err := validateCharacterLimit("upstream_type", req.UpstreamType, maxUpstreamTypeCharacters); err != nil {
					return invalidNodeRequest(err)
				}
			}
			if payload.UpstreamConfig == nil && !models.IsPolicy(req.Type) {
				req.UpstreamConfig = prev.UpstreamConfig
			}
			if err := h.validateUpdatedNodeUpstream(&req, prev.UpstreamMode); err != nil {
				return invalidNodeRequest(err)
			}
			if err := validatePolicyMembers(ctx, tx, &req); err != nil {
				return err
			}
			req.SortOrder = prev.SortOrder
			req.InboundPortPinned = prev.InboundPortPinned
			req.GroupID = prev.GroupID
			if payload.GroupID != nil && *payload.GroupID != prev.GroupID {
				if err := h.moveNodes(ctx, tx, []int{id}, *payload.GroupID); err != nil {
					return err
				}
				moved, err := loadNodeForMutation(ctx, tx, id)
				if err != nil {
					return err
				}
				req.GroupID, req.SortOrder, req.InboundPortPinned = moved.GroupID, moved.SortOrder, moved.InboundPortPinned
				req.InboundPort = moved.InboundPort
				prev.InboundPort, prev.InboundPortPinned = moved.InboundPort, moved.InboundPortPinned
			}

			group, err := requestGroup(ctx, tx, &req.GroupID)
			if err != nil {
				return err
			}
			preserveInboundPorts, err := getPreservePorts(ctx, tx)
			if err != nil {
				return err
			}
			usedInboundPorts, err := collectUsedInboundPortsTx(ctx, tx, id)
			if err != nil {
				return err
			}

			switch {
			case !group.AllocatePort:
				req.InboundPort = 0
			case preserveInboundPorts && req.InboundPort == 0:
				req.InboundPort, err = h.nextBindablePort(ctx, group.StartPort, usedInboundPorts)
				if err != nil {
					return err
				}
			case !preserveInboundPorts && !prev.InboundPortPinned:
				req.InboundPort = prev.InboundPort
			case !preserveInboundPorts && prev.InboundPortPinned && req.InboundPort == 0:
				req.InboundPort = prev.InboundPort
			}

			if err := validateInboundPort(req.InboundPort); group.AllocatePort && err != nil {
				return invalidNodeRequest(err)
			}
			if _, exists := usedInboundPorts[req.InboundPort]; exists {
				return invalidNodeRequest(errors.New("inbound port already in use"))
			}
			if group.AllocatePort && req.InboundPort != prev.InboundPort {
				if err := h.checkPort(ctx, req.InboundPort); err != nil {
					return invalidNodeRequest(err)
				}
			}

			result, err := tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET name = ?, remark = ?, type = ?, config = ?, inbound_port = ?,
				    username = ?, password = ?, tcp_reuse_enabled = ?, upstream_mode = ?,
				    upstream_type = ?, upstream_config = ?, enabled = ?,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, req.Name, req.Remark, req.Type, req.Config, models.NullablePort(req.InboundPort), req.Username,
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
				return errNodeNotFound
			}
			if err := clearChangedNodeUpstreamCheck(ctx, tx, prev, req); err != nil {
				return err
			}
			if group.AllocatePort && !preserveInboundPorts && !prev.InboundPortPinned {
				if err := h.reassignAutomaticPortsTx(ctx, tx, group); err != nil {
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
		writeNodeMutationError(c, mutationErr, true)
		return
	}

	h.respondNode(c, req.ID, http.StatusOK)
}

// UpdateNodeUpstream updates only a node's managed upstream policy and definition.
func (h *Handler) UpdateNodeUpstream(c *gin.Context) {
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	var req struct {
		Mode   string `json:"upstream_mode"`
		Type   string `json:"upstream_type"`
		Config string `json:"upstream_config"`
	}
	if !bindRequestJSON(c, &req) {
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	candidateNodes, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			previous, err := loadNodeForMutation(ctx, tx, id)
			if err != nil {
				return err
			}
			previous.UpstreamMode, _ = models.NormalizeUpstreamMode(previous.UpstreamMode)
			node := previous
			node.UpstreamMode = req.Mode
			node.UpstreamType = req.Type
			node.UpstreamConfig = req.Config
			if err := validateNodeStorageFields(node); err != nil {
				return invalidNodeRequest(err)
			}
			if err := h.validateNodeUpstream(&node, previous.UpstreamMode == models.UpstreamModeLegacy); err != nil {
				return invalidNodeRequest(err)
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
				return errNodeNotFound
			}
			return clearChangedNodeUpstreamCheck(ctx, tx, previous, node)
		},
	})
	if mutationErr != nil {
		writeNodeMutationError(c, mutationErr, true)
		return
	}

	for _, node := range candidateNodes {
		if node.ID == id {
			c.JSON(http.StatusOK, h.singBoxService.WithPolicyStatus(node))
			return
		}
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "updated node missing from runtime snapshot"})
}

func (h *Handler) UpdateNodeRemark(c *gin.Context) {
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	var req struct {
		Remark string `json:"remark"`
	}
	if !bindRequestJSON(c, &req) {
		return
	}
	if err := validateCharacterLimit("remark", req.Remark, maxNodeRemarkCharacters); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	res, err := h.db.ExecContext(c.Request.Context(), `
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
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	var req struct {
		Pinned *bool `json:"pinned"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Pinned == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pinned must be a boolean"})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	node, err := models.LoadProxyNode(c.Request.Context(), h.db, id)
	if err != nil {
		writeNodeReadError(c, err)
		return
	}
	if node.InboundPort == 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "node has no inbound port"})
		return
	}
	preserveInboundPorts, err := getPreservePorts(c.Request.Context(), h.db)
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
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			return h.deleteNodesTx(ctx, tx, []int{id}, false)
		},
	})
	if mutationErr != nil {
		writeNodeMutationError(c, mutationErr, false)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "node deleted"})
}

// BatchDeleteNodes deletes multiple proxy nodes at once (only restarts sing-box once)
func (h *Handler) BatchDeleteNodes(c *gin.Context) {
	var req struct {
		IDs []int `json:"ids" binding:"required"`
	}

	if !bindRequestJSON(c, &req) {
		return
	}

	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no nodes to delete"})
		return
	}

	if err := validateNodeIDs(req.IDs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			return h.deleteNodesTx(ctx, tx, req.IDs, true)
		},
	})
	if mutationErr != nil {
		writeNodeMutationError(c, mutationErr, false)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":       "nodes deleted",
		"deleted_count": len(req.IDs),
	})
}

// ReorderNodes 更新节点排序；自动模式仅重分配未固定端口，保留模式不改端口。
func (h *Handler) ReorderNodes(c *gin.Context) {
	var req struct {
		GroupID *int `json:"group_id"`
		Nodes   []struct {
			ID        int `json:"id"`
			SortOrder int `json:"sort_order"`
		} `json:"nodes"`
	}

	if !bindRequestJSON(c, &req) {
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			currentNodes, err := models.LoadProxyNodes(ctx, tx)
			if err != nil {
				return err
			}
			currentIDs := map[int]int{}
			if req.GroupID != nil {
				if _, err := requestGroup(ctx, tx, req.GroupID); err != nil {
					return err
				}
			}
			for _, node := range currentNodes {
				if req.GroupID == nil || node.GroupID == *req.GroupID {
					currentIDs[node.ID] = node.GroupID
				}
			}
			if len(req.Nodes) != len(currentIDs) {
				return invalidNodeRequest(errors.New("reorder request must contain every node exactly once"))
			}

			requestIDs := make(map[int]struct{}, len(req.Nodes))
			sortOrders := make(map[int]struct{}, len(req.Nodes))
			for _, order := range req.Nodes {
				if _, exists := currentIDs[order.ID]; !exists {
					return invalidNodeRequest(fmt.Errorf("reorder request contains unknown node id %d", order.ID))
				}
				if _, duplicate := requestIDs[order.ID]; duplicate {
					return invalidNodeRequest(fmt.Errorf("reorder request contains duplicate node id %d", order.ID))
				}
				if order.SortOrder < 0 || order.SortOrder >= len(req.Nodes) {
					return invalidNodeRequest(errors.New("sort_order must be a complete zero-based permutation"))
				}
				if _, duplicate := sortOrders[order.SortOrder]; duplicate {
					return invalidNodeRequest(errors.New("sort_order must be unique"))
				}
				requestIDs[order.ID] = struct{}{}
				sortOrders[order.SortOrder] = struct{}{}
			}

			sort.Slice(req.Nodes, func(left, right int) bool { return req.Nodes[left].SortOrder < req.Nodes[right].SortOrder })
			orders := map[int]int{}
			groups := map[int]bool{}
			for _, order := range req.Nodes {
				groupID := currentIDs[order.ID]
				groups[groupID] = true
				result, err := tx.ExecContext(ctx, `
					UPDATE proxy_nodes
					SET sort_order = ?, updated_at = CURRENT_TIMESTAMP
					WHERE id = ?
				`, orders[groupID], order.ID)
				orders[groupID]++
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

			return h.normalizeGroups(ctx, tx, groups)
		},
	})
	if mutationErr != nil {
		writeNodeMutationError(c, mutationErr, false)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "nodes reordered"})
}

type upstreamCheckOutcome struct {
	info *services.IPInfo
	err  error
}

var errUpstreamDefinitionChanged = errors.New("upstream proxy changed during IP check")

func (h *Handler) runUpstreamCheck(ctx context.Context, definition models.ProxyDefinition) upstreamCheckOutcome {
	info, err := h.checkUpstreamIP(ctx, definition)
	if err == nil && info == nil {
		err = errors.New("upstream IP check returned no result")
	}
	normalizeIPInfoForStorage(info)
	return upstreamCheckOutcome{info: info, err: err}
}

func (outcome upstreamCheckOutcome) storedValues(definition models.ProxyDefinition) (services.IPInfo, string) {
	if outcome.err != nil {
		return services.IPInfo{}, services.NodeIPCheckDiagnostic(outcome.err.Error(), models.ProxyNode{UpstreamConfig: definition.Config})
	}
	info := *outcome.info
	info.HTTPError = services.NodeIPCheckDiagnostic(info.HTTPError, models.ProxyNode{UpstreamConfig: definition.Config})
	return info, ""
}

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

func ipCheckFailureStatus(c *gin.Context, err error) int {
	if services.IsUpstreamValidationError(err) || errors.Is(err, services.ErrConfigRejected) {
		return http.StatusBadRequest
	}
	if !services.IsIPCheckRateLimited(err) {
		return http.StatusBadGateway
	}
	retryAfter := services.IPCheckRetryAfter(err)
	if retryAfter != "" && !strings.ContainsAny(retryAfter, "\r\n") {
		c.Header("Retry-After", retryAfter)
	}
	return http.StatusTooManyRequests
}

type nodeIPCheckResult struct {
	info    services.IPInfo
	err     error
	message string
}

func nodeProbeResult(node models.ProxyNode, scope string, info *services.IPInfo, err error) nodeIPCheckResult {
	if err == nil && (info == nil || strings.TrimSpace(info.IP) == "") {
		err = fmt.Errorf("%s IP check returned no result", scope)
	}
	if err != nil {
		message := services.NodeIPCheckDiagnostic(err.Error(), node)
		if strings.TrimSpace(message) == "" {
			message = scope + " IP check failed"
		}
		return nodeIPCheckResult{err: err, message: message}
	}
	result := nodeIPCheckResult{info: *info}
	normalizeIPInfoForStorage(&result.info)
	result.info.HTTPError = services.NodeIPCheckDiagnostic(result.info.HTTPError, node)
	return result
}

func (result nodeIPCheckResult) payload() gin.H {
	if result.err != nil {
		return gin.H{"error": result.message}
	}
	return ipInfoPayload(&result.info)
}

func (h *Handler) checkNodeExitIP(ctx context.Context, node models.ProxyNode) (*services.IPInfo, error) {
	if node.InboundPort == 0 && models.IsPolicy(node.Type) {
		return h.singBoxService.CheckPolicyIP(ctx, node.ID, h.checkProxyIP)
	}
	if node.InboundPort > 0 {
		return h.checkProxyIP(ctx, fmt.Sprintf("localhost:%d", node.InboundPort), node.Username, node.Password)
	}
	return h.singBoxService.CheckIsolatedNodeIPContext(ctx, node.ID, func(checkCtx context.Context) ([]models.ProxyNode, models.Settings, error) {
		return h.loadIsolatedCheckInputs(checkCtx, node)
	}, h.checkProxyIP)
}

func (h *Handler) loadIsolatedCheckInputs(ctx context.Context, target models.ProxyNode) ([]models.ProxyNode, models.Settings, error) {
	var settings models.Settings
	mode, err := models.NormalizeUpstreamMode(target.UpstreamMode)
	if err != nil {
		return nil, settings, err
	}
	if mode == models.UpstreamModeLegacy {
		settings, err = models.LoadRuntimeSettings(ctx, h.db)
		if err != nil {
			return nil, settings, errors.New("failed to load settings for isolated IP check")
		}
		nodes, err := models.LoadProxyNodes(ctx, h.db)
		if err != nil {
			return nil, settings, errors.New("failed to load dependencies for isolated IP check")
		}
		return nodes, settings, nil
	}
	if mode == models.UpstreamModeGlobal || mode == models.UpstreamModeGroup {
		var group models.GroupUpstream
		err := h.db.QueryRowContext(ctx, "SELECT upstream_enabled,upstream_type,upstream_config FROM node_groups WHERE id=?", target.GroupID).Scan(&group.Enabled, &group.Type, &group.Config)
		if err != nil {
			return nil, settings, errors.New("failed to load group for isolated IP check")
		}
		settings.GroupUpstreams = map[int]models.GroupUpstream{target.GroupID: group}
	}
	if mode == models.UpstreamModeGlobal {
		err := h.db.QueryRowContext(ctx, `SELECT global_upstream_enabled, global_upstream_type, global_upstream_config
			FROM settings WHERE singleton_key = 1`).Scan(&settings.GlobalUpstreamEnabled, &settings.GlobalUpstreamType, &settings.GlobalUpstreamConfig)
		if err != nil {
			return nil, settings, errors.New("failed to load settings for isolated IP check")
		}
	}
	nodes, err := models.LoadActiveInbounds(ctx, h.db)
	if err != nil {
		return nil, settings, err
	}
	return append(nodes, target), settings, nil
}

func (h *Handler) persistNodeIPCheck(ctx context.Context, node models.ProxyNode, status string, checkedAt time.Time, final nodeIPCheckResult, upstream *nodeIPCheckResult) (bool, error) {
	query := `UPDATE proxy_nodes SET check_status = ?, check_error = ?, checked_at = ?,
		node_ip = ?, location = ?, country_code = ?, latency = ?, updated_at = CURRENT_TIMESTAMP`
	args := []any{status, final.message, checkedAt, final.info.IP, final.info.Location, final.info.CountryCode, final.info.Latency}
	condition := ` WHERE id = ? AND check_revision = ? AND enabled`
	matchArgs := []any{node.ID, node.CheckRevision}
	if upstream != nil {
		query += `, upstream_ip = ?, upstream_location = ?, upstream_country_code = ?, upstream_latency = ?, upstream_error = ?`
		args = append(args, upstream.info.IP, upstream.info.Location, upstream.info.CountryCode, upstream.info.Latency, upstream.message)
		condition += ` AND upstream_mode = ? AND upstream_type = ? AND upstream_config = ?`
		matchArgs = append(matchArgs, node.UpstreamMode, node.UpstreamType, node.UpstreamConfig)
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	result, err := h.db.ExecContext(ctx, query+condition, append(args, matchArgs...)...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

// CheckNodeIP checks the final node exit and its custom upstream, when present.
func setIPCheckWriteDeadline(c *gin.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout + 15*time.Second)
	if err := http.NewResponseController(c.Writer).SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		_ = c.Error(err)
	}
}

func (h *Handler) CheckNodeIP(c *gin.Context) {
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	setIPCheckWriteDeadline(c, max(h.singBoxService.UpstreamCheckTimeout(), services.ProxyIPCheckTimeout))
	ctx := c.Request.Context()
	node, err := models.LoadProxyNode(ctx, h.db, id)
	if err != nil {
		writeNodeReadError(c, err)
		return
	}

	if !node.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "node is disabled"})
		return
	}

	var upstreamResult chan nodeIPCheckResult
	if node.UpstreamMode == models.UpstreamModeCustom {
		upstreamResult = make(chan nodeIPCheckResult, 1)
		go func() {
			info, err := h.checkUpstreamIP(ctx, models.ProxyDefinition{Type: node.UpstreamType, Config: node.UpstreamConfig})
			upstreamResult <- nodeProbeResult(node, "upstream", info, err)
		}()
	}

	info, probeErr := h.checkNodeExitIP(ctx, node)
	final := nodeProbeResult(node, "node", info, probeErr)
	var upstream *nodeIPCheckResult
	if upstreamResult != nil {
		select {
		case result := <-upstreamResult:
			upstream = &result
		case <-ctx.Done():
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	status := "ok"
	if final.err != nil {
		status = "failed"
	}
	checkedAt := time.Now().UTC().Truncate(time.Microsecond)
	matched, err := h.persistNodeIPCheck(ctx, node, status, checkedAt, final, upstream)
	if err != nil {
		if ctx.Err() == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save node check result"})
		}
		return
	}
	if !matched {
		message := "node changed during IP check"
		if upstream != nil {
			message = errUpstreamDefinitionChanged.Error()
		}
		c.JSON(http.StatusConflict, gin.H{"error": message})
		return
	}
	payload := final.payload()
	payload["check_status"], payload["check_error"], payload["checked_at"] = status, final.message, checkedAt
	if upstream != nil {
		payload["upstream"] = upstream.payload()
	}
	code := http.StatusOK
	if final.err != nil {
		code = ipCheckFailureStatus(c, final.err)
	}
	c.JSON(code, payload)
}

// BatchSetAuth sets authentication for multiple nodes
func (h *Handler) BatchSetAuth(c *gin.Context) {
	var req struct {
		NodeIDs     []int  `json:"node_ids"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		AuthEnabled *bool  `json:"auth_enabled,omitempty"`
	}

	if !bindRequestJSON(c, &req) {
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
	if err := validateNodeIDs(req.NodeIDs); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			if err := requireNodeIDs(ctx, tx, req.NodeIDs, true); err != nil {
				return err
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
		writeNodeMutationError(c, mutationErr, false)
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "authentication updated"})
}

// GetSettings returns current settings
func (h *Handler) GetSettings(c *gin.Context) {
	settings, err := models.LoadRuntimeSettings(c.Request.Context(), h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"preserve_inbound_ports":       settings.PreserveInboundPorts,
		"global_upstream_enabled":      settings.GlobalUpstreamEnabled,
		"global_upstream_type":         settings.GlobalUpstreamType,
		"global_upstream_config":       settings.GlobalUpstreamConfig,
		"global_upstream_ip":           settings.GlobalUpstreamIP,
		"global_upstream_location":     settings.GlobalUpstreamLocation,
		"global_upstream_country_code": settings.GlobalUpstreamCountryCode,
		"global_upstream_latency":      settings.GlobalUpstreamLatency,
		"global_upstream_error":        settings.GlobalUpstreamError,
		"node_list_column_widths":      settings.NodeListColumnWidths,
	})
}

// CheckGlobalUpstreamIP checks the configured global upstream without changing
// whether it is currently enabled for nodes.
func (h *Handler) CheckGlobalUpstreamIP(c *gin.Context) {
	h.checkSavedUpstreamIP(c, 0)
}

func (h *Handler) CheckGroupUpstreamIP(c *gin.Context) {
	if id, ok := parseNodeID(c, true); ok {
		h.checkSavedUpstreamIP(c, id)
	}
}

func (h *Handler) checkSavedUpstreamIP(c *gin.Context, groupID int) {
	setIPCheckWriteDeadline(c, h.singBoxService.UpstreamCheckTimeout())
	ctx := c.Request.Context()
	id := 1
	read := `SELECT global_upstream_type,global_upstream_config,global_upstream_revision FROM settings WHERE singleton_key=?`
	write := `UPDATE settings SET global_upstream_ip=?,global_upstream_location=?,global_upstream_country_code=?,global_upstream_latency=?,global_upstream_error=?,updated_at=CURRENT_TIMESTAMP
		WHERE singleton_key=? AND global_upstream_type=? AND global_upstream_config=? AND global_upstream_revision=?`
	if groupID != 0 {
		id = groupID
		read = `SELECT upstream_type,upstream_config,upstream_revision FROM node_groups WHERE id=?`
		write = `UPDATE node_groups SET upstream_ip=?,upstream_location=?,upstream_country_code=?,upstream_latency=?,upstream_error=?,updated_at=CURRENT_TIMESTAMP
			WHERE id=? AND upstream_type=? AND upstream_config=? AND upstream_revision=?`
	}
	type savedDefinition struct {
		models.ProxyDefinition
		revision int64
	}
	load := func() (savedDefinition, error) {
		var def savedDefinition
		err := h.db.QueryRowContext(ctx, read, id).Scan(&def.Type, &def.Config, &def.revision)
		return def, err
	}
	definition, err := load()
	if groupID != 0 && errors.Is(err, sql.ErrNoRows) {
		c.JSON(http.StatusNotFound, gin.H{"error": "group not found"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}
	if strings.TrimSpace(definition.Type) == "" || strings.TrimSpace(definition.Config) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "upstream proxy is not configured"})
		return
	}
	outcome := h.runUpstreamCheck(ctx, definition.ProxyDefinition)
	if ctx.Err() != nil {
		return
	}
	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	current, err := load()
	if errors.Is(err, sql.ErrNoRows) || err == nil && current != definition {
		c.JSON(http.StatusConflict, gin.H{"error": errUpstreamDefinitionChanged.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify upstream configuration"})
		return
	}
	info, message := outcome.storedValues(definition.ProxyDefinition)
	result, err := h.db.ExecContext(ctx, write, info.IP, info.Location, info.CountryCode, info.Latency, message, id, definition.Type, definition.Config, definition.revision)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to update upstream check result"})
		return
	}
	if err := verifyConditionalCheckUpdate(result, func() (bool, error) {
		current, err := load()
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return current == definition, err
	}); err != nil {
		if errors.Is(err, errUpstreamDefinitionChanged) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to verify upstream check result"})
		return
	}
	if outcome.err != nil {
		c.JSON(ipCheckFailureStatus(c, outcome.err), gin.H{"error": message})
		return
	}
	c.JSON(http.StatusOK, info)
}

func (h *Handler) GetHealth(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	var singleton int
	if err := h.db.QueryRowContext(ctx, "SELECT singleton_key FROM settings WHERE singleton_key = 1").Scan(&singleton); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy"})
		return
	}
	status := h.singBoxService.RuntimeStatus()
	c.JSON(http.StatusOK, gin.H{
		"status":           "ok",
		"runtime_running":  status.Running,
		"runtime_degraded": status.Degraded,
	})
}

func (h *Handler) GetRuntimeStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.singBoxService.RuntimeStatus())
}

func (h *Handler) RestartRuntime(c *gin.Context) {
	if h.nodeMutations == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sing-box runtime is unavailable"})
		return
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()
	err := h.nodeMutations.reapplyRuntime(c.Request.Context())
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
		AdminPassword         *string         `json:"admin_password,omitempty"`
		PreserveInboundPorts  *bool           `json:"preserve_inbound_ports,omitempty"`
		GlobalUpstreamEnabled *bool           `json:"global_upstream_enabled,omitempty"`
		GlobalUpstreamType    *string         `json:"global_upstream_type,omitempty"`
		GlobalUpstreamConfig  *string         `json:"global_upstream_config,omitempty"`
		NodeListColumnWidths  *map[string]int `json:"node_list_column_widths"`
	}

	const invalidColumnWidths = "node_list_column_widths must be an object with integer widths"
	var decodedColumnWidths map[string]int
	req.NodeListColumnWidths = &decodedColumnWidths
	if err := c.ShouldBindJSON(&req); err != nil {
		message := "invalid request"
		var typeError *json.UnmarshalTypeError
		if errors.As(err, &typeError) && (typeError.Field == "node_list_column_widths" || strings.HasPrefix(typeError.Field, "node_list_column_widths.")) {
			message = invalidColumnWidths
		}
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": message})
		return
	}
	if req.NodeListColumnWidths == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": invalidColumnWidths})
		return
	}
	if decodedColumnWidths == nil {
		req.NodeListColumnWidths = nil
	} else if err := models.ValidateNodeListColumnWidths(decodedColumnWidths); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
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

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	previous, err := models.LoadRuntimeSettings(c.Request.Context(), h.db)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to query settings"})
		return
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
	globalDefinitionRequested := req.GlobalUpstreamType != nil || req.GlobalUpstreamConfig != nil
	onlyColumnWidths := req.NodeListColumnWidths != nil && req.PreserveInboundPorts == nil &&
		req.GlobalUpstreamEnabled == nil && !globalDefinitionRequested && req.AdminPassword == nil
	if !onlyColumnWidths {
		if err := validateCharacterLimit("global_upstream_type", nextGlobalUpstreamType, maxUpstreamTypeCharacters); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
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
	}
	portSettingsChanged := nextPreserveInboundPorts != previous.PreserveInboundPorts
	globalDefinitionChanged := nextGlobalUpstreamType != previous.GlobalUpstreamType ||
		nextGlobalUpstreamConfig != previous.GlobalUpstreamConfig
	globalEnabledChanged := nextGlobalUpstreamEnabled != previous.GlobalUpstreamEnabled
	globalSettingsChanged := globalEnabledChanged || globalDefinitionChanged
	passwordChanged := req.AdminPassword != nil
	nextColumnWidths := previous.NodeListColumnWidths
	columnWidthsChanged := req.NodeListColumnWidths != nil && !maps.Equal(decodedColumnWidths, nextColumnWidths)
	var columnWidthsJSON []byte
	if columnWidthsChanged {
		nextColumnWidths = decodedColumnWidths
		columnWidthsJSON, err = json.Marshal(nextColumnWidths)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to encode node_list_column_widths"})
			return
		}
	}
	if !portSettingsChanged && !globalSettingsChanged && !passwordChanged && !columnWidthsChanged {
		c.JSON(http.StatusOK, gin.H{
			"message": "settings unchanged", "changed": false, "node_list_column_widths": nextColumnWidths,
		})
		return
	}
	shouldReassignInboundPorts := !nextPreserveInboundPorts && portSettingsChanged
	globalRuntimeChanged := globalEnabledChanged || (nextGlobalUpstreamEnabled && globalDefinitionChanged)

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: shouldReassignInboundPorts || globalRuntimeChanged,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			query := `
				UPDATE settings
				SET preserve_inbound_ports = ?,
				    global_upstream_enabled = ?, global_upstream_type = ?,
				    global_upstream_config = ?, updated_at = CURRENT_TIMESTAMP`
			args := []any{nextPreserveInboundPorts, nextGlobalUpstreamEnabled,
				nextGlobalUpstreamType, nextGlobalUpstreamConfig}
			if columnWidthsChanged {
				query += ", node_list_column_widths = ?"
				args = append(args, string(columnWidthsJSON))
			}
			if passwordChanged {
				query += ", admin_password = ?, auth_generation = auth_generation + 1"
				args = append(args, hashedPassword)
			}
			if _, err := tx.ExecContext(ctx, query+" WHERE singleton_key = 1", args...); err != nil {
				return err
			}

			if globalDefinitionChanged {
				if _, err := tx.ExecContext(ctx, `
					UPDATE settings
					SET global_upstream_revision = global_upstream_revision + 1, global_upstream_ip = '', global_upstream_location = '',
					    global_upstream_country_code = '', global_upstream_latency = 0,
					    global_upstream_error = ''
					WHERE singleton_key = 1
				`); err != nil {
					return err
				}
			}

			if shouldReassignInboundPorts {
				return h.normalizeGroups(ctx, tx, nil)
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
		"message":                 "settings updated",
		"changed":                 true,
		"password_changed":        passwordChanged,
		"node_list_column_widths": nextColumnWidths,
	})
}

// Logout revokes the current admin session token.
func (h *Handler) Logout(c *gin.Context) {
	tokenHash := c.GetString(adminSessionHashKey)
	if tokenHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}

	if _, err := h.db.ExecContext(c.Request.Context(), "DELETE FROM admin_sessions WHERE token_hash = ?", tokenHash); err != nil {
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

	if !bindRequestJSON(c, &req) {
		return
	}

	configJSON, proxyType, name, err := parseShareLinkConfig(req.Link)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"type":   proxyType,
		"name":   name,
		"config": string(configJSON),
	})
}

func parseShareLinkConfig(link string) ([]byte, string, string, error) {
	config, proxyType, name, err := services.ParseShareLink(link)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to parse link: %w", err)
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to marshal config: %w", err)
	}
	return configJSON, proxyType, name, nil
}

// ExportNode exports a node as its share link format.
func (h *Handler) ExportNode(c *gin.Context) {
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	node, err := loadNodeForExport(c.Request.Context(), h.db, id)
	if err != nil {
		writeNodeReadError(c, err)
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

	if !bindRequestJSON(c, &req) {
		return
	}

	if len(req.IDs) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no nodes selected"})
		return
	}

	results := make([]map[string]interface{}, 0, len(req.IDs))
	successCount := 0

	for chunk := range slices.Chunk(req.IDs, nodeIDsPerQuery) {
		nodes, loadErr := loadNodeExportChunk(c.Request.Context(), h.db, chunk)
		for _, id := range chunk {
			result := map[string]interface{}{"id": id, "success": false}
			record, found := nodes[id]
			switch {
			case record.err != nil || (!found && loadErr != nil):
				result["error"] = "database error"
			case !found:
				result["error"] = "node not found"
			default:
				link, err := services.BuildShareLink(record.node)
				if err != nil {
					result["error"] = err.Error()
				} else {
					result["success"] = true
					result["name"] = record.node.Name
					result["type"] = record.node.Type
					result["link"] = link
					successCount++
				}
			}
			results = append(results, result)
		}
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
	id, ok := parseNodeID(c, false)
	if !ok {
		return
	}

	var req struct {
		Link       string `json:"link" binding:"required"`
		UpdateName *bool  `json:"update_name,omitempty"`
	}

	if !bindRequestJSON(c, &req) {
		return
	}

	configJSON, proxyType, name, err := parseShareLinkConfig(req.Link)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	updateName := true
	if req.UpdateName != nil {
		updateName = *req.UpdateName
	}

	h.nodeWriteMu.Lock()
	defer h.nodeWriteMu.Unlock()

	_, mutationErr := h.nodeMutations.execute(c.Request.Context(), nodeMutationOperation{
		ApplyRuntime: true,
		ReplacedID:   id,
		Mutate: func(ctx context.Context, tx *sql.Tx) error {
			previous, err := loadNodeForMutation(ctx, tx, id)
			if err != nil {
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
				return invalidNodeRequest(err)
			}
			_, err = tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET name = ?, type = ?, config = ?, upstream_mode = ?,
				    updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, nextName, proxyType, string(configJSON), nextUpstreamMode, id)
			return err
		},
	})
	if mutationErr != nil {
		writeNodeMutationError(c, mutationErr, false)
		return
	}

	node, err := models.LoadProxyNode(c.Request.Context(), h.db, id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	c.JSON(http.StatusOK, h.singBoxService.WithPolicyStatus(node))
}
