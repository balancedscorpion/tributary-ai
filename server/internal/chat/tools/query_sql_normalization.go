package tools

import (
	"strconv"
	"strings"
)

// normalizeQuerySQLInput tolerates common model formatting wrappers (markdown
// fences and quoted strings) and returns SQL text suitable for execution.
func normalizeQuerySQLInput(raw string) string {
	sql := strings.TrimSpace(raw)
	if sql == "" {
		return ""
	}

	if unquoted, err := strconv.Unquote(sql); err == nil {
		sql = strings.TrimSpace(unquoted)
	}

	if strings.HasPrefix(sql, "```") {
		lines := strings.Split(sql, "\n")
		if len(lines) > 1 {
			if strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			lines = lines[1:]
			sql = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}

	if strings.HasPrefix(strings.ToUpper(sql), "SQL:") {
		sql = strings.TrimSpace(sql[4:])
	}

	return sql
}

func normalizeQueryDescription(raw string) string {
	description := strings.TrimSpace(raw)
	if description == "" {
		return ""
	}
	// Collapse whitespace noise from model output while keeping user-facing text intact.
	return strings.Join(strings.Fields(description), " ")
}
