package optionc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func TestBuildFileAttachmentTurnContextIncludesPreview(t *testing.T) {
	t.Parallel()

	isText := true
	previewAvailable := true
	previewTruncated := false
	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:       "artifact-1",
			FileName:         "notes.txt",
			MimeType:         "text/plain",
			SizeBytes:        int64PtrForTest(42),
			IsText:           &isText,
			PreviewAvailable: &previewAvailable,
			PreviewTruncated: &previewTruncated,
			PreviewText:      "first line\nsecond line",
		},
	})

	if !strings.Contains(context, "Provided preview:") {
		t.Fatalf("context missing preview heading:\n%s", context)
	}
	if !strings.Contains(context, "    first line\n    second line") {
		t.Fatalf("context missing indented preview:\n%s", context)
	}
	if !strings.Contains(context, "artifact_id=artifact-1") {
		t.Fatalf("context missing artifact id:\n%s", context)
	}
}

func TestBuildFileAttachmentTurnContextIncludesContentNote(t *testing.T) {
	t.Parallel()

	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:  "artifact-2",
			FileName:    "data.bin",
			MimeType:    "application/octet-stream",
			ContentNote: "Binary preview omitted.",
		},
	})

	if !strings.Contains(context, "Note: Binary preview omitted.") {
		t.Fatalf("context missing content note:\n%s", context)
	}
}

func TestBuildFileAttachmentTurnContextOmitsPreviewWhenNativeInputAvailable(t *testing.T) {
	t.Parallel()

	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:       "artifact-pdf",
			FileName:         "brief.pdf",
			MimeType:         "application/pdf",
			InlineDataBase64: "JVBERi0xLjQ=",
			PreviewText:      "decoded extraction preview should not be sent",
		},
	}, fileAttachmentTurnContextOptions{
		NativeInputAvailable: func(types.FileAttachment) bool { return true },
	})

	if !strings.Contains(context, "native_file_input=true") {
		t.Fatalf("context missing native file marker:\n%s", context)
	}
	if strings.Contains(context, "Provided preview:") || strings.Contains(context, "decoded extraction preview") {
		t.Fatalf("context included extracted preview despite native input:\n%s", context)
	}
	if !strings.Contains(context, "original file bytes are attached directly to the provider as a file input") {
		t.Fatalf("context missing direct-file guidance:\n%s", context)
	}
	if !strings.Contains(context, "Use the native file input before any workspace or attachment-inspection tools.") {
		t.Fatalf("context missing native-file precedence guidance:\n%s", context)
	}
}

func TestBuildFileAttachmentTurnContextOmitsPresentationPreviewWhenNativeInputAvailable(t *testing.T) {
	t.Parallel()

	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:       "artifact-pptx",
			FileName:         "strategy.pptx",
			MimeType:         "application/vnd.openxmlformats-officedocument.presentationml.presentation",
			FileKind:         "presentation",
			InlineDataBase64: "UEsDBAo=",
			PreviewAvailable: boolPtrForTest(true),
			PreviewText:      "garbled extracted OOXML text should not be sent",
		},
	}, fileAttachmentTurnContextOptions{
		NativeInputAvailable: func(types.FileAttachment) bool { return true },
	})

	if !strings.Contains(context, "native_file_input=true") {
		t.Fatalf("context missing native file marker:\n%s", context)
	}
	if strings.Contains(context, "Provided preview:") || strings.Contains(context, "garbled extracted") {
		t.Fatalf("context included presentation preview despite native input:\n%s", context)
	}
	if !strings.Contains(context, "do not use workspace/project files, local attachment readers, or extracted previews as substitutes") {
		t.Fatalf("context missing substitution warning:\n%s", context)
	}
}

func TestBuildFileAttachmentTurnContextOmitsPreviewWhenNativeImageInputAvailable(t *testing.T) {
	t.Parallel()

	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:       "artifact-png",
			FileName:         "diagram.png",
			MimeType:         "image/png",
			FileKind:         "image",
			PreviewAvailable: boolPtrForTest(true),
			PreviewText:      "image metadata fallback should not be sent as content",
		},
	}, fileAttachmentTurnContextOptions{
		NativeImageInputAvailable: func(types.FileAttachment) bool { return true },
	})

	if !strings.Contains(context, "native_image_input=true") {
		t.Fatalf("context missing native image marker:\n%s", context)
	}
	if strings.Contains(context, "Provided preview:") || strings.Contains(context, "metadata fallback") {
		t.Fatalf("context included preview despite native image input:\n%s", context)
	}
	if !strings.Contains(context, "original image bytes are attached directly to the provider as an image input") {
		t.Fatalf("context missing direct-image guidance:\n%s", context)
	}
}

func TestBuildFileAttachmentTurnContextIncludesOCRSupplementWhenNativeImageInputAvailable(t *testing.T) {
	t.Parallel()

	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:       "artifact-png",
			FileName:         "diagram.png",
			MimeType:         "image/png",
			FileKind:         "image",
			PreviewAvailable: boolPtrForTest(true),
			PreviewText:      "PNG_SENTINEL\nLIME-88 visible image text",
			ContentNote:      "Extracted text preview loaded from thread_uploads/thread/artifact.extract/ocr.txt.",
		},
	}, fileAttachmentTurnContextOptions{
		NativeImageInputAvailable: func(types.FileAttachment) bool { return true },
	})

	if !strings.Contains(context, "native_image_input=true") {
		t.Fatalf("context missing native image marker:\n%s", context)
	}
	if !strings.Contains(context, "OCR text supplement") || !strings.Contains(context, "PNG_SENTINEL") || !strings.Contains(context, "LIME-88") {
		t.Fatalf("context missing OCR supplement:\n%s", context)
	}
	if strings.Contains(context, "Provided preview:") {
		t.Fatalf("context used generic preview label for OCR supplement:\n%s", context)
	}
}

func TestBuildFileAttachmentTurnContextIncludesSpreadsheetSupplementWhenNativeInputAvailable(t *testing.T) {
	t.Parallel()

	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:       "artifact-xlsx",
			FileName:         "model.xlsx",
			MimeType:         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			FileKind:         "spreadsheet",
			InlineDataBase64: "UEsDBAo=",
			PreviewAvailable: boolPtrForTest(true),
			PreviewText:      "| Row | A | B | C |\n| 1 | XLSX_SENTINEL | Alpha | 120 |\n| 2 | Total | | 350 |",
		},
	}, fileAttachmentTurnContextOptions{
		NativeInputAvailable: func(types.FileAttachment) bool { return true },
	})

	if !strings.Contains(context, "native_file_input=true") {
		t.Fatalf("context missing native file marker:\n%s", context)
	}
	if !strings.Contains(context, "Extracted spreadsheet preview supplement") || !strings.Contains(context, "XLSX_SENTINEL") || !strings.Contains(context, "350") {
		t.Fatalf("context missing spreadsheet supplement:\n%s", context)
	}
	if strings.Contains(context, "Provided preview:") {
		t.Fatalf("context used generic preview label for spreadsheet supplement:\n%s", context)
	}
}

func TestBuildFileAttachmentTurnContextIncludesKeynoteSupplementWhenNativeInputAvailable(t *testing.T) {
	t.Parallel()

	context := buildFileAttachmentTurnContext([]types.FileAttachment{
		{
			ArtifactID:       "artifact-key",
			FileName:         "deck.key",
			MimeType:         "application/vnd.apple.keynote",
			FileKind:         "presentation",
			InlineDataBase64: "S0VZ",
			PreviewAvailable: boolPtrForTest(true),
			PreviewText:      "# deck.key\n\n## OCR Text\n\n### Slide 1\n\nKEYNOTE_SENTINEL plan update",
		},
	}, fileAttachmentTurnContextOptions{
		NativeInputAvailable: func(types.FileAttachment) bool { return true },
	})

	if !strings.Contains(context, "native_file_input=true") {
		t.Fatalf("context missing native file marker:\n%s", context)
	}
	if !strings.Contains(context, "Extracted Keynote preview/OCR supplement") || !strings.Contains(context, "KEYNOTE_SENTINEL") {
		t.Fatalf("context missing Keynote supplement:\n%s", context)
	}
	if strings.Contains(context, "Provided preview:") {
		t.Fatalf("context used generic preview label for Keynote supplement:\n%s", context)
	}
}

func TestFileAttachmentExtractionPreviewPathsUsesNestedSpreadsheetSheetOutputs(t *testing.T) {
	t.Parallel()

	paths := fileAttachmentExtractionPreviewPaths(map[string]any{
		"kind":         "spreadsheet",
		"rootPath":     "thread_uploads/thread/artifact.extract",
		"markdownPath": "thread_uploads/thread/artifact.extract/index/workbook.md",
		"outputs": map[string]any{
			"sheetOutputs": []any{"worksheets/sheet1/preview.md", "worksheets/sheet1/cells-0001.jsonl"},
		},
	})

	if len(paths) < 2 {
		t.Fatalf("expected multiple preview paths, got %#v", paths)
	}
	if paths[0] != "thread_uploads/thread/artifact.extract/index/workbook.md" {
		t.Fatalf("first preview path=%q, want workbook markdown; paths=%#v", paths[0], paths)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, ".jsonl") {
			t.Fatalf("spreadsheet preview paths should not include cell JSONL files: %#v", paths)
		}
	}
}

func TestHydrateReferencedFileAttachmentsCombinesVisibleSpreadsheetPreviews(t *testing.T) {
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

	rootRel := "thread_uploads/" + thread.ID + "/artifact-xlsx.extract"
	files := map[string]string{
		rootRel + "/index/workbook.md": "Workbook index\n\n## Lists\nPreview: `worksheets/sheet1/preview.md`\n\n## Cap Table\nPreview: `worksheets/sheet3/preview.md`",
		rootRel + "/workbook.json": `{
  "sheets": [
    {"name": "Lists", "state": "hidden", "previewPath": "worksheets/sheet1/preview.md"},
    {"name": "Cap Table", "state": "visible", "previewPath": "worksheets/sheet3/preview.md"}
  ]
}`,
		rootRel + "/worksheets/sheet1/preview.md": "# Lists\n\nLISTS_SENTINEL currency codes only",
		rootRel + "/worksheets/sheet3/preview.md": "# Cap Table\n\nFounder Name (1) | 1234567 | CAP_TABLE_SENTINEL",
	}
	for rel, content := range files {
		full := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	toolName := types.ThreadFileAttachmentToolName
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "artifact-xlsx",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "model.xlsx",
		ToolName: &toolName,
		Storage:  "file",
		ByteSize: 128,
		Metadata: map[string]any{
			"source":           types.ChatUploadAttachmentSource,
			"mimeType":         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"fileKind":         "spreadsheet",
			"previewAvailable": true,
			"previewText":      "Binary preview omitted.",
			"extraction": map[string]any{
				"kind":         "spreadsheet",
				"rootPath":     rootRel,
				"markdownPath": rootRel + "/index/workbook.md",
				"workbookPath": rootRel + "/workbook.json",
				"sheetOutputs": []any{
					rootRel + "/worksheets/sheet1/preview.md",
					rootRel + "/worksheets/sheet1/cells-0001.jsonl",
					rootRel + "/worksheets/sheet3/preview.md",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	got := manager.hydrateReferencedFileAttachments(ctx, thread.ID, []types.FileAttachment{{ArtifactID: artifact.ID}})
	if len(got) != 1 {
		t.Fatalf("hydrated attachments len=%d, want 1", len(got))
	}
	preview := got[0].PreviewText
	if !strings.Contains(preview, "Workbook index") || !strings.Contains(preview, "CAP_TABLE_SENTINEL") {
		t.Fatalf("combined spreadsheet preview missing workbook/cap table content:\n%s", preview)
	}
	capTableIdx := strings.Index(preview, "CAP_TABLE_SENTINEL")
	listsIdx := strings.Index(preview, "LISTS_SENTINEL")
	if capTableIdx < 0 || listsIdx < 0 || capTableIdx > listsIdx {
		t.Fatalf("visible sheet preview should appear before hidden sheet preview:\n%s", preview)
	}
	if strings.Contains(preview, "cells-0001") {
		t.Fatalf("combined spreadsheet preview included cell JSONL path/content:\n%s", preview)
	}
}

func TestFileAttachmentNativeInputAvailabilityRespectsProviderMode(t *testing.T) {
	t.Parallel()

	profileID := "legacy"
	settings := config.AppSettings{
		Provider: config.ProviderSelection{ProviderID: "compatible", Model: "model", ProfileID: &profileID},
		CompatibleProfiles: []config.CompatibleProfile{{
			ID:      profileID,
			ApiType: "chat_completions",
		}},
	}
	attachment := types.FileAttachment{
		ArtifactID:       "artifact-pdf",
		FileName:         "brief.pdf",
		MimeType:         "application/pdf",
		InlineDataBase64: "JVBERi0xLjQ=",
	}
	spreadsheetAttachment := types.FileAttachment{
		ArtifactID:       "artifact-xlsx",
		FileName:         "model.xlsx",
		MimeType:         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		InlineDataBase64: "UEsDBAo=",
	}
	presentationAttachment := types.FileAttachment{
		ArtifactID:       "artifact-pptx",
		FileName:         "deck.pptx",
		MimeType:         "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		InlineDataBase64: "UEsDBAo=",
	}
	keynoteAttachment := types.FileAttachment{
		ArtifactID:       "artifact-key",
		FileName:         "deck.key",
		MimeType:         "application/vnd.apple.keynote",
		InlineDataBase64: "S0VZ",
	}
	imageAttachment := types.FileAttachment{
		ArtifactID:       "artifact-png",
		FileName:         "diagram.png",
		MimeType:         "image/png",
		InlineDataBase64: "iVBORw0KGgo=",
	}

	nativeDefault := fileAttachmentNativeInputAvailability(settings, "compatible", &profileID, []types.FileAttachment{attachment, spreadsheetAttachment, presentationAttachment, keynoteAttachment, imageAttachment})
	if !nativeDefault[attachment.ArtifactID] {
		t.Fatalf("chat completions compatible profile did not default PDF to native input: %#v", nativeDefault)
	}
	if !nativeDefault[spreadsheetAttachment.ArtifactID] {
		t.Fatalf("chat completions compatible profile did not default XLSX to native input: %#v", nativeDefault)
	}
	if !nativeDefault[presentationAttachment.ArtifactID] {
		t.Fatalf("chat completions compatible profile did not default PPTX to native input: %#v", nativeDefault)
	}
	if !nativeDefault[keynoteAttachment.ArtifactID] {
		t.Fatalf("chat completions compatible profile did not default Keynote to native input: %#v", nativeDefault)
	}
	if nativeDefault[imageAttachment.ArtifactID] {
		t.Fatalf("chat completions compatible profile marked PNG as native file input instead of native image input: %#v", nativeDefault)
	}

	nativeImages := fileAttachmentNativeImageInputAvailability(settings, "compatible", &profileID, []types.FileAttachment{imageAttachment})
	if !nativeImages[imageAttachment.ArtifactID] {
		t.Fatalf("chat completions compatible profile did not mark PNG as native image input: %#v", nativeImages)
	}

	settings.CompatibleProfiles[0].FileInputMode = "native_pdf_only"
	nativePDFOnly := fileAttachmentNativeInputAvailability(settings, "compatible", &profileID, []types.FileAttachment{attachment, spreadsheetAttachment, presentationAttachment, keynoteAttachment})
	if !nativePDFOnly[attachment.ArtifactID] {
		t.Fatalf("native_pdf_only compatible profile did not keep PDF native input: %#v", nativePDFOnly)
	}
	if nativePDFOnly[spreadsheetAttachment.ArtifactID] {
		t.Fatalf("native_pdf_only compatible profile marked XLSX as native input: %#v", nativePDFOnly)
	}
	if nativePDFOnly[presentationAttachment.ArtifactID] {
		t.Fatalf("native_pdf_only compatible profile marked PPTX as native input: %#v", nativePDFOnly)
	}
	if nativePDFOnly[keynoteAttachment.ArtifactID] {
		t.Fatalf("native_pdf_only compatible profile marked Keynote as native input: %#v", nativePDFOnly)
	}

	binaryAttachment := types.FileAttachment{
		ArtifactID:       "artifact-zip",
		FileName:         "archive.zip",
		MimeType:         "application/zip",
		InlineDataBase64: "UEsDBAo=",
	}
	anthropicNative := fileAttachmentNativeInputAvailability(config.AppSettings{}, "anthropic", nil, []types.FileAttachment{binaryAttachment})
	if !anthropicNative[binaryAttachment.ArtifactID] {
		t.Fatalf("anthropic did not mark arbitrary inline file as native input: %#v", anthropicNative)
	}
}

func TestFilterLocalAttachmentToolDefinitionsRemovesAttachmentReaders(t *testing.T) {
	t.Parallel()

	defs := []types.ToolDefinition{
		{Function: types.ToolFunctionDefinition{Name: "inspect_attachment"}},
		{Function: types.ToolFunctionDefinition{Name: "read_attachment"}},
		{Function: types.ToolFunctionDefinition{Name: "search_attachment"}},
		{Function: types.ToolFunctionDefinition{Name: "get_artifact"}},
		{Function: types.ToolFunctionDefinition{Name: "get_artifact_chunk"}},
		{Function: types.ToolFunctionDefinition{Name: "search_artifact_content"}},
		{Function: types.ToolFunctionDefinition{Name: "get_json_artifact"}},
		{Function: types.ToolFunctionDefinition{Name: "query_database"}},
	}

	filtered := filterLocalAttachmentToolDefinitions(defs)
	got := make([]string, 0, len(filtered))
	for _, def := range filtered {
		got = append(got, def.Function.Name)
	}
	if strings.Join(got, ",") != "query_database" {
		t.Fatalf("filtered tools=%v, want query_database only", got)
	}
}

func TestFilterProjectFileToolDefinitionsWithoutProjectRemovesProjectReaders(t *testing.T) {
	t.Parallel()

	defs := []types.ToolDefinition{
		{Function: types.ToolFunctionDefinition{Name: "list_files"}},
		{Function: types.ToolFunctionDefinition{Name: "read_file"}},
		{Function: types.ToolFunctionDefinition{Name: "search_files"}},
		{Function: types.ToolFunctionDefinition{Name: "search_threads"}},
		{Function: types.ToolFunctionDefinition{Name: "query_database"}},
	}

	filtered := filterProjectFileToolDefinitionsWithoutProject(defs)
	got := make([]string, 0, len(filtered))
	for _, def := range filtered {
		got = append(got, def.Function.Name)
	}
	if strings.Join(got, ",") != "search_threads,query_database" {
		t.Fatalf("filtered tools=%v, want search_threads and query_database only", got)
	}
}

func TestHydrateReferencedFileAttachmentsLoadsExtractionPreview(t *testing.T) {
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

	markdownRel := "thread_uploads/" + thread.ID + "/artifact-1.extract/document.md"
	markdownFull := filepath.Join(cfgDir, filepath.FromSlash(markdownRel))
	if err := os.MkdirAll(filepath.Dir(markdownFull), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(markdownFull, []byte("extracted heading\n\nimportant workbook fact"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	toolName := types.ThreadFileAttachmentToolName
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "artifact-1",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "model.xlsx",
		ToolName: &toolName,
		Storage:  "file",
		ByteSize: 128,
		Metadata: map[string]any{
			"source":      types.ChatUploadAttachmentSource,
			"mimeType":    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"extraction":  map[string]any{"markdownPath": markdownRel},
			"contentNote": "",
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	got := manager.hydrateReferencedFileAttachments(ctx, thread.ID, []types.FileAttachment{{ArtifactID: artifact.ID}})
	if len(got) != 1 {
		t.Fatalf("hydrated attachments len=%d, want 1", len(got))
	}
	if !strings.Contains(got[0].PreviewText, "important workbook fact") {
		t.Fatalf("preview text did not include extraction markdown: %#v", got[0])
	}
	if got[0].PreviewAvailable == nil || !*got[0].PreviewAvailable {
		t.Fatalf("PreviewAvailable=%v, want true", got[0].PreviewAvailable)
	}
	if !strings.Contains(got[0].ContentNote, markdownRel) {
		t.Fatalf("ContentNote=%q, want source path", got[0].ContentNote)
	}
}

func TestHydrateReferencedFileAttachmentsPrefersStructuredExtractionPreview(t *testing.T) {
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

	markdownRel := "thread_uploads/" + thread.ID + "/artifact-structured.extract/workbook.md"
	markdownFull := filepath.Join(cfgDir, filepath.FromSlash(markdownRel))
	if err := os.MkdirAll(filepath.Dir(markdownFull), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(markdownFull, []byte("extracted workbook preview"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	toolName := types.ThreadFileAttachmentToolName
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "artifact-structured",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "model.xlsx",
		ToolName: &toolName,
		Storage:  "file",
		ByteSize: 128,
		Metadata: map[string]any{
			"source":           types.ChatUploadAttachmentSource,
			"mimeType":         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"fileKind":         "spreadsheet",
			"previewAvailable": true,
			"previewText":      "Binary or unsupported text preview omitted.",
			"extraction":       map[string]any{"markdownPath": markdownRel},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	got := manager.hydrateReferencedFileAttachments(ctx, thread.ID, []types.FileAttachment{{ArtifactID: artifact.ID}})
	if len(got) != 1 {
		t.Fatalf("hydrated attachments len=%d, want 1", len(got))
	}
	if got[0].PreviewText != "extracted workbook preview" {
		t.Fatalf("PreviewText=%q, want extraction preview", got[0].PreviewText)
	}
}

func TestFileAttachmentNativeInputCandidateCoversFileServiceDocumentFamilies(t *testing.T) {
	t.Parallel()

	text := true
	tests := []types.FileAttachment{
		{FileName: "brief.pdf", MimeType: "application/pdf"},
		{FileName: "table.csv", MimeType: "text/csv"},
		{FileName: "table.tsv", MimeType: "text/tab-separated-values"},
		{FileName: "table.tab"},
		{FileName: "data.json", MimeType: "application/json"},
		{FileName: "events.jsonl", MimeType: "application/x-ndjson"},
		{FileName: "page.html", MimeType: "text/html"},
		{FileName: "notes.txt", MimeType: "text/plain"},
		{FileName: "readme.md", MimeType: "text/markdown"},
		{FileName: "debug.log", IsText: &text},
		{FileName: "config.yaml", MimeType: "application/yaml"},
		{FileName: "config.yml"},
		{FileName: "config.toml", MimeType: "application/toml"},
		{FileName: "config.ini", IsText: &text},
		{FileName: "settings.cfg", IsText: &text},
		{FileName: "app.conf", IsText: &text},
		{FileName: "feed.xml", MimeType: "application/xml"},
		{FileName: "icon.svg", MimeType: "image/svg+xml"},
		{FileName: "query.sql"},
		{FileName: "main.go"},
		{FileName: "app.js", MimeType: "application/javascript"},
		{FileName: "view.jsx"},
		{FileName: "types.ts"},
		{FileName: "component.tsx"},
		{FileName: "script.py"},
		{FileName: "task.rb"},
		{FileName: "lib.rs"},
		{FileName: "Main.java"},
		{FileName: "style.css"},
		{FileName: "style.scss"},
		{FileName: "run.sh", MimeType: "application/x-sh"},
		{FileName: "run.bash"},
		{FileName: "run.zsh"},
		{FileName: "main.c"},
		{FileName: "main.h"},
		{FileName: "main.cpp"},
		{FileName: "main.hpp"},
		{FileName: "brief.rtf", MimeType: "application/rtf"},
		{FileName: "brief.doc", MimeType: "application/msword"},
		{FileName: "brief.docx", MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{FileName: "model.xls", MimeType: "application/vnd.ms-excel"},
		{FileName: "model.xlsx", MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{FileName: "deck.ppt", MimeType: "application/vnd.ms-powerpoint"},
		{FileName: "deck.pptx", MimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation"},
		{FileName: "deck.key", MimeType: "application/vnd.apple.keynote"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.FileName, func(t *testing.T) {
			t.Parallel()
			if !fileAttachmentNativeInputCandidate(tt) {
				t.Fatalf("fileAttachmentNativeInputCandidate(%#v)=false, want true", tt)
			}
		})
	}
}

func int64PtrForTest(v int64) *int64 {
	return &v
}

func boolPtrForTest(v bool) *bool {
	return &v
}
