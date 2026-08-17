package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"sb-proxy/backend/models"
)

type upstreamIPChecker func(context.Context, string, string, string) (*IPInfo, error)

const upstreamIPCheckMaxAttempts = 3

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

	binary, err := s.resolveSingBoxBinary()
	if err != nil {
		return nil, err
	}
	var lastErr error
	usedPorts := make(map[int]struct{}, upstreamIPCheckMaxAttempts)
	for attempt := 1; attempt <= upstreamIPCheckMaxAttempts; attempt++ {
		info, attemptErr, retryable := s.runUpstreamIPCheckAttempt(ctx, definition, checker, binary, usedPorts)
		if attemptErr == nil {
			return info, nil
		}
		lastErr = attemptErr
		if !retryable || ctx.Err() != nil {
			return nil, attemptErr
		}
	}
	return nil, fmt.Errorf("isolated upstream check failed after %d startup attempts: %w", upstreamIPCheckMaxAttempts, lastErr)
}

func (s *SingBoxService) runUpstreamIPCheckAttempt(
	ctx context.Context,
	definition models.ProxyDefinition,
	checker upstreamIPChecker,
	binary string,
	usedPorts map[int]struct{},
) (*IPInfo, error, bool) {
	port, err := reserveFreshLoopbackPort(usedPorts)
	if err != nil {
		return nil, fmt.Errorf("reserve upstream check port: %w", err), true
	}
	usedPorts[port] = struct{}{}
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
		return nil, fmt.Errorf("build isolated upstream check config: %w", err), false
	}

	tempDir, err := os.MkdirTemp("", "sbpm-upstream-check-*")
	if err != nil {
		return nil, fmt.Errorf("create isolated upstream check directory: %w", err), false
	}
	defer func() {
		if cleanupErr := os.RemoveAll(tempDir); cleanupErr != nil {
			log.Printf("[UpstreamIPCheck] Failed to remove temporary directory %s: %v", tempDir, cleanupErr)
		}
	}()
	if err := os.Chmod(tempDir, 0o700); err != nil {
		return nil, fmt.Errorf("secure isolated upstream check directory: %w", err), false
	}
	configPath := filepath.Join(tempDir, "config.json")
	if err := writeSensitiveFileAtomically(configPath, configJSON); err != nil {
		return nil, fmt.Errorf("write isolated upstream check config: %w", err), false
	}

	cmd := exec.Command(binary, "run", "-c", configPath)
	processGuard, err := prepareSingBoxCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("prepare isolated upstream check process: %w", err), true
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		_ = processGuard.Close()
		return nil, fmt.Errorf("start isolated upstream check process: %w", err), true
	}

	processDone := make(chan error, 1)
	go func() {
		waitErr := cmd.Wait()
		_ = processGuard.Close()
		processDone <- waitErr
	}()
	processExited := false
	var processErr error
	stopProcess := func() {
		if processExited {
			return
		}
		_ = terminateProcess(cmd)
		select {
		case processErr = <-processDone:
			processExited = true
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			select {
			case processErr = <-processDone:
				processExited = true
			case <-time.After(time.Second):
			}
		}
	}
	defer stopProcess()

	type checkResult struct {
		info *IPInfo
		err  error
	}
	attemptCtx, cancelAttempt := context.WithCancel(ctx)
	defer cancelAttempt()
	checkDone := make(chan checkResult, 1)
	checkerStarted := make(chan struct{})
	go func() {
		close(checkerStarted)
		info, checkErr := checker(attemptCtx, fmt.Sprintf("127.0.0.1:%d", port), "", "")
		checkDone <- checkResult{info: info, err: checkErr}
	}()
	<-checkerStarted

	select {
	case processErr = <-processDone:
		processExited = true
		cancelAttempt()
		return nil, upstreamProcessExitError(processErr, stdout.String(), stderr.String()), true
	case result := <-checkDone:
		if result.err == nil {
			return result.info, nil, false
		}
		select {
		case processErr = <-processDone:
			processExited = true
			return nil, upstreamProcessExitError(processErr, stdout.String(), stderr.String()), true
		default:
		}
		stopProcess()
		checkErr := appendUpstreamProcessDetail(result.err, stderr.String())
		return nil, checkErr, errors.Is(result.err, ErrProxyNotReady)
	case <-ctx.Done():
		cancelAttempt()
		return nil, ctx.Err(), false
	}
}

func upstreamProcessExitError(processErr error, stdout, stderr string) error {
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = strings.TrimSpace(stdout)
	}
	base := errors.New("isolated sing-box exited before the local proxy became ready")
	if processErr != nil {
		base = fmt.Errorf("%w: %v", base, processErr)
	}
	return appendUpstreamProcessDetail(base, detail)
}

func appendUpstreamProcessDetail(base error, detail string) error {
	detail = strings.TrimSpace(detail)
	if len(detail) > 4096 {
		detail = detail[len(detail)-4096:]
	}
	if detail == "" {
		return base
	}
	return fmt.Errorf("%w; sing-box: %s", base, detail)
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

func reserveFreshLoopbackPort(used map[int]struct{}) (int, error) {
	for attempt := 0; attempt < 10; attempt++ {
		port, err := reserveLoopbackPort()
		if err != nil {
			return 0, err
		}
		if _, exists := used[port]; !exists {
			return port, nil
		}
	}
	return 0, fmt.Errorf("operating system repeatedly returned a previously attempted port")
}
