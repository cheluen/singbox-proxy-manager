package database

import (
	"strings"
	"testing"
)

func TestNormalizeMySQLDSNFromURL(t *testing.T) {
	dsn, err := normalizeMySQLDSN("mysql://user:pass@example.com:4000/appdb?charset=utf8mb4")
	if err != nil {
		t.Fatalf("normalizeMySQLDSN failed: %v", err)
	}
	for _, fragment := range []string{"user:pass@tcp(example.com:4000)/appdb", "charset=utf8mb4", "parseTime=true", "tls=true"} {
		if !strings.Contains(dsn, fragment) {
			t.Fatalf("dsn %q missing %q", dsn, fragment)
		}
	}
}

func TestNormalizeMySQLDSNDoesNotForceTLSForLocalhost(t *testing.T) {
	dsn, err := normalizeMySQLDSN("mysql://user:pass@127.0.0.1:3306/appdb")
	if err != nil {
		t.Fatalf("normalizeMySQLDSN failed: %v", err)
	}
	if strings.Contains(dsn, "tls=true") {
		t.Fatalf("local mysql dsn should not force TLS: %q", dsn)
	}
	if !strings.Contains(dsn, "parseTime=true") {
		t.Fatalf("dsn should enable parseTime: %q", dsn)
	}
}

func TestNormalizeMySQLDSNUsesWritableDefaultForSystemSchema(t *testing.T) {
	dsn, err := normalizeMySQLDSN("mysql://user:pass@example.com:4000/sys")
	if err != nil {
		t.Fatalf("normalizeMySQLDSN failed: %v", err)
	}
	if !strings.Contains(dsn, "/test?") {
		t.Fatalf("system schema should normalize to test database: %q", dsn)
	}
}
