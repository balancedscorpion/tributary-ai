package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/types"
)

type MySQLStore struct {
	db *sql.DB
}

func NewMySQL(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

func (s *MySQLStore) Ping(ctx context.Context) error {
	if s.db == nil {
		return errors.New("nil db")
	}
	return s.db.PingContext(ctx)
}

func nowUTC() time.Time { return time.Now().UTC() }

func (s *MySQLStore) CreateThread(ctx context.Context, projectID *string) (Thread, error) {
	id := types.NewUUID()
	now := nowUTC()
	title := "New Thread"
	status := "active"

	_, err := s.db.ExecContext(ctx, `INSERT INTO threads (id, title, summary, project_id, status, created_at, updated_at, last_message_at, last_run_id, next_seq)
		VALUES (?, ?, NULL, ?, ?, ?, ?, NULL, NULL, 1)`,
		id, title, projectID, status, now, now)
	if err != nil {
		return Thread{}, err
	}
	return Thread{ID: id, Title: title, ProjectID: projectID, Status: status, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *MySQLStore) ListThreads(ctx context.Context, status string, limit int) ([]Thread, error) {
	st := strings.TrimSpace(status)
	if st == "" {
		st = "active"
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.id, t.title, t.summary, t.run_count, t.summary_run_count, t.project_id, t.status, t.created_at, t.updated_at, t.last_message_at, t.last_run_id,
		t.last_target_connection_id, t.last_target_model, t.attachment_policies,
		(SELECT COUNT(*) FROM artifacts a WHERE a.thread_id=t.id) AS artifact_count
		FROM threads t WHERE t.status=? ORDER BY t.updated_at DESC LIMIT ?`, st, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Thread{}
	for rows.Next() {
		var t Thread
		var summary sql.NullString
		var projectID sql.NullString
		var lastMsg sql.NullTime
		var lastRun sql.NullString
		var targetConnection, targetModel sql.NullString
		var policiesJSON []byte
		if err := rows.Scan(&t.ID, &t.Title, &summary, &t.RunCount, &t.SummaryRunCount, &projectID, &t.Status, &t.CreatedAt, &t.UpdatedAt, &lastMsg, &lastRun, &targetConnection, &targetModel, &policiesJSON, &t.ArtifactCount); err != nil {
			return nil, err
		}
		if summary.Valid {
			t.Summary = &summary.String
		}
		if projectID.Valid {
			t.ProjectID = &projectID.String
		}
		if lastMsg.Valid {
			t.LastMessageAt = &lastMsg.Time
		}
		if lastRun.Valid {
			t.LastRunID = &lastRun.String
		}
		if err := applyThreadRuntimeFields(&t, targetConnection, targetModel, policiesJSON); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *MySQLStore) GetThread(ctx context.Context, threadID string) (Thread, error) {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return Thread{}, errors.New("threadId is required")
	}
	var t Thread
	var summary sql.NullString
	var projectID sql.NullString
	var lastMsg sql.NullTime
	var lastRun sql.NullString
	var targetConnection, targetModel sql.NullString
	var policiesJSON []byte
	row := s.db.QueryRowContext(ctx, `SELECT t.id, t.title, t.summary, t.run_count, t.summary_run_count, t.project_id, t.status, t.created_at, t.updated_at, t.last_message_at, t.last_run_id,
		t.last_target_connection_id, t.last_target_model, t.attachment_policies,
		(SELECT COUNT(*) FROM artifacts a WHERE a.thread_id=t.id) AS artifact_count
		FROM threads t WHERE t.id=?`, id)
	if err := row.Scan(&t.ID, &t.Title, &summary, &t.RunCount, &t.SummaryRunCount, &projectID, &t.Status, &t.CreatedAt, &t.UpdatedAt, &lastMsg, &lastRun, &targetConnection, &targetModel, &policiesJSON, &t.ArtifactCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Thread{}, ErrNotFound
		}
		return Thread{}, err
	}
	if summary.Valid {
		t.Summary = &summary.String
	}
	if projectID.Valid {
		t.ProjectID = &projectID.String
	}
	if lastMsg.Valid {
		t.LastMessageAt = &lastMsg.Time
	}
	if lastRun.Valid {
		t.LastRunID = &lastRun.String
	}
	if err := applyThreadRuntimeFields(&t, targetConnection, targetModel, policiesJSON); err != nil {
		return Thread{}, err
	}
	return t, nil
}

func (s *MySQLStore) ArchiveThread(ctx context.Context, threadID string) error {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE threads SET status='archived', updated_at=? WHERE id=?`, nowUTC(), id)
	return err
}

func (s *MySQLStore) DeleteThread(ctx context.Context, threadID string) error {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM threads WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MySQLStore) UpdateThreadTitleSummary(ctx context.Context, threadID string, title *string, summary *string) error {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	fields := []string{"updated_at=?"}
	args := []any{nowUTC()}
	if title != nil {
		fields = append(fields, "title=?")
		args = append(args, strings.TrimSpace(*title))
	}
	if summary != nil {
		fields = append(fields, "summary=?", "summary_run_count=run_count")
		args = append(args, *summary)
	}
	args = append(args, id)
	q := fmt.Sprintf("UPDATE threads SET %s WHERE id=?", strings.Join(fields, ", "))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func (s *MySQLStore) UpdateThreadProject(ctx context.Context, threadID string, projectID string) error {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return errors.New("projectId is required")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE threads SET project_id=?, updated_at=? WHERE id=?`, pid, nowUTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MySQLStore) IncrementThreadRunCount(ctx context.Context, threadID string) error {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE threads SET run_count=run_count+1 WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MySQLStore) SetThreadTarget(ctx context.Context, threadID string, target RunTarget) (Thread, error) {
	id := strings.TrimSpace(threadID)
	target.ConnectionID = strings.TrimSpace(target.ConnectionID)
	target.Model = strings.TrimSpace(target.Model)
	if id == "" || target.ConnectionID == "" || target.Model == "" {
		return Thread{}, fmt.Errorf("%w: threadId, connectionId, and model are required", ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE threads SET last_target_connection_id=?, last_target_model=?, updated_at=? WHERE id=?`, target.ConnectionID, target.Model, nowUTC(), id)
	if err != nil {
		return Thread{}, err
	}
	// MySQL may report zero affected rows for an idempotent update (notably
	// when DATETIME(3) rounds updated_at to its existing value). GetThread is
	// the authoritative existence check and preserves idempotent target saves.
	return s.GetThread(ctx, id)
}

func (s *MySQLStore) DeleteThreadAttachmentPolicy(ctx context.Context, threadID string, policyID string) (Thread, error) {
	id := strings.TrimSpace(threadID)
	policyID = strings.TrimSpace(policyID)
	if id == "" || policyID == "" {
		return Thread{}, fmt.Errorf("%w: threadId and policyId are required", ErrInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Thread{}, err
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT attachment_policies FROM threads WHERE id=? FOR UPDATE`, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Thread{}, ErrNotFound
		}
		return Thread{}, err
	}
	policies, err := decodeAttachmentPolicies(raw)
	if err != nil {
		return Thread{}, err
	}
	next := make([]AttachmentPolicy, 0, len(policies))
	removed := false
	for _, policy := range policies {
		if strings.TrimSpace(policy.ID) == policyID {
			removed = true
			continue
		}
		next = append(next, policy)
	}
	if !removed {
		return Thread{}, ErrNotFound
	}
	b, _ := json.Marshal(next)
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET attachment_policies=?, updated_at=? WHERE id=?`, b, nowUTC(), id); err != nil {
		return Thread{}, err
	}
	if err := tx.Commit(); err != nil {
		return Thread{}, err
	}
	return s.GetThread(ctx, id)
}

func (s *MySQLStore) CreateRun(ctx context.Context, threadID string, providerID string, model string, profileID *string, systemPrompt string, maxIterations int) (Run, error) {
	id := types.NewUUID()
	now := nowUTC()
	status := "queued"
	if maxIterations <= 0 {
		maxIterations = 200
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs (id, thread_id, status, provider_id, model, profile_id, system_prompt, max_iterations,
			request_id, input_tokens, output_tokens, total_tokens, error_code, error_message,
			context_tokens_before, context_tokens_after, context_summary_buckets, context_summary_rounds, context_artifact_refs_loaded,
			context_need_retrieval, context_need_retrieval_tooling, context_hard_capped, context_plan, purpose,
			created_at, started_at, ended_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'analysis', ?, NULL, NULL, ?)`,
		id, threadID, status, providerID, model, profileID, systemPrompt, maxIterations, now, now)
	if err != nil {
		return Run{}, err
	}
	return Run{ID: id, ThreadID: threadID, Status: status, Purpose: "analysis", ProviderID: providerID, Model: model, ProfileID: profileID, SystemPrompt: systemPrompt, MaxIterations: maxIterations, MaxAutoContinuations: 3, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *MySQLStore) GetRun(ctx context.Context, runID string) (Run, error) {
	id := strings.TrimSpace(runID)
	if id == "" {
		return Run{}, errors.New("runId is required")
	}
	var r Run
	var purpose sql.NullString
	var profileID sql.NullString
	var requestID sql.NullString
	var inputTokens sql.NullInt64
	var outputTokens sql.NullInt64
	var totalTokens sql.NullInt64
	var errorCode sql.NullString
	var errorMsg sql.NullString
	var contextTokensBefore sql.NullInt64
	var contextTokensAfter sql.NullInt64
	var contextSummaryBuckets sql.NullInt64
	var contextSummaryRounds sql.NullInt64
	var contextArtifactRefsLoaded sql.NullInt64
	var contextNeedRetrieval sql.NullBool
	var contextNeedRetrievalTooling sql.NullBool
	var contextHardCapped sql.NullBool
	var contextOverflowCompacted sql.NullBool
	var contextPlan sql.NullString
	var wakeAt, resumeLeaseExpiresAt sql.NullTime
	var resumeLeaseHolder sql.NullString
	var started sql.NullTime
	var ended sql.NullTime
	row := s.db.QueryRowContext(ctx, `SELECT id, thread_id, status, revision, provider_id, connection_id, plan_id, model, profile_id, system_prompt, max_iterations,
		continuation_count, max_auto_continuations, last_checkpoint_sequence, wake_at, resume_lease_holder, resume_lease_expires_at,
		request_id, input_tokens, output_tokens, total_tokens, error_code, error_message, purpose,
		context_tokens_before, context_tokens_after, context_summary_buckets, context_summary_rounds, context_artifact_refs_loaded,
		context_need_retrieval, context_need_retrieval_tooling, context_hard_capped, context_overflow_compacted, context_plan,
		created_at, started_at, ended_at, updated_at
		FROM runs WHERE id=?`, id)
	var connectionID, planID sql.NullString
	if err := row.Scan(&r.ID, &r.ThreadID, &r.Status, &r.Revision, &r.ProviderID, &connectionID, &planID, &r.Model, &profileID, &r.SystemPrompt, &r.MaxIterations,
		&r.ContinuationCount, &r.MaxAutoContinuations, &r.LastCheckpointSequence, &wakeAt, &resumeLeaseHolder, &resumeLeaseExpiresAt,
		&requestID, &inputTokens, &outputTokens, &totalTokens, &errorCode, &errorMsg, &purpose,
		&contextTokensBefore, &contextTokensAfter, &contextSummaryBuckets, &contextSummaryRounds, &contextArtifactRefsLoaded,
		&contextNeedRetrieval, &contextNeedRetrievalTooling, &contextHardCapped, &contextOverflowCompacted, &contextPlan,
		&r.CreatedAt, &started, &ended, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, ErrNotFound
		}
		return Run{}, err
	}
	if profileID.Valid {
		r.ProfileID = &profileID.String
	}
	if connectionID.Valid {
		r.ConnectionID = strings.TrimSpace(connectionID.String)
	}
	if planID.Valid {
		r.PlanID = strings.TrimSpace(planID.String)
	}
	if purpose.Valid {
		r.Purpose = strings.TrimSpace(purpose.String)
	}
	if wakeAt.Valid {
		v := wakeAt.Time
		r.WakeAt = &v
	}
	if resumeLeaseHolder.Valid {
		r.ResumeLeaseHolder = strings.TrimSpace(resumeLeaseHolder.String)
	}
	if resumeLeaseExpiresAt.Valid {
		v := resumeLeaseExpiresAt.Time
		r.ResumeLeaseExpiresAt = &v
	}
	if r.Purpose == "" {
		r.Purpose = "analysis"
	}
	if requestID.Valid {
		r.RequestID = &requestID.String
	}
	if inputTokens.Valid {
		i := int(inputTokens.Int64)
		r.InputTokens = &i
	}
	if outputTokens.Valid {
		i := int(outputTokens.Int64)
		r.OutputTokens = &i
	}
	if totalTokens.Valid {
		i := int(totalTokens.Int64)
		r.TotalTokens = &i
	}
	if errorCode.Valid {
		r.ErrorCode = &errorCode.String
	}
	if errorMsg.Valid {
		r.ErrorMessage = &errorMsg.String
	}
	if contextTokensBefore.Valid {
		v := int(contextTokensBefore.Int64)
		r.ContextTokensBefore = &v
	}
	if contextTokensAfter.Valid {
		v := int(contextTokensAfter.Int64)
		r.ContextTokensAfter = &v
	}
	if contextSummaryBuckets.Valid {
		v := int(contextSummaryBuckets.Int64)
		r.ContextSummaryBuckets = &v
	}
	if contextSummaryRounds.Valid {
		v := int(contextSummaryRounds.Int64)
		r.ContextSummaryRounds = &v
	}
	if contextArtifactRefsLoaded.Valid {
		v := int(contextArtifactRefsLoaded.Int64)
		r.ContextArtifactRefsLoaded = &v
	}
	if contextNeedRetrieval.Valid {
		v := contextNeedRetrieval.Bool
		r.ContextNeedRetrieval = &v
	}
	if contextNeedRetrievalTooling.Valid {
		v := contextNeedRetrievalTooling.Bool
		r.ContextNeedTooling = &v
	}
	if contextHardCapped.Valid {
		v := contextHardCapped.Bool
		r.ContextHardCapped = &v
	}
	if contextOverflowCompacted.Valid {
		v := contextOverflowCompacted.Bool
		r.ContextOverflowCompacted = &v
	}
	if contextPlan.Valid {
		r.ContextPlanJSON = json.RawMessage(contextPlan.String)
	}
	if started.Valid {
		r.StartedAt = &started.Time
	}
	if ended.Valid {
		r.EndedAt = &ended.Time
	}
	return r, nil
}

func (s *MySQLStore) ListRuns(ctx context.Context, threadID string, limit int) ([]Run, error) {
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, thread_id, status, revision, provider_id, connection_id, plan_id, model, profile_id, system_prompt, max_iterations,
		continuation_count, max_auto_continuations, last_checkpoint_sequence, wake_at, resume_lease_holder, resume_lease_expires_at,
		request_id, input_tokens, output_tokens, total_tokens, error_code, error_message, purpose,
		context_tokens_before, context_tokens_after, context_summary_buckets, context_summary_rounds, context_artifact_refs_loaded,
		context_need_retrieval, context_need_retrieval_tooling, context_hard_capped, context_overflow_compacted, context_plan,
		created_at, started_at, ended_at, updated_at
		FROM runs WHERE thread_id=? ORDER BY created_at DESC LIMIT ?`, tid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Run, 0, limit)
	for rows.Next() {
		var r Run
		var purpose sql.NullString
		var profileID sql.NullString
		var requestID sql.NullString
		var inputTokens sql.NullInt64
		var outputTokens sql.NullInt64
		var totalTokens sql.NullInt64
		var errorCode sql.NullString
		var errorMsg sql.NullString
		var contextTokensBefore sql.NullInt64
		var contextTokensAfter sql.NullInt64
		var contextSummaryBuckets sql.NullInt64
		var contextSummaryRounds sql.NullInt64
		var contextArtifactRefsLoaded sql.NullInt64
		var contextNeedRetrieval sql.NullBool
		var contextNeedRetrievalTooling sql.NullBool
		var contextHardCapped sql.NullBool
		var contextOverflowCompacted sql.NullBool
		var contextPlan sql.NullString
		var wakeAt, resumeLeaseExpiresAt sql.NullTime
		var resumeLeaseHolder sql.NullString
		var started sql.NullTime
		var ended sql.NullTime
		var connectionID, planID sql.NullString
		if err := rows.Scan(&r.ID, &r.ThreadID, &r.Status, &r.Revision, &r.ProviderID, &connectionID, &planID, &r.Model, &profileID, &r.SystemPrompt, &r.MaxIterations,
			&r.ContinuationCount, &r.MaxAutoContinuations, &r.LastCheckpointSequence, &wakeAt, &resumeLeaseHolder, &resumeLeaseExpiresAt,
			&requestID, &inputTokens, &outputTokens, &totalTokens, &errorCode, &errorMsg, &purpose,
			&contextTokensBefore, &contextTokensAfter, &contextSummaryBuckets, &contextSummaryRounds, &contextArtifactRefsLoaded,
			&contextNeedRetrieval, &contextNeedRetrievalTooling, &contextHardCapped, &contextOverflowCompacted, &contextPlan,
			&r.CreatedAt, &started, &ended, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if profileID.Valid {
			v := profileID.String
			r.ProfileID = &v
		}
		if connectionID.Valid {
			r.ConnectionID = strings.TrimSpace(connectionID.String)
		}
		if planID.Valid {
			r.PlanID = strings.TrimSpace(planID.String)
		}
		if purpose.Valid {
			r.Purpose = strings.TrimSpace(purpose.String)
		}
		if r.Purpose == "" {
			r.Purpose = "analysis"
		}
		if wakeAt.Valid {
			v := wakeAt.Time
			r.WakeAt = &v
		}
		if resumeLeaseHolder.Valid {
			r.ResumeLeaseHolder = strings.TrimSpace(resumeLeaseHolder.String)
		}
		if resumeLeaseExpiresAt.Valid {
			v := resumeLeaseExpiresAt.Time
			r.ResumeLeaseExpiresAt = &v
		}
		if requestID.Valid {
			v := requestID.String
			r.RequestID = &v
		}
		if inputTokens.Valid {
			v := int(inputTokens.Int64)
			r.InputTokens = &v
		}
		if outputTokens.Valid {
			v := int(outputTokens.Int64)
			r.OutputTokens = &v
		}
		if totalTokens.Valid {
			v := int(totalTokens.Int64)
			r.TotalTokens = &v
		}
		if errorCode.Valid {
			v := errorCode.String
			r.ErrorCode = &v
		}
		if errorMsg.Valid {
			v := errorMsg.String
			r.ErrorMessage = &v
		}
		if contextTokensBefore.Valid {
			v := int(contextTokensBefore.Int64)
			r.ContextTokensBefore = &v
		}
		if contextTokensAfter.Valid {
			v := int(contextTokensAfter.Int64)
			r.ContextTokensAfter = &v
		}
		if contextSummaryBuckets.Valid {
			v := int(contextSummaryBuckets.Int64)
			r.ContextSummaryBuckets = &v
		}
		if contextSummaryRounds.Valid {
			v := int(contextSummaryRounds.Int64)
			r.ContextSummaryRounds = &v
		}
		if contextArtifactRefsLoaded.Valid {
			v := int(contextArtifactRefsLoaded.Int64)
			r.ContextArtifactRefsLoaded = &v
		}
		if contextNeedRetrieval.Valid {
			v := contextNeedRetrieval.Bool
			r.ContextNeedRetrieval = &v
		}
		if contextNeedRetrievalTooling.Valid {
			v := contextNeedRetrievalTooling.Bool
			r.ContextNeedTooling = &v
		}
		if contextHardCapped.Valid {
			v := contextHardCapped.Bool
			r.ContextHardCapped = &v
		}
		if contextOverflowCompacted.Valid {
			v := contextOverflowCompacted.Bool
			r.ContextOverflowCompacted = &v
		}
		if contextPlan.Valid {
			r.ContextPlanJSON = json.RawMessage(contextPlan.String)
		}
		if started.Valid {
			v := started.Time
			r.StartedAt = &v
		}
		if ended.Valid {
			v := ended.Time
			r.EndedAt = &v
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *MySQLStore) UpdateRunStatus(ctx context.Context, runID string, status string, fields map[string]any) error {
	id := strings.TrimSpace(runID)
	if id == "" {
		return errors.New("runId is required")
	}
	st := strings.TrimSpace(status)
	if st == "" {
		return errors.New("status is required")
	}
	set := []string{"status=?", "revision=revision+1", "updated_at=?"}
	args := []any{st, nowUTC()}
	for k, v := range fields {
		set = append(set, k+"=?")
		args = append(args, v)
	}
	args = append(args, id)
	q := fmt.Sprintf("UPDATE runs SET %s WHERE id=?", strings.Join(set, ", "))
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MySQLStore) AppendRunEvent(ctx context.Context, ev RunEvent) (RunEvent, error) {
	runID := strings.TrimSpace(ev.RunID)
	if runID == "" {
		return RunEvent{}, errors.New("runId is required")
	}
	if ev.Seq < 0 {
		return RunEvent{}, errors.New("runEvent.seq must be >= 0")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RunEvent{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var threadID string
	if err := tx.QueryRowContext(ctx, `SELECT thread_id FROM runs WHERE id=? FOR UPDATE`, runID).Scan(&threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunEvent{}, ErrNotFound
		}
		return RunEvent{}, err
	}
	if tid := strings.TrimSpace(ev.ThreadID); tid == "" {
		ev.ThreadID = threadID
	} else if tid != threadID {
		return RunEvent{}, errors.New("runEvent.thread_id does not match run")
	}
	ev.RunID = runID
	var latest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT seq FROM run_events WHERE run_id=? ORDER BY seq DESC LIMIT 1 FOR UPDATE`, runID).Scan(&latest); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return RunEvent{}, err
	}
	if ev.Seq == 0 {
		ev.Seq = latest.Int64 + 1
	} else if ev.Seq <= latest.Int64 {
		return RunEvent{}, ErrConflict
	}
	prepared, err := prepareRunEvent(ev)
	if err != nil {
		return RunEvent{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO run_events (run_id, thread_id, seq, type, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		prepared.RunID, prepared.ThreadID, prepared.Seq, prepared.Type, string(prepared.Data), prepared.CreatedAt)
	if err != nil {
		if isDuplicateKeyError(err) {
			return RunEvent{}, ErrConflict
		}
		return RunEvent{}, err
	}
	if err := tx.Commit(); err != nil {
		return RunEvent{}, err
	}
	return prepared, nil
}

func (s *MySQLStore) ListRunEvents(ctx context.Context, p ListRunEventsParams) ([]RunEvent, error) {
	runID := strings.TrimSpace(p.RunID)
	if runID == "" {
		return nil, errors.New("runId is required")
	}
	if _, err := s.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, thread_id, seq, type, data, created_at
		FROM run_events
		WHERE run_id=? AND seq>?
		ORDER BY seq ASC
		LIMIT ?`, runID, p.AfterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RunEvent, 0, limit)
	for rows.Next() {
		ev, err := scanRunEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *MySQLStore) CreateRunPlan(ctx context.Context, plan RunPlan) (RunPlan, error) {
	if err := validateNewRunPlan(plan); err != nil {
		return RunPlan{}, err
	}
	blockers, err := json.Marshal(plan.Blockers)
	if err != nil {
		return RunPlan{}, err
	}
	choices, err := json.Marshal(plan.Choices)
	if err != nil {
		return RunPlan{}, err
	}
	notices, err := json.Marshal(plan.Notices)
	if err != nil {
		return RunPlan{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO run_plans
		(plan_id, thread_id, run_id, target_connection_id, target_model, config_fingerprint,
		 requirements_hash, result_status, status, blockers, choices, notices, stale_evidence,
		 capability_report, choice_selections, created_at, expires_at, finalized_at)
		VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, NULL)`,
		plan.ID, plan.ThreadID, plan.Target.ConnectionID, plan.Target.Model, nullIfBlank(plan.ConfigFingerprint),
		plan.RequirementsHash, plan.ResultStatus, plan.Status, blockers, choices, notices, plan.StaleEvidence,
		[]byte(plan.CapabilityReport), plan.CreatedAt.UTC(), plan.ExpiresAt.UTC())
	if err != nil {
		if isDuplicateKeyError(err) {
			return RunPlan{}, ErrConflict
		}
		return RunPlan{}, err
	}
	return cloneRunPlan(plan), nil
}

func (s *MySQLStore) GetRunPlan(ctx context.Context, planID string) (RunPlan, error) {
	id := strings.TrimSpace(planID)
	if id == "" {
		return RunPlan{}, fmt.Errorf("%w: planId is required", ErrInvalid)
	}
	plan, err := scanMySQLRunPlan(s.db.QueryRowContext(ctx, runPlanSelectSQL+` WHERE plan_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return RunPlan{}, ErrNotFound
	}
	return plan, err
}

func (s *MySQLStore) GetRunPlanForRun(ctx context.Context, runID string) (RunPlan, error) {
	id := strings.TrimSpace(runID)
	if id == "" {
		return RunPlan{}, fmt.Errorf("%w: runId is required", ErrInvalid)
	}
	var planID sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT plan_id FROM runs WHERE id=?`, id).Scan(&planID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RunPlan{}, ErrNotFound
		}
		return RunPlan{}, err
	}
	if !planID.Valid || strings.TrimSpace(planID.String) == "" {
		return RunPlan{}, ErrNotFound
	}
	return s.GetRunPlan(ctx, planID.String)
}

func (s *MySQLStore) FinalizeRunPlanAndCreateRun(ctx context.Context, req FinalizeRunPlanRequest) (Run, RunPlan, error) {
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		return Run{}, RunPlan{}, ErrPlanStale
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	defer tx.Rollback()
	plan, err := scanMySQLRunPlan(tx.QueryRowContext(ctx, runPlanSelectSQL+` WHERE plan_id=? FOR UPDATE`, planID))
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, RunPlan{}, ErrPlanStale
	}
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	if plan.Status == RunPlanStatusFinalized {
		if err := validateFinalizedPlanReplay(plan, req); err != nil {
			return Run{}, RunPlan{}, ErrPlanStale
		}
		runID := *plan.RunID
		if err := tx.Commit(); err != nil {
			return Run{}, RunPlan{}, err
		}
		run, err := s.GetRun(ctx, runID)
		return run, plan, err
	}
	selections, policies, err := validatePlanFinalization(plan, req)
	if err != nil {
		return Run{}, RunPlan{}, stalePlanError(err)
	}
	now := req.Now.UTC()
	if now.IsZero() {
		now = nowUTC()
	}

	var policiesJSON []byte
	if err := tx.QueryRowContext(ctx, `SELECT attachment_policies FROM threads WHERE id=? FOR UPDATE`, plan.ThreadID).Scan(&policiesJSON); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Run{}, RunPlan{}, ErrNotFound
		}
		return Run{}, RunPlan{}, err
	}
	threadPolicies, err := decodeAttachmentPolicies(policiesJSON)
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	if expected := strings.TrimSpace(req.ExpectedThreadPolicyFingerprint); expected != "" &&
		AttachmentPoliciesFingerprint(threadPolicies) != expected {
		return Run{}, RunPlan{}, ErrPlanStale
	}
	for _, policy := range policies {
		threadPolicies = appendPolicyIdempotent(threadPolicies, policy)
	}
	policiesJSON, _ = json.Marshal(threadPolicies)

	runID := types.NewUUID()
	maxIterations := req.Run.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 200
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runs
		(id, thread_id, status, provider_id, connection_id, plan_id, model, profile_id, system_prompt, max_iterations,
		 request_id, input_tokens, output_tokens, total_tokens, error_code, error_message,
		 context_tokens_before, context_tokens_after, context_summary_buckets, context_summary_rounds, context_artifact_refs_loaded,
		 context_need_retrieval, context_need_retrieval_tooling, context_hard_capped, context_plan, purpose,
		 created_at, started_at, ended_at, updated_at)
		VALUES (?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?,
		 NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 'analysis',
		 ?, NULL, NULL, ?)`,
		runID, plan.ThreadID, strings.TrimSpace(req.Run.ProviderID), plan.Target.ConnectionID, plan.ID,
		plan.Target.Model, req.Run.ProfileID, req.Run.SystemPrompt, maxIterations, now, now)
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE threads SET last_target_connection_id=?, last_target_model=?, attachment_policies=?, updated_at=? WHERE id=?`,
		plan.Target.ConnectionID, plan.Target.Model, policiesJSON, now, plan.ThreadID); err != nil {
		return Run{}, RunPlan{}, err
	}
	selectionsJSON, _ := json.Marshal(selections)
	result, err := tx.ExecContext(ctx, `UPDATE run_plans SET status='finalized', run_id=?, choice_selections=?, finalized_at=? WHERE plan_id=? AND status='draft'`,
		runID, selectionsJSON, now, plan.ID)
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return Run{}, RunPlan{}, err
	} else if affected != 1 {
		return Run{}, RunPlan{}, ErrPlanStale
	}
	if err := tx.Commit(); err != nil {
		return Run{}, RunPlan{}, err
	}
	plan.Status = RunPlanStatusFinalized
	plan.Selections = selections
	plan.RunID = &runID
	plan.FinalizedAt = &now
	run := Run{
		ID: runID, ThreadID: plan.ThreadID, Status: "queued", Purpose: "analysis",
		ProviderID: strings.TrimSpace(req.Run.ProviderID), ConnectionID: plan.Target.ConnectionID,
		PlanID: plan.ID, Model: plan.Target.Model, ProfileID: req.Run.ProfileID,
		SystemPrompt: req.Run.SystemPrompt, MaxIterations: maxIterations, MaxAutoContinuations: 3, CreatedAt: now, UpdatedAt: now,
	}
	return run, cloneRunPlan(plan), nil
}

func (s *MySQLStore) DeleteExpiredRunPlans(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = nowUTC()
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM run_plans WHERE status='draft' AND expires_at<=?`, now.UTC())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return n, err
}

func (s *MySQLStore) UpsertProviderCapabilityEvidence(ctx context.Context, evidence ProviderCapabilityEvidence) (ProviderCapabilityEvidence, error) {
	prepared, err := prepareProviderCapabilityEvidence(evidence)
	if err != nil {
		return ProviderCapabilityEvidence{}, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO provider_capability_evidence
		(connection_id, model, fingerprint, probe, verdict, reason, message, request_id, duration_ms, verified_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
		verdict=IF(VALUES(verified_at)>=verified_at, VALUES(verdict), verdict),
		reason=IF(VALUES(verified_at)>=verified_at, VALUES(reason), reason),
		message=IF(VALUES(verified_at)>=verified_at, VALUES(message), message),
		request_id=IF(VALUES(verified_at)>=verified_at, VALUES(request_id), request_id),
		duration_ms=IF(VALUES(verified_at)>=verified_at, VALUES(duration_ms), duration_ms),
		verified_at=GREATEST(verified_at, VALUES(verified_at))`,
		prepared.ConnectionID, prepared.Model, prepared.Fingerprint, prepared.Probe, prepared.Verdict,
		nullIfBlank(prepared.Reason), prepared.Message, nullIfBlank(prepared.RequestID), prepared.DurationMS, prepared.VerifiedAt)
	if err != nil {
		return ProviderCapabilityEvidence{}, err
	}
	stored, err := s.ListProviderCapabilityEvidence(ctx, ProviderCapabilityEvidenceFilter{
		ConnectionID: prepared.ConnectionID,
		Model:        prepared.Model,
		Fingerprint:  prepared.Fingerprint,
		Probe:        prepared.Probe,
		Limit:        1,
	})
	if err != nil {
		return ProviderCapabilityEvidence{}, err
	}
	if len(stored) != 1 {
		return ProviderCapabilityEvidence{}, errors.New("capability evidence upsert could not be read back")
	}
	return stored[0], nil
}

func (s *MySQLStore) ListProviderCapabilityEvidence(ctx context.Context, filter ProviderCapabilityEvidenceFilter) ([]ProviderCapabilityEvidence, error) {
	filter, err := normalizeProviderCapabilityEvidenceFilter(filter)
	if err != nil {
		return nil, err
	}
	conditions := []string{"connection_id=?"}
	args := []any{filter.ConnectionID}
	if filter.Model != "" {
		conditions = append(conditions, "model=?")
		args = append(args, filter.Model)
	}
	if filter.Fingerprint != "" {
		conditions = append(conditions, "fingerprint=?")
		args = append(args, filter.Fingerprint)
	}
	if filter.Probe != "" {
		conditions = append(conditions, "probe=?")
		args = append(args, filter.Probe)
	}
	args = append(args, filter.Limit)
	query := `SELECT connection_id, model, fingerprint, probe, verdict, reason, message, request_id, duration_ms, verified_at
		FROM provider_capability_evidence WHERE ` + strings.Join(conditions, " AND ") + ` ORDER BY verified_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ProviderCapabilityEvidence, 0)
	for rows.Next() {
		var evidence ProviderCapabilityEvidence
		var reason, requestID sql.NullString
		if err := rows.Scan(&evidence.ConnectionID, &evidence.Model, &evidence.Fingerprint, &evidence.Probe,
			&evidence.Verdict, &reason, &evidence.Message, &requestID, &evidence.DurationMS, &evidence.VerifiedAt); err != nil {
			return nil, err
		}
		if reason.Valid {
			evidence.Reason = strings.TrimSpace(reason.String)
		}
		if requestID.Valid {
			evidence.RequestID = strings.TrimSpace(requestID.String)
		}
		prepared, err := validateStoredProviderCapabilityEvidence(evidence)
		if err != nil {
			return nil, err
		}
		out = append(out, prepared)
	}
	return out, rows.Err()
}

func (s *MySQLStore) LatestRunEventSeq(ctx context.Context, runID string) (int64, error) {
	id := strings.TrimSpace(runID)
	if id == "" {
		return 0, errors.New("runId is required")
	}
	if _, err := s.GetRun(ctx, id); err != nil {
		return 0, err
	}
	var latest sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(seq) FROM run_events WHERE run_id=?`, id).Scan(&latest); err != nil {
		return 0, err
	}
	if !latest.Valid {
		return 0, nil
	}
	return latest.Int64, nil
}

func (s *MySQLStore) CreateApprovalRequest(ctx context.Context, approval ApprovalRequest) (ApprovalRequest, error) {
	prepared, err := prepareApprovalRequest(approval)
	if err != nil {
		return ApprovalRequest{}, err
	}
	run, err := s.GetRun(ctx, prepared.RunID)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if strings.TrimSpace(run.ThreadID) != prepared.ThreadID {
		return ApprovalRequest{}, errors.New("approval run_id does not match thread_id")
	}
	subjectJSON, err := json.Marshal(prepared.Subject)
	if err != nil {
		return ApprovalRequest{}, err
	}
	previewJSON, err := json.Marshal(prepared.Preview)
	if err != nil {
		return ApprovalRequest{}, err
	}
	optionsJSON, err := json.Marshal(prepared.Options)
	if err != nil {
		return ApprovalRequest{}, err
	}
	var preview any
	if len(prepared.Preview) > 0 {
		preview = string(previewJSON)
	}
	var options any
	if len(prepared.Options) > 0 {
		options = string(optionsJSON)
	}
	var toolCallID any
	if strings.TrimSpace(prepared.ToolCallID) != "" {
		toolCallID = prepared.ToolCallID
	}
	var operation any
	if strings.TrimSpace(prepared.Operation) != "" {
		operation = prepared.Operation
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO approval_requests
		(id, thread_id, run_id, tool_call_id, tool, category, operation, subject, summary, preview, options_json, status, decision, created_at, expires_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CAST(? AS JSON), ?, CAST(? AS JSON), CAST(? AS JSON), ?, NULL, ?, ?, ?)`,
		prepared.ID, prepared.ThreadID, prepared.RunID, toolCallID, prepared.Tool, prepared.Category, operation,
		string(subjectJSON), prepared.Summary, preview, options, prepared.Status, prepared.CreatedAt, prepared.ExpiresAt, prepared.UpdatedAt)
	if err != nil {
		if isDuplicateKeyError(err) {
			return ApprovalRequest{}, ErrConflict
		}
		return ApprovalRequest{}, err
	}
	return prepared, nil
}

func (s *MySQLStore) GetApprovalRequest(ctx context.Context, approvalID string) (ApprovalRequest, error) {
	id := strings.TrimSpace(approvalID)
	if id == "" {
		return ApprovalRequest{}, errors.New("approvalId is required")
	}
	row := s.db.QueryRowContext(ctx, approvalRequestSelectSQL()+` WHERE id=?`, id)
	out, err := scanApprovalRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ApprovalRequest{}, ErrNotFound
		}
		return ApprovalRequest{}, err
	}
	return out, nil
}

func (s *MySQLStore) ListApprovalRequests(ctx context.Context, filter ApprovalFilter) ([]ApprovalRequest, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	where := []string{"1=1"}
	args := []any{}
	if tid := strings.TrimSpace(filter.ThreadID); tid != "" {
		where = append(where, "thread_id=?")
		args = append(args, tid)
	}
	if rid := strings.TrimSpace(filter.RunID); rid != "" {
		where = append(where, "run_id=?")
		args = append(args, rid)
	}
	if status := strings.TrimSpace(strings.ToLower(filter.Status)); status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, approvalRequestSelectSQL()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at ASC, id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ApprovalRequest{}
	for rows.Next() {
		approval, err := scanApprovalRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, approval)
	}
	return out, rows.Err()
}

func (s *MySQLStore) DecideApprovalRequest(ctx context.Context, approvalID string, decision ApprovalDecision, status string) (ApprovalRequest, error) {
	id := strings.TrimSpace(approvalID)
	if id == "" {
		return ApprovalRequest{}, errors.New("approvalId is required")
	}
	st := strings.TrimSpace(strings.ToLower(status))
	switch st {
	case "approved", "rejected", "expired", "canceled":
	default:
		return ApprovalRequest{}, errors.New("approval status must be approved, rejected, expired, or canceled")
	}
	now := nowUTC()
	decision.Action = strings.TrimSpace(strings.ToLower(decision.Action))
	if decision.Scope == "" {
		decision.Scope = "once"
	}
	decision.Scope = strings.TrimSpace(strings.ToLower(decision.Scope))
	if decision.Subject != nil {
		subject := clonePermissionSubject(*decision.Subject)
		decision.Subject = &subject
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = now
	}
	decision.DecidedAt = decision.DecidedAt.UTC()
	decisionJSON, err := json.Marshal(decision)
	if err != nil {
		return ApprovalRequest{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ApprovalRequest{}, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, approvalRequestSelectSQL()+` WHERE id=? FOR UPDATE`, id)
	current, err := scanApprovalRequest(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ApprovalRequest{}, ErrNotFound
		}
		return ApprovalRequest{}, err
	}
	if strings.TrimSpace(strings.ToLower(current.Status)) == "pending" {
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status=?, decision=CAST(? AS JSON), updated_at=? WHERE id=?`, st, string(decisionJSON), now, id); err != nil {
			return ApprovalRequest{}, err
		}
		current.Status = st
		current.Decision = &decision
		current.UpdatedAt = now
	}
	if err := tx.Commit(); err != nil {
		return ApprovalRequest{}, err
	}
	return current, nil
}

func (s *MySQLStore) CancelRunApprovalRequests(ctx context.Context, runID string) error {
	id := strings.TrimSpace(runID)
	if id == "" {
		return errors.New("runId is required")
	}
	now := nowUTC()
	decisionJSON, err := json.Marshal(ApprovalDecision{Action: "cancel", Scope: "once", DecidedAt: now})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE approval_requests
		SET status='canceled', decision=CAST(? AS JSON), updated_at=?
		WHERE run_id=? AND status='pending'`, string(decisionJSON), now, id)
	return err
}

func (s *MySQLStore) ExpireStaleApprovalRequests(ctx context.Context, now time.Time) error {
	if now.IsZero() {
		now = nowUTC()
	}
	now = now.UTC()
	decisionJSON, err := json.Marshal(ApprovalDecision{Action: "expire", Scope: "once", DecidedAt: now})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE approval_requests
		SET status='expired', decision=CAST(? AS JSON), updated_at=?
		WHERE status='pending' AND expires_at<=?`, string(decisionJSON), now, now)
	return err
}

func (s *MySQLStore) UpsertLocalSearchJob(ctx context.Context, job LocalSearchJob) (LocalSearchJob, error) {
	prepared, err := prepareLocalSearchJob(job)
	if err != nil {
		return LocalSearchJob{}, err
	}
	run, err := s.GetRun(ctx, prepared.RunID)
	if err != nil {
		return LocalSearchJob{}, err
	}
	if strings.TrimSpace(run.ThreadID) != prepared.ThreadID {
		return LocalSearchJob{}, errors.New("local_search_job run_id does not match thread_id")
	}
	resultJSON, err := json.Marshal(prepared.Result)
	if err != nil {
		return LocalSearchJob{}, err
	}
	var toolCallID any
	if strings.TrimSpace(prepared.ToolCallID) != "" {
		toolCallID = prepared.ToolCallID
	}
	var grantID any
	if strings.TrimSpace(prepared.GrantID) != "" {
		grantID = prepared.GrantID
	}
	var localRoot any
	if strings.TrimSpace(prepared.LocalRoot) != "" {
		localRoot = prepared.LocalRoot
	}
	var localKind any
	if strings.TrimSpace(prepared.LocalKind) != "" {
		localKind = prepared.LocalKind
	}
	var completedAt any
	if prepared.CompletedAt != nil {
		completedAt = *prepared.CompletedAt
	}
	var errText any
	if strings.TrimSpace(prepared.Error) != "" {
		errText = prepared.Error
	}
	var result any
	if len(prepared.Result) > 0 {
		result = string(resultJSON)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO local_search_jobs
		(id, thread_id, run_id, tool_call_id, status, query, grant_id, local_root, local_kind, created_at, updated_at, completed_at, files_count, error, result)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CAST(? AS JSON))
		ON DUPLICATE KEY UPDATE
			thread_id=VALUES(thread_id),
			run_id=VALUES(run_id),
			tool_call_id=VALUES(tool_call_id),
			status=VALUES(status),
			query=VALUES(query),
			grant_id=VALUES(grant_id),
			local_root=VALUES(local_root),
			local_kind=VALUES(local_kind),
			created_at=VALUES(created_at),
			updated_at=VALUES(updated_at),
			completed_at=VALUES(completed_at),
			files_count=VALUES(files_count),
			error=VALUES(error),
			result=VALUES(result)`,
		prepared.ID, prepared.ThreadID, prepared.RunID, toolCallID, prepared.Status, prepared.Query, grantID, localRoot, localKind,
		prepared.CreatedAt, prepared.UpdatedAt, completedAt, prepared.FilesCount, errText, result)
	if err != nil {
		return LocalSearchJob{}, err
	}
	return prepared, nil
}

func (s *MySQLStore) GetLocalSearchJob(ctx context.Context, jobID string) (LocalSearchJob, error) {
	id := strings.TrimSpace(jobID)
	if id == "" {
		return LocalSearchJob{}, errors.New("jobId is required")
	}
	row := s.db.QueryRowContext(ctx, localSearchJobSelectSQL()+` WHERE id=?`, id)
	out, err := scanLocalSearchJob(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LocalSearchJob{}, ErrNotFound
		}
		return LocalSearchJob{}, err
	}
	return out, nil
}

func (s *MySQLStore) ListLocalSearchJobs(ctx context.Context, filter LocalSearchJobFilter) ([]LocalSearchJob, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	where := []string{"1=1"}
	args := []any{}
	if tid := strings.TrimSpace(filter.ThreadID); tid != "" {
		where = append(where, "thread_id=?")
		args = append(args, tid)
	}
	if rid := strings.TrimSpace(filter.RunID); rid != "" {
		where = append(where, "run_id=?")
		args = append(args, rid)
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, localSearchJobSelectSQL()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LocalSearchJob{}
	for rows.Next() {
		job, err := scanLocalSearchJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (s *MySQLStore) GetActiveContextCheckpoint(ctx context.Context, threadID string) (*ContextCheckpoint, error) {
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if _, err := s.GetThread(ctx, tid); err != nil {
		return nil, err
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, thread_id, run_id, source_message_seq, source_message_id,
		replacement_history, summary, summary_message_ids, referenced_artifact_ids, token_estimate, chars_per_token,
		model, provider_id, plan, created_at, updated_at
		FROM chat_context_checkpoints
		WHERE thread_id=? AND is_active=1
		ORDER BY source_message_seq DESC, updated_at DESC, created_at DESC, id DESC
		LIMIT 1`, tid)
	cp, err := scanContextCheckpoint(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &cp, nil
}

func (s *MySQLStore) UpsertActiveContextCheckpoint(ctx context.Context, checkpoint ContextCheckpoint) (ContextCheckpoint, error) {
	cp, err := prepareContextCheckpoint(checkpoint)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	historyJSON, err := json.Marshal(cp.ReplacementHistory)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	summaryIDsJSON, err := json.Marshal(cp.SummaryMessageIDs)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	artifactIDsJSON, err := json.Marshal(cp.ReferencedArtifactIDs)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	var plan any = nil
	if len(cp.PlanJSON) > 0 {
		plan = string(cp.PlanJSON)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var active ContextCheckpoint
	hasActive := false
	activeRow := tx.QueryRowContext(ctx, `SELECT id, thread_id, run_id, source_message_seq, source_message_id,
		replacement_history, summary, summary_message_ids, referenced_artifact_ids, token_estimate, chars_per_token,
		model, provider_id, plan, created_at, updated_at
		FROM chat_context_checkpoints
		WHERE thread_id=? AND is_active=1
		ORDER BY source_message_seq DESC, updated_at DESC, created_at DESC, id DESC
		LIMIT 1
		FOR UPDATE`, cp.ThreadID)
	if scanned, err := scanContextCheckpoint(activeRow); err == nil {
		active = scanned
		hasActive = true
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ContextCheckpoint{}, err
	}

	staleActivation := hasActive && active.SourceMessageSeq > cp.SourceMessageSeq
	if staleActivation && active.ID == cp.ID {
		if err := tx.Commit(); err != nil {
			return ContextCheckpoint{}, err
		}
		return active, nil
	}
	isActive := 1
	if staleActivation {
		isActive = 0
	} else if _, err := tx.ExecContext(ctx, `UPDATE chat_context_checkpoints SET is_active=0, updated_at=? WHERE thread_id=? AND is_active=1`, cp.UpdatedAt, cp.ThreadID); err != nil {
		return ContextCheckpoint{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO chat_context_checkpoints
		(id, thread_id, run_id, source_message_seq, source_message_id, replacement_history, summary, summary_message_ids,
		 referenced_artifact_ids, token_estimate, chars_per_token, model, provider_id, plan, is_active, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			run_id=VALUES(run_id),
			source_message_seq=VALUES(source_message_seq),
			source_message_id=VALUES(source_message_id),
			replacement_history=VALUES(replacement_history),
			summary=VALUES(summary),
			summary_message_ids=VALUES(summary_message_ids),
			referenced_artifact_ids=VALUES(referenced_artifact_ids),
			token_estimate=VALUES(token_estimate),
			chars_per_token=VALUES(chars_per_token),
			model=VALUES(model),
			provider_id=VALUES(provider_id),
			plan=VALUES(plan),
			is_active=VALUES(is_active),
			created_at=VALUES(created_at),
			updated_at=VALUES(updated_at)`,
		cp.ID, cp.ThreadID, cp.RunID, cp.SourceMessageSeq, cp.SourceMessageID, string(historyJSON), cp.Summary, string(summaryIDsJSON),
		string(artifactIDsJSON), cp.TokenEstimate, cp.CharsPerToken, cp.Model, cp.ProviderID, plan, isActive, cp.CreatedAt, cp.UpdatedAt)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContextCheckpoint{}, err
	}
	if staleActivation {
		return active, nil
	}
	return cp, nil
}

func (s *MySQLStore) ListContextCheckpoints(ctx context.Context, threadID string, limit int) ([]ContextCheckpoint, error) {
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if _, err := s.GetThread(ctx, tid); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, thread_id, run_id, source_message_seq, source_message_id,
		replacement_history, summary, summary_message_ids, referenced_artifact_ids, token_estimate, chars_per_token,
		model, provider_id, plan, created_at, updated_at
		FROM chat_context_checkpoints
		WHERE thread_id=?
		ORDER BY created_at DESC, id DESC
		LIMIT ?`, tid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ContextCheckpoint, 0, limit)
	for rows.Next() {
		cp, err := scanContextCheckpoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cp)
	}
	return out, rows.Err()
}

func (s *MySQLStore) AppendMessage(ctx context.Context, threadID string, runID *string, msg Message) (Message, error) {
	if strings.TrimSpace(threadID) == "" {
		return Message{}, errors.New("threadId is required")
	}
	// Allocate seq via threads.next_seq row lock.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var nextSeq int64
	row := tx.QueryRowContext(ctx, `SELECT next_seq FROM threads WHERE id=? FOR UPDATE`, threadID)
	if err := row.Scan(&nextSeq); err != nil {
		return Message{}, err
	}

	seq := nextSeq
	now := nowUTC()
	_, err = tx.ExecContext(ctx, `UPDATE threads SET next_seq=next_seq+1, updated_at=?, last_message_at=? WHERE id=?`, now, now, threadID)
	if err != nil {
		return Message{}, err
	}

	id := msg.ID
	if strings.TrimSpace(id) == "" {
		id = types.NewUUID()
	}

	role := msg.Role
	if role == "" {
		return Message{}, errors.New("role is required")
	}

	var toolCallsJSON any = nil
	if len(msg.ToolCalls) > 0 {
		b, err := json.Marshal(msg.ToolCalls)
		if err != nil {
			return Message{}, err
		}
		toolCallsJSON = string(b)
	}

	var referencedArtifactIDs any = nil
	if len(msg.ReferencedArtifactIDs) > 0 {
		referencedArtifactIDs = dedupeStrings(msg.ReferencedArtifactIDs)
		raw, err := json.Marshal(referencedArtifactIDs)
		if err != nil {
			return Message{}, err
		}
		referencedArtifactIDs = string(raw)
	}

	var referencedMessageIDs any = nil
	if len(msg.ReferencedMessageIDs) > 0 {
		referencedMessageIDs = dedupeStrings(msg.ReferencedMessageIDs)
		raw, err := json.Marshal(referencedMessageIDs)
		if err != nil {
			return Message{}, err
		}
		referencedMessageIDs = string(raw)
	}

	var referencedThreadIDs any = nil
	if len(msg.ReferencedThreadIDs) > 0 {
		referencedThreadIDs = dedupeStrings(msg.ReferencedThreadIDs)
		raw, err := json.Marshal(referencedThreadIDs)
		if err != nil {
			return Message{}, err
		}
		referencedThreadIDs = string(raw)
	}

	var contextPlan any = nil
	if len(msg.ContextPlan) > 0 {
		contextPlan = string(msg.ContextPlan)
	}
	var providerPayload any = nil
	if len(msg.ProviderPayload) > 0 {
		providerPayload = string(msg.ProviderPayload)
	}

	contextSummary := strings.TrimSpace(msg.ContextSummary)
	var hiddenContext any = nil
	if msg.HiddenContext != nil && strings.TrimSpace(*msg.HiddenContext) != "" {
		h := strings.TrimSpace(*msg.HiddenContext)
		hiddenContext = h
	}

	toolCallID := sql.NullString{}
	if msg.ToolCallID != nil && strings.TrimSpace(*msg.ToolCallID) != "" {
		toolCallID = sql.NullString{String: strings.TrimSpace(*msg.ToolCallID), Valid: true}
	}
	name := sql.NullString{}
	if msg.Name != nil && strings.TrimSpace(*msg.Name) != "" {
		name = sql.NullString{String: strings.TrimSpace(*msg.Name), Valid: true}
	}

	thinking := sql.NullString{}
	if msg.Thinking != nil {
		thinking = sql.NullString{String: *msg.Thinking, Valid: true}
	}

	uiHidden := 0
	if msg.UIHidden {
		uiHidden = 1
	}
	thinkingComplete := 0
	if msg.ThinkingComplete {
		thinkingComplete = 1
	}

	var runIDNull sql.NullString
	if runID != nil && strings.TrimSpace(*runID) != "" {
		runIDNull = sql.NullString{String: strings.TrimSpace(*runID), Valid: true}
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO messages (id, thread_id, run_id, seq, role, content, provider_payload, thinking, thinking_complete, tool_calls, tool_call_id, name, ui_hidden,
		referenced_artifact_ids, referenced_message_ids, referenced_thread_ids, context_summary, context_plan, hidden_context, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, threadID, runIDNull, seq, string(role), msg.Content, providerPayload, thinking, thinkingComplete, toolCallsJSON, toolCallID, name, uiHidden,
		referencedArtifactIDs, referencedMessageIDs, referencedThreadIDs, contextSummary, contextPlan, hiddenContext, now, now)
	if err != nil {
		return Message{}, err
	}

	if err := tx.Commit(); err != nil {
		return Message{}, err
	}

	msg.ID = id
	msg.ThreadID = threadID
	msg.RunID = runID
	msg.Seq = seq
	msg.CreatedAt = now
	msg.UpdatedAt = now
	return msg, nil
}

func (s *MySQLStore) UpdateMessage(ctx context.Context, messageID string, fields map[string]any) error {
	id := strings.TrimSpace(messageID)
	if id == "" {
		return errors.New("messageId is required")
	}
	if len(fields) == 0 {
		return nil
	}
	set := []string{"updated_at=?"}
	args := []any{nowUTC()}
	for k, v := range fields {
		switch k {
		case "referenced_artifact_ids", "referenced_message_ids", "referenced_thread_ids":
			if ids, ok := anyStringSlice(v); ok {
				ids = dedupeStrings(ids)
				if len(ids) == 0 {
					v = nil
				} else {
					raw, err := json.Marshal(ids)
					if err != nil {
						return err
					}
					v = string(raw)
				}
			}
		}
		set = append(set, k+"=?")
		args = append(args, v)
	}
	args = append(args, id)
	q := fmt.Sprintf("UPDATE messages SET %s WHERE id=?", strings.Join(set, ", "))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func (s *MySQLStore) ListMessages(ctx context.Context, p ListMessagesParams) ([]Message, error) {
	id := strings.TrimSpace(p.ThreadID)
	if id == "" {
		return nil, errors.New("threadId is required")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	afterSeq := p.AfterSeq
	beforeSeq := p.BeforeSeq
	orderBy := strings.TrimSpace(p.OrderBy)
	if orderBy == "" {
		orderBy = "seq_desc"
	}

	where := []string{"thread_id=?"}
	args := []any{id}

	if !p.IncludeHidden {
		where = append(where, "ui_hidden=0")
	}
	if afterSeq != nil {
		where = append(where, "seq > ?")
		args = append(args, *afterSeq)
	}
	if beforeSeq != nil {
		where = append(where, "seq < ?")
		args = append(args, *beforeSeq)
	}
	if p.BeforeTS != nil {
		where = append(where, "created_at < ?")
		args = append(args, p.BeforeTS.UTC())
	}
	if p.AfterTS != nil {
		where = append(where, "created_at > ?")
		args = append(args, p.AfterTS.UTC())
	}
	if len(p.MessageIDs) > 0 {
		ids := dedupeStrings(p.MessageIDs)
		if len(ids) > 0 {
			placeholders := make([]string, 0, len(ids))
			for _, id := range ids {
				placeholders = append(placeholders, "?")
				args = append(args, id)
			}
			where = append(where, "id IN ("+strings.Join(placeholders, ", ")+")")
		}
	}
	if len(p.ArtifactIDs) > 0 {
		artifactIDs := dedupeStrings(p.ArtifactIDs)
		if len(artifactIDs) > 0 {
			artPlaceholders := make([]string, 0, len(artifactIDs))
			for _, id := range artifactIDs {
				artPlaceholders = append(artPlaceholders, "CAST(? AS JSON)")
				args = append(args, `"`+id+`"`)
			}
			where = append(where, "referenced_artifact_ids IS NOT NULL")
			where = append(where, "JSON_OVERLAPS(referenced_artifact_ids, JSON_ARRAY("+strings.Join(artPlaceholders, ", ")+"))")
		}
	}

	var orderExpr string
	switch orderBy {
	case "seq_asc":
		orderExpr = "seq ASC"
	case "updated_at_asc":
		orderExpr = "created_at ASC"
	case "updated_at_desc":
		orderExpr = "created_at DESC"
	default:
		orderExpr = "seq DESC"
	}
	if orderExpr == "" {
		orderExpr = "seq DESC"
	}

	whereClause := strings.Join(where, " AND ")
	args = append(args, limit)
	q := fmt.Sprintf(`SELECT id, thread_id, run_id, seq, role, content, provider_payload, thinking, thinking_complete, tool_calls, tool_call_id, name, ui_hidden,
			referenced_artifact_ids, referenced_message_ids, referenced_thread_ids, context_summary, context_plan, hidden_context, created_at, updated_at
			FROM messages WHERE %s ORDER BY %s, id DESC LIMIT ?`, whereClause, orderExpr)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := []Message{}
	for rows.Next() {
		var m Message
		var run sql.NullString
		var content sql.NullString
		var providerPayload sql.NullString
		var thinking sql.NullString
		var thinkingComplete int
		var toolCalls sql.NullString
		var toolCallID sql.NullString
		var name sql.NullString
		var referencedArtifactIDs sql.NullString
		var referencedMessageIDs sql.NullString
		var referencedThreadIDs sql.NullString
		var contextSummary sql.NullString
		var contextPlan sql.NullString
		var hiddenContext sql.NullString
		var uiHidden int
		if err := rows.Scan(&m.ID, &m.ThreadID, &run, &m.Seq, &m.Role, &content, &providerPayload, &thinking, &thinkingComplete, &toolCalls, &toolCallID, &name, &uiHidden,
			&referencedArtifactIDs, &referencedMessageIDs, &referencedThreadIDs, &contextSummary, &contextPlan, &hiddenContext, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		if run.Valid {
			rid := run.String
			m.RunID = &rid
		}
		if content.Valid {
			c := content.String
			m.Content = &c
		} else {
			m.Content = nil
		}
		if providerPayload.Valid {
			m.ProviderPayload = []byte(providerPayload.String)
		}
		if thinking.Valid {
			t := thinking.String
			m.Thinking = &t
		}
		m.ThinkingComplete = thinkingComplete != 0
		if toolCalls.Valid {
			var calls []types.ToolCall
			_ = json.Unmarshal([]byte(toolCalls.String), &calls)
			m.ToolCalls = calls
		}
		if toolCallID.Valid {
			v := toolCallID.String
			m.ToolCallID = &v
		}
		if name.Valid {
			v := name.String
			m.Name = &v
		}
		m.ReferencedArtifactIDs = splitJSONArrayOfStrings(referencedArtifactIDs)
		m.ReferencedMessageIDs = splitJSONArrayOfStrings(referencedMessageIDs)
		m.ReferencedThreadIDs = splitJSONArrayOfStrings(referencedThreadIDs)
		if contextSummary.Valid {
			m.ContextSummary = strings.TrimSpace(contextSummary.String)
		}
		if contextPlan.Valid {
			m.ContextPlan = []byte(contextPlan.String)
		}
		if hiddenContext.Valid {
			h := strings.TrimSpace(hiddenContext.String)
			if h != "" {
				m.HiddenContext = &h
			}
		}
		m.UIHidden = uiHidden != 0
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// We fetched newest-first; return ascending for UI.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *MySQLStore) InsertArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	if strings.TrimSpace(a.ID) == "" {
		a.ID = types.NewUUID()
	}
	if strings.TrimSpace(a.ThreadID) == "" {
		return Artifact{}, errors.New("threadId is required")
	}
	if strings.TrimSpace(a.Type) == "" {
		return Artifact{}, errors.New("artifact type is required")
	}
	if strings.TrimSpace(a.Title) == "" {
		a.Title = "Artifact"
	}
	if strings.TrimSpace(a.Storage) == "" {
		a.Storage = "mysql"
	}
	now := nowUTC()

	var metadataJSON any = nil
	if a.Metadata != nil {
		b, err := json.Marshal(a.Metadata)
		if err != nil {
			return Artifact{}, err
		}
		metadataJSON = string(b)
	}

	_, err := s.db.ExecContext(ctx, `INSERT INTO artifacts (id, thread_id, message_id, type, title, language, tool_name, tool_call_id, ok,
		content_storage, content_text, content_path, byte_size, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.ThreadID, a.MessageID, a.Type, a.Title, a.Language, a.ToolName, a.ToolCallID, a.OK,
		a.Storage, a.ContentText, a.ContentPath, a.ByteSize, metadataJSON, now)
	if err != nil {
		if isDuplicateKeyError(err) {
			return Artifact{}, ErrConflict
		}
		return Artifact{}, err
	}
	a.CreatedAt = now
	return a, nil
}

func (s *MySQLStore) ReplaceArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return Artifact{}, errors.New("artifactId is required")
	}
	if strings.TrimSpace(a.ThreadID) == "" {
		return Artifact{}, errors.New("threadId is required")
	}
	if strings.TrimSpace(a.Type) == "" {
		return Artifact{}, errors.New("artifact type is required")
	}
	if strings.TrimSpace(a.Title) == "" {
		a.Title = "Artifact"
	}
	if strings.TrimSpace(a.Storage) == "" {
		a.Storage = "mysql"
	}

	var metadataJSON any = nil
	if a.Metadata != nil {
		b, err := json.Marshal(a.Metadata)
		if err != nil {
			return Artifact{}, err
		}
		metadataJSON = string(b)
	}

	res, err := s.db.ExecContext(ctx, `UPDATE artifacts SET message_id = ?, type = ?, title = ?, language = ?, tool_name = ?,
		tool_call_id = ?, ok = ?, content_storage = ?, content_text = ?, content_path = ?, byte_size = ?, metadata = ?
		WHERE id = ? AND thread_id = ?`,
		a.MessageID, a.Type, a.Title, a.Language, a.ToolName, a.ToolCallID, a.OK,
		a.Storage, a.ContentText, a.ContentPath, a.ByteSize, metadataJSON, id, a.ThreadID)
	if err != nil {
		return Artifact{}, err
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		// Either the artifact does not exist or it belongs to another thread;
		// distinguish so callers can treat missing artifacts as ErrNotFound.
		existing, getErr := s.GetArtifact(ctx, id)
		if getErr != nil {
			return Artifact{}, getErr
		}
		if existing.ThreadID != a.ThreadID {
			return Artifact{}, errors.New("artifact thread mismatch")
		}
	}
	return s.GetArtifact(ctx, id)
}

func (s *MySQLStore) GetArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return Artifact{}, errors.New("artifactId is required")
	}
	var a Artifact
	var messageID sql.NullString
	var lang sql.NullString
	var toolName sql.NullString
	var toolCallID sql.NullString
	var ok sql.NullBool
	var contentText sql.NullString
	var contentPath sql.NullString
	var metadata sql.NullString
	row := s.db.QueryRowContext(ctx, `SELECT id, thread_id, message_id, type, title, language, tool_name, tool_call_id, ok,
		content_storage, content_text, content_path, byte_size, metadata, created_at
		FROM artifacts WHERE id=?`, id)
	if err := row.Scan(&a.ID, &a.ThreadID, &messageID, &a.Type, &a.Title, &lang, &toolName, &toolCallID, &ok,
		&a.Storage, &contentText, &contentPath, &a.ByteSize, &metadata, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Artifact{}, ErrNotFound
		}
		return Artifact{}, err
	}
	if messageID.Valid {
		v := messageID.String
		a.MessageID = &v
	}
	if lang.Valid {
		v := lang.String
		a.Language = &v
	}
	if toolName.Valid {
		v := toolName.String
		a.ToolName = &v
	}
	if toolCallID.Valid {
		v := toolCallID.String
		a.ToolCallID = &v
	}
	if ok.Valid {
		v := ok.Bool
		a.OK = &v
	}
	if contentText.Valid {
		v := contentText.String
		a.ContentText = &v
	}
	if contentPath.Valid {
		v := contentPath.String
		a.ContentPath = &v
	}
	if metadata.Valid {
		var m map[string]any
		_ = json.Unmarshal([]byte(metadata.String), &m)
		a.Metadata = m
	}
	return a, nil
}

func (s *MySQLStore) DeleteArtifact(ctx context.Context, artifactID string) error {
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return errors.New("artifactId is required")
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM artifacts WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MySQLStore) ListArtifacts(ctx context.Context, threadID string, limit int) ([]Artifact, error) {
	return s.listArtifacts(ctx, threadID, limit, "")
}

func (s *MySQLStore) ListFileArtifacts(ctx context.Context, threadID string, limit int) ([]Artifact, error) {
	return s.listArtifacts(ctx, threadID, limit, "file")
}

func (s *MySQLStore) ListArtifactsPage(ctx context.Context, p ListArtifactsPageParams) (ArtifactPage, error) {
	tid := strings.TrimSpace(p.ThreadID)
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, thread_id, message_id, type, title, language, tool_name, tool_call_id, ok,
		content_storage, content_text, content_path, byte_size, metadata, created_at
		FROM artifacts`
	args := []any{}
	conds := []string{}
	if tid != "" {
		conds = append(conds, "thread_id=?")
		args = append(args, tid)
	}
	if typ := strings.TrimSpace(p.Type); typ != "" {
		conds = append(conds, "type=?")
		args = append(args, typ)
	}
	if p.BeforeCreatedAt != nil {
		beforeID := strings.TrimSpace(p.BeforeID)
		conds = append(conds, "(created_at < ? OR (created_at = ? AND id < ?))")
		args = append(args, p.BeforeCreatedAt.UTC(), p.BeforeCreatedAt.UTC(), beforeID)
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return ArtifactPage{}, err
	}
	defer rows.Close()
	out := make([]Artifact, 0, limit+1)
	for rows.Next() {
		var a Artifact
		var messageID sql.NullString
		var lang sql.NullString
		var toolName sql.NullString
		var toolCallID sql.NullString
		var ok sql.NullBool
		var contentText sql.NullString
		var contentPath sql.NullString
		var metadata sql.NullString
		if err := rows.Scan(&a.ID, &a.ThreadID, &messageID, &a.Type, &a.Title, &lang, &toolName, &toolCallID, &ok,
			&a.Storage, &contentText, &contentPath, &a.ByteSize, &metadata, &a.CreatedAt); err != nil {
			return ArtifactPage{}, err
		}
		if messageID.Valid {
			v := messageID.String
			a.MessageID = &v
		}
		if lang.Valid {
			v := lang.String
			a.Language = &v
		}
		if toolName.Valid {
			v := toolName.String
			a.ToolName = &v
		}
		if toolCallID.Valid {
			v := toolCallID.String
			a.ToolCallID = &v
		}
		if ok.Valid {
			v := ok.Bool
			a.OK = &v
		}
		if contentText.Valid {
			v := contentText.String
			a.ContentText = &v
		}
		if contentPath.Valid {
			v := contentPath.String
			a.ContentPath = &v
		}
		if metadata.Valid {
			var m map[string]any
			_ = json.Unmarshal([]byte(metadata.String), &m)
			a.Metadata = m
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return ArtifactPage{}, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return ArtifactPage{Artifacts: out, HasMore: hasMore}, nil
}

func (s *MySQLStore) listArtifacts(ctx context.Context, threadID string, limit int, typeFilter string) ([]Artifact, error) {
	tid := strings.TrimSpace(threadID)
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT id, thread_id, message_id, type, title, language, tool_name, tool_call_id, ok,
		content_storage, content_text, content_path, byte_size, metadata, created_at
		FROM artifacts`
	args := []any{}
	conds := []string{}
	if tid != "" {
		conds = append(conds, "thread_id=?")
		args = append(args, tid)
	}
	if strings.TrimSpace(typeFilter) != "" {
		conds = append(conds, "type=?")
		args = append(args, strings.TrimSpace(typeFilter))
	}
	if len(conds) > 0 {
		query += ` WHERE ` + strings.Join(conds, " AND ")
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Artifact, 0, limit)
	for rows.Next() {
		var a Artifact
		var messageID sql.NullString
		var lang sql.NullString
		var toolName sql.NullString
		var toolCallID sql.NullString
		var ok sql.NullBool
		var contentText sql.NullString
		var contentPath sql.NullString
		var metadata sql.NullString
		if err := rows.Scan(&a.ID, &a.ThreadID, &messageID, &a.Type, &a.Title, &lang, &toolName, &toolCallID, &ok,
			&a.Storage, &contentText, &contentPath, &a.ByteSize, &metadata, &a.CreatedAt); err != nil {
			return nil, err
		}
		if messageID.Valid {
			v := messageID.String
			a.MessageID = &v
		}
		if lang.Valid {
			v := lang.String
			a.Language = &v
		}
		if toolName.Valid {
			v := toolName.String
			a.ToolName = &v
		}
		if toolCallID.Valid {
			v := toolCallID.String
			a.ToolCallID = &v
		}
		if ok.Valid {
			v := ok.Bool
			a.OK = &v
		}
		if contentText.Valid {
			v := contentText.String
			a.ContentText = &v
		}
		if contentPath.Valid {
			v := contentPath.String
			a.ContentPath = &v
		}
		if metadata.Valid {
			var m map[string]any
			_ = json.Unmarshal([]byte(metadata.String), &m)
			a.Metadata = m
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func (s *MySQLStore) CreateProject(ctx context.Context, name string, rootRelPath string) (Project, error) {
	id := types.NewUUID()
	now := nowUTC()
	nm := strings.TrimSpace(name)
	if nm == "" {
		nm = "Project"
	}
	rp := strings.TrimSpace(rootRelPath)
	if rp == "" {
		return Project{}, errors.New("rootRelPath is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO projects (id, name, root_rel_path, file_count, total_bytes, created_at, updated_at)
		VALUES (?, ?, ?, 0, 0, ?, ?)`, id, nm, rp, now, now)
	if err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: nm, RootRelPath: rp, FileCount: 0, TotalBytes: 0, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *MySQLStore) ListProjects(ctx context.Context, limit int) ([]Project, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, root_rel_path, file_count, total_bytes, created_at, updated_at
		FROM projects ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.RootRelPath, &p.FileCount, &p.TotalBytes, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *MySQLStore) GetProject(ctx context.Context, projectID string) (Project, error) {
	id := strings.TrimSpace(projectID)
	if id == "" {
		return Project{}, errors.New("projectId is required")
	}
	var p Project
	row := s.db.QueryRowContext(ctx, `SELECT id, name, root_rel_path, file_count, total_bytes, created_at, updated_at FROM projects WHERE id=?`, id)
	if err := row.Scan(&p.ID, &p.Name, &p.RootRelPath, &p.FileCount, &p.TotalBytes, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, ErrNotFound
		}
		return Project{}, err
	}
	return p, nil
}

func (s *MySQLStore) UpsertProjectFile(ctx context.Context, projectID string, path string, byteSize int64, sha256 []byte) error {
	pid := strings.TrimSpace(projectID)
	p := strings.TrimSpace(path)
	if pid == "" || p == "" {
		return errors.New("projectId and path are required")
	}
	if byteSize < 0 {
		byteSize = 0
	}
	now := nowUTC()
	var sha any = nil
	if len(sha256) == 32 {
		sha = sha256
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_files (project_id, path, byte_size, sha256, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE byte_size=VALUES(byte_size), sha256=VALUES(sha256), updated_at=VALUES(updated_at)`,
		pid, p, byteSize, sha, now)
	return err
}

func (s *MySQLStore) ReplaceProjectFiles(ctx context.Context, projectID string, files []ProjectFileStat) (Project, error) {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return Project{}, errors.New("projectId is required")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM project_files WHERE project_id=?`, pid); err != nil {
		return Project{}, err
	}

	now := nowUTC()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO project_files (project_id, path, byte_size, sha256, updated_at)
		VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return Project{}, err
	}
	defer stmt.Close()

	seen := map[string]struct{}{}
	total := int64(0)
	count := 0
	for _, f := range files {
		pth := strings.TrimSpace(f.Path)
		if pth == "" {
			continue
		}
		if _, ok := seen[pth]; ok {
			continue
		}
		seen[pth] = struct{}{}
		size := f.ByteSize
		if size < 0 {
			size = 0
		}
		var sha any = nil
		if len(f.SHA256) == 32 {
			sha = f.SHA256
		}
		if _, err := stmt.ExecContext(ctx, pid, pth, size, sha, now); err != nil {
			return Project{}, err
		}
		total += size
		count++
	}

	res, err := tx.ExecContext(ctx, `UPDATE projects SET file_count=?, total_bytes=?, updated_at=? WHERE id=?`, count, total, now, pid)
	if err != nil {
		return Project{}, err
	}
	if affected, err := res.RowsAffected(); err == nil && affected == 0 {
		return Project{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, pid)
}

func (s *MySQLStore) FinalizeProject(ctx context.Context, projectID string) (Project, error) {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return Project{}, errors.New("projectId is required")
	}
	var cnt int
	var total sql.NullInt64
	row := s.db.QueryRowContext(ctx, `SELECT COUNT(*), SUM(byte_size) FROM project_files WHERE project_id=?`, pid)
	if err := row.Scan(&cnt, &total); err != nil {
		return Project{}, err
	}
	totalBytes := int64(0)
	if total.Valid {
		totalBytes = total.Int64
	}
	now := nowUTC()
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET file_count=?, total_bytes=?, updated_at=? WHERE id=?`, cnt, totalBytes, now, pid)
	if err != nil {
		return Project{}, err
	}
	return s.GetProject(ctx, pid)
}

func (s *MySQLStore) EnqueueJob(ctx context.Context, kind string, threadID string, runID *string) (Job, error) {
	id := types.NewUUID()
	now := nowUTC()
	k := strings.TrimSpace(kind)
	if k == "" {
		return Job{}, errors.New("kind is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs (id, kind, thread_id, run_id, status, attempts, not_before, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'queued', 0, ?, NULL, ?, ?)`, id, k, threadID, runID, now, now, now)
	if err != nil {
		return Job{}, err
	}
	return Job{ID: id, Kind: k, ThreadID: threadID, RunID: runID, Status: "queued", Attempts: 0, NotBefore: now, CreatedAt: now, UpdatedAt: now}, nil
}

func (s *MySQLStore) ClaimNextJob(ctx context.Context, now time.Time) (*Job, error) {
	// MySQL lacks SKIP LOCKED prior to 8.0; assume 8.0+ and use it.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `SELECT id, kind, thread_id, run_id, status, attempts, not_before, last_error, created_at, updated_at
		FROM jobs WHERE status='queued' AND not_before<=? ORDER BY created_at ASC LIMIT 1 FOR UPDATE`, now)
	var j Job
	var run sql.NullString
	var lastErr sql.NullString
	if err := row.Scan(&j.ID, &j.Kind, &j.ThreadID, &run, &j.Status, &j.Attempts, &j.NotBefore, &lastErr, &j.CreatedAt, &j.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if run.Valid {
		v := run.String
		j.RunID = &v
	}
	if lastErr.Valid {
		v := lastErr.String
		j.LastError = &v
	}

	j.Attempts++
	upd := nowUTC()
	_, err = tx.ExecContext(ctx, `UPDATE jobs SET status='running', attempts=?, updated_at=? WHERE id=?`, j.Attempts, upd, j.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.Status = "running"
	j.UpdatedAt = upd
	return &j, nil
}

func (s *MySQLStore) CompleteJob(ctx context.Context, jobID string) error {
	id := strings.TrimSpace(jobID)
	if id == "" {
		return errors.New("jobId is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='completed', updated_at=? WHERE id=?`, nowUTC(), id)
	return err
}

func (s *MySQLStore) FailJob(ctx context.Context, jobID string, errMsg string, notBefore time.Time) error {
	id := strings.TrimSpace(jobID)
	if id == "" {
		return errors.New("jobId is required")
	}
	msg := strings.TrimSpace(errMsg)
	if msg == "" {
		msg = "unknown error"
	}
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status='queued', last_error=?, not_before=?, updated_at=? WHERE id=?`, msg, notBefore.UTC(), nowUTC(), id)
	return err
}

func splitJSONArrayOfStrings(raw sql.NullString) []string {
	if !raw.Valid {
		return nil
	}
	trimmed := strings.TrimSpace(raw.String)
	if trimmed == "" || trimmed == "[]" {
		return nil
	}
	var parsed []string
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil
	}
	return dedupeStrings(parsed)
}

func isDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "1062")
}

type sqlScanner interface {
	Scan(dest ...any) error
}

const runPlanSelectSQL = `SELECT plan_id, thread_id, run_id, target_connection_id, target_model,
	config_fingerprint, requirements_hash, result_status, status, blockers, choices, notices,
	stale_evidence, capability_report, choice_selections, created_at, expires_at, finalized_at
	FROM run_plans`

func scanMySQLRunPlan(scanner sqlScanner) (RunPlan, error) {
	var plan RunPlan
	var runID, fingerprint, selectionsJSON sql.NullString
	var blockersJSON, choicesJSON, noticesJSON, capabilityJSON string
	var finalizedAt sql.NullTime
	if err := scanner.Scan(
		&plan.ID, &plan.ThreadID, &runID, &plan.Target.ConnectionID, &plan.Target.Model,
		&fingerprint, &plan.RequirementsHash, &plan.ResultStatus, &plan.Status,
		&blockersJSON, &choicesJSON, &noticesJSON, &plan.StaleEvidence, &capabilityJSON,
		&selectionsJSON, &plan.CreatedAt, &plan.ExpiresAt, &finalizedAt,
	); err != nil {
		return RunPlan{}, err
	}
	if runID.Valid && strings.TrimSpace(runID.String) != "" {
		v := strings.TrimSpace(runID.String)
		plan.RunID = &v
	}
	if fingerprint.Valid {
		plan.ConfigFingerprint = strings.TrimSpace(fingerprint.String)
	}
	if err := json.Unmarshal([]byte(blockersJSON), &plan.Blockers); err != nil {
		return RunPlan{}, err
	}
	if err := json.Unmarshal([]byte(choicesJSON), &plan.Choices); err != nil {
		return RunPlan{}, err
	}
	if err := json.Unmarshal([]byte(noticesJSON), &plan.Notices); err != nil {
		return RunPlan{}, err
	}
	plan.CapabilityReport = cloneRawJSON([]byte(capabilityJSON))
	if selectionsJSON.Valid && strings.TrimSpace(selectionsJSON.String) != "" && strings.TrimSpace(selectionsJSON.String) != "null" {
		if err := json.Unmarshal([]byte(selectionsJSON.String), &plan.Selections); err != nil {
			return RunPlan{}, err
		}
	}
	plan.CreatedAt = plan.CreatedAt.UTC()
	plan.ExpiresAt = plan.ExpiresAt.UTC()
	if finalizedAt.Valid {
		v := finalizedAt.Time.UTC()
		plan.FinalizedAt = &v
	}
	if err := validateRunPlan(plan); err != nil {
		return RunPlan{}, err
	}
	return plan, nil
}

func applyThreadRuntimeFields(thread *Thread, connection, model sql.NullString, policiesJSON []byte) error {
	if thread == nil {
		return nil
	}
	if connection.Valid && model.Valid && strings.TrimSpace(connection.String) != "" && strings.TrimSpace(model.String) != "" {
		thread.LastTarget = &RunTarget{ConnectionID: strings.TrimSpace(connection.String), Model: strings.TrimSpace(model.String)}
	}
	policies, err := decodeAttachmentPolicies(policiesJSON)
	if err != nil {
		return fmt.Errorf("decode thread attachment policies: %w", err)
	}
	thread.AttachmentPolicies = policies
	return nil
}

func decodeAttachmentPolicies(raw []byte) ([]AttachmentPolicy, error) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "" || strings.TrimSpace(string(raw)) == "null" {
		return nil, nil
	}
	var policies []AttachmentPolicy
	if err := json.Unmarshal(raw, &policies); err != nil {
		return nil, err
	}
	return policies, nil
}

func appendPolicyIdempotent(existing []AttachmentPolicy, policy AttachmentPolicy) []AttachmentPolicy {
	for _, current := range existing {
		if strings.EqualFold(strings.TrimSpace(current.Class), strings.TrimSpace(policy.Class)) &&
			strings.EqualFold(strings.TrimSpace(current.Action), strings.TrimSpace(policy.Action)) &&
			strings.EqualFold(strings.TrimSpace(current.APISurface), strings.TrimSpace(policy.APISurface)) {
			return existing
		}
	}
	return append(existing, policy)
}

func nullIfBlank(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func approvalRequestSelectSQL() string {
	return `SELECT id, thread_id, run_id, tool_call_id, tool, category, operation, subject, summary, preview, options_json, status, decision, created_at, expires_at, updated_at FROM approval_requests`
}

func localSearchJobSelectSQL() string {
	return `SELECT id, thread_id, run_id, tool_call_id, status, query, grant_id, local_root, local_kind, created_at, updated_at, completed_at, files_count, error, result FROM local_search_jobs`
}

func scanRunEvent(scanner sqlScanner) (RunEvent, error) {
	var ev RunEvent
	var data []byte
	if err := scanner.Scan(&ev.RunID, &ev.ThreadID, &ev.Seq, &ev.Type, &data, &ev.CreatedAt); err != nil {
		return RunEvent{}, err
	}
	ev.Data = cloneRawJSON(data)
	prepared, err := prepareRunEvent(ev)
	if err != nil {
		return RunEvent{}, err
	}
	return prepared, nil
}

func scanApprovalRequest(scanner sqlScanner) (ApprovalRequest, error) {
	var approval ApprovalRequest
	var toolCallID sql.NullString
	var operation sql.NullString
	var subjectJSON string
	var previewJSON sql.NullString
	var optionsJSON sql.NullString
	var decisionJSON sql.NullString
	if err := scanner.Scan(&approval.ID, &approval.ThreadID, &approval.RunID, &toolCallID, &approval.Tool, &approval.Category, &operation,
		&subjectJSON, &approval.Summary, &previewJSON, &optionsJSON, &approval.Status, &decisionJSON,
		&approval.CreatedAt, &approval.ExpiresAt, &approval.UpdatedAt); err != nil {
		return ApprovalRequest{}, err
	}
	if toolCallID.Valid {
		approval.ToolCallID = strings.TrimSpace(toolCallID.String)
	}
	if operation.Valid {
		approval.Operation = strings.TrimSpace(operation.String)
	}
	if strings.TrimSpace(subjectJSON) != "" {
		if err := json.Unmarshal([]byte(subjectJSON), &approval.Subject); err != nil {
			return ApprovalRequest{}, err
		}
	}
	if previewJSON.Valid && strings.TrimSpace(previewJSON.String) != "" && strings.TrimSpace(previewJSON.String) != "null" {
		var preview map[string]any
		if err := json.Unmarshal([]byte(previewJSON.String), &preview); err != nil {
			return ApprovalRequest{}, err
		}
		approval.Preview = preview
	}
	if optionsJSON.Valid && strings.TrimSpace(optionsJSON.String) != "" && strings.TrimSpace(optionsJSON.String) != "null" {
		var options []string
		if err := json.Unmarshal([]byte(optionsJSON.String), &options); err != nil {
			return ApprovalRequest{}, err
		}
		approval.Options = options
	}
	if decisionJSON.Valid && strings.TrimSpace(decisionJSON.String) != "" && strings.TrimSpace(decisionJSON.String) != "null" {
		var decision ApprovalDecision
		if err := json.Unmarshal([]byte(decisionJSON.String), &decision); err != nil {
			return ApprovalRequest{}, err
		}
		approval.Decision = &decision
	}
	prepared, err := prepareApprovalRequest(approval)
	if err != nil {
		return ApprovalRequest{}, err
	}
	return prepared, nil
}

func scanLocalSearchJob(scanner sqlScanner) (LocalSearchJob, error) {
	var job LocalSearchJob
	var toolCallID sql.NullString
	var grantID sql.NullString
	var localRoot sql.NullString
	var localKind sql.NullString
	var completedAt sql.NullTime
	var errText sql.NullString
	var resultJSON sql.NullString
	if err := scanner.Scan(&job.ID, &job.ThreadID, &job.RunID, &toolCallID, &job.Status, &job.Query,
		&grantID, &localRoot, &localKind, &job.CreatedAt, &job.UpdatedAt, &completedAt, &job.FilesCount, &errText, &resultJSON); err != nil {
		return LocalSearchJob{}, err
	}
	if toolCallID.Valid {
		job.ToolCallID = strings.TrimSpace(toolCallID.String)
	}
	if grantID.Valid {
		job.GrantID = strings.TrimSpace(grantID.String)
	}
	if localRoot.Valid {
		job.LocalRoot = strings.TrimSpace(localRoot.String)
	}
	if localKind.Valid {
		job.LocalKind = strings.TrimSpace(localKind.String)
	}
	if completedAt.Valid {
		t := completedAt.Time.UTC()
		job.CompletedAt = &t
	}
	if errText.Valid {
		job.Error = strings.TrimSpace(errText.String)
	}
	if resultJSON.Valid && strings.TrimSpace(resultJSON.String) != "" && strings.TrimSpace(resultJSON.String) != "null" {
		var result map[string]any
		if err := json.Unmarshal([]byte(resultJSON.String), &result); err != nil {
			return LocalSearchJob{}, err
		}
		job.Result = result
	}
	prepared, err := prepareLocalSearchJob(job)
	if err != nil {
		return LocalSearchJob{}, err
	}
	return prepared, nil
}

func scanContextCheckpoint(scanner sqlScanner) (ContextCheckpoint, error) {
	var cp ContextCheckpoint
	var runID sql.NullString
	var replacementHistory string
	var summary sql.NullString
	var summaryIDs sql.NullString
	var artifactIDs sql.NullString
	var plan sql.NullString
	if err := scanner.Scan(&cp.ID, &cp.ThreadID, &runID, &cp.SourceMessageSeq, &cp.SourceMessageID,
		&replacementHistory, &summary, &summaryIDs, &artifactIDs, &cp.TokenEstimate, &cp.CharsPerToken,
		&cp.Model, &cp.ProviderID, &plan, &cp.CreatedAt, &cp.UpdatedAt); err != nil {
		return ContextCheckpoint{}, err
	}
	if runID.Valid {
		v := strings.TrimSpace(runID.String)
		if v != "" {
			cp.RunID = &v
		}
	}
	if summary.Valid {
		cp.Summary = summary.String
	}
	if strings.TrimSpace(replacementHistory) != "" {
		var msgs []types.ChatMessage
		if err := json.Unmarshal([]byte(replacementHistory), &msgs); err != nil {
			return ContextCheckpoint{}, err
		}
		cp.ReplacementHistory = msgs
	}
	cp.SummaryMessageIDs = splitJSONArrayOfStrings(summaryIDs)
	cp.ReferencedArtifactIDs = splitJSONArrayOfStrings(artifactIDs)
	if plan.Valid {
		cp.PlanJSON = cloneRawJSON([]byte(plan.String))
	}
	prepared, err := prepareContextCheckpoint(cp)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	return prepared, nil
}
