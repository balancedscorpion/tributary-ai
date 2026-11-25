package sqlutil

import "testing"

func TestIsReadOnlySQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want bool
	}{
		{
			name: "allows simple select with glob-like pattern",
			sql:  "SELECT '**' AS pattern",
			want: true,
		},
		{
			name: "allows regex-style usage to proceed to engine validation",
			sql:  "SELECT REGEXP_CONTAINS(path, r'**') FROM events",
			want: true,
		},
		{
			name: "allows banned keywords inside string literals",
			sql:  "SELECT * FROM events WHERE path LIKE '**/drop/**'",
			want: true,
		},
		{
			name: "allows semicolon inside string literal",
			sql:  "SELECT * FROM events WHERE note = 'semi;colon'",
			want: true,
		},
		{
			name: "allows trailing semicolon",
			sql:  "SELECT 1;",
			want: true,
		},
		{
			name: "rejects multiple statements",
			sql:  "SELECT 1; SELECT 2",
			want: false,
		},
		{
			name: "rejects non-read-only first keyword",
			sql:  "DELETE FROM t",
			want: false,
		},
		{
			name: "rejects write statement that starts with WITH",
			sql:  "WITH cte AS (SELECT 1) DELETE FROM t WHERE id = 1",
			want: false,
		},
		{
			name: "ignores banned words in comments",
			sql:  "/* DROP TABLE x */ SELECT 1",
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsReadOnlySQL(tc.sql)
			if got != tc.want {
				t.Fatalf("IsReadOnlySQL(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
