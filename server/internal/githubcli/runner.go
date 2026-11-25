package githubcli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"ai-analyst-local/internal/localexec"
)

const (
	defaultCommandTimeout = 30 * time.Second
	defaultStdoutCap      = int64(512 * 1024)
	defaultStderrCap      = int64(128 * 1024)
)

type DefaultRunner struct{}

func NewDefaultRunner() *DefaultRunner {
	return &DefaultRunner{}
}

func (r *DefaultRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args := append([]string(nil), req.Args...)
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	stdoutCap := req.StdoutCap
	if stdoutCap <= 0 {
		stdoutCap = defaultStdoutCap
	}
	stderrCap := req.StderrCap
	if stderrCap <= 0 {
		stderrCap = defaultStderrCap
	}

	configDir, err := os.MkdirTemp("", "ai-analyst-gh-config-*")
	if err != nil {
		return RunResult{Args: args, ExitCode: -1}, redactError(err, req.Token)
	}
	defer os.RemoveAll(configDir)

	workDir := strings.TrimSpace(req.WorkDir)
	if workDir == "" {
		var err error
		workDir, err = os.MkdirTemp("", "ai-analyst-gh-cwd-*")
		if err != nil {
			return RunResult{Args: args, ExitCode: -1}, redactError(err, req.Token)
		}
		defer os.RemoveAll(workDir)
	}

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ghPath, ok := localexec.ResolveCommand("gh")
	if !ok {
		return RunResult{Args: args, ExitCode: -1}, errors.New("GitHub CLI executable was not found")
	}
	cmd := exec.CommandContext(runCtx, ghPath, args...)
	cmd.Dir = workDir
	cmd.Env = sanitizedGHEnv(req.Token, configDir)
	cmd.Stdin = strings.NewReader("")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{Args: args, ExitCode: -1}, redactError(err, req.Token)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return RunResult{Args: args, ExitCode: -1}, redactError(err, req.Token)
	}
	if err := cmd.Start(); err != nil {
		return RunResult{Args: args, ExitCode: -1}, redactError(err, req.Token)
	}

	type readResult struct {
		text      string
		truncated bool
		err       error
	}
	stdoutCh := make(chan readResult, 1)
	stderrCh := make(chan readResult, 1)
	go func() {
		text, truncated, err := readLimitedAndDrain(stdout, stdoutCap)
		stdoutCh <- readResult{text: text, truncated: truncated, err: err}
	}()
	go func() {
		text, truncated, err := readLimitedAndDrain(stderr, stderrCap)
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
	res = redactRunResult(res, req.Token)
	return res, redactError(waitErr, req.Token)
}

func sanitizedGHEnv(token string, configDir string) []string {
	return sanitizedGHEnvForOS(token, configDir, runtime.GOOS)
}

func sanitizedGHEnvForOS(token string, configDir string, goos string) []string {
	env := []string{
		"GH_CONFIG_DIR=" + configDir,
		"GH_PROMPT_DISABLED=1",
		"GH_NO_UPDATE_NOTIFIER=1",
		"NO_COLOR=1",
		"CLICOLOR=0",
	}
	if strings.TrimSpace(token) != "" {
		env = append(env, "GH_TOKEN="+strings.TrimSpace(token))
	}
	if goos == "windows" {
		pathKey := "PATH"
		pathValue := os.Getenv("PATH")
		if pathValue == "" {
			if value := os.Getenv("Path"); value != "" {
				pathKey = "Path"
				pathValue = value
			}
		}
		env = append(env, pathKey+"="+withCommandSearchPathForOS(pathValue, goos))
		appendIfSet := func(key string) {
			if value := os.Getenv(key); value != "" {
				env = append(env, key+"="+value)
			}
		}
		appendIfSet("SYSTEMROOT")
		appendIfSet("SystemRoot")
	}
	return env
}

func withCommandSearchPathForOS(existing string, goos string) string {
	dirs := localexec.CommandSearchDirsForOS(goos)
	if len(dirs) == 0 {
		return existing
	}
	seen := map[string]bool{}
	parts := make([]string, 0, len(dirs)+1)
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		key := strings.ToLower(dir)
		if seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, dir)
	}
	for _, dir := range splitPathListForOS(existing, goos) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		key := strings.ToLower(dir)
		if seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, dir)
	}
	return strings.Join(parts, pathListSeparatorForOS(goos))
}

func splitPathListForOS(value string, goos string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, pathListSeparatorForOS(goos))
}

func pathListSeparatorForOS(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return string(os.PathListSeparator)
}

func readLimitedAndDrain(r io.Reader, limit int64) (string, bool, error) {
	if limit <= 0 {
		limit = defaultStdoutCap
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return "", false, err
	}
	truncated := n > limit
	if truncated {
		_, _ = io.Copy(io.Discard, r)
		b := buf.Bytes()
		if int64(len(b)) > limit {
			b = b[:limit]
		}
		return string(b), true, nil
	}
	return buf.String(), false, nil
}
