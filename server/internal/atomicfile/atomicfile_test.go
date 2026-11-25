package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileCreatesAndReplacesExistingFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "thread.json")

	if err := WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(old): %v", err)
	}
	if err := WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(new): %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(os): %v", err)
	}
	if string(got) != "new\n" {
		t.Fatalf("content=%q, want new", string(got))
	}

	matches, err := filepath.Glob(filepath.Join(dir, "thread.jsontmp-*"))
	if err != nil {
		t.Fatalf("Glob(tmp): %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temp files were not cleaned up: %v", matches)
	}
}

func TestReadFileReturnsContents(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "thread.json")
	if err := os.WriteFile(path, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile(os): %v", err)
	}

	got, err := ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("content=%q", string(got))
	}
}
