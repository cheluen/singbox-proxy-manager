package services

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"sb-proxy/backend/models"
)

func BuildShareLink(node models.ProxyNode) (string, error) {
	parsedConfig, err := node.ParseConfig()
	if err != nil {
		return "", err
	}
	if parsedConfig == nil {
		return "", fmt.Errorf("unsupported proxy type: %s", node.Type)
	}

	switch node.Type {
	case "ss":
		return buildSSShareLink(node.Name, parsedConfig.(*models.SSConfig))
	case "vless":
		return buildVLESSShareLink(node.Name, parsedConfig.(*models.VLESSConfig))
	case "vmess":
		return buildVMESSShareLink(node.Name, parsedConfig.(*models.VMESSConfig))
	case "trojan":
		return buildTrojanShareLink(node.Name, parsedConfig.(*models.TrojanConfig))
	case "hy2":
		return buildHysteria2ShareLink(node.Name, parsedConfig.(*models.Hysteria2Config))
	case "tuic":
		return buildTUICShareLink(node.Name, parsedConfig.(*models.TUICConfig))
	case "anytls":
		return buildAnyTLSShareLink(node.Name, parsedConfig.(*models.AnyTLSConfig))
	case "socks5", "socks5h":
		return buildSOCKS5ShareLink(node.Type, node.Name, parsedConfig.(*models.SOCKS5Config))
	case "http":
		return buildHTTPProxyShareLink(node.Name, parsedConfig.(*models.HTTPProxyConfig))
	case "wireguard":
		return buildWireGuardShareLink(node.Name, parsedConfig.(*models.WireGuardConfig))
	default:
		return "", fmt.Errorf("unsupported proxy type: %s", node.Type)
	}
}

func buildSSShareLink(name string, cfg *models.SSConfig) (string, error) {
	if err := validateShadowsocksPlugin(normalizeShadowsocksPlugin(cfg.Plugin)); err != nil {
		return "", err
	}
	cred := ""
	if strings.HasPrefix(strings.ToLower(cfg.Method), "2022-") {
		cred = url.UserPassword(cfg.Method, cfg.Password).String()
	} else {
		cred = base64.RawURLEncoding.EncodeToString([]byte(cfg.Method + ":" + cfg.Password))
	}
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "ss://" + cred + "@" + server

	params := url.Values{}
	if cfg.Plugin != "" {
		plugin := normalizeShadowsocksPlugin(cfg.Plugin)
		if cfg.PluginOpts != "" {
			plugin += ";" + cfg.PluginOpts
		}
		params.Set("plugin", plugin)
		link += "/"
	}
	if enabled, version := exportUDPOverTCP(cfg.UDPOverTCP, cfg.UDPOverTCPOptions); enabled {
		params.Set("udp_over_tcp", "1")
		if version > 0 {
			params.Set("udp_over_tcp_version", strconv.Itoa(version))
		}
	}
	if len(cfg.Network) > 0 {
		params.Set("network", strings.Join([]string(cfg.Network), ","))
	}
	mergeDialerShareParams(params, cfg.DialerOptions)
	if err := setMapShareParam(params, "multiplex", cfg.MultiplexConfig); err != nil {
		return "", err
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}

	return link + encodeNameFragment(name), nil
}

func buildVLESSShareLink(name string, cfg *models.VLESSConfig) (string, error) {
	if strings.TrimSpace(cfg.SpiderX) != "" {
		return "", fmt.Errorf("vless reality spider_x is not supported by sing-box 1.12.12")
	}
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "vless://" + url.User(cfg.UUID).String() + "@" + server

	params := url.Values{}
	if cfg.Flow != "" {
		params.Set("flow", cfg.Flow)
	}
	if cfg.Encryption != "" {
		params.Set("encryption", cfg.Encryption)
	}
	if cfg.Network != "" {
		params.Set("type", cfg.Network)
	}
	security := cfg.Security
	if security == "" {
		security = nativeTLSShareMode(cfg.TLSOptions)
	}
	if security != "" {
		params.Set("security", security)
	}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if cfg.ALPN != "" {
		params.Set("alpn", cfg.ALPN)
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", normalizeFingerprint(cfg.Fingerprint))
	}
	if cfg.PublicKey != "" {
		params.Set("pbk", cfg.PublicKey)
	}
	if cfg.ShortID != "" {
		params.Set("sid", cfg.ShortID)
	}
	if cfg.PacketEncoding != "" {
		params.Set("packetEncoding", cfg.PacketEncoding)
	}
	if cfg.Insecure {
		params.Set("allowInsecure", "1")
	}
	if len(cfg.OutboundNetwork) > 0 {
		params.Set("network", strings.Join([]string(cfg.OutboundNetwork), ","))
	}

	switch cfg.Network {
	case "ws", "http":
		if cfg.Path != "" {
			params.Set("path", cfg.Path)
		}
		if cfg.Host != "" {
			params.Set("host", cfg.Host)
		}
		if cfg.MaxEarlyData > 0 {
			params.Set("maxEarlyData", strconv.Itoa(cfg.MaxEarlyData))
		}
		if cfg.EarlyDataHeader != "" {
			params.Set("earlyDataHeaderName", cfg.EarlyDataHeader)
		}
	case "grpc":
		if cfg.ServiceName != "" {
			params.Set("serviceName", cfg.ServiceName)
		}
	case "httpupgrade":
		path := cfg.HTTPUpgradePath
		host := cfg.HTTPUpgradeHost
		if path == "" {
			path = cfg.Path
		}
		if host == "" {
			host = cfg.Host
		}
		if path != "" {
			params.Set("path", path)
		}
		if host != "" {
			params.Set("host", host)
		}
	case "quic", "kcp":
		if cfg.Seed != "" {
			params.Set("seed", cfg.Seed)
		}
		if cfg.HeaderType != "" {
			params.Set("headerType", cfg.HeaderType)
		}
	}
	mergeNativeTLSShareParams(params, cfg.TLSOptions)
	mergeNativeTransportShareParams(params, cfg.TransportOptions)
	mergeDialerShareParams(params, cfg.DialerOptions)
	if err := setNativeOptionsShareParam(params, "tls_options", cfg.TLSOptions); err != nil {
		return "", err
	}
	if err := setNativeOptionsShareParam(params, "transport_options", cfg.TransportOptions); err != nil {
		return "", err
	}
	if err := setMapShareParam(params, "multiplex", cfg.MultiplexConfig); err != nil {
		return "", err
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildVMESSShareLink(name string, cfg *models.VMESSConfig) (string, error) {
	if vmessNeedsURLShareLink(cfg) {
		return buildVMESSURLShareLink(name, cfg)
	}

	type vmessJSON struct {
		Add                 string      `json:"add"`
		Port                interface{} `json:"port"`
		ID                  string      `json:"id"`
		AID                 interface{} `json:"aid"`
		Net                 string      `json:"net,omitempty"`
		Type                string      `json:"type,omitempty"`
		Host                string      `json:"host,omitempty"`
		Path                string      `json:"path,omitempty"`
		TLS                 string      `json:"tls,omitempty"`
		SNI                 string      `json:"sni,omitempty"`
		ALPN                string      `json:"alpn,omitempty"`
		FP                  string      `json:"fp,omitempty"`
		PS                  string      `json:"ps,omitempty"`
		V                   string      `json:"v,omitempty"`
		Scy                 string      `json:"scy,omitempty"`
		AllowInsecure       interface{} `json:"allowInsecure,omitempty"`
		MaxEarlyData        interface{} `json:"maxEarlyData,omitempty"`
		EarlyDataHeaderName string      `json:"earlyDataHeaderName,omitempty"`
		Seed                string      `json:"seed,omitempty"`
		GlobalPadding       interface{} `json:"globalPadding,omitempty"`
		AuthenticatedLength interface{} `json:"authenticatedLength,omitempty"`
		PacketEncoding      string      `json:"packetEncoding,omitempty"`
		Method              string      `json:"method,omitempty"`
		PublicKey           string      `json:"pbk,omitempty"`
		ShortID             string      `json:"sid,omitempty"`
		ECH                 string      `json:"ech,omitempty"`
	}

	network, headerType, host, path, maxEarlyData, earlyDataHeaderName, method := effectiveVMessLegacyTransport(cfg)

	tlsMode, sni, alpn, fingerprint, insecure, publicKey, shortID, ech := effectiveVMessLegacyTLS(cfg)
	raw := vmessJSON{
		Add:                 cfg.Server,
		Port:                cfg.ServerPort,
		ID:                  cfg.UUID,
		AID:                 cfg.AlterID,
		Net:                 network,
		Type:                headerType,
		Host:                host,
		Path:                path,
		TLS:                 tlsMode,
		SNI:                 sni,
		ALPN:                alpn,
		FP:                  fingerprint,
		PS:                  name,
		V:                   "2",
		Scy:                 cfg.Security,
		AllowInsecure:       insecure,
		MaxEarlyData:        maxEarlyData,
		EarlyDataHeaderName: earlyDataHeaderName,
		Seed:                cfg.Seed,
		GlobalPadding:       cfg.GlobalPadding,
		AuthenticatedLength: cfg.AuthenticatedLength,
		PacketEncoding:      cfg.PacketEncoding,
		Method:              method,
		PublicKey:           publicKey,
		ShortID:             shortID,
		ECH:                 ech,
	}

	encodedJSON, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}

	return "vmess://" + base64.RawURLEncoding.EncodeToString(encodedJSON), nil
}

func buildVMESSURLShareLink(name string, cfg *models.VMESSConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "vmess://" + url.User(cfg.UUID).String() + "@" + server
	params := url.Values{}
	if cfg.AlterID > 0 {
		params.Set("alterId", strconv.Itoa(cfg.AlterID))
	}
	if cfg.Security != "" {
		params.Set("encryption", cfg.Security)
	}
	if cfg.Network != "" {
		params.Set("type", cfg.Network)
	}
	tlsMode := cfg.TLS
	if tlsMode == "" {
		tlsMode = nativeTLSShareMode(cfg.TLSOptions)
	}
	if tlsMode != "" {
		params.Set("security", tlsMode)
	}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if cfg.ALPN != "" {
		params.Set("alpn", cfg.ALPN)
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", normalizeFingerprint(cfg.Fingerprint))
	}
	if cfg.Insecure {
		params.Set("allowInsecure", "1")
	}
	if cfg.Path != "" {
		params.Set("path", cfg.Path)
	}
	if cfg.Host != "" {
		params.Set("host", cfg.Host)
	}
	if cfg.MaxEarlyData > 0 {
		params.Set("maxEarlyData", strconv.Itoa(cfg.MaxEarlyData))
	}
	if cfg.EarlyDataHeader != "" {
		params.Set("earlyDataHeaderName", cfg.EarlyDataHeader)
	}
	if cfg.ServiceName != "" {
		params.Set("serviceName", cfg.ServiceName)
	}
	if cfg.Method != "" {
		params.Set("method", cfg.Method)
	}
	if cfg.Network == "httpupgrade" {
		setQueryIfEmpty(params, "path", cfg.HTTPUpgradePath)
		setQueryIfEmpty(params, "host", cfg.HTTPUpgradeHost)
	}
	if cfg.PacketEncoding != "" {
		params.Set("packetEncoding", cfg.PacketEncoding)
	}
	if cfg.GlobalPadding {
		params.Set("globalPadding", "1")
	}
	if cfg.AuthenticatedLength {
		params.Set("authenticatedLength", "1")
	}
	if len(cfg.OutboundNetwork) > 0 {
		params.Set("network", strings.Join([]string(cfg.OutboundNetwork), ","))
	}
	mergeNativeTLSShareParams(params, cfg.TLSOptions)
	mergeNativeTransportShareParams(params, cfg.TransportOptions)
	mergeDialerShareParams(params, cfg.DialerOptions)
	if err := setNativeOptionsShareParam(params, "tls_options", cfg.TLSOptions); err != nil {
		return "", err
	}
	if err := setNativeOptionsShareParam(params, "transport_options", cfg.TransportOptions); err != nil {
		return "", err
	}
	if err := setMapShareParam(params, "multiplex", cfg.MultiplexConfig); err != nil {
		return "", err
	}
	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func vmessNeedsURLShareLink(cfg *models.VMESSConfig) bool {
	if cfg == nil {
		return false
	}
	return len(cfg.OutboundNetwork) > 0 ||
		len(cfg.MultiplexConfig) > 0 ||
		hasDialerShareOptions(cfg.DialerOptions) ||
		vmessTLSOptionsNeedURL(cfg.TLSOptions) ||
		vmessTransportOptionsNeedURL(cfg.TransportOptions) ||
		vmessFlatTransportNeedsURL(cfg)
}

func effectiveVMessLegacyTLS(cfg *models.VMESSConfig) (mode, serverName, alpn, fingerprint string, insecure bool, publicKey, shortID, ech string) {
	mode = cfg.TLS
	serverName = cfg.SNI
	alpn = cfg.ALPN
	fingerprint = cfg.Fingerprint
	insecure = cfg.Insecure
	options := cfg.TLSOptions
	if len(options) == 0 {
		return
	}
	if mode == "" && nativeBool(options["enabled"]) {
		mode = "tls"
	}
	if serverName == "" {
		serverName = nativeString(options["server_name"])
	}
	if alpn == "" {
		alpn = strings.Join(nativeStringSlice(options["alpn"]), ",")
	}
	if !insecure {
		insecure = nativeBool(options["insecure"])
	}
	if utls := nativeMap(options["utls"]); fingerprint == "" && nativeBool(utls["enabled"]) {
		fingerprint = nativeString(utls["fingerprint"])
	}
	if reality := nativeMap(options["reality"]); len(reality) > 0 && nativeBool(reality["enabled"]) {
		mode = "reality"
		publicKey = nativeString(reality["public_key"])
		shortID = nativeString(reality["short_id"])
	}
	if echOptions := nativeMap(options["ech"]); nativeBool(echOptions["enabled"]) {
		if configs := nativeStringSlice(echOptions["config"]); len(configs) > 0 {
			ech = nativeECHShareValue(configs)
		}
	}
	fingerprint = normalizeFingerprint(fingerprint)
	return
}

func nativeTLSShareMode(options models.NativeOptions) string {
	if reality := nativeMap(options["reality"]); len(reality) > 0 && nativeBool(reality["enabled"]) {
		return "reality"
	}
	if nativeBool(options["enabled"]) {
		return "tls"
	}
	return ""
}

func effectiveVMessLegacyTransport(cfg *models.VMESSConfig) (network, headerType, host, path string, maxEarlyData int, earlyDataHeaderName, method string) {
	network = cfg.Network
	headerType = cfg.HeaderType
	host = cfg.Host
	path = cfg.Path
	maxEarlyData = cfg.MaxEarlyData
	earlyDataHeaderName = cfg.EarlyDataHeader
	method = cfg.Method

	options := cfg.TransportOptions
	if network == "" {
		network = nativeString(options["type"])
	}
	if path == "" {
		path = nativeString(options["path"])
	}
	if host == "" {
		if hosts := nativeStringSlice(options["host"]); len(hosts) > 0 {
			host = strings.Join(hosts, ",")
		} else if headers := nativeMap(options["headers"]); len(headers) > 0 {
			if headerHost, representable := nativeSingleStringHeaderValue(headers, "host"); representable {
				host = headerHost
			}
		}
	}
	if maxEarlyData <= 0 {
		maxEarlyData = intFromAny(options["max_early_data"])
	}
	if earlyDataHeaderName == "" {
		earlyDataHeaderName = nativeString(options["early_data_header_name"])
	}
	if method == "" {
		method = nativeString(options["method"])
	}
	if network == "grpc" {
		if cfg.ServiceName != "" {
			path = cfg.ServiceName
		} else if serviceName := nativeString(options["service_name"]); serviceName != "" {
			path = serviceName
		}
	}
	if network == "httpupgrade" {
		if cfg.HTTPUpgradePath != "" {
			path = cfg.HTTPUpgradePath
		}
		if cfg.HTTPUpgradeHost != "" {
			host = cfg.HTTPUpgradeHost
		}
	}
	if network == "http" && len(cfg.HTTPPath) == 1 {
		path = cfg.HTTPPath[0]
	}
	return
}

func vmessTLSOptionsNeedURL(options models.NativeOptions) bool {
	for key, value := range options {
		switch key {
		case "enabled", "server_name", "insecure", "alpn":
		case "utls":
			for nestedKey := range nativeMap(value) {
				if nestedKey != "enabled" && nestedKey != "fingerprint" {
					return true
				}
			}
		case "reality":
			for nestedKey := range nativeMap(value) {
				if nestedKey != "enabled" && nestedKey != "public_key" && nestedKey != "short_id" {
					return true
				}
			}
		case "ech":
			echOptions := nativeMap(value)
			for nestedKey := range echOptions {
				if nestedKey != "enabled" && nestedKey != "config" {
					return true
				}
			}
			if nativeBool(echOptions["enabled"]) {
				configs := nativeStringSlice(echOptions["config"])
				if len(configs) == 0 || nativeECHShareValue(configs) == "" {
					return true
				}
			}
		default:
			return true
		}
	}
	return false
}

func vmessTransportOptionsNeedURL(options models.NativeOptions) bool {
	if len(options) == 0 {
		return false
	}
	transportType := strings.ToLower(nativeString(options["type"]))
	allowed := map[string]struct{}{"type": {}}
	switch transportType {
	case "ws":
		allowed["path"] = struct{}{}
		allowed["headers"] = struct{}{}
		allowed["max_early_data"] = struct{}{}
		allowed["early_data_header_name"] = struct{}{}
		if headers := nativeMap(options["headers"]); len(headers) > 0 {
			// Legacy VMess JSON only has one scalar Host field. Preserve every
			// other valid sing-box HTTPHeader shape through the URL form and its
			// native transport_options extension instead of stringifying arrays or
			// choosing one of multiple case-insensitive Host entries.
			if _, representable := nativeSingleStringHeaderValue(headers, "host"); !representable {
				return true
			}
		}
	case "http", "h2":
		allowed["path"] = struct{}{}
		allowed["host"] = struct{}{}
		allowed["method"] = struct{}{}
	case "grpc":
		allowed["service_name"] = struct{}{}
	case "httpupgrade":
		allowed["path"] = struct{}{}
		allowed["host"] = struct{}{}
	case "quic":
	default:
		return true
	}
	for key := range options {
		if _, ok := allowed[key]; !ok {
			return true
		}
	}
	return false
}

func vmessFlatTransportNeedsURL(cfg *models.VMESSConfig) bool {
	for key := range cfg.Headers {
		if !strings.EqualFold(key, "host") {
			return true
		}
	}
	return len(cfg.HTTPPath) > 1
}

func setNativeOptionsShareParam(params url.Values, key string, options models.NativeOptions) error {
	if len(options) == 0 {
		return nil
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	params.Set(key, string(raw))
	return nil
}

func setMapShareParam(params url.Values, key string, options map[string]interface{}) error {
	if len(options) == 0 {
		return nil
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	params.Set(key, string(raw))
	return nil
}

func buildTrojanShareLink(name string, cfg *models.TrojanConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "trojan://" + url.User(cfg.Password).String() + "@" + server

	params := url.Values{}
	if cfg.Security != "" {
		params.Set("security", cfg.Security)
	}
	if cfg.Network != "" {
		params.Set("type", cfg.Network)
	}
	if cfg.Host != "" {
		params.Set("host", cfg.Host)
	}
	if cfg.Path != "" {
		params.Set("path", cfg.Path)
	}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if len(cfg.ALPN) > 0 {
		params.Set("alpn", strings.Join(cfg.ALPN, ","))
	}
	if cfg.Insecure {
		params.Set("insecure", "1")
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", normalizeFingerprint(cfg.Fingerprint))
	}
	if cfg.ServiceName != "" {
		params.Set("serviceName", cfg.ServiceName)
	}
	if cfg.HTTPMethod != "" {
		params.Set("method", cfg.HTTPMethod)
	}
	if len(cfg.OutboundNetwork) > 0 {
		params.Set("network", strings.Join([]string(cfg.OutboundNetwork), ","))
	}
	mergeNativeTLSShareParams(params, cfg.TLSOptions)
	mergeNativeTransportShareParams(params, cfg.TransportOptions)
	mergeDialerShareParams(params, cfg.DialerOptions)
	if err := setNativeOptionsShareParam(params, "tls_options", cfg.TLSOptions); err != nil {
		return "", err
	}
	if err := setNativeOptionsShareParam(params, "transport_options", cfg.TransportOptions); err != nil {
		return "", err
	}
	if err := setMapShareParam(params, "multiplex", cfg.MultiplexConfig); err != nil {
		return "", err
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildHysteria2ShareLink(name string, cfg *models.Hysteria2Config) (string, error) {
	portSpec := strconv.Itoa(cfg.ServerPort)
	if len(cfg.ServerPorts) > 0 {
		ports := make([]string, 0, len(cfg.ServerPorts))
		for _, port := range cfg.ServerPorts {
			ports = append(ports, strings.ReplaceAll(port, ":", "-"))
		}
		portSpec = strings.Join(ports, ",")
	}
	server := net.JoinHostPort(cfg.Server, portSpec)
	link := "hysteria2://" + url.User(cfg.Password).String() + "@" + server + "/"

	params := url.Values{}
	if cfg.UpMbps > 0 {
		params.Set("up", strconv.Itoa(cfg.UpMbps))
	}
	if cfg.DownMbps > 0 {
		params.Set("down", strconv.Itoa(cfg.DownMbps))
	}
	if cfg.BrutalUpMbps > 0 {
		params.Set("brutal_up_mbps", strconv.Itoa(cfg.BrutalUpMbps))
	}
	if cfg.BrutalDownMbps > 0 {
		params.Set("brutal_down_mbps", strconv.Itoa(cfg.BrutalDownMbps))
	}
	if obfsType, obfsPassword := exportHysteria2Obfs(cfg.Obfs, cfg.ObfsPassword, cfg.SalamanderPassword); obfsType != "" {
		params.Set("obfs", obfsType)
		if obfsPassword != "" {
			params.Set("obfs-password", obfsPassword)
		}
	}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if len(cfg.ALPN) > 0 {
		params.Set("alpn", strings.Join(cfg.ALPN, ","))
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", normalizeFingerprint(cfg.Fingerprint))
	}
	if cfg.InsecureSkipVerify {
		params.Set("insecure", "1")
	}
	if len(cfg.Network) > 0 {
		params.Set("network", strings.Join([]string(cfg.Network), ","))
	}
	if cfg.HopInterval != "" {
		params.Set("hopInterval", cfg.HopInterval)
	}
	if cfg.BrutalDebug {
		params.Set("brutal_debug", "1")
	}
	mergeNativeTLSShareParams(params, cfg.TLSOptions)
	mergeDialerShareParams(params, cfg.DialerOptions)
	if err := setNativeOptionsShareParam(params, "tls_options", cfg.TLSOptions); err != nil {
		return "", err
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildTUICShareLink(name string, cfg *models.TUICConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "tuic://" + url.UserPassword(cfg.UUID, cfg.Password).String() + "@" + server

	params := url.Values{}
	if cfg.CongestionControl != "" {
		params.Set("congestion_control", cfg.CongestionControl)
	}
	if cfg.UDPRelayMode != "" {
		params.Set("udp_relay_mode", cfg.UDPRelayMode)
	}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if len(cfg.ALPN) > 0 {
		params.Set("alpn", strings.Join(cfg.ALPN, ","))
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", normalizeFingerprint(cfg.Fingerprint))
	}
	if cfg.InsecureSkipVerify {
		params.Set("insecure", "1")
	}
	if cfg.ZeroRTTHandshake {
		params.Set("zero_rtt_handshake", "1")
	}
	if cfg.UDPOverStream {
		params.Set("udp_over_stream", "1")
	}
	if cfg.DisableSNI {
		params.Set("disable_sni", "1")
	}
	if cfg.Heartbeat != "" {
		params.Set("heartbeat", cfg.Heartbeat)
	}
	if len(cfg.Network) > 0 {
		params.Set("network", strings.Join([]string(cfg.Network), ","))
	}
	mergeNativeTLSShareParams(params, cfg.TLSOptions)
	mergeDialerShareParams(params, cfg.DialerOptions)
	if err := setNativeOptionsShareParam(params, "tls_options", cfg.TLSOptions); err != nil {
		return "", err
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildAnyTLSShareLink(name string, cfg *models.AnyTLSConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "anytls://" + url.User(cfg.Password).String() + "@" + server + "/"

	params := url.Values{}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if len(cfg.ALPN) > 0 {
		params.Set("alpn", strings.Join(cfg.ALPN, ","))
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", normalizeFingerprint(cfg.Fingerprint))
	}
	if cfg.Insecure {
		params.Set("insecure", "1")
	}
	if cfg.IdleSessionCheckInterval != "" {
		params.Set("idle_session_check_interval", cfg.IdleSessionCheckInterval)
	}
	if cfg.IdleSessionTimeout != "" {
		params.Set("idle_session_timeout", cfg.IdleSessionTimeout)
	}
	if cfg.MinIdleSession > 0 {
		params.Set("min_idle_session", strconv.Itoa(cfg.MinIdleSession))
	}
	mergeNativeTLSShareParams(params, cfg.TLSOptions)
	mergeDialerShareParams(params, cfg.DialerOptions)
	if err := setNativeOptionsShareParam(params, "tls_options", cfg.TLSOptions); err != nil {
		return "", err
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildSOCKS5ShareLink(proxyType string, name string, cfg *models.SOCKS5Config) (string, error) {
	scheme := "socks5"
	if proxyType == "socks5h" {
		scheme = "socks5h"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort)),
	}

	if cfg.Username != "" || cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}
	params := url.Values{}
	if len(cfg.Network) > 0 {
		params.Set("network", strings.Join([]string(cfg.Network), ","))
	}
	if enabled, version := exportUDPOverTCP(cfg.UDPOverTCP, cfg.UDPOverTCPOptions); enabled {
		params.Set("udp_over_tcp", "1")
		if version > 0 {
			params.Set("udp_over_tcp_version", strconv.Itoa(version))
		}
	}
	mergeDialerShareParams(params, cfg.DialerOptions)
	u.RawQuery = params.Encode()

	u.Fragment = strings.TrimSpace(name)
	return u.String(), nil
}

func buildHTTPProxyShareLink(name string, cfg *models.HTTPProxyConfig) (string, error) {
	scheme := "http"
	if cfg.TLS || nativeBool(cfg.TLSOptions["enabled"]) {
		scheme = "https"
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort)),
	}

	if cfg.Username != "" || cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}

	q := url.Values{}
	// HTTP(S) URLs are also subscription URLs. Mark links produced by this
	// exporter so batch import never performs a network fetch for a proxy node.
	q.Set("proxy", "1")
	if cfg.SNI != "" {
		q.Set("sni", cfg.SNI)
	}
	if cfg.Insecure {
		q.Set("insecure", "1")
	}
	if len(cfg.Headers) > 0 {
		if rawHeaders, err := json.Marshal(cfg.Headers); err == nil {
			q.Set("headers", string(rawHeaders))
		}
	}
	mergeNativeTLSShareParams(q, cfg.TLSOptions)
	mergeDialerShareParams(q, cfg.DialerOptions)
	if err := setNativeOptionsShareParam(q, "tls_options", cfg.TLSOptions); err != nil {
		return "", err
	}
	u.RawQuery = q.Encode()
	if cfg.Path != "" {
		u.Path = cfg.Path
	}

	u.Fragment = strings.TrimSpace(name)
	return u.String(), nil
}

func buildWireGuardShareLink(name string, cfg *models.WireGuardConfig) (string, error) {
	peer, ok := wireGuardSinglePeerFromConfig(cfg)
	if !ok {
		return "", fmt.Errorf("wireguard export requires a single peer")
	}

	u := &url.URL{
		Scheme: "wireguard",
		Host:   net.JoinHostPort(peer.Server, strconv.Itoa(peer.ServerPort)),
		User:   url.User(cfg.PrivateKey),
	}

	params := url.Values{}
	params.Set("publickey", peer.PublicKey)
	if peer.PreSharedKey != "" {
		params.Set("presharedkey", peer.PreSharedKey)
	}

	var ipv4Addresses []string
	var ipv6Addresses []string
	for _, address := range cfg.LocalAddress {
		trimmed := strings.TrimSpace(address)
		switch {
		case strings.Contains(trimmed, ":"):
			ipv6Addresses = append(ipv6Addresses, trimmed)
		default:
			ipv4Addresses = append(ipv4Addresses, trimmed)
		}
	}

	switch {
	case len(ipv4Addresses) == 1:
		params.Set("ip", ipv4Addresses[0])
	case len(ipv4Addresses) > 1:
		params.Set("address", strings.Join(ipv4Addresses, ","))
	}
	switch {
	case len(ipv6Addresses) == 1:
		params.Set("ipv6", ipv6Addresses[0])
	case len(ipv6Addresses) > 1:
		existing := params.Get("address")
		combined := append([]string{}, ipv6Addresses...)
		if existing != "" {
			combined = append(parseWireGuardList(existing), combined...)
		}
		params.Set("address", strings.Join(combined, ","))
	}
	if params.Get("address") == "" && len(cfg.LocalAddress) > 0 {
		params.Set("address", strings.Join(cfg.LocalAddress, ","))
	}

	if len(peer.AllowedIPs) > 0 {
		params.Set("allowedips", strings.Join(peer.AllowedIPs, ","))
	}
	if reserved := formatWireGuardReserved(peer.Reserved); reserved != "" {
		params.Set("reserved", reserved)
	}
	if cfg.MTU > 0 {
		params.Set("mtu", strconv.Itoa(cfg.MTU))
	}
	if cfg.Workers > 0 {
		params.Set("workers", strconv.Itoa(cfg.Workers))
	}
	if cfg.ListenPort > 0 {
		params.Set("listen_port", strconv.Itoa(cfg.ListenPort))
	}
	if cfg.UDPTimeout != "" {
		params.Set("udp_timeout", cfg.UDPTimeout)
	}
	if peer.PersistentKeepaliveInterval > 0 {
		params.Set("persistent_keepalive_interval", strconv.Itoa(peer.PersistentKeepaliveInterval))
	}
	if cfg.Network != "" {
		params.Set("network", cfg.Network)
	}
	if cfg.SystemInterface {
		params.Set("system_interface", "1")
	}
	if cfg.InterfaceName != "" {
		params.Set("interface_name", cfg.InterfaceName)
	}
	if cfg.Detour != "" {
		params.Set("detour", cfg.Detour)
	}
	resolver, err := wireGuardDomainResolverValue(cfg)
	if err != nil {
		return "", fmt.Errorf("invalid wireguard domain resolver: %w", err)
	}
	if resolverMap, ok := generatorOptionMap(resolver); ok && len(resolverMap) > 0 {
		raw, marshalErr := json.Marshal(resolverMap)
		if marshalErr != nil {
			return "", fmt.Errorf("encode wireguard domain resolver: %w", marshalErr)
		}
		params.Set("domain_resolver_options", string(raw))
	} else if resolverValue := nativeQueryValue(resolver); resolverValue != "" {
		params.Set("domain_resolver", resolverValue)
	}
	if routingMark := nativeQueryValue(cfg.RoutingMark); routingMark != "" {
		params.Set("routing_mark", routingMark)
	}
	if cfg.UDPFragment != nil {
		if *cfg.UDPFragment {
			params.Set("udp_fragment", "1")
		} else {
			params.Set("udp_fragment", "0")
		}
	}
	if cfg.ConnectTimeout != "" {
		params.Set("connect_timeout", cfg.ConnectTimeout)
	}
	if cfg.BindInterface != "" {
		params.Set("bind_interface", cfg.BindInterface)
	}
	if cfg.Inet4BindAddress != "" {
		params.Set("inet4_bind_address", cfg.Inet4BindAddress)
	}
	if cfg.Inet6BindAddress != "" {
		params.Set("inet6_bind_address", cfg.Inet6BindAddress)
	}
	if cfg.ProtectPath != "" {
		params.Set("protect_path", cfg.ProtectPath)
	}
	if cfg.ReuseAddr {
		params.Set("reuse_addr", "1")
	}
	if cfg.NetNS != "" {
		params.Set("netns", cfg.NetNS)
	}
	if cfg.TCPFastOpen {
		params.Set("tcp_fast_open", "1")
	}
	if cfg.TCPMultiPath {
		params.Set("tcp_multi_path", "1")
	}
	if cfg.NetworkStrategy != "" {
		params.Set("network_strategy", cfg.NetworkStrategy)
	}
	if len(cfg.NetworkType) > 0 {
		params.Set("network_type", strings.Join([]string(cfg.NetworkType), ","))
	}
	if len(cfg.FallbackNetworkType) > 0 {
		params.Set("fallback_network_type", strings.Join([]string(cfg.FallbackNetworkType), ","))
	}
	if cfg.FallbackDelay != "" {
		params.Set("fallback_delay", cfg.FallbackDelay)
	}
	if cfg.DomainStrategy != "" {
		params.Set("domain_strategy", cfg.DomainStrategy)
	}

	u.RawQuery = params.Encode()
	u.Fragment = strings.TrimSpace(name)
	return u.String(), nil
}

func encodeNameFragment(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "#" + url.PathEscape(name)
}

func exportUDPOverTCP(raw any, compatibility models.NativeOptions) (bool, int) {
	enabled := false
	version := 0
	read := func(value any) {
		switch typed := value.(type) {
		case bool:
			enabled = typed
		case map[string]any:
			if value, ok := typed["enabled"].(bool); ok {
				enabled = value
			}
			version = intFromAny(typed["version"])
		case models.NativeOptions:
			readMap := map[string]any(typed)
			if value, ok := readMap["enabled"].(bool); ok {
				enabled = value
			}
			version = intFromAny(readMap["version"])
		}
	}
	if raw != nil {
		read(raw)
	} else if len(compatibility) > 0 {
		read(compatibility)
	}
	return enabled, version
}

func exportHysteria2Obfs(raw any, compatibilityPassword string, salamanderPassword string) (string, string) {
	password := firstNonEmpty(compatibilityPassword, salamanderPassword)
	switch typed := raw.(type) {
	case string:
		typeName := strings.ToLower(strings.TrimSpace(typed))
		if typeName == "" || typeName == "none" {
			return "", ""
		}
		// String form is form-managed; do not resurrect the historical
		// salamander_password alias after an explicit password clear.
		return typeName, compatibilityPassword
	case map[string]any:
		typeName := strings.ToLower(nativeString(typed["type"]))
		if typeName == "" || typeName == "none" {
			return "", ""
		}
		return typeName, firstNonEmpty(password, nativeString(typed["password"]))
	case models.NativeOptions:
		typeName := strings.ToLower(strings.TrimSpace(nativeString(typed["type"])))
		if typeName == "" || typeName == "none" {
			return "", ""
		}
		return typeName, firstNonEmpty(password, nativeString(typed["password"]))
	default:
		if password != "" {
			return "salamander", password
		}
		return "", ""
	}
}

func mergeNativeTLSShareParams(params url.Values, options models.NativeOptions) {
	if len(options) == 0 {
		return
	}
	setQueryIfEmpty(params, "sni", nativeString(options["server_name"]))
	if nativeBool(options["insecure"]) {
		setQueryIfEmpty(params, "insecure", "1")
	}
	if nativeBool(options["disable_sni"]) {
		setQueryIfEmpty(params, "disable_sni", "1")
	}
	if alpn := nativeStringSlice(options["alpn"]); len(alpn) > 0 {
		setQueryIfEmpty(params, "alpn", strings.Join(alpn, ","))
	}
	if utls := nativeMap(options["utls"]); nativeBool(utls["enabled"]) {
		setQueryIfEmpty(params, "fp", normalizeFingerprint(nativeString(utls["fingerprint"])))
	}
	if reality := nativeMap(options["reality"]); nativeBool(reality["enabled"]) {
		setQueryIfEmpty(params, "security", "reality")
		setQueryIfEmpty(params, "pbk", nativeString(reality["public_key"]))
		setQueryIfEmpty(params, "sid", nativeString(reality["short_id"]))
	}
	if ech := nativeMap(options["ech"]); nativeBool(ech["enabled"]) {
		if configs := nativeStringSlice(ech["config"]); len(configs) > 0 {
			setQueryIfEmpty(params, "ech", nativeECHShareValue(configs))
		}
	}
}

func mergeNativeTransportShareParams(params url.Values, options models.NativeOptions) {
	if len(options) == 0 {
		return
	}
	setQueryIfEmpty(params, "type", nativeString(options["type"]))
	setQueryIfEmpty(params, "path", nativeString(options["path"]))
	if hosts := nativeStringSlice(options["host"]); len(hosts) > 0 {
		setQueryIfEmpty(params, "host", strings.Join(hosts, ","))
	}
	if headers := nativeMap(options["headers"]); len(headers) > 0 {
		if headerHost, representable := nativeSingleStringHeaderValue(headers, "host"); representable {
			setQueryIfEmpty(params, "host", headerHost)
		}
	}
	setQueryIfEmpty(params, "method", nativeString(options["method"]))
	setQueryIfEmpty(params, "serviceName", nativeString(options["service_name"]))
	setQueryIfEmpty(params, "earlyDataHeaderName", nativeString(options["early_data_header_name"]))
	if earlyData := intFromAny(options["max_early_data"]); earlyData > 0 {
		setQueryIfEmpty(params, "maxEarlyData", strconv.Itoa(earlyData))
	}
	setQueryIfEmpty(params, "idleTimeout", nativeString(options["idle_timeout"]))
	setQueryIfEmpty(params, "pingTimeout", nativeString(options["ping_timeout"]))
	if nativeBool(options["permit_without_stream"]) {
		setQueryIfEmpty(params, "permitWithoutStream", "1")
	}
}

func mergeDialerShareParams(params url.Values, options models.DialerOptions) {
	setQueryIfEmpty(params, "detour", options.Detour)
	setQueryIfEmpty(params, "bind_interface", options.BindInterface)
	setQueryIfEmpty(params, "inet4_bind_address", options.Inet4BindAddress)
	setQueryIfEmpty(params, "inet6_bind_address", options.Inet6BindAddress)
	setQueryIfEmpty(params, "protect_path", options.ProtectPath)
	setQueryIfEmpty(params, "routing_mark", nativeQueryValue(options.RoutingMark))
	if options.ReuseAddr {
		setQueryIfEmpty(params, "reuse_addr", "1")
	}
	setQueryIfEmpty(params, "netns", options.NetNS)
	setQueryIfEmpty(params, "connect_timeout", options.ConnectTimeout)
	if options.TCPFastOpen {
		setQueryIfEmpty(params, "tcp_fast_open", "1")
	}
	if options.TCPMultiPath {
		setQueryIfEmpty(params, "tcp_multi_path", "1")
	}
	if options.UDPFragment != nil {
		if *options.UDPFragment {
			setQueryIfEmpty(params, "udp_fragment", "1")
		} else {
			setQueryIfEmpty(params, "udp_fragment", "0")
		}
	}
	if resolver := nativeMap(options.DomainResolver); len(resolver) > 0 {
		if raw, err := json.Marshal(resolver); err == nil {
			setQueryIfEmpty(params, "domain_resolver_options", string(raw))
		}
	} else {
		setQueryIfEmpty(params, "domain_resolver", nativeQueryValue(options.DomainResolver))
	}
	setQueryIfEmpty(params, "network_strategy", options.NetworkStrategy)
	if len(options.NetworkType) > 0 {
		setQueryIfEmpty(params, "network_type", strings.Join([]string(options.NetworkType), ","))
	}
	if len(options.FallbackNetworkType) > 0 {
		setQueryIfEmpty(params, "fallback_network_type", strings.Join([]string(options.FallbackNetworkType), ","))
	}
	setQueryIfEmpty(params, "fallback_delay", options.FallbackDelay)
	setQueryIfEmpty(params, "domain_strategy", options.DomainStrategy)
}

func hasDialerShareOptions(options models.DialerOptions) bool {
	return options.Detour != "" ||
		options.BindInterface != "" ||
		options.Inet4BindAddress != "" ||
		options.Inet6BindAddress != "" ||
		options.ProtectPath != "" ||
		options.RoutingMark != nil ||
		options.ReuseAddr ||
		options.NetNS != "" ||
		options.ConnectTimeout != "" ||
		options.TCPFastOpen ||
		options.TCPMultiPath ||
		options.UDPFragment != nil ||
		options.DomainResolver != nil ||
		options.NetworkStrategy != "" ||
		len(options.NetworkType) > 0 ||
		len(options.FallbackNetworkType) > 0 ||
		options.FallbackDelay != "" ||
		options.DomainStrategy != ""
}

func setQueryIfEmpty(values url.Values, key string, value string) {
	if strings.TrimSpace(value) != "" && values.Get(key) == "" {
		values.Set(key, value)
	}
}

func nativeMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case models.NativeOptions:
		return map[string]any(typed)
	default:
		return nil
	}
}

// nativeSingleStringHeaderValue returns a legacy-share-link-compatible header
// value only when exactly one case-insensitive key exists and its value is a
// scalar string. sing-box HTTPHeader also accepts list values; those must stay
// in the native JSON extension because flattening them would change semantics.
func nativeSingleStringHeaderValue(headers map[string]any, targetName string) (string, bool) {
	var value string
	found := false
	for name, rawValue := range headers {
		if !strings.EqualFold(name, targetName) {
			return "", false
		}
		if found {
			return "", false
		}
		stringValue, ok := rawValue.(string)
		if !ok {
			return "", false
		}
		value = strings.TrimSpace(stringValue)
		found = true
	}
	return value, found
}

func nativeString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func nativeStringSlice(value any) []string {
	switch typed := value.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []string{strings.TrimSpace(typed)}
	case []string:
		return typed
	case models.ListableString:
		return []string(typed)
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := nativeString(item); text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func nativeBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(typed)
		return parsed
	default:
		return intFromAny(value) != 0
	}
}

func nativeQueryValue(value any) string {
	if value == nil {
		return ""
	}
	switch value.(type) {
	case map[string]any, models.NativeOptions, []any, []string:
		encoded, err := json.Marshal(value)
		if err == nil {
			return string(encoded)
		}
	}
	return nativeString(value)
}
