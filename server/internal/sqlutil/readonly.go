package sqlutil

import "strings"

var bannedStatementKeywords = map[string]struct{}{
	"INSERT":   {},
	"UPDATE":   {},
	"DELETE":   {},
	"MERGE":    {},
	"CREATE":   {},
	"DROP":     {},
	"ALTER":    {},
	"TRUNCATE": {},
	"GRANT":    {},
	"REVOKE":   {},
	"CALL":     {},
	"BEGIN":    {},
	"COMMIT":   {},
	"ROLLBACK": {},
	"EXPORT":   {},
	"LOAD":     {},
	"DECLARE":  {},
	"SET":      {},
}

// IsReadOnlySQL is a best-effort guardrail to prevent non-read-only statements.
// It is intentionally conservative and may reject some valid SELECT queries.
func IsReadOnlySQL(sql string) bool {
	s := strings.TrimSpace(sql)
	if s == "" {
		return false
	}

	keywords, semicolons, lastSemicolonPos, ok := scanSQL(s)
	if !ok {
		return false
	}

	// Disallow multiple statements. Allow a single trailing semicolon.
	if semicolons > 1 {
		return false
	}
	if semicolons == 1 && lastSemicolonPos != len(s)-1 {
		return false
	}

	if len(keywords) == 0 {
		return false
	}
	first := keywords[0]
	if first != "SELECT" && first != "WITH" {
		return false
	}

	// Ban common write / admin statements outside of comments and string literals.
	for _, kw := range keywords {
		if _, banned := bannedStatementKeywords[kw]; banned {
			return false
		}
	}
	return true
}

// scanSQL extracts bareword tokens and semicolon positions while skipping comments
// and quoted literals/identifiers.
func scanSQL(sql string) (keywords []string, semicolons int, lastSemicolonPos int, ok bool) {
	keywords = make([]string, 0, 16)
	lastSemicolonPos = -1

	for i := 0; i < len(sql); {
		ch := sql[i]

		if isASCIIWhitespace(ch) {
			i++
			continue
		}

		// Line comment.
		if hasPrefixAt(sql, i, "--") {
			j := strings.IndexByte(sql[i:], '\n')
			if j == -1 {
				return keywords, semicolons, lastSemicolonPos, true
			}
			i += j + 1
			continue
		}

		// Block comment.
		if hasPrefixAt(sql, i, "/*") {
			j := strings.Index(sql[i+2:], "*/")
			if j == -1 {
				return nil, 0, -1, false
			}
			i += 2 + j + 2
			continue
		}

		// Single-quoted string literal.
		if ch == '\'' {
			next, closed := skipQuoted(sql, i, '\'', true)
			if !closed {
				return nil, 0, -1, false
			}
			i = next
			continue
		}

		// Double-quoted identifier/string.
		if ch == '"' {
			next, closed := skipQuoted(sql, i, '"', true)
			if !closed {
				return nil, 0, -1, false
			}
			i = next
			continue
		}

		// Backtick-quoted identifier.
		if ch == '`' {
			next, closed := skipQuoted(sql, i, '`', true)
			if !closed {
				return nil, 0, -1, false
			}
			i = next
			continue
		}

		if ch == ';' {
			semicolons++
			lastSemicolonPos = i
			i++
			continue
		}

		if isIdentStart(ch) {
			start := i
			i++
			for i < len(sql) && isIdentPart(sql[i]) {
				i++
			}
			keywords = append(keywords, strings.ToUpper(sql[start:i]))
			continue
		}

		i++
	}

	return keywords, semicolons, lastSemicolonPos, true
}

func hasPrefixAt(s string, pos int, prefix string) bool {
	if pos < 0 || pos+len(prefix) > len(s) {
		return false
	}
	return s[pos:pos+len(prefix)] == prefix
}

func skipQuoted(s string, start int, quote byte, allowDoubledQuote bool) (next int, closed bool) {
	i := start + 1
	for i < len(s) {
		if s[i] == quote {
			if allowDoubledQuote && i+1 < len(s) && s[i+1] == quote {
				i += 2
				continue
			}
			return i + 1, true
		}
		if s[i] == '\\' && i+1 < len(s) {
			i += 2
			continue
		}
		i++
	}
	return 0, false
}

func isASCIIWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f' || ch == '\v'
}

func isIdentStart(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '_'
}

func isIdentPart(ch byte) bool {
	return isIdentStart(ch) || (ch >= '0' && ch <= '9')
}
