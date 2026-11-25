package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func TestPolicyMemberValidationAndPruning(t *testing.T) {
	client := newGroupTestClient(t)
	pool := client.createGroup("pool", 33001, false)
	first := client.createNode("first", pool.ID, true)
	second := client.createNode("second", pool.ID, true)
	payload := gin.H{"type": "failover", "name": "priority", "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d,%d,%d]}`, first.ID, second.ID, first.ID)}
	var policy models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", payload, 201, &policy)
	if policy.PolicyStatus == nil || !slices.Equal(policy.Effective, []int{first.ID, second.ID}) || policy.UpstreamMode != "none" {
		t.Fatalf("invalid policy response %+v", policy.PolicyStatus)
	}
	var parsed models.PolicyConfig
	if err := json.Unmarshal([]byte(policy.Config), &parsed); err != nil || !slices.Equal(parsed.MemberIDs, policy.Effective) || parsed.Interval != 30 {
		t.Fatal("policy defaults or duplicate removal failed")
	}
	client.request(http.MethodGet, testNodePath(policy.ID)+"/export", nil, 400, nil)
	for _, input := range []string{`{"member_ids":[]}`, `{"member_ids":[-1]}`, `{"member_ids":[9999]}`, fmt.Sprintf(`{"member_ids":[%d]}`, policy.ID)} {
		payload["config"] = input
		client.request(http.MethodPut, testNodePath(policy.ID), payload, 400, nil)
	}
	payload["config"] = policy.Config
	payload["upstream_mode"] = "global"
	client.request(http.MethodPut, testNodePath(policy.ID), payload, 400, nil)
	client.request(http.MethodPatch, testNodePath(first.ID), gin.H{"enabled": false}, 200, nil)
	client.request(http.MethodGet, testNodePath(policy.ID), nil, 200, &policy)
	if !slices.Equal(policy.Effective, []int{second.ID}) || len(policy.Warnings) == 0 {
		t.Fatal("disabled member was not skipped")
	}
	client.request(http.MethodPost, "/api/nodes/batch-delete", gin.H{"ids": []int{second.ID}}, 200, nil)
	client.request(http.MethodGet, testNodePath(policy.ID), nil, 200, &policy)
	if err := json.Unmarshal([]byte(policy.Config), &parsed); err != nil || !slices.Equal(parsed.MemberIDs, []int{first.ID}) {
		t.Fatal("batch deletion lost the wrong member")
	}
	client.request(http.MethodDelete, testGroupPath(pool.ID)+"?mode=delete_nodes", nil, 200, nil)
	client.request(http.MethodGet, testNodePath(policy.ID), nil, 200, &policy)
	if err := json.Unmarshal([]byte(policy.Config), &parsed); err != nil || len(parsed.MemberIDs) != 0 || len(policy.Warnings) == 0 {
		t.Fatal("group deletion left a dangling reference")
	}
}

func createLinkedTestPolicy(client groupTestClient, sources []models.PolicyMemberSource) models.ProxyNode {
	client.t.Helper()
	encoded, err := json.Marshal(models.PolicyConfig{MemberSources: sources})
	if err != nil {
		client.t.Fatal(err)
	}
	var policy models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", gin.H{"type": "failover", "name": "linked-policy", "enabled": true, "config": string(encoded)}, 201, &policy)
	return policy
}

func assertPolicyMembers(client groupTestClient, id int, members []int, sources []models.PolicyMemberSource) models.ProxyNode {
	client.t.Helper()
	var node models.ProxyNode
	client.request(http.MethodGet, testNodePath(id), nil, 200, &node)
	var config models.PolicyConfig
	if err := json.Unmarshal([]byte(node.Config), &config); err != nil || !slices.Equal(config.MemberIDs, members) || !slices.Equal(config.MemberSources, sources) {
		client.t.Fatalf("策略成员不同步：%+v，期望成员 %v、来源 %v，错误 %v", config, members, sources, err)
	}
	return node
}

func TestPolicyGroupSourcesTrackMembershipAndUnlink(t *testing.T) {
	client := newGroupTestClient(t)
	pool := client.createGroup("pool", 33001, false)
	destination := client.createGroup("destination", 34001, false)
	first := client.createNode("first", pool.ID, true)
	second := client.createNode("second", pool.ID, true)
	outside := client.createNode("outside", destination.ID, true)
	if _, err := client.db.Exec("UPDATE proxy_nodes SET sort_order=10 WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	client.request(http.MethodPost, "/api/nodes", gin.H{"type": "failover", "name": "same-group-policy", "group_id": pool.ID, "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d]}`, first.ID)}, 201, nil)
	sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}, {Type: models.PolicyMemberSourceNode, ID: outside.ID}}
	policy := createLinkedTestPolicy(client, append(append([]models.PolicyMemberSource{}, sources...), sources[0]))
	assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID, outside.ID}, sources)
	third := client.createNode("third", pool.ID, true)
	assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID, third.ID, outside.ID}, sources)
	client.request(http.MethodDelete, testNodePath(first.ID), nil, 200, nil)
	assertPolicyMembers(client, policy.ID, []int{second.ID, third.ID, outside.ID}, sources)
	client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{third.ID}, "group_id": destination.ID}, 200, nil)
	assertPolicyMembers(client, policy.ID, []int{second.ID, outside.ID}, sources)
	client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{outside.ID}, "group_id": pool.ID}, 200, nil)
	assertPolicyMembers(client, policy.ID, []int{second.ID, outside.ID}, sources)
	client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": policy.Type, "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d,%d]}`, second.ID, outside.ID)}, 200, nil)
	client.createNode("after-unlink", pool.ID, true)
	client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{second.ID}, "group_id": destination.ID}, 200, nil)
	assertPolicyMembers(client, policy.ID, []int{second.ID, outside.ID}, nil)
}

func TestPolicyGroupSourceValidation(t *testing.T) {
	client := newGroupTestClient(t)
	pool := client.createGroup("pool", 33001, false)
	member := client.createNode("member", pool.ID, true)
	policy := createLinkedTestPolicy(client, []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}})
	for _, source := range []models.PolicyMemberSource{
		{Type: models.PolicyMemberSourceGroup, ID: 99999},
		{Type: models.PolicyMemberSourceNode, ID: 99999},
		{Type: models.PolicyMemberSourceNode, ID: policy.ID},
		{Type: "invalid", ID: member.ID},
	} {
		encoded, err := json.Marshal(models.PolicyConfig{MemberSources: []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}, source}})
		if err != nil {
			t.Fatal(err)
		}
		client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": policy.Type, "enabled": true, "config": string(encoded)}, 400, nil)
	}
}

func TestDeleteLinkedPolicyGroupsIncludingEmpty(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprintf("empty=%t", empty), func(t *testing.T) {
			client := newGroupTestClient(t)
			pool := client.createGroup("deleted", 33001, false)
			remaining := client.createGroup("remaining", 34001, false)
			if !empty {
				client.createNode("moved", pool.ID, true)
			}
			member := client.createNode("remaining-member", remaining.ID, true)
			outside := client.createNode("manual-member", 0, true)
			sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}, {Type: models.PolicyMemberSourceGroup, ID: remaining.ID}, {Type: models.PolicyMemberSourceNode, ID: outside.ID}}
			policy := createLinkedTestPolicy(client, sources)
			path := testGroupPath(pool.ID)
			if !empty {
				path += fmt.Sprintf("?mode=move&target_group_id=%d", outside.GroupID)
			}
			client.request(http.MethodDelete, path, nil, 200, nil)
			assertPolicyMembers(client, policy.ID, []int{member.ID, outside.ID}, sources[1:])
			client.request(http.MethodDelete, testGroupPath(remaining.ID)+"?mode=delete_nodes", nil, 200, nil)
			assertPolicyMembers(client, policy.ID, []int{outside.ID}, nil)
		})
	}
}

func TestDeletingLastLinkedGroupPreservesMovedPhysicalMembers(t *testing.T) {
	client := newGroupTestClient(t)
	pool := client.createGroup("deleted", 33001, false)
	member := client.createNode("moved-member", pool.ID, true)
	policy := createLinkedTestPolicy(client, []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}})
	client.request(http.MethodDelete, testGroupPath(pool.ID)+fmt.Sprintf("?mode=move&target_group_id=%d", policy.GroupID), nil, 200, nil)
	assertPolicyMembers(client, policy.ID, []int{member.ID}, nil)
	client.createNode("new-manual-group-member", policy.GroupID, true)
	assertPolicyMembers(client, policy.ID, []int{member.ID}, nil)
}

func TestPolicyGroupSourcesFollowSubscriptionRefresh(t *testing.T) {
	handler := newTestHandler(t, nil)
	groupID := configureTestSubscription(t, handler, false)
	feed := []services.ImportItem{subscriptionItem("first", "first-test-key"), subscriptionItem("second", "second-test-key")}
	handler.fetchSubscription = func(context.Context, []string, services.BatchImportSourceType) ([]services.ImportItem, []services.ImportFailure, error) {
		return feed, nil, nil
	}
	if _, err := handler.refreshGroupSubscription(t.Context(), groupID, false); err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(models.PolicyConfig{MemberSources: []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: groupID}}})
	if err != nil {
		t.Fatal(err)
	}
	response := postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", gin.H{"name": "subscription-linked", "type": "failover", "enabled": true, "config": string(config)}, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("创建关联策略失败：%s", response.Body.String())
	}
	var policy models.ProxyNode
	if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	firstID := subscriptionTestNodes(t, handler, groupID)["first"].ID
	feed = []services.ImportItem{subscriptionItem("first", "rotated-test-key"), subscriptionItem("third", "third-test-key")}
	if _, err := handler.refreshGroupSubscription(t.Context(), groupID, false); err != nil {
		t.Fatal(err)
	}
	after := subscriptionTestNodes(t, handler, groupID)
	if after["first"].ID != firstID {
		t.Fatal("订阅原地更新改变了节点 ID")
	}
	policy, err = models.LoadProxyNode(t.Context(), handler.db, policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	var parsed models.PolicyConfig
	if err := json.Unmarshal([]byte(policy.Config), &parsed); err != nil || !slices.Equal(parsed.MemberIDs, []int{firstID, after["third"].ID}) || len(parsed.MemberSources) != 1 {
		t.Fatalf("订阅增删未同步策略成员：%+v，%v", parsed, err)
	}
}
