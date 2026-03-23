package services

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"sb-proxy/backend/models"
)

func TestParseTrojanLink(t *testing.T) {
	link := "trojan://pass123@example.com:443?type=ws&host=example.com&path=%2Fws&sni=sni.example.com&alpn=h2,http/1.1&insecure=1&fp=firefox#TestName"

	cfg, typ, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if typ != "trojan" {
		t.Fatalf("expected trojan type, got %s", typ)
	}
	if name != "TestName" {
		t.Fatalf("unexpected name: %s", name)
	}

	trojanCfg, ok := cfg.(models.TrojanConfig)
	if !ok {
		t.Fatalf("unexpected config type %T", cfg)
	}
	if trojanCfg.Server != "example.com" || trojanCfg.ServerPort != 443 || trojanCfg.Password != "pass123" {
		t.Fatalf("server/port/password mismatch: %+v", trojanCfg)
	}
	if trojanCfg.Network != "ws" || trojanCfg.Path != "/ws" || trojanCfg.Host != "example.com" {
		t.Fatalf("transport fields mismatch: %+v", trojanCfg)
	}
	if trojanCfg.SNI != "sni.example.com" || !trojanCfg.Insecure {
		t.Fatalf("tls fields mismatch: %+v", trojanCfg)
	}
	if len(trojanCfg.ALPN) != 2 || trojanCfg.ALPN[0] != "h2" {
		t.Fatalf("alpn parse mismatch: %+v", trojanCfg.ALPN)
	}
	if trojanCfg.Fingerprint != "firefox" {
		t.Fatalf("fingerprint parse mismatch: %+v", trojanCfg)
	}
}

func TestGenerateTrojanOutbound(t *testing.T) {
	rawCfg := models.TrojanConfig{
		Server:      "node.example.com",
		ServerPort:  443,
		Password:    "pwd",
		Network:     "ws",
		Path:        "/ws",
		Host:        "h.example.com",
		SNI:         "sni.example.com",
		ALPN:        []string{"h2"},
		Fingerprint: "firefox",
		Insecure:    true,
		Headers: map[string]string{
			"X-Test": "1",
		},
	}
	cfgBytes, _ := json.Marshal(rawCfg)

	node := models.ProxyNode{
		ID:          1,
		Name:        "trojan",
		Type:        "trojan",
		Config:      string(cfgBytes),
		InboundPort: 30010,
	}

	svc := &SingBoxService{}
	out, err := svc.generateOutbound(&node, "trojan-out")
	if err != nil {
		t.Fatalf("generateOutbound error: %v", err)
	}
	if out.Type != "trojan" || out.Tag != "trojan-out" {
		t.Fatalf("unexpected outbound meta: %+v", out)
	}
	tls, ok := out.Extra["tls"].(map[string]interface{})
	if !ok || tls["server_name"] != "sni.example.com" || tls["insecure"] != true {
		t.Fatalf("tls not set correctly: %+v", tls)
	}
	if utls, ok := tls["utls"].(map[string]interface{}); !ok || utls["fingerprint"] != "firefox" {
		t.Fatalf("utls not set correctly: %+v", utls)
	}
	transport, ok := out.Extra["transport"].(map[string]interface{})
	if !ok || transport["type"] != "ws" || transport["path"] != "/ws" {
		t.Fatalf("transport missing: %+v", transport)
	}
	headers, ok := transport["headers"].(map[string]string)
	if !ok {
		if h2, ok2 := transport["headers"].(map[string]interface{}); ok2 {
			if h2["Host"] != "h.example.com" {
				t.Fatalf("headers missing host: %+v", h2)
			}
		} else {
			t.Fatalf("headers missing: %+v", transport["headers"])
		}
	} else if headers["Host"] != "h.example.com" {
		t.Fatalf("headers host mismatch: %+v", headers)
	}
}

func TestParseSSLink_Base64FullForm(t *testing.T) {
	decoded := "aes-128-gcm:pwd@example.com:443"
	encoded := base64.RawURLEncoding.EncodeToString([]byte(decoded))
	link := "ss://" + encoded + "#SS-Full"

	cfg, typ, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if typ != "ss" {
		t.Fatalf("expected ss type, got %s", typ)
	}
	if name != "SS-Full" {
		t.Fatalf("unexpected name: %s", name)
	}

	ssCfg, ok := cfg.(models.SSConfig)
	if !ok {
		t.Fatalf("unexpected config type %T", cfg)
	}
	if ssCfg.Server != "example.com" || ssCfg.ServerPort != 443 {
		t.Fatalf("server/port mismatch: %+v", ssCfg)
	}
	if ssCfg.Method != "aes-128-gcm" || ssCfg.Password != "pwd" {
		t.Fatalf("method/password mismatch: %+v", ssCfg)
	}
}

func TestGenerateVLESSOutboundSplitsALPN(t *testing.T) {
	rawCfg := models.VLESSConfig{
		Server:     "node.example.com",
		ServerPort: 443,
		UUID:       "00000000-0000-0000-0000-000000000000",
		Security:   "tls",
		SNI:        "sni.example.com",
		ALPN:       "h2,http/1.1",
	}
	cfgBytes, _ := json.Marshal(rawCfg)
	node := models.ProxyNode{
		ID:          1,
		Name:        "vless",
		Type:        "vless",
		Config:      string(cfgBytes),
		InboundPort: 30010,
	}

	svc := &SingBoxService{}
	out, err := svc.generateOutbound(&node, "vless-out")
	if err != nil {
		t.Fatalf("generateOutbound error: %v", err)
	}
	tls, ok := out.Extra["tls"].(map[string]interface{})
	if !ok {
		t.Fatalf("tls missing: %+v", out.Extra["tls"])
	}
	alpn, ok := tls["alpn"].([]string)
	if !ok {
		t.Fatalf("unexpected alpn type: %T", tls["alpn"])
	}
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatalf("unexpected alpn value: %+v", alpn)
	}
}

func TestGenerateVMessOutboundSplitsALPN(t *testing.T) {
	rawCfg := models.VMESSConfig{
		Server:     "node.example.com",
		ServerPort: 443,
		UUID:       "00000000-0000-0000-0000-000000000000",
		TLS:        "tls",
		SNI:        "sni.example.com",
		ALPN:       "h2,http/1.1",
	}
	cfgBytes, _ := json.Marshal(rawCfg)
	node := models.ProxyNode{
		ID:          1,
		Name:        "vmess",
		Type:        "vmess",
		Config:      string(cfgBytes),
		InboundPort: 30010,
	}

	svc := &SingBoxService{}
	out, err := svc.generateOutbound(&node, "vmess-out")
	if err != nil {
		t.Fatalf("generateOutbound error: %v", err)
	}
	tls, ok := out.Extra["tls"].(map[string]interface{})
	if !ok {
		t.Fatalf("tls missing: %+v", out.Extra["tls"])
	}
	alpn, ok := tls["alpn"].([]string)
	if !ok {
		t.Fatalf("unexpected alpn type: %T", tls["alpn"])
	}
	if len(alpn) != 2 || alpn[0] != "h2" || alpn[1] != "http/1.1" {
		t.Fatalf("unexpected alpn value: %+v", alpn)
	}
}

func TestParseWireGuardLink(t *testing.T) {
	link := "wireguard://private-key@engage.cloudflareclient.com:2408?publickey=peer-public-key&ip=172.16.0.2/32&ipv6=2606:4700:110:8765::2/128&allowedips=0.0.0.0/0,::/0&reserved=162,104,222&mtu=1280&workers=2&detour=warp-selector&domain_resolver=local&domain_resolver_strategy=prefer_ipv4&udp_fragment=1#WARP"

	cfg, typ, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if typ != "wireguard" {
		t.Fatalf("expected wireguard type, got %s", typ)
	}
	if name != "WARP" {
		t.Fatalf("unexpected name: %s", name)
	}

	wgCfg, ok := cfg.(models.WireGuardConfig)
	if !ok {
		t.Fatalf("unexpected config type %T", cfg)
	}
	if wgCfg.Server != "engage.cloudflareclient.com" || wgCfg.ServerPort != 2408 {
		t.Fatalf("unexpected endpoint: %+v", wgCfg)
	}
	if len(wgCfg.LocalAddress) != 2 || wgCfg.LocalAddress[0] != "172.16.0.2/32" {
		t.Fatalf("unexpected local addresses: %+v", wgCfg.LocalAddress)
	}
	if len(wgCfg.AllowedIPs) != 2 || wgCfg.AllowedIPs[1] != "::/0" {
		t.Fatalf("unexpected allowed ips: %+v", wgCfg.AllowedIPs)
	}
	if len(wgCfg.Reserved) != 3 || wgCfg.Reserved[0] != 162 || wgCfg.Reserved[2] != 222 {
		t.Fatalf("unexpected reserved bytes: %+v", wgCfg.Reserved)
	}
	if wgCfg.DomainResolver != "local" || wgCfg.DomainResolverStrategy != "prefer_ipv4" {
		t.Fatalf("unexpected domain resolver fields: %+v", wgCfg)
	}
	if wgCfg.UDPFragment == nil || !*wgCfg.UDPFragment {
		t.Fatalf("expected udp_fragment=true")
	}
}

func TestGenerateWireGuardOutboundUsesPeersWhenAllowedIPsPresent(t *testing.T) {
	udpFragment := true
	rawCfg := models.WireGuardConfig{
		Server:                 "engage.cloudflareclient.com",
		ServerPort:             2408,
		LocalAddress:           []string{"172.16.0.2/32", "2606:4700:110:8765::2/128"},
		PrivateKey:             "private-key",
		PeerPublicKey:          "peer-public-key",
		AllowedIPs:             []string{"0.0.0.0/0", "::/0"},
		Reserved:               []uint8{162, 104, 222},
		MTU:                    1280,
		Workers:                2,
		Detour:                 "warp-selector",
		DomainResolver:         "local",
		DomainResolverStrategy: "prefer_ipv4",
		UDPFragment:            &udpFragment,
	}
	cfgBytes, _ := json.Marshal(rawCfg)

	node := models.ProxyNode{
		ID:          1,
		Name:        "warp",
		Type:        "wireguard",
		Config:      string(cfgBytes),
		InboundPort: 30010,
	}

	svc := &SingBoxService{}
	out, err := svc.generateOutbound(&node, "wireguard-out")
	if err != nil {
		t.Fatalf("generateOutbound error: %v", err)
	}
	if out.Type != "wireguard" || out.Tag != "wireguard-out" {
		t.Fatalf("unexpected outbound meta: %+v", out)
	}

	peers, ok := out.Extra["peers"].([]map[string]interface{})
	if !ok || len(peers) != 1 {
		t.Fatalf("expected one synthesized peer, got %#v", out.Extra["peers"])
	}
	if peers[0]["public_key"] != "peer-public-key" {
		t.Fatalf("unexpected peer config: %#v", peers[0])
	}

	domainResolver, ok := out.Extra["domain_resolver"].(map[string]interface{})
	if !ok || domainResolver["server"] != "local" || domainResolver["strategy"] != "prefer_ipv4" {
		t.Fatalf("unexpected domain_resolver: %#v", out.Extra["domain_resolver"])
	}
	if out.Extra["udp_fragment"] != true {
		t.Fatalf("expected udp_fragment=true, got %#v", out.Extra["udp_fragment"])
	}
}

func TestBuildWireGuardShareLinkRoundTrip(t *testing.T) {
	udpFragment := true
	config := models.WireGuardConfig{
		Server:                 "engage.cloudflareclient.com",
		ServerPort:             2408,
		LocalAddress:           []string{"172.16.0.2/32", "2606:4700:110:8765::2/128"},
		PrivateKey:             "private-key",
		PeerPublicKey:          "peer-public-key",
		AllowedIPs:             []string{"0.0.0.0/0", "::/0"},
		Reserved:               []uint8{162, 104, 222},
		MTU:                    1280,
		Workers:                2,
		Detour:                 "warp-selector",
		DomainResolver:         "local",
		DomainResolverStrategy: "prefer_ipv4",
		RoutingMark:            "0x10",
		UDPFragment:            &udpFragment,
	}
	cfgBytes, _ := json.Marshal(config)

	node := models.ProxyNode{
		Name:   "WARP",
		Type:   "wireguard",
		Config: string(cfgBytes),
	}

	link, err := BuildShareLink(node)
	if err != nil {
		t.Fatalf("BuildShareLink failed: %v", err)
	}

	parsedCfg, typ, name, err := ParseShareLink(link)
	if err != nil {
		t.Fatalf("ParseShareLink failed: %v", err)
	}
	if typ != "wireguard" || name != "WARP" {
		t.Fatalf("unexpected round-trip metadata: type=%s name=%s", typ, name)
	}
	wgCfg := parsedCfg.(models.WireGuardConfig)
	if wgCfg.Detour != "warp-selector" || wgCfg.RoutingMark != "0x10" {
		t.Fatalf("unexpected round-trip cfg: %+v", wgCfg)
	}
	if len(wgCfg.Reserved) != 3 || wgCfg.Reserved[1] != 104 {
		t.Fatalf("unexpected round-trip reserved bytes: %+v", wgCfg.Reserved)
	}
}
