package optionc

import (
	"strings"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func githubEnabledFromSettings(settings *config.AppSettings) bool {
	if settings == nil || settings.GitHub == nil || settings.GitHub.Enabled == nil || !*settings.GitHub.Enabled {
		return false
	}
	return settings.GitHub.ReadTools == nil || *settings.GitHub.ReadTools
}

func isGitHubToolAllowedForRun(toolName string, enabled bool) bool {
	if !tools.IsGitHubToolName(strings.TrimSpace(toolName)) {
		return true
	}
	return enabled
}

func filterToolsByGitHubEnabled(toolDefs []types.ToolDefinition, enabled bool) []types.ToolDefinition {
	out := make([]types.ToolDefinition, 0, len(toolDefs))
	for _, def := range toolDefs {
		if !isGitHubToolAllowedForRun(def.Function.Name, enabled) {
			continue
		}
		out = append(out, def)
	}
	return out
}

func embedGitHubGuidance(systemPrompt string, enabled bool) string {
	if !enabled {
		return systemPrompt
	}
	return strings.Join([]string{
		strings.TrimSpace(systemPrompt),
		"",
		"GitHub tools:",
		"- GitHub MCP tools use stored app GitHub auth through isolated gh and managed git subprocesses.",
		"- Prefer project repository context when choosing repositories, pull requests, issues, workflows, or code search scope.",
		"- Never request, reveal, paste, or expose GitHub tokens or other credentials.",
		"- GitHub clone, pull/refresh, and raw gh write commands only queue approvals. The user must approve the exact command or repository action in the chat UI before it runs.",
		"- For large repositories, prefer partial clone, shallow clone, or sparse checkout paths before requesting a full clone.",
	}, "\n")
}
