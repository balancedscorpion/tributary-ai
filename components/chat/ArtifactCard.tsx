import React from 'react';
import { Table as TableIcon, Code2, ImageIcon, ExternalLink, MessageSquarePlus, MessageSquareDashed, BarChart2, FileText, PieChart, TrendingUp, Maximize2, Download } from 'lucide-react';
import { Card } from '@/components/ui/card';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { hasNonCollapsedSelectionWithin } from '@/lib/textSelection';

const InlineChartPreview = React.lazy(() => import('./InlineChartPreview'));

interface ArtifactCardProps {
  title: string;
  type: 'data' | 'chart' | 'code' | 'image' | 'file';
  details?: React.ReactNode;
  thumbnailSrc?: string;
  /** Raw echarts/highcharts plot spec — when present on a chart card, renders an actual inline chart preview. */
  plotSpec?: unknown;
  preview?: React.ReactNode;
  testId?: string;
  onClick: () => void;
  onDownload?: (e: React.MouseEvent) => void;
  onPin?: (e: React.MouseEvent) => void;
  isPinned?: boolean;
}

const typeConfig: Record<string, { bg: string; text: string; Icon: React.ComponentType<{ size?: number }> }> = {
  data:  { bg: 'bg-artifact-data/10',  text: 'text-artifact-data',  Icon: TableIcon },
  chart: { bg: 'bg-artifact-chart/10', text: 'text-artifact-chart', Icon: BarChart2 },
  code:  { bg: 'bg-artifact-code/10',  text: 'text-artifact-code',  Icon: Code2 },
  file:  { bg: 'bg-artifact-file/10',  text: 'text-artifact-file',  Icon: FileText },
  image: { bg: 'bg-purple-500/10',     text: 'text-purple-400',     Icon: ImageIcon },
};

/** Derive the correct chart icon from the ECharts/Highcharts spec series type. */
function getChartIcon(plotSpec: unknown): React.ComponentType<{ size?: number }> {
  if (!plotSpec || typeof plotSpec !== 'object') return BarChart2;
  const spec = plotSpec as Record<string, unknown>;
  const options = (spec.options || spec) as Record<string, unknown>;
  const series = options?.series;
  if (!Array.isArray(series) || series.length === 0) return BarChart2;
  const firstType = typeof series[0]?.type === 'string' ? series[0].type.toLowerCase() : '';
  if (firstType === 'pie' || firstType === 'funnel' || firstType === 'sunburst' || firstType === 'treemap') return PieChart;
  if (firstType === 'line' || firstType === 'area' || firstType === 'scatter') return TrendingUp;
  return BarChart2;
}

const ArtifactCard: React.FC<ArtifactCardProps> = ({ title, type, details, thumbnailSrc, plotSpec, preview, testId, onClick, onDownload, onPin, isPinned }) => {
  const cfg = typeConfig[type] || typeConfig.code;
  const showChartPreview = type === 'chart' && plotSpec && !thumbnailSrc;
  const showCustomPreview = Boolean(preview);
  const hasInlinePreview = showChartPreview || showCustomPreview;

  // Use the correct chart-type icon when we have a plotSpec
  const FooterIcon = (type === 'chart' && plotSpec) ? getChartIcon(plotSpec) : cfg.Icon;

  return (
    <Card
      data-testid={testId}
      onClick={(e) => {
        if (hasNonCollapsedSelectionWithin(e.currentTarget as HTMLElement)) return;
        onClick();
      }}
      className={`group my-2 cursor-pointer transition-all duration-200
        border bg-card/50 hover:bg-card
        hover:shadow-lg hover:shadow-black/20
        overflow-hidden
        ${isPinned ? 'border-sky-400/50 hover:border-sky-400/70 shadow-[0_0_12px_-4px_hsl(200_80%_60%/0.2)]' : 'border-border/40 hover:border-border'}
        ${hasInlinePreview ? 'flex min-w-0 max-w-full flex-col' : 'flex min-w-0 max-w-full items-center gap-3 p-3'}`}
    >
      {/* Inline chart preview — rendered above the card footer for chart artifacts */}
      {showChartPreview && (
        <div className="relative border-b border-border/30">
          <React.Suspense fallback={<div className="h-[180px] w-full bg-muted/20 animate-pulse rounded-t-lg" />}>
            <InlineChartPreview plotSpec={plotSpec} height={180} />
          </React.Suspense>
          {/* Expand affordance overlay */}
          <div className="absolute inset-0 flex items-center justify-center bg-black/0 group-hover:bg-black/20 transition-colors duration-200">
            <div className="opacity-0 group-hover:opacity-100 transition-opacity duration-200 bg-black/60 rounded-full p-2">
              <Maximize2 size={16} className="text-white" />
            </div>
          </div>
        </div>
      )}

      {showCustomPreview && (
        <div
          className="relative border-b border-border/30"
          onClick={(e) => e.stopPropagation()}
        >
          {preview}
        </div>
      )}

      {/* Card footer row */}
      <div className={`flex min-w-0 max-w-full items-center gap-3 ${hasInlinePreview ? 'px-3 py-2.5' : ''}`}>
        {thumbnailSrc ? (
          <img src={thumbnailSrc} alt="" className="h-10 w-10 rounded-lg object-cover shrink-0" />
        ) : (
          <div className={`p-2 rounded-lg ${cfg.bg} ${cfg.text}`}>
            <FooterIcon size={20} />
          </div>
        )}
        <div className="flex-1 min-w-0">
          <h4 className="text-sm font-medium text-card-foreground truncate group-hover:text-foreground transition-colors">
            {title}
          </h4>
          {details && (
            <p className="text-xs text-muted-foreground truncate group-hover:text-muted-foreground/80">
              {details}
            </p>
          )}
        </div>
        {onDownload && (
          <Tooltip>
            <TooltipTrigger asChild>
              <button
                type="button"
                onClick={(e) => { e.stopPropagation(); onDownload(e); }}
                className="group/download shrink-0 inline-flex items-center gap-1 p-1 rounded-md text-muted-foreground/35 hover:text-muted-foreground/70 transition-all"
                aria-label="Download artifact"
              >
                <Download size={14} />
              </button>
            </TooltipTrigger>
            <TooltipContent>Download artifact</TooltipContent>
          </Tooltip>
        )}
        {onPin && (
          <button
            onClick={(e) => { e.stopPropagation(); onPin(e); }}
            className={`group/pin shrink-0 inline-flex items-center gap-1 p-1 rounded-md transition-all
              ${isPinned
                ? 'text-sky-400 opacity-100'
                : 'text-muted-foreground/30 hover:text-muted-foreground/60'
              }`}
          >
            {isPinned ? <MessageSquareDashed size={14} /> : <MessageSquarePlus size={14} />}
          </button>
        )}
        <ExternalLink size={14} className="text-muted-foreground/40 group-hover:text-primary opacity-0 group-hover:opacity-100 transition-all" />
      </div>
    </Card>
  );
};

export default ArtifactCard;
