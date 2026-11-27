package services

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	appdb "sb-proxy/backend/database"
	"sb-proxy/backend/models"
)

const maxQueuedLogs = 4096

type logBatch struct {
	events  []LogEvent
	buckets map[trafficKey]totals
}

type Observability struct {
	primary      *sql.DB
	service      *SingBoxService
	Connections  *ConnectionMonitor
	saveMu       sync.Mutex
	retired      sync.WaitGroup
	shutdownDone chan struct{}
	shutdownErr  error
	store        *logStore
	mu           sync.Mutex
	cfg          LogSettings
	ring         [1000]LogEvent
	sequence     int64
	dropped      uint64
	sizeBytes    int64
	lastError    string
	rotateError  string
	enabled      bool
	pending      logBatch
	wake         chan struct{}
	cancel       context.CancelFunc
}

func NewObservability(primary *sql.DB, service *SingBoxService) *Observability {
	observer := &Observability{primary: primary, service: service, Connections: NewConnectionMonitor(service), cfg: LogSettings{MinLevel: "INFO", StoreMode: "local_sqlite", RemoteDriver: "postgres", Days: 7, MaxMB: 200}, wake: make(chan struct{}, 1)}
	service.features.mu.Lock()
	service.features.observer = observer
	service.features.beforeStop = observer.finalSample
	service.features.mu.Unlock()
	return observer
}

type LogSettingsInput struct {
	LogSettings
	DSNAction   string `json:"dsn_action"`
	TokenAction string `json:"token_action"`
}

func (cfg *LogSettings) validate() error {
	cfg.MinLevel = strings.ToUpper(cfg.MinLevel)
	if levelRank(cfg.MinLevel) == 0 {
		return errors.New("min_level must be INFO, WARN or ERROR")
	}
	switch cfg.StoreMode {
	case "primary", "local_sqlite", "independent":
	default:
		return errors.New("invalid log storage mode")
	}
	switch appdb.Dialect(cfg.RemoteDriver) {
	case appdb.DialectPostgres, appdb.DialectSQLite, appdb.DialectTurso, appdb.DialectMySQL:
	default:
		return errors.New("invalid log database driver")
	}
	if cfg.Days < 0 || cfg.Days > 36500 || cfg.MaxMB < 0 || cfg.MaxMB > 1048576 {
		return errors.New("log retention is out of range")
	}
	if cfg.Enabled && cfg.StoreMode == "independent" {
		if cfg.RemoteDriver == "turso" && cfg.URL == "" || cfg.RemoteDriver != "turso" && cfg.DSN == "" {
			return errors.New("independent log storage requires a database address")
		}
	}
	return nil
}

func secretSetting(action, previous, next string) (string, error) {
	switch action {
	case "", "keep":
		return previous, nil
	case "replace":
		if strings.TrimSpace(next) == "" {
			return "", errors.New("replacement credential is empty")
		}
		return next, nil
	case "clear":
		return "", nil
	default:
		return "", errors.New("invalid credential update action")
	}
}

func (o *Observability) Restore(ctx context.Context) error {
	var raw string
	if err := o.primary.QueryRowContext(ctx, "SELECT COALESCE(log_settings_json,'{}') FROM settings WHERE singleton_key=1").Scan(&raw); err != nil {
		return errors.New("cannot load log settings")
	}
	o.saveMu.Lock()
	defer o.saveMu.Unlock()
	cfg := o.cfg
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return errors.New("invalid saved log settings")
		}
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	err := o.activate(ctx, cfg, false)
	if err != nil {
		o.mu.Lock()
		o.cfg = cfg
		o.lastError = err.Error()
		o.mu.Unlock()
	}
	return err
}

func (o *Observability) Save(ctx context.Context, input LogSettingsInput) error {
	o.saveMu.Lock()
	defer o.saveMu.Unlock()
	o.mu.Lock()
	previous := o.cfg
	o.mu.Unlock()
	cfg := input.LogSettings
	if cfg.MinLevel == "" {
		cfg.MinLevel = previous.MinLevel
	}
	if cfg.StoreMode == "" {
		cfg.StoreMode = previous.StoreMode
	}
	if cfg.RemoteDriver == "" {
		cfg.RemoteDriver = previous.RemoteDriver
	}
	var err error
	if cfg.RemoteDriver == "turso" {
		cfg.DSN = previous.DSN
		cfg.URL, err = secretSetting(input.DSNAction, previous.URL, cfg.URL)
	} else {
		cfg.URL = previous.URL
		cfg.DSN, err = secretSetting(input.DSNAction, previous.DSN, cfg.DSN)
	}
	if err != nil {
		return err
	}
	if cfg.RemoteDriver == "turso" && cfg.URL != previous.URL && previous.Token != "" && input.TokenAction != "replace" && input.TokenAction != "clear" {
		return errors.New("changing the Turso address requires replacing or clearing its token")
	}
	cfg.Token, err = secretSetting(input.TokenAction, previous.Token, cfg.Token)
	if err != nil {
		return err
	}
	if err = cfg.validate(); err != nil {
		return err
	}
	return o.activate(ctx, cfg, true)
}

func (o *Observability) activate(ctx context.Context, cfg LogSettings, persist bool) error {
	o.mu.Lock()
	closing := o.shutdownDone != nil
	o.mu.Unlock()
	if closing {
		return errors.New("log storage is shutting down")
	}
	var next *logStore
	var err error
	if cfg.Enabled {
		checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		next, err = openLogStore(checkCtx, o.primary, o.service.configDir, cfg)
		cancel()
		if err != nil {
			return err
		}
	}
	o.mu.Lock()
	wasActive := o.enabled
	o.mu.Unlock()
	if wasActive {
		_, _ = o.Connections.Capture(ctx)
	}
	if persist {
		saveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, marshalErr := json.Marshal(cfg)
		if marshalErr != nil {
			err = marshalErr
		} else if _, writeErr := o.primary.ExecContext(saveCtx, "UPDATE settings SET log_settings_json=? WHERE singleton_key=1", string(raw)); writeErr != nil {
			err = errors.New("cannot save log configuration")
		}
		cancel()
	}
	if err != nil {
		next.close()
		return err
	}
	o.stopWorker()
	o.mu.Lock()
	previous := o.store
	old := o.pending
	o.pending = logBatch{}
	o.store = next
	o.cfg = cfg
	o.enabled = cfg.Enabled
	o.sizeBytes = 0
	o.lastError = ""
	o.rotateError = ""
	o.mu.Unlock()
	if next != nil {
		o.startWorker(next)
	}
	if cfg.Enabled && !wasActive {
		o.Connections.SetSink(o.collect)
	} else if wasActive && !cfg.Enabled {
		o.Connections.SetSink(nil)
	}
	if previous != nil {
		done := o.retireStore(previous, old)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return nil
}

func (o *Observability) retireStore(store *logStore, batch logBatch) <-chan error {
	done := make(chan error, 1)
	o.retired.Go(func() {
		store.users.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var result error
		for _, pending := range []logBatch{store.unwritten, batch} {
			if err := store.write(ctx, pending.events, pending.buckets); err != nil {
				o.mu.Lock()
				o.dropBatchLocked(store, pending, err)
				o.mu.Unlock()
				result = err
			}
		}
		store.close()
		done <- result
	})
	return done
}

func (o *Observability) startWorker(store *logStore) {
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	store.users.Go(func() {
		write := time.NewTicker(2 * time.Second)
		defer write.Stop()
		rotate := time.NewTicker(time.Minute)
		defer rotate.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-o.wake:
				o.flushStore(ctx, store, false)
			case <-write.C:
				o.flushStore(ctx, store, false)
			case <-rotate.C:
				o.flushStore(ctx, store, true)
			}
		}
	})
}
func (o *Observability) stopWorker() {
	if o.cancel != nil {
		o.cancel()
		o.cancel = nil
	}
}

func (o *Observability) finalSample() {
	o.mu.Lock()
	enabled := o.enabled
	o.mu.Unlock()
	if enabled {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = o.Connections.Capture(ctx)
		cancel()
		o.flush(false)
	}
}
func (o *Observability) Close() error {
	o.mu.Lock()
	if o.shutdownDone == nil {
		o.shutdownDone = make(chan struct{})
		go func() {
			o.shutdownErr = o.shutdown()
			close(o.shutdownDone)
		}()
	}
	done := o.shutdownDone
	o.mu.Unlock()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return o.shutdownErr
	case <-timer.C:
		return fmt.Errorf("log storage shutdown: %w", context.DeadlineExceeded)
	}
}

func (o *Observability) shutdown() error {
	o.saveMu.Lock()
	defer o.saveMu.Unlock()
	o.mu.Lock()
	active := o.enabled
	o.mu.Unlock()
	if active {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = o.Connections.Capture(ctx)
		cancel()
	}
	o.Connections.SetSink(nil)
	o.stopWorker()
	o.service.features.mu.Lock()
	o.service.features.beforeStop = nil
	o.service.features.observer = nil
	o.service.features.mu.Unlock()
	o.mu.Lock()
	o.enabled = false
	store := o.store
	o.store = nil
	batch := o.pending
	o.pending = logBatch{}
	o.mu.Unlock()
	var err error
	if store != nil {
		err = <-o.retireStore(store, batch)
	}
	o.retired.Wait()
	if err != nil {
		return errors.New("final log database flush failed")
	}
	return nil
}

func (o *Observability) notify() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}
func levelRank(level string) int {
	switch level {
	case "ERROR":
		return 3
	case "WARN":
		return 2
	case "INFO":
		return 1
	}
	return 0
}
func (o *Observability) appendRingLocked(event LogEvent) {
	o.sequence++
	event.ID = o.sequence
	o.ring[(event.ID-1)%int64(len(o.ring))] = event
}

func (o *Observability) addEventLocked(event LogEvent) {
	o.appendRingLocked(event)
	if o.enabled && levelRank(event.Level) >= levelRank(o.cfg.MinLevel) {
		if len(o.pending.events) == maxQueuedLogs {
			o.dropped++
			return
		}
		o.pending.events = append(o.pending.events, event)
		if len(o.pending.events) == 100 {
			o.notify()
		}
	}
}
func (o *Observability) addBucketLocked(key trafficKey, up, down int64) {
	if o.pending.buckets == nil {
		o.pending.buckets = map[trafficKey]totals{}
	}
	value, exists := o.pending.buckets[key]
	if !exists && len(o.pending.buckets) >= maxQueuedLogs {
		o.dropped++
		return
	}
	value.add(up, down)
	o.pending.buckets[key] = value
}
func (o *Observability) mergeBatchLocked(old logBatch) {
	tail := o.pending
	o.pending = old
	remaining := maxQueuedLogs - len(o.pending.events)
	count := min(remaining, len(tail.events))
	o.pending.events = append(o.pending.events, tail.events[:count]...)
	o.dropped += uint64(len(tail.events) - count)
	for key, value := range tail.buckets {
		o.addBucketLocked(key, value.Upload, value.Download)
	}
}

func (o *Observability) collect(at time.Time, deltas []routeDelta) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.enabled {
		return
	}
	for _, d := range deltas {
		r := d.route
		if d.opened {
			event := LogEvent{TS: at, Source: "panel", Level: "INFO", Message: "Proxy connection opened", IP: r.ClientIP, Port: r.InboundPort, Outbound: r.OutboundName, Member: r.MemberName}
			if models.IsPolicy(r.OutboundType) {
				event.Policy = r.OutboundName
			}
			o.addEventLocked(event)
		}
		if d.up != 0 || d.down != 0 {
			o.addBucketLocked(trafficKey{trafficPath{r.ClientIP, r.InboundPort, r.InboundName, r.RouteID, r.OutboundID, r.OutboundName, r.MemberName}, at.Truncate(time.Minute).Unix()}, d.up, d.down)
		}
	}
}

func (o *Observability) flush(rotate bool) {
	o.mu.Lock()
	store := o.store
	if store != nil {
		store.users.Add(1)
	}
	o.mu.Unlock()
	if store == nil {
		return
	}
	defer store.users.Done()
	o.flushStore(context.Background(), store, rotate)
}

func (o *Observability) dropBatchLocked(store *logStore, batch logBatch, failure error) {
	message := "Previous log database unavailable, buffered records were dropped"
	if errors.Is(failure, errCommitUncertain) {
		message = "Log database commit outcome unknown, batch will not be replayed"
	}
	o.dropped += uint64(len(batch.events) + len(batch.buckets))
	o.lastError = message
	event := LogEvent{TS: time.Now().UTC(), Source: "panel", Level: "WARN", Message: message}
	if o.store == store {
		o.appendRingLocked(event)
	} else {
		o.addEventLocked(event)
	}
}

func (o *Observability) flushStore(parent context.Context, store *logStore, rotate bool) {
	store.batchMu.Lock()
	defer store.batchMu.Unlock()
	o.mu.Lock()
	if o.store != store {
		o.mu.Unlock()
		return
	}
	batch := o.pending
	o.pending = logBatch{}
	cfg := o.cfg
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	if err := store.write(ctx, batch.events, batch.buckets); err != nil {
		o.mu.Lock()
		if errors.Is(err, errCommitUncertain) {
			o.dropBatchLocked(store, batch, err)
		} else if o.store != store {
			store.unwritten = batch
		} else {
			o.mergeBatchLocked(batch)
			o.lastError = "log database write failed"
		}
		o.mu.Unlock()
		return
	}
	if rotate {
		if err := store.rotate(ctx, cfg); err != nil {
			o.mu.Lock()
			if o.store == store {
				o.rotateError = "log retention cleanup failed"
			}
			o.mu.Unlock()
			return
		}
	}
	if rotate || store.path != "" {
		size, err := store.size(ctx)
		if err == nil {
			o.mu.Lock()
			if o.store == store {
				o.sizeBytes = size
			}
			o.mu.Unlock()
		}
	}
	o.mu.Lock()
	if o.store == store && len(batch.events)+len(batch.buckets) > 0 {
		o.lastError = ""
	}
	if o.store == store && rotate {
		o.rotateError = ""
	}
	o.mu.Unlock()
}

func (o *Observability) Settings() map[string]any {
	o.mu.Lock()
	defer o.mu.Unlock()
	cfg := o.cfg
	mask := func(value string) string {
		if value != "" {
			return "••••"
		}
		return ""
	}
	driver := cfg.RemoteDriver
	switch cfg.StoreMode {
	case "primary":
		driver = string(appdb.DialectFor(o.primary))
	case "local_sqlite":
		driver = "sqlite"
	}
	lastError := o.lastError
	if lastError == "" {
		lastError = o.rotateError
	}
	return map[string]any{"enabled": cfg.Enabled, "min_level": cfg.MinLevel, "store_mode": cfg.StoreMode, "remote_driver": cfg.RemoteDriver, "local_sqlite_path": cfg.Path, "remote_url_masked": mask(cfg.URL), "remote_dsn_masked": mask(cfg.DSN), "has_remote_dsn": cfg.DSN != "" || cfg.URL != "", "has_remote_token": cfg.Token != "", "retention_days": cfg.Days, "max_size_mb": cfg.MaxMB, "status": map[string]any{"active": o.enabled, "driver": driver, "size_bytes": o.sizeBytes, "dropped_events": o.dropped, "last_error": lastError}}
}

var logANSI = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
var logLevel = regexp.MustCompile(`(?i)\b(error|fatal|panic|warn(?:ing)?|failed)\b`)
var logNode = regexp.MustCompile(`(inbound|outbound)/[^ ]+\[node-(\d+)-(?:in|out)\]`)
var logPeer = regexp.MustCompile(`\bfrom (\[[a-fA-F0-9:]+\]|[\d.]+):\d+`)

func (o *Observability) Writer(source string) io.Writer {
	return &logLineWriter{emit: func(line string) error {
		o.service.features.mu.RLock()
		wiring := o.service.features.wiring
		o.service.features.mu.RUnlock()
		o.line(source, strings.TrimSpace(logANSI.ReplaceAllString(line, "")), wiring)
		return nil
	}}
}
func (o *Observability) line(source, message string, wiring *runtimeWiring) string {
	clean := cleanLogLine(message, wiring)
	if o == nil || message == "" || source == "singbox" && strings.Contains(message, "["+dispatchTag+"]") {
		return clean
	}
	e := LogEvent{TS: time.Now().UTC(), Level: "INFO", Source: source}
	if match := logLevel.FindString(message); match != "" {
		if strings.HasPrefix(strings.ToLower(match), "warn") {
			e.Level = "WARN"
		} else {
			e.Level = "ERROR"
		}
	}
	if match := logPeer.FindStringSubmatch(message); len(match) > 0 {
		if ip := net.ParseIP(strings.Trim(match[1], "[]")); ip != nil {
			e.IP = ip.String()
		}
	}
	if wiring != nil {
		for _, match := range logNode.FindAllStringSubmatch(message, -1) {
			id, _ := strconv.Atoi(match[2])
			node := wiring.nodes[id]
			if match[1] == "inbound" {
				e.Port = node.InboundPort
			} else {
				e.Outbound = node.Name
			}
		}
	}
	e.Message = boundIPCheckDiagnostic(clean)
	o.record(e)
	return clean
}

func (o *Observability) record(event LogEvent) {
	o.mu.Lock()
	o.addEventLocked(event)
	o.mu.Unlock()
}
