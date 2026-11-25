package services

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	appdb "sb-proxy/backend/database"
)

type LogSettings struct {
	Enabled      bool   `json:"enabled"`
	MinLevel     string `json:"min_level"`
	StoreMode    string `json:"store_mode"`
	RemoteDriver string `json:"remote_driver"`
	Path         string `json:"local_sqlite_path"`
	DSN          string `json:"remote_dsn,omitempty"`
	URL          string `json:"remote_url,omitempty"`
	Token        string `json:"remote_token,omitempty"`
	Days         int    `json:"retention_days"`
	MaxMB        int    `json:"max_size_mb"`
}

type LogEvent struct {
	ID       int64     `json:"id"`
	TS       time.Time `json:"ts"`
	Source   string    `json:"source"`
	Level    string    `json:"level"`
	Message  string    `json:"message"`
	IP       string    `json:"client_ip"`
	Port     int       `json:"inbound_port"`
	Outbound string    `json:"outbound_name"`
	Policy   string    `json:"policy_name"`
	Member   string    `json:"member_name"`
}

type trafficPath struct {
	IP         string `json:"client_ip"`
	Port       int    `json:"inbound_port"`
	Inbound    string `json:"inbound_node_name"`
	Route      string `json:"route_id"`
	OutboundID int    `json:"outbound_node_id"`
	Outbound   string `json:"outbound_node_name"`
	Member     string `json:"policy_member_name"`
}

type trafficKey struct {
	trafficPath
	Minute int64
}
type totals struct {
	Upload   int64 `json:"upload_bytes"`
	Download int64 `json:"download_bytes"`
	Total    int64 `json:"total_bytes"`
}

func (t *totals) add(up, down int64) {
	t.Upload += up
	t.Download += down
	t.Total = t.Upload + t.Download
}

type logStore struct {
	unwritten logBatch
	batchMu   sync.RWMutex
	users     sync.WaitGroup
	db        *sql.DB
	dialect   appdb.Dialect
	owned     bool
	path      string
}

func (s *logStore) close() {
	if s != nil && s.owned {
		_ = s.db.Close()
	}
}

func localLogPath(dir, path string) (string, os.FileInfo, error) {
	denied := errors.New("local SQLite log path must be inside the configuration directory")
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", nil, denied
		}
	}
	base, err := filepath.Abs(dir)
	if err != nil {
		return "", nil, denied
	}
	if filepath.IsAbs(path) {
		path, err = filepath.Rel(base, path)
	}
	if err != nil || path == "." || !filepath.IsLocal(path) {
		return "", nil, denied
	}
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		return "", nil, denied
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return "", nil, denied
	}
	defer root.Close()
	if err = root.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", nil, denied
	}
	entry, err := root.Lstat(path)
	if err != nil && !os.IsNotExist(err) || err == nil && !entry.Mode().IsRegular() {
		return "", nil, denied
	}
	file, err := root.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", nil, denied
	}
	info, statErr := file.Stat()
	if err := file.Close(); err != nil || statErr != nil || !info.Mode().IsRegular() {
		return "", nil, denied
	}
	parent, err := filepath.EvalSymlinks(filepath.Join(base, filepath.Dir(path)))
	if err != nil {
		return "", nil, denied
	}
	resolved := filepath.Join(parent, filepath.Base(path))
	rel, err := filepath.Rel(base, resolved)
	if err != nil || !filepath.IsLocal(rel) || !sameLogFile(resolved, info) {
		return "", nil, denied
	}
	return resolved, info, nil
}

func sameLogFile(path string, expected os.FileInfo) bool {
	current, err := os.Lstat(path)
	return err == nil && current.Mode().IsRegular() && os.SameFile(current, expected)
}

func openLogStore(ctx context.Context, primary *sql.DB, dir string, cfg LogSettings) (*logStore, error) {
	s := &logStore{db: primary, dialect: appdb.DialectFor(primary)}
	if cfg.StoreMode != "primary" {
		var expected os.FileInfo
		s.owned = true
		s.dialect = appdb.Dialect(cfg.RemoteDriver)
		dsn := cfg.DSN
		if cfg.StoreMode == "local_sqlite" {
			s.dialect = appdb.DialectSQLite
		}
		switch s.dialect {
		case appdb.DialectSQLite:
			path := cfg.Path
			if cfg.StoreMode == "independent" {
				path = cfg.DSN
			}
			if path == "" {
				path = "logs.db"
			}
			if cfg.StoreMode == "local_sqlite" {
				var err error
				s.path, expected, err = localLogPath(dir, path)
				if err != nil {
					return nil, err
				}
				dsn = s.path
				break
			}
			if !filepath.IsAbs(path) {
				path = filepath.Join(dir, path)
			}
			var err error
			s.path, err = filepath.Abs(path)
			if err != nil {
				return nil, errors.New("invalid SQLite log path")
			}
			if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
				return nil, errors.New("cannot create SQLite log directory")
			}
			file, err := os.OpenFile(s.path, os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				return nil, errors.New("cannot open SQLite log file")
			}
			_ = file.Close()
			dsn = s.path
		case appdb.DialectTurso:
			dsn = cfg.URL
		}
		var err error
		s.db, err = appdb.OpenLogTarget(ctx, s.dialect, dsn, cfg.Token)
		if err != nil {
			return nil, err
		}
		if expected != nil && !sameLogFile(s.path, expected) {
			s.close()
			return nil, errors.New("SQLite log file changed while opening")
		}
	}
	if err := s.verify(ctx); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

const eventCols = "ts,source,level,message,client_ip,inbound_port,outbound_name,policy_name,member_name"
const eventInsert = "INSERT INTO log_events (" + eventCols + ") VALUES (?,?,?,?,?,?,?,?,?)"
const trafficCols = "bucket_ts,path_key,client_ip,inbound_port,inbound_node_name,route_id,outbound_node_id,outbound_node_name,policy_member_name,upload_bytes,download_bytes"

func (s *logStore) verify(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return errors.New("log database ping failed")
	}
	if s.path != "" {
		var other int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND substr(name,1,7)<>'sqlite_' AND name NOT IN ('log_events','traffic_buckets')").Scan(&other); err != nil || other > 0 {
			return errors.New("SQLite path must identify a dedicated log database")
		}
		var vacuum int
		if err := s.db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&vacuum); err != nil {
			return errors.New("cannot inspect SQLite page reclamation")
		}
		if vacuum != 2 {
			if _, err := s.db.ExecContext(ctx, "PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
				return errors.New("cannot configure SQLite page reclamation")
			}
			if _, err := s.db.ExecContext(ctx, "VACUUM"); err != nil {
				return errors.New("cannot prepare SQLite page reclamation")
			}
		}
		if _, err := s.db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
			return errors.New("cannot enable SQLite WAL")
		}
	}
	id := "INTEGER PRIMARY KEY AUTOINCREMENT"
	switch s.dialect {
	case appdb.DialectPostgres:
		id = "BIGSERIAL PRIMARY KEY"
	case appdb.DialectMySQL:
		id = "BIGINT AUTO_INCREMENT PRIMARY KEY"
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS log_events(id ` + id + `,ts BIGINT NOT NULL,source VARCHAR(16) NOT NULL,level VARCHAR(16) NOT NULL,message TEXT NOT NULL,client_ip VARCHAR(64) NOT NULL,inbound_port INTEGER NOT NULL,outbound_name VARCHAR(255) NOT NULL,policy_name VARCHAR(255) NOT NULL,member_name VARCHAR(255) NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS traffic_buckets(bucket_ts BIGINT NOT NULL,path_key CHAR(64) NOT NULL,client_ip VARCHAR(64) NOT NULL,inbound_port INTEGER NOT NULL,inbound_node_name VARCHAR(255) NOT NULL,route_id VARCHAR(280) NOT NULL,outbound_node_id BIGINT NOT NULL,outbound_node_name VARCHAR(255) NOT NULL,policy_member_name VARCHAR(255) NOT NULL,upload_bytes BIGINT NOT NULL,download_bytes BIGINT NOT NULL,PRIMARY KEY(bucket_ts,path_key))`,
	} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return errors.New("cannot create log or traffic tables")
		}
	}
	if s.dialect == appdb.DialectMySQL {
		var count int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='log_events' AND index_name='idx_log_events_ts'").Scan(&count); err != nil {
			return errors.New("cannot inspect log database index")
		}
		if count == 0 {
			if _, err := s.db.ExecContext(ctx, "CREATE INDEX idx_log_events_ts ON log_events(ts)"); err != nil {
				return errors.New("cannot create log timestamp index")
			}
		}
	} else if _, err := s.db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS idx_log_events_ts ON log_events(ts)"); err != nil {
		return errors.New("cannot create log timestamp index")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin log database write test")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, eventInsert, int64(0), "panel", "INFO", "storage check", "", 0, "", "", ""); err != nil {
		return errors.New("log database write test failed")
	}
	if err = s.writeBucket(ctx, tx, trafficKey{Minute: 0}, totals{Upload: 1}); err != nil {
		return errors.New("traffic database write test failed")
	}
	verificationRows := []string{" FROM log_events WHERE ts=0 AND message='storage check'", " FROM traffic_buckets WHERE bucket_ts=0 AND client_ip='' AND outbound_node_id=0"}
	for _, rows := range verificationRows {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*)"+rows).Scan(&count); err != nil || count == 0 {
			return errors.New("log database read test failed")
		}
	}
	for _, rows := range verificationRows {
		result, err := tx.ExecContext(ctx, "DELETE"+rows)
		if err != nil {
			return errors.New("log database delete test failed")
		}
		count, err := result.RowsAffected()
		if err != nil || count <= 0 {
			return errors.New("log database did not delete its verification row")
		}
	}
	for _, rows := range verificationRows {
		var remaining int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*)"+rows).Scan(&remaining); err != nil || remaining != 0 {
			return errors.New("log database could not verify removal of its verification row")
		}
	}
	if err = tx.Rollback(); err != nil {
		return errors.New("log database rollback test failed")
	}
	return nil
}

func (s *logStore) writeBucket(ctx context.Context, tx *sql.Tx, key trafficKey, value totals) error {
	suffix := " ON CONFLICT(bucket_ts,path_key) DO UPDATE SET upload_bytes=traffic_buckets.upload_bytes+excluded.upload_bytes,download_bytes=traffic_buckets.download_bytes+excluded.download_bytes"
	if s.dialect == appdb.DialectMySQL {
		suffix = " ON DUPLICATE KEY UPDATE upload_bytes=upload_bytes+VALUES(upload_bytes),download_bytes=download_bytes+VALUES(download_bytes)"
	}
	data, err := json.Marshal(key.trafficPath)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO traffic_buckets ("+trafficCols+") VALUES (?,?,?,?,?,?,?,?,?,?,?)"+suffix, key.Minute, fmt.Sprintf("%x", sha256.Sum256(data)), key.IP, key.Port, key.Inbound, key.Route, key.OutboundID, key.Outbound, key.Member, value.Upload, value.Download)
	return err
}

var errCommitUncertain = errors.New("log database commit outcome is unknown")

func (s *logStore) write(ctx context.Context, events []LogEvent, buckets map[trafficKey]totals) error {
	if len(events)+len(buckets) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(events) > 0 {
		stmt, err := tx.PrepareContext(ctx, eventInsert)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range events {
			if _, err = stmt.ExecContext(ctx, e.TS.UnixMilli(), e.Source, e.Level, e.Message, e.IP, e.Port, e.Outbound, e.Policy, e.Member); err != nil {
				return err
			}
		}
	}
	for key, value := range buckets {
		if err = s.writeBucket(ctx, tx, key, value); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errCommitUncertain
	}
	return nil
}

func (s *logStore) size(ctx context.Context) (int64, error) {
	if s.path != "" {
		var size int64
		for _, suffix := range []string{"", "-wal"} {
			info, err := os.Stat(s.path + suffix)
			if err == nil {
				size += info.Size()
			} else if !os.IsNotExist(err) {
				return 0, err
			}
		}
		return size, nil
	}
	var estimate int64
	err := s.db.QueryRowContext(ctx, "SELECT (SELECT COALESCE(SUM(LENGTH(message)+200),0) FROM log_events)+(SELECT COUNT(*)*480 FROM traffic_buckets)").Scan(&estimate)
	return estimate, err
}

func (s *logStore) reclaim(ctx context.Context) error {
	if s.dialect != appdb.DialectSQLite {
		return nil
	}
	var mode int
	if err := s.db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if mode == 2 {
		rows, err := s.db.QueryContext(ctx, "PRAGMA incremental_vacuum")
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
	}
	var busy, logged, checkpointed int
	if err := s.db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logged, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("SQLite checkpoint is busy")
	}
	return nil
}

func (s *logStore) rotate(ctx context.Context, cfg LogSettings) error {
	deleted := false
	if cfg.Days > 0 {
		cutoff := time.Now().AddDate(0, 0, -cfg.Days)
		for table, column := range map[string]string{"log_events": "ts", "traffic_buckets": "bucket_ts"} {
			stamp := cutoff.Unix()
			if column == "ts" {
				stamp = cutoff.UnixMilli()
			}
			result, err := s.db.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+column+"<?", stamp)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			deleted = deleted || count > 0
		}
	}
	if deleted || s.path != "" {
		if err := s.reclaim(ctx); err != nil {
			return err
		}
	}
	if cfg.MaxMB == 0 {
		return nil
	}
	for {
		size, err := s.size(ctx)
		if err != nil || size <= int64(cfg.MaxMB)<<20 {
			return err
		}
		var e, b sql.NullInt64
		if err = s.db.QueryRowContext(ctx, "SELECT (SELECT MIN(ts) FROM log_events),(SELECT MIN(bucket_ts) FROM traffic_buckets)").Scan(&e, &b); err != nil {
			return err
		}
		if !e.Valid && !b.Valid {
			return nil
		}
		var query string
		if e.Valid && (!b.Valid || e.Int64/1000 <= b.Int64) {
			query = "DELETE FROM log_events WHERE id IN (SELECT id FROM (SELECT id FROM log_events ORDER BY ts,id LIMIT 256) AS oldest)"
		} else {
			query = "DELETE FROM traffic_buckets WHERE (bucket_ts,path_key) IN (SELECT bucket_ts,path_key FROM (SELECT bucket_ts,path_key FROM traffic_buckets ORDER BY bucket_ts,path_key LIMIT 256) AS oldest)"
		}
		if _, err = s.db.ExecContext(ctx, query); err != nil {
			return err
		}
		if err = s.reclaim(ctx); err != nil {
			return err
		}
	}
}

type LogFilter struct {
	Source, Level, Query, IP string
	From, To                 time.Time
	Cursor                   int64
	Limit, Port, Outbound    int
}

func (f LogFilter) where(traffic bool) (string, []any) {
	parts := []string{}
	args := []any{}
	add := func(condition string, value any) { parts = append(parts, condition); args = append(args, value) }
	column := "ts"
	timestamp := func(t time.Time) int64 { return t.UnixMilli() }
	if traffic {
		column = "bucket_ts"
		timestamp = func(t time.Time) int64 { return t.Truncate(time.Minute).Unix() }
	}
	if !f.From.IsZero() {
		add(column+">=?", timestamp(f.From))
	}
	if !f.To.IsZero() {
		add(column+"<=?", timestamp(f.To))
	}
	if f.IP != "" {
		add("client_ip=?", f.IP)
	}
	if traffic {
		if f.Port > 0 {
			add("inbound_port=?", f.Port)
		}
		if f.Outbound > 0 {
			add("outbound_node_id=?", f.Outbound)
		}
	} else {
		if f.Source != "" {
			add("source=?", f.Source)
		}
		if f.Level != "" {
			add("level=?", f.Level)
		}
		if f.Cursor > 0 {
			add("id<?", f.Cursor)
		}
	}
	if f.Query != "" {
		cols := []string{"client_ip", "outbound_name", "policy_name", "member_name", "message"}
		if traffic {
			cols = []string{"client_ip", "inbound_node_name", "outbound_node_name", "policy_member_name", "route_id"}
		}
		var likes []string
		escaped := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(f.Query))
		for _, col := range cols {
			likes = append(likes, "LOWER("+col+") LIKE ? ESCAPE '!'")
			args = append(args, "%"+escaped+"%")
		}
		parts = append(parts, "("+strings.Join(likes, " OR ")+")")
	}
	if len(parts) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(parts, " AND "), args
}
