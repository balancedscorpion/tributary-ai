package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/chat/store"
)

func TestAtlassianJiraToolNamesIncludeFullCapabilitySet(t *testing.T) {
	names := AtlassianJiraToolNames()
	for _, want := range []string{
		"jira_get_issue_attachments",
		"jira_read_attachment",
		"jira_get_issue_link_types",
		"jira_find_users",
		"jira_add_related_tickets",
		"jira_upload_attachment",
		"jira_update_issue",
		"jira_create_issue",
	} {
		if !atlassianStringSliceContains(names, want) {
			t.Fatalf("AtlassianJiraToolNames missing %q: %v", want, names)
		}
	}
}

func TestJiraWriteToolSchemasExposeDryRun(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	for _, tool := range []Tool{
		newJiraAddCommentTool(manager),
		newJiraReplyToCommentTool(manager),
		newJiraAddRelatedTicketsTool(manager),
		newJiraUploadAttachmentTool(manager),
		newJiraUpdateIssueTool(manager),
		newJiraCreateIssueTool(manager),
	} {
		def := tool.Definition()
		properties := toolDefinitionProperties(t, def.Function.Parameters)
		if _, ok := properties["dry_run"]; !ok {
			t.Fatalf("%s schema missing dry_run", def.Function.Name)
		}
	}
}

func TestAtlassianToolSchemasAreTopLevelClosed(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	registry := NewRegistry(&RegistryOptions{Atlassian: manager})
	defs := registry.AtlassianToolDefinitions(true, true)
	if len(defs) != len(AtlassianSharedToolNames())+len(AtlassianJiraToolNames())+len(AtlassianConfluenceToolNames()) {
		t.Fatalf("Atlassian definitions=%d, want all shared/jira/confluence tools", len(defs))
	}
	for _, def := range defs {
		if got, ok := def.Function.Parameters["additionalProperties"].(bool); !ok || got {
			t.Fatalf("%s additionalProperties=%#v, want false", def.Function.Name, def.Function.Parameters["additionalProperties"])
		}
	}

	updateProps := toolDefinitionProperties(t, newJiraUpdateIssueTool(manager).Definition().Function.Parameters)
	fields, ok := updateProps["fields"].(map[string]any)
	if !ok {
		t.Fatalf("jira_update_issue.fields schema=%T, want object", updateProps["fields"])
	}
	if got, ok := fields["additionalProperties"].(bool); !ok || !got {
		t.Fatalf("jira_update_issue.fields.additionalProperties=%#v, want true", fields["additionalProperties"])
	}
	mentions, ok := updateProps["mentions"].(map[string]any)
	if !ok {
		t.Fatalf("jira_update_issue.mentions schema=%T, want object", updateProps["mentions"])
	}
	items, ok := mentions["items"].(map[string]any)
	if !ok {
		t.Fatalf("jira_update_issue.mentions.items schema=%T, want object", mentions["items"])
	}
	if got, ok := items["additionalProperties"].(bool); !ok || got {
		t.Fatalf("jira mention item schema additionalProperties=%#v, want false", items["additionalProperties"])
	}
}

func requireAtlassianResultMetadata(t *testing.T, out Output, widget string) {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("result should not contain nested result_metadata: %#v", result)
		}
	}
	meta := out.ResultMetadata
	if meta["integration"] != "atlassian" || meta["product"] != "jira" || meta["widget"] != widget {
		t.Fatalf("ResultMetadata=%#v, want Atlassian Jira %s", meta, widget)
	}
}

func TestAtlassianWriteOutputUsesFirstClassResultMetadata(t *testing.T) {
	out := atlassianWriteOutput("jira_add_comment", map[string]any{
		"issueKey": "TEAM-123",
		"result_metadata": map[string]any{
			"integration": "atlassian",
			"product":     "jira",
			"widget":      "comment",
		},
	})
	if !out.OK {
		t.Fatalf("atlassianWriteOutput returned not OK: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	if result["issueKey"] != "TEAM-123" {
		t.Fatalf("issueKey=%#v, want TEAM-123", result["issueKey"])
	}
	requireAtlassianBridgeResultMetadata(t, out, "jira_add_comment", "jira", "comment")
}

func TestAtlassianApprovalOutputUsesCatalogResultMetadata(t *testing.T) {
	out := atlassianApprovalOutput("jira_update_issue", atlassian.PendingApproval{
		ToolName: "jira_update_issue",
		Product:  "jira",
		Action:   "update_issue",
		Target:   map[string]any{"issue_key": "TEAM-123"},
	})
	if !out.OK {
		t.Fatalf("atlassianApprovalOutput returned not OK: %#v", out.Result)
	}
	requireAtlassianBridgeResultMetadata(t, out, "jira_update_issue", "jira", "approval")
}

func requireAtlassianBridgeResultMetadata(t *testing.T, out Output, toolName, product, widget string) map[string]any {
	t.Helper()
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result=%T, want map[string]any", out.Result)
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("result should not contain nested result_metadata: %#v", result)
	}
	meta := out.ResultMetadata
	if meta == nil {
		t.Fatalf("ResultMetadata is nil")
	}
	if got := strings.TrimSpace(anyToString(meta["tool_name"])); got != toolName {
		t.Fatalf("ResultMetadata.tool_name=%q, want %q; metadata=%#v", got, toolName, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["integration"])); got != "atlassian" {
		t.Fatalf("ResultMetadata.integration=%q, want atlassian; metadata=%#v", got, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["product"])); got != product {
		t.Fatalf("ResultMetadata.product=%q, want %q; metadata=%#v", got, product, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["widget"])); got != widget {
		t.Fatalf("ResultMetadata.widget=%q, want %q; metadata=%#v", got, widget, meta)
	}

	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q) not found", toolName)
	}
	if got := strings.TrimSpace(anyToString(meta["tool_set_id"])); got != toolMeta.ToolSetID {
		t.Fatalf("ResultMetadata.tool_set_id=%q, want %q; metadata=%#v", got, toolMeta.ToolSetID, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["tool_set_name"])); got != toolMeta.ToolSetName {
		t.Fatalf("ResultMetadata.tool_set_name=%q, want %q; metadata=%#v", got, toolMeta.ToolSetName, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["connector_id"])); got != toolMeta.ConnectorID {
		t.Fatalf("ResultMetadata.connector_id=%q, want %q; metadata=%#v", got, toolMeta.ConnectorID, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["origin"])); got != string(toolMeta.Origin) {
		t.Fatalf("ResultMetadata.origin=%q, want %q; metadata=%#v", got, toolMeta.Origin, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["category"])); got != string(toolMeta.Category) {
		t.Fatalf("ResultMetadata.category=%q, want %q; metadata=%#v", got, toolMeta.Category, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["kind"])); got != toolMeta.Presentation.ResultKind {
		t.Fatalf("ResultMetadata.kind=%q, want %q; metadata=%#v", got, toolMeta.Presentation.ResultKind, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["renderer_strategy"])); got != toolMeta.Presentation.RendererStrategy {
		t.Fatalf("ResultMetadata.renderer_strategy=%q, want %q; metadata=%#v", got, toolMeta.Presentation.RendererStrategy, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["renderer_key"])); got != toolMeta.Presentation.RendererKey {
		t.Fatalf("ResultMetadata.renderer_key=%q, want %q; metadata=%#v", got, toolMeta.Presentation.RendererKey, meta)
	}
	if got := strings.TrimSpace(anyToString(meta["native_viewer"])); got != toolMeta.Presentation.NativeViewer {
		t.Fatalf("ResultMetadata.native_viewer=%q, want %q; metadata=%#v", got, toolMeta.Presentation.NativeViewer, meta)
	}
	readonly, ok := meta["readonly"].(bool)
	if !ok || readonly != (toolMeta.Category == ToolCategoryRead) {
		t.Fatalf("ResultMetadata.readonly=%#v, want %v; metadata=%#v", meta["readonly"], toolMeta.Category == ToolCategoryRead, meta)
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("ResultMetadata.tool_presentation=%T, want map[string]any; metadata=%#v", meta["tool_presentation"], meta)
	}
	if got := strings.TrimSpace(anyToString(presentation["family"])); got != toolMeta.Presentation.Family {
		t.Fatalf("tool_presentation.family=%q, want %q; presentation=%#v", got, toolMeta.Presentation.Family, presentation)
	}
	if got := strings.TrimSpace(anyToString(presentation["result_kind"])); got != toolMeta.Presentation.ResultKind {
		t.Fatalf("tool_presentation.result_kind=%q, want %q; presentation=%#v", got, toolMeta.Presentation.ResultKind, presentation)
	}
	if got := strings.TrimSpace(anyToString(presentation["native_viewer"])); got != toolMeta.Presentation.NativeViewer {
		t.Fatalf("tool_presentation.native_viewer=%q, want %q; presentation=%#v", got, toolMeta.Presentation.NativeViewer, presentation)
	}
	return meta
}

func TestAtlassianBridgeStatusOutputUsesFirstClassResultMetadata(t *testing.T) {
	out := atlassianBridgeOutput(AtlassianSharedToolAtlassianStatus, map[string]any{
		"ok": true,
		"result_metadata": map[string]any{
			"integration":       "legacy_bridge",
			"family":            "legacy_family",
			"kind":              "legacy_kind",
			"native_viewer":     "legacy_viewer",
			"renderer_strategy": "legacy_strategy",
			"renderer_key":      "legacy_key",
			"widget":            "bridge_status",
		},
	})
	if !out.OK {
		t.Fatalf("atlassianBridgeOutput returned not OK: %#v", out.Result)
	}
	requireAtlassianBridgeResultMetadata(t, out, AtlassianSharedToolAtlassianStatus, "atlassian", "bridge_status")
}

func TestAtlassianBridgeJiraReadOutputUsesFirstClassResultMetadata(t *testing.T) {
	out := atlassianBridgeOutput("jira_get_issue", map[string]any{
		"issueKey": "TEAM-123",
		"result_metadata": map[string]any{
			"integration": "atlassian",
			"product":     "jira",
			"widget":      "issue",
		},
	})
	if !out.OK {
		t.Fatalf("atlassianBridgeOutput returned not OK: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	if result["issueKey"] != "TEAM-123" {
		t.Fatalf("issueKey=%#v, want TEAM-123", result["issueKey"])
	}
	requireAtlassianBridgeResultMetadata(t, out, "jira_get_issue", "jira", "issue")
}

func TestAtlassianBridgeConfluenceSearchOutputUsesFirstClassResultMetadata(t *testing.T) {
	out := atlassianBridgeOutput("confluence_search", map[string]any{
		"results": []any{},
		"result_metadata": map[string]any{
			"product":           "jira",
			"family":            "legacy_family",
			"kind":              "legacy_kind",
			"native_viewer":     "legacy_viewer",
			"renderer_strategy": "legacy_strategy",
			"renderer_key":      "legacy_key",
			"widget":            "search_results",
		},
	})
	if !out.OK {
		t.Fatalf("atlassianBridgeOutput returned not OK: %#v", out.Result)
	}
	requireAtlassianBridgeResultMetadata(t, out, "confluence_search", "confluence", "search_results")
}

func TestJiraReadAttachmentImageArtifactUsesNativeImagePayload(t *testing.T) {
	result := map[string]any{
		"issueKey":       "TEAM-123",
		"attachmentId":   "10001",
		"contentType":    "application/octet-stream",
		"contentBase64":  "QUJDRA==",
		"bytesReturned":  float64(4),
		"native_ignored": "kept",
		"attachment": map[string]any{
			"fileName": "screenshot.png",
			"mimeType": "image/png",
		},
	}

	artifact := jiraReadAttachmentImageArtifact(result, map[string]any{
		"issueKey":      "TEAM-123",
		"attachmentId":  "10001",
		"includeBase64": true,
		"maxBytes":      float64(4096),
	}, map[string]any{
		"integration": "atlassian",
		"product":     "jira",
		"widget":      "attachment",
	})
	if artifact == nil {
		t.Fatalf("artifact is nil")
	}
	if artifact.Type != "image" {
		t.Fatalf("artifact.Type=%q, want image", artifact.Type)
	}
	if artifact.Title != "screenshot.png" {
		t.Fatalf("artifact.Title=%q, want screenshot.png", artifact.Title)
	}
	if artifact.Content != "data:image/png;base64,QUJDRA==" {
		t.Fatalf("artifact.Content=%q, want PNG data URI", artifact.Content)
	}
	if _, ok := result["contentBase64"]; ok {
		t.Fatalf("contentBase64 was not removed from model-facing result")
	}
	if native, _ := result["native_image_input"].(bool); !native {
		t.Fatalf("native_image_input=%v, want true", result["native_image_input"])
	}
	if omitted, _ := result["contentBase64Omitted"].(bool); !omitted {
		t.Fatalf("contentBase64Omitted=%v, want true", result["contentBase64Omitted"])
	}
	if mimeType, _ := artifact.Metadata["mimeType"].(string); mimeType != "image/png" {
		t.Fatalf("metadata mimeType=%v, want image/png", artifact.Metadata["mimeType"])
	}
	artifactToolArgs, ok := artifact.Metadata["tool_args"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.tool_args=%T, want object: %#v", artifact.Metadata["tool_args"], artifact.Metadata)
	}
	if artifactToolArgs["issueKey"] != "TEAM-123" || artifactToolArgs["attachmentId"] != "10001" || artifactToolArgs["includeBase64"] != true {
		t.Fatalf("artifact metadata.tool_args=%#v, want Jira attachment read args", artifactToolArgs)
	}
	artifactResultMeta, ok := artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.result_metadata=%T, want object: %#v", artifact.Metadata["result_metadata"], artifact.Metadata)
	}
	requireAtlassianBridgeResultMetadata(t, Output{Result: map[string]any{}, ResultMetadata: artifactResultMeta}, "jira_read_attachment", "jira", "attachment")
	if artifactPresentation, ok := artifact.Metadata["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("artifact metadata.tool_presentation=%T, want object: %#v", artifact.Metadata["tool_presentation"], artifact.Metadata)
	} else if artifactPresentation["renderer_key"] != "file" || artifactPresentation["native_viewer"] != "file" {
		t.Fatalf("artifact tool_presentation=%#v, want native file metadata", artifactPresentation)
	}
}

func TestJiraReadAttachmentImageArtifactStripsTruncatedImageBase64(t *testing.T) {
	result := map[string]any{
		"contentType":   "image/png",
		"contentBase64": "QUJDRA==",
		"truncated":     true,
		"attachment": map[string]any{
			"fileName": "large.png",
		},
	}

	if artifact := jiraReadAttachmentImageArtifact(result, nil, nil); artifact != nil {
		t.Fatalf("artifact=%#v, want nil", artifact)
	}
	if _, ok := result["contentBase64"]; ok {
		t.Fatalf("contentBase64 was not removed from truncated image result")
	}
	if omitted, _ := result["contentBase64Omitted"].(bool); !omitted {
		t.Fatalf("contentBase64Omitted=%v, want true", result["contentBase64Omitted"])
	}
}

func TestJiraAddRelatedTicketsRequiresPersistentPermission(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	tool := newJiraAddRelatedTicketsTool(manager)

	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
		"issueKey":"team-123",
		"relatedIssueKeys":["team-456","TEAM-456","TEAM-789","TEAM-123"],
		"comment":"Related investigation"
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if out.OK {
		t.Fatalf("Execute returned OK without persistent permission: %#v", out.Result)
	}
	requireAtlassianBridgeResultMetadata(t, out, "jira_add_related_tickets", "jira", "permission_required")
	result := out.Result.(map[string]any)
	if result["permission_required"] != true || result["operation"] != jiraOperationAddRelatedTickets {
		t.Fatalf("permission result=%#v", result)
	}
	if approvals := manager.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("legacy approval should not be queued: %#v", approvals)
	}
}

func TestJiraAddCommentPermissionApprovedDoesNotQueueLegacyApproval(t *testing.T) {
	manager := atlassian.NewManager("", nil)
	tool := newJiraAddCommentTool(manager)
	var phases []string

	out, err := tool.Execute(context.Background(), ExecContext{
		PermissionApproved: true,
		OnProgress: func(progress map[string]any) {
			if phase, ok := progress["phase"].(string); ok {
				phases = append(phases, phase)
			}
		},
	}, json.RawMessage(`{
		"issueKey":"team-123",
		"comment":"Approved comment"
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if out.OK {
		t.Fatalf("Execute returned OK despite no fake bridge; wanted direct execution attempt: %#v", out.Result)
	}
	if approvals := manager.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("legacy approval should not be queued: %#v", approvals)
	}
	if !atlassianStringSliceContains(phases, "running") {
		t.Fatalf("progress phases=%v, want direct running phase", phases)
	}
	if atlassianStringSliceContains(phases, "approval_required") {
		t.Fatalf("progress phases=%v, should not include approval_required", phases)
	}
}

func TestJiraWriteDryRunsDoNotQueueLegacyApproval(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		tool      func(*atlassian.Manager) Tool
		args      string
		operation string
	}{
		{
			name:      "add_comment",
			toolName:  "jira_add_comment",
			tool:      newJiraAddCommentTool,
			args:      `{"issueKey":"team-123","comment":"Dry-run comment","dry_run":true}`,
			operation: jiraOperationAddComment,
		},
		{
			name:      "reply_to_comment",
			toolName:  "jira_reply_to_comment",
			tool:      newJiraReplyToCommentTool,
			args:      `{"issueKey":"team-123","commentId":"10001","comment":"Dry-run reply","dry_run":true}`,
			operation: jiraOperationAddComment,
		},
		{
			name:      "add_related_tickets",
			toolName:  "jira_add_related_tickets",
			tool:      newJiraAddRelatedTicketsTool,
			args:      `{"issueKey":"team-123","relatedIssueKeys":["TEAM-456"],"dry_run":true}`,
			operation: jiraOperationAddRelatedTickets,
		},
		{
			name:      "upload_attachment",
			toolName:  "jira_upload_attachment",
			tool:      newJiraUploadAttachmentTool,
			args:      `{"issueKey":"team-123","fileName":"dry-run.txt","contentText":"dry-run","dry_run":true}`,
			operation: jiraOperationUploadAttachment,
		},
		{
			name:      "update_issue",
			toolName:  "jira_update_issue",
			tool:      newJiraUpdateIssueTool,
			args:      `{"issueKey":"team-123","summary":"Dry-run summary","dry_run":true}`,
			operation: jiraOperationUpdateIssue,
		},
		{
			name:      "create_issue",
			toolName:  "jira_create_issue",
			tool:      newJiraCreateIssueTool,
			args:      `{"projectKey":"team","summary":"Dry-run issue","dry_run":true}`,
			operation: jiraOperationCreateIssue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := atlassian.NewManager(t.TempDir(), nil)
			out, err := tc.tool(manager).Execute(context.Background(), ExecContext{}, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if !out.OK {
				t.Fatalf("Execute returned not OK: %#v", out.Result)
			}
			if approvals := manager.ListApprovals(); len(approvals) != 0 {
				t.Fatalf("dry-run queued legacy approval: %#v", approvals)
			}
			result := out.Result.(map[string]any)
			if result["dry_run"] != true || result["operation"] != tc.operation {
				t.Fatalf("unexpected dry-run result: %#v", result)
			}
			requireAtlassianBridgeResultMetadata(t, out, tc.toolName, "jira", "dry_run")
		})
	}
}

func TestJiraUploadAttachmentDryRunIncludesTextPreview(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	tool := newJiraUploadAttachmentTool(manager)

	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
			"issueKey":"TEAM-123",
			"fileName":"notes.md",
			"contentText":"# Notes\n\nUpload this.",
			"dry_run":true
		}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !out.OK {
		t.Fatalf("Execute returned not OK: %#v", out.Result)
	}
	requireAtlassianBridgeResultMetadata(t, out, "jira_upload_attachment", "jira", "dry_run")
	result := out.Result.(map[string]any)
	preview := result["preview"].(map[string]any)
	if preview["file_name"] != "notes.md" {
		t.Fatalf("file_name = %#v, want notes.md", preview["file_name"])
	}
	if !strings.Contains(strings.TrimSpace(preview["preview"].(string)), "Upload this.") {
		t.Fatalf("preview = %#v, want uploaded text preview", preview["preview"])
	}
}

func TestResolveFileUploadSourceAllowsManagedLocalPath(t *testing.T) {
	cfgDir := t.TempDir()
	downloadDir := filepath.Join(cfgDir, "atlassian", "downloads", "jira", "TEAM-123", "10001")
	if err := os.MkdirAll(downloadDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	managedPath := filepath.Join(downloadDir, "notes.txt")
	if err := os.WriteFile(managedPath, []byte("downloaded from jira"), 0o600); err != nil {
		t.Fatalf("WriteFile managed: %v", err)
	}

	fileName, mimeType, raw, source, out, ok := resolveFileUploadSource(
		context.Background(),
		ExecContext{CfgDir: cfgDir},
		"",
		"",
		managedPath,
		"",
		"",
		"",
		"",
		1024,
		"test upload",
	)
	if !ok {
		t.Fatalf("resolveFileUploadSource failed: %#v", out.Result)
	}
	if fileName != "notes.txt" {
		t.Fatalf("fileName=%q, want notes.txt", fileName)
	}
	if mimeType != "text/plain; charset=utf-8" && mimeType != "text/plain" {
		t.Fatalf("mimeType=%q, want text/plain", mimeType)
	}
	if string(raw) != "downloaded from jira" {
		t.Fatalf("raw=%q, want downloaded content", string(raw))
	}
	if source != "managed_local_file" {
		t.Fatalf("source=%q, want managed_local_file", source)
	}
}

func TestResolveFileUploadSourceAllowsThreadArtifact(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	content := "generated report"
	toolName := "export_thread"
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-upload-source",
		ThreadID:    thread.ID,
		Type:        "code",
		Title:       "report.txt",
		ToolName:    &toolName,
		Storage:     "json",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	fileName, mimeType, raw, source, out, ok := resolveFileUploadSource(
		ctx,
		ExecContext{Store: st, ThreadID: thread.ID, CfgDir: cfgDir},
		"artifact-upload-source",
		"",
		"",
		"",
		"",
		"",
		"",
		1024,
		"test upload",
	)
	if !ok {
		t.Fatalf("resolveFileUploadSource failed: %#v", out.Result)
	}
	if fileName != "report.txt" {
		t.Fatalf("fileName=%q, want report.txt", fileName)
	}
	if mimeType != "text/plain; charset=utf-8" && mimeType != "text/plain" {
		t.Fatalf("mimeType=%q, want text/plain", mimeType)
	}
	if string(raw) != content {
		t.Fatalf("raw=%q, want generated artifact content", string(raw))
	}
	if source != "artifact" {
		t.Fatalf("source=%q, want artifact", source)
	}
}

func TestResolveFileUploadSourceRejectsUnmanagedLocalPath(t *testing.T) {
	cfgDir := t.TempDir()
	unmanagedDir := filepath.Join(cfgDir, "secrets")
	if err := os.MkdirAll(unmanagedDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	unmanagedPath := filepath.Join(unmanagedDir, "secret.txt")
	if err := os.WriteFile(unmanagedPath, []byte("do not upload"), 0o600); err != nil {
		t.Fatalf("WriteFile unmanaged: %v", err)
	}

	_, _, _, _, out, ok := resolveFileUploadSource(
		context.Background(),
		ExecContext{CfgDir: cfgDir},
		"",
		"",
		unmanagedPath,
		"",
		"",
		"",
		"",
		1024,
		"test upload",
	)
	if ok {
		t.Fatalf("resolveFileUploadSource accepted unmanaged local path")
	}
	errorText := out.Result.(map[string]any)["error"].(string)
	if !strings.Contains(strings.ToLower(errorText), "app-managed download") {
		t.Fatalf("error=%q, want app-managed download guidance", errorText)
	}
}

func TestJiraCommentToolsExposeMentions(t *testing.T) {
	for _, tool := range []Tool{
		newJiraAddCommentTool(atlassian.NewManager(t.TempDir(), nil)),
		newJiraReplyToCommentTool(atlassian.NewManager(t.TempDir(), nil)),
	} {
		def := tool.Definition()
		props := def.Function.Parameters["properties"].(map[string]any)
		if _, ok := props["mentions"]; !ok {
			t.Fatalf("%s schema missing mentions property", def.Function.Name)
		}
		if !strings.Contains(def.Function.Description, "jira_find_users") {
			t.Fatalf("%s description should guide user lookup before mentions: %s", def.Function.Name, def.Function.Description)
		}
	}
}

func TestJiraCommentRequiresAccountIDBackedMention(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	tool := newJiraAddCommentTool(manager)

	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
		"issueKey":"TEAM-123",
		"comment":"Please check this @Alex"
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if out.OK {
		t.Fatalf("Execute returned OK, want mention validation error: %#v", out.Result)
	}
	errorText := out.Result.(map[string]any)["error"].(string)
	if !strings.Contains(strings.ToLower(errorText), "accountid") || !strings.Contains(errorText, "jira_find_users") {
		t.Fatalf("error = %q, want accountId/jira_find_users guidance", errorText)
	}
}

func TestJiraCommentAllowsMentionTextAlias(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	tool := newJiraAddCommentTool(manager)

	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
			"issueKey":"TEAM-123",
			"comment":"Please check this @Alex",
			"mentions":[{"accountId":"abc-123","displayName":"Alex Example","mentionText":"@Alex"}],
			"dry_run":true
		}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !out.OK {
		t.Fatalf("Execute returned not OK: %#v", out.Result)
	}
	requireAtlassianBridgeResultMetadata(t, out, "jira_add_comment", "jira", "dry_run")
	result := out.Result.(map[string]any)
	preview := result["preview"].(map[string]any)
	mentions := preview["mentions"].([]map[string]any)
	if got := mentions[0]["mentionText"]; got != "@Alex" {
		t.Fatalf("mentionText = %#v, want @Alex", got)
	}
}

func TestJiraUpdateIssueDryRunNormalizesFields(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	tool := newJiraUpdateIssueTool(manager)

	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
		"issueKey":"team-123",
		"summary":"Updated summary",
		"description":"Looping in @Alex",
			"assigneeAccountId":"abc-123",
			"labels":["ops","ops","release"],
			"components":["API","api","Frontend"],
			"mentions":[{"accountId":"abc-123","displayName":"Alex Example","mentionText":"@Alex"}],
			"dry_run":true
		}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !out.OK {
		t.Fatalf("Execute returned not OK: %#v", out.Result)
	}
	requireAtlassianBridgeResultMetadata(t, out, "jira_update_issue", "jira", "dry_run")
	result := out.Result.(map[string]any)
	preview := result["preview"].(map[string]any)
	fieldNames := preview["field_names"].([]string)
	if strings.Join(fieldNames, ",") != "assignee,components,description,labels,summary" {
		t.Fatalf("field_names = %v, want assignee,components,description,labels,summary", fieldNames)
	}
}

func TestJiraCreateIssueDryRunNormalizesComponents(t *testing.T) {
	manager := atlassian.NewManager(t.TempDir(), nil)
	tool := newJiraCreateIssueTool(manager)

	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
			"projectKey":"team",
			"summary":"Build component-aware ticket",
			"components":["API","api","Frontend"],
			"dry_run":true
		}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !out.OK {
		t.Fatalf("Execute returned not OK: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	preview := result["preview"].(map[string]any)
	components := preview["components"].([]string)
	if strings.Join(components, ",") != "API,Frontend" {
		t.Fatalf("components = %v, want API,Frontend", components)
	}
}

func atlassianStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
