package publicapi

import (
	"encoding/json"
	"strings"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func publicThreadDTO(t store.Thread) map[string]any {
	out := map[string]any{
		"object":         "thread",
		"id":             t.ID,
		"title":          t.Title,
		"status":         t.Status,
		"artifact_count": t.ArtifactCount,
		"created_at":     t.CreatedAt.UnixMilli(),
		"updated_at":     t.UpdatedAt.UnixMilli(),
	}
	if t.Summary != nil {
		out["summary"] = *t.Summary
	}
	if t.ProjectID != nil {
		out["project_id"] = *t.ProjectID
	}
	if t.LastMessageAt != nil {
		out["last_message_at"] = t.LastMessageAt.UnixMilli()
	}
	if t.LastRunID != nil {
		out["last_run_id"] = *t.LastRunID
	}
	return out
}

func publicRunDTO(r store.Run) map[string]any {
	out := map[string]any{
		"object":         "run",
		"id":             r.ID,
		"thread_id":      r.ThreadID,
		"status":         r.Status,
		"provider_id":    r.ProviderID,
		"model":          r.Model,
		"max_iterations": r.MaxIterations,
		"created_at":     r.CreatedAt.UnixMilli(),
		"updated_at":     r.UpdatedAt.UnixMilli(),
	}
	if strings.TrimSpace(r.Purpose) != "" {
		out["purpose"] = r.Purpose
	}
	if strings.TrimSpace(r.ConnectionID) != "" {
		out["connection_id"] = r.ConnectionID
	}
	if strings.TrimSpace(r.PlanID) != "" {
		out["plan_id"] = r.PlanID
	}
	if r.ProfileID != nil {
		out["profile_id"] = *r.ProfileID
	}
	usage := map[string]any{}
	if r.RequestID != nil {
		usage["request_id"] = *r.RequestID
	}
	if r.InputTokens != nil {
		usage["input_tokens"] = *r.InputTokens
	}
	if r.OutputTokens != nil {
		usage["output_tokens"] = *r.OutputTokens
	}
	if r.TotalTokens != nil {
		usage["total_tokens"] = *r.TotalTokens
	}
	if len(usage) > 0 {
		out["usage"] = usage
	}
	if r.ErrorCode != nil || r.ErrorMessage != nil {
		errObj := map[string]any{}
		if r.ErrorCode != nil {
			errObj["code"] = *r.ErrorCode
		}
		if r.ErrorMessage != nil {
			errObj["message"] = *r.ErrorMessage
		}
		out["error"] = errObj
	}
	contextObj := map[string]any{}
	addIntPtr(contextObj, "tokens_before", r.ContextTokensBefore)
	addIntPtr(contextObj, "tokens_after", r.ContextTokensAfter)
	addIntPtr(contextObj, "summary_buckets", r.ContextSummaryBuckets)
	addIntPtr(contextObj, "summary_rounds", r.ContextSummaryRounds)
	addIntPtr(contextObj, "artifact_refs_loaded", r.ContextArtifactRefsLoaded)
	addBoolPtr(contextObj, "need_retrieval", r.ContextNeedRetrieval)
	addBoolPtr(contextObj, "need_retrieval_tooling", r.ContextNeedTooling)
	addBoolPtr(contextObj, "hard_capped", r.ContextHardCapped)
	addBoolPtr(contextObj, "overflow_compacted", r.ContextOverflowCompacted)
	if len(r.ContextPlanJSON) > 0 {
		var plan any
		if err := json.Unmarshal(r.ContextPlanJSON, &plan); err == nil {
			contextObj["plan"] = plan
		}
	}
	if len(contextObj) > 0 {
		out["context"] = contextObj
	}
	if r.StartedAt != nil {
		out["started_at"] = r.StartedAt.UnixMilli()
	}
	if r.EndedAt != nil {
		out["ended_at"] = r.EndedAt.UnixMilli()
	}
	return out
}

func publicMessageDTO(m store.Message) map[string]any {
	out := map[string]any{
		"object":     "message",
		"id":         m.ID,
		"thread_id":  m.ThreadID,
		"seq":        m.Seq,
		"role":       string(m.Role),
		"content":    nil,
		"created_at": m.CreatedAt.UnixMilli(),
		"updated_at": m.UpdatedAt.UnixMilli(),
	}
	if m.RunID != nil {
		out["run_id"] = *m.RunID
	}
	if m.Content != nil {
		out["content"] = *m.Content
	}
	if len(m.ToolCalls) > 0 {
		out["tool_calls"] = m.ToolCalls
	}
	if m.ToolCallID != nil {
		out["tool_call_id"] = *m.ToolCallID
	}
	if m.Name != nil {
		out["name"] = *m.Name
	}
	if len(m.ReferencedArtifactIDs) > 0 {
		out["referenced_artifact_ids"] = append([]string(nil), m.ReferencedArtifactIDs...)
	}
	if len(m.ReferencedMessageIDs) > 0 {
		out["referenced_message_ids"] = append([]string(nil), m.ReferencedMessageIDs...)
	}
	if len(m.ReferencedThreadIDs) > 0 {
		out["referenced_thread_ids"] = append([]string(nil), m.ReferencedThreadIDs...)
	}
	if m.Role == types.RoleTool && m.Content != nil {
		if tr, ok := parseToolResultEnvelope(*m.Content); ok {
			out["tool_result"] = tr
		}
	}
	return out
}

func publicRunResultMessageDTO(m store.Message) map[string]any {
	out := publicMessageDTO(m)
	if m.Role != types.RoleTool || m.Content == nil {
		return out
	}
	env, ok := parseToolResultEnvelope(*m.Content)
	if !ok {
		content, truncated := truncateStringByBytes(*m.Content, maxRunResultToolResultBytes)
		out["content"] = content
		if truncated {
			out["content_truncated"] = true
			out["content_bytes"] = len(*m.Content)
		}
		return out
	}
	bounded, truncated := boundedToolResultEnvelope(env, maxRunResultToolResultBytes)
	out["tool_result"] = bounded
	if b, err := json.Marshal(bounded); err == nil {
		out["content"] = string(b)
		if truncated || len(b) < len(*m.Content) {
			out["content_truncated"] = true
			out["content_bytes"] = len(*m.Content)
		}
	}
	return out
}

func publicMessageFromMap(raw map[string]any) map[string]any {
	out := map[string]any{
		"object":  "message",
		"role":    strings.TrimSpace(stringValue(raw["role"])),
		"content": nil,
	}
	copyStringField(out, raw, "id")
	copyNumberField(out, raw, "seq")
	copyStringField(out, raw, "thread_id")
	copyStringField(out, raw, "run_id")
	copyStringField(out, raw, "tool_call_id")
	copyStringField(out, raw, "name")
	if content, ok := raw["content"].(string); ok {
		out["content"] = content
		if out["role"] == string(types.RoleTool) {
			if tr, ok := parseToolResultEnvelope(content); ok {
				out["tool_result"] = tr
			}
		}
	}
	if calls, ok := raw["tool_calls"]; ok {
		out["tool_calls"] = calls
	}
	return out
}

func parseToolResultEnvelope(content string) (map[string]any, bool) {
	var env map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &env); err != nil {
		return nil, false
	}
	if strings.TrimSpace(stringValue(env["artifact_id"])) == "" {
		return nil, false
	}
	if strings.TrimSpace(stringValue(env["result_format"])) == "" {
		env["result_format"] = "text"
	}
	if suppress, ok := env["ui_suppress"].(bool); ok {
		display := map[string]any{"suppress": suppress}
		env["display"] = display
		delete(env, "ui_suppress")
	}
	return env, true
}

func boundedToolResultEnvelope(env map[string]any, maxBytes int) (map[string]any, bool) {
	out := cloneAnyMap(env)
	if jsonLen(out) <= maxBytes {
		return out, false
	}
	originalBytes := jsonLen(out)
	if result, ok := out["result"]; ok {
		preview, resultBytes, resultTruncated := jsonValuePreview(result, maxBytes/2)
		delete(out, "result")
		out["result_preview"] = preview
		out["result_bytes"] = resultBytes
		out["result_truncated"] = resultTruncated || resultBytes > len(preview)
	}
	if jsonLen(out) <= maxBytes {
		out["preview_only"] = true
		out["response_truncated"] = true
		out["response_bytes"] = originalBytes
		if jsonLen(out) <= maxBytes {
			return out, true
		}
	}

	compact := map[string]any{}
	for _, key := range []string{
		"artifact_id",
		"tool_name",
		"tool_call_id",
		"ok",
		"result_format",
		"display",
		"status",
		"error",
		"preview_only",
		"result_preview",
		"result_bytes",
		"result_truncated",
	} {
		if value, ok := out[key]; ok {
			compact[key] = value
		}
	}
	if value, ok := env["query_metadata"]; ok {
		compact["query_metadata"] = boundedJSONValue(value, maxRunResultMetadataBytes)
	}
	if value, ok := env["result_metadata"]; ok {
		compact["result_metadata"] = boundedJSONValue(value, maxRunResultMetadataBytes)
	}
	if _, ok := compact["result_preview"]; !ok {
		if result, ok := env["result"]; ok {
			preview, resultBytes, resultTruncated := jsonValuePreview(result, maxBytes/2)
			compact["result_preview"] = preview
			compact["result_bytes"] = resultBytes
			compact["result_truncated"] = resultTruncated || resultBytes > len(preview)
		}
	}
	if _, ok := compact["result_preview"]; !ok {
		preview, _, _ := jsonValuePreview(env, maxBytes/2)
		compact["content_preview"] = preview
	}
	compact["preview_only"] = true
	compact["response_truncated"] = true
	if originalBytes > 0 {
		compact["response_bytes"] = originalBytes
	}
	return compact, true
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func jsonLen(value any) int {
	b, err := json.Marshal(value)
	if err != nil {
		return maxRunResultToolResultBytes + 1
	}
	return len(b)
}

func jsonValuePreview(value any, maxBytes int) (string, int, bool) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", 0, false
	}
	preview, truncated := truncateStringByBytes(string(b), maxBytes)
	return preview, len(b), truncated
}

func boundedJSONValue(value any, maxBytes int) any {
	if jsonLen(value) <= maxBytes {
		return value
	}
	preview, size, truncated := jsonValuePreview(value, maxBytes)
	return map[string]any{
		"preview":   preview,
		"bytes":     size,
		"truncated": truncated || size > len(preview),
	}
}

func publicArtifactDTO(a store.Artifact, content string) map[string]any {
	out := map[string]any{
		"object":     "artifact",
		"id":         a.ID,
		"thread_id":  a.ThreadID,
		"type":       a.Type,
		"title":      a.Title,
		"content":    content,
		"metadata":   a.Metadata,
		"created_at": a.CreatedAt.UnixMilli(),
	}
	if a.MessageID != nil {
		out["message_id"] = *a.MessageID
	}
	if a.Language != nil {
		out["language"] = *a.Language
	}
	if a.ToolName != nil {
		out["tool_name"] = *a.ToolName
	}
	if a.ToolCallID != nil {
		out["tool_call_id"] = *a.ToolCallID
	}
	if a.OK != nil {
		out["ok"] = *a.OK
	}
	return out
}

func publicArtifactPreviewDTO(a store.Artifact, content string, truncated bool) map[string]any {
	out := publicArtifactDTO(a, content)
	out["byte_size"] = a.ByteSize
	out["content_preview_bytes"] = len(content)
	out["content_truncated"] = truncated
	if truncated {
		out["preview_only"] = true
	}
	return out
}

func addIntPtr(out map[string]any, key string, value *int) {
	if value != nil {
		out[key] = *value
	}
}

func addBoolPtr(out map[string]any, key string, value *bool) {
	if value != nil {
		out[key] = *value
	}
}

func copyStringField(out map[string]any, raw map[string]any, key string) {
	if value := strings.TrimSpace(stringValue(raw[key])); value != "" {
		out[key] = value
	}
}

func copyNumberField(out map[string]any, raw map[string]any, key string) {
	if value, ok := raw[key]; ok {
		out[key] = value
	}
}

func stringValue(v any) string {
	switch s := v.(type) {
	case string:
		return s
	default:
		return ""
	}
}
