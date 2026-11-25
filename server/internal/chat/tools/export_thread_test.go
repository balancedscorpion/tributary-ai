package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/threadtransfer"
	"ai-analyst-local/internal/chat/types"
)

func TestExportThreadToolCreatesDownloadableBundleArtifact(t *testing.T) {
	t.Parallel()

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

	content := "Please export this thread."
	if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role:    types.RoleUser,
		Content: &content,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	insertThreadAttachmentArtifact(t, ctx, st, cfgDir, thread.ID, "notes.txt", "alpha\nbeta\n")

	tool := newExportThreadTool()
	out, err := tool.Execute(ctx, ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		CfgDir:   cfgDir,
	}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %s", toolErrorMessage(t, out))
	}
	if out.ResultFormat != ResultFormatJSON {
		t.Fatalf("ResultFormat=%q, want %q", out.ResultFormat, ResultFormatJSON)
	}
	meta := out.ResultMetadata
	if meta["tool_name"] != "export_thread" ||
		meta["provider"] != "artifacts" ||
		meta["product"] != "artifacts" ||
		meta["family"] != "artifacts" ||
		meta["kind"] != "file" ||
		meta["tool_set_id"] != "artifacts" ||
		meta["category"] != "read" ||
		meta["origin"] != "builtin" ||
		meta["readonly"] != true ||
		meta["native_viewer"] != "file" ||
		meta["renderer_strategy"] != "artifact_type" ||
		meta["renderer_key"] != "file" ||
		meta["file_capability"] != "artifact_create" {
		t.Fatalf("ResultMetadata=%#v, want export_thread file-rendering metadata", meta)
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("ResultMetadata.tool_presentation=%T, want object; metadata=%#v", meta["tool_presentation"], meta)
	}
	if presentation["produces_file_artifact"] != true ||
		presentation["native_viewer"] != "file" ||
		presentation["renderer_key"] != "file" {
		t.Fatalf("tool_presentation=%#v, want file artifact presentation", presentation)
	}
	if out.Artifact == nil {
		t.Fatalf("Artifact=nil, want file artifact")
	}
	if out.Artifact.Type != "file" {
		t.Fatalf("Artifact.Type=%q, want file", out.Artifact.Type)
	}
	if got := strings.TrimSpace(firstStringMapValue(out.Artifact.Metadata, "download_name")); got == "" {
		t.Fatalf("download_name missing from artifact metadata: %#v", out.Artifact.Metadata)
	}
	if available, ok := out.Artifact.Metadata["download_available"].(bool); !ok || !available {
		t.Fatalf("download_available=%#v, want true", out.Artifact.Metadata["download_available"])
	}
	if got := strings.TrimSpace(firstStringMapValue(out.Artifact.Metadata, "mimeType")); got != threadtransfer.BundleMimeType {
		t.Fatalf("mimeType=%q, want %q", got, threadtransfer.BundleMimeType)
	}
	if got := strings.TrimSpace(firstStringMapValue(out.Artifact.Metadata, "tool_name")); got != "export_thread" {
		t.Fatalf("artifact tool_name=%q, want export_thread", got)
	}
	if _, ok := out.Artifact.Metadata["tool_args"].(map[string]any); !ok {
		t.Fatalf("artifact metadata.tool_args=%T, want object: %#v", out.Artifact.Metadata["tool_args"], out.Artifact.Metadata)
	}
	artifactResultMeta, ok := out.Artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.result_metadata=%T, want object: %#v", out.Artifact.Metadata["result_metadata"], out.Artifact.Metadata)
	}
	if artifactResultMeta["renderer_key"] != "file" || artifactResultMeta["native_viewer"] != "file" {
		t.Fatalf("artifact result_metadata=%#v, want export_thread native file rendering metadata", artifactResultMeta)
	}
	artifactPresentation, ok := out.Artifact.Metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.tool_presentation=%T, want object: %#v", out.Artifact.Metadata["tool_presentation"], out.Artifact.Metadata)
	}
	if artifactPresentation["renderer_key"] != "file" || artifactPresentation["produces_file_artifact"] != true {
		t.Fatalf("artifact tool_presentation=%#v, want export_thread file artifact presentation", artifactPresentation)
	}

	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", out.Result)
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("result_metadata should be first-class Output.ResultMetadata, not nested in result: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(result["thread_id"])); got != thread.ID {
		t.Fatalf("thread_id=%q, want %q", got, thread.ID)
	}
	if got := strings.TrimSpace(anyToString(result["mime_type"])); got != threadtransfer.BundleMimeType {
		t.Fatalf("mime_type=%q, want %q", got, threadtransfer.BundleMimeType)
	}

	bundle, err := threadtransfer.ParseBundle([]byte(out.Artifact.Content))
	if err != nil {
		t.Fatalf("ParseBundle(artifact content): %v", err)
	}
	if bundle.Summary.MessageCount != 1 {
		t.Fatalf("bundle message_count=%d, want 1", bundle.Summary.MessageCount)
	}
	if bundle.Summary.ArtifactCount != 1 {
		t.Fatalf("bundle artifact_count=%d, want 1", bundle.Summary.ArtifactCount)
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].Title != "notes.txt" {
		t.Fatalf("bundle artifacts=%+v, want exported notes attachment", bundle.Artifacts)
	}
}
