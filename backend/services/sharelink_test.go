package services

import (
	"encoding/json"
	"testing"

	"sb-proxy/backend/models"
)

func TestParseTrojanLink(t *testing.T) {
	link := "trojan://pass123@example.com:443?type=ws&host=example.com&path=%2Fws&sni=sni.example.com&alpn=h2,http/1.1&insecure=1&fp=firefox#TestName"

	cfg, typ, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if typ != "trojan" {
		t.Fatalf("expected trojan type, got %s", typ)
	}
	if name != "TestName" {
		t.Fatalf("unexpected name: %s", name)
	}

	trojanCfg, ok := cfg.(models.TrojanConfig)
	if !ok {
		t.Fatalf("unexpected config type %T", cfg)
	}
	if trojanCfg.Server != "example.com" || trojanCfg.ServerPort != 443 || trojanCfg.Password != "pass123" {
		t.Fatalf("server/port/password mismatch: %+v", trojanCfg)
	}
	if trojanCfg.Network != "ws" || trojanCfg.Path != "/ws" || trojanCfg.Host != "example.com" {
		t.Fatalf("transport fields mismatch: %+v", trojanCfg)
	}
	if trojanCfg.SNI != "sni.example.com" || !trojanCfg.Insecure {
		t.Fatalf("tls fields mismatch: %+v", trojanCfg)
	}
	if len(trojanCfg.ALPN) != 2 || trojanCfg.ALPN[0] != "h2" {
		t.Fatalf("alpn parse mismatch: %+v", trojanCfg.ALPN)
	}
	if trojanCfg.Fingerprint != "firefox" {
		t.Fatalf("fingerprint parse mismatch: %+v", trojanCfg)
	}
}

func TestGenerateTrojanOutbound(t *testing.T) {
	rawCfg := models.TrojanConfig{
		Server:      "node.example.com",
		ServerPort:  443,
		Password:    "pwd",
		Network:     "ws",
		Path:        "/ws",
		Host:        "h.example.com",
		SNI:         "sni.example.com",
		ALPN:        []string{"h2"},
		Fingerprint: "firefox",
		Insecure:    true,
		Headers: map[string]string{
			"X-Test": "1",
		},
	}
	cfgBytes, _ := json.Marshal(rawCfg)

	node := models.ProxyNode{
		ID:          1,
		Name:        "trojan",
		Type:        "trojan",
		Config:      string(cfgBytes),
		InboundPort: 30010,
	}

	svc := &SingBoxService{}
	out, err := svc.generateOutbound(&node, "trojan-out")
	if err != nil {
		t.Fatalf("generateOutbound error: %v", err)
	}
	if out.Type != "trojan" || out.Tag != "trojan-out" {
		t.Fatalf("unexpected outbound meta: %+v", out)
	}
	tls, ok := out.Extra["tls"].(map[string]interface{})
	if !ok || tls["server_name"] != "sni.example.com" || tls["insecure"] != true {
		t.Fatalf("tls not set correctly: %+v", tls)
	}
	if utls, ok := tls["utls"].(map[string]interface{}); !ok || utls["fingerprint"] != "firefox" {
		t.Fatalf("utls not set correctly: %+v", utls)
	}
	transport, ok := out.Extra["transport"].(map[string]interface{})
	if !ok || transport["type"] != "ws" || transport["path"] != "/ws" {
		t.Fatalf("transport missing: %+v", transport)
	}
	headers, ok := transport["headers"].(map[string]string)
	if !ok {
		if h2, ok2 := transport["headers"].(map[string]interface{}); ok2 {
			if h2["Host"] != "h.example.com" {
				t.Fatalf("headers missing host: %+v", h2)
			}
		} else {
			t.Fatalf("headers missing: %+v", transport["headers"])
		}
	} else if headers["Host"] != "h.example.com" {
		t.Fatalf("headers host mismatch: %+v", headers)
	}
}
