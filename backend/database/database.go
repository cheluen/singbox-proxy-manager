package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/tursodatabase/libsql-client-go/libsql"
	sqlite "modernc.org/sqlite"
)

type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectTurso    Dialect = "turso"
	DialectPostgres Dialect = "postgres"
	DialectMySQL    Dialect = "mysql"
)

const postgresQMarkDriverName = "pgx-qmark"

var (
	dialectByDB sync.Map
	sqliteHook  sync.Once
)

func init() {
	sql.Register(postgresQMarkDriverName, &qmarkDriver{driver: stdlib.GetDefaultDriver()})
}

func RegisterDialect(db *sql.DB, dialect Dialect) {
	if db == nil || dialect == "" {
		return
	}
	dialectByDB.Store(db, dialect)
}

func DialectFor(db *sql.DB) Dialect {
	if db == nil {
		return DialectSQLite
	}
	if dialect, ok := dialectByDB.Load(db); ok {
		if typed, ok := dialect.(Dialect); ok && typed != "" {
			return typed
		}
	}
	return DialectSQLite
}

func Open(configDir string) (*sql.DB, error) {
	if dbURL := firstNonEmptyEnv("DATABASE_URL", "POSTGRES_DATABASE_URL", "POSTGRES_URL", "PGSQL_DATABASE_URL", "PGSQL", "MYSQL_DATABASE_URL", "MYSQL"); dbURL != "" {
		db, dialect, err := OpenURL(dbURL)
		if err != nil {
			return nil, err
		}
		RegisterDialect(db, dialect)
		log.Printf("Using %s database", dialect)
		return db, nil
	}

	tursoURL := os.Getenv("TURSO_DATABASE_URL")
	tursoToken := os.Getenv("TURSO_AUTH_TOKEN")
	if tursoURL != "" && tursoToken != "" {
		connector, err := libsql.NewConnector(tursoURL, libsql.WithAuthToken(tursoToken))
		if err != nil {
			return nil, err
		}

		db := sql.OpenDB(connector)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, err
		}

		RegisterDialect(db, DialectTurso)
		log.Printf("Using Turso database: %s", tursoURL)
		return db, nil
	}
	if tursoURL != "" || tursoToken != "" {
		log.Printf("Turso config incomplete (need both TURSO_DATABASE_URL and TURSO_AUTH_TOKEN), falling back to local sqlite")
	}

	return openSQLite(configDir)
}

func OpenURL(rawURL string) (*sql.DB, Dialect, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, "", fmt.Errorf("database URL is empty")
	}

	scheme := strings.ToLower(strings.TrimSpace(strings.SplitN(rawURL, ":", 2)[0]))
	switch scheme {
	case "postgres", "postgresql":
		db, err := sql.Open(postgresQMarkDriverName, rawURL)
		if err != nil {
			return nil, "", err
		}
		configureRemotePool(db, DialectPostgres)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, "", err
		}
		return db, DialectPostgres, nil
	case "mysql":
		dsn, err := normalizeMySQLDSN(rawURL)
		if err != nil {
			return nil, "", err
		}
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			return nil, "", err
		}
		configureRemotePool(db, DialectMySQL)
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, "", err
		}
		return db, DialectMySQL, nil
	default:
		// Also accept native go-sql-driver DSNs through MYSQL_DATABASE_URL.
		if strings.Contains(rawURL, "@tcp(") || strings.Contains(rawURL, "@unix(") {
			db, err := sql.Open("mysql", ensureMySQLDSNDefaults(rawURL))
			if err != nil {
				return nil, "", err
			}
			configureRemotePool(db, DialectMySQL)
			if err := db.Ping(); err != nil {
				db.Close()
				return nil, "", err
			}
			return db, DialectMySQL, nil
		}
		return nil, "", fmt.Errorf("unsupported database URL scheme %q", scheme)
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func openSQLite(configDir string) (*sql.DB, error) {
	sqliteHook.Do(func() {
		sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
			if _, err := conn.ExecContext(context.Background(), "PRAGMA foreign_keys = ON", nil); err != nil {
				return fmt.Errorf("sqlite init failed (foreign_keys): %w", err)
			}
			if _, err := conn.ExecContext(context.Background(), "PRAGMA busy_timeout = 10000", nil); err != nil {
				return fmt.Errorf("sqlite init failed (busy_timeout): %w", err)
			}
			if _, err := conn.ExecContext(context.Background(), "PRAGMA journal_mode = WAL", nil); err != nil {
				return fmt.Errorf("sqlite init failed (journal_mode): %w", err)
			}
			return nil
		})
	})

	dbPath := filepath.Join(configDir, "proxy.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}

	RegisterDialect(db, DialectSQLite)
	log.Printf("Using local sqlite database: %s", dbPath)
	return db, nil
}

func configureRemotePool(db *sql.DB, dialect Dialect) {
	db.SetMaxOpenConns(readIntEnv("DB_MAX_OPEN_CONNS", 10))
	db.SetMaxIdleConns(readIntEnv("DB_MAX_IDLE_CONNS", 5))
	if dialect == DialectMySQL {
		db.SetConnMaxLifetime(readDurationEnv("DB_CONN_MAX_LIFETIME", 3*time.Minute))
		return
	}
	db.SetConnMaxLifetime(readDurationEnv("DB_CONN_MAX_LIFETIME", 30*time.Minute))
}

func readIntEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	var value int
	if _, err := fmt.Sscanf(raw, "%d", &value); err != nil || value < 0 {
		return fallback
	}
	return value
}

func readDurationEnv(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func normalizeMySQLDSN(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if strings.ToLower(parsed.Scheme) != "mysql" {
		return "", fmt.Errorf("unsupported mysql URL scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("mysql URL host is required")
	}

	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = parsed.Host
	cfg.User = parsed.User.Username()
	if password, ok := parsed.User.Password(); ok {
		cfg.Passwd = password
	}
	cfg.DBName = normalizeMySQLDatabaseName(strings.TrimPrefix(parsed.Path, "/"))
	cfg.Params = map[string]string{}

	query := parsed.Query()
	for key, values := range query {
		if len(values) == 0 {
			continue
		}
		cfg.Params[key] = values[len(values)-1]
	}
	if !hasQueryKey(query, "parseTime") {
		cfg.Params["parseTime"] = "true"
	}
	if !hasQueryKey(query, "tls") && shouldDefaultMySQLTLS(parsed.Hostname()) {
		cfg.Params["tls"] = "true"
	}

	return cfg.FormatDSN(), nil
}

func normalizeMySQLDatabaseName(name string) string {
	name = strings.TrimSpace(name)
	if override := strings.TrimSpace(os.Getenv("MYSQL_DATABASE_NAME")); override != "" {
		return override
	}
	switch strings.ToLower(name) {
	case "", "sys", "mysql", "information_schema", "performance_schema":
		return "test"
	default:
		return name
	}
}

func ensureMySQLDSNDefaults(dsn string) string {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return dsn
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.DBName = normalizeMySQLDatabaseName(cfg.DBName)
	if _, ok := cfg.Params["parseTime"]; !ok && !cfg.ParseTime {
		cfg.Params["parseTime"] = "true"
	}
	if _, ok := cfg.Params["tls"]; !ok && cfg.TLSConfig == "" && shouldDefaultMySQLTLS(hostFromMySQLAddr(cfg.Addr)) {
		cfg.Params["tls"] = "true"
	}
	return cfg.FormatDSN()
}

func hasQueryKey(values url.Values, key string) bool {
	for candidate := range values {
		if strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}

func shouldDefaultMySQLTLS(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" || host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsUnspecified()
}

func hostFromMySQLAddr(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}

func LastInsertID(ctx context.Context, tx *sql.Tx, result sql.Result, dialect Dialect) (int64, error) {
	if result != nil {
		if id, err := result.LastInsertId(); err == nil && id > 0 {
			return id, nil
		}
	}
	if dialect == DialectPostgres {
		var id int64
		if err := tx.QueryRowContext(ctx, "SELECT LASTVAL()").Scan(&id); err != nil {
			return 0, err
		}
		return id, nil
	}
	if result == nil {
		return 0, fmt.Errorf("missing sql result")
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

type qmarkDriver struct {
	driver driver.Driver
}

func (d *qmarkDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.driver.Open(name)
	if err != nil {
		return nil, err
	}
	return &qmarkConn{Conn: conn}, nil
}

type qmarkConn struct {
	driver.Conn
}

func (c *qmarkConn) Prepare(query string) (driver.Stmt, error) {
	return c.Conn.Prepare(QMarkToPostgres(query))
}

func (c *qmarkConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	if preparer, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return preparer.PrepareContext(ctx, QMarkToPostgres(query))
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if execer, ok := c.Conn.(driver.ExecerContext); ok {
		return execer.ExecContext(ctx, QMarkToPostgres(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if queryer, ok := c.Conn.(driver.QueryerContext); ok {
		return queryer.QueryContext(ctx, QMarkToPostgres(query), args)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if beginner, ok := c.Conn.(driver.ConnBeginTx); ok {
		return beginner.BeginTx(ctx, opts)
	}
	return nil, driver.ErrSkip
}

func (c *qmarkConn) Ping(ctx context.Context) error {
	if pinger, ok := c.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}

func (c *qmarkConn) ResetSession(ctx context.Context) error {
	if resetter, ok := c.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}

func (c *qmarkConn) IsValid() bool {
	if validator, ok := c.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}
