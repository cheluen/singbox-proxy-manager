package services

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"sb-proxy/backend/models"
)

func TestExpandBatchImportSources_Base64Subscription(t *testing.T) {
	src := "trojan://pass123@example.com:443#A\nvless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com#B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(src))

	items, failures, err := ExpandBatchImportSources(context.Background(), []string{encoded})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Link == "" || items[1].Link == "" {
		t.Fatalf("expected link items, got %+v", items)
	}
}

func TestExpandBatchImportSources_ClashMetaYAML(t *testing.T) {
	yaml := `
proxies:
  - name: "SS1"
    type: ss
    server: ss.example.com
    port: 8388
    cipher: aes-128-gcm
    password: "pass"
  - name: "VLESS1"
    type: vless
    server: vless.example.com
    port: 443
    uuid: "11111111-1111-1111-1111-111111111111"
    tls: true
    servername: example.com
    network: ws
    ws-opts:
      path: /ws
      headers:
        Host: example.com
  - name: "HY2"
    type: hysteria2
    server: hy2.example.com
    port: 443
    password: "p"
    up: "30 Mbps"
    down: "200 Mbps"
    hop-interval: 30
    alpn: [h3]
    skip-cert-verify: true
  - name: "TUIC"
    type: tuic
    server: tuic.example.com
    port: 443
    uuid: "22222222-2222-2222-2222-222222222222"
    password: "pp"
    heartbeat-interval: 10000
    alpn: [h3]
    reduce-rtt: true
    disable-sni: true
    skip-cert-verify: true
  - name: "AnyTLS"
    type: anytls
    server: anytls.example.com
    port: 443
    password: "pw"
    idle-session-check-interval: 30
    idle-session-timeout: 30
    min-idle-session: 5
    alpn: [h2, http/1.1]
    skip-cert-verify: true
  - name: "HTTP1"
    type: http
    server: http.example.com
    port: 8080
    tls: false
    username: u
    password: p
  - name: "SOCKS1"
    type: socks5
    server: socks.example.com
    port: 1080
    username: u
    password: p
`

	items, failures, err := ExpandBatchImportSources(context.Background(), []string{yaml})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	if len(items) != 7 {
		t.Fatalf("expected 7 items, got %d", len(items))
	}

	var foundHY2, foundAnyTLS, foundTUIC bool
	for _, item := range items {
		switch item.Type {
		case "hy2":
			foundHY2 = true
			cfg, ok := item.Config.(models.Hysteria2Config)
			if !ok {
				t.Fatalf("hy2 config type mismatch: %T", item.Config)
			}
			if cfg.HopInterval != "30s" {
				t.Fatalf("expected hop_interval=30s, got %q", cfg.HopInterval)
			}
		case "anytls":
			foundAnyTLS = true
			cfg, ok := item.Config.(models.AnyTLSConfig)
			if !ok {
				t.Fatalf("anytls config type mismatch: %T", item.Config)
			}
			if cfg.IdleSessionCheckInterval != "30s" || cfg.IdleSessionTimeout != "30s" {
				t.Fatalf("unexpected anytls durations: check=%q timeout=%q", cfg.IdleSessionCheckInterval, cfg.IdleSessionTimeout)
			}
		case "tuic":
			foundTUIC = true
			cfg, ok := item.Config.(models.TUICConfig)
			if !ok {
				t.Fatalf("tuic config type mismatch: %T", item.Config)
			}
			if cfg.Heartbeat != "10s" {
				t.Fatalf("expected heartbeat=10s, got %q", cfg.Heartbeat)
			}
			if !cfg.ZeroRTTHandshake {
				t.Fatalf("expected zero_rtt_handshake=true")
			}
		}
	}

	if !foundHY2 || !foundAnyTLS || !foundTUIC {
		t.Fatalf("expected hy2/anytls/tuic items, got %+v", items)
	}
}

func TestExpandBatchImportSources_SubscriptionURL(t *testing.T) {
	sub := "trojan://pass123@example.com:443#A\nvless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls#B\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(sub))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sub" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(encoded))
	}))
	t.Cleanup(srv.Close)

	items, failures, err := ExpandBatchImportSources(context.Background(), []string{srv.URL + "/sub"})
	if err != nil {
		t.Fatalf("ExpandBatchImportSources: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
}
