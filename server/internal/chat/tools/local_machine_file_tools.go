package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/filepipeline"
	"ai-analyst-local/internal/chat/pdfextract"
	chatstore "ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/localexec"
)

const (
	defaultLocalFolderListEntries = 100
	maxLocalFolderListEntries     = 1000
	defaultLocalPDFPageDPI        = 220
	maxLocalPDFPageDPI            = 320
	defaultLocalPDFPageChars      = 12000
	maxLocalPDFPageChars          = 50000
	defaultLocalFileSearchTimeout = 20 * time.Second
	maxLocalFileSearchVisited     = 50000
	localOpenFileMaxBytes         = 128 << 20
	localOpenFileExtractionBytes  = 32 << 10
	localOpenFileExtractionTime   = 8 * time.Second
)

func newFileSearchTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "file_search",
				Description: "Search file names and text under already-configured read-only Local Computer access without copying files into the app. Local access must be configured in Settings > Connections > Local Computer before this tool can search a location.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "File-name or text query to search for.",
						},
						"location_hint": map[string]any{
							"type":        "string",
							"description": "Optional natural-language hint for where to search, such as Downloads or a work folder. Use this when the user describes a likely location; the app maps it to configured Local Computer access when available.",
						},
						"file_types": map[string]any{
							"type":        "array",
							"description": "Optional file extensions or broad types to prefer, such as pdf, pptx, spreadsheet, image, or code.",
							"items": map[string]any{
								"type": "string",
							},
						},
						"search_mode": map[string]any{
							"type":        "string",
							"enum":        []string{"auto", "name", "content"},
							"description": "Search filenames, file contents, or both. Defaults to auto.",
						},
						"max_results": map[string]any{
							"type":        "number",
							"description": "Maximum total matches to return (default 20, max 200).",
						},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				LocalRoot     string   `json:"local_root"`
				Path          string   `json:"path,omitempty"`
				Query         string   `json:"query"`
				LocationHint  string   `json:"location_hint,omitempty"`
				FileTypes     []string `json:"file_types,omitempty"`
				MaxResults    int      `json:"max_results,omitempty"`
				CaseSensitive bool     `json:"case_sensitive,omitempty"`
				Limit         int      `json:"limit,omitempty"`
				MaxFileBytes  int64    `json:"max_file_bytes,omitempty"`
				GrantID       string   `json:"grant_id,omitempty"`
				LocalKind     string   `json:"local_kind,omitempty"`
				PortableOnly  bool     `json:"portable_search,omitempty"`
				SearchMode    string   `json:"search_mode,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			query := normalizeLocalFileSearchQuery(in.Query, in.FileTypes)
			if query == "" {
				return JSONError("query is required"), nil
			}
			root, base, relPrefix, singleFile, err := resolveLocalMachineSearchScope(in.LocalRoot, in.Path, in.LocalKind)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			limitInput := in.Limit
			if limitInput <= 0 {
				limitInput = in.MaxResults
			}
			limit := clampPositiveInt(limitInput, defaultSearchMatches, maxSearchMatches)
			maxFileBytes := clampPositiveInt64(in.MaxFileBytes, int64(defaultReadFileBytes), int64(maxReadFileBytes))
			fileTypes := normalizeLocalMachineFileTypes(in.FileTypes)
			searchMode := normalizeLocalFileSearchMode(in.SearchMode)
			if !in.PortableOnly {
				if out, ok := commandBackedFileSearch(
					ctx,
					root,
					base,
					relPrefix,
					singleFile,
					strings.TrimSpace(in.GrantID),
					query,
					fileTypes,
					in.CaseSensitive,
					limit,
					maxFileBytes,
					searchMode,
				); ok {
					return out, nil
				}
			}
			normalize := func(value string) string {
				if in.CaseSensitive {
					return value
				}
				return strings.ToLower(value)
			}
			needle := normalize(query)
			nameMatches := []string{}
			contentMatches := map[string][]string{}
			matchCount := 0
			visitedFiles := 0
			truncated := false
			timedOut := false
			searchCtx := ctx
			var cancel context.CancelFunc
			if searchCtx == nil {
				searchCtx = context.Background()
			}
			searchCtx, cancel = context.WithTimeout(searchCtx, defaultLocalFileSearchTimeout)
			defer cancel()
			addMatch := func() bool {
				if matchCount >= limit {
					truncated = true
					return false
				}
				matchCount++
				return true
			}
			visitFile := func(p string, rel string, d fs.DirEntry) error {
				select {
				case <-searchCtx.Done():
					if ctx != nil && ctx.Err() != nil {
						return ctx.Err()
					}
					timedOut = true
					truncated = true
					return fs.SkipAll
				default:
				}
				if d.Type()&os.ModeSymlink != 0 || rel == "" {
					return nil
				}
				info, err := d.Info()
				if err != nil || info == nil || !info.Mode().IsRegular() {
					return nil
				}
				visitedFiles++
				if visitedFiles > maxLocalFileSearchVisited {
					truncated = true
					return fs.SkipAll
				}
				if len(fileTypes) > 0 && !localMachineFileTypeAllowed(rel, fileTypes) {
					return nil
				}
				if searchMode != "content" && localSearchNameMatches(rel, query, false, in.CaseSensitive) {
					if !addMatch() {
						return fs.SkipAll
					}
					nameMatches = append(nameMatches, rel)
				}
				if searchMode == "name" || maxFileBytes > 0 && info.Size() > maxFileBytes {
					return nil
				}
				b, err := os.ReadFile(p)
				if err != nil || len(b) == 0 || localMachineBytesHaveNUL(b, 8192) {
					return nil
				}
				for i, line := range strings.Split(string(b), "\n") {
					if strings.Contains(normalize(line), needle) {
						if !addMatch() {
							return fs.SkipAll
						}
						contentMatches[rel] = append(contentMatches[rel], fmt.Sprintf("Line %d: %s", i+1, strings.TrimSpace(line)))
					}
				}
				return nil
			}
			var walkErr error
			if singleFile {
				info, err := os.Stat(base)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				walkErr = visitFile(base, filepath.Base(base), fs.FileInfoToDirEntry(info))
			} else {
				walkedBases := map[string]bool{}
				for _, walkBase := range localMachineSearchBases(base) {
					currentBase := filepath.Clean(walkBase)
					walkErr = filepath.WalkDir(currentBase, func(p string, d fs.DirEntry, walkErr error) error {
						if walkErr != nil {
							return nil
						}
						select {
						case <-searchCtx.Done():
							if ctx != nil && ctx.Err() != nil {
								return ctx.Err()
							}
							timedOut = true
							truncated = true
							return fs.SkipAll
						default:
						}
						rel, ok := localMachineRel(root, p)
						if !ok {
							return nil
						}
						if d.IsDir() {
							cleanDir := filepath.Clean(p)
							if cleanDir != currentBase && walkedBases[cleanDir] {
								return fs.SkipDir
							}
							if d.Name() == ".git" {
								return fs.SkipDir
							}
							if rel != "" && chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
								return fs.SkipDir
							}
							return nil
						}
						if chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
							return nil
						}
						return visitFile(p, rel, d)
					})
					walkedBases[currentBase] = true
					if walkErr != nil || truncated || timedOut || matchCount >= limit {
						break
					}
					if localMachinePathIsFilesystemRoot(base) && matchCount > 0 {
						break
					}
				}
			}
			if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
				if errors.Is(walkErr, context.DeadlineExceeded) {
					// A bounded search deadline is a partial observation, not a tool
					// transport failure. Preserve any matches already collected and let
					// the caller narrow/retry instead of exposing the raw context error.
					timedOut = true
					truncated = true
				} else {
					return JSONError(walkErr.Error()), nil
				}
			}
			sort.Strings(nameMatches)
			files := localMachineSearchFileEntries(strings.TrimSpace(in.GrantID), relPrefix, nameMatches, contentMatches)
			out := map[string]any{
				"local_root":      root,
				"local_kind":      localMachineGrantKindForOutput(singleFile),
				"path":            relPrefix,
				"query":           query,
				"name_matches":    nameMatches,
				"content_matches": contentMatches,
				"files":           files,
				"matches_count":   matchCount,
				"limit":           limit,
				"truncated":       truncated,
				"timed_out":       timedOut,
				"files_visited":   visitedFiles,
				"max_file_bytes":  maxFileBytes,
			}
			if strings.TrimSpace(in.GrantID) != "" {
				out["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			extra := map[string]any{
				"local_root":    root,
				"local_kind":    localMachineGrantKindForOutput(singleFile),
				"path":          relPrefix,
				"query":         query,
				"matches":       matchCount,
				"truncated":     truncated,
				"timed_out":     timedOut,
				"files_visited": visitedFiles,
				"max_bytes":     maxFileBytes,
				"file_count":    len(nameMatches) + len(contentMatches),
				"files_count":   len(files),
			}
			if strings.TrimSpace(in.GrantID) != "" {
				extra["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			return localFilesOutput("file_search", "file_search", out, extra), nil
		},
	}
}

func newLocalListFolderTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "local_list_folder",
				Description: "List files and subfolders in a configured Local Computer folder on this device without copying files into the app. Use this when the user asks what is in a folder, wants a folder inventory, or wants to browse local files.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Optional folder path to list. Use an absolute path when the user names one under configured Local Computer access; otherwise use a relative subfolder under the configured local folder. Defaults to the configured folder root.",
						},
						"recursive": map[string]any{
							"type":        "boolean",
							"description": "Whether to include nested files and folders. Defaults to false.",
						},
						"max_entries": map[string]any{
							"type":        "number",
							"description": "Maximum entries to return (default 100, max 1000).",
						},
					},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				LocalRoot  string `json:"local_root"`
				GrantID    string `json:"grant_id,omitempty"`
				LocalKind  string `json:"local_kind,omitempty"`
				Path       string `json:"path,omitempty"`
				Recursive  bool   `json:"recursive,omitempty"`
				MaxEntries int    `json:"max_entries,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			root, base, relPrefix, err := resolveLocalMachineFolder(in.LocalRoot, in.Path, in.LocalKind)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			limit := clampPositiveInt(in.MaxEntries, defaultLocalFolderListEntries, maxLocalFolderListEntries)
			entries, files, folders, truncated, err := localMachineListFolderEntries(ctx, root, base, strings.TrimSpace(in.GrantID), in.Recursive, limit)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			out := map[string]any{
				"local_root":    root,
				"local_kind":    "folder",
				"path":          relPrefix,
				"recursive":     in.Recursive,
				"entries":       entries,
				"files":         files,
				"folders":       folders,
				"entries_count": len(entries),
				"files_count":   len(files),
				"folders_count": len(folders),
				"limit":         limit,
				"truncated":     truncated,
			}
			if strings.TrimSpace(in.GrantID) != "" {
				out["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			extra := map[string]any{
				"local_root":    root,
				"local_kind":    "folder",
				"path":          relPrefix,
				"recursive":     in.Recursive,
				"entries_count": len(entries),
				"files_count":   len(files),
				"folders_count": len(folders),
				"truncated":     truncated,
				"max_entries":   limit,
			}
			if strings.TrimSpace(in.GrantID) != "" {
				extra["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			return localFilesOutput("local_list_folder", "folder_list", out, extra), nil
		},
	}
}

func newLocalReadFileTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "local_read_file",
				Description: "Read a bounded text chunk from a single local file under configured Local Computer access without copying it into the app. Use this for exact text snippets or pagination. When the user asks to summarize, view, render, preview, or work with a whole local document/PDF/spreadsheet/presentation/image, use local_open_file first so the file is promoted to the native file pipeline.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "File path to read. Use an absolute path when the user names one under configured Local Computer access; otherwise use a relative file path under the configured local folder.",
						},
						"max_bytes": map[string]any{
							"type":        "number",
							"description": "Maximum bytes to return for this chunk (default 256 KiB, max 256 KiB).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from a previous page. Include it to fetch the next chunk.",
						},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				LocalRoot string `json:"local_root"`
				GrantID   string `json:"grant_id,omitempty"`
				LocalKind string `json:"local_kind,omitempty"`
				Path      string `json:"path"`
				MaxBytes  int    `json:"max_bytes,omitempty"`
				PageToken string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			root, fullPath, relPath, singleFileGrant, err := resolveLocalMachineFile(in.LocalRoot, in.Path, in.LocalKind)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			maxBytes := clampPositiveInt(in.MaxBytes, maxReadFileBytes, maxReadFileBytes)
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset < 0 {
				return JSONError("Invalid page_token"), nil
			}

			if extracted, handled, err := localMachineReadExtractedFile(ctx, root, fullPath, relPath, singleFileGrant, strings.TrimSpace(in.GrantID), maxBytes, offset); handled {
				return extracted, err
			}

			f, err := os.Open(fullPath)
			if err != nil {
				return JSONError(fmt.Sprintf("File not found: %s", relPath)), nil
			}
			defer f.Close()

			if offset > 0 {
				if _, err := f.Seek(offset, io.SeekStart); err != nil {
					return JSONError("Invalid page_token"), nil
				}
			}
			limit := int64(maxBytes)
			b, err := io.ReadAll(io.LimitReader(f, limit+1))
			if err != nil {
				return JSONError(fmt.Sprintf("Error reading file: %v", err)), nil
			}
			hasMore := len(b) > maxBytes
			if hasMore {
				b = b[:maxBytes]
			}
			isBinary := localMachineBytesHaveNUL(b, 8192)
			content := ""
			if !isBinary {
				content = string(b)
			}
			out := map[string]any{
				"local_root":      root,
				"local_kind":      localMachineGrantKindForOutput(singleFileGrant),
				"path":            relPath,
				"local_path":      fullPath,
				"file_ref":        relPath,
				"display_path":    relPath,
				"mime_type":       localMachineMimeType(relPath),
				"file_kind":       localMachineFileKind(relPath),
				"offset":          offset,
				"max_bytes":       maxBytes,
				"bytes_returned":  len(b),
				"has_more":        hasMore,
				"binary":          isBinary,
				"content":         content,
				"content_omitted": isBinary,
			}
			if hasMore {
				nextOffset := offset + int64(maxBytes)
				if nextOffset < 0 || nextOffset < offset {
					return JSONError("page_token out of range"), nil
				}
				out["next_page_token"] = encodePageOffsetToken(nextOffset)
			}
			if strings.TrimSpace(in.GrantID) != "" {
				out["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			extra := map[string]any{
				"local_root":      root,
				"local_kind":      localMachineGrantKindForOutput(singleFileGrant),
				"path":            relPath,
				"file_ref":        relPath,
				"display_path":    relPath,
				"mime_type":       localMachineMimeType(relPath),
				"file_kind":       localMachineFileKind(relPath),
				"local_path":      fullPath,
				"offset":          offset,
				"bytes_returned":  len(b),
				"has_more":        hasMore,
				"binary":          isBinary,
				"content_omitted": isBinary,
			}
			if strings.TrimSpace(in.GrantID) != "" {
				extra["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			return localFilesOutput("local_read_file", "file", out, extra), nil
		},
	}
}

func newLocalOpenFileTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        LocalOpenFileToolName,
				Description: "Open a single file under configured Local Computer access in the chat's native file viewer and provider-native file input pipeline by snapshotting only that file into app-managed thread storage. Use this before summarizing, viewing, open-file inspection, rendering, previewing, downloading, or otherwise working with a whole local document/PDF/spreadsheet/presentation/image that was found or named.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "File path to open. Use an absolute path under configured Local Computer access, or a relative file path under the configured local folder.",
						},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				LocalRoot string `json:"local_root"`
				GrantID   string `json:"grant_id,omitempty"`
				LocalKind string `json:"local_kind,omitempty"`
				Path      string `json:"path"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			if exec.Store == nil {
				return JSONError("store is not configured"), nil
			}
			threadID := strings.TrimSpace(exec.ThreadID)
			if threadID == "" {
				return JSONError("thread id is required"), nil
			}
			cfgDir := strings.TrimSpace(exec.CfgDir)
			if cfgDir == "" {
				return JSONError("config directory is not configured"), nil
			}

			root, fullPath, relPath, singleFileGrant, err := resolveLocalMachineFile(in.LocalRoot, in.Path, in.LocalKind)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			info, err := os.Stat(fullPath)
			if err != nil {
				return JSONError(fmt.Sprintf("File not found: %s", relPath)), nil
			}
			if info.IsDir() {
				return JSONError("path points to a directory, not a file"), nil
			}
			if info.Size() > localOpenFileMaxBytes {
				return JSONError(fmt.Sprintf("file exceeds native viewer snapshot limit of %d bytes", localOpenFileMaxBytes)), nil
			}

			artifact, result, resultMetadata, err := snapshotLocalOpenFileArtifact(ctx, exec, root, fullPath, relPath, singleFileGrant, strings.TrimSpace(in.GrantID))
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if _, err := exec.Store.InsertArtifact(ctx, artifact); err != nil {
				if errors.Is(err, chatstore.ErrConflict) {
					return JSONError("file snapshot artifact already exists; try again"), nil
				}
				return JSONError("error saving file snapshot: " + err.Error()), nil
			}
			return localFilesOutput(LocalOpenFileToolName, "file", result, resultMetadata), nil
		},
	}
}

func snapshotLocalOpenFileArtifact(ctx context.Context, exec ExecContext, root string, fullPath string, relPath string, singleFileGrant bool, grantID string) (chatstore.Artifact, map[string]any, map[string]any, error) {
	fileName, err := fileingest.SafeFileName(filepath.Base(relPath))
	if err != nil {
		return chatstore.Artifact{}, nil, nil, err
	}
	artifactID := types.NewUUID()
	relStoragePath := filepath.ToSlash(filepath.Join("local_snapshots", strings.TrimSpace(exec.ThreadID), artifactID, fileName))
	targetPath := filepath.Join(strings.TrimSpace(exec.CfgDir), filepath.FromSlash(relStoragePath))
	source, err := os.Open(fullPath)
	if err != nil {
		return chatstore.Artifact{}, nil, nil, err
	}
	defer source.Close()

	ingested, err := fileingest.Ingest(fileingest.IngestRequest{
		Scope:        fileingest.ScopeThread,
		Source:       fileingest.SourceLocalDir,
		FileName:     fileName,
		DeclaredMIME: localMachineMimeType(relPath),
		Reader:       source,
		TargetPath:   targetPath,
		MaxBytes:     localOpenFileMaxBytes,
		PreviewBytes: fileingest.DefaultPreviewBytes,
	})
	if err != nil {
		return chatstore.Artifact{}, nil, nil, err
	}

	evaluation := ingested.Evaluation
	mimeType := firstNonEmptyString(evaluation.NormalizedMIME, localMachineMimeType(relPath), "application/octet-stream")
	fileKind := localMachineFileKindForEvaluation(evaluation, relPath)
	metadata := evaluation.Metadata(fileingest.SourceLocalDir)
	metadata["source"] = "external_tool"
	metadata["local_source"] = "local_file_snapshot"
	metadata["externalToolFile"] = true
	metadata["external_tool_file"] = true
	metadata["model_file_input"] = true
	metadata["native_file_input"] = true
	metadata["sourceToolName"] = LocalOpenFileToolName
	metadata["source_tool_name"] = LocalOpenFileToolName
	metadata["localRoot"] = root
	metadata["local_root"] = root
	metadata["localKind"] = localMachineGrantKindForOutput(singleFileGrant)
	metadata["local_kind"] = localMachineGrantKindForOutput(singleFileGrant)
	metadata["path"] = relPath
	metadata["localPath"] = fullPath
	metadata["local_path"] = fullPath
	metadata["attachment_artifact_id"] = artifactID
	metadata["attachmentArtifactId"] = artifactID
	metadata["file_artifact_id"] = artifactID
	if grantID != "" {
		metadata["grant_id"] = grantID
	}
	if meta, ok := ToolMetadataByName(LocalOpenFileToolName); ok {
		metadata["tool_presentation"] = meta.Presentation
	}

	extractionRelPath := filepath.ToSlash(filepath.Join("local_snapshots", strings.TrimSpace(exec.ThreadID), artifactID+".extract"))
	if extraction := filepipeline.ExtractFile(ctx, filepipeline.ExtractionRequest{
		SourcePath:             targetPath,
		OutputDir:              filepath.Join(strings.TrimSpace(exec.CfgDir), filepath.FromSlash(extractionRelPath)),
		OutputRelPath:          extractionRelPath,
		FileName:               fileName,
		Evaluation:             evaluation,
		IncludeRawXML:          true,
		EnableExternalTools:    false,
		EnableRendering:        true,
		EnableOCR:              true,
		ExternalCommandTimeout: localOpenFileExtractionTime,
	}); extraction != nil {
		metadata["extraction"] = extraction
		hydrateLocalOpenFilePreview(metadata, strings.TrimSpace(exec.CfgDir), extraction)
	}

	resultMetadata := map[string]any{
		"artifact_id":            artifactID,
		"attachment_artifact_id": artifactID,
		"attachmentArtifactId":   artifactID,
		"file_artifact_id":       artifactID,
		"file_name":              fileName,
		"fileName":               fileName,
		"mime_type":              mimeType,
		"mimeType":               mimeType,
		"file_kind":              fileKind,
		"fileKind":               fileKind,
		"size_bytes":             ingested.ByteSize,
		"sizeBytes":              ingested.ByteSize,
		"sha256":                 ingested.SHA256Hex,
		"native_file_input":      true,
		"model_file_input":       true,
		"external_tool_file":     true,
		"source":                 "external_tool",
		"local_source":           "local_file_snapshot",
		"local_root":             root,
		"local_kind":             localMachineGrantKindForOutput(singleFileGrant),
		"path":                   relPath,
		"local_path":             fullPath,
		"managed_local_path":     relStoragePath,
	}
	if grantID != "" {
		resultMetadata["grant_id"] = grantID
	}
	metadata["result_metadata"] = cloneOutputMetadataMap(resultMetadata)

	okVal := true
	toolName := LocalOpenFileToolName
	artifact := chatstore.Artifact{
		ID:          artifactID,
		ThreadID:    strings.TrimSpace(exec.ThreadID),
		Type:        "file",
		Title:       fileName,
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     "file",
		ContentPath: &relStoragePath,
		ByteSize:    ingested.ByteSize,
		Metadata:    metadata,
	}
	if evaluation.Language != "" {
		artifact.Language = &evaluation.Language
	}

	result := map[string]any{
		"ok":                     true,
		"artifact_id":            artifactID,
		"attachment_artifact_id": artifactID,
		"attachmentArtifactId":   artifactID,
		"file_artifact_id":       artifactID,
		"file_name":              fileName,
		"fileName":               fileName,
		"local_root":             root,
		"local_kind":             localMachineGrantKindForOutput(singleFileGrant),
		"path":                   relPath,
		"local_path":             fullPath,
		"managed_local_path":     relStoragePath,
		"file_ref":               relPath,
		"display_path":           relPath,
		"mime_type":              mimeType,
		"mimeType":               mimeType,
		"file_kind":              fileKind,
		"fileKind":               fileKind,
		"size_bytes":             ingested.ByteSize,
		"sizeBytes":              ingested.ByteSize,
		"sha256":                 ingested.SHA256Hex,
		"native_file_input":      true,
		"model_file_input":       true,
	}
	if grantID != "" {
		result["grant_id"] = grantID
	}
	return artifact, result, resultMetadata, nil
}

func hydrateLocalOpenFilePreview(metadata map[string]any, cfgDir string, extraction map[string]any) {
	if metadata == nil || strings.TrimSpace(firstStringMapValue(metadata, "previewText", "preview_text")) != "" {
		return
	}
	textRel := strings.TrimSpace(firstStringMapValue(extraction, "textPath", "text_path", "markdownPath", "markdown_path"))
	if textRel == "" {
		return
	}
	clean, err := cleanRelPath(textRel)
	if err != nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(cfgDir, filepath.FromSlash(clean)))
	if err != nil || len(raw) == 0 {
		return
	}
	truncated := false
	if len(raw) > localOpenFileExtractionBytes {
		raw = raw[:localOpenFileExtractionBytes]
		truncated = true
	}
	preview := strings.TrimSpace(string(bytes.ToValidUTF8(raw, []byte("?"))))
	if preview == "" {
		return
	}
	metadata["previewText"] = preview
	metadata["previewAvailable"] = true
	metadata["previewTruncated"] = truncated || boolValue(metadata["previewTruncated"])
	delete(metadata, "contentNote")
	if caps, ok := metadata["capabilities"].(map[string]bool); ok {
		caps["canPreviewText"] = true
	}
}

func boolValue(raw any) bool {
	value, ok := raw.(bool)
	return ok && value
}

func newLocalReadPDFPageTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "local_read_pdf_page",
				Description: "Read one page from a configured Local Computer PDF by rendering only that page and OCRing it when ordinary PDF text extraction is unavailable. For scanned PDFs or exact page-number questions, use this instead of whole-document PDF extraction. The page_number argument is 1-based and the result returns bounded OCR text plus page_count and method metadata.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "PDF file path to read. Use an absolute path under configured Local Computer access, or a relative file path under the configured local folder.",
						},
						"page_number": map[string]any{
							"type":        "number",
							"description": "1-based page number in the PDF file to render and OCR.",
							"minimum":     1,
						},
						"dpi": map[string]any{
							"type":        "number",
							"description": "Render resolution for OCR. Defaults to 220 and is capped at 320.",
						},
						"max_chars": map[string]any{
							"type":        "number",
							"description": "Maximum OCR characters to return. Defaults to 12000 and is capped at 50000.",
						},
					},
					"required":             []string{"path", "page_number"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, execCtx ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				LocalRoot  string `json:"local_root"`
				GrantID    string `json:"grant_id,omitempty"`
				LocalKind  string `json:"local_kind,omitempty"`
				Path       string `json:"path"`
				PageNumber int    `json:"page_number"`
				DPI        int    `json:"dpi,omitempty"`
				MaxChars   int    `json:"max_chars,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			if strings.TrimSpace(in.Path) == "" {
				return JSONError("path is required"), nil
			}
			if in.PageNumber <= 0 {
				return JSONError("page_number must be a positive 1-based page number"), nil
			}
			root, fullPath, relPath, singleFileGrant, err := resolveLocalMachineFile(in.LocalRoot, in.Path, in.LocalKind)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if localMachineFileKind(relPath) != "pdf" && !strings.EqualFold(localMachineMimeType(relPath), "application/pdf") {
				return JSONError("local_read_pdf_page only supports PDF files"), nil
			}

			pageCount, pageCountMethod, pageCountErr := localPDFPageCount(ctx, fullPath)
			if pageCount > 0 && in.PageNumber > pageCount {
				return JSONError(fmt.Sprintf("page_number %d is outside this PDF's %d pages", in.PageNumber, pageCount)), nil
			}
			dpi := clampPositiveInt(in.DPI, defaultLocalPDFPageDPI, maxLocalPDFPageDPI)
			maxChars := clampPositiveInt(in.MaxChars, defaultLocalPDFPageChars, maxLocalPDFPageChars)
			text, method, warnings, err := localExtractPDFPageText(ctx, fullPath, in.PageNumber)
			if err != nil {
				textErr := err
				pageText, pageTextErr := pdfextract.ExtractPageText(fullPath, in.PageNumber)
				if pageTextErr == nil {
					text = pageText.Text
					method = pageText.Method
					if method == "" {
						method = "pypdf"
					}
					warnings = append(warnings, pageText.Warnings...)
					if pageText.Encrypted {
						warnings = append(warnings, "PDF declares encryption; embedded text extraction may be incomplete.")
					}
					if pageText.PageCount > 0 {
						pageCount = pageText.PageCount
						pageCountMethod = "pypdf"
					}
				} else {
					ocrText, ocrMethod, ocrWarnings, ocrErr := localOCRPDFPage(ctx, fullPath, in.PageNumber, dpi)
					if ocrErr != nil {
						return JSONError("PDF page text extraction failed: " + textErr.Error() + "; pypdf fallback failed: " + pageTextErr.Error() + "; OCR fallback failed: " + ocrErr.Error()), nil
					}
					text = ocrText
					method = ocrMethod
					warnings = append(warnings, ocrWarnings...)
					warnings = append(warnings, "Embedded PDF page text extraction failed ("+textErr.Error()+"; pypdf fallback failed: "+pageTextErr.Error()+"); returned OCR text instead.")
				}
			}
			text = strings.TrimSpace(text)
			truncated := false
			if len([]rune(text)) > maxChars {
				runes := []rune(text)
				text = string(runes[:maxChars])
				truncated = true
			}
			if pageCountErr != nil {
				warnings = append(warnings, pageCountErr.Error())
			}
			out := map[string]any{
				"local_root":     root,
				"local_kind":     localMachineGrantKindForOutput(singleFileGrant),
				"path":           relPath,
				"local_path":     fullPath,
				"file_ref":       relPath,
				"display_path":   relPath,
				"mime_type":      "application/pdf",
				"file_kind":      "pdf",
				"page_number":    in.PageNumber,
				"page_count":     pageCount,
				"page_count_via": pageCountMethod,
				"dpi":            dpi,
				"method":         method,
				"text":           text,
				"content":        text,
				"text_chars":     len([]rune(text)),
				"truncated":      truncated,
				"warnings":       warnings,
			}
			if strings.TrimSpace(in.GrantID) != "" {
				out["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			extra := map[string]any{
				"local_root":     root,
				"local_kind":     localMachineGrantKindForOutput(singleFileGrant),
				"path":           relPath,
				"file_ref":       relPath,
				"display_path":   relPath,
				"mime_type":      "application/pdf",
				"file_kind":      "pdf",
				"local_path":     fullPath,
				"page_number":    in.PageNumber,
				"page_count":     pageCount,
				"page_count_via": pageCountMethod,
				"dpi":            dpi,
				"method":         method,
				"text_chars":     len([]rune(text)),
				"truncated":      truncated,
				"warnings":       warnings,
			}
			if strings.TrimSpace(in.GrantID) != "" {
				extra["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			return localFilesOutput("local_read_pdf_page", "file", out, extra), nil
		},
	}
}

func localPDFPageCount(ctx context.Context, pdfPath string) (int, string, error) {
	pdfinfo, ok := localToolExecutable("pdfinfo")
	if !ok {
		return 0, "unavailable", fmt.Errorf("pdfinfo is not available; page count was not checked")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, pdfinfo, pdfPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return 0, "pdfinfo", fmt.Errorf("pdfinfo failed: %s", detail)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Pages:") {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, "Pages:"))
		count, err := strconv.Atoi(raw)
		if err == nil && count > 0 {
			return count, "pdfinfo", nil
		}
	}
	return 0, "pdfinfo", fmt.Errorf("pdfinfo did not report a page count")
}

func localOCRPDFPage(ctx context.Context, pdfPath string, pageNumber int, dpi int) (string, string, []string, error) {
	pdftoppm, ok := localToolExecutable("pdftoppm")
	if !ok {
		return "", "", nil, fmt.Errorf("pdftoppm is not available; install Poppler or include it in the app runtime")
	}
	tesseract, ok := localToolExecutable("tesseract")
	if !ok {
		return "", "", nil, fmt.Errorf("tesseract is not available; install Tesseract OCR or include it in the app runtime")
	}
	tmpDir, err := os.MkdirTemp("", "ai-analyst-pdf-page-*")
	if err != nil {
		return "", "", nil, err
	}
	defer os.RemoveAll(tmpDir)

	prefix := filepath.Join(tmpDir, "page")
	renderCtx, renderCancel := context.WithTimeout(ctx, 60*time.Second)
	defer renderCancel()
	render := exec.CommandContext(renderCtx, pdftoppm,
		"-f", strconv.Itoa(pageNumber),
		"-l", strconv.Itoa(pageNumber),
		"-r", strconv.Itoa(dpi),
		"-png",
		pdfPath,
		prefix,
	)
	var renderStderr bytes.Buffer
	render.Stderr = &renderStderr
	if err := render.Run(); err != nil {
		detail := strings.TrimSpace(renderStderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", "", nil, fmt.Errorf("pdftoppm failed: %s", detail)
	}
	matches, err := filepath.Glob(prefix + "-*.png")
	if err != nil || len(matches) == 0 {
		return "", "", nil, fmt.Errorf("pdftoppm did not produce a rendered page image")
	}
	sort.Strings(matches)
	imagePath := matches[0]

	ocrCtx, ocrCancel := context.WithTimeout(ctx, 90*time.Second)
	defer ocrCancel()
	ocr := exec.CommandContext(ocrCtx, tesseract, filepath.Base(imagePath), "stdout", "-l", "eng", "--psm", "6")
	ocr.Dir = filepath.Dir(imagePath)
	var ocrStderr bytes.Buffer
	ocr.Stderr = &ocrStderr
	out, err := ocr.Output()
	if err != nil {
		detail := strings.TrimSpace(ocrStderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", "", nil, fmt.Errorf("tesseract failed: %s", detail)
	}
	text := string(bytes.ToValidUTF8(out, []byte("?")))
	warnings := []string(nil)
	if strings.TrimSpace(text) == "" {
		warnings = append(warnings, "OCR returned no text for this rendered PDF page")
	}
	return text, "pdftoppm+tesseract", warnings, nil
}

func localExtractPDFPageText(ctx context.Context, pdfPath string, pageNumber int) (string, string, []string, error) {
	pdftotext, ok := localToolExecutable("pdftotext")
	if !ok {
		return "", "", nil, fmt.Errorf("pdftotext is not available")
	}
	textCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(textCtx, pdftotext,
		"-f", strconv.Itoa(pageNumber),
		"-l", strconv.Itoa(pageNumber),
		"-layout",
		"-enc", "UTF-8",
		pdfPath,
		"-",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return "", "pdftotext", nil, fmt.Errorf("pdftotext failed: %s", detail)
	}
	text := strings.TrimSpace(string(bytes.ToValidUTF8(out, []byte("?"))))
	if text == "" {
		return "", "pdftotext", nil, fmt.Errorf("pdftotext returned no text for this PDF page")
	}
	return text, "pdftotext", nil, nil
}

var localToolExecutable = resolveLocalToolExecutable

func resolveLocalToolExecutable(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if path, ok := localexec.ResolveCommand(name); ok {
		return path, true
	}
	candidates := []string{
		filepath.Join("/opt/homebrew/bin", name),
		filepath.Join("/usr/local/bin", name),
		filepath.Join("/usr/bin", name),
		filepath.Join("/bin", name),
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		runtimeRoot := filepath.Join(home, ".cache", "codex-runtimes", "codex-primary-runtime", "dependencies")
		candidates = append(candidates,
			filepath.Join(runtimeRoot, "bin", name),
			filepath.Join(runtimeRoot, "native", "poppler", "bin", name),
			filepath.Join(runtimeRoot, "native", "poppler", "poppler", "bin", name),
			filepath.Join(runtimeRoot, "native", "tesseract", "bin", name),
			filepath.Join(runtimeRoot, "native", "tesseract", "tesseract", "bin", name),
		)
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info != nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func localMachineReadExtractedFile(ctx context.Context, root string, fullPath string, relPath string, singleFileGrant bool, grantID string, maxBytes int, offset int64) (Output, bool, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return Output{}, true, ctx.Err()
		default:
		}
	}
	evaluation, err := fileingest.EvaluatePath(filepath.Base(relPath), localMachineMimeType(relPath), fullPath, fileingest.DefaultPreviewBytes, true)
	if err != nil {
		return Output{}, false, nil
	}
	if !localMachineShouldUseExtraction(evaluation) {
		return Output{}, false, nil
	}
	outputRoot, err := os.MkdirTemp("", "ai-analyst-local-extract-*")
	if err != nil {
		return JSONError(fmt.Sprintf("Error preparing file extraction: %v", err)), true, nil
	}
	defer os.RemoveAll(outputRoot)

	outputRelPath := "extract"
	extraction := filepipeline.ExtractFile(ctx, filepipeline.ExtractionRequest{
		SourcePath:          fullPath,
		OutputDir:           filepath.Join(outputRoot, outputRelPath),
		OutputRelPath:       outputRelPath,
		FileName:            filepath.Base(relPath),
		Evaluation:          evaluation,
		IncludeRawXML:       true,
		EnableExternalTools: false,
		EnableRendering:     false,
		EnableOCR:           false,
	})
	if extraction == nil {
		return Output{}, false, nil
	}

	content := ""
	hasMore := false
	sourcePath := ""
	var readErr error
	content, hasMore, sourcePath, readErr = localMachineReadExtractionPage(outputRoot, extraction, offset, int64(maxBytes))
	if readErr != nil {
		status := strings.TrimSpace(stringMapValue(extraction, "status"))
		if status == "" {
			status = "unknown"
		}
		extraction["read_status"] = "unreadable"
		extraction["read_error"] = readErr.Error()
		if strings.EqualFold(status, "complete") || strings.EqualFold(status, "partial") {
			extraction["status"] = "partial"
		}
	}

	source := "file_extraction"
	if evaluation.Kind == fileingest.KindPDF {
		source = "pdf_extraction"
	}
	contentOmitted := readErr != nil || content == ""
	mimeType := strings.TrimSpace(evaluation.NormalizedMIME)
	if mimeType == "" {
		mimeType = localMachineMimeType(relPath)
	}
	fileKind := localMachineFileKindForEvaluation(evaluation, relPath)

	out := map[string]any{
		"local_root":      root,
		"local_kind":      localMachineGrantKindForOutput(singleFileGrant),
		"path":            relPath,
		"local_path":      fullPath,
		"file_ref":        relPath,
		"display_path":    relPath,
		"mime_type":       mimeType,
		"file_kind":       fileKind,
		"offset":          offset,
		"max_bytes":       maxBytes,
		"bytes_returned":  len(content),
		"has_more":        hasMore,
		"binary":          false,
		"content":         content,
		"content_omitted": contentOmitted,
		"source":          source,
		"extraction":      extraction,
	}
	if sourcePath != "" {
		out["source_path"] = sourcePath
	}
	if hasMore {
		nextOffset := offset + int64(len(content))
		if nextOffset < 0 || nextOffset < offset {
			return JSONError("page_token out of range"), true, nil
		}
		out["next_page_token"] = encodePageOffsetToken(nextOffset)
	}
	if grantID != "" {
		out["grant_id"] = grantID
	}
	extra := map[string]any{
		"local_root":      root,
		"local_kind":      localMachineGrantKindForOutput(singleFileGrant),
		"path":            relPath,
		"file_ref":        relPath,
		"display_path":    relPath,
		"mime_type":       mimeType,
		"file_kind":       fileKind,
		"local_path":      fullPath,
		"offset":          offset,
		"bytes_returned":  len(content),
		"has_more":        hasMore,
		"binary":          false,
		"content_omitted": contentOmitted,
		"source":          source,
		"extraction":      extraction,
	}
	if sourcePath != "" {
		extra["source_path"] = sourcePath
	}
	if grantID != "" {
		extra["grant_id"] = grantID
	}
	return localFilesOutput("local_read_file", "file", out, extra), true, nil
}

func localMachineShouldUseExtraction(evaluation fileingest.Evaluation) bool {
	switch evaluation.Kind {
	case fileingest.KindPDF,
		fileingest.KindSpreadsheet,
		fileingest.KindPresentation,
		fileingest.KindDocument,
		fileingest.KindCSV,
		fileingest.KindJSON,
		fileingest.KindHTML,
		fileingest.KindArchive,
		fileingest.KindImage:
		return true
	default:
		return false
	}
}

func localMachineReadExtractionPage(cfgDir string, extraction map[string]any, offset int64, maxBytes int64) (string, bool, string, error) {
	if offset < 0 {
		return "", false, "", fmt.Errorf("invalid page offset")
	}
	if maxBytes <= 0 {
		return "", false, "", fmt.Errorf("invalid max bytes")
	}
	rels, err := extractionSearchPaths(cfgDir, extraction)
	if err != nil || len(rels) == 0 {
		return readAttachmentExtractionPage(cfgDir, extraction, offset, maxBytes)
	}
	limit := offset + maxBytes + 1
	if limit < offset {
		return "", false, "", fmt.Errorf("page_token out of range")
	}
	var b strings.Builder
	for _, rel := range rels {
		if int64(b.Len()) >= limit {
			break
		}
		header := "\n\n--- " + rel + " ---\n"
		remaining := limit - int64(b.Len()+len(header))
		if remaining <= 0 {
			break
		}
		content, _, err := readRelFilePage(cfgDir, rel, 0, remaining)
		if err != nil {
			continue
		}
		b.WriteString(header)
		b.WriteString(content)
	}
	combined := b.String()
	if combined == "" {
		return readAttachmentExtractionPage(cfgDir, extraction, offset, maxBytes)
	}
	if offset > int64(len(combined)) {
		offset = int64(len(combined))
	}
	end := offset + maxBytes
	if end < offset {
		return "", false, "", fmt.Errorf("page_token out of range")
	}
	if end > int64(len(combined)) {
		end = int64(len(combined))
	}
	return combined[offset:end], end < int64(len(combined)), strings.Join(rels, ","), nil
}

func localMachineFileKindForEvaluation(evaluation fileingest.Evaluation, relPath string) string {
	switch evaluation.Kind {
	case fileingest.KindPDF, fileingest.KindSpreadsheet, fileingest.KindPresentation, fileingest.KindDocument, fileingest.KindCSV, fileingest.KindText, fileingest.KindJSON, fileingest.KindHTML, fileingest.KindArchive, fileingest.KindImage:
		return string(evaluation.Kind)
	default:
		return localMachineFileKind(relPath)
	}
}

func newLocalListGrantFolderTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "local_list_grant_folder",
				Description: "Internal tool for the local file search agent. List files and subfolders inside one configured Local Computer folder grant.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"grant_id": map[string]any{
							"type":        "string",
							"description": "Configured Local Computer folder grant ID supplied by the application.",
						},
						"relative_path": map[string]any{
							"type":        "string",
							"description": "Optional relative subfolder inside the configured Local Computer grant.",
						},
						"recursive": map[string]any{
							"type":        "boolean",
							"description": "Whether to include nested files and folders. Defaults to false.",
						},
						"max_entries": map[string]any{
							"type":        "number",
							"description": "Maximum entries to return (default 100, max 1000).",
						},
					},
					"required":             []string{"grant_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				GrantID      string `json:"grant_id"`
				LocalRoot    string `json:"local_root"`
				LocalKind    string `json:"local_kind,omitempty"`
				RelativePath string `json:"relative_path,omitempty"`
				Recursive    bool   `json:"recursive,omitempty"`
				MaxEntries   int    `json:"max_entries,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			if strings.TrimSpace(in.GrantID) == "" {
				return JSONError("grant_id is required"), nil
			}
			if strings.TrimSpace(in.LocalRoot) == "" {
				return JSONError("configured Local Computer grant is not available"), nil
			}
			listArgs := map[string]any{
				"local_root":  in.LocalRoot,
				"local_kind":  in.LocalKind,
				"grant_id":    strings.TrimSpace(in.GrantID),
				"recursive":   in.Recursive,
				"max_entries": in.MaxEntries,
			}
			if strings.TrimSpace(in.RelativePath) != "" {
				listArgs["path"] = in.RelativePath
			}
			out, err := newLocalListFolderTool().Execute(ctx, exec, mustJSONForLocalGrantWrapper(listArgs))
			if err != nil || !out.OK {
				return out, err
			}
			if result, ok := out.Result.(map[string]any); ok {
				result["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			if out.ResultMetadata != nil {
				out.ResultMetadata["tool_name"] = "local_list_grant_folder"
				out.ResultMetadata["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			return out, nil
		},
	}
}

func newLocalSearchInGrantTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "local_search_in_grant",
				Description: "Internal tool for the local file search agent. Search within one configured Local Computer file grant.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"grant_id": map[string]any{
							"type":        "string",
							"description": "Configured Local Computer file grant ID supplied by the application.",
						},
						"query": map[string]any{
							"type":        "string",
							"description": "File-name or text query to search for.",
						},
						"relative_path": map[string]any{
							"type":        "string",
							"description": "Optional relative subfolder inside the configured Local Computer grant.",
						},
						"file_types": map[string]any{
							"type":        "array",
							"description": "Optional file extensions or broad file types to prefer.",
							"items": map[string]any{
								"type": "string",
							},
						},
						"search_mode": map[string]any{
							"type":        "string",
							"enum":        []string{"auto", "name", "content"},
							"description": "Internal search strategy. Name mode is used for fast filename preflight.",
						},
						"max_results": map[string]any{
							"type":        "number",
							"description": "Maximum total matches to return (default 20, max 200).",
						},
					},
					"required":             []string{"grant_id", "query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				GrantID      string   `json:"grant_id"`
				LocalRoot    string   `json:"local_root"`
				LocalKind    string   `json:"local_kind,omitempty"`
				Query        string   `json:"query"`
				RelativePath string   `json:"relative_path,omitempty"`
				FileTypes    []string `json:"file_types,omitempty"`
				MaxResults   int      `json:"max_results,omitempty"`
				PortableOnly bool     `json:"portable_search,omitempty"`
				SearchMode   string   `json:"search_mode,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			if strings.TrimSpace(in.GrantID) == "" {
				return JSONError("grant_id is required"), nil
			}
			if strings.TrimSpace(in.LocalRoot) == "" {
				return JSONError("configured Local Computer grant is not available"), nil
			}
			searchArgs := map[string]any{
				"local_root":      in.LocalRoot,
				"local_kind":      in.LocalKind,
				"grant_id":        strings.TrimSpace(in.GrantID),
				"query":           in.Query,
				"max_results":     in.MaxResults,
				"portable_search": in.PortableOnly,
				"search_mode":     normalizeLocalFileSearchMode(in.SearchMode),
			}
			if strings.TrimSpace(in.RelativePath) != "" {
				searchArgs["path"] = in.RelativePath
			}
			if len(in.FileTypes) > 0 {
				searchArgs["file_types"] = in.FileTypes
			}
			out, err := newFileSearchTool().Execute(ctx, exec, mustJSONForLocalGrantWrapper(searchArgs))
			if err != nil || !out.OK {
				return out, err
			}
			if result, ok := out.Result.(map[string]any); ok {
				result["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			if out.ResultMetadata != nil {
				out.ResultMetadata["tool_name"] = "local_search_in_grant"
				out.ResultMetadata["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			return out, nil
		},
	}
}

func newLocalReadGrantFileTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "local_read_grant_file",
				Description: "Internal tool for the local file search agent. Read one file inside one configured Local Computer file grant.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"grant_id": map[string]any{
							"type":        "string",
							"description": "Configured Local Computer file grant ID supplied by the application.",
						},
						"file_ref": map[string]any{
							"type":        "string",
							"description": "Relative file reference returned by local_search_in_grant.",
						},
						"max_bytes": map[string]any{
							"type":        "number",
							"description": "Maximum bytes to return for this chunk (default 256 KiB, max 256 KiB).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from a previous page. Include it to fetch the next chunk.",
						},
					},
					"required":             []string{"grant_id", "file_ref"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				GrantID   string `json:"grant_id"`
				LocalRoot string `json:"local_root"`
				LocalKind string `json:"local_kind,omitempty"`
				FileRef   string `json:"file_ref"`
				MaxBytes  int    `json:"max_bytes,omitempty"`
				PageToken string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			if strings.TrimSpace(in.GrantID) == "" {
				return JSONError("grant_id is required"), nil
			}
			if strings.TrimSpace(in.LocalRoot) == "" {
				return JSONError("configured Local Computer grant is not available"), nil
			}
			readArgs := map[string]any{
				"local_root": in.LocalRoot,
				"local_kind": in.LocalKind,
				"grant_id":   strings.TrimSpace(in.GrantID),
				"path":       in.FileRef,
				"max_bytes":  in.MaxBytes,
				"page_token": in.PageToken,
			}
			out, err := newLocalReadFileTool().Execute(ctx, exec, mustJSONForLocalGrantWrapper(readArgs))
			if err != nil || !out.OK {
				return out, err
			}
			if result, ok := out.Result.(map[string]any); ok {
				result["grant_id"] = strings.TrimSpace(in.GrantID)
				result["file_ref"] = result["path"]
			}
			if out.ResultMetadata != nil {
				out.ResultMetadata["tool_name"] = "local_read_grant_file"
				out.ResultMetadata["grant_id"] = strings.TrimSpace(in.GrantID)
			}
			return out, nil
		},
	}
}

func mustJSONForLocalGrantWrapper(value map[string]any) json.RawMessage {
	b, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

func resolveLocalMachineFolder(localRoot string, relPath string, localKind string) (string, string, string, error) {
	root, err := cleanLocalMachinePath(localRoot)
	if err != nil {
		return "", "", "", err
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", "", "", err
	}
	kind := normalizeLocalMachineGrantKind(localKind)
	if kind == "" {
		if st.IsDir() {
			kind = "folder"
		} else {
			kind = "file"
		}
	}
	if kind == "file" {
		return "", "", "", errors.New("local folder listing requires a folder grant")
	}
	if !st.IsDir() {
		return "", "", "", errors.New("local_root is not a directory")
	}
	rel, err := cleanLocalMachineRel(relPath, true)
	if err != nil {
		return "", "", "", err
	}
	if rel != "" && chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
		return "", "", "", errors.New("path is excluded by local permission rules")
	}
	base := root
	if rel != "" {
		base = filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	}
	if !localMachineIsSubpath(root, base) {
		return "", "", "", errors.New("path escapes local_root")
	}
	if evaluated, err := filepath.EvalSymlinks(base); err == nil {
		base = filepath.Clean(evaluated)
		if !localMachineIsSubpath(root, base) {
			return "", "", "", errors.New("path escapes local_root")
		}
	}
	st, err = os.Stat(base)
	if err != nil {
		return "", "", "", err
	}
	if !st.IsDir() {
		return "", "", "", errors.New("path is not a directory")
	}
	return root, base, rel, nil
}

func resolveLocalMachineSearchScope(localRoot string, relPath string, localKind string) (string, string, string, bool, error) {
	root, err := cleanLocalMachinePath(localRoot)
	if err != nil {
		return "", "", "", false, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", "", "", false, err
	}
	kind := normalizeLocalMachineGrantKind(localKind)
	if kind == "" {
		if st.IsDir() {
			kind = "folder"
		} else {
			kind = "file"
		}
	}
	if kind == "file" {
		if st.IsDir() {
			return "", "", "", false, errors.New("local_root is not a file")
		}
		rel, err := cleanLocalMachineRel(relPath, true)
		if err != nil {
			return "", "", "", false, err
		}
		if rel != "" && !strings.EqualFold(rel, filepath.Base(root)) {
			return "", "", "", false, errors.New("path escapes local file grant")
		}
		return root, root, rel, true, nil
	}
	if !st.IsDir() {
		return "", "", "", false, errors.New("local_root is not a directory")
	}
	rel, err := cleanLocalMachineRel(relPath, true)
	if err != nil {
		return "", "", "", false, err
	}
	if rel != "" && chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
		return "", "", "", false, errors.New("path is excluded by local permission rules")
	}
	base := root
	if rel != "" {
		base = filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	}
	if !localMachineIsSubpath(root, base) {
		return "", "", "", false, errors.New("path escapes local_root")
	}
	if evaluated, err := filepath.EvalSymlinks(base); err == nil {
		base = filepath.Clean(evaluated)
		if !localMachineIsSubpath(root, base) {
			return "", "", "", false, errors.New("path escapes local_root")
		}
	}
	st, err = os.Stat(base)
	if err != nil {
		return "", "", "", false, err
	}
	if !st.IsDir() {
		return "", "", "", false, errors.New("path is not a directory")
	}
	return root, base, rel, false, nil
}

func resolveLocalMachineFile(localRoot string, relPath string, localKind string) (string, string, string, bool, error) {
	root, err := cleanLocalMachinePath(localRoot)
	if err != nil {
		return "", "", "", false, err
	}
	st, err := os.Stat(root)
	if err != nil {
		return "", "", "", false, err
	}
	kind := normalizeLocalMachineGrantKind(localKind)
	if kind == "" {
		if st.IsDir() {
			kind = "folder"
		} else {
			kind = "file"
		}
	}
	if kind == "file" {
		if st.IsDir() {
			return "", "", "", false, errors.New("local_root is not a file")
		}
		rel, err := cleanLocalMachineRel(relPath, true)
		if err != nil {
			return "", "", "", false, err
		}
		baseName := filepath.Base(root)
		if rel != "" && rel != baseName {
			return "", "", "", false, errors.New("path escapes local file grant")
		}
		return root, root, baseName, true, nil
	}
	if !st.IsDir() {
		return "", "", "", false, errors.New("local_root is not a directory")
	}
	rel, err := cleanLocalMachineRel(relPath, false)
	if err != nil {
		return "", "", "", false, err
	}
	if chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
		return "", "", "", false, errors.New("path is excluded by local permission rules")
	}
	fullPath := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	if !localMachineIsSubpath(root, fullPath) {
		return "", "", "", false, errors.New("path escapes local_root")
	}
	if evaluated, err := filepath.EvalSymlinks(fullPath); err == nil {
		fullPath = filepath.Clean(evaluated)
		if !localMachineIsSubpath(root, fullPath) {
			return "", "", "", false, errors.New("path escapes local_root")
		}
	}
	st, err = os.Stat(fullPath)
	if err != nil {
		return "", "", "", false, err
	}
	if st.IsDir() {
		return "", "", "", false, errors.New("path is a directory")
	}
	resolvedRel, ok := localMachineRel(root, fullPath)
	if !ok || resolvedRel == "" {
		return "", "", "", false, errors.New("path escapes local_root")
	}
	if chatstore.LocalPathExcludedByPermissionPatterns(resolvedRel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
		return "", "", "", false, errors.New("path is excluded by local permission rules")
	}
	return root, fullPath, resolvedRel, false, nil
}

func localMachineListFolderEntries(ctx context.Context, root string, base string, grantID string, recursive bool, limit int) ([]map[string]any, []map[string]any, []map[string]any, bool, error) {
	grantID = strings.TrimSpace(grantID)
	limit = clampPositiveInt(limit, defaultLocalFolderListEntries, maxLocalFolderListEntries)
	entries := []map[string]any{}
	files := []map[string]any{}
	folders := []map[string]any{}
	truncated := false
	addEntry := func(p string, d fs.DirEntry) bool {
		rel, ok := localMachineRel(root, p)
		if !ok || rel == "" {
			return true
		}
		if d.Type()&os.ModeSymlink != 0 {
			return true
		}
		if chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
			return true
		}
		if len(entries) >= limit {
			truncated = true
			return false
		}
		entry := localMachineListEntry(p, rel, d, grantID)
		if len(entry) == 0 {
			return true
		}
		entries = append(entries, entry)
		if entry["entry_kind"] == "folder" {
			folders = append(folders, entry)
		} else if entry["entry_kind"] == "file" {
			files = append(files, entry)
		}
		return true
	}

	if !recursive {
		dirEntries, err := os.ReadDir(base)
		if err != nil {
			return nil, nil, nil, false, err
		}
		for _, d := range dirEntries {
			if ctx != nil {
				select {
				case <-ctx.Done():
					return nil, nil, nil, false, ctx.Err()
				default:
				}
			}
			if d.Name() == ".git" && d.IsDir() {
				continue
			}
			p := filepath.Join(base, d.Name())
			if !addEntry(p, d) {
				break
			}
		}
		return entries, files, folders, truncated, nil
	}

	walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		rel, ok := localMachineRel(root, p)
		if !ok {
			return nil
		}
		if d.IsDir() {
			if rel == "" {
				return nil
			}
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			if chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
				return fs.SkipDir
			}
		}
		if !addEntry(p, d) {
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return nil, nil, nil, false, walkErr
	}
	return entries, files, folders, truncated, nil
}

func localMachineListEntry(p string, rel string, d fs.DirEntry, grantID string) map[string]any {
	info, err := d.Info()
	if err != nil {
		return nil
	}
	entryKind := "file"
	if d.IsDir() {
		entryKind = "folder"
	}
	entry := map[string]any{
		"name":         d.Name(),
		"path":         rel,
		"display_path": rel,
		"entry_kind":   entryKind,
		"local_path":   filepath.Clean(p),
	}
	if !info.ModTime().IsZero() {
		entry["modified_at"] = info.ModTime().UTC().Format(time.RFC3339)
	}
	if grantID != "" {
		entry["grant_id"] = grantID
	}
	if entryKind == "folder" {
		entry["folder_ref"] = rel
		return entry
	}
	entry["file_ref"] = rel
	entry["mime_type"] = localMachineMimeType(rel)
	entry["file_kind"] = localMachineFileKind(rel)
	entry["size_bytes"] = info.Size()
	return entry
}

func normalizeLocalMachineGrantKind(kind string) string {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "file":
		return "file"
	case "folder":
		return "folder"
	default:
		return ""
	}
}

func localMachineGrantKindForOutput(singleFile bool) string {
	if singleFile {
		return "file"
	}
	return "folder"
}

func localMachineSearchBases(base string) []string {
	base = filepath.Clean(strings.TrimSpace(base))
	if base == "" || !localMachinePathIsFilesystemRoot(base) {
		return []string{base}
	}
	candidates := []string{}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		home = filepath.Clean(home)
		for _, name := range []string{"Downloads", "Documents", "Desktop"} {
			candidates = append(candidates, filepath.Join(home, name))
		}
		candidates = append(candidates, home)
	}
	if temp := strings.TrimSpace(os.TempDir()); temp != "" {
		candidates = append(candidates, filepath.Clean(temp))
	}
	candidates = append(candidates, base)

	out := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = filepath.Clean(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if candidate != base && !localMachineIsSubpath(base, candidate) {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil || info == nil || !info.IsDir() {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	if len(out) == 0 {
		return []string{base}
	}
	return out
}

func localMachinePathIsFilesystemRoot(path string) bool {
	clean := filepath.Clean(strings.TrimSpace(path))
	return clean != "" && clean != "." && filepath.Dir(clean) == clean
}

func cleanLocalMachinePath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("local path is required")
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			if value == "~" {
				value = home
			} else {
				value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
			}
		}
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		abs = evaluated
	}
	return filepath.Clean(abs), nil
}

func cleanLocalMachineRel(raw string, allowEmpty bool) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("path is required")
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if localMachineRelativePathIsAbsolute(value) {
		return "", errors.New("path must be relative to local_root")
	}
	value = strings.TrimPrefix(value, "./")
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("invalid path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid path traversal")
	}
	if strings.ContainsRune(clean, '\x00') {
		return "", errors.New("invalid path")
	}
	return clean, nil
}

func localMachineRelativePathIsAbsolute(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) {
		return true
	}
	if len(value) >= 2 && value[1] == ':' {
		letter := value[0]
		return (letter >= 'A' && letter <= 'Z') || (letter >= 'a' && letter <= 'z')
	}
	return false
}

func localMachineRel(root string, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." {
		return "", true
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

func localMachineSearchFileEntries(grantID string, _ string, nameMatches []string, contentMatches map[string][]string) []map[string]any {
	grantID = strings.TrimSpace(grantID)
	byPath := map[string]map[string]any{}
	add := func(rel string, matchType string, lineMatches []string) {
		rel = strings.TrimSpace(filepath.ToSlash(rel))
		if rel == "" {
			return
		}
		entry := byPath[rel]
		if entry == nil {
			displayPath := rel
			entry = map[string]any{
				"file_ref":     rel,
				"path":         rel,
				"display_path": displayPath,
				"mime_type":    localMachineMimeType(rel),
				"file_kind":    localMachineFileKind(rel),
				"match_types":  []string{},
			}
			if grantID != "" {
				entry["grant_id"] = grantID
			}
			byPath[rel] = entry
		}
		existing, _ := entry["match_types"].([]string)
		if !localMachineStringSliceContains(existing, matchType) {
			entry["match_types"] = append(existing, matchType)
		}
		if len(lineMatches) > 0 {
			entry["line_matches"] = append([]string(nil), lineMatches...)
		}
	}
	for _, rel := range nameMatches {
		add(rel, "name", nil)
	}
	for rel, matches := range contentMatches {
		add(rel, "content", matches)
	}
	paths := make([]string, 0, len(byPath))
	for rel := range byPath {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	files := make([]map[string]any, 0, len(paths))
	for _, rel := range paths {
		files = append(files, byPath[rel])
	}
	return files
}

func localMachineMimeType(rel string) string {
	ext := strings.ToLower(filepath.Ext(rel))
	if ext == "" {
		return ""
	}
	return strings.TrimSpace(mime.TypeByExtension(ext))
}

func localMachineStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func localMachineFileKind(rel string) string {
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), ".")
	switch ext {
	case "doc", "docx", "odt", "rtf", "txt", "md":
		return "document"
	case "csv", "tsv", "xls", "xlsx", "ods":
		return "spreadsheet"
	case "ppt", "pptx", "odp", "key":
		return "presentation"
	case "jpg", "jpeg", "png", "gif", "webp", "tif", "tiff", "bmp", "heic":
		return "image"
	case "pdf":
		return "pdf"
	case "go", "js", "jsx", "ts", "tsx", "py", "rb", "java", "c", "cc", "cpp", "h", "hpp", "rs", "swift", "kt", "php", "sh", "sql", "json", "yaml", "yml", "toml", "xml", "html", "css", "scss":
		return "code"
	default:
		return ext
	}
}

func localMachineIsSubpath(root string, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func normalizeLocalMachineFileTypes(values []string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, raw := range values {
		value := strings.TrimSpace(strings.ToLower(raw))
		value = strings.TrimPrefix(value, ".")
		if value == "" {
			continue
		}
		for _, ext := range localMachineFileTypeExtensions(value) {
			out[ext] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func localMachineFileTypeExtensions(value string) []string {
	switch value {
	case "doc", "docs", "document", "documents", "word":
		return []string{"doc", "docx", "odt", "rtf", "txt", "md"}
	case "sheet", "sheets", "spreadsheet", "spreadsheets", "excel":
		return []string{"csv", "tsv", "xls", "xlsx", "ods"}
	case "deck", "decks", "presentation", "presentations", "slides", "powerpoint":
		return []string{"ppt", "pptx", "odp", "key"}
	case "image", "images", "photo", "photos":
		return []string{"jpg", "jpeg", "png", "gif", "webp", "tif", "tiff", "bmp", "heic"}
	case "pdf", "pdfs":
		return []string{"pdf"}
	case "code", "source":
		return []string{"go", "js", "jsx", "ts", "tsx", "py", "rb", "java", "c", "cc", "cpp", "h", "hpp", "rs", "swift", "kt", "php", "sh", "sql", "json", "yaml", "yml", "toml", "xml", "html", "css", "scss"}
	default:
		return []string{value}
	}
}

func localMachineFileTypeAllowed(rel string, allowed map[string]struct{}) bool {
	if len(allowed) == 0 {
		return true
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(rel)), ".")
	if ext == "" {
		return false
	}
	_, ok := allowed[ext]
	return ok
}

func localMachineBytesHaveNUL(b []byte, max int) bool {
	if max <= 0 || max > len(b) {
		max = len(b)
	}
	for i := 0; i < max; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}
