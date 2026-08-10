package database

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstanceLeaseRejectsSecondManagerAndAllowsCleanHandoff(t *testing.T) {
	db, err := openSQLite(t.TempDir())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	first, err := acquireInstanceLease(ctx, db, time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if _, err := acquireInstanceLease(ctx, db, time.Second, 100*time.Millisecond); !errors.Is(err, ErrManagerInstanceActive) {
		t.Fatalf("second manager was not rejected: %v", err)
	}
	if err := first.Release(ctx); err != nil {
		t.Fatalf("release first lease: %v", err)
	}

	second, err := acquireInstanceLease(ctx, db, time.Second, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire lease after clean handoff: %v", err)
	}
	if err := second.Release(ctx); err != nil {
		t.Fatalf("release second lease: %v", err)
	}
}

func TestInstanceLeaseMonitorReportsOwnershipLoss(t *testing.T) {
	db, err := openSQLite(t.TempDir())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	lease, err := acquireInstanceLease(context.Background(), db, 200*time.Millisecond, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	errorsCh := lease.Monitor(monitorCtx)
	if _, err := db.Exec(
		"UPDATE manager_instance_lock SET owner_id = ?, lease_expires_at = ? WHERE singleton_key = 1",
		"replacement-owner",
		time.Now().Add(time.Second).UnixMilli(),
	); err != nil {
		t.Fatalf("replace lease owner: %v", err)
	}

	select {
	case monitorErr := <-errorsCh:
		if !errors.Is(monitorErr, ErrManagerInstanceActive) {
			t.Fatalf("unexpected monitor error: %v", monitorErr)
		}
	case <-time.After(time.Second):
		t.Fatalf("lease monitor did not report ownership loss")
	}
	cancelMonitor()
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("release lost lease: %v", err)
	}
}

func TestInstanceLeaseMonitorTimesOutBlockedRenewalBeforeExpiry(t *testing.T) {
	db, err := openSQLite(t.TempDir())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const (
		duration  = 300 * time.Millisecond
		heartbeat = 100 * time.Millisecond
	)
	lease, err := acquireInstanceLease(context.Background(), db, duration, heartbeat)
	if err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	blockedConnection, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve the only database connection: %v", err)
	}

	startedAt := time.Now()
	errorsCh := lease.Monitor(context.Background())
	select {
	case monitorErr := <-errorsCh:
		if !errors.Is(monitorErr, context.DeadlineExceeded) {
			t.Fatalf("unexpected blocked renewal error: %v", monitorErr)
		}
		if elapsed := time.Since(startedAt); elapsed >= duration {
			t.Fatalf("lease monitor reported the blocked renewal after expiry: %v", elapsed)
		}
	case <-time.After(2 * duration):
		t.Fatalf("lease monitor remained blocked past the lease expiry")
	}

	if err := blockedConnection.Close(); err != nil {
		t.Fatalf("release blocked database connection: %v", err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("release lease: %v", err)
	}
}
