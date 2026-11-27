package services

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"sb-proxy/backend/models"
)

const policyTestID = 1000

type policyTestAttempt struct {
	member int
	target string
}

func policyTestWiring(protocol string, members []int, healthURL string) *runtimeWiring {
	wiring := &runtimeWiring{
		nodes:        map[int]models.ProxyNode{policyTestID: {ID: policyTestID, Type: protocol, Enabled: true}},
		policies:     map[int]*models.PolicyConfig{policyTestID: {MemberIDs: members, HealthURL: healthURL, Interval: 3600}},
		policySecret: "policy-test-auth",
		memberSecret: "member-test-auth",
	}
	for _, member := range members {
		wiring.nodes[member] = models.ProxyNode{ID: member, Type: "direct", Enabled: true}
	}
	return wiring
}

func policyTestState(protocol string, members []int) *policyDispatcher {
	state := &scheduledPolicy{members: members, health: map[int]models.MemberHealth{}, probed: map[int]bool{}, initialChecked: map[int]bool{}}
	for _, member := range members {
		state.health[member] = models.MemberHealth{NodeID: member, Healthy: true}
	}
	return &policyDispatcher{wiring: policyTestWiring(protocol, members, ""), policies: map[int]*scheduledPolicy{policyTestID: state}}
}

func policyTestBaseline(dispatch *policyDispatcher, latencies map[int]int, active int) {
	dispatch.mu.Lock()
	defer dispatch.mu.Unlock()
	state := dispatch.policies[policyTestID]
	now := time.Now().UTC()
	for member, latency := range latencies {
		state.health[member] = models.MemberHealth{NodeID: member, Healthy: true, Latency: latency, CheckedAt: &now}
		state.probed[member] = true
		state.initialChecked[member] = true
	}
	state.initialProbed = len(state.initialChecked) == len(state.members)
	state.active = active
}

func waitPolicyTest(testContext *testing.T, message string, ready func() bool) {
	testContext.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ticker.C:
		case <-deadline.C:
			testContext.Fatal(message)
		}
	}
}

func policyTestProbesDone(dispatch *policyDispatcher) bool {
	dispatch.mu.Lock()
	defer dispatch.mu.Unlock()
	state := dispatch.policies[policyTestID]
	return state.initialProbed && len(state.probing) == 0
}

func servePolicyTestMember(ctx context.Context, conn net.Conn, wiring *runtimeWiring, attempts chan<- policyTestAttempt, dial func(context.Context, int, string) (net.Conn, error)) error {
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:2]); err != nil || head[0] != 5 {
		return fmt.Errorf("无效 SOCKS 握手：%v", err)
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if !slices.Contains(methods, byte(2)) {
		return fmt.Errorf("缺少成员认证方式")
	}
	if _, err := conn.Write([]byte{5, 2}); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, head[:1]); err != nil || head[0] != 1 {
		return fmt.Errorf("无效成员认证：%v", err)
	}
	username, err := readSOCKSString(conn)
	if err != nil {
		return err
	}
	authPassword, err := readSOCKSString(conn)
	if err != nil {
		return err
	}
	member, valid := tagID(username, "member-", "")
	if !valid || authPassword != wiring.memberSecret {
		return fmt.Errorf("成员认证失败")
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, head[:]); err != nil || head[0] != 5 || head[1] != 1 {
		return fmt.Errorf("无效成员连接请求：%v", err)
	}
	var host string
	switch head[3] {
	case 1, 4:
		size := 4
		if head[3] == 4 {
			size = 16
		}
		address := make(net.IP, size)
		if _, err := io.ReadFull(conn, address); err != nil {
			return err
		}
		host = address.String()
	case 3:
		host, err = readSOCKSString(conn)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("无效成员目标地址")
	}
	if _, err := io.ReadFull(conn, head[:2]); err != nil {
		return err
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(head[:2]))))
	select {
	case attempts <- policyTestAttempt{member: member, target: target}:
	case <-ctx.Done():
		return ctx.Err()
	}
	remote, err := dial(ctx, member, target)
	if err != nil {
		writeSOCKSReply(conn, 4)
		return err
	}
	defer remote.Close()
	_ = conn.SetDeadline(time.Time{})
	writeSOCKSReply(conn, 0)
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(remote, conn)
		_ = remote.Close()
		close(done)
	}()
	_, _ = io.Copy(conn, remote)
	_ = conn.Close()
	_ = remote.Close()
	<-done
	return nil
}

func policyTestDispatcher(testContext *testing.T, protocol string, members []int, healthURL string, dial func(context.Context, int, string) (net.Conn, error)) (*policyDispatcher, <-chan policyTestAttempt) {
	testContext.Helper()
	wiring := policyTestWiring(protocol, members, healthURL)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		testContext.Fatal(err)
	}
	wiring.dispatch = listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithCancel(testContext.Context())
	if dial == nil {
		dial = func(ctx context.Context, member int, target string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
		}
	}
	attempts := make(chan policyTestAttempt, 128)
	var workers sync.WaitGroup
	var socketsMutex sync.Mutex
	sockets := map[net.Conn]bool{}
	workers.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			socketsMutex.Lock()
			if ctx.Err() != nil {
				socketsMutex.Unlock()
				_ = conn.Close()
				return
			}
			sockets[conn] = true
			socketsMutex.Unlock()
			workers.Go(func() {
				defer func() { _ = conn.Close(); socketsMutex.Lock(); delete(sockets, conn); socketsMutex.Unlock() }()
				_ = servePolicyTestMember(ctx, conn, wiring, attempts, dial)
			})
		}
	})
	testContext.Cleanup(func() {
		cancel()
		_ = listener.Close()
		socketsMutex.Lock()
		for conn := range sockets {
			_ = conn.Close()
		}
		socketsMutex.Unlock()
		workers.Wait()
	})
	dispatch, err := startPolicyDispatcher(wiring, nil)
	if err != nil {
		testContext.Fatal(err)
	}
	testContext.Cleanup(dispatch.close)
	dispatch.mu.Lock()
	dispatch.policies[policyTestID].due = time.Now().Add(time.Hour)
	dispatch.mu.Unlock()
	return dispatch, attempts
}

func dialPolicyTest(dispatch *policyDispatcher, target string) (net.Conn, error) {
	client, err := proxy.SOCKS5("tcp", dispatch.listener.Addr().String(), &proxy.Auth{User: fmt.Sprintf("policy-%d", policyTestID), Password: dispatch.wiring.policySecret}, &net.Dialer{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(dispatch.ctx, 2*time.Second)
	defer cancel()
	return client.(proxy.ContextDialer).DialContext(ctx, "tcp", target)
}

func TestURLTestCandidatesByBaselineLatency(testContext *testing.T) {
	dispatch := policyTestState("urltest", []int{5, 2, 6, 1, 4, 3, 7})
	policyTestBaseline(dispatch, map[int]int{5: 0, 2: 35, 1: 35, 4: 1}, 0)
	state := dispatch.policies[policyTestID]
	state.health[4] = models.MemberHealth{NodeID: 4, Healthy: false, Latency: 1}
	state.health[3] = models.MemberHealth{NodeID: 3, Healthy: false}
	state.health[6] = models.MemberHealth{NodeID: 6, Healthy: true, Latency: 500}
	for _, active := range []int{0, 5, 6, 4, 99} {
		state.active = active
		if got := dispatch.candidates(policyTestID, "example.test:443"); !slices.Equal(got, []int{5, 2, 1, 6, 7, 4, 3}) || state.cursor != 0 {
			testContext.Fatalf("基准延迟、未探测成员或兜底顺序错误：active=%d，%v", active, got)
		}
	}
}

func TestURLTestToleranceBoundaries(testContext *testing.T) {
	zero := 0
	custom := 20
	for _, testCase := range []struct {
		name      string
		tolerance *int
		latency   int
		want      []int
	}{
		{name: "默认容差内", latency: 149, want: []int{1, 2, 3}},
		{name: "默认容差相等", latency: 150, want: []int{1, 2, 3}},
		{name: "默认容差超出", latency: 151, want: []int{2, 3, 1}},
		{name: "显式零相等", tolerance: &zero, latency: 100, want: []int{1, 2, 3}},
		{name: "显式零切换", tolerance: &zero, latency: 101, want: []int{2, 1, 3}},
		{name: "自定义相等", tolerance: &custom, latency: 120, want: []int{1, 2, 3}},
		{name: "自定义超出", tolerance: &custom, latency: 121, want: []int{2, 1, 3}},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			dispatch := policyTestState("urltest", []int{2, 3, 1})
			dispatch.wiring.policies[policyTestID].Tolerance = testCase.tolerance
			policyTestBaseline(dispatch, map[int]int{1: testCase.latency, 2: 100, 3: 125}, 1)
			if got := dispatch.candidates(policyTestID, "example.test:443"); !slices.Equal(got, testCase.want) {
				testContext.Fatalf("容差边界选路错误：%v，期望 %v", got, testCase.want)
			}
		})
	}
}

func TestURLTestRecordSeparatesBusinessAndProbeHealth(testContext *testing.T) {
	dispatch := policyTestState("urltest", []int{1, 2, 3})
	state := dispatch.policies[policyTestID]
	dispatch.record(policyTestID, 1, time.Now().Add(-10*time.Millisecond), nil, false)
	baseline := state.health[1]
	if !state.probed[1] || state.initialProbed {
		testContext.Fatal("首轮探测在全部成员完成前被标记完成")
	}
	dispatch.record(policyTestID, 1, time.Now(), nil, true)
	if state.active != 1 || state.health[1] != baseline {
		testContext.Fatal("业务成功覆盖了基准延迟或探测时间")
	}
	dispatch.record(policyTestID, 3, time.Now(), nil, true)
	if state.probed[3] || state.health[3].CheckedAt != nil || state.initialProbed {
		testContext.Fatal("业务拨号被当成统一基准探测")
	}
	dispatch.record(policyTestID, 2, time.Now().Add(-20*time.Millisecond), nil, false)
	dispatch.record(policyTestID, 3, time.Now(), nil, false)
	if !state.initialProbed {
		testContext.Fatal("首轮基准探测完成后未更新状态")
	}
	dispatch.record(policyTestID, 1, baseline.CheckedAt.Add(-time.Second), io.EOF, false)
	if state.health[1] != baseline || !state.probed[1] {
		testContext.Fatal("过期探测覆盖了较新的基准结果")
	}
	dispatch.record(policyTestID, 1, time.Now(), io.EOF, false)
	if state.health[1].Healthy || state.probed[1] || !state.initialProbed {
		testContext.Fatal("失败探测未清除无效基准或丢失首轮完成状态")
	}
}

func TestPolicyInitialProbeCompletionIncludesFailures(testContext *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance", "urltest"} {
		testContext.Run(protocol, func(testContext *testing.T) {
			dispatch := policyTestState(protocol, []int{1, 2})
			state := dispatch.policies[policyTestID]
			dispatch.record(policyTestID, 1, time.Now(), io.EOF, false)
			if state.initialProbed || state.probed[1] || !state.initialChecked[1] {
				testContext.Fatal("失败探测被当成有效基准或未记录首轮执行状态")
			}
			dispatch.record(policyTestID, 2, time.Now(), nil, false)
			if !state.initialProbed || !state.initialChecked[2] || state.probed[1] || !state.probed[2] {
				testContext.Fatal("死成员阻止首轮探测完成或污染有效基准标记")
			}
		})
	}
}

func TestPolicyRecordPreservesLegacyStaleProbeProtection(testContext *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance"} {
		testContext.Run(protocol, func(testContext *testing.T) {
			dispatch := policyTestState(protocol, []int{1})
			state := dispatch.policies[policyTestID]
			businessStarted := time.Now().Add(-5 * time.Millisecond)
			probeStarted := businessStarted.Add(-20 * time.Millisecond)
			dispatch.record(policyTestID, 1, businessStarted, nil, true)
			businessHealth := state.health[1]
			if state.initialProbed {
				testContext.Fatal("业务成功被计入首轮探测完成状态")
			}
			dispatch.record(policyTestID, 1, probeStarted, io.EOF, false)
			if !state.health[1].Healthy || state.active != 1 || state.health[1] != businessHealth || businessHealth.CheckedAt == nil || businessHealth.Latency < 5 {
				testContext.Fatalf("旧探测失败覆盖业务成功或清除主用：health=%+v，active=%d", state.health[1], state.active)
			}
			if !state.initialProbed {
				testContext.Fatal("被忽略的过期探测未计入首轮完成状态")
			}
			dispatch.record(policyTestID, 1, time.Now(), io.EOF, false)
			if state.health[1].Healthy || state.health[1].LastError == "" || state.active != 0 {
				testContext.Fatal("新的失败探测未更新原策略健康状态")
			}
		})
	}
}

func TestURLTestRecoveryRejectsFailedLatencyAndProbesUrgently(testContext *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	testContext.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var requests atomic.Int32
	health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		select {
		case <-release:
		case <-request.Context().Done():
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	testContext.Cleanup(health.Close)
	dispatch, _ := policyTestDispatcher(testContext, "urltest", []int{1, 2}, health.URL, nil)
	policyTestBaseline(dispatch, map[int]int{1: 300, 2: 100}, 2)
	probeStarted := time.Now()
	time.Sleep(time.Millisecond)
	dispatch.record(policyTestID, 1, probeStarted, io.EOF, false)
	failed := dispatch.status(policyTestID).Health[0]
	dispatch.mu.Lock()
	failedBaseline := dispatch.policies[policyTestID].probed[1]
	dispatch.mu.Unlock()
	if failed.Healthy || failed.Latency >= 100 || failedBaseline {
		testContext.Fatalf("快速失败探测仍被当成有效基准：%+v，probed=%t", failed, failedBaseline)
	}
	dispatch.record(policyTestID, 1, time.Now(), nil, true)
	waitPolicyTest(testContext, "业务恢复未触发紧急基准探测", func() bool { return requests.Load() == 1 })
	dispatch.mu.Lock()
	state := dispatch.policies[policyTestID]
	recovered := state.health[1]
	valid := state.probed[1]
	active := state.active
	dispatch.mu.Unlock()
	if !recovered.Healthy || recovered.LastError != "" || active != 1 || valid || recovered.Latency != failed.Latency || recovered.CheckedAt != failed.CheckedAt {
		testContext.Fatalf("业务恢复未清理无效基准或污染探测记录：%+v，probed=%t", recovered, valid)
	}
	if got := dispatch.candidates(policyTestID, "example.test:443"); !slices.Equal(got, []int{2, 1}) {
		testContext.Fatalf("恢复成员利用失败耗时抢占已测健康成员：%v", got)
	}
	releaseOnce.Do(func() { close(release) })
	waitPolicyTest(testContext, "恢复后的紧急基准探测未完成", func() bool { return policyTestProbesDone(dispatch) })
	dispatch.mu.Lock()
	refreshed := state.health[1]
	valid = state.probed[1] && !state.lastUrgent[1].IsZero()
	dispatch.mu.Unlock()
	if !valid || !refreshed.Healthy || !refreshed.CheckedAt.After(*failed.CheckedAt) || requests.Load() != 1 {
		testContext.Fatalf("紧急探测未生成有效基准或重复触发：%+v，次数=%d", refreshed, requests.Load())
	}
}

func TestPolicyCandidatesPreserveLegacyStrategies(testContext *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance"} {
		dispatch := policyTestState(protocol, []int{1, 2, 3})
		policyTestBaseline(dispatch, map[int]int{1: 500, 2: 10, 3: 0}, 2)
		dispatch.policies[policyTestID].health[3] = models.MemberHealth{NodeID: 3}
		if got := dispatch.candidates(policyTestID, "Example.test:443"); !slices.Equal(got, []int{1, 2, 3}) {
			testContext.Fatalf("%s 的既有首轮顺序改变：%v", protocol, got)
		}
		want := []int{1, 2, 3}
		if protocol == "loadbalance" {
			want = []int{2, 1, 3}
		}
		if got := dispatch.candidates(policyTestID, "Example.test:443"); !slices.Equal(got, want) {
			testContext.Fatalf("%s 的轮询行为改变：%v", protocol, got)
		}
		if protocol == "loadbalance" {
			dispatch.wiring.policies[policyTestID].Strategy = "consistent-hashing"
			first := dispatch.candidates(policyTestID, "Example.test:443")
			if second := dispatch.candidates(policyTestID, "example.test.:80"); !slices.Equal(first, second) || first[2] != 3 {
				testContext.Fatalf("一致性哈希或不健康兜底行为改变：%v，%v", first, second)
			}
		}
	}
}

func TestURLTestSlowBusinessDialDoesNotPolluteBaseline(testContext *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	testContext.Cleanup(health.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { _, _ = io.WriteString(writer, "ok") }))
	testContext.Cleanup(origin.Close)
	healthTarget, businessTarget := health.Listener.Addr().String(), origin.Listener.Addr().String()
	dispatch, attempts := policyTestDispatcher(testContext, "urltest", []int{1, 2}, health.URL, func(ctx context.Context, member int, target string) (net.Conn, error) {
		delay := 10 * time.Millisecond
		if target == healthTarget && member == 2 {
			delay = 80 * time.Millisecond
		}
		if target == businessTarget && member == 1 {
			delay = 200 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
	})
	for _, member := range []int{1, 2} {
		dispatch.scheduleProbe(policyTestID, member, false)
	}
	waitPolicyTest(testContext, "基准探测未完成", func() bool { return policyTestProbesDone(dispatch) })
	baseline := dispatch.status(policyTestID)
	if baseline.Health[0].Latency >= baseline.Health[1].Latency {
		testContext.Fatalf("模拟基准探测未体现延迟差异：%+v", baseline.Health)
	}
	for range 2 {
		conn, err := dialPolicyTest(dispatch, businessTarget)
		if err != nil {
			testContext.Fatal(err)
		}
		_ = conn.Close()
		current := dispatch.status(policyTestID)
		if current.Active != 1 || current.Health[0] != baseline.Health[0] || current.Health[1] != baseline.Health[1] {
			testContext.Fatalf("慢业务站点污染基准或改变最快成员：%+v", current)
		}
	}
	var selected []int
	for len(attempts) > 0 {
		attempt := <-attempts
		if attempt.target == businessTarget {
			selected = append(selected, attempt.member)
		}
	}
	if !slices.Equal(selected, []int{1, 1}) {
		testContext.Fatalf("业务站点耗时污染后续选路：%v", selected)
	}
}

func TestURLTestPrimaryFailureUsesNextFastestAndUrgentProbe(testContext *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	testContext.Cleanup(health.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	testContext.Cleanup(origin.Close)
	target := origin.Listener.Addr().String()
	dispatch, attempts := policyTestDispatcher(testContext, "urltest", []int{1, 2, 3}, health.URL, func(ctx context.Context, member int, target string) (net.Conn, error) {
		if member == 1 {
			return nil, fmt.Errorf("主用成员已失效")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
	})
	policyTestBaseline(dispatch, map[int]int{1: 10, 2: 90, 3: 30}, 1)
	started := time.Now()
	conn, err := dialPolicyTest(dispatch, target)
	if err != nil {
		testContext.Fatal(err)
	}
	defer conn.Close()
	if time.Since(started) >= time.Second || dispatch.status(policyTestID).Active != 3 {
		testContext.Fatal("主用失败未立即顺延到次快成员")
	}
	waitPolicyTest(testContext, "主用失效未触发紧急基准探测", func() bool {
		dispatch.mu.Lock()
		defer dispatch.mu.Unlock()
		state := dispatch.policies[policyTestID]
		return !state.health[1].Healthy && !state.lastUrgent[1].IsZero() && !state.probing[1]
	})
	var selected []int
	for len(attempts) > 0 {
		attempt := <-attempts
		if attempt.target == target {
			selected = append(selected, attempt.member)
		}
	}
	if !slices.Equal(selected, []int{1, 3}) || !slices.Equal(dispatch.candidates(policyTestID, target), []int{3, 2, 1}) {
		testContext.Fatalf("失败顺延未按基准延迟排序：%v", selected)
	}
}

func TestPolicyAllDialFailuresOnlyURLTestProbesUrgently(testContext *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance", "urltest"} {
		testContext.Run(protocol, func(testContext *testing.T) {
			dispatch, _ := policyTestDispatcher(testContext, protocol, []int{1, 2}, "http://127.0.0.1:12345/health", func(context.Context, int, string) (net.Conn, error) {
				return nil, fmt.Errorf("成员不可用")
			})
			policyTestBaseline(dispatch, map[int]int{1: 10, 2: 30}, 1)
			before := dispatch.status(policyTestID)
			conn, err := dialPolicyTest(dispatch, "127.0.0.1:12346")
			if err == nil {
				_ = conn.Close()
				testContext.Fatal("全部成员失效仍返回连接成功")
			}
			if protocol == "urltest" {
				waitPolicyTest(testContext, "全部失败未紧急探测所有成员", func() bool {
					dispatch.mu.Lock()
					defer dispatch.mu.Unlock()
					state := dispatch.policies[policyTestID]
					return !state.health[1].Healthy && !state.health[2].Healthy && len(state.lastUrgent) == 2 && len(state.probing) == 0
				})
				if dispatch.status(policyTestID).Active != 0 {
					testContext.Fatal("失效主用未被基准探测清除")
				}
			} else {
				after := dispatch.status(policyTestID)
				dispatch.mu.Lock()
				urgent := len(dispatch.policies[policyTestID].lastUrgent)
				dispatch.mu.Unlock()
				if urgent != 0 || after.Active != before.Active || !slices.Equal(after.Health, before.Health) {
					testContext.Fatal("原策略全部拨号失败的健康边界改变")
				}
			}
		})
	}
}

func TestPolicyIdleWindow(testContext *testing.T) {
	dispatch := &policyDispatcher{}
	for _, testCase := range []struct {
		interval int
		want     time.Duration
	}{{10, 5 * time.Minute}, {30, 5 * time.Minute}, {100, 5 * time.Minute}, {101, 303 * time.Second}, {3600, 3 * time.Hour}} {
		if got := dispatch.idleWindow(testCase.interval); got != testCase.want {
			testContext.Fatalf("空闲阈值错误：interval=%d，%s", testCase.interval, got)
		}
	}
	dispatch.idlePauseAfter = 25 * time.Millisecond
	if dispatch.idleWindow(3600) != 25*time.Millisecond {
		testContext.Fatal("内部空闲阈值覆写未生效")
	}
}

func TestPolicyIdlePauseAndTrafficWake(testContext *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance", "urltest"} {
		testContext.Run(protocol, func(testContext *testing.T) {
			health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
			testContext.Cleanup(health.Close)
			origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
			testContext.Cleanup(origin.Close)
			var blockHealth atomic.Bool
			release := make(chan struct{})
			var releaseOnce sync.Once
			testContext.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
			dispatch, _ := policyTestDispatcher(testContext, protocol, []int{1, 2}, health.URL, func(ctx context.Context, member int, target string) (net.Conn, error) {
				if target == health.Listener.Addr().String() && blockHealth.Load() {
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
			})
			dispatch.mu.Lock()
			dispatch.idlePauseAfter = 50 * time.Millisecond
			state := dispatch.policies[policyTestID]
			state.lastUsed = time.Now().Add(-time.Second)
			state.due = time.Now().Add(-time.Millisecond)
			dispatch.mu.Unlock()
			dispatch.probeDue(policyTestID, time.Now())
			waitPolicyTest(testContext, "空闲启动跳过了首轮基准探测", func() bool { return policyTestProbesDone(dispatch) })
			baseline := dispatch.status(policyTestID)
			dispatch.mu.Lock()
			state.due = time.Now().Add(-time.Millisecond)
			dispatch.mu.Unlock()
			dispatch.probeDue(policyTestID, time.Now())
			dispatch.mu.Lock()
			paused := state.paused && len(state.probing) == 0
			dispatch.mu.Unlock()
			if !paused || !slices.Equal(dispatch.status(policyTestID).Health, baseline.Health) {
				testContext.Fatal("首轮完成后的空闲策略未懒暂停")
			}
			blockHealth.Store(true)
			started := time.Now()
			conn, err := dialPolicyTest(dispatch, origin.Listener.Addr().String())
			if err != nil {
				testContext.Fatal(err)
			}
			defer conn.Close()
			dispatch.mu.Lock()
			awake := !state.paused && state.activeConns == 1 && !state.lastUsed.Before(started) && state.due.After(started) && len(state.probing) == 2
			dispatch.mu.Unlock()
			if !awake || time.Since(started) >= time.Second {
				testContext.Fatal("新流量未解除暂停、更新使用状态或被刷新探测阻塞")
			}
			now := time.Now().Add(time.Second)
			dispatch.mu.Lock()
			state.due = now.Add(-time.Millisecond)
			dispatch.mu.Unlock()
			dispatch.probeDue(policyTestID, now)
			dispatch.mu.Lock()
			paused = state.paused
			dispatch.mu.Unlock()
			if paused {
				testContext.Fatal("仍有活跃连接时进入空闲暂停")
			}
			releaseOnce.Do(func() { close(release) })
			waitPolicyTest(testContext, "唤醒后的刷新探测未完成", func() bool { return policyTestProbesDone(dispatch) })
			if current := dispatch.status(policyTestID); !current.Health[0].CheckedAt.After(*baseline.Health[0].CheckedAt) || !current.Health[1].CheckedAt.After(*baseline.Health[1].CheckedAt) {
				testContext.Fatal("唤醒后未刷新已到期的基准探测")
			}
			_ = conn.Close()
			waitPolicyTest(testContext, "连接结束未释放活跃计数", func() bool {
				dispatch.mu.Lock()
				defer dispatch.mu.Unlock()
				return state.activeConns == 0
			})
			now = time.Now().Add(time.Second)
			dispatch.mu.Lock()
			state.due = now.Add(-time.Millisecond)
			dispatch.mu.Unlock()
			dispatch.probeDue(policyTestID, now)
			dispatch.mu.Lock()
			paused = state.paused
			dispatch.mu.Unlock()
			if !paused {
				testContext.Fatal("流量结束后的空闲策略未再次暂停")
			}
		})
	}
}

func TestPolicyWakeDoesNotProbeBeforeDue(testContext *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	testContext.Cleanup(origin.Close)
	dispatch, _ := policyTestDispatcher(testContext, "urltest", []int{1}, origin.URL, nil)
	policyTestBaseline(dispatch, map[int]int{1: 25}, 1)
	baseline := dispatch.status(policyTestID).Health[0]
	dispatch.mu.Lock()
	state := dispatch.policies[policyTestID]
	state.paused = true
	dispatch.mu.Unlock()
	conn, err := dialPolicyTest(dispatch, origin.Listener.Addr().String())
	if err != nil {
		testContext.Fatal(err)
	}
	defer conn.Close()
	dispatch.mu.Lock()
	awake := !state.paused && state.activeConns == 1 && len(state.probing) == 0
	dispatch.mu.Unlock()
	if !awake || dispatch.status(policyTestID).Health[0] != baseline {
		testContext.Fatal("未到期的暂停策略唤醒时产生多余探测")
	}
}

func TestPolicyProbeLoopSchedulesAsynchronously(testContext *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	testContext.Cleanup(health.Close)
	release := make(chan struct{})
	var releaseOnce sync.Once
	testContext.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	dispatch, _ := policyTestDispatcher(testContext, "urltest", []int{1, 2}, health.URL, func(ctx context.Context, member int, target string) (net.Conn, error) {
		if member == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
	})
	dispatch.mu.Lock()
	dispatch.policies[policyTestID].due = time.Now().Add(-time.Second)
	dispatch.mu.Unlock()
	waitPolicyTest(testContext, "周期探测被慢成员同步堵塞", func() bool {
		dispatch.mu.Lock()
		defer dispatch.mu.Unlock()
		state := dispatch.policies[policyTestID]
		return state.probed[2] && !state.probed[1] && !state.initialProbed
	})
	releaseOnce.Do(func() { close(release) })
	waitPolicyTest(testContext, "首轮异步探测未全部完成", func() bool { return policyTestProbesDone(dispatch) })
}

func TestPolicyProbeConcurrencyLimitAndDeduplication(testContext *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
	testContext.Cleanup(health.Close)
	members := make([]int, 16)
	for index := range members {
		members[index] = index + 1
	}
	const delay = 80 * time.Millisecond
	var callsMutex sync.Mutex
	calls := map[int]int{}
	running, maximum := 0, 0
	dispatch, _ := policyTestDispatcher(testContext, "urltest", members, health.URL, func(ctx context.Context, member int, target string) (net.Conn, error) {
		callsMutex.Lock()
		calls[member]++
		running++
		maximum = max(maximum, running)
		callsMutex.Unlock()
		defer func() { callsMutex.Lock(); running--; callsMutex.Unlock() }()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", target)
	})
	if cap(dispatch.probeSlots) != 8 {
		testContext.Fatalf("全局探测并发容量错误：%d", cap(dispatch.probeSlots))
	}
	started := time.Now()
	for _, member := range members {
		dispatch.scheduleProbe(policyTestID, member, false)
		dispatch.scheduleProbe(policyTestID, member, true)
		dispatch.scheduleProbe(policyTestID, member, false)
	}
	if time.Since(started) >= delay {
		testContext.Fatal("非紧急探测仍同步阻塞调度")
	}
	waitPolicyTest(testContext, "受控并发探测未完成", func() bool { return policyTestProbesDone(dispatch) })
	elapsed := time.Since(started)
	callsMutex.Lock()
	defer callsMutex.Unlock()
	if maximum != 8 || elapsed < 2*delay || elapsed >= time.Second {
		testContext.Fatalf("并发限流或耗时错误：最大并发 %d，16 成员耗时 %s", maximum, elapsed)
	}
	for _, member := range members {
		if calls[member] != 1 {
			testContext.Fatalf("成员 %d 并发重探：%d 次", member, calls[member])
		}
	}
}

func TestURLTestRealKernelEndToEnd(testContext *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		testContext.Skip("未设置 SINGBOX_TEST_BINARY，跳过真实 sing-box 内核验证")
	}
	testContext.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "100ms")
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host, _, _ := net.SplitHostPort(request.RemoteAddr)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"ip":%q}`, host)
	}))
	testContext.Cleanup(origin.Close)
	health := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host, _, _ := net.SplitHostPort(request.RemoteAddr)
		if host == "127.0.0.2" {
			time.Sleep(120 * time.Millisecond)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	testContext.Cleanup(health.Close)
	_, firstConfig := liveMember(testContext, "127.0.0.2")
	fastMember, _ := liveMember(testContext, "127.0.0.3")
	fast := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Host == origin.Listener.Addr().String() {
			time.Sleep(200 * time.Millisecond)
		}
		fastMember.Config.Handler.ServeHTTP(writer, request)
	}))
	testContext.Cleanup(fast.Close)
	secondConfig := fmt.Sprintf(`{"server":"127.0.0.1","server_port":%d}`, fast.Listener.Addr().(*net.TCPAddr).Port)
	policy := models.ProxyNode{ID: policyTestID, Name: "最低延迟", Type: "urltest", Enabled: true, InboundPort: loopbackTestPort(testContext), Username: "urltest-user", Password: "urltest-auth", TCPReuseEnabled: true, Config: fmt.Sprintf(`{"member_ids":[1,2],"tolerance":0,"health_check_interval":60,"health_check_url":%q}`, health.URL)}
	service := NewSingBoxService(testContext.TempDir(), WithBinaryPath(binary))
	testContext.Cleanup(func() { _ = service.Stop() })
	nodes := []models.ProxyNode{{ID: 1, Name: "较慢成员", Type: "http", Enabled: true, Config: firstConfig}, {ID: 2, Name: "较快成员", Type: "http", Enabled: true, Config: secondConfig}, policy}
	if err := NewRuntimeApplier(service).Apply(nodes); err != nil {
		testContext.Fatal(err)
	}
	dispatch := service.features.dispatcher
	waitPolicyTest(testContext, "真实内核首轮基准探测未完成", func() bool { return policyTestProbesDone(dispatch) })
	baseline := service.WithPolicyStatus(policy)
	if !baseline.Health[0].Healthy || !baseline.Health[1].Healthy || baseline.Health[0].Latency <= baseline.Health[1].Latency {
		testContext.Fatalf("真实内核基准延迟错误：%+v", baseline.PolicyStatus)
	}
	request := func() string {
		testContext.Helper()
		proxyURL := &url.URL{Scheme: "http", Host: loopback(policy.InboundPort), User: url.UserPassword(policy.Username, policy.Password)}
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 7 * time.Second}
		response, err := client.Get(origin.URL)
		if err != nil {
			testContext.Fatal(err)
		}
		defer response.Body.Close()
		var result struct {
			IP string `json:"ip"`
		}
		if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
			testContext.Fatal(err)
		}
		return result.IP
	}
	for range 2 {
		if got := request(); got != "127.0.0.3" {
			testContext.Fatalf("真实内核未选择最低基准延迟：%s", got)
		}
		if current := service.WithPolicyStatus(policy); current.Active != 2 || !slices.Equal(current.Health, baseline.Health) {
			testContext.Fatalf("真实慢业务拨号污染基准延迟：%+v", current.PolicyStatus)
		}
	}
	fast.Close()
	if got := request(); got != "127.0.0.2" || service.WithPolicyStatus(policy).Active != 1 {
		testContext.Fatalf("真实主用失败未顺延：%s", got)
	}
	waitPolicyTest(testContext, "真实主用失败未触发紧急探测", func() bool {
		return !service.WithPolicyStatus(policy).Health[1].Healthy
	})
}
