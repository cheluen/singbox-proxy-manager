package services

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"

	"sb-proxy/backend/models"
)

type RuntimeApplier struct {
	service *SingBoxService
}

type RuntimePlan struct {
	configJSON []byte
	hash       string
}

type runtimeFileState struct {
	content []byte
	exists  bool
}

type runtimeApplySnapshot struct {
	current        runtimeFileState
	lastGood       runtimeFileState
	status         SingBoxRuntimeStatus
	desiredRunning bool
	processRunning bool
}

type RuntimeApplyTransaction struct {
	service  *SingBoxService
	snapshot runtimeApplySnapshot
	plan     RuntimePlan
	done     bool
	mu       sync.Mutex
}

func NewRuntimeApplier(service *SingBoxService) *RuntimeApplier {
	return &RuntimeApplier{service: service}
}

func (a *RuntimeApplier) Prepare(nodes []models.ProxyNode, settings ...models.Settings) (*RuntimePlan, error) {
	return a.PrepareContext(context.Background(), nodes, settings...)
}

func (a *RuntimeApplier) PrepareContext(
	ctx context.Context,
	nodes []models.ProxyNode,
	settings ...models.Settings,
) (*RuntimePlan, error) {
	if a == nil || a.service == nil {
		return nil, fmt.Errorf("runtime applier is not configured")
	}
	configJSON, err := a.service.BuildGlobalConfigContext(ctx, nodes, settings...)
	if err != nil {
		return nil, fmt.Errorf("runtime build stage failed: %w", err)
	}
	if err := a.service.ValidateConfigContext(ctx, configJSON); err != nil {
		return nil, fmt.Errorf("runtime validation stage failed: %w", err)
	}
	return &RuntimePlan{
		configJSON: append([]byte(nil), configJSON...),
		hash:       runtimeConfigHash(configJSON),
	}, nil
}

func (a *RuntimeApplier) Apply(nodes []models.ProxyNode, settings ...models.Settings) error {
	plan, err := a.Prepare(nodes, settings...)
	if err != nil {
		return err
	}
	transaction, err := a.Begin(plan)
	if err != nil {
		a.service.recordDesiredConfigHash(plan.hash)
		return err
	}
	transaction.Commit()
	return nil
}

func (a *RuntimeApplier) Begin(plan *RuntimePlan) (*RuntimeApplyTransaction, error) {
	if a == nil || a.service == nil {
		return nil, fmt.Errorf("runtime applier is not configured")
	}
	if plan == nil || len(plan.configJSON) == 0 {
		return nil, fmt.Errorf("runtime plan is empty")
	}
	return a.service.beginRuntimeApply(*plan)
}

func runtimeConfigHash(configJSON []byte) string {
	sum := sha256.Sum256(configJSON)
	return fmt.Sprintf("%x", sum[:8])
}

func readRuntimeFile(path string) (runtimeFileState, error) {
	content, err := os.ReadFile(path)
	if err == nil {
		return runtimeFileState{content: content, exists: true}, nil
	}
	if os.IsNotExist(err) {
		return runtimeFileState{}, nil
	}
	return runtimeFileState{}, err
}

func restoreRuntimeFile(path string, state runtimeFileState) error {
	if state.exists {
		return writeSensitiveFileAtomically(path, state.content)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *SingBoxService) captureRuntimeApplySnapshot() (runtimeApplySnapshot, error) {
	current, err := readRuntimeFile(s.configPath())
	if err != nil {
		return runtimeApplySnapshot{}, fmt.Errorf("failed to read current runtime config: %w", err)
	}
	lastGood, err := readRuntimeFile(s.lastGoodConfigPath())
	if err != nil {
		return runtimeApplySnapshot{}, fmt.Errorf("failed to read last-good runtime config: %w", err)
	}

	s.mu.RLock()
	snapshot := runtimeApplySnapshot{
		current:        current,
		lastGood:       lastGood,
		status:         s.runtimeStatus,
		desiredRunning: s.desiredRunning,
		processRunning: s.process != nil,
	}
	s.mu.RUnlock()
	return snapshot, nil
}

func (s *SingBoxService) beginRuntimeApply(plan RuntimePlan) (*RuntimeApplyTransaction, error) {
	s.operationMu.Lock()
	snapshot, err := s.captureRuntimeApplySnapshot()
	if err != nil {
		s.operationMu.Unlock()
		return nil, err
	}

	s.mu.Lock()
	s.cancelRecoveryLocked()
	s.recoveryAttempts = 0
	s.desiredRunning = true
	s.runtimeStatus.ApplyStage = "writing"
	s.runtimeStatus.DesiredConfigHash = plan.hash
	s.mu.Unlock()

	if err := s.writeConfigFile(plan.configJSON); err != nil {
		s.mu.Lock()
		s.desiredRunning = snapshot.desiredRunning
		s.runtimeStatus = snapshot.status
		s.mu.Unlock()
		s.operationMu.Unlock()
		return nil, fmt.Errorf("runtime write stage failed: %w", err)
	}

	s.setApplyStage("starting", plan.hash)
	if err := s.stopProcessLocked(); err != nil {
		return nil, s.failRuntimeApplyLocked(snapshot, plan, "stop", err)
	}
	if err := s.startProcessLocked(plan.hash); err != nil {
		return nil, s.failRuntimeApplyLocked(snapshot, plan, "start", err)
	}

	s.setApplyStage("snapshotting", plan.hash)
	if err := s.saveLastGoodBytes(plan.configJSON); err != nil {
		return nil, s.failRuntimeApplyLocked(snapshot, plan, "snapshot", err)
	}

	s.setApplyStage("ready", plan.hash)
	return &RuntimeApplyTransaction{
		service:  s,
		snapshot: snapshot,
		plan:     plan,
	}, nil
}

func (s *SingBoxService) setApplyStage(stage string, desiredHash string) {
	s.mu.Lock()
	s.runtimeStatus.ApplyStage = stage
	s.runtimeStatus.DesiredConfigHash = desiredHash
	s.mu.Unlock()
}

func (s *SingBoxService) failRuntimeApplyLocked(
	snapshot runtimeApplySnapshot,
	plan RuntimePlan,
	stage string,
	stageErr error,
) error {
	rollbackErr := s.rollbackRuntimeApplyLocked(snapshot, plan.hash)
	s.operationMu.Unlock()
	if rollbackErr != nil {
		return fmt.Errorf(
			"runtime %s stage failed: %v; rollback also failed: %w",
			stage,
			stageErr,
			rollbackErr,
		)
	}
	return fmt.Errorf("runtime %s stage failed and was rolled back: %w", stage, stageErr)
}

func (s *SingBoxService) rollbackRuntimeApplyLocked(
	snapshot runtimeApplySnapshot,
	failedHash string,
) error {
	s.mu.Lock()
	s.cancelRecoveryLocked()
	s.desiredRunning = false
	s.mu.Unlock()

	var rollbackErr error
	if err := s.stopProcessLocked(); err != nil {
		rollbackErr = fmt.Errorf("stop failed candidate: %w", err)
	}

	fallback := selectRuntimeFallback(snapshot)
	if err := restoreRuntimeFile(s.configPath(), fallback); err != nil {
		rollbackErr = combineRuntimeErrors(rollbackErr, fmt.Errorf("restore live config: %w", err))
	}

	fallbackHash := ""
	desiredHash := snapshot.status.DesiredConfigHash
	shouldStart := fallback.exists && (snapshot.processRunning || snapshot.lastGood.exists || runtimeConfigHash(fallback.content) != failedHash)
	if rollbackErr == nil && shouldStart {
		fallbackHash = runtimeConfigHash(fallback.content)
		if desiredHash == "" {
			desiredHash = fallbackHash
		}
		s.mu.Lock()
		s.desiredRunning = true
		s.runtimeStatus.ApplyStage = snapshot.status.ApplyStage
		s.runtimeStatus.DesiredConfigHash = desiredHash
		s.mu.Unlock()
		if err := s.startProcessLocked(fallbackHash); err != nil {
			rollbackErr = fmt.Errorf("restart previous runtime: %w", err)
		}
	}
	lastGoodState := snapshot.lastGood
	if shouldStart && rollbackErr == nil {
		lastGoodState = fallback
	}
	if err := restoreRuntimeFile(s.lastGoodConfigPath(), lastGoodState); err != nil {
		rollbackErr = combineRuntimeErrors(rollbackErr, fmt.Errorf("restore last-good snapshot: %w", err))
	}

	if rollbackErr != nil {
		s.MarkDegraded(rollbackErr)
		return rollbackErr
	}
	if shouldStart && desiredHash != fallbackHash {
		s.markConfigMismatch("rolled back")
	}
	if !shouldStart {
		s.mu.Lock()
		s.desiredRunning = snapshot.desiredRunning
		s.runtimeStatus = snapshot.status
		s.mu.Unlock()
	}
	return nil
}

func (s *SingBoxService) recordDesiredConfigHash(hash string) {
	s.mu.Lock()
	s.runtimeStatus.DesiredConfigHash = hash
	s.mu.Unlock()
}

func selectRuntimeFallback(snapshot runtimeApplySnapshot) runtimeFileState {
	if snapshot.processRunning && snapshot.status.ActiveConfigHash != "" {
		if snapshot.current.exists && runtimeConfigHash(snapshot.current.content) == snapshot.status.ActiveConfigHash {
			return snapshot.current
		}
		if snapshot.lastGood.exists && runtimeConfigHash(snapshot.lastGood.content) == snapshot.status.ActiveConfigHash {
			return snapshot.lastGood
		}
	}
	if snapshot.processRunning && snapshot.current.exists {
		return snapshot.current
	}
	if snapshot.lastGood.exists {
		return snapshot.lastGood
	}
	return snapshot.current
}

func combineRuntimeErrors(existing error, next error) error {
	if existing == nil {
		return next
	}
	return fmt.Errorf("%v; %w", existing, next)
}

func (transaction *RuntimeApplyTransaction) Commit() {
	if transaction == nil || transaction.service == nil {
		return
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.done {
		return
	}
	transaction.service.setApplyStage("committed", transaction.plan.hash)
	transaction.done = true
	transaction.service.operationMu.Unlock()
}

func (transaction *RuntimeApplyTransaction) Rollback() error {
	if transaction == nil || transaction.service == nil {
		return nil
	}
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.done {
		return nil
	}
	err := transaction.service.rollbackRuntimeApplyLocked(
		transaction.snapshot,
		transaction.plan.hash,
	)
	transaction.done = true
	transaction.service.operationMu.Unlock()
	return err
}
