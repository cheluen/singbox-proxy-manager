package services

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

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
