package api

import (
	"context"
	"database/sql"
	"fmt"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

type nodeMutationOperation struct {
	ApplyRuntime bool
	Mutate       func(context.Context, *sql.Tx) error
}

type nodeMutationCoordinator struct {
	db             *sql.DB
	singBoxService *services.SingBoxService
	runtimeApplier *services.RuntimeApplier
}

func newNodeMutationCoordinator(db *sql.DB, singBoxService *services.SingBoxService) *nodeMutationCoordinator {
	if db == nil || singBoxService == nil {
		return nil
	}
	return &nodeMutationCoordinator{
		db:             db,
		singBoxService: singBoxService,
		runtimeApplier: services.NewRuntimeApplier(singBoxService),
	}
}

// reapplyRuntime 由调用方持有写锁，只读取节点和设置，不创建数据库事务或快照。
func (coordinator *nodeMutationCoordinator) reapplyRuntime(ctx context.Context) error {
	nodes, err := models.LoadProxyNodes(ctx, coordinator.db)
	if err != nil {
		return err
	}
	settings, err := models.LoadRuntimeSettings(ctx, coordinator.db)
	if err != nil {
		return err
	}
	plan, err := coordinator.runtimeApplier.PrepareContext(ctx, nodes, settings)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 内核事务仍负责配置校验后的启动，以及失败时恢复之前可用的配置和进程。
	transaction, err := coordinator.runtimeApplier.Begin(plan)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		if rollbackErr := transaction.Rollback(); rollbackErr != nil {
			return fmt.Errorf("runtime restart canceled: %w; rollback failed: %v", err, rollbackErr)
		}
		return err
	}
	transaction.Commit()
	return nil
}

func (coordinator *nodeMutationCoordinator) execute(
	ctx context.Context,
	operation nodeMutationOperation,
) ([]models.ProxyNode, error) {
	tx, err := coordinator.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var before []nodeMutationTable
	if operation.ApplyRuntime {
		before, err = captureNodeMutationTables(ctx, tx)
		if err != nil {
			return nil, err
		}
	}

	if err := operation.Mutate(ctx, tx); err != nil {
		return nil, err
	}

	if !operation.ApplyRuntime {
		return nil, tx.Commit()
	}
	candidateNodes, err := models.LoadProxyNodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	settings, err := models.LoadRuntimeSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	after, err := captureNodeMutationTables(ctx, tx)
	if err != nil {
		return nil, err
	}
	// 候选数据只保留在内存中，内核校验、启动和回滚均不持有数据库事务。
	if err := tx.Rollback(); err != nil {
		return nil, err
	}
	runtimePlan, err := coordinator.runtimeApplier.PrepareContext(ctx, candidateNodes, settings)
	if err != nil {
		return nil, err
	}

	runtimeTransaction, err := coordinator.runtimeApplier.Begin(runtimePlan)
	if err != nil {
		return nil, err
	}
	if err := coordinator.commitCandidate(ctx, before, after); err != nil {
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

func normalizeNodeSortOrderTx(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, "SELECT id, sort_order FROM proxy_nodes ORDER BY sort_order ASC, id ASC")
	if err != nil {
		return err
	}
	type sortChange struct {
		id        int
		sortOrder int
	}
	var changes []sortChange
	sortOrder := 0
	for rows.Next() {
		var id, previousOrder int
		if err := rows.Scan(&id, &previousOrder); err != nil {
			rows.Close()
			return err
		}
		if previousOrder != sortOrder {
			changes = append(changes, sortChange{id: id, sortOrder: sortOrder})
		}
		sortOrder++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	for _, change := range changes {
		if _, err := tx.ExecContext(ctx, `
			UPDATE proxy_nodes
			SET sort_order = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ?
		`, change.sortOrder, change.id); err != nil {
			return err
		}
	}
	return nil
}

// reassignAutomaticPortsTx 按排序重分配未固定节点的端口，预留并跳过所有固定端口。
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
		if state.port != nextPort {
			if _, err := tx.ExecContext(ctx, `
				UPDATE proxy_nodes
				SET inbound_port = ?, updated_at = CURRENT_TIMESTAMP
				WHERE id = ?
			`, nextPort, state.id); err != nil {
				return err
			}
		}
		used[nextPort] = struct{}{}
		nextPort++
	}
	return nil
}
