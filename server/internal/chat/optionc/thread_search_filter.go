package optionc

import (
	"strings"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func threadSearchEnabledFromSettings(settings *config.AppSettings) bool {
	if settings == nil {
		return false
	}
	if settings.ThreadSearch == nil || settings.ThreadSearch.Enabled == nil {
		return true
	}
	return *settings.ThreadSearch.Enabled
}

func isThreadSearchToolAllowedForRun(toolName string, enabled bool) bool {
	if !tools.IsThreadSearchToolName(strings.TrimSpace(toolName)) {
		return true
	}
	return enabled
}

func filterToolsByThreadSearchEnabled(toolDefs []types.ToolDefinition, enabled bool) []types.ToolDefinition {
	out := make([]types.ToolDefinition, 0, len(toolDefs))
	for _, def := range toolDefs {
		if !isThreadSearchToolAllowedForRun(def.Function.Name, enabled) {
			continue
		}
		out = append(out, def)
	}
	return out
}

func embedThreadSearchAvailabilityGuidance(systemPrompt string, enabled bool) string {
	if enabled {
		return systemPrompt
	}
	base := strings.TrimSpace(systemPrompt)
	lines := []string{
		"Search Threads tools are disabled for this run.",
		"Do not call search_threads, search_docs, get_doc, get_artifact, list_messages, or rlm_exec. Use only available context and other enabled tools.",
		"If exact prior-conversation evidence is required, ask the user to enable Search Threads.",
	}
	if base == "" {
		return strings.Join(lines, "\n")
	}
	return strings.Join(append([]string{base, ""}, lines...), "\n")
}
