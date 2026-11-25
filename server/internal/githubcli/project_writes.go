package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/localexec"
	"ai-analyst-local/internal/sources"
)

const (
	defaultCloneTimeout  = 30 * time.Minute
	defaultPullTimeout   = 15 * time.Minute
	defaultRawGHTimeout  = 10 * time.Minute
	maxReadRawGHTimeout  = 2 * time.Minute
	maxWriteTimeout      = 60 * time.Minute
	gitProjectRootPrefix = "git:"
)

var projectSourceNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (m *Manager) executeCloneRepository(ctx context.Context, args map[string]any) (map[string]any, error) {
	var req CloneRepositoryRequest
	if err := decodeApprovalArgs(args, &req); err != nil {
		return nil, err
	}
	result, err := m.cloneRepository(ctx, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"project_id":      result.ProjectID,
		"source_id":       result.SourceID,
		"source_kind":     result.SourceKind,
		"repository":      result.Repository,
		"url":             result.URL,
		"added_path":      result.AddedPath,
		"worktree":        result.Worktree,
		"partial_clone":   result.PartialClone,
		"sparse_checkout": result.SparseCheckout,
		"output":          result.Output,
		"project":         result.Project,
	}, nil
}

func (m *Manager) executeRefreshRepositories(ctx context.Context, args map[string]any) (map[string]any, error) {
	var req RefreshRepositoryRequest
	if err := decodeApprovalArgs(args, &req); err != nil {
		return nil, err
	}
	result, err := m.refreshRepositories(ctx, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"project_id":   result.ProjectID,
		"repositories": result.Repositories,
		"refreshed":    result.Refreshed,
		"project":      result.Project,
	}, nil
}

func (m *Manager) RefreshRepository(ctx context.Context, req RefreshRepositoryRequest) (RefreshRepositoryResult, error) {
	return m.refreshRepositories(ctx, req)
}

func (m *Manager) executeRawGHCommand(ctx context.Context, args map[string]any) (map[string]any, error) {
	var req RawGHCommandRequest
	if err := decodeApprovalArgs(args, &req); err != nil {
		return nil, err
	}
	normalizedArgs, err := ValidateRawGHArgs(req.Args)
	if err != nil {
		return nil, err
	}
	if err := m.validateRawGHAllowedRepositories(normalizedArgs); err != nil {
		return nil, err
	}
	token, err := m.githubSecrets(ctx)
	if err != nil {
		return nil, err
	}
	timeout := boundedWriteTimeout(req.TimeoutSeconds, defaultRawGHTimeout)
	workDir := ""
	if rawGHCommandIsRepoClone(normalizedArgs) {
		if strings.TrimSpace(req.ProjectID) == "" {
			return nil, errors.New("project context is required for raw gh repo clone")
		}
		if err := validateRawGHRepoCloneTarget(normalizedArgs); err != nil {
			return nil, err
		}
		if m == nil || m.projects == nil {
			return nil, errors.New("project store is not configured")
		}
		workDir, err = m.projects.EnsureFilesDir(req.ProjectID)
		if err != nil {
			return nil, err
		}
	}
	res, err := m.runGHWithOptionsInDir(ctx, token.OAuthAccessToken, normalizedArgs, timeout, defaultStdoutCap, defaultStderrCap, workDir)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"args":   normalizedArgs,
		"result": res,
	}
	if workDir != "" && m.refreshProjectIndex != nil {
		project, err := m.refreshProjectIndex(ctx, req.ProjectID)
		if err != nil {
			return nil, err
		}
		out["project_id"] = req.ProjectID
		out["project"] = project
	}
	return out, nil
}

func (m *Manager) RunReadGHCommand(ctx context.Context, req RawGHCommandRequest) (RawGHCommandResult, error) {
	normalizedArgs, err := ValidateRawGHArgs(req.Args)
	if err != nil {
		return RawGHCommandResult{}, err
	}
	if !RawGHCommandIsReadOnly(normalizedArgs) {
		return RawGHCommandResult{}, errors.New("raw gh command requires approval")
	}
	if err := m.validateRawGHAllowedRepositories(normalizedArgs); err != nil {
		return RawGHCommandResult{}, err
	}
	token, err := m.githubSecrets(ctx)
	if err != nil {
		return RawGHCommandResult{}, err
	}
	res, err := m.runGHWithOptions(ctx, token.OAuthAccessToken, normalizedArgs, boundedReadRawGHTimeout(req.TimeoutSeconds), defaultStdoutCap, defaultStderrCap)
	if err != nil {
		return RawGHCommandResult{}, err
	}
	return RawGHCommandResult{
		Args:   normalizedArgs,
		Result: res,
	}, nil
}

func (m *Manager) cloneRepository(ctx context.Context, req CloneRepositoryRequest) (CloneRepositoryResult, error) {
	if m != nil && m.sources != nil {
		return m.cloneRepositoryToSource(ctx, req)
	}
	if err := contextErr(ctx); err != nil {
		return CloneRepositoryResult{}, err
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if m == nil || m.projects == nil {
		return CloneRepositoryResult{}, errors.New("project store is not configured")
	}
	fullName, ok := parseGitHubFullName(req.Repository)
	if !ok {
		return CloneRepositoryResult{}, errors.New("repository must be a GitHub owner/name or github.com URL")
	}
	if !repositoryAllowedBySet(fullName, m.allowedRepositorySet()) {
		return CloneRepositoryResult{}, fmt.Errorf("GitHub repository is not allowed by settings: %s", fullName)
	}
	_, repoName, _ := strings.Cut(fullName, "/")
	sourceName := strings.TrimSpace(req.Name)
	if sourceName == "" {
		sourceName = repoName
	}
	repoURL := "https://github.com/" + fullName + ".git"
	branch, err := safeFlagValue("branch", req.Branch, 200)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	sparsePaths, err := normalizeSparsePaths(req.SparsePaths)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	depth := req.Depth
	if depth < 0 {
		return CloneRepositoryResult{}, errors.New("depth must be positive")
	}
	if depth > 100000 {
		depth = 100000
	}
	token, err := m.githubSecrets(ctx)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	tmpParent, err := os.MkdirTemp(m.projects.BaseDir(), ".github-clone-*")
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	if !filepath.IsAbs(tmpParent) {
		abs, err := filepath.Abs(tmpParent)
		if err != nil {
			_ = os.RemoveAll(tmpParent)
			return CloneRepositoryResult{}, err
		}
		tmpParent = abs
	}
	defer os.RemoveAll(tmpParent)

	cloneDir := filepath.Join(tmpParent, "repo")
	gitArgs := []string{
		"-c", "filter.lfs.smudge=",
		"-c", "filter.lfs.process=",
		"-c", "filter.lfs.required=false",
		"clone", "--progress",
	}
	if req.PartialClone || len(sparsePaths) > 0 {
		gitArgs = append(gitArgs, "--filter=blob:none")
	}
	if len(sparsePaths) > 0 {
		gitArgs = append(gitArgs, "--sparse")
	}
	if depth > 0 {
		gitArgs = append(gitArgs, "--depth", strconv.Itoa(depth))
	}
	if branch != "" {
		gitArgs = append(gitArgs, "--branch", branch)
	}
	gitArgs = append(gitArgs, "--", repoURL, cloneDir)
	timeout := boundedWriteTimeout(req.TimeoutSeconds, defaultCloneTimeout)
	cloneRes, err := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, tmpParent, gitArgs...)
	if err != nil {
		return CloneRepositoryResult{}, gitCommandError(cloneRes, err, token.OAuthAccessToken)
	}
	output := strings.TrimSpace(strings.Join([]string{cloneRes.Stdout, cloneRes.Stderr}, "\n"))
	if len(sparsePaths) > 0 {
		sparseArgs := append([]string{"-C", cloneDir, "sparse-checkout", "set", "--"}, sparsePaths...)
		sparseRes, err := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, cloneDir, sparseArgs...)
		if err != nil {
			return CloneRepositoryResult{}, gitCommandError(sparseRes, err, token.OAuthAccessToken)
		}
		sparseOutput := strings.TrimSpace(strings.Join([]string{sparseRes.Stdout, sparseRes.Stderr}, "\n"))
		if sparseOutput != "" {
			if output != "" {
				output += "\n"
			}
			output += sparseOutput
		}
	}
	if info, err := os.Stat(cloneDir); err != nil {
		return CloneRepositoryResult{}, fmt.Errorf("git clone did not create checkout directory: %w", err)
	} else if !info.IsDir() {
		return CloneRepositoryResult{}, fmt.Errorf("git clone checkout path is not a directory: %s", cloneDir)
	}
	var createdProject any
	if projectID == "" {
		if m.createProject == nil {
			return CloneRepositoryResult{}, errors.New("project context is required and project creation is not configured")
		}
		project, err := m.createProject(ctx, sourceName, gitProjectRootPrefix+repoURL)
		if err != nil {
			return CloneRepositoryResult{}, err
		}
		projectID = strings.TrimSpace(project.ID)
		if projectID == "" {
			return CloneRepositoryResult{}, errors.New("created project did not include an id")
		}
		createdProject = project
	}
	filesDir, err := m.projects.EnsureFilesDir(projectID)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	targetName, targetPath, err := uniqueGitHubSourcePath(filesDir, sourceName)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	if err := moveGitHubSourceDirectory(ctx, cloneDir, targetPath); err != nil {
		return CloneRepositoryResult{}, err
	}
	project := createdProject
	if m.refreshProjectIndex != nil {
		project, err = m.refreshProjectIndex(ctx, projectID)
		if err != nil {
			return CloneRepositoryResult{}, err
		}
	}
	return CloneRepositoryResult{
		ProjectID:      projectID,
		Repository:     fullName,
		URL:            repoURL,
		AddedPath:      filepath.ToSlash(targetName),
		PartialClone:   req.PartialClone || len(sparsePaths) > 0,
		SparseCheckout: len(sparsePaths) > 0,
		Output:         truncateText(output, 4000),
		Project:        project,
	}, nil
}

// CloneRepository is used by explicit UI actions. Model/tool execution still
// enters through the persistent-approval path above.
func (m *Manager) CloneRepository(ctx context.Context, req CloneRepositoryRequest) (CloneRepositoryResult, error) {
	return m.cloneRepository(ctx, req)
}

func (m *Manager) cloneRepositoryToSource(ctx context.Context, req CloneRepositoryRequest) (CloneRepositoryResult, error) {
	if err := contextErr(ctx); err != nil {
		return CloneRepositoryResult{}, err
	}
	fullName, ok := parseGitHubFullName(req.Repository)
	if !ok {
		return CloneRepositoryResult{}, errors.New("repository must be a GitHub owner/name or github.com URL")
	}
	if !repositoryAllowedBySet(fullName, m.allowedRepositorySet()) {
		return CloneRepositoryResult{}, fmt.Errorf("GitHub repository is not allowed by settings: %s", fullName)
	}
	owner, repoName, _ := strings.Cut(fullName, "/")
	sourceID := sources.GitHubSourceID(fullName)
	repoURL := sources.GitHubCloneURL(fullName)
	worktree, err := m.sources.GitHubWorktreePath(sourceID)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	branch, err := safeFlagValue("branch", req.Branch, 200)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	sparsePaths, err := normalizeSparsePaths(req.SparsePaths)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	depth := req.Depth
	if depth < 0 {
		return CloneRepositoryResult{}, errors.New("depth must be positive")
	}
	if depth > 100000 {
		depth = 100000
	}
	token, err := m.githubSecrets(ctx)
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	timeout := boundedWriteTimeout(req.TimeoutSeconds, defaultCloneTimeout)
	output := ""
	if hasGitMetadata(worktree) {
		fetchRes, fetchErr := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, worktree, "-C", worktree, "fetch", "--prune")
		if fetchErr != nil {
			return CloneRepositoryResult{}, gitCommandError(fetchRes, fetchErr, token.OAuthAccessToken)
		}
		pullRes, pullErr := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, worktree, "-C", worktree, "pull", "--ff-only")
		if pullErr != nil {
			return CloneRepositoryResult{}, gitCommandError(pullRes, pullErr, token.OAuthAccessToken)
		}
		output = strings.TrimSpace(strings.Join([]string{fetchRes.Stdout, fetchRes.Stderr, pullRes.Stdout, pullRes.Stderr}, "\n"))
	} else {
		if entries, readErr := os.ReadDir(worktree); readErr == nil && len(entries) > 0 {
			return CloneRepositoryResult{}, fmt.Errorf("GitHub source worktree exists but is not a git repository: %s", worktree)
		}
		if err := os.RemoveAll(worktree); err != nil {
			return CloneRepositoryResult{}, err
		}
		tmpParent, err := os.MkdirTemp(filepath.Dir(worktree), ".clone-*")
		if err != nil {
			return CloneRepositoryResult{}, err
		}
		defer os.RemoveAll(tmpParent)
		cloneDir := filepath.Join(tmpParent, "repo")
		gitArgs := []string{
			"-c", "filter.lfs.smudge=",
			"-c", "filter.lfs.process=",
			"-c", "filter.lfs.required=false",
			"clone", "--progress",
		}
		if req.PartialClone || len(sparsePaths) > 0 {
			gitArgs = append(gitArgs, "--filter=blob:none")
		}
		if len(sparsePaths) > 0 {
			gitArgs = append(gitArgs, "--sparse")
		}
		if depth > 0 {
			gitArgs = append(gitArgs, "--depth", strconv.Itoa(depth))
		}
		if branch != "" {
			gitArgs = append(gitArgs, "--branch", branch)
		}
		gitArgs = append(gitArgs, "--", repoURL, cloneDir)
		cloneRes, err := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, tmpParent, gitArgs...)
		if err != nil {
			return CloneRepositoryResult{}, gitCommandError(cloneRes, err, token.OAuthAccessToken)
		}
		output = strings.TrimSpace(strings.Join([]string{cloneRes.Stdout, cloneRes.Stderr}, "\n"))
		if len(sparsePaths) > 0 {
			sparseArgs := append([]string{"-C", cloneDir, "sparse-checkout", "set", "--"}, sparsePaths...)
			sparseRes, err := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, cloneDir, sparseArgs...)
			if err != nil {
				return CloneRepositoryResult{}, gitCommandError(sparseRes, err, token.OAuthAccessToken)
			}
			if sparseOutput := strings.TrimSpace(strings.Join([]string{sparseRes.Stdout, sparseRes.Stderr}, "\n")); sparseOutput != "" {
				if output != "" {
					output += "\n"
				}
				output += sparseOutput
			}
		}
		if err := moveGitHubSourceDirectory(ctx, cloneDir, worktree); err != nil {
			return CloneRepositoryResult{}, err
		}
	}
	head := gitSourceSingleLine(ctx, worktree, "rev-parse", "HEAD")
	currentBranch := gitSourceSingleLine(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if currentBranch == "HEAD" {
		currentBranch = ""
	}
	_, err = m.sources.Upsert(ctx, sources.Source{
		ID:          sourceID,
		Kind:        sources.KindGitHubClone,
		Label:       "GitHub repository",
		Title:       fullName,
		Description: "App-managed local clone",
		Repository:  fullName,
		Owner:       owner,
		Repo:        repoName,
		CloneURL:    repoURL,
		Worktree:    worktree,
		Branch:      currentBranch,
		Head:        head,
		Capabilities: []string{
			"read",
			"search",
			"preview",
			"refresh",
			"git_history",
		},
		Metadata: map[string]any{
			"partial_clone":   req.PartialClone || len(sparsePaths) > 0,
			"sparse_checkout": len(sparsePaths) > 0,
			"sparse_paths":    sparsePaths,
			"depth":           depth,
		},
	})
	if err != nil {
		return CloneRepositoryResult{}, err
	}
	return CloneRepositoryResult{
		SourceID:       sourceID,
		SourceKind:     sources.KindGitHubClone,
		Repository:     fullName,
		URL:            repoURL,
		AddedPath:      fullName,
		Worktree:       worktree,
		PartialClone:   req.PartialClone || len(sparsePaths) > 0,
		SparseCheckout: len(sparsePaths) > 0,
		Output:         truncateText(output, 4000),
	}, nil
}

func (m *Manager) refreshRepositories(ctx context.Context, req RefreshRepositoryRequest) (RefreshRepositoryResult, error) {
	if err := contextErr(ctx); err != nil {
		return RefreshRepositoryResult{}, err
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		if m != nil && m.sources != nil {
			return m.refreshSourceRepositories(ctx, req)
		}
		return RefreshRepositoryResult{}, errors.New("project_id is required")
	}
	if m == nil || m.projects == nil {
		return RefreshRepositoryResult{}, errors.New("project store is not configured")
	}
	filesDir, err := m.projects.FilesDir(projectID)
	if err != nil {
		return RefreshRepositoryResult{}, err
	}
	selector := repoSelector(req.RepoPath, req.Repository)
	var repos []Repository
	if strings.TrimSpace(selector) != "" {
		repo, err := m.resolveRepository(ctx, projectID, selector)
		if err != nil {
			return RefreshRepositoryResult{}, err
		}
		repos = []Repository{repo}
	} else {
		repos, err = m.ListRepositories(ctx, projectID)
		if err != nil {
			return RefreshRepositoryResult{}, err
		}
	}
	if len(repos) == 0 {
		return RefreshRepositoryResult{
			ProjectID: projectID,
			Repositories: []RepositoryRefreshResult{{
				Path:   ".",
				Status: "skipped",
				Detail: "No visible GitHub repositories are attached to this project or allowed by GitHub MCP settings.",
			}},
		}, nil
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].RepoPath < repos[j].RepoPath })
	token, err := m.githubSecrets(ctx)
	if err != nil {
		return RefreshRepositoryResult{}, err
	}
	timeout := boundedWriteTimeout(req.TimeoutSeconds, defaultPullTimeout)
	results := make([]RepositoryRefreshResult, 0, len(repos))
	var firstErr error
	for _, repo := range repos {
		repoDir := filepath.Clean(filepath.Join(filesDir, filepath.FromSlash(repo.RepoPath)))
		if !isProjectSubpath(filepath.Clean(filesDir), repoDir) {
			return RefreshRepositoryResult{}, fmt.Errorf("repository path escapes project root: %s", repo.RepoPath)
		}
		fetchRes, fetchErr := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, repoDir, "-C", repoDir, "fetch", "--prune")
		if fetchErr != nil {
			err := gitCommandError(fetchRes, fetchErr, token.OAuthAccessToken)
			results = append(results, RepositoryRefreshResult{Path: repo.RepoPath, Status: "error", Detail: err.Error()})
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", repo.RepoPath, err)
			}
			if !req.BestEffort {
				break
			}
			continue
		}
		pullRes, pullErr := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, repoDir, "-C", repoDir, "pull", "--ff-only")
		if pullErr != nil {
			err := gitCommandError(pullRes, pullErr, token.OAuthAccessToken)
			results = append(results, RepositoryRefreshResult{Path: repo.RepoPath, Status: "error", Detail: err.Error()})
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", repo.RepoPath, err)
			}
			if !req.BestEffort {
				break
			}
			continue
		}
		detail := strings.TrimSpace(strings.Join([]string{pullRes.Stdout, pullRes.Stderr}, "\n"))
		if detail == "" {
			detail = "Already up to date."
		}
		results = append(results, RepositoryRefreshResult{Path: repo.RepoPath, Status: "updated", Detail: truncateText(detail, 4000)})
	}
	refreshed := false
	for _, result := range results {
		if result.Status == "updated" {
			refreshed = true
			break
		}
	}
	var project any
	if m.refreshProjectIndex != nil {
		project, err = m.refreshProjectIndex(ctx, projectID)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	out := RefreshRepositoryResult{
		ProjectID:    projectID,
		Repositories: results,
		Refreshed:    refreshed,
		Project:      project,
	}
	if firstErr != nil && !req.BestEffort {
		return out, firstErr
	}
	return out, nil
}

func (m *Manager) refreshSourceRepositories(ctx context.Context, req RefreshRepositoryRequest) (RefreshRepositoryResult, error) {
	if m == nil || m.sources == nil {
		return RefreshRepositoryResult{}, errors.New("source store is not configured")
	}
	selector := repoSelector(req.RepoPath, req.Repository)
	var repos []Repository
	var err error
	if strings.TrimSpace(selector) != "" {
		repo, err := m.resolveRepository(ctx, "", selector)
		if err != nil {
			return RefreshRepositoryResult{}, err
		}
		repos = []Repository{repo}
	} else {
		repos, err = m.ListRepositories(ctx, "")
		if err != nil {
			return RefreshRepositoryResult{}, err
		}
	}
	if len(repos) == 0 {
		return RefreshRepositoryResult{
			SourceKind: sources.KindGitHubClone,
			Repositories: []RepositoryRefreshResult{{
				Path:   ".",
				Status: "skipped",
				Detail: "No app-managed GitHub repository sources are available or allowed by GitHub settings.",
			}},
		}, nil
	}
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].FullName != repos[j].FullName {
			return repos[i].FullName < repos[j].FullName
		}
		return repos[i].SourceID < repos[j].SourceID
	})
	token, err := m.githubSecrets(ctx)
	if err != nil {
		return RefreshRepositoryResult{}, err
	}
	timeout := boundedWriteTimeout(req.TimeoutSeconds, defaultPullTimeout)
	results := make([]RepositoryRefreshResult, 0, len(repos))
	var firstErr error
	for _, repo := range repos {
		sourceID := strings.TrimSpace(repo.SourceID)
		worktree := filepath.Clean(strings.TrimSpace(repo.Worktree))
		result := RepositoryRefreshResult{
			Path:       repo.RepoPath,
			SourceID:   sourceID,
			Repository: repo.FullName,
			Worktree:   worktree,
		}
		if sourceID == "" || worktree == "." || !filepath.IsAbs(worktree) || !hasGitMetadata(worktree) {
			err := fmt.Errorf("GitHub source %q is not a usable local git worktree", repo.FullName)
			result.Status = "error"
			result.Detail = err.Error()
			results = append(results, result)
			if firstErr == nil {
				firstErr = err
			}
			if !req.BestEffort {
				break
			}
			continue
		}
		fetchRes, fetchErr := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, worktree, "-C", worktree, "fetch", "--prune")
		if fetchErr != nil {
			err := gitCommandError(fetchRes, fetchErr, token.OAuthAccessToken)
			result.Status = "error"
			result.Detail = err.Error()
			results = append(results, result)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", repo.FullName, err)
			}
			if !req.BestEffort {
				break
			}
			continue
		}
		pullRes, pullErr := m.runGitInDir(ctx, token.OAuthAccessToken, timeout, worktree, "-C", worktree, "pull", "--ff-only")
		if pullErr != nil {
			err := gitCommandError(pullRes, pullErr, token.OAuthAccessToken)
			result.Status = "error"
			result.Detail = err.Error()
			results = append(results, result)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", repo.FullName, err)
			}
			if !req.BestEffort {
				break
			}
			continue
		}
		detail := strings.TrimSpace(strings.Join([]string{pullRes.Stdout, pullRes.Stderr}, "\n"))
		if detail == "" {
			detail = "Already up to date."
		}
		result.Status = "updated"
		result.Detail = truncateText(detail, 4000)
		results = append(results, result)
		m.updateGitHubSourceRevision(ctx, sourceID, worktree)
	}
	refreshed := false
	for _, result := range results {
		if result.Status == "updated" {
			refreshed = true
			break
		}
	}
	out := RefreshRepositoryResult{
		SourceKind:   sources.KindGitHubClone,
		Repositories: results,
		Refreshed:    refreshed,
	}
	if firstErr != nil && !req.BestEffort {
		return out, firstErr
	}
	return out, nil
}

func (m *Manager) updateGitHubSourceRevision(ctx context.Context, sourceID string, worktree string) {
	sourceID = strings.TrimSpace(sourceID)
	if m == nil || m.sources == nil || sourceID == "" {
		return
	}
	src, err := m.sources.Get(ctx, sourceID)
	if err != nil {
		return
	}
	if strings.TrimSpace(worktree) == "" {
		worktree = src.Worktree
	}
	if strings.TrimSpace(worktree) == "" || !hasGitMetadata(worktree) {
		return
	}
	src.Head = gitSourceSingleLine(ctx, worktree, "rev-parse", "HEAD")
	src.Branch = gitSourceSingleLine(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD")
	if src.Branch == "HEAD" {
		src.Branch = ""
	}
	_, _ = m.sources.Upsert(ctx, src)
}

func (m *Manager) runGit(ctx context.Context, token string, timeout time.Duration, args ...string) (RunResult, error) {
	return m.runGitInDir(ctx, token, timeout, "", args...)
}

func (m *Manager) runGitInDir(ctx context.Context, token string, timeout time.Duration, workDir string, args ...string) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args = append([]string(nil), args...)
	dir, err := m.gitWorkingDir(workDir)
	if err != nil {
		return RunResult{Args: args, ExitCode: -1}, err
	}
	authEnv, cleanup, err := gitAuthEnv(token)
	if err != nil {
		return RunResult{Args: args, ExitCode: -1}, err
	}
	defer cleanup()
	if len(authEnv) > 0 {
		prefix := []string{"-c", "credential.helper="}
		if hooksPath := gitDisabledHooksPath(); hooksPath != "" {
			prefix = append(prefix, "-c", "core.hooksPath="+hooksPath)
		}
		args = append(prefix, args...)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := localexec.CommandContext(runCtx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	if len(authEnv) > 0 {
		cmd.Env = append(os.Environ(), authEnv...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{Args: args, ExitCode: -1}, redactError(err, token)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{Args: args, ExitCode: -1}, redactError(err, token)
	}
	if err := cmd.Start(); err != nil {
		return RunResult{Args: args, ExitCode: -1}, redactError(err, token)
	}
	type readResult struct {
		text      string
		truncated bool
		err       error
	}
	stdoutCh := make(chan readResult, 1)
	stderrCh := make(chan readResult, 1)
	go func() {
		text, truncated, err := readLimitedAndDrain(stdout, defaultStdoutCap)
		stdoutCh <- readResult{text: text, truncated: truncated, err: err}
	}()
	go func() {
		text, truncated, err := readLimitedAndDrain(stderr, defaultStderrCap)
		stderrCh <- readResult{text: text, truncated: truncated, err: err}
	}()
	waitErr := cmd.Wait()
	stdoutRes := <-stdoutCh
	stderrRes := <-stderrCh
	if stdoutRes.err != nil && waitErr == nil {
		waitErr = stdoutRes.err
	}
	if stderrRes.err != nil && waitErr == nil {
		waitErr = stderrRes.err
	}
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	if runCtx.Err() != nil && waitErr == nil {
		waitErr = runCtx.Err()
	}
	exitCode := 0
	if waitErr != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	res := RunResult{
		Args:            args,
		Stdout:          stdoutRes.text,
		Stderr:          stderrRes.text,
		StdoutTruncated: stdoutRes.truncated,
		StderrTruncated: stderrRes.truncated,
		ExitCode:        exitCode,
		TimedOut:        timedOut,
	}
	return redactRunResult(res, token), redactError(waitErr, token)
}

func (m *Manager) gitWorkingDir(preferred string) (string, error) {
	candidates := []string{strings.TrimSpace(preferred)}
	if m != nil && m.projects != nil {
		candidates = append(candidates, m.projects.BaseDir())
	}
	candidates = append(candidates, os.TempDir())

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

func gitAuthEnv(token string) ([]string, func(), error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, func() {}, nil
	}
	tmpDir, err := os.MkdirTemp("", "ai-analyst-git-askpass-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	askpassPath, err := writeGitAskpass(tmpDir)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=" + askpassPath,
		"GCM_INTERACTIVE=never",
		"AIA_GITHUB_TOKEN=" + token,
	}, cleanup, nil
}

func gitCommandError(res RunResult, err error, token string) error {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if res.TimedOut {
		if msg == "" {
			msg = "git command timed out"
		} else {
			msg += "\n\ngit command timed out"
		}
	}
	if msg == "" {
		msg = "git command failed"
	}
	msg = redactText(msg, token)
	return &CommandError{
		Args:     append([]string(nil), res.Args...),
		ExitCode: res.ExitCode,
		TimedOut: res.TimedOut,
		Stdout:   redactText(res.Stdout, token),
		Stderr:   redactText(res.Stderr, token),
		Message:  msg,
	}
}

func decodeApprovalArgs(args map[string]any, out any) error {
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	return dec.Decode(out)
}

func boundedWriteTimeout(seconds int, fallback time.Duration) time.Duration {
	if fallback <= 0 {
		fallback = defaultRawGHTimeout
	}
	if seconds <= 0 {
		return fallback
	}
	d := time.Duration(seconds) * time.Second
	if d > maxWriteTimeout {
		return maxWriteTimeout
	}
	if d < time.Second {
		return time.Second
	}
	return d
}

func boundedReadRawGHTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultCommandTimeout
	}
	d := time.Duration(seconds) * time.Second
	if d > maxReadRawGHTimeout {
		return maxReadRawGHTimeout
	}
	if d < time.Second {
		return time.Second
	}
	return d
}

func normalizeSparsePaths(paths []string) ([]string, error) {
	out := []string{}
	seen := map[string]struct{}{}
	for _, raw := range paths {
		value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
		if value == "" {
			continue
		}
		if len(value) > 500 || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid sparse path: %s", value)
		}
		clean := filepath.ToSlash(filepath.Clean(value))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
			return nil, fmt.Errorf("invalid sparse path: %s", value)
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out, nil
}

func uniqueGitHubSourcePath(parent string, preferred string) (string, string, error) {
	base := sanitizeGitHubSourceName(preferred)
	for i := 0; i < 1000; i++ {
		name := base
		if i > 0 {
			name = fmt.Sprintf("%s-%d", base, i+1)
		}
		p := filepath.Join(parent, name)
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return name, p, nil
		} else if err != nil {
			return "", "", err
		}
	}
	return "", "", errors.New("could not choose a destination directory")
}

func sanitizeGitHubSourceName(name string) string {
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

func moveGitHubSourceDirectory(ctx context.Context, src string, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	} else {
		if copyErr := copyGitHubSourceDirectory(ctx, src, dst); copyErr != nil {
			_ = os.RemoveAll(dst)
			return fmt.Errorf("move directory failed: rename: %v; copy fallback: %w", err, copyErr)
		}
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("remove temporary source after copy: %w", err)
	}
	return nil
}

func copyGitHubSourceDirectory(ctx context.Context, src string, dst string) error {
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
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		target := filepath.Join(dst, rel)
		if d.Type()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			return os.Symlink(linkTarget, target)
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		info, err := d.Info()
		if err != nil || info == nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return copyFileWithMode(p, target, info.Mode().Perm())
	})
}

func copyFileWithMode(src string, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
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

func windowsSafeSingleDirName(name string) bool {
	base := strings.TrimSpace(name)
	if base == "" || strings.TrimRight(base, ". ") != base {
		return false
	}
	if projectSourceNameRe.MatchString(base) {
		return false
	}
	return !windowsReservedDeviceName(windowsPathStem(base))
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

func gitSourceSingleLine(ctx context.Context, repoDir string, args ...string) string {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return ""
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := localexec.CommandContext(runCtx, "git", append([]string{"-C", repoDir}, args...)...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func truncateText(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || len(text) <= max {
		return text
	}
	return text[:max] + "\n... output truncated ..."
}

var blockedRawGHCommands = map[string]string{
	"alias":      "gh alias can hide or rewrite future commands",
	"auth":       "gh auth commands can expose or alter credentials",
	"completion": "gh completion is local shell configuration",
	"config":     "gh config can expose or alter local client configuration",
	"codespace":  "gh codespace can open remote shells and port forwards",
	"extension":  "gh extension can install and execute third-party code",
	"gpg-key":    "gh gpg-key manages account credentials",
	"ssh-key":    "gh ssh-key manages account credentials",
}

func ValidateRawGHArgs(input []string) ([]string, error) {
	args := make([]string, 0, len(input))
	for _, raw := range input {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if len(value) > 4000 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, errors.New("gh arguments must be finite strings without control characters")
		}
		args = append(args, value)
	}
	if len(args) > 0 && strings.EqualFold(args[0], "gh") {
		args = args[1:]
	}
	args = canonicalRawGHArgs(args)
	if len(args) == 0 {
		return nil, errors.New("gh command arguments are required")
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	if strings.HasPrefix(command, "-") {
		return nil, errors.New("the first gh argument must be a command, not a flag")
	}
	if reason, blocked := blockedRawGHCommands[command]; blocked {
		return nil, fmt.Errorf("blocked gh %s: %s", command, reason)
	}
	for _, arg := range args {
		if strings.EqualFold(arg, "--show-token") {
			return nil, errors.New("blocked gh flag: --show-token")
		}
	}
	return args, nil
}

func RawGHCommandIsReadOnly(args []string) bool {
	args = canonicalRawGHArgs(args)
	if len(args) == 0 {
		return false
	}
	if rawGHReadCommandHasBlockedFlag(args[1:]) {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	if command == "status" {
		return true
	}
	if command == "api" {
		return rawGHAPICommandIsReadOnly(args[1:])
	}
	if len(args) < 2 {
		return false
	}
	subcommand := strings.ToLower(strings.TrimSpace(args[1]))
	if command == "search" {
		return stringSetContains(rawGHReadOnlySearchSubcommands, subcommand)
	}
	if allowed, ok := rawGHReadOnlySubcommands[command]; ok {
		return stringSetContains(allowed, subcommand)
	}
	return false
}

func rawGHAPICommandIsReadOnly(args []string) bool {
	method := ""
	hasFields := false
	endpoint := ""
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		lower := strings.ToLower(arg)
		switch {
		case lower == "--method" || lower == "-x":
			if i+1 >= len(args) {
				return false
			}
			i++
			method = strings.ToUpper(strings.TrimSpace(args[i]))
		case strings.HasPrefix(lower, "--method="):
			method = strings.ToUpper(strings.TrimSpace(arg[len("--method="):]))
		case strings.HasPrefix(lower, "-x") && len(arg) > 2:
			method = strings.ToUpper(strings.TrimSpace(arg[2:]))
		case lower == "--field" || lower == "--raw-field" || lower == "-f":
			if i+1 >= len(args) {
				return false
			}
			hasFields = true
			i++
		case strings.HasPrefix(lower, "--field=") || strings.HasPrefix(lower, "--raw-field="):
			hasFields = true
		case strings.HasPrefix(lower, "-f") && len(arg) > 2:
			hasFields = true
		case lower == "--input":
			return false
		case strings.HasPrefix(lower, "--input="):
			return false
		case rawGHAPIFlagConsumesValue(lower):
			if i+1 >= len(args) {
				return false
			}
			i++
		case strings.HasPrefix(arg, "-"):
			continue
		case endpoint == "":
			endpoint = strings.TrimSpace(arg)
		default:
			return false
		}
	}
	if endpoint == "" || strings.EqualFold(endpoint, "graphql") {
		return false
	}
	if method == "" && hasFields {
		return false
	}
	if method == "" {
		method = "GET"
	}
	return method == "GET" || method == "HEAD"
}

func rawGHAPIFlagConsumesValue(flag string) bool {
	switch {
	case flag == "--header" || flag == "-h":
		return true
	case strings.HasPrefix(flag, "--header="):
		return false
	case strings.HasPrefix(flag, "-h") && len(flag) > 2:
		return false
	case flag == "--hostname":
		return true
	case strings.HasPrefix(flag, "--hostname="):
		return false
	case flag == "--jq" || flag == "-q":
		return true
	case strings.HasPrefix(flag, "--jq="):
		return false
	case strings.HasPrefix(flag, "-q") && len(flag) > 2:
		return false
	case flag == "--preview" || flag == "-p":
		return true
	case strings.HasPrefix(flag, "--preview="):
		return false
	case strings.HasPrefix(flag, "-p") && len(flag) > 2:
		return false
	case flag == "--template" || flag == "-t":
		return true
	case strings.HasPrefix(flag, "--template="):
		return false
	case strings.HasPrefix(flag, "-t") && len(flag) > 2:
		return false
	case flag == "--cache":
		return true
	case strings.HasPrefix(flag, "--cache="):
		return false
	default:
		return false
	}
}

func canonicalRawGHArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	switch command {
	case "search_code":
		return append([]string{"search", "code"}, args[1:]...)
	case "search_commits":
		return append([]string{"search", "commits"}, args[1:]...)
	case "search_issues":
		return append([]string{"search", "issues"}, args[1:]...)
	case "search_prs":
		return append([]string{"search", "prs"}, args[1:]...)
	case "search_repos":
		return append([]string{"search", "repos"}, args[1:]...)
	case "rs":
		return append([]string{"ruleset"}, args[1:]...)
	default:
		return args
	}
}

var rawGHReadOnlySearchSubcommands = map[string]struct{}{
	"code":    {},
	"commits": {},
	"issues":  {},
	"prs":     {},
	"repos":   {},
}

var rawGHReadOnlySubcommands = map[string]map[string]struct{}{
	"cache": {
		"list": {},
	},
	"gist": {
		"list": {},
		"view": {},
	},
	"issue": {
		"list":   {},
		"status": {},
		"view":   {},
	},
	"label": {
		"list": {},
	},
	"org": {
		"list": {},
	},
	"pr": {
		"checks": {},
		"diff":   {},
		"list":   {},
		"status": {},
		"view":   {},
	},
	"project": {
		"field-list": {},
		"item-list":  {},
		"list":       {},
		"view":       {},
	},
	"release": {
		"list": {},
		"view": {},
	},
	"repo": {
		"gitignore": {},
		"license":   {},
		"list":      {},
		"view":      {},
	},
	"ruleset": {
		"check": {},
		"list":  {},
		"view":  {},
	},
	"run": {
		"list":  {},
		"view":  {},
		"watch": {},
	},
	"workflow": {
		"list": {},
		"view": {},
	},
}

func stringSetContains(values map[string]struct{}, value string) bool {
	_, ok := values[strings.ToLower(strings.TrimSpace(value))]
	return ok
}

func rawGHReadCommandHasBlockedFlag(args []string) bool {
	for _, arg := range args {
		flag := strings.ToLower(strings.TrimSpace(arg))
		if flag == "--web" || flag == "-w" || strings.HasPrefix(flag, "--web=") {
			return true
		}
	}
	return false
}

func rawGHCommandIsRepoClone(args []string) bool {
	return len(args) >= 3 && strings.EqualFold(args[0], "repo") && strings.EqualFold(args[1], "clone")
}

func validateRawGHRepoCloneTarget(args []string) error {
	if !rawGHCommandIsRepoClone(args) {
		return nil
	}
	positional := []string{}
	afterDashDash := false
	for _, arg := range args[2:] {
		if afterDashDash {
			continue
		}
		if arg == "--" {
			afterDashDash = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) == 0 {
		return errors.New("gh repo clone requires a repository")
	}
	if _, ok := parseGitHubFullName(positional[0]); !ok {
		return errors.New("gh repo clone is limited to GitHub owner/name or github.com URL repositories")
	}
	if len(positional) < 2 {
		return nil
	}
	target := strings.TrimSpace(strings.ReplaceAll(positional[1], "\\", "/"))
	if target == "" {
		return nil
	}
	if strings.ContainsAny(target, "\x00\r\n") || strings.HasPrefix(target, "/") || strings.Contains(target, "/") || strings.Contains(target, "..") || !windowsSafeSingleDirName(target) {
		return errors.New("gh repo clone target must be a single safe directory name")
	}
	return nil
}

func (m *Manager) validateRawGHAllowedRepositories(args []string) error {
	allowed := m.allowedRepositorySet()
	if len(allowed) == 0 {
		return nil
	}
	selectors := rawGHRepositorySelectors(args)
	if len(selectors) == 0 {
		return errors.New("raw gh commands require an explicit allowed repository selector when allowed repositories are configured")
	}
	for _, selector := range selectors {
		fullName, ok := parseGitHubFullName(selector)
		if !ok {
			return fmt.Errorf("invalid GitHub repository selector: %s", selector)
		}
		if !repositoryAllowedBySet(fullName, allowed) {
			return fmt.Errorf("GitHub repository is not allowed by settings: %s", fullName)
		}
	}
	return nil
}

func rawGHRepositorySelectors(args []string) []string {
	selectors := []string{}
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		lower := strings.ToLower(arg)
		switch {
		case lower == "--repo" || lower == "-r":
			if i+1 < len(args) {
				selectors = append(selectors, args[i+1])
				i++
			}
		case strings.HasPrefix(lower, "--repo="):
			selectors = append(selectors, strings.TrimSpace(arg[len("--repo="):]))
		}
	}
	if rawGHCommandIsRepoClone(args) {
		for _, arg := range args[2:] {
			if arg == "--" {
				break
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			selectors = append(selectors, arg)
			break
		}
	} else if len(args) >= 2 && strings.EqualFold(args[0], "api") {
		if selector := rawGHAPIRepositorySelector(args[1:]); selector != "" {
			selectors = append(selectors, selector)
		}
	} else if len(args) >= 3 && strings.EqualFold(args[0], "repo") {
		for _, arg := range args[2:] {
			if arg == "--" {
				break
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if _, ok := parseGitHubFullName(arg); ok {
				selectors = append(selectors, arg)
			}
			break
		}
	}
	return selectors
}

func rawGHAPIRepositorySelector(args []string) string {
	endpoint := rawGHAPIEndpoint(args)
	endpoint, _, _ = strings.Cut(endpoint, "?")
	endpoint = strings.Trim(strings.TrimSpace(endpoint), "/")
	lowerEndpoint := strings.ToLower(endpoint)
	switch {
	case strings.HasPrefix(lowerEndpoint, "https://api.github.com/"):
		endpoint = endpoint[len("https://api.github.com/"):]
	case strings.HasPrefix(lowerEndpoint, "http://api.github.com/"):
		endpoint = endpoint[len("http://api.github.com/"):]
	case strings.HasPrefix(lowerEndpoint, "api.github.com/"):
		endpoint = endpoint[len("api.github.com/"):]
	}
	parts := strings.Split(endpoint, "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "repos") {
		return ""
	}
	owner := strings.TrimSpace(parts[1])
	repo := strings.TrimSpace(parts[2])
	if owner == "" || repo == "" {
		return ""
	}
	return owner + "/" + repo
}

func rawGHAPIEndpoint(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		lower := strings.ToLower(arg)
		switch {
		case lower == "--method" || lower == "-x" || lower == "--field" || lower == "--raw-field" || lower == "-f" || lower == "--input":
			if i+1 >= len(args) {
				return ""
			}
			i++
		case strings.HasPrefix(lower, "--method=") || strings.HasPrefix(lower, "--field=") || strings.HasPrefix(lower, "--raw-field=") || strings.HasPrefix(lower, "--input="):
			continue
		case strings.HasPrefix(lower, "-x") && len(arg) > 2:
			continue
		case strings.HasPrefix(lower, "-f") && len(arg) > 2:
			continue
		case rawGHAPIFlagConsumesValue(lower):
			if i+1 >= len(args) {
				return ""
			}
			i++
		case strings.HasPrefix(arg, "-"):
			continue
		default:
			return arg
		}
	}
	return ""
}
