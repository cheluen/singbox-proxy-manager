package api

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

const proxyNodeSelectColumns = `
	id, name, remark, type, config, inbound_port, inbound_port_pinned,
	username, password, tcp_reuse_enabled, sort_order, node_ip, location,
	country_code, latency, enabled, created_at, updated_at
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

type NodeMutationOperation struct {
	ApplyRuntime bool
	Mutate       func(context.Context, *sql.Tx) error
	Compensate   func(context.Context, *sql.Tx) error
}

type NodeMutationCoordinator struct {
	db             *sql.DB
	singBoxService *services.SingBoxService
}

func NewNodeMutationCoordinator(db *sql.DB, singBoxService *services.SingBoxService) *NodeMutationCoordinator {
	return &NodeMutationCoordinator{db: db, singBoxService: singBoxService}
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
	var candidateConfig []byte
	if operation.ApplyRuntime {
		candidateNodes, err = loadAllNodesFrom(ctx, tx)
		if err != nil {
			return nil, err
		}
		candidateConfig, err = coordinator.singBoxService.BuildGlobalConfig(candidateNodes)
		if err != nil {
			return nil, err
		}
		if err := coordinator.singBoxService.ValidateConfig(candidateConfig); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	if !operation.ApplyRuntime {
		return candidateNodes, nil
	}
	if err := coordinator.singBoxService.ApplyConfig(candidateConfig); err != nil {
		compensationErr := coordinator.compensate(operation.Compensate)
		if compensationErr != nil {
			coordinator.singBoxService.MarkDegraded(fmt.Errorf(
				"node mutation apply failed and database compensation failed: apply=%v compensation=%v",
				err,
				compensationErr,
			))
			return nil, fmt.Errorf(
				"runtime apply failed: %v; database compensation failed: %v",
				err,
				compensationErr,
			)
		}
		return nil, err
	}

	return candidateNodes, nil
}

func (coordinator *NodeMutationCoordinator) compensate(
	compensate func(context.Context, *sql.Tx) error,
) error {
	if compensate == nil {
		return fmt.Errorf("database compensation callback is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := coordinator.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := compensate(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
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

func restoreNodeOrderPorts(
	ctx context.Context,
	tx *sql.Tx,
	snapshots []nodeOrderPortSnapshot,
) error {
	for _, snapshot := range snapshots {
		if _, err := tx.ExecContext(ctx, `
			UPDATE proxy_nodes
			SET sort_order = ?, inbound_port = ?, inbound_port_pinned = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, snapshot.SortOrder, snapshot.InboundPort, snapshot.InboundPortPinned, snapshot.ID); err != nil {
			return err
		}
	}
	return nil
}

func insertProxyNodeSnapshot(ctx context.Context, tx *sql.Tx, node models.ProxyNode) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO proxy_nodes (
			id, name, remark, type, config, inbound_port, inbound_port_pinned,
			username, password, tcp_reuse_enabled, sort_order, node_ip, location,
			country_code, latency, enabled, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, node.ID, node.Name, node.Remark, node.Type, node.Config, node.InboundPort,
		node.InboundPortPinned, node.Username, node.Password, node.TCPReuseEnabled,
		node.SortOrder, node.NodeIP, node.Location, node.CountryCode, node.Latency,
		node.Enabled, node.CreatedAt, node.UpdatedAt)
	return err
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
