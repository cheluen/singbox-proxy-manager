package models

import (
	"context"
	"database/sql"
	"time"
)

type NodeGroup struct {
	ID                          int        `json:"id"`
	Name                        string     `json:"name"`
	SortOrder                   int        `json:"sort_order"`
	AllocatePort                bool       `json:"allocate_port"`
	StartPort                   int        `json:"start_port"`
	UpstreamEnabled             bool       `json:"upstream_enabled"`
	UpstreamType                string     `json:"upstream_type"`
	UpstreamConfig              string     `json:"upstream_config"`
	UpstreamIP                  string     `json:"upstream_ip"`
	UpstreamLocation            string     `json:"upstream_location"`
	UpstreamCountryCode         string     `json:"upstream_country_code"`
	UpstreamLatency             int        `json:"upstream_latency"`
	UpstreamError               string     `json:"upstream_error"`
	UpstreamRevision            int64      `json:"upstream_revision"`
	SubscriptionURL             string     `json:"subscription_url"`
	SubscriptionAutoUpdate      bool       `json:"subscription_auto_update"`
	SubscriptionIntervalMinutes int        `json:"subscription_interval_minutes"`
	SubscriptionLastUpdatedAt   *time.Time `json:"subscription_last_updated_at"`
	SubscriptionLastError       string     `json:"subscription_last_error"`
	Revision                    int        `json:"revision"`
	CreatedAt                   time.Time  `json:"created_at"`
	UpdatedAt                   time.Time  `json:"updated_at"`
	NodeCount                   int        `json:"node_count"`
	EnabledNodeCount            int        `json:"enabled_node_count"`
}

const GroupColumns = `id,name,sort_order,allocate_port,start_port,upstream_enabled,upstream_type,upstream_config,upstream_ip,upstream_location,upstream_country_code,upstream_latency,upstream_error,upstream_revision,subscription_url,subscription_auto_update,subscription_interval_minutes,subscription_last_updated_at,subscription_last_error,revision,created_at,updated_at`
const groupSelect = `SELECT ` + GroupColumns + `,
	(SELECT COUNT(*) FROM proxy_nodes WHERE group_id=node_groups.id),
	(SELECT COUNT(*) FROM proxy_nodes WHERE group_id=node_groups.id AND enabled)
	FROM node_groups`

func scanGroup(row rowScanner) (NodeGroup, error) {
	var group NodeGroup
	err := row.Scan(&group.ID, &group.Name, &group.SortOrder, &group.AllocatePort, &group.StartPort,
		&group.UpstreamEnabled, &group.UpstreamType, &group.UpstreamConfig, &group.UpstreamIP, &group.UpstreamLocation, &group.UpstreamCountryCode, &group.UpstreamLatency, &group.UpstreamError, &group.UpstreamRevision,
		&group.SubscriptionURL, &group.SubscriptionAutoUpdate, &group.SubscriptionIntervalMinutes, &group.SubscriptionLastUpdatedAt, &group.SubscriptionLastError, &group.Revision, &group.CreatedAt, &group.UpdatedAt, &group.NodeCount, &group.EnabledNodeCount)
	return group, err
}

func LoadGroup(ctx context.Context, db queryRower, id int) (NodeGroup, error) {
	if id == 0 {
		return scanGroup(db.QueryRowContext(ctx, groupSelect+" ORDER BY sort_order,id LIMIT 1"))
	}
	return scanGroup(db.QueryRowContext(ctx, groupSelect+" WHERE id=?", id))
}

func LoadGroups(ctx context.Context, db queryer) ([]NodeGroup, error) {
	rows, err := db.QueryContext(ctx, groupSelect+" ORDER BY sort_order,id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	groups := []NodeGroup{}
	for rows.Next() {
		group, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func NullablePort(port int) sql.NullInt64 { return sql.NullInt64{Int64: int64(port), Valid: port != 0} }
