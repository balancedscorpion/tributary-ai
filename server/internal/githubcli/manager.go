package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/localexec"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sources"
)

type Options struct {
	Settings *config.Store
	Secrets  *secrets.Store
	Projects *projects.Store
	Sources  *sources.Store
	Runner   Runner

	CreateProject       CreateProjectFunc
	RefreshProjectIndex func(ctx context.Context, projectID string) (any, error)
}

type Manager struct {
	settings *config.Store
	secrets  *secrets.Store
	projects *projects.Store
	sources  *sources.Store
	runner   Runner

	createProject       CreateProjectFunc
	refreshProjectIndex func(ctx context.Context, projectID string) (any, error)

	mu        sync.Mutex
	approvals map[string]approvalAction
}

type CommandError struct {
	Args     []string `json:"args,omitempty"`
	ExitCode int      `json:"exit_code,omitempty"`
	TimedOut bool     `json:"timed_out,omitempty"`
	Stdout   string   `json:"stdout,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
	Message  string   `json:"message"`
}

func (e *CommandError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Stderr != "" {
		return e.Stderr
	}
	if e.Stdout != "" {
		return e.Stdout
	}
	return "gh command failed"
}

func NewManager(opts Options) *Manager {
	runner := opts.Runner
	if runner == nil {
		runner = NewDefaultRunner()
	}
	return &Manager{
		settings:            opts.Settings,
		secrets:             opts.Secrets,
		projects:            opts.Projects,
		sources:             opts.Sources,
		runner:              runner,
		createProject:       opts.CreateProject,
		refreshProjectIndex: opts.RefreshProjectIndex,
		approvals:           map[string]approvalAction{},
	}
}

func (m *Manager) Status(ctx context.Context) (StatusResult, error) {
	if err := contextErr(ctx); err != nil {
		return StatusResult{}, err
	}
	out := StatusResult{}
	if m == nil || m.secrets == nil {
		return out, nil
	}
	initialized, locked, configured := m.secrets.Status()
	out.SecretsInitialized = initialized
	out.SecretsLocked = locked
	out.Connected = configured.GitHub
	out.Login = configured.GitHubLogin
	if m.settings != nil {
		settings := m.settings.Get()
		if settings.GitHubOAuthClientID != nil && strings.TrimSpace(*settings.GitHubOAuthClientID) != "" {
			out.ClientIDConfigured = true
			out.ClientID = strings.TrimSpace(*settings.GitHubOAuthClientID)
			out.Configured = true
		}
		if settings.GitHub != nil {
			out.ReadToolsEnabled = settings.GitHub.Enabled != nil && *settings.GitHub.Enabled
			if settings.GitHub.ReadTools != nil {
				out.ReadToolsEnabled = out.ReadToolsEnabled && *settings.GitHub.ReadTools
			}
			out.AllowedRepositories = append([]string(nil), settings.GitHub.AllowedRepositories...)
			out.LastCheckedAt = cloneInt64Ptr(settings.GitHub.LastCheckedAt)
			out.LastCheckOk = cloneBoolPtr(settings.GitHub.LastCheckOk)
			if settings.GitHub.LastCheckMessage != nil {
				out.LastCheckMessage = strings.TrimSpace(*settings.GitHub.LastCheckMessage)
			}
		}
	}
	if initialized && !locked {
		if gh, err := m.secrets.GetGitHub(); err == nil {
			out.Connected = strings.TrimSpace(gh.OAuthAccessToken) != ""
			out.Login = strings.TrimSpace(gh.Login)
			out.Scope = strings.TrimSpace(gh.Scope)
		}
	}
	if !out.Connected {
		if gh, err := m.githubCLISecrets(ctx); err == nil {
			out.Connected = true
			out.Login = strings.TrimSpace(gh.Login)
			out.Scope = strings.TrimSpace(gh.Scope)
		}
	}
	if out.Connected {
		out.Configured = true
	}
	return out, nil
}

func (m *Manager) Check(ctx context.Context) (CheckResult, error) {
	status, statusErr := m.Status(ctx)
	if statusErr != nil {
		return CheckResult{}, statusErr
	}
	out := CheckResult{Status: status}
	gh, err := m.githubSecrets(ctx)
	if err != nil {
		m.applyGitHubHealth(&out.Status, false, err.Error())
		return out, err
	}
	res, err := m.runGH(ctx, gh.OAuthAccessToken, []string{"repo", "list", "--limit", "1", "--json", "nameWithOwner,url"})
	if err != nil {
		m.applyGitHubHealth(&out.Status, false, err.Error())
		return out, err
	}
	var repos []map[string]any
	if err := decodeGHJSON(res.Stdout, &repos); err != nil {
		checkErr := ghJSONError(res, err)
		m.applyGitHubHealth(&out.Status, false, checkErr.Error())
		return out, checkErr
	}
	out.OK = true
	out.Status.Connected = true
	out.Status.Configured = true
	m.applyGitHubHealth(&out.Status, true, "GitHub CLI authentication is usable.")
	return out, nil
}

func (m *Manager) applyGitHubHealth(status *StatusResult, ok bool, message string) {
	checkedAt, checkedOk, checkedMessage := m.persistGitHubHealth(ok, message)
	if status == nil {
		return
	}
	status.LastCheckedAt = &checkedAt
	status.LastCheckOk = &checkedOk
	status.LastCheckMessage = checkedMessage
	status.Message = checkedMessage
}

func (m *Manager) persistGitHubHealth(ok bool, message string) (int64, bool, string) {
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		if ok {
			checkedMessage = "GitHub connection verified."
		} else {
			checkedMessage = "GitHub connection check failed."
		}
	}
	if m == nil || m.settings == nil {
		return checkedAt, ok, checkedMessage
	}
	next := m.settings.Get()
	if next.GitHub == nil {
		next.GitHub = config.DefaultSettings().GitHub
	}
	if next.GitHub == nil {
		next.GitHub = &config.GitHubSettings{}
	}
	next.GitHub.LastCheckedAt = &checkedAt
	next.GitHub.LastCheckOk = &ok
	next.GitHub.LastCheckMessage = &checkedMessage
	_ = m.settings.Save(next)
	return checkedAt, ok, checkedMessage
}

func (m *Manager) githubSecrets(ctx context.Context) (secrets.GitHubSecrets, error) {
	if err := contextErr(ctx); err != nil {
		return secrets.GitHubSecrets{}, err
	}
	if m == nil || m.secrets == nil {
		return secrets.GitHubSecrets{}, errors.New("GitHub secrets store is not configured")
	}
	gh, err := m.secrets.GetGitHub()
	if err != nil {
		if gh, cliErr := m.githubCLISecrets(ctx); cliErr == nil {
			return gh, nil
		}
		return secrets.GitHubSecrets{}, err
	}
	if strings.TrimSpace(gh.OAuthAccessToken) == "" {
		if gh, cliErr := m.githubCLISecrets(ctx); cliErr == nil {
			return gh, nil
		}
		return secrets.GitHubSecrets{}, errors.New("GitHub OAuth token is not configured")
	}
	return gh, nil
}

func (m *Manager) githubCLISecrets(ctx context.Context) (secrets.GitHubSecrets, error) {
	if !m.allowHostGitHubCLIAuth() {
		return secrets.GitHubSecrets{}, errors.New("GitHub CLI host authentication is not available")
	}
	token, err := hostGitHubCLIToken(ctx)
	if err != nil {
		return secrets.GitHubSecrets{}, err
	}
	gh := secrets.GitHubSecrets{
		OAuthAccessToken: token,
		TokenType:        "bearer",
	}
	if res, err := m.runGH(ctx, token, []string{"api", "user", "--jq", ".login"}); err == nil {
		gh.Login = strings.TrimSpace(res.Stdout)
	}
	return gh, nil
}

func (m *Manager) allowHostGitHubCLIAuth() bool {
	if m == nil {
		return false
	}
	_, ok := m.runner.(*DefaultRunner)
	return ok
}

func hostGitHubCLIToken(ctx context.Context) (string, error) {
	cmd := localexec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com")
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.Output()
	token := strings.TrimSpace(string(out))
	if err != nil {
		return "", errors.New("GitHub CLI authentication is not available")
	}
	if token == "" {
		return "", errors.New("GitHub CLI did not return a token")
	}
	return token, nil
}

func (m *Manager) runGH(ctx context.Context, token string, args []string) (RunResult, error) {
	return m.runGHWithOptions(ctx, token, args, defaultCommandTimeout, defaultStdoutCap, defaultStderrCap)
}

func (m *Manager) runGHWithOptions(ctx context.Context, token string, args []string, timeout time.Duration, stdoutCap int64, stderrCap int64) (RunResult, error) {
	return m.runGHWithOptionsInDir(ctx, token, args, timeout, stdoutCap, stderrCap, "")
}

func (m *Manager) runGHWithOptionsInDir(ctx context.Context, token string, args []string, timeout time.Duration, stdoutCap int64, stderrCap int64, workDir string) (RunResult, error) {
	if err := contextErr(ctx); err != nil {
		return RunResult{Args: append([]string(nil), args...), ExitCode: -1}, err
	}
	if m == nil {
		return RunResult{Args: append([]string(nil), args...), ExitCode: -1}, errors.New("GitHub CLI manager is not configured")
	}
	runner := m.runner
	if runner == nil {
		runner = NewDefaultRunner()
	}
	req := RunRequest{
		Args:      append([]string(nil), args...),
		Token:     strings.TrimSpace(token),
		Timeout:   timeout,
		StdoutCap: stdoutCap,
		StderrCap: stderrCap,
		WorkDir:   strings.TrimSpace(workDir),
	}
	res, err := runner.Run(ctx, req)
	res.Args = append([]string(nil), req.Args...)
	res = redactRunResult(res, token)
	if err != nil {
		return res, commandError(res, err, token)
	}
	return res, nil
}

func commandError(res RunResult, err error, token string) error {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if msg == "" {
		msg = "gh command failed"
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

func ghJSONError(res RunResult, err error) error {
	msg := fmt.Sprintf("failed to parse gh JSON output: %v", err)
	if strings.TrimSpace(res.Stderr) != "" {
		msg += ": " + strings.TrimSpace(res.Stderr)
	} else if strings.TrimSpace(res.Stdout) != "" {
		msg += ": " + strings.TrimSpace(res.Stdout)
	}
	return &CommandError{
		Args:     append([]string(nil), res.Args...),
		ExitCode: res.ExitCode,
		TimedOut: res.TimedOut,
		Stdout:   res.Stdout,
		Stderr:   res.Stderr,
		Message:  msg,
	}
}

func decodeGHJSON(text string, out any) error {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	return dec.Decode(out)
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func normalizeLimit(limit int, fallback int, max int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > max {
		return max
	}
	return limit
}

func normalizeState(raw string, fallback string, allowed map[string]struct{}) (string, error) {
	state := strings.TrimSpace(strings.ToLower(raw))
	if state == "" {
		state = fallback
	}
	if _, ok := allowed[state]; !ok {
		return "", fmt.Errorf("invalid state %q (allowed: %s)", raw, strings.Join(sortedKeys(allowed), ", "))
	}
	return state, nil
}

func safeFlagValue(name string, value string, maxLen int) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if maxLen <= 0 {
		maxLen = 200
	}
	if len(value) > maxLen || strings.HasPrefix(value, "-") || strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("invalid %s", name)
	}
	return value, nil
}

func positiveNumber(name string, n int) error {
	if n <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	return nil
}

func positiveInt64(name string, n int64) error {
	if n <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	return nil
}

func numberArg(n int) string {
	return strconv.Itoa(n)
}

func int64Arg(n int64) string {
	return strconv.FormatInt(n, 10)
}

func cloneInt64Ptr(src *int64) *int64 {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func cloneBoolPtr(src *bool) *bool {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
