package publicapi

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"ai-analyst-local/internal/chat/store"
)

const (
	maxArtifactReadBytes  int64 = 256 << 20
	maxArtifactReplyBytes       = 2 << 20
	defaultRowsLimit            = 200
	maxRowsLimit                = 5000

	maxRunResultArtifactPreviewBytes = 64 << 10
	maxRunResultToolResultBytes      = 64 << 10
	maxRunResultMetadataBytes        = 8 << 10
)

func (h *Handler) readArtifactContent(a store.Artifact, maxBytes int64) (string, error) {
	switch strings.ToLower(strings.TrimSpace(a.Storage)) {
	case "file":
		if a.ContentPath == nil || strings.TrimSpace(*a.ContentPath) == "" {
			return "", nil
		}
		rel, err := cleanRelPath(*a.ContentPath)
		if err != nil {
			return "", err
		}
		b, err := osReadFileLimit(filepath.Join(h.cfgDir, filepath.FromSlash(rel)), maxBytes)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		if a.ContentText == nil {
			return "", nil
		}
		return *a.ContentText, nil
	}
}

func (h *Handler) readArtifactContentPreview(a store.Artifact, maxBytes int) (string, bool, error) {
	switch strings.ToLower(strings.TrimSpace(a.Storage)) {
	case "file":
		if a.ContentPath == nil || strings.TrimSpace(*a.ContentPath) == "" {
			return "", false, nil
		}
		rel, err := cleanRelPath(*a.ContentPath)
		if err != nil {
			return "", false, err
		}
		b, truncated, err := osReadFilePreview(filepath.Join(h.cfgDir, filepath.FromSlash(rel)), int64(maxBytes))
		if err != nil {
			return "", false, err
		}
		content := string(b)
		if !utf8.ValidString(content) {
			content = strings.ToValidUTF8(content, "")
		}
		return content, truncated, nil
	default:
		if a.ContentText == nil {
			return "", false, nil
		}
		content, truncated := truncateStringByBytes(*a.ContentText, maxBytes)
		return content, truncated, nil
	}
}

func parseRows(raw string) ([]map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("artifact content is empty")
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		return arr, nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err == nil {
		if rows, ok := obj["rows"].([]any); ok {
			out := make([]map[string]any, 0, len(rows))
			for _, row := range rows {
				if m, ok := row.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out, nil
		}
	}
	r := csv.NewReader(strings.NewReader(raw))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, errors.New("artifact is not a supported row dataset")
	}
	if len(records) == 0 {
		return nil, nil
	}
	headers := records[0]
	out := make([]map[string]any, 0, len(records)-1)
	for _, rec := range records[1:] {
		row := map[string]any{}
		for i, header := range headers {
			if i < len(rec) {
				row[header] = rec[i]
			} else {
				row[header] = nil
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func projectRows(rows []map[string]any, columns []string) []map[string]any {
	if len(columns) == 0 {
		return rows
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		next := map[string]any{}
		for _, col := range columns {
			if value, ok := row[col]; ok {
				next[col] = value
			}
		}
		out = append(out, next)
	}
	return out
}

func clampRowsByBytes(rows []map[string]any, maxBytes int) []map[string]any {
	if maxBytes <= 0 {
		return rows
	}
	for len(rows) > 0 {
		b, err := json.Marshal(rows)
		if err != nil || len(b) <= maxBytes {
			return rows
		}
		rows = rows[:len(rows)-1]
	}
	return rows
}

func cleanRelPath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimPrefix(raw, "/")
	clean := path.Clean(raw)
	if clean == "." || clean == "" {
		return "", errors.New("invalid path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path traversal is not allowed")
	}
	return clean, nil
}

func osReadFileLimit(filename string, limit int64) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if limit <= 0 {
		return io.ReadAll(f)
	}
	var b strings.Builder
	n, err := io.Copy(&b, io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, errors.New("artifact content exceeds read limit")
	}
	return []byte(b.String()), nil
}

func osReadFilePreview(filename string, limit int64) ([]byte, bool, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if limit < 0 {
		limit = 0
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, err
	}
	truncated := int64(len(b)) > limit
	if truncated {
		b = b[:limit]
	}
	return b, truncated, nil
}

func truncateStringByBytes(s string, maxBytes int) (string, bool) {
	if maxBytes < 0 {
		return s, false
	}
	if len(s) <= maxBytes {
		return s, false
	}
	out := s[:maxBytes]
	for len(out) > 0 && !utf8.ValidString(out) {
		out = out[:len(out)-1]
	}
	return out, true
}
