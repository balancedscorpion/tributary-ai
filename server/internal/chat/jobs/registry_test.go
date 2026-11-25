package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRegistryOwnershipStrictThreadAndHandle(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	job, err := r.Register(
		JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, ok := r.Snapshot(job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_owner"}); !ok {
		t.Fatalf("expected matching owner to read snapshot")
	}
	if _, ok := r.Snapshot(job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_other"}); ok {
		t.Fatalf("expected mismatched handle to be rejected")
	}
	if _, ok := r.Snapshot(job.ID, JobOwner{ThreadID: "thread-2", Handle: "tok_owner"}); ok {
		t.Fatalf("expected mismatched thread to be rejected")
	}
}

func TestRegistryAttachOwnerDoesNotRehomeExistingRun(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	job, err := r.Register(
		JobOwner{ThreadID: "thread-1", RunID: "run-old", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	newRunObserver := JobOwner{ThreadID: "thread-1", RunID: "run-new", Handle: "tok_owner"}
	if err := r.AttachOwner(job.ID, newRunObserver); err != nil {
		t.Fatalf("attach same-thread observer: %v", err)
	}
	snap, ok := r.Snapshot(job.ID, newRunObserver)
	if !ok {
		t.Fatal("same-thread observer lost access to existing job")
	}
	if snap.Owner.RunID != "run-old" {
		t.Fatalf("owner run=%q want immutable original run %q", snap.Owner.RunID, "run-old")
	}
}

func TestRegistryAttachOwnerFillsBlankRunOnlyOnce(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	job, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	firstAttachment := JobOwner{ThreadID: "thread-1", RunID: "run-first", Handle: "tok_owner"}
	if err := r.AttachOwner(job.ID, firstAttachment); err != nil {
		t.Fatalf("attach previously unscoped job: %v", err)
	}
	secondAttachment := JobOwner{ThreadID: "thread-1", RunID: "run-second", Handle: "tok_owner"}
	if err := r.AttachOwner(job.ID, secondAttachment); err != nil {
		t.Fatalf("observe scoped job from later run: %v", err)
	}
	snap, ok := r.Snapshot(job.ID, secondAttachment)
	if !ok {
		t.Fatal("same-thread observer lost access to attached job")
	}
	if snap.Owner.RunID != "run-first" {
		t.Fatalf("owner run=%q want first attached run %q", snap.Owner.RunID, "run-first")
	}
}

func TestRegistryCancelIsTerminalAndIdempotent(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	job, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_owner"},
		"databricks",
		"select 1",
		"select 1",
		nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		func(ctx context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	owner := JobOwner{ThreadID: "thread-1", Handle: "tok_owner"}
	if err := r.Cancel(job.ID, owner); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := r.Cancel(job.ID, owner); err != nil {
		t.Fatalf("cancel idempotent: %v", err)
	}
	snap, ok := r.Snapshot(job.ID, owner)
	if !ok {
		t.Fatalf("snapshot after cancel not found")
	}
	if snap.Status != JobStatusCancelled {
		t.Fatalf("status=%s want=%s", snap.Status, JobStatusCancelled)
	}
}

func TestRegistryResultsCompletedAndOwnerChecked(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	job, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_owner"},
		"bigquery",
		"select 1 as n",
		"select 1 as n",
		nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			return true, &QueryResult{
				Rows: []map[string]any{
					{"n": 1},
				},
				Metadata: map[string]any{
					"state":           "DONE",
					"terminal_status": "completed",
				},
			}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		snap, ok := r.Snapshot(job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_owner"})
		if ok && snap.Status == JobStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not complete in time")
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := r.Results(job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_other"}); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("results ownership error=%v want=%v", err, ErrOwnershipMismatch)
	}
	res, err := r.Results(job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_owner"})
	if err != nil {
		t.Fatalf("results: %v", err)
	}
	if len(res.Rows) != 1 {
		t.Fatalf("rows=%d want=1", len(res.Rows))
	}
}

func TestRegistryCancelActiveByThread(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	jobA, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_a"},
		"bigquery",
		"select 1",
		"select 1",
		nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register jobA: %v", err)
	}
	jobB, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_b"},
		"databricks",
		"select 2",
		"select 2",
		nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register jobB: %v", err)
	}
	other, err := r.Register(
		JobOwner{ThreadID: "thread-2", Handle: "tok_other"},
		"bigquery",
		"select 3",
		"select 3",
		nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register other: %v", err)
	}

	cancelled := r.CancelActiveByThread("thread-1")
	if len(cancelled) != 2 {
		t.Fatalf("cancelled=%v want 2 jobs", cancelled)
	}
	if snap, ok := r.Snapshot(jobA.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_a"}); !ok || snap.Status != JobStatusCancelled {
		t.Fatalf("jobA status=%v ok=%v want cancelled,true", statusOf(snap), ok)
	}
	if snap, ok := r.Snapshot(jobB.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_b"}); !ok || snap.Status != JobStatusCancelled {
		t.Fatalf("jobB status=%v ok=%v want cancelled,true", statusOf(snap), ok)
	}
	if snap, ok := r.Snapshot(other.ID, JobOwner{ThreadID: "thread-2", Handle: "tok_other"}); !ok || isTerminal(snap.Status) {
		t.Fatalf("other status=%v ok=%v want non-terminal,true", statusOf(snap), ok)
	}
}

func statusOf(job *Job) JobStatus {
	if job == nil {
		return JobStatusUnknown
	}
	return job.Status
}

func TestNewJobIDProviderPrefixes(t *testing.T) {
	for _, tc := range []struct {
		provider string
		prefix   string
	}{
		{provider: "bigquery", prefix: "qj-bq-"},
		{provider: "databricks", prefix: "qj-dbx-"},
		{provider: "trino", prefix: "qj-tri-"},
		{provider: "snowflake", prefix: "qj-sf-"},
		{provider: "unknown", prefix: "qj-job-"},
	} {
		id, err := newJobID(tc.provider)
		if err != nil {
			t.Fatalf("%s: newJobID error: %v", tc.provider, err)
		}
		if !strings.HasPrefix(id, tc.prefix) {
			t.Fatalf("%s: id=%q, want prefix %q", tc.provider, id, tc.prefix)
		}
	}
}
