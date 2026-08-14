package services

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"sb-proxy/backend/models"
)

func decodeGeneratedConfig(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode generated config: %v", err)
	}
	return config
}

func generatedEntryByTag(t *testing.T, config map[string]interface{}, section string, tag string) map[string]interface{} {
	t.Helper()
	entries, _ := config[section].([]interface{})
	for _, raw := range entries {
		entry, _ := raw.(map[string]interface{})
		if entry["tag"] == tag {
			return entry
		}
	}
	t.Fatalf("%s entry %q not found", section, tag)
	return nil
}

func TestBuildGlobalConfigAppliesManagedUpstreamModes(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	nodes := []models.ProxyNode{
		{ID: 1, Name: "global", Type: "direct", Config: `{}`, InboundPort: 31001, Enabled: true, UpstreamMode: models.UpstreamModeGlobal},
		{
			ID: 2, Name: "custom", Type: "direct", Config: `{}`, InboundPort: 31002, Enabled: true,
			UpstreamMode: models.UpstreamModeCustom, UpstreamType: "http",
			UpstreamConfig: `{"server":"proxy.example.com","server_port":8080,"username":"u","password":"p"}`,
		},
		{ID: 3, Name: "bypass", Type: "direct", Config: `{}`, InboundPort: 31003, Enabled: true, UpstreamMode: models.UpstreamModeNone},
	}
	settings := models.Settings{
		GlobalUpstreamEnabled: true,
		GlobalUpstreamType:    "socks5",
		GlobalUpstreamConfig:  `{"server":"global.example.com","server_port":1080,"username":"u","password":"p"}`,
	}

	data, err := service.BuildGlobalConfig(nodes, settings)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	config := decodeGeneratedConfig(t, data)
	globalUpstream := generatedEntryByTag(t, config, "outbounds", globalUpstreamTag)
	if globalUpstream["type"] != "socks" || globalUpstream["server"] != "global.example.com" {
		t.Fatalf("unexpected global upstream: %#v", globalUpstream)
	}
	customUpstream := generatedEntryByTag(t, config, "outbounds", "node-2-upstream")
	if customUpstream["type"] != "http" || customUpstream["server"] != "proxy.example.com" {
		t.Fatalf("unexpected custom upstream: %#v", customUpstream)
	}
	globalNode := generatedEntryByTag(t, config, "outbounds", "node-1-out")
	if globalNode["type"] != "selector" || !strings.Contains(fmt.Sprint(globalNode["outbounds"]), globalUpstreamTag) {
		t.Fatalf("global direct selector=%#v", globalNode)
	}
	customNode := generatedEntryByTag(t, config, "outbounds", "node-2-out")
	if customNode["type"] != "selector" || !strings.Contains(fmt.Sprint(customNode["outbounds"]), "node-2-upstream") {
		t.Fatalf("custom direct selector=%#v", customNode)
	}
	if _, exists := generatedEntryByTag(t, config, "outbounds", "node-3-out")["detour"]; exists {
		t.Fatal("bypass mode must not have detour")
	}
}

func TestValidateUpstreamDefinitionSupportsEveryManagedProtocol(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	cases := []models.ProxyDefinition{
		{Type: "direct", Config: `{}`},
		{Type: "ss", Config: `{"server":"127.0.0.2","server_port":443,"method":"aes-128-gcm","password":"p"}`},
		{Type: "vless", Config: `{"server":"127.0.0.2","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","encryption":"none","security":"none"}`},
		{Type: "vmess", Config: `{"server":"127.0.0.2","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","alter_id":0,"security":"auto","tls":"none"}`},
		{Type: "hy2", Config: `{"server":"127.0.0.2","server_port":443,"password":"p","insecure_skip_verify":true}`},
		{Type: "tuic", Config: `{"server":"127.0.0.2","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","password":"p","congestion_control":"bbr","insecure_skip_verify":true}`},
		{Type: "trojan", Config: `{"server":"127.0.0.2","server_port":443,"password":"p","security":"none"}`},
		{Type: "anytls", Config: `{"server":"127.0.0.2","server_port":443,"password":"p","insecure":true}`},
		{Type: "socks5", Config: `{"server":"127.0.0.2","server_port":1080}`},
		{Type: "socks5h", Config: `{"server":"127.0.0.2","server_port":1080}`},
		{Type: "http", Config: `{"server":"127.0.0.2","server_port":8080}`},
		{Type: "wireguard", Config: testWireGuardFlatConfig},
	}
	for _, testCase := range cases {
		t.Run(testCase.Type, func(t *testing.T) {
			if err := service.ValidateUpstreamDefinition(testCase); err != nil {
				t.Fatalf("validate %s upstream: %v", testCase.Type, err)
			}
		})
	}
}

func TestBuildGlobalConfigRejectsRouteNumberLocalInboundUpstream(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	nodes := []models.ProxyNode{
		{
			ID: 1, Name: "source", Type: "direct", Config: `{}`, InboundPort: 32001, Enabled: true,
			UpstreamMode: models.UpstreamModeCustom, UpstreamType: "socks5",
			UpstreamConfig: `{"server":"127.0.0.1","server_port":32002,"username":"target+32002","password":"p"}`,
		},
		{ID: 2, Name: "target", Type: "direct", Config: `{}`, InboundPort: 32002, Enabled: true, UpstreamMode: models.UpstreamModeNone},
	}

	_, err := service.BuildGlobalConfig(nodes)
	if err == nil || !IsUpstreamValidationError(err) || !strings.Contains(err.Error(), "route-number") {
		t.Fatalf("expected route-number recursion guard, got %v", err)
	}
}

func TestBuildGlobalConfigRejectsHysteriaPortRangeContainingInbound(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	nodes := []models.ProxyNode{
		{
			ID: 1, Name: "source", Type: "direct", Config: `{}`, InboundPort: 33001, Enabled: true,
			UpstreamMode: models.UpstreamModeCustom, UpstreamType: "hy2",
			UpstreamConfig: `{"server":"localhost","server_port":443,"server_ports":["33000:33010"],"password":"p","insecure_skip_verify":true}`,
		},
	}

	_, err := service.BuildGlobalConfig(nodes)
	if err == nil || !strings.Contains(err.Error(), "33001") {
		t.Fatalf("expected hopping-port inbound collision, got %v", err)
	}
}

func TestBuildGlobalConfigRejectsLocalDirectOverrideUpstream(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	node := models.ProxyNode{
		ID: 1, Name: "source", Type: "ss",
		Config:      `{"server":"proxy.example.com","server_port":32001,"method":"aes-128-gcm","password":"p"}`,
		InboundPort: 32001, Enabled: true, UpstreamMode: models.UpstreamModeCustom,
		UpstreamType: "direct", UpstreamConfig: `{"override_address":"127.0.0.1"}`,
	}

	_, err := service.BuildGlobalConfig([]models.ProxyNode{node})
	if err == nil || !IsUpstreamValidationError(err) || !strings.Contains(err.Error(), "32001") {
		t.Fatalf("expected local direct override recursion guard, got %v", err)
	}
}

func TestBuildGlobalConfigRejectsIdenticalAndNestedUpstreams(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	config := `{"server":"proxy.example.com","server_port":1080,"username":"u","password":"p"}`
	node := models.ProxyNode{
		ID: 1, Name: "same", Type: "socks5", Config: config, InboundPort: 34001,
		Enabled: true, UpstreamMode: models.UpstreamModeGlobal,
	}
	settings := models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "socks5", GlobalUpstreamConfig: config}
	if _, err := service.BuildGlobalConfig([]models.ProxyNode{node}, settings); err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("expected identical global upstream rejection, got %v", err)
	}

	node.Type = "socks5h"
	if _, err := service.BuildGlobalConfig([]models.ProxyNode{node}, settings); err == nil || !strings.Contains(err.Error(), "identical") {
		t.Fatalf("expected SOCKS5 alias identity rejection, got %v", err)
	}

	node.Type = "direct"
	node.Config = `{}`
	node.UpstreamMode = models.UpstreamModeCustom
	node.UpstreamType = "socks5"
	node.UpstreamConfig = `{"server":"proxy.example.com","server_port":1080,"detour":"node-1-out"}`
	if _, err := service.BuildGlobalConfig([]models.ProxyNode{node}); err == nil || !strings.Contains(err.Error(), "nested detour") {
		t.Fatalf("expected nested detour rejection, got %v", err)
	}
}

func TestBuildGlobalConfigGeneratesWireGuardCustomUpstreamEndpoint(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	node := models.ProxyNode{
		ID: 9, Name: "wg-upstream", Type: "direct", Config: `{}`, InboundPort: 35009, Enabled: true,
		UpstreamMode: models.UpstreamModeCustom, UpstreamType: "wireguard", UpstreamConfig: testWireGuardFlatConfig,
	}
	data, err := service.BuildGlobalConfig([]models.ProxyNode{node})
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	config := decodeGeneratedConfig(t, data)
	endpoint := generatedEntryByTag(t, config, "endpoints", "node-9-upstream")
	if endpoint["type"] != "wireguard" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
	selector := generatedEntryByTag(t, config, "outbounds", "node-9-out")
	if selector["type"] != "selector" || !strings.Contains(fmt.Sprint(selector["outbounds"]), "node-9-upstream") {
		t.Fatalf("wireguard custom selector=%#v", selector)
	}
}

func TestRealSingBoxAcceptsManagedUpstreamProtocolMatrix(t *testing.T) {
	realBinary := os.Getenv("SINGBOX_TEST_BINARY")
	if realBinary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}
	t.Setenv("SINGBOX_BINARY", realBinary)
	service := NewSingBoxService(t.TempDir())
	definitions := []models.ProxyDefinition{
		{Type: "direct", Config: `{}`},
		{Type: "ss", Config: `{"server":"192.0.2.2","server_port":443,"method":"aes-128-gcm","password":"p"}`},
		{Type: "vless", Config: `{"server":"192.0.2.2","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","encryption":"none","security":"none"}`},
		{Type: "vmess", Config: `{"server":"192.0.2.2","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","alter_id":0,"security":"auto","tls":"none"}`},
		{Type: "hy2", Config: `{"server":"192.0.2.2","server_port":443,"password":"p","insecure_skip_verify":true}`},
		{Type: "tuic", Config: `{"server":"192.0.2.2","server_port":443,"uuid":"00000000-0000-0000-0000-000000000000","password":"p","congestion_control":"bbr","insecure_skip_verify":true}`},
		{Type: "trojan", Config: `{"server":"192.0.2.2","server_port":443,"password":"p","security":"none"}`},
		{Type: "anytls", Config: `{"server":"192.0.2.2","server_port":443,"password":"p","insecure":true}`},
		{Type: "socks5", Config: `{"server":"192.0.2.2","server_port":1080}`},
		{Type: "socks5h", Config: `{"server":"192.0.2.2","server_port":1080}`},
		{Type: "http", Config: `{"server":"192.0.2.2","server_port":8080}`},
		{Type: "wireguard", Config: testWireGuardFlatConfig},
	}

	for index, definition := range definitions {
		t.Run("upstream-"+definition.Type, func(t *testing.T) {
			nodeType := "direct"
			nodeConfig := `{}`
			if definition.Type == "direct" {
				nodeType = "ss"
				nodeConfig = `{"server":"192.0.2.3","server_port":8388,"method":"aes-128-gcm","password":"node"}`
			}
			node := models.ProxyNode{
				ID: index + 1, Name: "managed", Type: nodeType, Config: nodeConfig,
				InboundPort: 37000 + index, Enabled: true, UpstreamMode: models.UpstreamModeCustom,
				UpstreamType: definition.Type, UpstreamConfig: definition.Config,
			}
			configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{node})
			if err != nil {
				t.Fatalf("BuildGlobalConfig: %v", err)
			}
			if err := service.ValidateConfig(configJSON); err != nil {
				t.Fatalf("real sing-box rejected %s upstream: %v\n%s", definition.Type, err, configJSON)
			}
		})
	}

	customUpstream := models.ProxyDefinition{
		Type: "socks5", Config: `{"server":"192.0.2.99","server_port":1081}`,
	}
	for index, definition := range definitions {
		t.Run("node-"+definition.Type, func(t *testing.T) {
			node := models.ProxyNode{
				ID: index + 100, Name: "managed-node", Type: definition.Type, Config: definition.Config,
				InboundPort: 37100 + index, Enabled: true, UpstreamMode: models.UpstreamModeCustom,
				UpstreamType: customUpstream.Type, UpstreamConfig: customUpstream.Config,
			}
			configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{node})
			if err != nil {
				t.Fatalf("BuildGlobalConfig: %v", err)
			}
			if err := service.ValidateConfig(configJSON); err != nil {
				t.Fatalf("real sing-box rejected managed %s node: %v\n%s", definition.Type, err, configJSON)
			}
		})
	}
}
