package services

import (
	"maps"
	"os"
	"testing"

	"sb-proxy/backend/models"
)

func TestBuildGlobalConfigPortlessNodesKeepOutboundAndEndpoint(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	portless := []models.ProxyNode{
		{ID: 1, GroupID: 1, Name: "relay", Type: "socks5", Config: `{"server":"192.0.2.11","server_port":1080}`,
			Enabled: true, Username: "relay", Password: "relay-pass", TCPReuseEnabled: true},
		{ID: 2, GroupID: 1, Name: "tunnel", Type: "wireguard", Config: testWireGuardFlatConfig,
			Enabled: true, Username: "tunnel", Password: "tunnel-pass", TCPReuseEnabled: true},
	}
	public := models.ProxyNode{ID: 3, GroupID: 2, Name: "entry", Type: "socks5", UpstreamMode: models.UpstreamModeLegacy,
		Config: `{"server":"192.0.2.12","server_port":1080,"detour":"relay"}`, InboundPort: 31003,
		Enabled: true, Username: "entry", Password: "entry-pass", TCPReuseEnabled: true}
	for _, withPublic := range []bool{false, true} {
		name := "all portless"
		nodes := portless
		if withPublic {
			name = "public entry with cross-group detour"
			nodes = append(nodes, public)
		}
		t.Run(name, func(t *testing.T) {
			data, err := service.BuildGlobalConfigContext(t.Context(), nodes)
			if err != nil {
				t.Fatal(err)
			}
			config := decodeGeneratedConfig(t, data)
			if relay := generatedEntryByTag(t, config, "outbounds", "node-1-out"); relay["type"] != "socks" || relay["server"] != "192.0.2.11" {
				t.Fatalf("missing portless SOCKS outbound: %v", relay)
			}
			if tunnel := generatedEntryByTag(t, config, "endpoints", "node-2-out"); tunnel["type"] != "wireguard" {
				t.Fatalf("missing portless WireGuard endpoint: %v", tunnel["type"])
			}
			inbounds := inboundsFromConfig(t, config)
			route, ok := config["route"].(map[string]any)
			if !ok || route["final"] != "direct" {
				t.Fatal("missing default route")
			}
			if withPublic {
				entry := generatedEntryByTag(t, config, "outbounds", "node-3-out")
				inbound := generatedEntryByTag(t, config, "inbounds", "node-3-in")
				if entry["detour"] != "node-1-out" || len(inbounds) != 1 || inbound["listen_port"] != float64(public.InboundPort) {
					t.Fatalf("unexpected public entry: detour=%v inbounds=%d port=%v", entry["detour"], len(inbounds), inbound["listen_port"])
				}
				users := usersForInboundTag(t, inbounds, "node-3-in")
				if !maps.Equal(users, map[string]string{"entry": "entry-pass", "entry+31003": "entry-pass"}) {
					t.Fatal("public inbound exposed incorrect authentication users")
				}
				rules := routeRulesFromConfig(t, config)
				reuse := findRuleIndexByAuthUser(rules, "entry+31003")
				direct := findRuleIndexByInboundTag(rules, "node-3-in")
				if len(rules) != 2 || reuse < 0 || direct < 0 || reuse >= direct {
					t.Fatalf("expected only the public reuse and inbound routes: %v", rules)
				}
			} else {
				rules, _ := route["rules"].([]any)
				if len(inbounds) != 0 || len(rules) != 0 {
					t.Fatalf("portless nodes exposed inbounds or routes: %v %v", inbounds, rules)
				}
			}
			t.Run("real sing-box check", func(t *testing.T) {
				binary := os.Getenv("SINGBOX_TEST_BINARY")
				if binary == "" {
					t.Skip("SINGBOX_TEST_BINARY not set")
				}
				t.Setenv("SINGBOX_BINARY", binary)
				if err := service.ValidateConfigContext(t.Context(), data); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
