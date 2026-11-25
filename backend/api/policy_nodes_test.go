package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
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
