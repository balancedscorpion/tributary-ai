# Atlassian Browser MCP Integration Plan

Date: 2026-05-13

## Goal

Integrate the `JIRA-BROWSER` prototype into the app as a first-class Jira and Confluence tool integration while preserving its core security and authentication model:

- One user-driven Atlassian SSO login.
- One persistent local Playwright browser profile shared by Jira and Confluence.
- Browser-authenticated REST reads for Jira and Confluence.
- Approval-gated Jira writes.
- Per-product tool exposure toggles so the model only sees enabled tools.
- Optional headful automation for advanced troubleshooting.
- Rich chat widgets for issues, pages, searches, and approvals.

## Architecture

The production app keeps Go as the orchestration boundary and uses a local Node/Playwright bridge for browser automation.

```text
React Settings / Chat
  -> Go HTTP API /api/integrations/atlassian/*
  -> server/internal/atlassian.Manager
  -> local Node bridge server/internal/atlassian/bridge.mjs
  -> Playwright persistent Chromium profile
  -> Atlassian Jira/Confluence REST endpoints using browser SSO cookies
```

The agent tool path is:

```text
optionc.Manager
  -> tools.Registry Atlassian tools
  -> atlassian.Manager.CallTool
  -> bridge.mjs /tool/{name}
  -> browser-authenticated REST navigation/fetch
```

## Settings Model

`AppSettings.atlassian` stores non-secret integration configuration:

- `jiraBaseUrl`
- `confluenceBaseUrl`
- `defaultJiraProjectKey`
- `allowedProjects`
- `allowedAuthDomains`
- `browserProfileDir`
- `jira.enabled`
- `jira.showBrowser`
- `confluence.enabled`
- `confluence.showBrowser`

No Atlassian passwords, tokens, API keys, or cookies are stored in settings. Cookies remain in the local Playwright profile directory.

## Tool Exposure

The model receives Atlassian tool definitions only when enabled:

- Shared tools are exposed when either Jira or Confluence is enabled:
  - `atlassian_status`
  - `atlassian_check_connection`
- Jira tools are exposed only when `atlassian.jira.enabled` is true:
  - `jira_get_issue`
  - `jira_get_issue_history`
  - `jira_get_issue_comments`
  - `jira_get_issue_attachments`
  - `jira_read_attachment`
  - `jira_get_issue_link_types`
  - `jira_find_users`
  - `jira_search_issues`
  - `jira_find`
  - `jira_rest_get`
  - `jira_add_comment`
  - `jira_reply_to_comment`
  - `jira_add_related_tickets`
  - `jira_upload_attachment`
  - `jira_update_issue`
  - `jira_create_issue`
- Confluence tools are exposed only when `atlassian.confluence.enabled` is true:
  - `confluence_search`
  - `confluence_open_page`
  - `confluence_open_space`
  - `confluence_rest_get`

Execution also checks enablement server-side, so stale provider tool calls cannot bypass the toggles.

## Approval Model

Read tools execute directly. Jira write tools only queue an approval:

- `jira_add_comment` records issue key and exact comment body.
- `jira_reply_to_comment` records issue key, parent comment ID, and exact reply body.
- `jira_add_related_tickets` records target issue, related issue keys, link type, and optional comment.
- `jira_upload_attachment` records target issue, source (`artifact_id`, project `path`, or inline content), filename, MIME type, size, checksum, and a bounded text preview where possible.
- `jira_update_issue` records target issue, edited fields, exact Markdown description updates, labels, assignee account ID, priority, issue type, and any accountId-backed mentions.
- `jira_create_issue` records project key, issue type, summary, and description.

The chat UI renders an approval widget with target and content. Jira comment and description previews render Markdown in the widget, while the bridge converts supported Markdown to Jira ADF before sending to the REST API. The user can approve or reject via:

- `POST /api/integrations/atlassian/approvals/{id}/approve`
- `POST /api/integrations/atlassian/approvals/{id}/reject`

Approval execution calls the bridge’s internal write tools. The model cannot approve or reject its own queued action.

## Frontend Work

Settings adds an `MCPs` tab with an Atlassian Browser MCP panel:

- Jira and Confluence base URLs.
- Default and allowed Jira projects.
- Shared Connect, Check, and Finish Login actions.
- Per-product model exposure toggles.
- Per-product advanced “show browser during automation” toggles.
- Browser profile directory and auth domain allowlist.

Chat rendering adds Atlassian widgets for:

- Jira issue and issue-search results.
- Confluence pages, page searches, and spaces.
- Approval cards for Jira writes.

## Cross-Platform Notes

The bridge uses Node and Playwright Chromium, which work on macOS and Windows. Go starts `node server/internal/atlassian/bridge.mjs` without shell-specific process management. Paths are passed as command arguments and resolved with Go/Node path APIs.

The user must have Node dependencies installed and Playwright Chromium available:

```bash
npm install
npx playwright install chromium
```

## Verification

Implemented checks:

- `node --check server/internal/atlassian/bridge.mjs`
- `npm run build`
- `go test ./internal/chat/optionc`

Full-suite note: one full `go test ./...` run hit a transient temp directory cleanup race in `internal/chat/optionc`; rerunning that package passed.
