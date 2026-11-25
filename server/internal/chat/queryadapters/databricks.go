package queryadapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/databricks"
)

type databricksClient interface {
	SubmitStatement(context.Context, databricks.StatementSubmitRequest) (databricks.StatementSnapshot, error)
	GetStatement(context.Context, databricks.StatementHandle) (databricks.StatementSnapshot, error)
	GetStatementResultChunk(context.Context, databricks.StatementHandle, int32) (databricks.ResultChunk, error)
	RequestCancelStatement(context.Context, databricks.StatementHandle) (databricks.StatementCancelReceipt, error)
	ExplainQuery(context.Context, databricks.ExplainQueryRequest) (databricks.ExplainQueryResult, error)
	GetStatementProfile(context.Context, databricks.StatementHandle) (databricks.StatementProfile, error)
	FindStatementCandidates(context.Context, databricks.StatementReconcileRequest) ([]databricks.StatementCandidate, error)
	MaterializeStatementResult(context.Context, databricks.MaterializeStatementRequest) (databricks.MaterializedStatementResult, error)
}

// Databricks implements Statement Execution with bounded provider-side result
// limits. Submission is deliberately non-idempotent; ambiguous POST outcomes
// are reconciled through Query History using durable query tags and SQL hash.
type Databricks struct {
	client databricksClient
}

func NewDatabricks(handler *databricks.Handler) *Databricks {
	if handler == nil {
		return nil
	}
	return &Databricks{client: handler}
}

func newDatabricksForClient(client databricksClient) *Databricks {
	return &Databricks{client: client}
}

func (a *Databricks) Provider() string { return "databricks" }

func (a *Databricks) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{
		IdempotentSubmit: false,
		DetachedRepoll:   true,
		NativePagination: true,
		ProfileBound:     50 * time.Second,
		// Statement Execution's PENDING state does not distinguish admission,
		// queueing, and planning, so the adapter cannot honestly expose queueing.
		QueuedStateVisible: false,
		CancelConfirmable:  true,
		ProgressSignals: []jobs.ProgressSignal{
			jobs.SignalBytesProcessed,
			jobs.SignalRowsProcessed,
		},
	}
}

func (a *Databricks) Profile(ctx context.Context, req jobs.SubmitRequest) (jobs.QueryProfile, error) {
	return a.ProfileWithObserver(ctx, req, nil)
}

func (a *Databricks) ProfileWithObserver(ctx context.Context, req jobs.SubmitRequest, obs jobs.ProfileObserver) (jobs.QueryProfile, error) {
	if a == nil || a.client == nil {
		return jobs.QueryProfile{}, errors.New("Databricks query adapter is not configured")
	}
	host := firstNonBlank(metadataString(req.Metadata, "host"), req.ConnectionID)
	warehouseID := metadataString(req.Metadata, "warehouse_id")
	tags := databricksTags(req.IdempotencyKey+"-profile", req.SQL)
	var onObservation func(databricks.ExplainObservation) error
	if obs != nil {
		onObservation = func(native databricks.ExplainObservation) error {
			handle := databricksNativeHandle(native.Statement.Handle, req, tags)
			if err := obs.PublishProfileHandle(handle); err != nil {
				return err
			}
			return obs.PublishProfilePhase(native.Phase)
		}
	}
	result, err := a.client.ExplainQuery(ctx, databricks.ExplainQueryRequest{
		StatementSubmitRequest: databricks.StatementSubmitRequest{
			SQL:           req.SQL,
			Host:          host,
			WarehouseID:   warehouseID,
			Catalog:       sessionString(req.Session, req.Metadata, "catalog"),
			Schema:        sessionString(req.Session, req.Metadata, "schema"),
			WaitTimeout:   50 * time.Second,
			OnWaitTimeout: "CANCEL",
			Disposition:   "INLINE",
			Format:        "JSON_ARRAY",
			RowLimit:      500,
			ByteLimit:     1 << 20,
			QueryTags:     tags,
		},
		Mode:          "COST",
		PollInterval:  500 * time.Millisecond,
		OnObservation: onObservation,
	})
	if err != nil {
		err = withDatabricksRetryAfter(err)
		return jobs.QueryProfile{Valid: false, Error: err.Error()}, err
	}
	snapshot := result.Statement
	for !snapshot.Terminal {
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return jobs.QueryProfile{Valid: false, Error: ctx.Err().Error()}, ctx.Err()
		case <-timer.C:
		}
		snapshot, err = a.client.GetStatement(ctx, snapshot.Handle)
		if err != nil {
			err = withDatabricksRetryAfter(err)
			return jobs.QueryProfile{Valid: false, Error: err.Error()}, err
		}
	}
	if !snapshot.Succeeded {
		message := "Databricks EXPLAIN failed"
		if snapshot.Error != nil && strings.TrimSpace(snapshot.Error.Message) != "" {
			message = strings.TrimSpace(snapshot.Error.Message)
		}
		return jobs.QueryProfile{Valid: false, Error: message}, errors.New(message)
	}
	plan := strings.TrimSpace(result.PlanText)
	planRows := result.PlanRows
	if plan == "" {
		materialized, materializeErr := a.client.MaterializeStatementResult(ctx, databricks.MaterializeStatementRequest{
			Handle:   snapshot.Handle,
			Snapshot: &snapshot,
			MaxRows:  500,
			MaxBytes: 1 << 20,
		})
		if materializeErr != nil {
			materializeErr = withDatabricksRetryAfter(materializeErr)
			return jobs.QueryProfile{Valid: false, Error: materializeErr.Error()}, materializeErr
		}
		planRows = materialized.Rows
		parts := make([]string, 0, len(planRows))
		for _, row := range planRows {
			for _, value := range row {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
					break
				}
			}
		}
		plan = strings.Join(parts, "\n")
	}
	if len(plan) > 16<<10 {
		plan = plan[:16<<10] + "…"
	}
	return jobs.QueryProfile{
		Valid:       true,
		PlanSummary: plan,
		Warnings: []string{
			"Databricks EXPLAIN COST estimates depend on table statistics and may be incomplete",
			"Databricks Statement Execution has no provider-side execution timeout; HardExecution is enforced by durable monitor cancellation",
		},
		ProviderDetails: map[string]any{
			"statement_id":                     snapshot.Handle.StatementID,
			"state":                            snapshot.State,
			"plan_rows":                        planRows,
			"provider_execution_cap_supported": false,
			"hard_execution_enforcement":       "durable_monitor_cancellation",
		},
	}, nil
}

func (a *Databricks) CancelProfile(ctx context.Context, handle jobs.NativeHandle) error {
	return a.Cancel(ctx, handle)
}

func (a *Databricks) Submit(ctx context.Context, req jobs.SubmitRequest) (jobs.SubmitResult, error) {
	if a == nil || a.client == nil {
		return jobs.SubmitResult{}, jobs.SubmitNotDelivered(errors.New("Databricks query adapter is not configured"))
	}
	host := firstNonBlank(metadataString(req.Metadata, "host"), req.ConnectionID)
	warehouseID := metadataString(req.Metadata, "warehouse_id")
	tags := databricksTags(req.IdempotencyKey, req.SQL)
	// Databricks Statement Execution exposes result limits and an inline wait
	// timeout, but no provider-side query execution timeout. WaitTimeout only
	// controls this HTTP response; HardExecution remains a durable monitor
	// cancellation budget and must not be misrepresented as a provider cap.
	snapshot, err := a.client.SubmitStatement(ctx, databricks.StatementSubmitRequest{
		SQL:           req.SQL,
		Host:          host,
		WarehouseID:   warehouseID,
		Catalog:       sessionString(req.Session, req.Metadata, "catalog"),
		Schema:        sessionString(req.Session, req.Metadata, "schema"),
		WaitTimeout:   300 * time.Millisecond,
		OnWaitTimeout: "CONTINUE",
		Disposition:   "INLINE",
		Format:        "JSON_ARRAY",
		RowLimit:      int64(req.Budgets.MaxResultRows),
		ByteLimit:     databricks.NormalizeStatementByteLimit("INLINE", req.Budgets.MaxResultBytes),
		QueryTags:     tags,
	})
	if err != nil {
		var apiErr *databricks.StatementAPIError
		if errors.As(err, &apiErr) && !apiErr.Ambiguous {
			return jobs.SubmitResult{}, withDatabricksRetryAfter(jobs.SubmitNotDelivered(err))
		}
		return jobs.SubmitResult{}, withDatabricksRetryAfter(err)
	}
	if strings.TrimSpace(snapshot.Handle.StatementID) == "" {
		return jobs.SubmitResult{}, errors.New("Databricks accepted the statement but returned no statement_id")
	}
	handle := databricksNativeHandle(snapshot.Handle, req, tags)
	initial := databricksStatus(snapshot, nil)
	if snapshot.Succeeded && snapshot.ResultAvailable && !snapshot.Closed {
		handle = databricksHandleWithResultManifest(handle, snapshot.Manifest, databricksFirstResultChunkIndex(snapshot))
		updated := handle
		initial.UpdatedHandle = &updated
	}
	return jobs.SubmitResult{Handle: handle, Initial: &initial}, nil
}

func (a *Databricks) Poll(ctx context.Context, handle jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	native, err := parseDatabricksHandle(handle)
	if err != nil {
		return jobs.StatusSnapshot{}, err
	}
	profile, profileErr := a.client.GetStatementProfile(ctx, native)
	if profile.Statement.Handle.StatementID == "" {
		if profileErr != nil {
			return jobs.StatusSnapshot{}, databricksObservationError(profileErr)
		}
		return jobs.StatusSnapshot{}, errors.New("Databricks returned an empty statement snapshot")
	}
	if databricksResultObservationLost(profile.Statement) {
		return jobs.StatusSnapshot{}, jobs.PermanentObservationLoss(errors.New("Databricks statement completed but its result is CLOSED or unavailable; durable recovery is required"))
	}
	status := databricksStatus(profile.Statement, profile.History)
	if profile.Statement.Succeeded && profile.Statement.ResultAvailable {
		updated := databricksHandleWithResultManifest(handle, profile.Statement.Manifest, databricksFirstResultChunkIndex(profile.Statement))
		status.UpdatedHandle = &updated
	}
	if profileErr != nil {
		// Query History enrichment is optional. A permission or transient
		// history error must not hide a valid Statement Execution observation.
		status.Telemetry["query_history_error"] = profileErr.Error()
		status.RetryAfter = databricksRetryAfter(profileErr)
	}
	return status, nil
}

func (a *Databricks) Fetch(ctx context.Context, handle jobs.NativeHandle, cursor jobs.FetchCursor) (jobs.ResultPage, error) {
	native, err := parseDatabricksHandle(handle)
	if err != nil {
		return jobs.ResultPage{}, err
	}
	// A terminal status response can carry and consume chunk zero, or advertise
	// a first manifest chunk without embedding it. Use the result context
	// persisted with the handle and request exactly the required chunk directly.
	// Re-reading the statement here can close an already-drained result and
	// Databricks then answers 400/404.
	if manifest, ok, manifestErr := databricksResultManifest(handle); manifestErr != nil {
		return jobs.ResultPage{}, manifestErr
	} else if ok {
		chunkIndex := databricksResultFirstChunkIndex(handle)
		rowOffset := 0
		if strings.TrimSpace(cursor.PageToken) != "" {
			var cursorErr error
			chunkIndex, rowOffset, _, cursorErr = databricksChunkCursor(cursor.PageToken, databricks.StatementSnapshot{Manifest: manifest})
			if cursorErr != nil {
				return jobs.ResultPage{}, cursorErr
			}
		}
		chunk, chunkErr := a.client.GetStatementResultChunk(ctx, native, chunkIndex)
		if chunkErr != nil {
			return jobs.ResultPage{}, databricksObservationError(chunkErr)
		}
		return databricksResultPage(manifest, chunk, rowOffset, cursor.MaxRows, cursor.MaxBytes)
	}
	snapshot, err := a.client.GetStatement(ctx, native)
	if err != nil {
		return jobs.ResultPage{}, databricksObservationError(err)
	}
	if !snapshot.Succeeded {
		return jobs.ResultPage{}, errors.New("Databricks results requested before successful completion")
	}
	if snapshot.Closed || !snapshot.ResultAvailable {
		return jobs.ResultPage{}, jobs.PermanentObservationLoss(errors.New("Databricks statement is CLOSED and its result is no longer available"))
	}
	chunkIndex, rowOffset, first, err := databricksChunkCursor(cursor.PageToken, snapshot)
	if err != nil {
		return jobs.ResultPage{}, err
	}
	chunk := snapshot.FirstChunk
	if !first || (len(chunk.DataArray) == 0 && snapshot.Manifest.TotalRowCount > 0) {
		chunk, err = a.client.GetStatementResultChunk(ctx, native, chunkIndex)
		if err != nil {
			return jobs.ResultPage{}, databricksObservationError(err)
		}
	}
	return databricksResultPage(snapshot.Manifest, chunk, rowOffset, cursor.MaxRows, cursor.MaxBytes)
}

func databricksResultPage(manifest databricks.StatementManifest, chunk databricks.ResultChunk, rowOffset, maxRows int, maxBytes int64) (jobs.ResultPage, error) {
	if len(chunk.ExternalLinks) > 0 && len(chunk.DataArray) == 0 {
		return jobs.ResultPage{}, errors.New("Databricks returned external result links; durable external-link materialization is not available")
	}
	rows := databricksRows(manifest.Columns, chunk.DataArray)
	if rowOffset < 0 || rowOffset > len(rows) {
		return jobs.ResultPage{}, errors.New("Databricks result page token row offset is out of range")
	}
	page := boundedResultPage(rows[rowOffset:], databricksSchema(manifest.Columns), maxRows, maxBytes)
	consumed := len(page.Rows)
	if rowOffset+consumed < len(rows) {
		if consumed == 0 {
			return jobs.ResultPage{}, errors.New("Databricks result page byte limit is too small for one row")
		}
		page.NextPageToken = fmt.Sprintf("dbx:%d:%d", chunk.ChunkIndex, rowOffset+consumed)
	} else if next, ok := nextDatabricksChunk(chunk, manifest); ok {
		page.NextPageToken = fmt.Sprintf("dbx:%d:0", next)
	}
	// manifest.truncated describes the bounded statement result as a whole,
	// not every intermediate chunk. Marking chunk zero truncated makes the
	// registry stop before following next_chunk_index.
	page.Truncated = page.Truncated || (manifest.Truncated && page.NextPageToken == "")
	page.Metadata = map[string]any{
		"chunk_index":        chunk.ChunkIndex,
		"row_offset":         chunk.RowOffset,
		"total_row_count":    manifest.TotalRowCount,
		"total_byte_count":   manifest.TotalByteCount,
		"provider_truncated": manifest.Truncated,
		"result_available":   true,
	}
	return page, nil
}

func (a *Databricks) Cancel(ctx context.Context, handle jobs.NativeHandle) error {
	native, err := parseDatabricksHandle(handle)
	if err != nil {
		return err
	}
	receipt, err := a.client.RequestCancelStatement(ctx, native)
	if err != nil {
		return withDatabricksRetryAfter(err)
	}
	if !receipt.Accepted {
		return errors.New("Databricks did not accept the cancellation request")
	}
	return nil
}

func (a *Databricks) ConfirmCancel(ctx context.Context, handle jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	status, err := a.Poll(ctx, handle)
	if err != nil {
		return false, jobs.StatusSnapshot{}, err
	}
	return status.State == jobs.ProviderStateCancelled, status, nil
}

func (a *Databricks) Adopt(ctx context.Context, handle jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	status, err := a.Poll(ctx, handle)
	if err != nil {
		if jobs.IsPermanentObservationLoss(err) || databricks.StatementHTTPStatus(err) == 404 {
			return jobs.AdoptOutcomeOrphaned, jobs.StatusSnapshot{}, nil
		}
		return "", jobs.StatusSnapshot{}, err
	}
	if providerTerminal(status.State) {
		return jobs.AdoptOutcomeTerminal, status, nil
	}
	return jobs.AdoptOutcomeAdopted, status, nil
}

func (a *Databricks) ReconcileSubmission(ctx context.Context, req jobs.ReconcileRequest) (jobs.ReconcileOutcome, error) {
	host := firstNonBlank(metadataString(req.Metadata, "host"), req.ConnectionID)
	warehouseID := metadataString(req.Metadata, "warehouse_id")
	tags := databricksTags(req.IdempotencyKey, req.SQL)
	start := req.SubmittedAt.Add(-5 * time.Minute).UnixMilli()
	end := time.Now().Add(time.Minute).UnixMilli()
	candidates, err := a.client.FindStatementCandidates(ctx, databricks.StatementReconcileRequest{
		Host:               host,
		WarehouseID:        warehouseID,
		SubmittedSQLSHA256: databricks.StatementSQLSHA256(req.SQL),
		RequiredTags:       databricksTagMap(tags),
		StartTimeMs:        start,
		EndTimeMs:          end,
		MaxPages:           5,
	})
	if err != nil {
		return jobs.ReconcileOutcome{}, withDatabricksRetryAfter(err)
	}
	if len(candidates) != 1 {
		// Zero can be history lag/permission filtering; multiple is a real
		// duplicate ambiguity. Neither is permission to guess or resubmit.
		return jobs.ReconcileOutcome{Resolution: jobs.ReconcileUnknown}, nil
	}
	native := databricks.StatementHandle{
		Host:        host,
		WarehouseID: warehouseID,
		StatementID: candidates[0].StatementID,
	}
	handle := databricksNativeHandle(native, jobs.SubmitRequest{
		SQL:            req.SQL,
		ConnectionID:   req.ConnectionID,
		IdempotencyKey: req.IdempotencyKey,
		Metadata:       req.Metadata,
	}, tags)
	snapshot, err := a.Poll(ctx, handle)
	if err != nil {
		return jobs.ReconcileOutcome{}, err
	}
	out := jobs.ReconcileOutcome{Resolution: jobs.ReconcileFound, Handle: handle}
	if providerTerminal(snapshot.State) {
		out.Snapshot = &snapshot
	}
	return out, nil
}

func databricksTags(idempotencyKey, sql string) []databricks.QueryTag {
	idemHash := sha256.Sum256([]byte(strings.TrimSpace(idempotencyKey)))
	sqlHash := databricks.StatementSQLSHA256(sql)
	return []databricks.QueryTag{
		{Key: "dbstream_idem", Value: hex.EncodeToString(idemHash[:16])},
		{Key: "dbstream_sql", Value: sqlHash[:32]},
	}
}

func databricksTagMap(tags []databricks.QueryTag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, tag := range tags {
		out[tag.Key] = tag.Value
	}
	return out
}

func databricksNativeHandle(native databricks.StatementHandle, req jobs.SubmitRequest, tags []databricks.QueryTag) jobs.NativeHandle {
	extra := map[string]string{
		"host":            native.Host,
		"warehouse_id":    native.WarehouseID,
		"catalog":         sessionString(req.Session, req.Metadata, "catalog"),
		"schema":          sessionString(req.Session, req.Metadata, "schema"),
		"sql_sha256":      databricks.StatementSQLSHA256(req.SQL),
		"idempotency_key": req.IdempotencyKey,
	}
	_ = putJSON(extra, "query_tags", tags)
	return jobs.NativeHandle{Provider: "databricks", ID: native.StatementID, Location: native.Host, Extra: extra}
}

// databricksHandleWithResultManifest persists the schema and aggregate result
// facts needed to decode later chunks. Chunk zero's rows deliberately do not
// live in the handle: they are handed to the registry as an observed page and
// materialized there. Omitting the manifest's chunk list keeps the durable
// handle bounded; every chunk response advertises its own successor.
func databricksHandleWithResultManifest(handle jobs.NativeHandle, manifest databricks.StatementManifest, firstChunkIndex int32) jobs.NativeHandle {
	handle.Extra = copyExtra(handle.Extra)
	manifest.Chunks = nil
	_ = putJSON(handle.Extra, "result_manifest", manifest)
	if firstChunkIndex < 0 {
		firstChunkIndex = 0
	}
	handle.Extra["result_first_chunk_index"] = strconv.FormatInt(int64(firstChunkIndex), 10)
	return handle
}

func databricksResultManifest(handle jobs.NativeHandle) (databricks.StatementManifest, bool, error) {
	if strings.TrimSpace(handle.Extra["result_manifest"]) == "" {
		return databricks.StatementManifest{}, false, nil
	}
	var manifest databricks.StatementManifest
	if err := getJSON(handle.Extra, "result_manifest", &manifest); err != nil {
		return databricks.StatementManifest{}, false, err
	}
	return manifest, true, nil
}

func databricksResultFirstChunkIndex(handle jobs.NativeHandle) int32 {
	value, err := strconv.ParseInt(strings.TrimSpace(handle.Extra["result_first_chunk_index"]), 10, 32)
	if err != nil || value < 0 {
		return 0
	}
	return int32(value)
}

func databricksFirstResultChunkIndex(snapshot databricks.StatementSnapshot) int32 {
	first := snapshot.FirstChunk
	if first.ChunkIndex != 0 || first.RowCount > 0 || len(first.DataArray) > 0 || len(first.ExternalLinks) > 0 {
		return first.ChunkIndex
	}
	if len(snapshot.Manifest.Chunks) > 0 && snapshot.Manifest.Chunks[0].ChunkIndex >= 0 {
		return snapshot.Manifest.Chunks[0].ChunkIndex
	}
	return 0
}

func parseDatabricksHandle(handle jobs.NativeHandle) (databricks.StatementHandle, error) {
	native := databricks.StatementHandle{
		Host:        firstNonBlank(handle.Extra["host"], handle.Location),
		WarehouseID: handle.Extra["warehouse_id"],
		StatementID: strings.TrimSpace(handle.ID),
	}
	if native.Host == "" || native.StatementID == "" {
		return databricks.StatementHandle{}, errors.New("Databricks native handle is missing host or statement_id")
	}
	return native, nil
}

func databricksStatus(snapshot databricks.StatementSnapshot, history *databricks.QueryHistoryEntry) jobs.StatusSnapshot {
	state := jobs.ProviderStateUnknown
	switch strings.ToUpper(strings.TrimSpace(snapshot.State)) {
	case "PENDING":
		state = jobs.ProviderStatePending
	case "RUNNING":
		state = jobs.ProviderStateRunning
	case "SUCCEEDED":
		state = jobs.ProviderStateSucceeded
	case "CLOSED":
		// CLOSED means Databricks has discarded the result. It is not a usable
		// success; Poll returns PermanentObservationLoss so the registry enters
		// recovery instead of publishing an empty completed result.
		state = jobs.ProviderStateUnknown
	case "FAILED":
		state = jobs.ProviderStateFailed
	case "CANCELED", "CANCELLED":
		state = jobs.ProviderStateCancelled
	}
	status := jobs.StatusSnapshot{
		State:    state,
		RawState: snapshot.State,
		Telemetry: map[string]any{
			"manifest":           snapshot.Manifest,
			"result_available":   snapshot.ResultAvailable,
			"provider_closed":    snapshot.Closed,
			"provider_truncated": snapshot.Manifest.Truncated,
		},
	}
	if snapshot.Error != nil {
		status.Error = firstNonBlank(snapshot.Error.Message, snapshot.Error.ErrorCode, "Databricks statement failed")
		status.Telemetry["sql_state"] = snapshot.Error.SQLState
		status.Telemetry["error_code"] = snapshot.Error.ErrorCode
	}
	if state == jobs.ProviderStateSucceeded && snapshot.ResultAvailable && !snapshot.Closed {
		first := snapshot.FirstChunk
		// An empty result still carries useful schema/row-count evidence. If a
		// non-empty result lacks an inline first chunk, leave pages empty and let
		// Fetch request the first manifest chunk through the provider.
		if len(first.DataArray) > 0 || snapshot.Manifest.TotalRowCount == 0 {
			if page, err := databricksResultPage(snapshot.Manifest, first, 0, 0, 0); err == nil {
				status.DrainedPages = []jobs.ResultPage{page}
			} else {
				status.Telemetry["inline_result_error"] = err.Error()
			}
		}
	}
	if history != nil {
		bytes := history.Metrics.ReadBytes
		rows := history.Metrics.RowsReadCount
		status.BytesProcessed = &bytes
		status.RowsProcessed = &rows
		status.Telemetry["query_history"] = history
		status.Telemetry["rows_produced"] = history.Metrics.RowsProducedCount
		status.Telemetry["projected_remaining_wallclock_ms"] = history.Metrics.ProjectedRemainingWallclockTimeMs
		status.Telemetry["remaining_task_count"] = history.Metrics.RemainingTaskCount
		status.Telemetry["spill_to_disk_bytes"] = history.Metrics.SpillToDiskBytes
	}
	return status
}

func withDatabricksRetryAfter(err error) error {
	return jobs.WithRetryAfter(err, databricksRetryAfter(err))
}

func databricksResultObservationLost(snapshot databricks.StatementSnapshot) bool {
	state := strings.ToUpper(strings.TrimSpace(snapshot.State))
	return snapshot.Closed || state == "CLOSED" ||
		(snapshot.Terminal && snapshot.Succeeded && !snapshot.ResultAvailable)
}

// Databricks uses 400 for some expired/closed result cursors in addition to
// the more conventional 404/410. These are deterministic observation losses;
// retrying the same cursor cannot recover it and must trigger the registry's
// recovery path rather than an endless failing fetch loop.
func databricksObservationError(err error) error {
	if err == nil {
		return nil
	}
	wrapped := withDatabricksRetryAfter(err)
	switch databricks.StatementHTTPStatus(err) {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusGone:
		return jobs.PermanentObservationLoss(wrapped)
	default:
		return wrapped
	}
}

func databricksRetryAfter(err error) time.Duration {
	var apiErr *databricks.StatementAPIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr.RetryAfter
	}
	return 0
}

func databricksChunkCursor(token string, snapshot databricks.StatementSnapshot) (int32, int, bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		index := snapshot.FirstChunk.ChunkIndex
		if len(snapshot.FirstChunk.DataArray) == 0 && len(snapshot.Manifest.Chunks) > 0 {
			index = snapshot.Manifest.Chunks[0].ChunkIndex
		}
		return index, 0, true, nil
	}
	if !strings.HasPrefix(token, "dbx:") {
		return 0, 0, false, errors.New("invalid Databricks result page token")
	}
	parts := strings.Split(strings.TrimPrefix(token, "dbx:"), ":")
	if len(parts) < 1 || len(parts) > 2 {
		return 0, 0, false, errors.New("invalid Databricks result page token")
	}
	value, err := strconv.ParseInt(parts[0], 10, 32)
	if err != nil || value < 0 {
		return 0, 0, false, errors.New("invalid Databricks result page token")
	}
	offset := int64(0)
	if len(parts) == 2 {
		offset, err = strconv.ParseInt(parts[1], 10, 32)
		if err != nil || offset < 0 {
			return 0, 0, false, errors.New("invalid Databricks result page token")
		}
	}
	return int32(value), int(offset), false, nil
}

func nextDatabricksChunk(chunk databricks.ResultChunk, manifest databricks.StatementManifest) (int32, bool) {
	if chunk.NextChunkIndex != nil {
		return *chunk.NextChunkIndex, true
	}
	if link := strings.TrimSpace(chunk.NextChunkInternalLink); link != "" {
		parsed, err := url.Parse(link)
		if err == nil {
			segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
			if len(segments) > 0 {
				if value, parseErr := strconv.ParseInt(segments[len(segments)-1], 10, 32); parseErr == nil && value >= 0 {
					return int32(value), true
				}
			}
		}
	}
	for index, item := range manifest.Chunks {
		if item.ChunkIndex == chunk.ChunkIndex && index+1 < len(manifest.Chunks) {
			return manifest.Chunks[index+1].ChunkIndex, true
		}
	}
	return 0, false
}

func databricksRows(columns []databricks.StatementColumn, data [][]any) []map[string]any {
	rows := make([]map[string]any, 0, len(data))
	for _, values := range data {
		row := make(map[string]any, len(values))
		for index, value := range values {
			name := fmt.Sprintf("col_%d", index)
			if index < len(columns) && strings.TrimSpace(columns[index].Name) != "" {
				name = strings.TrimSpace(columns[index].Name)
			}
			row[name] = value
		}
		rows = append(rows, row)
	}
	return rows
}

func databricksSchema(columns []databricks.StatementColumn) []map[string]any {
	schema := make([]map[string]any, 0, len(columns))
	for _, column := range columns {
		item := map[string]any{"name": column.Name}
		if value := firstNonBlank(column.TypeName, column.TypeText); value != "" {
			item["type"] = value
		}
		schema = append(schema, item)
	}
	return schema
}
