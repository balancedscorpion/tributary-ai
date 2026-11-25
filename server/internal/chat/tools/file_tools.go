package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/types"
)

const missingProjectSnapshotMessage = "No imported project snapshot is active. Use Local Computer to reference files in place, clone a GitHub repository as an app-managed source, or import a snapshot copy first."

func requireProject(exec ExecContext) (*projects.Store, string, Output, bool) {
	if exec.Projects == nil {
		return nil, "", JSONError(missingProjectSnapshotMessage), false
	}
	pid := strings.TrimSpace(exec.ProjectID)
	if pid == "" {
		return nil, "", JSONError("No imported project snapshot is selected for this run."), false
	}
	return exec.Projects, pid, Output{}, true
}

func normalizeRelPathForDisplay(p string, allowEmpty bool) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("path is required")
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimPrefix(raw, "/")
	clean := path.Clean(raw)
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("invalid path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid path traversal")
	}
	return clean, nil
}

func newListFilesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_files",
				Description: "List files in the imported project snapshot attached to this thread. Do not use this for Local Computer references or app-managed GitHub clones; use file_search/local_list_folder or git repository tools instead.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "Optional project-snapshot-relative directory path to filter by (e.g. \"src/\"). Defaults to root.",
						},
					},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			ps, pid, errOut, ok := requireProject(exec)
			if !ok {
				return errOut, nil
			}

			var in struct {
				Path string `json:"path,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}

			res, err := ps.ListFiles(ctx, pid, projects.ListOptions{Prefix: in.Path, Limit: 0})
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return JSONError(missingProjectSnapshotMessage), nil
				}
				return JSONError(err.Error()), nil
			}

			out := map[string]any{
				"files":       res.Files,
				"total_count": res.Total,
			}
			if res.Truncated {
				out["note"] = "Output truncated."
			}
			return localFilesOutput("list_files", "file_list", out, map[string]any{
				"path":        strings.TrimSpace(in.Path),
				"total_count": res.Total,
				"truncated":   res.Truncated,
			}), nil
		},
	}
}

func newReadFileTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "read_file",
				Description: "Read a file from the imported project snapshot attached to this thread. Do not use this for Local Computer references or app-managed GitHub clones; use local_read_file or git_show_file_at_ref instead.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"description": "The project-snapshot-relative path of the file to read (e.g. \"src/App.tsx\").",
						},
						"max_bytes": map[string]any{
							"type":        "number",
							"description": "Maximum bytes to return for this chunk (default 256 KiB, max 256 KiB).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from previous page. Include to fetch the next chunk.",
						},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			ps, pid, errOut, ok := requireProject(exec)
			if !ok {
				return errOut, nil
			}

			var in struct {
				Path      string `json:"path"`
				MaxBytes  int    `json:"max_bytes,omitempty"`
				PageToken string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			displayPath, err := normalizeRelPathForDisplay(in.Path, false)
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

			f, err := ps.OpenFile(pid, displayPath)
			if err != nil {
				return JSONError(fmt.Sprintf("File not found: %s", strings.TrimSpace(in.Path))), nil
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

			content := string(b)
			hasMore := len(b) > maxBytes
			if hasMore {
				b = b[:maxBytes]
				content = string(b)
			}
			out := map[string]any{
				"path":           displayPath,
				"offset":         offset,
				"max_bytes":      maxBytes,
				"bytes_returned": len(b),
				"has_more":       hasMore,
				"content":        content,
			}
			if hasMore {
				nextOffset := offset + int64(maxBytes)
				if nextOffset < 0 || nextOffset < offset {
					return JSONError("page_token out of range"), nil
				}
				out["next_page_token"] = encodePageOffsetToken(nextOffset)
			}
			return localFilesOutput("read_file", "file", out, map[string]any{
				"path":           displayPath,
				"offset":         offset,
				"bytes_returned": len(b),
				"has_more":       hasMore,
			}), nil
		},
	}
}

func newSearchFilesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "search_files",
				Description: "Search text in the imported project snapshot attached to this thread. For Local Computer references use file_search; for app-managed GitHub clones use git_grep/search-history tools; use GitHub code search only as a remote broad-discovery fallback.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"query": map[string]any{
							"type":        "string",
							"description": "The string to search for.",
						},
						"case_sensitive": map[string]any{
							"type":        "boolean",
							"description": "Whether the search should be case sensitive. Default false.",
						},
						"limit": map[string]any{
							"type":        "number",
							"description": "Maximum matches to return for this page (default 20, max 200).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from previous page. Include to fetch the next matches page.",
						},
						"max_file_bytes": map[string]any{
							"type":        "number",
							"description": "Per-file read cap while scanning (default 32 KiB, max 256 KiB).",
						},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			ps, pid, errOut, ok := requireProject(exec)
			if !ok {
				return errOut, nil
			}

			var in struct {
				Query         string `json:"query"`
				CaseSensitive bool   `json:"case_sensitive,omitempty"`
				Limit         int    `json:"limit,omitempty"`
				PageToken     string `json:"page_token,omitempty"`
				MaxFileBytes  int64  `json:"max_file_bytes,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			q := strings.TrimSpace(in.Query)
			if q == "" {
				return JSONError("query is required"), nil
			}

			pageSize := clampPositiveInt(in.Limit, defaultSearchMatches, maxSearchMatches)
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset < 0 {
				return JSONError("Invalid page_token"), nil
			}
			maxInt := int64(^uint(0) >> 1)
			maxMatches := int(maxInt)
			if offset > maxInt-int64(pageSize)-1 {
				return JSONError("page_token out of range"), nil
			}
			requestedMatches := offset + int64(pageSize) + 1
			if requestedMatches <= int64(pageSize) {
				requestedMatches = int64(pageSize) + 1
			}
			if requestedMatches > maxInt {
				requestedMatches = maxInt
			}
			maxMatches = int(requestedMatches)
			if maxMatches < pageSize {
				maxMatches = pageSize
			}
			maxFileBytes := clampPositiveInt64(in.MaxFileBytes, int64(defaultReadFileBytes), int64(maxReadFileBytes))

			res, err := ps.SearchFiles(ctx, pid, q, projects.SearchOptions{
				CaseSensitive: in.CaseSensitive,
				MaxMatches:    maxMatches,
				MaxFileBytes:  maxFileBytes,
			})
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return JSONError(missingProjectSnapshotMessage), nil
				}
				return JSONError(err.Error()), nil
			}
			if len(res.Matches) == 0 {
				out := map[string]any{
					"matches":   map[string][]string{},
					"page_size": pageSize,
					"offset":    offset,
					"has_more":  false,
				}
				return localFilesOutput("search_files", "file_search", out, map[string]any{
					"query":       q,
					"matches":     0,
					"page_size":   pageSize,
					"offset":      offset,
					"has_more":    false,
					"empty_state": true,
				}), nil
			}

			fileNames := make([]string, 0, len(res.Matches))
			for file := range res.Matches {
				fileNames = append(fileNames, file)
			}
			sort.Strings(fileNames)

			outMatches := map[string][]string{}
			seen := int64(0)
			collected := 0
			for _, file := range fileNames {
				lines := res.Matches[file]
				for _, line := range lines {
					seen++
					if seen <= offset {
						continue
					}
					if collected >= pageSize {
						continue
					}
					outMatches[file] = append(outMatches[file], line)
					collected++
				}
			}

			out := map[string]any{
				"matches":        outMatches,
				"page_size":      pageSize,
				"offset":         offset,
				"has_more":       false,
				"max_file_bytes": maxFileBytes,
			}
			out["has_more"] = res.Truncated || seen > offset+int64(pageSize)
			if out["has_more"].(bool) {
				nextOffset := offset + int64(collected)
				if nextOffset < 0 || nextOffset < offset {
					return JSONError("page_token out of range"), nil
				}
				out["next_page_token"] = encodePageOffsetToken(nextOffset)
			}
			return localFilesOutput("search_files", "file_search", out, map[string]any{
				"query":      q,
				"matches":    collected,
				"page_size":  pageSize,
				"offset":     offset,
				"has_more":   out["has_more"].(bool),
				"truncated":  res.Truncated,
				"max_bytes":  maxFileBytes,
				"file_count": len(outMatches),
				"total_seen": seen,
			}), nil
		},
	}
}
