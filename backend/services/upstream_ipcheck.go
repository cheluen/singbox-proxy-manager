package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sb-proxy/backend/models"
)

type upstreamIPChecker func(context.Context, string, string, string) (*IPInfo, error)

const upstreamIPCheckMaxAttempts = 3

func (s *SingBoxService) UpstreamCheckTimeout() time.Duration {
	if s == nil || s.upstreamCheckTimeout <= 0 {
		return 30 * time.Second
	}
	return s.upstreamCheckTimeout
}

type isolatedIPCheckPlan struct {
	nodes         []parsedEnabledNode
	upstreams     []parsedUpstreamProxy
	target        *models.ProxyNode
	usedPorts     map[int]struct{}
	needsLocalDNS bool
	credentials   []string
}

// CheckUpstreamIPContext checks a managed upstream in an isolated sing-box
// process, leaving the primary runtime and its configuration untouched.
func (s *SingBoxService) CheckUpstreamIPContext(
	ctx context.Context,
	definition models.ProxyDefinition,
	listeners func(context.Context) ([]models.ProxyNode, error),
) (*IPInfo, error) {
	return s.checkUpstreamIPContext(ctx, definition, listeners, CheckProxyIPContext)
}

func (s *SingBoxService) checkUpstreamIPContext(
	ctx context.Context,
	definition models.ProxyDefinition,
	listeners func(context.Context) ([]models.ProxyNode, error),
	checker upstreamIPChecker,
) (*IPInfo, error) {
	target := models.ProxyNode{
		Enabled: true, Type: "direct", Config: `{}`, UpstreamMode: models.UpstreamModeCustom,
		UpstreamType: definition.Type, UpstreamConfig: definition.Config,
	}
	if strings.EqualFold(strings.TrimSpace(definition.Type), "direct") {
		upstream, err := s.parseManagedUpstreamDefinition(definition, "")
		if err != nil {
			return nil, redactIPCheckError(err, appendIPCheckConfigCredentials(nil, definition.Config))
		}
		if directUpstreamIsEmpty(upstream.Config) {
			target.UpstreamMode = models.UpstreamModeNone
		}
	}
	return s.CheckIsolatedNodeIPContext(ctx, target.ID, func(ctx context.Context) ([]models.ProxyNode, models.Settings, error) {
		var nodes []models.ProxyNode
		if listeners != nil {
			var err error
			nodes, err = listeners(ctx)
			if err != nil {
				return nil, models.Settings{}, errors.New("failed to load active inbound ports")
			}
		}
		return append(nodes, target), models.Settings{}, nil
	}, checker)
}

func (s *SingBoxService) CheckIsolatedNodeIPContext(
	ctx context.Context,
	targetID int,
	load func(context.Context) ([]models.ProxyNode, models.Settings, error),
	checker func(context.Context, string, string, string) (*IPInfo, error),
) (*IPInfo, error) {
	if s == nil {
		return nil, fmt.Errorf("sing-box service is unavailable")
	}
	if checker == nil || load == nil {
		return nil, fmt.Errorf("isolated IP checker is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkCtx, cancelCheck := context.WithTimeout(ctx, s.UpstreamCheckTimeout())
	defer cancelCheck()
	release, err := acquireExecutionSlot(checkCtx, s.upstreamCheckSlots, "isolated IP check")
	if err != nil {
		return nil, err
	}
	defer release()

	nodes, settings, err := load(checkCtx)
	if err != nil {
		return nil, err
	}
	plan, err := s.prepareIsolatedIPCheck(checkCtx, targetID, nodes, settings)
	if err != nil {
		return nil, fmt.Errorf("build isolated IP check config: %w", err)
	}
	binary, err := s.resolveSingBoxBinary()
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 1; attempt <= upstreamIPCheckMaxAttempts; attempt++ {
		info, attemptErr, retryable := s.runIsolatedIPCheckAttempt(checkCtx, plan, checker, binary)
		if attemptErr == nil {
			return info, nil
		}
		lastErr = attemptErr
		if !retryable || checkCtx.Err() != nil {
			return nil, attemptErr
		}
	}
	return nil, fmt.Errorf("isolated IP check failed after %d startup attempts: %w", upstreamIPCheckMaxAttempts, lastErr)
}

func isolatedNodeDependencies(targetID int, nodes []models.ProxyNode) ([]models.ProxyNode, error) {
	byTag := make(map[string]int)
	byName := make(map[string][]int)
	var pending []int
	for index, node := range nodes {
		if !node.Enabled {
			continue
		}
		byTag[fmt.Sprintf("node-%d-out", node.ID)] = index
		name := strings.TrimSpace(node.Name)
		byName[name] = append(byName[name], index)
		if node.ID == targetID {
			pending = append(pending, index)
		}
	}
	if len(pending) == 0 {
		return nil, fmt.Errorf("enabled node %d not found", targetID)
	}
	seen := make(map[int]bool)
	var selected []models.ProxyNode
	for len(pending) != 0 {
		index := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[index] {
			continue
		}
		seen[index] = true
		node := nodes[index]
		node.InboundPort, node.Username, node.Password, node.TCPReuseEnabled = 0, "", "", false
		node.InboundMode = models.InboundModeMixed
		selected = append(selected, node)
		mode, err := models.NormalizeUpstreamMode(node.UpstreamMode)
		if err != nil {
			return nil, err
		}
		if mode != models.UpstreamModeLegacy {
			continue
		}
		var reference struct {
			Detour string `json:"detour"`
		}
		if err := json.Unmarshal([]byte(node.Config), &reference); err != nil {
			return nil, fmt.Errorf("read node %d detour: %w", node.ID, err)
		}
		detour := strings.TrimSpace(reference.Detour)
		if detour == "" || detour == "direct" {
			continue
		}
		if other, exists := byTag[detour]; exists {
			pending = append(pending, other)
		} else {
			pending = append(pending, byName[detour]...)
		}
	}
	return selected, nil
}

func (s *SingBoxService) prepareIsolatedIPCheck(ctx context.Context, targetID int, nodes []models.ProxyNode, settings models.Settings) (_ *isolatedIPCheckPlan, err error) {
	selected, err := isolatedNodeDependencies(targetID, nodes)
	if err != nil {
		return nil, err
	}
	var usesGlobal bool
	var credentials []string
	usedGroups := make(map[int]models.GroupUpstream)
	for _, group := range settings.GroupUpstreams {
		credentials = appendIPCheckConfigCredentials(credentials, group.Config)
	}
	for _, node := range selected {
		source, _ := settings.EffectiveUpstream(node)
		usesGlobal = usesGlobal || source == models.UpstreamModeGlobal
		if source == models.UpstreamModeGroup {
			usedGroups[node.GroupID] = settings.GroupUpstreams[node.GroupID]
		}
		credentials = appendIPCheckConfigCredentials(credentials, node.Config, node.UpstreamConfig)
	}
	settings.GroupUpstreams = usedGroups
	settings.GlobalUpstreamEnabled = usesGlobal
	if settings.GlobalUpstreamEnabled {
		credentials = appendIPCheckConfigCredentials(credentials, settings.GlobalUpstreamConfig)
	}
	defer func() { err = redactIPCheckError(err, credentials) }()
	parsed, localDNS, err := prepareEnabledNodes(selected)
	if err != nil {
		return nil, err
	}
	upstreams, upstreamDNS, err := s.prepareManagedUpstreams(ctx, parsed, settings, 0)
	if err != nil {
		return nil, err
	}
	referenced := make(map[string]bool, len(parsed))
	for _, node := range parsed {
		referenced[node.ManagedUpstreamTag] = true
		referenced[configDetour(node.Config)] = true
	}
	upstreams = slices.DeleteFunc(upstreams, func(upstream parsedUpstreamProxy) bool { return !referenced[upstream.Tag] })
	plan := &isolatedIPCheckPlan{
		nodes: parsed, target: parsed[0].Node, upstreams: upstreams,
		usedPorts: make(map[int]struct{}), needsLocalDNS: localDNS || upstreamDNS, credentials: credentials,
	}
	inbounds := make(map[int]*models.ProxyNode)
	for port, tag := range s.runningInboundTags() {
		id, _ := tagID(tag, "node-", "-in")
		inbounds[port] = &models.ProxyNode{ID: id, InboundPort: port}
		plan.usedPorts[port] = struct{}{}
	}
	for index := range nodes {
		node := &nodes[index]
		if node.InboundPort > 0 {
			plan.usedPorts[node.InboundPort] = struct{}{}
			if node.Enabled {
				inbounds[node.InboundPort] = node
			}
		}
	}
	if err := s.validateManagedUpstreamInboundCollisions(ctx, upstreams, inbounds); err != nil {
		return nil, err
	}
	for _, node := range parsed {
		if wireguard, ok := node.Config.(*models.WireGuardConfig); ok {
			wireguard.SystemInterface, wireguard.InterfaceName, wireguard.ListenPort = false, "", 0
		}
	}
	for _, upstream := range upstreams {
		if endpoint := upstream.Endpoint; endpoint != nil {
			delete(endpoint.Extra, "system")
			delete(endpoint.Extra, "name")
			delete(endpoint.Extra, "listen_port")
		}
	}
	return plan, nil
}

func (s *SingBoxService) runIsolatedIPCheckAttempt(
	ctx context.Context,
	plan *isolatedIPCheckPlan,
	checker upstreamIPChecker,
	binary string,
) (info *IPInfo, err error, retryable bool) {
	reservation, err := reserveFreshLoopbackPort(plan.usedPorts)
	if err != nil {
		return nil, fmt.Errorf("reserve upstream check port: %w", err), true
	}
	defer reservation.Close()
	port := reservation.Addr().(*net.TCPAddr).Port
	plan.usedPorts[port] = struct{}{}
	credentialBytes := make([]byte, 32)
	if _, err := rand.Read(credentialBytes); err != nil {
		return nil, fmt.Errorf("generate upstream check credentials: %w", err), false
	}
	username := hex.EncodeToString(credentialBytes[:8])
	password := hex.EncodeToString(credentialBytes[8:])
	credentials := append([]string{username, password}, plan.credentials...)
	defer func() {
		err = redactIPCheckError(err, credentials)
		if info != nil && info.HTTPError != "" {
			clean := *info
			clean.HTTPError = redactIPCheckDiagnostic(info.HTTPError, credentials...)
			info = &clean
		}
	}()
	plan.target.InboundPort, plan.target.Username, plan.target.Password = port, username, password
	if err := s.validateManagedUpstreamInboundCollisions(ctx, plan.upstreams, map[int]*models.ProxyNode{port: plan.target}); err != nil {
		return nil, fmt.Errorf("build isolated upstream check config: %w", err), false
	}
	configJSON, err := s.renderGlobalConfig(plan.nodes, plan.upstreams, "127.0.0.1", plan.needsLocalDNS)
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
	stdout := newBoundedCommandOutput()
	stderr := newBoundedCommandOutput()
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := reservation.Close(); err != nil {
		_ = processGuard.Close()
		return nil, fmt.Errorf("release isolated IP check port: %w", err), true
	}
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
		info, checkErr := checker(attemptCtx, fmt.Sprintf("127.0.0.1:%d", port), username, password)
		checkDone <- checkResult{info: info, err: checkErr}
	}()
	<-checkerStarted

	select {
	case processErr = <-processDone:
		processExited = true
		cancelAttempt()
		return nil, upstreamProcessExitError(processErr, stdout.String(), stderr.String()), true
	case result := <-checkDone:
		select {
		case processErr = <-processDone:
			processExited = true
			return nil, upstreamProcessExitError(processErr, stdout.String(), stderr.String()), true
		default:
		}
		if result.err == nil {
			return result.info, nil, false
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
	detail = strings.ToLower(detail)
	for _, cause := range []string{"address already in use", "permission denied", "connection refused", "network is unreachable", "i/o timeout", "invalid key", "authentication failed"} {
		if strings.Contains(detail, cause) {
			return fmt.Errorf("%w; sing-box: %s", base, cause)
		}
	}
	return base
}

func reserveFreshLoopbackPort(used map[int]struct{}) (*net.TCPListener, error) {
	for attempt := 0; attempt < 10; attempt++ {
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			return nil, err
		}
		if _, exists := used[listener.Addr().(*net.TCPAddr).Port]; !exists {
			return listener, nil
		}
		_ = listener.Close()
	}
	return nil, fmt.Errorf("operating system repeatedly returned an excluded port")
}
