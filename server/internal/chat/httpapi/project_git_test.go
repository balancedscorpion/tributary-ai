package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeGitHubRepoURL(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantURL  string
		wantName string
		wantErr  bool
	}{
		{
			name:     "scp style ssh",
			in:       "git@github.com:owner/repo.git",
			wantURL:  "git@github.com:owner/repo.git",
			wantName: "repo",
		},
		{
			name:     "https without git suffix",
			in:       "https://github.com/owner/repo",
			wantURL:  "https://github.com/owner/repo.git",
			wantName: "repo",
		},
		{
			name:     "owner repo shorthand",
			in:       "owner/repo",
			wantURL:  "https://github.com/owner/repo.git",
			wantName: "repo",
		},
		{
			name:     "ssh url",
			in:       "ssh://git@github.com/owner/repo.git",
			wantURL:  "ssh://git@github.com/owner/repo.git",
			wantName: "repo",
		},
		{
			name:    "reject credentials",
			in:      "https://token@github.com/owner/repo.git",
			wantErr: true,
		},
		{
			name:    "reject local path",
			in:      "../repo",
			wantErr: true,
		},
		{
			name:    "reject non github host",
			in:      "https://example.com/owner/repo.git",
			wantErr: true,
		},
		{
			name:    "reject option shaped url",
			in:      "--upload-pack=touch",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotName, err := normalizeGitHubRepoURL(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeGitHubRepoURL(%q) error = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeGitHubRepoURL(%q) error = %v", tt.in, err)
			}
			if gotURL != tt.wantURL || gotName != tt.wantName {
				t.Fatalf("normalizeGitHubRepoURL(%q) = (%q, %q), want (%q, %q)", tt.in, gotURL, gotName, tt.wantURL, tt.wantName)
			}
		})
	}
}

func TestGitHubHTTPSRepoURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{
			name: "scp style ssh",
			in:   "git@github.com:owner/repo.git",
			want: "https://github.com/owner/repo.git",
			ok:   true,
		},
		{
			name: "ssh url",
			in:   "ssh://git@github.com/owner/repo.git",
			want: "https://github.com/owner/repo.git",
			ok:   true,
		},
		{
			name: "https url",
			in:   "https://github.com/owner/repo.git",
			want: "https://github.com/owner/repo.git",
			ok:   true,
		},
		{
			name: "non github",
			in:   "https://example.com/owner/repo.git",
			ok:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := githubHTTPSRepoURL(tt.in)
			if ok != tt.ok {
				t.Fatalf("githubHTTPSRepoURL(%q) ok = %v, want %v", tt.in, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("githubHTTPSRepoURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseGhActiveAccount(t *testing.T) {
	out := `github.com
  ✓ Logged in to github.com account previous-user (keyring)
  - Active account: false

  ✓ Logged in to github.com account example-account (keyring)
  - Active account: true
`
	if got := parseGhActiveAccount(out); got != "example-account" {
		t.Fatalf("parseGhActiveAccount() = %q, want example-account", got)
	}
}

func TestParseGitProgressLine(t *testing.T) {
	tests := []struct {
		name        string
		line        string
		wantStage   string
		wantPercent int
		wantCurrent int
		wantTotal   int
		wantUnit    string
	}{
		{
			name:        "receiving objects",
			line:        "Receiving objects:  42% (21/50), 1.23 MiB | 2.00 MiB/s",
			wantStage:   "Receiving objects",
			wantPercent: 42,
			wantCurrent: 21,
			wantTotal:   50,
			wantUnit:    "objects",
		},
		{
			name:        "updating files",
			line:        "Updating files: 100% (7/7), done.",
			wantStage:   "Updating files",
			wantPercent: 100,
			wantCurrent: 7,
			wantTotal:   7,
			wantUnit:    "files",
		},
		{
			name:        "resolving deltas",
			line:        "Resolving deltas:  5% (3/60)",
			wantStage:   "Resolving deltas",
			wantPercent: 5,
			wantCurrent: 3,
			wantTotal:   60,
			wantUnit:    "deltas",
		},
		{
			name:        "remote compressing objects",
			line:        "remote: Compressing objects:  88% (44/50), done.",
			wantStage:   "Compressing objects",
			wantPercent: 88,
			wantCurrent: 44,
			wantTotal:   50,
			wantUnit:    "objects",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseGitProgressLine(tt.line)
			if !ok {
				t.Fatalf("parseGitProgressLine(%q) ok = false, want true", tt.line)
			}
			if got.Stage != tt.wantStage || got.Percent != tt.wantPercent || got.Current != tt.wantCurrent || got.Total != tt.wantTotal || got.Unit != tt.wantUnit {
				t.Fatalf("parseGitProgressLine(%q) = %#v, want stage=%q percent=%d current=%d total=%d unit=%q", tt.line, got, tt.wantStage, tt.wantPercent, tt.wantCurrent, tt.wantTotal, tt.wantUnit)
			}
		})
	}

	if got, ok := parseGitProgressLine("remote: Enumerating objects: 573, done."); ok {
		t.Fatalf("parseGitProgressLine(non-percent line) = %#v, true; want false", got)
	}
}

func TestSanitizeProjectSourceNameWindowsReserved(t *testing.T) {
	tests := map[string]string{
		"CON":       "CON-repo",
		"nul.git":   "nul-repo",
		"COM1.txt":  "COM1-repo.txt",
		"LPT9.log":  "LPT9-repo.log",
		"repo.":     "repo",
		"repo name": "repo-name",
		"":          "source",
	}
	for in, want := range tests {
		if got := sanitizeProjectSourceName(in); got != want {
			t.Fatalf("sanitizeProjectSourceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteGitAskpassForOS(t *testing.T) {
	tmp := t.TempDir()
	windowsPath, err := writeGitAskpassForOS(tmp, "windows")
	if err != nil {
		t.Fatalf("writeGitAskpassForOS(windows): %v", err)
	}
	if filepath.Ext(windowsPath) != ".cmd" {
		t.Fatalf("windows askpass path = %q, want .cmd extension", windowsPath)
	}
	windowsScript, err := os.ReadFile(windowsPath)
	if err != nil {
		t.Fatalf("read windows askpass: %v", err)
	}
	if !strings.Contains(string(windowsScript), "AIA_GITHUB_TOKEN") || !strings.Contains(string(windowsScript), "x-access-token") {
		t.Fatalf("windows askpass script missing expected credentials handling: %s", windowsScript)
	}

	posixPath, err := writeGitAskpassForOS(tmp, "darwin")
	if err != nil {
		t.Fatalf("writeGitAskpassForOS(darwin): %v", err)
	}
	if filepath.Ext(posixPath) != ".sh" {
		t.Fatalf("posix askpass path = %q, want .sh extension", posixPath)
	}
}

func TestGitArgsWithOAuthEnvUsesPortableHooksPath(t *testing.T) {
	args := gitArgsWithOAuthEnv([]string{"GIT_ASKPASS=/tmp/askpass"}, "clone", "repo")
	joined := strings.Join(args, "\x00")
	if strings.Contains(joined, "/dev/null") {
		t.Fatalf("git args use POSIX-only hooks path: %#v", args)
	}
	if !strings.Contains(joined, "credential.helper=") || !strings.Contains(joined, "core.hooksPath=") {
		t.Fatalf("git args missing auth safety config: %#v", args)
	}
}

func TestNormalizeLocalSourcePathRejectsRelative(t *testing.T) {
	if _, err := normalizeLocalSourcePath("../repo"); err == nil {
		t.Fatal("normalizeLocalSourcePath() error = nil, want error")
	}
}

func TestRunGitCommandUsesExplicitWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake git is POSIX-only")
	}
	binDir := t.TempDir()
	writeFakeGitWorkingDir(t, filepath.Join(binDir, "git"))
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	workDir := t.TempDir()
	out, err := runGitCommandInDir(context.Background(), workDir, "status")
	if err != nil {
		t.Fatalf("runGitCommandInDir: %v\noutput=%s", err, out)
	}
	if !sameDirectory(t, strings.TrimSpace(out), workDir) {
		t.Fatalf("git cwd = %q, want %q", strings.TrimSpace(out), workDir)
	}
}

func writeFakeGitWorkingDir(t *testing.T, path string) {
	t.Helper()
	script := "#!/bin/sh\npwd -P\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
}

func sameDirectory(t *testing.T, got string, want string) bool {
	t.Helper()
	gotEval, gotErr := filepath.EvalSymlinks(got)
	wantEval, wantErr := filepath.EvalSymlinks(want)
	if gotErr == nil && wantErr == nil {
		got = gotEval
		want = wantEval
	}
	return filepath.Clean(got) == filepath.Clean(want)
}
