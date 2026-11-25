package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	AtlassianSharedToolAtlassianStatus          = "atlassian_status"
	AtlassianSharedToolAtlassianCheckConnection = "atlassian_check_connection"
)

var atlassianJiraToolNames = []string{
	"jira_get_issue",
	"jira_get_issue_history",
	"jira_get_issue_comments",
	"jira_get_issue_attachments",
	"jira_read_attachment",
	"jira_get_issue_link_types",
	"jira_find_users",
	"jira_search_issues",
	"jira_find",
	"jira_rest_get",
	"jira_add_comment",
	"jira_reply_to_comment",
	"jira_add_related_tickets",
	"jira_upload_attachment",
	"jira_update_issue",
	"jira_create_issue",
}

const (
	maxJiraAttachmentUploadBytes int64 = 16 << 20
	jiraAttachmentPreviewBytes         = 4 << 10
)

const (
	jiraOperationAddComment        = "jira_add_comment_execute"
	jiraOperationAddRelatedTickets = "jira_add_related_tickets_execute"
	jiraOperationUploadAttachment  = "jira_upload_attachment_execute"
	jiraOperationUpdateIssue       = "jira_update_issue_execute"
	jiraOperationCreateIssue       = "jira_create_issue_execute"
)

var atlassianConfluenceToolNames = []string{
	"confluence_search",
	"confluence_open_page",
	"confluence_open_space",
	"confluence_rest_get",
}

var atlassianSharedToolNames = []string{
	AtlassianSharedToolAtlassianStatus,
	AtlassianSharedToolAtlassianCheckConnection,
}

func AtlassianJiraToolNames() []string {
	return append([]string(nil), atlassianJiraToolNames...)
}

func AtlassianConfluenceToolNames() []string {
	return append([]string(nil), atlassianConfluenceToolNames...)
}

func AtlassianSharedToolNames() []string {
	return append([]string(nil), atlassianSharedToolNames...)
}

func AtlassianProductForToolName(toolName string) string {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return ""
	}
	for _, candidate := range atlassianJiraToolNames {
		if name == candidate {
			return "jira"
		}
	}
	for _, candidate := range atlassianConfluenceToolNames {
		if name == candidate {
			return "confluence"
		}
	}
	for _, candidate := range atlassianSharedToolNames {
		if name == candidate {
			return "atlassian"
		}
	}
	return ""
}

func newAtlassianStatusTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, AtlassianSharedToolAtlassianStatus, "Atlassian Browser Status", "Return the shared browser state for Jira and Confluence automation.", map[string]any{})
}

func newAtlassianCheckConnectionTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, AtlassianSharedToolAtlassianCheckConnection, "Check Atlassian Connection", "Read-only check Jira and Confluence connection state from the shared persistent Playwright profile.", map[string]any{})
}

func newJiraGetIssueTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_get_issue", "Get Jira Issue", "Read a Jira issue through Atlassian REST using the Playwright browser session.", objectSchema(map[string]any{
		"issueKey": stringSchema("Jira issue key, for example TEAM-123."),
		"fields":   stringSchema("Optional comma-separated Jira fields."),
		"expand":   stringSchema("Optional Jira expand value, for example changelog."),
	}, []string{"issueKey"}))
}

func newJiraGetIssueHistoryTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_get_issue_history", "Get Jira Issue History", "Read Jira changelog and sprint history through browser-authenticated Atlassian REST.", objectSchema(map[string]any{
		"issueKey": stringSchema("Jira issue key, for example TEAM-123."),
	}, []string{"issueKey"}))
}

func newJiraGetIssueCommentsTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_get_issue_comments", "Get Jira Issue Comments", "Read Jira comments, comment IDs, authors, and stable comment links before replying.", objectSchema(map[string]any{
		"issueKey":   stringSchema("Jira issue key, for example TEAM-123."),
		"maxResults": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
		"orderBy": map[string]any{
			"type":        "string",
			"enum":        []string{"created", "-created", "+created"},
			"description": "Sort comments by creation time. Use -created for newest first.",
		},
	}, []string{"issueKey"}))
}

func newJiraGetIssueAttachmentsTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_get_issue_attachments", "Get Jira Issue Attachments", "Read Jira attachment metadata for an issue before selecting files to inspect or reference.", objectSchema(map[string]any{
		"issueKey": stringSchema("Jira issue key, for example TEAM-123."),
	}, []string{"issueKey"}))
}

func newJiraReadAttachmentTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_read_attachment", "Read Jira Attachment", "Read a Jira issue attachment by attachment ID. Call jira_get_issue_attachments first to choose the correct attachment. Text-like files return content; binary files return metadata unless includeBase64 is true. Successful downloads are also saved under an app-managed local_path that can be reused by Jira or Slack upload tools.", objectSchema(map[string]any{
		"issueKey":     stringSchema("Jira issue key containing the attachment, for example TEAM-123."),
		"attachmentId": stringSchema("Jira attachment ID from jira_get_issue_attachments or jira_get_issue."),
		"maxBytes": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     1048576,
			"description": "Maximum attachment bytes to return (default 65536, max 1048576).",
		},
		"includeBase64": map[string]any{
			"type":        "boolean",
			"description": "For binary files, include bounded base64 content. Default false.",
		},
		"maxDownloadBytes": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     134217728,
			"description": "Maximum bytes to save into the managed local download cache. Defaults to 134217728.",
		},
	}, []string{"issueKey", "attachmentId"}))
}

func newJiraGetIssueLinkTypesTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_get_issue_link_types", "Get Jira Issue Link Types", "Read available Jira issue link types. Use this if the default Relates link type is unavailable or a specific relationship is needed.", objectSchema(map[string]any{}, nil))
}

func newJiraFindUsersTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_find_users", "Find Jira Users", "Find Jira users by display name or email so Jira writes can tag people using native accountId-backed mentions. Use before mentioning someone in comments, replies, issue descriptions, or issue updates.", objectSchema(map[string]any{
		"query": stringSchema("Name, email, or handle text to find."),
		"maxResults": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     50,
			"description": "Maximum users to return (default 25, max 50).",
		},
	}, []string{"query"}))
}

func newJiraSearchIssuesTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_search_issues", "Search Jira Issues", "Search Jira issues with JQL through browser-authenticated Atlassian REST.", objectSchema(map[string]any{
		"jql": stringSchema("JQL query. When allowed projects are configured, include an allowed project clause."),
	}, []string{"jql"}))
}

func newJiraFindTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_find", "Find Jira Objects", "Find Jira issues, projects, or boards by human-readable names or text.", objectSchema(map[string]any{
		"type": map[string]any{
			"type":        "string",
			"enum":        []string{"issue", "project", "board", "all"},
			"description": "Object family to find.",
		},
		"query":          stringSchema("Human-readable name or text to find."),
		"projectOrBoard": stringSchema("Optional project key or board/project name to scope issue searches."),
		"orderBy": map[string]any{
			"type": "string",
			"enum": []string{"created", "updated", "key"},
		},
		"direction": map[string]any{
			"type": "string",
			"enum": []string{"ASC", "DESC"},
		},
	}, nil))
}

func newJiraRestGetTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "jira_rest_get", "Get Jira REST Path", "Read an allowed Jira REST path through the browser-authenticated session.", objectSchema(map[string]any{
		"path": stringSchema("Allowed Jira REST path under /rest/api/3 or /rest/agile/1.0."),
	}, []string{"path"}))
}

func newConfluenceSearchTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "confluence_search", "Search Confluence", "Search Confluence pages through browser-authenticated REST.", objectSchema(map[string]any{
		"query":    stringSchema("Search text."),
		"spaceKey": stringSchema("Optional Confluence space key."),
	}, []string{"query"}))
}

func newConfluenceOpenPageTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "confluence_open_page", "Open Confluence Page", "Read a Confluence page by same-site URL, page id, or exact title through browser-authenticated REST.", objectSchema(map[string]any{
		"url":        stringSchema("Optional absolute Confluence URL on the configured tenant."),
		"pageId":     stringSchema("Optional Confluence page id."),
		"spaceKey":   stringSchema("Optional space key for title lookup."),
		"title":      stringSchema("Optional exact page title."),
		"bodyFormat": map[string]any{"type": "string", "enum": []string{"storage", "atlas_doc_format"}},
	}, nil))
}

func newConfluenceOpenSpaceTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "confluence_open_space", "Open Confluence Space", "Read a Confluence space and homepage metadata through browser-authenticated REST.", objectSchema(map[string]any{
		"spaceKey": stringSchema("Confluence space key, for example TEAM."),
	}, []string{"spaceKey"}))
}

func newConfluenceRestGetTool(manager *atlassian.Manager) Tool {
	return atlassianBridgeTool(manager, "confluence_rest_get", "Get Confluence REST Path", "Read an allowed Confluence REST path through the browser-authenticated session.", objectSchema(map[string]any{
		"path": stringSchema("Allowed Confluence REST path under /api/v2 or /rest/api."),
	}, []string{"path"}))
}

func newJiraAddCommentTool(manager *atlassian.Manager) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "jira_add_comment",
				Description: "Add a new top-level comment to a Jira issue after persistent permission approval. The comment is interpreted as Markdown and posted as Jira Atlassian Document Format (ADF), so fenced code blocks, tables, lists, headings, links, and inline code render natively. To tag people, first call jira_find_users and pass a mentions array with accountId/displayName; plain @name text alone will not create a Jira mention. Use mentionText when the visible text differs from @Display Name. Use jira_reply_to_comment instead when the user intends to reply to a specific existing comment.",
				Parameters: closeTopLevelObjectSchema(objectSchema(map[string]any{
					"issueKey": stringSchema("Jira issue key, for example TEAM-123."),
					"comment":  stringSchema("Markdown comment body to add. It will be converted to Jira ADF before posting."),
					"mentions": jiraMentionsSchema(),
					"dry_run":  boolSchema("Validate and preview the Jira comment without posting it or requesting permission."),
				}, []string{"issueKey", "comment"})),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("Atlassian integration is not configured"), nil
			}
			var in struct {
				IssueKey string           `json:"issueKey"`
				Comment  string           `json:"comment"`
				Mentions []jiraMentionRef `json:"mentions"`
				DryRun   bool             `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			issueKey := strings.TrimSpace(strings.ToUpper(in.IssueKey))
			comment := strings.TrimSpace(in.Comment)
			if issueKey == "" || comment == "" {
				return JSONError("issueKey and comment are required"), nil
			}
			if err := validateJiraMentionUsage([]jiraMentionTextPart{{Label: "comment", Text: comment}}, in.Mentions); err != nil {
				return JSONError(err.Error()), nil
			}
			operationArgs := map[string]any{
				"issueKey": issueKey,
				"comment":  comment,
				"mentions": jiraMentionArgs(in.Mentions),
			}
			progressFields := map[string]any{
				"provider": "atlassian",
				"product":  "jira",
				"target":   issueKey,
			}
			if in.DryRun {
				return atlassianDryRunOutput("jira_add_comment", jiraOperationAddComment, operationArgs, map[string]any{
					"target":   progressFields,
					"comment":  comment,
					"mentions": jiraMentionPreview(in.Mentions),
				}), nil
			}
			out, _ := runApprovedAtlassianWrite(ctx, exec, manager, "jira_add_comment", jiraOperationAddComment, "Posting approved Jira comment", "Posted Jira comment", operationArgs, progressFields)
			return out, nil
		},
	}
}

func newJiraReplyToCommentTool(manager *atlassian.Manager) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "jira_reply_to_comment",
				Description: "Add a reply-style Jira comment tied to an existing comment ID after persistent permission approval. Use this when the user asks to reply to a comment. The body is interpreted as Markdown and posted as Jira Atlassian Document Format (ADF); the posted comment includes a native link to the parent comment and reply metadata. To tag people, first call jira_find_users and pass a mentions array with accountId/displayName; plain @name text alone will not create a Jira mention. Use mentionText when the visible text differs from @Display Name.",
				Parameters: closeTopLevelObjectSchema(objectSchema(map[string]any{
					"issueKey":  stringSchema("Jira issue key containing the comment, for example TEAM-123."),
					"commentId": stringSchema("Existing Jira comment ID to reply to. Call jira_get_issue_comments first if the ID is not known."),
					"comment":   stringSchema("Markdown reply body. It will be converted to Jira ADF before posting."),
					"mentions":  jiraMentionsSchema(),
					"dry_run":   boolSchema("Validate and preview the Jira reply without posting it or requesting permission."),
				}, []string{"issueKey", "commentId", "comment"})),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("Atlassian integration is not configured"), nil
			}
			var in struct {
				IssueKey  string           `json:"issueKey"`
				CommentID string           `json:"commentId"`
				Comment   string           `json:"comment"`
				Mentions  []jiraMentionRef `json:"mentions"`
				DryRun    bool             `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			issueKey := strings.TrimSpace(strings.ToUpper(in.IssueKey))
			commentID := strings.TrimSpace(in.CommentID)
			comment := strings.TrimSpace(in.Comment)
			if issueKey == "" || commentID == "" || comment == "" {
				return JSONError("issueKey, commentId, and comment are required"), nil
			}
			if err := validateJiraMentionUsage([]jiraMentionTextPart{{Label: "reply", Text: comment}}, in.Mentions); err != nil {
				return JSONError(err.Error()), nil
			}
			operationArgs := map[string]any{
				"issueKey":          issueKey,
				"comment":           comment,
				"replyToCommentId":  commentID,
				"replyContextLabel": "Replying to Jira comment",
				"mentions":          jiraMentionArgs(in.Mentions),
			}
			progressFields := map[string]any{
				"provider":  "atlassian",
				"product":   "jira",
				"target":    issueKey,
				"commentId": commentID,
			}
			if in.DryRun {
				return atlassianDryRunOutput("jira_reply_to_comment", jiraOperationAddComment, operationArgs, map[string]any{
					"target":     progressFields,
					"comment":    comment,
					"comment_id": commentID,
					"mentions":   jiraMentionPreview(in.Mentions),
				}), nil
			}
			out, _ := runApprovedAtlassianWrite(ctx, exec, manager, "jira_reply_to_comment", jiraOperationAddComment, "Posting approved Jira reply", "Posted Jira reply", operationArgs, progressFields)
			return out, nil
		},
	}
}

func newJiraAddRelatedTicketsTool(manager *atlassian.Manager) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "jira_add_related_tickets",
				Description: "Add one or more Jira issues to the target issue's related/linked tickets section after persistent permission approval. Prefer this when you identify relevant related tickets during Jira work, instead of only listing them in prose. Defaults to the Jira link type Relates.",
				Parameters: closeTopLevelObjectSchema(objectSchema(map[string]any{
					"issueKey": stringSchema("Target Jira issue key that should show the related tickets, for example TEAM-123."),
					"relatedIssueKeys": map[string]any{
						"type":        "array",
						"description": "Jira issue keys to attach as related tickets.",
						"items":       stringSchema("Related Jira issue key, for example TEAM-456."),
					},
					"relationship": stringSchema("Optional Jira issue link type name. Defaults to Relates. Call jira_get_issue_link_types if unsure."),
					"comment":      stringSchema("Optional Markdown comment to include with each issue link."),
					"dry_run":      boolSchema("Validate and preview the Jira issue links without creating them or requesting permission."),
				}, []string{"issueKey", "relatedIssueKeys"})),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("Atlassian integration is not configured"), nil
			}
			var in struct {
				IssueKey         string   `json:"issueKey"`
				RelatedIssueKeys []string `json:"relatedIssueKeys"`
				Relationship     string   `json:"relationship"`
				Comment          string   `json:"comment"`
				DryRun           bool     `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			issueKey := strings.TrimSpace(strings.ToUpper(in.IssueKey))
			related := normalizeJiraIssueKeys(in.RelatedIssueKeys, issueKey)
			if issueKey == "" || len(related) == 0 {
				return JSONError("issueKey and at least one relatedIssueKeys entry are required"), nil
			}
			relationship := strings.TrimSpace(in.Relationship)
			if relationship == "" {
				relationship = "Relates"
			}
			comment := strings.TrimSpace(in.Comment)
			operationArgs := map[string]any{
				"issueKey":         issueKey,
				"relatedIssueKeys": related,
				"relationship":     relationship,
				"comment":          comment,
			}
			progressFields := map[string]any{
				"provider": "atlassian",
				"product":  "jira",
				"target":   issueKey,
			}
			if in.DryRun {
				return atlassianDryRunOutput("jira_add_related_tickets", jiraOperationAddRelatedTickets, operationArgs, map[string]any{
					"target":             progressFields,
					"related_issue_keys": related,
					"relationship":       relationship,
					"comment":            comment,
				}), nil
			}
			out, _ := runApprovedAtlassianWrite(ctx, exec, manager, "jira_add_related_tickets", jiraOperationAddRelatedTickets, "Linking approved Jira issues", "Linked Jira issues", operationArgs, progressFields)
			return out, nil
		},
	}
}

func newJiraUploadAttachmentTool(manager *atlassian.Manager) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "jira_upload_attachment",
				Description: "Upload a file attachment to a Jira issue after persistent permission approval. Use artifact_id for a file uploaded in the current chat, path for a project-relative file, local_path for an app-managed downloaded file, or contentText/contentBase64 with fileName for generated content. Exactly one source should be provided: artifact_id, path, local_path, or generated content.",
				Parameters: closeTopLevelObjectSchema(objectSchema(map[string]any{
					"issueKey":      stringSchema("Jira issue key to upload the file to, for example TEAM-123."),
					"artifact_id":   stringSchema("Optional thread-scoped uploaded file artifact id to upload."),
					"path":          stringSchema("Optional project-relative file path to upload from the attached workspace."),
					"local_path":    stringSchema("Optional app-managed local download path returned by a previous tool, for example jira_read_attachment.local_path."),
					"fileName":      stringSchema("File name to use in Jira. Required for contentText/contentBase64 sources; optional for artifact_id/path/local_path."),
					"mimeType":      stringSchema("Optional MIME type. Inferred from the file name/content when omitted."),
					"contentText":   stringSchema("Optional UTF-8 text content for a new file upload."),
					"contentBase64": stringSchema("Optional base64 file content for a new binary or text file upload."),
					"dry_run":       boolSchema("Validate and preview the Jira attachment upload without uploading it or requesting permission."),
				}, []string{"issueKey"})),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("Atlassian integration is not configured"), nil
			}
			var in struct {
				IssueKey      string `json:"issueKey"`
				ArtifactID    string `json:"artifact_id"`
				Path          string `json:"path"`
				LocalPath     string `json:"local_path"`
				FileName      string `json:"fileName"`
				MimeType      string `json:"mimeType"`
				ContentText   string `json:"contentText"`
				ContentBase64 string `json:"contentBase64"`
				DryRun        bool   `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			issueKey := strings.TrimSpace(strings.ToUpper(in.IssueKey))
			if issueKey == "" {
				return JSONError("issueKey is required"), nil
			}
			fileName, mimeType, raw, source, errOut, ok := resolveJiraAttachmentUploadSource(ctx, exec, in.ArtifactID, in.Path, in.LocalPath, in.FileName, in.MimeType, in.ContentText, in.ContentBase64)
			if !ok {
				return errOut, nil
			}
			if int64(len(raw)) > maxJiraAttachmentUploadBytes {
				return JSONError(fmt.Sprintf("attachment exceeds Jira upload cap of %d bytes", maxJiraAttachmentUploadBytes)), nil
			}
			sourceRef := strings.TrimSpace(in.ArtifactID)
			if sourceRef == "" {
				sourceRef = strings.TrimSpace(in.Path)
			}
			if sourceRef == "" {
				sourceRef = strings.TrimSpace(in.LocalPath)
			}
			if sourceRef == "" {
				sourceRef = "inline"
			}
			sum := sha256.Sum256(raw)
			shaHex := hex.EncodeToString(sum[:])
			preview := ""
			if uploadBytesAreText(fileName, mimeType, raw) {
				preview = string(raw)
				if len(preview) > jiraAttachmentPreviewBytes {
					preview = preview[:jiraAttachmentPreviewBytes]
				}
			}
			operationArgs := map[string]any{
				"issueKey":      issueKey,
				"fileName":      fileName,
				"mimeType":      mimeType,
				"contentBase64": base64.StdEncoding.EncodeToString(raw),
				"sha256":        shaHex,
				"source":        source,
				"sourceRef":     sourceRef,
			}
			progressFields := map[string]any{
				"provider": "atlassian",
				"product":  "jira",
				"target":   issueKey,
			}
			if in.DryRun {
				return atlassianDryRunOutput("jira_upload_attachment", jiraOperationUploadAttachment, operationArgs, map[string]any{
					"target":     progressFields,
					"file_name":  fileName,
					"mime_type":  mimeType,
					"sha256":     shaHex,
					"source":     source,
					"source_ref": sourceRef,
					"preview":    preview,
				}), nil
			}
			out, _ := runApprovedAtlassianWrite(ctx, exec, manager, "jira_upload_attachment", jiraOperationUploadAttachment, "Uploading approved Jira attachment", "Uploaded Jira attachment", operationArgs, progressFields)
			return out, nil
		},
	}
}

func newJiraUpdateIssueTool(manager *atlassian.Manager) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "jira_update_issue",
				Description: "Edit an existing Jira issue after persistent permission approval. Use this for requested issue updates such as summary, description, assignee, labels, components, priority, issue type, or custom fields. Description Markdown is converted to Jira ADF. To tag people in the description, first call jira_find_users and pass a mentions array with accountId/displayName; plain @name text alone will not create a Jira mention.",
				Parameters: closeTopLevelObjectSchema(objectSchema(map[string]any{
					"issueKey":          stringSchema("Jira issue key to update, for example TEAM-123."),
					"summary":           stringSchema("Optional replacement issue summary."),
					"description":       stringSchema("Optional replacement Markdown issue description. It will be converted to Jira ADF before posting."),
					"clearDescription":  map[string]any{"type": "boolean", "description": "Set true to clear the issue description."},
					"issueType":         stringSchema("Optional replacement issue type name."),
					"assigneeAccountId": stringSchema("Optional Jira account ID to assign the issue to. Use jira_find_users first."),
					"priorityName":      stringSchema("Optional replacement Jira priority name."),
					"labels": map[string]any{
						"type":        "array",
						"description": "Optional full replacement label list.",
						"items":       stringSchema("Jira label."),
					},
					"components": map[string]any{
						"type":        "array",
						"description": "Optional full replacement Jira component name list. Components are project-scoped; when the target board or project is clear, include relevant component names instead of leaving them implicit.",
						"items":       stringSchema("Jira component name."),
					},
					"fields": map[string]any{
						"type":                 "object",
						"description":          "Optional advanced Jira fields object for project-specific/custom fields. Values are passed to Jira under fields; string description values are converted to ADF.",
						"additionalProperties": true,
					},
					"mentions": jiraMentionsSchema(),
					"dry_run":  boolSchema("Validate and preview the Jira issue update without applying it or requesting permission."),
				}, []string{"issueKey"})),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("Atlassian integration is not configured"), nil
			}
			var in struct {
				IssueKey          string           `json:"issueKey"`
				Summary           string           `json:"summary"`
				Description       string           `json:"description"`
				ClearDescription  bool             `json:"clearDescription"`
				IssueType         string           `json:"issueType"`
				AssigneeAccountID string           `json:"assigneeAccountId"`
				PriorityName      string           `json:"priorityName"`
				Labels            []string         `json:"labels"`
				Components        []string         `json:"components"`
				Fields            map[string]any   `json:"fields"`
				Mentions          []jiraMentionRef `json:"mentions"`
				DryRun            bool             `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			issueKey := strings.TrimSpace(strings.ToUpper(in.IssueKey))
			if issueKey == "" {
				return JSONError("issueKey is required"), nil
			}
			description := strings.TrimSpace(in.Description)
			if description != "" {
				if err := validateJiraMentionUsage([]jiraMentionTextPart{{Label: "description", Text: description}}, in.Mentions); err != nil {
					return JSONError(err.Error()), nil
				}
			}
			if rawDescription, ok := in.Fields["description"].(string); ok && strings.TrimSpace(rawDescription) != "" {
				if err := validateJiraMentionUsage([]jiraMentionTextPart{{Label: "fields.description", Text: rawDescription}}, in.Mentions); err != nil {
					return JSONError(err.Error()), nil
				}
			}
			fieldNames := jiraUpdateFieldNames(in.Summary, description, in.ClearDescription, in.IssueType, in.AssigneeAccountID, in.PriorityName, in.Labels != nil, in.Components != nil, in.Fields)
			if len(fieldNames) == 0 {
				return JSONError("provide at least one issue field to update"), nil
			}
			labels := normalizeNonEmptyStrings(in.Labels)
			components := normalizeNonEmptyStrings(in.Components)
			operationArgs := map[string]any{
				"issueKey":          issueKey,
				"summary":           strings.TrimSpace(in.Summary),
				"description":       description,
				"clearDescription":  in.ClearDescription,
				"issueType":         strings.TrimSpace(in.IssueType),
				"assigneeAccountId": strings.TrimSpace(in.AssigneeAccountID),
				"priorityName":      strings.TrimSpace(in.PriorityName),
				"labels":            labels,
				"components":        components,
				"fields":            in.Fields,
				"mentions":          jiraMentionArgs(in.Mentions),
			}
			progressFields := map[string]any{
				"provider": "atlassian",
				"product":  "jira",
				"target":   issueKey,
			}
			if in.DryRun {
				return atlassianDryRunOutput("jira_update_issue", jiraOperationUpdateIssue, operationArgs, map[string]any{
					"target":      progressFields,
					"field_names": fieldNames,
					"changes":     strings.Join(fieldNames, ", "),
					"mentions":    jiraMentionPreview(in.Mentions),
				}), nil
			}
			out, _ := runApprovedAtlassianWrite(ctx, exec, manager, "jira_update_issue", jiraOperationUpdateIssue, "Applying approved Jira issue update", "Updated Jira issue", operationArgs, progressFields)
			return out, nil
		},
	}
}

func newJiraCreateIssueTool(manager *atlassian.Manager) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "jira_create_issue",
				Description: "Create a Jira issue after persistent permission approval. The description is interpreted as Markdown and posted as Jira Atlassian Document Format (ADF), so fenced code blocks, tables, lists, headings, links, and inline code render natively. Include components when the user names them or when the target project/board makes the relevant component clear. To tag people in the description, first call jira_find_users and pass a mentions array with accountId/displayName; plain @name text alone will not create a Jira mention.",
				Parameters: closeTopLevelObjectSchema(objectSchema(map[string]any{
					"projectKey":  stringSchema("Jira project key, for example TEAM."),
					"summary":     stringSchema("Issue summary."),
					"description": stringSchema("Optional Markdown issue description. It will be converted to Jira ADF before posting."),
					"issueType":   stringSchema("Optional Jira issue type name. Defaults to Task."),
					"components": map[string]any{
						"type":        "array",
						"description": "Optional Jira component names to add at creation. Components are project-scoped; include them when the target project or board makes the component clear.",
						"items":       stringSchema("Jira component name."),
					},
					"mentions": jiraMentionsSchema(),
					"dry_run":  boolSchema("Validate and preview the Jira issue creation without creating it or requesting permission."),
				}, []string{"projectKey", "summary"})),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("Atlassian integration is not configured"), nil
			}
			var in struct {
				ProjectKey  string           `json:"projectKey"`
				Summary     string           `json:"summary"`
				Description string           `json:"description"`
				IssueType   string           `json:"issueType"`
				Components  []string         `json:"components"`
				Mentions    []jiraMentionRef `json:"mentions"`
				DryRun      bool             `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			projectKey := strings.TrimSpace(strings.ToUpper(in.ProjectKey))
			summary := strings.TrimSpace(in.Summary)
			if projectKey == "" || summary == "" {
				return JSONError("projectKey and summary are required"), nil
			}
			issueType := strings.TrimSpace(in.IssueType)
			if issueType == "" {
				issueType = "Task"
			}
			description := strings.TrimSpace(in.Description)
			if description != "" {
				if err := validateJiraMentionUsage([]jiraMentionTextPart{{Label: "description", Text: description}}, in.Mentions); err != nil {
					return JSONError(err.Error()), nil
				}
			}
			components := normalizeNonEmptyStrings(in.Components)
			operationArgs := map[string]any{
				"projectKey":  projectKey,
				"summary":     summary,
				"description": description,
				"issueType":   issueType,
				"components":  components,
				"mentions":    jiraMentionArgs(in.Mentions),
			}
			progressFields := map[string]any{
				"provider": "atlassian",
				"product":  "jira",
				"target":   projectKey,
			}
			if in.DryRun {
				return atlassianDryRunOutput("jira_create_issue", jiraOperationCreateIssue, operationArgs, map[string]any{
					"target":      progressFields,
					"summary":     summary,
					"issue_type":  issueType,
					"components":  components,
					"mentions":    jiraMentionPreview(in.Mentions),
					"description": description,
				}), nil
			}
			out, _ := runApprovedAtlassianWrite(ctx, exec, manager, "jira_create_issue", jiraOperationCreateIssue, "Creating approved Jira issue", "Created Jira issue", operationArgs, progressFields)
			return out, nil
		},
	}
}

func atlassianBridgeTool(manager *atlassian.Manager, name, title, description string, parameters map[string]any) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        name,
				Description: description,
				Parameters:  closeTopLevelObjectSchema(parameters),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("Atlassian integration is not configured"), nil
			}
			var input map[string]any
			if len(strings.TrimSpace(string(args))) == 0 {
				input = map[string]any{}
			} else if err := json.Unmarshal(args, &input); err != nil {
				return JSONError(err.Error()), nil
			}
			emitAtlassianProgress(exec, name, "browser_starting", "Starting Atlassian browser session", 8, map[string]any{
				"provider": "atlassian",
				"product":  AtlassianProductForToolName(name),
			})
			emitAtlassianProgress(exec, name, atlassianProgressPhase(name), atlassianProgressLabel(name, input), 42, map[string]any{
				"provider": "atlassian",
				"product":  AtlassianProductForToolName(name),
			})
			result, err := manager.CallTool(ctx, name, input)
			if err != nil {
				emitAtlassianProgress(exec, name, "validation_failed", err.Error(), -1, map[string]any{
					"provider": "atlassian",
					"product":  AtlassianProductForToolName(name),
				})
				return JSONError(err.Error()), nil
			}
			completeMeta := map[string]any{
				"provider": "atlassian",
				"product":  AtlassianProductForToolName(name),
			}
			if url, ok := result["url"].(string); ok && strings.TrimSpace(url) != "" {
				completeMeta["url"] = strings.TrimSpace(url)
			}
			emitAtlassianProgress(exec, name, "browser_ready", atlassianProgressCompleteLabel(name), 100, completeMeta)
			var artifact *ArtifactPayload
			if name == "jira_read_attachment" {
				rawMeta, _ := result["result_metadata"].(map[string]any)
				artifact = jiraReadAttachmentImageArtifact(result, input, atlassianBridgeResultMetadata(name, rawMeta))
			}
			output := atlassianBridgeOutput(name, result)
			output.Artifact = artifact
			return output, nil
		},
	}
}

func atlassianBridgeOutput(toolName string, result map[string]any) Output {
	out := cloneStringAnyMap(result)
	if out == nil {
		out = map[string]any{}
	}
	rawMeta, _ := out["result_metadata"].(map[string]any)
	delete(out, "result_metadata")
	return JSONOKWithMetadata(out, atlassianBridgeResultMetadata(toolName, rawMeta))
}

func atlassianBridgeResultMetadata(toolName string, bridgeMeta map[string]any) map[string]any {
	meta := cloneStringAnyMap(bridgeMeta)
	toolName = strings.TrimSpace(toolName)
	meta["tool_name"] = toolName
	meta["integration"] = "atlassian"
	product := AtlassianProductForToolName(toolName)
	if product != "" {
		meta["product"] = product
	}

	if toolMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_name"] = toolMeta.Name
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		meta["connector_id"] = toolMeta.ConnectorID
		meta["origin"] = string(toolMeta.Origin)
		meta["category"] = string(toolMeta.Category)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := atlassianToolPresentationMetadata(toolMeta.Presentation)
		if family := strings.TrimSpace(fmt.Sprint(presentation["family"])); family != "" && family != "<nil>" {
			meta["family"] = family
		}
		if resultKind := strings.TrimSpace(fmt.Sprint(presentation["result_kind"])); resultKind != "" && resultKind != "<nil>" {
			meta["kind"] = resultKind
			meta["result_kind"] = resultKind
		}
		if rendererStrategy := strings.TrimSpace(fmt.Sprint(presentation["renderer_strategy"])); rendererStrategy != "" && rendererStrategy != "<nil>" {
			meta["renderer_strategy"] = rendererStrategy
		}
		if rendererKey := strings.TrimSpace(fmt.Sprint(presentation["renderer_key"])); rendererKey != "" && rendererKey != "<nil>" {
			meta["renderer_key"] = rendererKey
		}
		if nativeViewer := strings.TrimSpace(fmt.Sprint(presentation["native_viewer"])); nativeViewer != "" && nativeViewer != "<nil>" {
			meta["native_viewer"] = nativeViewer
		}
		if fileCapability := strings.TrimSpace(fmt.Sprint(presentation["file_capability"])); fileCapability != "" && fileCapability != "<nil>" {
			meta["file_capability"] = fileCapability
		}
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
		}
	}

	if strings.TrimSpace(fmt.Sprint(meta["integration"])) == "" || fmt.Sprint(meta["integration"]) == "<nil>" {
		meta["integration"] = "atlassian"
	}
	if strings.TrimSpace(fmt.Sprint(meta["product"])) == "" || fmt.Sprint(meta["product"]) == "<nil>" {
		if product != "" {
			meta["product"] = product
		}
	}
	if strings.TrimSpace(fmt.Sprint(meta["widget"])) == "" || fmt.Sprint(meta["widget"]) == "<nil>" {
		meta["widget"] = atlassianDefaultWidgetForTool(toolName)
	}
	return meta
}

func atlassianToolPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putAtlassianPresentationString(out, "family", p.Family)
	putAtlassianPresentationString(out, "result_kind", p.ResultKind)
	putAtlassianPresentationString(out, "renderer_strategy", p.RendererStrategy)
	putAtlassianPresentationString(out, "renderer_key", p.RendererKey)
	putAtlassianPresentationString(out, "native_viewer", p.NativeViewer)
	putAtlassianPresentationString(out, "file_capability", p.FileCapability)
	putAtlassianPresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putAtlassianPresentationStrings(out, "file_kinds", p.FileKinds)
	putAtlassianPresentationStrings(out, "preview_modes", p.PreviewModes)
	putAtlassianPresentationStrings(out, "source_refs", p.SourceRefs)
	putAtlassianPresentationStrings(out, "native_links", p.NativeLinks)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	return out
}

func putAtlassianPresentationString(out map[string]any, key, value string) {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		out[key] = trimmed
	}
}

func putAtlassianPresentationStrings(out map[string]any, key string, values []string) {
	if len(values) > 0 {
		out[key] = append([]string(nil), values...)
	}
}

func atlassianDefaultWidgetForTool(toolName string) string {
	switch toolName {
	case AtlassianSharedToolAtlassianStatus:
		return "status"
	case AtlassianSharedToolAtlassianCheckConnection:
		return "connection"
	case "jira_get_issue":
		return "issue"
	case "jira_get_issue_history":
		return "issue_history"
	case "jira_get_issue_comments":
		return "comments"
	case "jira_get_issue_attachments":
		return "attachments"
	case "jira_read_attachment":
		return "attachment"
	case "jira_get_issue_link_types":
		return "issue_link_types"
	case "jira_find_users":
		return "user_list"
	case "jira_search_issues":
		return "issue_list"
	case "jira_find":
		return "find"
	case "jira_rest_get", "confluence_rest_get":
		return "rest"
	case "confluence_search":
		return "search"
	case "confluence_open_page":
		return "page"
	case "confluence_open_space":
		return "space"
	default:
		return "connector"
	}
}

func atlassianApprovalOutput(toolName string, approval any) Output {
	return JSONOKWithMetadata(map[string]any{"approval": approval}, atlassianWriteResultMetadata(toolName, "approval", nil))
}

func atlassianDryRunOutput(toolName, bridgeTool string, args map[string]any, preview map[string]any) Output {
	return JSONOKWithMetadata(map[string]any{
		"ok":            true,
		"dry_run":       true,
		"validated":     true,
		"tool_name":     toolName,
		"operation":     bridgeTool,
		"would_execute": bridgeTool,
		"args":          cloneStringAnyMap(args),
		"preview":       cloneStringAnyMap(preview),
		"message":       "Dry run only. Jira was not changed.",
	}, atlassianWriteResultMetadata(toolName, "dry_run", nil))
}

func runApprovedAtlassianWrite(ctx context.Context, exec ExecContext, manager *atlassian.Manager, toolName, bridgeTool, runningLabel, completedLabel string, args map[string]any, fields map[string]any) (Output, bool) {
	if !exec.PermissionApproved {
		progressFields := cloneStringAnyMap(fields)
		emitAtlassianProgress(exec, toolName, "permission_required", "Persistent approval is required before this Jira operation can run", 100, progressFields)
		return atlassianPersistentPermissionRequiredOutput(toolName, bridgeTool, progressFields), true
	}
	progressFields := cloneStringAnyMap(fields)
	emitAtlassianProgress(exec, toolName, "running", runningLabel, 25, progressFields)
	result, err := manager.RunApprovedOperation(ctx, bridgeTool, cloneStringAnyMap(args))
	if err != nil {
		return JSONError(err.Error()), true
	}
	completedFields := cloneStringAnyMap(progressFields)
	if issueKey := strings.TrimSpace(stringMapValue(result, "issueKey", "issue_key")); issueKey != "" {
		completedFields["target"] = issueKey
	}
	emitAtlassianProgress(exec, toolName, "completed", completedLabel, 100, completedFields)
	return atlassianWriteOutput(toolName, result), true
}

func atlassianPersistentPermissionRequiredOutput(toolName, operation string, target map[string]any) Output {
	return Output{
		OK:           false,
		ResultFormat: ResultFormatJSON,
		Result: map[string]any{
			"ok":                  false,
			"status":              "permission_required",
			"permission_required": true,
			"tool_name":           toolName,
			"operation":           operation,
			"target":              cloneStringAnyMap(target),
			"message":             "Persistent permission approval is required before this Jira operation can run.",
		},
		ResultMetadata: atlassianWriteResultMetadata(toolName, "permission_required", map[string]any{
			"widget": "permission_required",
		}),
	}
}

func atlassianWriteOutput(toolName string, result map[string]any) Output {
	out := cloneStringAnyMap(result)
	if out == nil {
		out = map[string]any{}
	}
	rawMeta, _ := out["result_metadata"].(map[string]any)
	delete(out, "result_metadata")
	return JSONOKWithMetadata(out, atlassianWriteResultMetadata(toolName, "write_result", rawMeta))
}

func atlassianWriteResultMetadata(toolName, widget string, rawMeta map[string]any) map[string]any {
	meta := cloneStringAnyMap(rawMeta)
	if meta == nil {
		meta = map[string]any{}
	}
	if atlassianMetadataValueMissing(meta, "widget") {
		if trimmed := strings.TrimSpace(widget); trimmed != "" {
			meta["widget"] = trimmed
		}
	}
	return atlassianBridgeResultMetadata(toolName, meta)
}

func atlassianMetadataValueMissing(meta map[string]any, key string) bool {
	if meta == nil {
		return true
	}
	value := strings.TrimSpace(fmt.Sprint(meta[key]))
	return value == "" || value == "<nil>"
}

func jiraReadAttachmentImageArtifact(result, toolArgs, resultMetadata map[string]any) *ArtifactPayload {
	if result == nil {
		return nil
	}

	attachment := mapAny(result["attachment"])
	fileName := strings.TrimSpace(firstNonEmptyString(
		stringMapValue(result, "fileName", "file_name"),
		stringMapValue(attachment, "fileName", "filename", "name"),
	))
	mimeType := firstSupportedModelImageMimeType(
		stringMapValue(result, "mimeType", "mime_type"),
		stringMapValue(result, "contentType", "content_type"),
		stringMapValue(attachment, "mimeType", "mime_type", "contentType", "content_type"),
		mime.TypeByExtension(strings.ToLower(path.Ext(fileName))),
	)
	if mimeType == "" {
		return nil
	}

	contentBase64 := strings.TrimSpace(stringMapValue(result, "contentBase64", "content_base64"))
	if contentBase64 == "" {
		return nil
	}
	if truncated, ok := boolMapValue(result, "truncated"); ok && truncated {
		delete(result, "contentBase64")
		delete(result, "content_base64")
		result["contentBase64Omitted"] = true
		result["note"] = "Image attachment exceeded native image input size; base64 content was omitted."
		return nil
	}
	if _, err := base64.StdEncoding.DecodeString(contentBase64); err != nil {
		delete(result, "contentBase64")
		delete(result, "content_base64")
		result["contentBase64Omitted"] = true
		result["note"] = "Image attachment base64 content was invalid and omitted."
		return nil
	}

	delete(result, "contentBase64")
	delete(result, "content_base64")
	result["native_image_input"] = true
	result["contentBase64Omitted"] = true
	result["note"] = "Image attachment was provided to the model as a native image input."

	if fileName == "" {
		fileName = "Jira image attachment"
	}
	metadata := map[string]any{
		"mimeType":           mimeType,
		"source":             "jira_attachment",
		"nativeImageInput":   true,
		"native_image_input": true,
		"tool_args":          jiraReadAttachmentArtifactToolArgs(toolArgs, result),
	}
	if issueKey := strings.TrimSpace(stringMapValue(result, "issueKey", "issue_key")); issueKey != "" {
		metadata["issueKey"] = issueKey
	}
	if attachmentID := strings.TrimSpace(stringMapValue(result, "attachmentId", "attachment_id")); attachmentID != "" {
		metadata["attachmentId"] = attachmentID
	}
	if sizeBytes, ok := jiraAttachmentInt64Value(result, "sizeBytes", "size_bytes", "bytesReturned", "bytes_returned"); ok && sizeBytes > 0 {
		metadata["sizeBytes"] = sizeBytes
	}
	artifactResultMetadata := atlassianBridgeResultMetadata("jira_read_attachment", resultMetadata)
	metadata["result_metadata"] = cloneStringAnyMap(artifactResultMetadata)
	if presentation, ok := artifactResultMetadata["tool_presentation"].(map[string]any); ok && len(presentation) > 0 {
		metadata["tool_presentation"] = cloneStringAnyMap(presentation)
	}

	return &ArtifactPayload{
		Type:     "image",
		Title:    fileName,
		Content:  "data:" + mimeType + ";base64," + contentBase64,
		Metadata: metadata,
	}
}

func jiraReadAttachmentArtifactToolArgs(toolArgs, result map[string]any) map[string]any {
	out := cloneStringAnyMap(toolArgs)
	if atlassianMetadataValueMissing(out, "issueKey") {
		if issueKey := strings.TrimSpace(stringMapValue(result, "issueKey", "issue_key")); issueKey != "" {
			out["issueKey"] = issueKey
		}
	}
	if atlassianMetadataValueMissing(out, "attachmentId") {
		if attachmentID := strings.TrimSpace(stringMapValue(result, "attachmentId", "attachment_id")); attachmentID != "" {
			out["attachmentId"] = attachmentID
		}
	}
	return out
}

func normalizeModelImageMimeType(raw string) string {
	mediaType := strings.ToLower(strings.TrimSpace(raw))
	if semi := strings.Index(mediaType, ";"); semi >= 0 {
		mediaType = strings.TrimSpace(mediaType[:semi])
	}
	switch mediaType {
	case "image/png", "image/webp", "image/gif":
		return mediaType
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	default:
		return ""
	}
}

func firstSupportedModelImageMimeType(values ...string) string {
	for _, value := range values {
		if mimeType := normalizeModelImageMimeType(value); mimeType != "" {
			return mimeType
		}
	}
	return ""
}

func mapAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func jiraAttachmentInt64Value(m map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		value, ok := m[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case int:
			return int64(v), true
		case int64:
			return v, true
		case float64:
			return int64(v), true
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func emitAtlassianProgress(exec ExecContext, toolName, phase, label string, percent int, fields map[string]any) {
	if exec.OnProgress == nil {
		return
	}
	progress := map[string]any{
		"tool_name": toolName,
		"provider":  "atlassian",
		"phase":     phase,
		"label":     label,
	}
	if percent >= 0 {
		progress["percent"] = percent
	}
	for key, value := range fields {
		progress[key] = value
	}
	exec.OnProgress(progress)
}

func atlassianProgressPhase(toolName string) string {
	switch toolName {
	case "jira_search_issues", "jira_find", "jira_find_users", "confluence_search":
		return "browser_searching"
	case "jira_get_issue", "jira_get_issue_history", "jira_get_issue_comments", "jira_get_issue_attachments", "jira_read_attachment", "jira_get_issue_link_types", "jira_rest_get", "confluence_open_page", "confluence_open_space", "confluence_rest_get":
		return "browser_reading"
	default:
		return "browser_navigating"
	}
}

func atlassianProgressLabel(toolName string, args map[string]any) string {
	textArg := func(key string) string {
		if value, ok := args[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	switch toolName {
	case "atlassian_status":
		return "Inspecting Atlassian browser state"
	case "atlassian_check_connection":
		return "Checking Jira and Confluence connection"
	case "jira_get_issue":
		if issueKey := textArg("issueKey"); issueKey != "" {
			return "Opening Jira issue " + strings.ToUpper(issueKey)
		}
		return "Opening Jira issue"
	case "jira_get_issue_history":
		if issueKey := textArg("issueKey"); issueKey != "" {
			return "Reading Jira history for " + strings.ToUpper(issueKey)
		}
		return "Reading Jira issue history"
	case "jira_get_issue_comments":
		if issueKey := textArg("issueKey"); issueKey != "" {
			return "Reading Jira comments for " + strings.ToUpper(issueKey)
		}
		return "Reading Jira comments"
	case "jira_get_issue_attachments":
		if issueKey := textArg("issueKey"); issueKey != "" {
			return "Reading Jira attachments for " + strings.ToUpper(issueKey)
		}
		return "Reading Jira attachments"
	case "jira_read_attachment":
		return "Reading Jira attachment"
	case "jira_get_issue_link_types":
		return "Reading Jira issue link types"
	case "jira_find_users":
		return "Finding Jira users"
	case "jira_search_issues":
		return "Searching Jira issues"
	case "jira_find":
		return "Finding Jira objects"
	case "jira_rest_get":
		return "Reading Jira REST response"
	case "jira_update_issue":
		if issueKey := textArg("issueKey"); issueKey != "" {
			return "Preparing Jira issue update for " + strings.ToUpper(issueKey)
		}
		return "Preparing Jira issue update"
	case "confluence_search":
		return "Searching Confluence pages"
	case "confluence_open_page":
		return "Opening Confluence page"
	case "confluence_open_space":
		if spaceKey := textArg("spaceKey"); spaceKey != "" {
			return "Opening Confluence space " + strings.ToUpper(spaceKey)
		}
		return "Opening Confluence space"
	case "confluence_rest_get":
		return "Reading Confluence REST response"
	default:
		return "Running Atlassian browser command"
	}
}

func atlassianProgressCompleteLabel(toolName string) string {
	switch toolName {
	case "jira_search_issues", "jira_find":
		return "Jira search results ready"
	case "confluence_search":
		return "Confluence search results ready"
	case "jira_get_issue", "jira_get_issue_history", "jira_get_issue_comments", "jira_get_issue_attachments", "jira_read_attachment", "jira_get_issue_link_types", "jira_find_users", "jira_rest_get":
		return "Jira response ready"
	case "confluence_open_page", "confluence_open_space", "confluence_rest_get":
		return "Confluence response ready"
	default:
		return "Atlassian browser command complete"
	}
}

func jiraMentionsSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Optional users to tag with native Jira mentions. Get accountId values from jira_find_users. Each mention must appear in the Markdown body as @Display Name, <@accountId>, or the exact mentionText value.",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"accountId":   stringSchema("Jira account ID from jira_find_users."),
				"displayName": stringSchema("Display name to tag, without the leading @."),
				"mentionText": stringSchema("Optional exact visible token in the Markdown body, for example @Alex. Use when the body does not contain @Display Name."),
			},
			"required":             []string{"accountId", "displayName"},
			"additionalProperties": false,
		},
	}
}

type jiraMentionRef struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
	MentionText string `json:"mentionText"`
}

type jiraMentionTextPart struct {
	Label string
	Text  string
}

var jiraPotentialMentionPattern = regexp.MustCompile(`(^|[\s([{<])@([A-Za-z][A-Za-z0-9._'-]{1,63})`)

func jiraMentionPreview(in []jiraMentionRef) []map[string]any {
	return jiraMentionArgs(in)
}

func jiraMentionArgs(in []jiraMentionRef) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		accountID := strings.TrimSpace(item.AccountID)
		displayName := strings.TrimSpace(strings.TrimPrefix(item.DisplayName, "@"))
		if accountID == "" || displayName == "" {
			continue
		}
		key := strings.ToLower(accountID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ref := map[string]any{
			"accountId":   accountID,
			"displayName": displayName,
		}
		if mentionText := normalizeJiraMentionText(item.MentionText); mentionText != "" {
			ref["mentionText"] = mentionText
		}
		out = append(out, ref)
	}
	return out
}

func validateJiraMentionUsage(parts []jiraMentionTextPart, mentions []jiraMentionRef) error {
	refs := normalizeJiraMentionRefs(mentions)
	for _, part := range parts {
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		if len(refs) == 0 {
			if token := firstPotentialJiraMention(text); token != "" {
				return fmt.Errorf("%s contains %s, but Jira native mentions require an accountId. Call jira_find_users first and pass the user in the mentions array", part.Label, token)
			}
			continue
		}
		lowerText := strings.ToLower(text)
		for _, ref := range refs {
			found := false
			for _, token := range jiraMentionTokenCandidates(ref) {
				if strings.Contains(lowerText, strings.ToLower(token)) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%s is missing a visible token for Jira mention %q. Include @%s, <@%s>, or set mentionText to the exact body token", part.Label, ref.DisplayName, ref.DisplayName, ref.AccountID)
			}
		}
	}
	return nil
}

func normalizeJiraMentionRefs(in []jiraMentionRef) []jiraMentionRef {
	out := make([]jiraMentionRef, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		accountID := strings.TrimSpace(item.AccountID)
		displayName := strings.TrimSpace(strings.TrimPrefix(item.DisplayName, "@"))
		if accountID == "" || displayName == "" {
			continue
		}
		key := strings.ToLower(accountID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, jiraMentionRef{
			AccountID:   accountID,
			DisplayName: displayName,
			MentionText: normalizeJiraMentionText(item.MentionText),
		})
	}
	return out
}

func jiraMentionTokenCandidates(ref jiraMentionRef) []string {
	tokens := []string{}
	if mentionText := normalizeJiraMentionText(ref.MentionText); mentionText != "" {
		tokens = append(tokens, mentionText)
	}
	displayName := strings.TrimSpace(strings.TrimPrefix(ref.DisplayName, "@"))
	if displayName != "" {
		tokens = append(tokens, "@"+displayName)
	}
	accountID := strings.TrimSpace(ref.AccountID)
	if accountID != "" {
		tokens = append(tokens, "<@"+accountID+">")
	}
	return dedupeStrings(tokens)
}

func normalizeJiraMentionText(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(text, "<@") {
		return text
	}
	if strings.HasPrefix(text, "@") {
		return text
	}
	return "@" + strings.TrimLeft(text, "@")
}

func firstPotentialJiraMention(text string) string {
	matches := jiraPotentialMentionPattern.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) < 3 {
			continue
		}
		token := strings.TrimSpace(match[2])
		if token == "" {
			continue
		}
		return "@" + token
	}
	return ""
}

func normalizeJiraIssueKeys(raw []string, except string) []string {
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	except = strings.TrimSpace(strings.ToUpper(except))
	for _, item := range raw {
		key := strings.TrimSpace(strings.ToUpper(item))
		if key == "" || key == except {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func normalizeNonEmptyStrings(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func jiraUpdateFieldNames(summary, description string, clearDescription bool, issueType string, assigneeAccountID string, priorityName string, labelsSet bool, componentsSet bool, fields map[string]any) []string {
	seen := map[string]struct{}{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		seen[name] = struct{}{}
	}
	if strings.TrimSpace(summary) != "" {
		add("summary")
	}
	if strings.TrimSpace(description) != "" || clearDescription {
		add("description")
	}
	if strings.TrimSpace(issueType) != "" {
		add("issuetype")
	}
	if strings.TrimSpace(assigneeAccountID) != "" {
		add("assignee")
	}
	if strings.TrimSpace(priorityName) != "" {
		add("priority")
	}
	if labelsSet {
		add("labels")
	}
	if componentsSet {
		add("components")
	}
	for key, value := range fields {
		if value == nil {
			continue
		}
		if strings.TrimSpace(key) == "" {
			continue
		}
		add(key)
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func dedupeStrings(raw []string) []string {
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, item := range raw {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	return out
}

func resolveJiraAttachmentUploadSource(ctx context.Context, exec ExecContext, artifactID, filePath, localPath, fileName, mimeType, contentText, contentBase64 string) (string, string, []byte, string, Output, bool) {
	return resolveFileUploadSource(ctx, exec, artifactID, filePath, localPath, fileName, mimeType, contentText, contentBase64, maxJiraAttachmentUploadBytes, "Jira attachment upload")
}

func resolveFileUploadSource(ctx context.Context, exec ExecContext, artifactID, filePath, localPath, fileName, mimeType, contentText, contentBase64 string, maxBytes int64, uploadLabel string) (string, string, []byte, string, Output, bool) {
	artifactID = strings.TrimSpace(artifactID)
	filePath = strings.TrimSpace(filePath)
	localPath = strings.TrimSpace(localPath)
	if strings.TrimSpace(uploadLabel) == "" {
		uploadLabel = "file upload"
	}
	contentTextSet := contentText != ""
	contentBase64 = strings.TrimSpace(contentBase64)
	contentBase64Set := contentBase64 != ""
	sourceCount := 0
	if artifactID != "" {
		sourceCount++
	}
	if filePath != "" {
		sourceCount++
	}
	if localPath != "" {
		sourceCount++
	}
	if contentTextSet {
		sourceCount++
	}
	if contentBase64Set {
		sourceCount++
	}
	if sourceCount != 1 {
		return "", "", nil, "", JSONError("provide exactly one of artifact_id, path, local_path, contentText, or contentBase64"), false
	}

	name := strings.TrimSpace(fileName)
	mimeValue := strings.TrimSpace(strings.ToLower(mimeType))
	source := "inline"
	var raw []byte
	if artifactID != "" {
		artifact, artifactSource, errOut, ok := requireUploadArtifactSource(ctx, exec, artifactID)
		if !ok {
			return "", "", nil, "", errOut, false
		}
		data, err := readUploadArtifactBytes(exec.CfgDir, artifact, maxBytes, uploadLabel)
		if err != nil {
			return "", "", nil, "", JSONError(err.Error()), false
		}
		raw = data
		source = artifactSource
		if name == "" {
			name = strings.TrimSpace(artifact.Title)
		}
		if mimeValue == "" {
			mimeValue = strings.TrimSpace(strings.ToLower(stringMapValue(artifact.Metadata, "mimeType", "mime_type")))
		}
		if mimeValue == "" {
			mimeValue = strings.TrimSpace(strings.ToLower(stringMapValue(artifact.Metadata, "detectedMimeType", "detected_mime_type")))
		}
	} else if filePath != "" {
		data, displayPath, errOut, ok := readUploadProjectFileBytes(ctx, exec, filePath, maxBytes, uploadLabel)
		if !ok {
			return "", "", nil, "", errOut, false
		}
		raw = data
		source = "project_file"
		if name == "" {
			name = path.Base(displayPath)
		}
	} else if localPath != "" {
		data, displayPath, errOut, ok := readManagedLocalFileBytes(exec.CfgDir, localPath, maxBytes, uploadLabel)
		if !ok {
			return "", "", nil, "", errOut, false
		}
		raw = data
		source = "managed_local_file"
		if name == "" {
			name = path.Base(filepath.ToSlash(displayPath))
		}
	} else if contentTextSet {
		raw = []byte(contentText)
	} else {
		data, err := base64.StdEncoding.DecodeString(contentBase64)
		if err != nil {
			return "", "", nil, "", JSONError("contentBase64 is not valid base64"), false
		}
		raw = data
	}
	name = sanitizeJiraUploadFileName(name)
	if name == "" {
		return "", "", nil, "", JSONError("fileName is required"), false
	}
	if len(raw) == 0 {
		return "", "", nil, "", JSONError("attachment content is empty"), false
	}
	if mimeValue == "" {
		mimeValue = inferUploadMimeType(name, raw)
	}
	return name, mimeValue, raw, source, Output{}, true
}

func requireUploadArtifactSource(ctx context.Context, exec ExecContext, artifactID string) (store.Artifact, string, Output, bool) {
	artifact, out, ok := requireThreadAttachment(ctx, exec, artifactID)
	if ok {
		return artifact, "thread_attachment", Output{}, true
	}
	if exec.Store == nil {
		return store.Artifact{}, "", out, false
	}
	threadID := strings.TrimSpace(exec.ThreadID)
	if threadID == "" {
		return store.Artifact{}, "", out, false
	}
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return store.Artifact{}, "", out, false
	}
	artifact, err := exec.Store.GetArtifact(ctx, id)
	if err != nil {
		return store.Artifact{}, "", out, false
	}
	if !strings.EqualFold(strings.TrimSpace(artifact.ThreadID), threadID) {
		return store.Artifact{}, "", JSONError("artifact is not available in this thread"), false
	}
	if artifact.ContentText == nil && artifact.ContentPath == nil {
		return store.Artifact{}, "", JSONError("artifact content is not available for upload"), false
	}
	return artifact, "artifact", Output{}, true
}

func readUploadProjectFileBytes(ctx context.Context, exec ExecContext, filePath string, maxBytes int64, uploadLabel string) ([]byte, string, Output, bool) {
	ps, pid, errOut, ok := requireProject(exec)
	if !ok {
		return nil, "", errOut, false
	}
	displayPath, err := normalizeRelPathForDisplay(filePath, false)
	if err != nil {
		return nil, "", JSONError(err.Error()), false
	}
	f, err := ps.OpenFile(pid, displayPath)
	if err != nil {
		return nil, "", JSONError(fmt.Sprintf("File not found: %s", strings.TrimSpace(filePath))), false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, "", JSONError(fmt.Sprintf("Error reading file: %v", err)), false
	}
	if int64(len(data)) > maxBytes {
		return nil, "", JSONError(fmt.Sprintf("%s exceeds cap of %d bytes", uploadLabel, maxBytes)), false
	}
	return data, displayPath, Output{}, true
}

func readManagedLocalFileBytes(cfgDir string, localPath string, maxBytes int64, uploadLabel string) ([]byte, string, Output, bool) {
	cfgDir = strings.TrimSpace(cfgDir)
	localPath = strings.TrimSpace(localPath)
	if cfgDir == "" {
		return nil, "", JSONError("App config directory is not available for managed local file upload"), false
	}
	if localPath == "" {
		return nil, "", JSONError("local_path is required"), false
	}
	baseAbs, err := filepath.Abs(cfgDir)
	if err != nil {
		return nil, "", JSONError(fmt.Sprintf("Invalid app config directory: %v", err)), false
	}
	target := localPath
	if !filepath.IsAbs(target) {
		clean, err := cleanRelPath(target)
		if err != nil {
			return nil, "", JSONError(err.Error()), false
		}
		target = filepath.Join(baseAbs, filepath.FromSlash(clean))
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return nil, "", JSONError(fmt.Sprintf("Invalid local_path: %v", err)), false
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return nil, "", JSONError("local_path must be inside the app-managed config directory"), false
	}
	relSlash := filepath.ToSlash(rel)
	if !fileartifact.IsManagedDownloadRelPath(relSlash) {
		return nil, "", JSONError("local_path must point to an app-managed download returned by a tool"), false
	}
	info, err := os.Stat(targetAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", JSONError(fmt.Sprintf("Managed local file not found: %s", relSlash)), false
		}
		return nil, "", JSONError(fmt.Sprintf("Cannot stat managed local file: %v", err)), false
	}
	if info.IsDir() {
		return nil, "", JSONError("local_path points to a directory, not a file"), false
	}
	if info.Size() > maxBytes {
		return nil, "", JSONError(fmt.Sprintf("%s exceeds cap of %d bytes", uploadLabel, maxBytes)), false
	}
	f, err := os.Open(targetAbs)
	if err != nil {
		return nil, "", JSONError(fmt.Sprintf("Cannot open managed local file: %v", err)), false
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, "", JSONError(fmt.Sprintf("Error reading managed local file: %v", err)), false
	}
	if int64(len(data)) > maxBytes {
		return nil, "", JSONError(fmt.Sprintf("%s exceeds cap of %d bytes", uploadLabel, maxBytes)), false
	}
	return data, relSlash, Output{}, true
}

func readUploadArtifactBytes(cfgDir string, artifact store.Artifact, maxBytes int64, uploadLabel string) ([]byte, error) {
	if artifact.ContentText != nil {
		data := []byte(*artifact.ContentText)
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("%s exceeds cap of %d bytes", uploadLabel, maxBytes)
		}
		return data, nil
	}
	if artifact.ContentPath == nil {
		return nil, fmt.Errorf("attachment content path is missing")
	}
	rel := strings.TrimSpace(*artifact.ContentPath)
	if rel == "" {
		return nil, fmt.Errorf("attachment content path is missing")
	}
	clean, err := cleanRelPath(rel)
	if err != nil {
		return nil, err
	}
	full := filepath.Join(cfgDir, filepath.FromSlash(clean))
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds cap of %d bytes", uploadLabel, maxBytes)
	}
	return data, nil
}

func sanitizeJiraUploadFileName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSpace(strings.Trim(name, "."))
	name = strings.ReplaceAll(name, "\x00", "")
	if len(name) > 180 {
		ext := path.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		if len(ext) > 32 {
			ext = ext[:32]
		}
		limit := 180 - len(ext)
		if limit < 1 {
			limit = 1
		}
		if len(stem) > limit {
			stem = stem[:limit]
		}
		name = stem + ext
	}
	return name
}

func inferUploadMimeType(fileName string, raw []byte) string {
	if extType := strings.TrimSpace(mime.TypeByExtension(strings.ToLower(path.Ext(fileName)))); extType != "" {
		return strings.ToLower(extType)
	}
	if len(raw) > 0 {
		return strings.ToLower(http.DetectContentType(raw))
	}
	return "application/octet-stream"
}

func uploadBytesAreText(fileName, mimeType string, raw []byte) bool {
	m := strings.ToLower(strings.TrimSpace(mimeType))
	if strings.HasPrefix(m, "text/") || strings.Contains(m, "json") || strings.Contains(m, "xml") || strings.Contains(m, "csv") || strings.Contains(m, "yaml") || strings.Contains(m, "markdown") {
		return true
	}
	switch strings.ToLower(path.Ext(fileName)) {
	case ".txt", ".md", ".markdown", ".json", ".csv", ".tsv", ".xml", ".yaml", ".yml", ".log", ".sql":
		return true
	}
	return false
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func normalizeObjectSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return objectSchema(map[string]any{}, nil)
	}
	if schemaType, _ := schema["type"].(string); schemaType == "object" || schema["properties"] != nil {
		out := map[string]any{}
		for key, value := range schema {
			out[key] = value
		}
		out["type"] = "object"
		if _, ok := out["properties"].(map[string]any); !ok {
			out["properties"] = map[string]any{}
		}
		return out
	}
	return objectSchema(schema, nil)
}

func closeTopLevelObjectSchema(schema map[string]any) map[string]any {
	out := normalizeObjectSchema(schema)
	out["additionalProperties"] = false
	return out
}

func stringSchema(description string) map[string]any {
	return map[string]any{
		"type":        "string",
		"description": description,
	}
}
