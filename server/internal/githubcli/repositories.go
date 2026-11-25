package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ai-analyst-local/internal/sources"
)

var githubOwnerRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (m *Manager) ListRepositories(ctx context.Context, projectID string) ([]Repository, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	projectID = strings.TrimSpace(projectID)
	repositories := []Repository{}
	seen := map[string]struct{}{}
	add := func(repo Repository) {
		if repo.FullName == "" {
			return
		}
		key := strings.ToLower(repo.FullName)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		repositories = append(repositories, repo)
	}
	sourceStoreConfigured := m != nil && m.sources != nil
	if sourceStoreConfigured {
		sourceRepos, err := m.listSourceRepositories(ctx)
		if err != nil {
			return nil, err
		}
		for _, repo := range sourceRepos {
			add(repo)
		}
	}
	if projectID == "" {
		if sourceStoreConfigured {
			sortRepositories(repositories)
			return repositories, nil
		}
		return nil, errors.New("project_id is required")
	}
	if m != nil && m.projects != nil {
		projectRepos, err := m.listProjectRepositories(ctx, projectID)
		if err != nil {
			return nil, err
		}
		for _, repo := range projectRepos {
			add(repo)
		}
	}
	if len(repositories) > 0 {
		sortRepositories(repositories)
		return repositories, nil
	}
	if m == nil || m.projects == nil {
		return nil, errors.New("project store is not configured")
	}
	return []Repository{}, nil
}

func (m *Manager) listProjectRepositories(ctx context.Context, projectID string) ([]Repository, error) {
	root, err := m.projects.FilesDir(projectID)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	sources, err := m.projects.ListSources(ctx, projectID)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return []Repository{}, nil
		}
		return nil, err
	}
	visibleSources := map[string]struct{}{}
	for _, source := range sources {
		name := strings.TrimSpace(filepath.ToSlash(source.Name))
		if name == "" || strings.Contains(name, "/") || source.Hidden {
			continue
		}
		visibleSources[name] = struct{}{}
	}
	allowed := m.allowedRepositorySet()
	repositories := []Repository{}
	seen := map[string]struct{}{}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return fs.SkipDir
		}
		if p != root {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			first, _, _ := strings.Cut(relSlash, "/")
			if _, ok := visibleSources[first]; !ok {
				return fs.SkipDir
			}
		}
		if !hasGitMetadata(p) {
			return nil
		}
		repo, ok := m.projectRepositoryFromWorktree(projectID, root, p)
		if !ok || repo.FullName == "" {
			return nil
		}
		if !repositoryAllowedBySet(repo.FullName, allowed) {
			return nil
		}
		key := strings.ToLower(repo.RepoPath + "\x00" + repo.FullName)
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		repositories = append(repositories, repo)
		return fs.SkipDir
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) || os.IsNotExist(walkErr) {
			return []Repository{}, nil
		}
		return nil, walkErr
	}
	sortRepositories(repositories)
	return repositories, nil
}

func (m *Manager) listSourceRepositories(ctx context.Context) ([]Repository, error) {
	if m == nil || m.sources == nil {
		return nil, errors.New("source store is not configured")
	}
	rows, err := m.sources.List(ctx, sources.ListOptions{Kind: sources.KindGitHubClone})
	if err != nil {
		return nil, err
	}
	allowed := m.allowedRepositorySet()
	repositories := make([]Repository, 0, len(rows))
	for _, src := range rows {
		repo := m.sourceRepositoryFromSource(ctx, src)
		if repo.FullName == "" || !repositoryAllowedBySet(repo.FullName, allowed) {
			continue
		}
		repositories = append(repositories, repo)
	}
	sort.Slice(repositories, func(i, j int) bool {
		return repositories[i].FullName < repositories[j].FullName
	})
	return repositories, nil
}

func sortRepositories(repositories []Repository) {
	sort.Slice(repositories, func(i, j int) bool {
		if repositories[i].SourceKind != repositories[j].SourceKind {
			return repositories[i].SourceKind > repositories[j].SourceKind
		}
		if repositories[i].FullName != repositories[j].FullName {
			return repositories[i].FullName < repositories[j].FullName
		}
		if repositories[i].RepoPath != repositories[j].RepoPath {
			return repositories[i].RepoPath < repositories[j].RepoPath
		}
		return repositories[i].SourceID < repositories[j].SourceID
	})
}

func (m *Manager) sourceRepositoryFromSource(ctx context.Context, src sources.Source) Repository {
	fullName := strings.Trim(strings.TrimSpace(src.Repository), "/")
	if fullName == "" && strings.TrimSpace(src.Owner) != "" && strings.TrimSpace(src.Repo) != "" {
		fullName = strings.TrimSpace(src.Owner) + "/" + strings.TrimSpace(src.Repo)
	}
	owner, repoName, _ := strings.Cut(fullName, "/")
	worktree := strings.TrimSpace(src.Worktree)
	branch := strings.TrimSpace(src.Branch)
	head := strings.TrimSpace(src.Head)
	if worktree != "" && hasGitMetadata(worktree) {
		if branch == "" {
			branch = gitSourceSingleLine(ctx, worktree, "rev-parse", "--abbrev-ref", "HEAD")
			if branch == "HEAD" {
				branch = ""
			}
		}
		if head == "" {
			head = gitSourceSingleLine(ctx, worktree, "rev-parse", "HEAD")
		}
	}
	remoteURL := strings.TrimSpace(src.CloneURL)
	if remoteURL == "" && fullName != "" {
		remoteURL = sources.GitHubCloneURL(fullName)
	}
	remote := GitHubRemote{
		Name:     "origin",
		URL:      strings.TrimSuffix(remoteURL, ".git"),
		Owner:    owner,
		Repo:     repoName,
		FullName: fullName,
		Fetch:    true,
	}
	repoPath := strings.TrimSpace(src.Repository)
	if repoPath == "" {
		repoPath = strings.TrimSpace(src.ID)
	}
	return Repository{
		SourceID:      strings.TrimSpace(src.ID),
		SourceKind:    sources.KindGitHubClone,
		RepoPath:      repoPath,
		Worktree:      worktree,
		Owner:         owner,
		Repo:          repoName,
		FullName:      fullName,
		DefaultRef:    branch,
		Head:          head,
		RemoteName:    "origin",
		RemoteURL:     remote.URL,
		GitHubRemotes: []GitHubRemote{remote},
		Resolvable:    worktree != "" && hasGitMetadata(worktree),
	}
}

func (m *Manager) ListProjectRepositories(ctx context.Context, projectID string) ([]Repository, error) {
	return m.ListRepositories(ctx, projectID)
}

func (m *Manager) ResolveRepository(ctx context.Context, ref ProjectRepoRef) (Repository, error) {
	return m.resolveRepository(ctx, ref.ProjectID, ref.RepoPath)
}

func (m *Manager) ListPullRequests(ctx context.Context, req RepositoryRequest) (PullRequestList, error) {
	repo, token, err := m.repositoryCommandContext(ctx, req.ProjectID, repoSelector(req.RepoPath, req.Repository))
	if err != nil {
		return PullRequestList{}, err
	}
	state, err := normalizeState(req.State, "open", map[string]struct{}{"open": {}, "closed": {}, "merged": {}, "all": {}})
	if err != nil {
		return PullRequestList{}, err
	}
	limit := normalizeLimit(req.Limit, 30, 100)
	var items []map[string]any
	if err := m.runGHJSON(ctx, token, []string{
		"pr", "list", "--repo", repo.FullName, "--state", state, "--limit", numberArg(limit),
		"--json", "number,title,state,url,isDraft,author,labels,assignees,comments,createdAt,updatedAt,closedAt,mergedAt,headRefName,baseRefName,reviewDecision,statusCheckRollup",
	}, &items); err != nil {
		return PullRequestList{}, err
	}
	prs := make([]PullRequest, 0, len(items))
	for _, item := range items {
		prs = append(prs, normalizePullRequest(item))
	}
	return PullRequestList{Repository: repo, PullRequests: prs, TotalCount: len(prs), Limit: limit, Truncated: len(prs) >= limit}, nil
}

func (m *Manager) GetPullRequest(ctx context.Context, req NumberedRepositoryRequest) (PullRequestResult, error) {
	if err := positiveNumber("number", req.Number); err != nil {
		return PullRequestResult{}, err
	}
	repo, token, err := m.repositoryCommandContext(ctx, req.ProjectID, repoSelector(req.RepoPath, req.Repository))
	if err != nil {
		return PullRequestResult{}, err
	}
	var item map[string]any
	if err := m.runGHJSON(ctx, token, []string{
		"pr", "view", numberArg(req.Number), "--repo", repo.FullName,
		"--json", "number,title,state,url,body,isDraft,author,labels,assignees,comments,createdAt,updatedAt,closedAt,mergedAt,headRefName,baseRefName,reviewDecision,statusCheckRollup",
	}, &item); err != nil {
		return PullRequestResult{}, err
	}
	return PullRequestResult{Repository: repo, PullRequest: normalizePullRequest(item)}, nil
}

func (m *Manager) ListIssues(ctx context.Context, req RepositoryRequest) (IssueList, error) {
	repo, token, err := m.repositoryCommandContext(ctx, req.ProjectID, repoSelector(req.RepoPath, req.Repository))
	if err != nil {
		return IssueList{}, err
	}
	state, err := normalizeState(req.State, "open", map[string]struct{}{"open": {}, "closed": {}, "all": {}})
	if err != nil {
		return IssueList{}, err
	}
	limit := normalizeLimit(req.Limit, 30, 100)
	var items []map[string]any
	if err := m.runGHJSON(ctx, token, []string{
		"issue", "list", "--repo", repo.FullName, "--state", state, "--limit", numberArg(limit),
		"--json", "number,title,state,url,author,labels,assignees,comments,createdAt,updatedAt",
	}, &items); err != nil {
		return IssueList{}, err
	}
	issues := make([]Issue, 0, len(items))
	for _, item := range items {
		issues = append(issues, normalizeIssue(item))
	}
	return IssueList{Repository: repo, Issues: issues, TotalCount: len(issues), Limit: limit, Truncated: len(issues) >= limit}, nil
}

func (m *Manager) GetIssue(ctx context.Context, req NumberedRepositoryRequest) (IssueResult, error) {
	if err := positiveNumber("number", req.Number); err != nil {
		return IssueResult{}, err
	}
	repo, token, err := m.repositoryCommandContext(ctx, req.ProjectID, repoSelector(req.RepoPath, req.Repository))
	if err != nil {
		return IssueResult{}, err
	}
	var item map[string]any
	if err := m.runGHJSON(ctx, token, []string{
		"issue", "view", numberArg(req.Number), "--repo", repo.FullName,
		"--json", "number,title,state,url,body,author,labels,assignees,comments,createdAt,updatedAt",
	}, &item); err != nil {
		return IssueResult{}, err
	}
	return IssueResult{Repository: repo, Issue: normalizeIssue(item)}, nil
}

func (m *Manager) ListWorkflowRuns(ctx context.Context, req RepositoryRequest) (WorkflowRunList, error) {
	repo, token, err := m.repositoryCommandContext(ctx, req.ProjectID, repoSelector(req.RepoPath, req.Repository))
	if err != nil {
		return WorkflowRunList{}, err
	}
	limit := normalizeLimit(req.Limit, 30, 100)
	args := []string{
		"run", "list", "--repo", repo.FullName, "--limit", numberArg(limit),
		"--json", "databaseId,workflowDatabaseId,number,attempt,name,workflowName,displayTitle,status,conclusion,event,headBranch,headSha,url,createdAt,updatedAt,startedAt",
	}
	if status, err := safeFlagValue("state", req.State, 80); err != nil {
		return WorkflowRunList{}, err
	} else if status != "" {
		args = append(args, "--status", status)
	}
	var items []map[string]any
	if err := m.runGHJSON(ctx, token, args, &items); err != nil {
		return WorkflowRunList{}, err
	}
	runs := make([]WorkflowRun, 0, len(items))
	for _, item := range items {
		runs = append(runs, normalizeWorkflowRun(item))
	}
	return WorkflowRunList{Repository: repo, Runs: runs, TotalCount: len(runs), Limit: limit, Truncated: len(runs) >= limit}, nil
}

func (m *Manager) GetWorkflowRunJobs(ctx context.Context, req WorkflowRunJobsRequest) (WorkflowRunJobsResult, error) {
	runID, err := safeFlagValue("run_id", req.RunID, 80)
	if err != nil {
		return WorkflowRunJobsResult{}, err
	}
	if runID == "" {
		return WorkflowRunJobsResult{}, errors.New("run_id is required")
	}
	parsedRunID, err := strconv.ParseInt(runID, 10, 64)
	if err != nil || parsedRunID <= 0 {
		return WorkflowRunJobsResult{}, errors.New("run_id must be a positive integer")
	}
	repo, token, err := m.repositoryCommandContext(ctx, req.ProjectID, repoSelector(req.RepoPath, req.Repository))
	if err != nil {
		return WorkflowRunJobsResult{}, err
	}
	var payload map[string]any
	if err := m.runGHJSON(ctx, token, []string{
		"run", "view", runID, "--repo", repo.FullName,
		"--json", "databaseId,displayTitle,status,conclusion,url,jobs",
	}, &payload); err != nil {
		return WorkflowRunJobsResult{}, err
	}
	jobsRaw := objectSlice(payload["jobs"])
	jobs := make([]WorkflowJob, 0, len(jobsRaw))
	for _, item := range jobsRaw {
		jobs = append(jobs, normalizeWorkflowJob(item))
	}
	return WorkflowRunJobsResult{Repository: repo, RunID: parsedRunID, Jobs: jobs}, nil
}

func (m *Manager) repositoryCommandContext(ctx context.Context, projectID string, repository string) (Repository, string, error) {
	repo, err := m.resolveRepository(ctx, projectID, repository)
	if err != nil {
		return Repository{}, "", err
	}
	token, err := m.githubSecrets(ctx)
	if err != nil {
		return Repository{}, "", err
	}
	return repo, token.OAuthAccessToken, nil
}

func repoSelector(repoPath string, repository string) string {
	if strings.TrimSpace(repoPath) != "" {
		return strings.TrimSpace(repoPath)
	}
	return strings.TrimSpace(repository)
}

func (m *Manager) resolveRepository(ctx context.Context, projectID string, repository string) (Repository, error) {
	repos, err := m.ListRepositories(ctx, projectID)
	if err != nil {
		return Repository{}, err
	}
	if len(repos) == 0 {
		return Repository{}, errors.New("no visible GitHub repositories are cloned as app-managed sources, attached to this project, or allowed by GitHub settings")
	}
	selector := strings.TrimSpace(repository)
	if selector == "" {
		if len(repos) == 1 {
			return repos[0], nil
		}
		names := make([]string, 0, len(repos))
		for _, repo := range repos {
			names = append(names, repo.FullName)
		}
		return Repository{}, fmt.Errorf("repository is required when multiple GitHub repositories are available: %s", strings.Join(names, ", "))
	}
	normalizedSelector, selectorIsFullName := normalizeRepositorySelector(selector)
	for _, repo := range repos {
		if strings.EqualFold(repo.SourceID, strings.Trim(selector, "/")) {
			return repo, nil
		}
		if selectorIsFullName && strings.EqualFold(repo.FullName, normalizedSelector) {
			return repo, nil
		}
		if strings.EqualFold(repo.RepoPath, strings.Trim(selector, "/")) {
			return repo, nil
		}
	}
	if selectorIsFullName && !repositoryAllowedBySet(normalizedSelector, m.allowedRepositorySet()) {
		return Repository{}, fmt.Errorf("GitHub repository is not allowed by settings: %s", normalizedSelector)
	}
	return Repository{}, fmt.Errorf("GitHub repository not found in app-managed sources or this project: %s", selector)
}

func (m *Manager) runGHJSON(ctx context.Context, token string, args []string, out any) error {
	res, err := m.runGH(ctx, token, args)
	if err != nil {
		return err
	}
	if err := decodeGHJSON(res.Stdout, out); err != nil {
		return ghJSONError(res, err)
	}
	return nil
}

func (m *Manager) projectRepositoryFromWorktree(projectID string, root string, worktree string) (Repository, bool) {
	rel := "."
	if r, err := filepath.Rel(root, worktree); err == nil && r != "." {
		rel = filepath.ToSlash(r)
	}
	remotes := gitHubRemotesFromConfig(worktree, root)
	if len(remotes) == 0 {
		return Repository{}, false
	}
	remote := preferredGitHubRemote(remotes)
	return Repository{
		ProjectID:     projectID,
		RepoPath:      rel,
		Owner:         remote.Owner,
		Repo:          remote.Repo,
		FullName:      remote.FullName,
		RemoteName:    remote.Name,
		RemoteURL:     remote.URL,
		GitHubRemotes: remotes,
		Resolvable:    true,
	}, true
}

func preferredGitHubRemote(remotes []GitHubRemote) GitHubRemote {
	if len(remotes) == 0 {
		return GitHubRemote{}
	}
	for _, remote := range remotes {
		if remote.Name == "origin" && remote.Fetch {
			return remote
		}
	}
	for _, remote := range remotes {
		if remote.Name == "origin" {
			return remote
		}
	}
	return remotes[0]
}

func hasGitMetadata(dir string) bool {
	st, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && st != nil
}

func gitHubRemotesFromConfig(worktree string, projectRoot string) []GitHubRemote {
	configPath := gitConfigPath(worktree, projectRoot)
	if configPath == "" {
		return nil
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	currentRemote := ""
	remotes := []GitHubRemote{}
	seen := map[string]int{}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentRemote = parseRemoteSectionName(line)
			continue
		}
		if currentRemote == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(strings.ToLower(key)) != "url" {
			continue
		}
		remote, ok := parseGitHubRemoteURL(currentRemote, strings.TrimSpace(value))
		if !ok {
			continue
		}
		remote.Fetch = true
		remoteKey := strings.ToLower(remote.Name + "\x00" + remote.FullName)
		if idx, ok := seen[remoteKey]; ok {
			remotes[idx].Fetch = remotes[idx].Fetch || remote.Fetch
			continue
		}
		seen[remoteKey] = len(remotes)
		remotes = append(remotes, remote)
	}
	sort.Slice(remotes, func(i, j int) bool {
		if remotes[i].Name != remotes[j].Name {
			return remotes[i].Name < remotes[j].Name
		}
		return remotes[i].FullName < remotes[j].FullName
	})
	return remotes
}

func gitConfigPath(worktree string, projectRoot string) string {
	gitPath := filepath.Join(worktree, ".git")
	st, err := os.Lstat(gitPath)
	if err != nil {
		return ""
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	if st.IsDir() {
		return filepath.Join(gitPath, "config")
	}
	if !st.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(line[len("gitdir:"):])
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if !isProjectSubpath(filepath.Clean(projectRoot), gitDir) {
		return ""
	}
	return filepath.Join(gitDir, "config")
}

func parseRemoteSectionName(line string) string {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
	if !strings.HasPrefix(body, `remote `) {
		return ""
	}
	name := strings.TrimSpace(strings.TrimPrefix(body, `remote `))
	name = strings.Trim(name, `"`)
	return name
}

func parseGitHubRemoteURL(name string, raw string) (GitHubRemote, bool) {
	fullName, ok := parseGitHubRemoteFullName(raw)
	if !ok {
		return GitHubRemote{}, false
	}
	owner, repo, _ := strings.Cut(fullName, "/")
	return GitHubRemote{
		Name:     strings.TrimSpace(name),
		URL:      "https://github.com/" + fullName,
		Owner:    owner,
		Repo:     repo,
		FullName: fullName,
	}, true
}

func parseGitHubRemoteFullName(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, ".git")
	if strings.HasPrefix(value, "git@github.com:") {
		return cleanGitHubFullName(strings.TrimPrefix(value, "git@github.com:"))
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil || parsed.Scheme == "" {
		return "", false
	}
	if !strings.EqualFold(parsed.Hostname(), "github.com") {
		return "", false
	}
	return cleanGitHubFullName(parsed.EscapedPath())
}

func parseGitHubFullName(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimSuffix(value, ".git")
	if strings.HasPrefix(value, "git@github.com:") {
		return cleanGitHubFullName(strings.TrimPrefix(value, "git@github.com:"))
	}
	if !strings.Contains(value, "://") {
		return cleanGitHubFullName(value)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Hostname(), "github.com") {
		return "", false
	}
	return cleanGitHubFullName(parsed.EscapedPath())
}

func isProjectSubpath(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	rel = filepath.Clean(rel)
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cleanGitHubFullName(path string) (string, bool) {
	clean := strings.Trim(strings.TrimSpace(path), "/")
	clean = strings.TrimSuffix(clean, ".git")
	parts := strings.Split(clean, "/")
	if len(parts) != 2 {
		return "", false
	}
	fullName := strings.ToLower(parts[0] + "/" + parts[1])
	if !githubOwnerRepoRe.MatchString(fullName) {
		return "", false
	}
	return fullName, true
}

func normalizeRepositorySelector(selector string) (string, bool) {
	fullName, ok := parseGitHubFullName(selector)
	return fullName, ok
}

func (m *Manager) allowedRepositorySet() map[string]struct{} {
	if m == nil || m.settings == nil {
		return nil
	}
	settings := m.settings.Get()
	if settings.GitHub == nil || len(settings.GitHub.AllowedRepositories) == 0 {
		return nil
	}
	allowed := map[string]struct{}{}
	for _, repo := range settings.GitHub.AllowedRepositories {
		fullName, ok := parseGitHubFullName(repo)
		if !ok {
			continue
		}
		allowed[fullName] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil
	}
	return allowed
}

func repositoryAllowedBySet(fullName string, allowed map[string]struct{}) bool {
	if len(allowed) == 0 {
		return true
	}
	_, ok := allowed[strings.ToLower(strings.TrimSpace(fullName))]
	return ok
}

func normalizeAny(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = normalizeAny(item)
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, normalizeAny(item))
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, normalizeAny(item))
		}
		return out
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := v.Float64(); err == nil {
			return f
		}
		return v.String()
	default:
		return value
	}
}
