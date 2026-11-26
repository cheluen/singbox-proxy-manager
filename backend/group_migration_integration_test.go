package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
)

//go:embed testdata/legacy-v1.7.29/*.sql
var legacyGroupSchemas embed.FS

const legacyGroupStartPort = 36773
const legacyPinnedPort = 31237

func seedLegacyGroupDatabase(t *testing.T, db *sql.DB) int {
	t.Helper()
	dialect := appdb.DialectFor(db)
	if dialect == appdb.DialectTurso {
		dialect = appdb.DialectSQLite
	}
	ddl, err := legacyGroupSchemas.ReadFile("testdata/legacy-v1.7.29/" + string(dialect) + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	for statement := range strings.SplitSeq(string(ddl), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("创建 1.7.29 历史表失败：%v", err)
			}
		}
	}
	adminHash, err := bcrypt.GenerateFromPassword([]byte("migration-login-fixture"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	envHash, err := bcrypt.GenerateFromPassword([]byte("migration-env-fixture"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings (
		admin_password, admin_password_env_fingerprint, auth_generation, start_port, preserve_inbound_ports,
		global_upstream_enabled, global_upstream_type, global_upstream_config, global_upstream_ip,
		global_upstream_location, global_upstream_country_code, global_upstream_latency, global_upstream_error,
		node_list_column_widths, created_at, updated_at
	) VALUES (?, ?, 7, ?, TRUE, TRUE, 'socks5', '{"server":"192.0.2.2","server_port":1080}',
		'192.0.2.2', 'Global location', 'GB', 39, 'previous upstream result', '{"name":211}',
		'2025-08-11 12:01:02', '2025-09-12 13:02:03')`, string(adminHash), string(envHash), legacyGroupStartPort); err != nil {
		t.Fatal(err)
	}
	sessionHash := sha256.Sum256([]byte("historical-session-fixture"))
	if _, err := db.Exec(`INSERT INTO admin_sessions (token_hash, auth_generation, expires_at, user_agent, ip, created_at)
		VALUES (?, 7, ?, 'Migration client', '192.0.2.17', '2025-10-13 14:03:04')`, hex.EncodeToString(sessionHash[:]), time.Now().Add(24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	seeds := []struct {
		name, ip string
		latency  int
	}{
		{"healthy-pinned", "198.51.100.2", 48},
		{"purged-middle", "", 0},
		{"ip-only", "198.51.100.3", 0},
		{"latency-only", "", 62},
		{"never-checked", "", 0},
		{"purged-high", "", 0},
	}
	for index, seed := range seeds {
		if _, err := db.Exec(`INSERT INTO proxy_nodes (
			name, remark, type, config, inbound_port, inbound_port_pinned, username, password, tcp_reuse_enabled,
			upstream_mode, upstream_type, upstream_config, upstream_ip, upstream_location, upstream_country_code,
			upstream_latency, upstream_error, sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at
		) VALUES (?, ?, 'socks5h', '{"server":"192.0.2.5","server_port":1080,"username":"remote","password":"outbound-fixture"}',
			?, ?, ?, ?, ?, 'custom', 'http', '{"server":"192.0.2.8","server_port":8080}',
			'192.0.2.8', 'Upstream location', 'NZ', 34, 'upstream check detail', ?, ?, 'Node location', 'CA', ?, ?,
			'2025-04-11 10:20:30', '2025-05-12 11:21:31')`,
			seed.name, "历史备注 "+seed.name, legacyPinnedPort+index*11, index == 0, "user-"+seed.name, "pass-"+seed.name,
			index%2 != 0, (len(seeds)-index)*3, seed.ip, seed.latency, seed.name != "ip-only"); err != nil {
			t.Fatalf("写入历史节点 %q 失败：%v", seed.name, err)
		}
	}
	var highID int
	if err := db.QueryRow("SELECT id FROM proxy_nodes WHERE name = 'purged-high'").Scan(&highID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE proxy_nodes SET inbound_port = NULL WHERE id = ?", highID); err == nil {
		t.Fatal("1.7.29 的入口端口应当拒绝 NULL")
	}
	if _, err := db.Exec("DELETE FROM proxy_nodes WHERE name IN ('purged-middle', 'purged-high')"); err != nil {
		t.Fatal(err)
	}
	return highID
}

type legacyGroupProjection struct {
	table, columns string
	rows           [][]sql.NullString
}

func (projection legacyGroupProjection) read(t *testing.T, db *sql.DB) [][]sql.NullString {
	t.Helper()
	rows, err := db.Query("SELECT " + projection.columns + " FROM " + projection.table + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var records [][]sql.NullString
	for rows.Next() {
		record := make([]sql.NullString, len(columns))
		destinations := make([]any, len(columns))
		for index := range record {
			destinations[index] = &record[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatalf("读取 %s 历史字段失败：%v", projection.table, err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return records
}

func verifyLegacyGroupMigration(t *testing.T, withDatabase func(func(*sql.DB))) {
	t.Helper()
	projections := []legacyGroupProjection{
		{table: "proxy_nodes", columns: `id, name, remark, type, config, inbound_port, inbound_port_pinned, username, password,
			tcp_reuse_enabled, upstream_mode, upstream_type, upstream_config, upstream_ip, upstream_location, upstream_country_code,
			upstream_latency, upstream_error, sort_order, node_ip, location, country_code, latency, enabled, created_at, updated_at`},
		{table: "settings", columns: `id, singleton_key, admin_password, admin_password_env_fingerprint, auth_generation,
			global_upstream_enabled, global_upstream_type, global_upstream_config, global_upstream_ip,
			global_upstream_location, global_upstream_country_code, global_upstream_latency, global_upstream_error,
			node_list_column_widths, created_at, updated_at`},
		{table: "admin_sessions", columns: "id, token_hash, auth_generation, expires_at, user_agent, ip, created_at"},
	}
	t.Cleanup(func() { withDatabase(func(db *sql.DB) { resetApplicationTables(t, db) }) })
	var highID int
	withDatabase(func(db *sql.DB) {
		resetApplicationTables(t, db)
		highID = seedLegacyGroupDatabase(t, db)
		for index, projection := range projections {
			expected := projection
			if projection.table == "proxy_nodes" {
				expected.columns = strings.Replace(projection.columns, "country_code, latency, enabled", "country_code, CASE WHEN COALESCE(node_ip,'')='' AND latency>0 THEN 0 ELSE latency END, enabled", 1)
			}
			projections[index].rows = expected.read(t, db)
		}
	})
	var beforeGroups []models.NodeGroup
	var beforeNodes []models.ProxyNode
	withDatabase(func(db *sql.DB) {
		if err := models.InitDB(db); err != nil {
			t.Fatalf("升级 1.7.29 数据库失败：%v", err)
		}
		verifyLegacyPreserveSettingAbsent(t, db)
		var logSettings string
		if err := db.QueryRow("SELECT COALESCE(log_settings_json,'{}') FROM settings WHERE singleton_key=1").Scan(&logSettings); err != nil || logSettings != "{}" {
			t.Fatalf("升级后的日志设置 %q, %v", logSettings, err)
		}
		if _, err := db.Exec(`UPDATE settings SET log_settings_json='{"enabled":false}' WHERE singleton_key=1`); err != nil {
			t.Fatal(err)
		}
		for _, projection := range projections {
			if !reflect.DeepEqual(projection.read(t, db), projection.rows) {
				t.Fatalf("升级改变了 %s 的历史字段或行", projection.table)
			}
		}
		groups, nodes := migratedGroupState(t, db)
		if len(groups) != 1 || groups[0].ID <= 0 || groups[0].Name == "" || groups[0].SortOrder != 0 ||
			!groups[0].AllocatePort || !groups[0].PreserveInboundPorts || groups[0].StartPort != legacyGroupStartPort || groups[0].NodeCount != 4 || groups[0].EnabledNodeCount != 3 {
			t.Fatalf("默认分组未继承旧端口和全部节点：%+v", groups)
		}
		if len(nodes) != 4 {
			t.Fatalf("迁移后节点数量为 %d，预期 4", len(nodes))
		}
		settings, err := models.LoadRuntimeSettings(t.Context(), db)
		if err != nil || settings.InboundTLSCertPath != "" || settings.InboundTLSKeyPath != "" {
			t.Fatalf("旧库的默认 TLS 选择错误：%+v，%v", settings, err)
		}
		for _, node := range nodes {
			if node.InboundMode != models.InboundModeMixed {
				t.Fatalf("历史节点 %d 未保留 mixed 入站模式", node.ID)
			}
			if node.Name == "latency-only" && node.Latency != 0 {
				t.Fatal("无 IP 的旧测速应清零")
			}
			status := "untested"
			if node.Name == "healthy-pinned" {
				status = "ok"
			}
			if node.GroupID != groups[0].ID || node.CheckStatus != status || node.CheckError != "" || node.CheckRevision != 0 ||
				node.SubscriptionKey != "" || node.SubscriptionName != "" || (status == "untested" && node.CheckedAt != nil) {
				t.Fatalf("历史节点 %d 分组或检测状态错误，分组 %d，状态 %q", node.ID, node.GroupID, node.CheckStatus)
			}
		}
		advanceMigratedGroupState(t, db, groups[0].ID, highID)
		if _, err := db.Exec("UPDATE proxy_nodes SET inbound_mode=? WHERE name='healthy-pinned'", models.InboundModeHTTPS); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("UPDATE settings SET inbound_tls_cert_path=?,inbound_tls_key_path=? WHERE singleton_key=1", "/app/certs/fullchain.pem", "/app/keys/privkey.pem"); err != nil {
			t.Fatal(err)
		}
		beforeGroups, beforeNodes = migratedGroupState(t, db)
	})
	withDatabase(func(db *sql.DB) {
		if err := models.InitDB(db); err != nil {
			t.Fatalf("重新打开已迁移数据库失败：%v", err)
		}
		verifyLegacyPreserveSettingAbsent(t, db)
		var logSettings string
		if err := db.QueryRow("SELECT log_settings_json FROM settings WHERE singleton_key=1").Scan(&logSettings); err != nil || logSettings != `{"enabled":false}` {
			t.Fatalf("重新迁移改变了日志设置 %q, %v", logSettings, err)
		}
		groups, nodes := migratedGroupState(t, db)
		settings, err := models.LoadRuntimeSettings(t.Context(), db)
		if err != nil || settings.InboundTLSCertPath != "/app/certs/fullchain.pem" || settings.InboundTLSKeyPath != "/app/keys/privkey.pem" {
			t.Fatalf("重启改变了入站 TLS 选择：%+v，%v", settings, err)
		}
		if !reflect.DeepEqual(groups, beforeGroups) || !reflect.DeepEqual(nodes, beforeNodes) {
			t.Fatal("重启后分组、端口或显式检测结果发生变化")
		}
		byName := make(map[string]models.ProxyNode, len(nodes))
		for _, node := range nodes {
			byName[node.Name] = node
		}
		pinned := byName["healthy-pinned"]
		if !pinned.InboundPortPinned || pinned.InboundPort != legacyPinnedPort || pinned.CheckStatus != "failed" ||
			pinned.CheckError != "probe timed out" || pinned.CheckedAt == nil || pinned.CheckRevision != 9 {
			t.Fatal("重启后无法读回固定端口和最近一次失败检测的完整状态")
		}
		if partial := byName["ip-only"]; partial.NodeIP == "" || partial.Latency != 75 || partial.CheckStatus != "untested" {
			t.Fatal("历史指标补全后，重启覆盖了显式未检测状态")
		}
		if _, err := db.Exec("UPDATE proxy_nodes SET inbound_port = ? WHERE id = ?", legacyPinnedPort, byName["latency-only"].ID); err == nil {
			t.Fatal("重启后入口端口丢失唯一约束")
		}
		for _, projection := range projections[1:] {
			if !reflect.DeepEqual(projection.read(t, db), projection.rows) {
				t.Fatalf("重启改变了 %s 的历史字段或行", projection.table)
			}
		}
		var nullPorts int
		if err := db.QueryRow("SELECT COUNT(*) FROM proxy_nodes WHERE inbound_port IS NULL").Scan(&nullPorts); err != nil || nullPorts != 2 {
			t.Fatalf("无端口分组的 SQL NULL 数量为 %d，预期 2，%v", nullPorts, err)
		}
	})
}

func migratedGroupState(t *testing.T, db *sql.DB) ([]models.NodeGroup, []models.ProxyNode) {
	t.Helper()
	groups, err := models.LoadGroups(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := models.LoadProxyNodes(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return groups, nodes
}

func verifyLegacyPreserveSettingAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query("SELECT * FROM settings WHERE singleton_key=1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range columns {
		if column == "preserve_inbound_ports" {
			t.Fatal("旧版全局保留入站端口字段未删除")
		}
	}
}

func verifyGroupedPreserveUpgrade(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, legacyPreserve := range []bool{false, true} {
		t.Run(strconv.FormatBool(legacyPreserve), func(t *testing.T) {
			resetApplicationTables(t, db)
			defer resetApplicationTables(t, db)
			if err := models.InitDB(db); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE node_groups SET name='Existing default',start_port=31000,revision=17`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO node_groups(name,sort_order,allocate_port,start_port,upstream_config,revision)
				VALUES ('Existing allocated',1,TRUE,32000,'',9),('Existing portless',2,FALSE,33000,'',12)`); err != nil {
				t.Fatal(err)
			}
			groups, err := models.LoadGroups(t.Context(), db)
			if err != nil || len(groups) != 3 {
				t.Fatalf("准备历史分组失败：%+v，%v", groups, err)
			}
			for index, group := range groups {
				port := 0
				if group.AllocatePort {
					port = 31731 + index
				}
				if _, err := db.Exec(`INSERT INTO proxy_nodes(name,type,config,inbound_port,inbound_port_pinned,sort_order,enabled,group_id,username,password,upstream_mode,upstream_config)
					VALUES (?,'direct','{}',?,?,?,?,?,'historical-user','historical-auth','none','')`, group.Name, models.NullablePort(port), index == 0, 7+index, index == 0, group.ID); err != nil {
					t.Fatal(err)
				}
			}
			beforeGroups, beforeNodes := migratedGroupState(t, db)
			settingsProjection := legacyGroupProjection{table: "settings", columns: models.SettingsSnapshotColumns}
			beforeSettings := settingsProjection.read(t, db)
			if _, err := db.Exec("ALTER TABLE node_groups DROP COLUMN preserve_inbound_ports"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("ALTER TABLE settings ADD COLUMN preserve_inbound_ports BOOLEAN NOT NULL DEFAULT FALSE"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE settings SET preserve_inbound_ports=? WHERE singleton_key=1", legacyPreserve); err != nil {
				t.Fatal(err)
			}
			if err := models.InitDB(db); err != nil {
				t.Fatalf("已有分组的保留策略迁移失败：%v", err)
			}
			verifyLegacyPreserveSettingAbsent(t, db)
			afterGroups, afterNodes := migratedGroupState(t, db)
			for index := range beforeGroups {
				beforeGroups[index].PreserveInboundPorts = legacyPreserve && beforeGroups[index].AllocatePort
			}
			if !reflect.DeepEqual(beforeGroups, afterGroups) || !reflect.DeepEqual(beforeNodes, afterNodes) || !reflect.DeepEqual(beforeSettings, settingsProjection.read(t, db)) {
				t.Fatal("迁移未正确复制分组保留策略，或改变了节点端口、固定状态、认证、排序与其他历史字段")
			}
			if _, err := db.Exec("UPDATE node_groups SET preserve_inbound_ports=? WHERE id=?", !legacyPreserve, groups[0].ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO node_groups(name,sort_order,start_port,upstream_config) VALUES ('New default policy',3,34000,'')`); err != nil {
				t.Fatal(err)
			}
			restartGroups, restartNodes := migratedGroupState(t, db)
			if restartGroups[len(restartGroups)-1].PreserveInboundPorts {
				t.Fatal("迁移后新建分组不应继承旧版全局保留策略")
			}
			if err := models.InitDB(db); err != nil {
				t.Fatal(err)
			}
			verifyLegacyPreserveSettingAbsent(t, db)
			afterGroups, afterNodes = migratedGroupState(t, db)
			if !reflect.DeepEqual(restartGroups, afterGroups) || !reflect.DeepEqual(restartNodes, afterNodes) {
				t.Fatal("重新初始化覆盖了已修改的分组保留策略或节点状态")
			}
		})
	}
}

func verifyGroupUpstreamUpgrade(t *testing.T, db *sql.DB) {
	t.Helper()
	resetApplicationTables(t, db)
	defer resetApplicationTables(t, db)
	if err := models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE node_groups SET name='historical',revision=17,subscription_url='https://example.invalid/feed'`); err != nil {
		t.Fatal(err)
	}
	old, err := models.LoadGroup(t.Context(), db, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"enabled", "type", "config", "ip", "location", "country_code", "latency", "error", "revision"} {
		if _, err := db.Exec("ALTER TABLE node_groups DROP COLUMN upstream_" + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("ALTER TABLE settings DROP COLUMN global_upstream_revision"); err != nil {
		t.Fatal(err)
	}
	if err := models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	var globalRevision int
	if err := db.QueryRow("SELECT global_upstream_revision FROM settings WHERE singleton_key=1").Scan(&globalRevision); err != nil || globalRevision != 0 {
		t.Fatalf("upgraded global revision %d: %v", globalRevision, err)
	}
	group, err := models.LoadGroup(t.Context(), db, old.ID)
	if err != nil || !reflect.DeepEqual(old, group) {
		t.Fatalf("group changed on upgrade: %+v, %v", group, err)
	}
	if _, err := db.Exec(`UPDATE node_groups SET upstream_enabled=TRUE,upstream_type='http',upstream_config='{"server":"192.0.2.2","server_port":8080}',upstream_ip='192.0.2.1',upstream_location='City',upstream_country_code='ZZ',upstream_latency=8,upstream_error='warning',upstream_revision=3 WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	if appdb.DialectFor(db) == appdb.DialectMySQL {
		if _, err := db.Exec(`ALTER TABLE node_groups MODIFY COLUMN upstream_ip VARCHAR(64) NOT NULL DEFAULT '', MODIFY COLUMN upstream_country_code VARCHAR(16) NOT NULL DEFAULT ''`); err != nil {
			t.Fatal(err)
		}
	}
	if err := models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	group, err = models.LoadGroup(t.Context(), db, old.ID)
	if err != nil || !group.UpstreamEnabled || group.UpstreamType != "http" || group.UpstreamConfig == "" || group.UpstreamIP != "192.0.2.1" || group.UpstreamLocation != "City" || group.UpstreamCountryCode != "ZZ" || group.UpstreamLatency != 8 || group.UpstreamError != "warning" || group.UpstreamRevision != 3 {
		t.Fatal("saved upstream fields lost on restart", err)
	}
	if appdb.DialectFor(db) == appdb.DialectMySQL {
		verifyMySQLLongTextContract(t, db)
		for column, want := range map[string]int{"upstream_ip": 255, "upstream_country_code": 32} {
			var width int
			if err := db.QueryRow(`SELECT character_maximum_length FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='node_groups' AND column_name=?`, column).Scan(&width); err != nil || width != want {
				t.Fatalf("upgraded %s width %d, want %d: %v", column, width, want, err)
			}
		}
		ip, country := strings.Repeat("x", 255), strings.Repeat("z", 32)
		if _, err := db.Exec(`UPDATE node_groups SET upstream_ip=?,upstream_country_code=? WHERE id=?`, ip, country, old.ID); err != nil {
			t.Fatal(err)
		}
		if group, err = models.LoadGroup(t.Context(), db, old.ID); err != nil || group.UpstreamIP != ip || group.UpstreamCountryCode != country {
			t.Fatal("full-length check result was not preserved", err)
		}
	}
}

func advanceMigratedGroupState(t *testing.T, db *sql.DB, defaultID, highID int) {
	t.Helper()
	if _, err := db.Exec(`UPDATE node_groups SET name = 'Retained group', start_port = 38200 WHERE id = ?`, defaultID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE proxy_nodes SET check_status = 'failed', check_error = 'probe timed out',
		checked_at = '2025-12-01 23:41:55', check_revision = 9 WHERE name = 'healthy-pinned'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE proxy_nodes SET latency = 75 WHERE name = 'ip-only'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO node_groups (name, sort_order, allocate_port, start_port, upstream_config) VALUES ('Second group', 1, TRUE, 41200, '')`); err != nil {
		t.Fatal(err)
	}
	var secondID int
	if err := db.QueryRow("SELECT id FROM node_groups WHERE name = 'Second group'").Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO proxy_nodes (name, type, config, upstream_config, sort_order, group_id, inbound_port)
		VALUES ('new-first', 'direct', '{}', '', 0, ?, 41200), ('new-second', 'direct', '{}', '', 1, ?, 41201)`, secondID, secondID); err != nil {
		t.Fatal(err)
	}
	var firstNewID, secondNewID int
	if err := db.QueryRow("SELECT MIN(id), MAX(id) FROM proxy_nodes WHERE group_id = ?", secondID).Scan(&firstNewID, &secondNewID); err != nil || firstNewID <= highID || secondNewID <= firstNewID {
		t.Fatalf("迁移重用了已分配 ID，历史最大值 %d，新值 %d 和 %d，%v", highID, firstNewID, secondNewID, err)
	}
	if _, err := db.Exec("UPDATE proxy_nodes SET inbound_port = ? WHERE id = ?", legacyPinnedPort, firstNewID); err == nil {
		t.Fatal("不同分组的节点占用了同一个非空入口端口")
	}
	if _, err := db.Exec("UPDATE proxy_nodes SET group_id = NULL WHERE id = ?", firstNewID); err == nil {
		t.Fatal("分组外键允许 NULL")
	}
	if _, err := db.Exec("UPDATE proxy_nodes SET inbound_port = NULL WHERE group_id = ?", secondID); err != nil {
		t.Fatalf("数据库拒绝了同组两个 NULL 端口：%v", err)
	}
	if _, err := db.Exec("UPDATE node_groups SET allocate_port = FALSE WHERE id = ?", secondID); err != nil {
		t.Fatal(err)
	}
}
