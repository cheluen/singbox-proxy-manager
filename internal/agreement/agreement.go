package agreement

import (
	"bufio"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
)

const EnvKey = "ACCEPT_USER_AGREEMENT"

const acceptedFileName = "user-agreement.accepted"

const refusalMessage = `未同意用户协议，程序已退出。User agreement not accepted. Exiting.
直接运行二进制时，请在交互式终端重新运行并输入 y。
Docker 或非交互式部署请在 .env 的 ADMIN_PASSWORD 上方，或在环境变量中设置 ACCEPT_USER_AGREEMENT=true。
For a standalone binary, rerun in a terminal and enter y. For Docker or unattended startup, set ACCEPT_USER_AGREEMENT=true above ADMIN_PASSWORD in .env or in the environment.
完整协议见 USER_AGREEMENT.md。See USER_AGREEMENT.md for the full agreement.`

//go:embed user_agreement.md
var text string

var ErrNotAccepted = errors.New("user agreement not accepted")

type Terminal struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
	IsTTY  bool
}

type acceptance struct {
	SHA256     string    `json:"sha256"`
	AcceptedAt time.Time `json:"accepted_at"`
	Method     string    `json:"method"`
}

func IsTTY(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0 && isatty.IsTerminal(file.Fd())
}

func Require(configDir string, envValue string, terminal Terminal, ensureConfigDir func(string) error) error {
	switch strings.ToLower(strings.TrimSpace(envValue)) {
	case "true", "yes", "y", "1":
		return nil
	}

	path := filepath.Join(configDir, acceptedFileName)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
	if hasAcceptance(path, digest) {
		return nil
	}
	if !terminal.IsTTY {
		return refuse(terminal.ErrOut)
	}

	const delimiter = "------------------------------------------------------------------------"
	if _, err := fmt.Fprintf(terminal.Out, "%s\n%s%s\n如同意，请输入 y 并回车。Enter y to accept.\n", delimiter, text, delimiter); err != nil {
		return fmt.Errorf("display user agreement: %w", err)
	}
	answer, err := bufio.NewReader(terminal.In).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if err != nil || (answer != "y" && answer != "yes") {
		return refuse(terminal.ErrOut)
	}
	if err := ensureConfigDir(configDir); err != nil {
		return fmt.Errorf("secure user agreement directory: %w", err)
	}
	if err := saveAcceptance(path, acceptance{SHA256: digest, AcceptedAt: time.Now().UTC(), Method: "interactive"}); err != nil {
		return fmt.Errorf("save user agreement acceptance: %w", err)
	}
	_, _ = fmt.Fprintln(terminal.Out, "已记录用户协议同意状态。User agreement acceptance saved.")
	return nil
}

func refuse(output io.Writer) error {
	_, _ = fmt.Fprintln(output, refusalMessage)
	return ErrNotAccepted
}

func hasAcceptance(path, digest string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var saved acceptance
	return json.Unmarshal(data, &saved) == nil && saved.SHA256 == digest
}

func saveAcceptance(path string, record acceptance) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".user-agreement-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
