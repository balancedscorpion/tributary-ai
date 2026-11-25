package threadtransfer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestExportAndImportThreadRewritesReferencedIDs(t *testing.T) {
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
	title := "Quarterly Review"
	summary := "Shareable summary"
	if err := st.UpdateThreadTitleSummary(ctx, thread.ID, &title, &summary); err != nil {
		t.Fatalf("UpdateThreadTitleSummary: %v", err)
	}

	userContent := "Please keep this artifact"
	userMsg, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		ID:      "msg-user-old",
		Role:    types.RoleUser,
		Content: &userContent,
	})
	if err != nil {
		t.Fatalf("AppendMessage(user): %v", err)
	}

	toolEnvelope, err := json.Marshal(map[string]any{
		"artifact_id":   "artifact-old",
		"tool_name":     "display_table",
		"tool_call_id":  "call-1",
		"ok":            true,
		"result_format": "json",
		"result": map[string]any{
			"thread_id":   thread.ID,
			"artifact_id": "artifact-old",
			"message_id":  "msg-user-old",
		},
	})
	if err != nil {
		t.Fatalf("Marshal(toolEnvelope): %v", err)
	}
	toolContent := string(toolEnvelope)
	toolName := "display_table"
	toolCallID := "call-1"
	contextPlan, err := json.Marshal(map[string]any{
		"thread_id":   thread.ID,
		"artifact_id": "artifact-old",
		"message_id":  "msg-user-old",
	})
	if err != nil {
		t.Fatalf("Marshal(contextPlan): %v", err)
	}
	hiddenContext := "internal retrieval summary"
	thinking := "private chain of thought"
	toolMsg, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		ID:                    "msg-tool-old",
		Role:                  types.RoleTool,
		Content:               &toolContent,
		ContextPlan:           contextPlan,
		HiddenContext:         &hiddenContext,
		Thinking:              &thinking,
		ThinkingComplete:      true,
		Name:                  &toolName,
		ToolCallID:            &toolCallID,
		ReferencedMessageIDs:  []string{"msg-user-old"},
		ReferencedArtifactIDs: []string{"artifact-old"},
	})
	if err != nil {
		t.Fatalf("AppendMessage(tool): %v", err)
	}

	artifactDir := filepath.Join(cfgDir, "artifacts")
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(artifactDir): %v", err)
	}
	rawBinary := []byte{0x00, 0x7F, 0xFF, 0x10}
	artifactPath := filepath.ToSlash(filepath.Join("artifacts", "artifact-old.bin"))
	if err := os.WriteFile(filepath.Join(cfgDir, filepath.FromSlash(artifactPath)), rawBinary, 0o600); err != nil {
		t.Fatalf("WriteFile(artifact): %v", err)
	}
	toolNameForArtifact := "display_table"
	okVal := true
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-old",
		ThreadID:    thread.ID,
		MessageID:   &toolMsg.ID,
		Type:        "file",
		Title:       "bundle.bin",
		ToolName:    &toolNameForArtifact,
		ToolCallID:  &toolCallID,
		OK:          &okVal,
		Storage:     "file",
		ContentPath: &artifactPath,
		ByteSize:    int64(len(rawBinary)),
		Metadata: map[string]any{
			"thread_id":   thread.ID,
			"artifact_id": "artifact-old",
			"message_id":  userMsg.ID,
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	hiddenContent := "hidden diagnostic note"
	hiddenMsg, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		ID:       "msg-hidden-old",
		Role:     types.RoleAssistant,
		Content:  &hiddenContent,
		UIHidden: true,
	})
	if err != nil {
		t.Fatalf("AppendMessage(hidden): %v", err)
	}
	hiddenArtifactText := "should not export"
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-hidden-old",
		ThreadID:    thread.ID,
		MessageID:   &hiddenMsg.ID,
		Type:        "file",
		Title:       "hidden.txt",
		Storage:     "mysql",
		ContentText: &hiddenArtifactText,
		ByteSize:    int64(len(hiddenArtifactText)),
	}); err != nil {
		t.Fatalf("InsertArtifact(hidden): %v", err)
	}

	fileName, payload, summaryOut, err := ExportThreadFile(ctx, st, cfgDir, thread.ID)
	if err != nil {
		t.Fatalf("ExportThreadFile: %v", err)
	}
	if !strings.HasSuffix(fileName, BundleFileExt) {
		t.Fatalf("fileName=%q, want suffix %q", fileName, BundleFileExt)
	}
	if summaryOut.MessageCount != 2 || summaryOut.ArtifactCount != 1 {
		t.Fatalf("summary=%+v, want 2 visible messages and 1 visible artifact", summaryOut)
	}

	bundle, err := ParseBundle(payload)
	if err != nil {
		t.Fatalf("ParseBundle(export): %v", err)
	}
	if len(bundle.Messages) != 2 {
		t.Fatalf("len(bundle.Messages)=%d, want 2 visible messages", len(bundle.Messages))
	}
	if len(bundle.Artifacts) != 1 || bundle.Artifacts[0].ContentEncoding != "base64" {
		t.Fatalf("artifact export=%+v, want base64 artifact payload", bundle.Artifacts)
	}
	if bundle.Messages[1].RunID != nil {
		t.Fatalf("exported RunID=%v, want nil", bundle.Messages[1].RunID)
	}
	if bundle.Messages[1].ContextPlan != nil {
		t.Fatalf("exported ContextPlan=%s, want nil", string(bundle.Messages[1].ContextPlan))
	}
	if bundle.Messages[1].HiddenContext != nil {
		t.Fatalf("exported HiddenContext=%q, want nil", stringPtrValue(bundle.Messages[1].HiddenContext))
	}
	if bundle.Messages[1].Thinking != nil || bundle.Messages[1].ThinkingComplete {
		t.Fatalf("exported Thinking=%q complete=%t, want stripped thinking", stringPtrValue(bundle.Messages[1].Thinking), bundle.Messages[1].ThinkingComplete)
	}
	if bundle.Messages[1].UIHidden {
		t.Fatalf("exported UIHidden=true, want false")
	}
	if strings.Contains(string(payload), "artifact-hidden-old") || strings.Contains(string(payload), hiddenContent) {
		t.Fatalf("export leaked hidden thread content: %s", string(payload))
	}

	imported, err := ImportThread(ctx, st, cfgDir, payload)
	if err != nil {
		t.Fatalf("ImportThread: %v", err)
	}
	if imported.Thread.ID == thread.ID {
		t.Fatalf("imported thread reused source id %q", imported.Thread.ID)
	}
	if imported.Thread.Title != title {
		t.Fatalf("imported title=%q, want %q", imported.Thread.Title, title)
	}

	importedMessages, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID:      imported.Thread.ID,
		Limit:         10,
		IncludeHidden: true,
		OrderBy:       "seq_asc",
	})
	if err != nil {
		t.Fatalf("ListMessages(imported): %v", err)
	}
	if len(importedMessages) != 2 {
		t.Fatalf("len(importedMessages)=%d, want 2", len(importedMessages))
	}
	if len(importedMessages[1].ReferencedArtifactIDs) != 1 {
		t.Fatalf("tool message refs=%v, want one artifact ref", importedMessages[1].ReferencedArtifactIDs)
	}
	newArtifactID := importedMessages[1].ReferencedArtifactIDs[0]
	if newArtifactID == "artifact-old" {
		t.Fatalf("artifact id was not remapped")
	}
	if len(importedMessages[1].ReferencedMessageIDs) != 1 || importedMessages[1].ReferencedMessageIDs[0] != importedMessages[0].ID {
		t.Fatalf("message refs=%v, want [%q]", importedMessages[1].ReferencedMessageIDs, importedMessages[0].ID)
	}
	if !strings.Contains(strings.TrimSpace(stringPtrValue(importedMessages[1].Content)), newArtifactID) {
		t.Fatalf("tool envelope content=%q, want remapped artifact id %q", stringPtrValue(importedMessages[1].Content), newArtifactID)
	}
	if importedMessages[1].ContextPlan != nil {
		t.Fatalf("imported ContextPlan=%s, want nil", string(importedMessages[1].ContextPlan))
	}
	if importedMessages[1].HiddenContext != nil {
		t.Fatalf("imported HiddenContext=%q, want nil", stringPtrValue(importedMessages[1].HiddenContext))
	}
	if importedMessages[1].Thinking != nil || importedMessages[1].ThinkingComplete || importedMessages[1].UIHidden {
		t.Fatalf("imported hidden fields not stripped: %+v", importedMessages[1])
	}

	importedArtifacts, err := st.ListArtifacts(ctx, imported.Thread.ID, 10)
	if err != nil {
		t.Fatalf("ListArtifacts(imported): %v", err)
	}
	if len(importedArtifacts) != 1 {
		t.Fatalf("len(importedArtifacts)=%d, want 1", len(importedArtifacts))
	}
	if importedArtifacts[0].ID != newArtifactID {
		t.Fatalf("imported artifact id=%q, want %q", importedArtifacts[0].ID, newArtifactID)
	}
	if got := strings.TrimSpace(stringMapValue(importedArtifacts[0].Metadata, "thread_id")); got != imported.Thread.ID {
		t.Fatalf("artifact metadata thread_id=%q, want %q", got, imported.Thread.ID)
	}
	if got := strings.TrimSpace(stringMapValue(importedArtifacts[0].Metadata, "artifact_id")); got != newArtifactID {
		t.Fatalf("artifact metadata artifact_id=%q, want %q", got, newArtifactID)
	}
	if got := strings.TrimSpace(stringMapValue(importedArtifacts[0].Metadata, "message_id")); got != importedMessages[0].ID {
		t.Fatalf("artifact metadata message_id=%q, want %q", got, importedMessages[0].ID)
	}
}

func TestParseBundleRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	raw := `{"bundle_type":"` + BundleType + `","schema_version":1,"exported_at":"2026-03-13T12:00:00Z","thread":{"id":"thread-1","title":"Thread","created_at":"2026-03-13T12:00:00Z","updated_at":"2026-03-13T12:00:00Z"},"messages":[],"artifacts":[],"summary":{"message_count":0,"artifact_count":0}}{"extra":true}`
	if _, err := ParseBundle([]byte(raw)); err == nil || !strings.Contains(err.Error(), "unexpected trailing JSON content") {
		t.Fatalf("ParseBundle err=%v, want unexpected trailing JSON content", err)
	}
}

func stringPtrValue(in *string) string {
	if in == nil {
		return ""
	}
	return *in
}

func stringMapValue(mm map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := mm[key]
		if !ok {
			continue
		}
		if asString, ok := value.(string); ok {
			return asString
		}
	}
	return ""
}
