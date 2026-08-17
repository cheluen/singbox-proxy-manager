package services

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

type commandProcessGuard struct {
	once    sync.Once
	closers []io.Closer
	err     error
}

func newCommandProcessGuard(closers ...io.Closer) *commandProcessGuard {
	return &commandProcessGuard{closers: closers}
}

func (g *commandProcessGuard) Close() error {
	if g == nil {
		return nil
	}
	g.once.Do(func() {
		var closeErrors []error
		for _, closer := range g.closers {
			if closer != nil {
				if err := closer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
					closeErrors = append(closeErrors, err)
				}
			}
		}
		g.err = errors.Join(closeErrors...)
	})
	return g.err
}

func prepareSingBoxCommand(cmd *exec.Cmd) (*commandProcessGuard, error) {
	if cmd == nil {
		return nil, errors.New("sing-box command is required")
	}
	cmd.Env = singBoxCommandEnvironment()
	return configureSysProcAttr(cmd)
}

func singBoxCommandEnvironment() []string {
	allowed := map[string]struct{}{
		"HOME":          {},
		"LANG":          {},
		"LC_ALL":        {},
		"PATH":          {},
		"SSL_CERT_DIR":  {},
		"SSL_CERT_FILE": {},
		"TMPDIR":        {},
		"TZ":            {},
	}
	for _, name := range strings.Split(os.Getenv("SINGBOX_ENV_ALLOWLIST"), ",") {
		name = strings.TrimSpace(name)
		if name != "" && !isSensitiveEnvironmentName(name) {
			allowed[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(allowed))
	for name := range allowed {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		if value, exists := os.LookupEnv(name); exists {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func isSensitiveEnvironmentName(name string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(name))
	if normalized == "" {
		return true
	}
	for _, exact := range []string{
		"MYSQL",
		"PGSQL",
		"POSTGRES_URL",
	} {
		if normalized == exact {
			return true
		}
	}
	for _, fragment := range []string{
		"PASSWORD",
		"PASSWD",
		"CREDENTIAL",
		"SECRET",
		"TOKEN",
		"DATABASE_URL",
		"_DSN",
		"PRIVATE_KEY",
		"API_KEY",
		"ACCESS_KEY",
	} {
		if strings.Contains(normalized, fragment) {
			return true
		}
	}
	return false
}
