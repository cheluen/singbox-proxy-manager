package services

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"sb-proxy/backend/models"
)

// ParseShareLink parses various proxy share link formats
func ParseShareLink(link string) (interface{}, string, string, error) {
	link = strings.TrimSpace(link)

	if strings.HasPrefix(link, "ss://") {
		return parseSSLink(link)
	} else if strings.HasPrefix(link, "vless://") {
		return parseVLESSLink(link)
	} else if strings.HasPrefix(link, "vmess://") {
		return parseVMESSLink(link)
	} else if strings.HasPrefix(link, "trojan://") {
		return parseTrojanLink(link)
	} else if strings.HasPrefix(link, "hysteria2://") || strings.HasPrefix(link, "hy2://") {
		return parseHysteria2Link(link)
	} else if strings.HasPrefix(link, "tuic://") {
		return parseTUICLink(link)
	} else if strings.HasPrefix(link, "anytls://") {
		return parseAnyTLSLink(link)
	} else if strings.HasPrefix(link, "socks5://") || strings.HasPrefix(link, "socks://") {
		return parseSOCKS5Link(link)
	} else if strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://") {
		return parseHTTPProxyLink(link)
	}

	return nil, "", "", fmt.Errorf("unsupported link format")
}

// parseSSLink parses Shadowsocks share links
// Format: ss://base64(method:password)@server:port?params#name
func parseSSLink(link string) (interface{}, string, string, error) {
	link = strings.TrimPrefix(link, "ss://")

	// Split name if exists
	parts := strings.SplitN(link, "#", 2)
	link = parts[0]
	name := "SS Node"
	if len(parts) == 2 {
		name, _ = url.QueryUnescape(parts[1])
	}

	// Split params
	parts = strings.SplitN(link, "?", 2)
	link = parts[0]
	params := url.Values{}
	if len(parts) == 2 {
		params, _ = url.ParseQuery(parts[1])
	}

	// Split server part
	atIndex := strings.LastIndex(link, "@")
	if atIndex == -1 {
		return nil, "", "", fmt.Errorf("invalid ss link format")
	}

	// Decode credentials
	credentials := link[:atIndex]
	serverPart := link[atIndex+1:]

	decoded, err := base64.RawURLEncoding.DecodeString(credentials)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(credentials)
		if err != nil {
			return nil, "", "", fmt.Errorf("failed to decode credentials")
		}
	}

	// Parse method:password
	credParts := strings.SplitN(string(decoded), ":", 2)
	if len(credParts) != 2 {
		return nil, "", "", fmt.Errorf("invalid credentials format")
	}

	method := credParts[0]
	password := credParts[1]

	// Parse server:port
	serverParts := strings.SplitN(serverPart, ":", 2)
	if len(serverParts) != 2 {
		return nil, "", "", fmt.Errorf("invalid server format")
	}

	server := serverParts[0]
	port, err := strconv.Atoi(serverParts[1])
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	config := models.SSConfig{
		Server:     server,
		ServerPort: port,
		Method:     method,
		Password:   password,
		Plugin:     params.Get("plugin"),
		PluginOpts: params.Get("plugin-opts"),
	}

	// Parse additional parameters
	if params.Get("udp_over_tcp") == "1" || params.Get("udp_over_tcp") == "true" {
		config.UDPOverTCP = true
	}

	return config, "ss", name, nil
}

// parseVLESSLink parses VLESS share links
// Format: vless://uuid@server:port?params#name
func parseVLESSLink(link string) (interface{}, string, string, error) {
	link = strings.TrimPrefix(link, "vless://")

	// Split name if exists
	parts := strings.SplitN(link, "#", 2)
	link = parts[0]
	name := "VLESS Node"
	if len(parts) == 2 {
		name, _ = url.QueryUnescape(parts[1])
	}

	// Split params
	parts = strings.SplitN(link, "?", 2)
	basicPart := parts[0]
	params := url.Values{}
	if len(parts) == 2 {
		params, _ = url.ParseQuery(parts[1])
	}

	// Parse uuid@server:port
	atIndex := strings.LastIndex(basicPart, "@")
	if atIndex == -1 {
		return nil, "", "", fmt.Errorf("invalid vless link format")
	}

	uuid := basicPart[:atIndex]
	serverPart := basicPart[atIndex+1:]

	serverParts := strings.SplitN(serverPart, ":", 2)
	if len(serverParts) != 2 {
		return nil, "", "", fmt.Errorf("invalid server format")
	}

	server := serverParts[0]
	port, err := strconv.Atoi(serverParts[1])
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	config := models.VLESSConfig{
		Server:         server,
		ServerPort:     port,
		UUID:           uuid,
		Flow:           params.Get("flow"),
		Encryption:     params.Get("encryption"),
		Network:        params.Get("type"),
		Security:       params.Get("security"),
		SNI:            params.Get("sni"),
		Fingerprint:    params.Get("fp"),
		PublicKey:      params.Get("pbk"),
		ShortID:        params.Get("sid"),
		SpiderX:        params.Get("spx"),
		Path:           params.Get("path"),
		Host:           params.Get("host"),
		ServiceName:    params.Get("serviceName"),
		HeaderType:     params.Get("headerType"),
		Seed:           params.Get("seed"),
		PacketEncoding: params.Get("packetEncoding"),
	}

	// Parse insecure
	if params.Get("allowInsecure") == "1" || params.Get("insecure") == "1" {
		config.Insecure = true
	}

	// Handle ALPN
	if alpn := params.Get("alpn"); alpn != "" {
		config.ALPN = alpn
	}

	// Handle max early data
	if med := params.Get("maxEarlyData"); med != "" {
		if medInt, err := strconv.Atoi(med); err == nil {
			config.MaxEarlyData = medInt
		}
	}
	config.EarlyDataHeader = params.Get("earlyDataHeaderName")

	// HTTPUpgrade specific
	if config.Network == "httpupgrade" {
		config.HTTPUpgradePath = params.Get("path")
		config.HTTPUpgradeHost = params.Get("host")
	}

	return config, "vless", name, nil
}

// parseVMESSLink parses VMess share links
// Format: vmess://base64(json)
func parseVMESSLink(link string) (interface{}, string, string, error) {
	link = strings.TrimPrefix(link, "vmess://")

	decoded, err := base64.RawURLEncoding.DecodeString(link)
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(link)
		if err != nil {
			return nil, "", "", fmt.Errorf("failed to decode vmess link")
		}
	}

	var vmessJSON struct {
		Add      string      `json:"add"`
		Port     interface{} `json:"port"`
		ID       string      `json:"id"`
		AID      interface{} `json:"aid"`
		Net      string      `json:"net"`
		Type     string      `json:"type"`
		Host     string      `json:"host"`
		Path     string      `json:"path"`
		TLS      string      `json:"tls"`
		SNI      string      `json:"sni"`
		ALPN     string      `json:"alpn"`
		FP       string      `json:"fp"`
		PS       string      `json:"ps"`
		V        string      `json:"v"`
		Scy      string      `json:"scy"`
		Insecure interface{} `json:"allowInsecure"`
		// Additional fields
		MaxEarlyData    interface{} `json:"maxEarlyData"`
		EarlyDataHeader string      `json:"earlyDataHeaderName"`
		Seed            string      `json:"seed"`
		GlobalPadding   interface{} `json:"globalPadding"`
		AuthLength      interface{} `json:"authenticatedLength"`
	}

	if err := json.Unmarshal(decoded, &vmessJSON); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse vmess json")
	}

	name := vmessJSON.PS
	if name == "" {
		name = "VMess Node"
	}

	port := 0
	switch v := vmessJSON.Port.(type) {
	case string:
		port, _ = strconv.Atoi(v)
	case float64:
		port = int(v)
	case int:
		port = v
	}

	alterID := 0
	switch v := vmessJSON.AID.(type) {
	case string:
		alterID, _ = strconv.Atoi(v)
	case float64:
		alterID = int(v)
	case int:
		alterID = v
	}

	config := models.VMESSConfig{
		Server:      vmessJSON.Add,
		ServerPort:  port,
		UUID:        vmessJSON.ID,
		AlterID:     alterID,
		Security:    vmessJSON.Scy,
		Network:     vmessJSON.Net,
		TLS:         vmessJSON.TLS,
		SNI:         vmessJSON.SNI,
		ALPN:        vmessJSON.ALPN,
		Fingerprint: vmessJSON.FP,
		Path:        vmessJSON.Path,
		Host:        vmessJSON.Host,
		HeaderType:  vmessJSON.Type,
		Seed:        vmessJSON.Seed,
	}

	// Parse insecure
	switch v := vmessJSON.Insecure.(type) {
	case bool:
		config.Insecure = v
	case string:
		config.Insecure = v == "1" || v == "true"
	case float64:
		config.Insecure = v == 1
	}

	// Parse max early data
	switch v := vmessJSON.MaxEarlyData.(type) {
	case string:
		if medInt, err := strconv.Atoi(v); err == nil {
			config.MaxEarlyData = medInt
		}
	case float64:
		config.MaxEarlyData = int(v)
	case int:
		config.MaxEarlyData = v
	}
	config.EarlyDataHeader = vmessJSON.EarlyDataHeader

	// Parse global padding
	switch v := vmessJSON.GlobalPadding.(type) {
	case bool:
		config.GlobalPadding = v
	case string:
		config.GlobalPadding = v == "1" || v == "true"
	case float64:
		config.GlobalPadding = v == 1
	}

	// Parse authenticated length
	switch v := vmessJSON.AuthLength.(type) {
	case bool:
		config.AuthenticatedLength = v
	case string:
		config.AuthenticatedLength = v == "1" || v == "true"
	case float64:
		config.AuthenticatedLength = v == 1
	}

	// Handle service name for gRPC
	if vmessJSON.Net == "grpc" {
		config.ServiceName = vmessJSON.Path
	}

	// HTTPUpgrade specific
	if vmessJSON.Net == "httpupgrade" {
		config.HTTPUpgradePath = vmessJSON.Path
		config.HTTPUpgradeHost = vmessJSON.Host
	}

	return config, "vmess", name, nil
}

// parseTrojanLink parses Trojan share links
// Format: trojan://password@server:port?params#name
func parseTrojanLink(link string) (interface{}, string, string, error) {
	link = strings.TrimPrefix(link, "trojan://")

	// Split name if exists
	parts := strings.SplitN(link, "#", 2)
	link = parts[0]
	name := "Trojan Node"
	if len(parts) == 2 {
		name, _ = url.QueryUnescape(parts[1])
	}

	// Split params
	parts = strings.SplitN(link, "?", 2)
	basicPart := parts[0]
	params := url.Values{}
	if len(parts) == 2 {
		params, _ = url.ParseQuery(parts[1])
	}

	// Parse password@server:port
	atIndex := strings.LastIndex(basicPart, "@")
	if atIndex == -1 {
		return nil, "", "", fmt.Errorf("invalid trojan link format")
	}
	password := basicPart[:atIndex]
	serverPart := basicPart[atIndex+1:]

	serverParts := strings.SplitN(serverPart, ":", 2)
	if len(serverParts) != 2 {
		return nil, "", "", fmt.Errorf("invalid server format")
	}

	server := serverParts[0]
	port, err := strconv.Atoi(serverParts[1])
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	network := params.Get("type")
	if network == "" {
		network = params.Get("transport")
	}

	insecure := params.Get("allowInsecure") == "1" || params.Get("insecure") == "1"
	sni := params.Get("sni")
	if sni == "" {
		sni = params.Get("peer")
	}

	alpn := []string{}
	if raw := params.Get("alpn"); raw != "" {
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				alpn = append(alpn, v)
			}
		}
	}

	cfg := models.TrojanConfig{
		Server:      server,
		ServerPort:  port,
		Password:    password,
		Network:     network,
		SNI:         sni,
		ALPN:        alpn,
		Fingerprint: params.Get("fp"),
		Insecure:    insecure,
		Host:        params.Get("host"),
		Path:        params.Get("path"),
		ServiceName: params.Get("serviceName"),
		HTTPMethod:  params.Get("method"),
	}

	if cfg.ServiceName == "" && params.Get("service_name") != "" {
		cfg.ServiceName = params.Get("service_name")
	}
	if cfg.ServiceName == "" && params.Get("grpc-service-name") != "" {
		cfg.ServiceName = params.Get("grpc-service-name")
	}

	return cfg, "trojan", name, nil
}

// parseHysteria2Link parses Hysteria2 share links
// Format: hysteria2://password@server:port?params#name or hy2://...
func parseHysteria2Link(link string) (interface{}, string, string, error) {
	link = strings.TrimPrefix(link, "hysteria2://")
	link = strings.TrimPrefix(link, "hy2://")

	// Split name if exists
	parts := strings.SplitN(link, "#", 2)
	link = parts[0]
	name := "Hysteria2 Node"
	if len(parts) == 2 {
		name, _ = url.QueryUnescape(parts[1])
	}

	// Split params
	parts = strings.SplitN(link, "?", 2)
	basicPart := parts[0]
	params := url.Values{}
	if len(parts) == 2 {
		params, _ = url.ParseQuery(parts[1])
	}

	// Parse password@server:port
	atIndex := strings.LastIndex(basicPart, "@")
	if atIndex == -1 {
		return nil, "", "", fmt.Errorf("invalid hysteria2 link format")
	}

	password := basicPart[:atIndex]
	serverPart := basicPart[atIndex+1:]

	serverParts := strings.SplitN(serverPart, ":", 2)
	if len(serverParts) != 2 {
		return nil, "", "", fmt.Errorf("invalid server format")
	}

	server := serverParts[0]
	port, err := strconv.Atoi(serverParts[1])
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	upMbps, _ := strconv.Atoi(params.Get("up"))
	downMbps, _ := strconv.Atoi(params.Get("down"))
	brutalUpMbps, _ := strconv.Atoi(params.Get("brutal_up_mbps"))
	brutalDownMbps, _ := strconv.Atoi(params.Get("brutal_down_mbps"))
	insecure := params.Get("insecure") == "1" || params.Get("allowInsecure") == "1"

	config := models.Hysteria2Config{
		Server:             server,
		ServerPort:         port,
		Password:           password,
		UpMbps:             upMbps,
		DownMbps:           downMbps,
		BrutalUpMbps:       brutalUpMbps,
		BrutalDownMbps:     brutalDownMbps,
		Obfs:               params.Get("obfs"),
		ObfsPassword:       params.Get("obfs-password"),
		SalamanderPassword: params.Get("salamander"),
		SNI:                params.Get("sni"),
		Fingerprint:        params.Get("fp"),
		InsecureSkipVerify: insecure,
		Network:            params.Get("network"),
		HopInterval:        params.Get("hopInterval"),
	}

	// Handle ALPN
	if alpn := params.Get("alpn"); alpn != "" {
		config.ALPN = strings.Split(alpn, ",")
	}

	return config, "hy2", name, nil
}

// parseTUICLink parses TUIC share links
// Format: tuic://uuid:password@server:port?params#name
func parseTUICLink(link string) (interface{}, string, string, error) {
	link = strings.TrimPrefix(link, "tuic://")

	// Split name if exists
	parts := strings.SplitN(link, "#", 2)
	link = parts[0]
	name := "TUIC Node"
	if len(parts) == 2 {
		name, _ = url.QueryUnescape(parts[1])
	}

	// Split params
	parts = strings.SplitN(link, "?", 2)
	basicPart := parts[0]
	params := url.Values{}
	if len(parts) == 2 {
		params, _ = url.ParseQuery(parts[1])
	}

	// Parse uuid:password@server:port
	atIndex := strings.LastIndex(basicPart, "@")
	if atIndex == -1 {
		return nil, "", "", fmt.Errorf("invalid tuic link format")
	}

	credPart := basicPart[:atIndex]
	serverPart := basicPart[atIndex+1:]

	credParts := strings.SplitN(credPart, ":", 2)
	if len(credParts) != 2 {
		return nil, "", "", fmt.Errorf("invalid credentials format")
	}

	uuid := credParts[0]
	password := credParts[1]

	serverParts := strings.SplitN(serverPart, ":", 2)
	if len(serverParts) != 2 {
		return nil, "", "", fmt.Errorf("invalid server format")
	}

	server := serverParts[0]
	port, err := strconv.Atoi(serverParts[1])
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	insecure := params.Get("insecure") == "1" || params.Get("allowInsecure") == "1"
	zeroRTT := params.Get("zero_rtt_handshake") == "1"
	disableSNI := params.Get("disable_sni") == "1"
	reduceRTT := params.Get("reduce_rtt") == "1"

	config := models.TUICConfig{
		Server:             server,
		ServerPort:         port,
		UUID:               uuid,
		Password:           password,
		CongestionControl:  params.Get("congestion_control"),
		UDPRelayMode:       params.Get("udp_relay_mode"),
		SNI:                params.Get("sni"),
		Fingerprint:        params.Get("fp"),
		InsecureSkipVerify: insecure,
		ZeroRTTHandshake:   zeroRTT,
		DisableSNI:         disableSNI,
		ReduceRTT:          reduceRTT,
		Heartbeat:          params.Get("heartbeat"),
		Network:            params.Get("network"),
	}

	// Handle ALPN
	if alpn := params.Get("alpn"); alpn != "" {
		config.ALPN = strings.Split(alpn, ",")
	}

	return config, "tuic", name, nil
}

// parseAnyTLSLink parses AnyTLS share links
// Format: anytls://password@server:port?params#name
func parseAnyTLSLink(link string) (interface{}, string, string, error) {
	link = strings.TrimPrefix(link, "anytls://")

	// Split name if exists
	parts := strings.SplitN(link, "#", 2)
	link = parts[0]
	name := "AnyTLS Node"
	if len(parts) == 2 {
		name, _ = url.QueryUnescape(parts[1])
	}

	// Split params
	parts = strings.SplitN(link, "?", 2)
	basicPart := parts[0]
	params := url.Values{}
	if len(parts) == 2 {
		params, _ = url.ParseQuery(parts[1])
	}

	// Parse password@server:port
	atIndex := strings.LastIndex(basicPart, "@")
	if atIndex == -1 {
		return nil, "", "", fmt.Errorf("invalid anytls link format")
	}
	password := basicPart[:atIndex]
	serverPart := basicPart[atIndex+1:]

	// Handle IPv6 addresses
	var server string
	var port int
	if strings.HasPrefix(serverPart, "[") {
		// IPv6 format: [ipv6]:port
		closeBracket := strings.LastIndex(serverPart, "]")
		if closeBracket == -1 {
			return nil, "", "", fmt.Errorf("invalid IPv6 format")
		}
		server = serverPart[1:closeBracket]
		portStr := strings.TrimPrefix(serverPart[closeBracket+1:], ":")
		var err error
		port, err = strconv.Atoi(portStr)
		if err != nil {
			return nil, "", "", fmt.Errorf("invalid port")
		}
	} else {
		// IPv4 or domain format: host:port
		serverParts := strings.SplitN(serverPart, ":", 2)
		if len(serverParts) != 2 {
			return nil, "", "", fmt.Errorf("invalid server format")
		}
		server = serverParts[0]
		var err error
		port, err = strconv.Atoi(serverParts[1])
		if err != nil {
			return nil, "", "", fmt.Errorf("invalid port")
		}
	}

	insecure := params.Get("insecure") == "1" || params.Get("allowInsecure") == "1"
	sni := params.Get("sni")
	if sni == "" {
		sni = server
	}

	alpn := []string{}
	if raw := params.Get("alpn"); raw != "" {
		for _, v := range strings.Split(raw, ",") {
			v = strings.TrimSpace(v)
			if v != "" {
				alpn = append(alpn, v)
			}
		}
	}

	config := models.AnyTLSConfig{
		Server:                   server,
		ServerPort:               port,
		Password:                 password,
		SNI:                      sni,
		ALPN:                     alpn,
		Fingerprint:              params.Get("fp"),
		Insecure:                 insecure,
		IdleSessionCheckInterval: params.Get("idle_session_check_interval"),
		IdleSessionTimeout:       params.Get("idle_session_timeout"),
	}

	if minIdle := params.Get("min_idle_session"); minIdle != "" {
		if val, err := strconv.Atoi(minIdle); err == nil {
			config.MinIdleSession = val
		}
	}

	return config, "anytls", name, nil
}

func parseSOCKS5Link(link string) (interface{}, string, string, error) {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid socks5 link: %v", err)
	}

	name := "SOCKS5 Node"
	if parsed.Fragment != "" {
		name, _ = url.QueryUnescape(parsed.Fragment)
	}

	portStr := parsed.Port()
	if portStr == "" {
		return nil, "", "", fmt.Errorf("missing port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	cfg := models.SOCKS5Config{
		Server:     parsed.Hostname(),
		ServerPort: port,
	}

	if parsed.User != nil {
		cfg.Username = parsed.User.Username()
		if pwd, ok := parsed.User.Password(); ok {
			cfg.Password = pwd
		}
	}

	return cfg, "socks5", name, nil
}

func parseHTTPProxyLink(link string) (interface{}, string, string, error) {
	parsed, err := url.Parse(link)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid http proxy link: %v", err)
	}

	name := "HTTP Proxy"
	if parsed.Fragment != "" {
		name, _ = url.QueryUnescape(parsed.Fragment)
	}

	portStr := parsed.Port()
	if portStr == "" {
		return nil, "", "", fmt.Errorf("missing port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	q := parsed.Query()
	cfg := models.HTTPProxyConfig{
		Server:     parsed.Hostname(),
		ServerPort: port,
		TLS:        parsed.Scheme == "https",
		SNI:        q.Get("sni"),
		Insecure:   q.Get("insecure") == "1" || q.Get("allowInsecure") == "1",
	}

	if parsed.User != nil {
		cfg.Username = parsed.User.Username()
		if pwd, ok := parsed.User.Password(); ok {
			cfg.Password = pwd
		}
	}

	return cfg, "http", name, nil
}
