package models

import (
	"context"
	"database/sql"
)

const ProxyNodeSelectColumns = `
	id, name, remark, type, config, inbound_port, inbound_port_pinned,
	username, password, tcp_reuse_enabled, upstream_mode, upstream_type,
	upstream_config, upstream_ip, upstream_location, upstream_country_code,
	upstream_latency, upstream_error, sort_order, node_ip, location, country_code, latency,
	enabled, created_at, updated_at
`

const RuntimeSettingsSelectColumns = `
	id, singleton_key, start_port, preserve_inbound_ports, global_upstream_enabled,
	global_upstream_type, global_upstream_config, global_upstream_ip,
	global_upstream_location, global_upstream_country_code, global_upstream_latency,
	global_upstream_error, created_at, updated_at
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
	err := scanner.Scan(
		&node.ID,
		&node.Name,
		&node.Remark,
		&node.Type,
		&node.Config,
		&node.InboundPort,
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
	)
	return node, err
}

func LoadProxyNodes(ctx context.Context, queryer queryer) ([]ProxyNode, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+ProxyNodeSelectColumns+`
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
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

func LoadRuntimeSettings(ctx context.Context, queryer queryRower) (Settings, error) {
	var settings Settings
	err := queryer.QueryRowContext(ctx, `
		SELECT `+RuntimeSettingsSelectColumns+`
		FROM settings
		WHERE singleton_key = 1
	`).Scan(
		&settings.ID,
		&settings.SingletonKey,
		&settings.StartPort,
		&settings.PreserveInboundPorts,
		&settings.GlobalUpstreamEnabled,
		&settings.GlobalUpstreamType,
		&settings.GlobalUpstreamConfig,
		&settings.GlobalUpstreamIP,
		&settings.GlobalUpstreamLocation,
		&settings.GlobalUpstreamCountryCode,
		&settings.GlobalUpstreamLatency,
		&settings.GlobalUpstreamError,
		&settings.CreatedAt,
		&settings.UpdatedAt,
	)
	return settings, err
}
