package services

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"sb-proxy/backend/models"
)

const (
	maxBatchImportItems       = 2000
	maxBatchImportFetches     = 20
	maxBatchImportExpandDepth = 3
	maxSubscriptionBytes      = 5 << 20 // 5MB
	subscriptionFetchTimeout  = 15 * time.Second
)

type ImportItem struct {
	Source string
	Link   string
	Type   string
	Name   string
	Config any
}

type ImportFailure struct {
	Source string
	Error  string
}

func ExpandBatchImportSources(ctx context.Context, sources []string) ([]ImportItem, []ImportFailure, error) {
	visited := make(map[string]struct{})
	fetchCount := 0

	var items []ImportItem
	var failures []ImportFailure

	for _, src := range sources {
		src = normalizeImportText(src)
		if src == "" {
			continue
		}

		expItems, expFailures, err := expandBatchImportInput(ctx, src, 0, visited, &fetchCount)
		if err != nil {
			failures = append(failures, ImportFailure{
				Source: summarizeSource(src),
				Error:  err.Error(),
			})
			continue
		}

		items = append(items, expItems...)
		failures = append(failures, expFailures...)
		if len(items)+len(failures) > maxBatchImportItems {
			return nil, nil, fmt.Errorf("too many nodes (>%d)", maxBatchImportItems)
		}
	}

	return items, failures, nil
}

func expandBatchImportInput(
	ctx context.Context,
	input string,
	depth int,
	visited map[string]struct{},
	fetchCount *int,
) ([]ImportItem, []ImportFailure, error) {
	if depth > maxBatchImportExpandDepth {
		return nil, nil, fmt.Errorf("subscription nesting too deep")
	}

	input = normalizeImportText(input)
	if input == "" {
		return nil, nil, nil
	}

	// 1) Clash Meta YAML
	yamlItems, yamlFailures, yamlOk, err := parseClashMetaYAML(input)
	if err != nil {
		return nil, nil, err
	}
	if yamlOk {
		return yamlItems, yamlFailures, nil
	}

	// 2) Base64 subscription (may include line breaks)
	if decoded, ok, err := decodeBase64Subscription(input); err != nil {
		return nil, nil, err
	} else if ok {
		return expandBatchImportInput(ctx, decoded, depth+1, visited, fetchCount)
	}

	// 3) Multi-line share links / subscription URLs
	lines := splitNonEmptyLines(input)
	if len(lines) > 1 {
		var items []ImportItem
		var failures []ImportFailure
		for _, line := range lines {
			subItems, subFailures, err := expandBatchImportInput(ctx, line, depth, visited, fetchCount)
			if err != nil {
				failures = append(failures, ImportFailure{
					Source: summarizeSource(line),
					Error:  err.Error(),
				})
				continue
			}
			items = append(items, subItems...)
			failures = append(failures, subFailures...)
			if len(items)+len(failures) > maxBatchImportItems {
				return nil, nil, fmt.Errorf("too many nodes (>%d)", maxBatchImportItems)
			}
		}
		return items, failures, nil
	}

	// 4) Single-line subscription URL or share link
	if isHTTPURL(input) {
		u, err := url.Parse(input)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid url: %v", err)
		}
		if isProbablyHTTPProxyShareLink(u) {
			return []ImportItem{{Source: input, Link: input}}, nil, nil
		}

		normalizedURL := u.String()
		if _, ok := visited[normalizedURL]; ok {
			return nil, nil, nil
		}
		if *fetchCount >= maxBatchImportFetches {
			return nil, nil, fmt.Errorf("too many subscription urls (>%d)", maxBatchImportFetches)
		}
		visited[normalizedURL] = struct{}{}
		*fetchCount++

		body, err := fetchSubscription(ctx, normalizedURL)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to fetch subscription: %v", err)
		}
		return expandBatchImportInput(ctx, body, depth+1, visited, fetchCount)
	}

	// Fallback: treat as one share link
	return []ImportItem{{Source: input, Link: input}}, nil, nil
}

func parseClashMetaYAML(input string) ([]ImportItem, []ImportFailure, bool, error) {
	type proxyProvider struct {
		Type    string           `yaml:"type"`
		Payload []map[string]any `yaml:"payload"`
	}
	type clashConfig struct {
		Proxies        []map[string]any         `yaml:"proxies"`
		ProxyProviders map[string]proxyProvider `yaml:"proxy-providers"`
	}

	var cfg clashConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		if looksLikeClashYAML(input) {
			return nil, nil, false, fmt.Errorf("invalid yaml: %v", err)
		}
		return nil, nil, false, nil
	}

	var proxies []map[string]any
	if len(cfg.Proxies) > 0 {
		proxies = append(proxies, cfg.Proxies...)
	}
	for _, p := range cfg.ProxyProviders {
		if strings.EqualFold(p.Type, "inline") && len(p.Payload) > 0 {
			proxies = append(proxies, p.Payload...)
		}
	}

	if len(proxies) == 0 {
		return nil, nil, false, nil
	}

	var items []ImportItem
	var failures []ImportFailure
	for _, proxy := range proxies {
		item, err := convertClashProxyToImportItem(proxy)
		if err != nil {
			failures = append(failures, ImportFailure{
				Source: summarizeSource(getString(proxy, "name")),
				Error:  err.Error(),
			})
			continue
		}
		items = append(items, item)
	}

	return items, failures, true, nil
}

func convertClashProxyToImportItem(proxy map[string]any) (ImportItem, error) {
	rawType := strings.ToLower(strings.TrimSpace(getString(proxy, "type")))
	name := getString(proxy, "name")
	if name == "" {
		name = "Imported Node"
	}
	server := getString(proxy, "server")
	port := getInt(proxy, "port")
	if server == "" || port <= 0 {
		return ImportItem{}, fmt.Errorf("missing server/port")
	}

	switch rawType {
	case "ss", "shadowsocks":
		cipher := getString(proxy, "cipher")
		if cipher == "" {
			cipher = getString(proxy, "method")
		}
		password := getString(proxy, "password")
		if cipher == "" || password == "" {
			return ImportItem{}, fmt.Errorf("missing cipher/password")
		}

		plugin := getString(proxy, "plugin")
		pluginOpts := ""
		if v, ok := proxy["plugin-opts"]; ok {
			pluginOpts = stringifyPluginOpts(plugin, v)
		} else if v, ok := proxy["plugin_opts"]; ok {
			pluginOpts = stringifyPluginOpts(plugin, v)
		}

		cfg := models.SSConfig{
			Server:     server,
			ServerPort: port,
			Method:     cipher,
			Password:   password,
			Plugin:     plugin,
			PluginOpts: pluginOpts,
		}
		if getBool(proxy, "udp-over-tcp") || getBool(proxy, "udp_over_tcp") {
			cfg.UDPOverTCP = true
		}

		return ImportItem{Source: "clash:" + name, Type: "ss", Name: name, Config: cfg}, nil

	case "vless":
		uuid := getString(proxy, "uuid")
		if uuid == "" {
			return ImportItem{}, fmt.Errorf("missing uuid")
		}

		cfg := models.VLESSConfig{
			Server:         server,
			ServerPort:     port,
			UUID:           uuid,
			Flow:           getString(proxy, "flow"),
			Encryption:     getString(proxy, "encryption"),
			Network:        getString(proxy, "network"),
			SNI:            firstNonEmpty(getString(proxy, "servername"), getString(proxy, "sni")),
			Fingerprint:    firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			PacketEncoding: firstNonEmpty(getString(proxy, "packet-encoding"), getString(proxy, "packet_encoding")),
		}

		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn[0]
		}

		if getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify") {
			cfg.Insecure = true
		}

		tlsEnabled := getBool(proxy, "tls")
		realityOpts, _ := getMap(proxy, "reality-opts", "reality_opts")
		if len(realityOpts) > 0 {
			cfg.Security = "reality"
			cfg.PublicKey = getString(realityOpts, "public-key", "public_key")
			cfg.ShortID = getString(realityOpts, "short-id", "short_id")
		} else if tlsEnabled {
			cfg.Security = "tls"
		}

		switch cfg.Network {
		case "ws":
			applyWSOpts(&cfg.Path, &cfg.Headers, &cfg.Host, proxy)
		case "grpc":
			cfg.ServiceName = getGRPCServiceName(proxy)
		}

		return ImportItem{Source: "clash:" + name, Type: "vless", Name: name, Config: cfg}, nil

	case "vmess":
		uuid := getString(proxy, "uuid")
		if uuid == "" {
			return ImportItem{}, fmt.Errorf("missing uuid")
		}

		cfg := models.VMESSConfig{
			Server:      server,
			ServerPort:  port,
			UUID:        uuid,
			AlterID:     getInt(proxy, "alterId"),
			Security:    firstNonEmpty(getString(proxy, "cipher"), getString(proxy, "security")),
			Network:     getString(proxy, "network"),
			SNI:         firstNonEmpty(getString(proxy, "servername"), getString(proxy, "sni")),
			Fingerprint: firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			Path:        getString(proxy, "path"),
			Host:        getString(proxy, "host"),
		}

		if getBool(proxy, "tls") {
			cfg.TLS = "tls"
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn[0]
		}
		if getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify") {
			cfg.Insecure = true
		}
		if getBool(proxy, "global-padding") || getBool(proxy, "global_padding") {
			cfg.GlobalPadding = true
		}
		if getBool(proxy, "authenticated-length") || getBool(proxy, "authenticated_length") {
			cfg.AuthenticatedLength = true
		}
		if pe := firstNonEmpty(getString(proxy, "packet-encoding"), getString(proxy, "packet_encoding")); pe != "" {
			cfg.PacketEncoding = pe
		}

		switch cfg.Network {
		case "ws":
			applyWSOpts(&cfg.Path, &cfg.Headers, &cfg.Host, proxy)
		case "grpc":
			cfg.ServiceName = getGRPCServiceName(proxy)
		case "http":
			if httpPath := getStringSliceFromNested(proxy, "http-opts", "path"); len(httpPath) > 0 {
				cfg.HTTPPath = httpPath
			}
			cfg.Method = getStringFromNested(proxy, "http-opts", "method")
		}

		return ImportItem{Source: "clash:" + name, Type: "vmess", Name: name, Config: cfg}, nil

	case "trojan":
		password := getString(proxy, "password")
		if password == "" {
			return ImportItem{}, fmt.Errorf("missing password")
		}

		cfg := models.TrojanConfig{
			Server:      server,
			ServerPort:  port,
			Password:    password,
			Network:     getString(proxy, "network"),
			SNI:         firstNonEmpty(getString(proxy, "sni"), getString(proxy, "servername")),
			Fingerprint: firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			Insecure:    getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn
		}

		switch cfg.Network {
		case "ws":
			applyWSOpts(&cfg.Path, &cfg.Headers, &cfg.Host, proxy)
		case "grpc":
			cfg.ServiceName = getGRPCServiceName(proxy)
		}

		return ImportItem{Source: "clash:" + name, Type: "trojan", Name: name, Config: cfg}, nil

	case "hysteria2", "hy2":
		password := getString(proxy, "password")
		if password == "" {
			return ImportItem{}, fmt.Errorf("missing password")
		}

		cfg := models.Hysteria2Config{
			Server:             server,
			ServerPort:         port,
			Password:           password,
			UpMbps:             parseBandwidthMbps(proxy["up"]),
			DownMbps:           parseBandwidthMbps(proxy["down"]),
			Obfs:               getString(proxy, "obfs"),
			ObfsPassword:       firstNonEmpty(getString(proxy, "obfs-password"), getString(proxy, "obfs_password")),
			SNI:                getString(proxy, "sni"),
			Fingerprint:        firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			InsecureSkipVerify: getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
			Network:            getString(proxy, "network"),
			HopInterval:        secondsToDurationString(proxy["hop-interval"]),
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn
		}

		return ImportItem{Source: "clash:" + name, Type: "hy2", Name: name, Config: cfg}, nil

	case "tuic":
		uuid := getString(proxy, "uuid")
		password := getString(proxy, "password")
		if uuid == "" || password == "" {
			if getString(proxy, "token") != "" {
				return ImportItem{}, fmt.Errorf("tuic token (v4) is not supported; uuid/password required (v5)")
			}
			return ImportItem{}, fmt.Errorf("missing uuid/password")
		}

		cfg := models.TUICConfig{
			Server:             server,
			ServerPort:         port,
			UUID:               uuid,
			Password:           password,
			CongestionControl:  firstNonEmpty(getString(proxy, "congestion-controller"), getString(proxy, "congestion_controller")),
			UDPRelayMode:       firstNonEmpty(getString(proxy, "udp-relay-mode"), getString(proxy, "udp_relay_mode")),
			SNI:                getString(proxy, "sni"),
			Fingerprint:        firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			InsecureSkipVerify: getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
			DisableSNI:         getBool(proxy, "disable-sni") || getBool(proxy, "disable_sni"),
			ReduceRTT:          getBool(proxy, "reduce-rtt") || getBool(proxy, "reduce_rtt"),
			ZeroRTTHandshake:   getBool(proxy, "reduce-rtt") || getBool(proxy, "zero-rtt-handshake") || getBool(proxy, "zero_rtt_handshake"),
			Heartbeat:          millisecondsToDurationString(proxy["heartbeat-interval"]),
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn
		}

		return ImportItem{Source: "clash:" + name, Type: "tuic", Name: name, Config: cfg}, nil

	case "anytls":
		password := getString(proxy, "password")
		if password == "" {
			return ImportItem{}, fmt.Errorf("missing password")
		}

		cfg := models.AnyTLSConfig{
			Server:                   server,
			ServerPort:               port,
			Password:                 password,
			SNI:                      firstNonEmpty(getString(proxy, "sni"), getString(proxy, "servername")),
			Fingerprint:              firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			Insecure:                 getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
			IdleSessionCheckInterval: secondsToDurationString(proxy["idle-session-check-interval"]),
			IdleSessionTimeout:       secondsToDurationString(proxy["idle-session-timeout"]),
			MinIdleSession:           getInt(proxy, "min-idle-session"),
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn
		}

		return ImportItem{Source: "clash:" + name, Type: "anytls", Name: name, Config: cfg}, nil

	case "wireguard", "wg":
		privateKey := firstNonEmpty(
			getString(proxy, "private-key"),
			getString(proxy, "private_key"),
			getString(proxy, "secret-key"),
			getString(proxy, "secret_key"),
		)
		if privateKey == "" {
			return ImportItem{}, fmt.Errorf("missing private-key")
		}

		localAddresses := normalizeWireGuardAddresses(append(
			append(
				append([]string{}, getStringSlice(proxy, "ip")...),
				getStringSlice(proxy, "ipv6")...,
			),
			append(getStringSlice(proxy, "address"), getStringSlice(proxy, "local-address")...)...,
		))
		if len(localAddresses) == 0 {
			return ImportItem{}, fmt.Errorf("missing local address")
		}

		cfg := models.WireGuardConfig{
			Server:          server,
			ServerPort:      port,
			LocalAddress:    localAddresses,
			PrivateKey:      privateKey,
			PeerPublicKey:   firstNonEmpty(getString(proxy, "public-key"), getString(proxy, "public_key")),
			PreSharedKey:    firstNonEmpty(getString(proxy, "pre-shared-key"), getString(proxy, "pre_shared_key")),
			AllowedIPs:      firstNonEmptyStringSlice(getStringSlice(proxy, "allowed-ips"), getStringSlice(proxy, "allowed_ips")),
			InterfaceName:   firstNonEmpty(getString(proxy, "interface-name"), getString(proxy, "interface_name"), getString(proxy, "name")),
			Network:         getString(proxy, "network"),
			Detour:          firstNonEmpty(getString(proxy, "dialer-proxy"), getString(proxy, "dialer_proxy")),
			ConnectTimeout:  secondsToDurationString(proxy["connect-timeout"]),
			RoutingMark:     getString(proxy, "routing-mark"),
			SystemInterface: getBool(proxy, "system-interface") || getBool(proxy, "system_interface"),
		}

		if reserved, err := parseWireGuardReservedAny(proxy["reserved"]); err != nil {
			return ImportItem{}, err
		} else {
			cfg.Reserved = reserved
		}
		if mtu := getInt(proxy, "mtu"); mtu > 0 {
			cfg.MTU = mtu
		}
		if workers := getInt(proxy, "workers"); workers > 0 {
			cfg.Workers = workers
		}
		if strings.TrimSpace(cfg.Network) == "" && getBool(proxy, "udp") {
			cfg.Network = "udp"
		}
		if udpFragment := getBool(proxy, "udp-fragment") || getBool(proxy, "udp_fragment"); udpFragment {
			cfg.UDPFragment = &udpFragment
		}

		if rawPeers, ok := proxy["peers"].([]any); ok && len(rawPeers) > 0 {
			peers := make([]models.WireGuardPeerConfig, 0, len(rawPeers))
			for _, rawPeer := range rawPeers {
				peerMap, ok := rawPeer.(map[string]any)
				if !ok {
					continue
				}
				peerReserved, err := parseWireGuardReservedAny(peerMap["reserved"])
				if err != nil {
					return ImportItem{}, err
				}
				peer := models.WireGuardPeerConfig{
					Server:       firstNonEmpty(getString(peerMap, "server"), getString(peerMap, "address")),
					ServerPort:   getInt(peerMap, "port"),
					PublicKey:    firstNonEmpty(getString(peerMap, "public-key"), getString(peerMap, "public_key")),
					PreSharedKey: firstNonEmpty(getString(peerMap, "pre-shared-key"), getString(peerMap, "pre_shared_key")),
					AllowedIPs:   firstNonEmptyStringSlice(getStringSlice(peerMap, "allowed-ips"), getStringSlice(peerMap, "allowed_ips")),
					Reserved:     peerReserved,
				}
				peers = append(peers, peer)
			}

			switch len(peers) {
			case 0:
				// ignore malformed empty peers block
			case 1:
				peer := peers[0]
				if cfg.Server == "" {
					cfg.Server = peer.Server
				}
				if cfg.ServerPort <= 0 {
					cfg.ServerPort = peer.ServerPort
				}
				if cfg.PeerPublicKey == "" {
					cfg.PeerPublicKey = peer.PublicKey
				}
				if cfg.PreSharedKey == "" {
					cfg.PreSharedKey = peer.PreSharedKey
				}
				if len(cfg.AllowedIPs) == 0 {
					cfg.AllowedIPs = peer.AllowedIPs
				}
				if len(cfg.Reserved) == 0 {
					cfg.Reserved = peer.Reserved
				}
			default:
				cfg.Peers = peers
			}
		}

		if len(cfg.Peers) == 0 {
			if cfg.Server == "" || cfg.ServerPort <= 0 || cfg.PeerPublicKey == "" {
				return ImportItem{}, fmt.Errorf("missing server/port/public-key")
			}
		}

		return ImportItem{Source: "clash:" + name, Type: "wireguard", Name: name, Config: cfg}, nil

	case "socks5", "socks":
		cfg := models.SOCKS5Config{
			Server:     server,
			ServerPort: port,
			Username:   getString(proxy, "username"),
			Password:   getString(proxy, "password"),
		}
		return ImportItem{Source: "clash:" + name, Type: "socks5", Name: name, Config: cfg}, nil

	case "http", "https":
		cfg := models.HTTPProxyConfig{
			Server:     server,
			ServerPort: port,
			Username:   getString(proxy, "username"),
			Password:   getString(proxy, "password"),
			TLS:        rawType == "https" || getBool(proxy, "tls"),
			SNI:        getString(proxy, "sni"),
			Insecure:   getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
		}
		return ImportItem{Source: "clash:" + name, Type: "http", Name: name, Config: cfg}, nil

	default:
		return ImportItem{}, fmt.Errorf("unsupported proxy type: %s", rawType)
	}
}

func fetchSubscription(ctx context.Context, rawURL string) (string, error) {
	client := &http.Client{Timeout: subscriptionFetchTimeout}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "sb-proxy-manager/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status: %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBytes))
	if err != nil {
		return "", err
	}

	return normalizeImportText(string(body)), nil
}

func decodeBase64Subscription(input string) (string, bool, error) {
	compact := strings.Join(strings.Fields(input), "")
	if compact == "" {
		return "", false, nil
	}
	if strings.Contains(compact, "://") {
		return "", false, nil
	}
	if len(compact) < 64 {
		return "", false, nil
	}
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' || c == '-' || c == '_' {
			continue
		}
		return "", false, nil
	}

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(compact)
		if err != nil {
			continue
		}
		if !utf8.Valid(decoded) {
			continue
		}
		out := normalizeImportText(string(decoded))
		if out == "" {
			continue
		}
		lower := strings.ToLower(out)
		if strings.Contains(lower, "://") || strings.Contains(lower, "proxies:") || strings.Contains(lower, "proxy-groups:") || strings.Contains(lower, "proxy-providers:") {
			return out, true, nil
		}
	}

	// Looks like base64 but decoding didn't yield recognizable text; treat as error to help users.
	if looksLikeBase64(compact) {
		return "", false, fmt.Errorf("invalid base64 subscription")
	}
	return "", false, nil
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func isProbablyHTTPProxyShareLink(u *url.URL) bool {
	if u == nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Hostname() == "" || u.Port() == "" {
		return false
	}
	if u.Path != "" && u.Path != "/" {
		return false
	}
	allowedKeys := map[string]struct{}{
		"sni":           {},
		"insecure":      {},
		"allowInsecure": {},
	}
	for k := range u.Query() {
		if _, ok := allowedKeys[k]; !ok {
			return false
		}
	}
	return true
}

func splitNonEmptyLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = normalizeImportText(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func normalizeImportText(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "\uFEFF")
	return s
}

func summarizeSource(s string) string {
	s = normalizeImportText(s)
	if s == "" {
		return ""
	}
	if len([]rune(s)) <= 64 {
		return s
	}
	r := []rune(s)
	return string(r[:61]) + "..."
}

func looksLikeClashYAML(input string) bool {
	lower := strings.ToLower(input)
	return strings.Contains(lower, "proxies:") || strings.Contains(lower, "proxy-groups:") || strings.Contains(lower, "proxy-providers:")
}

func looksLikeBase64(compact string) bool {
	if len(compact) < 64 {
		return false
	}
	for i := 0; i < len(compact); i++ {
		c := compact[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' || c == '-' || c == '_' {
			continue
		}
		return false
	}
	return true
}

func getString(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch vv := v.(type) {
			case string:
				return strings.TrimSpace(vv)
			case fmt.Stringer:
				return strings.TrimSpace(vv.String())
			}
		}
	}
	return ""
}

func getInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch vv := v.(type) {
	case int:
		return vv
	case int64:
		return int(vv)
	case float64:
		return int(vv)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(vv))
		return i
	default:
		return 0
	}
}

func getBool(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch vv := v.(type) {
	case bool:
		return vv
	case int:
		return vv != 0
	case int64:
		return vv != 0
	case float64:
		return vv != 0
	case string:
		s := strings.ToLower(strings.TrimSpace(vv))
		return s == "1" || s == "true" || s == "yes" || s == "on"
	default:
		return false
	}
}

func getStringSlice(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch vv := v.(type) {
	case string:
		parts := strings.FieldsFunc(vv, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n' || r == '\r'
		})
		out := make([]string, 0, len(parts))
		for _, item := range parts {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	case fmt.Stringer:
		return getStringSlice(map[string]any{key: vv.String()}, key)
	case []any:
		out := make([]string, 0, len(vv))
		for _, item := range vv {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(vv))
		for _, s := range vv {
			s = strings.TrimSpace(s)
			if s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func getMap(m map[string]any, keys ...string) (map[string]any, bool) {
	if m == nil {
		return nil, false
	}
	for _, key := range keys {
		v, ok := m[key]
		if !ok || v == nil {
			continue
		}
		if mm, ok := v.(map[string]any); ok {
			return mm, true
		}
		if mm, ok := v.(map[any]any); ok {
			out := make(map[string]any, len(mm))
			for k, val := range mm {
				ks, ok := k.(string)
				if !ok {
					continue
				}
				out[ks] = val
			}
			return out, true
		}
	}
	return nil, false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstNonEmptyStringSlice(values ...[]string) []string {
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		out := make([]string, 0, len(value))
		for _, item := range value {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func stringifyPluginOpts(plugin string, v any) string {
	switch vv := v.(type) {
	case string:
		return strings.TrimSpace(vv)
	case map[string]any:
		return pluginOptsFromMap(plugin, vv)
	case map[any]any:
		m := make(map[string]any, len(vv))
		for k, val := range vv {
			if ks, ok := k.(string); ok {
				m[ks] = val
			}
		}
		return pluginOptsFromMap(plugin, m)
	default:
		return ""
	}
}

func pluginOptsFromMap(plugin string, m map[string]any) string {
	plugin = strings.ToLower(strings.TrimSpace(plugin))
	if plugin == "obfs" || plugin == "simple-obfs" {
		mode := firstNonEmpty(getString(m, "mode"), getString(m, "obfs"))
		host := firstNonEmpty(getString(m, "host"), getString(m, "hostname"))
		parts := []string{}
		if mode != "" {
			parts = append(parts, "obfs="+mode)
		}
		if host != "" {
			parts = append(parts, "obfs-host="+host)
		}
		return strings.Join(parts, ";")
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		val := m[k]
		switch v := val.(type) {
		case string:
			parts = append(parts, k+"="+v)
		case bool:
			parts = append(parts, k+"="+strconv.FormatBool(v))
		case int:
			parts = append(parts, k+"="+strconv.Itoa(v))
		case int64:
			parts = append(parts, k+"="+strconv.FormatInt(v, 10))
		case float64:
			parts = append(parts, k+"="+strconv.FormatFloat(v, 'f', -1, 64))
		default:
			parts = append(parts, k+"="+fmt.Sprint(v))
		}
	}
	return strings.Join(parts, ";")
}

func applyWSOpts(path *string, headers *map[string]string, host *string, proxy map[string]any) {
	wsOpts, ok := getMap(proxy, "ws-opts", "ws_opts")
	if !ok {
		return
	}
	if p := getString(wsOpts, "path"); p != "" {
		*path = p
	}
	hdrs, ok := getMap(wsOpts, "headers")
	if ok && len(hdrs) > 0 {
		out := make(map[string]string, len(hdrs))
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				out[k] = s
			}
		}
		if len(out) > 0 {
			*headers = out
		}
		if hv := firstNonEmpty(out["Host"], out["host"]); hv != "" {
			*host = hv
		}
	}
}

func getGRPCServiceName(proxy map[string]any) string {
	grpcOpts, ok := getMap(proxy, "grpc-opts", "grpc_opts")
	if !ok {
		return ""
	}
	return firstNonEmpty(getString(grpcOpts, "grpc-service-name"), getString(grpcOpts, "serviceName"), getString(grpcOpts, "service_name"))
}

func getStringFromNested(m map[string]any, nestedKey string, key string) string {
	nested, ok := getMap(m, nestedKey)
	if !ok {
		return ""
	}
	return getString(nested, key)
}

func getStringSliceFromNested(m map[string]any, nestedKey string, key string) []string {
	nested, ok := getMap(m, nestedKey)
	if !ok {
		return nil
	}
	if v, ok := nested[key]; ok {
		switch vv := v.(type) {
		case []any:
			out := make([]string, 0, len(vv))
			for _, item := range vv {
				if s, ok := item.(string); ok {
					s = strings.TrimSpace(s)
					if s != "" {
						out = append(out, s)
					}
				}
			}
			return out
		case []string:
			out := make([]string, 0, len(vv))
			for _, s := range vv {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
			return out
		case string:
			s := strings.TrimSpace(vv)
			if s == "" {
				return nil
			}
			return []string{s}
		}
	}
	return nil
}

var bandwidthRe = regexp.MustCompile(`(?i)^\\s*([0-9]+(?:\\.[0-9]+)?)\\s*([a-z]+)?`)

func parseBandwidthMbps(v any) int {
	if v == nil {
		return 0
	}
	switch vv := v.(type) {
	case int:
		return vv
	case int64:
		return int(vv)
	case float64:
		return int(vv)
	case string:
		s := strings.TrimSpace(vv)
		if s == "" {
			return 0
		}
		m := bandwidthRe.FindStringSubmatch(s)
		if len(m) < 2 {
			return 0
		}
		num, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0
		}
		unit := ""
		if len(m) >= 3 {
			unit = strings.ToLower(m[2])
		}
		switch {
		case strings.Contains(unit, "gbps") || strings.Contains(unit, "g"):
			num *= 1000
		case strings.Contains(unit, "kbps") || strings.Contains(unit, "k"):
			num /= 1000
		}
		if num < 0 {
			return 0
		}
		return int(num + 0.5)
	default:
		return 0
	}
}

func secondsToDurationString(v any) string {
	if v == nil {
		return ""
	}
	switch vv := v.(type) {
	case int:
		if vv <= 0 {
			return ""
		}
		return fmt.Sprintf("%ds", vv)
	case int64:
		if vv <= 0 {
			return ""
		}
		return fmt.Sprintf("%ds", vv)
	case float64:
		if vv <= 0 {
			return ""
		}
		return fmt.Sprintf("%ds", int(vv))
	case string:
		s := strings.TrimSpace(vv)
		if s == "" {
			return ""
		}
		if strings.HasSuffix(s, "s") || strings.HasSuffix(s, "ms") || strings.HasSuffix(s, "m") || strings.HasSuffix(s, "h") {
			return s
		}
		if i, err := strconv.Atoi(s); err == nil && i > 0 {
			return fmt.Sprintf("%ds", i)
		}
		return s
	default:
		return ""
	}
}

func millisecondsToDurationString(v any) string {
	if v == nil {
		return ""
	}
	switch vv := v.(type) {
	case int:
		if vv <= 0 {
			return ""
		}
		return (time.Duration(vv) * time.Millisecond).String()
	case int64:
		if vv <= 0 {
			return ""
		}
		return (time.Duration(vv) * time.Millisecond).String()
	case float64:
		if vv <= 0 {
			return ""
		}
		return (time.Duration(int(vv)) * time.Millisecond).String()
	case string:
		s := strings.TrimSpace(vv)
		if s == "" {
			return ""
		}
		if i, err := strconv.Atoi(s); err == nil && i > 0 {
			return (time.Duration(i) * time.Millisecond).String()
		}
		return s
	default:
		return ""
	}
}
