package tools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPrepareToolTestArgsInjectsDryRunAndPreservesArguments(t *testing.T) {
	meta, ok := ToolMetadataByName(GitHubToolCloneRepository)
	if !ok {
		t.Fatalf("missing metadata for %s", GitHubToolCloneRepository)
	}
	args, err := PrepareToolTestArgs(meta, json.RawMessage(`{"repository":"owner/repo","dry_run":false}`))
	if err != nil {
		t.Fatalf("PrepareToolTestArgs: %v", err)
	}
	decoded, err := DecodePreparedToolTestArgs(args)
	if err != nil {
		t.Fatalf("DecodePreparedToolTestArgs: %v", err)
	}
	if decoded["repository"] != "owner/repo" {
		t.Fatalf("repository=%#v want owner/repo", decoded["repository"])
	}
	if decoded["dry_run"] != true {
		t.Fatalf("dry_run=%#v want true", decoded["dry_run"])
	}
}

func TestPrepareToolTestArgsRejectsUnsupportedAndNonObjectArgs(t *testing.T) {
	unsupported := ToolMetadata{Name: "legacy_tool", Test: ToolTestSpec{Mode: ToolTestModeNone}}
	if _, err := PrepareToolTestArgs(unsupported, nil); err == nil || !strings.Contains(err.Error(), "does not support testing") {
		t.Fatalf("unsupported error=%v want unsupported message", err)
	}

	meta, ok := ToolMetadataByName("list_files")
	if !ok {
		t.Fatal("missing list_files metadata")
	}
	if _, err := PrepareToolTestArgs(meta, json.RawMessage(`[]`)); err == nil || !strings.Contains(err.Error(), "invalid test arguments") {
		t.Fatalf("non-object error=%v want invalid test arguments", err)
	}
	if _, err := PrepareToolTestArgs(meta, json.RawMessage(`null`)); err == nil || !strings.Contains(err.Error(), "must be a JSON object") {
		t.Fatalf("null error=%v want object error", err)
	}
}

func TestPrepareToolTestArgsRejectsMissingRequiredArguments(t *testing.T) {
	meta, ok := ToolMetadataByName(GitHubToolCloneRepository)
	if !ok {
		t.Fatalf("missing metadata for %s", GitHubToolCloneRepository)
	}
	_, err := PrepareToolTestArgs(meta, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected missing input error")
	}
	var inputErr *MissingToolTestInputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("error=%T %v, want MissingToolTestInputError", err, err)
	}
	if inputErr.ToolName != GitHubToolCloneRepository || !stringSliceContains(inputErr.MissingArguments, "repository") {
		t.Fatalf("missing input error=%#v", inputErr)
	}
	if inputErr.InputHint == "" {
		t.Fatalf("missing input error should include input hint: %#v", inputErr)
	}
}

func TestPrepareToolTestArgsAcceptsAlternativeArgumentGroups(t *testing.T) {
	sharepointMeta, ok := ToolMetadataByName(SharePointToolRead)
	if !ok {
		t.Fatalf("missing metadata for %s", SharePointToolRead)
	}
	sharepointArgs, err := PrepareToolTestArgs(sharepointMeta, json.RawMessage(`{"server_relative_url":"/sites/team/Shared Documents/file.docx"}`))
	if err != nil {
		t.Fatalf("SharePoint PrepareToolTestArgs: %v", err)
	}
	sharepointDecoded, err := DecodePreparedToolTestArgs(sharepointArgs)
	if err != nil {
		t.Fatalf("SharePoint DecodePreparedToolTestArgs: %v", err)
	}
	if sharepointDecoded["server_relative_url"] != "/sites/team/Shared Documents/file.docx" {
		t.Fatalf("server_relative_url=%#v", sharepointDecoded["server_relative_url"])
	}

	jiraMeta, ok := ToolMetadataByName("jira_update_issue")
	if !ok {
		t.Fatal("missing metadata for jira_update_issue")
	}
	jiraArgs, err := PrepareToolTestArgs(jiraMeta, json.RawMessage(`{"issueKey":"PROJ-123","fields":{"customfield_1":"value"},"dry_run":false}`))
	if err != nil {
		t.Fatalf("Jira PrepareToolTestArgs: %v", err)
	}
	jiraDecoded, err := DecodePreparedToolTestArgs(jiraArgs)
	if err != nil {
		t.Fatalf("Jira DecodePreparedToolTestArgs: %v", err)
	}
	if jiraDecoded["issueKey"] != "PROJ-123" {
		t.Fatalf("issueKey=%#v", jiraDecoded["issueKey"])
	}
	if jiraDecoded["dry_run"] != true {
		t.Fatalf("dry_run=%#v want true", jiraDecoded["dry_run"])
	}
	if fields, ok := jiraDecoded["fields"].(map[string]any); !ok || fields["customfield_1"] != "value" {
		t.Fatalf("fields=%#v", jiraDecoded["fields"])
	}

	clickMeta, ok := ToolMetadataByName(BrowserControlToolClick)
	if !ok {
		t.Fatalf("missing metadata for %s", BrowserControlToolClick)
	}
	clickArgs, err := PrepareToolTestArgs(clickMeta, json.RawMessage(`{"selector":"button.primary","dry_run":false}`))
	if err != nil {
		t.Fatalf("browser click PrepareToolTestArgs: %v", err)
	}
	clickDecoded, err := DecodePreparedToolTestArgs(clickArgs)
	if err != nil {
		t.Fatalf("browser click DecodePreparedToolTestArgs: %v", err)
	}
	if clickDecoded["selector"] != "button.primary" || clickDecoded["dry_run"] != true {
		t.Fatalf("browser click args=%#v", clickDecoded)
	}

	typeMeta, ok := ToolMetadataByName(BrowserControlToolType)
	if !ok {
		t.Fatalf("missing metadata for %s", BrowserControlToolType)
	}
	typeArgs, err := PrepareToolTestArgs(typeMeta, json.RawMessage(`{"text":"Email","value":"person@example.com","dry_run":false}`))
	if err != nil {
		t.Fatalf("browser type PrepareToolTestArgs: %v", err)
	}
	typeDecoded, err := DecodePreparedToolTestArgs(typeArgs)
	if err != nil {
		t.Fatalf("browser type DecodePreparedToolTestArgs: %v", err)
	}
	if typeDecoded["text"] != "Email" || typeDecoded["value"] != "person@example.com" || typeDecoded["dry_run"] != true {
		t.Fatalf("browser type args=%#v", typeDecoded)
	}

	slackUploadMeta, ok := ToolMetadataByName(SlackToolUploadFile)
	if !ok {
		t.Fatalf("missing metadata for %s", SlackToolUploadFile)
	}
	slackUploadArgs, err := PrepareToolTestArgs(slackUploadMeta, json.RawMessage(`{"channel_id":"C123","artifact_id":"artifact-123","dry_run":false}`))
	if err != nil {
		t.Fatalf("Slack upload PrepareToolTestArgs: %v", err)
	}
	slackUploadDecoded, err := DecodePreparedToolTestArgs(slackUploadArgs)
	if err != nil {
		t.Fatalf("Slack upload DecodePreparedToolTestArgs: %v", err)
	}
	if slackUploadDecoded["channel_id"] != "C123" || slackUploadDecoded["artifact_id"] != "artifact-123" || slackUploadDecoded["dry_run"] != true {
		t.Fatalf("Slack upload args=%#v", slackUploadDecoded)
	}

	jiraUploadMeta, ok := ToolMetadataByName("jira_upload_attachment")
	if !ok {
		t.Fatal("missing metadata for jira_upload_attachment")
	}
	jiraUploadArgs, err := PrepareToolTestArgs(jiraUploadMeta, json.RawMessage(`{"issueKey":"PROJ-123","fileName":"report.txt","contentBase64":"aGVsbG8=","dry_run":false}`))
	if err != nil {
		t.Fatalf("Jira upload PrepareToolTestArgs: %v", err)
	}
	jiraUploadDecoded, err := DecodePreparedToolTestArgs(jiraUploadArgs)
	if err != nil {
		t.Fatalf("Jira upload DecodePreparedToolTestArgs: %v", err)
	}
	if jiraUploadDecoded["issueKey"] != "PROJ-123" || jiraUploadDecoded["fileName"] != "report.txt" || jiraUploadDecoded["contentBase64"] != "aGVsbG8=" || jiraUploadDecoded["dry_run"] != true {
		t.Fatalf("Jira upload args=%#v", jiraUploadDecoded)
	}

	displayMeta, ok := ToolMetadataByName("display_table")
	if !ok {
		t.Fatal("missing metadata for display_table")
	}
	displayArgs, err := PrepareToolTestArgs(displayMeta, json.RawMessage(`{"source":{"artifact_id":"artifact-123"}}`))
	if err != nil {
		t.Fatalf("display_table PrepareToolTestArgs: %v", err)
	}
	displayDecoded, err := DecodePreparedToolTestArgs(displayArgs)
	if err != nil {
		t.Fatalf("display_table DecodePreparedToolTestArgs: %v", err)
	}
	if source, ok := displayDecoded["source"].(map[string]any); !ok || source["artifact_id"] != "artifact-123" {
		t.Fatalf("display_table args=%#v", displayDecoded)
	}

	jiraFindMeta, ok := ToolMetadataByName("jira_find")
	if !ok {
		t.Fatal("missing metadata for jira_find")
	}
	jiraFindArgs, err := PrepareToolTestArgs(jiraFindMeta, json.RawMessage(`{"projectOrBoard":"ENG"}`))
	if err != nil {
		t.Fatalf("jira_find PrepareToolTestArgs: %v", err)
	}
	jiraFindDecoded, err := DecodePreparedToolTestArgs(jiraFindArgs)
	if err != nil {
		t.Fatalf("jira_find DecodePreparedToolTestArgs: %v", err)
	}
	if jiraFindDecoded["projectOrBoard"] != "ENG" {
		t.Fatalf("jira_find args=%#v", jiraFindDecoded)
	}
}

func TestPrepareToolTestArgsRejectsMissingAlternativeArgumentGroup(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{name: SharePointToolRead, raw: json.RawMessage(`{}`), want: "item_url|server_relative_url"},
		{name: "jira_update_issue", raw: json.RawMessage(`{"issueKey":"PROJ-123"}`), want: "summary|description|clearDescription|issueType|assigneeAccountId|priorityName|labels|components|fields"},
		{name: BrowserControlToolClick, raw: json.RawMessage(`{}`), want: "index|selector|text"},
		{name: BrowserControlToolType, raw: json.RawMessage(`{"value":"hello"}`), want: "index|selector|text"},
		{name: SlackToolUploadFile, raw: json.RawMessage(`{"channel_id":"C123"}`), want: "artifact_id|path|local_path|fileName|contentText|contentBase64"},
		{name: "jira_upload_attachment", raw: json.RawMessage(`{"issueKey":"PROJ-123"}`), want: "artifact_id|path|local_path|fileName|contentText|contentBase64"},
		{name: "display_table", raw: json.RawMessage(`{}`), want: "data_json|source"},
		{name: "jira_find", raw: json.RawMessage(`{}`), want: "query|projectOrBoard"},
	}
	for _, tc := range cases {
		meta, ok := ToolMetadataByName(tc.name)
		if !ok {
			t.Fatalf("missing metadata for %s", tc.name)
		}
		_, err := PrepareToolTestArgs(meta, tc.raw)
		if err == nil {
			t.Fatalf("%s expected missing input error", tc.name)
		}
		var inputErr *MissingToolTestInputError
		if !errors.As(err, &inputErr) {
			t.Fatalf("%s error=%T %v, want MissingToolTestInputError", tc.name, err, err)
		}
		if len(inputErr.RequiredArgumentGroups) == 0 {
			t.Fatalf("%s missing required argument groups in error: %#v", tc.name, inputErr)
		}
		for _, part := range strings.Split(tc.want, "|") {
			if !strings.Contains(strings.Join(inputErr.MissingArguments, "|"), part) {
				t.Fatalf("%s missing arguments=%v, want part %q", tc.name, inputErr.MissingArguments, part)
			}
		}
	}
}

func TestPrepareToolTestArgsAcceptsAggregateDatasetDefaultMetrics(t *testing.T) {
	meta, ok := ToolMetadataByName("aggregate_dataset")
	if !ok {
		t.Fatal("missing metadata for aggregate_dataset")
	}
	args, err := PrepareToolTestArgs(meta, json.RawMessage(`{"artifact_id":"artifact-123"}`))
	if err != nil {
		t.Fatalf("PrepareToolTestArgs: %v", err)
	}
	decoded, err := DecodePreparedToolTestArgs(args)
	if err != nil {
		t.Fatalf("DecodePreparedToolTestArgs: %v", err)
	}
	if decoded["artifact_id"] != "artifact-123" {
		t.Fatalf("prepared args=%#v", decoded)
	}
	if _, ok := decoded["metrics"]; ok {
		t.Fatalf("metrics should not be required or injected: %#v", decoded)
	}
}

func TestPrepareToolTestArgsMatchesEverySupportedTool(t *testing.T) {
	for _, meta := range AllToolMetadata() {
		if !meta.Test.Supported {
			continue
		}
		args, err := PrepareToolTestArgs(meta, toolTestRunnerArgs(meta, nil))
		if err != nil {
			t.Fatalf("%s PrepareToolTestArgs: %v", meta.Name, err)
		}
		decoded, err := DecodePreparedToolTestArgs(args)
		if err != nil {
			t.Fatalf("%s DecodePreparedToolTestArgs: %v", meta.Name, err)
		}
		for _, name := range meta.Test.RequiredArguments {
			if toolTestArgMissing(decoded[name]) {
				t.Fatalf("%s prepared args missing required argument %q: %#v", meta.Name, name, decoded)
			}
		}
		if len(meta.Test.RequiredArgumentGroups) > 0 {
			group := meta.Test.RequiredArgumentGroups[0]
			for _, name := range group {
				if toolTestArgMissing(decoded[name]) {
					t.Fatalf("%s prepared args missing first alternative-group argument %q from %v: %#v", meta.Name, name, group, decoded)
				}
			}
		}
		if meta.Test.Mode == ToolTestModeDryRun && decoded["dry_run"] != true {
			t.Fatalf("%s dry_run=%#v want true", meta.Name, decoded["dry_run"])
		}
	}
}

func TestPrepareToolTestArgsAcceptsEveryAlternativeArgumentGroup(t *testing.T) {
	for _, meta := range AllToolMetadata() {
		if !meta.Test.Supported || len(meta.Test.RequiredArgumentGroups) == 0 {
			continue
		}
		for idx, group := range meta.Test.RequiredArgumentGroups {
			group := append([]string(nil), group...)
			t.Run(meta.Name+"/"+strings.Join(group, "+"), func(t *testing.T) {
				rawArgs := map[string]any{}
				for _, name := range meta.Test.RequiredArguments {
					rawArgs[name] = toolTestRunnerValue(meta, name)
				}
				for _, name := range group {
					rawArgs[name] = toolTestRunnerValue(meta, name)
				}
				input, err := json.Marshal(rawArgs)
				if err != nil {
					t.Fatalf("%s group %d marshal args: %v", meta.Name, idx, err)
				}
				args, err := PrepareToolTestArgs(meta, json.RawMessage(input))
				if err != nil {
					t.Fatalf("%s group %d %v PrepareToolTestArgs: %v", meta.Name, idx, group, err)
				}
				decoded, err := DecodePreparedToolTestArgs(args)
				if err != nil {
					t.Fatalf("%s group %d DecodePreparedToolTestArgs: %v", meta.Name, idx, err)
				}
				for _, name := range meta.Test.RequiredArguments {
					if toolTestArgMissing(decoded[name]) {
						t.Fatalf("%s group %d missing required argument %q: %#v", meta.Name, idx, name, decoded)
					}
				}
				for _, name := range group {
					if toolTestArgMissing(decoded[name]) {
						t.Fatalf("%s group %d missing alternative argument %q from %v: %#v", meta.Name, idx, name, group, decoded)
					}
				}
				if meta.Test.Mode == ToolTestModeDryRun && decoded["dry_run"] != true {
					t.Fatalf("%s group %d dry_run=%#v want true", meta.Name, idx, decoded["dry_run"])
				}
			})
		}
	}
}

func TestPrepareToolTestArgsMatchesEveryDryRunTool(t *testing.T) {
	for _, meta := range AllToolMetadata() {
		if meta.Test.Mode != ToolTestModeDryRun {
			continue
		}
		args, err := PrepareToolTestArgs(meta, toolTestRunnerArgs(meta, map[string]any{"dry_run": false}))
		if err != nil {
			t.Fatalf("%s PrepareToolTestArgs: %v", meta.Name, err)
		}
		decoded, err := DecodePreparedToolTestArgs(args)
		if err != nil {
			t.Fatalf("%s DecodePreparedToolTestArgs: %v", meta.Name, err)
		}
		if decoded["dry_run"] != true {
			t.Fatalf("%s dry_run=%#v want true", meta.Name, decoded["dry_run"])
		}
	}
}

func toolTestRunnerArgs(meta ToolMetadata, base map[string]any) json.RawMessage {
	if base == nil {
		base = map[string]any{}
	}
	for _, name := range meta.Test.RequiredArguments {
		if _, ok := base[name]; ok {
			continue
		}
		base[name] = toolTestRunnerValue(meta, name)
	}
	if len(meta.Test.RequiredArgumentGroups) > 0 {
		for _, name := range meta.Test.RequiredArgumentGroups[0] {
			if _, ok := base[name]; ok {
				continue
			}
			base[name] = toolTestRunnerValue(meta, name)
		}
	}
	b, err := json.Marshal(base)
	if err != nil {
		panic(err)
	}
	return b
}

func toolTestRunnerValue(meta ToolMetadata, name string) any {
	if meta.Test.SampleArguments != nil {
		if value, ok := meta.Test.SampleArguments[name]; ok {
			return value
		}
	}
	switch name {
	case "args", "authors", "categories", "ids", "labels", "components", "metrics", "relatedIssueKeys":
		return []string{"test-" + name}
	case "chart":
		return map[string]any{"kind": "bar", "x": "name", "y": "value"}
	case "source":
		return map[string]any{"artifact_id": "artifact-123"}
	case "fields":
		return map[string]any{"test": name}
	case "clearDescription", "recursive":
		return true
	default:
		return "test-" + name
	}
}
