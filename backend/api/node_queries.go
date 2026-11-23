package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"sb-proxy/backend/models"
)

const nodeIDsPerQuery = 500

const insertNodeSQL = `
	INSERT INTO proxy_nodes (
		name, remark, type, config, inbound_port, inbound_port_pinned,
		username, password, tcp_reuse_enabled, upstream_mode, upstream_type,
		upstream_config, sort_order, latency, enabled
	)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

const nodeExportColumns = "name, type, config"

func nodeInsertArgs(node models.ProxyNode) []any {
	return []any{
		node.Name, node.Remark, node.Type, node.Config, node.InboundPort, node.InboundPortPinned,
		node.Username, node.Password, node.TCPReuseEnabled, node.UpstreamMode, node.UpstreamType,
		node.UpstreamConfig, node.SortOrder, node.Latency, node.Enabled,
	}
}

func validateNodeIDs(ids []int) error {
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return errors.New("invalid node id")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("duplicate node id")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func nodeIDCondition(ids []int) (string, []any) {
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	return " WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}

func loadNodeIDs(ctx context.Context, tx *sql.Tx, ids []int) (map[int]struct{}, error) {
	query := "SELECT id FROM proxy_nodes"
	var args []any
	if len(ids) != 0 {
		condition, values := nodeIDCondition(ids)
		query += condition
		args = values
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	found := make(map[int]struct{}, len(ids))
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		found[id] = struct{}{}
	}
	return found, rows.Err()
}

func requireNodeIDs(ctx context.Context, tx *sql.Tx, ids []int, reportMissingID bool) error {
	for chunk := range slices.Chunk(ids, nodeIDsPerQuery) {
		found, err := loadNodeIDs(ctx, tx, chunk)
		if err != nil {
			return err
		}
		for _, id := range chunk {
			if _, exists := found[id]; !exists {
				if reportMissingID {
					return nodeRequestError{status: http.StatusNotFound, cause: fmt.Errorf("node %d not found", id)}
				}
				return errNodeNotFound
			}
		}
	}
	return nil
}

func deleteNodesTx(ctx context.Context, tx *sql.Tx, ids []int, reportMissingID bool) error {
	if err := requireNodeIDs(ctx, tx, ids, reportMissingID); err != nil {
		return err
	}
	for chunk := range slices.Chunk(ids, nodeIDsPerQuery) {
		condition, args := nodeIDCondition(chunk)
		if _, err := tx.ExecContext(ctx, "DELETE FROM proxy_nodes"+condition, args...); err != nil {
			return err
		}
	}
	if err := normalizeNodeSortOrderTx(ctx, tx); err != nil {
		return err
	}
	startPort, preserveInboundPorts, err := getPortSettings(ctx, tx)
	if err != nil {
		return err
	}
	if !preserveInboundPorts {
		return reassignAutomaticPortsTx(ctx, tx, startPort)
	}
	return nil
}

func loadNodeForMutation(ctx context.Context, tx *sql.Tx, id int) (models.ProxyNode, error) {
	node, err := models.LoadProxyNode(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return node, errNodeNotFound
	}
	return node, err
}

func loadNodeForExport(ctx context.Context, db *sql.DB, id int) (models.ProxyNode, error) {
	var node models.ProxyNode
	err := db.QueryRowContext(ctx, "SELECT "+nodeExportColumns+" FROM proxy_nodes WHERE id = ?", id).
		Scan(&node.Name, &node.Type, &node.Config)
	return node, err
}

type nodeExportRecord struct {
	node models.ProxyNode
	err  error
}

func loadNodeExportChunk(ctx context.Context, db *sql.DB, ids []int) (map[int]nodeExportRecord, error) {
	condition, args := nodeIDCondition(ids)
	rows, err := db.QueryContext(ctx, "SELECT id, "+nodeExportColumns+" FROM proxy_nodes"+condition, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make(map[int]nodeExportRecord, len(ids))
	for rows.Next() {
		var node models.ProxyNode
		err := rows.Scan(&node.ID, &node.Name, &node.Type, &node.Config)
		nodes[node.ID] = nodeExportRecord{node: node, err: err}
	}
	return nodes, rows.Err()
}
