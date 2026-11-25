package optionc

import (
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func TestArxivEnablementFromSettings(t *testing.T) {
	settings := config.DefaultSettings()
	if arxivEnabledFromSettings(&settings) {
		t.Fatal("arXiv should default to disabled")
	}

	enabled := true
	settings.Arxiv.Enabled = &enabled
	if !arxivEnabledFromSettings(&settings) {
		t.Fatal("arXiv should be enabled when enabled is true")
	}

	enabled = false
	settings.Arxiv.Enabled = &enabled
	if arxivEnabledFromSettings(&settings) {
		t.Fatal("arXiv should be disabled when enabled is false")
	}
}

func TestArxivToolGate(t *testing.T) {
	if !isArxivToolAllowedForRun("read_file", false) {
		t.Fatal("non-arXiv tools should not be blocked")
	}
	for _, name := range []string{
		"arxiv_status",
		"arxiv_check_connection",
		"arxiv_search",
		"arxiv_screening_report",
		"arxiv_recommendations",
		"arxiv_download_paper",
		"arxiv_read_paper",
		"arxiv_list_papers",
		"arxiv_get_paper",
		"arxiv_get_paper_text",
	} {
		if isArxivToolAllowedForRun(name, false) {
			t.Fatalf("%s should be blocked when arXiv is disabled", name)
		}
		if !isArxivToolAllowedForRun(name, true) {
			t.Fatalf("%s should be allowed when arXiv is enabled", name)
		}
	}
}

func TestFilterToolsByArxivEnabled(t *testing.T) {
	defs := []types.ToolDefinition{
		{Function: types.ToolFunctionDefinition{Name: "read_file"}},
		{Function: types.ToolFunctionDefinition{Name: "arxiv_search"}},
	}

	disabled := filterToolsByArxivEnabled(defs, false)
	if len(disabled) != 1 || disabled[0].Function.Name != "read_file" {
		t.Fatalf("disabled filter = %#v, want only read_file", disabled)
	}

	enabled := filterToolsByArxivEnabled(defs, true)
	if len(enabled) != 2 {
		t.Fatalf("enabled filter length = %d, want 2", len(enabled))
	}
}

func TestEmbedArxivGuidanceOnlyWhenEnabled(t *testing.T) {
	base := "base prompt"
	if got := embedArxivGuidance(base, false); got != base {
		t.Fatalf("disabled guidance = %q, want base prompt", got)
	}
	got := embedArxivGuidance(base, true)
	for _, needle := range []string{
		"arXiv tools:",
		"arxiv_search",
		"arxiv_screening_report",
		"arxiv_recommendations",
		"arxiv_download_paper",
		"arxiv_read_paper",
		"first-class file uploads",
		"untrusted external data",
		"Cite papers by arXiv ID",
	} {
		if !strings.Contains(got, needle) {
			t.Fatalf("enabled guidance missing %q: %q", needle, got)
		}
	}
}
