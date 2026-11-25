import React from 'react';
import { ArrowRight, History } from 'lucide-react';
import { registerInspectorResponseWidget } from '../inspector/responseWidgets';
import type { InspectorResponseWidgetProps } from '../inspector/responseWidgets';
import { formatTimestamp } from '../inspector/values';
import { JiraIssueCard, extractJiraIssueFromResult } from './IssueCard';

/**
 * Inspector response widget for jira_get_issue — the audit-overlay companion
 * to the chat-side Jira issue card. The full ToolInspector shell (Request
 * zone, identity chips, Raw disclosure) stays generic; this only claims the
 * Response zone: the issue card with the ADF description rendered richly,
 * plus the changelog the bridge ships alongside it.
 */

function isObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export interface ChangelogEntry {
  created: string;
  author: string;
  items: Array<{ field: string; from: string; to: string }>;
}

export function normalizeChangelog(value: unknown): ChangelogEntry[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter(isObject)
    .map((history) => ({
      created: typeof history.created === 'string' ? history.created : '',
      author:
        (isObject(history.author) && typeof history.author.displayName === 'string'
          ? history.author.displayName
          : '') || '',
      items: (Array.isArray(history.items) ? history.items : [])
        .filter(isObject)
        // Jira's changelog item field is literally named `toString`; the cast
        // plus typeof guard keeps TS from resolving Object.prototype.toString
        // and rejects the inherited method when the own property is absent.
        .map((item: any) => ({
          field: typeof item.field === 'string' ? item.field : '',
          from: typeof item.fromString === 'string' ? item.fromString : '',
          to: typeof item.toString === 'string' ? item.toString : '',
        }))
        .filter((item) => item.field && (item.from || item.to)),
    }))
    .filter((entry) => entry.items.length > 0);
}

const MAX_CHANGELOG_ENTRIES = 8;

export function ChangelogList({ entries }: { entries: ChangelogEntry[] }) {
  const shown = entries.slice(0, MAX_CHANGELOG_ENTRIES);
  const hidden = entries.length - shown.length;
  return (
    <div>
      <h4 className="mb-1.5 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wide text-muted-foreground/60">
        <History size={11} aria-hidden />
        Recent changes
      </h4>
      <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
        {shown.map((entry, i) => (
          <li
            key={`${entry.created}-${i}`}
            className="flex flex-col gap-1 px-3 py-2 text-[12px]"
          >
            {entry.items.map((item, j) => (
              <span key={j} className="flex flex-wrap items-center gap-1.5 min-w-0">
                <span className="font-mono text-muted-foreground">{item.field}</span>
                <span className="text-foreground [overflow-wrap:anywhere]">
                  {item.from || '—'}
                </span>
                <ArrowRight size={11} aria-hidden className="shrink-0 text-muted-foreground/60" />
                <span className="sr-only">to</span>
                <span className="text-foreground [overflow-wrap:anywhere]">{item.to || '—'}</span>
              </span>
            ))}
            {(entry.author || entry.created) && (
              <span className="text-[11px] text-muted-foreground">
                {entry.author}
                {entry.author && entry.created && ' · '}
                {entry.created && (
                  <time dateTime={entry.created}>
                    {formatTimestamp(entry.created) || entry.created}
                  </time>
                )}
              </span>
            )}
          </li>
        ))}
      </ul>
      {hidden > 0 && (
        <p className="mt-1 text-[11px] text-muted-foreground/60">
          Showing the {shown.length} most recent changes — full changelog under Metadata → Raw
        </p>
      )}
    </div>
  );
}

const JiraIssueInspectorResponse: React.FC<InspectorResponseWidgetProps> = ({ result }) => {
  const issue = extractJiraIssueFromResult(result);
  const changelog = normalizeChangelog(isObject(result) ? result.changelog : null);
  if (!issue) return null; // unreachable: the matcher requires an extractable issue
  return (
    <div className="flex max-w-[640px] flex-col gap-4">
      <JiraIssueCard issue={issue} bordered />
      {changelog.length > 0 && <ChangelogList entries={changelog} />}
    </div>
  );
};

registerInspectorResponseWidget({
  match: (meta, result) => {
    const resultMeta = isObject(meta.result_metadata) ? meta.result_metadata : {};
    const isJiraIssue =
      String(meta.tool_name || '') === 'jira_get_issue' ||
      (String(resultMeta.product || meta.product || '') === 'jira' &&
        String(resultMeta.widget || meta.widget || '') === 'issue');
    return isJiraIssue && extractJiraIssueFromResult(result) !== null;
  },
  component: JiraIssueInspectorResponse,
});

export default JiraIssueInspectorResponse;
