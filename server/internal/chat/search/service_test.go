package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestRebuildGeneratesTurnAndArtifactDocs(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("new json store: %v", err)
	}

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-4.1", nil, "sys", 8)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	user := "Find trend changes by quarter."
	if _, err := st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		ID:      types.NewUUID(),
		Role:    types.RoleUser,
		Content: &user,
	}); err != nil {
		t.Fatalf("append user: %v", err)
	}

	assistant := "Revenue rises in Q3 and Q4."
	toolCalls := []types.ToolCall{{ID: "tool_1", Name: "search_docs", Args: map[string]any{"q": "revenue"}}}
	assistantMsg, err := st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		ID:        types.NewUUID(),
		Role:      types.RoleAssistant,
		Content:   &assistant,
		ToolCalls: toolCalls,
	})
	if err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	artifactContent := strings.Repeat("row,", 1500)
	toolName := "search_docs"
	toolCallID := "tool_1"
	okVal := true
	artifactID := types.NewUUID()
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          artifactID,
		ThreadID:    thread.ID,
		MessageID:   &assistantMsg.ID,
		Type:        "data",
		Title:       "Quarterly Revenue",
		ToolName:    &toolName,
		ToolCallID:  &toolCallID,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &artifactContent,
		ByteSize:    int64(len(artifactContent)),
		Metadata:    map[string]any{"rows": 1500},
	}); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}

	svc, err := NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	turnDoc, ok := svc.GetDoc("turn:" + run.ID)
	if !ok {
		t.Fatalf("expected turn doc")
	}
	if !strings.Contains(turnDoc.Text, "Find trend changes by quarter.") {
		t.Fatalf("turn doc missing user text: %s", turnDoc.Text)
	}
	if !strings.Contains(turnDoc.Text, "Revenue rises in Q3 and Q4.") {
		t.Fatalf("turn doc missing assistant text: %s", turnDoc.Text)
	}
	if !strings.Contains(turnDoc.Text, "Quarterly Revenue") {
		t.Fatalf("turn doc missing artifact title: %s", turnDoc.Text)
	}

	artifactDoc, ok := svc.GetDoc("artifact:" + artifactID)
	if !ok {
		t.Fatalf("expected artifact doc")
	}
	if !strings.Contains(artifactDoc.Text, "Quarterly Revenue") {
		t.Fatalf("artifact doc missing title: %s", artifactDoc.Text)
	}
	if len(artifactDoc.Text) >= len(artifactContent)+500 {
		t.Fatalf("artifact doc should store excerpt, got len=%d", len(artifactDoc.Text))
	}
}

func TestFileAttachmentArtifactSearchUsesExtractionInsteadOfRawBytes(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("new json store: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("create thread: %v", err)
	}

	artifactID := types.NewUUID()
	rawRel := "thread_uploads/" + thread.ID + "/" + artifactID + ".xlsx"
	rootRel := "thread_uploads/" + thread.ID + "/" + artifactID + ".extract"
	files := map[string]string{
		rawRel:                         "\x00\x00RAW_BINARY_SENTINEL\x00\x00",
		rootRel + "/index/workbook.md": "Workbook index",
		rootRel + "/worksheets/sheet1/preview.md": "# Lists\n\nCurrency only",
		rootRel + "/worksheets/sheet3/preview.md": "# Cap Table\n\nFounder Name (1) 1234567 SEARCH_CAP_TABLE_SENTINEL",
	}
	for rel, content := range files {
		full := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	toolName := types.ThreadFileAttachmentToolName
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          artifactID,
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "model.xlsx",
		ToolName:    &toolName,
		Storage:     "file",
		ContentPath: &rawRel,
		ByteSize:    int64(len(files[rawRel])),
		Metadata: map[string]any{
			"source":      types.ChatUploadAttachmentSource,
			"fileKind":    "spreadsheet",
			"previewText": "Binary preview omitted.",
			"extraction": map[string]any{
				"kind":         "spreadsheet",
				"rootPath":     rootRel,
				"markdownPath": rootRel + "/index/workbook.md",
				"sheetOutputs": []any{
					rootRel + "/worksheets/sheet1/preview.md",
					rootRel + "/worksheets/sheet1/cells-0001.jsonl",
					rootRel + "/worksheets/sheet3/preview.md",
				},
			},
		},
	}); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}

	svc, err := NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.Rebuild(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	artifactDoc, ok := svc.GetDoc("artifact:" + artifactID)
	if !ok {
		t.Fatalf("expected artifact doc")
	}
	if !strings.Contains(artifactDoc.Text, "SEARCH_CAP_TABLE_SENTINEL") {
		t.Fatalf("artifact doc missing extracted sheet preview: %s", artifactDoc.Text)
	}
	if strings.Contains(artifactDoc.Text, "RAW_BINARY_SENTINEL") {
		t.Fatalf("artifact doc used raw binary content: %s", artifactDoc.Text)
	}
}
