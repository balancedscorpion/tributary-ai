package search

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
	"ai-analyst-local/internal/chat/jsondb"
	"ai-analyst-local/internal/chat/search/index"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	maxThreadFetch   = 200000
	maxMessageFetch  = 200000
	maxRunFetch      = 50000
	maxArtifactFetch = 50000
	maxArtifactBytes = 64 << 10
)

type Query struct {
	Text        string
	Limit       int
	Status      string
	ProjectID   string
	ThreadIDs   []string
	ArtifactIDs []string
	MessageIDs  []string
	BeforeTS    *time.Time
	AfterTS     *time.Time
	MinScore    float64
	OrderBy     string
	Intent      string
	Depth       string
	MaxChars    int
}

type Intent struct {
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence"`
}

type Result struct {
	DocID      string  `json:"doc_id"`
	DocKind    string  `json:"doc_kind"`
	ThreadID   string  `json:"thread_id"`
	RunID      *string `json:"run_id,omitempty"`
	MessageID  *string `json:"message_id,omitempty"`
	ArtifactID *string `json:"artifact_id,omitempty"`
	Title      string  `json:"title"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
	UpdatedAt  int64   `json:"updated_at"`
}

type Service struct {
	cfgDir  string
	docPath string
	base    store.Store
	idx     *index.Index

	mu   sync.RWMutex
	docs map[string]jsondb.SearchDoc
}

func NewService(cfgDir string, base store.Store) (*Service, error) {
	if strings.TrimSpace(cfgDir) == "" {
		return nil, errors.New("cfgDir is required")
	}
	if base == nil {
		return nil, errors.New("store is required")
	}
	dir := jsondb.IndexesDir(cfgDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Service{
		cfgDir:  cfgDir,
		docPath: filepath.Join(dir, "documents.jsonl"),
		base:    base,
		idx:     index.New(),
		docs:    map[string]jsondb.SearchDoc{},
	}
	if err := s.loadDocsFromDisk(); err != nil {
		return nil, err
	}
	if len(s.docs) == 0 {
		if err := s.Rebuild(context.Background()); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Service) Search(ctx context.Context, q Query) (Intent, []Result, error) {
	intent := classifyIntent(q.Text, q.Intent)
	// If forced keyword mode, do not reinterpret.
	query := index.Query{
		Text:        q.Text,
		Limit:       q.Limit,
		Status:      q.Status,
		ProjectID:   q.ProjectID,
		ThreadIDs:   q.ThreadIDs,
		ArtifactIDs: q.ArtifactIDs,
		MessageIDs:  q.MessageIDs,
		From:        q.AfterTS,
		To:          q.BeforeTS,
		OrderBy:     q.OrderBy,
	}
	raw := s.idx.Search(query)
	out := make([]Result, 0, len(raw))
	for _, hit := range raw {
		select {
		case <-ctx.Done():
			return intent, nil, ctx.Err()
		default:
		}
		if q.MinScore > 0 && hit.Score < q.MinScore {
			continue
		}
		snippet := hit.Snippet
		if q.MaxChars > 0 && len(snippet) > q.MaxChars {
			snippet = snippet[:q.MaxChars]
		}
		out = append(out, Result{
			DocID:      hit.Doc.ID,
			DocKind:    hit.Doc.DocKind,
			ThreadID:   hit.Doc.ThreadID,
			RunID:      hit.Doc.RunID,
			MessageID:  hit.Doc.MessageID,
			ArtifactID: hit.Doc.ArtifactID,
			Title:      hit.Doc.Title,
			Snippet:    snippet,
			Score:      hit.Score,
			UpdatedAt:  hit.Doc.UpdatedAt.UnixMilli(),
		})
	}
	return intent, out, nil
}

func (s *Service) Suggest(text string, limit int) []jsondb.SearchDoc {
	return s.idx.Suggest(text, limit)
}

func (s *Service) GetDoc(docID string) (jsondb.SearchDoc, bool) {
	id := strings.TrimSpace(docID)
	if id == "" {
		return jsondb.SearchDoc{}, false
	}
	return s.idx.Get(id)
}

func (s *Service) Rebuild(ctx context.Context) error {
	threads, err := s.listAllThreads(ctx)
	if err != nil {
		return err
	}
	next := make(map[string]jsondb.SearchDoc, len(threads)*3)
	for _, thread := range threads {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		docs, err := s.buildDocsForThread(ctx, thread)
		if err != nil {
			return err
		}
		for _, d := range docs {
			next[d.ID] = d
		}
	}
	all := mapToSlice(next)
	s.mu.Lock()
	s.docs = next
	s.idx.ReplaceAll(all)
	err = s.persistDocsLocked()
	s.mu.Unlock()
	return err
}

func (s *Service) SyncThread(ctx context.Context, threadID string) error {
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return errors.New("threadId is required")
	}
	thread, err := s.base.GetThread(ctx, tid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return s.RemoveThread(tid)
		}
		return err
	}
	docs, err := s.buildDocsForThread(ctx, thread)
	if err != nil {
		return err
	}
	s.mu.Lock()
	for id, existing := range s.docs {
		if existing.ThreadID != tid {
			continue
		}
		delete(s.docs, id)
		s.idx.Delete(id)
	}
	for _, d := range docs {
		s.docs[d.ID] = d
		s.idx.Upsert(d)
	}
	err = s.persistDocsLocked()
	s.mu.Unlock()
	return err
}

func (s *Service) RemoveThread(threadID string) error {
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil
	}
	s.mu.Lock()
	for id, existing := range s.docs {
		if existing.ThreadID != tid {
			continue
		}
		delete(s.docs, id)
		s.idx.Delete(id)
	}
	err := s.persistDocsLocked()
	s.mu.Unlock()
	return err
}

func (s *Service) listAllThreads(ctx context.Context) ([]store.Thread, error) {
	active, err := s.base.ListThreads(ctx, "active", maxThreadFetch)
	if err != nil {
		return nil, err
	}
	archived, err := s.base.ListThreads(ctx, "archived", maxThreadFetch)
	if err != nil {
		return nil, err
	}
	byID := map[string]store.Thread{}
	for _, t := range active {
		byID[t.ID] = t
	}
	for _, t := range archived {
		byID[t.ID] = t
	}
	out := make([]store.Thread, 0, len(byID))
	for _, t := range byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *Service) buildDocsForThread(ctx context.Context, thread store.Thread) ([]jsondb.SearchDoc, error) {
	now := nowUTC()
	status := strings.TrimSpace(thread.Status)
	if status == "" {
		status = "active"
	}
	projectID := trimPtr(thread.ProjectID)
	out := make([]jsondb.SearchDoc, 0, 8)

	threadText := strings.TrimSpace(strings.Join([]string{
		thread.Title,
		stringOrEmpty(thread.Summary),
	}, "\n"))
	threadDoc := jsondb.SearchDoc{
		Kind:          jsondb.KindSearchDoc,
		SchemaVersion: jsondb.SchemaVersion,
		ID:            "thread:" + thread.ID,
		DocKind:       "thread",
		ThreadID:      thread.ID,
		Status:        status,
		ProjectID:     projectID,
		Title:         thread.Title,
		Text:          threadText,
		CreatedAt:     thread.CreatedAt,
		UpdatedAt:     maxTime(thread.UpdatedAt, now),
	}
	out = append(out, threadDoc)

	runs, err := s.base.ListRuns(ctx, thread.ID, maxRunFetch)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	msgs, err := s.base.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    maxMessageFetch,
	})
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	arts, err := s.base.ListArtifacts(ctx, thread.ID, maxArtifactFetch)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	msgByRun := map[string][]store.Message{}
	msgByID := map[string]store.Message{}
	for _, m := range msgs {
		msgByID[m.ID] = m
		if m.RunID != nil && strings.TrimSpace(*m.RunID) != "" {
			rid := strings.TrimSpace(*m.RunID)
			msgByRun[rid] = append(msgByRun[rid], m)
		}
	}
	artTitlesByMessageID := map[string][]string{}
	for _, a := range arts {
		if a.MessageID == nil || strings.TrimSpace(*a.MessageID) == "" {
			continue
		}
		mid := strings.TrimSpace(*a.MessageID)
		artTitlesByMessageID[mid] = append(artTitlesByMessageID[mid], strings.TrimSpace(a.Title))
	}

	for _, run := range runs {
		rid := run.ID
		runMsgs := msgByRun[rid]
		if len(runMsgs) == 0 {
			continue
		}
		sort.Slice(runMsgs, func(i, j int) bool { return runMsgs[i].Seq < runMsgs[j].Seq })

		var userText string
		var assistantText string
		toolNames := map[string]struct{}{}
		artTitles := []string{}
		artSeen := map[string]struct{}{}
		for _, m := range runMsgs {
			if m.Role == "user" && userText == "" {
				userText = strings.TrimSpace(stringOrEmpty(m.Content))
			}
			if m.Role == "assistant" {
				t := strings.TrimSpace(stringOrEmpty(m.Content))
				if t != "" {
					assistantText = t
				}
				for _, call := range m.ToolCalls {
					name := strings.TrimSpace(call.Name)
					if name != "" {
						toolNames[name] = struct{}{}
					}
				}
			}
			if m.Name != nil && strings.TrimSpace(*m.Name) != "" {
				toolNames[strings.TrimSpace(*m.Name)] = struct{}{}
			}
			for _, title := range artTitlesByMessageID[m.ID] {
				if _, ok := artSeen[title]; ok {
					continue
				}
				artSeen[title] = struct{}{}
				artTitles = append(artTitles, title)
			}
		}

		toolList := mapKeys(toolNames)
		sort.Strings(toolList)
		combined := strings.TrimSpace(strings.Join([]string{
			"User: " + userText,
			"Assistant: " + assistantText,
			"Tools: " + strings.Join(toolList, ", "),
			"Artifacts: " + strings.Join(artTitles, ", "),
		}, "\n"))
		doc := jsondb.SearchDoc{
			Kind:          jsondb.KindSearchDoc,
			SchemaVersion: jsondb.SchemaVersion,
			ID:            "turn:" + rid,
			DocKind:       "turn",
			ThreadID:      thread.ID,
			RunID:         &rid,
			Status:        status,
			ProjectID:     projectID,
			Title:         fmt.Sprintf("Turn %s", rid),
			Text:          combined,
			CreatedAt:     run.CreatedAt,
			UpdatedAt:     run.UpdatedAt,
		}
		out = append(out, doc)
	}

	for _, a := range arts {
		content := s.artifactSearchContent(a)
		excerpt := summarizeArtifactContent(content)
		tool := ""
		if a.ToolName != nil {
			tool = strings.TrimSpace(*a.ToolName)
		}
		metaText := metadataSummary(a.Metadata)
		text := strings.TrimSpace(strings.Join([]string{
			"Title: " + a.Title,
			"Type: " + strings.TrimSpace(a.Type),
			"Tool: " + tool,
			metaText,
			"Excerpt:",
			excerpt,
		}, "\n"))
		aid := a.ID
		doc := jsondb.SearchDoc{
			Kind:          jsondb.KindSearchDoc,
			SchemaVersion: jsondb.SchemaVersion,
			ID:            "artifact:" + a.ID,
			DocKind:       "artifact",
			ThreadID:      thread.ID,
			ArtifactID:    &aid,
			MessageID:     trimPtr(a.MessageID),
			Status:        status,
			ProjectID:     projectID,
			Title:         a.Title,
			Text:          text,
			CreatedAt:     a.CreatedAt,
			UpdatedAt:     a.CreatedAt,
		}
		if a.MessageID != nil {
			if msg, ok := msgByID[*a.MessageID]; ok && msg.RunID != nil {
				doc.RunID = trimPtr(msg.RunID)
			}
		}
		out = append(out, doc)
	}
	return out, nil
}

func (s *Service) artifactSearchContent(a store.Artifact) string {
	if content := strings.TrimSpace(stringOrEmpty(a.ContentText)); content != "" {
		return content
	}
	if content := s.artifactExtractionSearchContent(a.Metadata, maxArtifactBytes); content != "" {
		return content
	}
	if preview := strings.TrimSpace(searchMetadataString(a.Metadata, "previewText", "preview_text")); preview != "" {
		return preview
	}
	if isThreadUploadFileArtifact(a) {
		return ""
	}
	if a.ContentPath != nil && strings.TrimSpace(*a.ContentPath) != "" {
		return s.readArtifactExcerpt(strings.TrimSpace(*a.ContentPath), maxArtifactBytes)
	}
	return ""
}

func (s *Service) artifactExtractionSearchContent(meta map[string]any, maxBytes int) string {
	extraction, ok := searchMetadataMap(meta, "extraction")
	if !ok || len(extraction) == 0 || maxBytes <= 0 {
		return ""
	}
	paths := artifactExtractionSearchPaths(extraction)
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	for _, rel := range paths {
		remaining := maxBytes - b.Len()
		if remaining <= 0 {
			break
		}
		content := strings.TrimSpace(s.readArtifactExcerpt(rel, remaining))
		if content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString(content)
	}
	return strings.TrimSpace(b.String())
}

func artifactExtractionSearchPaths(extraction map[string]any) []string {
	kind := strings.TrimSpace(strings.ToLower(searchMetadataString(extraction, "kind")))
	paths := []string{
		searchMetadataString(extraction, "markdownPath", "markdown_path"),
		searchMetadataString(extraction, "textPath", "text_path"),
		searchMetadataString(extraction, "documentPath", "document_path"),
		searchMetadataString(extraction, "contentPath", "content_path"),
		searchMetadataString(extraction, "ocrTextPath", "ocr_text_path"),
	}
	if kind == "spreadsheet" {
		paths = append(paths, artifactSpreadsheetPreviewMarkdownPaths(searchMetadataStringSlice(extraction, "sheetOutputs", "sheet_outputs"))...)
		if outputs, ok := searchMetadataMap(extraction, "outputs"); ok {
			rootPath := searchMetadataString(extraction, "rootPath", "root_path")
			for _, nested := range artifactSpreadsheetPreviewMarkdownPaths(searchMetadataStringSlice(outputs, "sheetOutputs", "sheet_outputs")) {
				paths = append(paths, artifactJoinExtractionRoot(rootPath, nested))
			}
		}
	}
	return compactSearchStrings(paths...)
}

func artifactSpreadsheetPreviewMarkdownPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := strings.TrimSpace(filepath.ToSlash(path))
		if clean != "" && strings.HasSuffix(strings.ToLower(clean), ".md") {
			out = append(out, clean)
		}
	}
	return out
}

func artifactJoinExtractionRoot(rootPath string, relPath string) string {
	rootPath = strings.Trim(strings.TrimSpace(filepath.ToSlash(rootPath)), "/")
	relPath = strings.Trim(strings.TrimSpace(filepath.ToSlash(relPath)), "/")
	switch {
	case relPath == "":
		return ""
	case rootPath == "":
		return relPath
	default:
		return rootPath + "/" + relPath
	}
}

func isThreadUploadFileArtifact(a store.Artifact) bool {
	if !strings.EqualFold(strings.TrimSpace(a.Type), "file") {
		return false
	}
	toolName := ""
	if a.ToolName != nil {
		toolName = strings.TrimSpace(*a.ToolName)
	}
	source := strings.TrimSpace(searchMetadataString(a.Metadata, "source"))
	return strings.EqualFold(toolName, types.ThreadFileAttachmentToolName) ||
		strings.EqualFold(source, types.ChatUploadAttachmentSource)
}

func (s *Service) readArtifactExcerpt(rel string, maxBytes int) string {
	clean := strings.TrimSpace(rel)
	if clean == "" || maxBytes <= 0 {
		return ""
	}
	full := filepath.Join(s.cfgDir, filepath.FromSlash(clean))
	b, err := os.ReadFile(full)
	if err != nil {
		return ""
	}
	if len(b) > maxBytes {
		b = b[:maxBytes]
	}
	return string(b)
}

func summarizeArtifactContent(content string) string {
	text := strings.TrimSpace(content)
	if text == "" {
		return ""
	}
	if len(text) <= 4000 {
		return text
	}
	return text[:4000]
}

func metadataSummary(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

func searchMetadataString(meta map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := meta[key]
		if !ok {
			continue
		}
		if s, ok := raw.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func searchMetadataStringSlice(meta map[string]any, keys ...string) []string {
	for _, key := range keys {
		raw, ok := meta[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case []string:
			return append([]string(nil), v...)
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func searchMetadataMap(meta map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		raw, ok := meta[key]
		if !ok {
			continue
		}
		if m, ok := raw.(map[string]any); ok {
			return m, true
		}
	}
	return nil, false
}

func compactSearchStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		key := filepath.ToSlash(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func (s *Service) loadDocsFromDisk() error {
	if _, err := os.Stat(s.docPath); errors.Is(err, os.ErrNotExist) {
		s.idx.ReplaceAll(nil)
		return nil
	}
	f, err := os.Open(s.docPath)
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	docs := map[string]jsondb.SearchDoc{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var doc jsondb.SearchDoc
		if err := json.Unmarshal([]byte(line), &doc); err != nil {
			return err
		}
		if strings.TrimSpace(doc.ID) == "" {
			continue
		}
		docs[doc.ID] = doc
	}
	if err := sc.Err(); err != nil {
		return err
	}
	all := mapToSlice(docs)
	s.mu.Lock()
	s.docs = docs
	s.idx.ReplaceAll(all)
	s.mu.Unlock()
	return nil
}

func (s *Service) persistDocsLocked() error {
	all := mapToSlice(s.docs)
	sort.Slice(all, func(i, j int) bool {
		if all[i].UpdatedAt.Equal(all[j].UpdatedAt) {
			return all[i].ID < all[j].ID
		}
		return all[i].UpdatedAt.After(all[j].UpdatedAt)
	})
	var b strings.Builder
	for _, doc := range all {
		line, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return atomicfile.WriteFile(s.docPath, []byte(b.String()), 0o600)
}

func classifyIntent(text string, forced string) Intent {
	mode := strings.TrimSpace(strings.ToLower(forced))
	switch mode {
	case "keyword":
		return Intent{Kind: "keyword", Confidence: 1}
	case "question":
		return Intent{Kind: "question", Confidence: 1}
	}
	q := strings.TrimSpace(strings.ToLower(text))
	if q == "" {
		return Intent{Kind: "keyword", Confidence: 0.5}
	}
	questionHints := []string{"?", " what ", " why ", " how ", " when ", " where ", " who ", "which "}
	for _, hint := range questionHints {
		if strings.Contains(" "+q+" ", hint) {
			return Intent{Kind: "question", Confidence: 0.82}
		}
	}
	tokenCount := len(strings.Fields(q))
	if tokenCount >= 7 {
		return Intent{Kind: "question", Confidence: 0.72}
	}
	if tokenCount <= 4 {
		return Intent{Kind: "keyword", Confidence: 0.76}
	}
	if strings.ContainsAny(q, "\"*_:/.") {
		return Intent{Kind: "keyword", Confidence: 0.78}
	}
	return Intent{Kind: "question", Confidence: 0.58}
}

func mapToSlice(m map[string]jsondb.SearchDoc) []jsondb.SearchDoc {
	out := make([]jsondb.SearchDoc, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	return out
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func trimPtr(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil
	}
	out := s
	return &out
}

func stringOrEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func maxTime(a time.Time, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

func nowUTC() time.Time {
	return time.Now().UTC()
}
