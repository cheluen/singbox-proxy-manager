package services

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"sb-proxy/backend/models"
)

type controlledNetIPResolver struct {
	mu        sync.Mutex
	addresses map[string][]netip.Addr
	err       error
	calls     int
}

func (resolver *controlledNetIPResolver) LookupNetIP(
	ctx context.Context,
	_ string,
	host string,
) ([]netip.Addr, error) {
	resolver.mu.Lock()
	resolver.calls++
	addresses := append([]netip.Addr(nil), resolver.addresses[host]...)
	err := resolver.err
	resolver.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if addresses == nil {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return addresses, nil
}

func (resolver *controlledNetIPResolver) callCount() int {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.calls
}

func managedUpstreamLoopTestNode(server string, upstreamPort int, inboundPort int) models.ProxyNode {
	return models.ProxyNode{
		ID:             1,
		Name:           "loop-guard",
		Type:           "direct",
		Config:         `{}`,
		InboundPort:    inboundPort,
		Enabled:        true,
		UpstreamMode:   models.UpstreamModeCustom,
		UpstreamType:   "socks5",
		UpstreamConfig: fmt.Sprintf(`{"server":%q,"server_port":%d}`, server, upstreamPort),
	}
}

func TestBuildGlobalConfigRejectsDomainResolvingToLocalInbound(t *testing.T) {
	resolver := &controlledNetIPResolver{addresses: map[string][]netip.Addr{
		"loop.example.test": {
			netip.MustParseAddr("203.0.113.10"),
			netip.MustParseAddr("127.0.0.1"),
		},
	}}
	service := NewSingBoxService(t.TempDir())
	service.resolver = resolver
	node := managedUpstreamLoopTestNode("loop.example.test", 35123, 35123)

	_, err := service.BuildGlobalConfig([]models.ProxyNode{node})
	if err == nil || !IsUpstreamValidationError(err) {
		t.Fatalf("expected resolved local upstream rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), "resolves to local address 127.0.0.1") || !strings.Contains(err.Error(), "35123") {
		t.Fatalf("unexpected loop rejection: %v", err)
	}
	if resolver.callCount() != 1 {
		t.Fatalf("unexpected resolver calls: %d", resolver.callCount())
	}
}

func TestBuildGlobalConfigAllowsResolvedRemoteAddressAndSkipsIrrelevantPorts(t *testing.T) {
	remoteResolver := &controlledNetIPResolver{addresses: map[string][]netip.Addr{
		"remote.example.test": {netip.MustParseAddr("203.0.113.20")},
	}}
	service := NewSingBoxService(t.TempDir())
	service.resolver = remoteResolver
	if _, err := service.BuildGlobalConfig([]models.ProxyNode{
		managedUpstreamLoopTestNode("remote.example.test", 35124, 35124),
	}); err != nil {
		t.Fatalf("remote upstream was rejected: %v", err)
	}
	if remoteResolver.callCount() != 1 {
		t.Fatalf("remote hostname resolver calls=%d want 1", remoteResolver.callCount())
	}

	skippedResolver := &controlledNetIPResolver{err: errors.New("DNS must not be used")}
	service.resolver = skippedResolver
	if _, err := service.BuildGlobalConfig([]models.ProxyNode{
		managedUpstreamLoopTestNode("unrelated.example.test", 443, 35125),
	}); err != nil {
		t.Fatalf("non-colliding upstream triggered DNS validation: %v", err)
	}
	if skippedResolver.callCount() != 0 {
		t.Fatalf("non-colliding target triggered %d DNS lookups", skippedResolver.callCount())
	}
}

func TestBuildGlobalConfigFailsClosedWhenCollisionDomainCannotResolve(t *testing.T) {
	resolver := &controlledNetIPResolver{err: errors.New("resolver unavailable")}
	service := NewSingBoxService(t.TempDir())
	service.resolver = resolver
	_, err := service.BuildGlobalConfig([]models.ProxyNode{
		managedUpstreamLoopTestNode("unknown.example.test", 35126, 35126),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot safely dial") || !strings.Contains(err.Error(), "resolver unavailable") {
		t.Fatalf("expected fail-closed DNS validation, got %v", err)
	}
}

func TestBuildGlobalConfigDNSValidationHonorsContext(t *testing.T) {
	resolver := &controlledNetIPResolver{addresses: map[string][]netip.Addr{}}
	service := NewSingBoxService(t.TempDir())
	service.resolver = resolver
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	startedAt := time.Now()
	_, err := service.BuildGlobalConfigContext(ctx, []models.ProxyNode{
		managedUpstreamLoopTestNode("slow.example.test", 35127, 35127),
	})
	if err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("expected DNS context deadline, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("DNS cancellation took too long: %s", elapsed)
	}
}
