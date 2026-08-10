package services

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type subscriptionResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type defaultSubscriptionResolver struct {
	resolver *net.Resolver
}

func (r defaultSubscriptionResolver) LookupNetIP(
	ctx context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	return r.resolver.LookupNetIP(ctx, network, host)
}

type subscriptionTargetPolicy struct {
	resolver subscriptionResolver
	dialer   net.Dialer
}

var subscriptionHTTPClient = newSecureSubscriptionHTTPClient(defaultSubscriptionResolver{
	resolver: net.DefaultResolver,
})

func newSecureSubscriptionHTTPClient(resolver subscriptionResolver) *http.Client {
	policy := &subscriptionTargetPolicy{
		resolver: resolver,
		dialer: net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		},
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           policy.dialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second,
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       subscriptionFetchTimeout,
		CheckRedirect: policy.checkRedirect,
	}
}

func (p *subscriptionTargetPolicy) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("too many subscription redirects")
	}
	_, err := p.resolveAndValidate(req.Context(), req.URL)
	if err != nil {
		return fmt.Errorf("unsafe subscription redirect target: %w", err)
	}
	return nil
}

func (p *subscriptionTargetPolicy) dialContext(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid subscription target address: %w", err)
	}

	targetURL := &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
	addresses, err := p.resolveAndValidate(ctx, targetURL)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, ip := range addresses {
		if network == "tcp4" && !ip.Is4() {
			continue
		}
		if network == "tcp6" && !ip.Is6() {
			continue
		}
		connection, dialErr := p.dialer.DialContext(
			ctx,
			network,
			net.JoinHostPort(ip.String(), port),
		)
		if dialErr == nil {
			return connection, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no address matched network %s", network)
	}
	return nil, fmt.Errorf("failed to connect to subscription target: %w", lastErr)
}

func (p *subscriptionTargetPolicy) resolveAndValidate(
	ctx context.Context,
	target *url.URL,
) ([]netip.Addr, error) {
	if target == nil {
		return nil, fmt.Errorf("missing subscription URL")
	}
	scheme := strings.ToLower(strings.TrimSpace(target.Scheme))
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("unsupported subscription URL scheme %q", target.Scheme)
	}

	host := strings.TrimSpace(target.Hostname())
	if host == "" {
		return nil, fmt.Errorf("subscription URL host is required")
	}
	normalizedHost := strings.TrimSuffix(strings.ToLower(host), ".")
	if normalizedHost == "localhost" || strings.HasSuffix(normalizedHost, ".localhost") {
		return nil, fmt.Errorf("subscription target %q is local", host)
	}

	if literal, err := netip.ParseAddr(host); err == nil {
		literal = literal.Unmap()
		if !isPublicSubscriptionAddress(literal) {
			return nil, fmt.Errorf("subscription target address %s is not public", literal)
		}
		return []netip.Addr{literal}, nil
	}

	addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve subscription target: %w", err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("subscription target resolved to no addresses")
	}

	validated := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !isPublicSubscriptionAddress(address) {
			return nil, fmt.Errorf("subscription target resolved to non-public address %s", address)
		}
		validated = append(validated, address)
	}
	return validated, nil
}

var blockedSubscriptionPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

func isPublicSubscriptionAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsUnspecified() || address.IsLinkLocalUnicast() ||
		address.IsLinkLocalMulticast() || address.IsMulticast() {
		return false
	}
	for _, prefix := range blockedSubscriptionPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
