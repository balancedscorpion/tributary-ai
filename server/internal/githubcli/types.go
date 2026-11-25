package githubcli

import (
	"context"
	"time"
)

// Runner executes GitHub CLI commands. Implementations must treat Token as
// secret material and must not expose it outside the child process environment.
type Runner interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

type RunRequest struct {
	Args      []string
	Token     string
	Timeout   time.Duration
	StdoutCap int64
	StderrCap int64
	WorkDir   string
}

type RunResult struct {
	Args            []string `json:"args,omitempty"`
	Stdout          string   `json:"stdout,omitempty"`
	Stderr          string   `json:"stderr,omitempty"`
	StdoutTruncated bool     `json:"stdout_truncated,omitempty"`
	StderrTruncated bool     `json:"stderr_truncated,omitempty"`
	ExitCode        int      `json:"exit_code,omitempty"`
	TimedOut        bool     `json:"timed_out,omitempty"`
}

type StatusResult struct {
	Configured          bool     `json:"configured"`
	ClientIDConfigured  bool     `json:"client_id_configured"`
	ClientID            string   `json:"client_id,omitempty"`
	SecretsInitialized  bool     `json:"secrets_initialized"`
	SecretsLocked       bool     `json:"secrets_locked"`
	Connected           bool     `json:"connected"`
	Login               string   `json:"login,omitempty"`
	Scope               string   `json:"scope,omitempty"`
	ReadToolsEnabled    bool     `json:"read_tools_enabled"`
	AllowedRepositories []string `json:"allowed_repositories,omitempty"`
	LastCheckedAt       *int64   `json:"lastCheckedAt,omitempty"`
	LastCheckOk         *bool    `json:"lastCheckOk,omitempty"`
	LastCheckMessage    string   `json:"lastCheckMessage,omitempty"`
	Message             string   `json:"message,omitempty"`
}

type CheckResult struct {
	OK     bool           `json:"ok"`
	Status StatusResult   `json:"status"`
	User   map[string]any `json:"user,omitempty"`
}

type PendingApproval struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Detail    string         `json:"detail"`
	ToolName  string         `json:"toolName"`
	Product   string         `json:"product"`
	Action    string         `json:"action"`
	Target    map[string]any `json:"target,omitempty"`
	Content   map[string]any `json:"content,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

type approvalAction struct {
	PendingApproval
	Operation string
	Args      map[string]any
	Decision  chan approvalDecision
	Waiting   bool
}

type approvalDecision struct {
	Action string
}

type ProjectRepoRef struct {
	ProjectID string `json:"project_id"`
	RepoPath  string `json:"repo_path,omitempty"`
}

type GitHubRemote struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	FullName string `json:"full_name"`
	Fetch    bool   `json:"fetch,omitempty"`
	Push     bool   `json:"push,omitempty"`
}

type ProjectRepository struct {
	ProjectID     string         `json:"project_id"`
	SourceID      string         `json:"source_id,omitempty"`
	SourceKind    string         `json:"source_kind,omitempty"`
	RepoPath      string         `json:"repo_path"`
	Worktree      string         `json:"worktree,omitempty"`
	Owner         string         `json:"owner,omitempty"`
	Repo          string         `json:"repo,omitempty"`
	FullName      string         `json:"full_name,omitempty"`
	DefaultRef    string         `json:"default_ref,omitempty"`
	Head          string         `json:"head,omitempty"`
	RemoteName    string         `json:"remote_name,omitempty"`
	RemoteURL     string         `json:"remote_url,omitempty"`
	GitHubRemotes []GitHubRemote `json:"github_remotes,omitempty"`
	Resolvable    bool           `json:"resolvable"`
}

type Repository = ProjectRepository

type RepositoryRequest struct {
	ProjectID  string `json:"project_id"`
	RepoPath   string `json:"repo_path,omitempty"`
	Repository string `json:"repository,omitempty"`
	State      string `json:"state,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type CloneRepositoryRequest struct {
	ProjectID      string   `json:"project_id"`
	Repository     string   `json:"repository"`
	Name           string   `json:"name,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	Depth          int      `json:"depth,omitempty"`
	PartialClone   bool     `json:"partial_clone,omitempty"`
	SparsePaths    []string `json:"sparse_paths,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type ProjectRecord struct {
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	RootRelPath string `json:"root_rel_path,omitempty"`
	FileCount   int    `json:"file_count,omitempty"`
	TotalBytes  int64  `json:"total_bytes,omitempty"`
}

type CreateProjectFunc func(ctx context.Context, name string, rootRelPath string) (ProjectRecord, error)

type RefreshRepositoryRequest struct {
	ProjectID      string `json:"project_id"`
	RepoPath       string `json:"repo_path,omitempty"`
	Repository     string `json:"repository,omitempty"`
	BestEffort     bool   `json:"best_effort,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

type RawGHCommandRequest struct {
	ProjectID      string   `json:"project_id,omitempty"`
	Args           []string `json:"args"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type NumberedRepositoryRequest struct {
	ProjectID  string `json:"project_id"`
	RepoPath   string `json:"repo_path,omitempty"`
	Repository string `json:"repository,omitempty"`
	Number     int    `json:"number"`
}

type WorkflowRunJobsRequest struct {
	ProjectID  string `json:"project_id"`
	RepoPath   string `json:"repo_path,omitempty"`
	Repository string `json:"repository,omitempty"`
	RunID      string `json:"run_id"`
}

type ProjectRepositoryList struct {
	ProjectID    string              `json:"project_id"`
	Repositories []ProjectRepository `json:"repositories"`
	TotalCount   int                 `json:"total_count"`
}

type CloneRepositoryResult struct {
	ProjectID      string `json:"project_id"`
	SourceID       string `json:"source_id,omitempty"`
	SourceKind     string `json:"source_kind,omitempty"`
	Repository     string `json:"repository"`
	URL            string `json:"url"`
	AddedPath      string `json:"added_path"`
	Worktree       string `json:"worktree,omitempty"`
	PartialClone   bool   `json:"partial_clone,omitempty"`
	SparseCheckout bool   `json:"sparse_checkout,omitempty"`
	Output         string `json:"output,omitempty"`
	Project        any    `json:"project,omitempty"`
}

type RepositoryRefreshResult struct {
	Path       string `json:"path"`
	SourceID   string `json:"source_id,omitempty"`
	Repository string `json:"repository,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
}

type RefreshRepositoryResult struct {
	ProjectID    string                    `json:"project_id"`
	SourceKind   string                    `json:"source_kind,omitempty"`
	Repositories []RepositoryRefreshResult `json:"repositories"`
	Refreshed    bool                      `json:"refreshed"`
	Project      any                       `json:"project,omitempty"`
}

type RawGHCommandResult struct {
	Args   []string  `json:"args"`
	Result RunResult `json:"result"`
}

type RepositoryTreeEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	ByteSize int64  `json:"byte_size,omitempty"`
}

type RepositoryTreeResult struct {
	SourceID   string                `json:"source_id"`
	Repository string                `json:"repository"`
	Ref        string                `json:"ref,omitempty"`
	Path       string                `json:"path"`
	Entries    []RepositoryTreeEntry `json:"entries"`
}

type RepositoryFileResult struct {
	SourceID        string `json:"source_id"`
	Repository      string `json:"repository"`
	Ref             string `json:"ref,omitempty"`
	Path            string `json:"path"`
	Content         string `json:"content"`
	ByteSize        int64  `json:"byte_size"`
	Truncated       bool   `json:"truncated"`
	ContentTypeHint string `json:"content_type_hint,omitempty"`
}

type ListPullRequestsInput struct {
	ProjectRepoRef
	State string `json:"state,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type GetPullRequestInput struct {
	ProjectRepoRef
	Number int `json:"number"`
}

type PullRequestList struct {
	Repository   ProjectRepository `json:"repository"`
	PullRequests []PullRequest     `json:"pull_requests"`
	TotalCount   int               `json:"total_count"`
	Limit        int               `json:"limit"`
	Truncated    bool              `json:"truncated,omitempty"`
}

type PullRequestResult struct {
	Repository  ProjectRepository `json:"repository"`
	PullRequest PullRequest       `json:"pull_request"`
}

type PullRequest struct {
	Number            int              `json:"number"`
	Title             string           `json:"title,omitempty"`
	State             string           `json:"state,omitempty"`
	URL               string           `json:"url,omitempty"`
	Body              string           `json:"body,omitempty"`
	IsDraft           bool             `json:"is_draft,omitempty"`
	Author            map[string]any   `json:"author,omitempty"`
	Labels            []map[string]any `json:"labels,omitempty"`
	Assignees         []map[string]any `json:"assignees,omitempty"`
	Comments          []map[string]any `json:"comments,omitempty"`
	CommentsCount     int              `json:"comments_count,omitempty"`
	CreatedAt         string           `json:"created_at,omitempty"`
	UpdatedAt         string           `json:"updated_at,omitempty"`
	ClosedAt          string           `json:"closed_at,omitempty"`
	MergedAt          string           `json:"merged_at,omitempty"`
	HeadRefName       string           `json:"head_ref_name,omitempty"`
	BaseRefName       string           `json:"base_ref_name,omitempty"`
	ReviewDecision    string           `json:"review_decision,omitempty"`
	StatusCheckRollup []map[string]any `json:"status_check_rollup,omitempty"`
}

type ListIssuesInput struct {
	ProjectRepoRef
	State string `json:"state,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type GetIssueInput struct {
	ProjectRepoRef
	Number int `json:"number"`
}

type IssueList struct {
	Repository ProjectRepository `json:"repository"`
	Issues     []Issue           `json:"issues"`
	TotalCount int               `json:"total_count"`
	Limit      int               `json:"limit"`
	Truncated  bool              `json:"truncated,omitempty"`
}

type IssueResult struct {
	Repository ProjectRepository `json:"repository"`
	Issue      Issue             `json:"issue"`
}

type Issue struct {
	Number        int              `json:"number"`
	Title         string           `json:"title,omitempty"`
	State         string           `json:"state,omitempty"`
	URL           string           `json:"url,omitempty"`
	Body          string           `json:"body,omitempty"`
	Author        map[string]any   `json:"author,omitempty"`
	Labels        []map[string]any `json:"labels,omitempty"`
	Assignees     []map[string]any `json:"assignees,omitempty"`
	Comments      []map[string]any `json:"comments,omitempty"`
	CommentsCount int              `json:"comments_count,omitempty"`
	CreatedAt     string           `json:"created_at,omitempty"`
	UpdatedAt     string           `json:"updated_at,omitempty"`
	ClosedAt      string           `json:"closed_at,omitempty"`
}

type ListWorkflowRunsInput struct {
	ProjectRepoRef
	Limit    int    `json:"limit,omitempty"`
	Workflow string `json:"workflow,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Status   string `json:"status,omitempty"`
	Event    string `json:"event,omitempty"`
}

type GetWorkflowRunJobsInput struct {
	ProjectRepoRef
	RunID int64 `json:"run_id"`
}

type WorkflowRunList struct {
	Repository ProjectRepository `json:"repository"`
	Runs       []WorkflowRun     `json:"runs"`
	TotalCount int               `json:"total_count"`
	Limit      int               `json:"limit"`
	Truncated  bool              `json:"truncated,omitempty"`
}

type WorkflowRunJobsResult struct {
	Repository ProjectRepository `json:"repository"`
	RunID      int64             `json:"run_id"`
	Jobs       []WorkflowJob     `json:"jobs"`
}

type WorkflowRun struct {
	DatabaseID         int64  `json:"database_id,omitempty"`
	WorkflowDatabaseID int64  `json:"workflow_database_id,omitempty"`
	Number             int    `json:"number,omitempty"`
	Attempt            int    `json:"attempt,omitempty"`
	Name               string `json:"name,omitempty"`
	WorkflowName       string `json:"workflow_name,omitempty"`
	DisplayTitle       string `json:"display_title,omitempty"`
	Status             string `json:"status,omitempty"`
	Conclusion         string `json:"conclusion,omitempty"`
	Event              string `json:"event,omitempty"`
	HeadBranch         string `json:"head_branch,omitempty"`
	HeadSHA            string `json:"head_sha,omitempty"`
	URL                string `json:"url,omitempty"`
	CreatedAt          string `json:"created_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
	StartedAt          string `json:"started_at,omitempty"`
}

type WorkflowJob struct {
	DatabaseID  int64            `json:"database_id,omitempty"`
	Name        string           `json:"name,omitempty"`
	Status      string           `json:"status,omitempty"`
	Conclusion  string           `json:"conclusion,omitempty"`
	URL         string           `json:"url,omitempty"`
	StartedAt   string           `json:"started_at,omitempty"`
	CompletedAt string           `json:"completed_at,omitempty"`
	Steps       []map[string]any `json:"steps,omitempty"`
}
