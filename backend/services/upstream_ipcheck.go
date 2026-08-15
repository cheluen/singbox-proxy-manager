package services

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sb-proxy/backend/models"
)

type upstreamIPChecker func(context.Context, string, string, string) (*IPInfo, error)

// CheckUpstreamIPContext checks a managed upstream in an isolated sing-box
// process, leaving the primary runtime and its configuration untouched.
func (s *SingBoxService) CheckUpstreamIPContext(
	ctx context.Context,
	definition models.ProxyDefinition,
) (*IPInfo, error) {
	return s.checkUpstreamIPContext(ctx, definition, CheckProxyIPContext)
}

func (s *SingBoxService) checkUpstreamIPContext(
	ctx context.Context,
	definition models.ProxyDefinition,
	checker upstreamIPChecker,
) (*IPInfo, error) {
	if s == nil {
		return nil, fmt.Errorf("sing-box service is unavailable")
	}
	if checker == nil {
		return nil, fmt.Errorf("upstream IP checker is unavailable")
	}
	if err := s.ValidateUpstreamDefinition(definition); err != nil {
		return nil, err
	}

	port, err := reserveLoopbackPort()
	if err != nil {
		return nil, fmt.Errorf("reserve upstream check port: %w", err)
	}
	configJSON, err := s.BuildGlobalConfig([]models.ProxyNode{{
		ID:             1,
		Name:           "upstream-ip-check",
		Type:           "direct",
		Config:         `{}`,
		InboundPort:    port,
		UpstreamMode:   models.UpstreamModeCustom,
		UpstreamType:   definition.Type,
		UpstreamConfig: definition.Config,
		Enabled:        true,
	}})
	if err != nil {
		return nil, fmt.Errorf("build isolated upstream check config: %w", err)
	}

	tempDir, err := os.MkdirTemp("", "sbpm-upstream-check-*")
	if err != nil {
		return nil, fmt.Errorf("create isolated upstream check directory: %w", err)
	}
	defer os.RemoveAll(tempDir)
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure isolated upstream check directory: %w", err)
	}
	configPath := filepath.Join(tempDir, "config.json")
	if err := writeSensitiveFileAtomically(configPath, configJSON); err != nil {
		return nil, fmt.Errorf("write isolated upstream check config: %w", err)
	}

	binary, err := s.resolveSingBoxBinary()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(binary, "run", "-c", configPath)
	configureSysProcAttr(cmd)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start isolated upstream check process: %w", err)
	}

	processDone := make(chan error, 1)
	go func() {
		processDone <- cmd.Wait()
	}()
	stopped := false
	stopProcess := func() {
		if stopped {
			return
		}
		stopped = true
		_ = terminateProcess(cmd)
		select {
		case <-processDone:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			select {
			case <-processDone:
			case <-time.After(time.Second):
			}
		}
	}
	defer stopProcess()

	info, checkErr := checker(ctx, fmt.Sprintf("127.0.0.1:%d", port), "", "")
	if checkErr == nil {
		return info, nil
	}
	stopProcess()
	detail := strings.TrimSpace(stderr.String())
	if len(detail) > 4096 {
		detail = detail[len(detail)-4096:]
	}
	if detail == "" {
		return nil, checkErr
	}
	return nil, fmt.Errorf("%w; sing-box: %s", checkErr, detail)
}

func reserveLoopbackPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || address.Port <= 0 {
		return 0, fmt.Errorf("unexpected listener address %q", listener.Addr())
	}
	return address.Port, nil
}
