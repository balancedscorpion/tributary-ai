# Query Metadata UI/UX Implementation Plan

## User Journey & Requirements

### The Five Touchpoints

**1. Query Submission** — User sends a natural-language question. The LLM generates SQL and calls `run_bigquery_query` or `run_databricks_query`. A `ToolCallBlock` appears with the SQL in a `CodeBlock`.

> **User need**: "Is my query running? How long has it been?"

**2. Query Executing (In-Progress)** — Currently nothing changes visually. The ToolCallBlock is static. Users stare at frozen SQL for potentially 30-60s with zero progress feedback.

> **User need**: "Show me a timer counting up. Show me the query is alive, not stuck."

**3. Query Completed (Inline Result)** — A `ToolResultBlock` appears showing an `ArtifactCard` with "Success · N rows" or "Error". That's it.

> **User need**: "How long did that take? How much data was scanned? Was it cached? What's the Job ID for debugging?"

**4. Full Artifact View** — User clicks the card to open `ArtifactPanel` with `DataVisualization` (table + chart). No metadata shown.

> **User need**: "Show me column types alongside the data. Give me a place to inspect all query details."

**5. Iterative Analysis** — User references the artifact in follow-ups, re-queries.

> **User need**: "Consistent metadata makes comparing query performance natural."

---

## Provider Metadata Inventory

### BigQuery — Available from `bqQueryResponse` (already parsed in handler)

| Field | Source | Currently used? |
|-------|--------|----------------|
| `JobReference.JobID` | `jobs.query` response | No (discarded in `runQuery`) |
| `JobReference.Location` | `jobs.query` response | No (discarded) |
| `JobReference.ProjectID` | `jobs.query` response | No (discarded) |
| `TotalBytesProcessed` | `jobs.query` response | Preflight only |
| `JobComplete` | `jobs.query` response | Used for polling, then discarded |
| `Schema.Fields[]` (name, type, mode, description) | `jobs.query` response | Used for column names only, types discarded |
| `Errors[]` (message, reason, location) | `jobs.query` response | Partially (first error message) |

**Not in current struct but available via same API with one extra `jobs.get` call:**
- `cacheHit`, `totalBytesBilled`, `totalSlotMs`, `statementType`
- `referencedTables[]`, `totalPartitionsProcessed`
- `queryPlan[]` (stages), `timeline[]`, `performanceInsights`

### Databricks — Available from Statement Execution API

**Currently parsed in `statementResponse` struct (line 479):**

| Field | Currently used? |
|-------|----------------|
| `statement_id` | Used for polling path, then discarded |
| `status.state` | Used for polling loop, then discarded |
| `status.error.message` | Used for error reporting |
| `manifest.schema.columns[].name` | Used for column names |
| `result.data_array` | Used for rows |

**Available but NOT in current struct — needs struct expansion:**

| Field | Value |
|-------|-------|
| `status.error.error_code` | Structured error code (e.g., `SYNTAX_ERROR`) |
| `manifest.format` | `JSON_ARRAY` / `CSV` / `ARROW_STREAM` |
| `manifest.total_row_count` | Total rows across all chunks |
| `manifest.total_byte_count` | Total result bytes |
| `manifest.total_chunk_count` | Number of result chunks |
| `manifest.truncated` | Whether `row_limit`/`byte_limit` trimmed results |
| `manifest.schema.column_count` | Number of columns |
| `manifest.schema.columns[].type_name` | Column type name (e.g., `INT`, `STRING`) |
| `manifest.schema.columns[].type_text` | Full type text (e.g., `DECIMAL(10,2)`) |
| `manifest.schema.columns[].type_precision` | Precision for numeric types |
| `manifest.schema.columns[].type_scale` | Scale for numeric types |
| `manifest.schema.columns[].position` | Column ordinal position |
| `result.row_count` | Rows in current chunk |
| `result.byte_count` | Bytes in current chunk |

**Available from request context (we already have these values in `runQuery` args):**

| Field | Source |
|-------|--------|
| `warehouse_id` | Passed as arg to `runQuery` |
| `catalog` | Passed as arg to `runQuery` |
| `schema` | Passed as arg to `runQuery` |

**Databricks "query URL" equivalent:**
- `statement_id` → locates query in Databricks Query History UI
- Pattern: `https://{host}/sql/history?filter={"statementId":"{statement_id}"}`
- No `spark_ui_url` from statement execution API (that's query history API only)

---

## Design: Unified `QueryMetadata` with Provider Variants

The UI should render a single consistent component but conditionally show provider-specific fields. The type system uses a discriminated union on `provider`.

### Frontend Type

```typescript
// Common fields every provider returns
interface QueryMetadataBase {
  provider: string;
  duration_ms: number;
  total_rows: number;
  rows_returned: number;       // after truncation
  truncated: boolean;
  schema: ColumnSchema[];
}

interface ColumnSchema {
  name: string;
  position: number;
  type: string;               // BQ: type field, DB: type_name
  type_text?: string;         // DB: full type text like "DECIMAL(10,2)"
  type_precision?: number;    // DB only
  type_scale?: number;        // DB only
  mode?: string;              // BQ: NULLABLE/REQUIRED/REPEATED
  description?: string;       // BQ: field description
}

// BigQuery-specific
interface BigQueryMetadata extends QueryMetadataBase {
  provider: 'bigquery';
  job_id: string;
  job_location: string;
  project_id: string;
  total_bytes_processed: number;
  cache_hit?: boolean;           // Phase 2 (requires jobs.get)
  total_bytes_billed?: number;   // Phase 2
  total_slot_ms?: number;        // Phase 2
  statement_type?: string;       // Phase 2
}

// Databricks-specific
interface DatabricksMetadata extends QueryMetadataBase {
  provider: 'databricks';
  statement_id: string;
  warehouse_id: string;
  catalog?: string;
  db_schema?: string;           // "schema" is reserved
  total_byte_count?: number;    // manifest.total_byte_count
  total_chunk_count?: number;
  query_history_url?: string;   // constructed from host + statement_id
}

type QueryMetadata = BigQueryMetadata | DatabricksMetadata;
```

### UI Rendering Per Provider

**Inline summary (ToolResultBlock):**

BigQuery:
```
Success · 1,247 rows · 2.3s · 45.2 MB scanned
```

Databricks:
```
Success · 1,247 rows · 2.3s · 12.1 MB result
```

(BigQuery says "scanned" because `total_bytes_processed` = bytes read. Databricks says "result" because `total_byte_count` = bytes returned.)

**Query details drawer — adapts per provider:**

BigQuery section:
```
Job ID        bq-abc123-def456         [copy]
Project       my-gcp-project
Location      US
```

Databricks section:
```
Statement ID  01ef-abcd-1234           [copy]
Warehouse     abc123def456
Catalog       main
Schema        default
              [Open in Query History →]
```

The "Open in Query History" link is only shown for Databricks, constructed as:
`https://{host}/sql/history?filter={"statementId":"{statement_id}"}`

**Schema tab — slightly different columns per provider:**

BigQuery: `#  Name  Type  Mode  Description`
Databricks: `#  Name  Type  Type Detail` (type_text for precision/scale info)

---

## Implementation (Single Pass, Both Providers)

### Step 1: Expand Databricks `statementResponse` struct

**File: `server/internal/databricks/handler.go`** (line 479-497)

Expand the struct to capture the rich manifest metadata:

```go
type statementResponse struct {
    StatementID string `json:"statement_id"`
    Status      struct {
        State string `json:"state"`
        Error *struct {
            ErrorCode string `json:"error_code,omitempty"`
            Message   string `json:"message,omitempty"`
        } `json:"error,omitempty"`
    } `json:"status"`
    Manifest struct {
        Format         string `json:"format,omitempty"`
        TotalRowCount  *int64 `json:"total_row_count,omitempty"`
        TotalByteCount *int64 `json:"total_byte_count,omitempty"`
        TotalChunkCount *int  `json:"total_chunk_count,omitempty"`
        Truncated      bool   `json:"truncated,omitempty"`
        Schema struct {
            ColumnCount int `json:"column_count,omitempty"`
            Columns []struct {
                Name          string `json:"name"`
                Position      int    `json:"position"`
                TypeName      string `json:"type_name,omitempty"`
                TypeText      string `json:"type_text,omitempty"`
                TypePrecision *int   `json:"type_precision,omitempty"`
                TypeScale     *int   `json:"type_scale,omitempty"`
            } `json:"columns"`
        } `json:"schema"`
    } `json:"manifest"`
    Result struct {
        DataArray [][]any `json:"data_array"`
        RowCount  *int64  `json:"row_count,omitempty"`
        ByteCount *int64  `json:"byte_count,omitempty"`
    } `json:"result"`
}
```

No behavior change — existing code only reads the fields it already uses. The new fields are captured automatically by JSON unmarshalling.

### Step 2: Create `QueryResult` return type for both handlers

**File: `server/internal/bigquery/handler.go`**

Define a result struct and update `runQuery`:

```go
type QueryResult struct {
    Rows     []map[string]any
    Metadata map[string]any
}
```

`runQuery` captures timing (`time.Now()` before/after) and collects metadata from the `bqQueryResponse` before returning:

```go
func (h *Handler) runQuery(...) (QueryResult, error) {
    start := time.Now()
    // ...existing query logic...
    durationMs := time.Since(start).Milliseconds()

    schema := make([]map[string]any, 0, len(resp.Schema.Fields))
    for i, f := range resp.Schema.Fields {
        schema = append(schema, map[string]any{
            "name": f.Name, "type": f.Type,
            "mode": f.Mode, "description": f.Description,
            "position": i,
        })
    }

    return QueryResult{
        Rows: queryResponseToRows(resp),
        Metadata: map[string]any{
            "provider":              "bigquery",
            "duration_ms":           durationMs,
            "total_bytes_processed": parseBytesString(resp.TotalBytesProcessed),
            "job_id":                resp.JobReference.JobID,
            "job_location":          resp.JobReference.Location,
            "project_id":            resp.JobReference.ProjectID,
            "total_rows":            len(resp.Rows),
            "schema":                schema,
        },
    }, nil
}
```

`handleQuery` returns both:
```go
writeJSON(w, http.StatusOK, map[string]any{
    "rows":           result.Rows,
    "query_metadata": result.Metadata,
})
```

**File: `server/internal/databricks/handler.go`**

Same pattern for `runQuery`. The metadata map includes Databricks-specific fields:

```go
func (h *Handler) runQuery(ctx context.Context, host, warehouseID, sql, catalog, schema string) (QueryResult, error) {
    start := time.Now()
    // ...existing query + polling logic...
    durationMs := time.Since(start).Milliseconds()

    cols := make([]map[string]any, 0, len(stmt.Manifest.Schema.Columns))
    for _, c := range stmt.Manifest.Schema.Columns {
        cols = append(cols, map[string]any{
            "name": c.Name, "position": c.Position,
            "type": c.TypeName, "type_text": c.TypeText,
            "type_precision": c.TypePrecision, "type_scale": c.TypeScale,
        })
    }

    meta := map[string]any{
        "provider":          "databricks",
        "duration_ms":       durationMs,
        "statement_id":      stmt.StatementID,
        "warehouse_id":      warehouseID,
        "total_rows":        derefInt64(stmt.Manifest.TotalRowCount),
        "total_byte_count":  derefInt64(stmt.Manifest.TotalByteCount),
        "total_chunk_count": derefInt(stmt.Manifest.TotalChunkCount),
        "truncated":         stmt.Manifest.Truncated,
        "schema":            cols,
    }
    if catalog != "" { meta["catalog"] = catalog }
    if schema != "" { meta["db_schema"] = schema }
    // Construct query history URL
    if host != "" && stmt.StatementID != "" {
        meta["query_history_url"] = fmt.Sprintf("https://%s/sql/history?filter=%%7B%%22statementId%%22%%3A%%22%s%%22%%7D",
            host, stmt.StatementID)
    }

    return QueryResult{
        Rows:     statementToRows(stmt),
        Metadata: meta,
    }, nil
}
```

### Step 3: Add `QueryMetadata` to tool Output and envelope

**File: `server/internal/chat/tools/types.go`** (line 26-30)

```go
type Output struct {
    OK             bool            `json:"ok"`
    ResultFormat   ResultFormat    `json:"result_format"`
    Result         any             `json:"result"`
    QueryMetadata  map[string]any  `json:"query_metadata,omitempty"`
}
```

**File: `server/internal/chat/tools/bigquery_tools.go`** (line 174-200)

After extracting rows, also extract metadata:
```go
qm, _ := m["query_metadata"].(map[string]any)
return Output{OK: true, ResultFormat: ResultFormatJSON, Result: out, QueryMetadata: qm}, nil
```

**File: `server/internal/chat/tools/databricks_tools.go`** (line 160-185) — Same.

**File: `server/internal/chat/optionc/manager.go`** (line 856)

Add to envelope:
```go
if out.QueryMetadata != nil {
    env["query_metadata"] = out.QueryMetadata
}
```

### Step 4: Frontend types

**File: `types.ts`** (after line 202)

```typescript
export interface ColumnSchema {
  name: string;
  position?: number;
  type?: string;
  type_text?: string;
  type_precision?: number;
  type_scale?: number;
  mode?: string;              // BigQuery: NULLABLE/REQUIRED/REPEATED
  description?: string;       // BigQuery field description
}

export interface QueryMetadata {
  provider: 'bigquery' | 'databricks';
  duration_ms: number;
  total_rows?: number;
  truncated?: boolean;
  schema?: ColumnSchema[];

  // BigQuery
  job_id?: string;
  job_location?: string;
  project_id?: string;
  total_bytes_processed?: number;
  cache_hit?: boolean;
  total_bytes_billed?: number;
  total_slot_ms?: number;
  statement_type?: string;

  // Databricks
  statement_id?: string;
  warehouse_id?: string;
  catalog?: string;
  db_schema?: string;
  total_byte_count?: number;
  total_chunk_count?: number;
  query_history_url?: string;
}
```

Extend `ToolResultEnvelope`:
```typescript
export interface ToolResultEnvelope {
  artifact_id: string;
  tool_name?: string;
  tool_call_id?: string;
  ok: boolean;
  result_format: 'json' | 'text';
  result: any;
  query_metadata?: QueryMetadata;
}
```

### Step 5: Formatting utilities

**New file: `lib/formatters.ts`**

```typescript
export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m ${Math.round(s % 60)}s`;
}

export function formatRowCount(count: number): string {
  return new Intl.NumberFormat().format(count);
}
```

### Step 6: Thread metadata to artifacts

**File: `hooks/useChat.ts`** — `buildArtifactFromEnvelope` (line 169)

Pass `query_metadata` into artifact metadata:
```typescript
metadata: {
  tool_name: toolName,
  ok,
  placeholder: false,
  row_count: (maybeDataset as any[]).length,
  query_metadata: envelope.query_metadata,
},
```

Also stamp `_uiStartedAt` in the `tool_calls` SSE handler (line 752):
```typescript
for (const tc of toolCalls) {
  (tc as any)._uiStartedAt = Date.now();
}
```

### Step 7: In-progress timer (ToolCallBlock)

**File: `components/chat/ToolCallBlock.tsx`**

New prop: `isExecuting?: boolean`

For SQL query tools when `isExecuting`:
- Elapsed timer via `useState`/`setInterval(1s)`, from `toolCall._uiStartedAt`
- Thin indeterminate progress bar (CSS animation)
- "Executing query..." text with `Clock` icon

```
[Terminal] [run_bigquery_query badge]
SELECT ...
[======= indeterminate progress ========]
[Clock] 0:23  Executing query...
```

**File: `components/chat/AssistantMessage.tsx`** — New prop `isLastMessage?: boolean`, passed through to ToolCallBlock as `isExecuting`.

**File: `components/chat/MessageList.tsx`** — Pass `isLastMessage={index === messages.length - 1}` to AssistantMessage.

### Step 8: Rich inline summary (ToolResultBlock)

**File: `components/chat/ArtifactCard.tsx`** (line 11)

Change `details?: string` to `details?: React.ReactNode`.

**File: `components/chat/ToolResultBlock.tsx`** (lines 72-75)

Extract `query_metadata` from envelope. Build per-provider summary:

BigQuery:
```
Success · 1,247 rows · 2.3s · 45.2 MB scanned
```

Databricks:
```
Success · 1,247 rows · 2.3s · 12.1 MB result
```

Metrics separated by `·` (`text-muted-foreground/40`). Cache badge (BQ only) when `cache_hit === true`.

### Step 9: Expandable query details (ToolResultBlock)

Below the ArtifactCard, add a collapsible "Query details" section using existing `Collapsible`/`CollapsibleContent`.

**2-column grid, provider-adaptive:**

Common section:
```
Duration        2.3s
Rows returned   1,247 (truncated)
```

BigQuery section:
```
Bytes scanned   45.2 MB
Job ID          bq-abc123          [copy]
Project         my-project
Location        US
```

Databricks section:
```
Result size     12.1 MB
Chunks          3
Statement ID    01ef-abcd-1234     [copy]
Warehouse       abc123def456
Catalog         main
Schema          default
[Open in Query History →]           ← external link
```

Schema summary (nested collapsible):
- BigQuery: `customer_id  STRING  NULLABLE`
- Databricks: `customer_id  INT  DECIMAL(10,2)`

Styling: `bg-background/30 border border-border rounded-lg`. Labels: `text-[10px] uppercase tracking-wider`. Values: `text-xs font-mono`.

### Step 10: ArtifactPanel tabs

**File: `components/ArtifactPanel.tsx`** (line 87-90)

For query artifacts (detect via `artifact.metadata?.query_metadata`), add tabs using existing `Tabs`/`TabsList`/`TabsTrigger`/`TabsContent`:

- **Data & Charts** (default) — existing `DataVisualization`
- **Schema** — column type viewer
- **Query Info** — full metadata panel

Non-query artifacts: no tabs (current behavior preserved).

### Step 11: New components

**New file: `components/SchemaViewer.tsx`**

Table of columns. Per-provider columns:

BigQuery: `#  Name  Type  Mode  Description`
Databricks: `#  Name  Type  Type Detail` (shows `type_text` for precision/scale info, e.g., `DECIMAL(10,2)`)

Reuse table styling from DataVisualization (sticky header, hover rows, mono fonts).

**New file: `components/QueryInfoPanel.tsx`**

Full metadata key-value display. Sections adapt per provider:

- Performance section (common): Duration, Total rows, Truncated
- Data section (provider-specific):
  - BQ: Bytes scanned, Bytes billed, Slot time, Cache hit, Statement type
  - DB: Result size, Chunk count
- Identity section (provider-specific):
  - BQ: Job ID (copyable), Project, Location
  - DB: Statement ID (copyable), Warehouse, Catalog, Schema, "Open in Query History" link
- Use `sonner` toast for "Copied" feedback

---

## Files to Modify (Summary)

### Server (Go)
| File | Change |
|------|--------|
| `server/internal/chat/tools/types.go` | Add `QueryMetadata` field to `Output` |
| `server/internal/bigquery/handler.go` | Add `QueryResult` struct, refactor `runQuery` to return metadata, update `handleQuery` |
| `server/internal/databricks/handler.go` | Expand `statementResponse` struct, add `QueryResult`, refactor `runQuery`, update `handleQuery` |
| `server/internal/chat/tools/bigquery_tools.go` | Extract and pass through `query_metadata` |
| `server/internal/chat/tools/databricks_tools.go` | Extract and pass through `query_metadata` |
| `server/internal/chat/optionc/manager.go` | Include `query_metadata` in ToolResultEnvelope |

### Frontend (TypeScript/React)
| File | Change |
|------|--------|
| `types.ts` | Add `QueryMetadata`, `ColumnSchema` interfaces; extend `ToolResultEnvelope` |
| `lib/formatters.ts` | **New** — `formatBytes`, `formatDuration`, `formatRowCount` |
| `hooks/useChat.ts` | Stamp `_uiStartedAt`; pass `query_metadata` to artifact metadata |
| `components/chat/ToolCallBlock.tsx` | Elapsed timer, progress bar, `isExecuting` prop |
| `components/chat/ToolResultBlock.tsx` | Rich inline summary (per-provider), expandable query details |
| `components/chat/ArtifactCard.tsx` | `details` prop → `React.ReactNode` |
| `components/chat/AssistantMessage.tsx` | `isLastMessage` prop for timer logic |
| `components/chat/MessageList.tsx` | Compute and pass `isLastMessage` |
| `components/ArtifactPanel.tsx` | Tabs for Schema + Query Info |
| `components/SchemaViewer.tsx` | **New** — column type viewer (adapts per provider) |
| `components/QueryInfoPanel.tsx` | **New** — full metadata panel (adapts per provider) |

### Existing utilities to reuse
- `Collapsible` / `CollapsibleContent` from `@/components/ui/collapsible`
- `Tabs` / `TabsList` / `TabsTrigger` / `TabsContent` from `@/components/ui/tabs`
- `Badge` from `@/components/ui/badge`
- `Tooltip` / `TooltipTrigger` / `TooltipContent` from `@/components/ui/tooltip`
- `toast` from `sonner`
- `Copy`, `Clock`, `ChevronDown`, `Zap`, `ExternalLink` icons from `lucide-react`
- `hasNonCollapsedSelectionWithin` from `@/lib/textSelection`

---

## Verification Plan

1. **Databricks struct expansion**: Verify the expanded `statementResponse` struct unmarshals correctly by running a Databricks query and logging the raw JSON response. New fields (`manifest.total_row_count`, `manifest.total_byte_count`, `manifest.schema.columns[].type_name`, etc.) should populate.

2. **Server metadata**: `curl POST /api/bigquery/query` and `curl POST /api/databricks/query` — verify responses include `query_metadata` with all expected fields.

3. **Envelope passthrough**: Send a chat message. Inspect SSE `tool_result` event in browser Network tab. Confirm `query_metadata` appears in the ToolResultEnvelope JSON with the correct provider-specific fields.

4. **In-progress timer**: Trigger a query. Timer ticks up. Result arrives → timer disappears.

5. **Inline summary**: BigQuery shows "Success · 1,247 rows · 2.3s · 45.2 MB scanned". Databricks shows "Success · 1,247 rows · 2.3s · 12.1 MB result".

6. **Query details drawer**: BigQuery shows Job ID, Project, Location, Bytes scanned. Databricks shows Statement ID, Warehouse, Catalog, Schema, "Open in Query History" link. Copy buttons work.

7. **Schema tab**: BigQuery shows Name/Type/Mode/Description. Databricks shows Name/Type/Type Detail with precision/scale info.

8. **ArtifactPanel tabs**: Three tabs for query artifacts. No tabs for non-query artifacts.

9. **Error state**: Failed queries show error badge + any available metadata (BQ: Job ID, DB: Statement ID + error_code).

10. **Non-query tools**: `read_file`, `list_bq_datasets`, etc. render unchanged.

11. **Build**: `npm run build` passes with no type errors.

12. **Go tests**: `go test ./...` passes.
