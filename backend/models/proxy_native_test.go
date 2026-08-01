package models

import (
	"encoding/json"
	"strings"
	"testing"

	appdb "sb-proxy/backend/database"
)

func TestListableStringAcceptsStringAndArray(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  string
		want []string
	}{
		{name: "string", raw: `"tcp"`, want: []string{"tcp"}},
		{name: "array", raw: `["tcp","udp"]`, want: []string{"tcp", "udp"}},
		{name: "empty", raw: `""`, want: nil},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var value ListableString
			if err := json.Unmarshal([]byte(testCase.raw), &value); err != nil {
				t.Fatalf("Unmarshal failed: %v", err)
			}
			if len(value) != len(testCase.want) {
				t.Fatalf("got %#v, want %#v", value, testCase.want)
			}
			for index := range value {
				if value[index] != testCase.want[index] {
					t.Fatalf("got %#v, want %#v", value, testCase.want)
				}
			}
		})
	}
}

func TestByteListUsesNumericJSONAndReadsHistoricalBase64(t *testing.T) {
	value := ByteList{162, 104, 222}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	if string(encoded) != `[162,104,222]` {
		t.Fatalf("reserved bytes must use numeric-array JSON, got %s", encoded)
	}

	for _, raw := range []string{`[162,104,222]`, `"omje"`, `"omje"`} {
		var decoded ByteList
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("Unmarshal(%s) failed: %v", raw, err)
		}
		if len(decoded) != 3 || decoded[0] != 162 || decoded[1] != 104 || decoded[2] != 222 {
			t.Fatalf("unexpected decoded bytes for %s: %#v", raw, decoded)
		}
	}
}

func TestProxyNodeParseConfigIsStrict(t *testing.T) {
	tests := []struct {
		name      string
		node      ProxyNode
		wantError string
	}{
		{
			name:      "unknown proxy type",
			node:      ProxyNode{Type: "made-up", Config: `{}`},
			wantError: "unsupported proxy type",
		},
		{
			name:      "unknown field",
			node:      ProxyNode{Type: "direct", Config: `{"not_a_sing_box_field":true}`},
			wantError: "unknown field",
		},
		{
			name:      "trailing value",
			node:      ProxyNode{Type: "direct", Config: `{} {}`},
			wantError: "trailing JSON value",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := testCase.node.ParseConfig()
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("expected error containing %q, got %v", testCase.wantError, err)
			}
		})
	}
}

func TestProxyNodeParseConfigAcceptsNativeUnionShapes(t *testing.T) {
	ssNode := ProxyNode{Type: "ss", Config: `{
		"server":"127.0.0.1","server_port":8388,"method":"aes-128-gcm","password":"secret",
		"network":"tcp","udp_over_tcp":{"enabled":true,"version":2},
		"domain_resolver":{"server":"local","strategy":"prefer_ipv4"}
	}`}
	parsedSS, err := ssNode.ParseConfig()
	if err != nil {
		t.Fatalf("parse shadowsocks native union config: %v", err)
	}
	ss := parsedSS.(*SSConfig)
	if len(ss.Network) != 1 || ss.Network[0] != "tcp" {
		t.Fatalf("unexpected network: %#v", ss.Network)
	}
	if _, ok := ss.UDPOverTCP.(map[string]interface{}); !ok {
		t.Fatalf("udp_over_tcp object was not preserved: %#v", ss.UDPOverTCP)
	}

	hy2Node := ProxyNode{Type: "hy2", Config: `{
		"server":"127.0.0.1","server_port":443,"password":"secret",
		"network":["tcp","udp"],"server_ports":"20000:30000",
		"obfs":{"type":"salamander","password":"obfs-secret"}
	}`}
	parsedHy2, err := hy2Node.ParseConfig()
	if err != nil {
		t.Fatalf("parse hysteria2 native shapes: %v", err)
	}
	hy2 := parsedHy2.(*Hysteria2Config)
	if len(hy2.Network) != 2 || len(hy2.ServerPorts) != 1 {
		t.Fatalf("native listable values were not preserved: network=%#v ports=%#v", hy2.Network, hy2.ServerPorts)
	}
	if _, ok := hy2.Obfs.(map[string]interface{}); !ok {
		t.Fatalf("native obfs object was not preserved: %#v", hy2.Obfs)
	}

	wireGuardNode := ProxyNode{Type: "wireguard", Config: `{
		"local_address":["10.0.0.2/32"],"private_key":"private",
		"routing_mark":16,"domain_resolver":{"server":"local","strategy":"prefer_ipv4"},
		"domain_resolver_options":{"server":"stale","disable_cache":true},"reserved":"AQID",
		"peers":[{"server":"127.0.0.1","server_port":51820,"public_key":"public","reserved":[4,5,6]}]
	}`}
	parsedWG, err := wireGuardNode.ParseConfig()
	if err != nil {
		t.Fatalf("parse wireguard native unions: %v", err)
	}
	wireGuard := parsedWG.(*WireGuardConfig)
	if wireGuard.RoutingMark != float64(16) {
		t.Fatalf("numeric routing_mark was not preserved: %#v", wireGuard.RoutingMark)
	}
	resolver, ok := wireGuard.DomainResolver.(map[string]interface{})
	if !ok || resolver["server"] != "local" || resolver["strategy"] != "prefer_ipv4" {
		t.Fatalf("wireguard object domain_resolver was not preserved: %#v", wireGuard.DomainResolver)
	}
	if wireGuard.DomainResolverOptions["server"] != "stale" || wireGuard.DomainResolverOptions["disable_cache"] != true {
		t.Fatalf("wireguard domain_resolver_options were not preserved: %#v", wireGuard.DomainResolverOptions)
	}
	if len(wireGuard.Reserved) != 3 || wireGuard.Reserved[2] != 3 || len(wireGuard.Peers[0].Reserved) != 3 {
		t.Fatalf("wireguard reserved compatibility failed: top=%#v peer=%#v", wireGuard.Reserved, wireGuard.Peers[0].Reserved)
	}
}

func TestProxyNodeParseConfigClearsRealityCredentialsOutsideRealityMode(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		tlsOptions string
	}{
		{name: "without native TLS", tlsOptions: ""},
		{name: "with inactive native Reality", tlsOptions: `,"tls_options":{"enabled":true,"reality":{"enabled":false,"public_key":"native","short_id":"01"}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			node := ProxyNode{Type: "vless", Config: `{
				"server":"127.0.0.1","server_port":443,"uuid":"id",
				"security":"tls","public_key":"stale","short_id":"deadbeef"` + testCase.tlsOptions + `
			}`}
			parsed, err := node.ParseConfig()
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			config := parsed.(*VLESSConfig)
			if config.PublicKey != "" || config.ShortID != "" {
				t.Fatalf("non-Reality mode retained flat credentials: %#v", config)
			}
			if _, exists := config.TLSOptions["reality"]; exists {
				t.Fatalf("non-Reality mode retained native Reality options: %#v", config.TLSOptions)
			}
		})
	}
}

func TestMySQLProxyConfigColumnUsesLongText(t *testing.T) {
	statements := schemaStatements(appdb.DialectMySQL)
	if len(statements) == 0 || !strings.Contains(statements[0], "config LONGTEXT NOT NULL") {
		t.Fatalf("mysql proxy_nodes.config must use LONGTEXT: %q", statements)
	}
	for _, dialect := range []appdb.Dialect{appdb.DialectPostgres, appdb.DialectSQLite} {
		statements := schemaStatements(dialect)
		if len(statements) == 0 || !strings.Contains(statements[0], "config TEXT NOT NULL") {
			t.Fatalf("%s proxy_nodes.config should remain TEXT: %q", dialect, statements)
		}
	}
}
