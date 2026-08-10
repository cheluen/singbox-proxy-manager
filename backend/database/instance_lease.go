package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

var ErrManagerInstanceActive = errors.New("another manager instance already holds the database lease")

type InstanceLease struct {
	db             *sql.DB
	ownerID        string
	duration       time.Duration
	heartbeat      time.Duration
	stop           chan struct{}
	done           chan struct{}
	monitorOnce    sync.Once
	stopOnce       sync.Once
	releaseOnce    sync.Once
	releaseErr     error
	stateMu        sync.Mutex
	monitorStarted bool
}

func AcquireInstanceLease(ctx context.Context, db *sql.DB) (*InstanceLease, error) {
	duration := readLeaseDurationEnv("SBPM_INSTANCE_LEASE_DURATION", 30*time.Second, 5*time.Second, 10*time.Minute)
	heartbeat := readLeaseDurationEnv("SBPM_INSTANCE_LEASE_HEARTBEAT", duration/3, time.Second, duration/2)
	return acquireInstanceLease(ctx, db, duration, heartbeat)
}

func acquireInstanceLease(
	ctx context.Context,
	db *sql.DB,
	duration time.Duration,
	heartbeat time.Duration,
) (*InstanceLease, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required for instance lease")
	}
	if duration <= 0 || heartbeat <= 0 || heartbeat >= duration {
		return nil, fmt.Errorf("invalid instance lease timing")
	}
	if err := ensureInstanceLeaseTable(ctx, db, DialectFor(db)); err != nil {
		return nil, err
	}

	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return nil, fmt.Errorf("generate instance lease owner: %w", err)
	}
	ownerID := hex.EncodeToString(ownerBytes)
	now := time.Now().UnixMilli()
	expiresAt := time.Now().Add(duration).UnixMilli()

	acquired, err := updateInstanceLease(ctx, db, ownerID, now, expiresAt)
	if err != nil {
		return nil, err
	}
	if !acquired {
		_, insertErr := db.ExecContext(ctx, `
			INSERT INTO manager_instance_lock (singleton_key, owner_id, lease_expires_at, updated_at)
			VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		`, 1, ownerID, expiresAt)
		if insertErr != nil {
			acquired, err = updateInstanceLease(ctx, db, ownerID, now, expiresAt)
			if err != nil {
				return nil, err
			}
			if !acquired {
				return nil, ErrManagerInstanceActive
			}
		}
	}

	return &InstanceLease{
		db:        db,
		ownerID:   ownerID,
		duration:  duration,
		heartbeat: heartbeat,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}, nil
}

func updateInstanceLease(
	ctx context.Context,
	db *sql.DB,
	ownerID string,
	now int64,
	expiresAt int64,
) (bool, error) {
	result, err := db.ExecContext(ctx, `
		UPDATE manager_instance_lock
		SET owner_id = ?, lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP
		WHERE singleton_key = 1
		  AND (owner_id = ? OR lease_expires_at <= ?)
	`, ownerID, expiresAt, ownerID, now)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

func ensureInstanceLeaseTable(ctx context.Context, db *sql.DB, dialect Dialect) error {
	statement := `CREATE TABLE IF NOT EXISTS manager_instance_lock (
		singleton_key INTEGER PRIMARY KEY,
		owner_id TEXT NOT NULL,
		lease_expires_at BIGINT NOT NULL,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`
	switch dialect {
	case DialectPostgres:
		statement = `CREATE TABLE IF NOT EXISTS manager_instance_lock (
			singleton_key SMALLINT PRIMARY KEY,
			owner_id TEXT NOT NULL,
			lease_expires_at BIGINT NOT NULL,
			updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
		)`
	case DialectMySQL:
		statement = `CREATE TABLE IF NOT EXISTS manager_instance_lock (
			singleton_key SMALLINT PRIMARY KEY,
			owner_id VARCHAR(64) NOT NULL,
			lease_expires_at BIGINT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`
	}
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("create manager instance lease table: %w", err)
	}
	return nil
}

func (lease *InstanceLease) Monitor(ctx context.Context) <-chan error {
	errorsCh := make(chan error, 1)
	if lease == nil {
		errorsCh <- fmt.Errorf("instance lease is nil")
		close(errorsCh)
		return errorsCh
	}

	started := false
	lease.monitorOnce.Do(func() {
		started = true
		lease.stateMu.Lock()
		lease.monitorStarted = true
		lease.stateMu.Unlock()
		go lease.monitor(ctx, errorsCh)
	})
	if !started {
		errorsCh <- fmt.Errorf("instance lease monitor already started")
		close(errorsCh)
	}
	return errorsCh
}

func (lease *InstanceLease) monitor(ctx context.Context, errorsCh chan<- error) {
	defer close(lease.done)
	defer close(errorsCh)
	ticker := time.NewTicker(lease.heartbeat)
	defer ticker.Stop()
	lastRenewal := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-lease.stop:
			return
		case <-ticker.C:
			renewalStartedAt := time.Now()
			expiresAt := renewalStartedAt.Add(lease.duration).UnixMilli()
			renewalTimeout := lease.heartbeat
			if safetyTimeout := (lease.duration - lease.heartbeat) / 2; safetyTimeout < renewalTimeout {
				renewalTimeout = safetyTimeout
			}
			renewalCtx, cancelRenewal := context.WithTimeout(ctx, renewalTimeout)
			result, err := lease.db.ExecContext(renewalCtx, `
				UPDATE manager_instance_lock
				SET lease_expires_at = ?, updated_at = CURRENT_TIMESTAMP
				WHERE singleton_key = 1 AND owner_id = ?
			`, expiresAt, lease.ownerID)
			cancelRenewal()
			checkedAt := time.Now()
			if err == nil {
				var affected int64
				affected, err = result.RowsAffected()
				if err == nil && affected != 1 {
					errorsCh <- ErrManagerInstanceActive
					return
				}
			}
			if err == nil {
				lastRenewal = renewalStartedAt
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-lease.stop:
				return
			default:
			}
			if checkedAt.Sub(lastRenewal) >= lease.duration-lease.heartbeat {
				errorsCh <- fmt.Errorf("manager instance lease renewal failed: %w", err)
				return
			}
		}
	}
}

func (lease *InstanceLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.releaseOnce.Do(func() {
		lease.stopOnce.Do(func() { close(lease.stop) })
		lease.stateMu.Lock()
		monitorStarted := lease.monitorStarted
		lease.stateMu.Unlock()
		if monitorStarted {
			select {
			case <-lease.done:
			case <-ctx.Done():
				lease.releaseErr = ctx.Err()
				return
			}
		}
		_, lease.releaseErr = lease.db.ExecContext(
			ctx,
			"DELETE FROM manager_instance_lock WHERE singleton_key = 1 AND owner_id = ?",
			lease.ownerID,
		)
	})
	return lease.releaseErr
}

func readLeaseDurationEnv(
	key string,
	fallback time.Duration,
	minimum time.Duration,
	maximum time.Duration,
) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value < minimum || value > maximum {
		return fallback
	}
	return value
}
