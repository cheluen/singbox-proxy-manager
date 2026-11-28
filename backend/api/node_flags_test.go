package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
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
	previous, err := models.LoadProxyNode(context.Background(), handler.db, nodeID)
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
		stored, err := models.LoadProxyNode(context.Background(), handler.db, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		if value, ok := patch["enabled"]; ok {
			if previous.Enabled != value {
				previous.CheckRevision++
			}
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
	node, err := models.LoadProxyNode(context.Background(), handler.db, nodeID)
	if err != nil || !node.Enabled || node.Password != "pass" {
		t.Fatal("invalid request changed stored node")
	}
}

func newBatchEnabledTestHandler(testContext *testing.T) (*Handler, string) {
	testContext.Helper()
	commandsPath := filepath.Join(testContext.TempDir(), "kernel-commands")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$1\" >> %s\nif [ \"$1\" = check ]; then exit 0; fi\nexec sleep 300\n", strconv.Quote(commandsPath))
	return newTestHandlerWithKernelScript(testContext, script), commandsPath
}

func insertBatchEnabledTestNodes(testContext *testing.T, handler *Handler, enabledStates ...bool) []int {
	testContext.Helper()
	groupID := firstTestGroup(testContext, handler.db).ID
	ctx := testContext.Context()
	tx, err := handler.db.BeginTx(ctx, nil)
	if err != nil {
		testContext.Fatal(err)
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, insertNodeSQL)
	if err != nil {
		testContext.Fatal(err)
	}
	defer statement.Close()
	ids := make([]int, 0, len(enabledStates))
	stamp := time.Date(2000, time.January, 2, 3, 4, 5, 0, time.UTC)
	for index, enabled := range enabledStates {
		node := models.ProxyNode{
			Name: fmt.Sprintf("batch-node-%d", index), Type: "direct", Config: "{}", GroupID: groupID,
			InboundPort: 32001 + index, TCPReuseEnabled: true, UpstreamMode: models.UpstreamModeNone,
			SortOrder: index, Enabled: enabled,
		}
		result, err := statement.ExecContext(ctx, nodeInsertArgs(node)...)
		if err != nil {
			testContext.Fatal(err)
		}
		id, err := appdb.LastInsertID(ctx, tx, result, appdb.DialectFor(handler.db))
		if err != nil {
			testContext.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE proxy_nodes SET check_status='failed',check_error='saved failure',
			checked_at=?,check_revision=7,node_ip='192.0.2.1',location='saved',country_code='ZZ',latency=42,updated_at=? WHERE id=?`, stamp, stamp, id); err != nil {
			testContext.Fatal(err)
		}
		ids = append(ids, int(id))
	}
	if err := tx.Commit(); err != nil {
		testContext.Fatal(err)
	}
	return ids
}

func loadBatchEnabledTestNodes(testContext *testing.T, handler *Handler) []models.ProxyNode {
	testContext.Helper()
	nodes, err := models.LoadProxyNodes(testContext.Context(), handler.db)
	if err != nil {
		testContext.Fatal(err)
	}
	return nodes
}

func batchEnabledKernelCommands(testContext *testing.T, path string) string {
	testContext.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		testContext.Fatal(err)
	}
	return string(data)
}

func assertBatchEnabledResponse(testContext *testing.T, response *httptest.ResponseRecorder, enabled bool, updated, unchanged int) {
	testContext.Helper()
	var result struct {
		Message        string `json:"message"`
		Enabled        bool   `json:"enabled"`
		UpdatedCount   int    `json:"updated_count"`
		UnchangedCount int    `json:"unchanged_count"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || response.Code != http.StatusOK {
		testContext.Fatalf("批量启停失败：HTTP %d，%s，%v", response.Code, response.Body.String(), err)
	}
	if result.Message != "nodes updated" || result.Enabled != enabled || result.UpdatedCount != updated || result.UnchangedCount != unchanged {
		testContext.Fatalf("批量启停响应错误：%+v，期望 enabled=%t updated=%d unchanged=%d", result, enabled, updated, unchanged)
	}
}

func TestBatchSetNodesEnabledMixedStates(testContext *testing.T) {
	for _, enabled := range []bool{true, false} {
		testContext.Run(strconv.FormatBool(enabled), func(testContext *testing.T) {
			handler, commandsPath := newBatchEnabledTestHandler(testContext)
			ids := insertBatchEnabledTestNodes(testContext, handler, false, true, false, true)
			if response := postJSON(testContext, handler.RestartRuntime, http.MethodPost, "/api/runtime/restart", nil, nil); response.Code != http.StatusOK {
				testContext.Fatal(response.Body.String())
			}
			before := loadBatchEnabledTestNodes(testContext, handler)
			beforeRuntime := handler.singBoxService.RuntimeStatus()
			beforeCommands := batchEnabledKernelCommands(testContext, commandsPath)
			updated := 0
			for _, node := range before[:3] {
				if node.Enabled != enabled {
					updated++
				}
			}
			response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids[:3], "enabled": enabled}, nil)
			assertBatchEnabledResponse(testContext, response, enabled, updated, 3-updated)
			after := loadBatchEnabledTestNodes(testContext, handler)
			for index, previous := range before {
				expected := previous
				if index < 3 && previous.Enabled != enabled {
					expected.Enabled = enabled
					expected.CheckRevision++
					expected.CheckStatus, expected.CheckError, expected.CheckedAt = "untested", "", nil
					expected.NodeIP, expected.Location, expected.CountryCode, expected.Latency = "", "", "", 0
					expected.UpdatedAt = after[index].UpdatedAt
					if after[index].UpdatedAt.Equal(time.Date(2000, time.January, 2, 3, 4, 5, 0, time.UTC)) {
						testContext.Fatal("已变更节点未更新 updated_at")
					}
				}
				if !reflect.DeepEqual(expected, after[index]) {
					testContext.Fatalf("节点 %d 的启停修改了预期之外的字段", previous.ID)
				}
			}
			afterCommands := batchEnabledKernelCommands(testContext, commandsPath)
			if strings.Count(afterCommands, "run\n")-strings.Count(beforeCommands, "run\n") != 1 ||
				strings.Count(afterCommands, "check\n")-strings.Count(beforeCommands, "check\n") != 1 {
				testContext.Fatalf("批量请求未仅校验并启动一次内核：%q", afterCommands)
			}
			if afterRuntime := handler.singBoxService.RuntimeStatus(); !afterRuntime.Running || afterRuntime.PID == beforeRuntime.PID {
				testContext.Fatal("批量启停未应用新的内核配置")
			}
		})
	}
}

func TestBatchSetNodesEnabledNoChanges(testContext *testing.T) {
	for _, enabled := range []bool{true, false} {
		testContext.Run(strconv.FormatBool(enabled), func(testContext *testing.T) {
			handler, commandsPath := newBatchEnabledTestHandler(testContext)
			ids := insertBatchEnabledTestNodes(testContext, handler, enabled, enabled)
			if response := postJSON(testContext, handler.RestartRuntime, http.MethodPost, "/api/runtime/restart", nil, nil); response.Code != http.StatusOK {
				testContext.Fatal(response.Body.String())
			}
			before := loadBatchEnabledTestNodes(testContext, handler)
			beforeRuntime := handler.singBoxService.RuntimeStatus()
			beforeCommands := batchEnabledKernelCommands(testContext, commandsPath)
			response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": enabled}, nil)
			assertBatchEnabledResponse(testContext, response, enabled, 0, len(ids))
			if !reflect.DeepEqual(before, loadBatchEnabledTestNodes(testContext, handler)) ||
				handler.singBoxService.RuntimeStatus() != beforeRuntime || batchEnabledKernelCommands(testContext, commandsPath) != beforeCommands {
				testContext.Fatal("无变化批次修改了节点、检测状态、时间戳或内核")
			}
		})
	}
}

func TestBatchSetNodesEnabledValidation(testContext *testing.T) {
	handler := newTestHandler(testContext, nil)
	ids := insertBatchEnabledTestNodes(testContext, handler, true, true)
	before := loadBatchEnabledTestNodes(testContext, handler)
	valid := fmt.Sprintf(`{"ids":[%d],"enabled":false}`, ids[0])
	for _, testCase := range []struct {
		name, body string
		status     int
		error      string
	}{
		{"empty ids", `{"ids":[],"enabled":false}`, 400, ""},
		{"missing ids", `{"enabled":false}`, 400, ""},
		{"null ids", `{"ids":null,"enabled":false}`, 400, ""},
		{"missing enabled", fmt.Sprintf(`{"ids":[%d]}`, ids[0]), 400, ""},
		{"null enabled", fmt.Sprintf(`{"ids":[%d],"enabled":null}`, ids[0]), 400, ""},
		{"wrong enabled type", fmt.Sprintf(`{"ids":[%d],"enabled":"false"}`, ids[0]), 400, ""},
		{"unknown field", fmt.Sprintf(`{"ids":[%d],"enabled":false,"name":"changed"}`, ids[0]), 400, ""},
		{"trailing object", valid + `{}`, 400, ""},
		{"trailing null", valid + `null`, 400, ""},
		{"trailing invalid token", valid + `!`, 400, ""},
		{"malformed JSON", `{"ids":[`, 400, ""},
		{"zero id", fmt.Sprintf(`{"ids":[%d,0],"enabled":false}`, ids[0]), 400, ""},
		{"negative id", fmt.Sprintf(`{"ids":[%d,-1],"enabled":false}`, ids[0]), 400, ""},
		{"duplicate id", fmt.Sprintf(`{"ids":[%d,%d],"enabled":false}`, ids[0], ids[0]), 400, ""},
		{"missing id order", fmt.Sprintf(`{"ids":[%d,999,%d,888],"enabled":false}`, ids[1], ids[0]), 404, "node 999 not found"},
		{"missing unchanged id", fmt.Sprintf(`{"ids":[%d,999],"enabled":true}`, ids[0]), 404, "node 999 not found"},
	} {
		testContext.Run(testCase.name, func(testContext *testing.T) {
			response := httptest.NewRecorder()
			requestContext, _ := gin.CreateTestContext(response)
			requestContext.Request = httptest.NewRequest(http.MethodPost, "/api/nodes/batch-enabled", strings.NewReader(testCase.body))
			handler.BatchSetNodesEnabled(requestContext)
			if response.Code != testCase.status {
				testContext.Fatalf("错误请求返回 HTTP %d，期望 %d：%s", response.Code, testCase.status, response.Body.String())
			}
			if testCase.error != "" {
				var result struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Error != testCase.error {
					testContext.Fatalf("缺失节点错误未按请求顺序返回：%s", response.Body.String())
				}
			}
			if !reflect.DeepEqual(before, loadBatchEnabledTestNodes(testContext, handler)) || handler.singBoxService.RuntimeStatus().Running {
				testContext.Fatal("无效请求修改了节点或启动了内核")
			}
		})
	}
}

func TestBatchSetNodesEnabledAcrossChunks(testContext *testing.T) {
	handler, commandsPath := newBatchEnabledTestHandler(testContext)
	states := make([]bool, nodeIDsPerQuery+1)
	states[0] = true
	ids := insertBatchEnabledTestNodes(testContext, handler, states...)
	for _, enabled := range []bool{true, false} {
		updated := len(ids)
		if enabled {
			updated--
		}
		response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": enabled}, nil)
		assertBatchEnabledResponse(testContext, response, enabled, updated, len(ids)-updated)
		for _, node := range loadBatchEnabledTestNodes(testContext, handler) {
			if node.Enabled != enabled {
				testContext.Fatalf("跨查询块节点 %d 未更新状态", node.ID)
			}
		}
	}
	before := loadBatchEnabledTestNodes(testContext, handler)
	beforeCommands := batchEnabledKernelCommands(testContext, commandsPath)
	missingIDs := append(slices.Clone(ids), 99999, 88888)
	response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": missingIDs, "enabled": true}, nil)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "node 99999 not found") {
		testContext.Fatalf("跨块缺失 ID 返回错误：HTTP %d，%s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(before, loadBatchEnabledTestNodes(testContext, handler)) || batchEnabledKernelCommands(testContext, commandsPath) != beforeCommands {
		testContext.Fatal("后续块缺失节点导致前面的节点被部分修改")
	}
}

func TestBatchSetNodesEnabledAcrossGroupsAndPolicies(testContext *testing.T) {
	for _, protocol := range []string{"failover", "loadbalance", "urltest"} {
		testContext.Run(protocol, func(testContext *testing.T) {
			client := newGroupTestClient(testContext)
			firstGroup := client.createGroup("first", 33001, true)
			secondGroup := client.createGroup("second", 34001, true)
			first := client.createNode("first-member", firstGroup.ID, true)
			second := client.createNode("second-member", secondGroup.ID, false)
			sources := []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: firstGroup.ID}, {Type: models.PolicyMemberSourceGroup, ID: secondGroup.ID}}
			policy := createLinkedTestPolicyOfType(client, protocol, sources)
			ids := []int{second.ID, policy.ID, first.ID}
			response := postJSON(testContext, client.handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": true}, nil)
			assertBatchEnabledResponse(testContext, response, true, 1, 2)
			policy = assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID}, sources)
			if !slices.Equal(policy.Effective, []int{first.ID, second.ID}) {
				testContext.Fatal("跨分组成员启用后未同步策略有效成员")
			}
			response = postJSON(testContext, client.handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": []int{second.ID, first.ID}, "enabled": false}, nil)
			assertBatchEnabledResponse(testContext, response, false, 2, 0)
			policy = assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID}, sources)
			if !policy.Enabled || len(policy.Effective) != 0 || len(policy.Warnings) == 0 {
				testContext.Fatal("关闭成员后策略未保留引用并清空有效成员")
			}
			response = postJSON(testContext, client.handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": false}, nil)
			assertBatchEnabledResponse(testContext, response, false, 1, 2)
			response = postJSON(testContext, client.handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": true}, nil)
			assertBatchEnabledResponse(testContext, response, true, 3, 0)
			policy = assertPolicyMembers(client, policy.ID, []int{first.ID, second.ID}, sources)
			if !policy.Enabled || !slices.Equal(policy.Effective, []int{first.ID, second.ID}) {
				testContext.Fatal("策略及成员同批开启未恢复有效成员")
			}
			client.assertPort(first.ID, firstGroup.ID, first.InboundPort, false)
			client.assertPort(second.ID, secondGroup.ID, second.InboundPort, false)
		})
	}
}

func TestBatchSetNodesEnabledLegacyDetour(testContext *testing.T) {
	handler, commandsPath := newBatchEnabledTestHandler(testContext)
	ids := insertBatchEnabledTestNodes(testContext, handler, true, true)
	if _, err := handler.db.Exec("UPDATE proxy_nodes SET config=?,upstream_mode=? WHERE id=?", fmt.Sprintf(`{"detour":"node-%d-out"}`, ids[0]), models.UpstreamModeLegacy, ids[1]); err != nil {
		testContext.Fatal(err)
	}
	if response := postJSON(testContext, handler.RestartRuntime, http.MethodPost, "/api/runtime/restart", nil, nil); response.Code != http.StatusOK {
		testContext.Fatal(response.Body.String())
	}
	before := loadBatchEnabledTestNodes(testContext, handler)
	beforeRuntime := handler.singBoxService.RuntimeStatus()
	beforeCommands := batchEnabledKernelCommands(testContext, commandsPath)
	response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids[:1], "enabled": false}, nil)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "undefined enabled node") {
		testContext.Fatalf("单独关闭被引用出站未失败：HTTP %d，%s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(before, loadBatchEnabledTestNodes(testContext, handler)) || handler.singBoxService.RuntimeStatus() != beforeRuntime || batchEnabledKernelCommands(testContext, commandsPath) != beforeCommands {
		testContext.Fatal("detour 校验失败未保留原节点与内核")
	}
	for _, enabled := range []bool{false, true} {
		response = postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": []int{ids[1], ids[0]}, "enabled": enabled}, nil)
		assertBatchEnabledResponse(testContext, response, enabled, 2, 0)
		for _, node := range loadBatchEnabledTestNodes(testContext, handler) {
			if node.Enabled != enabled {
				testContext.Fatal("detour 引用方与被引用方未同批启停")
			}
		}
	}
}

func TestBatchSetNodesEnabledHTTPS(testContext *testing.T) {
	directory := testContext.TempDir()
	setAPITLSDirectories(testContext, directory)
	handler, commandsPath := newBatchEnabledTestHandler(testContext)
	ids := insertBatchEnabledTestNodes(testContext, handler, false, false)
	if _, err := handler.db.Exec("UPDATE proxy_nodes SET inbound_mode=? WHERE id=?", models.InboundModeHTTPS, ids[0]); err != nil {
		testContext.Fatal(err)
	}
	if response := postJSON(testContext, handler.RestartRuntime, http.MethodPost, "/api/runtime/restart", nil, nil); response.Code != http.StatusOK {
		testContext.Fatal(response.Body.String())
	}
	before := loadBatchEnabledTestNodes(testContext, handler)
	beforeCommands := batchEnabledKernelCommands(testContext, commandsPath)
	response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": true}, nil)
	if response.Code != http.StatusBadRequest || !reflect.DeepEqual(before, loadBatchEnabledTestNodes(testContext, handler)) || batchEnabledKernelCommands(testContext, commandsPath) != beforeCommands {
		testContext.Fatalf("缺失 TLS 证书未整批拒绝开启：HTTP %d，%s", response.Code, response.Body.String())
	}
	certPath, keyPath := writeAPITLSCertificate(testContext, directory, "batch-enabled")
	response = postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": true}, nil)
	assertBatchEnabledResponse(testContext, response, true, 2, 0)
	settings, err := models.LoadRuntimeSettings(testContext.Context(), handler.db)
	if err != nil || settings.InboundTLSCertPath != certPath || settings.InboundTLSKeyPath != keyPath {
		testContext.Fatalf("批量开启 HTTPS 未保存自动选择的证书：%v", err)
	}
	for _, path := range []string{certPath, keyPath} {
		if err := os.Remove(path); err != nil {
			testContext.Fatal(err)
		}
	}
	response = postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": false}, nil)
	assertBatchEnabledResponse(testContext, response, false, 2, 0)
	after := loadBatchEnabledTestNodes(testContext, handler)
	if after[0].InboundMode != models.InboundModeHTTPS || after[0].Enabled || after[1].Enabled {
		testContext.Fatal("证书丢失后批量关闭未保留 HTTPS 模式")
	}
}

func TestBatchSetNodesEnabledRollback(testContext *testing.T) {
	for _, failure := range []string{"start", "late database commit"} {
		testContext.Run(failure, func(testContext *testing.T) {
			handler, _ := newBatchEnabledTestHandler(testContext)
			ids := insertBatchEnabledTestNodes(testContext, handler, false, false)
			injectCommitFailure := false
			if failure == "late database commit" {
				service := services.NewSingBoxService(testContext.TempDir(), services.WithLastGoodSnapshotWriter(func(path string, data []byte) error {
					if injectCommitFailure {
						if _, err := handler.db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_batch_enabled BEFORE UPDATE OF enabled ON proxy_nodes
							WHEN NEW.id=%d BEGIN SELECT RAISE(ABORT, 'injected batch commit failure'); END`, ids[1])); err != nil {
							return err
						}
						injectCommitFailure = false
					}
					return os.WriteFile(path, data, 0o600)
				}))
				testContext.Cleanup(func() { _ = service.Stop() })
				handler.singBoxService = service
				handler.nodeMutations = newNodeMutationCoordinator(handler.db, service)
			}
			if response := postJSON(testContext, handler.RestartRuntime, http.MethodPost, "/api/runtime/restart", nil, nil); response.Code != http.StatusOK {
				testContext.Fatal(response.Body.String())
			}
			before := loadBatchEnabledTestNodes(testContext, handler)
			beforeRuntime := handler.singBoxService.RuntimeStatus()
			beforeConfig, err := os.ReadFile(handler.singBoxService.ConfigPath())
			if err != nil {
				testContext.Fatal(err)
			}
			beforeSnapshot, err := os.ReadFile(handler.singBoxService.ConfigPath() + ".last-good")
			if err != nil {
				testContext.Fatal(err)
			}
			if failure == "start" {
				binary := filepath.Join(testContext.TempDir(), "reject-start")
				script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = check ]; then exit 0; fi\nif grep -q '\"node-%d-out\"' \"$3\"; then exit 1; fi\nexec sleep 300\n", ids[0])
				if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
					testContext.Fatal(err)
				}
				testContext.Setenv("SINGBOX_BINARY", binary)
			} else {
				injectCommitFailure = true
			}
			response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", gin.H{"ids": ids, "enabled": true}, nil)
			if response.Code != http.StatusInternalServerError || injectCommitFailure {
				testContext.Fatalf("注入故障未在预期阶段拒绝批量启停：HTTP %d，%s", response.Code, response.Body.String())
			}
			expectedStage := "runtime start stage failed"
			if failure == "late database commit" {
				expectedStage = "database commit failed"
			}
			if !strings.Contains(response.Body.String(), expectedStage) {
				testContext.Fatalf("批量启停未在预期故障阶段失败：%s", response.Body.String())
			}
			if !reflect.DeepEqual(before, loadBatchEnabledTestNodes(testContext, handler)) {
				testContext.Fatal("失败批次未恢复整批节点、检测状态和时间戳")
			}
			afterConfig, err := os.ReadFile(handler.singBoxService.ConfigPath())
			if err != nil || !bytes.Equal(beforeConfig, afterConfig) {
				testContext.Fatalf("失败批次未恢复运行配置：%v", err)
			}
			afterSnapshot, err := os.ReadFile(handler.singBoxService.ConfigPath() + ".last-good")
			if err != nil || !bytes.Equal(beforeSnapshot, afterSnapshot) {
				testContext.Fatalf("失败批次未恢复最后可用配置：%v", err)
			}
			afterRuntime := handler.singBoxService.RuntimeStatus()
			if !afterRuntime.Running || afterRuntime.Degraded || afterRuntime.ActiveConfigHash != beforeRuntime.ActiveConfigHash || afterRuntime.DesiredConfigHash != beforeRuntime.DesiredConfigHash {
				testContext.Fatalf("失败批次未恢复原内核状态：%+v", afterRuntime)
			}
		})
	}
}

func TestBatchSetNodesEnabledRouteRequiresAuthentication(testContext *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := newTestHandler(testContext, nil)
	ids := insertBatchEnabledTestNodes(testContext, handler, true)
	token, _, err := createTestAdminSession(testContext, handler)
	if err != nil {
		testContext.Fatal(err)
	}
	router := gin.New()
	authorized := router.Group("/api")
	authorized.Use(handler.AuthMiddleware())
	authorized.POST("/nodes/batch-enabled", handler.BatchSetNodesEnabled)
	body := fmt.Sprintf(`{"ids":[%d],"enabled":false}`, ids[0])
	for _, authenticated := range []bool{false, true} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/api/nodes/batch-enabled", strings.NewReader(body))
		if authenticated {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		router.ServeHTTP(response, request)
		if authenticated {
			assertBatchEnabledResponse(testContext, response, false, 1, 0)
		} else if response.Code != http.StatusUnauthorized || !loadBatchEnabledTestNodes(testContext, handler)[0].Enabled {
			testContext.Fatal("批量启停路由未阻止未鉴权请求")
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/nodes/batch-enabled", nil))
	if response.Code != http.StatusNotFound {
		testContext.Fatal("批量启停路由不应接受 GET")
	}
}
