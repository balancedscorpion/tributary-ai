import React from "react";
import { AlertTriangle } from "lucide-react";
import type { ChartBudgetReport } from "@/types";

interface ChartRenderErrorBoundaryProps {
  children: React.ReactNode;
  artifactId?: string;
  engine?: string;
  chartBudget?: ChartBudgetReport;
  resetKey?: string;
  className?: string;
}

interface ChartRenderErrorBoundaryState {
  error: Error | null;
}

class ChartRenderErrorBoundary extends React.Component<
  ChartRenderErrorBoundaryProps,
  ChartRenderErrorBoundaryState
> {
  state: ChartRenderErrorBoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): ChartRenderErrorBoundaryState {
    return { error };
  }

  componentDidCatch(error: Error, errorInfo: React.ErrorInfo): void {
    const report = {
      ...(this.props.chartBudget || {}),
      artifactId: String(this.props.artifactId || "").trim(),
      engine: String(this.props.engine || "").trim(),
      error: String(error?.message || error || "Unknown render error"),
      stack: typeof error?.stack === "string" ? error.stack : undefined,
      componentStack: errorInfo?.componentStack,
    };
    console.error("chart_render_failure", report);
  }

  componentDidUpdate(prevProps: ChartRenderErrorBoundaryProps): void {
    if (prevProps.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  renderFallback(sourceError: string | null): React.ReactNode {
    const stage = String(this.props.chartBudget?.failure_stage || "").trim();
    const code = String(this.props.chartBudget?.failure_code || "").trim();
    const budgetMessage = String(
      this.props.chartBudget?.failure_message || "",
    ).trim();
    const detail = [stage, code].filter(Boolean).join(" / ");
    const message =
      sourceError ||
      budgetMessage ||
      "Chart failed to render for this dataset.";

    return (
      <div
        data-testid="chart-render-fallback"
        className={[
          this.props.className || "",
          "rounded-md border border-destructive/40 bg-destructive/10 px-4 py-3 text-destructive",
          "flex flex-col justify-center gap-1",
        ]
          .join(" ")
          .trim()}
      >
        <div className="inline-flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wide">
          <AlertTriangle size={13} />
          Chart rendering unavailable
        </div>
        <div className="text-xs">{message}</div>
        {detail && <div className="text-[11px] opacity-80">{detail}</div>}
      </div>
    );
  }

  render(): React.ReactNode {
    const prefailureStage = String(this.props.chartBudget?.failure_stage || "")
      .trim()
      .toLowerCase();
    if (prefailureStage === "frontend_render") {
      return this.renderFallback(null);
    }

    if (this.state.error) {
      const rendered = String(this.state.error?.message || "").trim();
      return this.renderFallback(rendered || null);
    }

    return this.props.children;
  }
}

export default ChartRenderErrorBoundary;
