package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/sources"
)

const (
	defaultGitListLimit   = 50
	maxGitListLimit       = 200
	defaultGitOutputBytes = 64 * 1024
	maxGitOutputBytes     = 256 * 1024
	maxGitCommandBytes    = 512 * 1024
	gitCommandTimeout     = 30 * time.Second
	gitRecordSeparator    = "\x1e"
	gitFieldSeparator     = "\x1f"
	defaultGitRef         = "HEAD"
)

var gitUnsafeRefRe = regexp.MustCompile(`[\x00\r\n\t ]`)

type gitRepository struct {
	ProjectID   string
	ProjectName string
	SourceID    string
	SourceKind  string
	Repository  string
	RelPath     string
	FullDir     string
}

type gitCommandResult struct {
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	Err             error
}

func newListGitRepositoriesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_git_repositories",
				Description: "List visible Git repositories attached to the current project.",
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var noop struct{}
			if err := decodeArgs(args, &noop); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			repos, errOut, ok := resolveGitRepositories(ctx, exec)
			if !ok {
				return errOut, nil
			}
			out := make([]map[string]any, 0, len(repos))
			for _, repo := range repos {
				item := map[string]any{
					"repo_path": repo.RelPath,
				}
				if repo.SourceID != "" {
					item["source_id"] = repo.SourceID
				}
				if repo.SourceKind != "" {
					item["source_kind"] = repo.SourceKind
				}
				if repo.Repository != "" {
					item["repository"] = repo.Repository
				}
				if repo.ProjectID != "" {
					item["project_id"] = repo.ProjectID
				}
				if repo.ProjectName != "" {
					item["project_name"] = repo.ProjectName
				}
				if branch := gitReadSingleLine(ctx, repo.FullDir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
					item["branch"] = branch
				}
				if head := gitReadSingleLine(ctx, repo.FullDir, "rev-parse", "HEAD"); head != "" {
					item["head"] = head
				}
				if remotes := gitRemoteSummaries(ctx, repo.FullDir); len(remotes) > 0 {
					item["remotes"] = remotes
				}
				out = append(out, item)
			}
			result := map[string]any{
				"repositories": out,
				"total_count":  len(out),
			}
			return localFilesOutput("list_git_repositories", "repository_list", result, map[string]any{
				"repository_count": len(out),
				"total_count":      len(out),
			}), nil
		},
	}
}

func newGitListRefsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "git_list_refs",
				Description: "List branches, remote branches, and tags for a visible Git repository.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo_path": map[string]any{"type": "string", "description": "Repository path from list_git_repositories. Omit when only one repository is attached."},
						"limit":     map[string]any{"type": "number", "description": "Maximum refs to return. Default 50, max 200."},
					},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				RepoPath string `json:"repo_path,omitempty"`
				Limit    int    `json:"limit,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			repo, errOut, ok := resolveGitRepository(ctx, exec, in.RepoPath)
			if !ok {
				return errOut, nil
			}
			limit := clampPositiveInt(in.Limit, defaultGitListLimit, maxGitListLimit)
			cmd := []string{
				"for-each-ref",
				"--sort=-committerdate",
				"--format=%(objectname)%1f%(objectname:short)%1f%(refname)%1f%(refname:short)%1f%(committerdate:iso-strict)%1f%(subject)",
				"refs/heads",
				"refs/remotes",
				"refs/tags",
			}
			res := runGitReadOnly(ctx, repo.FullDir, maxGitCommandBytes, cmd...)
			if res.Err != nil {
				return gitJSONCommandError(res), nil
			}
			refs := parseGitRefs(res.Stdout, limit)
			result := map[string]any{
				"repo_path": repo.RelPath,
				"refs":      refs,
				"truncated": res.StdoutTruncated || len(refs) == limit,
			}
			return localFilesOutput("git_list_refs", "git_refs", result, map[string]any{
				"repo_path": repo.RelPath,
				"ref_count": len(refs),
				"truncated": result["truncated"],
			}), nil
		},
	}
}

func newGitLogTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "git_log",
				Description: "Read commit history for a visible Git repository, optionally filtered by path, author, date, or message query.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo_path": map[string]any{"type": "string", "description": "Repository path from list_git_repositories. Omit when only one repository is attached."},
						"path":      map[string]any{"type": "string", "description": "Optional repository-relative path to filter history."},
						"query":     map[string]any{"type": "string", "description": "Optional commit-message grep pattern."},
						"author":    map[string]any{"type": "string", "description": "Optional author pattern."},
						"since":     map[string]any{"type": "string", "description": "Optional git date expression, e.g. 2025-01-01."},
						"until":     map[string]any{"type": "string", "description": "Optional git date expression."},
						"all_refs":  map[string]any{"type": "boolean", "description": "Search all local and remote refs. Defaults true."},
						"max_count": map[string]any{"type": "number", "description": "Maximum commits. Default 50, max 200."},
					},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				RepoPath string `json:"repo_path,omitempty"`
				Path     string `json:"path,omitempty"`
				Query    string `json:"query,omitempty"`
				Author   string `json:"author,omitempty"`
				Since    string `json:"since,omitempty"`
				Until    string `json:"until,omitempty"`
				AllRefs  *bool  `json:"all_refs,omitempty"`
				MaxCount int    `json:"max_count,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			repo, errOut, ok := resolveGitRepository(ctx, exec, in.RepoPath)
			if !ok {
				return errOut, nil
			}
			pathspec, err := normalizeOptionalGitPath(in.Path)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			limit := clampPositiveInt(in.MaxCount, defaultGitListLimit, maxGitListLimit)
			cmd := gitLogArgs(limit, in.AllRefs == nil || *in.AllRefs)
			if q := strings.TrimSpace(in.Query); q != "" {
				cmd = append(cmd, "--grep="+q)
			}
			if author := strings.TrimSpace(in.Author); author != "" {
				cmd = append(cmd, "--author="+author)
			}
			if since := strings.TrimSpace(in.Since); since != "" {
				cmd = append(cmd, "--since="+since)
			}
			if until := strings.TrimSpace(in.Until); until != "" {
				cmd = append(cmd, "--until="+until)
			}
			if specs := gitPathspecsWithSensitiveExcludes(pathspec); len(specs) > 0 {
				cmd = append(append(cmd, "--"), specs...)
			}
			res := runGitReadOnly(ctx, repo.FullDir, maxGitCommandBytes, cmd...)
			if res.Err != nil {
				return gitJSONCommandError(res), nil
			}
			commits := parseGitLogRecords(res.Stdout)
			result := map[string]any{
				"repo_path": repo.RelPath,
				"commits":   commits,
				"truncated": res.StdoutTruncated,
			}
			return localFilesOutput("git_log", "git_commit_history", result, map[string]any{
				"repo_path":    repo.RelPath,
				"path":         pathspec,
				"commit_count": len(commits),
				"truncated":    res.StdoutTruncated,
			}), nil
		},
	}
}

func newGitSearchHistoryTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "git_search_history",
				Description: "Search commit history for text or regex changes across all refs in a visible Git repository.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo_path": map[string]any{"type": "string", "description": "Repository path from list_git_repositories. Omit when only one repository is attached."},
						"query":     map[string]any{"type": "string", "description": "Text or regex to search in historical diffs."},
						"regex":     map[string]any{"type": "boolean", "description": "Use git -G regex search instead of git -S text search. Default false."},
						"path":      map[string]any{"type": "string", "description": "Optional repository-relative path filter."},
						"all_refs":  map[string]any{"type": "boolean", "description": "Search all local and remote refs. Defaults true."},
						"max_count": map[string]any{"type": "number", "description": "Maximum matching commits. Default 50, max 200."},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				RepoPath string `json:"repo_path,omitempty"`
				Query    string `json:"query"`
				Regex    bool   `json:"regex,omitempty"`
				Path     string `json:"path,omitempty"`
				AllRefs  *bool  `json:"all_refs,omitempty"`
				MaxCount int    `json:"max_count,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return JSONError("query is required"), nil
			}
			repo, errOut, ok := resolveGitRepository(ctx, exec, in.RepoPath)
			if !ok {
				return errOut, nil
			}
			pathspec, err := normalizeOptionalGitPath(in.Path)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			limit := clampPositiveInt(in.MaxCount, defaultGitListLimit, maxGitListLimit)
			cmd := gitLogArgs(limit, in.AllRefs == nil || *in.AllRefs)
			if in.Regex {
				cmd = append(cmd, "-G"+query)
			} else {
				cmd = append(cmd, "-S"+query)
			}
			if specs := gitPathspecsWithSensitiveExcludes(pathspec); len(specs) > 0 {
				cmd = append(append(cmd, "--"), specs...)
			}
			res := runGitReadOnly(ctx, repo.FullDir, maxGitCommandBytes, cmd...)
			if res.Err != nil {
				return gitJSONCommandError(res), nil
			}
			commits := parseGitLogRecords(res.Stdout)
			result := map[string]any{
				"repo_path": repo.RelPath,
				"commits":   commits,
				"mode":      map[bool]string{true: "diff_regex", false: "diff_text"}[in.Regex],
				"truncated": res.StdoutTruncated,
			}
			return localFilesOutput("git_search_history", "git_history_search", result, map[string]any{
				"repo_path":    repo.RelPath,
				"path":         pathspec,
				"query":        query,
				"mode":         result["mode"],
				"commit_count": len(commits),
				"truncated":    res.StdoutTruncated,
			}), nil
		},
	}
}

func newGitGrepTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "git_grep",
				Description: "Search file contents at a specific git ref without modifying the repository.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo_path":      map[string]any{"type": "string", "description": "Repository path from list_git_repositories. Omit when only one repository is attached."},
						"query":          map[string]any{"type": "string", "description": "Text or regex to search for."},
						"ref":            map[string]any{"type": "string", "description": "Commit, branch, tag, or remote ref. Defaults to HEAD."},
						"path":           map[string]any{"type": "string", "description": "Optional repository-relative path filter."},
						"regex":          map[string]any{"type": "boolean", "description": "Use extended regex instead of fixed-string matching. Default false."},
						"case_sensitive": map[string]any{"type": "boolean", "description": "Case-sensitive search. Default false."},
						"max_matches":    map[string]any{"type": "number", "description": "Maximum matches to return. Default 50, max 200."},
					},
					"required":             []string{"query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				RepoPath      string `json:"repo_path,omitempty"`
				Query         string `json:"query"`
				Ref           string `json:"ref,omitempty"`
				Path          string `json:"path,omitempty"`
				Regex         bool   `json:"regex,omitempty"`
				CaseSensitive bool   `json:"case_sensitive,omitempty"`
				MaxMatches    int    `json:"max_matches,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return JSONError("query is required"), nil
			}
			repo, errOut, ok := resolveGitRepository(ctx, exec, in.RepoPath)
			if !ok {
				return errOut, nil
			}
			ref, err := normalizeGitRef(in.Ref, defaultGitRef)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			pathspec, err := normalizeOptionalGitPath(in.Path)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			limit := clampPositiveInt(in.MaxMatches, defaultGitListLimit, maxGitListLimit)
			cmd := []string{"grep", "-n", "-I"}
			if !in.CaseSensitive {
				cmd = append(cmd, "-i")
			}
			if in.Regex {
				cmd = append(cmd, "-E")
			} else {
				cmd = append(cmd, "-F")
			}
			cmd = append(cmd, "-e", query, ref)
			if specs := gitPathspecsWithSensitiveExcludes(pathspec); len(specs) > 0 {
				cmd = append(append(cmd, "--"), specs...)
			}
			res := runGitReadOnly(ctx, repo.FullDir, maxGitCommandBytes, cmd...)
			if res.Err != nil && !(gitExitCode(res.Err) == 1 && strings.TrimSpace(res.Stderr) == "") {
				return gitJSONCommandError(res), nil
			}
			matches, truncated := parseGitGrepMatches(res.Stdout, ref, limit)
			result := map[string]any{
				"repo_path":     repo.RelPath,
				"ref":           ref,
				"matches":       matches,
				"matches_count": len(matches),
				"truncated":     truncated || res.StdoutTruncated,
			}
			return localFilesOutput("git_grep", "git_file_search", result, map[string]any{
				"repo_path":     repo.RelPath,
				"path":          pathspec,
				"ref":           ref,
				"query":         query,
				"matches_count": len(matches),
				"truncated":     result["truncated"],
			}), nil
		},
	}
}

func newGitShowCommitTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "git_show_commit",
				Description: "Show a commit's metadata, stats, and patch for a visible Git repository.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo_path":  map[string]any{"type": "string", "description": "Repository path from list_git_repositories. Omit when only one repository is attached."},
						"ref":        map[string]any{"type": "string", "description": "Commit, branch, tag, or remote ref. Defaults to HEAD."},
						"path":       map[string]any{"type": "string", "description": "Optional repository-relative path filter."},
						"max_bytes":  map[string]any{"type": "number", "description": "Maximum bytes to return for this chunk. Default 64 KiB, max 256 KiB."},
						"page_token": map[string]any{"type": "string", "description": "Opaque token returned from previous page."},
					},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				RepoPath  string `json:"repo_path,omitempty"`
				Ref       string `json:"ref,omitempty"`
				Path      string `json:"path,omitempty"`
				MaxBytes  int    `json:"max_bytes,omitempty"`
				PageToken string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			repo, errOut, ok := resolveGitRepository(ctx, exec, in.RepoPath)
			if !ok {
				return errOut, nil
			}
			ref, err := normalizeGitRef(in.Ref, defaultGitRef)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			pathspec, err := normalizeOptionalGitPath(in.Path)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			cmd := []string{"show", "--no-ext-diff", "--no-textconv", "--date=iso-strict", "--stat", "--patch", "--find-renames", "--find-copies", "--format=fuller", "--max-count=1", ref}
			if specs := gitPathspecsWithSensitiveExcludes(pathspec); len(specs) > 0 {
				cmd = append(append(cmd, "--"), specs...)
			}
			res := runGitReadOnly(ctx, repo.FullDir, maxGitCommandBytes, cmd...)
			if res.Err != nil {
				return gitJSONCommandError(res), nil
			}
			return gitPagedTextOutput("git_show_commit", "git_commit", repo.RelPath, ref, pathspec, res.Stdout, res.StdoutTruncated, in.MaxBytes, in.PageToken), nil
		},
	}
}

func newGitShowFileAtRefTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "git_show_file_at_ref",
				Description: "Read a file as it existed at a commit, branch, tag, or remote ref.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"repo_path":  map[string]any{"type": "string", "description": "Repository path from list_git_repositories. Omit when only one repository is attached."},
						"ref":        map[string]any{"type": "string", "description": "Commit, branch, tag, or remote ref. Defaults to HEAD."},
						"path":       map[string]any{"type": "string", "description": "Repository-relative file path."},
						"max_bytes":  map[string]any{"type": "number", "description": "Maximum bytes to return for this chunk. Default 64 KiB, max 256 KiB."},
						"page_token": map[string]any{"type": "string", "description": "Opaque token returned from previous page."},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				RepoPath  string `json:"repo_path,omitempty"`
				Ref       string `json:"ref,omitempty"`
				Path      string `json:"path"`
				MaxBytes  int    `json:"max_bytes,omitempty"`
				PageToken string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			repo, errOut, ok := resolveGitRepository(ctx, exec, in.RepoPath)
			if !ok {
				return errOut, nil
			}
			ref, err := normalizeGitRef(in.Ref, defaultGitRef)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			filePath, err := normalizeGitPath(in.Path)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			spec := ref + ":" + filePath
			res := runGitReadOnly(ctx, repo.FullDir, maxGitCommandBytes, "show", "--no-ext-diff", "--no-textconv", spec)
			if res.Err != nil {
				return gitJSONCommandError(res), nil
			}
			return gitPagedTextOutput("git_show_file_at_ref", "git_file", repo.RelPath, ref, filePath, res.Stdout, res.StdoutTruncated, in.MaxBytes, in.PageToken), nil
		},
	}
}

func resolveGitRepositories(ctx context.Context, execCtx ExecContext) ([]gitRepository, Output, bool) {
	if repos, errOut, ok := resolveGitRepositoriesFromSources(ctx, execCtx); ok || !gitSourceMissingOrEmpty(errOut) {
		return repos, errOut, ok
	}
	if execCtx.Projects == nil {
		return nil, JSONError("No app-managed GitHub clones or imported project snapshots are available."), false
	}
	pid := strings.TrimSpace(execCtx.ProjectID)
	if pid == "" {
		return resolveGitRepositoriesWithoutProject(ctx, execCtx)
	}
	repos, errOut, ok := resolveGitRepositoriesForProject(ctx, execCtx.Projects, pid, "")
	if !ok {
		return nil, errOut, false
	}
	return repos, Output{}, true
}

func resolveGitRepositoriesFromSources(ctx context.Context, execCtx ExecContext) ([]gitRepository, Output, bool) {
	if execCtx.Sources == nil {
		return nil, JSONError("No app-managed Git repositories are available."), false
	}
	sourceRows, err := execCtx.Sources.List(ctx, sources.ListOptions{Kind: sources.KindGitHubClone})
	if err != nil {
		return nil, JSONError(err.Error()), false
	}
	repos := make([]gitRepository, 0, len(sourceRows))
	for _, src := range sourceRows {
		worktree := filepath.Clean(strings.TrimSpace(src.Worktree))
		if worktree == "" || !filepath.IsAbs(worktree) || !hasGitMetadata(worktree) {
			continue
		}
		repoPath := strings.Trim(strings.TrimSpace(src.Repository), "/")
		if repoPath == "" {
			repoPath = strings.TrimSpace(src.ID)
		}
		repos = append(repos, gitRepository{
			SourceID:   strings.TrimSpace(src.ID),
			SourceKind: sources.KindGitHubClone,
			Repository: strings.Trim(strings.TrimSpace(src.Repository), "/"),
			RelPath:    repoPath,
			FullDir:    worktree,
		})
	}
	if len(repos) == 0 {
		return nil, JSONError("No app-managed Git repositories are available."), false
	}
	sort.Slice(repos, func(i, j int) bool {
		return strings.ToLower(repos[i].RelPath) < strings.ToLower(repos[j].RelPath)
	})
	return repos, Output{}, true
}

func gitSourceMissingOrEmpty(out Output) bool {
	raw, err := json.Marshal(out.Result)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), "No app-managed Git repositories")
}

func resolveGitRepositoriesWithoutProject(ctx context.Context, execCtx ExecContext) ([]gitRepository, Output, bool) {
	if execCtx.Store == nil {
		return nil, JSONError("Missing project id."), false
	}
	projects, err := execCtx.Store.ListProjects(ctx, 25)
	if err != nil {
		return nil, JSONError(err.Error()), false
	}
	candidates := make([]gitProjectRepositories, 0, len(projects))
	for _, project := range projects {
		if strings.TrimSpace(project.ID) == "" || project.FileCount == 0 {
			continue
		}
		repos, errOut, ok := resolveGitRepositoriesForProject(ctx, execCtx.Projects, project.ID, project.Name)
		if !ok {
			if gitProjectMissingOrEmpty(errOut) {
				continue
			}
			return nil, errOut, false
		}
		if len(repos) == 0 {
			continue
		}
		candidates = append(candidates, gitProjectRepositories{Project: project, Repositories: repos})
	}
	if len(candidates) == 1 {
		return candidates[0].Repositories, Output{}, true
	}
	if len(candidates) > 1 {
		labels := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			labels = append(labels, gitProjectLabel(candidate.Project))
		}
		return nil, JSONError("project context is required when multiple projects contain visible Git repositories. Available projects: " + strings.Join(labels, ", ")), false
	}
	return nil, JSONError("Missing project id. No visible Git repositories are available in recent projects."), false
}

type gitProjectRepositories struct {
	Project      store.Project
	Repositories []gitRepository
}

func resolveGitRepositoriesForProject(ctx context.Context, ps *projects.Store, pid string, projectName string) ([]gitRepository, Output, bool) {
	root, err := ps.FilesDir(pid)
	if err != nil {
		return nil, JSONError(err.Error()), false
	}
	root = filepath.Clean(root)
	sources, err := ps.ListSources(ctx, pid)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return nil, JSONError("No Git repository snapshot is attached to this project. Clone a GitHub repository as an app-managed source, or import a snapshot copy first."), false
		}
		return nil, JSONError(err.Error()), false
	}
	visibleSources := map[string]struct{}{}
	for _, source := range sources {
		name, err := normalizeRelPathForDisplay(source.Name, false)
		if err != nil || name == "" || strings.Contains(name, "/") || source.Hidden {
			continue
		}
		visibleSources[name] = struct{}{}
	}
	repos, err := findVisibleGitWorktrees(ctx, root, visibleSources)
	if err != nil {
		return nil, JSONError(err.Error()), false
	}
	if len(repos) == 0 {
		return nil, JSONError("No visible Git repositories are attached to this project."), false
	}
	for i := range repos {
		repos[i].ProjectID = pid
		repos[i].ProjectName = strings.TrimSpace(projectName)
	}
	return repos, Output{}, true
}

func gitProjectMissingOrEmpty(out Output) bool {
	raw, err := json.Marshal(out.Result)
	if err != nil {
		return false
	}
	message := string(raw)
	return strings.Contains(message, "No app-managed GitHub clones") ||
		strings.Contains(message, "No Git repository snapshot") ||
		strings.Contains(message, "No visible Git repositories")
}

func gitProjectLabel(project store.Project) string {
	name := strings.TrimSpace(project.Name)
	id := strings.TrimSpace(project.ID)
	if name == "" {
		return id
	}
	if id == "" || id == name {
		return name
	}
	return fmt.Sprintf("%s (%s)", name, id)
}

func resolveGitRepository(ctx context.Context, execCtx ExecContext, repoPath string) (gitRepository, Output, bool) {
	repos, errOut, ok := resolveGitRepositories(ctx, execCtx)
	if !ok {
		return gitRepository{}, errOut, false
	}
	requested := strings.TrimSpace(repoPath)
	if requested == "" {
		if len(repos) == 1 {
			return repos[0], Output{}, true
		}
		paths := make([]string, 0, len(repos))
		for _, repo := range repos {
			paths = append(paths, repo.RelPath)
		}
		return gitRepository{}, JSONError("repo_path is required when multiple Git repositories are attached. Available: " + strings.Join(paths, ", ")), false
	}
	clean, err := normalizeRelPathForDisplay(requested, false)
	if err != nil {
		return gitRepository{}, JSONError(err.Error()), false
	}
	for _, repo := range repos {
		if repo.RelPath == clean ||
			strings.EqualFold(repo.Repository, requested) ||
			strings.EqualFold(repo.SourceID, requested) {
			return repo, Output{}, true
		}
	}
	return gitRepository{}, JSONError("Git repository not found or hidden: " + clean), false
}

func findVisibleGitWorktrees(ctx context.Context, root string, visibleSources map[string]struct{}) ([]gitRepository, error) {
	root = filepath.Clean(root)
	seen := map[string]struct{}{}
	repos := []gitRepository{}
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
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			return nil
		}
		seen[clean] = struct{}{}
		rel := "."
		if p != root {
			if r, err := filepath.Rel(root, p); err == nil && r != "." {
				rel = filepath.ToSlash(r)
			}
		}
		repos = append(repos, gitRepository{RelPath: rel, FullDir: clean})
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) || os.IsNotExist(walkErr) {
			return nil, nil
		}
		return nil, walkErr
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].RelPath < repos[j].RelPath })
	return repos, nil
}

func hasGitMetadata(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && st != nil
}

func normalizeOptionalGitPath(p string) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", nil
	}
	return normalizeGitPath(p)
}

func normalizeGitPath(p string) (string, error) {
	clean, err := normalizeRelPathForDisplay(p, false)
	if err != nil {
		return "", err
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", errors.New("path must not point inside .git")
	}
	return clean, nil
}

func gitPathspecsWithSensitiveExcludes(pathspec string) []string {
	pathspec = strings.TrimSpace(pathspec)
	if pathspec != "" && store.LocalPathExcludedByPermissionPatterns(pathspec, store.DefaultSensitiveLocalPermissionExcludes()) {
		return []string{pathspec}
	}
	include := "."
	if pathspec != "" {
		include = pathspec
	}
	out := []string{include}
	seen := map[string]struct{}{include: {}}
	addExclude := func(pattern string) {
		pattern = strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/"))
		if pattern == "" {
			return
		}
		spec := ":(exclude,glob)" + pattern
		if _, ok := seen[spec]; ok {
			return
		}
		seen[spec] = struct{}{}
		out = append(out, spec)
	}
	for _, pattern := range store.DefaultSensitiveLocalPermissionExcludes() {
		addExclude(pattern)
		if !strings.Contains(pattern, "/") {
			addExclude("**/" + pattern)
		}
	}
	return out
}

func normalizeGitRef(ref string, fallback string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = strings.TrimSpace(fallback)
	}
	if ref == "" {
		return "", errors.New("ref is required")
	}
	if len(ref) > 200 || strings.HasPrefix(ref, "-") || strings.Contains(ref, ":") || gitUnsafeRefRe.MatchString(ref) {
		return "", errors.New("invalid git ref")
	}
	return ref, nil
}

func gitLogArgs(limit int, allRefs bool) []string {
	args := []string{
		"log",
		"--date=iso-strict",
		"--pretty=format:" + gitRecordSeparator + "%H" + gitFieldSeparator + "%h" + gitFieldSeparator + "%aI" + gitFieldSeparator + "%an" + gitFieldSeparator + "%s",
		"--name-status",
		"-n",
		strconv.Itoa(limit),
	}
	if allRefs {
		args = append(args, "--all")
	}
	return args
}

func runGitReadOnly(ctx context.Context, repoDir string, maxStdout int, gitArgs ...string) gitCommandResult {
	if maxStdout <= 0 || maxStdout > maxGitCommandBytes {
		maxStdout = maxGitCommandBytes
	}
	runCtx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	args := []string{"--no-pager", "-C", repoDir}
	args = append(args, gitArgs...)
	cmd := exec.CommandContext(runCtx, "git", args...)
	cmd.Stdin = strings.NewReader("")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return gitCommandResult{Err: err}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return gitCommandResult{Err: err}
	}
	if err := cmd.Start(); err != nil {
		return gitCommandResult{Err: err}
	}

	type readResult struct {
		text      string
		truncated bool
		err       error
	}
	stdoutCh := make(chan readResult, 1)
	stderrCh := make(chan readResult, 1)
	go func() {
		text, truncated, err := readLimitedAndDrain(stdout, maxStdout)
		stdoutCh <- readResult{text: text, truncated: truncated, err: err}
	}()
	go func() {
		text, truncated, err := readLimitedAndDrain(stderr, 64*1024)
		stderrCh <- readResult{text: text, truncated: truncated, err: err}
	}()

	stdoutRes := <-stdoutCh
	stderrRes := <-stderrCh
	waitErr := cmd.Wait()
	if stdoutRes.err != nil && waitErr == nil {
		waitErr = stdoutRes.err
	}
	if stderrRes.err != nil && waitErr == nil {
		waitErr = stderrRes.err
	}
	if runCtx.Err() != nil && waitErr == nil {
		waitErr = runCtx.Err()
	}
	return gitCommandResult{
		Stdout:          stdoutRes.text,
		Stderr:          stderrRes.text,
		StdoutTruncated: stdoutRes.truncated,
		StderrTruncated: stderrRes.truncated,
		Err:             waitErr,
	}
}

func readLimitedAndDrain(r io.Reader, limit int) (string, bool, error) {
	if limit <= 0 {
		limit = maxGitOutputBytes
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return "", false, err
	}
	truncated := n > int64(limit)
	if truncated {
		_, _ = io.Copy(io.Discard, r)
		b := buf.Bytes()
		if len(b) > limit {
			b = b[:limit]
		}
		return string(b), true, nil
	}
	return buf.String(), false, nil
}

func gitReadSingleLine(ctx context.Context, repoDir string, args ...string) string {
	res := runGitReadOnly(ctx, repoDir, 4096, args...)
	if res.Err != nil {
		return ""
	}
	line := strings.TrimSpace(res.Stdout)
	if idx := strings.IndexAny(line, "\r\n"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	return line
}

func gitRemoteSummaries(ctx context.Context, repoDir string) []map[string]any {
	res := runGitReadOnly(ctx, repoDir, 64*1024, "remote", "-v")
	if res.Err != nil || strings.TrimSpace(res.Stdout) == "" {
		return nil
	}
	seen := map[string]map[string]any{}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := strings.TrimSpace(fields[0])
		remoteURL := redactGitRemoteURL(strings.TrimSpace(fields[1]))
		kind := strings.Trim(fields[2], "()")
		if name == "" || remoteURL == "" {
			continue
		}
		item, ok := seen[name+" "+remoteURL]
		if !ok {
			item = map[string]any{"name": name, "url": remoteURL}
			seen[name+" "+remoteURL] = item
		}
		if kind == "fetch" || kind == "push" {
			item[kind] = true
		}
	}
	out := make([]map[string]any, 0, len(seen))
	for _, item := range seen {
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		return fmt.Sprint(out[i]["name"], out[i]["url"]) < fmt.Sprint(out[j]["name"], out[j]["url"])
	})
	return out
}

func redactGitRemoteURL(raw string) string {
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u != nil && u.Scheme != "" {
		u.User = nil
		return u.String()
	}
	if idx := strings.Index(raw, "://"); idx >= 0 {
		rest := raw[idx+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			return raw[:idx+3] + rest[at+1:]
		}
	}
	return raw
}

func parseGitRefs(stdout string, limit int) []map[string]any {
	out := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(out) >= limit {
			break
		}
		fields := strings.Split(line, gitFieldSeparator)
		if len(fields) < 6 {
			continue
		}
		full := strings.TrimSpace(fields[2])
		kind := "ref"
		switch {
		case strings.HasPrefix(full, "refs/heads/"):
			kind = "branch"
		case strings.HasPrefix(full, "refs/remotes/"):
			kind = "remote_branch"
		case strings.HasPrefix(full, "refs/tags/"):
			kind = "tag"
		}
		out = append(out, map[string]any{
			"kind":         kind,
			"name":         strings.TrimSpace(fields[3]),
			"full_name":    full,
			"commit":       strings.TrimSpace(fields[0]),
			"short_commit": strings.TrimSpace(fields[1]),
			"committed_at": strings.TrimSpace(fields[4]),
			"subject":      strings.TrimSpace(fields[5]),
		})
	}
	return out
}

func parseGitLogRecords(stdout string) []map[string]any {
	records := strings.Split(stdout, gitRecordSeparator)
	out := []map[string]any{}
	for _, record := range records {
		record = strings.Trim(record, "\r\n")
		if strings.TrimSpace(record) == "" {
			continue
		}
		lines := strings.Split(record, "\n")
		if len(lines) == 0 {
			continue
		}
		fields := strings.Split(lines[0], gitFieldSeparator)
		if len(fields) < 5 {
			continue
		}
		commit := map[string]any{
			"commit":       strings.TrimSpace(fields[0]),
			"short_commit": strings.TrimSpace(fields[1]),
			"authored_at":  strings.TrimSpace(fields[2]),
			"author":       strings.TrimSpace(fields[3]),
			"subject":      strings.TrimSpace(fields[4]),
		}
		files := []map[string]any{}
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\t")
			if len(parts) < 2 {
				continue
			}
			item := map[string]any{
				"status": strings.TrimSpace(parts[0]),
				"path":   strings.TrimSpace(parts[len(parts)-1]),
			}
			if len(parts) > 2 {
				item["old_path"] = strings.TrimSpace(parts[1])
			}
			files = append(files, item)
		}
		if len(files) > 0 {
			commit["changed_files"] = files
		}
		out = append(out, commit)
	}
	return out
}

func parseGitGrepMatches(stdout string, ref string, limit int) ([]map[string]any, bool) {
	matches := []map[string]any{}
	truncated := false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(matches) >= limit {
			truncated = true
			break
		}
		item := map[string]any{"raw": line}
		rest := line
		prefix := ref + ":"
		if strings.HasPrefix(rest, prefix) {
			rest = strings.TrimPrefix(rest, prefix)
		}
		parts := strings.SplitN(rest, ":", 3)
		if len(parts) == 3 {
			item["path"] = parts[0]
			if n, err := strconv.Atoi(parts[1]); err == nil {
				item["line"] = n
			}
			item["content"] = strings.TrimSpace(parts[2])
		}
		matches = append(matches, item)
	}
	return matches, truncated
}

func gitPagedTextOutput(toolName string, kind string, repoPath string, ref string, pathspec string, content string, commandTruncated bool, maxBytes int, pageToken string) Output {
	limit := clampPositiveInt(maxBytes, defaultGitOutputBytes, maxGitOutputBytes)
	offset := int64(0)
	if token, ok := parsePageOffsetToken(pageToken); ok {
		offset = token
	} else if strings.TrimSpace(pageToken) != "" {
		return JSONError("Invalid page_token")
	}
	if offset < 0 {
		return JSONError("Invalid page_token")
	}
	b := []byte(content)
	if offset > int64(len(b)) {
		return JSONError("page_token out of range")
	}
	end := offset + int64(limit)
	if end < offset {
		return JSONError("page_token out of range")
	}
	hasMore := commandTruncated || end < int64(len(b))
	if end > int64(len(b)) {
		end = int64(len(b))
	}
	chunk := b[offset:end]
	out := map[string]any{
		"repo_path":        repoPath,
		"ref":              ref,
		"path":             pathspec,
		"offset":           offset,
		"max_bytes":        limit,
		"bytes_returned":   len(chunk),
		"has_more":         hasMore,
		"output_truncated": commandTruncated,
		"content":          string(chunk),
	}
	if hasMore && end < int64(len(b)) {
		out["next_page_token"] = encodePageOffsetToken(end)
	}
	return localFilesOutput(toolName, kind, out, map[string]any{
		"repo_path":      repoPath,
		"ref":            ref,
		"path":           pathspec,
		"offset":         offset,
		"bytes_returned": len(chunk),
		"has_more":       hasMore,
		"truncated":      commandTruncated,
	})
}

func gitJSONCommandError(res gitCommandResult) Output {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	if msg == "" && res.Err != nil {
		msg = res.Err.Error()
	}
	if msg == "" {
		msg = "git command failed"
	}
	return JSONError(msg)
}

func gitExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
