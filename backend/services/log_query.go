package services

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

type LogPage struct {
	Items     []LogEvent `json:"items"`
	Next      int64      `json:"next_cursor"`
	Mode      string     `json:"storage_mode"`
	Persisted bool       `json:"persisted"`
	Dropped   uint64     `json:"dropped_events"`
}

func containsLogQuery(query string, values ...string) bool {
	if query == "" {
		return true
	}
	query = strings.ToLower(query)
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

func (filter LogFilter) matches(event LogEvent) bool {
	return (filter.Cursor == 0 || event.ID < filter.Cursor) && (filter.Source == "" || event.Source == filter.Source) && (filter.Level == "" || event.Level == filter.Level) && (filter.IP == "" || event.IP == filter.IP) && (filter.From.IsZero() || !event.TS.Before(filter.From)) && (filter.To.IsZero() || !event.TS.After(filter.To)) && containsLogQuery(filter.Query, event.Message, event.IP, event.Outbound, event.Policy, event.Member)
}

func (o *Observability) Logs(ctx context.Context, filter LogFilter) (LogPage, error) {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	filter.Limit = min(filter.Limit, 200)
	o.mu.Lock()
	store := o.store
	page := LogPage{Mode: "memory", Items: []LogEvent{}, Dropped: o.dropped, Persisted: o.store != nil}
	if o.store == nil {
		for id := o.sequence; id > max(0, o.sequence-int64(len(o.ring))) && len(page.Items) <= filter.Limit; id-- {
			event := o.ring[(id-1)%int64(len(o.ring))]
			if filter.matches(event) {
				page.Items = append(page.Items, event)
			}
		}
		o.mu.Unlock()
	} else {
		store.users.Add(1)
		defer store.users.Done()
		page.Mode = o.cfg.StoreMode
		o.mu.Unlock()
		where, args := filter.where(false)
		args = append(args, filter.Limit+1)
		rows, err := store.db.QueryContext(ctx, "SELECT id,"+eventCols+" FROM log_events"+where+" ORDER BY id DESC LIMIT ?", args...)
		if err != nil {
			return page, errors.New("log database query failed")
		}
		defer rows.Close()
		for rows.Next() {
			var event LogEvent
			var stamp int64
			if err = rows.Scan(&event.ID, &stamp, &event.Source, &event.Level, &event.Message, &event.IP, &event.Port, &event.Outbound, &event.Policy, &event.Member); err != nil {
				return page, errors.New("cannot read stored log event")
			}
			event.TS = time.UnixMilli(stamp).UTC()
			page.Items = append(page.Items, event)
		}
		if rows.Err() != nil {
			return page, errors.New("log database query interrupted")
		}
	}
	if len(page.Items) > filter.Limit {
		page.Items = page.Items[:filter.Limit]
		page.Next = page.Items[len(page.Items)-1].ID
	}
	return page, nil
}

type trafficRow struct {
	trafficPath
	totals
}
type trafficMinute struct {
	TS time.Time `json:"bucket_ts"`
	totals
}
type TrafficReport struct {
	Persisted bool `json:"persisted"`
	totals
	Items   []trafficRow    `json:"items"`
	Buckets []trafficMinute `json:"buckets"`
}

func (f LogFilter) matchesTraffic(key trafficKey) bool {
	return (f.From.IsZero() || key.Minute >= f.From.Truncate(time.Minute).Unix()) && (f.To.IsZero() || key.Minute <= f.To.Truncate(time.Minute).Unix()) && (f.IP == "" || f.IP == key.IP) && (f.Port == 0 || f.Port == key.Port) && (f.Outbound == 0 || f.Outbound == key.OutboundID) && containsLogQuery(f.Query, key.IP, key.Inbound, key.Outbound, key.Member, key.Route)
}

func (o *Observability) Traffic(ctx context.Context, f LogFilter) (TrafficReport, error) {
	report := TrafficReport{Items: []trafficRow{}, Buckets: []trafficMinute{}}
	o.mu.Lock()
	enabled := o.enabled
	o.mu.Unlock()
	if enabled {
		_, _ = o.Connections.Capture(ctx)
	}
	o.mu.Lock()
	store := o.store
	if store != nil {
		store.users.Add(1)
	}
	o.mu.Unlock()
	if store == nil {
		return report, nil
	}
	defer store.users.Done()
	report.Persisted = true
	paths := map[trafficPath]totals{}
	minutes := map[int64]totals{}
	addPath := func(path trafficPath, up, down int64) {
		p := paths[path]
		p.add(up, down)
		paths[path] = p
		report.add(up, down)
	}
	addMinute := func(minute int64, up, down int64) {
		m := minutes[minute]
		m.add(up, down)
		minutes[minute] = m
	}
	where, args := f.where(true)
	const dimensions = "client_ip,inbound_port,inbound_node_name,route_id,outbound_node_id,outbound_node_name,policy_member_name"
	const sums = ",SUM(upload_bytes),SUM(download_bytes) FROM traffic_buckets"
	query := "SELECT 0,0," + dimensions + sums + where + " GROUP BY " + dimensions +
		" UNION ALL SELECT 1,bucket_ts,'',0,'','',0,'',''" + sums + where + " GROUP BY bucket_ts"
	store.batchMu.RLock()
	o.mu.Lock()
	if o.store != store {
		o.mu.Unlock()
		store.batchMu.RUnlock()
		return report, errors.New("traffic storage changed; retry the query")
	}
	for key, val := range o.pending.buckets {
		if f.matchesTraffic(key) {
			addPath(key.trafficPath, val.Upload, val.Download)
			addMinute(key.Minute, val.Upload, val.Download)
		}
	}
	o.mu.Unlock()
	rows, err := store.db.QueryContext(ctx, query, append(args, args...)...)
	if err != nil {
		store.batchMu.RUnlock()
		return report, errors.New("traffic database query failed")
	}
	defer rows.Close()
	hasRow := rows.Next()
	store.batchMu.RUnlock()
	for ; hasRow; hasRow = rows.Next() {
		var kind int
		var path trafficPath
		var minute, up, down int64
		if err = rows.Scan(&kind, &minute, &path.IP, &path.Port, &path.Inbound, &path.Route, &path.OutboundID, &path.Outbound, &path.Member, &up, &down); err != nil {
			return report, errors.New("cannot read traffic bucket")
		}
		if kind == 0 {
			addPath(path, up, down)
		} else {
			addMinute(minute, up, down)
		}
	}
	if rows.Err() != nil {
		return report, errors.New("traffic query interrupted")
	}
	for path, value := range paths {
		report.Items = append(report.Items, trafficRow{path, value})
	}
	for minute, value := range minutes {
		report.Buckets = append(report.Buckets, trafficMinute{time.Unix(minute, 0).UTC(), value})
	}
	sort.Slice(report.Items, func(i, j int) bool { return report.Items[i].Total > report.Items[j].Total })
	sort.Slice(report.Buckets, func(i, j int) bool { return report.Buckets[i].TS.Before(report.Buckets[j].TS) })
	return report, nil
}
