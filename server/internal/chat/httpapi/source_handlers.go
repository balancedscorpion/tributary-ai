package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/githubcli"
	"ai-analyst-local/internal/localroots"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/sources"
)

const maxLocalGrantRawFileBytes int64 = 64 << 20

type localGrantFileScope struct {
	GrantID            string
	Root               string
	Kind               string
	Excludes           []string
	FromSettings       bool
	FromPermissionRule bool
}

func (h *Handler) handleListSources(w http.ResponseWriter, r *http.Request) {
	records, err := h.collectSources(r)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"sources": records,
		"scope": map[string]any{
			"thread_id":  strings.TrimSpace(r.URL.Query().Get("thread_id")),
			"project_id": strings.TrimSpace(r.URL.Query().Get("project_id")),
			"kind":       strings.TrimSpace(r.URL.Query().Get("kind")),
			"returned":   len(records),
		},
	})
}

func (h *Handler) handleLocalComputerRoots(w http.ResponseWriter, _ *http.Request) {
	roots := localroots.WholeComputerRoots()
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"os":             runtime.GOOS,
		"path_separator": string(os.PathSeparator),
		"primary_root":   localroots.PrimaryRoot(),
		"roots":          roots,
	})
}

func (h *Handler) handleGetSource(w http.ResponseWriter, r *http.Request) {
	if h.sources == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "source registry is not configured"})
		return
	}
	sourceID := strings.TrimSpace(r.PathValue("sourceId"))
	src, err := h.sources.Get(r.Context(), sourceID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "source": sourceDTO(src)})
}

func (h *Handler) handleSourcePreview(w http.ResponseWriter, r *http.Request) {
	sourceID := strings.TrimSpace(r.PathValue("sourceId"))
	relPath := strings.TrimSpace(r.URL.Query().Get("path"))
	maxBytes := int64(parseInt(r.URL.Query().Get("max_bytes"), 1<<20, 1, 8<<20))
	if strings.HasPrefix(sourceID, "artifact-") {
		h.handleArtifactSourcePreview(w, r, strings.TrimPrefix(sourceID, "artifact-"), maxBytes)
		return
	}
	if strings.HasPrefix(sourceID, "local-") {
		h.handleLocalComputerSourcePreview(w, r, sourceID, relPath, maxBytes)
		return
	}
	if h.sources == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "source registry is not configured"})
		return
	}
	src, err := h.sources.Get(r.Context(), sourceID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	switch src.Kind {
	case sources.KindGitHubClone:
		if h.github == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "GitHub integration is not configured"})
			return
		}
		file, err := h.github.RepositoryFile(r.Context(), sourceID, strings.TrimSpace(r.URL.Query().Get("ref")), relPath, maxBytes)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preview": map[string]any{
			"source_id":    sourceID,
			"kind":         src.Kind,
			"path":         file.Path,
			"content":      file.Content,
			"byte_size":    file.ByteSize,
			"truncated":    file.Truncated,
			"content_type": firstNonEmptyString(file.ContentTypeHint, "text/plain; charset=utf-8"),
		}})
	case sources.KindConnectorFile, sources.KindThreadUpload:
		if src.ArtifactID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "source is not materialized as an artifact"})
			return
		}
		h.handleArtifactSourcePreview(w, r, src.ArtifactID, maxBytes)
	case sources.KindLocalReference:
		h.previewLocalPath(w, r, sourceID, src.LocalPath, relPath, maxBytes)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "source preview is not available for this source kind"})
	}
}

func (h *Handler) handleDeleteSource(w http.ResponseWriter, r *http.Request) {
	if h.sources == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "source registry is not configured"})
		return
	}
	sourceID := strings.TrimSpace(r.PathValue("sourceId"))
	src, err := h.sources.Delete(r.Context(), sourceID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	h.removeDeletedGitHubSourceCache(src)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"source":  sourceDTO(src),
		"message": sourceStopReferencingMessage(src),
	})
}

func (h *Handler) handleListLocalSearchJobs(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "local search jobs are not configured"})
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if threadID == "" && runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "thread_id or run_id is required"})
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 50, 1, 200)
	jobs := h.manager.ListLocalSearchJobs(r.Context(), optionc.LocalSearchJobFilter{
		ThreadID: threadID,
		RunID:    runID,
		Limit:    limit,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"jobs": jobs,
		"scope": map[string]any{
			"thread_id": threadID,
			"run_id":    runID,
			"returned":  len(jobs),
			"limit":     limit,
		},
	})
}

func (h *Handler) handleGetLocalSearchJob(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "local search jobs are not configured"})
		return
	}
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "jobId is required"})
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if threadID == "" && runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "thread_id or run_id is required"})
		return
	}
	job, ok := h.manager.GetLocalSearchJob(r.Context(), jobID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "local search job was not found"})
		return
	}
	if (threadID != "" && job.ThreadID != threadID) || (runID != "" && job.RunID != runID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "local search job was not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "job": job})
}

func (h *Handler) handleArtifactSourcePreview(w http.ResponseWriter, r *http.Request, artifactID string, maxBytes int64) {
	artifact, err := h.store.GetArtifact(r.Context(), strings.TrimSpace(artifactID))
	if err != nil {
		h.writeErr(w, err)
		return
	}
	content, contentType, truncated, err := h.artifactPreviewContent(artifact, maxBytes)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preview": map[string]any{
		"source_id":    "artifact-" + artifact.ID,
		"artifact_id":  artifact.ID,
		"kind":         artifactSourceKind(artifact),
		"title":        artifact.Title,
		"content":      content,
		"byte_size":    artifact.ByteSize,
		"truncated":    truncated,
		"content_type": contentType,
	}})
}

func (h *Handler) artifactPreviewContent(artifact store.Artifact, maxBytes int64) (string, string, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	contentType := "text/plain; charset=utf-8"
	if mimeType := fileAuditMimeType(artifactHydrationMetadata(artifact)); mimeType != "" {
		contentType = mimeType
	}
	if artifact.ContentText != nil {
		content := *artifact.ContentText
		if int64(len(content)) > maxBytes {
			return content[:int(maxBytes)], contentType, true, nil
		}
		return content, contentType, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(artifact.Storage), "file") || artifact.ContentPath == nil {
		return "", contentType, false, nil
	}
	clean, err := cleanRelPath(strings.TrimSpace(*artifact.ContentPath))
	if err != nil {
		return "", contentType, false, err
	}
	full := filepath.Join(h.cfgDir, filepath.FromSlash(clean))
	f, err := os.Open(full)
	if err != nil {
		return "", contentType, false, err
	}
	defer f.Close()
	content, truncated, err := readPreviewString(f, maxBytes)
	return content, contentType, truncated, err
}

func artifactSourceKind(artifact store.Artifact) string {
	metadata := artifactHydrationMetadata(artifact)
	sourceValue := strings.TrimSpace(firstStringMapValue(metadata, "source"))
	if strings.EqualFold(sourceValue, fileartifact.SourceExternalTool) {
		return sources.KindConnectorFile
	}
	return sources.KindThreadUpload
}

func (h *Handler) handleLocalComputerSourcePreview(w http.ResponseWriter, r *http.Request, sourceID string, relPath string, maxBytes int64) {
	root, ok := h.localComputerSourceRoot(sourceID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "Local Computer source is not configured"})
		return
	}
	h.previewLocalPathWithExcludes(w, r, sourceID, root, "folder", relPath, store.DefaultSensitiveLocalPermissionExcludes(), maxBytes)
}

func (h *Handler) localComputerSourceRoot(sourceID string) (string, bool) {
	if h.settings == nil {
		return "", false
	}
	settings := h.settings.Get()
	if settings.LocalComputer == nil || settings.LocalComputer.Enabled == nil || !*settings.LocalComputer.Enabled {
		return "", false
	}
	mode := config.LocalComputerAccessModeWholeComputer
	if settings.LocalComputer.AccessMode != nil {
		mode = *settings.LocalComputer.AccessMode
	}
	if sourceID == "local-whole-computer" {
		if mode != config.LocalComputerAccessModeWholeComputer {
			return "", false
		}
		return localroots.PrimaryRoot(), true
	}
	if mode != config.LocalComputerAccessModeSelectedFolders {
		return "", false
	}
	for _, folder := range settings.LocalComputer.SelectedFolders {
		folder = strings.TrimSpace(folder)
		if folder == "" {
			continue
		}
		if sourceID == "local-"+shortSourceHash(folder) {
			return folder, true
		}
	}
	return "", false
}

func (h *Handler) previewLocalPath(w http.ResponseWriter, r *http.Request, sourceID string, root string, relPath string, maxBytes int64) {
	h.previewLocalPathWithExcludes(w, r, sourceID, root, "folder", relPath, nil, maxBytes)
}

func (h *Handler) previewLocalPathWithExcludes(w http.ResponseWriter, r *http.Request, sourceID string, root string, localKind string, relPath string, excludes []string, maxBytes int64) {
	full, _, err := resolveLocalGrantFilePath(root, localKind, relPath, excludes)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if st.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "preview path is a directory"})
		return
	}
	content, truncated, err := readPreviewString(f, maxBytes)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preview": map[string]any{
		"source_id":    sourceID,
		"kind":         sources.KindLocalReference,
		"path":         full,
		"content":      content,
		"byte_size":    st.Size(),
		"truncated":    truncated,
		"content_type": firstNonEmptyString(mime.TypeByExtension(filepath.Ext(full)), "text/plain; charset=utf-8"),
	}})
}

func (h *Handler) handleLocalGrantFileRaw(w http.ResponseWriter, r *http.Request) {
	grantID := strings.TrimSpace(r.PathValue("grantId"))
	if grantID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "grantId is required"})
		return
	}
	ref := firstNonEmptyString(
		strings.TrimSpace(r.URL.Query().Get("ref")),
		strings.TrimSpace(r.URL.Query().Get("file_ref")),
		strings.TrimSpace(r.URL.Query().Get("fileRef")),
		strings.TrimSpace(r.URL.Query().Get("path")),
	)
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	scope, ok, err := h.resolveLocalGrantFileScope(r.Context(), grantID, threadID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "local file grant was not found"})
		return
	}
	full, _, err := resolveLocalGrantFilePath(scope.Root, scope.Kind, ref, scope.Excludes)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	allowed, err := h.localGrantFileAllowed(r.Context(), threadID, scope, full)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "local file grant does not allow this file"})
		return
	}
	f, err := os.Open(full)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if st.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "requested local file path is a directory"})
		return
	}
	if st.Size() > maxLocalGrantRawFileBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"ok":          false,
			"error":       "local file is too large to preview directly",
			"max_bytes":   maxLocalGrantRawFileBytes,
			"actual_size": st.Size(),
		})
		return
	}
	contentType := localGrantFileContentType(full, f)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, filepath.Base(full), st.ModTime(), f)
}

func (h *Handler) resolveLocalGrantFileScope(ctx context.Context, grantID string, threadID string) (localGrantFileScope, bool, error) {
	grantID = strings.TrimSpace(grantID)
	if h.permissions != nil {
		rules, err := h.permissions.ListRules(ctx, permissions.RuleFilter{Category: permissions.CategoryRead})
		if err != nil {
			return localGrantFileScope{}, false, err
		}
		for _, rule := range rules {
			if strings.TrimSpace(rule.ID) != grantID {
				continue
			}
			if strings.TrimSpace(strings.ToLower(rule.Effect)) != permissions.EffectAllow {
				return localGrantFileScope{}, false, nil
			}
			scope := strings.TrimSpace(rule.Scope)
			if !localGrantPermissionScopeAllowsPreview(scope, threadID) {
				return localGrantFileScope{}, false, nil
			}
			subject := rule.Subject
			if strings.TrimSpace(strings.ToLower(subject.Kind)) != "local_location" || strings.TrimSpace(subject.LocalRoot) == "" {
				return localGrantFileScope{}, false, nil
			}
			return localGrantFileScope{
				GrantID:            rule.ID,
				Root:               subject.LocalRoot,
				Kind:               effectiveHTTPGrantKind(subject.LocalKind),
				Excludes:           localGrantExcludes(subject.Excludes),
				FromPermissionRule: true,
			}, true, nil
		}
	}
	if scope, ok := h.localComputerGrantFileScope(grantID); ok {
		return scope, true, nil
	}
	return localGrantFileScope{}, false, nil
}

func (h *Handler) localComputerGrantFileScope(grantID string) (localGrantFileScope, bool) {
	if h.settings == nil {
		return localGrantFileScope{}, false
	}
	settings := h.settings.Get()
	lc := settings.LocalComputer
	if lc == nil || lc.Enabled == nil || !*lc.Enabled {
		return localGrantFileScope{}, false
	}
	mode := config.LocalComputerAccessModeWholeComputer
	if lc.AccessMode != nil {
		mode = *lc.AccessMode
	}
	roots := []string{}
	if mode == config.LocalComputerAccessModeSelectedFolders {
		roots = append(roots, lc.SelectedFolders...)
	} else {
		roots = append(roots, localroots.WholeComputerRoots()...)
	}
	for _, rawRoot := range roots {
		root, err := cleanHTTPLocalPath(rawRoot)
		if err != nil || root == "" {
			continue
		}
		if grantID != localComputerSyntheticGrantID(root) && grantID != "local-"+shortSourceHash(root) && !(grantID == "local-whole-computer" && localGrantRootIsFilesystemRoot(root)) {
			continue
		}
		return localGrantFileScope{
			GrantID:      grantID,
			Root:         root,
			Kind:         "folder",
			Excludes:     store.DefaultSensitiveLocalPermissionExcludes(),
			FromSettings: true,
		}, true
	}
	return localGrantFileScope{}, false
}

func (h *Handler) localGrantFileAllowed(ctx context.Context, threadID string, scope localGrantFileScope, fullPath string) (bool, error) {
	subject := store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: fullPath,
		LocalKind: "file",
		Excludes:  scope.Excludes,
	}
	if h.permissions == nil {
		return scope.FromSettings || scope.FromPermissionRule, nil
	}
	evaluation, err := h.permissions.Evaluate(ctx, permissions.EvaluationInput{
		ThreadID: strings.TrimSpace(threadID),
		Tool:     "local_read_file",
		Category: permissions.CategoryRead,
		Subject:  subject,
	})
	if err != nil {
		return false, err
	}
	switch evaluation.Decision {
	case permissions.DecisionDeny:
		return false, nil
	case permissions.DecisionAllow:
		return true, nil
	default:
		return scope.FromSettings || scope.FromPermissionRule, nil
	}
}

func localGrantPermissionScopeAllowsPreview(scope string, threadID string) bool {
	scope = strings.TrimSpace(scope)
	threadID = strings.TrimSpace(threadID)
	if scope == "" || scope == "global" {
		return true
	}
	if !strings.HasPrefix(scope, "thread:") || strings.TrimPrefix(scope, "thread:") == "" {
		return false
	}
	if threadID == "" {
		return false
	}
	return scope == "thread:"+threadID
}

func localGrantFileContentType(path string, f *os.File) string {
	if contentType := strings.TrimSpace(mime.TypeByExtension(filepath.Ext(path))); contentType != "" {
		return contentType
	}
	var probe [512]byte
	n, err := f.Read(probe[:])
	if err == nil && n > 0 {
		_, _ = f.Seek(0, io.SeekStart)
		return http.DetectContentType(probe[:n])
	}
	_, _ = f.Seek(0, io.SeekStart)
	return "application/octet-stream"
}

func resolvePreviewLocalPath(root string, requested string) (string, error) {
	full, _, err := resolveLocalGrantFilePath(root, "folder", requested, nil)
	return full, err
}

func resolveLocalGrantFilePath(root string, localKind string, requested string, excludes []string) (string, string, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || !filepath.IsAbs(root) {
		return "", "", os.ErrNotExist
	}
	if evaluated, err := filepath.EvalSymlinks(root); err == nil {
		root = filepath.Clean(evaluated)
	}
	if effectiveHTTPGrantKind(localKind) == "file" {
		full, rel, err := resolveExactLocalGrantFilePath(root, requested)
		return full, rel, err
	}
	requested = strings.TrimSpace(requested)
	var full string
	if requested == "" {
		full = root
	} else if filepath.IsAbs(requested) {
		full = filepath.Clean(requested)
	} else {
		clean := filepath.Clean(strings.TrimPrefix(strings.ReplaceAll(requested, "\\", "/"), "/"))
		if clean == "." || strings.HasPrefix(clean, "..") {
			return "", "", os.ErrNotExist
		}
		full = filepath.Clean(filepath.Join(root, filepath.FromSlash(clean)))
	}
	evalRoot, rootErr := filepath.EvalSymlinks(root)
	evalFull, fullErr := filepath.EvalSymlinks(full)
	if rootErr == nil && fullErr == nil {
		root = filepath.Clean(evalRoot)
		full = filepath.Clean(evalFull)
	}
	if !pathWithin(root, full) {
		return "", "", os.ErrNotExist
	}
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", "", os.ErrNotExist
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." {
		rel = ""
	}
	if rel != "" && store.LocalPathExcludedByPermissionPatterns(rel, localGrantExcludes(excludes)) {
		return "", "", os.ErrPermission
	}
	return full, rel, nil
}

func resolveExactLocalGrantFilePath(root string, requested string) (string, string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "." || strings.EqualFold(requested, filepath.Base(root)) {
		return root, filepath.Base(root), nil
	}
	var full string
	if filepath.IsAbs(requested) {
		full = filepath.Clean(requested)
	} else {
		clean := filepath.Clean(strings.TrimPrefix(strings.ReplaceAll(requested, "\\", "/"), "/"))
		if !strings.EqualFold(clean, filepath.Base(root)) {
			return "", "", os.ErrNotExist
		}
		full = root
	}
	if evaluated, err := filepath.EvalSymlinks(full); err == nil {
		full = filepath.Clean(evaluated)
	}
	rootInfo, rootErr := os.Stat(root)
	fullInfo, fullErr := os.Stat(full)
	if rootErr != nil || fullErr != nil || rootInfo == nil || fullInfo == nil || !os.SameFile(rootInfo, fullInfo) {
		return "", "", os.ErrNotExist
	}
	return root, filepath.Base(root), nil
}

func localGrantExcludes(excludes []string) []string {
	if len(excludes) == 0 {
		return store.DefaultSensitiveLocalPermissionExcludes()
	}
	out := make([]string, 0, len(excludes))
	for _, value := range excludes {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return store.DefaultSensitiveLocalPermissionExcludes()
	}
	return out
}

func effectiveHTTPGrantKind(kind string) string {
	if strings.TrimSpace(strings.ToLower(kind)) == "file" {
		return "file"
	}
	return "folder"
}

func cleanHTTPLocalPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", os.ErrNotExist
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
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

func httpLocalComputerWholeRoot() string {
	root := localroots.PrimaryRoot()
	if clean, err := cleanHTTPLocalPath(root); err == nil && clean != "" {
		return clean
	}
	return root
}

func localComputerSyntheticGrantID(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(strings.TrimSpace(root))))
	return "local_computer_" + hex.EncodeToString(sum[:8])
}

func localGrantRootIsFilesystemRoot(root string) bool {
	clean := filepath.Clean(strings.TrimSpace(root))
	return clean != "" && clean != "." && filepath.Dir(clean) == clean
}

func pathWithin(root string, full string) bool {
	root = filepath.Clean(root)
	full = filepath.Clean(full)
	if root == full {
		return true
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == "." || rel == ".." {
		return err == nil && rel == "."
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func readPreviewString(r io.Reader, maxBytes int64) (string, bool, error) {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	var b strings.Builder
	n, err := io.Copy(&b, io.LimitReader(r, maxBytes+1))
	if err != nil {
		return "", false, err
	}
	content := b.String()
	if n > maxBytes {
		return content[:int(maxBytes)], true, nil
	}
	return content, false, nil
}

func (h *Handler) collectSources(r *http.Request) ([]map[string]any, error) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	includeHidden := parseBool(r.URL.Query().Get("include_hidden"))
	records := []map[string]any{}
	seen := map[string]struct{}{}
	add := func(record map[string]any) {
		id := strings.TrimSpace(auditAnyString(record["id"]))
		if id == "" {
			return
		}
		if kind != "" && strings.TrimSpace(auditAnyString(record["kind"])) != kind {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		records = append(records, record)
	}
	if h.sources != nil {
		rows, err := h.sources.List(r.Context(), sources.ListOptions{
			Kind:          includeSourceRegistryKind(kind),
			ThreadID:      threadID,
			ProjectID:     projectID,
			IncludeHidden: includeHidden,
		})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			add(sourceDTO(row))
		}
	}
	for _, record := range h.localComputerSourceDTOs() {
		add(record)
	}
	if projectID != "" {
		for _, record := range h.projectSnapshotSourceDTOs(r, projectID) {
			add(record)
		}
	} else {
		projects, err := h.store.ListProjects(r.Context(), 200)
		if err == nil {
			for _, project := range projects {
				for _, record := range h.projectSnapshotSourceDTOs(r, project.ID) {
					add(record)
				}
			}
		}
	}
	artifacts, err := h.store.ListFileArtifacts(r.Context(), threadID, 500)
	if err == nil {
		for _, artifact := range artifacts {
			add(artifactSourceDTO(artifact))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		ti := auditRecordCreatedAt(records[i])
		tj := auditRecordCreatedAt(records[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return strings.ToLower(auditAnyString(records[i]["title"])) < strings.ToLower(auditAnyString(records[j]["title"]))
	})
	return records, nil
}

func includeSourceRegistryKind(kind string) string {
	switch strings.TrimSpace(kind) {
	case sources.KindLocalReference, sources.KindProjectSnapshot, sources.KindGitHubClone, sources.KindConnectorFile, sources.KindThreadUpload:
		return strings.TrimSpace(kind)
	default:
		return ""
	}
}

func sourceDTO(src sources.Source) map[string]any {
	dto := map[string]any{
		"id":                  src.ID,
		"kind":                src.Kind,
		"label":               sourceKindLabel(src.Kind, src.Label),
		"title":               firstNonEmptyString(src.Title, src.Repository, src.LocalPath, src.RemotePath, src.ArtifactID, src.ID),
		"description":         src.Description,
		"thread_id":           src.ThreadID,
		"project_id":          src.ProjectID,
		"artifact_id":         src.ArtifactID,
		"repository":          src.Repository,
		"owner":               src.Owner,
		"repo":                src.Repo,
		"remote_id":           src.RemoteID,
		"remote_url":          src.RemoteURL,
		"source_url":          firstNonEmptyString(src.RemoteURL, strings.TrimSuffix(src.CloneURL, ".git")),
		"remote_path":         src.RemotePath,
		"local_path":          src.LocalPath,
		"worktree":            src.Worktree,
		"branch":              src.Branch,
		"head":                src.Head,
		"hidden":              src.Hidden,
		"capabilities":        src.Capabilities,
		"metadata":            src.Metadata,
		"created_at":          src.CreatedAt,
		"updated_at":          src.UpdatedAt,
		"action_delete_label": sourceDeleteLabel(src.Kind),
	}
	return dto
}

func (h *Handler) localComputerSourceDTOs() []map[string]any {
	if h.settings == nil {
		return nil
	}
	settings := h.settings.Get()
	lc := settings.LocalComputer
	if lc == nil || lc.Enabled == nil || !*lc.Enabled {
		return nil
	}
	mode := config.LocalComputerAccessModeWholeComputer
	if lc.AccessMode != nil {
		mode = *lc.AccessMode
	}
	if mode == config.LocalComputerAccessModeSelectedFolders {
		out := make([]map[string]any, 0, len(lc.SelectedFolders))
		for _, folder := range lc.SelectedFolders {
			folder = strings.TrimSpace(folder)
			if folder == "" {
				continue
			}
			out = append(out, sourceDTO(sources.Source{
				ID:           "local-" + shortSourceHash(folder),
				Kind:         sources.KindLocalReference,
				Label:        "This computer",
				Title:        filepath.Base(folder),
				Description:  "Read-only folder reference",
				LocalPath:    folder,
				Capabilities: []string{"read", "search", "preview"},
				CreatedAt:    time.Now().UTC(),
				UpdatedAt:    time.Now().UTC(),
				Metadata: map[string]any{
					"access_mode": string(mode),
				},
			}))
		}
		return out
	}
	return []map[string]any{sourceDTO(sources.Source{
		ID:           "local-whole-computer",
		Kind:         sources.KindLocalReference,
		Label:        "This computer",
		Title:        "Whole computer",
		Description:  "Read-only search and preview access",
		LocalPath:    localroots.PrimaryRoot(),
		Capabilities: []string{"read", "search", "preview"},
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		Metadata: map[string]any{
			"access_mode": string(mode),
			"roots":       localroots.WholeComputerRoots(),
		},
	})}
}

func (h *Handler) projectSnapshotSourceDTOs(r *http.Request, projectID string) []map[string]any {
	if h.projects == nil || strings.TrimSpace(projectID) == "" {
		return nil
	}
	project, err := h.store.GetProject(r.Context(), projectID)
	if err != nil {
		return nil
	}
	rows, err := h.projects.ListSources(r.Context(), projectID)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		id := "project-" + projectID + "-" + shortSourceHash(row.Name)
		dto := sourceDTO(sources.Source{
			ID:          id,
			Kind:        sources.KindProjectSnapshot,
			Label:       "Project snapshot",
			Title:       row.Name,
			Description: "Imported copy; does not update automatically",
			ProjectID:   projectID,
			LocalPath:   row.Path,
			Hidden:      row.Hidden,
			Capabilities: []string{
				"read",
				"search",
				"preview",
			},
			CreatedAt: project.CreatedAt,
			UpdatedAt: project.UpdatedAt,
			Metadata: map[string]any{
				"project_name": project.Name,
				"file_count":   row.FileCount,
				"total_bytes":  row.TotalBytes,
				"source_name":  row.Name,
			},
		})
		out = append(out, dto)
	}
	return out
}

func artifactSourceDTO(artifact store.Artifact) map[string]any {
	metadata := artifactHydrationMetadata(artifact)
	resultMetadata := plainMap(metadata["result_metadata"])
	sourceValue := strings.TrimSpace(firstNonEmptyString(firstStringMapValue(metadata, "source"), firstStringMapValue(resultMetadata, "source")))
	kind := sources.KindThreadUpload
	label := "Uploaded copy"
	description := "Attached copy stored with the chat"
	if strings.EqualFold(sourceValue, fileartifact.SourceExternalTool) || strings.EqualFold(firstStringMapValue(resultMetadata, "file_capability"), "remote_file_download") {
		kind = sources.KindConnectorFile
		label = "Connected file"
		description = "File downloaded from a connected service"
	}
	if sourceValue == "" || strings.EqualFold(sourceValue, types.ChatUploadAttachmentSource) {
		sourceValue = types.ChatUploadAttachmentSource
	}
	remoteURL, remotePath, remoteRef := cachedFileRemoteLocators(metadata, resultMetadata)
	dto := sourceDTO(sources.Source{
		ID:          "artifact-" + artifact.ID,
		Kind:        kind,
		Label:       label,
		Title:       artifact.Title,
		Description: description,
		ThreadID:    artifact.ThreadID,
		ArtifactID:  artifact.ID,
		RemoteID:    remoteRef,
		RemoteURL:   remoteURL,
		RemotePath:  remotePath,
		Capabilities: []string{
			"read",
			"preview",
		},
		CreatedAt: artifact.CreatedAt,
		UpdatedAt: artifact.CreatedAt,
		Metadata: map[string]any{
			"source":    sourceValue,
			"mime_type": fileAuditMimeType(metadata, resultMetadata),
			"file_kind": fileAuditFileKind(metadata, resultMetadata),
			"byte_size": artifact.ByteSize,
		},
	})
	dto["artifact"] = fileAttachmentArtifactDTO(artifact)
	return dto
}

func sourceKindLabel(kind string, fallback string) string {
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	switch kind {
	case sources.KindLocalReference:
		return "This computer"
	case sources.KindProjectSnapshot:
		return "Project snapshot"
	case sources.KindGitHubClone:
		return "GitHub repository"
	case sources.KindConnectorFile:
		return "Connected file"
	case sources.KindThreadUpload:
		return "Uploaded copy"
	default:
		return "Source"
	}
}

func sourceDeleteLabel(kind string) string {
	switch kind {
	case sources.KindLocalReference, sources.KindGitHubClone, sources.KindConnectorFile:
		return "Stop referencing"
	case sources.KindProjectSnapshot, sources.KindThreadUpload:
		return "Delete imported copy"
	default:
		return "Remove"
	}
}

func sourceStopReferencingMessage(src sources.Source) string {
	if src.Kind == sources.KindProjectSnapshot || src.Kind == sources.KindThreadUpload {
		return "Source row removed. Imported bytes are managed by their original file/artifact endpoint."
	}
	return "Stopped referencing this source. User-owned files were not deleted."
}

func shortSourceHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])[:16]
}

func (h *Handler) handleGitHubRepositories(w http.ResponseWriter, r *http.Request) {
	if h.github == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "GitHub integration is not configured"})
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	repos, err := h.github.ListRepositories(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "repositories": repos, "total_count": len(repos)})
}

func (h *Handler) handleCloneGitHubRepository(w http.ResponseWriter, r *http.Request) {
	if h.github == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "GitHub integration is not configured"})
		return
	}
	var body githubcli.CloneRepositoryRequest
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	result, err := h.github.CloneRepository(r.Context(), body)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "repository": result})
}

func (h *Handler) handleGitHubRepositoryTree(w http.ResponseWriter, r *http.Request) {
	if h.github == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "GitHub integration is not configured"})
		return
	}
	result, err := h.github.RepositoryTree(
		r.Context(),
		strings.TrimSpace(r.PathValue("sourceId")),
		strings.TrimSpace(r.URL.Query().Get("ref")),
		strings.TrimSpace(r.URL.Query().Get("path")),
	)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tree": result})
}

func (h *Handler) handleDeleteGitHubRepository(w http.ResponseWriter, r *http.Request) {
	if h.sources == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "source registry is not configured"})
		return
	}
	sourceID := strings.TrimSpace(r.PathValue("sourceId"))
	src, err := h.sources.Get(r.Context(), sourceID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if src.Kind != sources.KindGitHubClone {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "source is not a GitHub repository clone"})
		return
	}
	deleted, err := h.sources.Delete(r.Context(), sourceID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	h.removeDeletedGitHubSourceCache(deleted)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"source":  sourceDTO(deleted),
		"message": "Stopped referencing this repository. The app-managed clone was removed; GitHub was not changed.",
	})
}

func (h *Handler) removeDeletedGitHubSourceCache(src sources.Source) {
	if h.sources == nil || src.Kind != sources.KindGitHubClone {
		return
	}
	cacheDir, err := h.sources.GitHubSourceDir(src.ID)
	if err != nil {
		return
	}
	worktree := filepath.Clean(strings.TrimSpace(src.Worktree))
	if worktree != "" && filepath.IsAbs(worktree) && !pathWithin(cacheDir, worktree) {
		return
	}
	_ = os.RemoveAll(cacheDir)
}

func (h *Handler) handleRefreshGitHubRepository(w http.ResponseWriter, r *http.Request) {
	if h.github == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "GitHub integration is not configured"})
		return
	}
	sourceID := strings.TrimSpace(r.PathValue("sourceId"))
	result, err := h.github.RefreshRepository(r.Context(), githubcli.RefreshRepositoryRequest{
		RepoPath: sourceID,
	})
	if err != nil {
		h.writeErr(w, err)
		return
	}
	var repository any
	if sourceID != "" {
		repos, listErr := h.github.ListRepositories(r.Context(), "")
		if listErr == nil {
			for _, repo := range repos {
				if strings.EqualFold(strings.TrimSpace(repo.SourceID), sourceID) {
					repository = repo
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"repository": repository,
		"refresh":    result,
	})
}

func (h *Handler) handleGitHubRepositoryRaw(w http.ResponseWriter, r *http.Request) {
	if h.github == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "GitHub integration is not configured"})
		return
	}
	maxBytes := int64(parseInt(r.URL.Query().Get("max_bytes"), 1<<20, 1, 8<<20))
	result, err := h.github.RepositoryFile(
		r.Context(),
		strings.TrimSpace(r.PathValue("sourceId")),
		strings.TrimSpace(r.URL.Query().Get("ref")),
		strings.TrimSpace(r.URL.Query().Get("path")),
		maxBytes,
	)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("format")), "raw") {
		contentType := strings.TrimSpace(result.ContentTypeHint)
		if contentType == "" {
			contentType = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write([]byte(result.Content))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "file": result})
}
