package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
)

func TestDrainCommitAccountingIsCumulativeAndTruncationIsSticky(t *testing.T) {
	current := jobs.JobRecord{DrainRowsCommitted: 3, DrainBytesCommitted: 30, DrainTruncated: true}
	record := jobs.JobRecord{DrainRowsCommitted: 5, DrainBytesCommitted: 42, DrainTruncated: true}
	valid := jobs.DrainCommit{
		Page:               jobs.ResultPage{Rows: []map[string]any{{"v": 1}, {"v": 2}}, Bytes: 12},
		DrainRowsCommitted: 5, DrainBytesCommitted: 42, DrainTruncated: true, Record: record,
	}
	if err := validateDrainCommitAccounting(valid, current); err != nil {
		t.Fatalf("valid cumulative accounting: %v", err)
	}
	for _, mutate := range []func(*jobs.DrainCommit){
		func(commit *jobs.DrainCommit) { commit.DrainRowsCommitted--; commit.Record.DrainRowsCommitted-- },
		func(commit *jobs.DrainCommit) { commit.DrainBytesCommitted--; commit.Record.DrainBytesCommitted-- },
		func(commit *jobs.DrainCommit) { commit.DrainTruncated = false; commit.Record.DrainTruncated = false },
		func(commit *jobs.DrainCommit) { commit.Record.DrainRowsCommitted-- },
	} {
		candidate := valid
		mutate(&candidate)
		if err := validateDrainCommitAccounting(candidate, current); err == nil {
			t.Fatalf("invalid accounting accepted: %#v", candidate)
		}
	}
}

func TestQueryMaterializerDurablyDeduplicatesChunksAndFinalization(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	record := testDurableJob(thread, run, time.Date(2026, 7, 12, 17, 0, 0, 0, time.UTC))
	granted, _, err := store.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v err=%v", granted, err)
	}
	materializer := NewQueryMaterializer(store)
	cursor := jobs.NativeHandle{Provider: "bigquery", ID: "native-1", Extra: map[string]string{"page": "2"}}
	page := jobs.ResultPage{
		Rows: []map[string]any{{"value": 1}}, Schema: []map[string]any{{"name": "value"}},
		Bytes: 12, NextPageToken: "page-2", CursorAfter: &cursor,
	}
	if err := materializer.AppendChunk(ctx, record.ID, record.Owner, page); err != nil {
		t.Fatalf("AppendChunk: %v", err)
	}
	if err := materializer.AppendChunk(ctx, record.ID, record.Owner, page); err != nil {
		t.Fatalf("idempotent AppendChunk: %v", err)
	}
	chunks, err := store.ListQueryResultChunks(ctx, record.ID, 0, 10)
	if err != nil || len(chunks) != 1 || chunks[0].Sequence != 1 {
		t.Fatalf("chunks=%#v err=%v, want one durable chunk", chunks, err)
	}
	summary := jobs.MaterializationSummary{Rows: 1, Bytes: 12, Metadata: map[string]any{"source": "test"}}
	result := &jobs.QueryResult{Schema: page.Schema, Metadata: map[string]any{"cache_hit": false}}
	if err := materializer.Finalize(ctx, record.ID, record.Owner, result, summary); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if err := materializer.Finalize(ctx, record.ID, record.Owner, result, summary); err != nil {
		t.Fatalf("idempotent Finalize: %v", err)
	}
	events, err := store.ListQueryJobEvents(ctx, record.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "materialization_finalized" {
		t.Fatalf("events=%#v err=%v, want one durable finalization", events, err)
	}
	terminal, ok, err := store.GetQueryJob(ctx, record.ID)
	if err != nil || !ok {
		t.Fatalf("GetQueryJob before terminalize ok=%v err=%v", ok, err)
	}
	terminal.Status = jobs.JobStatusCompleted
	if _, err := store.UpsertQueryJobCAS(ctx, terminal, terminal.Revision); err != nil {
		t.Fatalf("terminalize durable query job: %v", err)
	}
	loaded, loadedSummary, err := materializer.LoadQueryResult(ctx, record.ID, record.Owner)
	if err != nil || loaded == nil || len(loaded.Rows) != 1 || loaded.Rows[0]["value"] != float64(1) && loaded.Rows[0]["value"] != 1 {
		t.Fatalf("LoadQueryResult result=%#v summary=%#v err=%v", loaded, loadedSummary, err)
	}
	if loadedSummary.Rows != 1 || loadedSummary.Bytes != 12 || loadedSummary.Metadata["source"] != "test" {
		t.Fatalf("LoadQueryResult summary=%#v", loadedSummary)
	}
	badOwner := record.Owner
	badOwner.Handle = "wrong"
	if err := materializer.AppendChunk(ctx, record.ID, badOwner, page); !errors.Is(err, jobs.ErrOwnershipMismatch) {
		t.Fatalf("wrong-owner AppendChunk err=%v, want ErrOwnershipMismatch", err)
	}
	if _, _, err := materializer.LoadQueryResult(ctx, record.ID, badOwner); !errors.Is(err, jobs.ErrOwnershipMismatch) {
		t.Fatalf("wrong-owner LoadQueryResult err=%v, want ErrOwnershipMismatch", err)
	}
}

func TestQueryMaterializerDelegatesAtomicDrainCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	record := testDurableJob(thread, run, time.Date(2026, 7, 12, 18, 0, 0, 0, time.UTC))
	record.ID = "job_materializer_trino"
	record.Provider = "trino"
	record.Status = jobs.JobStatusRunning
	record.Handle = jobs.NativeHandle{Provider: "trino", ID: "query-1", Extra: map[string]string{"next_uri": "one"}}
	granted, revision, err := store.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v err=%v", granted, err)
	}
	record.Revision = revision
	record.DrainSeq = 1
	record.DrainRowsCommitted = 1
	record.DrainBytesCommitted = 9
	next := jobs.NativeHandle{Provider: "trino", ID: "query-1", Extra: map[string]string{"next_uri": "two"}}
	page := jobs.ResultPage{Rows: []map[string]any{{"value": 1}}, Bytes: 9, CursorAfter: &next}
	materializer := NewQueryMaterializer(store)
	newRevision, err := materializer.CommitDrainObservation(ctx, jobs.DrainCommit{
		JobID: record.ID, Owner: record.Owner, Seq: 1, Page: page, Handle: next,
		DrainRowsCommitted: 1, DrainBytesCommitted: 9, Record: record,
	})
	if err != nil || newRevision != revision+1 {
		t.Fatalf("CommitDrainObservation revision=%d err=%v", newRevision, err)
	}
	if replayRevision, err := materializer.CommitDrainObservation(ctx, jobs.DrainCommit{
		JobID: record.ID, Owner: record.Owner, Seq: 1, Page: page, Handle: next,
		DrainRowsCommitted: 1, DrainBytesCommitted: 9, Record: record,
	}); err != nil || replayRevision != newRevision {
		t.Fatalf("identical drain replay revision=%d err=%v", replayRevision, err)
	}
	conflictingPage := page
	conflictingPage.Rows = []map[string]any{{"value": 999}}
	if _, err := materializer.CommitDrainObservation(ctx, jobs.DrainCommit{
		JobID: record.ID, Owner: record.Owner, Seq: 1, Page: conflictingPage, Handle: next,
		DrainRowsCommitted: 1, DrainBytesCommitted: 9, Record: record,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting drain replay err=%v, want ErrConflict", err)
	}
	got, ok, err := store.GetQueryJob(ctx, record.ID)
	if err != nil || !ok || got.DrainSeq != 1 || got.DrainRowsCommitted != 1 || got.DrainBytesCommitted != 9 ||
		got.DrainTruncated || got.Handle.Extra["next_uri"] != "two" {
		t.Fatalf("durable drained job=%#v ok=%v err=%v", got, ok, err)
	}
}

func TestQueryMaterializerFencedDrainRejectsExpiredAndTakenOverLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	first, thread, run := seedLongHorizonJSONRun(t, dir)
	second, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("open second JSON store: %v", err)
	}
	record := testDurableJob(thread, run, time.Date(2026, 7, 12, 18, 30, 0, 0, time.UTC))
	record.ID = "job_materializer_fenced_trino"
	record.Provider = "trino"
	record.Status = jobs.JobStatusRunning
	record.Handle = jobs.NativeHandle{Provider: "trino", ID: "query-fenced", Extra: map[string]string{"next_uri": "one"}}
	granted, revision, err := first.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v err=%v", granted, err)
	}
	if acquired, err := first.AcquireMonitorLease(ctx, record.ID, "holder-a", time.Minute); err != nil || !acquired {
		t.Fatalf("AcquireMonitorLease holder-a acquired=%v err=%v", acquired, err)
	}

	record.Revision = revision
	record.DrainSeq = 1
	record.DrainRowsCommitted = 1
	record.DrainBytesCommitted = 9
	next := jobs.NativeHandle{Provider: "trino", ID: "query-fenced", Extra: map[string]string{"next_uri": "two"}}
	commit := jobs.DrainCommit{
		JobID: record.ID, Owner: record.Owner, Seq: 1,
		Page:   jobs.ResultPage{Rows: []map[string]any{{"value": 1}}, Bytes: 9, CursorAfter: &next},
		Handle: next, DrainRowsCommitted: 1, DrainBytesCommitted: 9, Record: record,
	}

	// Expire holder-a in the authoritative record, then prove an expired
	// owner cannot leave even the chunk/event half of the atomic commit.
	first.longHorizonMu.Lock()
	wrapper, readErr := first.readJSONQueryJobLocked(record.ID, false)
	if readErr == nil {
		wrapper.MonitorLease.ExpiresAt = time.Now().UTC().Add(-time.Minute)
		readErr = first.writeJSONQueryJobLocked(wrapper)
	}
	first.longHorizonMu.Unlock()
	if readErr != nil {
		t.Fatalf("expire monitor lease: %v", readErr)
	}
	materializerA := NewQueryMaterializer(first)
	if _, err := materializerA.CommitDrainObservationFenced(ctx, commit, "holder-a"); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("expired holder commit err=%v, want ErrLeaseLost", err)
	}
	assertNoDrainEffects := func(stage string) {
		t.Helper()
		chunks, chunksErr := second.ListQueryResultChunks(ctx, record.ID, 0, 10)
		events, eventsErr := second.ListQueryJobEvents(ctx, record.ID, 0, 10)
		durable, ok, getErr := second.GetQueryJob(ctx, record.ID)
		if chunksErr != nil || eventsErr != nil || getErr != nil || !ok || len(chunks) != 0 || len(events) != 0 || durable.DrainSeq != 0 || durable.Revision != revision {
			t.Fatalf("%s left drain effects: chunks=%#v events=%#v job=%#v ok=%v errs=%v/%v/%v", stage, chunks, events, durable, ok, chunksErr, eventsErr, getErr)
		}
	}
	assertNoDrainEffects("expired holder")

	if acquired, err := second.AcquireMonitorLease(ctx, record.ID, "holder-b", time.Minute); err != nil || !acquired {
		t.Fatalf("AcquireMonitorLease holder-b acquired=%v err=%v", acquired, err)
	}
	if _, err := materializerA.CommitDrainObservationFenced(ctx, commit, "holder-a"); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("taken-over holder commit err=%v, want ErrLeaseLost", err)
	}
	assertNoDrainEffects("taken-over holder")
	badAccounting := commit
	badAccounting.DrainRowsCommitted = 2
	badAccounting.Record.DrainRowsCommitted = 2
	materializerB := NewQueryMaterializer(second)
	if _, err := materializerB.CommitDrainObservationFenced(ctx, badAccounting, "holder-b"); !errors.Is(err, ErrConflict) {
		t.Fatalf("non-monotonic accounting err=%v, want ErrConflict", err)
	}
	assertNoDrainEffects("non-monotonic accounting")

	staleRevision := commit
	staleRevision.Record.Revision++
	if _, err := materializerB.CommitDrainObservationFenced(ctx, staleRevision, "holder-b"); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("stale revision commit err=%v, want ErrRevisionConflict", err)
	}
	assertNoDrainEffects("stale revision")

	newRevision, err := materializerB.CommitDrainObservationFenced(ctx, commit, "holder-b")
	if err != nil || newRevision != revision+1 {
		t.Fatalf("fenced commit revision=%d err=%v", newRevision, err)
	}
	chunks, chunksErr := first.ListQueryResultChunks(ctx, record.ID, 0, 10)
	events, eventsErr := first.ListQueryJobEvents(ctx, record.ID, 0, 10)
	durable, ok, getErr := first.GetQueryJob(ctx, record.ID)
	if chunksErr != nil || eventsErr != nil || getErr != nil || !ok || len(chunks) != 1 || len(events) != 1 ||
		durable.DrainSeq != 1 || durable.DrainRowsCommitted != 1 || durable.DrainBytesCommitted != 9 || durable.DrainTruncated ||
		durable.Revision != newRevision || durable.Handle.Extra["next_uri"] != "two" {
		t.Fatalf("fenced commit not atomic: chunks=%#v events=%#v job=%#v ok=%v errs=%v/%v/%v", chunks, events, durable, ok, chunksErr, eventsErr, getErr)
	}

	durable.Status = jobs.JobStatusCompleted
	completedAt := time.Now().UTC()
	durable.CompletedAt = &completedAt
	terminalRevision, err := second.UpsertQueryJobCAS(ctx, durable, durable.Revision)
	if err != nil {
		t.Fatalf("terminalize drained job: %v", err)
	}
	if replayRevision, err := materializerB.CommitDrainObservationFenced(ctx, commit, "holder-b"); err != nil || replayRevision != terminalRevision {
		t.Fatalf("terminal identical replay revision=%d err=%v", replayRevision, err)
	}
	if replayRevision, err := materializerB.CommitDrainObservation(ctx, commit); err != nil || replayRevision != terminalRevision {
		t.Fatalf("legacy terminal identical replay revision=%d err=%v", replayRevision, err)
	}
	durable.Revision = terminalRevision
	durable.DrainSeq = 2
	durable.DrainRowsCommitted = 2
	durable.DrainBytesCommitted = 18
	nextAfterTerminal := jobs.NativeHandle{Provider: "trino", ID: "query-fenced", Extra: map[string]string{"next_uri": "three"}}
	terminalAppend := jobs.DrainCommit{
		JobID: durable.ID, Owner: durable.Owner, Seq: 2,
		Page:   jobs.ResultPage{Rows: []map[string]any{{"value": 2}}, Bytes: 9, CursorAfter: &nextAfterTerminal},
		Handle: nextAfterTerminal, DrainRowsCommitted: 2, DrainBytesCommitted: 18, Record: durable,
	}
	if _, err := materializerB.CommitDrainObservationFenced(ctx, terminalAppend, "holder-b"); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("new drain after terminal err=%v, want ErrRevisionConflict", err)
	}
	if _, err := materializerB.CommitDrainObservation(ctx, terminalAppend); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("legacy new drain after terminal err=%v, want ErrRevisionConflict", err)
	}
	chunks, chunksErr = first.ListQueryResultChunks(ctx, record.ID, 0, 10)
	events, eventsErr = first.ListQueryJobEvents(ctx, record.ID, 0, 10)
	durableAfterTerminal, ok, getErr := first.GetQueryJob(ctx, record.ID)
	if chunksErr != nil || eventsErr != nil || getErr != nil || !ok || len(chunks) != 1 || len(events) != 1 ||
		durableAfterTerminal.DrainSeq != 1 || durableAfterTerminal.DrainRowsCommitted != 1 || durableAfterTerminal.DrainBytesCommitted != 9 ||
		durableAfterTerminal.Revision != terminalRevision || durableAfterTerminal.Status != jobs.JobStatusCompleted {
		t.Fatalf("terminal append changed durable state: chunks=%#v events=%#v job=%#v ok=%v errs=%v/%v/%v", chunks, events, durableAfterTerminal, ok, chunksErr, eventsErr, getErr)
	}
}
