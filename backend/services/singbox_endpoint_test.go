package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sb-proxy/backend/models"
)

// Keys below are the sample keypair from the official sing-box documentation;
// they must be structurally valid because `sing-box check` decodes them.
const testWireGuardPrivateKey = "YNXtAzepDqRv9H52osJVDQnznT5AM11eCK3ESpwSt04="
const testWireGuardPeerPublicKey = "Z1XXLsKYkYxuiYjJIkRvtIKFepCYHTgON+GwPq7SOV4="

const testWireGuardFlatConfig = `{
	"server": "engage.cloudflareclient.com",
	"server_port": 2408,
	"local_address": ["172.16.0.2/32", "2606:4700:110:8765::2/128"],
	"private_key": "` + testWireGuardPrivateKey + `",
	"peer_public_key": "` + testWireGuardPeerPublicKey + `",
	"reserved": [162, 104, 222],
	"mtu": 1280
}`

func endpointsFromConfig(t *testing.T, config map[string]interface{}) []interface{} {
	t.Helper()

	endpoints, ok := config["endpoints"].([]interface{})
	if !ok {
		t.Fatalf("missing endpoints section")
	}
	return endpoints
}

func TestGenerateGlobalConfigWireGuardUsesEndpointFormat(t *testing.T) {
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:          7,
			Name:        "warp",
			Type:        "wireguard",
			Config:      testWireGuardFlatConfig,
			InboundPort: 30007,
			Enabled:     true,
		},
	}

	if err := service.GenerateGlobalConfig(nodes); err != nil {
		t.Fatalf("GenerateGlobalConfig failed: %v", err)
	}

	config := loadGeneratedConfigMap(t, configDir)

	// WireGuard must not appear in outbounds (legacy format is rejected by
	// sing-box 1.12+ and removed in 1.13).
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		t.Fatalf("missing outbounds")
	}
	for _, rawOutbound := range outbounds {
		outbound, _ := rawOutbound.(map[string]interface{})
		if outbound["type"] == "wireguard" {
			t.Fatalf("wireguard must be generated as endpoint, found legacy outbound: %v", outbound)
		}
	}

	endpoints := endpointsFromConfig(t, config)
	if len(endpoints) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(endpoints))
	}
	endpoint, _ := endpoints[0].(map[string]interface{})
	if endpoint["type"] != "wireguard" || endpoint["tag"] != "node-7-out" {
		t.Fatalf("unexpected endpoint identity: %v", endpoint)
	}

	// New-format field names.
	if _, hasLegacy := endpoint["local_address"]; hasLegacy {
		t.Fatalf("legacy field local_address must be renamed to address")
	}
	addresses, _ := endpoint["address"].([]interface{})
	if len(addresses) != 2 {
		t.Fatalf("expected 2 addresses, got %v", endpoint["address"])
	}
	if endpoint["private_key"] != testWireGuardPrivateKey {
		t.Fatalf("missing private_key")
	}
	if endpoint["mtu"] != float64(1280) {
		t.Fatalf("missing mtu, got %v", endpoint["mtu"])
	}

	// Flat single-peer fields must fold into the peers array with renamed keys
	// and defaulted allowed_ips.
	peers, _ := endpoint["peers"].([]interface{})
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %v", endpoint["peers"])
	}
	peer, _ := peers[0].(map[string]interface{})
	if peer["address"] != "engage.cloudflareclient.com" || peer["port"] != float64(2408) {
		t.Fatalf("peer server/server_port must map to address/port, got %v", peer)
	}
	if peer["public_key"] != testWireGuardPeerPublicKey {
		t.Fatalf("missing peer public_key")
	}
	allowedIPs, _ := peer["allowed_ips"].([]interface{})
	if len(allowedIPs) != 2 || allowedIPs[0] != "0.0.0.0/0" || allowedIPs[1] != "::/0" {
		t.Fatalf("single-peer conversion must default allowed_ips to full routes, got %v", peer["allowed_ips"])
	}
	reserved, _ := peer["reserved"].([]interface{})
	if len(reserved) != 3 {
		t.Fatalf("missing reserved bytes, got %v", peer["reserved"])
	}

	// The endpoint tag must still be routable from the node's inbound.
	rules := routeRulesFromConfig(t, config)
	ruleIdx := findRuleIndexByInboundTag(rules, "node-7-in")
	if ruleIdx < 0 {
		t.Fatalf("expected route rule for node-7-in")
	}
	rule, _ := rules[ruleIdx].(map[string]interface{})
	if rule["outbound"] != "node-7-out" {
		t.Fatalf("route rule must reference endpoint tag, got %v", rule["outbound"])
	}
}

func TestGenerateWireGuardEndpointMultiPeerAndDialFields(t *testing.T) {
	service := NewSingBoxService(t.TempDir())

	config := &models.WireGuardConfig{
		LocalAddress:    []string{"10.0.0.2/32"},
		PrivateKey:      "pk",
		SystemInterface: true,
		InterfaceName:   "wg0",
		Workers:         4,
		MTU:             1408,
		Network:         "tcp",
		Detour:          "upstream",
		ConnectTimeout:  "5s",
		Peers: []models.WireGuardPeerConfig{
			{
				Server:       "peer-a.example.com",
				ServerPort:   51820,
				PublicKey:    "pub-a",
				PreSharedKey: "psk-a",
				AllowedIPs:   []string{"10.0.0.0/24"},
				Reserved:     []uint8{1, 2, 3},
			},
			{
				Server:     "peer-b.example.com",
				ServerPort: 51821,
				PublicKey:  "pub-b",
			},
		},
	}

	endpoint, err := service.generateWireGuardEndpoint(config, "node-1-out")
	if err != nil {
		t.Fatalf("generateWireGuardEndpoint failed: %v", err)
	}

	if endpoint.Extra["system"] != true {
		t.Fatalf("system_interface must map to system")
	}
	if endpoint.Extra["name"] != "wg0" {
		t.Fatalf("interface_name must map to name")
	}
	if endpoint.Extra["workers"] != 4 || endpoint.Extra["mtu"] != 1408 {
		t.Fatalf("workers/mtu must carry over")
	}
	if endpoint.Extra["detour"] != "upstream" || endpoint.Extra["connect_timeout"] != "5s" {
		t.Fatalf("dial fields must carry over")
	}
	if _, hasNetwork := endpoint.Extra["network"]; hasNetwork {
		t.Fatalf("legacy outbound-only network limiter must be dropped in endpoint format")
	}

	peers, _ := endpoint.Extra["peers"].([]map[string]interface{})
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %v", endpoint.Extra["peers"])
	}
	if peers[0]["address"] != "peer-a.example.com" || peers[0]["port"] != 51820 {
		t.Fatalf("peer 0 must keep explicit values, got %v", peers[0])
	}
	if got, _ := peers[0]["allowed_ips"].([]string); len(got) != 1 || got[0] != "10.0.0.0/24" {
		t.Fatalf("explicit allowed_ips must be preserved, got %v", peers[0]["allowed_ips"])
	}
	if _, exists := peers[1]["allowed_ips"]; exists {
		t.Fatalf("explicit endpoint peers must preserve omitted allowed_ips, got %v", peers[1]["allowed_ips"])
	}
}

func TestWireGuardDomainResolverFlatFieldsOverrideAndClearCompatibility(t *testing.T) {
	config := &models.WireGuardConfig{
		DomainResolver:         "local",
		DomainResolverStrategy: "prefer_ipv6",
		DomainResolverOptions: models.NativeOptions{
			"server":        "stale-resolver",
			"strategy":      "prefer_ipv4",
			"disable_cache": true,
		},
	}

	resolver, err := wireGuardDomainResolverValue(config)
	if err != nil {
		t.Fatalf("merge WireGuard domain resolver: %v", err)
	}
	resolverMap, ok := resolver.(map[string]interface{})
	if !ok {
		t.Fatalf("expected resolver object, got %#v", resolver)
	}
	if resolverMap["server"] != "local" || resolverMap["strategy"] != "prefer_ipv6" || resolverMap["disable_cache"] != true {
		t.Fatalf("flat resolver fields must override stale native values while preserving native-only fields: %#v", resolverMap)
	}

	config.DomainResolverStrategy = ""
	resolver, err = wireGuardDomainResolverValue(config)
	if err != nil {
		t.Fatalf("clear WireGuard resolver strategy: %v", err)
	}
	resolverMap = resolver.(map[string]interface{})
	if resolverMap["server"] != "local" {
		t.Fatalf("edited flat resolver server was lost: %#v", resolverMap)
	}
	if _, exists := resolverMap["strategy"]; exists {
		t.Fatalf("explicit flat strategy clear must remove stale native strategy: %#v", resolverMap)
	}

	config.DomainResolver = ""
	resolver, err = wireGuardDomainResolverValue(config)
	if err != nil {
		t.Fatalf("clear WireGuard domain resolver: %v", err)
	}
	if resolver != nil {
		t.Fatalf("explicit empty resolver must clear stale native options: %#v", resolver)
	}
}

// writeScriptedSingBox writes a fake sing-box that fails `check` when the
// config contains BAD_MARKER, fails `run` when it contains RUN_FAIL_MARKER,
// and otherwise behaves like a long-running kernel.
func writeScriptedSingBox(t *testing.T) string {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  if grep -q "BAD_MARKER" "$3"; then
    echo "FATAL[0000] decode config: unknown field" >&2
    exit 1
  fi
  exit 0
fi
if grep -q "RUN_FAIL_MARKER" "$3"; then
  echo "start service: listen error" >&2
  exit 1
fi
sleep 300
`
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	return fakeBinary
}

func TestValidateConfigSurfacesKernelError(t *testing.T) {
	t.Setenv("SINGBOX_BINARY", writeScriptedSingBox(t))
	service := NewSingBoxService(t.TempDir())

	if err := service.ValidateConfig([]byte(`{"inbounds":[]}`)); err != nil {
		t.Fatalf("valid config must pass: %v", err)
	}

	err := service.ValidateConfig([]byte(`{"inbounds":[],"comment":"BAD_MARKER"}`))
	if err == nil {
		t.Fatalf("invalid config must be rejected")
	}
	if !strings.Contains(err.Error(), "decode config: unknown field") {
		t.Fatalf("kernel error output must be surfaced, got: %v", err)
	}
}

func TestApplyConfigRollsBackToLastGood(t *testing.T) {
	t.Setenv("SINGBOX_BINARY", writeScriptedSingBox(t))
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)
	t.Cleanup(func() { _ = service.Stop() })

	goodConfig := []byte(`{"good": true}`)
	if err := service.writeConfigFile(goodConfig); err != nil {
		t.Fatalf("write good config: %v", err)
	}
	if err := service.Start(); err != nil {
		t.Fatalf("start with good config: %v", err)
	}

	lastGood, err := os.ReadFile(service.lastGoodConfigPath())
	if err != nil || string(lastGood) != string(goodConfig) {
		t.Fatalf("successful start must snapshot last-good config, got %q err %v", lastGood, err)
	}

	badConfig := []byte(`{"good": false, "comment": "RUN_FAIL_MARKER"}`)
	err = service.ApplyConfig(badConfig)
	if err == nil {
		t.Fatalf("ApplyConfig must fail when the kernel cannot start")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("error must mention rollback, got: %v", err)
	}

	restored, err := os.ReadFile(service.configPath())
	if err != nil || string(restored) != string(goodConfig) {
		t.Fatalf("config.json must be restored to last-good, got %q err %v", restored, err)
	}

	service.mu.RLock()
	running := service.process != nil
	service.mu.RUnlock()
	if !running {
		t.Fatalf("sing-box must be running again on the last-good config")
	}
}

// TestRealSingBoxAcceptsGeneratedWireGuardConfig validates the generated
// endpoint config against a real sing-box binary when one is provided via
// SINGBOX_TEST_BINARY (skipped otherwise).
func TestRealSingBoxAcceptsGeneratedWireGuardConfig(t *testing.T) {
	realBinary := os.Getenv("SINGBOX_TEST_BINARY")
	if realBinary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}
	t.Setenv("SINGBOX_BINARY", realBinary)

	configDir := t.TempDir()
	service := NewSingBoxService(configDir)

	nodes := []models.ProxyNode{
		{
			ID:          1,
			Name:        "warp",
			Type:        "wireguard",
			Config:      testWireGuardFlatConfig,
			InboundPort: 31881,
			Enabled:     true,
		},
		{
			ID:          2,
			Name:        "direct",
			Type:        "direct",
			Config:      "{}",
			InboundPort: 31882,
			Username:    "u",
			Password:    "p",
			Enabled:     true,
		},
	}

	configJSON, err := service.BuildGlobalConfig(nodes)
	if err != nil {
		t.Fatalf("BuildGlobalConfig failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(configJSON, &parsed); err != nil {
		t.Fatalf("generated config is not valid JSON: %v", err)
	}

	if err := service.ValidateConfig(configJSON); err != nil {
		t.Fatalf("real sing-box rejected the generated endpoint config: %v", err)
	}
}

func TestRealSingBoxAcceptsEditedWireGuardDomainResolver(t *testing.T) {
	realBinary := os.Getenv("SINGBOX_TEST_BINARY")
	if realBinary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}
	t.Setenv("SINGBOX_BINARY", realBinary)
	service := NewSingBoxService(t.TempDir())

	node := nativeTestNode(t, 1, "warp-resolver", "wireguard", models.WireGuardConfig{
		Server:                 "engage.cloudflareclient.com",
		ServerPort:             2408,
		LocalAddress:           []string{"172.16.0.2/32"},
		PrivateKey:             testWireGuardPrivateKey,
		PeerPublicKey:          testWireGuardPeerPublicKey,
		DomainResolver:         "local",
		DomainResolverStrategy: "prefer_ipv4",
		DomainResolverOptions: models.NativeOptions{
			"server":        "stale-resolver",
			"strategy":      "prefer_ipv6",
			"disable_cache": true,
		},
	})
	configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{node})
	if err != nil {
		t.Fatalf("BuildGlobalConfig with edited resolver: %v", err)
	}
	if err := service.ValidateConfig(configJSON); err != nil {
		t.Fatalf("real sing-box rejected edited WireGuard resolver config: %v\n%s", err, configJSON)
	}
}
