package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"sb-proxy/backend/models"
)

func TestClashWireGuardPreservesDialerOptions(testContext *testing.T) {
	tests := []struct {
		name       string
		fragment   string
		wantValue  bool
		wantExists bool
	}{
		{name: "false", fragment: "    udp-fragment: false\n", wantExists: true},
		{name: "true", fragment: "    udp-fragment: true\n", wantValue: true, wantExists: true},
		{name: "hyphen alias takes precedence", fragment: "    udp-fragment: false\n    udp_fragment: true\n", wantExists: true},
		{name: "omitted"},
	}
	for _, test := range tests {
		testContext.Run(test.name, func(testContext *testing.T) {
			content := fmt.Sprintf(`proxies:
  - name: wireguard-dialer
    type: wireguard
    server: wg.example
    port: 51820
    ip: 10.0.0.2
    private-key: %s
    public-key: %s
    routing-mark: 42
    connect-timeout: 0.5
%s`, testWireGuardPrivateKey, testWireGuardPeerPublicKey, test.fragment)
			items, failures, err := ExpandBatchImportSourcesWithType(context.Background(), []string{content}, BatchImportSourceAuto)
			if err != nil || len(failures) != 0 || len(items) != 1 {
				testContext.Fatalf("Clash import: items=%d failures=%+v err=%v", len(items), failures, err)
			}
			node := nativeTestNode(testContext, 1, items[0].Name, items[0].Type, items[0].Config)
			parsed, err := node.ParseConfig()
			if err != nil {
				testContext.Fatal(err)
			}
			config := parsed.(*models.WireGuardConfig)
			if intFromAny(config.RoutingMark) != 42 || config.ConnectTimeout != "500ms" {
				testContext.Fatalf("dialer fields were lost: %+v", config.DialerOptions)
			}
			endpoint, err := (&SingBoxService{}).generateWireGuardEndpoint(config, "test")
			if err != nil {
				testContext.Fatal(err)
			}
			if intFromAny(endpoint.Extra["routing_mark"]) != 42 || endpoint.Extra["connect_timeout"] != "500ms" {
				testContext.Fatalf("generated dialer fields were lost: %+v", endpoint.Extra)
			}
			fragment, exists := endpoint.Extra["udp_fragment"]
			if exists != test.wantExists || (exists && fragment != test.wantValue) {
				testContext.Fatalf("udp_fragment=%v exists=%t, want %t/%t", fragment, exists, test.wantValue, test.wantExists)
			}
		})
	}
}

func TestWireGuardEmbeddedDialerKeepsNativeResolverCompatibility(testContext *testing.T) {
	config := models.WireGuardConfig{
		Server: "198.51.100.2", ServerPort: 51820, LocalAddress: []string{"10.0.0.2/32"},
		PrivateKey: testWireGuardPrivateKey, PeerPublicKey: testWireGuardPeerPublicKey,
		DomainResolverOptions: models.NativeOptions{"server": "local", "strategy": "prefer_ipv4", "disable_cache": true},
	}
	resolver, err := configDomainResolverValue(&config)
	if err != nil || nativeMap(resolver)["server"] != "local" || nativeMap(resolver)["strategy"] != "prefer_ipv4" {
		testContext.Fatalf("embedded dialer bypassed the WireGuard resolver merge: resolver=%+v err=%v", resolver, err)
	}
	node := nativeTestNode(testContext, 1, "wireguard-resolver", "wireguard", config)
	data, err := (&SingBoxService{}).BuildGlobalConfigContext(testContext.Context(), []models.ProxyNode{node})
	if err != nil {
		testContext.Fatal(err)
	}
	generated := decodeGeneratedConfig(testContext, data)
	if generated["dns"] == nil {
		testContext.Fatal("native-only WireGuard local resolver did not enable generated local DNS")
	}
}

func TestCompatibilityTextListsSurviveNativeReconciliation(testContext *testing.T) {
	for _, proxyType := range []string{"vless", "vmess"} {
		for _, flat := range []bool{false, true} {
			testContext.Run(fmt.Sprintf("%s/flat=%t", proxyType, flat), func(testContext *testing.T) {
				fields := map[string]any{
					"server": "proxy.example", "server_port": 443,
					"uuid": "00000000-0000-0000-0000-000000000301", "network": "http",
					"tls_options":       map[string]any{"enabled": true, "alpn": []string{"native-alpn"}, "min_version": "1.2"},
					"transport_options": map[string]any{"type": "http", "host": []string{"native.example"}, "method": "POST"},
				}
				if proxyType == "vmess" {
					fields["security"], fields["tls"] = "auto", "tls"
				} else {
					fields["security"] = "tls"
				}
				wantALPN, wantHosts := []string{"native-alpn"}, []string{"native.example"}
				if flat {
					fields["alpn"] = " h2,\r\nhttp/1.1 ; h3\r "
					fields["host"] = " a.example,\nb.example ; c.example\r "
					wantALPN, wantHosts = []string{"h2", "http/1.1", "h3"}, []string{"a.example", "b.example", "c.example"}
					if got := parseCommaSeparatedList(fields["alpn"].(string)); !reflect.DeepEqual(got, wantALPN) {
						testContext.Fatalf("share-link list=%v, want %v", got, wantALPN)
					}
				}
				node := nativeTestNode(testContext, 1, "lists", proxyType, fields)
				parsed, err := node.ParseConfig()
				if err != nil {
					testContext.Fatal(err)
				}
				outbound, err := (&SingBoxService{}).generateOutboundFromConfig(proxyType, parsed, "test")
				if err != nil {
					testContext.Fatal(err)
				}
				tlsOptions, transport := nativeMap(outbound.Extra["tls"]), nativeMap(outbound.Extra["transport"])
				if got := nativeStringSlice(tlsOptions["alpn"]); !reflect.DeepEqual(got, wantALPN) {
					testContext.Fatalf("generated alpn=%v, want %v", got, wantALPN)
				}
				if got := nativeStringSlice(transport["host"]); !reflect.DeepEqual(got, wantHosts) {
					testContext.Fatalf("generated host=%v, want %v", got, wantHosts)
				}
				if tlsOptions["min_version"] != "1.2" || transport["method"] != "POST" {
					testContext.Fatalf("native-only fields were lost: TLS=%+v transport=%+v", tlsOptions, transport)
				}
			})
		}
	}
}

func TestClashMixedHeadersSurvivePersistenceGenerationAndShareRoundTrip(testContext *testing.T) {
	wantHeaders := map[string]any{"Host": []any{"a.example", "b.example"}, "X-Test": "trace"}
	for _, proxyType := range []string{"vless", "vmess", "trojan"} {
		for _, transportType := range []string{"ws", "http", "httpupgrade"} {
			testContext.Run(proxyType+"/"+transportType, func(testContext *testing.T) {
				content := fmt.Sprintf(`proxies:
  - name: mixed-headers
    type: %s
    server: proxy.example
    port: 443
    uuid: 00000000-0000-0000-0000-000000000302
    password: test
    cipher: auto
    tls: true
    path: /root
    host: root.example
    network: %s
    %s-opts:
      path: /stream
      headers:
        Host: [a.example, b.example]
        X-Test: trace
`, proxyType, transportType, transportType)
				items, failures, err := ExpandBatchImportSourcesWithType(context.Background(), []string{content}, BatchImportSourceAuto)
				if err != nil || len(items) != 1 || len(failures) != 0 {
					testContext.Fatalf("Clash import: items=%d failures=%+v err=%v", len(items), failures, err)
				}
				node := nativeTestNode(testContext, 1, "mixed-headers", proxyType, items[0].Config)
				var stored map[string]any
				if err := json.Unmarshal([]byte(node.Config), &stored); err != nil {
					testContext.Fatal(err)
				}
				if _, exists := stored["headers"]; exists {
					testContext.Fatalf("mixed headers must not produce a lossy flat headers field: %+v", stored)
				}
				if got := nativeMap(nativeMap(stored["transport_options"])["headers"]); !reflect.DeepEqual(got, wantHeaders) {
					testContext.Fatalf("persisted headers=%+v, want %+v", got, wantHeaders)
				}
				for stage := 0; stage < 2; stage++ {
					parsed, err := node.ParseConfig()
					if err != nil {
						testContext.Fatal(err)
					}
					outbound, err := (&SingBoxService{}).generateOutboundFromConfig(proxyType, parsed, "test")
					if err != nil {
						testContext.Fatal(err)
					}
					if got := nativeMap(nativeMap(outbound.Extra["transport"])["headers"]); !reflect.DeepEqual(got, wantHeaders) {
						testContext.Fatalf("stage %d generated headers=%+v, want %+v", stage, got, wantHeaders)
					}
					if stage == 0 {
						link, err := BuildShareLink(node)
						if err != nil {
							testContext.Fatal(err)
						}
						reimported, importedType, name, err := ParseShareLink(link)
						if err != nil || importedType != proxyType {
							testContext.Fatalf("share reimport: type=%s err=%v", importedType, err)
						}
						node = nativeTestNode(testContext, 1, name, importedType, reimported)
					}
				}
			})
		}
	}
}

func TestClashStringHeadersRemainFormCompatible(testContext *testing.T) {
	want := map[string]string{"Host": "cdn.example", "X-Test": "trace"}
	got := clashTransportHeaders(models.NativeOptions{"headers": map[string]any{"Host": "cdn.example", "X-Test": "trace"}})
	if !reflect.DeepEqual(got, want) {
		testContext.Fatalf("string headers=%+v, want %+v", got, want)
	}
}

func TestClashGRPCServiceNameAliasesStayConsistent(testContext *testing.T) {
	for _, proxyType := range []string{"vless", "vmess", "trojan"} {
		for _, alias := range []string{"grpc-service-name", "serviceName", "service-name", "service_name"} {
			testContext.Run(proxyType+"/"+alias, func(testContext *testing.T) {
				content := fmt.Sprintf("proxies:\n  - {name: grpc, type: %s, server: proxy.example, port: 443, uuid: test-id, password: test, network: grpc, grpc-opts: {%s: service}}\n", proxyType, alias)
				items, failures, err := ExpandBatchImportSourcesWithType(context.Background(), []string{content}, BatchImportSourceAuto)
				if err != nil || len(items) != 1 || len(failures) != 0 {
					testContext.Fatalf("Clash import: items=%d failures=%+v err=%v", len(items), failures, err)
				}
				node := nativeTestNode(testContext, 1, "grpc", proxyType, items[0].Config)
				var stored map[string]any
				if err := json.Unmarshal([]byte(node.Config), &stored); err != nil {
					testContext.Fatal(err)
				}
				if nativeMap(stored["transport_options"])["service_name"] != "service" {
					testContext.Fatalf("原生 gRPC 服务名丢失：%+v", stored)
				}
				if alias == "service-name" {
					if _, present := stored["service_name"]; present {
						testContext.Fatal("不能新增历史上不存在的扁平 gRPC 字段")
					}
				} else if stored["service_name"] != "service" {
					testContext.Fatalf("扁平 gRPC 服务名丢失：%+v", stored)
				}
				data, err := (&SingBoxService{}).BuildGlobalConfigContext(testContext.Context(), []models.ProxyNode{node})
				if err != nil {
					testContext.Fatal(err)
				}
				found := false
				for _, value := range decodeGeneratedConfig(testContext, data)["outbounds"].([]any) {
					outbound := nativeMap(value)
					if outbound["type"] == proxyType {
						found = true
						if nativeMap(outbound["transport"])["service_name"] != "service" {
							testContext.Fatalf("生成配置丢失 gRPC 服务名：%+v", outbound)
						}
					}
				}
				if !found {
					testContext.Fatal("生成配置没有协议出站")
				}
			})
		}
	}
}

func TestVMessJSONAndURLShareTLSReconciliation(testContext *testing.T) {
	tests := []struct {
		name      string
		fields    map[string]any
		wantMode  string
		wantError string
	}{
		{name: "none rejects Reality", fields: map[string]any{"tls": "none", "pbk": protocolAuditRealityPublicKey}, wantError: "Reality parameters require security=reality"},
		{name: "explicit empty rejects Reality", fields: map[string]any{"tls": "", "pbk": protocolAuditRealityPublicKey}, wantError: "Reality parameters require security=reality"},
		{name: "TLS rejects Reality", fields: map[string]any{"tls": "tls", "pbk": protocolAuditRealityPublicKey}, wantError: "Reality parameters require security=reality"},
		{name: "missing mode infers Reality", fields: map[string]any{"pbk": protocolAuditRealityPublicKey, "sid": "0102", "sni": "peer.example"}, wantMode: "reality"},
		{name: "Reality needs public key", fields: map[string]any{"tls": "reality"}, wantError: "missing public key"},
		{name: "Reality conflicts with ECH", fields: map[string]any{"tls": "reality", "pbk": protocolAuditRealityPublicKey, "ech": protocolAuditECHConfigListBase64}, wantError: "Reality is incompatible with ECH"},
		{name: "TLS and ECH", fields: map[string]any{"tls": "tls", "ech": protocolAuditECHConfigListBase64}, wantMode: "tls"},
		{name: "TLS fields", fields: map[string]any{"tls": "tls", "sni": "peer.example", "alpn": "h2;http/1.1", "allowInsecure": true, "fp": "Chrome"}, wantMode: "tls"},
		{name: "plain", wantMode: "none"},
	}
	for _, test := range tests {
		testContext.Run(test.name, func(testContext *testing.T) {
			fields := map[string]any{"add": "proxy.example", "port": 443, "id": "00000000-0000-0000-0000-000000000303", "net": "tcp", "scy": "auto"}
			query := url.Values{"type": {"tcp"}, "encryption": {"auto"}}
			for key, value := range test.fields {
				fields[key] = value
				if key == "tls" {
					key = "security"
				}
				text, _ := scalarString(value)
				query.Set(key, text)
			}
			encoded, err := json.Marshal(fields)
			if err != nil {
				testContext.Fatal(err)
			}
			links := []string{
				"vmess://" + base64.RawURLEncoding.EncodeToString(encoded),
				"vmess://00000000-0000-0000-0000-000000000303@proxy.example:443?" + query.Encode(),
			}
			var firstTLS map[string]any
			for index, link := range links {
				parsed, proxyType, name, err := ParseShareLink(link)
				if test.wantError != "" {
					if err == nil || !strings.Contains(err.Error(), test.wantError) || strings.Contains(err.Error(), "invalid vmess ECH config") {
						testContext.Fatalf("format %d error=%v, want %q without false ECH context", index, err, test.wantError)
					}
					continue
				}
				if err != nil {
					testContext.Fatalf("format %d parse: %v", index, err)
				}
				wantMode := test.wantMode
				if index == 0 {
					if _, modePresent := test.fields["tls"]; !modePresent || wantMode == "none" {
						wantMode = ""
					}
				}
				if mode := parsed.(models.VMESSConfig).TLS; mode != wantMode {
					testContext.Fatalf("format %d mode=%s, want %s", index, mode, wantMode)
				}
				node := nativeTestNode(testContext, 1, name, proxyType, parsed)
				persisted, err := node.ParseConfig()
				if err != nil {
					testContext.Fatal(err)
				}
				outbound, err := (&SingBoxService{}).generateVMESSOutbound(persisted.(*models.VMESSConfig), "test")
				if err != nil {
					testContext.Fatalf("format %d generate: %v", index, err)
				}
				tlsOptions := nativeMap(outbound.Extra["tls"])
				if test.wantMode == "none" {
					if len(tlsOptions) != 0 {
						testContext.Fatalf("format %d plain VMess enabled TLS: %+v", index, tlsOptions)
					}
				} else if tlsOptions["enabled"] != true {
					testContext.Fatalf("format %d TLS is not enabled: %+v", index, tlsOptions)
				}
				if test.wantMode == "reality" && (nativeMap(tlsOptions["reality"])["public_key"] != protocolAuditRealityPublicKey || nativeMap(tlsOptions["utls"])["enabled"] != true) {
					testContext.Fatalf("format %d Reality was lost: %+v", index, tlsOptions)
				}
				if index == 0 {
					firstTLS = tlsOptions
				} else if !reflect.DeepEqual(firstTLS, tlsOptions) {
					testContext.Fatalf("JSON/URL TLS differ: JSON=%+v URL=%+v", firstTLS, tlsOptions)
				}
			}
		})
	}
}

func TestClashDurationScalarFormats(testContext *testing.T) {
	tests := []struct {
		name         string
		value        any
		seconds      string
		milliseconds string
	}{
		{name: "integer", value: 5, seconds: "5s", milliseconds: "5ms"},
		{name: "int64", value: int64(500), seconds: "500s", milliseconds: "500ms"},
		{name: "whole float", value: float64(500), seconds: "500s", milliseconds: "500ms"},
		{name: "float", value: 0.5, seconds: "500ms", milliseconds: "500µs"},
		{name: "float32", value: float32(0.5), seconds: "500ms", milliseconds: "500µs"},
		{name: "integer string", value: "500", seconds: "500s", milliseconds: "500ms"},
		{name: "decimal string", value: " 500.5 ", seconds: "500.5", milliseconds: "500.5ms"},
		{name: "JSON number", value: json.Number("500.5"), seconds: "8m20.5s", milliseconds: "500.5ms"},
		{name: "explicit seconds", value: "0.5s", seconds: "0.5s", milliseconds: "0.5s"},
		{name: "explicit minutes", value: "1m", seconds: "1m", milliseconds: "1m"},
		{name: "explicit milliseconds", value: "5000ms", seconds: "5000ms", milliseconds: "5000ms"},
		{name: "zero", value: 0},
		{name: "zero string", value: "0", seconds: "0", milliseconds: "0"},
		{name: "negative", value: -0.5},
		{name: "negative string", value: "-500", seconds: "-500", milliseconds: "-500"},
		{name: "explicit zero", value: "0ms", seconds: "0ms", milliseconds: "0ms"},
		{name: "explicit negative", value: "-1s", seconds: "-1s", milliseconds: "-1s"},
		{name: "nil"},
		{name: "ignored bool", value: false},
		{name: "invalid NaN", value: math.NaN(), seconds: "NaN", milliseconds: "NaN"},
		{name: "invalid infinity", value: math.Inf(1), seconds: "+Inf", milliseconds: "+Inf"},
		{name: "invalid overflow", value: 1e15 + 0.5, seconds: "1000000000000000.5", milliseconds: "1000000000000000.5"},
		{name: "invalid", value: "invalid", seconds: "invalid", milliseconds: "invalid"},
	}
	for _, test := range tests {
		testContext.Run(test.name, func(testContext *testing.T) {
			for _, unit := range []struct {
				name    string
				convert func(any) string
				want    string
			}{
				{name: "seconds", convert: secondsToDurationString, want: test.seconds},
				{name: "milliseconds", convert: millisecondsToDurationString, want: test.milliseconds},
			} {
				got := unit.convert(test.value)
				if got != unit.want {
					testContext.Fatalf("%s=%q, want %q", unit.name, got, unit.want)
				}
				if got == "" {
					continue
				}
				generated, err := normalizeGeneratorDuration(got, "connect_timeout")
				if strings.HasPrefix(test.name, "invalid") {
					if err == nil {
						testContext.Fatal("invalid imported duration was silently accepted by generation")
					}
					continue
				}
				if err != nil {
					testContext.Fatal(err)
				}
				wantNormalized, err := normalizeGeneratorDuration(unit.want, "connect_timeout")
				if err != nil {
					testContext.Fatal(err)
				}
				wantDuration, _ := time.ParseDuration(wantNormalized)
				gotDuration, _ := time.ParseDuration(generated)
				if gotDuration != wantDuration {
					testContext.Fatalf("generation changed duration: got %s want %s", gotDuration, wantDuration)
				}
			}
		})
	}
}

func TestVMessURLUsesSharedNativeExtensionParsers(testContext *testing.T) {
	base := "vmess://00000000-0000-0000-0000-000000000304@proxy.example:443?"
	query := url.Values{
		"transport_options": {`{"type":"gun","service_name":"service"}`},
		"multiplex":         {`{"enabled":true,"protocol":"h2mux","max_connections":2}`},
	}
	parsed, _, _, err := ParseShareLink(base + query.Encode())
	if err != nil {
		testContext.Fatal(err)
	}
	config := parsed.(models.VMESSConfig)
	if config.Network != "grpc" || config.TransportOptions["type"] != "grpc" || config.TransportOptions["service_name"] != "service" || config.MultiplexConfig["protocol"] != "h2mux" {
		testContext.Fatalf("native extensions were lost: %+v", config)
	}
	for _, test := range []struct {
		name  string
		query url.Values
		want  string
	}{
		{name: "transport JSON", query: url.Values{"transport_options": {"{"}}, want: "invalid transport_options JSON"},
		{name: "transport object", query: url.Values{"transport_options": {"null"}}, want: "expected object"},
		{name: "transport type", query: url.Values{"transport_options": {`{"type":"kcp"}`}}, want: "invalid transport_options type"},
		{name: "transport conflict", query: url.Values{"type": {"ws"}, "transport_options": {`{"type":"grpc"}`}}, want: "transport type conflicts with transport_options"},
		{name: "multiplex JSON", query: url.Values{"multiplex": {"[1]"}}, want: "invalid multiplex options JSON"},
		{name: "multiplex object", query: url.Values{"multiplex": {"null"}}, want: "expected object"},
	} {
		testContext.Run(test.name, func(testContext *testing.T) {
			_, _, _, err := ParseShareLink(base + test.query.Encode())
			if err == nil || !strings.Contains(err.Error(), "vmess") || !strings.Contains(err.Error(), test.want) {
				testContext.Fatalf("error=%v, want VMess context and %q", err, test.want)
			}
		})
	}
}

func TestLegacyVMessJSONCanonicalConfigRemainsByteEquivalent(testContext *testing.T) {
	const prefix = `{"server":"192.0.2.10","server_port":443,"uuid":"00000000-0000-0000-0000-000000000303","alter_id":0,"security":"auto","network":"tcp"`
	for _, test := range []struct {
		name   string
		fields map[string]any
		suffix string
	}{
		{name: "TLS 缺省"},
		{name: "TLS 空字符串", fields: map[string]any{"tls": ""}},
		{name: "TLS none", fields: map[string]any{"tls": "none"}},
		{name: "空扩展字段", fields: map[string]any{"tls": "", "sni": "", "alpn": "", "fp": "", "ech": "", "sid": "", "allowInsecure": false}},
		{name: "无公钥的旧 sid 被忽略", fields: map[string]any{"tls": "none", "sid": "0102"}},
		{name: "TLS insecure false", fields: map[string]any{"tls": "tls", "allowInsecure": false}, suffix: `,"tls":"tls","tls_options":{"enabled":true}`},
		{name: "TLS insecure 字符串零", fields: map[string]any{"tls": "tls", "allowInsecure": "0"}, suffix: `,"tls":"tls","tls_options":{"enabled":true}`},
		{name: "TLS insecure 数字零", fields: map[string]any{"tls": "tls", "allowInsecure": 0}, suffix: `,"tls":"tls","tls_options":{"enabled":true}`},
		{name: "TLS insecure true", fields: map[string]any{"tls": "tls", "allowInsecure": true}, suffix: `,"tls":"tls","insecure":true,"tls_options":{"enabled":true,"insecure":true}`},
		{name: "TLS 空扩展字段", fields: map[string]any{"tls": "tls", "sni": "", "alpn": "", "fp": "", "ech": ""}, suffix: `,"tls":"tls","tls_options":{"enabled":true}`},
		{name: "未启用 TLS 的 SNI", fields: map[string]any{"sni": "peer.example"}, suffix: `,"sni":"peer.example","tls_options":{"enabled":false,"server_name":"peer.example"}`},
		{name: "保留原 SNI 空白", fields: map[string]any{"tls": "tls", "sni": " peer.example "}, suffix: `,"tls":"tls","sni":" peer.example ","tls_options":{"enabled":true,"server_name":" peer.example "}`},
		{name: "保留空白扩展的 enabled 零值", fields: map[string]any{"sni": " ", "ech": " "}, suffix: `,"sni":" ","tls_options":{"enabled":false}`},
		{name: "ALPN 与 fingerprint", fields: map[string]any{"tls": "tls", "alpn": " h2,http/1.1 ", "fp": "Chrome"}, suffix: `,"tls":"tls","alpn":"h2,http/1.1","fingerprint":"chrome","tls_options":{"alpn":["h2","http/1.1"],"enabled":true,"utls":{"enabled":true,"fingerprint":"chrome"}}`},
		{name: "缺省模式推断 Reality 保留旧序列化", fields: map[string]any{"pbk": protocolAuditRealityPublicKey}, suffix: fmt.Sprintf(`,"tls_options":{"enabled":false,"reality":{"enabled":true,"public_key":%q,"short_id":""},"utls":{"enabled":true,"fingerprint":"chrome"}}`, protocolAuditRealityPublicKey)},
		{name: "Reality 默认 uTLS 与空 short ID", fields: map[string]any{"tls": "reality", "pbk": protocolAuditRealityPublicKey}, suffix: fmt.Sprintf(`,"tls":"reality","tls_options":{"enabled":true,"reality":{"enabled":true,"public_key":%q,"short_id":""},"utls":{"enabled":true,"fingerprint":"chrome"}}`, protocolAuditRealityPublicKey)},
		{name: "Reality 空 fingerprint", fields: map[string]any{"tls": "reality", "pbk": protocolAuditRealityPublicKey, "fp": "", "sid": ""}, suffix: fmt.Sprintf(`,"tls":"reality","tls_options":{"enabled":true,"reality":{"enabled":true,"public_key":%q,"short_id":""},"utls":{"enabled":true,"fingerprint":"chrome"}}`, protocolAuditRealityPublicKey)},
		{name: "Reality none fingerprint", fields: map[string]any{"tls": "reality", "pbk": protocolAuditRealityPublicKey, "fp": "none"}, suffix: fmt.Sprintf(`,"tls":"reality","tls_options":{"enabled":true,"reality":{"enabled":true,"public_key":%q,"short_id":""},"utls":{"enabled":true,"fingerprint":"chrome"}}`, protocolAuditRealityPublicKey)},
		{name: "Reality 显式 uTLS 与 short ID", fields: map[string]any{"tls": "reality", "pbk": protocolAuditRealityPublicKey, "fp": "Firefox", "sid": "0102"}, suffix: fmt.Sprintf(`,"tls":"reality","fingerprint":"firefox","tls_options":{"enabled":true,"reality":{"enabled":true,"public_key":%q,"short_id":"0102"},"utls":{"enabled":true,"fingerprint":"firefox"}}`, protocolAuditRealityPublicKey)},
	} {
		testContext.Run(test.name, func(testContext *testing.T) {
			fields := map[string]any{"add": "192.0.2.10", "port": 443, "id": "00000000-0000-0000-0000-000000000303", "net": "tcp", "scy": "auto", "ps": "原名"}
			for key, value := range test.fields {
				fields[key] = value
			}
			encoded, err := json.Marshal(fields)
			if err != nil {
				testContext.Fatal(err)
			}
			config, proxyType, _, err := ParseShareLink("vmess://" + base64.StdEncoding.EncodeToString(encoded))
			if err != nil {
				testContext.Fatal(err)
			}
			want := prefix + test.suffix + "}"
			raw, err := json.Marshal(config)
			if err != nil || string(raw) != want {
				testContext.Fatalf("导入 JSON 与清理前快照不同：\n实际 %s\n预期 %s\n错误 %v", raw, want, err)
			}
			assertLegacyCanonicalImportConfig(testContext, proxyType, config, want)
		})
	}
}

func TestLegacyClashCanonicalConfigRemainsByteEquivalent(testContext *testing.T) {
	const server = `"server":"192.0.2.10","server_port":443`
	const uuid = `"uuid":"00000000-0000-0000-0000-000000000303"`
	const headers = `{"Host":"cdn.example","X-Test":"trace"}`
	for _, protocol := range []struct {
		name string
		body string
		tls  string
	}{
		{name: "vless", body: server + "," + uuid},
		{name: "vmess", body: server + "," + uuid + `,"alter_id":0,"security":"auto"`},
		{name: "trojan", body: server + `,"password":"test","security":"tls"`, tls: `,"tls_options":{"enabled":true}`},
	} {
		for _, test := range []struct {
			name    string
			network string
			fields  map[string]any
			want    string
		}{
			{name: "普通 WS", network: "ws", fields: map[string]any{"ws-opts": map[string]any{"headers": map[string]any{"Host": "cdn.example", "X-Test": "trace"}}}},
			{name: "HTTP 原生字符串请求头", network: "http", fields: map[string]any{"http-opts": map[string]any{"headers": map[string]any{"Host": "cdn.example", "X-Test": "trace"}}}, want: "{" + protocol.body + `,"network":"http"` + protocol.tls + `,"transport_options":{"headers":` + headers + `,"type":"http"}}`},
			{name: "整数秒", network: "tcp", fields: map[string]any{"connect-timeout": 500, "fallback-delay": 500}, want: `{"connect_timeout":"500s","fallback_delay":"500s",` + protocol.body + `,"network":"tcp"` + protocol.tls + "}"},
			{name: "整数秒字符串", network: "tcp", fields: map[string]any{"connect-timeout": "500", "fallback-delay": "500"}, want: `{"connect_timeout":"500s","fallback_delay":"500s",` + protocol.body + `,"network":"tcp"` + protocol.tls + "}"},
			{name: "显式单位", network: "tcp", fields: map[string]any{"connect-timeout": "1m", "fallback-delay": "5000ms"}, want: `{"connect_timeout":"1m","fallback_delay":"5000ms",` + protocol.body + `,"network":"tcp"` + protocol.tls + "}"},
			{name: "字符串零", network: "tcp", fields: map[string]any{"connect-timeout": "0", "fallback-delay": "0"}, want: `{"connect_timeout":"0","fallback_delay":"0",` + protocol.body + `,"network":"tcp"` + protocol.tls + "}"},
		} {
			testContext.Run(protocol.name+"/"+test.name, func(testContext *testing.T) {
				fields := map[string]any{"name": "原名", "type": protocol.name, "server": "192.0.2.10", "port": 443, "uuid": "00000000-0000-0000-0000-000000000303", "password": "test", "network": test.network}
				for key, value := range test.fields {
					fields[key] = value
				}
				want := test.want
				if test.name == "普通 WS" {
					compatibility := `,"headers":` + headers + `,"host":"cdn.example"`
					if protocol.name == "trojan" {
						compatibility = `,"host":"cdn.example","headers":` + headers
					}
					want = "{" + protocol.body + `,"network":"ws"` + compatibility + protocol.tls + `,"transport_options":{"headers":` + headers + `,"type":"ws"}}`
				}
				encoded, err := json.Marshal(map[string]any{"proxies": []any{fields}})
				if err != nil {
					testContext.Fatal(err)
				}
				items, failures, err := ExpandBatchImportSourcesWithType(testContext.Context(), []string{string(encoded)}, BatchImportSourceAuto)
				if err != nil || len(failures) != 0 || len(items) != 1 {
					testContext.Fatalf("Clash 导入失败：items=%d failures=%+v err=%v", len(items), failures, err)
				}
				assertLegacyCanonicalImportConfig(testContext, protocol.name, items[0].Config, want)
			})
		}
		for _, alias := range []string{"grpc-service-name", "serviceName", "service-name", "service_name"} {
			testContext.Run(protocol.name+"/gRPC/"+alias, func(testContext *testing.T) {
				content := fmt.Sprintf("proxies:\n  - {name: grpc, type: %s, server: 192.0.2.10, port: 443, uuid: 00000000-0000-0000-0000-000000000303, password: test, network: grpc, grpc-opts: {%s: service, idle-timeout: 500, ping-timeout: 1m}}\n", protocol.name, alias)
				items, failures, err := ExpandBatchImportSourcesWithType(testContext.Context(), []string{content}, BatchImportSourceAuto)
				if err != nil || len(failures) != 0 || len(items) != 1 {
					testContext.Fatalf("gRPC 导入失败：items=%d failures=%+v err=%v", len(items), failures, err)
				}
				compatibility := `,"service_name":"service"`
				if alias == "service-name" {
					compatibility = ""
				}
				want := "{" + protocol.body + `,"network":"grpc"` + compatibility + protocol.tls + `,"transport_options":{"idle_timeout":"500s","ping_timeout":"1m","service_name":"service","type":"grpc"}}`
				assertLegacyCanonicalImportConfig(testContext, protocol.name, items[0].Config, want)
			})
		}
	}
}

func assertLegacyCanonicalImportConfig(testContext *testing.T, protocol string, config any, want string) {
	testContext.Helper()
	node := nativeTestNode(testContext, 1, "原名", protocol, config)
	parsed, err := node.ParseConfig()
	if err != nil {
		testContext.Fatal(err)
	}
	canonical, err := json.Marshal(parsed)
	if err != nil || string(canonical) != want {
		testContext.Fatalf("持久化规范 JSON 与清理前快照不同：\n实际 %s\n预期 %s\n错误 %v", canonical, want, err)
	}
}

func TestVMessJSONTLSFieldPresenceMatchesJSONCaseFolding(testContext *testing.T) {
	for _, modeKey := range []string{"tls", "TLS", "tLs", "tLſ"} {
		for _, mode := range []string{"none", "tls"} {
			testContext.Run(modeKey+"/"+mode, func(testContext *testing.T) {
				fields := map[string]any{"add": "192.0.2.10", "port": 443, "id": "00000000-0000-0000-0000-000000000303", "net": "tcp", modeKey: mode, "PBK": protocolAuditRealityPublicKey}
				encoded, err := json.Marshal(fields)
				if err != nil {
					testContext.Fatal(err)
				}
				_, _, _, err = ParseShareLink("vmess://" + base64.StdEncoding.EncodeToString(encoded))
				if err == nil || !strings.Contains(err.Error(), "Reality parameters require security=reality") {
					testContext.Fatalf("JSON 已识别的显式 TLS 模式不能被大小写绕过：%v", err)
				}
			})
		}
		testContext.Run(modeKey+"/合法 ECH", func(testContext *testing.T) {
			fields := map[string]any{"add": "192.0.2.10", "port": 443, "id": "00000000-0000-0000-000000000303", "net": "tcp", modeKey: "tls", "ECH": protocolAuditECHConfigListBase64, "FP": "Chrome", "ALPN": "h2,http/1.1", "SNI": "peer.example", "AllowInsecure": false}
			encoded, err := json.Marshal(fields)
			if err != nil {
				testContext.Fatal(err)
			}
			config, _, _, err := ParseShareLink("vmess://" + base64.StdEncoding.EncodeToString(encoded))
			if err != nil {
				testContext.Fatal(err)
			}
			parsed := config.(models.VMESSConfig)
			if parsed.TLS != "tls" || nativeMap(parsed.TLSOptions["ech"])["enabled"] != true || nativeMap(parsed.TLSOptions["utls"])["fingerprint"] != "chrome" {
				testContext.Fatalf("大小写字段丢失了已解析的 TLS/ECH/uTLS：%+v", parsed)
			}
			if _, present := parsed.TLSOptions["insecure"]; present {
				testContext.Fatal("大小写 allowInsecure=false 不能注入冗余零值")
			}
		})
	}
}

func TestLegacyVMessTextListsPreserveCanonicalConfigAndGeneratedArrays(testContext *testing.T) {
	const prefix = `{"server":"192.0.2.10","server_port":443,"uuid":"00000000-0000-0000-0000-000000000303","alter_id":0,"security":"auto"`
	for _, test := range []struct {
		name      string
		network   string
		fields    map[string]any
		want      string
		wantALPN  []string
		wantHosts []string
	}{
		{name: "ALPN 分号保留旧文本", network: "tcp", fields: map[string]any{"alpn": " h2;http/1.1 "}, want: prefix + `,"network":"tcp","tls":"tls","alpn":"h2;http/1.1","tls_options":{"alpn":["h2","http/1.1"],"enabled":true}}`, wantALPN: []string{"h2", "http/1.1"}},
		{name: "ALPN 换行保留旧逗号规范", network: "tcp", fields: map[string]any{"alpn": " h2\nhttp/1.1 "}, want: prefix + `,"network":"tcp","tls":"tls","alpn":"h2,http/1.1","tls_options":{"alpn":["h2","http/1.1"],"enabled":true}}`, wantALPN: []string{"h2", "http/1.1"}},
		{name: "HTTP host 分号与换行", network: "http", fields: map[string]any{"host": " a.example;\nb.example ", "path": "/path"}, want: prefix + `,"network":"http","tls":"tls","path":"/path","host":"a.example;,b.example","http_path":["/path"],"tls_options":{"enabled":true},"transport_options":{"host":["a.example","b.example"],"path":"/path","type":"http"}}`, wantHosts: []string{"a.example", "b.example"}},
	} {
		testContext.Run(test.name, func(testContext *testing.T) {
			fields := map[string]any{"add": "192.0.2.10", "port": 443, "id": "00000000-0000-0000-0000-000000000303", "net": test.network, "scy": "auto", "tls": "tls"}
			for key, value := range test.fields {
				fields[key] = value
			}
			encoded, err := json.Marshal(fields)
			if err != nil {
				testContext.Fatal(err)
			}
			config, _, _, err := ParseShareLink("vmess://" + base64.StdEncoding.EncodeToString(encoded))
			if err != nil {
				testContext.Fatal(err)
			}
			assertLegacyCanonicalImportConfig(testContext, "vmess", config, test.want)
			node := nativeTestNode(testContext, 1, "列表", "vmess", config)
			parsed, err := node.ParseConfig()
			if err != nil {
				testContext.Fatal(err)
			}
			outbound, err := (&SingBoxService{}).generateVMESSOutbound(parsed.(*models.VMESSConfig), "test")
			if err != nil {
				testContext.Fatal(err)
			}
			if test.wantALPN != nil && !reflect.DeepEqual(nativeStringSlice(nativeMap(outbound.Extra["tls"])["alpn"]), test.wantALPN) {
				testContext.Fatalf("最终 ALPN 数组错误：%+v", outbound.Extra)
			}
			if test.wantHosts != nil && !reflect.DeepEqual(nativeStringSlice(nativeMap(outbound.Extra["transport"])["host"]), test.wantHosts) {
				testContext.Fatalf("最终 HTTP host 数组错误：%+v", outbound.Extra)
			}
		})
	}
}
