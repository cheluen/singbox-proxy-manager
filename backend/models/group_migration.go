package models

import (
	"database/sql"
	"fmt"
	"strings"

	appdb "sb-proxy/backend/database"
)

func groupSQLTypes(dialect appdb.Dialect) (string, string) {
	switch dialect {
	case appdb.DialectPostgres:
		return "BIGSERIAL PRIMARY KEY", "TIMESTAMPTZ"
	case appdb.DialectMySQL:
		return "BIGINT AUTO_INCREMENT PRIMARY KEY", "DATETIME(6)"
	default:
		return "INTEGER PRIMARY KEY AUTOINCREMENT", "DATETIME"
	}
}

func createGroupTable(db *sql.DB, dialect appdb.Dialect) error {
	key, timestamp := groupSQLTypes(dialect)
	now := "CURRENT_TIMESTAMP"
	if dialect == appdb.DialectMySQL {
		now = "CURRENT_TIMESTAMP(6)"
	}
	_, err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS node_groups (
		id %s, name VARCHAR(255) NOT NULL, sort_order INTEGER NOT NULL, allocate_port BOOLEAN NOT NULL DEFAULT TRUE,
		start_port INTEGER NOT NULL, subscription_url VARCHAR(4096) NOT NULL DEFAULT '',
		subscription_auto_update BOOLEAN NOT NULL DEFAULT FALSE, subscription_interval_minutes INTEGER NOT NULL DEFAULT 60,
		subscription_last_updated_at %s NULL, subscription_last_error VARCHAR(2048) NOT NULL DEFAULT '',
		revision BIGINT NOT NULL DEFAULT 0, created_at %s DEFAULT %s, updated_at %s DEFAULT %s)`, key, timestamp, timestamp, now, timestamp, now))
	return err
}

func migrateGroupNodes(db *sql.DB, dialect appdb.Dialect) error {
	columns, err := loadSchemaColumns(db, dialect, "proxy_nodes")
	if err != nil {
		return err
	}
	_, timestamp := groupSQLTypes(dialect)
	fields := []struct{ name, definition string }{
		{"group_id", "BIGINT NULL"}, {"subscription_key", "VARCHAR(64) NOT NULL DEFAULT ''"}, {"subscription_name", "VARCHAR(255) NOT NULL DEFAULT ''"},
		{"check_status", "VARCHAR(16) NOT NULL DEFAULT 'untested'"}, {"check_error", "VARCHAR(2048) NOT NULL DEFAULT ''"}, {"checked_at", timestamp + " NULL"}, {"check_revision", "BIGINT NOT NULL DEFAULT 0"},
	}
	for _, field := range fields {
		if _, ok := columns[field.name]; !ok {
			if _, err = db.Exec("ALTER TABLE proxy_nodes ADD COLUMN " + field.name + " " + field.definition); err != nil {
				return err
			}
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO node_groups (name,sort_order,allocate_port,start_port)
		SELECT 'Default',0,TRUE,CASE WHEN start_port BETWEEN 1024 AND 65535 THEN start_port ELSE 30001 END
		FROM settings WHERE singleton_key=1 AND NOT EXISTS (SELECT 1 FROM node_groups)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE proxy_nodes SET group_id=(SELECT id FROM node_groups ORDER BY sort_order,id LIMIT 1),
		check_status=CASE WHEN node_ip<>'' AND latency>0 THEN 'ok' ELSE 'untested' END,
		latency=CASE WHEN COALESCE(node_ip,'')='' AND latency>0 THEN 0 ELSE latency END WHERE group_id IS NULL`)
	if err != nil {
		return err
	}
	if (dialect == appdb.DialectSQLite || dialect == appdb.DialectTurso) && (!strings.EqualFold(columns["group_id"].isNullable, "NO") || columns["inbound_port"].isNullable == "NO") {
		if err = rebuildGroupNodes(tx, dialect); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if dialect == appdb.DialectPostgres || dialect == appdb.DialectMySQL {
		if columns["inbound_port"].isNullable == "NO" || columns["group_id"].isNullable != "NO" {
			alter := "ALTER TABLE proxy_nodes ALTER COLUMN inbound_port DROP NOT NULL, ALTER COLUMN group_id SET NOT NULL"
			if dialect == appdb.DialectMySQL {
				alter = "ALTER TABLE proxy_nodes MODIFY inbound_port INTEGER NULL, MODIFY group_id BIGINT NOT NULL"
			}
			if _, err = db.Exec(alter); err != nil {
				return err
			}
		}
		var count int
		schema := "current_schema()"
		if dialect == appdb.DialectMySQL {
			schema = "DATABASE()"
		}
		if err = db.QueryRow("SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema=" + schema + " AND table_name='proxy_nodes' AND constraint_name='fk_proxy_group'").Scan(&count); err != nil {
			return err
		}
		if count == 0 {
			if _, err = db.Exec("ALTER TABLE proxy_nodes ADD CONSTRAINT fk_proxy_group FOREIGN KEY(group_id) REFERENCES node_groups(id)"); err != nil {
				return err
			}
		}
	}
	if err = ensureUniqueIndex(db, dialect, "idx_proxy_nodes_port", "proxy_nodes", "inbound_port"); err != nil {
		return err
	}
	if dialect == appdb.DialectMySQL {
		var count int
		if err = db.QueryRow("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='proxy_nodes' AND index_name='idx_proxy_groups_order'").Scan(&count); err != nil || count > 0 {
			return err
		}
		_, err = db.Exec("CREATE INDEX idx_proxy_groups_order ON proxy_nodes(group_id,sort_order,id)")
	} else {
		_, err = db.Exec("CREATE INDEX IF NOT EXISTS idx_proxy_groups_order ON proxy_nodes(group_id,sort_order,id)")
	}
	return err
}

func rebuildGroupNodes(tx *sql.Tx, dialect appdb.Dialect) error {
	var sequence int64
	var hasSequence bool
	if err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='sqlite_sequence')").Scan(&hasSequence); err != nil {
		return err
	}
	if hasSequence {
		if err := tx.QueryRow("SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name='proxy_nodes'").Scan(&sequence); err != nil {
			return err
		}
	}
	rows, err := tx.Query("SELECT sql FROM sqlite_master WHERE tbl_name='proxy_nodes' AND type IN ('index','trigger') AND sql IS NOT NULL")
	if err != nil {
		return err
	}
	var objects []string
	for rows.Next() {
		var statement string
		if err = rows.Scan(&statement); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, statement)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	create := strings.Replace(schemaStatements(dialect)[0], "IF NOT EXISTS proxy_nodes", "sbpm_grouped_nodes", 1)
	for _, statement := range append([]string{create, "INSERT INTO sbpm_grouped_nodes (" + ProxyNodeSelectColumns + ") SELECT " + ProxyNodeSelectColumns + " FROM proxy_nodes", "DROP TABLE proxy_nodes", "ALTER TABLE sbpm_grouped_nodes RENAME TO proxy_nodes"}, objects...) {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	if !hasSequence {
		return nil
	}
	if _, err = tx.Exec("UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name='proxy_nodes'", sequence); err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO sqlite_sequence(name,seq) SELECT 'proxy_nodes',? WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='proxy_nodes')", sequence)
	return err
}
