package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func TestNodeMutationRemoteDatabaseLifecycleIntegration(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("remote database integration environment is not set")
	}
	resetBatchImportIntegrationTables(t, db)
	t.Cleanup(func() {
		resetBatchImportIntegrationTables(t, db)
		_ = db.Close()
	})
	if err := models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("SINGBOX_TEST_BINARY") == "" {
		t.Fatal("远程节点生命周期测试需要设置 SINGBOX_TEST_BINARY")
	}
	useRealSingBoxForChecks(t)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	service := services.NewSingBoxService(t.TempDir())
	t.Cleanup(func() { _ = service.Stop() })
	handler := NewHandler(db, service)
	handler.skipPortAvailabilityCheck = true
	response := postJSON(t, handler.GetGroups, http.MethodGet, "/api/node-groups", nil, nil)
	var groups []models.NodeGroup
	if err := json.Unmarshal(response.Body.Bytes(), &groups); err != nil || response.Code != http.StatusOK || len(groups) != 1 || groups[0].ID <= 0 {
		t.Fatalf("无法读取初始分组，HTTP %d，%v", response.Code, err)
	}
	groupID := groups[0].ID
	var ids []int
	for _, name := range []string{"first", "second"} {
		response := postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes", map[string]any{
			"name": name, "type": "direct", "config": "{}", "enabled": true, "group_id": groupID,
			"username": "integration-user", "password": "integration-password", "upstream_mode": "global",
		}, nil)
		if response.Code != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
		}
		var node models.ProxyNode
		if err := json.Unmarshal(response.Body.Bytes(), &node); err != nil || node.ID <= 0 || node.GroupID != groupID {
			t.Fatal("创建响应中缺少节点 ID 或所属分组")
		}
		ids = append(ids, node.ID)
	}
	if ids[1] <= ids[0] {
		t.Fatal("replaying reserved IDs broke sequence allocation")
	}
	response = postJSON(t, handler.UpdateSettings, http.MethodPut, "/api/settings", map[string]bool{"preserve_inbound_ports": false}, nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("旧版保留端口 API 未拒绝：%d %s", response.Code, response.Body.String())
	}
	response = postJSON(t, handler.UpdateSettings, http.MethodPut, "/api/settings", map[string]any{
		"admin_password":          "integration-new-admin-password",
		"global_upstream_enabled": true, "global_upstream_type": "socks5",
		"global_upstream_config": `{"server":"198.51.100.9","server_port":1080}`,
	}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("combined settings mutation status=%d body=%s", response.Code, response.Body.String())
	}
	var generation int
	if err := db.QueryRow("SELECT auth_generation FROM settings WHERE singleton_key = 1").Scan(&generation); err != nil || generation != 1 {
		t.Fatalf("admin generation changed incorrectly: generation=%d err=%v", generation, err)
	}
	groupPath := "/api/node-groups/" + strconv.Itoa(groupID)
	response = postJSON(t, handler.UpdateGroup, http.MethodPatch, groupPath, map[string]int{"start_port": 32000}, ginParams("id", strconv.Itoa(groupID)))
	var changedGroup models.NodeGroup
	if err := json.Unmarshal(response.Body.Bytes(), &changedGroup); err != nil || response.Code != http.StatusOK || changedGroup.ID != groupID || changedGroup.StartPort != 32000 {
		t.Fatalf("分组端口更新失败，HTTP %d，%s", response.Code, response.Body.String())
	}
	for index, id := range ids {
		node, err := models.LoadProxyNode(context.Background(), db, id)
		if err != nil || node.InboundPort != 32000+index {
			t.Fatal("更新分组起始端口后节点分配错误")
		}
		response = postJSON(t, handler.UpdateNodeFlags, http.MethodPatch, "/api/nodes/"+strconv.Itoa(id),
			map[string]bool{"tcp_reuse_enabled": false}, ginParams("id", strconv.Itoa(id)))
		if response.Code != http.StatusOK {
			t.Fatalf("flag mutation status=%d body=%s", response.Code, response.Body.String())
		}
	}
	response = postJSON(t, handler.ReorderNodes, http.MethodPost, "/api/nodes/reorder", map[string]any{
		"group_id": groupID,
		"nodes":    []map[string]int{{"id": ids[0], "sort_order": 1}, {"id": ids[1], "sort_order": 0}},
	}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("reorder status=%d body=%s", response.Code, response.Body.String())
	}
	first, err := models.LoadProxyNode(context.Background(), db, ids[0])
	if err != nil || first.SortOrder != 1 || first.InboundPort != 32001 || first.Password != "integration-password" {
		t.Fatal("reorder changed credentials or lost the port assignment")
	}
	response = postJSON(t, handler.UpdateGroup, http.MethodPatch, groupPath,
		map[string]any{"preserve_inbound_ports": true, "start_port": 33000}, ginParams("id", strconv.Itoa(groupID)))
	if err := json.Unmarshal(response.Body.Bytes(), &changedGroup); err != nil || response.Code != http.StatusOK || !changedGroup.PreserveInboundPorts {
		t.Fatalf("远程数据库未保存分组保留策略：%d %s，%v", response.Code, response.Body.String(), err)
	}
	first, err = models.LoadProxyNode(context.Background(), db, ids[0])
	if err != nil || first.InboundPort != 32001 {
		t.Fatal("开启保留策略同时修改起始端口重排了现有端口", err)
	}
	response = postJSON(t, handler.CreateGroup, http.MethodPost, "/api/node-groups",
		map[string]any{"name": "Other allocated", "start_port": 34000}, nil)
	var otherGroup models.NodeGroup
	if err := json.Unmarshal(response.Body.Bytes(), &otherGroup); err != nil || response.Code != http.StatusCreated || otherGroup.PreserveInboundPorts {
		t.Fatalf("远程数据库新建分组默认策略错误：%d %s，%v", response.Code, response.Body.String(), err)
	}
	response = postJSON(t, handler.CreateNode, http.MethodPost, "/api/nodes",
		map[string]any{"name": "other", "type": "direct", "config": "{}", "enabled": true, "group_id": otherGroup.ID}, nil)
	var otherNode models.ProxyNode
	if err := json.Unmarshal(response.Body.Bytes(), &otherNode); err != nil || response.Code != http.StatusCreated {
		t.Fatalf("创建隔离分组节点失败：%d %s，%v", response.Code, response.Body.String(), err)
	}
	if _, err := db.Exec("UPDATE proxy_nodes SET inbound_port=34007,sort_order=9 WHERE id=?", otherNode.ID); err != nil {
		t.Fatal(err)
	}
	response = postJSON(t, handler.UpdateGroup, http.MethodPatch, groupPath,
		map[string]bool{"preserve_inbound_ports": false}, ginParams("id", strconv.Itoa(groupID)))
	if response.Code != http.StatusOK {
		t.Fatalf("关闭分组保留策略失败：%d %s", response.Code, response.Body.String())
	}
	first, err = models.LoadProxyNode(context.Background(), db, ids[0])
	if err != nil || first.InboundPort != 33001 || first.Password != "integration-password" {
		t.Fatal("关闭分组保留策略未按本组排序重排端口，或改变了凭据", err)
	}
	otherNode, err = models.LoadProxyNode(context.Background(), db, otherNode.ID)
	if err != nil || otherNode.InboundPort != 34007 || otherNode.SortOrder != 9 {
		t.Fatal("关闭本组保留策略改变了其他分组", err)
	}
	ids = append(ids, otherNode.ID)
	response = postJSON(t, handler.BatchDeleteNodes, http.MethodPost, "/api/nodes/batch-delete", map[string]any{"ids": ids}, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM proxy_nodes").Scan(&count); err != nil || count != 0 {
		t.Fatal("deleted nodes remained after runtime commit")
	}
}

func TestBatchImportRemoteDatabaseIntegration(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("remote database integration environment is not set")
	}
	resetBatchImportIntegrationTables(t, db)
	t.Cleanup(func() {
		resetBatchImportIntegrationTables(t, db)
		_ = db.Close()
	})
	if err := models.InitDB(db); err != nil {
		t.Fatalf("initialize remote database: %v", err)
	}

	script := `#!/bin/sh
if [ "$1" = "check" ]; then
  if grep -q "unsupported-flow" "$3"; then
    echo "FATAL[0000] decode config: unsupported flow" >&2
    exit 1
  fi
  exit 0
fi
sleep 300
`
	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	service := services.NewSingBoxService(t.TempDir())
	t.Cleanup(func() { _ = service.Stop() })
	handler := NewHandler(db, service)
	handler.skipPortAvailabilityCheck = true

	payload := map[string]interface{}{
		"content": strings.Join([]string{
			"socks5://user:pass@127.0.0.1:1080#remote-valid-socks",
			"vless://00000000-0000-0000-0000-000000000000@127.0.0.1:443?security=tls&flow=unsupported-flow&detour=remote-valid-socks#remote-bad-vless",
		}, "\n"),
		"enabled": true,
	}
	recorder := postJSON(t, handler.BatchImportNodes, http.MethodPost, "/api/nodes/batch-import", payload, nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("remote batch import failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"success":1`) || !strings.Contains(recorder.Body.String(), `"failed":1`) {
		t.Fatalf("remote batch isolation did not report partial success: %s", recorder.Body.String())
	}

	var count int
	var proxyType, name string
	if err := db.QueryRow("SELECT COUNT(*), MIN(type), MIN(name) FROM proxy_nodes").Scan(&count, &proxyType, &name); err != nil {
		t.Fatalf("query remote imported nodes: %v", err)
	}
	if count != 1 || proxyType != "socks5" || name != "remote-valid-socks" {
		t.Fatalf("remote database retained wrong nodes: count=%d type=%q name=%q", count, proxyType, name)
	}
}

func TestBatchSetNodesEnabledRemoteDatabaseIntegration(testContext *testing.T) {
	db := openBatchImportIntegrationDatabase(testContext)
	if db == nil {
		testContext.Skip("需要配置远程数据库集成测试环境")
	}
	resetBatchImportIntegrationTables(testContext, db)
	testContext.Cleanup(func() {
		resetBatchImportIntegrationTables(testContext, db)
		_ = db.Close()
	})
	if err := models.InitDB(db); err != nil {
		testContext.Fatal(err)
	}
	if os.Getenv("SINGBOX_TEST_BINARY") == "" {
		testContext.Fatal("远程批量启停测试需要设置 SINGBOX_TEST_BINARY")
	}
	useRealSingBoxForChecks(testContext)
	testContext.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	service := services.NewSingBoxService(testContext.TempDir())
	testContext.Cleanup(func() { _ = service.Stop() })
	handler := NewHandler(db, service)
	ids := insertBatchEnabledTestNodes(testContext, handler, false, true, false)
	for _, enabled := range []bool{true, true, false, false} {
		before := loadBatchEnabledTestNodes(testContext, handler)
		beforeRuntime := service.RuntimeStatus()
		updated := 0
		for _, node := range before {
			if node.Enabled != enabled {
				updated++
			}
		}
		response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", map[string]any{"ids": ids, "enabled": enabled}, nil)
		assertBatchEnabledResponse(testContext, response, enabled, updated, len(ids)-updated)
		after := loadBatchEnabledTestNodes(testContext, handler)
		for index, node := range after {
			if node.Enabled != enabled {
				testContext.Fatalf("远程节点 %d 未更新启停状态", node.ID)
			}
			if before[index].Enabled == enabled {
				if !reflect.DeepEqual(before[index], node) {
					testContext.Fatalf("远程未变节点 %d 的检测状态或时间戳被改写", node.ID)
				}
			} else if node.CheckRevision != before[index].CheckRevision+1 || node.CheckStatus != "untested" || node.CheckedAt != nil || node.NodeIP != "" {
				testContext.Fatalf("远程已变节点 %d 未重置检测状态", node.ID)
			}
		}
		if updated == 0 && service.RuntimeStatus() != beforeRuntime {
			testContext.Fatal("远程无变化批次重启了内核")
		}
	}
	before := loadBatchEnabledTestNodes(testContext, handler)
	beforeRuntime := service.RuntimeStatus()
	missingID := ids[len(ids)-1] + 1
	response := postJSON(testContext, handler.BatchSetNodesEnabled, http.MethodPost, "/api/nodes/batch-enabled", map[string]any{"ids": []int{ids[0], missingID, ids[1]}, "enabled": true}, nil)
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "node "+strconv.Itoa(missingID)+" not found") {
		testContext.Fatalf("远程缺失节点未返回 404：HTTP %d，%s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(before, loadBatchEnabledTestNodes(testContext, handler)) || service.RuntimeStatus() != beforeRuntime {
		testContext.Fatal("远程缺失节点请求部分修改了数据库或内核")
	}
}

func TestRemoteDatabaseRepeatedNodeSaveIntegration(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("remote database integration environment is not set")
	}
	resetBatchImportIntegrationTables(t, db)
	t.Cleanup(func() {
		resetBatchImportIntegrationTables(t, db)
		_ = db.Close()
	})
	if err := models.InitDB(db); err != nil {
		t.Fatalf("initialize remote database: %v", err)
	}

	fakeBinary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(fakeBinary, []byte("#!/bin/sh\nif [ \"$1\" = \"check\" ]; then exit 0; fi\nexec sleep 300\n"), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	service := services.NewSingBoxService(t.TempDir())
	t.Cleanup(func() { _ = service.Stop() })
	handler := NewHandler(db, service)
	handler.skipPortAvailabilityCheck = true

	if _, err := db.Exec(`
		INSERT INTO proxy_nodes (
			name, remark, type, config, inbound_port, username, password,
			tcp_reuse_enabled, upstream_config, sort_order, latency, enabled, group_id
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT id FROM node_groups ORDER BY sort_order, id LIMIT 1))
	`, "idempotent-save", "same-value", "direct", `{}`, 31001, "", "", true, "", 0, 0, false); err != nil {
		t.Fatalf("insert node for repeated save: %v", err)
	}
	var nodeID int
	if err := db.QueryRow("SELECT id FROM proxy_nodes WHERE name = ?", "idempotent-save").Scan(&nodeID); err != nil {
		t.Fatalf("query repeated-save node: %v", err)
	}

	payload := map[string]interface{}{
		"name":              "idempotent-save",
		"remark":            "same-value",
		"type":              "direct",
		"config":            `{}`,
		"inbound_port":      31001,
		"username":          "",
		"password":          "",
		"auth_enabled":      false,
		"enabled":           false,
		"tcp_reuse_enabled": true,
	}
	for attempt := 1; attempt <= 2; attempt++ {
		path := "/api/nodes/" + strconv.Itoa(nodeID)
		recorder := postJSON(
			t,
			handler.UpdateNode,
			http.MethodPut,
			path,
			payload,
			ginParams("id", strconv.Itoa(nodeID)),
		)
		if recorder.Code != http.StatusOK {
			t.Fatalf("repeated save attempt %d failed: status=%d body=%s", attempt, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRemoteDatabaseCleanupVerificationIntegration(t *testing.T) {
	db := openBatchImportIntegrationDatabase(t)
	if db == nil {
		t.Skip("remote database integration environment is not set")
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM (" + integrationTableQuery(db) + ") AS remaining").Scan(&count); err != nil {
		t.Fatalf("verify remote database cleanup: %v", err)
	}
	if count != 0 {
		t.Fatalf("remote database cleanup left %d application tables", count)
	}
}

func openBatchImportIntegrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	if databaseURL := strings.TrimSpace(os.Getenv("SBPM_BATCH_IMPORT_INTEGRATION_DATABASE_URL")); databaseURL != "" {
		db, dialect, err := appdb.OpenURL(databaseURL)
		if err != nil {
			t.Fatalf("open remote database: %v", err)
		}
		appdb.RegisterDialect(db, dialect)
		return db
	}

	tursoURL := strings.TrimSpace(os.Getenv("SBPM_BATCH_IMPORT_INTEGRATION_TURSO_URL"))
	tursoToken := strings.TrimSpace(os.Getenv("SBPM_BATCH_IMPORT_INTEGRATION_TURSO_TOKEN"))
	if tursoURL == "" && tursoToken == "" {
		return nil
	}
	if tursoURL == "" || tursoToken == "" {
		t.Fatal("Turso 集成测试需要同时设置地址和令牌")
	}
	for _, key := range []string{
		"DATABASE_URL", "POSTGRES_DATABASE_URL", "POSTGRES_URL", "PGSQL_DATABASE_URL", "PGSQL",
		"MYSQL_DATABASE_URL", "MYSQL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("TURSO_DATABASE_URL", tursoURL)
	t.Setenv("TURSO_AUTH_TOKEN", tursoToken)
	db, err := appdb.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open Turso database: %v", err)
	}
	return db
}

func integrationTableQuery(db *sql.DB) string {
	const names = "('admin_sessions', 'proxy_nodes', 'node_groups', 'settings', 'manager_instance_lock')"
	switch appdb.DialectFor(db) {
	case appdb.DialectPostgres:
		return `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name IN ` + names
	case appdb.DialectMySQL:
		return `SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name IN ` + names
	default:
		return `SELECT name FROM sqlite_master WHERE type = 'table' AND name IN ` + names
	}
}

func resetBatchImportIntegrationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(integrationTableQuery(db))
	if err != nil {
		t.Fatalf("inspect integration tables: %v", err)
	}
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			_ = rows.Close()
			t.Fatalf("inspect integration table name: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("inspect integration tables: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close integration table inspection: %v", err)
	}
	for _, table := range []string{"admin_sessions", "proxy_nodes", "node_groups", "settings", "manager_instance_lock"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatalf("reset integration table %s: %v", table, err)
		}
	}
}
