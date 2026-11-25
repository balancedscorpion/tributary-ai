package threadtransfer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	BundleType          = "ai_analyst_thread_bundle"
	BundleSchemaVersion = 1
	BundleMimeType      = "application/vnd.ai-analyst.thread+json"
	BundleFileExt       = ".ai-thread.json"

	maxExportMessages       = 200000
	maxExportArtifacts      = 50000
	importArtifactInlineMax = 512 << 10
)

type Bundle struct {
	BundleType    string           `json:"bundle_type"`
	SchemaVersion int              `json:"schema_version"`
	ExportedAt    time.Time        `json:"exported_at"`
	Thread        BundleThread     `json:"thread"`
	Messages      []BundleMessage  `json:"messages"`
	Artifacts     []BundleArtifact `json:"artifacts"`
	Summary       BundleSummary    `json:"summary"`
}

type BundleThread struct {
	ID            string     `json:"id"`
	Title         string     `json:"title"`
	Summary       *string    `json:"summary,omitempty"`
	Status        string     `json:"status,omitempty"`
	ProjectID     *string    `json:"project_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
}

type BundleMessage struct {
	ID                    string           `json:"id"`
	RunID                 *string          `json:"run_id,omitempty"`
	Seq                   int64            `json:"seq"`
	Role                  types.Role       `json:"role"`
	Content               *string          `json:"content,omitempty"`
	ProviderPayload       json.RawMessage  `json:"provider_payload,omitempty"`
	ReferencedMessageIDs  []string         `json:"referenced_message_ids,omitempty"`
	ReferencedThreadIDs   []string         `json:"referenced_thread_ids,omitempty"`
	ReferencedArtifactIDs []string         `json:"referenced_artifact_ids,omitempty"`
	ContextSummary        string           `json:"context_summary,omitempty"`
	ContextPlan           json.RawMessage  `json:"context_plan,omitempty"`
	HiddenContext         *string          `json:"hidden_context,omitempty"`
	Thinking              *string          `json:"thinking,omitempty"`
	ThinkingComplete      bool             `json:"thinking_complete,omitempty"`
	ToolCalls             []types.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID            *string          `json:"tool_call_id,omitempty"`
	Name                  *string          `json:"name,omitempty"`
	UIHidden              bool             `json:"ui_hidden,omitempty"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

type BundleArtifact struct {
	ID              string         `json:"id"`
	MessageID       *string        `json:"message_id,omitempty"`
	Type            string         `json:"type"`
	Title           string         `json:"title"`
	Language        *string        `json:"language,omitempty"`
	ToolName        *string        `json:"tool_name,omitempty"`
	ToolCallID      *string        `json:"tool_call_id,omitempty"`
	OK              *bool          `json:"ok,omitempty"`
	ByteSize        int64          `json:"byte_size"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	CreatedAt       time.Time      `json:"created_at"`
	ContentEncoding string         `json:"content_encoding"`
	Content         string         `json:"content"`
}

type BundleSummary struct {
	MessageCount  int    `json:"message_count"`
	ArtifactCount int    `json:"artifact_count"`
	FileName      string `json:"file_name,omitempty"`
}

type ImportResult struct {
	Thread        store.Thread `json:"thread"`
	MessageCount  int          `json:"message_count"`
	ArtifactCount int          `json:"artifact_count"`
}

func ExportThread(ctx context.Context, st store.Store, cfgDir string, threadID string) (Bundle, error) {
	if st == nil {
		return Bundle{}, errors.New("store is required")
	}
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return Bundle{}, errors.New("threadId is required")
	}
	thread, err := st.GetThread(ctx, tid)
	if err != nil {
		return Bundle{}, err
	}
	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID:      tid,
		Limit:         maxExportMessages,
		IncludeHidden: false,
		OrderBy:       "seq_asc",
	})
	if err != nil {
		return Bundle{}, err
	}
	arts, err := st.ListArtifacts(ctx, tid, maxExportArtifacts)
	if err != nil {
		return Bundle{}, err
	}
	includedMessageIDs := make(map[string]struct{}, len(msgs))
	referencedArtifactIDs := make(map[string]struct{})
	for _, msg := range msgs {
		msgID := strings.TrimSpace(msg.ID)
		if msgID != "" {
			includedMessageIDs[msgID] = struct{}{}
		}
		for _, artifactID := range msg.ReferencedArtifactIDs {
			artifactID = strings.TrimSpace(artifactID)
			if artifactID == "" {
				continue
			}
			referencedArtifactIDs[artifactID] = struct{}{}
		}
	}
	shareableArtifacts := make([]store.Artifact, 0, len(arts))
	for _, art := range arts {
		if !shouldExportArtifact(art, includedMessageIDs, referencedArtifactIDs) {
			continue
		}
		shareableArtifacts = append(shareableArtifacts, art)
	}
	sort.Slice(shareableArtifacts, func(i, j int) bool {
		if shareableArtifacts[i].CreatedAt.Equal(shareableArtifacts[j].CreatedAt) {
			return shareableArtifacts[i].ID < shareableArtifacts[j].ID
		}
		return shareableArtifacts[i].CreatedAt.Before(shareableArtifacts[j].CreatedAt)
	})

	bundle := Bundle{
		BundleType:    BundleType,
		SchemaVersion: BundleSchemaVersion,
		ExportedAt:    time.Now().UTC(),
		Thread: BundleThread{
			ID:            thread.ID,
			Title:         thread.Title,
			Summary:       cloneStringPtr(thread.Summary),
			Status:        strings.TrimSpace(thread.Status),
			ProjectID:     cloneStringPtr(thread.ProjectID),
			CreatedAt:     thread.CreatedAt,
			UpdatedAt:     thread.UpdatedAt,
			LastMessageAt: cloneTimePtr(thread.LastMessageAt),
		},
		Messages:  make([]BundleMessage, 0, len(msgs)),
		Artifacts: make([]BundleArtifact, 0, len(shareableArtifacts)),
	}

	for _, msg := range msgs {
		bundle.Messages = append(bundle.Messages, BundleMessage{
			ID:                    strings.TrimSpace(msg.ID),
			RunID:                 nil,
			Seq:                   msg.Seq,
			Role:                  msg.Role,
			Content:               cloneStringPtr(msg.Content),
			ProviderPayload:       cloneRawJSON(msg.ProviderPayload),
			ReferencedMessageIDs:  append([]string(nil), msg.ReferencedMessageIDs...),
			ReferencedThreadIDs:   append([]string(nil), msg.ReferencedThreadIDs...),
			ReferencedArtifactIDs: append([]string(nil), msg.ReferencedArtifactIDs...),
			ContextSummary:        strings.TrimSpace(msg.ContextSummary),
			ContextPlan:           nil,
			HiddenContext:         nil,
			Thinking:              nil,
			ThinkingComplete:      false,
			ToolCalls:             cloneToolCalls(msg.ToolCalls),
			ToolCallID:            cloneStringPtr(msg.ToolCallID),
			Name:                  cloneStringPtr(msg.Name),
			UIHidden:              false,
			CreatedAt:             msg.CreatedAt,
			UpdatedAt:             msg.UpdatedAt,
		})
	}

	for _, art := range shareableArtifacts {
		raw, err := readArtifactBytes(cfgDir, art)
		if err != nil {
			return Bundle{}, err
		}
		encoding := "utf8"
		content := string(raw)
		if !utf8.Valid(raw) {
			encoding = "base64"
			content = base64.StdEncoding.EncodeToString(raw)
		}
		bundle.Artifacts = append(bundle.Artifacts, BundleArtifact{
			ID:              strings.TrimSpace(art.ID),
			MessageID:       cloneStringPtr(art.MessageID),
			Type:            strings.TrimSpace(art.Type),
			Title:           art.Title,
			Language:        cloneStringPtr(art.Language),
			ToolName:        cloneStringPtr(art.ToolName),
			ToolCallID:      cloneStringPtr(art.ToolCallID),
			OK:              cloneBoolPtr(art.OK),
			ByteSize:        art.ByteSize,
			Metadata:        cloneMap(art.Metadata),
			CreatedAt:       art.CreatedAt,
			ContentEncoding: encoding,
			Content:         content,
		})
	}

	fileName := ExportFileName(bundle.Thread.Title, bundle.Thread.ID)
	bundle.Summary = BundleSummary{
		MessageCount:  len(bundle.Messages),
		ArtifactCount: len(bundle.Artifacts),
		FileName:      fileName,
	}
	return bundle, nil
}

func ExportThreadFile(ctx context.Context, st store.Store, cfgDir string, threadID string) (string, []byte, BundleSummary, error) {
	bundle, err := ExportThread(ctx, st, cfgDir, threadID)
	if err != nil {
		return "", nil, BundleSummary{}, err
	}
	payload, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return "", nil, BundleSummary{}, err
	}
	return bundle.Summary.FileName, payload, bundle.Summary, nil
}

func ImportThread(ctx context.Context, st store.Store, cfgDir string, payload []byte) (ImportResult, error) {
	if st == nil {
		return ImportResult{}, errors.New("store is required")
	}
	bundle, err := ParseBundle(payload)
	if err != nil {
		return ImportResult{}, err
	}

	projectID := resolveImportProject(ctx, st, bundle.Thread.ProjectID)
	thread, err := st.CreateThread(ctx, projectID)
	if err != nil {
		return ImportResult{}, err
	}
	if title := strings.TrimSpace(bundle.Thread.Title); title != "" || bundle.Thread.Summary != nil {
		titlePtr := stringPtrOrNil(title)
		if err := st.UpdateThreadTitleSummary(ctx, thread.ID, titlePtr, cloneStringPtr(bundle.Thread.Summary)); err != nil {
			return ImportResult{}, err
		}
		thread, err = st.GetThread(ctx, thread.ID)
		if err != nil {
			return ImportResult{}, err
		}
	}

	messageIDMap := make(map[string]string, len(bundle.Messages))
	for _, msg := range bundle.Messages {
		oldID := strings.TrimSpace(msg.ID)
		if oldID == "" {
			continue
		}
		messageIDMap[oldID] = types.NewUUID()
	}
	artifactIDMap := make(map[string]string, len(bundle.Artifacts))
	for _, art := range bundle.Artifacts {
		oldID := strings.TrimSpace(art.ID)
		if oldID == "" {
			continue
		}
		artifactIDMap[oldID] = types.NewUUID()
	}

	messages := append([]BundleMessage(nil), bundle.Messages...)
	sort.Slice(messages, func(i, j int) bool {
		if messages[i].Seq == messages[j].Seq {
			if messages[i].CreatedAt.Equal(messages[j].CreatedAt) {
				return messages[i].ID < messages[j].ID
			}
			return messages[i].CreatedAt.Before(messages[j].CreatedAt)
		}
		return messages[i].Seq < messages[j].Seq
	})
	for _, msg := range messages {
		newID := messageIDMap[strings.TrimSpace(msg.ID)]
		if newID == "" {
			newID = types.NewUUID()
		}
		toolCalls := cloneToolCalls(msg.ToolCalls)
		for i := range toolCalls {
			toolCalls[i].Args = rewriteMapIDs(toolCalls[i].Args, thread.ID, artifactIDMap, messageIDMap)
			toolCalls[i].ArgsText = rewriteJSONText(toolCalls[i].ArgsText, thread.ID, artifactIDMap, messageIDMap)
		}
		content := cloneStringPtr(msg.Content)
		if msg.Role == types.RoleTool {
			content = rewriteToolEnvelopeContent(content, thread.ID, artifactIDMap, messageIDMap)
		}
		_, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
			ID:                    newID,
			Role:                  msg.Role,
			Content:               content,
			ProviderPayload:       cloneRawJSON(msg.ProviderPayload),
			ReferencedMessageIDs:  rewriteStringSlice(msg.ReferencedMessageIDs, messageIDMap),
			ReferencedThreadIDs:   append([]string(nil), msg.ReferencedThreadIDs...),
			ReferencedArtifactIDs: rewriteStringSlice(msg.ReferencedArtifactIDs, artifactIDMap),
			ContextSummary:        strings.TrimSpace(msg.ContextSummary),
			ContextPlan:           nil,
			HiddenContext:         nil,
			Thinking:              nil,
			ThinkingComplete:      false,
			ToolCalls:             toolCalls,
			ToolCallID:            cloneStringPtr(msg.ToolCallID),
			Name:                  cloneStringPtr(msg.Name),
			UIHidden:              false,
		})
		if err != nil {
			return ImportResult{}, err
		}
	}

	artifacts := append([]BundleArtifact(nil), bundle.Artifacts...)
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].CreatedAt.Equal(artifacts[j].CreatedAt) {
			return artifacts[i].ID < artifacts[j].ID
		}
		return artifacts[i].CreatedAt.Before(artifacts[j].CreatedAt)
	})
	for _, art := range artifacts {
		raw, err := decodeArtifactContent(art)
		if err != nil {
			return ImportResult{}, err
		}
		newID := artifactIDMap[strings.TrimSpace(art.ID)]
		if newID == "" {
			newID = types.NewUUID()
		}
		metadata := rewriteMapIDs(art.Metadata, thread.ID, artifactIDMap, messageIDMap)
		storage, contentText, contentPath, err := persistImportedArtifact(cfgDir, newID, strings.TrimSpace(art.Type), raw)
		if err != nil {
			return ImportResult{}, err
		}
		var messageID *string
		if art.MessageID != nil {
			if mapped := strings.TrimSpace(messageIDMap[strings.TrimSpace(*art.MessageID)]); mapped != "" {
				messageID = &mapped
			}
		}
		_, err = st.InsertArtifact(ctx, store.Artifact{
			ID:          newID,
			ThreadID:    thread.ID,
			MessageID:   messageID,
			Type:        strings.TrimSpace(art.Type),
			Title:       strings.TrimSpace(art.Title),
			Language:    cloneStringPtr(art.Language),
			ToolName:    cloneStringPtr(art.ToolName),
			ToolCallID:  cloneStringPtr(art.ToolCallID),
			OK:          cloneBoolPtr(art.OK),
			Storage:     storage,
			ContentText: contentText,
			ContentPath: contentPath,
			ByteSize:    int64(len(raw)),
			Metadata:    metadata,
		})
		if err != nil {
			return ImportResult{}, err
		}
	}

	thread, err = st.GetThread(ctx, thread.ID)
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{
		Thread:        thread,
		MessageCount:  len(bundle.Messages),
		ArtifactCount: len(bundle.Artifacts),
	}, nil
}

func ParseBundle(payload []byte) (Bundle, error) {
	var bundle Bundle
	dec := json.NewDecoder(bytes.NewReader(payload))
	if err := dec.Decode(&bundle); err != nil {
		return Bundle{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != nil {
		if !errors.Is(err, io.EOF) {
			return Bundle{}, err
		}
	} else {
		return Bundle{}, errors.New("unexpected trailing JSON content")
	}
	if strings.TrimSpace(bundle.BundleType) != BundleType {
		return Bundle{}, errors.New("invalid thread bundle type")
	}
	if bundle.SchemaVersion != BundleSchemaVersion {
		return Bundle{}, fmt.Errorf("unsupported thread bundle schema version %d", bundle.SchemaVersion)
	}
	if strings.TrimSpace(bundle.Thread.Title) == "" {
		bundle.Thread.Title = "Imported Thread"
	}
	return bundle, nil
}

func ExportFileName(title string, threadID string) string {
	base := sanitizeFileStem(title)
	if base == "" {
		base = sanitizeFileStem(threadID)
	}
	if base == "" {
		base = "thread-export"
	}
	return base + BundleFileExt
}

func resolveImportProject(ctx context.Context, st store.Store, projectID *string) *string {
	if projectID == nil || strings.TrimSpace(*projectID) == "" {
		return nil
	}
	id := strings.TrimSpace(*projectID)
	if _, err := st.GetProject(ctx, id); err != nil {
		return nil
	}
	return &id
}

func rewriteToolEnvelopeContent(content *string, newThreadID string, artifactIDMap, messageIDMap map[string]string) *string {
	if content == nil {
		return nil
	}
	raw := strings.TrimSpace(*content)
	if raw == "" {
		return content
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return content
	}
	if _, ok := decoded["artifact_id"]; !ok {
		return content
	}
	rewritten := rewriteMapIDs(decoded, newThreadID, artifactIDMap, messageIDMap)
	if rewritten == nil {
		return content
	}
	b, err := json.Marshal(rewritten)
	if err != nil {
		return content
	}
	out := string(b)
	return &out
}

func rewriteRawJSON(raw json.RawMessage, newThreadID string, artifactIDMap, messageIDMap map[string]string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	rewritten := rewriteAnyIDs(decoded, "", newThreadID, artifactIDMap, messageIDMap)
	b, err := json.Marshal(rewritten)
	if err != nil {
		return append(json.RawMessage(nil), raw...)
	}
	return json.RawMessage(b)
}

func rewriteJSONText(raw string, newThreadID string, artifactIDMap, messageIDMap map[string]string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return raw
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return raw
	}
	rewritten := rewriteAnyIDs(decoded, "", newThreadID, artifactIDMap, messageIDMap)
	b, err := json.Marshal(rewritten)
	if err != nil {
		return raw
	}
	return string(b)
}

func rewriteMapIDs(in map[string]any, newThreadID string, artifactIDMap, messageIDMap map[string]string) map[string]any {
	if len(in) == 0 {
		return nil
	}
	rewritten, ok := rewriteAnyIDs(in, "", newThreadID, artifactIDMap, messageIDMap).(map[string]any)
	if !ok {
		return cloneMap(in)
	}
	return rewritten
}

func rewriteAnyIDs(value any, key string, newThreadID string, artifactIDMap, messageIDMap map[string]string) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for childKey, childValue := range typed {
			out[childKey] = rewriteAnyIDs(childValue, childKey, newThreadID, artifactIDMap, messageIDMap)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, rewriteAnyIDs(item, key, newThreadID, artifactIDMap, messageIDMap))
		}
		return out
	case []string:
		switch normalizeIDKey(key) {
		case "thread":
			return rewriteThreadStringSlice(typed, newThreadID)
		case "artifact":
			return rewriteStringSlice(typed, artifactIDMap)
		case "message":
			return rewriteStringSlice(typed, messageIDMap)
		default:
			return rewriteStringSlice(typed, nil)
		}
	case string:
		switch normalizeIDKey(key) {
		case "thread":
			if strings.TrimSpace(newThreadID) != "" {
				return newThreadID
			}
		case "artifact":
			if mapped := strings.TrimSpace(artifactIDMap[strings.TrimSpace(typed)]); mapped != "" {
				return mapped
			}
		case "message":
			if mapped := strings.TrimSpace(messageIDMap[strings.TrimSpace(typed)]); mapped != "" {
				return mapped
			}
		}
		return typed
	default:
		return value
	}
}

func shouldExportArtifact(artifact store.Artifact, includedMessageIDs map[string]struct{}, referencedArtifactIDs map[string]struct{}) bool {
	artifactID := strings.TrimSpace(artifact.ID)
	if artifactID == "" {
		return false
	}
	if _, ok := referencedArtifactIDs[artifactID]; ok {
		return true
	}
	if artifact.MessageID != nil {
		if _, ok := includedMessageIDs[strings.TrimSpace(*artifact.MessageID)]; ok {
			return true
		}
	}
	if artifact.ToolName != nil && strings.EqualFold(strings.TrimSpace(*artifact.ToolName), types.ThreadFileAttachmentToolName) {
		return true
	}
	return fileartifact.IsFileAttachmentArtifact(artifact)
}

func rewriteStringSlice(in []string, mapping map[string]string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if mapping != nil {
			if mapped := strings.TrimSpace(mapping[v]); mapped != "" {
				v = mapped
			}
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func rewriteThreadStringSlice(in []string, newThreadID string) []string {
	if len(in) == 0 {
		return nil
	}
	fallback := rewriteStringSlice(in, nil)
	if strings.TrimSpace(newThreadID) == "" {
		return fallback
	}
	out := make([]string, 0, len(fallback))
	for range fallback {
		out = append(out, newThreadID)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeIDKey(key string) string {
	switch strings.TrimSpace(strings.ToLower(key)) {
	case "thread_id", "threadid", "thread_ids", "threadids", "handover_thread_id":
		return "thread"
	case "artifact_id", "artifactid", "artifact_ids", "artifactids", "full_artifact_id", "fullartifactid", "candidate_artifact_ids", "referenced_artifact_ids":
		return "artifact"
	case "message_id", "messageid", "message_ids", "messageids", "candidate_message_ids", "referenced_message_ids":
		return "message"
	default:
		return ""
	}
}

func decodeArtifactContent(artifact BundleArtifact) ([]byte, error) {
	switch strings.TrimSpace(strings.ToLower(artifact.ContentEncoding)) {
	case "", "utf8", "text":
		return []byte(artifact.Content), nil
	case "base64":
		return base64.StdEncoding.DecodeString(strings.TrimSpace(artifact.Content))
	default:
		return nil, fmt.Errorf("unsupported artifact content encoding %q", artifact.ContentEncoding)
	}
}

func persistImportedArtifact(cfgDir string, artifactID string, artifactType string, raw []byte) (string, *string, *string, error) {
	if len(raw) <= importArtifactInlineMax && utf8.Valid(raw) {
		text := string(raw)
		return "mysql", &text, nil, nil
	}
	relPath, err := writeArtifactFile(cfgDir, artifactID, artifactType, raw)
	if err != nil {
		return "", nil, nil, err
	}
	return "file", nil, &relPath, nil
}

func writeArtifactFile(cfgDir string, artifactID string, artifactType string, raw []byte) (string, error) {
	dir := filepath.Join(cfgDir, "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ext := ".txt"
	switch strings.TrimSpace(strings.ToLower(artifactType)) {
	case "data":
		ext = ".json"
	case "image":
		ext = ".img"
	case "file":
		ext = ".bin"
	}
	fileName := artifactID + ext
	fullPath := filepath.Join(dir, fileName)
	if err := os.WriteFile(fullPath, raw, 0o600); err != nil {
		return "", err
	}
	return filepath.ToSlash(filepath.Join("artifacts", fileName)), nil
}

func readArtifactBytes(cfgDir string, artifact store.Artifact) ([]byte, error) {
	switch strings.TrimSpace(strings.ToLower(artifact.Storage)) {
	case "file":
		if artifact.ContentPath == nil || strings.TrimSpace(*artifact.ContentPath) == "" {
			return nil, nil
		}
		rel, err := cleanRelPath(*artifact.ContentPath)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(filepath.Join(cfgDir, filepath.FromSlash(rel)))
	default:
		if artifact.ContentText == nil {
			return nil, nil
		}
		return []byte(*artifact.ContentText), nil
	}
}

func cleanRelPath(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	clean := path.Clean(value)
	if clean == "." || clean == "" {
		return "", errors.New("invalid relative path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid path traversal")
	}
	return clean, nil
}

func sanitizeFileStem(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
		"*", "",
		"?", "",
		"\"", "",
		"<", "",
		">", "",
		"|", "",
	)
	value = replacer.Replace(value)
	value = strings.Join(strings.Fields(value), "-")
	value = strings.Trim(value, ".- _")
	if value == "" {
		return ""
	}
	if len(value) > 96 {
		value = strings.Trim(value[:96], ".- _")
	}
	return value
}

func cloneRawJSON(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

func cloneStringPtr(in *string) *string {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneTimePtr(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneBoolPtr(in *bool) *bool {
	if in == nil {
		return nil
	}
	value := *in
	return &value
}

func cloneToolCalls(in []types.ToolCall) []types.ToolCall {
	if len(in) == 0 {
		return nil
	}
	out := make([]types.ToolCall, 0, len(in))
	for _, call := range in {
		cloned := call
		cloned.Args = cloneMap(call.Args)
		out = append(out, cloned)
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func stringPtrOrNil(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	out := strings.TrimSpace(value)
	return &out
}

func firstStringMapValue(mm map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := mm[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			return typed
		case fmt.Stringer:
			return typed.String()
		}
	}
	return ""
}
