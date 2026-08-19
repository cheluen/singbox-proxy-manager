package services

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"sb-proxy/backend/models"
)

const (
	upstreamDNSValidationTimeout     = 5 * time.Second
	upstreamDNSValidationConcurrency = 8
)

type netIPResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type upstreamHostCollision struct {
	host        string
	scope       string
	inboundPort int
	nodeID      int
}

type upstreamHostLookup struct {
	host      string
	collision upstreamHostCollision
	addresses []netip.Addr
	err       error
}

func (s *SingBoxService) validateManagedUpstreamInboundCollisions(
	ctx context.Context,
	upstreams []parsedUpstreamProxy,
	inboundPorts map[int]*models.ProxyNode,
) error {
	if len(upstreams) == 0 || len(inboundPorts) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	localHosts := managerLocalHosts()
	orderedPorts := make([]int, 0, len(inboundPorts))
	for port := range inboundPorts {
		orderedPorts = append(orderedPorts, port)
	}
	sort.Ints(orderedPorts)

	lookups := make([]upstreamHostLookup, 0)
	lookupIndexes := make(map[string]int)
	for _, upstream := range upstreams {
		targets, err := upstreamDialTargets(upstream.Config)
		if err != nil {
			return upstreamValidationErrorf("%s has invalid dial target: %v", upstream.Scope, err)
		}
		for _, target := range targets {
			for _, inboundPort := range orderedPorts {
				if !upstreamTargetContainsPort(target, inboundPort) {
					continue
				}
				node := inboundPorts[inboundPort]
				collision := upstreamHostCollision{
					host:        normalizeManagerHost(target.host),
					scope:       upstream.Scope,
					inboundPort: inboundPort,
					nodeID:      node.ID,
				}
				if isManagerLocalHost(collision.host, localHosts) {
					return upstreamLoopError(collision, "local address")
				}
				if net.ParseIP(collision.host) != nil {
					continue
				}
				if collision.host == "" {
					return upstreamValidationErrorf("%s has an empty dial target", upstream.Scope)
				}
				if _, exists := lookupIndexes[collision.host]; exists {
					continue
				}
				lookupIndexes[collision.host] = len(lookups)
				lookups = append(lookups, upstreamHostLookup{host: collision.host, collision: collision})
			}
		}
	}
	if len(lookups) == 0 {
		return nil
	}

	resolver := s.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	lookupCtx, cancel := context.WithTimeout(ctx, upstreamDNSValidationTimeout)
	defer cancel()
	jobs := make(chan int, len(lookups))
	for index := range lookups {
		jobs <- index
	}
	close(jobs)

	workerCount := min(len(lookups), upstreamDNSValidationConcurrency)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				lookups[index].addresses, lookups[index].err = resolver.LookupNetIP(lookupCtx, "ip", lookups[index].host)
			}
		}()
	}
	workers.Wait()

	for _, lookup := range lookups {
		if lookup.err != nil {
			return upstreamValidationErrorf(
				"%s cannot safely dial inbound port %d (node %d): resolve %q: %v",
				lookup.collision.scope,
				lookup.collision.inboundPort,
				lookup.collision.nodeID,
				lookup.host,
				lookup.err,
			)
		}
		if len(lookup.addresses) == 0 {
			return upstreamValidationErrorf(
				"%s cannot safely dial inbound port %d (node %d): %q resolved to no addresses",
				lookup.collision.scope,
				lookup.collision.inboundPort,
				lookup.collision.nodeID,
				lookup.host,
			)
		}
		for _, address := range lookup.addresses {
			if isManagerLocalAddress(address, localHosts) {
				return upstreamLoopError(
					lookup.collision,
					fmt.Sprintf("hostname resolves to local address %s", address.Unmap()),
				)
			}
		}
	}
	return nil
}

func upstreamTargetContainsPort(target upstreamDialTarget, port int) bool {
	for _, portRange := range target.ports {
		if port >= portRange.start && port <= portRange.end {
			return true
		}
	}
	return false
}

func upstreamLoopError(collision upstreamHostCollision, reason string) error {
	return upstreamValidationErrorf(
		"%s must not dial %s %q on local inbound port %d (node %d); route-number authentication cannot bypass this recursion guard",
		collision.scope,
		reason,
		collision.host,
		collision.inboundPort,
		collision.nodeID,
	)
}

func isManagerLocalAddress(address netip.Addr, localHosts map[string]struct{}) bool {
	address = address.Unmap()
	if address.IsLoopback() || address.IsUnspecified() {
		return true
	}
	_, exists := localHosts[address.String()]
	return exists
}
