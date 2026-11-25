package optionc

import (
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/config"
)

func TestBrowserControlEnablementFromSettings(t *testing.T) {
	settings := config.DefaultSettings()
	if browserControlEnabledFromSettings(settings) {
		t.Fatal("browser control should default to disabled")
	}
	enabled := true
	settings.BrowserControl.Enabled = &enabled
	if !browserControlEnabledFromSettings(settings) {
		t.Fatal("browser control should be enabled when the setting is true")
	}
}

func TestBrowserControlToolGate(t *testing.T) {
	if !isBrowserControlToolAllowedForRun("read_file", false) {
		t.Fatal("non-browser tools should not be blocked")
	}
	for _, name := range tools.BrowserControlToolNames() {
		if isBrowserControlToolAllowedForRun(name, false) {
			t.Fatalf("%s should be blocked when browser control is disabled", name)
		}
		if !isBrowserControlToolAllowedForRun(name, true) {
			t.Fatalf("%s should be allowed when browser control is enabled", name)
		}
	}
}

func TestEmbedBrowserControlGuidanceOnlyWhenEnabled(t *testing.T) {
	base := "base prompt"
	if got := embedBrowserControlGuidance(base, false); got != base {
		t.Fatalf("disabled guidance = %q, want base prompt", got)
	}
	got := embedBrowserControlGuidance(base, true)
	if !strings.Contains(got, "Computer-use browser tools:") || !strings.Contains(got, "cua_browser_snapshot") || !strings.Contains(got, "cua_browser_logs") {
		t.Fatalf("enabled guidance missing browser-control instructions: %q", got)
	}
}
