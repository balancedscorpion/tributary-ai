package githubcli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sources"
)

type fakeRunner struct {
	calls  []RunRequest
	result RunResult
	err    error
}

func (r *fakeRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	req.Args = append([]string(nil), req.Args...)
	r.calls = append(r.calls, req)
	res := r.result
	res.Args = append([]string(nil), req.Args...)
	return res, r.err
}

func TestListRepositoriesDerivesGitHubRemoteFromVisibleProjectRepo(t *testing.T) {
	ctx := context.Background()
	_, sec, ps := newManagerStores(t, "ghs_testtokenvalue123456789012345")
	writeProjectGitRepo(t, ps, "proj", "visible", "https://github.com/acme/widget.git")
	writeProjectGitRepo(t, ps, "proj", "hidden", "git@github.com:acme/hidden.git")
	if err := ps.SetSourceHidden(ctx, "proj", "hidden", true); err != nil {
		t.Fatalf("SetSourceHidden: %v", err)
	}

	m := NewManager(Options{Secrets: sec, Projects: ps, Runner: &fakeRunner{}})
	list, err := m.ListRepositories(ctx, "proj")
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("repository count = %d, want 1: %#v", len(list), list)
	}
	repo := list[0]
	if repo.RepoPath != "visible" || repo.Owner != "acme" || repo.Repo != "widget" || repo.FullName != "acme/widget" || !repo.Resolvable {
		t.Fatalf("unexpected repository: %#v", repo)
	}
	if len(repo.GitHubRemotes) != 1 || repo.GitHubRemotes[0].Name != "origin" {
		t.Fatalf("unexpected remotes: %#v", repo.GitHubRemotes)
	}
}

func TestListPullRequestsUsesProjectScopedRepoAndFixedArgs(t *testing.T) {
	ctx := context.Background()
	token := "ghs_testtokenvalue123456789012345"
	_, sec, ps := newManagerStores(t, token)
	writeProjectGitRepo(t, ps, "proj", "repo", "git@github.com:acme/widget.git")
	runner := &fakeRunner{
		result: RunResult{
			Stdout: `[{"number":7,"title":"Fix it","state":"OPEN","isDraft":false,"url":"https://github.com/acme/widget/pull/7","author":{"login":"octo"},"comments":2}]`,
		},
	}
	m := NewManager(Options{Secrets: sec, Projects: ps, Runner: runner})

	out, err := m.ListPullRequests(ctx, RepositoryRequest{ProjectID: "proj", State: "open", Limit: 5})
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if out.Repository.FullName != "acme/widget" || len(out.PullRequests) != 1 {
		t.Fatalf("unexpected output: %#v", out)
	}
	if out.PullRequests[0].Number != 7 || out.PullRequests[0].CommentsCount != 2 {
		t.Fatalf("unexpected pull request payload: %#v", out.PullRequests[0])
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	expectedArgs := []string{
		"pr", "list",
		"--repo", "acme/widget",
		"--state", "open",
		"--limit", "5",
		"--json", "number,title,state,url,isDraft,author,labels,assignees,comments,createdAt,updatedAt,closedAt,mergedAt,headRefName,baseRefName,reviewDecision,statusCheckRollup",
	}
	if !reflect.DeepEqual(runner.calls[0].Args, expectedArgs) {
		t.Fatalf("args = %#v, want %#v", runner.calls[0].Args, expectedArgs)
	}
	if runner.calls[0].Token != token {
		t.Fatalf("runner token = %q, want configured token", runner.calls[0].Token)
	}
}

func TestListRepositoriesUsesAppManagedSourcesWithoutProject(t *testing.T) {
	ctx := context.Background()
	_, sec, _ := newManagerStores(t, "ghs_testtokenvalue123456789012345")
	sourceStore, err := sources.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("sources.NewStore: %v", err)
	}
	worktree := writeSourceGitRepo(t, "https://github.com/acme/widget.git")
	_, err = sourceStore.Upsert(ctx, sources.Source{
		ID:         sources.GitHubSourceID("acme/widget"),
		Kind:       sources.KindGitHubClone,
		Repository: "acme/widget",
		Owner:      "acme",
		Repo:       "widget",
		CloneURL:   "https://github.com/acme/widget.git",
		Worktree:   worktree,
	})
	if err != nil {
		t.Fatalf("source Upsert: %v", err)
	}
	m := NewManager(Options{Secrets: sec, Sources: sourceStore, Runner: &fakeRunner{}})

	list, err := m.ListRepositories(ctx, "")
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("repository count = %d, want 1: %#v", len(list), list)
	}
	repo := list[0]
	if repo.SourceID == "" || repo.SourceKind != sources.KindGitHubClone || repo.RepoPath != "acme/widget" || repo.FullName != "acme/widget" || repo.Worktree != worktree || !repo.Resolvable {
		t.Fatalf("unexpected source-backed repository: %#v", repo)
	}
	resolved, err := m.ResolveRepository(ctx, ProjectRepoRef{RepoPath: repo.SourceID})
	if err != nil {
		t.Fatalf("ResolveRepository by source id: %v", err)
	}
	if resolved.FullName != "acme/widget" || resolved.SourceID != repo.SourceID {
		t.Fatalf("resolved repository = %#v, want source-backed acme/widget", resolved)
	}
}

func TestListRepositoriesIncludesAppManagedSourcesWithActiveProject(t *testing.T) {
	ctx := context.Background()
	_, sec, ps := newManagerStores(t, "ghs_testtokenvalue123456789012345")
	writeProjectGitRepo(t, ps, "proj", "legacy-copy", "https://github.com/acme/widget.git")
	sourceStore, err := sources.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("sources.NewStore: %v", err)
	}
	sourceID := sources.GitHubSourceID("acme/widget")
	worktree := writeSourceGitRepo(t, "https://github.com/acme/widget.git")
	_, err = sourceStore.Upsert(ctx, sources.Source{
		ID:         sourceID,
		Kind:       sources.KindGitHubClone,
		Repository: "acme/widget",
		Owner:      "acme",
		Repo:       "widget",
		CloneURL:   "https://github.com/acme/widget.git",
		Worktree:   worktree,
	})
	if err != nil {
		t.Fatalf("source Upsert: %v", err)
	}
	m := NewManager(Options{Secrets: sec, Projects: ps, Sources: sourceStore, Runner: &fakeRunner{}})

	list, err := m.ListRepositories(ctx, "proj")
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("repository count = %d, want de-duplicated source repo: %#v", len(list), list)
	}
	if list[0].SourceID != sourceID || list[0].SourceKind != sources.KindGitHubClone || list[0].Worktree != worktree {
		t.Fatalf("repository = %#v, want source-backed clone to win over project copy", list[0])
	}
	resolved, err := m.ResolveRepository(ctx, ProjectRepoRef{ProjectID: "proj", RepoPath: "acme/widget"})
	if err != nil {
		t.Fatalf("ResolveRepository with active project: %v", err)
	}
	if resolved.SourceID != sourceID {
		t.Fatalf("resolved repository = %#v, want source-backed clone", resolved)
	}
}

func TestRefreshRepositoryUsesAppManagedSourceID(t *testing.T) {
	ctx := context.Background()
	token := "ghs_testtokenvalue123456789012345"
	_, sec, _ := newManagerStores(t, token)
	sourceStore, err := sources.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("sources.NewStore: %v", err)
	}
	sourceID := sources.GitHubSourceID("acme/widget")
	worktree := writeRefreshableSourceGitRepo(t)
	_, err = sourceStore.Upsert(ctx, sources.Source{
		ID:         sourceID,
		Kind:       sources.KindGitHubClone,
		Repository: "acme/widget",
		Owner:      "acme",
		Repo:       "widget",
		CloneURL:   "https://github.com/acme/widget.git",
		Worktree:   worktree,
	})
	if err != nil {
		t.Fatalf("source Upsert: %v", err)
	}
	m := NewManager(Options{Secrets: sec, Sources: sourceStore})

	out, err := m.RefreshRepository(ctx, RefreshRepositoryRequest{RepoPath: sourceID})
	if err != nil {
		t.Fatalf("RefreshRepository: %v", err)
	}
	if !out.Refreshed || len(out.Repositories) != 1 || out.Repositories[0].SourceID != sourceID {
		t.Fatalf("unexpected refresh result: %#v", out)
	}
	updated, err := sourceStore.Get(ctx, sourceID)
	if err != nil {
		t.Fatalf("source Get after refresh: %v", err)
	}
	if updated.Branch != "main" || strings.TrimSpace(updated.Head) == "" {
		t.Fatalf("source revision after refresh = branch %q head %q, want main and non-empty head", updated.Branch, updated.Head)
	}
}

func TestListPullRequestsUsesAppManagedSourceWhenNoProject(t *testing.T) {
	ctx := context.Background()
	token := "ghs_testtokenvalue123456789012345"
	_, sec, _ := newManagerStores(t, token)
	sourceStore, err := sources.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("sources.NewStore: %v", err)
	}
	worktree := writeSourceGitRepo(t, "git@github.com:acme/widget.git")
	_, err = sourceStore.Upsert(ctx, sources.Source{
		ID:         sources.GitHubSourceID("acme/widget"),
		Kind:       sources.KindGitHubClone,
		Repository: "acme/widget",
		Owner:      "acme",
		Repo:       "widget",
		CloneURL:   "git@github.com:acme/widget.git",
		Worktree:   worktree,
	})
	if err != nil {
		t.Fatalf("source Upsert: %v", err)
	}
	runner := &fakeRunner{
		result: RunResult{
			Stdout: `[{"number":9,"title":"Source PR","state":"OPEN","url":"https://github.com/acme/widget/pull/9"}]`,
		},
	}
	m := NewManager(Options{Secrets: sec, Sources: sourceStore, Runner: runner})

	out, err := m.ListPullRequests(ctx, RepositoryRequest{Repository: "acme/widget", State: "open", Limit: 3})
	if err != nil {
		t.Fatalf("ListPullRequests: %v", err)
	}
	if out.Repository.SourceKind != sources.KindGitHubClone || len(out.PullRequests) != 1 || out.PullRequests[0].Number != 9 {
		t.Fatalf("unexpected output: %#v", out)
	}
	expectedArgs := []string{
		"pr", "list",
		"--repo", "acme/widget",
		"--state", "open",
		"--limit", "3",
		"--json", "number,title,state,url,isDraft,author,labels,assignees,comments,createdAt,updatedAt,closedAt,mergedAt,headRefName,baseRefName,reviewDecision,statusCheckRollup",
	}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0].Args, expectedArgs) {
		t.Fatalf("runner calls=%#v, want args %#v", runner.calls, expectedArgs)
	}
	if runner.calls[0].Token != token {
		t.Fatalf("runner token = %q, want configured token", runner.calls[0].Token)
	}
}

func TestRunReadGHCommandAllowsSearchCodeWithoutApproval(t *testing.T) {
	ctx := context.Background()
	token := "ghs_testtokenvalue123456789012345"
	_, sec, ps := newManagerStores(t, token)
	runner := &fakeRunner{
		result: RunResult{Stdout: "result"},
	}
	m := NewManager(Options{Secrets: sec, Projects: ps, Runner: runner})

	out, err := m.RunReadGHCommand(ctx, RawGHCommandRequest{
		Args:           []string{"gh", "search_code", "approval_required", "--repo", "acme/widget"},
		TimeoutSeconds: 500,
	})
	if err != nil {
		t.Fatalf("RunReadGHCommand: %v", err)
	}
	wantArgs := []string{"search", "code", "approval_required", "--repo", "acme/widget"}
	if !reflect.DeepEqual(out.Args, wantArgs) {
		t.Fatalf("out args = %#v, want %#v", out.Args, wantArgs)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.calls))
	}
	if !reflect.DeepEqual(runner.calls[0].Args, wantArgs) {
		t.Fatalf("runner args = %#v, want %#v", runner.calls[0].Args, wantArgs)
	}
	if runner.calls[0].Token != token {
		t.Fatalf("runner token = %q, want configured token", runner.calls[0].Token)
	}
	if runner.calls[0].Timeout != maxReadRawGHTimeout {
		t.Fatalf("runner timeout = %v, want %v", runner.calls[0].Timeout, maxReadRawGHTimeout)
	}
}

func TestApproveSignalsWaitingApproval(t *testing.T) {
	ctx := context.Background()
	m := NewManager(Options{})
	approval := m.QueueApproval(PendingApproval{
		Title:    "Test approval",
		ToolName: "github_clone_repository",
	}, "unknown_test_operation", map[string]any{})

	awaited := make(chan map[string]any, 1)
	awaitErr := make(chan error, 1)
	go func() {
		result, err := m.AwaitApproval(ctx, approval.ID, 2*time.Second)
		if err != nil {
			awaitErr <- err
			return
		}
		awaited <- result
	}()
	waitForApprovalWaiter(t, m, approval.ID)

	approvalResponse, err := m.Approve(ctx, approval.ID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approvalResponse["approval_status"] != "approved" {
		t.Fatalf("approval_status=%v, want approved; response=%#v", approvalResponse["approval_status"], approvalResponse)
	}
	if _, ok := approvalResponse["result"]; ok {
		t.Fatalf("Approve executed operation while waiter was active: %#v", approvalResponse)
	}

	select {
	case err := <-awaitErr:
		t.Fatalf("AwaitApproval: %v", err)
	case result := <-awaited:
		if result["approval_status"] != "error" {
			t.Fatalf("awaited status=%v, want error from unknown operation; result=%#v", result["approval_status"], result)
		}
		errText, _ := result["error"].(string)
		if !strings.Contains(errText, "unknown GitHub approval operation") {
			t.Fatalf("awaited error=%v, want unknown operation; result=%#v", result["error"], result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitApproval did not receive approval decision")
	}
	if got := m.ListApprovals(); len(got) != 0 {
		t.Fatalf("pending approvals=%#v, want none", got)
	}
}

func TestAwaitApprovalWithoutTimeoutWaitsForDecision(t *testing.T) {
	ctx := context.Background()
	m := NewManager(Options{})
	approval := m.QueueApproval(PendingApproval{
		Title:    "Test approval",
		ToolName: "github_clone_repository",
	}, "unknown_test_operation", map[string]any{})

	awaited := make(chan map[string]any, 1)
	awaitErr := make(chan error, 1)
	go func() {
		result, err := m.AwaitApproval(ctx, approval.ID, 0)
		if err != nil {
			awaitErr <- err
			return
		}
		awaited <- result
	}()
	waitForApprovalWaiter(t, m, approval.ID)
	select {
	case err := <-awaitErr:
		t.Fatalf("AwaitApproval error before decision: %v", err)
	case result := <-awaited:
		t.Fatalf("AwaitApproval returned before decision: %#v", result)
	case <-time.After(20 * time.Millisecond):
	}

	if _, err := m.Reject(approval.ID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	select {
	case err := <-awaitErr:
		t.Fatalf("AwaitApproval error: %v", err)
	case result := <-awaited:
		if result["approval_status"] != "rejected" {
			t.Fatalf("approval_status=%v, want rejected; result=%#v", result["approval_status"], result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitApproval did not receive reject decision")
	}
}

func waitForApprovalWaiter(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		action, ok := m.approvals[id]
		waiting := ok && action.Waiting
		m.mu.Unlock()
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("approval %s did not enter waiting state", id)
}

func TestRawGHReadOnlyClassification(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "search code alias", args: []string{"gh", "search_code", "needle", "--repo", "owner/repo"}, want: true},
		{name: "search commits", args: []string{"search", "commits", "fix", "--owner", "owner"}, want: true},
		{name: "repo view", args: []string{"repo", "view", "owner/repo", "--json", "name"}, want: true},
		{name: "repo license", args: []string{"repo", "license", "view", "mit"}, want: true},
		{name: "pr diff", args: []string{"pr", "diff", "7", "--repo", "owner/repo"}, want: true},
		{name: "issue status", args: []string{"issue", "status", "--repo", "owner/repo"}, want: true},
		{name: "project item list", args: []string{"project", "item-list", "1", "--owner", "owner"}, want: true},
		{name: "release list", args: []string{"release", "list", "--repo", "owner/repo"}, want: true},
		{name: "status", args: []string{"status"}, want: true},
		{name: "api default get", args: []string{"api", "-H", "Accept: application/vnd.github.raw", "repos/owner/repo/contents/README.md"}, want: true},
		{name: "api explicit get with query fields", args: []string{"api", "search/code", "-X", "GET", "-f", "q=repo:owner/repo needle"}, want: true},
		{name: "api head", args: []string{"api", "--method=HEAD", "repos/owner/repo"}, want: true},
		{name: "web flag", args: []string{"repo", "view", "owner/repo", "--web"}, want: false},
		{name: "repo clone", args: []string{"repo", "clone", "owner/repo"}, want: false},
		{name: "workflow run", args: []string{"workflow", "run", "ci.yml", "--repo", "owner/repo"}, want: false},
		{name: "release download writes files", args: []string{"release", "download", "v1", "--repo", "owner/repo"}, want: false},
		{name: "api fields default to post", args: []string{"api", "repos/owner/repo/issues/1/comments", "-f", "body=hello"}, want: false},
		{name: "api post mutates", args: []string{"api", "repos/owner/repo/issues", "--method", "POST"}, want: false},
		{name: "api input reads local file", args: []string{"api", "repos/owner/repo/issues", "--method", "GET", "--input", "/tmp/body.json"}, want: false},
		{name: "api graphql can mutate", args: []string{"api", "graphql", "-f", "query=query { viewer { login } }"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, err := ValidateRawGHArgs(tt.args)
			if err != nil {
				t.Fatalf("ValidateRawGHArgs: %v", err)
			}
			if got := RawGHCommandIsReadOnly(normalized); got != tt.want {
				t.Fatalf("RawGHCommandIsReadOnly(%#v)=%v, want %v", normalized, got, tt.want)
			}
		})
	}
}

func TestValidateRawGHRepoCloneTargetRejectsWindowsUnsafeNames(t *testing.T) {
	tests := [][]string{
		{"repo", "clone", "owner/repo", "CON"},
		{"repo", "clone", "owner/repo", "COM1.txt"},
		{"repo", "clone", "owner/repo", "repo."},
		{"repo", "clone", "owner/repo", "repo name"},
		{"repo", "clone", "owner/repo", "repo:copy"},
	}
	for _, args := range tests {
		if err := validateRawGHRepoCloneTarget(args); err == nil {
			t.Fatalf("validateRawGHRepoCloneTarget(%#v) error = nil, want error", args)
		}
	}

	if err := validateRawGHRepoCloneTarget([]string{"repo", "clone", "owner/repo", "repo-copy"}); err != nil {
		t.Fatalf("validateRawGHRepoCloneTarget(safe target): %v", err)
	}
}

func TestRawGHAPIRepositorySelector(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "api repos path with header",
			args: []string{"api", "-H", "Accept: application/vnd.github.raw", "repos/acme/widget/contents/README.md"},
			want: []string{"acme/widget"},
		},
		{
			name: "api repos path with query",
			args: []string{"api", "repos/acme/widget/contents/README.md?ref=main", "--method=GET"},
			want: []string{"acme/widget"},
		},
		{
			name: "api search path has no repository selector",
			args: []string{"api", "search/code", "-X", "GET", "-f", "q=repo:acme/widget needle"},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rawGHRepositorySelectors(tt.args); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("rawGHRepositorySelectors(%#v)=%#v, want %#v", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunReadGHCommandRejectsWriteCommand(t *testing.T) {
	ctx := context.Background()
	_, sec, ps := newManagerStores(t, "ghs_testtokenvalue123456789012345")
	runner := &fakeRunner{result: RunResult{Stdout: "result"}}
	m := NewManager(Options{Secrets: sec, Projects: ps, Runner: runner})

	_, err := m.RunReadGHCommand(ctx, RawGHCommandRequest{Args: []string{"pr", "create", "--repo", "acme/widget"}})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("RunReadGHCommand err = %v, want requires approval", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("runner calls = %d, want 0", len(runner.calls))
	}
}

func TestManagerRedactsRunnerErrors(t *testing.T) {
	ctx := context.Background()
	token := "ghs_testtokenvalue123456789012345"
	_, sec, ps := newManagerStores(t, token)
	writeProjectGitRepo(t, ps, "proj", "repo", "https://github.com/acme/widget.git")
	runner := &fakeRunner{
		result: RunResult{
			Stdout: "stdout " + token,
			Stderr: "stderr ghp_abcdefghijklmnopqrstuvwxyz123456",
		},
		err: errors.New("failed with " + token),
	}
	m := NewManager(Options{Secrets: sec, Projects: ps, Runner: runner})

	_, err := m.ListIssues(ctx, RepositoryRequest{ProjectID: "proj"})
	if err == nil {
		t.Fatalf("ListIssues err = nil, want failure")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "ghp_abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("error was not redacted: %v", err)
	}
	var cmdErr *CommandError
	if !errors.As(err, &cmdErr) {
		t.Fatalf("err type = %T, want *CommandError", err)
	}
	if strings.Contains(cmdErr.Stdout, token) || strings.Contains(cmdErr.Stderr, "ghp_abcdefghijklmnopqrstuvwxyz123456") {
		t.Fatalf("command output was not redacted: %#v", cmdErr)
	}
}

func TestRunGitUsesExplicitWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake git is POSIX-only")
	}
	binDir := t.TempDir()
	fakeGitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGitPath, []byte("#!/bin/sh\npwd -P\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	workDir := t.TempDir()
	res, err := NewManager(Options{}).runGitInDir(context.Background(), "", 5*time.Second, workDir, "status")
	if err != nil {
		t.Fatalf("runGitInDir: %v\nstderr=%s", err, res.Stderr)
	}
	got := strings.TrimSpace(res.Stdout)
	if !sameTestDirectory(t, got, workDir) {
		t.Fatalf("git cwd = %q, want %q", got, workDir)
	}
}

func TestRunGitWithTokenUsesPortableHooksPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake git is POSIX-only")
	}
	binDir := t.TempDir()
	fakeGitPath := filepath.Join(binDir, "git")
	if err := os.WriteFile(fakeGitPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	workDir := t.TempDir()
	res, err := NewManager(Options{}).runGitInDir(context.Background(), "ghs_testtokenvalue123456789012345", 5*time.Second, workDir, "status")
	if err != nil {
		t.Fatalf("runGitInDir: %v\nstderr=%s", err, res.Stderr)
	}
	if strings.Contains(res.Stdout, "/dev/null") {
		t.Fatalf("git args use POSIX-only hooks path: %s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "credential.helper=") || !strings.Contains(res.Stdout, "core.hooksPath=") {
		t.Fatalf("git args missing auth safety config: %s", res.Stdout)
	}
}

func TestCloneRepositoryCreatesProjectWhenMissingProjectID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake git is POSIX-only")
	}
	binDir := t.TempDir()
	fakeGitPath := filepath.Join(binDir, "git")
	script := `#!/bin/sh
has_lfs_smudge=0
has_lfs_process=0
has_lfs_required=0
last=""
for arg in "$@"; do
	case "$arg" in
		filter.lfs.smudge=) has_lfs_smudge=1 ;;
		filter.lfs.process=) has_lfs_process=1 ;;
		filter.lfs.required=false) has_lfs_required=1 ;;
	esac
	last="$arg"
done
if [ "$has_lfs_smudge" != "1" ] || [ "$has_lfs_process" != "1" ] || [ "$has_lfs_required" != "1" ]; then
	echo "missing lfs checkout bypass" >&2
	exit 42
fi
mkdir -p "$last/.git"
printf 'cloned into %s\n' "$last"
`
	if err := os.WriteFile(fakeGitPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+oldPath)

	ctx := context.Background()
	_, sec, _ := newManagerStores(t, "ghs_testtokenvalue123456789012345")
	wd := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})
	ps, err := projects.NewStore(filepath.Join(".", "relative-config"))
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	var createdName string
	var createdRoot string
	m := NewManager(Options{
		Secrets:  sec,
		Projects: ps,
		CreateProject: func(ctx context.Context, name string, rootRelPath string) (ProjectRecord, error) {
			createdName = name
			createdRoot = rootRelPath
			return ProjectRecord{ID: "proj_new", Name: name, RootRelPath: rootRelPath}, nil
		},
		RefreshProjectIndex: func(ctx context.Context, projectID string) (any, error) {
			if projectID != "proj_new" {
				t.Fatalf("RefreshProjectIndex projectID=%q, want proj_new", projectID)
			}
			return ProjectRecord{ID: projectID, Name: createdName, RootRelPath: createdRoot, FileCount: 1}, nil
		},
	})

	result, err := m.cloneRepository(ctx, CloneRepositoryRequest{
		Repository: "acme/widget",
		Depth:      1,
	})
	if err != nil {
		t.Fatalf("cloneRepository: %v", err)
	}
	if result.ProjectID != "proj_new" || result.Repository != "acme/widget" || result.AddedPath != "widget" {
		t.Fatalf("unexpected clone result: %#v", result)
	}
	if createdName != "widget" {
		t.Fatalf("created project name=%q, want widget", createdName)
	}
	if createdRoot != "git:https://github.com/acme/widget.git" {
		t.Fatalf("created project root=%q, want git URL", createdRoot)
	}
	filesDir, err := ps.FilesDir("proj_new")
	if err != nil {
		t.Fatalf("FilesDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filesDir, "widget", ".git")); err != nil {
		t.Fatalf("cloned repository was not moved into project files: %v", err)
	}
}

func TestSanitizeGitHubSourceNameWindowsReserved(t *testing.T) {
	tests := map[string]string{
		"CON":       "CON-repo",
		"nul.git":   "nul-repo",
		"COM1.txt":  "COM1-repo.txt",
		"LPT9.log":  "LPT9-repo.log",
		"repo.":     "repo",
		"repo name": "repo-name",
		"":          "source",
	}
	for in, want := range tests {
		if got := sanitizeGitHubSourceName(in); got != want {
			t.Fatalf("sanitizeGitHubSourceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWriteGitAskpassForOS(t *testing.T) {
	tmp := t.TempDir()
	windowsPath, err := writeGitAskpassForOS(tmp, "windows")
	if err != nil {
		t.Fatalf("writeGitAskpassForOS(windows): %v", err)
	}
	if filepath.Ext(windowsPath) != ".cmd" {
		t.Fatalf("windows askpass path = %q, want .cmd extension", windowsPath)
	}
	windowsScript, err := os.ReadFile(windowsPath)
	if err != nil {
		t.Fatalf("read windows askpass: %v", err)
	}
	if !strings.Contains(string(windowsScript), "AIA_GITHUB_TOKEN") || !strings.Contains(string(windowsScript), "x-access-token") {
		t.Fatalf("windows askpass script missing expected credentials handling: %s", windowsScript)
	}

	posixPath, err := writeGitAskpassForOS(tmp, "linux")
	if err != nil {
		t.Fatalf("writeGitAskpassForOS(linux): %v", err)
	}
	if filepath.Ext(posixPath) != ".sh" {
		t.Fatalf("posix askpass path = %q, want .sh extension", posixPath)
	}
}

func TestCheckPersistsGitHubHealthState(t *testing.T) {
	ctx := context.Background()
	settingsStore, sec, ps := newManagerStores(t, "ghs_testtokenvalue123456789012345")
	runner := &fakeRunner{result: RunResult{Stdout: `[]`}}
	m := NewManager(Options{Settings: settingsStore, Secrets: sec, Projects: ps, Runner: runner})

	result, err := m.Check(ctx)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.OK || result.Status.LastCheckOk == nil || !*result.Status.LastCheckOk || result.Status.LastCheckedAt == nil {
		t.Fatalf("unexpected check result health: %#v", result)
	}
	settings := settingsStore.Get()
	if settings.GitHub == nil || settings.GitHub.LastCheckOk == nil || !*settings.GitHub.LastCheckOk || settings.GitHub.LastCheckedAt == nil {
		t.Fatalf("settings did not persist healthy GitHub state: %#v", settings.GitHub)
	}
}

func TestCheckPersistsGitHubFailureState(t *testing.T) {
	ctx := context.Background()
	settingsStore, sec, ps := newManagerStores(t, "ghs_testtokenvalue123456789012345")
	runner := &fakeRunner{
		result: RunResult{Stderr: "bad credentials", ExitCode: 1},
		err:    errors.New("exit status 1"),
	}
	m := NewManager(Options{Settings: settingsStore, Secrets: sec, Projects: ps, Runner: runner})

	result, err := m.Check(ctx)
	if err == nil {
		t.Fatal("Check err = nil, want failure")
	}
	if result.Status.LastCheckOk == nil || *result.Status.LastCheckOk || result.Status.LastCheckedAt == nil {
		t.Fatalf("unexpected failed check health: %#v", result.Status)
	}
	settings := settingsStore.Get()
	if settings.GitHub == nil || settings.GitHub.LastCheckOk == nil || *settings.GitHub.LastCheckOk || settings.GitHub.LastCheckedAt == nil {
		t.Fatalf("settings did not persist failed GitHub state: %#v", settings.GitHub)
	}
	if settings.GitHub.LastCheckMessage == nil || !strings.Contains(*settings.GitHub.LastCheckMessage, "bad credentials") {
		t.Fatalf("LastCheckMessage = %#v, want command failure detail", settings.GitHub.LastCheckMessage)
	}
}

func newManagerStores(t *testing.T, token string) (*config.Store, *secrets.Store, *projects.Store) {
	t.Helper()
	dir := t.TempDir()
	settingsStore, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	secretsStore, err := secrets.NewStore(dir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := secretsStore.Init("passphrase"); err != nil {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := secretsStore.Unlock("passphrase"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := secretsStore.SetGitHubOAuthAccessToken(token, "octo", "repo", "bearer"); err != nil {
		t.Fatalf("SetGitHubOAuthAccessToken: %v", err)
	}
	projectStore, err := projects.NewStore(dir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	return settingsStore, secretsStore, projectStore
}

func writeProjectGitRepo(t *testing.T, ps *projects.Store, projectID string, repoPath string, remoteURL string) {
	t.Helper()
	filesDir, err := ps.EnsureFilesDir(projectID)
	if err != nil {
		t.Fatalf("EnsureFilesDir: %v", err)
	}
	repoDir := filepath.Join(filesDir, filepath.FromSlash(repoPath))
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	configText := `[core]
	repositoryformatversion = 0
	filemode = true
[remote "origin"]
	url = ` + remoteURL + `
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(configText), 0o600); err != nil {
		t.Fatalf("write git config: %v", err)
	}
}

func writeSourceGitRepo(t *testing.T, remoteURL string) string {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "worktree")
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("mkdir source .git: %v", err)
	}
	configText := `[core]
	repositoryformatversion = 0
	filemode = true
[remote "origin"]
	url = ` + remoteURL + `
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(configText), 0o600); err != nil {
		t.Fatalf("write source git config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Widget\n"), 0o600); err != nil {
		t.Fatalf("write source README: %v", err)
	}
	return repoDir
}

func writeRefreshableSourceGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	remoteDir := filepath.Join(root, "remote.git")
	worktree := filepath.Join(root, "worktree")
	runTestGit(t, "", "init", "--bare", remoteDir)
	runTestGit(t, "", "init", worktree)
	runTestGit(t, worktree, "config", "user.email", "test@example.com")
	runTestGit(t, worktree, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("# Widget\n"), 0o600); err != nil {
		t.Fatalf("write source README: %v", err)
	}
	runTestGit(t, worktree, "add", "README.md")
	runTestGit(t, worktree, "commit", "-m", "initial")
	runTestGit(t, worktree, "branch", "-M", "main")
	runTestGit(t, worktree, "remote", "add", "origin", remoteDir)
	runTestGit(t, worktree, "push", "-u", "origin", "main")
	return worktree
}

func runTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
}

func sameTestDirectory(t *testing.T, got string, want string) bool {
	t.Helper()
	gotEval, gotErr := filepath.EvalSymlinks(got)
	wantEval, wantErr := filepath.EvalSymlinks(want)
	if gotErr == nil && wantErr == nil {
		got = gotEval
		want = wantEval
	}
	return filepath.Clean(got) == filepath.Clean(want)
}
