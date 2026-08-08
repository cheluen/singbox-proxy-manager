package services

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sb-proxy/backend/models"
)

// ParseShareLink parses various proxy share link formats
func ParseShareLink(link string) (interface{}, string, string, error) {
	link = strings.TrimSpace(link)
	lowerLink := strings.ToLower(link)

	if strings.HasPrefix(lowerLink, "ss://") {
		return parseSSLink(link)
	} else if strings.HasPrefix(lowerLink, "vless://") {
		return parseVLESSLink(link)
	} else if strings.HasPrefix(lowerLink, "vmess://") {
		return parseVMESSLink(link)
	} else if strings.HasPrefix(lowerLink, "trojan://") {
		return parseTrojanLink(link)
	} else if strings.HasPrefix(lowerLink, "hysteria2://") || strings.HasPrefix(lowerLink, "hy2://") {
		return parseHysteria2Link(link)
	} else if strings.HasPrefix(lowerLink, "tuic://") {
		return parseTUICLink(link)
	} else if strings.HasPrefix(lowerLink, "anytls://") {
		return parseAnyTLSLink(link)
	} else if strings.HasPrefix(lowerLink, "socks5://") || strings.HasPrefix(lowerLink, "socks5h://") || strings.HasPrefix(lowerLink, "socks://") {
		return parseSOCKS5Link(link)
	} else if strings.HasPrefix(lowerLink, "wireguard://") || strings.HasPrefix(lowerLink, "wg://") {
		return parseWireGuardLink(link)
	} else if strings.HasPrefix(lowerLink, "http://") || strings.HasPrefix(lowerLink, "https://") {
		return parseHTTPProxyLink(link)
	}

	return nil, "", "", fmt.Errorf("unsupported link format")
}

// parseSSLink parses SIP002 and the legacy full-base64 Shadowsocks URI form.
func parseSSLink(link string) (interface{}, string, string, error) {
	raw := strings.TrimSpace(link[len("ss://"):])
	raw, rawFragment := splitOnce(raw, "#")
	raw, rawQuery := splitOnce(raw, "?")

	name := decodeShareLinkName(rawFragment, "SS Node")
	params, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid ss query: %w", err)
	}

	var credentials, serverPart string
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		credentials = raw[:at]
		serverPart = strings.TrimSuffix(raw[at+1:], "/")
	} else {
		decoded, decodeErr := decodeBase64String(raw)
		if decodeErr != nil {
			return nil, "", "", fmt.Errorf("invalid ss link format")
		}
		decodedLink := string(decoded)
		at := strings.LastIndex(decodedLink, "@")
		if at < 0 {
			return nil, "", "", fmt.Errorf("invalid ss link format")
		}
		credentials = decodedLink[:at]
		serverPart = decodedLink[at+1:]
	}

	method, password, err := parseSSCredentials(credentials)
	if err != nil {
		return nil, "", "", err
	}
	server, port, err := parseRequiredHostPort(serverPart)
	if err != nil {
		return nil, "", "", err
	}

	plugin, pluginOpts := splitSIP003Plugin(params.Get("plugin"))
	if pluginOpts == "" {
		pluginOpts = params.Get("plugin-opts")
	}
	plugin = normalizeShadowsocksPlugin(plugin)
	if err := validateShadowsocksPlugin(plugin); err != nil {
		return nil, "", "", err
	}
	if pluginOpts != "" {
		normalizedPluginOpts, normalizeErr := stringifyPluginOpts(plugin, pluginOpts)
		if normalizeErr != nil {
			return nil, "", "", fmt.Errorf("invalid shadowsocks plugin options: %w", normalizeErr)
		}
		pluginOpts = normalizedPluginOpts
	}

	config := models.SSConfig{
		Server:     server,
		ServerPort: port,
		Method:     method,
		Password:   password,
		Plugin:     plugin,
		PluginOpts: pluginOpts,
	}

	network, err := normalizeNetworkList(firstQueryValue(params, "network"))
	if err != nil {
		return nil, "", "", err
	}
	if len(network) > 0 {
		config.Network = models.ListableString(network)
	}
	if enabled, present := queryBool(params, "udp_over_tcp", "udp-over-tcp", "uot"); present {
		config.UDPOverTCP = enabled
		if version := queryPositiveInt(params, "udp_over_tcp_version", "udp-over-tcp-version", "uot_version"); version > 0 {
			if version > 2 {
				return nil, "", "", fmt.Errorf("unsupported udp-over-tcp version: %d", version)
			}
			config.UDPOverTCPOptions = models.NativeOptions{
				"enabled": enabled,
				"version": version,
			}
			config.UDPOverTCP = models.NativeOptions(config.UDPOverTCPOptions)
		}
	}
	if multiplex, present, multiplexErr := parseMultiplexQuery(params); multiplexErr != nil {
		return nil, "", "", multiplexErr
	} else if present {
		config.MultiplexConfig = multiplex
	}
	if err := applyURLDialerOptions(&config.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	return config, "ss", name, nil
}

func decodeBase64String(raw string) ([]byte, error) {
	encodings := []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(raw)
		if err != nil {
			continue
		}
		return decoded, nil
	}
	return nil, fmt.Errorf("invalid base64")
}

type parsedProxyURL struct {
	URL  *url.URL
	Host string
	Port int
	Name string
}

func splitOnce(raw string, separator string) (string, string) {
	parts := strings.SplitN(raw, separator, 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func decodeShareLinkName(raw string, fallback string) string {
	if raw == "" {
		return fallback
	}
	name, err := url.PathUnescape(raw)
	if err != nil || strings.TrimSpace(name) == "" {
		return fallback
	}
	return name
}

func parseSSCredentials(raw string) (string, string, error) {
	var decoded string
	if candidate, err := decodeBase64String(raw); err == nil && strings.Contains(string(candidate), ":") {
		decoded = string(candidate)
	} else {
		unescaped, unescapeErr := url.PathUnescape(raw)
		if unescapeErr != nil {
			return "", "", fmt.Errorf("invalid shadowsocks credentials escaping: %w", unescapeErr)
		}
		decoded = unescaped
	}
	parts := strings.SplitN(decoded, ":", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return "", "", fmt.Errorf("invalid shadowsocks credentials")
	}
	return parts[0], parts[1], nil
}

func parseRequiredHostPort(raw string) (string, int, error) {
	host, portRaw, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return "", 0, fmt.Errorf("invalid server format: %w", err)
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port")
	}
	if strings.TrimSpace(host) == "" {
		return "", 0, fmt.Errorf("missing server")
	}
	return host, port, nil
}

func splitSIP003Plugin(raw string) (string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	escaped := false
	for index, char := range raw {
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == ';' {
			return strings.TrimSpace(raw[:index]), strings.TrimSpace(raw[index+1:])
		}
	}
	return raw, ""
}

func normalizeShadowsocksPlugin(plugin string) string {
	switch strings.ToLower(strings.TrimSpace(plugin)) {
	case "obfs", "simple-obfs":
		return "obfs-local"
	case "obfs-local", "v2ray-plugin":
		return strings.ToLower(strings.TrimSpace(plugin))
	default:
		return strings.TrimSpace(plugin)
	}
}

func validateShadowsocksPlugin(plugin string) error {
	switch strings.ToLower(strings.TrimSpace(plugin)) {
	case "", "obfs-local", "v2ray-plugin":
		return nil
	default:
		return fmt.Errorf("shadowsocks plugin %q is not supported by sing-box 1.12.12", plugin)
	}
}

func parseStandardProxyURL(link string, allowedSchemes []string, defaultPort int, defaultName string) (*parsedProxyURL, error) {
	parsed, err := url.Parse(strings.TrimSpace(link))
	if err != nil {
		return nil, err
	}
	schemeAllowed := false
	for _, scheme := range allowedSchemes {
		if strings.EqualFold(parsed.Scheme, scheme) {
			schemeAllowed = true
			break
		}
	}
	if !schemeAllowed {
		return nil, fmt.Errorf("unexpected scheme: %s", parsed.Scheme)
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return nil, fmt.Errorf("missing server")
	}
	port := defaultPort
	if portRaw := parsed.Port(); portRaw != "" {
		port, err = strconv.Atoi(portRaw)
		if err != nil {
			return nil, fmt.Errorf("invalid port")
		}
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("missing or invalid port")
	}
	name := defaultName
	if parsed.Fragment != "" {
		name = decodeShareLinkName(parsed.EscapedFragment(), defaultName)
	}
	return &parsedProxyURL{URL: parsed, Host: host, Port: port, Name: name}, nil
}

func decodedURLUserInfo(parsed *url.URL) string {
	if parsed == nil || parsed.User == nil {
		return ""
	}
	username := parsed.User.Username()
	if password, ok := parsed.User.Password(); ok {
		return username + ":" + password
	}
	return username
}

func queryBool(values url.Values, keys ...string) (bool, bool) {
	for _, key := range keys {
		rawValues, ok := values[key]
		if !ok || len(rawValues) == 0 {
			continue
		}
		raw := strings.ToLower(strings.TrimSpace(rawValues[len(rawValues)-1]))
		switch raw {
		case "1", "true", "yes", "on", "enable", "enabled":
			return true, true
		case "0", "false", "no", "off", "disable", "disabled":
			return false, true
		default:
			return false, false
		}
	}
	return false, false
}

func queryBoolValue(values url.Values, keys ...string) bool {
	value, _ := queryBool(values, keys...)
	return value
}

func queryParameter(values url.Values, keys ...string) (string, bool) {
	present := false
	for _, key := range keys {
		rawValues, ok := values[key]
		if !ok {
			continue
		}
		present = true
		if len(rawValues) == 0 {
			continue
		}
		value := strings.TrimSpace(rawValues[len(rawValues)-1])
		if value != "" {
			return value, true
		}
	}
	return "", present
}

func queryECHParameter(values url.Values) (string, bool) {
	rawValues, present := values["ech"]
	if !present || len(rawValues) == 0 {
		return "", present
	}
	return rawValues[len(rawValues)-1], true
}

func queryTLSBool(values url.Values, keys ...string) (bool, bool, error) {
	raw, present := queryParameter(values, keys...)
	if !present {
		return false, false, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on", "enable", "enabled":
		return true, true, nil
	case "0", "false", "no", "off", "disable", "disabled":
		return false, true, nil
	default:
		return false, true, fmt.Errorf("invalid boolean share parameter %q", keys[0])
	}
}

func canonicalShareParameterKey(key string) string {
	replacer := strings.NewReplacer("-", "", "_", "")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(key)))
}

func rejectUnsupportedShareParameter(values url.Values, feature string, keys ...string) error {
	aliases := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		aliases[canonicalShareParameterKey(key)] = struct{}{}
	}
	for key, candidates := range values {
		if _, ok := aliases[canonicalShareParameterKey(key)]; !ok {
			continue
		}
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate) != "" {
				return fmt.Errorf("share parameter %q (%s) is not supported by sing-box 1.12.12", key, feature)
			}
		}
	}
	return nil
}

func rejectUnsupportedTLSShareParameters(values url.Values) error {
	if err := rejectUnsupportedShareParameter(values, "pinnedPeerCertSha256", "pcs", "pinnedPeerCertSha256"); err != nil {
		return err
	}
	return rejectUnsupportedShareParameter(values, "verifyPeerCertByName", "vcn", "verifyPeerCertByName")
}

func rejectUnsupportedRealityShareParameters(values url.Values) error {
	if err := rejectUnsupportedShareParameter(values, "mldsa65Verify", "pqv", "mldsa65Verify"); err != nil {
		return err
	}
	return rejectUnsupportedShareParameter(values, "SpiderX", "spx", "spiderX")
}

func rejectUnsupportedJSONProperty(fields map[string]json.RawMessage, feature string, keys ...string) error {
	aliases := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		aliases[canonicalShareParameterKey(key)] = struct{}{}
	}
	for key, rawValue := range fields {
		if _, ok := aliases[canonicalShareParameterKey(key)]; !ok {
			continue
		}
		value := strings.TrimSpace(string(rawValue))
		if value == "" || value == "null" || value == `""` || value == "false" {
			continue
		}
		return fmt.Errorf("vmess JSON property %q (%s) is not supported by sing-box 1.12.12", key, feature)
	}
	return nil
}

func queryPositiveInt(values url.Values, keys ...string) int {
	raw := firstQueryValue(values, keys...)
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func queryNonNegativeInt(values url.Values, keys ...string) int {
	raw := firstQueryValue(values, keys...)
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func intFromAny(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func parseCommaSeparatedList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(char rune) bool {
		return char == ',' || char == '\n' || char == '\r'
	})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func normalizeFingerprint(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "none" {
		return ""
	}
	return value
}

func normalizePacketEncoding(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "none" {
		value = ""
	}
	switch value {
	case "", "packetaddr", "xudp":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported packet encoding for sing-box 1.12.12: %s", raw)
	}
}

func normalizeVMessCipher(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return "auto", nil
	}
	switch value {
	case "auto", "aes-128-gcm", "aes-128-cfb", "chacha20-poly1305", "none", "zero":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported vmess cipher for sing-box 1.12.12: %s", raw)
	}
}

func normalizeVMessTransportSecurity(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "", "none":
		return "", nil
	case "xtls":
		return "tls", nil
	case "tls", "reality":
		return value, nil
	default:
		return "", fmt.Errorf("unsupported vmess transport security: %s", raw)
	}
}

func normalizeNetworkList(raw string) ([]string, error) {
	values := parseCommaSeparatedList(strings.ReplaceAll(raw, ";", ","))
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if value == "both" || value == "all" {
			if len(values) == 1 {
				return nil, nil
			}
			return nil, fmt.Errorf("network %q cannot be combined with other values", value)
		}
		if value != "tcp" && value != "udp" {
			return nil, fmt.Errorf("unsupported network value: %s", value)
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func normalizeDurationString(raw string, numericUnit time.Duration) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if number, err := strconv.ParseFloat(raw, 64); err == nil {
		if number <= 0 {
			return ""
		}
		duration := time.Duration(number * float64(numericUnit))
		return duration.String()
	}
	if duration, err := time.ParseDuration(raw); err == nil && duration > 0 {
		return duration.String()
	}
	return raw
}

func applyURLDialerOptions(options *models.DialerOptions, values url.Values) error {
	if options == nil {
		return nil
	}
	options.Detour = firstQueryValue(values, "detour", "dialer-proxy", "dialer_proxy")
	options.BindInterface = firstQueryValue(values, "bind_interface", "bind-interface", "interface-name", "interface_name")
	options.Inet4BindAddress = firstQueryValue(values, "inet4_bind_address", "inet4-bind-address")
	options.Inet6BindAddress = firstQueryValue(values, "inet6_bind_address", "inet6-bind-address")
	options.ProtectPath = firstQueryValue(values, "protect_path", "protect-path")
	options.RoutingMark = parseWireGuardRoutingMark(firstQueryValue(values, "routing_mark", "routing-mark"))
	options.ReuseAddr = queryBoolValue(values, "reuse_addr", "reuse-addr")
	options.NetNS = firstQueryValue(values, "netns", "net-ns")
	options.ConnectTimeout = normalizeDurationString(firstQueryValue(values, "connect_timeout", "connect-timeout"), time.Second)
	options.TCPFastOpen = queryBoolValue(values, "tcp_fast_open", "tcp-fast-open", "tfo")
	options.TCPMultiPath = queryBoolValue(values, "tcp_multi_path", "tcp-multi-path", "mptcp")
	if udpFragment, present := queryBool(values, "udp_fragment", "udp-fragment"); present {
		options.UDPFragment = &udpFragment
	}
	if resolver := firstQueryValue(values, "domain_resolver", "domain-resolver"); resolver != "" {
		if strings.HasPrefix(strings.TrimSpace(resolver), "{") {
			var parsedResolver any
			if err := json.Unmarshal([]byte(resolver), &parsedResolver); err != nil {
				return fmt.Errorf("invalid domain resolver: %w", err)
			}
			options.DomainResolver = parsedResolver
		} else {
			options.DomainResolver = resolver
		}
	}
	if rawResolver := firstQueryValue(values, "domain_resolver_options", "domain-resolver-options"); rawResolver != "" {
		var resolver any
		if err := json.Unmarshal([]byte(rawResolver), &resolver); err != nil {
			return fmt.Errorf("invalid domain resolver options: %w", err)
		}
		options.DomainResolver = resolver
	}
	options.NetworkStrategy = firstQueryValue(values, "network_strategy", "network-strategy")
	options.NetworkType = models.ListableString(normalizeLooseStringList(firstQueryValue(values, "network_type", "network-type")))
	options.FallbackNetworkType = models.ListableString(normalizeLooseStringList(firstQueryValue(values, "fallback_network_type", "fallback-network-type")))
	options.FallbackDelay = normalizeDurationString(firstQueryValue(values, "fallback_delay", "fallback-delay"), time.Second)
	options.DomainStrategy = firstQueryValue(values, "domain_strategy", "domain-strategy", "ip-version", "ip_version")
	return nil
}

func normalizeLooseStringList(raw string) []string {
	values := parseCommaSeparatedList(strings.ReplaceAll(raw, ";", ","))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func buildV2RayTransportOptions(params url.Values) (string, map[string]any, error) {
	if err := rejectUnsupportedShareParameter(params, "FinalMask", "fm", "finalmask", "finalMask", "FinalMask", "final-mask", "final_mask"); err != nil {
		return "", nil, err
	}
	transportType := strings.ToLower(strings.TrimSpace(firstQueryValue(params, "type", "transport")))
	switch transportType {
	case "h2":
		transportType = "http"
	case "gun":
		transportType = "grpc"
	}
	switch transportType {
	case "", "none", "tcp", "raw":
		if headerType := strings.ToLower(strings.TrimSpace(firstQueryValue(params, "headerType", "header_type", "header-type"))); headerType != "" && headerType != "none" {
			return "", nil, fmt.Errorf("tcp header obfuscation is not supported by sing-box 1.12.12")
		}
		if params.Get("seed") != "" {
			return "", nil, fmt.Errorf("kcp seed is not supported by sing-box 1.12.12")
		}
		if transportType == "none" || transportType == "raw" {
			transportType = "tcp"
		}
		return transportType, nil, nil
	case "kcp", "mkcp", "xhttp", "splithttp", "mekya":
		return "", nil, fmt.Errorf("transport %s is not supported by sing-box 1.12.12", transportType)
	case "quic":
		if params.Get("seed") != "" {
			return "", nil, fmt.Errorf("quic seed is not supported by sing-box 1.12.12")
		}
		if security := strings.ToLower(strings.TrimSpace(firstQueryValue(params, "quicSecurity", "quic_security", "quic-security"))); security != "" && security != "none" {
			return "", nil, fmt.Errorf("quic security %q is not supported by sing-box 1.12.12", security)
		}
		if firstQueryValue(params, "key", "quicKey", "quic_key", "quic-key") != "" || params.Get("host") != "" || params.Get("path") != "" {
			return "", nil, fmt.Errorf("quic transport options are not supported by sing-box 1.12.12")
		}
		if headerType := strings.ToLower(strings.TrimSpace(firstQueryValue(params, "headerType", "header_type", "header-type"))); headerType != "" && headerType != "none" {
			return "", nil, fmt.Errorf("quic header obfuscation is not supported by sing-box 1.12.12")
		}
		return transportType, map[string]any{"type": transportType}, nil
	case "ws":
		options := map[string]any{"type": transportType}
		setMapIfNotEmpty(options, "path", params.Get("path"))
		headers := map[string]any{}
		if host := params.Get("host"); host != "" {
			headers["Host"] = host
		}
		if len(headers) > 0 {
			options["headers"] = headers
		}
		if maxEarlyData := queryPositiveInt(params, "maxEarlyData", "max_early_data", "ed"); maxEarlyData > 0 {
			options["max_early_data"] = maxEarlyData
		}
		setMapIfNotEmpty(options, "early_data_header_name", firstQueryValue(params, "earlyDataHeaderName", "early_data_header_name", "eh"))
		return transportType, options, nil
	case "http":
		options := map[string]any{"type": transportType}
		setMapIfNotEmpty(options, "path", params.Get("path"))
		if hosts := parseCommaSeparatedList(params.Get("host")); len(hosts) > 0 {
			options["host"] = hosts
		}
		setMapIfNotEmpty(options, "method", params.Get("method"))
		setMapIfNotEmpty(options, "idle_timeout", normalizeDurationString(firstQueryValue(params, "idleTimeout", "idle_timeout"), time.Second))
		setMapIfNotEmpty(options, "ping_timeout", normalizeDurationString(firstQueryValue(params, "pingTimeout", "ping_timeout"), time.Second))
		return transportType, options, nil
	case "grpc":
		if authority := firstQueryValue(params, "authority", "grpcAuthority", "grpc_authority", "grpc-authority"); authority != "" {
			return "", nil, fmt.Errorf("grpc authority is not supported by sing-box 1.12.12")
		}
		if userAgent := firstQueryValue(params, "grpc-user-agent", "grpc_user_agent", "grpcUserAgent", "user-agent", "user_agent", "userAgent"); userAgent != "" {
			return "", nil, fmt.Errorf("grpc user agent is not supported by sing-box 1.12.12")
		}
		mode := strings.ToLower(strings.TrimSpace(firstQueryValue(params, "mode", "grpcMode", "grpc_mode", "grpc-mode", "headerType", "header_type")))
		if mode == "multi" || queryBoolValue(params, "multiMode", "multi_mode", "multi-mode") {
			return "", nil, fmt.Errorf("grpc multi mode is not supported by sing-box 1.12.12")
		}
		options := map[string]any{"type": transportType}
		setMapIfNotEmpty(options, "service_name", firstQueryValue(params, "serviceName", "service_name", "grpc-service-name"))
		setMapIfNotEmpty(options, "idle_timeout", normalizeDurationString(firstQueryValue(params, "idleTimeout", "idle_timeout"), time.Second))
		setMapIfNotEmpty(options, "ping_timeout", normalizeDurationString(firstQueryValue(params, "pingTimeout", "ping_timeout"), time.Second))
		if value, present := queryBool(params, "permitWithoutStream", "permit_without_stream"); present {
			options["permit_without_stream"] = value
		}
		return transportType, options, nil
	case "httpupgrade":
		options := map[string]any{"type": transportType}
		setMapIfNotEmpty(options, "host", params.Get("host"))
		setMapIfNotEmpty(options, "path", params.Get("path"))
		return transportType, options, nil
	default:
		return "", nil, fmt.Errorf("unknown transport type: %s", transportType)
	}
}

func buildLegacyVMessTransportOptions(network string, headerType string, host string, path string, seed string, maxEarlyData int, earlyDataHeader string, method string) (string, map[string]any, error) {
	params := make(url.Values)
	params.Set("type", network)
	if normalizedNetwork := strings.ToLower(strings.TrimSpace(network)); normalizedNetwork == "grpc" || normalizedNetwork == "gun" {
		params.Set("mode", headerType)
		params.Set("serviceName", path)
	} else {
		params.Set("headerType", headerType)
	}
	params.Set("host", host)
	params.Set("path", path)
	params.Set("seed", seed)
	if maxEarlyData > 0 {
		params.Set("maxEarlyData", strconv.Itoa(maxEarlyData))
	}
	params.Set("earlyDataHeaderName", earlyDataHeader)
	params.Set("method", method)
	return buildV2RayTransportOptions(params)
}

func setMapIfNotEmpty(target map[string]any, key string, value string) {
	if strings.TrimSpace(value) != "" {
		target[key] = value
	}
}

func decodeShareECHConfigList(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("empty ECH config list")
	}

	if block, rest := pem.Decode([]byte(trimmed)); block != nil {
		if block.Type != "ECH CONFIGS" || len(bytes.TrimSpace(rest)) != 0 || !validECHConfigList(block.Bytes) {
			return nil, fmt.Errorf("invalid ECH CONFIGS PEM")
		}
		return append([]byte(nil), block.Bytes...), nil
	}

	candidates := []string{compactECHConfigBase64(raw, strings.Contains(raw, " "))}
	if strings.Contains(raw, " ") {
		candidates = append(candidates, compactECHConfigBase64(raw, false))
	}
	for index, candidate := range candidates {
		if index > 0 && candidate == candidates[0] {
			continue
		}
		decoded, err := decodeBase64String(candidate)
		if err == nil && validECHConfigList(decoded) {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("ECH config list must be valid base64 or ECH CONFIGS PEM")
}

func decodeNativeECHConfigList(config []string) ([]byte, error) {
	raw := strings.Join(config, "\n")
	block, rest := pem.Decode([]byte(raw))
	if block == nil || block.Type != "ECH CONFIGS" || len(bytes.TrimSpace(rest)) != 0 || !validECHConfigList(block.Bytes) {
		return nil, fmt.Errorf("invalid ECH CONFIGS PEM")
	}
	return append([]byte(nil), block.Bytes...), nil
}

func compactECHConfigBase64(raw string, spacesAsPlus bool) string {
	var compact strings.Builder
	compact.Grow(len(raw))
	for index := 0; index < len(raw); index++ {
		switch char := raw[index]; char {
		case ' ':
			if spacesAsPlus {
				compact.WriteByte('+')
			}
		case '\t', '\n', '\r', '\v', '\f':
		default:
			compact.WriteByte(char)
		}
	}
	return compact.String()
}

func validECHConfigList(configList []byte) bool {
	if len(configList) < 6 || int(binary.BigEndian.Uint16(configList[:2])) != len(configList)-2 {
		return false
	}
	remaining := configList[2:]
	configCount := 0
	for len(remaining) > 0 {
		if len(remaining) < 4 {
			return false
		}
		configLength := int(binary.BigEndian.Uint16(remaining[2:4]))
		if configLength > len(remaining)-4 {
			return false
		}
		remaining = remaining[configLength+4:]
		configCount++
	}
	return configCount > 0
}

func encodeNativeECHConfigList(configList []byte) []string {
	encoded := pem.EncodeToMemory(&pem.Block{Type: "ECH CONFIGS", Bytes: configList})
	return strings.Split(strings.TrimSuffix(string(encoded), "\n"), "\n")
}

func normalizeShareECHConfigList(raw string) ([]byte, []string, error) {
	configList, err := decodeShareECHConfigList(raw)
	if err != nil {
		return nil, nil, err
	}
	return configList, encodeNativeECHConfigList(configList), nil
}

func nativeECHShareValue(config []string) string {
	configList, err := decodeNativeECHConfigList(config)
	if err != nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(configList)
}

func buildTLSOptions(enabled bool, disableSNI bool, serverName string, insecure bool, alpn []string, fingerprint string, realityPublicKey string, realityShortID string, echConfig string) (map[string]any, error) {
	if !enabled && !disableSNI && serverName == "" && !insecure && len(alpn) == 0 && fingerprint == "" && echConfig == "" && realityPublicKey == "" {
		return nil, nil
	}
	if strings.TrimSpace(realityPublicKey) != "" && strings.TrimSpace(echConfig) != "" {
		return nil, fmt.Errorf("reality is incompatible with ECH in sing-box 1.12.12")
	}
	options := map[string]any{"enabled": enabled}
	if disableSNI {
		options["disable_sni"] = true
	}
	setMapIfNotEmpty(options, "server_name", serverName)
	if insecure {
		options["insecure"] = true
	}
	if len(alpn) > 0 {
		options["alpn"] = alpn
	}
	fingerprint = normalizeFingerprint(fingerprint)
	if fingerprint != "" {
		options["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
	}
	if realityPublicKey != "" {
		options["reality"] = map[string]any{
			"enabled":    true,
			"public_key": realityPublicKey,
			"short_id":   realityShortID,
		}
		if _, ok := options["utls"]; !ok {
			options["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
	}
	if strings.TrimSpace(echConfig) != "" {
		_, nativeConfig, err := normalizeShareECHConfigList(echConfig)
		if err != nil {
			return nil, err
		}
		options["ech"] = map[string]any{"enabled": true, "config": nativeConfig}
	}
	return options, nil
}

type shareTLSParameters struct {
	protocol string

	mode        string
	modePresent bool
	defaultMode string
	requireTLS  bool

	serverName         string
	serverNamePresent  bool
	insecure           bool
	insecurePresent    bool
	alpn               []string
	alpnPresent        bool
	fingerprint        string
	fingerprintPresent bool
	disableSNI         bool
	disableSNIPresent  bool
	publicKey          string
	publicKeyPresent   bool
	shortID            string
	shortIDPresent     bool
	echConfig          string
	echPresent         bool
}

type shareTLSResult struct {
	mode        string
	serverName  string
	insecure    bool
	alpn        []string
	fingerprint string
	disableSNI  bool
	publicKey   string
	shortID     string
	options     models.NativeOptions
}

func shareTLSConflict(protocol string, field string) error {
	return fmt.Errorf("%s %s conflicts with tls_options", protocol, field)
}

func shareTLSBoolField(options map[string]interface{}, key string, path string) (bool, bool, error) {
	value, present := options[key]
	if !present {
		return false, false, nil
	}
	typed, ok := value.(bool)
	if !ok {
		return false, true, fmt.Errorf("tls_options.%s must be a boolean", path)
	}
	return typed, true, nil
}

func shareTLSStringField(options map[string]interface{}, key string, path string) (string, bool, error) {
	value, present := options[key]
	if !present {
		return "", false, nil
	}
	typed, ok := value.(string)
	if !ok {
		return "", true, fmt.Errorf("tls_options.%s must be a string", path)
	}
	return strings.TrimSpace(typed), true, nil
}

func shareTLSStringListField(options map[string]interface{}, key string, path string) ([]string, bool, error) {
	value, present := options[key]
	if !present {
		return nil, false, nil
	}
	switch typed := value.(type) {
	case string:
		value := strings.TrimSpace(typed)
		if value == "" {
			return nil, true, nil
		}
		return []string{value}, true, nil
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, strings.TrimSpace(item))
		}
		return result, true, nil
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, true, fmt.Errorf("tls_options.%s must contain only strings", path)
			}
			result = append(result, strings.TrimSpace(text))
		}
		return result, true, nil
	default:
		return nil, true, fmt.Errorf("tls_options.%s must be a string or string array", path)
	}
}

func shareTLSNestedField(options map[string]interface{}, key string, path string) (map[string]interface{}, bool, error) {
	value, present := options[key]
	if !present || value == nil {
		return nil, present, nil
	}
	nested, ok := generatorOptionMap(value)
	if !ok {
		return nil, true, fmt.Errorf("tls_options.%s must be an object", path)
	}
	return nested, true, nil
}

func equalShareTLSStrings(left string, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func equalShareTLSStringLists(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if strings.TrimSpace(left[index]) != strings.TrimSpace(right[index]) {
			return false
		}
	}
	return true
}

func ensureShareTLSOptions(options *models.NativeOptions) models.NativeOptions {
	if *options == nil {
		*options = models.NativeOptions{}
	}
	return *options
}

func ensureShareTLSNested(options *models.NativeOptions, key string) map[string]interface{} {
	root := ensureShareTLSOptions(options)
	nested, _ := generatorOptionMap(root[key])
	if nested == nil {
		nested = map[string]interface{}{}
		root[key] = nested
	}
	return nested
}

func reconcileShareTLSOptions(native models.NativeOptions, parameters shareTLSParameters) (shareTLSResult, error) {
	result := shareTLSResult{
		mode:        strings.ToLower(strings.TrimSpace(parameters.mode)),
		serverName:  strings.TrimSpace(parameters.serverName),
		insecure:    parameters.insecure,
		alpn:        append([]string(nil), parameters.alpn...),
		fingerprint: normalizeFingerprint(parameters.fingerprint),
		disableSNI:  parameters.disableSNI,
		publicKey:   strings.TrimSpace(parameters.publicKey),
		shortID:     strings.TrimSpace(parameters.shortID),
		options:     native,
	}

	root := map[string]interface{}(result.options)
	nativeEnabled, nativeEnabledPresent, err := shareTLSBoolField(root, "enabled", "enabled")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativeServerName, nativeServerNamePresent, err := shareTLSStringField(root, "server_name", "server_name")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativeInsecure, nativeInsecurePresent, err := shareTLSBoolField(root, "insecure", "insecure")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativeALPN, nativeALPNPresent, err := shareTLSStringListField(root, "alpn", "alpn")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativeDisableSNI, nativeDisableSNIPresent, err := shareTLSBoolField(root, "disable_sni", "disable_sni")
	if err != nil {
		return shareTLSResult{}, err
	}

	reality, _, err := shareTLSNestedField(root, "reality", "reality")
	if err != nil {
		return shareTLSResult{}, err
	}
	realityEnabled, realityEnabledPresent, err := shareTLSBoolField(reality, "enabled", "reality.enabled")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativePublicKey, nativePublicKeyPresent, err := shareTLSStringField(reality, "public_key", "reality.public_key")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativeShortID, nativeShortIDPresent, err := shareTLSStringField(reality, "short_id", "reality.short_id")
	if err != nil {
		return shareTLSResult{}, err
	}

	if realityEnabled && nativeEnabledPresent && !nativeEnabled {
		return shareTLSResult{}, shareTLSConflict(parameters.protocol, "reality mode")
	}
	if parameters.requireTLS && nativeEnabledPresent && !nativeEnabled {
		return shareTLSResult{}, shareTLSConflict(parameters.protocol, "required TLS mode")
	}

	if !parameters.modePresent {
		switch {
		case result.publicKey != "":
			result.mode = "reality"
		case realityEnabled:
			result.mode = "reality"
		case nativeEnabledPresent && nativeEnabled:
			result.mode = "tls"
		case nativeEnabledPresent && !nativeEnabled:
			result.mode = "none"
		default:
			result.mode = strings.ToLower(strings.TrimSpace(parameters.defaultMode))
		}
	}
	if parameters.requireTLS {
		if realityEnabled {
			result.mode = "reality"
		} else {
			result.mode = "tls"
		}
	}
	if result.mode == "" {
		result.mode = "none"
	}
	switch result.mode {
	case "none", "tls", "reality":
	default:
		return shareTLSResult{}, fmt.Errorf("unsupported %s TLS mode: %s", parameters.protocol, result.mode)
	}

	if parameters.modePresent {
		switch result.mode {
		case "none":
			if nativeEnabled || realityEnabled {
				return shareTLSResult{}, shareTLSConflict(parameters.protocol, "security mode")
			}
		case "tls":
			if (nativeEnabledPresent && !nativeEnabled) || realityEnabled {
				return shareTLSResult{}, shareTLSConflict(parameters.protocol, "security mode")
			}
		case "reality":
			if nativeEnabledPresent && !nativeEnabled {
				return shareTLSResult{}, shareTLSConflict(parameters.protocol, "security mode")
			}
			if realityEnabledPresent && !realityEnabled {
				return shareTLSResult{}, shareTLSConflict(parameters.protocol, "reality mode")
			}
		}
	}

	if result.mode != "reality" && ((parameters.publicKeyPresent && result.publicKey != "") || (parameters.shortIDPresent && result.shortID != "")) {
		return shareTLSResult{}, fmt.Errorf("%s Reality parameters require security=reality", parameters.protocol)
	}

	switch result.mode {
	case "none":
		if len(result.options) > 0 {
			result.options["enabled"] = false
		}
		if parameters.modePresent && result.options != nil {
			delete(result.options, "reality")
		}
	case "tls":
		ensureShareTLSOptions(&result.options)["enabled"] = true
		if parameters.modePresent {
			delete(result.options, "reality")
		}
	case "reality":
		ensureShareTLSOptions(&result.options)["enabled"] = true
	}

	if parameters.serverNamePresent {
		if nativeServerNamePresent && !equalShareTLSStrings(result.serverName, nativeServerName) {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "server_name")
		}
		if result.serverName == "" {
			if result.options != nil {
				delete(result.options, "server_name")
			}
		} else {
			ensureShareTLSOptions(&result.options)["server_name"] = result.serverName
		}
	} else if nativeServerNamePresent {
		result.serverName = nativeServerName
	}

	if parameters.insecurePresent {
		if nativeInsecurePresent && result.insecure != nativeInsecure {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "insecure")
		}
		if result.options != nil || result.insecure {
			ensureShareTLSOptions(&result.options)["insecure"] = result.insecure
		}
	} else if nativeInsecurePresent {
		result.insecure = nativeInsecure
	}

	if parameters.alpnPresent {
		if nativeALPNPresent && !equalShareTLSStringLists(result.alpn, nativeALPN) {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "alpn")
		}
		if len(result.alpn) == 0 {
			if result.options != nil {
				delete(result.options, "alpn")
			}
		} else {
			ensureShareTLSOptions(&result.options)["alpn"] = append([]string(nil), result.alpn...)
		}
	} else if nativeALPNPresent {
		result.alpn = append([]string(nil), nativeALPN...)
	}

	if parameters.disableSNIPresent {
		if nativeDisableSNIPresent && result.disableSNI != nativeDisableSNI {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "disable_sni")
		}
		if result.options != nil || result.disableSNI {
			ensureShareTLSOptions(&result.options)["disable_sni"] = result.disableSNI
		}
	} else if nativeDisableSNIPresent {
		result.disableSNI = nativeDisableSNI
	}

	utls, _, err := shareTLSNestedField(root, "utls", "utls")
	if err != nil {
		return shareTLSResult{}, err
	}
	utlsEnabled, utlsEnabledPresent, err := shareTLSBoolField(utls, "enabled", "utls.enabled")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativeFingerprint, nativeFingerprintPresent, err := shareTLSStringField(utls, "fingerprint", "utls.fingerprint")
	if err != nil {
		return shareTLSResult{}, err
	}
	if parameters.fingerprintPresent {
		standardUTLSEnabled := result.fingerprint != ""
		if utlsEnabledPresent && utlsEnabled != standardUTLSEnabled {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "uTLS mode")
		}
		if standardUTLSEnabled && nativeFingerprintPresent && !equalShareTLSStrings(result.fingerprint, nativeFingerprint) {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "uTLS fingerprint")
		}
		if standardUTLSEnabled {
			nested := ensureShareTLSNested(&result.options, "utls")
			nested["enabled"] = true
			nested["fingerprint"] = result.fingerprint
		} else if utls != nil {
			utls["enabled"] = false
			ensureShareTLSOptions(&result.options)["utls"] = utls
		}
	} else if utlsEnabled {
		result.fingerprint = normalizeFingerprint(nativeFingerprint)
	}

	if result.mode == "reality" {
		if nativeEnabledPresent && !nativeEnabled {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "security mode")
		}
		if realityEnabledPresent && !realityEnabled {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "reality mode")
		}
		if parameters.publicKeyPresent {
			if nativePublicKeyPresent && strings.TrimSpace(result.publicKey) != strings.TrimSpace(nativePublicKey) {
				return shareTLSResult{}, shareTLSConflict(parameters.protocol, "Reality public key")
			}
		} else if nativePublicKeyPresent {
			result.publicKey = nativePublicKey
		}
		if parameters.shortIDPresent {
			if nativeShortIDPresent && !equalShareTLSStrings(result.shortID, nativeShortID) {
				return shareTLSResult{}, shareTLSConflict(parameters.protocol, "Reality short ID")
			}
		} else if nativeShortIDPresent {
			result.shortID = nativeShortID
		}
		if result.publicKey == "" {
			return shareTLSResult{}, fmt.Errorf("invalid %s reality link: missing public key", parameters.protocol)
		}
		nested := ensureShareTLSNested(&result.options, "reality")
		nested["enabled"] = true
		nested["public_key"] = result.publicKey
		if result.shortID == "" {
			delete(nested, "short_id")
		} else {
			nested["short_id"] = result.shortID
		}
	}

	ech, _, err := shareTLSNestedField(root, "ech", "ech")
	if err != nil {
		return shareTLSResult{}, err
	}
	echEnabled, echEnabledPresent, err := shareTLSBoolField(ech, "enabled", "ech.enabled")
	if err != nil {
		return shareTLSResult{}, err
	}
	nativeECHConfigs, nativeECHConfigPresent, err := shareTLSStringListField(ech, "config", "ech.config")
	if err != nil {
		return shareTLSResult{}, err
	}
	finalECHEnabled := echEnabled
	if parameters.echPresent {
		standardECHEnabled := strings.TrimSpace(parameters.echConfig) != ""
		finalECHEnabled = standardECHEnabled
		if echEnabledPresent && echEnabled != standardECHEnabled {
			return shareTLSResult{}, shareTLSConflict(parameters.protocol, "ECH mode")
		}
		if standardECHEnabled {
			standardECHConfig, normalizedNativeConfig, normalizeErr := normalizeShareECHConfigList(parameters.echConfig)
			if normalizeErr != nil {
				return shareTLSResult{}, fmt.Errorf("invalid %s ECH config: %w", parameters.protocol, normalizeErr)
			}
			if nativeECHConfigPresent {
				nativeECHConfig, nativeErr := decodeNativeECHConfigList(nativeECHConfigs)
				if nativeErr != nil {
					return shareTLSResult{}, fmt.Errorf("invalid tls_options.ech.config: %w", nativeErr)
				}
				if !bytes.Equal(standardECHConfig, nativeECHConfig) {
					return shareTLSResult{}, shareTLSConflict(parameters.protocol, "ECH config")
				}
			}
			nested := ensureShareTLSNested(&result.options, "ech")
			nested["enabled"] = true
			if !nativeECHConfigPresent {
				nested["config"] = normalizedNativeConfig
			}
		} else if ech != nil {
			ech["enabled"] = false
			ensureShareTLSOptions(&result.options)["ech"] = ech
		}
	}
	if result.mode == "reality" && finalECHEnabled {
		return shareTLSResult{}, fmt.Errorf("%s Reality is incompatible with ECH in sing-box 1.12.12", parameters.protocol)
	}

	return result, nil
}

func parseHysteria2URL(link string) (*parsedProxyURL, []string, error) {
	trimmed := strings.TrimSpace(link)
	schemeEnd := strings.Index(trimmed, "://")
	if schemeEnd <= 0 {
		return nil, nil, fmt.Errorf("invalid hysteria2 link")
	}
	scheme := strings.ToLower(trimmed[:schemeEnd])
	if scheme != "hysteria2" && scheme != "hy2" {
		return nil, nil, fmt.Errorf("unexpected hysteria2 scheme: %s", scheme)
	}
	remainder := trimmed[schemeEnd+3:]
	authorityEnd := len(remainder)
	if index := strings.IndexAny(remainder, "/?#"); index >= 0 {
		authorityEnd = index
	}
	authority := remainder[:authorityEnd]
	tail := remainder[authorityEnd:]
	userinfoRaw := ""
	hostPortRaw := authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userinfoRaw = authority[:at]
		hostPortRaw = authority[at+1:]
	}
	host, portSpec, err := splitHysteria2HostPort(hostPortRaw)
	if err != nil {
		return nil, nil, err
	}
	port, serverPorts, err := normalizeHysteria2Ports(portSpec)
	if err != nil {
		return nil, nil, err
	}
	authorityForParse := net.JoinHostPort(host, strconv.Itoa(port))
	if userinfoRaw != "" {
		authorityForParse = userinfoRaw + "@" + authorityForParse
	}
	parsedURL, err := url.Parse(scheme + "://" + authorityForParse + tail)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid hysteria2 link: %w", err)
	}
	name := decodeShareLinkName(parsedURL.EscapedFragment(), "Hysteria2 Node")
	return &parsedProxyURL{URL: parsedURL, Host: host, Port: port, Name: name}, serverPorts, nil
}

func splitHysteria2HostPort(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("missing hysteria2 server")
	}
	if strings.HasPrefix(raw, "[") {
		closeBracket := strings.Index(raw, "]")
		if closeBracket < 0 {
			return "", "", fmt.Errorf("invalid bracketed IPv6 address")
		}
		host := raw[1:closeBracket]
		rest := raw[closeBracket+1:]
		if rest == "" {
			return host, "", nil
		}
		if !strings.HasPrefix(rest, ":") {
			return "", "", fmt.Errorf("invalid hysteria2 server authority")
		}
		return host, rest[1:], nil
	}
	if strings.Count(raw, ":") > 1 {
		return "", "", fmt.Errorf("IPv6 addresses must be enclosed in brackets")
	}
	if colon := strings.LastIndex(raw, ":"); colon >= 0 {
		return raw[:colon], raw[colon+1:], nil
	}
	return raw, "", nil
}

func normalizeHysteria2Ports(raw string) (int, []string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 443, nil, nil
	}
	parts := strings.Split(raw, ",")
	normalized := make([]string, 0, len(parts))
	firstPort := 0
	hasRange := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, nil, fmt.Errorf("invalid empty hysteria2 port")
		}
		rangeSeparator := ""
		if strings.Contains(part, "-") {
			rangeSeparator = "-"
		} else if strings.Contains(part, ":") {
			rangeSeparator = ":"
		}
		if rangeSeparator != "" {
			rangeParts := strings.SplitN(part, rangeSeparator, 2)
			start, startErr := strconv.Atoi(strings.TrimSpace(rangeParts[0]))
			end, endErr := strconv.Atoi(strings.TrimSpace(rangeParts[1]))
			if startErr != nil || endErr != nil || start <= 0 || end > 65535 || start > end {
				return 0, nil, fmt.Errorf("invalid hysteria2 port range: %s", part)
			}
			if firstPort == 0 {
				firstPort = start
			}
			normalized = append(normalized, fmt.Sprintf("%d:%d", start, end))
			hasRange = true
			continue
		}
		port, err := strconv.Atoi(part)
		if err != nil || port <= 0 || port > 65535 {
			return 0, nil, fmt.Errorf("invalid hysteria2 port: %s", part)
		}
		if firstPort == 0 {
			firstPort = port
		}
		normalized = append(normalized, strconv.Itoa(port))
	}
	if len(normalized) == 1 && !hasRange {
		return firstPort, nil, nil
	}
	return firstPort, normalized, nil
}

// parseVLESSLink parses VLESS share links
// Format: vless://uuid@server:port?params#name
func parseVLESSLink(link string) (interface{}, string, string, error) {
	parsed, err := parseStandardProxyURL(link, []string{"vless"}, 0, "VLESS Node")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid vless link: %w", err)
	}
	if parsed.URL.User == nil || parsed.URL.User.Username() == "" {
		return nil, "", "", fmt.Errorf("invalid vless link: missing uuid")
	}
	if _, hasPassword := parsed.URL.User.Password(); hasPassword {
		return nil, "", "", fmt.Errorf("invalid vless link: unexpected password in userinfo")
	}

	params := parsed.URL.Query()
	if err := rejectUnsupportedTLSShareParameters(params); err != nil {
		return nil, "", "", err
	}
	if err := rejectUnsupportedRealityShareParameters(params); err != nil {
		return nil, "", "", err
	}
	transportType, transportOptions, err := buildV2RayTransportOptions(params)
	if err != nil {
		return nil, "", "", err
	}
	packetEncoding, err := normalizePacketEncoding(firstQueryValue(params, "packetEncoding", "packet-encoding", "packet_encoding"))
	if err != nil {
		return nil, "", "", err
	}
	encryption := strings.TrimSpace(params.Get("encryption"))
	if encryption != "" && !strings.EqualFold(encryption, "none") {
		return nil, "", "", fmt.Errorf("vless encryption %q is not supported by sing-box 1.12.12", encryption)
	}

	securityRaw, securityPresent := queryParameter(params, "security")
	security := strings.ToLower(strings.TrimSpace(securityRaw))
	if securityPresent && security == "" {
		security = "none"
	}
	if security == "xtls" {
		security = "tls"
	}
	if security != "" && security != "none" && security != "tls" && security != "reality" {
		return nil, "", "", fmt.Errorf("unsupported vless security: %s", security)
	}

	fingerprintRaw, fingerprintPresent := queryParameter(params, "fp", "fingerprint")
	publicKey, publicKeyPresent := queryParameter(params, "pbk", "publicKey", "public-key")
	shortID, shortIDPresent := queryParameter(params, "sid", "shortId", "short-id")
	insecure, insecurePresent, err := queryTLSBool(params, "allowInsecure", "allow_insecure", "insecure")
	if err != nil {
		return nil, "", "", err
	}
	alpnRaw, alpnPresent := queryParameter(params, "alpn")
	sni, sniPresent := queryParameter(params, "sni")
	echConfig, echPresent := queryECHParameter(params)
	nativeTLS, _, err := parseNativeOptionsQuery(params, "tls_options")
	if err != nil {
		return nil, "", "", err
	}
	tlsResult, err := reconcileShareTLSOptions(nativeTLS, shareTLSParameters{
		protocol:           "vless",
		mode:               security,
		modePresent:        securityPresent,
		defaultMode:        "none",
		serverName:         sni,
		serverNamePresent:  sniPresent,
		insecure:           insecure,
		insecurePresent:    insecurePresent,
		alpn:               parseCommaSeparatedList(alpnRaw),
		alpnPresent:        alpnPresent,
		fingerprint:        fingerprintRaw,
		fingerprintPresent: fingerprintPresent,
		publicKey:          publicKey,
		publicKeyPresent:   publicKeyPresent,
		shortID:            shortID,
		shortIDPresent:     shortIDPresent,
		echConfig:          echConfig,
		echPresent:         echPresent,
	})
	if err != nil {
		return nil, "", "", err
	}

	config := models.VLESSConfig{
		Server:          parsed.Host,
		ServerPort:      parsed.Port,
		UUID:            parsed.URL.User.Username(),
		Flow:            params.Get("flow"),
		Encryption:      encryption,
		Network:         transportType,
		Security:        tlsResult.mode,
		SNI:             tlsResult.serverName,
		ALPN:            strings.Join(tlsResult.alpn, ","),
		Fingerprint:     tlsResult.fingerprint,
		PublicKey:       tlsResult.publicKey,
		ShortID:         tlsResult.shortID,
		Insecure:        tlsResult.insecure,
		Path:            params.Get("path"),
		Host:            params.Get("host"),
		ServiceName:     firstQueryValue(params, "serviceName", "service_name", "grpc-service-name"),
		HeaderType:      firstQueryValue(params, "headerType", "header_type"),
		Seed:            params.Get("seed"),
		PacketEncoding:  packetEncoding,
		MaxEarlyData:    queryPositiveInt(params, "maxEarlyData", "max_early_data", "ed"),
		EarlyDataHeader: firstQueryValue(params, "earlyDataHeaderName", "early_data_header_name", "eh"),
	}
	if transportType == "httpupgrade" {
		config.HTTPUpgradePath = config.Path
		config.HTTPUpgradeHost = config.Host
	}
	if transportType == "http" {
		if hosts := nativeStringSlice(transportOptions["host"]); len(hosts) > 0 {
			config.Host = strings.Join(hosts, ",")
		}
	}
	if transportOptions != nil {
		config.TransportOptions = models.NativeOptions(transportOptions)
	}
	outboundNetwork, err := normalizeNetworkList(params.Get("network"))
	if err != nil {
		return nil, "", "", err
	}
	if len(outboundNetwork) > 0 {
		config.OutboundNetwork = models.ListableString(outboundNetwork)
	}
	config.TLSOptions = tlsResult.options
	if nativeTransport, nativeType, present, nativeErr := parseV2RayNativeTransportQuery(params, config.Network); nativeErr != nil {
		return nil, "", "", nativeErr
	} else if present {
		config.Network = nativeType
		config.TransportOptions = nativeTransport
	}
	if multiplex, present, multiplexErr := parseMultiplexQuery(params); multiplexErr != nil {
		return nil, "", "", multiplexErr
	} else if present {
		config.MultiplexConfig = multiplex
	}
	if err := applyURLDialerOptions(&config.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	return config, "vless", parsed.Name, nil
}

// parseVMESSLink parses VMess share links
// It accepts both the legacy base64(JSON) form and the URL form maintained by Xray.
func parseVMESSLink(link string) (interface{}, string, string, error) {
	raw := strings.TrimSpace(link[len("vmess://"):])
	if strings.Contains(strings.SplitN(raw, "?", 2)[0], "@") {
		return parseVMESSURLLink(link)
	}

	decoded, err := decodeBase64String(raw)
	if err != nil {
		return nil, "", "", fmt.Errorf("failed to decode vmess link")
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
		PacketEncoding  string      `json:"packetEncoding"`
		PacketEncoding2 string      `json:"packet_encoding"`
		PublicKey       string      `json:"pbk"`
		ShortID         string      `json:"sid"`
		ECH             string      `json:"ech"`
		Method          string      `json:"method"`
	}

	if err := json.Unmarshal(decoded, &vmessJSON); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse vmess json")
	}
	var rawFields map[string]json.RawMessage
	if err := json.Unmarshal(decoded, &rawFields); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse vmess json")
	}
	for _, unsupported := range []struct {
		feature string
		keys    []string
	}{
		{feature: "pinnedPeerCertSha256", keys: []string{"pcs", "pinnedPeerCertSha256"}},
		{feature: "verifyPeerCertByName", keys: []string{"vcn", "verifyPeerCertByName"}},
		{feature: "mldsa65Verify", keys: []string{"pqv", "mldsa65Verify"}},
		{feature: "SpiderX", keys: []string{"spx", "spiderX"}},
		{feature: "FinalMask", keys: []string{"fm", "finalmask", "finalMask", "FinalMask", "final-mask", "final_mask"}},
	} {
		if err := rejectUnsupportedJSONProperty(rawFields, unsupported.feature, unsupported.keys...); err != nil {
			return nil, "", "", err
		}
	}

	name := vmessJSON.PS
	if name == "" {
		name = "VMess Node"
	}

	port := intFromAny(vmessJSON.Port)
	if strings.TrimSpace(vmessJSON.Add) == "" || port <= 0 || port > 65535 || strings.TrimSpace(vmessJSON.ID) == "" {
		return nil, "", "", fmt.Errorf("invalid vmess server/port/uuid")
	}
	alterID := intFromAny(vmessJSON.AID)
	cipher, err := normalizeVMessCipher(vmessJSON.Scy)
	if err != nil {
		return nil, "", "", err
	}
	transportSecurity, err := normalizeVMessTransportSecurity(vmessJSON.TLS)
	if err != nil {
		return nil, "", "", err
	}
	if transportSecurity == "reality" && strings.TrimSpace(vmessJSON.PublicKey) == "" {
		return nil, "", "", fmt.Errorf("invalid vmess reality link: missing public key")
	}
	transportType, transportOptions, err := buildLegacyVMessTransportOptions(
		vmessJSON.Net,
		vmessJSON.Type,
		vmessJSON.Host,
		vmessJSON.Path,
		vmessJSON.Seed,
		intFromAny(vmessJSON.MaxEarlyData),
		vmessJSON.EarlyDataHeader,
		vmessJSON.Method,
	)
	if err != nil {
		return nil, "", "", err
	}
	packetEncoding, err := normalizePacketEncoding(firstNonEmpty(vmessJSON.PacketEncoding, vmessJSON.PacketEncoding2))
	if err != nil {
		return nil, "", "", err
	}
	fingerprint := normalizeFingerprint(vmessJSON.FP)
	alpn := parseCommaSeparatedList(vmessJSON.ALPN)

	config := models.VMESSConfig{
		Server:         vmessJSON.Add,
		ServerPort:     port,
		UUID:           vmessJSON.ID,
		AlterID:        alterID,
		Security:       cipher,
		Network:        transportType,
		TLS:            transportSecurity,
		SNI:            vmessJSON.SNI,
		ALPN:           strings.Join(alpn, ","),
		Fingerprint:    fingerprint,
		Path:           vmessJSON.Path,
		Host:           vmessJSON.Host,
		HeaderType:     vmessJSON.Type,
		Seed:           vmessJSON.Seed,
		Method:         vmessJSON.Method,
		PacketEncoding: packetEncoding,
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
	config.MaxEarlyData = intFromAny(vmessJSON.MaxEarlyData)
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
	if transportType == "grpc" {
		config.ServiceName = vmessJSON.Path
	}

	// HTTPUpgrade specific
	if transportType == "httpupgrade" {
		config.HTTPUpgradePath = vmessJSON.Path
		config.HTTPUpgradeHost = vmessJSON.Host
	}
	if transportType == "http" && vmessJSON.Path != "" {
		config.HTTPPath = []string{vmessJSON.Path}
	}
	if transportType == "http" {
		if hosts := nativeStringSlice(transportOptions["host"]); len(hosts) > 0 {
			config.Host = strings.Join(hosts, ",")
		}
	}
	if transportOptions != nil {
		config.TransportOptions = models.NativeOptions(transportOptions)
	}
	security := transportSecurity
	tlsEnabled := security == "tls" || security == "reality"
	tlsOptions, err := buildTLSOptions(tlsEnabled, false, config.SNI, config.Insecure, alpn, fingerprint, vmessJSON.PublicKey, vmessJSON.ShortID, vmessJSON.ECH)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid vmess ECH config: %w", err)
	}
	if tlsOptions != nil {
		config.TLSOptions = models.NativeOptions(tlsOptions)
	}

	return config, "vmess", name, nil
}

func parseVMESSURLLink(link string) (interface{}, string, string, error) {
	parsed, err := parseStandardProxyURL(link, []string{"vmess"}, 0, "VMess Node")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid vmess url: %w", err)
	}
	if parsed.URL.User == nil || parsed.URL.User.Username() == "" {
		return nil, "", "", fmt.Errorf("invalid vmess url: missing uuid")
	}
	if _, hasPassword := parsed.URL.User.Password(); hasPassword {
		return nil, "", "", fmt.Errorf("invalid vmess url: unexpected password in userinfo")
	}

	params := parsed.URL.Query()
	if err := rejectUnsupportedTLSShareParameters(params); err != nil {
		return nil, "", "", err
	}
	if err := rejectUnsupportedRealityShareParameters(params); err != nil {
		return nil, "", "", err
	}
	transportType, transportOptions, err := buildV2RayTransportOptions(params)
	if err != nil {
		return nil, "", "", err
	}
	packetEncoding, err := normalizePacketEncoding(firstQueryValue(params, "packetEncoding", "packet-encoding", "packet_encoding"))
	if err != nil {
		return nil, "", "", err
	}
	securityRaw, securityPresent := queryParameter(params, "security")
	security, err := normalizeVMessTransportSecurity(securityRaw)
	if err != nil {
		return nil, "", "", err
	}
	if securityPresent && security == "" {
		security = "none"
	}
	publicKey, publicKeyPresent := queryParameter(params, "pbk", "publicKey", "public-key")
	shortID, shortIDPresent := queryParameter(params, "sid", "shortId", "short-id")
	fingerprintRaw, fingerprintPresent := queryParameter(params, "fp", "fingerprint")
	insecure, insecurePresent, err := queryTLSBool(params, "allowInsecure", "allow_insecure", "insecure")
	if err != nil {
		return nil, "", "", err
	}
	alpnRaw, alpnPresent := queryParameter(params, "alpn")
	sni, sniPresent := queryParameter(params, "sni")
	echConfig, echPresent := queryECHParameter(params)
	nativeTLS, _, err := parseNativeOptionsQuery(params, "tls_options")
	if err != nil {
		return nil, "", "", err
	}
	tlsResult, err := reconcileShareTLSOptions(nativeTLS, shareTLSParameters{
		protocol:           "vmess",
		mode:               security,
		modePresent:        securityPresent,
		defaultMode:        "none",
		serverName:         sni,
		serverNamePresent:  sniPresent,
		insecure:           insecure,
		insecurePresent:    insecurePresent,
		alpn:               parseCommaSeparatedList(alpnRaw),
		alpnPresent:        alpnPresent,
		fingerprint:        fingerprintRaw,
		fingerprintPresent: fingerprintPresent,
		publicKey:          publicKey,
		publicKeyPresent:   publicKeyPresent,
		shortID:            shortID,
		shortIDPresent:     shortIDPresent,
		echConfig:          echConfig,
		echPresent:         echPresent,
	})
	if err != nil {
		return nil, "", "", err
	}

	cipher, err := normalizeVMessCipher(params.Get("encryption"))
	if err != nil {
		return nil, "", "", err
	}
	config := models.VMESSConfig{
		Server:              parsed.Host,
		ServerPort:          parsed.Port,
		UUID:                parsed.URL.User.Username(),
		AlterID:             queryNonNegativeInt(params, "alterId", "alter_id", "aid"),
		Security:            cipher,
		Network:             transportType,
		TLS:                 tlsResult.mode,
		SNI:                 tlsResult.serverName,
		ALPN:                strings.Join(tlsResult.alpn, ","),
		Fingerprint:         tlsResult.fingerprint,
		Insecure:            tlsResult.insecure,
		Path:                params.Get("path"),
		Host:                params.Get("host"),
		MaxEarlyData:        queryPositiveInt(params, "maxEarlyData", "max_early_data", "ed"),
		EarlyDataHeader:     firstQueryValue(params, "earlyDataHeaderName", "early_data_header_name", "eh"),
		ServiceName:         firstQueryValue(params, "serviceName", "service_name", "grpc-service-name"),
		PacketEncoding:      packetEncoding,
		GlobalPadding:       queryBoolValue(params, "globalPadding", "global-padding", "global_padding"),
		AuthenticatedLength: queryBoolValue(params, "authenticatedLength", "authenticated-length", "authenticated_length"),
	}
	if transportType == "http" && config.Path != "" {
		config.HTTPPath = []string{config.Path}
		config.Method = params.Get("method")
	}
	if transportType == "http" {
		if hosts := nativeStringSlice(transportOptions["host"]); len(hosts) > 0 {
			config.Host = strings.Join(hosts, ",")
		}
	}
	if transportType == "httpupgrade" {
		config.HTTPUpgradePath = config.Path
		config.HTTPUpgradeHost = config.Host
	}
	if transportOptions != nil {
		config.TransportOptions = models.NativeOptions(transportOptions)
	}
	outboundNetwork, err := normalizeNetworkList(params.Get("network"))
	if err != nil {
		return nil, "", "", err
	}
	if len(outboundNetwork) > 0 {
		config.OutboundNetwork = models.ListableString(outboundNetwork)
	}
	config.TLSOptions = tlsResult.options
	if nativeTransport, present, nativeErr := parseNativeOptionsQuery(params, "transport_options"); nativeErr != nil {
		return nil, "", "", nativeErr
	} else if present {
		nativeType := strings.ToLower(nativeString(nativeTransport["type"]))
		if nativeType == "h2" {
			nativeType = "http"
			nativeTransport["type"] = nativeType
		}
		switch nativeType {
		case "ws", "http", "quic", "grpc", "httpupgrade":
		default:
			return nil, "", "", fmt.Errorf("invalid vmess transport_options type: %s", nativeType)
		}
		if config.Network != "" && config.Network != nativeType {
			return nil, "", "", fmt.Errorf("vmess transport type conflicts with transport_options")
		}
		config.Network = nativeType
		config.TransportOptions = nativeTransport
	}
	if rawMultiplex := strings.TrimSpace(params.Get("multiplex")); rawMultiplex != "" {
		var multiplex map[string]interface{}
		if err := json.Unmarshal([]byte(rawMultiplex), &multiplex); err != nil {
			return nil, "", "", fmt.Errorf("invalid vmess multiplex options: %w", err)
		}
		if multiplex == nil {
			return nil, "", "", fmt.Errorf("invalid vmess multiplex options: expected object")
		}
		config.MultiplexConfig = multiplex
	}
	if err := applyURLDialerOptions(&config.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	return config, "vmess", parsed.Name, nil
}

func parseNativeOptionsQuery(values url.Values, key string) (models.NativeOptions, bool, error) {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return nil, false, nil
	}
	var options map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil, true, fmt.Errorf("invalid %s: %w", key, err)
	}
	if options == nil {
		return nil, true, fmt.Errorf("invalid %s: expected object", key)
	}
	if key == "tls_options" {
		if err := validateGeneratorTLSOptionSchema(options); err != nil {
			return nil, true, fmt.Errorf("invalid %s: %w", key, err)
		}
	}
	return models.NativeOptions(options), true, nil
}

func parseV2RayNativeTransportQuery(values url.Values, currentType string) (models.NativeOptions, string, bool, error) {
	options, present, err := parseNativeOptionsQuery(values, "transport_options")
	if err != nil || !present {
		return nil, currentType, present, err
	}
	transportType := strings.ToLower(nativeString(options["type"]))
	if transportType == "h2" {
		transportType = "http"
		options["type"] = transportType
	}
	switch transportType {
	case "ws", "http", "quic", "grpc", "httpupgrade":
	default:
		return nil, currentType, true, fmt.Errorf("invalid transport_options type: %s", transportType)
	}
	if currentType != "" && currentType != transportType {
		return nil, currentType, true, fmt.Errorf("transport type conflicts with transport_options")
	}
	return options, transportType, true, nil
}

func parseMultiplexQuery(values url.Values) (map[string]interface{}, bool, error) {
	raw := strings.TrimSpace(values.Get("multiplex"))
	if raw == "" {
		return nil, false, nil
	}
	var options map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &options); err != nil {
		return nil, true, fmt.Errorf("invalid multiplex options: %w", err)
	}
	if options == nil {
		return nil, true, fmt.Errorf("invalid multiplex options: expected object")
	}
	return options, true, nil
}

// parseTrojanLink parses Trojan share links
// Format: trojan://password@server:port?params#name
func parseTrojanLink(link string) (interface{}, string, string, error) {
	parsed, err := parseStandardProxyURL(link, []string{"trojan"}, 0, "Trojan Node")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid trojan link: %w", err)
	}
	password := decodedURLUserInfo(parsed.URL)
	if password == "" {
		return nil, "", "", fmt.Errorf("invalid trojan link: missing password")
	}
	params := parsed.URL.Query()
	if err := rejectUnsupportedTLSShareParameters(params); err != nil {
		return nil, "", "", err
	}
	if err := rejectUnsupportedRealityShareParameters(params); err != nil {
		return nil, "", "", err
	}
	network, transportOptions, err := buildV2RayTransportOptions(params)
	if err != nil {
		return nil, "", "", err
	}

	insecure, insecurePresent, err := queryTLSBool(params, "allowInsecure", "allow_insecure", "insecure")
	if err != nil {
		return nil, "", "", err
	}
	sni, sniPresent := queryParameter(params, "sni", "peer")
	alpnRaw, alpnPresent := queryParameter(params, "alpn")
	fingerprintRaw, fingerprintPresent := queryParameter(params, "fp", "fingerprint")
	securityRaw, securityPresent := queryParameter(params, "security")
	security := strings.ToLower(strings.TrimSpace(securityRaw))
	if securityPresent && security == "" {
		security = "tls"
	}
	if security == "xtls" {
		security = "tls"
	}
	if security != "" && security != "none" && security != "tls" && security != "reality" {
		return nil, "", "", fmt.Errorf("unsupported trojan security: %s", security)
	}
	publicKey, publicKeyPresent := queryParameter(params, "pbk", "publicKey", "public-key")
	shortID, shortIDPresent := queryParameter(params, "sid", "shortId", "short-id")
	echConfig, echPresent := queryECHParameter(params)
	nativeTLS, _, err := parseNativeOptionsQuery(params, "tls_options")
	if err != nil {
		return nil, "", "", err
	}
	tlsResult, err := reconcileShareTLSOptions(nativeTLS, shareTLSParameters{
		protocol:           "trojan",
		mode:               security,
		modePresent:        securityPresent,
		defaultMode:        "tls",
		serverName:         sni,
		serverNamePresent:  sniPresent,
		insecure:           insecure,
		insecurePresent:    insecurePresent,
		alpn:               parseCommaSeparatedList(alpnRaw),
		alpnPresent:        alpnPresent,
		fingerprint:        fingerprintRaw,
		fingerprintPresent: fingerprintPresent,
		publicKey:          publicKey,
		publicKeyPresent:   publicKeyPresent,
		shortID:            shortID,
		shortIDPresent:     shortIDPresent,
		echConfig:          echConfig,
		echPresent:         echPresent,
	})
	if err != nil {
		return nil, "", "", err
	}

	cfg := models.TrojanConfig{
		Server:      parsed.Host,
		ServerPort:  parsed.Port,
		Password:    password,
		Security:    tlsResult.mode,
		Network:     network,
		SNI:         tlsResult.serverName,
		ALPN:        tlsResult.alpn,
		Fingerprint: tlsResult.fingerprint,
		Insecure:    tlsResult.insecure,
		Host:        params.Get("host"),
		Path:        params.Get("path"),
		ServiceName: firstQueryValue(params, "serviceName", "service_name", "grpc-service-name"),
		HTTPMethod:  params.Get("method"),
	}
	if network == "http" {
		if hosts := nativeStringSlice(transportOptions["host"]); len(hosts) > 0 {
			cfg.Host = strings.Join(hosts, ",")
		}
	}
	if transportOptions != nil {
		cfg.TransportOptions = models.NativeOptions(transportOptions)
	}
	outboundNetwork, err := normalizeNetworkList(params.Get("network"))
	if err != nil {
		return nil, "", "", err
	}
	if len(outboundNetwork) > 0 {
		cfg.OutboundNetwork = models.ListableString(outboundNetwork)
	}
	cfg.TLSOptions = tlsResult.options
	if nativeTransport, nativeType, present, nativeErr := parseV2RayNativeTransportQuery(params, cfg.Network); nativeErr != nil {
		return nil, "", "", nativeErr
	} else if present {
		cfg.Network = nativeType
		cfg.TransportOptions = nativeTransport
	}
	if multiplex, present, multiplexErr := parseMultiplexQuery(params); multiplexErr != nil {
		return nil, "", "", multiplexErr
	} else if present {
		cfg.MultiplexConfig = multiplex
	}
	if err := applyURLDialerOptions(&cfg.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	return cfg, "trojan", parsed.Name, nil
}

// parseHysteria2Link parses Hysteria2 share links
// Format: hysteria2://password@server:port?params#name or hy2://...
func parseHysteria2Link(link string) (interface{}, string, string, error) {
	parsed, serverPorts, err := parseHysteria2URL(link)
	if err != nil {
		return nil, "", "", err
	}
	params := parsed.URL.Query()
	if err := rejectUnsupportedTLSShareParameters(params); err != nil {
		return nil, "", "", err
	}
	if pin := firstQueryValue(params, "pinSHA256", "pin-sha256", "pin_sha256"); pin != "" {
		return nil, "", "", fmt.Errorf("hysteria2 certificate pinning is not supported by sing-box 1.12.12")
	}
	obfs := strings.ToLower(strings.TrimSpace(params.Get("obfs")))
	if obfs == "none" {
		obfs = ""
	}
	if obfs != "" && obfs != "salamander" {
		return nil, "", "", fmt.Errorf("unsupported hysteria2 obfs for sing-box 1.12.12: %s", obfs)
	}
	insecure, insecurePresent, err := queryTLSBool(params, "insecure", "allowInsecure", "allow_insecure")
	if err != nil {
		return nil, "", "", err
	}
	alpnRaw, alpnPresent := queryParameter(params, "alpn")
	fingerprintRaw, fingerprintPresent := queryParameter(params, "fp", "fingerprint")
	sni, sniPresent := queryParameter(params, "sni")
	echConfig, echPresent := queryECHParameter(params)
	nativeTLS, _, err := parseNativeOptionsQuery(params, "tls_options")
	if err != nil {
		return nil, "", "", err
	}
	tlsResult, err := reconcileShareTLSOptions(nativeTLS, shareTLSParameters{
		protocol:           "hysteria2",
		defaultMode:        "tls",
		requireTLS:         true,
		serverName:         sni,
		serverNamePresent:  sniPresent,
		insecure:           insecure,
		insecurePresent:    insecurePresent,
		alpn:               parseCommaSeparatedList(alpnRaw),
		alpnPresent:        alpnPresent,
		fingerprint:        fingerprintRaw,
		fingerprintPresent: fingerprintPresent,
		echConfig:          echConfig,
		echPresent:         echPresent,
	})
	if err != nil {
		return nil, "", "", err
	}
	hopInterval := normalizeDurationString(firstQueryValue(params, "hopInterval", "hop-interval", "hop_interval"), time.Second)

	network, err := normalizeNetworkList(params.Get("network"))
	if err != nil {
		return nil, "", "", err
	}
	config := models.Hysteria2Config{
		Server:             parsed.Host,
		ServerPort:         parsed.Port,
		Password:           decodedURLUserInfo(parsed.URL),
		UpMbps:             parseBandwidthMbps(params.Get("up")),
		DownMbps:           parseBandwidthMbps(params.Get("down")),
		BrutalUpMbps:       queryPositiveInt(params, "brutal_up_mbps", "brutal-up-mbps"),
		BrutalDownMbps:     queryPositiveInt(params, "brutal_down_mbps", "brutal-down-mbps"),
		ObfsPassword:       firstQueryValue(params, "obfs-password", "obfs_password"),
		SalamanderPassword: params.Get("salamander"),
		SNI:                tlsResult.serverName,
		ALPN:               tlsResult.alpn,
		Fingerprint:        tlsResult.fingerprint,
		InsecureSkipVerify: tlsResult.insecure,
		Network:            models.ListableString(network),
		HopInterval:        hopInterval,
	}
	if config.ObfsPassword == "" {
		config.ObfsPassword = config.SalamanderPassword
	}
	if obfs != "" {
		config.Obfs = models.NativeOptions{"type": obfs, "password": config.ObfsPassword}
	}
	if len(serverPorts) > 0 {
		config.ServerPorts = models.ListableString(serverPorts)
	}
	if value, present := queryBool(params, "brutal_debug", "brutal-debug"); present {
		config.BrutalDebug = value
	}
	config.TLSOptions = tlsResult.options
	if err := applyURLDialerOptions(&config.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	return config, "hy2", parsed.Name, nil
}

// parseTUICLink parses TUIC share links
// Format: tuic://uuid:password@server:port?params#name
func parseTUICLink(link string) (interface{}, string, string, error) {
	parsed, err := parseStandardProxyURL(link, []string{"tuic"}, 443, "TUIC Node")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid tuic link: %w", err)
	}
	if parsed.URL.User == nil {
		return nil, "", "", fmt.Errorf("invalid tuic link: missing uuid")
	}
	uuid := parsed.URL.User.Username()
	password, _ := parsed.URL.User.Password()
	if uuid == "" {
		return nil, "", "", fmt.Errorf("invalid tuic link: missing uuid")
	}
	params := parsed.URL.Query()
	if err := rejectUnsupportedTLSShareParameters(params); err != nil {
		return nil, "", "", err
	}
	insecure, insecurePresent, err := queryTLSBool(params, "insecure", "allowInsecure", "allow_insecure")
	if err != nil {
		return nil, "", "", err
	}
	zeroRTT := queryBoolValue(params, "zero_rtt_handshake", "zero-rtt-handshake", "reduce_rtt", "reduce-rtt")
	disableSNI, disableSNIPresent, err := queryTLSBool(params, "disable_sni", "disable-sni")
	if err != nil {
		return nil, "", "", err
	}
	udpOverStream := queryBoolValue(params, "udp_over_stream", "udp-over-stream")
	alpnRaw, alpnPresent := queryParameter(params, "alpn")
	fingerprintRaw, fingerprintPresent := queryParameter(params, "fp", "fingerprint")
	sni, sniPresent := queryParameter(params, "sni")
	echConfig, echPresent := queryECHParameter(params)
	nativeTLS, _, err := parseNativeOptionsQuery(params, "tls_options")
	if err != nil {
		return nil, "", "", err
	}
	tlsResult, err := reconcileShareTLSOptions(nativeTLS, shareTLSParameters{
		protocol:           "tuic",
		defaultMode:        "tls",
		requireTLS:         true,
		serverName:         sni,
		serverNamePresent:  sniPresent,
		insecure:           insecure,
		insecurePresent:    insecurePresent,
		alpn:               parseCommaSeparatedList(alpnRaw),
		alpnPresent:        alpnPresent,
		fingerprint:        fingerprintRaw,
		fingerprintPresent: fingerprintPresent,
		disableSNI:         disableSNI,
		disableSNIPresent:  disableSNIPresent,
		echConfig:          echConfig,
		echPresent:         echPresent,
	})
	if err != nil {
		return nil, "", "", err
	}
	heartbeatRaw := params.Get("heartbeat")
	heartbeatUnit := time.Second
	if heartbeatRaw == "" {
		heartbeatRaw = firstQueryValue(params, "heartbeat-interval", "heartbeat_interval")
		heartbeatUnit = time.Millisecond
	}

	network, err := normalizeNetworkList(params.Get("network"))
	if err != nil {
		return nil, "", "", err
	}
	config := models.TUICConfig{
		Server:             parsed.Host,
		ServerPort:         parsed.Port,
		UUID:               uuid,
		Password:           password,
		CongestionControl:  firstQueryValue(params, "congestion_control", "congestion-control", "congestion-controller"),
		UDPRelayMode:       firstQueryValue(params, "udp_relay_mode", "udp-relay-mode"),
		SNI:                tlsResult.serverName,
		ALPN:               tlsResult.alpn,
		Fingerprint:        tlsResult.fingerprint,
		InsecureSkipVerify: tlsResult.insecure,
		ZeroRTTHandshake:   zeroRTT,
		DisableSNI:         tlsResult.disableSNI,
		Heartbeat:          normalizeDurationString(heartbeatRaw, heartbeatUnit),
		Network:            models.ListableString(network),
	}
	config.UDPOverStream = udpOverStream
	config.TLSOptions = tlsResult.options
	if err := applyURLDialerOptions(&config.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	return config, "tuic", parsed.Name, nil
}

// parseAnyTLSLink parses AnyTLS share links
// Format: anytls://password@server:port?params#name
func parseAnyTLSLink(link string) (interface{}, string, string, error) {
	parsed, err := parseStandardProxyURL(link, []string{"anytls"}, 443, "AnyTLS Node")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid anytls link: %w", err)
	}
	params := parsed.URL.Query()
	if err := rejectUnsupportedTLSShareParameters(params); err != nil {
		return nil, "", "", err
	}
	insecure, insecurePresent, err := queryTLSBool(params, "insecure", "allowInsecure", "allow_insecure")
	if err != nil {
		return nil, "", "", err
	}
	sni, sniPresent := queryParameter(params, "sni")
	disableSNI, disableSNIPresent, err := queryTLSBool(params, "disable_sni", "disable-sni")
	if err != nil {
		return nil, "", "", err
	}
	alpnRaw, alpnPresent := queryParameter(params, "alpn")
	fingerprintRaw, fingerprintPresent := queryParameter(params, "fp", "fingerprint")
	echConfig, echPresent := queryECHParameter(params)
	nativeTLS, _, err := parseNativeOptionsQuery(params, "tls_options")
	if err != nil {
		return nil, "", "", err
	}
	_, nativeDisableSNIPresent := nativeTLS["disable_sni"]
	if sniPresent && net.ParseIP(strings.Trim(sni, "[]")) != nil && !disableSNIPresent && !nativeDisableSNIPresent {
		disableSNI = true
		disableSNIPresent = true
	}
	tlsResult, err := reconcileShareTLSOptions(nativeTLS, shareTLSParameters{
		protocol:           "anytls",
		defaultMode:        "tls",
		requireTLS:         true,
		serverName:         sni,
		serverNamePresent:  sniPresent,
		insecure:           insecure,
		insecurePresent:    insecurePresent,
		alpn:               parseCommaSeparatedList(alpnRaw),
		alpnPresent:        alpnPresent,
		fingerprint:        fingerprintRaw,
		fingerprintPresent: fingerprintPresent,
		disableSNI:         disableSNI,
		disableSNIPresent:  disableSNIPresent,
		echConfig:          echConfig,
		echPresent:         echPresent,
	})
	if err != nil {
		return nil, "", "", err
	}
	if tlsResult.serverName == "" && !tlsResult.disableSNI {
		if net.ParseIP(strings.Trim(parsed.Host, "[]")) != nil {
			if !disableSNIPresent && !nativeDisableSNIPresent {
				tlsResult.disableSNI = true
				ensureShareTLSOptions(&tlsResult.options)["disable_sni"] = true
			}
		} else {
			tlsResult.serverName = parsed.Host
			ensureShareTLSOptions(&tlsResult.options)["server_name"] = parsed.Host
		}
	}

	config := models.AnyTLSConfig{
		Server:                   parsed.Host,
		ServerPort:               parsed.Port,
		Password:                 decodedURLUserInfo(parsed.URL),
		SNI:                      tlsResult.serverName,
		ALPN:                     tlsResult.alpn,
		Fingerprint:              tlsResult.fingerprint,
		Insecure:                 tlsResult.insecure,
		IdleSessionCheckInterval: normalizeDurationString(firstQueryValue(params, "idle_session_check_interval", "idle-session-check-interval"), time.Second),
		IdleSessionTimeout:       normalizeDurationString(firstQueryValue(params, "idle_session_timeout", "idle-session-timeout"), time.Second),
	}

	if minIdle := firstQueryValue(params, "min_idle_session", "min-idle-session"); minIdle != "" {
		if val, err := strconv.Atoi(minIdle); err == nil {
			config.MinIdleSession = val
		}
	}
	config.TLSOptions = tlsResult.options
	if err := applyURLDialerOptions(&config.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	return config, "anytls", parsed.Name, nil
}

func parseSOCKS5Link(link string) (interface{}, string, string, error) {
	parsed, err := parseStandardProxyURL(link, []string{"socks", "socks5", "socks5h"}, 1080, "SOCKS5 Node")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid socks5 link: %v", err)
	}

	cfg := models.SOCKS5Config{
		Server:     parsed.Host,
		ServerPort: parsed.Port,
	}

	if parsed.URL.User != nil {
		cfg.Username = parsed.URL.User.Username()
		if pwd, ok := parsed.URL.User.Password(); ok {
			cfg.Password = pwd
		}
	}
	params := parsed.URL.Query()
	network, err := normalizeNetworkList(params.Get("network"))
	if err != nil {
		return nil, "", "", err
	}
	if len(network) > 0 {
		cfg.Network = models.ListableString(network)
	}
	if enabled, present := queryBool(params, "udp_over_tcp", "udp-over-tcp", "uot"); present {
		cfg.UDPOverTCP = enabled
		if version := queryPositiveInt(params, "udp_over_tcp_version", "udp-over-tcp-version", "uot_version"); version > 0 {
			if version > 2 {
				return nil, "", "", fmt.Errorf("unsupported udp-over-tcp version: %d", version)
			}
			cfg.UDPOverTCPOptions = models.NativeOptions{"enabled": enabled, "version": version}
			cfg.UDPOverTCP = models.NativeOptions(cfg.UDPOverTCPOptions)
		}
	}
	if err := applyURLDialerOptions(&cfg.DialerOptions, params); err != nil {
		return nil, "", "", err
	}

	proxyType := "socks5"
	if strings.EqualFold(parsed.URL.Scheme, "socks5h") {
		proxyType = "socks5h"
	}

	return cfg, proxyType, parsed.Name, nil
}

func parseHTTPProxyLink(link string) (interface{}, string, string, error) {
	defaultPort := 80
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(link)), "https://") {
		defaultPort = 443
	}
	parsed, err := parseStandardProxyURL(link, []string{"http", "https"}, defaultPort, "HTTP Proxy")
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid http proxy link: %v", err)
	}

	q := parsed.URL.Query()
	if err := rejectUnsupportedTLSShareParameters(q); err != nil {
		return nil, "", "", err
	}
	insecure, insecurePresent, err := queryTLSBool(q, "insecure", "allowInsecure", "allow_insecure")
	if err != nil {
		return nil, "", "", err
	}
	sni, sniPresent := queryParameter(q, "sni")
	alpnRaw, alpnPresent := queryParameter(q, "alpn")
	fingerprintRaw, fingerprintPresent := queryParameter(q, "fp", "fingerprint")
	echConfig, echPresent := queryECHParameter(q)
	nativeTLS, _, err := parseNativeOptionsQuery(q, "tls_options")
	if err != nil {
		return nil, "", "", err
	}
	isHTTPS := strings.EqualFold(parsed.URL.Scheme, "https")
	tlsParameters := shareTLSParameters{
		protocol:           "http",
		defaultMode:        "tls",
		requireTLS:         isHTTPS,
		serverName:         sni,
		serverNamePresent:  sniPresent,
		insecure:           insecure,
		insecurePresent:    insecurePresent,
		alpn:               parseCommaSeparatedList(alpnRaw),
		alpnPresent:        alpnPresent,
		fingerprint:        fingerprintRaw,
		fingerprintPresent: fingerprintPresent,
		echConfig:          echConfig,
		echPresent:         echPresent,
	}
	if !isHTTPS {
		tlsParameters.mode = "none"
		tlsParameters.modePresent = true
		tlsParameters.defaultMode = "none"
	}
	tlsResult, err := reconcileShareTLSOptions(nativeTLS, tlsParameters)
	if err != nil {
		return nil, "", "", err
	}
	cfg := models.HTTPProxyConfig{
		Server:     parsed.Host,
		ServerPort: parsed.Port,
		TLS:        tlsResult.mode != "none",
		SNI:        tlsResult.serverName,
		Insecure:   tlsResult.insecure,
	}

	if parsed.URL.User != nil {
		cfg.Username = parsed.URL.User.Username()
		if pwd, ok := parsed.URL.User.Password(); ok {
			cfg.Password = pwd
		}
	}
	path := parsed.URL.EscapedPath()
	if path != "" {
		if decodedPath, pathErr := url.PathUnescape(path); pathErr == nil {
			path = decodedPath
		}
	}
	if path == "/" {
		path = ""
	}
	if path != "" {
		cfg.Path = path
	}
	if rawHeaders := q.Get("headers"); rawHeaders != "" {
		headers := map[string]any{}
		if err := json.Unmarshal([]byte(rawHeaders), &headers); err != nil {
			return nil, "", "", fmt.Errorf("invalid http proxy headers: %w", err)
		}
		cfg.Headers = models.NativeOptions(headers)
	}
	cfg.TLSOptions = tlsResult.options
	if err := applyURLDialerOptions(&cfg.DialerOptions, q); err != nil {
		return nil, "", "", err
	}

	return cfg, "http", parsed.Name, nil
}

func parseWireGuardLink(link string) (interface{}, string, string, error) {
	normalizedLink := strings.Replace(link, "wg://", "wireguard://", 1)
	parsed, err := url.Parse(normalizedLink)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid wireguard link: %v", err)
	}

	name := "Cloudflare WireGuard"
	if parsed.Fragment != "" {
		name = decodeShareLinkName(parsed.EscapedFragment(), name)
	}

	portStr := parsed.Port()
	if portStr == "" {
		return nil, "", "", fmt.Errorf("missing port")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, "", "", fmt.Errorf("invalid port")
	}

	query := parsed.Query()
	privateKey := ""
	if parsed.User != nil {
		privateKey = parsed.User.Username()
	}
	if privateKey == "" {
		privateKey = firstQueryValue(query, "privatekey", "private-key", "private_key", "secretkey", "secret-key")
	}
	if privateKey == "" {
		return nil, "", "", fmt.Errorf("missing private key")
	}

	var domainResolverValue any
	if rawResolver := firstQueryValue(query, "domain_resolver", "domain-resolver"); rawResolver != "" {
		domainResolverValue = rawResolver
		if strings.HasPrefix(strings.TrimSpace(rawResolver), "{") {
			if err := json.Unmarshal([]byte(rawResolver), &domainResolverValue); err != nil {
				return nil, "", "", fmt.Errorf("invalid wireguard domain resolver: %w", err)
			}
		}
	}
	cfg := models.WireGuardConfig{
		Server:              parsed.Hostname(),
		ServerPort:          port,
		LocalAddress:        collectWireGuardAddressesFromQuery(query),
		PrivateKey:          privateKey,
		PeerPublicKey:       firstQueryValue(query, "publickey", "public-key", "public_key", "peer", "peer_public_key"),
		PreSharedKey:        firstQueryValue(query, "presharedkey", "pre-shared-key", "pre_shared_key", "psk"),
		AllowedIPs:          parseWireGuardList(firstQueryValue(query, "allowedips", "allowed-ips", "allowed_ips")),
		InterfaceName:       firstQueryValue(query, "interface_name", "interface-name", "name"),
		Network:             firstQueryValue(query, "network"),
		Detour:              firstQueryValue(query, "detour", "dialer-proxy", "dialer_proxy"),
		DomainResolver:      domainResolverValue,
		ConnectTimeout:      normalizeDurationString(firstQueryValue(query, "connect_timeout", "connect-timeout"), time.Second),
		RoutingMark:         parseWireGuardRoutingMark(firstQueryValue(query, "routing_mark", "routing-mark")),
		BindInterface:       firstQueryValue(query, "bind_interface", "bind-interface"),
		Inet4BindAddress:    firstQueryValue(query, "inet4_bind_address", "inet4-bind-address"),
		Inet6BindAddress:    firstQueryValue(query, "inet6_bind_address", "inet6-bind-address"),
		ProtectPath:         firstQueryValue(query, "protect_path", "protect-path"),
		ReuseAddr:           queryBoolValue(query, "reuse_addr", "reuse-addr"),
		NetNS:               firstQueryValue(query, "netns", "net-ns"),
		TCPFastOpen:         queryBoolValue(query, "tcp_fast_open", "tcp-fast-open", "tfo"),
		TCPMultiPath:        queryBoolValue(query, "tcp_multi_path", "tcp-multi-path", "mptcp"),
		NetworkStrategy:     firstQueryValue(query, "network_strategy", "network-strategy"),
		NetworkType:         models.ListableString(normalizeLooseStringList(firstQueryValue(query, "network_type", "network-type"))),
		FallbackNetworkType: models.ListableString(normalizeLooseStringList(firstQueryValue(query, "fallback_network_type", "fallback-network-type"))),
		FallbackDelay:       normalizeDurationString(firstQueryValue(query, "fallback_delay", "fallback-delay"), time.Second),
		DomainStrategy:      firstQueryValue(query, "domain_strategy", "domain-strategy", "ip-version", "ip_version"),
	}

	if len(cfg.LocalAddress) == 0 {
		return nil, "", "", fmt.Errorf("missing local address")
	}
	if cfg.PeerPublicKey == "" {
		return nil, "", "", fmt.Errorf("missing peer public key")
	}

	if reserved, err := parseWireGuardReservedString(firstQueryValue(query, "reserved")); err != nil {
		return nil, "", "", err
	} else {
		cfg.Reserved = reserved
	}

	if systemInterface, ok := parseWireGuardBool(firstQueryValue(query, "system_interface", "system-interface")); ok {
		cfg.SystemInterface = *systemInterface
	}
	if udpFragment, ok := parseWireGuardBool(firstQueryValue(query, "udp_fragment", "udp-fragment")); ok {
		cfg.UDPFragment = udpFragment
	}
	if mtu, ok := parseWireGuardInt(firstQueryValue(query, "mtu")); ok {
		cfg.MTU = mtu
	}
	if workers, ok := parseWireGuardInt(firstQueryValue(query, "workers")); ok {
		cfg.Workers = workers
	}
	if listenPort, ok := parseWireGuardInt(firstQueryValue(query, "listen_port", "listen-port")); ok {
		if listenPort < 0 || listenPort > 65535 {
			return nil, "", "", fmt.Errorf("invalid wireguard listen port")
		}
		cfg.ListenPort = listenPort
	}
	cfg.UDPTimeout = normalizeDurationString(firstQueryValue(query, "udp_timeout", "udp-timeout"), time.Second)
	if strings.TrimSpace(cfg.Network) == "" {
		if udp, ok := parseWireGuardBool(firstQueryValue(query, "udp")); ok {
			if *udp {
				cfg.Network = "udp"
			} else {
				cfg.Network = "tcp"
			}
		}
	}
	if strategy := firstQueryValue(query, "domain_resolver_strategy", "domain-resolver-strategy", "resolver_strategy"); strategy != "" {
		cfg.DomainResolverStrategy = strategy
	}
	if rawResolver := firstQueryValue(query, "domain_resolver_options", "domain-resolver-options"); rawResolver != "" {
		if err := json.Unmarshal([]byte(rawResolver), &cfg.DomainResolverOptions); err != nil {
			return nil, "", "", fmt.Errorf("invalid wireguard domain resolver options: %w", err)
		}
	}
	if keepalive := queryPositiveInt(query, "persistent_keepalive_interval", "persistent-keepalive-interval", "persistent-keepalive"); keepalive > 0 {
		if keepalive > 65535 {
			return nil, "", "", fmt.Errorf("invalid wireguard persistent keepalive interval")
		}
		cfg.Peers = []models.WireGuardPeerConfig{{
			Server:                      cfg.Server,
			ServerPort:                  cfg.ServerPort,
			PublicKey:                   cfg.PeerPublicKey,
			PreSharedKey:                cfg.PreSharedKey,
			AllowedIPs:                  cfg.AllowedIPs,
			Reserved:                    cfg.Reserved,
			PersistentKeepaliveInterval: keepalive,
		}}
	}

	return cfg, "wireguard", name, nil
}
