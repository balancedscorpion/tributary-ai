package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/store"
)

const (
	defaultFileAuditLimit    = 100
	maxFileAuditLimit        = 500
	fileAuditFallbackMaxScan = 1_000_000

	remoteFileAuditRecordPrefix = "remote:"
)

var (
	fileAuditStorePageSize = 1000
	maxFileAuditOffset     = int(^uint(0)>>1) - maxFileAuditLimit
)

func (h *Handler) handleListFiles(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	h.handleListFileArtifacts(w, r, threadID)
}

func (h *Handler) handleListThreadFiles(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	h.handleListFileArtifacts(w, r, threadID)
}

func (h *Handler) handleListFileArtifacts(w http.ResponseWriter, r *http.Request, threadID string) {
	limit := parseInt(r.URL.Query().Get("limit"), defaultFileAuditLimit, 1, maxFileAuditLimit)
	offset := parseInt(r.URL.Query().Get("offset"), 0, 0, maxFileAuditOffset)
	pageWindow := offset + limit
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	sourceFilter := strings.TrimSpace(r.URL.Query().Get("source"))
	if sourceFilter == "" {
		sourceFilter = strings.TrimSpace(r.URL.Query().Get("source_label"))
	}
	remoteOnlyFilter := parseBool(r.URL.Query().Get("remote_only"))
	threadID = strings.TrimSpace(threadID)
	fileArtifacts, cachedFileLimitHit, err := listFileAuditArtifactsWithLimit(r.Context(), h.store, threadID, "file")
	if err != nil {
		h.writeErr(w, err)
		return
	}
	sortArtifactsNewestFirst(fileArtifacts)
	remoteRefArtifacts, remoteScanLimitHit, err := listFileAuditArtifactsWithLimit(r.Context(), h.store, threadID, "")
	if err != nil {
		h.writeErr(w, err)
		return
	}
	sortArtifactsNewestFirst(remoteRefArtifacts)
	// Generated images are canonical image artifacts, not duplicate file rows.
	// Promote them into the cached Files inventory while retaining the all-type
	// scan for remote references embedded in other artifact kinds.
	for _, artifact := range remoteRefArtifacts {
		if isGeneratedImageArtifact(artifact) {
			fileArtifacts = append(fileArtifacts, artifact)
		}
	}
	sortArtifactsNewestFirst(fileArtifacts)

	threadTitles := map[string]string{}
	for _, artifact := range append(append([]store.Artifact{}, fileArtifacts...), remoteRefArtifacts...) {
		tid := strings.TrimSpace(artifact.ThreadID)
		if tid == "" {
			continue
		}
		if _, ok := threadTitles[tid]; ok {
			continue
		}
		if thread, err := h.store.GetThread(r.Context(), tid); err == nil {
			threadTitles[tid] = thread.Title
		}
	}

	cachedIndex := newCachedFileAuditIndex(fileArtifacts)
	records := make([]map[string]any, 0, len(fileArtifacts)+len(remoteRefArtifacts))
	for _, artifact := range fileArtifacts {
		record := fileAuditRecordDTO(artifact, threadTitles[strings.TrimSpace(artifact.ThreadID)])
		records = append(records, record)
	}
	for _, artifact := range remoteRefArtifacts {
		if strings.EqualFold(strings.TrimSpace(artifact.Type), "file") || isGeneratedImageArtifact(artifact) {
			continue
		}
		records = append(records, h.remoteFileAuditRecordDTOs(artifact, threadTitles[strings.TrimSpace(artifact.ThreadID)], cachedIndex)...)
	}
	sort.Slice(records, func(i, j int) bool {
		return auditRecordCreatedAt(records[i]).After(auditRecordCreatedAt(records[j]))
	})
	if query != "" {
		records = filterFileAuditRecords(records, query)
	}
	sourceCountRecords := records
	if remoteOnlyFilter {
		sourceCountRecords = filterFileAuditRecordsRemoteOnly(sourceCountRecords)
	}
	sourceCounts := fileAuditSourceCounts(sourceCountRecords)
	statusCountRecords := records
	if sourceFilter != "" {
		statusCountRecords = filterFileAuditRecordsSource(statusCountRecords, sourceFilter)
	}
	statusCounts := fileAuditStatusCounts(statusCountRecords)
	appliedSourceLabel := fileAuditAppliedSourceLabel(sourceCounts, sourceFilter)
	if sourceFilter != "" {
		records = filterFileAuditRecordsSource(records, sourceFilter)
	}
	if remoteOnlyFilter {
		records = filterFileAuditRecordsRemoteOnly(records)
	}
	totalBeforePage := len(records)
	responseTruncated := totalBeforePage > pageWindow
	if offset > 0 {
		if offset >= len(records) {
			records = nil
		} else {
			records = records[offset:]
		}
	}
	if len(records) > limit {
		records = records[:limit]
	}
	hasMore := responseTruncated
	nextOffset := 0
	if responseTruncated && len(records) > 0 {
		candidateNextOffset := offset + len(records)
		if candidateNextOffset <= maxFileAuditOffset {
			nextOffset = candidateNextOffset
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files": records,
		"scope": map[string]any{
			"thread_id":             strings.TrimSpace(threadID),
			"limit":                 limit,
			"offset":                offset,
			"returned":              len(records),
			"has_more":              hasMore,
			"truncated":             hasMore,
			"next_offset":           nextOffset,
			"cached_file_limit_hit": cachedFileLimitHit,
			"remote_scan_limit":     len(remoteRefArtifacts),
			"remote_scan_limit_hit": remoteScanLimitHit,
			"candidate_count":       totalBeforePage,
			"query":                 query,
			"source":                sourceFilter,
			"source_label":          appliedSourceLabel,
			"remote_only":           remoteOnlyFilter,
			"source_counts":         sourceCounts,
			"status_counts":         statusCounts,
		},
	})
}

func filterFileAuditRecordsSource(records []map[string]any, source string) []map[string]any {
	sourceKey := fileAuditFilterKey(source)
	if sourceKey == "" || len(records) == 0 {
		return records
	}
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if fileAuditRecordMatchesSource(record, sourceKey) {
			out = append(out, record)
		}
	}
	return out
}

func fileAuditRecordMatchesSource(record map[string]any, sourceKey string) bool {
	if record == nil || sourceKey == "" {
		return false
	}
	for _, key := range []string{"source_label", "source", "product", "family", "integration", "source_tool_name", "tool_name"} {
		if fileAuditFilterKey(auditAnyString(record[key])) == sourceKey {
			return true
		}
	}
	return false
}

func filterFileAuditRecordsRemoteOnly(records []map[string]any) []map[string]any {
	if len(records) == 0 {
		return records
	}
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		if fileAuditRecordRemoteOnly(record) {
			out = append(out, record)
		}
	}
	return out
}

func fileAuditRecordRemoteOnly(record map[string]any) bool {
	if record == nil {
		return false
	}
	if cached, ok := record["cached"].(bool); ok {
		return !cached
	}
	availability := strings.ToLower(strings.TrimSpace(auditAnyString(record["availability"])))
	return availability == "remote_only" || availability == "remote"
}

func fileAuditSourceCounts(records []map[string]any) []map[string]any {
	type bucket struct {
		label string
		count int
	}
	buckets := map[string]*bucket{}
	for _, record := range records {
		label := strings.TrimSpace(auditAnyString(record["source_label"]))
		if label == "" {
			label = strings.TrimSpace(auditAnyString(record["source"]))
		}
		if label == "" {
			label = "File artifact"
		}
		key := fileAuditFilterKey(label)
		if key == "" {
			key = strings.ToLower(label)
		}
		if _, ok := buckets[key]; !ok {
			buckets[key] = &bucket{label: label}
		}
		buckets[key].count++
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return strings.ToLower(buckets[keys[i]].label) < strings.ToLower(buckets[keys[j]].label)
	})
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{
			"key":   buckets[key].label,
			"label": buckets[key].label,
			"count": buckets[key].count,
		})
	}
	return out
}

func fileAuditStatusCounts(records []map[string]any) map[string]any {
	cached := 0
	remoteOnly := 0
	for _, record := range records {
		if fileAuditRecordRemoteOnly(record) {
			remoteOnly++
		} else {
			cached++
		}
	}
	return map[string]any{
		"total":       len(records),
		"cached":      cached,
		"remote_only": remoteOnly,
	}
}

func fileAuditAppliedSourceLabel(sourceCounts []map[string]any, source string) string {
	sourceKey := fileAuditFilterKey(source)
	if sourceKey == "" {
		return ""
	}
	for _, count := range sourceCounts {
		label := auditAnyString(count["label"])
		if fileAuditFilterKey(label) == sourceKey {
			return label
		}
	}
	return strings.TrimSpace(source)
}

func fileAuditFilterKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastSpace = false
			continue
		}
		if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

func filterFileAuditRecords(records []map[string]any, query string) []map[string]any {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(records) == 0 {
		return records
	}
	terms := strings.Fields(query)
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		text := fileAuditRecordSearchText(record)
		if text == "" {
			continue
		}
		if strings.Contains(text, query) {
			out = append(out, record)
			continue
		}
		if len(terms) <= 1 {
			continue
		}
		matchesAllTerms := true
		for _, term := range terms {
			if !strings.Contains(text, term) {
				matchesAllTerms = false
				break
			}
		}
		if matchesAllTerms {
			out = append(out, record)
		}
	}
	return out
}

func fileAuditRecordSearchText(record map[string]any) string {
	if record == nil {
		return ""
	}
	keys := []string{
		"title", "source_label", "source_tool_name", "tool_name", "thread_title",
		"file_kind", "mime_type", "content_path", "managed_local_path",
		"remote_url", "source_url", "remote_path", "remote_ref", "remote_id",
		"preview_text", "content_note", "request_tool_name",
	}
	parts := make([]string, 0, len(keys)+8)
	for _, key := range keys {
		if value := strings.TrimSpace(auditAnyString(record[key])); value != "" {
			parts = append(parts, value)
		}
	}
	metadata := plainMap(record["metadata"])
	resultMetadata := plainMap(metadata["result_metadata"])
	remoteReference := plainMap(metadata["remote_reference"])
	toolArgs := plainMap(metadata["tool_args"])
	for _, m := range []map[string]any{metadata, resultMetadata, remoteReference, toolArgs} {
		for _, key := range []string{
			"name", "file_name", "filename", "title", "summary", "snippet",
			"remote_url", "source_url", "web_url", "item_url", "download_url", "permalink", "pdf_url", "abs_url",
			"remote_path", "server_relative_url", "drive_path", "path", "file_path",
			"remote_id", "file_id", "attachmentId", "attachment_id", "item_id", "arxiv_id",
			"query", "jql", "issueKey", "issue_key", "channel_id",
		} {
			if value := strings.TrimSpace(firstStringMapValue(m, key)); value != "" {
				parts = append(parts, value)
			}
		}
	}
	return strings.ToLower(strings.Join(parts, "\n"))
}

func auditAnyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}

type fileAuditArtifactPager interface {
	ListArtifactsPage(context.Context, store.ListArtifactsPageParams) (store.ArtifactPage, error)
}

func listFileAuditArtifacts(ctx context.Context, st store.Store, threadID string, typeFilter string) ([]store.Artifact, error) {
	artifacts, _, err := listFileAuditArtifactsWithLimit(ctx, st, threadID, typeFilter)
	return artifacts, err
}

func listFileAuditArtifactsWithLimit(ctx context.Context, st store.Store, threadID string, typeFilter string) ([]store.Artifact, bool, error) {
	if pager, ok := st.(fileAuditArtifactPager); ok {
		var out []store.Artifact
		params := store.ListArtifactsPageParams{
			ThreadID: strings.TrimSpace(threadID),
			Type:     strings.TrimSpace(typeFilter),
			Limit:    fileAuditStorePageSize,
		}
		for {
			page, err := pager.ListArtifactsPage(ctx, params)
			if err != nil {
				return nil, false, err
			}
			out = append(out, page.Artifacts...)
			if !page.HasMore || len(page.Artifacts) == 0 {
				return out, false, nil
			}
			last := page.Artifacts[len(page.Artifacts)-1]
			params.BeforeCreatedAt = &last.CreatedAt
			params.BeforeID = last.ID
		}
	}
	var out []store.Artifact
	var err error
	if strings.EqualFold(strings.TrimSpace(typeFilter), "file") {
		out, err = st.ListFileArtifacts(ctx, threadID, fileAuditFallbackMaxScan)
	} else {
		out, err = st.ListArtifacts(ctx, threadID, fileAuditFallbackMaxScan)
	}
	if err != nil {
		return nil, false, err
	}
	return out, len(out) >= fileAuditFallbackMaxScan, nil
}

func sortArtifactsNewestFirst(artifacts []store.Artifact) {
	sort.SliceStable(artifacts, func(i, j int) bool {
		if !artifacts[i].CreatedAt.Equal(artifacts[j].CreatedAt) {
			return artifacts[i].CreatedAt.After(artifacts[j].CreatedAt)
		}
		return artifacts[i].ID > artifacts[j].ID
	})
}

func (h *Handler) handleDeleteArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing artifactId"})
		return
	}
	if strings.HasPrefix(artifactID, remoteFileAuditRecordPrefix) {
		h.handleHideRemoteFileAuditReference(w, r, artifactID)
		return
	}
	artifact, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if !strings.EqualFold(strings.TrimSpace(artifact.Type), "file") && !isGeneratedImageArtifact(artifact) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "only Files artifacts can be removed from the file audit"})
		return
	}
	cleanup := artifactCleanupTargets(artifact)
	if err := h.store.DeleteArtifact(r.Context(), artifactID); err != nil {
		h.writeErr(w, err)
		return
	}
	for _, target := range cleanup {
		full, ok := safeConfigRelativePath(h.cfgDir, target.relPath)
		if !ok {
			continue
		}
		if target.dir {
			_ = os.RemoveAll(full)
			continue
		}
		_ = os.Remove(full)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifact_id": artifactID})
}

func (h *Handler) handleHideRemoteFileAuditReference(w http.ResponseWriter, r *http.Request, recordID string) {
	sourceArtifactID, recordKey, ok := splitRemoteFileAuditRecordID(recordID)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid remote file audit record id"})
		return
	}
	artifact, err := h.store.GetArtifact(r.Context(), sourceArtifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	metadata := cloneAuditMap(artifact.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	hidden := appendRemoteFileAuditHiddenRef(metadata, recordID)
	if recordKey != "" {
		hidden = appendRemoteFileAuditHiddenRef(metadata, recordKey) || hidden
	}
	artifact.Metadata = metadata
	if hidden {
		if _, err := h.store.ReplaceArtifact(r.Context(), artifact); err != nil {
			h.writeErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"record_id":          recordID,
		"source_artifact_id": sourceArtifactID,
		"removed":            true,
	})
}

func splitRemoteFileAuditRecordID(recordID string) (sourceArtifactID string, recordKey string, ok bool) {
	value := strings.TrimSpace(recordID)
	if !strings.HasPrefix(value, remoteFileAuditRecordPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(value, remoteFileAuditRecordPrefix)
	sourceArtifactID, recordKey, ok = strings.Cut(rest, ":")
	sourceArtifactID = strings.TrimSpace(sourceArtifactID)
	recordKey = strings.TrimSpace(recordKey)
	if !ok || sourceArtifactID == "" || recordKey == "" {
		return "", "", false
	}
	return sourceArtifactID, recordKey, true
}

func appendRemoteFileAuditHiddenRef(metadata map[string]any, ref string) bool {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return false
	}
	seen := remoteFileAuditHiddenRefs(metadata)
	if _, exists := seen[ref]; exists {
		return false
	}
	values := remoteFileAuditHiddenRefValues(metadata)
	values = append(values, ref)
	metadata["file_audit_hidden_refs"] = values
	return true
}

func remoteFileAuditHiddenRefs(metadata map[string]any) map[string]struct{} {
	seen := map[string]struct{}{}
	for _, value := range remoteFileAuditHiddenRefValues(metadata) {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	return seen
}

func remoteFileAuditHiddenRefValues(metadata map[string]any) []string {
	if metadata == nil {
		return nil
	}
	raw := metadata["file_audit_hidden_refs"]
	if raw == nil {
		raw = metadata["fileAuditHiddenRefs"]
	}
	out := []string{}
	switch values := raw.(type) {
	case []string:
		for _, value := range values {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				out = append(out, trimmed)
			}
		}
	case []any:
		for _, value := range values {
			if text, ok := value.(string); ok {
				if trimmed := strings.TrimSpace(text); trimmed != "" {
					out = append(out, trimmed)
				}
			}
		}
	}
	return out
}

type artifactCleanupTarget struct {
	relPath string
	dir     bool
}

func artifactCleanupTargets(a store.Artifact) []artifactCleanupTarget {
	out := make([]artifactCleanupTarget, 0, 2)
	if a.ContentPath != nil && strings.TrimSpace(*a.ContentPath) != "" {
		out = append(out, artifactCleanupTarget{relPath: strings.TrimSpace(*a.ContentPath)})
	}
	extraction := plainMap(a.Metadata["extraction"])
	root := strings.TrimSpace(firstStringMapValue(extraction, "rootPath", "root_path"))
	if root != "" {
		out = append(out, artifactCleanupTarget{relPath: filepath.ToSlash(root), dir: true})
	}
	return out
}

// fileAuditMimeTypeKeys mirrors FILE_MIME_TYPE_KEYS in lib/fileProvenance.ts
// so /api/files rows resolve the same connector-native aliases (Slack
// `mimetype`, HTTP-ish `content_type`, ...) as the native file viewer.
var fileAuditMimeTypeKeys = []string{
	"detectedMimeType", "detected_mime_type",
	"normalizedMimeType", "normalized_mime_type",
	"mimeType", "mime_type",
	"mimetype", "mime",
	"contentType", "content_type",
}

// fileAuditFileKindKeys mirrors FILE_KIND_KEYS in lib/fileProvenance.ts.
// `kind` is last: tool envelopes use it for the generic result kind ("file"),
// so connector-specific keys like Slack's `filetype` must win over it.
var fileAuditFileKindKeys = []string{
	"fileKind", "file_kind",
	"fileType", "file_type", "filetype",
	"kind",
}

func fileAuditMimeType(maps ...map[string]any) string {
	for _, m := range maps {
		if value := firstStringMapValue(m, fileAuditMimeTypeKeys...); value != "" {
			return value
		}
	}
	return ""
}

// fileAuditFileKind skips the generic "file" placeholder so it cannot mask a
// real kind in a lower-priority key or map.
func fileAuditFileKind(maps ...map[string]any) string {
	for _, m := range maps {
		for _, key := range fileAuditFileKindKeys {
			value := firstStringMapValue(m, key)
			if value == "" || strings.EqualFold(value, "file") {
				continue
			}
			return value
		}
	}
	return ""
}

func fileAuditRecordDTO(a store.Artifact, threadTitle string) map[string]any {
	generatedImage := isGeneratedImageArtifact(a)
	metadata := artifactHydrationMetadata(a)
	resultMetadata := plainMap(metadata["result_metadata"])
	presentation := plainMap(metadata["tool_presentation"])
	if len(presentation) == 0 {
		presentation = plainMap(resultMetadata["tool_presentation"])
	}
	toolName := ""
	if a.ToolName != nil {
		toolName = strings.TrimSpace(*a.ToolName)
	}
	sourceTool := firstNonEmptyString(
		firstStringMapValue(metadata, "sourceToolName", "source_tool_name"),
		firstStringMapValue(resultMetadata, "sourceToolName", "source_tool_name", "tool_name"),
		toolName,
	)
	source := firstNonEmptyString(
		firstStringMapValue(metadata, "source"),
		firstStringMapValue(resultMetadata, "source"),
	)
	if generatedImage && source == "" {
		source = "generated_image"
	}
	sourceLabel := fileSourceLabel(source, sourceTool, resultMetadata, presentation)
	mimeType := fileAuditMimeType(metadata, resultMetadata)
	fileKind := fileAuditFileKind(metadata, resultMetadata)
	if generatedImage && fileKind == "" {
		fileKind = "image"
	}
	contentPath := ""
	if a.ContentPath != nil {
		contentPath = strings.TrimSpace(*a.ContentPath)
	}

	dto := map[string]any{
		"id":                a.ID,
		"artifact_id":       a.ID,
		"record_kind":       "cached_file",
		"availability":      "cached",
		"cached":            true,
		"openable":          true,
		"thread_id":         a.ThreadID,
		"thread_title":      strings.TrimSpace(threadTitle),
		"type":              a.Type,
		"title":             a.Title,
		"source":            source,
		"source_label":      sourceLabel,
		"source_tool_name":  sourceTool,
		"tool_name":         toolName,
		"tool_call_id":      auditStringPtrValue(a.ToolCallID),
		"file_kind":         fileKind,
		"mime_type":         mimeType,
		"byte_size":         a.ByteSize,
		"content_storage":   strings.TrimSpace(a.Storage),
		"content_path":      contentPath,
		"preview_text":      firstStringMapValue(metadata, "previewText", "preview_text"),
		"content_note":      firstStringMapValue(metadata, "contentNote", "content_note"),
		"preview_available": boolMapValue(metadata["previewAvailable"]) || boolMapValue(metadata["preview_available"]),
		"preview_truncated": boolMapValue(metadata["previewTruncated"]) || boolMapValue(metadata["preview_truncated"]),
		"downloadable":      fileartifact.IsFileAttachmentArtifact(a) || generatedImage,
		"created_at":        a.CreatedAt,
		"metadata":          metadata,
		"artifact":          fileAttachmentArtifactDTO(a),
	}
	if a.Language != nil && strings.TrimSpace(*a.Language) != "" {
		dto["language"] = strings.TrimSpace(*a.Language)
	}
	if product := firstNonEmptyString(firstStringMapValue(resultMetadata, "product"), firstStringMapValue(presentation, "family")); product != "" {
		dto["product"] = product
	}
	if family := firstNonEmptyString(firstStringMapValue(resultMetadata, "family"), firstStringMapValue(presentation, "family")); family != "" {
		dto["family"] = family
	}
	if integration := firstStringMapValue(resultMetadata, "integration", "tool_set_id"); integration != "" {
		dto["integration"] = integration
	}
	if managed := firstNonEmptyString(firstStringMapValue(metadata, "managedLocalPath", "managed_local_path"), firstStringMapValue(resultMetadata, "managedLocalPath", "managed_local_path")); managed != "" {
		dto["managed_local_path"] = managed
	}
	if remoteURL, remotePath, remoteRef := cachedFileRemoteLocators(metadata, resultMetadata); remoteURL != "" || remotePath != "" || remoteRef != "" {
		if remoteURL != "" {
			dto["remote_url"] = remoteURL
			dto["source_url"] = remoteURL
		}
		if remotePath != "" {
			dto["remote_path"] = remotePath
		}
		if remoteRef != "" {
			dto["remote_ref"] = remoteRef
		}
	}
	return dto
}

func cachedFileRemoteLocators(metadata map[string]any, resultMetadata map[string]any) (remoteURL string, remotePath string, remoteRef string) {
	toolArgs := plainMap(metadata["tool_args"])
	if len(toolArgs) == 0 {
		toolArgs = plainMap(metadata["toolArgs"])
	}
	remoteURL = firstNonEmptyString(
		firstStringMapValue(metadata, "remote_url", "remoteUrl", "source_url", "sourceUrl", "item_url", "itemUrl", "web_url", "webUrl", "url", "download_url", "downloadUrl", "url_private_download", "urlPrivateDownload", "url_private", "permalink", "pdf_url", "pdfUrl", "abs_url", "absUrl"),
		firstStringMapValue(resultMetadata, "remote_url", "remoteUrl", "source_url", "sourceUrl", "item_url", "itemUrl", "web_url", "webUrl", "url", "download_url", "downloadUrl", "url_private_download", "urlPrivateDownload", "url_private", "permalink", "pdf_url", "pdfUrl", "abs_url", "absUrl"),
		firstStringMapValue(toolArgs, "remote_url", "remoteUrl", "source_url", "sourceUrl", "item_url", "itemUrl", "web_url", "webUrl", "url", "download_url", "downloadUrl", "url_private_download", "urlPrivateDownload", "url_private", "permalink", "pdf_url", "pdfUrl", "abs_url", "absUrl"),
	)
	remotePath = firstNonEmptyString(
		firstStringMapValue(metadata, "remote_path", "remotePath", "server_relative_url", "serverRelativeUrl", "drive_path", "drivePath"),
		firstStringMapValue(resultMetadata, "remote_path", "remotePath", "server_relative_url", "serverRelativeUrl", "drive_path", "drivePath"),
		firstStringMapValue(toolArgs, "remote_path", "remotePath", "server_relative_url", "serverRelativeUrl", "drive_path", "drivePath", "path", "file_path", "filePath"),
	)
	remoteRef = firstNonEmptyString(
		firstStringMapValue(metadata, "remote_id", "remoteId", "file_id", "fileId", "attachmentId", "attachment_id", "item_id", "itemId", "driveItemId", "drive_item_id", "arxiv_id", "arxivId"),
		firstStringMapValue(resultMetadata, "remote_id", "remoteId", "file_id", "fileId", "attachmentId", "attachment_id", "item_id", "itemId", "driveItemId", "drive_item_id", "arxiv_id", "arxivId"),
		firstStringMapValue(toolArgs, "remote_id", "remoteId", "file_id", "fileId", "attachmentId", "attachment_id", "item_id", "itemId", "driveItemId", "drive_item_id", "arxiv_id", "arxivId"),
	)
	return remoteURL, remotePath, remoteRef
}

type remoteFileAuditRef struct {
	Title             string
	RemoteID          string
	RemoteURL         string
	RemotePath        string
	ArtifactID        string
	MimeType          string
	FileKind          string
	ByteSize          int64
	PreviewText       string
	RequestToolName   string
	RequestToolArgs   map[string]any
	DownloadPrompt    string
	ReferenceMetadata map[string]any
}

type cachedFileAuditIndex struct {
	artifactIDs map[string]struct{}
	locators    map[string]struct{}
}

func newCachedFileAuditIndex(artifacts []store.Artifact) cachedFileAuditIndex {
	idx := cachedFileAuditIndex{
		artifactIDs: map[string]struct{}{},
		locators:    map[string]struct{}{},
	}
	for _, artifact := range artifacts {
		if id := strings.TrimSpace(artifact.ID); id != "" {
			idx.artifactIDs[id] = struct{}{}
		}
		metadata := artifactHydrationMetadata(artifact)
		resultMetadata := plainMap(metadata["result_metadata"])
		presentation := plainMap(metadata["tool_presentation"])
		if len(presentation) == 0 {
			presentation = plainMap(resultMetadata["tool_presentation"])
		}
		toolName := ""
		if artifact.ToolName != nil {
			toolName = strings.TrimSpace(*artifact.ToolName)
		}
		sourceTool := firstNonEmptyString(
			firstStringMapValue(metadata, "sourceToolName", "source_tool_name"),
			firstStringMapValue(resultMetadata, "sourceToolName", "source_tool_name", "tool_name"),
			toolName,
		)
		scope := fileAuditRemoteLocatorScope(resultMetadata, presentation, sourceTool)
		remoteURL, remotePath, remoteRef := cachedFileRemoteLocators(metadata, resultMetadata)
		idx.addLocator("url", remoteURL)
		idx.addLocator("path", remotePath)
		idx.addLocator(fileAuditRefLocatorKind(scope), remoteRef)
	}
	return idx
}

func (idx cachedFileAuditIndex) addLocator(kind string, value string) {
	key := fileAuditLocatorKey(kind, value)
	if key == "" {
		return
	}
	idx.locators[key] = struct{}{}
}

func (idx cachedFileAuditIndex) hasLocator(kind string, value string) bool {
	key := fileAuditLocatorKey(kind, value)
	if key == "" {
		return false
	}
	_, ok := idx.locators[key]
	return ok
}

func (idx cachedFileAuditIndex) hasRemoteRef(ref remoteFileAuditRef, scope string) bool {
	if id := strings.TrimSpace(ref.ArtifactID); id != "" {
		if _, ok := idx.artifactIDs[id]; ok {
			return true
		}
	}
	return idx.hasLocator("url", ref.RemoteURL) ||
		idx.hasLocator("path", ref.RemotePath) ||
		idx.hasLocator(fileAuditRefLocatorKind(scope), ref.RemoteID)
}

func fileAuditLocatorKey(kind string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimRight(strings.ReplaceAll(value, "\\", "/"), "/")
	// PathUnescape, not QueryUnescape: a literal "+" in a file name must not
	// collapse into a space, or distinct files would share a locator key and
	// a real remote-only record could be suppressed from the audit.
	if unescaped, err := url.PathUnescape(value); err == nil {
		value = strings.TrimRight(strings.ReplaceAll(unescaped, "\\", "/"), "/")
	}
	value = strings.ToLower(value)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "value"
	}
	return kind + ":" + value
}

func fileAuditRemoteLocatorScope(resultMetadata map[string]any, presentation map[string]any, sourceTool string) string {
	scope := firstNonEmptyString(
		firstStringMapValue(resultMetadata, "family", "product", "integration", "tool_set_id"),
		firstStringMapValue(presentation, "family", "product", "integration", "tool_set_id"),
	)
	if scope != "" {
		return scope
	}
	tool := strings.ToLower(strings.TrimSpace(sourceTool))
	for _, family := range []string{"sharepoint", "slack", "jira", "atlassian", "arxiv", "github"} {
		if strings.Contains(tool, family) {
			return family
		}
	}
	return ""
}

func fileAuditRefLocatorKind(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "ref"
	}
	return "ref:" + scope
}

func (h *Handler) remoteFileAuditRecordDTOs(a store.Artifact, threadTitle string, cachedIndex cachedFileAuditIndex) []map[string]any {
	metadata := artifactHydrationMetadata(a)
	resultMetadata := plainMap(metadata["result_metadata"])
	presentation := plainMap(metadata["tool_presentation"])
	if len(presentation) == 0 {
		presentation = plainMap(resultMetadata["tool_presentation"])
	}
	toolName := ""
	if a.ToolName != nil {
		toolName = strings.TrimSpace(*a.ToolName)
	}
	sourceTool := firstNonEmptyString(
		firstStringMapValue(metadata, "sourceToolName", "source_tool_name"),
		firstStringMapValue(resultMetadata, "sourceToolName", "source_tool_name", "tool_name"),
		toolName,
	)
	if !artifactCanContainRemoteFileRefs(toolName, resultMetadata, presentation) {
		return nil
	}
	result, ok := h.auditArtifactResultMap(a)
	if !ok {
		return nil
	}
	refs := collectRemoteFileAuditRefs(result, resultMetadata, presentation, metadata, toolName)
	if len(refs) == 0 && artifactIsRemoteFileResult(resultMetadata, presentation) {
		if ref, ok := remoteFileAuditRefFromMap(result, resultMetadata, presentation, metadata, toolName); ok {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil
	}

	source := firstNonEmptyString(
		firstStringMapValue(metadata, "source"),
		firstStringMapValue(resultMetadata, "source"),
		"external_tool",
	)
	sourceLabel := fileSourceLabel(source, sourceTool, resultMetadata, presentation)
	refScope := fileAuditRemoteLocatorScope(resultMetadata, presentation, sourceTool)
	hiddenRefs := remoteFileAuditHiddenRefs(metadata)
	out := make([]map[string]any, 0, len(refs))
	seen := map[string]struct{}{}
	for idx, ref := range refs {
		if cachedIndex.hasRemoteRef(ref, refScope) {
			continue
		}
		key := firstNonEmptyString(ref.ArtifactID, ref.RemoteID, ref.RemoteURL, ref.RemotePath, ref.Title)
		if key == "" {
			continue
		}
		key = strings.ToLower(key)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		requestTool := firstNonEmptyString(ref.RequestToolName, suggestedRemoteFileDownloadTool(toolName, sourceLabel), toolName)
		requestArgs := cloneAuditMap(ref.RequestToolArgs)
		if requestArgs == nil {
			requestArgs = map[string]any{}
		}
		requestArgs = enrichRemoteFileRequestArgs(requestArgs, requestTool, sourceLabel, ref)
		prompt := strings.TrimSpace(ref.DownloadPrompt)
		if prompt == "" {
			prompt = remoteFileDownloadPrompt(sourceLabel, requestTool, ref)
		}

		recordKey := auditRemoteRecordKey(key, idx)
		recordID := remoteFileAuditRecordPrefix + a.ID + ":" + recordKey
		if _, hidden := hiddenRefs[recordID]; hidden {
			continue
		}
		if _, hidden := hiddenRefs[recordKey]; hidden {
			continue
		}
		dto := map[string]any{
			"id":                      recordID,
			"record_kind":             "remote_reference",
			"availability":            "remote_only",
			"cached":                  false,
			"openable":                false,
			"can_request_download":    true,
			"removable":               true,
			"thread_id":               a.ThreadID,
			"thread_title":            strings.TrimSpace(threadTitle),
			"type":                    "file_reference",
			"title":                   firstNonEmptyString(ref.Title, ref.RemotePath, ref.RemoteURL, ref.RemoteID, "Remote file"),
			"source":                  source,
			"source_label":            sourceLabel,
			"source_tool_name":        sourceTool,
			"tool_name":               toolName,
			"tool_call_id":            auditStringPtrValue(a.ToolCallID),
			"file_kind":               ref.FileKind,
			"mime_type":               ref.MimeType,
			"byte_size":               ref.ByteSize,
			"preview_text":            ref.PreviewText,
			"preview_available":       false,
			"preview_truncated":       false,
			"downloadable":            false,
			"created_at":              a.CreatedAt,
			"remote_id":               ref.RemoteID,
			"remote_url":              ref.RemoteURL,
			"source_url":              ref.RemoteURL,
			"remote_path":             ref.RemotePath,
			"request_tool_name":       requestTool,
			"request_tool_args":       requestArgs,
			"request_download_prompt": prompt,
			"metadata": map[string]any{
				"source":            source,
				"sourceToolName":    sourceTool,
				"source_tool_name":  sourceTool,
				"tool_args":         requestArgs,
				"remote_reference":  ref.ReferenceMetadata,
				"result_metadata":   resultMetadata,
				"tool_presentation": presentation,
			},
		}
		if ref.ArtifactID != "" {
			dto["referenced_artifact_id"] = ref.ArtifactID
		}
		if product := firstNonEmptyString(firstStringMapValue(resultMetadata, "product"), firstStringMapValue(presentation, "family")); product != "" {
			dto["product"] = product
		}
		if family := firstNonEmptyString(firstStringMapValue(resultMetadata, "family"), firstStringMapValue(presentation, "family")); family != "" {
			dto["family"] = family
		}
		if integration := firstStringMapValue(resultMetadata, "integration", "tool_set_id"); integration != "" {
			dto["integration"] = integration
		}
		out = append(out, dto)
	}
	return out
}

func enrichRemoteFileRequestArgs(requestArgs map[string]any, requestTool string, sourceLabel string, ref remoteFileAuditRef) map[string]any {
	if requestArgs == nil {
		requestArgs = map[string]any{}
	}
	putIfMissing := func(key string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		if _, ok := requestArgs[key]; ok {
			return
		}
		requestArgs[key] = strings.TrimSpace(value)
	}

	putIfMissing("remote_id", ref.RemoteID)
	putIfMissing("source_url", ref.RemoteURL)
	putIfMissing("remote_path", ref.RemotePath)

	tool := strings.ToLower(strings.TrimSpace(requestTool))
	label := strings.ToLower(strings.TrimSpace(sourceLabel))
	switch {
	case strings.Contains(label, "sharepoint") || strings.HasPrefix(tool, "sharepoint_"):
		putIfMissing("item_url", ref.RemoteURL)
		putIfMissing("server_relative_url", ref.RemotePath)
	case strings.Contains(label, "jira") || strings.HasPrefix(tool, "jira_"):
		putIfMissing("attachmentId", ref.RemoteID)
	case strings.Contains(label, "slack") || strings.HasPrefix(tool, "slack_"):
		putIfMissing("file_id", ref.RemoteID)
	case strings.Contains(label, "arxiv") || strings.HasPrefix(tool, "arxiv_"):
		putIfMissing("arxiv_id", ref.RemoteID)
	}
	return requestArgs
}

func (h *Handler) auditArtifactResultMap(a store.Artifact) (map[string]any, bool) {
	content := ""
	if a.ContentText != nil {
		content = strings.TrimSpace(*a.ContentText)
	} else if strings.EqualFold(strings.TrimSpace(a.Storage), "file") && a.ContentPath != nil && strings.TrimSpace(*a.ContentPath) != "" {
		rel, err := cleanRelPath(strings.TrimSpace(*a.ContentPath))
		if err != nil {
			return nil, false
		}
		full := filepath.Join(h.cfgDir, filepath.FromSlash(rel))
		b, err := osReadFileLimit(full, 2<<20)
		if err != nil {
			return nil, false
		}
		content = strings.TrimSpace(string(b))
	}
	if content == "" {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(content), &decoded); err != nil {
		return nil, false
	}
	if result, ok := decoded.(map[string]any); ok {
		return result, true
	}
	return nil, false
}

func artifactCanContainRemoteFileRefs(toolName string, resultMetadata map[string]any, presentation map[string]any) bool {
	if artifactIsRemoteFileResult(resultMetadata, presentation) {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(toolName))
	if strings.Contains(name, "attachment") ||
		strings.Contains(name, "sharepoint") ||
		strings.Contains(name, "slack") ||
		strings.Contains(name, "arxiv") ||
		strings.Contains(name, "jira") ||
		strings.Contains(name, "github") {
		return true
	}
	family := strings.ToLower(firstNonEmptyString(
		firstStringMapValue(resultMetadata, "family", "product", "integration"),
		firstStringMapValue(presentation, "family"),
	))
	return family == "sharepoint" || family == "slack" || family == "jira" || family == "atlassian" || family == "arxiv"
}

func artifactIsRemoteFileResult(resultMetadata map[string]any, presentation map[string]any) bool {
	fileCapability := strings.ToLower(firstNonEmptyString(
		firstStringMapValue(resultMetadata, "file_capability"),
		firstStringMapValue(presentation, "file_capability"),
	))
	if fileCapability == "remote_file_download" || fileCapability == "remote_file_read" {
		return true
	}
	nativeViewer := strings.ToLower(firstNonEmptyString(
		firstStringMapValue(resultMetadata, "native_viewer"),
		firstStringMapValue(presentation, "native_viewer"),
	))
	resultKind := strings.ToLower(firstNonEmptyString(
		firstStringMapValue(resultMetadata, "result_kind", "kind"),
		firstStringMapValue(presentation, "result_kind"),
	))
	return nativeViewer == "file" || resultKind == "file"
}

func collectRemoteFileAuditRefs(result map[string]any, resultMetadata map[string]any, presentation map[string]any, artifactMetadata map[string]any, toolName string) []remoteFileAuditRef {
	out := make([]remoteFileAuditRef, 0, 8)
	var walk func(value any, inherited map[string]any, depth int)
	walk = func(value any, inherited map[string]any, depth int) {
		if depth > 5 || len(out) >= 40 {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			nextInherited := mergeAuditInheritedFileContext(inherited, typed)
			if ref, ok := remoteFileAuditRefFromMap(typed, resultMetadata, presentation, artifactMetadata, toolName); ok {
				for key, value := range nextInherited {
					if _, exists := ref.RequestToolArgs[key]; !exists {
						ref.RequestToolArgs[key] = value
					}
				}
				out = append(out, ref)
			}
			for key, child := range typed {
				if !auditFileContainerKey(key) && depth > 0 {
					continue
				}
				walk(child, nextInherited, depth+1)
			}
		case []any:
			for _, child := range typed {
				walk(child, inherited, depth+1)
			}
		}
	}
	walk(result, map[string]any{}, 0)
	return out
}

func remoteFileAuditRefFromMap(raw map[string]any, resultMetadata map[string]any, presentation map[string]any, artifactMetadata map[string]any, toolName string) (remoteFileAuditRef, bool) {
	if raw == nil {
		return remoteFileAuditRef{}, false
	}
	title := firstNonEmptyString(
		firstStringMapValue(raw, "fileName", "file_name", "filename", "name", "title"),
		pathBase(firstStringMapValue(raw, "server_relative_url", "serverRelativeUrl", "path", "drive_path")),
		pathBase(firstStringMapValue(raw, "url", "web_url", "webUrl", "item_url", "itemUrl", "source_url", "sourceUrl")),
	)
	remoteID := firstStringMapValue(raw, "remote_id", "remoteId", "file_id", "fileId", "attachmentId", "attachment_id", "item_id", "itemId", "driveItemId", "drive_item_id", "arxiv_id", "arxivId", "id")
	remoteURL := firstStringMapValue(raw, "source_url", "sourceUrl", "item_url", "itemUrl", "web_url", "webUrl", "url", "download_url", "downloadUrl", "url_private_download", "urlPrivateDownload", "url_private", "permalink", "pdf_url", "pdfUrl", "abs_url", "absUrl")
	remotePath := firstStringMapValue(raw, "server_relative_url", "serverRelativeUrl", "drive_path", "drivePath", "path", "file_path", "filePath")
	mimeType := fileAuditMimeType(raw, resultMetadata)
	fileKind := fileAuditFileKind(raw, resultMetadata)
	artifactID := firstStringMapValue(raw, "attachment_artifact_id", "attachmentArtifactId", "file_artifact_id", "artifact_id", "artifactId")
	if artifactID == "" {
		artifactID = firstStringMapValue(resultMetadata, "attachment_artifact_id", "attachmentArtifactId", "file_artifact_id")
	}
	if title == "" && remoteID == "" && remoteURL == "" && remotePath == "" && artifactID == "" {
		return remoteFileAuditRef{}, false
	}
	if !auditMapLooksLikeFileReference(raw, title, remoteURL, remotePath, mimeType, fileKind, resultMetadata, presentation) {
		return remoteFileAuditRef{}, false
	}
	requestArgs := remoteFileRequestArgs(raw, artifactMetadata)
	ref := remoteFileAuditRef{
		Title:           firstNonEmptyString(title, "Remote file"),
		RemoteID:        remoteID,
		RemoteURL:       remoteURL,
		RemotePath:      remotePath,
		ArtifactID:      artifactID,
		MimeType:        mimeType,
		FileKind:        fileKind,
		ByteSize:        auditInt64MapValue(raw, "sizeBytes", "size_bytes", "bytes", "byte_size", "file_size", "fileSize", "size"),
		PreviewText:     firstStringMapValue(raw, "previewText", "preview_text", "snippet", "summary", "text"),
		RequestToolName: suggestedRemoteFileDownloadTool(toolName, fileSourceLabel("", toolName, resultMetadata, presentation)),
		RequestToolArgs: requestArgs,
		ReferenceMetadata: map[string]any{
			"remote_id":   remoteID,
			"remote_url":  remoteURL,
			"remote_path": remotePath,
			"file_name":   title,
			"mime_type":   mimeType,
			"file_kind":   fileKind,
		},
	}
	return ref, true
}

func auditMapLooksLikeFileReference(raw map[string]any, title string, remoteURL string, remotePath string, mimeType string, fileKind string, resultMetadata map[string]any, presentation map[string]any) bool {
	if artifactIsRemoteFileResult(resultMetadata, presentation) {
		return true
	}
	// The raw-key check keeps the generic envelope kind ("file") counting as a
	// file signal even though fileAuditFileKind filters it out of the DTO value.
	if mimeType != "" || fileKind != "" || firstStringMapValue(raw, fileAuditFileKindKeys...) != "" {
		return true
	}
	for _, key := range []string{
		"fileName", "file_name", "filename", "file_id", "fileId", "attachmentId", "attachment_id",
		"server_relative_url", "serverRelativeUrl", "item_url", "itemUrl", "download_url", "downloadUrl",
		"url_private_download", "urlPrivateDownload", "url_private", "driveItemId", "drive_item_id",
		"arxiv_id", "arxivId", "pdf_url", "pdfUrl",
	} {
		if firstStringMapValue(raw, key) != "" {
			return true
		}
	}
	return fileAuditLooksLikeFileName(title) || fileAuditLooksLikeFileName(remotePath) || fileAuditLooksLikeFileName(remoteURL)
}

func remoteFileRequestArgs(raw map[string]any, artifactMetadata map[string]any) map[string]any {
	out := cloneAuditMap(plainMap(artifactMetadata["tool_args"]))
	if out == nil {
		out = map[string]any{}
	}
	for _, key := range []string{
		"issueKey", "issue_key", "attachmentId", "attachment_id", "file_id", "fileId", "channel_id",
		"item_url", "itemUrl", "server_relative_url", "serverRelativeUrl", "site_url", "siteUrl",
		"arxiv_id", "arxivId", "remote_id", "remoteId",
	} {
		if value := firstStringMapValue(raw, key); value != "" {
			out[key] = value
		}
	}
	return out
}

func mergeAuditInheritedFileContext(parent map[string]any, current map[string]any) map[string]any {
	out := cloneAuditMap(parent)
	if out == nil {
		out = map[string]any{}
	}
	for _, key := range []string{"issueKey", "issue_key", "channel_id", "site_url", "siteUrl", "arxiv_id", "arxivId"} {
		if value := firstStringMapValue(current, key); value != "" {
			out[key] = value
		}
	}
	return out
}

func auditFileContainerKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "attachments", "attachment", "files", "file", "items", "item", "results", "entries", "documents", "value", "matches", "downloads", "papers":
		return true
	default:
		return false
	}
}

func suggestedRemoteFileDownloadTool(toolName string, sourceLabel string) string {
	name := strings.TrimSpace(toolName)
	label := strings.ToLower(strings.TrimSpace(sourceLabel))
	switch {
	case strings.Contains(label, "sharepoint") || strings.HasPrefix(name, "sharepoint_"):
		return "sharepoint_read"
	case strings.Contains(label, "slack") || strings.HasPrefix(name, "slack_"):
		return "slack_download_file"
	case strings.Contains(label, "jira") || strings.HasPrefix(name, "jira_"):
		return "jira_read_attachment"
	case strings.Contains(label, "arxiv") || strings.HasPrefix(name, "arxiv_"):
		return "arxiv_download_paper"
	default:
		return name
	}
}

func remoteFileDownloadPrompt(sourceLabel string, requestTool string, ref remoteFileAuditRef) string {
	parts := []string{}
	if ref.Title != "" {
		parts = append(parts, fmt.Sprintf("%q", ref.Title))
	}
	if ref.RemotePath != "" {
		parts = append(parts, "path "+ref.RemotePath)
	}
	if ref.RemoteID != "" {
		parts = append(parts, "id "+ref.RemoteID)
	}
	if ref.RemoteURL != "" {
		parts = append(parts, "url "+ref.RemoteURL)
	}
	target := strings.Join(parts, ", ")
	if target == "" {
		target = "the selected remote file"
	}
	tool := strings.TrimSpace(requestTool)
	if tool == "" {
		tool = "the relevant connector tool"
	}
	source := strings.TrimSpace(sourceLabel)
	if source == "" {
		source = "remote connector"
	}
	return "Please download or read the " + source + " file " + target + " using " + tool + ", cache it as a first-class file artifact if needed, then open it with the native file viewer. Ask me for any required permission."
}

func auditInt64MapValue(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case int:
			return int64(v)
		case int64:
			return v
		case float64:
			return int64(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n
			}
		}
	}
	return 0
}

func fileAuditLooksLikeFileName(value string) bool {
	clean := strings.TrimSpace(strings.ToLower(value))
	if clean == "" {
		return false
	}
	if q := strings.Index(clean, "?"); q >= 0 {
		clean = clean[:q]
	}
	ext := strings.TrimPrefix(filepath.Ext(clean), ".")
	switch ext {
	case "pdf", "txt", "md", "markdown", "csv", "tsv", "json", "jsonl", "xml", "html", "htm", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "png", "jpg", "jpeg", "gif", "webp", "zip":
		return true
	default:
		return false
	}
}

func pathBase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if q := strings.Index(value, "?"); q >= 0 {
		value = value[:q]
	}
	value = strings.TrimRight(strings.ReplaceAll(value, "\\", "/"), "/")
	if value == "" {
		return ""
	}
	return filepath.Base(value)
}

func cloneAuditMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		if strings.TrimSpace(key) == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func auditRemoteRecordKey(key string, fallback int) string {
	clean := strings.ToLower(strings.TrimSpace(key))
	clean = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return '-'
	}, clean)
	clean = strings.Trim(clean, "-")
	if len(clean) > 48 {
		clean = clean[:48]
	}
	if clean == "" {
		return fmt.Sprintf("%d", fallback)
	}
	return clean
}

func auditRecordCreatedAt(record map[string]any) time.Time {
	switch v := record["created_at"].(type) {
	case time.Time:
		return v
	case string:
		if ts, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func plainMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func auditStringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func fileSourceLabel(source string, sourceTool string, resultMetadata map[string]any, presentation map[string]any) string {
	source = strings.TrimSpace(strings.ToLower(source))
	sourceTool = strings.TrimSpace(strings.ToLower(sourceTool))
	product := strings.TrimSpace(firstNonEmptyString(
		firstStringMapValue(resultMetadata, "product", "integration"),
		firstStringMapValue(presentation, "family"),
	))
	switch {
	case strings.EqualFold(source, "chat_upload"):
		return "Chat upload"
	case strings.EqualFold(source, "generated_image") || strings.EqualFold(source, "image_generation") || sourceTool == "generate_image":
		return "Generated"
	case strings.Contains(sourceTool, "jira") || strings.Contains(product, "jira") || strings.Contains(product, "atlassian"):
		return "Jira"
	case strings.Contains(sourceTool, "sharepoint") || strings.Contains(product, "sharepoint"):
		return "SharePoint"
	case strings.Contains(sourceTool, "slack") || strings.Contains(product, "slack"):
		return "Slack"
	case strings.Contains(sourceTool, "arxiv") || strings.Contains(product, "arxiv"):
		return "arXiv"
	case strings.Contains(sourceTool, "github") || strings.Contains(product, "github"):
		return "GitHub"
	case strings.EqualFold(source, fileartifact.SourceExternalTool):
		return "Connector download"
	case strings.EqualFold(source, "external_api"):
		return "Remote connector"
	default:
		if strings.TrimSpace(product) != "" {
			return strings.TrimSpace(product)
		}
		return "File artifact"
	}
}
