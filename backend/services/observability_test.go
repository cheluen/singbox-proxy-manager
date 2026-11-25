package services

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
)

func newTestObserver(t *testing.T) *Observability {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "proxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err = models.InitDB(db); err != nil {
		t.Fatal(err)
	}
	o := NewObservability(db, NewSingBoxService(dir))
	if err = o.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := o.Close(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	return o
}

func TestObservabilityRingFilteringAndSQLiteSwitch(t *testing.T) {
	o := newTestObserver(t)
	writer := o.Writer("singbox")
	fmt.Fprint(writer, "WARN inbound/mixed[node-1-in]: connection from 127.0.0.1:44 password=hidden-credential\n")
	page, err := o.Logs(t.Context(), LogFilter{Level: "WARN", IP: "127.0.0.1", Query: "password"})
	if err != nil || len(page.Items) != 1 || strings.Contains(page.Items[0].Message, "hidden-credential") {
		t.Fatalf("unsafe or missing event %+v, %v", page, err)
	}
	for number := range 1005 {
		fmt.Fprintf(writer, "INFO entry %d\n", number)
	}
	filter := LogFilter{Limit: 200}
	total := 0
	for {
		page, err = o.Logs(t.Context(), filter)
		if err != nil {
			t.Fatal(err)
		}
		total += len(page.Items)
		if page.Next == 0 {
			break
		}
		filter.Cursor = page.Next
	}
	if total != 1000 {
		t.Fatalf("ring retained %d", total)
	}
	path := filepath.Join(o.service.configDir, "logs.db")
	if _, err = os.Stat(path); !os.IsNotExist(err) || o.cancel != nil {
		t.Fatal("memory mode allocated persistent resources")
	}
	input := LogSettingsInput{LogSettings: LogSettings{Enabled: true, MinLevel: "WARN", StoreMode: "local_sqlite", RemoteDriver: "postgres", Days: 1, MaxMB: 1}}
	if err = o.Save(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(writer, "INFO ignore")
	fmt.Fprintln(writer, "WARN persisted event")
	o.flush(false)
	page, err = o.Logs(t.Context(), LogFilter{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Message != "WARN persisted event" || !page.Persisted {
		t.Fatalf("persisted page %+v, %v", page, err)
	}
	bad := input
	bad.StoreMode = "independent"
	bad.DSN = "postgres://user:hide-this-value@127.0.0.1:1/invalid"
	bad.DSNAction = "replace"
	err = o.Save(t.Context(), bad)
	if err == nil || strings.Contains(err.Error(), "hide-this-value") || o.Settings()["store_mode"] != "local_sqlite" {
		t.Fatal("failed validation changed the active database or exposed credentials")
	}
	var saved string
	if err = o.primary.QueryRow("SELECT log_settings_json FROM settings").Scan(&saved); err != nil || strings.Contains(saved, "hide-this") {
		t.Fatal("failed settings were persisted")
	}
	now := time.Now().UTC()
	r := ConnectionRoute{ClientIP: "127.0.0.3", InboundPort: 31001, InboundName: "in", OutboundID: 3, OutboundName: "business", MemberName: "primary"}
	o.collect(now, []routeDelta{{route: r, up: 12, down: 20, opened: true}})
	o.flush(false)
	o.collect(now, []routeDelta{{route: r, up: 3, down: 4}})
	f := LogFilter{IP: r.ClientIP, Port: r.InboundPort, Outbound: r.OutboundID, Query: "business"}
	traffic, err := o.Traffic(t.Context(), f)
	if err != nil || !traffic.Persisted || traffic.Total != 39 || len(traffic.Items) != 1 || len(traffic.Buckets) != 1 {
		t.Fatalf("traffic %+v %v", traffic, err)
	}
	o.flush(false)
	traffic, err = o.Traffic(t.Context(), f)
	if err != nil || traffic.Total != 39 {
		t.Fatalf("double-counted after flush: %+v %v", traffic, err)
	}
	old := now.Add(-72 * time.Hour)
	large := make([]LogEvent, 1300)
	for idx := range large {
		large[idx] = LogEvent{TS: now, Source: "panel", Level: "WARN", Message: strings.Repeat("a", 1800)}
	}
	large[0].TS = old
	o.store.batchMu.Lock()
	err = o.store.write(t.Context(), large, map[trafficKey]totals{{Minute: old.Truncate(time.Minute).Unix()}: {Upload: 5}})
	if err == nil {
		err = o.store.rotate(t.Context(), input.LogSettings)
	}
	size, sizeErr := o.store.size(t.Context())
	var expired int
	readErr := o.store.db.QueryRow("SELECT COUNT(*) FROM traffic_buckets WHERE bucket_ts<?", now.Add(-24*time.Hour).Unix()).Scan(&expired)
	o.store.batchMu.Unlock()
	if err != nil || sizeErr != nil || readErr != nil || expired != 0 || size > 1<<20 {
		t.Fatalf("SQLite rotation: %d bytes, %d expired, errors %v %v %v", size, expired, err, sizeErr, readErr)
	}
	input.Enabled = false
	if err = o.Save(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	if o.cancel != nil || o.Settings()["enabled"] != false {
		t.Fatal("storage worker survived disabling persistence")
	}
}

func TestObservabilityCredentialMaskAndRestore(t *testing.T) {
	o := newTestObserver(t)
	input := LogSettingsInput{LogSettings: LogSettings{StoreMode: "independent", RemoteDriver: "turso", URL: "https://tenant.invalid/sensitive-address", Token: "sensitive-value", MinLevel: "INFO"}, DSNAction: "replace", TokenAction: "replace"}
	if err := o.Save(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	input.DSNAction = "keep"
	input.TokenAction = "keep"
	input.URL = ""
	input.Token = ""
	if err := o.Save(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(o.Settings())
	if strings.Contains(string(public), "sensitive") || o.Settings()["has_remote_token"] != true || o.cfg.Token == "" || o.cfg.URL == "" {
		t.Fatal("stored credential masking or preservation failed")
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	changed := input
	changed.Enabled, changed.DSNAction, changed.URL = true, "replace", server.URL
	before := o.cfg
	for _, action := range []string{"", "keep"} {
		changed.TokenAction = action
		if err := o.Save(t.Context(), changed); err == nil || requests.Load() != 0 || o.cfg != before {
			t.Fatal("changed Turso address accepted the previous token")
		}
	}
	for _, invalid := range []string{"file:///tmp/unapproved.db", "/tmp/unapproved.db", "ftp://example.invalid"} {
		if db, err := appdb.OpenLogTarget(t.Context(), appdb.DialectTurso, invalid, ""); err == nil {
			db.Close()
			t.Fatal("invalid Turso scheme accepted")
		}
	}
	input.TokenAction = "clear"
	input.DSNAction = "clear"
	if err := o.Save(t.Context(), input); err != nil || o.Settings()["has_remote_dsn"] != false || o.Settings()["has_remote_token"] != false {
		t.Fatal("credential clearing failed")
	}
}

func TestObservabilitySQLitePathBoundary(t *testing.T) {
	o := newTestObserver(t)
	dir := o.service.configDir
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "absent.db"), filepath.Join(dir, "linked-file")); err != nil {
		t.Fatal(err)
	}
	cfg := LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "local_sqlite"}}
	for _, path := range []string{filepath.Join(outside, "foreign.db"), "../foreign.db", "linked/new/subdir.db", "linked-file", "proxy.db"} {
		cfg.Path = path
		if err := o.Save(t.Context(), cfg); err == nil {
			t.Fatal("invalid local path accepted")
		}
	}
	contents, err := os.ReadDir(outside)
	if err != nil || len(contents) != 0 {
		t.Fatal("created file outside configuration directory")
	}
	cfg.Path = filepath.Join(dir, "nested", "logs.db")
	if err := o.Save(t.Context(), cfg); err != nil {
		t.Fatal(err)
	}
	alias := cfg
	alias.Path = "internal-link.db"
	if err := os.Symlink(filepath.Join("nested", "logs.db"), filepath.Join(dir, alias.Path)); err != nil {
		t.Fatal(err)
	}
	if err := o.Save(t.Context(), alias); err == nil || o.store.path != cfg.Path {
		t.Fatal("accepted a symbolic link to a log file inside the configuration directory")
	}
	cfg.StoreMode, cfg.RemoteDriver, cfg.DSNAction, cfg.DSN = "independent", "sqlite", "replace", filepath.Join(dir, "proxy.db")
	if err := o.Save(t.Context(), cfg); err == nil || o.store.path != cfg.Path {
		t.Fatal("independent SQLite accepted business database")
	}
}

func TestLogFileReplacedDuringOpen(t *testing.T) {
	dir := t.TempDir()
	path, info, err := localLogPath(dir, "old.db")
	if err != nil || !sameLogFile(path, info) {
		t.Fatalf("same file rejected: %v", err)
	}
	if err = os.Rename(path, path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(path+".saved", path); err != nil {
		t.Fatal(err)
	}
	if sameLogFile(path, info) {
		t.Fatal("accepted substituted symlink")
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if sameLogFile(path, info) {
		t.Fatal("accepted different inode")
	}
}

func TestObservabilityDedicatedTablesAndSilentDelete(t *testing.T) {
	observer := newTestObserver(t)
	settings := LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "local_sqlite"}}
	if err := observer.Save(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	store := observer.store
	for _, table := range []string{"log_events", "traffic_buckets"} {
		if _, err := store.db.Exec("CREATE TRIGGER ignore_delete BEFORE DELETE ON " + table + " BEGIN SELECT RAISE(IGNORE); END"); err != nil {
			t.Fatal(err)
		}
		if err := store.verify(t.Context()); err == nil || !strings.Contains(err.Error(), "verification row") {
			t.Fatalf("silently ignored delete on %s: %v", table, err)
		}
		if _, err := store.db.Exec("DROP TRIGGER ignore_delete"); err != nil {
			t.Fatal(err)
		}
		columns := trafficCols
		if table == "log_events" {
			columns = eventCols
		}
		oldValues := "OLD." + strings.ReplaceAll(columns, ",", ",OLD.")
		statement := "CREATE TRIGGER restore_row AFTER DELETE ON " + table + " BEGIN INSERT INTO " + table + " (" + columns + ") VALUES (" + oldValues + "); END"
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
		if err := store.verify(t.Context()); err == nil || !strings.Contains(err.Error(), "verification row") {
			t.Fatalf("accepted restored verification row in %s: %v", table, err)
		}
		var remaining int
		if err := store.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&remaining); err != nil || remaining != 0 {
			t.Fatalf("verification persisted data in %s: %d %v", table, remaining, err)
		}
		if _, err := store.db.Exec("DROP TRIGGER restore_row"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.Exec("CREATE TABLE sqlitecache(id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := store.verify(t.Context()); err == nil || !strings.Contains(err.Error(), "dedicated") {
		t.Fatalf("business table sqlitecache: %v", err)
	}
}

func TestObservabilityCloseBoundsRetiredStoreWait(t *testing.T) {
	observer := newTestObserver(t)
	release := make(chan struct{})
	finish := sync.OnceFunc(func() { close(release) })
	defer finish()
	observer.retired.Go(func() { <-release })
	start := time.Now()
	if err := observer.Close(); !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 7*time.Second {
		t.Fatalf("unbounded shutdown: %v", err)
	}
	finish()
	if err := observer.Close(); err != nil {
		t.Fatalf("drain after released store: %v", err)
	}
}

func TestObservabilityPrecommitRetry(t *testing.T) {
	observer := newTestObserver(t)
	if err := observer.Save(t.Context(), LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "local_sqlite"}}); err != nil {
		t.Fatal(err)
	}
	db := observer.store.db
	if _, err := db.Exec(`CREATE TRIGGER fail_bucket BEFORE INSERT ON traffic_buckets BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(observer.Writer("panel"), "retry fixture")
	observer.collect(time.Now(), []routeDelta{{route: ConnectionRoute{OutboundName: "retry fixture"}, up: 12}})
	observer.flush(false)
	var rows int
	if err := db.QueryRow("SELECT COUNT(*) FROM log_events").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("partial batch persisted: %d %v", rows, err)
	}
	observer.mu.Lock()
	pending := len(observer.pending.events) + len(observer.pending.buckets)
	observer.mu.Unlock()
	if pending != 2 {
		t.Fatalf("pending records %d", pending)
	}
	if _, err := db.Exec("DROP TRIGGER fail_bucket"); err != nil {
		t.Fatal(err)
	}
	observer.flush(false)
	page, err := observer.Logs(t.Context(), LogFilter{Query: "retry fixture"})
	if err != nil || len(page.Items) != 1 || page.Dropped != 0 {
		t.Fatalf("event retry: %+v %v", page, err)
	}
	report, err := observer.Traffic(t.Context(), LogFilter{})
	if err != nil || report.Upload != 12 {
		t.Fatalf("bucket retry: %+v %v", report, err)
	}
}

func TestObservabilityLibSQLCommitInterruption(t *testing.T) {
	address := os.Getenv("SBPM_TEST_OPEN_LIBSQL_URL")
	if address == "" {
		t.Skip("uncredentialed libSQL fixture not configured")
	}
	target, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"stay", "disable", "switch"} {
		t.Run(mode, func(t *testing.T) {
			observer := newTestObserver(t)
			proxy := httputil.NewSingleHostReverseProxy(target)
			committed, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			var commits atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("old credential was forwarded")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					return
				}
				r.Body.Close()
				r.Body = io.NopCloser(bytes.NewReader(raw))
				if !bytes.Contains(raw, []byte(`"COMMIT"`)) {
					proxy.ServeHTTP(w, r)
					return
				}
				commits.Add(1)
				reply := httptest.NewRecorder()
				proxy.ServeHTTP(reply, r)
				if reply.Code != http.StatusOK {
					t.Error("upstream commit failed")
				}
				once.Do(func() { close(committed) })
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)
			cfg := LogSettingsInput{LogSettings: LogSettings{RemoteDriver: "turso", StoreMode: "independent", URL: "https://previous.invalid", Token: t.Name()}, DSNAction: "replace", TokenAction: "replace"}
			if err := observer.Save(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			cfg.Enabled, cfg.URL, cfg.TokenAction = true, server.URL, "clear"
			if err := observer.Save(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			unique := fmt.Sprintf("commit-%s-%d", mode, time.Now().UnixNano())
			observer.collect(time.Now(), []routeDelta{{route: ConnectionRoute{OutboundName: unique}, up: 17}})
			finished := make(chan struct{})
			go func() { observer.flush(false); close(finished) }()
			select {
			case <-committed:
			case <-time.After(5 * time.Second):
				t.Fatal("commit did not reach the database")
			}
			if mode != "stay" {
				cfg = LogSettingsInput{LogSettings: LogSettings{Enabled: mode == "switch", StoreMode: "local_sqlite"}}
				started := time.Now()
				if err := observer.Save(t.Context(), cfg); err != nil {
					t.Fatal(err)
				}
				if time.Since(started) > 3*time.Second || observer.Settings()["enabled"] != cfg.Enabled {
					t.Fatal("blocked old store delayed activation")
				}
			}
			select {
			case <-finished:
			case <-time.After(6 * time.Second):
				t.Fatal("driver failed to cancel unacknowledged commit")
			}
			diagnostic := observer.Settings()["status"].(map[string]any)
			if diagnostic["dropped_events"] != uint64(1) || !strings.Contains(diagnostic["last_error"].(string), "unknown") {
				t.Fatal("missing commit outcome diagnostic")
			}
			observer.flush(false)
			db, err := appdb.OpenLogTarget(t.Context(), appdb.DialectTurso, address, "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			defer db.Exec("DELETE FROM traffic_buckets WHERE outbound_node_name=?", unique)
			var total int
			if err = db.QueryRow("SELECT SUM(upload_bytes) FROM traffic_buckets WHERE outbound_node_name=?", unique).Scan(&total); err != nil || total != 17 || commits.Load() != 1 {
				t.Fatalf("uncertain commit replay: %d bytes, %d commits, %v", total, commits.Load(), err)
			}
		})
	}
}

func TestObservabilityUnavailablePreviousStore(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		o := newTestObserver(t)
		cfg := LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "local_sqlite"}}
		if err := o.Save(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		if err := o.store.db.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(o.Writer("panel"), "queued for unavailable target")
		cfg.Enabled, cfg.Path = enabled, "replacement.db"
		if err := o.Save(t.Context(), cfg); err != nil {
			t.Fatal(err)
		}
		o.flush(false)
		page, err := o.Logs(t.Context(), LogFilter{Level: "WARN"})
		if err != nil || page.Persisted != enabled || page.Dropped != 1 || len(page.Items) != 1 {
			t.Fatal("store recovery not reported")
		}
	}
}

func TestObservabilityInternalHopAndRetentionErrors(t *testing.T) {
	o := newTestObserver(t)
	singbox := o.Writer("singbox")
	entry := "INFO inbound/socks[" + dispatchTag + "]"
	for _, mode := range []string{"memory", "primary"} {
		if mode == "primary" {
			cfg := LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "primary", Days: 1}}
			if err := o.Save(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
		}
		fmt.Fprintln(singbox, entry)
		fmt.Fprintln(o.Writer("panel"), entry)
		o.flush(false)
		page, err := o.Logs(t.Context(), LogFilter{Query: dispatchTag})
		if err != nil || len(page.Items) != 1 || page.Items[0].Source != "panel" {
			t.Fatal("internal sing-box hop reached log panel")
		}
	}
	if _, err := o.primary.Exec(`CREATE TRIGGER block_cleanup BEFORE DELETE ON log_events BEGIN SELECT RAISE(ABORT,'denied'); END`); err != nil {
		t.Fatal(err)
	}
	if err := o.store.verify(t.Context()); err == nil {
		t.Fatal("missing delete permission was not detected")
	}
	o.record(LogEvent{TS: time.Now().Add(-72 * time.Hour), Source: "panel", Level: "WARN", Message: "expired"})
	o.flush(true)
	o.flush(false)
	status := o.Settings()["status"].(map[string]any)
	if status["last_error"] != "log retention cleanup failed" {
		t.Fatal("write cleared cleanup error")
	}
	if _, err := o.primary.Exec(`DROP TRIGGER block_cleanup`); err != nil {
		t.Fatal(err)
	}
	o.flush(true)
	if o.Settings()["status"].(map[string]any)["last_error"] != "" {
		t.Fatal("cleanup error not cleared after recovery")
	}
}

func TestObservabilitySwitchPrimaryAndIndependentSQLite(t *testing.T) {
	observer := newTestObserver(t)
	settings := LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "primary", RemoteDriver: "sqlite", MinLevel: "INFO"}}
	if err := observer.Save(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(observer.Writer("panel"), "queued for primary")
	settings.StoreMode = "independent"
	settings.DSNAction = "replace"
	settings.DSN = filepath.Join(observer.service.configDir, "independent.db")
	if err := observer.Save(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := observer.primary.QueryRow("SELECT COUNT(*) FROM log_events WHERE message='queued for primary'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("previous target flush %d, %v", count, err)
	}
	fmt.Fprintln(observer.Writer("panel"), "queued for independent")
	settings.StoreMode = "primary"
	settings.DSNAction = "keep"
	if err := observer.Save(t.Context(), settings); err != nil {
		t.Fatal(err)
	}
	stored, err := sql.Open("sqlite", settings.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer stored.Close()
	if err = stored.QueryRow("SELECT COUNT(*) FROM log_events WHERE message='queued for independent'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("dedicated target flush %d, %v", count, err)
	}
	if err = observer.primary.QueryRow("SELECT COUNT(*) FROM node_groups").Scan(&count); err != nil || count != 1 {
		t.Fatalf("primary business data %d, %v", count, err)
	}
}

func TestPrimarySQLiteRetentionReclaimsOnlyLogSpace(t *testing.T) {
	for _, mode := range []string{"NONE", "INCREMENTAL"} {
		t.Run(mode, func(t *testing.T) {
			o := newTestObserver(t)
			for _, pragma := range []string{"PRAGMA auto_vacuum=" + mode, "VACUUM", "PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"} {
				if _, err := o.primary.Exec(pragma); err != nil {
					t.Fatal(err)
				}
			}
			cfg := LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "primary", Days: 1}}
			if err := o.Save(t.Context(), cfg); err != nil {
				t.Fatal(err)
			}
			events := make([]LogEvent, 256)
			for i := range events {
				events[i] = LogEvent{TS: time.Now().Add(-48 * time.Hour), Source: "panel", Level: "INFO", Message: strings.Repeat("x", 4000)}
			}
			if err := o.store.write(t.Context(), events, nil); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(o.service.configDir, "proxy.db-wal")
			before, err := os.Stat(path)
			if err != nil || before.Size() == 0 {
				t.Fatal("expected pending SQLite WAL")
			}
			o.flush(true)
			after, err := os.Stat(path)
			if err != nil || after.Size() != 0 {
				t.Fatal("primary WAL not reclaimed")
			}
			var remaining, groups, vacuum, free int
			if err := o.primary.QueryRow("SELECT (SELECT COUNT(*) FROM log_events),(SELECT COUNT(*) FROM node_groups)").Scan(&remaining, &groups); err != nil || remaining != 0 || groups != 1 {
				t.Fatal("retention damaged main database")
			}
			if err := o.primary.QueryRow("PRAGMA auto_vacuum").Scan(&vacuum); err != nil {
				t.Fatal(err)
			}
			if err := o.primary.QueryRow("PRAGMA freelist_count").Scan(&free); err != nil {
				t.Fatal(err)
			}
			if mode == "INCREMENTAL" && (vacuum != 2 || free != 0) || mode == "NONE" && vacuum != 0 {
				t.Fatal("unexpected primary SQLite reclamation mode or free pages")
			}
		})
	}
}

func TestTrafficReadsCommittedAndPendingExactlyOnce(t *testing.T) {
	o := newTestObserver(t)
	if err := o.Save(t.Context(), LogSettingsInput{LogSettings: LogSettings{Enabled: true, StoreMode: "local_sqlite"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Minute)
	for bucket := range 3 {
		for route := range 2 {
			o.collect(now.Add(time.Duration(bucket)*time.Minute), []routeDelta{{route: ConnectionRoute{ClientIP: "192.0.2.4", OutboundID: route + 1}, up: 3, down: 7}})
		}
		if bucket == 1 {
			o.flush(false)
		}
	}
	done := make(chan struct{})
	go func() { defer close(done); o.flush(false) }()
	for range 5 {
		report, err := o.Traffic(t.Context(), LogFilter{IP: "192.0.2.4"})
		if err != nil || report.Total != 60 || len(report.Items) != 2 || len(report.Buckets) != 3 || report.Items[0].Total != 30 || report.Buckets[0].Total != 20 {
			t.Fatalf("inconsistent grouped traffic: %+v %v", report, err)
		}
	}
	<-done
}

func TestObservabilityRemoteDatabase(t *testing.T) {
	dsn := os.Getenv("SBPM_INTEGRATION_DATABASE_URL")
	turso := os.Getenv("SBPM_INTEGRATION_TURSO_URL")
	if dsn == "" && turso == "" {
		t.Skip("external database not configured")
	}
	o := newTestObserver(t)
	cfg := LogSettings{Enabled: true, StoreMode: "independent", RemoteDriver: "postgres", DSN: dsn, MinLevel: "INFO", Days: 7, MaxMB: 4}
	var expectedMySQL string
	if strings.HasPrefix(dsn, "mysql://") {
		target, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid MySQL test URL")
		}
		expectedMySQL = strings.TrimPrefix(target.Path, "/")
	} else if strings.Contains(dsn, "@tcp(") || strings.Contains(dsn, "@unix(") {
		target, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatal("invalid MySQL test DSN")
		}
		expectedMySQL = target.DBName
	}
	if expectedMySQL != "" {
		t.Setenv("MYSQL_DATABASE_NAME", "must_not_override_independent_logs")
		cfg.RemoteDriver = "mysql"
	}
	if turso != "" {
		cfg.RemoteDriver = "turso"
		cfg.URL = turso
		cfg.Token = os.Getenv("SBPM_INTEGRATION_TURSO_TOKEN")
	}
	input := LogSettingsInput{LogSettings: cfg, DSNAction: "replace", TokenAction: "keep"}
	if cfg.Token != "" {
		input.TokenAction = "replace"
	}
	if err := o.Save(t.Context(), input); err != nil {
		t.Fatal(err)
	}
	store := o.store
	unique := fmt.Sprintf("fixture-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := store.db.Exec("DELETE FROM log_events WHERE message=?", unique); err != nil {
			t.Error(err)
		}
		if _, err := store.db.Exec("DELETE FROM traffic_buckets WHERE outbound_node_name=?", unique); err != nil {
			t.Error(err)
		}
	})
	if store.dialect == appdb.DialectMySQL {
		var selected string
		if err := store.db.QueryRow("SELECT DATABASE()").Scan(&selected); err != nil || selected != expectedMySQL {
			t.Fatal("unexpected independent MySQL database")
		}
	}
	fmt.Fprintln(o.Writer("panel"), unique)
	now := time.Now()
	delta := routeDelta{route: ConnectionRoute{ClientIP: "192.0.2.10", OutboundName: unique}, up: 10, down: 5}
	o.collect(now, []routeDelta{delta})
	o.flush(false)
	o.collect(now, []routeDelta{delta})
	o.flush(true)
	page, err := o.Logs(context.Background(), LogFilter{Query: unique})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("remote page %+v, %v", page, err)
	}
	report, err := o.Traffic(t.Context(), LogFilter{Query: unique})
	if err != nil || report.Total != 30 {
		t.Fatalf("remote upsert %+v, %v", report, err)
	}
}
