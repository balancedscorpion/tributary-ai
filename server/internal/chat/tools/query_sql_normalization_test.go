package tools

import "testing"

func TestNormalizeQuerySQLInput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		out  string
	}{
		{name: "plain", in: "SELECT 1", out: "SELECT 1"},
		{name: "quoted", in: "\"SELECT * FROM table\"", out: "SELECT * FROM table"},
		{name: "fenced sql", in: "```sql\nSELECT 1\n```", out: "SELECT 1"},
		{name: "fenced no language", in: "```\nSELECT 2\n```", out: "SELECT 2"},
		{name: "sql prefix", in: "SQL: SELECT 3", out: "SELECT 3"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeQuerySQLInput(tc.in); got != tc.out {
				t.Fatalf("normalizeQuerySQLInput(%q) = %q, want %q", tc.in, got, tc.out)
			}
		})
	}
}
