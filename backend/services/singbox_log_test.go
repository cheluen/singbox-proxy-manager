package services

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"sb-proxy/backend/models"
)

func TestRealKernelAuthenticationLogsAreRedacted(t *testing.T) {
	binary := os.Getenv("SINGBOX_TEST_BINARY")
	if binary == "" {
		t.Skip("SINGBOX_TEST_BINARY is not set")
	}
	t.Setenv("SBPM_SINGBOX_LOG_OUTPUT", "file")
	t.Setenv("SBPM_SINGBOX_STARTUP_GRACE", "80ms")
	dir := t.TempDir()
	s := NewSingBoxService(dir, WithBinaryPath(binary))
	observer := NewObservability(nil, s)
	defer s.Stop()
	node := models.ProxyNode{ID: 1, Name: "test", Type: "direct", Config: "{}", Enabled: true, InboundPort: loopbackTestPort(t), Username: "test-admin", Password: t.Name()}
	if err := NewRuntimeApplier(s).Apply([]models.ProxyNode{node}); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(http.NotFoundHandler())
	defer origin.Close()
	const rejected = "test-rejected-value with trailing text"
	address := loopback(node.InboundPort)
	httpProxy := &url.URL{Scheme: "http", Host: address, User: url.UserPassword(node.Username, rejected)}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(httpProxy), DisableKeepAlives: true}}
	reply, err := client.Get(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	reply.Body.Close()
	if reply.StatusCode != http.StatusProxyAuthRequired {
		t.Fatal("expected HTTP authentication failure")
	}
	socks, err := proxy.SOCKS5("tcp", address, &proxy.Auth{User: node.Username, Password: rejected}, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := socks.(proxy.ContextDialer).DialContext(t.Context(), "tcp", origin.Listener.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatal("expected SOCKS authentication failure")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		page, err := observer.Logs(t.Context(), LogFilter{Query: "authentication failed"})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) >= 2 {
			for _, event := range page.Items {
				if strings.Contains(event.Message, "test-rejected") || strings.Contains(event.Message, "trailing text") {
					t.Fatal("panel retained authentication data")
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("missing kernel auth log events")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(dir, "singbox.log"))
	if err != nil || !bytes.Contains(file, []byte("socks5: authentication failed")) || !bytes.Contains(file, []byte("http: authentication failed")) || bytes.Contains(file, []byte("test-rejected")) || bytes.Contains(file, []byte("trailing text")) {
		t.Fatal("kernel auth log on disk missing or unsafe")
	}
}

func TestSingBoxRedactsBeforeLogFanout(t *testing.T) {
	const fixture = "SBPM_LOG_FANOUT_FIXTURE"
	if dir := os.Getenv(fixture); dir != "" {
		s := NewSingBoxService(dir)
		s.features.wiring = &runtimeWiring{redactSecrets: strings.NewReplacer("configured-node-value", "[redacted]")}
		var observer *Observability
		if os.Getenv("SBPM_LOG_FIXTURE_OBSERVER") == "yes" {
			observer = NewObservability(nil, s)
		}
		stdout, stderr, closer, err := s.openProcessLog()
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range []io.Writer{stdout, stderr} {
			fmt.Fprint(output, "ERROR socks5: authentication failed, username=fixture, pass")
			fmt.Fprint(output, "word=bad-attempt with-private-tail\nINFO continuation-secret\nERROR continuation-secret\n2020-01-01 INFO continuation-secret\n+0000 2020-01-01 01:02:03 WARN continuation-secret\n")
			fmt.Fprint(output, time.Now().Format(kernelTimestamp), " WARN runtime configured-node-value")
		}
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
		if observer != nil {
			page, err := observer.Logs(t.Context(), LogFilter{})
			if err != nil || len(page.Items) != 4 {
				t.Fatal("missing panel events")
			}
			for _, event := range page.Items {
				fmt.Println("panel:", event.Message)
			}
		}
		return
	}
	for _, observe := range []string{"yes", "no"} {
		dir := t.TempDir()
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSingBoxRedactsBeforeLogFanout$")
		cmd.Env = append(os.Environ(), fixture+"="+dir, "SBPM_LOG_FIXTURE_OBSERVER="+observe, "SBPM_SINGBOX_LOG_OUTPUT=both")
		console, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.ReadFile(filepath.Join(dir, "singbox.log"))
		if err != nil {
			t.Fatal(err)
		}
		for _, output := range [][]byte{console, file} {
			if !bytes.Contains(output, []byte("[redacted]")) {
				t.Fatal("missing sanitized log line")
			}
			for _, secret := range []string{"bad-attempt", "with-private-tail", "continuation-secret", "configured-node-value"} {
				if bytes.Contains(output, []byte(secret)) {
					t.Fatal("credential reached a log sink")
				}
			}
		}
	}
}

func TestRotatingFileWriterEnforcesSizeAndRetention(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "singbox.log")
	writer, err := newRotatingFileWriter(logPath, 32, 2)
	if err != nil {
		t.Fatalf("open rotating writer: %v", err)
	}
	content := bytes.Repeat([]byte("0123456789"), 12)
	if count, err := writer.Write(content); err != nil || count != len(content) {
		t.Fatalf("write rotating log: count=%d err=%v", count, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close rotating writer: %v", err)
	}

	for _, path := range []string{logPath, logPath + ".1", logPath + ".2"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat retained log %s: %v", path, err)
		}
		if info.Size() > 32 {
			t.Fatalf("rotated log %s exceeds size cap: %d", path, info.Size())
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("rotated log %s mode=%#o want=0600", path, info.Mode().Perm())
		}
	}
	if _, err := os.Stat(logPath + ".3"); err == nil {
		t.Fatalf("rotation retained more backups than configured")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat excess backup: %v", err)
	}
}

func TestSingBoxStdoutLoggingDoesNotCreatePrivateLogFile(t *testing.T) {
	t.Setenv("SBPM_SINGBOX_LOG_OUTPUT", "stdout")
	configDir := t.TempDir()
	service := NewSingBoxService(configDir)
	_, _, closer, err := service.openProcessLog()
	if err != nil {
		t.Fatalf("open stdout log: %v", err)
	}
	if err := closer.Close(); err != nil {
		t.Fatalf("close stdout log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "singbox.log")); err == nil {
		t.Fatalf("stdout logging unexpectedly created singbox.log")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat singbox.log: %v", err)
	}
}
