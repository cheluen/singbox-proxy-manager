package services

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

	aliasServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not a subscription"))
	}))
	t.Cleanup(aliasServer.Close)
	aliasLink := aliasServer.URL + "?allow_insecure=1#proxy"
	items, failures, err = ExpandBatchImportSources(context.Background(), []string{aliasLink})
	if err != nil || len(failures) != 0 || len(items) != 1 || items[0].Link != aliasLink {
		t.Fatalf("third-party HTTP proxy alias not recognized after fetch: items=%#v failures=%#v err=%v", items, failures, err)
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
