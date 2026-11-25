import React from 'react';
import { Terminal, Clock, ExternalLink, BookOpen, Search } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Collapsible, CollapsibleContent } from '@/components/ui/collapsible';
import { ScrollArea, ScrollBar } from '@/components/ui/scroll-area';
import type { Artifact, MessageBlockReference, ToolCall, ToolProgressEvent } from '@/types';
import { Role, isSandboxedHtmlToolName } from '@/types';
import ArtifactCard from './ArtifactCard';
import CodeBlock from './CodeBlock';
import MessageReferenceButton from './MessageReferenceButton';
import { hasNonCollapsedSelectionWithin } from '@/lib/textSelection';
import { formatElapsed, formatBytes } from '@/lib/formatters';
import {
  getQueryProviderLabel,
  getQueryProviderProgressLinkLabel,
  isQueryProvider,
  isQueryRunToolName,
  normalizeQueryProvider,
} from '@/lib/queryProviderRegistry';
import { getDeterministicQueryToolProgressPercent } from '@/lib/queryProgress';

type ToolProgressEventWithRows = ToolProgressEvent & {
  rows_processed?: unknown;
  rows_returned?: unknown;
  row_count?: unknown;
};

interface ToolCallBlockProps {
  toolCall: ToolCall;
  isExecuting?: boolean;
  progress?: ToolProgressEvent;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (a: Artifact) => void;
  pinnedArtifactIds?: string[];
  parentMessageId?: string;
  parentMessageSeq?: number;
  toolCallIndex?: number;
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
  pinnedMessageReferenceIds?: string[];
}

function normalizeSnapshot(value: string): string {
  return String(value || '')
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .trim();
}

function toPreview(value: string, maxChars = 180): string {
  const oneLine = value.replace(/\s+/g, ' ').trim();
  if (!oneLine) return '(empty)';
  if (oneLine.length <= maxChars) return oneLine;
  return `${oneLine.slice(0, maxChars - 3).trimEnd()}...`;
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
    if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  }
  return '';
}

function firstNumber(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      const parsed = Number(value);
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return undefined;
}

function isArxivReadToolName(toolName: string): boolean {
  return toolName === 'arxiv_read_paper' || toolName === 'arxiv_get_paper_text';
}

/**
 * Generated-HTML artifact tools carry the entire HTML document in their args —
 * dumping that as JSON is unreadable and dwarfs the transcript. Summarize the
 * args instead, with the payload reduced to its size.
 */
function buildSandboxedHtmlArgsSummary(args: Record<string, any>): string {
  const lines: string[] = [];
  for (const [key, value] of Object.entries(args || {})) {
    if (typeof value === 'string' && (key === 'html' || value.length > 600)) {
      lines.push(`${key}: ${formatBytes(value.length)} of generated content (omitted — open the artifact to view)`);
      continue;
    }
    let text: string;
    try {
      text = typeof value === 'string' ? value : JSON.stringify(value);
    } catch {
      text = String(value);
    }
    lines.push(`${key}: ${toPreview(text, 160)}`);
  }
  return lines.join('\n') || '(no arguments)';
}

const ToolCallBlock: React.FC<ToolCallBlockProps> = ({
  toolCall,
  isExecuting,
  progress,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  parentMessageId,
  parentMessageSeq,
  toolCallIndex,
  onToggleMessageReference,
  pinnedMessageReferenceIds,
}) => {
  const [isOpen, setIsOpen] = React.useState(false);
  const [elapsed, setElapsed] = React.useState(0);
  const [progressWidth, setProgressWidth] = React.useState<number | null>(null);
  const startedAt = (toolCall as any)._uiStartedAt as number | undefined;
  const typedProgress = progress as ToolProgressEventWithRows | undefined;

  React.useEffect(() => {
    if (!isExecuting || !startedAt) {
      setElapsed(0);
      return;
    }
    setElapsed((prev) => Math.max(prev, Date.now() - startedAt));
    const interval = setInterval(() => {
      setElapsed((prev) => Math.max(prev, Date.now() - startedAt));
    }, 1000);
    return () => clearInterval(interval);
  }, [isExecuting, startedAt]);

  React.useEffect(() => {
    if (!isExecuting) {
      setProgressWidth(null);
      return;
    }

    const nextWidth = getDeterministicQueryToolProgressPercent(progress);
    if (nextWidth === null) {
      setProgressWidth((prevWidth) => prevWidth);
      return;
    }

    setProgressWidth((prevWidth) => {
      if (prevWidth === null || nextWidth === 100) {
        return nextWidth;
      }
      return Math.max(prevWidth, nextWidth);
    });
  }, [isExecuting, progress?.provider, progress?.phase, progress?.state, progress?.percent, progress?.bytes_processed, progress?.page_current, progress?.page_count, typedProgress?.rows_processed, typedProgress?.rows_returned, typedProgress?.row_count]);

  const argsText =
    typeof toolCall.argsText === 'string'
      ? toolCall.argsText
      : JSON.stringify(toolCall.args || {}, null, 2);
  const argsSnapshot = normalizeSnapshot(argsText) || '(empty)';
  const isDisplayTable = toolCall.name === 'display_table';
  const isSqlQueryTool = isQueryRunToolName(toolCall.name);

  const parsedArgs: any = toolCall.args || {};
  const parsedData = isDisplayTable && parsedArgs.data_json ? parsedArgs.data_json : null;
  const parsedHighchartsSpec = isDisplayTable && parsedArgs.chart_spec && typeof parsedArgs.chart_spec === 'object' && !Array.isArray(parsedArgs.chart_spec)
    ? parsedArgs.chart_spec
    : null;
  const parsedEchartsSpec = isDisplayTable && parsedArgs.echarts_spec && typeof parsedArgs.echarts_spec === 'object' && !Array.isArray(parsedArgs.echarts_spec)
    ? parsedArgs.echarts_spec
    : null;
  const parsedPlotSpec = parsedEchartsSpec || parsedHighchartsSpec;
  const parsedChartEngine = parsedEchartsSpec ? 'echarts' : (parsedHighchartsSpec ? 'highcharts' : null);
  const parsedChartSpecVersion = isDisplayTable && typeof (parsedArgs.echarts_spec_version || parsedArgs.chart_spec_version) === 'string'
    ? (parsedArgs.echarts_spec_version || parsedArgs.chart_spec_version)
    : null;
  const sqlQuery = isSqlQueryTool && typeof parsedArgs.sql === 'string' ? parsedArgs.sql : null;
  const progressProvider = progress && isQueryProvider(progress.provider) ? normalizeQueryProvider(progress.provider, 'bigquery') : null;
  const progressProviderLabel = progressProvider ? getQueryProviderLabel(progressProvider) : null;
  const progressLinkLabel = progressProvider ? getQueryProviderProgressLinkLabel(progressProvider) : 'View query';
  const progressAccount = progress?.account_identifier;
  const progressDatabase = progress?.database_name || progress?.project_id;
  const progressWarehouse = progress?.warehouse_name || progress?.warehouse_id;
  const progressMessage = typeof progress?.message === 'string' && progress.message.trim()
    ? progress.message.trim()
    : typeof progress?.label === 'string' && progress.label.trim()
      ? progress.label.trim()
      : '';
  const progressPaperTitle = typeof progress?.title === 'string' ? progress.title.trim() : '';
  const isProviderHostedWebSearch = toolCall.name === 'web_search' && ((toolCall as any).provider_executed === true || progress?.provider === 'provider-hosted');
  const webSearchQuery = firstString(progress?.query, parsedArgs.query, parsedArgs.search_query);
  const webSearchResultCount = firstNumber(progress?.results_count);
  const progressPageCurrent = Number(progress?.page_current ?? progress?.pdf?.page_current);
  const progressPageCount = Number(progress?.page_count ?? progress?.pdf?.page_count);
  const progressPageDetail = Number.isFinite(progressPageCurrent) && progressPageCurrent > 0
    ? Number.isFinite(progressPageCount) && progressPageCount > 0
      ? `page ${progressPageCurrent} of ${progressPageCount}`
      : `page ${progressPageCurrent}`
    : '';
  const isArxivReadTool = isArxivReadToolName(toolCall.name);
  const requestedStartPage = firstNumber(parsedArgs.start_page, parsedArgs.startPage);
  const requestedMaxPages = firstNumber(parsedArgs.max_pages, parsedArgs.maxPages);
  const requestedEndPage = requestedStartPage !== undefined && requestedMaxPages !== undefined && requestedMaxPages > 0
    ? requestedStartPage + Math.max(0, requestedMaxPages - 1)
    : requestedStartPage;
  const arxivReadTitle = progressPaperTitle || firstString(parsedArgs.title) || 'Reading arXiv paper';
  const arxivReadId = firstString(progress?.arxiv_id, parsedArgs.paper)
    .replace(/^arxiv:/i, '')
    .replace(/^https?:\/\/(?:www\.)?arxiv\.org\/(?:abs|pdf)\//i, '')
    .replace(/\.pdf$/i, '');
  const arxivReadTotalSuffix =
    Number.isFinite(progressPageCount) && progressPageCount > 0 ? ` / ${progressPageCount}` : '';
  const arxivReadRange = progressPageDetail
    || (requestedStartPage !== undefined
      ? requestedEndPage !== undefined && requestedEndPage !== requestedStartPage
        ? `pp. ${requestedStartPage}–${requestedEndPage}${arxivReadTotalSuffix}`
        : `p. ${requestedStartPage}${arxivReadTotalSuffix}`
      : 'paper text');
  const arxivReadLocator = [
    arxivReadId ? `arXiv:${arxivReadId}` : '',
    arxivReadRange,
  ].filter(Boolean).join(' · ');
  const pinnedMessageReferenceIdSet = React.useMemo(
    () => new Set((pinnedMessageReferenceIds || []).map((id) => String(id || '').trim()).filter(Boolean)),
    [pinnedMessageReferenceIds],
  );

  const messageReference = React.useMemo<MessageBlockReference>(() => {
    const messageId = typeof parentMessageId === 'string' && parentMessageId.trim()
      ? parentMessageId.trim()
      : undefined;
    const seq = Number(parentMessageSeq);
    const messageSeq = Number.isFinite(seq) && seq > 0 ? Math.trunc(seq) : undefined;
    const messageIdentity = messageId
      ? `id:${messageId}`
      : messageSeq !== undefined
        ? `seq:${messageSeq}`
        : `tool_index:${toolCallIndex ?? 0}`;
    const callIdentity = String(toolCall.id || `index:${toolCallIndex ?? 0}`).trim() || `index:${toolCallIndex ?? 0}`;
    const id = `tool_call:${messageIdentity}:${callIdentity}`;
    const title = `Tool call - ${toolCall.name}`;
    return {
      id,
      key: id,
      kind: 'tool_call',
      messageId,
      messageSeq,
      role: Role.Tool,
      title,
      label: title,
      preview: toPreview(argsSnapshot),
      contentSnapshot: argsSnapshot,
    };
  }, [argsSnapshot, parentMessageId, parentMessageSeq, toolCall.id, toolCall.name, toolCallIndex]);
  const isMessageReferenced = pinnedMessageReferenceIdSet.has(messageReference.id);

  const displayArtifact: Artifact | null = parsedData ? {
    id: toolCall.id,
    type: 'data',
    title: parsedArgs.title || 'Data Visualization',
    content: parsedData,
    metadata: {
      tool_name: toolCall.name,
      tool_call_id: toolCall.id,
      ...(parsedPlotSpec ? { plot_spec: parsedPlotSpec } : {}),
      ...(parsedChartEngine ? { chart_engine: parsedChartEngine } : {}),
      ...(parsedChartSpecVersion ? { chart_spec_version: parsedChartSpecVersion } : {}),
    },
  } : null;

  const handleViewArtifact = () => {
    if (displayArtifact) onOpenArtifact(displayArtifact);
  };

  return (
    <Collapsible open={isOpen} onOpenChange={setIsOpen} className="my-2 border border-border rounded-lg overflow-hidden bg-background/50">
      <div className="group/msg flex items-center gap-1 pr-1">
        <button
          type="button"
          aria-expanded={isOpen}
          className="w-full flex items-center gap-2 px-3 py-2 text-xs font-mono text-muted-foreground hover:bg-muted/50 transition-colors text-left select-text"
          onClick={(e) => {
            // Allow selecting/copying the tool name without toggling the collapsible.
            if (hasNonCollapsedSelectionWithin(e.currentTarget as HTMLElement)) return;
            setIsOpen((prev) => !prev);
          }}
        >
          {isProviderHostedWebSearch ? (
            <>
              <Search size={13} className="shrink-0 text-blue-500" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-xs font-medium text-foreground">
                  {progress?.phase === 'complete' ? 'Web search complete' : 'Searching the web'}
                </span>
                <span className="block truncate font-mono text-[11px] text-muted-foreground">
                  {webSearchQuery || 'Provider-hosted search'}
                </span>
              </span>
              {webSearchResultCount !== undefined && webSearchResultCount > 0 && (
                <Badge variant="secondary" className="text-[10px] font-mono border-blue-500/25 bg-blue-500/10 text-blue-500">
                  {webSearchResultCount} {webSearchResultCount === 1 ? 'RESULT' : 'RESULTS'}
                </Badge>
              )}
              <Badge variant="secondary" className="text-[10px] font-mono border-blue-500/25 bg-blue-500/10 text-blue-500">
                READ
              </Badge>
            </>
          ) : isArxivReadTool ? (
            <>
              <BookOpen size={13} className="shrink-0 text-orange-400" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-xs font-medium text-foreground">
                  {arxivReadTitle}
                </span>
                <span className="block truncate font-mono text-[11px] text-muted-foreground">
                  {arxivReadLocator || toolCall.name}
                </span>
              </span>
              <Badge variant="secondary" className="text-[10px] font-mono border-orange-500/25 bg-orange-500/10 text-orange-400">
                READ
              </Badge>
            </>
          ) : (
            <>
              <Terminal size={12} className="text-muted-foreground" />
              <Badge variant="secondary" className="text-[10px] font-mono bg-muted text-muted-foreground border-border/50">
                {toolCall.name}
              </Badge>
            </>
          )}
        </button>
        {onToggleMessageReference && (
          <MessageReferenceButton
            isReferenced={isMessageReferenced}
            onToggle={() => onToggleMessageReference(messageReference)}
            className={isMessageReferenced
              ? 'text-accent opacity-100'
              : 'text-muted-foreground/40 opacity-60 group-hover/msg:opacity-100 hover:text-accent focus-visible:opacity-100 focus-visible:text-accent'}
          />
        )}
      </div>

      {isDisplayTable && parsedData && (() => {
        const hasPlot = !!parsedPlotSpec;
        const cardType = hasPlot ? 'chart' as const : 'data' as const;
        let cardDetails: string;
        if (hasPlot) {
          const series = (parsedPlotSpec.options || parsedPlotSpec)?.series;
          const chartType = Array.isArray(series) && series[0]?.type ? series[0].type : 'chart';
          const seriesCount = Array.isArray(series) ? series.length : 0;
          const engine = parsedChartEngine === 'echarts' ? 'ECharts' : parsedChartEngine === 'highcharts' ? 'Highcharts' : 'Chart';
          cardDetails = [engine, chartType, seriesCount > 1 ? `${seriesCount} series` : ''].filter(Boolean).join(' · ');
        } else {
          let rows = 0, cols = 0;
          try {
            const arr = typeof parsedData === 'string' ? JSON.parse(parsedData) : parsedData;
            if (Array.isArray(arr)) { rows = arr.length; if (arr[0] && typeof arr[0] === 'object') cols = Object.keys(arr[0]).length; }
          } catch {}
          cardDetails = rows > 0 ? `${rows} rows${cols > 0 ? ` × ${cols} columns` : ''}` : 'View interactive table';
        }
        return (
          <div className="px-3 pb-3">
            <ArtifactCard
              title={parsedArgs.title || 'Generated Table'}
              type={cardType}
              details={cardDetails}
              plotSpec={hasPlot ? parsedPlotSpec : undefined}
              onClick={handleViewArtifact}
              onPin={displayArtifact && onPinArtifact ? (_e) => onPinArtifact(displayArtifact) : undefined}
              isPinned={displayArtifact ? pinnedArtifactIds?.includes(displayArtifact.id) : false}
            />
          </div>
        );
      })()}

      {sqlQuery && (
        <div className="px-3 pb-2">
          <CodeBlock language="sql">{sqlQuery}</CodeBlock>
        </div>
      )}

      {isExecuting && (
        <div className="px-3 pb-3 animate-fade-in">
          {/* --- Progress bar --- */}
          {progressWidth !== null ? (
            <div
              className="h-0.5 w-full bg-border rounded-full overflow-hidden mb-2.5"
              role="progressbar"
              aria-label={progress?.provider === 'arxiv' ? 'Paper reading progress' : 'Tool progress'}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuenow={progressWidth}
              aria-valuetext={[progressMessage, progressPageDetail, progressPaperTitle].filter(Boolean).join(' · ') || undefined}
            >
              <div
                className="h-full bg-muted-foreground/50 rounded-full transition-all duration-700 ease-out"
                style={{
                  width: `${progressWidth}%`,
                }}
              />
            </div>
          ) : (
            <div
              className="h-0.5 w-full bg-border rounded-full overflow-hidden mb-2.5"
              role="progressbar"
              aria-label={progress?.provider === 'arxiv' ? 'Paper reading progress' : 'Tool progress'}
              aria-valuemin={0}
              aria-valuemax={100}
              aria-valuetext={[progressMessage, progressPageDetail, progressPaperTitle].filter(Boolean).join(' · ') || undefined}
            >
              <div className="h-full w-1/3 bg-muted-foreground/50 rounded-full animate-[shimmer_1.5s_ease-in-out_infinite]" />
            </div>
          )}

          {/* --- Info row --- */}
          <div className="flex items-center gap-1.5 text-xs text-muted-foreground flex-wrap">
            {progress ? (
              <>
                {progressProviderLabel && (
                  <>
                    <span className="font-medium text-muted-foreground">
                      {progressProviderLabel}
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}
                {!progressProviderLabel && progress.provider === 'arxiv' && (
                  <>
                    <span className="font-medium text-muted-foreground">arXiv</span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}
                {progressMessage && (
                  <>
                    <span className="text-muted-foreground/80">{progressMessage}</span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}
                {progressPageDetail && (
                  <>
                    <span className="font-mono text-[11px] text-muted-foreground/70">
                      {progressPageDetail}
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}
                {progressPaperTitle && (
                  <>
                    <span className="truncate max-w-[220px] text-muted-foreground/70">
                      {progressPaperTitle}
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}

                {progressAccount && (
                  <>
                    <span className="font-mono text-muted-foreground/60 text-[11px] truncate max-w-[160px]">
                      {progressAccount}
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}
                {progressDatabase && (
                  <>
                    <span className="font-mono text-muted-foreground/60 text-[11px] truncate max-w-[160px]">
                      {progressDatabase}
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}
                {progressWarehouse && (
                  <>
                    <span className="font-mono text-muted-foreground/60 text-[11px] truncate max-w-[160px]">
                      {progressWarehouse}
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}

                {(progressProvider === 'databricks' || progressProvider === 'trino' || progressProvider === 'snowflake') && progress.state && (
                  <>
                    <span className="font-mono text-muted-foreground/70 text-[11px]">
                      {progress.state}
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}

                {progress.bytes_processed && Number(progress.bytes_processed) > 0 && (
                  <>
                    <span className="font-mono text-[11px]">
                      {formatBytes(Number(progress.bytes_processed))} scanned
                    </span>
                    <span className="text-muted-foreground/30">&middot;</span>
                  </>
                )}

                <Clock size={11} className="text-muted-foreground animate-pulse" />
                <span className="font-mono">{formatElapsed(elapsed)}</span>
              </>
            ) : (
              <>
                <Clock size={12} className="text-muted-foreground animate-pulse" />
                <span className="font-mono">{formatElapsed(elapsed)}</span>
                <span className="text-muted-foreground/60">Executing query...</span>
              </>
            )}
          </div>

          {/* --- Direct link to provider console --- */}
          {progress?.url && (
            <a
              href={progress.url}
              target="_blank"
              rel="noopener noreferrer"
              className="mt-2 inline-flex items-center gap-1.5 text-[11px] text-muted-foreground/70 hover:text-foreground transition-colors group"
            >
              <ExternalLink size={11} className="group-hover:translate-x-0.5 transition-transform" />
              <span>{progressLinkLabel}</span>
            </a>
          )}
        </div>
      )}

      <CollapsibleContent>
        {!isDisplayTable && (
          <ScrollArea className="max-h-[200px]">
            <div className="p-3 border-t border-border bg-background/30 font-mono text-xs text-muted-foreground whitespace-pre-wrap break-all">
              {isSandboxedHtmlToolName(toolCall.name)
                ? buildSandboxedHtmlArgsSummary(toolCall.args || {})
                : argsText}
            </div>
            <ScrollBar orientation="vertical" />
          </ScrollArea>
        )}
      </CollapsibleContent>
    </Collapsible>
  );
};

export default ToolCallBlock;
