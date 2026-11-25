# Plan: CSV Analysis Tools for Large Dataset Support

## Problem
When large CSVs are uploaded via the Directory feature, the model calls `read_file` which loads raw CSV text (up to 200KB) into the context window. The model then tries to parse and reason over thousands of rows in-context, hitting context limits and degrading analysis quality.

## Solution
Add four pure-Go server-side CSV analysis tools that process data on the server and return only structured summaries/results to the model. Update the system prompt to guide the model toward these tools instead of `read_file` for CSV files.

## Constraint
`CGO_ENABLED=0` in the Docker build — all tools must be pure Go (no DuckDB/C bindings).

---

## Implementation Steps

### Step 1: Add `OpenFile` streaming method to Projects Store
**File:** `server/internal/chat/projects/projects.go`

Add an `OpenFile(projectID, relPath) (*os.File, error)` method that returns an open file handle for streaming reads. Follows the same security pattern as existing `ReadFile` (path normalization, traversal prevention, subpath check). This is needed because `ReadFile` loads everything into `[]byte` — the CSV tools need `io.Reader` for streaming.

~30 lines.

### Step 2: Create CSV helper functions
**File (new):** `server/internal/chat/tools/csv_helpers.go`

Core analysis engine with unexported functions:

| Function | Purpose |
|---|---|
| `newCSVReader(r io.Reader) *csv.Reader` | Creates reader with BOM stripping, flexible field count, lazy quotes |
| `detectDelimiter(r io.ReadSeeker) rune` | Scans first 4KB, tries `,` `\t` `;` `\|`, picks most consistent |
| `inferColumnType(values []string) ColumnType` | Tries boolean -> number -> date -> string on sampled values |
| `profileDataset(ctx, f) (*DatasetProfile, error)` | Single-pass streaming profile: row count, per-column types/stats/nulls/samples. Uses Welford's algorithm for mean/variance, reservoir sampling (k=10,000) for median |
| `reservoirSample(reader, k) ([][]string, int64)` | Algorithm R for random row sampling |
| `evalFilter(value, operator, compareValue) bool` | Evaluates filter predicates (eq, neq, gt, lt, contains, etc.) |
| `aggregateCSV(ctx, f, groupBy, metrics) (results, error)` | Single-pass grouped aggregation with 1000-group cap |

Key types:
```go
type ColumnType string // "string", "number", "date", "boolean"

type ColumnProfile struct {
    Name         string     `json:"name"`
    Type         ColumnType `json:"type"`
    NullCount    int64      `json:"null_count"`
    Min          *float64   `json:"min,omitempty"`
    Max          *float64   `json:"max,omitempty"`
    Mean         *float64   `json:"mean,omitempty"`
    Median       *float64   `json:"median,omitempty"`
    Stddev       *float64   `json:"stddev,omitempty"`
    Cardinality  *int64     `json:"cardinality,omitempty"`
    SampleValues []string   `json:"sample_values"`
}

type DatasetProfile struct {
    Path     string          `json:"path"`
    RowCount int64           `json:"row_count"`
    Columns  []ColumnProfile `json:"columns"`
}
```

~400-500 lines.

### Step 3: Create CSV tool definitions (Go backend)
**File (new):** `server/internal/chat/tools/csv_tools.go`

Four tools following the exact `ExecutableTool` pattern from `file_tools.go` and `bigquery_tools.go`:

#### `describe_dataset`
- **Input:** `path` (string, required)
- **Output:** `DatasetProfile` JSON — column names, inferred types, row count, null counts, numeric stats (min/max/mean/median/stddev), string cardinality, 5 sample values per column
- Uses `ps.OpenFile` -> `profileDataset()`
- Validates `.csv` extension

#### `sample_rows`
- **Input:** `path` (required), `count` (default 10, max 1000), `offset` (default 0), `random` (default false)
- **Output:** `{"rows": [...], "total_rows": N, "columns": [...]}`
- Sequential mode: skip offset rows, read count rows
- Random mode: reservoir sampling across entire file

#### `filter_csv`
- **Input:** `path` (required), `column` (required), `operator` (required: eq/neq/gt/lt/gte/lte/contains/starts_with/ends_with/is_null/not_null), `value`, `limit` (default 100, max 1000)
- **Output:** `{"rows": [...], "total_matches": N, "columns": [...]}`
- Streams through file, applies filter, collects up to limit matches, counts total

#### `aggregate_csv`
- **Input:** `path` (required), `group_by` (optional array of column names), `metrics` (required array of `{column, function}` where function is count/sum/avg/min/max/count_distinct)
- **Output:** `{"results": [...], "group_count": N}`
- Single-pass with group accumulators, 1000-group cap

All tools use `requireProject(exec)` for validation, `decodeArgs` for argument parsing, `normalizeRelPathForDisplay` for path sanitization, and return `JSONOK()`/`JSONError()`.

~350-400 lines.

### Step 4: Register tools in Go registry
**File:** `server/internal/chat/tools/registry.go`

Add after line 41 (after file tools, before BigQuery tools):
```go
// CSV analysis tools
r.mustRegister(newDescribeDatasetTool(), true, false, false)
r.mustRegister(newSampleRowsTool(), true, false, false)
r.mustRegister(newFilterCSVTool(), true, false, false)
r.mustRegister(newAggregateCSVTool(), true, false, false)
```

### Step 5: Create frontend tool definitions
**File (new):** `tools/csvAnalysis.ts`

Mirror the four tool definitions (name, description, parameters) character-for-character from the Go backend. Execute functions are stubs since tools run server-side. Export as `csvAnalysisTools` array.

**File:** `tools/registry.ts`

Add import and spread:
```typescript
import { csvAnalysisTools } from './csvAnalysis';

const allTools: ExecutableTool[] = [
  ...bigQueryTools,
  ...fileSystemTools,
  ...csvAnalysisTools,
  displayTableTool
];
```

~120 lines new, 3 lines modified.

### Step 6: Update system prompt
**File:** `constants.ts` (line 73, after existing file tool instructions)

Add:
```
When working with CSV files:
- Use 'describe_dataset' first to profile any CSV file — it returns column names, types, row count, statistics, and sample values without loading the full file.
- Use 'sample_rows' to inspect specific rows or get a random sample.
- Use 'filter_csv' to find rows matching specific conditions.
- Use 'aggregate_csv' to compute group-by aggregations (count, sum, avg, min, max, count_distinct).
- Avoid using 'read_file' on large CSV files; prefer the CSV analysis tools above.
- For small CSV files (under ~50 rows), 'read_file' is acceptable.
```

### Step 7: Tests
**File (new):** `server/internal/chat/tools/csv_tools_test.go`

Unit tests:
- `TestDetectDelimiter` — comma, tab, semicolon, pipe files
- `TestInferColumnType` — numbers, dates, booleans, strings, mixed
- `TestProfileDataset` — verify row count, column types, numeric stats, null counts, samples
- `TestReservoirSampling` — 10K rows, verify roughly uniform distribution
- `TestFilterEval` — all operators with string and numeric values
- `TestAggregation` — single-group and multi-group, all metric functions, group cap

Integration tests (using `t.TempDir()` + `projects.Store`):
- `TestDescribeDatasetTool` — full tool execution path
- `TestSampleRowsTool` — sequential and random modes
- `TestFilterCSVTool` — various operators
- `TestAggregateCSVTool` — grouped and ungrouped

Edge cases:
- Empty CSV (header only), single column, 100K rows (no OOM), malformed CSV, BOM

~400-500 lines.

---

## Key Design Decisions

1. **Streaming everywhere** — All tools use `encoding/csv.Reader` (line-by-line). Memory bounded regardless of file size.
2. **Median via reservoir sample** — k=10,000 reservoir for approximate median. Exact for files with <10K numeric values per column.
3. **Welford's algorithm** — Online mean/variance in single pass (no second pass needed).
4. **String cardinality cap** — Track unique values per column up to 10,001; report `>10000` when exceeded.
5. **Auto-detect delimiter** — Scan first 4KB, try common delimiters, pick most consistent. No user configuration needed.
6. **Numeric parsing** — Strip `$`, `,`, `%` before `strconv.ParseFloat` during type inference for real-world CSV compatibility.
7. **Context cancellation** — Check `ctx.Done()` every 10,000 rows in streaming loops.

## Files Summary

| Action | File |
|---|---|
| Modify | `server/internal/chat/projects/projects.go` |
| Create | `server/internal/chat/tools/csv_helpers.go` |
| Create | `server/internal/chat/tools/csv_tools.go` |
| Modify | `server/internal/chat/tools/registry.go` |
| Create | `tools/csvAnalysis.ts` |
| Modify | `tools/registry.ts` |
| Modify | `constants.ts` |
| Create | `server/internal/chat/tools/csv_tools_test.go` |

## Verification

1. `cd server && go build ./...` — confirms compilation with CGO_ENABLED=0
2. `cd server && go test ./internal/chat/tools/... -run TestCSV -v` — runs all CSV tool tests
3. Manual test: upload a large CSV via Directory picker, send a message like "Analyze this dataset", verify the model uses `describe_dataset` first instead of `read_file`
4. Verify tool results render correctly in the artifact panel (JSON envelope format is consistent)
