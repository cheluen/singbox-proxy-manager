package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	Tag     string `json:"tag"`
	Address string `json:"address"`
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

	enabledNodes := make([]*models.ProxyNode, 0, len(nodes))
	for i := range nodes {
		if !nodes[i].Enabled {
			continue
		}
		if strings.Contains(nodes[i].Username, "+") {
			return nil, fmt.Errorf("node %d username must not contain '+'", nodes[i].ID)
		}
		enabledNodes = append(enabledNodes, &nodes[i])
	}

	reuseRoutes := make([]tcpReuseRoute, 0, len(enabledNodes))
	reuseRouteSet := make(map[string]struct{}, len(enabledNodes))
	for _, node := range enabledNodes {
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
			OutboundTag: fmt.Sprintf("node-%d-out", node.ID),
		})
	}

	directInboundRoutes := make([]RouteRule, 0, len(enabledNodes))

	// Generate inbounds and outbounds for each enabled node.
	for _, node := range enabledNodes {
		inboundTag := fmt.Sprintf("node-%d-in", node.ID)
		outboundTag := fmt.Sprintf("node-%d-out", node.ID)

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
			endpoint, err := s.generateWireGuardEndpointForNode(node, outboundTag)
			if err != nil {
				return nil, fmt.Errorf("failed to generate endpoint for node %d: %v", node.ID, err)
			}
			config.Endpoints = append(config.Endpoints, endpoint)
		} else {
			outbound, err := s.generateOutbound(node, outboundTag)
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

	// Add direct outbound
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

func (s *SingBoxService) generateOutbound(node *models.ProxyNode, tag string) (OutboundConfig, error) {
	parsedConfig, err := node.ParseConfig()
	if err != nil {
		return OutboundConfig{}, err
	}

	switch node.Type {
	case "direct":
		return s.generateDirectOutbound(parsedConfig.(*models.DirectConfig), tag)
	case "ss":
		return s.generateSSOutbound(parsedConfig.(*models.SSConfig), tag)
	case "vless":
		return s.generateVLESSOutbound(parsedConfig.(*models.VLESSConfig), tag)
	case "vmess":
		return s.generateVMESSOutbound(parsedConfig.(*models.VMESSConfig), tag)
	case "hy2":
		return s.generateHysteria2Outbound(parsedConfig.(*models.Hysteria2Config), tag)
	case "tuic":
		return s.generateTUICOutbound(parsedConfig.(*models.TUICConfig), tag)
	case "trojan":
		return s.generateTrojanOutbound(parsedConfig.(*models.TrojanConfig), tag)
	case "anytls":
		return s.generateAnyTLSOutbound(parsedConfig.(*models.AnyTLSConfig), tag)
	case "socks5", "socks5h":
		return s.generateSOCKS5Outbound(parsedConfig.(*models.SOCKS5Config), tag)
	case "http":
		return s.generateHTTPProxyOutbound(parsedConfig.(*models.HTTPProxyConfig), tag)
	default:
		return OutboundConfig{}, fmt.Errorf("unsupported proxy type: %s", node.Type)
	}
}

func (s *SingBoxService) generateDirectOutbound(config *models.DirectConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type: "direct",
		Tag:  tag,
	}

	extra := map[string]interface{}{}
	if config.OverrideAddress != "" {
		extra["override_address"] = config.OverrideAddress
	}
	if config.OverridePort != 0 {
		extra["override_port"] = config.OverridePort
	}
	if len(extra) > 0 {
		outbound.Extra = extra
	}

	return outbound, nil
}

func (s *SingBoxService) generateSSOutbound(config *models.SSConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "shadowsocks",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"method":   config.Method,
			"password": config.Password,
		},
	}

	if config.Plugin != "" {
		outbound.Extra["plugin"] = config.Plugin
		if config.PluginOpts != "" {
			outbound.Extra["plugin_opts"] = config.PluginOpts
		}
	}

	if config.UDPOverTCP {
		outbound.Extra["udp_over_tcp"] = true
	}

	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = config.MultiplexConfig
	}

	return outbound, nil
}

func (s *SingBoxService) generateVLESSOutbound(config *models.VLESSConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "vless",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"uuid": config.UUID,
		},
	}

	if config.Flow != "" {
		outbound.Extra["flow"] = config.Flow
	}

	if config.PacketEncoding != "" {
		outbound.Extra["packet_encoding"] = config.PacketEncoding
	}

	// TLS configuration
	if config.Security == "tls" || config.Security == "reality" {
		tls := map[string]interface{}{
			"enabled": true,
		}
		if config.SNI != "" {
			tls["server_name"] = config.SNI
		}
		if config.ALPN != "" {
			if alpn := splitCommaList(config.ALPN); len(alpn) > 0 {
				tls["alpn"] = alpn
			}
		}
		if config.Fingerprint != "" {
			tls["utls"] = map[string]interface{}{
				"enabled":     true,
				"fingerprint": config.Fingerprint,
			}
		}
		if config.Insecure {
			tls["insecure"] = true
		}

		// Reality specific
		if config.Security == "reality" {
			reality := map[string]interface{}{
				"enabled": true,
			}
			if config.PublicKey != "" {
				reality["public_key"] = config.PublicKey
			}
			if config.ShortID != "" {
				reality["short_id"] = config.ShortID
			}
			tls["reality"] = reality
		}

		outbound.Extra["tls"] = tls
	}

	// Transport configuration
	if config.Network != "" && config.Network != "tcp" {
		transport := map[string]interface{}{
			"type": config.Network,
		}

		switch config.Network {
		case "ws":
			if config.Path != "" {
				transport["path"] = config.Path
			}
			if config.Headers != nil {
				transport["headers"] = config.Headers
			} else if config.Host != "" {
				transport["headers"] = map[string]string{"Host": config.Host}
			}
			if config.MaxEarlyData > 0 {
				transport["max_early_data"] = config.MaxEarlyData
				if config.EarlyDataHeader != "" {
					transport["early_data_header_name"] = config.EarlyDataHeader
				}
			}
		case "grpc":
			if config.ServiceName != "" {
				transport["service_name"] = config.ServiceName
			}
		case "httpupgrade":
			if config.HTTPUpgradePath != "" {
				transport["path"] = config.HTTPUpgradePath
			}
			if config.HTTPUpgradeHost != "" {
				transport["host"] = config.HTTPUpgradeHost
			}
		case "quic", "kcp":
			if config.Seed != "" {
				transport["seed"] = config.Seed
			}
			if config.HeaderType != "" {
				transport["header"] = map[string]interface{}{
					"type": config.HeaderType,
				}
			}
		case "http":
			if config.Host != "" {
				transport["host"] = []string{config.Host}
			}
			if config.Path != "" {
				transport["path"] = config.Path
			}
		}

		outbound.Extra["transport"] = transport
	}

	// Multiplex
	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = config.MultiplexConfig
	}

	return outbound, nil
}

func (s *SingBoxService) generateVMESSOutbound(config *models.VMESSConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "vmess",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"uuid":     config.UUID,
			"alter_id": config.AlterID,
		},
	}

	if config.Security != "" {
		outbound.Extra["security"] = config.Security
	}

	if config.GlobalPadding {
		outbound.Extra["global_padding"] = true
	}

	if config.AuthenticatedLength {
		outbound.Extra["authenticated_length"] = true
	}

	if config.PacketEncoding != "" {
		outbound.Extra["packet_encoding"] = config.PacketEncoding
	}

	// TLS configuration
	if config.TLS == "tls" {
		tls := map[string]interface{}{
			"enabled": true,
		}
		if config.SNI != "" {
			tls["server_name"] = config.SNI
		}
		if config.ALPN != "" {
			if alpn := splitCommaList(config.ALPN); len(alpn) > 0 {
				tls["alpn"] = alpn
			}
		}
		if config.Fingerprint != "" {
			tls["utls"] = map[string]interface{}{
				"enabled":     true,
				"fingerprint": config.Fingerprint,
			}
		}
		if config.Insecure {
			tls["insecure"] = true
		}
		outbound.Extra["tls"] = tls
	}

	// Transport configuration
	if config.Network != "" && config.Network != "tcp" {
		transport := map[string]interface{}{
			"type": config.Network,
		}

		switch config.Network {
		case "ws":
			if config.Path != "" {
				transport["path"] = config.Path
			}
			if config.Headers != nil {
				transport["headers"] = config.Headers
			} else if config.Host != "" {
				transport["headers"] = map[string]string{"Host": config.Host}
			}
			if config.MaxEarlyData > 0 {
				transport["max_early_data"] = config.MaxEarlyData
				if config.EarlyDataHeader != "" {
					transport["early_data_header_name"] = config.EarlyDataHeader
				}
			}
		case "grpc":
			if config.ServiceName != "" {
				transport["service_name"] = config.ServiceName
			}
		case "httpupgrade":
			if config.HTTPUpgradePath != "" {
				transport["path"] = config.HTTPUpgradePath
			}
			if config.HTTPUpgradeHost != "" {
				transport["host"] = config.HTTPUpgradeHost
			}
		case "quic", "kcp":
			if config.Seed != "" {
				transport["seed"] = config.Seed
			}
			if config.HeaderType != "" {
				transport["header"] = map[string]interface{}{
					"type": config.HeaderType,
				}
			}
		case "http":
			if config.Host != "" {
				transport["host"] = []string{config.Host}
			}
			if len(config.HTTPPath) > 0 {
				transport["path"] = config.HTTPPath
			} else if config.Path != "" {
				transport["path"] = []string{config.Path}
			}
			if config.Method != "" {
				transport["method"] = config.Method
			}
		}

		outbound.Extra["transport"] = transport
	}

	// Multiplex
	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = config.MultiplexConfig
	}

	return outbound, nil
}

func splitCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func (s *SingBoxService) generateHysteria2Outbound(config *models.Hysteria2Config, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "hysteria2",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"password": config.Password,
		},
	}

	// Bandwidth settings (use brutal if specified, otherwise legacy up/down)
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

	// Obfuscation
	if config.Obfs != "" {
		obfs := map[string]interface{}{
			"type": config.Obfs,
		}
		if config.ObfsPassword != "" {
			obfs["password"] = config.ObfsPassword
		}
		outbound.Extra["obfs"] = obfs
	}

	// Salamander obfuscation
	if config.SalamanderPassword != "" {
		outbound.Extra["salamander"] = map[string]interface{}{
			"password": config.SalamanderPassword,
		}
	}

	// Network type
	if config.Network != "" {
		outbound.Extra["network"] = config.Network
	}

	// Hop interval
	if config.HopInterval != "" {
		outbound.Extra["hop_interval"] = config.HopInterval
	}

	// TLS configuration
	tls := map[string]interface{}{
		"enabled": true,
	}
	if config.SNI != "" {
		tls["server_name"] = config.SNI
	}
	if len(config.ALPN) > 0 {
		tls["alpn"] = config.ALPN
	}
	if config.Fingerprint != "" {
		tls["utls"] = map[string]interface{}{
			"enabled":     true,
			"fingerprint": config.Fingerprint,
		}
	}
	if config.InsecureSkipVerify {
		tls["insecure"] = true
	}
	outbound.Extra["tls"] = tls

	return outbound, nil
}

func (s *SingBoxService) generateTUICOutbound(config *models.TUICConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "tuic",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"uuid":     config.UUID,
			"password": config.Password,
		},
	}

	if config.CongestionControl != "" {
		outbound.Extra["congestion_control"] = config.CongestionControl
	}
	if config.UDPRelayMode != "" {
		outbound.Extra["udp_relay_mode"] = config.UDPRelayMode
	}
	if config.ZeroRTTHandshake {
		outbound.Extra["zero_rtt_handshake"] = config.ZeroRTTHandshake
	}
	if config.Heartbeat != "" {
		outbound.Extra["heartbeat"] = config.Heartbeat
	}
	if config.Network != "" {
		outbound.Extra["network"] = config.Network
	}
	if config.DisableSNI {
		outbound.Extra["disable_sni"] = true
	}
	if config.ReduceRTT {
		outbound.Extra["reduce_rtt"] = true
	}

	// TLS configuration
	tls := map[string]interface{}{
		"enabled": true,
	}
	if config.SNI != "" {
		tls["server_name"] = config.SNI
	}
	if len(config.ALPN) > 0 {
		tls["alpn"] = config.ALPN
	}
	if config.Fingerprint != "" {
		tls["utls"] = map[string]interface{}{
			"enabled":     true,
			"fingerprint": config.Fingerprint,
		}
	}
	if config.InsecureSkipVerify {
		tls["insecure"] = true
	}
	outbound.Extra["tls"] = tls

	return outbound, nil
}

func (s *SingBoxService) generateTrojanOutbound(config *models.TrojanConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "trojan",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"password": config.Password,
		},
	}

	// TLS configuration (Trojan requires TLS)
	tls := map[string]interface{}{
		"enabled": true,
	}
	if config.SNI != "" {
		tls["server_name"] = config.SNI
	}
	if len(config.ALPN) > 0 {
		tls["alpn"] = config.ALPN
	}
	if config.Fingerprint != "" {
		tls["utls"] = map[string]interface{}{
			"enabled":     true,
			"fingerprint": config.Fingerprint,
		}
	}
	if config.Insecure {
		tls["insecure"] = true
	}
	outbound.Extra["tls"] = tls

	// Transport configuration
	if config.Network != "" && config.Network != "tcp" {
		transport := map[string]interface{}{
			"type": config.Network,
		}

		switch config.Network {
		case "ws":
			if config.Path != "" {
				transport["path"] = config.Path
			}
			if config.Host != "" || len(config.Headers) > 0 {
				headers := map[string]string{}
				if config.Host != "" {
					headers["Host"] = config.Host
				}
				for k, v := range config.Headers {
					headers[k] = v
				}
				if len(headers) > 0 {
					transport["headers"] = headers
				}
			}
		case "grpc":
			if config.ServiceName != "" {
				transport["service_name"] = config.ServiceName
			}
		case "http", "h2":
			if config.Host != "" {
				transport["host"] = []string{config.Host}
			}
			if config.Path != "" {
				transport["path"] = config.Path
			}
			if config.HTTPMethod != "" {
				transport["method"] = config.HTTPMethod
			}
		case "httpupgrade":
			if config.Path != "" {
				transport["path"] = config.Path
			}
			if config.Host != "" {
				transport["host"] = config.Host
			}
		}

		outbound.Extra["transport"] = transport
	}

	// Multiplex
	if config.MultiplexConfig != nil {
		outbound.Extra["multiplex"] = config.MultiplexConfig
	}

	return outbound, nil
}

func (s *SingBoxService) generateAnyTLSOutbound(config *models.AnyTLSConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "anytls",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"password": config.Password,
		},
	}

	// Session management options
	if config.IdleSessionCheckInterval != "" {
		outbound.Extra["idle_session_check_interval"] = config.IdleSessionCheckInterval
	}
	if config.IdleSessionTimeout != "" {
		outbound.Extra["idle_session_timeout"] = config.IdleSessionTimeout
	}
	if config.MinIdleSession > 0 {
		outbound.Extra["min_idle_session"] = config.MinIdleSession
	}

	// TLS configuration (required for AnyTLS)
	tls := map[string]interface{}{
		"enabled": true,
	}
	if config.SNI != "" {
		tls["server_name"] = config.SNI
	}
	if len(config.ALPN) > 0 {
		tls["alpn"] = config.ALPN
	}
	if config.Fingerprint != "" {
		tls["utls"] = map[string]interface{}{
			"enabled":     true,
			"fingerprint": config.Fingerprint,
		}
	}
	if config.Insecure {
		tls["insecure"] = true
	}
	outbound.Extra["tls"] = tls

	return outbound, nil
}

func (s *SingBoxService) generateSOCKS5Outbound(config *models.SOCKS5Config, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "socks",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra: map[string]interface{}{
			"version": "5",
		},
	}

	if config.Username != "" {
		outbound.Extra["username"] = config.Username
	}
	if config.Password != "" {
		outbound.Extra["password"] = config.Password
	}

	return outbound, nil
}

func (s *SingBoxService) generateHTTPProxyOutbound(config *models.HTTPProxyConfig, tag string) (OutboundConfig, error) {
	outbound := OutboundConfig{
		Type:   "http",
		Tag:    tag,
		Server: config.Server,
		Port:   config.ServerPort,
		Extra:  map[string]interface{}{},
	}

	if config.Username != "" {
		outbound.Extra["username"] = config.Username
	}
	if config.Password != "" {
		outbound.Extra["password"] = config.Password
	}

	if config.TLS {
		tls := map[string]interface{}{
			"enabled": true,
		}
		if config.SNI != "" {
			tls["server_name"] = config.SNI
		}
		if config.Insecure {
			tls["insecure"] = true
		}
		outbound.Extra["tls"] = tls
	}

	return outbound, nil
}

// generateWireGuardEndpointForNode parses the node config and renders a
// WireGuard endpoint in the sing-box 1.11+ format. Field mapping from the
// legacy outbound format (per official migration guide):
// local_address→address, system_interface→system, interface_name→name,
// peer server→address, peer server_port→port; single-peer flat fields are
// folded into the peers array, which is the only representation endpoints
// accept. The legacy outbound-only "network" limiter has no endpoint
// equivalent and is dropped. Dial fields (detour, domain_resolver,
// udp_fragment, connect_timeout, routing_mark) carry over unchanged.
func (s *SingBoxService) generateWireGuardEndpointForNode(node *models.ProxyNode, tag string) (EndpointConfig, error) {
	parsedConfig, err := node.ParseConfig()
	if err != nil {
		return EndpointConfig{}, err
	}
	config, ok := parsedConfig.(*models.WireGuardConfig)
	if !ok {
		return EndpointConfig{}, fmt.Errorf("unexpected config type for wireguard node")
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

	endpoint := EndpointConfig{
		Type:  "wireguard",
		Tag:   tag,
		Extra: map[string]interface{}{},
	}

	endpoint.Extra["address"] = config.LocalAddress
	endpoint.Extra["private_key"] = config.PrivateKey
	if config.SystemInterface {
		endpoint.Extra["system"] = true
	}
	if config.InterfaceName != "" {
		endpoint.Extra["name"] = config.InterfaceName
	}
	if config.Workers > 0 {
		endpoint.Extra["workers"] = config.Workers
	}
	if config.MTU > 0 {
		endpoint.Extra["mtu"] = config.MTU
	}
	if config.Detour != "" {
		endpoint.Extra["detour"] = config.Detour
	}
	if config.DomainResolver != "" {
		if config.DomainResolverStrategy != "" {
			endpoint.Extra["domain_resolver"] = map[string]interface{}{
				"server":   config.DomainResolver,
				"strategy": config.DomainResolverStrategy,
			}
		} else {
			endpoint.Extra["domain_resolver"] = config.DomainResolver
		}
	}
	if config.UDPFragment != nil {
		endpoint.Extra["udp_fragment"] = *config.UDPFragment
	}
	if config.ConnectTimeout != "" {
		endpoint.Extra["connect_timeout"] = config.ConnectTimeout
	}
	if routingMark := parseWireGuardRoutingMark(config.RoutingMark); routingMark != nil {
		endpoint.Extra["routing_mark"] = routingMark
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
	for _, peer := range peers {
		if strings.TrimSpace(peer.Server) == "" || peer.ServerPort <= 0 || strings.TrimSpace(peer.PublicKey) == "" {
			return EndpointConfig{}, fmt.Errorf("wireguard peer requires server, server_port and public_key")
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
		if len(allowedIPs) == 0 {
			// The legacy single-peer format had no allowed_ips and routed all
			// traffic through the peer; endpoints require them explicitly.
			allowedIPs = []string{"0.0.0.0/0", "::/0"}
		}
		peerConfig["allowed_ips"] = allowedIPs
		if len(peer.Reserved) > 0 {
			// []uint8 would marshal to a base64 string; emit the documented
			// numeric-array form instead.
			reserved := make([]int, 0, len(peer.Reserved))
			for _, b := range peer.Reserved {
				reserved = append(reserved, int(b))
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
