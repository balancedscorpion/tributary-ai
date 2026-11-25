import React, { useMemo } from 'react';
import { BarChart2 } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import DataVisualization from '@/components/DataVisualization';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';

const AggregateCsvRenderer: React.FC<RendererProps> = ({ artifact, settings }) => {
  const plotSpec = artifact.metadata?.plot_spec
    || ((artifact.metadata?.raw_result as any)?.plot_spec ?? null);
  const chartEngine = artifact.metadata?.chart_engine
    || ((artifact.metadata?.raw_result as any)?.chart_engine ?? null);
  const rowCountTotalRaw = Number(
    artifact.metadata?.row_count_total
    ?? (artifact.metadata?.raw_result as any)?.row_count_total
  );
  const rowCountTotal = Number.isFinite(rowCountTotalRaw) && rowCountTotalRaw >= 0
    ? rowCountTotalRaw
    : undefined;
  const downsampleRatioRaw = Number(
    artifact.metadata?.downsample_ratio
    ?? (artifact.metadata?.raw_result as any)?.downsample_ratio
  );
  const downsampleRatio = Number.isFinite(downsampleRatioRaw) && downsampleRatioRaw > 0
    ? downsampleRatioRaw
    : undefined;

  const { results, dataString } = useMemo(() => {
    const raw = artifact.metadata?.raw_result as { results?: unknown[] } | undefined;
    if (Array.isArray(raw?.results) && raw.results.length > 0) {
      return { results: raw.results, dataString: JSON.stringify(raw.results) };
    }
    // Fallback: try parsing artifact.content
    try {
      const parsed = JSON.parse(artifact.content);
      const arr = Array.isArray(parsed) ? parsed : parsed?.results;
      if (Array.isArray(arr) && arr.length > 0) {
        return { results: arr, dataString: JSON.stringify(arr) };
      }
    } catch {
      // not parseable
    }
    return { results: null, dataString: null };
  }, [artifact]);

  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border min-w-0">
        <BarChart2 size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs font-semibold text-foreground truncate">
          {artifact.title || 'Aggregation Results'}
        </span>
        {results && (
          <Badge variant="secondary" className="text-[10px] shrink-0">
            {results.length} group{results.length !== 1 ? 's' : ''}
          </Badge>
        )}
      </div>

      {/* Body */}
      <div className="flex-1 overflow-auto p-3">
        {dataString ? (
          <DataVisualization
            key={artifact.id}
            dataString={dataString}
            title={artifact.title}
            plotSpec={plotSpec}
            chartEngine={chartEngine || undefined}
            artifactId={artifact.id}
            partial={artifact.metadata?.partial === true || artifact.metadata?.preview_only === true}
            fullArtifactId={typeof artifact.metadata?.full_artifact_id === 'string' ? artifact.metadata.full_artifact_id : undefined}
            rowCountTotal={rowCountTotal}
            downsampleRatio={downsampleRatio}
            dimensionFiltersEnabled={settings?.featureFlags?.chartDimensionFilters === true}
          />
        ) : (
          <pre className="p-4 text-xs font-mono text-foreground whitespace-pre-wrap">
            {artifact.content}
          </pre>
        )}
      </div>
    </div>
  );
};

registerToolRenderer('aggregate_csv', {
  component: AggregateCsvRenderer,
  label: 'Aggregation Results',
  icon: BarChart2,
});

export default AggregateCsvRenderer;
