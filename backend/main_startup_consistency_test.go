package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"sb-proxy/backend/models"
	"sb-proxy/backend/services"

	_ "modernc.org/sqlite"
)

func TestEnsureSecureConfigDirRepairsExistingPermissions(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := ensureSecureConfigDir(configDir); err != nil {
		t.Fatalf("secure config dir: %v", err)
	}
	info, err := os.Stat(configDir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("config directory mode=%#o want=0700", got)
	}
}

func TestStartupDatabaseFailurePreservesExistingConfigAndReportsDegraded(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config.json")
	originalConfig := []byte(`{"preserved":true}`)
	if err := os.WriteFile(configPath, originalConfig, 0o644); err != nil {
		t.Fatalf("write existing config: %v", err)
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

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	service := services.NewSingBoxService(configDir)
	t.Cleanup(func() { _ = service.Stop() })
	startSingBoxFromDatabase(db, service, configDir)

	currentConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read preserved config: %v", err)
	}
	if string(currentConfig) != string(originalConfig) {
		t.Fatalf("database failure overwrote existing config: got=%q want=%q", currentConfig, originalConfig)
	}
	status := service.RuntimeStatus()
	if !status.Running || !status.Degraded || status.State != "degraded" {
		t.Fatalf("preserved runtime should run while exposing degraded database state: %+v", status)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("preserved sensitive config mode=%#o want=0600", got)
	}
}

func newPolicyStartupTest(t *testing.T) (*sql.DB, *services.SingBoxService, int) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	group, err := models.LoadGroup(t.Context(), db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE node_groups SET allocate_port=FALSE WHERE id=?", group.ID); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "fake-sing-box")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = check ]; then exit 0; fi\nexec sleep 300\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SINGBOX_BINARY", binary)
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "10ms")
	service := services.NewSingBoxService(t.TempDir())
	t.Cleanup(func() { _ = service.Stop() })
	return db, service, group.ID
}

func insertPolicyStartupNode(t *testing.T, db *sql.DB, groupID int, node models.ProxyNode) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO proxy_nodes(id,group_id,name,type,config,sort_order,enabled,updated_at) VALUES (?,?,?,?,?,?,?,'2001-01-01 00:00:00')`, node.ID, groupID, fmt.Sprintf("node-%d", node.ID), node.Type, node.Config, node.SortOrder, node.Enabled); err != nil {
		t.Fatal(err)
	}
}

func TestStartupReconcilesLinkedPolicyPriorityWithoutRewritingManual(t *testing.T) {
	db, service, groupID := newPolicyStartupTest(t)
	linkedConfig := fmt.Sprintf(`{"member_ids":[10,30,20,40],"member_sources":[{"type":"group","id":%d}],"health_check_interval":30}`, groupID)
	disabledConfig := fmt.Sprintf(`{"member_ids":[30,10,20,40],"member_sources":[{"type":"node","id":30},{"type":"group","id":%d},{"type":"node","id":40}],"strategy":"consistent-hashing","health_check_interval":30}`, groupID)
	manualConfig := `{ "member_ids": [30,10] }`
	manualSourcesConfig := `{ "member_ids": [10,30], "member_sources": [{"type":"node","id":30},{"type":"node","id":10}] }`
	correctConfig := fmt.Sprintf(`{ "member_ids": [20,40,10,30], "member_sources": [{"type":"group","id":%d}] }`, groupID)
	for _, node := range []models.ProxyNode{
		{ID: 10, Type: "direct", Config: "{}", SortOrder: 1, Enabled: true},
		{ID: 20, Type: "direct", Config: "{}", SortOrder: 0, Enabled: false},
		{ID: 30, Type: "direct", Config: "{}", SortOrder: 2, Enabled: true},
		{ID: 40, Type: "direct", Config: "{}", SortOrder: 0, Enabled: true},
		{ID: 50, Type: "failover", Config: linkedConfig, SortOrder: 3, Enabled: true},
		{ID: 60, Type: "loadbalance", Config: disabledConfig, SortOrder: 4, Enabled: false},
		{ID: 70, Type: "failover", Config: manualConfig, SortOrder: 5, Enabled: true},
		{ID: 80, Type: "failover", Config: correctConfig, SortOrder: 6, Enabled: false},
		{ID: 90, Type: "failover", Config: manualSourcesConfig, SortOrder: 7, Enabled: true},
	} {
		insertPolicyStartupNode(t, db, groupID, node)
	}
	beforeNodes, err := models.LoadProxyNodes(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	startSingBoxFromDatabase(db, service, filepath.Dir(service.ConfigPath()))
	status := service.RuntimeStatus()
	if !status.Running || status.Degraded || status.ActiveConfigHash != status.DesiredConfigHash {
		t.Fatalf("启动对账后的运行时不可用：%+v", status)
	}
	linked, err := models.LoadProxyNode(t.Context(), db, 50)
	if err != nil {
		t.Fatal(err)
	}
	var linkedPolicy models.PolicyConfig
	if err := json.Unmarshal([]byte(linked.Config), &linkedPolicy); err != nil || !slices.Equal(linkedPolicy.MemberIDs, []int{20, 40, 10, 30}) || !slices.Equal(linkedPolicy.MemberSources, []models.PolicyMemberSource{{Type: models.PolicyMemberSourceGroup, ID: groupID}}) {
		t.Fatalf("存量整组策略未按组内顺序收敛：%+v，%v", linkedPolicy, err)
	}
	if got := service.WithPolicyStatus(linked).Effective; !slices.Equal(got, []int{40, 10, 30}) {
		t.Fatalf("启动运行时未使用最新顺序或未跳过禁用成员：%v", got)
	}
	disabled, err := models.LoadProxyNode(t.Context(), db, 60)
	if err != nil {
		t.Fatal(err)
	}
	var disabledPolicy models.PolicyConfig
	if err := json.Unmarshal([]byte(disabled.Config), &disabledPolicy); err != nil || !slices.Equal(disabledPolicy.MemberIDs, []int{30, 20, 40, 10}) || disabledPolicy.Strategy != "consistent-hashing" || len(disabledPolicy.MemberSources) != 3 {
		t.Fatalf("禁用策略或混合来源未正确收敛：%+v，%v", disabledPolicy, err)
	}
	for _, previous := range beforeNodes {
		if previous.ID != 70 && previous.ID != 80 && previous.ID != 90 {
			continue
		}
		current, err := models.LoadProxyNode(t.Context(), db, previous.ID)
		if err != nil || current.Config != previous.Config || !current.UpdatedAt.Equal(previous.UpdatedAt) {
			t.Fatalf("启动改写了手工策略或已正确的关联策略：id=%d，err=%v", previous.ID, err)
		}
	}
	afterNodes, err := models.LoadProxyNodes(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_repeated_startup_update BEFORE UPDATE ON proxy_nodes BEGIN SELECT RAISE(ABORT, 'startup reconciliation is not idempotent'); END`); err != nil {
		t.Fatal(err)
	}
	startSingBoxFromDatabase(db, service, filepath.Dir(service.ConfigPath()))
	status = service.RuntimeStatus()
	if !status.Running || status.Degraded {
		t.Fatalf("再次启动未保持幂等或运行时不可用：%+v", status)
	}
	restartedNodes, err := models.LoadProxyNodes(t.Context(), db)
	if err != nil || !reflect.DeepEqual(afterNodes, restartedNodes) {
		t.Fatalf("再次启动改写了已收敛的数据：err=%v", err)
	}
}

func TestStartupPolicyReconcileFailurePreservesConfigAndRollsBack(t *testing.T) {
	db, service, groupID := newPolicyStartupTest(t)
	oldPolicy := fmt.Sprintf(`{"member_ids":[10,20],"member_sources":[{"type":"group","id":%d}],"health_check_interval":30}`, groupID)
	for _, node := range []models.ProxyNode{
		{ID: 10, Type: "direct", Config: "{}", SortOrder: 1, Enabled: true},
		{ID: 20, Type: "direct", Config: "{}", SortOrder: 0, Enabled: true},
		{ID: 30, Type: "failover", Config: oldPolicy, SortOrder: 2, Enabled: true},
		{ID: 40, Type: "failover", Config: oldPolicy, SortOrder: 3, Enabled: true},
	} {
		insertPolicyStartupNode(t, db, groupID, node)
	}
	beforeNodes, err := models.LoadProxyNodes(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_startup_policy BEFORE UPDATE OF config ON proxy_nodes WHEN OLD.id=40 BEGIN SELECT RAISE(ABORT, 'injected startup reconciliation failure'); END`); err != nil {
		t.Fatal(err)
	}
	beforeConfig := []byte(`{"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}]}`)
	if err := os.WriteFile(service.ConfigPath(), beforeConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	startSingBoxFromDatabase(db, service, filepath.Dir(service.ConfigPath()))
	afterNodes, err := models.LoadProxyNodes(t.Context(), db)
	if err != nil || !reflect.DeepEqual(beforeNodes, afterNodes) {
		t.Fatalf("启动对账失败未回滚全部策略：err=%v", err)
	}
	afterConfig, err := os.ReadFile(service.ConfigPath())
	if err != nil || !bytes.Equal(beforeConfig, afterConfig) {
		t.Fatalf("启动对账失败覆盖了原运行配置：err=%v", err)
	}
	status := service.RuntimeStatus()
	if !status.Running || !status.Degraded {
		t.Fatalf("启动对账失败未保留原内核并标记降级：%+v", status)
	}
}
