//go:build linux

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"sb-proxy/backend/models"
)

func TestCheckUpstreamIPRetriesOnlyStartupFailuresWithFreshPorts(t *testing.T) {
	tempDir := t.TempDir()
	attemptRecord := filepath.Join(tempDir, "attempts")
	fakeBinary := filepath.Join(tempDir, "fake-sing-box")
	script := `#!/bin/sh
count=0
if [ -f "$UPSTREAM_TEST_ATTEMPT_RECORD" ]; then
  count=$(cat "$UPSTREAM_TEST_ATTEMPT_RECORD")
fi
count=$((count + 1))
printf '%s' "$count" > "$UPSTREAM_TEST_ATTEMPT_RECORD"
if [ "$count" -eq 1 ]; then
  printf 'listen tcp: address already in use\n' >&2
  exit 1
fi
while true; do sleep 1; done
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write retry fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("UPSTREAM_TEST_ATTEMPT_RECORD", attemptRecord)
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "UPSTREAM_TEST_ATTEMPT_RECORD")

	service := NewSingBoxService(t.TempDir())
	var checkerCalls atomic.Int32
	var addresses sync.Map
	info, err := service.checkUpstreamIPContext(
		context.Background(),
		models.ProxyDefinition{Type: "socks5", Config: `{"server":"upstream.example.com","server_port":1080}`},
		func(ctx context.Context, address, _, _ string) (*IPInfo, error) {
			addresses.Store(address, struct{}{})
			if checkerCalls.Add(1) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				content, readErr := os.ReadFile(attemptRecord)
				if readErr == nil && strings.TrimSpace(string(content)) == "2" {
					return &IPInfo{IP: "198.51.100.77"}, nil
				}
				time.Sleep(5 * time.Millisecond)
			}
			return nil, fmt.Errorf("second isolated process did not start")
		},
	)
	if err != nil || info == nil || info.IP != "198.51.100.77" {
		t.Fatalf("startup retry did not recover: info=%+v err=%v", info, err)
	}
	attempts, err := os.ReadFile(attemptRecord)
	if err != nil || strings.TrimSpace(string(attempts)) != "2" {
		t.Fatalf("unexpected process attempt count: value=%q err=%v", attempts, err)
	}
	addressCount := 0
	addresses.Range(func(_, _ any) bool {
		addressCount++
		return true
	})
	if addressCount != 2 {
		t.Fatalf("retry reused the same temporary port: unique addresses=%d", addressCount)
	}
}

func TestCheckUpstreamIPDoesNotRetryExternalQueryFailure(t *testing.T) {
	tempDir := t.TempDir()
	attemptRecord := filepath.Join(tempDir, "attempts")
	fakeBinary := filepath.Join(tempDir, "fake-sing-box")
	script := `#!/bin/sh
printf 'started\n' >> "$UPSTREAM_TEST_ATTEMPT_RECORD"
while true; do sleep 1; done
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write query-failure fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("UPSTREAM_TEST_ATTEMPT_RECORD", attemptRecord)
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "UPSTREAM_TEST_ATTEMPT_RECORD")

	service := NewSingBoxService(t.TempDir())
	_, err := service.checkUpstreamIPContext(
		context.Background(),
		models.ProxyDefinition{Type: "socks5", Config: `{"server":"upstream.example.com","server_port":1080}`},
		func(ctx context.Context, _ string, _ string, _ string) (*IPInfo, error) {
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if _, statErr := os.Stat(attemptRecord); statErr == nil {
					return nil, fmt.Errorf("external IP service returned HTTP 503")
				}
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(5 * time.Millisecond):
				}
			}
			return nil, fmt.Errorf("isolated process did not start")
		},
	)
	if err == nil || !strings.Contains(err.Error(), "external IP service") {
		t.Fatalf("unexpected external query failure: %v", err)
	}
	content, readErr := os.ReadFile(attemptRecord)
	if readErr != nil {
		t.Fatalf("read attempt record: %v", readErr)
	}
	if lines := strings.Count(strings.TrimSpace(string(content)), "started"); lines != 1 {
		t.Fatalf("external query failure was retried %d times", lines)
	}
}

func TestCheckUpstreamIPContextUsesIsolatedProcessAndCleansItUp(t *testing.T) {
	tempDir := t.TempDir()
	configPathRecord := filepath.Join(tempDir, "config-path")
	pidRecord := filepath.Join(tempDir, "pid")
	fakeBinary := filepath.Join(tempDir, "fake-sing-box")
	script := `#!/bin/sh
if [ "$1" = "run" ]; then
  printf '%s' "$3" > "$UPSTREAM_TEST_CONFIG_PATH_RECORD"
  printf '%s' "$$" > "$UPSTREAM_TEST_PID_RECORD"
  while true; do sleep 1; done
fi
exit 2
`
	if err := os.WriteFile(fakeBinary, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake sing-box: %v", err)
	}
	t.Setenv("SINGBOX_BINARY", fakeBinary)
	t.Setenv("UPSTREAM_TEST_CONFIG_PATH_RECORD", configPathRecord)
	t.Setenv("UPSTREAM_TEST_PID_RECORD", pidRecord)
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "UPSTREAM_TEST_CONFIG_PATH_RECORD,UPSTREAM_TEST_PID_RECORD")

	service := NewSingBoxService(t.TempDir())
	var isolatedConfigPath string
	info, err := service.checkUpstreamIPContext(
		context.Background(),
		models.ProxyDefinition{
			Type:   "socks5",
			Config: `{"server":"upstream.example.com","server_port":1080}`,
		},
		func(_ context.Context, address, username, password string) (*IPInfo, error) {
			if username != "" || password != "" {
				t.Fatalf("isolated inbound unexpectedly uses authentication")
			}
			host, port, err := net.SplitHostPort(address)
			if err != nil || host != "127.0.0.1" || port == "" {
				t.Fatalf("unexpected isolated proxy address %q: %v", address, err)
			}
			deadline := time.Now().Add(2 * time.Second)
			for {
				data, readErr := os.ReadFile(configPathRecord)
				if readErr == nil && len(data) > 0 {
					isolatedConfigPath = string(data)
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("fake sing-box did not record its config path: %v", readErr)
				}
				time.Sleep(10 * time.Millisecond)
			}
			configJSON, err := os.ReadFile(isolatedConfigPath)
			if err != nil {
				t.Fatalf("read isolated config: %v", err)
			}
			var config map[string]interface{}
			if err := json.Unmarshal(configJSON, &config); err != nil {
				t.Fatalf("decode isolated config: %v", err)
			}
			upstream := generatedEntryByTag(t, config, "outbounds", "node-1-upstream")
			if upstream["type"] != "socks" || upstream["server"] != "upstream.example.com" {
				t.Fatalf("unexpected isolated upstream: %#v", upstream)
			}
			selector := generatedEntryByTag(t, config, "outbounds", "node-1-out")
			if selector["type"] != "selector" || !strings.Contains(strings.TrimSpace(toString(selector["outbounds"])), "node-1-upstream") {
				t.Fatalf("isolated node does not route through upstream: %#v", selector)
			}
			return &IPInfo{IP: "198.51.100.40", Location: "Isolated", Latency: 25}, nil
		},
	)
	if err != nil {
		t.Fatalf("check isolated upstream: %v", err)
	}
	if info == nil || info.IP != "198.51.100.40" {
		t.Fatalf("unexpected isolated check result: %+v", info)
	}
	if isolatedConfigPath == "" {
		t.Fatal("isolated config path was not captured")
	}
	if _, err := os.Stat(isolatedConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("isolated config was not removed: %v", err)
	}

	pidData, err := os.ReadFile(pidRecord)
	if err != nil {
		t.Fatalf("read isolated process id: %v", err)
	}
	pid, err := strconv.Atoi(string(pidData))
	if err != nil {
		t.Fatalf("parse isolated process id: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("isolated sing-box process %d remains alive: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func toString(value interface{}) string {
	data, _ := json.Marshal(value)
	return string(data)
}
