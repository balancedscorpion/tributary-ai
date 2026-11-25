import React from 'react';
import { Info, X } from 'lucide-react';

interface PreflightNoticeRowProps {
  texts: string[];
  onDismiss: () => void;
}

/**
 * Thin non-blocking notice above the composer for notices-only plans
 * (plan §4.4): the send proceeds without another click; this only informs.
 */
const PreflightNoticeRow: React.FC<PreflightNoticeRowProps> = ({ texts, onDismiss }) => {
  if (texts.length === 0) return null;
  return (
    <div
      data-preflight-notice-row
      role="status"
      className="mx-auto w-full max-w-4xl px-4"
    >
      <div className="flex items-start gap-1.5 rounded-lg border border-amber-500/30 bg-amber-500/5 px-2.5 py-1.5 text-[11px] text-muted-foreground">
        <Info size={12} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
        <span className="min-w-0 flex-1">{texts.join(' ')}</span>
        <button
          type="button"
          aria-label="Dismiss notice"
          className="shrink-0 rounded-sm p-0.5 text-muted-foreground/70 hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
          onClick={onDismiss}
        >
          <X size={12} aria-hidden />
        </button>
      </div>
    </div>
  );
};

export default PreflightNoticeRow;
