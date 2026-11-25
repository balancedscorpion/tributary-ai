package sources

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreUpsertListGetDelete(t *testing.T) {
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	created, err := store.Upsert(ctx, Source{
		ID:           "github-test",
		Kind:         KindGitHubClone,
		Title:        "acme/widget",
		Repository:   "acme/widget",
		Capabilities: []string{"search", "read", "search"},
		Metadata:     map[string]any{"depth": 1},
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("timestamps were not populated: %#v", created)
	}
	if len(created.Capabilities) != 2 {
		t.Fatalf("capabilities were not deduplicated: %#v", created.Capabilities)
	}

	got, err := store.Get(ctx, "github-test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got.Metadata["depth"] = 9

	again, err := store.Get(ctx, "github-test")
	if err != nil {
		t.Fatalf("Get after clone mutation: %v", err)
	}
	if again.Metadata["depth"] != float64(1) && again.Metadata["depth"] != 1 {
		t.Fatalf("Get returned mutable metadata: %#v", again.Metadata)
	}

	rows, err := store.List(ctx, ListOptions{Kind: KindGitHubClone})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "github-test" {
		t.Fatalf("List rows=%#v, want github-test", rows)
	}

	if _, err := store.Delete(ctx, "github-test"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows, err = store.List(ctx, ListOptions{Kind: KindGitHubClone})
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows after delete=%#v, want none", rows)
	}
}

func TestGitHubWorktreePathIsAppManaged(t *testing.T) {
	cfgDir := t.TempDir()
	store, err := NewStore(cfgDir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	sourceID := GitHubSourceID("Acme/Widget")
	cacheDir, err := store.GitHubSourceDir(sourceID)
	if err != nil {
		t.Fatalf("GitHubSourceDir: %v", err)
	}
	worktree, err := store.GitHubWorktreePath(sourceID)
	if err != nil {
		t.Fatalf("GitHubWorktreePath: %v", err)
	}
	wantPrefix := filepath.Join(cfgDir, "sources", "github", sourceID)
	if cacheDir != wantPrefix {
		t.Fatalf("cacheDir=%s, want %s", cacheDir, wantPrefix)
	}
	if filepath.Dir(worktree) != wantPrefix {
		t.Fatalf("worktree=%s, want under %s", worktree, wantPrefix)
	}
	if _, err := os.Stat(wantPrefix); err != nil {
		t.Fatalf("source directory was not created: %v", err)
	}
	if GitHubSourceID("Acme/Widget") != GitHubSourceID("acme/widget") {
		t.Fatalf("GitHubSourceID should be case-insensitive")
	}
}
