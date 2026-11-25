import React from 'react';
import { registerInspectorResponseWidget } from '../inspector/responseWidgets';
import type { InspectorResponseWidgetProps } from '../inspector/responseWidgets';
import { JiraWidgetForKey, resolveJiraWidgetKey } from './widgets';

/**
 * Inspector response-widget registration for the whole Jira family.
 *
 * jira_get_issue keeps its dedicated registration (JiraIssueResponse.tsx,
 * imported FIRST in components/renderers/index.ts so it wins the `issue`
 * widget); this entry claims every other Jira/Atlassian payload — status,
 * connection, history, comment_list, attachment_list, attachment,
 * issue_link_types, user_list, issue_list, find, rest, approval and
 * write_result — so the full-window ToolInspector Response zone shows the
 * rendered widget by default. Raw JSON stays behind Metadata → Raw, and
 * unknown payloads (`generic`) fall through to the inspector's structured
 * ValueBlock fallback.
 */

function isObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function isJiraFamily(meta: Record<string, any>): boolean {
  const resultMeta = isObject(meta.result_metadata) ? meta.result_metadata : {};
  const product = String(resultMeta.product ?? meta.product ?? '').toLowerCase();
  const toolName = String(meta.tool_name || '');
  return /^(jira|atlassian)_/.test(toolName) || product === 'jira' || product === 'atlassian';
}

const JiraInspectorResponse: React.FC<InspectorResponseWidgetProps> = ({ result, meta }) => (
  <JiraWidgetForKey widgetKey={resolveJiraWidgetKey(meta)} raw={result} meta={meta} />
);

registerInspectorResponseWidget({
  match: (meta) => {
    if (!isJiraFamily(meta)) return false;
    const key = resolveJiraWidgetKey(meta);
    // `issue` is owned by JiraIssueResponse (registered before this entry);
    // if that matcher declined (no extractable issue) the generic fallback is
    // the honest render, so never double-claim it here. `generic` keeps the
    // inspector's structured ValueBlock fallback.
    return key !== 'generic' && key !== 'issue';
  },
  component: JiraInspectorResponse,
});

export default JiraInspectorResponse;
