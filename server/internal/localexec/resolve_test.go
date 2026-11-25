package localexec

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestResolveCommandChecksExtraDirsWhenPathMisses(t *testing.T) {
	dir := t.TempDir()
	name := "gh"
	if runtime.GOOS == "windows" {
		name = "gh.exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	t.Setenv("PATH", "")
	restore := swapExtraCommandDirs(func(string) []string { return []string{dir} })
	defer restore()

	got, ok := ResolveCommand("gh")
	if !ok {
		t.Fatal("ResolveCommand did not find executable in extra dirs")
	}
	if got != path {
		t.Fatalf("ResolveCommand path = %q, want %q", got, path)
	}
}

func TestResolveCommandChecksBundledRuntimeNativeDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX executable bits")
	}
	runtimeDir := t.TempDir()
	binDir := filepath.Join(runtimeDir, "native", "poppler", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(binDir, "pdftoppm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(runtimeDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	t.Setenv("PATH", "")
	restore := swapExtraCommandDirs(func(string) []string { return nil })
	defer restore()

	got, ok := ResolveCommand("pdftoppm")
	if !ok {
		t.Fatal("ResolveCommand did not find executable in bundled native runtime dirs")
	}
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(path)
	if gotReal != wantReal {
		t.Fatalf("ResolveCommand path = %q, want %q", got, path)
	}
}

func TestResolveCommandChecksSharedBundledNativeToolDirs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX executable bits")
	}
	runtimeDir := t.TempDir()
	binDir := filepath.Join(runtimeDir, "native", "tools", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(binDir, "pdftoppm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(runtimeDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	t.Setenv("PATH", "")
	restore := swapExtraCommandDirs(func(string) []string { return nil })
	defer restore()

	got, ok := ResolveCommand("pdftoppm")
	if !ok {
		t.Fatal("ResolveCommand did not find executable in shared bundled native tool dir")
	}
	gotReal, _ := filepath.EvalSymlinks(got)
	wantReal, _ := filepath.EvalSymlinks(path)
	if gotReal != wantReal {
		t.Fatalf("ResolveCommand path = %q, want %q", got, path)
	}
}

func TestResolveCommandRejectsNonExecutableExtraDirCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not use POSIX executable bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write non-executable: %v", err)
	}
	t.Setenv("PATH", "")
	restore := swapExtraCommandDirs(func(string) []string { return []string{dir} })
	defer restore()

	if got, ok := ResolveCommand("gh"); ok {
		t.Fatalf("ResolveCommand found non-executable path %q", got)
	}
}

func TestDefaultWindowsExtraCommandDirsIncludeGitTools(t *testing.T) {
	t.Setenv("LOCALAPPDATA", `C:\Users\Alex\AppData\Local`)
	t.Setenv("ProgramFiles", `C:\Program Files`)
	t.Setenv("ProgramFiles(x86)", `C:\Program Files (x86)`)

	dirs := defaultExtraCommandDirs("windows")
	want := []string{
		filepath.Join(`C:\Users\Alex\AppData\Local`, "Programs", "Git", "cmd"),
		filepath.Join(`C:\Program Files`, "Git", "cmd"),
		filepath.Join(`C:\Program Files`, "GitHub CLI"),
	}
	for _, dir := range want {
		if !slices.Contains(dirs, dir) {
			t.Fatalf("defaultExtraCommandDirs(windows) missing %q in %#v", dir, dirs)
		}
	}
}

func TestRuntimeCommandDirsFromBasesIncludeNativeTools(t *testing.T) {
	base := filepath.Join(t.TempDir(), "runtime")
	dirs := runtimeCommandDirsFromBases("darwin", []string{base, base})
	want := []string{
		filepath.Join(base, "bin"),
		filepath.Join(base, "native", "tools", "bin"),
		filepath.Join(base, "native", "poppler", "bin"),
		filepath.Join(base, "native", "tesseract", "bin"),
		filepath.Join(base, "native", "search", "bin"),
	}
	for _, dir := range want {
		if !slices.Contains(dirs, dir) {
			t.Fatalf("runtimeCommandDirsFromBases missing %q in %#v", dir, dirs)
		}
	}
	if got, want := len(dirs), len(dedupeCommandDirs(dirs)); got != want {
		t.Fatalf("runtimeCommandDirsFromBases returned duplicate dirs: %#v", dirs)
	}
}

func swapExtraCommandDirs(fn commandDirsFunc) func() {
	old := extraCommandDirs
	extraCommandDirs = fn
	return func() {
		extraCommandDirs = old
	}
}
