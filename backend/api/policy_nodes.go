package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sb-proxy/backend/models"
)

func validatePolicyMembers(ctx context.Context, tx *sql.Tx, node *models.ProxyNode) error {
	if !models.IsPolicy(node.Type) {
		return nil
	}
	parsed, err := node.ParseConfig()
	if err != nil {
		return invalidNodeRequest(err)
	}
	policy := parsed.(*models.PolicyConfig)
	if len(policy.MemberIDs) == 0 {
		return invalidNodeRequest(errors.New("a policy needs at least one physical member"))
	}
	for _, id := range policy.MemberIDs {
		var protocol string
		err = tx.QueryRowContext(ctx, "SELECT type FROM proxy_nodes WHERE id=?", id).Scan(&protocol)
		if errors.Is(err, sql.ErrNoRows) || id == node.ID || models.IsPolicy(protocol) {
			return invalidNodeRequest(fmt.Errorf("invalid physical member %d", id))
		}
		if err != nil {
			return err
		}
	}
	encoded, err := json.Marshal(policy)
	node.Config = string(encoded)
	return err
}

func reconcilePolicyReferences(ctx context.Context, tx *sql.Tx, nodes []models.ProxyNode) error {
	types := make(map[int]string, len(nodes))
	for _, node := range nodes {
		types[node.ID] = node.Type
	}
	for index := range nodes {
		node := &nodes[index]
		if !models.IsPolicy(node.Type) {
			continue
		}
		parsed, err := node.ParseConfig()
		if err != nil {
			return err
		}
		policy := parsed.(*models.PolicyConfig)
		kept := make([]int, 0, len(policy.MemberIDs))
		for _, id := range policy.MemberIDs {
			protocol, exists := types[id]
			if !exists {
				continue
			}
			if models.IsPolicy(protocol) {
				return invalidNodeRequest(fmt.Errorf("policy %d references policy %d", node.ID, id))
			}
			kept = append(kept, id)
		}
		if len(kept) == len(policy.MemberIDs) {
			continue
		}
		policy.MemberIDs = kept
		encoded, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		node.Config = string(encoded)
		if _, err = tx.ExecContext(ctx, "UPDATE proxy_nodes SET config=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", node.Config, node.ID); err != nil {
			return err
		}
	}
	return nil
}
