package api

import (
	"context"
	"database/sql"
	"fmt"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

const proxyNodeSelectColumns = `
	id, name, remark, type, config, inbound_port, inbound_port_pinned,
	username, password, tcp_reuse_enabled, upstream_mode, upstream_type,
	upstream_config, upstream_ip, upstream_location, upstream_country_code,
	upstream_latency, upstream_error, sort_order, node_ip, location, country_code, latency,
	enabled, created_at, updated_at
`

const runtimeSettingsSelectColumns = `
	id, singleton_key, start_port, preserve_inbound_ports, global_upstream_enabled,
	global_upstream_type, global_upstream_config, global_upstream_ip,
	global_upstream_location, global_upstream_country_code, global_upstream_latency,
	global_upstream_error, created_at, updated_at
`

type rowScanner interface {
	Scan(dest ...interface{}) error
}

type nodeQueryer interface {
	QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
}

type nodeQueryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}

func scanProxyNode(scanner rowScanner) (models.ProxyNode, error) {
	var node models.ProxyNode
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

func loadAllNodesFrom(ctx context.Context, queryer nodeQueryer) ([]models.ProxyNode, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+proxyNodeSelectColumns+`
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make([]models.ProxyNode, 0)
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
	return nodes, nil
}

func loadNodeByIDFrom(ctx context.Context, queryer nodeQueryRower, id int) (models.ProxyNode, error) {
	return scanProxyNode(queryer.QueryRowContext(ctx, `
		SELECT `+proxyNodeSelectColumns+`
		FROM proxy_nodes
		WHERE id = ?
	`, id))
}

func loadRuntimeSettingsFrom(ctx context.Context, queryer nodeQueryRower) (models.Settings, error) {
	var settings models.Settings
	err := queryer.QueryRowContext(ctx, `
		SELECT `+runtimeSettingsSelectColumns+`
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

type NodeMutationOperation struct {
	ApplyRuntime bool
	Mutate       func(context.Context, *sql.Tx) error
}

type NodeMutationCoordinator struct {
	db             *sql.DB
	singBoxService *services.SingBoxService
	runtimeApplier *services.RuntimeApplier
}

func NewNodeMutationCoordinator(db *sql.DB, singBoxService *services.SingBoxService) *NodeMutationCoordinator {
	return &NodeMutationCoordinator{
		db:             db,
		singBoxService: singBoxService,
		runtimeApplier: services.NewRuntimeApplier(singBoxService),
	}
}

func (coordinator *NodeMutationCoordinator) Execute(
	ctx context.Context,
	operation NodeMutationOperation,
) ([]models.ProxyNode, error) {
	if operation.Mutate == nil {
		return nil, fmt.Errorf("node mutation callback is required")
	}

	tx, err := coordinator.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	if err := operation.Mutate(ctx, tx); err != nil {
		return nil, err
	}

	var candidateNodes []models.ProxyNode
	var runtimePlan *services.RuntimePlan
	if operation.ApplyRuntime {
		candidateNodes, err = loadAllNodesFrom(ctx, tx)
		if err != nil {
			return nil, err
		}
		settings, err := loadRuntimeSettingsFrom(ctx, tx)
		if err != nil {
			return nil, err
		}
		runtimePlan, err = coordinator.runtimeApplier.Prepare(candidateNodes, settings)
		if err != nil {
			return nil, err
		}
	}

	if !operation.ApplyRuntime {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return candidateNodes, nil
	}

	runtimeTransaction, err := coordinator.runtimeApplier.Begin(runtimePlan)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		if rollbackErr := runtimeTransaction.Rollback(); rollbackErr != nil {
			coordinator.singBoxService.MarkDegraded(fmt.Errorf(
				"database commit failed after runtime start: commit=%v rollback=%v",
				err,
				rollbackErr,
			))
			return nil, fmt.Errorf(
				"database commit failed: %v; runtime rollback failed: %v",
				err,
				rollbackErr,
			)
		}
		return nil, fmt.Errorf("database commit failed and runtime was rolled back: %w", err)
	}
	runtimeTransaction.Commit()
	return candidateNodes, nil
}

type nodeOrderPortSnapshot struct {
	ID                int
	SortOrder         int
	InboundPort       int
	InboundPortPinned bool
}

func snapshotNodeOrderPorts(ctx context.Context, tx *sql.Tx) ([]nodeOrderPortSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, sort_order, inbound_port, inbound_port_pinned
		FROM proxy_nodes
		ORDER BY sort_order ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	snapshots := make([]nodeOrderPortSnapshot, 0)
	for rows.Next() {
		var snapshot nodeOrderPortSnapshot
		if err := rows.Scan(
			&snapshot.ID,
			&snapshot.SortOrder,
			&snapshot.InboundPort,
			&snapshot.InboundPortPinned,
		); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return snapshots, nil
}

func normalizeNodeSortOrderTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM proxy_nodes ORDER BY sort_order ASC, id ASC")
	if err != nil {
		return err
	}
	ids := make([]int, 0)
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for sortOrder, id := range ids {
		if _, err := tx.ExecContext(ctx, `
			UPDATE proxy_nodes
			SET sort_order = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, sortOrder, id); err != nil {
			return err
		}
	}
	return nil
}

func reassignAutomaticPortsTx(ctx context.Context, tx *sql.Tx, startPort int) error {
	if err := validateStartPort(startPort); err != nil {
		return err
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT id, inbound_port, inbound_port_pinned
		FROM proxy_nodes
		ORDER BY sort_order ASC, id ASC
	`)
	if err != nil {
		return err
	}
	type portState struct {
		id     int
		port   int
		pinned bool
	}
	states := make([]portState, 0)
	for rows.Next() {
		var state portState
		if err := rows.Scan(&state.id, &state.port, &state.pinned); err != nil {
			rows.Close()
			return err
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	used := make(map[int]struct{}, len(states))
	for _, state := range states {
		if !state.pinned {
			continue
		}
		if err := validateInboundPort(state.port); err != nil {
			return fmt.Errorf("pinned node %d: %w", state.id, err)
		}
		if _, duplicate := used[state.port]; duplicate {
			return fmt.Errorf("duplicate pinned inbound port %d", state.port)
		}
		used[state.port] = struct{}{}
	}

	nextPort := startPort
	for _, state := range states {
		if state.pinned {
			continue
		}
		for nextPort <= 65535 {
			if _, occupied := used[nextPort]; !occupied {
				break
			}
			nextPort++
		}
		if err := validateInboundPort(nextPort); err != nil {
			return fmt.Errorf("node %d: %w", state.id, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE proxy_nodes
			SET inbound_port = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, nextPort, state.id); err != nil {
			return err
		}
		used[nextPort] = struct{}{}
		nextPort++
	}
	return nil
}
