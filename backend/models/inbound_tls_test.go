package models

import (
	"database/sql"
	"strings"
	"testing"

	appdb "sb-proxy/backend/database"
)

func TestNormalizeInboundMode(t *testing.T) {
	for _, test := range []struct{ raw, want string }{{"", InboundModeMixed}, {"mixed", InboundModeMixed}, {" HTTPS ", InboundModeHTTPS}} {
		mode, err := NormalizeInboundMode(test.raw)
		if err != nil || mode != test.want {
			t.Fatalf("入站模式 %q 规范化为 %q，错误 %v", test.raw, mode, err)
		}
	}
	if _, err := NormalizeInboundMode("socks5"); err == nil {
		t.Fatal("接受了不支持的入站模式")
	}
}

func TestInboundTLSSchemaAndMigrationDialects(t *testing.T) {
	for _, dialect := range []appdb.Dialect{appdb.DialectSQLite, appdb.DialectTurso, appdb.DialectPostgres, appdb.DialectMySQL} {
		t.Run(string(dialect), func(t *testing.T) {
			statements := schemaStatements(dialect)
			modeType, pathType := "TEXT", "TEXT"
			if dialect == appdb.DialectMySQL {
				modeType, pathType = "VARCHAR(16)", "VARCHAR(4096)"
			}
			if !strings.Contains(statements[0], "inbound_mode "+modeType+" NOT NULL DEFAULT 'mixed'") {
				t.Fatal("节点建表没有默认 mixed 入站模式")
			}
			migrations := map[string]string{}
			for _, migration := range migrationColumns(dialect) {
				migrations[migration.Name] = migration.AlterSQL
			}
			if !strings.Contains(migrations["inbound_mode"], "inbound_mode "+modeType+" NOT NULL DEFAULT 'mixed'") {
				t.Fatal("老节点表没有入站模式迁移")
			}
			for _, column := range []string{"inbound_tls_cert_path", "inbound_tls_key_path"} {
				definition := column + " " + pathType + " NOT NULL DEFAULT ''"
				if !strings.Contains(statements[1], definition) || !strings.Contains(migrations[column], definition) || !strings.Contains(SettingsSnapshotColumns, column) {
					t.Fatalf("证书选择字段 %s 未覆盖建表、迁移或事务快照", column)
				}
			}
		})
	}
}

func TestInboundTLSSettingsAndModePersistAcrossMigration(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := InitDB(db); err != nil {
		t.Fatal(err)
	}
	group, err := LoadGroup(t.Context(), db, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO proxy_nodes(name,type,config,sort_order,group_id) VALUES ('mode-test','direct','{}',0,?)`, group.ID); err != nil {
		t.Fatal(err)
	}
	nodes, err := LoadProxyNodes(t.Context(), db)
	if err != nil || len(nodes) != 1 || nodes[0].InboundMode != InboundModeMixed {
		t.Fatalf("新节点默认模式错误：%+v，%v", nodes, err)
	}
	if _, err := db.Exec("UPDATE proxy_nodes SET inbound_mode='https' WHERE id=?", nodes[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE settings SET inbound_tls_cert_path=?,inbound_tls_key_path=? WHERE singleton_key=1", "/app/certs/fullchain.pem", "/app/keys/privkey.pem"); err != nil {
		t.Fatal(err)
	}
	if err := InitDB(db); err != nil {
		t.Fatal(err)
	}
	node, err := LoadProxyNode(t.Context(), db, nodes[0].ID)
	if err != nil || node.InboundMode != InboundModeHTTPS {
		t.Fatalf("重复迁移改变了保存的入站模式：%+v，%v", node, err)
	}
	settings, err := LoadRuntimeSettings(t.Context(), db)
	if err != nil || settings.InboundTLSCertPath != "/app/certs/fullchain.pem" || settings.InboundTLSKeyPath != "/app/keys/privkey.pem" {
		t.Fatalf("证书选择未持久化：%+v，%v", settings, err)
	}
}
