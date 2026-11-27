package services

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
	"gopkg.in/yaml.v3"

	"sb-proxy/backend/models"
)

func TestSSHConfigStrictParsing(testContext *testing.T) {
	for _, testCase := range []struct {
		name      string
		config    string
		wantError string
	}{
		{name: "defaults", config: `{"server":"192.0.2.1"}`},
		{name: "string lists", config: `{"server":"192.0.2.1","private_key":"key","host_key":"host","host_key_algorithms":"ssh-ed25519"}`},
		{name: "array lists", config: `{"server":"192.0.2.1","private_key":["line1","line2"],"host_key":["host1","host2"],"host_key_algorithms":["ssh-ed25519","rsa-sha2-512"]}`},
		{name: "cipher", config: `{"server":"192.0.2.1","cipher":"aes256-ctr"}`, wantError: "unknown field"},
		{name: "mac", config: `{"server":"192.0.2.1","mac":"hmac-sha2-256"}`, wantError: "unknown field"},
		{name: "kex", config: `{"server":"192.0.2.1","kex_algorithm":"curve25519-sha256"}`, wantError: "unknown field"},
		{name: "username is Clash only", config: `{"server":"192.0.2.1","username":"root"}`, wantError: "unknown field"},
		{name: "unknown dialer option", config: `{"server":"192.0.2.1","dialer_unknown":true}`, wantError: "unknown field"},
		{name: "numeric private key", config: `{"server":"192.0.2.1","private_key":42}`, wantError: "cannot unmarshal"},
		{name: "numeric host key", config: `{"server":"192.0.2.1","host_key":[42]}`, wantError: "cannot unmarshal"},
		{name: "object algorithms", config: `{"server":"192.0.2.1","host_key_algorithms":{}}`, wantError: "cannot unmarshal"},
		{name: "string port", config: `{"server":"192.0.2.1","server_port":"22"}`, wantError: "cannot unmarshal"},
		{name: "fractional port", config: `{"server":"192.0.2.1","server_port":22.5}`, wantError: "cannot unmarshal"},
		{name: "trailing JSON", config: `{"server":"192.0.2.1"} {}`, wantError: "trailing JSON value"},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			node := models.ProxyNode{Type: " SSH ", Config: testCase.config}
			parsed, err := node.ParseConfig()
			if testCase.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
					testContext.Fatalf("expected %q, got %v", testCase.wantError, err)
				}
				return
			}
			if err != nil {
				testContext.Fatal(err)
			}
			config, ok := parsed.(*models.SSHConfig)
			if !ok || config.Server != "192.0.2.1" {
				testContext.Fatalf("unexpected parsed SSH configuration: %T", parsed)
			}
			if err := config.Validate(); err != nil {
				testContext.Fatal(err)
			}
			if testCase.name == "string lists" && (len(config.PrivateKey) != 1 || len(config.HostKey) != 1 || len(config.HostKeyAlgorithms) != 1) {
				testContext.Fatal("native shorthand lists were not preserved")
			}
			if testCase.name == "array lists" && (len(config.PrivateKey) != 2 || len(config.HostKey) != 2 || len(config.HostKeyAlgorithms) != 2) {
				testContext.Fatal("native array lists were not preserved")
			}
		})
	}
}

func TestSSHConfigValidation(testContext *testing.T) {
	for _, testCase := range []struct {
		name   string
		server string
		port   int
		valid  bool
	}{
		{name: "empty server", port: 22},
		{name: "blank server", server: " \t\n", port: 22},
		{name: "negative port", server: "192.0.2.1", port: -1},
		{name: "overflow port", server: "192.0.2.1", port: 65536},
		{name: "default port", server: "192.0.2.1", valid: true},
		{name: "minimum port", server: "192.0.2.1", port: 1, valid: true},
		{name: "standard port", server: "192.0.2.1", port: 22, valid: true},
		{name: "maximum port", server: "192.0.2.1", port: 65535, valid: true},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			config := &models.SSHConfig{Server: testCase.server, ServerPort: testCase.port}
			if err := config.Validate(); (err == nil) != testCase.valid {
				testContext.Fatalf("validation success=%t, expected %t: %v", err == nil, testCase.valid, err)
			}
			if _, err := generateSSHOutbound(config, "ssh-out"); (err == nil) != testCase.valid {
				testContext.Fatalf("generation success=%t, expected %t: %v", err == nil, testCase.valid, err)
			}
			if _, err := buildSSHShareLink("SSH", config); (err == nil) != testCase.valid {
				testContext.Fatalf("export success=%t, expected %t: %v", err == nil, testCase.valid, err)
			}
		})
	}
}

func TestSSHGenerateOutbound(testContext *testing.T) {
	config := fullSSHTestConfig(testContext)
	outbound, err := generateSSHOutbound(&config, "ssh-out")
	if err != nil {
		testContext.Fatal(err)
	}
	expected := sshTestJSONMap(testContext, config)
	expected["type"] = "ssh"
	expected["tag"] = "ssh-out"
	if actual := sshTestJSONMap(testContext, outbound.configFields()); !reflect.DeepEqual(actual, expected) {
		testContext.Fatal("generated SSH fields differ from the native model or dialer options")
	}
	for field, original := range map[string]models.ListableString{
		"private_key": config.PrivateKey, "host_key": config.HostKey, "host_key_algorithms": config.HostKeyAlgorithms,
	} {
		outbound.Extra[field].([]string)[0] = "changed"
		if original[0] == "changed" {
			testContext.Fatalf("generated %s aliases the source configuration", field)
		}
	}

	for _, port := range []int{0, 22, 65535} {
		testContext.Run(strconv.Itoa(port), func(testContext *testing.T) {
			outbound, err := generateSSHOutbound(&models.SSHConfig{Server: "192.0.2.1", ServerPort: port}, "ssh-defaults")
			if err != nil {
				testContext.Fatal(err)
			}
			fields := outbound.configFields()
			_, hasPort := fields["server_port"]
			if hasPort != (port > 0) || len(outbound.Extra) != 0 {
				testContext.Fatal("empty SSH fields or the zero default port must be omitted")
			}
		})
	}
	invalidDialer := models.SSHConfig{Server: "192.0.2.1", DialerOptions: models.DialerOptions{ConnectTimeout: "invalid"}}
	if _, err := generateSSHOutbound(&invalidDialer, "ssh-out"); err == nil {
		testContext.Fatal("invalid dialer duration was accepted")
	}
	service := NewSingBoxService(testContext.TempDir())
	if _, err := service.generateOutboundFromConfig("ssh", &config, "ssh-out"); err != nil {
		testContext.Fatal(err)
	}
	if _, err := service.generateOutboundFromConfig("ssh", &models.DirectConfig{}, "ssh-out"); err == nil {
		testContext.Fatal("outbound dispatch accepted a non-SSH model")
	}
}

func TestSSHDetourResolutionAndCycles(testContext *testing.T) {
	service := NewSingBoxService(testContext.TempDir())
	for _, detour := range []string{"relay", "node-1-out", "direct"} {
		testContext.Run(detour, func(testContext *testing.T) {
			nodes := []models.ProxyNode{
				nativeTestNode(testContext, 1, "relay", "direct", models.DirectConfig{}),
				nativeTestNode(testContext, 2, "SSH", "ssh", models.SSHConfig{Server: "192.0.2.1", DialerOptions: models.DialerOptions{
					Detour: detour, DomainResolver: map[string]any{"server": "local", "strategy": "prefer_ipv4"},
				}}),
			}
			data, err := service.BuildGlobalConfigContext(testContext.Context(), nodes)
			if err != nil {
				testContext.Fatal(err)
			}
			config := decodeGeneratedConfig(testContext, data)
			outbound := generatedEntryByTag(testContext, config, "outbounds", "node-2-out")
			if detour == "direct" {
				if _, exists := outbound["detour"]; exists {
					testContext.Fatal("built-in direct detour must normalize to direct dialing")
				}
			} else if outbound["detour"] != "node-1-out" {
				testContext.Fatal("SSH detour did not resolve to the generated outbound tag")
			}
			if config["dns"] == nil || outbound["domain_resolver"] == nil {
				testContext.Fatal("SSH domain resolver did not enable local DNS")
			}
		})
	}
	for _, testCase := range []struct {
		name      string
		detour    string
		otherName string
		otherHop  string
		wantError string
	}{
		{name: "self", detour: "node-1-out", otherName: "relay", wantError: "itself"},
		{name: "undefined", detour: "missing", otherName: "relay", wantError: "undefined"},
		{name: "cycle", detour: "node-2-out", otherName: "relay", otherHop: "node-1-out", wantError: "cycle"},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			nodes := []models.ProxyNode{
				nativeTestNode(testContext, 1, "SSH", "ssh", models.SSHConfig{Server: "192.0.2.1", DialerOptions: models.DialerOptions{Detour: testCase.detour}}),
				nativeTestNode(testContext, 2, testCase.otherName, "ssh", models.SSHConfig{Server: "192.0.2.2", DialerOptions: models.DialerOptions{Detour: testCase.otherHop}}),
			}
			if _, err := service.BuildGlobalConfigContext(testContext.Context(), nodes); err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				testContext.Fatalf("expected %s detour rejection, got %v", testCase.wantError, err)
			}
		})
	}
	ambiguousNodes := []models.ProxyNode{
		nativeTestNode(testContext, 1, "SSH", "ssh", models.SSHConfig{Server: "192.0.2.1", DialerOptions: models.DialerOptions{Detour: "relay"}}),
		nativeTestNode(testContext, 2, "relay", "direct", models.DirectConfig{}),
		nativeTestNode(testContext, 3, "relay", "ssh", models.SSHConfig{Server: "192.0.2.3"}),
	}
	if _, err := service.BuildGlobalConfigContext(testContext.Context(), ambiguousNodes); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		testContext.Fatalf("ambiguous SSH detour name must be rejected: %v", err)
	}
}

func TestSSHManagedUpstreamsAndInboundCollisions(testContext *testing.T) {
	service := NewSingBoxService(testContext.TempDir())
	for _, testCase := range []struct {
		name      string
		server    string
		port      int
		collision bool
	}{
		{name: "implicit default", server: "127.0.0.1", collision: true},
		{name: "explicit default", server: "127.0.0.1", port: 22, collision: true},
		{name: "IPv6 default", server: "::1", collision: true},
		{name: "localhost default", server: "localhost", collision: true},
		{name: "different port", server: "127.0.0.1", port: 23},
		{name: "remote default", server: "192.0.2.1"},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			upstream := models.SSHConfig{Server: testCase.server, ServerPort: testCase.port}
			targets, err := upstreamDialTargets(&upstream)
			if err != nil || len(targets) != 1 {
				testContext.Fatalf("unexpected SSH dial targets: %v", err)
			}
			port := testCase.port
			if port == 0 {
				port = 22
			}
			if targets[0].ports[0].start != port || targets[0].ports[0].end != port {
				testContext.Fatal("SSH collision detection did not use the effective port")
			}
			node := models.ProxyNode{
				ID: 1, Type: "direct", Config: `{}`, InboundPort: 22, Enabled: true,
				UpstreamMode: models.UpstreamModeCustom, UpstreamType: "ssh",
				UpstreamConfig: marshalSSHTestConfig(testContext, upstream),
			}
			_, err = service.BuildGlobalConfigContext(testContext.Context(), []models.ProxyNode{node})
			if testCase.collision {
				if err == nil || !IsUpstreamValidationError(err) || !strings.Contains(err.Error(), "22") {
					testContext.Fatalf("expected local inbound collision, got %v", err)
				}
			} else if err != nil {
				testContext.Fatal(err)
			}
		})
	}

	sshConfig := models.SSHConfig{Server: "192.0.2.1", ServerPort: 22, User: "root"}
	for _, mode := range []string{models.UpstreamModeGlobal, models.UpstreamModeGroup, models.UpstreamModeCustom} {
		testContext.Run(mode, func(testContext *testing.T) {
			node := nativeTestNode(testContext, 1, "SSH", "ssh", sshConfig)
			node.GroupID = 7
			node.UpstreamMode = mode
			node.UpstreamType = "ssh"
			node.UpstreamConfig = `{"server":"192.0.2.2","server_port":22}`
			settings := models.Settings{
				GlobalUpstreamEnabled: true, GlobalUpstreamType: "ssh", GlobalUpstreamConfig: node.UpstreamConfig,
				GroupUpstreams: map[int]models.GroupUpstream{},
			}
			expectedTag := globalUpstreamTag
			if mode == models.UpstreamModeGroup {
				settings.GroupUpstreams[7] = models.GroupUpstream{Enabled: true, ProxyDefinition: models.ProxyDefinition{Type: "ssh", Config: node.UpstreamConfig}}
				expectedTag = groupUpstreamTag(7)
			} else if mode == models.UpstreamModeCustom {
				expectedTag = "node-1-upstream"
			}
			data, err := service.BuildGlobalConfigContext(testContext.Context(), []models.ProxyNode{node}, settings)
			if err != nil {
				testContext.Fatal(err)
			}
			generated := decodeGeneratedConfig(testContext, data)
			if generatedEntryByTag(testContext, generated, "outbounds", "node-1-out")["detour"] != expectedTag {
				testContext.Fatal("SSH node did not receive its managed upstream")
			}
			if generatedEntryByTag(testContext, generated, "outbounds", expectedTag)["type"] != "ssh" {
				testContext.Fatal("SSH managed upstream did not generate an SSH outbound")
			}
		})
	}
	node := nativeTestNode(testContext, 1, "SSH", "ssh", sshConfig)
	node.UpstreamMode = models.UpstreamModeCustom
	node.UpstreamType = "ssh"
	node.UpstreamConfig = node.Config
	if _, err := service.BuildGlobalConfigContext(testContext.Context(), []models.ProxyNode{node}); err == nil || !strings.Contains(err.Error(), "identical") {
		testContext.Fatalf("identical SSH upstream must be rejected: %v", err)
	}
	if err := service.ValidateUpstreamDefinition(models.ProxyDefinition{Type: "ssh", Config: `{"server":"192.0.2.1","detour":"direct"}`}); err == nil || !strings.Contains(err.Error(), "nested detour") {
		testContext.Fatalf("nested SSH upstream detour must be rejected: %v", err)
	}
}

func TestSSHShareLinkParsing(testContext *testing.T) {
	for _, testCase := range []struct {
		link   string
		server string
		port   int
		user   string
		secret string
		name   string
	}{
		{link: "ssh://192.0.2.1", server: "192.0.2.1", port: 22, name: "SSH Node"},
		{link: "ssh://root@192.0.2.1", server: "192.0.2.1", port: 22, user: "root", name: "SSH Node"},
		{link: "SSH://root:secret@192.0.2.1:2222#SSH%20%E8%8A%82%E7%82%B9", server: "192.0.2.1", port: 2222, user: "root", secret: "secret", name: "SSH 节点"},
		{link: "ssh://u%40ser:p%25%3A%40%2B%2F%3F%23@[::1]:65535#A+B", server: "::1", port: 65535, user: "u@ser", secret: "p%:@+/?#", name: "A+B"},
		{link: "ssh://:secret@192.0.2.1", server: "192.0.2.1", port: 22, secret: "secret", name: "SSH Node"},
	} {
		testContext.Run(testCase.name+strconv.Itoa(testCase.port)+testCase.user, func(testContext *testing.T) {
			parsed, proxyType, name, err := ParseShareLink(testCase.link)
			if err != nil {
				testContext.Fatal(err)
			}
			config, ok := parsed.(models.SSHConfig)
			if !ok || proxyType != "ssh" || name != testCase.name || config.Server != testCase.server || config.ServerPort != testCase.port || config.User != testCase.user || config.Password != testCase.secret {
				testContext.Fatal("SSH URL authority, credentials, default port or fragment was parsed incorrectly")
			}
		})
	}
	parsed, _, _, err := ParseShareLink("ssh://192.0.2.1?private_key=line1&private_key=line2&host_key=host1&host_key=host2&host_key_algorithms=ssh-ed25519,rsa-sha2-512&client_version=SSH-2.0-SBPM")
	if err != nil {
		testContext.Fatal(err)
	}
	config := parsed.(models.SSHConfig)
	if !reflect.DeepEqual(config.PrivateKey, models.ListableString{"line1", "line2"}) || !reflect.DeepEqual(config.HostKey, models.ListableString{"host1", "host2"}) || !reflect.DeepEqual(config.HostKeyAlgorithms, models.ListableString{"ssh-ed25519", "rsa-sha2-512"}) || config.ClientVersion != "SSH-2.0-SBPM" {
		testContext.Fatal("SSH repeated keys or comma-separated algorithms were lost")
	}
	for _, link := range []string{
		"ssh://", "ssh://root:credential@", "ssh://192.0.2.1:0", "ssh://192.0.2.1:-1",
		"ssh://192.0.2.1:65536", "ssh://192.0.2.1:port", "ssh://root:%GG@192.0.2.1",
		"ssh://192.0.2.1?private_key=%GG", "ssh://192.0.2.1?domain_resolver_options=%7B",
	} {
		if _, _, _, err := ParseShareLink(link); err == nil {
			testContext.Fatal("invalid SSH URL was accepted")
		} else if strings.Contains(err.Error(), "credential") || strings.Contains(err.Error(), link) {
			testContext.Fatal("SSH URL parse errors expose the input credentials")
		}
	}
}

func TestSSHShareLinkRoundTrip(testContext *testing.T) {
	fullConfig := fullSSHTestConfig(testContext)
	lineConfig := fullConfig
	lineConfig.PrivateKey = models.ListableString(strings.Split(strings.TrimSuffix(fullConfig.PrivateKey[0], "\n"), "\n"))
	for _, testCase := range []struct {
		name   string
		config models.SSHConfig
	}{
		{name: "minimal", config: models.SSHConfig{Server: "192.0.2.1"}},
		{name: "user without secret", config: models.SSHConfig{Server: "192.0.2.1", ServerPort: 22, User: "root"}},
		{name: "multiline key + IPv6 % 中文", config: fullConfig},
		{name: "key line arrays", config: lineConfig},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			node := nativeTestNode(testContext, 1, testCase.name, "ssh", testCase.config)
			link, err := BuildShareLink(node)
			if err != nil {
				testContext.Fatal(err)
			}
			parsed, proxyType, name, err := ParseShareLink(link)
			if err != nil || proxyType != "ssh" || name != testCase.name {
				testContext.Fatalf("SSH export could not be imported: %v", err)
			}
			expected := testCase.config
			if expected.ServerPort == 0 {
				expected.ServerPort = 22
			}
			if !reflect.DeepEqual(sshTestJSONMap(testContext, parsed), sshTestJSONMap(testContext, expected)) {
				testContext.Fatal("SSH credentials, lists, key whitespace or dialer options did not round-trip")
			}
			secondLink, err := BuildShareLink(nativeTestNode(testContext, 1, name, proxyType, parsed))
			if err != nil || secondLink != link {
				testContext.Fatalf("SSH export is not stable across repeated round-trips: %v", err)
			}
		})
	}
}

func TestSSHClashYAMLImport(testContext *testing.T) {
	privateKey, signer := sshTestKey(testContext, "")
	for _, style := range []string{"hyphen", "underscore"} {
		testContext.Run(style, func(testContext *testing.T) {
			field := func(name string) string {
				if style == "hyphen" {
					return strings.ReplaceAll(name, "_", "-")
				}
				return name
			}
			userField := "username"
			if style == "underscore" {
				userField = "user"
			}
			proxyConfig := map[string]any{
				"type": "ssh", "name": "SSH YAML", "server": "192.0.2.1", userField: " root ", "password": " secret :@+% ",
				field("private_key"): privateKey, field("private_key_path"): "/tmp/key path.pem", field("private_key_passphrase"): " passphrase :@+% ",
				field("host_key"): []string{string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}, field("host_key_algorithms"): []string{ssh.KeyAlgoED25519},
				field("client_version"): "SSH-2.0-SBPM", "dialer-proxy": "relay", "interface-name": "lo", "routing-mark": 16,
				"connect-timeout": 5, "udp-fragment": false, "domain-resolver": map[string]any{"server": "local"},
			}
			encoded, err := yaml.Marshal(map[string]any{"proxies": []any{proxyConfig}})
			if err != nil {
				testContext.Fatal(err)
			}
			items, failures, err := ExpandBatchImportSourcesWithType(testContext.Context(), []string{string(encoded)}, BatchImportSourceAuto)
			if err != nil || len(items) != 1 || len(failures) != 0 {
				testContext.Fatalf("SSH YAML import failed: %v, failures=%v", err, failures)
			}
			config, ok := items[0].Config.(models.SSHConfig)
			if !ok || items[0].Type != "ssh" || config.ServerPort != 22 || config.User != " root " || config.Password != " secret :@+% " || config.PrivateKeyPassphrase != " passphrase :@+% " || config.PrivateKeyPath != "/tmp/key path.pem" || config.ClientVersion != "SSH-2.0-SBPM" {
				testContext.Fatal("SSH aliases, default port or credential whitespace were not preserved")
			}
			if !reflect.DeepEqual(config.PrivateKey, models.ListableString{privateKey}) || !reflect.DeepEqual(config.HostKey, models.ListableString{string(ssh.MarshalAuthorizedKey(signer.PublicKey()))}) || !reflect.DeepEqual(config.HostKeyAlgorithms, models.ListableString{ssh.KeyAlgoED25519}) {
				testContext.Fatal("Clash SSH listable fields were not preserved")
			}
			if config.Detour != "relay" || config.BindInterface != "lo" || config.ConnectTimeout != "5s" || config.UDPFragment == nil || *config.UDPFragment || config.DomainResolver == nil || config.RoutingMark != 16 {
				testContext.Fatal("Clash SSH dialer options were not applied")
			}
			if _, err := generateSSHOutbound(&config, "ssh-yaml"); err != nil {
				testContext.Fatal(err)
			}
		})
	}
	for _, testCase := range []struct {
		name  string
		field string
		value any
	}{
		{name: "empty server", field: "server", value: ""},
		{name: "negative port", field: "port", value: -1},
		{name: "overflow port", field: "port", value: 65536},
		{name: "invalid port", field: "port", value: "invalid"},
		{name: "fractional port", field: "port", value: 22.5},
		{name: "invalid key", field: "private-key", value: 42},
		{name: "invalid host list", field: "host-key", value: []any{"host", 42}},
		{name: "invalid algorithm list", field: "host-key-algorithms", value: map[string]any{}},
		{name: "invalid dialer", field: "ip-version", value: "unknown"},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			proxyConfig := map[string]any{"type": "ssh", "server": "192.0.2.1", testCase.field: testCase.value}
			if _, err := convertClashProxyToImportItem(proxyConfig); err == nil {
				testContext.Fatal("invalid Clash SSH configuration was accepted")
			}
		})
	}
	for _, port := range []any{nil, 0, "2222", 65535} {
		if _, err := convertClashProxyToImportItem(map[string]any{"type": "ssh", "server": "192.0.2.1", "port": port}); err != nil {
			testContext.Fatalf("valid Clash SSH port %v was rejected: %v", port, err)
		}
	}
}

func TestSSHDispatchRegression(testContext *testing.T) {
	service := NewSingBoxService(testContext.TempDir())
	for _, link := range []string{
		"ss://YWVzLTEyOC1nY206c2VjcmV0@192.0.2.1:8388#SS",
		"socks5://user:secret@192.0.2.1:1080#SOCKS",
		"socks5h://user:secret@192.0.2.1:1080#SOCKS",
		"http://user:secret@192.0.2.1:8080?proxy=1#HTTP",
		"https://user:secret@192.0.2.1:443?proxy=1#HTTPS",
	} {
		parsed, proxyType, name, err := ParseShareLink(link)
		if err != nil {
			testContext.Fatal(err)
		}
		node := nativeTestNode(testContext, 1, name, proxyType, parsed)
		if _, err := BuildShareLink(node); err != nil {
			testContext.Fatalf("existing %s export regressed: %v", proxyType, err)
		}
		modelConfig, err := node.ParseConfig()
		if err != nil {
			testContext.Fatal(err)
		}
		if _, err := service.generateOutboundFromConfig(proxyType, modelConfig, "regression-out"); err != nil {
			testContext.Fatalf("existing %s generation regressed: %v", proxyType, err)
		}
	}
	items, failures, err := ExpandBatchImportSourcesWithType(testContext.Context(), []string{`
proxies:
  - {name: SSH, type: ssh, server: 192.0.2.1}
  - {name: SOCKS, type: socks5, server: 192.0.2.2, port: 1080}
  - {name: Invalid SSH, type: ssh, server: 192.0.2.3, port: 65536}
`}, BatchImportSourceAuto)
	if err != nil || len(items) != 2 || len(failures) != 1 {
		testContext.Fatalf("mixed subscription failure isolation regressed: %v", err)
	}
}

func TestSSHCredentialRedaction(testContext *testing.T) {
	for _, field := range []string{"passphrase", "private_key_passphrase", "private-key-passphrase", "PRIVATE_KEY_PASSPHRASE"} {
		if !ipCheckSecretKey.MatchString(field) {
			testContext.Fatalf("credential matcher does not recognize %s", field)
		}
		message := field + `="ssh-passphrase-sensitive"`
		if result := SafeDiagnostic(message); strings.Contains(result, "ssh-passphrase-sensitive") || !strings.Contains(result, "[redacted]") {
			testContext.Fatalf("assignment diagnostic did not redact %s", field)
		}
	}
	config := models.SSHConfig{
		Server: "192.0.2.1", User: "ssh-user-sensitive", Password: "ssh-login-sensitive",
		PrivateKey: models.ListableString{"ssh-key-sensitive-line1", "ssh-key-sensitive-line2"}, PrivateKeyPassphrase: "ssh-passphrase-sensitive",
	}
	node := models.ProxyNode{Type: "ssh", Config: marshalSSHTestConfig(testContext, config), UpstreamConfig: `{"private_key_passphrase":"ssh-upstream-sensitive"}`}
	message := "ssh-user-sensitive ssh-login-sensitive ssh-key-sensitive-line1 ssh-key-sensitive-line2 ssh-passphrase-sensitive ssh-upstream-sensitive"
	result := NodeIPCheckDiagnostic(message, node)
	for _, credential := range strings.Fields(message) {
		if strings.Contains(result, credential) {
			testContext.Fatal("SSH node or upstream credentials leaked into a diagnostic")
		}
	}
	if !strings.Contains(result, "[redacted]") {
		testContext.Fatal("SSH diagnostic contains no redaction markers")
	}
}

func TestSSHRealSingBoxForwarding(testContext *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		testContext.Skip("SINGBOX_TEST_BINARY not set")
	}
	const responseBody = "sbpm-real-ssh-forwarding-ok"
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, responseBody+request.URL.Path)
	}))
	testContext.Cleanup(target.Close)
	targetURL, err := url.Parse(target.URL)
	if err != nil {
		testContext.Fatal(err)
	}
	plainKey, userSigner := sshTestKey(testContext, "")
	encryptedKey, encryptedSigner := sshTestKey(testContext, "ssh-e2e-passphrase")
	_, hostSigner := sshTestKey(testContext, "")
	serverConfig := &ssh.ServerConfig{
		PasswordCallback: func(metadata ssh.ConnMetadata, credential []byte) (*ssh.Permissions, error) {
			if metadata.User() == "root" && string(credential) == "ssh-e2e-secret" {
				return nil, nil
			}
			return nil, fmt.Errorf("SSH authentication rejected")
		},
		PublicKeyCallback: func(metadata ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if metadata.User() == "root" && (bytes.Equal(key.Marshal(), userSigner.PublicKey().Marshal()) || bytes.Equal(key.Marshal(), encryptedSigner.PublicKey().Marshal())) {
				return nil, nil
			}
			return nil, fmt.Errorf("SSH authentication rejected")
		},
	}
	serverConfig.AddHostKey(hostSigner)
	serverAddress, forwarded := startSSHLoopbackServer(testContext, serverConfig, targetURL.Host)
	serverHost, serverPortText, err := net.SplitHostPort(serverAddress)
	if err != nil {
		testContext.Fatal(err)
	}
	serverPort, err := strconv.Atoi(serverPortText)
	if err != nil {
		testContext.Fatal(err)
	}
	keyDirectory := testContext.TempDir()
	plainKeyPath := filepath.Join(keyDirectory, "plain key.pem")
	encryptedKeyPath := filepath.Join(keyDirectory, "encrypted key.pem")
	for path, contents := range map[string]string{plainKeyPath: plainKey, encryptedKeyPath: encryptedKey} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			testContext.Fatal(err)
		}
	}
	for _, testCase := range []struct {
		name      string
		config    models.SSHConfig
		managed   bool
		wantError string
	}{
		{name: "default-root-password", config: models.SSHConfig{Password: "ssh-e2e-secret"}},
		{name: "inline-key", config: models.SSHConfig{User: "root", PrivateKey: models.ListableString{plainKey}}},
		{name: "encrypted-key-lines", config: models.SSHConfig{User: "root", PrivateKey: models.ListableString(strings.Split(strings.TrimSuffix(encryptedKey, "\n"), "\n")), PrivateKeyPassphrase: "ssh-e2e-passphrase"}},
		{name: "private-key-path", config: models.SSHConfig{User: "root", PrivateKeyPath: plainKeyPath}},
		{name: "encrypted-key-upstream", managed: true, config: models.SSHConfig{User: "root", PrivateKeyPath: encryptedKeyPath, PrivateKeyPassphrase: "ssh-e2e-passphrase"}},
		{name: "host-key-mismatch", wantError: "host key mismatch", config: models.SSHConfig{Password: "ssh-e2e-secret", HostKey: models.ListableString{string(ssh.MarshalAuthorizedKey(userSigner.PublicKey()))}}},
		{name: "authentication-failure", wantError: "unable to authenticate", config: models.SSHConfig{Password: "ssh-e2e-invalid"}},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			config := testCase.config
			config.Server = serverHost
			config.ServerPort = serverPort
			if len(config.HostKey) == 0 {
				config.HostKey = models.ListableString{string(ssh.MarshalAuthorizedKey(hostSigner.PublicKey()))}
			}
			config.HostKeyAlgorithms = models.ListableString{ssh.KeyAlgoED25519}
			config.ClientVersion = "SSH-2.0-SBPM_SSH_Test"
			config.ConnectTimeout = "3s"
			link, err := BuildShareLink(nativeTestNode(testContext, 1, testCase.name, "ssh", config))
			if err != nil {
				testContext.Fatal(err)
			}
			parsed, proxyType, name, err := ParseShareLink(link)
			if err != nil {
				testContext.Fatal(err)
			}
			node := nativeTestNode(testContext, 1, name, proxyType, parsed)
			if testCase.managed {
				node.Type = "direct"
				node.UpstreamMode = models.UpstreamModeCustom
				node.UpstreamType = "ssh"
				node.UpstreamConfig = node.Config
				node.Config = `{}`
			}
			node.InboundPort = reserveLiveProtocolPort(testContext, "tcp")
			service := NewSingBoxService(testContext.TempDir(), WithBinaryPath(binary))
			configJSON, err := service.BuildGlobalConfigContext(testContext.Context(), []models.ProxyNode{node})
			if err != nil {
				testContext.Fatal(err)
			}
			generated := decodeGeneratedConfig(testContext, configJSON)
			for _, inbound := range generated["inbounds"].([]any) {
				inbound.(map[string]any)["listen"] = "127.0.0.1"
			}
			logPath := startLiveSingBoxProcess(testContext, binary, generated)
			inboundAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(node.InboundPort))
			if err := waitForTCPReady(testContext.Context(), inboundAddress, 3*time.Second); err != nil {
				testContext.Fatal(err)
			}
			for _, scheme := range []string{"http", "socks5"} {
				transport := &http.Transport{}
				if scheme == "http" {
					transport.Proxy = http.ProxyURL(&url.URL{Scheme: "http", Host: inboundAddress})
				} else {
					dialer, err := proxy.SOCKS5("tcp", inboundAddress, nil, &net.Dialer{Timeout: 3 * time.Second})
					if err != nil {
						testContext.Fatal(err)
					}
					transport.DialContext = dialer.(proxy.ContextDialer).DialContext
				}
				testContext.Cleanup(transport.CloseIdleConnections)
				client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
				baseline := forwarded.Load()
				requestPath := "/" + testCase.name + "/" + scheme
				response, err := client.Get(target.URL + requestPath)
				if testCase.wantError != "" {
					if response != nil {
						_ = response.Body.Close()
						if response.StatusCode < http.StatusBadRequest {
							testContext.Fatal("SSH connection with invalid credentials or host key succeeded")
						}
					}
					if forwarded.Load() != baseline {
						testContext.Fatal("rejected SSH authentication still forwarded traffic")
					}
					logData, _ := os.ReadFile(logPath)
					if !strings.Contains(string(logData), testCase.wantError) {
						testContext.Fatalf("missing SSH rejection reason %q: %v\n%s", testCase.wantError, err, NodeIPCheckDiagnostic(string(logData), node))
					}
					return
				}
				if err != nil {
					logData, _ := os.ReadFile(logPath)
					testContext.Fatalf("real SSH forwarding failed: %v\n%s", err, NodeIPCheckDiagnostic(string(logData), node))
				}
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil || response.StatusCode != http.StatusOK || string(body) != responseBody+requestPath || forwarded.Load() <= baseline {
					testContext.Fatalf("traffic did not traverse a real SSH direct-tcpip channel: status=%d, read_error=%v", response.StatusCode, readErr)
				}
				transport.CloseIdleConnections()
			}
		})
	}
}

func fullSSHTestConfig(testContext *testing.T) models.SSHConfig {
	testContext.Helper()
	privateKey, signer := sshTestKey(testContext, "")
	_, secondSigner := sshTestKey(testContext, "")
	udpFragment := false
	return models.SSHConfig{
		Server: "::1", ServerPort: 2222, User: " root:@+% 中文 ", Password: " secret:@+%/#? 中文 ",
		PrivateKey: models.ListableString{privateKey}, PrivateKeyPath: "/tmp/private key @+%.pem", PrivateKeyPassphrase: " passphrase:@+% 中文 ",
		HostKey:           models.ListableString{string(ssh.MarshalAuthorizedKey(signer.PublicKey())), string(ssh.MarshalAuthorizedKey(secondSigner.PublicKey()))},
		HostKeyAlgorithms: models.ListableString{ssh.KeyAlgoED25519, ssh.KeyAlgoRSASHA512}, ClientVersion: "SSH-2.0-SBPM",
		DialerOptions: models.DialerOptions{
			Detour: "relay", BindInterface: "lo", Inet4BindAddress: "127.0.0.1", Inet6BindAddress: "::1",
			ProtectPath: "/tmp/ssh-protect.sock", RoutingMark: "0x1234", ReuseAddr: true, NetNS: "ssh-test",
			ConnectTimeout: "5s", TCPFastOpen: true, TCPMultiPath: true, UDPFragment: &udpFragment,
			DomainResolver: map[string]any{"server": "local", "strategy": "prefer_ipv4"}, NetworkStrategy: "hybrid",
			NetworkType: models.ListableString{"wifi"}, FallbackNetworkType: models.ListableString{"cellular"},
			FallbackDelay: "300ms", DomainStrategy: "prefer_ipv4",
		},
	}
}

func sshTestKey(testContext *testing.T, passphrase string) (string, ssh.Signer) {
	testContext.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		testContext.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		testContext.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(privateKey, "sbpm-ssh-test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(privateKey, "sbpm-ssh-test", []byte(passphrase))
	}
	if err != nil {
		testContext.Fatal(err)
	}
	return string(pem.EncodeToMemory(block)), signer
}

func marshalSSHTestConfig(testContext *testing.T, config models.SSHConfig) string {
	testContext.Helper()
	encoded, err := json.Marshal(config)
	if err != nil {
		testContext.Fatal(err)
	}
	return string(encoded)
}

func sshTestJSONMap(testContext *testing.T, value any) map[string]any {
	testContext.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		testContext.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		testContext.Fatal(err)
	}
	return fields
}

func startSSHLoopbackServer(testContext *testing.T, serverConfig *ssh.ServerConfig, destination string) (string, *atomic.Int64) {
	testContext.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		testContext.Fatal(err)
	}
	forwarded := &atomic.Int64{}
	var activeConnections sync.Map
	var workers sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			activeConnections.Store(connection, true)
			workers.Go(func() {
				defer activeConnections.Delete(connection)
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
				serverConnection, channels, requests, err := ssh.NewServerConn(connection, serverConfig)
				if err != nil {
					return
				}
				defer serverConnection.Close()
				_ = connection.SetDeadline(time.Time{})
				workers.Go(func() { ssh.DiscardRequests(requests) })
				for newChannel := range channels {
					if newChannel.ChannelType() != "direct-tcpip" {
						_ = newChannel.Reject(ssh.UnknownChannelType, "only direct-tcpip is supported")
						continue
					}
					var request struct {
						Host       string
						Port       uint32
						OriginHost string
						OriginPort uint32
					}
					if err := ssh.Unmarshal(newChannel.ExtraData(), &request); err != nil || net.JoinHostPort(request.Host, strconv.Itoa(int(request.Port))) != destination {
						_ = newChannel.Reject(ssh.Prohibited, "only the loopback test target is allowed")
						continue
					}
					targetConnection, err := net.DialTimeout("tcp", destination, 3*time.Second)
					if err != nil {
						_ = newChannel.Reject(ssh.ConnectionFailed, "loopback test target unavailable")
						continue
					}
					channel, channelRequests, err := newChannel.Accept()
					if err != nil {
						_ = targetConnection.Close()
						continue
					}
					forwarded.Add(1)
					workers.Go(func() { ssh.DiscardRequests(channelRequests) })
					workers.Go(func() {
						defer channel.Close()
						defer targetConnection.Close()
						copyDone := make(chan struct{})
						go func() {
							_, _ = io.Copy(targetConnection, channel)
							_ = targetConnection.(*net.TCPConn).CloseWrite()
							close(copyDone)
						}()
						_, _ = io.Copy(channel, targetConnection)
						_ = channel.CloseWrite()
						<-copyDone
					})
				}
			})
		}
	}()
	testContext.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		activeConnections.Range(func(connection, _ any) bool {
			_ = connection.(net.Conn).Close()
			return true
		})
		workersDone := make(chan struct{})
		go func() {
			workers.Wait()
			close(workersDone)
		}()
		select {
		case <-workersDone:
		case <-time.After(5 * time.Second):
			testContext.Error("SSH loopback server did not stop")
		}
	})
	return listener.Addr().String(), forwarded
}
