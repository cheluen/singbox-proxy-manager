package api

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	appdb "sb-proxy/backend/database"
)

func TestNodeMutationLateFailureRestoresRuntimeWithoutOverwritingDatabase(t *testing.T) {
	for _, failure := range []string{"conflict", "write-failure", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			handler := newTestHandler(t, nil)
			handler.db.SetMaxOpenConns(1)
			nodeID := insertTestNode(t, handler.db)
			nodes, err := handler.loadAllNodes()
			if err != nil {
				t.Fatal(err)
			}
			if err := handler.singBoxService.GenerateGlobalConfig(nodes); err != nil {
				t.Fatal(err)
			}
			if err := handler.singBoxService.Start(); err != nil {
				t.Fatal(err)
			}
			originalHash := handler.singBoxService.RuntimeStatus().ActiveConfigHash
			directory := t.TempDir()
			marker := filepath.Join(directory, "checking")
			gate := filepath.Join(directory, "continue")
			binary := filepath.Join(directory, "gated-sing-box")
			script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = check ]; then\n touch '%s'\n while [ ! -f '%s' ]; do sleep 0.01; done\n exit 0\nfi\nexec sleep 300\n", marker, gate)
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SINGBOX_BINARY", binary)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := handler.nodeMutations.Execute(ctx, NodeMutationOperation{
					ApplyRuntime: true,
					Mutate: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, "UPDATE proxy_nodes SET config = ? WHERE id = ?", `{"server":"candidate.example.com","server_port":443,"method":"aes-128-gcm","password":"p"}`, nodeID)
						return err
					},
				})
				done <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("validation did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch failure {
			case "conflict":
				_, err = handler.db.Exec("UPDATE proxy_nodes SET remark = 'concurrent-remark' WHERE id = ?", nodeID)
			case "write-failure":
				_, err = handler.db.Exec(`CREATE TRIGGER reject_candidate BEFORE UPDATE ON proxy_nodes BEGIN SELECT RAISE(ABORT, 'injected write failure'); END`)
			case "cancel":
				cancel()
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(gate, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("injected failure did not reject the mutation")
			}
			node, err := loadNodeByIDFrom(context.Background(), handler.db, nodeID)
			if err != nil || bytes.Contains([]byte(node.Config), []byte("candidate.example.com")) {
				t.Fatal("failed candidate remained in the database")
			}
			if failure == "conflict" && node.Remark != "concurrent-remark" {
				t.Fatal("rollback overwrote a concurrent database change")
			}
			status := handler.singBoxService.RuntimeStatus()
			if !status.Running || status.Degraded || status.ActiveConfigHash != originalHash || status.DesiredConfigHash != originalHash {
				t.Fatalf("failed candidate did not restore the original runtime: %+v", status)
			}
		})
	}
}

func TestNodeMutationDoesNotHoldSQLiteDuringRuntimeWork(t *testing.T) {
	for _, phase := range []string{"check", "run"} {
		t.Run(phase, func(t *testing.T) {
			handler := newTestHandler(t, nil)
			handler.db.SetMaxOpenConns(1)
			nodeID := insertTestNode(t, handler.db)
			t.Setenv("SBPM_INSTANCE_LEASE_DURATION", "5s")
			t.Setenv("SBPM_INSTANCE_LEASE_HEARTBEAT", "1s")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lease, err := appdb.AcquireInstanceLease(ctx, handler.db)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lease.Release(context.Background()) }()
			leaseErrors := lease.Monitor(ctx)
			directory := t.TempDir()
			marker := filepath.Join(directory, "runtime-started")
			binary := filepath.Join(directory, "slow-sing-box")
			script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = \"%s\" ]; then touch '%s'; fi\nif [ \"$1\" = check ]; then %s exit 0; fi\nexec sleep 300\n", phase, marker, map[string]string{"check": "sleep 6;", "run": ""}[phase])
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SINGBOX_BINARY", binary)
			if phase == "run" {
				t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "6s")
			}
			done := make(chan error, 1)
			go func() {
				_, err := handler.nodeMutations.Execute(ctx, NodeMutationOperation{
					ApplyRuntime: true,
					Mutate: func(ctx context.Context, tx *sql.Tx) error {
						_, err := tx.ExecContext(ctx, "UPDATE proxy_nodes SET name = ? WHERE id = ?", "changed", nodeID)
						return err
					},
				})
				done <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("runtime work did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			readCtx, cancelRead := context.WithTimeout(ctx, 300*time.Millisecond)
			var name string
			err = handler.db.QueryRowContext(readCtx, "SELECT name FROM proxy_nodes WHERE id = ?", nodeID).Scan(&name)
			cancelRead()
			if err != nil {
				t.Errorf("runtime work blocked database reads: %v", err)
			} else if name != "node1" {
				t.Error("uncommitted candidate was visible during runtime work")
			}
			if err := <-done; err != nil {
				t.Fatalf("mutation failed: %v", err)
			}
			select {
			case err := <-leaseErrors:
				t.Errorf("runtime work caused lease loss: %v", err)
			default:
			}
			if err := handler.db.QueryRow("SELECT name FROM proxy_nodes WHERE id = ?", nodeID).Scan(&name); err != nil || name != "changed" {
				t.Fatalf("candidate was not committed: name=%q err=%v", name, err)
			}
		})
	}
}
