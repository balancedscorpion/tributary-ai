// Renderer registry - import all renderers to trigger registration side effects

// Type fallback renderers
import './type/DataRenderer';
import './type/CodeRenderer';
import './type/ImageRenderer';
import './type/FileRenderer';
import './type/SandboxedHtmlRenderer';

// Tool-specific renderers
import './tool/ReadFileRenderer';
import './tool/SearchFilesRenderer';
import './tool/SearchThreadsRenderer';
import './tool/ListFilesRenderer';
// Local-machine file tools (file_search, local_list_folder + grant twins).
// Exact tool-name registrations, so these win before ToolInspector's generic
// connector claim and render as a first-class local file result list.
import './tool/LocalFilesRenderer';
import './tool/AggregateCsvRenderer';
import './tool/DescribeDatasetRenderer';
// Query inspector must register before ErrorRenderer so query failures route
// through the Query Tool Inspector's error branch (which keeps the SQL block
// visible) instead of the generic error fallback.
import './tool/query/QueryToolRenderer';
import './tool/ErrorRenderer';
import './tool/CatalogBrowserRenderer';
import './tool/RlmExecRenderer';
// Unified persistent-approvals card. Registered before GitHubToolRenderer so
// new-shape approval envelopes (result.approval with category/options) render
// through the shared ApprovalCard; legacy approval payloads fall through to
// the connector renderers below.
import './tool/ApprovalRenderer';
import './tool/GitHubToolRenderer';
import './tool/arxiv/ArxivToolRenderer';
// Generic connector-aware inspector — registered LAST so every more-specific
// resolver (Query, GitHub, SandboxedHtml, Error) claims first. It backstops
// every remaining tool (Slack, Atlassian, Browser, web_search, future tools)
// with the shared three-zone inspector instead of a raw-JSON dump.
import './tool/inspector/ToolInspector';
// Response widgets rendered INSIDE the generic inspector's Response zone
// (tool/inspector/responseWidgets.tsx). They don't claim the artifact — they
// upgrade the Response zone of the shared three-zone shell, so the Request
// zone, identity chips, and Raw disclosure stay generic.
import './tool/jira/JiraIssueResponse';
// The rest of the Jira family (status/connection, history, comments,
// attachments, link types, users, search, find, rest, approvals and write
// results) — registered AFTER the dedicated issue widget so that keeps
// winning the `issue` payloads.
import './tool/jira/JiraResponses';

// Re-exports
export { resolveRenderer } from './registry';
export type { RendererProps, RendererEntry } from './types';
