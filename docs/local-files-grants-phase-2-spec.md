# Local Files Grants Phase 2 Spec

## Goal

Local file access should feel like asking a trusted desktop assistant to look
somewhere, not like configuring a filesystem API. The user can say "find this
file on my machine"; the model requests access by intent; the application asks
the user to choose a file or folder with a native picker; and the backend turns
that choice into a scoped read grant.

The model must never ask the user to type a local path, invent a path, or choose
the approval location itself.

## UX Pattern

### Chat Approval

For a local search intent, the model calls:

```json
{
  "query": "Q3 forecast",
  "location_hint": "Documents",
  "file_types": ["spreadsheet"]
}
```

The model-facing tool has no `local_root`. The chat shows an inline approval:

```text
Allow local file search?

Search: "Q3 forecast"
Hint: Documents
Location: Choose a folder on this computer

Read-only search handled by the local file search agent.
Hidden files and credential files are excluded by default.

[Choose folder] [Don't allow]
```

After the picker returns a concrete folder:

```text
~/Documents
Read-only. Includes subfolders except protected paths.

[Once] [This chat] [Always allow]
[Allow] [Choose different folder] [Don't allow]
```

For exact-file reads, the same card uses "Choose file" and `local_kind: "file"`:

```text
Location: Choose a file on this computer
Only the selected file can be read.
```

For folder inventory, the model calls the list tool instead of pretending a
search result is a directory listing:

```json
{
  "path": "reports",
  "recursive": false,
  "max_entries": 100
}
```

The approval card uses the same picker-backed local folder flow and describes
the operation as "List files in a local folder".

### Settings

Settings exposes this as `Permissions > Read > Local files`.

The section shows:

- Pending local requests, using the same approval card as chat.
- Added locations: folders and exact files chosen through native pickers.
- Per-location `Off / Ask / Always allow`.
- Scope, source, last used, test, change, revoke.
- Tool summary for `file_search`, `local_list_folder`, `local_read_file`, and
  internal grant tools.

There is no local path text box in the normal UX. Settings has `Choose folder`
and `Choose file` actions. Tool tests use the same picker pattern.

## Permission Model

Local grants use the existing `local_location` permission subject with an
explicit kind:

```json
{
  "kind": "local_location",
  "local_root": "/Users/example/Documents",
  "local_kind": "folder",
  "excludes": [".env", "**/.ssh/**"]
}
```

```json
{
  "kind": "local_location",
  "local_root": "/Users/example/Documents/Q3 forecast.xlsx",
  "local_kind": "file"
}
```

Rules:

- `local_kind` defaults to `folder` for backward compatibility.
- Folder grants match descendants after canonicalization and symlink checks.
- File grants match only the exact canonical file.
- Local roots are hidden execution details, not model-facing arguments.
- The user-selected subject override is only accepted for `local_location`
  approvals.
- Remote connectors must use `remote_resource` subjects, not local grants.

## Tool Shape

Model-facing:

```ts
file_search({
  query: string,
  location_hint?: string,
  file_types?: string[],
  max_results?: number
})
```

```ts
local_list_folder({
  path?: string,
  recursive?: boolean,
  max_entries?: number
})
```

Internal execution-only fields:

```ts
{
  local_root: string,
  local_kind: "folder" | "file",
  grant_id?: string
}
```

Search-agent tools:

```ts
local_list_grant_folder({
  grant_id: string,
  relative_path?: string,
  recursive?: boolean,
  max_entries?: number
})
```

```ts
local_search_command({
  grant_id: string,
  program: "auto" | "rg" | "grep" | "fd" | "find",
  query: string,
  relative_path?: string,
  args?: string[],
  file_types?: string[],
  regex?: boolean,
  case_sensitive?: boolean,
  max_results?: number,
  timeout_ms?: number,
  max_file_bytes?: number
})
```

```ts
local_search_in_grant({
  grant_id: string,
  query: string,
  relative_path?: string,
  file_types?: string[],
  max_results?: number
})
```

```ts
local_read_grant_file({
  grant_id: string,
  file_ref: string,
  max_bytes?: number,
  page_token?: string
})
```

The main model gets only the intent tool. The designated local search agent gets
only scoped grant tools and cannot approve, broaden, write, or invoke arbitrary
processes. `local_search_command` is the sole execute-class capability: it maps a
closed program enum to direct `rg`/`fd` argv, rejects shell syntax and execution
hooks, fixes the working directory to one configured grant, and enforces output,
file-size, result, and time limits.

## Desktop Agent Primitive Review

Claude Code and Codex both expose the same practical first-class local-machine
primitives, even though the names and permission surfaces differ:

- Directory listing: Claude Code uses `ls` through `Bash`; Codex allows commands
  to list directories inside the approved sandbox.
- Name globbing: Claude Code has `Glob`; Codex agents commonly use `rg --files`,
  `find`, or project file-listing tools.
- Content search: Claude Code has `Grep`, backed by ripgrep semantics; Codex
  reaches for `rg` first.
- File read: Claude Code has `Read`; Codex has built-in file reads and shell
  commands within its granted filesystem scope.
- Shell execution: both can execute bounded shell commands, but raw shell is too
  broad for an end-user local-file permission UX.

The app should therefore model local desktop search as structured read-only
capabilities, not as arbitrary Bash:

1. `local_list_folder` / `local_list_grant_folder` for `ls`-style inventory.
2. `local_search_command` for direct, bounded `rg`/`grep` and `fd`/`find`
   semantics without a shell.
3. `file_search` / `local_search_in_grant` for the deterministic portable
   fallback and the public intent contract.
4. `local_read_file` / `local_read_grant_file` for bounded file reads.
   Supported structured/text files such as PDFs, spreadsheets, documents,
   HTML, JSON, and CSV return extracted readable text from the existing file
   pipeline when available, so the search agent can read a listed candidate
   without first copying it into the app.

The local search agent receives only the grant tools. The main model asks for
intent, the Local Computer connection supplies its preconfigured whole-machine
or selected-folder scope, the run manager injects `local_root` server-side, and
every tool re-checks traversal, symlinks, and sensitive excludes before returning
results. Local path access is not requested again in chat.

## Implementation Status

Implemented in this phase:

- Persistent approvals keep runs open until approved, denied, expired, or
  canceled.
- Chat approval subject override accepts picker-selected local locations.
- Native Tauri dialog support for folder and file selection.
- `local_kind` added to backend and frontend permission subjects.
- Folder grants remain backward compatible.
- Exact-file grants are supported and enforced.
- `file_search` can search a selected folder or a single selected file.
- `local_list_folder` can list a selected local folder without copying files
  into the app.
- `local_read_file` can read from a selected folder or a single selected file.
- Settings local grants can add/change/test/revoke file and folder grants.
- Tool tests hide `local_root`; local read tests use a file picker.
- Generated metadata and permissions UI include all local tools.
- Approved `file_search` calls now dispatch a bounded local search agent from
  the run manager instead of exposing the direct filesystem search as the public
  execution path.
- The local search agent uses the provider selected in the Providers settings
  through the existing provider factory, runs through `engine.RunAgentLoop` with
  isolated history/provider state, receives only `local_list_grant_folder`,
  `local_search_command`, `local_search_in_grant`, and
  `local_read_grant_file`, and emits parent-run
  `tool_progress` events while it lists, searches, reads, ranks, and completes.
- `local_search_command` supports validated `auto`, `rg`/`grep`, and `fd`/`find`
  modes. It never invokes a shell, accepts no arbitrary executable or path, and
  filters every returned canonical path back through grant membership and
  sensitive-file exclusions.
- The desktop release runtime and Docker image bundle ripgrep and fd. When the
  selected provider or native search runtime is unavailable, the existing Go
  walker remains the deterministic fallback rather than stalling the run.
- Windows whole-computer mode discovers every attached local fixed, removable,
  and RAM-disk volume through the Windows API instead of assuming `C:\\` or
  treating `/` as a valid root. Mapped network and optical drives are excluded;
  users can still add an explicit folder when they need a different scope.
- Windows `rg.exe` and `fd.exe` processes run directly with hidden console
  windows, a temporary profile, cancellation, and the same grant/output/time
  bounds. Release packaging requires native `.exe` files and runs a Windows
  search smoke test before building the installer.
- Local Computer setup creates read-only location grants plus an Always rule for
  this bounded command action. A later atomic Ask or Off override removes the
  command from the sub-agent surface and uses the portable fallback; it never
  broadens filesystem access.
- The sub-agent grant executor fails closed for every non-grant tool and injects
  `local_root`/`local_kind` server-side from the already-approved `file_search`
  args without calling the approval system again.
- Generated metadata and static fallback inventory include
  `local_folder_list` presentation metadata so folder snapshots are rendered as
  first-class local-file outputs.

## Remaining Work

### 1. Local Search Job Persistence And Polling

Implemented. The dispatch path records local search-agent jobs in durable chat
storage and keeps an in-memory overlay for active runs. The read-only status API
requires `thread_id` or `run_id` scope for list and one-job reads:

```text
GET /api/local-files/search-jobs?thread_id=...&run_id=...
GET /api/local-files/search-jobs/{jobId}?thread_id=...
```

JSON storage writes records under each thread. MySQL storage uses
`local_search_jobs`, added in migration `0014_local_search_jobs.sql`. The parent
run SSE stream remains the live progress surface; the job API is the reconnect
and recovery surface after reload or process restart.

### 2. Native Preview Stream Endpoint

Implemented. The local equivalent of artifact raw streaming is:

```text
GET /api/local-files/grants/{grantId}/file?ref=...
```

The endpoint must re-check:

- Grant exists and is not revoked.
- Scope is valid for the current user/session.
- `file_ref` is contained in the grant or exactly matches a file grant.
- Symlink traversal does not escape.
- MIME and size limits are enforced.

This powers native preview/rendering without copying the file into the app.

### 3. Renderer Upgrade

Implemented. Local search results render as a first-class local file result
list:

- File icon/type, display path, grant badge.
- Match snippets and match counts.
- Preview action using the stream endpoint.
- Reveal/open action only from explicit user click in desktop builds.
- Existing `SearchFilesRenderer` behavior remains compatible for project files.

### 4. Search Quality

Improve search implementation with:

- Optional macOS `mdfind -onlyin` fast path where available.
- WalkDir fallback.
- Wall-clock budget with partial results.
- Progress events during long scans.
- Size caps before content reads.

### 5. Sensitive File Policy

Folder grants exclude sensitive defaults. Exact-file grants currently mean the
user deliberately picked the exact file; if product wants to block selected
credential files too, add an explicit confirmation or rejection path instead of
silently skipping.

## Testing Requirements

Backend:

- Path canonicalization.
- Symlink escape rejection.
- Traversal rejection.
- Folder grant descendant matching.
- File grant exact-only matching.
- Revoked/unknown grant IDs fail closed.
- Sensitive excludes for folder grants.
- Approval denial returns non-fatal tool result.
- Run resumes only after approval resolution.
- Local search-agent surface is exactly scoped grant tools.
- Search-command schemas reject shell metacharacters, unknown flags, command
  hooks, traversal, symlink escapes, sensitive paths, oversized output, and
  canceled contexts.
- Folder-list tools do not expose `local_root` to the model-facing schema.
- Folder-list tools skip sensitive paths and reject traversal/symlink escapes.

Frontend/E2E:

- Chat approval is picker-backed before approval.
- Folder approval sends `local_kind: "folder"`.
- File grant add/change/test sends `local_kind: "file"`.
- Tool tests do not show `arg:local_root`.
- `file_search` test uses folder picker.
- `local_read_file` test uses file picker and auto-fills file ref.
- Always allow skips repeat prompts.
- Revoke forces a new prompt.
- Remote connectors never create local grants.

## Remote Connector Boundary

SharePoint, Jira, Slack, GitHub, attachments, and future remote file sources are
not local grants. They use their connector tools and permission subjects:

- `remote_resource` for connector files/attachments.
- `repository` for GitHub repositories.
- App-managed artifact/file subjects for uploaded files.

The approval UI can share visual grammar, but provenance must be explicit:
`Local`, `Attached`, `Connected: SharePoint`, `Connected: Jira`, etc.
