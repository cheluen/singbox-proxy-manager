package services

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"sb-proxy/backend/models"
)

const protocolAuditECHConfigListBase64 = "AEb+DQBCAAAgACDn42pkDCHtQSt7TaqS+9xaZ8yxZyDKaRS4NfglFz8PeQAMAAEAAQABAAIAAQADAAtleGFtcGxlLmNvbQAA"

const protocolAuditRealityPublicKey = "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0"

const protocolAuditECHConfigPEM = `-----BEGIN ECH CONFIGS-----
AEb+DQBCAAAgACDn42pkDCHtQSt7TaqS+9xaZ8yxZyDKaRS4NfglFz8PeQAMAAEA
AQABAAIAAQADAAtleGFtcGxlLmNvbQAA
-----END ECH CONFIGS-----`

func protocolAuditECHConfigPEMLines() []string {
	return strings.Split(protocolAuditECHConfigPEM, "\n")
}

func TestShareLinkAuditShadowsocksSIP002Forms(t *testing.T) {
	t.Run("AEAD 2022 plain userinfo", func(t *testing.T) {
		credentials := url.UserPassword("2022-blake3-aes-128-gcm", "key+/=").String()
		cfgAny, typ, _, err := ParseShareLink("ss://" + credentials + "@[2001:db8::1]:8388/#node")
		if err != nil {
			t.Fatalf("ParseShareLink: %v", err)
		}
		cfg := cfgAny.(models.SSConfig)
		if typ != "ss" || cfg.Method != "2022-blake3-aes-128-gcm" || cfg.Password != "key+/=" || cfg.Server != "2001:db8::1" {
			t.Fatalf("unexpected config: %#v", cfg)
		}
	})

	t.Run("SIP003 plugin and slash", func(t *testing.T) {
		credentials := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:p@ss"))
		link := "ss://" + credentials + "@example.com:443/?plugin=v2ray-plugin%3Bmode%3Dwebsocket%3Bhost%3Dcdn.example#plugin"
		cfgAny, _, _, err := ParseShareLink(link)
		if err != nil {
			t.Fatalf("ParseShareLink: %v", err)
		}
		cfg := cfgAny.(models.SSConfig)
		if cfg.Password != "p@ss" || cfg.Plugin != "v2ray-plugin" || cfg.PluginOpts != "mode=websocket;host=cdn.example" {
			t.Fatalf("unexpected plugin config: %#v", cfg)
		}
	})

	t.Run("legacy standard base64 containing slash", func(t *testing.T) {
		decoded := "aes-128-gcm:\u083f@example.com:443"
		encoded := base64.StdEncoding.EncodeToString([]byte(decoded))
		if !strings.Contains(encoded, "/") {
			t.Fatalf("test fixture must exercise slash in base64: %q", encoded)
		}
		cfgAny, _, _, err := ParseShareLink("ss://" + encoded + "#legacy")
		if err != nil {
			t.Fatalf("ParseShareLink: %v", err)
		}
		cfg := cfgAny.(models.SSConfig)
		if cfg.Password != "\u083f" || cfg.Server != "example.com" || cfg.ServerPort != 443 {
			t.Fatalf("unexpected legacy config: %#v", cfg)
		}
	})
}

func TestShareLinkAuditNormalizesShadowsocksSIP003URLPluginOptions(t *testing.T) {
	credentials := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:secret"))
	tests := []struct {
		name       string
		query      string
		wantPlugin string
		wantOpts   string
	}{
		{
			name:       "combined v2ray mux true and ignored scalar",
			query:      "plugin=v2ray-plugin%3Bmode%3Dwebsocket%3Bmux%3Dtrue%3Bloglevel%3Dnone",
			wantPlugin: "v2ray-plugin",
			wantOpts:   "mode=websocket;mux=1;loglevel=none",
		},
		{
			name:       "separate v2ray mux false",
			query:      "plugin=v2ray-plugin&plugin-opts=mode%3Dwebsocket%3Bmux%3Dfalse",
			wantPlugin: "v2ray-plugin",
			wantOpts:   "mode=websocket;mux=0",
		},
		{
			name:       "simple obfs aliases",
			query:      "plugin=simple-obfs%3Bmode%3Dtls%3Bhost%3Dcdn.example",
			wantPlugin: "obfs-local",
			wantOpts:   "obfs=tls;obfs-host=cdn.example",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			link := "ss://" + credentials + "@127.0.0.1:8388/?" + test.query + "#plugin"
			parsed, proxyType, _, err := ParseShareLink(link)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			config := parsed.(models.SSConfig)
			if proxyType != "ss" || config.Plugin != test.wantPlugin || config.PluginOpts != test.wantOpts {
				t.Fatalf("unexpected normalized plugin config: %#v", config)
			}
		})
	}
}

func TestShareLinkAuditRealSingBoxAcceptsNormalizedSSPluginURLs(t *testing.T) {
	realBinary := os.Getenv("SINGBOX_TEST_BINARY")
	if realBinary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}

	credentials := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:secret"))
	links := []string{
		"ss://" + credentials + "@127.0.0.1:8388/?plugin=v2ray-plugin%3Bmode%3Dwebsocket%3Bmux%3Dtrue%3Bloglevel%3Dnone#v2ray",
		"ss://" + credentials + "@127.0.0.1:8389/?plugin=simple-obfs%3Bmode%3Dtls%3Bhost%3Dcdn.example#obfs",
	}

	t.Setenv("SINGBOX_BINARY", realBinary)
	service := NewSingBoxService(t.TempDir())
	for index, link := range links {
		parsed, proxyType, name, err := ParseShareLink(link)
		if err != nil {
			t.Fatalf("parse plugin URL %d: %v", index, err)
		}
		node := nativeTestNode(t, index+1, name, proxyType, parsed)
		configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{node})
		if err != nil {
			t.Fatalf("BuildGlobalConfig %s: %v", name, err)
		}
		if err := service.ValidateConfig(configJSON); err != nil {
			t.Fatalf("sing-box rejected normalized %s URL plugin: %v\n%s", name, err, configJSON)
		}
	}
}

func TestShareLinkAuditIPv6DefaultsAndDurations(t *testing.T) {
	tests := []struct {
		name string
		link string
		kind string
		host string
		port int
	}{
		{"vless ipv6", "vless://00000000-0000-0000-0000-000000000000@[2001:db8::10]:8443?security=none", "vless", "2001:db8::10", 8443},
		{"trojan ipv6", "trojan://p%40ss@[2001:db8::11]:443?security=tls", "trojan", "2001:db8::11", 443},
		{"tuic ipv6 default", "tuic://id:p%40ss@[2001:db8::12]?heartbeat=10", "tuic", "2001:db8::12", 443},
		{"anytls default", "anytls://p%40ss@example.com/#any", "anytls", "example.com", 443},
		{"http default", "http://proxy.example/path#http", "http", "proxy.example", 80},
		{"https default", "https://proxy.example/#https", "http", "proxy.example", 443},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, typ, _, err := ParseShareLink(test.link)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			if typ != test.kind {
				t.Fatalf("type=%q, want %q", typ, test.kind)
			}
			var host string
			var port int
			switch value := cfg.(type) {
			case models.VLESSConfig:
				host, port = value.Server, value.ServerPort
			case models.TrojanConfig:
				host, port = value.Server, value.ServerPort
			case models.TUICConfig:
				host, port = value.Server, value.ServerPort
				if value.Heartbeat != "10s" {
					t.Fatalf("heartbeat=%q", value.Heartbeat)
				}
			case models.AnyTLSConfig:
				host, port = value.Server, value.ServerPort
				if value.Password != "p@ss" {
					t.Fatalf("password=%q", value.Password)
				}
			case models.HTTPProxyConfig:
				host, port = value.Server, value.ServerPort
			}
			if host != test.host || port != test.port {
				t.Fatalf("endpoint=%s:%d, want %s:%d", host, port, test.host, test.port)
			}
		})
	}

	cfgAny, _, _, err := ParseShareLink("tuic://id:pass@example.com?heartbeat-interval=10000")
	if err != nil {
		t.Fatalf("TUIC heartbeat interval: %v", err)
	}
	if got := cfgAny.(models.TUICConfig).Heartbeat; got != "10s" {
		t.Fatalf("heartbeat interval=%q, want 10s", got)
	}
}

func TestShareLinkAuditHysteria2PortsECHAndPinRejection(t *testing.T) {
	cfgAny, _, _, err := ParseShareLink("hysteria2://pass@example.com/?ech=" + url.QueryEscape(protocolAuditECHConfigListBase64) + "&obfs=salamander&obfs-password=secret")
	if err != nil {
		t.Fatalf("default port parse: %v", err)
	}
	cfg := cfgAny.(models.Hysteria2Config)
	if cfg.ServerPort != 443 || nativeMap(cfg.TLSOptions["ech"])["enabled"] != true || strings.Join(nativeStringSlice(nativeMap(cfg.TLSOptions["ech"])["config"]), "\n") != protocolAuditECHConfigPEM {
		t.Fatalf("unexpected default/ECH config: %#v", cfg)
	}

	cfgAny, _, _, err = ParseShareLink("hy2://pass@example.com:443,5000-5002,6000/")
	if err != nil {
		t.Fatalf("multi-port parse: %v", err)
	}
	cfg = cfgAny.(models.Hysteria2Config)
	if cfg.ServerPort != 443 || strings.Join([]string(cfg.ServerPorts), ",") != "443,5000:5002,6000" {
		t.Fatalf("unexpected ports: %#v", cfg.ServerPorts)
	}

	if _, _, _, err := ParseShareLink("hy2://pass@example.com/?pinSHA256=deadbeef"); err == nil {
		t.Fatal("certificate pinning must be rejected for sing-box 1.12.12")
	}
}

func TestShareLinkAuditVMessEncodingsValidationAndH2(t *testing.T) {
	rawJSON := []byte(`{"v":"2","ps":"vm","add":"example.com","port":"443","id":"00000000-0000-0000-0000-000000000000","aid":"0","scy":"auto","net":"h2","type":"none","host":"a.example,b.example","path":"/h2","tls":"tls","alpn":"h2,http/1.1"}`)
	encodings := map[string]*base64.Encoding{
		"std": base64.StdEncoding, "raw std": base64.RawStdEncoding,
		"url": base64.URLEncoding, "raw url": base64.RawURLEncoding,
	}
	for name, encoding := range encodings {
		t.Run(name, func(t *testing.T) {
			cfgAny, _, _, err := ParseShareLink("vmess://" + encoding.EncodeToString(rawJSON))
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			cfg := cfgAny.(models.VMESSConfig)
			if cfg.Network != "http" || cfg.Method != "" || cfg.Host != "a.example,b.example" || cfg.ALPN != "h2,http/1.1" {
				t.Fatalf("unexpected H2 config: %#v", cfg)
			}
		})
	}

	cfbJSON := []byte(`{"v":"2","ps":"cfb","add":"example.com","port":"443","id":"00000000-0000-0000-0000-000000000021","aid":"0","scy":"aes-128-cfb","net":"tcp","type":"none","tls":"none"}`)
	cfbAny, _, _, err := ParseShareLink("vmess://" + base64.RawURLEncoding.EncodeToString(cfbJSON))
	if err != nil {
		t.Fatalf("legacy VMess aes-128-cfb: %v", err)
	}
	if got := cfbAny.(models.VMESSConfig).Security; got != "aes-128-cfb" {
		t.Fatalf("legacy VMess security=%q, want aes-128-cfb", got)
	}

	cfbAny, _, _, err = ParseShareLink("vmess://00000000-0000-0000-0000-000000000022@example.com:443?encryption=aes-128-cfb")
	if err != nil {
		t.Fatalf("URL VMess aes-128-cfb: %v", err)
	}
	if got := cfbAny.(models.VMESSConfig).Security; got != "aes-128-cfb" {
		t.Fatalf("URL VMess security=%q, want aes-128-cfb", got)
	}

	invalid := []string{
		`{"add":"example.com","port":443,"id":"id","scy":"auto","tls":"foo"}`,
		`{"add":"example.com","port":443,"id":"id","scy":"auto","tls":"reality"}`,
	}
	for _, raw := range invalid {
		if _, _, _, err := ParseShareLink("vmess://" + base64.RawURLEncoding.EncodeToString([]byte(raw))); err == nil {
			t.Fatalf("invalid VMess JSON accepted: %s", raw)
		}
	}
	if _, _, _, err := ParseShareLink("vmess://id@example.com:443?encryption=bad"); err == nil {
		t.Fatal("invalid VMess URL cipher accepted")
	}
}

func TestShareLinkAuditTUICAllowsEmptyPassword(t *testing.T) {
	links := []string{
		"tuic://00000000-0000-0000-0000-000000000023:@example.com:443?insecure=1",
		"tuic://00000000-0000-0000-0000-000000000024@example.com:443?insecure=1",
	}

	for index, link := range links {
		parsed, proxyType, name, err := ParseShareLink(link)
		if err != nil {
			t.Fatalf("parse empty-password TUIC link %d: %v", index, err)
		}
		config := parsed.(models.TUICConfig)
		if proxyType != "tuic" || config.Password != "" {
			t.Fatalf("unexpected empty-password TUIC config: %#v", config)
		}

		if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
			t.Setenv("SINGBOX_BINARY", realBinary)
			service := NewSingBoxService(t.TempDir())
			configJSON, buildErr := service.BuildGlobalConfig([]models.ProxyNode{
				nativeTestNode(t, 300+index, name, proxyType, parsed),
			})
			if buildErr != nil {
				t.Fatalf("build empty-password TUIC config: %v", buildErr)
			}
			if validateErr := service.ValidateConfig(configJSON); validateErr != nil {
				t.Fatalf("sing-box rejected empty-password TUIC config: %v\n%s", validateErr, configJSON)
			}
		}
	}
}

func TestShareLinkAuditVMessLegacyCompatibilityAndAdvancedRoundTrip(t *testing.T) {
	common := &models.VMESSConfig{
		Server: "vm.example", ServerPort: 443, UUID: "00000000-0000-0000-0000-000000000000",
		Security: "auto", Network: "ws", TLS: "tls", SNI: "sni.example", ALPN: "h2,http/1.1",
		Path: "/ws", Host: "cdn.example",
		TLSOptions:       models.NativeOptions{"enabled": true, "server_name": "sni.example", "alpn": []string{"h2", "http/1.1"}},
		TransportOptions: models.NativeOptions{"type": "ws", "path": "/ws", "headers": map[string]any{"Host": "cdn.example"}},
	}
	link, err := buildVMESSShareLink("common", common)
	if err != nil {
		t.Fatalf("build common VMess: %v", err)
	}
	if strings.Contains(strings.TrimPrefix(link, "vmess://"), "@") {
		t.Fatalf("ordinary VMess must retain legacy base64 JSON compatibility: %s", link)
	}

	advanced := *common
	advanced.OutboundNetwork = models.ListableString{"tcp"}
	advanced.Detour = "selector"
	advanced.TLS = "reality"
	advanced.TLSOptions = models.NativeOptions{
		"enabled": true,
		"reality": map[string]any{"enabled": true, "public_key": protocolAuditRealityPublicKey, "short_id": "abcd"},
	}
	advanced.TransportOptions = models.NativeOptions{"type": "grpc", "service_name": "svc", "idle_timeout": "15s", "permit_without_stream": true}
	advanced.Network = "grpc"
	advanced.ServiceName = "svc"
	advanced.MultiplexConfig = map[string]interface{}{"enabled": true, "protocol": "h2mux"}
	link, err = buildVMESSShareLink("advanced", &advanced)
	if err != nil {
		t.Fatalf("build advanced VMess: %v", err)
	}
	if !strings.Contains(strings.TrimPrefix(link, "vmess://"), "@") {
		t.Fatalf("advanced VMess must use URL form: %s", link)
	}
	parsedAny, _, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse advanced VMess: %v", err)
	}
	parsed := parsedAny.(models.VMESSConfig)
	if name != "advanced" || parsed.Detour != "selector" || parsed.Network != "grpc" || nativeString(parsed.TransportOptions["idle_timeout"]) != "15s" {
		t.Fatalf("advanced round-trip mismatch: %#v", parsed)
	}
	if nativeString(nativeMap(parsed.TLSOptions["reality"])["public_key"]) != protocolAuditRealityPublicKey || parsed.MultiplexConfig["protocol"] != "h2mux" {
		t.Fatalf("advanced native options lost: %#v %#v", parsed.TLSOptions, parsed.MultiplexConfig)
	}

	legacyGun := map[string]any{
		"v": "2", "ps": "legacy-gun", "add": "gun.example", "port": "443",
		"id": "11111111-1111-1111-1111-111111111111", "aid": "0", "scy": "auto",
		"net": "gun", "type": "none", "path": "gun-service", "tls": "tls",
	}
	rawLegacyGun, err := json.Marshal(legacyGun)
	if err != nil {
		t.Fatalf("marshal legacy gun fixture: %v", err)
	}
	parsedAny, _, _, err = ParseShareLink("vmess://" + base64.RawURLEncoding.EncodeToString(rawLegacyGun))
	if err != nil {
		t.Fatalf("parse legacy gun alias: %v", err)
	}
	parsedGun := parsedAny.(models.VMESSConfig)
	if parsedGun.Network != "grpc" || parsedGun.ServiceName != "gun-service" || nativeString(parsedGun.TransportOptions["service_name"]) != "gun-service" {
		t.Fatalf("legacy gun alias mapping mismatch: %#v", parsedGun)
	}
}

func TestShareLinkAuditNativeOnlyExportCompatibility(t *testing.T) {
	t.Run("VMess legacy transport", func(t *testing.T) {
		cfg := &models.VMESSConfig{
			Server: "vmess.example", ServerPort: 443,
			UUID:     "00000000-0000-0000-0000-000000000020",
			Security: "auto", Network: "ws", TLS: "none",
			TransportOptions: models.NativeOptions{
				"type": "ws", "path": "/native",
				"headers":                map[string]interface{}{"Host": "native.example"},
				"max_early_data":         2048,
				"early_data_header_name": "Sec-WebSocket-Protocol",
			},
		}
		link, err := buildVMESSShareLink("native", cfg)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if strings.Contains(strings.TrimPrefix(link, "vmess://"), "@") {
			t.Fatalf("legacy-compatible native transport unexpectedly forced URL form: %s", link)
		}
		parsed, _, _, err := ParseShareLink(link)
		if err != nil {
			t.Fatalf("parse exported VMess: %v", err)
		}
		got := parsed.(models.VMESSConfig)
		if got.Path != "/native" || got.Host != "native.example" || got.MaxEarlyData != 2048 || got.EarlyDataHeader != "Sec-WebSocket-Protocol" {
			t.Fatalf("native-only VMess transport lost: link=%s config=%#v", link, got)
		}
	})

	t.Run("VMess legacy mixed-case Host", func(t *testing.T) {
		for _, hostKey := range []string{"Host", "host", "HOST", "hOsT"} {
			cfg := &models.VMESSConfig{
				Server: "vmess.example", ServerPort: 443,
				UUID:     "00000000-0000-0000-0000-000000000023",
				Security: "auto", Network: "ws", TLS: "none",
				TransportOptions: models.NativeOptions{
					"type": "ws", "path": "/native",
					"headers": map[string]interface{}{hostKey: "native.example"},
				},
			}
			link, err := buildVMESSShareLink("native", cfg)
			if err != nil {
				t.Fatalf("build %s: %v", hostKey, err)
			}
			if strings.Contains(strings.TrimPrefix(link, "vmess://"), "@") {
				t.Fatalf("scalar %s header unexpectedly forced URL form: %s", hostKey, link)
			}
			parsed, _, _, err := ParseShareLink(link)
			if err != nil {
				t.Fatalf("parse %s: %v", hostKey, err)
			}
			if got := parsed.(models.VMESSConfig); got.Host != "native.example" {
				t.Fatalf("mixed-case %s Host lost: link=%s config=%#v", hostKey, link, got)
			}
		}
	})

	realityOptions := func() models.NativeOptions {
		return models.NativeOptions{
			"enabled":     true,
			"server_name": "reality.example",
			"reality": map[string]interface{}{
				"enabled":    true,
				"public_key": "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0",
				"short_id":   "0123456789abcdef",
			},
		}
	}

	t.Run("VLESS native Reality", func(t *testing.T) {
		cfg := &models.VLESSConfig{
			Server: "vless.example", ServerPort: 443,
			UUID:       "00000000-0000-0000-0000-000000000021",
			TLSOptions: realityOptions(),
		}
		link, err := buildVLESSShareLink("native-reality", cfg)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		parsed, _, _, err := ParseShareLink(link)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := parsed.(models.VLESSConfig); got.Security != "reality" || got.PublicKey == "" {
			t.Fatalf("native VLESS Reality exported incorrectly: link=%s config=%#v", link, got)
		}
	})

	t.Run("VMess URL native Reality", func(t *testing.T) {
		cfg := &models.VMESSConfig{
			Server: "vmess.example", ServerPort: 443,
			UUID: "00000000-0000-0000-0000-000000000022", Security: "auto",
			TLSOptions:      realityOptions(),
			MultiplexConfig: map[string]interface{}{"enabled": true},
		}
		link, err := buildVMESSShareLink("native-reality", cfg)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !strings.Contains(strings.TrimPrefix(link, "vmess://"), "@") {
			t.Fatalf("fixture must exercise VMess URL form: %s", link)
		}
		parsed, _, _, err := ParseShareLink(link)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if got := parsed.(models.VMESSConfig); got.TLS != "reality" {
			t.Fatalf("native VMess Reality exported incorrectly: link=%s config=%#v", link, got)
		}
	})
}

func TestShareLinkAuditNativeWebSocketHostListsRoundTrip(t *testing.T) {
	type fixture struct {
		name      string
		proxyType string
		build     func(models.NativeOptions) (string, error)
	}
	fixtures := []fixture{
		{
			name: "vless", proxyType: "vless",
			build: func(transport models.NativeOptions) (string, error) {
				return buildVLESSShareLink("native-host-list", &models.VLESSConfig{
					Server: "127.0.0.1", ServerPort: 80,
					UUID: "00000000-0000-0000-0000-000000000024", Security: "none", Network: "ws",
					TransportOptions: transport,
				})
			},
		},
		{
			name: "vmess URL", proxyType: "vmess",
			build: func(transport models.NativeOptions) (string, error) {
				return buildVMESSShareLink("native-host-list", &models.VMESSConfig{
					Server: "127.0.0.1", ServerPort: 80,
					UUID: "00000000-0000-0000-0000-000000000025", Security: "auto", Network: "ws", TLS: "none",
					TransportOptions: transport,
				})
			},
		},
		{
			name: "trojan", proxyType: "trojan",
			build: func(transport models.NativeOptions) (string, error) {
				return buildTrojanShareLink("native-host-list", &models.TrojanConfig{
					Server: "127.0.0.1", ServerPort: 443, Password: "secret",
					Security: "tls", Network: "ws", TransportOptions: transport,
				})
			},
		},
	}

	service := &SingBoxService{}
	hostShapes := []struct {
		name  string
		value interface{}
	}{
		{name: "string-list", value: []string{"native.example", "backup.example"}},
		{name: "interface-list", value: []interface{}{"native.example", "backup.example"}},
	}
	nodes := make([]models.ProxyNode, 0, len(fixtures)*len(hostShapes))
	for index, test := range fixtures {
		for shapeIndex, shape := range hostShapes {
			t.Run(test.name+"/"+shape.name, func(t *testing.T) {
				transport := models.NativeOptions{
					"type": "ws", "path": "/native",
					"headers": map[string]interface{}{"hOsT": shape.value},
				}
				link, err := test.build(transport)
				if err != nil {
					t.Fatalf("build: %v", err)
				}
				parsedURL, err := url.Parse(link)
				if err != nil {
					t.Fatalf("parse exported URL: %v", err)
				}
				if parsedURL.Query().Get("host") != "" {
					t.Fatalf("native Host list must not be flattened into a lossy host parameter: %s", link)
				}
				if test.proxyType == "vmess" && !strings.Contains(strings.TrimPrefix(link, "vmess://"), "@") {
					t.Fatalf("VMess Host list must use URL form with transport_options: %s", link)
				}

				parsedAny, proxyType, _, err := ParseShareLink(link)
				if err != nil {
					t.Fatalf("parse exported link: %v", err)
				}
				node := nativeTestNode(t, 50+index*len(hostShapes)+shapeIndex, "native-host-list-"+test.proxyType+"-"+shape.name, proxyType, parsedAny)
				nodes = append(nodes, node)
				parsedConfig, err := node.ParseConfig()
				if err != nil {
					t.Fatalf("parse persisted config: %v", err)
				}

				var outbound OutboundConfig
				switch config := parsedConfig.(type) {
				case *models.VLESSConfig:
					outbound, err = service.generateVLESSOutbound(config, "test")
				case *models.VMESSConfig:
					outbound, err = service.generateVMESSOutbound(config, "test")
				case *models.TrojanConfig:
					outbound, err = service.generateTrojanOutbound(config, "test")
				default:
					t.Fatalf("unexpected parsed type %T", parsedConfig)
				}
				if err != nil {
					t.Fatalf("generate persisted config: %v", err)
				}
				headers := nativeMap(nativeMap(outbound.Extra["transport"])["headers"])
				var hostValue interface{}
				for key, value := range headers {
					if strings.EqualFold(key, "host") {
						hostValue = value
						break
					}
				}
				hosts := nativeStringSlice(hostValue)
				if len(hosts) != 2 || hosts[0] != "native.example" || hosts[1] != "backup.example" {
					t.Fatalf("native Host list was overwritten by a flat value: %#v", headers)
				}
				if _, scalar := hostValue.(string); scalar {
					t.Fatalf("native Host list was collapsed to a scalar: %#v", headers)
				}
			})
		}
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		realService := NewSingBoxService(t.TempDir())
		configJSON, err := realService.BuildGlobalConfig(nodes)
		if err != nil {
			t.Fatalf("build native Host-list config: %v", err)
		}
		if err := realService.ValidateConfig(configJSON); err != nil {
			t.Fatalf("sing-box 1.12.12 rejected native Host-list headers: %v\n%s", err, configJSON)
		}
	}
}

func TestShareLinkAuditDisabledNativeTLSSubOptionsStayDisabled(t *testing.T) {
	disabled := models.NativeOptions{
		"enabled": true,
		"utls": map[string]interface{}{
			"enabled": false, "fingerprint": "firefox",
		},
		"reality": map[string]interface{}{
			"enabled": false, "public_key": "disabled-public-key", "short_id": "0123456789abcdef",
		},
		"ech": map[string]interface{}{
			"enabled": false, "config": []interface{}{"ZGlzYWJsZWQtZWNo"},
		},
	}
	params := url.Values{}
	mergeNativeTLSShareParams(params, disabled)
	for _, key := range []string{"fp", "security", "pbk", "sid", "ech"} {
		if params.Get(key) != "" {
			t.Fatalf("disabled native TLS child leaked into %s=%q", key, params.Get(key))
		}
	}

	active := models.NativeOptions{
		"enabled": true,
		"utls": map[string]interface{}{
			"enabled": true, "fingerprint": "firefox",
		},
		"reality": map[string]interface{}{
			"enabled": true, "public_key": protocolAuditRealityPublicKey, "short_id": "0123456789abcdef",
		},
	}
	params = url.Values{}
	mergeNativeTLSShareParams(params, active)
	if params.Get("fp") != "firefox" || params.Get("security") != "reality" || params.Get("pbk") != protocolAuditRealityPublicKey || params.Get("sid") != "0123456789abcdef" || params.Get("ech") != "" {
		t.Fatalf("enabled native TLS children were not exported: %v", params)
	}
	activeECH := models.NativeOptions{
		"enabled": true,
		"ech": map[string]interface{}{
			"enabled": true, "config": protocolAuditECHConfigPEMLines(),
		},
	}
	params = url.Values{}
	mergeNativeTLSShareParams(params, activeECH)
	if params.Get("ech") != protocolAuditECHConfigListBase64 || params.Get("security") != "" {
		t.Fatalf("enabled native ECH was not exported independently: %v", params)
	}

	legacyConfig := &models.VMESSConfig{
		Server: "vmess.example", ServerPort: 443,
		UUID: "00000000-0000-0000-0000-000000000026", Security: "auto", Network: "ws", TLS: "tls",
		TLSOptions: disabled,
	}
	link, err := buildVMESSShareLink("disabled-native-tls", legacyConfig)
	if err != nil {
		t.Fatalf("build legacy VMess: %v", err)
	}
	if strings.Contains(strings.TrimPrefix(link, "vmess://"), "@") {
		t.Fatalf("fixture must exercise legacy VMess JSON: %s", link)
	}
	parsedAny, _, _, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse legacy VMess: %v", err)
	}
	parsed := parsedAny.(models.VMESSConfig)
	if parsed.Fingerprint != "" || nativeBool(nativeMap(parsed.TLSOptions["utls"])["enabled"]) || nativeBool(nativeMap(parsed.TLSOptions["reality"])["enabled"]) || nativeBool(nativeMap(parsed.TLSOptions["ech"])["enabled"]) {
		t.Fatalf("disabled native TLS child was re-enabled by VMess legacy export: link=%s config=%#v", link, parsed)
	}
}

func TestShareLinkAuditNativeV2RayRoundTrip(t *testing.T) {
	vless := &models.VLESSConfig{
		Server: "v.example", ServerPort: 443, UUID: "00000000-0000-0000-0000-000000000010", Security: "tls", Network: "ws",
		TLSOptions:       models.NativeOptions{"enabled": true, "server_name": "v.example", "min_version": "1.2"},
		TransportOptions: models.NativeOptions{"type": "ws", "path": "/ws", "headers": map[string]any{"Host": "v.example", "X-Test": "1"}},
		MultiplexConfig:  map[string]interface{}{"enabled": true, "max_connections": 2},
	}
	link, err := buildVLESSShareLink("native", vless)
	if err != nil {
		t.Fatalf("build VLESS: %v", err)
	}
	parsedAny, _, _, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse VLESS: %v", err)
	}
	parsedVLESS := parsedAny.(models.VLESSConfig)
	if nativeString(parsedVLESS.TLSOptions["min_version"]) != "1.2" || nativeMap(parsedVLESS.TransportOptions["headers"])["X-Test"] != "1" {
		t.Fatalf("VLESS native options lost: %#v %#v", parsedVLESS.TLSOptions, parsedVLESS.TransportOptions)
	}

	trojan := &models.TrojanConfig{
		Server: "t.example", ServerPort: 443, Password: "pass", Security: "tls", Network: "grpc",
		TLSOptions:       models.NativeOptions{"enabled": true, "server_name": "t.example", "fragment": true},
		TransportOptions: models.NativeOptions{"type": "grpc", "service_name": "svc", "idle_timeout": "20s"},
		MultiplexConfig:  map[string]interface{}{"enabled": true},
	}
	link, err = buildTrojanShareLink("native", trojan)
	if err != nil {
		t.Fatalf("build Trojan: %v", err)
	}
	parsedAny, _, _, err = ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse Trojan: %v", err)
	}
	parsedTrojan := parsedAny.(models.TrojanConfig)
	if parsedTrojan.TLSOptions["fragment"] != true || nativeString(parsedTrojan.TransportOptions["idle_timeout"]) != "20s" || parsedTrojan.MultiplexConfig["enabled"] != true {
		t.Fatalf("Trojan native options lost: %#v", parsedTrojan)
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		service := NewSingBoxService(t.TempDir())
		configJSON, buildErr := service.BuildGlobalConfig([]models.ProxyNode{
			nativeTestNode(t, 31, "native-vless", "vless", parsedVLESS),
			nativeTestNode(t, 32, "native-trojan", "trojan", parsedTrojan),
		})
		if buildErr != nil {
			t.Fatalf("build native round-trip config: %v", buildErr)
		}
		if validateErr := service.ValidateConfig(configJSON); validateErr != nil {
			t.Fatalf("sing-box rejected native round-trip config: %v\n%s", validateErr, configJSON)
		}
	}
}

func TestShareLinkAuditRejectsConflictingTLSRepresentations(t *testing.T) {
	withTLS := func(base string, rawTLS string) string {
		separator := "?"
		if strings.Contains(base, "?") {
			separator = "&"
		}
		return base + separator + "tls_options=" + url.QueryEscape(rawTLS)
	}
	mismatchedECHTLS, err := json.Marshal(models.NativeOptions{
		"enabled": true,
		"ech": map[string]interface{}{
			"enabled": true,
			"config": func() []string {
				configList, decodeErr := decodeShareECHConfigList(protocolAuditECHConfigListBase64)
				if decodeErr != nil {
					t.Fatalf("decode ECH mismatch fixture: %v", decodeErr)
				}
				configList[len(configList)-1] ^= 1
				return encodeNativeECHConfigList(configList)
			}(),
		},
	})
	if err != nil {
		t.Fatalf("marshal mismatched ECH TLS fixture: %v", err)
	}

	tests := []struct {
		name string
		link string
	}{
		{
			name: "vless explicit none versus enabled TLS",
			link: withTLS("vless://id@example.com:443?security=none", `{"enabled":true}`),
		},
		{
			name: "vless explicit empty security versus enabled TLS",
			link: withTLS("vless://id@example.com:443?security=", `{"enabled":true}`),
		},
		{
			name: "vless explicit TLS versus Reality",
			link: withTLS("vless://id@example.com:443?security=tls", `{"enabled":true,"reality":{"enabled":true,"public_key":"public"}}`),
		},
		{
			name: "vless explicit Reality versus disabled Reality",
			link: withTLS("vless://id@example.com:443?security=reality&pbk=public", `{"enabled":true,"reality":{"enabled":false,"public_key":"public"}}`),
		},
		{
			name: "vless implied Reality versus disabled root TLS",
			link: withTLS("vless://id@example.com:443?pbk=public", `{"enabled":false}`),
		},
		{
			name: "vmess explicit none versus enabled TLS",
			link: withTLS("vmess://id@example.com:443?security=none", `{"enabled":true}`),
		},
		{
			name: "vmess explicit empty security versus enabled TLS",
			link: withTLS("vmess://id@example.com:443?security=", `{"enabled":true}`),
		},
		{
			name: "trojan explicit none versus enabled TLS",
			link: withTLS("trojan://pass@example.com:443?security=none", `{"enabled":true}`),
		},
		{
			name: "trojan explicit empty security versus disabled TLS",
			link: withTLS("trojan://pass@example.com:443?security=", `{"enabled":false}`),
		},
		{
			name: "http scheme versus enabled TLS",
			link: withTLS("http://example.com:80?proxy=1", `{"enabled":true}`),
		},
		{
			name: "https scheme versus disabled TLS",
			link: withTLS("https://example.com:443?proxy=1", `{"enabled":false}`),
		},
		{
			name: "hysteria2 requires TLS",
			link: withTLS("hysteria2://pass@example.com:443", `{"enabled":false}`),
		},
		{
			name: "tuic requires TLS",
			link: withTLS("tuic://id:pass@example.com:443", `{"enabled":false}`),
		},
		{
			name: "anytls requires TLS",
			link: withTLS("anytls://pass@example.com:443", `{"enabled":false}`),
		},
		{
			name: "explicit false insecure",
			link: withTLS("vless://id@example.com:443?security=tls&insecure=0", `{"enabled":true,"insecure":true}`),
		},
		{
			name: "server name mismatch",
			link: withTLS("vmess://id@example.com:443?security=tls&sni=standard.example", `{"enabled":true,"server_name":"native.example"}`),
		},
		{
			name: "ALPN mismatch",
			link: withTLS("trojan://pass@example.com:443?security=tls&alpn=h2", `{"enabled":true,"alpn":["http/1.1"]}`),
		},
		{
			name: "disable SNI mismatch",
			link: withTLS("tuic://id:pass@example.com:443?disable_sni=0", `{"enabled":true,"disable_sni":true}`),
		},
		{
			name: "uTLS enabled mismatch",
			link: withTLS("vless://id@example.com:443?security=tls&fp=chrome", `{"enabled":true,"utls":{"enabled":false,"fingerprint":"chrome"}}`),
		},
		{
			name: "Reality key mismatch",
			link: withTLS("vless://id@example.com:443?security=reality&pbk=standard", `{"enabled":true,"reality":{"enabled":true,"public_key":"native"}}`),
		},
		{
			name: "ECH mismatch",
			link: withTLS("vless://id@example.com:443?security=tls&ech="+url.QueryEscape(protocolAuditECHConfigListBase64), string(mismatchedECHTLS)),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := ParseShareLink(test.link); err == nil || !strings.Contains(err.Error(), "conflict") {
				t.Fatalf("conflicting TLS representations were not rejected: err=%v link=%s", err, test.link)
			}
		})
	}
}

func TestShareLinkAuditTLSInferenceSurvivesPersistenceAndBuild(t *testing.T) {
	type testCase struct {
		name       string
		link       string
		proxyType  string
		outbound   string
		validate   bool
		assertMode func(t *testing.T, parsed interface{})
		assertTLS  func(t *testing.T, tls map[string]interface{}, outbound map[string]interface{})
	}

	encodeTLS := func(raw string) string {
		return url.QueryEscape(raw)
	}
	tests := []testCase{
		{
			name:      "vless infers native TLS",
			link:      "vless://id@example.com:443?tls_options=" + encodeTLS(`{"enabled":true,"server_name":"native.example","min_version":"1.2"}`),
			proxyType: "vless",
			outbound:  "vless",
			assertMode: func(t *testing.T, parsed interface{}) {
				config := parsed.(models.VLESSConfig)
				if config.Security != "tls" || config.SNI != "native.example" {
					t.Fatalf("native TLS mode was not inferred: %#v", config)
				}
			},
			assertTLS: func(t *testing.T, tls map[string]interface{}, _ map[string]interface{}) {
				if tls["enabled"] != true || tls["server_name"] != "native.example" || tls["min_version"] != "1.2" {
					t.Fatalf("persisted VLESS TLS mismatch: %#v", tls)
				}
			},
		},
		{
			name: "vmess infers native Reality",
			link: "vmess://00000000-0000-0000-0000-000000000110@example.com:443?encryption=auto&tls_options=" + encodeTLS(
				`{"enabled":true,"reality":{"enabled":true,"public_key":"`+protocolAuditRealityPublicKey+`","short_id":"01"}}`,
			),
			proxyType: "vmess",
			outbound:  "vmess",
			validate:  true,
			assertMode: func(t *testing.T, parsed interface{}) {
				config := parsed.(models.VMESSConfig)
				if config.TLS != "reality" {
					t.Fatalf("native Reality mode was not inferred: %#v", config)
				}
			},
			assertTLS: func(t *testing.T, tls map[string]interface{}, _ map[string]interface{}) {
				reality := nativeMap(tls["reality"])
				if tls["enabled"] != true || reality["enabled"] != true || reality["public_key"] != protocolAuditRealityPublicKey {
					t.Fatalf("persisted VMess Reality mismatch: %#v", tls)
				}
			},
		},
		{
			name:      "trojan native disabled TLS",
			link:      "trojan://pass@example.com:443?tls_options=" + encodeTLS(`{"enabled":false,"server_name":"dormant.example"}`),
			proxyType: "trojan",
			outbound:  "trojan",
			assertMode: func(t *testing.T, parsed interface{}) {
				config := parsed.(models.TrojanConfig)
				if config.Security != "none" {
					t.Fatalf("native disabled TLS was not respected: %#v", config)
				}
			},
			assertTLS: func(t *testing.T, tls map[string]interface{}, outbound map[string]interface{}) {
				if tls != nil {
					t.Fatalf("disabled Trojan TLS unexpectedly generated: %#v", outbound)
				}
			},
		},
		{
			name:      "hysteria2 fills required enabled flag",
			link:      "hysteria2://pass@example.com:443?tls_options=" + encodeTLS(`{"server_name":"native.example","min_version":"1.2"}`),
			proxyType: "hy2",
			outbound:  "hysteria2",
			assertTLS: func(t *testing.T, tls map[string]interface{}, _ map[string]interface{}) {
				if tls["enabled"] != true || tls["server_name"] != "native.example" || tls["min_version"] != "1.2" {
					t.Fatalf("required Hysteria2 TLS mismatch: %#v", tls)
				}
			},
		},
		{
			name:      "https fills scheme selected enabled flag",
			link:      "https://example.com:443?proxy=1&tls_options=" + encodeTLS(`{"server_name":"native.example","record_fragment":true}`),
			proxyType: "http",
			outbound:  "http",
			assertTLS: func(t *testing.T, tls map[string]interface{}, _ map[string]interface{}) {
				if tls["enabled"] != true || tls["server_name"] != "native.example" || tls["record_fragment"] != true {
					t.Fatalf("HTTPS TLS mismatch: %#v", tls)
				}
			},
		},
		{
			name: "explicit standard fields merge with advanced native options",
			link: "vless://id@example.com:443?security=tls&sni=standard.example&insecure=0&alpn=h2&fp=chrome&ech=" + url.QueryEscape(protocolAuditECHConfigListBase64) + "&tls_options=" + encodeTLS(
				`{"min_version":"1.2"}`,
			),
			proxyType: "vless",
			outbound:  "vless",
			assertTLS: func(t *testing.T, tls map[string]interface{}, _ map[string]interface{}) {
				utls := nativeMap(tls["utls"])
				ech := nativeMap(tls["ech"])
				if tls["enabled"] != true || tls["server_name"] != "standard.example" || tls["insecure"] != false || tls["min_version"] != "1.2" {
					t.Fatalf("standard/native TLS merge mismatch: %#v", tls)
				}
				if utls["enabled"] != true || utls["fingerprint"] != "chrome" || ech["enabled"] != true {
					t.Fatalf("nested TLS merge mismatch: %#v", tls)
				}
			},
		},
	}

	service := NewSingBoxService(t.TempDir())
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, proxyType, name, err := ParseShareLink(test.link)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			if proxyType != test.proxyType {
				t.Fatalf("proxy type=%q, want %q", proxyType, test.proxyType)
			}
			if test.assertMode != nil {
				test.assertMode(t, parsed)
			}

			node := nativeTestNode(t, 100+index, name, proxyType, parsed)
			configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{node})
			if err != nil {
				t.Fatalf("BuildGlobalConfig: %v", err)
			}
			var document map[string]interface{}
			if err := json.Unmarshal([]byte(configJSON), &document); err != nil {
				t.Fatalf("decode global config: %v", err)
			}
			var outbound map[string]interface{}
			for _, candidate := range document["outbounds"].([]interface{}) {
				item := candidate.(map[string]interface{})
				if item["type"] == test.outbound {
					outbound = item
					break
				}
			}
			if outbound == nil {
				t.Fatalf("generated outbound %q not found: %s", test.outbound, configJSON)
			}
			tls, _ := outbound["tls"].(map[string]interface{})
			test.assertTLS(t, tls, outbound)
			if test.validate {
				if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
					t.Setenv("SINGBOX_BINARY", realBinary)
					if err := service.ValidateConfig(configJSON); err != nil {
						t.Fatalf("sing-box rejected persisted %s config: %v\n%s", test.name, err, configJSON)
					}
				}
			}
		})
	}
}

func TestShareLinkAuditAnyTLSKeepsExplicitIPServerName(t *testing.T) {
	nativeTLS := `{"enabled":true,"server_name":"1.2.3.4","disable_sni":true}`
	links := []struct {
		link           string
		wantDisableSNI bool
	}{
		{link: "anytls://pass@example.com:443?sni=1.2.3.4", wantDisableSNI: true},
		{link: "anytls://pass@example.com:443?sni=1.2.3.4&disable_sni=1&tls_options=" + url.QueryEscape(nativeTLS), wantDisableSNI: true},
		{link: "anytls://pass@example.com:443?sni=1.2.3.4&disable_sni=0", wantDisableSNI: false},
		{
			link: "anytls://pass@example.com:443?sni=1.2.3.4&disable_sni=0&tls_options=" + url.QueryEscape(
				`{"enabled":true,"server_name":"1.2.3.4","disable_sni":false}`,
			),
			wantDisableSNI: false,
		},
	}
	for _, test := range links {
		parsed, proxyType, _, err := ParseShareLink(test.link)
		if err != nil {
			t.Fatalf("ParseShareLink(%s): %v", test.link, err)
		}
		config := parsed.(models.AnyTLSConfig)
		if proxyType != "anytls" || config.SNI != "1.2.3.4" {
			t.Fatalf("explicit IP server_name was not preserved: %#v", config)
		}
		if config.TLSOptions["server_name"] != "1.2.3.4" || config.TLSOptions["disable_sni"] != test.wantDisableSNI {
			t.Fatalf("explicit IP TLS options mismatch: %#v", config.TLSOptions)
		}
	}

	original := &models.AnyTLSConfig{
		Server: "example.com", ServerPort: 443, Password: "pass",
		TLSOptions: models.NativeOptions{
			"enabled": true, "server_name": "1.2.3.4", "disable_sni": true,
		},
	}
	link, err := buildAnyTLSShareLink("ip-sni", original)
	if err != nil {
		t.Fatalf("build AnyTLS share link: %v", err)
	}
	parsed, proxyType, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("round-trip AnyTLS share link: %v\n%s", err, link)
	}
	config := parsed.(models.AnyTLSConfig)
	if proxyType != "anytls" || name != "ip-sni" || config.SNI != "1.2.3.4" || config.TLSOptions["disable_sni"] != true {
		t.Fatalf("AnyTLS IP SNI round-trip mismatch: %#v", config)
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		service := NewSingBoxService(t.TempDir())
		configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{
			nativeTestNode(t, 199, "anytls-ip-sni", "anytls", config),
		})
		if err != nil {
			t.Fatalf("BuildGlobalConfig: %v", err)
		}
		if err := service.ValidateConfig(configJSON); err != nil {
			t.Fatalf("sing-box rejected AnyTLS IP server_name: %v\n%s", err, configJSON)
		}
	}
}

func TestShareLinkAuditNormalizesMixedCaseUTLSFingerprint(t *testing.T) {
	tests := []struct {
		name        string
		link        string
		proxyType   string
		want        string
		fingerprint func(interface{}) string
	}{
		{
			name: "vless matching native",
			link: "vless://00000000-0000-0000-0000-000000000201@example.com:443?security=tls&fp=Chrome&tls_options=" + url.QueryEscape(
				`{"enabled":true,"utls":{"enabled":true,"fingerprint":"chrome"}}`,
			),
			proxyType: "vless",
			want:      "chrome",
			fingerprint: func(value interface{}) string {
				return value.(models.VLESSConfig).Fingerprint
			},
		},
		{
			name:      "trojan flat",
			link:      "trojan://pass@example.com:443?security=tls&fp=Firefox",
			proxyType: "trojan",
			want:      "firefox",
			fingerprint: func(value interface{}) string {
				return value.(models.TrojanConfig).Fingerprint
			},
		},
		{
			name: "anytls matching native",
			link: "anytls://pass@example.com:443?fp=Safari&tls_options=" + url.QueryEscape(
				`{"enabled":true,"utls":{"enabled":true,"fingerprint":"safari"}}`,
			),
			proxyType: "anytls",
			want:      "safari",
			fingerprint: func(value interface{}) string {
				return value.(models.AnyTLSConfig).Fingerprint
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parsed, proxyType, name, err := ParseShareLink(test.link)
			if err != nil {
				t.Fatalf("ParseShareLink: %v", err)
			}
			if proxyType != test.proxyType || test.fingerprint(parsed) != test.want {
				t.Fatalf("fingerprint was not normalized: type=%s config=%#v", proxyType, parsed)
			}

			if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
				t.Setenv("SINGBOX_BINARY", realBinary)
				service := NewSingBoxService(t.TempDir())
				configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{
					nativeTestNode(t, 210+index, name, proxyType, parsed),
				})
				if err != nil {
					t.Fatalf("BuildGlobalConfig: %v", err)
				}
				if err := service.ValidateConfig(configJSON); err != nil {
					t.Fatalf("sing-box rejected normalized fingerprint: %v\n%s", err, configJSON)
				}
			}
		})
	}
}

func TestShareLinkAuditECHConfigListRoundTrip(t *testing.T) {
	echLines := protocolAuditECHConfigPEMLines()
	trailingPlusConfigList, err := decodeShareECHConfigList(protocolAuditECHConfigListBase64)
	if err != nil {
		t.Fatalf("decode trailing-plus ECH fixture: %v", err)
	}
	trailingPlusConfigList = append(trailingPlusConfigList, 0xff, 0xff, 0x00, 0x02, 0x00, 0x3e)
	binary.BigEndian.PutUint16(trailingPlusConfigList[:2], uint16(len(trailingPlusConfigList)-2))
	trailingPlusBase64 := base64.StdEncoding.EncodeToString(trailingPlusConfigList)
	if !strings.HasSuffix(trailingPlusBase64, "+") {
		t.Fatalf("trailing-plus ECH fixture does not end in +: %q", trailingPlusBase64)
	}
	trailingPlusPEM := strings.Join(encodeNativeECHConfigList(trailingPlusConfigList), "\n")
	nativeTLS := models.NativeOptions{
		"enabled": true,
		"ech": map[string]interface{}{
			"enabled": true,
			"config":  append([]string(nil), echLines...),
		},
	}
	rawTLS, err := json.Marshal(nativeTLS)
	if err != nil {
		t.Fatalf("marshal ECH TLS options: %v", err)
	}

	legacyVMessJSON, err := json.Marshal(map[string]interface{}{
		"v": "2", "ps": "vmess-flat-ech", "add": "example.com", "port": 443,
		"id": "00000000-0000-0000-0000-000000000225", "aid": 0, "scy": "auto",
		"net": "tcp", "type": "none", "tls": "tls", "sni": "example.com",
		"ech": protocolAuditECHConfigListBase64,
	})
	if err != nil {
		t.Fatalf("marshal VMess ECH fixture: %v", err)
	}

	base := "vless://00000000-0000-0000-0000-000000000220@example.com:443?security=tls"
	links := []struct {
		name      string
		proxyType string
		link      string
		wantPEM   string
	}{
		{name: "vless-native-only", proxyType: "vless", link: base + "&tls_options=" + url.QueryEscape(string(rawTLS))},
		{name: "vless-flat-base64", proxyType: "vless", link: base + "&ech=" + url.QueryEscape(protocolAuditECHConfigListBase64)},
		{name: "vless-flat-pem-compatibility", proxyType: "vless", link: base + "&ech=" + url.QueryEscape(protocolAuditECHConfigPEM)},
		{name: "vless-equivalent-flat-native", proxyType: "vless", link: base + "&ech=" + url.QueryEscape(protocolAuditECHConfigListBase64) + "&tls_options=" + url.QueryEscape(string(rawTLS))},
		{name: "hysteria2-flat-base64", proxyType: "hy2", link: "hysteria2://pass@example.com:443?ech=" + url.QueryEscape(protocolAuditECHConfigListBase64)},
		{name: "hysteria2-flat-unescaped-plus", proxyType: "hy2", link: "hysteria2://pass@example.com:443?ech=" + protocolAuditECHConfigListBase64},
		{name: "hysteria2-flat-unescaped-trailing-plus", proxyType: "hy2", link: "hysteria2://pass@example.com:443?ech=" + trailingPlusBase64, wantPEM: trailingPlusPEM},
		{name: "vmess-legacy-flat-base64", proxyType: "vmess", link: "vmess://" + base64.RawURLEncoding.EncodeToString(legacyVMessJSON)},
	}

	tlsOptions := func(t *testing.T, parsed interface{}) models.NativeOptions {
		t.Helper()
		switch config := parsed.(type) {
		case models.VLESSConfig:
			return config.TLSOptions
		case models.Hysteria2Config:
			return config.TLSOptions
		case models.VMESSConfig:
			return config.TLSOptions
		default:
			t.Fatalf("unexpected ECH fixture config type: %T", parsed)
			return nil
		}
	}

	nodes := make([]models.ProxyNode, 0, len(links)+2)
	for index, test := range links {
		parsed, proxyType, _, err := ParseShareLink(test.link)
		if err != nil {
			t.Fatalf("parse %s: %v\n%s", test.name, err, test.link)
		}
		if proxyType != test.proxyType {
			t.Fatalf("%s proxy type=%q, want %q", test.name, proxyType, test.proxyType)
		}
		options := tlsOptions(t, parsed)
		configs := nativeStringSlice(nativeMap(options["ech"])["config"])
		wantPEM := test.wantPEM
		if wantPEM == "" {
			wantPEM = protocolAuditECHConfigPEM
		}
		if strings.Join(configs, "\n") != wantPEM {
			t.Fatalf("%s did not normalize to the native PEM line list: %#v", test.name, options)
		}
		nodes = append(nodes, nativeTestNode(t, 230+index, test.name, proxyType, parsed))
	}

	exported, err := buildVLESSShareLink("ech-lines", &models.VLESSConfig{
		Server: "example.com", ServerPort: 443,
		UUID: "00000000-0000-0000-0000-000000000221", Security: "tls",
		TLSOptions: nativeTLS,
	})
	if err != nil {
		t.Fatalf("build ECH VLESS share link: %v", err)
	}
	exportedURL, err := url.Parse(exported)
	if err != nil {
		t.Fatalf("parse exported ECH URL: %v", err)
	}
	query := exportedURL.Query()
	if query.Get("ech") != protocolAuditECHConfigListBase64 {
		t.Fatalf("native ECH was not exported as the standard base64 config list: %q", query.Get("ech"))
	}
	query.Del("tls_options")
	exportedURL.RawQuery = query.Encode()
	standardOnly, _, _, err := ParseShareLink(exportedURL.String())
	if err != nil {
		t.Fatalf("parse standard-only exported ECH link: %v", err)
	}
	if configs := nativeStringSlice(nativeMap(standardOnly.(models.VLESSConfig).TLSOptions["ech"])["config"]); strings.Join(configs, "\n") != protocolAuditECHConfigPEM {
		t.Fatalf("standard-only export did not restore native PEM: %#v", standardOnly)
	}
	nodes = append(nodes, nativeTestNode(t, 238, "vless-exported-flat-ech", "vless", standardOnly))

	vmessLink, err := buildVMESSShareLink("ech-legacy", &models.VMESSConfig{
		Server: "example.com", ServerPort: 443,
		UUID: "00000000-0000-0000-0000-000000000222", Security: "auto", TLS: "tls",
		TLSOptions: nativeTLS,
	})
	if err != nil {
		t.Fatalf("build VMess ECH share link: %v", err)
	}
	if strings.Contains(strings.TrimPrefix(vmessLink, "vmess://"), "@") {
		t.Fatalf("representable ECH config should retain VMess legacy form: %s", vmessLink)
	}
	exportedVMessJSON, err := decodeBase64String(strings.TrimPrefix(vmessLink, "vmess://"))
	if err != nil {
		t.Fatalf("decode exported VMess ECH link: %v", err)
	}
	var exportedVMessFields map[string]interface{}
	if err := json.Unmarshal(exportedVMessJSON, &exportedVMessFields); err != nil {
		t.Fatalf("decode exported VMess ECH JSON: %v", err)
	}
	if exportedVMessFields["ech"] != protocolAuditECHConfigListBase64 {
		t.Fatalf("VMess legacy ECH was not exported as base64: %#v", exportedVMessFields["ech"])
	}
	parsedVMess, _, _, err := ParseShareLink(vmessLink)
	if err != nil {
		t.Fatalf("parse VMess legacy ECH link: %v", err)
	}
	if configs := nativeStringSlice(nativeMap(parsedVMess.(models.VMESSConfig).TLSOptions["ech"])["config"]); strings.Join(configs, "\n") != protocolAuditECHConfigPEM {
		t.Fatalf("VMess legacy ECH did not restore native PEM: %#v", parsedVMess)
	}
	nodes = append(nodes, nativeTestNode(t, 239, "vmess-exported-ech", "vmess", parsedVMess))

	service := NewSingBoxService(t.TempDir())
	configJSON, err := service.BuildGlobalConfig(nodes)
	if err != nil {
		t.Fatalf("BuildGlobalConfig ECH round-trip: %v", err)
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		if err := service.ValidateConfig(configJSON); err != nil {
			t.Fatalf("sing-box rejected normalized ECH config lists: %v\n%s", err, configJSON)
		}
	}
}

func TestShareLinkAuditRejectsRealityWithECH(t *testing.T) {
	nativeRealityECH, err := json.Marshal(models.NativeOptions{
		"enabled": true,
		"reality": map[string]interface{}{
			"enabled": true, "public_key": "public-key",
		},
		"ech": map[string]interface{}{
			"enabled": true, "config": protocolAuditECHConfigPEMLines(),
		},
	})
	if err != nil {
		t.Fatalf("marshal native Reality/ECH fixture: %v", err)
	}
	legacyVMess, err := json.Marshal(map[string]interface{}{
		"v": "2", "add": "example.com", "port": 443,
		"id": "00000000-0000-0000-0000-000000000240", "aid": 0, "scy": "auto",
		"net": "tcp", "type": "none", "tls": "reality", "pbk": "public-key",
		"ech": protocolAuditECHConfigListBase64,
	})
	if err != nil {
		t.Fatalf("marshal legacy VMess Reality/ECH fixture: %v", err)
	}

	tests := []struct {
		name string
		link string
	}{
		{
			name: "VLESS standard fields",
			link: "vless://00000000-0000-0000-0000-000000000240@example.com:443?security=reality&pbk=public-key&ech=" + url.QueryEscape(protocolAuditECHConfigListBase64),
		},
		{
			name: "Trojan standard fields",
			link: "trojan://password@example.com:443?security=reality&pbk=public-key&ech=" + url.QueryEscape(protocolAuditECHConfigListBase64),
		},
		{
			name: "native TLS fields",
			link: "vless://00000000-0000-0000-0000-000000000241@example.com:443?tls_options=" + url.QueryEscape(string(nativeRealityECH)),
		},
		{
			name: "VMess legacy JSON",
			link: "vmess://" + base64.RawURLEncoding.EncodeToString(legacyVMess),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := ParseShareLink(test.link); err == nil || !strings.Contains(err.Error(), "incompatible with ECH") {
				t.Fatalf("Reality/ECH combination was not rejected: err=%v\n%s", err, test.link)
			}
		})
	}
}

func TestShareLinkAuditFragmentSemantics(t *testing.T) {
	_, _, name, err := ParseShareLink("vless://id@example.com:443?security=none#A+B%20C")
	if err != nil || name != "A+B C" {
		t.Fatalf("fragment plus decoding: name=%q err=%v", name, err)
	}
	_, _, name, err = ParseShareLink("vless://id@example.com:443?security=none#A%2520B")
	if err != nil || name != "A%20B" {
		t.Fatalf("fragment double decoding: name=%q err=%v", name, err)
	}

	cfg := &models.VLESSConfig{Server: "example.com", ServerPort: 443, UUID: "id", Security: "none"}
	link, err := buildVLESSShareLink("A+B C%20", cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	_, _, name, err = ParseShareLink(link)
	if err != nil || name != "A+B C%20" {
		t.Fatalf("self round-trip: link=%q name=%q err=%v", link, name, err)
	}
}

func TestShareLinkAuditWireGuardNativeRoundTrip(t *testing.T) {
	cfg := &models.WireGuardConfig{
		Server: "wg.example", ServerPort: 51820, LocalAddress: []string{"10.0.0.2/32"}, PrivateKey: "private",
		PeerPublicKey: "public", ListenPort: 51821, UDPTimeout: "45s",
		DomainResolver: map[string]interface{}{"server": "local", "strategy": "prefer_ipv4"},
		Peers:          []models.WireGuardPeerConfig{{Server: "wg.example", ServerPort: 51820, PublicKey: "public", PersistentKeepaliveInterval: 25}},
	}
	link, err := buildWireGuardShareLink("WG+A %20", cfg)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	parsedAny, _, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	parsed := parsedAny.(models.WireGuardConfig)
	if name != "WG+A %20" || parsed.ListenPort != 51821 || parsed.UDPTimeout != "45s" || len(parsed.Peers) != 1 || parsed.Peers[0].PersistentKeepaliveInterval != 25 {
		t.Fatalf("round-trip mismatch: %#v name=%q", parsed, name)
	}
	if parsed.DomainResolverOptions["server"] != "local" || parsed.DomainResolverOptions["strategy"] != "prefer_ipv4" {
		t.Fatalf("domain resolver object lost: %#v", parsed.DomainResolverOptions)
	}
}

func TestShareLinkAuditWireGuardKeepaliveDefaultsAllowedIPs(t *testing.T) {
	link := "wireguard://" + url.User(testWireGuardPrivateKey).String() +
		"@127.0.0.1:51820?ip=10.0.0.2%2F32&publickey=" + url.QueryEscape(testWireGuardPeerPublicKey) +
		"&persistent_keepalive_interval=25#keepalive"
	parsed, proxyType, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse WireGuard keepalive link: %v", err)
	}
	config := parsed.(models.WireGuardConfig)
	if len(config.Peers) != 1 || config.Peers[0].PersistentKeepaliveInterval != 25 {
		t.Fatalf("WireGuard keepalive peer was not retained: %#v", config.Peers)
	}

	service := NewSingBoxService(t.TempDir())
	endpoint, err := service.generateWireGuardEndpoint(&config, "wireguard-keepalive")
	if err != nil {
		t.Fatalf("generate WireGuard keepalive endpoint: %v", err)
	}
	peers := endpoint.Extra["peers"].([]map[string]interface{})
	allowedIPs, _ := peers[0]["allowed_ips"].([]string)
	if len(allowedIPs) != 2 || allowedIPs[0] != "0.0.0.0/0" || allowedIPs[1] != "::/0" {
		t.Fatalf("WireGuard keepalive peer did not receive default routes: %#v", peers[0])
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		configJSON, buildErr := service.BuildGlobalConfig([]models.ProxyNode{
			nativeTestNode(t, 330, name, proxyType, parsed),
		})
		if buildErr != nil {
			t.Fatalf("BuildGlobalConfig: %v", buildErr)
		}
		if validateErr := service.ValidateConfig(configJSON); validateErr != nil {
			t.Fatalf("sing-box rejected WireGuard keepalive config: %v\n%s", validateErr, configJSON)
		}
	}
}

func TestShareLinkAuditWireGuardResolverExportPrecedence(t *testing.T) {
	baseConfig := func() *models.WireGuardConfig {
		return &models.WireGuardConfig{
			Server:         "wg.example",
			ServerPort:     51820,
			LocalAddress:   []string{"10.0.0.2/32"},
			PrivateKey:     "private",
			PeerPublicKey:  "public",
			AllowedIPs:     []string{"0.0.0.0/0"},
			DomainResolver: "local",
		}
	}

	t.Run("flat values override stale compatibility options", func(t *testing.T) {
		cfg := baseConfig()
		cfg.DomainResolverStrategy = "prefer_ipv4"
		cfg.DomainResolverOptions = models.NativeOptions{
			"server":        "stale-resolver",
			"strategy":      "prefer_ipv6",
			"client_subnet": "192.0.2.0/24",
		}

		link, err := buildWireGuardShareLink("resolver", cfg)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		parsedURL, err := url.Parse(link)
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		query := parsedURL.Query()
		if query.Get("domain_resolver") != "" || query.Get("domain_resolver_strategy") != "" {
			t.Fatalf("resolver must have one unambiguous object representation: %s", link)
		}
		var options map[string]any
		if err := json.Unmarshal([]byte(query.Get("domain_resolver_options")), &options); err != nil {
			t.Fatalf("decode resolver options: %v", err)
		}
		if options["server"] != "local" || options["strategy"] != "prefer_ipv4" || options["client_subnet"] != "192.0.2.0/24" {
			t.Fatalf("unexpected resolver precedence: %#v", options)
		}

		parsedAny, _, _, err := ParseShareLink(link)
		if err != nil {
			t.Fatalf("round-trip parse: %v", err)
		}
		parsedConfig := parsedAny.(models.WireGuardConfig)
		resolved, err := wireGuardDomainResolverValue(&parsedConfig)
		if err != nil {
			t.Fatalf("round-trip resolve: %v", err)
		}
		resolvedMap, ok := generatorOptionMap(resolved)
		if !ok || resolvedMap["server"] != "local" || resolvedMap["strategy"] != "prefer_ipv4" {
			t.Fatalf("round-trip resolver mismatch: %#v", resolved)
		}
	})

	t.Run("explicit empty flat value clears stale options", func(t *testing.T) {
		cfg := baseConfig()
		cfg.DomainResolver = ""
		cfg.DomainResolverStrategy = "prefer_ipv4"
		cfg.DomainResolverOptions = models.NativeOptions{"server": "stale-resolver", "strategy": "prefer_ipv6"}

		link, err := buildWireGuardShareLink("resolver-clear", cfg)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		parsedURL, err := url.Parse(link)
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		query := parsedURL.Query()
		if query.Get("domain_resolver") != "" || query.Get("domain_resolver_options") != "" || query.Get("domain_resolver_strategy") != "" {
			t.Fatalf("cleared resolver leaked stale options: %s", link)
		}
	})
}

func TestShareLinkAuditRejectsUnsupportedNativeGaps(t *testing.T) {
	for _, link := range []string{
		"vless://id@example.com:443?security=none&network=icmp",
		"trojan://pass@example.com:443?security=tls&network=icmp",
		"vless://id@example.com:443?security=reality&pbk=pub&spx=%2F",
		"vless://id@example.com:443?security=reality&pbk=pub&pqv=post-quantum-verify",
		"vmess://id@example.com:443?security=tls&pcs=certificate-hash",
		"trojan://pass@example.com:443?security=tls&vcn=certificate.example",
		"vless://id@example.com:443?security=none&fm=%7B%22udp%22%3A%5B%5D%7D",
		"hysteria2://pass@example.com:443?pcs=certificate-hash",
		"tuic://id:pass@example.com:443?vcn=certificate.example",
		"anytls://pass@example.com:443?pcs=certificate-hash",
		"vless://id@example.com:443?security=tls&type=grpc&serviceName=service&authority=authority.example",
		"vmess://id@example.com:443?security=tls&type=grpc&serviceName=service&mode=multi",
		"trojan://pass@example.com:443?security=tls&type=quic&quicSecurity=aes-128-gcm&key=secret",
		"vless://id@example.com:443?security=tls&type=quic&header-type=srtp",
		"vmess://id@example.com:443?security=tls&type=grpc&serviceName=service&user_agent=custom-agent",
	} {
		if _, _, _, err := ParseShareLink(link); err == nil {
			t.Fatalf("unsupported option accepted: %s", link)
		}
	}

	for _, rawTLS := range []string{
		`{"enabled":true,"certificate_public_key_sha256":["pin"]}`,
		`{"enabled":true,"reality":{"enabled":true,"public_key":"public","spider_x":"/"}}`,
		`{"enabled":true,"reality":{"enabled":true,"public_key":"public","mldsa65Verify":"verify"}}`,
	} {
		link := "vless://id@example.com:443?security=tls&tls_options=" + url.QueryEscape(rawTLS)
		if _, _, _, err := ParseShareLink(link); err == nil {
			t.Fatalf("unsupported native TLS options accepted: %s", rawTLS)
		}
	}

	for _, property := range []struct {
		key   string
		value any
	}{
		{key: "pqv", value: "post-quantum-verify"},
		{key: "spx", value: "/"},
		{key: "pcs", value: "certificate-hash"},
		{key: "vcn", value: "certificate.example"},
		{key: "fm", value: map[string]any{"udp": []any{}}},
		{key: "finalmask", value: map[string]any{"udp": []any{}}},
		{key: "type", value: "multi"},
	} {
		fixture := map[string]any{
			"v": "2", "ps": "unsupported", "add": "example.com", "port": "443",
			"id": "11111111-1111-1111-1111-111111111111", "aid": "0", "scy": "auto", "net": "ws",
		}
		if property.key == "type" {
			fixture["net"] = "grpc"
			fixture["path"] = "service"
		}
		fixture[property.key] = property.value
		raw, err := json.Marshal(fixture)
		if err != nil {
			t.Fatalf("marshal VMess fixture: %v", err)
		}
		link := "vmess://" + base64.RawURLEncoding.EncodeToString(raw)
		if _, _, _, err := ParseShareLink(link); err == nil {
			t.Fatalf("unsupported VMess JSON property accepted: %s", property.key)
		}
	}
}

func TestShareLinkAuditJSONFixtureValidity(t *testing.T) {
	// Guard accidental invalid literals in VMess table fixtures.
	var value map[string]interface{}
	if err := json.Unmarshal([]byte(`{"add":"example.com"}`), &value); err != nil {
		t.Fatal(err)
	}
}
