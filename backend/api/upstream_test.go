package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"

	"github.com/gin-gonic/gin"
)

type staticSQLResult struct {
	rowsAffected int64
}

func (result staticSQLResult) LastInsertId() (int64, error) {
	return 0, nil
}

func (result staticSQLResult) RowsAffected() (int64, error) {
	return result.rowsAffected, nil
}

func TestVerifyConditionalCheckUpdateHandlesMatchedRowsSemantics(t *testing.T) {
	if err := verifyConditionalCheckUpdate(staticSQLResult{}, func() (bool, error) {
		return true, nil
	}); err != nil {
		t.Fatalf("zero changed rows with an unchanged target was rejected: %v", err)
	}
	if err := verifyConditionalCheckUpdate(staticSQLResult{}, func() (bool, error) {
		return false, nil
	}); !errors.Is(err, errUpstreamDefinitionChanged) {
		t.Fatalf("changed target was not detected: %v", err)
	}
	if err := verifyConditionalCheckUpdate(staticSQLResult{rowsAffected: 2}, func() (bool, error) {
		return true, nil
	}); err == nil {
		t.Fatal("multi-row conditional update was accepted")
	}
}

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
	if created.UpstreamMode != models.UpstreamModeNone {
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

func TestReplaceNodeMovesLegacyNodeWithoutDetourToDirectMode(t *testing.T) {
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
	if stored.UpstreamMode != models.UpstreamModeNone {
		t.Fatalf("replaced legacy node mode=%q want %q", stored.UpstreamMode, models.UpstreamModeNone)
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
	if stored.UpstreamMode != models.UpstreamModeNone || stored.UpstreamType != "" {
		t.Fatalf("failed mutation was persisted: %+v", stored)
	}
	if targetID == 0 {
		t.Fatal("target node was not created")
	}
}

func TestUpdateSettingsAppliesGlobalUpstreamAndRejectsConflicts(t *testing.T) {
	handler := newTestHandler(t, nil)
	nodeID := insertTestNodeWithPortAndOrder(t, handler.db, "node", 36201, 0)
	if _, err := handler.db.Exec(
		"UPDATE proxy_nodes SET upstream_mode = ? WHERE id = ?",
		models.UpstreamModeGlobal,
		nodeID,
	); err != nil {
		t.Fatalf("set node to follow global: %v", err)
	}

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

func TestUpdateNodeUpstreamChangesOnlyManagedUpstreamFields(t *testing.T) {
	handler := newTestHandler(t, nil)
	nodeID := insertTestNodeWithPortAndOrder(t, handler.db, "node", 36301, 0)
	before, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
	if err != nil {
		t.Fatalf("load original node: %v", err)
	}

	payload := map[string]interface{}{
		"upstream_mode":   models.UpstreamModeCustom,
		"upstream_type":   " SOCKS5 ",
		"upstream_config": ` {"server":"upstream.example.com","server_port":1080} `,
	}
	recorder := postJSON(
		t,
		handler.UpdateNodeUpstream,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(nodeID)+"/upstream",
		payload,
		ginParams("id", strconv.Itoa(nodeID)),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("update node upstream: status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	after, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
	if err != nil {
		t.Fatalf("load updated node: %v", err)
	}
	if after.UpstreamMode != models.UpstreamModeCustom || after.UpstreamType != "socks5" || !strings.Contains(after.UpstreamConfig, "upstream.example.com") {
		t.Fatalf("unexpected managed upstream: %+v", after)
	}
	if after.Name != before.Name || after.Config != before.Config || after.InboundPort != before.InboundPort || after.Username != before.Username || after.Password != before.Password {
		t.Fatalf("dedicated upstream update changed unrelated node fields: before=%+v after=%+v", before, after)
	}

	invalid := postJSON(
		t,
		handler.UpdateNodeUpstream,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(nodeID)+"/upstream",
		map[string]interface{}{
			"upstream_mode":   models.UpstreamModeCustom,
			"upstream_type":   "",
			"upstream_config": "",
		},
		ginParams("id", strconv.Itoa(nodeID)),
	)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid custom upstream was accepted: status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	rolledBack, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
	if err != nil {
		t.Fatalf("load rolled back node: %v", err)
	}
	if rolledBack.UpstreamMode != after.UpstreamMode || rolledBack.UpstreamConfig != after.UpstreamConfig {
		t.Fatalf("invalid upstream update was persisted: %+v", rolledBack)
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

func TestCheckGlobalUpstreamIPPersistsSuccessAndFailure(t *testing.T) {
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec(`
		UPDATE settings
		SET global_upstream_type = 'socks5',
		    global_upstream_config = '{"server":"global.example.com","server_port":1080}'
		WHERE singleton_key = 1
	`); err != nil {
		t.Fatalf("configure global upstream: %v", err)
	}

	handler.checkUpstreamIP = func(_ context.Context, definition models.ProxyDefinition) (*services.IPInfo, error) {
		if definition.Type != "socks5" {
			t.Fatalf("unexpected global upstream definition: %+v", definition)
		}
		return &services.IPInfo{IP: "198.51.100.30", Location: "Global", CountryCode: "GL", Latency: 42}, nil
	}
	success := postJSON(t, handler.CheckGlobalUpstreamIP, http.MethodPost, "/api/settings/upstream/check-ip", nil, nil)
	if success.Code != http.StatusOK {
		t.Fatalf("check global upstream: status=%d body=%s", success.Code, success.Body.String())
	}
	settings, err := loadRuntimeSettingsFrom(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("load successful global check: %v", err)
	}
	if settings.GlobalUpstreamIP != "198.51.100.30" || settings.GlobalUpstreamLocation != "Global" || settings.GlobalUpstreamCountryCode != "GL" || settings.GlobalUpstreamLatency != 42 || settings.GlobalUpstreamError != "" {
		t.Fatalf("unexpected successful global check: %+v", settings)
	}

	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
		return nil, fmt.Errorf("global upstream unavailable")
	}
	failure := postJSON(t, handler.CheckGlobalUpstreamIP, http.MethodPost, "/api/settings/upstream/check-ip", nil, nil)
	if failure.Code != http.StatusBadGateway {
		t.Fatalf("failed global check status=%d body=%s", failure.Code, failure.Body.String())
	}
	settings, err = loadRuntimeSettingsFrom(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("load failed global check: %v", err)
	}
	if settings.GlobalUpstreamIP != "" || settings.GlobalUpstreamLatency != 0 || settings.GlobalUpstreamError != "global upstream unavailable" {
		t.Fatalf("failed global check did not clear stale data: %+v", settings)
	}

	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
		return nil, nil
	}
	empty := postJSON(t, handler.CheckGlobalUpstreamIP, http.MethodPost, "/api/settings/upstream/check-ip", nil, nil)
	if empty.Code != http.StatusBadGateway || !strings.Contains(empty.Body.String(), "upstream IP check returned no result") {
		t.Fatalf("empty global check status=%d body=%s", empty.Code, empty.Body.String())
	}
	settings, err = loadRuntimeSettingsFrom(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("load empty global check: %v", err)
	}
	if settings.GlobalUpstreamError != "upstream IP check returned no result" {
		t.Fatalf("empty global check was not persisted: %+v", settings)
	}
}

func TestCheckGlobalUpstreamIPMapsExternalRateLimitToHTTP429(t *testing.T) {
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec(`
		UPDATE settings
		SET global_upstream_type = 'socks5',
		    global_upstream_config = '{"server":"global.example.com","server_port":1080}'
		WHERE singleton_key = 1
	`); err != nil {
		t.Fatalf("configure global upstream: %v", err)
	}
	handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
		return nil, &services.IPCheckHTTPStatusError{
			Service:    "ip-api.com",
			StatusCode: http.StatusTooManyRequests,
			Status:     "429 Too Many Requests",
			RetryAfter: "31",
		}
	}

	recorder := postJSON(t, handler.CheckGlobalUpstreamIP, http.MethodPost, "/api/settings/upstream/check-ip", nil, nil)
	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "31" {
		t.Fatalf("global rate limit did not preserve HTTP semantics: status=%d retry=%q body=%s", recorder.Code, recorder.Header().Get("Retry-After"), recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "rate limited") || !strings.Contains(recorder.Body.String(), "HTTP 429") {
		t.Fatalf("global rate-limit response is not actionable: %s", recorder.Body.String())
	}
}

func TestUpstreamIPChecksDiscardResultsAfterDefinitionChanges(t *testing.T) {
	type checkResponse struct {
		status int
		body   string
	}

	t.Run("node upstream", func(t *testing.T) {
		handler := newTestHandler(t, func(string, string, string) (*services.IPInfo, error) {
			return &services.IPInfo{IP: "203.0.113.60", Location: "Final", Latency: 50}, nil
		})
		handler.db.SetMaxOpenConns(1)
		nodeID := insertTestNode(t, handler.db)
		originalConfig := `{"server":"old.example.com","server_port":1080}`
		if _, err := handler.db.Exec(`
			UPDATE proxy_nodes
			SET upstream_mode = 'custom', upstream_type = 'socks5', upstream_config = ?
			WHERE id = ?
		`, originalConfig, nodeID); err != nil {
			t.Fatalf("configure original node upstream: %v", err)
		}

		started := make(chan struct{})
		release := make(chan struct{})
		handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
			close(started)
			<-release
			return &services.IPInfo{IP: "198.51.100.60", Location: "Old upstream", Latency: 20}, nil
		}
		done := make(chan checkResponse, 1)
		go func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = ginParams("id", strconv.Itoa(nodeID))
			ctx.Request, _ = http.NewRequest(http.MethodPost, "/api/nodes/"+strconv.Itoa(nodeID)+"/check-ip", nil)
			handler.CheckNodeIP(ctx)
			done <- checkResponse{status: recorder.Code, body: recorder.Body.String()}
		}()

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("node upstream check did not start")
		}
		if _, err := handler.db.Exec(`
			UPDATE proxy_nodes
			SET upstream_mode = 'none', upstream_ip = '', upstream_error = ''
			WHERE id = ?
		`, nodeID); err != nil {
			t.Fatalf("change node upstream during check: %v", err)
		}
		close(release)

		var response checkResponse
		select {
		case response = <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("node upstream check did not finish")
		}
		if response.status != http.StatusConflict || !strings.Contains(response.body, errUpstreamDefinitionChanged.Error()) {
			t.Fatalf("stale node check response: status=%d body=%s", response.status, response.body)
		}
		var mode, upstreamIP, upstreamError string
		if err := handler.db.QueryRow(`
			SELECT upstream_mode, upstream_ip, upstream_error FROM proxy_nodes WHERE id = ?
		`, nodeID).Scan(&mode, &upstreamIP, &upstreamError); err != nil {
			t.Fatalf("query changed node upstream: %v", err)
		}
		if mode != models.UpstreamModeNone || upstreamIP != "" || upstreamError != "" {
			t.Fatalf("stale node check overwrote changed definition: mode=%q ip=%q error=%q", mode, upstreamIP, upstreamError)
		}
	})

	t.Run("global upstream", func(t *testing.T) {
		handler := newTestHandler(t, nil)
		handler.db.SetMaxOpenConns(1)
		originalConfig := `{"server":"old-global.example.com","server_port":1080}`
		newConfig := `{"server":"new-global.example.com","server_port":1080}`
		if _, err := handler.db.Exec(`
			UPDATE settings
			SET global_upstream_type = 'socks5', global_upstream_config = ?
			WHERE singleton_key = 1
		`, originalConfig); err != nil {
			t.Fatalf("configure original global upstream: %v", err)
		}

		started := make(chan struct{})
		release := make(chan struct{})
		handler.checkUpstreamIP = func(context.Context, models.ProxyDefinition) (*services.IPInfo, error) {
			close(started)
			<-release
			return &services.IPInfo{IP: "198.51.100.61", Location: "Old global", Latency: 21}, nil
		}
		done := make(chan checkResponse, 1)
		go func() {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request, _ = http.NewRequest(http.MethodPost, "/api/settings/upstream/check-ip", nil)
			handler.CheckGlobalUpstreamIP(ctx)
			done <- checkResponse{status: recorder.Code, body: recorder.Body.String()}
		}()

		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("global upstream check did not start")
		}
		if _, err := handler.db.Exec(`
			UPDATE settings
			SET global_upstream_config = ?, global_upstream_ip = '', global_upstream_error = ''
			WHERE singleton_key = 1
		`, newConfig); err != nil {
			t.Fatalf("change global upstream during check: %v", err)
		}
		close(release)

		var response checkResponse
		select {
		case response = <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("global upstream check did not finish")
		}
		if response.status != http.StatusConflict || !strings.Contains(response.body, errUpstreamDefinitionChanged.Error()) {
			t.Fatalf("stale global check response: status=%d body=%s", response.status, response.body)
		}
		var storedConfig, upstreamIP, upstreamError string
		if err := handler.db.QueryRow(`
			SELECT global_upstream_config, global_upstream_ip, global_upstream_error
			FROM settings WHERE singleton_key = 1
		`).Scan(&storedConfig, &upstreamIP, &upstreamError); err != nil {
			t.Fatalf("query changed global upstream: %v", err)
		}
		if storedConfig != newConfig || upstreamIP != "" || upstreamError != "" {
			t.Fatalf("stale global check overwrote changed definition: config=%q ip=%q error=%q", storedConfig, upstreamIP, upstreamError)
		}
	})
}

func ginParams(key string, value string) gin.Params {
	return gin.Params{{Key: key, Value: value}}
}
