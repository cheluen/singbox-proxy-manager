package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"sb-proxy/backend/models"
)

func TestGroupUpstreamRouting(t *testing.T) {
	service := NewSingBoxService(t.TempDir(), WithBinaryPath(os.Getenv("SINGBOX_TEST_BINARY")))
	const activeCredential = "group-fixture-key"
	const inactiveCredential = "inactive-fixture-key"
	settings := models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "http", GlobalUpstreamConfig: `{"server":"192.0.2.9","server_port":8080}`,
		GroupUpstreams: map[int]models.GroupUpstream{
			40: {Enabled: true, ProxyDefinition: models.ProxyDefinition{Type: "socks5", Config: fmt.Sprintf(`{"server":"192.0.2.4","server_port":1080,"password":%q}`, activeCredential)}},
			7:  {Enabled: true, ProxyDefinition: models.ProxyDefinition{Type: "wireguard", Config: strings.Replace(testWireGuardFlatConfig, "{", `{"domain_resolver":"local",`, 1)}},
			8:  {ProxyDefinition: models.ProxyDefinition{Type: "http", Config: fmt.Sprintf(`{"password":%q}`, inactiveCredential)}},
		}}
	cases := []struct {
		group            int
		mode, tag, label string
	}{
		{40, "custom", "node-1-upstream", "custom: http"}, {40, "group", "managed-upstream-group-40", "group: socks5"},
		{40, "global", "managed-upstream-group-40", "group: socks5"}, {40, "none", "", ""},
		{8, "group", "", ""}, {8, "global", globalUpstreamTag, "global: http"}, {9, "global", globalUpstreamTag, "global: http"},
		{7, "group", "managed-upstream-group-7", "group: wireguard"},
	}
	var nodes []models.ProxyNode
	for index, item := range cases {
		nodes = append(nodes, models.ProxyNode{ID: index + 1, GroupID: item.group, Type: "direct", Enabled: true, Config: `{}`, UpstreamMode: item.mode, UpstreamType: "http", UpstreamConfig: `{"server":"192.0.2.5","server_port":80}`})
	}
	wiring, err := newWiring(nodes, settings)
	if err != nil {
		t.Fatal(err)
	}
	data, err := service.buildWiredConfig(t.Context(), nodes, settings, wiring)
	if err != nil {
		t.Fatal(err)
	}
	config := decodeGeneratedConfig(t, data)
	if os.Getenv("SINGBOX_TEST_BINARY") != "" {
		if err := service.ValidateConfigContext(t.Context(), data); err != nil {
			t.Fatal(err)
		}
	}
	generatedEntryByTag(t, config, "endpoints", "managed-upstream-group-7")
	if config["dns"] == nil || strings.Contains(string(data), "managed-upstream-group-8") {
		t.Fatal("incorrect DNS or disabled group configuration")
	}
	for index, item := range cases {
		node := nodes[index]
		out := generatedEntryByTag(t, config, "outbounds", fmt.Sprintf("node-%d-out", node.ID))
		var route ConnectionRoute
		route.setUpstream(wiring, node.ID)
		if item.tag == "" && out["type"] != "direct" || item.tag != "" && (out["type"] != "selector" || out["default"] != item.tag) || route.Upstream != item.label {
			t.Fatalf("group %d %s selected %v, label %q", item.group, item.mode, out, route.Upstream)
		}
	}
	parsed, _, err := prepareEnabledNodes(nodes)
	if err != nil {
		t.Fatal(err)
	}
	prepared, _, err := service.prepareManagedUpstreams(t.Context(), parsed, settings, 0)
	if err != nil {
		t.Fatal(err)
	}
	var tags []string
	for _, upstream := range prepared {
		tags = append(tags, upstream.Tag)
	}
	if !slices.Equal(tags, []string{globalUpstreamTag, "managed-upstream-group-7", "managed-upstream-group-40", "node-1-upstream"}) {
		t.Fatalf("unexpected upstream order %v", tags)
	}
	if got := wiring.redactSecrets.Replace(activeCredential + " " + inactiveCredential); got != "[redacted] [redacted]" {
		t.Fatal("group credentials were not redacted")
	}
	var tunnel struct {
		PrivateKey string `json:"private_key"`
	}
	if err := json.Unmarshal([]byte(testWireGuardFlatConfig), &tunnel); err != nil || tunnel.PrivateKey == "" || wiring.redactSecrets.Replace(tunnel.PrivateKey) != "[redacted]" {
		t.Fatal("group tunnel key was not redacted")
	}
	plan, err := service.prepareIsolatedIPCheck(t.Context(), nodes[2].ID, nodes, settings)
	if err != nil || len(plan.upstreams) != 1 || plan.upstreams[0].Tag != "managed-upstream-group-40" || !slices.Contains(plan.credentials, activeCredential) {
		t.Fatalf("isolated group path or diagnostic credentials: %v", err)
	}
}

func TestGroupUpstreamRealTraffic(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY is not set")
	}
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "100ms")
	_, globalConfig := liveMember(t, "127.0.0.2")
	_, groupConfig := liveMember(t, "127.0.0.3")
	_, customConfig := liveMember(t, "127.0.0.4")
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		_ = json.NewEncoder(writer).Encode(map[string]string{"ip": host, "country": "Example", "cc": "ZZ"})
	}))
	defer origin.Close()
	t.Setenv("SBPM_IPCHECK_URLS", origin.URL)
	settings := models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "http", GlobalUpstreamConfig: globalConfig, GroupUpstreams: map[int]models.GroupUpstream{1: {Enabled: true, ProxyDefinition: models.ProxyDefinition{Type: "http", Config: groupConfig}}}}
	cases := []struct {
		group    int
		mode, ip string
	}{{1, "custom", "127.0.0.4"}, {1, "global", "127.0.0.3"}, {1, "group", "127.0.0.3"}, {1, "none", "127.0.0.1"}, {2, "group", "127.0.0.1"}, {2, "global", "127.0.0.2"}}
	var nodes []models.ProxyNode
	for index, item := range cases {
		nodes = append(nodes, models.ProxyNode{ID: index + 1, GroupID: item.group, Enabled: true, Type: "direct", Config: "{}", InboundPort: loopbackTestPort(t), UpstreamMode: item.mode, UpstreamType: "http", UpstreamConfig: customConfig})
	}
	service := NewSingBoxService(t.TempDir(), WithBinaryPath(binary))
	t.Cleanup(func() { _ = service.Stop() })
	if err := NewRuntimeApplier(service).Apply(nodes, settings); err != nil {
		t.Fatal(err)
	}
	for index, item := range cases {
		info, err := CheckProxyIPContext(t.Context(), loopback(nodes[index].InboundPort), "", "")
		if err != nil || info.IP != item.ip {
			t.Fatalf("group %d mode %s: %+v, %v", item.group, item.mode, info, err)
		}
	}
	for _, index := range []int{1, 2} {
		target := nodes[index]
		target.InboundPort = 0
		info, err := service.CheckIsolatedNodeIPContext(t.Context(), target.ID, func(context.Context) ([]models.ProxyNode, models.Settings, error) {
			return []models.ProxyNode{target}, settings, nil
		}, CheckProxyIPContext)
		if err != nil || info.IP != cases[index].ip {
			t.Fatalf("portless %s: %+v, %v", target.UpstreamMode, info, err)
		}
	}
	info, err := service.CheckUpstreamIPContext(t.Context(), models.ProxyDefinition{Type: "direct", Config: "{}"}, nil)
	if err != nil || info.IP != "127.0.0.1" {
		t.Fatalf("standalone direct upstream check: %+v, %v", info, err)
	}
}

func TestManagedUpstreamBuildErrorRedactsEveryScope(t *testing.T) {
	const globalSecret, groupSecret, nodeSecret = "fixture-global-credential", "fixture-group-credential", "fixture-node-credential"
	diagnostic := strings.Join([]string{globalSecret, groupSecret, nodeSecret}, " ")
	settings := models.Settings{
		GlobalUpstreamEnabled: true, GlobalUpstreamType: "http", GlobalUpstreamConfig: fmt.Sprintf(`{"server":"192.0.2.1","server_port":8000,"username":%q}`, globalSecret),
		GroupUpstreams: map[int]models.GroupUpstream{1: {Enabled: true, ProxyDefinition: models.ProxyDefinition{
			Type: "http", Config: fmt.Sprintf(`{"server":"192.0.2.2","server_port":8000,"username":%q,"detour":%q}`, groupSecret, diagnostic),
		}}},
	}
	node := models.ProxyNode{ID: 1, GroupID: 1, Enabled: true, Type: "direct", Config: `{}`, UpstreamMode: "custom", UpstreamType: "http", UpstreamConfig: fmt.Sprintf(`{"server":"192.0.2.3","server_port":8000,"username":%q}`, nodeSecret)}
	err := NewSingBoxService(t.TempDir()).ValidateGroupUpstreamContext(t.Context(), []models.ProxyNode{node}, settings, 1)
	if !IsUpstreamValidationError(err) || !strings.Contains(err.Error(), "nested detour") {
		t.Fatal("expected a nested detour validation error")
	}
	for _, secret := range []string{globalSecret, groupSecret, nodeSecret} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("validation error exposed an upstream credential")
		}
	}
}

func TestUnusedGroupResolutionScope(t *testing.T) {
	service := NewSingBoxService(t.TempDir())
	resolver := &controlledNetIPResolver{err: errors.New("fixture DNS unavailable")}
	service.resolver = resolver
	settings := models.Settings{GroupUpstreams: map[int]models.GroupUpstream{2: {Enabled: true, ProxyDefinition: models.ProxyDefinition{Type: "http", Config: `{"server":"fixture.invalid","server_port":30101}`}}}}
	entry := models.ProxyNode{ID: 1, GroupID: 1, Enabled: true, InboundPort: 30101, Type: "direct", Config: "{}", UpstreamMode: "global"}
	nodes := []models.ProxyNode{entry}
	for _, id := range []int{2, 3} {
		if _, err := service.BuildGlobalConfigContext(t.Context(), nodes, settings); err != nil {
			t.Errorf("unrelated group blocked by unused upstream: %v", err)
		}
		nodes = append(nodes, models.ProxyNode{ID: id, GroupID: 1, Enabled: true, Type: "direct", Config: "{}", UpstreamMode: "global"})
	}
	if resolver.callCount() != 0 {
		t.Error("unused group triggered DNS lookups")
	}
	nodes[1].GroupID = 2
	if _, err := service.BuildGlobalConfigContext(t.Context(), nodes, settings); !IsUpstreamValidationError(err) || !strings.Contains(err.Error(), "cannot safely dial inbound port") {
		t.Fatalf("first active consumer did not validate group: %v", err)
	}
	if err := service.ValidateGroupUpstreamContext(t.Context(), []models.ProxyNode{entry}, settings, 2); !IsUpstreamValidationError(err) {
		t.Fatalf("explicit offline check: %v", err)
	}
}

func TestConnectionUpstreamSource(t *testing.T) {
	settings := models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "http", GroupUpstreams: map[int]models.GroupUpstream{2: {Enabled: true, ProxyDefinition: models.ProxyDefinition{Type: "socks5"}}}}
	for _, test := range []struct {
		mode          string
		group         int
		label, source string
	}{
		{"global", 1, "global: http", "global"}, {"global", 2, "group: socks5", "group"}, {"group", 2, "group: socks5", "group"},
		{"custom", 2, "custom: http", "custom"}, {"legacy", 2, "global: http", "legacy"}, {"none", 2, "", ""}, {"group", 1, "", ""},
	} {
		node := models.ProxyNode{ID: 1, Enabled: true, GroupID: test.group, Type: "http", UpstreamMode: test.mode, UpstreamType: "http", Config: `{"detour":"node-3-out"}`}
		wiring, err := newWiring([]models.ProxyNode{node, {ID: 3, Name: "global: http"}}, settings)
		if err != nil {
			t.Fatal(err)
		}
		var conn clashConnection
		conn.Metadata.Type = "mixed/node-1-in"
		route, ok := conn.route(wiring)
		encoded, err := json.Marshal(route)
		var output struct {
			Label  string `json:"upstream_label"`
			Source string `json:"upstream_source"`
		}
		if !ok || err != nil || json.Unmarshal(encoded, &output) != nil || output.Label != test.label || output.Source != test.source {
			t.Errorf("%s in group %d: got %q / %q", test.mode, test.group, output.Label, output.Source)
		}
	}
}
