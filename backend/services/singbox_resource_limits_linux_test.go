//go:build linux

package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestValidateConfigTimesOutAndTerminatesProcessGroup(t *testing.T) {
	t.Setenv("SBPM_SINGBOX_CHECK_TIMEOUT", "100ms")
	t.Setenv("SBPM_SINGBOX_CHECK_CONCURRENCY", "1")
	tempDir := t.TempDir()
	pidPath := filepath.Join(tempDir, "check.pid")
	childPIDPath := filepath.Join(tempDir, "check-child.pid")
	binary := filepath.Join(tempDir, "fake-sing-box")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "check" ]; then
  echo $$ > %s
  sleep 300 &
  child=$!
  echo "$child" > %s
  wait "$child"
fi
`, strconv.Quote(pidPath), strconv.Quote(childPIDPath))
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", binary)

	service := NewSingBoxService(t.TempDir())
	startedAt := time.Now()
	err := service.ValidateConfig([]byte(`{"inbounds":[]}`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected validation deadline, got %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 2*time.Second {
		t.Fatalf("validation timeout took too long: %s", elapsed)
	}

	for _, process := range []struct {
		name string
		path string
	}{
		{name: "validation parent", path: pidPath},
		{name: "validation child", path: childPIDPath},
	} {
		pidData, readErr := os.ReadFile(process.path)
		if readErr != nil {
			t.Fatalf("read %s process id: %v", process.name, readErr)
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if parseErr != nil {
			t.Fatalf("parse %s process id: %v", process.name, parseErr)
		}
		deadline := time.Now().Add(time.Second)
		for {
			killErr := syscall.Kill(pid, 0)
			if errors.Is(killErr, syscall.ESRCH) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s process %d remains alive: %v", process.name, pid, killErr)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if matches, globErr := filepath.Glob(filepath.Join(service.configDir, "config.check-*.json")); globErr != nil || len(matches) != 0 {
		t.Fatalf("temporary validation configs remain: matches=%v err=%v", matches, globErr)
	}
}

func TestValidateConfigLimitsConcurrentProcesses(t *testing.T) {
	t.Setenv("SBPM_SINGBOX_CHECK_TIMEOUT", "5s")
	t.Setenv("SBPM_SINGBOX_CHECK_CONCURRENCY", "2")
	tempDir := t.TempDir()
	markerDir := filepath.Join(tempDir, "markers")
	if err := os.Mkdir(markerDir, 0o700); err != nil {
		t.Fatalf("create marker directory: %v", err)
	}
	releasePath := filepath.Join(tempDir, "release")
	binary := filepath.Join(tempDir, "fake-sing-box")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "check" ]; then
  marker=%s/$$
  touch "$marker"
  while [ ! -f %s ]; do sleep 0.02; done
  rm -f "$marker"
  exit 0
fi
sleep 300
`, strconv.Quote(markerDir), strconv.Quote(releasePath))
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", binary)
	service := NewSingBoxService(t.TempDir())

	const requests = 6
	errorsCh := make(chan error, requests)
	var workers sync.WaitGroup
	workers.Add(requests)
	for index := 0; index < requests; index++ {
		go func() {
			defer workers.Done()
			errorsCh <- service.ValidateConfig([]byte(`{"inbounds":[]}`))
		}()
	}

	deadline := time.Now().Add(2 * time.Second)
	maximum := 0
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(markerDir)
		if err != nil {
			t.Fatalf("read marker directory: %v", err)
		}
		if len(entries) > maximum {
			maximum = len(entries)
		}
		if len(entries) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if maximum != 2 {
		t.Fatalf("expected exactly two concurrent validation processes, observed %d", maximum)
	}
	observationDeadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(observationDeadline) {
		entries, err := os.ReadDir(markerDir)
		if err != nil {
			t.Fatalf("read marker directory: %v", err)
		}
		if len(entries) > 2 {
			t.Fatalf("validation process limit exceeded: %d", len(entries))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatalf("release validation processes: %v", err)
	}

	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("validation requests did not finish")
	}
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatalf("validation request failed: %v", err)
		}
	}
}
