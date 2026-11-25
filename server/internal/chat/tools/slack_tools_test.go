package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"ai-analyst-local/internal/secrets"
	slackapi "ai-analyst-local/internal/slack"
)

func TestSlackToolNamesAndLookup(t *testing.T) {
	names := SlackToolNames()
	if len(names) != 12 {
		t.Fatalf("len(SlackToolNames())=%d, want 12", len(names))
	}

	seen := map[string]bool{}
	for _, name := range names {
		if name == "" {
			t.Fatal("SlackToolNames contains empty name")
		}
		if seen[name] {
			t.Fatalf("SlackToolNames contains duplicate %q", name)
		}
		seen[name] = true
		if !IsSlackToolName(name) {
			t.Fatalf("IsSlackToolName(%q)=false, want true", name)
		}
	}
	if !IsSlackWriteToolName("slack_post_message") {
		t.Fatal("slack_post_message should be classified as a Slack write tool")
	}
	if !IsSlackReadToolName(SlackToolDownloadFile) {
		t.Fatal("slack_download_file should be classified as a Slack read tool")
	}
	if IsSlackToolName("slack_delete_message") {
		t.Fatal("IsSlackToolName accepted a destructive write-style tool")
	}

	names[0] = "mutated"
	if SlackToolNames()[0] == "mutated" {
		t.Fatal("SlackToolNames should return a defensive copy")
	}
}

func TestSlackToolDefinitionsAreGated(t *testing.T) {
	registry := NewRegistry(nil)
	if defs := registry.SlackToolDefinitions(false, false); len(defs) != 0 {
		t.Fatalf("disabled Slack definitions=%d, want 0", len(defs))
	}
	if defs := registry.SlackToolDefinitions(true, true); len(defs) != 0 {
		t.Fatalf("Slack definitions without manager=%d, want 0", len(defs))
	}

	registry = NewRegistry(&RegistryOptions{Slack: slackapi.NewManager(nil, nil)})
	if defs := registry.SlackToolDefinitions(true, false); len(defs) != len(SlackReadToolNames()) {
		t.Fatalf("read Slack definitions=%d, want %d", len(defs), len(SlackReadToolNames()))
	}
	if defs := registry.SlackToolDefinitions(false, true); len(defs) != len(SlackWriteToolNames()) {
		t.Fatalf("write Slack definitions=%d, want %d", len(defs), len(SlackWriteToolNames()))
	}
	if defs := registry.SlackToolDefinitions(true, true); len(defs) != len(SlackToolNames()) {
		t.Fatalf("all Slack definitions=%d, want %d", len(defs), len(SlackToolNames()))
	}
	if defs := registry.Definitions(); toolDefsContain(defs, SlackToolStatus) {
		t.Fatal("Slack tools should not be included in default definitions")
	}
}

func TestSlackToolsUseFirstClassResultMetadata(t *testing.T) {
	raw, err := os.ReadFile("slack_tools.go")
	if err != nil {
		t.Fatalf("read slack_tools.go: %v", err)
	}
	if strings.Contains(string(raw), `"result_metadata"`) {
		t.Fatal("Slack tools should set Output.ResultMetadata directly instead of nesting result_metadata in result maps")
	}

	cases := []struct {
		name     string
		kind     string
		readonly bool
		output   Output
	}{
		{
			name:     SlackToolStatus,
			kind:     "status",
			readonly: true,
			output:   slackReadOutput(SlackToolStatus, map[string]any{"ok": true}, "status"),
		},
		{
			name:     SlackToolDownloadFile,
			kind:     "file_download",
			readonly: true,
			output:   slackReadOutput(SlackToolDownloadFile, map[string]any{"ok": true, "local_path": "/tmp/example.txt"}, "file_download"),
		},
		{
			name:     SlackToolPostMessage,
			kind:     "post_message",
			readonly: false,
			output:   slackWriteOutput(SlackToolPostMessage, map[string]any{"ok": true}, "post_message"),
		},
		{
			name:     SlackToolUploadFile,
			kind:     "file_upload",
			readonly: false,
			output:   slackWriteOutput(SlackToolUploadFile, map[string]any{"ok": true}, "file_upload"),
		},
	}
	for _, tc := range cases {
		meta := requireSlackCatalogResultMetadata(t, tc.output, tc.name, tc.kind, tc.readonly)
		if got := slackMetadataString(meta, "provider"); got != "slack" {
			t.Fatalf("%s ResultMetadata.provider=%q, want slack; metadata=%#v", tc.name, got, meta)
		}
		if got := slackMetadataString(meta, "integration"); got != "slack" {
			t.Fatalf("%s ResultMetadata.integration=%q, want slack; metadata=%#v", tc.name, got, meta)
		}
	}

	approvalOut := slackApprovalOutput(SlackToolPostMessage, map[string]any{"id": "approval-1"})
	approvalMeta := requireSlackCatalogResultMetadata(t, approvalOut, SlackToolPostMessage, "approval", false)
	if got := slackMetadataString(approvalMeta, "widget"); got != "approval" {
		t.Fatalf("approval ResultMetadata.widget=%q, want approval; metadata=%#v", got, approvalMeta)
	}
}

func requireSlackCatalogResultMetadata(t *testing.T, out Output, toolName, kind string, readonly bool) map[string]any {
	t.Helper()
	meta := out.ResultMetadata
	if len(meta) == 0 {
		t.Fatalf("%s ResultMetadata is empty", toolName)
	}
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("%s result contains nested result_metadata: %#v", toolName, result)
		}
	}
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q)=false", toolName)
	}
	expected := map[string]string{
		"tool_name":         toolName,
		"kind":              kind,
		"tool_set_id":       toolMeta.ToolSetID,
		"tool_set_name":     toolMeta.ToolSetName,
		"connector_id":      toolMeta.ConnectorID,
		"category":          string(toolMeta.Category),
		"origin":            string(toolMeta.Origin),
		"result_kind":       toolMeta.Presentation.ResultKind,
		"native_viewer":     toolMeta.Presentation.NativeViewer,
		"renderer_strategy": toolMeta.Presentation.RendererStrategy,
		"renderer_key":      toolMeta.Presentation.RendererKey,
	}
	if toolMeta.Presentation.FileCapability != "" {
		expected["file_capability"] = toolMeta.Presentation.FileCapability
	}
	for key, want := range expected {
		if got := slackMetadataString(meta, key); got != want {
			t.Fatalf("%s ResultMetadata[%s]=%q, want %q; metadata=%#v", toolName, key, got, want, meta)
		}
	}
	if got, ok := meta["readonly"].(bool); !ok || got != readonly {
		t.Fatalf("%s ResultMetadata.readonly=%#v, want %v; metadata=%#v", toolName, meta["readonly"], readonly, meta)
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("%s ResultMetadata.tool_presentation=%T, want map[string]any; metadata=%#v", toolName, meta["tool_presentation"], meta)
	}
	presentationExpected := map[string]string{
		"family":            toolMeta.Presentation.Family,
		"result_kind":       toolMeta.Presentation.ResultKind,
		"native_viewer":     toolMeta.Presentation.NativeViewer,
		"renderer_strategy": toolMeta.Presentation.RendererStrategy,
		"renderer_key":      toolMeta.Presentation.RendererKey,
	}
	if toolMeta.Presentation.FileCapability != "" {
		presentationExpected["file_capability"] = toolMeta.Presentation.FileCapability
	}
	for key, want := range presentationExpected {
		if got := slackMetadataString(presentation, key); got != want {
			t.Fatalf("%s tool_presentation[%s]=%q, want %q; presentation=%#v", toolName, key, got, want, presentation)
		}
	}
	return meta
}

func slackMetadataString(meta map[string]any, key string) string {
	value, _ := meta[key].(string)
	return strings.TrimSpace(value)
}

func TestSlackToolSchemasAreClosed(t *testing.T) {
	for _, tool := range []Tool{
		newSlackStatusTool(nil),
		newSlackCheckConnectionTool(nil),
		newSlackListConversationsTool(nil),
		newSlackGetConversationTool(nil),
		newSlackGetConversationMembersTool(nil),
		newSlackGetMessagesTool(nil),
		newSlackGetThreadRepliesTool(nil),
		newSlackGetUserTool(nil),
		newSlackSearchMessagesTool(nil),
		newSlackDownloadFileTool(nil),
		newSlackPostMessageTool(nil),
		newSlackUploadFileTool(nil),
	} {
		def := tool.Definition()
		if got, ok := def.Function.Parameters["additionalProperties"].(bool); !ok || got {
			t.Fatalf("%s additionalProperties=%#v, want false", def.Function.Name, def.Function.Parameters["additionalProperties"])
		}
	}

	downloadProps := toolDefinitionProperties(t, newSlackDownloadFileTool(nil).Definition().Function.Parameters)
	for _, hiddenAlias := range []string{"fileId", "channel", "max_download_bytes"} {
		if _, ok := downloadProps[hiddenAlias]; ok {
			t.Fatalf("slack_download_file schema should keep runtime alias %q hidden", hiddenAlias)
		}
	}
	postProps := toolDefinitionProperties(t, newSlackPostMessageTool(nil).Definition().Function.Parameters)
	for _, hiddenAlias := range []string{"channel", "sendAs", "dryRun", "replyBroadcast"} {
		if _, ok := postProps[hiddenAlias]; ok {
			t.Fatalf("slack_post_message schema should keep runtime alias %q hidden", hiddenAlias)
		}
	}
}

func TestSlackWriteToolSchemasExposeApprovalInputs(t *testing.T) {
	postDef := newSlackPostMessageTool(nil).Definition()
	properties := toolDefinitionProperties(t, postDef.Function.Parameters)
	for _, key := range []string{"channel_id", "send_as", "text", "thread_ts", "reply_broadcast"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("slack_post_message schema missing %s", key)
		}
	}
	if _, ok := properties["dry_run"]; !ok {
		t.Fatal("slack_post_message schema missing dry_run")
	}
	required := toolDefinitionRequired(t, postDef.Function.Parameters)
	for _, key := range []string{"channel_id", "text"} {
		if !stringSliceContains(required, key) {
			t.Fatalf("slack_post_message required=%v, want %s", required, key)
		}
	}

	uploadDef := newSlackUploadFileTool(nil).Definition()
	properties = toolDefinitionProperties(t, uploadDef.Function.Parameters)
	for _, key := range []string{"channel_id", "send_as", "artifact_id", "path", "local_path", "fileName", "contentText", "contentBase64", "initial_comment"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("slack_upload_file schema missing %s", key)
		}
	}
	if _, ok := properties["dry_run"]; !ok {
		t.Fatal("slack_upload_file schema missing dry_run")
	}
	required = toolDefinitionRequired(t, uploadDef.Function.Parameters)
	if !stringSliceContains(required, "channel_id") {
		t.Fatalf("slack_upload_file required=%v, want channel_id", required)
	}
}

func TestSlackPostMessageDryRunDoesNotQueueApprovalOrCallSlack(t *testing.T) {
	manager := newSlackToolsTestManager(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "chat:write",
		TeamID:      "T123",
	})
	out, err := newSlackPostMessageTool(manager).Execute(context.Background(), ExecContext{}, mustJSONRaw(t, map[string]any{
		"channel_id": "C123",
		"text":       "Dry-run Slack message",
		"dry_run":    true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("slack_post_message dry-run returned error: %#v", out.Result)
	}
	if approvals := manager.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("dry-run queued legacy approval: %#v", approvals)
	}
	result := out.Result.(map[string]any)
	if result["dry_run"] != true || result["operation"] != slackPostMessageExecuteOperation {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if out.ResultMetadata["kind"] != "post_message" || out.ResultMetadata["readonly"] != false {
		t.Fatalf("unexpected dry-run ResultMetadata: %#v", out.ResultMetadata)
	}
}

func TestSlackUploadFileDryRunDoesNotQueueApprovalOrCallSlack(t *testing.T) {
	manager := newSlackToolsTestManager(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "files:write",
		TeamID:      "T123",
	})
	out, err := newSlackUploadFileTool(manager).Execute(context.Background(), ExecContext{}, mustJSONRaw(t, map[string]any{
		"channel_id":  "C123",
		"fileName":    "dry-run.txt",
		"contentText": "dry-run",
		"dry_run":     true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("slack_upload_file dry-run returned error: %#v", out.Result)
	}
	if approvals := manager.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("dry-run queued legacy approval: %#v", approvals)
	}
	result := out.Result.(map[string]any)
	if result["dry_run"] != true || result["operation"] != slackUploadFileExecuteOperation {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if out.ResultMetadata["kind"] != "file_upload" || out.ResultMetadata["readonly"] != false {
		t.Fatalf("unexpected dry-run ResultMetadata: %#v", out.ResultMetadata)
	}
}

func TestSlackPostMessageRunsDirectlyWhenPermissionApproved(t *testing.T) {
	manager := newSlackToolsTestManager(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "chat:write",
		TeamID:      "T123",
	})
	var gotPost map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat.postMessage":
			if auth := r.Header.Get("Authorization"); auth != "Bearer xoxp-test" {
				t.Fatalf("Authorization=%q, want bearer token", auth)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotPost); err != nil {
				t.Fatalf("decode post body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "C123",
				"ts":      "1780000000.000300",
			})
		case "/api/chat.getPermalink":
			if r.URL.Query().Get("channel") != "C123" || r.URL.Query().Get("message_ts") != "1780000000.000300" {
				t.Fatalf("unexpected permalink query: %#v", r.URL.Query())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":        true,
				"permalink": "https://example.slack.com/archives/C123/p1780000000000300",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackToolsAPIRedirect(t, srv.URL)

	var progress []map[string]any
	out, err := newSlackPostMessageTool(manager).Execute(context.Background(), ExecContext{
		PermissionApproved: true,
		OnProgress: func(event map[string]any) {
			progress = append(progress, event)
		},
	}, mustJSONRaw(t, map[string]any{
		"channel_id":      "C123",
		"text":            "Approved Slack message",
		"thread_ts":       "1780000000.000001",
		"reply_broadcast": true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("slack_post_message returned error: %#v", out.Result)
	}
	if gotPost["channel"] != "C123" || gotPost["text"] != "Approved Slack message" || gotPost["reply_broadcast"] != true {
		t.Fatalf("unexpected chat.postMessage body: %#v", gotPost)
	}
	if approvals := manager.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("legacy Slack approval should not be queued: %#v", approvals)
	}
	if slackProgressPhaseSeen(progress, "approval_required") {
		t.Fatalf("approved path emitted approval_required progress: %#v", progress)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type=%T, want map", out.Result)
	}
	if _, ok := result["approval"]; ok {
		t.Fatalf("approved path returned approval payload: %#v", result)
	}
	if result["ts"] != "1780000000.000300" || result["permalink"] != "https://example.slack.com/archives/C123/p1780000000000300" {
		t.Fatalf("unexpected manager result: %#v", result)
	}
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("approved path returned nested result_metadata: %#v", result)
	}
	if out.ResultMetadata["kind"] != "post_message" || out.ResultMetadata["readonly"] != false {
		t.Fatalf("unexpected ResultMetadata: %#v", out.ResultMetadata)
	}
}

func TestSlackUploadFileRunsDirectlyWhenPermissionApproved(t *testing.T) {
	manager := newSlackToolsTestManager(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "files:write,files:read",
		TeamID:      "T123",
	})
	var gotTicket map[string]any
	var gotUploaded []byte
	var gotComplete map[string]any
	serverURL := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files.getUploadURLExternal":
			if auth := r.Header.Get("Authorization"); auth != "Bearer xoxp-test" {
				t.Fatalf("Authorization=%q, want bearer token", auth)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotTicket); err != nil {
				t.Fatalf("decode upload ticket body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"upload_url": serverURL + "/upload",
				"file_id":    "F123",
			})
		case "/upload":
			if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
				t.Fatalf("raw upload should not send Slack bearer auth, got %q", auth)
			}
			gotUploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		case "/api/files.completeUploadExternal":
			if err := json.NewDecoder(r.Body).Decode(&gotComplete); err != nil {
				t.Fatalf("decode complete body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "files": []any{map[string]any{"id": "F123"}}})
		case "/api/files.info":
			if r.URL.Query().Get("file") != "F123" {
				t.Fatalf("files.info query=%#v", r.URL.Query())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id":                   "F123",
					"name":                 "approved.txt",
					"permalink":            "https://example.slack.com/files/F123/approved.txt",
					"url_private_download": "https://files.slack.com/files-pri/T123-F123/download/approved.txt",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	serverURL = srv.URL
	t.Cleanup(srv.Close)
	withSlackToolsAPIRedirect(t, srv.URL)

	var progress []map[string]any
	out, err := newSlackUploadFileTool(manager).Execute(context.Background(), ExecContext{
		PermissionApproved: true,
		OnProgress: func(event map[string]any) {
			progress = append(progress, event)
		},
	}, mustJSONRaw(t, map[string]any{
		"channel_id":      "C123",
		"fileName":        "approved.txt",
		"contentText":     "approved",
		"initial_comment": "Please review",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("slack_upload_file returned error: %#v", out.Result)
	}
	if gotTicket["filename"] != "approved.txt" || gotTicket["length"] != float64(len("approved")) {
		t.Fatalf("unexpected files.getUploadURLExternal body: %#v", gotTicket)
	}
	if !bytes.Equal(gotUploaded, []byte("approved")) {
		t.Fatalf("uploaded bytes=%q, want approved", string(gotUploaded))
	}
	if gotComplete["channel_id"] != "C123" || gotComplete["initial_comment"] != "Please review" {
		t.Fatalf("unexpected files.completeUploadExternal body: %#v", gotComplete)
	}
	if approvals := manager.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("legacy Slack approval should not be queued: %#v", approvals)
	}
	if slackProgressPhaseSeen(progress, "approval_required") {
		t.Fatalf("approved path emitted approval_required progress: %#v", progress)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type=%T, want map", out.Result)
	}
	if _, ok := result["approval"]; ok {
		t.Fatalf("approved path returned approval payload: %#v", result)
	}
	if result["file_id"] != "F123" || result["source"] != "inline" {
		t.Fatalf("unexpected manager result: %#v", result)
	}
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("approved path returned nested result_metadata: %#v", result)
	}
	if out.ResultMetadata["kind"] != "file_upload" || out.ResultMetadata["readonly"] != false {
		t.Fatalf("unexpected ResultMetadata: %#v", out.ResultMetadata)
	}
}

func TestSlackToolSchemasExposeReadOnlyInputs(t *testing.T) {
	listDef := newSlackListConversationsTool(nil).Definition()
	properties := toolDefinitionProperties(t, listDef.Function.Parameters)
	for _, key := range []string{"types", "limit", "cursor", "exclude_archived"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("slack_list_conversations schema missing %s", key)
		}
	}
	for _, key := range []string{"token", "access_token", "webhook_url", "message"} {
		if _, ok := properties[key]; ok {
			t.Fatalf("Slack list schema should not expose %s", key)
		}
	}

	messagesDef := newSlackGetMessagesTool(nil).Definition()
	properties = toolDefinitionProperties(t, messagesDef.Function.Parameters)
	for _, key := range []string{"channel_id", "limit", "cursor", "oldest", "latest", "inclusive"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("slack_get_messages schema missing %s", key)
		}
	}
	required := toolDefinitionRequired(t, messagesDef.Function.Parameters)
	if !stringSliceContains(required, "channel_id") {
		t.Fatalf("slack_get_messages required=%v, want channel_id", required)
	}

	searchDef := newSlackSearchMessagesTool(nil).Definition()
	properties = toolDefinitionProperties(t, searchDef.Function.Parameters)
	for _, key := range []string{"query", "count", "page", "sort", "sort_dir"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("slack_search_messages schema missing %s", key)
		}
	}
	required = toolDefinitionRequired(t, searchDef.Function.Parameters)
	if !stringSliceContains(required, "query") {
		t.Fatalf("slack_search_messages required=%v, want query", required)
	}

	downloadDef := newSlackDownloadFileTool(nil).Definition()
	properties = toolDefinitionProperties(t, downloadDef.Function.Parameters)
	for _, key := range []string{"file_id", "channel_id", "maxDownloadBytes"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("slack_download_file schema missing %s", key)
		}
	}
	required = toolDefinitionRequired(t, downloadDef.Function.Parameters)
	if !stringSliceContains(required, "file_id") {
		t.Fatalf("slack_download_file required=%v, want file_id", required)
	}
}

func newSlackToolsTestManager(t *testing.T, sec secrets.SlackSecrets) *slackapi.Manager {
	t.Helper()
	store, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init("passphrase"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Unlock("passphrase"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := store.SetSlackOAuth(sec); err != nil {
		t.Fatalf("SetSlackOAuth: %v", err)
	}
	return slackapi.NewManager(nil, store)
}

func withSlackToolsAPIRedirect(t *testing.T, serverURL string) {
	t.Helper()
	target, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	base := http.DefaultTransport
	http.DefaultTransport = slackToolsRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Scheme == "https" && req.URL.Host == "slack.com" && strings.HasPrefix(req.URL.Path, "/api/") {
			next := req.Clone(req.Context())
			nextURL := *req.URL
			nextURL.Scheme = target.Scheme
			nextURL.Host = target.Host
			next.URL = &nextURL
			next.Host = target.Host
			return base.RoundTrip(next)
		}
		return base.RoundTrip(req)
	})
	t.Cleanup(func() {
		http.DefaultTransport = base
	})
}

type slackToolsRoundTripFunc func(*http.Request) (*http.Response, error)

func (f slackToolsRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func slackProgressPhaseSeen(events []map[string]any, phase string) bool {
	for _, event := range events {
		if strings.TrimSpace(stringMapValue(event, "phase")) == phase {
			return true
		}
	}
	return false
}
