package services

import (
	"encoding/base64"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestWireGuardReservedErrorsExcludeSensitiveInput(testContext *testing.T) {
	const credential = "wireguardReservedSensitiveCredential"
	const numericCredential = "987654321098765432109876543210"
	for _, fixture := range []struct {
		name      string
		value     any
		sensitive string
	}{
		{"comma", "1," + credential + ",3", credential},
		{"semicolon", "1;" + credential + ";3", credential},
		{"whitespace", "1\t" + credential + "\n3", credential},
		{"json-string", `[1,"` + credential + `",3]`, credential},
		{"json-number-overflow", "[1," + numericCredential + ",3]", numericCredential},
		{"json-out-of-range", "[1,987654321,3]", "987654321"},
		{"delimited-out-of-range", "1,987654321,3", "987654321"},
		{"invalid-encoding", credential, credential},
		{"integer-array", []int{1, 987654321, 3}, "987654321"},
		{"mixed-array", []interface{}{1, credential, 3}, credential},
		{"unsupported-object", map[string]string{credential: credential}, credential},
	} {
		testContext.Run(fixture.name, func(testContext *testing.T) {
			reserved, err := parseWireGuardReservedAny(fixture.value)
			if err == nil || reserved != nil || !strings.Contains(err.Error(), "reserved") {
				testContext.Fatal("expected a reserved diagnostic without partial output")
			}
			if strings.Contains(err.Error(), fixture.sensitive) {
				testContext.Error("reserved diagnostic exposed sensitive input")
			}
		})
	}
}

func TestWireGuardReservedFormatsRemainSupported(testContext *testing.T) {
	for _, fixture := range []struct {
		name  string
		value any
	}{
		{"json", "[1,2,255]"},
		{"comma", "1,2,255"},
		{"semicolon", "1;2;255"},
		{"whitespace", "1\t2\n255"},
		{"hex", "0102ff"},
		{"hex-prefix", "0x0102ff"},
		{"base64", "AQL/"},
		{"base64-url", "AQL_"},
		{"bytes", []byte{1, 2, 255}},
		{"integers", []int{1, 2, 255}},
		{"mixed-integers", []interface{}{1, int64(2), float64(255)}},
	} {
		testContext.Run(fixture.name, func(testContext *testing.T) {
			reserved, err := parseWireGuardReservedAny(fixture.value)
			if err != nil || !reflect.DeepEqual(reserved, []uint8{1, 2, 255}) {
				testContext.Fatalf("valid reserved format changed: %v", err)
			}
		})
	}
}

func TestShareOptionDiagnosticsDoNotEchoValues(testContext *testing.T) {
	for _, network := range []string{"all,tcp", "both,tcp"} {
		_, err := normalizeNetworkList(network)
		if err == nil || err.Error() != "network all/both cannot be combined with other values" {
			testContext.Error("combined wildcard networks must use a fixed diagnostic")
		}
	}
	for _, transport := range []string{"kcp", "mkcp", "xhttp", "splithttp", "mekya"} {
		_, _, err := buildV2RayTransportOptions(url.Values{"type": {transport}})
		if err == nil || err.Error() != "transport is not supported by sing-box 1.12.12" {
			testContext.Error("unsupported transports must use a fixed diagnostic")
		}
	}
}

func TestShareLinkDiagnosticsExcludeSensitiveInputs(testContext *testing.T) {
	const credential = "shareLinkSensitiveFixture"
	const numericCredential = "987654321098765432109876543210"
	for _, protocol := range []struct {
		name, scheme, userinfo string
		query                  url.Values
	}{
		{"ss", "ss", "aes-128-gcm:" + credential, nil},
		{"vmess-url", "vmess", credential, nil},
		{"vless", "vless", credential, nil},
		{"trojan", "trojan", credential, nil},
		{"tuic", "tuic", "fixture-id:" + credential, nil},
		{"anytls", "anytls", credential, nil},
		{"http", "http", "fixture-user:" + credential, nil},
		{"https", "https", "fixture-user:" + credential, nil},
		{"socks", "socks", "fixture-user:" + credential, nil},
		{"socks5", "socks5", "fixture-user:" + credential, nil},
		{"socks5h", "socks5h", "fixture-user:" + credential, nil},
		{"hysteria2", "hysteria2", credential, nil},
		{"hy2", "hy2", credential, nil},
		{"wireguard", "wireguard", credential, url.Values{"address": {"10.0.0.2/32"}, "publickey": {"fixture-peer"}}},
		{"wg", "wg", credential, url.Values{"address": {"10.0.0.2/32"}, "publickey": {"fixture-peer"}}},
	} {
		testContext.Run(protocol.name, func(testContext *testing.T) {
			base := protocol.scheme + "://" + protocol.userinfo + "@192.0.2.1:443"
			query := make(url.Values)
			for key, values := range protocol.query {
				query[key] = append([]string(nil), values...)
			}
			query.Set("domain_resolver_options", `{"`+credential+`":`+strings.Repeat("9", 400)+`}`)
			for _, fixture := range []struct{ name, link string }{
				{"malformed-url", base + "/" + credential + "%?" + protocol.query.Encode()},
				{"invalid-port", strings.Replace(base, ":443", ":"+credential, 1) + "?" + protocol.query.Encode()},
				{"invalid-resolver-json", base + "?" + query.Encode()},
			} {
				testContext.Run(fixture.name, func(testContext *testing.T) {
					config, proxyType, name, err := ParseShareLink(fixture.link)
					if err == nil || config != nil || proxyType != "" || name != "" {
						testContext.Fatal("expected a share-link error without partial output")
					}
					for _, sensitive := range []string{credential, strings.ToLower(credential), strings.Repeat("9", 400), fixture.link} {
						if strings.Contains(err.Error(), sensitive) {
							testContext.Error("share-link diagnostic exposed sensitive input")
						}
					}
				})
			}
		})
	}
	for _, fixture := range []struct{ name, payload string }{
		{"invalid-json", `{"id":"` + credential + `","port":` + numericCredential},
		{"invalid-port", `{"add":"192.0.2.1","id":"` + credential + `","port":"` + credential + `"}`},
		{"unsupported-cipher", `{"add":"192.0.2.1","id":"` + credential + `","port":443,"scy":"` + credential + `"}`},
		{"unsupported-security", `{"add":"192.0.2.1","id":"` + credential + `","port":443,"tls":"` + credential + `"}`},
		{"unsupported-transport", `{"add":"192.0.2.1","id":"` + credential + `","port":443,"net":"` + credential + `"}`},
	} {
		testContext.Run("vmess-json/"+fixture.name, func(testContext *testing.T) {
			link := "vmess://" + base64.RawStdEncoding.EncodeToString([]byte(fixture.payload))
			_, _, _, err := ParseShareLink(link)
			if err == nil {
				testContext.Fatal("expected a VMess JSON diagnostic")
			}
			for _, sensitive := range []string{credential, strings.ToLower(credential), numericCredential, fixture.payload, link} {
				if strings.Contains(err.Error(), sensitive) {
					testContext.Error("VMess JSON diagnostic exposed sensitive input")
				}
			}
		})
	}
}
