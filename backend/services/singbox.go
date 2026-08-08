package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"sb-proxy/backend/models"
)

type SingBoxService struct {
	configDir string
	process   *exec.Cmd
	logFile   *os.File
	waitCh    chan error
	mu        sync.RWMutex
}

var (
	ErrSingBoxBinaryNotFound      = errors.New("sing-box binary not found")
	ErrSingBoxBinaryNotExecutable = errors.New("sing-box binary is not executable")
)

func (s *SingBoxService) resolveSingBoxBinary() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("SINGBOX_BINARY")); explicit != "" {
		if resolved, err := exec.LookPath(explicit); err == nil {
			return resolved, nil
		}
		if strings.Contains(explicit, "/") || strings.ContainsRune(explicit, os.PathSeparator) {
			info, err := os.Stat(explicit)
			if err == nil && isExecutableBinary(info) {
				return explicit, nil
			}
			if err == nil {
				return "", fmt.Errorf("%w: SINGBOX_BINARY=%s", ErrSingBoxBinaryNotExecutable, explicit)
			}
		}
		return "", fmt.Errorf("%w: SINGBOX_BINARY=%s", ErrSingBoxBinaryNotFound, explicit)
	}

	if pathFromEnv, err := exec.LookPath("sing-box"); err == nil {
		return pathFromEnv, nil
	}

	candidates := []string{
		filepath.Join(s.configDir, "sing-box"),
	}
	if executablePath, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executablePath), "sing-box"))
	}

	nonExecutableCandidates := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && isExecutableBinary(info) {
			return candidate, nil
		}
		if err == nil {
			nonExecutableCandidates = append(nonExecutableCandidates, candidate)
		}
	}

	if len(nonExecutableCandidates) > 0 {
		return "", fmt.Errorf(
			"%w: %s",
			ErrSingBoxBinaryNotExecutable,
			strings.Join(nonExecutableCandidates, ", "),
		)
	}

	return "", fmt.Errorf(
		"%w: set SINGBOX_BINARY or place sing-box in PATH, %s, or executable directory",
		ErrSingBoxBinaryNotFound,
		s.configDir,
	)
}

func isExecutableBinary(info os.FileInfo) bool {
	if info == nil || info.IsDir() {
		return false
	}
	// Windows executability depends on file extension and launcher behavior;
	// keep compatibility by only rejecting directories there.
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}

func NewSingBoxService(configDir string) *SingBoxService {
	return &SingBoxService{
		configDir: configDir,
	}
}

// SingBoxConfig represents sing-box configuration structure
type SingBoxConfig struct {
	Log       LogConfig        `json:"log"`
	DNS       *DNSConfig       `json:"dns,omitempty"`
	Endpoints []EndpointConfig `json:"endpoints,omitempty"`
	Inbounds  []InboundConfig  `json:"inbounds"`
	Outbounds []OutboundConfig `json:"outbounds"`
	Route     RouteConfig      `json:"route,omitempty"`
}

// EndpointConfig represents a sing-box endpoint (sing-box 1.11+). Endpoints are
// protocols that act as both inbound and outbound (currently WireGuard); their
// tags can be referenced by route rules just like outbound tags.
type EndpointConfig struct {
	Type  string                 `json:"type"`
	Tag   string                 `json:"tag"`
	Extra map[string]interface{} `json:"-"`
}

type DNSConfig struct {
	Servers  []DNSServer `json:"servers"`
	Rules    []DNSRule   `json:"rules,omitempty"`
	Strategy string      `json:"strategy,omitempty"`
}

type DNSServer struct {
	Type    string `json:"type,omitempty"`
	Tag     string `json:"tag"`
	Address string `json:"address,omitempty"`
	Detour  string `json:"detour,omitempty"`
}

type DNSRule struct {
	Server   string   `json:"server"`
	Outbound []string `json:"outbound,omitempty"`
}

type LogConfig struct {
	Level     string `json:"level"`
	Timestamp bool   `json:"timestamp"`
}

type InboundConfig struct {
	Type   string                 `json:"type"`
	Tag    string                 `json:"tag"`
	Listen string                 `json:"listen"`
	Port   int                    `json:"listen_port"`
	Users  []InboundUser          `json:"users,omitempty"`
	Extra  map[string]interface{} `json:"-"`
}

type InboundUser struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type OutboundConfig struct {
	Type   string                 `json:"type"`
	Tag    string                 `json:"tag"`
	Server string                 `json:"server,omitempty"`
	Port   int                    `json:"server_port,omitempty"`
	Extra  map[string]interface{} `json:"-"`
}

type RouteConfig struct {
	Rules []RouteRule `json:"rules,omitempty"`
	Final string      `json:"final,omitempty"`
}

type RouteRule struct {
	Inbound  []string               `json:"inbound,omitempty"`
	Outbound string                 `json:"outbound,omitempty"`
	Action   string                 `json:"action,omitempty"`
	Strategy string                 `json:"strategy,omitempty"`
	Extra    map[string]interface{} `json:"-"`
}

type tcpReuseRoute struct {
	AuthUser    string
	Password    string
	OutboundTag string
}

type parsedEnabledNode struct {
	Node        *models.ProxyNode
	Config      interface{}
	InboundTag  string
	OutboundTag string
}

// BuildGlobalConfig renders the unified sing-box configuration for all enabled
// nodes and returns it as JSON without touching the filesystem or the running
// process, so callers can validate it before applying.
func (s *SingBoxService) BuildGlobalConfig(nodes []models.ProxyNode) ([]byte, error) {
	config := SingBoxConfig{
		Log: LogConfig{
			Level:     "info",
			Timestamp: true,
		},
		Inbounds:  []InboundConfig{},
		Outbounds: []OutboundConfig{},
		Route: RouteConfig{
			Rules: []RouteRule{},
			Final: "direct",
		},
	}

	parsedNodes, needsLocalDNS, err := prepareEnabledNodes(nodes)
	if err != nil {
		return nil, err
	}
	if needsLocalDNS {
		config.DNS = &DNSConfig{
			Servers: []DNSServer{{Type: "local", Tag: "local"}},
		}
	}

	reuseRoutes := make([]tcpReuseRoute, 0, len(parsedNodes))
	reuseRouteSet := make(map[string]struct{}, len(parsedNodes))
	for _, parsedNode := range parsedNodes {
		node := parsedNode.Node
		if !node.TCPReuseEnabled || node.Username == "" || node.Password == "" {
			continue
		}
		authUser := fmt.Sprintf("%s+%d", node.Username, node.InboundPort)
		if _, exists := reuseRouteSet[authUser]; exists {
			return nil, fmt.Errorf("duplicate tcp reuse auth user %q", authUser)
		}
		reuseRouteSet[authUser] = struct{}{}
		reuseRoutes = append(reuseRoutes, tcpReuseRoute{
			AuthUser:    authUser,
			Password:    node.Password,
			OutboundTag: parsedNode.OutboundTag,
		})
	}

	directInboundRoutes := make([]RouteRule, 0, len(parsedNodes))

	// Generate inbounds and outbounds for each enabled node.
	for _, parsedNode := range parsedNodes {
		node := parsedNode.Node
		inboundTag := parsedNode.InboundTag
		outboundTag := parsedNode.OutboundTag

		inbound := InboundConfig{
			Type:   "mixed",
			Tag:    inboundTag,
			Listen: "::",
			Port:   node.InboundPort,
			Extra: map[string]interface{}{
				"sniff":                      true,
				"sniff_override_destination": true,
			},
		}

		hasInboundAuth := node.Username != "" && node.Password != ""
		if hasInboundAuth {
			inbound.Users = append(inbound.Users, InboundUser{
				Username: node.Username,
				Password: node.Password,
			})
			for _, route := range reuseRoutes {
				inbound.Users = append(inbound.Users, InboundUser{
					Username: route.AuthUser,
					Password: route.Password,
				})
			}
		}

		config.Inbounds = append(config.Inbounds, inbound)

		// WireGuard is generated as an endpoint (sing-box 1.11+ format): the
		// legacy wireguard outbound is rejected by sing-box 1.12+ at startup
		// and removed in 1.13. Endpoint tags remain routable like outbounds.
		if node.Type == "wireguard" {
			wireGuardConfig, ok := parsedNode.Config.(*models.WireGuardConfig)
			if !ok {
				return nil, fmt.Errorf("failed to generate endpoint for node %d: unexpected config type %T", node.ID, parsedNode.Config)
			}
			endpoint, err := s.generateWireGuardEndpoint(wireGuardConfig, outboundTag)
			if err != nil {
				return nil, fmt.Errorf("failed to generate endpoint for node %d: %v", node.ID, err)
			}
			config.Endpoints = append(config.Endpoints, endpoint)
		} else {
			outbound, err := s.generateOutboundFromConfig(node.Type, parsedNode.Config, outboundTag)
			if err != nil {
				return nil, fmt.Errorf("failed to generate outbound for node %d: %v", node.ID, err)
			}
			config.Outbounds = append(config.Outbounds, outbound)
		}
		directInboundRoutes = append(directInboundRoutes, RouteRule{
			Inbound:  []string{inboundTag},
			Outbound: outboundTag,
		})
	}

	// Route username+route-number users first so they can override inbound->outbound defaults.
	for _, route := range reuseRoutes {
		config.Route.Rules = append(config.Route.Rules, RouteRule{
			Outbound: route.OutboundTag,
			Extra: map[string]interface{}{
				"auth_user": []string{route.AuthUser},
			},
		})
	}
	config.Route.Rules = append(config.Route.Rules, directInboundRoutes...)

	// Add direct outbound.
	config.Outbounds = append(config.Outbounds, OutboundConfig{
		Type: "direct",
		Tag:  "direct",
	})

	return s.marshalConfig(config)
}

// GenerateGlobalConfig renders and writes the unified configuration file for
// all enabled nodes. It performs no kernel validation; use BuildGlobalConfig +
// ValidateConfig + ApplyConfig when replacing the config of a running service.
func (s *SingBoxService) GenerateGlobalConfig(nodes []models.ProxyNode) error {
	configJSON, err := s.BuildGlobalConfig(nodes)
	if err != nil {
		return err
	}
	return s.writeConfigFile(configJSON)
}

func (s *SingBoxService) configPath() string {
	return filepath.Join(s.configDir, "config.json")
}

func (s *SingBoxService) lastGoodConfigPath() string {
	return filepath.Join(s.configDir, "config.json.last-good")
}

// writeConfigFile writes config.json atomically (temp file + rename) so an
// interrupted write can never leave a truncated config behind.
func (s *SingBoxService) writeConfigFile(configJSON []byte) error {
	tmpPath := s.configPath() + ".tmp"
	if err := os.WriteFile(tmpPath, configJSON, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.configPath())
}

// ValidateConfig runs `sing-box check` against a candidate configuration
// without touching the running process or the live config file. On rejection
// the kernel's own error output is returned so the caller can surface it.
func (s *SingBoxService) ValidateConfig(configJSON []byte) error {
	singBoxBinary, err := s.resolveSingBoxBinary()
	if err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(s.configDir, "config.check-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmpFile.Write(configJSON); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}

	output, err := exec.Command(singBoxBinary, "check", "-c", tmpPath).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("sing-box rejected the generated config: %s", detail)
	}
	return nil
}

// ApplyConfig atomically writes an already-validated configuration and
// restarts sing-box with it. If the new config still fails to start (for
// example an inbound port was taken by another program), it rolls back to the
// last known-good configuration so existing nodes keep working.
func (s *SingBoxService) ApplyConfig(configJSON []byte) error {
	if err := s.writeConfigFile(configJSON); err != nil {
		return err
	}
	if err := s.Restart(); err != nil {
		if rollbackErr := s.rollbackToLastGood(); rollbackErr != nil {
			return fmt.Errorf("sing-box failed to start with the new config: %v (rollback also failed: %v)", err, rollbackErr)
		}
		return fmt.Errorf("sing-box failed to start with the new config, rolled back to the previous working config: %v", err)
	}
	return nil
}

// rollbackToLastGood restores the last configuration that started successfully
// and brings sing-box back up with it.
func (s *SingBoxService) rollbackToLastGood() error {
	lastGood, err := os.ReadFile(s.lastGoodConfigPath())
	if err != nil {
		return fmt.Errorf("no last-good config available: %w", err)
	}
	if err := s.writeConfigFile(lastGood); err != nil {
		return err
	}
	return s.Start()
}

// saveLastGoodConfig snapshots the configuration that just started
// successfully so ApplyConfig can roll back to it later.
func (s *SingBoxService) saveLastGoodConfig() {
	configJSON, err := os.ReadFile(s.configPath())
	if err != nil {
		fmt.Printf("Warning: failed to read config for last-good snapshot: %v\n", err)
		return
	}
	tmpPath := s.lastGoodConfigPath() + ".tmp"
	if err := os.WriteFile(tmpPath, configJSON, 0644); err != nil {
		fmt.Printf("Warning: failed to save last-good config: %v\n", err)
		return
	}
	if err := os.Rename(tmpPath, s.lastGoodConfigPath()); err != nil {
		fmt.Printf("Warning: failed to save last-good config: %v\n", err)
	}
}

func (s *SingBoxService) marshalConfig(config SingBoxConfig) ([]byte, error) {
	// Custom marshaling to handle Extra fields
	type Alias SingBoxConfig

	// Convert to map for custom marshaling
	data, err := json.Marshal((Alias)(config))
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}

	// Merge Extra fields for inbounds
	if inbounds, ok := result["inbounds"].([]interface{}); ok {
		for i, inbound := range inbounds {
			if inboundMap, ok := inbound.(map[string]interface{}); ok {
				if i < len(config.Inbounds) && config.Inbounds[i].Extra != nil {
					for k, v := range config.Inbounds[i].Extra {
						inboundMap[k] = v
					}
				}
			}
		}
	}

	// Merge Extra fields for outbounds
	if outbounds, ok := result["outbounds"].([]interface{}); ok {
		for i, outbound := range outbounds {
			if outboundMap, ok := outbound.(map[string]interface{}); ok {
				if i < len(config.Outbounds) && config.Outbounds[i].Extra != nil {
					for k, v := range config.Outbounds[i].Extra {
						outboundMap[k] = v
					}
				}
			}
		}
	}

	// Merge Extra fields for endpoints
	if endpoints, ok := result["endpoints"].([]interface{}); ok {
		for i, endpoint := range endpoints {
			if endpointMap, ok := endpoint.(map[string]interface{}); ok {
				if i < len(config.Endpoints) && config.Endpoints[i].Extra != nil {
					for k, v := range config.Endpoints[i].Extra {
						endpointMap[k] = v
					}
				}
			}
		}
	}

	// Merge Extra fields for route rules
	if route, ok := result["route"].(map[string]interface{}); ok {
		if rules, ok := route["rules"].([]interface{}); ok {
			for i, rule := range rules {
				if ruleMap, ok := rule.(map[string]interface{}); ok {
					if i < len(config.Route.Rules) && config.Route.Rules[i].Extra != nil {
						for k, v := range config.Route.Rules[i].Extra {
							ruleMap[k] = v
						}
					}
				}
			}
		}
	}

	return json.MarshalIndent(result, "", "  ")
}

func prepareEnabledNodes(nodes []models.ProxyNode) ([]parsedEnabledNode, bool, error) {
	parsedNodes := make([]parsedEnabledNode, 0, len(nodes))
	nameToIndices := make(map[string][]int, len(nodes))
	tagToIndex := make(map[string]int, len(nodes))
	idToIndex := make(map[int]int, len(nodes))

	for i := range nodes {
		node := &nodes[i]
		if !node.Enabled {
			continue
		}
		if strings.Contains(node.Username, "+") {
			return nil, false, fmt.Errorf("node %d username must not contain '+'", node.ID)
		}
		if previous, exists := idToIndex[node.ID]; exists {
			return nil, false, fmt.Errorf("duplicate enabled node id %d (nodes %q and %q)", node.ID, parsedNodes[previous].Node.Name, node.Name)
		}

		parsedConfig, err := node.ParseConfig()
		if err != nil {
			return nil, false, fmt.Errorf("failed to parse config for node %d: %w", node.ID, err)
		}
		parsedIndex := len(parsedNodes)
		parsedNodes = append(parsedNodes, parsedEnabledNode{
			Node:        node,
			Config:      parsedConfig,
			InboundTag:  fmt.Sprintf("node-%d-in", node.ID),
			OutboundTag: fmt.Sprintf("node-%d-out", node.ID),
		})
		idToIndex[node.ID] = parsedIndex

		tagToIndex[parsedNodes[parsedIndex].OutboundTag] = parsedIndex
		if name := strings.TrimSpace(node.Name); name != "" {
			nameToIndices[name] = append(nameToIndices[name], parsedIndex)
		}
	}

	detourEdges := make(map[int]int)
	needsLocalDNS := false
	for index := range parsedNodes {
		parsedNode := &parsedNodes[index]
		detour := strings.TrimSpace(configDetour(parsedNode.Config))
		if detour != "" {
			if detour == "direct" {
				setConfigDetour(parsedNode.Config, "direct")
				// "direct" is a reserved built-in tag, even if a node has that name.
			} else {
				targetIndex, exists := tagToIndex[detour]
				if !exists {
					nameMatches := nameToIndices[detour]
					if len(nameMatches) > 1 {
						matchingIDs := make([]string, 0, len(nameMatches))
						for _, matchIndex := range nameMatches {
							matchingIDs = append(matchingIDs, strconv.Itoa(parsedNodes[matchIndex].Node.ID))
						}
						return nil, false, fmt.Errorf("node %d detour name %q is ambiguous across enabled node ids %s", parsedNode.Node.ID, detour, strings.Join(matchingIDs, ", "))
					}
					if len(nameMatches) == 1 {
						targetIndex = nameMatches[0]
						exists = true
					}
				}
				if !exists {
					return nil, false, fmt.Errorf("node %d detour references undefined enabled node %q", parsedNode.Node.ID, detour)
				}
				if targetIndex == index {
					return nil, false, fmt.Errorf("node %d detour must not reference itself (%q)", parsedNode.Node.ID, detour)
				}
				setConfigDetour(parsedNode.Config, parsedNodes[targetIndex].OutboundTag)
				detourEdges[index] = targetIndex
			}
		}

		resolver, err := configDomainResolverValue(parsedNode.Config)
		if err != nil {
			return nil, false, fmt.Errorf("node %d has invalid domain_resolver: %w", parsedNode.Node.ID, err)
		}
		usesLocal, err := validateGeneratedDomainResolver(resolver)
		if err != nil {
			return nil, false, fmt.Errorf("node %d has invalid domain_resolver: %w", parsedNode.Node.ID, err)
		}
		needsLocalDNS = needsLocalDNS || usesLocal
	}

	if err := validateDetourGraph(parsedNodes, detourEdges); err != nil {
		return nil, false, err
	}
	return parsedNodes, needsLocalDNS, nil
}

func dialerOptionsForConfig(config interface{}) *models.DialerOptions {
	switch typed := config.(type) {
	case *models.DirectConfig:
		return &typed.DialerOptions
	case *models.SSConfig:
		return &typed.DialerOptions
	case *models.VLESSConfig:
		return &typed.DialerOptions
	case *models.VMESSConfig:
		return &typed.DialerOptions
	case *models.Hysteria2Config:
		return &typed.DialerOptions
	case *models.TrojanConfig:
		return &typed.DialerOptions
	case *models.TUICConfig:
		return &typed.DialerOptions
	case *models.AnyTLSConfig:
		return &typed.DialerOptions
	case *models.SOCKS5Config:
		return &typed.DialerOptions
	case *models.HTTPProxyConfig:
		return &typed.DialerOptions
	default:
		return nil
	}
}

func configDetour(config interface{}) string {
	if dialer := dialerOptionsForConfig(config); dialer != nil {
		return dialer.Detour
	}
	if wireGuard, ok := config.(*models.WireGuardConfig); ok {
		return wireGuard.Detour
	}
	return ""
}

func setConfigDetour(config interface{}, detour string) {
	if dialer := dialerOptionsForConfig(config); dialer != nil {
		dialer.Detour = detour
		return
	}
	if wireGuard, ok := config.(*models.WireGuardConfig); ok {
		wireGuard.Detour = detour
	}
}

func configDomainResolverValue(config interface{}) (interface{}, error) {
	if dialer := dialerOptionsForConfig(config); dialer != nil {
		return dialer.DomainResolver, nil
	}
	if wireGuard, ok := config.(*models.WireGuardConfig); ok {
		return wireGuardDomainResolverValue(wireGuard)
	}
	return nil, nil
}

func wireGuardDomainResolverValue(config *models.WireGuardConfig) (interface{}, error) {
	if config == nil {
		return nil, nil
	}
	strategy := strings.TrimSpace(config.DomainResolverStrategy)
	compatibility := cloneOptionValue(map[string]interface{}(config.DomainResolverOptions)).(map[string]interface{})

	switch typed := config.DomainResolver.(type) {
	case nil:
		if strategy != "" {
			compatibility["strategy"] = strategy
		}
		if len(compatibility) == 0 {
			return nil, nil
		}
		return compatibility, nil
	case string:
		server := strings.TrimSpace(typed)
		if server == "" {
			// A present empty flat value is an explicit form clear. It must not
			// resurrect an imported domain_resolver_options object.
			return nil, nil
		}
		if len(compatibility) == 0 && strategy == "" {
			return server, nil
		}
		compatibility["server"] = server
		if strategy == "" {
			delete(compatibility, "strategy")
		} else {
			compatibility["strategy"] = strategy
		}
		return compatibility, nil
	default:
		resolver, ok := generatorOptionMap(typed)
		if !ok {
			return nil, fmt.Errorf("unsupported value type %T", config.DomainResolver)
		}
		resolver = mergeGeneratorOptionMaps(compatibility, resolver)
		if strategy != "" {
			resolver["strategy"] = strategy
		}
		if len(resolver) == 0 {
			return nil, nil
		}
		return resolver, nil
	}
}

func validateGeneratedDomainResolver(value interface{}) (bool, error) {
	if value == nil {
		return false, nil
	}
	var server string
	switch typed := value.(type) {
	case string:
		server = strings.TrimSpace(typed)
	case models.NativeOptions:
		return validateGeneratedDomainResolver(map[string]interface{}(typed))
	case map[string]interface{}:
		rawServer, exists := typed["server"]
		if !exists {
			return false, fmt.Errorf("object form requires a non-empty server")
		}
		serverValue, ok := rawServer.(string)
		if !ok {
			return false, fmt.Errorf("server must be a string, got %T", rawServer)
		}
		server = strings.TrimSpace(serverValue)
	default:
		return false, fmt.Errorf("must be a string or object, got %T", value)
	}
	if server == "" {
		return false, nil
	}
	if server != "local" {
		return false, fmt.Errorf("references undefined DNS server tag %q; only the automatically provided local resolver is available", server)
	}
	return true, nil
}

func validateDetourGraph(nodes []parsedEnabledNode, edges map[int]int) error {
	state := make([]uint8, len(nodes))
	stack := make([]int, 0, len(nodes))
	var visit func(int) error
	visit = func(index int) error {
		state[index] = 1
		stack = append(stack, index)
		if target, exists := edges[index]; exists {
			switch state[target] {
			case 0:
				if err := visit(target); err != nil {
					return err
				}
			case 1:
				cycle := make([]string, 0, len(stack)+1)
				start := 0
				for start < len(stack) && stack[start] != target {
					start++
				}
				for _, cycleIndex := range stack[start:] {
					cycle = append(cycle, fmt.Sprintf("%s(id=%d)", nodes[cycleIndex].Node.Name, nodes[cycleIndex].Node.ID))
				}
				cycle = append(cycle, fmt.Sprintf("%s(id=%d)", nodes[target].Node.Name, nodes[target].Node.ID))
				return fmt.Errorf("detour cycle detected: %s", strings.Join(cycle, " -> "))
			}
		}
		stack = stack[:len(stack)-1]
		state[index] = 2
		return nil
	}
	for index := range nodes {
		if state[index] == 0 {
			if err := visit(index); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SingBoxService) generateOutbound(node *models.ProxyNode, tag string) (OutboundConfig, error) {
	parsedConfig, err := node.ParseConfig()
	if err != nil {
		return OutboundConfig{}, err
	}
	return s.generateOutboundFromConfig(node.Type, parsedConfig, tag)
}

func (s *SingBoxService) generateOutboundFromConfig(proxyType string, parsedConfig interface{}, tag string) (OutboundConfig, error) {
	switch proxyType {
	case "direct":
		config, ok := parsedConfig.(*models.DirectConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected direct config type %T", parsedConfig)
		}
		return s.generateDirectOutbound(config, tag)
	case "ss":
		config, ok := parsedConfig.(*models.SSConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected shadowsocks config type %T", parsedConfig)
		}
		return s.generateSSOutbound(config, tag)
	case "vless":
		config, ok := parsedConfig.(*models.VLESSConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected vless config type %T", parsedConfig)
		}
		return s.generateVLESSOutbound(config, tag)
	case "vmess":
		config, ok := parsedConfig.(*models.VMESSConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected vmess config type %T", parsedConfig)
		}
		return s.generateVMESSOutbound(config, tag)
	case "hy2":
		config, ok := parsedConfig.(*models.Hysteria2Config)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected hysteria2 config type %T", parsedConfig)
		}
		return s.generateHysteria2Outbound(config, tag)
	case "tuic":
		config, ok := parsedConfig.(*models.TUICConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected tuic config type %T", parsedConfig)
		}
		return s.generateTUICOutbound(config, tag)
	case "trojan":
		config, ok := parsedConfig.(*models.TrojanConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected trojan config type %T", parsedConfig)
		}
		return s.generateTrojanOutbound(config, tag)
	case "anytls":
		config, ok := parsedConfig.(*models.AnyTLSConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected anytls config type %T", parsedConfig)
		}
		return s.generateAnyTLSOutbound(config, tag)
	case "socks5", "socks5h":
		config, ok := parsedConfig.(*models.SOCKS5Config)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected socks config type %T", parsedConfig)
		}
		return s.generateSOCKS5Outbound(config, tag)
	case "http":
		config, ok := parsedConfig.(*models.HTTPProxyConfig)
		if !ok {
			return OutboundConfig{}, fmt.Errorf("unexpected http config type %T", parsedConfig)
		}
		return s.generateHTTPProxyOutbound(config, tag)
	default:
		return OutboundConfig{}, fmt.Errorf("unsupported proxy type: %s", proxyType)
	}
}

func applyGeneratorDialerOptions(extra map[string]interface{}, options models.DialerOptions) error {
	if options.Detour != "" {
		extra["detour"] = options.Detour
	}
	if options.BindInterface != "" {
		extra["bind_interface"] = options.BindInterface
	}
	if options.Inet4BindAddress != "" {
		extra["inet4_bind_address"] = options.Inet4BindAddress
	}
	if options.Inet6BindAddress != "" {
		extra["inet6_bind_address"] = options.Inet6BindAddress
	}
	if options.ProtectPath != "" {
		extra["protect_path"] = options.ProtectPath
	}
	if !generatorValueEmpty(options.RoutingMark) {
		extra["routing_mark"] = options.RoutingMark
	}
	if options.ReuseAddr {
		extra["reuse_addr"] = true
	}
	if options.NetNS != "" {
		extra["netns"] = options.NetNS
	}
	if options.ConnectTimeout != "" {
		duration, err := normalizeGeneratorDuration(options.ConnectTimeout, "connect_timeout")
		if err != nil {
			return err
		}
		extra["connect_timeout"] = duration
	}
	if options.TCPFastOpen {
		extra["tcp_fast_open"] = true
	}
	if options.TCPMultiPath {
		extra["tcp_multi_path"] = true
	}
	if options.UDPFragment != nil {
		extra["udp_fragment"] = *options.UDPFragment
	}
	if !generatorValueEmpty(options.DomainResolver) {
		extra["domain_resolver"] = cloneOptionValue(options.DomainResolver)
	}
	if options.NetworkStrategy != "" {
		extra["network_strategy"] = options.NetworkStrategy
	}
	if len(options.NetworkType) > 0 {
		extra["network_type"] = append([]string(nil), options.NetworkType...)
	}
	if len(options.FallbackNetworkType) > 0 {
		extra["fallback_network_type"] = append([]string(nil), options.FallbackNetworkType...)
	}
	if options.FallbackDelay != "" {
		duration, err := normalizeGeneratorDuration(options.FallbackDelay, "fallback_delay")
		if err != nil {
			return err
		}
		extra["fallback_delay"] = duration
	}
	if options.DomainStrategy != "" {
		extra["domain_strategy"] = options.DomainStrategy
	}
	return nil
}

func generatorValueEmpty(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case models.NativeOptions:
		return len(typed) == 0
	case map[string]interface{}:
		return len(typed) == 0
	default:
		return false
	}
}

func normalizeGeneratorDuration(raw string, field string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		value += "s"
	}
	if _, err := time.ParseDuration(value); err != nil {
		return "", fmt.Errorf("%s has invalid duration %q: %w", field, raw, err)
	}
	return value, nil
}

var generatorNativeDurationPaths = map[string]string{
	"tls.fragment_fallback_delay": "fragment_fallback_delay",
	"transport.idle_timeout":      "idle_timeout",
	"transport.ping_timeout":      "ping_timeout",
}

func normalizeGeneratorNativeOptions(options models.NativeOptions, rootPath string) (map[string]interface{}, error) {
	if len(options) == 0 {
		return nil, nil
	}
	normalized, err := normalizeGeneratorNativeValue(map[string]interface{}(options), rootPath)
	if err != nil {
		return nil, err
	}
	return normalized.(map[string]interface{}), nil
}

func normalizeGeneratorNativeValue(value interface{}, path string) (interface{}, error) {
	switch typed := value.(type) {
	case models.NativeOptions:
		return normalizeGeneratorNativeValue(map[string]interface{}(typed), path)
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for childKey, childValue := range typed {
			childPath := childKey
			if path != "" {
				childPath = path + "." + childKey
			}
			normalized, err := normalizeGeneratorNativeValue(childValue, childPath)
			if err != nil {
				return nil, err
			}
			result[childKey] = normalized
		}
		return result, nil
	case []interface{}:
		result := make([]interface{}, len(typed))
		for i, childValue := range typed {
			normalized, err := normalizeGeneratorNativeValue(childValue, path)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	case string:
		if field, isDuration := generatorNativeDurationPaths[path]; isDuration {
			return normalizeGeneratorDuration(typed, field)
		}
		return typed, nil
	default:
		return cloneOptionValue(typed), nil
	}
}

func cloneOptionValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case models.NativeOptions:
		return cloneOptionValue(map[string]interface{}(typed))
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			result[key] = cloneOptionValue(child)
		}
		return result
	case map[string]string:
		result := make(map[string]interface{}, len(typed))
		for key, child := range typed {
			result[key] = child
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(typed))
		for i, child := range typed {
			result[i] = cloneOptionValue(child)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	case models.ListableString:
		return append([]string(nil), typed...)
	case models.ByteList:
		return append(models.ByteList(nil), typed...)
	default:
		return typed
	}
}

func generatorOptionMap(value interface{}) (map[string]interface{}, bool) {
	switch typed := value.(type) {
	case models.NativeOptions:
		return cloneOptionValue(map[string]interface{}(typed)).(map[string]interface{}), true
	case map[string]interface{}:
		return cloneOptionValue(typed).(map[string]interface{}), true
	case map[string]string:
		return cloneOptionValue(typed).(map[string]interface{}), true
	default:
		return nil, false
	}
}

func mergeGeneratorOptionMaps(base map[string]interface{}, override map[string]interface{}) map[string]interface{} {
	result := map[string]interface{}{}
	for key, value := range base {
		result[key] = cloneOptionValue(value)
	}
	for key, value := range override {
		if existingMap, existingOK := generatorOptionMap(result[key]); existingOK {
			if overrideMap, overrideOK := generatorOptionMap(value); overrideOK {
				result[key] = mergeGeneratorOptionMaps(existingMap, overrideMap)
				continue
			}
		}
		result[key] = cloneOptionValue(value)
	}
	return result
}

func normalizeGeneratorFingerprint(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "none" {
		return ""
	}
	return value
}

func legacyGeneratorTLS(enabled bool, disableSNI bool, serverName string, insecure bool, alpn []string, fingerprint string) map[string]interface{} {
	if !enabled {
		return map[string]interface{}{}
	}
	tls := map[string]interface{}{"enabled": true}
	if disableSNI {
		tls["disable_sni"] = true
	}
	if strings.TrimSpace(serverName) != "" {
		tls["server_name"] = serverName
	}
	if insecure {
		tls["insecure"] = true
	}
	if len(alpn) > 0 {
		tls["alpn"] = append([]string{}, alpn...)
	}
	if fingerprint = normalizeGeneratorFingerprint(fingerprint); fingerprint != "" {
		tls["utls"] = map[string]interface{}{
			"enabled":     true,
			"fingerprint": fingerprint,
		}
	}
	return tls
}

func finalizeGeneratorTLS(base map[string]interface{}, native models.NativeOptions, forceReality bool, requireTLS bool) (map[string]interface{}, error) {
	nativeOptions, err := normalizeGeneratorNativeOptions(native, "tls")
	if err != nil {
		return nil, fmt.Errorf("invalid tls_options: %w", err)
	}
	if err := validateGeneratorTLSOptionSchema(nativeOptions); err != nil {
		return nil, fmt.Errorf("invalid tls_options: %w", err)
	}
	// Native options retain fields the form does not model, while non-empty
	// legacy/form fields win so editing an imported node actually changes the
	// generated configuration instead of being shadowed by stale import data.
	tls := mergeGeneratorOptionMaps(nativeOptions, base)
	if len(tls) == 0 && !forceReality && !requireTLS {
		return nil, nil
	}

	if rawUTLS, exists := tls["utls"]; exists {
		if rawUTLS == nil {
			delete(tls, "utls")
		} else {
			utls, ok := generatorOptionMap(rawUTLS)
			if !ok {
				return nil, fmt.Errorf("tls.utls must be an object")
			}
			if fingerprint, ok := utls["fingerprint"].(string); ok && strings.EqualFold(strings.TrimSpace(fingerprint), "none") {
				delete(utls, "fingerprint")
			}
			tls["utls"] = utls
		}
	}

	realityEnabled := forceReality
	var reality map[string]interface{}
	if rawReality, exists := tls["reality"]; exists {
		if rawReality == nil {
			delete(tls, "reality")
		} else {
			var ok bool
			reality, ok = generatorOptionMap(rawReality)
			if !ok {
				return nil, fmt.Errorf("tls.reality must be an object")
			}
			if enabled, exists := reality["enabled"]; exists {
				boolEnabled, ok := enabled.(bool)
				if !ok {
					return nil, fmt.Errorf("tls.reality.enabled must be a boolean")
				}
				realityEnabled = realityEnabled || boolEnabled
			}
		}
	}
	if realityEnabled {
		if reality == nil {
			reality = map[string]interface{}{}
		}
		reality["enabled"] = true
		publicKey, _ := reality["public_key"].(string)
		if strings.TrimSpace(publicKey) == "" {
			return nil, fmt.Errorf("reality client requires tls.reality.public_key")
		}
		tls["reality"] = reality
		tls["enabled"] = true
		utls, _ := generatorOptionMap(tls["utls"])
		if utls == nil {
			utls = map[string]interface{}{}
		}
		utls["enabled"] = true
		if fingerprint, _ := utls["fingerprint"].(string); strings.TrimSpace(fingerprint) == "" {
			utls["fingerprint"] = "chrome"
		}
		tls["utls"] = utls
	}
	if requireTLS {
		tls["enabled"] = true
	}
	if enabled, exists := tls["enabled"]; exists {
		if _, ok := enabled.(bool); !ok {
			return nil, fmt.Errorf("tls.enabled must be a boolean")
		}
	}
	return tls, nil
}

var generatorTLSAllowedKeys = map[string]struct{}{
	"enabled": {}, "disable_sni": {}, "server_name": {}, "insecure": {},
	"alpn": {}, "min_version": {}, "max_version": {}, "cipher_suites": {},
	"certificate": {}, "certificate_path": {}, "fragment": {},
	"fragment_fallback_delay": {}, "record_fragment": {}, "ech": {},
	"utls": {}, "reality": {},
}

var generatorTLSNestedAllowedKeys = map[string]map[string]struct{}{
	"ech": {
		"enabled": {}, "config": {}, "config_path": {},
		"pq_signature_schemes_enabled": {}, "dynamic_record_sizing_disabled": {},
	},
	"utls": {
		"enabled": {}, "fingerprint": {},
	},
	"reality": {
		"enabled": {}, "public_key": {}, "short_id": {},
	},
}

func validateGeneratorTLSOptionSchema(options map[string]interface{}) error {
	for key, value := range options {
		if _, allowed := generatorTLSAllowedKeys[key]; !allowed {
			return fmt.Errorf("tls contains unsupported option %q for sing-box 1.12.12", key)
		}
		allowedNested, nested := generatorTLSNestedAllowedKeys[key]
		if !nested || value == nil {
			continue
		}
		nestedOptions, ok := generatorOptionMap(value)
		if !ok {
			return fmt.Errorf("tls.%s must be an object", key)
		}
		for nestedKey := range nestedOptions {
			if _, allowed := allowedNested[nestedKey]; !allowed {
				return fmt.Errorf("tls.%s contains unsupported option %q for sing-box 1.12.12", key, nestedKey)
			}
		}
	}
	return nil
}

func generatorTLSEnabled(tls map[string]interface{}) bool {
	if tls == nil {
		return false
	}
	enabled, _ := tls["enabled"].(bool)
	return enabled
}

func generatorNativeRealityEnabled(options models.NativeOptions) bool {
	reality, ok := generatorOptionMap(options["reality"])
	if !ok {
		return false
	}
	enabled, _ := reality["enabled"].(bool)
	return enabled
}

func generatorNativeTLSEnabled(options models.NativeOptions) bool {
	enabled, _ := options["enabled"].(bool)
	return enabled
}

func normalizeGeneratorTransportType(raw string) (string, error) {
	transportType := strings.ToLower(strings.TrimSpace(raw))
	switch transportType {
	case "", "tcp", "none", "raw":
		return "", nil
	case "h2":
		return "http", nil
	case "ws", "http", "quic", "grpc", "httpupgrade":
		return transportType, nil
	case "kcp", "mkcp":
		return "", fmt.Errorf("transport %q is not supported by sing-box 1.12.12", raw)
	default:
		return "", fmt.Errorf("unknown sing-box transport %q", raw)
	}
}

func finalizeGeneratorTransport(legacyType string, legacy map[string]interface{}, native models.NativeOptions, tls map[string]interface{}) (map[string]interface{}, error) {
	transportType, err := normalizeGeneratorTransportType(legacyType)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(legacyType) != "" && transportType == "" {
		// An explicit form choice of tcp/none clears transport_options retained
		// from import. Empty legacyType, in contrast, leaves native-only data.
		return nil, nil
	}
	base := map[string]interface{}{}
	if transportType != "" {
		base["type"] = transportType
		for key, value := range legacy {
			base[key] = cloneOptionValue(value)
		}
	}
	nativeOptions, err := normalizeGeneratorNativeOptions(native, "transport")
	if err != nil {
		return nil, fmt.Errorf("invalid transport_options: %w", err)
	}
	// Keep native-only fields, but let the active form transport and its
	// non-empty legacy fields override values captured during import.
	transport := mergeGeneratorOptionMaps(nativeOptions, base)
	pruneGeneratorTransportOptions(transport)
	if len(transport) == 0 {
		return nil, nil
	}
	rawType, ok := transport["type"].(string)
	if !ok || strings.TrimSpace(rawType) == "" {
		return nil, fmt.Errorf("transport_options requires a string type")
	}
	finalType, err := normalizeGeneratorTransportType(rawType)
	if err != nil {
		return nil, err
	}
	if finalType == "" {
		delete(transport, "type")
		if len(transport) != 0 {
			return nil, fmt.Errorf("tcp transport must be represented by omitting transport, not by transport options")
		}
		return nil, nil
	}
	transport["type"] = finalType

	if finalType == "http" {
		if path, exists := transport["path"]; exists {
			if _, ok := path.(string); !ok {
				return nil, fmt.Errorf("http transport path must be a string, got %T", path)
			}
		}
	}
	if finalType == "quic" {
		if seed, exists := transport["seed"]; exists {
			seedValue, ok := seed.(string)
			if !ok || strings.TrimSpace(seedValue) != "" {
				return nil, fmt.Errorf("quic transport seed is not supported by sing-box 1.12.12")
			}
			delete(transport, "seed")
		}
		if header, exists := transport["header"]; exists {
			if !generatorNoopQUICHeader(header) {
				return nil, fmt.Errorf("quic transport header is not supported by sing-box 1.12.12")
			}
			delete(transport, "header")
		}
		if !generatorTLSEnabled(tls) {
			return nil, fmt.Errorf("quic transport requires enabled TLS")
		}
	}
	if err := pruneGeneratorTransportUnionOptions(transport, finalType); err != nil {
		return nil, err
	}
	return transport, nil
}

var generatorTransportAllowedKeys = map[string]map[string]struct{}{
	"http": {
		"type": {}, "host": {}, "path": {}, "method": {}, "headers": {},
		"idle_timeout": {}, "ping_timeout": {},
	},
	"ws": {
		"type": {}, "path": {}, "headers": {}, "max_early_data": {},
		"early_data_header_name": {},
	},
	"quic": {
		"type": {},
	},
	"grpc": {
		"type": {}, "service_name": {}, "idle_timeout": {}, "ping_timeout": {},
		"permit_without_stream": {},
	},
	"httpupgrade": {
		"type": {}, "host": {}, "path": {}, "headers": {},
	},
}

var generatorKnownTransportKeys = func() map[string]struct{} {
	known := map[string]struct{}{}
	for _, allowed := range generatorTransportAllowedKeys {
		for key := range allowed {
			known[key] = struct{}{}
		}
	}
	return known
}()

func pruneGeneratorTransportUnionOptions(options map[string]interface{}, transportType string) error {
	allowed, exists := generatorTransportAllowedKeys[transportType]
	if !exists {
		return fmt.Errorf("unknown sing-box transport %q", transportType)
	}
	for key := range options {
		if _, keep := allowed[key]; keep {
			continue
		}
		if _, staleUnionField := generatorKnownTransportKeys[key]; staleUnionField {
			delete(options, key)
			continue
		}
		return fmt.Errorf("%s transport contains unsupported option %q for sing-box 1.12.12", transportType, key)
	}
	return nil
}

func pruneGeneratorTransportOptions(options map[string]interface{}) {
	for key, value := range options {
		switch typed := value.(type) {
		case map[string]interface{}:
			if key == "headers" {
				// Header values are opaque strings. Only the empty Host tombstone
				// inserted by the form is removed; an empty value for any other
				// header is valid and must survive unchanged.
				for headerName, headerValue := range typed {
					if !strings.EqualFold(headerName, "host") {
						continue
					}
					if stringValue, ok := headerValue.(string); ok && strings.TrimSpace(stringValue) == "" {
						delete(typed, headerName)
					}
				}
				if len(typed) == 0 {
					delete(options, key)
				}
				continue
			}
			pruneGeneratorTransportOptions(typed)
			if len(typed) == 0 {
				delete(options, key)
			}
		case string:
			if key != "type" && strings.TrimSpace(typed) == "" {
				delete(options, key)
			}
		case []string:
			if len(typed) == 0 {
				delete(options, key)
			}
		case []interface{}:
			if len(typed) == 0 {
				delete(options, key)
			}
		case int:
			if typed == 0 {
				delete(options, key)
			}
		case int64:
			if typed == 0 {
				delete(options, key)
			}
		case float64:
			if typed == 0 {
				delete(options, key)
			}
		}
	}
}

func generatorNoopQUICHeader(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == "" || strings.EqualFold(strings.TrimSpace(typed), "none")
	case models.NativeOptions:
		return generatorNoopQUICHeader(map[string]interface{}(typed))
	case map[string]interface{}:
		for key, child := range typed {
			if key == "type" {
				typeName, ok := child.(string)
				if !ok || (strings.TrimSpace(typeName) != "" && !strings.EqualFold(strings.TrimSpace(typeName), "none")) {
					return false
				}
				continue
			}
			if !generatorValueEmpty(child) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func normalizeGeneratorPacketEncoding(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "none":
		return "", nil
	case "packetaddr", "xudp":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported packet_encoding %q; sing-box 1.12.12 accepts packetaddr or xudp", raw)
	}
}

func resolveGeneratorUDPOverTCP(value interface{}, compatibility models.NativeOptions) (interface{}, error) {
	switch typed := value.(type) {
	case nil:
		if len(compatibility) == 0 {
			return nil, nil
		}
		normalized, err := normalizeGeneratorNativeOptions(compatibility, "udp_over_tcp")
		if err != nil {
			return nil, fmt.Errorf("invalid udp_over_tcp_options: %w", err)
		}
		return normalized, nil
	case bool:
		if !typed {
			// An explicit flat false is authoritative and clears stale native
			// udp_over_tcp_options retained from an earlier import.
			return nil, nil
		}
		return true, nil
	case models.NativeOptions:
		return resolveGeneratorUDPOverTCP(map[string]interface{}(typed), nil)
	case map[string]interface{}:
		normalized, err := normalizeGeneratorNativeValue(typed, "udp_over_tcp")
		if err != nil {
			return nil, err
		}
		return normalized, nil
	default:
		return nil, fmt.Errorf("udp_over_tcp must be a boolean or object, got %T", value)
	}
}

func splitGeneratorCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func generatorTransportHeaders(host string, configured map[string]string) map[string]interface{} {
	headers := make(map[string]interface{}, len(configured)+1)
	if host != "" {
		headers["Host"] = host
	}
	for key, value := range configured {
		headers[key] = value
	}
	return headers
}

func (s *SingBoxService) generateDirectOutbound(config *models.DirectConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{Type: "direct", Tag: tag, Extra: map[string]interface{}{}}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.OverrideAddress != "" {
		outbound.Extra["override_address"] = config.OverrideAddress
	}
	if config.OverridePort != 0 {
		outbound.Extra["override_port"] = config.OverridePort
	}
	if config.ProxyProtocol != 0 {
		outbound.Extra["proxy_protocol"] = config.ProxyProtocol
	}
	if len(outbound.Extra) == 0 {
		outbound.Extra = nil
	}
	return outbound, nil
}

func (s *SingBoxService) generateSSOutbound(config *models.SSConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "shadowsocks", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"method": config.Method, "password": config.Password},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.Plugin != "" {
		outbound.Extra["plugin"] = config.Plugin
		if config.PluginOpts != "" {
			outbound.Extra["plugin_opts"] = config.PluginOpts
		}
	}
	if len(config.Network) > 0 {
		outbound.Extra["network"] = append([]string(nil), config.Network...)
	}
	uot, err := resolveGeneratorUDPOverTCP(config.UDPOverTCP, config.UDPOverTCPOptions)
	if err != nil {
		return OutboundConfig{}, err
	}
	if uot != nil {
		outbound.Extra["udp_over_tcp"] = uot
	}
	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = cloneOptionValue(config.MultiplexConfig)
	}
	return outbound, nil
}

func (s *SingBoxService) generateVLESSOutbound(config *models.VLESSConfig, tag string) (OutboundConfig, error) {
	if encryption := strings.ToLower(strings.TrimSpace(config.Encryption)); encryption != "" && encryption != "none" {
		return OutboundConfig{}, fmt.Errorf("vless encryption %q is not supported by sing-box 1.12.12; only none is valid", config.Encryption)
	}
	if strings.TrimSpace(config.SpiderX) != "" {
		return OutboundConfig{}, fmt.Errorf("vless reality spider_x is not supported by sing-box 1.12.12")
	}
	outbound := OutboundConfig{
		Type: "vless", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"uuid": config.UUID},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.Flow != "" {
		outbound.Extra["flow"] = config.Flow
	}
	if len(config.OutboundNetwork) > 0 {
		outbound.Extra["network"] = append([]string(nil), config.OutboundNetwork...)
	}
	packetEncoding, err := normalizeGeneratorPacketEncoding(config.PacketEncoding)
	if err != nil {
		return OutboundConfig{}, err
	}
	if packetEncoding != "" {
		outbound.Extra["packet_encoding"] = packetEncoding
	}

	security := strings.ToLower(strings.TrimSpace(config.Security))
	if security == "xtls" {
		security = "tls"
	}
	if security != "" && security != "none" && security != "tls" && security != "reality" {
		return OutboundConfig{}, fmt.Errorf("unsupported vless security %q", config.Security)
	}
	var tls map[string]interface{}
	if security != "none" {
		tlsBase := legacyGeneratorTLS(security == "tls" || security == "reality", false, config.SNI, config.Insecure, splitGeneratorCommaList(config.ALPN), config.Fingerprint)
		if security == "tls" {
			tlsBase["reality"] = nil
		}
		if security == "reality" {
			realityBase := map[string]interface{}{"enabled": true}
			// Empty compatibility fields mean that the imported node did not
			// carry a flat override. Do not let those zero values erase valid
			// credentials retained in native tls_options. Explicit form clears
			// are reconciled into TLSOptions by ProxyNode.ParseConfig first.
			if strings.TrimSpace(config.PublicKey) != "" {
				realityBase["public_key"] = config.PublicKey
			}
			if strings.TrimSpace(config.ShortID) != "" {
				realityBase["short_id"] = config.ShortID
			}
			tlsBase["reality"] = realityBase
		}
		tls, err = finalizeGeneratorTLS(tlsBase, config.TLSOptions, security == "reality", false)
		if err != nil {
			return OutboundConfig{}, err
		}
	}
	if tls != nil {
		outbound.Extra["tls"] = tls
	}

	legacyTransport, err := generatorV2RayLegacyTransport(config.Network, config.Path, config.Headers, config.Host, config.MaxEarlyData, config.EarlyDataHeader, config.ServiceName, config.HTTPUpgradePath, config.HTTPUpgradeHost, config.HeaderType, config.Seed, "", nil)
	if err != nil {
		return OutboundConfig{}, err
	}
	transport, err := finalizeGeneratorTransport(config.Network, legacyTransport, config.TransportOptions, tls)
	if err != nil {
		return OutboundConfig{}, err
	}
	if transport != nil {
		outbound.Extra["transport"] = transport
	}
	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = cloneOptionValue(config.MultiplexConfig)
	}
	return outbound, nil
}

func (s *SingBoxService) generateVMESSOutbound(config *models.VMESSConfig, tag string) (OutboundConfig, error) {
	security := strings.TrimSpace(config.Security)
	if security == "" {
		security = "auto"
	}
	outbound := OutboundConfig{
		Type: "vmess", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"uuid": config.UUID, "security": security},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.AlterID != 0 {
		outbound.Extra["alter_id"] = config.AlterID
	}
	if config.GlobalPadding {
		outbound.Extra["global_padding"] = true
	}
	if config.AuthenticatedLength {
		outbound.Extra["authenticated_length"] = true
	}
	if len(config.OutboundNetwork) > 0 {
		outbound.Extra["network"] = append([]string(nil), config.OutboundNetwork...)
	}
	packetEncoding, err := normalizeGeneratorPacketEncoding(config.PacketEncoding)
	if err != nil {
		return OutboundConfig{}, err
	}
	if packetEncoding != "" {
		outbound.Extra["packet_encoding"] = packetEncoding
	}

	tlsMode := strings.ToLower(strings.TrimSpace(config.TLS))
	if tlsMode == "xtls" {
		tlsMode = "tls"
	}
	if tlsMode != "" && tlsMode != "none" && tlsMode != "tls" && tlsMode != "reality" {
		return OutboundConfig{}, fmt.Errorf("unsupported vmess tls mode %q", config.TLS)
	}
	var tls map[string]interface{}
	if tlsMode != "none" {
		tlsBase := legacyGeneratorTLS(tlsMode == "tls" || tlsMode == "reality", false, config.SNI, config.Insecure, splitGeneratorCommaList(config.ALPN), config.Fingerprint)
		if tlsMode == "tls" {
			tlsBase["reality"] = nil
		}
		tls, err = finalizeGeneratorTLS(tlsBase, config.TLSOptions, tlsMode == "reality", false)
		if err != nil {
			return OutboundConfig{}, err
		}
	}
	if tls != nil {
		outbound.Extra["tls"] = tls
	}

	legacyTransport, err := generatorV2RayLegacyTransport(config.Network, config.Path, config.Headers, config.Host, config.MaxEarlyData, config.EarlyDataHeader, config.ServiceName, config.HTTPUpgradePath, config.HTTPUpgradeHost, config.HeaderType, config.Seed, config.Method, config.HTTPPath)
	if err != nil {
		return OutboundConfig{}, err
	}
	transport, err := finalizeGeneratorTransport(config.Network, legacyTransport, config.TransportOptions, tls)
	if err != nil {
		return OutboundConfig{}, err
	}
	if transport != nil {
		outbound.Extra["transport"] = transport
	}
	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = cloneOptionValue(config.MultiplexConfig)
	}
	return outbound, nil
}

func generatorV2RayLegacyTransport(network string, path string, headers map[string]string, host string, maxEarlyData int, earlyDataHeader string, serviceName string, upgradePath string, upgradeHost string, headerType string, seed string, method string, httpPaths []string) (map[string]interface{}, error) {
	transportType, err := normalizeGeneratorTransportType(network)
	if err != nil {
		return nil, err
	}
	if normalizedHeaderType := strings.ToLower(strings.TrimSpace(headerType)); normalizedHeaderType != "" && normalizedHeaderType != "none" {
		displayType := transportType
		if displayType == "" {
			displayType = "tcp"
		}
		return nil, fmt.Errorf("%s transport header_type %q is not supported by sing-box 1.12.12", displayType, headerType)
	}
	if strings.TrimSpace(seed) != "" {
		displayType := transportType
		if displayType == "" {
			displayType = "tcp"
		}
		return nil, fmt.Errorf("%s transport seed is not supported by sing-box 1.12.12", displayType)
	}
	transport := map[string]interface{}{}
	switch transportType {
	case "ws":
		if path != "" {
			transport["path"] = path
		}
		combinedHeaders := generatorTransportHeaders(host, headers)
		if host != "" {
			combinedHeaders["Host"] = host
		}
		if len(combinedHeaders) > 0 {
			transport["headers"] = combinedHeaders
		}
		if maxEarlyData > 0 {
			transport["max_early_data"] = maxEarlyData
		}
		if earlyDataHeader != "" {
			transport["early_data_header_name"] = earlyDataHeader
		}
	case "grpc":
		if serviceName != "" {
			transport["service_name"] = serviceName
		}
	case "httpupgrade":
		if upgradePath == "" {
			upgradePath = path
		}
		if upgradeHost == "" {
			upgradeHost = host
		}
		if upgradePath != "" {
			transport["path"] = upgradePath
		}
		if upgradeHost != "" {
			transport["host"] = upgradeHost
		}
		if len(headers) > 0 {
			transport["headers"] = cloneOptionValue(headers)
		}
	case "quic":
	case "http":
		if hosts := splitGeneratorCommaList(host); len(hosts) > 0 {
			transport["host"] = hosts
		}
		if len(httpPaths) > 1 {
			return nil, fmt.Errorf("http transport supports one path in sing-box 1.12.12, got %d", len(httpPaths))
		}
		if len(httpPaths) == 1 {
			if httpPaths[0] != "" {
				transport["path"] = httpPaths[0]
			}
		} else if path != "" {
			transport["path"] = path
		}
		if method != "" {
			transport["method"] = method
		}
		if len(headers) > 0 {
			transport["headers"] = cloneOptionValue(headers)
		}
	}
	return transport, nil
}

func (s *SingBoxService) generateHysteria2Outbound(config *models.Hysteria2Config, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "hysteria2", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"password": config.Password},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	serverPorts, err := normalizeGeneratorHysteria2ServerPorts(config.ServerPort, config.ServerPorts)
	if err != nil {
		return OutboundConfig{}, err
	}
	if len(serverPorts) > 0 {
		outbound.Extra["server_ports"] = serverPorts
	}
	if config.BrutalUpMbps > 0 || config.BrutalDownMbps > 0 {
		if config.BrutalUpMbps > 0 {
			outbound.Extra["up_mbps"] = config.BrutalUpMbps
		}
		if config.BrutalDownMbps > 0 {
			outbound.Extra["down_mbps"] = config.BrutalDownMbps
		}
	} else {
		if config.UpMbps > 0 {
			outbound.Extra["up_mbps"] = config.UpMbps
		}
		if config.DownMbps > 0 {
			outbound.Extra["down_mbps"] = config.DownMbps
		}
	}
	obfs, err := generatorHysteria2Obfs(config)
	if err != nil {
		return OutboundConfig{}, err
	}
	if obfs != nil {
		outbound.Extra["obfs"] = obfs
	}
	if len(config.Network) > 0 {
		outbound.Extra["network"] = append([]string(nil), config.Network...)
	}
	if config.HopInterval != "" {
		duration, err := normalizeGeneratorDuration(config.HopInterval, "hop_interval")
		if err != nil {
			return OutboundConfig{}, err
		}
		outbound.Extra["hop_interval"] = duration
	}
	if config.BrutalDebug {
		outbound.Extra["brutal_debug"] = true
	}
	tlsBase := legacyGeneratorTLS(true, false, config.SNI, config.InsecureSkipVerify, config.ALPN, config.Fingerprint)
	tls, err := finalizeGeneratorTLS(tlsBase, config.TLSOptions, false, true)
	if err != nil {
		return OutboundConfig{}, err
	}
	outbound.Extra["tls"] = tls
	return outbound, nil
}

func normalizeGeneratorHysteria2ServerPorts(serverPort int, rawPorts models.ListableString) ([]string, error) {
	ports := make([]string, 0, len(rawPorts))
	nonEmptyCount := 0
	for _, rawPort := range rawPorts {
		if strings.TrimSpace(rawPort) != "" {
			nonEmptyCount++
		}
	}
	for _, rawPort := range rawPorts {
		value := strings.TrimSpace(rawPort)
		if value == "" {
			continue
		}
		if singlePort, err := strconv.Atoi(value); err == nil {
			if singlePort < 1 || singlePort > 65535 {
				return nil, fmt.Errorf("invalid hysteria2 server port %q", value)
			}
			if nonEmptyCount == 1 && singlePort == serverPort {
				// A normal single-port URI is represented by server_port. sing-box's
				// server_ports field only accepts ranges such as 2000:3000.
				continue
			}
			// Degenerate ranges preserve mixed explicit ports in a hopping list and
			// are accepted by sing-box 1.12.12.
			ports = append(ports, fmt.Sprintf("%d:%d", singlePort, singlePort))
			continue
		}
		value = strings.Replace(value, "-", ":", 1)
		parts := strings.Split(value, ":")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid hysteria2 server port range %q", value)
		}
		start, startErr := strconv.Atoi(strings.TrimSpace(parts[0]))
		end, endErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if startErr != nil || endErr != nil || start < 1 || end > 65535 || start > end {
			return nil, fmt.Errorf("invalid hysteria2 server port range %q", value)
		}
		ports = append(ports, fmt.Sprintf("%d:%d", start, end))
	}
	return ports, nil
}

func generatorHysteria2Obfs(config *models.Hysteria2Config) (map[string]interface{}, error) {
	legacyPassword := config.ObfsPassword
	if legacyPassword == "" {
		legacyPassword = config.SalamanderPassword
	}
	var base map[string]interface{}
	switch typed := config.Obfs.(type) {
	case nil:
		if legacyPassword != "" {
			base = map[string]interface{}{"type": "salamander", "password": legacyPassword}
		}
	case string:
		typeName := strings.ToLower(strings.TrimSpace(typed))
		if typeName == "none" || typeName == "" {
			// String form is the form-managed representation. Empty/none is an
			// explicit clear and must not fall back to stale compatibility fields.
			return nil, nil
		}
		base = map[string]interface{}{"type": typeName}
		if config.ObfsPassword != "" {
			base["password"] = config.ObfsPassword
		}
	case models.NativeOptions:
		base = cloneOptionValue(map[string]interface{}(typed)).(map[string]interface{})
		if legacyPassword != "" {
			base["password"] = legacyPassword
		}
	case map[string]interface{}:
		base = cloneOptionValue(typed).(map[string]interface{})
		if legacyPassword != "" {
			base["password"] = legacyPassword
		}
	default:
		return nil, fmt.Errorf("hysteria2 obfs must be a string or object, got %T", config.Obfs)
	}
	if len(base) == 0 {
		return nil, nil
	}
	typeName, _ := base["type"].(string)
	typeName = strings.ToLower(strings.TrimSpace(typeName))
	if typeName == "none" {
		return nil, nil
	}
	if typeName == "" && legacyPassword != "" {
		typeName = "salamander"
	}
	if typeName != "salamander" {
		return nil, fmt.Errorf("hysteria2 obfs type %q is not supported by sing-box 1.12.12", typeName)
	}
	password, passwordPresent := base["password"].(string)
	if !passwordPresent || password == "" {
		return nil, fmt.Errorf("hysteria2 salamander obfuscation requires a non-empty password")
	}
	base["type"] = "salamander"
	return base, nil
}

func (s *SingBoxService) generateTUICOutbound(config *models.TUICConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "tuic", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"uuid": config.UUID, "password": config.Password},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.CongestionControl != "" {
		outbound.Extra["congestion_control"] = config.CongestionControl
	}
	if config.UDPRelayMode != "" {
		outbound.Extra["udp_relay_mode"] = config.UDPRelayMode
	}
	if config.UDPOverStream {
		outbound.Extra["udp_over_stream"] = true
	}
	if config.ZeroRTTHandshake || config.ReduceRTT {
		outbound.Extra["zero_rtt_handshake"] = true
	}
	if config.Heartbeat != "" {
		duration, err := normalizeGeneratorDuration(config.Heartbeat, "heartbeat")
		if err != nil {
			return OutboundConfig{}, err
		}
		outbound.Extra["heartbeat"] = duration
	}
	if len(config.Network) > 0 {
		outbound.Extra["network"] = append([]string(nil), config.Network...)
	}
	tlsBase := legacyGeneratorTLS(true, config.DisableSNI, config.SNI, config.InsecureSkipVerify, config.ALPN, config.Fingerprint)
	tls, err := finalizeGeneratorTLS(tlsBase, config.TLSOptions, false, true)
	if err != nil {
		return OutboundConfig{}, err
	}
	outbound.Extra["tls"] = tls
	return outbound, nil
}

func (s *SingBoxService) generateTrojanOutbound(config *models.TrojanConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "trojan", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"password": config.Password},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if len(config.OutboundNetwork) > 0 {
		outbound.Extra["network"] = append([]string(nil), config.OutboundNetwork...)
	}
	tlsMode := strings.ToLower(strings.TrimSpace(config.Security))
	if tlsMode == "" {
		if generatorNativeRealityEnabled(config.TLSOptions) {
			tlsMode = "reality"
		} else {
			tlsMode = "tls"
		}
	}
	if tlsMode == "xtls" {
		tlsMode = "tls"
	}
	if tlsMode != "none" && tlsMode != "tls" && tlsMode != "reality" {
		return OutboundConfig{}, fmt.Errorf("unsupported trojan security %q", config.Security)
	}
	var tls map[string]interface{}
	if tlsMode != "none" {
		tlsBase := legacyGeneratorTLS(true, false, config.SNI, config.Insecure, config.ALPN, config.Fingerprint)
		if tlsMode == "tls" {
			tlsBase["reality"] = nil
		}
		tlsResult, tlsErr := finalizeGeneratorTLS(tlsBase, config.TLSOptions, tlsMode == "reality", true)
		if tlsErr != nil {
			return OutboundConfig{}, tlsErr
		}
		tls = tlsResult
	}
	if tls != nil {
		outbound.Extra["tls"] = tls
	}

	legacyTransport, err := generatorV2RayLegacyTransport(config.Network, config.Path, config.Headers, config.Host, 0, "", config.ServiceName, config.Path, config.Host, "", "", config.HTTPMethod, nil)
	if err != nil {
		return OutboundConfig{}, err
	}
	transport, err := finalizeGeneratorTransport(config.Network, legacyTransport, config.TransportOptions, tls)
	if err != nil {
		return OutboundConfig{}, err
	}
	if transport != nil {
		outbound.Extra["transport"] = transport
	}
	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = cloneOptionValue(config.MultiplexConfig)
	}
	return outbound, nil
}

func (s *SingBoxService) generateAnyTLSOutbound(config *models.AnyTLSConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "anytls", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"password": config.Password},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.IdleSessionCheckInterval != "" {
		duration, err := normalizeGeneratorDuration(config.IdleSessionCheckInterval, "idle_session_check_interval")
		if err != nil {
			return OutboundConfig{}, err
		}
		outbound.Extra["idle_session_check_interval"] = duration
	}
	if config.IdleSessionTimeout != "" {
		duration, err := normalizeGeneratorDuration(config.IdleSessionTimeout, "idle_session_timeout")
		if err != nil {
			return OutboundConfig{}, err
		}
		outbound.Extra["idle_session_timeout"] = duration
	}
	if config.MinIdleSession > 0 {
		outbound.Extra["min_idle_session"] = config.MinIdleSession
	}
	tlsBase := legacyGeneratorTLS(true, false, config.SNI, config.Insecure, config.ALPN, config.Fingerprint)
	tls, err := finalizeGeneratorTLS(tlsBase, config.TLSOptions, false, true)
	if err != nil {
		return OutboundConfig{}, err
	}
	outbound.Extra["tls"] = tls
	return outbound, nil
}

func (s *SingBoxService) generateSOCKS5Outbound(config *models.SOCKS5Config, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "socks", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{"version": "5"},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.Username != "" {
		outbound.Extra["username"] = config.Username
	}
	if config.Password != "" {
		outbound.Extra["password"] = config.Password
	}
	if len(config.Network) > 0 {
		outbound.Extra["network"] = append([]string(nil), config.Network...)
	}
	uot, err := resolveGeneratorUDPOverTCP(config.UDPOverTCP, config.UDPOverTCPOptions)
	if err != nil {
		return OutboundConfig{}, err
	}
	if uot != nil {
		outbound.Extra["udp_over_tcp"] = uot
	}
	return outbound, nil
}

func (s *SingBoxService) generateHTTPProxyOutbound(config *models.HTTPProxyConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "http", Tag: tag, Server: config.Server, Port: config.ServerPort,
		Extra: map[string]interface{}{},
	}
	if err := applyGeneratorDialerOptions(outbound.Extra, config.DialerOptions); err != nil {
		return OutboundConfig{}, err
	}
	if config.Username != "" {
		outbound.Extra["username"] = config.Username
	}
	if config.Password != "" {
		outbound.Extra["password"] = config.Password
	}
	if config.Path != "" {
		outbound.Extra["path"] = config.Path
	}
	if len(config.Headers) > 0 {
		outbound.Extra["headers"] = cloneOptionValue(config.Headers)
	}
	var tls map[string]interface{}
	if config.TLS || generatorNativeTLSEnabled(config.TLSOptions) {
		tlsBase := legacyGeneratorTLS(true, false, config.SNI, config.Insecure, nil, "")
		// HTTP has no legacy ALPN/fingerprint controls; keep those native fields.
		delete(tlsBase, "alpn")
		delete(tlsBase, "utls")
		tlsResult, tlsErr := finalizeGeneratorTLS(tlsBase, config.TLSOptions, false, false)
		if tlsErr != nil {
			return OutboundConfig{}, tlsErr
		}
		tls = tlsResult
	}
	if tls != nil {
		outbound.Extra["tls"] = tls
	}
	return outbound, nil
}

// generateWireGuardEndpointForNode parses the node config and renders a
// WireGuard endpoint in the sing-box 1.11+ format.
func (s *SingBoxService) generateWireGuardEndpointForNode(node *models.ProxyNode, tag string) (EndpointConfig, error) {
	parsedConfig, err := node.ParseConfig()
	if err != nil {
		return EndpointConfig{}, err
	}
	config, ok := parsedConfig.(*models.WireGuardConfig)
	if !ok {
		return EndpointConfig{}, fmt.Errorf("unexpected config type for wireguard node: %T", parsedConfig)
	}
	return s.generateWireGuardEndpoint(config, tag)
}

func (s *SingBoxService) generateWireGuardEndpoint(config *models.WireGuardConfig, tag string) (EndpointConfig, error) {
	if len(config.LocalAddress) == 0 {
		return EndpointConfig{}, fmt.Errorf("wireguard local_address is required")
	}
	if strings.TrimSpace(config.PrivateKey) == "" {
		return EndpointConfig{}, fmt.Errorf("wireguard private_key is required")
	}

	endpoint := EndpointConfig{Type: "wireguard", Tag: tag, Extra: map[string]interface{}{}}
	endpoint.Extra["address"] = append([]string(nil), config.LocalAddress...)
	endpoint.Extra["private_key"] = config.PrivateKey
	if config.SystemInterface {
		endpoint.Extra["system"] = true
	}
	if config.InterfaceName != "" {
		endpoint.Extra["name"] = config.InterfaceName
	}
	if config.MTU > 0 {
		endpoint.Extra["mtu"] = config.MTU
	}
	if config.ListenPort > 0 {
		endpoint.Extra["listen_port"] = config.ListenPort
	}
	if config.UDPTimeout != "" {
		duration, err := normalizeGeneratorDuration(config.UDPTimeout, "udp_timeout")
		if err != nil {
			return EndpointConfig{}, err
		}
		endpoint.Extra["udp_timeout"] = duration
	}
	if config.Workers > 0 {
		endpoint.Extra["workers"] = config.Workers
	}

	resolver, err := wireGuardDomainResolverValue(config)
	if err != nil {
		return EndpointConfig{}, fmt.Errorf("invalid wireguard domain_resolver: %w", err)
	}
	dialer := models.DialerOptions{
		Detour:              config.Detour,
		BindInterface:       config.BindInterface,
		Inet4BindAddress:    config.Inet4BindAddress,
		Inet6BindAddress:    config.Inet6BindAddress,
		ProtectPath:         config.ProtectPath,
		RoutingMark:         config.RoutingMark,
		ReuseAddr:           config.ReuseAddr,
		NetNS:               config.NetNS,
		ConnectTimeout:      config.ConnectTimeout,
		TCPFastOpen:         config.TCPFastOpen,
		TCPMultiPath:        config.TCPMultiPath,
		UDPFragment:         config.UDPFragment,
		DomainResolver:      resolver,
		NetworkStrategy:     config.NetworkStrategy,
		NetworkType:         config.NetworkType,
		FallbackNetworkType: config.FallbackNetworkType,
		FallbackDelay:       config.FallbackDelay,
		DomainStrategy:      config.DomainStrategy,
	}
	if err := applyGeneratorDialerOptions(endpoint.Extra, dialer); err != nil {
		return EndpointConfig{}, err
	}

	peers := config.Peers
	if len(peers) == 0 {
		peer, ok := wireGuardSinglePeerFromConfig(config)
		if !ok {
			return EndpointConfig{}, fmt.Errorf("wireguard server, server_port and peer_public_key are required")
		}
		peers = []models.WireGuardPeerConfig{peer}
	}

	peerConfigs := make([]map[string]interface{}, 0, len(peers))
	for index, peer := range peers {
		if strings.TrimSpace(peer.Server) == "" || peer.ServerPort <= 0 || strings.TrimSpace(peer.PublicKey) == "" {
			return EndpointConfig{}, fmt.Errorf("wireguard peer %d requires server, server_port and public_key", index)
		}
		peerConfig := map[string]interface{}{
			"address":    peer.Server,
			"port":       peer.ServerPort,
			"public_key": peer.PublicKey,
		}
		if peer.PreSharedKey != "" {
			peerConfig["pre_shared_key"] = peer.PreSharedKey
		}
		allowedIPs := peer.AllowedIPs
		if len(allowedIPs) == 0 && len(peers) == 1 {
			allowedIPs = defaultWireGuardAllowedIPs()
		}
		if len(allowedIPs) > 0 {
			peerConfig["allowed_ips"] = append([]string(nil), allowedIPs...)
		}
		if peer.PersistentKeepaliveInterval > 0 {
			peerConfig["persistent_keepalive_interval"] = peer.PersistentKeepaliveInterval
		}
		if len(peer.Reserved) > 0 {
			reserved := make([]int, len(peer.Reserved))
			for i, value := range peer.Reserved {
				reserved[i] = int(value)
			}
			peerConfig["reserved"] = reserved
		}
		peerConfigs = append(peerConfigs, peerConfig)
	}
	endpoint.Extra["peers"] = peerConfigs
	return endpoint, nil
}

// Start starts the single sing-box process
func (s *SingBoxService) Start() error {
	if err := s.Stop(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	configPath := filepath.Join(s.configDir, "config.json")

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", configPath)
	}

	singBoxBinary, err := s.resolveSingBoxBinary()
	if err != nil {
		return err
	}
	cmd := exec.Command(singBoxBinary, "run", "-c", configPath)
	// Tie the child to the manager process where the OS supports it, so even a
	// SIGKILL'ed manager cannot leave an orphaned sing-box behind.
	configureSysProcAttr(cmd)

	// Set up logging
	logPath := filepath.Join(s.configDir, "singbox.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		logFile.Close()
		return fmt.Errorf("sing-box exited early: %w", err)
	case <-time.After(300 * time.Millisecond):
	}

	s.process = cmd
	s.logFile = logFile // Save log file handle for later cleanup
	s.waitCh = waitCh
	s.saveLastGoodConfig()
	return nil
}

// Stop stops the sing-box process
func (s *SingBoxService) Stop() error {
	s.mu.Lock()
	cmd := s.process
	waitCh := s.waitCh
	logFile := s.logFile
	s.process = nil
	s.waitCh = nil
	s.logFile = nil
	s.mu.Unlock()

	if cmd == nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil
	}

	if cmd.Process != nil {
		if err := cmd.Process.Kill(); err != nil {
			// Log but continue - process may already be dead
			fmt.Printf("Warning: failed to kill existing process: %v\n", err)
		}
	}

	if waitCh != nil {
		<-waitCh
	} else {
		cmd.Wait()
	}

	if logFile != nil {
		logFile.Close()
	}

	return nil
}

// Restart restarts the sing-box process
func (s *SingBoxService) Restart() error {
	if err := s.Stop(); err != nil {
		return err
	}
	return s.Start()
}
