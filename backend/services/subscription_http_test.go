package services

import (
	"context"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

type staticSubscriptionResolver struct {
	addresses map[string][]netip.Addr
	lookups   []string
}

func (resolver *staticSubscriptionResolver) LookupNetIP(
	_ context.Context,
	_ string,
	host string,
) ([]netip.Addr, error) {
	resolver.lookups = append(resolver.lookups, host)
	return resolver.addresses[host], nil
}

func TestSubscriptionTargetPolicyRejectsLocalAndMetadataAddresses(t *testing.T) {
	policy := &subscriptionTargetPolicy{resolver: &staticSubscriptionResolver{}}
	for _, rawURL := range []string{
		"http://0.1.2.3/sub",
		"http://127.0.0.1/sub",
		"http://127.12.34.56/sub",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.1/sub",
		"http://240.0.0.1/sub",
		"http://[::1]/sub",
		"http://[fe80::1]/sub",
		"http://[fec0::1]/sub",
		"http://localhost/sub",
	} {
		target, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse test URL %q: %v", rawURL, err)
		}
		if _, err := policy.resolveAndValidate(context.Background(), target); err == nil {
			t.Fatalf("unsafe subscription target %q was accepted", rawURL)
		}
	}
}

func TestSubscriptionTargetPolicyRejectsMixedDNSAnswers(t *testing.T) {
	resolver := &staticSubscriptionResolver{addresses: map[string][]netip.Addr{
		"rebind.example": {
			netip.MustParseAddr("8.8.8.8"),
			netip.MustParseAddr("192.168.1.20"),
		},
	}}
	policy := &subscriptionTargetPolicy{resolver: resolver}
	target, _ := url.Parse("https://rebind.example/sub")
	if _, err := policy.resolveAndValidate(context.Background(), target); err == nil {
		t.Fatalf("mixed public/private DNS answer was accepted")
	}
}

func TestSubscriptionRedirectPolicyRevalidatesEveryTarget(t *testing.T) {
	resolver := &staticSubscriptionResolver{addresses: map[string][]netip.Addr{
		"public.example":  {netip.MustParseAddr("8.8.8.8")},
		"private.example": {netip.MustParseAddr("10.0.0.5")},
	}}
	policy := &subscriptionTargetPolicy{resolver: resolver}

	publicURL, _ := url.Parse("https://public.example/sub")
	if err := policy.checkRedirect(&http.Request{URL: publicURL}, nil); err != nil {
		t.Fatalf("public redirect should pass: %v", err)
	}
	privateURL, _ := url.Parse("https://private.example/sub")
	err := policy.checkRedirect(&http.Request{URL: privateURL}, []*http.Request{{URL: publicURL}})
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("private redirect target was not rejected: %v", err)
	}
	if len(resolver.lookups) != 2 {
		t.Fatalf("redirect targets were not independently resolved: %v", resolver.lookups)
	}
}

func TestSubscriptionTargetPolicyAcceptsOnlyPublicDNSAnswers(t *testing.T) {
	resolver := &staticSubscriptionResolver{addresses: map[string][]netip.Addr{
		"subscription.example": {
			netip.MustParseAddr("1.1.1.1"),
			netip.MustParseAddr("2606:4700:4700::1111"),
		},
	}}
	policy := &subscriptionTargetPolicy{resolver: resolver}
	target, _ := url.Parse("https://subscription.example/sub")
	addresses, err := policy.resolveAndValidate(context.Background(), target)
	if err != nil || len(addresses) != 2 {
		t.Fatalf("public target rejected: addresses=%v err=%v", addresses, err)
	}
}
