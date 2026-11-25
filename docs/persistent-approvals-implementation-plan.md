# Persistent Approvals Implementation Plan

## Objective

Create a persistent, first-class approvals and permissions system for chat tools, remote connectors, GitHub repository operations, and local file search. The system must make approvals clear in chat, durable in settings, grouped by Read, Write, and Execute, and suitable for a future local file search agent that searches approved locations without requiring files to be copied into the app.

## Implementation Order

This project should follow the agreed order:

1. Generalized persistent approvals.
2. Local filesystem grants and permission UI.
3. Local search/read/snapshot tools.
4. Unified file-result renderer and remote connector normalization.
5. GitHub repo browser and viewer integration.
6. Cleanup old copied-directory assumptions and dormant client-side file tools.

## Ownership

### Backend and integration owner

Codex owns:

- Backend permission model and service.
- Persistent rule and approval request storage.
- HTTP APIs.
- SSE/event integration with chat.
- Manager-level tool permission evaluation.
- Migration path for GitHub, Atlassian, and Slack approval flows.
- Local file search permission boundary and guard behavior.
- Verification, tests, and final integration review.

### Front-end and user experience owner

Fable owns:

- Approval user experience inside chat.
- Permissions Settings information architecture.
- Read, Write, Execute organization and labels.
- Shared approval card/dialog components.
- Pending approval queue design.
- Front-end API clients and reducer/state integration.
- Visual polish, empty/error/loading states, and accessibility.
- UX review of local file search permission prompts.

Fable may spin up sub-agents when useful, especially for isolated review passes, component audits, accessibility review, or settings information architecture.

## Requirements

### Product requirements

- Approvals must be shown per tool and per local search location.
- Settings must expose approvals as a first-class permissions system.
- Permissions must be grouped into Read, Write, and Execute.
- Chat must pause clearly when approval is required.
- Chat approval UI must add the request to a durable approval queue.
- The user must be able to approve or reject from chat.
- Approvals must survive reloads where appropriate.
- The model should receive a non-fatal tool result when approval is denied. While approval is still pending, the chat run remains open and no final assistant message is produced.
- Local files should not need to be copied if they are available locally.
- Local file access must be requested by location with clear disclosure of what will be searched.
- Remote files from connectors should be accessed normally through their connector tools.
- All file results must continue to render through the existing application viewing tools.
- GitHub repositories should be cloneable locally and viewable/searchable through the file pipeline.

### Security requirements

- Remote read tools remain controlled by connector activation/scope.
- Local filesystem reads are denied unless inside an approved location.
- Local paths must be normalized and symlink-resolved before access.
- Path traversal outside an approved root must be blocked.
- Sensitive locations and credential-like files must remain protected unless explicitly approved.
- Write tools default to Ask first.
- Execute tools default to Ask first.
- Broad always-allow execute grants should not be created. Execute grants must be scoped to a concrete tool/action/resource.
- Approval decisions must only be accepted through guarded HTTP routes.
- Tools and sub-agents must not be able to approve their own requests.
- The local file search agent must not be able to create new approval requests.

## Domain Model

### Permission categories

```go
type PermissionCategory string

const (
    PermissionRead    PermissionCategory = "read"
    PermissionWrite   PermissionCategory = "write"
    PermissionExecute PermissionCategory = "execute"
)
```

### Permission subjects

```go
type PermissionSubject struct {
    Kind       string   `json:"kind"` // tool | tool_set | connector | local_location | repository | remote_resource | provider_hosted_tool
    Tool       string   `json:"tool,omitempty"`
    Connector  string   `json:"connector,omitempty"`
    ToolSetID  string   `json:"tool_set_id,omitempty"`
    LocalRoot  string   `json:"local_root,omitempty"`
    Repository string   `json:"repository,omitempty"`
    RemoteKind string   `json:"remote_kind,omitempty"`
    RemoteID   string   `json:"remote_id,omitempty"`
    Operation  string   `json:"operation,omitempty"`
    Excludes   []string `json:"excludes,omitempty"`
}
```

Canonical subject kinds:

- `tool`: one concrete tool, optionally narrowed by connector, tool set, operation, or remote target.
- `tool_set`: every tool in a stable tool set, using `tool_set_id` and connector provenance where applicable.
- `connector`: connector-level capability or health access.
- `local_location`: one normalized local root plus the sensitive-file exclusion list.
- `repository`: one local or remote repository target.
- `remote_resource`: one connector-backed target such as an artifact, attachment, Jira issue, Slack channel/file/thread, SharePoint file, arXiv paper, or query job, identified by `remote_kind` and `remote_id`.
- `provider_hosted_tool`: one model-provider-hosted capability such as web search or code execution.

### Durable permission rules

```go
type PermissionRule struct {
    ID         string            `json:"id"`
    Category   PermissionCategory `json:"category"`
    Effect     string            `json:"effect"` // allow | ask | deny
    Scope      string            `json:"scope"`  // global | thread:<thread_id>
    Subject    PermissionSubject `json:"subject"`
    CreatedVia string            `json:"created_via"`
    CreatedAt  time.Time         `json:"created_at"`
    LastUsedAt *time.Time        `json:"last_used_at,omitempty"`
    ExpiresAt  *time.Time        `json:"expires_at,omitempty"`
}
```

Rules are durable policy. They should be visible, editable, and revokable in Settings.

### Approval requests

```go
type ApprovalRequest struct {
    ID         string            `json:"id"`
    ThreadID   string            `json:"thread_id"`
    RunID      string            `json:"run_id"`
    ToolCallID string            `json:"tool_call_id,omitempty"`
    Tool       string            `json:"tool"`
    Category   PermissionCategory `json:"category"`
    Operation  string            `json:"operation,omitempty"`
    Subject    PermissionSubject `json:"subject"`
    Summary    string            `json:"summary"`
    Preview    map[string]any    `json:"preview,omitempty"`
    Options    []string          `json:"options"` // once | thread | always
    Status     string            `json:"status"`  // pending | approved | rejected | expired | canceled
    Decision   *ApprovalDecision `json:"decision,omitempty"`
    CreatedAt  time.Time         `json:"created_at"`
    ExpiresAt  time.Time         `json:"expires_at"`
}
```

Approval requests are live questions. Approving a request may mint a durable rule, depending on the selected scope.

### Approval decisions

```go
type ApprovalDecision struct {
    Action    string    `json:"action"` // approve | reject
    Scope     string    `json:"scope"`  // once | thread | always
    DecidedAt time.Time `json:"decided_at"`
}
```

## Backend Architecture

### New package

Add `server/internal/permissions`.

Primary service:

```go
type Service struct {
    rules    RuleStore
    requests ApprovalRequestStore
    audit    AuditStore
}
```

Expected methods:

```go
Evaluate(ctx context.Context, input EvaluationInput) (EvaluationResult, error)
Request(ctx context.Context, input RequestInput) (ApprovalRequest, error)
Await(ctx context.Context, approvalID string, timeout time.Duration) (ApprovalDecision, error)
Approve(ctx context.Context, approvalID string, scope string) (ApprovalRequest, error)
Reject(ctx context.Context, approvalID string) (ApprovalRequest, error)
ListRules(ctx context.Context, filter RuleFilter) ([]PermissionRule, error)
CreateRule(ctx context.Context, input RuleInput) (PermissionRule, error)
UpdateRule(ctx context.Context, id string, input RuleUpdate) (PermissionRule, error)
DeleteRule(ctx context.Context, id string) error
ListApprovals(ctx context.Context, filter ApprovalFilter) ([]ApprovalRequest, error)
```

### Rule storage

Store durable rules in local application config, not environment variables.

Suggested path:

```text
<app_config_dir>/permissions/rules.json
```

Use atomic writes and restrictive file permissions.

### Approval request storage

Approval requests are tied to threads and runs, so they should be persisted in the chat store. Extend `server/internal/chat/store.Store` and implement JSON and MySQL parity:

```go
CreateApprovalRequest(ctx context.Context, approval ApprovalRequest) error
GetApprovalRequest(ctx context.Context, approvalID string) (ApprovalRequest, error)
ListApprovalRequests(ctx context.Context, filter ApprovalFilter) ([]ApprovalRequest, error)
DecideApprovalRequest(ctx context.Context, approvalID string, decision ApprovalDecision) (ApprovalRequest, error)
CancelRunApprovalRequests(ctx context.Context, runID string) error
ExpireStaleApprovalRequests(ctx context.Context, now time.Time) error
```

### Evaluation precedence

1. Explicit deny.
2. Thread-scoped rule.
3. Global rule.
4. Built-in default.

Built-in defaults:

- Read: allow for enabled remote connector reads, ask for local filesystem locations.
- Write: ask.
- Execute: ask.

Connector scope and permission are separate:

- Connector scope determines whether a tool is visible or available to the model.
- Permission rules determine whether an available tool can execute without asking.

## Tool Integration

Tool metadata lives in the canonical product manifest, not next to each tool as
a second minimal struct. Local registered tools must be represented in
`server/internal/chat/tools/tool_metadata.go` with `ToolMetadata`, including
permission category, origin, tool set, connector id, presentation metadata, and
settings test metadata. Provider-hosted capabilities must be represented in
`server/internal/chat/tools/provider_hosted_tool_metadata.go` and exposed
through the `provider_hosted_tool` permission subject.

The manager should evaluate permission before tool execution:

```text
allow -> execute tool
deny  -> return non-fatal permission_denied tool result
ask   -> create approval request, emit approval event, wait or defer
```

Existing connector-specific queueing should migrate gradually:

1. GitHub clone/pull/raw-gh approvals.
2. Atlassian write approvals.
3. Slack post/upload approvals.

Compatibility routes can stay during migration.

## Chat and SSE

Add a persisted SSE event:

```json
{
  "type": "approval",
  "approval": {
    "id": "apr_...",
    "state": "pending",
    "category": "write",
    "tool": "jira_create_issue",
    "tool_call_id": "call_...",
    "summary": "Create Jira issue TEAM-123",
    "options": ["once", "thread", "always"],
    "subject": {},
    "preview": {}
  }
}
```

While a run is blocked on approval, emit periodic `tool_progress` with:

```json
{
  "phase": "approval_required",
  "approval_id": "apr_...",
  "message": "Waiting for approval"
}
```

Timeout behavior:

- Blocking approval wait should be bounded.
- On timeout, keep the run in an approval-waiting state and continue heartbeat/progress events. Do not produce a final assistant message while the required permission is unresolved.
- The approval request remains pending until the user approves, rejects, or cancels the run.

Cancel behavior:

- Canceling a run cancels pending blocking approvals for that run.
- Deferred approvals may remain pending only if they can still execute safely outside the run.

## HTTP API

Add unified routes:

```text
GET    /api/permissions/rules
POST   /api/permissions/rules
PATCH  /api/permissions/rules/{id}
DELETE /api/permissions/rules/{id}

GET    /api/approvals
POST   /api/approvals/{id}/approve
POST   /api/approvals/{id}/reject
```

Approve payload:

```json
{
  "scope": "once"
}
```

Valid scopes:

- `once`
- `thread`
- `always`

## Front-End UX

### Chat approval experience

When approval is required:

- The relevant tool card must stay expanded.
- The approval card should show category, tool, destination, exact operation, and preview.
- The primary action should approve the narrowest safe scope.
- Secondary actions should include reject and scope selection where appropriate.
- The user should never need to understand internal tool names to make the decision.
- Denied and approved states should remain visible in transcript history.

### Settings permissions experience

Add a first-class Permissions settings area:

- Pending
- Read
- Write
- Execute
- Audit

Rows should expose:

- Subject.
- Category.
- Effect.
- Scope.
- Last used.
- Created via.
- Revoke/edit controls.

Local file locations should show:

- Folder path.
- Whether subfolders are included.
- Excluded sensitive paths.
- Scope.
- Last searched/read.
- Revoke action.

### Shared components

Create or refactor toward:

- `ApprovalCard`
- `ApprovalScopeSelector`
- `PermissionsSettings`
- `PermissionRuleRow`
- `PendingApprovalsQueue`

Existing GitHub, Jira, and Slack approval widgets should use the shared approval contract.

## Local File Search Agent

The main model receives a single `file_search` tool.

`file_search` responsibilities:

1. Parse requested search intent and locations.
2. Ask for local folder access if needed.
3. Snapshot approved roots for the search dispatch.
4. Start a designated local file search agent.
5. Return structured file results.

The local search agent may only receive restricted local capabilities. In the
current registry, those model-facing capabilities are `file_search` for scoped
local search and `local_read_file` for approved file reads. If implementation
later adds internal-only helpers for folder listing or content search, they
must remain behind the same approved `local_location` subject and must not be
exposed as broad global filesystem access.

The agent must not be able to:

- Create approval requests.
- Approve requests.
- Access paths outside the approved roots.
- Use write tools or arbitrary execute tools. The only execute-class exception
  is `local_search_command`, a no-shell broker that accepts a closed
  `rg`/`grep`/`fd`/`find` program enum, validated search flags, and an
  application-injected configured grant.

Every local filesystem operation must re-check:

- Normalized absolute path.
- Symlink-resolved path.
- Approved root membership.
- Sensitive deny/ask rules.
- Active revocation state.

## Verification Plan

### Backend tests

- Rule evaluation precedence.
- Approval request lifecycle.
- Approve once does not create a durable rule.
- Approve thread creates thread-scoped rule.
- Approve always creates global rule.
- Reject returns non-fatal denial.
- Timeout leaves the run in an approval-waiting state with no final assistant
  message while the required permission remains unresolved.
- Cancel run cancels blocking pending approvals.
- JSON and MySQL store parity.
- Local path traversal is blocked.
- Symlink escape is blocked.
- Revocation affects subsequent filesystem operations.

### Front-end tests/manual checks

- Approval appears in chat while run is waiting.
- Tool group remains expanded while approval is pending.
- Approve/reject updates UI state.
- Settings lists pending approvals.
- Settings groups rules under Read, Write, Execute.
- Revoking a rule removes future auto-approval.
- Local file search prompt clearly explains the folders being searched.
- Permission UI works at narrow desktop and mobile widths.

### Integration checks

- GitHub clone approval still works through old and new routes during migration.
- Jira create/update/comment approval still works.
- Slack post/upload approval still works.
- SSE replay shows approval state after refresh.
- Existing file rendering remains intact.

## Completion Criteria

The implementation is complete only when:

- The implementation order above has concrete code support through the persistent approval foundation.
- Chat can create, display, decide, and replay approval requests.
- Settings exposes persistent permission rules grouped by Read, Write, and Execute.
- GitHub, Atlassian, and Slack approval flows have either migrated to the shared system or have compatibility wrappers backed by it.
- The local file search permission boundary is designed and implemented enough for future file search agent work to use it safely.
- Tests and manual verification prove the behavior, rather than only confirming compilation.
