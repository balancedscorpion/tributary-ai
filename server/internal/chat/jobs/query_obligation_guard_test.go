package jobs

import (
	"context"
	"testing"
	"time"
)

// A managed job whose durable run was deleted (run_id tombstoned to blank)
// must never be re-homed into a caller's newer run: the in-memory RunID feeds
// run-scoped obligation identity in status payloads. Legacy never-scoped jobs
// keep the historical attach-once behavior.
func TestAttachOwnerNeverRehomesManagedDeletedRunTombstone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryJobStore()
	now := time.Now().UTC()
	seed := func(id string, managed bool) {
		record := JobRecord{
			ID: id, Provider: "bigquery", Status: JobStatusRunning,
			Owner:       JobOwner{ThreadID: "thread-1", RunID: "", Handle: "qown-" + id},
			SubmittedAt: now, LastAccess: now,
		}
		if managed {
			record.Metadata = map[string]any{QueryObligationManagedMetadataKey: true}
		}
		if _, err := store.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	seed("qj-managed-tombstone", true)
	seed("qj-legacy-unscoped", false)

	registry := NewRegistry(RegistryOptions{Store: store})

	managedOwner := JobOwner{ThreadID: "thread-1", RunID: "run-2", Handle: "qown-qj-managed-tombstone"}
	if err := registry.AttachOwner("qj-managed-tombstone", managedOwner); err != nil {
		t.Fatalf("AttachOwner managed: %v", err)
	}
	snapshot, ok := registry.Snapshot("qj-managed-tombstone", managedOwner)
	if !ok {
		t.Fatal("managed tombstone job not visible after attach")
	}
	if snapshot.Owner.RunID != "" {
		t.Fatalf("managed tombstone re-homed to run %q; blank durable run scope must be preserved", snapshot.Owner.RunID)
	}

	legacyOwner := JobOwner{ThreadID: "thread-1", RunID: "run-2", Handle: "qown-qj-legacy-unscoped"}
	if err := registry.AttachOwner("qj-legacy-unscoped", legacyOwner); err != nil {
		t.Fatalf("AttachOwner legacy: %v", err)
	}
	legacy, ok := registry.Snapshot("qj-legacy-unscoped", legacyOwner)
	if !ok {
		t.Fatal("legacy job not visible after attach")
	}
	if legacy.Owner.RunID != "run-2" {
		t.Fatalf("legacy never-scoped job RunID=%q, want historical attach-once fill", legacy.Owner.RunID)
	}
}

// UserToken is durable identity (freezeQueryJobIdentity rejects mismatches)
// and ownerMatches does not compare it. AttachOwner must never change it,
// including filling a blank durable token from caller-local context.
func TestAttachOwnerNeverMutatesDurableUserToken(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewMemoryJobStore()
	now := time.Now().UTC()
	record := JobRecord{
		ID: "qj-user-token", Provider: "bigquery", Status: JobStatusRunning,
		Owner:       JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "qown-user-token", UserToken: "user-original"},
		SubmittedAt: now, LastAccess: now,
	}
	if _, err := store.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	registry := NewRegistry(RegistryOptions{Store: store})
	owner := JobOwner{ThreadID: "thread-1", Handle: "qown-user-token", UserToken: "user-imposter"}
	if err := registry.AttachOwner("qj-user-token", owner); err != nil {
		t.Fatalf("AttachOwner: %v", err)
	}
	snapshot, ok := registry.Snapshot("qj-user-token", owner)
	if !ok {
		t.Fatal("job not visible after attach")
	}
	if snapshot.Owner.UserToken != "user-original" {
		t.Fatalf("UserToken overwritten to %q; durable identity must be preserved", snapshot.Owner.UserToken)
	}

	blank := JobRecord{
		ID: "qj-user-token-blank", Provider: "bigquery", Status: JobStatusRunning,
		Owner:       JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "qown-user-token-blank"},
		SubmittedAt: now, LastAccess: now,
	}
	if _, err := store.UpsertQueryJobCAS(ctx, blank, 0); err != nil {
		t.Fatalf("seed blank-token job: %v", err)
	}
	blankOwner := JobOwner{ThreadID: "thread-1", Handle: "qown-user-token-blank", UserToken: "user-first"}
	if err := registry.AttachOwner("qj-user-token-blank", blankOwner); err != nil {
		t.Fatalf("AttachOwner blank: %v", err)
	}
	attached, ok := registry.Snapshot("qj-user-token-blank", blankOwner)
	if !ok {
		t.Fatal("blank-token job not visible after attach")
	}
	if attached.Owner.UserToken != "" {
		t.Fatalf("blank durable UserToken filled from caller context: owner=%#v", attached.Owner)
	}
}
