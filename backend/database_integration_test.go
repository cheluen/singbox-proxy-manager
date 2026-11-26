package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"

	"sb-proxy/backend/api"
	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
	"sb-proxy/backend/services"
)

func TestLocalSQLiteDatabaseIntegration(t *testing.T) {
	for _, key := range []string{
		"DATABASE_URL",
		"POSTGRES_DATABASE_URL",
		"POSTGRES_URL",
		"PGSQL_DATABASE_URL",
		"PGSQL",
		"MYSQL_DATABASE_URL",
		"MYSQL",
		"TURSO_DATABASE_URL",
		"TURSO_AUTH_TOKEN",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("ADMIN_PASSWORD", "")

	runDatabaseIntegrationSuite(t, appdb.DialectSQLite)
}

func TestDatabaseURLIntegration(t *testing.T) {
	databaseURL := os.Getenv("SBPM_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SBPM_INTEGRATION_DATABASE_URL is not set")
	}

	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("TURSO_DATABASE_URL", "")
	t.Setenv("TURSO_AUTH_TOKEN", "")
	t.Setenv("ADMIN_PASSWORD", "")

	runDatabaseIntegrationSuite(t, appdb.DialectPostgres, appdb.DialectMySQL)
}

func TestTursoDatabaseIntegration(t *testing.T) {
	tursoURL := os.Getenv("SBPM_INTEGRATION_TURSO_URL")
	tursoToken := os.Getenv("SBPM_INTEGRATION_TURSO_TOKEN")
	if tursoURL == "" && tursoToken == "" {
		t.Skip("SBPM_INTEGRATION_TURSO_URL/SBPM_INTEGRATION_TURSO_TOKEN are not set")
	}
	if tursoURL == "" || tursoToken == "" {
		t.Fatal("Turso 集成测试需要同时设置地址和令牌")
	}

	for _, key := range []string{
		"DATABASE_URL",
		"POSTGRES_DATABASE_URL",
		"POSTGRES_URL",
		"PGSQL_DATABASE_URL",
		"PGSQL",
		"MYSQL_DATABASE_URL",
		"MYSQL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("TURSO_DATABASE_URL", tursoURL)
	t.Setenv("TURSO_AUTH_TOKEN", tursoToken)
	t.Setenv("ADMIN_PASSWORD", "")

	runDatabaseIntegrationSuite(t, appdb.DialectTurso)
}

func runDatabaseIntegrationSuite(t *testing.T, allowed ...appdb.Dialect) {
	t.Helper()
	configDir := t.TempDir()
	withDatabase := func(t *testing.T, verify func(*sql.DB)) {
		t.Helper()
		db, err := appdb.Open(configDir)
		if err != nil {
			t.Fatalf("打开集成测试数据库失败：%v", err)
		}
		defer db.Close()
		if dialect := appdb.DialectFor(db); !slices.Contains(allowed, dialect) {
			t.Fatalf("集成测试连接了错误的数据库 %q", dialect)
		}
		verify(db)
	}
	t.Run("Fresh", func(t *testing.T) {
		withDatabase(t, func(db *sql.DB) {
			runDatabaseIntegrationContract(t, db)
			if appdb.DialectFor(db) == appdb.DialectTurso {
				if got := db.Stats().MaxOpenConnections; got != 1 {
					t.Fatalf("Turso must serialize database operations: max open connections = %d", got)
				}
				verifyTursoConnectionReuse(t, db)
			}
		})
	})
	t.Run("UpgradeFromV1_7_29", func(t *testing.T) {
		verifyLegacyGroupMigration(t, func(verify func(*sql.DB)) { withDatabase(t, verify) })
	})
	t.Run("GroupUpstreamUpgradeFromV1_7_30", func(t *testing.T) { withDatabase(t, func(db *sql.DB) { verifyGroupUpstreamUpgrade(t, db) }) })
	t.Run("GroupPreservePortsUpgrade", func(t *testing.T) { withDatabase(t, func(db *sql.DB) { verifyGroupedPreserveUpgrade(t, db) }) })
}

func runDatabaseIntegrationContract(t *testing.T, db *sql.DB) {
	t.Helper()

	dialect := appdb.DialectFor(db)
	resetApplicationTables(t, db)
	defer resetApplicationTables(t, db)

	if err := models.InitDB(db); err != nil {
		t.Fatalf("init db (%s): %v", dialect, err)
	}
	verifyLegacyPasswordSetupStateAbsent(t, db)
	verifyInstanceLeaseContract(t, db)
	verifyNodeListColumnWidthsContract(t, db)

	// Keep the payload above MySQL TEXT's 64 KiB limit. Protocol configs can
	// legitimately grow this large (for example, a WireGuard endpoint with
	// many peers), so every supported remote backend must preserve it intact.
	largeConfig := `{"server":"example.com","server_port":1080,"padding":"` + strings.Repeat("x", 70<<10) + `"}`
	largeUpstreamConfig := `{"server":"upstream.example.com","server_port":1080,"padding":"` + strings.Repeat("y", 70<<10) + `"}`

	_, err := db.Exec(`
		INSERT INTO proxy_nodes (
			name, remark, type, config, inbound_port, username, password,
			tcp_reuse_enabled, upstream_mode, upstream_type, upstream_config,
			upstream_ip, upstream_location, upstream_country_code,
			upstream_latency, upstream_error, sort_order, latency, enabled, group_id
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (SELECT id FROM node_groups ORDER BY sort_order, id LIMIT 1))
	`, "integration-socks5h", "remote-db", "socks5h", largeConfig, 31001, "user", "pass",
		true, models.UpstreamModeCustom, "socks5", largeUpstreamConfig,
		"198.51.100.40", "Upstream Integration", "UI", 45, "", 0, 0, true)
	if err != nil {
		t.Fatalf("insert node (%s): %v", dialect, err)
	}

	var node models.ProxyNode
	if err := db.QueryRow(`
		SELECT id, name, remark, type, config, inbound_port, inbound_mode, inbound_port_pinned, username, password, tcp_reuse_enabled,
		       upstream_mode, upstream_type, upstream_config, upstream_ip, upstream_location,
		       upstream_country_code, upstream_latency, upstream_error, sort_order, node_ip,
		       location, country_code, latency, enabled, created_at, updated_at, group_id, subscription_key,
		       subscription_name, check_status, check_error, checked_at, check_revision
		FROM proxy_nodes WHERE name = ?
	`, "integration-socks5h").Scan(
		&node.ID, &node.Name, &node.Remark, &node.Type, &node.Config, &node.InboundPort,
		&node.InboundMode,
		&node.InboundPortPinned, &node.Username, &node.Password, &node.TCPReuseEnabled, &node.UpstreamMode,
		&node.UpstreamType, &node.UpstreamConfig, &node.UpstreamIP, &node.UpstreamLocation,
		&node.UpstreamCountryCode, &node.UpstreamLatency, &node.UpstreamError, &node.SortOrder,
		&node.NodeIP, &node.Location, &node.CountryCode, &node.Latency, &node.Enabled,
		&node.CreatedAt, &node.UpdatedAt, &node.GroupID, &node.SubscriptionKey, &node.SubscriptionName,
		&node.CheckStatus, &node.CheckError, &node.CheckedAt, &node.CheckRevision,
	); err != nil {
		t.Fatalf("query node (%s): %v", dialect, err)
	}
	if node.Type != "socks5h" || node.InboundMode != models.InboundModeMixed || !node.TCPReuseEnabled || !node.Enabled || node.CreatedAt.IsZero() || node.UpdatedAt.IsZero() {
		t.Fatalf("unexpected node from %s: %+v", dialect, node)
	}
	if node.Config != largeConfig {
		t.Fatalf("large config was truncated by %s: got=%d want=%d", dialect, len(node.Config), len(largeConfig))
	}
	if node.UpstreamMode != models.UpstreamModeCustom || node.UpstreamType != "socks5" || node.UpstreamConfig != largeUpstreamConfig {
		t.Fatalf("upstream config was not preserved by %s: mode=%q type=%q got=%d want=%d", dialect, node.UpstreamMode, node.UpstreamType, len(node.UpstreamConfig), len(largeUpstreamConfig))
	}
	if node.UpstreamIP != "198.51.100.40" || node.UpstreamLocation != "Upstream Integration" || node.UpstreamCountryCode != "UI" || node.UpstreamLatency != 45 || node.UpstreamError != "" {
		t.Fatalf("upstream IP check state was not preserved by %s: %+v", dialect, node)
	}

	var preserve bool
	if err := db.QueryRow("SELECT preserve_inbound_ports FROM node_groups WHERE id = ?", node.GroupID).Scan(&preserve); err != nil {
		t.Fatalf("query group (%s): %v", dialect, err)
	}
	if preserve {
		t.Fatalf("default preserve_inbound_ports should be false")
	}
	verifyLegacyPreserveSettingAbsent(t, db)
	verifySharedReadContract(t, db, node)

	verifyUpstreamMigrationContract(t, db, largeUpstreamConfig)
	verifyAdminSessionContract(t, db)
	if dialect == appdb.DialectSQLite || dialect == appdb.DialectTurso {
		verifySQLiteWriterSerialization(t, db)
	}
}

func verifyAdminSessionContract(t *testing.T, db *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	const loginPassword = "session-integration-fixture"
	const userAgent = "session-integration"
	const clientIP = "192.0.2.42"
	hash, err := bcrypt.GenerateFromPassword([]byte(loginPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		UPDATE settings SET admin_password = ?, auth_generation = 2 WHERE singleton_key = 1
	`, string(hash)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if _, err := db.Exec(`
		INSERT INTO admin_sessions (token_hash, auth_generation, expires_at)
		VALUES ('expired-fixture', 2, ?), ('old-generation-fixture', 1, ?)
	`, now-1, now+3600); err != nil {
		t.Fatal(err)
	}
	handler := api.NewHandler(db, nil)
	router := gin.New()
	router.POST("/api/login", handler.Login)
	router.POST("/api/logout", handler.AuthMiddleware(), handler.Logout)
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"password":"`+loginPassword+`"}`))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginRequest.Header.Set("User-Agent", userAgent)
	loginRequest.RemoteAddr = clientIP + ":43210"
	loginResponse := httptest.NewRecorder()
	router.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("签发会话失败（%s）：%d %s", appdb.DialectFor(db), loginResponse.Code, loginResponse.Body.String())
	}
	var session struct {
		Token  string `json:"token"`
		Expiry int64  `json:"expiry"`
	}
	if err := json.Unmarshal(loginResponse.Body.Bytes(), &session); err != nil || session.Token == "" || session.Expiry <= now {
		t.Fatalf("签发会话响应字段错误：%v", err)
	}
	var count, generation int
	var expiry int64
	var storedAgent, storedIP string
	var createdAt time.Time
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&count); err != nil || count != 1 {
		t.Fatalf("过期或旧代次会话未清理：count=%d err=%v", count, err)
	}
	if err := db.QueryRow("SELECT auth_generation, expires_at, user_agent, ip, created_at FROM admin_sessions").
		Scan(&generation, &expiry, &storedAgent, &storedIP, &createdAt); err != nil {
		t.Fatal(err)
	}
	if generation != 2 || expiry != session.Expiry || storedAgent != userAgent || storedIP != clientIP || createdAt.IsZero() {
		t.Fatal("会话代次、到期时间或审计列写入错误")
	}
	logoutRequest := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	logoutRequest.Header.Set("Authorization", "Bearer "+session.Token)
	logoutResponse := httptest.NewRecorder()
	router.ServeHTTP(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("注销会话失败：%d %s", logoutResponse.Code, logoutResponse.Body.String())
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM admin_sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("注销后会话仍存在：count=%d err=%v", count, err)
	}
}

func verifyNodeListColumnWidthsContract(t *testing.T, db *sql.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dialect := appdb.DialectFor(db)
	handler := api.NewHandler(db, services.NewSingBoxService(t.TempDir()))
	router := gin.New()
	router.GET("/api/settings", handler.GetSettings)
	router.PUT("/api/settings", handler.UpdateSettings)
	request := func(method, body string, expected map[string]int, changed bool) {
		t.Helper()
		req := httptest.NewRequest(method, "/api/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		var got struct {
			Widths  map[string]int `json:"node_list_column_widths"`
			Changed bool           `json:"changed"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || response.Code != http.StatusOK ||
			got.Widths == nil || !maps.Equal(got.Widths, expected) || got.Changed != changed {
			t.Fatalf("%s settings (%s) = %d %s, parse error %v", method, dialect, response.Code, response.Body.String(), err)
		}
	}
	empty := map[string]int{}
	request(http.MethodGet, "", empty, false)
	if _, err := db.Exec("ALTER TABLE settings DROP COLUMN node_list_column_widths"); err != nil {
		t.Fatalf("prepare pre-width settings schema (%s): %v", dialect, err)
	}
	if err := models.InitDB(db); err != nil {
		t.Fatalf("migrate pre-width settings schema (%s): %v", dialect, err)
	}
	request(http.MethodGet, "", empty, false)
	widths := map[string]int{"port": 40, "server": 1200}
	request(http.MethodPut, `{"node_list_column_widths":{"port":40,"server":1200}}`, widths, true)
	if err := models.InitDB(db); err != nil {
		t.Fatalf("re-run width migration (%s): %v", dialect, err)
	}
	request(http.MethodGet, "", widths, false)
	request(http.MethodPut, `{}`, widths, false)
	request(http.MethodPut, `{"node_list_column_widths":{}}`, empty, true)
	request(http.MethodGet, "", empty, false)
}

func verifySharedReadContract(t *testing.T, db *sql.DB, expected models.ProxyNode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stored, err := models.LoadProxyNode(ctx, db, expected.ID)
	if err != nil || !reflect.DeepEqual(stored, expected) {
		t.Fatalf("共用节点读取与完整列映射不符：node=%+v err=%v", stored, err)
	}
	nodes, err := models.LoadProxyNodes(ctx, db)
	if err != nil || len(nodes) != 1 || !reflect.DeepEqual(nodes[0], expected) {
		t.Fatalf("共用节点列表读取不符：nodes=%d err=%v", len(nodes), err)
	}
	settings, err := models.LoadRuntimeSettings(ctx, db)
	if err != nil || settings.InboundTLSCertPath != "" || settings.InboundTLSKeyPath != "" {
		t.Fatalf("共用设置读取不符：settings=%+v err=%v", settings, err)
	}
	group, err := models.LoadGroup(ctx, db, expected.GroupID)
	if err != nil || group.ID != expected.GroupID || group.StartPort != 30001 || !group.AllocatePort || group.PreserveInboundPorts {
		t.Fatalf("共用分组读取不符：group=%+v err=%v", group, err)
	}
	var settingsID int
	if err := db.QueryRowContext(ctx, "SELECT id FROM settings WHERE singleton_key = 1").Scan(&settingsID); err != nil || settingsID <= 0 {
		t.Fatalf("设置单例不存在：id=%d err=%v", settingsID, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE proxy_nodes SET inbound_port_pinned = ?, inbound_mode = ? WHERE id = ?", true, models.InboundModeHTTPS, expected.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE settings SET inbound_tls_cert_path = ?, inbound_tls_key_path = ? WHERE singleton_key = 1", "/app/certs/fullchain.pem", "/app/keys/privkey.pem"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE node_groups SET start_port = ?, preserve_inbound_ports = ? WHERE id = ?", 32050, true, group.ID); err != nil {
		t.Fatal(err)
	}
	transactionNode, err := models.LoadProxyNode(ctx, tx, expected.ID)
	if err != nil || !transactionNode.InboundPortPinned || transactionNode.InboundMode != models.InboundModeHTTPS {
		t.Fatalf("共用读取未使用事务内的节点状态：%v", err)
	}
	transactionNodes, err := models.LoadProxyNodes(ctx, tx)
	if err != nil || len(transactionNodes) != 1 || !transactionNodes[0].InboundPortPinned || transactionNodes[0].InboundMode != models.InboundModeHTTPS {
		t.Fatalf("共用读取未使用事务内的节点列表：%v", err)
	}
	transactionSettings, err := models.LoadRuntimeSettings(ctx, tx)
	if err != nil || transactionSettings.InboundTLSCertPath != "/app/certs/fullchain.pem" || transactionSettings.InboundTLSKeyPath != "/app/keys/privkey.pem" {
		t.Fatalf("共用读取未使用事务内的设置状态：%v", err)
	}
	transactionGroup, err := models.LoadGroup(ctx, tx, group.ID)
	if err != nil || transactionGroup.StartPort != 32050 || !transactionGroup.PreserveInboundPorts {
		t.Fatalf("共用读取未使用事务内的分组端口：%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := models.LoadProxyNode(ctx, db, expected.ID+10000); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("共用读取未保留节点缺失语义：%v", err)
	}
	canceled, cancelRead := context.WithCancel(ctx)
	cancelRead()
	if _, err := models.LoadProxyNodes(canceled, db); !errors.Is(err, context.Canceled) {
		t.Fatalf("共用列表读取未保留取消语义：%v", err)
	}
}

func verifyTursoConnectionReuse(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	errorCh := make(chan error, 60)
	for range 3 {
		workers.Go(func() {
			for range 20 {
				var value int
				if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&value); err != nil {
					errorCh <- err
				}
			}
		})
	}
	workers.Wait()
	close(errorCh)
	for err := range errorCh {
		t.Errorf("queued Turso read failed: %v", err)
	}
	if stats := db.Stats(); stats.Idle != 0 || stats.MaxLifetimeClosed == 0 {
		t.Fatalf("Turso reused remote streams: %+v", stats)
	}
}

func verifySQLiteWriterSerialization(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	group, err := models.LoadGroup(ctx, db, 0)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin serialized transaction: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE node_groups SET start_port = ? WHERE id = ?", 32001, group.ID); err != nil {
		t.Fatalf("write in transaction: %v", err)
	}
	waitCount := db.Stats().WaitCount
	done := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, "UPDATE node_groups SET start_port = ? WHERE id = ?", 32002, group.ID)
		done <- err
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for db.Stats().WaitCount == waitCount {
		select {
		case err := <-done:
			t.Fatalf("concurrent writer did not wait for the transaction: %v", err)
		case <-deadline.C:
			t.Fatal("concurrent writer never entered the connection queue")
		case <-ticker.C:
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit serialized transaction: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("queued writer failed after commit: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("queued writer did not finish: %v", ctx.Err())
	}
	group, err = models.LoadGroup(ctx, db, group.ID)
	if err != nil || group.StartPort != 32002 {
		t.Fatalf("queued write not persisted: port=%d err=%v", group.StartPort, err)
	}
}

func verifyLegacyPasswordSetupStateAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	dialect := appdb.DialectFor(db)
	var count int
	var err error
	switch dialect {
	case appdb.DialectPostgres:
		err = db.QueryRow(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = 'settings'
			  AND column_name = 'admin_password_set'
		`).Scan(&count)
	case appdb.DialectMySQL:
		err = db.QueryRow(`
			SELECT COUNT(*) FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = 'settings'
			  AND column_name = 'admin_password_set'
		`).Scan(&count)
	default:
		rows, queryErr := db.Query("PRAGMA table_info(settings)")
		if queryErr != nil {
			err = queryErr
			break
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue interface{}
			if scanErr := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); scanErr != nil {
				err = scanErr
				break
			}
			if name == "admin_password_set" {
				count++
			}
		}
		if err == nil {
			err = rows.Err()
		}
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		t.Fatalf("inspect settings schema (%s): %v", dialect, err)
	}
	if count != 0 {
		t.Fatalf("legacy first-start state remains in new %s schema", dialect)
	}
}

func verifyUpstreamMigrationContract(t *testing.T, db *sql.DB, largeUpstreamConfig string) {
	t.Helper()
	dialect := appdb.DialectFor(db)
	if _, err := db.Exec(`
		INSERT INTO proxy_nodes (name, type, config, inbound_port, upstream_config, sort_order, enabled, group_id)
		VALUES ('integration-legacy-detour', 'direct', '{"detour":"direct"}', 31002, '', 1, ?, (SELECT id FROM node_groups ORDER BY sort_order, id LIMIT 1))
	`, true); err != nil {
		t.Fatalf("seed legacy detour node (%s): %v", dialect, err)
	}

	for _, column := range []struct {
		table string
		name  string
	}{
		{table: "proxy_nodes", name: "upstream_mode"},
		{table: "proxy_nodes", name: "upstream_type"},
		{table: "proxy_nodes", name: "upstream_config"},
		{table: "proxy_nodes", name: "upstream_ip"},
		{table: "proxy_nodes", name: "upstream_location"},
		{table: "proxy_nodes", name: "upstream_country_code"},
		{table: "proxy_nodes", name: "upstream_latency"},
		{table: "proxy_nodes", name: "upstream_error"},
		{table: "settings", name: "admin_password_env_fingerprint"},
		{table: "settings", name: "global_upstream_enabled"},
		{table: "settings", name: "global_upstream_type"},
		{table: "settings", name: "global_upstream_config"},
		{table: "settings", name: "global_upstream_ip"},
		{table: "settings", name: "global_upstream_location"},
		{table: "settings", name: "global_upstream_country_code"},
		{table: "settings", name: "global_upstream_latency"},
		{table: "settings", name: "global_upstream_error"},
	} {
		if _, err := db.Exec("ALTER TABLE " + column.table + " DROP COLUMN " + column.name); err != nil {
			t.Fatalf("drop legacy migration column %s.%s (%s): %v", column.table, column.name, dialect, err)
		}
	}
	if err := models.InitDB(db); err != nil {
		t.Fatalf("re-run upstream schema migration (%s): %v", dialect, err)
	}

	rows, err := db.Query("SELECT name, upstream_mode FROM proxy_nodes ORDER BY sort_order")
	if err != nil {
		t.Fatalf("query migrated upstream modes (%s): %v", dialect, err)
	}
	defer rows.Close()
	wantModes := map[string]string{
		"integration-socks5h":       models.UpstreamModeGlobal,
		"integration-legacy-detour": models.UpstreamModeLegacy,
	}
	for rows.Next() {
		var name, mode string
		if err := rows.Scan(&name, &mode); err != nil {
			t.Fatalf("scan migrated upstream mode (%s): %v", dialect, err)
		}
		if want, exists := wantModes[name]; !exists || mode != want {
			t.Fatalf("migrated upstream mode for %q on %s=%q want %q", name, dialect, mode, want)
		}
		delete(wantModes, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate migrated upstream modes (%s): %v", dialect, err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close migrated upstream modes (%s): %v", dialect, err)
	}
	if len(wantModes) != 0 {
		t.Fatalf("missing migrated nodes on %s: %v", dialect, wantModes)
	}

	if _, err := db.Exec(`
		UPDATE proxy_nodes
		SET upstream_mode = ?, upstream_type = 'socks5', upstream_config = ?,
		    upstream_ip = '198.51.100.41', upstream_location = 'Migrated Upstream',
		    upstream_country_code = 'MU', upstream_latency = 46, upstream_error = ''
		WHERE name = 'integration-socks5h'
	`, models.UpstreamModeCustom, largeUpstreamConfig); err != nil {
		t.Fatalf("write migrated node upstream config (%s): %v", dialect, err)
	}
	if _, err := db.Exec(`
		UPDATE settings
		SET global_upstream_enabled = ?, global_upstream_type = 'socks5', global_upstream_config = ?,
		    global_upstream_ip = '198.51.100.42', global_upstream_location = 'Migrated Global',
		    global_upstream_country_code = 'MG', global_upstream_latency = 47,
		    global_upstream_error = ''
		WHERE singleton_key = 1
	`, true, largeUpstreamConfig); err != nil {
		t.Fatalf("write migrated global upstream config (%s): %v", dialect, err)
	}
	var nodeConfig, nodeUpstreamIP, globalConfig, globalUpstreamIP, passwordFingerprint string
	var nodeUpstreamLatency, globalUpstreamLatency int
	if err := db.QueryRow(`
		SELECT node.upstream_config, node.upstream_ip, node.upstream_latency,
		       singleton.global_upstream_config, singleton.global_upstream_ip,
		       singleton.global_upstream_latency, singleton.admin_password_env_fingerprint
		FROM proxy_nodes AS node
		CROSS JOIN settings AS singleton
		WHERE node.name = 'integration-socks5h' AND singleton.singleton_key = 1
	`).Scan(
		&nodeConfig,
		&nodeUpstreamIP,
		&nodeUpstreamLatency,
		&globalConfig,
		&globalUpstreamIP,
		&globalUpstreamLatency,
		&passwordFingerprint,
	); err != nil {
		t.Fatalf("read migrated upstream configs (%s): %v", dialect, err)
	}
	if nodeConfig != largeUpstreamConfig || globalConfig != largeUpstreamConfig {
		t.Fatalf("migrated upstream LONGTEXT truncated on %s: node=%d global=%d want=%d", dialect, len(nodeConfig), len(globalConfig), len(largeUpstreamConfig))
	}
	if nodeUpstreamIP != "198.51.100.41" || nodeUpstreamLatency != 46 || globalUpstreamIP != "198.51.100.42" || globalUpstreamLatency != 47 || passwordFingerprint != "" {
		t.Fatalf(
			"migrated password/upstream check fields failed on %s: node=%q/%d global=%q/%d fingerprint=%q",
			dialect,
			nodeUpstreamIP,
			nodeUpstreamLatency,
			globalUpstreamIP,
			globalUpstreamLatency,
			passwordFingerprint,
		)
	}
	if dialect == appdb.DialectMySQL {
		verifyMySQLLongTextContract(t, db)
	}
}

func verifyMySQLLongTextContract(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, column := range []struct {
		table string
		name  string
	}{
		{table: "proxy_nodes", name: "config"},
		{table: "proxy_nodes", name: "upstream_config"},
		{table: "settings", name: "global_upstream_config"},
		{table: "node_groups", name: "upstream_config"},
	} {
		var dataType, isNullable string
		var defaultValue sql.NullString
		if err := db.QueryRow(`
			SELECT data_type, is_nullable, column_default
			FROM information_schema.columns
			WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?
		`, column.table, column.name).Scan(&dataType, &isNullable, &defaultValue); err != nil {
			t.Fatalf("inspect MySQL column %s.%s: %v", column.table, column.name, err)
		}
		if !strings.EqualFold(dataType, "longtext") || !strings.EqualFold(isNullable, "NO") || defaultValue.Valid {
			t.Fatalf(
				"unexpected MySQL column %s.%s: type=%q nullable=%q default=%q",
				column.table,
				column.name,
				dataType,
				isNullable,
				defaultValue.String,
			)
		}
	}
}

func verifyInstanceLeaseContract(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	first, err := appdb.AcquireInstanceLease(ctx, db)
	if err != nil {
		t.Fatalf("acquire first instance lease: %v", err)
	}
	if _, err := appdb.AcquireInstanceLease(ctx, db); !errors.Is(err, appdb.ErrManagerInstanceActive) {
		_ = first.Release(context.Background())
		t.Fatalf("second manager was not rejected: %v", err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatalf("release first instance lease: %v", err)
	}

	second, err := appdb.AcquireInstanceLease(ctx, db)
	if err != nil {
		t.Fatalf("acquire instance lease after handoff: %v", err)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatalf("release handoff instance lease: %v", err)
	}
}

func resetApplicationTables(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"admin_sessions", "proxy_nodes", "node_groups", "settings", "manager_instance_lock"} {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + table); err != nil {
			t.Fatalf("drop %s: %v", table, err)
		}
	}
}
