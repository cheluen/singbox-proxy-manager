package services

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"sb-proxy/backend/models"
)

type SingBoxService struct {
	configDir string
	process   *exec.Cmd
	logFile   *os.File
	mu        sync.RWMutex
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
	Inbounds  []InboundConfig  `json:"inbounds"`
	Outbounds []OutboundConfig `json:"outbounds"`
	Route     RouteConfig      `json:"route,omitempty"`
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

// GenerateGlobalConfig generates a single sing-box configuration for all enabled nodes
func (s *SingBoxService) GenerateGlobalConfig(nodes []models.ProxyNode) error {
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

	// Generate inbounds, outbounds, and route rules for each enabled node
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}

		inboundTag := fmt.Sprintf("node-%d-in", node.ID)
		outboundTag := fmt.Sprintf("node-%d-out", node.ID)

		// Generate inbound
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

		// Add authentication if provided
		if node.Username != "" && node.Password != "" {
			inbound.Users = []InboundUser{
				{
					Username: node.Username,
					Password: node.Password,
				},
			}
		}

		config.Inbounds = append(config.Inbounds, inbound)

		// Generate outbound
		outbound, err := s.generateOutbound(&node, outboundTag)
		if err != nil {
			return fmt.Errorf("failed to generate outbound for node %d: %v", node.ID, err)
		}
		config.Outbounds = append(config.Outbounds, outbound)

		// Generate simple route rule: direct inbound to outbound mapping
		config.Route.Rules = append(config.Route.Rules, RouteRule{
			Inbound:  []string{inboundTag},
			Outbound: outboundTag,
		})
	}

	// Add direct outbound
	config.Outbounds = append(config.Outbounds, OutboundConfig{
		Type: "direct",
		Tag:  "direct",
	})

	// Marshal config to JSON
	configJSON, err := s.marshalConfig(config)
	if err != nil {
		return err
	}

	// Write config to file
	configPath := filepath.Join(s.configDir, "config.json")
	return os.WriteFile(configPath, configJSON, 0644)
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
	case "socks5":
		return s.generateSOCKS5Outbound(parsedConfig.(*models.SOCKS5Config), tag)
	case "http":
		return s.generateHTTPProxyOutbound(parsedConfig.(*models.HTTPProxyConfig), tag)
	default:
		return OutboundConfig{}, fmt.Errorf("unsupported proxy type: %s", node.Type)
	}
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
			tls["alpn"] = []string{config.ALPN}
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

		if config.Network == "ws" {
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
		} else if config.Network == "grpc" {
			if config.ServiceName != "" {
				transport["service_name"] = config.ServiceName
			}
		} else if config.Network == "httpupgrade" {
			if config.HTTPUpgradePath != "" {
				transport["path"] = config.HTTPUpgradePath
			}
			if config.HTTPUpgradeHost != "" {
				transport["host"] = config.HTTPUpgradeHost
			}
		} else if config.Network == "quic" || config.Network == "kcp" {
			if config.Seed != "" {
				transport["seed"] = config.Seed
			}
			if config.HeaderType != "" {
				transport["header"] = map[string]interface{}{
					"type": config.HeaderType,
				}
			}
		} else if config.Network == "http" {
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
			tls["alpn"] = []string{config.ALPN}
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

		if config.Network == "ws" {
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
		} else if config.Network == "grpc" {
			if config.ServiceName != "" {
				transport["service_name"] = config.ServiceName
			}
		} else if config.Network == "httpupgrade" {
			if config.HTTPUpgradePath != "" {
				transport["path"] = config.HTTPUpgradePath
			}
			if config.HTTPUpgradeHost != "" {
				transport["host"] = config.HTTPUpgradeHost
			}
		} else if config.Network == "quic" || config.Network == "kcp" {
			if config.Seed != "" {
				transport["seed"] = config.Seed
			}
			if config.HeaderType != "" {
				transport["header"] = map[string]interface{}{
					"type": config.HeaderType,
				}
			}
		} else if config.Network == "http" {
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
		outbound.Extra["network"] = config.Network
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

// Start starts the single sing-box process
func (s *SingBoxService) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Stop existing process if any
	if s.process != nil {
		if s.process.Process != nil {
			if err := s.process.Process.Kill(); err != nil {
				// Log but continue - process may already be dead
				fmt.Printf("Warning: failed to kill existing process: %v\n", err)
			}
			s.process.Wait() // Prevent zombie process, ignore error as process may be already reaped
		}
		s.process = nil
	}

	// Close existing log file handle if any
	if s.logFile != nil {
		s.logFile.Close()
		s.logFile = nil
	}

	configPath := filepath.Join(s.configDir, "config.json")

	// Check if config exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("config file not found: %s", configPath)
	}

	cmd := exec.Command("sing-box", "run", "-c", configPath)

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

	s.process = cmd
	s.logFile = logFile // Save log file handle for later cleanup
	return nil
}

// Stop stops the sing-box process
func (s *SingBoxService) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.process == nil {
		return nil
	}

	if s.process.Process != nil {
		if err := s.process.Process.Kill(); err != nil {
			return err
		}
		// Wait for process to prevent zombie
		s.process.Wait()
	}

	s.process = nil

	// Close log file handle
	if s.logFile != nil {
		s.logFile.Close()
		s.logFile = nil
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

// RegenerateAndRestart regenerates config from all nodes in database and restarts
func (s *SingBoxService) RegenerateAndRestart(db interface{}) error {
	// This method is for compatibility, actual implementation in handlers
	return s.Restart()
}
