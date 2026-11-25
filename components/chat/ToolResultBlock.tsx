import React, { useState } from 'react';
import { CheckCircle2, AlertCircle, ChevronDown, Copy, Zap } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import { ScrollArea, ScrollBar } from '@/components/ui/scroll-area';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import type { Artifact, AsyncQueryHandle, MessageBlockReference, QueryMetadata, ToolResultEnvelope } from '@/types';
import ArtifactCard from './ArtifactCard';
import RunningQueryCard from './RunningQueryCard';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';
import MessageReferenceButton from './MessageReferenceButton';
import AtlassianToolResult from './AtlassianToolResult';
import { hasNonCollapsedSelectionWithin } from '@/lib/textSelection';
import { formatBytes, formatDuration, formatRowCount } from '@/lib/formatters';
import { toast } from 'sonner';
import {
  getQueryProviderLabel,
  isQueryProvider,
  isQueryToolName,
  normalizeQueryProvider,
} from '@/lib/queryProviderRegistry';
import { tryParseEnvelope, isAsyncQueryHandle } from '@/lib/toolResultEnvelope';
import {
  extractToolErrorDiagnosticFromEnvelope,
  formatToolErrorCardText,
} from '@/lib/toolErrorDiagnostics';
import ConnectionReauthBanner, { resolveConnectionId } from './ConnectionReauthBanner';
import { isToolResultFailure } from './toolActivity/toolResultOutcome';

interface ToolResultBlockProps {
  name?: string;
  content: string | null;
  artifactsById: Record<string, Artifact>;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (artifact: Artifact) => void;
  pinnedArtifactIds?: string[];
  messageReference?: MessageBlockReference;
  isMessageReferenced?: boolean;
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
}

function isPlainObject(value: any): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function unwrapSingleArrayField(value: any): any {
  if (!isPlainObject(value)) return value;
  const keys = Object.keys(value);
  if (keys.length !== 1) return value;
  const inner = (value as any)[keys[0]];
  return Array.isArray(inner) ? inner : value;
}

function isArrayOfObjects(value: any): value is Array<Record<string, any>> {
  return Array.isArray(value) && value.length > 0 && isPlainObject(value[0]);
}

function isQueryTool(toolName: string | undefined): boolean {
  return isQueryToolName(toolName || '');
}

function toolPresentationNativeViewer(value: any): string {
  if (!isPlainObject(value)) return '';
  const presentation = isPlainObject(value.tool_presentation) ? value.tool_presentation : null;
  const nativeViewer = typeof presentation?.native_viewer === 'string'
    ? presentation.native_viewer
    : typeof value.native_viewer === 'string'
      ? value.native_viewer
      : '';
  return nativeViewer.trim();
}

function hasNativeViewerArtifact(envelope: ToolResultEnvelope, artifact?: Artifact): boolean {
  if (!artifact) return false;
  const candidates = [
    envelope.result_metadata,
    artifact.metadata?.result_metadata,
    artifact.metadata,
  ];
  return candidates.some((candidate) => {
    const nativeViewer = toolPresentationNativeViewer(candidate);
    return nativeViewer !== '' && nativeViewer !== 'connector_inspector';
  });
}

function shouldUseConnectorWidget(envelope: ToolResultEnvelope, toolName: string, artifact?: Artifact): boolean {
  const resultMeta = envelope.result_metadata || {};
  const integration = String(resultMeta.integration || resultMeta.provider || '').trim();
  const widget = String(resultMeta.widget || '').trim();
  const product = String(resultMeta.product || '').trim();

  if (widget === 'approval') return true;
  if (hasNativeViewerArtifact(envelope, artifact)) return false;
  if (integration !== 'atlassian') return false;
  if (product === 'confluence' || toolName.startsWith('confluence_')) return true;
  if (widget === 'comment_list' || toolName === 'jira_get_issue_comments') return true;
  return toolName.startsWith('jira_');
}

function CopyButton({ text, label }: { text: string; label?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className="inline-flex items-center gap-1 text-muted-foreground/60 hover:text-muted-foreground transition-colors"
          onClick={(e) => {
            e.stopPropagation();
            navigator.clipboard.writeText(text);
            toast.success('Copied to clipboard');
          }}
        >
          <Copy size={10} />
        </button>
      </TooltipTrigger>
      <TooltipContent>{label || 'Copy'}</TooltipContent>
    </Tooltip>
  );
}

function RichQueryDetails({ details, isError }: { details: React.ReactNode; isError: boolean }) {
  return details;
}

function buildRichDetails(qm: QueryMetadata | undefined, rowCount: number, isError: boolean, errorDetail?: string | null): React.ReactNode {
  if (isError && errorDetail) {
    return errorDetail;
  }
  if (!qm) {
    const status = isError ? 'Error' : 'Success';
    return rowCount > 0 ? `${status} · ${formatRowCount(rowCount)} rows` : `${status} · View output`;
  }

  const parts: React.ReactNode[] = [];
  parts.push(
    <span key="status" className={isError ? 'text-destructive' : 'text-green-500'}>
      {isError ? 'Error' : 'Success'}
    </span>
  );

  if (rowCount > 0) {
    parts.push(<span key="rows">{formatRowCount(rowCount)} rows</span>);
  }

  if (qm.duration_ms !== undefined) {
    parts.push(<span key="duration">{formatDuration(qm.duration_ms)}</span>);
  }

  if (qm.effective_limit !== undefined) {
    parts.push(<span key="effective-limit">{formatRowCount(qm.effective_limit)} row cap</span>);
  } else if (qm.page_size !== undefined) {
    parts.push(<span key="page-size">{formatRowCount(qm.page_size)} row cap</span>);
  }

  if (qm.requested_limit !== undefined && qm.effective_limit !== undefined && qm.requested_limit !== qm.effective_limit) {
    parts.push(<span key="requested-limit">requested {formatRowCount(qm.requested_limit)}</span>);
  }

  if (qm.limit_capped !== undefined) {
    parts.push(
      <span key="limit-capped">
        limit cap {qm.limit_capped ? 'applied' : 'not hit'}
      </span>
    );
  }

  if (qm.total_bytes_processed !== undefined) {
    const bytesStr = formatBytes(qm.total_bytes_processed);
    if (bytesStr) {
      parts.push(<span key="bytes">{bytesStr} scanned</span>);
    }
  }

  if (qm.cache_hit) {
    parts.push(
      <span key="cache" className="inline-flex items-center gap-0.5 text-muted-foreground">
        <Zap size={10} /> cached
      </span>
    );
  }

  return (
    <span className="flex items-center gap-1 flex-wrap text-xs text-muted-foreground">
      {parts.map((part, i) => (
        <React.Fragment key={i}>
          {i > 0 && <span className="text-muted-foreground/40">·</span>}
          {part}
        </React.Fragment>
      ))}
    </span>
  );
}

function QueryDetailsPanel({ qm, rowCount }: { qm: QueryMetadata; rowCount: number }) {
  const [isOpen, setIsOpen] = useState(false);
  const [schemaOpen, setSchemaOpen] = useState(false);
  const managedJobId = normalizeManagedQueryJobId(qm.job_id);

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen} className="mt-2">
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="flex items-center gap-1 text-[11px] text-muted-foreground/60 hover:text-muted-foreground transition-colors"
        >
          <ChevronDown
            size={12}
            className={`transition-transform ${isOpen ? 'rotate-0' : '-rotate-90'}`}
          />
          Query details
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <div className="mt-2 p-3 bg-background/30 border border-border rounded-lg">
          <div className="grid grid-cols-2 gap-x-6 gap-y-2">
            {qm.duration_ms !== undefined && (
              <MetadataField label="Duration" value={formatDuration(qm.duration_ms)} />
            )}
            {rowCount > 0 && (
              <MetadataField label="Rows returned" value={formatRowCount(rowCount)} />
            )}
            {qm.effective_limit !== undefined && (
              <MetadataField label="Applied row limit" value={formatRowCount(qm.effective_limit)} />
            )}
            {qm.requested_limit !== undefined && (
              <MetadataField label="Requested row limit" value={formatRowCount(qm.requested_limit)} />
            )}
            {qm.configured_limit !== undefined && (
              <MetadataField label="Configured row cap" value={formatRowCount(qm.configured_limit)} />
            )}
            {qm.limit_capped !== undefined && (
              <MetadataField label="Limit capped" value={qm.limit_capped ? 'Yes' : 'No'} />
            )}
            {qm.total_bytes_processed !== undefined && (
              <MetadataField label="Bytes scanned" value={formatBytes(qm.total_bytes_processed)} />
            )}
            {qm.provider && (
              <MetadataField
                label="Provider"
                value={isQueryProvider(qm.provider) ? getQueryProviderLabel(qm.provider) : String(qm.provider)}
              />
            )}
            {managedJobId && (
              <MetadataField label="Job ID" value={managedJobId} copyable />
            )}
            {qm.query_id && (
              <MetadataField label="Query ID" value={qm.query_id} copyable />
            )}
            {qm.statement_id && (
              <MetadataField label="Statement ID" value={qm.statement_id} copyable />
            )}
            {qm.job_location && (
              <MetadataField label="Location" value={qm.job_location} />
            )}
            {qm.cache_hit !== undefined && (
              <MetadataField label="Cache hit" value={qm.cache_hit ? 'Yes' : 'No'} />
            )}
          </div>

          {qm.schema && qm.schema.length > 0 && (
            <Collapsible open={schemaOpen} onOpenChange={setSchemaOpen} className="mt-3">
              <CollapsibleTrigger asChild>
                <button
                  type="button"
                  className="flex items-center gap-1 text-[11px] text-muted-foreground/60 hover:text-muted-foreground transition-colors"
                >
                  <ChevronDown
                    size={12}
                    className={`transition-transform ${schemaOpen ? 'rotate-0' : '-rotate-90'}`}
                  />
                  Schema ({qm.schema.length} columns)
                </button>
              </CollapsibleTrigger>
              <CollapsibleContent>
                <div className="mt-1.5 max-h-[200px] overflow-y-auto">
                  <table className="w-full text-xs">
                    <thead>
                      <tr className="border-b border-border">
                        <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold py-1 pr-3">#</th>
                        <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold py-1 pr-3">Name</th>
                        <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold py-1 pr-3">Type</th>
                        {qm.schema.some(c => c.mode) && (
                          <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold py-1">Mode</th>
                        )}
                      </tr>
                    </thead>
                    <tbody>
                      {qm.schema.map((col, i) => (
                        <tr key={i} className="border-b border-border/50 hover:bg-muted/30">
                          <td className="py-1 pr-3 text-muted-foreground/60 font-mono">{i + 1}</td>
                          <td className="py-1 pr-3 font-mono text-foreground">{col.name}</td>
                          <td className="py-1 pr-3 font-mono text-muted-foreground">{col.type || '-'}</td>
                          {qm.schema!.some(c => c.mode) && (
                            <td className="py-1">
                              {col.mode && (
                                <Badge variant="outline" className="text-[9px] px-1 py-0">{col.mode}</Badge>
                              )}
                            </td>
                          )}
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </CollapsibleContent>
            </Collapsible>
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
}

function MetadataField({ label, value, copyable }: { label: string; value: string; copyable?: boolean }) {
  return (
    <div className="flex flex-col gap-0.5">
      <span className="text-[10px] text-muted-foreground uppercase tracking-wider font-semibold">{label}</span>
      <span className="text-xs font-mono text-foreground flex items-center gap-1">
        {value}
        {copyable && <CopyButton text={value} label={`Copy ${label}`} />}
      </span>
    </div>
  );
}

const ToolResultBlock: React.FC<ToolResultBlockProps> = ({
  name,
  content,
  artifactsById,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  messageReference,
  isMessageReferenced,
  onToggleMessageReference,
}) => {
  const [isOpen, setIsOpen] = useState(false);
  const envelope = tryParseEnvelope(content);

  // New envelope-based tool result UI (preferred path).
  if (envelope) {
    const toolName = envelope.tool_name || name || 'tool_result';
    if (toolName === 'check_query_status' || envelope.ui_suppress === true) {
      return null;
    }
    const artifact = artifactsById[envelope.artifact_id];
    if (shouldUseConnectorWidget(envelope, toolName, artifact)) {
      return <AtlassianToolResult envelope={envelope} fallbackName={name} />;
    }
    const qm: QueryMetadata | undefined = envelope.query_metadata || artifact?.metadata?.query_metadata;

    // Async query handle: the query was submitted but hasn't completed yet.
    // Render the RunningQueryCard with live status polling and cancel support.
    if (isQueryTool(toolName) && isAsyncQueryHandle(envelope.result)) {
      const handle = envelope.result as AsyncQueryHandle;
      const managedJobId = normalizeManagedQueryJobId(handle.job_id);
      if (!managedJobId) return null;
      const runningQm: QueryMetadata | undefined = artifact?.metadata?.query_metadata || envelope.query_metadata;
      const provider = normalizeQueryProvider(
        handle.provider || runningQm?.provider || artifact?.metadata?.provider,
        'bigquery',
      );
      const ownerToken = handle.owner_token || runningQm?.owner_token || artifact?.metadata?.owner_token;
      const threadId = handle.thread_id || runningQm?.thread_id || artifact?.metadata?.thread_id;
      const runId = handle.run_id || runningQm?.run_id || artifact?.metadata?.run_id;
      const queryObligationId =
        handle.query_obligation_id
        || runningQm?.query_obligation_id
        || artifact?.metadata?.query_obligation_id;
      const hasCompletedResultsArtifact =
        artifact?.metadata?.tool_name === 'get_query_results'
        && artifact?.metadata?.ok === true
        && artifact?.metadata?.placeholder !== true;
      const derivedInitialStatus = hasCompletedResultsArtifact ? 'completed' : handle.status;
      const startedAtMs = typeof handle._uiStartedAt === 'number' && Number.isFinite(handle._uiStartedAt)
        ? handle._uiStartedAt
        : undefined;
      return (
        <div className="group/msg w-full max-w-[85%] md:max-w-[75%] pl-[3rem] mb-4 animate-fade-in min-w-0">
          {messageReference && onToggleMessageReference && (
            <div className="mb-1 flex justify-end pr-1">
              <MessageReferenceButton
                isReferenced={isMessageReferenced}
                onToggle={() => onToggleMessageReference(messageReference)}
                className={isMessageReferenced
                  ? 'text-accent opacity-100'
                  : 'text-muted-foreground/40 opacity-60 group-hover/msg:opacity-100 hover:text-accent focus-visible:opacity-100 focus-visible:text-accent'}
              />
            </div>
          )}
          <RunningQueryCard
            jobId={managedJobId}
            provider={provider}
            sqlPreview={handle.sql_preview}
            ownerToken={ownerToken}
            threadId={threadId}
            runId={runId}
            queryObligationId={queryObligationId}
            queryMetadata={runningQm}
            initialStatus={derivedInitialStatus}
            startedAtMs={startedAtMs}
          />
        </div>
      );
    }

    const isError = isToolResultFailure(envelope);
    const errorDiagnostic = isError
      ? extractToolErrorDiagnosticFromEnvelope(envelope, { fallbackTitle: toolName })
      : null;
    const errorDetail = errorDiagnostic ? formatToolErrorCardText(errorDiagnostic) : null;

    const unwrapped = envelope.result_format === 'json' ? unwrapSingleArrayField(envelope.result) : null;
    const isDataset = isArrayOfObjects(unwrapped);
    const rowCount = isDataset ? unwrapped.length : 0;

    const title =
      artifact?.title
        || (isQueryTool(toolName) ? 'Query Results' : `${toolName} result`);
    const hasPlotSpec = Boolean(artifact?.metadata?.plot_spec || envelope.plot_spec);
    const type = artifact?.type === 'image' ? 'image'
      : (artifact?.type === 'data' && hasPlotSpec) ? 'chart'
      : artifact?.type === 'data' ? 'data'
      : toolName === 'read_file' ? 'file'
      : isDataset ? 'data' : 'code';
    const thumbnailSrc = type === 'image' && artifact ? artifact.content : undefined;

    const colCount = isDataset && unwrapped.length > 0 && typeof unwrapped[0] === 'object' && unwrapped[0] !== null
      ? Object.keys(unwrapped[0]).length : 0;
    const plotSpec = artifact?.metadata?.plot_spec || envelope.plot_spec;
    const chartEngine = artifact?.metadata?.chart_engine || envelope.chart_engine;

    let details: React.ReactNode;
    if (isQueryTool(toolName)) {
      details = buildRichDetails(qm, rowCount, isError, errorDetail);
    } else if (type === 'image') {
      details = isError && errorDetail ? errorDetail : `${isError ? 'Error' : 'Success'} · View image`;
    } else if (type === 'chart' && plotSpec) {
      const series = (plotSpec.options || plotSpec)?.series;
      const chartType = Array.isArray(series) && series[0]?.type ? series[0].type : 'chart';
      const seriesCount = Array.isArray(series) ? series.length : 0;
      const engineLabel = chartEngine === 'echarts' ? 'ECharts' : chartEngine === 'highcharts' ? 'Highcharts' : 'Chart';
      details = [engineLabel, chartType, seriesCount > 1 ? `${seriesCount} series` : '', rowCount > 0 ? `${formatRowCount(rowCount)} pts` : ''].filter(Boolean).join(' · ');
    } else if (isDataset) {
      details = `${formatRowCount(rowCount)} rows${colCount > 0 ? ` × ${colCount} columns` : ''}`;
    } else if (toolName === 'read_file') {
      details = isError && errorDetail ? errorDetail : `${isError ? 'Error' : 'Success'} · View file`;
    } else {
      details = isError && errorDetail ? errorDetail : `${isError ? 'Error' : 'Success'} · View output`;
    }

    const handleClick = () => {
      if (artifact) onOpenArtifact(artifact);
      else setIsOpen((prev) => !prev);
    };

    return (
      <div className="group/msg w-full max-w-[85%] md:max-w-[75%] pl-12 mb-4 animate-fade-in min-w-0">
        {messageReference && onToggleMessageReference && (
          <div className="flex justify-end pr-1">
            <MessageReferenceButton
              isReferenced={isMessageReferenced}
              onToggle={() => onToggleMessageReference(messageReference)}
            />
          </div>
        )}
        <ArtifactCard
          title={title}
          type={type}
          details={details}
          thumbnailSrc={thumbnailSrc}
          plotSpec={type === 'chart' ? plotSpec : undefined}
          onClick={handleClick}
          onPin={artifact && onPinArtifact ? (_e) => onPinArtifact(artifact) : undefined}
          isPinned={artifact ? pinnedArtifactIds?.includes(artifact.id) : false}
        />

        {errorDiagnostic?.kind === 'auth' && (
          <ConnectionReauthBanner
            connectionId={resolveConnectionId(toolName, envelope.result_metadata?.integration)}
            toolCallId={envelope.tool_call_id}
            message={errorDiagnostic.message || errorDiagnostic.title}
          />
        )}

        {isQueryTool(toolName) && qm && (
          <QueryDetailsPanel qm={qm} rowCount={rowCount} />
        )}

        {!artifact && isOpen && (
          <div className="mt-2 border border-border rounded-lg bg-background/30 overflow-hidden">
            <div className="px-3 py-2 border-b border-border flex items-center gap-2 text-xs font-medium">
              {isError ? (
                <Badge variant="destructive" className="text-[10px] gap-1">
                  <AlertCircle size={10} /> Error
                </Badge>
              ) : (
                <Badge variant="secondary" className="text-[10px] gap-1 bg-green-500/10 text-green-500 border-green-500/20">
                  <CheckCircle2 size={10} /> Success
                </Badge>
              )}
              <span className="font-mono text-muted-foreground">{toolName}</span>
            </div>
            <ScrollArea className="max-h-[200px]">
              <div className="p-3 overflow-x-auto">
                <pre className="text-xs font-mono text-muted-foreground whitespace-pre-wrap break-all">
                  {content}
                </pre>
              </div>
              <ScrollBar orientation="vertical" />
            </ScrollArea>
          </div>
        )}
      </div>
    );
  }

  // Legacy/non-envelope tool results (back-compat).
  if (name === 'check_query_status') {
    return null;
  }

  const isFileRead = name === 'read_file';
  const isQuery = isQueryTool(name);

  let isDataset = false;
  let rowCount = 0;
  let parsedContent: any = null;
  let isError = false;

  try {
    parsedContent = JSON.parse(content || '[]');
    if (!Array.isArray(parsedContent) && parsedContent && typeof parsedContent === 'object') {
      if (parsedContent.error) isError = true;
      const keys = Object.keys(parsedContent);
      if (keys.length === 1 && Array.isArray(parsedContent[keys[0]])) {
        parsedContent = parsedContent[keys[0]];
      }
    }
    if (Array.isArray(parsedContent) && parsedContent.length > 0 && typeof parsedContent[0] === 'object') {
      isDataset = true;
      rowCount = parsedContent.length;
    }
  } catch {
    isDataset = false;
  }

  const handleOpenArtifact = () => {
    if (isFileRead) {
      const match = content?.match(/^File: (.+)\n\n/);
      const title = match ? match[1] : 'File Content';
      const rawCode = content?.replace(/^File: .+\n\n/, '') || '';
      onOpenArtifact({
        id: crypto.randomUUID(),
        type: 'code',
        title,
        content: rawCode,
        language: title.split('.').pop() || 'text',
      });
    } else if (isDataset) {
      onOpenArtifact({
        id: crypto.randomUUID(),
        type: 'data',
        title: isQuery ? 'Query Results' : 'Dataset',
        content: JSON.stringify(parsedContent),
      });
    } else {
      setIsOpen(!isOpen);
    }
  };

  if (isFileRead || isDataset) {
    return (
      <div className="group/msg w-full max-w-[85%] md:max-w-[75%] pl-12 mb-4 animate-fade-in min-w-0">
        {messageReference && onToggleMessageReference && (
          <div className="flex justify-end pr-1">
            <MessageReferenceButton
              isReferenced={isMessageReferenced}
              onToggle={() => onToggleMessageReference(messageReference)}
            />
          </div>
        )}
        <ArtifactCard
          title={isFileRead ? (content?.split('\n')[0] || 'Read File') : (isQuery ? 'Query Results' : 'Tool Result')}
          type={isFileRead ? 'code' : 'data'}
          details={isFileRead ? 'View source code' : `${rowCount} rows`}
          onClick={handleOpenArtifact}
        />
      </div>
    );
  }

  return (
    <div className="group/msg w-full max-w-[85%] md:max-w-[75%] pl-12 mb-4 animate-fade-in min-w-0">
      {messageReference && onToggleMessageReference && (
        <div className="mb-1 flex justify-end pr-1">
          <MessageReferenceButton
            isReferenced={isMessageReferenced}
            onToggle={() => onToggleMessageReference(messageReference)}
            className={isMessageReferenced
              ? 'text-accent opacity-100'
              : 'text-muted-foreground/40 opacity-60 group-hover/msg:opacity-100 hover:text-accent focus-visible:opacity-100 focus-visible:text-accent'}
          />
        </div>
      )}
      <Collapsible open={isOpen} onOpenChange={setIsOpen} className="border border-border rounded-lg bg-background/30 overflow-hidden">
        <button
          type="button"
          aria-expanded={isOpen}
          className="w-full flex items-center justify-between px-3 py-2 hover:bg-muted/50 transition-colors select-text"
          onClick={(e) => {
            // Allow selecting/copying the tool name without collapsing/expanding.
            if (hasNonCollapsedSelectionWithin(e.currentTarget as HTMLElement)) return;
            setIsOpen((prev) => !prev);
          }}
        >
          <div className="flex items-center gap-2 text-xs font-medium">
            {isError ? (
              <Badge variant="destructive" className="text-[10px] gap-1">
                <AlertCircle size={10} /> Error
              </Badge>
            ) : (
              <Badge variant="secondary" className="text-[10px] gap-1 bg-green-500/10 text-green-500 border-green-500/20">
                <CheckCircle2 size={10} /> Success
              </Badge>
            )}
            <span className="font-mono text-muted-foreground">{name}</span>
          </div>
        </button>
        <CollapsibleContent>
          <ScrollArea className="max-h-[200px]">
            <div className="p-3 overflow-x-auto">
              <pre className="text-xs font-mono text-muted-foreground whitespace-pre-wrap break-all">
                {content}
              </pre>
            </div>
            <ScrollBar orientation="vertical" />
          </ScrollArea>
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
};

export default ToolResultBlock;
