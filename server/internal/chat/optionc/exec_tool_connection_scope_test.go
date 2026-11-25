package optionc

import (
	"context"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

// Exec-time connection gating must honor the run-start scoped values passed
// into execTool, NOT live global settings. Regression: execTool used to
// re-derive GitHub/Slack/Atlassian/Browser enablement from m.settings.Get()
// per call, so a run whose connection scope excluded those families could
// still execute their tools (and mid-run settings edits leaked into
// in-flight runs) while every other connector honored the run scope.
func TestExecToolConnectionGatesUseRunScopedValuesNotGlobalSettings(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	on := true
	jiraBaseURL := "https://example.atlassian.net"
	confluenceBaseURL := "https://example.atlassian.net/wiki"
	enabledGlobals := settingsStore.Get()
	enabledGlobals.GitHub = &config.GitHubSettings{Enabled: &on, ReadTools: &on}
	enabledGlobals.Slack = &config.SlackSettings{Enabled: &on, ReadTools: &on, WriteTools: &on}
	enabledGlobals.Atlassian = &config.AtlassianSettings{
		JiraBaseURL:       &jiraBaseURL,
		ConfluenceBaseURL: &confluenceBaseURL,
		Jira:              &config.AtlassianProductSettings{Enabled: &on},
		Confluence:        &config.AtlassianProductSettings{Enabled: &on},
	}
	enabledGlobals.BrowserControl = &config.BrowserControlSettings{Enabled: &on}
	if err := settingsStore.Save(enabledGlobals); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "fake", "fake-model", nil, "system", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Globals all-enabled: the pre-fix code would have allowed excluded calls.
	managerGlobalsOn := &Manager{store: st, tools: tools.NewRegistry(nil), settings: settingsStore, cfgDir: cfgDir}
	// No settings service at all: the pre-fix code would have refused included
	// calls (its live re-read yielded zero values).
	managerNoSettings := &Manager{store: st, tools: tools.NewRegistry(nil), cfgDir: cfgDir}

	callTool := func(m *Manager, name string, atlassian atlassianEnablement, browser, github bool, slack slackToolAccess) string {
		t.Helper()
		out, err := m.execTool(
			ctx, thread, run, testProjectID(""),
			types.ToolCall{ID: "tc_scope_" + name, Name: name},
			false, "shallow", 0, nil,
			nil, defaultChartEngineEnablement(), false, false, true, true, false,
			atlassian, browser, github, slack,
		)
		if err != nil {
			t.Fatalf("execTool(%s): %v", name, err)
		}
		if out.Content == nil {
			t.Fatalf("execTool(%s): no envelope content", name)
		}
		return *out.Content
	}

	cases := []struct {
		family string
		tool   string
	}{
		{"github", tools.GitHubToolStatus},
		{"slack", tools.SlackToolListConversations},
		{"atlassian", "jira_get_issue"},
		{"browser", tools.BrowserControlToolNavigate},
	}

	for _, tc := range cases {
		t.Run(tc.family+" excluded by run scope is refused despite enabled globals", func(t *testing.T) {
			content := callTool(managerGlobalsOn, tc.tool, atlassianEnablement{}, false, false, slackToolAccess{})
			if !strings.Contains(content, "tool is disabled for this run") {
				t.Fatalf("expected run-scope refusal for %s, got: %s", tc.tool, content)
			}
		})
		t.Run(tc.family+" included by run scope passes the gate without reading globals", func(t *testing.T) {
			content := callTool(
				managerNoSettings, tc.tool,
				atlassianEnablement{Jira: true, Confluence: true}, true, true,
				slackToolAccess{Read: true, Write: true},
			)
			if strings.Contains(content, "tool is disabled for this run") {
				t.Fatalf("run-scope-included %s was refused by the connection gate: %s", tc.tool, content)
			}
		})
	}
}
