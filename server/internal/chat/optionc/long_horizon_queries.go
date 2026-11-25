package optionc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-analyst-local/internal/chat/engine"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

const defaultMaxAutomaticQueryContinuations = 8

const (
	continuationExecutionSnapshotVersion = 1
	runWakeLeaseTTL                      = 2 * time.Minute
	minimumRunWakeLeaseTTL               = 5 * time.Second
	continuationMessageScanPageSize      = 256
	continuationMessageFallbackPages     = 16
	continuationRunScanLimit             = 10000
)

type continuationClaimOutcome string

const (
	continuationClaimAcquired      continuationClaimOutcome = "acquired"
	continuationClaimHeldElsewhere continuationClaimOutcome = "held_elsewhere"
	continuationClaimCapReached    continuationClaimOutcome = "cap_reached"
)

var errContinuationSuperseded = errors.New("continuation was superseded by newer user activity")

type continuationDispatchIdentity struct {
	ConnectionID      string `json:"connection_id"`
	LegacyProfileID   string `json:"legacy_profile_id,omitempty"`
	ProviderID        string `json:"provider_id"`
	Model             string `json:"model"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
	PlannedAPISurface string `json:"planned_api_surface,omitempty"`
}

type continuationToolScope struct {
	Datasources      map[string]bool                `json:"datasources"`
	ChartEcharts     bool                           `json:"chart_echarts"`
	ChartHighcharts  bool                           `json:"chart_highcharts"`
	ChartPipeline    bool                           `json:"chart_pipeline"`
	ChartFilters     bool                           `json:"chart_filters"`
	AtlassianJira    bool                           `json:"atlassian_jira"`
	AtlassianWiki    bool                           `json:"atlassian_confluence"`
	BrowserControl   bool                           `json:"browser_control"`
	SharePoint       bool                           `json:"sharepoint"`
	GitHub           bool                           `json:"github"`
	SlackRead        bool                           `json:"slack_read"`
	SlackWrite       bool                           `json:"slack_write"`
	Arxiv            bool                           `json:"arxiv"`
	ThreadSearch     bool                           `json:"thread_search"`
	LocalComputer    bool                           `json:"local_computer"`
	LocalGrants      []localComputerPermissionGrant `json:"local_grants,omitempty"`
	HostedToolDenied []string                       `json:"hosted_tool_denied,omitempty"`
}

// continuationExecutionSnapshot is the immutable execution envelope required
// to restart after a process crash without consulting mutable settings to
// rebuild model-visible context. Secrets intentionally remain outside this
// payload and are re-resolved only after the dispatch fingerprint is checked.
type continuationExecutionSnapshot struct {
	Version                int                          `json:"version"`
	Digest                 string                       `json:"digest"`
	RunID                  string                       `json:"run_id"`
	ThreadID               string                       `json:"thread_id"`
	Dispatch               continuationDispatchIdentity `json:"dispatch"`
	NextDispatchOrdinal    int                          `json:"next_dispatch_ordinal"`
	HistoryDigest          string                       `json:"history_digest"`
	ProviderStateDigest    string                       `json:"provider_state_digest"`
	SystemPromptBase       string                       `json:"system_prompt_base"`
	SystemPrompt           string                       `json:"system_prompt"`
	ToolDefinitions        []types.ToolDefinition       `json:"tool_definitions"`
	ToolScope              continuationToolScope        `json:"tool_scope"`
	ProjectID              string                       `json:"project_id,omitempty"`
	SearchMode             bool                         `json:"search_mode"`
	SearchDepth            string                       `json:"search_depth"`
	QueryRowLimit          int                          `json:"query_row_limit"`
	ContextPlan            RunContextPlan               `json:"context_plan"`
	MaxContextInputTokens  int                          `json:"max_context_input_tokens"`
	MaxContextHardTokens   int                          `json:"max_context_hard_tokens"`
	TokenCharsPerToken     int                          `json:"token_chars_per_token"`
	EngineInputCap         int                          `json:"engine_input_cap"`
	HardInputCap           int                          `json:"hard_input_cap"`
	RunInputCap            int                          `json:"run_input_cap"`
	EvidenceBudget         int                          `json:"evidence_budget"`
	EvidenceUsed           int                          `json:"evidence_used"`
	RuntimeReference       time.Time                    `json:"runtime_reference"`
	RuntimePlan            *types.RuntimePlanPolicy     `json:"runtime_plan,omitempty"`
	NearCapFallbackApplied bool                         `json:"near_cap_fallback_applied"`
}

func (snapshot continuationExecutionSnapshot) canonicalJSON() ([]byte, error) {
	snapshot.Digest = ""
	return json.Marshal(snapshot)
}

func marshalContinuationExecutionSnapshot(snapshot continuationExecutionSnapshot) (json.RawMessage, error) {
	snapshot.Version = continuationExecutionSnapshotVersion
	canonical, err := snapshot.canonicalJSON()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	snapshot.Digest = fmt.Sprintf("sha256:%x", sum[:])
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(raw), nil
}

func continuationPayloadDigest(raw json.RawMessage) string {
	canonical := []byte(raw)
	var value any
	if json.Unmarshal(raw, &value) == nil {
		if encoded, err := json.Marshal(value); err == nil {
			canonical = encoded
		}
	}
	sum := sha256.Sum256(canonical)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func validateContinuationCheckpointDigests(snapshot *continuationExecutionSnapshot, checkpoint *store.ContinuationCheckpoint) error {
	if snapshot == nil || checkpoint == nil {
		return errors.New("continuation checkpoint or execution snapshot is missing")
	}
	if strings.TrimSpace(snapshot.HistoryDigest) == "" || snapshot.HistoryDigest != continuationPayloadDigest(checkpoint.HistoryJSON) {
		return errors.New("continuation history integrity check failed")
	}
	if strings.TrimSpace(snapshot.ProviderStateDigest) == "" || snapshot.ProviderStateDigest != continuationPayloadDigest(checkpoint.ProviderStateJSON) {
		return errors.New("continuation provider state integrity check failed")
	}
	return nil
}

func unmarshalContinuationExecutionSnapshot(raw json.RawMessage, run store.Run) (*continuationExecutionSnapshot, error) {
	if len(raw) == 0 {
		return nil, errors.New("continuation execution snapshot is missing")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var snapshot continuationExecutionSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode continuation execution snapshot: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("continuation execution snapshot contains trailing data")
	}
	if snapshot.Version != continuationExecutionSnapshotVersion {
		return nil, fmt.Errorf("unsupported continuation execution snapshot version %d", snapshot.Version)
	}
	wantDigest := strings.TrimSpace(snapshot.Digest)
	canonical, err := snapshot.canonicalJSON()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	if wantDigest == "" || wantDigest != fmt.Sprintf("sha256:%x", sum[:]) {
		return nil, errors.New("continuation execution snapshot integrity check failed")
	}
	if strings.TrimSpace(snapshot.RunID) != strings.TrimSpace(run.ID) ||
		strings.TrimSpace(snapshot.ThreadID) != strings.TrimSpace(run.ThreadID) {
		return nil, errors.New("continuation execution snapshot ownership mismatch")
	}
	if snapshot.NextDispatchOrdinal < 0 || snapshot.QueryRowLimit <= 0 ||
		snapshot.MaxContextInputTokens <= 0 || snapshot.MaxContextHardTokens <= 0 ||
		snapshot.TokenCharsPerToken <= 0 || snapshot.EngineInputCap <= 0 ||
		snapshot.HardInputCap <= 0 || snapshot.RunInputCap <= 0 || snapshot.RuntimeReference.IsZero() {
		return nil, errors.New("continuation execution snapshot has invalid execution limits")
	}
	snapshotConnectionID := strings.TrimSpace(snapshot.Dispatch.ConnectionID)
	runConnectionID := strings.TrimSpace(run.ConnectionID)
	connectionMatches := snapshotConnectionID == runConnectionID || (runConnectionID == "" && snapshotConnectionID == "legacy-default")
	if !connectionMatches ||
		strings.TrimSpace(snapshot.Dispatch.ProviderID) != strings.TrimSpace(run.ProviderID) ||
		strings.TrimSpace(snapshot.Dispatch.Model) != strings.TrimSpace(run.Model) {
		return nil, errors.New("continuation execution snapshot dispatch target mismatch")
	}
	if strings.TrimSpace(run.PlanID) != "" && (strings.TrimSpace(snapshot.Dispatch.ConfigFingerprint) == "" ||
		strings.TrimSpace(snapshot.Dispatch.PlannedAPISurface) == "") {
		return nil, errors.New("planned continuation execution snapshot lacks a dispatch fingerprint or API surface")
	}
	if len(snapshot.ToolDefinitions) == 0 || strings.TrimSpace(snapshot.SystemPrompt) == "" {
		return nil, errors.New("continuation execution snapshot is incomplete")
	}
	snapshot.Digest = wantDigest
	return &snapshot, nil
}

func deterministicContinuationCheckpointID(runID string, expectedRevision int64, toolBatchSequence int64, history, providerState, execution json.RawMessage, dependencies []string) string {
	material, _ := json.Marshal(struct {
		RunID            string          `json:"run_id"`
		ExpectedRevision int64           `json:"expected_revision"`
		ToolBatch        int64           `json:"tool_batch"`
		History          json.RawMessage `json:"history"`
		ProviderState    json.RawMessage `json:"provider_state"`
		Execution        json.RawMessage `json:"execution"`
		Dependencies     []string        `json:"dependencies"`
	}{strings.TrimSpace(runID), expectedRevision, toolBatchSequence, history, providerState, execution, append([]string(nil), dependencies...)})
	sum := sha256.Sum256(material)
	return fmt.Sprintf("checkpoint_%x", sum[:])
}

type runWakeLeaseGuard struct {
	repository store.RunContinuationRepository
	runID      string
	ttl        time.Duration
	interval   time.Duration
	cancel     context.CancelFunc

	mu        sync.Mutex
	token     string
	expiresAt time.Time
	validator func(context.Context) (bool, error)
	stopped   bool
	stop      chan struct{}
	renew     chan struct{}
	done      chan struct{}
	lost      atomic.Bool
}

// SetSupersessionValidator installs a durable-generation check for resumed
// model work. The callback returns true only when newer user activity is
// confirmed. It is intentionally separate from warehouse-query ownership.
func (guard *runWakeLeaseGuard) SetSupersessionValidator(validator func(context.Context) (bool, error)) {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	guard.validator = validator
	guard.mu.Unlock()
}

func newRunWakeLeaseGuard(repository store.RunContinuationRepository, runID string, ttl time.Duration, cancel context.CancelFunc) *runWakeLeaseGuard {
	if ttl <= 0 {
		ttl = runWakeLeaseTTL
	}
	interval := ttl / 4
	if interval <= 0 {
		interval = time.Millisecond
	}
	// Sub-second leases are too short to distinguish ownership loss from an
	// ordinary scheduler/GC/store stall. Preserve the requested (early)
	// heartbeat cadence, but renew a production-safe minimum durable window.
	if ttl < minimumRunWakeLeaseTTL {
		ttl = minimumRunWakeLeaseTTL
	}
	guard := &runWakeLeaseGuard{
		repository: repository,
		runID:      strings.TrimSpace(runID),
		ttl:        ttl,
		interval:   interval,
		cancel:     cancel,
		stop:       make(chan struct{}),
		renew:      make(chan struct{}, 1),
		done:       make(chan struct{}),
	}
	go guard.loop()
	return guard
}

func (guard *runWakeLeaseGuard) Hold(token string, expiresAt *time.Time) {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	guard.token = strings.TrimSpace(token)
	guard.expiresAt = time.Now().UTC().Add(guard.ttl)
	if expiresAt != nil && expiresAt.After(time.Now().UTC()) {
		guard.expiresAt = expiresAt.UTC()
	}
	guard.lost.Store(false)
	guard.mu.Unlock()
	// Establish a full effective lease immediately instead of spending the
	// first interval on the shorter/near-expiry claim inherited by Hold.
	select {
	case guard.renew <- struct{}{}:
	default:
	}
}

func (guard *runWakeLeaseGuard) Clear(token string) {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	if token == "" || guard.token == strings.TrimSpace(token) {
		guard.token = ""
		guard.expiresAt = time.Time{}
	}
	guard.mu.Unlock()
}

func (guard *runWakeLeaseGuard) Lost() bool {
	return guard != nil && guard.lost.Load()
}

func (guard *runWakeLeaseGuard) Stop() {
	if guard == nil {
		return
	}
	guard.mu.Lock()
	if !guard.stopped {
		guard.stopped = true
		close(guard.stop)
	}
	guard.mu.Unlock()
	<-guard.done
}

func (guard *runWakeLeaseGuard) loop() {
	defer close(guard.done)
	// Renew with three quarters of the lease still available. Besides giving
	// transient store latency another attempt before expiry, this avoids a
	// single delayed scheduler tick consuming the entire safety margin.
	ticker := time.NewTicker(guard.interval)
	defer ticker.Stop()
	for {
		select {
		case <-guard.stop:
			return
		case <-guard.renew:
			guard.heartbeat()
		case <-ticker.C:
			guard.heartbeat()
		}
	}
}

func (guard *runWakeLeaseGuard) heartbeat() {
	guard.mu.Lock()
	token, expiresAt, validator, stopped := guard.token, guard.expiresAt, guard.validator, guard.stopped
	guard.mu.Unlock()
	if stopped || token == "" || guard.repository == nil {
		return
	}
	heartbeatTimeout := guard.ttl / 3
	if heartbeatTimeout <= 0 {
		heartbeatTimeout = time.Millisecond
	}
	if validator != nil {
		validationCtx, validationCancel := context.WithTimeout(context.Background(), heartbeatTimeout)
		superseded, validationErr := validator(validationCtx)
		validationCancel()
		if superseded {
			guard.markLost(token)
			return
		}
		if validationErr != nil {
			// Do not extend a lease while its generation cannot be validated.
			// Retry transient failures until the last confirmed lease expires,
			// matching renewal-error tolerance without allowing indefinite stale
			// ownership through successful blind renewals.
			if time.Now().UTC().Before(expiresAt) {
				return
			}
			guard.markLost(token)
			return
		}
	}
	renewalCtx, renewalCancel := context.WithTimeout(context.Background(), heartbeatTimeout)
	renewed, err := guard.repository.RenewRunWakeLease(renewalCtx, guard.runID, token, guard.ttl)
	renewalCancel()
	if renewed {
		guard.mu.Lock()
		if !guard.stopped && guard.token == token {
			guard.expiresAt = time.Now().UTC().Add(guard.ttl)
		}
		guard.mu.Unlock()
		return
	}
	if err != nil && time.Now().UTC().Before(expiresAt) {
		return
	}
	guard.markLost(token)
}

func (guard *runWakeLeaseGuard) markLost(token string) {
	guard.mu.Lock()
	// Stop is a clean ownership handoff boundary, not lease loss. A ticker
	// selected concurrently with stop—or an in-flight renewal completing
	// afterward—must never cancel execution or report a stale-driver loss.
	if guard.stopped || guard.token != token {
		guard.mu.Unlock()
		return
	}
	guard.token = ""
	guard.lost.Store(true)
	executionCancel := guard.cancel
	guard.mu.Unlock()
	if executionCancel != nil {
		executionCancel()
	}
}

type queryDependencyRef struct {
	ID              string
	Owner           jobs.JobOwner
	Status          jobs.JobStatus
	ValidationState string
	ObligationID    string
}

// queryContinuationIdentityNotice reintroduces the current durable capability
// after an exact checkpoint replay. Checkpoint history is intentionally opaque
// and may still contain a legacy concrete id that was atomically repaired while
// the process was down; this authoritative system turn lets the model address
// the healthy remote job without rewriting provider-native history.
func queryContinuationIdentityNotice(refs []queryDependencyRef) string {
	type queryCapability struct {
		JobID             string `json:"job_id"`
		OwnerToken        string `json:"owner_token"`
		QueryObligationID string `json:"query_obligation_id,omitempty"`
		Status            string `json:"status,omitempty"`
		ValidationState   string `json:"validation_state,omitempty"`
	}
	capabilities := make([]queryCapability, 0, len(refs))
	for _, ref := range refs {
		jobID := strings.TrimSpace(ref.ID)
		ownerToken := strings.TrimSpace(ref.Owner.Handle)
		if jobID == "" || ownerToken == "" || jobs.IsQueryJobIDSentinel(jobID) {
			continue
		}
		obligationID := strings.TrimSpace(ref.ObligationID)
		if store.ValidateQueryObligationID(obligationID) != nil {
			obligationID = ""
		}
		capabilities = append(capabilities, queryCapability{
			JobID: jobID, OwnerToken: ownerToken, QueryObligationID: obligationID,
			Status: strings.TrimSpace(string(ref.Status)), ValidationState: strings.TrimSpace(ref.ValidationState),
		})
	}
	if len(capabilities) == 0 {
		return ""
	}
	payload, err := json.Marshal(capabilities)
	if err != nil {
		return ""
	}
	return "[Orchestrator durable query identity refresh] These are the authoritative current query capabilities for this run. " +
		"Ignore stale concrete job IDs in earlier checkpoint history. Use job_id with owner_token for status/results; " +
		"use query_obligation_id for a corrected replacement when present. Keep the task unfinished while required work remains.\n" + string(payload)
}

func (m *Manager) maxAutomaticQueryContinuations() int {
	if m == nil || m.settings == nil {
		return defaultMaxAutomaticQueryContinuations
	}
	settings := m.settings.Get()
	if settings.DatabaseQueryPolicy == nil || settings.DatabaseQueryPolicy.MaxAutoContinuations <= 0 {
		return defaultMaxAutomaticQueryContinuations
	}
	return settings.DatabaseQueryPolicy.MaxAutoContinuations
}

func (m *Manager) queryDependenciesForRun(threadID, runID string) []queryDependencyRef {
	threadID = strings.TrimSpace(threadID)
	runID = strings.TrimSpace(runID)
	if m == nil || threadID == "" || runID == "" {
		return nil
	}
	m.recoverPersistedQueryProtocolFault(threadID, runID)
	if m.longHorizon != nil {
		dependencies, err := m.longHorizon.ListRunQueryDependencies(context.Background(), runID)
		if err != nil {
			// Fail closed. An unreadable durable barrier is recovery work, not
			// evidence that the run has no outstanding query dependency.
			return []queryDependencyRef{{
				ID:              "dependency-store:" + runID,
				Owner:           jobs.JobOwner{ThreadID: threadID},
				ValidationState: store.QueryValidationUnvalidated,
			}}
		}
		records, recordsErr := m.longHorizon.ListQueryJobsByThread(context.Background(), threadID)
		if recordsErr != nil {
			return []queryDependencyRef{{
				ID:              "dependency-job-store:" + runID,
				Owner:           jobs.JobOwner{ThreadID: threadID},
				ValidationState: store.QueryValidationUnvalidated,
			}}
		}
		var rootsRepaired bool
		dependencies, rootsRepaired = m.repairUnregisteredQueryRoots(context.Background(), runID, dependencies, records)
		dependencies = m.repairHalfCommittedQueryReplacements(context.Background(), runID, dependencies, records)
		dependencies = m.reconcileQueryDependencyValidation(context.Background(), runID, dependencies, records)
		if rootsRepaired {
			// The initial protocol scan ran before the crash-window repair and may
			// have classified the stamped job/qob as unrepresented. Re-evaluate the
			// same persisted action against the now-authoritative dependency.
			m.rearmQueryProtocolFaultRecovery(runID)
			m.recoverPersistedQueryProtocolFault(threadID, runID)
		}
		// Clear only job-scoped write faults whose exact durable job/dependency is
		// now authoritative. An unrelated successful list or query must never
		// erase a different failed write or an unresolved protocol preflight.
		representedFaultJobs := make(map[string]struct{}, len(dependencies)+len(records))
		for _, dependency := range dependencies {
			id := strings.TrimSpace(dependency.JobID)
			if id == "" || !dependency.RequiredForCompletion || dependency.Detached ||
				strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) {
				continue
			}
			representedFaultJobs[id] = struct{}{}
		}
		for _, record := range records {
			id := strings.TrimSpace(record.ID)
			if id == "" || strings.TrimSpace(record.Owner.RunID) != runID ||
				strings.EqualFold(string(record.ValidationState), store.QueryValidationValidated) {
				continue
			}
			if record.CancelOutcome == jobs.CancelOutcomeBeforeSubmit && !record.SubmitDispatched &&
				record.Handle.IsZero() && strings.TrimSpace(record.ReplacesJobID) != "" {
				continue
			}
			representedFaultJobs[id] = struct{}{}
		}
		for id := range representedFaultJobs {
			m.setQueryDependencyFaultFor(runID, "job:"+id, nil)
		}
		refs := make([]queryDependencyRef, 0, len(dependencies)+len(records))
		seen := make(map[string]struct{}, len(dependencies))
		registered := make(map[string]struct{}, len(dependencies))
		for _, dependency := range dependencies {
			id := strings.TrimSpace(dependency.JobID)
			if id == "" {
				continue
			}
			registered[id] = struct{}{}
			if dependency.Detached || !dependency.RequiredForCompletion ||
				strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ref := queryDependencyRef{
				ID: id, ValidationState: strings.TrimSpace(dependency.ValidationState),
				ObligationID: strings.TrimSpace(dependency.ObligationID),
			}
			record, ok, getErr := m.longHorizon.GetQueryJob(context.Background(), id)
			if getErr == nil && ok && strings.TrimSpace(record.Owner.ThreadID) == threadID {
				ref.Owner = record.Owner
				ref.Status = record.Status
			} else {
				// Preserve the id with an intentionally incomplete owner. Waiting
				// maps this to recovery_required rather than silently completing.
				ref.Owner = jobs.JobOwner{ThreadID: threadID}
			}
			refs = append(refs, ref)
		}
		// Reconcile the durable job ledger as a fail-closed backstop. A remote
		// query can be submitted successfully before dependency registration
		// fails; after restart the in-memory fault marker is gone, but the job's
		// owner RunID remains durable. Any such unregistered, unvalidated job is
		// still a required completion obligation. Explicitly detached dependency
		// rows remain authoritative for correctly linked replacements.
		for _, record := range records {
			id := strings.TrimSpace(record.ID)
			if id == "" || strings.TrimSpace(record.Owner.RunID) != runID ||
				strings.EqualFold(string(record.ValidationState), store.QueryValidationValidated) {
				continue
			}
			if record.CancelOutcome == jobs.CancelOutcomeBeforeSubmit && !record.SubmitDispatched &&
				record.Handle.IsZero() && strings.TrimSpace(record.ReplacesJobID) != "" {
				// A concurrent replacement loser was deliberately cancelled by the
				// pre-submit obligation binder. It is audit evidence, not work owed
				// by the run, and must never be resurrected by the backstop.
				continue
			}
			if _, ok := registered[id]; ok {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			obligationID, _ := store.DeriveQueryObligationID(context.Background(), m.longHorizon, runID, id)
			refs = append(refs, queryDependencyRef{
				ID:              id,
				Owner:           record.Owner,
				Status:          record.Status,
				ValidationState: string(record.ValidationState),
				ObligationID:    obligationID,
			})
		}
		refs = append(refs, m.queryDependencyFaultRefs(threadID, runID)...)
		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
		return refs
	}
	if m.queryJobs == nil {
		refs := m.queryDependencyFaultRefs(threadID, runID)
		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
		return refs
	}
	registry, ok := m.queryJobs.(*jobs.Registry)
	if !ok || registry == nil {
		refs := m.queryDependencyFaultRefs(threadID, runID)
		sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
		return refs
	}
	active := registry.ListActiveByThread(threadID)
	refs := make([]queryDependencyRef, 0, len(active))
	seen := make(map[string]struct{}, len(active))
	for _, job := range active {
		if job == nil || strings.TrimSpace(job.Owner.RunID) != runID {
			continue
		}
		id := strings.TrimSpace(job.ID)
		ownerToken := strings.TrimSpace(job.Owner.Handle)
		if id == "" || ownerToken == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		refs = append(refs, queryDependencyRef{
			ID: id,
			Owner: jobs.JobOwner{
				ThreadID: threadID,
				RunID:    runID,
				Handle:   ownerToken,
			},
			Status:       job.Status,
			ObligationID: firstStringMapValue(job.Metadata, "query_obligation_id"),
		})
	}
	refs = append(refs, m.queryDependencyFaultRefs(threadID, runID)...)
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs
}

// reconcileQueryDependencyValidation closes the validation crash window: the
// job record is the authoritative validation write, while dependency state is
// a denormalized completion index. If the process dies between those writes,
// repair the index before deciding whether the run must remain suspended.
func (m *Manager) reconcileQueryDependencyValidation(
	ctx context.Context,
	runID string,
	dependencies []store.RunQueryDependency,
	records []jobs.JobRecord,
) []store.RunQueryDependency {
	if m == nil || m.longHorizon == nil {
		return dependencies
	}
	_ = records // The direct per-job read below closes validation-write races.
	out := append([]store.RunQueryDependency(nil), dependencies...)
	for index, dependency := range out {
		jobID := strings.TrimSpace(dependency.JobID)
		if jobID == "" || dependency.Detached || !dependency.RequiredForCompletion {
			continue
		}
		candidate := dependency
		reconciled := false
		var reconcileErr error
		for attempt := 0; attempt < 3; attempt++ {
			record, found, readErr := m.longHorizon.GetQueryJob(ctx, jobID)
			if readErr != nil {
				reconcileErr = readErr
				break
			}
			desiredState := store.QueryValidationUnvalidated
			if found {
				desiredState = observedQueryDependencyValidationState(record)
			}
			if desiredState == store.QueryValidationUnvalidated &&
				strings.EqualFold(strings.TrimSpace(candidate.ValidationState), store.QueryValidationRejected) {
				// Rejected is also the run-local "replacement required" state
				// established before wrong/superseded cancellation. A still-
				// unvalidated job record must not erase that semantic rejection.
				desiredState = store.QueryValidationRejected
			}
			if !strings.EqualFold(strings.TrimSpace(candidate.ValidationState), desiredState) {
				candidate.ValidationState = desiredState
				candidate.UpdatedAt = time.Now().UTC()
				persisted, persistErr := m.longHorizon.UpsertRunQueryDependency(ctx, candidate)
				reconcileErr = persistErr
				if reconcileErr != nil {
					break
				}
				candidate = persisted
			}

			// Validation can change between the read and dependency write. Verify
			// the authoritative job axis again before allowing a validated row to
			// disappear from the completion barrier.
			latest, latestFound, latestErr := m.longHorizon.GetQueryJob(ctx, jobID)
			if latestErr != nil {
				reconcileErr = latestErr
				break
			}
			latestState := store.QueryValidationUnvalidated
			if latestFound {
				latestState = observedQueryDependencyValidationState(latest)
			}
			if latestState == store.QueryValidationUnvalidated &&
				strings.EqualFold(strings.TrimSpace(candidate.ValidationState), store.QueryValidationRejected) {
				latestState = store.QueryValidationRejected
			}
			if strings.EqualFold(strings.TrimSpace(candidate.ValidationState), latestState) {
				reconciled = true
				break
			}
			reconcileErr = errors.New("query validation changed during dependency reconciliation")
		}
		if !reconciled {
			// Even if the durable row is stale-validated and the write failed,
			// this scan must remain fail-closed. A subsequent scan retries the
			// durable repair; the current one still returns a blocking ref.
			blocked := candidate
			if strings.EqualFold(strings.TrimSpace(blocked.ValidationState), store.QueryValidationValidated) {
				blocked.ValidationState = store.QueryValidationUnvalidated
			}
			out[index] = blocked
			m.setQueryDependencyFaultFor(runID, "job:"+jobID, firstNonNilError(reconcileErr, errors.New("query validation could not be reconciled")))
			continue
		}
		out[index] = candidate
		m.setQueryDependencyFaultFor(runID, "job:"+jobID, nil)
	}
	return out
}

// recoverPersistedQueryObservations makes cross-run lifecycle handoff
// restart-safe. It authenticates persisted status/results actions using the
// exact executed tool_args and adopts every durable same-thread job they
// proved. Actions are reconciled independently: only a newer authenticated
// action for the same job and lifecycle kind may supersede an older fault.
// The bool result requests another scan after a transient persistence error.
func (m *Manager) recoverPersistedQueryObservations(threadID, runID string) bool {
	if m == nil || m.store == nil || m.longHorizon == nil {
		return false
	}

	const pageSize = 256
	var beforeSeq *int64
	reconciliationFailed := false
	scanComplete := false
	seenActions := make(map[string]struct{})
	seenCallKeys := make(map[string]string)

	for {
		messages, err := m.store.ListMessages(context.Background(), store.ListMessagesParams{
			ThreadID: threadID, Limit: pageSize, BeforeSeq: beforeSeq,
			IncludeHidden: true, OrderBy: "seq_desc",
		})
		if err != nil {
			m.setQueryDependencyFaultFor(runID, "protocol:history-read", fmt.Errorf("read durable query observation history: %w", err))
			m.rearmQueryProtocolFaultRecovery(runID)
			return true
		}
		for _, message := range messages {
			if message.RunID == nil || strings.TrimSpace(*message.RunID) != strings.TrimSpace(runID) || message.Role != types.RoleTool {
				continue
			}
			toolName := ""
			if message.Name != nil {
				toolName = strings.TrimSpace(*message.Name)
			}
			var envelope map[string]any
			decodeErr := error(nil)
			if message.Content == nil {
				decodeErr = errors.New("persisted query lifecycle result is missing")
			} else {
				decodeErr = json.Unmarshal([]byte(*message.Content), &envelope)
			}
			if toolName == "" {
				toolName = firstStringMapValue(envelope, "tool_name")
			}
			if tools.IsAsyncQueryRunTool(toolName) {
				continue
			}
			call := persistedQueryToolCall(message, toolName, envelope)
			_, guardedCancellation := guardedQueryCancellationReason(call)
			if !isAsyncQueryStatusTool(toolName) && !isAsyncQueryResultsTool(toolName) && !isAsyncQueryValidationTool(toolName) && !guardedCancellation {
				continue
			}
			faultKey := queryDependencyProtocolFaultKey(call)
			if persistedQueryToolCallIDConflict(message, envelope) {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query lifecycle envelope and message disagree on tool-call identity"))
				continue
			}
			callFingerprint := persistedQueryToolCallFingerprint(call)
			if newerFingerprint, replay := seenCallKeys[faultKey]; replay {
				if newerFingerprint != callFingerprint {
					m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query lifecycle actions reuse a tool-call identity with different executed arguments"))
				}
				continue
			}
			seenCallKeys[faultKey] = callFingerprint
			if decodeErr != nil {
				m.setQueryDependencyFaultFor(runID, faultKey, fmt.Errorf("decode persisted query lifecycle result: %w", decodeErr))
				continue
			}

			requestedJobID, requestedOwnerToken, requestedOK := executedQueryLifecycleIdentity(call)
			if !requestedOK {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query lifecycle action has no exact managed job identity and owner capability"))
				continue
			}
			actionKind := "status"
			if guardedCancellation {
				actionKind = "guarded-cancellation"
			} else if isAsyncQueryValidationTool(toolName) {
				actionKind = "validation"
			} else if isAsyncQueryResultsTool(toolName) {
				actionKind = "results"
			}
			outputOK, _ := envelope["ok"].(bool)
			out := tools.Output{
				OK: outputOK, Result: envelope["result"],
				QueryMetadata:  anyMap(envelope["query_metadata"]),
				ResultMetadata: anyMap(envelope["result_metadata"]),
			}
			identityMismatch := queryOutputFlag(anyMap(out.Result), "query_observation_identity_mismatch") ||
				queryOutputFlag(out.QueryMetadata, "query_observation_identity_mismatch")
			approvalStatus := strings.ToLower(strings.TrimSpace(firstStringMapValue(anyMap(out.Result), "status")))
			cancellationWasNotDispatched := strings.HasPrefix(approvalStatus, "approval_")

			record, found, getErr := m.longHorizon.GetQueryJob(context.Background(), requestedJobID)
			if getErr != nil {
				m.setQueryDependencyFaultFor(runID, faultKey, fmt.Errorf("read observed durable query: %w", getErr))
				reconciliationFailed = true
				continue
			}
			if !found || strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(threadID) ||
				strings.TrimSpace(record.Owner.Handle) == "" || strings.TrimSpace(record.Owner.Handle) != requestedOwnerToken {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query lifecycle owner capability did not verify"))
				continue
			}
			obligationID, deriveErr := store.DeriveQueryObligationID(context.Background(), m.longHorizon, runID, requestedJobID)
			if deriveErr != nil || store.ValidateQueryObligationID(obligationID) != nil {
				m.setQueryDependencyFaultFor(runID, faultKey, firstNonNilError(deriveErr, errors.New("persisted query lifecycle lineage has an invalid obligation identity")))
				reconciliationFailed = true
				continue
			}
			actionKey := obligationID + "\x00" + actionKind
			if _, newerActionSeen := seenActions[actionKey]; newerActionSeen {
				// Messages are read newest-first. Only a newer action in the same
				// authenticated qob lineage and lifecycle kind owns this verdict.
				m.clearQueryDependencyFaultFor(runID, faultKey)
				continue
			}
			seenActions[actionKey] = struct{}{}
			returnedJobID, conflictingReturnedJobIDs := selectAsyncQueryJobID(out)
			if conflictingReturnedJobIDs || (returnedJobID != "" && returnedJobID != requestedJobID) ||
				(outputOK && returnedJobID != requestedJobID) {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query lifecycle response does not match its executed job identity"))
				continue
			}

			var ensureErr error
			for attempt := 0; attempt < 3; attempt++ {
				var obligationID string
				var needsBarrier bool
				obligationID, needsBarrier, ensureErr = m.ensureObservedQueryDependency(context.Background(), threadID, runID, record)
				if ensureErr == nil && guardedCancellation && needsBarrier && !cancellationWasNotDispatched {
					_, ensureErr = m.rejectObservedQueryAttempt(context.Background(), runID, requestedJobID, obligationID)
				}
				if ensureErr == nil {
					break
				}
			}
			if ensureErr != nil {
				m.setQueryDependencyFaultFor(runID, faultKey, ensureErr)
				reconciliationFailed = true
				continue
			}
			if identityMismatch {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query lifecycle response reported an identity mismatch"))
				continue
			}
			m.clearQueryDependencyFaultFor(runID, faultKey)
		}
		if len(messages) < pageSize {
			scanComplete = true
			break
		}
		lastSeq := messages[len(messages)-1].Seq
		if beforeSeq != nil && lastSeq >= *beforeSeq {
			m.setQueryDependencyFaultFor(runID, "protocol:history-scan-incomplete", errors.New("persisted query lifecycle history pagination made no forward progress"))
			return true
		}
		beforeSeq = &lastSeq
	}

	if !scanComplete {
		m.setQueryDependencyFaultFor(runID, "protocol:history-scan-incomplete", errors.New("persisted query lifecycle history scan did not complete"))
		return true
	}
	m.clearQueryDependencyFaultFor(runID, "protocol:history-scan-incomplete")
	return reconciliationFailed
}

// persistedQueryToolCall reconstructs the same per-call protocol identity used
// by live execution. ToolCallID is durable in both the envelope and message;
// message identity is the final fallback for legacy rows.
func persistedQueryToolCall(message store.Message, toolName string, envelope map[string]any) types.ToolCall {
	callID := ""
	if message.ToolCallID != nil {
		callID = strings.TrimSpace(*message.ToolCallID)
	}
	if callID == "" {
		callID = firstStringMapValue(envelope, "tool_call_id")
	}
	if callID == "" {
		callID = strings.TrimSpace(message.ID)
	}
	if callID == "" {
		callID = fmt.Sprintf("persisted-query-message-%d", message.Seq)
	}
	return types.ToolCall{ID: callID, Name: strings.TrimSpace(toolName), Args: anyMap(envelope["tool_args"])}
}

func persistedQueryToolCallIDConflict(message store.Message, envelope map[string]any) bool {
	envelopeID := firstStringMapValue(envelope, "tool_call_id")
	messageID := ""
	if message.ToolCallID != nil {
		messageID = strings.TrimSpace(*message.ToolCallID)
	}
	return envelopeID != "" && messageID != "" && envelopeID != messageID
}

func persistedQueryToolCallFingerprint(call types.ToolCall) string {
	material := map[string]any{
		"tool_name": strings.ToLower(strings.TrimSpace(call.Name)),
		"tool_args": call.Args,
	}
	encoded, err := json.Marshal(material)
	if err != nil {
		encoded = []byte(strings.ToLower(strings.TrimSpace(call.Name)))
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("%x", digest[:12])
}

// repairUnregisteredQueryRoots closes the record/dependency crash window. A
// managed launch (or deterministic failed-attempt reservation) can durably
// create its current-run root before dependency insertion fails. The root is
// still owed work and has a deterministic qob, so bind it idempotently before
// the ledger/backstop clears any job-scoped persistence fault.
func (m *Manager) repairUnregisteredQueryRoots(
	ctx context.Context,
	runID string,
	dependencies []store.RunQueryDependency,
	records []jobs.JobRecord,
) ([]store.RunQueryDependency, bool) {
	if m == nil || m.longHorizon == nil {
		return dependencies, false
	}
	registered := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		if id := strings.TrimSpace(dependency.JobID); id != "" {
			registered[id] = struct{}{}
		}
	}
	repaired := false
	for _, record := range records {
		jobID := strings.TrimSpace(record.ID)
		if !validManagedQueryJobID(jobID) || strings.TrimSpace(record.Owner.RunID) != strings.TrimSpace(runID) ||
			strings.TrimSpace(record.ReplacesJobID) != "" ||
			strings.EqualFold(string(record.ValidationState), store.QueryValidationValidated) {
			continue
		}
		if _, exists := registered[jobID]; exists {
			continue
		}
		if record.CancelOutcome == jobs.CancelOutcomeBeforeSubmit && !record.SubmitDispatched && record.Handle.IsZero() {
			continue
		}
		if err := m.longHorizon.BindQueryObligation(ctx, jobID); err != nil {
			m.setQueryDependencyFaultFor(runID, "job:"+jobID, err)
			continue
		}
		repaired = true
		registered[jobID] = struct{}{}
	}
	if !repaired {
		return dependencies, false
	}
	refreshed, err := m.longHorizon.ListRunQueryDependencies(ctx, runID)
	if err != nil {
		return dependencies, false
	}
	return refreshed, true
}

func (m *Manager) setQueryDependencyFaultFor(runID, key string, err error) {
	if m == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	key = strings.TrimSpace(key)
	if runID == "" || key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.HasPrefix(key, "protocol:") {
		if m.queryProtocolFaultChecked == nil {
			m.queryProtocolFaultChecked = make(map[string]bool)
		}
		m.queryProtocolFaultChecked[runID] = true
	}
	if err == nil {
		if faults := m.queryDependencyFaults[runID]; faults != nil {
			delete(faults, key)
			if len(faults) == 0 {
				delete(m.queryDependencyFaults, runID)
			}
		}
	} else {
		if m.queryDependencyFaults == nil {
			m.queryDependencyFaults = make(map[string]map[string]string)
		}
		if m.queryDependencyFaults[runID] == nil {
			m.queryDependencyFaults[runID] = make(map[string]string)
		}
		m.queryDependencyFaults[runID][key] = err.Error()
	}
}

func (m *Manager) clearQueryDependencyFaultFor(runID, key string) {
	if m == nil {
		return
	}
	runID = strings.TrimSpace(runID)
	key = strings.TrimSpace(key)
	if runID == "" || key == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if faults := m.queryDependencyFaults[runID]; faults != nil {
		delete(faults, key)
		if len(faults) == 0 {
			delete(m.queryDependencyFaults, runID)
		}
	}
}

func (m *Manager) queryDependencyFaultRefs(threadID, runID string) []queryDependencyRef {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	faults := make(map[string]string, len(m.queryDependencyFaults[runID]))
	for key, message := range m.queryDependencyFaults[runID] {
		faults[key] = message
	}
	m.mu.Unlock()
	refs := make([]queryDependencyRef, 0, len(faults))
	for key := range faults {
		digest := sha256.Sum256([]byte(strings.TrimSpace(runID) + "\x00" + key))
		refs = append(refs, queryDependencyRef{
			ID:              fmt.Sprintf("dependency-fault-%x", digest[:12]),
			Owner:           jobs.JobOwner{ThreadID: strings.TrimSpace(threadID), RunID: strings.TrimSpace(runID)},
			ValidationState: store.QueryValidationUnvalidated,
		})
	}
	return refs
}

// recoverPersistedQueryProtocolFault makes every query action restart-safe
// without manufacturing fake warehouse work. Recovery is per exact tool call,
// with one safe semantic supersession rule: a newer action may replace an older
// verdict only when both carry the same verified query obligation. An unrelated
// successful query can therefore never erase unfinished work.
func (m *Manager) recoverPersistedQueryProtocolFault(threadID, runID string) {
	if m == nil || m.store == nil {
		return
	}
	m.mu.Lock()
	if m.queryProtocolFaultChecked == nil {
		m.queryProtocolFaultChecked = make(map[string]bool)
	}
	if m.queryProtocolFaultChecked[runID] {
		m.mu.Unlock()
		return
	}
	m.queryProtocolFaultChecked[runID] = true
	m.mu.Unlock()
	observationRetry := m.recoverPersistedQueryObservations(threadID, runID)

	const pageSize = 256
	var beforeSeq *int64
	scanComplete := false
	seenCallKeys := make(map[string]string)
	seenObligations := make(map[string]struct{})
	for {
		messages, err := m.store.ListMessages(context.Background(), store.ListMessagesParams{
			ThreadID: threadID, Limit: pageSize, BeforeSeq: beforeSeq,
			IncludeHidden: true, OrderBy: "seq_desc",
		})
		if err != nil {
			m.setQueryDependencyFaultFor(runID, "protocol:history-read", fmt.Errorf("read durable query protocol history: %w", err))
			m.rearmQueryProtocolFaultRecovery(runID)
			return
		}
		for _, message := range messages {
			if message.RunID == nil || strings.TrimSpace(*message.RunID) != runID || message.Role != types.RoleTool {
				continue
			}
			toolName := ""
			if message.Name != nil {
				toolName = strings.TrimSpace(*message.Name)
			}
			var envelope map[string]any
			decodeErr := error(nil)
			if message.Content == nil {
				decodeErr = errors.New("persisted query action result is missing")
			} else {
				decodeErr = json.Unmarshal([]byte(*message.Content), &envelope)
			}
			if toolName == "" {
				toolName = firstStringMapValue(envelope, "tool_name")
			}
			if !tools.IsAsyncQueryRunTool(toolName) {
				continue
			}
			call := persistedQueryToolCall(message, toolName, envelope)
			faultKey := queryDependencyProtocolFaultKey(call)
			if persistedQueryToolCallIDConflict(message, envelope) {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query envelope and message disagree on tool-call identity"))
				continue
			}
			callFingerprint := persistedQueryToolCallFingerprint(call)
			if newerFingerprint, replay := seenCallKeys[faultKey]; replay {
				if newerFingerprint != callFingerprint {
					m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query actions reuse a tool-call identity with different executed arguments"))
				}
				continue
			}
			seenCallKeys[faultKey] = callFingerprint
			if decodeErr != nil {
				m.setQueryDependencyFaultFor(runID, faultKey, fmt.Errorf("decode persisted query action result: %w", decodeErr))
				continue
			}
			result := anyMap(envelope["result"])
			queryMetadata := anyMap(envelope["query_metadata"])
			preflightFailed := queryOutputFlag(result, "replacement_preflight_failed") ||
				queryOutputFlag(queryMetadata, "replacement_preflight_failed")
			waitRequired := queryOutputFlag(result, "replacement_wait_required") ||
				queryOutputFlag(queryMetadata, "replacement_wait_required")
			jobID, conflictingJobIDs := selectAsyncQueryJobID(tools.Output{Result: result, QueryMetadata: queryMetadata})
			obligationID := firstStringMapValue(result, "query_obligation_id")
			if obligationID == "" {
				obligationID = firstStringMapValue(queryMetadata, "query_obligation_id")
			}
			if store.ValidateQueryObligationID(obligationID) == nil {
				if _, newerActionSeen := seenObligations[obligationID]; newerActionSeen {
					m.clearQueryDependencyFaultFor(runID, faultKey)
					continue
				}
				seenObligations[obligationID] = struct{}{}
			}
			if conflictingJobIDs {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query action contains conflicting durable job identities"))
				continue
			}
			identityClaimed := jobID != "" || strings.TrimSpace(obligationID) != ""
			if identityClaimed {
				represented, identityErr := m.persistedQueryActionIdentityIsDurable(runID, jobID, obligationID)
				if identityErr != nil {
					m.setQueryDependencyFaultFor(runID, faultKey, fmt.Errorf("verify durable query action identity: %w", identityErr))
					observationRetry = true
					continue
				}
				if !represented {
					// A claimed id without durable evidence is a healable protocol
					// failure, not a job-scoped dependency. Otherwise a bogus/nil/zero
					// adapter result poisons the restarted run forever because no record
					// can ever reconcile job:<bogus>.
					m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query action identity has no durable current-run evidence"))
				} else {
					m.clearQueryDependencyFaultFor(runID, faultKey)
				}
				continue
			}
			representedByCall, callIdentityErr := m.persistedQueryToolCallIsDurable(runID, call.ID)
			if callIdentityErr != nil {
				m.setQueryDependencyFaultFor(runID, faultKey, fmt.Errorf("verify durable query tool-call identity: %w", callIdentityErr))
				observationRetry = true
				continue
			}
			if representedByCall {
				m.clearQueryDependencyFaultFor(runID, faultKey)
				continue
			}
			outputOK, okPresent := envelope["ok"].(bool)
			materializedDataset := false
			for _, key := range []string{"rows", "data", "results"} {
				if _, exists := result[key]; exists {
					materializedDataset = true
					break
				}
			}
			unresolvedFailure := jobID == "" && strings.TrimSpace(obligationID) == "" &&
				(!okPresent || !outputOK || preflightFailed || waitRequired || !materializedDataset)
			if unresolvedFailure {
				m.setQueryDependencyFaultFor(runID, faultKey, errors.New("persisted query retry target remains unverified"))
			} else {
				m.clearQueryDependencyFaultFor(runID, faultKey)
			}
		}
		if len(messages) < pageSize {
			scanComplete = true
			break
		}
		lastSeq := messages[len(messages)-1].Seq
		if beforeSeq != nil && lastSeq >= *beforeSeq {
			m.setQueryDependencyFaultFor(runID, "protocol:history-scan-incomplete", errors.New("persisted query action history pagination made no forward progress"))
			m.rearmQueryProtocolFaultRecovery(runID)
			return
		}
		beforeSeq = &lastSeq
	}
	if !scanComplete {
		m.setQueryDependencyFaultFor(runID, "protocol:history-scan-incomplete", errors.New("persisted query action history scan did not complete"))
		m.rearmQueryProtocolFaultRecovery(runID)
		return
	}
	m.clearQueryDependencyFaultFor(runID, "protocol:history-scan-incomplete")
	if !observationRetry {
		m.clearQueryDependencyFaultFor(runID, "protocol:history-read")
		return
	}
	m.rearmQueryProtocolFaultRecovery(runID)
}

func (m *Manager) rearmQueryProtocolFaultRecovery(runID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.queryProtocolFaultChecked != nil {
		m.queryProtocolFaultChecked[strings.TrimSpace(runID)] = false
	}
	m.mu.Unlock()
}

func (m *Manager) persistedQueryActionIdentityIsDurable(runID, jobID, obligationID string) (bool, error) {
	if m == nil || m.longHorizon == nil {
		return false, nil
	}
	runID = strings.TrimSpace(runID)
	jobID = strings.TrimSpace(jobID)
	obligationID = strings.TrimSpace(obligationID)
	if runID == "" {
		return false, nil
	}
	if jobID != "" && !validManagedQueryJobID(jobID) {
		return false, nil
	}
	if obligationID != "" && store.ValidateQueryObligationID(obligationID) != nil {
		return false, nil
	}
	dependencies, err := m.longHorizon.ListRunQueryDependencies(context.Background(), runID)
	if err != nil {
		return false, err
	}
	if jobID != "" {
		represented := false
		for _, dependency := range dependencies {
			if strings.TrimSpace(dependency.JobID) == jobID && dependency.RequiredForCompletion && !dependency.Detached {
				represented = true
				break
			}
		}
		record, found, getErr := m.longHorizon.GetQueryJob(context.Background(), jobID)
		if getErr != nil {
			return false, getErr
		}
		if found && strings.TrimSpace(record.Owner.RunID) == runID {
			concurrentLoser := record.CancelOutcome == jobs.CancelOutcomeBeforeSubmit && !record.SubmitDispatched &&
				record.Handle.IsZero() && strings.TrimSpace(record.ReplacesJobID) != ""
			if !concurrentLoser {
				represented = true
			}
		}
		if !represented {
			return false, nil
		}
		if obligationID == "" {
			return true, nil
		}
		derived, deriveErr := store.DeriveQueryObligationID(context.Background(), m.longHorizon, runID, jobID)
		if deriveErr != nil {
			return false, deriveErr
		}
		return derived == obligationID, nil
	}
	if obligationID == "" {
		return false, nil
	}
	for _, dependency := range dependencies {
		if !dependency.RequiredForCompletion || dependency.Detached {
			continue
		}
		candidate := strings.TrimSpace(dependency.ObligationID)
		if candidate == "" {
			candidate, err = store.DeriveQueryObligationID(context.Background(), m.longHorizon, runID, dependency.JobID)
			if err != nil {
				return false, err
			}
		}
		if candidate == obligationID {
			return true, nil
		}
	}
	return false, nil
}

// persistedQueryToolCallIsDurable closes the crash window in which a managed
// job was committed but its model-facing result lost the qj/qob stamp. The
// exact durable tool_call_id is a safe correlation; provider or SQL similarity
// is deliberately insufficient because one run may contain independent jobs.
func (m *Manager) persistedQueryToolCallIsDurable(runID, toolCallID string) (bool, error) {
	if m == nil || m.store == nil || m.longHorizon == nil {
		return false, nil
	}
	runID = strings.TrimSpace(runID)
	toolCallID = strings.TrimSpace(toolCallID)
	if runID == "" || toolCallID == "" {
		return false, nil
	}
	run, err := m.store.GetRun(context.Background(), runID)
	if err != nil {
		return false, err
	}
	records, err := m.longHorizon.ListQueryJobsByThread(context.Background(), run.ThreadID)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if strings.TrimSpace(record.Owner.RunID) != runID || !validManagedQueryJobID(record.ID) ||
			firstStringMapValue(record.Metadata, "tool_call_id") != toolCallID {
			continue
		}
		represented, verifyErr := m.persistedQueryActionIdentityIsDurable(runID, record.ID, "")
		if verifyErr != nil {
			return false, verifyErr
		}
		if represented {
			return true, nil
		}
	}
	return false, nil
}

// repairHalfCommittedQueryReplacements completes replacement swaps that
// crashed between a successor's durable launch (its record carries
// ReplacesJobID) and the dependency-ledger swap. Without repair the run would
// double-count both attempts as obligations forever. The swap is the stores'
// idempotent, CAS-guarded ReplaceRunQueryDependency, so racing another worker
// is safe; any error keeps the original fail-closed double-count.
func (m *Manager) repairHalfCommittedQueryReplacements(
	ctx context.Context,
	runID string,
	dependencies []store.RunQueryDependency,
	records []jobs.JobRecord,
) []store.RunQueryDependency {
	if m == nil || m.longHorizon == nil || len(records) == 0 {
		return dependencies
	}
	activeByJob := make(map[string]store.RunQueryDependency, len(dependencies))
	registered := make(map[string]bool, len(dependencies))
	for _, dependency := range dependencies {
		jobID := strings.TrimSpace(dependency.JobID)
		if jobID == "" {
			continue
		}
		registered[jobID] = true
		if dependency.RequiredForCompletion && !dependency.Detached {
			activeByJob[jobID] = dependency
		}
	}
	candidatesByPredecessor := make(map[string][]jobs.JobRecord)
	for _, record := range records {
		successorID := strings.TrimSpace(record.ID)
		replacesJobID := strings.TrimSpace(record.ReplacesJobID)
		if successorID == "" || replacesJobID == "" || replacesJobID == successorID {
			continue
		}
		if record.CancelOutcome == jobs.CancelOutcomeBeforeSubmit && !record.SubmitDispatched && record.Handle.IsZero() {
			continue
		}
		if strings.TrimSpace(record.Owner.RunID) != runID || registered[successorID] {
			continue
		}
		candidatesByPredecessor[replacesJobID] = append(candidatesByPredecessor[replacesJobID], record)
	}
	repaired := false
	for replacesJobID, candidates := range candidatesByPredecessor {
		// Multiple non-aborted successors are ambiguous evidence from an older
		// partial implementation. Never let record ordering choose the winner;
		// retain the fail-closed barrier for operator/model recovery.
		if len(candidates) != 1 {
			continue
		}
		record := candidates[0]
		successorID := strings.TrimSpace(record.ID)
		oldRow, oldActive := activeByJob[replacesJobID]
		if !oldActive || strings.TrimSpace(oldRow.ReplacementJobID) != "" {
			continue
		}
		validationState := strings.TrimSpace(string(record.ValidationState))
		if validationState == "" {
			validationState = store.QueryValidationUnvalidated
		}
		if record.ProfileOutcome != jobs.ProfileOutcomeNone && record.ProfileOutcome != jobs.ProfileOutcomeAccepted {
			validationState = store.QueryValidationRejected
		}
		obligationID, obligationErr := store.DeriveQueryObligationID(ctx, m.longHorizon, runID, successorID)
		if obligationErr != nil {
			continue
		}
		now := time.Now().UTC()
		if _, replaceErr := m.longHorizon.ReplaceRunQueryDependency(ctx, runID, replacesJobID, store.RunQueryDependency{
			RunID:                 runID,
			JobID:                 successorID,
			RequiredForCompletion: true,
			ValidationState:       validationState,
			ObligationID:          obligationID,
			CreatedAt:             now,
			UpdatedAt:             now,
		}); replaceErr != nil {
			// Store guards decide eligibility (the old attempt may still be
			// live or already swapped elsewhere); the fail-closed double-count
			// stands until a later pass can complete the swap.
			continue
		}
		repaired = true
	}
	if !repaired {
		return dependencies
	}
	refreshed, refreshErr := m.longHorizon.ListRunQueryDependencies(ctx, runID)
	if refreshErr != nil {
		return dependencies
	}
	return refreshed
}

// persistRunQueryDependency establishes the durable completion barrier as
// soon as a managed run-query tool returns its job identity. A rejected query
// remains audit-visible, but launching its replacement detaches the rejected
// dependency and links it to the new job rather than abandoning the task.
func (m *Manager) persistRunQueryDependency(ctx context.Context, runID, jobID, requestedReplacementJobID string) error {
	if m == nil || m.longHorizon == nil {
		return nil
	}
	runID, jobID = strings.TrimSpace(runID), strings.TrimSpace(jobID)
	if runID == "" || jobID == "" {
		return errors.New("run id and query job id are required")
	}
	record, ok, err := m.longHorizon.GetQueryJob(ctx, jobID)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(record.Owner.RunID) != runID {
		return fmt.Errorf("query job %s is not owned by run %s", jobID, runID)
	}
	replacesJobID := strings.TrimSpace(record.ReplacesJobID)
	if requested := strings.TrimSpace(requestedReplacementJobID); requested != "" && requested != replacesJobID {
		return fmt.Errorf("query job %s durable replacement link %q does not match tool output %q", jobID, replacesJobID, requested)
	}
	// Registry.Launch already performed this bind after durable winner
	// selection and before provider work. Repeating it here is an idempotent
	// verification/validation refresh; it can never reactivate a detached row.
	return m.longHorizon.BindQueryObligation(ctx, jobID)
}

func (m *Manager) attachedRequiredQueryDependency(ctx context.Context, runID, jobID string) (store.RunQueryDependency, bool, error) {
	dependencies, err := m.longHorizon.ListRunQueryDependencies(ctx, strings.TrimSpace(runID))
	if err != nil {
		return store.RunQueryDependency{}, false, err
	}
	jobID = strings.TrimSpace(jobID)
	for _, dependency := range dependencies {
		if strings.TrimSpace(dependency.JobID) == jobID && dependency.RequiredForCompletion && !dependency.Detached {
			return dependency, true, nil
		}
	}
	return store.RunQueryDependency{}, false, nil
}

// persistRunQueryWaitDependency keeps an already-running same-thread query as
// a completion barrier for the current run without transferring ownership or
// authorizing a cross-run replacement. This is how a newer user run safely
// observes older warehouse work instead of launching a duplicate.
func (m *Manager) persistRunQueryWaitDependency(ctx context.Context, threadID, runID, jobID string) error {
	if m == nil || m.longHorizon == nil {
		return errors.New("long-horizon repository is not configured")
	}
	threadID, runID, jobID = strings.TrimSpace(threadID), strings.TrimSpace(runID), strings.TrimSpace(jobID)
	if threadID == "" || runID == "" || jobID == "" {
		return errors.New("thread id, run id, and query job id are required")
	}
	record, ok, err := m.longHorizon.GetQueryJob(ctx, jobID)
	if err != nil {
		return err
	}
	if !ok || strings.TrimSpace(record.Owner.ThreadID) != threadID {
		return fmt.Errorf("query job %s is not owned by thread %s", jobID, threadID)
	}
	observable := record.Status == jobs.JobStatusCompleted && record.ValidationState != jobs.ValidationValidated
	switch record.Status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted,
		jobs.JobStatusSubmissionUnknown, jobs.JobStatusRunning,
		jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		observable = true
	}
	if !observable {
		return fmt.Errorf("query job %s is not active or awaiting result validation", jobID)
	}
	validationState := strings.TrimSpace(string(record.ValidationState))
	if validationState == "" {
		validationState = store.QueryValidationUnvalidated
	}
	obligationID, obligationErr := store.DeriveQueryObligationID(ctx, m.longHorizon, runID, jobID)
	if obligationErr != nil {
		return obligationErr
	}
	now := time.Now().UTC()
	persisted, err := m.longHorizon.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID:                 runID,
		JobID:                 jobID,
		RequiredForCompletion: true,
		ValidationState:       validationState,
		ObligationID:          obligationID,
		CreatedAt:             now,
		UpdatedAt:             now,
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(persisted.RunID) != runID || strings.TrimSpace(persisted.JobID) != jobID ||
		!persisted.RequiredForCompletion || persisted.Detached ||
		strings.EqualFold(strings.TrimSpace(persisted.ValidationState), store.QueryValidationValidated) {
		return fmt.Errorf("query job %s wait dependency did not become an active completion barrier", jobID)
	}
	return nil
}

func (m *Manager) suspendRunAtCheckpoint(
	ctx context.Context,
	run store.Run,
	point engine.LoopControlPoint,
	dependencyIDs []string,
	executionSnapshot continuationExecutionSnapshot,
	continuationCount int,
	maxContinuations int,
	claimToken string,
) (store.ContinuationCheckpoint, store.Run, error) {
	if m == nil || m.longHorizon == nil {
		return store.ContinuationCheckpoint{}, store.Run{}, errors.New("long-horizon repository is not configured")
	}
	historyJSON, err := json.Marshal(point.History)
	if err != nil {
		return store.ContinuationCheckpoint{}, store.Run{}, fmt.Errorf("marshal continuation history: %w", err)
	}
	providerStateJSON, err := json.Marshal(point.ProviderState)
	if err != nil {
		return store.ContinuationCheckpoint{}, store.Run{}, fmt.Errorf("marshal continuation provider state: %w", err)
	}
	executionSnapshot.RunID = run.ID
	executionSnapshot.ThreadID = run.ThreadID
	executionSnapshot.HistoryDigest = continuationPayloadDigest(historyJSON)
	executionSnapshot.ProviderStateDigest = continuationPayloadDigest(providerStateJSON)
	executionJSON, err := marshalContinuationExecutionSnapshot(executionSnapshot)
	if err != nil {
		return store.ContinuationCheckpoint{}, store.Run{}, fmt.Errorf("marshal continuation execution snapshot: %w", err)
	}
	dependencyIDs = queryDependencyIDsFromStrings(dependencyIDs)
	current, err := m.store.GetRun(ctx, run.ID)
	if err != nil {
		return store.ContinuationCheckpoint{}, store.Run{}, err
	}
	checkpointID := deterministicContinuationCheckpointID(
		run.ID,
		current.Revision,
		int64(point.Iteration),
		historyJSON,
		providerStateJSON,
		executionJSON,
		dependencyIDs,
	)
	checkpointCreatedAt := time.Now().UTC()
	wakeAt := checkpointCreatedAt.Add(runRecoveryProbeInterval)
	mutation := store.SuspendRunMutation{
		Checkpoint: store.ContinuationCheckpoint{
			ID:                    checkpointID,
			RunID:                 run.ID,
			ThreadID:              run.ThreadID,
			ToolBatchSequence:     int64(point.Iteration),
			HistoryJSON:           historyJSON,
			ProviderStateJSON:     providerStateJSON,
			ExecutionSnapshotJSON: executionJSON,
			DependencyIDs:         dependencyIDs,
			CreatedAt:             checkpointCreatedAt,
		},
		ExpectedRevision:          current.Revision,
		ExpectedStatuses:          []string{store.RunStatusRunning, store.RunStatusResuming, store.RunStatusResumePending},
		ExpectedResumeLeaseHolder: strings.TrimSpace(claimToken),
		ContinuationCount:         continuationCount,
		MaxAutoContinuations:      maxContinuations,
		WakeAt:                    wakeAt,
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		checkpoint, waiting, changed, suspendErr := m.longHorizon.SuspendRun(ctx, mutation)
		if suspendErr == nil {
			if changed || (checkpoint.ID == checkpointID && waiting.Status == store.RunStatusWaitingExternal) {
				return checkpoint, waiting, nil
			}
			return store.ContinuationCheckpoint{}, waiting, store.ErrConflict
		}
		lastErr = suspendErr
		if ctx.Err() != nil || errors.Is(suspendErr, store.ErrConflict) {
			break
		}
	}
	return store.ContinuationCheckpoint{}, store.Run{}, lastErr
}

const runRecoveryProbeInterval = 15 * time.Second

func queryDependencyIDsFromStrings(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func runHasAutomaticContinuation(status string) bool {
	switch strings.TrimSpace(status) {
	case store.RunStatusWaitingExternal, store.RunStatusResumePending, store.RunStatusResuming:
		return true
	default:
		return false
	}
}

func messageRunID(message store.Message) string {
	if message.RunID == nil {
		return ""
	}
	return strings.TrimSpace(*message.RunID)
}

// continuationUserMessageSequence resolves the durable thread generation of a
// checkpoint. In normal checkpoints the latest user message ID is preserved,
// so this is one indexed message read. The bounded backwards scan exists only
// for legacy or hand-authored checkpoints that omitted message IDs.
func (m *Manager) continuationUserMessageSequence(
	ctx context.Context,
	run store.Run,
	checkpoint *store.ContinuationCheckpoint,
) (int64, bool, error) {
	if m == nil || m.store == nil || checkpoint == nil {
		return 0, false, nil
	}
	if messageID := latestUserMessageID(checkpoint); messageID != "" {
		messages, err := m.store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID: run.ThreadID, MessageIDs: []string{messageID}, Limit: 2, IncludeHidden: true,
		})
		if err != nil {
			return 0, false, err
		}
		for _, message := range messages {
			if message.ID == messageID && message.Role == types.RoleUser && messageRunID(message) == run.ID {
				return message.Seq, true, nil
			}
		}
	}

	// Legacy fallback. A continuation normally has exactly one originating user
	// message, but cap the scan so a corrupt checkpoint cannot turn every wake
	// probe into an unbounded thread-history query.
	upper := checkpoint.CreatedAt.Add(time.Millisecond)
	var beforeSeq *int64
	for page := 0; page < continuationMessageFallbackPages; page++ {
		messages, err := m.store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID: run.ThreadID, Limit: continuationMessageScanPageSize, BeforeSeq: beforeSeq,
			BeforeTS: &upper, IncludeHidden: true, OrderBy: "seq_desc",
		})
		if err != nil {
			return 0, false, err
		}
		if len(messages) == 0 {
			break
		}
		minimumSequence := messages[0].Seq
		for _, message := range messages {
			if message.Seq < minimumSequence {
				minimumSequence = message.Seq
			}
			if message.Role == types.RoleUser && messageRunID(message) == run.ID {
				return message.Seq, true, nil
			}
		}
		if minimumSequence <= 1 || (beforeSeq != nil && minimumSequence >= *beforeSeq) {
			break
		}
		value := minimumSequence
		beforeSeq = &value
	}
	return 0, false, nil
}

// supersedingUserRunID detects a newer user turn by thread message sequence,
// not process memory or wall-clock ordering. Finalized-plan replay is safe: it
// reuses the same run and does not append another user message.
func (m *Manager) supersedingUserRunID(
	ctx context.Context,
	run store.Run,
	checkpoint *store.ContinuationCheckpoint,
) (string, error) {
	if m == nil || m.store == nil || checkpoint == nil {
		return "", nil
	}
	baseSequence, baseFound, err := m.continuationUserMessageSequence(ctx, run, checkpoint)
	if err != nil {
		return "", err
	}
	var afterSequence *int64
	if baseFound {
		value := baseSequence
		afterSequence = &value
	}
	// The timestamp is only a compatibility fallback when an old checkpoint did
	// not preserve message IDs. Widen by one millisecond for DATETIME(3) stores;
	// the candidate run creation check below prevents earlier runs from winning.
	var afterTimestamp *time.Time
	if !baseFound {
		value := checkpoint.CreatedAt.Add(-time.Millisecond)
		afterTimestamp = &value
	}
	for {
		messages, listErr := m.store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID: run.ThreadID, Limit: continuationMessageScanPageSize, AfterSeq: afterSequence,
			AfterTS: afterTimestamp, IncludeHidden: true, OrderBy: "seq_asc",
		})
		if listErr != nil {
			return "", listErr
		}
		if len(messages) == 0 {
			return "", nil
		}
		maximumSequence := int64(0)
		for _, message := range messages {
			if message.Seq > maximumSequence {
				maximumSequence = message.Seq
			}
			candidateRunID := messageRunID(message)
			if message.Role != types.RoleUser || candidateRunID == "" || candidateRunID == run.ID {
				continue
			}
			if baseFound && message.Seq <= baseSequence {
				continue
			}
			if !baseFound {
				candidate, getErr := m.store.GetRun(ctx, candidateRunID)
				if getErr != nil {
					if errors.Is(getErr, store.ErrNotFound) {
						continue
					}
					return "", getErr
				}
				if candidate.ThreadID != run.ThreadID || candidate.CreatedAt.Before(run.CreatedAt) {
					continue
				}
			}
			return candidateRunID, nil
		}
		if maximumSequence == 0 || (afterSequence != nil && maximumSequence <= *afterSequence) {
			return "", nil
		}
		value := maximumSequence
		afterSequence = &value
	}
}

// disarmRunContinuation revokes only model-loop continuation. Query jobs and
// completion dependencies remain durable and continue to be monitored.
func (m *Manager) disarmRunContinuation(ctx context.Context, runID string) (store.Run, bool, error) {
	if m == nil || m.longHorizon == nil {
		return store.Run{}, false, errors.New("long-horizon repository is not configured")
	}
	for attempt := 0; attempt < 8; attempt++ {
		current, err := m.store.GetRun(ctx, strings.TrimSpace(runID))
		if err != nil {
			return store.Run{}, false, err
		}
		if !runHasAutomaticContinuation(current.Status) {
			return current, false, nil
		}
		disarmed, changed, err := m.longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
			RunID:            current.ID,
			ExpectedRevision: current.Revision,
			ExpectedStatuses: []string{current.Status},
			Status:           store.RunStatusWaitingUser,
			ClearWakeAt:      true,
			ClearResumeLease: true,
		})
		if err != nil {
			return store.Run{}, false, err
		}
		if changed {
			return disarmed, true, nil
		}
	}
	return store.Run{}, false, store.ErrConflict
}

func (m *Manager) disarmIfSuperseded(
	ctx context.Context,
	run store.Run,
	checkpoint *store.ContinuationCheckpoint,
) (store.Run, string, error) {
	if checkpoint == nil {
		var err error
		checkpoint, err = m.longHorizon.LatestContinuationCheckpoint(ctx, run.ID)
		if err != nil {
			return store.Run{}, "", err
		}
	}
	if checkpoint == nil {
		return run, "", nil
	}
	supersedingRunID, err := m.supersedingUserRunID(ctx, run, checkpoint)
	if err != nil || supersedingRunID == "" {
		return run, supersedingRunID, err
	}
	disarmed, _, err := m.disarmRunContinuation(ctx, run.ID)
	return disarmed, supersedingRunID, err
}

func (m *Manager) validateRunContinuationGeneration(ctx context.Context, runID string) (bool, error) {
	if m == nil || m.longHorizon == nil {
		return false, errors.New("long-horizon repository is not configured")
	}
	current, err := m.store.GetRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return false, err
	}
	_, supersedingRunID, disarmErr := m.disarmIfSuperseded(ctx, current, nil)
	// Discovery of the newer durable user generation is definitive even when
	// the best-effort CAS disarm reports a transient write error. The local
	// model worker must stop and cease renewing; lease expiry then fences it for
	// every other process as well.
	return supersedingRunID != "", disarmErr
}

// disarmPriorContinuationsForNewRun is called only after the new user message
// commits. Its message sequence is the durable thread generation, so two
// cross-process submissions agree which continuation is older even when their
// run creation timestamps collide.
func (m *Manager) disarmPriorContinuationsForNewRun(ctx context.Context, newRun store.Run, newUserSequence int64) error {
	if m == nil || m.longHorizon == nil || newUserSequence <= 0 {
		return nil
	}
	runs, err := m.store.ListRuns(ctx, newRun.ThreadID, continuationRunScanLimit)
	if err != nil {
		return err
	}
	for _, candidate := range runs {
		if candidate.ID == newRun.ID || !runHasAutomaticContinuation(candidate.Status) {
			continue
		}
		checkpoint, checkpointErr := m.longHorizon.LatestContinuationCheckpoint(ctx, candidate.ID)
		if checkpointErr != nil {
			return checkpointErr
		}
		sequence, found, sequenceErr := m.continuationUserMessageSequence(ctx, candidate, checkpoint)
		if sequenceErr != nil {
			return sequenceErr
		}
		if found && sequence >= newUserSequence {
			continue
		}
		if !found && candidate.CreatedAt.After(newRun.CreatedAt) {
			continue
		}
		if _, _, disarmErr := m.disarmRunContinuation(ctx, candidate.ID); disarmErr != nil {
			return disarmErr
		}
	}
	return nil
}

// claimRunContinuation makes the run wake exactly-once across processes. The
// resume lease remains held while model execution is in flight; the next
// suspension or terminal transition clears/overwrites it. A process crash is
// therefore recoverable after lease expiry.
func (m *Manager) claimRunContinuation(ctx context.Context, runID string) (store.Run, continuationClaimOutcome, string, error) {
	if m == nil || m.longHorizon == nil {
		return store.Run{}, continuationClaimHeldElsewhere, "", errors.New("long-horizon repository is not configured")
	}
	claimToken := m.resumeClaimant + "_" + strings.ReplaceAll(types.NewUUID(), "-", "")
	for attempt := 0; attempt < 4; attempt++ {
		current, err := m.store.GetRun(ctx, runID)
		if err != nil {
			return store.Run{}, continuationClaimHeldElsewhere, "", err
		}
		if attempt == 0 && runHasAutomaticContinuation(current.Status) {
			checked, supersedingRunID, checkErr := m.disarmIfSuperseded(ctx, current, nil)
			if checkErr != nil {
				return store.Run{}, continuationClaimHeldElsewhere, "", checkErr
			}
			if supersedingRunID != "" {
				return checked, continuationClaimHeldElsewhere, "", nil
			}
		}
		now := time.Now().UTC()
		if current.Status == store.RunStatusWaitingExternal {
			pending, changed, err := m.longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
				RunID:            runID,
				ExpectedRevision: current.Revision,
				ExpectedStatuses: []string{store.RunStatusWaitingExternal},
				Status:           store.RunStatusResumePending,
				WakeAt:           &now,
			})
			if err != nil {
				return store.Run{}, continuationClaimHeldElsewhere, "", err
			}
			if !changed {
				continue
			}
			current = pending
		}
		leaseUntil := now.Add(runWakeLeaseTTL)
		claimed, ok, err := m.longHorizon.ClaimRunWake(ctx, runID, current.Revision, claimToken, now, leaseUntil)
		if err != nil {
			return store.Run{}, continuationClaimHeldElsewhere, "", err
		}
		if ok {
			checked, supersedingRunID, checkErr := m.disarmIfSuperseded(ctx, claimed, nil)
			if checkErr != nil {
				wakeAt := time.Now().UTC().Add(runRecoveryProbeInterval)
				_ = m.longHorizon.ReleaseRunWake(context.Background(), claimed.ID, claimToken, store.RunStatusResumePending, &wakeAt)
				return store.Run{}, continuationClaimHeldElsewhere, "", checkErr
			}
			if supersedingRunID != "" {
				return checked, continuationClaimHeldElsewhere, "", nil
			}
			return claimed, continuationClaimAcquired, claimToken, nil
		}
		if claimed.Status == store.RunStatusWaitingUser {
			return claimed, continuationClaimCapReached, "", nil
		}
		if claimed.ID != "" && (claimed.Status == store.RunStatusResuming || claimed.Status == store.RunStatusCancelled ||
			claimed.Status == store.RunStatusCompleted || claimed.Status == store.RunStatusFailed) {
			return claimed, continuationClaimHeldElsewhere, "", nil
		}
	}
	current, err := m.store.GetRun(ctx, runID)
	return current, continuationClaimHeldElsewhere, "", err
}

func (m *Manager) transitionRunToWaitingUser(
	ctx context.Context,
	runID string,
	continuationCount int,
	maxContinuations int,
	claimToken string,
) error {
	if m == nil || m.longHorizon == nil {
		return errors.New("long-horizon repository is not configured")
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		current, err := m.store.GetRun(ctx, runID)
		if err != nil {
			lastErr = err
			continue
		}
		claimToken = strings.TrimSpace(claimToken)
		if current.Status == store.RunStatusResuming && (claimToken == "" || current.ResumeLeaseHolder != claimToken) {
			return store.ErrConflict
		}
		_, changed, err := m.longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
			RunID:                     runID,
			ExpectedRevision:          current.Revision,
			ExpectedStatuses:          []string{store.RunStatusQueued, store.RunStatusRunning, store.RunStatusWaitingExternal, store.RunStatusResumePending, store.RunStatusResuming},
			Status:                    store.RunStatusWaitingUser,
			ContinuationCount:         &continuationCount,
			MaxAutoContinuations:      &maxContinuations,
			ClearWakeAt:               true,
			ClearResumeLease:          true,
			ExpectedResumeLeaseHolder: claimToken,
		})
		if err != nil {
			lastErr = err
			continue
		}
		if changed || current.Status == store.RunStatusWaitingUser {
			return nil
		}
		lastErr = store.ErrConflict
	}
	return firstNonNilError(lastErr, store.ErrConflict)
}

// transitionRunTerminal is the only terminal write used by model execution.
// It first fences the state change with the run revision (and, for resumed
// executions, the unique continuation claim token), then applies ancillary
// terminal fields. A stale worker therefore cannot overwrite a cancellation or
// a newer continuation after losing its lease.
func (m *Manager) transitionRunTerminal(
	ctx context.Context,
	runID string,
	status string,
	claimToken string,
	fields map[string]any,
) (bool, error) {
	status = strings.TrimSpace(status)
	if status != store.RunStatusCompleted && status != store.RunStatusFailed && status != store.RunStatusCancelled {
		return false, errors.New("terminal run status is required")
	}
	if m == nil || m.longHorizon == nil {
		return true, m.updateRunStatusWithFallback(ctx, runID, status, fields)
	}
	claimToken = strings.TrimSpace(claimToken)
	for attempt := 0; attempt < 4; attempt++ {
		current, err := m.store.GetRun(ctx, runID)
		if err != nil {
			return false, err
		}
		if current.Status == store.RunStatusCompleted || current.Status == store.RunStatusFailed || current.Status == store.RunStatusCancelled {
			return false, nil
		}
		expectedStatuses := []string{store.RunStatusQueued, store.RunStatusRunning}
		expectedHolder := ""
		if claimToken != "" {
			if (current.Status != store.RunStatusRunning && current.Status != store.RunStatusResuming) ||
				current.ResumeLeaseHolder != claimToken {
				return false, store.ErrConflict
			}
			expectedStatuses = []string{current.Status}
			expectedHolder = claimToken
		}
		_, changed, err := m.longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
			RunID:                     runID,
			ExpectedRevision:          current.Revision,
			ExpectedStatuses:          expectedStatuses,
			Status:                    status,
			ClearWakeAt:               true,
			ClearResumeLease:          true,
			ExpectedResumeLeaseHolder: expectedHolder,
		})
		if err != nil {
			return false, err
		}
		if !changed {
			continue
		}
		if err := m.updateRunStatusWithFallback(ctx, runID, status, fields); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, store.ErrConflict
}

func (m *Manager) transitionRunExecutionStarted(ctx context.Context, runID, claimToken string, fields map[string]any) error {
	if m == nil || m.longHorizon == nil {
		return m.updateRunStatusWithFallback(ctx, runID, store.RunStatusRunning, fields)
	}
	claimToken = strings.TrimSpace(claimToken)
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		current, err := m.store.GetRun(ctx, runID)
		if err != nil {
			lastErr = err
			continue
		}
		if claimToken != "" {
			if current.Status == store.RunStatusResuming && current.ResumeLeaseHolder == claimToken &&
				current.ResumeLeaseExpiresAt != nil && current.ResumeLeaseExpiresAt.After(time.Now().UTC()) {
				return nil
			}
			if current.Status == store.RunStatusRunning && current.ResumeLeaseHolder == claimToken &&
				current.ResumeLeaseExpiresAt != nil && current.ResumeLeaseExpiresAt.After(time.Now().UTC()) {
				if metadataErr := m.longHorizon.UpdateClaimedRunMetadata(ctx, runID, claimToken, fields); metadataErr == nil {
					return nil
				} else {
					lastErr = metadataErr
					continue
				}
			}
			if current.Status != store.RunStatusQueued {
				return store.ErrConflict
			}
			now := time.Now().UTC()
			claimed, changed, claimErr := m.longHorizon.ClaimRunWake(
				ctx, runID, current.Revision, claimToken, now, now.Add(runWakeLeaseTTL),
			)
			if claimErr != nil {
				lastErr = claimErr
				continue
			}
			if changed && claimed.Status == store.RunStatusRunning && claimed.ResumeLeaseHolder == claimToken {
				if metadataErr := m.longHorizon.UpdateClaimedRunMetadata(ctx, runID, claimToken, fields); metadataErr == nil {
					return nil
				} else {
					lastErr = metadataErr
					continue
				}
			}
			lastErr = store.ErrConflict
			continue
		}
		if current.Status == store.RunStatusRunning {
			return nil
		}
		if current.Status != store.RunStatusQueued {
			return store.ErrConflict
		}
		_, changed, err := m.longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
			RunID:            runID,
			ExpectedRevision: current.Revision,
			ExpectedStatuses: []string{store.RunStatusQueued},
			Status:           store.RunStatusRunning,
		})
		if err != nil {
			lastErr = err
			continue
		}
		if changed {
			return nil
		}
		lastErr = store.ErrConflict
	}
	return firstNonNilError(lastErr, store.ErrConflict)
}

// cancelRunDurably is user-authoritative: it may revoke an active continuation
// lease, but deliberately never cancels or detaches warehouse query jobs.
func (m *Manager) cancelRunDurably(ctx context.Context, runID string) (bool, error) {
	if m == nil || m.longHorizon == nil {
		return false, nil
	}
	for attempt := 0; attempt < 4; attempt++ {
		current, err := m.store.GetRun(ctx, runID)
		if err != nil {
			return false, err
		}
		switch current.Status {
		case store.RunStatusCompleted, store.RunStatusFailed, store.RunStatusCancelled:
			return false, nil
		}
		_, changed, err := m.longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
			RunID:            runID,
			ExpectedRevision: current.Revision,
			ExpectedStatuses: []string{current.Status},
			Status:           store.RunStatusCancelled,
			ClearWakeAt:      true,
			ClearResumeLease: true,
		})
		if err != nil {
			return false, err
		}
		if !changed {
			continue
		}
		endedAt := time.Now().UTC()
		if err := m.updateRunStatusWithFallback(ctx, runID, store.RunStatusCancelled, map[string]any{"ended_at": endedAt}); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, store.ErrConflict
}

func (m *Manager) requeueClaimedRun(ctx context.Context, runID, claimToken string) error {
	if m == nil || m.longHorizon == nil {
		return errors.New("long-horizon repository is not configured")
	}
	wakeAt := time.Now().UTC().Add(runRecoveryProbeInterval)
	return m.longHorizon.ReleaseRunWake(ctx, runID, strings.TrimSpace(claimToken), store.RunStatusResumePending, &wakeAt)
}

// parkClaimedInitialRun releases a live initial-execution lease directly to
// waiting_user. Unlike continuation requeue, it never enters resume_pending:
// no model checkpoint exists yet, so claiming a wake would only consume a
// continuation before discovering that recovery must quarantine the run.
func (m *Manager) parkClaimedInitialRun(ctx context.Context, runID, claimToken string) error {
	if m == nil || m.longHorizon == nil {
		return errors.New("long-horizon repository is not configured")
	}
	return m.longHorizon.ReleaseRunWake(
		ctx, strings.TrimSpace(runID), strings.TrimSpace(claimToken), store.RunStatusWaitingUser, nil,
	)
}

func queryDependencyIDs(refs []queryDependencyRef) []string {
	if len(refs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if id := strings.TrimSpace(ref.ID); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func queryJobTerminal(status jobs.JobStatus) bool {
	switch status {
	case jobs.JobStatusCompleted, jobs.JobStatusFailed, jobs.JobStatusCancelled,
		jobs.JobStatusExpired, jobs.JobStatusOrphaned:
		return true
	default:
		return false
	}
}

// waitForQueryDependencyWake consumes the first durable wake from any
// dependency, or returns once every dependency is terminal. Each job keeps its
// own owner token, and Claim makes wake consumption exactly-once in durable
// stores even when more than one process is able to resume work.
func (m *Manager) waitForQueryDependencyWake(ctx context.Context, refs []queryDependencyRef) (jobs.WakeKind, error) {
	registry, ok := m.queryJobs.(*jobs.Registry)
	if !ok || registry == nil || len(refs) == 0 {
		return jobs.WakeTerminal, nil
	}
	jobRefs := make([]jobs.JobRef, 0, len(refs))
	for _, ref := range refs {
		jobRefs = append(jobRefs, jobs.JobRef{ID: ref.ID, Owner: ref.Owner})
	}
	result, err := registry.WaitForJobRefs(ctx, jobRefs, jobs.WaitOptions{Claim: true})
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) || errors.Is(err, jobs.ErrOwnershipMismatch) {
			// A reference that disappeared during rehydration/reaping needs an
			// explicit recovery decision; it is never equivalent to success.
			return jobs.WakeRecoveryRequired, nil
		}
		return "", err
	}
	if result == nil || result.Event == nil {
		return jobs.WakeTerminal, nil
	}
	return result.Event.Kind, nil
}
