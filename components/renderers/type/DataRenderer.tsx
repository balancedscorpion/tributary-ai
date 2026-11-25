import { Database } from "lucide-react";
import DataVisualization from "@/components/DataVisualization";
import { registerTypeRenderer } from "../registry";
import type { RendererProps } from "../types";

const DataRenderer: React.FC<RendererProps> = ({ artifact, settings }) => {
  const rowCountTotalRaw = Number(artifact.metadata?.row_count_total);
  const rowCountTotal =
    Number.isFinite(rowCountTotalRaw) && rowCountTotalRaw >= 0
      ? rowCountTotalRaw
      : undefined;
  const downsampleRatioRaw = Number(artifact.metadata?.downsample_ratio);
  const downsampleRatio =
    Number.isFinite(downsampleRatioRaw) && downsampleRatioRaw > 0
      ? downsampleRatioRaw
      : undefined;

  return (
    <DataVisualization
      key={artifact.id}
      dataString={artifact.content}
      title={artifact.title}
      className="h-full"
      plotSpec={artifact.metadata?.plot_spec}
      chartEngine={artifact.metadata?.chart_engine}
      artifactId={artifact.id}
      partial={
        artifact.metadata?.partial === true ||
        artifact.metadata?.preview_only === true
      }
      fullArtifactId={
        typeof artifact.metadata?.full_artifact_id === "string"
          ? artifact.metadata.full_artifact_id
          : undefined
      }
      rowCountTotal={rowCountTotal}
      downsampleRatio={downsampleRatio}
      chartBudget={artifact.metadata?.chart_budget}
      dimensionFiltersEnabled={
        settings?.featureFlags?.chartDimensionFilters === true
      }
    />
  );
};

registerTypeRenderer("data", {
  component: DataRenderer,
  label: "Interactive Data",
  icon: Database,
});

export default DataRenderer;
