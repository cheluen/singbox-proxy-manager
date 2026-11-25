package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"sb-proxy/backend/models"
)

func loopbackTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

func liveMember(t *testing.T, peerIP string, blockedHost ...string) (*httptest.Server, string) {
	t.Helper()
	dialer := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(peerIP)}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		if req.Method != "CONNECT" {
			http.Error(writer, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		host, port, err := net.SplitHostPort(req.Host)
		if err != nil {
			http.Error(writer, "bad target", 400)
			return
		}
		if len(blockedHost) != 0 && host == blockedHost[0] {
			http.Error(writer, "unavailable destination", http.StatusServiceUnavailable)
			return
		}
		remote, err := dialer.DialContext(req.Context(), "tcp", net.JoinHostPort("127.0.0.1", port))
		if err != nil {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
			return
		}
		defer remote.Close()
		conn, rw, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = rw.Flush()
		done := make(chan struct{})
		go func() { _, _ = io.Copy(remote, rw); _ = remote.(*net.TCPConn).CloseWrite(); close(done) }()
		_, _ = io.Copy(conn, remote)
		_ = conn.Close()
		_ = remote.Close()
		<-done
	}))
	t.Cleanup(server.Close)
	return server, fmt.Sprintf(`{"server":"127.0.0.1","server_port":%d}`, server.Listener.Addr().(*net.TCPAddr).Port)
}

func TestPolicyAndConnectionsRealKernel(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY is not set")
	}
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "100ms")
	service := NewSingBoxService(t.TempDir(), WithBinaryPath(binary))
	t.Cleanup(func() { _ = service.Stop() })
	primary, configA := liveMember(t, "127.0.0.2", "restricted.test")
	_, configB := liveMember(t, "127.0.0.3")
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		host, _, _ := net.SplitHostPort(req.RemoteAddr)
		writer.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(writer, `{"ip":%q,"country":"test"}`, host)
	}))
	t.Cleanup(origin.Close)
	target := origin.Listener.Addr().String()
	t.Setenv("SBPM_IPCHECK_URLS", origin.URL)
	requestPolicy := func(node models.ProxyNode, host string) string {
		t.Helper()
		targetURL := origin.URL
		if host != "" {
			_, port, _ := net.SplitHostPort(target)
			targetURL = "http://" + net.JoinHostPort(host, port)
		}
		u := &url.URL{Scheme: "http", Host: loopback(node.InboundPort), User: url.UserPassword(node.Username, node.Password)}
		client := http.Client{Timeout: 7 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(u), DisableKeepAlives: true}}
		response, err := client.Get(targetURL)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var payload struct {
			IP string `json:"ip"`
		}
		if err = json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatalf("proxy response %d: %v", response.StatusCode, err)
		}
		return payload.IP
	}
	policy := models.ProxyNode{ID: 3, Name: "priority", Type: "failover", Enabled: true, InboundPort: loopbackTestPort(t), Username: "user", Password: "ingress-key", TCPReuseEnabled: true, Config: fmt.Sprintf(`{"member_ids":[1,2],"health_check_interval":60,"health_check_url":%q}`, origin.URL)}
	nodes := []models.ProxyNode{{ID: 1, Name: "first", Type: "http", Enabled: true, Config: configA, GroupID: 2}, {ID: 2, Name: "second", Type: "http", Enabled: true, Config: configB, GroupID: 2}, policy}
	applier := NewRuntimeApplier(service)
	if err := applier.Apply(nodes); err != nil {
		t.Fatal(err)
	}
	if got := requestPolicy(policy, ""); got != "127.0.0.2" {
		t.Fatalf("primary source = %s", got)
	}
	plan, err := applier.Prepare(nodes[:2])
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := applier.Begin(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := requestPolicy(policy, ""); got != "127.0.0.2" {
		t.Fatalf("rolled back policy source = %s", got)
	}
	if err := service.Stop(); err != nil {
		t.Fatal(err)
	}
	if err := service.writeConfigFile([]byte(`{"inbounds":[{"type":"invalid-fixture"}]}`)); err != nil {
		t.Fatal(err)
	}
	service.operationMu.Lock()
	err = service.recoverProcessLocked()
	service.operationMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := requestPolicy(policy, "restricted.test"); got != "127.0.0.3" {
		t.Fatalf("fallback source = %s", got)
	}
	if got := requestPolicy(policy, ""); got != "127.0.0.2" {
		t.Fatalf("target-specific refusal removed the primary: %s", got)
	}
	ip, err := CheckProxyIPContext(t.Context(), loopback(policy.InboundPort), policy.Username, policy.Password)
	if err != nil || ip.IP != "127.0.0.2" {
		t.Fatalf("public policy IP = %v, %v", ip, err)
	}
	dispatch := service.features.dispatcher
	internal, err := proxy.SOCKS5("tcp", loopback(dispatch.wiring.scheduler), &proxy.Auth{User: fmt.Sprintf("policy-%d", policy.ID), Password: dispatch.wiring.policySecret}, &net.Dialer{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dead, err := internal.(proxy.ContextDialer).DialContext(t.Context(), "tcp", loopback(loopbackTestPort(t)))
	if err == nil {
		dead.Close()
		t.Fatal("closed destination accepted")
	}
	state := service.WithPolicyStatus(policy)
	if !state.Health[0].Healthy || !state.Health[1].Healthy || state.Active != 1 || requestPolicy(policy, "") != "127.0.0.2" {
		t.Fatal("single unreachable destination changed global policy health")
	}
	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		status, _ := strconv.Atoi(strings.TrimPrefix(req.URL.Path, "/"))
		w.WriteHeader(status)
	}))
	defer statusServer.Close()
	for _, status := range []int{204, 302, 401, 403, 404, 503} {
		err := dispatch.probe(t.Context(), 1, statusServer.URL+"/"+strconv.Itoa(status))
		if (err == nil) != (status >= 200 && status < 400) {
			t.Fatalf("probe status %d: %v", status, err)
		}
	}
	for _, host := range []string{"127.0.0.1", "::1", "localhost"} {
		if _, blocked := dispatch.validateTarget(t.Context(), net.JoinHostPort(host, strconv.Itoa(dispatch.wiring.manager))); !blocked {
			t.Fatal("manager port not protected")
		}
	}
	for _, member := range []int{1, 2} {
		dispatch.record(policy.ID, member, time.Now(), io.EOF, false)
	}
	if requestPolicy(policy, "") != "127.0.0.2" {
		t.Fatal("no recovery candidate after both members marked unhealthy")
	}
	dispatch.record(policy.ID, 2, time.Now(), nil, false)
	monitor := NewConnectionMonitor(service)
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); _, _ = io.Copy(conn, conn) }()
		}
	}()
	dialer, err := proxy.SOCKS5("tcp", loopback(policy.InboundPort), &proxy.Auth{User: policy.Username + "+" + strconv.Itoa(policy.InboundPort), Password: policy.Password}, &net.Dialer{Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	dial := func() net.Conn {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
		defer cancel()
		conn, err := dialer.(proxy.ContextDialer).DialContext(ctx, "tcp", echo.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	exchange := func(conn net.Conn, payload string) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.WriteString(conn, payload); err != nil {
			t.Fatal(err)
		}
		out := make([]byte, len(payload))
		if _, err := io.ReadFull(conn, out); err != nil || string(out) != payload {
			t.Fatalf("echo failed: %v", err)
		}
	}
	first, second := dial(), dial()
	exchange(first, "first-data")
	exchange(second, "second-data")
	updates, release := monitor.Subscribe()
	defer release()
	wait := func() ConnectionSnapshot {
		t.Helper()
		select {
		case snap := <-updates:
			return snap
		case <-time.After(4 * time.Second):
			t.Fatal("missing connection snapshot")
			return ConnectionSnapshot{}
		}
	}
	snap := wait()
	if snap.Up != nil || snap.Active != 2 || len(snap.Routes) != 1 || snap.Routes[0].MemberID != 1 || snap.Routes[0].RouteID != policy.Username+"+"+strconv.Itoa(policy.InboundPort) {
		t.Fatalf("unexpected first snapshot %+v", snap)
	}
	exchange(first, "new-data")
	next := wait()
	if next.Up == nil || *next.Up <= 0 || next.Epoch != snap.Epoch {
		t.Fatalf("rate %+v", next)
	}
	release()
	first.Close()
	second.Close()
	monitor.mu.Lock()
	idle := monitor.cancel == nil && len(monitor.subs) == 0
	monitor.mu.Unlock()
	if !idle {
		t.Fatal("monitor kept running without subscribers")
	}
	primary.Close()
	if got := requestPolicy(policy, ""); got != "127.0.0.3" {
		t.Fatalf("backup source = %s", got)
	}
	deadline := time.Now().Add(5 * time.Second)
	for service.WithPolicyStatus(policy).Health[0].Healthy && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if state := service.WithPolicyStatus(policy); state.Active != 2 || state.Health[0].Healthy {
		t.Fatalf("failover health %+v", state.PolicyStatus)
	}
	_, nodes[0].Config = liveMember(t, "127.0.0.2")
	nodes[2].Type = "loadbalance"
	for _, strategy := range []string{"round-robin", "consistent-hashing"} {
		nodes[2].Config = fmt.Sprintf(`{"member_ids":[1,2],"strategy":%q,"health_check_interval":60,"health_check_url":%q}`, strategy, origin.URL)
		if err := applier.Apply(nodes); err != nil {
			t.Fatal(err)
		}
		left, right := requestPolicy(policy, "Sample.test"), requestPolicy(policy, "sample.test")
		if (left == right) != (strategy == "consistent-hashing") {
			t.Fatalf("%s: %s %s", strategy, left, right)
		}
	}
	nodes[2].InboundPort = 0
	if err := applier.Apply(nodes); err != nil {
		t.Fatal(err)
	}
	if result, err := service.CheckPolicyIP(t.Context(), 3, CheckProxyIPContext); err != nil || !strings.HasPrefix(result.IP, "127.0.0.") {
		t.Fatalf("portless policy IP: %v %v", result, err)
	}
	nodes[0].Enabled = false
	nodes[1].Enabled = false
	if err := applier.Apply(nodes); err != nil {
		t.Fatal(err)
	}
	state = service.WithPolicyStatus(nodes[2])
	if len(state.Effective) != 0 || len(state.Warnings) == 0 {
		t.Fatalf("empty policy %+v", state.PolicyStatus)
	}
}

func TestPolicyControlAliases(t *testing.T) {
	dispatch := &policyDispatcher{wiring: &runtimeWiring{manager: 30000, controller: 30001, scheduler: 30002, dispatch: 30003, managerHosts: map[string]bool{"192.0.2.4": true}}}
	calls := 0
	dispatch.lookupNetIP = func(context.Context, string, string) ([]netip.Addr, error) { calls++; return nil, nil }
	if target, blocked := dispatch.validateTarget(t.Context(), "example.test:443"); blocked || target != "example.test:443" || calls != 0 {
		t.Fatal("ordinary destination performed a control lookup")
	}
	for _, ip := range []string{"127.0.0.2", "::", "::ffff:192.0.2.4", "192.0.2.9"} {
		dispatch.lookupNetIP = func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 2*time.Second || host != "alias.test" || network != "ip" {
				t.Fatal("invalid control lookup")
			}
			return []netip.Addr{netip.MustParseAddr("192.0.2.5"), netip.MustParseAddr(ip)}, nil
		}
		for port := 30000; port <= 30003; port++ {
			target, blocked := dispatch.validateTarget(t.Context(), net.JoinHostPort("alias.test", strconv.Itoa(port)))
			if blocked != (ip != "192.0.2.9") || blocked && target != "" || !blocked && target != net.JoinHostPort("192.0.2.5", strconv.Itoa(port)) {
				t.Fatalf("control DNS %s: target %q, blocked %t", ip, target, blocked)
			}
		}
	}
	if calls != 16 {
		t.Fatalf("unexpected lookups %d", calls)
	}
}
