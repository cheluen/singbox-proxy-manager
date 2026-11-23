package database

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestLogSelectedDatabaseDoesNotExposeTursoCredentials(t *testing.T) {
	originalWriter := log.Writer()
	originalFlags := log.Flags()
	originalPrefix := log.Prefix()
	t.Cleanup(func() {
		log.SetOutput(originalWriter)
		log.SetFlags(originalFlags)
		log.SetPrefix(originalPrefix)
	})

	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")

	tursoURL := "libsql://sensitive-database.example.invalid"
	tursoToken := "sensitive-auth-token"
	t.Setenv("TURSO_DATABASE_URL", tursoURL)
	t.Setenv("TURSO_AUTH_TOKEN", tursoToken)

	logSelectedDatabase(DialectTurso)

	got := strings.TrimSpace(output.String())
	if got != "Using turso database" {
		t.Fatalf("unexpected Turso selection log: %q", got)
	}
	if strings.Contains(got, tursoURL) || strings.Contains(got, tursoToken) {
		t.Fatalf("Turso selection log exposed connection credentials: %q", got)
	}
}

func TestNormalizeMySQLDSNFromURL(t *testing.T) {
	dsn, err := normalizeMySQLDSN("mysql://user:pass@example.com:4000/appdb?charset=utf8mb4")
	if err != nil {
		t.Fatalf("normalizeMySQLDSN failed: %v", err)
	}
	for _, fragment := range []string{"user:pass@tcp(example.com:4000)/appdb", "charset=utf8mb4", "clientFoundRows=true", "parseTime=true", "tls=true"} {
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
	if !strings.Contains(dsn, "clientFoundRows=true") {
		t.Fatalf("dsn should report matched rows for idempotent updates: %q", dsn)
	}
}

func TestNormalizeMySQLDSNPreservesExplicitOptions(t *testing.T) {
	for _, raw := range []string{
		"user@tcp(db.example.com:3306)/appdb?clientFoundRows=false&parseTime=false&tls=false&charset=utf8mb4",
		"mysql://user@db.example.com:3306/appdb?clientFoundRows=false&parseTime=false&tls=false&charset=utf8mb4",
		"user@tcp(db.example.com:3306)/appdb?clientFoundRows=false&parseTime=false&tls=&charset=utf8mb4",
		"mysql://user@db.example.com:3306/appdb?clientFoundRows=false&parseTime=false&tls=&charset=utf8mb4",
	} {
		dsn, err := normalizeMySQLDSN(raw)
		if err != nil {
			t.Fatal(err)
		}
		config, err := mysql.ParseDSN(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if !config.ClientFoundRows || config.ParseTime || config.TLS != nil || config.TLSConfig == "true" || !strings.Contains(dsn, "charset=utf8mb4") {
			t.Fatalf("expected matched rows and explicit time, TLS and charset options for %q, got %q", raw, dsn)
		}
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
