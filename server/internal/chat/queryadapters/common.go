package queryadapters

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ai-analyst-local/internal/chat/jobs"
)

func metadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if metadata == nil {
			break
		}
		if value, ok := metadata[key]; ok {
			text := strings.TrimSpace(fmt.Sprint(value))
			if text != "" && text != "<nil>" {
				return text
			}
		}
	}
	return ""
}

func metadataInt64(metadata map[string]any, keys ...string) int64 {
	value := metadataString(metadata, keys...)
	if value == "" {
		return 0
	}
	parsed, _ := strconv.ParseInt(value, 10, 64)
	return parsed
}

func sessionString(session map[string]string, metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(session[key]); value != "" {
			return value
		}
	}
	return metadataString(metadata, keys...)
}

func copyExtra(extra map[string]string) map[string]string {
	if len(extra) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(extra))
	for key, value := range extra {
		out[key] = value
	}
	return out
}

func putJSON(extra map[string]string, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	extra[key] = string(encoded)
	return nil
}

func getJSON(extra map[string]string, key string, destination any) error {
	raw := strings.TrimSpace(extra[key])
	if raw == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(raw), destination); err != nil {
		return fmt.Errorf("decode native handle %s: %w", key, err)
	}
	return nil
}

func boundedResultPage(rows []map[string]any, schema []map[string]any, maxRows int, maxBytes int64) jobs.ResultPage {
	page := jobs.ResultPage{Schema: schema}
	for _, row := range rows {
		if maxRows > 0 && len(page.Rows) >= maxRows {
			page.Truncated = true
			break
		}
		encoded, _ := json.Marshal(row)
		rowBytes := int64(len(encoded))
		if maxBytes > 0 && page.Bytes+rowBytes > maxBytes {
			page.Truncated = true
			break
		}
		page.Rows = append(page.Rows, row)
		page.Bytes += rowBytes
	}
	return page
}

func stringMap(values map[string]string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringSlice[T ~string](values []T) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value))
	}
	return out
}
