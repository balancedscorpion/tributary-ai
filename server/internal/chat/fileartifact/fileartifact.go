package fileartifact

import (
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const SourceExternalTool = "external_tool"

var ManagedDownloadPrefixes = []string{
	"atlassian/downloads/",
	"sharepoint/downloads/",
	"slack/downloads/",
	"github/downloads/",
	"browser/downloads/",
	"connectors/downloads/",
	"external/downloads/",
	"arxiv/papers/",
}

func IsManagedDownloadRelPath(relSlash string) bool {
	relSlash = strings.TrimPrefix(strings.TrimSpace(filepath.ToSlash(relSlash)), "./")
	for _, prefix := range ManagedDownloadPrefixes {
		if strings.HasPrefix(relSlash, prefix) {
			return true
		}
	}
	return false
}

func IsThreadFileArtifact(threadID string, artifact store.Artifact) bool {
	if strings.TrimSpace(threadID) == "" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(artifact.ThreadID), strings.TrimSpace(threadID)) {
		return false
	}
	return IsFileAttachmentArtifact(artifact)
}

func IsFileAttachmentArtifact(artifact store.Artifact) bool {
	if !strings.EqualFold(strings.TrimSpace(artifact.Type), "file") {
		return false
	}
	if artifact.ToolName != nil && strings.EqualFold(strings.TrimSpace(*artifact.ToolName), types.ThreadFileAttachmentToolName) {
		return true
	}
	if external, ok := boolMetadataValue(artifact.Metadata, "externalToolFile", "external_tool_file"); ok && external {
		return true
	}
	source := strings.TrimSpace(stringMetadataValue(artifact.Metadata, "source"))
	return strings.EqualFold(source, types.ChatUploadAttachmentSource) ||
		strings.EqualFold(source, SourceExternalTool)
}

func boolMetadataValue(m map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		if value, ok := raw.(bool); ok {
			return value, true
		}
	}
	return false, false
}

func stringMetadataValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}
