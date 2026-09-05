package api

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
)

type nodeMutationTable struct {
	name    string
	columns []string
	ids     []int64
	rows    map[int64][]any
}

func captureNodeMutationTables(ctx context.Context, tx *sql.Tx) ([]nodeMutationTable, error) {
	tables := []struct{ name, columns string }{
		{"proxy_nodes", proxyNodeSelectColumns},
		{"settings", runtimeSettingsSelectColumns + ", admin_password, admin_password_env_fingerprint, auth_generation"},
	}
	snapshots := make([]nodeMutationTable, 0, len(tables))
	for _, table := range tables {
		snapshot, err := captureNodeMutationTable(ctx, tx, table.name, table.columns)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func captureNodeMutationTable(ctx context.Context, tx *sql.Tx, name, columns string) (nodeMutationTable, error) {
	rows, err := tx.QueryContext(ctx, "SELECT "+columns+" FROM "+name+" ORDER BY id")
	if err != nil {
		return nodeMutationTable{}, err
	}
	defer rows.Close()
	columnNames, err := rows.Columns()
	if err != nil {
		return nodeMutationTable{}, err
	}
	snapshot := nodeMutationTable{name: name, columns: columnNames, rows: make(map[int64][]any)}
	for rows.Next() {
		values := make([]any, len(columnNames))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		var id int64
		destinations[0] = &id
		if err := rows.Scan(destinations...); err != nil {
			return nodeMutationTable{}, err
		}
		values[0] = id
		for index, value := range values {
			if bytes, ok := value.([]byte); ok {
				values[index] = string(bytes)
			}
		}
		snapshot.ids = append(snapshot.ids, id)
		snapshot.rows[id] = values
	}
	return snapshot, rows.Err()
}

func (coordinator *NodeMutationCoordinator) commitCandidate(
	ctx context.Context,
	before, after []nodeMutationTable,
) error {
	tx, err := coordinator.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := captureNodeMutationTables(ctx, tx)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, before) {
		return fmt.Errorf("node or settings data changed while preparing runtime; retry the request")
	}
	for index := range before {
		if err := applyNodeMutationTable(ctx, tx, before[index], after[index]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func applyNodeMutationTable(ctx context.Context, tx *sql.Tx, before, after nodeMutationTable) error {
	for _, id := range before.ids {
		if _, exists := after.rows[id]; exists {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+before.name+" WHERE id = ?", id); err != nil {
			return err
		}
	}
	for _, id := range after.ids {
		values := after.rows[id]
		previous, exists := before.rows[id]
		if !exists {
			parameters := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
			// 复用预演分配的 ID，保证数据库记录与已验证的内核标签一致。
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+after.name+" ("+strings.Join(after.columns, ",")+") VALUES ("+parameters+")", values...); err != nil {
				return err
			}
			continue
		}
		var assignments []string
		var arguments []any
		for index := 1; index < len(values); index++ {
			if reflect.DeepEqual(previous[index], values[index]) {
				continue
			}
			assignments = append(assignments, after.columns[index]+" = ?")
			arguments = append(arguments, values[index])
		}
		if len(assignments) == 0 {
			continue
		}
		arguments = append(arguments, id)
		if _, err := tx.ExecContext(ctx, "UPDATE "+after.name+" SET "+strings.Join(assignments, ",")+" WHERE id = ?", arguments...); err != nil {
			return err
		}
	}
	return nil
}
