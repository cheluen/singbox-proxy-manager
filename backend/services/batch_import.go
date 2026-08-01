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
		if hasHTTPProxyMarker(u) {
			return []ImportItem{{Source: input, Link: input}}, nil, nil
		}
		proxyCandidate := isProbablyHTTPProxyShareLink(u)
		explicitPort := u.Port() != ""
		fallbackToProxy := explicitPort || proxyCandidate

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
			if fallbackToProxy {
				return []ImportItem{{Source: input, Link: input}}, nil, nil
			}
			return nil, nil, fmt.Errorf("failed to fetch subscription: %v", err)
		}
		if fallbackToProxy && !looksLikeImportPayload(body) {
			return []ImportItem{{Source: input, Link: input}}, nil, nil
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
		Payload        []map[string]any         `yaml:"payload"`
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
	if len(cfg.Payload) > 0 {
		proxies = append(proxies, cfg.Payload...)
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
	if (rawType == "hysteria2" || rawType == "hy2") && port <= 0 {
		if firstPort, _, err := parseClashHysteria2Ports(proxy); err != nil {
			return ImportItem{}, err
		} else if firstPort > 0 {
			port = firstPort
		}
	}
	if rawType != "wireguard" && rawType != "wg" && (server == "" || port <= 0) {
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

		plugin := normalizeShadowsocksPlugin(getString(proxy, "plugin"))
		if err := validateShadowsocksPlugin(plugin); err != nil {
			return ImportItem{}, err
		}
		pluginOpts := ""
		if v, ok := proxy["plugin-opts"]; ok {
			var err error
			pluginOpts, err = stringifyPluginOpts(plugin, v)
			if err != nil {
				return ImportItem{}, err
			}
		} else if v, ok := proxy["plugin_opts"]; ok {
			var err error
			pluginOpts, err = stringifyPluginOpts(plugin, v)
			if err != nil {
				return ImportItem{}, err
			}
		}

		cfg := models.SSConfig{
			Server:     server,
			ServerPort: port,
			Method:     cipher,
			Password:   password,
			Plugin:     plugin,
			PluginOpts: pluginOpts,
		}
		network, err := normalizeNetworkList(getString(proxy, "network"))
		if err != nil {
			return ImportItem{}, err
		}
		cfg.Network = models.ListableString(network)
		if getBool(proxy, "udp-over-tcp") || getBool(proxy, "udp_over_tcp") {
			cfg.UDPOverTCP = true
			if version := firstPositiveInt(getInt(proxy, "udp-over-tcp-version"), getInt(proxy, "udp_over_tcp_version")); version > 0 {
				if version > 2 {
					return ImportItem{}, fmt.Errorf("unsupported udp-over-tcp version: %d", version)
				}
				cfg.UDPOverTCPOptions = models.NativeOptions{"enabled": true, "version": version}
				cfg.UDPOverTCP = cfg.UDPOverTCPOptions
			}
		}
		if cfg.MultiplexConfig, err = buildClashMultiplex(proxy); err != nil {
			return ImportItem{}, err
		}
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
		}

		return ImportItem{Source: "clash:" + name, Type: "ss", Name: name, Config: cfg}, nil

	case "vless":
		uuid := getString(proxy, "uuid")
		if uuid == "" {
			return ImportItem{}, fmt.Errorf("missing uuid")
		}

		transportType, transportOptions, err := buildClashTransportOptions(proxy)
		if err != nil {
			return ImportItem{}, err
		}
		packetEncoding, err := normalizePacketEncoding(firstNonEmpty(getString(proxy, "packet-encoding"), getString(proxy, "packet_encoding")))
		if err != nil {
			return ImportItem{}, err
		}
		encryption := getString(proxy, "encryption")
		if encryption != "" && !strings.EqualFold(encryption, "none") {
			return ImportItem{}, fmt.Errorf("vless encryption %q is not supported by sing-box 1.12.12", encryption)
		}
		cfg := models.VLESSConfig{
			Server:           server,
			ServerPort:       port,
			UUID:             uuid,
			Flow:             getString(proxy, "flow"),
			Encryption:       encryption,
			Network:          transportType,
			SNI:              firstNonEmpty(getString(proxy, "servername"), getString(proxy, "sni")),
			Fingerprint:      firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			PacketEncoding:   packetEncoding,
			TransportOptions: transportOptions,
		}

		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = strings.Join(alpn, ",")
		}
		outboundNetwork, err := normalizeNetworkList(firstNonEmpty(getString(proxy, "outbound-network"), getString(proxy, "outbound_network")))
		if err != nil {
			return ImportItem{}, err
		}
		cfg.OutboundNetwork = models.ListableString(outboundNetwork)

		if getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify") {
			cfg.Insecure = true
		}

		tlsEnabled := getBool(proxy, "tls")
		realityOpts, _ := getMap(proxy, "reality-opts", "reality_opts")
		if spiderX := firstNonEmpty(getString(realityOpts, "spider-x"), getString(realityOpts, "spider_x")); spiderX != "" {
			return ImportItem{}, fmt.Errorf("vless reality spider_x is not supported by sing-box 1.12.12")
		}
		if len(realityOpts) > 0 {
			cfg.Security = "reality"
			cfg.PublicKey = getString(realityOpts, "public-key", "public_key")
			cfg.ShortID = getString(realityOpts, "short-id", "short_id")
		} else if tlsEnabled {
			cfg.Security = "tls"
		}
		cfg.TLSOptions, err = buildClashTLSOptions(proxy, tlsEnabled || len(realityOpts) > 0, false)
		if err != nil {
			return ImportItem{}, err
		}

		switch cfg.Network {
		case "ws":
			applyWSOpts(&cfg.Path, &cfg.Headers, &cfg.Host, proxy)
		case "grpc":
			cfg.ServiceName = getGRPCServiceName(proxy)
		case "http":
			cfg.Path = nativeString(transportOptions["path"])
			if hosts := nativeStringSlice(transportOptions["host"]); len(hosts) > 0 {
				cfg.Host = strings.Join(hosts, ",")
			}
		case "httpupgrade":
			cfg.HTTPUpgradePath = nativeString(transportOptions["path"])
			cfg.HTTPUpgradeHost = nativeString(transportOptions["host"])
		}
		if cfg.MultiplexConfig, err = buildClashMultiplex(proxy); err != nil {
			return ImportItem{}, err
		}
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
		}

		return ImportItem{Source: "clash:" + name, Type: "vless", Name: name, Config: cfg}, nil

	case "vmess":
		uuid := getString(proxy, "uuid")
		if uuid == "" {
			return ImportItem{}, fmt.Errorf("missing uuid")
		}

		transportType, transportOptions, err := buildClashTransportOptions(proxy)
		if err != nil {
			return ImportItem{}, err
		}
		packetEncoding, err := normalizePacketEncoding(firstNonEmpty(getString(proxy, "packet-encoding"), getString(proxy, "packet_encoding")))
		if err != nil {
			return ImportItem{}, err
		}
		cipher, err := normalizeVMessCipher(firstNonEmpty(getString(proxy, "cipher"), getString(proxy, "security")))
		if err != nil {
			return ImportItem{}, err
		}
		cfg := models.VMESSConfig{
			Server:           server,
			ServerPort:       port,
			UUID:             uuid,
			AlterID:          firstPositiveInt(getInt(proxy, "alterId"), getInt(proxy, "alter-id"), getInt(proxy, "alter_id")),
			Security:         cipher,
			Network:          transportType,
			SNI:              firstNonEmpty(getString(proxy, "servername"), getString(proxy, "sni")),
			Fingerprint:      firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			Path:             getString(proxy, "path"),
			Host:             getString(proxy, "host"),
			PacketEncoding:   packetEncoding,
			TransportOptions: transportOptions,
		}

		realityOpts, _ := getMap(proxy, "reality-opts", "reality_opts")
		if len(realityOpts) > 0 {
			cfg.TLS = "reality"
		} else if getBool(proxy, "tls") {
			cfg.TLS = "tls"
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = strings.Join(alpn, ",")
		}
		outboundNetwork, err := normalizeNetworkList(firstNonEmpty(getString(proxy, "outbound-network"), getString(proxy, "outbound_network")))
		if err != nil {
			return ImportItem{}, err
		}
		cfg.OutboundNetwork = models.ListableString(outboundNetwork)
		if getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify") {
			cfg.Insecure = true
		}
		if getBool(proxy, "global-padding") || getBool(proxy, "global_padding") {
			cfg.GlobalPadding = true
		}
		if getBool(proxy, "authenticated-length") || getBool(proxy, "authenticated_length") {
			cfg.AuthenticatedLength = true
		}
		cfg.TLSOptions, err = buildClashTLSOptions(proxy, getBool(proxy, "tls") || hasNonEmptyMap(proxy, "reality-opts", "reality_opts"), false)
		if err != nil {
			return ImportItem{}, err
		}

		switch cfg.Network {
		case "ws":
			applyWSOpts(&cfg.Path, &cfg.Headers, &cfg.Host, proxy)
		case "grpc":
			cfg.ServiceName = getGRPCServiceName(proxy)
		case "http":
			if path := nativeString(transportOptions["path"]); path != "" {
				cfg.Path = path
				cfg.HTTPPath = []string{path}
			}
			cfg.Method = nativeString(transportOptions["method"])
			if hosts := nativeStringSlice(transportOptions["host"]); len(hosts) > 0 {
				cfg.Host = strings.Join(hosts, ",")
			}
		case "httpupgrade":
			cfg.HTTPUpgradePath = nativeString(transportOptions["path"])
			cfg.HTTPUpgradeHost = nativeString(transportOptions["host"])
		}
		if cfg.MultiplexConfig, err = buildClashMultiplex(proxy); err != nil {
			return ImportItem{}, err
		}
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
		}

		return ImportItem{Source: "clash:" + name, Type: "vmess", Name: name, Config: cfg}, nil

	case "trojan":
		password := getString(proxy, "password")
		if password == "" {
			return ImportItem{}, fmt.Errorf("missing password")
		}

		transportType, transportOptions, err := buildClashTransportOptions(proxy)
		if err != nil {
			return ImportItem{}, err
		}
		cfg := models.TrojanConfig{
			Server:           server,
			ServerPort:       port,
			Password:         password,
			Network:          transportType,
			SNI:              firstNonEmpty(getString(proxy, "sni"), getString(proxy, "servername")),
			Fingerprint:      firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			Insecure:         getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
			TransportOptions: transportOptions,
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn
		}
		outboundNetwork, err := normalizeNetworkList(firstNonEmpty(getString(proxy, "outbound-network"), getString(proxy, "outbound_network")))
		if err != nil {
			return ImportItem{}, err
		}
		cfg.OutboundNetwork = models.ListableString(outboundNetwork)
		realityOpts, _ := getMap(proxy, "reality-opts", "reality_opts")
		switch {
		case len(realityOpts) > 0:
			cfg.Security = "reality"
		case clashBoolExplicitlyFalse(proxy, "tls"):
			cfg.Security = "none"
		default:
			cfg.Security = "tls"
		}

		switch cfg.Network {
		case "ws":
			applyWSOpts(&cfg.Path, &cfg.Headers, &cfg.Host, proxy)
		case "grpc":
			cfg.ServiceName = getGRPCServiceName(proxy)
		case "http":
			cfg.Path = nativeString(transportOptions["path"])
			cfg.HTTPMethod = nativeString(transportOptions["method"])
			if hosts := nativeStringSlice(transportOptions["host"]); len(hosts) > 0 {
				cfg.Host = strings.Join(hosts, ",")
			}
		case "httpupgrade":
			cfg.Path = nativeString(transportOptions["path"])
			cfg.Host = nativeString(transportOptions["host"])
		}
		cfg.TLSOptions, err = buildClashTLSOptions(proxy, cfg.Security != "none", false)
		if err != nil {
			return ImportItem{}, err
		}
		if cfg.MultiplexConfig, err = buildClashMultiplex(proxy); err != nil {
			return ImportItem{}, err
		}
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
		}

		return ImportItem{Source: "clash:" + name, Type: "trojan", Name: name, Config: cfg}, nil

	case "hysteria2", "hy2":
		password := getString(proxy, "password")
		if password == "" {
			return ImportItem{}, fmt.Errorf("missing password")
		}

		network, err := normalizeNetworkList(getString(proxy, "network"))
		if err != nil {
			return ImportItem{}, err
		}
		firstPort, serverPorts, err := parseClashHysteria2Ports(proxy)
		if err != nil {
			return ImportItem{}, err
		}
		if firstPort > 0 {
			port = firstPort
		}
		obfsType := strings.ToLower(getString(proxy, "obfs"))
		if obfsType == "none" {
			obfsType = ""
		}
		if obfsType != "" && obfsType != "salamander" {
			return ImportItem{}, fmt.Errorf("unsupported hysteria2 obfs for sing-box 1.12.12: %s", obfsType)
		}
		obfsPassword := firstNonEmpty(getString(proxy, "obfs-password"), getString(proxy, "obfs_password"))
		cfg := models.Hysteria2Config{
			Server:             server,
			ServerPort:         port,
			ServerPorts:        models.ListableString(serverPorts),
			Password:           password,
			UpMbps:             parseBandwidthMbps(proxy["up"]),
			DownMbps:           parseBandwidthMbps(proxy["down"]),
			ObfsPassword:       obfsPassword,
			SNI:                getString(proxy, "sni"),
			Fingerprint:        firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint")),
			InsecureSkipVerify: getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
			Network:            models.ListableString(network),
			HopInterval:        secondsToDurationString(proxy["hop-interval"]),
			BrutalDebug:        getBool(proxy, "brutal-debug") || getBool(proxy, "brutal_debug"),
		}
		if obfsType != "" {
			cfg.Obfs = models.NativeOptions{"type": obfsType, "password": obfsPassword}
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn
		}
		tlsOptions, tlsErr := buildClashTLSOptions(proxy, true, false)
		if tlsErr != nil {
			return ImportItem{}, tlsErr
		}
		cfg.TLSOptions = tlsOptions
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
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

		network, err := normalizeNetworkList(getString(proxy, "network"))
		if err != nil {
			return ImportItem{}, err
		}
		disableSNI := getBool(proxy, "disable-sni") || getBool(proxy, "disable_sni")
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
			ZeroRTTHandshake:   getBool(proxy, "reduce-rtt") || getBool(proxy, "zero-rtt-handshake") || getBool(proxy, "zero_rtt_handshake"),
			UDPOverStream:      getBool(proxy, "udp-over-stream") || getBool(proxy, "udp_over_stream"),
			Heartbeat:          millisecondsToDurationString(firstMapValueOrNil(proxy, "heartbeat-interval", "heartbeat_interval")),
			Network:            models.ListableString(network),
		}
		if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
			cfg.ALPN = alpn
		}
		cfg.TLSOptions, err = buildClashTLSOptions(proxy, true, disableSNI)
		if err != nil {
			return ImportItem{}, err
		}
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
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
		tlsOptions, tlsErr := buildClashTLSOptions(proxy, true, false)
		if tlsErr != nil {
			return ImportItem{}, tlsErr
		}
		cfg.TLSOptions = tlsOptions
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
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
			InterfaceName:   firstNonEmpty(getString(proxy, "wireguard-interface-name"), getString(proxy, "wireguard_interface_name")),
			Network:         getString(proxy, "network"),
			Detour:          firstNonEmpty(getString(proxy, "dialer-proxy"), getString(proxy, "dialer_proxy")),
			ConnectTimeout:  secondsToDurationString(firstMapValueOrNil(proxy, "connect-timeout", "connect_timeout")),
			SystemInterface: getBool(proxy, "system-interface") || getBool(proxy, "system_interface"),
		}
		if raw, ok := firstMapValue(proxy, "routing-mark", "routing_mark"); ok {
			if number := intFromAny(raw); number != 0 {
				cfg.RoutingMark = number
			} else {
				cfg.RoutingMark = parseWireGuardRoutingMark(fmt.Sprint(raw))
			}
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
		if listenPort := firstPositiveInt(getInt(proxy, "listen-port"), getInt(proxy, "listen_port")); listenPort > 0 {
			if listenPort > 65535 {
				return ImportItem{}, fmt.Errorf("invalid wireguard listen port")
			}
			cfg.ListenPort = listenPort
		}
		cfg.UDPTimeout = secondsToDurationString(firstMapValueOrNil(proxy, "udp-timeout", "udp_timeout"))
		var dialer models.DialerOptions
		if err := applyClashDialerOptions(&dialer, proxy); err != nil {
			return ImportItem{}, err
		}
		cfg.Detour = dialer.Detour
		cfg.BindInterface = dialer.BindInterface
		cfg.Inet4BindAddress = dialer.Inet4BindAddress
		cfg.Inet6BindAddress = dialer.Inet6BindAddress
		cfg.ProtectPath = dialer.ProtectPath
		cfg.ReuseAddr = dialer.ReuseAddr
		cfg.NetNS = dialer.NetNS
		cfg.TCPFastOpen = dialer.TCPFastOpen
		cfg.TCPMultiPath = dialer.TCPMultiPath
		if dialer.UDPFragment != nil {
			cfg.UDPFragment = dialer.UDPFragment
		}
		cfg.NetworkStrategy = dialer.NetworkStrategy
		cfg.NetworkType = dialer.NetworkType
		cfg.FallbackNetworkType = dialer.FallbackNetworkType
		cfg.FallbackDelay = dialer.FallbackDelay
		cfg.DomainStrategy = dialer.DomainStrategy
		if dialer.DomainResolver != nil {
			cfg.DomainResolver = dialer.DomainResolver
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
					PersistentKeepaliveInterval: firstPositiveInt(
						getInt(peerMap, "persistent-keepalive-interval"),
						getInt(peerMap, "persistent_keepalive_interval"),
						getInt(peerMap, "persistent-keepalive"),
					),
				}
				peers = append(peers, peer)
			}

			switch len(peers) {
			case 0:
				// ignore malformed empty peers block
			case 1:
				peer := peers[0]
				cfg.Peers = peers
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
			if keepalive := firstPositiveInt(getInt(proxy, "persistent-keepalive-interval"), getInt(proxy, "persistent_keepalive_interval"), getInt(proxy, "persistent-keepalive")); keepalive > 0 {
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
		}

		if len(cfg.Peers) == 0 {
			if cfg.Server == "" || cfg.ServerPort <= 0 || cfg.PeerPublicKey == "" {
				return ImportItem{}, fmt.Errorf("missing server/port/public-key")
			}
		}

		return ImportItem{Source: "clash:" + name, Type: "wireguard", Name: name, Config: cfg}, nil

	case "socks5", "socks5h", "socks":
		cfg := models.SOCKS5Config{
			Server:     server,
			ServerPort: port,
			Username:   getString(proxy, "username"),
			Password:   getString(proxy, "password"),
		}
		network, err := normalizeNetworkList(getString(proxy, "network"))
		if err != nil {
			return ImportItem{}, err
		}
		cfg.Network = models.ListableString(network)
		if getBool(proxy, "udp-over-tcp") || getBool(proxy, "udp_over_tcp") {
			cfg.UDPOverTCP = true
			if version := firstPositiveInt(getInt(proxy, "udp-over-tcp-version"), getInt(proxy, "udp_over_tcp_version")); version > 0 {
				if version > 2 {
					return ImportItem{}, fmt.Errorf("unsupported udp-over-tcp version: %d", version)
				}
				cfg.UDPOverTCPOptions = models.NativeOptions{"enabled": true, "version": version}
				cfg.UDPOverTCP = cfg.UDPOverTCPOptions
			}
		}
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
		}
		proxyType := "socks5"
		if rawType == "socks5h" {
			proxyType = "socks5h"
		}
		return ImportItem{Source: "clash:" + name, Type: proxyType, Name: name, Config: cfg}, nil

	case "http", "https":
		cfg := models.HTTPProxyConfig{
			Server:     server,
			ServerPort: port,
			Username:   getString(proxy, "username"),
			Password:   getString(proxy, "password"),
			TLS:        rawType == "https" || getBool(proxy, "tls"),
			SNI:        getString(proxy, "sni"),
			Insecure:   getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify"),
			Path:       getString(proxy, "path"),
		}
		if headers, ok := getMap(proxy, "headers"); ok {
			cfg.Headers = models.NativeOptions(headers)
		}
		var err error
		cfg.TLSOptions, err = buildClashTLSOptions(proxy, cfg.TLS, false)
		if err != nil {
			return ImportItem{}, err
		}
		if err := applyClashDialerOptions(&cfg.DialerOptions, proxy); err != nil {
			return ImportItem{}, err
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
	if len(compact) < 4 {
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
	if len(compact) >= 64 && looksLikeBase64(compact) {
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
	if u.Hostname() == "" {
		return false
	}
	if u.User != nil {
		return true
	}
	if hasHTTPProxyMarker(u) {
		return true
	}
	allowedKeys := map[string]struct{}{
		"proxy": {}, "sni": {}, "insecure": {}, "allowInsecure": {}, "allow_insecure": {},
		"headers": {}, "alpn": {}, "fp": {}, "fingerprint": {}, "ech": {}, "tls_options": {},
		"detour": {}, "dialer-proxy": {}, "dialer_proxy": {}, "bind_interface": {}, "bind-interface": {},
		"interface-name": {}, "interface_name": {}, "inet4_bind_address": {}, "inet4-bind-address": {},
		"inet6_bind_address": {}, "inet6-bind-address": {}, "protect_path": {}, "protect-path": {},
		"routing_mark": {}, "routing-mark": {}, "reuse_addr": {}, "reuse-addr": {}, "netns": {}, "net-ns": {},
		"connect_timeout": {}, "connect-timeout": {}, "tcp_fast_open": {}, "tcp-fast-open": {}, "tfo": {},
		"tcp_multi_path": {}, "tcp-multi-path": {}, "mptcp": {}, "udp_fragment": {}, "udp-fragment": {},
		"domain_resolver": {}, "domain-resolver": {}, "domain_resolver_options": {}, "domain-resolver-options": {},
		"network_strategy": {}, "network-strategy": {}, "network_type": {}, "network-type": {},
		"fallback_network_type": {}, "fallback-network-type": {}, "fallback_delay": {}, "fallback-delay": {},
		"domain_strategy": {}, "domain-strategy": {}, "ip-version": {}, "ip_version": {},
	}
	hasProxyOption := false
	for k := range u.Query() {
		if _, ok := allowedKeys[k]; !ok {
			return false
		}
		hasProxyOption = true
	}
	return hasProxyOption
}

func hasHTTPProxyMarker(u *url.URL) bool {
	if u == nil {
		return false
	}
	marker := strings.ToLower(strings.TrimSpace(u.Query().Get("proxy")))
	return marker == "1" || marker == "true" || marker == "spm"
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
	return strings.Contains(lower, "proxies:") || strings.Contains(lower, "payload:") || strings.Contains(lower, "proxy-groups:") || strings.Contains(lower, "proxy-providers:")
}

func looksLikeBase64(compact string) bool {
	if len(compact) < 4 {
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

func looksLikeImportPayload(input string) bool {
	normalized := normalizeImportText(input)
	if normalized == "" {
		return false
	}
	if looksLikeClashYAML(normalized) {
		return true
	}
	lower := strings.ToLower(normalized)
	for _, scheme := range []string{"ss://", "vless://", "vmess://", "trojan://", "hysteria2://", "hy2://", "tuic://", "anytls://", "socks://", "socks5://", "socks5h://", "wireguard://", "wg://", "http://", "https://"} {
		if strings.Contains(lower, scheme) {
			return true
		}
	}
	if decoded, ok, _ := decodeBase64Subscription(normalized); ok && strings.TrimSpace(decoded) != "" {
		return true
	}
	return false
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
			case bool:
				return strconv.FormatBool(vv)
			case int:
				return strconv.Itoa(vv)
			case int8:
				return strconv.FormatInt(int64(vv), 10)
			case int16:
				return strconv.FormatInt(int64(vv), 10)
			case int32:
				return strconv.FormatInt(int64(vv), 10)
			case int64:
				return strconv.FormatInt(vv, 10)
			case uint:
				return strconv.FormatUint(uint64(vv), 10)
			case uint8:
				return strconv.FormatUint(uint64(vv), 10)
			case uint16:
				return strconv.FormatUint(uint64(vv), 10)
			case uint32:
				return strconv.FormatUint(uint64(vv), 10)
			case uint64:
				return strconv.FormatUint(vv, 10)
			case float32:
				return strconv.FormatFloat(float64(vv), 'f', -1, 32)
			case float64:
				return strconv.FormatFloat(vv, 'f', -1, 64)
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

func clashBoolExplicitlyFalse(m map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, ok := m[key]; ok {
			return !getBool(m, key)
		}
	}
	return false
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

func stringifyPluginOpts(plugin string, v any) (string, error) {
	switch vv := v.(type) {
	case string:
		parsed, err := parseSIP003Options(vv)
		if err != nil {
			return "", err
		}
		return pluginOptsFromMap(plugin, parsed)
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
		return "", fmt.Errorf("invalid shadowsocks plugin options type %T", v)
	}
}

func pluginOptsFromMap(plugin string, m map[string]any) (string, error) {
	plugin = normalizeShadowsocksPlugin(plugin)
	if plugin == "obfs-local" {
		allowed := map[string]struct{}{
			"mode": {}, "obfs": {}, "host": {}, "hostname": {}, "obfs-host": {}, "obfs_host": {},
		}
		for key := range m {
			if _, ok := allowed[key]; !ok {
				return "", fmt.Errorf("unsupported obfs-local option: %s", key)
			}
		}
		mode := firstNonEmpty(getString(m, "mode"), getString(m, "obfs"))
		if mode != "" && mode != "http" && mode != "tls" {
			return "", fmt.Errorf("unsupported obfs-local mode: %s", mode)
		}
		host := firstNonEmpty(getString(m, "host"), getString(m, "hostname"), getString(m, "obfs-host"), getString(m, "obfs_host"))
		parts := []string{}
		if mode != "" {
			parts = append(parts, "obfs="+escapeSIP003Option(mode))
		}
		if host != "" {
			parts = append(parts, "obfs-host="+escapeSIP003Option(host))
		}
		return strings.Join(parts, ";"), nil
	}
	if plugin == "v2ray-plugin" {
		return v2rayPluginOptsFromMap(m)
	}
	return "", fmt.Errorf("plugin options are not supported for shadowsocks plugin %q", plugin)
}

func v2rayPluginOptsFromMap(options map[string]any) (string, error) {
	aliases := map[string]string{
		"mode": "mode", "host": "host", "path": "path", "tls": "tls", "mux": "mux",
		"cert": "cert", "certraw": "certRaw", "cert-raw": "certRaw", "cert_raw": "certRaw",
	}
	normalized := make(map[string]any, len(options))
	unknown := make(map[string]any)
	for rawKey, value := range options {
		trimmedKey := strings.TrimSpace(rawKey)
		if trimmedKey == "" {
			return "", fmt.Errorf("invalid empty v2ray-plugin option key")
		}
		key, ok := aliases[strings.ToLower(trimmedKey)]
		if !ok {
			// sing-box's built-in SIP003 parser accepts arbitrary scalar
			// options and the v1.12.12 v2ray-plugin implementation ignores
			// keys it does not consume (for example loglevel). Preserve them
			// so importing a working URI does not become unnecessarily strict.
			unknown[trimmedKey] = value
			continue
		}
		normalized[key] = value
	}

	if rawMode, ok := normalized["mode"]; ok {
		mode := strings.ToLower(strings.TrimSpace(fmt.Sprint(rawMode)))
		if mode != "websocket" && mode != "quic" {
			return "", fmt.Errorf("unsupported v2ray-plugin mode: %s", mode)
		}
		normalized["mode"] = mode
	}

	parts := make([]string, 0, len(normalized))
	for _, key := range []string{"mode", "host", "path", "cert", "certRaw"} {
		value, ok := normalized[key]
		if !ok {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" {
			parts = append(parts, key+"="+escapeSIP003Option(text))
		}
	}
	if rawTLS, ok := normalized["tls"]; ok {
		enabled, err := strictPluginBool(rawTLS)
		if err != nil {
			return "", fmt.Errorf("invalid v2ray-plugin tls option: %w", err)
		}
		if enabled {
			parts = append(parts, "tls")
		}
	}
	if rawMux, ok := normalized["mux"]; ok {
		mux, err := normalizeV2RayPluginMux(rawMux)
		if err != nil {
			return "", err
		}
		parts = append(parts, "mux="+strconv.Itoa(mux))
	}
	unknownKeys := make([]string, 0, len(unknown))
	for key := range unknown {
		unknownKeys = append(unknownKeys, key)
	}
	sort.Strings(unknownKeys)
	for _, key := range unknownKeys {
		encoded, err := encodeUnknownSIP003Option(key, unknown[key])
		if err != nil {
			return "", err
		}
		parts = append(parts, encoded)
	}
	return strings.Join(parts, ";"), nil
}

func encodeUnknownSIP003Option(key string, value any) (string, error) {
	escapedKey := escapeSIP003Option(key)
	switch typed := value.(type) {
	case string:
		return escapedKey + "=" + escapeSIP003Option(typed), nil
	case bool:
		if typed {
			return escapedKey, nil
		}
		return escapedKey + "=false", nil
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return escapedKey + "=" + escapeSIP003Option(fmt.Sprint(typed)), nil
	default:
		return "", fmt.Errorf("invalid v2ray-plugin option %q value type %T", key, value)
	}
}

func strictPluginBool(value any) (bool, error) {
	switch typed := value.(type) {
	case bool:
		return typed, nil
	case int:
		return typed != 0, nil
	case int64:
		return typed != 0, nil
	case float64:
		return typed != 0, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "on", "enabled":
			return true, nil
		case "0", "false", "no", "off", "disabled", "":
			return false, nil
		}
	}
	return false, fmt.Errorf("expected boolean, got %v", value)
}

func normalizeV2RayPluginMux(value any) (int, error) {
	if boolean, ok := value.(bool); ok {
		if boolean {
			return 1, nil
		}
		return 0, nil
	}
	text := strings.TrimSpace(fmt.Sprint(value))
	if strings.EqualFold(text, "true") {
		return 1, nil
	}
	if strings.EqualFold(text, "false") {
		return 0, nil
	}
	mux, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("invalid v2ray-plugin mux option %q", text)
	}
	return mux, nil
}

func escapeSIP003Option(value string) string {
	var builder strings.Builder
	for _, char := range value {
		if char == '\\' || char == ';' || char == '=' {
			builder.WriteByte('\\')
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

func parseSIP003Options(raw string) (map[string]any, error) {
	result := map[string]any{}
	var key strings.Builder
	var value strings.Builder
	inValue := false
	escaped := false
	flush := func() error {
		optionKey := strings.TrimSpace(key.String())
		if optionKey == "" {
			if value.Len() == 0 {
				return nil
			}
			return fmt.Errorf("invalid empty SIP003 option key")
		}
		if inValue {
			result[optionKey] = value.String()
		} else {
			result[optionKey] = true
		}
		key.Reset()
		value.Reset()
		inValue = false
		return nil
	}
	for _, char := range raw {
		if escaped {
			if inValue {
				value.WriteRune(char)
			} else {
				key.WriteRune(char)
			}
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '=' && !inValue {
			inValue = true
			continue
		}
		if char == ';' {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		if inValue {
			value.WriteRune(char)
		} else {
			key.WriteRune(char)
		}
	}
	if escaped {
		return nil, fmt.Errorf("invalid trailing SIP003 escape")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
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

var bandwidthRe = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([a-z]+)?`)

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

func parseClashHysteria2Ports(proxy map[string]any) (int, []string, error) {
	raw := getString(proxy, "ports")
	if raw == "" {
		if values := getStringSlice(proxy, "ports"); len(values) > 0 {
			raw = strings.Join(values, ",")
		}
	}
	if raw == "" {
		return 0, nil, nil
	}
	return normalizeHysteria2Ports(raw)
}

func applyClashDialerOptions(options *models.DialerOptions, proxy map[string]any) error {
	if options == nil {
		return nil
	}
	options.Detour = firstNonEmpty(getString(proxy, "dialer-proxy"), getString(proxy, "dialer_proxy"), getString(proxy, "detour"))
	options.BindInterface = firstNonEmpty(getString(proxy, "interface-name"), getString(proxy, "interface_name"), getString(proxy, "bind-interface"), getString(proxy, "bind_interface"))
	options.Inet4BindAddress = firstNonEmpty(getString(proxy, "inet4-bind-address"), getString(proxy, "inet4_bind_address"))
	options.Inet6BindAddress = firstNonEmpty(getString(proxy, "inet6-bind-address"), getString(proxy, "inet6_bind_address"))
	options.ProtectPath = firstNonEmpty(getString(proxy, "protect-path"), getString(proxy, "protect_path"))
	if raw, ok := firstMapValue(proxy, "routing-mark", "routing_mark"); ok {
		switch value := raw.(type) {
		case int, int64, float64:
			options.RoutingMark = intFromAny(value)
		default:
			options.RoutingMark = parseWireGuardRoutingMark(fmt.Sprint(value))
		}
	}
	options.ReuseAddr = getBool(proxy, "reuse-addr") || getBool(proxy, "reuse_addr")
	options.NetNS = firstNonEmpty(getString(proxy, "netns"), getString(proxy, "net-ns"))
	options.ConnectTimeout = secondsToDurationString(firstMapValueOrNil(proxy, "connect-timeout", "connect_timeout"))
	options.TCPFastOpen = getBool(proxy, "tfo") || getBool(proxy, "tcp-fast-open") || getBool(proxy, "tcp_fast_open")
	options.TCPMultiPath = getBool(proxy, "mptcp") || getBool(proxy, "tcp-multi-path") || getBool(proxy, "tcp_multi_path")
	if raw, ok := firstMapValue(proxy, "udp-fragment", "udp_fragment"); ok {
		value := getBool(map[string]any{"value": raw}, "value")
		options.UDPFragment = &value
	}
	if resolver, ok := firstMapValue(proxy, "domain-resolver", "domain_resolver"); ok {
		options.DomainResolver = resolver
	}
	options.NetworkStrategy = firstNonEmpty(getString(proxy, "network-strategy"), getString(proxy, "network_strategy"))
	options.NetworkType = models.ListableString(firstNonEmptyStringSlice(getStringSlice(proxy, "network-type"), getStringSlice(proxy, "network_type")))
	options.FallbackNetworkType = models.ListableString(firstNonEmptyStringSlice(getStringSlice(proxy, "fallback-network-type"), getStringSlice(proxy, "fallback_network_type")))
	options.FallbackDelay = secondsToDurationString(firstMapValueOrNil(proxy, "fallback-delay", "fallback_delay"))

	ipVersion := strings.ToLower(firstNonEmpty(getString(proxy, "ip-version"), getString(proxy, "ip_version"), getString(proxy, "domain-strategy"), getString(proxy, "domain_strategy")))
	switch ipVersion {
	case "", "dual", "auto":
	case "ipv4", "ipv4-only", "ipv4_only":
		options.DomainStrategy = "ipv4_only"
	case "ipv6", "ipv6-only", "ipv6_only":
		options.DomainStrategy = "ipv6_only"
	case "prefer-ipv4", "prefer_ipv4", "ipv4-prefer", "ipv4_prefer":
		options.DomainStrategy = "prefer_ipv4"
	case "prefer-ipv6", "prefer_ipv6", "ipv6-prefer", "ipv6_prefer":
		options.DomainStrategy = "prefer_ipv6"
	default:
		return fmt.Errorf("unsupported ip-version/domain-strategy: %s", ipVersion)
	}
	return nil
}

func buildClashMultiplex(proxy map[string]any) (map[string]interface{}, error) {
	smux, ok := getMap(proxy, "smux")
	if !ok || len(smux) == 0 {
		return nil, nil
	}
	if getBool(smux, "only-tcp") || getBool(smux, "only_tcp") {
		return nil, fmt.Errorf("smux only-tcp is not supported by sing-box 1.12.12")
	}
	protocol := strings.ToLower(getString(smux, "protocol"))
	if protocol != "" && protocol != "smux" && protocol != "yamux" && protocol != "h2mux" {
		return nil, fmt.Errorf("unsupported smux protocol: %s", protocol)
	}
	result := map[string]interface{}{
		"enabled": getBool(smux, "enabled"),
	}
	if protocol != "" {
		result["protocol"] = protocol
	}
	for source, target := range map[string]string{
		"max-connections": "max_connections",
		"min-streams":     "min_streams",
		"max-streams":     "max_streams",
	} {
		if value := getInt(smux, source); value > 0 {
			result[target] = value
		}
	}
	if getBool(smux, "padding") {
		result["padding"] = true
	}
	if brutal, ok := getMap(smux, "brutal-opts", "brutal_opts"); ok && len(brutal) > 0 {
		result["brutal"] = map[string]any{
			"enabled":   getBool(brutal, "enabled"),
			"up_mbps":   firstPositiveInt(getInt(brutal, "up"), getInt(brutal, "up-mbps"), getInt(brutal, "up_mbps")),
			"down_mbps": firstPositiveInt(getInt(brutal, "down"), getInt(brutal, "down-mbps"), getInt(brutal, "down_mbps")),
		}
	}
	return result, nil
}

func buildClashTLSOptions(proxy map[string]any, enabled bool, disableSNI bool) (models.NativeOptions, error) {
	if hasMeaningfulClashValue(proxy, "pcs", "pinned-peer-cert-sha256", "pinned_peer_cert_sha256", "pinnedPeerCertSha256") {
		return nil, fmt.Errorf("TLS pinnedPeerCertSha256 is not supported by sing-box 1.12.12")
	}
	if hasMeaningfulClashValue(proxy, "vcn", "verify-peer-cert-by-name", "verify_peer_cert_by_name", "verifyPeerCertByName") {
		return nil, fmt.Errorf("TLS verifyPeerCertByName is not supported by sing-box 1.12.12")
	}
	reality, _ := getMap(proxy, "reality-opts", "reality_opts")
	ech, _ := getMap(proxy, "ech-opts", "ech_opts")
	if len(reality) > 0 {
		enabled = true
		if hasMeaningfulClashValue(reality, "pqv", "mldsa65-verify", "mldsa65_verify", "mldsa65Verify") {
			return nil, fmt.Errorf("reality mldsa65Verify is not supported by sing-box 1.12.12")
		}
		if hasMeaningfulClashValue(reality, "spx", "spider-x", "spider_x", "spiderX") {
			return nil, fmt.Errorf("reality SpiderX is not supported by sing-box 1.12.12")
		}
		if getBool(reality, "support-x25519mlkem768") || getBool(reality, "support_x25519mlkem768") {
			return nil, fmt.Errorf("reality ML-KEM extension is not supported by sing-box 1.12.12")
		}
	}
	if len(ech) > 0 && getBool(ech, "enable") {
		enabled = true
	}
	if !enabled && !disableSNI && len(reality) == 0 && len(ech) == 0 {
		return nil, nil
	}
	result := models.NativeOptions{"enabled": enabled}
	if disableSNI {
		result["disable_sni"] = true
	}
	if serverName := firstNonEmpty(getString(proxy, "servername"), getString(proxy, "sni")); serverName != "" {
		result["server_name"] = serverName
	}
	if getBool(proxy, "skip-cert-verify") || getBool(proxy, "skip_cert_verify") {
		result["insecure"] = true
	}
	if alpn := getStringSlice(proxy, "alpn"); len(alpn) > 0 {
		result["alpn"] = alpn
	}
	fingerprint := normalizeFingerprint(firstNonEmpty(getString(proxy, "client-fingerprint"), getString(proxy, "client_fingerprint"), getString(proxy, "fingerprint")))
	if fingerprint != "" {
		result["utls"] = map[string]any{"enabled": true, "fingerprint": fingerprint}
	}
	if len(reality) > 0 {
		publicKey := firstNonEmpty(getString(reality, "public-key"), getString(reality, "public_key"))
		if publicKey == "" {
			return nil, fmt.Errorf("reality-opts missing public-key")
		}
		result["reality"] = map[string]any{
			"enabled":    true,
			"public_key": publicKey,
			"short_id":   firstNonEmpty(getString(reality, "short-id"), getString(reality, "short_id")),
		}
		if _, ok := result["utls"]; !ok {
			result["utls"] = map[string]any{"enabled": true, "fingerprint": "chrome"}
		}
	}
	if len(ech) > 0 && getBool(ech, "enable") {
		if queryName := firstNonEmpty(getString(ech, "query-server-name"), getString(ech, "query_server_name")); queryName != "" {
			return nil, fmt.Errorf("ECH DNS query-server-name is not supported by sing-box 1.12.12")
		}
		configs := getStringSlice(ech, "config")
		if len(configs) == 0 {
			if config := getString(ech, "config"); config != "" {
				configs = []string{config}
			}
		}
		if len(configs) == 0 {
			return nil, fmt.Errorf("ech-opts enabled without config")
		}
		result["ech"] = map[string]any{"enabled": true, "config": configs}
	}
	return result, nil
}

func buildClashTransportOptions(proxy map[string]any) (string, models.NativeOptions, error) {
	if hasMeaningfulClashValue(proxy, "fm", "finalmask", "final-mask", "final_mask", "finalMask", "FinalMask") {
		return "", nil, fmt.Errorf("FinalMask is not supported by sing-box 1.12.12")
	}
	rawNetwork := strings.ToLower(strings.TrimSpace(getString(proxy, "network")))
	network := rawNetwork
	switch network {
	case "h2":
		network = "http"
	case "gun":
		network = "grpc"
	}
	switch network {
	case "", "tcp", "raw", "none":
		if network == "raw" || network == "none" {
			network = "tcp"
		}
		return network, nil, nil
	case "kcp", "mkcp", "xhttp", "splithttp", "mekya":
		return "", nil, fmt.Errorf("transport %s is not supported by sing-box 1.12.12", network)
	case "quic":
		quicOptions, _ := getMap(proxy, "quic-opts", "quic_opts")
		if len(quicOptions) > 0 || hasMeaningfulClashValue(proxy, "seed", "header-type", "header_type", "quic-security", "quic_security", "quic-key", "quic_key") {
			return "", nil, fmt.Errorf("quic transport options are not supported by sing-box 1.12.12")
		}
		return network, models.NativeOptions{"type": "quic"}, nil
	case "ws":
		opts, _ := getMap(proxy, "ws-opts", "ws_opts")
		result := models.NativeOptions{"type": "ws"}
		setNativeIfNotEmpty(result, "path", getString(opts, "path"))
		if headers, ok := getMap(opts, "headers"); ok && len(headers) > 0 {
			result["headers"] = headers
		}
		if value := getInt(opts, "max-early-data"); value > 0 {
			result["max_early_data"] = value
		}
		setNativeIfNotEmpty(result, "early_data_header_name", firstNonEmpty(getString(opts, "early-data-header-name"), getString(opts, "early_data_header_name")))
		return network, result, nil
	case "http":
		key := "http-opts"
		if rawNetwork == "h2" {
			key = "h2-opts"
		}
		opts, _ := getMap(proxy, key, strings.ReplaceAll(key, "-", "_"))
		result := models.NativeOptions{"type": "http"}
		paths := getStringSlice(opts, "path")
		if len(paths) > 1 {
			return "", nil, fmt.Errorf("multiple HTTP transport paths cannot be represented by sing-box 1.12.12")
		}
		if len(paths) == 1 {
			result["path"] = paths[0]
		}
		if hosts := getStringSlice(opts, "host"); len(hosts) > 0 {
			result["host"] = hosts
		}
		setNativeIfNotEmpty(result, "method", getString(opts, "method"))
		if headers, ok := getMap(opts, "headers"); ok && len(headers) > 0 {
			result["headers"] = headers
		}
		setNativeIfNotEmpty(result, "idle_timeout", secondsToDurationString(firstMapValueOrNil(opts, "idle-timeout", "idle_timeout")))
		setNativeIfNotEmpty(result, "ping_timeout", secondsToDurationString(firstMapValueOrNil(opts, "ping-timeout", "ping_timeout")))
		return network, result, nil
	case "grpc":
		opts, _ := getMap(proxy, "grpc-opts", "grpc_opts")
		if firstNonEmpty(
			getString(opts, "grpc-user-agent"),
			getString(opts, "grpc_user_agent"),
			getString(opts, "grpcUserAgent"),
			getString(opts, "user-agent"),
			getString(opts, "user_agent"),
			getString(opts, "userAgent"),
		) != "" {
			return "", nil, fmt.Errorf("grpc-user-agent is not supported by sing-box 1.12.12")
		}
		if firstNonEmpty(getString(opts, "authority"), getString(opts, "grpc-authority"), getString(opts, "grpc_authority")) != "" {
			return "", nil, fmt.Errorf("grpc authority is not supported by sing-box 1.12.12")
		}
		if strings.EqualFold(firstNonEmpty(getString(opts, "mode"), getString(opts, "grpc-mode"), getString(opts, "grpc_mode")), "multi") ||
			getBool(opts, "multi-mode") || getBool(opts, "multi_mode") || getBool(opts, "multiMode") {
			return "", nil, fmt.Errorf("grpc multi mode is not supported by sing-box 1.12.12")
		}
		result := models.NativeOptions{"type": "grpc"}
		setNativeIfNotEmpty(result, "service_name", firstNonEmpty(getString(opts, "grpc-service-name"), getString(opts, "service-name"), getString(opts, "service_name")))
		setNativeIfNotEmpty(result, "idle_timeout", secondsToDurationString(firstMapValueOrNil(opts, "idle-timeout", "idle_timeout")))
		setNativeIfNotEmpty(result, "ping_timeout", secondsToDurationString(firstMapValueOrNil(opts, "ping-timeout", "ping_timeout")))
		if getBool(opts, "permit-without-stream") || getBool(opts, "permit_without_stream") {
			result["permit_without_stream"] = true
		}
		return network, result, nil
	case "httpupgrade":
		opts, _ := getMap(proxy, "httpupgrade-opts", "httpupgrade_opts")
		result := models.NativeOptions{"type": "httpupgrade"}
		setNativeIfNotEmpty(result, "host", getString(opts, "host"))
		setNativeIfNotEmpty(result, "path", getString(opts, "path"))
		if headers, ok := getMap(opts, "headers"); ok && len(headers) > 0 {
			result["headers"] = headers
		}
		return network, result, nil
	default:
		return "", nil, fmt.Errorf("unknown transport type: %s", network)
	}
}

func hasMeaningfulClashValue(source map[string]any, keys ...string) bool {
	value, ok := firstMapValue(source, keys...)
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case bool:
		return typed
	case map[string]any:
		return len(typed) > 0
	case map[any]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	default:
		text := strings.TrimSpace(fmt.Sprint(value))
		return text != "" && text != "0"
	}
}

func firstMapValue(m map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := m[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func firstMapValueOrNil(m map[string]any, keys ...string) any {
	value, _ := firstMapValue(m, keys...)
	return value
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func setNativeIfNotEmpty(target models.NativeOptions, key string, value string) {
	if strings.TrimSpace(value) != "" {
		target[key] = value
	}
}

func hasNonEmptyMap(source map[string]any, keys ...string) bool {
	value, ok := getMap(source, keys...)
	return ok && len(value) > 0
}
