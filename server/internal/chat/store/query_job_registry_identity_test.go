package store

import (
	"context"
	"testing"

	"ai-analyst-local/internal/chat/jobs"
)

func TestJSONRegistryAttachOwnerPreservesBlankDurableUserTokenAcrossTransition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	durableOwner := jobs.JobOwner{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Handle:   "qown-blank-user-token",
	}

	job, err := registry.Register(
		durableOwner,
		"bigquery",
		"select 1",
		"select 1",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	stored, found, err := repository.GetQueryJob(ctx, job.ID)
	if err != nil || !found {
		t.Fatalf("GetQueryJob after register: found=%v err=%v", found, err)
	}
	if stored.Owner.UserToken != "" {
		t.Fatalf("seeded UserToken=%q, want blank", stored.Owner.UserToken)
	}

	observer := durableOwner
	observer.UserToken = "caller-local-user-token"
	if err := registry.AttachOwner(job.ID, observer); err != nil {
		t.Fatalf("AttachOwner: %v", err)
	}
	if err := registry.SetArtifactID(job.ID, observer, "artifact-after-attach"); err != nil {
		t.Fatalf("SetArtifactID: %v", err)
	}

	stored, found, err = repository.GetQueryJob(ctx, job.ID)
	if err != nil || !found {
		t.Fatalf("GetQueryJob after transition: found=%v err=%v", found, err)
	}
	if stored.Owner.UserToken != "" {
		t.Fatalf("durable UserToken=%q after attach and transition, want blank", stored.Owner.UserToken)
	}
	if stored.ArtifactID != "artifact-after-attach" {
		t.Fatalf("ArtifactID=%q, want persisted transition after attach", stored.ArtifactID)
	}
}
