package api

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"sync/atomic"
	"testing"

	"modernc.org/sqlite"

	"sb-proxy/backend/models"
)

type restartReadCounts struct {
	begins  atomic.Int64
	queries atomic.Int64
	writes  atomic.Int64
	rows    atomic.Int64
}

type restartReadConnector struct {
	counts *restartReadCounts
}

func (connector *restartReadConnector) Driver() driver.Driver { return &sqlite.Driver{} }

func (connector *restartReadConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := connector.Driver().Open(":memory:")
	if err != nil {
		return nil, err
	}
	return &restartReadConn{Conn: conn, counts: connector.counts}, nil
}

type restartReadConn struct {
	driver.Conn
	counts *restartReadCounts
}

func (conn *restartReadConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}

func (conn *restartReadConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	conn.counts.begins.Add(1)
	return conn.Conn.(driver.ConnBeginTx).BeginTx(ctx, options)
}

func (conn *restartReadConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	conn.counts.writes.Add(1)
	return conn.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (conn *restartReadConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	conn.counts.queries.Add(1)
	rows, err := conn.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &restartReadRows{Rows: rows, counts: conn.counts}, nil
}

type restartReadRows struct {
	driver.Rows
	counts *restartReadCounts
}

func (rows *restartReadRows) Next(values []driver.Value) error {
	err := rows.Rows.Next(values)
	if err == nil {
		rows.counts.rows.Add(1)
	}
	return err
}

func TestRestartRuntimeReadOnlyUsesTwoQueries(t *testing.T) {
	handler := newTestHandler(t, nil)
	counts := &restartReadCounts{}
	db := sql.OpenDB(&restartReadConnector{counts: counts})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if err := models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	handler.db = db
	handler.nodeMutations = newNodeMutationCoordinator(db, handler.singBoxService)
	for index := range 16 {
		insertTestNodeWithPortAndOrder(t, db, "restart-node", 31001+index, index)
	}
	nodesBefore := postJSON(t, handler.GetNodes, http.MethodGet, "/api/nodes", nil, nil).Body.String()
	settingsBefore := postJSON(t, handler.GetSettings, http.MethodGet, "/api/settings", nil, nil).Body.String()
	counts.begins.Store(0)
	counts.queries.Store(0)
	counts.writes.Store(0)
	counts.rows.Store(0)

	response := postJSON(t, handler.RestartRuntime, http.MethodPost, "/api/runtime/restart", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("重启失败：status=%d body=%s", response.Code, response.Body.String())
	}
	begins, queries, writes, scanned := counts.begins.Load(), counts.queries.Load(), counts.writes.Load(), counts.rows.Load()
	t.Logf("16 个节点重启：事务=%d 查询=%d 写入=%d 扫描行=%d", begins, queries, writes, scanned)
	if begins != 0 || queries != 2 || writes != 0 || scanned != 17 {
		t.Fatalf("重启不应预演数据修改：事务=%d 查询=%d 写入=%d 扫描行=%d", begins, queries, writes, scanned)
	}
	if got := postJSON(t, handler.GetNodes, http.MethodGet, "/api/nodes", nil, nil).Body.String(); got != nodesBefore {
		t.Fatal("重启修改了节点数据")
	}
	if got := postJSON(t, handler.GetSettings, http.MethodGet, "/api/settings", nil, nil).Body.String(); got != settingsBefore {
		t.Fatal("重启修改了设置数据")
	}
}
