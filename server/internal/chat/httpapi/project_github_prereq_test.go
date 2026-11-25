package httpapi

import (
	"context"
	"testing"
)

func TestFirstNonEmptyLine(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"git version 2.43.0":        "git version 2.43.0",
		"\n\n  git version 2.43.0 ": "git version 2.43.0",
		"first\nsecond":             "first",
	}
	for in, want := range cases {
		if got := firstNonEmptyLine(in); got != want {
			t.Errorf("firstNonEmptyLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPackageInstallCommand(t *testing.T) {
	// The command is OS-specific; on every platform it must be non-empty for a
	// known tool name or deliberately empty (never panic / never include the
	// raw package twice).
	if cmd := packageInstallCommand("gh"); cmd != "" && !contains(cmd, "gh") {
		t.Errorf("packageInstallCommand(gh) = %q, expected it to reference gh", cmd)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// TestGitHubAuthPrerequisitesInvariants verifies the assembled prerequisite
// report is internally consistent regardless of what tools happen to be
// installed in the test environment.
func TestGitHubAuthPrerequisitesInvariants(t *testing.T) {
	h := &Handler{} // nil settings/secrets => no custom client ID, CLI path.
	prereq := h.githubAuthPrerequisites(context.Background())

	if prereq.Missing == nil {
		t.Fatal("Missing should be a non-nil slice so the JSON encodes [] not null")
	}
	if prereq.ClientIDConfigured {
		t.Fatal("expected no custom OAuth client ID with a zero-value Handler")
	}

	wantReady := prereq.Git.Installed && prereq.GH.Installed && prereq.GHAuthenticated
	if prereq.Ready != wantReady {
		t.Errorf("Ready = %v, want %v (git=%v gh=%v ghAuth=%v)",
			prereq.Ready, wantReady, prereq.Git.Installed, prereq.GH.Installed, prereq.GHAuthenticated)
	}

	for _, step := range prereq.Missing {
		if step.Key == "" || step.Title == "" || step.Instruction == "" {
			t.Errorf("missing step has empty fields: %+v", step)
		}
	}

	if prereq.Ready && len(prereq.Missing) != 0 {
		t.Errorf("Ready but %d missing steps reported", len(prereq.Missing))
	}
}
