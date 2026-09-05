package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strconv"
	"testing"

	"sb-proxy/backend/models"
)

func TestNodeFlagsPreserveLatestConfigurationAndCredentials(t *testing.T) {
	handler := newTestHandler(t, nil)
	nodeID := insertTestNode(t, handler.db)
	path := "/api/nodes/" + strconv.Itoa(nodeID)
	params := ginParams("id", strconv.Itoa(nodeID))
	payload := map[string]any{
		"name": "updated-node", "remark": "updated-remark", "type": "socks5",
		"config":   `{"server":"updated.example.com","server_port":1081}`,
		"username": "rotated-user", "password": "rotated-password", "auth_enabled": true,
		"enabled": true, "tcp_reuse_enabled": true,
	}
	if response := postJSON(t, handler.UpdateNode, http.MethodPut, path, payload, params); response.Code != http.StatusOK {
		t.Fatalf("update config before toggle: status=%d", response.Code)
	}
	previous, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []map[string]bool{
		{"enabled": false}, {"enabled": true}, {"tcp_reuse_enabled": false}, {"tcp_reuse_enabled": true},
	} {
		response := postJSON(t, handler.UpdateNodeFlags, http.MethodPatch, path, patch, params)
		if response.Code != http.StatusOK {
			t.Fatalf("toggle failed: status=%d body=%s", response.Code, response.Body.String())
		}
		stored, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if value, ok := patch["enabled"]; ok {
			previous.Enabled = value
		}
		if value, ok := patch["tcp_reuse_enabled"]; ok {
			previous.TCPReuseEnabled = value
		}
		previous.UpdatedAt = stored.UpdatedAt
		if !reflect.DeepEqual(previous, stored) {
			t.Fatal("toggle changed fields outside its requested flags")
		}
		var returned models.ProxyNode
		if err := json.Unmarshal(response.Body.Bytes(), &returned); err != nil {
			t.Fatal(err)
		}
		if returned.Config != stored.Config || returned.Password != stored.Password || returned.Username != stored.Username || returned.Name != stored.Name {
			t.Fatal("toggle response did not refresh the latest node state")
		}
	}
}

func TestNodeFlagsRejectInvalidFieldsAndMissingNodes(t *testing.T) {
	handler := newTestHandler(t, nil)
	nodeID := insertTestNode(t, handler.db)
	for _, payload := range []map[string]any{
		{}, {"enabled": nil}, {"enabled": "false"}, {"tcp_reuse_enabled": 1},
		{"enabled": false, "password": "stale-password"}, {"enabled": false, "config": "{}"},
	} {
		response := postJSON(t, handler.UpdateNodeFlags, http.MethodPatch, "/api/nodes/1", payload, ginParams("id", strconv.Itoa(nodeID)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid flag payload accepted: status=%d", response.Code)
		}
	}
	response := postJSON(t, handler.UpdateNodeFlags, http.MethodPatch, "/api/nodes/999", map[string]bool{"enabled": false}, ginParams("id", "999"))
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing node status=%d", response.Code)
	}
	node, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
	if err != nil || !node.Enabled || node.Password != "pass" {
		t.Fatal("invalid request changed stored node")
	}
}
