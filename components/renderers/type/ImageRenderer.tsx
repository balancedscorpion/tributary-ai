import React from 'react';
import { ImageIcon, ImageOff, ZoomIn, ZoomOut } from 'lucide-react';
import { registerTypeRenderer } from '../registry';
import type { RendererProps } from '../types';
import {
  generatedImageAltText,
  isGeneratedImageArtifact,
} from '@/lib/generatedImageArtifacts';
import { artifactRawUrl } from '@/lib/artifactDownload';

const MIN_ZOOM = 0.25;
const MAX_ZOOM = 4;
const ZOOM_STEP = 0.25;

function clampZoom(value: number): number {
  return Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, Number(value.toFixed(2))));
}

/**
 * Subtle, theme-neutral checkerboard rendered directly behind the image so
 * transparent regions read as transparent rather than as the panel background.
 * A single mid-grey at low alpha is legible in both light and dark themes.
 */
const CHECKERBOARD_STYLE: React.CSSProperties = {
  backgroundImage:
    'linear-gradient(45deg, rgba(128,128,128,0.14) 25%, transparent 25%), ' +
    'linear-gradient(-45deg, rgba(128,128,128,0.14) 25%, transparent 25%), ' +
    'linear-gradient(45deg, transparent 75%, rgba(128,128,128,0.14) 75%), ' +
    'linear-gradient(-45deg, transparent 75%, rgba(128,128,128,0.14) 75%)',
  backgroundSize: '18px 18px',
  backgroundPosition: '0 0, 0 9px, 9px -9px, -9px 0',
};

const ImageRenderer: React.FC<RendererProps> = ({ artifact }) => {
  // `null` zoom means Fit (object-contain within the viewport); a number is an
  // explicit scale relative to the image's natural pixel size.
  const [zoom, setZoom] = React.useState<number | null>(null);
  const [natural, setNatural] = React.useState<{ w: number; h: number } | null>(null);
  const [errored, setErrored] = React.useState(false);
  const generated = isGeneratedImageArtifact(artifact);
  const inlineContent = typeof artifact.content === 'string' ? artifact.content : '';
  const imageSource = generated && !inlineContent.startsWith('data:')
    ? artifactRawUrl(artifact.id)
    : inlineContent;

  // A new artifact (or newly hydrated source) is a fresh image: reset view +
  // error state so a prior failed URL does not permanently mask valid bytes.
  React.useEffect(() => {
    setZoom(null);
    setNatural(null);
    setErrored(false);
  }, [artifact.id, imageSource]);

  const isFit = zoom === null;
  const alt = generated
    ? generatedImageAltText(artifact)
    : artifact.title || 'Image';

  if (errored) {
    return (
      <div
        role="status"
        className="flex h-full w-full flex-col items-center justify-center gap-2 bg-muted/20 p-6 text-center text-muted-foreground"
      >
        <ImageOff size={28} aria-hidden="true" />
        <p className="text-sm font-medium text-foreground">Preview unavailable</p>
        <p className="max-w-xs text-xs text-muted-foreground">
          The image could not be displayed. Try downloading it to view the original bytes.
        </p>
      </div>
    );
  }

  const zoomOutDisabled = !isFit && zoom <= MIN_ZOOM;
  const zoomInDisabled = !isFit && zoom >= MAX_ZOOM;

  const imgStyle: React.CSSProperties =
    !isFit && natural
      ? { width: `${natural.w * zoom}px`, height: 'auto', maxWidth: 'none', maxHeight: 'none' }
      : {};
  const imgClass = isFit
    ? 'max-h-full max-w-full object-contain'
    : 'object-contain';

  return (
    <div className="flex h-full w-full flex-col bg-muted/20">
      <div
        role="toolbar"
        aria-label="Image controls"
        className="flex items-center gap-1 border-b border-border/60 bg-card/60 px-2 py-1"
      >
        <button
          type="button"
          onClick={() => setZoom(null)}
          aria-pressed={isFit}
          className={`rounded px-2 py-0.5 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
            isFit
              ? 'bg-muted text-foreground'
              : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'
          }`}
        >
          Fit
        </button>
        <button
          type="button"
          onClick={() => setZoom(1)}
          aria-pressed={zoom === 1}
          className={`rounded px-2 py-0.5 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
            zoom === 1
              ? 'bg-muted text-foreground'
              : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'
          }`}
        >
          100%
        </button>
        <span className="mx-0.5 h-4 w-px bg-border/60" aria-hidden="true" />
        <button
          type="button"
          aria-label="Zoom out"
          disabled={zoomOutDisabled}
          onClick={() => setZoom((z) => clampZoom((z ?? 1) - ZOOM_STEP))}
          className="rounded p-1 text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-40"
        >
          <ZoomOut size={14} aria-hidden="true" />
        </button>
        <span className="min-w-[3rem] text-center text-[11px] tabular-nums text-muted-foreground">
          {isFit ? 'Fit' : `${Math.round(zoom * 100)}%`}
        </span>
        <button
          type="button"
          aria-label="Zoom in"
          disabled={zoomInDisabled}
          onClick={() => setZoom((z) => clampZoom((z ?? 1) + ZOOM_STEP))}
          className="rounded p-1 text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-40"
        >
          <ZoomIn size={14} aria-hidden="true" />
        </button>
      </div>

      <div
        tabIndex={0}
        aria-label={`Image preview: ${alt}`}
        className={`flex flex-1 overflow-auto p-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring ${
          isFit ? 'items-center justify-center' : 'items-start justify-start'
        }`}
      >
        <span className="inline-flex shrink-0 rounded-md" style={CHECKERBOARD_STYLE}>
          <img
            src={imageSource}
            alt={alt}
            style={imgStyle}
            onLoad={(event) =>
              setNatural({
                w: event.currentTarget.naturalWidth,
                h: event.currentTarget.naturalHeight,
              })
            }
            onError={() => setErrored(true)}
            className={`rounded-md shadow-md ${imgClass}`}
          />
        </span>
      </div>
    </div>
  );
};

registerTypeRenderer('image', {
  component: ImageRenderer,
  label: 'Image',
  icon: ImageIcon,
});

export default ImageRenderer;
