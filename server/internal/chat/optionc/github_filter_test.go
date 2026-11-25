package optionc

import (
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func TestGitHubEnablementFromSettings(t *testing.T) {
	settings := config.DefaultSettings()
	if githubEnabledFromSettings(&settings) {
		t.Fatal("GitHub should default to disabled")
	}

	enabled := true
	readTools := true
	settings.GitHub.Enabled = &enabled
	settings.GitHub.ReadTools = nil
	if !githubEnabledFromSettings(&settings) {
		t.Fatal("GitHub should be enabled when enabled is true and readTools is unset")
	}

	readTools = false
	settings.GitHub.ReadTools = &readTools
	if githubEnabledFromSettings(&settings) {
		t.Fatal("GitHub should be disabled when readTools is explicitly false")
	}

	readTools = true
	settings.GitHub.ReadTools = &readTools
	if !githubEnabledFromSettings(&settings) {
		t.Fatal("GitHub should be enabled when enabled and readTools are true")
	}
}

func TestGitHubToolGate(t *testing.T) {
	if !isGitHubToolAllowedForRun("read_file", false) {
		t.Fatal("non-GitHub tools should not be blocked")
	}
	for _, name := range []string{
		"github_status",
		"github_check_connection",
		"github_list_repositories",
		"github_list_pull_requests",
		"github_get_pull_request",
		"github_list_issues",
		"github_get_issue",
		"github_list_workflow_runs",
		"github_get_workflow_run_jobs",
		"github_clone_repository",
		"github_pull_repository",
		"github_refresh_repository",
		"github_run_gh_command",
	} {
		if isGitHubToolAllowedForRun(name, false) {
			t.Fatalf("%s should be blocked when GitHub is disabled", name)
		}
		if !isGitHubToolAllowedForRun(name, true) {
			t.Fatalf("%s should be allowed when GitHub is enabled", name)
		}
	}
}

func TestFilterToolsByGitHubEnabled(t *testing.T) {
	defs := []types.ToolDefinition{
		{Function: types.ToolFunctionDefinition{Name: "read_file"}},
		{Function: types.ToolFunctionDefinition{Name: "github_status"}},
	}

	disabled := filterToolsByGitHubEnabled(defs, false)
	if len(disabled) != 1 || disabled[0].Function.Name != "read_file" {
		t.Fatalf("disabled filter = %#v, want only read_file", disabled)
	}

	enabled := filterToolsByGitHubEnabled(defs, true)
	if len(enabled) != 2 {
		t.Fatalf("enabled filter length = %d, want 2", len(enabled))
	}
}

func TestEmbedGitHubGuidanceOnlyWhenEnabled(t *testing.T) {
	base := "base prompt"
	if got := embedGitHubGuidance(base, false); got != base {
		t.Fatalf("disabled guidance = %q, want base prompt", got)
	}
	got := embedGitHubGuidance(base, true)
	if !strings.Contains(got, "GitHub tools:") ||
		!strings.Contains(got, "managed git subprocesses") ||
		!strings.Contains(got, "isolated gh") ||
		!strings.Contains(got, "Never request, reveal, paste, or expose GitHub tokens") ||
		!strings.Contains(got, "only queue approvals") ||
		!strings.Contains(got, "partial clone") {
		t.Fatalf("enabled guidance missing GitHub instructions: %q", got)
	}
}
