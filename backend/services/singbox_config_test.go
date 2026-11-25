package services

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"sb-proxy/backend/models"
)

func TestMarshalConfigPreservesCanonicalEncoding(t *testing.T) {
	config := SingBoxConfig{
		Log: LogConfig{Level: "info", Timestamp: true},
		DNS: &DNSConfig{Servers: []DNSServer{{Type: "local", Tag: "local"}}},
		Inbounds: []InboundConfig{{
			Type: "mixed", Tag: "in", Listen: "::", Port: 30001,
			Users: []InboundUser{{Username: "user", Password: "test-pass"}},
		}},
		Outbounds: []OutboundConfig{{
			Type: "direct", Tag: "overridden",
			Extra: map[string]any{"tag": "out", "routing_mark": json.Number("9007199254740993")},
		}},
		Route: RouteConfig{Rules: []RouteRule{{Inbound: []string{"in"}, Outbound: "out"}}},
	}
	const want = `{
  "dns": {
    "servers": [
      {
        "tag": "local",
        "type": "local"
      }
    ]
  },
  "inbounds": [
    {
      "listen": "::",
      "listen_port": 30001,
      "tag": "in",
      "type": "mixed",
      "users": [
        {
          "password": "test-pass",
          "username": "user"
        }
      ]
    }
  ],
  "log": {
    "level": "info",
    "timestamp": true
  },
  "outbounds": [
    {
      "routing_mark": 9007199254740993,
      "tag": "out",
      "type": "direct"
    }
  ],
  "route": {
    "rules": [
      {
        "inbound": [
          "in"
        ],
        "outbound": "out"
      }
    ]
  }
}`
	data, err := marshalConfig(config)
	if err != nil || string(data) != want {
		t.Fatalf("unexpected canonical JSON: %s, err=%v", data, err)
	}
}

func TestBuildGlobalConfigHTTPSPreservesMixedAndTCPReuse(t *testing.T) {
	root := t.TempDir()
	setInboundTLSTestDirectories(t, root, filepath.Join(root, "missing"))
	certificate, key, _ := testInboundTLSPEM(t, testInboundTLSTemplate("proxy.example"))
	certPath, keyPath := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem")
	writeInboundTLSFile(t, certPath, certificate)
	writeInboundTLSFile(t, keyPath, key)
	service := NewSingBoxService(t.TempDir())
	nodes := []models.ProxyNode{
		{ID: 1, Name: "https", Type: "direct", Config: "{}", Enabled: true, InboundPort: 31001, InboundMode: models.InboundModeHTTPS, Username: "secure", Password: "secure-key", TCPReuseEnabled: true},
		{ID: 2, Name: "mixed", Type: "direct", Config: "{}", Enabled: true, InboundPort: 31002, InboundMode: models.InboundModeMixed, Username: "mixed", Password: "mixed-key", TCPReuseEnabled: true},
	}
	data, err := service.BuildGlobalConfigContext(t.Context(), nodes)
	if err != nil {
		t.Fatal(err)
	}
	config := decodeGeneratedConfig(t, data)
	secure := generatedEntryByTag(t, config, "inbounds", "node-1-in")
	tlsOptions, ok := secure["tls"].(map[string]any)
	if !ok || secure["type"] != "http" || tlsOptions["enabled"] != true || tlsOptions["certificate_path"] != certPath || tlsOptions["key_path"] != keyPath {
		t.Fatalf("HTTPS 入站未生成原生 HTTP + TLS 配置：%+v", secure)
	}
	mixed := generatedEntryByTag(t, config, "inbounds", "node-2-in")
	if mixed["type"] != "mixed" || mixed["tls"] != nil {
		t.Fatalf("mixed 入站被改变：%+v", mixed)
	}
	inbounds := inboundsFromConfig(t, config)
	secureUsers, mixedUsers := usersForInboundTag(t, inbounds, "node-1-in"), usersForInboundTag(t, inbounds, "node-2-in")
	if secureUsers["secure"] != "secure-key" || secureUsers["mixed+31002"] != "mixed-key" || mixedUsers["secure+31001"] != "secure-key" {
		t.Fatal("HTTPS 和 mixed 入站没有共享相同认证与单端口复用路由")
	}
	if findRuleIndexByAuthUser(routeRulesFromConfig(t, config), "mixed+31002") < 0 {
		t.Fatal("HTTPS 入站缺少 TCP 复用路由")
	}
}

func TestBuildGlobalConfigHTTPSRequiresSelectedValidCertificate(t *testing.T) {
	root := t.TempDir()
	setInboundTLSTestDirectories(t, root, filepath.Join(root, "missing"))
	service := NewSingBoxService(t.TempDir())
	node := models.ProxyNode{ID: 1, Name: "https", Type: "direct", Config: "{}", Enabled: true, InboundPort: 31001, InboundMode: models.InboundModeHTTPS}
	other := node
	other.ID, other.InboundPort = 2, 31002
	mixed := models.ProxyNode{ID: 3, Name: "mixed", Type: "direct", Config: "{}", Enabled: true, InboundPort: 31003}
	assertUnavailable := func() {
		t.Helper()
		data, err := service.BuildGlobalConfigContext(t.Context(), []models.ProxyNode{node, other, mixed})
		if err != nil {
			t.Fatalf("失效 HTTPS 入站阻止了其他节点生成配置：%v", err)
		}
		for _, tag := range []string{"node-1-in", "node-2-in"} {
			if strings.Contains(string(data), `"`+tag+`"`) {
				t.Fatalf("失效 HTTPS 节点仍有入站监听或直连路由：%s", tag)
			}
		}
		config := decodeGeneratedConfig(t, data)
		for _, tag := range []string{"node-1-out", "node-2-out"} {
			if generatedEntryByTag(t, config, "outbounds", tag)["type"] != "direct" {
				t.Fatalf("失效 HTTPS 节点的出站未保留：%s", tag)
			}
		}
		if generatedEntryByTag(t, config, "inbounds", "node-3-in")["type"] != "mixed" {
			t.Fatal("失效 HTTPS 入站影响了正常 mixed 监听")
		}
	}
	assertUnavailable()
	node.Enabled = false
	if _, err := service.BuildGlobalConfigContext(t.Context(), []models.ProxyNode{node}); err != nil {
		t.Fatalf("禁用的 HTTPS 入站不应要求证书：%v", err)
	}
	node.Enabled, node.InboundPort = true, 0
	if _, err := service.BuildGlobalConfigContext(t.Context(), []models.ProxyNode{node}); err != nil {
		t.Fatalf("无入站端口的节点不应要求证书：%v", err)
	}
	node.InboundPort = 31001
	var selectedCert, selectedKey string
	for _, name := range []string{"first", "second"} {
		certificate, key, _ := testInboundTLSPEM(t, testInboundTLSTemplate(name+".example"))
		selectedCert, selectedKey = filepath.Join(root, name+".pem"), filepath.Join(root, name+".key")
		writeInboundTLSFile(t, selectedCert, certificate)
		writeInboundTLSFile(t, selectedKey, key)
	}
	assertUnavailable()
	data, err := service.BuildGlobalConfigContext(t.Context(), []models.ProxyNode{node}, models.Settings{InboundTLSCertPath: selectedCert, InboundTLSKeyPath: selectedKey})
	if err != nil {
		t.Fatal(err)
	}
	options := generatedEntryByTag(t, decodeGeneratedConfig(t, data), "inbounds", "node-1-in")["tls"].(map[string]any)
	if options["certificate_path"] != selectedCert || options["key_path"] != selectedKey {
		t.Fatal("HTTPS 入站未使用数据库保存的证书选择")
	}
}

func routeRulesFromConfig(t *testing.T, config map[string]interface{}) []interface{} {
	t.Helper()

	route, ok := config["route"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing route section")
	}
	rules, ok := route["rules"].([]interface{})
	if !ok {
		t.Fatalf("missing route.rules")
	}
	return rules
}

func inboundsFromConfig(t *testing.T, config map[string]interface{}) []interface{} {
	t.Helper()

	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		t.Fatalf("missing inbounds")
	}
	return inbounds
}

func findRuleIndexByAuthUser(rules []interface{}, authUser string) int {
	for idx, raw := range rules {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		authUsers, ok := rule["auth_user"].([]interface{})
		if !ok {
			continue
		}
		for _, candidate := range authUsers {
			if candidateStr, ok := candidate.(string); ok && candidateStr == authUser {
				return idx
			}
		}
	}
	return -1
}

func findRuleIndexByInboundTag(rules []interface{}, inboundTag string) int {
	for idx, raw := range rules {
		rule, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		inbounds, ok := rule["inbound"].([]interface{})
		if !ok {
			continue
		}
		for _, candidate := range inbounds {
			if candidateStr, ok := candidate.(string); ok && candidateStr == inboundTag {
				return idx
			}
		}
	}
	return -1
}

func usersForInboundTag(t *testing.T, inbounds []interface{}, tag string) map[string]string {
	t.Helper()

	for _, rawInbound := range inbounds {
		inbound, ok := rawInbound.(map[string]interface{})
		if !ok {
			continue
		}
		inboundTag, _ := inbound["tag"].(string)
		if inboundTag != tag {
			continue
		}
		users := map[string]string{}
		rawUsers, hasUsers := inbound["users"].([]interface{})
		if !hasUsers {
			return users
		}
		for _, rawUser := range rawUsers {
			user, ok := rawUser.(map[string]interface{})
			if !ok {
				continue
			}
			username, _ := user["username"].(string)
			password, _ := user["password"].(string)
			users[username] = password
		}
		return users
	}

	t.Fatalf("inbound %s not found", tag)
	return nil
}

func TestBuildGlobalConfigRejectsLegacyUnpairedWhitespaceCredentials(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	testCases := []struct {
		name     string
		username string
		password string
	}{
		{name: "whitespace username only", username: " ", password: ""},
		{name: "whitespace password only", username: "", password: "\t"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.BuildGlobalConfigContext(context.Background(), []models.ProxyNode{
				{
					ID:          1,
					Name:        "legacy-node",
					Type:        "direct",
					Config:      "{}",
					InboundPort: 30001,
					Username:    testCase.username,
					Password:    testCase.password,
					Enabled:     true,
				},
			})
			if err == nil {
				t.Fatal("unpaired legacy credentials must not generate a public inbound")
			}
		})
	}
}

func TestBuildGlobalConfigAddsTCPReuseRouting(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:              1,
			Name:            "node-1",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30001,
			Username:        "entry",
			Password:        "entry-pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
		{
			ID:              2,
			Name:            "node-2",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30005,
			Username:        "target",
			Password:        "target-pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
	}

	data, err := service.BuildGlobalConfigContext(context.Background(), nodes)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}

	config := decodeGeneratedConfig(t, data)
	rules := routeRulesFromConfig(t, config)
	inbounds := inboundsFromConfig(t, config)

	authRuleIdx := findRuleIndexByAuthUser(rules, "target+30005")
	if authRuleIdx < 0 {
		t.Fatalf("expected auth_user rule for target+30005")
	}
	directRuleIdx := findRuleIndexByInboundTag(rules, "node-2-in")
	if directRuleIdx < 0 {
		t.Fatalf("expected direct inbound rule for node-2-in")
	}
	if authRuleIdx >= directRuleIdx {
		t.Fatalf("auth_user rule should be before direct inbound rules, auth=%d direct=%d", authRuleIdx, directRuleIdx)
	}

	users := usersForInboundTag(t, inbounds, "node-1-in")
	if users["entry"] != "entry-pass" {
		t.Fatalf("expected inbound base auth for node-1")
	}
	if users["target+30005"] != "target-pass" {
		t.Fatalf("expected shared reuse auth target+30005 on node-1 inbound")
	}
}

func TestBuildGlobalConfigRespectsTCPReuseSwitch(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:              1,
			Name:            "node-1",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30001,
			Username:        "entry",
			Password:        "entry-pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
		{
			ID:              2,
			Name:            "node-2",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30005,
			Username:        "target",
			Password:        "target-pass",
			TCPReuseEnabled: false,
			Enabled:         true,
		},
	}

	data, err := service.BuildGlobalConfigContext(context.Background(), nodes)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}

	config := decodeGeneratedConfig(t, data)
	rules := routeRulesFromConfig(t, config)
	if idx := findRuleIndexByAuthUser(rules, "target+30005"); idx >= 0 {
		t.Fatalf("did not expect auth_user route for disabled tcp reuse node")
	}

	users := usersForInboundTag(t, inboundsFromConfig(t, config), "node-1-in")
	if _, ok := users["target+30005"]; ok {
		t.Fatalf("did not expect shared auth user for disabled tcp reuse node")
	}
}

func TestBuildGlobalConfigSkipsTCPReuseForNodeWithoutInboundAuth(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:              1,
			Name:            "node-1",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30001,
			Username:        "entry",
			Password:        "entry-pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
		{
			ID:              2,
			Name:            "node-2",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30005,
			Username:        "",
			Password:        "",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
	}

	data, err := service.BuildGlobalConfigContext(context.Background(), nodes)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}

	config := decodeGeneratedConfig(t, data)
	rules := routeRulesFromConfig(t, config)
	if idx := findRuleIndexByAuthUser(rules, "node-2+30005"); idx >= 0 {
		t.Fatalf("did not expect auth_user route for node without inbound auth")
	}

	users := usersForInboundTag(t, inboundsFromConfig(t, config), "node-1-in")
	if _, ok := users["node-2+30005"]; ok {
		t.Fatalf("did not expect shared auth user for node without inbound auth")
	}
}

func TestBuildGlobalConfigRejectsUsernameWithPlus(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:              1,
			Name:            "node-1",
			Type:            "direct",
			Config:          "{}",
			InboundPort:     30001,
			Username:        "bad+name",
			Password:        "pass",
			TCPReuseEnabled: true,
			Enabled:         true,
		},
	}

	if _, err := service.BuildGlobalConfigContext(context.Background(), nodes); err == nil {
		t.Fatalf("expected username with '+' to be rejected")
	}
}
