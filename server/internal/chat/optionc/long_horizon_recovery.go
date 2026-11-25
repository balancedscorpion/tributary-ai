package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
)

var errIncompatibleContinuationSnapshot = errors.New("incompatible continuation execution snapshot")

const (
	longHorizonRecoveryTick  = 5 * time.Second
	longHorizonRecoveryBatch = 100
)

// StartLongHorizonRecovery starts the durable continuation scheduler. Query
// monitors are rehydrated separately by jobs.Registry; this worker owns only
// run wake claims and exact model-loop checkpoint resumption.
func (m *Manager) StartLongHorizonRecovery(ctx context.Context) {
	if m == nil || m.longHorizon == nil {
		return
	}
	m.longHorizonRecoveryOnce.Do(func() {
		go m.runLongHorizonRecovery(ctx)
	})
}

func (m *Manager) runLongHorizonRecovery(ctx context.Context) {
	m.recoverLongHorizonRuns(ctx)
	ticker := time.NewTicker(longHorizonRecoveryTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.recoverLongHorizonRuns(ctx)
		}
	}
}

func (m *Manager) recoverLongHorizonRuns(ctx context.Context) {
	runs, err := m.longHorizon.ListRecoverableRuns(ctx, time.Now().UTC(), longHorizonRecoveryBatch)
	if err != nil {
		log.Printf("long-horizon run recovery scan failed: %v", err)
		return
	}
	for _, run := range runs {
		if ctx.Err() != nil {
			return
		}
		if m.runActiveInThisProcess(run.ID, run.ThreadID) {
			continue
		}
		if run.Status == store.RunStatusRunning {
			// Initial executions have no resumable model checkpoint yet. Only an
			// expired execution lease makes a new-format run recoverable. Legacy
			// lease-less rows use a conservative age fence in the store and still
			// require durable query work here before recovery may touch them.
			if run.ResumeLeaseExpiresAt == nil && len(m.queryDependenciesForRun(run.ThreadID, run.ID)) == 0 {
				// A pre-lease running row cannot be replayed safely without either a
				// checkpoint or durable query work. Park it for user review instead of
				// returning it on every bounded recovery scan: otherwise enough stale
				// legacy rows can permanently starve actionable runs later in the batch.
				if err := m.transitionRunToWaitingUser(
					context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, "",
				); err != nil && !errors.Is(err, store.ErrConflict) {
					log.Printf("run %s: failed to quarantine stale legacy execution: %v", run.ID, err)
				}
				continue
			}
		}
		if run.Status == store.RunStatusWaitingExternal && !m.queryRunHasActionableWake(run) {
			if err := m.rescheduleRunRecoveryProbe(ctx, run); err != nil {
				log.Printf("run %s: failed to reschedule dependency probe: %v", run.ID, err)
			}
			continue
		}
		claimed, outcome, claimToken, err := m.claimRunContinuation(ctx, run.ID)
		if err != nil {
			log.Printf("run %s: durable recovery claim failed: %v", run.ID, err)
			continue
		}
		if outcome != continuationClaimAcquired {
			continue
		}
		checkpoint, err := m.longHorizon.LatestContinuationCheckpoint(ctx, claimed.ID)
		if err != nil || checkpoint == nil {
			reason := "durable continuation checkpoint is missing"
			if err != nil {
				reason = "durable continuation checkpoint is unreadable: " + err.Error()
			}
			if transitionErr := m.transitionRunToWaitingUser(context.Background(), claimed.ID, claimed.ContinuationCount, claimed.MaxAutoContinuations, claimToken); transitionErr != nil {
				log.Printf("run %s: failed to quarantine missing checkpoint: %v", claimed.ID, transitionErr)
			}
			log.Printf("run %s: %s", claimed.ID, reason)
			continue
		}
		if err := m.launchRecoveredRun(ctx, claimed, checkpoint, claimToken); err != nil {
			if errors.Is(err, errContinuationSuperseded) {
				continue
			}
			if errors.Is(err, errIncompatibleContinuationSnapshot) || errors.Is(err, store.ErrNotFound) {
				_ = m.transitionRunToWaitingUser(context.Background(), claimed.ID, claimed.ContinuationCount, claimed.MaxAutoContinuations, claimToken)
				log.Printf("run %s: recovery quarantined: %v", claimed.ID, err)
				continue
			}
			if requeueErr := m.requeueClaimedRun(context.Background(), claimed.ID, claimToken); requeueErr != nil && !errors.Is(requeueErr, store.ErrConflict) {
				log.Printf("run %s: transient recovery launch failed and requeue failed: launch=%v requeue=%v", claimed.ID, err, requeueErr)
			} else {
				log.Printf("run %s: transient recovery launch deferred: %v", claimed.ID, err)
			}
		}
	}
}

func (m *Manager) runActiveInThisProcess(runID, threadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if active := strings.TrimSpace(m.activeByThread[strings.TrimSpace(threadID)]); active != "" {
		return true
	}
	state := m.runs[strings.TrimSpace(runID)]
	if state == nil {
		return false
	}
	state.mu.Lock()
	done := state.done
	state.mu.Unlock()
	return !done
}

func (m *Manager) queryRunHasActionableWake(run store.Run) bool {
	// An expired resuming lease means a model continuation was already
	// legitimately claimed before its worker died. It should be stolen and
	// resumed without consuming a second continuation count.
	if run.Status == store.RunStatusResuming {
		return true
	}
	refs := m.queryDependenciesForRun(run.ThreadID, run.ID)
	if len(refs) == 0 {
		// Dependencies may have become validated since the run suspended; the
		// final assistant turn still has to be allowed to complete.
		return true
	}
	registry, ok := m.queryJobs.(*jobs.Registry)
	if !ok || registry == nil {
		return true
	}
	var checkpointAt time.Time
	if checkpoint, err := m.longHorizon.LatestContinuationCheckpoint(context.Background(), run.ID); err == nil && checkpoint != nil {
		checkpointAt = checkpoint.CreatedAt
	}
	for _, ref := range refs {
		if strings.TrimSpace(ref.Owner.Handle) == "" {
			return true
		}
		snapshot, found := registry.Snapshot(ref.ID, ref.Owner)
		if !found {
			return true
		}
		if queryJobTerminal(snapshot.Status) || snapshot.Status == jobs.JobStatusSubmissionUnknown ||
			snapshot.Status == jobs.JobStatusCancelRequested || snapshot.Status == jobs.JobStatusCancelling ||
			snapshot.MonitorHealth != jobs.MonitorHealthOK || snapshot.PolicyHealth != jobs.PolicyHealthOK {
			return true
		}
		for _, event := range snapshot.WakeEvents {
			// A claimed event newer than the last checkpoint was consumed by a
			// worker that died before checkpointing its response. Replaying that
			// wake is required; older claimed events were already incorporated.
			if !event.Claimed || checkpointAt.IsZero() || event.At.After(checkpointAt) {
				return true
			}
		}
	}
	return false
}

func (m *Manager) rescheduleRunRecoveryProbe(ctx context.Context, run store.Run) error {
	wakeAt := time.Now().UTC().Add(runRecoveryProbeInterval)
	_, changed, err := m.longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID:            run.ID,
		ExpectedRevision: run.Revision,
		ExpectedStatuses: []string{store.RunStatusWaitingExternal},
		Status:           store.RunStatusWaitingExternal,
		WakeAt:           &wakeAt,
	})
	if err != nil {
		return err
	}
	if !changed {
		return store.ErrConflict
	}
	return nil
}

func (m *Manager) launchRecoveredRun(ctx context.Context, run store.Run, checkpoint *store.ContinuationCheckpoint, claimToken string) error {
	if checkpoint == nil {
		return fmt.Errorf("%w: checkpoint is missing", errIncompatibleContinuationSnapshot)
	}
	_, supersedingRunID, err := m.disarmIfSuperseded(ctx, run, checkpoint)
	if err != nil {
		return err
	}
	if supersedingRunID != "" {
		return fmt.Errorf("%w: run %s", errContinuationSuperseded, supersedingRunID)
	}
	executionSnapshot, err := unmarshalContinuationExecutionSnapshot(checkpoint.ExecutionSnapshotJSON, run)
	if err != nil {
		return fmt.Errorf("%w: %v", errIncompatibleContinuationSnapshot, err)
	}
	if err := validateContinuationCheckpointDigests(executionSnapshot, checkpoint); err != nil {
		return fmt.Errorf("%w: %v", errIncompatibleContinuationSnapshot, err)
	}
	thread, err := m.store.GetThread(ctx, run.ThreadID)
	if err != nil {
		return err
	}
	dispatchSnapshot, err := m.providers.SnapshotConnection(
		m.settings.Get(), executionSnapshot.Dispatch.ConnectionID, executionSnapshot.Dispatch.Model,
		executionSnapshot.Dispatch.ConfigFingerprint, executionSnapshot.Dispatch.PlannedAPISurface,
	)
	if err != nil {
		return fmt.Errorf("%w: dispatch target cannot be reconstructed: %v", errIncompatibleContinuationSnapshot, err)
	}
	if strings.TrimSpace(dispatchSnapshot.ConnectionID) != strings.TrimSpace(executionSnapshot.Dispatch.ConnectionID) ||
		strings.TrimSpace(dispatchSnapshot.LegacyProfileID) != strings.TrimSpace(executionSnapshot.Dispatch.LegacyProfileID) ||
		strings.TrimSpace(dispatchSnapshot.ProviderID) != strings.TrimSpace(executionSnapshot.Dispatch.ProviderID) ||
		strings.TrimSpace(dispatchSnapshot.Model) != strings.TrimSpace(executionSnapshot.Dispatch.Model) ||
		strings.TrimSpace(dispatchSnapshot.ConfigFingerprint) != strings.TrimSpace(executionSnapshot.Dispatch.ConfigFingerprint) ||
		strings.TrimSpace(dispatchSnapshot.PlannedAPISurface) != strings.TrimSpace(executionSnapshot.Dispatch.PlannedAPISurface) {
		return fmt.Errorf("%w: reconstructed dispatch identity differs", errIncompatibleContinuationSnapshot)
	}

	m.mu.Lock()
	if active := strings.TrimSpace(m.activeByThread[thread.ID]); active != "" {
		m.mu.Unlock()
		return store.ErrConflict
	}
	state := &runState{threadID: thread.ID, subs: make(map[int]chan SSEEvent), doneCh: make(chan struct{}), durableEvents: true}
	m.runs[run.ID] = state
	m.activeByThread[thread.ID] = run.ID
	m.mu.Unlock()

	attribution := usageAttributionForRun(run, thread.ID, dispatchSnapshot)
	runCtx := providers.WithDispatchSnapshot(usage.WithAttribution(context.Background(), attribution), dispatchSnapshot)
	runCtx, cancel := context.WithCancel(runCtx)
	state.mu.Lock()
	state.cancel = cancel
	state.mu.Unlock()

	userMessageID := latestUserMessageID(checkpoint)
	req := StartRunRequest{ThreadID: thread.ID, Purpose: run.Purpose}
	if strings.EqualFold(strings.TrimSpace(run.Purpose), "search") {
		req.SearchDepth = "deep"
	}
	go m.executeRun(runCtx, thread, run, userMessageID, req, dispatchSnapshot, checkpoint, claimToken, false)
	return nil
}

func latestUserMessageID(checkpoint *store.ContinuationCheckpoint) string {
	if checkpoint == nil {
		return ""
	}
	var history []types.ChatMessage
	if err := jsonUnmarshalCheckpointHistory(checkpoint.HistoryJSON, &history); err != nil {
		return ""
	}
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == types.RoleUser {
			return strings.TrimSpace(history[index].ID)
		}
	}
	return ""
}

// Kept as a narrow seam so recovery tests can exercise malformed checkpoint
// handling without reaching the model provider.
var jsonUnmarshalCheckpointHistory = func(raw []byte, target any) error {
	return json.Unmarshal(raw, target)
}
