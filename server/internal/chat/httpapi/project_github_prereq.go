package httpapi

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"time"

	"ai-analyst-local/internal/localexec"
)

// githubPrereqTool describes whether a local CLI dependency is available.
type githubPrereqTool struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
}

// githubPrereqStep is a concise, actionable instruction for a missing
// prerequisite shown directly in the sign-in path.
type githubPrereqStep struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
	Command     string `json:"command,omitempty"`
}

type githubPrerequisites struct {
	Git                githubPrereqTool   `json:"git"`
	GH                 githubPrereqTool   `json:"gh"`
	GHAuthenticated    bool               `json:"gh_authenticated"`
	GHLogin            string             `json:"gh_login,omitempty"`
	ClientIDConfigured bool               `json:"client_id_configured"`
	Ready              bool               `json:"ready"`
	Missing            []githubPrereqStep `json:"missing"`
}

func (h *Handler) handleGitHubAuthPrerequisites(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, map[string]any{"prerequisites": h.githubAuthPrerequisites(ctx)})
}

// githubAuthPrerequisites inspects the local environment for the tools the
// GitHub sign-in flow depends on. When a custom OAuth client ID is configured
// the browser device-code flow is used and the GitHub CLI is optional;
// otherwise sign-in relies on an authenticated `gh` CLI on this machine.
func (h *Handler) githubAuthPrerequisites(ctx context.Context) githubPrerequisites {
	prereq := githubPrerequisites{Missing: []githubPrereqStep{}}
	prereq.ClientIDConfigured = h.githubOAuthClientID() != ""

	prereq.Git = detectLocalCommand(ctx, "git", "--version")
	prereq.GH = detectLocalCommand(ctx, "gh", "--version")

	if prereq.GH.Installed {
		profile := loadGitProfile(ctx)
		prereq.GHAuthenticated = profile.GitHubAvailable
		prereq.GHLogin = strings.TrimSpace(profile.GitHubLogin)
	}

	if !prereq.Git.Installed {
		prereq.Missing = append(prereq.Missing, githubPrereqStep{
			Key:         "git",
			Title:       "Install Git",
			Instruction: gitInstallInstruction(),
			Command:     packageInstallCommand("git"),
		})
	}

	if !prereq.ClientIDConfigured {
		switch {
		case !prereq.GH.Installed:
			prereq.Missing = append(prereq.Missing, githubPrereqStep{
				Key:         "gh",
				Title:       "Install the GitHub CLI",
				Instruction: ghInstallInstruction(),
				Command:     packageInstallCommand("gh"),
			})
		case !prereq.GHAuthenticated:
			prereq.Missing = append(prereq.Missing, githubPrereqStep{
				Key:         "gh-auth",
				Title:       "Sign in to the GitHub CLI",
				Instruction: "Authenticate the GitHub CLI, then re-check to connect.",
				Command:     "gh auth login",
			})
		}
	}

	prereq.Ready = prereq.Git.Installed &&
		(prereq.ClientIDConfigured || (prereq.GH.Installed && prereq.GHAuthenticated))
	return prereq
}

func detectLocalCommand(ctx context.Context, name string, versionArgs ...string) githubPrereqTool {
	tool := githubPrereqTool{}
	path, ok := localexec.ResolveCommand(name)
	if !ok {
		return tool
	}
	tool.Installed = true
	tool.Path = path
	if len(versionArgs) > 0 {
		if out, err := runCommandOutput(ctx, path, versionArgs...); err == nil {
			tool.Version = firstNonEmptyLine(out)
		}
	}
	return tool
}

func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func gitInstallInstruction() string {
	switch runtime.GOOS {
	case "darwin":
		return "Install Git with Homebrew, or download it from git-scm.com."
	case "windows":
		return "Install Git for Windows from git-scm.com."
	default:
		return "Install Git with your package manager, or from git-scm.com."
	}
}

func ghInstallInstruction() string {
	switch runtime.GOOS {
	case "darwin":
		return "Install the GitHub CLI with Homebrew, or from cli.github.com."
	case "windows":
		return "Install the GitHub CLI from cli.github.com."
	default:
		return "Install the GitHub CLI with your package manager, or from cli.github.com."
	}
}

func packageInstallCommand(pkg string) string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install " + pkg
	case "linux":
		return "sudo apt-get install " + pkg
	default:
		return ""
	}
}
