package optionc

import (
	"testing"

	"ai-analyst-local/internal/chat/tools"
)

func TestApplyRunActiveConnectionScopeRestrictsEnabledFamilies(t *testing.T) {
	datasources := map[string]bool{
		"bigquery":   true,
		"databricks": true,
		"trino":      true,
		"snowflake":  true,
	}
	atlassian := atlassianEnablement{Jira: true, Confluence: true}
	slack := slackToolAccess{Read: true, Write: true}

	scopedDatasources, scopedAtlassian, scopedBrowser, scopedSharePoint, scopedGitHub, scopedSlack, scopedArxiv, scopedThreadSearch, scopedLocalComputer := applyRunActiveConnectionScope(
		datasources,
		atlassian,
		true,
		true,
		true,
		slack,
		true,
		true,
		true,
		[]string{"github", "databricks", "thread-search", "local-computer"},
	)

	if scopedDatasources["databricks"] != true {
		t.Fatalf("databricks should remain enabled")
	}
	for _, name := range []string{"bigquery", "trino", "snowflake"} {
		if scopedDatasources[name] {
			t.Fatalf("%s should be disabled by run scope", name)
		}
	}
	if scopedAtlassian.Jira || scopedAtlassian.Confluence {
		t.Fatalf("atlassian should be disabled by run scope: %+v", scopedAtlassian)
	}
	if scopedBrowser {
		t.Fatal("browser should be disabled by run scope")
	}
	if scopedSharePoint {
		t.Fatal("sharepoint should be disabled by run scope")
	}
	if !scopedGitHub {
		t.Fatal("github should remain enabled")
	}
	if scopedSlack.Read || scopedSlack.Write {
		t.Fatalf("slack should be disabled by run scope: %+v", scopedSlack)
	}
	if scopedArxiv {
		t.Fatal("arxiv should be disabled by run scope")
	}
	if !scopedThreadSearch {
		t.Fatal("thread search should remain enabled")
	}
	if !scopedLocalComputer {
		t.Fatal("local computer should remain enabled")
	}
}

func TestNewRunConnectionScopeRecognizesToolConnections(t *testing.T) {
	scope := newRunConnectionScope([]string{"Image_Generation", "web search", "PYTHON-SANDBOX"})
	for _, id := range []string{"image-generation", "web-search", "python-sandbox"} {
		if !scope[id] {
			t.Fatalf("%s should be recognized in the run connection scope: %#v", id, scope)
		}
	}
}

func TestApplyRunActiveConnectionScopeEmptyDisablesConnectionTools(t *testing.T) {
	scopedDatasources, scopedAtlassian, scopedBrowser, scopedSharePoint, scopedGitHub, scopedSlack, scopedArxiv, scopedThreadSearch, scopedLocalComputer := applyRunActiveConnectionScope(
		map[string]bool{"bigquery": true, "databricks": true},
		atlassianEnablement{Jira: true, Confluence: true},
		true,
		true,
		true,
		slackToolAccess{Read: true, Write: true},
		true,
		true,
		true,
		nil,
	)

	for name, enabled := range scopedDatasources {
		if enabled {
			t.Fatalf("%s should be disabled by empty run scope", name)
		}
	}
	if scopedAtlassian.Jira || scopedAtlassian.Confluence || scopedBrowser || scopedSharePoint || scopedGitHub || scopedSlack.Read || scopedSlack.Write || scopedArxiv || scopedThreadSearch || scopedLocalComputer {
		t.Fatalf("all non-datasource connection tools should be disabled, got atlassian=%+v browser=%v sharepoint=%v github=%v slack=%+v arxiv=%v threadSearch=%v localComputer=%v", scopedAtlassian, scopedBrowser, scopedSharePoint, scopedGitHub, scopedSlack, scopedArxiv, scopedThreadSearch, scopedLocalComputer)
	}
}

func TestApplyRunActiveConnectionScopeKeepsOnlyGloballyEnabledTools(t *testing.T) {
	scopedDatasources, scopedAtlassian, scopedBrowser, scopedSharePoint, scopedGitHub, scopedSlack, scopedArxiv, scopedThreadSearch, scopedLocalComputer := applyRunActiveConnectionScope(
		map[string]bool{"bigquery": false, "databricks": true},
		atlassianEnablement{Jira: false, Confluence: true},
		false,
		true,
		true,
		slackToolAccess{Read: true, Write: false},
		true,
		false,
		false,
		[]string{"google-cloud", "atlassian", "browser", "sharepoint", "github", "slack", "arxiv", "search-threads", "local-computer"},
	)

	if scopedDatasources["bigquery"] {
		t.Fatal("bigquery should remain disabled when globally unavailable")
	}
	if scopedDatasources["databricks"] {
		t.Fatal("databricks should be disabled when omitted from run scope")
	}
	if scopedAtlassian.Jira {
		t.Fatal("jira should remain disabled when globally unavailable")
	}
	if !scopedAtlassian.Confluence {
		t.Fatal("confluence should remain enabled")
	}
	if scopedBrowser {
		t.Fatal("browser should remain disabled when globally unavailable")
	}
	if !scopedSharePoint {
		t.Fatal("sharepoint should remain enabled")
	}
	if !scopedGitHub {
		t.Fatal("github should remain enabled")
	}
	if !scopedSlack.Read || scopedSlack.Write {
		t.Fatalf("slack should preserve global read/write access, got %+v", scopedSlack)
	}
	if !scopedArxiv {
		t.Fatal("arxiv should remain enabled")
	}
	if scopedThreadSearch {
		t.Fatal("thread search should remain disabled when globally unavailable")
	}
	if scopedLocalComputer {
		t.Fatal("local computer should remain disabled when globally unavailable")
	}
}

func TestIsLocalComputerToolAllowedForRun(t *testing.T) {
	for _, name := range []string{"file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page", "local_list_grant_folder", tools.LocalSearchCommandToolName, "local_search_in_grant", "local_read_grant_file"} {
		if isLocalComputerToolAllowedForRun(name, false) {
			t.Fatalf("%s should be disabled when local computer is not armed", name)
		}
		if !isLocalComputerToolAllowedForRun(name, true) {
			t.Fatalf("%s should be enabled when local computer is armed", name)
		}
	}
	if !isLocalComputerToolAllowedForRun("list_files", false) {
		t.Fatal("legacy project snapshot tools should not be controlled by the local-computer connection")
	}
}
