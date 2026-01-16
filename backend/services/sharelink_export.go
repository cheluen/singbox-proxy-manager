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
	case "socks5":
		return buildSOCKS5ShareLink(node.Name, parsedConfig.(*models.SOCKS5Config))
	case "http":
		return buildHTTPProxyShareLink(node.Name, parsedConfig.(*models.HTTPProxyConfig))
	default:
		return "", fmt.Errorf("unsupported proxy type: %s", node.Type)
	}
}

func buildSSShareLink(name string, cfg *models.SSConfig) (string, error) {
	cred := base64.RawURLEncoding.EncodeToString([]byte(cfg.Method + ":" + cfg.Password))
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "ss://" + cred + "@" + server

	params := url.Values{}
	if cfg.Plugin != "" {
		params.Set("plugin", cfg.Plugin)
	}
	if cfg.PluginOpts != "" {
		params.Set("plugin-opts", cfg.PluginOpts)
	}
	if cfg.UDPOverTCP {
		params.Set("udp_over_tcp", "1")
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}

	return link + encodeNameFragment(name), nil
}

func buildVLESSShareLink(name string, cfg *models.VLESSConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "vless://" + cfg.UUID + "@" + server

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
	if cfg.Security != "" {
		params.Set("security", cfg.Security)
	}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if cfg.ALPN != "" {
		params.Set("alpn", cfg.ALPN)
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", cfg.Fingerprint)
	}
	if cfg.PublicKey != "" {
		params.Set("pbk", cfg.PublicKey)
	}
	if cfg.ShortID != "" {
		params.Set("sid", cfg.ShortID)
	}
	if cfg.SpiderX != "" {
		params.Set("spx", cfg.SpiderX)
	}
	if cfg.PacketEncoding != "" {
		params.Set("packetEncoding", cfg.PacketEncoding)
	}
	if cfg.Insecure {
		params.Set("allowInsecure", "1")
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

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildVMESSShareLink(name string, cfg *models.VMESSConfig) (string, error) {
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
	}

	path := cfg.Path
	if cfg.Network == "grpc" && cfg.ServiceName != "" {
		path = cfg.ServiceName
	}

	raw := vmessJSON{
		Add:                 cfg.Server,
		Port:                cfg.ServerPort,
		ID:                  cfg.UUID,
		AID:                 cfg.AlterID,
		Net:                 cfg.Network,
		Type:                cfg.HeaderType,
		Host:                cfg.Host,
		Path:                path,
		TLS:                 cfg.TLS,
		SNI:                 cfg.SNI,
		ALPN:                cfg.ALPN,
		FP:                  cfg.Fingerprint,
		PS:                  name,
		V:                   "2",
		Scy:                 cfg.Security,
		AllowInsecure:       cfg.Insecure,
		MaxEarlyData:        cfg.MaxEarlyData,
		EarlyDataHeaderName: cfg.EarlyDataHeader,
		Seed:                cfg.Seed,
		GlobalPadding:       cfg.GlobalPadding,
		AuthenticatedLength: cfg.AuthenticatedLength,
	}

	encodedJSON, err := json.Marshal(raw)
	if err != nil {
		return "", err
	}

	return "vmess://" + base64.RawURLEncoding.EncodeToString(encodedJSON), nil
}

func buildTrojanShareLink(name string, cfg *models.TrojanConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "trojan://" + cfg.Password + "@" + server

	params := url.Values{}
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
		params.Set("fp", cfg.Fingerprint)
	}
	if cfg.ServiceName != "" {
		params.Set("serviceName", cfg.ServiceName)
	}
	if cfg.HTTPMethod != "" {
		params.Set("method", cfg.HTTPMethod)
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildHysteria2ShareLink(name string, cfg *models.Hysteria2Config) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "hysteria2://" + cfg.Password + "@" + server

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
	if cfg.Obfs != "" {
		params.Set("obfs", cfg.Obfs)
	}
	if cfg.ObfsPassword != "" {
		params.Set("obfs-password", cfg.ObfsPassword)
	}
	if cfg.SalamanderPassword != "" {
		params.Set("salamander", cfg.SalamanderPassword)
	}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if len(cfg.ALPN) > 0 {
		params.Set("alpn", strings.Join(cfg.ALPN, ","))
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", cfg.Fingerprint)
	}
	if cfg.InsecureSkipVerify {
		params.Set("insecure", "1")
	}
	if cfg.Network != "" {
		params.Set("network", cfg.Network)
	}
	if cfg.HopInterval != "" {
		params.Set("hopInterval", cfg.HopInterval)
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildTUICShareLink(name string, cfg *models.TUICConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "tuic://" + cfg.UUID + ":" + cfg.Password + "@" + server

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
		params.Set("fp", cfg.Fingerprint)
	}
	if cfg.InsecureSkipVerify {
		params.Set("insecure", "1")
	}
	if cfg.ZeroRTTHandshake {
		params.Set("zero_rtt_handshake", "1")
	}
	if cfg.DisableSNI {
		params.Set("disable_sni", "1")
	}
	if cfg.ReduceRTT {
		params.Set("reduce_rtt", "1")
	}
	if cfg.Heartbeat != "" {
		params.Set("heartbeat", cfg.Heartbeat)
	}
	if cfg.Network != "" {
		params.Set("network", cfg.Network)
	}

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildAnyTLSShareLink(name string, cfg *models.AnyTLSConfig) (string, error) {
	server := net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort))
	link := "anytls://" + cfg.Password + "@" + server

	params := url.Values{}
	if cfg.SNI != "" {
		params.Set("sni", cfg.SNI)
	}
	if len(cfg.ALPN) > 0 {
		params.Set("alpn", strings.Join(cfg.ALPN, ","))
	}
	if cfg.Fingerprint != "" {
		params.Set("fp", cfg.Fingerprint)
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

	if encoded := params.Encode(); encoded != "" {
		link += "?" + encoded
	}
	return link + encodeNameFragment(name), nil
}

func buildSOCKS5ShareLink(name string, cfg *models.SOCKS5Config) (string, error) {
	u := &url.URL{
		Scheme: "socks5",
		Host:   net.JoinHostPort(cfg.Server, strconv.Itoa(cfg.ServerPort)),
	}

	if cfg.Username != "" || cfg.Password != "" {
		u.User = url.UserPassword(cfg.Username, cfg.Password)
	}

	u.Fragment = strings.TrimSpace(name)
	return u.String(), nil
}

func buildHTTPProxyShareLink(name string, cfg *models.HTTPProxyConfig) (string, error) {
	scheme := "http"
	if cfg.TLS {
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
	if cfg.SNI != "" {
		q.Set("sni", cfg.SNI)
	}
	if cfg.Insecure {
		q.Set("insecure", "1")
	}
	u.RawQuery = q.Encode()

	u.Fragment = strings.TrimSpace(name)
	return u.String(), nil
}

func encodeNameFragment(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "#" + url.QueryEscape(name)
}
