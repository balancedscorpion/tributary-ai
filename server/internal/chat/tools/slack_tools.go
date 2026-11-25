package tools

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"ai-analyst-local/internal/chat/types"
	slackapi "ai-analyst-local/internal/slack"
)

const (
	SlackToolStatus                 = "slack_status"
	SlackToolCheckConnection        = "slack_check_connection"
	SlackToolListConversations      = "slack_list_conversations"
	SlackToolGetConversation        = "slack_get_conversation"
	SlackToolGetConversationMembers = "slack_get_conversation_members"
	SlackToolGetMessages            = "slack_get_messages"
	SlackToolGetThreadReplies       = "slack_get_thread_replies"
	SlackToolGetUser                = "slack_get_user"
	SlackToolSearchMessages         = "slack_search_messages"
	SlackToolDownloadFile           = "slack_download_file"
	SlackToolPostMessage            = "slack_post_message"
	SlackToolUploadFile             = "slack_upload_file"
)

const (
	maxSlackFileUploadBytes int64 = 128 << 20
	slackFilePreviewBytes         = 4 << 10
)

const (
	slackPostMessageExecuteOperation = "slack_post_message_execute"
	slackUploadFileExecuteOperation  = "slack_upload_file_execute"
)

var slackReadToolNames = []string{
	SlackToolStatus,
	SlackToolCheckConnection,
	SlackToolListConversations,
	SlackToolGetConversation,
	SlackToolGetConversationMembers,
	SlackToolGetMessages,
	SlackToolGetThreadReplies,
	SlackToolGetUser,
	SlackToolSearchMessages,
	SlackToolDownloadFile,
}

var slackWriteToolNames = []string{
	SlackToolPostMessage,
	SlackToolUploadFile,
}

func SlackToolNames() []string {
	out := append([]string(nil), slackReadToolNames...)
	out = append(out, slackWriteToolNames...)
	return out
}

func SlackReadToolNames() []string {
	return append([]string(nil), slackReadToolNames...)
}

func SlackWriteToolNames() []string {
	return append([]string(nil), slackWriteToolNames...)
}

func SlackToolNamesForAccess(readEnabled, writeEnabled bool) []string {
	out := []string{}
	if readEnabled {
		out = append(out, slackReadToolNames...)
	}
	if writeEnabled {
		out = append(out, slackWriteToolNames...)
	}
	return out
}

func IsSlackToolName(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	for _, candidate := range SlackToolNames() {
		if toolName == candidate {
			return true
		}
	}
	return false
}

func IsSlackReadToolName(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	for _, candidate := range slackReadToolNames {
		if toolName == candidate {
			return true
		}
	}
	return false
}

func IsSlackWriteToolName(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	for _, candidate := range slackWriteToolNames {
		if toolName == candidate {
			return true
		}
	}
	return false
}

func slackManager(manager any) (*slackapi.Manager, bool) {
	m, ok := manager.(*slackapi.Manager)
	return m, ok && m != nil
}

func slackToolDefinition(name string, description string, parameters map[string]any) types.ToolDefinition {
	parameters = normalizeObjectSchema(parameters)
	parameters["additionalProperties"] = false
	return types.ToolDefinition{
		Type: "function",
		Function: types.ToolFunctionDefinition{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

func slackResultMetadata(toolName, kind string) map[string]any {
	return slackToolResultMetadata(toolName, kind, true, nil)
}

func slackApprovalMetadata(toolName string) map[string]any {
	return slackToolResultMetadata(toolName, "approval", false, map[string]any{
		"widget": "approval",
	})
}

func slackWriteResultMetadata(toolName, kind string) map[string]any {
	return slackToolResultMetadata(toolName, kind, false, nil)
}

func slackToolResultMetadata(toolName, kind string, readonly bool, extra map[string]any) map[string]any {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "slack_result"
	}
	metadata := map[string]any{
		"provider":    "slack",
		"integration": "slack",
		"product":     "slack",
		"kind":        kind,
		"readonly":    readonly,
	}
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		metadata[key] = value
	}
	return toolCatalogResultMetadata(toolName, kind, metadata)
}

func slackReadOutput(toolName string, result map[string]any, kind string) Output {
	return JSONOKWithMetadata(result, slackResultMetadata(toolName, kind))
}

func slackWriteOutput(toolName string, result map[string]any, kind string) Output {
	return JSONOKWithMetadata(result, slackWriteResultMetadata(toolName, kind))
}

func slackApprovalOutput(toolName string, approval any) Output {
	return JSONOKWithMetadata(map[string]any{"approval": approval}, slackApprovalMetadata(toolName))
}

func slackPersistentPermissionRequiredOutput(toolName, operation string, target map[string]any) Output {
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
			"message":             "Persistent permission approval is required before this Slack operation can run.",
		},
		ResultMetadata: slackToolResultMetadata(toolName, "permission_required", false, map[string]any{
			"widget": "permission_required",
		}),
	}
}

func slackWriteOperationOutput(toolName string, result map[string]any, kind string) Output {
	out := cloneStringAnyMap(result)
	if out == nil {
		out = map[string]any{}
	}
	return slackWriteOutput(toolName, out, kind)
}

func slackDryRunResult(toolName, operation, kind string, preview map[string]any) map[string]any {
	return map[string]any{
		"ok":            true,
		"dry_run":       true,
		"validated":     true,
		"tool_name":     toolName,
		"operation":     operation,
		"would_execute": operation,
		"preview":       cloneStringAnyMap(preview),
		"message":       "Dry run only. Slack was not changed.",
	}
}

func slackDryRunOutput(toolName, operation, kind string, preview map[string]any) Output {
	return slackWriteOutput(toolName, slackDryRunResult(toolName, operation, kind, preview), kind)
}

func newSlackStatusTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolStatus,
			"Return local Slack OAuth integration status. Does not expose token values.",
			map[string]any{},
		),
		Exec: func(ctx context.Context, _ ExecContext, _ json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			status, err := m.Status(ctx)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolStatus, map[string]any{"status": status}, "status"), nil
		},
	}
}

func newSlackCheckConnectionTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolCheckConnection,
			"Check the stored Slack OAuth connection by calling read-only Slack API methods.",
			map[string]any{},
		),
		Exec: func(ctx context.Context, _ ExecContext, _ json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			result, err := m.Check(ctx)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolCheckConnection, map[string]any{"connection": result}, "connection_status"), nil
		},
	}
}

func newSlackListConversationsTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolListConversations,
			"List Slack conversations visible to the OAuth user token. Results may be filtered by allowed channel settings.",
			objectSchema(map[string]any{
				"types":            stringSchema("Comma-separated Slack conversation types. Defaults to public_channel,private_channel,mpim,im."),
				"limit":            intSchema("Maximum conversations to return. Defaults to 20, max 100."),
				"cursor":           stringSchema("Slack pagination cursor from response_metadata.next_cursor."),
				"exclude_archived": boolSchema("Whether to exclude archived conversations."),
			}, nil),
		),
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var in slackapi.ConversationListRequest
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			result, err := m.ListConversations(ctx, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolListConversations, map[string]any{"conversations": result}, "conversation_list"), nil
		},
	}
}

func newSlackGetConversationTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolGetConversation,
			"Read Slack conversation metadata by channel ID.",
			objectSchema(map[string]any{
				"channel_id": stringSchema("Slack channel/conversation ID, for example C123, G123, or D123."),
			}, []string{"channel_id"}),
		),
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var in slackapi.ConversationRequest
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			result, err := m.GetConversation(ctx, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolGetConversation, map[string]any{"conversation": result}, "conversation"), nil
		},
	}
}

func newSlackGetConversationMembersTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolGetConversationMembers,
			"List member user IDs for a Slack conversation.",
			objectSchema(map[string]any{
				"channel_id": stringSchema("Slack channel/conversation ID."),
				"limit":      intSchema("Maximum members to return. Defaults to 50, max 200."),
				"cursor":     stringSchema("Slack pagination cursor from response_metadata.next_cursor."),
			}, []string{"channel_id"}),
		),
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var in slackapi.ConversationMembersRequest
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			result, err := m.GetConversationMembers(ctx, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolGetConversationMembers, map[string]any{"members": result}, "conversation_members"), nil
		},
	}
}

func newSlackGetMessagesTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolGetMessages,
			"Read a bounded page of Slack messages from a conversation.",
			objectSchema(slackMessagePageSchema(map[string]any{
				"channel_id": stringSchema("Slack channel/conversation ID."),
			}), []string{"channel_id"}),
		),
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var in slackapi.MessagesRequest
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			result, err := m.GetMessages(ctx, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolGetMessages, map[string]any{"messages": result}, "message_history"), nil
		},
	}
}

func newSlackGetThreadRepliesTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolGetThreadReplies,
			"Read a bounded page of replies for a Slack message thread.",
			objectSchema(slackMessagePageSchema(map[string]any{
				"channel_id": stringSchema("Slack channel/conversation ID."),
				"thread_ts":  stringSchema("Slack thread root timestamp."),
			}), []string{"channel_id", "thread_ts"}),
		),
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var in slackapi.ThreadRepliesRequest
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			result, err := m.GetThreadReplies(ctx, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolGetThreadReplies, map[string]any{"replies": result}, "thread_replies"), nil
		},
	}
}

func newSlackGetUserTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolGetUser,
			"Read Slack user profile metadata by user ID.",
			objectSchema(map[string]any{
				"user_id": stringSchema("Slack user ID, for example U123."),
			}, []string{"user_id"}),
		),
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var in slackapi.UserRequest
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			result, err := m.GetUser(ctx, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolGetUser, map[string]any{"user": result}, "user"), nil
		},
	}
}

func newSlackSearchMessagesTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolSearchMessages,
			"Search Slack messages visible to the OAuth user token. Requires Slack search:read scope.",
			objectSchema(map[string]any{
				"query":    stringSchema("Slack search query."),
				"count":    intSchema("Maximum search results to return. Defaults to 20, max 25."),
				"page":     intSchema("Slack search page number."),
				"sort":     enumStringSchema("Sort field.", []string{"score", "timestamp"}),
				"sort_dir": enumStringSchema("Sort direction.", []string{"asc", "desc"}),
			}, []string{"query"}),
		),
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var in slackapi.SearchMessagesRequest
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			result, err := m.SearchMessages(ctx, in)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolSearchMessages, map[string]any{"search": result}, "message_search"), nil
		},
	}
}

func newSlackDownloadFileTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolDownloadFile,
			"Download a Slack file by file ID into the app-managed download cache and return a local_path that can be inspected by the model.",
			objectSchema(map[string]any{
				"file_id":          stringSchema("Slack file ID, for example F123."),
				"channel_id":       stringSchema("Optional Slack channel/conversation ID associated with the file."),
				"maxDownloadBytes": intSchema("Optional maximum bytes to download. Defaults to 134217728 and cannot exceed that cap."),
			}, []string{"file_id"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var raw map[string]any
			if err := json.Unmarshal(args, &raw); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			maxBytes := int64(0)
			if value, ok := slackInt64MapValue(raw, "maxDownloadBytes", "max_download_bytes"); ok && value > 0 {
				maxBytes = value
			}
			result, err := m.DownloadFile(ctx, exec.CfgDir, slackapi.DownloadFileRequest{
				FileID:           stringMapValue(raw, "file_id", "fileId"),
				ChannelID:        stringMapValue(raw, "channel_id", "channel"),
				MaxDownloadBytes: maxBytes,
			})
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return slackReadOutput(SlackToolDownloadFile, result, "file_download"), nil
		},
	}
}

func slackInt64MapValue(m map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
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

func newSlackPostMessageTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolPostMessage,
			"Post a Slack message after persistent permission approval. The message is not sent until the user approves the exact destination and text.",
			objectSchema(map[string]any{
				"channel_id":      stringSchema("Slack channel/conversation ID to post into, for example C123, G123, or D123."),
				"send_as":         enumStringSchema("Identity to post as. Defaults to the Slack setting.", []string{"user", "bot"}),
				"text":            stringSchema("Slack message text. Slack mrkdwn is allowed; keep the approved text exact."),
				"thread_ts":       stringSchema("Optional Slack thread root timestamp to reply in a thread."),
				"reply_broadcast": boolSchema("For thread replies, whether to also broadcast the reply to the channel."),
				"unfurl_links":    boolSchema("Whether Slack should unfurl links."),
				"unfurl_media":    boolSchema("Whether Slack should unfurl media."),
				"dry_run":         boolSchema("Validate and preview the Slack message without sending it or requesting permission."),
			}, []string{"channel_id", "text"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var raw map[string]any
			if err := json.Unmarshal(args, &raw); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			channel := strings.TrimSpace(stringMapValue(raw, "channel_id", "channel"))
			text := strings.TrimSpace(stringMapValue(raw, "text"))
			if channel == "" || text == "" {
				return JSONError("channel_id and text are required"), nil
			}
			sendAs := m.ResolveWriteIdentity(stringMapValue(raw, "send_as", "sendAs"))
			if errOut, ok := requireSlackWriteScopes(ctx, m, sendAs, "chat:write"); !ok {
				return errOut, nil
			}
			dryRun, _ := boolMapValue(raw, "dry_run", "dryRun")
			threadTS := strings.TrimSpace(stringMapValue(raw, "thread_ts"))
			replyBroadcast, replyBroadcastSet := boolMapValue(raw, "reply_broadcast", "replyBroadcast")
			unfurlLinks, unfurlLinksSet := boolMapValue(raw, "unfurl_links", "unfurlLinks")
			unfurlMedia, unfurlMediaSet := boolMapValue(raw, "unfurl_media", "unfurlMedia")
			content := map[string]any{
				"text":          text,
				"send_as":       sendAs,
				"source_format": "slack_mrkdwn",
				"target_format": "slack_message",
			}
			execArgs := map[string]any{
				"channel_id": channel,
				"send_as":    sendAs,
				"text":       text,
			}
			if threadTS != "" {
				content["thread_ts"] = threadTS
				execArgs["thread_ts"] = threadTS
				if replyBroadcastSet {
					content["reply_broadcast"] = replyBroadcast
					execArgs["reply_broadcast"] = replyBroadcast
				}
			}
			if unfurlLinksSet {
				content["unfurl_links"] = unfurlLinks
				execArgs["unfurl_links"] = unfurlLinks
			}
			if unfurlMediaSet {
				content["unfurl_media"] = unfurlMedia
				execArgs["unfurl_media"] = unfurlMedia
			}
			if dryRun {
				return slackDryRunOutput(SlackToolPostMessage, slackPostMessageExecuteOperation, "post_message", map[string]any{
					"target":  channel,
					"content": content,
					"args":    execArgs,
				}), nil
			}
			if exec.PermissionApproved {
				emitSlackProgress(exec, SlackToolPostMessage, "running", "Posting approved Slack message", 25, map[string]any{
					"provider": "slack",
					"target":   channel,
				})
				result, err := m.RunApprovedOperation(ctx, slackPostMessageExecuteOperation, execArgs)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				emitSlackProgress(exec, SlackToolPostMessage, "completed", "Posted Slack message", 100, map[string]any{
					"provider": "slack",
					"target":   channel,
				})
				return slackWriteOperationOutput(SlackToolPostMessage, result, "post_message"), nil
			}
			emitSlackProgress(exec, SlackToolPostMessage, "permission_required", "Persistent approval is required before posting this Slack message", 100, map[string]any{
				"provider": "slack",
				"target":   channel,
			})
			return slackPersistentPermissionRequiredOutput(SlackToolPostMessage, slackPostMessageExecuteOperation, map[string]any{
				"type":       "conversation",
				"channel_id": channel,
				"thread_ts":  nilIfEmptyString(threadTS),
				"send_as":    sendAs,
			}), nil
		},
	}
}

func newSlackUploadFileTool(manager any) Tool {
	return ExecutableTool{
		Def: slackToolDefinition(
			SlackToolUploadFile,
			"Upload and share a file in Slack after persistent permission approval. Use artifact_id for a chat-uploaded file, path for a project file, local_path for an app-managed downloaded file, or contentText/contentBase64 with fileName for generated content. Exactly one file source is allowed.",
			objectSchema(map[string]any{
				"channel_id":      stringSchema("Slack channel/conversation ID to share the file into."),
				"send_as":         enumStringSchema("Identity to upload as. Defaults to the Slack setting.", []string{"user", "bot"}),
				"artifact_id":     stringSchema("Optional thread-scoped uploaded file artifact id to upload."),
				"path":            stringSchema("Optional project-relative file path to upload from the attached workspace."),
				"local_path":      stringSchema("Optional app-managed local download path returned by a previous tool, for example jira_read_attachment.local_path."),
				"fileName":        stringSchema("File name to use in Slack. Required for contentText/contentBase64 sources; optional for artifact_id/path/local_path."),
				"mimeType":        stringSchema("Optional MIME type. Inferred from the file name/content when omitted."),
				"title":           stringSchema("Optional Slack file title. Defaults to fileName."),
				"initial_comment": stringSchema("Optional message text to include with the file upload."),
				"thread_ts":       stringSchema("Optional Slack thread root timestamp to upload into a thread."),
				"alt_text":        stringSchema("Optional alt text for screen readers."),
				"contentText":     stringSchema("Optional UTF-8 text content for a new file upload."),
				"contentBase64":   stringSchema("Optional base64 file content for a new binary or text file upload."),
				"dry_run":         boolSchema("Validate and preview the Slack file upload without uploading it or requesting permission."),
			}, []string{"channel_id"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			m, ok := slackManager(manager)
			if !ok {
				return JSONError("Slack integration is not configured"), nil
			}
			var rawArgs map[string]any
			if err := json.Unmarshal(args, &rawArgs); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			channel := strings.TrimSpace(stringMapValue(rawArgs, "channel_id", "channel"))
			if channel == "" {
				return JSONError("channel_id is required"), nil
			}
			sendAs := m.ResolveWriteIdentity(stringMapValue(rawArgs, "send_as", "sendAs"))
			if errOut, ok := requireSlackWriteScopes(ctx, m, sendAs, "files:write"); !ok {
				return errOut, nil
			}
			fileName, mimeType, raw, source, errOut, ok := resolveFileUploadSource(
				ctx,
				exec,
				stringMapValue(rawArgs, "artifact_id", "artifactId"),
				stringMapValue(rawArgs, "path"),
				stringMapValue(rawArgs, "local_path", "localPath"),
				stringMapValue(rawArgs, "fileName", "file_name"),
				stringMapValue(rawArgs, "mimeType", "mime_type"),
				stringMapValue(rawArgs, "contentText", "content_text"),
				stringMapValue(rawArgs, "contentBase64", "content_base64"),
				maxSlackFileUploadBytes,
				"Slack file upload",
			)
			if !ok {
				return errOut, nil
			}
			sourceRef := strings.TrimSpace(stringMapValue(rawArgs, "artifact_id", "artifactId"))
			if sourceRef == "" {
				sourceRef = strings.TrimSpace(stringMapValue(rawArgs, "path"))
			}
			if sourceRef == "" {
				sourceRef = strings.TrimSpace(stringMapValue(rawArgs, "local_path", "localPath"))
			}
			if sourceRef == "" {
				sourceRef = "inline"
			}
			if int64(len(raw)) > maxSlackFileUploadBytes {
				return JSONError(fmt.Sprintf("Slack file upload exceeds cap of %d bytes", maxSlackFileUploadBytes)), nil
			}
			sum := sha256.Sum256(raw)
			shaHex := hex.EncodeToString(sum[:])
			preview := ""
			if uploadBytesAreText(fileName, mimeType, raw) {
				preview = string(raw)
				if len(preview) > slackFilePreviewBytes {
					preview = preview[:slackFilePreviewBytes]
				}
			}
			title := strings.TrimSpace(stringMapValue(rawArgs, "title"))
			if title == "" {
				title = fileName
			}
			initialComment := strings.TrimSpace(stringMapValue(rawArgs, "initial_comment", "initialComment"))
			threadTS := strings.TrimSpace(stringMapValue(rawArgs, "thread_ts"))
			altText := strings.TrimSpace(stringMapValue(rawArgs, "alt_text", "altText"))
			dryRun, _ := boolMapValue(rawArgs, "dry_run", "dryRun")
			execArgs := map[string]any{
				"channel_id":      channel,
				"send_as":         sendAs,
				"file_name":       fileName,
				"title":           title,
				"mime_type":       mimeType,
				"initial_comment": initialComment,
				"thread_ts":       threadTS,
				"alt_text":        altText,
				"content_base64":  base64.StdEncoding.EncodeToString(raw),
				"sha256":          shaHex,
				"source":          source,
				"source_ref":      sourceRef,
			}
			if dryRun {
				return slackDryRunOutput(SlackToolUploadFile, slackUploadFileExecuteOperation, "file_upload", map[string]any{
					"target": channel,
					"content": map[string]any{
						"file_name":       fileName,
						"title":           title,
						"mime_type":       mimeType,
						"sha256":          shaHex,
						"source":          source,
						"source_ref":      sourceRef,
						"initial_comment": initialComment,
						"preview":         preview,
					},
					"args": execArgs,
				}), nil
			}
			if exec.PermissionApproved {
				emitSlackProgress(exec, SlackToolUploadFile, "running", "Uploading approved Slack file", 25, map[string]any{
					"provider": "slack",
					"target":   channel,
				})
				result, err := m.RunApprovedOperation(ctx, slackUploadFileExecuteOperation, execArgs)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				emitSlackProgress(exec, SlackToolUploadFile, "completed", "Uploaded Slack file", 100, map[string]any{
					"provider": "slack",
					"target":   channel,
				})
				return slackWriteOperationOutput(SlackToolUploadFile, result, "file_upload"), nil
			}
			emitSlackProgress(exec, SlackToolUploadFile, "permission_required", "Persistent approval is required before uploading this Slack file", 100, map[string]any{
				"provider": "slack",
				"target":   channel,
			})
			return slackPersistentPermissionRequiredOutput(SlackToolUploadFile, slackUploadFileExecuteOperation, map[string]any{
				"type":       "conversation",
				"channel_id": channel,
				"thread_ts":  nilIfEmptyString(threadTS),
				"send_as":    sendAs,
				"file_name":  fileName,
				"source":     source,
				"source_ref": sourceRef,
			}), nil
		},
	}
}

func requireSlackWriteScopes(ctx context.Context, manager *slackapi.Manager, identity string, scopes ...string) (Output, bool) {
	status, err := manager.Status(ctx)
	if err != nil {
		return JSONError(err.Error()), false
	}
	granted := map[string]struct{}{}
	grantedScopes := status.UserGrantedScopes
	if identity == slackapi.IdentityBot {
		grantedScopes = status.BotGrantedScopes
	}
	for _, scope := range grantedScopes {
		granted[strings.ToLower(strings.TrimSpace(scope))] = struct{}{}
	}
	missing := make([]string, 0)
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "" {
			continue
		}
		if _, ok := granted[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	if len(missing) > 0 {
		return JSONError("Slack " + identity + " OAuth profile is missing required scope(s): " + strings.Join(missing, ", ") + ". Reconnect Slack from Settings to grant the new scopes."), false
	}
	return Output{}, true
}

func emitSlackProgress(exec ExecContext, toolName, phase, label string, progress int, extra map[string]any) {
	if exec.OnProgress == nil {
		return
	}
	payload := map[string]any{
		"provider":  "slack",
		"tool_name": toolName,
		"phase":     phase,
		"label":     label,
	}
	if progress >= 0 {
		payload["percent"] = progress
	}
	for key, value := range extra {
		payload[key] = value
	}
	exec.OnProgress(payload)
}

func nilIfEmptyString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func slackMessagePageSchema(base map[string]any) map[string]any {
	base["limit"] = intSchema("Maximum messages to return. Defaults to 20, max 100.")
	base["cursor"] = stringSchema("Slack pagination cursor from response_metadata.next_cursor.")
	base["oldest"] = stringSchema("Only messages after this Slack timestamp are included.")
	base["latest"] = stringSchema("Only messages before this Slack timestamp are included.")
	base["inclusive"] = boolSchema("Whether oldest/latest boundary messages are included.")
	return base
}

func enumStringSchema(description string, values []string) map[string]any {
	out := stringSchema(description)
	out["enum"] = values
	return out
}
