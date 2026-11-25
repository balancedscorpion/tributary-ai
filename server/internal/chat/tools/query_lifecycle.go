package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultBigQueryScanBytes = int64(100 << 30)
)

type managedQueryLaunchRequest struct {
	ToolName        string
	Provider        string
	ConnectionID    string
	SQL             string
	SubmittedSQL    string
	Description     string
	Adapter         jobs.ProviderAdapter
	Session         map[string]string
	Metadata        map[string]any
	Budgets         jobs.Budgets
	RequestedLimit  int
	PageSize        int
	ConfiguredLimit int
	LimitCapped     bool
	FullData        bool
	ReplacesJobID   string
	// QueryObligationID is the opaque stable retry handle (qob-<digest>). When
	// set it targets the obligation's current active attempt regardless of how
	// many replacements have happened since the model last observed the chain.
	QueryObligationID string
}

// managedQueryObligationIDSchema defines the shared stable retry handle input.
func managedQueryObligationIDSchema() map[string]any {
	return map[string]any{
		"type":    "string",
		"pattern": "^qob-[0-9a-f]{32}$",
		"description": "Stable run-scoped obligation handle returned by the query tools for unfinished work. " +
			"It resolves to the obligation's current attempt and remains valid across replacements. Copy it exactly; never substitute a job, statement, query, or provider id.",
	}
}

func decodeOptionalQueryObligationID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", errors.New("query_obligation_id must be a non-null string")
	}
	id := strings.TrimSpace(*value)
	if id == "" {
		return "", errors.New("query_obligation_id must not be blank")
	}
	if err := store.ValidateQueryObligationID(id); err != nil {
		return "", err
	}
	return id, nil
}

func decodeOptionalManagedReplacementJobID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value *string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", errors.New("replaces_job_id must be a non-null app-managed job id")
	}
	return validateManagedReplacementJobID(*value)
}

func validateManagedReplacementJobID(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("replaces_job_id must not be blank")
	}
	id, err := jobs.NormalizeManagedQueryJobID(value)
	if err != nil {
		return "", errors.New("replaces_job_id must be an exact non-sentinel app-managed qj-* id; provider-native ids are not accepted")
	}
	return id, nil
}

func invalidManagedQueryRetryInput(err error) Output {
	message := "Invalid query retry target."
	if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = strings.TrimSpace(err.Error())
	}
	result := map[string]any{
		"error":                        message,
		"replacement_preflight_failed": true,
		"replacement_wait_required":    true,
		"rewrite_required":             true,
		"instructions":                 "Copy the exact query_obligation_id from the existing query result. Do not use nil, zero, a job id, or a provider-native statement/query id, and do not launch unrelated work to bypass this validation error.",
	}
	return Output{
		OK:           false,
		ResultFormat: ResultFormatJSON,
		Result:       result,
		QueryMetadata: map[string]any{
			"replacement_preflight_failed": true,
			"replacement_wait_required":    true,
		},
	}
}

// launchManagedQuery delegates reserve-before-profile-before-submit ordering
// entirely to Registry.Launch. Every non-nil job is formatted from its durable
// identity and typed profile state, including replays and rejected profiles.
func launchManagedQuery(ctx context.Context, exec ExecContext, req managedQueryLaunchRequest) (Output, error) {
	if exec.Jobs == nil {
		return JSONError("Async query registry is not configured."), nil
	}
	if req.Adapter == nil {
		return JSONError(fmt.Sprintf("%s managed query adapter is not configured.", req.Provider)), nil
	}
	provider := strings.TrimSpace(strings.ToLower(req.Provider))
	if provider == "" {
		provider = strings.TrimSpace(strings.ToLower(req.Adapter.Provider()))
	}
	submittedSQL := strings.TrimSpace(req.SubmittedSQL)
	if submittedSQL == "" {
		submittedSQL = strings.TrimSpace(req.SQL)
	}
	requestedReplacementJobID := strings.TrimSpace(req.ReplacesJobID)
	requestedObligationID := strings.TrimSpace(req.QueryObligationID)
	var resolvedObligationID string
	if requestedReplacementJobID != "" || requestedObligationID != "" {
		resolution, resolutionErr := resolveManagedQueryRetryTarget(
			ctx, exec.LongHorizon, exec.ThreadID, exec.RunID, requestedObligationID, requestedReplacementJobID,
		)
		if resolutionErr != nil {
			return formatManagedQueryReplacementPreflightFailure(resolution), nil
		}
		req.ReplacesJobID = resolution.Record.ID
		resolvedObligationID = resolution.ObligationID
	}
	idempotencyKey, err := managedQueryIdempotencyKey(exec, provider)
	if err != nil {
		if resolvedObligationID != "" {
			return formatManagedQueryRetryLaunchDeferred(resolvedObligationID), nil
		}
		return JSONError("Failed to create a durable query identity."), nil
	}
	metadata := cloneMap(req.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["provider"] = provider
	metadata["tool_call_id"] = strings.TrimSpace(exec.ToolCallID)
	metadata["sql"] = strings.TrimSpace(req.SQL)
	metadata["original_sql"] = strings.TrimSpace(req.SQL)
	metadata["submitted_sql"] = submittedSQL
	metadata["sql_preview"] = asyncSQLPreview(req.SQL)
	metadata["full_data_requested"] = req.FullData
	metadata["requested_limit"] = req.RequestedLimit
	metadata["effective_limit"] = req.PageSize
	metadata["configured_limit"] = req.ConfiguredLimit
	metadata["limit_capped"] = req.LimitCapped
	metadata["max_result_rows"] = req.Budgets.MaxResultRows
	metadata["max_result_bytes"] = req.Budgets.MaxResultBytes
	metadata["automatic_scan_bytes"] = req.Budgets.AutomaticScanBytes
	metadata["max_scan_bytes"] = req.Budgets.MaxScanBytes
	metadata[jobs.QueryObligationManagedMetadataKey] = true
	if replacesJobID := strings.TrimSpace(req.ReplacesJobID); replacesJobID != "" {
		metadata["replaces_job_id"] = replacesJobID
		if requestedReplacementJobID != "" && requestedReplacementJobID != replacesJobID {
			metadata["attempted_replaces_job_id"] = requestedReplacementJobID
			metadata["replacement_target_canonicalized"] = true
		}
	}
	if resolvedObligationID != "" {
		metadata["query_obligation_id"] = resolvedObligationID
	}
	if description := strings.TrimSpace(req.Description); description != "" {
		metadata["description"] = description
	}

	exec.Jobs.RegisterAdapter(req.Adapter)
	job, launchErr := exec.Jobs.Launch(ctx, jobs.LaunchRequest{
		Owner: jobs.JobOwner{
			ThreadID: exec.ThreadID,
			RunID:    exec.RunID,
		},
		Provider:       provider,
		Adapter:        req.Adapter,
		ConnectionID:   strings.TrimSpace(req.ConnectionID),
		SQL:            submittedSQL,
		SQLPreview:     asyncSQLPreview(req.SQL),
		Metadata:       metadata,
		Budgets:        req.Budgets,
		Session:        req.Session,
		IdempotencyKey: idempotencyKey,
		ReplacesJobID:  strings.TrimSpace(req.ReplacesJobID),
		ScanApproval:   exec.RequestScanApproval,
	})
	if errors.Is(launchErr, jobs.ErrQueryObligationBinding) && strings.TrimSpace(req.ReplacesJobID) != "" {
		return formatManagedQueryBindingFailure(resolvedObligationID), nil
	}
	if job == nil {
		// A retry that already resolved its obligation must never surface a
		// bare error: the manager would synthesize an unrelated root
		// obligation for it, splitting one logical task in two while the
		// resolved obligation is still the run's active barrier. Report a
		// deferred retry against the SAME verified handle instead.
		if resolvedObligationID != "" {
			return formatManagedQueryRetryLaunchDeferred(resolvedObligationID), nil
		}
		if launchErr != nil {
			return JSONError(launchErr.Error()), nil
		}
		return JSONError("Managed query launch returned no durable job."), nil
	}
	return formatManagedQueryLaunch(ctx, exec, req, provider, metadata, job, launchErr), nil
}

// formatManagedQueryRetryLaunchDeferred reports a no-durable-job launch
// failure for an already-resolved retry target. The verified obligation stays
// the run's active barrier; the model retries the same handle after the
// transient condition clears, and the manager's wait branch preserves the
// obligation instead of synthesizing an unrelated one.
func formatManagedQueryRetryLaunchDeferred(obligationID string) Output {
	result := map[string]any{
		"error":                                "The managed retry could not reach a durable reservation.",
		"replacement_preflight_failed":         true,
		"replacement_wait_required":            true,
		"replacement_preflight_retry_required": true,
		"rewrite_required":                     false,
		"instructions":                         "The retry could not reach a durable reservation; no provider work happened and the original obligation is still active. Fix or wait out the reported condition, then retry with this exact query_obligation_id. Do not start unrelated query work for this task.",
	}
	queryMetadata := map[string]any{
		"replacement_preflight_failed": true,
		"replacement_wait_required":    true,
	}
	obligationID = strings.TrimSpace(obligationID)
	if store.ValidateQueryObligationID(obligationID) == nil {
		result["query_obligation_id"] = obligationID
		queryMetadata["query_obligation_id"] = obligationID
	}
	return Output{OK: false, ResultFormat: ResultFormatJSON, Result: result, QueryMetadata: queryMetadata}
}

// formatManagedQueryBindingFailure reports a durable-winner binding failure
// without exposing or re-attaching the deliberately aborted loser job.
func formatManagedQueryBindingFailure(obligationID string) Output {
	result := map[string]any{
		"replacement_preflight_failed": true,
		"replacement_wait_required":    true,
		"rewrite_required":             false,
		"error":                        "The durable replacement attempt could not be bound to its query obligation before provider work.",
		"instructions":                 "Do not submit another query yet. Re-check this query obligation: another durable successor may already be active, and the aborted attempt performed no provider work.",
	}
	obligationID = strings.TrimSpace(obligationID)
	if store.ValidateQueryObligationID(obligationID) == nil {
		result["query_obligation_id"] = obligationID
	}
	queryMetadata := map[string]any{
		"replacement_preflight_failed": true,
		"replacement_wait_required":    true,
	}
	if store.ValidateQueryObligationID(obligationID) == nil {
		queryMetadata["query_obligation_id"] = obligationID
	}
	return Output{OK: false, ResultFormat: ResultFormatJSON, Result: result, QueryMetadata: queryMetadata}
}

func formatManagedQueryLaunch(
	ctx context.Context,
	exec ExecContext,
	req managedQueryLaunchRequest,
	provider string,
	fallbackMetadata map[string]any,
	job *jobs.Job,
	launchErr error,
) Output {
	ownerToken := strings.TrimSpace(job.Owner.Handle)
	ownerThreadID := strings.TrimSpace(job.Owner.ThreadID)
	if ownerThreadID == "" {
		ownerThreadID = strings.TrimSpace(exec.ThreadID)
	}
	ownerRunID := strings.TrimSpace(job.Owner.RunID)
	if ownerRunID == "" {
		ownerRunID = strings.TrimSpace(exec.RunID)
	}
	artifactID := deterministicAsyncArtifactID(ownerThreadID, job.ID)
	_ = exec.Jobs.SetArtifactID(job.ID, jobs.JobOwner{
		ThreadID: ownerThreadID,
		RunID:    ownerRunID,
		Handle:   ownerToken,
	}, artifactID)

	qm := cloneMap(job.Metadata)
	if qm == nil {
		qm = cloneMap(fallbackMetadata)
	}
	if qm == nil {
		qm = map[string]any{}
	}
	stripManagedQueryLegacyRetryMetadata(qm)
	executionStatus := strings.TrimSpace(string(job.Status))
	qm["job_id"] = job.ID
	qm["owner_token"] = ownerToken
	qm["thread_id"] = ownerThreadID
	qm["run_id"] = ownerRunID
	qm["provider"] = provider
	qm["artifact_id"] = artifactID
	qm["status"] = executionStatus
	qm["execution_status"] = executionStatus
	qm["adaptive_wait_ms"] = 1100
	qm["submitted_at_ms"] = job.SubmittedAt.UnixMilli()
	providerSubmissionConfirmed := !job.Handle.IsZero()
	qm["submit_dispatched"] = job.SubmitDispatched
	qm["provider_submission_confirmed"] = providerSubmissionConfirmed
	qm["submission_uncertain"] = managedQuerySubmissionUncertain(job.Status, job.SubmitDispatched, providerSubmissionConfirmed)
	qm["remote_submitted"] = providerSubmissionConfirmed
	qm["max_result_rows"] = job.Budgets.MaxResultRows
	qm["max_result_bytes"] = job.Budgets.MaxResultBytes
	qm["automatic_scan_bytes"] = job.Budgets.AutomaticScanBytes
	qm["max_scan_bytes"] = job.Budgets.MaxScanBytes
	if providerCap, ok := managedQueryInt64(qm["provider_max_result_bytes"]); ok && providerCap > 0 {
		effectiveCap := job.Budgets.MaxResultBytes
		if effectiveCap <= 0 || effectiveCap > providerCap {
			effectiveCap = providerCap
		}
		qm["effective_provider_result_byte_limit"] = effectiveCap
	}
	if obligationID := managedQueryObligationID(ctx, exec.LongHorizon, ownerRunID, job, fallbackMetadata); obligationID != "" {
		qm["query_obligation_id"] = obligationID
	}
	qm["profile_outcome"] = string(job.ProfileOutcome)
	qm["profile_phase"] = strings.TrimSpace(job.ProfilePhase)
	qm["profile_dispatched"] = job.ProfileDispatched
	if job.Profile != nil {
		qm["query_profile"] = job.Profile
		qm["profile"] = job.Profile
		if authorized, ok := managedQueryInt64(job.Profile.ProviderDetails["authorized_scan_bytes"]); ok && authorized > 0 {
			qm["authorized_scan_bytes"] = authorized
		}
	}
	if !job.ProfileHandle.IsZero() {
		qm["profile_handle"] = job.ProfileHandle
	}
	addNativeQueryMetadata(qm, job)

	if job.Status == jobs.JobStatusProfiling || errors.Is(launchErr, jobs.ErrProfilePending) || errors.Is(launchErr, jobs.ErrLeaseLost) {
		qm["status"] = string(jobs.JobStatusProfiling)
		qm["execution_status"] = string(jobs.JobStatusProfiling)
		qm["profile_pending"] = true
		result := managedQueryIdentityResult(req, provider, job, ownerToken, artifactID, qm)
		result["status"] = string(jobs.JobStatusProfiling)
		result["execution_status"] = string(jobs.JobStatusProfiling)
		result["profile_pending"] = true
		result["message"] = fmt.Sprintf("%s query is durably reserved and profiling before submission. Keep this job identity and wait; do not re-profile or resubmit it.", providerDisplayName(provider))
		result["instructions"] = "The original durable job is still profiling. Continue the task by waiting for its next meaningful state; never launch a duplicate merely because this call yielded."
		emitManagedQueryProgress(exec, provider, "profiling", job, ownerToken, qm)
		return Output{
			OK:             true,
			ResultFormat:   ResultFormatJSON,
			Result:         result,
			ResultMetadata: datasourceAsyncQueryResultMetadata(req.ToolName, provider, qm),
			QueryMetadata:  qm,
		}
	}

	if isRejectedProfileOutcome(job.ProfileOutcome) || errors.Is(launchErr, jobs.ErrProfileRejected) {
		return formatManagedQueryProfileRejection(exec, req, provider, job, ownerToken, artifactID, qm)
	}

	terminalFailure := false
	switch job.Status {
	case jobs.JobStatusFailed, jobs.JobStatusCancelled, jobs.JobStatusExpired, jobs.JobStatusOrphaned:
		terminalFailure = true
	}
	if terminalFailure || (launchErr != nil && !managedQueryStatusCanContinue(job.Status)) {
		message := strings.TrimSpace(job.Error)
		if message == "" && launchErr != nil {
			message = strings.TrimSpace(launchErr.Error())
		}
		if message == "" {
			switch job.Status {
			case jobs.JobStatusCancelled:
				message = "Managed query was cancelled before a usable remote execution could be confirmed."
			case jobs.JobStatusExpired:
				message = "Managed query expired before a usable remote execution could be confirmed."
			case jobs.JobStatusOrphaned:
				message = "Managed query submission could not be reconciled with a provider execution."
			default:
				message = "Managed query launch failed before a confirmed remote submission."
			}
		}
		qm["status"] = executionStatus
		qm["remote_submitted"] = providerSubmissionConfirmed
		qm["replacement_required"] = true
		result := managedQueryIdentityResult(req, provider, job, ownerToken, artifactID, qm)
		result["error"] = message
		result["remote_submitted"] = providerSubmissionConfirmed
		result["submit_dispatched"] = job.SubmitDispatched
		result["provider_submission_confirmed"] = providerSubmissionConfirmed
		result["submission_uncertain"] = qm["submission_uncertain"]
		result["replacement_required"] = true
		result["rewrite_required"] = true
		result["instructions"] = fmt.Sprintf("Inspect this durable failed attempt, then run a corrected or safer alternative with %s. Do not abandon the task.", managedQueryRetryTargetClause(qm))
		diagnosticCode := "QUERY_LAUNCH_FAILED"
		diagnosticTitle := "Managed query launch failed"
		diagnosticKind := "unavailable"
		diagnosticWhy := "The attempt reached a terminal failure or was rejected before a usable remote execution could be confirmed."
		diagnosticTryThis := "Inspect connection/query diagnostics and launch a corrected replacement targeting the returned query_obligation_id."
		if isBigQueryBytesBilledLimitError(provider, message) {
			qm["scan_budget_reprofile_required"] = true
			result["scan_budget_reprofile_required"] = true
			result["retryable"] = true
			result["rewrite_required"] = false
			result["instructions"] = fmt.Sprintf(
				"BigQuery's execution-time estimate moved above the approved cap. Launch the same SQL as a linked replacement with %s so it is freshly profiled and asks for supplemental approval if needed. Do not abandon the task or retry the failed provider job ID.",
				managedQueryRetryTargetClause(qm),
			)
			diagnosticCode = "QUERY_SCAN_BUDGET_ESTIMATE_CHANGED"
			diagnosticTitle = "BigQuery estimate changed"
			diagnosticKind = "budget"
			diagnosticWhy = "BigQuery recalculated the query after approval and required a higher maximumBytesBilled value. The failed attempt did not materialize rows."
			diagnosticTryThis = "Launch the same SQL as a linked replacement so it is re-profiled and, if still above the automatic limit, presents a fresh one-time approval."
		}
		diagnostic := managedQueryDiagnostic(
			diagnosticCode, diagnosticTitle, diagnosticKind, message,
			diagnosticWhy, diagnosticTryThis, asyncSQLPreview(req.SQL),
		)
		emitManagedQueryProgress(exec, provider, "failed", job, ownerToken, qm)
		return Output{
			OK:             false,
			ResultFormat:   ResultFormatJSON,
			Result:         result,
			ResultMetadata: managedQueryFailureResultMetadata(req.ToolName, provider, qm, diagnostic),
			QueryMetadata:  qm,
		}
	}

	status := "running"
	message := fmt.Sprintf("Query submitted to %s. Use check_query_status to monitor progress.", providerDisplayName(provider))
	instructions := "Keep this durable job identity and let orchestration continue it; do not submit a duplicate."
	if launchErr != nil {
		qm["monitor_recovery_pending"] = true
		qm["monitor_recovery_detail"] = strings.TrimSpace(launchErr.Error())
	}
	switch job.Status {
	case jobs.JobStatusReserved:
		status = string(jobs.JobStatusReserved)
		message = fmt.Sprintf("%s query is durably reserved and awaiting driver recovery before submission.", providerDisplayName(provider))
		qm["monitor_recovery_pending"] = true
	case jobs.JobStatusSubmissionUnknown:
		status = string(jobs.JobStatusSubmissionUnknown)
		message = fmt.Sprintf("%s submission response was ambiguous. The durable task is reconciling the provider identity and will not blindly resubmit or give up.", providerDisplayName(provider))
		qm["monitor_recovery_pending"] = true
	case jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		status = string(job.Status)
		message = fmt.Sprintf("%s query is in confirmed cancellation recovery. Wait for its terminal state before choosing a replacement.", providerDisplayName(provider))
		instructions = "Do not replace or resubmit while cancellation is unconfirmed; keep this job identity and wait for the durable terminal outcome."
		qm["cancellation_pending"] = true
	case jobs.JobStatusCompleted:
		status = string(jobs.JobStatusCompleted)
		message = fmt.Sprintf("%s query completed. Retrieve and validate the durable results.", providerDisplayName(provider))
		instructions = "Call get_query_results with this job_id and owner_token, then validate the result before finalizing."
	case jobs.JobStatusSubmitted, jobs.JobStatusRunning:
		// Defaults above are the public running state used by the UI.
	}

	qm["status"] = status
	qm["execution_status"] = executionStatus
	qm["replacement_required"] = false
	qm["rewrite_required"] = false

	if exec.OnProgress != nil {
		progress := map[string]any{
			"provider":        provider,
			"phase":           "submitted",
			"state":           executionStatus,
			"job_id":          job.ID,
			"owner_token":     ownerToken,
			"provider_job_id": qm["provider_job_id"],
		}
		if obligationID := firstStringMapValue(qm, "query_obligation_id"); obligationID != "" {
			progress["query_obligation_id"] = obligationID
		}
		exec.OnProgress(progress)
	}
	result := managedQueryIdentityResult(req, provider, job, ownerToken, artifactID, qm)
	result["status"] = status
	result["execution_status"] = executionStatus
	result["message"] = message
	result["instructions"] = instructions
	result["replacement_required"] = false
	result["rewrite_required"] = false
	if value, ok := qm["monitor_recovery_pending"]; ok {
		result["monitor_recovery_pending"] = value
	}
	if value, ok := qm["monitor_recovery_detail"]; ok {
		result["monitor_recovery_detail"] = value
	}
	if value, ok := qm["cancellation_pending"]; ok {
		result["cancellation_pending"] = value
	}
	if description := strings.TrimSpace(req.Description); description != "" {
		result["description"] = description
	}
	return Output{
		OK:             true,
		ResultFormat:   ResultFormatJSON,
		Result:         result,
		ResultMetadata: datasourceAsyncQueryResultMetadata(req.ToolName, provider, qm),
		QueryMetadata:  qm,
	}
}

func stripManagedQueryLegacyRetryMetadata(metadata map[string]any) {
	for _, key := range []string{
		jobs.QueryObligationManagedMetadataKey,
		"query_obligation_id", "queryObligationId", "queryObligationID",
		"replaces_job_id", "replacesJobId", "replacesJobID",
		"attempted_replaces_job_id", "attemptedReplacesJobId", "attemptedReplacesJobID",
		"attempted_retry_target", "replacement_target_job_id", "candidate_job_ids",
	} {
		delete(metadata, key)
	}
}

// cloneManagedQueryMetadataWithoutObligationIDs copies metadata while removing
// run-scoped retry handles and internal provider cursors at every nesting level.
// Durable result artifacts are keyed by thread/job and can be read by later
// runs, so embedding a run-scoped obligation would make an old run's authority
// appear portable. Provider cursors are consumable recovery state and must
// never become a public pagination contract.
func cloneManagedQueryMetadataWithoutObligationIDs(metadata map[string]any) map[string]any {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		if isManagedQueryObligationMetadataKey(key) || isManagedQueryInternalMetadataKey(key) {
			continue
		}
		out[key] = cloneManagedQueryMetadataValueWithoutObligationIDs(value)
	}
	return out
}

func cloneManagedQueryMetadataValueWithoutObligationIDs(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneManagedQueryMetadataWithoutObligationIDs(typed)
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = cloneManagedQueryMetadataValueWithoutObligationIDs(item)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for index, item := range typed {
			out[index] = cloneManagedQueryMetadataWithoutObligationIDs(item)
		}
		return out
	default:
		return value
	}
}

func isManagedQueryObligationMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalized)
	return normalized == "queryobligationid"
}

func isManagedQueryInternalMetadataKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.NewReplacer("_", "", "-", "", " ", "").Replace(normalized)
	return normalized == "resultcursor"
}

// managedQueryObligationID reads the durable run dependency first. The
// dependency ledger is authoritative because it records both detached and
// current attempts after the pre-submit binding transaction. Metadata is only
// a compatibility fallback for old/in-memory jobs.
func managedQueryObligationID(
	ctx context.Context,
	repository store.LongHorizonRepository,
	runID string,
	job *jobs.Job,
	fallbackMetadata map[string]any,
) string {
	if job == nil {
		return ""
	}
	requestedRunID := strings.TrimSpace(runID)
	ownerRunID := strings.TrimSpace(job.Owner.RunID)
	runID = strings.TrimSpace(firstNonEmpty(requestedRunID, ownerRunID))
	jobID := strings.TrimSpace(job.ID)
	if repository != nil && runID != "" && jobID != "" {
		// With repository and run context, the durable run ledger is strictly
		// authoritative: a job that is not registered as a dependency of THIS
		// run has no obligation handle here. Falling back to job metadata or a
		// self-root derivation would leak another run's handle into this run's
		// payloads (an older same-thread job observed from a newer run), and a
		// transient list error must fail closed rather than degrade to that
		// same leak.
		dependencies, err := repository.ListRunQueryDependencies(ctx, runID)
		if err != nil {
			return ""
		}
		for _, dependency := range dependencies {
			if strings.TrimSpace(dependency.JobID) != jobID {
				continue
			}
			obligationID, deriveErr := runQueryDependencyObligationID(ctx, repository, runID, dependency)
			if deriveErr != nil {
				return ""
			}
			// A detached observation may still point at the stable handle for its
			// live successor. Conversely, a validated chain has no retry authority
			// left and must not advertise a handle that preflight will reject.
			for _, candidate := range dependencies {
				if !candidate.RequiredForCompletion || candidate.Detached ||
					strings.EqualFold(strings.TrimSpace(candidate.ValidationState), store.QueryValidationValidated) {
					continue
				}
				candidateID, candidateErr := runQueryDependencyObligationID(ctx, repository, runID, candidate)
				if candidateErr != nil {
					return ""
				}
				if candidateID == obligationID {
					return obligationID
				}
			}
			return ""
		}
		return ""
	}
	// Metadata fallback is permitted only for the job's owning run. Without a
	// repository there is no ledger proof that an obligation belongs to a
	// different caller, and a blank owner run is a deleted-run tombstone rather
	// than an invitation to mint or reuse a handle.
	if requestedRunID == "" || ownerRunID == "" || requestedRunID != ownerRunID {
		return ""
	}
	return managedQueryObligationIDFromJob(job, fallbackMetadata)
}

// managedQueryObligationIDFromJob recovers the stable retry handle for a
// job's payload: explicit metadata first (set at launch for replacements),
// then the self-root derivation for chain roots. Mid-chain legacy jobs with
// no recorded obligation return "" — annotation only, never load-bearing.
func managedQueryObligationIDFromJob(job *jobs.Job, qm map[string]any) string {
	if job != nil {
		if value, ok := job.Metadata["query_obligation_id"].(string); ok {
			value = strings.TrimSpace(value)
			if store.ValidateQueryObligationID(value) == nil {
				return value
			}
		}
	}
	if qm != nil {
		if value, ok := qm["query_obligation_id"].(string); ok {
			value = strings.TrimSpace(value)
			if store.ValidateQueryObligationID(value) == nil {
				return value
			}
		}
	}
	if job == nil {
		return ""
	}
	// Self-root derivation is only truthful for managed obligation jobs: their
	// launch validated the run scope and both stores freeze it durably (a blank
	// RunID is a deleted-run tombstone and must never mint a handle). Generic
	// registry jobs never participate in the obligation protocol, so minting a
	// handle for them would advertise an identity no ledger can resolve.
	if managed, _ := job.Metadata[jobs.QueryObligationManagedMetadataKey].(bool); !managed {
		return ""
	}
	if strings.TrimSpace(job.ReplacesJobID) == "" && strings.TrimSpace(job.ID) != "" {
		if id, err := store.QueryObligationIDForRoot(job.Owner.RunID, job.ID); err == nil {
			return id
		}
	}
	return ""
}

// managedQueryRetryTargetClause names only the obligation already verified for
// the current run. Job metadata can contain the owning run's handle when a
// later run observes the same durable job, so it must never be a fallback for
// model retry instructions.
func managedQueryRetryTargetClause(qm map[string]any) string {
	obligationID := managedQueryObligationIDFromMetadata(qm)
	if obligationID != "" {
		return fmt.Sprintf("query_obligation_id=%s", obligationID)
	}
	return "the verified query_obligation_id returned by check_query_status for this job_id and owner_token (retry the status check if the ledger read is temporarily unavailable; never invent a replacement target)"
}

func managedQueryObligationIDFromMetadata(qm map[string]any) string {
	if qm == nil {
		return ""
	}
	value, ok := qm["query_obligation_id"].(string)
	if !ok {
		return ""
	}
	value = strings.TrimSpace(value)
	if store.ValidateQueryObligationID(value) != nil {
		return ""
	}
	return value
}

func managedQueryIdentityResult(
	req managedQueryLaunchRequest,
	provider string,
	job *jobs.Job,
	ownerToken, artifactID string,
	qm map[string]any,
) map[string]any {
	remoteSubmitted := !job.Handle.IsZero()
	if value, ok := qm["remote_submitted"].(bool); ok {
		remoteSubmitted = value
	}
	result := map[string]any{
		"job_id":             job.ID,
		"provider":           provider,
		"sql":                strings.TrimSpace(req.SQL),
		"sql_preview":        asyncSQLPreview(req.SQL),
		"owner_token":        ownerToken,
		"thread_id":          job.Owner.ThreadID,
		"run_id":             job.Owner.RunID,
		"artifact_id":        artifactID,
		"remote_submitted":   remoteSubmitted,
		"profile_outcome":    string(job.ProfileOutcome),
		"profile_phase":      strings.TrimSpace(job.ProfilePhase),
		"profile_dispatched": job.ProfileDispatched,
	}
	if result["thread_id"] == "" {
		result["thread_id"] = qm["thread_id"]
	}
	if result["run_id"] == "" {
		result["run_id"] = qm["run_id"]
	}
	if job.Profile != nil {
		result["query_profile"] = job.Profile
		result["profile"] = job.Profile
	}
	if !job.ProfileHandle.IsZero() {
		result["profile_handle"] = job.ProfileHandle
	}
	if description := strings.TrimSpace(req.Description); description != "" {
		result["description"] = description
	}
	for _, key := range []string{
		"provider_job_id", "provider_job_url", "warehouse_job_id", "warehouse_location",
		"project_id", "job_location", "statement_id", "query_id", "next_uri",
		"requested_limit", "effective_limit", "configured_limit", "limit_capped",
		"full_data_requested", "max_result_rows", "max_result_bytes", "max_scan_bytes",
		"submit_dispatched", "provider_submission_confirmed", "submission_uncertain",
		"provider_max_result_bytes", "effective_provider_result_byte_limit",
		"monitor_recovery_pending", "monitor_recovery_detail",
		"replacement_target_canonicalized", "query_obligation_id",
	} {
		if value, ok := qm[key]; ok {
			result[key] = value
		}
	}
	return result
}

func managedQueryStatusCanContinue(status jobs.JobStatus) bool {
	switch status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted,
		jobs.JobStatusSubmissionUnknown, jobs.JobStatusRunning,
		jobs.JobStatusCancelRequested, jobs.JobStatusCancelling,
		jobs.JobStatusCompleted:
		return true
	default:
		return false
	}
}

func managedQuerySubmissionUncertain(status jobs.JobStatus, dispatched, confirmed bool) bool {
	if confirmed {
		return false
	}
	switch status {
	case jobs.JobStatusSubmissionUnknown, jobs.JobStatusOrphaned:
		return true
	case jobs.JobStatusReserved, jobs.JobStatusSubmitted, jobs.JobStatusRunning,
		jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return dispatched
	default:
		return false
	}
}

func managedQueryInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int32:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func isRejectedProfileOutcome(outcome jobs.ProfileOutcome) bool {
	switch outcome {
	case jobs.ProfileOutcomeInvalid, jobs.ProfileOutcomeOverBudget, jobs.ProfileOutcomeTimedOut,
		jobs.ProfileOutcomeErrored, jobs.ProfileOutcomeIndeterminate:
		return true
	default:
		return false
	}
}

func isBigQueryBytesBilledLimitError(provider, message string) bool {
	if !strings.EqualFold(strings.TrimSpace(provider), "bigquery") {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(lower, "exceeded limit for bytes billed") ||
		strings.Contains(lower, "maximumbytesbilled") ||
		strings.Contains(lower, "maximum bytes billed")
}

func managedQueryDiagnostic(code, title, kind, message, why, tryThis, failedRequest string) map[string]any {
	return map[string]any{
		"code":           strings.TrimSpace(code),
		"title":          strings.TrimSpace(title),
		"kind":           strings.TrimSpace(kind),
		"message":        strings.TrimSpace(message),
		"why":            strings.TrimSpace(why),
		"try_this":       strings.TrimSpace(tryThis),
		"failed_request": strings.TrimSpace(failedRequest),
	}
}

func managedQueryFailureResultMetadata(toolName, provider string, qm, diagnostic map[string]any) map[string]any {
	extra := cloneMap(qm)
	if extra == nil {
		extra = map[string]any{}
	}
	extra["kind"] = "query_failed"
	extra["status"] = "failed"
	extra["async_query"] = true
	extra["diagnostic"] = diagnostic
	return datasourceToolResultMetadata(toolName, provider, "query_failed", extra)
}

func emitManagedQueryProgress(exec ExecContext, provider, phase string, job *jobs.Job, ownerToken string, qm map[string]any) {
	if exec.OnProgress == nil || job == nil {
		return
	}
	progress := map[string]any{
		"provider":           provider,
		"phase":              strings.TrimSpace(phase),
		"state":              string(job.Status),
		"job_id":             job.ID,
		"owner_token":        ownerToken,
		"remote_submitted":   !job.Handle.IsZero(),
		"submit_dispatched":  job.SubmitDispatched,
		"profile_outcome":    string(job.ProfileOutcome),
		"profile_phase":      strings.TrimSpace(job.ProfilePhase),
		"profile_dispatched": job.ProfileDispatched,
	}
	for _, key := range []string{"provider_job_id", "rewrite_required", "replacement_required", "approval_required", "query_obligation_id"} {
		if value, ok := qm[key]; ok {
			progress[key] = value
		}
	}
	exec.OnProgress(progress)
}

func formatManagedQueryProfileRejection(
	exec ExecContext,
	req managedQueryLaunchRequest,
	provider string,
	job *jobs.Job,
	ownerToken, artifactID string,
	qm map[string]any,
) Output {
	outcome := job.ProfileOutcome
	if outcome == jobs.ProfileOutcomeNone || outcome == jobs.ProfileOutcomeAccepted {
		outcome = jobs.ProfileOutcomeErrored
	}

	message := strings.TrimSpace(job.Error)
	if job.Profile != nil && strings.TrimSpace(job.Profile.Error) != "" {
		message = strings.TrimSpace(job.Profile.Error)
	}
	code := "QUERY_PROFILE_ERRORED"
	title := "Query profile failed"
	kind := "unavailable"
	why := "The provider could not produce a trustworthy pre-submit profile, so the durable job was not submitted remotely."
	tryThis := "Inspect connection and profile diagnostics, gather cheaper metadata if useful, then launch a corrected replacement."
	steps := []string{
		"Inspect the profile diagnostic and relevant catalog/schema metadata.",
		"Rewrite or narrow the SQL so it can be profiled safely.",
		fmt.Sprintf("Launch the next attempt with %s.", managedQueryRetryTargetClause(qm)),
	}
	approvalRequired := false

	switch outcome {
	case jobs.ProfileOutcomeInvalid:
		code = "QUERY_PROFILE_INVALID"
		title = "Query profile rejected the SQL"
		kind = "validation"
		why = "The provider determined that the SQL is invalid before remote execution."
		tryThis = "Inspect catalog/schema metadata, correct the SQL, and launch a replacement tied to this failed job."
		steps = []string{
			"Inspect the referenced catalogs, schemas, tables, and columns.",
			"Correct the SQL using the provider diagnostic.",
			fmt.Sprintf("Launch the corrected query with %s.", managedQueryRetryTargetClause(qm)),
		}
	case jobs.ProfileOutcomeOverBudget:
		code = "QUERY_PROFILE_OVER_BUDGET"
		title = "Query exceeds the scan budget"
		kind = "validation"
		why = "The provider estimate exceeds the configured maximum scan budget, so submission was blocked before cost was incurred."
		tryThis = "Prune partitions and columns, aggregate or sample earlier, or obtain explicit approval for a suitable configured budget; then launch a replacement."
		approvalRequired = true
		qm["budget_exceeded"] = true
		steps = []string{
			"Reduce scanned data using partition predicates, column projection, aggregation, or a representative sample.",
			"If the full scan is materially required, obtain explicit user approval and use an appropriately configured budget.",
			fmt.Sprintf("Launch the revised query with %s.", managedQueryRetryTargetClause(qm)),
		}
	case jobs.ProfileOutcomeApprovalDenied:
		code = "QUERY_SCAN_APPROVAL_NOT_GRANTED"
		title = "Query scan was not approved"
		kind = "approval"
		why = "The query is above the automatic scan limit and was not authorized, so it was not submitted."
		tryThis = "Reduce the scan below the automatic limit, or ask the user whether they want to approve a new attempt."
		steps = []string{
			"Reduce scanned data using partition predicates, column projection, aggregation, or a representative sample.",
			"If the full scan is still required, explain why and launch a fresh attempt so the user can decide.",
			fmt.Sprintf("Tie the next attempt to this durable failure with %s.", managedQueryRetryTargetClause(qm)),
		}
	case jobs.ProfileOutcomeTimedOut:
		code = "QUERY_PROFILE_TIMED_OUT"
		title = "Query profiling timed out"
		kind = "timeout"
		why = "The bounded pre-submit profile did not finish, so the system could not safely characterize the query and did not submit it."
		tryThis = "Use catalog metadata, a bounded sample, or a cheaper preliminary query to reduce uncertainty, then launch a replacement."
		steps = []string{
			"Gather schema, partition, and table-size metadata without running the full query.",
			"Try a bounded sample or narrower preliminary query.",
			fmt.Sprintf("Launch the safer query with %s.", managedQueryRetryTargetClause(qm)),
		}
	case jobs.ProfileOutcomeIndeterminate:
		code = "QUERY_PROFILE_INDETERMINATE"
		title = "Query profile outcome is indeterminate"
		kind = "unavailable"
		why = "Profiling was interrupted and its result cannot be safely inferred after recovery; blindly submitting this attempt could duplicate work or bypass safety checks."
		tryThis = "Gather metadata or run a bounded alternative, then create a fresh replacement rather than re-profiling this durable job."
		steps = []string{
			"Inspect durable profile telemetry and provider metadata.",
			"Choose a bounded alternative or corrected query based on that evidence.",
			fmt.Sprintf("Launch a fresh attempt with %s; do not retry this same job.", managedQueryRetryTargetClause(qm)),
		}
	case jobs.ProfileOutcomeErrored:
		// Defaults above are tailored to provider/transport errors.
	}
	if message == "" {
		message = title + "."
	}

	qm["status"] = string(job.Status)
	qm["execution_status"] = string(job.Status)
	qm["profile_outcome"] = string(outcome)
	qm["preflight_failed"] = true
	qm["profile_failed"] = true
	qm["remote_submitted"] = false
	qm["replacement_required"] = true
	qm["rewrite_required"] = true
	qm["approval_required"] = approvalRequired
	if job.Profile != nil {
		qm["query_profile"] = job.Profile
		qm["profile"] = job.Profile
		if job.Profile.EstimatedBytes != nil {
			qm["estimated_bytes"] = *job.Profile.EstimatedBytes
			qm["estimate_is_exact"] = job.Profile.EstimateIsExact
		}
	}

	result := managedQueryIdentityResult(req, provider, job, ownerToken, artifactID, qm)
	result["status"] = string(job.Status)
	result["execution_status"] = string(job.Status)
	result["error"] = message
	result["preflight_failed"] = true
	result["profile_failed"] = true
	result["remote_submitted"] = false
	result["profile_outcome"] = string(outcome)
	result["replacement_required"] = true
	result["rewrite_required"] = true
	result["approval_required"] = approvalRequired
	if outcome == jobs.ProfileOutcomeOverBudget {
		result["budget_exceeded"] = true
	}
	result["instructions"] = tryThis + fmt.Sprintf(" Preserve this durable failure as evidence and use %s; do not abandon the task.", managedQueryRetryTargetClause(qm))
	alternativePlan := map[string]any{
		"action": "profile_and_run_replacement_query",
		"steps":  steps,
	}
	if obligationID := managedQueryObligationIDFromMetadata(qm); obligationID != "" {
		alternativePlan["query_obligation_id"] = obligationID
	}
	result["alternative_plan"] = alternativePlan
	if job.Profile != nil && job.Profile.EstimatedBytes != nil {
		result["estimated_bytes"] = *job.Profile.EstimatedBytes
		result["estimate_is_exact"] = job.Profile.EstimateIsExact
	}
	if job.Budgets.MaxScanBytes > 0 {
		result["maximum_scan_bytes"] = job.Budgets.MaxScanBytes
		qm["maximum_scan_bytes"] = job.Budgets.MaxScanBytes
	}
	if job.Budgets.AutomaticScanBytes > 0 {
		result["automatic_scan_bytes"] = job.Budgets.AutomaticScanBytes
		qm["automatic_scan_bytes"] = job.Budgets.AutomaticScanBytes
	}
	diagnostic := managedQueryDiagnostic(code, title, kind, message, why, tryThis, asyncSQLPreview(req.SQL))
	emitManagedQueryProgress(exec, provider, "failed", job, ownerToken, qm)
	return Output{
		OK:             false,
		ResultFormat:   ResultFormatJSON,
		Result:         result,
		ResultMetadata: managedQueryFailureResultMetadata(req.ToolName, provider, qm, diagnostic),
		QueryMetadata:  qm,
	}
}

func managedQueryIdempotencyKey(exec ExecContext, provider string) (string, error) {
	runID := strings.TrimSpace(exec.RunID)
	toolCallID := strings.TrimSpace(exec.ToolCallID)
	if runID != "" && toolCallID != "" {
		digest := sha256.Sum256([]byte(runID + "\x00" + toolCallID + "\x00" + strings.TrimSpace(strings.ToLower(provider))))
		return "tool_" + hex.EncodeToString(digest[:]), nil
	}
	token, err := jobs.GenerateOwnerToken()
	if err != nil {
		return "", err
	}
	return "tool_" + strings.TrimPrefix(token, "qown_"), nil
}

type managedQueryReplacementResolution struct {
	Record            jobs.JobRecord
	RequestThreadID   string
	RequestRunID      string
	Canonicalized     bool
	Active            bool
	Retryable         bool
	SelectionRequired bool
	CandidateJobIDs   []string
	// ObligationID is the stable retry handle (qob-<digest>) of the
	// resolved target, when it could be derived. Annotation only — resolution
	// success never depends on it.
	ObligationID string
}

// forwardResolveQueryDependencyChain follows detached dependency rows'
// replacement links to the chain's current attempt within one run. Any job in
// a superseded prefix therefore targets the same live tail the backend
// actually owes the run, instead of dead-ending on a superseded row.
func forwardResolveQueryDependencyChain(dependencies []store.RunQueryDependency, jobID string) string {
	jobID = strings.TrimSpace(jobID)
	byJob := make(map[string]store.RunQueryDependency, len(dependencies))
	for _, dependency := range dependencies {
		byJob[strings.TrimSpace(dependency.JobID)] = dependency
	}
	seen := make(map[string]bool, 4)
	current := jobID
	for hops := 0; hops < 64; hops++ {
		if current == "" || seen[current] {
			return current
		}
		seen[current] = true
		row, ok := byJob[current]
		if !ok || !row.Detached || strings.TrimSpace(row.ReplacementJobID) == "" {
			return current
		}
		current = strings.TrimSpace(row.ReplacementJobID)
	}
	return current
}

// resolveManagedQueryRetryTarget funnels the public obligation handle and the
// decode-only legacy app-job field into one canonical (obligation, active
// attempt) pair. Provider-native handles are never accepted.
func resolveManagedQueryRetryTarget(
	ctx context.Context,
	repository store.LongHorizonRepository,
	threadID, runID, obligationID, replacesJobID string,
) (managedQueryReplacementResolution, error) {
	resolution := managedQueryReplacementResolution{
		RequestThreadID: strings.TrimSpace(threadID),
		RequestRunID:    strings.TrimSpace(runID),
	}
	if repository == nil {
		return resolution, errors.New("durable query dependency repository is not configured")
	}
	runID = strings.TrimSpace(runID)
	obligationID = strings.TrimSpace(obligationID)
	replacesJobID = strings.TrimSpace(replacesJobID)
	if obligationID == "" && replacesJobID == "" {
		return resolution, errors.New("query_obligation_id is required when replacing unfinished query work")
	}
	dependencies, listErr := repository.ListRunQueryDependencies(ctx, runID)
	if listErr != nil {
		resolution.Retryable = true
		return resolution, listErr
	}

	resolveObligation := func(id string) (managedQueryReplacementResolution, error) {
		resolved := managedQueryReplacementResolution{
			RequestThreadID: strings.TrimSpace(threadID),
			RequestRunID:    runID,
		}
		if err := store.ValidateQueryObligationID(id); err != nil {
			return resolved, err
		}
		activeJobID := ""
		matched := false
		for _, dependency := range dependencies {
			candidateID, err := runQueryDependencyObligationID(ctx, repository, runID, dependency)
			if err != nil {
				resolved.Retryable = true
				return resolved, err
			}
			if candidateID != id {
				continue
			}
			matched = true
			if dependency.RequiredForCompletion && !dependency.Detached {
				if activeJobID != "" && activeJobID != dependency.JobID {
					return resolved, fmt.Errorf("query obligation %s has multiple active attempts: %w", id, store.ErrConflict)
				}
				activeJobID = strings.TrimSpace(dependency.JobID)
			}
		}
		if !matched || activeJobID == "" {
			return resolved, fmt.Errorf("query_obligation_id %s is not an active obligation of the current run", id)
		}
		resolved, err := resolveManagedQueryReplacementTarget(ctx, repository, threadID, runID, activeJobID)
		resolved.ObligationID = id
		return resolved, err
	}

	var obligationResolution managedQueryReplacementResolution
	if obligationID != "" {
		var err error
		obligationResolution, err = resolveObligation(obligationID)
		if err != nil {
			return obligationResolution, err
		}
	}

	var legacyResolution managedQueryReplacementResolution
	var legacyObligationID string
	if replacesJobID != "" {
		if _, err := validateManagedReplacementJobID(replacesJobID); err != nil {
			return legacyResolution, err
		}
		canonicalJobID := forwardResolveQueryDependencyChain(dependencies, replacesJobID)
		matched := false
		for _, dependency := range dependencies {
			if strings.TrimSpace(dependency.JobID) != strings.TrimSpace(canonicalJobID) &&
				strings.TrimSpace(dependency.JobID) != replacesJobID {
				continue
			}
			matched = true
			var err error
			legacyObligationID, err = runQueryDependencyObligationID(ctx, repository, runID, dependency)
			if err != nil {
				legacyResolution.Retryable = true
				return legacyResolution, err
			}
			break
		}
		if !matched {
			return legacyResolution, fmt.Errorf("replaces_job_id %s is not attached to the current run", replacesJobID)
		}
		if !matched || legacyObligationID == "" {
			return legacyResolution, fmt.Errorf("replaces_job_id %s is not attached to the current run", replacesJobID)
		}
		var err error
		legacyResolution, err = resolveObligation(legacyObligationID)
		if err != nil {
			return legacyResolution, err
		}
	}

	if obligationID != "" && replacesJobID != "" {
		if legacyObligationID != obligationID || legacyResolution.Record.ID != obligationResolution.Record.ID {
			return resolution, fmt.Errorf("query_obligation_id and replaces_job_id identify different current-run obligations")
		}
		return obligationResolution, nil
	}
	if obligationID != "" {
		return obligationResolution, nil
	}
	return legacyResolution, nil
}

func runQueryDependencyObligationID(
	ctx context.Context,
	repository store.LongHorizonRepository,
	runID string,
	dependency store.RunQueryDependency,
) (string, error) {
	if id := strings.TrimSpace(dependency.ObligationID); id != "" {
		if err := store.ValidateQueryObligationID(id); err != nil {
			return "", fmt.Errorf("dependency %s has invalid query obligation identity: %w", dependency.JobID, err)
		}
		return id, nil
	}
	return store.DeriveQueryObligationID(ctx, repository, runID, dependency.JobID)
}

func formatManagedQueryReplacementPreflightFailure(resolution managedQueryReplacementResolution) Output {
	result := map[string]any{
		"error":                        "The query retry target could not be verified against the current run's durable obligation ledger.",
		"replacement_preflight_failed": true,
		"replacement_wait_required":    true,
		"rewrite_required":             true,
		"instructions":                 "Use the exact stable query_obligation_id (qob-...) returned by the query tools. Never substitute statement_id, provider_job_id, query_id, a profile handle, or a guessed value. The existing obligation remains unfinished so the task can continue safely.",
	}
	verifiedObligationID := strings.TrimSpace(resolution.ObligationID)
	if store.ValidateQueryObligationID(verifiedObligationID) == nil {
		result["query_obligation_id"] = verifiedObligationID
	}
	if resolution.Active && strings.TrimSpace(resolution.Record.ID) != "" {
		requestThreadID := strings.TrimSpace(resolution.RequestThreadID)
		if requestThreadID == "" {
			requestThreadID = strings.TrimSpace(resolution.Record.Owner.ThreadID)
		}
		requestRunID := strings.TrimSpace(resolution.RequestRunID)
		if requestRunID == "" {
			requestRunID = strings.TrimSpace(resolution.Record.Owner.RunID)
		}
		result["job_id"] = resolution.Record.ID
		result["owner_token"] = strings.TrimSpace(resolution.Record.Owner.Handle)
		result["provider"] = strings.TrimSpace(resolution.Record.Provider)
		// The obligation is scoped to the requesting/observing run, which may
		// be waiting on an older same-thread job. Pairing its qob with the job
		// owner's prior run makes frontend status calls fail run-scope checks.
		result["thread_id"] = requestThreadID
		result["run_id"] = requestRunID
		result["status"] = string(resolution.Record.Status)
		result["rewrite_required"] = false
		result["replacement_wait_required"] = true
		result["instructions"] = "The canonical target is still active or recoverable. Keep its job_id and owner_token, wait or check its status, and do not launch a replacement yet."
	} else if resolution.Retryable {
		result["rewrite_required"] = false
		result["replacement_wait_required"] = true
		result["replacement_preflight_retry_required"] = true
		result["instructions"] = "The durable replacement ledger could not be read reliably. Do not submit or replace anything yet; retry this same preflight after durable state is available."
	} else if resolution.SelectionRequired {
		result["rewrite_required"] = false
		result["replacement_wait_required"] = true
		result["instructions"] = "The supplied target is ambiguous or already superseded. Re-read the current run's query output and retry with its exact query_obligation_id; do not submit a duplicate."
	}
	queryMetadata := map[string]any{
		"replacement_preflight_failed": true,
		"replacement_wait_required":    true,
	}
	if store.ValidateQueryObligationID(verifiedObligationID) == nil {
		queryMetadata["query_obligation_id"] = verifiedObligationID
	}
	return Output{OK: false, ResultFormat: ResultFormatJSON, Result: result, QueryMetadata: queryMetadata}
}

// resolveManagedQueryReplacementTarget accepts only an exact app-managed job
// id. Provider/profile handles deliberately live in a different namespace and
// are never aliases for durable retry ownership.
func resolveManagedQueryReplacementTarget(
	ctx context.Context,
	repository store.LongHorizonRepository,
	threadID, runID, replacesJobID string,
) (managedQueryReplacementResolution, error) {
	resolution := managedQueryReplacementResolution{
		RequestThreadID: strings.TrimSpace(threadID),
		RequestRunID:    strings.TrimSpace(runID),
	}
	if repository == nil {
		return resolution, errors.New("durable query dependency repository is not configured")
	}
	threadID = strings.TrimSpace(threadID)
	runID = strings.TrimSpace(runID)
	replacesJobID = strings.TrimSpace(replacesJobID)
	if threadID == "" || runID == "" || replacesJobID == "" {
		return resolution, errors.New("thread, run, and replaces_job_id are required for a replacement query")
	}
	var dependencies []store.RunQueryDependency
	dependenciesLoaded := false
	loadDependencies := func() error {
		if dependenciesLoaded {
			return nil
		}
		var loadErr error
		dependencies, loadErr = repository.ListRunQueryDependencies(ctx, runID)
		if loadErr != nil {
			resolution.Retryable = true
			return loadErr
		}
		dependenciesLoaded = true
		return nil
	}
	attachedDependency := func(jobID string) *store.RunQueryDependency {
		jobID = strings.TrimSpace(jobID)
		for index := range dependencies {
			dependency := &dependencies[index]
			if strings.TrimSpace(dependency.JobID) == jobID && dependency.RequiredForCompletion && !dependency.Detached {
				return dependency
			}
		}
		return nil
	}
	record, ok, err := repository.GetQueryJob(ctx, replacesJobID)
	if err != nil {
		resolution.Retryable = true
		return resolution, err
	}
	if ok {
		if strings.TrimSpace(record.Owner.ThreadID) != threadID {
			return resolution, fmt.Errorf("replacement target %s belongs to a different thread; use an app-managed job_id from this thread", replacesJobID)
		}
		if strings.TrimSpace(record.Owner.RunID) != runID {
			if managedQueryReplacementTargetNeedsObservation(record) {
				resolution.Record = record
				resolution.Active = true
				return resolution, fmt.Errorf("replacement target %s belongs to an earlier run and is still active or requires result validation", replacesJobID)
			}
			if err := loadDependencies(); err != nil {
				return resolution, err
			}
			if attachedDependency(record.ID) == nil {
				return resolution, fmt.Errorf("replacement target %s belongs to a different run and is not an attached dependency of the current run", replacesJobID)
			}
		}
	} else {
		return resolution, fmt.Errorf("replacement target %s is not an exact app-managed qj-* id in the durable job store; provider-native ids and profile handles are not accepted", replacesJobID)
	}
	resolution.Record = record
	eligible := record.ValidationState == jobs.ValidationRejected ||
		(record.ProfileOutcome != jobs.ProfileOutcomeNone && record.ProfileOutcome != jobs.ProfileOutcomeAccepted)
	switch record.Status {
	case jobs.JobStatusFailed, jobs.JobStatusCancelled, jobs.JobStatusExpired, jobs.JobStatusOrphaned:
		eligible = true
	}
	if !eligible && managedQueryReplacementTargetMayBeLive(record.Status) {
		resolution.Active = true
		return resolution, fmt.Errorf("replacement target %s is still active or recoverable and must not be replaced", record.ID)
	}
	if err := loadDependencies(); err != nil {
		return resolution, err
	}
	attached := false
	foundDependency := false
	var matchedDependency *store.RunQueryDependency
	for _, dependency := range dependencies {
		if strings.TrimSpace(dependency.JobID) != strings.TrimSpace(record.ID) {
			continue
		}
		foundDependency = true
		dependencyCopy := dependency
		matchedDependency = &dependencyCopy
		if strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationRejected) {
			eligible = true
		}
		break
	}
	if !eligible {
		resolution.Active = true
		return resolution, fmt.Errorf("replacement target %s must be validated or explicitly rejected before replacement", record.ID)
	}
	if matchedDependency != nil {
		if matchedDependency.RequiredForCompletion && !matchedDependency.Detached {
			attached = true
		}
		if matchedDependency.Detached || !matchedDependency.RequiredForCompletion {
			resolution.SelectionRequired = true
			if replacementID := strings.TrimSpace(matchedDependency.ReplacementJobID); replacementID != "" {
				resolution.CandidateJobIDs = []string{replacementID}
			}
			return resolution, fmt.Errorf("replacement target %s is already detached or superseded", record.ID)
		}
	}
	if !foundDependency {
		if strings.TrimSpace(record.Owner.RunID) != runID {
			return resolution, fmt.Errorf("replacement target %s belongs to a different run and is not an attached dependency of the current run", record.ID)
		}
		validationState := store.QueryValidationUnvalidated
		if record.ValidationState == jobs.ValidationRejected ||
			(record.ProfileOutcome != jobs.ProfileOutcomeNone && record.ProfileOutcome != jobs.ProfileOutcomeAccepted) {
			validationState = store.QueryValidationRejected
		}
		now := time.Now().UTC()
		if _, repairErr := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
			RunID: runID, JobID: record.ID, RequiredForCompletion: true,
			ValidationState: validationState, CreatedAt: now, UpdatedAt: now,
		}); repairErr != nil {
			resolution.Retryable = true
			return resolution, fmt.Errorf("repair missing replacement dependency %s: %w", record.ID, repairErr)
		}
		attached = true
	}
	if !attached {
		return resolution, fmt.Errorf("replacement target %s is not an attached required query dependency", record.ID)
	}
	return resolution, nil
}

func managedQueryReplacementTargetMayBeLive(status jobs.JobStatus) bool {
	switch status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted,
		jobs.JobStatusSubmissionUnknown, jobs.JobStatusRunning,
		jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return true
	default:
		return false
	}
}

func managedQueryReplacementTargetNeedsObservation(record jobs.JobRecord) bool {
	if managedQueryReplacementTargetMayBeLive(record.Status) {
		return true
	}
	return record.Status == jobs.JobStatusCompleted && record.ValidationState != jobs.ValidationValidated && record.ValidationState != jobs.ValidationRejected
}

func addNativeQueryMetadata(metadata map[string]any, job *jobs.Job) {
	if metadata == nil || job == nil {
		return
	}
	if nativeID := strings.TrimSpace(job.Handle.ID); nativeID != "" {
		metadata["provider_job_id"] = nativeID
		switch strings.TrimSpace(strings.ToLower(job.Provider)) {
		case "bigquery":
			metadata["job_id_native"] = nativeID
			metadata["warehouse_job_id"] = nativeID
		case "databricks":
			metadata["statement_id"] = nativeID
		case "snowflake":
			metadata["query_id"] = nativeID
			metadata["statement_handle"] = nativeID
		case "trino":
			metadata["query_id"] = nativeID
		}
	}
	if location := strings.TrimSpace(job.Handle.Location); location != "" {
		metadata["warehouse_location"] = location
		metadata["job_location"] = location
	}
	for key, value := range job.Handle.Extra {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	if provider := strings.TrimSpace(strings.ToLower(job.Provider)); provider == "bigquery" {
		if projectID := strings.TrimSpace(anyToString(metadata["project_id"])); projectID != "" {
			metadata["provider_job_url"] = bigqueryJobConsoleURL(projectID, strings.TrimSpace(job.Handle.Location), strings.TrimSpace(job.Handle.ID))
		}
	} else if provider == "databricks" {
		host := strings.TrimSpace(anyToString(metadata["host"]))
		if host != "" && strings.TrimSpace(job.Handle.ID) != "" {
			metadata["provider_job_url"] = databricksQueryHistoryURL(host, job.Handle.ID)
		}
	} else if value := strings.TrimSpace(anyToString(metadata["info_uri"])); value != "" {
		metadata["provider_job_url"] = value
	}
}

func providerDisplayName(provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case "bigquery":
		return "BigQuery"
	case "databricks":
		return "Databricks"
	case "snowflake":
		return "Snowflake"
	case "trino":
		return "Trino"
	default:
		return provider
	}
}

func jobsBudgets(maxRows int, maxScanBytes int64) jobs.Budgets {
	return jobsScanBudgets(maxRows, maxScanBytes, maxScanBytes)
}

func jobsScanBudgets(maxRows int, automaticScanBytes, approvedScanBytes int64) jobs.Budgets {
	if maxRows <= 0 {
		maxRows = 2500
	}
	return jobs.Budgets{
		MaxResultRows:      maxRows,
		AutomaticScanBytes: automaticScanBytes,
		MaxScanBytes:       approvedScanBytes,
	}
}

// preSubmittedCompatibilityAdapter exists only for local HTTP test doubles
// that expose the historical combined submit/poll endpoint instead of the
// provider-native clients. Production handlers return earlier through the
// four queryadapters and never use this path. Even here the registry lifecycle
// is Launch-based (confirmed cancellation, durable status, no run sweep).
type preSubmittedCompatibilityAdapter struct {
	provider string
	handle   jobs.NativeHandle
	poll     jobs.PollFunc
	cancel   jobs.CancelFunc

	mu        sync.Mutex
	result    *jobs.QueryResult
	cancelled bool
}

func (a *preSubmittedCompatibilityAdapter) Provider() string { return a.provider }
func (*preSubmittedCompatibilityAdapter) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{IdempotentSubmit: true, CancelConfirmable: true}
}
func (a *preSubmittedCompatibilityAdapter) Submit(context.Context, jobs.SubmitRequest) (jobs.SubmitResult, error) {
	return jobs.SubmitResult{Handle: a.handle}, nil
}
func (a *preSubmittedCompatibilityAdapter) Poll(ctx context.Context, _ jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	a.mu.Lock()
	if a.cancelled {
		a.mu.Unlock()
		return jobs.StatusSnapshot{State: jobs.ProviderStateCancelled}, nil
	}
	a.mu.Unlock()
	done, result, err := a.poll(ctx)
	if err != nil {
		return jobs.StatusSnapshot{}, err
	}
	if !done {
		return jobs.StatusSnapshot{State: jobs.ProviderStateRunning}, nil
	}
	a.mu.Lock()
	a.result = result
	a.mu.Unlock()
	state := jobs.ProviderStateSucceeded
	if result != nil && result.Metadata != nil {
		terminal := strings.ToLower(strings.TrimSpace(anyToString(result.Metadata["terminal_status"])))
		rawState := strings.ToLower(strings.TrimSpace(anyToString(result.Metadata["state"])))
		switch {
		case strings.Contains(terminal, "cancel") || strings.Contains(rawState, "cancel"):
			state = jobs.ProviderStateCancelled
		case strings.Contains(terminal, "fail") || strings.Contains(rawState, "fail") || strings.Contains(rawState, "error"):
			state = jobs.ProviderStateFailed
		}
	}
	return jobs.StatusSnapshot{State: state}, nil
}
func (a *preSubmittedCompatibilityAdapter) Fetch(context.Context, jobs.NativeHandle, jobs.FetchCursor) (jobs.ResultPage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.result == nil {
		return jobs.ResultPage{}, nil
	}
	return jobs.ResultPage{Rows: a.result.Rows, Schema: a.result.Schema, Metadata: a.result.Metadata}, nil
}
func (a *preSubmittedCompatibilityAdapter) Cancel(ctx context.Context, _ jobs.NativeHandle) error {
	if a.cancel != nil {
		if err := a.cancel(ctx); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.cancelled = true
	a.mu.Unlock()
	return nil
}
func (a *preSubmittedCompatibilityAdapter) ConfirmCancel(context.Context, jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelled, jobs.StatusSnapshot{State: jobs.ProviderStateCancelled}, nil
}
func (*preSubmittedCompatibilityAdapter) Adopt(context.Context, jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	return jobs.AdoptOutcomeOrphaned, jobs.StatusSnapshot{Error: "local compatibility query cannot be adopted after restart"}, nil
}

func launchPreSubmittedCompatibility(
	ctx context.Context,
	registry *jobs.Registry,
	owner jobs.JobOwner,
	provider, sql, sqlPreview string,
	metadata map[string]any,
	poll jobs.PollFunc,
	cancel jobs.CancelFunc,
) (*jobs.Job, error) {
	providerID := firstNonEmpty(
		anyToString(metadata["provider_job_id"]),
		anyToString(metadata["statement_id"]),
		anyToString(metadata["query_id"]),
		anyToString(metadata["next_uri"]),
	)
	if providerID == "" {
		token, err := jobs.GenerateOwnerToken()
		if err != nil {
			return nil, err
		}
		providerID = "compat_" + strings.TrimPrefix(token, "qown_")
	}
	adapter := &preSubmittedCompatibilityAdapter{
		provider: provider,
		handle:   jobs.NativeHandle{Provider: provider, ID: providerID},
		poll:     poll,
		cancel:   cancel,
	}
	return registry.Launch(ctx, jobs.LaunchRequest{
		Owner:      owner,
		Provider:   provider,
		Adapter:    adapter,
		SQL:        sql,
		SQLPreview: sqlPreview,
		Metadata:   metadata,
	})
}

func queryLifecycleResultMetadata(toolName, kind, provider, status string, extra map[string]any) map[string]any {
	meta := map[string]any{
		"provider":  "query-lifecycle",
		"product":   "query",
		"family":    "query-lifecycle",
		"kind":      kind,
		"tool_name": toolName,
		"readonly":  toolName != "cancel_query" && toolName != "validate_query_result",
	}
	if provider = strings.TrimSpace(provider); provider != "" {
		meta["query_provider"] = provider
	}
	if status = strings.TrimSpace(status); status != "" {
		meta["status"] = status
	}
	if toolMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		meta["connector_id"] = toolMeta.ConnectorID
		meta["category"] = string(toolMeta.Category)
		meta["origin"] = string(toolMeta.Origin)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := toolPresentationMetadata(toolMeta.Presentation)
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
			if v := firstStringMapValue(presentation, "result_kind"); v != "" {
				meta["result_kind"] = v
			}
			if v := firstStringMapValue(presentation, "native_viewer"); v != "" {
				meta["native_viewer"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_strategy"); v != "" {
				meta["renderer_strategy"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_key"); v != "" {
				meta["renderer_key"] = v
			}
			if v := firstStringMapValue(presentation, "file_capability"); v != "" {
				meta["file_capability"] = v
			}
		}
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		meta[key] = value
	}
	if toolName == "validate_query_result" {
		meta["warehouse_readonly"] = true
		meta["internal_validation_write"] = true
	}
	return meta
}

func queryLifecycleResultMetadataForJob(toolName, kind string, job *jobs.Job, status string, extra map[string]any) map[string]any {
	provider := ""
	var jobMeta map[string]any
	identityExtra := cloneMap(extra)
	if identityExtra == nil {
		identityExtra = map[string]any{}
	}
	if job != nil {
		provider = job.Provider
		if strings.TrimSpace(status) == "" {
			status = string(job.Status)
		}
		// A replacement job may retain the originating run's obligation in
		// durable metadata. Only the caller-provided extra map may reintroduce
		// an obligation after the current run's ledger lookup has verified it.
		jobMeta = cloneManagedQueryMetadataWithoutObligationIDs(job.Metadata)
		identityExtra["job_id"] = strings.TrimSpace(job.ID)
		identityExtra["thread_id"] = strings.TrimSpace(job.Owner.ThreadID)
		identityExtra["owner_token"] = strings.TrimSpace(job.Owner.Handle)
		artifactID := strings.TrimSpace(job.ArtifactID)
		if artifactID == "" {
			artifactID = deterministicAsyncArtifactID(job.Owner.ThreadID, job.ID)
		}
		if artifactID != "" {
			identityExtra["artifact_id"] = artifactID
		}
	}
	return queryLifecycleResultMetadata(toolName, kind, provider, status, queryLifecycleMetadataExtras(jobMeta, identityExtra))
}

func queryLifecycleResultMetadataForQuery(toolName, kind, provider, status string, queryMetadata map[string]any, extra map[string]any) map[string]any {
	return queryLifecycleResultMetadata(toolName, kind, provider, status, queryLifecycleMetadataExtras(queryMetadata, extra))
}

func queryLifecycleMetadataExtras(source map[string]any, extra map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		"query_obligation_id",
		"provider_job_id",
		"provider_job_url",
		"warehouse_job_id",
		"statement_id",
		"query_id",
		"next_uri",
	} {
		if source == nil {
			continue
		}
		if value, ok := source[key]; ok {
			out[key] = value
		}
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	return out
}

func newCheckQueryStatusTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "check_query_status",
				Description: "Check the status of a previously submitted async query.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{
							"type":        "string",
							"description": "Async query job ID.",
						},
						"owner_token": map[string]any{
							"type":        "string",
							"description": "Opaque owner token returned when the query was submitted.",
						},
					},
					"required":             []string{"job_id", "owner_token"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Jobs == nil {
				return JSONError("Async query registry is not configured."), nil
			}
			var in struct {
				JobID      string `json:"job_id"`
				OwnerToken string `json:"owner_token"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			jobID := strings.TrimSpace(in.JobID)
			ownerToken := strings.TrimSpace(in.OwnerToken)
			if jobID == "" || ownerToken == "" {
				return JSONError("job_id and owner_token are required"), nil
			}

			owner := jobs.JobOwner{
				ThreadID: exec.ThreadID,
				RunID:    exec.RunID,
				Handle:   ownerToken,
			}
			_ = exec.Jobs.AttachOwner(jobID, owner)
			job, ok := exec.Jobs.Status(jobID, owner)
			if !ok {
				result := map[string]any{
					"status":      string(jobs.JobStatusUnknown),
					"job_id":      jobID,
					"owner_token": ownerToken,
					"message":     "Query handle was not found for this thread and owner token.",
					"ui_suppress": true,
				}
				return JSONOKWithMetadata(result, queryLifecycleResultMetadata("check_query_status", "query_status", "", string(jobs.JobStatusUnknown), map[string]any{
					"job_id": jobID,
					"found":  false,
				})), nil
			}

			status := strings.TrimSpace(string(job.Status))
			if status == "" {
				status = string(jobs.JobStatusUnknown)
			}
			elapsed := time.Since(job.SubmittedAt).Milliseconds()
			if elapsed < 0 {
				elapsed = 0
			}
			result := map[string]any{
				"status":      status,
				"job_id":      job.ID,
				"owner_token": ownerToken,
				"provider":    job.Provider,
				"elapsed_ms":  elapsed,
				"message":     queryStatusMessage(job.Status, elapsed),
				"ui_suppress": true,
			}
			for key, value := range queryJobLifecycleDetails(job) {
				result[key] = value
			}
			if obligationID := managedQueryObligationID(ctx, exec.LongHorizon, exec.RunID, job, nil); obligationID != "" {
				result["query_obligation_id"] = obligationID
			}
			validation, validationErr := loadDurableQueryValidation(ctx, exec.LongHorizon, exec.RunID, job.ID)
			applyDurableValidationFields(result, validation)
			if validationErr != nil {
				result["validation_read_error"] = validationErr.Error()
			}
			if strings.TrimSpace(job.Error) != "" {
				result["error"] = strings.TrimSpace(job.Error)
			}
			if job.Status == jobs.JobStatusFailed && isBigQueryBytesBilledLimitError(job.Provider, job.Error) {
				result["message"] = "BigQuery's execution-time estimate exceeded the approved scan cap. Re-profile this same SQL as a linked replacement; do not abandon the task."
				result["retryable"] = true
				result["replacement_required"] = true
				result["rewrite_required"] = false
				result["scan_budget_reprofile_required"] = true
				result["instructions"] = fmt.Sprintf(
					"Launch the same SQL as a linked replacement with %s. The replacement will obtain a fresh estimate and request supplemental approval if needed; do not retry the failed provider job ID.",
					managedQueryRetryTargetClause(result),
				)
			}
			statusMetadata := queryJobLifecycleDetails(job)
			if obligationID := firstStringMapValue(result, "query_obligation_id"); obligationID != "" {
				statusMetadata["query_obligation_id"] = obligationID
			}
			statusMetadata["job_id"] = job.ID
			statusMetadata["elapsed_ms"] = elapsed
			statusMetadata["found"] = true
			applyDurableValidationFields(statusMetadata, validation)
			if validationErr != nil {
				statusMetadata["validation_read_error"] = validationErr.Error()
			}
			return JSONOKWithMetadata(result, queryLifecycleResultMetadataForJob("check_query_status", "query_status", job, status, statusMetadata)), nil
		},
	}
}

func newGetQueryResultsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_query_results",
				Description: "Retrieve results for a completed async query. Retrieval does not validate results; call validate_query_result after checking them.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{
							"type":        "string",
							"description": "Async query job ID.",
						},
						"owner_token": map[string]any{
							"type":        "string",
							"description": "Opaque owner token returned when the query was submitted.",
						},
						"limit": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"maximum":     maxQueryPageSize,
							"description": "Maximum rows to return. Defaults to the configured query row limit.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque pagination token from the previous get_query_results call.",
						},
					},
					"required":             []string{"job_id", "owner_token"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Jobs == nil {
				return JSONError("Async query registry is not configured."), nil
			}
			var in struct {
				JobID      string `json:"job_id"`
				OwnerToken string `json:"owner_token"`
				Limit      int    `json:"limit,omitempty"`
				PageToken  string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			jobID := strings.TrimSpace(in.JobID)
			ownerToken := strings.TrimSpace(in.OwnerToken)
			if jobID == "" || ownerToken == "" {
				return JSONError("job_id and owner_token are required"), nil
			}

			owner := jobs.JobOwner{
				ThreadID: exec.ThreadID,
				RunID:    exec.RunID,
				Handle:   ownerToken,
			}
			_ = exec.Jobs.AttachOwner(jobID, owner)
			job, ok := exec.Jobs.Snapshot(jobID, owner)
			if !ok {
				return JSONError("Job not found for this thread and owner token."), nil
			}
			if job.Status != jobs.JobStatusCompleted {
				elapsed := time.Since(job.SubmittedAt).Milliseconds()
				if elapsed < 0 {
					elapsed = 0
				}
				result := map[string]any{
					"job_id":         job.ID,
					"owner_token":    ownerToken,
					"status":         strings.TrimSpace(string(job.Status)),
					"provider":       job.Provider,
					"elapsed_ms":     elapsed,
					"message":        fmt.Sprintf("Query is not completed yet (status=%s). Call check_query_status and retry only after completion.", job.Status),
					"ui_suppress":    true,
					"ui_hidden":      true,
					"retryable":      true,
					"retry_after_ms": 1500,
				}
				if strings.TrimSpace(job.Error) != "" {
					result["error"] = strings.TrimSpace(job.Error)
				}
				pendingMetadata := map[string]any{
					"job_id":         job.ID,
					"elapsed_ms":     elapsed,
					"retryable":      true,
					"retry_after_ms": 1500,
				}
				if obligationID := managedQueryObligationID(ctx, exec.LongHorizon, exec.RunID, job, nil); obligationID != "" {
					result["query_obligation_id"] = obligationID
					pendingMetadata["query_obligation_id"] = obligationID
				}
				validation, validationErr := loadDurableQueryValidation(ctx, exec.LongHorizon, exec.RunID, job.ID)
				applyDurableValidationFields(result, validation)
				applyDurableValidationFields(pendingMetadata, validation)
				if validationErr != nil {
					result["validation_read_error"] = validationErr.Error()
					pendingMetadata["validation_read_error"] = validationErr.Error()
				}
				return JSONOKWithMetadata(result, queryLifecycleResultMetadataForJob("get_query_results", "query_pending", job, strings.TrimSpace(string(job.Status)), pendingMetadata)), nil
			}

			result, err := exec.Jobs.Results(jobID, owner)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if result == nil {
				result = &jobs.QueryResult{}
			}
			validation, validationErr := loadDurableQueryValidation(ctx, exec.LongHorizon, exec.RunID, job.ID)
			validationRequired := validation.State != jobs.ValidationValidated
			validationInstructions := durableValidationInstructions(validation)

			requestedLimit, pageSize, isCapped, effectiveConfiguredLimit := resolveQueryPageSize(in.Limit, exec.DatabaseQueryRowLimit)
			offset := int64(0)
			window := []map[string]any(nil)
			resultSchema := result.Schema
			hasMore := false
			nextPageToken := ""
			if token, ok := decodeManagedResultOffsetToken(in.PageToken, job.ID, job.Provider); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				// Provider cursors are consumable (Databricks closes a statement
				// after the final chunk). Never expose or replay one from this tool;
				// completed jobs page only through their durable materialization.
				return JSONError("Invalid or expired page_token; restart pagination from the durable first page."), nil
			}
			if offset < 0 || offset > int64(len(result.Rows)) {
				return JSONError("page_token out of range"), nil
			}
			start := int(offset)
			end := start + pageSize + 1
			if end > len(result.Rows) {
				end = len(result.Rows)
			}
			window = result.Rows[start:end]
			hasMore = len(window) > pageSize
			if hasMore {
				window = window[:pageSize]
				nextPageToken = encodeManagedResultOffsetToken(job.ID, job.Provider, offset+int64(pageSize))
			}

			rows := make([]any, 0, len(window))
			for _, row := range window {
				rows = append(rows, row)
			}
			out := map[string]any{
				"rows":                    rows,
				"page_size":               pageSize,
				"offset":                  offset,
				"has_more":                hasMore,
				"native_page":             false,
				"validation_state":        string(validation.State),
				"validation_required":     validationRequired,
				"validation_instructions": validationInstructions,
				"retrieval_validates":     false,
			}
			if validation.Detail != "" {
				out["validation_detail"] = validation.Detail
			}
			if validationErr != nil {
				out["validation_read_error"] = validationErr.Error()
			}
			if nextPageToken != "" {
				out["next_page_token"] = nextPageToken
			}

			artifactID := deterministicAsyncArtifactID(owner.ThreadID, job.ID)
			fullRows := result.Rows
			rowCountTotal := len(fullRows)

			queryMetadata := cloneManagedQueryMetadataWithoutObligationIDs(job.Metadata)
			if queryMetadata == nil {
				queryMetadata = map[string]any{}
			}
			stripManagedQueryLegacyRetryMetadata(queryMetadata)
			for key, value := range cloneManagedQueryMetadataWithoutObligationIDs(result.Metadata) {
				queryMetadata[key] = value
			}
			stripManagedQueryLegacyRetryMetadata(queryMetadata)
			if submittedSQL := strings.TrimSpace(job.SQL); submittedSQL != "" {
				queryMetadata["submitted_sql"] = submittedSQL
				if originalSQL := strings.TrimSpace(anyToString(queryMetadata["original_sql"])); originalSQL != "" {
					queryMetadata["sql"] = originalSQL
				} else {
					queryMetadata["sql"] = submittedSQL
				}
			}
			if sqlPreview := strings.TrimSpace(job.SQLPreview); sqlPreview != "" {
				queryMetadata["sql_preview"] = sqlPreview
			}
			queryMetadata["provider"] = job.Provider
			queryMetadata["job_id"] = job.ID
			queryMetadata["status"] = string(jobs.JobStatusCompleted)
			queryMetadata["owner_token"] = ownerToken
			queryMetadata["thread_id"] = owner.ThreadID
			queryMetadata["artifact_id"] = artifactID
			queryMetadata["full_artifact_id"] = artifactID
			queryMetadata["requested_limit"] = requestedLimit
			queryMetadata["effective_limit"] = pageSize
			queryMetadata["configured_limit"] = effectiveConfiguredLimit
			queryMetadata["limit_capped"] = isCapped
			queryMetadata["rows_returned"] = len(rows)
			queryMetadata["rows_preview_count"] = len(rows)
			queryMetadata["row_count_total"] = rowCountTotal
			queryMetadata["result_counts"] = map[string]any{
				"rows": rowCountTotal,
			}
			queryMetadata["page_size"] = pageSize
			queryMetadata["offset"] = offset
			queryMetadata["has_more"] = hasMore
			queryMetadata["validation_state"] = string(validation.State)
			queryMetadata["validation_required"] = validationRequired
			queryMetadata["validation_instructions"] = validationInstructions
			queryMetadata["retrieval_validates"] = false
			if obligationID := managedQueryObligationID(ctx, exec.LongHorizon, exec.RunID, job, queryMetadata); obligationID != "" {
				queryMetadata["query_obligation_id"] = obligationID
				out["query_obligation_id"] = obligationID
			}
			queryMetadata["validation_revision"] = validation.Revision
			queryMetadata["validation_source"] = validation.Source
			if validation.Detail != "" {
				queryMetadata["validation_detail"] = validation.Detail
			}
			if validationErr != nil {
				queryMetadata["validation_read_error"] = validationErr.Error()
			}
			queryMetadata["validation_checks"] = map[string]any{
				"provider_execution_completed": true,
				"materialization_state":        job.Materialization,
				"rows_retrieved":               rowCountTotal,
				"schema_columns":               len(resultSchema),
			}
			if len(resultSchema) > 0 {
				queryMetadata["schema"] = resultSchema
			}
			if nextPageToken != "" {
				queryMetadata["next_page_token"] = nextPageToken
			}
			resultMetadata := queryLifecycleResultMetadataForQuery("get_query_results", "query_results", job.Provider, string(jobs.JobStatusCompleted), queryMetadata, map[string]any{
				"job_id":              job.ID,
				"owner_token":         ownerToken,
				"thread_id":           owner.ThreadID,
				"rows_returned":       len(rows),
				"rows_preview_count":  len(rows),
				"row_count_total":     rowCountTotal,
				"page_size":           pageSize,
				"offset":              offset,
				"has_more":            hasMore,
				"validation_state":    string(validation.State),
				"validation_required": validationRequired,
				"validation_revision": validation.Revision,
			})
			resultMetadata = addRowsArtifactReferenceMetadata(out, resultMetadata, artifactID, rowCountTotal, len(rows), "json_rows", rowCountTotal > len(rows) || hasMore)

			// This artifact has a deterministic thread/job identity and is reused
			// across runs. Persist only job-scoped metadata; the owning run keeps
			// its verified obligation in the immediate tool response above.
			artifactMetadata := cloneManagedQueryMetadataWithoutObligationIDs(queryMetadata)
			artifactPayload, payloadErr := newLargeRowsArtifactPayload(artifactID, "Query Results", fullRows, "get_query_results", artifactMetadata)
			if payloadErr != nil {
				return JSONError("Failed to encode query results for artifact persistence."), nil
			}

			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				Result:         out,
				ResultMetadata: resultMetadata,
				QueryMetadata:  queryMetadata,
				Artifact:       artifactPayload,
			}, nil
		},
	}
}

func newValidateQueryResultTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "validate_query_result",
				Description: "Record an explicit durable validation or rejection of completed query results. A validated verdict requires passing structured checks; the server derives mechanical availability, schema, and row-count evidence from the managed result. Optional evidence may record additional semantic observations. Retrieval never validates automatically.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Managed query job ID.",
						},
						"owner_token": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Opaque owner token returned when the query was submitted.",
						},
						"verdict": map[string]any{
							"type":        "string",
							"enum":        []string{string(jobs.ValidationValidated), string(jobs.ValidationRejected)},
							"description": "validated only after checking the result; rejected when it is unsuitable or incorrect.",
						},
						"detail": map[string]any{
							"type":        "string",
							"minLength":   1,
							"maxLength":   4000,
							"description": "Non-empty explanation of the validation evidence or rejection reason.",
						},
						"checks": map[string]any{
							"type":        "array",
							"maxItems":    64,
							"description": "Required for a validated verdict. Every supplied check must explicitly pass. Use semantic categories such as filters, units, nulls, plausibility, reconciliation, or business_logic when applicable; the server verifies result availability, schema, and materialization directly. Rejection does not require complete passing checks.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"category": map[string]any{
										"type":        "string",
										"enum":        queryValidationCategoryValues(),
										"description": "Provider-neutral validation category.",
									},
									"name": map[string]any{
										"type": "string", "minLength": 1, "maxLength": 128,
									},
									"passed": map[string]any{"type": "boolean"},
									"detail": map[string]any{
										"type": "string", "maxLength": 1000,
									},
								},
								"required":             []string{"category", "name", "passed"},
								"additionalProperties": false,
							},
						},
						"evidence": map[string]any{
							"type":        "array",
							"maxItems":    64,
							"description": "Optional supplemental observations for a validated or rejected verdict. Multiple distinct observations may share a semantic category such as reconciliation; give each a clear name. Do not restate result availability, schema shape, or row-count/materialization facts unless needed; those three mechanical categories are singleton facts that the server derives and records from the managed result.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"category": map[string]any{
										"type": "string",
										"enum": queryValidationCategoryValues(),
									},
									"name": map[string]any{
										"type": "string", "minLength": 1, "maxLength": 128,
									},
									"observed": map[string]any{
										"type": "string", "minLength": 1, "maxLength": 2000,
									},
									"expected": map[string]any{
										"type": "string", "maxLength": 2000,
									},
									"source": map[string]any{
										"type": "string", "minLength": 1, "maxLength": 512,
										"description": "Where the observation was read, such as get_query_results or its durable artifact.",
									},
									"detail": map[string]any{
										"type": "string", "maxLength": 1000,
									},
									"result_available": map[string]any{
										"type":        "boolean",
										"description": "Required and true for result_availability evidence.",
									},
									"column_count": map[string]any{
										"type": "integer", "minimum": 0,
										"description": "Required for schema_shape evidence and must match the retrieved schema.",
									},
									"columns": map[string]any{
										"type": "array", "maxItems": 1024,
										"items":       map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
										"description": "Required ordered column names for schema_shape evidence.",
									},
									"row_count": map[string]any{
										"type": "integer", "minimum": 0,
										"description": "Required for row_count_materialization evidence and must match the retrieved result count.",
									},
									"materialization_state": map[string]any{
										"type":        "string",
										"enum":        []string{"complete", "truncated", "retrievable", "partial", "expired", "none"},
										"description": "Required for row_count_materialization evidence. Partial or expired results cannot be validated.",
									},
								},
								"required":             []string{"category", "name", "observed", "source"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{"job_id", "owner_token", "verdict", "detail"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Jobs == nil {
				return JSONError("Async query registry is not configured."), nil
			}
			if exec.LongHorizon == nil {
				return JSONError("Durable query validation repository is not configured."), nil
			}
			if strings.TrimSpace(exec.RunID) == "" || strings.TrimSpace(exec.ThreadID) == "" {
				return JSONError("A current run and thread are required to validate query results."), nil
			}
			var in struct {
				JobID      string                    `json:"job_id"`
				OwnerToken string                    `json:"owner_token"`
				Verdict    string                    `json:"verdict"`
				Detail     string                    `json:"detail"`
				Checks     []queryValidationCheck    `json:"checks,omitempty"`
				Evidence   []queryValidationEvidence `json:"evidence,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			jobID := strings.TrimSpace(in.JobID)
			ownerToken := strings.TrimSpace(in.OwnerToken)
			verdict := jobs.ValidationState(strings.TrimSpace(strings.ToLower(in.Verdict)))
			detail := strings.TrimSpace(in.Detail)
			if jobID == "" || ownerToken == "" || detail == "" {
				return JSONError("job_id, owner_token, verdict, and non-empty detail are required."), nil
			}
			if len(detail) > 4000 {
				return JSONError("detail must be 4000 characters or fewer."), nil
			}
			if verdict != jobs.ValidationValidated && verdict != jobs.ValidationRejected {
				return JSONError("verdict must be validated or rejected."), nil
			}
			if validationErr := normalizeStructuredValidation(in.Checks, in.Evidence); validationErr != nil {
				return JSONError(validationErr.Error()), nil
			}
			if !structuredValidationPayloadWithinLimit(in.Checks, in.Evidence) {
				return JSONError("checks and evidence must encode to 64 KiB or fewer."), nil
			}
			recordedEvidence := in.Evidence

			owner := jobs.JobOwner{ThreadID: exec.ThreadID, RunID: exec.RunID, Handle: ownerToken}
			job, ok := exec.Jobs.Snapshot(jobID, owner)
			if !ok {
				return JSONError("Job not found for this thread and owner token."), nil
			}
			if job.Status != jobs.JobStatusCompleted {
				return JSONError(fmt.Sprintf("Query result validation requires completed execution (status=%s).", job.Status)), nil
			}
			if verdict == jobs.ValidationValidated {
				result, resultErr := exec.Jobs.Results(jobID, owner)
				if resultErr != nil {
					return JSONError("Completed query results are not retrievable: " + resultErr.Error()), nil
				}
				if !queryResultIsMaterializedOrRetrievable(job, result) {
					return JSONError("Cannot validate this query: completed results are neither durably materialized nor safely retrievable. Retrieve or recover the result first."), nil
				}
				var validationErr error
				recordedEvidence, validationErr = validateSuccessfulQueryResult(in.Checks, in.Evidence, job, result)
				if validationErr != nil {
					return JSONError("Cannot validate this query: " + validationErr.Error()), nil
				}
				if !structuredValidationPayloadWithinLimit(in.Checks, recordedEvidence) {
					return JSONError("checks and server-derived evidence must encode to 64 KiB or fewer."), nil
				}
			}

			now := time.Now().UTC()
			anchorState := job.Validation
			if anchorState == "" {
				anchorState = jobs.ValidationUnvalidated
			}
			if _, anchorErr := upsertCurrentValidationDependency(ctx, exec.LongHorizon, exec.RunID, job.ID, anchorState, now); anchorErr != nil {
				// Establish the current-run completion obligation before mutating
				// the job verdict. A crash or evidence-write failure after the
				// verdict commit can then be reconciled from durable job truth.
				return JSONError("Query validation was not changed because the current run dependency could not be established: " + anchorErr.Error()), nil
			}

			record, newRevision, updateErr := setQueryValidationWithRetry(ctx, exec.Jobs, exec.LongHorizon, owner, job, verdict, detail)
			if updateErr != nil {
				return JSONError("Failed to persist query validation: " + updateErr.Error()), nil
			}
			evidencePayload := map[string]any{
				"state":       string(verdict),
				"detail":      detail,
				"checks":      in.Checks,
				"evidence":    recordedEvidence,
				"revision":    newRevision,
				"run_id":      exec.RunID,
				"thread_id":   exec.ThreadID,
				"owner_token": ownerToken,
				"artifact_id": job.ArtifactID,
				"provider":    job.Provider,
			}
			payloadJSON, _ := json.Marshal(evidencePayload)
			if _, eventErr := exec.LongHorizon.AppendQueryJobEvent(ctx, store.QueryJobEvent{
				JobID:          job.ID,
				Type:           "validation_evidence",
				Payload:        payloadJSON,
				ObservationKey: fmt.Sprintf("validation-evidence:%d", newRevision),
				CreatedAt:      now,
			}); eventErr != nil && !errors.Is(eventErr, store.ErrConflict) {
				return JSONError("Validation state was persisted, but structured evidence could not be recorded: " + eventErr.Error()), nil
			}

			dependency, dependencyErr := upsertCurrentValidationDependency(ctx, exec.LongHorizon, exec.RunID, job.ID, verdict, now)
			if dependencyErr != nil {
				return JSONError("Validation state was persisted, but the current run dependency could not be updated: " + dependencyErr.Error()), nil
			}

			instructions := "The query result is durably validated and may satisfy this run's query dependency."
			message := "Query result validated."
			replacementRequired := false
			var alternativePlan map[string]any
			obligationID := ""
			retryMetadata := map[string]any{}
			if verdict == jobs.ValidationRejected {
				obligationID = strings.TrimSpace(dependency.ObligationID)
				if obligationID == "" {
					obligationID = managedQueryObligationID(ctx, exec.LongHorizon, exec.RunID, job, nil)
				}
				if obligationID != "" {
					retryMetadata["query_obligation_id"] = obligationID
				}
				message = "Query result rejected."
				retryClause := managedQueryRetryTargetClause(retryMetadata)
				instructions = fmt.Sprintf("Do not use this result as evidence and do not cancel the completed query. Profile and run a corrected or alternative query with %s, then validate that replacement result.", retryClause)
				replacementRequired = true
				alternativePlan = map[string]any{
					"action":                 "profile_and_run_replacement_query",
					"cancel_completed_query": false,
					"steps": []string{
						"Use the rejection detail and checks to correct or redesign the SQL.",
						"Profile the replacement query before submission.",
						fmt.Sprintf("Run it with %s and wait for completion.", retryClause),
						"Retrieve and explicitly validate the replacement result.",
					},
				}
				if obligationID != "" {
					alternativePlan["query_obligation_id"] = obligationID
				}
			}
			publicDependency := dependency
			if verdict == jobs.ValidationValidated {
				// A validated dependency is settled, so its stable retry handle is
				// no longer actionable. Publishing it here caused later model turns
				// to reuse a handle that preflight correctly considered inactive.
				publicDependency.ObligationID = ""
			}
			result := map[string]any{
				"ok":                   true,
				"job_id":               job.ID,
				"owner_token":          ownerToken,
				"provider":             job.Provider,
				"validation_state":     string(verdict),
				"validation_detail":    detail,
				"validation_revision":  newRevision,
				"checks":               in.Checks,
				"evidence":             recordedEvidence,
				"replacement_required": replacementRequired,
				"dependency":           publicDependency,
				"message":              message,
				"instructions":         instructions,
			}
			if alternativePlan != nil {
				result["alternative_plan"] = alternativePlan
			}
			if obligationID != "" {
				result["query_obligation_id"] = obligationID
			}
			queryMetadata := cloneManagedQueryMetadataWithoutObligationIDs(job.Metadata)
			if queryMetadata == nil {
				queryMetadata = map[string]any{}
			}
			stripManagedQueryLegacyRetryMetadata(queryMetadata)
			queryMetadata["provider"] = job.Provider
			queryMetadata["job_id"] = job.ID
			queryMetadata["owner_token"] = ownerToken
			queryMetadata["thread_id"] = exec.ThreadID
			queryMetadata["run_id"] = exec.RunID
			queryMetadata["artifact_id"] = job.ArtifactID
			queryMetadata["validation_state"] = string(verdict)
			queryMetadata["validation_detail"] = detail
			queryMetadata["validation_revision"] = newRevision
			queryMetadata["validation_checks"] = in.Checks
			queryMetadata["validation_evidence"] = recordedEvidence
			queryMetadata["replacement_required"] = replacementRequired
			if obligationID != "" {
				queryMetadata["query_obligation_id"] = obligationID
			}
			if alternativePlan != nil {
				queryMetadata["alternative_plan"] = alternativePlan
			}
			queryMetadata["validation_instructions"] = instructions
			queryMetadata["durable_job_revision"] = record.Revision
			metadata := queryLifecycleResultMetadataForQuery("validate_query_result", "query_validation", job.Provider, string(job.Status), queryMetadata, map[string]any{
				"job_id":               job.ID,
				"owner_token":          ownerToken,
				"thread_id":            exec.ThreadID,
				"run_id":               exec.RunID,
				"validation_state":     string(verdict),
				"validation_revision":  newRevision,
				"replacement_required": replacementRequired,
			})
			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				Result:         result,
				ResultMetadata: metadata,
				QueryMetadata:  queryMetadata,
			}, nil
		},
	}
}

type durableQueryValidation struct {
	State           jobs.ValidationState
	Detail          string
	Revision        int64
	Source          string
	DependencyState jobs.ValidationState
}

type queryValidationCheck struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Passed   *bool  `json:"passed"`
	Detail   string `json:"detail,omitempty"`
}

type queryValidationEvidence struct {
	Category             string   `json:"category"`
	Name                 string   `json:"name"`
	Observed             string   `json:"observed"`
	Expected             string   `json:"expected,omitempty"`
	Source               string   `json:"source"`
	Detail               string   `json:"detail,omitempty"`
	ResultAvailable      *bool    `json:"result_available,omitempty"`
	ColumnCount          *int     `json:"column_count,omitempty"`
	Columns              []string `json:"columns,omitempty"`
	RowCount             *int64   `json:"row_count,omitempty"`
	MaterializationState string   `json:"materialization_state,omitempty"`
}

const (
	queryValidationResultAvailability      = "result_availability"
	queryValidationSchemaShape             = "schema_shape"
	queryValidationRowCountMaterialization = "row_count_materialization"
)

func queryValidationCategoryValues() []string {
	return []string{
		queryValidationResultAvailability,
		queryValidationSchemaShape,
		queryValidationRowCountMaterialization,
		"filters",
		"units",
		"nulls",
		"plausibility",
		"reconciliation",
		"business_logic",
	}
}

func validQueryValidationCategory(category string) bool {
	for _, candidate := range queryValidationCategoryValues() {
		if category == candidate {
			return true
		}
	}
	return false
}

func normalizeStructuredValidation(checks []queryValidationCheck, evidence []queryValidationEvidence) error {
	if len(checks) > 64 || len(evidence) > 64 {
		return errors.New("checks and evidence may contain at most 64 entries each")
	}
	for i := range checks {
		checks[i].Category = strings.TrimSpace(strings.ToLower(checks[i].Category))
		checks[i].Name = strings.TrimSpace(checks[i].Name)
		checks[i].Detail = strings.TrimSpace(checks[i].Detail)
		if !validQueryValidationCategory(checks[i].Category) || checks[i].Name == "" || checks[i].Passed == nil {
			return fmt.Errorf("checks[%d] requires a supported category, non-empty name, and explicit passed value", i)
		}
		if len(checks[i].Name) > 128 || len(checks[i].Detail) > 1000 {
			return fmt.Errorf("checks[%d] exceeds the allowed field length", i)
		}
	}
	for i := range evidence {
		evidence[i].Category = strings.TrimSpace(strings.ToLower(evidence[i].Category))
		evidence[i].Name = strings.TrimSpace(evidence[i].Name)
		evidence[i].Observed = strings.TrimSpace(evidence[i].Observed)
		evidence[i].Expected = strings.TrimSpace(evidence[i].Expected)
		evidence[i].Source = strings.TrimSpace(evidence[i].Source)
		evidence[i].Detail = strings.TrimSpace(evidence[i].Detail)
		for columnIndex := range evidence[i].Columns {
			evidence[i].Columns[columnIndex] = strings.TrimSpace(evidence[i].Columns[columnIndex])
		}
		evidence[i].MaterializationState = strings.TrimSpace(strings.ToLower(evidence[i].MaterializationState))
		if !validQueryValidationCategory(evidence[i].Category) || evidence[i].Name == "" || evidence[i].Observed == "" || evidence[i].Source == "" {
			return fmt.Errorf("evidence[%d] requires a supported category and non-empty name, observed, and source", i)
		}
		if len(evidence[i].Name) > 128 || len(evidence[i].Observed) > 2000 || len(evidence[i].Expected) > 2000 || len(evidence[i].Source) > 512 || len(evidence[i].Detail) > 1000 || len(evidence[i].Columns) > 1024 {
			return fmt.Errorf("evidence[%d] exceeds the allowed field length", i)
		}
		for columnIndex, column := range evidence[i].Columns {
			if column == "" || len(column) > 256 {
				return fmt.Errorf("evidence[%d].columns[%d] must be non-empty and 256 characters or fewer", i, columnIndex)
			}
		}
		if evidence[i].ColumnCount != nil && *evidence[i].ColumnCount < 0 {
			return fmt.Errorf("evidence[%d].column_count cannot be negative", i)
		}
		if evidence[i].RowCount != nil && *evidence[i].RowCount < 0 {
			return fmt.Errorf("evidence[%d].row_count cannot be negative", i)
		}
	}
	return nil
}

func validateSuccessfulQueryResult(checks []queryValidationCheck, evidence []queryValidationEvidence, job *jobs.Job, result *jobs.QueryResult) ([]queryValidationEvidence, error) {
	if len(checks) == 0 {
		return nil, errors.New("validated requires structured checks; checks cannot be empty")
	}
	for i, check := range checks {
		if check.Passed == nil {
			return nil, fmt.Errorf("checks[%d] must explicitly set passed", i)
		}
		if !*check.Passed {
			return nil, fmt.Errorf("checks[%d] (%s) did not pass; record a rejected verdict with a corrective plan instead", i, check.Name)
		}
	}

	// Semantic categories are labels, not unique keys. A result can have several
	// distinct reconciliation, filter, plausibility, or business-logic
	// observations. Only the three mechanical categories are single canonical
	// facts, because the server verifies and replaces those observations below.
	mechanicalEvidence := make(map[string]queryValidationEvidence, 3)
	for _, item := range evidence {
		if !isMechanicalQueryValidationCategory(item.Category) {
			continue
		}
		if _, duplicate := mechanicalEvidence[item.Category]; duplicate {
			return nil, fmt.Errorf("mechanical evidence category %s appears more than once; omit it and rely on the server-derived fact", item.Category)
		}
		mechanicalEvidence[item.Category] = item
	}

	if job == nil || result == nil || !queryResultIsMaterializedOrRetrievable(job, result) {
		return nil, errors.New("result availability cannot be established from the managed query result")
	}
	if availability, supplied := mechanicalEvidence[queryValidationResultAvailability]; supplied {
		if availability.ResultAvailable == nil || !*availability.ResultAvailable {
			return nil, errors.New("result_availability evidence must explicitly set result_available=true")
		}
	}

	actualColumns, schemaErr := queryValidationSchemaColumns(result)
	if schemaErr != nil {
		return nil, schemaErr
	}
	if schemaEvidence, supplied := mechanicalEvidence[queryValidationSchemaShape]; supplied {
		if schemaEvidence.ColumnCount == nil {
			return nil, errors.New("schema_shape evidence must include column_count")
		}
		if *schemaEvidence.ColumnCount != len(actualColumns) {
			return nil, fmt.Errorf("schema_shape column_count=%d does not match retrieved schema column count=%d", *schemaEvidence.ColumnCount, len(actualColumns))
		}
		if len(schemaEvidence.Columns) != len(actualColumns) {
			return nil, fmt.Errorf("schema_shape columns contains %d names but retrieved schema contains %d", len(schemaEvidence.Columns), len(actualColumns))
		}
		for i := range actualColumns {
			if !strings.EqualFold(strings.TrimSpace(schemaEvidence.Columns[i]), actualColumns[i]) {
				return nil, fmt.Errorf("schema_shape columns[%d]=%q does not match retrieved column %q", i, schemaEvidence.Columns[i], actualColumns[i])
			}
		}
	}

	materializationState, stateErr := queryValidationMaterializationState(job, result)
	if stateErr != nil {
		return nil, stateErr
	}
	actualRows := int64(len(result.Rows))
	if rowEvidence, supplied := mechanicalEvidence[queryValidationRowCountMaterialization]; supplied {
		if rowEvidence.RowCount == nil {
			return nil, errors.New("row_count_materialization evidence must include row_count")
		}
		if *rowEvidence.RowCount != actualRows {
			return nil, fmt.Errorf("row_count_materialization row_count=%d does not match retrieved row count=%d", *rowEvidence.RowCount, actualRows)
		}
		if rowEvidence.MaterializationState == "" {
			return nil, errors.New("row_count_materialization evidence must include materialization_state")
		}
		if rowEvidence.MaterializationState != materializationState {
			return nil, fmt.Errorf("row_count_materialization materialization_state=%q does not match managed result state=%q", rowEvidence.MaterializationState, materializationState)
		}
	}

	resultAvailable := true
	columnCount := len(actualColumns)
	canonical := []queryValidationEvidence{
		{
			Category:        queryValidationResultAvailability,
			Name:            "managed result availability",
			Observed:        "completed result is retrievable",
			Source:          "managed_query_registry",
			ResultAvailable: &resultAvailable,
		},
		{
			Category:    queryValidationSchemaShape,
			Name:        "managed result schema",
			Observed:    fmt.Sprintf("%d columns: %s", columnCount, strings.Join(actualColumns, ", ")),
			Source:      "managed_query_result",
			ColumnCount: &columnCount,
			Columns:     append([]string(nil), actualColumns...),
		},
		{
			Category:             queryValidationRowCountMaterialization,
			Name:                 "managed result materialization",
			Observed:             fmt.Sprintf("%d rows with materialization state %s", actualRows, materializationState),
			Source:               "managed_query_result",
			RowCount:             &actualRows,
			MaterializationState: materializationState,
		},
	}
	for _, item := range evidence {
		switch item.Category {
		case queryValidationResultAvailability, queryValidationSchemaShape, queryValidationRowCountMaterialization:
			// These observations were checked above and are replaced with the
			// canonical server-derived facts so durable evidence never depends on
			// the model accurately restating the managed result.
			continue
		default:
			canonical = append(canonical, item)
		}
	}
	return canonical, nil
}

func isMechanicalQueryValidationCategory(category string) bool {
	switch category {
	case queryValidationResultAvailability, queryValidationSchemaShape, queryValidationRowCountMaterialization:
		return true
	default:
		return false
	}
}

func queryValidationSchemaColumns(result *jobs.QueryResult) ([]string, error) {
	if result == nil {
		return nil, errors.New("schema_shape cannot be established because the result is unavailable")
	}
	if len(result.Schema) > 0 {
		columns := make([]string, 0, len(result.Schema))
		for i, field := range result.Schema {
			name := firstNonEmpty(
				anyToString(field["name"]),
				anyToString(field["column_name"]),
				anyToString(field["field_name"]),
			)
			if name == "" {
				return nil, fmt.Errorf("schema_shape cannot be established because retrieved schema column %d has no name", i)
			}
			columns = append(columns, name)
		}
		return columns, nil
	}
	if len(result.Rows) > 0 {
		columns := make([]string, 0, len(result.Rows[0]))
		for name := range result.Rows[0] {
			name = strings.TrimSpace(name)
			if name != "" {
				columns = append(columns, name)
			}
		}
		sort.Strings(columns)
		if len(columns) > 0 {
			return columns, nil
		}
	}
	return nil, errors.New("schema_shape cannot be established because the retrieved result has no schema")
}

func queryValidationMaterializationState(job *jobs.Job, result *jobs.QueryResult) (string, error) {
	if job == nil || result == nil {
		return "", errors.New("row_count_materialization cannot be established because the result is unavailable")
	}
	switch job.Materialization {
	case jobs.MaterializationComplete:
		return string(jobs.MaterializationComplete), nil
	case jobs.MaterializationTruncated:
		return string(jobs.MaterializationTruncated), nil
	case jobs.MaterializationPartial:
		return "", errors.New("row_count_materialization cannot pass while materialization is partial; complete or deliberately truncate retrieval first")
	case jobs.MaterializationExpired:
		return "", errors.New("row_count_materialization cannot pass because materialized results expired; recover the result first")
	case "", jobs.MaterializationNone:
		if len(result.Rows) > 0 || len(result.Schema) > 0 || strings.TrimSpace(anyToString(result.Metadata["materialized_rows"])) != "" {
			return "retrievable", nil
		}
	}
	return "", fmt.Errorf("row_count_materialization cannot establish a safe materialization state from %q", job.Materialization)
}

func structuredValidationPayloadWithinLimit(values ...any) bool {
	encoded, err := json.Marshal(values)
	return err == nil && len(encoded) <= 64<<10
}

func queryResultIsMaterializedOrRetrievable(job *jobs.Job, result *jobs.QueryResult) bool {
	if job == nil || result == nil {
		return false
	}
	// The durable materialization state is authoritative. Older records may
	// retain a transient fetch/finalize error even after a later retry stored a
	// complete or deliberately truncated result; that stale diagnostic must not
	// permanently block validation of the recovered result.
	switch job.Materialization {
	case jobs.MaterializationPartial, jobs.MaterializationComplete, jobs.MaterializationTruncated:
		return true
	}
	if strings.TrimSpace(anyToString(job.Metadata["result_fetch_error"])) != "" ||
		strings.TrimSpace(anyToString(job.Metadata["materialization_error"])) != "" {
		return false
	}
	if len(result.Rows) > 0 || len(result.Schema) > 0 {
		return true
	}
	if result.Metadata != nil {
		if _, ok := result.Metadata["materialized_rows"]; ok {
			return true
		}
	}
	return strings.TrimSpace(job.ArtifactID) != ""
}

func setQueryValidationWithRetry(ctx context.Context, registry *jobs.Registry, repository store.LongHorizonRepository, owner jobs.JobOwner, job *jobs.Job, state jobs.ValidationState, detail string) (jobs.JobRecord, int64, error) {
	const maxAttempts = 5
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err := registry.SetValidation(job.ID, owner, state, detail)
		if errors.Is(err, jobs.ErrRevisionConflict) {
			continue
		}
		if err != nil {
			return jobs.JobRecord{}, 0, err
		}
		record, ok, readErr := repository.GetQueryJob(ctx, job.ID)
		if readErr != nil {
			return jobs.JobRecord{}, 0, readErr
		}
		if !ok {
			return jobs.JobRecord{}, 0, store.ErrNotFound
		}
		if strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(job.Owner.ThreadID) ||
			strings.TrimSpace(record.Owner.Handle) != strings.TrimSpace(job.Owner.Handle) {
			return jobs.JobRecord{}, 0, jobs.ErrOwnershipMismatch
		}
		if record.ValidationState != state || strings.TrimSpace(record.ValidationDetail) != strings.TrimSpace(detail) {
			return jobs.JobRecord{}, 0, errors.New("durable query validation did not match the requested verdict")
		}
		return record, record.Revision, nil
	}
	return jobs.JobRecord{}, 0, jobs.ErrRevisionConflict
}

func upsertCurrentValidationDependency(ctx context.Context, repository store.LongHorizonRepository, runID, jobID string, state jobs.ValidationState, now time.Time) (store.RunQueryDependency, error) {
	dependencies, err := repository.ListRunQueryDependencies(ctx, runID)
	if err != nil {
		return store.RunQueryDependency{}, err
	}
	dependency := store.RunQueryDependency{
		RunID:                 strings.TrimSpace(runID),
		JobID:                 strings.TrimSpace(jobID),
		RequiredForCompletion: true,
		ValidationState:       string(state),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	for _, existing := range dependencies {
		if strings.TrimSpace(existing.JobID) != strings.TrimSpace(jobID) {
			continue
		}
		dependency = existing
		dependency.ValidationState = string(state)
		dependency.UpdatedAt = now
		if dependency.CreatedAt.IsZero() {
			dependency.CreatedAt = now
		}
		break
	}
	if strings.TrimSpace(dependency.ObligationID) == "" {
		obligationID, deriveErr := store.DeriveQueryObligationID(ctx, repository, runID, jobID)
		if deriveErr != nil {
			return store.RunQueryDependency{}, deriveErr
		}
		dependency.ObligationID = obligationID
	}
	return repository.UpsertRunQueryDependency(ctx, dependency)
}

func loadDurableQueryValidation(ctx context.Context, repository store.LongHorizonRepository, runID, jobID string) (durableQueryValidation, error) {
	out := durableQueryValidation{State: jobs.ValidationUnvalidated, Source: "default"}
	if repository == nil {
		return out, nil
	}
	record, ok, err := repository.GetQueryJob(ctx, jobID)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, store.ErrNotFound
	}
	out.Revision = record.Revision
	if validDurableValidationState(record.ValidationState) {
		out.State = record.ValidationState
		out.Detail = strings.TrimSpace(record.ValidationDetail)
		out.Source = "query_job"
	}
	if strings.TrimSpace(runID) != "" {
		dependencies, dependencyErr := repository.ListRunQueryDependencies(ctx, runID)
		if dependencyErr != nil {
			return out, dependencyErr
		}
		for _, dependency := range dependencies {
			if strings.TrimSpace(dependency.JobID) != strings.TrimSpace(jobID) {
				continue
			}
			state := jobs.ValidationState(strings.TrimSpace(dependency.ValidationState))
			if validDurableValidationState(state) {
				out.DependencyState = state
			}
			break
		}
	}
	return out, nil
}

func validDurableValidationState(state jobs.ValidationState) bool {
	switch state {
	case jobs.ValidationUnvalidated, jobs.ValidationValidating, jobs.ValidationValidated, jobs.ValidationRejected:
		return true
	default:
		return false
	}
}

func applyDurableValidationFields(target map[string]any, validation durableQueryValidation) {
	if target == nil {
		return
	}
	state := validation.State
	if !validDurableValidationState(state) {
		state = jobs.ValidationUnvalidated
	}
	target["validation_state"] = string(state)
	target["validation_required"] = state != jobs.ValidationValidated
	target["validation_revision"] = validation.Revision
	target["validation_source"] = validation.Source
	target["validation_instructions"] = durableValidationInstructions(validation)
	if validDurableValidationState(validation.DependencyState) {
		target["dependency_validation_state"] = string(validation.DependencyState)
		target["dependency_validation_satisfied"] = validation.DependencyState == jobs.ValidationValidated
	}
	if validation.Detail != "" {
		target["validation_detail"] = validation.Detail
	}
}

func durableValidationInstructions(validation durableQueryValidation) string {
	switch validation.State {
	case jobs.ValidationValidated:
		return "This result has an explicit durable validation verdict. Retrieval did not create or change that verdict."
	case jobs.ValidationRejected:
		return "This result is explicitly rejected. Do not use it as evidence; profile and run a corrected or alternative replacement query, then validate that result."
	case jobs.ValidationValidating:
		return "Validation is in progress. Do not finalize the task until a validated or rejected verdict is recorded."
	default:
		return "Retrieval does not validate results. Check schema, row counts, filters, units, null behavior, and plausibility, then call validate_query_result with validated or rejected."
	}
}

func newCancelQueryTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "cancel_query",
				Description: "Cancel a running async query for an explicit durable reason. A query must not be cancelled merely because it is slow; use check_query_status and the long-horizon continuation flow unless the query is wrong, superseded, no longer needed, or the user explicitly requested cancellation.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"job_id": map[string]any{
							"type":        "string",
							"description": "Async query job ID.",
						},
						"owner_token": map[string]any{
							"type":        "string",
							"description": "Opaque owner token returned when the query was submitted.",
						},
						"reason": map[string]any{
							"type": "string",
							"enum": []string{
								string(jobs.CancelReasonUserRequest),
								string(jobs.CancelReasonSuperseded),
								string(jobs.CancelReasonWrongQuery),
								string(jobs.CancelReasonNoLongerNeeded),
							},
							"description": "Required durable reason. Choose wrong_query for an invalid query, superseded when a replacement has made it obsolete, no_longer_needed when its result is no longer required, or user_request only when the user explicitly asked to cancel.",
						},
						"dry_run": map[string]any{
							"type":        "boolean",
							"description": "Validate that this query can be cancelled and return a no-mutation preview without cancelling it.",
						},
					},
					"required":             []string{"job_id", "owner_token", "reason"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			_ = ctx
			if exec.Jobs == nil {
				return JSONError("Async query registry is not configured."), nil
			}
			var in struct {
				JobID      string `json:"job_id"`
				OwnerToken string `json:"owner_token"`
				Reason     string `json:"reason"`
				DryRun     bool   `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			jobID := strings.TrimSpace(in.JobID)
			ownerToken := strings.TrimSpace(in.OwnerToken)
			cancelReason := jobs.CancelReason(strings.TrimSpace(in.Reason))
			if jobID == "" || ownerToken == "" || cancelReason == "" {
				return JSONError("job_id, owner_token, and reason are required"), nil
			}
			switch cancelReason {
			case jobs.CancelReasonUserRequest, jobs.CancelReasonSuperseded,
				jobs.CancelReasonWrongQuery, jobs.CancelReasonNoLongerNeeded:
			default:
				return JSONError("reason must be user_request, superseded, wrong_query, or no_longer_needed"), nil
			}
			owner := jobs.JobOwner{
				ThreadID: exec.ThreadID,
				RunID:    exec.RunID,
				Handle:   ownerToken,
			}
			if in.DryRun {
				job, ok := exec.Jobs.Snapshot(jobID, owner)
				if !ok {
					return Output{
						OK:             false,
						ResultFormat:   ResultFormatJSON,
						ResultMetadata: queryLifecycleResultMetadata("cancel_query", "cancel_dry_run", "", string(jobs.JobStatusUnknown), map[string]any{"job_id": jobID, "mode": "dry_run"}),
						Result: map[string]any{
							"ok":      false,
							"dry_run": true,
							"error":   "Job not found for this thread and owner token.",
						},
					}, nil
				}
				wouldCancel := queryJobIsActive(job.Status)
				result := map[string]any{
					"ok":            true,
					"dry_run":       true,
					"job_id":        jobID,
					"status":        string(job.Status),
					"cancel_reason": string(cancelReason),
					"would_cancel":  wouldCancel,
					"message":       cancelQueryDryRunMessage(job.Status),
				}
				return JSONOKWithMetadata(result, queryLifecycleResultMetadataForJob("cancel_query", "cancel_dry_run", job, string(job.Status), map[string]any{
					"job_id":        jobID,
					"mode":          "dry_run",
					"cancel_reason": string(cancelReason),
					"would_cancel":  wouldCancel,
				})), nil
			}
			job, _ := exec.Jobs.Snapshot(jobID, owner)
			if err := exec.Jobs.RequestCancel(jobID, owner, cancelReason); err != nil {
				if errors.Is(err, jobs.ErrNotFound) || errors.Is(err, jobs.ErrOwnershipMismatch) {
					return Output{
						OK:             false,
						ResultFormat:   ResultFormatJSON,
						ResultMetadata: queryLifecycleResultMetadata("cancel_query", "cancel_query", "", string(jobs.JobStatusUnknown), map[string]any{"job_id": jobID}),
						Result: map[string]any{
							"ok":    false,
							"error": "Job not found for this thread and owner token.",
						},
					}, nil
				}
				return Output{
					OK:             false,
					ResultFormat:   ResultFormatJSON,
					ResultMetadata: queryLifecycleResultMetadata("cancel_query", "cancel_query", "", "", map[string]any{"job_id": jobID}),
					Result: map[string]any{
						"ok":    false,
						"error": strings.TrimSpace(err.Error()),
					},
				}, nil
			}
			updated, _ := exec.Jobs.Snapshot(jobID, owner)
			if updated != nil {
				job = updated
			}
			status := "cancel_requested"
			if job != nil && strings.TrimSpace(string(job.Status)) != "" {
				status = string(job.Status)
			}
			message := "Cancellation requested. The query remains active until the provider confirms cancellation; completion may still win the race."
			if job != nil && (job.Status == jobs.JobStatusCancelled || job.Status == jobs.JobStatusExpired) {
				message = "Provider-confirmed query cancellation completed."
			}
			result := map[string]any{
				"ok":                   true,
				"job_id":               jobID,
				"status":               status,
				"cancel_reason":        string(cancelReason),
				"cancellation_pending": status == string(jobs.JobStatusCancelRequested) || status == string(jobs.JobStatusCancelling),
				"message":              message,
			}
			return JSONOKWithMetadata(result, queryLifecycleResultMetadataForJob("cancel_query", "cancel_query", job, status, map[string]any{
				"job_id":               jobID,
				"cancel_reason":        string(cancelReason),
				"cancellation_pending": result["cancellation_pending"],
			})), nil
		},
	}
}

func cancelQueryDryRunMessage(status jobs.JobStatus) string {
	switch status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted, jobs.JobStatusSubmissionUnknown,
		jobs.JobStatusRunning, jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return "Dry run only. This query is cancellable and would be cancelled after approval."
	case jobs.JobStatusCompleted:
		return "Dry run only. This query has already completed."
	case jobs.JobStatusCancelled:
		return "Dry run only. This query has already been cancelled."
	case jobs.JobStatusFailed:
		return "Dry run only. This query has already failed."
	default:
		return "Dry run only. Query status is unknown."
	}
}

func queryStatusMessage(status jobs.JobStatus, elapsedMs int64) string {
	switch status {
	case jobs.JobStatusReserved:
		return "Query has a durable reservation and is waiting for provider submission capacity."
	case jobs.JobStatusProfiling:
		return "Query is durably reserved and profiling before remote submission. Continue monitoring this same job; do not re-profile or resubmit it."
	case jobs.JobStatusSubmissionUnknown:
		return "The provider submission response was ambiguous. Reconciliation is continuing without a blind retry."
	case jobs.JobStatusSubmitted, jobs.JobStatusRunning:
		return fmt.Sprintf("Query is executing (%0.1fs elapsed).", float64(elapsedMs)/1000.0)
	case jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return "Cancellation is pending provider confirmation; query completion may still win."
	case jobs.JobStatusCompleted:
		return "Query execution completed. Retrieve and validate the results before finalizing the task."
	case jobs.JobStatusCancelled:
		return "Query was cancelled."
	case jobs.JobStatusFailed:
		return "Query failed."
	case jobs.JobStatusExpired:
		return "Query exceeded its hard execution budget and reached provider-confirmed expiry."
	case jobs.JobStatusOrphaned:
		return "The provider query can no longer be monitored safely; a linked recovery plan is required."
	default:
		return "Query status is unknown."
	}
}

func queryJobLifecycleDetails(job *jobs.Job) map[string]any {
	if job == nil {
		return map[string]any{}
	}
	now := time.Now()
	details := map[string]any{
		"execution_status":              string(job.Status),
		"monitor_health":                string(job.MonitorHealth),
		"policy_health":                 string(job.PolicyHealth),
		"materialization_state":         string(job.Materialization),
		"keep_on_run_completion":        job.KeepOnRunCompletion,
		"active":                        queryJobIsActive(job.Status),
		"telemetry_age_ms":              maxInt64(0, now.Sub(job.LastAccess).Milliseconds()),
		"queue_elapsed_ms":              maxInt64(0, now.Sub(job.SubmittedAt).Milliseconds()),
		"max_result_rows":               job.Budgets.MaxResultRows,
		"max_result_bytes":              job.Budgets.MaxResultBytes,
		"max_scan_bytes":                job.Budgets.MaxScanBytes,
		"remote_submitted":              !job.Handle.IsZero(),
		"submit_dispatched":             job.SubmitDispatched,
		"provider_submission_confirmed": !job.Handle.IsZero(),
		"submission_uncertain":          managedQuerySubmissionUncertain(job.Status, job.SubmitDispatched, !job.Handle.IsZero()),
		"profile_outcome":               string(job.ProfileOutcome),
		"profile_phase":                 strings.TrimSpace(job.ProfilePhase),
		"profile_dispatched":            job.ProfileDispatched,
	}
	// query_obligation_id is deliberately absent here: these details are
	// generic (not run-scoped), and an obligation handle is only truthful for
	// the run whose ledger records it. Scoped callers add a ledger-verified
	// handle themselves via managedQueryObligationID.
	if job.Profile != nil {
		details["query_profile"] = job.Profile
		details["profile"] = job.Profile
	}
	if !job.ProfileHandle.IsZero() {
		details["profile_handle"] = job.ProfileHandle
	}
	if providerJobID := strings.TrimSpace(job.Handle.ID); providerJobID != "" {
		details["provider_job_id"] = providerJobID
	}
	if providerLocation := strings.TrimSpace(job.Handle.Location); providerLocation != "" {
		details["provider_location"] = providerLocation
	}
	if job.StartedAt != nil {
		details["started_at"] = job.StartedAt.UTC().Format(time.RFC3339Nano)
		details["execution_elapsed_ms"] = maxInt64(0, now.Sub(*job.StartedAt).Milliseconds())
	}
	if job.CompletedAt != nil {
		details["completed_at"] = job.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if job.CancelReason != "" {
		details["cancel_reason"] = string(job.CancelReason)
	}
	if job.CancelOutcome != "" {
		details["cancel_outcome"] = string(job.CancelOutcome)
	}
	for _, key := range []string{
		"provider_state", "provider_state_raw", "bytes_processed", "rows_processed",
		"splits_done", "splits_total", "native_progress_percent",
		"result_fetch_error", "materialization_error", "query_history_error",
	} {
		if value, ok := job.Metadata[key]; ok {
			details[key] = value
		}
	}
	return details
}

func queryJobIsActive(status jobs.JobStatus) bool {
	switch status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted, jobs.JobStatusSubmissionUnknown,
		jobs.JobStatusRunning, jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return true
	default:
		return false
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

type managedResultOffsetTokenEnvelope struct {
	Version  int    `json:"v"`
	JobID    string `json:"job_id"`
	Provider string `json:"provider"`
	Offset   int64  `json:"offset"`
}

func encodeManagedResultOffsetToken(jobID, provider string, offset int64) string {
	jobID = strings.TrimSpace(jobID)
	provider = strings.TrimSpace(strings.ToLower(provider))
	if jobID == "" || provider == "" || offset < 0 {
		return ""
	}
	payload, err := json.Marshal(managedResultOffsetTokenEnvelope{
		Version:  1,
		JobID:    jobID,
		Provider: provider,
		Offset:   offset,
	})
	if err != nil {
		return ""
	}
	return "result-offset:" + base64.RawURLEncoding.EncodeToString(payload)
}

func decodeManagedResultOffsetToken(token, jobID, provider string) (int64, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, true
	}
	if !strings.HasPrefix(token, "result-offset:") {
		return 0, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "result-offset:"))
	if err != nil || len(decoded) == 0 {
		return 0, false
	}
	var envelope managedResultOffsetTokenEnvelope
	if err := json.Unmarshal(decoded, &envelope); err != nil || envelope.Version != 1 {
		return 0, false
	}
	if strings.TrimSpace(envelope.JobID) != strings.TrimSpace(jobID) ||
		strings.TrimSpace(strings.ToLower(envelope.Provider)) != strings.TrimSpace(strings.ToLower(provider)) ||
		envelope.Offset < 0 {
		return 0, false
	}
	return envelope.Offset, true
}

func asyncSQLPreview(sql string) string {
	s := strings.Join(strings.Fields(strings.TrimSpace(sql)), " ")
	if len(s) <= 160 {
		return s
	}
	return s[:157] + "..."
}

func deterministicAsyncArtifactID(threadID, jobID string) string {
	threadID = strings.TrimSpace(threadID)
	jobID = strings.TrimSpace(jobID)
	if threadID == "" || jobID == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(threadID + ":" + jobID))
	return "art-" + hex.EncodeToString(hash[:12])
}

// DeterministicAsyncArtifactID returns the lifecycle artifact id for an async query.
func DeterministicAsyncArtifactID(threadID, jobID string) string {
	return deterministicAsyncArtifactID(threadID, jobID)
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
