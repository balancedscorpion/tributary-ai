package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/sources"
)

func TestGitToolsReadVisibleRepositoryHistory(t *testing.T) {
	requireGit(t)
	ps := newGitToolProjectStore(t)
	repoDir := initToolTestRepo(t, ps, "proj", "repo")

	writeRepoFile(t, repoDir, "app.txt", "alpha\n")
	runRepoGit(t, repoDir, "add", "app.txt")
	runRepoGit(t, repoDir, "commit", "-m", "add alpha")
	firstCommit := runRepoGit(t, repoDir, "rev-parse", "HEAD")

	writeRepoFile(t, repoDir, "app.txt", "alpha\nbeta\n")
	runRepoGit(t, repoDir, "add", "app.txt")
	runRepoGit(t, repoDir, "commit", "-m", "add beta")

	execCtx := ExecContext{ProjectID: "proj", Projects: ps}

	reposOut, repos := executeGitToolOutput(t, newListGitRepositoriesTool(), execCtx, `{}`)
	reposMeta := requireLocalFilesResultMetadata(t, reposOut, "list_git_repositories", "repository_list")
	if reposMeta["repository_count"] != 1 {
		t.Fatalf("repository_count metadata=%#v, want 1", reposMeta)
	}
	if repos["total_count"].(int) != 1 {
		t.Fatalf("total_count=%v, want 1; result=%#v", repos["total_count"], repos)
	}

	refsOut, refsResult := executeGitToolOutput(t, newGitListRefsTool(), execCtx, `{"repo_path":"repo","limit":5}`)
	refsMeta := requireLocalFilesResultMetadata(t, refsOut, "git_list_refs", "git_refs")
	if refsMeta["repo_path"] != "repo" {
		t.Fatalf("git_list_refs metadata=%#v, want repo_path repo", refsMeta)
	}
	if got := len(asAnySlice(t, refsResult["refs"])); got == 0 {
		t.Fatalf("refs=%d, want at least one ref; result=%#v", got, refsResult)
	}

	logOut, logResult := executeGitToolOutput(t, newGitLogTool(), execCtx, `{"repo_path":"repo","max_count":5}`)
	logMeta := requireLocalFilesResultMetadata(t, logOut, "git_log", "git_commit_history")
	if logMeta["commit_count"] != 2 {
		t.Fatalf("git_log metadata=%#v, want commit_count 2", logMeta)
	}
	commits := asAnySlice(t, logResult["commits"])
	if len(commits) != 2 {
		t.Fatalf("len(commits)=%d, want 2; result=%#v", len(commits), logResult)
	}

	searchOut, searchResult := executeGitToolOutput(t, newGitSearchHistoryTool(), execCtx, `{"repo_path":"repo","query":"beta","max_count":5}`)
	searchMeta := requireLocalFilesResultMetadata(t, searchOut, "git_search_history", "git_history_search")
	if searchMeta["query"] != "beta" || searchMeta["commit_count"] != 1 {
		t.Fatalf("git_search_history metadata=%#v, want beta one-commit metadata", searchMeta)
	}
	matches := asAnySlice(t, searchResult["commits"])
	if len(matches) != 1 {
		t.Fatalf("len(history matches)=%d, want 1; result=%#v", len(matches), searchResult)
	}
	if got := strings.TrimSpace(matches[0].(map[string]any)["subject"].(string)); got != "add beta" {
		t.Fatalf("history subject=%q, want add beta", got)
	}

	grepOut, grepResult := executeGitToolOutput(t, newGitGrepTool(), execCtx, `{"repo_path":"repo","query":"alpha","ref":"HEAD","max_matches":5}`)
	grepMeta := requireLocalFilesResultMetadata(t, grepOut, "git_grep", "git_file_search")
	if grepMeta["query"] != "alpha" || grepMeta["matches_count"] != 1 {
		t.Fatalf("git_grep metadata=%#v, want alpha one-match metadata", grepMeta)
	}
	grepMatches := asAnySlice(t, grepResult["matches"])
	if len(grepMatches) != 1 {
		t.Fatalf("len(grep matches)=%d, want 1; result=%#v", len(grepMatches), grepResult)
	}

	commitOut, commitResult := executeGitToolOutput(t, newGitShowCommitTool(), execCtx, `{"repo_path":"repo","ref":"`+firstCommit+`"}`)
	commitMeta := requireLocalFilesResultMetadata(t, commitOut, "git_show_commit", "git_commit")
	if commitMeta["repo_path"] != "repo" || commitMeta["ref"] != firstCommit {
		t.Fatalf("git_show_commit metadata=%#v, want repo/ref metadata", commitMeta)
	}
	if !strings.Contains(commitResult["content"].(string), "add alpha") {
		t.Fatalf("commit content=%q, want add alpha", commitResult["content"])
	}

	fileOut, fileAtRef := executeGitToolOutput(t, newGitShowFileAtRefTool(), execCtx, `{"repo_path":"repo","ref":"`+firstCommit+`","path":"app.txt"}`)
	fileMeta := requireLocalFilesResultMetadata(t, fileOut, "git_show_file_at_ref", "git_file")
	if fileMeta["path"] != "app.txt" || fileMeta["ref"] != firstCommit {
		t.Fatalf("git_show_file_at_ref metadata=%#v, want path/ref metadata", fileMeta)
	}
	content := fileAtRef["content"].(string)
	if strings.Contains(content, "beta") || !strings.Contains(content, "alpha") {
		t.Fatalf("file content at first commit=%q, want alpha only", content)
	}
}

func TestGitToolsDoNotExposeHiddenSources(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	ps := newGitToolProjectStore(t)
	repoDir := initToolTestRepo(t, ps, "proj", "repo")
	writeRepoFile(t, repoDir, "app.txt", "alpha\n")
	runRepoGit(t, repoDir, "add", "app.txt")
	runRepoGit(t, repoDir, "commit", "-m", "add alpha")

	if err := ps.SetSourceHidden(ctx, "proj", "repo", true); err != nil {
		t.Fatalf("SetSourceHidden: %v", err)
	}

	out, err := newListGitRepositoriesTool().Execute(ctx, ExecContext{ProjectID: "proj", Projects: ps}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("hidden repository should not be exposed, got %#v", out.Result)
	}
	raw, _ := json.Marshal(out.Result)
	if !strings.Contains(string(raw), "No visible Git repositories") {
		t.Fatalf("unexpected hidden repo error: %s", raw)
	}
}

func TestGitToolsSkipSensitivePathsInBroadReads(t *testing.T) {
	requireGit(t)
	ps := newGitToolProjectStore(t)
	repoDir := initToolTestRepo(t, ps, "proj", "repo")

	writeRepoFile(t, repoDir, "public.txt", "SECRET public marker\n")
	writeRepoFile(t, repoDir, ".env", "SECRET_TOKEN=env\n")
	writeRepoFile(t, repoDir, ".ssh/id_ed25519", "SECRET ssh key\n")
	writeRepoFile(t, repoDir, "src/client.pem", "SECRET pem key\n")
	runRepoGit(t, repoDir, "add", "public.txt", ".env", ".ssh/id_ed25519", "src/client.pem")
	runRepoGit(t, repoDir, "commit", "-m", "add files")
	commit := runRepoGit(t, repoDir, "rev-parse", "HEAD")

	execCtx := ExecContext{ProjectID: "proj", Projects: ps}

	_, grepResult := executeGitToolOutput(t, newGitGrepTool(), execCtx, `{"repo_path":"repo","query":"SECRET","ref":"HEAD","max_matches":20}`)
	grepMatches := asAnySlice(t, grepResult["matches"])
	if len(grepMatches) != 1 {
		t.Fatalf("grep matches=%#v, want only public.txt", grepMatches)
	}
	if got := grepMatches[0].(map[string]any)["path"]; got != "public.txt" {
		t.Fatalf("grep path=%#v, want public.txt; matches=%#v", got, grepMatches)
	}

	_, explicitGrep := executeGitToolOutput(t, newGitGrepTool(), execCtx, `{"repo_path":"repo","query":"SECRET_TOKEN","ref":"HEAD","path":".env","max_matches":20}`)
	if got := len(asAnySlice(t, explicitGrep["matches"])); got != 1 {
		t.Fatalf("explicit sensitive grep matches=%d, want 1; result=%#v", got, explicitGrep)
	}

	_, searchResult := executeGitToolOutput(t, newGitSearchHistoryTool(), execCtx, `{"repo_path":"repo","query":"SECRET","max_count":5}`)
	for _, commit := range asAnySlice(t, searchResult["commits"]) {
		for _, file := range asAnySliceOptional(commit.(map[string]any)["changed_files"]) {
			path := fmt.Sprint(file.(map[string]any)["path"])
			if path == ".env" || strings.Contains(path, ".ssh/") || strings.HasSuffix(path, ".pem") {
				t.Fatalf("git_search_history exposed sensitive changed file %q in %#v", path, searchResult["commits"])
			}
		}
	}

	_, logResult := executeGitToolOutput(t, newGitLogTool(), execCtx, `{"repo_path":"repo","max_count":5}`)
	for _, commit := range asAnySlice(t, logResult["commits"]) {
		for _, file := range asAnySliceOptional(commit.(map[string]any)["changed_files"]) {
			path := fmt.Sprint(file.(map[string]any)["path"])
			if path == ".env" || strings.Contains(path, ".ssh/") || strings.HasSuffix(path, ".pem") {
				t.Fatalf("git_log exposed sensitive changed file %q in %#v", path, logResult["commits"])
			}
		}
	}

	_, commitResult := executeGitToolOutput(t, newGitShowCommitTool(), execCtx, `{"repo_path":"repo","ref":"`+commit+`","max_bytes":262144}`)
	content := commitResult["content"].(string)
	for _, forbidden := range []string{"SECRET_TOKEN", ".env", "id_ed25519", "client.pem", "SECRET ssh key", "SECRET pem key"} {
		if strings.Contains(content, forbidden) {
			t.Fatalf("git_show_commit exposed %q in content:\n%s", forbidden, content)
		}
	}
	if !strings.Contains(content, "public.txt") || !strings.Contains(content, "SECRET public marker") {
		t.Fatalf("git_show_commit content missing public file diff:\n%s", content)
	}
}

func TestListGitRepositoriesInfersSingleGitProject(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	cfgDir := t.TempDir()
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	project, err := st.CreateProject(ctx, "Repo Project", "git:https://github.com/acme/widget.git")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	repoDir := initToolTestRepo(t, ps, project.ID, "repo")
	writeRepoFile(t, repoDir, "app.txt", "alpha\n")
	runRepoGit(t, repoDir, "add", "app.txt")
	runRepoGit(t, repoDir, "commit", "-m", "add alpha")
	if _, err := st.ReplaceProjectFiles(ctx, project.ID, []store.ProjectFileStat{{
		Path:     "repo/app.txt",
		ByteSize: 6,
	}}); err != nil {
		t.Fatalf("ReplaceProjectFiles: %v", err)
	}

	result := executeGitTool(t, newListGitRepositoriesTool(), ExecContext{Projects: ps, Store: st}, `{}`)
	if result["total_count"].(int) != 1 {
		t.Fatalf("total_count=%v, want 1; result=%#v", result["total_count"], result)
	}
	repos := asAnySlice(t, result["repositories"])
	repo := repos[0].(map[string]any)
	if repo["project_id"] != project.ID {
		t.Fatalf("project_id=%v, want %s; repo=%#v", repo["project_id"], project.ID, repo)
	}
	if repo["repo_path"] != "repo" {
		t.Fatalf("repo_path=%v, want repo; repo=%#v", repo["repo_path"], repo)
	}
}

func TestGitToolsUseAppManagedGitHubSourcesWithoutProject(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	sourceStore, err := sources.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("sources.NewStore: %v", err)
	}
	repoDir := filepath.Join(t.TempDir(), "widget")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatalf("MkdirAll repo: %v", err)
	}
	runRepoGit(t, repoDir, "init")
	runRepoGit(t, repoDir, "config", "user.name", "Test User")
	runRepoGit(t, repoDir, "config", "user.email", "test@example.com")
	runRepoGit(t, repoDir, "remote", "add", "origin", "https://github.com/acme/widget.git")
	writeRepoFile(t, repoDir, "app.txt", "alpha source-backed clone\n")
	runRepoGit(t, repoDir, "add", "app.txt")
	runRepoGit(t, repoDir, "commit", "-m", "add app")
	_, err = sourceStore.Upsert(ctx, sources.Source{
		ID:         sources.GitHubSourceID("acme/widget"),
		Kind:       sources.KindGitHubClone,
		Repository: "acme/widget",
		Owner:      "acme",
		Repo:       "widget",
		CloneURL:   "https://github.com/acme/widget.git",
		Worktree:   repoDir,
	})
	if err != nil {
		t.Fatalf("source Upsert: %v", err)
	}
	execCtx := ExecContext{Sources: sourceStore}

	result := executeGitTool(t, newListGitRepositoriesTool(), execCtx, `{}`)
	if result["total_count"].(int) != 1 {
		t.Fatalf("total_count=%v, want 1; result=%#v", result["total_count"], result)
	}
	repos := asAnySlice(t, result["repositories"])
	repo := repos[0].(map[string]any)
	if repo["source_kind"] != sources.KindGitHubClone || repo["repo_path"] != "acme/widget" || repo["repository"] != "acme/widget" {
		t.Fatalf("unexpected source-backed repo: %#v", repo)
	}

	_, grepResult := executeGitToolOutput(t, newGitGrepTool(), execCtx, `{"repo_path":"acme/widget","query":"source-backed","ref":"HEAD","max_matches":5}`)
	matches := asAnySlice(t, grepResult["matches"])
	if len(matches) != 1 || matches[0].(map[string]any)["path"] != "app.txt" {
		t.Fatalf("unexpected source-backed grep matches: %#v", matches)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git executable not available")
	}
}

func newGitToolProjectStore(t *testing.T) *projects.Store {
	t.Helper()
	ps, err := projects.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	if _, err := ps.EnsureFilesDir("proj"); err != nil {
		t.Fatalf("EnsureFilesDir: %v", err)
	}
	return ps
}

func initToolTestRepo(t *testing.T, ps *projects.Store, projectID string, name string) string {
	t.Helper()
	filesDir, err := ps.FilesDir(projectID)
	if err != nil {
		t.Fatalf("FilesDir: %v", err)
	}
	repoDir := filepath.Join(filesDir, name)
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatalf("MkdirAll repo: %v", err)
	}
	runRepoGit(t, repoDir, "init")
	runRepoGit(t, repoDir, "config", "user.name", "Test User")
	runRepoGit(t, repoDir, "config", "user.email", "test@example.com")
	return repoDir
}

func writeRepoFile(t *testing.T, repoDir string, rel string, content string) {
	t.Helper()
	full := filepath.Join(repoDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("MkdirAll file parent: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func runRepoGit(t *testing.T, repoDir string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", repoDir}, args...)
	cmd := exec.Command("git", cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func executeGitTool(t *testing.T, tool Tool, execCtx ExecContext, args string) map[string]any {
	t.Helper()
	_, result := executeGitToolOutput(t, tool, execCtx, args)
	return result
}

func executeGitToolOutput(t *testing.T, tool Tool, execCtx ExecContext, args string) (Output, map[string]any) {
	t.Helper()
	out, err := tool.Execute(context.Background(), execCtx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("tool returned error: %#v", out.Result)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result type=%T, want map", out.Result)
	}
	return out, result
}

func asAnySlice(t *testing.T, v any) []any {
	t.Helper()
	items, ok := v.([]map[string]any)
	if ok {
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		return out
	}
	generic, ok := v.([]any)
	if !ok {
		t.Fatalf("value type=%T, want slice", v)
	}
	return generic
}

func asAnySliceOptional(v any) []any {
	if v == nil {
		return nil
	}
	if items, ok := v.([]map[string]any); ok {
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, item)
		}
		return out
	}
	if generic, ok := v.([]any); ok {
		return generic
	}
	return nil
}
