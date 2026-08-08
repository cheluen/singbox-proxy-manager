package services

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"sb-proxy/backend/models"
)

func TestProtocolImportsForwardTrafficThroughRealSingBox(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}

	const responseBody = "sbpm-protocol-import-live-ok"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(target.Close)

	const (
		vmessUUID = "00000000-0000-0000-0000-000000000041"
		tuicUUID  = "00000000-0000-0000-0000-000000000042"
	)

	tests := []struct {
		name          string
		serverNetwork string
		clashYAML     func(port int) string
		serverConfig  func(t *testing.T, port int) map[string]any
		assertImport  func(t *testing.T, config any)
	}{
		{
			name:          "vmess-aes-128-cfb",
			serverNetwork: "tcp",
			clashYAML: func(port int) string {
				return fmt.Sprintf(`
proxies:
  - name: vmess-live
    type: vmess
    server: 127.0.0.1
    port: %d
    uuid: %s
    cipher: aes-128-cfb
`, port, vmessUUID)
			},
			serverConfig: func(_ *testing.T, port int) map[string]any {
				return liveProtocolServerConfig(map[string]any{
					"type":        "vmess",
					"tag":         "vmess-in",
					"listen":      "127.0.0.1",
					"listen_port": port,
					"users": []map[string]any{{
						"uuid": vmessUUID,
					}},
				})
			},
			assertImport: func(t *testing.T, config any) {
				t.Helper()
				vmess, ok := config.(models.VMESSConfig)
				if !ok || vmess.Security != "aes-128-cfb" {
					t.Fatalf("VMess CFB import mismatch: %#v", config)
				}
			},
		},
		{
			name:          "tuic-empty-password",
			serverNetwork: "udp",
			clashYAML: func(port int) string {
				return fmt.Sprintf(`
proxies:
  - name: tuic-live
    type: tuic
    server: 127.0.0.1
    port: %d
    uuid: %s
    password: ""
    skip-cert-verify: true
`, port, tuicUUID)
			},
			serverConfig: func(t *testing.T, port int) map[string]any {
				certPath, keyPath := writeLiveProtocolCertificate(t)
				return liveProtocolServerConfig(map[string]any{
					"type":        "tuic",
					"tag":         "tuic-in",
					"listen":      "127.0.0.1",
					"listen_port": port,
					"users": []map[string]any{{
						"uuid":     tuicUUID,
						"password": "",
					}},
					"tls": map[string]any{
						"enabled":          true,
						"certificate_path": certPath,
						"key_path":         keyPath,
					},
				})
			},
			assertImport: func(t *testing.T, config any) {
				t.Helper()
				tuic, ok := config.(models.TUICConfig)
				if !ok || tuic.Password != "" {
					t.Fatalf("TUIC empty-password import mismatch: %#v", config)
				}
			},
		},
		{
			name:          "trojan-empty-password",
			serverNetwork: "tcp",
			clashYAML: func(port int) string {
				return fmt.Sprintf(`
proxies:
  - name: trojan-empty-live
    type: trojan
    server: 127.0.0.1
    port: %d
    password: ""
    skip-cert-verify: true
`, port)
			},
			serverConfig: func(t *testing.T, port int) map[string]any {
				certPath, keyPath := writeLiveProtocolCertificate(t)
				return liveProtocolServerConfig(map[string]any{
					"type":        "trojan",
					"tag":         "trojan-empty-in",
					"listen":      "127.0.0.1",
					"listen_port": port,
					"users":       []map[string]any{{"password": ""}},
					"tls": map[string]any{
						"enabled":          true,
						"certificate_path": certPath,
						"key_path":         keyPath,
					},
				})
			},
			assertImport: func(t *testing.T, config any) {
				t.Helper()
				trojan, ok := config.(models.TrojanConfig)
				if !ok || trojan.Password != "" {
					t.Fatalf("Trojan empty-password import mismatch: %#v", config)
				}
			},
		},
		{
			name:          "hysteria2-empty-password",
			serverNetwork: "udp",
			clashYAML: func(port int) string {
				return fmt.Sprintf(`
proxies:
  - name: hysteria2-empty-live
    type: hysteria2
    server: 127.0.0.1
    port: %d
    password: ""
    skip-cert-verify: true
`, port)
			},
			serverConfig: func(t *testing.T, port int) map[string]any {
				certPath, keyPath := writeLiveProtocolCertificate(t)
				return liveProtocolServerConfig(map[string]any{
					"type":        "hysteria2",
					"tag":         "hysteria2-empty-in",
					"listen":      "127.0.0.1",
					"listen_port": port,
					"users":       []map[string]any{{"password": ""}},
					"tls": map[string]any{
						"enabled":          true,
						"certificate_path": certPath,
						"key_path":         keyPath,
					},
				})
			},
			assertImport: func(t *testing.T, config any) {
				t.Helper()
				hysteria2, ok := config.(models.Hysteria2Config)
				if !ok || hysteria2.Password != "" {
					t.Fatalf("Hysteria2 empty-password import mismatch: %#v", config)
				}
			},
		},
		{
			name:          "anytls-empty-password",
			serverNetwork: "tcp",
			clashYAML: func(port int) string {
				return fmt.Sprintf(`
proxies:
  - name: anytls-empty-live
    type: anytls
    server: 127.0.0.1
    port: %d
    password: ""
    skip-cert-verify: true
`, port)
			},
			serverConfig: func(t *testing.T, port int) map[string]any {
				certPath, keyPath := writeLiveProtocolCertificate(t)
				return liveProtocolServerConfig(map[string]any{
					"type":        "anytls",
					"tag":         "anytls-empty-in",
					"listen":      "127.0.0.1",
					"listen_port": port,
					"users":       []map[string]any{{"password": ""}},
					"tls": map[string]any{
						"enabled":          true,
						"certificate_path": certPath,
						"key_path":         keyPath,
					},
				})
			},
			assertImport: func(t *testing.T, config any) {
				t.Helper()
				anyTLS, ok := config.(models.AnyTLSConfig)
				if !ok || anyTLS.Password != "" {
					t.Fatalf("AnyTLS empty-password import mismatch: %#v", config)
				}
			},
		},
		{
			name:          "shadowsocks-whitespace-password",
			serverNetwork: "tcp",
			clashYAML: func(port int) string {
				return fmt.Sprintf(`
proxies:
  - name: shadowsocks-live
    type: ss
    server: 127.0.0.1
    port: %d
    cipher: aes-128-gcm
    password: " live secret "
`, port)
			},
			serverConfig: func(_ *testing.T, port int) map[string]any {
				return liveProtocolServerConfig(map[string]any{
					"type":        "shadowsocks",
					"tag":         "shadowsocks-in",
					"listen":      "127.0.0.1",
					"listen_port": port,
					"method":      "aes-128-gcm",
					"password":    " live secret ",
				})
			},
			assertImport: func(t *testing.T, config any) {
				t.Helper()
				shadowsocks, ok := config.(models.SSConfig)
				if !ok || shadowsocks.Password != " live secret " {
					t.Fatalf("Shadowsocks whitespace password import mismatch: %#v", config)
				}
			},
		},
	}

	for index, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			serverPort := reserveLiveProtocolPort(t, testCase.serverNetwork)
			serverLog := startLiveSingBoxProcess(t, binary, testCase.serverConfig(t, serverPort))

			items, failures, err := ExpandBatchImportSources(context.Background(), []string{testCase.clashYAML(serverPort)})
			if err != nil || len(failures) != 0 || len(items) != 1 {
				t.Fatalf("import failed: items=%#v failures=%#v err=%v", items, failures, err)
			}
			testCase.assertImport(t, items[0].Config)

			clientPort := reserveLiveProtocolPort(t, "tcp")
			node := nativeTestNode(t, 900+index, items[0].Name, items[0].Type, items[0].Config)
			node.InboundPort = clientPort
			clientDir := t.TempDir()
			clientService := NewSingBoxService(clientDir)
			t.Setenv("SINGBOX_BINARY", binary)
			clientConfig, err := clientService.BuildGlobalConfig([]models.ProxyNode{node})
			if err != nil {
				t.Fatalf("build imported client config: %v", err)
			}
			if err := clientService.ValidateConfig(clientConfig); err != nil {
				t.Fatalf("validate imported client config: %v\n%s", err, clientConfig)
			}
			if err := clientService.ApplyConfig(clientConfig); err != nil {
				t.Fatalf("apply imported client config: %v", err)
			}
			if err := clientService.Start(); err != nil {
				t.Fatalf("start imported client config: %v", err)
			}
			t.Cleanup(func() {
				if err := clientService.Stop(); err != nil {
					t.Errorf("stop imported client: %v", err)
				}
			})

			proxyURL := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(clientPort))}
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
			t.Cleanup(transport.CloseIdleConnections)
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			deadline := time.Now().Add(8 * time.Second)
			var lastErr error
			for time.Now().Before(deadline) {
				response, requestErr := client.Get(target.URL + "/" + testCase.name)
				if requestErr == nil {
					body, readErr := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if readErr == nil && response.StatusCode == http.StatusOK && string(body) == responseBody {
						return
					}
					lastErr = fmt.Errorf("status=%d body=%q read_error=%v", response.StatusCode, body, readErr)
				} else {
					lastErr = requestErr
				}
				time.Sleep(100 * time.Millisecond)
			}

			clientLog, _ := os.ReadFile(filepath.Join(clientDir, "singbox.log"))
			serverLogContent, _ := os.ReadFile(serverLog)
			t.Fatalf("traffic did not traverse imported node: %v\nclient log:\n%s\nserver log:\n%s", lastErr, clientLog, serverLogContent)
		})
	}
}

func liveProtocolServerConfig(inbound map[string]any) map[string]any {
	return map[string]any{
		"log": map[string]any{"level": "warn"},
		"inbounds": []map[string]any{
			inbound,
		},
		"outbounds": []map[string]any{
			{"type": "direct", "tag": "direct"},
		},
		"route": map[string]any{"final": "direct"},
	}
}

func reserveLiveProtocolPort(t *testing.T, network string) int {
	t.Helper()
	address := "127.0.0.1:0"
	if network == "udp" {
		packet, err := net.ListenPacket("udp", address)
		if err != nil {
			t.Fatalf("reserve UDP port: %v", err)
		}
		port := packet.LocalAddr().(*net.UDPAddr).Port
		if err := packet.Close(); err != nil {
			t.Fatalf("release UDP port: %v", err)
		}
		return port
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("reserve TCP port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release TCP port: %v", err)
	}
	return port
}

func startLiveSingBoxProcess(t *testing.T, binary string, config map[string]any) string {
	t.Helper()
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.json")
	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal sing-box server config: %v", err)
	}
	if err := os.WriteFile(configPath, configJSON, 0o600); err != nil {
		t.Fatalf("write sing-box server config: %v", err)
	}
	if output, err := exec.Command(binary, "check", "-c", configPath).CombinedOutput(); err != nil {
		t.Fatalf("sing-box rejected server config: %v\n%s\n%s", err, output, configJSON)
	}

	logPath := filepath.Join(directory, "singbox.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open sing-box server log: %v", err)
	}
	command := exec.Command(binary, "run", "-c", configPath)
	configureSysProcAttr(command)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start sing-box server: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()

	select {
	case err := <-done:
		_ = logFile.Close()
		logContent, _ := os.ReadFile(logPath)
		t.Fatalf("sing-box server exited early: %v\n%s", err, logContent)
	case <-time.After(300 * time.Millisecond):
	}

	t.Cleanup(func() {
		if command.Process != nil {
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Errorf("kill sing-box server: %v", err)
			}
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("timed out waiting for sing-box server to stop")
		}
		if err := logFile.Close(); err != nil {
			t.Errorf("close sing-box server log: %v", err)
		}
	})

	return logPath
}

func writeLiveProtocolCertificate(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test TLS key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("create test TLS certificate: %v", err)
	}
	privateKeyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal test TLS key: %v", err)
	}

	directory := t.TempDir()
	certPath := filepath.Join(directory, "cert.pem")
	keyPath := filepath.Join(directory, "key.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privateKeyDER})
	if err := os.WriteFile(certPath, certificatePEM, 0o600); err != nil {
		t.Fatalf("write test TLS certificate: %v", err)
	}
	if err := os.WriteFile(keyPath, privateKeyPEM, 0o600); err != nil {
		t.Fatalf("write test TLS key: %v", err)
	}
	return certPath, keyPath
}
