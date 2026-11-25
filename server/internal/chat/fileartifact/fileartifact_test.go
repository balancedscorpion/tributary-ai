package fileartifact

import (
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestIsThreadFileArtifactAcceptsUnifiedSources(t *testing.T) {
	threadID := "thread-1"
	toolName := types.ThreadFileAttachmentToolName

	tests := []struct {
		name     string
		artifact store.Artifact
		want     bool
	}{
		{
			name: "chat upload source",
			artifact: store.Artifact{
				ThreadID: threadID,
				Type:     "file",
				Metadata: map[string]any{"source": types.ChatUploadAttachmentSource},
			},
			want: true,
		},
		{
			name: "thread file tool",
			artifact: store.Artifact{
				ThreadID: threadID,
				Type:     "file",
				ToolName: &toolName,
			},
			want: true,
		},
		{
			name: "external source",
			artifact: store.Artifact{
				ThreadID: threadID,
				Type:     "file",
				Metadata: map[string]any{"source": SourceExternalTool},
			},
			want: true,
		},
		{
			name: "external flag",
			artifact: store.Artifact{
				ThreadID: threadID,
				Type:     "file",
				Metadata: map[string]any{"externalToolFile": true},
			},
			want: true,
		},
		{
			name: "wrong type",
			artifact: store.Artifact{
				ThreadID: threadID,
				Type:     "data",
				Metadata: map[string]any{"source": SourceExternalTool},
			},
			want: false,
		},
		{
			name: "wrong thread",
			artifact: store.Artifact{
				ThreadID: "thread-2",
				Type:     "file",
				Metadata: map[string]any{"source": SourceExternalTool},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsThreadFileArtifact(threadID, tt.artifact); got != tt.want {
				t.Fatalf("IsThreadFileArtifact=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsManagedDownloadRelPath(t *testing.T) {
	for _, rel := range []string{
		"atlassian/downloads/jira/ABC-1/file.xlsx",
		"slack/downloads/F123/report.csv",
		"github/downloads/release/archive.zip",
		"browser/downloads/export.pdf",
		"sharepoint/downloads/contoso.sharepoint.com/abc/report.docx",
		"connectors/downloads/tool/file.txt",
		"external/downloads/source/file.txt",
	} {
		if !IsManagedDownloadRelPath(rel) {
			t.Fatalf("IsManagedDownloadRelPath(%q)=false, want true", rel)
		}
	}
	for _, rel := range []string{"thread_uploads/thread/file.txt", "../slack/downloads/file.txt", "secrets/file.txt"} {
		if IsManagedDownloadRelPath(rel) {
			t.Fatalf("IsManagedDownloadRelPath(%q)=true, want false", rel)
		}
	}
}
