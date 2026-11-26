package models

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestSplitCompatibilityList(testingT *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		want  []string
	}{
		{name: "空列表", input: " , ; \n\r ", want: []string{}},
		{name: "逗号", input: " h2 , http/1.1 ", want: []string{"h2", "http/1.1"}},
		{name: "混合分隔符", input: " h2;http/1.1\nh3\r custom ,,; ", want: []string{"h2", "http/1.1", "h3", "custom"}},
		{name: "保留重复值", input: "h2;h2", want: []string{"h2", "h2"}},
	} {
		testingT.Run(test.name, func(testingT *testing.T) {
			if got := SplitCompatibilityList(test.input); !slices.Equal(got, test.want) {
				testingT.Fatalf("列表解析结果 %v，期望 %v", got, test.want)
			}
		})
	}
}

func TestWireGuardDialerOptionsRoundTrip(testingT *testing.T) {
	fragment := false
	original := WireGuardConfig{
		DialerOptions: DialerOptions{
			Detour: "relay", BindInterface: "eth0", Inet4BindAddress: "192.0.2.1", Inet6BindAddress: "2001:db8::1",
			ProtectPath: "/tmp/protect", RoutingMark: float64(123), ReuseAddr: true, NetNS: "test-namespace",
			ConnectTimeout: "5s", TCPFastOpen: true, TCPMultiPath: true, UDPFragment: &fragment,
			DomainResolver: "local", NetworkStrategy: "fallback", NetworkType: ListableString{"wifi"},
			FallbackNetworkType: ListableString{"cellular"}, FallbackDelay: "2s", DomainStrategy: "prefer_ipv4",
		},
		LocalAddress: []string{"10.0.0.2/32"}, PrivateKey: "private-key",
		DomainResolverStrategy: "prefer_ipv4", DomainResolverOptions: NativeOptions{"strategy": "prefer_ipv4"},
		Peers: []WireGuardPeerConfig{{Server: "192.0.2.10", ServerPort: 2408, PublicKey: "peer-key", Reserved: ByteList{1, 2, 3}}},
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		testingT.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		testingT.Fatal(err)
	}
	for _, field := range []string{
		"detour", "bind_interface", "inet4_bind_address", "inet6_bind_address", "protect_path", "routing_mark",
		"reuse_addr", "netns", "connect_timeout", "tcp_fast_open", "tcp_multi_path", "udp_fragment",
		"domain_resolver", "network_strategy", "network_type", "fallback_network_type", "fallback_delay", "domain_strategy",
	} {
		if _, exists := fields[field]; !exists {
			testingT.Fatalf("嵌入拨号字段 %s 没有保持平铺 JSON 契约", field)
		}
	}
	if _, nested := fields["DialerOptions"]; nested {
		testingT.Fatal("拨号字段不应新增 JSON 嵌套层")
	}
	parsed, err := (ProxyDefinition{Type: "wireguard", Config: string(encoded)}).ParseConfig()
	if err != nil {
		testingT.Fatal(err)
	}
	if got := parsed.(*WireGuardConfig); !reflect.DeepEqual(*got, original) {
		testingT.Fatalf("WireGuard 拨号及专用字段往返后发生变化：%+v", got)
	}
}

func TestReconcileAdminPasswordReusesHash(testingT *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		testingT.Fatal(err)
	}
	defer db.Close()
	if err := InitDB(db); err != nil {
		testingT.Fatal(err)
	}
	for _, environmentPassword := range []string{"initial-environment-value", "changed-environment-value"} {
		if err := ReconcileAdminPassword(db, environmentPassword); err != nil {
			testingT.Fatal(err)
		}
		var hash, fingerprint string
		if err := db.QueryRow("SELECT admin_password,admin_password_env_fingerprint FROM settings WHERE singleton_key=1").Scan(&hash, &fingerprint); err != nil {
			testingT.Fatal(err)
		}
		if hash != fingerprint {
			testingT.Fatal("初始化和环境重置应复用已经生成的同一 bcrypt 结果")
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(environmentPassword)); err != nil {
			testingT.Fatal(err)
		}
		if cost, err := bcrypt.Cost([]byte(hash)); err != nil || cost != bcrypt.DefaultCost {
			testingT.Fatalf("bcrypt 成本发生变化：%d，%v", cost, err)
		}
	}
}
