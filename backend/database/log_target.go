package database

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/tursodatabase/libsql-client-go/libsql"
)

type libSQLTransport struct{ base http.RoundTripper }
type timedResponseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body timedResponseBody) Close() error {
	defer body.cancel()
	return body.ReadCloser.Close()
}

func (transport libSQLTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, bounded := req.Context().Deadline(); bounded || !strings.HasPrefix(req.Header.Get("X-Libsql-Client-Version"), "libsql-remote-go-") || !strings.HasSuffix(req.URL.Path, "/v2/pipeline") {
		return transport.base.RoundTrip(req)
	}
	ctx, cancel := context.WithTimeout(req.Context(), 4*time.Second)
	response, err := transport.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	response.Body = timedResponseBody{response.Body, cancel}
	return response, nil
}

func init() {
	base := http.DefaultClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	http.DefaultClient.Transport = libSQLTransport{base}
}

func setMySQLLogTimeouts(config *mysql.Config, specified map[string]bool) {
	const timeout = 5 * time.Second
	if !specified["timeout"] {
		config.Timeout = timeout
	}
	if !specified["readTimeout"] {
		config.ReadTimeout = timeout
	}
	if !specified["writeTimeout"] {
		config.WriteTimeout = timeout
	}
}

func OpenLogTarget(ctx context.Context, dialect Dialect, dsn, token string) (*sql.DB, error) {
	var db *sql.DB
	var err error
	switch dialect {
	case DialectSQLite:
		db, err = sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dsn, RawQuery: "_pragma=busy_timeout%281000%29"}).String())
	case DialectPostgres:
		if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
			return nil, errors.New("invalid PostgreSQL connection string")
		}
		db, err = sql.Open(postgresQMarkDriverName, dsn)
	case DialectMySQL:
		var target string
		target, err = mysqlDSNForTarget(dsn, false)
		if err == nil {
			db, err = sql.Open("mysql", target)
		}
	case DialectTurso:
		address, err := url.Parse(dsn)
		if err != nil || address.Hostname() == "" || (address.Scheme != "libsql" && address.Scheme != "https" && address.Scheme != "http") {
			return nil, errors.New("invalid Turso address: expected a libsql, https or http URL")
		}
		var options []libsql.Option
		if token != "" {
			options = append(options, libsql.WithAuthToken(token))
		}
		connector, err := libsql.NewConnector(dsn, options...)
		if err != nil {
			return nil, errors.New("invalid libSQL connection settings")
		}
		db = sql.OpenDB(connector)
	default:
		return nil, errors.New("unsupported log database driver")
	}
	if err != nil {
		return nil, errors.New("invalid database connection settings")
	}
	db.SetMaxOpenConns(2)
	if dialect == DialectSQLite || dialect == DialectTurso {
		db.SetMaxOpenConns(1)
	}
	if dialect == DialectTurso {
		db.SetMaxIdleConns(0)
		db.SetConnMaxLifetime(time.Nanosecond)
	}
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errors.New("log database connection failed")
	}
	return db, nil
}
