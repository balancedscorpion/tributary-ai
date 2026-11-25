package optionc

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/filepipeline"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

const (
	externalToolFileArtifactSource = fileartifact.SourceExternalTool
	externalToolFileMaxBytes       = 128 << 20
)

type externalToolFileCandidate struct {
	LocalPath  string
	FileName   string
	MimeType   string
	SizeBytes  int64
	SourceURL  string
	RemotePath string
	RemoteRef  string
}

type pendingExternalToolFileArtifact struct {
	Artifact store.Artifact
}

func (m *Manager) prepareExternalToolFileArtifacts(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	msgID string,
	toolCallID string,
	out *tools.Output,
) []pendingExternalToolFileArtifact {
	if m == nil || strings.TrimSpace(m.cfgDir) == "" || out == nil || !out.OK {
		return nil
	}
	resultMap, ok := out.Result.(map[string]any)
	if !ok || len(resultMap) == 0 {
		return nil
	}

	toolName := strings.TrimSpace(call.Name)
	candidates := collectExternalToolFileCandidates(resultMap)
	if len(candidates) == 0 {
		return nil
	}

	pending := make([]pendingExternalToolFileArtifact, 0, len(candidates))
	seenPaths := map[string]struct{}{}
	artifactIDs := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		fullPath, managedRelPath, err := m.resolveManagedExternalToolPath(candidate.LocalPath)
		if err != nil {
			log.Printf("run %s: skipped managed tool file for %s: %v", run.ID, toolName, err)
			continue
		}
		if _, seen := seenPaths[fullPath]; seen {
			continue
		}
		seenPaths[fullPath] = struct{}{}

		artifactID := types.NewUUID()
		artifact, err := m.ingestExternalToolFileArtifact(ctx, thread, msgID, toolCallID, toolName, call.Args, artifactID, fullPath, managedRelPath, candidate)
		if err != nil {
			log.Printf("run %s: failed to ingest managed tool file for %s: %v", run.ID, toolName, err)
			continue
		}
		if planID := strings.TrimSpace(run.PlanID); planID != "" {
			if artifact.Metadata == nil {
				artifact.Metadata = map[string]any{}
			}
			artifact.Metadata["plan_id"] = planID
		}
		pending = append(pending, pendingExternalToolFileArtifact{Artifact: artifact})
		artifactIDs = append(artifactIDs, artifact.ID)
	}

	if len(artifactIDs) == 0 {
		return nil
	}
	annotateExternalToolFileResult(resultMap, artifactIDs)
	annotateExternalToolFileResultMetadata(out, pending, artifactIDs)
	return pending
}

func (m *Manager) persistExternalToolFileArtifacts(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	toolName string,
	pending []pendingExternalToolFileArtifact,
) []types.FileAttachment {
	if len(pending) == 0 {
		return nil
	}
	refs := make([]types.FileAttachment, 0, len(pending))
	for _, item := range pending {
		created, err := m.store.InsertArtifact(ctx, item.Artifact)
		if err != nil {
			if errorsIsConflict(err) {
				log.Printf("run %s: external tool file artifact %s already exists; reusing existing artifact", run.ID, item.Artifact.ID)
				created = item.Artifact
			} else {
				log.Printf("run %s: failed to persist external tool file artifact %s for %s: %v", run.ID, item.Artifact.ID, toolName, err)
				continue
			}
		}
		refs = append(refs, types.FileAttachment{ArtifactID: created.ID})
	}
	if len(refs) == 0 {
		return nil
	}
	attachments := m.hydrateReferencedFileAttachments(ctx, thread.ID, refs)
	attachments = m.hydrateFileAttachmentInlineData(ctx, thread.ID, attachments)
	return attachments
}

func (m *Manager) ingestExternalToolFileArtifact(
	ctx context.Context,
	thread store.Thread,
	msgID string,
	toolCallID string,
	toolName string,
	toolArgs map[string]any,
	artifactID string,
	sourcePath string,
	managedRelPath string,
	candidate externalToolFileCandidate,
) (store.Artifact, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return store.Artifact{}, err
	}
	if info.IsDir() {
		return store.Artifact{}, fmt.Errorf("managed tool file path points to a directory")
	}
	if info.Size() > externalToolFileMaxBytes {
		return store.Artifact{}, fmt.Errorf("managed tool file exceeds cap of %d bytes", externalToolFileMaxBytes)
	}

	fileName, err := fileingest.SafeFileName(firstNonEmptyString(candidate.FileName, filepath.Base(sourcePath), "download"))
	if err != nil {
		fileName = "download"
	}
	ext := strings.TrimSpace(filepath.Ext(fileName))
	relPath := filepath.ToSlash(filepath.Join("tool_files", strings.TrimSpace(thread.ID), artifactID+ext))
	targetPath := filepath.Join(m.cfgDir, filepath.FromSlash(relPath))

	f, err := os.Open(sourcePath)
	if err != nil {
		return store.Artifact{}, err
	}
	defer f.Close()

	ingested, err := fileingest.Ingest(fileingest.IngestRequest{
		Scope:        fileingest.ScopeThread,
		Source:       fileingest.SourceConnector,
		FileName:     fileName,
		DeclaredMIME: candidate.MimeType,
		Reader:       f,
		TargetPath:   targetPath,
		MaxBytes:     externalToolFileMaxBytes,
		PreviewBytes: fileingest.DefaultPreviewBytes,
	})
	if err != nil {
		return store.Artifact{}, err
	}

	evaluation := ingested.Evaluation
	metadata := evaluation.Metadata(fileingest.SourceConnector)
	metadata["source"] = externalToolFileArtifactSource
	metadata["externalToolFile"] = true
	metadata["external_tool_file"] = true
	metadata["sourceToolName"] = toolName
	metadata["source_tool_name"] = toolName
	if len(toolArgs) > 0 {
		metadata["tool_args"] = cloneStringAnyMap(toolArgs)
	}
	metadata["managedLocalPath"] = managedRelPath
	metadata["managed_local_path"] = managedRelPath
	applyExternalToolFileRemoteMetadata(metadata, candidate)
	metadata["result_metadata"] = externalToolFileResultMetadata(toolName, fileName, managedRelPath, candidate)
	if meta, ok := tools.ToolMetadataByName(toolName); ok {
		metadata["tool_presentation"] = meta.Presentation
	}
	extractionRelPath := filepath.ToSlash(filepath.Join("tool_files", strings.TrimSpace(thread.ID), artifactID+".extract"))
	if extraction := filepipeline.ExtractFile(ctx, filepipeline.ExtractionRequest{
		SourcePath:          targetPath,
		OutputDir:           filepath.Join(m.cfgDir, filepath.FromSlash(extractionRelPath)),
		OutputRelPath:       extractionRelPath,
		FileName:            fileName,
		Evaluation:          evaluation,
		IncludeRawXML:       true,
		EnableExternalTools: false,
	}); extraction != nil {
		metadata["extraction"] = extraction
	}

	okVal := true
	var messageID *string
	if strings.TrimSpace(msgID) != "" {
		messageID = &msgID
	}
	artifact := store.Artifact{
		ID:          artifactID,
		ThreadID:    thread.ID,
		MessageID:   messageID,
		Type:        "file",
		Title:       fileName,
		ToolName:    &toolName,
		ToolCallID:  &toolCallID,
		OK:          &okVal,
		Storage:     "file",
		ContentPath: &relPath,
		ByteSize:    ingested.ByteSize,
		Metadata:    metadata,
	}
	if evaluation.Language != "" {
		artifact.Language = &evaluation.Language
	}
	return artifact, nil
}

func externalToolFileResultMetadata(toolName string, fileName string, managedRelPath string, candidate externalToolFileCandidate) map[string]any {
	result := map[string]any{
		"external_tool_file": true,
		"file_name":          strings.TrimSpace(fileName),
		"managed_local_path": strings.TrimSpace(managedRelPath),
	}
	metadata := map[string]any{
		"external_tool_file": true,
		"source_tool_name":   strings.TrimSpace(toolName),
		"file_name":          strings.TrimSpace(fileName),
		"managed_local_path": strings.TrimSpace(managedRelPath),
	}
	applyExternalToolFileRemoteMetadata(result, candidate)
	applyExternalToolFileRemoteMetadata(metadata, candidate)
	out := enrichToolOutputResultMetadata(toolName, tools.Output{
		OK:             true,
		ResultFormat:   tools.ResultFormatJSON,
		Result:         result,
		ResultMetadata: metadata,
	})
	return cloneStringAnyMap(out.ResultMetadata)
}

func applyExternalToolFileRemoteMetadata(target map[string]any, candidate externalToolFileCandidate) {
	if target == nil {
		return
	}
	if sourceURL := strings.TrimSpace(candidate.SourceURL); sourceURL != "" {
		target["source_url"] = sourceURL
		target["remote_url"] = sourceURL
	}
	if remotePath := strings.TrimSpace(candidate.RemotePath); remotePath != "" {
		target["remote_path"] = remotePath
	}
	if remoteRef := strings.TrimSpace(candidate.RemoteRef); remoteRef != "" {
		target["remote_id"] = remoteRef
		target["remote_ref"] = remoteRef
	}
}

func (m *Manager) resolveManagedExternalToolPath(raw string) (string, string, error) {
	localPath := strings.TrimSpace(raw)
	if localPath == "" {
		return "", "", fmt.Errorf("local path is empty")
	}
	baseAbs, err := filepath.Abs(m.cfgDir)
	if err != nil {
		return "", "", fmt.Errorf("invalid app config directory: %w", err)
	}
	target := localPath
	if !filepath.IsAbs(target) {
		clean, err := cleanFileAttachmentRelPath(target)
		if err != nil {
			return "", "", err
		}
		target = filepath.Join(baseAbs, filepath.FromSlash(clean))
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("invalid local path: %w", err)
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("local path must be inside the app-managed config directory")
	}
	relSlash := filepath.ToSlash(rel)
	if !fileartifact.IsManagedDownloadRelPath(relSlash) {
		return "", "", fmt.Errorf("local path must point to an app-managed external tool download")
	}
	return targetAbs, relSlash, nil
}

func collectExternalToolFileCandidates(result map[string]any) []externalToolFileCandidate {
	if len(result) == 0 {
		return nil
	}
	out := make([]externalToolFileCandidate, 0, 4)
	var walk func(value any, inherited externalToolFileCandidate, depth int)
	walk = func(value any, inherited externalToolFileCandidate, depth int) {
		if depth > 5 || len(out) >= 8 {
			return
		}
		switch typed := value.(type) {
		case map[string]any:
			next := inherited
			if fileName := firstStringMapValue(typed, "fileName", "file_name", "filename", "name", "title"); fileName != "" {
				next.FileName = fileName
			}
			if mimeType := firstStringMapValue(typed, "mimeType", "mime_type", "contentType", "content_type"); mimeType != "" {
				next.MimeType = mimeType
			}
			if sizeBytes, ok := int64MapValue(typed, "sizeBytes", "size_bytes", "bytesTotal", "bytes_total"); ok && sizeBytes > 0 {
				next.SizeBytes = sizeBytes
			}
			if sourceURL := firstExternalToolFileSourceURL(typed); sourceURL != "" {
				next.SourceURL = sourceURL
			}
			if remotePath := firstStringMapValue(typed, "remote_path", "remotePath", "server_relative_url", "serverRelativeUrl", "drive_path", "drivePath", "path", "file_path", "filePath"); remotePath != "" {
				next.RemotePath = remotePath
			}
			if remoteRef := firstStringMapValue(typed, "remote_id", "remoteId", "file_id", "fileId", "attachmentId", "attachment_id", "item_id", "itemId", "driveItemId", "drive_item_id", "arxiv_id", "arxivId", "id"); remoteRef != "" {
				next.RemoteRef = remoteRef
			}
			if next.SourceURL == "" {
				next.SourceURL = firstExternalToolFileSiblingSourceURL(typed)
			}
			if next.RemotePath == "" {
				next.RemotePath = firstExternalToolFileSiblingRemotePath(typed)
			}
			if next.RemoteRef == "" {
				next.RemoteRef = firstExternalToolFileSiblingRemoteRef(typed)
			}
			if localPath := firstStringMapValue(typed, "localPath", "local_path"); localPath != "" {
				candidate := next
				candidate.LocalPath = localPath
				out = append(out, candidate)
			}
			for _, child := range typed {
				walk(child, next, depth+1)
			}
		case []any:
			for _, child := range typed {
				walk(child, inherited, depth+1)
			}
		}
	}
	walk(result, externalToolFileCandidate{}, 0)
	return out
}

func firstExternalToolFileSiblingSourceURL(m map[string]any) string {
	for _, key := range []string{"file", "paper", "attachment", "item", "drive_item", "driveItem", "metadata"} {
		if sourceURL := firstExternalToolFileSourceURL(plainMapForExternalToolFile(m[key])); sourceURL != "" {
			return sourceURL
		}
	}
	return ""
}

func firstExternalToolFileSiblingRemotePath(m map[string]any) string {
	for _, key := range []string{"file", "paper", "attachment", "item", "drive_item", "driveItem", "metadata"} {
		if remotePath := firstStringMapValue(plainMapForExternalToolFile(m[key]), "remote_path", "remotePath", "server_relative_url", "serverRelativeUrl", "drive_path", "drivePath", "path", "file_path", "filePath"); remotePath != "" {
			return remotePath
		}
	}
	return ""
}

func firstExternalToolFileSiblingRemoteRef(m map[string]any) string {
	for _, key := range []string{"file", "paper", "attachment", "item", "drive_item", "driveItem", "metadata"} {
		if remoteRef := firstStringMapValue(plainMapForExternalToolFile(m[key]), "remote_id", "remoteId", "file_id", "fileId", "attachmentId", "attachment_id", "item_id", "itemId", "driveItemId", "drive_item_id", "arxiv_id", "arxivId", "id"); remoteRef != "" {
			return remoteRef
		}
	}
	return ""
}

func firstExternalToolFileSourceURL(m map[string]any) string {
	sourceURL := firstStringMapValue(
		m,
		"source_url", "sourceUrl", "remote_url", "remoteUrl",
		"item_url", "itemUrl", "web_url", "webUrl",
		"permalink", "permalink_public", "pdf_url", "pdfUrl", "abs_url", "absUrl",
		"download_url", "downloadUrl", "url_private_download", "urlPrivateDownload", "url_private",
		"content_url", "contentUrl", "self_url", "selfUrl", "url",
	)
	if !externalToolFileLooksLikeRemoteURL(sourceURL) {
		return ""
	}
	return sourceURL
}

func externalToolFileLooksLikeRemoteURL(raw string) bool {
	lower := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(lower, "https://") || strings.HasPrefix(lower, "http://")
}

func plainMapForExternalToolFile(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			out[key] = value
		}
		return out
	default:
		return nil
	}
}

func annotateExternalToolFileResult(result map[string]any, artifactIDs []string) {
	artifactIDs = dedupeStringSlice(artifactIDs)
	if len(artifactIDs) == 0 {
		return
	}
	result["model_file_input"] = true
	result["native_file_input"] = true
	result["attachment_artifact_ids"] = append([]string(nil), artifactIDs...)
	if len(artifactIDs) == 1 {
		result["attachment_artifact_id"] = artifactIDs[0]
		result["attachmentArtifactId"] = artifactIDs[0]
	}
}

func annotateExternalToolFileResultMetadata(out *tools.Output, pending []pendingExternalToolFileArtifact, artifactIDs []string) {
	if out == nil {
		return
	}
	artifactIDs = dedupeStringSlice(artifactIDs)
	if len(artifactIDs) == 0 {
		return
	}
	metadata := cloneStringAnyMap(out.ResultMetadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["model_file_input"] = true
	metadata["native_file_input"] = true
	metadata["external_tool_file"] = true
	metadata["externalToolFile"] = true
	metadata["attachment_artifact_ids"] = append([]string(nil), artifactIDs...)
	if len(artifactIDs) == 1 {
		metadata["attachment_artifact_id"] = artifactIDs[0]
		metadata["attachmentArtifactId"] = artifactIDs[0]
		metadata["file_artifact_id"] = artifactIDs[0]
	}
	if len(pending) == 1 {
		artifact := pending[0].Artifact
		metadata["file_name"] = artifact.Title
		metadata["fileName"] = artifact.Title
		metadata["source"] = externalToolFileArtifactSource
		for _, key := range []string{"mimeType", "mime_type", "detectedMimeType", "detected_mime_type", "normalizedMimeType", "normalized_mime_type", "fileKind", "file_kind"} {
			if value := firstStringMapValue(artifact.Metadata, key); value != "" {
				metadata[key] = value
			}
		}
		for _, key := range []string{"source_url", "remote_url", "remote_path", "remote_id", "remote_ref"} {
			if value := firstStringMapValue(artifact.Metadata, key); value != "" {
				metadata[key] = value
			}
		}
	}
	out.ResultMetadata = metadata
}

func errorsIsConflict(err error) bool {
	return errors.Is(err, store.ErrConflict)
}
