package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/jsondb"
)

func seedObligationRunAndJobs(t *testing.T) (*JSONStore, Thread, Run) {
	t.Helper()
	repository, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	ctx := context.Background()
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return repository, thread, run
}

func upsertObligationJob(t *testing.T, repository *JSONStore, thread Thread, run Run, id, replaces string, status jobs.JobStatus, validation jobs.ValidationState) jobs.JobRecord {
	t.Helper()
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: id, Provider: "databricks", Status: status,
		Owner:         jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-" + id},
		ReplacesJobID: replaces, ValidationState: validation,
		Metadata:    map[string]any{jobs.QueryObligationManagedMetadataKey: true},
		SubmittedAt: now, LastAccess: now,
	}
	if status == jobs.JobStatusFailed || status == jobs.JobStatusCancelled || status == jobs.JobStatusCompleted {
		record.CompletedAt = &now
	}
	if _, err := repository.UpsertQueryJobCAS(context.Background(), record, 0); err != nil {
		t.Fatalf("persist job %s: %v", id, err)
	}
	return record
}

func TestQueryObligationIDIsStrictOpaqueAndRunScoped(t *testing.T) {
	t.Parallel()
	repository, thread, run := seedObligationRunAndJobs(t)
	ctx := context.Background()
	upsertObligationJob(t, repository, thread, run, "qj-root", "", jobs.JobStatusFailed, jobs.ValidationRejected)
	upsertObligationJob(t, repository, thread, run, "qj-second", "qj-root", jobs.JobStatusFailed, jobs.ValidationRejected)
	upsertObligationJob(t, repository, thread, run, "qj-third", "qj-second", jobs.JobStatusRunning, jobs.ValidationUnvalidated)

	want, err := QueryObligationIDForRoot(run.ID, "qj-root")
	if err != nil {
		t.Fatalf("QueryObligationIDForRoot: %v", err)
	}
	for _, member := range []string{"qj-root", "qj-second", "qj-third"} {
		derived, deriveErr := DeriveQueryObligationID(ctx, repository, run.ID, member)
		if deriveErr != nil || derived != want {
			t.Fatalf("Derive(%s)=%q err=%v, want %q", member, derived, deriveErr, want)
		}
		if validateErr := ValidateQueryObligationID(derived); validateErr != nil {
			t.Fatalf("Validate(%q): %v", derived, validateErr)
		}
	}
	otherRunID, err := QueryObligationIDForRoot("another-run", "qj-root")
	if err != nil || otherRunID == want {
		t.Fatalf("other-run obligation=%q err=%v, want distinct run-scoped id", otherRunID, err)
	}
	for _, malformed := range []string{"", "qob_123", "qob-00000000000000000000000000000000", "qob-ABCDEF0123456789abcdef0123456789", "qj-root"} {
		if ValidateQueryObligationID(malformed) == nil {
			t.Fatalf("ValidateQueryObligationID(%q) succeeded", malformed)
		}
	}

	upsertObligationJob(t, repository, thread, run, "qj-cycle-a", "qj-cycle-b", jobs.JobStatusFailed, jobs.ValidationRejected)
	upsertObligationJob(t, repository, thread, run, "qj-cycle-b", "qj-cycle-a", jobs.JobStatusFailed, jobs.ValidationRejected)
	if _, err := DeriveQueryObligationID(ctx, repository, run.ID, "qj-cycle-a"); !errors.Is(err, ErrConflict) {
		t.Fatalf("cyclic derivation err=%v, want ErrConflict", err)
	}
}

func TestDeriveQueryObligationIDRejectsMissingLineage(t *testing.T) {
	t.Parallel()
	repository, thread, run := seedObligationRunAndJobs(t)
	ctx := context.Background()
	upsertObligationJob(t, repository, thread, run, "qj-child", "qj-missing-parent", jobs.JobStatusRunning, jobs.ValidationUnvalidated)

	for _, jobID := range []string{"qj-child", "qj-missing-root"} {
		if derived, err := DeriveQueryObligationID(ctx, repository, run.ID, jobID); !errors.Is(err, ErrConflict) {
			t.Fatalf("Derive(%s)=%q err=%v, want fail-closed ErrConflict", jobID, derived, err)
		}
	}
}

func TestJSONListRunQueryDependenciesRejectsCorruptIdentityOrObligation(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		mutate func(*jsonRunQueryDependencyRecord)
	}{
		{
			name: "run id differs from directory",
			mutate: func(record *jsonRunQueryDependencyRecord) {
				record.RunID = "run-from-another-directory"
			},
		},
		{
			name: "job id differs from filename",
			mutate: func(record *jsonRunQueryDependencyRecord) {
				record.JobID = "qj-from-another-file"
			},
		},
		{
			name: "malformed obligation id",
			mutate: func(record *jsonRunQueryDependencyRecord) {
				record.ObligationID = "qob-00000000000000000000000000000000"
			},
		},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			repository, thread, run := seedObligationRunAndJobs(t)
			ctx := context.Background()
			job := upsertObligationJob(t, repository, thread, run, "qj-corrupt", "", jobs.JobStatusRunning, jobs.ValidationUnvalidated)
			if err := repository.BindQueryObligation(ctx, job.ID); err != nil {
				t.Fatalf("bind dependency: %v", err)
			}
			path, err := runQueryDependencyPath(repository.cfgDir, run.ID, job.ID)
			if err != nil {
				t.Fatalf("dependency path: %v", err)
			}
			var record jsonRunQueryDependencyRecord
			if err := readJSONLoose(path, &record); err != nil {
				t.Fatalf("read dependency record: %v", err)
			}
			testCase.mutate(&record)
			if err := jsondb.WriteJSONFile(path, record); err != nil {
				t.Fatalf("write corrupt dependency record: %v", err)
			}

			if dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID); !errors.Is(err, ErrConflict) {
				t.Fatalf("dependencies=%#v err=%v, want corruption ErrConflict", dependencies, err)
			}
		})
	}
}

func TestJSONBindQueryObligationUpgradesUnmarkedLegacyJob(t *testing.T) {
	t.Parallel()
	repository, thread, run := seedObligationRunAndJobs(t)
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-legacy-unmarked", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-legacy"},
		SubmittedAt: now, LastAccess: now,
	}
	if _, err := repository.UpsertQueryJobCAS(context.Background(), record, 0); err != nil {
		t.Fatalf("persist legacy job: %v", err)
	}
	if err := repository.BindQueryObligation(context.Background(), record.ID); err != nil {
		t.Fatalf("bind unmarked legacy job: %v", err)
	}
	dependencies, err := repository.ListRunQueryDependencies(context.Background(), run.ID)
	if err != nil || len(dependencies) != 1 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	want, _ := QueryObligationIDForRoot(run.ID, record.ID)
	if dependencies[0].JobID != record.ID || dependencies[0].ObligationID != want || dependencies[0].Detached {
		t.Fatalf("legacy dependency=%#v want active obligation %s", dependencies[0], want)
	}
}

func TestJSONBindQueryObligationAtomicallyAdvancesConcreteSuccessor(t *testing.T) {
	t.Parallel()
	repository, thread, run := seedObligationRunAndJobs(t)
	ctx := context.Background()
	upsertObligationJob(t, repository, thread, run, "qj-root", "", jobs.JobStatusFailed, jobs.ValidationRejected)
	if err := repository.BindQueryObligation(ctx, "qj-root"); err != nil {
		t.Fatalf("bind root: %v", err)
	}
	want, _ := QueryObligationIDForRoot(run.ID, "qj-root")

	upsertObligationJob(t, repository, thread, run, "qj-next", "qj-root", jobs.JobStatusRunning, jobs.ValidationUnvalidated)
	if err := repository.BindQueryObligation(ctx, "qj-next"); err != nil {
		t.Fatalf("bind replacement: %v", err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	active := 0
	for _, dependency := range dependencies {
		if dependency.ObligationID != want {
			t.Fatalf("dependency %s obligation=%q, want %q", dependency.JobID, dependency.ObligationID, want)
		}
		switch dependency.JobID {
		case "qj-root":
			if !dependency.Detached || dependency.ReplacementJobID != "qj-next" {
				t.Fatalf("predecessor=%#v", dependency)
			}
		case "qj-next":
			if dependency.Detached || !dependency.RequiredForCompletion {
				t.Fatalf("successor=%#v", dependency)
			}
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active attempts=%d, want one", active)
	}

	// Replaying an old generic write must never reactivate or erase lineage.
	if _, err := repository.UpsertRunQueryDependency(ctx, RunQueryDependency{
		RunID: run.ID, JobID: "qj-root", RequiredForCompletion: true,
		ValidationState: QueryValidationRejected, ObligationID: want,
	}); err != nil {
		t.Fatalf("replay old dependency: %v", err)
	}
	replayed, _ := repository.ListRunQueryDependencies(ctx, run.ID)
	for _, dependency := range replayed {
		if dependency.JobID == "qj-root" && (!dependency.Detached || dependency.ReplacementJobID != "qj-next") {
			t.Fatalf("old replay reactivated predecessor: %#v", dependency)
		}
	}
	if err := repository.BindQueryObligation(ctx, "qj-next"); err != nil {
		t.Fatalf("idempotent successor bind: %v", err)
	}
}

func seedJSONDetachedObligationReplay(t *testing.T) (*JSONStore, Run, string) {
	t.Helper()
	repository, thread, run := seedObligationRunAndJobs(t)
	ctx := context.Background()
	upsertObligationJob(t, repository, thread, run, "qj-root", "", jobs.JobStatusFailed, jobs.ValidationRejected)
	if err := repository.BindQueryObligation(ctx, "qj-root"); err != nil {
		t.Fatalf("bind root: %v", err)
	}
	upsertObligationJob(t, repository, thread, run, "qj-next", "qj-root", jobs.JobStatusRunning, jobs.ValidationUnvalidated)
	if err := repository.BindQueryObligation(ctx, "qj-next"); err != nil {
		t.Fatalf("bind replacement: %v", err)
	}
	want, err := QueryObligationIDForRoot(run.ID, "qj-root")
	if err != nil {
		t.Fatalf("QueryObligationIDForRoot: %v", err)
	}
	return repository, run, want
}

func rewriteJSONDependencyObligation(t *testing.T, repository *JSONStore, runID, jobID, obligationID string) {
	t.Helper()
	path, err := runQueryDependencyPath(repository.cfgDir, runID, jobID)
	if err != nil {
		t.Fatalf("dependency path %s: %v", jobID, err)
	}
	var record jsonRunQueryDependencyRecord
	if err := readJSONLoose(path, &record); err != nil {
		t.Fatalf("read dependency %s: %v", jobID, err)
	}
	record.ObligationID = obligationID
	if err := jsondb.WriteJSONFile(path, record); err != nil {
		t.Fatalf("rewrite dependency %s: %v", jobID, err)
	}
}

func readJSONDependencyForTest(t *testing.T, repository *JSONStore, runID, jobID string) RunQueryDependency {
	t.Helper()
	path, err := runQueryDependencyPath(repository.cfgDir, runID, jobID)
	if err != nil {
		t.Fatalf("dependency path %s: %v", jobID, err)
	}
	var record jsonRunQueryDependencyRecord
	if err := readJSONLoose(path, &record); err != nil {
		t.Fatalf("read dependency %s: %v", jobID, err)
	}
	return record.RunQueryDependency
}

func TestJSONReplaceRunQueryDependencyHydratesLegacyDetachedReplay(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name             string
		blankPredecessor bool
		blankSuccessor   bool
	}{
		{name: "both null", blankPredecessor: true, blankSuccessor: true},
		{name: "predecessor null", blankPredecessor: true},
		{name: "successor null", blankSuccessor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, run, want := seedJSONDetachedObligationReplay(t)
			if test.blankPredecessor {
				rewriteJSONDependencyObligation(t, repository, run.ID, "qj-root", "")
			}
			if test.blankSuccessor {
				rewriteJSONDependencyObligation(t, repository, run.ID, "qj-next", "")
			}

			replayed, err := repository.ReplaceRunQueryDependency(context.Background(), run.ID, "qj-root", RunQueryDependency{
				RunID: run.ID, JobID: "qj-next", RequiredForCompletion: true,
				ValidationState: QueryValidationUnvalidated,
			})
			if err != nil {
				t.Fatalf("ReplaceRunQueryDependency: %v", err)
			}
			if replayed.JobID != "qj-next" || replayed.ObligationID != want || replayed.Detached || !replayed.RequiredForCompletion {
				t.Fatalf("replayed dependency=%#v, want active successor with obligation %q", replayed, want)
			}
			predecessor := readJSONDependencyForTest(t, repository, run.ID, "qj-root")
			successor := readJSONDependencyForTest(t, repository, run.ID, "qj-next")
			if predecessor.ObligationID != want || !predecessor.Detached || predecessor.ReplacementJobID != successor.JobID {
				t.Fatalf("hydrated predecessor=%#v, want detached lineage with obligation %q", predecessor, want)
			}
			if successor.ObligationID != want || successor.Detached || !successor.RequiredForCompletion {
				t.Fatalf("hydrated successor=%#v, want sole active attempt with obligation %q", successor, want)
			}
		})
	}
}

func TestJSONReplaceRunQueryDependencyRejectsMismatchedLegacyObligations(t *testing.T) {
	t.Parallel()
	repository, run, want := seedJSONDetachedObligationReplay(t)
	other, err := QueryObligationIDForRoot(run.ID, "qj-other-root")
	if err != nil || other == want {
		t.Fatalf("alternate obligation=%q err=%v, want distinct id", other, err)
	}
	rewriteJSONDependencyObligation(t, repository, run.ID, "qj-root", want)
	rewriteJSONDependencyObligation(t, repository, run.ID, "qj-next", other)

	_, err = repository.ReplaceRunQueryDependency(context.Background(), run.ID, "qj-root", RunQueryDependency{
		RunID: run.ID, JobID: "qj-next", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched obligation replay err=%v, want ErrConflict", err)
	}
	predecessor := readJSONDependencyForTest(t, repository, run.ID, "qj-root")
	successor := readJSONDependencyForTest(t, repository, run.ID, "qj-next")
	if predecessor.ObligationID != want || successor.ObligationID != other {
		t.Fatalf("mismatch rejection mutated rows: predecessor=%#v successor=%#v", predecessor, successor)
	}
}

func TestJSONReplaceRunQueryDependencyRejectsMissingAncestorWithoutMutation(t *testing.T) {
	t.Parallel()
	repository, thread, run := seedObligationRunAndJobs(t)
	ctx := context.Background()
	upsertObligationJob(t, repository, thread, run, "qj-old", "qj-missing-ancestor", jobs.JobStatusFailed, jobs.ValidationRejected)
	now := time.Now().UTC()
	if _, err := repository.UpsertRunQueryDependency(ctx, RunQueryDependency{
		RunID: run.ID, JobID: "qj-old", RequiredForCompletion: true,
		ValidationState: QueryValidationRejected, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed active predecessor dependency: %v", err)
	}
	upsertObligationJob(t, repository, thread, run, "qj-new", "qj-old", jobs.JobStatusReserved, jobs.ValidationUnvalidated)
	before := readJSONDependencyForTest(t, repository, run.ID, "qj-old")

	_, err := repository.ReplaceRunQueryDependency(ctx, run.ID, "qj-old", RunQueryDependency{
		RunID: run.ID, JobID: "qj-new", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated, CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("missing-ancestor replacement err=%v, want ErrConflict", err)
	}
	after := readJSONDependencyForTest(t, repository, run.ID, "qj-old")
	if after != before {
		t.Fatalf("missing-ancestor rejection mutated predecessor: before=%#v after=%#v", before, after)
	}
	dependencies, listErr := repository.ListRunQueryDependencies(ctx, run.ID)
	if listErr != nil || len(dependencies) != 1 || dependencies[0].JobID != "qj-old" {
		t.Fatalf("dependencies after rejected swap=%#v err=%v, want only unchanged predecessor", dependencies, listErr)
	}
}

func TestJSONBindQueryObligationRejectsCompetingOrMislinkedSuccessor(t *testing.T) {
	t.Parallel()
	repository, thread, run := seedObligationRunAndJobs(t)
	ctx := context.Background()
	upsertObligationJob(t, repository, thread, run, "qj-root", "", jobs.JobStatusFailed, jobs.ValidationRejected)
	if err := repository.BindQueryObligation(ctx, "qj-root"); err != nil {
		t.Fatalf("bind root: %v", err)
	}
	upsertObligationJob(t, repository, thread, run, "qj-winner", "qj-root", jobs.JobStatusReserved, jobs.ValidationUnvalidated)
	upsertObligationJob(t, repository, thread, run, "qj-loser", "qj-root", jobs.JobStatusReserved, jobs.ValidationUnvalidated)
	if err := repository.BindQueryObligation(ctx, "qj-winner"); err != nil {
		t.Fatalf("bind winner: %v", err)
	}
	if err := repository.BindQueryObligation(ctx, "qj-loser"); !errors.Is(err, ErrConflict) {
		t.Fatalf("competing successor bind err=%v, want ErrConflict", err)
	}

	// A caller cannot attach an existing job whose durable lineage names a
	// different predecessor, even if it supplies a plausible dependency row.
	upsertObligationJob(t, repository, thread, run, "qj-other-root", "", jobs.JobStatusFailed, jobs.ValidationRejected)
	if err := repository.BindQueryObligation(ctx, "qj-other-root"); err != nil {
		t.Fatalf("bind other root: %v", err)
	}
	if _, err := repository.ReplaceRunQueryDependency(ctx, run.ID, "qj-other-root", RunQueryDependency{
		RunID: run.ID, JobID: "qj-loser", RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mislinked successor err=%v, want ErrConflict", err)
	}
}

func TestMySQLListRunQueryDependenciesRejectsInvalidObligation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	db, state := openLongHorizonScriptDB(t, longHorizonScriptStep{
		kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9),
		rows: [][]driver.Value{{
			"run-1", "job-corrupt", true, false, QueryValidationUnvalidated,
			"qob-00000000000000000000000000000000", nil, now, now,
		}},
	})
	_, err := NewMySQL(db).ListRunQueryDependencies(context.Background(), "run-1")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("invalid stored obligation err=%v, want ErrConflict", err)
	}
	assertLongHorizonScriptDone(t, state)
}
