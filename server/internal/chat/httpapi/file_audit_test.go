package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestHandleListThreadFilesReturnsFileArtifactsOnly(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "file-1",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "report.pdf",
		ToolName: &toolName,
		Storage:  "file",
		Metadata: map[string]any{
			"source":       types.ChatUploadAttachmentSource,
			"mimeType":     "application/pdf",
			"fileKind":     "pdf",
			"previewText":  "Quarterly report",
			"sizeBytes":    1234,
			"capabilities": map[string]any{"canPreviewText": true},
		},
		ByteSize: 1234,
	}); err != nil {
		t.Fatalf("InsertArtifact file: %v", err)
	}
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "data-1",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "table",
		Storage:     "mysql",
		ContentText: strPtrForFileAuditTest("[]"),
		ByteSize:    2,
	}); err != nil {
		t.Fatalf("InsertArtifact data: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one file artifact", payload.Files)
	}
	file := payload.Files[0]
	if file["artifact_id"] != "file-1" || file["title"] != "report.pdf" {
		t.Fatalf("file row=%#v, want report file", file)
	}
	if file["source_label"] != "Chat upload" || file["downloadable"] != true {
		t.Fatalf("file source/download=%#v/%#v, want chat upload downloadable", file["source_label"], file["downloadable"])
	}
	if file["preview_text"] != "Quarterly report" || file["mime_type"] != "application/pdf" {
		t.Fatalf("file preview/mime=%#v/%#v", file["preview_text"], file["mime_type"])
	}
}

func TestHandleListThreadFilesIncludesGeneratedImages(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := "generate_image"
	content := "data:image/png;base64,iVBORw0KGgo="
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "generated-image-1",
		ThreadID:    thread.ID,
		Type:        "image",
		Title:       "Terrarium city",
		ToolName:    &toolName,
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    1234,
		Metadata: map[string]any{
			"source":    "generated_image",
			"mimeType":  "image/png",
			"sizeBytes": 1234,
			"prompt":    "An isometric city inside a terrarium",
		},
	}); err != nil {
		t.Fatalf("InsertArtifact generated image: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()
	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one generated image", payload.Files)
	}
	file := payload.Files[0]
	if file["artifact_id"] != "generated-image-1" || file["type"] != "image" {
		t.Fatalf("generated image row=%#v", file)
	}
	if file["source_label"] != "Generated" || file["file_kind"] != "image" || file["downloadable"] != true {
		t.Fatalf("generated image provenance=%#v", file)
	}
	artifact, ok := file["artifact"].(map[string]any)
	if !ok || artifact["type"] != "image" {
		t.Fatalf("generated image artifact=%#v", file["artifact"])
	}
}

func TestHandleListThreadFilesReturnsRemoteConnectorReferences(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := st.UpdateThreadTitleSummary(ctx, thread.ID, strPtrForFileAuditTest("SharePoint search"), nil); err != nil {
		t.Fatalf("UpdateThreadTitleSummary: %v", err)
	}
	toolName := "sharepoint_search"
	toolCallID := "tool-call-sharepoint-search"
	result := `{
		"items": [
			{
				"name": "Vendor SOW.docx",
				"web_url": "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				"sizeBytes": 48213
			}
		]
	}`
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "tool-result-1",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "SharePoint search result",
		ToolName:    &toolName,
		ToolCallID:  &toolCallID,
		Storage:     "mysql",
		ContentText: &result,
		Metadata: map[string]any{
			"source": "external_tool",
			"tool_args": map[string]any{
				"query": "vendor SOW contracts",
			},
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_search",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact search result: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one remote reference", payload.Files)
	}
	file := payload.Files[0]
	if file["record_kind"] != "remote_reference" || file["availability"] != "remote_only" {
		t.Fatalf("remote kind/availability=%#v/%#v; file=%#v", file["record_kind"], file["availability"], file)
	}
	if file["cached"] != false || file["openable"] != false || file["downloadable"] != false || file["removable"] != true {
		t.Fatalf("remote booleans cached/openable/downloadable/removable=%#v/%#v/%#v/%#v; file=%#v", file["cached"], file["openable"], file["downloadable"], file["removable"], file)
	}
	if file["title"] != "Vendor SOW.docx" || file["source_label"] != "SharePoint" || file["request_tool_name"] != "sharepoint_read" {
		t.Fatalf("remote identity title/source/tool=%#v/%#v/%#v; file=%#v", file["title"], file["source_label"], file["request_tool_name"], file)
	}
	if file["remote_url"] != "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx" {
		t.Fatalf("remote_url=%#v; file=%#v", file["remote_url"], file)
	}
	if file["remote_path"] != "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx" {
		t.Fatalf("remote_path=%#v; file=%#v", file["remote_path"], file)
	}
	if file["tool_call_id"] != toolCallID || file["thread_title"] != "SharePoint search" {
		t.Fatalf("tool/thread provenance=%#v/%#v; file=%#v", file["tool_call_id"], file["thread_title"], file)
	}
	if prompt, ok := file["request_download_prompt"].(string); !ok || !strings.Contains(prompt, "sharepoint_read") || !strings.Contains(prompt, "Vendor SOW.docx") {
		t.Fatalf("request_download_prompt=%#v; file=%#v", file["request_download_prompt"], file)
	}
	args, ok := file["request_tool_args"].(map[string]any)
	if !ok {
		t.Fatalf("request_tool_args=%#v, want object", file["request_tool_args"])
	}
	if args["query"] != "vendor SOW contracts" || args["source_url"] != file["remote_url"] || args["remote_path"] != file["remote_path"] {
		t.Fatalf("request_tool_args=%#v, want query/source_url/remote_path", args)
	}
}

func TestHandleListThreadFilesOmitsRemoteReferenceWhenCachedByLocator(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	downloadToolName := "sharepoint_read"
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "cached-sharepoint-file",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "Vendor SOW.docx",
		ToolName: &downloadToolName,
		Storage:  "file",
		Metadata: map[string]any{
			"source": "external_tool",
			"tool_args": map[string]any{
				"item_url":            "https://contoso.sharepoint.com/sites/streaming/Shared%20Documents/Contracts/Vendor%20SOW.docx",
				"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
			},
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_read",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact cached file: %v", err)
	}

	searchToolName := "sharepoint_search"
	result := `{
		"items": [
			{
				"name": "Vendor SOW.docx",
				"web_url": "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
		]
	}`
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "sharepoint-search-result",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "SharePoint search result",
		ToolName:    &searchToolName,
		Storage:     "mysql",
		ContentText: &result,
		Metadata: map[string]any{
			"source": "external_tool",
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_search",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact search result: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want only the cached file row", payload.Files)
	}
	file := payload.Files[0]
	if file["record_kind"] != "cached_file" || file["artifact_id"] != "cached-sharepoint-file" {
		t.Fatalf("file row=%#v, want cached file without remote duplicate", file)
	}
}

func TestHandleListThreadFilesDedupeIndexesCachedFilesBeyondCurrentPage(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	downloadToolName := "sharepoint_read"
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "cached-vendor-sow",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "Vendor SOW.docx",
		ToolName: &downloadToolName,
		Storage:  "file",
		Metadata: map[string]any{
			"source": "external_tool",
			"tool_args": map[string]any{
				"item_url":            "https://contoso.sharepoint.com/sites/streaming/Shared%20Documents/Contracts/Vendor%20SOW.docx",
				"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
			},
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_read",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact cached target: %v", err)
	}
	time.Sleep(time.Millisecond)
	for _, id := range []string{"newer-cached-file-a", "newer-cached-file-b"} {
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:       id,
			ThreadID: thread.ID,
			Type:     "file",
			Title:    id + ".md",
			ToolName: &downloadToolName,
			Storage:  "file",
			Metadata: map[string]any{
				"source": "external_tool",
				"result_metadata": map[string]any{
					"family":           "sharepoint",
					"product":          "sharepoint",
					"source_tool_name": "sharepoint_read",
				},
			},
		}); err != nil {
			t.Fatalf("InsertArtifact %s: %v", id, err)
		}
		time.Sleep(time.Millisecond)
	}

	searchToolName := "sharepoint_search"
	result := `{
		"items": [
			{
				"name": "Vendor SOW.docx",
				"web_url": "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
		]
	}`
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "newer-sharepoint-search-result",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "SharePoint search result",
		ToolName:    &searchToolName,
		Storage:     "mysql",
		ContentText: &result,
		Metadata: map[string]any{
			"source": "external_tool",
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_search",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact search result: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one paged cached file", payload.Files)
	}
	file := payload.Files[0]
	if file["record_kind"] != "cached_file" || file["artifact_id"] != "newer-cached-file-b" {
		t.Fatalf("file row=%#v, want newest cached file; remote duplicate must not outrank it", file)
	}
	if payload.Scope["candidate_count"] != float64(3) {
		t.Fatalf("scope=%#v, want only three cached-file candidates after remote duplicate suppression", payload.Scope)
	}
}

func TestFileAuditLocatorKeyNormalization(t *testing.T) {
	escaped := fileAuditLocatorKey("url", "https://contoso.sharepoint.com/sites/streaming/Shared%20Documents/Vendor%20SOW.docx")
	unescaped := fileAuditLocatorKey("url", "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Vendor SOW.docx")
	if escaped == "" || escaped != unescaped {
		t.Fatalf("percent-escaped and literal locators must match: %q vs %q", escaped, unescaped)
	}
	if fileAuditLocatorKey("path", "/docs/report+v2.pdf") == fileAuditLocatorKey("path", "/docs/report v2.pdf") {
		t.Fatal("literal '+' in a file name must not collide with a space-named file")
	}
	if got, want := fileAuditLocatorKey("path", `\sites\Streaming\Contracts\`), "path:/sites/streaming/contracts"; got != want {
		t.Fatalf("backslash/trailing-slash normalization: got %q, want %q", got, want)
	}
	if fileAuditLocatorKey("url", "   ") != "" {
		t.Fatal("blank locator must produce no key")
	}
}

func TestCachedFileAuditIndexScopesRemoteIDsByConnector(t *testing.T) {
	idx := cachedFileAuditIndex{
		artifactIDs: map[string]struct{}{},
		locators:    map[string]struct{}{},
	}
	idx.addLocator(fileAuditRefLocatorKind("jira"), "10001")

	ref := remoteFileAuditRef{RemoteID: "10001"}
	if !idx.hasRemoteRef(ref, "jira") {
		t.Fatal("expected matching Jira remote ID to be treated as cached")
	}
	if idx.hasRemoteRef(ref, "slack") {
		t.Fatal("same remote ID from a different connector must remain visible")
	}
}

func TestListFileAuditArtifactsPagesThroughFullStoreHistory(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	originalPageSize := fileAuditStorePageSize
	fileAuditStorePageSize = 2
	t.Cleanup(func() {
		fileAuditStorePageSize = originalPageSize
	})
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	total := fileAuditStorePageSize + 2
	for i := 0; i < total; i++ {
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:       "file-history-" + strconv.Itoa(i),
			ThreadID: thread.ID,
			Type:     "file",
			Title:    "file-history-" + strconv.Itoa(i) + ".md",
			ToolName: &toolName,
			Storage:  "file",
			Metadata: map[string]any{
				"source": types.ChatUploadAttachmentSource,
			},
		}); err != nil {
			t.Fatalf("InsertArtifact file %d: %v", i, err)
		}
	}

	files, err := listFileAuditArtifacts(ctx, st, thread.ID, "file")
	if err != nil {
		t.Fatalf("listFileAuditArtifacts: %v", err)
	}
	if len(files) != total {
		t.Fatalf("files=%d, want %d across more than one store page", len(files), total)
	}
}

func TestHandleDeleteArtifactHidesRemoteFileAuditReference(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := "sharepoint_search"
	result := `{
		"items": [
			{
				"name": "Vendor SOW.docx",
				"web_url": "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
		]
	}`
	sourceArtifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "tool-result-remote-hide",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "SharePoint search result",
		ToolName:    &toolName,
		Storage:     "mysql",
		ContentText: &result,
		Metadata: map[string]any{
			"source": "external_tool",
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_search",
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact search result: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()
	h.handleListThreadFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal list: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one remote reference before delete", payload.Files)
	}
	recordID, ok := payload.Files[0]["id"].(string)
	if !ok || !strings.HasPrefix(recordID, remoteFileAuditRecordPrefix+sourceArtifact.ID+":") {
		t.Fatalf("record id=%#v, want remote record for source artifact", payload.Files[0]["id"])
	}
	if payload.Files[0]["removable"] != true {
		t.Fatalf("removable=%#v, want remote reference removable", payload.Files[0]["removable"])
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/artifacts/"+recordID, nil)
	deleteReq.SetPathValue("artifactId", recordID)
	deleteRec := httptest.NewRecorder()
	h.handleDeleteArtifact(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.handleListThreadFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list after delete status=%d body=%s", rec.Code, rec.Body.String())
	}
	payload.Files = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal list after delete: %v", err)
	}
	if len(payload.Files) != 0 {
		t.Fatalf("files=%#v, want hidden remote reference omitted", payload.Files)
	}
	preserved, err := st.GetArtifact(ctx, sourceArtifact.ID)
	if err != nil {
		t.Fatalf("GetArtifact source after remote delete: %v", err)
	}
	hiddenRefs := remoteFileAuditHiddenRefs(preserved.Metadata)
	if _, ok := hiddenRefs[recordID]; !ok {
		t.Fatalf("hidden refs=%#v, want record id %s persisted", hiddenRefs, recordID)
	}
}

func TestHandleListThreadFilesDoesNotLetDataArtifactsSkewCachedFileListing(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "old-file",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "kept-even-with-many-data-results.md",
		ToolName: &toolName,
		Storage:  "file",
		Metadata: map[string]any{
			"source": types.ChatUploadAttachmentSource,
		},
	}); err != nil {
		t.Fatalf("InsertArtifact file: %v", err)
	}
	for i := 0; i < defaultFileAuditLimit+25; i++ {
		result := `{"rows":[{"id":` + strconv.Itoa(i) + `}]}`
		dataTool := "display_table"
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:          "data-result-" + strconv.Itoa(i),
			ThreadID:    thread.ID,
			Type:        "data",
			Title:       "data result",
			ToolName:    &dataTool,
			Storage:     "mysql",
			ContentText: &result,
		}); err != nil {
			t.Fatalf("InsertArtifact data %d: %v", i, err)
		}
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one cached file despite newer data artifacts", payload.Files)
	}
	file := payload.Files[0]
	if file["artifact_id"] != "old-file" || file["record_kind"] != "cached_file" {
		t.Fatalf("file row=%#v, want old cached file", file)
	}
	if payload.Scope["has_more"] != false || payload.Scope["truncated"] != false {
		t.Fatalf("scope=%#v, want no file-audit truncation from non-file data artifacts", payload.Scope)
	}
}

func TestHandleListThreadFilesReportsExactCachedFilePagination(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	for _, id := range []string{"file-a", "file-b"} {
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:       id,
			ThreadID: thread.ID,
			Type:     "file",
			Title:    id + ".md",
			ToolName: &toolName,
			Storage:  "file",
			Metadata: map[string]any{
				"source": types.ChatUploadAttachmentSource,
			},
		}); err != nil {
			t.Fatalf("InsertArtifact %s: %v", id, err)
		}
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one returned file", payload.Files)
	}
	if payload.Scope["returned"] != float64(1) ||
		payload.Scope["has_more"] != true ||
		payload.Scope["next_offset"] != float64(1) ||
		payload.Scope["candidate_count"] != float64(2) ||
		payload.Scope["cached_file_limit_hit"] != false {
		t.Fatalf("scope=%#v, want exact pagination metadata without scan-limit marker", payload.Scope)
	}
}

func TestHandleListThreadFilesPaginatesWithOffset(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	for _, id := range []string{"file-old", "file-mid", "file-new"} {
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:       id,
			ThreadID: thread.ID,
			Type:     "file",
			Title:    id + ".md",
			ToolName: &toolName,
			Storage:  "file",
			Metadata: map[string]any{
				"source": types.ChatUploadAttachmentSource,
			},
		}); err != nil {
			t.Fatalf("InsertArtifact %s: %v", id, err)
		}
		time.Sleep(time.Millisecond)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1&offset=1", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one paged file", payload.Files)
	}
	if got := payload.Files[0]["artifact_id"]; got != "file-mid" {
		t.Fatalf("artifact_id=%#v, want second newest file-mid", got)
	}
	if payload.Scope["offset"] != float64(1) || payload.Scope["next_offset"] != float64(2) || payload.Scope["has_more"] != true {
		t.Fatalf("scope=%#v, want offset=1 next_offset=2 has_more=true", payload.Scope)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1&offset=2", nil)
	req.SetPathValue("threadId", thread.ID)
	rec = httptest.NewRecorder()
	h.handleListThreadFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("last page status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal last page: %v", err)
	}
	if len(payload.Files) != 1 || payload.Files[0]["artifact_id"] != "file-old" {
		t.Fatalf("last page files=%#v, want oldest file-old", payload.Files)
	}
	if payload.Scope["next_offset"] != float64(0) || payload.Scope["has_more"] != false {
		t.Fatalf("last page scope=%#v, want no next page", payload.Scope)
	}
}

func TestHandleListThreadFilesSearchesCachedFilesBeforePagination(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	for _, item := range []struct {
		id      string
		title   string
		preview string
	}{
		{"file-target", "roadmap-needle.md", "Older target file should still be found."},
		{"file-new", "latest-notes.md", "Newest non-match."},
	} {
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:       item.id,
			ThreadID: thread.ID,
			Type:     "file",
			Title:    item.title,
			ToolName: &toolName,
			Storage:  "file",
			Metadata: map[string]any{
				"source":      types.ChatUploadAttachmentSource,
				"previewText": item.preview,
			},
		}); err != nil {
			t.Fatalf("InsertArtifact %s: %v", item.id, err)
		}
		time.Sleep(time.Millisecond)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1&q=needle", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 || payload.Files[0]["artifact_id"] != "file-target" {
		t.Fatalf("files=%#v, want the matching older file before pagination", payload.Files)
	}
	if payload.Scope["candidate_count"] != float64(1) || payload.Scope["has_more"] != false || payload.Scope["query"] != "needle" {
		t.Fatalf("scope=%#v, want one matched candidate and no next page", payload.Scope)
	}
}

func TestHandleListThreadFilesSearchesRemoteReferencesBeforePagination(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := "sharepoint_search"
	toolCallID := "tool-call-sharepoint-search"
	result := `{
		"items": [
			{
				"name": "Budget.xlsx",
				"web_url": "https://contoso.sharepoint.com/sites/team/Budget.xlsx",
				"server_relative_url": "/sites/team/Budget.xlsx",
				"mimeType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
			},
			{
				"name": "Legal Hold.pdf",
				"server_relative_url": "/sites/team/Legal/Legal Hold.pdf",
				"mimeType": "application/pdf",
				"summary": "Matter archive"
			}
		]
	}`
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "tool-result-search",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "SharePoint search result",
		ToolName:    &toolName,
		ToolCallID:  &toolCallID,
		Storage:     "mysql",
		ContentText: &result,
		Metadata: map[string]any{
			"source": "external_tool",
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_search",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact result: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1&q=Legal+Hold", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one matched remote reference", payload.Files)
	}
	file := payload.Files[0]
	if file["record_kind"] != "remote_reference" || file["title"] != "Legal Hold.pdf" {
		t.Fatalf("file=%#v, want Legal Hold remote reference", file)
	}
	if file["remote_path"] != "/sites/team/Legal/Legal Hold.pdf" || file["request_tool_name"] != "sharepoint_read" {
		t.Fatalf("remote path/tool=%#v/%#v; file=%#v", file["remote_path"], file["request_tool_name"], file)
	}
	if payload.Scope["candidate_count"] != float64(1) || payload.Scope["has_more"] != false || payload.Scope["query"] != "Legal Hold" {
		t.Fatalf("scope=%#v, want one matched candidate and original query", payload.Scope)
	}
}

func TestHandleListThreadFilesFiltersSourceBeforePagination(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	sharepointTool := "sharepoint_read"
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "sharepoint-old",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "Roadmap.md",
		ToolName: &sharepointTool,
		Storage:  "file",
		Metadata: map[string]any{
			"source": "external_tool",
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_read",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact sharepoint: %v", err)
	}
	time.Sleep(time.Millisecond)
	uploadTool := types.ThreadFileAttachmentToolName
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "upload-new",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "latest-upload.md",
		ToolName: &uploadTool,
		Storage:  "file",
		Metadata: map[string]any{
			"source": types.ChatUploadAttachmentSource,
		},
	}); err != nil {
		t.Fatalf("InsertArtifact upload: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1&source=SharePoint", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 || payload.Files[0]["artifact_id"] != "sharepoint-old" {
		t.Fatalf("files=%#v, want older SharePoint row before pagination", payload.Files)
	}
	if payload.Scope["candidate_count"] != float64(1) || payload.Scope["source"] != "SharePoint" || payload.Scope["source_label"] != "SharePoint" {
		t.Fatalf("scope=%#v, want one SharePoint candidate and source echo", payload.Scope)
	}
	sourceCounts, ok := payload.Scope["source_counts"].([]any)
	if !ok || len(sourceCounts) != 2 {
		t.Fatalf("source_counts=%#v, want both source buckets before source filter", payload.Scope["source_counts"])
	}
	statusCounts, ok := payload.Scope["status_counts"].(map[string]any)
	if !ok || statusCounts["total"] != float64(1) || statusCounts["cached"] != float64(1) || statusCounts["remote_only"] != float64(0) {
		t.Fatalf("status_counts=%#v, want status counts scoped to SharePoint", payload.Scope["status_counts"])
	}
}

func TestHandleListThreadFilesFiltersRemoteOnlyBeforePagination(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	uploadTool := types.ThreadFileAttachmentToolName
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "upload-new",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "latest-upload.md",
		ToolName: &uploadTool,
		Storage:  "file",
		Metadata: map[string]any{
			"source": types.ChatUploadAttachmentSource,
		},
	}); err != nil {
		t.Fatalf("InsertArtifact upload: %v", err)
	}
	time.Sleep(time.Millisecond)
	searchTool := "sharepoint_search"
	result := `{
		"items": [
			{
				"name": "Vendor SOW.docx",
				"web_url": "https://contoso.sharepoint.com/sites/team/Vendor SOW.docx",
				"server_relative_url": "/sites/team/Vendor SOW.docx",
				"mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
			}
		]
	}`
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "sharepoint-result",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "SharePoint search result",
		ToolName:    &searchTool,
		Storage:     "mysql",
		ContentText: &result,
		Metadata: map[string]any{
			"source": "external_tool",
			"result_metadata": map[string]any{
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_search",
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact result: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files?limit=1&remote_only=true", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleListThreadFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
		Scope map[string]any   `json:"scope"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("files=%#v, want one remote-only reference", payload.Files)
	}
	file := payload.Files[0]
	if file["record_kind"] != "remote_reference" || file["title"] != "Vendor SOW.docx" {
		t.Fatalf("file=%#v, want Vendor SOW remote reference", file)
	}
	if payload.Scope["candidate_count"] != float64(1) || payload.Scope["remote_only"] != true {
		t.Fatalf("scope=%#v, want one remote-only candidate and remote_only echo", payload.Scope)
	}
	sourceCounts, ok := payload.Scope["source_counts"].([]any)
	if !ok || len(sourceCounts) != 1 {
		t.Fatalf("source_counts=%#v, want source buckets scoped to remote-only rows", payload.Scope["source_counts"])
	}
	statusCounts, ok := payload.Scope["status_counts"].(map[string]any)
	if !ok || statusCounts["total"] != float64(2) || statusCounts["cached"] != float64(1) || statusCounts["remote_only"] != float64(1) {
		t.Fatalf("status_counts=%#v, want status counts before remote-only filter", payload.Scope["status_counts"])
	}
}

func TestHandleListThreadFilesNormalizesRemoteConnectorReferences(t *testing.T) {
	cases := []struct {
		name            string
		toolName        string
		sourceLabel     string
		family          string
		product         string
		result          string
		wantTitle       string
		wantRequestTool string
		wantRemoteURL   string
		wantRemotePath  string
		wantArgs        map[string]any
	}{
		{
			name:        "jira attachment list",
			toolName:    "jira_get_issue_attachments",
			sourceLabel: "Jira",
			family:      "jira",
			product:     "jira",
			result: `{
				"issueKey": "STREAM-4821",
				"attachments": [
					{
						"id": "10032",
						"fileName": "incident-bundle.md",
						"mimeType": "text/markdown",
						"sizeBytes": 912
					}
				]
			}`,
			wantTitle:       "incident-bundle.md",
			wantRequestTool: "jira_read_attachment",
			wantArgs: map[string]any{
				"issueKey":     "STREAM-4821",
				"attachmentId": "10032",
				"remote_id":    "10032",
			},
		},
		{
			name:        "slack file list",
			toolName:    "slack_list_files",
			sourceLabel: "Slack",
			family:      "slack",
			product:     "slack",
			result: `{
				"channel_id": "C0STREAM",
				"files": [
					{
						"id": "F123",
						"name": "pipeline-export.md",
						"mimetype": "text/markdown",
						"permalink": "https://example.slack.com/files/F123/pipeline-export.md",
						"size": 811
					}
				]
			}`,
			wantTitle:       "pipeline-export.md",
			wantRequestTool: "slack_download_file",
			wantRemoteURL:   "https://example.slack.com/files/F123/pipeline-export.md",
			wantArgs: map[string]any{
				"channel_id": "C0STREAM",
				"file_id":    "F123",
				"remote_id":  "F123",
				"source_url": "https://example.slack.com/files/F123/pipeline-export.md",
			},
		},
		{
			name:        "arxiv search result",
			toolName:    "arxiv_search",
			sourceLabel: "arXiv",
			family:      "arxiv",
			product:     "arxiv",
			result: `{
				"entries": [
					{
						"arxiv_id": "2301.12345",
						"title": "Sparse Attention Models",
						"summary": "Search result summary.",
						"pdf_url": "https://arxiv.org/pdf/2301.12345",
						"abs_url": "https://arxiv.org/abs/2301.12345"
					}
				]
			}`,
			wantTitle:       "Sparse Attention Models",
			wantRequestTool: "arxiv_download_paper",
			wantRemoteURL:   "https://arxiv.org/pdf/2301.12345",
			wantArgs: map[string]any{
				"arxiv_id":   "2301.12345",
				"remote_id":  "2301.12345",
				"source_url": "https://arxiv.org/pdf/2301.12345",
			},
		},
		{
			name:        "sharepoint search result",
			toolName:    "sharepoint_search",
			sourceLabel: "SharePoint",
			family:      "sharepoint",
			product:     "sharepoint",
			result: `{
				"items": [
					{
						"name": "Vendor SOW.docx",
						"web_url": "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
						"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
						"mimeType": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
						"sizeBytes": 48213
					}
				]
			}`,
			wantTitle:       "Vendor SOW.docx",
			wantRequestTool: "sharepoint_read",
			wantRemoteURL:   "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
			wantRemotePath:  "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
			wantArgs: map[string]any{
				"source_url":          "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"item_url":            "https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"remote_path":         "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
				"server_relative_url": "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("NewJSON: %v", err)
			}
			thread, err := st.CreateThread(ctx, nil)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			toolCallID := "tool-call-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := st.InsertArtifact(ctx, store.Artifact{
				ID:          "tool-result-" + strings.ReplaceAll(tc.name, " ", "-"),
				ThreadID:    thread.ID,
				Type:        "data",
				Title:       tc.name,
				ToolName:    &tc.toolName,
				ToolCallID:  &toolCallID,
				Storage:     "mysql",
				ContentText: &tc.result,
				Metadata: map[string]any{
					"source": "external_tool",
					"result_metadata": map[string]any{
						"family":           tc.family,
						"product":          tc.product,
						"source_tool_name": tc.toolName,
					},
				},
			}); err != nil {
				t.Fatalf("InsertArtifact result: %v", err)
			}

			h := &Handler{store: st, cfgDir: cfgDir}
			req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/files", nil)
			req.SetPathValue("threadId", thread.ID)
			rec := httptest.NewRecorder()

			h.handleListThreadFiles(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var payload struct {
				Files []map[string]any `json:"files"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if len(payload.Files) != 1 {
				t.Fatalf("files=%#v, want one remote reference", payload.Files)
			}
			file := payload.Files[0]
			if file["record_kind"] != "remote_reference" || file["availability"] != "remote_only" || file["cached"] != false {
				t.Fatalf("remote state mismatch: %#v", file)
			}
			if file["title"] != tc.wantTitle || file["source_label"] != tc.sourceLabel || file["request_tool_name"] != tc.wantRequestTool {
				t.Fatalf("identity title/source/tool=%#v/%#v/%#v; file=%#v", file["title"], file["source_label"], file["request_tool_name"], file)
			}
			if tc.wantRemoteURL != "" && file["remote_url"] != tc.wantRemoteURL {
				t.Fatalf("remote_url=%#v, want %q; file=%#v", file["remote_url"], tc.wantRemoteURL, file)
			}
			if tc.wantRemotePath != "" && file["remote_path"] != tc.wantRemotePath {
				t.Fatalf("remote_path=%#v, want %q; file=%#v", file["remote_path"], tc.wantRemotePath, file)
			}
			args, ok := file["request_tool_args"].(map[string]any)
			if !ok {
				t.Fatalf("request_tool_args=%#v, want object", file["request_tool_args"])
			}
			for key, want := range tc.wantArgs {
				if got := args[key]; got != want {
					t.Fatalf("request_tool_args[%s]=%#v, want %#v; args=%#v file=%#v", key, got, want, args, file)
				}
			}
		})
	}
}

func TestHandleListFilesReturnsGlobalFileArtifacts(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	threadA, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread A: %v", err)
	}
	threadB, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread B: %v", err)
	}
	if err := st.UpdateThreadTitleSummary(ctx, threadB.ID, strPtrForFileAuditTest("SharePoint task"), nil); err != nil {
		t.Fatalf("UpdateThreadTitleSummary: %v", err)
	}
	for _, item := range []struct {
		id       string
		threadID string
		title    string
		tool     string
		source   string
	}{
		{"file-a", threadA.ID, "upload.txt", types.ThreadFileAttachmentToolName, types.ChatUploadAttachmentSource},
		{"file-b", threadB.ID, "plan.docx", "sharepoint_read", "external_tool"},
	} {
		tool := item.tool
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:       item.id,
			ThreadID: item.threadID,
			Type:     "file",
			Title:    item.title,
			ToolName: &tool,
			Storage:  "file",
			Metadata: map[string]any{
				"source":           item.source,
				"externalToolFile": item.source == "external_tool",
			},
		}); err != nil {
			t.Fatalf("InsertArtifact %s: %v", item.id, err)
		}
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/files?limit=10", nil)
	rec := httptest.NewRecorder()

	h.handleListFiles(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(payload.Files) != 2 {
		t.Fatalf("files=%#v, want two global file artifacts", payload.Files)
	}
	var sawSharePoint bool
	for _, file := range payload.Files {
		if file["artifact_id"] == "file-b" {
			sawSharePoint = true
			if file["source_label"] != "SharePoint" || file["thread_title"] != "SharePoint task" {
				t.Fatalf("sharepoint row=%#v, want SharePoint label and thread title", file)
			}
		}
	}
	if !sawSharePoint {
		t.Fatalf("files=%#v, missing SharePoint file", payload.Files)
	}
}

func TestFileAuditRecordDTOLabelsConnectorProvenance(t *testing.T) {
	cases := []struct {
		name             string
		toolName         string
		source           string
		resultMetadata   map[string]any
		wantSourceLabel  string
		wantIntegration  string
		wantFamily       string
		wantProduct      string
		wantSourceTool   string
		wantDownloadable bool
	}{
		{
			name:             "chat upload",
			toolName:         types.ThreadFileAttachmentToolName,
			source:           types.ChatUploadAttachmentSource,
			wantSourceLabel:  "Chat upload",
			wantSourceTool:   types.ThreadFileAttachmentToolName,
			wantDownloadable: true,
		},
		{
			name:     "jira attachment",
			toolName: "jira_read_attachment",
			source:   "external_tool",
			resultMetadata: map[string]any{
				"integration":      "atlassian",
				"family":           "jira",
				"product":          "jira",
				"source_tool_name": "jira_read_attachment",
			},
			wantSourceLabel:  "Jira",
			wantIntegration:  "atlassian",
			wantFamily:       "jira",
			wantProduct:      "jira",
			wantSourceTool:   "jira_read_attachment",
			wantDownloadable: true,
		},
		{
			name:     "sharepoint document",
			toolName: "sharepoint_read",
			source:   "external_tool",
			resultMetadata: map[string]any{
				"integration":      "sharepoint",
				"family":           "sharepoint",
				"product":          "sharepoint",
				"source_tool_name": "sharepoint_read",
			},
			wantSourceLabel:  "SharePoint",
			wantIntegration:  "sharepoint",
			wantFamily:       "sharepoint",
			wantProduct:      "sharepoint",
			wantSourceTool:   "sharepoint_read",
			wantDownloadable: true,
		},
		{
			name:     "slack file",
			toolName: "slack_download_file",
			source:   "external_tool",
			resultMetadata: map[string]any{
				"integration":      "slack",
				"family":           "slack",
				"product":          "slack",
				"source_tool_name": "slack_download_file",
			},
			wantSourceLabel:  "Slack",
			wantIntegration:  "slack",
			wantFamily:       "slack",
			wantProduct:      "slack",
			wantSourceTool:   "slack_download_file",
			wantDownloadable: true,
		},
		{
			name:     "arxiv paper",
			toolName: "arxiv_download_paper",
			source:   "external_tool",
			resultMetadata: map[string]any{
				"integration":      "arxiv",
				"family":           "arxiv",
				"product":          "arxiv",
				"source_tool_name": "arxiv_download_paper",
			},
			wantSourceLabel:  "arXiv",
			wantIntegration:  "arxiv",
			wantFamily:       "arxiv",
			wantProduct:      "arxiv",
			wantSourceTool:   "arxiv_download_paper",
			wantDownloadable: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toolName := tc.toolName
			metadata := map[string]any{
				"source":   tc.source,
				"mimeType": "application/pdf",
			}
			if tc.resultMetadata != nil {
				metadata["result_metadata"] = tc.resultMetadata
				if family, _ := tc.resultMetadata["family"].(string); family != "" {
					metadata["tool_presentation"] = map[string]any{
						"family":            family,
						"result_kind":       "file",
						"renderer_strategy": "artifact_type",
						"renderer_key":      "file",
						"native_viewer":     "file",
					}
				}
			}

			dto := fileAuditRecordDTO(store.Artifact{
				ID:       "artifact-" + tc.name,
				ThreadID: "thread-1",
				Type:     "file",
				Title:    tc.name + ".pdf",
				ToolName: &toolName,
				Storage:  "file",
				Metadata: metadata,
			}, "Connector QA")

			if dto["source_label"] != tc.wantSourceLabel {
				t.Fatalf("source_label=%#v, want %q; dto=%#v", dto["source_label"], tc.wantSourceLabel, dto)
			}
			if dto["source_tool_name"] != tc.wantSourceTool {
				t.Fatalf("source_tool_name=%#v, want %q; dto=%#v", dto["source_tool_name"], tc.wantSourceTool, dto)
			}
			if dto["downloadable"] != tc.wantDownloadable {
				t.Fatalf("downloadable=%#v, want %v; dto=%#v", dto["downloadable"], tc.wantDownloadable, dto)
			}
			if tc.wantIntegration != "" && dto["integration"] != tc.wantIntegration {
				t.Fatalf("integration=%#v, want %q; dto=%#v", dto["integration"], tc.wantIntegration, dto)
			}
			if tc.wantFamily != "" && dto["family"] != tc.wantFamily {
				t.Fatalf("family=%#v, want %q; dto=%#v", dto["family"], tc.wantFamily, dto)
			}
			if tc.wantProduct != "" && dto["product"] != tc.wantProduct {
				t.Fatalf("product=%#v, want %q; dto=%#v", dto["product"], tc.wantProduct, dto)
			}
		})
	}
}

func TestFileAuditRecordDTOResolvesConnectorNativeMimeAliases(t *testing.T) {
	cases := []struct {
		name     string
		metadata map[string]any
		wantMime string
		wantKind string
	}{
		{
			// Mirrors the e2e Slack PDF fixture ("Quarterly Close Scan"):
			// MIME/kind live only in nested result_metadata under Slack keys.
			name: "slack aliases nested in result_metadata",
			metadata: map[string]any{
				"source": "external_tool",
				"result_metadata": map[string]any{
					"mimetype": "application/pdf",
					"filetype": "pdf",
				},
			},
			wantMime: "application/pdf",
			wantKind: "pdf",
		},
		{
			name: "http content_type alias",
			metadata: map[string]any{
				"source":       "external_tool",
				"content_type": "text/csv",
			},
			wantMime: "text/csv",
			wantKind: "",
		},
		{
			name: "generic envelope kind never masks connector filetype",
			metadata: map[string]any{
				"source": "external_tool",
				"kind":   "file",
				"result_metadata": map[string]any{
					"filetype": "png",
				},
			},
			wantMime: "",
			wantKind: "png",
		},
		{
			name: "normalized top-level keys keep priority over aliases",
			metadata: map[string]any{
				"source":           "chat_upload",
				"detectedMimeType": "application/pdf",
				"mimetype":         "text/plain",
				"fileKind":         "pdf",
				"filetype":         "txt",
			},
			wantMime: "application/pdf",
			wantKind: "pdf",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dto := fileAuditRecordDTO(store.Artifact{
				ID:       "artifact-mime-alias",
				ThreadID: "thread-1",
				Type:     "file",
				Title:    "quarterly-close-scan.pdf",
				Storage:  "file",
				Metadata: tc.metadata,
			}, "Alias QA")

			if dto["mime_type"] != tc.wantMime {
				t.Fatalf("mime_type=%#v, want %q; dto=%#v", dto["mime_type"], tc.wantMime, dto)
			}
			if dto["file_kind"] != tc.wantKind {
				t.Fatalf("file_kind=%#v, want %q; dto=%#v", dto["file_kind"], tc.wantKind, dto)
			}
		})
	}
}

func TestRemoteFileAuditRefResolvesConnectorNativeMimeAliases(t *testing.T) {
	resultMetadata := map[string]any{"family": "slack"}

	ref, ok := remoteFileAuditRefFromMap(map[string]any{
		"id":       "F999",
		"name":     "close-scan.pdf",
		"mimetype": "application/pdf",
		"filetype": "pdf",
		"kind":     "file",
	}, resultMetadata, nil, nil, "slack_list_files")
	if !ok {
		t.Fatalf("expected slack file map to yield a remote reference")
	}
	if ref.MimeType != "application/pdf" || ref.FileKind != "pdf" {
		t.Fatalf("mime/kind=%q/%q, want application/pdf/pdf", ref.MimeType, ref.FileKind)
	}

	// A generic envelope kind still marks the map as a file reference but must
	// not surface as the record's file_kind.
	ref, ok = remoteFileAuditRefFromMap(map[string]any{
		"file_name": "board-notes",
		"kind":      "file",
	}, resultMetadata, nil, nil, "slack_list_files")
	if !ok {
		t.Fatalf("expected generic kind map to still yield a remote reference")
	}
	if ref.FileKind != "" {
		t.Fatalf("file_kind=%q, want empty for generic envelope kind", ref.FileKind)
	}
	if ref.ReferenceMetadata["file_kind"] != "" {
		t.Fatalf("reference metadata file_kind=%#v, want empty", ref.ReferenceMetadata["file_kind"])
	}

	ref, ok = remoteFileAuditRefFromMap(map[string]any{
		"file_name":    "export.csv",
		"content_type": "text/csv",
	}, map[string]any{}, nil, nil, "http_fetch")
	if !ok {
		t.Fatalf("expected content_type map to yield a remote reference")
	}
	if ref.MimeType != "text/csv" {
		t.Fatalf("mime=%q, want text/csv", ref.MimeType)
	}
}

func TestFileAuditRecordDTOPreservesCachedConnectorRemoteLocators(t *testing.T) {
	cases := []struct {
		name           string
		toolName       string
		family         string
		toolArgs       map[string]any
		resultMetadata map[string]any
		wantRemoteURL  string
		wantRemotePath string
		wantRemoteRef  string
	}{
		{
			name:     "sharepoint",
			toolName: "sharepoint_read",
			family:   "sharepoint",
			toolArgs: map[string]any{
				"item_url":            "https://contoso.sharepoint.com/sites/team/Shared%20Documents/forecast.csv",
				"server_relative_url": "/sites/team/Shared Documents/forecast.csv",
			},
			wantRemoteURL:  "https://contoso.sharepoint.com/sites/team/Shared%20Documents/forecast.csv",
			wantRemotePath: "/sites/team/Shared Documents/forecast.csv",
		},
		{
			name:     "slack",
			toolName: "slack_download_file",
			family:   "slack",
			toolArgs: map[string]any{
				"file_id":    "F123",
				"channel_id": "C456",
			},
			resultMetadata: map[string]any{
				"source_url": "https://example.slack.com/files/F123/pipeline-export.md",
			},
			wantRemoteURL: "https://example.slack.com/files/F123/pipeline-export.md",
			wantRemoteRef: "F123",
		},
		{
			name:     "jira",
			toolName: "jira_read_attachment",
			family:   "jira",
			toolArgs: map[string]any{
				"issueKey":     "STREAM-4821",
				"attachmentId": "10032",
			},
			wantRemoteRef: "10032",
		},
		{
			name:     "arxiv",
			toolName: "arxiv_download_paper",
			family:   "arxiv",
			toolArgs: map[string]any{
				"arxiv_id": "2301.12345",
			},
			resultMetadata: map[string]any{
				"source_url": "https://arxiv.org/pdf/2301.12345",
			},
			wantRemoteURL: "https://arxiv.org/pdf/2301.12345",
			wantRemoteRef: "2301.12345",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resultMetadata := map[string]any{
				"integration":      tc.family,
				"family":           tc.family,
				"product":          tc.family,
				"source_tool_name": tc.toolName,
			}
			for key, value := range tc.resultMetadata {
				resultMetadata[key] = value
			}
			toolName := tc.toolName
			dto := fileAuditRecordDTO(store.Artifact{
				ID:       "cached-" + tc.name,
				ThreadID: "thread-1",
				Type:     "file",
				Title:    tc.name + "-cached-file",
				ToolName: &toolName,
				Storage:  "file",
				Metadata: map[string]any{
					"source":          "external_tool",
					"tool_args":       tc.toolArgs,
					"result_metadata": resultMetadata,
				},
			}, "Connector QA")

			if tc.wantRemoteURL != "" && (dto["remote_url"] != tc.wantRemoteURL || dto["source_url"] != tc.wantRemoteURL) {
				t.Fatalf("remote url/source_url=%#v/%#v, want %q; dto=%#v", dto["remote_url"], dto["source_url"], tc.wantRemoteURL, dto)
			}
			if tc.wantRemotePath != "" && dto["remote_path"] != tc.wantRemotePath {
				t.Fatalf("remote_path=%#v, want %q; dto=%#v", dto["remote_path"], tc.wantRemotePath, dto)
			}
			if tc.wantRemoteRef != "" && dto["remote_ref"] != tc.wantRemoteRef {
				t.Fatalf("remote_ref=%#v, want %q; dto=%#v", dto["remote_ref"], tc.wantRemoteRef, dto)
			}
		})
	}
}

func TestHandleDeleteArtifactRemovesFileArtifactAndManagedBytes(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	contentRel := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, "file-1.txt"))
	contentFull := filepath.Join(cfgDir, filepath.FromSlash(contentRel))
	if err := os.MkdirAll(filepath.Dir(contentFull), 0o755); err != nil {
		t.Fatalf("MkdirAll content: %v", err)
	}
	if err := os.WriteFile(contentFull, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile content: %v", err)
	}
	extractRel := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, "file-1.extract"))
	extractFull := filepath.Join(cfgDir, filepath.FromSlash(extractRel))
	if err := os.MkdirAll(extractFull, 0o755); err != nil {
		t.Fatalf("MkdirAll extract: %v", err)
	}
	if err := os.WriteFile(filepath.Join(extractFull, "preview.txt"), []byte("preview"), 0o644); err != nil {
		t.Fatalf("WriteFile extract: %v", err)
	}
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "file-1",
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "file.txt",
		ToolName:    &toolName,
		Storage:     "file",
		ContentPath: &contentRel,
		ByteSize:    5,
		Metadata: map[string]any{
			"source": types.ChatUploadAttachmentSource,
			"extraction": map[string]any{
				"rootPath": extractRel,
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodDelete, "/api/artifacts/file-1", nil)
	req.SetPathValue("artifactId", "file-1")
	rec := httptest.NewRecorder()

	h.handleDeleteArtifact(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetArtifact(ctx, "file-1"); err != store.ErrNotFound {
		t.Fatalf("GetArtifact after delete err=%v, want ErrNotFound", err)
	}
	if _, err := os.Stat(contentFull); !os.IsNotExist(err) {
		t.Fatalf("content file still exists or unexpected err=%v", err)
	}
	if _, err := os.Stat(extractFull); !os.IsNotExist(err) {
		t.Fatalf("extract dir still exists or unexpected err=%v", err)
	}
}

func strPtrForFileAuditTest(value string) *string {
	return &value
}
