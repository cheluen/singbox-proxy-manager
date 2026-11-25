package models

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const ProxyNodeSelectColumns = `
	id, name, remark, type, config, inbound_port, inbound_mode, inbound_port_pinned,
	username, password, tcp_reuse_enabled, upstream_mode, upstream_type,
	upstream_config, upstream_ip, upstream_location, upstream_country_code,
	upstream_latency, upstream_error, sort_order, node_ip, location, country_code, latency,
	enabled, created_at, updated_at, group_id, subscription_key, subscription_name,
	check_status, check_error, checked_at, check_revision
`

const runtimeSettingsSelectColumns = `
	preserve_inbound_ports, global_upstream_enabled,
	global_upstream_type, global_upstream_config, global_upstream_ip,
	global_upstream_location, global_upstream_country_code, global_upstream_latency,
	global_upstream_error, node_list_column_widths, inbound_tls_cert_path, inbound_tls_key_path
`

const SettingsSnapshotColumns = `
	id, singleton_key, ` + runtimeSettingsSelectColumns + `, global_upstream_revision,
	admin_password, admin_password_env_fingerprint, auth_generation, created_at, updated_at
`

type rowScanner interface {
	Scan(dest ...interface{}) error
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

func scanProxyNode(scanner rowScanner) (ProxyNode, error) {
	var node ProxyNode
	var port sql.NullInt64
	err := scanner.Scan(
		&node.ID,
		&node.Name,
		&node.Remark,
		&node.Type,
		&node.Config,
		&port,
		&node.InboundMode,
		&node.InboundPortPinned,
		&node.Username,
		&node.Password,
		&node.TCPReuseEnabled,
		&node.UpstreamMode,
		&node.UpstreamType,
		&node.UpstreamConfig,
		&node.UpstreamIP,
		&node.UpstreamLocation,
		&node.UpstreamCountryCode,
		&node.UpstreamLatency,
		&node.UpstreamError,
		&node.SortOrder,
		&node.NodeIP,
		&node.Location,
		&node.CountryCode,
		&node.Latency,
		&node.Enabled,
		&node.CreatedAt,
		&node.UpdatedAt,
		&node.GroupID, &node.SubscriptionKey, &node.SubscriptionName,
		&node.CheckStatus, &node.CheckError, &node.CheckedAt, &node.CheckRevision,
	)
	node.InboundPort = int(port.Int64)
	return node, err
}

func LoadProxyNodes(ctx context.Context, queryer queryer) ([]ProxyNode, error) {
	return LoadGroupNodes(ctx, queryer, 0)
}

func LoadActiveInbounds(ctx context.Context, db queryer) ([]ProxyNode, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, inbound_port, enabled FROM proxy_nodes WHERE inbound_port > 0 AND enabled`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var listeners []ProxyNode
	for rows.Next() {
		var listener ProxyNode
		if err := rows.Scan(&listener.ID, &listener.InboundPort, &listener.Enabled); err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
	}
	return listeners, rows.Err()
}

func LoadGroupNodes(ctx context.Context, queryer queryer, groupID int) ([]ProxyNode, error) {
	query := "SELECT " + ProxyNodeSelectColumns + " FROM proxy_nodes"
	var args []any
	if groupID > 0 {
		query += " WHERE group_id = ?"
		args = append(args, groupID)
	}
	query += " ORDER BY (SELECT sort_order FROM node_groups WHERE id = proxy_nodes.group_id), group_id, sort_order, id"
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make([]ProxyNode, 0)
	for rows.Next() {
		node, err := scanProxyNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func LoadProxyNode(ctx context.Context, queryer queryRower, id int) (ProxyNode, error) {
	return scanProxyNode(queryer.QueryRowContext(ctx, `
		SELECT `+ProxyNodeSelectColumns+`
		FROM proxy_nodes
		WHERE id = ?
	`, id))
}

type runtimeSettingsReader interface {
	queryRower
	queryer
}

func LoadRuntimeSettings(ctx context.Context, db runtimeSettingsReader) (Settings, error) {
	var settings Settings
	var columnWidths sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT `+runtimeSettingsSelectColumns+`
		FROM settings
		WHERE singleton_key = 1
	`).Scan(
		&settings.PreserveInboundPorts,
		&settings.GlobalUpstreamEnabled,
		&settings.GlobalUpstreamType,
		&settings.GlobalUpstreamConfig,
		&settings.GlobalUpstreamIP,
		&settings.GlobalUpstreamLocation,
		&settings.GlobalUpstreamCountryCode,
		&settings.GlobalUpstreamLatency,
		&settings.GlobalUpstreamError,
		&columnWidths,
		&settings.InboundTLSCertPath,
		&settings.InboundTLSKeyPath,
	)
	if err != nil {
		return settings, err
	}
	if columnWidths.Valid && strings.TrimSpace(columnWidths.String) != "" {
		if err := json.Unmarshal([]byte(columnWidths.String), &settings.NodeListColumnWidths); err != nil {
			return settings, fmt.Errorf("decode node_list_column_widths: %w", err)
		}
	}
	if settings.NodeListColumnWidths == nil {
		settings.NodeListColumnWidths = map[string]int{}
	}
	rows, err := db.QueryContext(ctx, "SELECT id,upstream_enabled,upstream_type,upstream_config FROM node_groups WHERE upstream_enabled OR upstream_config<>''")
	if err != nil {
		return settings, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var upstream GroupUpstream
		if err := rows.Scan(&id, &upstream.Enabled, &upstream.Type, &upstream.Config); err != nil {
			return settings, err
		}
		if settings.GroupUpstreams == nil {
			settings.GroupUpstreams = make(map[int]GroupUpstream)
		}
		settings.GroupUpstreams[id] = upstream
	}
	return settings, rows.Err()
}
