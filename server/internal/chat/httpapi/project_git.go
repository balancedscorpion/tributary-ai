package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/filepipeline"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/localexec"
)

const (
	gitProjectRootPrefix   = "git:"
	localProjectRootPrefix = "local:"
)

var githubRepoPartRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
var projectSourceNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
var gitProgressLineRe = regexp.MustCompile(`^(?:remote:\s+)?([A-Za-z][A-Za-z ]+):\s+(\d+)% \((\d+)/(\d+)\)`)

type gitProfileInfo struct {
	GitUserName     string `json:"git_user_name,omitempty"`
	GitUserEmail    string `json:"git_user_email,omitempty"`
	GitHubLogin     string `json:"github_login,omitempty"`
	GitHubAvailable bool   `json:"github_available"`
	GitHubError     string `json:"github_error,omitempty"`
}

type gitRefreshResult struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type gitProjectRequest struct {
	URL       string `json:"url"`
	Name      string `json:"name,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
}

type gitProgressUpdate struct {
	Stage   string `json:"stage"`
	Percent int    `json:"percent"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
	Unit    string `json:"unit"`
	Line    string `json:"line,omitempty"`
}

func (h *Handler) handleGitProfile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"profile": loadGitProfile(ctx)})
}

func (h *Handler) handleCreateGitProject(w http.ResponseWriter, r *http.Request) {
	var body gitProjectRequest
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if parseBool(r.URL.Query().Get("stream")) {
		h.handleCreateGitProjectStream(w, r, body)
		return
	}

	h.createGitProjectJSON(w, r, body)
}

func (h *Handler) createGitProjectJSON(w http.ResponseWriter, r *http.Request, body gitProjectRequest) {
	repoURL, repoName, err := normalizeGitHubRepoURL(body.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = repoName
	}
	if name == "" {
		name = "GitHub repository"
	}

	var existing *store.Project
	if pid := strings.TrimSpace(body.ProjectID); pid != "" {
		p, err := h.store.GetProject(r.Context(), pid)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		existing = &p
	}

	tmpParent, err := os.MkdirTemp(h.projects.BaseDir(), ".git-clone-*")
	if err != nil {
		h.writeErr(w, err)
		return
	}
	defer os.RemoveAll(tmpParent)

	cloneDir := filepath.Join(tmpParent, "repo")
	gitCtx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	cloneURL, authEnv, cleanup := h.githubCloneURLAndAuthEnv(gitCtx, repoURL)
	defer cleanup()
	if output, err := runGitCommandInDirWithEnv(gitCtx, tmpParent, authEnv, gitArgsWithOAuthEnv(authEnv, "clone", "--", cloneURL, cloneDir)...); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":   "git clone failed",
			"detail":  gitCommandErrorDetail(gitCtx, output, err),
			"profile": loadGitProfileWithTimeout(),
		})
		return
	}

	p, targetRel, err := h.placeProjectSource(r.Context(), existing, name, gitProjectRootPrefix+cloneURL, repoName, cloneDir)
	if err != nil {
		h.writeErr(w, err)
		return
	}

	indexed, err := h.refreshProjectFileIndex(r.Context(), p.ID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	dto := projectToDTO(indexed)
	dto["source"] = "git"
	dto["git_url"] = cloneURL
	dto["added_path"] = targetRel
	writeJSON(w, http.StatusOK, map[string]any{
		"project": dto,
		"profile": loadGitProfileWithTimeout(),
		"message": fmt.Sprintf("Repository added at %s", targetRel),
	})
}

func (h *Handler) handleCreateGitProjectStream(w http.ResponseWriter, r *http.Request, body gitProjectRequest) {
	repoURL, repoName, err := normalizeGitHubRepoURL(body.URL)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = repoName
	}
	if name == "" {
		name = "GitHub repository"
	}

	var existing *store.Project
	if pid := strings.TrimSpace(body.ProjectID); pid != "" {
		p, err := h.store.GetProject(r.Context(), pid)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		existing = &p
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.createGitProjectJSON(w, r, body)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	writeGitStreamEvent(w, flusher, map[string]any{
		"type":    "status",
		"message": "Starting clone",
		"detail":  repoURL,
		"profile": loadGitProfileWithTimeout(),
	})

	tmpParent, err := os.MkdirTemp(h.projects.BaseDir(), ".git-clone-*")
	if err != nil {
		writeGitStreamEvent(w, flusher, gitStreamError("setup failed", err.Error(), nil))
		return
	}
	defer os.RemoveAll(tmpParent)

	cloneDir := filepath.Join(tmpParent, "repo")
	gitCtx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	cloneURL, authEnv, cleanup := h.githubCloneURLAndAuthEnv(gitCtx, repoURL)
	defer cleanup()

	progressCh := make(chan gitProgressUpdate, 32)
	doneCh := make(chan struct {
		output string
		err    error
	}, 1)
	go func() {
		output, err := runGitCommandProgressInDirWithEnv(gitCtx, progressCh, tmpParent, authEnv, gitArgsWithOAuthEnv(authEnv, "clone", "--progress", "--", cloneURL, cloneDir)...)
		doneCh <- struct {
			output string
			err    error
		}{output: output, err: err}
	}()

	var cloneOutput string
	var cloneErr error
	for {
		select {
		case progress, ok := <-progressCh:
			if !ok {
				progressCh = nil
				continue
			}
			writeGitStreamEvent(w, flusher, map[string]any{
				"type":    "progress",
				"stage":   progress.Stage,
				"percent": progress.Percent,
				"current": progress.Current,
				"total":   progress.Total,
				"unit":    progress.Unit,
				"line":    progress.Line,
			})
		case done := <-doneCh:
			cloneOutput = done.output
			cloneErr = done.err
			goto cloneDone
		case <-r.Context().Done():
			return
		}
	}

cloneDone:
	if cloneErr != nil {
		writeGitStreamEvent(w, flusher, gitStreamError(
			"git clone failed",
			gitCommandErrorDetail(gitCtx, cloneOutput, cloneErr),
			loadGitProfileWithTimeout(),
		))
		return
	}

	writeGitStreamEvent(w, flusher, map[string]any{
		"type":    "status",
		"message": "Indexing files",
	})
	p, targetRel, err := h.placeProjectSource(r.Context(), existing, name, gitProjectRootPrefix+cloneURL, repoName, cloneDir)
	if err != nil {
		writeGitStreamEvent(w, flusher, gitStreamError("placing repository failed", err.Error(), nil))
		return
	}
	indexed, err := h.refreshProjectFileIndex(r.Context(), p.ID)
	if err != nil {
		writeGitStreamEvent(w, flusher, gitStreamError("indexing failed", err.Error(), nil))
		return
	}
	dto := projectToDTO(indexed)
	dto["source"] = "git"
	dto["git_url"] = cloneURL
	dto["added_path"] = targetRel
	writeGitStreamEvent(w, flusher, map[string]any{
		"type":    "complete",
		"project": dto,
		"profile": loadGitProfileWithTimeout(),
		"message": fmt.Sprintf("Repository added at %s", targetRel),
	})
}

func (h *Handler) handleAddLocalProjectPath(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path      string `json:"path"`
		Name      string `json:"name,omitempty"`
		ProjectID string `json:"project_id,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	sourcePath, err := normalizeLocalSourcePath(body.Path)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if !info.IsDir() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "local path must be a directory"})
		return
	}

	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = filepath.Base(sourcePath)
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "Local directory"
	}

	var existing *store.Project
	if pid := strings.TrimSpace(body.ProjectID); pid != "" {
		p, err := h.store.GetProject(r.Context(), pid)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		existing = &p
	}

	tmpParent, err := os.MkdirTemp(h.projects.BaseDir(), ".local-source-*")
	if err != nil {
		h.writeErr(w, err)
		return
	}
	defer os.RemoveAll(tmpParent)
	tmpDir := filepath.Join(tmpParent, "source")

	sourceKind := "local"
	rootRelPath := localProjectRootPrefix + sourcePath
	if isGitWorktreeRoot(sourcePath) {
		sourceKind = "git"
		rootRelPath = gitProjectRootPrefix + sourcePath
		gitCtx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		if output, err := runGitCommandInDir(gitCtx, tmpParent, "clone", "--local", "--", sourcePath, tmpDir); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":  "local git clone failed",
				"detail": gitCommandErrorDetail(gitCtx, output, err),
			})
			return
		}
	} else if err := copyLocalDirectory(r.Context(), sourcePath, tmpDir); err != nil {
		h.writeErr(w, err)
		return
	}

	p, targetRel, err := h.placeProjectSource(r.Context(), existing, name, rootRelPath, name, tmpDir)
	if err != nil {
		h.writeErr(w, err)
		return
	}

	indexed, err := h.refreshProjectFileIndex(r.Context(), p.ID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	dto := projectToDTO(indexed)
	dto["source"] = sourceKind
	dto["local_path"] = sourcePath
	dto["added_path"] = targetRel
	writeJSON(w, http.StatusOK, map[string]any{
		"project": dto,
		"message": fmt.Sprintf("Directory added at %s", targetRel),
	})
}

func (h *Handler) handleRefreshGitProject(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing projectId"})
		return
	}
	bestEffort := parseBool(r.URL.Query().Get("best_effort"))
	project, results, err := h.refreshGitRepositories(r.Context(), projectID, bestEffort)
	if err != nil && !bestEffort {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":        "git refresh failed",
			"detail":       err.Error(),
			"repositories": gitRefreshResultsToDTO(results),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project":      projectToDTO(project),
		"repositories": gitRefreshResultsToDTO(results),
		"refreshed":    gitRefreshHadSuccess(results),
	})
}

func (h *Handler) refreshGitRepositoriesBestEffort(ctx context.Context, projectID string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	_, _, _ = h.refreshGitRepositories(ctx, projectID, true)
}

func (h *Handler) refreshGitRepositoriesBestEffortAsync(projectID string) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	go h.refreshGitRepositoriesBestEffort(context.Background(), projectID)
}

func (h *Handler) refreshGitRepositories(ctx context.Context, projectID string, bestEffort bool) (store.Project, []gitRefreshResult, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return store.Project{}, nil, errors.New("projectId is required")
	}
	project, err := h.store.GetProject(ctx, projectID)
	if err != nil {
		return store.Project{}, nil, err
	}
	filesDir, err := h.projects.FilesDir(projectID)
	if err != nil {
		return store.Project{}, nil, err
	}
	repos, err := findGitWorktrees(ctx, filesDir)
	if err != nil {
		if bestEffort {
			return project, []gitRefreshResult{{Path: ".", Status: "error", Detail: err.Error()}}, nil
		}
		return store.Project{}, nil, err
	}
	if len(repos) == 0 {
		return project, []gitRefreshResult{{Path: ".", Status: "skipped", Detail: "No Git repositories found."}}, nil
	}

	results := make([]gitRefreshResult, 0, len(repos))
	var firstErr error
	for _, repoDir := range repos {
		rel := relativeDisplayPath(filesDir, repoDir)
		authEnv, cleanup := h.githubGitAuthEnv(ctx)
		gitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		fetchOut, fetchErr := runGitCommandInDirWithEnv(gitCtx, repoDir, authEnv, gitArgsWithOAuthEnv(authEnv, "-C", repoDir, "fetch", "--prune")...)
		cancel()
		if fetchErr != nil {
			cleanup()
			detail := gitCommandErrorDetail(gitCtx, fetchOut, fetchErr)
			results = append(results, gitRefreshResult{Path: rel, Status: "error", Detail: detail})
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %s", rel, detail)
			}
			if !bestEffort {
				break
			}
			continue
		}

		gitCtx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		pullOut, pullErr := runGitCommandInDirWithEnv(gitCtx, repoDir, authEnv, gitArgsWithOAuthEnv(authEnv, "-C", repoDir, "pull", "--ff-only")...)
		cancel()
		cleanup()
		if pullErr != nil {
			detail := gitCommandErrorDetail(gitCtx, pullOut, pullErr)
			results = append(results, gitRefreshResult{Path: rel, Status: "error", Detail: detail})
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %s", rel, detail)
			}
			if !bestEffort {
				break
			}
			continue
		}
		detail := strings.TrimSpace(pullOut)
		if detail == "" {
			detail = "Already up to date."
		}
		results = append(results, gitRefreshResult{Path: rel, Status: "updated", Detail: detail})
	}

	indexed, indexErr := h.refreshProjectFileIndex(ctx, projectID)
	if indexErr != nil {
		if firstErr == nil {
			firstErr = indexErr
		}
		if bestEffort {
			return project, results, nil
		}
		return store.Project{}, results, indexErr
	}
	if firstErr != nil && !bestEffort {
		return indexed, results, firstErr
	}
	return indexed, results, nil
}

func (h *Handler) refreshProjectFileIndex(ctx context.Context, projectID string) (store.Project, error) {
	stats, err := h.projects.ScanFiles(ctx, projectID)
	if err != nil {
		return store.Project{}, err
	}
	if err := h.writeProjectFileEvaluationIndex(ctx, projectID, stats); err != nil {
		return store.Project{}, err
	}
	files := make([]store.ProjectFileStat, 0, len(stats))
	for _, stat := range stats {
		files = append(files, store.ProjectFileStat{
			Path:     stat.Path,
			ByteSize: stat.ByteSize,
		})
	}
	return h.store.ReplaceProjectFiles(ctx, projectID, files)
}

func (h *Handler) writeProjectFileEvaluationIndex(ctx context.Context, projectID string, stats []projects.FileStat) error {
	if h == nil || h.projects == nil {
		return nil
	}
	root, err := h.projects.FilesDir(projectID)
	if err != nil {
		return err
	}
	type projectFileEvaluation struct {
		Path       string         `json:"path"`
		Evaluation map[string]any `json:"evaluation"`
	}
	out := make([]projectFileEvaluation, 0, len(stats))
	for _, stat := range stats {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		rel := strings.TrimSpace(stat.Path)
		if rel == "" {
			continue
		}
		fullPath := filepath.Join(root, filepath.FromSlash(rel))
		evaluation, err := fileingest.EvaluatePath(filepath.Base(filepath.FromSlash(rel)), "", fullPath, fileingest.DefaultPreviewBytes, false)
		if err != nil {
			return err
		}
		metadata := evaluation.Metadata("")
		extractionKey := filepipeline.ProjectExtractionKey(rel)
		extractionRelPath := filepath.ToSlash(filepath.Join("projects", projectID, "file_extractions", extractionKey))
		if extraction := filepipeline.ExtractFile(ctx, filepipeline.ExtractionRequest{
			SourcePath:          fullPath,
			OutputDir:           filepath.Join(filepath.Dir(root), "file_extractions", extractionKey),
			OutputRelPath:       extractionRelPath,
			FileName:            filepath.Base(filepath.FromSlash(rel)),
			Evaluation:          evaluation,
			IncludeRawXML:       false,
			EnableExternalTools: false,
		}); extraction != nil {
			metadata["extraction"] = extraction
		}
		out = append(out, projectFileEvaluation{
			Path:       rel,
			Evaluation: metadata,
		})
	}
	payload, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"files":   out,
	}, "", "  ")
	if err != nil {
		return err
	}
	target := filepath.Join(filepath.Dir(root), "file_evaluations.json")
	return atomicfile.WriteFile(target, append(payload, '\n'), 0o600)
}

func (h *Handler) placeProjectSource(ctx context.Context, existing *store.Project, projectName string, rootRelPath string, sourceName string, sourceDir string) (store.Project, string, error) {
	var project store.Project
	var err error
	if existing != nil {
		project = *existing
	} else {
		project, err = h.store.CreateProject(ctx, projectName, rootRelPath)
		if err != nil {
			return store.Project{}, "", err
		}
	}

	filesDir, err := h.projects.EnsureFilesDir(project.ID)
	if err != nil {
		return store.Project{}, "", err
	}
	targetName, targetPath, err := uniqueProjectSourcePath(filesDir, sourceName)
	if err != nil {
		return store.Project{}, "", err
	}
	if err := moveProjectSourceDirectory(ctx, sourceDir, targetPath); err != nil {
		return store.Project{}, "", err
	}
	return project, filepath.ToSlash(targetName), nil
}

func (h *Handler) githubCloneURLAndAuthEnv(ctx context.Context, repoURL string) (string, []string, func()) {
	authEnv, cleanup := h.githubGitAuthEnv(ctx)
	if len(authEnv) == 0 {
		return repoURL, nil, cleanup
	}
	if httpsURL, ok := githubHTTPSRepoURL(repoURL); ok {
		return httpsURL, authEnv, cleanup
	}
	return repoURL, authEnv, cleanup
}

func (h *Handler) githubGitAuthEnv(ctx context.Context) ([]string, func()) {
	cleanup := func() {}
	token := ""
	if h.secrets != nil {
		if gh, err := h.secrets.GetGitHub(); err == nil {
			token = strings.TrimSpace(gh.OAuthAccessToken)
		}
	}
	if token == "" {
		if cliToken, err := githubCLIToken(ctx); err == nil {
			token = strings.TrimSpace(cliToken)
		}
	}
	if token == "" {
		return nil, cleanup
	}
	tmpDir, err := os.MkdirTemp("", "ai-analyst-git-askpass-*")
	if err != nil {
		return nil, cleanup
	}
	cleanup = func() { _ = os.RemoveAll(tmpDir) }
	askpassPath, err := writeGitAskpass(tmpDir)
	if err != nil {
		cleanup()
		return nil, func() {}
	}
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=" + askpassPath,
		"GCM_INTERACTIVE=never",
		"AIA_GITHUB_TOKEN=" + token,
	}, cleanup
}

func githubHTTPSRepoURL(repoURL string) (string, bool) {
	in := strings.TrimSpace(repoURL)
	if strings.HasPrefix(in, "git@github.com:") {
		owner, repo, err := parseGitHubOwnerRepo(strings.TrimPrefix(in, "git@github.com:"))
		if err != nil {
			return "", false
		}
		return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo), true
	}
	u, err := url.Parse(in)
	if err != nil || u == nil || u.Scheme == "" || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", false
	}
	owner, repo, err := parseGitHubOwnerRepo(u.EscapedPath())
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo), true
}

func loadGitProfile(ctx context.Context) gitProfileInfo {
	name, _ := runCommandOutput(ctx, "git", "config", "--global", "--get", "user.name")
	email, _ := runCommandOutput(ctx, "git", "config", "--global", "--get", "user.email")
	profile := gitProfileInfo{
		GitUserName:  strings.TrimSpace(name),
		GitUserEmail: strings.TrimSpace(email),
	}
	out, err := runCommandOutput(ctx, "gh", "auth", "status", "-h", "github.com")
	if err != nil {
		profile.GitHubError = truncateGitOutput(out)
		if profile.GitHubError == "" {
			profile.GitHubError = err.Error()
		}
		return profile
	}
	profile.GitHubAvailable = true
	profile.GitHubLogin = parseGhActiveAccount(out)
	return profile
}

func loadGitProfileWithTimeout() gitProfileInfo {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return loadGitProfile(ctx)
}

func parseGhActiveAccount(output string) string {
	scanner := bufio.NewScanner(strings.NewReader(output))
	pending := ""
	first := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if idx := strings.Index(line, "Logged in to github.com account "); idx >= 0 {
			rest := strings.TrimSpace(line[idx+len("Logged in to github.com account "):])
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				pending = strings.Trim(fields[0], "()")
				if first == "" {
					first = pending
				}
			}
			continue
		}
		if strings.Contains(line, "Active account: true") && pending != "" {
			return pending
		}
	}
	return first
}

func runCommandOutput(ctx context.Context, name string, args ...string) (string, error) {
	return runCommandOutputWithEnv(ctx, nil, name, args...)
}

func runCommandOutputWithEnv(ctx context.Context, env []string, name string, args ...string) (string, error) {
	return runCommandOutputInDirWithEnv(ctx, "", env, name, args...)
}

func runCommandOutputInDirWithEnv(ctx context.Context, workDir string, env []string, name string, args ...string) (string, error) {
	dir, err := commandWorkingDir(workDir)
	if err != nil {
		return "", err
	}
	cmd := localexec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.CombinedOutput()
	return truncateGitOutput(string(out)), err
}

func runGitCommand(ctx context.Context, args ...string) (string, error) {
	return runGitCommandWithEnv(ctx, nil, args...)
}

func runGitCommandInDir(ctx context.Context, workDir string, args ...string) (string, error) {
	return runGitCommandInDirWithEnv(ctx, workDir, nil, args...)
}

func runGitCommandWithEnv(ctx context.Context, env []string, args ...string) (string, error) {
	return runGitCommandInDirWithEnv(ctx, "", env, args...)
}

func runGitCommandInDirWithEnv(ctx context.Context, workDir string, env []string, args ...string) (string, error) {
	return runCommandOutputInDirWithEnv(ctx, workDir, env, "git", args...)
}

func gitArgsWithOAuthEnv(env []string, args ...string) []string {
	if len(env) == 0 {
		return args
	}
	out := []string{"-c", "credential.helper="}
	if hooksPath := gitDisabledHooksPath(); hooksPath != "" {
		out = append(out, "-c", "core.hooksPath="+hooksPath)
	}
	out = append(out, args...)
	return out
}

func runGitCommandProgress(ctx context.Context, progress chan<- gitProgressUpdate, args ...string) (string, error) {
	return runGitCommandProgressWithEnv(ctx, progress, nil, args...)
}

func runGitCommandProgressWithEnv(ctx context.Context, progress chan<- gitProgressUpdate, env []string, args ...string) (string, error) {
	return runGitCommandProgressInDirWithEnv(ctx, progress, "", env, args...)
}

func runGitCommandProgressInDirWithEnv(ctx context.Context, progress chan<- gitProgressUpdate, workDir string, env []string, args ...string) (string, error) {
	defer close(progress)

	dir, err := commandWorkingDir(workDir)
	if err != nil {
		return "", err
	}
	cmd := localexec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}

	var mu sync.Mutex
	var output strings.Builder
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		readGitProgressStream(ctx, stdout, &output, &mu, progress)
	}()
	go func() {
		defer wg.Done()
		readGitProgressStream(ctx, stderr, &output, &mu, progress)
	}()

	err = cmd.Wait()
	wg.Wait()
	return truncateGitOutput(output.String()), err
}

func commandWorkingDir(preferred string) (string, error) {
	candidates := []string{strings.TrimSpace(preferred), os.TempDir()}
	for _, candidate := range candidates {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		clean := filepath.Clean(candidate)
		if !filepath.IsAbs(clean) {
			abs, err := filepath.Abs(clean)
			if err != nil {
				continue
			}
			clean = abs
		}
		info, err := os.Stat(clean)
		if err == nil && info.IsDir() {
			return clean, nil
		}
	}
	return "", errors.New("working directory is not available")
}

func readGitProgressStream(ctx context.Context, r io.Reader, output *strings.Builder, mu *sync.Mutex, progress chan<- gitProgressUpdate) {
	reader := bufio.NewReader(r)
	var line strings.Builder
	emit := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		mu.Lock()
		output.WriteString(raw)
		output.WriteByte('\n')
		mu.Unlock()

		update, ok := parseGitProgressLine(raw)
		if !ok {
			return
		}
		select {
		case progress <- update:
		case <-ctx.Done():
		}
	}

	for {
		b, err := reader.ReadByte()
		if err != nil {
			if line.Len() > 0 {
				emit(line.String())
			}
			return
		}
		if b == '\r' || b == '\n' {
			emit(line.String())
			line.Reset()
			continue
		}
		line.WriteByte(b)
	}
}

func writeGitStreamEvent(w http.ResponseWriter, flusher http.Flusher, event map[string]any) {
	_ = json.NewEncoder(w).Encode(event)
	flusher.Flush()
}

func gitStreamError(message string, detail string, profile any) map[string]any {
	event := map[string]any{
		"type":  "error",
		"error": message,
	}
	if strings.TrimSpace(detail) != "" {
		event["detail"] = detail
	}
	if profile != nil {
		event["profile"] = profile
	}
	return event
}

func gitCommandErrorDetail(ctx context.Context, output string, err error) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if strings.TrimSpace(output) == "" {
			return "git command timed out"
		}
		return strings.TrimSpace(output) + "\n\ngit command timed out"
	}
	if strings.TrimSpace(output) != "" {
		return strings.TrimSpace(output)
	}
	if err != nil {
		return err.Error()
	}
	return "git command failed"
}

func truncateGitOutput(output string) string {
	output = strings.TrimSpace(output)
	const max = 4000
	if len(output) <= max {
		return output
	}
	return output[:max] + "\n... output truncated ..."
}

func parseGitProgressLine(line string) (gitProgressUpdate, bool) {
	matches := gitProgressLineRe.FindStringSubmatch(strings.TrimSpace(line))
	if len(matches) != 5 {
		return gitProgressUpdate{}, false
	}
	percent, percentErr := strconv.Atoi(matches[2])
	current, currentErr := strconv.Atoi(matches[3])
	total, totalErr := strconv.Atoi(matches[4])
	if percentErr != nil || currentErr != nil || totalErr != nil {
		return gitProgressUpdate{}, false
	}
	stage := strings.TrimSpace(matches[1])
	return gitProgressUpdate{
		Stage:   stage,
		Percent: percent,
		Current: current,
		Total:   total,
		Unit:    gitProgressUnit(stage),
		Line:    strings.TrimSpace(line),
	}, true
}

func gitProgressUnit(stage string) string {
	lower := strings.ToLower(stage)
	switch {
	case strings.Contains(lower, "file"):
		return "files"
	case strings.Contains(lower, "object"):
		return "objects"
	case strings.Contains(lower, "delta"):
		return "deltas"
	default:
		return "items"
	}
}

func normalizeGitHubRepoURL(raw string) (string, string, error) {
	in := strings.TrimSpace(raw)
	if in == "" {
		return "", "", errors.New("repository URL is required")
	}
	if len(in) > 2048 {
		return "", "", errors.New("repository URL is too long")
	}
	if strings.HasPrefix(in, "-") || strings.ContainsAny(in, " \t\r\n\x00") {
		return "", "", errors.New("invalid repository URL")
	}
	if strings.HasPrefix(in, "/") || strings.HasPrefix(in, "./") || strings.HasPrefix(in, "../") {
		return "", "", errors.New("local repository paths are not supported")
	}

	if !strings.Contains(in, "://") && !strings.Contains(in, "@") && strings.Count(strings.Trim(in, "/"), "/") == 1 {
		owner, repo, err := parseGitHubOwnerRepo(in)
		if err != nil {
			return "", "", err
		}
		return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo), repo, nil
	}

	const scpPrefix = "git@github.com:"
	if strings.HasPrefix(in, scpPrefix) {
		owner, repo, err := parseGitHubOwnerRepo(strings.TrimPrefix(in, scpPrefix))
		if err != nil {
			return "", "", err
		}
		return fmt.Sprintf("git@github.com:%s/%s.git", owner, repo), repo, nil
	}

	u, err := url.Parse(in)
	if err != nil || u == nil || u.Scheme == "" {
		return "", "", errors.New("repository URL must be a GitHub SSH or HTTPS URL")
	}
	if strings.TrimSpace(u.RawQuery) != "" || strings.TrimSpace(u.Fragment) != "" {
		return "", "", errors.New("repository URL must not include query parameters or fragments")
	}
	if strings.EqualFold(u.Scheme, "file") {
		return "", "", errors.New("local repository paths are not supported")
	}
	if !strings.EqualFold(u.Hostname(), "github.com") {
		return "", "", errors.New("only github.com repositories are supported")
	}

	owner, repo, err := parseGitHubOwnerRepo(u.EscapedPath())
	if err != nil {
		return "", "", err
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		if u.User != nil {
			return "", "", errors.New("repository URL must not include credentials")
		}
		return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo), repo, nil
	case "ssh":
		if u.User == nil || u.User.Username() != "git" {
			return "", "", errors.New("ssh GitHub URLs must use git@github.com")
		}
		if _, hasPassword := u.User.Password(); hasPassword {
			return "", "", errors.New("repository URL must not include credentials")
		}
		return fmt.Sprintf("ssh://git@github.com/%s/%s.git", owner, repo), repo, nil
	default:
		return "", "", errors.New("repository URL must use ssh or https")
	}
}

func parseGitHubOwnerRepo(pathPart string) (string, string, error) {
	clean := strings.Trim(strings.TrimSpace(pathPart), "/")
	parts := strings.Split(clean, "/")
	if len(parts) != 2 {
		return "", "", errors.New("repository URL must point to a GitHub owner/repo")
	}
	owner := strings.TrimSpace(parts[0])
	repo := strings.TrimSuffix(strings.TrimSpace(parts[1]), ".git")
	if owner == "" || repo == "" || !githubRepoPartRe.MatchString(owner) || !githubRepoPartRe.MatchString(repo) {
		return "", "", errors.New("repository URL must point to a valid GitHub owner/repo")
	}
	return owner, repo, nil
}

func normalizeLocalSourcePath(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", errors.New("local path is required")
	}
	if strings.ContainsRune(p, '\x00') {
		return "", errors.New("invalid local path")
	}
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if p == "~" {
			p = home
		} else {
			p = filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("local path must be absolute")
	}
	return filepath.Clean(p), nil
}

func isGitWorktreeRoot(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, ".git")); err == nil && st != nil {
		return true
	}
	return false
}

func copyLocalDirectory(ctx context.Context, src string, dst string) error {
	return copyProjectDirectory(ctx, src, dst, true)
}

func moveProjectSourceDirectory(ctx context.Context, src string, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else {
		if copyErr := copyProjectDirectory(ctx, src, dst, false); copyErr != nil {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("move directory failed: rename: %v; copy fallback: %w", err, copyErr)
		}
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove temporary source after copy: %w", err)
	}
	return nil
}

func copyProjectDirectory(ctx context.Context, src string, dst string, skipGit bool) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if skipGit && name == ".git" {
				return fs.SkipDir
			}
		} else if d.Type()&os.ModeSymlink != 0 {
			if skipGit {
				return nil
			}
			return copySymlink(p, filepath.Join(dst, rel))
		}

		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err != nil || info == nil {
			return err
		}
		if skipGit && info.Size() > maxProjectFileBytes {
			return fmt.Errorf("file too large: %s", filepath.ToSlash(rel))
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if !skipGit {
			mode = info.Mode().Perm()
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		inErr := in.Close()
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if inErr != nil {
			return inErr
		}
		return closeErr
	})
}

func copySymlink(src string, dst string) error {
	target, err := os.Readlink(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.Symlink(target, dst)
}

func uniqueProjectSourcePath(parent string, preferred string) (string, string, error) {
	base := sanitizeProjectSourceName(preferred)
	for i := 0; i < 1000; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i+1)
		}
		p := filepath.Join(parent, name)
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			return name, p, nil
		} else if err != nil {
			return "", "", err
		}
	}
	return "", "", errors.New("could not choose a destination directory")
}

func sanitizeProjectSourceName(name string) string {
	base := strings.TrimSpace(name)
	base = strings.TrimSuffix(base, ".git")
	base = strings.Trim(base, ". /")
	base = projectSourceNameRe.ReplaceAllString(base, "-")
	base = strings.Trim(base, ".-")
	if base == "" {
		base = "source"
	}
	if len(base) > 80 {
		base = base[:80]
		base = strings.Trim(base, ".-")
		if base == "" {
			base = "source"
		}
	}
	base = windowsSafeSourceName(base, "source")
	return base
}

func windowsSafeSourceName(name string, fallback string) string {
	base := strings.TrimRight(strings.TrimSpace(name), ". ")
	if base == "" {
		base = fallback
	}
	stem := windowsPathStem(base)
	if windowsReservedDeviceName(stem) {
		if idx := strings.IndexByte(base, '.'); idx >= 0 {
			base = base[:idx] + "-repo" + base[idx:]
		} else {
			base += "-repo"
		}
	}
	return base
}

func windowsPathStem(name string) string {
	if idx := strings.IndexByte(name, '.'); idx >= 0 {
		return name[:idx]
	}
	return name
}

func windowsReservedDeviceName(stem string) bool {
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	default:
		return false
	}
}

func writeGitAskpass(tmpDir string) (string, error) {
	return writeGitAskpassForOS(tmpDir, runtime.GOOS)
}

func writeGitAskpassForOS(tmpDir string, goos string) (string, error) {
	if goos == "windows" {
		askpassPath := filepath.Join(tmpDir, "askpass.cmd")
		script := "@echo off\r\nsetlocal\r\nset \"prompt=%~1\"\r\necho(%prompt% | findstr /I \"Username\" >nul\r\nif not errorlevel 1 (\r\n  echo x-access-token\r\n) else (\r\n  echo %AIA_GITHUB_TOKEN%\r\n)\r\n"
		if err := os.WriteFile(askpassPath, []byte(script), 0o700); err != nil {
			return "", err
		}
		return askpassPath, nil
	}
	askpassPath := filepath.Join(tmpDir, "askpass.sh")
	script := `#!/bin/sh
case "$1" in
*Username*) printf '%s\n' 'x-access-token' ;;
*) printf '%s\n' "$AIA_GITHUB_TOKEN" ;;
esac
`
	if err := os.WriteFile(askpassPath, []byte(script), 0o700); err != nil {
		return "", err
	}
	return askpassPath, nil
}

func gitDisabledHooksPath() string {
	path := filepath.Join(os.TempDir(), "ai-analyst-empty-git-hooks")
	if err := os.MkdirAll(path, 0o700); err != nil {
		return ""
	}
	return path
}

func findGitWorktrees(ctx context.Context, root string) ([]string, error) {
	root = filepath.Clean(root)
	seen := map[string]struct{}{}
	repos := []string{}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return fs.SkipDir
		}
		if !hasGitMetadata(p) {
			return nil
		}
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			return nil
		}
		seen[clean] = struct{}{}
		repos = append(repos, clean)
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) || os.IsNotExist(walkErr) {
			return nil, nil
		}
		return nil, walkErr
	}
	sort.Strings(repos)
	return repos, nil
}

func hasGitMetadata(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, ".git")); err == nil && st != nil {
		return true
	}
	return false
}

func relativeDisplayPath(root string, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." || strings.TrimSpace(rel) == "" {
		return "."
	}
	return filepath.ToSlash(rel)
}

func gitRefreshResultsToDTO(results []gitRefreshResult) []map[string]any {
	out := make([]map[string]any, 0, len(results))
	for _, r := range results {
		item := map[string]any{
			"path":   r.Path,
			"status": r.Status,
		}
		if strings.TrimSpace(r.Detail) != "" {
			item["detail"] = r.Detail
		}
		out = append(out, item)
	}
	return out
}

func gitRefreshHadSuccess(results []gitRefreshResult) bool {
	for _, r := range results {
		if r.Status == "updated" {
			return true
		}
	}
	return false
}
