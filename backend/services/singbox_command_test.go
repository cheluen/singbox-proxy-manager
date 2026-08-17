package services

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPrepareSingBoxCommandUsesMinimalEnvironment(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "administrator-secret")
	t.Setenv("DATABASE_URL", "postgres://database-secret@example.invalid/app")
	t.Setenv("POSTGRES_URL", "postgres://postgres-alias-secret@example.invalid/app")
	t.Setenv("PGSQL", "postgres://pgsql-alias-secret@example.invalid/app")
	t.Setenv("MYSQL", "mysql://mysql-alias-secret@example.invalid/app")
	t.Setenv("TURSO_AUTH_TOKEN", "turso-secret")
	t.Setenv("GITHUB_TOKEN", "github-secret")
	t.Setenv("SBPM_TEST_VISIBLE_VALUE", "visible-to-test-child")
	t.Setenv("SBPM_TEST_PASSWORD", "still-must-be-blocked")
	t.Setenv("SINGBOX_ENV_ALLOWLIST", "SBPM_TEST_VISIBLE_VALUE,SBPM_TEST_PASSWORD,ADMIN_PASSWORD,POSTGRES_URL,PGSQL,MYSQL")

	command := exec.Command("/bin/true")
	guard, err := prepareSingBoxCommand(command)
	if err != nil {
		t.Fatalf("prepare command: %v", err)
	}
	t.Cleanup(func() { _ = guard.Close() })

	joined := strings.Join(command.Env, "\n")
	if !strings.Contains(joined, "SBPM_TEST_VISIBLE_VALUE=visible-to-test-child") {
		t.Fatalf("explicit non-sensitive allowlist entry was omitted: %q", joined)
	}
	for _, secret := range []string{
		"administrator-secret",
		"database-secret",
		"postgres-alias-secret",
		"pgsql-alias-secret",
		"mysql-alias-secret",
		"turso-secret",
		"github-secret",
		"still-must-be-blocked",
	} {
		if strings.Contains(joined, secret) {
			t.Fatalf("sing-box command inherited sensitive value %q: %q", secret, joined)
		}
	}
}
