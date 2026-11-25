package services

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sb-proxy/backend/models"
)

func TestCheckProxyIPPreservesHTTPAuthenticationCredentials(t *testing.T) {
	for _, credentials := range [][2]string{{" user name ", " p a s s "}, {"user@%/", "p+@:%/?#"}, {"", ""}} {
		t.Run(credentials[0], func(t *testing.T) {
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				want := ""
				if credentials[0] != "" {
					want = "Basic " + base64.StdEncoding.EncodeToString([]byte(credentials[0]+":"+credentials[1]))
				}
				if r.Header.Get("Proxy-Authorization") != want {
					w.WriteHeader(http.StatusProxyAuthRequired)
					return
				}
				_, _ = w.Write([]byte(`{"ip":"203.0.113.7","country":"Test","cc":"TT"}`))
			}))
			t.Cleanup(proxy.Close)
			t.Setenv("SBPM_IPCHECK_URLS", "http://check.example/ip")
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			info, err := CheckProxyIPContext(ctx, strings.TrimPrefix(proxy.URL, "http://"), credentials[0], credentials[1])
			if err != nil || info == nil || info.Transport != "http" || info.HTTPError != "" || info.IP != "203.0.113.7" {
				t.Fatalf("HTTP proxy authentication failed: info=%+v err=%v", info, err)
			}
		})
	}
}

func TestCheckProxyIPWithModeHTTPSLoopback(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		t.Run(host, func(t *testing.T) {
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("test-user:test-key"))
				if request.Header.Get("Proxy-Authorization") != wantAuth {
					writer.WriteHeader(http.StatusProxyAuthRequired)
					return
				}
				_, _ = writer.Write([]byte(`{"ip":"203.0.113.7","country":"Test","cc":"TT"}`))
			}))
			if host == "::1" {
				listener, err := net.Listen("tcp6", "[::1]:0")
				if err != nil {
					server.Close()
					t.Skip("IPv6 回环不可用")
				}
				_ = server.Listener.Close()
				server.Listener = listener
			}
			server.StartTLS()
			t.Cleanup(server.Close)
			t.Setenv("SBPM_IPCHECK_URLS", "http://check.example/ip")
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			address := net.JoinHostPort(host, strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port))
			info, err := CheckProxyIPWithModeContext(ctx, address, "test-user", "test-key", models.InboundModeHTTPS)
			if err != nil || info == nil || info.Transport != "https" || info.HTTPError != "" || info.IP != "203.0.113.7" {
				t.Fatalf("HTTPS 回环代理检测失败：%+v，%v", info, err)
			}
		})
	}
}

func TestCheckProxyIPWithModeHTTPSDoesNotFallBackToSOCKS5(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Setenv("SBPM_IPCHECK_URLS", "http://check.example/ip")
	info, err := CheckProxyIPWithModeContext(t.Context(), server.Listener.Addr().String(), "", "", models.InboundModeHTTPS)
	if err == nil || info != nil || !strings.Contains(err.Error(), "HTTPS") || strings.Contains(err.Error(), "SOCKS5") || connections.Load() != 2 {
		t.Fatalf("HTTPS 失败发生 SOCKS5 回退：结果 %+v、错误 %v、连接数 %d", info, err, connections.Load())
	}
}

func TestInboundProxyTLSVerificationOnlyBypassesLocalHop(t *testing.T) {
	certificatePEM, _, _ := testInboundTLSPEM(t, testInboundTLSTemplate("untrusted.example"))
	block, _ := pem.Decode(certificatePEM)
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		host, serverName string
		allowed          bool
	}{
		{"localhost", "localhost", true}, {"127.0.0.1", "", true}, {"::1", "", true},
		{"remote.example", "remote.example", false}, {"localhost", "remote.example", false},
		{"127.0.0.1", "remote.example", false}, {"127.0.0.2", "", false},
	} {
		config := inboundProxyTLSConfig(test.host)
		err := config.VerifyConnection(tls.ConnectionState{ServerName: test.serverName, PeerCertificates: []*x509.Certificate{certificate}})
		if (err == nil) != test.allowed {
			t.Fatalf("代理 %q、TLS 名称 %q 的证书验证结果错误：%v", test.host, test.serverName, err)
		}
	}
}

func TestHTTPSProxyStillVerifiesDestinationCertificate(t *testing.T) {
	var originRequests atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		originRequests.Add(1)
		_, _ = writer.Write([]byte(`{"ip":"203.0.113.7"}`))
	}))
	t.Cleanup(origin.Close)
	proxyServer := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_, port, err := net.SplitHostPort(request.Host)
		if err != nil {
			writer.WriteHeader(http.StatusBadRequest)
			return
		}
		dialer := net.Dialer{Timeout: time.Second}
		remote, err := dialer.DialContext(request.Context(), "tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		defer remote.Close()
		connection, buffered, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, buffered); close(done) }()
		_, _ = io.Copy(connection, remote)
		_ = connection.Close()
		_ = remote.Close()
		<-done
	}))
	t.Cleanup(proxyServer.Close)
	t.Setenv("SBPM_IPCHECK_URLS", strings.Replace(origin.URL, "127.0.0.1", "localhost", 1))
	address := net.JoinHostPort("localhost", strconv.Itoa(proxyServer.Listener.Addr().(*net.TCPAddr).Port))
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	info, err := CheckProxyIPWithModeContext(ctx, address, "", "", models.InboundModeHTTPS)
	if err == nil || info != nil || originRequests.Load() != 0 || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("目标站点的证书验证被代理跳点豁免：%+v，%v，请求数 %d", info, err, originRequests.Load())
	}
}

func TestCheckWithServiceReturnsTypedRateLimitError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	_, err := checkWithService(context.Background(), server.Client(), server.URL)
	var statusErr *IPCheckHTTPStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("status error lost its type: %T %v", err, err)
	}
	if statusErr.StatusCode != http.StatusTooManyRequests || statusErr.RetryAfter != "17" || !IsIPCheckRateLimited(err) {
		t.Fatalf("unexpected rate-limit details: %+v", statusErr)
	}
	if !strings.Contains(err.Error(), "rate limited") || !strings.Contains(err.Error(), "HTTP 429") {
		t.Fatalf("rate-limit message is not actionable: %v", err)
	}
}

func TestDefaultIPCheckServicesKeepIPAPIOutOfInitialRace(t *testing.T) {
	t.Setenv("SBPM_IPCHECK_URLS", "")
	services := ipCheckServiceURLs()
	if len(services) < ipCheckServicesMaxConcurrency {
		t.Fatalf("default service list is too short: %d", len(services))
	}
	for _, service := range services[:ipCheckServicesMaxConcurrency] {
		if strings.Contains(service, "ip-api.com") {
			t.Fatalf("rate-limited fallback service was started in the initial race: %q", service)
		}
	}
}

func TestCheckWithServicesRacingPreservesRateLimitOverLaterGenericErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/limited" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	_, err := checkWithServicesRacing(context.Background(), server.Client(), []string{
		server.URL + "/limited",
		server.URL + "/generic",
	})
	if !IsIPCheckRateLimited(err) {
		t.Fatalf("a later generic failure hid the rate-limit cause: %v", err)
	}
}

func TestIPCheckRequestsUseGlobalConcurrencyBound(t *testing.T) {
	globalSlots := make(chan struct{}, 2)

	var mu sync.Mutex
	active := 0
	maximum := 0
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		mu.Lock()
		active--
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ip":"203.0.113.7","country":"Test","cc":"TT"}`))
	}))
	t.Cleanup(server.Close)

	done := make(chan error, 4)
	for index := 0; index < 4; index++ {
		go func() {
			_, err := checkWithServicesRacingLimited(
				context.Background(),
				server.Client(),
				[]string{server.URL},
				globalSlots,
			)
			done <- err
		}()
	}
	for count := 0; count < 2; count++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("global IP-check slots did not start expected requests")
		}
	}
	select {
	case <-started:
		close(release)
		t.Fatal("more requests than the global bound reached external services")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for count := 0; count < 4; count++ {
		if err := <-done; err != nil {
			t.Fatalf("bounded IP check failed: %v", err)
		}
	}
	mu.Lock()
	observedMaximum := maximum
	mu.Unlock()
	if observedMaximum != 2 {
		t.Fatalf("unexpected maximum external request concurrency: %d", observedMaximum)
	}
}

func TestCheckWithService_SendsHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "sb-proxy-manager/1.0" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"1.2.3.4","country":"Testland","cc":"TL","city":"Test City","region":"Test Region"}`))
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{}
	info, err := checkWithService(context.Background(), client, srv.URL)
	if err != nil {
		t.Fatalf("checkWithService: %v", err)
	}
	if info.IP != "1.2.3.4" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
}

func TestRunIPCheckAttemptGivesFallbackAFreshTimeout(t *testing.T) {
	firstStarted := time.Now()
	_, firstErr := runIPCheckAttempt(context.Background(), 30*time.Millisecond, func(ctx context.Context) (*IPInfo, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if firstErr == nil || time.Since(firstStarted) < 20*time.Millisecond {
		t.Fatalf("first attempt did not consume its own timeout: %v", firstErr)
	}

	info, err := runIPCheckAttempt(context.Background(), 30*time.Millisecond, func(ctx context.Context) (*IPInfo, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 20*time.Millisecond {
			return nil, fmt.Errorf("fallback context has no fresh timeout budget")
		}
		return &IPInfo{IP: "203.0.113.10"}, nil
	})
	if err != nil || info == nil || info.IP == "" {
		t.Fatalf("fallback did not receive a fresh timeout: info=%+v err=%v", info, err)
	}
}

func TestCheckWithService_ParsesCountryCodeCC(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip":"1.2.3.4","country":"Testland","cc":"TL"}`))
	}))
	t.Cleanup(srv.Close)

	client := &http.Client{}
	info, err := checkWithService(context.Background(), client, srv.URL)
	if err != nil {
		t.Fatalf("checkWithService: %v", err)
	}
	if info.CountryCode != "TL" {
		t.Fatalf("unexpected country code: %q", info.CountryCode)
	}
}

func TestCheckProxyIPContextUsesEnvServicesWithFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fail":
			w.WriteHeader(http.StatusInternalServerError)
		case "/ok":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"1.2.3.4","country":"Testland","cc":"TL"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("SBPM_IPCHECK_URLS", srv.URL+"/fail,"+srv.URL+"/ok")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	info, err := CheckProxyIPContext(ctx, strings.TrimPrefix(srv.URL, "http://"), "", "")
	if err != nil {
		t.Fatalf("CheckProxyIPContext: %v", err)
	}
	if info.IP != "1.2.3.4" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
}

func TestCheckWithServicesRacing_PrefersGeoWhenArrivesSoon(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip-only":
			time.Sleep(50 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"1.1.1.1"}`))
		case "/geo":
			time.Sleep(200 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"2.2.2.2","country":"Testland","cc":"TL"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	startedAt := time.Now()
	info, err := checkWithServicesRacing(ctx, &http.Client{}, []string{srv.URL + "/ip-only", srv.URL + "/geo"})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("checkWithServicesRacing: %v", err)
	}
	if info.IP != "2.2.2.2" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
	if info.CountryCode != "TL" {
		t.Fatalf("unexpected country code: %q", info.CountryCode)
	}
	if info.Latency <= 0 {
		t.Fatalf("expected positive latency, got %d", info.Latency)
	}
	if elapsed > 900*time.Millisecond {
		t.Fatalf("expected fast return, took %v", elapsed)
	}
}

func TestCheckWithServicesRacing_ReturnsIPOnlyAfterGeoWaitWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ip-only":
			time.Sleep(80 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"1.1.1.1"}`))
		case "/geo":
			select {
			case <-time.After(900 * time.Millisecond):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ip":"2.2.2.2","country":"Testland","cc":"TL"}`))
			case <-r.Context().Done():
				return
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	startedAt := time.Now()
	info, err := checkWithServicesRacing(ctx, &http.Client{}, []string{srv.URL + "/ip-only", srv.URL + "/geo"})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("checkWithServicesRacing: %v", err)
	}
	if info.IP != "1.1.1.1" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
	if info.Latency <= 0 {
		t.Fatalf("expected positive latency, got %d", info.Latency)
	}
	elapsedMs := elapsed.Milliseconds()
	if elapsed < 550*time.Millisecond {
		t.Fatalf("expected return around geo wait window, took %v", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected return within context deadline, took %v", elapsed)
	}
	if elapsedMs-int64(info.Latency) < 300 {
		t.Fatalf("expected latency to reflect winning request only, got latency=%dms elapsed=%dms", info.Latency, elapsedMs)
	}
}

func TestCheckWithServicesRacing_NotBlockedByFirstSlowService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/slow":
			select {
			case <-time.After(5 * time.Second):
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"ip":"9.9.9.9"}`))
			case <-r.Context().Done():
				return
			}
		case "/geo":
			time.Sleep(100 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ip":"2.2.2.2","country":"Testland","cc":"TL"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)

	startedAt := time.Now()
	info, err := checkWithServicesRacing(ctx, &http.Client{}, []string{srv.URL + "/slow", srv.URL + "/geo"})
	elapsed := time.Since(startedAt)
	if err != nil {
		t.Fatalf("checkWithServicesRacing: %v", err)
	}
	if info.IP != "2.2.2.2" {
		t.Fatalf("unexpected ip: %q", info.IP)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("expected fast return even with slow first service, took %v", elapsed)
	}
}
