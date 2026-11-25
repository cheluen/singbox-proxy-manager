package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"sb-proxy/backend/models"
)

const dispatchTag = "internal-policy-dispatch-in"

type runtimeWiring struct {
	hash                                         string
	controller, scheduler, dispatch, manager     int
	controllerSecret, policySecret, memberSecret string
	nodes                                        map[int]models.ProxyNode
	policies                                     map[int]*models.PolicyConfig
	settings                                     models.Settings
	redactSecrets                                *strings.Replacer
	managerHosts                                 map[string]bool
}

type liveFeatures struct {
	mu         sync.RWMutex
	wiring     *runtimeWiring
	dispatcher *policyDispatcher
	epoch      uint64
	beforeStop func()
	observer   *Observability
}

func newWiring(nodes []models.ProxyNode, settings models.Settings) (*runtimeWiring, error) {
	w := &runtimeWiring{nodes: make(map[int]models.ProxyNode, len(nodes)), policies: map[int]*models.PolicyConfig{}, settings: settings}
	managerPort, _ := strconv.Atoi(os.Getenv("PORT"))
	if managerPort == 0 {
		managerPort = 30000
	}
	w.manager = managerPort
	occupied := map[int]bool{managerPort: true}
	secretSet := map[string]bool{}
	addSecrets := func(values []string) {
		for _, value := range values {
			if value != "" {
				secretSet[value] = true
			}
		}
	}
	addSecrets(appendIPCheckConfigCredentials(nil, settings.GlobalUpstreamConfig))
	for _, group := range settings.GroupUpstreams {
		addSecrets(appendIPCheckConfigCredentials(nil, group.Config))
	}
	for _, node := range nodes {
		w.nodes[node.ID] = node
		occupied[node.InboundPort] = true
		addSecrets(appendIPCheckConfigCredentials([]string{node.Password}, node.Config, node.UpstreamConfig))
		if node.Enabled && models.IsPolicy(node.Type) {
			parsed, err := node.ParseConfig()
			if err != nil {
				return nil, err
			}
			w.policies[node.ID] = parsed.(*models.PolicyConfig)
		}
	}
	ports := []*int{&w.controller}
	if len(w.policies) > 0 {
		ports = append(ports, &w.scheduler, &w.dispatch)
		w.managerHosts = map[string]bool{}
		if name, err := os.Hostname(); err == nil {
			w.managerHosts[normalizedTarget(name)] = true
		}
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			return nil, fmt.Errorf("list local policy control addresses: %w", err)
		}
		for _, address := range addresses {
			if prefix, ok := address.(*net.IPNet); ok {
				w.managerHosts[prefix.IP.String()] = true
			}
		}
	}
	for _, port := range ports {
		for {
			listener, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			candidate := listener.Addr().(*net.TCPAddr).Port
			_ = listener.Close()
			if !occupied[candidate] {
				occupied[candidate] = true
				*port = candidate
				break
			}
		}
	}
	var secrets [96]byte
	if _, err := rand.Read(secrets[:]); err != nil {
		return nil, err
	}
	w.controllerSecret = hex.EncodeToString(secrets[:32])
	w.policySecret = hex.EncodeToString(secrets[32:64])
	w.memberSecret = hex.EncodeToString(secrets[64:])
	addSecrets([]string{w.policySecret, w.memberSecret, w.controllerSecret})
	secretList := make([]string, 0, len(secretSet))
	for secret := range secretSet {
		secretList = append(secretList, secret)
	}
	sort.Slice(secretList, func(i, j int) bool { return len(secretList[i]) > len(secretList[j]) })
	replacements := make([]string, 0, len(secretList)*2)
	for _, secret := range secretList {
		replacements = append(replacements, secret, "[redacted]")
	}
	w.redactSecrets = strings.NewReplacer(replacements...)
	return w, nil
}

func loopback(port int) string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }

func (w *runtimeWiring) addControls(config *SingBoxConfig) {
	config.Experimental = map[string]any{"clash_api": map[string]string{"external_controller": loopback(w.controller), "secret": w.controllerSecret}}
	if len(w.policies) == 0 {
		return
	}
	members := map[int]bool{}
	for _, policy := range w.policies {
		for _, id := range policy.MemberIDs {
			if member, ok := w.nodes[id]; ok && member.Enabled && !models.IsPolicy(member.Type) {
				members[id] = true
			}
		}
	}
	ids := make([]int, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	in := InboundConfig{Type: "socks", Tag: dispatchTag, Listen: "127.0.0.1", Port: w.dispatch}
	for _, id := range ids {
		username := fmt.Sprintf("member-%d", id)
		in.Users = append(in.Users, InboundUser{Username: username, Password: w.memberSecret})
		config.Route.Rules = append(config.Route.Rules, RouteRule{Inbound: []string{dispatchTag}, Outbound: fmt.Sprintf("node-%d-out", id), Extra: map[string]any{"auth_user": []string{username}}})
	}
	if len(in.Users) == 0 {
		in.Users = []InboundUser{{Username: "inactive", Password: w.memberSecret}}
	}
	config.Route.Rules = append(config.Route.Rules, RouteRule{Inbound: []string{dispatchTag}, Extra: map[string]any{"action": "reject"}})
	config.Inbounds = append(config.Inbounds, in)
}

func (s *SingBoxService) policyStatus(node models.ProxyNode) *models.PolicyStatus {
	if !models.IsPolicy(node.Type) {
		return nil
	}
	s.features.mu.RLock()
	dispatcher := s.features.dispatcher
	s.features.mu.RUnlock()
	if dispatcher != nil {
		if state := dispatcher.status(node.ID); state != nil {
			return state
		}
	}
	return &models.PolicyStatus{Effective: []int{}, Health: []models.MemberHealth{}, Warnings: []string{"policy is inactive"}}
}

func (s *SingBoxService) WithPolicyStatus(node models.ProxyNode) models.ProxyNode {
	node.PolicyStatus = s.policyStatus(node)
	return node
}

func (s *SingBoxService) startFeatures() error {
	s.features.mu.Lock()
	defer s.features.mu.Unlock()
	w := s.features.wiring
	if w != nil && len(w.policies) > 0 {
		var logEvent func(LogEvent)
		if observer := s.features.observer; observer != nil {
			logEvent = observer.record
		}
		dispatcher, err := startPolicyDispatcher(w, logEvent)
		if err != nil {
			return err
		}
		s.features.dispatcher = dispatcher
	}
	s.features.epoch++
	return nil
}

func (s *SingBoxService) stopFeatures() {
	s.features.mu.Lock()
	dispatcher := s.features.dispatcher
	s.features.dispatcher = nil
	s.features.mu.Unlock()
	if dispatcher != nil {
		dispatcher.close()
	}
}

func (s *SingBoxService) CheckPolicyIP(ctx context.Context, id int, check upstreamIPChecker) (*IPInfo, error) {
	s.features.mu.RLock()
	w := s.features.wiring
	running := s.features.dispatcher != nil
	s.features.mu.RUnlock()
	if w == nil || !running || w.policies[id] == nil {
		return nil, fmt.Errorf("policy is inactive")
	}
	return check(ctx, loopback(w.scheduler), fmt.Sprintf("policy-%d", id), w.policySecret)
}
