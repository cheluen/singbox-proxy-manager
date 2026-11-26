package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
)

type groupTestClient struct {
	t       *testing.T
	db      *sql.DB
	router  *gin.Engine
	handler *Handler
}

func newGroupTestClient(t *testing.T) groupTestClient {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/api/node-groups", handler.GetGroups)
	router.GET("/api/node-groups/:id", handler.GetGroup)
	router.POST("/api/node-groups", handler.CreateGroup)
	router.PATCH("/api/node-groups/:id", handler.UpdateGroup)
	router.DELETE("/api/node-groups/:id", handler.DeleteGroup)
	router.POST("/api/node-groups/reorder", handler.ReorderGroups)
	router.GET("/api/nodes", handler.GetNodes)
	router.GET("/api/nodes/:id", handler.GetNode)
	router.GET("/api/nodes/:id/export", handler.ExportNode)
	router.DELETE("/api/nodes/:id", handler.DeleteNode)
	router.POST("/api/nodes/batch-delete", handler.BatchDeleteNodes)
	router.POST("/api/nodes", handler.CreateNode)
	router.PUT("/api/nodes/:id", handler.UpdateNode)
	router.PUT("/api/nodes/:id/upstream", handler.UpdateNodeUpstream)
	router.PATCH("/api/nodes/:id", handler.UpdateNodeFlags)
	router.PUT("/api/nodes/:id/port-pin", handler.SetNodeInboundPortPinned)
	router.POST("/api/nodes/batch-move", handler.BatchMoveNodes)
	router.POST("/api/nodes/batch-import", handler.BatchImportNodes)
	router.POST("/api/nodes/reorder", handler.ReorderNodes)
	router.GET("/api/settings", handler.GetSettings)
	router.PUT("/api/settings", handler.UpdateSettings)
	return groupTestClient{t: t, db: handler.db, router: router, handler: handler}
}

func (client groupTestClient) request(method, path string, payload any, wantStatus int, result any) {
	client.t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			client.t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	client.router.ServeHTTP(recorder, request)
	if recorder.Code != wantStatus {
		client.t.Fatalf("%s %s: status %d, want %d: %s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	if result != nil {
		if err := json.Unmarshal(recorder.Body.Bytes(), result); err != nil {
			client.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

func TestGroupAllocatorSkipsListeningUDPPort(t *testing.T) {
	listener, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	occupied := listener.LocalAddr().(*net.UDPAddr).Port
	handler := newTestHandler(t, nil)
	handler.skipPortAvailabilityCheck = false
	if err := handler.checkPort(t.Context(), occupied); err == nil {
		t.Fatal("allocator accepted an occupied UDP port")
	}
	owned := context.WithValue(t.Context(), ownedPortKey{}, func() map[int]bool { return map[int]bool{occupied: true} })
	if err := handler.checkPort(owned, occupied); err != nil {
		t.Fatalf("allocator rejected a running node's UDP port: %v", err)
	}
}

func testGroupPath(id int) string { return "/api/node-groups/" + strconv.Itoa(id) }
func testNodePath(id int) string  { return "/api/nodes/" + strconv.Itoa(id) }

func (client groupTestClient) createGroup(name string, startPort int, allocate bool) models.NodeGroup {
	client.t.Helper()
	var group models.NodeGroup
	client.request(http.MethodPost, "/api/node-groups", gin.H{"name": name, "start_port": startPort, "allocate_port": allocate}, http.StatusCreated, &group)
	return group
}

func (client groupTestClient) createNode(name string, groupID int, enabled bool) models.ProxyNode {
	client.t.Helper()
	payload := gin.H{"name": name, "type": "direct", "config": "{}", "enabled": enabled}
	if groupID > 0 {
		payload["group_id"] = groupID
	}
	var node models.ProxyNode
	client.request(http.MethodPost, "/api/nodes", payload, http.StatusCreated, &node)
	return node
}

func (client groupTestClient) assertNodeOrder(groupID int, ids ...int) {
	client.t.Helper()
	path := "/api/nodes"
	if groupID > 0 {
		path += "?group_id=" + strconv.Itoa(groupID)
	}
	var nodes []models.ProxyNode
	client.request(http.MethodGet, path, nil, http.StatusOK, &nodes)
	if nodes == nil || len(nodes) != len(ids) {
		client.t.Fatalf("%s returned %d nodes, want %v", path, len(nodes), ids)
	}
	for order, node := range nodes {
		if node.ID != ids[order] || (groupID > 0 && (node.GroupID != groupID || node.SortOrder != order)) {
			client.t.Fatalf("%s index %d: id=%d group=%d order=%d, want IDs %v", path, order, node.ID, node.GroupID, node.SortOrder, ids)
		}
	}
}

func (client groupTestClient) assertPort(id, groupID, port int, pinned bool) {
	client.t.Helper()
	var node struct {
		GroupID int  `json:"group_id"`
		Port    *int `json:"inbound_port"`
		Pinned  bool `json:"inbound_port_pinned"`
	}
	client.request(http.MethodGet, testNodePath(id), nil, http.StatusOK, &node)
	if node.Port == nil {
		client.t.Fatalf("node %d has no numeric inbound_port in its API response", id)
	}
	if node.GroupID != groupID || *node.Port != port || node.Pinned != pinned {
		client.t.Fatalf("node %d: group=%d port=%d pinned=%t, want group=%d port=%d pinned=%t", id, node.GroupID, *node.Port, node.Pinned, groupID, port, pinned)
	}
	var stored sql.NullInt64
	if err := client.db.QueryRow("SELECT inbound_port FROM proxy_nodes WHERE id = ?", id).Scan(&stored); err != nil {
		client.t.Fatal(err)
	}
	if stored.Valid != (port != 0) || stored.Int64 != int64(port) {
		client.t.Fatalf("node %d: stored port %+v, want port %d (zero maps to SQL NULL)", id, stored, port)
	}
}

func TestGroupCRUDCountsAndDeletionModes(t *testing.T) {
	client := newGroupTestClient(t)
	var groups []models.NodeGroup
	client.request(http.MethodGet, "/api/node-groups", nil, http.StatusOK, &groups)
	if len(groups) != 1 || groups[0].Name != "Default" || !groups[0].AllocatePort || groups[0].StartPort != 30001 {
		t.Fatalf("unexpected initial groups: %+v", groups)
	}
	home := groups[0]
	type deleteConflict struct {
		Code      string `json:"code"`
		Error     string `json:"error"`
		NodeCount int    `json:"node_count"`
	}
	var last deleteConflict
	client.request(http.MethodDelete, testGroupPath(home.ID), nil, http.StatusConflict, &last)
	if last.Code != "last_group" || last.Error == "" {
		t.Fatalf("expected last_group: %+v", last)
	}
	var settings map[string]json.RawMessage
	client.request(http.MethodGet, "/api/settings", nil, http.StatusOK, &settings)
	if _, exposed := settings["start_port"]; exposed {
		t.Fatal("global settings exposed the group start port")
	}
	client.request(http.MethodPost, "/api/node-groups", gin.H{"name": ""}, http.StatusBadRequest, nil)
	pool := client.createGroup("Pool", 35100, true)
	poolPath := testGroupPath(pool.ID)
	client.request(http.MethodPatch, poolPath, gin.H{"name": "bad change", "start_port": 65536}, http.StatusBadRequest, nil)
	const subscription = "https://feeds.example/nodes?plan=basic"
	var updated models.NodeGroup
	client.request(http.MethodPatch, poolPath, gin.H{
		"name": "Renamed", "subscription_url": subscription, "subscription_auto_update": true, "subscription_interval_minutes": 15,
	}, http.StatusOK, &updated)
	if updated.ID != pool.ID || updated.StartPort != 35100 || !updated.AllocatePort || updated.Revision <= pool.Revision {
		t.Fatalf("unexpected group patch result: %+v", updated)
	}
	active := client.createNode("active", pool.ID, true)
	inactive := client.createNode("inactive", pool.ID, false)
	client.request(http.MethodGet, "/api/node-groups", nil, http.StatusOK, &groups)
	if len(groups) != 2 || groups[0].ID != home.ID || groups[0].NodeCount != 0 || groups[0].EnabledNodeCount != 0 {
		t.Fatalf("incorrect group list: %+v", groups)
	}
	var detail models.NodeGroup
	client.request(http.MethodGet, poolPath, nil, http.StatusOK, &detail)
	for _, group := range []models.NodeGroup{groups[1], detail} {
		if group.ID != pool.ID || group.Name != "Renamed" || group.SubscriptionURL != subscription || !group.SubscriptionAutoUpdate ||
			group.SubscriptionIntervalMinutes != 15 || group.NodeCount != 2 || group.EnabledNodeCount != 1 {
			t.Fatalf("incorrect group details or counts: %+v", group)
		}
	}
	var nonempty deleteConflict
	client.request(http.MethodDelete, poolPath, nil, http.StatusConflict, &nonempty)
	if nonempty.Code != "group_not_empty" || nonempty.Error == "" || nonempty.NodeCount != 2 {
		t.Fatalf("expected group_not_empty with two nodes: %+v", nonempty)
	}
	client.request(http.MethodDelete, poolPath+"?mode=move&target_group_id="+strconv.Itoa(pool.ID), nil, http.StatusBadRequest, nil)
	client.assertNodeOrder(pool.ID, active.ID, inactive.ID)
	client.request(http.MethodDelete, poolPath+"?mode=move&target_group_id="+strconv.Itoa(home.ID), nil, http.StatusOK, nil)
	client.request(http.MethodGet, poolPath, nil, http.StatusNotFound, nil)
	client.assertNodeOrder(home.ID, active.ID, inactive.ID)
	client.assertPort(active.ID, home.ID, home.StartPort, false)
	client.assertPort(inactive.ID, home.ID, home.StartPort+1, false)
	keep := client.createGroup("Keep", 35100, false)
	survivor := client.createNode("survivor", keep.ID, true)
	client.request(http.MethodDelete, testGroupPath(home.ID)+"?mode=delete_nodes", nil, http.StatusOK, nil)
	for _, id := range []int{active.ID, inactive.ID} {
		client.request(http.MethodGet, testNodePath(id), nil, http.StatusNotFound, nil)
	}
	var cascade deleteConflict
	client.request(http.MethodDelete, testGroupPath(keep.ID)+"?mode=delete_nodes", nil, http.StatusConflict, &cascade)
	if cascade.Code != "last_group" || cascade.Error == "" {
		t.Fatalf("last nonempty group: %+v", cascade)
	}
	client.assertNodeOrder(0, survivor.ID)
	empty := client.createGroup("Empty", 35100, true)
	client.request(http.MethodDelete, testGroupPath(empty.ID), nil, http.StatusOK, nil)
	client.request(http.MethodGet, testGroupPath(empty.ID), nil, http.StatusNotFound, nil)
}

func TestGroupOrderingAndLegacyNodeRequests(t *testing.T) {
	client := newGroupTestClient(t)
	home := firstTestGroup(t, client.db)
	other := client.createGroup("Other", 35200, true)
	first := client.createNode("first", home.ID, true)
	second := client.createNode("second", home.ID, true)
	third := client.createNode("third", other.ID, true)
	fourth := client.createNode("fourth", other.ID, true)
	orders := func(ids ...int) []map[string]int {
		var nodes []map[string]int
		for order, id := range ids {
			nodes = append(nodes, map[string]int{"id": id, "sort_order": order})
		}
		return nodes
	}
	client.request(http.MethodPost, "/api/nodes/reorder", gin.H{"group_id": home.ID, "nodes": orders(second.ID, third.ID)}, http.StatusBadRequest, nil)
	client.request(http.MethodPost, "/api/nodes/reorder", gin.H{"group_id": home.ID, "nodes": orders(second.ID)}, http.StatusBadRequest, nil)
	client.assertNodeOrder(home.ID, first.ID, second.ID)
	client.request(http.MethodPost, "/api/nodes/reorder", gin.H{"group_id": home.ID, "nodes": orders(second.ID, first.ID)}, http.StatusOK, nil)
	client.assertNodeOrder(home.ID, second.ID, first.ID)
	client.assertPort(second.ID, home.ID, home.StartPort, false)
	client.assertPort(first.ID, home.ID, home.StartPort+1, false)
	client.assertNodeOrder(other.ID, third.ID, fourth.ID)
	client.assertPort(third.ID, other.ID, other.StartPort, false)
	client.request(http.MethodPost, "/api/nodes/reorder", gin.H{"nodes": orders(fourth.ID, first.ID, third.ID, second.ID)}, http.StatusOK, nil)
	client.assertNodeOrder(0, first.ID, second.ID, fourth.ID, third.ID)
	client.assertNodeOrder(other.ID, fourth.ID, third.ID)
	assertGroupOrder := func(firstID, secondID int) {
		t.Helper()
		var groups []models.NodeGroup
		client.request(http.MethodGet, "/api/node-groups", nil, http.StatusOK, &groups)
		if len(groups) != 2 || groups[0].ID != firstID || groups[0].SortOrder != 0 || groups[1].ID != secondID || groups[1].SortOrder != 1 {
			t.Fatalf("expected group IDs %d/%d at positions 0/1, got %+v", firstID, secondID, groups)
		}
	}
	for _, ids := range [][]int{{other.ID}, {other.ID, other.ID}, {other.ID, other.ID + 100}} {
		client.request(http.MethodPost, "/api/node-groups/reorder", gin.H{"ids": ids}, http.StatusBadRequest, nil)
		assertGroupOrder(home.ID, other.ID)
	}
	client.request(http.MethodPost, "/api/node-groups/reorder", gin.H{"ids": []int{other.ID, home.ID}}, http.StatusOK, nil)
	assertGroupOrder(other.ID, home.ID)
	client.assertNodeOrder(0, fourth.ID, third.ID, first.ID, second.ID)
	client.request(http.MethodGet, "/api/nodes?group_id=invalid", nil, http.StatusBadRequest, nil)
	legacy := client.createNode("legacy", 0, true)
	if legacy.GroupID != other.ID || legacy.SortOrder != 2 {
		t.Fatalf("legacy create used group=%d order=%d", legacy.GroupID, legacy.SortOrder)
	}
	client.request(http.MethodPut, testNodePath(first.ID), gin.H{"name": "edited", "type": "direct", "config": "{}", "enabled": true}, http.StatusOK, nil)
	client.assertNodeOrder(home.ID, first.ID, second.ID)
	hidden := client.createGroup("Portless imports", 35200, false)
	for _, testCase := range []struct {
		groupID int
		want    int
		name    string
	}{{0, other.ID, "fallback"}, {hidden.ID, hidden.ID, "explicit"}} {
		payload := gin.H{"content": "socks5://proxy.example:1080#" + testCase.name + "\nsocks5://proxy.example:1081#" + testCase.name + "-two", "enabled": true}
		if testCase.groupID != 0 {
			payload["group_id"] = testCase.groupID
		}
		var imported struct{ Success, Failed int }
		client.request(http.MethodPost, "/api/nodes/batch-import", payload, http.StatusOK, &imported)
		if imported.Success != 2 || imported.Failed != 0 {
			t.Fatalf("batch %s: result=%+v", testCase.name, imported)
		}
		for index, name := range []string{testCase.name, testCase.name + "-two"} {
			var id int
			if err := client.db.QueryRow("SELECT id FROM proxy_nodes WHERE name = ?", name).Scan(&id); err != nil {
				t.Fatal(err)
			}
			wantPort := 0
			if testCase.want == other.ID {
				wantPort = other.StartPort + 3 + index
			}
			client.assertPort(id, testCase.want, wantPort, false)
		}
	}
}

func TestGroupAllocationToggleHonorsPinsAndGlobalReservations(t *testing.T) {
	t.Setenv("PORT", "35001")
	client := newGroupTestClient(t)
	setTestGroupStartPort(t, client.db, 35000)
	home := firstTestGroup(t, client.db)
	reserved := client.createNode("disabled reservation", home.ID, false)
	held := client.createNode("fixed reservation", home.ID, true)
	client.request(http.MethodPut, testNodePath(held.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusOK, nil)
	client.assertPort(reserved.ID, home.ID, 35000, false)
	client.assertPort(held.ID, home.ID, 35002, true)
	pool := client.createGroup("No public listeners", 35000, false)
	poolPath := testGroupPath(pool.ID)
	first := client.createNode("first", pool.ID, true)
	second := client.createNode("second", pool.ID, false)
	client.assertPort(first.ID, pool.ID, 0, false)
	client.assertPort(second.ID, pool.ID, 0, false)
	client.request(http.MethodPut, testNodePath(first.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusConflict, nil)
	client.request(http.MethodPatch, testNodePath(second.ID), gin.H{"enabled": true}, http.StatusOK, nil)
	client.assertPort(second.ID, pool.ID, 0, false)
	var group models.NodeGroup
	client.request(http.MethodGet, poolPath, nil, http.StatusOK, &group)
	if group.NodeCount != 2 || group.EnabledNodeCount != 2 {
		t.Fatalf("enabling a portless node returned counts %d/%d", group.EnabledNodeCount, group.NodeCount)
	}
	client.request(http.MethodPatch, poolPath, gin.H{"allocate_port": true}, http.StatusOK, nil)
	client.assertPort(first.ID, pool.ID, 35003, false)
	client.assertPort(second.ID, pool.ID, 35004, false)
	for _, id := range []int{first.ID, second.ID} {
		client.request(http.MethodPut, testNodePath(id)+"/port-pin", gin.H{"pinned": true}, http.StatusOK, nil)
	}
	client.request(http.MethodPatch, poolPath, gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
	var blocked struct {
		Code   string `json:"code"`
		Error  string `json:"error"`
		Pinned []int  `json:"pinned_node_ids"`
	}
	client.request(http.MethodPatch, poolPath, gin.H{"allocate_port": false, "name": "blocked change"}, http.StatusConflict, &blocked)
	slices.Sort(blocked.Pinned)
	if blocked.Code != "pinned_ports_exist" || blocked.Error == "" || !slices.Equal(blocked.Pinned, []int{first.ID, second.ID}) {
		t.Fatalf("unexpected pin conflict: %+v", blocked)
	}
	client.request(http.MethodGet, poolPath, nil, http.StatusOK, &group)
	if !group.AllocatePort || !group.PreserveInboundPorts || group.Name != pool.Name {
		t.Fatalf("rejected disable partially changed group: %+v", group)
	}
	client.assertPort(first.ID, pool.ID, 35003, true)
	client.assertPort(second.ID, pool.ID, 35004, true)
	client.request(http.MethodPatch, poolPath, gin.H{"allocate_port": false, "release_pins": true}, http.StatusOK, nil)
	client.assertPort(first.ID, pool.ID, 0, false)
	client.assertPort(second.ID, pool.ID, 0, false)
	client.assertPort(held.ID, home.ID, 35002, true)
	client.request(http.MethodPatch, poolPath, gin.H{"allocate_port": true}, http.StatusOK, nil)
	client.assertPort(first.ID, pool.ID, 35003, false)
	client.assertPort(second.ID, pool.ID, 35004, false)
	client.request(http.MethodPatch, poolPath, gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
	for _, port := range []int{35000, 35001, 35002} {
		payload := gin.H{"name": "collision", "type": "direct", "config": "{}", "enabled": true, "group_id": pool.ID, "inbound_port": port}
		client.request(http.MethodPost, "/api/nodes", payload, http.StatusBadRequest, nil)
		client.request(http.MethodPut, testNodePath(first.ID), payload, http.StatusBadRequest, nil)
	}
	client.assertPort(first.ID, pool.ID, 35003, false)
	client.assertNodeOrder(pool.ID, first.ID, second.ID)
}

func TestGroupMoveRespectsPortAndPinPolicy(t *testing.T) {
	client := newGroupTestClient(t)
	home := firstTestGroup(t, client.db)
	target := client.createGroup("Target", 36000, true)
	hidden := client.createGroup("Portless", 36000, false)
	pinned := client.createNode("pinned", home.ID, true)
	automatic := client.createNode("automatic", home.ID, true)
	resident := client.createNode("resident", target.ID, true)
	client.request(http.MethodPut, testNodePath(pinned.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusOK, nil)
	move := func(ids []int, groupID, status int) {
		t.Helper()
		client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": ids, "group_id": groupID}, status, nil)
	}
	move([]int{pinned.ID, resident.ID + 100}, target.ID, http.StatusNotFound)
	move([]int{pinned.ID, pinned.ID}, target.ID, http.StatusBadRequest)
	move([]int{pinned.ID}, hidden.ID+100, http.StatusNotFound)
	client.assertNodeOrder(home.ID, pinned.ID, automatic.ID)
	client.assertPort(pinned.ID, home.ID, home.StartPort, true)
	move([]int{automatic.ID, pinned.ID}, target.ID, http.StatusOK)
	client.assertNodeOrder(home.ID)
	client.assertNodeOrder(target.ID, resident.ID, automatic.ID, pinned.ID)
	client.assertPort(pinned.ID, target.ID, home.StartPort, true)
	client.assertPort(automatic.ID, target.ID, target.StartPort+1, false)
	client.request(http.MethodPut, testNodePath(pinned.ID), gin.H{
		"name": "moved by edit", "type": "direct", "config": "{}", "enabled": true, "group_id": hidden.ID, "inbound_port": home.StartPort,
	}, http.StatusOK, nil)
	client.assertPort(pinned.ID, hidden.ID, 0, false)
	client.request(http.MethodPatch, testGroupPath(home.ID), gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
	client.request(http.MethodPatch, testGroupPath(target.ID), gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
	move([]int{pinned.ID}, target.ID, http.StatusOK)
	client.assertPort(pinned.ID, target.ID, target.StartPort+2, false)
	move([]int{automatic.ID}, home.ID, http.StatusOK)
	client.assertPort(automatic.ID, home.ID, target.StartPort+1, false)
	client.assertNodeOrder(target.ID, resident.ID, pinned.ID)
	client.request(http.MethodDelete, testGroupPath(target.ID)+"?mode=move&target_group_id="+strconv.Itoa(hidden.ID), nil, http.StatusOK, nil)
	client.assertNodeOrder(hidden.ID, resident.ID, pinned.ID)
	client.assertPort(resident.ID, hidden.ID, 0, false)
	client.assertPort(pinned.ID, hidden.ID, 0, false)
	client.assertPort(automatic.ID, home.ID, target.StartPort+1, false)
}

func TestGroupPreserveInboundPortsDependencyContract(t *testing.T) {
	for _, test := range []struct {
		name     string
		payload  gin.H
		allocate bool
		preserve bool
		status   int
	}{
		{"default", gin.H{}, true, false, http.StatusCreated},
		{"enabled by default allocation", gin.H{"preserve_inbound_ports": true}, true, true, http.StatusCreated},
		{"explicitly enabled", gin.H{"allocate_port": true, "preserve_inbound_ports": true}, true, true, http.StatusCreated},
		{"portless default", gin.H{"allocate_port": false}, false, false, http.StatusCreated},
		{"portless disabled", gin.H{"allocate_port": false, "preserve_inbound_ports": false}, false, false, http.StatusCreated},
		{"invalid dependency", gin.H{"allocate_port": false, "preserve_inbound_ports": true}, false, false, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newGroupTestClient(t)
			test.payload["name"] = test.name
			var group models.NodeGroup
			client.request(http.MethodPost, "/api/node-groups", test.payload, test.status, &group)
			if test.status == http.StatusCreated && (group.AllocatePort != test.allocate || group.PreserveInboundPorts != test.preserve) {
				t.Fatalf("分组默认值或依赖关系错误：%+v", group)
			}
		})
	}
	client := newGroupTestClient(t)
	group := client.createGroup("Toggle dependency", 37000, true)
	path := testGroupPath(group.ID)
	client.request(http.MethodPatch, path, gin.H{"preserve_inbound_ports": true}, http.StatusOK, &group)
	before := group
	client.request(http.MethodPatch, path, gin.H{"allocate_port": false, "preserve_inbound_ports": true, "name": "rejected"}, http.StatusBadRequest, nil)
	client.request(http.MethodGet, path, nil, http.StatusOK, &group)
	if !reflect.DeepEqual(before, group) {
		t.Fatal("拒绝依赖冲突时修改了分组")
	}
	client.request(http.MethodPatch, path, gin.H{"allocate_port": false}, http.StatusOK, &group)
	if group.AllocatePort || group.PreserveInboundPorts {
		t.Fatal("关闭端口分配未自动关闭保留策略")
	}
	client.request(http.MethodPatch, path, gin.H{"preserve_inbound_ports": true}, http.StatusBadRequest, nil)
	client.request(http.MethodPatch, path, gin.H{"allocate_port": true, "preserve_inbound_ports": true}, http.StatusOK, &group)
	if !group.AllocatePort || !group.PreserveInboundPorts {
		t.Fatal("同请求重新开启端口分配与保留策略失败")
	}
	var groups []map[string]any
	client.request(http.MethodGet, "/api/node-groups", nil, http.StatusOK, &groups)
	for _, response := range groups {
		if _, ok := response["preserve_inbound_ports"].(bool); !ok {
			t.Fatal("分组列表缺少布尔类型的保留策略字段")
		}
	}
}

func TestGroupPreserveAllocationRestoresNullPorts(t *testing.T) {
	t.Setenv("PORT", "37001")
	client := newGroupTestClient(t)
	setTestGroupStartPort(t, client.db, 37000)
	home := firstTestGroup(t, client.db)
	reserved := client.createNode("other group reservation", home.ID, false)
	group := client.createGroup("Portless", 37000, false)
	first := client.createNode("first", group.ID, true)
	second := client.createNode("second", group.ID, false)
	path := testGroupPath(group.ID)
	client.request(http.MethodPatch, path, gin.H{"allocate_port": true, "preserve_inbound_ports": true}, http.StatusOK, &group)
	if !group.PreserveInboundPorts {
		t.Fatal("重新开启端口分配后未保存保留策略")
	}
	client.assertPort(first.ID, group.ID, 37002, false)
	client.assertPort(second.ID, group.ID, 37003, false)
	client.assertPort(reserved.ID, home.ID, 37000, false)
	client.request(http.MethodPatch, path, gin.H{"allocate_port": false}, http.StatusOK, &group)
	if group.PreserveInboundPorts {
		t.Fatal("关闭端口分配后残留了保留策略")
	}
	client.assertPort(first.ID, group.ID, 0, false)
	client.assertPort(second.ID, group.ID, 0, false)
}

func TestGroupPreserveChangesKeepRunningKernelAndPorts(t *testing.T) {
	client := newGroupTestClient(t)
	group := firstTestGroup(t, client.db)
	node := client.createNode("running", group.ID, true)
	before := client.handler.singBoxService.RuntimeStatus()
	if !before.Running || before.PID == 0 {
		t.Fatal("测试内核未启动")
	}
	for _, payload := range []gin.H{
		{"preserve_inbound_ports": true},
		{"start_port": 41000},
		{"start_port": 42000, "preserve_inbound_ports": true},
	} {
		client.request(http.MethodPatch, testGroupPath(group.ID), payload, http.StatusOK, &group)
		client.assertPort(node.ID, group.ID, node.InboundPort, false)
		after := client.handler.singBoxService.RuntimeStatus()
		if !group.PreserveInboundPorts || !after.Running || after.PID != before.PID || after.ActiveConfigHash != before.ActiveConfigHash || after.DesiredConfigHash != before.DesiredConfigHash {
			t.Fatal("纯开启保留策略或保留模式下修改起始端口重启了内核")
		}
	}
	added := client.createNode("new baseline", group.ID, true)
	client.assertPort(added.ID, group.ID, 42000, false)
	client.assertPort(node.ID, group.ID, node.InboundPort, false)
}

func TestGroupPreserveEnableWithNewStartKeepsRunningKernelAndPorts(t *testing.T) {
	client := newGroupTestClient(t)
	group := firstTestGroup(t, client.db)
	node := client.createNode("existing automatic", group.ID, true)
	before := client.handler.singBoxService.RuntimeStatus()
	client.request(http.MethodPatch, testGroupPath(group.ID), gin.H{"preserve_inbound_ports": true, "start_port": 41000}, http.StatusOK, &group)
	client.assertPort(node.ID, group.ID, node.InboundPort, false)
	after := client.handler.singBoxService.RuntimeStatus()
	if !group.PreserveInboundPorts || group.StartPort != 41000 || !after.Running || after.PID != before.PID || after.ActiveConfigHash != before.ActiveConfigHash {
		t.Fatal("同请求开启保留策略并修改起始端口重排端口或重启了内核")
	}
}

func TestGroupPreserveRealignmentIsIsolatedAndRetainsPins(t *testing.T) {
	client := newGroupTestClient(t)
	home := firstTestGroup(t, client.db)
	other := client.createNode("unrelated automatic", home.ID, true)
	fixed := client.createNode("unrelated fixed", home.ID, true)
	client.request(http.MethodPut, testNodePath(fixed.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusOK, nil)
	client.request(http.MethodPut, testNodePath(fixed.ID), gin.H{"name": fixed.Name, "type": "direct", "config": "{}", "enabled": true, "inbound_port": 443}, http.StatusOK, nil)
	group := client.createGroup("Selected", 38000, true)
	pinned := client.createNode("retained pin", group.ID, true)
	automatic := client.createNode("selected automatic", group.ID, false)
	client.request(http.MethodPut, testNodePath(pinned.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusOK, nil)
	client.request(http.MethodPut, testNodePath(pinned.ID), gin.H{"name": pinned.Name, "type": "direct", "config": "{}", "enabled": true, "inbound_port": 444}, http.StatusOK, nil)
	if _, err := client.db.Exec("UPDATE proxy_nodes SET inbound_port=41070,sort_order=8 WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	beforeOther, err := models.LoadGroupNodes(t.Context(), client.db, home.ID)
	if err != nil {
		t.Fatal(err)
	}
	client.request(http.MethodPatch, testGroupPath(group.ID), gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
	client.assertPort(pinned.ID, group.ID, 444, true)
	client.request(http.MethodPut, testNodePath(automatic.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusConflict, nil)
	client.request(http.MethodPut, testNodePath(automatic.ID), gin.H{"name": automatic.Name, "type": "direct", "config": "{}", "enabled": false, "inbound_port": 42022}, http.StatusOK, nil)
	client.request(http.MethodPatch, testGroupPath(group.ID), gin.H{"start_port": 40000, "preserve_inbound_ports": false}, http.StatusOK, nil)
	client.assertPort(pinned.ID, group.ID, 444, true)
	client.assertPort(automatic.ID, group.ID, 40000, false)
	client.request(http.MethodPatch, testGroupPath(group.ID), gin.H{"start_port": 42000}, http.StatusOK, nil)
	client.assertPort(pinned.ID, group.ID, 444, true)
	client.assertPort(automatic.ID, group.ID, 42000, false)
	afterOther, err := models.LoadGroupNodes(t.Context(), client.db, home.ID)
	if err != nil || !reflect.DeepEqual(beforeOther, afterOther) {
		t.Fatal("关闭本组保留策略重排了其他分组或改变了其固定状态", err)
	}
	client.request(http.MethodPut, testNodePath(automatic.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusOK, nil)
}

func TestUpdateNodePreserveOmittedZeroAndManualPorts(t *testing.T) {
	for _, preserve := range []bool{false, true} {
		t.Run(strconv.FormatBool(preserve), func(t *testing.T) {
			client := newGroupTestClient(t)
			group := firstTestGroup(t, client.db)
			node := client.createNode("editable", group.ID, true)
			if preserve {
				client.request(http.MethodPatch, testGroupPath(group.ID), gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
			} else {
				client.request(http.MethodPut, testNodePath(node.ID)+"/port-pin", gin.H{"pinned": true}, http.StatusOK, nil)
			}
			payload := gin.H{"name": "edited", "type": "direct", "config": "{}", "enabled": true, "inbound_port": 42050}
			client.request(http.MethodPut, testNodePath(node.ID), payload, http.StatusOK, nil)
			client.request(http.MethodPatch, testGroupPath(group.ID), gin.H{"start_port": 41000}, http.StatusOK, nil)
			delete(payload, "inbound_port")
			client.request(http.MethodPut, testNodePath(node.ID), payload, http.StatusOK, nil)
			client.assertPort(node.ID, group.ID, 42050, !preserve)
			payload["inbound_port"] = 0
			client.request(http.MethodPut, testNodePath(node.ID), payload, http.StatusOK, nil)
			client.assertPort(node.ID, group.ID, 41000, !preserve)
			payload["inbound_port"] = 43050
			client.request(http.MethodPut, testNodePath(node.ID), payload, http.StatusOK, nil)
			client.assertPort(node.ID, group.ID, 43050, !preserve)
		})
	}
}

func TestUpdateNodeMoveUsesTargetGroupPortContract(t *testing.T) {
	for _, test := range []struct {
		name   string
		port   *int
		want   int
		status int
	}{
		{"omitted", nil, 42050, http.StatusOK},
		{"automatic", new(0), 45001, http.StatusOK},
		{"manual", new(46050), 46050, http.StatusOK},
		{"collision rollback", new(45000), 42050, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newGroupTestClient(t)
			home := firstTestGroup(t, client.db)
			client.request(http.MethodPatch, testGroupPath(home.ID), gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
			node := client.createNode("moving", home.ID, true)
			payload := gin.H{"name": node.Name, "type": "direct", "config": "{}", "enabled": true, "inbound_port": 42050}
			client.request(http.MethodPut, testNodePath(node.ID), payload, http.StatusOK, nil)
			target := client.createGroup("Target", 45000, true)
			resident := client.createNode("resident", target.ID, true)
			client.request(http.MethodPatch, testGroupPath(target.ID), gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
			payload["group_id"] = target.ID
			delete(payload, "inbound_port")
			if test.port != nil {
				payload["inbound_port"] = *test.port
			}
			client.request(http.MethodPut, testNodePath(node.ID), payload, test.status, nil)
			groupID := target.ID
			if test.status != http.StatusOK {
				groupID = home.ID
			}
			client.assertPort(node.ID, groupID, test.want, false)
			client.assertPort(resident.ID, target.ID, 45000, false)
		})
	}
}

func TestGroupMoveUsesSourceAndTargetPreservePoliciesIndependently(t *testing.T) {
	for _, test := range []struct {
		name           string
		sourcePreserve bool
		targetPreserve bool
		remainingPort  int
		movedPort      int
	}{
		{"preserved to automatic", true, false, 42021, 45001},
		{"automatic to preserved", false, true, 30002, 30001},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newGroupTestClient(t)
			home := firstTestGroup(t, client.db)
			client.request(http.MethodPatch, testGroupPath(home.ID), gin.H{"preserve_inbound_ports": test.sourcePreserve}, http.StatusOK, nil)
			first := client.createNode("moving", home.ID, true)
			second := client.createNode("remaining", home.ID, true)
			if test.sourcePreserve {
				for index, node := range []models.ProxyNode{first, second} {
					client.request(http.MethodPut, testNodePath(node.ID), gin.H{
						"name": node.Name, "type": "direct", "config": "{}", "enabled": true, "inbound_port": 42020 + index,
					}, http.StatusOK, nil)
				}
			}
			target := client.createGroup("Target", 45000, true)
			resident := client.createNode("resident", target.ID, true)
			client.request(http.MethodPatch, testGroupPath(target.ID), gin.H{"preserve_inbound_ports": test.targetPreserve}, http.StatusOK, nil)
			client.request(http.MethodPost, "/api/nodes/batch-move", gin.H{"ids": []int{first.ID}, "group_id": target.ID}, http.StatusOK, nil)
			client.assertPort(first.ID, target.ID, test.movedPort, false)
			client.assertPort(second.ID, home.ID, test.remainingPort, false)
			client.assertPort(resident.ID, target.ID, 45000, false)
			client.assertNodeOrder(home.ID, second.ID)
			client.assertNodeOrder(target.ID, resident.ID, first.ID)
		})
	}
}

func TestGroupMixedPreservePoliciesAcrossReorderDeleteImportAndGroupMove(t *testing.T) {
	client := newGroupTestClient(t)
	home := firstTestGroup(t, client.db)
	first := client.createNode("automatic first", home.ID, true)
	second := client.createNode("automatic second", home.ID, false)
	stable := client.createGroup("Preserved", 39000, true)
	stableFirst := client.createNode("preserved first", stable.ID, true)
	stableSecond := client.createNode("preserved second", stable.ID, false)
	client.request(http.MethodPatch, testGroupPath(stable.ID), gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
	for _, group := range []struct {
		id, first, second int
	}{{home.ID, first.ID, second.ID}, {stable.ID, stableFirst.ID, stableSecond.ID}} {
		client.request(http.MethodPost, "/api/nodes/reorder", gin.H{"group_id": group.id, "nodes": []gin.H{
			{"id": group.first, "sort_order": 1}, {"id": group.second, "sort_order": 0},
		}}, http.StatusOK, nil)
	}
	client.assertPort(first.ID, home.ID, 30002, false)
	client.assertPort(second.ID, home.ID, 30001, false)
	client.assertPort(stableFirst.ID, stable.ID, 39000, false)
	client.assertPort(stableSecond.ID, stable.ID, 39001, false)
	client.request(http.MethodPost, "/api/nodes/batch-delete", gin.H{"ids": []int{second.ID, stableFirst.ID}}, http.StatusOK, nil)
	client.assertPort(first.ID, home.ID, 30001, false)
	client.assertPort(stableSecond.ID, stable.ID, 39001, false)
	client.request(http.MethodPost, "/api/nodes/batch-import", gin.H{
		"group_id": stable.ID, "content": "socks5://127.0.0.1:1080#import-a\nsocks5://127.0.0.1:1081#import-b", "enabled": true,
	}, http.StatusOK, nil)
	nodes, err := models.LoadGroupNodes(t.Context(), client.db, stable.ID)
	if err != nil || len(nodes) != 3 {
		t.Fatalf("保留分组的批量导入失败：%d，%v", len(nodes), err)
	}
	client.assertPort(nodes[0].ID, stable.ID, 39001, false)
	client.assertPort(nodes[1].ID, stable.ID, 39000, false)
	client.assertPort(nodes[2].ID, stable.ID, 39002, false)
	client.assertPort(first.ID, home.ID, 30001, false)
	client.request(http.MethodDelete, testGroupPath(stable.ID)+"?mode=move&target_group_id="+strconv.Itoa(home.ID), nil, http.StatusOK, nil)
	client.assertPort(stableSecond.ID, home.ID, 30002, false)
	client.assertPort(first.ID, home.ID, 30001, false)
	if firstTestGroup(t, client.db).PreserveInboundPorts {
		t.Fatal("删除分组并移动节点时将来源保留策略覆盖了目标分组")
	}
}
