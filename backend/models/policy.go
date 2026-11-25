package models

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	PolicyMemberSourceNode  = "node"
	PolicyMemberSourceGroup = "group"
)

type PolicyMemberSource struct {
	Type string `json:"type"`
	ID   int    `json:"id"`
}

type PolicyConfig struct {
	MemberIDs     []int                `json:"member_ids"`
	MemberSources []PolicyMemberSource `json:"member_sources,omitempty"`
	Strategy      string               `json:"strategy,omitempty"`
	HealthURL     string               `json:"health_check_url,omitempty"`
	Interval      int                  `json:"health_check_interval"`
}

type MemberHealth struct {
	NodeID    int        `json:"node_id"`
	Healthy   bool       `json:"healthy"`
	Latency   int        `json:"latency"`
	LastError string     `json:"last_error"`
	CheckedAt *time.Time `json:"checked_at"`
}

type PolicyStatus struct {
	Effective []int          `json:"effective_member_ids"`
	Active    int            `json:"policy_active_member_id"`
	Health    []MemberHealth `json:"policy_member_health"`
	Warnings  []string       `json:"policy_warnings"`
}

func IsPolicy(protocol string) bool { return protocol == "failover" || protocol == "loadbalance" }

func (config *PolicyConfig) normalize(protocol string) error {
	if protocol == "failover" {
		config.Strategy = ""
	} else if config.Strategy == "" {
		config.Strategy = "round-robin"
	} else if config.Strategy != "round-robin" && config.Strategy != "consistent-hashing" {
		return fmt.Errorf("invalid load balancing strategy")
	}
	if config.Interval == 0 {
		config.Interval = 30
	}
	if config.Interval < 10 || config.Interval > 3600 {
		return fmt.Errorf("health_check_interval must be between 10 and 3600 seconds")
	}
	config.HealthURL = strings.TrimSpace(config.HealthURL)
	if config.HealthURL != "" {
		target, err := url.Parse(config.HealthURL)
		if err != nil || target.Hostname() == "" || (target.Scheme != "http" && target.Scheme != "https") || target.User != nil {
			return fmt.Errorf("health_check_url requires an HTTP or HTTPS URL without credentials")
		}
	}
	sources := make([]PolicyMemberSource, 0, len(config.MemberSources))
	seenSources := make(map[PolicyMemberSource]bool, len(config.MemberSources))
	hasGroup := false
	for _, source := range config.MemberSources {
		if source.ID <= 0 || (source.Type != PolicyMemberSourceNode && source.Type != PolicyMemberSourceGroup) {
			return fmt.Errorf("invalid policy member source")
		}
		if !seenSources[source] {
			sources = append(sources, source)
			seenSources[source] = true
		}
		hasGroup = hasGroup || source.Type == PolicyMemberSourceGroup
	}
	config.MemberSources = sources
	if !hasGroup {
		if len(config.MemberIDs) == 0 {
			for _, source := range sources {
				config.MemberIDs = append(config.MemberIDs, source.ID)
			}
		}
		config.MemberSources = nil
	}
	members := make([]int, 0, len(config.MemberIDs))
	seen := make(map[int]bool, len(config.MemberIDs))
	for _, id := range config.MemberIDs {
		if id <= 0 {
			return fmt.Errorf("invalid member id")
		}
		if !seen[id] {
			members = append(members, id)
			seen[id] = true
		}
	}
	config.MemberIDs = members
	return nil
}
