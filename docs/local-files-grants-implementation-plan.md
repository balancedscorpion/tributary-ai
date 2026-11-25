# Local Files Grants Implementation Plan

## Purpose

This is the implementation contract for roadmap item 2:
local filesystem grants and permission UI.

The goal is that a user can say "find this file on my machine" and the model
can request access without asking the user to type a path. The user remains the
only actor that chooses the local folder or file. The application then converts
that picker choice into a bounded read-only grant that the agent can use.

Remote files are not local grants. Requests such as "find this in SharePoint" or
"find this JIRA attachment" should go through connector permissions and
`remote_resource` subjects, while still using the same approval-card language
and native file rendering system.

## UX Pattern

Use a chat-first, picker-backed, grant-based pattern.

1. The model calls an intent-level local tool:

   ```json
   {
     "query": "Q3 forecast deck",
     "location_hint": "Downloads",
     "file_types": ["presentation"],
     "max_results": 20
   }
   ```

2. The app shows an inline approval card:

   ```text
   Allow local file search?

   The assistant wants to search for:
   "Q3 forecast deck"

   Location:
   Choose a folder on this computer

   Access:
   Read-only search handled by the local file search agent
   Sensitive files excluded by default

   [Choose folder...] [Don't allow]
   ```

3. `Choose folder...` opens the native OS picker. No path text box appears in
   normal UX.

4. After the picker returns, the card becomes concrete:

   ```text
   Allow search in Documents?

   Path: ~/Documents
   Includes subfolders
   Excludes credentials and sensitive files
   Used by: Local file search agent
   Tools: file search, read selected result

   Scope:
   [Once] [This chat] [Always allow]

   [Allow] [Choose different folder] [Don't allow]
   ```

5. The run remains open while the approval is pending. It resumes only after the
   user approves or denies.

The core rule is: the model may request local access by intent, but the user
chooses the local filesystem location.

## User-Facing Surfaces

### Chat Approval Card

The chat approval card must:

- Show the assistant's search intent, not a model-provided filesystem path.
- Show a native `Choose folder...` or `Choose file...` action when no concrete
  grant has been selected.
- Show the selected display path only after the user chooses it.
- Explain read-only scope, subfolder behavior, sensitive excludes, and which
  agent/tool will use the grant.
- Offer `Once`, `This chat`, and `Always allow`.
- Provide `Choose different folder` before approval.
- Return a non-fatal tool result on denial.
- Keep the run pending until approved or denied.

### Settings Permissions

Settings should expose this under `Permissions > Read > Local files`.

It should show:

- Added locations created through chat or Settings.
- Pending local access requests.
- Per-location state: `Off`, `Ask`, `Always allow`.
- Per-tool state under each location for `file_search`, `local_read_file`, and
  internal grant tools when shown to advanced users.
- Scope and provenance badges: `Built in`, `This device`, `Added by you`,
  `Connected`, or `Attached`.
- Last used, source (`chat` or `settings`), excludes, revoke, change folder, and
  test actions.
- A picker-backed `Add folder` action.
- A picker-backed test action. The test UI must never expose `local_root` as a
  normal text input.

### Remote Connectors

Connector-backed files should not create local grants. They should use:

- `connector` or `tool_set` subjects for broad connector capability.
- `remote_resource` subjects for targetable resources such as SharePoint files,
  JIRA issues, JIRA attachments, Slack files, or app artifacts.
- Connector provenance badges so the user understands whether a tool came from a
  connected account, built-in app capability, or an attachment.

## Tool Shape

### Model-Facing Local Tool

`local_root` is not part of the model-facing schema.

```json
file_search({
  "query": "string",
  "location_hint": "string, optional",
  "file_types": ["string"],
  "max_results": "number, optional"
})
```

`local_read_file` may accept a result `path` or `file_ref`, but the approved root
is injected by the backend. The model should not choose a root path.

### Internal Search-Agent Tools

Internal tools receive application-supplied grants only:

```json
local_search_command({
  "grant_id": "string",
  "program": "auto | rg | grep | fd | find",
  "query": "string",
  "relative_path": "string, optional",
  "args": ["validated search flags only"],
  "file_types": ["string"],
  "max_results": "number, optional"
})
```

```json
local_search_in_grant({
  "grant_id": "string",
  "query": "string",
  "relative_path": "string, optional",
  "file_types": ["string"],
  "max_results": "number, optional"
})
```

```json
local_read_grant_file({
  "grant_id": "string",
  "file_ref": "string",
  "page_token": "string, optional",
  "max_bytes": "number, optional"
})
```

The backend resolves `grant_id` to a canonical local root and injects the hidden
`local_root` execution argument. The local search agent cannot create approval
requests, approve requests, write files, invoke a shell, choose an executable,
or broaden its root. The execute-class `local_search_command` broker can launch
only bundled/resolved `rg` and `fd` directly with validated argv and fixed bounds.

The scoped tool surface for this agent is `Registry.LocalGrantToolDefinitions()`.
It must return only `local_list_grant_folder`, `local_search_command`,
`local_search_in_grant`, and `local_read_grant_file`. These
tools stay registered for execution and metadata/testing, but they must not be
advertised through the default model, analysis-core, or general search
definition surfaces.

## Permission Model

Use `local_location` permission subjects for local filesystem grants:

```json
{
  "kind": "local_location",
  "local_root": "/canonical/root",
  "excludes": ["**/.env", "**/.ssh/**", "**/id_*"]
}
```

Each durable rule should include enough metadata for Settings:

- `grant_id` or durable rule ID.
- Display name.
- Canonical root and display root.
- Grant kind: `folder` or `file`.
- Include-subfolders setting.
- Excludes.
- Source: `chat` or `settings`.
- Scope: `once`, `thread`, or `global`.
- Last used.
- Revoked/deleted state.
- Platform bookmark/security-scoped token if the OS requires it.

Every local operation must re-check:

- Rule still exists and is allowed.
- Root canonicalization and symlink resolution.
- Path containment inside the approved root.
- Traversal attempts.
- Sensitive excludes.
- Revocation.
- Tool category remains read-only.

## Backend Implementation Plan

1. Keep `local_root` as an internal execution detail only.
   The public schemas for `file_search` and `local_read_file` should stay
   intent-based. Generated metadata and Settings tool tests must not show
   `local_root`.

2. Support approval subject override after picker selection.
   A pending `local_location` approval starts abstract. When the user chooses a
   folder, the approve route accepts the selected `subject`, canonicalizes it,
   and mints the durable rule from that selected location.

3. Inject approved roots at execution time.
   `optionc.Manager` resolves saved local grants or newly approved selections
   and injects the hidden root into local execution args.

4. Resolve grant IDs for internal search-agent tools.
   Internal tools must fail closed when a grant ID is unknown, revoked, denied,
   or not in scope for the current run/thread.

5. Expose a dedicated local-grant tool-definition surface.
   The orchestration layer should use `Registry.LocalGrantToolDefinitions()` for
   the designated local file search agent. Do not assemble this surface by hand
   in provider or manager code, and do not expose these tools to the main model.

6. Keep runs open during approval.
   Pending local approvals must not let the model produce a final answer until
   the approval is resolved. Denial returns a tool result and allows the model to
   continue with alternatives.

7. Preserve native rendering.
   Local search results should return stable file references, grant IDs, display
   paths, MIME/type hints, preview metadata, and `tool_presentation` metadata so
   existing file/code/search viewers can render without copying files.

8. Keep remote files on connector subjects.
   SharePoint, JIRA, Slack, attachments, and artifacts should normalize into
   `remote_resource` or connector/tool-set subjects. Do not tunnel them through
   local grant code.

## Frontend Implementation Plan

1. Use one approval-card component for both chat and approval queue rendering.
   The same visual treatment should be used before and after approval, including
   loading and disabled states.

2. Add a native picker service.
   Prefer the Tauri dialog plugin in desktop builds. Keep bridge or backend
   fallbacks for development, but the primary UX is native folder/file picking.

3. Replace local path inputs with picker controls.
   Settings, tool testing, and chat approvals should use `Choose folder...` or
   `Choose file...`. Any raw path entry belongs only in developer/debug tooling.

4. Show provenance explicitly.
   Local grants should read as `This device` or `Added by you`; connector tools
   should read as `Connected`; built-in tools should read as `Built in`; thread
   uploads should read as `Attached`.

5. Make hierarchy editable at every level.
   The same `Off / Ask / Always allow` control should work globally, per tool
   set, per local location, and per atomic tool.

6. Make collapsed state auditable.
   Local location rows and tool-set rows should show whether children are all
   off, all ask, all always, or mixed.

7. Add picker-backed tool tests.
   Local tool tests should require a selected folder before running and should
   pass the chosen root only as an internal request detail.

## Testing Plan

### Backend

- Approval subject override creates a canonical `local_location` rule.
- Subject override is rejected for non-local approval types.
- Rootless `file_search` creates a pending local approval.
- Approved local selection injects the root into tool execution args.
- A single saved local grant can satisfy a rootless local request.
- Multiple matching saved grants ask instead of guessing.
- Internal grant tools resolve known grant IDs and deny unknown IDs.
- Symlink escape, traversal, revoked grants, and sensitive excludes fail closed.
- `local_root` remains absent from generated public tool metadata.
- Internal grant tools have bounded schemas and correct presentation metadata.

### Frontend and E2E

- Chat local search request shows a picker-backed approval.
- Selecting a folder updates the approval card before final approval.
- Approving resumes the same run.
- Denying returns a non-fatal tool result.
- `Always allow` skips the second prompt for the same granted location.
- Revoking a grant forces a new prompt.
- Settings `Add folder` uses a picker-backed flow.
- Tool tests for local search/read use a folder picker.
- No normal UI shows a `local_root` text field.
- Tool-set and per-tool permission controls remain editable as `Off`, `Ask`, or
  `Always allow`.

## Rollout Checklist

- Public model schemas are intent-based.
- Native picker works in the desktop shell.
- Approval requests persist and survive reload.
- Run lifecycle waits for approval resolution.
- Settings can list, edit, test, and revoke grants.
- Search/read execution re-checks containment on every call.
- Local result rendering works through native viewers.
- Remote connector file access remains connector-scoped.
- Contract tests and E2E tests are green.
