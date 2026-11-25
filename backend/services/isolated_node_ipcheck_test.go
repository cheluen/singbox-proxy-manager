package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"sb-proxy/backend/models"
)

func checkIsolatedWireGuardConfig(t *testing.T, config []byte) {
	t.Helper()
	t.Run("kernel", func(t *testing.T) {
		binary := os.Getenv("SINGBOX_TEST_BINARY")
		if binary == "" {
			t.Skip("SINGBOX_TEST_BINARY not set")
		}
		service := NewSingBoxService(t.TempDir(), WithBinaryPath(binary))
		if err := service.ValidateConfigContext(context.Background(), config); err != nil {
			t.Fatal(err)
		}
	})
}

func TestIsolatedNodeIPCheckRendersOnlyEffectiveDependencies(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	wireguard := strings.Replace(testWireGuardFlatConfig, `"mtu": 1280`, `"mtu":1280,"system_interface":true,"interface_name":"active-wg","listen_port":51820,"domain_resolver":"local"`, 1)
	settings := models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "wireguard", GlobalUpstreamConfig: wireguard}
	sources := []models.ProxyNode{
		{ID: 2, Enabled: true, Type: "socks5", Config: `{"server":"192.0.2.2","server_port":1080,"detour":"relay"}`, UpstreamMode: models.UpstreamModeLegacy},
		{ID: 3, Name: "relay", Enabled: true, Type: "direct", Config: `{"detour":"node-4-out"}`, InboundPort: 31003, Username: "existing-ingress", Password: "existing-key", TCPReuseEnabled: true, UpstreamMode: models.UpstreamModeLegacy},
		{ID: 4, Name: "tunnel", Enabled: true, Type: "http", Config: `{"server":"192.0.2.4","server_port":8080,"detour":"ignored"}`, UpstreamMode: models.UpstreamModeGlobal},
		{ID: 5, Name: "node-4-out", Enabled: true, Type: "invalid", Config: `invalid`, InboundPort: 31005},
		{ID: 6, Name: "relay", Type: "invalid", Config: `invalid`, InboundPort: 31006},
	}
	original := slices.Clone(sources)
	plan, err := service.prepareIsolatedIPCheck(context.Background(), 2, sources, settings)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sources, original) || len(plan.nodes) != 3 || len(plan.upstreams) != 1 || plan.target.ID != 2 || !plan.needsLocalDNS {
		t.Fatal("dependency selection changed stored settings or selected the wrong graph")
	}
	for _, port := range []int{31003, 31005, 31006} {
		if _, reserved := plan.usedPorts[port]; !reserved {
			t.Fatalf("configured port %d is eligible for the probe listener", port)
		}
	}
	plan.target.InboundPort, plan.target.Username, plan.target.Password = 31999, "one-use-user", "one-use-key"
	data, err := service.renderGlobalConfig(plan.nodes, plan.upstreams, "127.0.0.1", plan.needsLocalDNS)
	if err != nil {
		t.Fatal(err)
	}
	config := decodeGeneratedConfig(t, data)
	inbound := generatedEntryByTag(t, config, "inbounds", "node-2-in")
	if len(config["inbounds"].([]any)) != 1 || inbound["listen"] != "127.0.0.1" || inbound["listen_port"] != float64(31999) || len(inbound["users"].([]any)) != 1 {
		t.Fatal("dependency nodes opened extra listeners or exposed extra users")
	}
	for _, forbidden := range []string{"active-wg", "existing-ingress", "existing-key", "node-5-out", "node-6-out", "51820"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("isolated configuration contains %q", forbidden)
		}
	}
	root := generatedEntryByTag(t, config, "outbounds", "node-2-out")
	relay := generatedEntryByTag(t, config, "outbounds", "node-3-out")
	terminal := generatedEntryByTag(t, config, "outbounds", "node-4-out")
	endpoint := generatedEntryByTag(t, config, "endpoints", globalUpstreamTag)
	if root["detour"] != "node-3-out" || relay["type"] != "selector" || relay["default"] != "node-4-out" ||
		terminal["detour"] != globalUpstreamTag || endpoint["type"] != "wireguard" || endpoint["system"] != nil || endpoint["listen_port"] != nil || endpoint["name"] != nil {
		t.Fatal("isolated route did not retain the effective upstream chain and userspace WireGuard endpoint")
	}
	if !slices.Equal(sources, original) {
		t.Fatal("rendering changed the live node settings")
	}
	checkIsolatedWireGuardConfig(t, data)
}

func TestIsolatedWireGuardTargetUsesItsManagedUpstream(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	config := strings.Replace(testWireGuardFlatConfig, `"mtu": 1280`, `"mtu":1280,"listen_port":51820,"interface_name":"live-wg","system_interface":true,"detour":"stale"`, 1)
	target := models.ProxyNode{
		ID: 7, Enabled: true, Type: "wireguard", Config: config, UpstreamMode: models.UpstreamModeCustom,
		UpstreamType: "socks5", UpstreamConfig: `{"server":"192.0.2.8","server_port":1080}`,
	}
	plan, err := service.prepareIsolatedIPCheck(context.Background(), 7, []models.ProxyNode{target}, models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "invalid"})
	if err != nil {
		t.Fatal(err)
	}
	plan.target.InboundPort, plan.target.Username, plan.target.Password = 32100, "one-use-user", "one-use-key"
	data, err := service.renderGlobalConfig(plan.nodes, plan.upstreams, "127.0.0.1", plan.needsLocalDNS)
	if err != nil {
		t.Fatal(err)
	}
	output := decodeGeneratedConfig(t, data)
	endpoint := generatedEntryByTag(t, output, "endpoints", "node-7-out")
	upstream := generatedEntryByTag(t, output, "outbounds", "node-7-upstream")
	if endpoint["system"] != nil || endpoint["name"] != nil || endpoint["listen_port"] != nil || endpoint["detour"] != "node-7-upstream" ||
		upstream["type"] != "socks" || strings.Contains(string(data), globalUpstreamTag) {
		t.Fatal("WireGuard target did not render the isolated custom upstream path")
	}
	checkIsolatedWireGuardConfig(t, data)
}

func TestIsolatedIPCheckReservationExcludesActiveListeners(t *testing.T) {
	first, err := reserveFreshLoopbackPort(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := reserveFreshLoopbackPort(nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if first.Addr().String() == second.Addr().String() || !first.Addr().(*net.TCPAddr).IP.IsLoopback() {
		t.Fatal("simultaneous preparations must reserve distinct loopback listeners")
	}
}

func TestIsolatedIPCheckOmitsBypassedDirectUpstream(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	target := models.ProxyNode{ID: 3, Enabled: true, Type: "http", Config: `{"server":"192.0.2.8","server_port":8080}`, UpstreamMode: models.UpstreamModeGlobal}
	settings := models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "direct", GlobalUpstreamConfig: `{}`}
	plan, err := service.prepareIsolatedIPCheck(context.Background(), target.ID, []models.ProxyNode{target}, settings)
	if err != nil || len(plan.upstreams) != 0 || configDetour(plan.nodes[0].Config) != "" {
		t.Fatalf("probe rendered an unused upstream: %v", err)
	}
}

func TestIPCheckDiagnosticProtectsCredentialsAndRateLimitHeaders(t *testing.T) {
	node := models.ProxyNode{
		Username: "ingress-name", Password: "ingress-value",
		Config:         `{"uuid":"node-secret","peers":[{"pre_shared_key":"peer-secret"}]}`,
		UpstreamConfig: `{"password":"upstream-value"}`,
	}
	message := "handshake error https://somewhere.example/sub/secret?token=t escaped https:\\/\\/private.example/feed " +
		"ingress-name ingress-value node-secret peer-secret upstream-value Authorization: Bearer header-secret " +
		`"password":"quoted secret" mismatched:pair@target ` + strings.Repeat("界", 1000) + "\xff"
	safe := NodeIPCheckDiagnostic(message, node)
	for _, forbidden := range []string{"somewhere.example", "private.example", "ingress-name", "ingress-value", "node-secret", "peer-secret", "upstream-value", "header-secret", "quoted secret", "pair@target"} {
		if strings.Contains(safe, forbidden) {
			t.Fatalf("diagnostic retains sensitive value %q", forbidden)
		}
	}
	if len(safe) > 2000 || len(safe) < 1998 || !utf8.ValidString(safe) || !strings.Contains(safe, "handshake error") {
		t.Fatal("diagnostic was not capped on a UTF-8 boundary")
	}
	for _, header := range []string{"37", "Wed, 21 Oct 2015 07:28:00 GMT", "https://bad.example/sub/private", "1\r\nOther: value"} {
		limited := &IPCheckHTTPStatusError{StatusCode: http.StatusTooManyRequests, RetryAfter: header}
		joined := fmt.Errorf("HTTP and SOCKS checks: %w", errors.Join(&IPCheckHTTPStatusError{StatusCode: http.StatusBadGateway}, limited))
		valid := header == "37" || strings.HasSuffix(header, "GMT")
		if !IsIPCheckRateLimited(joined) || valid && IPCheckRetryAfter(joined) != header || !valid && (IPCheckRetryAfter(joined) != "" || strings.Contains(joined.Error(), header)) {
			t.Fatal("combined probe errors lost HTTP 429 or exposed an invalid Retry-After value")
		}
	}
}
