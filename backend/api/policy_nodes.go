package api

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

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
	if len(policy.MemberSources) == 0 && len(policy.MemberIDs) == 0 {
		return invalidNodeRequest(errors.New("a policy needs at least one physical member"))
	}
	memberIDs := policy.MemberIDs
	groups := make(map[int][]int)
	if len(policy.MemberSources) > 0 {
		memberIDs = nil
		for _, source := range policy.MemberSources {
			if source.Type == models.PolicyMemberSourceNode {
				memberIDs = append(memberIDs, source.ID)
				continue
			}
			var groupID int
			if err := tx.QueryRowContext(ctx, "SELECT id FROM node_groups WHERE id=?", source.ID).Scan(&groupID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return invalidNodeRequest(fmt.Errorf("invalid member group %d", source.ID))
				}
				return err
			}
			ids, err := loadPhysicalGroupMembers(ctx, tx, source.ID, node.ID)
			if err != nil {
				return err
			}
			groups[source.ID] = ids
		}
	}
	for _, id := range memberIDs {
		var protocol string
		err = tx.QueryRowContext(ctx, "SELECT type FROM proxy_nodes WHERE id=?", id).Scan(&protocol)
		if errors.Is(err, sql.ErrNoRows) || id == node.ID || models.IsPolicy(protocol) {
			return invalidNodeRequest(fmt.Errorf("invalid physical member %d", id))
		}
		if err != nil {
			return err
		}
	}
	if len(policy.MemberSources) > 0 {
		policy.MemberIDs = expandPolicyMembers(policy.MemberSources, groups, node.ID)
	}
	encoded, err := json.Marshal(policy)
	node.Config = string(encoded)
	return err
}

func loadPhysicalGroupMembers(ctx context.Context, tx *sql.Tx, groupID, selfID int) ([]int, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,type FROM proxy_nodes WHERE group_id=? AND id<>? ORDER BY sort_order ASC, id ASC", groupID, selfID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []int
	for rows.Next() {
		var id int
		var protocol string
		if err := rows.Scan(&id, &protocol); err != nil {
			return nil, err
		}
		if !models.IsPolicy(protocol) {
			members = append(members, id)
		}
	}
	return members, rows.Err()
}

func expandPolicyMembers(sources []models.PolicyMemberSource, groups map[int][]int, selfID int) []int {
	members := make([]int, 0)
	seen := map[int]bool{}
	appendMember := func(id int) {
		if id != selfID && !seen[id] {
			members = append(members, id)
			seen[id] = true
		}
	}
	for _, source := range sources {
		if source.Type == models.PolicyMemberSourceNode {
			appendMember(source.ID)
		} else {
			for _, id := range groups[source.ID] {
				appendMember(id)
			}
		}
	}
	return members
}

func reconcilePolicyReferences(ctx context.Context, tx *sql.Tx, nodes []models.ProxyNode) error {
	return reconcilePolicyMembers(ctx, tx, nodes, false)
}

func ReconcileLinkedPolicyMembers(ctx context.Context, db *sql.DB) ([]models.ProxyNode, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nodes, err := models.LoadProxyNodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := reconcilePolicyMembers(ctx, tx, nodes, true); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return nodes, nil
}

func reconcilePolicyMembers(ctx context.Context, tx *sql.Tx, nodes []models.ProxyNode, onlyLinked bool) error {
	groupIDs, err := readIDs(ctx, tx, "SELECT id FROM node_groups")
	if err != nil {
		return err
	}
	existingGroups := make(map[int]bool, len(groupIDs))
	for _, id := range groupIDs {
		existingGroups[id] = true
	}
	types := make(map[int]string, len(nodes))
	sortOrders := make(map[int]int, len(nodes))
	groups := make(map[int][]int)
	for _, node := range nodes {
		types[node.ID] = node.Type
		if !models.IsPolicy(node.Type) {
			sortOrders[node.ID] = node.SortOrder
			groups[node.GroupID] = append(groups[node.GroupID], node.ID)
		}
	}
	for _, ids := range groups {
		slices.SortFunc(ids, func(left, right int) int {
			if order := cmp.Compare(sortOrders[left], sortOrders[right]); order != 0 {
				return order
			}
			return cmp.Compare(left, right)
		})
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
		if onlyLinked && !slices.ContainsFunc(policy.MemberSources, func(source models.PolicyMemberSource) bool {
			return source.Type == models.PolicyMemberSourceGroup
		}) {
			continue
		}
		previousMembers, previousSources := policy.MemberIDs, policy.MemberSources
		hadSources := len(policy.MemberSources) > 0
		if hadSources {
			kept := make([]models.PolicyMemberSource, 0, len(policy.MemberSources))
			hasGroup := false
			for _, source := range policy.MemberSources {
				if source.Type == models.PolicyMemberSourceGroup {
					if !existingGroups[source.ID] {
						continue
					}
					hasGroup = true
				} else {
					protocol, exists := types[source.ID]
					if !exists || source.ID == node.ID {
						continue
					}
					if models.IsPolicy(protocol) {
						return invalidNodeRequest(fmt.Errorf("policy %d references policy %d", node.ID, source.ID))
					}
				}
				kept = append(kept, source)
			}
			if hasGroup {
				policy.MemberIDs = expandPolicyMembers(kept, groups, node.ID)
				policy.MemberSources = kept
			} else {
				policy.MemberSources = nil
			}
		}
		if len(policy.MemberSources) == 0 {
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
			if len(kept) == len(policy.MemberIDs) && !hadSources {
				continue
			}
			policy.MemberIDs = kept
		}
		if onlyLinked && slices.Equal(previousMembers, policy.MemberIDs) && slices.Equal(previousSources, policy.MemberSources) {
			continue
		}
		encoded, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		config := string(encoded)
		if config == node.Config {
			continue
		}
		if _, err = tx.ExecContext(ctx, "UPDATE proxy_nodes SET config=?,updated_at=CURRENT_TIMESTAMP WHERE id=?", config, node.ID); err != nil {
			return err
		}
		node.Config = config
	}
	return nil
}
