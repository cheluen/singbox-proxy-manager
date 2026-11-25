package api

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func TestNodeCredentialMutationsRejectUnpairedValues(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)

	createPayload := map[string]interface{}{
		"name":         "unsafe-node",
		"type":         "ss",
		"config":       `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"username":     "only-user",
		"password":     "",
		"auth_enabled": true,
		"enabled":      true,
	}
	recorder := postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", createPayload, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected create to reject unpaired credentials, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	nodeID := insertTestNode(t, handler.db)
	updatePayload := map[string]interface{}{
		"name":         "node1",
		"type":         "ss",
		"config":       `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"username":     "",
		"password":     "only-password",
		"auth_enabled": true,
		"enabled":      true,
	}
	recorder = postJSON(
		t,
		handler.UpdateNode,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(nodeID),
		updatePayload,
		gin.Params{{Key: "id", Value: strconv.Itoa(nodeID)}},
	)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected update to reject unpaired credentials, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	batchPayload := map[string]interface{}{
		"node_ids":     []int{nodeID},
		"username":     "only-user",
		"password":     "",
		"auth_enabled": true,
	}
	recorder = postJSON(t, handler.BatchSetAuth, http.MethodPost, "/api/nodes/batch-auth", batchPayload, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected batch auth to reject unpaired credentials, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var username, password string
	if err := handler.db.QueryRow("SELECT username, password FROM proxy_nodes WHERE id = ?", nodeID).Scan(&username, &password); err != nil {
		t.Fatalf("query credentials: %v", err)
	}
	if username != "user" || password != "pass" {
		t.Fatalf("rejected credential mutations changed the row: username=%q password=%q", username, password)
	}
}

func TestNodeCredentialsRejectLineBreaks(t *testing.T) {
	handler := newTestHandler(t, nil)
	nodeID := insertTestNode(t, handler.db)
	for _, field := range []string{"username", "password"} {
		for _, lineBreak := range []string{"\r", "\n"} {
			payload := gin.H{"name": "credential-test", "type": "direct", "config": "{}", "username": "user", "password": "pass", "node_ids": []int{nodeID}}
			payload[field] = "first" + lineBreak + "second"
			for _, action := range []struct {
				handler gin.HandlerFunc
				method  string
			}{{handler.CreateNode, http.MethodPost}, {handler.UpdateNode, http.MethodPut}, {handler.BatchSetAuth, http.MethodPost}} {
				response := postJSON(t, action.handler, action.method, "/nodes", payload, gin.Params{{Key: "id", Value: strconv.Itoa(nodeID)}})
				if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("line breaks")) {
					t.Fatalf("%s %s: HTTP %d", action.method, field, response.Code)
				}
			}
		}
	}
}

func TestDeleteReferencedNodeLeavesDatabaseUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	targetID := insertTestNodeWithPortAndOrder(t, handler.db, "detour-target", 30001, 0)
	insertTestNodeWithPortAndOrder(t, handler.db, "dependent", 30002, 1)
	if _, err := handler.db.Exec(
		`UPDATE proxy_nodes SET config = ?, upstream_mode = ? WHERE name = 'dependent'`,
		`{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p","detour":"detour-target"}`,
		models.UpstreamModeLegacy,
	); err != nil {
		t.Fatalf("configure detour: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(targetID)}}
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/api/nodes/"+strconv.Itoa(targetID), nil)
	handler.DeleteNode(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected referenced delete to fail, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var count int
	if err := handler.db.QueryRow("SELECT COUNT(*) FROM proxy_nodes").Scan(&count); err != nil {
		t.Fatalf("count nodes: %v", err)
	}
	if count != 2 {
		t.Fatalf("failed delete must leave both rows intact, got %d", count)
	}
}

func TestReorderValidationAndPinnedPortLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	firstID := insertTestNodeWithPortAndOrder(t, handler.db, "node-a", 30001, 0)
	pinnedID := insertTestNodeWithPortAndOrder(t, handler.db, "node-pinned", 80, 1)
	thirdID := insertTestNodeWithPortAndOrder(t, handler.db, "node-b", 30002, 2)

	invalid := map[string]interface{}{
		"nodes": []map[string]int{
			{"id": firstID, "sort_order": 0},
			{"id": pinnedID, "sort_order": 0},
		},
	}
	recorder := postJSON(t, handler.ReorderNodes, http.MethodPost, "/api/nodes/reorder", invalid, nil)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("expected incomplete/duplicate reorder to fail, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	pinPayload := map[string]bool{"pinned": true}
	recorder = postJSON(
		t,
		handler.SetNodeInboundPortPinned,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(pinnedID)+"/port-pin",
		pinPayload,
		gin.Params{{Key: "id", Value: strconv.Itoa(pinnedID)}},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("pin port failed: %d body=%s", recorder.Code, recorder.Body.String())
	}

	valid := map[string]interface{}{
		"nodes": []map[string]int{
			{"id": thirdID, "sort_order": 0},
			{"id": firstID, "sort_order": 1},
			{"id": pinnedID, "sort_order": 2},
		},
	}
	recorder = postJSON(t, handler.ReorderNodes, http.MethodPost, "/api/nodes/reorder", valid, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid reorder failed: %d body=%s", recorder.Code, recorder.Body.String())
	}

	assertPort := func(id, want int) {
		t.Helper()
		var got int
		if err := handler.db.QueryRow("SELECT inbound_port FROM proxy_nodes WHERE id = ?", id).Scan(&got); err != nil {
			t.Fatalf("query port for %d: %v", id, err)
		}
		if got != want {
			t.Fatalf("node %d port=%d want=%d", id, got, want)
		}
	}
	assertPort(thirdID, 30001)
	assertPort(firstID, 30002)
	assertPort(pinnedID, 80)

	updatePinned := map[string]interface{}{
		"name":         "node-pinned",
		"type":         "ss",
		"config":       `{"server":"example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port": 443,
		"username":     "user",
		"password":     "pass",
		"auth_enabled": true,
		"enabled":      true,
	}
	recorder = postJSON(
		t,
		handler.UpdateNode,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(pinnedID),
		updatePinned,
		gin.Params{{Key: "id", Value: strconv.Itoa(pinnedID)}},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("editing pinned port failed: %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertPort(pinnedID, 443)
	assertPort(thirdID, 30001)
	assertPort(firstID, 30002)

	recorder = postJSON(
		t,
		handler.SetNodeInboundPortPinned,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(pinnedID)+"/port-pin",
		map[string]bool{"pinned": false},
		gin.Params{{Key: "id", Value: strconv.Itoa(pinnedID)}},
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unpin port failed: %d body=%s", recorder.Code, recorder.Body.String())
	}
	assertPort(pinnedID, 443)
}

func TestRuntimeStartFailureCompensatesDatabaseAndRestoresLastGood(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	configDir := t.TempDir()
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
if grep -q "FAIL_START" "$3"; then
  exit 23
fi
exec sleep 300
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "500ms")

	service := services.NewSingBoxService(configDir)
	handler := NewHandler(db, service)
	handler.skipPortAvailabilityCheck = true
	nodeID := insertTestNode(t, db)
	nodes, err := models.LoadProxyNodes(context.Background(), handler.db)
	if err != nil {
		t.Fatalf("load initial nodes: %v", err)
	}
	writeTestRuntimeConfig(t, service, nodes)
	if err := service.Start(); err != nil {
		t.Fatalf("start initial runtime: %v", err)
	}
	t.Cleanup(func() {
		_ = service.Stop()
		_ = db.Close()
	})

	badUpdate := map[string]interface{}{
		"name":         "node1",
		"type":         "ss",
		"config":       `{"server":"FAIL_START.example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port": 30001,
		"username":     "user",
		"password":     "pass",
		"auth_enabled": true,
		"enabled":      true,
	}
	recorder := postJSON(
		t,
		handler.UpdateNode,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(nodeID),
		badUpdate,
		gin.Params{{Key: "id", Value: strconv.Itoa(nodeID)}},
	)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected runtime start failure, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var storedConfig string
	if err := db.QueryRow("SELECT config FROM proxy_nodes WHERE id = ?", nodeID).Scan(&storedConfig); err != nil {
		t.Fatalf("query compensated config: %v", err)
	}
	if bytes.Contains([]byte(storedConfig), []byte("FAIL_START")) {
		t.Fatalf("database retained failed runtime config: %s", storedConfig)
	}
	liveConfig, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		t.Fatalf("read restored live config: %v", err)
	}
	if bytes.Contains(liveConfig, []byte("FAIL_START")) {
		t.Fatalf("live config was not restored to last-good")
	}
	status := service.RuntimeStatus()
	if !status.Running || status.Degraded || status.ActiveConfigHash == "" ||
		status.ActiveConfigHash != status.DesiredConfigHash {
		t.Fatalf("last-good runtime should remain healthy after compensation: %+v", status)
	}
}

const rejectPort41000Kernel = `#!/bin/sh
if [ "$1" = "check" ]; then
  if grep -q '"listen_port": 41000' "$3"; then
    echo "requested port rejected" >&2
    exit 1
  fi
  exit 0
fi
exec sleep 300
`

func TestGroupPortValidationFailureRollsBackGroupAndNode(t *testing.T) {
	handler := newTestHandlerWithKernelScript(t, rejectPort41000Kernel)
	nodeID := insertTestNode(t, handler.db)
	before := firstTestGroup(t, handler.db)
	id := strconv.Itoa(before.ID)
	response := postJSON(t, handler.UpdateGroup, http.MethodPatch, "/api/node-groups/"+id,
		map[string]any{"name": "candidate", "start_port": 41000}, ginParams("id", id))
	if response.Code != http.StatusInternalServerError || !bytes.Contains(response.Body.Bytes(), []byte("requested port rejected")) {
		t.Fatalf("expected kernel rejection, got %d %s", response.Code, response.Body.String())
	}
	group := firstTestGroup(t, handler.db)
	if group.Name != before.Name || group.StartPort != before.StartPort || group.Revision != before.Revision {
		t.Fatalf("failed group update changed name, port, or revision: %+v", group)
	}
	node, err := models.LoadProxyNode(t.Context(), handler.db, nodeID)
	if err != nil || node.InboundPort != 30001 {
		t.Fatalf("failed group update changed node port: port=%d err=%v", node.InboundPort, err)
	}
}

func TestSettingsValidationFailureDoesNotPartiallyChangePasswordOrPorts(t *testing.T) {
	handler := newTestHandlerWithKernelScript(t, rejectPort41000Kernel)
	db := handler.db
	nodeID := insertTestNode(t, db)
	setTestGroupStartPort(t, db, 41000)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("old-password-123"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("hash old password: %v", err)
	}
	if _, err := db.Exec(`
		UPDATE settings
		SET admin_password = ?, auth_generation = 7, preserve_inbound_ports = 1
		WHERE singleton_key = 1
	`, string(oldHash)); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	token, _, err := createAdminSessionWithGeneration(context.Background(), db, 7, "", "")
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}

	payload := map[string]interface{}{
		"admin_password":         "new-password-456",
		"preserve_inbound_ports": false,
	}
	recorder := postJSON(t, handler.UpdateSettings, http.MethodPut, "/api/settings", payload, nil)
	if recorder.Code != http.StatusInternalServerError || !bytes.Contains(recorder.Body.Bytes(), []byte("requested port rejected")) {
		t.Fatalf("expected settings validation failure, got %d body=%s", recorder.Code, recorder.Body.String())
	}

	var currentHash string
	var generation int64
	var preserve bool
	if err := db.QueryRow(`
		SELECT admin_password, auth_generation, preserve_inbound_ports
		FROM settings WHERE singleton_key = 1
	`).Scan(&currentHash, &generation, &preserve); err != nil {
		t.Fatalf("query settings: %v", err)
	}
	if currentHash != string(oldHash) || generation != 7 || !preserve || firstTestGroup(t, db).StartPort != 41000 {
		t.Fatalf("failed settings request changed password, generation, port policy, or group start port")
	}
	var inboundPort int
	if err := db.QueryRow("SELECT inbound_port FROM proxy_nodes WHERE id = ?", nodeID).Scan(&inboundPort); err != nil {
		t.Fatalf("query node port: %v", err)
	}
	if inboundPort != 30001 {
		t.Fatalf("failed settings request changed node port to %d", inboundPort)
	}
	hash, err := handler.verifiedAdminSessionHash(context.Background(), token)
	if err != nil || hash == "" {
		t.Fatalf("failed settings request invalidated the previous session: %v", err)
	}
}

func TestRuntimeSnapshotFailureRollsBackOpenDatabaseTransaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	db.SetMaxOpenConns(1)
	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db: %v", err)
	}

	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  exit 0
fi
exec sleep 300
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")

	serviceWriteErr := fmt.Errorf("last-good volume is read-only")
	serviceConfigDir := t.TempDir()
	service := services.NewSingBoxService(
		serviceConfigDir,
		services.WithLastGoodSnapshotWriter(func(path string, content []byte) error {
			if bytes.Contains(content, []byte("candidate.example.com")) {
				return serviceWriteErr
			}
			return os.WriteFile(path, content, 0o600)
		}),
	)
	handler := NewHandler(db, service)
	handler.skipPortAvailabilityCheck = true
	nodeID := insertTestNode(t, db)
	nodes, err := models.LoadProxyNodes(context.Background(), db)
	if err != nil {
		t.Fatalf("load initial nodes: %v", err)
	}
	writeTestRuntimeConfig(t, service, nodes)
	if err := service.Start(); err != nil {
		t.Fatalf("start initial runtime: %v", err)
	}
	initialLiveConfig, err := os.ReadFile(service.ConfigPath())
	if err != nil {
		t.Fatalf("read initial live config: %v", err)
	}
	staleLastGood := bytes.Replace(
		initialLiveConfig,
		[]byte("example.com"),
		[]byte("stale.example.com"),
		1,
	)
	if bytes.Equal(staleLastGood, initialLiveConfig) {
		t.Fatalf("failed to construct mismatched last-good fixture")
	}
	if err := os.WriteFile(
		filepath.Join(serviceConfigDir, "config.json.last-good"),
		staleLastGood,
		0o600,
	); err != nil {
		t.Fatalf("write stale last-good fixture: %v", err)
	}
	t.Cleanup(func() {
		_ = service.Stop()
		_ = db.Close()
	})
	payload := map[string]interface{}{
		"name":         "snapshot-candidate",
		"type":         "ss",
		"config":       `{"server":"candidate.example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`,
		"inbound_port": 30001,
		"username":     "user",
		"password":     "pass",
		"auth_enabled": true,
		"enabled":      true,
	}
	recorder := postJSON(
		t,
		handler.UpdateNode,
		http.MethodPut,
		"/api/nodes/"+strconv.Itoa(nodeID),
		payload,
		gin.Params{{Key: "id", Value: strconv.Itoa(nodeID)}},
	)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected snapshot failure, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var storedName string
	if err := db.QueryRow("SELECT name FROM proxy_nodes WHERE id = ?", nodeID).Scan(&storedName); err != nil {
		t.Fatalf("query rolled-back node: %v", err)
	}
	if storedName == "snapshot-candidate" {
		t.Fatalf("database transaction committed despite runtime snapshot failure")
	}
	restoredLiveConfig, err := os.ReadFile(filepath.Join(serviceConfigDir, "config.json"))
	if err != nil {
		t.Fatalf("read restored live config: %v", err)
	}
	if !bytes.Equal(restoredLiveConfig, initialLiveConfig) {
		t.Fatalf("rollback did not restore the config that was actually running")
	}
	restoredLastGood, err := os.ReadFile(filepath.Join(serviceConfigDir, "config.json.last-good"))
	if err != nil {
		t.Fatalf("read healed last-good config: %v", err)
	}
	if !bytes.Equal(restoredLastGood, initialLiveConfig) {
		t.Fatalf("rollback did not heal last-good to the restored runtime config")
	}
	status := service.RuntimeStatus()
	if !status.Running || status.Degraded {
		t.Fatalf("previous runtime was not restored: %+v", status)
	}
}

func TestCancelledIPCheckDoesNotUpdateNodeStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	nodeID := insertTestNode(t, handler.db)
	if _, err := handler.db.Exec(`
		UPDATE proxy_nodes
		SET node_ip = '198.51.100.7', location = 'Before', country_code = 'ZZ', latency = 77
		WHERE id = ?
	`, nodeID); err != nil {
		t.Fatalf("seed node status: %v", err)
	}

	started := make(chan struct{})
	handler.checkProxyIP = func(ctx context.Context, _, _, _ string) (*services.IPInfo, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	requestContext, cancel := context.WithCancel(context.Background())
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Params = gin.Params{{Key: "id", Value: strconv.Itoa(nodeID)}}
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/api/nodes/1/check-ip", nil).WithContext(requestContext)

	done := make(chan struct{})
	go func() {
		handler.CheckNodeIP(ginContext)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("cancelled IP check handler did not return")
	}

	var nodeIP, location, countryCode string
	var latency int
	if err := handler.db.QueryRow(`
		SELECT node_ip, location, country_code, latency FROM proxy_nodes WHERE id = ?
	`, nodeID).Scan(&nodeIP, &location, &countryCode, &latency); err != nil {
		t.Fatalf("query node status: %v", err)
	}
	if nodeIP != "198.51.100.7" || location != "Before" || countryCode != "ZZ" || latency != 77 {
		t.Fatalf("cancelled check modified node status: ip=%q location=%q country=%q latency=%d", nodeIP, location, countryCode, latency)
	}
}

func TestGetNodesReturnsErrorOnScanFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(t, nil)
	if _, err := handler.db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, sort_order, enabled, group_id)
		VALUES ('corrupt', 'direct', '{}', 'not-a-port', 0, 1, ?)
	`, firstTestGroup(t, handler.db).ID); err != nil {
		t.Fatalf("seed corrupt row: %v", err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/nodes", nil)
	handler.GetNodes(ctx)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("scan failure must return 500, got %d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestValidateInboundCredentialsMatchesGeneratorEmptiness(t *testing.T) {
	enabled := true
	disabled := false
	if err := validateInboundCredentials("user", "pass", &enabled); err != nil {
		t.Fatalf("paired credentials should pass: %v", err)
	}
	if err := validateInboundCredentials("", "", &disabled); err != nil {
		t.Fatalf("explicitly disabled empty credentials should pass: %v", err)
	}
	if err := validateInboundCredentials(" ", " ", &enabled); err != nil {
		t.Fatalf("non-empty credentials should use the same semantics as config generation: %v", err)
	}
	if err := validateInboundCredentials("user", "", nil); err == nil {
		t.Fatalf("unpaired credentials must be rejected")
	}
	if err := validateInboundCredentials("", "", &enabled); err == nil {
		t.Fatalf("enabled authentication without credentials must be rejected")
	}
	if err := validateInboundCredentials("user", "pass", &disabled); err == nil {
		t.Fatalf("disabled authentication with credentials must be rejected")
	}
}
