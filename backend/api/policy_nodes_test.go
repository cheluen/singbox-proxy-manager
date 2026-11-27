package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

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

func TestPolicyURLTestToleranceCreateAndEdit(testContext *testing.T) {
	for _, testCase := range []struct {
		name      string
		field     string
		tolerance int
	}{
		{name: "默认", tolerance: 50},
		{name: "null", field: `,"tolerance":null`, tolerance: 50},
		{name: "显式零", field: `,"tolerance":0`, tolerance: 0},
		{name: "合法值", field: `,"tolerance":125`, tolerance: 125},
		{name: "上限", field: `,"tolerance":5000`, tolerance: 5000},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			client := newGroupTestClient(testContext)
			member := client.createNode("member", 0, true)
			config := fmt.Sprintf(`{"member_ids":[%d],"strategy":"consistent-hashing"%s}`, member.ID, testCase.field)
			payload := gin.H{"name": "最低延迟", "type": "urltest", "enabled": true, "config": config}
			var policy models.ProxyNode
			client.request(http.MethodPost, "/api/nodes", payload, http.StatusCreated, &policy)
			assertTolerance := func() {
				testContext.Helper()
				var parsed models.PolicyConfig
				if err := json.Unmarshal([]byte(policy.Config), &parsed); err != nil || parsed.Tolerance == nil || *parsed.Tolerance != testCase.tolerance || parsed.Strategy != "" || parsed.Interval != 30 {
					testContext.Fatalf("API 未归一化 urltest 容差或 strategy：%s，%v", policy.Config, err)
				}
				if policy.PolicyStatus == nil || !slices.Equal(policy.Effective, []int{member.ID}) || policy.UpstreamMode != models.UpstreamModeNone || !strings.Contains(policy.Config, fmt.Sprintf(`"tolerance":%d`, testCase.tolerance)) {
					testContext.Fatalf("API 策略状态或容差 JSON 错误：%+v", policy)
				}
			}
			assertTolerance()
			payload["config"] = fmt.Sprintf(`{"member_ids":[%d],"tolerance":7}`, member.ID)
			client.request(http.MethodPut, testNodePath(policy.ID), payload, http.StatusOK, nil)
			payload["config"] = config
			client.request(http.MethodPut, testNodePath(policy.ID), payload, http.StatusOK, &policy)
			assertTolerance()
		})
	}
}

func TestPolicyURLTestRejectsOutOfRangeTolerance(testContext *testing.T) {
	client := newGroupTestClient(testContext)
	member := client.createNode("member", 0, true)
	policy := createLinkedTestPolicyOfType(client, "urltest", []models.PolicyMemberSource{{Type: models.PolicyMemberSourceNode, ID: member.ID}})
	for _, tolerance := range []int{-1, 5001} {
		payload := gin.H{"name": policy.Name, "type": "urltest", "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d],"tolerance":%d}`, member.ID, tolerance)}
		for _, request := range []struct {
			method string
			path   string
		}{{http.MethodPost, "/api/nodes"}, {http.MethodPut, testNodePath(policy.ID)}} {
			var rejection struct {
				Error string `json:"error"`
			}
			client.request(request.method, request.path, payload, http.StatusBadRequest, &rejection)
			if rejection.Error != "invalid config format" {
				testContext.Fatalf("越界容差未返回统一配置错误：%s", rejection.Error)
			}
		}
		var unchanged models.ProxyNode
		client.request(http.MethodGet, testNodePath(policy.ID), nil, http.StatusOK, &unchanged)
		if unchanged.Config != policy.Config {
			testContext.Fatal("越界编辑改变了原策略配置")
		}
	}
}

func TestPolicyURLTestReferencesNestingAndPruning(testContext *testing.T) {
	client := newGroupTestClient(testContext)
	first := client.createNode("first", 0, true)
	second := client.createNode("second", 0, true)
	policy := createLinkedTestPolicyOfType(client, "urltest", []models.PolicyMemberSource{{Type: models.PolicyMemberSourceNode, ID: first.ID}, {Type: models.PolicyMemberSourceNode, ID: second.ID}})
	for _, protocol := range []string{"failover", "loadbalance", "urltest"} {
		other := createLinkedTestPolicyOfType(client, protocol, []models.PolicyMemberSource{{Type: models.PolicyMemberSourceNode, ID: second.ID}})
		client.request(http.MethodPost, "/api/nodes", gin.H{"name": "nested", "type": protocol, "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d]}`, policy.ID)}, http.StatusBadRequest, nil)
		client.request(http.MethodPost, "/api/nodes", gin.H{"name": "nested-urltest", "type": "urltest", "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d]}`, other.ID)}, http.StatusBadRequest, nil)
		client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": "urltest", "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d]}`, other.ID)}, http.StatusBadRequest, nil)
	}
	for _, config := range []string{`{"member_ids":[]}`, `{"member_ids":[99999]}`, fmt.Sprintf(`{"member_ids":[%d]}`, policy.ID)} {
		client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": "urltest", "enabled": true, "config": config}, http.StatusBadRequest, nil)
	}
	client.request(http.MethodPut, testNodePath(first.ID), gin.H{"name": first.Name, "type": "urltest", "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d]}`, second.ID)}, http.StatusBadRequest, nil)
	var unchanged models.ProxyNode
	client.request(http.MethodGet, testNodePath(first.ID), nil, http.StatusOK, &unchanged)
	if unchanged.Type != first.Type {
		testContext.Fatal("被引用的物理成员被转换成策略节点")
	}
	client.request(http.MethodPatch, testNodePath(first.ID), gin.H{"enabled": false}, http.StatusOK, nil)
	policy = assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID}, nil)
	if !slices.Equal(policy.Effective, []int{second.ID}) || len(policy.Warnings) == 0 {
		testContext.Fatal("urltest 未跳过禁用成员或未保留引用")
	}
	client.request(http.MethodPost, "/api/nodes/batch-delete", gin.H{"ids": []int{second.ID}}, http.StatusOK, nil)
	assertPolicyMembers(client, policy.ID, []int{first.ID}, nil)
	client.request(http.MethodDelete, testNodePath(first.ID), nil, http.StatusOK, nil)
	policy = assertPolicyMembers(client, policy.ID, []int{}, nil)
	if len(policy.Effective) != 0 || len(policy.Warnings) == 0 {
		testContext.Fatal("删除成员后 urltest 留下悬空引用")
	}
}

func TestPolicyURLTestRejectsUpstreamAndShareExport(testContext *testing.T) {
	client := newGroupTestClient(testContext)
	member := client.createNode("member", 0, true)
	policy := createLinkedTestPolicyOfType(client, "urltest", []models.PolicyMemberSource{{Type: models.PolicyMemberSourceNode, ID: member.ID}})
	for _, fields := range []gin.H{
		{"upstream_mode": "global"},
		{"upstream_mode": "group"},
		{"upstream_mode": "legacy"},
		{"upstream_mode": "custom", "upstream_type": "http", "upstream_config": `{"server":"127.0.0.1","server_port":8080}`},
		{"upstream_mode": "none", "upstream_type": "http", "upstream_config": `{"server":"127.0.0.1","server_port":8080}`},
	} {
		payload := gin.H{"name": policy.Name, "type": "urltest", "enabled": true, "config": policy.Config}
		for field, value := range fields {
			payload[field] = value
		}
		client.request(http.MethodPost, "/api/nodes", payload, http.StatusBadRequest, nil)
		client.request(http.MethodPut, testNodePath(policy.ID), payload, http.StatusBadRequest, nil)
		client.request(http.MethodPut, testNodePath(policy.ID)+"/upstream", fields, http.StatusBadRequest, nil)
	}
	var unchanged models.ProxyNode
	client.request(http.MethodGet, testNodePath(policy.ID), nil, http.StatusOK, &unchanged)
	if unchanged.UpstreamMode != models.UpstreamModeNone || unchanged.UpstreamType != "" || unchanged.UpstreamConfig != "" {
		testContext.Fatal("urltest 禁用前置代理失败")
	}
	client.request(http.MethodGet, testNodePath(policy.ID)+"/export", nil, http.StatusBadRequest, nil)
}

func TestPolicyURLTestGroupSyncAndUnlink(testContext *testing.T) {
	client := newGroupTestClient(testContext)
	pool := client.createGroup("pool", 33001, false)
	destination := client.createGroup("destination", 34001, false)
	first := client.createNode("first", pool.ID, true)
	second := client.createNode("second", pool.ID, true)
	disabled := client.createNode("disabled", pool.ID, false)
	outside := client.createNode("outside", destination.ID, true)
	var excluded models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", gin.H{"name": "excluded-policy", "type": "failover", "group_id": pool.ID, "enabled": true, "config": fmt.Sprintf(`{"member_ids":[%d]}`, first.ID)}, http.StatusCreated, &excluded)
	sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}, {Type: models.PolicyMemberSourceNode, ID: outside.ID}}
	policy := createLinkedTestPolicyOfType(client, "urltest", sources)
	policy = assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID, disabled.ID, outside.ID}, sources)
	if !slices.Equal(policy.Effective, []int{first.ID, second.ID, outside.ID}) {
		testContext.Fatal("整组 urltest 未跳过策略节点或禁用物理成员")
	}
	reorderPolicyTestGroup(client, pool.ID, second.ID, first.ID, disabled.ID, excluded.ID)
	assertPolicyMembers(client, policy.ID, []int{second.ID, first.ID, disabled.ID, outside.ID}, sources)
	third := client.createNode("third", pool.ID, true)
	assertPolicyMembers(client, policy.ID, []int{second.ID, first.ID, disabled.ID, third.ID, outside.ID}, sources)
	client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{first.ID}, "group_id": destination.ID}, http.StatusOK, nil)
	assertPolicyMembers(client, policy.ID, []int{second.ID, disabled.ID, third.ID, outside.ID}, sources)
	client.request(http.MethodDelete, testNodePath(disabled.ID), nil, http.StatusOK, nil)
	policy = assertPolicyMembers(client, policy.ID, []int{second.ID, third.ID, outside.ID}, sources)
	var config models.PolicyConfig
	if err := json.Unmarshal([]byte(policy.Config), &config); err != nil {
		testContext.Fatal(err)
	}
	config.MemberSources = nil
	encoded, err := json.Marshal(config)
	if err != nil {
		testContext.Fatal(err)
	}
	client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": "urltest", "enabled": true, "config": string(encoded)}, http.StatusOK, nil)
	afterUnlink := client.createNode("after-unlink", pool.ID, true)
	client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{second.ID}, "group_id": destination.ID}, http.StatusOK, nil)
	reorderPolicyTestGroup(client, pool.ID, afterUnlink.ID, third.ID, excluded.ID)
	assertPolicyMembers(client, policy.ID, []int{second.ID, third.ID, outside.ID}, nil)
	config.MemberSources = sources
	encoded, err = json.Marshal(config)
	if err != nil {
		testContext.Fatal(err)
	}
	client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": "urltest", "enabled": true, "config": string(encoded)}, http.StatusOK, nil)
	assertPolicyMembers(client, policy.ID, []int{afterUnlink.ID, third.ID, outside.ID}, sources)
	client.request(http.MethodDelete, testGroupPath(pool.ID)+"?mode=delete_nodes", nil, http.StatusOK, nil)
	policy = assertPolicyMembers(client, policy.ID, []int{outside.ID}, nil)
	if err := json.Unmarshal([]byte(policy.Config), &config); err != nil || config.Tolerance == nil || *config.Tolerance != models.DefaultPolicyTolerance {
		testContext.Fatalf("整组同步或断链丢失 urltest 容差：%s，%v", policy.Config, err)
	}
}

func createLinkedTestPolicy(client groupTestClient, sources []models.PolicyMemberSource) models.ProxyNode {
	return createLinkedTestPolicyOfType(client, "failover", sources)
}

func createLinkedTestPolicyOfType(client groupTestClient, protocol string, sources []models.PolicyMemberSource) models.ProxyNode {
	client.t.Helper()
	encoded, err := json.Marshal(models.PolicyConfig{MemberSources: sources})
	if err != nil {
		client.t.Fatal(err)
	}
	var policy models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", gin.H{"type": protocol, "name": "linked-policy", "enabled": true, "config": string(encoded)}, 201, &policy)
	return policy
}

func reorderPolicyTestGroup(client groupTestClient, groupID int, ids ...int) {
	client.t.Helper()
	nodes := make([]map[string]int, 0, len(ids))
	for order, id := range ids {
		nodes = append(nodes, map[string]int{"id": id, "sort_order": order})
	}
	client.request(http.MethodPost, "/api/nodes/reorder", gin.H{"group_id": groupID, "nodes": nodes}, http.StatusOK, nil)
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
	assertPolicyMembers(client, policy.ID, []int{second.ID, first.ID, outside.ID}, sources)
	third := client.createNode("third", pool.ID, true)
	assertPolicyMembers(client, policy.ID, []int{second.ID, first.ID, third.ID, outside.ID}, sources)
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

func TestPolicyGroupMembersUseSortOrderAndIDWithUnorderedInput(t *testing.T) {
	client := newGroupTestClient(t)
	pool := client.createGroup("pool", 33001, false)
	sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}}
	config, err := json.Marshal(models.PolicyConfig{MemberIDs: []int{10, 40, 30, 20}, MemberSources: sources, Interval: 30})
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range []models.ProxyNode{
		{ID: 40, Type: "direct", Config: "{}", SortOrder: 1, Enabled: true},
		{ID: 10, Type: "direct", Config: "{}", SortOrder: 2, Enabled: true},
		{ID: 30, Type: "direct", Config: "{}", SortOrder: 1, Enabled: false},
		{ID: 20, Type: "direct", Config: "{}", SortOrder: 0, Enabled: true},
		{ID: 99, Type: "failover", Config: string(config), SortOrder: -1, Enabled: true},
	} {
		if _, err := client.db.Exec(`INSERT INTO proxy_nodes(id,group_id,name,type,config,sort_order,enabled) VALUES (?,?,?,?,?,?,?)`, node.ID, pool.ID, fmt.Sprintf("node-%d", node.ID), node.Type, node.Config, node.SortOrder, node.Enabled); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := client.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, test := range []struct {
		selfID int
		want   []int
	}{
		{selfID: 99, want: []int{20, 30, 40, 10}},
		{selfID: 20, want: []int{30, 40, 10}},
	} {
		members, err := loadPhysicalGroupMembers(t.Context(), tx, pool.ID, test.selfID)
		if err != nil || !slices.Equal(members, test.want) {
			t.Fatalf("组成员顺序错误：self=%d，members=%v，want=%v，err=%v", test.selfID, members, test.want, err)
		}
	}
	nodes, err := models.LoadProxyNodes(t.Context(), tx)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(nodes)
	policy, err := models.LoadProxyNode(t.Context(), tx, 99)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePolicyMembers(t.Context(), tx, &policy); err != nil {
		t.Fatal(err)
	}
	var validated models.PolicyConfig
	if err := json.Unmarshal([]byte(policy.Config), &validated); err != nil || !slices.Equal(validated.MemberIDs, []int{20, 30, 40, 10}) {
		t.Fatalf("创建校验未按组内顺序展开：%+v，%v", validated, err)
	}
	if err := reconcilePolicyReferences(t.Context(), tx, nodes); err != nil {
		t.Fatal(err)
	}
	policy, err = models.LoadProxyNode(t.Context(), tx, 99)
	if err != nil {
		t.Fatal(err)
	}
	var reconciled models.PolicyConfig
	if err := json.Unmarshal([]byte(policy.Config), &reconciled); err != nil || !slices.Equal(reconciled.MemberIDs, []int{20, 30, 40, 10}) || !slices.Equal(reconciled.MemberSources, sources) {
		t.Fatalf("未排序输入导致对账顺序错误：%+v，%v", reconciled, err)
	}
}

func TestPolicyGroupReorderBeforeAndAfterCreation(t *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance", "urltest"} {
		for _, createAfterReorder := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/create_after_reorder=%t", protocol, createAfterReorder), func(t *testing.T) {
				client := newGroupTestClient(t)
				pool := client.createGroup("pool", 33001, false)
				first := client.createNode("first", pool.ID, true)
				second := client.createNode("second", pool.ID, true)
				disabled := client.createNode("disabled", pool.ID, false)
				sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}}
				var policy models.ProxyNode
				if !createAfterReorder {
					policy = createLinkedTestPolicyOfType(client, protocol, sources)
					assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID, disabled.ID}, sources)
				}
				reorderPolicyTestGroup(client, pool.ID, second.ID, disabled.ID, first.ID)
				if createAfterReorder {
					policy = createLinkedTestPolicyOfType(client, protocol, sources)
				}
				policy = assertPolicyMembers(client, policy.ID, []int{second.ID, disabled.ID, first.ID}, sources)
				if !slices.Equal(policy.Effective, []int{second.ID, first.ID}) {
					t.Fatalf("有效成员顺序错误或未跳过禁用成员：%v", policy.Effective)
				}
				reorderPolicyTestGroup(client, pool.ID, first.ID, second.ID, disabled.ID)
				assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID, disabled.ID}, sources)
				for _, previous := range []models.ProxyNode{first, second, disabled} {
					current, err := models.LoadProxyNode(t.Context(), client.db, previous.ID)
					if err != nil || current.ID != previous.ID || current.InboundPort != previous.InboundPort || current.InboundPortPinned != previous.InboundPortPinned || current.Username != previous.Username || current.Password != previous.Password {
						t.Fatalf("策略排序改变了成员身份、端口或凭据：id=%d，err=%v", previous.ID, err)
					}
				}
			})
		}
	}
}

func TestPolicyGroupReorderPreservesMixedSourcesAndManualPriority(t *testing.T) {
	client := newGroupTestClient(t)
	firstGroup := client.createGroup("first-group", 33001, false)
	secondGroup := client.createGroup("second-group", 34001, false)
	first := client.createNode("first", firstGroup.ID, true)
	second := client.createNode("second", firstGroup.ID, true)
	third := client.createNode("third", secondGroup.ID, true)
	fourth := client.createNode("fourth", secondGroup.ID, true)
	reorderPolicyTestGroup(client, firstGroup.ID, second.ID, first.ID)
	reorderPolicyTestGroup(client, secondGroup.ID, fourth.ID, third.ID)
	sources := []models.PolicyMemberSource{
		{Type: models.PolicyMemberSourceNode, ID: first.ID},
		{Type: models.PolicyMemberSourceGroup, ID: secondGroup.ID},
		{Type: models.PolicyMemberSourceNode, ID: fourth.ID},
		{Type: models.PolicyMemberSourceGroup, ID: firstGroup.ID},
		{Type: models.PolicyMemberSourceNode, ID: third.ID},
	}
	policy := createLinkedTestPolicy(client, sources)
	assertPolicyMembers(client, policy.ID, []int{first.ID, fourth.ID, third.ID, second.ID}, sources)
	reorderPolicyTestGroup(client, firstGroup.ID, first.ID, second.ID)
	reorderPolicyTestGroup(client, secondGroup.ID, third.ID, fourth.ID)
	assertPolicyMembers(client, policy.ID, []int{first.ID, third.ID, fourth.ID, second.ID}, sources)
	groups, err := models.LoadGroups(t.Context(), client.db)
	if err != nil {
		t.Fatal(err)
	}
	groupIDs := make([]int, 0, len(groups))
	for _, group := range groups {
		groupIDs = append(groupIDs, group.ID)
	}
	slices.Reverse(groupIDs)
	client.request(http.MethodPost, "/api/node-groups/reorder", gin.H{"ids": groupIDs}, http.StatusOK, nil)
	assertPolicyMembers(client, policy.ID, []int{first.ID, third.ID, fourth.ID, second.ID}, sources)
	sources = []models.PolicyMemberSource{
		{Type: models.PolicyMemberSourceGroup, ID: firstGroup.ID},
		{Type: models.PolicyMemberSourceNode, ID: fourth.ID},
		{Type: models.PolicyMemberSourceGroup, ID: secondGroup.ID},
		{Type: models.PolicyMemberSourceNode, ID: first.ID},
	}
	encoded, err := json.Marshal(models.PolicyConfig{MemberSources: sources})
	if err != nil {
		t.Fatal(err)
	}
	client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": policy.Type, "enabled": true, "config": string(encoded)}, http.StatusOK, nil)
	assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID, fourth.ID, third.ID}, sources)
}

func TestPolicyGroupMoveAppendsLowerIDAndPreservesManualMembers(t *testing.T) {
	client := newGroupTestClient(t)
	source := client.createGroup("source", 33001, false)
	destination := client.createGroup("destination", 34001, false)
	moved := client.createNode("lower-id", source.ID, true)
	first := client.createNode("destination-first", destination.ID, true)
	second := client.createNode("destination-second", destination.ID, true)
	sourceSources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: source.ID}}
	destinationSources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: destination.ID}}
	sourcePolicy := createLinkedTestPolicy(client, sourceSources)
	destinationPolicy := createLinkedTestPolicy(client, destinationSources)
	manualPolicy := createLinkedTestPolicy(client, []models.PolicyMemberSource{{Type: models.PolicyMemberSourceNode, ID: moved.ID}})
	client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{moved.ID}, "group_id": destination.ID}, http.StatusOK, nil)
	assertPolicyMembers(client, sourcePolicy.ID, []int{}, sourceSources)
	assertPolicyMembers(client, destinationPolicy.ID, []int{first.ID, second.ID, moved.ID}, destinationSources)
	assertPolicyMembers(client, manualPolicy.ID, []int{moved.ID}, nil)
	for _, target := range []int{source.ID, destination.ID} {
		client.request(http.MethodPut, testNodePath(moved.ID), gin.H{"name": moved.Name, "type": moved.Type, "config": moved.Config, "enabled": true, "group_id": target}, http.StatusOK, nil)
		wantSource, wantDestination := []int{moved.ID}, []int{first.ID, second.ID}
		if target == destination.ID {
			wantSource, wantDestination = []int{}, []int{first.ID, second.ID, moved.ID}
		}
		assertPolicyMembers(client, sourcePolicy.ID, wantSource, sourceSources)
		assertPolicyMembers(client, destinationPolicy.ID, wantDestination, destinationSources)
		assertPolicyMembers(client, manualPolicy.ID, []int{moved.ID}, nil)
	}
}

func TestPolicyGroupUnlinkFreezesOrderAndRelinkTracksLatestOrder(t *testing.T) {
	client := newGroupTestClient(t)
	pool := client.createGroup("pool", 33001, false)
	first := client.createNode("first", pool.ID, true)
	second := client.createNode("second", pool.ID, true)
	third := client.createNode("third", pool.ID, true)
	reorderPolicyTestGroup(client, pool.ID, third.ID, first.ID, second.ID)
	sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}}
	policy := createLinkedTestPolicy(client, sources)
	policy = assertPolicyMembers(client, policy.ID, []int{third.ID, first.ID, second.ID}, sources)
	var config models.PolicyConfig
	if err := json.Unmarshal([]byte(policy.Config), &config); err != nil {
		t.Fatal(err)
	}
	config.MemberSources = nil
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": policy.Type, "enabled": true, "config": string(encoded)}, http.StatusOK, nil)
	reorderPolicyTestGroup(client, pool.ID, second.ID, third.ID, first.ID)
	fourth := client.createNode("after-unlink", pool.ID, true)
	assertPolicyMembers(client, policy.ID, []int{third.ID, first.ID, second.ID}, nil)
	config.MemberSources = sources
	encoded, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	client.request(http.MethodPut, testNodePath(policy.ID), gin.H{"name": policy.Name, "type": policy.Type, "enabled": true, "config": string(encoded)}, http.StatusOK, nil)
	assertPolicyMembers(client, policy.ID, []int{second.ID, third.ID, first.ID, fourth.ID}, sources)
	reorderPolicyTestGroup(client, pool.ID, fourth.ID, first.ID, third.ID, second.ID)
	assertPolicyMembers(client, policy.ID, []int{fourth.ID, first.ID, third.ID, second.ID}, sources)
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
	initial := subscriptionTestNodes(t, handler, groupID)
	firstID, secondID := initial["first"].ID, initial["second"].ID
	response := postJSON(t, handler.ReorderNodes, http.MethodPost, "/api/nodes/reorder", gin.H{
		"group_id": groupID,
		"nodes":    []map[string]int{{"id": secondID, "sort_order": 0}, {"id": firstID, "sort_order": 1}},
	}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("订阅节点排序失败：%s", response.Body.String())
	}
	config, err := json.Marshal(models.PolicyConfig{MemberSources: []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: groupID}}})
	if err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", gin.H{"name": "subscription-linked", "type": "failover", "enabled": true, "config": string(config)}, nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("创建关联策略失败：%s", response.Body.String())
	}
	var policy models.ProxyNode
	if err := json.Unmarshal(response.Body.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	feed = []services.ImportItem{subscriptionItem("first", "rotated-test-key"), subscriptionItem("second", "second-test-key"), subscriptionItem("third", "third-test-key")}
	if _, err := handler.refreshGroupSubscription(t.Context(), groupID, false); err != nil {
		t.Fatal(err)
	}
	updated := subscriptionTestNodes(t, handler, groupID)
	policy, err = models.LoadProxyNode(t.Context(), handler.db, policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	var parsed models.PolicyConfig
	if err := json.Unmarshal([]byte(policy.Config), &parsed); err != nil || !slices.Equal(parsed.MemberIDs, []int{secondID, firstID, updated["third"].ID}) || !slices.Equal(handler.singBoxService.WithPolicyStatus(policy).Effective, parsed.MemberIDs) {
		t.Fatalf("订阅更新覆盖了拖拽顺序或未同步运行时：%+v，%v", parsed, err)
	}
	if updated["first"].ID != firstID || updated["second"].ID != secondID || updated["first"].SortOrder != 1 || updated["second"].SortOrder != 0 {
		t.Fatal("订阅更新改变了已有节点身份或排序")
	}
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
	if err := json.Unmarshal([]byte(policy.Config), &parsed); err != nil || !slices.Equal(parsed.MemberIDs, []int{firstID, after["third"].ID}) || len(parsed.MemberSources) != 1 {
		t.Fatalf("订阅增删未同步策略成员：%+v，%v", parsed, err)
	}
}

func TestPolicyGroupReorderCommitFailureRestoresMembersAndRuntime(t *testing.T) {
	client := newGroupTestClient(t)
	injectCommitFailure := false
	service := services.NewSingBoxService(t.TempDir(), services.WithLastGoodSnapshotWriter(func(path string, data []byte) error {
		if injectCommitFailure {
			if _, err := client.db.Exec(`CREATE TRIGGER reject_policy_reorder BEFORE UPDATE OF sort_order ON proxy_nodes BEGIN SELECT RAISE(ABORT, 'injected priority commit failure'); END`); err != nil {
				return err
			}
			injectCommitFailure = false
		}
		return os.WriteFile(path, data, 0o600)
	}))
	t.Cleanup(func() { _ = service.Stop() })
	client.handler.singBoxService = service
	client.handler.nodeMutations = newNodeMutationCoordinator(client.db, service)
	pool := client.createGroup("pool", 33001, false)
	first := client.createNode("first", pool.ID, true)
	second := client.createNode("second", pool.ID, true)
	sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}}
	policy := createLinkedTestPolicy(client, sources)
	beforeNodes, err := models.LoadProxyNodes(t.Context(), client.db)
	if err != nil {
		t.Fatal(err)
	}
	beforeConfig, err := os.ReadFile(service.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	beforeSnapshot, err := os.ReadFile(service.ConfigPath() + ".last-good")
	if err != nil {
		t.Fatal(err)
	}
	beforeStatus := service.RuntimeStatus()
	injectCommitFailure = true
	client.request(http.MethodPost, "/api/nodes/reorder", gin.H{
		"group_id": pool.ID,
		"nodes":    []map[string]int{{"id": second.ID, "sort_order": 0}, {"id": first.ID, "sort_order": 1}},
	}, http.StatusInternalServerError, nil)
	if injectCommitFailure {
		t.Fatal("未进入运行时启动后的最终数据库提交阶段")
	}
	afterNodes, err := models.LoadProxyNodes(t.Context(), client.db)
	if err != nil || !reflect.DeepEqual(beforeNodes, afterNodes) {
		t.Fatalf("失败的排序或策略成员配置泄漏到数据库：err=%v", err)
	}
	afterConfig, err := os.ReadFile(service.ConfigPath())
	if err != nil || !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatalf("失败的排序未恢复运行配置：err=%v", err)
	}
	afterSnapshot, err := os.ReadFile(service.ConfigPath() + ".last-good")
	if err != nil || !bytes.Equal(beforeSnapshot, afterSnapshot) {
		t.Fatalf("失败的排序未恢复最后可用快照：err=%v", err)
	}
	afterStatus := service.RuntimeStatus()
	if !afterStatus.Running || afterStatus.Degraded || afterStatus.ActiveConfigHash != beforeStatus.ActiveConfigHash || afterStatus.DesiredConfigHash != beforeStatus.DesiredConfigHash {
		t.Fatalf("最终提交失败未恢复原运行时：%+v", afterStatus)
	}
	policy = assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID}, sources)
	if !slices.Equal(policy.Effective, []int{first.ID, second.ID}) {
		t.Fatalf("回滚后有效成员顺序错误：%v", policy.Effective)
	}
}

func policyLiveHTTPMember(t *testing.T, peerIP string) (*httptest.Server, string) {
	t.Helper()
	dialer := net.Dialer{Timeout: time.Second, LocalAddr: &net.TCPAddr{IP: net.ParseIP(peerIP)}}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			http.Error(writer, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		remote, err := dialer.DialContext(request.Context(), "tcp", request.Host)
		if err != nil {
			http.Error(writer, "unavailable", http.StatusServiceUnavailable)
			return
		}
		defer remote.Close()
		connection, buffer, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer connection.Close()
		if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := buffer.Flush(); err != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(remote, buffer)
			_ = remote.(*net.TCPConn).CloseWrite()
			close(done)
		}()
		_, _ = io.Copy(connection, remote)
		_ = connection.Close()
		_ = remote.Close()
		<-done
	}))
	t.Cleanup(server.Close)
	return server, fmt.Sprintf(`{"server":"127.0.0.1","server_port":%d}`, server.Listener.Addr().(*net.TCPAddr).Port)
}

func TestPolicyGroupReorderRealKernel(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("未设置 SINGBOX_TEST_BINARY，真实转发测试在 Codespace 或 CI 执行")
	}
	client := newGroupTestClient(t)
	t.Setenv("SINGBOX_BINARY", binary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "100ms")
	if _, err := client.db.Exec("UPDATE node_groups SET allocate_port=FALSE"); err != nil {
		t.Fatal(err)
	}
	pool := client.createGroup("loopback-members", 33001, false)
	_, firstConfig := policyLiveHTTPMember(t, "127.0.0.2")
	secondServer, secondConfig := policyLiveHTTPMember(t, "127.0.0.3")
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		peer, _, err := net.SplitHostPort(request.RemoteAddr)
		if err != nil {
			http.Error(writer, "bad peer", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"ip":%q}`, peer)
	}))
	t.Cleanup(origin.Close)
	t.Setenv("SBPM_IPCHECK_URLS", origin.URL)
	var first, second models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", gin.H{"group_id": pool.ID, "name": "first", "type": "http", "config": firstConfig, "enabled": true}, http.StatusCreated, &first)
	client.request(http.MethodPost, "/api/nodes", gin.H{"group_id": pool.ID, "name": "second", "type": "http", "config": secondConfig, "enabled": true}, http.StatusCreated, &second)
	sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: pool.ID}}
	policy := createLinkedTestPolicy(client, sources)
	assertExit := func(wantIP string, wantActive int) {
		t.Helper()
		info, err := client.handler.singBoxService.CheckPolicyIP(t.Context(), policy.ID, services.CheckProxyIPContext)
		if err != nil || info == nil || info.IP != wantIP {
			t.Fatalf("真实策略出口错误：info=%+v，want=%s，err=%v", info, wantIP, err)
		}
		current := client.handler.singBoxService.WithPolicyStatus(policy)
		if current.Active != wantActive {
			t.Fatalf("真实策略活跃成员错误：got=%d，want=%d", current.Active, wantActive)
		}
	}
	assertExit("127.0.0.2", first.ID)
	reorderPolicyTestGroup(client, pool.ID, second.ID, first.ID)
	policy = assertPolicyMembers(client, policy.ID, []int{second.ID, first.ID}, sources)
	assertExit("127.0.0.3", second.ID)
	secondServer.Close()
	assertExit("127.0.0.2", first.ID)
	if err := client.handler.singBoxService.Stop(); err != nil {
		t.Fatal(err)
	}
}
