package services

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"sb-proxy/backend/models"
)

type policySession struct {
	Policy, Member int
	Target         string
	DialTarget     string
	Started        time.Time
}
type scheduledPolicy struct {
	members    []int
	health     map[int]models.MemberHealth
	cursor     uint64
	active     int
	due        time.Time
	probing    map[int]bool
	lastUrgent map[int]time.Time
}
type policyDispatcher struct {
	wiring      *runtimeWiring
	logEvent    func(LogEvent)
	listener    net.Listener
	ctx         context.Context
	cancel      context.CancelFunc
	mu          sync.Mutex
	policies    map[int]*scheduledPolicy
	sockets     map[net.Conn]bool
	sessions    map[string]policySession
	wg          sync.WaitGroup
	probeSlots  chan struct{}
	lookupNetIP func(context.Context, string, string) ([]netip.Addr, error)
}

func startPolicyDispatcher(w *runtimeWiring, logEvent func(LogEvent)) (*policyDispatcher, error) {
	listener, err := net.Listen("tcp4", loopback(w.scheduler))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &policyDispatcher{wiring: w, logEvent: logEvent, listener: listener, ctx: ctx, cancel: cancel, policies: map[int]*scheduledPolicy{}, sockets: map[net.Conn]bool{}, sessions: map[string]policySession{}}
	d.probeSlots = make(chan struct{}, 4)
	d.lookupNetIP = net.DefaultResolver.LookupNetIP
	for id, cfg := range w.policies {
		state := &scheduledPolicy{members: []int{}, health: map[int]models.MemberHealth{}, due: time.Now().Add(time.Second)}
		state.probing = make(map[int]bool)
		state.lastUrgent = make(map[int]time.Time)
		for _, member := range cfg.MemberIDs {
			node, ok := w.nodes[member]
			if ok && node.Enabled && !models.IsPolicy(node.Type) {
				state.members = append(state.members, member)
				state.health[member] = models.MemberHealth{NodeID: member, Healthy: true}
			}
		}
		d.policies[id] = state
	}
	d.wg.Go(d.accept)
	d.wg.Go(d.probeLoop)
	return d, nil
}

func (d *policyDispatcher) close() {
	d.cancel()
	_ = d.listener.Close()
	d.mu.Lock()
	for conn := range d.sockets {
		_ = conn.Close()
	}
	d.mu.Unlock()
	d.wg.Wait()
}

func (d *policyDispatcher) accept() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		if d.ctx.Err() != nil {
			d.mu.Unlock()
			_ = conn.Close()
			return
		}
		d.sockets[conn] = true
		d.mu.Unlock()
		d.wg.Go(func() {
			defer func() { _ = conn.Close(); d.mu.Lock(); delete(d.sockets, conn); d.mu.Unlock() }()
			d.serve(conn)
		})
	}
}

func readSOCKSString(reader io.Reader) (string, error) {
	var length [1]byte
	if _, err := io.ReadFull(reader, length[:]); err != nil {
		return "", err
	}
	value := make([]byte, int(length[0]))
	_, err := io.ReadFull(reader, value)
	return string(value), err
}

func (d *policyDispatcher) handshake(conn net.Conn) (int, string, error) {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:2]); err != nil || head[0] != 5 {
		return 0, "", fmt.Errorf("invalid SOCKS version")
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return 0, "", err
	}
	if !slices.Contains(methods, byte(2)) {
		_, _ = conn.Write([]byte{5, 255})
		return 0, "", fmt.Errorf("authentication required")
	}
	if _, err := conn.Write([]byte{5, 2}); err != nil {
		return 0, "", err
	}
	if _, err := io.ReadFull(conn, head[:1]); err != nil || head[0] != 1 {
		return 0, "", fmt.Errorf("invalid authentication frame")
	}
	user, err := readSOCKSString(conn)
	if err != nil {
		return 0, "", err
	}
	password, err := readSOCKSString(conn)
	if err != nil {
		return 0, "", err
	}
	id, valid := tagID(user, "policy-", "")
	if !valid || d.policies[id] == nil || subtle.ConstantTimeCompare([]byte(password), []byte(d.wiring.policySecret)) != 1 {
		_, _ = conn.Write([]byte{1, 1})
		return 0, "", fmt.Errorf("authentication failed")
	}
	if _, err = conn.Write([]byte{1, 0}); err != nil {
		return 0, "", err
	}
	if _, err = io.ReadFull(conn, head[:]); err != nil {
		return 0, "", err
	}
	if head[0] != 5 || head[1] != 1 || head[2] != 0 {
		writeSOCKSReply(conn, 7)
		return 0, "", fmt.Errorf("only CONNECT is supported")
	}
	var host string
	switch head[3] {
	case 1, 4:
		size := 4
		if head[3] == 4 {
			size = 16
		}
		addr := make(net.IP, size)
		if _, err = io.ReadFull(conn, addr); err != nil {
			return 0, "", err
		}
		host = addr.String()
	case 3:
		host, err = readSOCKSString(conn)
		if err != nil {
			return 0, "", err
		}
	default:
		writeSOCKSReply(conn, 8)
		return 0, "", fmt.Errorf("invalid address type")
	}
	if _, err = io.ReadFull(conn, head[:2]); err != nil {
		return 0, "", err
	}
	port := binary.BigEndian.Uint16(head[:2])
	if host == "" || port == 0 {
		return 0, "", fmt.Errorf("invalid target")
	}
	return id, net.JoinHostPort(host, strconv.Itoa(int(port))), nil
}

func tagID(tag, prefix, suffix string) (int, bool) {
	value, ok := strings.CutPrefix(tag, prefix)
	if !ok {
		return 0, false
	}
	value, ok = strings.CutSuffix(value, suffix)
	id, err := strconv.Atoi(value)
	return id, ok && err == nil && id > 0
}
func writeSOCKSReply(conn net.Conn, status byte) {
	_, _ = conn.Write([]byte{5, status, 0, 1, 0, 0, 0, 0, 0, 0})
}
func normalizedTarget(target string) string {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		host = target
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap().String()
	}
	return strings.ToLower(strings.TrimSuffix(host, "."))
}

func (d *policyDispatcher) candidates(id int, target string) []int {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.policies[id]
	var ids, fallback []int
	for _, member := range state.members {
		if state.health[member].Healthy {
			ids = append(ids, member)
		} else {
			fallback = append(fallback, member)
		}
	}
	if len(ids) < 2 || d.wiring.nodes[id].Type == "failover" {
		return append(ids, fallback...)
	}
	if d.wiring.policies[id].Strategy == "consistent-hashing" {
		host := normalizedTarget(target)
		score := func(member int) uint64 {
			sum := sha256.Sum256([]byte(host + "\x00" + strconv.Itoa(member)))
			return binary.BigEndian.Uint64(sum[:8])
		}
		slices.SortFunc(ids, func(left, right int) int {
			a, b := score(left), score(right)
			if a > b {
				return -1
			}
			if a < b {
				return 1
			}
			return 0
		})
		return append(ids, fallback...)
	}
	start := int(state.cursor % uint64(len(ids)))
	state.cursor++
	ordered := append(ids[start:len(ids):len(ids)], ids[:start]...)
	return append(ordered, fallback...)
}

type dispatchDialer struct {
	net.Dialer
	socket net.Conn
}

func (dialer *dispatchDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := dialer.Dialer.DialContext(ctx, network, address)
	dialer.socket = conn
	return conn, err
}

func (d *policyDispatcher) dial(ctx context.Context, member int, target string) (net.Conn, error) {
	forward := &dispatchDialer{Dialer: net.Dialer{Timeout: 5 * time.Second}}
	client, err := proxy.SOCKS5("tcp", loopback(d.wiring.dispatch), &proxy.Auth{User: fmt.Sprintf("member-%d", member), Password: d.wiring.memberSecret}, forward)
	if err != nil {
		return nil, err
	}
	if _, err = client.(proxy.ContextDialer).DialContext(ctx, "tcp", target); err != nil {
		return nil, err
	}
	return forward.socket, nil
}

func (d *policyDispatcher) record(id, member int, start time.Time, err error, active bool) {
	now := time.Now().UTC()
	h := models.MemberHealth{NodeID: member, Healthy: err == nil, Latency: int(time.Since(start).Milliseconds()), CheckedAt: &now}
	if err != nil {
		h.LastError = "member connection failed"
	}
	d.mu.Lock()
	state := d.policies[id]
	if active && err == nil {
		state.active = member
	}
	old := state.health[member]
	if old.CheckedAt != nil && old.CheckedAt.After(start) {
		d.mu.Unlock()
		return
	}
	state.health[member] = h
	if err != nil && state.active == member {
		state.active = 0
	}
	d.mu.Unlock()
}

func (d *policyDispatcher) serve(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	id, target, err := d.handshake(conn)
	if err != nil {
		return
	}
	dialTarget, blocked := d.validateTarget(d.ctx, target)
	if blocked {
		writeSOCKSReply(conn, 2)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	var failures []int
	for _, member := range d.candidates(id, target) {
		start := time.Now()
		ctx, cancel := context.WithTimeout(d.ctx, 5*time.Second)
		remote, err := d.dial(ctx, member, dialTarget)
		cancel()
		if err != nil {
			failures = append(failures, member)
			continue
		}
		for _, failed := range failures {
			d.scheduleProbe(id, failed, true)
		}
		d.record(id, member, start, nil, true)
		key := remote.LocalAddr().String()
		d.mu.Lock()
		d.sessions[key] = policySession{Policy: id, Member: member, Target: target, DialTarget: dialTarget, Started: start}
		d.mu.Unlock()
		d.logSelection(id, member)
		defer func() { _ = remote.Close(); d.mu.Lock(); delete(d.sessions, key); d.mu.Unlock() }()
		stop := context.AfterFunc(d.ctx, func() { _ = remote.Close() })
		defer stop()
		writeSOCKSReply(conn, 0)
		transfer := func(dst, src net.Conn) {
			if _, err := io.Copy(dst, src); err != nil {
				_ = dst.Close()
				_ = src.Close()
				return
			}
			if half, ok := dst.(interface{ CloseWrite() error }); ok {
				_ = half.CloseWrite()
			}
		}
		upDone := make(chan struct{})
		go func() { defer close(upDone); transfer(remote, conn) }()
		transfer(conn, remote)
		<-upDone
		return
	}
	d.logSelection(id, 0)
	writeSOCKSReply(conn, 4)
}

func (d *policyDispatcher) logSelection(policyID, memberID int) {
	if d.logEvent == nil {
		return
	}
	name := d.wiring.nodes[policyID].Name
	event := LogEvent{TS: time.Now().UTC(), Source: "panel", Level: "INFO", Message: "Policy selected a member", Policy: name, Outbound: name}
	if memberID > 0 {
		event.Member = d.wiring.nodes[memberID].Name
	} else {
		event.Level = "WARN"
		event.Message = "No healthy member could connect"
	}
	d.logEvent(event)
}

func (d *policyDispatcher) status(id int) *models.PolicyStatus {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.policies[id]
	if state == nil {
		return nil
	}
	out := &models.PolicyStatus{Effective: append([]int{}, state.members...), Active: state.active, Health: []models.MemberHealth{}, Warnings: []string{}}
	healthy := 0
	for _, member := range state.members {
		health := state.health[member]
		out.Health = append(out.Health, health)
		if health.Healthy {
			healthy++
		}
	}
	if healthy == 0 {
		out.Warnings = append(out.Warnings, "no healthy enabled members")
	}
	if len(state.members) < len(d.wiring.policies[id].MemberIDs) {
		out.Warnings = append(out.Warnings, "disabled members are skipped")
	}
	return out
}

func (d *policyDispatcher) probeLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-d.ctx.Done():
			return
		case now := <-ticker.C:
			for id, state := range d.policies {
				if now.Before(state.due) {
					continue
				}
				cfg := d.wiring.policies[id]
				state.due = now.Add(time.Duration(cfg.Interval) * time.Second)
				for _, member := range state.members {
					if d.ctx.Err() != nil {
						return
					}
					d.scheduleProbe(id, member, false)
				}
			}
		}
	}
}

func (d *policyDispatcher) scheduleProbe(id, member int, urgent bool) {
	d.mu.Lock()
	state := d.policies[id]
	if d.ctx.Err() != nil || state.probing[member] || urgent && time.Since(state.lastUrgent[member]) < time.Second {
		d.mu.Unlock()
		return
	}
	state.probing[member] = true
	d.mu.Unlock()
	run := func() {
		defer func() {
			d.mu.Lock()
			delete(state.probing, member)
			if urgent {
				state.lastUrgent[member] = time.Now()
			}
			d.mu.Unlock()
		}()
		select {
		case d.probeSlots <- struct{}{}:
		case <-d.ctx.Done():
			return
		}
		defer func() { <-d.probeSlots }()
		start := time.Now()
		ctx, cancel := context.WithTimeout(d.ctx, 4*time.Second)
		defer cancel()
		err := d.probe(ctx, member, d.wiring.policies[id].HealthURL)
		if d.ctx.Err() == nil {
			d.record(id, member, start, err, false)
		}
	}
	if urgent {
		d.wg.Go(run)
	} else {
		run()
	}
}

func (d *policyDispatcher) probe(ctx context.Context, member int, rawURL string) error {
	dial := func(ctx context.Context, _, address string) (net.Conn, error) {
		target, blocked := d.validateTarget(ctx, address)
		if blocked {
			return nil, fmt.Errorf("health target is a local control endpoint or could not be validated")
		}
		return d.dial(ctx, member, target)
	}
	if rawURL == "" {
		conn, err := dial(ctx, "tcp", "1.1.1.1:443")
		if err == nil {
			_ = conn.Close()
		}
		return err
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: dial}
	defer transport.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, "HEAD", rawURL, nil)
	if err != nil {
		return err
	}
	res, err := transport.RoundTrip(req)
	if err != nil {
		return err
	}
	_ = res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return fmt.Errorf("health endpoint unavailable")
	}
	return nil
}

func (d *policyDispatcher) validateTarget(ctx context.Context, target string) (string, bool) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", true
	}
	number, err := strconv.Atoi(port)
	if err != nil {
		return "", true
	}
	if number != d.wiring.manager && number != d.wiring.controller && number != d.wiring.dispatch && number != d.wiring.scheduler {
		return target, false
	}
	if d.wiring.managerHosts[normalizedTarget(host)] || strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return "", true
	}
	local := func(ip netip.Addr) bool {
		ip = ip.WithZone("").Unmap()
		return !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || d.wiring.managerHosts[ip.String()]
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if local(ip) {
			return "", true
		}
		return target, false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addresses, err := d.lookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 || slices.ContainsFunc(addresses, local) {
		return "", true
	}
	return net.JoinHostPort(addresses[0].Unmap().String(), port), false
}
