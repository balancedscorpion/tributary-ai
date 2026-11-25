# Atlassian Browser MCP Integration

Date: 2026-05-13

## Summary

Jira and Confluence are integrated through a local Playwright Atlassian browser session. The implementation follows the `JIRA-BROWSER` repository’s pattern: the user signs in once through SSO, the app reuses the same persistent browser profile, and Jira/Confluence reads use Atlassian REST endpoints through the authenticated browser session.

The bridge launches bundled Playwright Chromium for that profile. This keeps Atlassian automation deterministic across platforms and avoids probing OS default or enterprise-managed browser installs.

The integration is intentionally local. It does not introduce Atlassian API tokens, personal access tokens, basic auth, or environment-variable credentials.

## Components

- `server/internal/atlassian/manager.go`
  - Starts and supervises the local Node bridge.
  - Uses the source checkout bridge script in development and materializes the embedded bridge script from the app config directory in release binaries.
  - Pushes sanitized app settings to the bridge.
  - Exposes connection, login, tool-call, and approval methods to Go handlers/tools.
- `server/internal/atlassian/bridge.mjs`
  - Owns the Playwright persistent browser context.
  - Launches bundled Playwright Chromium for login and hidden automation.
  - Performs Jira and Confluence browser-authenticated REST reads.
  - Performs approved Jira writes using the same browser-authenticated session.
- `server/internal/atlassian/bridge_embed.go`
  - Embeds `bridge.mjs` into Go release builds so standalone binaries do not depend on `server/internal/atlassian/bridge.mjs` being present beside the executable.
- `server/internal/chat/tools/atlassian_tools.go`
  - Defines agent-visible Jira and Confluence tools.
  - Emits `tool_progress` updates while browser-backed commands start, navigate, search, read, or queue approvals.
  - Queues approval records for Jira write tools.
- `server/internal/chat/optionc/atlassian_filter.go`
  - Filters model-visible tools per Jira/Confluence toggle.
  - Adds Atlassian safety guidance to the system prompt when enabled.
- `components/settings/McpIntegrationsTab.tsx`
  - Settings UI for connection, URLs, exposure toggles, and headful automation.
- `components/chat/AtlassianToolResult.tsx`
  - Rich chat widgets for Jira, Confluence, and approval results.
- `components/chat/CollapsedToolsSummary.tsx`
  - Renders browser/computer-use activity as human-readable steps instead of raw function names.
  - Shows search terms, summarized responses, and clickable source links for web/Jira/Confluence search results.
- `hooks/useChat.ts`
  - Preserves non-query tool progress providers such as `atlassian` so browser activity does not get mislabeled as a database query.

## Connection Flow

1. User opens Settings -> MCPs.
2. User enters Jira and Confluence base URLs.
   - Bare Atlassian Cloud hosts such as `your-company.atlassian.net` are normalized to `https://your-company.atlassian.net`.
   - Confluence Cloud hosts with no path are normalized to `https://your-company.atlassian.net/wiki`.
3. User clicks `Connect`.
4. Go starts the local Node bridge if needed.
5. The bridge opens a visible bundled Playwright Chromium window at the Jira base URL.
6. User completes Atlassian SSO.
7. The bridge detects return to the configured Atlassian tenant.
8. The visible browser closes and the same persistent profile reopens in hidden mode using the same browser runtime.
9. The bridge checks configured Jira and Confluence URLs.

The same local browser profile applies to both Jira and Confluence.

## Tool Behavior

Read tools return JSON envelopes with `result_metadata.integration = "atlassian"` so the frontend can render specialized widgets.

Browser-backed tools also stream activity updates into the same chat timeline. The UI translates these updates into concise labels such as opening a Jira issue, searching Confluence, reading a REST response, or waiting for write approval. Search tools display the submitted search term and the returned result summary. When a result includes URLs, the UI renders those URLs as clickable source attribution links.

Write tools return approval envelopes instead of mutating Jira. The approval card shows:

- Action.
- Target issue, target comment, project, related issues, or attachment metadata.
- Exact comment, reply, summary, description, edited fields, issue type, related-ticket relationship, or upload preview.
- A Markdown-rendered preview of the content that will be converted to Jira Atlassian Document Format (ADF).
- Approve and Reject buttons.

Only the approval API can execute queued writes.

Jira comment writes intentionally separate top-level comments from replies:

- `jira_add_comment` is only for new top-level comments.
- `jira_get_issue_comments` reads comment IDs, authors, text, and stable comment URLs so the model can identify the target comment before replying.
- `jira_reply_to_comment` queues a reply-style write with the parent comment ID. The bridge sends Jira's `parentId` field where supported, stores reply metadata as a Jira comment property, and includes a native link to the parent comment in the posted ADF body as a durable fallback.
- `jira_find_users` resolves display names or email addresses to Jira `accountId` values. Comment, reply, create, and update tools accept a `mentions` array; matching `@Display Name`, `<@accountId>`, or explicit `mentionText` text is converted to native Jira mention nodes in ADF. Plain `@name` text is rejected before approval when no matching accountId-backed mention is supplied.

Issue edits are approval-gated:

- `jira_update_issue` queues approval to update summary, description, issue type, assignee, priority, labels, or project-specific/custom fields.
- Description updates are converted from Markdown to ADF, including accountId-backed native Jira mentions.

Related tickets are handled as Jira issue links:

- `jira_get_issue` includes existing `linkedIssues` and attachment metadata.
- `jira_get_issue_link_types` reads tenant issue link types.
- `jira_add_related_tickets` queues approval to link one or more issues to the target issue, defaulting to Jira's `Relates` link type.

Jira file uploads are first-class:

- `jira_get_issue_attachments` lists attachment metadata for a ticket.
- `jira_read_attachment` reads bounded text-like attachment content, or returns binary metadata unless bounded base64 is explicitly requested. Successful attachment downloads are also written to an app-managed local download cache under the Atlassian state directory and return `local_path`, `local_relative_path`, `download`, `sha256`, and byte-size metadata so the file is findable and reusable.
- `jira_upload_attachment` queues approval to upload a current-thread uploaded file by `artifact_id`, a project file by `path`, an app-managed downloaded file by `local_path`, or generated `contentText`/`contentBase64` content.
- `local_path` upload sources are restricted to app-managed download directories such as `atlassian/downloads/` and cannot reference arbitrary files or secrets elsewhere on disk. Slack upload tools use the same resolver and guardrail.

The bridge accepts Markdown for Jira write bodies and converts supported structures to ADF before calling Jira REST. Supported conversions include paragraphs, hard breaks, headings, fenced code blocks, tables, bullet lists, ordered lists, block quotes, links, bold, emphasis, and inline code.

## Security Boundaries

- Atlassian content is untrusted. The system prompt tells the model not to treat Jira/Confluence content as instructions.
- Browser session cookies stay in the configured local profile directory.
- The bundled browser profile path is local settings only; no browser-auth env vars are required.
- Atlassian URLs are constrained to the configured tenant host.
- Jira REST paths are limited to `/rest/api/3` and `/rest/agile/1.0`.
- Confluence REST paths are limited to `/api/v2` and `/rest/api` under the configured Confluence base path.
- Jira issue/project access can be scoped with `allowedProjects`.
- The model cannot approve or reject queued writes.

## Runtime Requirements

Local development uses the repository Node dependencies and Playwright package. Install the bundled Chromium fallback for development:

```bash
npm install
npx playwright install chromium
```

The bridge is started by Go with `node` directly and does not require shell-specific commands, so the process model works on macOS and Windows.

Release downloads are bundled app archives, not bare single-file executables. Each archive contains:

- `ai-analyst` or `ai-analyst.exe`.
- `runtime/node` with the target-platform Node.js runtime.
- `runtime/atlassian/node_modules/playwright` with the Playwright package used by the bridge.
- `runtime/ms-playwright` with target-platform Chromium used for Atlassian browser automation.

The Go binary embeds the bridge script and writes it to the local Atlassian state directory before launching Node. At startup it prefers the bundled `runtime/` sidecars next to the executable, then falls back to local development dependencies. The browser runtime uses bundled Chromium for a deterministic automation environment. Keep `runtime/` beside the executable when moving or installing the app.

The generic Computer Use Browser MCP setting in Settings -> MCPs reuses the same bundled Node.js, Playwright package, and Chromium browser sidecars. Its bridge is separate from Atlassian and uses its own persistent browser profile, but it follows the same runtime lookup and macOS quarantine-removal path.
