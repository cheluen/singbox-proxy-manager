package services

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"sb-proxy/backend/models"
)

func TestRealUpstreamCheckRejectsUnauthenticatedAndNonLoopbackClients(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY not set")
	}
	t.Setenv("SINGBOX_BINARY", binary)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "isolated-check-ok")
	}))
	defer target.Close()
	upstreamPort := reserveLiveProtocolPort(t, "tcp")
	startLiveSingBoxProcess(t, binary, liveProtocolServerConfig(map[string]any{
		"type": "mixed", "tag": "upstream", "listen": "127.0.0.1", "listen_port": upstreamPort,
	}))
	service := NewSingBoxService(t.TempDir())
	var checkedAddress string
	_, err := service.checkUpstreamIPContext(context.Background(), models.ProxyDefinition{
		Type: "socks5", Config: fmt.Sprintf(`{"server":"127.0.0.1","server_port":%d}`, upstreamPort),
	}, func(ctx context.Context, address, username, password string) (*IPInfo, error) {
		checkedAddress = address
		deadline := time.Now().Add(3 * time.Second)
		for {
			connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
			if err == nil {
				_ = connection.Close()
				break
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("isolated listener did not start: %w", err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		request := func(scheme, user, pass string) (int, string, error) {
			proxyURL := &url.URL{Scheme: scheme, Host: address}
			if user != "" || pass != "" {
				proxyURL.User = url.UserPassword(user, pass)
			}
			transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
			response, err := client.Do(req)
			if err != nil {
				return 0, "", err
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			return response.StatusCode, string(body), err
		}
		for _, scheme := range []string{"http", "socks5"} {
			status, body, err := request(scheme, username, password)
			if err != nil || status != http.StatusOK || body != "isolated-check-ok" {
				return nil, fmt.Errorf("authenticated %s check failed: status=%d err=%v", scheme, status, err)
			}
			for _, credentials := range [][2]string{{"", ""}, {username, "incorrect-one-time-password"}} {
				status, _, err := request(scheme, credentials[0], credentials[1])
				if err == nil && status != http.StatusProxyAuthRequired {
					t.Errorf("%s forwarded without correct authentication: status=%d", scheme, status)
				}
			}
		}
		_, port, _ := net.SplitHostPort(address)
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			return nil, err
		}
		for _, address := range addresses {
			network, ok := address.(*net.IPNet)
			if !ok || network.IP.IsLoopback() || !network.IP.IsGlobalUnicast() {
				continue
			}
			connection, err := net.DialTimeout("tcp", net.JoinHostPort(network.IP.String(), port), 300*time.Millisecond)
			if err == nil {
				_ = connection.Close()
				t.Error("isolated listener is reachable through a non-loopback interface")
			}
		}
		// SOCKS4 无法携带密码，认证入口也必须拒绝它。
		connection, err := net.DialTimeout("tcp", address, time.Second)
		if err != nil {
			return nil, err
		}
		defer connection.Close()
		_ = connection.SetDeadline(time.Now().Add(time.Second))
		_, _ = connection.Write([]byte{4, 1, 0, 80, 127, 0, 0, 1, 0})
		var reply [8]byte
		if _, err := io.ReadFull(connection, reply[:]); err == nil && reply[1] == 90 {
			t.Error("SOCKS4 bypassed one-time authentication")
		}
		return &IPInfo{IP: "198.51.100.42"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("tcp", checkedAddress, 200*time.Millisecond)
	if err == nil {
		_ = connection.Close()
		t.Fatal("isolated listener remained after the check")
	}
}
