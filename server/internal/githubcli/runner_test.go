package githubcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	base := filepath.Base(os.Args[0])
	if base == "gh" || base == "gh.exe" {
		fakeGHMain()
		return
	}
	os.Exit(m.Run())
}

func fakeGHMain() {
	cwd, _ := os.Getwd()
	payload := map[string]any{
		"args":               os.Args[1:],
		"cwd":                cwd,
		"gh_config_dir":      os.Getenv("GH_CONFIG_DIR"),
		"has_gh_token":       os.Getenv("GH_TOKEN") != "",
		"gh_token_value":     os.Getenv("GH_TOKEN"),
		"gh_prompt_disabled": os.Getenv("GH_PROMPT_DISABLED"),
		"no_update_notifier": os.Getenv("GH_NO_UPDATE_NOTIFIER"),
		"no_color":           os.Getenv("NO_COLOR"),
		"clicolor":           os.Getenv("CLICOLOR"),
		"has_parent_secret":  os.Getenv("GITHUBCLI_PARENT_SECRET") != "",
	}
	_ = json.NewEncoder(os.Stdout).Encode(payload)
	fmt.Fprintf(os.Stdout, "stdout token %s ghp_abcdefghijklmnopqrstuvwxyz123456\n", os.Getenv("GH_TOKEN"))
	fmt.Fprintf(os.Stderr, "stderr token %s github_pat_abcdefghijklmnopqrstuvwxyz_abcdefghijklmnopqrstuvwxyz123456789\n", os.Getenv("GH_TOKEN"))
}

func TestDefaultRunnerIsolatesEnvAndRedacts(t *testing.T) {
	dir := t.TempDir()
	ghName := "gh"
	if runtime.GOOS == "windows" {
		ghName = "gh.exe"
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	fakePath := filepath.Join(dir, ghName)
	copyExecutable(t, exe, fakePath)

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath)
	t.Setenv("GITHUBCLI_PARENT_SECRET", "must-not-leak")

	token := "ghs_exactsecretvalue123456789012345"
	res, err := NewDefaultRunner().Run(context.Background(), RunRequest{
		Args:    []string{"api", "user"},
		Token:   token,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v\nstderr=%s", err, res.Stderr)
	}
	if strings.Contains(res.Stdout, token) || strings.Contains(res.Stderr, token) {
		t.Fatalf("exact token was not redacted:\nstdout=%s\nstderr=%s", res.Stdout, res.Stderr)
	}
	if strings.Contains(res.Stdout, "ghp_abcdefghijklmnopqrstuvwxyz123456") || strings.Contains(res.Stderr, "github_pat_") {
		t.Fatalf("GitHub token pattern was not redacted:\nstdout=%s\nstderr=%s", res.Stdout, res.Stderr)
	}

	firstLine := strings.SplitN(res.Stdout, "\n", 2)[0]
	var payload map[string]any
	if err := json.Unmarshal([]byte(firstLine), &payload); err != nil {
		t.Fatalf("decode fake gh payload: %v\nstdout=%s", err, res.Stdout)
	}
	if payload["has_gh_token"] != true {
		t.Fatalf("GH_TOKEN was not set in child env: %#v", payload)
	}
	if payload["gh_token_value"] != redactedGitHubToken {
		t.Fatalf("GH_TOKEN value was not redacted in output: %#v", payload["gh_token_value"])
	}
	if payload["has_parent_secret"] != false {
		t.Fatalf("parent env leaked into child: %#v", payload)
	}
	if payload["gh_prompt_disabled"] != "1" || payload["no_update_notifier"] != "1" || payload["no_color"] != "1" || payload["clicolor"] != "0" {
		t.Fatalf("expected gh noninteractive env, got %#v", payload)
	}
	configDir, _ := payload["gh_config_dir"].(string)
	if configDir == "" {
		t.Fatalf("GH_CONFIG_DIR missing from child env: %#v", payload)
	}
	if _, err := os.Stat(configDir); !os.IsNotExist(err) {
		t.Fatalf("GH_CONFIG_DIR was not cleaned up; stat err=%v", err)
	}
	cwd, _ := payload["cwd"].(string)
	if cwd == "" {
		t.Fatalf("child cwd missing from payload: %#v", payload)
	}
	if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Fatalf("neutral cwd was not cleaned up; stat err=%v", err)
	}
}

func TestSanitizedGHEnvWindowsPrependsGitSearchDirs(t *testing.T) {
	t.Setenv("PATH", `C:\Existing\bin`)
	t.Setenv("Path", "")
	t.Setenv("ProgramFiles", `C:\Program Files`)
	t.Setenv("ProgramFiles(x86)", `C:\Program Files (x86)`)
	t.Setenv("LOCALAPPDATA", `C:\Users\Alex\AppData\Local`)

	env := sanitizedGHEnvForOS("ghs_exactsecretvalue123456789012345", `C:\Config`, "windows")
	pathValue := ""
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			pathValue = strings.TrimPrefix(item, "PATH=")
			break
		}
	}
	if pathValue == "" {
		t.Fatalf("windows PATH was not set in sanitized gh env: %#v", env)
	}
	parts := strings.Split(pathValue, ";")
	wantFirst := filepath.Join(`C:\Users\Alex\AppData\Local`, "Programs", "Git", "cmd")
	if len(parts) == 0 || parts[0] != wantFirst {
		t.Fatalf("PATH first entry = %q, want %q in %q", firstPathEntry(parts), wantFirst, pathValue)
	}
	if !strings.Contains(pathValue, filepath.Join(`C:\Program Files`, "Git", "cmd")) {
		t.Fatalf("PATH missing Program Files Git fallback: %q", pathValue)
	}
	if !strings.Contains(pathValue, `C:\Existing\bin`) {
		t.Fatalf("PATH missing original entry: %q", pathValue)
	}
}

func firstPathEntry(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func copyExecutable(t *testing.T, src string, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open test executable: %v", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatalf("create fake gh: %v", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy fake gh: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close fake gh: %v", err)
	}
}
