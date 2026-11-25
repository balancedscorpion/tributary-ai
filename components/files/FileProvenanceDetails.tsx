import React from 'react';
import { Check, Copy, ExternalLink } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { fileSourceBadgeClass, isHttpUrl, type FileArtifactProvenance } from '@/lib/fileProvenance';
import { formatAbsoluteDateTime, formatBytes, formatRelativeTime } from '@/lib/formatters';
import { cn } from '@/lib/utils';

// tool_args is connector-controlled and untyped: keys that duplicate the
// managed/stored paths add noise, and anything credential-shaped must never
// reach the audit UI.
const TOOL_ARG_SKIP_KEYS = /^(local_?path|path|file_?path|download_?path|managed_?local_?path)$/i;
const TOOL_ARG_SECRET_KEYS = /token|secret|auth|password|cookie|signature|credential|api_?key/i;
const TOOL_ARG_MAX_ENTRIES = 6;
const TOOL_ARG_MAX_VALUE_LENGTH = 80;

export interface ToolArgEntry {
  key: string;
  value: string;
  fullValue: string;
}

export function formatToolArgEntries(
  toolArgs: Record<string, unknown> | undefined,
  duplicateValues: Array<string | undefined> = [],
): { entries: ToolArgEntry[]; hiddenCount: number } {
  if (!toolArgs) return { entries: [], hiddenCount: 0 };
  const duplicates = new Set(duplicateValues.filter(Boolean).map((value) => String(value)));
  const entries: ToolArgEntry[] = [];
  let hiddenCount = 0;
  for (const [key, raw] of Object.entries(toolArgs)) {
    if (TOOL_ARG_SKIP_KEYS.test(key.trim())) continue;
    if (TOOL_ARG_SECRET_KEYS.test(key)) continue;
    if (raw === null || raw === undefined) continue;
    let fullValue: string;
    if (typeof raw === 'object') {
      try {
        fullValue = JSON.stringify(raw);
      } catch {
        continue;
      }
    } else {
      fullValue = String(raw);
    }
    fullValue = fullValue.trim();
    if (!fullValue || duplicates.has(fullValue)) continue;
    if (entries.length >= TOOL_ARG_MAX_ENTRIES) {
      hiddenCount += 1;
      continue;
    }
    const value = fullValue.length > TOOL_ARG_MAX_VALUE_LENGTH
      ? `${fullValue.slice(0, TOOL_ARG_MAX_VALUE_LENGTH)}…`
      : fullValue;
    entries.push({ key, value, fullValue });
  }
  return { entries, hiddenCount };
}

function urlContainsPath(url: string, path: string): boolean {
  if (url.includes(path)) return true;
  try {
    return decodeURIComponent(url).includes(path);
  } catch {
    return false;
  }
}

const CopyValueButton: React.FC<{
  value: string;
  fieldLabel: string;
  fileTitle: string;
  onCopied?: (fieldLabel: string) => void;
}> = ({ value, fieldLabel, fileTitle, onCopied }) => {
  const [copied, setCopied] = React.useState(false);
  const resetRef = React.useRef<number | null>(null);
  React.useEffect(() => () => {
    if (resetRef.current !== null) window.clearTimeout(resetRef.current);
  }, []);
  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      onCopied?.(fieldLabel);
      if (resetRef.current !== null) window.clearTimeout(resetRef.current);
      resetRef.current = window.setTimeout(() => setCopied(false), 1500);
    } catch {
      // Clipboard unavailable; the value stays selectable.
    }
  };
  return (
    <button
      type="button"
      className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-sm text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
      onClick={handleCopy}
      aria-label={`Copy ${fieldLabel.toLowerCase()} for ${fileTitle}`}
    >
      {copied ? <Check size={12} aria-hidden="true" /> : <Copy size={12} aria-hidden="true" />}
    </button>
  );
};

export interface FileProvenanceDetailsProps {
  provenance: FileArtifactProvenance;
  variant?: 'row' | 'popover';
  onCopied?: (fieldLabel: string) => void;
  className?: string;
}

interface FactRow {
  label: string;
  content: React.ReactNode;
}

const FileProvenanceDetails: React.FC<FileProvenanceDetailsProps> = ({
  provenance,
  variant = 'row',
  onCopied,
  className,
}) => {
  const title = provenance.title || 'file';
  const managedPath = (provenance.managedLocalPath || '').trim();
  const storedPath = (provenance.contentPath || '').trim();
  const showStoredPath = Boolean(storedPath) && storedPath !== managedPath;
  const toolArgs = React.useMemo(
    () => formatToolArgEntries(provenance.toolArgs, [managedPath, storedPath]),
    [provenance.toolArgs, managedPath, storedPath],
  );
  const sourceTool = (provenance.sourceToolName || '').trim();
  const genericTool = (provenance.toolName || '').trim();
  const typeParts = [provenance.fileKind, provenance.mimeType, provenance.byteSize !== undefined ? formatBytes(provenance.byteSize) : '']
    .map((part) => (part || '').trim())
    .filter(Boolean);
  const addedAbsolute = formatAbsoluteDateTime(provenance.createdAt);

  const monoValueClass = 'break-all font-mono text-[11px] leading-4 text-foreground select-text';
  // Web URLs become real links (a remote-only file has no other view path);
  // connector paths and bare IDs stay copy-only text.
  const pathRow = (label: string, path: string, extra?: string) => ({
    label,
    content: (
      <span className="flex min-w-0 items-start gap-1">
        {isHttpUrl(path) ? (
          <a
            href={path}
            target="_blank"
            rel="noopener noreferrer"
            className={cn(monoValueClass, 'rounded-sm underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background')}
          >
            <span>{path}</span>
            <ExternalLink size={10} aria-hidden="true" className="ml-1 inline-block align-baseline text-muted-foreground" />
            <span className="sr-only">(opens in a new tab)</span>
            {extra ? <span className="ml-1 font-sans text-muted-foreground">({extra})</span> : null}
          </a>
        ) : (
          <span className={monoValueClass}>{path}{extra ? <span className="ml-1 font-sans text-muted-foreground">({extra})</span> : null}</span>
        )}
        <CopyValueButton value={path} fieldLabel={label} fileTitle={title} onCopied={onCopied} />
      </span>
    ),
  });

  const rows: FactRow[] = [];
  rows.push({
    label: 'Source',
    content: (
      <Badge variant="outline" className={`h-5 px-1.5 text-[10px] ${fileSourceBadgeClass(provenance.sourceLabel)}`}>
        {provenance.sourceLabel}
      </Badge>
    ),
  });
  if (sourceTool || genericTool) {
    rows.push({
      label: 'Tool',
      content: (
        <span className={monoValueClass}>
          {sourceTool || genericTool}
          {sourceTool && genericTool && genericTool !== sourceTool ? (
            <span className="ml-1 font-sans text-muted-foreground">({genericTool})</span>
          ) : null}
        </span>
      ),
    });
  }
  if (toolArgs.entries.length > 0) {
    rows.push({
      label: 'Request',
      content: (
        <span className="min-w-0 text-[11px] leading-4 text-foreground">
          {toolArgs.entries.map((entry, index) => (
            <span key={entry.key} title={entry.fullValue !== entry.value ? entry.fullValue : undefined}>
              {index > 0 && <span aria-hidden="true" className="text-muted-foreground"> · </span>}
              <span className="text-muted-foreground">{entry.key}: </span>
              <span className="break-all font-mono select-text">{entry.value}</span>
              {/* The clipped remainder lives only in a hover title, which
                  keyboard and SR users can't reach — give them the full
                  value through the standard copy control instead. */}
              {entry.fullValue !== entry.value && (
                <CopyValueButton
                  value={entry.fullValue}
                  fieldLabel={`full ${entry.key} request value`}
                  fileTitle={title}
                  onCopied={onCopied}
                />
              )}
            </span>
          ))}
          {toolArgs.hiddenCount > 0 && (
            <span className="text-muted-foreground"> · +{toolArgs.hiddenCount} more</span>
          )}
        </span>
      ),
    });
  }
  if (provenance.threadTitle) {
    rows.push({
      label: 'Origin chat',
      content: <span className="text-[11px] leading-4 text-foreground">{provenance.threadTitle}</span>,
    });
  }
  if (provenance.toolCallId) {
    rows.push(pathRow('Tool call', provenance.toolCallId));
  }
  const remoteUrl = (provenance.remoteUrl || '').trim();
  const remotePath = (provenance.remotePath || '').trim();
  const remoteLocator = remoteUrl || remotePath || (provenance.remoteRef || '').trim();
  if (remoteLocator) {
    rows.push(pathRow('Remote source', remoteLocator));
  }
  // A connector path earns its own row only when the URL doesn't already
  // spell it out — one locator to scan when they're redundant.
  if (remoteUrl && remotePath && !urlContainsPath(remoteUrl, remotePath)) {
    rows.push(pathRow('Remote path', remotePath));
  }
  if (managedPath) {
    rows.push(pathRow('Local copy', managedPath, 'app-managed'));
  }
  if (showStoredPath) {
    rows.push(pathRow('Stored copy', storedPath, provenance.contentStorage || undefined));
  }
  if (typeParts.length > 0) {
    rows.push({
      label: 'Type',
      content: (
        <span className="text-[11px] leading-4 text-foreground">
          {provenance.fileKind ? <span>{provenance.fileKind}</span> : null}
          {provenance.fileKind && (provenance.mimeType || provenance.byteSize !== undefined) ? <span aria-hidden="true" className="text-muted-foreground"> · </span> : null}
          {provenance.mimeType ? <span className="font-mono select-text">{provenance.mimeType}</span> : null}
          {provenance.mimeType && provenance.byteSize !== undefined ? <span aria-hidden="true" className="text-muted-foreground"> · </span> : null}
          {provenance.byteSize !== undefined ? <span>{formatBytes(provenance.byteSize)}</span> : null}
        </span>
      ),
    });
  }
  if (addedAbsolute) {
    rows.push({
      label: 'Added',
      content: (
        <span className="text-[11px] leading-4 text-foreground" title={formatRelativeTime(provenance.createdAt)}>
          {addedAbsolute}
        </span>
      ),
    });
  }
  const remoteOnly = provenance.cached === false;
  const canRemove = provenance.removable !== false;
  // Legacy records without the backend gate stay eligible, matching the
  // row-level ask-agent rule.
  const remoteRemovalAccess = canRemove
    ? 'Removable from Files/history only'
    : 'Not removable from Files/history';
  const remoteAccess = provenance.canRequestDownload === false
    ? `Remote only: no local copy · Automatic download unavailable · ${remoteRemovalAccess}`
    : `Remote only: no local copy · Agent can download on request · ${remoteRemovalAccess}`;
  const cachedAccess = canRemove
    ? `${provenance.downloadable ? 'Original downloadable' : 'Preview only'} · Removable: delete removes the app-managed copy`
    : `${provenance.downloadable ? 'Original downloadable' : 'Preview only'} · Not removable from Files/history`;
  rows.push({
    label: 'Access',
    content: (
      <span className="text-[11px] leading-4 text-muted-foreground">
        {remoteOnly
          ? remoteAccess
          : cachedAccess}
      </span>
    ),
  });

  return (
    <dl
      className={cn(
        'grid grid-cols-[max-content_minmax(0,1fr)] gap-x-3',
        variant === 'popover' ? 'gap-y-1.5' : 'gap-y-1',
        className,
      )}
      data-file-provenance-details
    >
      {rows.map((row) => (
        <React.Fragment key={row.label}>
          <dt className="pt-px text-right text-[10px] leading-4 text-muted-foreground">{row.label}</dt>
          <dd className="min-w-0">{row.content}</dd>
        </React.Fragment>
      ))}
    </dl>
  );
};

export default FileProvenanceDetails;
