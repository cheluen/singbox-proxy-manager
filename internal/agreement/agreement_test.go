package agreement

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserAgreementSynced(t *testing.T) {
	root, err := os.ReadFile(filepath.Join("..", "..", "USER_AGREEMENT.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(root, []byte(text)) {
		t.Fatal("USER_AGREEMENT.md does not match embedded user_agreement.md")
	}
}

func TestRequireUserAgreementEnvironment(t *testing.T) {
	for _, value := range []string{"true", "TRUE", "yes", "YeS", "y", "Y", "1", " true ", "true\r"} {
		t.Run(value, func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "config")
			if err := Require(configDir, value, Terminal{}, nil); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(configDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("environment acceptance created a config directory: %v", err)
			}
		})
	}
	for _, value := range []string{"", "false", "no", "on", "0", "2", "true story"} {
		t.Run(fmt.Sprintf("reject_%q", value), func(t *testing.T) {
			var diagnostics bytes.Buffer
			terminal := Terminal{ErrOut: &diagnostics}
			if err := Require(t.TempDir(), value, terminal, nil); !errors.Is(err, ErrNotAccepted) {
				t.Fatalf("got %v, want ErrNotAccepted", err)
			}
			assertRefusalGuidance(t, diagnostics.String())
		})
	}
}

func TestRequireUserAgreementTerminal(t *testing.T) {
	for _, test := range []struct {
		answer   string
		accepted bool
	}{
		{"n\n", false}, {"true\n", false}, {"\n", false}, {"", false}, {"y", false},
		{"y\n", true}, {"  YeS  \r\n", true},
	} {
		t.Run(fmt.Sprintf("answer_%q", test.answer), func(t *testing.T) {
			configDir := filepath.Join(t.TempDir(), "config")
			var output, diagnostics bytes.Buffer
			terminal := Terminal{In: strings.NewReader(test.answer), Out: &output, ErrOut: &diagnostics, IsTTY: true}
			err := Require(configDir, "", terminal, func(path string) error { return os.Mkdir(path, 0o700) })
			if !strings.Contains(output.String(), text) || !strings.Contains(output.String(), "Enter y to accept.") {
				t.Fatal("terminal output did not include the full agreement and prompt")
			}
			if !test.accepted {
				if !errors.Is(err, ErrNotAccepted) {
					t.Fatalf("got %v, want ErrNotAccepted", err)
				}
				assertRefusalGuidance(t, diagnostics.String())
				if _, err := os.Stat(configDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("refusal created a config directory: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(configDir, acceptedFileName)
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var stored acceptance
			if err := json.Unmarshal(contents, &stored); err != nil {
				t.Fatal(err)
			}
			if stored.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) || stored.AcceptedAt.IsZero() || stored.Method != "interactive" {
				t.Fatalf("invalid saved acceptance: %+v", stored)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("acceptance file must have mode 0600: %v, %v", info, err)
			}
			if err := Require(configDir, "false", Terminal{}, nil); err != nil {
				t.Fatalf("saved acceptance did not bypass the prompt: %v", err)
			}
		})
	}
}

func TestRequireUserAgreementRejectsOutdatedOrMalformedAcceptance(t *testing.T) {
	for _, content := range []string{`{"sha256":"old-digest"}`, `{invalid`} {
		configDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(configDir, acceptedFileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		terminal := Terminal{ErrOut: io.Discard}
		if err := Require(configDir, "", terminal, nil); !errors.Is(err, ErrNotAccepted) {
			t.Fatalf("accepted invalid record %q: %v", content, err)
		}
	}
}

func TestRequireUserAgreementFailedSaveRemovesTemporaryFile(t *testing.T) {
	configDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(configDir, acceptedFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	terminal := Terminal{In: strings.NewReader("yes\n"), Out: io.Discard, ErrOut: io.Discard, IsTTY: true}
	if err := Require(configDir, "", terminal, func(string) error { return nil }); err == nil {
		t.Fatal("overwriting a directory with the agreement record succeeded")
	}
	entries, err := os.ReadDir(configDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != acceptedFileName {
		t.Fatalf("temporary acceptance files remained: %v, %v", entries, err)
	}
}

func TestIsTTYRejectsDevNull(t *testing.T) {
	nonTerminal, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer nonTerminal.Close()
	if IsTTY(nonTerminal) {
		t.Fatal("null device is not an interactive terminal")
	}
}

func assertRefusalGuidance(t *testing.T, diagnostics string) {
	t.Helper()
	for _, expected := range []string{"未同意用户协议，程序已退出", "User agreement not accepted", "ADMIN_PASSWORD", "ACCEPT_USER_AGREEMENT=true", "USER_AGREEMENT.md", "Docker", "输入 y"} {
		if !strings.Contains(diagnostics, expected) {
			t.Fatalf("refusal diagnostics missing %q: %s", expected, diagnostics)
		}
	}
}
