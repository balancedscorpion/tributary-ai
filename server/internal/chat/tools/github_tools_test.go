package tools

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestGitHubToolNamesAndLookup(t *testing.T) {
	names := GitHubToolNames()
	if len(names) != 13 {
		t.Fatalf("len(GitHubToolNames())=%d, want 13", len(names))
	}

	seen := map[string]bool{}
	for _, name := range names {
		if name == "" {
			t.Fatal("GitHubToolNames contains empty name")
		}
		if seen[name] {
			t.Fatalf("GitHubToolNames contains duplicate %q", name)
		}
		seen[name] = true
		if !IsGitHubToolName(name) {
			t.Fatalf("IsGitHubToolName(%q)=false, want true", name)
		}
	}
	if IsGitHubToolName("github_delete_repository") {
		t.Fatal("IsGitHubToolName accepted a write-style tool")
	}

	names[0] = "mutated"
	if GitHubToolNames()[0] == "mutated" {
		t.Fatal("GitHubToolNames should return a defensive copy")
	}
}

func TestGitHubToolsUseFirstClassResultMetadata(t *testing.T) {
	raw, err := os.ReadFile("github_tools.go")
	if err != nil {
		t.Fatalf("ReadFile github_tools.go: %v", err)
	}
	if strings.Contains(string(raw), `"result_metadata"`) {
		t.Fatal("GitHub tools should use Output.ResultMetadata instead of nested result_metadata payloads")
	}
}

func requireGitHubResultMetadata(t *testing.T, out Output, toolName, widget string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("result should not contain nested result_metadata: %#v", result)
		}
	}
	meta := out.ResultMetadata
	if _, nested := meta["result_metadata"]; nested {
		t.Fatalf("ResultMetadata should not contain nested result_metadata: %#v", meta)
	}
	requireGitHubMetadataFields(t, meta, toolName, widget)
	return meta
}

func requireGitHubMetadataFields(t *testing.T, meta map[string]any, toolName, widget string) {
	t.Helper()
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q) missing", toolName)
	}
	expected := map[string]any{
		"integration":       "github",
		"provider":          "github",
		"product":           "github",
		"family":            toolMeta.Presentation.Family,
		"tool_name":         toolName,
		"tool_set":          toolMeta.ToolSetID,
		"tool_set_id":       toolMeta.ToolSetID,
		"tool_set_name":     toolMeta.ToolSetName,
		"connector_id":      toolMeta.ConnectorID,
		"category":          string(toolMeta.Category),
		"origin":            string(toolMeta.Origin),
		"kind":              widget,
		"widget":            widget,
		"result_kind":       toolMeta.Presentation.ResultKind,
		"native_viewer":     toolMeta.Presentation.NativeViewer,
		"renderer_strategy": toolMeta.Presentation.RendererStrategy,
		"renderer_key":      toolMeta.Presentation.RendererKey,
		"readonly":          toolMeta.Category == ToolCategoryRead,
	}
	for key, want := range expected {
		if got := meta[key]; got != want {
			t.Fatalf("%s ResultMetadata[%s]=%#v, want %#v; metadata=%#v", toolName, key, got, want, meta)
		}
	}
	if toolMeta.Presentation.FileCapability != "" && meta["file_capability"] != toolMeta.Presentation.FileCapability {
		t.Fatalf("%s ResultMetadata.file_capability=%#v, want %#v; metadata=%#v", toolName, meta["file_capability"], toolMeta.Presentation.FileCapability, meta)
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("%s ResultMetadata.tool_presentation=%T, want map[string]any; metadata=%#v", toolName, meta["tool_presentation"], meta)
	}
	for key, want := range map[string]any{
		"family":            toolMeta.Presentation.Family,
		"result_kind":       toolMeta.Presentation.ResultKind,
		"native_viewer":     toolMeta.Presentation.NativeViewer,
		"renderer_strategy": toolMeta.Presentation.RendererStrategy,
		"renderer_key":      toolMeta.Presentation.RendererKey,
	} {
		if got := presentation[key]; got != want {
			t.Fatalf("%s tool_presentation[%s]=%#v, want %#v; presentation=%#v", toolName, key, got, want, presentation)
		}
	}
	if toolMeta.Presentation.FileCapability != "" && presentation["file_capability"] != toolMeta.Presentation.FileCapability {
		t.Fatalf("%s tool_presentation.file_capability=%#v, want %#v; presentation=%#v", toolName, presentation["file_capability"], toolMeta.Presentation.FileCapability, presentation)
	}
	for key, want := range map[string][]string{
		"artifact_types": toolMeta.Presentation.ArtifactTypes,
		"file_kinds":     toolMeta.Presentation.FileKinds,
		"preview_modes":  toolMeta.Presentation.PreviewModes,
		"source_refs":    toolMeta.Presentation.SourceRefs,
		"native_links":   toolMeta.Presentation.NativeLinks,
	} {
		requireGitHubPresentationStrings(t, toolName, presentation, key, want)
	}
	requireGitHubPresentationBool(t, toolName, presentation, "produces_file_artifact", toolMeta.Presentation.ProducesFileArtifact)
	requireGitHubPresentationBool(t, toolName, presentation, "promotes_native_input", toolMeta.Presentation.PromotesNativeInput)
}

func requireGitHubPresentationStrings(t *testing.T, toolName string, presentation map[string]any, key string, want []string) {
	t.Helper()
	gotRaw, exists := presentation[key]
	if len(want) == 0 {
		if !exists {
			return
		}
		got, ok := githubPresentationStringSlice(gotRaw)
		if !ok || len(got) > 0 {
			t.Fatalf("%s tool_presentation[%s]=%#v, want omitted or empty; presentation=%#v", toolName, key, gotRaw, presentation)
		}
		return
	}
	got, ok := githubPresentationStringSlice(gotRaw)
	if !ok {
		t.Fatalf("%s tool_presentation[%s]=%T, want []string %#v; presentation=%#v", toolName, key, gotRaw, want, presentation)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s tool_presentation[%s]=%#v, want %#v; presentation=%#v", toolName, key, got, want, presentation)
	}
}

func githubPresentationStringSlice(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

func requireGitHubPresentationBool(t *testing.T, toolName string, presentation map[string]any, key string, want bool) {
	t.Helper()
	gotRaw, exists := presentation[key]
	if !want {
		if exists && gotRaw != false {
			t.Fatalf("%s tool_presentation[%s]=%#v, want omitted or false; presentation=%#v", toolName, key, gotRaw, presentation)
		}
		return
	}
	if gotRaw != true {
		t.Fatalf("%s tool_presentation[%s]=%#v, want true; presentation=%#v", toolName, key, gotRaw, presentation)
	}
}

func TestGitHubResultMetadataUsesCatalogPresentation(t *testing.T) {
	for _, tc := range []struct {
		toolName string
		widget   string
	}{
		{GitHubToolListPullRequests, "pull_request_list"},
		{GitHubToolGetWorkflowRunJobs, "workflow_run_jobs"},
		{GitHubToolCloneRepository, "dry_run"},
		{GitHubToolCloneRepository, "repository_write"},
		{GitHubToolRunGHCommand, "gh_command"},
		{GitHubToolPullRepository, "approval"},
	} {
		t.Run(tc.toolName+"/"+tc.widget, func(t *testing.T) {
			meta := githubResultMetadata(tc.toolName, tc.widget)
			requireGitHubMetadataFields(t, meta, tc.toolName, tc.widget)
			if _, nested := meta["result_metadata"]; nested {
				t.Fatalf("metadata should not contain nested result_metadata: %#v", meta)
			}
		})
	}
}

func TestGitHubToolDefinitionsAreGated(t *testing.T) {
	registry := NewRegistry(nil)
	if defs := registry.GitHubToolDefinitions(false); len(defs) != 0 {
		t.Fatalf("disabled GitHub definitions=%d, want 0", len(defs))
	}
	if defs := registry.GitHubToolDefinitions(true); len(defs) != 0 {
		t.Fatalf("GitHub definitions without manager=%d, want 0", len(defs))
	}

	registry = NewRegistry(&RegistryOptions{GitHub: &fakeGitHubManager{}})
	if defs := registry.GitHubToolDefinitions(true); len(defs) != len(GitHubToolNames()) {
		t.Fatalf("enabled GitHub definitions=%d, want %d", len(defs), len(GitHubToolNames()))
	}
	if defs := registry.Definitions(); toolDefsContain(defs, GitHubToolStatus) {
		t.Fatal("GitHub tools should not be included in default definitions")
	}
}

func TestGitHubToolSchemasExposeReadOnlyInputs(t *testing.T) {
	for _, tool := range []Tool{
		newGitHubStatusTool(nil),
		newGitHubCheckConnectionTool(nil),
		newGitHubListRepositoriesTool(nil),
		newGitHubListPullRequestsTool(nil),
		newGitHubGetPullRequestTool(nil),
		newGitHubListIssuesTool(nil),
		newGitHubGetIssueTool(nil),
		newGitHubListWorkflowRunsTool(nil),
		newGitHubGetWorkflowRunJobsTool(nil),
		newGitHubCloneRepositoryTool(nil),
		newGitHubPullRepositoryTool(nil),
		newGitHubRefreshRepositoryTool(nil),
		newGitHubRunGHCommandTool(nil),
	} {
		def := tool.Definition()
		if got, ok := def.Function.Parameters["additionalProperties"].(bool); !ok || got {
			t.Fatalf("%s additionalProperties=%#v, want false", def.Function.Name, def.Function.Parameters["additionalProperties"])
		}
	}

	listDef := newGitHubListPullRequestsTool(nil).Definition()
	properties := toolDefinitionProperties(t, listDef.Function.Parameters)
	for _, key := range []string{"repository", "state", "limit"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("github_list_pull_requests schema missing %s", key)
		}
	}
	if _, ok := properties["project_id"]; ok {
		t.Fatal("GitHub tool schema should not expose project_id")
	}
	if _, ok := properties["args"]; ok {
		t.Fatal("GitHub tool schema should not expose generic shell args")
	}
	if _, ok := properties["token"]; ok {
		t.Fatal("GitHub tool schema should not expose auth tokens")
	}

	prDef := newGitHubGetPullRequestTool(nil).Definition()
	properties = toolDefinitionProperties(t, prDef.Function.Parameters)
	if _, ok := properties["number"]; !ok {
		t.Fatal("github_get_pull_request schema missing number")
	}
	required := toolDefinitionRequired(t, prDef.Function.Parameters)
	if !stringSliceContains(required, "number") {
		t.Fatalf("github_get_pull_request required=%v, want number", required)
	}

	jobsDef := newGitHubGetWorkflowRunJobsTool(nil).Definition()
	properties = toolDefinitionProperties(t, jobsDef.Function.Parameters)
	if _, ok := properties["run_id"]; !ok {
		t.Fatal("github_get_workflow_run_jobs schema missing run_id")
	}
	required = toolDefinitionRequired(t, jobsDef.Function.Parameters)
	if !stringSliceContains(required, "run_id") {
		t.Fatalf("github_get_workflow_run_jobs required=%v, want run_id", required)
	}
}

func TestGitHubExecuteToolSchemasExposeDryRun(t *testing.T) {
	for _, tool := range []Tool{
		newGitHubCloneRepositoryTool(nil),
		newGitHubPullRepositoryTool(nil),
		newGitHubRefreshRepositoryTool(nil),
		newGitHubRunGHCommandTool(nil),
	} {
		def := tool.Definition()
		properties := toolDefinitionProperties(t, def.Function.Parameters)
		if _, ok := properties["dry_run"]; !ok {
			t.Fatalf("%s schema missing dry_run", def.Function.Name)
		}
	}
}

func TestGitHubWriteToolRequiresPersistentPermission(t *testing.T) {
	manager := &fakeGitHubManager{}
	out, err := newGitHubRunGHCommandTool(manager).Execute(
		context.Background(),
		ExecContext{ProjectID: "proj_123"},
		json.RawMessage(`{"args":["gh","pr","create","--repo","owner/repo"],"timeout_seconds":120}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("tool returned OK without persistent permission: %#v", out.Result)
	}
	if manager.lastApproval.ToolName != "" || manager.lastOperation != "" {
		t.Fatalf("legacy approval should not be queued: approval=%#v operation=%q", manager.lastApproval, manager.lastOperation)
	}
	result := out.Result.(map[string]any)
	if result["permission_required"] != true || result["operation"] != githubOperationRawGHCommand {
		t.Fatalf("permission result=%#v", result)
	}
	target := result["target"].(map[string]any)
	if target["command"] != "gh pr create --repo owner/repo" || target["project_id"] != "proj_123" {
		t.Fatalf("target=%#v", target)
	}
	requireGitHubResultMetadata(t, out, GitHubToolRunGHCommand, "permission_required")
}

func TestGitHubCloneRequiresPersistentPermissionWithoutProject(t *testing.T) {
	manager := &fakeGitHubManager{}
	var progressEvents []map[string]any
	out, err := newGitHubCloneRepositoryTool(manager).Execute(
		context.Background(),
		ExecContext{OnProgress: func(progress map[string]any) {
			progressEvents = append(progressEvents, progress)
		}},
		json.RawMessage(`{"repository":"owner/repo","depth":1,"partial_clone":true}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("tool returned OK without persistent permission: %#v", out.Result)
	}
	if manager.lastApproval.ToolName != "" || manager.lastOperation != "" {
		t.Fatalf("legacy approval should not be queued: approval=%#v operation=%q", manager.lastApproval, manager.lastOperation)
	}
	result := out.Result.(map[string]any)
	if result["permission_required"] != true || result["operation"] != githubOperationCloneRepository {
		t.Fatalf("permission result=%#v", result)
	}
	target := result["target"].(map[string]any)
	if target["destination"] != "app-managed repository sources" || target["repository"] != "owner/repo" {
		t.Fatalf("target=%#v", target)
	}
	var permissionProgress map[string]any
	for _, event := range progressEvents {
		if event["phase"] == "permission_required" {
			permissionProgress = event
			break
		}
	}
	if permissionProgress == nil {
		t.Fatalf("expected permission progress event, got %#v", progressEvents)
	}
	requireGitHubResultMetadata(t, out, GitHubToolCloneRepository, "permission_required")
}

func TestGitHubReadRawGHCommandRunsWithoutApproval(t *testing.T) {
	manager := &fakeGitHubManager{}
	out, err := newGitHubRunGHCommandTool(manager).Execute(
		context.Background(),
		ExecContext{ProjectID: "proj_123"},
		json.RawMessage(`{"args":["gh","search_code","approval_required","--repo","owner/repo"],"timeout_seconds":45}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("tool returned error: %#v", out.Result)
	}
	if manager.lastApproval.ToolName != "" || manager.lastOperation != "" {
		t.Fatalf("read-only raw gh command should not queue approval: approval=%#v operation=%q", manager.lastApproval, manager.lastOperation)
	}
	if manager.lastReadGH.ProjectID != "proj_123" || manager.lastReadGH.TimeoutSeconds != 45 {
		t.Fatalf("read request metadata mismatch: %#v", manager.lastReadGH)
	}
	wantArgs := []string{"search", "code", "approval_required", "--repo", "owner/repo"}
	if !reflect.DeepEqual(manager.lastReadGH.Args, wantArgs) {
		t.Fatalf("read raw gh args=%#v, want %#v", manager.lastReadGH.Args, wantArgs)
	}
	result := out.Result.(map[string]any)
	if result["command"] != "gh search code approval_required --repo owner/repo" {
		t.Fatalf("command=%#v", result["command"])
	}
	requireGitHubResultMetadata(t, out, GitHubToolRunGHCommand, "gh_command")
}

func TestGitHubRawGHReadOnlyClassification(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "search code alias", args: []string{"search_code", "needle", "--repo", "owner/repo"}, want: true},
		{name: "search prs", args: []string{"search", "prs", "bug", "--owner", "owner"}, want: true},
		{name: "repo view", args: []string{"repo", "view", "owner/repo", "--json", "name"}, want: true},
		{name: "pr checks", args: []string{"pr", "checks", "7", "--repo", "owner/repo"}, want: true},
		{name: "issue list", args: []string{"issue", "list", "--repo", "owner/repo"}, want: true},
		{name: "run view", args: []string{"run", "view", "42", "--repo", "owner/repo"}, want: true},
		{name: "workflow list", args: []string{"workflow", "list", "--repo", "owner/repo"}, want: true},
		{name: "ruleset alias", args: []string{"rs", "list", "--repo", "owner/repo"}, want: true},
		{name: "status", args: []string{"status"}, want: true},
		{name: "api default get", args: []string{"api", "-H", "Accept: application/vnd.github.raw", "repos/owner/repo/contents/README.md"}, want: true},
		{name: "api explicit get with query fields", args: []string{"api", "search/code", "-X", "GET", "-f", "q=repo:owner/repo needle"}, want: true},
		{name: "api head", args: []string{"api", "-XHEAD", "repos/owner/repo"}, want: true},
		{name: "web flag is not direct", args: []string{"pr", "view", "7", "--web"}, want: false},
		{name: "repo clone writes files", args: []string{"repo", "clone", "owner/repo"}, want: false},
		{name: "pr merge mutates", args: []string{"pr", "merge", "7", "--repo", "owner/repo"}, want: false},
		{name: "run rerun mutates", args: []string{"run", "rerun", "42", "--repo", "owner/repo"}, want: false},
		{name: "api fields default to post", args: []string{"api", "repos/owner/repo/issues/1/comments", "-f", "body=hello"}, want: false},
		{name: "api post mutates", args: []string{"api", "repos/owner/repo/issues", "--method", "POST"}, want: false},
		{name: "api input reads local file", args: []string{"api", "repos/owner/repo/issues", "--method", "GET", "--input", "/tmp/body.json"}, want: false},
		{name: "api graphql can mutate", args: []string{"api", "graphql", "-f", "query=query { viewer { login } }"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := githubNormalizeRawGHArgs(tt.args)
			if err != nil {
				t.Fatalf("githubNormalizeRawGHArgs: %v", err)
			}
			if got := githubRawGHCommandIsReadOnly(normalized); got != tt.want {
				t.Fatalf("githubRawGHCommandIsReadOnly(%#v)=%v, want %v", normalized, got, tt.want)
			}
		})
	}
}

func TestGitHubRawGHCommandBlocksCredentialCommands(t *testing.T) {
	out, err := newGitHubRunGHCommandTool(&fakeGitHubManager{}).Execute(
		context.Background(),
		ExecContext{ProjectID: "proj_123"},
		json.RawMessage(`{"args":["auth","status","--show-token"]}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("blocked raw gh command returned OK: %#v", out.Result)
	}
}

func toolDefinitionProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has type %T, want map[string]any", schema["properties"])
	}
	return properties
}

func toolDefinitionRequired(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, ok := schema["required"].([]string)
	if !ok {
		t.Fatalf("required has type %T, want []string", schema["required"])
	}
	return raw
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestGitHubRepositoryReadToolsCanRunWithoutProject(t *testing.T) {
	out, err := newGitHubListPullRequestsTool(&fakeGitHubManager{}).Execute(context.Background(), ExecContext{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("repo-scoped GitHub read tool should allow source-backed resolution without project context, got %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	if result["project_id"] != "" {
		t.Fatalf("project_id=%#v, want empty when no project context; result=%#v", result["project_id"], result)
	}

	out, err = newGitHubStatusTool(&fakeGitHubManager{}).Execute(context.Background(), ExecContext{}, nil)
	if err != nil {
		t.Fatalf("Execute status: %v", err)
	}
	if !out.OK {
		t.Fatalf("github_status should not require project context: %#v", out.Result)
	}
}

func TestGitHubToolCallsManagerAndAddsMetadata(t *testing.T) {
	manager := &fakeGitHubManager{}
	out, err := newGitHubListPullRequestsTool(manager).Execute(
		context.Background(),
		ExecContext{ProjectID: "proj_123"},
		json.RawMessage(`{"repository":"owner/repo","state":"open","limit":7}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("tool returned error: %#v", out.Result)
	}
	if manager.lastRepo.ProjectID != "proj_123" || manager.lastRepo.Repository != "owner/repo" || manager.lastRepo.State != "open" || manager.lastRepo.Limit != 7 {
		t.Fatalf("manager request mismatch: %#v", manager.lastRepo)
	}
	requireGitHubResultMetadata(t, out, GitHubToolListPullRequests, "pull_request_list")

	out, err = newGitHubGetWorkflowRunJobsTool(manager).Execute(
		context.Background(),
		ExecContext{ProjectID: "proj_123"},
		json.RawMessage(`{"repository":"owner/repo","run_id":"42"}`),
	)
	if err != nil {
		t.Fatalf("Execute jobs: %v", err)
	}
	if !out.OK {
		t.Fatalf("jobs tool returned error: %#v", out.Result)
	}
	if manager.lastJobs.ProjectID != "proj_123" || manager.lastJobs.Repository != "owner/repo" || manager.lastJobs.RunID != 42 {
		t.Fatalf("jobs request mismatch: %#v", manager.lastJobs)
	}
	requireGitHubResultMetadata(t, out, GitHubToolGetWorkflowRunJobs, "workflow_run_jobs")
}

func TestGitHubCloneDoesNotAwaitLegacyApprovalWithoutPersistentPermission(t *testing.T) {
	manager := &fakeGitHubManager{
		awaitResult: map[string]any{
			"status":          "approved",
			"approval_status": "approved",
			"result": map[string]any{
				"project_id": "proj_created",
				"added_path": "repo",
			},
		},
	}
	out, err := newGitHubCloneRepositoryTool(manager).Execute(
		context.Background(),
		ExecContext{},
		json.RawMessage(`{"repository":"owner/repo","partial_clone":true}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("clone tool returned OK without persistent permission: %#v", out.Result)
	}
	if manager.awaitID != "" || manager.lastApproval.ID != "" {
		t.Fatalf("legacy approval should not be queued or awaited: awaitID=%q approval=%#v", manager.awaitID, manager.lastApproval)
	}
	result := out.Result.(map[string]any)
	if result["permission_required"] != true {
		t.Fatalf("permission_required=%v, want true; result=%#v", result["permission_required"], result)
	}
	requireGitHubResultMetadata(t, out, GitHubToolCloneRepository, "permission_required")
}

func TestGitHubCloneRunsDirectlyWhenPermissionApproved(t *testing.T) {
	manager := &fakeGitHubManager{
		approvedResult: map[string]any{
			"project_id": "proj_created",
			"repository": "owner/repo",
			"added_path": "repo",
		},
	}
	out, err := newGitHubCloneRepositoryTool(manager).Execute(
		context.Background(),
		ExecContext{PermissionApproved: true},
		json.RawMessage(`{"repository":"owner/repo","partial_clone":true}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("clone tool returned error: %#v", out.Result)
	}
	if manager.lastOperation != "" || manager.lastApproval.ID != "" {
		t.Fatalf("legacy approval should not be queued: operation=%q approval=%#v", manager.lastOperation, manager.lastApproval)
	}
	if manager.approvedOperation != githubOperationCloneRepository {
		t.Fatalf("approvedOperation=%q, want %q", manager.approvedOperation, githubOperationCloneRepository)
	}
	result := out.Result.(map[string]any)
	if result["project_id"] != "proj_created" {
		t.Fatalf("project_id=%#v, want proj_created", result["project_id"])
	}
	requireGitHubResultMetadata(t, out, GitHubToolCloneRepository, "repository_write")
}

func TestGitHubCloneDryRunDoesNotQueueOrEmitApproval(t *testing.T) {
	manager := &fakeGitHubManager{}
	var progress []map[string]any
	out, err := newGitHubCloneRepositoryTool(manager).Execute(
		context.Background(),
		ExecContext{ProjectID: "proj_123", OnProgress: func(event map[string]any) {
			progress = append(progress, event)
		}},
		json.RawMessage(`{"repository":"owner/repo","partial_clone":true,"dry_run":true}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("clone dry-run returned error: %#v", out.Result)
	}
	if manager.lastOperation != "" || manager.approvedOperation != "" || manager.lastApproval.ID != "" {
		t.Fatalf("dry-run should not queue or execute: operation=%q approved=%q approval=%#v", manager.lastOperation, manager.approvedOperation, manager.lastApproval)
	}
	if githubProgressPhaseSeen(progress, "approval_required") {
		t.Fatalf("dry-run emitted approval_required progress: %#v", progress)
	}
	result := out.Result.(map[string]any)
	if result["dry_run"] != true || result["operation"] != githubOperationCloneRepository {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	requireGitHubResultMetadata(t, out, GitHubToolCloneRepository, "dry_run")
}

func TestGitHubRawGHDryRunDoesNotRunCommandOrQueueApproval(t *testing.T) {
	cases := []struct {
		name     string
		args     string
		command  string
		readOnly bool
	}{
		{
			name:     "read only",
			args:     `{"args":["gh","repo","view","owner/repo"],"dry_run":true}`,
			command:  "gh repo view owner/repo",
			readOnly: true,
		},
		{
			name:     "mutating",
			args:     `{"args":["gh","pr","create","--repo","owner/repo"],"dry_run":true}`,
			command:  "gh pr create --repo owner/repo",
			readOnly: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := &fakeGitHubManager{}
			out, err := newGitHubRunGHCommandTool(manager).Execute(
				context.Background(),
				ExecContext{ProjectID: "proj_123"},
				json.RawMessage(tc.args),
			)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !out.OK {
				t.Fatalf("raw gh dry-run returned error: %#v", out.Result)
			}
			if manager.lastReadGH.ProjectID != "" || len(manager.lastReadGH.Args) != 0 || manager.lastOperation != "" || manager.approvedOperation != "" || manager.lastApproval.ID != "" {
				t.Fatalf("dry-run should not run gh or approval paths: read=%#v operation=%q approved=%q approval=%#v", manager.lastReadGH, manager.lastOperation, manager.approvedOperation, manager.lastApproval)
			}
			result := out.Result.(map[string]any)
			if result["dry_run"] != true || result["operation"] != githubOperationRawGHCommand {
				t.Fatalf("unexpected dry-run result: %#v", result)
			}
			requireGitHubResultMetadata(t, out, GitHubToolRunGHCommand, "dry_run")
			preview := result["preview"].(map[string]any)
			if preview["command"] != tc.command || preview["read_only"] != tc.readOnly {
				t.Fatalf("unexpected dry-run preview: %#v", preview)
			}
		})
	}
}

func TestGitHubRepositoryWriteDryRunsDoNotQueueOrExecute(t *testing.T) {
	cases := []struct {
		name                  string
		toolName              string
		tool                  Tool
		args                  string
		operation             string
		wantPreviewRepository string
	}{
		{
			name:                  "pull",
			toolName:              GitHubToolPullRepository,
			tool:                  newGitHubPullRepositoryTool(&fakeGitHubManager{}),
			args:                  `{"repository":"owner/repo","best_effort":true,"dry_run":true}`,
			operation:             githubOperationPullRepository,
			wantPreviewRepository: "owner/repo",
		},
		{
			name:                  "refresh",
			toolName:              GitHubToolRefreshRepository,
			tool:                  newGitHubRefreshRepositoryTool(&fakeGitHubManager{}),
			args:                  `{"repository":"owner/repo","best_effort":true,"dry_run":true}`,
			operation:             githubOperationRefreshRepository,
			wantPreviewRepository: "owner/repo",
		},
		{
			name:                  "pull all visible",
			toolName:              GitHubToolPullRepository,
			tool:                  newGitHubPullRepositoryTool(&fakeGitHubManager{}),
			args:                  `{"best_effort":true,"dry_run":true}`,
			operation:             githubOperationPullRepository,
			wantPreviewRepository: "all visible GitHub repositories",
		},
		{
			name:                  "refresh all visible",
			toolName:              GitHubToolRefreshRepository,
			tool:                  newGitHubRefreshRepositoryTool(&fakeGitHubManager{}),
			args:                  `{"best_effort":true,"dry_run":true}`,
			operation:             githubOperationRefreshRepository,
			wantPreviewRepository: "all visible GitHub repositories",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := &fakeGitHubManager{}
			switch tc.operation {
			case githubOperationPullRepository:
				tc.tool = newGitHubPullRepositoryTool(manager)
			case githubOperationRefreshRepository:
				tc.tool = newGitHubRefreshRepositoryTool(manager)
			}
			out, err := tc.tool.Execute(
				context.Background(),
				ExecContext{ProjectID: "proj_123"},
				json.RawMessage(tc.args),
			)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if !out.OK {
				t.Fatalf("%s dry-run returned error: %#v", tc.name, out.Result)
			}
			if manager.lastOperation != "" || manager.approvedOperation != "" || manager.lastApproval.ID != "" {
				t.Fatalf("dry-run should not queue or execute: operation=%q approved=%q approval=%#v", manager.lastOperation, manager.approvedOperation, manager.lastApproval)
			}
			result := out.Result.(map[string]any)
			if result["dry_run"] != true || result["operation"] != tc.operation {
				t.Fatalf("unexpected dry-run result: %#v", result)
			}
			preview := result["preview"].(map[string]any)
			if preview["repository"] != tc.wantPreviewRepository {
				t.Fatalf("preview repository=%#v want %q", preview["repository"], tc.wantPreviewRepository)
			}
			requireGitHubResultMetadata(t, out, tc.toolName, "dry_run")
		})
	}
}

type fakeGitHubManager struct {
	lastRepo          fakeGitHubRepositoryRequest
	lastJobs          fakeGitHubWorkflowRunJobsRequest
	lastApproval      fakeGitHubApproval
	lastOperation     string
	lastArgs          map[string]any
	lastReadGH        fakeGitHubRawGHCommandRequest
	awaitResult       map[string]any
	awaitID           string
	approvedOperation string
	approvedArgs      map[string]any
	approvedResult    map[string]any
}

type fakeGitHubRepositoryRequest struct {
	ProjectID  string
	Repository string
	State      string
	Limit      int
}

type fakeGitHubWorkflowRunJobsRequest struct {
	ProjectID  string
	Repository string
	RunID      int64
}

type fakeGitHubApproval struct {
	ID       string
	Title    string
	Detail   string
	ToolName string
	Product  string
	Action   string
	Target   map[string]any
	Content  map[string]any
}

type fakeGitHubRawGHCommandRequest struct {
	ProjectID      string
	Args           []string
	TimeoutSeconds int
}

func (m *fakeGitHubManager) Status(context.Context) (map[string]any, error) {
	return map[string]any{"configured": true}, nil
}

func (m *fakeGitHubManager) Check(context.Context) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func (m *fakeGitHubManager) ListRepositories(context.Context, string) ([]string, error) {
	return []string{"owner/repo"}, nil
}

func (m *fakeGitHubManager) ListPullRequests(_ context.Context, req fakeGitHubRepositoryRequest) (any, error) {
	m.lastRepo = req
	return []map[string]any{{"number": 1}}, nil
}

func (m *fakeGitHubManager) GetPullRequest(context.Context, fakeGitHubRepositoryRequest) (any, error) {
	return map[string]any{"number": 1}, nil
}

func (m *fakeGitHubManager) ListIssues(context.Context, fakeGitHubRepositoryRequest) (any, error) {
	return []map[string]any{{"number": 2}}, nil
}

func (m *fakeGitHubManager) GetIssue(context.Context, fakeGitHubRepositoryRequest) (any, error) {
	return map[string]any{"number": 2}, nil
}

func (m *fakeGitHubManager) ListWorkflowRuns(context.Context, fakeGitHubRepositoryRequest) (any, error) {
	return []map[string]any{{"database_id": 42}}, nil
}

func (m *fakeGitHubManager) GetWorkflowRunJobs(_ context.Context, req fakeGitHubWorkflowRunJobsRequest) (any, error) {
	m.lastJobs = req
	return []map[string]any{{"name": "test"}}, nil
}

func (m *fakeGitHubManager) QueueApproval(input fakeGitHubApproval, operation string, args map[string]any) fakeGitHubApproval {
	input.ID = "gh-test"
	m.lastApproval = input
	m.lastOperation = operation
	m.lastArgs = args
	return input
}

func (m *fakeGitHubManager) AwaitApproval(_ context.Context, id string, _ time.Duration) (map[string]any, error) {
	m.awaitID = id
	if m.awaitResult != nil {
		return m.awaitResult, nil
	}
	return map[string]any{
		"approval":        m.lastApproval,
		"status":          "pending",
		"approval_status": "pending",
	}, nil
}

func (m *fakeGitHubManager) RunApprovedOperation(_ context.Context, operation string, args map[string]any) (map[string]any, error) {
	m.approvedOperation = operation
	m.approvedArgs = args
	if m.approvedResult != nil {
		return m.approvedResult, nil
	}
	return map[string]any{}, nil
}

func (m *fakeGitHubManager) RunReadGHCommand(_ context.Context, req fakeGitHubRawGHCommandRequest) (map[string]any, error) {
	m.lastReadGH = req
	return map[string]any{
		"args": req.Args,
		"result": map[string]any{
			"stdout": "search results",
		},
	}, nil
}

func githubProgressPhaseSeen(events []map[string]any, phase string) bool {
	for _, event := range events {
		if strings.TrimSpace(stringMapValue(event, "phase")) == phase {
			return true
		}
	}
	return false
}
