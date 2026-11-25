package projects

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileRejectsSymlinkEscape(t *testing.T) {
	store := newTestStore(t)
	projectID := "project-symlink-escape"
	root := ensureTestFilesDir(t, store, projectID)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	link := filepath.Join(root, "link.txt")
	createSymlinkOrSkip(t, outside, link)

	if data, _, err := store.ReadFile(projectID, "link.txt", 1024); err == nil {
		t.Fatalf("ReadFile followed symlink outside project root; data=%q", string(data))
	} else if !strings.Contains(err.Error(), "escapes project root") {
		t.Fatalf("ReadFile error=%v, want project-root escape error", err)
	}

	f, err := store.OpenFile(projectID, "link.txt")
	if err == nil {
		_ = f.Close()
		t.Fatal("OpenFile followed symlink outside project root")
	}
	if !strings.Contains(err.Error(), "escapes project root") {
		t.Fatalf("OpenFile error=%v, want project-root escape error", err)
	}
}

func TestReadFileRejectsSymlinkToGitInternalPath(t *testing.T) {
	store := newTestStore(t)
	projectID := "project-symlink-git"
	root := ensureTestFilesDir(t, store, projectID)
	gitConfig := filepath.Join(root, ".git", "config")
	if err := os.MkdirAll(filepath.Dir(gitConfig), dirPerm); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(gitConfig, []byte("[core]\n"), filePerm); err != nil {
		t.Fatalf("write .git config: %v", err)
	}
	createSymlinkOrSkip(t, gitConfig, filepath.Join(root, "git-config-link"))

	if _, _, err := store.ReadFile(projectID, "git-config-link", 1024); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile error=%v, want fs.ErrNotExist", err)
	}
}

func TestReadFileRejectsSymlinkToHiddenSource(t *testing.T) {
	store := newTestStore(t)
	projectID := "project-symlink-hidden"
	root := ensureTestFilesDir(t, store, projectID)
	if err := store.PutFile(projectID, "hidden/secret.txt", []byte("secret")); err != nil {
		t.Fatalf("PutFile hidden: %v", err)
	}
	if err := store.SetSourceHidden(context.Background(), projectID, "hidden", true); err != nil {
		t.Fatalf("SetSourceHidden: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "visible"), dirPerm); err != nil {
		t.Fatalf("mkdir visible: %v", err)
	}
	createSymlinkOrSkip(t, filepath.Join(root, "hidden", "secret.txt"), filepath.Join(root, "visible", "secret-link.txt"))

	if _, _, err := store.ReadFile(projectID, "visible/secret-link.txt", 1024); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadFile error=%v, want fs.ErrNotExist", err)
	}
}

func TestSearchFilesSkipsSensitiveLocalPaths(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	projectID := "project-sensitive-search"
	files := map[string]string{
		"public.txt":      "SECRET public marker",
		".env":            "SECRET env token",
		".ssh/id_ed25519": "SECRET ssh key",
		".config/gcloud/application_default_credentials.json": "SECRET gcloud token",
		".docker/config.json":            "SECRET docker auth",
		"credentials/service.json":       "SECRET service credential",
		"nested/my-service-account.json": "SECRET service account",
		"src/.npmrc":                     "SECRET npm token",
		"src/client.pem":                 "SECRET pem key",
	}
	for path, content := range files {
		if err := store.PutFile(projectID, path, []byte(content)); err != nil {
			t.Fatalf("PutFile %s: %v", path, err)
		}
	}

	result, err := store.SearchFiles(ctx, projectID, "SECRET", SearchOptions{MaxMatches: 100})
	if err != nil {
		t.Fatalf("SearchFiles: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches=%#v, want only public.txt", result.Matches)
	}
	if _, ok := result.Matches["public.txt"]; !ok {
		t.Fatalf("matches=%#v, want public.txt", result.Matches)
	}
	for sensitive := range files {
		if sensitive == "public.txt" {
			continue
		}
		if _, ok := result.Matches[sensitive]; ok {
			t.Fatalf("sensitive path %s was searchable: %#v", sensitive, result.Matches[sensitive])
		}
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func ensureTestFilesDir(t *testing.T, store *Store, projectID string) string {
	t.Helper()
	root, err := store.EnsureFilesDir(projectID)
	if err != nil {
		t.Fatalf("EnsureFilesDir: %v", err)
	}
	return root
}

func createSymlinkOrSkip(t *testing.T, oldname string, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}
}
