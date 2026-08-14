package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"sb-proxy/backend/models"

	"github.com/gin-gonic/gin"
)

func TestCreateNodePersistsManagedUpstreamModes(t *testing.T) {
	handler := newTestHandler(t, nil)
	basePayload := map[string]interface{}{
		"name": "global-default", "type": "direct", "config": `{}`,
		"inbound_port": 0, "auth_enabled": false, "username": "", "password": "", "enabled": true,
	}
	recorder := postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", basePayload, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create default node: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created models.ProxyNode
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.UpstreamMode != models.UpstreamModeGlobal {
		t.Fatalf("default upstream mode=%q", created.UpstreamMode)
	}

	customPayload := map[string]interface{}{
		"name": "custom", "type": "direct", "config": `{}`,
		"inbound_port": 0, "auth_enabled": false, "username": "", "password": "", "enabled": true,
		"upstream_mode": " CUSTOM ", "upstream_type": " SOCKS5 ",
		"upstream_config": ` {"server":"proxy.example.com","server_port":1080} `,
	}
	recorder = postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", customPayload, nil)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create custom node: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode custom response: %v", err)
	}
	stored, err := loadNodeByIDFrom(context.Background(), handler.db, created.ID)
	if err != nil {
		t.Fatalf("load custom node: %v", err)
	}
	if stored.UpstreamMode != models.UpstreamModeCustom || stored.UpstreamType != "socks5" || !strings.Contains(stored.UpstreamConfig, "proxy.example.com") {
		t.Fatalf("unexpected stored upstream: %+v", stored)
	}
}

func TestReplaceNodeMovesLegacyNodeWithoutDetourToGlobalMode(t *testing.T) {
	handler := newTestHandler(t, nil)
	nodeID := insertTestNodeWithPortAndOrder(t, handler.db, "legacy", 36111, 0)
	if _, err := handler.db.Exec(`
		UPDATE proxy_nodes
		SET type = 'direct', config = '{"detour":"direct"}', upstream_mode = 'legacy'
		WHERE id = ?
	`, nodeID); err != nil {
		t.Fatalf("seed legacy node: %v", err)
	}

	recorder := postJSON(
		t,
		handler.ReplaceNode,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(nodeID)+"/replace",
		map[string]interface{}{
			"link":        "socks5://user:pass@proxy.example.com:1080#replacement",
			"update_name": false,
		},
		ginParams("id", strconv.Itoa(nodeID)),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("replace legacy node: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	stored, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
	if err != nil {
		t.Fatalf("load replaced node: %v", err)
	}
	if stored.UpstreamMode != models.UpstreamModeGlobal {
		t.Fatalf("replaced legacy node mode=%q want %q", stored.UpstreamMode, models.UpstreamModeGlobal)
	}
	if stored.Type != "socks5" || strings.Contains(stored.Config, `"detour"`) {
		t.Fatalf("unexpected replacement config: type=%q config=%s", stored.Type, stored.Config)
	}
}

func TestUpdateNodeRejectsLocalInboundUpstreamAndRollsBack(t *testing.T) {
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec("UPDATE settings SET preserve_inbound_ports = 1"); err != nil {
		t.Fatalf("preserve inbound ports: %v", err)
	}
	sourceID := insertTestNodeWithPortAndOrder(t, handler.db, "source", 36101, 0)
	targetID := insertTestNodeWithPortAndOrder(t, handler.db, "target", 36102, 1)

	payload := map[string]interface{}{
		"name": "source", "type": "ss",
		"config":       `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port": 36101, "auth_enabled": true, "username": "user", "password": "pass", "enabled": true,
		"upstream_mode": models.UpstreamModeCustom, "upstream_type": "socks5",
		"upstream_config": `{"server":"localhost","server_port":36102,"username":"target+36102","password":"pass"}`,
	}
	recorder := postJSON(
		t,
		handler.UpdateNode,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(sourceID),
		payload,
		ginParams("id", strconv.Itoa(sourceID)),
	)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "route-number") {
		t.Fatalf("expected recursion rejection: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	stored, err := loadNodeByIDFrom(context.Background(), handler.db, sourceID)
	if err != nil {
		t.Fatalf("load rolled back node: %v", err)
	}
	if stored.UpstreamMode != models.UpstreamModeGlobal || stored.UpstreamType != "" {
		t.Fatalf("failed mutation was persisted: %+v", stored)
	}
	if targetID == 0 {
		t.Fatal("target node was not created")
	}
}

func TestUpdateSettingsAppliesGlobalUpstreamAndRejectsConflicts(t *testing.T) {
	handler := newTestHandler(t, nil)
	insertTestNodeWithPortAndOrder(t, handler.db, "node", 36201, 0)

	valid := map[string]interface{}{
		"global_upstream_enabled": true,
		"global_upstream_type":    "socks5",
		"global_upstream_config":  `{"server":"global.example.com","server_port":1080}`,
	}
	recorder := postJSON(t, handler.UpdateSettings, http.MethodPut, "/api/settings", valid, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("enable global upstream: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	settings, err := loadRuntimeSettingsFrom(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	if !settings.GlobalUpstreamEnabled || settings.GlobalUpstreamType != "socks5" {
		t.Fatalf("global upstream not persisted: %+v", settings)
	}
	nodes, err := loadAllNodesFrom(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("load nodes: %v", err)
	}
	configJSON, err := handler.singBoxService.BuildGlobalConfig(nodes, settings)
	if err != nil {
		t.Fatalf("build persisted state: %v", err)
	}
	if !strings.Contains(string(configJSON), `"detour": "managed-upstream-global"`) {
		t.Fatalf("global upstream detour missing: %s", configJSON)
	}

	localInbound := map[string]interface{}{
		"global_upstream_enabled": true,
		"global_upstream_type":    "socks5",
		"global_upstream_config":  `{"server":"127.0.0.1","server_port":36201}`,
	}
	recorder = postJSON(t, handler.UpdateSettings, http.MethodPut, "/api/settings", localInbound, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected local inbound rejection: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	after, err := loadRuntimeSettingsFrom(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if after.GlobalUpstreamConfig != settings.GlobalUpstreamConfig {
		t.Fatalf("rejected settings were persisted: before=%q after=%q", settings.GlobalUpstreamConfig, after.GlobalUpstreamConfig)
	}
}

func TestUpdateSettingsCanDisableMalformedStoredGlobalUpstream(t *testing.T) {
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec(`
		UPDATE settings
		SET global_upstream_enabled = 1,
		    global_upstream_type = 'socks5',
		    global_upstream_config = ''
		WHERE singleton_key = 1
	`); err != nil {
		t.Fatalf("seed malformed global upstream: %v", err)
	}

	recorder := postJSON(
		t,
		handler.UpdateSettings,
		http.MethodPut,
		"/api/settings",
		map[string]interface{}{"global_upstream_enabled": false},
		nil,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("disable malformed global upstream: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	settings, err := loadRuntimeSettingsFrom(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("reload settings: %v", err)
	}
	if settings.GlobalUpstreamEnabled {
		t.Fatal("malformed global upstream remained enabled")
	}
}

func ginParams(key string, value string) gin.Params {
	return gin.Params{{Key: key, Value: value}}
}
