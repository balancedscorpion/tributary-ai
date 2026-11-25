package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
)

// QueryMaterializer is the store-backed durable result sink used directly by
// jobs.Registry. It deliberately returns as a concrete type because the
// registry requires both Materializer and DrainCommitter for Trino.
type QueryMaterializer struct {
	repository LongHorizonRepository
}

var (
	_ jobs.Materializer         = (*QueryMaterializer)(nil)
	_ jobs.DrainCommitter       = (*QueryMaterializer)(nil)
	_ jobs.FencedDrainCommitter = (*QueryMaterializer)(nil)
	_ jobs.TerminalResultLoader = (*QueryMaterializer)(nil)
)

func NewQueryMaterializer(repository LongHorizonRepository) *QueryMaterializer {
	return &QueryMaterializer{repository: repository}
}

func (m *QueryMaterializer) AppendChunk(ctx context.Context, jobID string, owner jobs.JobOwner, page jobs.ResultPage) error {
	record, err := m.ownedJob(ctx, jobID, owner)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 4; attempt++ {
		chunks, err := m.repository.ListQueryResultChunks(ctx, record.ID, 0, 1_000_000)
		if err != nil {
			return err
		}
		var nextSequence int64 = 1
		for _, existing := range chunks {
			if existing.Sequence >= nextSequence {
				nextSequence = existing.Sequence + 1
			}
			candidate := queryChunkForPage(record.ID, existing.Sequence, page)
			if queryResultChunksSemanticallyEqual(existing, candidate) {
				return nil
			}
		}
		candidate := queryChunkForPage(record.ID, nextSequence, page)
		if err := m.repository.AppendQueryResultChunk(ctx, candidate); err == nil {
			return nil
		} else if !errors.Is(err, ErrConflict) {
			return err
		}
		// Another process selected the same sequence. Re-read: either its
		// payload is our idempotent replay or we append at the next sequence.
	}
	return ErrConflict
}

func queryChunkForPage(jobID string, sequence int64, page jobs.ResultPage) QueryResultChunk {
	cursor := jobs.NativeHandle{}
	if page.CursorAfter != nil {
		cursor = *page.CursorAfter
	}
	return QueryResultChunk{
		JobID: jobID, Sequence: sequence, Page: page, CursorAfter: cursor,
		NativePageToken: page.NextPageToken, RowCount: len(page.Rows), ByteCount: page.Bytes,
		Truncated: page.Truncated, CreatedAt: time.Now().UTC(),
	}
}

func (m *QueryMaterializer) Finalize(ctx context.Context, jobID string, owner jobs.JobOwner, result *jobs.QueryResult, summary jobs.MaterializationSummary) error {
	record, err := m.ownedJob(ctx, jobID, owner)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"rows":      summary.Rows,
		"bytes":     summary.Bytes,
		"truncated": summary.Truncated,
		"metadata":  summary.Metadata,
	}
	if result != nil {
		payload["result_metadata"] = result.Metadata
		payload["schema"] = result.Schema
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	_, err = m.repository.AppendQueryJobEvent(ctx, QueryJobEvent{
		JobID: record.ID, Type: "materialization_finalized",
		ObservationKey: "materialization:" + hex.EncodeToString(digest[:]),
		Payload:        raw, CreatedAt: time.Now().UTC(),
	})
	return err
}

func (m *QueryMaterializer) CommitDrainObservation(ctx context.Context, commit jobs.DrainCommit) (int64, error) {
	if m == nil || m.repository == nil {
		return 0, errors.New("query materializer is not configured")
	}
	return m.repository.CommitDrainObservation(ctx, commit)
}

func (m *QueryMaterializer) CommitDrainObservationFenced(ctx context.Context, commit jobs.DrainCommit, holder string) (int64, error) {
	if m == nil || m.repository == nil {
		return 0, errors.New("query materializer is not configured")
	}
	return m.repository.CommitDrainObservationFenced(ctx, commit, holder)
}

func (m *QueryMaterializer) LoadQueryResult(ctx context.Context, jobID string, owner jobs.JobOwner) (*jobs.QueryResult, jobs.MaterializationSummary, error) {
	record, err := m.ownedJob(ctx, jobID, owner)
	if err != nil {
		return nil, jobs.MaterializationSummary{}, err
	}
	if record.Status != jobs.JobStatusCompleted {
		return nil, jobs.MaterializationSummary{}, jobs.ErrNotCompleted
	}
	chunks, err := m.repository.ListQueryResultChunks(ctx, record.ID, 0, 1_000_000)
	if err != nil {
		return nil, jobs.MaterializationSummary{}, err
	}
	result := &jobs.QueryResult{Metadata: map[string]any{}}
	summary := jobs.MaterializationSummary{}
	for _, chunk := range chunks {
		result.Rows = append(result.Rows, chunk.Page.Rows...)
		if len(result.Schema) == 0 && len(chunk.Page.Schema) > 0 {
			result.Schema = append([]map[string]any(nil), chunk.Page.Schema...)
		}
		for key, value := range chunk.Page.Metadata {
			result.Metadata[key] = value
		}
		summary.Rows += chunk.RowCount
		summary.Bytes += chunk.ByteCount
		summary.Truncated = summary.Truncated || chunk.Truncated
	}
	events, err := m.repository.ListQueryJobEvents(ctx, record.ID, 0, 1_000_000)
	if err != nil {
		return nil, jobs.MaterializationSummary{}, err
	}
	for _, event := range events {
		if event.Type != "materialization_finalized" || len(event.Payload) == 0 {
			continue
		}
		var finalized struct {
			Rows           int              `json:"rows"`
			Bytes          int64            `json:"bytes"`
			Truncated      bool             `json:"truncated"`
			Metadata       map[string]any   `json:"metadata"`
			ResultMetadata map[string]any   `json:"result_metadata"`
			Schema         []map[string]any `json:"schema"`
		}
		if err := json.Unmarshal(event.Payload, &finalized); err != nil {
			return nil, jobs.MaterializationSummary{}, err
		}
		if finalized.Rows != summary.Rows || finalized.Bytes != summary.Bytes || finalized.Truncated != summary.Truncated {
			return nil, jobs.MaterializationSummary{}, fmt.Errorf(
				"materialization summary does not match durable chunks: summary=%d/%d/%t chunks=%d/%d/%t",
				finalized.Rows, finalized.Bytes, finalized.Truncated, summary.Rows, summary.Bytes, summary.Truncated,
			)
		}
		summary.Rows = finalized.Rows
		summary.Bytes = finalized.Bytes
		summary.Truncated = finalized.Truncated
		summary.Metadata = finalized.Metadata
		for key, value := range finalized.ResultMetadata {
			result.Metadata[key] = value
		}
		if len(result.Schema) == 0 && len(finalized.Schema) > 0 {
			result.Schema = finalized.Schema
		}
	}
	if len(result.Metadata) == 0 {
		result.Metadata = nil
	}
	return result, summary, nil
}

func (m *QueryMaterializer) ownedJob(ctx context.Context, jobID string, owner jobs.JobOwner) (jobs.JobRecord, error) {
	if m == nil || m.repository == nil {
		return jobs.JobRecord{}, errors.New("query materializer is not configured")
	}
	record, ok, err := m.repository.GetQueryJob(ctx, strings.TrimSpace(jobID))
	if err != nil {
		return jobs.JobRecord{}, err
	}
	if !ok {
		return jobs.JobRecord{}, ErrNotFound
	}
	if strings.TrimSpace(record.Owner.ThreadID) == "" ||
		strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(owner.ThreadID) ||
		strings.TrimSpace(record.Owner.Handle) == "" ||
		strings.TrimSpace(record.Owner.Handle) != strings.TrimSpace(owner.Handle) {
		return jobs.JobRecord{}, jobs.ErrOwnershipMismatch
	}
	return record, nil
}
