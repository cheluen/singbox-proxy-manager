package services

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"sb-proxy/backend/models"
)

func TestGeneratorNativeOnlyTLSCompatibilityAcrossProtocols(t *testing.T) {
	nativeTLS := func() models.NativeOptions {
		return models.NativeOptions{
			"enabled": true, "server_name": "native.example", "insecure": true,
			"alpn":        []interface{}{"h2"},
			"utls":        map[string]interface{}{"enabled": true, "fingerprint": "firefox"},
			"min_version": "1.2",
		}
	}
	service := &SingBoxService{}
	tests := []struct {
		name     string
		generate func() (OutboundConfig, error)
	}{
		{
			name: "vless",
			generate: func() (OutboundConfig, error) {
				return service.generateVLESSOutbound(&models.VLESSConfig{
					Server: "127.0.0.1", ServerPort: 443,
					UUID: "00000000-0000-0000-0000-000000000101", TLSOptions: nativeTLS(),
				}, "test")
			},
		},
		{
			name: "vmess",
			generate: func() (OutboundConfig, error) {
				return service.generateVMESSOutbound(&models.VMESSConfig{
					Server: "127.0.0.1", ServerPort: 443,
					UUID: "00000000-0000-0000-0000-000000000102", Security: "auto", TLSOptions: nativeTLS(),
				}, "test")
			},
		},
		{
			name: "trojan",
			generate: func() (OutboundConfig, error) {
				return service.generateTrojanOutbound(&models.TrojanConfig{
					Server: "127.0.0.1", ServerPort: 443, Password: "secret", TLSOptions: nativeTLS(),
				}, "test")
			},
		},
		{
			name: "hysteria2",
			generate: func() (OutboundConfig, error) {
				return service.generateHysteria2Outbound(&models.Hysteria2Config{
					Server: "127.0.0.1", ServerPort: 443, Password: "secret", TLSOptions: nativeTLS(),
				}, "test")
			},
		},
		{
			name: "tuic",
			generate: func() (OutboundConfig, error) {
				return service.generateTUICOutbound(&models.TUICConfig{
					Server: "127.0.0.1", ServerPort: 443,
					UUID: "00000000-0000-0000-0000-000000000103", Password: "secret", TLSOptions: nativeTLS(),
				}, "test")
			},
		},
		{
			name: "anytls",
			generate: func() (OutboundConfig, error) {
				return service.generateAnyTLSOutbound(&models.AnyTLSConfig{
					Server: "127.0.0.1", ServerPort: 443, Password: "secret", TLSOptions: nativeTLS(),
				}, "test")
			},
		},
		{
			name: "http",
			generate: func() (OutboundConfig, error) {
				return service.generateHTTPProxyOutbound(&models.HTTPProxyConfig{
					Server: "127.0.0.1", ServerPort: 443, TLSOptions: nativeTLS(),
				}, "test")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outbound, err := test.generate()
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			tlsOptions, ok := outbound.Extra["tls"].(map[string]interface{})
			if !ok {
				t.Fatalf("native-only TLS was omitted: %#v", outbound.Extra)
			}
			if tlsOptions["server_name"] != "native.example" || tlsOptions["insecure"] != true || tlsOptions["min_version"] != "1.2" {
				t.Fatalf("native-only TLS fields lost: %#v", tlsOptions)
			}
			if got := nativeStringSlice(tlsOptions["alpn"]); len(got) != 1 || got[0] != "h2" {
				t.Fatalf("native-only ALPN lost: %#v", tlsOptions)
			}
			utls := nativeMap(tlsOptions["utls"])
			if utls["fingerprint"] != "firefox" {
				t.Fatalf("native-only uTLS lost: %#v", tlsOptions)
			}
		})
	}
}

func TestGeneratorVLESSRealityKeepsNativeOnlyCredentials(t *testing.T) {
	node := models.ProxyNode{
		ID: 41, Name: "native-reality", Type: "vless", InboundPort: 32041, Enabled: true,
		Config: `{
			"server":"127.0.0.1","server_port":443,
			"uuid":"00000000-0000-0000-0000-000000000141",
			"security":"reality",
			"tls_options":{
				"enabled":true,
				"server_name":"reality.example",
				"reality":{
					"enabled":true,
					"public_key":"jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0",
					"short_id":"0123456789abcdef"
				}
			}
		}`,
	}

	parsed, err := node.ParseConfig()
	if err != nil {
		t.Fatalf("parse native-only VLESS Reality credentials: %v", err)
	}
	outbound, err := (&SingBoxService{}).generateVLESSOutbound(parsed.(*models.VLESSConfig), "native-reality-out")
	if err != nil {
		t.Fatalf("generate native-only VLESS Reality credentials: %v", err)
	}
	reality := nativeMap(nativeMap(outbound.Extra["tls"])["reality"])
	if reality["public_key"] != "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0" || reality["short_id"] != "0123456789abcdef" {
		t.Fatalf("native-only VLESS Reality credentials were overwritten: %#v", reality)
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		service := NewSingBoxService(t.TempDir())
		configJSON, buildErr := service.BuildGlobalConfig([]models.ProxyNode{node})
		if buildErr != nil {
			t.Fatalf("build native-only VLESS Reality config: %v", buildErr)
		}
		if validateErr := service.ValidateConfig(configJSON); validateErr != nil {
			t.Fatalf("sing-box 1.12.12 rejected native-only VLESS Reality config: %v\n%s", validateErr, configJSON)
		}
	}
}

func TestGeneratorNativeOnlyV2RayTransportCompatibility(t *testing.T) {
	service := &SingBoxService{}
	tests := []struct {
		name       string
		network    string
		options    models.NativeOptions
		assertions func(t *testing.T, transport map[string]interface{})
	}{
		{
			name: "websocket", network: "ws",
			options: models.NativeOptions{
				"type": "ws", "path": "/native",
				"headers":        map[string]interface{}{"Host": "native.example", "X-Test": "keep"},
				"max_early_data": 2048, "early_data_header_name": "Sec-WebSocket-Protocol",
			},
			assertions: func(t *testing.T, transport map[string]interface{}) {
				if transport["path"] != "/native" || intFromAny(transport["max_early_data"]) != 2048 || transport["early_data_header_name"] != "Sec-WebSocket-Protocol" {
					t.Fatalf("native websocket fields lost: %#v", transport)
				}
				headers := nativeMap(transport["headers"])
				if headers["Host"] != "native.example" || headers["X-Test"] != "keep" {
					t.Fatalf("native websocket headers lost: %#v", transport)
				}
			},
		},
		{
			name: "http", network: "http",
			options: models.NativeOptions{
				"type": "http", "path": "/native", "host": []interface{}{"native.example"},
				"method": "POST", "headers": map[string]interface{}{"X-Test": "keep"},
				"idle_timeout": "10s",
			},
			assertions: func(t *testing.T, transport map[string]interface{}) {
				if transport["path"] != "/native" || transport["method"] != "POST" || transport["idle_timeout"] != "10s" {
					t.Fatalf("native HTTP fields lost: %#v", transport)
				}
			},
		},
		{
			name: "grpc", network: "grpc",
			options: models.NativeOptions{
				"type": "grpc", "service_name": "native-service", "idle_timeout": "10s", "permit_without_stream": true,
			},
			assertions: func(t *testing.T, transport map[string]interface{}) {
				if transport["service_name"] != "native-service" || transport["idle_timeout"] != "10s" || transport["permit_without_stream"] != true {
					t.Fatalf("native gRPC fields lost: %#v", transport)
				}
			},
		},
		{
			name: "httpupgrade", network: "httpupgrade",
			options: models.NativeOptions{
				"type": "httpupgrade", "path": "/native", "host": "native.example",
				"headers": map[string]interface{}{"X-Test": "keep"},
			},
			assertions: func(t *testing.T, transport map[string]interface{}) {
				if transport["path"] != "/native" || transport["host"] != "native.example" {
					t.Fatalf("native HTTPUpgrade fields lost: %#v", transport)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outbound, err := service.generateVLESSOutbound(&models.VLESSConfig{
				Server: "127.0.0.1", ServerPort: 443,
				UUID: "00000000-0000-0000-0000-000000000104", Security: "tls",
				Network: test.network, TransportOptions: test.options,
			}, "test")
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			transport := outbound.Extra["transport"].(map[string]interface{})
			test.assertions(t, transport)
		})
	}

	rawTLS := `{"enabled":true,"server_name":"native.example","alpn":["h2"],"insecure":true,"utls":{"enabled":true,"fingerprint":"firefox"}}`
	link := "vless://00000000-0000-0000-0000-000000000105@127.0.0.1:443?security=tls&tls_options=" + url.QueryEscape(rawTLS)
	parsed, _, _, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse native-only TLS link: %v", err)
	}
	outbound, err := service.generateVLESSOutbound(ptrVLESSConfig(parsed.(models.VLESSConfig)), "test")
	if err != nil {
		t.Fatalf("generate parsed native-only TLS link: %v", err)
	}
	if tlsOptions := outbound.Extra["tls"].(map[string]interface{}); tlsOptions["server_name"] != "native.example" {
		t.Fatalf("parsed native-only TLS was overwritten: %#v", tlsOptions)
	}
}

func ptrVLESSConfig(config models.VLESSConfig) *models.VLESSConfig {
	return &config
}

func nativeTestNode(t *testing.T, id int, name string, proxyType string, config interface{}) models.ProxyNode {
	t.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal %s config: %v", proxyType, err)
	}
	return models.ProxyNode{
		ID: id, Name: name, Type: proxyType, Config: string(encoded),
		InboundPort: 32000 + id, Enabled: true,
	}
}

func TestGeneratorFormFieldsOverrideAndClearNativeOptions(t *testing.T) {
	service := &SingBoxService{}
	config := &models.VLESSConfig{
		Server:     "127.0.0.1",
		ServerPort: 443,
		UUID:       "00000000-0000-0000-0000-000000000001",
		Security:   "tls",
		SNI:        "edited.example.com",
		Network:    "ws",
		Path:       "/edited",
		Host:       "edited-host.example.com",
		TLSOptions: models.NativeOptions{
			"enabled":     true,
			"server_name": "stale.example.com",
			"min_version": "1.2",
		},
		TransportOptions: models.NativeOptions{
			"type": "ws",
			"path": "/stale",
			"headers": map[string]interface{}{
				"Host":   "stale-host.example.com",
				"X-Test": "kept",
			},
		},
	}
	outbound, err := service.generateVLESSOutbound(config, "test")
	if err != nil {
		t.Fatalf("generateVLESSOutbound: %v", err)
	}
	tls := outbound.Extra["tls"].(map[string]interface{})
	if tls["server_name"] != "edited.example.com" || tls["min_version"] != "1.2" {
		t.Fatalf("form SNI must win while native-only TLS fields survive: %#v", tls)
	}
	transport := outbound.Extra["transport"].(map[string]interface{})
	if transport["path"] != "/edited" {
		t.Fatalf("form path must win while native-only transport fields survive: %#v", transport)
	}
	headers := transport["headers"].(map[string]interface{})
	if headers["Host"] != "edited-host.example.com" || headers["X-Test"] != "kept" {
		t.Fatalf("form Host must win while custom native headers survive: %#v", headers)
	}

	clearNode := models.ProxyNode{Type: "vless", Config: `{
		"server":"127.0.0.1","server_port":443,
		"uuid":"00000000-0000-0000-0000-000000000001",
		"security":"tls","sni":"edited.example.com","network":"ws",
		"path":"","host":"",
		"tls_options":{"enabled":true,"server_name":"edited.example.com","min_version":"1.2"},
		"transport_options":{"type":"ws","path":"/stale","headers":{"Host":"stale-host.example.com","X-Test":"kept"}}
	}`}
	parsedClear, err := clearNode.ParseConfig()
	if err != nil {
		t.Fatalf("parse VLESS clear fixture: %v", err)
	}
	clearConfig := parsedClear.(*models.VLESSConfig)
	outbound, err = service.generateVLESSOutbound(clearConfig, "test")
	if err != nil {
		t.Fatalf("generate VLESS with cleared websocket fields: %v", err)
	}
	transport = outbound.Extra["transport"].(map[string]interface{})
	if _, exists := transport["path"]; exists {
		t.Fatalf("cleared websocket path must not retain stale native value: %#v", transport)
	}
	headers = transport["headers"].(map[string]interface{})
	if _, exists := headers["Host"]; exists || headers["X-Test"] != "kept" {
		t.Fatalf("cleared websocket Host must be removed while custom headers survive: %#v", headers)
	}

	switchNode := models.ProxyNode{Type: "vless", Config: `{
		"server":"127.0.0.1","server_port":443,
		"uuid":"00000000-0000-0000-0000-000000000001",
		"security":"tls","network":"grpc","service_name":"",
		"transport_options":{"type":"ws","path":"/stale","headers":{"Host":"stale-host.example.com","X-Test":"kept"}}
	}`}
	parsedSwitch, err := switchNode.ParseConfig()
	if err != nil {
		t.Fatalf("parse VLESS transport-switch fixture: %v", err)
	}
	switchConfig := parsedSwitch.(*models.VLESSConfig)
	outbound, err = service.generateVLESSOutbound(switchConfig, "test")
	if err != nil {
		t.Fatalf("generate VLESS after transport switch: %v", err)
	}
	transport = outbound.Extra["transport"].(map[string]interface{})
	if transport["type"] != "grpc" {
		t.Fatalf("transport switch/clear must replace stale union arm: %#v", transport)
	}
	if _, exists := transport["service_name"]; exists {
		t.Fatalf("cleared grpc service_name must be omitted: %#v", transport)
	}
	if _, exists := transport["path"]; exists {
		t.Fatalf("stale websocket path leaked into grpc transport: %#v", transport)
	}

	clearAllNode := models.ProxyNode{Type: "vless", Config: `{
		"server":"127.0.0.1","server_port":443,
		"uuid":"00000000-0000-0000-0000-000000000001",
		"security":"none","network":"tcp",
		"tls_options":{"enabled":true,"server_name":"stale.example.com"},
		"transport_options":{"type":"ws","path":"/stale"}
	}`}
	parsedClearAll, err := clearAllNode.ParseConfig()
	if err != nil {
		t.Fatalf("parse VLESS TLS/transport clear fixture: %v", err)
	}
	outbound, err = service.generateVLESSOutbound(parsedClearAll.(*models.VLESSConfig), "test")
	if err != nil {
		t.Fatalf("generate cleared VLESS outbound: %v", err)
	}
	if _, exists := outbound.Extra["tls"]; exists {
		t.Fatalf("explicit security=none must clear imported tls_options: %#v", outbound.Extra["tls"])
	}
	if _, exists := outbound.Extra["transport"]; exists {
		t.Fatalf("explicit network=tcp must clear imported transport_options: %#v", outbound.Extra["transport"])
	}
}

func TestGeneratorDurationNormalizationUsesSchemaPaths(t *testing.T) {
	service := &SingBoxService{}
	outbound, err := service.generateVLESSOutbound(&models.VLESSConfig{
		Server:     "127.0.0.1",
		ServerPort: 80,
		UUID:       "00000000-0000-0000-0000-000000000005",
		Security:   "none",
		Network:    "http",
		Headers: map[string]string{
			"idle_timeout":    "opaque-header-value",
			"connect_timeout": "another-opaque-value",
		},
		TransportOptions: models.NativeOptions{
			"type":         "http",
			"idle_timeout": "7",
		},
	}, "test")
	if err != nil {
		t.Fatalf("generate VLESS HTTP transport: %v", err)
	}

	transport := outbound.Extra["transport"].(map[string]interface{})
	if transport["idle_timeout"] != "7s" {
		t.Fatalf("top-level transport duration must be normalized: %#v", transport)
	}
	headers := transport["headers"].(map[string]interface{})
	if headers["idle_timeout"] != "opaque-header-value" || headers["connect_timeout"] != "another-opaque-value" {
		t.Fatalf("HTTP header values that resemble duration field names must remain opaque: %#v", headers)
	}
}

func TestGeneratorHysteria2FormObfsOverridesNativeAndClears(t *testing.T) {
	config := &models.Hysteria2Config{
		Obfs: models.NativeOptions{
			"type":     "salamander",
			"password": "stale-native-password",
		},
		ObfsPassword: "edited-form-password",
	}
	obfs, err := generatorHysteria2Obfs(config)
	if err != nil {
		t.Fatalf("merge Hysteria2 obfs: %v", err)
	}
	if obfs["type"] != "salamander" || obfs["password"] != "edited-form-password" {
		t.Fatalf("flat form password must override stale native obfs: %#v", obfs)
	}

	config.Obfs = "salamander"
	config.ObfsPassword = ""
	config.SalamanderPassword = "stale-legacy-password"
	obfs, err = generatorHysteria2Obfs(config)
	if err != nil {
		t.Fatalf("clear Hysteria2 obfs password: %v", err)
	}
	if _, exists := obfs["password"]; exists {
		t.Fatalf("explicit form password clear must not resurrect a stale alias: %#v", obfs)
	}

	config.Obfs = ""
	obfs, err = generatorHysteria2Obfs(config)
	if err != nil {
		t.Fatalf("clear Hysteria2 obfs: %v", err)
	}
	if obfs != nil {
		t.Fatalf("explicit empty obfs type must clear stale compatibility data: %#v", obfs)
	}
}

func TestGeneratorUDPOverTCPExplicitValueOverridesCompatibility(t *testing.T) {
	stale := models.NativeOptions{"enabled": true, "version": float64(2)}

	value, err := resolveGeneratorUDPOverTCP(false, stale)
	if err != nil {
		t.Fatalf("disable UDP-over-TCP: %v", err)
	}
	if value != nil {
		t.Fatalf("explicit false must clear stale udp_over_tcp_options: %#v", value)
	}

	value, err = resolveGeneratorUDPOverTCP(true, stale)
	if err != nil {
		t.Fatalf("enable UDP-over-TCP: %v", err)
	}
	if value != true {
		t.Fatalf("explicit true must win over compatibility options: %#v", value)
	}

	value, err = resolveGeneratorUDPOverTCP(nil, stale)
	if err != nil {
		t.Fatalf("preserve UDP-over-TCP compatibility options: %v", err)
	}
	options, ok := value.(map[string]interface{})
	if !ok || options["enabled"] != true || options["version"] != float64(2) {
		t.Fatalf("nil flat value must preserve native compatibility options: %#v", value)
	}
}

func TestShareExportUsesSameCompatibilityPrecedence(t *testing.T) {
	staleUOT := models.NativeOptions{"enabled": true, "version": float64(2)}
	if enabled, version := exportUDPOverTCP(false, staleUOT); enabled || version != 0 {
		t.Fatalf("explicit UDP-over-TCP false must override stale export options: enabled=%v version=%d", enabled, version)
	}
	if enabled, version := exportUDPOverTCP(nil, staleUOT); !enabled || version != 2 {
		t.Fatalf("nil UDP-over-TCP value must preserve export compatibility: enabled=%v version=%d", enabled, version)
	}

	typeName, password := exportHysteria2Obfs(
		models.NativeOptions{"type": "salamander", "password": "stale-native-password"},
		"edited-form-password",
		"stale-legacy-password",
	)
	if typeName != "salamander" || password != "edited-form-password" {
		t.Fatalf("flat Hysteria2 password must win during export: type=%q password=%q", typeName, password)
	}

	typeName, password = exportHysteria2Obfs("salamander", "", "stale-legacy-password")
	if typeName != "salamander" || password != "" {
		t.Fatalf("form password clear must survive export: type=%q password=%q", typeName, password)
	}
	typeName, password = exportHysteria2Obfs(nil, "", "legacy-password")
	if typeName != "salamander" || password != "legacy-password" {
		t.Fatalf("legacy password-only Hysteria2 config must export as salamander: type=%q password=%q", typeName, password)
	}
}

func TestGeneratorTransportSwitchPrunesOnlyStaleUnionFields(t *testing.T) {
	service := &SingBoxService{}
	tests := []struct {
		name            string
		config          models.VLESSConfig
		wantPresent     []string
		wantAbsent      []string
		wantEmptyHeader string
	}{
		{
			name: "ws to grpc",
			config: models.VLESSConfig{
				Network: "grpc", ServiceName: "edited-service",
				TransportOptions: models.NativeOptions{
					"type": "ws", "path": "/stale", "headers": map[string]interface{}{"X-Stale": "value"},
					"max_early_data": float64(2048), "permit_without_stream": true, "idle_timeout": "4",
				},
			},
			wantPresent: []string{"service_name", "permit_without_stream", "idle_timeout"},
			wantAbsent:  []string{"path", "headers", "max_early_data"},
		},
		{
			name: "grpc to ws",
			config: models.VLESSConfig{
				Network: "ws", Path: "/edited", Headers: map[string]string{"X-Empty": ""},
				TransportOptions: models.NativeOptions{
					"type": "grpc", "service_name": "stale", "idle_timeout": "4", "ping_timeout": "2",
					"permit_without_stream": true,
				},
			},
			wantPresent:     []string{"path", "headers"},
			wantAbsent:      []string{"service_name", "idle_timeout", "ping_timeout", "permit_without_stream"},
			wantEmptyHeader: "X-Empty",
		},
		{
			name: "http to httpupgrade",
			config: models.VLESSConfig{
				Network: "httpupgrade", HTTPUpgradePath: "/edited", HTTPUpgradeHost: "edited.example.com",
				TransportOptions: models.NativeOptions{
					"type": "http", "path": "/stale", "host": []interface{}{"stale.example.com"},
					"method": "POST", "headers": map[string]interface{}{"X-Kept": "value"},
					"idle_timeout": "4", "ping_timeout": "2",
				},
			},
			wantPresent: []string{"path", "host", "headers"},
			wantAbsent:  []string{"method", "idle_timeout", "ping_timeout"},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			config := testCase.config
			config.Server = "127.0.0.1"
			config.ServerPort = 80
			config.UUID = "00000000-0000-0000-0000-000000000006"
			config.Security = "none"
			outbound, err := service.generateVLESSOutbound(&config, "test")
			if err != nil {
				t.Fatalf("generate switched transport: %v", err)
			}
			transport := outbound.Extra["transport"].(map[string]interface{})
			for _, key := range testCase.wantPresent {
				if _, exists := transport[key]; !exists {
					t.Fatalf("expected %q to survive in %#v", key, transport)
				}
			}
			for _, key := range testCase.wantAbsent {
				if _, exists := transport[key]; exists {
					t.Fatalf("stale union field %q survived in %#v", key, transport)
				}
			}
			if testCase.wantEmptyHeader != "" {
				headers := transport["headers"].(map[string]interface{})
				if value, exists := headers[testCase.wantEmptyHeader]; !exists || value != "" {
					t.Fatalf("empty opaque header must survive unchanged: %#v", headers)
				}
			}
		})
	}
}

func TestGeneratorProtocolNativeFieldsAndNormalization(t *testing.T) {
	service := &SingBoxService{}

	direct, err := service.generateDirectOutbound(&models.DirectConfig{
		DialerOptions: models.DialerOptions{BindInterface: "eth0", ConnectTimeout: "5"},
		ProxyProtocol: 2,
	}, "direct-test")
	if err != nil {
		t.Fatalf("direct: %v", err)
	}
	if direct.Extra["bind_interface"] != "eth0" || direct.Extra["connect_timeout"] != "5s" || direct.Extra["proxy_protocol"] != 2 {
		t.Fatalf("direct native fields missing: %#v", direct.Extra)
	}

	ss, err := service.generateSSOutbound(&models.SSConfig{
		Server: "127.0.0.1", ServerPort: 8388, Method: "aes-128-gcm", Password: "secret",
		Network:    models.ListableString{"tcp", "udp"},
		UDPOverTCP: map[string]interface{}{"enabled": true, "version": float64(2)},
	}, "ss-test")
	if err != nil {
		t.Fatalf("shadowsocks: %v", err)
	}
	if _, ok := ss.Extra["udp_over_tcp"].(map[string]interface{}); !ok {
		t.Fatalf("native udp_over_tcp object missing: %#v", ss.Extra)
	}

	hy2, err := service.generateHysteria2Outbound(&models.Hysteria2Config{
		Server: "127.0.0.1", ServerPort: 443, Password: "secret",
		ServerPorts: models.ListableString{"443", "8443", "20000:30000"},
		Network:     models.ListableString{"tcp", "udp"},
		Obfs:        map[string]interface{}{"type": "salamander", "password": "obfs"},
		HopInterval: "10", BrutalDebug: true, InsecureSkipVerify: true,
	}, "hy2-test")
	if err != nil {
		t.Fatalf("hysteria2: %v", err)
	}
	if hy2.Extra["hop_interval"] != "10s" || hy2.Extra["brutal_debug"] != true {
		t.Fatalf("hysteria2 fields missing: %#v", hy2.Extra)
	}
	if ports := hy2.Extra["server_ports"].([]string); len(ports) != 3 || ports[0] != "443:443" || ports[1] != "8443:8443" {
		t.Fatalf("hysteria2 mixed hopping ports were not normalized: %#v", ports)
	}
	if _, exists := hy2.Extra["salamander"]; exists {
		t.Fatalf("legacy invalid salamander top-level field must never be emitted")
	}

	tuic, err := service.generateTUICOutbound(&models.TUICConfig{
		Server: "127.0.0.1", ServerPort: 443,
		UUID: "00000000-0000-0000-0000-000000000002", Password: "secret",
		DisableSNI: true, ReduceRTT: true, UDPOverStream: true, Heartbeat: "3",
		InsecureSkipVerify: true,
	}, "tuic-test")
	if err != nil {
		t.Fatalf("tuic: %v", err)
	}
	if _, exists := tuic.Extra["disable_sni"]; exists {
		t.Fatalf("disable_sni belongs under tls: %#v", tuic.Extra)
	}
	if _, exists := tuic.Extra["reduce_rtt"]; exists {
		t.Fatalf("reduce_rtt is not a sing-box field: %#v", tuic.Extra)
	}
	if tuic.Extra["zero_rtt_handshake"] != true || tuic.Extra["heartbeat"] != "3s" {
		t.Fatalf("tuic compatibility mapping failed: %#v", tuic.Extra)
	}
	if tuic.Extra["tls"].(map[string]interface{})["disable_sni"] != true {
		t.Fatalf("tls.disable_sni missing: %#v", tuic.Extra["tls"])
	}

	trojan, err := service.generateTrojanOutbound(&models.TrojanConfig{
		Server: "127.0.0.1", ServerPort: 80, Password: "secret", Security: "none",
	}, "trojan-test")
	if err != nil {
		t.Fatalf("trojan without TLS: %v", err)
	}
	if _, exists := trojan.Extra["tls"]; exists {
		t.Fatalf("trojan security=none must not emit TLS: %#v", trojan.Extra)
	}
}

func TestGeneratorRealityForcesUTLSFingerprint(t *testing.T) {
	service := &SingBoxService{}
	outbound, err := service.generateVLESSOutbound(&models.VLESSConfig{
		Server: "127.0.0.1", ServerPort: 443,
		UUID:      "00000000-0000-0000-0000-000000000003",
		Security:  "reality",
		SNI:       "google.com",
		PublicKey: "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0",
		ShortID:   "0123456789abcdef",
	}, "reality-test")
	if err != nil {
		t.Fatalf("reality: %v", err)
	}
	utls := outbound.Extra["tls"].(map[string]interface{})["utls"].(map[string]interface{})
	if utls["enabled"] != true || utls["fingerprint"] != "chrome" {
		t.Fatalf("reality must force usable uTLS defaults: %#v", utls)
	}
}

func TestGeneratorRejectsKnownKernelInvalidValues(t *testing.T) {
	service := &SingBoxService{}
	base := models.VLESSConfig{
		Server: "127.0.0.1", ServerPort: 443,
		UUID: "00000000-0000-0000-0000-000000000004",
	}
	for _, testCase := range []struct {
		name      string
		mutate    func(*models.VLESSConfig)
		wantError string
	}{
		{name: "kcp", mutate: func(c *models.VLESSConfig) { c.Network = "kcp" }, wantError: "not supported"},
		{name: "quic without tls", mutate: func(c *models.VLESSConfig) { c.Network = "quic" }, wantError: "requires enabled TLS"},
		{name: "invalid packet encoding", mutate: func(c *models.VLESSConfig) { c.PacketEncoding = "invalid" }, wantError: "unsupported packet_encoding"},
		{name: "quic seed", mutate: func(c *models.VLESSConfig) { c.Network, c.Security, c.Seed = "quic", "tls", "seed" }, wantError: "seed is not supported"},
		{name: "unsupported vless encryption", mutate: func(c *models.VLESSConfig) { c.Encryption = "aes-128-gcm" }, wantError: "only none is valid"},
		{name: "unsupported reality spider x", mutate: func(c *models.VLESSConfig) { c.SpiderX = "/spider" }, wantError: "spider_x is not supported"},
		{name: "tcp header type", mutate: func(c *models.VLESSConfig) { c.Network, c.HeaderType = "raw", "srtp" }, wantError: "header_type"},
		{name: "tcp seed", mutate: func(c *models.VLESSConfig) { c.Network, c.Seed = "tcp", "seed" }, wantError: "seed is not supported"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := base
			testCase.mutate(&config)
			_, err := service.generateVLESSOutbound(&config, "test")
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("expected %q error, got %v", testCase.wantError, err)
			}
		})
	}

	for _, testCase := range []struct {
		name      string
		mutate    func(*models.VMESSConfig)
		wantError string
	}{
		{name: "tcp header type", mutate: func(c *models.VMESSConfig) { c.Network, c.HeaderType = "raw", "srtp" }, wantError: "header_type"},
		{name: "tcp seed", mutate: func(c *models.VMESSConfig) { c.Network, c.Seed = "tcp", "seed" }, wantError: "seed is not supported"},
	} {
		t.Run("vmess "+testCase.name, func(t *testing.T) {
			config := models.VMESSConfig{
				Server: "127.0.0.1", ServerPort: 443,
				UUID: "00000000-0000-0000-0000-000000000009",
			}
			testCase.mutate(&config)
			_, err := service.generateVMESSOutbound(&config, "test")
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("expected %q error, got %v", testCase.wantError, err)
			}
		})
	}
}

func TestBuildGlobalConfigResolvesDetourAndLocalDNS(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	nodes := []models.ProxyNode{
		nativeTestNode(t, 1, "upstream", "direct", models.DirectConfig{}),
		nativeTestNode(t, 2, "client", "direct", models.DirectConfig{DialerOptions: models.DialerOptions{
			Detour: "upstream", DomainResolver: map[string]interface{}{"server": "local", "strategy": "prefer_ipv4"},
		}}),
	}
	configJSON, err := service.BuildGlobalConfig(nodes)
	if err != nil {
		t.Fatalf("BuildGlobalConfig: %v", err)
	}
	var generated map[string]interface{}
	if err := json.Unmarshal(configJSON, &generated); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	dns := generated["dns"].(map[string]interface{})
	server := dns["servers"].([]interface{})[0].(map[string]interface{})
	if server["type"] != "local" || server["tag"] != "local" {
		t.Fatalf("local DNS server was not generated: %#v", dns)
	}
	outbounds := generated["outbounds"].([]interface{})
	if outbounds[1].(map[string]interface{})["detour"] != "node-1-out" {
		t.Fatalf("detour name was not resolved: %#v", outbounds[1])
	}

	// Existing generated tags are also valid native references.
	nodes[1] = nativeTestNode(t, 2, "client", "direct", models.DirectConfig{DialerOptions: models.DialerOptions{Detour: "node-1-out"}})
	if _, err := service.BuildGlobalConfig(nodes); err != nil {
		t.Fatalf("generated outbound tag detour should be accepted: %v", err)
	}

	// The built-in direct tag wins over a node with the reserved name.
	nodes[0].Name = "direct"
	nodes[1] = nativeTestNode(t, 2, "client", "direct", models.DirectConfig{DialerOptions: models.DialerOptions{Detour: "direct"}})
	configJSON, err = service.BuildGlobalConfig(nodes)
	if err != nil {
		t.Fatalf("built-in direct detour: %v", err)
	}
	if !strings.Contains(string(configJSON), `"detour": "direct"`) {
		t.Fatalf("detour direct was not preserved: %s", configJSON)
	}
}

func TestBuildGlobalConfigRejectsAmbiguousOrInvalidDetours(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	duplicateNames := []models.ProxyNode{
		nativeTestNode(t, 1, "same", "direct", models.DirectConfig{}),
		nativeTestNode(t, 2, "same", "direct", models.DirectConfig{}),
	}
	if _, err := service.BuildGlobalConfig(duplicateNames); err != nil {
		t.Fatalf("duplicate names without detour must remain valid: %v", err)
	}
	duplicateNames = append(duplicateNames, nativeTestNode(t, 3, "client", "direct", models.DirectConfig{DialerOptions: models.DialerOptions{Detour: "same"}}))
	if _, err := service.BuildGlobalConfig(duplicateNames); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous detour name must fail clearly, got %v", err)
	}

	self := []models.ProxyNode{nativeTestNode(t, 1, "self", "direct", models.DirectConfig{DialerOptions: models.DialerOptions{Detour: "node-1-out"}})}
	if _, err := service.BuildGlobalConfig(self); err == nil || !strings.Contains(err.Error(), "itself") {
		t.Fatalf("self detour must fail, got %v", err)
	}
}

func TestRealSingBoxAcceptsNativeOutboundMatrix(t *testing.T) {
	realBinary := os.Getenv("SINGBOX_TEST_BINARY")
	if realBinary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}
	t.Setenv("SINGBOX_BINARY", realBinary)
	service := NewSingBoxService(t.TempDir())

	tests := []struct {
		name      string
		proxyType string
		config    interface{}
	}{
		{name: "direct", proxyType: "direct", config: models.DirectConfig{}},
		{name: "shadowsocks", proxyType: "ss", config: models.SSConfig{Server: "127.0.0.1", ServerPort: 8388, Method: "aes-128-gcm", Password: "secret"}},
		{name: "vless-reality", proxyType: "vless", config: models.VLESSConfig{Server: "127.0.0.1", ServerPort: 443, UUID: "00000000-0000-0000-0000-000000000011", Security: "reality", SNI: "google.com", PublicKey: "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0", ShortID: "0123456789abcdef"}},
		{name: "vless-http-duration-header", proxyType: "vless", config: models.VLESSConfig{Server: "127.0.0.1", ServerPort: 80, UUID: "00000000-0000-0000-0000-000000000014", Security: "none", Network: "http", Headers: map[string]string{"idle_timeout": "opaque-header-value"}, TransportOptions: models.NativeOptions{"type": "http", "idle_timeout": "7"}}},
		{name: "vless-ws-to-grpc", proxyType: "vless", config: models.VLESSConfig{Server: "127.0.0.1", ServerPort: 80, UUID: "00000000-0000-0000-0000-000000000015", Security: "none", Network: "grpc", ServiceName: "edited", TransportOptions: models.NativeOptions{"type": "ws", "path": "/stale", "headers": map[string]interface{}{"X-Stale": "value"}, "permit_without_stream": true}}},
		{name: "vless-grpc-to-ws", proxyType: "vless", config: models.VLESSConfig{Server: "127.0.0.1", ServerPort: 80, UUID: "00000000-0000-0000-0000-000000000016", Security: "none", Network: "ws", Path: "/edited", Headers: map[string]string{"X-Empty": ""}, TransportOptions: models.NativeOptions{"type": "grpc", "service_name": "stale", "idle_timeout": "4", "permit_without_stream": true}}},
		{name: "vless-http-to-httpupgrade", proxyType: "vless", config: models.VLESSConfig{Server: "127.0.0.1", ServerPort: 80, UUID: "00000000-0000-0000-0000-000000000017", Security: "none", Network: "httpupgrade", HTTPUpgradePath: "/edited", HTTPUpgradeHost: "edited.example.com", TransportOptions: models.NativeOptions{"type": "http", "method": "POST", "idle_timeout": "4", "headers": map[string]interface{}{"X-Kept": "value"}}}},
		{name: "vmess-http", proxyType: "vmess", config: models.VMESSConfig{Server: "127.0.0.1", ServerPort: 80, UUID: "00000000-0000-0000-0000-000000000012", Security: "auto", Network: "http", Host: "one.example.com,two.example.com", Path: "/vmess"}},
		{name: "vmess-aes-128-cfb", proxyType: "vmess", config: models.VMESSConfig{Server: "127.0.0.1", ServerPort: 80, UUID: "00000000-0000-0000-0000-000000000018", Security: "aes-128-cfb"}},
		{name: "hysteria2", proxyType: "hy2", config: models.Hysteria2Config{Server: "127.0.0.1", ServerPort: 443, ServerPorts: models.ListableString{"443", "8443", "10000:20000"}, Password: "secret", InsecureSkipVerify: true}},
		{name: "hysteria2-obfs-edit", proxyType: "hy2", config: models.Hysteria2Config{Server: "127.0.0.1", ServerPort: 443, Password: "secret", Obfs: models.NativeOptions{"type": "salamander", "password": "stale"}, ObfsPassword: "edited", InsecureSkipVerify: true}},
		{name: "tuic", proxyType: "tuic", config: models.TUICConfig{Server: "127.0.0.1", ServerPort: 443, UUID: "00000000-0000-0000-0000-000000000013", Password: "secret", InsecureSkipVerify: true}},
		{name: "tuic-empty-password", proxyType: "tuic", config: models.TUICConfig{Server: "127.0.0.1", ServerPort: 443, UUID: "00000000-0000-0000-0000-000000000019", InsecureSkipVerify: true}},
		{name: "trojan-no-tls", proxyType: "trojan", config: models.TrojanConfig{Server: "127.0.0.1", ServerPort: 80, Password: "secret", Security: "none"}},
		{name: "anytls", proxyType: "anytls", config: models.AnyTLSConfig{Server: "127.0.0.1", ServerPort: 443, Password: "secret", Insecure: true}},
		{name: "socks5", proxyType: "socks5", config: models.SOCKS5Config{Server: "127.0.0.1", ServerPort: 1080}},
		{name: "http", proxyType: "http", config: models.HTTPProxyConfig{Server: "127.0.0.1", ServerPort: 8080, Path: "/proxy"}},
	}
	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			node := nativeTestNode(t, index+1, testCase.name, testCase.proxyType, testCase.config)
			configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{node})
			if err != nil {
				t.Fatalf("BuildGlobalConfig: %v", err)
			}
			if err := service.ValidateConfig(configJSON); err != nil {
				t.Fatalf("sing-box 1.12.12 rejected generated %s config: %v\n%s", testCase.name, err, configJSON)
			}
		})
	}
}
