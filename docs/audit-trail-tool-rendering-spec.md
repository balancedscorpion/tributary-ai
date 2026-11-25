# Audit‑Trail Tool Rendering Spec

Status: living document. Owner: chat UI. Last substantive update: 2026‑06‑10.

This app is an agent harness: the model issues tool calls and the UI must keep a
**full, trustworthy audit trail** of what was sent, what came back, how long it
took, and whether it succeeded. This spec defines how every tool family is
rendered in the two surfaces — the **collapsed scanning list** and the **rich
inspector** — so coverage is uniform and new tools have an obvious home instead
of dumping raw JSON.

---

## 1. Principles

1. **Collapsed list is for scanning.** One row per tool call: connector logo,
   tool name (or human activity label), the single most useful argument, status,
   duration. Errors on any child propagate to the group header. No raw JSON.
2. **Inspector is for depth.** Clicking a row opens a three‑zone inspector
   (Header / Request / Response / Metadata). Structured, data‑rich rendering by
   default; a **Raw** disclosure is always present but never the default.
3. **One inspector family.** Every connector reads as the same product: identical
   header band, identical status vocabulary, identical error skeleton. Only the
   `StatusPill` carries state colour.
4. **Connector identity is first‑class.** Logos come from the connection catalog
   (`lib/connectionsCatalog.ts`) and real brand SVGs in `public/brand-logos/`.
5. **No instructional copy.** The UI does not explain itself on screen.
6. **Never cards‑in‑cards; never overflow.** Inspectors own their chrome; tables
   scroll horizontally; long tokens wrap with `[overflow-wrap:anywhere]`.

---

## 2. Data contracts

### 2.1 Result envelope

Backend tools return `Output` (`server/internal/chat/tools/types.go`):
`ok`, `result_format` (`"json"|"text"`), `result` (payload), `query_metadata`
(map), and an optional persisted `Artifact`. The frontend parses
`ToolResultEnvelope` (`types.ts`):

```
artifact_id, tool_name?, tool_call_id?, ok, result_format, result,
query_metadata?, result_metadata?, plot_spec?, chart_budget?, chart_engine?,
chart_spec_version?, chart_module(s)?, preview_only?, full_artifact_id?,
row_count_total?, downsample_ratio?, ui_suppress?
```

Two metadata channels exist and the UI reads **both**:

| Channel | Set by | Carries |
|---|---|---|
| `query_metadata` | SQL tools (BigQuery/Databricks/Trino/Snowflake) + a little of `rlm_exec` | provider, duration_ms, status, sql, job/statement ids, schema, row/byte counts, cache_hit, paging |
| `result_metadata` | GitHub / Slack / Atlassian / Browser | `{integration, product, widget, kind, readonly}` + optional `diagnostic` |

The near‑universal error shape is `{ok:false, result:{error:"…"}}`. Structured
"reason + fix" diagnostics are opt‑in (see §2.4).

### 2.2 The click‑to‑inspect artifact

`CollapsedToolsSummary` turns a tool row into an `Artifact` for the panel. When
there is no real produced artifact it **synthesises** one:

```
{ type:"data", title:<tool>, content:<json>, metadata:{
    tool_name, tool_call_id, integration, product, widget, ok,
    tool_args, raw_result, result_metadata,
    started_at_ms, ended_at_ms, duration_ms } }
```

`'tool_args' in metadata` is the reliable fingerprint for "a user clicked a tool
row" and is what the generic inspector keys on.

### 2.3 Renderer resolution (`components/renderers/registry.ts`)

`resolveRenderer(artifact)` precedence:

0. `metadata.ok === false` → custom resolvers first (errors never land in a
   success renderer).
1. exact `metadata.tool_name` in `toolRenderers`.
2. custom resolvers (in registration order).
3. `artifact.type` in `typeRenderers` (`data`/`code`/`image`/`file`).
4. `null` → `ArtifactPanel` fallback (`DataVisualization` for charts, else `RawViewer`).

`RendererEntry.ownsChrome` makes `ArtifactPanel` render the component full‑bleed
(skipping its own Data/Schema/Query‑Info tabs). All first‑class inspectors set it.

### 2.4 Error diagnostic contract (`tool/errorDiagnostic.tsx`)

`extractToolErrorDiagnostic(artifact, {classifier?})` resolves, in priority order:
nested `result_metadata.diagnostic` (aliases `error_diagnostic`/`diagnostics`) →
nested diagnostic on `raw_result` → direct fields on either → optional connector
string classifier → generic fallback. Fields: `title, code, kind, message, why,
tryThis, failedRequest, source`. `ErrorDiagnosticBody` renders: diagnostic banner
→ failed‑request zone (+ raw args) → Why / Try this → Details disclosure. Tools
opt into rich errors by adding a `diagnostic` object to their result payload — no
backend change is required (the manager copies `result_metadata` through).

### 2.5 Visibility & approval signals

- `ui_suppress: true` (envelope) — do not render this call in the stream. Also
  `result.ui_suppress`/`ui_hidden` on `check_query_status` and incomplete
  `get_query_results`.
- SQL preflight failures carry `instructions:"Do not show this preflight error
  in the UI"` + `preflight_failed`/`rewrite_required` — suppress, the model
  rewrites and resubmits.
- **Write/approval**: `result.approval` present (+ `result_metadata.widget ===
  "approval"`). The action is **not executed** until separately approved. Render
  a write/approval badge and the pending action; never imply it already ran.

---

## 3. The inspector family

| Component | Location | Claims | Chrome |
|---|---|---|---|
| **QueryInspector** | `tool/query/*` | query_metadata / SQL provider / `run_*_query`,`get_query_results` | own |
| **GitHubToolRenderer** | `tool/GitHubToolRenderer.tsx` | `integration==='github'` or `github_*` (success + error) | own |
| **ToolInspector** (generic, connector‑aware) | `tool/inspector/ToolInspector.tsx` | synthesised tool envelopes without a dedicated renderer (Slack, Atlassian, Browser, web_search, future tools) | own |
| **ErrorRenderer** | `tool/ErrorRenderer.tsx` | any `ok===false` not owned by Query/GitHub | own |
| Dedicated tool renderers | `tool/{ReadFile,SearchFiles,ListFiles,SearchThreads,AggregateCsv,DescribeDataset,CatalogBrowser,RlmExec}Renderer.tsx` | their exact tool name | inline card |
| Type fallbacks | `type/{Data,Code,Image,File,SandboxedHtml}Renderer.tsx` | by `artifact.type` | varies |

### 3.1 Shared three‑zone shell (`tool/inspector/InspectorShell.tsx`)

The header band that Query/GitHub/Error previously hand‑rolled is extracted into
`InspectorShell` + `InspectorZone`, built on the shared `tool/query/atoms.tsx`
(`StatusPill`, `CopyIcon`, `Disclosure`, `Stat`, `formatDurationMs`,
`formatClock`, `shortId`). New inspectors compose it; Query and GitHub keep their
own equivalent header (they are preserved) but follow the same visual contract.

- **Header**: `[logo tile] [tool name] [StatusPill] [optional write/approval badge]`
  then a meta line `provider · duration · clock · shortId(callId)⧉ · [Open in …]`.
- **Zones**: `InspectorZone label="Request|Response|Metadata"` — an uppercase
  micro‑label over the content. Error replaces Response with `ErrorDiagnosticBody`.

### 3.2 Structured value rendering (`tool/inspector/values.tsx`)

The anti‑raw‑JSON engine used by `ToolInspector`:

- `ValueBlock` dispatches by shape: array‑of‑objects → `AutoTable`; array‑of‑
  scalars → chip list; object → `KeyValueGrid`; string → text/mono; scalar →
  typed cell.
- Typed cells detect URLs (link), ISO timestamps (localised), emails, booleans
  (✓/✗ pill), numbers (grouped). Nested depth is capped; deep values collapse to
  a `Disclosure` of compact JSON.
- `AutoTable`: union of keys as columns (capped), rows capped with "+N more",
  horizontal scroll, sticky header.

### 3.3 ToolInspector behaviour

1. Resolve connector from `integration` / `getConnectionForToolName(tool_name)`.
2. **Header** — connector logo + tool name + success `StatusPill`; write/approval
   badge when applicable.
3. **Request** — `tool_args` as a `KeyValueGrid` (or "No arguments").
4. **Response** — `pickPrimary()` selects the dominant payload (the single array
   field, or a known key like `messages`/`issues`/`results`/`rows`/`logs`) and
   renders it with `ValueBlock`; remaining top‑level scalars render compactly.
   Text results render as text. Approval payloads feature the pending action.
5. **Metadata** — identity chips (`integration·product·widget`, `readonly`),
   paging signals (`next_cursor`, `has_more`, totals), and a **Raw** disclosure
   of the full result.

Errors for these families fall to `ErrorRenderer`, which now shows the same
connector logo, so success and failure read as one product.

---

## 4. Per‑family rendering

Notation: **collapsed** = the one‑line card; **inspector** = the panel zones.
Logo column is the `connectionsCatalog` id.

### Family 1 — Project files / local search / git history
Logo: none (local/project; neutral lucide icon). Tools: `list_files`,
`read_file`, `search_files`, `file_search`, `local_read_file`,
`list_git_repositories`, `git_list_refs`, `git_log`, `git_search_history`,
`git_grep`, `git_show_commit`, `git_show_file_at_ref`.

- **read_file / git_show_*** → dedicated `ReadFileRenderer` style (path, size,
  language, Truncated badge, syntax‑highlighted body). Collapsed: path + size.
- **list_files / search_files / git_grep / git_log** → `ListFiles` /
  `SearchFiles` renderers (file tree, match groups). Collapsed: query / count.
- Result fields to surface: `files/total_count`; `matches{path→lines}`;
  `content/has_more/next_page_token`; git `commits[]{commit,subject,author,
  authored_at,changed_files}`, `refs[]`, `matches[]{path,line,content}`.
- Errors: plain `{error}` (git errors are stderr‑derived) → ErrorRenderer.
- No external API; these are local. Paging via opaque `next_page_token` (byte/row offset).
- `file_search` and `local_read_file` are local-machine capabilities and must
  surface the approved root/path in metadata so the permissions UI and audit
  trail show exactly what location was searched or read.

### Family 2 — Thread attachments / artifacts / search
Logo: none. Tools incl. `inspect_attachment`, `read_attachment`,
`search_attachment`, `export_thread`, `get_artifact_chunk`,
`search_artifact_content`, `get_json_artifact`, `search_threads`, `search_docs`,
`get_doc`, `get_artifact`, `list_messages`.

- **search_threads / search_docs** → `SearchThreadsRenderer` (result list with
  title, snippet, score, kind). Collapsed: query + count.
- **inspect_attachment / read_attachment** → ToolInspector: KeyValueGrid of
  `file_name, mime_type, size_bytes, sha256, is_text, preview` (+ preview block).
- **export_thread** emits a `file` artifact (`bundle_type`, counts) →
  FileRenderer / download.
- All carry a descriptor (`id, type, title, byte_size, metadata`). Paging fields
  (`offset, has_more, next_page_token`) shown in Metadata.

### Family 3 — CSV / dataset / chart / interactive artifacts
Logo: none. Tools incl. `describe_dataset`, `sample_rows`, `filter_csv`,
`aggregate_csv`, dataset‑artifact tools, `display_table`,
`build_chart_artifact`, `create_interactive_artifact`,
`create_interactive_data_artifact`, `get_*_capabilities`.

- **describe_dataset / aggregate_csv** → dedicated renderers (profile, group
  table). **build_chart_artifact / display_table** emit `data` artifacts with
  `plot_spec`/`chart_engine` → `DataVisualization` (charts). **create_interactive
  *** → `SandboxedHtmlRenderer` (sandboxed iframe).
- Structured errors here use the only backend "reason + fix" shape outside SQL:
  `structuredChartError {error, code, hint, failure_stage, chart_budget}` — map
  `code`→banner code, `hint`→Try this.
- `preview_only` + `full_artifact_id` + `retrieval` ⇒ "preview; load full
  artifact" affordance.

### Family 4 — SQL data sources  (PRESERVED — QueryInspector)
Logos: `bigquery`, `databricks`, `trino`, `snowflake`. Tools: `run_*_query`,
`list_*`, `describe_*_table`/`get_bq_table_schema`, async
`check_query_status` / `get_query_results` / `cancel_query`.

- Collapsed: provider logo + status + duration; running shows a live progress
  bar; the query card is a one‑row launcher into the inspector.
- Inspector (existing): Header (provider, duration, job id, **Open in console**),
  Request (description + highlighted SQL), Response (stat row rows/bytes/cache/
  duration + live‑paged results `Explorer`; Running/Cancelled/Error variants).
- Result fields: `rows`, `page_size/offset/has_more/next_page_token`,
  `query_metadata{provider, duration_ms, status, sql, schema, rows_returned,
  row_count_total, total_bytes_processed, cache_hit, job_id/statement_id/query_id,
  provider_job_url}`. Async submit returns `{status:"running", job_id,
  owner_token, provider_job_url}`.
- `list_*`/`describe_*` → `CatalogBrowserRenderer`.
- **Hide** internal polling (`check_query_status`, incomplete `get_query_results`)
  via `ui_suppress`.
- Primary sources: BigQuery jobs.query/getQueryResults (`schema, rows, totalRows,
  pageToken, totalBytesProcessed, cacheHit, jobReference.jobId, jobComplete`);
  Databricks Statement Execution (`statement_id, status.state, manifest.schema.
  columns, result.data_array/external_links, next_chunk_index`); Trino client
  protocol (`id, columns, data, stats{state,processedRows,processedBytes},
  nextUri, error`); Snowflake SQL API (`statementHandle, resultSetMetaData
  {numRows,rowType,partitionInfo}, data, code, message, statementStatusUrl`). See §6.

### Family 5 — Atlassian (Jira / Confluence)
Logo: `atlassian` (product `atlassian|jira|confluence`). Read tools return opaque
bridge maps; the inspector treats `result` as free‑form (ValueBlock) — the one
reliably present optional is `result.url` → header "Open in Atlassian".

- **jira_get_issue** → KeyValueGrid of `fields{summary,status,assignee,issuetype,
  priority,labels}`. **jira_search_issues / jira_find** → AutoTable of issues.
  **jira_get_issue_comments / _history** → list/timeline. **jira_read_attachment**
  may emit an `image` artifact → ImageRenderer.
- **Write** (`jira_add_comment`, `_reply_to_comment`, `_add_related_tickets`,
  `_upload_attachment`, `_update_issue`, `_create_issue`) → approval payload;
  render write badge + pending `{action, target, content}`. Inline approval card
  (`components/chat/AtlassianToolResult.tsx`) still owns the approve/deny action.
- Why/fix examples: mention validation ("requires an accountId — call
  jira_find_users first"); upload "exactly one of artifact_id/path/…".
- Sources: Jira Cloud REST v3 (issue `fields`, `comment`, changelog
  `items[]{field,fromString,toString}`, `/search/jql` `issues,nextPageToken`);
  Confluence REST v2 (`results[], _links.next`; page `body.storage`). See §6.

### Family 6 — Browser automation (`cua_browser_*`)
Logo: `browser` (Chrome). Tools: `cua_browser_{status,navigate,snapshot,click,
type,press,wait,back,logs,clear_logs}`. Results are opaque maps (commonly `url`,
snapshot text/elements, logs). Status/snapshot/log reads are read-only; navigate,
click, type, press, wait, back, and clear_logs are execute-class tools governed
by the persistent approval policy. They emit `tool_progress`
(`browser_navigating`/`browser_reading`) while running.

- Collapsed: activity label ("Navigated to …", "Read page", "Clicked …") + url.
- Inspector: navigate → KV `{url, title, status}`; snapshot → mono element/AX
  tree + element count; logs → AutoTable `{level/type, text, url, status}`;
  click/type/press → the target + resulting url.
- Maps to Playwright: goto/goBack/click/fill/press/waitForLoadState/aria snapshot
  /console messages. See §6.

### Family 7 — GitHub  (PRESERVED — GitHubToolRenderer)
Logo: `github` (mono). Widget‑aware sub‑system (`tool/github/widgets.tsx`):
status, connection, repository_list, pull_request(_list), issue(_list),
workflow_run_list, workflow_run_jobs, approval, gh_command. Read results embed
typed structs (`githubcli/types.go`); list wrappers carry `total_count, limit,
truncated`. Errors route through the same renderer with `ErrorDiagnosticBody` and
a 401/403/404 classifier + synthesised `$ gh …` failed‑request line.

- **Write** (`github_clone_repository`, `_pull_`, `_refresh_`,
  `github_run_gh_command` when not read‑only) → approval widget.
- Sources: GitHub REST repos/pulls/issues/Actions runs+jobs; `gh --json`
  (camelCase keys). See §6.

### Family 8 — Slack
Logo: `slack`. Read results attach `result_metadata{provider:'slack',
kind, readonly:true}`. Cursor paging via Slack `response_metadata.next_cursor`.

- **slack_get_messages / _thread_replies** → AutoTable `{ts→time, user, text}`.
  **slack_list_conversations** → AutoTable `{name, id, is_private, num_members,
  topic}`. **slack_get_conversation_members** → chip list. **slack_get_user** →
  KV profile. **slack_search_messages** → matches table `{channel, user, ts, text,
  permalink}`. **slack_download_file** → `local_path`.
- **Write** (`slack_post_message`, `slack_upload_file`) → approval payload; write
  badge + pending message/upload. Scope errors ("missing chat:write — reconnect
  Slack") → ErrorRenderer.
- Sources: Slack Web API `conversations.*`, `chat.postMessage`,
  `files.getUploadURLExternal`+`completeUploadExternal`, `users.info`,
  `search.messages`; envelope `{ok, error, response_metadata.next_cursor}`. See §6.

### Family 9 — SharePoint
Logo: `sharepoint`. Tools: `sharepoint_status`, `sharepoint_check_connection`,
`sharepoint_search`, `sharepoint_get_metadata`, `sharepoint_read`,
`sharepoint_summarize`, `sharepoint_download`.

- Search/metadata tools render through the connector inspector with result
  lists, file names, URLs, sizes, and modified timestamps surfaced before Raw.
- Read/summarize/download tools promote remote files to native viewers where
  possible. The response and metadata must preserve `item_url`,
  `server_relative_url`, `remote_id`, `artifact_id`, and `source_url`.
- Errors should keep Microsoft/SharePoint context visible: missing permissions,
  tenant/auth problems, unsupported file type, or download failure.

### Family 10 — arXiv
Logo: none/provider-neutral unless a future arXiv mark is added. Tools:
`arxiv_status`, `arxiv_check_connection`, `arxiv_search`,
`arxiv_screening_report`, `arxiv_recommendations`, `arxiv_list_papers`,
`arxiv_get_paper`, `arxiv_get_paper_text`, `arxiv_read_paper`,
`arxiv_download_paper`.

- Search/recommendation/screening tools render ranked paper lists with title,
  authors, arXiv id, score/reasoning, and downloaded/cached state.
- Paper text/page readers render as code/text-native artifacts with page/window
  metadata. Downloads render as native file artifacts with `arxiv_id`,
  `artifact_id`, `remote_id`, and `source_url`.
- Screening/audit fixtures must show progress, cached paper state, and any
  parse/download error without dumping raw provider JSON first.

### Family 11 — Provider-hosted model capabilities
Logo: provider label, not a normal connection. Current capabilities:
`openai-responses/web_search`, `openai-responses/code_interpreter`,
`anthropic-messages/web_search`, `anthropic-messages/code_execution`.

- These are not registered local tools. They arrive as provider-executed tool
  calls/results and render through the connector inspector with provider-specific
  identity (`OpenAI Responses` or `Anthropic`), `provider_hosted_tool_id`,
  `adapter_family`, `settings_key`, and the provider result payload.
- Web search results should surface query, titles, URLs, and snippets.
  Code-execution/code-interpreter results should surface status, stdout/stderr,
  generated files, and sandbox/container ids when provided.
- They are permissioned with `subject.kind = provider_hosted_tool`; approval
  and settings rows must not pretend they are local registry tools.

### Family 12 — `rlm_exec`
Logo: none. Dedicated `RlmExecRenderer` (stdout, evidence, final answer).
`query_metadata` may carry `evidence_budget/used/exhausted`. Validation errors
are precise ("open() is not supported", "f‑strings are not valid Starlark") →
surfaced as `{error}`.

---

## 5. Collapsed summary requirements (Requirement C)

`CollapsedToolsSummary` already, and must continue to:

- **Propagate child errors** to the group header (`groupStatus.hasError` via
  `isEnvelopeError`) and per row (`rowHasError`).
- **Show connector logos**: a deduped logo stack in the header
  (`groupConnectorMetas`) and a per‑row logo (`getConnectionForToolName` →
  `CONNECTION_META` → `ConnectionLogo`); mono marks tinted via `.ta-mono`.
- **Show duration**: group `durationMs` (max end − min start) and per‑row
  duration (hidden only in `compact`).
- **Avoid duplicate single‑child naming**: the per‑tool name‑pill strip is gated
  on `> 1` visible call; a single child shows only its row. Activity rows
  intentionally pair a human label with a dim raw tool name (identifier, not a
  duplicate title).

---

## 6. Primary API references

These are the canonical source keys for the `Owner/source` column in §8. Local
project, artifact, CSV, dataset, and `rlm_exec` tools are app-local and source
from `server/internal/chat/tools/*.go` rather than an external API.

| Source key | Official reference | Fields this UI depends on |
|---|---|---|
| `GH-Repos` | `https://docs.github.com/en/rest/repos` | repository identity, owner, visibility, timestamps, clone URL |
| `GH-Pulls` | `https://docs.github.com/en/rest/pulls` | PR number/title/state/user/branch/update timestamps |
| `GH-Issues` | `https://docs.github.com/en/rest/issues/issues` | issue number/title/state/labels/assignee/comment metadata |
| `GH-Actions-Runs` | `https://docs.github.com/en/rest/actions/workflow-runs` | workflow run id/name/status/conclusion/event/branch |
| `GH-Actions-Jobs` | `https://docs.github.com/en/rest/actions/workflow-jobs` | job id/name/status/conclusion/steps/log links |
| `GH-CLI` | `https://cli.github.com/manual` | raw `gh` args, stdout/stderr, `--json` camelCase payloads |
| `Slack-Conversations` | `https://docs.slack.dev/apis/web-api/using-the-conversations-api/` and `https://docs.slack.dev/reference/methods/conversations.history/` | channel ids, message `ts`, user ids, `response_metadata.next_cursor` |
| `Slack-Write` | `https://docs.slack.dev/reference/methods/chat.postMessage/`, `https://docs.slack.dev/reference/methods/files.getUploadURLExternal/`, `https://docs.slack.dev/reference/methods/files.completeUploadExternal/` | message/file submission args, channel/thread ids, Slack error codes |
| `Slack-Users-Search` | `https://docs.slack.dev/reference/methods/users.info/`, `https://docs.slack.dev/reference/methods/search.messages/` | user profile fields, message matches, permalinks |
| `Jira-v3` | `https://developer.atlassian.com/cloud/jira/platform/rest/v3/` | issue fields, comments, attachments, users, link types, ADF bodies |
| `Jira-Search` | `https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issue-search/` | JQL/search results, issue pages, next-page tokens |
| `Jira-Issues` | `https://developer.atlassian.com/cloud/jira/platform/rest/v3/api-group-issues/` | create/update issue fields, changelog, transitions |
| `Confluence-v2` | `https://developer.atlassian.com/cloud/confluence/rest/v2/intro/` | pages, spaces, `_links.next`, body representations |
| `BigQuery-Query` | `https://cloud.google.com/bigquery/docs/reference/rest/v2/jobs/query` | submitted SQL, `jobReference.jobId`, `jobComplete`, schema/rows |
| `BigQuery-Results` | `https://cloud.google.com/bigquery/docs/reference/rest/v2/jobs/getQueryResults` and `https://cloud.google.com/bigquery/docs/reference/rest/v2/tabledata/list` | paged rows, `totalRows`, `pageToken`, schema, cache/byte stats |
| `Databricks-Statement` | `https://docs.databricks.com/api/workspace/statementexecution` | `statement_id`, `status.state`, manifest schema/chunks, `result.data_array`, external links |
| `Trino-Protocol` | `https://trino.io/docs/current/develop/client-protocol.html` | `QueryResults{id,nextUri,columns,data,stats,error}` and polling/deletion semantics |
| `Snowflake-SQL` | `https://docs.snowflake.com/en/developer-guide/sql-api/handling-responses` and `https://docs.snowflake.com/en/developer-guide/sql-api/reference` | `statementHandle`, `ResultSet`, `resultSetMetaData`, partitions, status URLs |
| `Playwright-Page` | `https://playwright.dev/docs/api/class-page` | navigation, load-state, console/network/event state |
| `Playwright-Locator` | `https://playwright.dev/docs/api/class-locator` | click/fill/press target actions |
| `Playwright-ARIA-Console` | `https://playwright.dev/docs/aria-snapshots` and `https://playwright.dev/docs/api/class-consolemessage` | accessibility snapshots and browser logs |

---

## 7. Dev fixtures & tests

`/dev/audit-trail` (`components/dev/AuditTrailFixture.tsx`) mounts the **real**
`CollapsedToolsSummary` + `ArtifactPanel` from a flat `FixtureEntry[]`
(`{label, call, envelope}`). It must cover, per family: success, error,
running (async), cancelled, write/approval, paged, and a generic fallback.
Add a fixture = append an entry; SQL fixtures additionally route through
`sqlToolResultsByScopedKey`. Tests are Playwright (`tests/e2e`,
`playwright.config.ts`); a spec visits `/dev/audit-trail` and asserts on the
rendered cards (`data-fixture-label`) and inspector zones.

---

## 8. Per-tool appendix

This appendix is the implementation checklist. It is manually maintained against
the authoritative registry in `server/internal/chat/tools/registry.go`, the
tool metadata manifest, and the tool-definition/output code in
`server/internal/chat/tools/*.go`. Keep it in sync when tool output shapes
change; `tests/e2e/audit-trail-inspectors.spec.ts` guards the rendered fixture
coverage.

Default rules for every row:

- **Request zone** renders the listed sent fields, with empty/omitted optional
  fields hidden unless they explain paging/filtering.
- **Response zone** renders the listed returned fields as structured content
  (`AutoTable`, `KeyValueGrid`, text/code block, or existing dedicated widget).
- **Metadata zone** always keeps `tool_call_id`, `duration_ms`, `integration`,
  `product`, `widget`, paging tokens, truncation flags, and a Raw disclosure.
- **Error** uses §2.4 unless the row calls out a provider-specific classifier.
- **Approval** uses §2.5. Approval rows show the pending action; they never claim
  the action already executed.

### 8.1 Local project files and git history

Source: local project storage plus local `git` CLI. No internet API.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner |
|---|---|---|---|---|---|
| `list_files` | `path?` | `files[]`, `total_count`, `note?` | folder/path + count | file tree/list, note badge | `ListFilesRenderer` |
| `read_file` | `path`, `max_bytes?`, `page_token?` | `path`, `offset`, `max_bytes`, `bytes_returned`, `has_more`, `content`, `next_page_token?` | path + bytes/truncated | syntax-highlighted file body with paging metadata | `ReadFileRenderer` |
| `search_files` | `query`, `case_sensitive?`, `limit?`, `page_token?`, `max_file_bytes?` | `query`, `matches[]`, `matches_count`, `has_more`, `next_page_token?`, truncation fields | query + match count | grouped match list/table by path and line | `SearchFilesRenderer` |
| `file_search` | `local_root`, `query`, `max_results?`, `include_globs?`, `exclude_globs?` | searched root, matches, snippets, omitted/skipped files, permission subject fields | root + query + match count | grouped local match list with approved-root badge | `SearchFilesRenderer`-style/generic |
| `local_read_file` | `local_root`, `path`, `max_bytes?`, `page_token?` | approved root/path, content window, byte counts, `has_more`, `next_page_token?` | local path + bytes/truncated | syntax-highlighted/paged body with approved-root metadata | `ReadFileRenderer`-style/generic |
| `list_git_repositories` | none | `repositories[]{repo_path,branch?,head?,remotes?}`, `total_count` | repo count | repository table with branch/head/remotes | generic or GitHub renderer when connector metadata present |
| `git_list_refs` | `repo_path?`, `limit?` | `repo_path`, `refs[]{object,short,refname,short_name,date,subject}`, `truncated` | repo + ref count | refs table, grouped branches/remotes/tags when possible | generic |
| `git_log` | `repo_path?`, `path?`, `query?`, `author?`, `since?`, `until?`, `all_refs?`, `max_count?` | `repo_path`, `commits[]`, `truncated` | repo/path + commits | commit table/timeline with subject, author, date, hash | generic |
| `git_search_history` | `repo_path?`, `query`, `regex?`, `path?`, `all_refs?`, `max_count?` | `repo_path`, `commits[]`, `mode`, `truncated` | search query + commit count | commit table with mode chip (`diff_text`/`diff_regex`) | generic |
| `git_grep` | `repo_path?`, `query`, `ref?`, `path?`, `regex?`, `case_sensitive?`, `max_matches?` | `repo_path`, `ref`, `matches[]`, `matches_count`, `truncated` | query + matches | match table `{path,line,content}` with ref chip | `SearchFilesRenderer`-style/generic |
| `git_show_commit` | `repo_path?`, `ref?`, `path?`, `max_bytes?`, `page_token?` | `repo_path`, `ref`, `path?`, `content`, `offset`, `has_more`, `next_page_token?`, `command_truncated?` | commit/ref + bytes | patch/stat code block with paging | `ReadFileRenderer`-style/generic |
| `git_show_file_at_ref` | `repo_path?`, `ref?`, `path`, `max_bytes?`, `page_token?` | `repo_path`, `ref`, `path`, `content`, `offset`, `has_more`, `next_page_token?`, `command_truncated?` | path@ref + bytes | syntax-highlighted historical file body | `ReadFileRenderer` |

### 8.2 Thread search, attachments, and artifacts

Source: local indexed store / artifact store.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner |
|---|---|---|---|---|---|
| `search_threads` | `query`/`q`, `limit?`, `status?`, `project_id?`, `thread_ids?`, `artifact_ids?`, `message_ids?`, time filters, `order_by?`, `min_score?`, snippet/file limits | `query`, `intent`, ranked `results[]`, optional file matches, counts | query + hit count | search result list with title/snippet/score/kind | `SearchThreadsRenderer` |
| `search_docs` | `q`/`query`, same filters minus current-project files by default | ranked metadata-only hits | query + hit count | same as `search_threads`; show alias in metadata | `SearchThreadsRenderer` |
| `get_doc` | `doc_id`, `max_chars?` | selected indexed document metadata/content/snippet | doc title/id | document preview + metadata grid | generic/search renderer |
| `get_artifact` | `artifact_id` | artifact descriptor, content/metadata summary | artifact title | descriptor grid + preview by type where safe | type renderer/generic |
| `list_messages` | `thread_id`, `limit?`, `page_token?`, explicit sequence/time/id filters, `order_by?` | `messages[]`, `count`, `page_size`, `order_by`, `has_more`, `next_page_token?` | thread + message count | message table/timeline | generic |
| `inspect_attachment` | `artifact_id`, `max_preview_bytes?` | file metadata, text/extraction preview, extraction summary | file name + size | metadata grid + preview/extraction blocks | generic |
| `read_attachment` | `artifact_id`, `max_bytes?`, `page_token?` | `file_name`, `mime_type`, `offset`, `bytes_returned`, `has_more`, `content`, `source`, `next_page_token?` | file name + bytes | paged text block, source badge | `ReadFileRenderer`-style/generic |
| `search_attachment` | `artifact_id`, `query`, `case_sensitive?`, `limit?`, `max_file_bytes?` | `file_name`, `query`, `matches[]`, `matches_count`, `search_truncated`, `source` | query + match count | match list with source badge | generic |
| `export_thread` | none | `thread_id`, `file_name`, `message_count`, `artifact_count`, `mime_type` + file artifact | export file name | file/download artifact with bundle stats | `FileRenderer` |
| `get_artifact_chunk` | `artifact_id`, `max_bytes?`, `offset?`, `page_token?`, `include_image_data?` | descriptor + `content`, `offset`, `bytes_returned`, `byte_size`, `has_more`, `next_page_token?`, image omission note | artifact + byte window | paged content block; image omission note when present | generic/type renderer |
| `search_artifact_content` | `artifact_id`, `query`, `case_sensitive?`, `limit?`, `page_token?`, `max_bytes?` | descriptor + `matches[]`, `count`, scanned byte counts, `has_more`, `next_page_token?`, `note?` | query + match count | match table/list + scanned/truncated stats | generic |
| `get_json_artifact` | `artifact_id`, `path?`, `limit?`, `offset?`, `page_token?`, `columns?`, `max_bytes?` | selected `value`/`rows`, `path`, `offset`, `limit`, `has_more`, `next_page_token?`, parse/truncation fields | artifact/path + row count | object grid or table slice; columns projection in metadata | generic |

### 8.3 CSV, dataset artifacts, charts, and interactive artifacts

Source: local project files and persisted artifacts.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner |
|---|---|---|---|---|---|
| `describe_dataset` | `path` | CSV profile: `path`, `row_count`, `columns[]`, inferred types, null/numeric/string stats, samples | CSV path + rows/cols | dataset profile summary and column stats | `DescribeDatasetRenderer` |
| `sample_rows` | `path`, `count?`, `offset?`, `page_token?`, `random?` | `columns[]`, `rows[]`, `total_rows`, `page_size`, `offset`, `has_more`, `next_page_token?` | CSV path + rows returned | table sample; random/sequential badge | generic/table |
| `filter_csv` | `path`, `column`, `operator`, `value?`, `limit?`, `page_token?` | `columns[]`, `rows[]`, `total_matches`, `page_size`, `offset`, `has_more`, `next_page_token?` | filter summary + matches | filtered table and predicate grid | generic/table |
| `aggregate_csv` | `path`, `group_by?`, `metrics[]` | `results[]`, `group_count` | path + group count | aggregate table with metric chips | `AggregateCsvRenderer` |
| `inspect_dataset_artifact` | `artifact_id`, `sample_rows?` | artifact descriptor, `row_count`, `column_count`, profiled `columns[]`, `sample_rows[]`, usage note | artifact + rows/cols | profile summary + sample table | generic |
| `get_dataset_rows` | `artifact_id`, `limit?`, `offset?`, `page_token?`, `columns?`, `filters?`, `sort?` | `rows[]`, row counts, paging, projected/missing columns | artifact + page window | table page with filters/sort metadata | generic/table |
| `aggregate_dataset` | `artifact_id`, `group_by?`, `metrics?`, `filters?`, `sort?`, `limit?` | `rows[]`, `row_count`, `filtered_row_count`, `group_count`, `group_by`, `metrics`, `group_capped`, `has_more` | aggregation + groups | aggregate table + metric/filter chips | generic/table |
| `search_dataset_rows` | `artifact_id`, `query`, `columns?`, `case_sensitive?`, `limit?`, `page_token?`, `filters?` | `rows[]`, `total_matches`, `matches_returned`, `searched_columns`, `has_more`, `next_page_token?` | query + match count | matching rows table | generic/table |
| `get_echarts_capabilities` | optional capability filters/version fields | ECharts capability JSON | "ECharts capabilities" | compact capability tree/raw disclosure | generic |
| `get_highcharts_capabilities` | optional capability filters/version fields | Highcharts capability JSON | "Highcharts capabilities" | compact capability tree/raw disclosure | generic |
| `build_chart_artifact` | chart engine/spec, dataset/artifact references, transforms, title/options | chart artifact + `plot_spec`, `chart_engine`, budget/preview metadata | chart title + engine | chart preview (`DataVisualization`) + budget/error diagnostics | `DataVisualization` / chart renderer |
| `create_interactive_artifact` | HTML/JS/CSS or artifact instructions/title | sandboxed HTML/file artifact metadata | artifact title | sandboxed iframe + raw/code disclosure | `SandboxedHtmlRenderer` |
| `create_interactive_data_artifact` | dataset rows/schema + interactive artifact spec | dataset-backed sandboxed artifact + retrieval metadata | artifact title + rows | sandboxed iframe + dataset retrieval metadata | `SandboxedHtmlRenderer` |
| `display_table` | rows/columns/title/description | data artifact with rows/schema/preview/full artifact metadata | table title + rows | table/visualization; full-artifact affordance when preview-only | `DataVisualization` |

### 8.4 SQL and catalog tools

Sources: BigQuery REST `jobs.query`, `jobs.getQueryResults`, `tabledata.list`;
Databricks Statement Execution; Trino client protocol; Snowflake SQL API.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `run_bigquery_query` | `query`/`sql`, description/context, project/dataset/location options, dry-run/limits as supported | rows or async running payload, `query_metadata{provider,bigquery,status,sql,job_id,location,schema,row counts,total_bytes_processed,cache_hit,duration_ms,provider_job_url}` | BigQuery logo + query title/status/progress | Query zone + live explorer/running/error state | `QueryInspector` / BigQuery REST |
| `list_bq_datasets` | project/location filters | `datasets[]` descriptors | BigQuery + dataset count | catalog table | `CatalogBrowserRenderer` |
| `list_bq_tables` | dataset/project, paging/filter options | `tables[]` descriptors | BigQuery + table count | catalog table | `CatalogBrowserRenderer` |
| `get_bq_table_schema` | project/dataset/table | schema fields + table metadata | table id + columns | schema table | `CatalogBrowserRenderer` |
| `run_databricks_query` | SQL, warehouse/http path/catalog/schema/timeout options | rows or async statement payload, `query_metadata{provider,statement_id,status,schema,row counts,duration_ms,provider_job_url}` | Databricks logo + query status/progress | Query zone + live explorer/running/error state | `QueryInspector` / Databricks Statement Execution |
| `list_databricks_catalogs` | workspace/warehouse filters | `catalogs[]` | Databricks + count | catalog table | `CatalogBrowserRenderer` |
| `list_databricks_schemas` | `catalog_name` | `schemas[]` | Databricks + count | schema table | `CatalogBrowserRenderer` |
| `list_databricks_tables` | `catalog_name`, `schema_name` | `tables[]` | Databricks + count | table list | `CatalogBrowserRenderer` |
| `describe_databricks_table` | `catalog_name`, `schema_name`, `table_name` | columns/properties/table metadata | table id + columns | schema/detail grid | `CatalogBrowserRenderer` |
| `run_trino_query` | SQL, catalog/schema/session options, timeout/limits | rows or async query payload, `query_metadata{provider,id,status,schema,stats,next_uri,row counts,duration_ms}` | Trino logo + status/progress | Query zone + explorer/running/error state | `QueryInspector` / Trino protocol |
| `list_trino_catalogs` | none/connection options | `catalogs[]` | Trino + count | catalog table | `CatalogBrowserRenderer` |
| `list_trino_schemas` | `catalog` | `schemas[]` | Trino + count | schema table | `CatalogBrowserRenderer` |
| `list_trino_tables` | `catalog`, `schema` | `tables[]` | Trino + count | table list | `CatalogBrowserRenderer` |
| `describe_trino_table` | `catalog`, `schema`, `table` | `columns[]` and table metadata | table id + columns | schema/detail grid | `CatalogBrowserRenderer` |
| `run_snowflake_query` | SQL, database/schema/warehouse/role options, timeout/limits | rows or async statement payload, `query_metadata{provider,statementHandle,status,resultSetMetaData,row counts,duration_ms,statementStatusUrl}` | Snowflake logo + status/progress | Query zone + explorer/running/error state | `QueryInspector` / Snowflake SQL API |
| `list_snowflake_databases` | optional pattern/limit | `databases[]` | Snowflake + count | database table | `CatalogBrowserRenderer` |
| `list_snowflake_schemas` | database/pattern/limit | `schemas[]` | Snowflake + count | schema table | `CatalogBrowserRenderer` |
| `list_snowflake_tables` | database/schema/pattern/limit | `tables[]` | Snowflake + count | table list | `CatalogBrowserRenderer` |
| `describe_snowflake_table` | database/schema/table | columns/table metadata | table id + columns | schema/detail grid | `CatalogBrowserRenderer` |
| `check_query_status` | `query_id`/`job_id`, provider, owner token | status metadata, progress, `ui_suppress` while internal | normally hidden; if visible, status only | running/completed/error diagnostic | `QueryInspector` / lifecycle |
| `get_query_results` | provider, query/job/statement id, page/window (`offset`/`page_token`/`limit`) | `rows[]`, `schema`, paging, counts, `query_metadata`; hidden when incomplete | data-page count | live explorer page | `QueryInspector` / lifecycle |
| `cancel_query` | provider + query/job/statement id | cancellation status/result metadata | provider + cancelled | cancelled banner + failed/request metadata | `QueryInspector` / lifecycle |

### 8.5 Atlassian: Jira and Confluence

Sources: Jira Cloud REST v3 and Confluence Cloud REST v2 through the local
browser-authenticated bridge.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `atlassian_status` | none | auth/session/product availability | Atlassian status | status grid | generic / bridge |
| `atlassian_check_connection` | none/product filters | connection check result | Atlassian connection | connection grid + diagnostics | generic / bridge |
| `jira_get_issue` | issue key/id, field expansion options | issue fields (`summary,status,assignee,type,priority,labels,url`) | issue key + status | issue detail grid | generic / Jira issue API |
| `jira_get_issue_history` | issue key/id, paging | changelog/history items | issue key + event count | timeline/table | generic / Jira changelog |
| `jira_get_issue_comments` | issue key/id, paging/order | comments with author/body/created | issue key + comment count | comment timeline | generic / Jira comments |
| `jira_get_issue_attachments` | issue key/id | attachments metadata | issue key + attachment count | attachment table | generic / Jira attachments |
| `jira_read_attachment` | attachment id/name or artifact reference | attachment bytes/preview or image artifact | attachment name | file/image preview | type renderer/generic |
| `jira_get_issue_link_types` | none | link types | "Jira link types" | link-type table | generic / Jira issue links |
| `jira_find_users` | query/account filters | users with `accountId`, display/avatar | user query + count | user table; accountId copy affordance | generic / Jira users |
| `jira_search_issues` | JQL/search text, fields, paging | issues + paging token/total | JQL/query + issue count | issue table | generic / Jira search |
| `jira_find` | natural query/JQL helper args | matching issues/pages depending bridge | query + count | result table/list | generic / bridge |
| `jira_rest_get` | REST path/query | raw Jira response map | method/path | structured response + Raw | generic / Jira REST |
| `jira_add_comment` | issue key/id, comment/body | approval payload `{action,target,content}` | write badge + issue | pending comment approval | Atlassian approval card/generic |
| `jira_reply_to_comment` | issue/comment ids, body | approval payload | write badge + issue/comment | pending reply approval | Atlassian approval card/generic |
| `jira_add_related_tickets` | issue key/id, related keys/link type | approval payload | write badge + links | pending link approval | Atlassian approval card/generic |
| `jira_upload_attachment` | issue key/id + artifact/path/file info | approval payload/file summary | write badge + attachment | pending upload approval | Atlassian approval card/generic |
| `jira_update_issue` | issue key/id + field updates | approval payload | write badge + issue | pending field update approval | Atlassian approval card/generic |
| `jira_create_issue` | project/issue type/summary/description/fields | approval payload | write badge + project | pending issue-create approval | Atlassian approval card/generic |
| `confluence_search` | query/CQL/space/type/paging | pages/spaces results + `_links.next` | Confluence + result count | result list/table | generic / Confluence search |
| `confluence_open_page` | page id/url/title | page metadata/body/links | page title | page detail + body preview | generic / Confluence pages |
| `confluence_open_space` | space key/id | space metadata/pages | space key | space detail/table | generic / Confluence spaces |
| `confluence_rest_get` | REST path/query | raw Confluence response map | method/path | structured response + Raw | generic / Confluence REST |

### 8.6 Browser automation

Source: Playwright-backed local browser controller.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `cua_browser_status` | none | session/browser status, current url/title | browser status | status grid | generic / Playwright |
| `cua_browser_navigate` | `url`, wait options | url/title/status/load info | "Navigated to ..." | navigation grid | generic / Playwright `page.goto` |
| `cua_browser_snapshot` | optional selector/scope | accessibility/DOM snapshot, element refs, url/title | "Read page" + url | mono snapshot/tree + element count | generic / Playwright snapshot |
| `cua_browser_click` | selector/ref/text target, button/modifiers | action result + url/title | "Clicked ..." | target grid + resulting page state | generic / Playwright locator click |
| `cua_browser_type` | target + text, clear/submit options | action result + url/title | "Typed ..." | target/text summary + resulting page state | generic / Playwright fill/type |
| `cua_browser_press` | key + target? | action result + url/title | key pressed | target/key grid | generic / Playwright press |
| `cua_browser_wait` | time/state/selector options | wait result | wait label | wait criteria/result grid | generic / Playwright waits |
| `cua_browser_back` | wait options | url/title/status | "Went back" | navigation grid | generic / Playwright goBack |
| `cua_browser_logs` | filters/limit? | logs `{level/type,text,url,status,timestamp}` | log count | logs table | generic / Playwright console/network logs |
| `cua_browser_clear_logs` | none | cleared count/status | "Cleared logs" | clear result grid | generic |

### 8.7 GitHub

Sources: GitHub REST (repositories, pulls, issues, Actions) and GitHub CLI for
raw `gh` command execution.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `github_status` | none | auth/settings/status | GitHub status | status widget | `GitHubToolRenderer` |
| `github_check_connection` | none | connection health, login/scopes | GitHub connection | connection widget + diagnostics | `GitHubToolRenderer` |
| `github_list_repositories` | owner/visibility/search/paging options | `repositories[]`, `total_count`, `limit`, `truncated` | GitHub logo + repo count | repository table | `GitHubToolRenderer` / REST repos |
| `github_list_pull_requests` | repo selector, state, limit | `pull_requests[]`, counts | repo + PR count | PR table | `GitHubToolRenderer` / REST pulls |
| `github_get_pull_request` | repo selector + PR number | PR detail, checks/files/comments as available | PR number + state | PR detail widget | `GitHubToolRenderer` / REST pulls |
| `github_list_issues` | repo selector, state, labels, limit | `issues[]`, counts | repo + issue count | issue table | `GitHubToolRenderer` / REST issues |
| `github_get_issue` | repo selector + issue number | issue detail/comments as available | issue number + state | issue detail widget | `GitHubToolRenderer` / REST issues |
| `github_list_workflow_runs` | repo selector, branch/status/event filters | `workflow_runs[]`, counts | repo + run count | workflow run table | `GitHubToolRenderer` / Actions runs |
| `github_get_workflow_run_jobs` | repo selector + run id | jobs/steps/status | run id + job count | jobs table | `GitHubToolRenderer` / Actions jobs |
| `github_clone_repository` | repository, project_id, name?, branch?, sparse_paths?, partial_clone?, depth?, timeout? | approval payload, then clone result after approval | write badge + repo | pending clone approval/result | `GitHubToolRenderer` / GitHub + local git |
| `github_pull_repository` | project_id, repo_path/repository, timeout? | approval payload/result | write badge + repo | pending pull approval/result | `GitHubToolRenderer` / local git |
| `github_refresh_repository` | project_id, repo_path/repository?, best_effort?, timeout? | approval payload/result(s) | write badge + repo/all | pending refresh approval/result table | `GitHubToolRenderer` / local git |
| `github_run_gh_command` | `args[]`, repository selectors, timeout, project_id for writes | read-only `gh` output or approval payload for writes | `$ gh ...` + status | command, stdout/stderr, approval when mutating | `GitHubToolRenderer` / GitHub CLI |

Internal approval execution IDs are not exposed as model-callable tools, but
they can appear in approval bookkeeping and logs after the user approves a
pending GitHub write. They inherit the card and inspector treatment of the
agent-facing tool that queued them:

| Internal operation ID | Queued by | Display as |
|---|---|---|
| `github_clone_repository_execute` | `github_clone_repository` | approved clone result in the GitHub approval widget |
| `github_pull_repository_execute` | `github_pull_repository` | approved pull result in the GitHub approval widget |
| `github_refresh_repository_execute` | `github_refresh_repository` | approved refresh result table in the GitHub approval widget |
| `github_raw_gh_command_execute` | mutating `github_run_gh_command` | approved `$ gh ...` command result in the GitHub command widget |

### 8.8 Slack

Source: Slack Web API through the Slack manager.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `slack_status` | none | auth/settings/status | Slack status | status grid | generic / Slack API |
| `slack_check_connection` | none | connection health/team/user/scopes | Slack connection | connection grid + diagnostics | generic / Slack API |
| `slack_list_conversations` | types/limit/cursor filters | conversations + `response_metadata.next_cursor` | conversation count | conversation table | generic / `conversations.list` |
| `slack_get_conversation` | channel id/name | conversation info/topic/purpose | channel name | conversation detail grid | generic / `conversations.info` |
| `slack_get_conversation_members` | channel id, limit/cursor | members/user ids + cursor | channel + member count | chip list/table | generic / `conversations.members` |
| `slack_get_messages` | channel id, oldest/latest/limit/cursor | messages + cursor | channel + message count | messages table `{ts,user,text}` | generic / `conversations.history` |
| `slack_get_thread_replies` | channel id, thread ts, limit/cursor | replies + cursor | thread + reply count | replies table/timeline | generic / `conversations.replies` |
| `slack_get_user` | user id/email/name | user/profile/team info | user display name | profile grid | generic / `users.info` |
| `slack_search_messages` | query, sort, count/page | matches/messages + paging | query + match count | matches table with permalinks | generic / `search.messages` |
| `slack_download_file` | file id/url/name | local path/file metadata | file name | file descriptor/download path | generic / Slack files |
| `slack_post_message` | channel, text/blocks/thread/broadcast options | approval payload | write badge + channel | pending post approval | generic approval / `chat.postMessage` |
| `slack_upload_file` | channel/file/artifact info, title/comment/thread | approval payload | write badge + file/channel | pending upload approval | generic approval / external upload API |

### 8.9 SharePoint

Source: Microsoft Graph/SharePoint connection through the SharePoint manager.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `sharepoint_status` | none | auth/settings/status | SharePoint status | status grid | generic / SharePoint |
| `sharepoint_check_connection` | none | connection health/site/user/scopes | SharePoint connection | connection grid + diagnostics | generic / SharePoint |
| `sharepoint_search` | query, site/path filters, limit | search results with names, URLs, size, modified metadata | query + result count | result table/list with source URLs | generic / SharePoint search |
| `sharepoint_get_metadata` | `item_url` or `server_relative_url` | file/folder metadata, ids, source URL | file name | metadata grid + native/source links | generic |
| `sharepoint_read` | `item_url` or `server_relative_url`, byte/page options | promoted file artifact/text preview, `artifact_id`, `remote_id`, `source_url` | file name + bytes/pages | native file/text viewer | `FileRenderer`/generic |
| `sharepoint_summarize` | `item_url` or `server_relative_url`, summary options | summary, citations/source metadata, artifact id when produced | file name + summary | summary block + source metadata | generic |
| `sharepoint_download` | `item_url` or `server_relative_url` | app-managed local path or file artifact metadata, source URL | file name | native file/download artifact | `FileRenderer` |

### 8.10 arXiv

Source: arXiv API/download cache plus local paper text extraction.

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `arxiv_status` | none | cache/index/status | arXiv status | status grid | generic |
| `arxiv_check_connection` | none | network/cache health | arXiv connection | connection grid | generic |
| `arxiv_search` | query/categories/authors/ids, limit/sort | papers with id/title/authors/summary/links | query + paper count | ranked paper list/table | generic |
| `arxiv_screening_report` | search/screening criteria, paper refs | screening report, decisions, evidence | report title + counts | report sections and paper table | arXiv audit renderer/generic |
| `arxiv_recommendations` | seed papers/query/preferences | recommended papers + rationale | recommendation count | ranked list with reasons | generic |
| `arxiv_list_papers` | cache filters | downloaded/cached paper list | paper count | paper table | generic |
| `arxiv_get_paper` | paper/arxiv id | metadata, local/cache state, source URL | arXiv id + title | metadata grid + links | generic |
| `arxiv_get_paper_text` | paper/arxiv id, window/page options | text window, page metadata, `artifact_id`/`remote_id` | paper + text window | code/text-native viewer | `ReadFileRenderer`-style |
| `arxiv_read_paper` | paper/arxiv id, pages/window options | page text, coverage, extracted pages | paper + pages | text/code viewer with page chips | `ReadFileRenderer`-style |
| `arxiv_download_paper` | arxiv id | file artifact/path, `artifact_id`, `remote_id`, `source_url` | arXiv id + file | native PDF/file viewer | `FileRenderer` |

### 8.11 Provider-hosted model capabilities

Source: model provider adapters. These are catalogued separately from
`AllToolMetadata()` and execute inside the provider request.

| Capability | Sent | Returned | Collapsed card | Inspector response | Owner/source |
|---|---|---|---|---|---|
| `openai-responses/web_search` | provider-native web search args/query | `web_search_call` status/action/results/citations | OpenAI Responses + query/status | query, result titles/URLs/snippets, provider ids | provider-hosted inspector |
| `openai-responses/code_interpreter` | provider-native code/container args | code-interpreter status, stdout/stderr/files/container ids when streamed | OpenAI Responses + execution status | code output/files/sandbox metadata | provider-hosted inspector |
| `anthropic-messages/web_search` | provider-native web search args/query | `server_tool_use` + `web_search_tool_result` content | Anthropic + query/status | query, result titles/URLs/snippets, provider ids | provider-hosted inspector |
| `anthropic-messages/code_execution` | provider-native code-execution args | code-execution tool result content/stdout/files when streamed | Anthropic + execution status | execution output/files/sandbox metadata | provider-hosted inspector |

### 8.12 RLM

| Tool | Sent | Returned | Collapsed card | Inspector response | Owner |
|---|---|---|---|---|---|
| `rlm_exec` | Starlark program, inputs/context/evidence budget | stdout/stderr-like transcript, evidence, final answer, `query_metadata` budget fields/errors | command label + status | stdout/evidence/final answer panels | `RlmExecRenderer` |

## 9. Open gaps / future work

- Per‑connector error **classifiers** (Slack scope, Jira mention, Trino syntax)
  feeding `extractToolErrorDiagnostic` for richer Why/Try‑this without backend
  changes.
- Bespoke Atlassian/Slack panel widgets (parity with the GitHub widget set) if
  the generic `ValueBlock` proves too coarse for common shapes.
- Optional migration of the Query/GitHub headers onto `InspectorShell` once it
  has proven stable, to collapse the last duplicated header markup.
