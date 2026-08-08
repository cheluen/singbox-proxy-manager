package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"sb-proxy/backend/models"
)

func TestBatchImportAuditClashV2RayTransportsAndDialerAliases(t *testing.T) {
	yaml := `
proxies:
  - name: vless-ws
    type: vless
    server: v.example
    port: 443
    uuid: id-vless
    tls: true
    alpn: [h2, http/1.1]
    network: ws
    ws-opts:
      path: /ws
      headers: {Host: cdn.example}
    ip-version: ipv4-prefer
  - name: trojan-grpc
    type: trojan
    server: t.example
    port: 443
    password: pass
    network: grpc
    grpc-opts: {grpc-service-name: svc}
    ip-version: ipv6-prefer
  - name: vmess-h2
    type: vmess
    server: m.example
    port: 443
    uuid: id-vmess
    cipher: auto
    tls: true
    alpn: [h2, http/1.1]
    network: h2
    h2-opts:
      path: [/h2]
      host: [a.example, b.example]
  - name: vmess-gun-alias
    type: vmess
    server: gun.example
    port: 443
    uuid: id-vmess-gun
    cipher: auto
    tls: true
    network: gun
    grpc-opts: {grpc-service-name: gun-service}
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(failures) != 0 || len(items) != 4 {
		t.Fatalf("items=%d failures=%#v", len(items), failures)
	}
	vless := items[0].Config.(models.VLESSConfig)
	if vless.Network != "ws" || vless.DomainStrategy != "prefer_ipv4" || vless.ALPN != "h2,http/1.1" || len(vless.OutboundNetwork) != 0 {
		t.Fatalf("VLESS mapping mismatch: %#v", vless)
	}
	trojan := items[1].Config.(models.TrojanConfig)
	if trojan.Network != "grpc" || trojan.ServiceName != "svc" || trojan.Security != "tls" || trojan.DomainStrategy != "prefer_ipv6" {
		t.Fatalf("Trojan mapping mismatch: %#v", trojan)
	}
	vmess := items[2].Config.(models.VMESSConfig)
	if vmess.Network != "http" || vmess.Host != "a.example,b.example" || vmess.ALPN != "h2,http/1.1" {
		t.Fatalf("VMess H2 mapping mismatch: %#v", vmess)
	}
	gun := items[3].Config.(models.VMESSConfig)
	if gun.Network != "grpc" || gun.ServiceName != "gun-service" {
		t.Fatalf("VMess gun alias mapping mismatch: %#v", gun)
	}
}

func TestBatchImportAuditRejectsUnsupportedXraySecurityExtensions(t *testing.T) {
	yaml := `
proxies:
  - name: vless-pqv
    type: vless
    server: v.example
    port: 443
    uuid: id-vless
    reality-opts:
      public-key: public
      pqv: post-quantum-verify
  - name: vmess-pcs
    type: vmess
    server: m.example
    port: 443
    uuid: id-vmess
    cipher: auto
    tls: true
    pcs: certificate-hash
  - name: trojan-spx
    type: trojan
    server: t.example
    port: 443
    password: pass
    reality-opts:
      public-key: public
      spider-x: /
  - name: vless-final-mask
    type: vless
    server: f.example
    port: 443
    uuid: id-final-mask
    fm: '{"udp":[]}'
  - name: vmess-finalmask
    type: vmess
    server: finalmask.example
    port: 443
    uuid: id-finalmask-lowercase
    cipher: auto
    finalmask: {udp: []}
  - name: vmess-grpc-authority
    type: vmess
    server: authority.example
    port: 443
    uuid: id-authority
    cipher: auto
    network: grpc
    grpc-opts:
      grpc-service-name: service
      authority: authority.example
  - name: trojan-grpc-multi
    type: trojan
    server: multi.example
    port: 443
    password: pass
    network: grpc
    grpc-opts:
      grpc-service-name: service
      mode: multi
  - name: vmess-gun-multi-mode
    type: vmess
    server: gun-multi.example
    port: 443
    uuid: id-gun-multi
    cipher: auto
    network: gun
    grpc-opts:
      grpc-service-name: service
      multiMode: true
  - name: vless-grpc-user-agent-snake
    type: vless
    server: user-agent-snake.example
    port: 443
    uuid: id-user-agent-snake
    network: grpc
    grpc-opts:
      grpc-service-name: service
      user_agent: custom-agent
  - name: trojan-grpc-user-agent-camel
    type: trojan
    server: user-agent-camel.example
    port: 443
    password: pass
    network: grpc
    grpc-opts:
      grpc-service-name: service
      userAgent: custom-agent
  - name: vless-quic-options
    type: vless
    server: quic.example
    port: 443
    uuid: id-quic
    tls: true
    network: quic
    quic-opts:
      security: aes-128-gcm
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(items) != 0 || len(failures) != 11 {
		t.Fatalf("unsupported extensions must fail explicitly: items=%#v failures=%#v", items, failures)
	}
}

func TestBatchImportAuditClashPayloadAndNumericCredentials(t *testing.T) {
	yaml := `
payload:
  - name: numeric-ss
    type: ss
    server: ss.example
    port: 8388
    cipher: aes-128-gcm
    password: 123456
  - name: numeric-trojan
    type: trojan
    server: t.example
    port: 443
    password: 987654
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(failures) != 0 || len(items) != 2 {
		t.Fatalf("items=%d failures=%#v", len(items), failures)
	}
	if items[0].Config.(models.SSConfig).Password != "123456" || items[1].Config.(models.TrojanConfig).Password != "987654" {
		t.Fatalf("numeric credentials lost: %#v %#v", items[0].Config, items[1].Config)
	}
}

func TestBatchImportAuditShadowsocksPluginNormalization(t *testing.T) {
	yaml := `
proxies:
  - name: v2ray-map
    type: ss
    server: ss.example
    port: 8388
    cipher: aes-128-gcm
    password: pass
    plugin: v2ray-plugin
    plugin-opts: {mode: websocket, host: "a;b=c\\d", mux: true, tls: false}
  - name: v2ray-string
    type: ss
    server: ss.example
    port: 8388
    cipher: aes-128-gcm
    password: pass
    plugin: v2ray-plugin
    plugin-opts: "mode=websocket;mux=true"
  - name: obfs
    type: ss
    server: ss.example
    port: 8388
    cipher: aes-128-gcm
    password: pass
    plugin: obfs-local
    plugin-opts: {mode: tls, host: cdn.example}
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(failures) != 0 || len(items) != 3 {
		t.Fatalf("items=%d failures=%#v", len(items), failures)
	}
	first := items[0].Config.(models.SSConfig)
	if !strings.Contains(first.PluginOpts, "mux=1") || strings.Contains(first.PluginOpts, "tls") || !strings.Contains(first.PluginOpts, `host=a\;b\=c\\d`) {
		t.Fatalf("v2ray map options not normalized: %q", first.PluginOpts)
	}
	if got := items[1].Config.(models.SSConfig).PluginOpts; got != "mode=websocket;mux=1" {
		t.Fatalf("v2ray string options=%q", got)
	}
	if got := items[2].Config.(models.SSConfig).PluginOpts; got != "obfs=tls;obfs-host=cdn.example" {
		t.Fatalf("obfs-local options=%q", got)
	}
}

func TestBatchImportAuditWireGuardExplicitSinglePeer(t *testing.T) {
	yaml := `
proxies:
  - name: explicit-peer
    type: wireguard
    ip: 10.0.0.2/32
    private-key: private
    peers:
      - server: wg.example
        port: 51820
        public-key: public
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(failures) != 0 || len(items) != 1 {
		t.Fatalf("items=%d failures=%#v", len(items), failures)
	}
	cfg := items[0].Config.(models.WireGuardConfig)
	if len(cfg.Peers) != 1 || len(cfg.Peers[0].AllowedIPs) != 0 {
		t.Fatalf("explicit peer shape lost: %#v", cfg)
	}
	if len(cfg.AllowedIPs) != 0 {
		t.Fatalf("explicit empty allowed_ips must remain empty: %#v", cfg.AllowedIPs)
	}
}

func TestBatchImportAuditWireGuardKeepaliveDefaultsAllowedIPs(t *testing.T) {
	yaml := `
proxies:
  - name: keepalive-default-route
    type: wireguard
    server: 127.0.0.1
    port: 51820
    ip: 10.0.0.2/32
    private-key: ` + testWireGuardPrivateKey + `
    public-key: ` + testWireGuardPeerPublicKey + `
    persistent-keepalive-interval: 25
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(failures) != 0 || len(items) != 1 {
		t.Fatalf("items=%#v failures=%#v err=%v", items, failures, err)
	}
	config := items[0].Config.(models.WireGuardConfig)
	if len(config.Peers) != 1 || config.Peers[0].PersistentKeepaliveInterval != 25 {
		t.Fatalf("keepalive peer was not retained: %#v", config.Peers)
	}
	service := NewSingBoxService(t.TempDir())
	endpoint, err := service.generateWireGuardEndpoint(&config, "wireguard-keepalive")
	if err != nil {
		t.Fatalf("generate WireGuard endpoint: %v", err)
	}
	peers := endpoint.Extra["peers"].([]map[string]interface{})
	allowedIPs, _ := peers[0]["allowed_ips"].([]string)
	if len(allowedIPs) != 2 || allowedIPs[0] != "0.0.0.0/0" || allowedIPs[1] != "::/0" {
		t.Fatalf("keepalive peer did not receive default routes: %#v", peers[0])
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		configJSON, buildErr := service.BuildGlobalConfig([]models.ProxyNode{
			nativeTestNode(t, 310, items[0].Name, items[0].Type, items[0].Config),
		})
		if buildErr != nil {
			t.Fatalf("BuildGlobalConfig: %v", buildErr)
		}
		if validateErr := service.ValidateConfig(configJSON); validateErr != nil {
			t.Fatalf("sing-box rejected keepalive WireGuard config: %v\n%s", validateErr, configJSON)
		}
	}
}

func TestBatchImportAuditPreservesCredentialWhitespace(t *testing.T) {
	yaml := `
proxies:
  - {name: ss-space, type: ss, server: ss.example, port: 8388, cipher: aes-128-gcm, password: " ss secret "}
  - {name: trojan-space, type: trojan, server: trojan.example, port: 443, password: " trojan secret ", tls: false}
  - {name: hy2-space, type: hysteria2, server: hy2.example, port: 443, password: " hy2 secret ", obfs: salamander, obfs-password: " obfs secret "}
  - {name: tuic-space, type: tuic, server: tuic.example, port: 443, uuid: 00000000-0000-0000-0000-000000000031, password: " tuic secret "}
  - {name: anytls-space, type: anytls, server: anytls.example, port: 443, password: " anytls secret "}
  - {name: socks-space, type: socks5, server: socks.example, port: 1080, username: " socks user ", password: " socks secret "}
  - {name: http-space, type: http, server: http.example, port: 8080, username: " http user ", password: " http secret "}
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(failures) != 0 || len(items) != 7 {
		t.Fatalf("items=%#v failures=%#v err=%v", items, failures, err)
	}

	if got := items[0].Config.(models.SSConfig).Password; got != " ss secret " {
		t.Fatalf("Shadowsocks password changed: %q", got)
	}
	if got := items[1].Config.(models.TrojanConfig).Password; got != " trojan secret " {
		t.Fatalf("Trojan password changed: %q", got)
	}
	hy2 := items[2].Config.(models.Hysteria2Config)
	if hy2.Password != " hy2 secret " || hy2.ObfsPassword != " obfs secret " {
		t.Fatalf("Hysteria2 credentials changed: %#v", hy2)
	}
	if got := items[3].Config.(models.TUICConfig).Password; got != " tuic secret " {
		t.Fatalf("TUIC password changed: %q", got)
	}
	if got := items[4].Config.(models.AnyTLSConfig).Password; got != " anytls secret " {
		t.Fatalf("AnyTLS password changed: %q", got)
	}
	socks := items[5].Config.(models.SOCKS5Config)
	if socks.Username != " socks user " || socks.Password != " socks secret " {
		t.Fatalf("SOCKS credentials changed: %#v", socks)
	}
	httpConfig := items[6].Config.(models.HTTPProxyConfig)
	if httpConfig.Username != " http user " || httpConfig.Password != " http secret " {
		t.Fatalf("HTTP credentials changed: %#v", httpConfig)
	}
}

func TestBatchImportAuditAcceptsVMessCFBAndTUICEmptyPassword(t *testing.T) {
	yaml := `
proxies:
  - name: vmess-cfb
    type: vmess
    server: 127.0.0.1
    port: 10001
    uuid: 00000000-0000-0000-0000-000000000032
    cipher: aes-128-cfb
  - name: tuic-empty-password
    type: tuic
    server: 127.0.0.1
    port: 10002
    uuid: 00000000-0000-0000-0000-000000000033
    password: ""
    skip-cert-verify: true
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(failures) != 0 || len(items) != 2 {
		t.Fatalf("items=%#v failures=%#v err=%v", items, failures, err)
	}
	if got := items[0].Config.(models.VMESSConfig).Security; got != "aes-128-cfb" {
		t.Fatalf("VMess cipher=%q, want aes-128-cfb", got)
	}
	if got := items[1].Config.(models.TUICConfig).Password; got != "" {
		t.Fatalf("TUIC empty password changed: %q", got)
	}
}

func TestBatchImportAuditNormalizesClashECHAndAllowsDNSFallback(t *testing.T) {
	yaml := `
proxies:
  - name: ech-static
    type: vless
    server: example.com
    port: 443
    uuid: 00000000-0000-0000-0000-000000000034
    tls: true
    servername: example.com
    ech-opts:
      enable: true
      config: ` + protocolAuditECHConfigListBase64 + `
  - name: ech-dns
    type: vless
    server: example.com
    port: 443
    uuid: 00000000-0000-0000-0000-000000000035
    tls: true
    servername: example.com
    ech-opts:
      enable: true
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(failures) != 0 || len(items) != 2 {
		t.Fatalf("items=%#v failures=%#v err=%v", items, failures, err)
	}
	staticECH := nativeMap(items[0].Config.(models.VLESSConfig).TLSOptions["ech"])
	if staticECH["enabled"] != true || strings.Join(nativeStringSlice(staticECH["config"]), "\n") != protocolAuditECHConfigPEM {
		t.Fatalf("static ECH was not converted to sing-box PEM: %#v", staticECH)
	}
	dnsECH := nativeMap(items[1].Config.(models.VLESSConfig).TLSOptions["ech"])
	if dnsECH["enabled"] != true {
		t.Fatalf("DNS ECH was not enabled: %#v", dnsECH)
	}
	if _, exists := dnsECH["config"]; exists {
		t.Fatalf("DNS ECH must not synthesize a static config: %#v", dnsECH)
	}

	if realBinary := os.Getenv("SINGBOX_TEST_BINARY"); realBinary != "" {
		t.Setenv("SINGBOX_BINARY", realBinary)
		service := NewSingBoxService(t.TempDir())
		nodes := make([]models.ProxyNode, 0, len(items))
		for index, item := range items {
			nodes = append(nodes, nativeTestNode(t, 320+index, item.Name, item.Type, item.Config))
		}
		configJSON, buildErr := service.BuildGlobalConfig(nodes)
		if buildErr != nil {
			t.Fatalf("BuildGlobalConfig: %v", buildErr)
		}
		if validateErr := service.ValidateConfig(configJSON); validateErr != nil {
			t.Fatalf("sing-box rejected normalized Clash ECH configs: %v\n%s", validateErr, configJSON)
		}
	}
}

func TestBatchImportAuditHTTPProxyAndSubscriptionDisambiguation(t *testing.T) {
	proxyCfg := &models.HTTPProxyConfig{
		Server: "127.0.0.1", ServerPort: 65534, Path: "/proxy", DialerOptions: models.DialerOptions{Detour: "selector"},
	}
	proxyLink, err := buildHTTPProxyShareLink("anonymous", proxyCfg)
	if err != nil {
		t.Fatalf("buildHTTPProxyShareLink: %v", err)
	}
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{proxyLink})
	if err != nil || len(failures) != 0 || len(items) != 1 || items[0].Link != proxyLink {
		t.Fatalf("marked HTTP proxy was fetched/misclassified: items=%#v failures=%#v err=%v", items, failures, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "user" || password != "pass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("trojan://secret@example.com:443#subscription\n"))
	}))
	t.Cleanup(server.Close)
	authURL := strings.Replace(server.URL, "://", "://user:pass@", 1) + "/sub"
	items, failures, err = ExpandBatchImportSources(context.Background(), []string{authURL})
	if err != nil || len(failures) != 0 || len(items) != 1 || !strings.HasPrefix(items[0].Link, "trojan://") {
		t.Fatalf("basic-auth subscription not fetched first: items=%#v failures=%#v err=%v", items, failures, err)
	}

	var uppercaseHits atomic.Int32
	uppercaseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		uppercaseHits.Add(1)
		_, _ = w.Write([]byte("trojan://secret@example.com:443#uppercase-subscription\n"))
	}))
	t.Cleanup(uppercaseServer.Close)
	uppercaseURL := strings.Replace(uppercaseServer.URL, "http://", "HTTP://", 1)
	items, failures, err = ExpandBatchImportSources(context.Background(), []string{uppercaseURL})
	if err != nil || len(failures) != 0 || len(items) != 1 || !strings.HasPrefix(items[0].Link, "trojan://") || uppercaseHits.Load() != 1 {
		t.Fatalf("uppercase HTTP subscription was not fetched: hits=%d items=%#v failures=%#v err=%v", uppercaseHits.Load(), items, failures, err)
	}

	aliasServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not a subscription"))
	}))
	t.Cleanup(aliasServer.Close)
	aliasLink := aliasServer.URL + "?allow_insecure=1#proxy"
	items, failures, err = ExpandBatchImportSources(context.Background(), []string{aliasLink})
	if err != nil || len(failures) != 1 || len(items) != 0 {
		t.Fatalf("invalid subscription payload silently became a proxy: items=%#v failures=%#v err=%v", items, failures, err)
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancel()
	defaultPortProxy := "http://127.0.0.1"
	items, failures, err = ExpandBatchImportSources(cancelledContext, []string{defaultPortProxy})
	if err != nil || len(failures) != 1 || len(items) != 0 {
		t.Fatalf("failed subscription silently became a proxy: items=%#v failures=%#v err=%v", items, failures, err)
	}

	items, failures, err = ExpandBatchImportSourcesWithType(
		context.Background(), []string{defaultPortProxy}, BatchImportSourceHTTPProxy,
	)
	if err != nil || len(failures) != 0 || len(items) != 1 || items[0].Link != defaultPortProxy {
		t.Fatalf("explicit anonymous HTTP proxy was not imported directly: items=%#v failures=%#v err=%v", items, failures, err)
	}
}

func TestBatchImportAuditSubscriptionHTTPFailuresNeverBecomeProxyNodes(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)

			items, failures, err := ExpandBatchImportSources(context.Background(), []string{server.URL})
			if err != nil || len(items) != 0 || len(failures) != 1 {
				t.Fatalf("HTTP %d subscription became an item: items=%#v failures=%#v err=%v", status, items, failures, err)
			}
			if !strings.Contains(failures[0].Error, strconv.Itoa(status)) {
				t.Fatalf("failure does not expose status %d: %#v", status, failures[0])
			}
		})
	}
}

func TestBatchImportAuditHTMLSubscriptionCannotImportPageLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>\nhttp://127.0.0.1:8080\n</body></html>"))
	}))
	t.Cleanup(server.Close)

	items, failures, err := ExpandBatchImportSources(context.Background(), []string{server.URL})
	if err != nil || len(items) != 0 || len(failures) != 1 {
		t.Fatalf("HTML page links became proxy nodes: items=%#v failures=%#v err=%v", items, failures, err)
	}
	if !strings.Contains(failures[0].Error, "HTML") {
		t.Fatalf("unexpected HTML subscription failure: %#v", failures[0])
	}
}

func TestBatchImportAuditMislabelledBOMHTMLCannotImportPageLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("\uFEFF \n<head><title>not a subscription</title></head>\nhttp://127.0.0.1:8080"))
	}))
	t.Cleanup(server.Close)

	items, failures, err := ExpandBatchImportSources(context.Background(), []string{server.URL})
	if err != nil || len(items) != 0 || len(failures) != 1 {
		t.Fatalf("mislabelled BOM HTML page links became proxy nodes: items=%#v failures=%#v err=%v", items, failures, err)
	}
	if !strings.Contains(failures[0].Error, "HTML") {
		t.Fatalf("unexpected mislabelled BOM HTML subscription failure: %#v", failures[0])
	}
}

func TestBatchImportAuditClashAllowsExplicitEmptyProtocolPasswords(t *testing.T) {
	yaml := `
proxies:
  - {name: empty-trojan, type: trojan, server: trojan.example, port: 443, password: ""}
  - {name: empty-hy2, type: hysteria2, server: hy2.example, port: 443, password: ""}
  - {name: empty-anytls, type: anytls, server: anytls.example, port: 443, password: ""}
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(failures) != 0 || len(items) != 3 {
		t.Fatalf("explicit empty passwords were rejected: items=%#v failures=%#v err=%v", items, failures, err)
	}
	for _, item := range items {
		encoded, marshalErr := json.Marshal(item.Config)
		if marshalErr != nil {
			t.Fatalf("marshal %s: %v", item.Name, marshalErr)
		}
		var config map[string]interface{}
		if unmarshalErr := json.Unmarshal(encoded, &config); unmarshalErr != nil {
			t.Fatalf("unmarshal %s: %v", item.Name, unmarshalErr)
		}
		if password, exists := config["password"]; !exists || password != "" {
			t.Fatalf("%s did not preserve explicit empty password: %#v", item.Name, config)
		}
	}
}

func TestBatchImportAuditClashStillRejectsMissingProtocolPasswords(t *testing.T) {
	yaml := `
proxies:
  - {name: missing-trojan, type: trojan, server: trojan.example, port: 443}
  - {name: missing-hy2, type: hysteria2, server: hy2.example, port: 443}
  - {name: missing-anytls, type: anytls, server: anytls.example, port: 443}
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(items) != 0 || len(failures) != 3 {
		t.Fatalf("missing passwords were not rejected independently: items=%#v failures=%#v err=%v", items, failures, err)
	}
	for _, failure := range failures {
		if !strings.Contains(failure.Error, "missing password") {
			t.Fatalf("unexpected missing-password error: %#v", failure)
		}
	}
}

func TestBatchImportAuditHysteria2SalamanderStillRequiresPassword(t *testing.T) {
	yaml := `
proxies:
  - {name: invalid-obfs, type: hysteria2, server: hy2.example, port: 443, password: "", obfs: salamander, obfs-password: ""}
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(items) != 0 || len(failures) != 1 {
		t.Fatalf("empty salamander password was not isolated: items=%#v failures=%#v err=%v", items, failures, err)
	}
	if !strings.Contains(failures[0].Error, "obfs-password") {
		t.Fatalf("unexpected salamander validation error: %#v", failures[0])
	}
}

func TestBatchImportAuditShortBase64Subscription(t *testing.T) {
	payload := "ss://YWVzLTEyOC1nY206cA@example.com:1"
	encoded := base64.RawStdEncoding.EncodeToString([]byte(payload))
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{encoded})
	if err != nil || len(failures) != 0 || len(items) != 1 || items[0].Link != payload {
		t.Fatalf("short base64 subscription failed: items=%#v failures=%#v err=%v", items, failures, err)
	}
}

func TestBatchImportAuditRealSingBoxAcceptsNormalizedSSPlugins(t *testing.T) {
	realBinary := os.Getenv("SINGBOX_TEST_BINARY")
	if realBinary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}
	yaml := `
proxies:
  - name: v2ray
    type: ss
    server: 127.0.0.1
    port: 8388
    cipher: aes-128-gcm
    password: 123456
    plugin: v2ray-plugin
    plugin-opts: "mode=websocket;host=example.com;mux=true"
  - name: obfs
    type: ss
    server: 127.0.0.1
    port: 8389
    cipher: aes-128-gcm
    password: secret
    plugin: obfs-local
    plugin-opts: {mode: tls, host: example.com}
`
	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil || len(failures) != 0 || len(items) != 2 {
		t.Fatalf("import plugins: items=%#v failures=%#v err=%v", items, failures, err)
	}
	t.Setenv("SINGBOX_BINARY", realBinary)
	service := NewSingBoxService(t.TempDir())
	for index, item := range items {
		node := nativeTestNode(t, index+1, item.Name, item.Type, item.Config)
		configJSON, err := service.BuildGlobalConfig([]models.ProxyNode{node})
		if err != nil {
			t.Fatalf("BuildGlobalConfig %s: %v", item.Name, err)
		}
		if err := service.ValidateConfig(configJSON); err != nil {
			t.Fatalf("sing-box rejected normalized %s plugin: %v\n%s", item.Name, err, configJSON)
		}
	}
}
