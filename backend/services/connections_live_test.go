package services

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"sb-proxy/backend/models"
)

func TestConnectionRatesIncludeNewConnectionsAfterBaseline(t *testing.T) {
	wiring := &runtimeWiring{nodes: map[int]models.ProxyNode{1: {ID: 1, Type: "direct", InboundPort: 30001}}}
	var monitor ConnectionMonitor
	existing := clashConnection{ID: "existing", Upload: 600, Download: 900}
	existing.Metadata.Type = "mixed/node-1-in"
	existing.Metadata.SourceIP = "192.0.2.1"
	baseline := ConnectionSnapshot{At: time.Unix(60, 0)}
	monitor.counters, _ = monitor.aggregate(&baseline, []clashConnection{existing}, wiring, nil)
	monitor.last = baseline
	if len(baseline.Routes) != 1 || baseline.Up != nil || baseline.Down != nil || baseline.Routes[0].UploadRate != nil || baseline.Routes[0].DownloadRate != nil {
		t.Fatalf("expected null rates on first frame, got %+v", baseline)
	}

	existing.Upload += 20
	existing.Download += 40
	fresh := existing
	fresh.ID, fresh.Upload, fresh.Download = "new", 80, 160
	next := ConnectionSnapshot{At: baseline.At.Add(2 * time.Second)}
	monitor.aggregate(&next, []clashConnection{existing, fresh}, wiring, nil)
	if len(next.Routes) != 1 || next.Routes[0].Count != 2 || next.Up == nil || next.Down == nil {
		t.Fatalf("expected one route with two connections and total rates, got %+v", next)
	}
	route := next.Routes[0]
	if route.UploadRate == nil || route.DownloadRate == nil {
		t.Fatal("missing route rates on second frame")
	}
	if *route.UploadRate != 50 || *route.DownloadRate != 100 || *next.Up != 50 || *next.Down != 100 {
		t.Fatalf("upload/download B/s: route %g/%g, total %g/%g, want 50/100 for both", *route.UploadRate, *route.DownloadRate, *next.Up, *next.Down)
	}
}

func TestConnectionsRealKernelChainedRoutes(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY is not set")
	}
	_, memberConfig := liveMember(t, "127.0.0.2")
	hold := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "ready")
		writer.(http.Flusher).Flush()
		select {
		case <-hold:
		case <-request.Context().Done():
		}
	}))
	defer origin.Close()
	defer close(hold)
	service := NewSingBoxService(t.TempDir(), WithBinaryPath(binary))
	defer service.Stop()
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "80ms")
	base := models.ProxyNode{ID: 1, Name: "public entry", Enabled: true, Type: "direct", Config: "{}", Username: "entry", Password: "entry-key", InboundPort: loopbackTestPort(t), TCPReuseEnabled: true}
	custom := models.ProxyNode{ID: 2, Name: "custom route", Enabled: true, Type: "direct", Config: "{}", Username: "route", Password: "route-key", InboundPort: loopbackTestPort(t), TCPReuseEnabled: true, UpstreamMode: "custom", UpstreamType: "http", UpstreamConfig: memberConfig}
	global := custom
	global.ID = 3
	global.Name = "global route"
	global.Username = "global"
	global.InboundPort = loopbackTestPort(t)
	global.UpstreamMode = "global"
	global.UpstreamConfig = ""
	global.UpstreamType = ""
	legacy := custom
	legacy.ID = 4
	legacy.Name = "legacy route"
	legacy.Username = "legacy"
	legacy.InboundPort = loopbackTestPort(t)
	legacy.UpstreamMode = "legacy"
	legacy.Config = `{"detour":"relay"}`
	legacy.UpstreamConfig = ""
	legacy.UpstreamType = ""
	nodes := []models.ProxyNode{base, custom, global, legacy, {ID: 5, Name: "relay", Type: "http", Enabled: true, Config: memberConfig}}
	settings := models.Settings{GlobalUpstreamEnabled: true, GlobalUpstreamType: "http", GlobalUpstreamConfig: memberConfig}
	if err := NewRuntimeApplier(service).Apply(nodes, settings); err != nil {
		t.Fatal(err)
	}
	if err := waitForTCPReady(t.Context(), loopback(base.InboundPort), 3*time.Second); err != nil {
		t.Fatalf("public proxy listener did not become ready: %v", err)
	}
	for _, node := range []models.ProxyNode{custom, global, legacy} {
		routeID := node.Username + "+" + strconv.Itoa(node.InboundPort)
		proxyURL := &url.URL{Scheme: "http", Host: loopback(base.InboundPort), User: url.UserPassword(routeID, node.Password)}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
		client := http.Client{Transport: transport, Timeout: 20 * time.Second}
		response, err := client.Get(origin.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var ready [5]byte
		if _, err = io.ReadFull(response.Body, ready[:]); err != nil || string(ready[:]) != "ready" {
			t.Fatalf("route %d stream: %v", node.ID, err)
		}
	}
	monitor := NewConnectionMonitor(service)
	snap, err := monitor.Capture(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	labels := map[int]string{2: "custom: http", 3: "global: http", 4: "relay"}
	if snap.Active != 3 || len(snap.Routes) != 3 || snap.Up != nil {
		t.Fatalf("unexpected routed snapshot %+v", snap)
	}
	for _, route := range snap.Routes {
		if route.InboundID != base.ID || route.InboundPort != base.InboundPort || labels[route.OutboundID] != route.Upstream || route.RouteID == "" || route.OutboundName != nodes[route.OutboundID-1].Name || route.UpstreamSource != nodes[route.OutboundID-1].UpstreamMode {
			t.Fatalf("lost routed business node %+v", route)
		}
	}
	time.Sleep(120 * time.Millisecond)
	next, err := monitor.Capture(t.Context())
	if err != nil || next.Up == nil || monitor.cancel != nil {
		t.Fatalf("polling did not reuse the previous counters: %v", err)
	}
	deadline := time.Now().Add(pollingStateTTL + 2*time.Second)
	for {
		monitor.sampleMu.Lock()
		released := monitor.counters == nil && monitor.idleTimer == nil
		monitor.sampleMu.Unlock()
		if released {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle polling state was retained")
		}
		time.Sleep(25 * time.Millisecond)
	}
	var collected []routeDelta
	monitor.SetSink(func(_ time.Time, deltas []routeDelta) { collected = append(collected, deltas...) })
	defer monitor.SetSink(nil)
	if _, err = monitor.Capture(t.Context()); err != nil {
		t.Fatal(err)
	}
	monitor.sampleMu.Lock()
	count := len(collected)
	monitor.sampleMu.Unlock()
	if count != 0 {
		t.Fatal("history from before persistence was enabled was collected")
	}
	proxyURL := &url.URL{Scheme: "http", Host: loopback(base.InboundPort), User: url.UserPassword(base.Username, base.Password)}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}}
	res, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if _, err = io.ReadFull(res.Body, make([]byte, 5)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Millisecond)
	if _, err = monitor.Capture(t.Context()); err != nil {
		t.Fatal(err)
	}
	monitor.sampleMu.Lock()
	var newBytes int64
	for _, delta := range collected {
		if delta.route.OutboundID == base.ID && delta.opened {
			newBytes += delta.down
		}
	}
	monitor.sampleMu.Unlock()
	if newBytes == 0 {
		t.Fatal("new connection bytes missing after persistence baseline")
	}
	controller, err := http.Get("http://" + loopback(service.features.wiring.controller) + "/connections")
	if err != nil {
		t.Fatal(err)
	}
	controller.Body.Close()
	if controller.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unprotected controller returned %d", controller.StatusCode)
	}
}

func TestConnectionMemberAmbiguity(t *testing.T) {
	wiring, err := newWiring([]models.ProxyNode{{ID: 3, Name: "policy", Type: "loadbalance", Enabled: true, Config: `{"member_ids":[1,2]}`, InboundPort: 35003}}, models.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &policyDispatcher{wiring: wiring, sessions: map[string]policySession{}}
	var raw []clashConnection
	for index, member := range []int{1, 2} {
		outer := clashConnection{ID: fmt.Sprintf("outer-%d", index)}
		outer.Metadata.Type = "mixed/node-3-in"
		outer.Metadata.DestinationIP = "127.0.0.1"
		outer.Metadata.DestinationPort = "443"
		outer.Metadata.SourceIP = "192.0.2.1"
		inner := outer
		inner.ID = fmt.Sprintf("inner-%d", index)
		inner.Metadata.Type = "socks/" + dispatchTag
		inner.Metadata.SourceIP = "127.0.0.1"
		inner.Metadata.SourcePort = strconv.Itoa(44000 + index)
		dispatcher.sessions["127.0.0.1:"+inner.Metadata.SourcePort] = policySession{Policy: 3, Member: member}
		raw = append(raw, outer, inner)
	}
	monitor := NewConnectionMonitor(NewSingBoxService(t.TempDir()))
	snapshot := ConnectionSnapshot{At: time.Now()}
	monitor.aggregate(&snapshot, raw, wiring, dispatcher)
	if len(snapshot.Routes) != 1 || snapshot.Active != 2 || snapshot.Routes[0].MemberID != 0 {
		t.Fatalf("ambiguous clients were assigned a member: %+v", snapshot)
	}
}

func TestConnectionMemberForPinnedControlPort(t *testing.T) {
	policy := models.ProxyNode{ID: 3, Type: "failover", Enabled: true, InboundPort: 33003}
	wiring := &runtimeWiring{nodes: map[int]models.ProxyNode{3: policy}}
	outer := clashConnection{}
	outer.Metadata.Type = "mixed/node-3-in"
	outer.Metadata.Host = "target.test"
	outer.Metadata.DestinationPort = "30000"
	inner := clashConnection{}
	inner.Metadata.Type = "socks/" + dispatchTag
	inner.Metadata.SourceIP, inner.Metadata.SourcePort = "127.0.0.1", "44000"
	inner.Metadata.DestinationIP, inner.Metadata.DestinationPort = "192.0.2.5", "30000"
	dispatch := &policyDispatcher{wiring: wiring, sessions: map[string]policySession{
		"127.0.0.1:44000": {Policy: 3, Member: 2, Target: "Target.Test.:30000", DialTarget: "192.0.2.5:30000"},
	}}
	key := "3/target.test:30000"
	if dispatch.matchedMembers([]clashConnection{outer, inner})[key] != 2 {
		t.Fatal("missing member for pinned domain")
	}
	inner.Metadata.DestinationIP = "192.0.2.7"
	if len(dispatch.matchedMembers([]clashConnection{outer, inner})) != 0 {
		t.Fatal("mismatched inner connection claimed the business route")
	}
}

func TestConnectionStreamFailureAndRecovery(t *testing.T) {
	var healthy atomic.Bool
	var samples atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		samples.Add(1)
		if !healthy.Load() {
			writer.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(writer, `{"connections":[]}`)
	}))
	defer server.Close()
	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	defer self.Release()
	service := NewSingBoxService(t.TempDir())
	service.process = &exec.Cmd{Process: self}
	service.features.wiring = &runtimeWiring{controller: server.Listener.Addr().(*net.TCPAddr).Port}
	monitor := NewConnectionMonitor(service)
	updates, cancel := monitor.Subscribe()
	defer cancel()
	select {
	case event := <-updates:
		if event.StreamError == "" || samples.Load() != 3 {
			t.Fatalf("failure after %d samples: %+v", samples.Load(), event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing stream failure")
	}
	late, leave := monitor.Subscribe()
	defer leave()
	if event := <-late; event.StreamError == "" {
		t.Fatal("new subscriber got stale snapshot")
	}
	select {
	case <-updates:
		t.Fatal("duplicate error frame")
	case <-time.After(1300 * time.Millisecond):
	}
	healthy.Store(true)
	select {
	case event := <-updates:
		if event.StreamError != "" || event.At.IsZero() || event.Routes == nil {
			t.Fatal("missing recovery snapshot")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not recover")
	}
}
