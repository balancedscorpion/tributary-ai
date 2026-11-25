import type { Artifact } from "@/types";
import SandboxedHtmlRenderer from "@/components/renderers/type/SandboxedHtmlRenderer";

interface InlineHtmlPreviewProps {
  artifact: Artifact;
  height?: number;
}

const InlineHtmlPreview: React.FC<InlineHtmlPreviewProps> = ({
  artifact,
  height,
}) => {
  const rawHeight = Number.isFinite(height)
    ? Number(height)
    : Number(artifact.metadata?.preview_height);
  const numericHeight = Number.isFinite(rawHeight) ? Math.trunc(rawHeight) : 320;
  const previewHeight = Math.max(220, Math.min(640, numericHeight));
  return (
    <div
      className="min-w-0 bg-background"
      data-testid="inline-html-preview"
      style={{ height: previewHeight, maxHeight: 640 }}
    >
      <SandboxedHtmlRenderer
        artifact={artifact}
        className="h-full min-h-0 rounded-none border-0"
      />
    </div>
  );
};

export default InlineHtmlPreview;
