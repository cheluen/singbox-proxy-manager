package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sb-proxy/backend/models"
)

type ConnectionRoute struct {
	Key          string   `json:"key"`
	ClientIP     string   `json:"client_ip"`
	InboundPort  int      `json:"inbound_port"`
	InboundID    int      `json:"inbound_node_id"`
	InboundName  string   `json:"inbound_node_name"`
	RouteID      string   `json:"route_id"`
	OutboundID   int      `json:"outbound_node_id"`
	OutboundName string   `json:"outbound_node_name"`
	OutboundType string   `json:"outbound_type"`
	MemberID     int      `json:"policy_member_id"`
	MemberName   string   `json:"policy_member_name"`
	Upstream     string   `json:"upstream_label"`
	Count        int      `json:"active_count"`
	Upload       int64    `json:"upload_bytes"`
	Download     int64    `json:"download_bytes"`
	UploadRate   *float64 `json:"upload_bps"`
	DownloadRate *float64 `json:"download_bps"`
}

type ConnectionSnapshot struct {
	StreamError string            `json:"-"`
	Epoch       uint64            `json:"runtime_epoch"`
	At          time.Time         `json:"sampled_at"`
	Active      int               `json:"active_connections"`
	Clients     int               `json:"client_count"`
	RouteCount  int               `json:"route_count"`
	Up          *float64          `json:"total_upload_bps"`
	Down        *float64          `json:"total_download_bps"`
	Routes      []ConnectionRoute `json:"routes"`
}

type clashConnection struct {
	ID       string    `json:"id"`
	Start    time.Time `json:"start"`
	Upload   int64     `json:"upload"`
	Download int64     `json:"download"`
	Chains   []string  `json:"chains"`
	Rule     string    `json:"rule"`
	Metadata struct {
		Type            string `json:"type"`
		SourceIP        string `json:"sourceIP"`
		SourcePort      string `json:"sourcePort"`
		Host            string `json:"host"`
		DestinationIP   string `json:"destinationIP"`
		DestinationPort string `json:"destinationPort"`
	} `json:"metadata"`
}

type connectionCounter struct {
	up, down int64
	member   int
}
type routeDelta struct {
	route    ConnectionRoute
	up, down int64
	opened   bool
}
type connectionSink func(time.Time, []routeDelta)

type ConnectionMonitor struct {
	service         *SingBoxService
	client          *http.Client
	mu              sync.Mutex
	subs            map[chan ConnectionSnapshot]struct{}
	cancel          context.CancelFunc
	sink            connectionSink
	sampleMu        sync.Mutex
	last            ConnectionSnapshot
	counters        map[string]connectionCounter
	baselinePending bool
	idleTimer       *time.Timer
	idleUntil       time.Time
	streamError     string
}

const pollingStateTTL = 5 * time.Second

func NewConnectionMonitor(service *SingBoxService) *ConnectionMonitor {
	return &ConnectionMonitor{service: service, client: &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}, subs: make(map[chan ConnectionSnapshot]struct{})}
}

func (m *ConnectionMonitor) updateWorker() {
	if len(m.subs) > 0 || m.sink != nil {
		if m.cancel == nil {
			ctx, cancel := context.WithCancel(context.Background())
			m.cancel = cancel
			go m.run(ctx)
		}
		return
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

func (m *ConnectionMonitor) releaseIdle() {
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel == nil {
		m.streamError = ""
		m.stopIdleTimer()
		m.last = ConnectionSnapshot{}
		m.counters = nil
	}
}

func (m *ConnectionMonitor) stopIdleTimer() {
	if m.idleTimer != nil {
		m.idleTimer.Stop()
		m.idleTimer = nil
	}
	m.idleUntil = time.Time{}
}

func (m *ConnectionMonitor) retainPollingState() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	m.idleUntil = time.Now().Add(pollingStateTTL)
	if m.idleTimer != nil {
		m.idleTimer.Reset(pollingStateTTL)
		return
	}
	m.idleTimer = time.AfterFunc(pollingStateTTL, func() {
		m.sampleMu.Lock()
		defer m.sampleMu.Unlock()
		if !m.idleUntil.IsZero() && !time.Now().Before(m.idleUntil) {
			m.stopIdleTimer()
			m.last, m.counters = ConnectionSnapshot{}, nil
		}
	})
}

func (m *ConnectionMonitor) Subscribe() (<-chan ConnectionSnapshot, func()) {
	ch := make(chan ConnectionSnapshot, 1)
	m.sampleMu.Lock()
	m.mu.Lock()
	m.stopIdleTimer()
	if m.cancel != nil && m.streamError != "" {
		ch <- ConnectionSnapshot{StreamError: m.streamError}
	} else if m.cancel != nil && !m.last.At.IsZero() {
		ch <- m.last
	}
	m.subs[ch] = struct{}{}
	m.updateWorker()
	m.mu.Unlock()
	m.sampleMu.Unlock()
	return ch, sync.OnceFunc(func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.updateWorker()
		m.mu.Unlock()
		m.releaseIdle()
	})
}

func (m *ConnectionMonitor) SetSink(sink connectionSink) {
	m.sampleMu.Lock()
	m.mu.Lock()
	if m.sink == nil && sink != nil {
		m.stopIdleTimer()
		m.baselinePending = true
	}
	m.sink = sink
	m.updateWorker()
	m.mu.Unlock()
	m.sampleMu.Unlock()
	if sink == nil {
		m.releaseIdle()
	}
}

func (m *ConnectionMonitor) run(ctx context.Context) {
	ticker := time.NewTicker(1200 * time.Millisecond)
	defer ticker.Stop()
	failures := 0
	for ctx.Err() == nil {
		snapshot, err := m.capture(ctx, false)
		m.mu.Lock()
		if err != nil {
			failures++
			snapshot.StreamError = "connection telemetry unavailable"
		} else {
			failures = 0
		}
		if ctx.Err() == nil && (err == nil || failures >= 3) && (snapshot.StreamError == "" || snapshot.StreamError != m.streamError) {
			m.streamError = snapshot.StreamError
			for ch := range m.subs {
				select {
				case ch <- snapshot:
				default:
					select {
					case <-ch:
					default:
					}
					select {
					case ch <- snapshot:
					default:
					}
				}
			}
		}
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *ConnectionMonitor) Capture(ctx context.Context) (ConnectionSnapshot, error) {
	return m.capture(ctx, true)
}

func (m *ConnectionMonitor) capture(ctx context.Context, poll bool) (ConnectionSnapshot, error) {
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return ConnectionSnapshot{}, err
	}
	if !m.idleUntil.IsZero() && !time.Now().Before(m.idleUntil) {
		m.last, m.counters = ConnectionSnapshot{}, nil
	}
	if poll {
		defer m.retainPollingState()
	}
	f := &m.service.features
	f.mu.RLock()
	w, epoch, dispatcher := f.wiring, f.epoch, f.dispatcher
	f.mu.RUnlock()
	now := time.Now().UTC()
	snapshot := ConnectionSnapshot{Epoch: epoch, At: now, Routes: []ConnectionRoute{}}
	if w == nil || !m.service.RuntimeStatus().Running {
		snapshot.StreamError = "sing-box runtime is unavailable"
		m.counters = nil
		m.last = snapshot
		return snapshot, nil
	}
	if !m.baselinePending && m.last.Epoch == epoch && now.Sub(m.last.At) < 100*time.Millisecond {
		return m.last, nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", "http://"+loopback(w.controller)+"/connections", nil)
	if err != nil {
		return snapshot, err
	}
	req.Header.Set("Authorization", "Bearer "+w.controllerSecret)
	res, err := m.client.Do(req)
	if err != nil {
		return snapshot, fmt.Errorf("connection telemetry unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return snapshot, fmt.Errorf("connection telemetry returned HTTP %d", res.StatusCode)
	}
	var raw struct {
		Connections []clashConnection `json:"connections"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(&raw); err != nil {
		return snapshot, fmt.Errorf("invalid connection telemetry")
	}
	if ctx.Err() != nil {
		return snapshot, ctx.Err()
	}
	if epoch != m.last.Epoch {
		m.counters = nil
	}
	next, deltas := m.aggregate(&snapshot, raw.Connections, w, dispatcher)
	m.counters = next
	m.last = snapshot
	m.mu.Lock()
	sink := m.sink
	m.mu.Unlock()
	if sink != nil {
		if !m.baselinePending {
			sink(now, deltas)
		}
		m.baselinePending = false
	}
	return snapshot, nil
}

func upstreamLabel(w *runtimeWiring, node models.ProxyNode) string {
	switch node.UpstreamMode {
	case models.UpstreamModeGlobal:
		if w.settings.GlobalUpstreamEnabled {
			return "global: " + w.settings.GlobalUpstreamType
		}
	case models.UpstreamModeCustom:
		return "custom: " + node.UpstreamType
	case models.UpstreamModeLegacy, "":
		var legacy struct {
			Detour string `json:"detour"`
		}
		if json.Unmarshal([]byte(node.Config), &legacy) == nil && legacy.Detour != "" && legacy.Detour != "direct" {
			if id, ok := tagID(legacy.Detour, "node-", "-out"); ok {
				return w.nodes[id].Name
			}
			return legacy.Detour
		}
	}
	return ""
}

func (c clashConnection) route(w *runtimeWiring) (ConnectionRoute, bool) {
	_, tag, _ := strings.Cut(c.Metadata.Type, "/")
	id, ok := tagID(tag, "node-", "-in")
	if !ok {
		return ConnectionRoute{}, false
	}
	in := w.nodes[id]
	out := in
	for _, chain := range c.Chains {
		if match, ok := tagID(chain, "node-", "-out"); ok {
			out = w.nodes[match]
			break
		}
	}
	if _, action, ok := strings.Cut(c.Rule, " => route("); ok {
		if target, valid := tagID(strings.TrimSuffix(action, ")"), "node-", "-out"); valid {
			if node, exists := w.nodes[target]; exists {
				out = node
			}
		}
	}
	route := ConnectionRoute{ClientIP: c.Metadata.SourceIP, InboundID: id, InboundName: in.Name, InboundPort: in.InboundPort, OutboundID: out.ID, OutboundName: out.Name, OutboundType: out.Type, Upstream: upstreamLabel(w, out)}
	reuse := out.Username + "+" + strconv.Itoa(out.InboundPort)
	if out.Username != "" && strings.Contains(c.Rule, "auth_user="+reuse+" =>") {
		route.RouteID = reuse
	}
	return route, true
}

func (c clashConnection) target() string {
	host := c.Metadata.Host
	if host == "" {
		host = c.Metadata.DestinationIP
	}
	return net.JoinHostPort(normalizedTarget(host), c.Metadata.DestinationPort)
}

func (d *policyDispatcher) matchedMembers(raw []clashConnection) map[string]int {
	resolved := map[string]int{}
	innerCounts := map[string]int{}
	outerCounts := map[string]int{}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range raw {
		if c.Metadata.Type != "socks/"+dispatchTag {
			if route, ok := c.route(d.wiring); ok && models.IsPolicy(route.OutboundType) {
				outerCounts[strconv.Itoa(route.OutboundID)+"/"+c.target()]++
			}
			continue
		}
		session, ok := d.sessions[net.JoinHostPort(c.Metadata.SourceIP, c.Metadata.SourcePort)]
		if !ok || (!c.Start.IsZero() && c.Start.Before(session.Started)) {
			continue
		}
		target := c.target()
		if host, port, err := net.SplitHostPort(session.DialTarget); err == nil && net.JoinHostPort(normalizedTarget(host), port) != target {
			continue
		}
		if host, port, err := net.SplitHostPort(session.Target); err == nil {
			target = net.JoinHostPort(normalizedTarget(host), port)
		}
		key := strconv.Itoa(session.Policy) + "/" + target
		innerCounts[key]++
		if previous, exists := resolved[key]; exists && previous != session.Member {
			resolved[key] = 0
		} else {
			resolved[key] = session.Member
		}
	}
	for key, count := range innerCounts {
		if count != outerCounts[key] {
			delete(resolved, key)
		}
	}
	return resolved
}

func (m *ConnectionMonitor) aggregate(snapshot *ConnectionSnapshot, raw []clashConnection, w *runtimeWiring, dispatcher *policyDispatcher) (map[string]connectionCounter, []routeDelta) {
	next := make(map[string]connectionCounter, len(raw))
	deltas := make([]routeDelta, 0, len(raw))
	routes := map[string]*ConnectionRoute{}
	clients := map[string]bool{}
	elapsed := snapshot.At.Sub(m.last.At).Seconds()
	hasRate := m.counters != nil && elapsed > 0
	if hasRate {
		snapshot.Up = new(float64)
		snapshot.Down = new(float64)
	}
	var members map[string]int
	if dispatcher != nil {
		members = dispatcher.matchedMembers(raw)
	}
	for _, c := range raw {
		r, ok := c.route(w)
		if !ok {
			continue
		}
		prev, seen := m.counters[c.ID]
		up, down := c.Upload, c.Download
		if seen {
			up = max(0, up-prev.up)
			down = max(0, down-prev.down)
		}
		if models.IsPolicy(r.OutboundType) {
			r.MemberID = prev.member
			if r.MemberID == 0 {
				r.MemberID = members[strconv.Itoa(r.OutboundID)+"/"+c.target()]
			}
			if member, ok := w.nodes[r.MemberID]; ok {
				r.MemberName = member.Name
				r.Upstream = upstreamLabel(w, member)
			}
		}
		r.Key = fmt.Sprintf("%s|%d|%d|%s|%d", r.ClientIP, r.InboundPort, r.OutboundID, r.RouteID, r.MemberID)
		deltas = append(deltas, routeDelta{r, up, down, !seen})
		next[c.ID] = connectionCounter{c.Upload, c.Download, r.MemberID}
		aggregate := routes[r.Key]
		if aggregate == nil {
			aggregate = &r
			routes[r.Key] = aggregate
			if hasRate {
				aggregate.UploadRate = new(float64)
				aggregate.DownloadRate = new(float64)
			}
		}
		aggregate.Count++
		aggregate.Upload += c.Upload
		aggregate.Download += c.Download
		clients[r.ClientIP] = true
		snapshot.Active++
		if hasRate {
			*aggregate.UploadRate += float64(up) / elapsed
			*aggregate.DownloadRate += float64(down) / elapsed
			*snapshot.Up += float64(up) / elapsed
			*snapshot.Down += float64(down) / elapsed
		}
	}
	for _, r := range routes {
		snapshot.Routes = append(snapshot.Routes, *r)
	}
	sort.Slice(snapshot.Routes, func(i, j int) bool { return snapshot.Routes[i].Key < snapshot.Routes[j].Key })
	snapshot.RouteCount = len(routes)
	snapshot.Clients = len(clients)
	return next, deltas
}
