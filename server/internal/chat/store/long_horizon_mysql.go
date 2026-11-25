package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	chattypes "ai-analyst-local/internal/chat/types"
	mysqldriver "github.com/go-sql-driver/mysql"
)

var _ LongHorizonRepository = (*MySQLStore)(nil)

const queryJobSelectColumns = `id, provider, connection_id, connection_key, thread_id, run_id, owner_user_token, owner_handle,
	sql_text, sql_preview, execution_state, monitor_health, policy_health, materialization_state,
	validation_state, validation_detail, cancellation_reason, cancellation_outcome, cancel_started_at, native_handle, idempotency_key, replaces_job_id,
	keep_on_run_completion, submit_dispatched, query_profile, profile_outcome, profile_handle, profile_phase, profile_dispatched,
	budgets, telemetry, metadata, artifact_id, error_message,
	submitted_at, remote_submitted_at, next_poll_at, next_poll_action, started_at, completed_at, last_access_at, revision, drain_seq,
	drain_rows_committed, drain_bytes_committed, drain_truncated`

const queryCapacityGateLockSQL = `SELECT id FROM query_capacity_gate WHERE id=1 FOR UPDATE`

const runWakeCommitReconcileTimeout = 5 * time.Second

type sqlTxStarter interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func validateQueryJobRecord(record jobs.JobRecord) error {
	if err := validateQueryJobRecordIdentity(record); err != nil {
		return err
	}
	if strings.TrimSpace(record.Provider) == "" {
		return errors.New("query job provider is required")
	}
	if strings.TrimSpace(record.Owner.ThreadID) == "" {
		return errors.New("query job owner.thread_id is required")
	}
	if strings.TrimSpace(string(record.Status)) == "" {
		return errors.New("query job status is required")
	}
	if record.SubmittedAt.IsZero() {
		return errors.New("query job submitted_at is required")
	}
	if record.DrainSeq < 0 || record.DrainRowsCommitted < 0 || record.DrainBytesCommitted < 0 {
		return errors.New("query job drain sequence and accounting cannot be negative")
	}
	if record.LastAccess.IsZero() {
		record.LastAccess = record.SubmittedAt
	}
	return nil
}

func marshalNullableJSON(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if string(raw) == "null" || string(raw) == "{}" {
		return nil, nil
	}
	return string(raw), nil
}

func queryJobWriteValues(record jobs.JobRecord, revision int64) ([]any, error) {
	handle, err := marshalNullableJSON(record.Handle)
	if err != nil {
		return nil, fmt.Errorf("marshal native handle: %w", err)
	}
	budgets, err := marshalNullableJSON(record.Budgets)
	if err != nil {
		return nil, fmt.Errorf("marshal query budgets: %w", err)
	}
	profile, err := marshalNullableJSON(record.Profile)
	if err != nil {
		return nil, fmt.Errorf("marshal query profile: %w", err)
	}
	profileHandle, err := marshalNullableJSON(record.ProfileHandle)
	if err != nil {
		return nil, fmt.Errorf("marshal query profile handle: %w", err)
	}
	metadata, err := marshalNullableJSON(record.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal query metadata: %w", err)
	}
	var telemetry any
	if record.Metadata != nil {
		telemetry, err = marshalNullableJSON(record.Metadata["telemetry"])
		if err != nil {
			return nil, fmt.Errorf("marshal query telemetry: %w", err)
		}
	}
	validationState := record.ValidationState
	if validationState == "" {
		validationState = jobs.ValidationUnvalidated
	}
	lastAccess := record.LastAccess.UTC()
	if lastAccess.IsZero() {
		lastAccess = record.SubmittedAt.UTC()
	}
	return []any{
		record.ID, strings.ToLower(strings.TrimSpace(record.Provider)), nullIfBlank(record.ConnectionID), queryConnectionKey(record),
		record.Owner.ThreadID, nullIfBlank(record.Owner.RunID), nullIfBlank(record.Owner.UserToken), nullIfBlank(record.Owner.Handle),
		nullIfBlank(record.SQL), nullIfBlank(record.SQLPreview), string(record.Status), string(record.MonitorHealth),
		string(record.PolicyHealth), string(record.MaterializationState), string(validationState), nullIfBlank(record.ValidationDetail), nullIfBlank(string(record.CancelReason)),
		nullIfBlank(string(record.CancelOutcome)), record.CancelStartedAt, handle, nullIfBlank(record.IdempotencyKey), nullIfBlank(record.ReplacesJobID),
		record.KeepOnRunCompletion, record.SubmitDispatched, profile, nullIfBlank(string(record.ProfileOutcome)), profileHandle,
		nullIfBlank(record.ProfilePhase), record.ProfileDispatched, budgets, telemetry, metadata, nullIfBlank(record.ArtifactID), nullIfBlank(record.Error),
		record.SubmittedAt.UTC(), record.RemoteSubmittedAt, record.NextPollAt, nullIfBlank(string(record.NextPollAction)), record.StartedAt, record.CompletedAt, lastAccess, revision, record.DrainSeq,
		record.DrainRowsCommitted, record.DrainBytesCommitted, record.DrainTruncated,
	}, nil
}

func insertQueryJobTx(ctx context.Context, tx *sql.Tx, record jobs.JobRecord, revision int64) error {
	values, err := queryJobWriteValues(record, revision)
	if err != nil {
		return err
	}
	now := nowUTC()
	values = append(values, now, now)
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	_, err = tx.ExecContext(ctx, `INSERT INTO query_jobs (`+queryJobSelectColumns+`, created_at, updated_at)
		VALUES (`+placeholders+`)`, values...)
	if err != nil && isDuplicateKeyError(err) {
		return jobs.ErrRevisionConflict
	}
	return err
}

const queryJobUpdateSQL = `UPDATE query_jobs SET
	provider=?, connection_id=?, connection_key=?, thread_id=?, run_id=?, owner_user_token=?, owner_handle=?,
	sql_text=?, sql_preview=?, execution_state=?, monitor_health=?, policy_health=?, materialization_state=?,
	validation_state=?, validation_detail=?, cancellation_reason=?, cancellation_outcome=?, cancel_started_at=?, native_handle=?, idempotency_key=?, replaces_job_id=?,
	keep_on_run_completion=?, submit_dispatched=?, query_profile=?, profile_outcome=?, profile_handle=?, profile_phase=?, profile_dispatched=?,
	budgets=?, telemetry=?, metadata=?, artifact_id=?, error_message=?,
	submitted_at=?, remote_submitted_at=?, next_poll_at=?, next_poll_action=?, started_at=?, completed_at=?, last_access_at=?, revision=?, drain_seq=?,
	drain_rows_committed=?, drain_bytes_committed=?, drain_truncated=?, updated_at=?`

func queryJobUpdateValues(record jobs.JobRecord, revision int64) ([]any, error) {
	values, err := queryJobWriteValues(record, revision)
	if err != nil {
		return nil, err
	}
	// id is the immutable key and is not updated.
	values = values[1:]
	return append(values, nowUTC()), nil
}

func updateQueryJobTx(ctx context.Context, tx *sql.Tx, record jobs.JobRecord, revision int64) error {
	values, err := queryJobUpdateValues(record, revision)
	if err != nil {
		return err
	}
	values = append(values, record.ID)
	result, err := tx.ExecContext(ctx, queryJobUpdateSQL+` WHERE id=?`, values...)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func updateQueryJobTxFenced(ctx context.Context, tx *sql.Tx, record jobs.JobRecord, revision, expectedRevision, expectedDrainSeq int64, expectedStatus jobs.JobStatus, holder string) error {
	values, err := queryJobUpdateValues(record, revision)
	if err != nil {
		return err
	}
	values = append(values, record.ID, expectedRevision, expectedDrainSeq, string(expectedStatus), holder)
	result, err := tx.ExecContext(ctx, queryJobUpdateSQL+` WHERE id=? AND revision=? AND drain_seq=?
		AND execution_state=? AND monitor_lease_holder=? AND monitor_lease_expires_at>UTC_TIMESTAMP(3)`, values...)
	if err != nil {
		return err
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return rowsErr
	} else if affected == 0 {
		return jobs.ErrLeaseLost
	}
	return nil
}

func lockMySQLQueryJobTx(ctx context.Context, tx *sql.Tx, jobID string) (jobs.JobRecord, error) {
	return scanMySQLQueryJob(tx.QueryRowContext(ctx, `SELECT `+queryJobSelectColumns+` FROM query_jobs WHERE id=? FOR UPDATE`, jobID))
}

func freezeMySQLQueryJobIdentityTx(ctx context.Context, tx *sql.Tx, current jobs.JobRecord, proposed *jobs.JobRecord) error {
	if proposed == nil {
		return freezeQueryJobIdentity(current, proposed)
	}
	requestedRunID := strings.TrimSpace(proposed.Owner.RunID)
	if err := freezeQueryJobIdentity(current, proposed); err != nil {
		return err
	}
	if strings.TrimSpace(current.Owner.RunID) != "" || requestedRunID == "" {
		return nil
	}
	if jobs.RequiresQueryObligationBinding(current) {
		// Managed jobs were scoped before provider work. A blank durable run id
		// is a deletion tombstone, not an invitation to adopt a newer run.
		return nil
	}
	var threadID string
	err := tx.QueryRowContext(ctx, `SELECT thread_id FROM runs WHERE id=?`, requestedRunID).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		// ON DELETE SET NULL won while this process still held the old run id.
		// Keep the durable blank and allow the lifecycle transition to proceed.
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(threadID) != strings.TrimSpace(current.Owner.ThreadID) {
		return fmt.Errorf("query job %s cannot attach to a run in another thread: %w", current.ID, ErrConflict)
	}
	proposed.Owner.RunID = requestedRunID
	return nil
}

func (s *MySQLStore) UpsertQueryJob(ctx context.Context, record jobs.JobRecord) error {
	if err := validateQueryJobRecord(record); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := lockMySQLQueryJobTx(ctx, tx, record.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		record.Revision = 1
		if err := insertQueryJobTx(ctx, tx, record, record.Revision); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if err := freezeMySQLQueryJobIdentityTx(ctx, tx, current, &record); err != nil {
			return err
		}
		record.Revision = current.Revision + 1
		if err := updateQueryJobTx(ctx, tx, record, record.Revision); err != nil {
			return err
		}
	}
	if err := syncWakeEventsMySQLTx(ctx, tx, record); err != nil {
		return err
	}
	if err := syncCapacityReservationMySQLTx(ctx, tx, record); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *MySQLStore) UpsertQueryJobCAS(ctx context.Context, record jobs.JobRecord, expectedRevision int64) (int64, error) {
	if err := validateQueryJobRecord(record); err != nil {
		return 0, err
	}
	if expectedRevision < 0 {
		return 0, jobs.ErrRevisionConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	current, err := lockMySQLQueryJobTx(ctx, tx, record.ID)
	if errors.Is(err, sql.ErrNoRows) {
		if expectedRevision != 0 {
			return 0, jobs.ErrRevisionConflict
		}
		record.Revision = 1
		if err := insertQueryJobTx(ctx, tx, record, record.Revision); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	} else {
		if current.Revision != expectedRevision {
			return 0, jobs.ErrRevisionConflict
		}
		if err := freezeMySQLQueryJobIdentityTx(ctx, tx, current, &record); err != nil {
			return 0, err
		}
		record.Revision = current.Revision + 1
		if err := updateQueryJobTx(ctx, tx, record, record.Revision); err != nil {
			return 0, err
		}
	}
	if err := syncWakeEventsMySQLTx(ctx, tx, record); err != nil {
		return 0, err
	}
	if err := syncCapacityReservationMySQLTx(ctx, tx, record); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return record.Revision, nil
}

func isActiveQueryExecutionState(status jobs.JobStatus) bool {
	switch status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted, jobs.JobStatusSubmissionUnknown,
		jobs.JobStatusRunning, jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return true
	default:
		return false
	}
}

func queryConnectionKey(record jobs.JobRecord) string {
	if value := strings.TrimSpace(record.ConnectionID); value != "" {
		return strings.ToLower(value)
	}
	return strings.ToLower(strings.TrimSpace(record.Provider))
}

func syncCapacityReservationMySQLTx(ctx context.Context, tx *sql.Tx, record jobs.JobRecord) error {
	if isActiveQueryExecutionState(record.Status) {
		_, err := tx.ExecContext(ctx, `INSERT INTO query_capacity_reservations
			(job_id, thread_id, connection_key, reservation_state, reserved_at, released_at)
			VALUES (?, ?, ?, 'active', ?, NULL)
			ON DUPLICATE KEY UPDATE reservation_state='active', released_at=NULL`,
			record.ID, record.Owner.ThreadID, queryConnectionKey(record), record.SubmittedAt.UTC())
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE query_capacity_reservations
		SET reservation_state='released', released_at=COALESCE(released_at, ?)
		WHERE job_id=? AND reservation_state='active'`, nowUTC(), record.ID)
	return err
}

func wakeObservationKey(event jobs.WakeEvent) string {
	material := event.At.UTC().Format(time.RFC3339Nano) + "\x00" + string(event.Kind) + "\x00" + event.Detail
	sum := sha256.Sum256([]byte(material))
	return "wake:" + hex.EncodeToString(sum[:])
}

func syncWakeEventsMySQLTx(ctx context.Context, tx *sql.Tx, record jobs.JobRecord) error {
	for _, event := range record.WakeEvents {
		at := event.At.UTC()
		if at.IsZero() {
			at = nowUTC()
		}
		key := wakeObservationKey(event)
		var sequence int64
		err := tx.QueryRowContext(ctx, `SELECT sequence FROM query_job_events WHERE job_id=? AND observation_key=?`, record.ID, key).Scan(&sequence)
		if errors.Is(err, sql.ErrNoRows) {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0)+1 FROM query_job_events WHERE job_id=? FOR UPDATE`, record.ID).Scan(&sequence); err != nil {
				return err
			}
			var claimedBy any
			var claimedAt any
			if event.Claimed {
				claimedBy, claimedAt = "record", at
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO query_job_events
				(job_id, sequence, event_type, wake_kind, detail, payload, observation_key, created_at, claimed_by, claimed_at)
				VALUES (?, ?, 'wake', ?, ?, NULL, ?, ?, ?, ?)`,
				record.ID, sequence, string(event.Kind), nullIfBlank(event.Detail), key, at, claimedBy, claimedAt)
			if err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if event.Claimed {
			if _, err := tx.ExecContext(ctx, `UPDATE query_job_events SET claimed_by=COALESCE(claimed_by, 'record'), claimed_at=COALESCE(claimed_at, ?) WHERE job_id=? AND sequence=?`, at, record.ID, sequence); err != nil {
				return err
			}
		}
	}
	return nil
}

type jobRowScanner interface {
	Scan(...any) error
}

func scanMySQLQueryJob(scanner jobRowScanner) (jobs.JobRecord, error) {
	record, err := scanMySQLQueryJobRaw(scanner)
	if err != nil {
		return jobs.JobRecord{}, err
	}
	if err := sanitizeLegacyQueryJobRecordIdentity(&record); err != nil {
		return jobs.JobRecord{}, err
	}
	return record, nil
}

func scanMySQLQueryJobRaw(scanner jobRowScanner) (jobs.JobRecord, error) {
	var record jobs.JobRecord
	var connectionID, runID, userToken, ownerHandle sql.NullString
	var connectionKey string
	var sqlText, sqlPreview, validationDetail, cancelReason, cancelOutcome sql.NullString
	var idempotencyKey, replacesJobID sql.NullString
	var handleRaw, profileRaw, profileHandleRaw, budgetsRaw, telemetryRaw, metadataRaw []byte
	var artifactID, errorMessage sql.NullString
	var cancelStartedAt, remoteSubmittedAt, nextPollAt, startedAt, completedAt sql.NullTime
	var nextPollAction sql.NullString
	var keep, submitDispatched, profileDispatched bool
	var status, monitorHealth, policyHealth, materializationState, validationState string
	var profileOutcome, profilePhase sql.NullString
	err := scanner.Scan(
		&record.ID, &record.Provider, &connectionID, &connectionKey, &record.Owner.ThreadID, &runID, &userToken, &ownerHandle,
		&sqlText, &sqlPreview, &status, &monitorHealth, &policyHealth, &materializationState,
		&validationState, &validationDetail, &cancelReason, &cancelOutcome, &cancelStartedAt, &handleRaw, &idempotencyKey, &replacesJobID,
		&keep, &submitDispatched, &profileRaw, &profileOutcome, &profileHandleRaw, &profilePhase, &profileDispatched,
		&budgetsRaw, &telemetryRaw, &metadataRaw, &artifactID, &errorMessage,
		&record.SubmittedAt, &remoteSubmittedAt, &nextPollAt, &nextPollAction, &startedAt, &completedAt, &record.LastAccess, &record.Revision, &record.DrainSeq,
		&record.DrainRowsCommitted, &record.DrainBytesCommitted, &record.DrainTruncated,
	)
	if err != nil {
		return jobs.JobRecord{}, err
	}
	record.ConnectionID = connectionID.String
	if connectionKey != queryConnectionKey(record) {
		return jobs.JobRecord{}, fmt.Errorf("query job %s has non-canonical connection key", record.ID)
	}
	record.Owner.RunID = runID.String
	record.Owner.UserToken = userToken.String
	record.Owner.Handle = ownerHandle.String
	record.SQL = sqlText.String
	record.SQLPreview = sqlPreview.String
	record.Status = jobs.JobStatus(status)
	record.MonitorHealth = jobs.MonitorHealth(monitorHealth)
	record.PolicyHealth = jobs.PolicyHealth(policyHealth)
	record.MaterializationState = jobs.MaterializationState(materializationState)
	record.ValidationState = jobs.ValidationState(validationState)
	record.ValidationDetail = validationDetail.String
	record.CancelReason = jobs.CancelReason(cancelReason.String)
	record.CancelOutcome = jobs.CancelOutcome(cancelOutcome.String)
	if cancelStartedAt.Valid {
		value := cancelStartedAt.Time
		record.CancelStartedAt = &value
	}
	record.IdempotencyKey = idempotencyKey.String
	record.ReplacesJobID = replacesJobID.String
	record.KeepOnRunCompletion = keep
	record.SubmitDispatched = submitDispatched
	record.ProfileOutcome = jobs.ProfileOutcome(profileOutcome.String)
	record.ProfilePhase = profilePhase.String
	record.ProfileDispatched = profileDispatched
	record.NextPollAction = jobs.RetryAction(nextPollAction.String)
	record.ArtifactID = artifactID.String
	record.Error = errorMessage.String
	if remoteSubmittedAt.Valid {
		value := remoteSubmittedAt.Time
		record.RemoteSubmittedAt = &value
	}
	if nextPollAt.Valid {
		value := nextPollAt.Time
		record.NextPollAt = &value
	}
	if startedAt.Valid {
		value := startedAt.Time
		record.StartedAt = &value
	}
	if completedAt.Valid {
		value := completedAt.Time
		record.CompletedAt = &value
	}
	if len(handleRaw) > 0 {
		if err := json.Unmarshal(handleRaw, &record.Handle); err != nil {
			return jobs.JobRecord{}, fmt.Errorf("decode native handle for %s: %w", record.ID, err)
		}
	}
	if len(profileRaw) > 0 {
		var profile jobs.QueryProfile
		if err := json.Unmarshal(profileRaw, &profile); err != nil {
			return jobs.JobRecord{}, fmt.Errorf("decode query profile for %s: %w", record.ID, err)
		}
		record.Profile = &profile
	}
	if len(profileHandleRaw) > 0 {
		if err := json.Unmarshal(profileHandleRaw, &record.ProfileHandle); err != nil {
			return jobs.JobRecord{}, fmt.Errorf("decode query profile handle for %s: %w", record.ID, err)
		}
	}
	if len(budgetsRaw) > 0 {
		if err := json.Unmarshal(budgetsRaw, &record.Budgets); err != nil {
			return jobs.JobRecord{}, fmt.Errorf("decode budgets for %s: %w", record.ID, err)
		}
	}
	if len(metadataRaw) > 0 {
		if err := json.Unmarshal(metadataRaw, &record.Metadata); err != nil {
			return jobs.JobRecord{}, fmt.Errorf("decode metadata for %s: %w", record.ID, err)
		}
	}
	if len(telemetryRaw) > 0 {
		if record.Metadata == nil {
			record.Metadata = map[string]any{}
		}
		var telemetry any
		if err := json.Unmarshal(telemetryRaw, &telemetry); err != nil {
			return jobs.JobRecord{}, fmt.Errorf("decode telemetry for %s: %w", record.ID, err)
		}
		record.Metadata["telemetry"] = telemetry
	}
	return record, nil
}

func (s *MySQLStore) GetQueryJob(ctx context.Context, id string) (jobs.JobRecord, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return jobs.JobRecord{}, false, errors.New("query job id is required")
	}
	if jobs.IsQueryJobIDSentinel(id) {
		return jobs.JobRecord{}, false, nil
	}
	record, err := scanMySQLQueryJob(s.db.QueryRowContext(ctx, `SELECT `+queryJobSelectColumns+` FROM query_jobs WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.JobRecord{}, false, nil
	}
	if err != nil {
		return jobs.JobRecord{}, false, err
	}
	wakes, err := s.listMySQLWakeEvents(ctx, id)
	if err != nil {
		return jobs.JobRecord{}, false, err
	}
	record.WakeEvents = wakes
	return record, true, nil
}

func (s *MySQLStore) GetQueryJobByIdempotency(ctx context.Context, provider, connectionKey, key string) (jobs.JobRecord, bool, error) {
	provider, connectionKey, key, err := normalizeQueryIdempotencyIdentity(provider, connectionKey, key)
	if err != nil {
		return jobs.JobRecord{}, false, err
	}
	// Decode without applying the read-side quarantine first. Returning a
	// quarantined idempotency winner as "not found" would let the registry
	// submit the same provider query again. Repair that winner in place (or
	// surface its live monitor lease) before the caller may proceed.
	sawRepairableWinner := false
	for attempt := 0; attempt < 3; attempt++ {
		record, lookupErr := scanMySQLQueryJobRaw(s.db.QueryRowContext(ctx, `SELECT `+queryJobSelectColumns+`
			FROM query_jobs WHERE provider=? AND connection_key=? AND idempotency_key=?
			ORDER BY submitted_at, id LIMIT 1`, provider, connectionKey, key))
		if errors.Is(lookupErr, sql.ErrNoRows) {
			if sawRepairableWinner {
				return jobs.JobRecord{}, false, fmt.Errorf("query job idempotency winner disappeared during identity repair: %w", ErrConflict)
			}
			return jobs.JobRecord{}, false, nil
		}
		if lookupErr != nil {
			return jobs.JobRecord{}, false, lookupErr
		}
		if queryJobIdentityNeedsRepair(record.ID) {
			sawRepairableWinner = true
			repaired, repairErr := s.repairMySQLQueryJobIdentity(ctx, record.ID)
			if errors.Is(repairErr, ErrNotFound) {
				// A concurrent repair may have committed between the lookup and
				// our row lock. Re-read the unique idempotency winner.
				continue
			}
			if repairErr != nil {
				return jobs.JobRecord{}, false, repairErr
			}
			return repaired, true, nil
		}
		if err := sanitizeLegacyQueryJobRecordIdentity(&record); err != nil {
			// A non-repairable corrupt winner is still an error, never absence.
			return jobs.JobRecord{}, false, err
		}
		wakes, wakeErr := s.listMySQLWakeEvents(ctx, record.ID)
		if wakeErr != nil {
			return jobs.JobRecord{}, false, wakeErr
		}
		record.WakeEvents = wakes
		return record, true, nil
	}
	return jobs.JobRecord{}, false, fmt.Errorf("query job identity repair did not converge: %w", ErrConflict)
}

func (s *MySQLStore) listMySQLWakeEvents(ctx context.Context, jobID string) ([]jobs.WakeEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT wake_kind, detail, created_at, claimed_at FROM query_job_events WHERE job_id=? AND wake_kind IS NOT NULL ORDER BY sequence`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []jobs.WakeEvent
	for rows.Next() {
		var kind string
		var detail sql.NullString
		var at time.Time
		var claimedAt sql.NullTime
		if err := rows.Scan(&kind, &detail, &at, &claimedAt); err != nil {
			return nil, err
		}
		out = append(out, jobs.WakeEvent{At: at, Kind: jobs.WakeKind(kind), Detail: detail.String, Claimed: claimedAt.Valid})
	}
	return out, rows.Err()
}

func (s *MySQLStore) listQueryJobIDs(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *MySQLStore) queryJobsByIDs(ctx context.Context, ids []string) ([]jobs.JobRecord, error) {
	out := make([]jobs.JobRecord, 0, len(ids))
	for _, id := range ids {
		if queryJobIdentityNeedsRepair(id) {
			repaired, err := s.repairMySQLQueryJobIdentity(ctx, id)
			if errors.Is(err, ErrNotFound) {
				// Another process atomically re-keyed the row after this listing
				// selected the legacy id. Omit it for this snapshot; the next bounded
				// scan will select the repaired identity instead of failing recovery.
				continue
			}
			if errors.Is(err, jobs.ErrLeaseLost) {
				// A live legacy process still owns this job. Leave its identity and
				// capacity intact; a later recovery scan repairs it after lease expiry.
				continue
			}
			if err != nil {
				return nil, err
			}
			out = append(out, repaired)
			continue
		}
		record, ok, err := s.GetQueryJob(ctx, id)
		if errors.Is(err, ErrQuarantinedQueryJobIdentity) {
			record, err = s.repairMySQLQueryJobIdentity(ctx, id)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if errors.Is(err, jobs.ErrLeaseLost) {
				continue
			}
			ok = err == nil
		}
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *MySQLStore) repairMySQLQueryJobIdentity(ctx context.Context, oldID string) (jobs.JobRecord, error) {
	oldID = strings.TrimSpace(oldID)
	if !queryJobIdentityNeedsRepair(oldID) {
		return jobs.JobRecord{}, fmt.Errorf("query job %q does not require identity repair: %w", oldID, ErrConflict)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return jobs.JobRecord{}, err
	}
	defer tx.Rollback()
	raw, err := scanMySQLQueryJobRaw(tx.QueryRowContext(ctx, `SELECT `+queryJobSelectColumns+` FROM query_jobs WHERE id=? FOR UPDATE`, oldID))
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.JobRecord{}, ErrNotFound
	}
	if err != nil {
		return jobs.JobRecord{}, err
	}
	if strings.TrimSpace(raw.ID) != oldID || !queryJobIdentityNeedsRepair(raw.ID) {
		return jobs.JobRecord{}, ErrConflict
	}
	var leaseHolder sql.NullString
	var leaseExpires sql.NullTime
	var createdAt, updatedAt, databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT monitor_lease_holder, monitor_lease_expires_at,
		created_at, updated_at, UTC_TIMESTAMP(3) FROM query_jobs WHERE id=? FOR UPDATE`, oldID).
		Scan(&leaseHolder, &leaseExpires, &createdAt, &updatedAt, &databaseNow); err != nil {
		return jobs.JobRecord{}, err
	}
	if leaseHolder.Valid && strings.TrimSpace(leaseHolder.String) != "" && leaseExpires.Valid && leaseExpires.Time.After(databaseNow) {
		return jobs.JobRecord{}, jobs.ErrLeaseLost
	}

	repaired := raw
	repaired.ID = repairedManagedQueryJobID(raw)
	if strings.TrimSpace(repaired.ReplacesJobID) == oldID || jobs.IsQueryJobIDSentinel(repaired.ReplacesJobID) {
		repaired.ReplacesJobID = ""
	}
	if err := sanitizeLegacyQueryJobRecordIdentity(&repaired); err != nil {
		return jobs.JobRecord{}, err
	}
	if err := validateQueryJobRecord(repaired); err != nil {
		return jobs.JobRecord{}, err
	}
	if _, err := lockMySQLQueryJobTx(ctx, tx, repaired.ID); err == nil {
		return jobs.JobRecord{}, fmt.Errorf("repaired query job identity collision at %s: %w", repaired.ID, ErrConflict)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return jobs.JobRecord{}, err
	}
	if strings.TrimSpace(raw.IdempotencyKey) != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE query_jobs SET idempotency_key=NULL WHERE id=?`, oldID); err != nil {
			return jobs.JobRecord{}, err
		}
	}
	if err := insertQueryJobTx(ctx, tx, repaired, repaired.Revision); err != nil {
		return jobs.JobRecord{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE query_jobs SET created_at=?, updated_at=?,
		monitor_lease_holder=NULL, monitor_lease_expires_at=NULL WHERE id=?`, createdAt, updatedAt, repaired.ID); err != nil {
		return jobs.JobRecord{}, err
	}

	for _, statement := range []string{
		`UPDATE query_job_events SET job_id=? WHERE job_id=?`,
		`UPDATE query_result_chunks SET job_id=? WHERE job_id=?`,
		`UPDATE run_query_dependencies SET job_id=? WHERE job_id=?`,
		`UPDATE query_capacity_reservations SET job_id=? WHERE job_id=?`,
		`UPDATE query_jobs SET replaces_job_id=? WHERE replaces_job_id=?`,
		`UPDATE run_query_dependencies SET replacement_job_id=? WHERE replacement_job_id=?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, repaired.ID, oldID); err != nil {
			return jobs.JobRecord{}, err
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, dependency_ids FROM run_continuation_checkpoints FOR UPDATE`)
	if err != nil {
		return jobs.JobRecord{}, err
	}
	type checkpointRepair struct {
		id  string
		raw []byte
	}
	var checkpointRepairs []checkpointRepair
	for rows.Next() {
		var id string
		var rawDependencies []byte
		if err := rows.Scan(&id, &rawDependencies); err != nil {
			rows.Close()
			return jobs.JobRecord{}, err
		}
		var dependencyIDs []string
		if len(rawDependencies) > 0 {
			if err := json.Unmarshal(rawDependencies, &dependencyIDs); err != nil {
				rows.Close()
				return jobs.JobRecord{}, err
			}
		}
		changed := false
		for index := range dependencyIDs {
			if strings.TrimSpace(dependencyIDs[index]) == oldID {
				dependencyIDs[index] = repaired.ID
				changed = true
			}
		}
		if changed {
			encoded, err := json.Marshal(dependencyIDs)
			if err != nil {
				rows.Close()
				return jobs.JobRecord{}, err
			}
			checkpointRepairs = append(checkpointRepairs, checkpointRepair{id: id, raw: encoded})
		}
	}
	if err := rows.Close(); err != nil {
		return jobs.JobRecord{}, err
	}
	if err := rows.Err(); err != nil {
		return jobs.JobRecord{}, err
	}
	for _, checkpoint := range checkpointRepairs {
		if _, err := tx.ExecContext(ctx, `UPDATE run_continuation_checkpoints SET dependency_ids=? WHERE id=?`, checkpoint.raw, checkpoint.id); err != nil {
			return jobs.JobRecord{}, err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM query_jobs WHERE id=?`, oldID)
	if err != nil {
		return jobs.JobRecord{}, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err != nil {
			return jobs.JobRecord{}, err
		}
		return jobs.JobRecord{}, ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return jobs.JobRecord{}, err
	}
	stored, found, err := s.GetQueryJob(ctx, repaired.ID)
	if err != nil {
		return jobs.JobRecord{}, err
	}
	if !found {
		return jobs.JobRecord{}, ErrNotFound
	}
	return stored, nil
}

func (s *MySQLStore) ListActiveQueryJobs(ctx context.Context) ([]jobs.JobRecord, error) {
	ids, err := s.listQueryJobIDs(ctx, `SELECT id FROM query_jobs WHERE execution_state IN ('reserved','profiling','submitted','submission_unknown','running','cancel_requested','cancelling') ORDER BY submitted_at`)
	if err != nil {
		return nil, err
	}
	return s.queryJobsByIDs(ctx, ids)
}

func (s *MySQLStore) ListQueryJobsByThread(ctx context.Context, threadID string) ([]jobs.JobRecord, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, errors.New("thread id is required")
	}
	ids, err := s.listQueryJobIDs(ctx, `SELECT id FROM query_jobs WHERE thread_id=? ORDER BY submitted_at`, threadID)
	if err != nil {
		return nil, err
	}
	return s.queryJobsByIDs(ctx, ids)
}

func (s *MySQLStore) DeleteQueryJob(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("query job id is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM query_jobs WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MySQLStore) ReserveQueryCapacity(ctx context.Context, record jobs.JobRecord, limits jobs.CapacityLimits) (bool, int64, error) {
	if err := validateQueryJobRecord(record); err != nil {
		return false, 0, err
	}
	if limits.MaxGlobal <= 0 {
		limits.MaxGlobal = 8
	}
	if limits.MaxPerThread <= 0 {
		limits.MaxPerThread = 3
	}
	if limits.MaxPerConnection <= 0 {
		limits.MaxPerConnection = 2
	}
	if limits.MaxProfilingGlobal <= 0 {
		limits.MaxProfilingGlobal = 4
	}
	if limits.MaxProfilingPerThread <= 0 {
		limits.MaxProfilingPerThread = 1
	}
	if limits.MaxProfilingPerConnection <= 0 {
		limits.MaxProfilingPerConnection = 1
	}
	revision, err := retryMySQLCapacityTransaction(ctx, func() (int64, error) {
		return s.reserveQueryJobOnce(ctx, record, limits)
	})
	if errors.Is(err, jobs.ErrConcurrencyLimit) {
		return false, 0, nil
	}
	return err == nil, revision, err
}

func (s *MySQLStore) reserveQueryJobOnce(ctx context.Context, record jobs.JobRecord, limits jobs.CapacityLimits) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var gateID int
	if err := tx.QueryRowContext(ctx, queryCapacityGateLockSQL).Scan(&gateID); err != nil {
		return 0, err
	}
	if gateID != 1 {
		return 0, errors.New("query capacity gate is not initialized")
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE id=? FOR UPDATE`, record.ID).Scan(&existing); err != nil {
		return 0, err
	}
	if existing != 0 {
		return 0, jobs.ErrRevisionConflict
	}
	if key := strings.TrimSpace(record.IdempotencyKey); key != "" {
		var winnerID string
		err := tx.QueryRowContext(ctx, `SELECT id FROM query_jobs
			WHERE provider=? AND connection_key=? AND idempotency_key=?
			ORDER BY submitted_at, id LIMIT 1 FOR UPDATE`,
			strings.ToLower(strings.TrimSpace(record.Provider)), queryConnectionKey(record), key).Scan(&winnerID)
		if err == nil {
			return 0, jobs.ErrRevisionConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	var globalCount, threadCount, connectionCount int
	const activeStates = `'reserved','profiling','submitted','submission_unknown','running','cancel_requested','cancelling'`
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE execution_state IN (`+activeStates+`)`).Scan(&globalCount); err != nil {
		return 0, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE execution_state IN (`+activeStates+`) AND thread_id=?`, record.Owner.ThreadID).Scan(&threadCount); err != nil {
		return 0, err
	}
	connectionKey := queryConnectionKey(record)
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE execution_state IN (`+activeStates+`) AND connection_key=?`, connectionKey).Scan(&connectionCount); err != nil {
		return 0, err
	}
	if globalCount >= limits.MaxGlobal || threadCount >= limits.MaxPerThread || connectionCount >= limits.MaxPerConnection {
		return 0, jobs.ErrConcurrencyLimit
	}
	if record.Status == jobs.JobStatusProfiling {
		var profilingGlobal, profilingThread, profilingConnection int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE execution_state='profiling'`).Scan(&profilingGlobal); err != nil {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE execution_state='profiling' AND thread_id=?`, record.Owner.ThreadID).Scan(&profilingThread); err != nil {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE execution_state='profiling' AND connection_key=?`, connectionKey).Scan(&profilingConnection); err != nil {
			return 0, err
		}
		if (limits.MaxProfilingGlobal > 0 && profilingGlobal >= limits.MaxProfilingGlobal) ||
			(limits.MaxProfilingPerThread > 0 && profilingThread >= limits.MaxProfilingPerThread) ||
			(limits.MaxProfilingPerConnection > 0 && profilingConnection >= limits.MaxProfilingPerConnection) {
			return 0, jobs.ErrConcurrencyLimit
		}
	}
	record.Revision = 1
	if err := insertQueryJobTx(ctx, tx, record, record.Revision); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO query_capacity_reservations
		(job_id, thread_id, connection_key, reservation_state, reserved_at, released_at)
		VALUES (?, ?, ?, 'active', ?, NULL)`, record.ID, record.Owner.ThreadID, connectionKey, record.SubmittedAt.UTC()); err != nil {
		return 0, err
	}
	if err := syncWakeEventsMySQLTx(ctx, tx, record); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return record.Revision, nil
}

func isMySQLTransactionRetryable(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	return mysqlErr.Number == 1213 || mysqlErr.Number == 1205
}

func retryMySQLCapacityTransaction(ctx context.Context, operation func() (int64, error)) (int64, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		revision, err := operation()
		if err == nil || !isMySQLTransactionRetryable(err) {
			return revision, err
		}
		lastErr = err
		timer := time.NewTimer(time.Duration(attempt+1) * 20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return 0, ctx.Err()
		case <-timer.C:
		}
	}
	return 0, lastErr
}

func (s *MySQLStore) ReleaseQueryCapacity(ctx context.Context, jobID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE query_capacity_reservations SET reservation_state='released',
		released_at=COALESCE(released_at, UTC_TIMESTAMP(3)) WHERE job_id=?`, strings.TrimSpace(jobID))
	return err
}

func (s *MySQLStore) AcquireMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	jobID, holder = strings.TrimSpace(jobID), strings.TrimSpace(holder)
	if jobID == "" || holder == "" || ttl <= 0 {
		return false, errors.New("job id, holder, and positive ttl are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE query_jobs SET monitor_lease_holder=?,
		monitor_lease_expires_at=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)), updated_at=UTC_TIMESTAMP(3)
		WHERE id=? AND (monitor_lease_holder IS NULL OR monitor_lease_expires_at<=UTC_TIMESTAMP(3) OR monitor_lease_holder=?)`,
		holder, ttl.Microseconds(), jobID, holder)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *MySQLStore) RenewMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	jobID, holder = strings.TrimSpace(jobID), strings.TrimSpace(holder)
	if jobID == "" || holder == "" || ttl <= 0 {
		return false, errors.New("job id, holder, and positive ttl are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE query_jobs SET
		monitor_lease_expires_at=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)), updated_at=UTC_TIMESTAMP(3)
		WHERE id=? AND monitor_lease_holder=? AND monitor_lease_expires_at>UTC_TIMESTAMP(3)`,
		ttl.Microseconds(), jobID, holder)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *MySQLStore) ReleaseMonitorLease(ctx context.Context, jobID, holder string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE query_jobs SET monitor_lease_holder=NULL, monitor_lease_expires_at=NULL,
		updated_at=UTC_TIMESTAMP(3) WHERE id=? AND monitor_lease_holder=?`, strings.TrimSpace(jobID), strings.TrimSpace(holder))
	return err
}

// ApplyProfileMutation commits the narrow profile patch, optional lifecycle
// transition, wake appends, and terminal capacity release in one MySQL
// transaction. The final UPDATE repeats all three fences so correctness does
// not depend only on the preceding row lock or application wall time.
func (s *MySQLStore) ApplyProfileMutation(ctx context.Context, jobID, holder string, expectedRevision int64, mut jobs.ProfileMutation) (jobs.ProfileMutationResult, error) {
	jobID, holder = strings.TrimSpace(jobID), strings.TrimSpace(holder)
	if jobID == "" || holder == "" || expectedRevision < 0 {
		return jobs.ProfileMutationResult{}, errors.New("job id, holder, and expected revision are required")
	}
	terminal := false
	if mut.TransitionTo != nil {
		var valid bool
		terminal, valid = queryProfileTransition(*mut.TransitionTo)
		if !valid {
			return jobs.ProfileMutationResult{}, jobs.ErrRevisionConflict
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return jobs.ProfileMutationResult{}, err
	}
	defer tx.Rollback()

	var currentRevision int64
	var currentStatus string
	var leaseHolder sql.NullString
	var leaseExpires sql.NullTime
	var databaseNow time.Time
	err = tx.QueryRowContext(ctx, `SELECT revision, execution_state, monitor_lease_holder,
		monitor_lease_expires_at, UTC_TIMESTAMP(3) FROM query_jobs WHERE id=? FOR UPDATE`, jobID).
		Scan(&currentRevision, &currentStatus, &leaseHolder, &leaseExpires, &databaseNow)
	if errors.Is(err, sql.ErrNoRows) {
		return jobs.ProfileMutationResult{}, jobs.ErrRevisionConflict
	}
	if err != nil {
		return jobs.ProfileMutationResult{}, err
	}
	databaseNow = databaseNow.UTC()
	if currentRevision != expectedRevision || jobs.JobStatus(currentStatus) != jobs.JobStatusProfiling {
		return jobs.ProfileMutationResult{}, jobs.ErrRevisionConflict
	}
	if !leaseHolder.Valid || leaseHolder.String != holder || !leaseExpires.Valid || !leaseExpires.Time.After(databaseNow) {
		return jobs.ProfileMutationResult{}, jobs.ErrLeaseLost
	}

	setClauses := []string{"last_access_at=?", "updated_at=?", "revision=?"}
	args := []any{databaseNow, databaseNow, expectedRevision + 1}
	if mut.Dispatched != nil {
		setClauses = append(setClauses, "profile_dispatched=?")
		args = append(args, *mut.Dispatched)
	}
	if mut.Handle != nil {
		handle, marshalErr := marshalNullableJSON(*mut.Handle)
		if marshalErr != nil {
			return jobs.ProfileMutationResult{}, fmt.Errorf("marshal query profile handle: %w", marshalErr)
		}
		setClauses = append(setClauses, "profile_handle=?")
		args = append(args, handle)
	}
	if mut.Phase != nil {
		setClauses = append(setClauses, "profile_phase=?")
		args = append(args, nullIfBlank(*mut.Phase))
	}
	if mut.Profile != nil {
		profile, marshalErr := marshalNullableJSON(mut.Profile)
		if marshalErr != nil {
			return jobs.ProfileMutationResult{}, fmt.Errorf("marshal query profile: %w", marshalErr)
		}
		setClauses = append(setClauses, "query_profile=?")
		args = append(args, profile)
	}
	if mut.Outcome != nil {
		setClauses = append(setClauses, "profile_outcome=?")
		args = append(args, nullIfBlank(string(*mut.Outcome)))
	}
	if mut.Error != nil {
		setClauses = append(setClauses, "error_message=?")
		args = append(args, nullIfBlank(*mut.Error))
	}
	result := jobs.ProfileMutationResult{NewRevision: expectedRevision + 1, At: databaseNow}
	if mut.TransitionTo != nil {
		setClauses = append(setClauses, "execution_state=?")
		args = append(args, string(*mut.TransitionTo))
	}
	if terminal {
		completedAt := databaseNow
		result.CompletedAt = &completedAt
		result.CapacityReleased = true
		setClauses = append(setClauses, "completed_at=?")
		args = append(args, databaseNow)
		if mut.CancelReason != nil {
			setClauses = append(setClauses, "cancellation_reason=?")
			args = append(args, nullIfBlank(string(*mut.CancelReason)))
		}
		if mut.CancelOutcome != nil {
			setClauses = append(setClauses, "cancellation_outcome=?")
			args = append(args, nullIfBlank(string(*mut.CancelOutcome)))
		}
	}
	args = append(args, jobID, expectedRevision, holder)
	updateSQL := `UPDATE query_jobs SET ` + strings.Join(setClauses, ", ") + `
		WHERE id=? AND revision=? AND execution_state='profiling'
		AND monitor_lease_holder=? AND monitor_lease_expires_at>UTC_TIMESTAMP(3)`

	if len(mut.Wakes) > 0 {
		rows, queryErr := tx.QueryContext(ctx, `SELECT wake_kind FROM query_job_events
			WHERE job_id=? AND wake_kind IS NOT NULL FOR UPDATE`, jobID)
		if queryErr != nil {
			return jobs.ProfileMutationResult{}, queryErr
		}
		existingKinds := map[jobs.WakeKind]bool{}
		for rows.Next() {
			var kind string
			if scanErr := rows.Scan(&kind); scanErr != nil {
				rows.Close()
				return jobs.ProfileMutationResult{}, scanErr
			}
			existingKinds[jobs.WakeKind(kind)] = true
		}
		if closeErr := rows.Close(); closeErr != nil {
			return jobs.ProfileMutationResult{}, closeErr
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return jobs.ProfileMutationResult{}, rowsErr
		}
		for _, spec := range mut.Wakes {
			if spec.Kind == "" || existingKinds[spec.Kind] {
				continue
			}
			existingKinds[spec.Kind] = true
			wake := jobs.WakeEvent{At: databaseNow, Kind: spec.Kind, Detail: spec.Detail}
			if _, insertErr := insertQueryJobEventMySQLTx(ctx, tx, QueryJobEvent{
				JobID: jobID, Type: "wake", WakeKind: spec.Kind, Detail: spec.Detail,
				ObservationKey: wakeObservationKey(wake), CreatedAt: databaseNow,
			}); insertErr != nil {
				return jobs.ProfileMutationResult{}, insertErr
			}
			result.AppendedWakes = append(result.AppendedWakes, wake)
		}
	}
	if terminal {
		if _, err := tx.ExecContext(ctx, `UPDATE query_capacity_reservations
			SET reservation_state='released', released_at=COALESCE(released_at, UTC_TIMESTAMP(3))
			WHERE job_id=? AND reservation_state='active'`, jobID); err != nil {
			return jobs.ProfileMutationResult{}, err
		}
	}
	// Keep the triple-fenced lifecycle write last so an expiry while wake or
	// capacity side effects are prepared rolls the entire transaction back.
	updated, err := tx.ExecContext(ctx, updateSQL, args...)
	if err != nil {
		return jobs.ProfileMutationResult{}, err
	}
	if affected, affectedErr := updated.RowsAffected(); affectedErr != nil {
		return jobs.ProfileMutationResult{}, affectedErr
	} else if affected != 1 {
		// The row was locked and its revision/status were already verified;
		// the only mutable fence here is expiration against the DB clock.
		return jobs.ProfileMutationResult{}, jobs.ErrLeaseLost
	}
	if err := tx.Commit(); err != nil {
		return jobs.ProfileMutationResult{}, err
	}
	return result, nil
}

func (s *MySQLStore) ClaimWakeEvents(ctx context.Context, jobID, claimant string) ([]jobs.WakeEvent, int64, error) {
	jobID, claimant = strings.TrimSpace(jobID), strings.TrimSpace(claimant)
	if jobID == "" || claimant == "" {
		return nil, 0, errors.New("job id and claimant are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM query_jobs WHERE id=? FOR UPDATE`, jobID).Scan(&currentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, ErrNotFound
		}
		return nil, 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence, wake_kind, detail, created_at FROM query_job_events
		WHERE job_id=? AND wake_kind IS NOT NULL AND claimed_at IS NULL ORDER BY sequence FOR UPDATE`, jobID)
	if err != nil {
		return nil, 0, err
	}
	type claim struct {
		sequence int64
		event    jobs.WakeEvent
	}
	var claims []claim
	for rows.Next() {
		var item claim
		var kind string
		var detail sql.NullString
		if err := rows.Scan(&item.sequence, &kind, &detail, &item.event.At); err != nil {
			rows.Close()
			return nil, 0, err
		}
		item.event.Kind, item.event.Detail, item.event.Claimed = jobs.WakeKind(kind), detail.String, true
		claims = append(claims, item)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, err
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	now := nowUTC()
	for _, item := range claims {
		if _, err := tx.ExecContext(ctx, `UPDATE query_job_events SET claimed_by=?, claimed_at=? WHERE job_id=? AND sequence=? AND claimed_at IS NULL`, claimant, now, jobID, item.sequence); err != nil {
			return nil, 0, err
		}
	}
	newRevision := currentRevision
	if len(claims) > 0 {
		newRevision++
		if _, err := tx.ExecContext(ctx, `UPDATE query_jobs SET revision=?, last_access_at=UTC_TIMESTAMP(3),
			updated_at=UTC_TIMESTAMP(3) WHERE id=? AND revision=?`, newRevision, jobID, currentRevision); err != nil {
			return nil, 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, err
	}
	out := make([]jobs.WakeEvent, 0, len(claims))
	for _, item := range claims {
		out = append(out, item.event)
	}
	return out, newRevision, nil
}

func newStoreID(prefix string) string {
	return prefix + chattypes.NewUUID()
}

func validateRunStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case RunStatusQueued, RunStatusRunning, RunStatusWaitingExternal, RunStatusResumePending,
		RunStatusResuming, RunStatusWaitingUser, RunStatusCompleted, RunStatusFailed, RunStatusCancelled:
		return true
	default:
		return false
	}
}

func (s *MySQLStore) TransitionRunCAS(ctx context.Context, mutation RunStateMutation) (Run, bool, error) {
	mutation.RunID = strings.TrimSpace(mutation.RunID)
	mutation.Status = strings.TrimSpace(mutation.Status)
	mutation.ExpectedResumeLeaseHolder = strings.TrimSpace(mutation.ExpectedResumeLeaseHolder)
	if mutation.RunID == "" || !validateRunStatus(mutation.Status) || mutation.ExpectedRevision < 0 {
		return Run{}, false, errors.New("valid run id, status, and revision are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	var revision int64
	var currentStatus string
	if err := tx.QueryRowContext(ctx, `SELECT revision, status FROM runs WHERE id=? FOR UPDATE`, mutation.RunID).Scan(&revision, &currentStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, ErrNotFound
		}
		return Run{}, false, err
	}
	if revision != mutation.ExpectedRevision || !statusAllowed(currentStatus, mutation.ExpectedStatuses) {
		return Run{}, false, nil
	}
	set := []string{"status=?", "revision=revision+1", "updated_at=?"}
	args := []any{mutation.Status, nowUTC()}
	if mutation.ContinuationCount != nil {
		set = append(set, "continuation_count=?")
		args = append(args, *mutation.ContinuationCount)
	}
	if mutation.MaxAutoContinuations != nil {
		set = append(set, "max_auto_continuations=?")
		args = append(args, *mutation.MaxAutoContinuations)
	}
	if mutation.LastCheckpointSequence != nil {
		set = append(set, "last_checkpoint_sequence=?")
		args = append(args, *mutation.LastCheckpointSequence)
	}
	if mutation.ClearWakeAt {
		set = append(set, "wake_at=NULL")
	} else if mutation.WakeAt != nil {
		set = append(set, "wake_at=?")
		args = append(args, mutation.WakeAt.UTC())
	}
	if mutation.ClearResumeLease {
		set = append(set, "resume_lease_holder=NULL", "resume_lease_expires_at=NULL")
	}
	args = append(args, mutation.RunID, mutation.ExpectedRevision)
	where := "id=? AND revision=?"
	if mutation.ExpectedResumeLeaseHolder != "" {
		where += " AND resume_lease_holder=? AND resume_lease_expires_at>UTC_TIMESTAMP(3)"
		args = append(args, mutation.ExpectedResumeLeaseHolder)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET `+strings.Join(set, ", ")+` WHERE `+where, args...)
	if err != nil {
		return Run{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Run{}, false, err
	} else if affected != 1 {
		return Run{}, false, nil
	}
	if err := tx.Commit(); err != nil {
		return Run{}, false, err
	}
	run, err := s.GetRun(ctx, mutation.RunID)
	return run, err == nil, err
}

func statusAllowed(status string, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	for _, value := range expected {
		if strings.TrimSpace(value) == status {
			return true
		}
	}
	return false
}

func (s *MySQLStore) ClaimRunWake(ctx context.Context, runID string, expectedRevision int64, claimant string, now time.Time, leaseUntil time.Time) (Run, bool, error) {
	runID, claimant = strings.TrimSpace(runID), strings.TrimSpace(claimant)
	if runID == "" || claimant == "" || expectedRevision < 0 || leaseUntil.IsZero() {
		return Run{}, false, errors.New("run id, revision, claimant, and lease deadline are required")
	}
	if now.IsZero() {
		now = nowUTC()
	}
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	if !leaseUntil.After(now) {
		return Run{}, false, errors.New("resume lease must expire after now")
	}
	leaseMicroseconds := leaseUntil.Sub(now).Microseconds()
	if leaseMicroseconds <= 0 {
		leaseMicroseconds = 1
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, false, err
	}
	defer tx.Rollback()
	var revision int64
	var status string
	var count, maxContinuations int
	var wakeAt, leaseExpires sql.NullTime
	var leaseHolder sql.NullString
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT revision, status, continuation_count, max_auto_continuations,
		wake_at, resume_lease_holder, resume_lease_expires_at, UTC_TIMESTAMP(3)
		FROM runs WHERE id=? FOR UPDATE`, runID).Scan(&revision, &status, &count, &maxContinuations,
		&wakeAt, &leaseHolder, &leaseExpires, &databaseNow); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, false, ErrNotFound
		}
		return Run{}, false, err
	}
	eligibleStatus := status == RunStatusResumePending || status == RunStatusWaitingExternal
	databaseNow = databaseNow.UTC()
	initialExecution := status == RunStatusQueued && !leaseHolder.Valid && !leaseExpires.Valid
	expiredExecution := (status == RunStatusRunning || status == RunStatusResuming) &&
		leaseExpires.Valid && !leaseExpires.Time.After(databaseNow)
	legacyExecutionCandidate := status == RunStatusRunning && !leaseExpires.Valid
	leaseAvailable := !leaseExpires.Valid || !leaseExpires.Time.After(databaseNow) || leaseHolder.String == claimant
	wakeDue := wakeAt.Valid && !wakeAt.Time.After(databaseNow)
	if revision != expectedRevision || !leaseAvailable ||
		(!initialExecution && !expiredExecution && !legacyExecutionCandidate && (!eligibleStatus || !wakeDue)) {
		return Run{}, false, nil
	}
	if initialExecution {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, wake_at=NULL, resume_lease_holder=?,
			resume_lease_expires_at=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)),
			revision=revision+1, updated_at=UTC_TIMESTAMP(3) WHERE id=? AND revision=? AND status=?
			AND resume_lease_holder IS NULL AND resume_lease_expires_at IS NULL`,
			RunStatusRunning, claimant, leaseMicroseconds, runID, revision, RunStatusQueued)
		if err != nil {
			return Run{}, false, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return Run{}, false, err
		} else if affected != 1 {
			return Run{}, false, nil
		}
		return s.reconcileRunWakeClaimCommit(runID, expectedRevision, claimant, []string{RunStatusRunning}, tx.Commit())
	}
	if expiredExecution {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET resume_lease_holder=?,
			resume_lease_expires_at=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)),
			revision=revision+1, updated_at=UTC_TIMESTAMP(3) WHERE id=? AND revision=? AND status=?
			AND resume_lease_expires_at<=UTC_TIMESTAMP(3)`,
			claimant, leaseMicroseconds, runID, revision, status)
		if err != nil {
			return Run{}, false, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return Run{}, false, err
		} else if affected != 1 {
			return Run{}, false, nil
		}
		return s.reconcileRunWakeClaimCommit(runID, expectedRevision, claimant, []string{status}, tx.Commit())
	}
	if legacyExecutionCandidate {
		result, err := tx.ExecContext(ctx, `UPDATE runs SET resume_lease_holder=?,
			resume_lease_expires_at=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)),
			revision=revision+1, updated_at=UTC_TIMESTAMP(3) WHERE id=? AND revision=? AND status=?
			AND resume_lease_expires_at IS NULL
			AND updated_at<=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3))`,
			claimant, leaseMicroseconds, runID, revision, RunStatusRunning, -legacyRunningRecoveryAge.Microseconds())
		if err != nil {
			return Run{}, false, err
		}
		if affected, err := result.RowsAffected(); err != nil {
			return Run{}, false, err
		} else if affected != 1 {
			return Run{}, false, nil
		}
		return s.reconcileRunWakeClaimCommit(runID, expectedRevision, claimant, []string{RunStatusRunning}, tx.Commit())
	}
	if maxContinuations <= 0 {
		maxContinuations = 8
	}
	if count >= maxContinuations {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, wake_at=NULL, resume_lease_holder=NULL,
			resume_lease_expires_at=NULL, revision=revision+1, updated_at=UTC_TIMESTAMP(3)
			WHERE id=? AND revision=?`, RunStatusWaitingUser, runID, revision); err != nil {
			return Run{}, false, err
		}
		return s.reconcileRunWakeParkingCommit(runID, expectedRevision, tx.Commit())
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, continuation_count=continuation_count+1,
		wake_at=NULL, resume_lease_holder=?, resume_lease_expires_at=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)),
		revision=revision+1, updated_at=UTC_TIMESTAMP(3) WHERE id=? AND revision=? AND wake_at<=UTC_TIMESTAMP(3)`,
		RunStatusResuming, claimant, leaseMicroseconds, runID, revision)
	if err != nil {
		return Run{}, false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Run{}, false, err
	} else if affected != 1 {
		return Run{}, false, nil
	}
	return s.reconcileRunWakeClaimCommit(runID, expectedRevision, claimant, []string{RunStatusResuming}, tx.Commit())
}

// readRunAfterWakeCommit deliberately leaves the request context behind. A
// successful COMMIT is durable even when the HTTP request is cancelled before
// the confirmation read, and a COMMIT error can be ambiguous at the network
// boundary. A short retry window lets the lease token itself prove ownership.
func (s *MySQLStore) readRunAfterWakeCommit(runID string) (Run, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runWakeCommitReconcileTimeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		run, err := s.GetRun(ctx, runID)
		if err == nil {
			return run, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr != nil {
		return Run{}, lastErr
	}
	return Run{}, ctx.Err()
}

func (s *MySQLStore) reconcileRunWakeClaimCommit(
	runID string,
	expectedRevision int64,
	claimant string,
	expectedStatuses []string,
	commitErr error,
) (Run, bool, error) {
	run, readErr := s.readRunAfterWakeCommit(runID)
	if readErr == nil && run.Revision >= expectedRevision+1 && strings.TrimSpace(run.ResumeLeaseHolder) == claimant {
		for _, status := range expectedStatuses {
			if run.Status == status {
				return run, true, nil
			}
		}
	}
	if commitErr != nil {
		if readErr != nil {
			return Run{}, false, fmt.Errorf("commit run wake claim: %w (reconciliation failed: %v)", commitErr, readErr)
		}
		return Run{}, false, commitErr
	}
	if readErr != nil {
		return Run{}, false, readErr
	}
	return Run{}, false, ErrConflict
}

func (s *MySQLStore) reconcileRunWakeParkingCommit(runID string, expectedRevision int64, commitErr error) (Run, bool, error) {
	run, readErr := s.readRunAfterWakeCommit(runID)
	if readErr == nil && run.Revision >= expectedRevision+1 && run.Status == RunStatusWaitingUser && strings.TrimSpace(run.ResumeLeaseHolder) == "" {
		return run, false, nil
	}
	if commitErr != nil {
		if readErr != nil {
			return Run{}, false, fmt.Errorf("commit run wake parking transition: %w (reconciliation failed: %v)", commitErr, readErr)
		}
		return Run{}, false, commitErr
	}
	if readErr != nil {
		return Run{}, false, readErr
	}
	return Run{}, false, ErrConflict
}

func (s *MySQLStore) UpdateClaimedRunMetadata(ctx context.Context, runID, claimant string, fields map[string]any) error {
	runID, claimant = strings.TrimSpace(runID), strings.TrimSpace(claimant)
	if runID == "" || claimant == "" {
		return errors.New("run id and claimant are required")
	}
	allowed := map[string]bool{
		"started_at": true, "context_tokens_before": true, "context_tokens_after": true,
		"context_summary_buckets": true, "context_summary_rounds": true,
		"context_artifact_refs_loaded": true, "context_evidence_budget": true,
		"context_evidence_used": true, "context_evidence_exhausted": true,
		"context_need_retrieval": true, "context_need_retrieval_tooling": true,
		"context_hard_capped": true, "context_overflow_compacted": true, "context_plan": true,
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		if !allowed[key] {
			return fmt.Errorf("unsupported claimed run metadata field %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	set := make([]string, 0, len(keys)+2)
	args := make([]any, 0, len(keys)+3)
	for _, key := range keys {
		set = append(set, key+"=?")
		args = append(args, fields[key])
	}
	set = append(set, "revision=revision+1", "updated_at=UTC_TIMESTAMP(3)")
	args = append(args, runID, claimant)
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET `+strings.Join(set, ", ")+`
		WHERE id=? AND status IN ('running','resuming') AND resume_lease_holder=?
		AND resume_lease_expires_at>UTC_TIMESTAMP(3)`, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrConflict
	}
	return nil
}

func (s *MySQLStore) RenewRunWakeLease(ctx context.Context, runID, claimant string, ttl time.Duration) (bool, error) {
	runID, claimant = strings.TrimSpace(runID), strings.TrimSpace(claimant)
	if runID == "" || claimant == "" || ttl <= 0 {
		return false, errors.New("run id, claimant, and positive ttl are required")
	}
	microseconds := ttl.Microseconds()
	if microseconds <= 0 {
		microseconds = 1
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET
		resume_lease_expires_at=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)), updated_at=UTC_TIMESTAMP(3)
		WHERE id=? AND status IN ('running','resuming') AND resume_lease_holder=?
		AND resume_lease_expires_at>UTC_TIMESTAMP(3)`, microseconds, runID, claimant)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (s *MySQLStore) ReleaseRunWake(ctx context.Context, runID string, claimant string, nextStatus string, wakeAt *time.Time) error {
	runID, claimant, nextStatus = strings.TrimSpace(runID), strings.TrimSpace(claimant), strings.TrimSpace(nextStatus)
	if runID == "" || claimant == "" || !validateRunStatus(nextStatus) {
		return errors.New("run id, claimant, and valid next status are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET status=?, wake_at=?, resume_lease_holder=NULL,
		resume_lease_expires_at=NULL, revision=revision+1, updated_at=UTC_TIMESTAMP(3)
		WHERE id=? AND status IN ('running','resuming') AND resume_lease_holder=?
		AND resume_lease_expires_at>UTC_TIMESTAMP(3)`, nextStatus, wakeAt, runID, claimant)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return ErrConflict
	}
	return nil
}

func (s *MySQLStore) ListRecoverableRuns(ctx context.Context, now time.Time, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM runs WHERE
		(status IN ('waiting_external','resume_pending') AND wake_at IS NOT NULL AND wake_at<=UTC_TIMESTAMP(3)) OR
		(status IN ('running','resuming') AND resume_lease_expires_at IS NOT NULL AND resume_lease_expires_at<=UTC_TIMESTAMP(3)) OR
		(status='running' AND resume_lease_expires_at IS NULL
			AND updated_at<=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3))) OR
		(status='queued' AND updated_at<=TIMESTAMPADD(MICROSECOND, ?, UTC_TIMESTAMP(3)))
		ORDER BY COALESCE(wake_at, resume_lease_expires_at, updated_at), created_at LIMIT ?`,
		-legacyRunningRecoveryAge.Microseconds(), -queuedRunRecoveryAge.Microseconds(), limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(ids))
	for _, id := range ids {
		run, err := s.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, nil
}

func normalizeContinuationCheckpoint(checkpoint ContinuationCheckpoint) (ContinuationCheckpoint, error) {
	checkpoint.ID = strings.TrimSpace(checkpoint.ID)
	checkpoint.RunID = strings.TrimSpace(checkpoint.RunID)
	checkpoint.ThreadID = strings.TrimSpace(checkpoint.ThreadID)
	if checkpoint.ID == "" {
		checkpoint.ID = newStoreID("checkpoint_")
	}
	if checkpoint.RunID == "" || checkpoint.ThreadID == "" {
		return ContinuationCheckpoint{}, errors.New("checkpoint run_id and thread_id are required")
	}
	if len(checkpoint.HistoryJSON) == 0 || !json.Valid(checkpoint.HistoryJSON) {
		return ContinuationCheckpoint{}, errors.New("checkpoint history_json must be valid JSON")
	}
	if len(checkpoint.ProviderStateJSON) > 0 && !json.Valid(checkpoint.ProviderStateJSON) {
		return ContinuationCheckpoint{}, errors.New("checkpoint provider_state_json must be valid JSON")
	}
	if len(checkpoint.ExecutionSnapshotJSON) > 0 && !json.Valid(checkpoint.ExecutionSnapshotJSON) {
		return ContinuationCheckpoint{}, errors.New("checkpoint execution_snapshot_json must be valid JSON")
	}
	checkpoint.DependencyIDs = normalizeStringIDs(checkpoint.DependencyIDs)
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = nowUTC()
	} else {
		checkpoint.CreatedAt = checkpoint.CreatedAt.UTC()
	}
	return checkpoint, nil
}

func normalizeStringIDs(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func (s *MySQLStore) SuspendRun(ctx context.Context, mutation SuspendRunMutation) (ContinuationCheckpoint, Run, bool, error) {
	mutation, err := normalizeSuspendRunMutation(mutation)
	if err != nil {
		return ContinuationCheckpoint{}, Run{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuationCheckpoint{}, Run{}, false, err
	}
	defer tx.Rollback()

	var current Run
	var wakeAt, leaseExpires sql.NullTime
	var leaseHolder sql.NullString
	var databaseNow time.Time
	if err := tx.QueryRowContext(ctx, `SELECT thread_id, revision, status, continuation_count,
		max_auto_continuations, last_checkpoint_sequence, wake_at, resume_lease_holder,
		resume_lease_expires_at, UTC_TIMESTAMP(3) FROM runs WHERE id=? FOR UPDATE`, mutation.Checkpoint.RunID).
		Scan(&current.ThreadID, &current.Revision, &current.Status, &current.ContinuationCount,
			&current.MaxAutoContinuations, &current.LastCheckpointSequence, &wakeAt, &leaseHolder,
			&leaseExpires, &databaseNow); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ContinuationCheckpoint{}, Run{}, false, ErrNotFound
		}
		return ContinuationCheckpoint{}, Run{}, false, err
	}
	current.ID = mutation.Checkpoint.RunID
	if wakeAt.Valid {
		value := wakeAt.Time.UTC()
		current.WakeAt = &value
	}
	if leaseHolder.Valid {
		current.ResumeLeaseHolder = strings.TrimSpace(leaseHolder.String)
	}
	if leaseExpires.Valid {
		value := leaseExpires.Time.UTC()
		current.ResumeLeaseExpiresAt = &value
	}

	existing, existingErr := scanContinuationCheckpoint(tx.QueryRowContext(ctx, `SELECT id, run_id, thread_id, sequence,
		tool_batch_sequence, history_json, provider_state_json, execution_snapshot_json, dependency_ids, created_at
		FROM run_continuation_checkpoints WHERE id=?`, mutation.Checkpoint.ID))
	if existingErr == nil {
		proposed := cloneContinuationCheckpoint(mutation.Checkpoint)
		proposed.Sequence = existing.Sequence
		if !continuationCheckpointsSemanticallyEqual(existing, proposed) || !runMatchesSuspension(current, mutation, existing) {
			return ContinuationCheckpoint{}, current, false, ErrConflict
		}
		return existing, current, false, nil
	}
	if !errors.Is(existingErr, sql.ErrNoRows) {
		return ContinuationCheckpoint{}, current, false, existingErr
	}

	if current.ThreadID != mutation.Checkpoint.ThreadID {
		return ContinuationCheckpoint{}, current, false, errors.New("checkpoint thread does not match run")
	}
	if current.Revision != mutation.ExpectedRevision || !statusAllowed(current.Status, mutation.ExpectedStatuses) {
		return ContinuationCheckpoint{}, current, false, nil
	}
	requireLease := mutation.ExpectedResumeLeaseHolder != "" || current.Status == RunStatusResuming
	if requireLease && (mutation.ExpectedResumeLeaseHolder == "" || current.ResumeLeaseHolder != mutation.ExpectedResumeLeaseHolder ||
		current.ResumeLeaseExpiresAt == nil || !current.ResumeLeaseExpiresAt.After(databaseNow.UTC())) {
		return ContinuationCheckpoint{}, current, false, nil
	}

	checkpoint := cloneContinuationCheckpoint(mutation.Checkpoint)
	if checkpoint.Sequence <= 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0)+1
			FROM run_continuation_checkpoints WHERE run_id=? FOR UPDATE`, checkpoint.RunID).Scan(&checkpoint.Sequence); err != nil {
			return ContinuationCheckpoint{}, current, false, err
		}
	}
	dependencies, _ := json.Marshal(checkpoint.DependencyIDs)
	_, err = tx.ExecContext(ctx, `INSERT INTO run_continuation_checkpoints
		(id, run_id, thread_id, sequence, tool_batch_sequence, history_json, provider_state_json,
		execution_snapshot_json, dependency_ids, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, checkpoint.ID, checkpoint.RunID, checkpoint.ThreadID,
		checkpoint.Sequence, checkpoint.ToolBatchSequence, string(checkpoint.HistoryJSON), nullableRawJSON(checkpoint.ProviderStateJSON),
		nullableRawJSON(checkpoint.ExecutionSnapshotJSON), string(dependencies), checkpoint.CreatedAt)
	if err != nil {
		if isDuplicateKeyError(err) {
			return ContinuationCheckpoint{}, current, false, ErrConflict
		}
		return ContinuationCheckpoint{}, current, false, err
	}
	where := "id=? AND revision=?"
	args := []any{RunStatusWaitingExternal, mutation.ContinuationCount, mutation.MaxAutoContinuations,
		checkpoint.Sequence, mutation.WakeAt, checkpoint.RunID, mutation.ExpectedRevision}
	if mutation.ExpectedResumeLeaseHolder != "" {
		where += " AND resume_lease_holder=? AND resume_lease_expires_at>UTC_TIMESTAMP(3)"
		args = append(args, mutation.ExpectedResumeLeaseHolder)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runs SET status=?, continuation_count=?, max_auto_continuations=?,
		last_checkpoint_sequence=?, wake_at=?, resume_lease_holder=NULL, resume_lease_expires_at=NULL,
		revision=revision+1, updated_at=UTC_TIMESTAMP(3) WHERE `+where, args...)
	if err != nil {
		return ContinuationCheckpoint{}, current, false, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return ContinuationCheckpoint{}, current, false, err
	} else if affected != 1 {
		return ContinuationCheckpoint{}, current, false, nil
	}
	if err := tx.Commit(); err != nil {
		return ContinuationCheckpoint{}, current, false, err
	}
	run, err := s.GetRun(ctx, checkpoint.RunID)
	if err != nil {
		return ContinuationCheckpoint{}, Run{}, false, err
	}
	return checkpoint, run, true, nil
}

func (s *MySQLStore) AppendContinuationCheckpoint(ctx context.Context, checkpoint ContinuationCheckpoint) (ContinuationCheckpoint, error) {
	checkpoint, err := normalizeContinuationCheckpoint(checkpoint)
	if err != nil {
		return ContinuationCheckpoint{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContinuationCheckpoint{}, err
	}
	defer tx.Rollback()
	var threadID string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM runs WHERE id=? FOR UPDATE`, checkpoint.RunID).Scan(&threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ContinuationCheckpoint{}, ErrNotFound
		}
		return ContinuationCheckpoint{}, err
	}
	if threadID != checkpoint.ThreadID {
		return ContinuationCheckpoint{}, errors.New("checkpoint thread does not match run")
	}
	if checkpoint.Sequence <= 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0)+1 FROM run_continuation_checkpoints WHERE run_id=? FOR UPDATE`, checkpoint.RunID).Scan(&checkpoint.Sequence); err != nil {
			return ContinuationCheckpoint{}, err
		}
	}
	dependencies, _ := json.Marshal(checkpoint.DependencyIDs)
	_, err = tx.ExecContext(ctx, `INSERT INTO run_continuation_checkpoints
		(id, run_id, thread_id, sequence, tool_batch_sequence, history_json, provider_state_json, execution_snapshot_json, dependency_ids, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, checkpoint.ID, checkpoint.RunID, checkpoint.ThreadID,
		checkpoint.Sequence, checkpoint.ToolBatchSequence, string(checkpoint.HistoryJSON), nullableRawJSON(checkpoint.ProviderStateJSON),
		nullableRawJSON(checkpoint.ExecutionSnapshotJSON), string(dependencies), checkpoint.CreatedAt)
	if err != nil {
		if isDuplicateKeyError(err) {
			return ContinuationCheckpoint{}, ErrConflict
		}
		return ContinuationCheckpoint{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET last_checkpoint_sequence=GREATEST(last_checkpoint_sequence, ?), revision=revision+1, updated_at=? WHERE id=?`, checkpoint.Sequence, nowUTC(), checkpoint.RunID); err != nil {
		return ContinuationCheckpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContinuationCheckpoint{}, err
	}
	return cloneContinuationCheckpoint(checkpoint), nil
}

func nullableRawJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

func cloneContinuationCheckpoint(value ContinuationCheckpoint) ContinuationCheckpoint {
	value.HistoryJSON = append(json.RawMessage(nil), value.HistoryJSON...)
	value.ProviderStateJSON = append(json.RawMessage(nil), value.ProviderStateJSON...)
	value.ExecutionSnapshotJSON = append(json.RawMessage(nil), value.ExecutionSnapshotJSON...)
	value.DependencyIDs = append([]string(nil), value.DependencyIDs...)
	return value
}

func scanContinuationCheckpoint(scanner jobRowScanner) (ContinuationCheckpoint, error) {
	var checkpoint ContinuationCheckpoint
	var history, providerState, executionSnapshot, dependencies []byte
	if err := scanner.Scan(&checkpoint.ID, &checkpoint.RunID, &checkpoint.ThreadID, &checkpoint.Sequence,
		&checkpoint.ToolBatchSequence, &history, &providerState, &executionSnapshot, &dependencies, &checkpoint.CreatedAt); err != nil {
		return ContinuationCheckpoint{}, err
	}
	checkpoint.HistoryJSON = append(json.RawMessage(nil), history...)
	checkpoint.ProviderStateJSON = append(json.RawMessage(nil), providerState...)
	checkpoint.ExecutionSnapshotJSON = append(json.RawMessage(nil), executionSnapshot...)
	if len(dependencies) > 0 {
		if err := json.Unmarshal(dependencies, &checkpoint.DependencyIDs); err != nil {
			return ContinuationCheckpoint{}, err
		}
	}
	return checkpoint, nil
}

func (s *MySQLStore) LatestContinuationCheckpoint(ctx context.Context, runID string) (*ContinuationCheckpoint, error) {
	checkpoint, err := scanContinuationCheckpoint(s.db.QueryRowContext(ctx, `SELECT id, run_id, thread_id, sequence,
		tool_batch_sequence, history_json, provider_state_json, execution_snapshot_json, dependency_ids, created_at
		FROM run_continuation_checkpoints WHERE run_id=? ORDER BY sequence DESC LIMIT 1`, strings.TrimSpace(runID)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

func normalizeRunQueryDependency(dependency RunQueryDependency) (RunQueryDependency, error) {
	dependency.RunID = strings.TrimSpace(dependency.RunID)
	dependency.JobID = strings.TrimSpace(dependency.JobID)
	dependency.ValidationState = strings.TrimSpace(dependency.ValidationState)
	dependency.ObligationID = strings.TrimSpace(dependency.ObligationID)
	dependency.ReplacementJobID = strings.TrimSpace(dependency.ReplacementJobID)
	if dependency.RunID == "" || dependency.JobID == "" {
		return RunQueryDependency{}, errors.New("dependency run_id and job_id are required")
	}
	if err := validateQueryJobIdentityValue(dependency.JobID, "dependency job_id"); err != nil {
		return RunQueryDependency{}, err
	}
	if dependency.ReplacementJobID != "" {
		if err := validateQueryJobIdentityValue(dependency.ReplacementJobID, "dependency replacement_job_id"); err != nil {
			return RunQueryDependency{}, err
		}
		if dependency.ReplacementJobID == dependency.JobID {
			return RunQueryDependency{}, errors.New("dependency job cannot replace itself")
		}
	}
	if dependency.ObligationID != "" {
		if err := ValidateQueryObligationID(dependency.ObligationID); err != nil {
			return RunQueryDependency{}, err
		}
	}
	if dependency.ValidationState == "" {
		dependency.ValidationState = QueryValidationUnvalidated
	}
	now := nowUTC()
	if dependency.CreatedAt.IsZero() {
		dependency.CreatedAt = now
	}
	if dependency.UpdatedAt.IsZero() {
		dependency.UpdatedAt = now
	}
	return dependency, nil
}

func (s *MySQLStore) UpsertRunQueryDependency(ctx context.Context, dependency RunQueryDependency) (RunQueryDependency, error) {
	dependency, err := normalizeRunQueryDependency(dependency)
	if err != nil {
		return RunQueryDependency{}, err
	}
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		result, writeErr := s.upsertRunQueryDependencyOnce(ctx, dependency)
		if writeErr == nil {
			return result, nil
		}
		lastErr = writeErr
		if isDuplicateKeyError(writeErr) {
			// A concurrent binder may have inserted this exact (run,job)
			// winner. Accept only a semantically equivalent row; a duplicate on
			// the active-obligation unique key for another job remains conflict.
			existing, readErr := scanRunQueryDependency(s.db.QueryRowContext(ctx, `SELECT `+runQueryDependencyColumns+`
				FROM run_query_dependencies WHERE run_id=? AND job_id=?`, dependency.RunID, dependency.JobID))
			if errors.Is(readErr, sql.ErrNoRows) {
				return RunQueryDependency{}, ErrConflict
			}
			if readErr != nil {
				return RunQueryDependency{}, readErr
			}
			if existing.ObligationID != "" && dependency.ObligationID != "" && existing.ObligationID != dependency.ObligationID {
				return RunQueryDependency{}, ErrConflict
			}
			if existing.Detached {
				return existing, nil
			}
			if existing.RequiredForCompletion && existing.ValidationState == dependency.ValidationState &&
				(dependency.ObligationID == "" || existing.ObligationID == dependency.ObligationID) {
				return existing, nil
			}
			// The exact row exists but still needs a monotonic enrichment (for
			// example a mixed-version NULL obligation); retry under row lock.
			continue
		}
		if !isMySQLTransactionRetryable(writeErr) {
			return RunQueryDependency{}, writeErr
		}
	}
	return RunQueryDependency{}, lastErr
}

func (s *MySQLStore) upsertRunQueryDependencyOnce(ctx context.Context, dependency RunQueryDependency) (RunQueryDependency, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunQueryDependency{}, err
	}
	defer tx.Rollback()
	existing, readErr := scanRunQueryDependency(tx.QueryRowContext(ctx, `SELECT `+runQueryDependencyColumns+`
		FROM run_query_dependencies WHERE run_id=? AND job_id=? FOR UPDATE`, dependency.RunID, dependency.JobID))
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return RunQueryDependency{}, readErr
	}
	if readErr == nil {
		if existing.ObligationID != "" && dependency.ObligationID != "" && existing.ObligationID != dependency.ObligationID {
			return RunQueryDependency{}, ErrConflict
		}
		// Stale replays are monotonic: once detached, a row can never be
		// reactivated or have its replacement edge cleared by a generic upsert.
		if existing.Detached {
			if err := tx.Commit(); err != nil {
				return RunQueryDependency{}, err
			}
			return existing, nil
		}
		dependency.CreatedAt = existing.CreatedAt
		dependency.RequiredForCompletion = existing.RequiredForCompletion || dependency.RequiredForCompletion
		dependency.Detached = false
		dependency.ReplacementJobID = existing.ReplacementJobID
		if existing.ObligationID != "" {
			dependency.ObligationID = existing.ObligationID
		}
		_, err = tx.ExecContext(ctx, `UPDATE run_query_dependencies SET
			required_for_completion=?, validation_state=?, query_obligation_id=COALESCE(?, query_obligation_id), updated_at=?
			WHERE run_id=? AND job_id=? AND detached=0`,
			dependency.RequiredForCompletion, dependency.ValidationState, nullIfBlank(dependency.ObligationID),
			dependency.UpdatedAt.UTC(), dependency.RunID, dependency.JobID)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO run_query_dependencies
			(run_id, job_id, required_for_completion, detached, validation_state, query_obligation_id, replacement_job_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			dependency.RunID, dependency.JobID, dependency.RequiredForCompletion, dependency.Detached,
			dependency.ValidationState, nullIfBlank(dependency.ObligationID), nullIfBlank(dependency.ReplacementJobID),
			dependency.CreatedAt.UTC(), dependency.UpdatedAt.UTC())
	}
	if err != nil {
		return RunQueryDependency{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunQueryDependency{}, err
	}
	return dependency, nil
}

func (s *MySQLStore) BindQueryObligation(ctx context.Context, jobID string) error {
	return bindQueryObligation(ctx, s, jobID)
}

func scanRunQueryDependency(scanner jobRowScanner) (RunQueryDependency, error) {
	var dependency RunQueryDependency
	var obligation, replacement sql.NullString
	if err := scanner.Scan(&dependency.RunID, &dependency.JobID, &dependency.RequiredForCompletion, &dependency.Detached,
		&dependency.ValidationState, &obligation, &replacement,
		&dependency.CreatedAt, &dependency.UpdatedAt); err != nil {
		return RunQueryDependency{}, err
	}
	dependency.ObligationID = strings.TrimSpace(obligation.String)
	if obligation.Valid && obligation.String != "" &&
		(obligation.String != dependency.ObligationID || ValidateQueryObligationID(dependency.ObligationID) != nil) {
		return RunQueryDependency{}, fmt.Errorf("run query dependency has an invalid query_obligation_id: %w", ErrConflict)
	}
	dependency.ReplacementJobID = strings.TrimSpace(replacement.String)
	if err := sanitizeLegacyRunQueryDependencyIdentity(&dependency); err != nil {
		return RunQueryDependency{}, err
	}
	return dependency, nil
}

const runQueryDependencyColumns = `run_id, job_id, required_for_completion, detached, validation_state,
		query_obligation_id, replacement_job_id, created_at, updated_at`

func (s *MySQLStore) ReplaceRunQueryDependency(ctx context.Context, runID, replacedJobID string, replacement RunQueryDependency) (RunQueryDependency, error) {
	runID, replacedJobID = strings.TrimSpace(runID), strings.TrimSpace(replacedJobID)
	replacement, err := normalizeRunQueryDependency(replacement)
	if err != nil {
		return RunQueryDependency{}, err
	}
	if runID == "" || replacedJobID == "" || replacement.RunID != runID || replacement.JobID == replacedJobID {
		return RunQueryDependency{}, errors.New("replacement requires one run and distinct old and new query jobs")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunQueryDependency{}, err
	}
	defer tx.Rollback()
	var threadID string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM runs WHERE id=? FOR UPDATE`, runID).Scan(&threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunQueryDependency{}, ErrNotFound
		}
		return RunQueryDependency{}, err
	}
	old, err := scanRunQueryDependency(tx.QueryRowContext(ctx, `SELECT `+runQueryDependencyColumns+` FROM run_query_dependencies
		WHERE run_id=? AND job_id=? FOR UPDATE`, runID, replacedJobID))
	if errors.Is(err, sql.ErrNoRows) {
		return RunQueryDependency{}, ErrNotFound
	}
	if err != nil {
		return RunQueryDependency{}, err
	}
	existing, existingErr := scanRunQueryDependency(tx.QueryRowContext(ctx, `SELECT `+runQueryDependencyColumns+` FROM run_query_dependencies
		WHERE run_id=? AND job_id=? FOR UPDATE`, runID, replacement.JobID))
	if existingErr != nil && !errors.Is(existingErr, sql.ErrNoRows) {
		return RunQueryDependency{}, existingErr
	}
	oldJob, err := lockMySQLQueryJobTx(ctx, tx, replacedJobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunQueryDependency{}, ErrNotFound
		}
		return RunQueryDependency{}, err
	}
	replacementJob, err := lockMySQLQueryJobTx(ctx, tx, replacement.JobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunQueryDependency{}, ErrNotFound
		}
		return RunQueryDependency{}, err
	}
	// Either job may be owned by an earlier run in the same thread. The locked,
	// active dependency row in this run is the replacement capability; exact
	// durable lineage and same-thread ownership fence the successor without
	// rewriting its immutable owner run.
	if oldJob.Owner.ThreadID != threadID || replacementJob.Owner.ThreadID != threadID {
		return RunQueryDependency{}, ErrConflict
	}
	if replacementJob.ReplacesJobID != replacedJobID {
		return RunQueryDependency{}, fmt.Errorf("replacement job %s does not durably replace %s: %w", replacement.JobID, replacedJobID, ErrConflict)
	}

	// Reconcile every persisted identity source before checking for an
	// idempotent replay. Older binaries could leave either side of an already
	// completed swap NULL; returning early in that state would permanently
	// bypass the one-active-attempt constraint for this obligation.
	obligationID := ""
	acceptObligationID := func(candidate string) error {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return nil
		}
		if err := ValidateQueryObligationID(candidate); err != nil {
			return fmt.Errorf("stored query obligation is invalid: %w", ErrConflict)
		}
		if obligationID != "" && obligationID != candidate {
			return ErrConflict
		}
		obligationID = candidate
		return nil
	}
	if err := acceptObligationID(replacement.ObligationID); err != nil {
		return RunQueryDependency{}, err
	}
	if err := acceptObligationID(old.ObligationID); err != nil {
		return RunQueryDependency{}, err
	}
	if existingErr == nil {
		if err := acceptObligationID(existing.ObligationID); err != nil {
			return RunQueryDependency{}, err
		}
	}
	if obligationID == "" {
		root := replacedJobID
		ancestor := oldJob.ReplacesJobID
		seen := map[string]bool{root: true}
		for hops := 0; ancestor != ""; hops++ {
			if hops >= 64 || seen[ancestor] {
				return RunQueryDependency{}, fmt.Errorf("query replacement lineage is cyclic or exceeds 64 attempts: %w", ErrConflict)
			}
			seen[ancestor] = true
			root = ancestor
			ancestorJob, ancestorErr := lockMySQLQueryJobTx(ctx, tx, ancestor)
			if errors.Is(ancestorErr, sql.ErrNoRows) {
				return RunQueryDependency{}, fmt.Errorf("query replacement ancestor %s is missing: %w", ancestor, ErrConflict)
			}
			if ancestorErr != nil {
				return RunQueryDependency{}, ancestorErr
			}
			ancestor = ancestorJob.ReplacesJobID
		}
		obligationID, err = QueryObligationIDForRoot(runID, root)
		if err != nil {
			return RunQueryDependency{}, err
		}
	}
	now := nowUTC()
	if old.Detached {
		if old.ReplacementJobID != replacement.JobID || existingErr != nil || !existing.RequiredForCompletion || existing.Detached {
			return RunQueryDependency{}, ErrConflict
		}
		if old.ObligationID == obligationID && existing.ObligationID == obligationID {
			return existing, nil
		}
		// Hydrate both halves atomically. Updating the active successor can hit
		// the generated unique obligation key if another attempt already owns
		// it; report that as a conflict and leave both legacy rows untouched.
		if old.ObligationID != obligationID {
			result, updateErr := tx.ExecContext(ctx, `UPDATE run_query_dependencies SET query_obligation_id=?, updated_at=?
				WHERE run_id=? AND job_id=? AND detached=1 AND replacement_job_id=?`,
				nullIfBlank(obligationID), now, runID, replacedJobID, replacement.JobID)
			if updateErr != nil {
				if isDuplicateKeyError(updateErr) {
					return RunQueryDependency{}, ErrConflict
				}
				return RunQueryDependency{}, updateErr
			}
			if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
				return RunQueryDependency{}, rowsErr
			} else if affected != 1 {
				return RunQueryDependency{}, ErrConflict
			}
		}
		if existing.ObligationID != obligationID {
			result, updateErr := tx.ExecContext(ctx, `UPDATE run_query_dependencies SET query_obligation_id=?, updated_at=?
				WHERE run_id=? AND job_id=? AND required_for_completion=1 AND detached=0`,
				nullIfBlank(obligationID), now, runID, replacement.JobID)
			if updateErr != nil {
				if isDuplicateKeyError(updateErr) {
					return RunQueryDependency{}, ErrConflict
				}
				return RunQueryDependency{}, updateErr
			}
			if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
				return RunQueryDependency{}, rowsErr
			} else if affected != 1 {
				return RunQueryDependency{}, ErrConflict
			}
		}
		if err := tx.Commit(); err != nil {
			return RunQueryDependency{}, err
		}
		existing.ObligationID = obligationID
		existing.UpdatedAt = now
		return existing, nil
	}
	if !old.RequiredForCompletion || old.ReplacementJobID != "" {
		return RunQueryDependency{}, ErrConflict
	}

	if !dependencyReplacementEligible(old, oldJob) {
		return RunQueryDependency{}, ErrConflict
	}
	if existingErr == nil && (existing.Detached || existing.ReplacementJobID != "") {
		return RunQueryDependency{}, ErrConflict
	}

	replacement.ObligationID = obligationID

	replacement.RequiredForCompletion = true
	replacement.Detached = false
	replacement.ReplacementJobID = ""
	replacement.UpdatedAt = now
	if existingErr == nil {
		replacement.CreatedAt = existing.CreatedAt
	}
	// Detach first so the generated unique active-obligation key is released
	// before the successor becomes active. Both writes are inside this
	// transaction, so readers never observe a gap and any insert failure rolls
	// the predecessor back to active.
	result, err := tx.ExecContext(ctx, `UPDATE run_query_dependencies SET detached=1, replacement_job_id=?, query_obligation_id=?,
		updated_at=?
		WHERE run_id=? AND job_id=? AND required_for_completion=1 AND detached=0`,
		replacement.JobID, nullIfBlank(obligationID), now, runID, replacedJobID)
	if err != nil {
		return RunQueryDependency{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return RunQueryDependency{}, err
	} else if affected != 1 {
		return RunQueryDependency{}, ErrConflict
	}
	if existingErr == nil {
		result, updateErr := tx.ExecContext(ctx, `UPDATE run_query_dependencies SET
			required_for_completion=1, detached=0, validation_state=?, query_obligation_id=?,
			replacement_job_id=NULL, updated_at=?
			WHERE run_id=? AND job_id=? AND detached=0`,
			replacement.ValidationState, nullIfBlank(obligationID), replacement.UpdatedAt.UTC(),
			replacement.RunID, replacement.JobID)
		if updateErr != nil {
			if isDuplicateKeyError(updateErr) {
				return RunQueryDependency{}, ErrConflict
			}
			return RunQueryDependency{}, updateErr
		}
		if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
			return RunQueryDependency{}, rowsErr
		} else if affected != 1 {
			return RunQueryDependency{}, ErrConflict
		}
	} else {
		if _, insertErr := tx.ExecContext(ctx, `INSERT INTO run_query_dependencies
			(run_id, job_id, required_for_completion, detached, validation_state, query_obligation_id, replacement_job_id, created_at, updated_at)
			VALUES (?, ?, 1, 0, ?, ?, NULL, ?, ?)`,
			replacement.RunID, replacement.JobID, replacement.ValidationState, nullIfBlank(obligationID),
			replacement.CreatedAt.UTC(), replacement.UpdatedAt.UTC()); insertErr != nil {
			if isDuplicateKeyError(insertErr) {
				return RunQueryDependency{}, ErrConflict
			}
			return RunQueryDependency{}, insertErr
		}
	}
	if err := tx.Commit(); err != nil {
		return RunQueryDependency{}, err
	}
	return replacement, nil
}

func (s *MySQLStore) ListRunQueryDependencies(ctx context.Context, runID string) ([]RunQueryDependency, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+runQueryDependencyColumns+`
		FROM run_query_dependencies WHERE run_id=? ORDER BY created_at, job_id`, strings.TrimSpace(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunQueryDependency
	for rows.Next() {
		dependency, err := scanRunQueryDependency(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, dependency)
	}
	return out, rows.Err()
}

func (s *MySQLStore) DetachRunQueryDependencies(ctx context.Context, runID string, now time.Time) error {
	if now.IsZero() {
		now = nowUTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE run_query_dependencies SET detached=1, updated_at=? WHERE run_id=?`, now.UTC(), strings.TrimSpace(runID))
	return err
}

func validQueryValidationState(state string) bool {
	switch strings.TrimSpace(state) {
	case QueryValidationUnvalidated, QueryValidationValidating, QueryValidationValidated, QueryValidationRejected:
		return true
	default:
		return false
	}
}

func (s *MySQLStore) UpdateQueryJobValidation(ctx context.Context, jobID string, state jobs.ValidationState, detail string, expectedRevision int64) (int64, error) {
	jobID, detail = strings.TrimSpace(jobID), strings.TrimSpace(detail)
	if jobID == "" || expectedRevision < 0 || !validQueryValidationState(string(state)) {
		return 0, errors.New("query job id, expected revision, and valid validation state are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM query_jobs WHERE id=? FOR UPDATE`, jobID).Scan(&currentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if currentRevision != expectedRevision {
		return 0, jobs.ErrRevisionConflict
	}
	newRevision := currentRevision + 1
	if _, err := tx.ExecContext(ctx, `UPDATE query_jobs SET validation_state=?, validation_detail=?, revision=?,
		updated_at=UTC_TIMESTAMP(3) WHERE id=? AND revision=?`,
		string(state), nullIfBlank(detail), newRevision, jobID, currentRevision); err != nil {
		return 0, err
	}
	payload, _ := json.Marshal(map[string]any{"state": state, "detail": detail, "lifecycle_revision": newRevision})
	if _, err := insertQueryJobEventMySQLTx(ctx, tx, QueryJobEvent{
		JobID: jobID, Type: "validation", ObservationKey: validationObservationKey(jobID, string(state), detail, newRevision), Payload: payload, CreatedAt: nowUTC(),
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newRevision, nil
}

func validationObservationKey(jobID, state, detail string, lifecycleRevision int64) string {
	material := fmt.Sprintf("%s\x00%s\x00%s\x00%d", strings.TrimSpace(jobID), strings.TrimSpace(state), strings.TrimSpace(detail), lifecycleRevision)
	sum := sha256.Sum256([]byte(material))
	return "validation:" + hex.EncodeToString(sum[:])
}

func normalizeQueryJobEvent(event QueryJobEvent) (QueryJobEvent, error) {
	event.JobID = strings.TrimSpace(event.JobID)
	event.Type = strings.TrimSpace(event.Type)
	event.Detail = strings.TrimSpace(event.Detail)
	event.ObservationKey = strings.TrimSpace(event.ObservationKey)
	if event.JobID == "" || event.Type == "" {
		return QueryJobEvent{}, errors.New("query event job_id and type are required")
	}
	if len(event.Payload) > 0 && !json.Valid(event.Payload) {
		return QueryJobEvent{}, errors.New("query event payload must be valid JSON")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = nowUTC()
	} else {
		event.CreatedAt = event.CreatedAt.UTC()
	}
	if event.ObservationKey == "" {
		material := event.Type + "\x00" + string(event.WakeKind) + "\x00" + event.Detail + "\x00" + string(event.Payload) + "\x00" + event.CreatedAt.Format(time.RFC3339Nano)
		sum := sha256.Sum256([]byte(material))
		event.ObservationKey = "event:" + hex.EncodeToString(sum[:])
	}
	return event, nil
}

func insertQueryJobEventMySQLTx(ctx context.Context, tx *sql.Tx, event QueryJobEvent) (QueryJobEvent, error) {
	event, err := normalizeQueryJobEvent(event)
	if err != nil {
		return QueryJobEvent{}, err
	}
	if event.Sequence <= 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0)+1 FROM query_job_events WHERE job_id=? FOR UPDATE`, event.JobID).Scan(&event.Sequence); err != nil {
			return QueryJobEvent{}, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO query_job_events
		(job_id, sequence, event_type, wake_kind, detail, payload, observation_key, created_at, claimed_by, claimed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.JobID, event.Sequence, event.Type,
		nullIfBlank(string(event.WakeKind)), nullIfBlank(event.Detail), nullableRawJSON(event.Payload), event.ObservationKey,
		event.CreatedAt, nullIfBlank(event.ClaimedBy), event.ClaimedAt)
	if err != nil {
		if isDuplicateKeyError(err) {
			return QueryJobEvent{}, ErrConflict
		}
		return QueryJobEvent{}, err
	}
	return event, nil
}

func (s *MySQLStore) AppendQueryJobEvent(ctx context.Context, event QueryJobEvent) (QueryJobEvent, error) {
	event, err := normalizeQueryJobEvent(event)
	if err != nil {
		return QueryJobEvent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return QueryJobEvent{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM query_jobs WHERE id=? FOR UPDATE`, event.JobID).Scan(&exists); err != nil {
		return QueryJobEvent{}, err
	}
	if exists == 0 {
		return QueryJobEvent{}, ErrNotFound
	}
	var existingSequence int64
	err = tx.QueryRowContext(ctx, `SELECT sequence FROM query_job_events WHERE job_id=? AND observation_key=?`, event.JobID, event.ObservationKey).Scan(&existingSequence)
	if err == nil {
		event.Sequence = existingSequence
		if err := tx.Commit(); err != nil {
			return QueryJobEvent{}, err
		}
		return event, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return QueryJobEvent{}, err
	}
	event, err = insertQueryJobEventMySQLTx(ctx, tx, event)
	if err != nil {
		return QueryJobEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return QueryJobEvent{}, err
	}
	return event, nil
}

func scanQueryJobEvent(scanner jobRowScanner) (QueryJobEvent, error) {
	var event QueryJobEvent
	var wakeKind, detail, claimedBy sql.NullString
	var payload []byte
	var claimedAt sql.NullTime
	if err := scanner.Scan(&event.JobID, &event.Sequence, &event.Type, &wakeKind, &detail, &payload,
		&event.ObservationKey, &event.CreatedAt, &claimedBy, &claimedAt); err != nil {
		return QueryJobEvent{}, err
	}
	event.WakeKind = jobs.WakeKind(wakeKind.String)
	event.Detail = detail.String
	event.Payload = append(json.RawMessage(nil), payload...)
	event.ClaimedBy = claimedBy.String
	if claimedAt.Valid {
		value := claimedAt.Time
		event.ClaimedAt = &value
	}
	return event, nil
}

func (s *MySQLStore) ListQueryJobEvents(ctx context.Context, jobID string, afterSequence int64, limit int) ([]QueryJobEvent, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT job_id, sequence, event_type, wake_kind, detail, payload,
		observation_key, created_at, claimed_by, claimed_at FROM query_job_events
		WHERE job_id=? AND sequence>? ORDER BY sequence LIMIT ?`, strings.TrimSpace(jobID), afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryJobEvent
	for rows.Next() {
		event, err := scanQueryJobEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func normalizeQueryResultChunk(chunk QueryResultChunk) (QueryResultChunk, error) {
	chunk.JobID = strings.TrimSpace(chunk.JobID)
	if chunk.JobID == "" || chunk.Sequence <= 0 {
		return QueryResultChunk{}, errors.New("query result chunk job_id and positive sequence are required")
	}
	if chunk.CursorAfter.IsZero() && chunk.Page.CursorAfter != nil {
		chunk.CursorAfter = *chunk.Page.CursorAfter
	}
	if chunk.NativePageToken == "" {
		chunk.NativePageToken = chunk.Page.NextPageToken
	}
	if chunk.RowCount <= 0 {
		chunk.RowCount = len(chunk.Page.Rows)
	}
	if chunk.ByteCount <= 0 {
		chunk.ByteCount = chunk.Page.Bytes
	}
	chunk.Truncated = chunk.Truncated || chunk.Page.Truncated
	if chunk.CreatedAt.IsZero() {
		chunk.CreatedAt = nowUTC()
	} else {
		chunk.CreatedAt = chunk.CreatedAt.UTC()
	}
	return chunk, nil
}

func insertQueryResultChunkMySQLTx(ctx context.Context, tx *sql.Tx, chunk QueryResultChunk) error {
	chunk, err := normalizeQueryResultChunk(chunk)
	if err != nil {
		return err
	}
	pageJSON, err := json.Marshal(chunk.Page)
	if err != nil {
		return err
	}
	var cursorJSON any
	if !chunk.CursorAfter.IsZero() {
		raw, err := json.Marshal(chunk.CursorAfter)
		if err != nil {
			return err
		}
		cursorJSON = string(raw)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO query_result_chunks
		(job_id, sequence, page_json, cursor_after_json, native_page_token, row_count, byte_count, truncated, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, chunk.JobID, chunk.Sequence, string(pageJSON), cursorJSON,
		nullIfBlank(chunk.NativePageToken), chunk.RowCount, chunk.ByteCount, chunk.Truncated, chunk.CreatedAt)
	if err == nil {
		return nil
	}
	if !isDuplicateKeyError(err) {
		return err
	}
	var existingPage []byte
	var existingCursor []byte
	if scanErr := tx.QueryRowContext(ctx, `SELECT page_json, cursor_after_json FROM query_result_chunks WHERE job_id=? AND sequence=?`, chunk.JobID, chunk.Sequence).Scan(&existingPage, &existingCursor); scanErr != nil {
		return scanErr
	}
	if !jsonSemanticallyEqual(existingPage, pageJSON) || !nullableJSONSemanticallyEqual(existingCursor, cursorJSON) {
		return ErrConflict
	}
	return nil
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return string(left) == string(right)
	}
	leftCanonical, _ := json.Marshal(leftValue)
	rightCanonical, _ := json.Marshal(rightValue)
	return string(leftCanonical) == string(rightCanonical)
}

func nullableJSONSemanticallyEqual(existing []byte, proposed any) bool {
	if proposed == nil {
		return len(existing) == 0 || string(existing) == "null"
	}
	return jsonSemanticallyEqual(existing, []byte(fmt.Sprint(proposed)))
}

func (s *MySQLStore) AppendQueryResultChunk(ctx context.Context, chunk QueryResultChunk) error {
	chunk, err := normalizeQueryResultChunk(chunk)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertQueryResultChunkMySQLTx(ctx, tx, chunk); err != nil {
		return err
	}
	return tx.Commit()
}

func scanQueryResultChunk(scanner jobRowScanner) (QueryResultChunk, error) {
	var chunk QueryResultChunk
	var pageJSON, cursorJSON []byte
	var pageToken sql.NullString
	if err := scanner.Scan(&chunk.JobID, &chunk.Sequence, &pageJSON, &cursorJSON, &pageToken,
		&chunk.RowCount, &chunk.ByteCount, &chunk.Truncated, &chunk.CreatedAt); err != nil {
		return QueryResultChunk{}, err
	}
	if err := json.Unmarshal(pageJSON, &chunk.Page); err != nil {
		return QueryResultChunk{}, err
	}
	if len(cursorJSON) > 0 {
		if err := json.Unmarshal(cursorJSON, &chunk.CursorAfter); err != nil {
			return QueryResultChunk{}, err
		}
	}
	chunk.NativePageToken = pageToken.String
	return chunk, nil
}

func (s *MySQLStore) ListQueryResultChunks(ctx context.Context, jobID string, afterSequence int64, limit int) ([]QueryResultChunk, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT job_id, sequence, page_json, cursor_after_json, native_page_token,
		row_count, byte_count, truncated, created_at FROM query_result_chunks
		WHERE job_id=? AND sequence>? ORDER BY sequence LIMIT ?`, strings.TrimSpace(jobID), afterSequence, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueryResultChunk
	for rows.Next() {
		chunk, err := scanQueryResultChunk(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	return out, rows.Err()
}

func (s *MySQLStore) CommitDrainObservation(ctx context.Context, commit jobs.DrainCommit) (int64, error) {
	return s.commitDrainObservation(ctx, commit, "")
}

func (s *MySQLStore) CommitDrainObservationFenced(ctx context.Context, commit jobs.DrainCommit, holder string) (int64, error) {
	holder = strings.TrimSpace(holder)
	if holder == "" {
		return 0, errors.New("monitor lease holder is required")
	}
	return s.commitDrainObservation(ctx, commit, holder)
}

func (s *MySQLStore) commitDrainObservation(ctx context.Context, commit jobs.DrainCommit, holder string) (int64, error) {
	if strings.TrimSpace(commit.JobID) == "" || commit.Seq <= 0 || commit.Record.ID != commit.JobID || commit.Record.DrainSeq != commit.Seq {
		return 0, errors.New("invalid drain commit identity or sequence")
	}
	if commit.Owner.ThreadID != commit.Record.Owner.ThreadID || commit.Owner.Handle != commit.Record.Owner.Handle {
		return 0, jobs.ErrOwnershipMismatch
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	current, err := lockMySQLQueryJobTx(ctx, tx, commit.JobID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	var leaseHolder sql.NullString
	var leaseExpires sql.NullTime
	var databaseNow time.Time
	if current.Owner.ThreadID != commit.Owner.ThreadID || current.Owner.Handle != commit.Owner.Handle {
		return 0, jobs.ErrOwnershipMismatch
	}
	if err := freezeMySQLQueryJobIdentityTx(ctx, tx, current, &commit.Record); err != nil {
		return 0, err
	}
	if holder != "" {
		if err := tx.QueryRowContext(ctx, `SELECT monitor_lease_holder, monitor_lease_expires_at, UTC_TIMESTAMP(3)
			FROM query_jobs WHERE id=?`, commit.JobID).Scan(&leaseHolder, &leaseExpires, &databaseNow); err != nil {
			return 0, err
		}
	}
	if holder != "" && (!leaseHolder.Valid || leaseHolder.String != holder || !leaseExpires.Valid || !leaseExpires.Time.After(databaseNow)) {
		return 0, jobs.ErrLeaseLost
	}
	if current.DrainSeq >= commit.Seq {
		storedChunk, err := scanQueryResultChunk(tx.QueryRowContext(ctx, `SELECT job_id, sequence, page_json,
			cursor_after_json, native_page_token, row_count, byte_count, truncated, created_at
			FROM query_result_chunks WHERE job_id=? AND sequence=?`, commit.JobID, commit.Seq))
		if errors.Is(err, sql.ErrNoRows) {
			return 0, errors.New("drain sequence advanced without durable result chunk")
		}
		if err != nil {
			return 0, err
		}
		proposedChunk := QueryResultChunk{JobID: commit.JobID, Sequence: commit.Seq, Page: commit.Page,
			CursorAfter: commit.Handle, RowCount: len(commit.Page.Rows), ByteCount: commit.Page.Bytes, Truncated: commit.Page.Truncated}
		if !queryResultChunksSemanticallyEqual(storedChunk, proposedChunk) {
			return 0, ErrConflict
		}
		if current.DrainSeq == commit.Seq {
			currentRecord := jobs.JobRecord{
				DrainRowsCommitted: current.DrainRowsCommitted, DrainBytesCommitted: current.DrainBytesCommitted, DrainTruncated: current.DrainTruncated,
			}
			if !nativeHandlesSemanticallyEqual(current.Handle, commit.Handle) || !drainReplayAccountingMatches(commit, currentRecord) {
				return 0, ErrConflict
			}
		}
		return current.Revision, tx.Commit()
	}
	if isTerminalQueryExecutionState(current.Status) {
		return 0, jobs.ErrRevisionConflict
	}
	if current.Revision != commit.Record.Revision || current.DrainSeq+1 != commit.Seq {
		return 0, jobs.ErrRevisionConflict
	}
	if err := validateDrainCommitAccounting(commit, jobs.JobRecord{
		DrainRowsCommitted: current.DrainRowsCommitted, DrainBytesCommitted: current.DrainBytesCommitted, DrainTruncated: current.DrainTruncated,
	}); err != nil {
		return 0, err
	}
	chunk := QueryResultChunk{JobID: commit.JobID, Sequence: commit.Seq, Page: commit.Page,
		CursorAfter: commit.Handle, RowCount: len(commit.Page.Rows), ByteCount: commit.Page.Bytes, Truncated: commit.Page.Truncated, CreatedAt: nowUTC()}
	if err := insertQueryResultChunkMySQLTx(ctx, tx, chunk); err != nil {
		return 0, err
	}
	commit.Record.Handle = commit.Handle
	commit.Record.Revision = current.Revision + 1
	payload, _ := json.Marshal(map[string]any{
		"drain_sequence": commit.Seq,
		"rows":           len(commit.Page.Rows),
		"bytes":          commit.Page.Bytes,
		"truncated":      commit.Page.Truncated,
		"cursor_after":   commit.Handle,
	})
	if _, err := insertQueryJobEventMySQLTx(ctx, tx, QueryJobEvent{
		JobID: commit.JobID, Type: "drain_observation", ObservationKey: fmt.Sprintf("drain:%d", commit.Seq),
		Payload: payload, CreatedAt: nowUTC(),
	}); err != nil && !errors.Is(err, ErrConflict) {
		return 0, err
	}
	if err := syncWakeEventsMySQLTx(ctx, tx, commit.Record); err != nil {
		return 0, err
	}
	if err := syncCapacityReservationMySQLTx(ctx, tx, commit.Record); err != nil {
		return 0, err
	}
	// Keep the guarded lifecycle write last. If the lease expired while the
	// chunk/event side effects were being prepared, the zero-row update turns
	// the whole SQL transaction into ErrLeaseLost and every side effect rolls
	// back with it.
	if holder != "" {
		if err := updateQueryJobTxFenced(ctx, tx, commit.Record, commit.Record.Revision, current.Revision, current.DrainSeq, current.Status, holder); err != nil {
			return 0, err
		}
	} else if err := updateQueryJobTx(ctx, tx, commit.Record, commit.Record.Revision); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return commit.Record.Revision, nil
}
