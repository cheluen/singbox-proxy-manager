package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"sb-proxy/backend/models"
)

type groupTestClient struct {
	t      *testing.T
	db     *sql.DB
	router *gin.Engine
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
	return groupTestClient{t: t, db: handler.db, router: router}
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
	client.request(http.MethodPut, "/api/settings", gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
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
	if !group.AllocatePort || group.Name != pool.Name {
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
	client.request(http.MethodPut, "/api/settings", gin.H{"preserve_inbound_ports": true}, http.StatusOK, nil)
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
