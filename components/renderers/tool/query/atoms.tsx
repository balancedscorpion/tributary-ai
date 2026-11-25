import React, { useState } from 'react';
import {
  AlertTriangle,
  Check,
  ChevronRight,
  Copy,
  Loader2,
  XCircle,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import type { QueryJobStatus } from '@/types';

/**
 * Shared visual atoms for the query inspector family. Used by both the chat
 * inline surface and the artifact-panel surface so the two stay
 * pixel-identical without each branch reinventing the same widget.
 */

export function CopyIcon({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const onClick = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // ignore
    }
  };
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      className="inline-flex items-center justify-center rounded p-0.5 text-muted-foreground/60 hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
    >
      {copied ? (
        <Check size={12} className="text-emerald-600" aria-hidden />
      ) : (
        <Copy size={12} aria-hidden />
      )}
    </button>
  );
}

export function Disclosure({
  label,
  children,
  defaultOpen = false,
}: {
  label: string;
  children: React.ReactNode;
  defaultOpen?: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
      >
        <ChevronRight
          size={12}
          aria-hidden
          className={cn('transition-transform', open && 'rotate-90')}
        />
        {label}
      </button>
      {open && <div className="mt-2">{children}</div>}
    </div>
  );
}

export type InspectorStatusKind =
  | 'running'
  | 'cancelling'
  | 'warning'
  | 'success'
  | 'error'
  | 'cancelled';

export function statusKindFor(status: QueryJobStatus): InspectorStatusKind {
  if (status === 'completed') return 'success';
  if (status === 'failed' || status === 'expired' || status === 'orphaned') return 'error';
  if (status === 'cancelled') return 'cancelled';
  if (status === 'cancel_requested' || status === 'cancelling') return 'cancelling';
  if (status === 'submission_unknown') return 'warning';
  return 'running';
}

/**
 * StatusPill — the only state-coloured token in the header. Carries an
 * aria-label so screen readers don't have to infer state from icon + colour.
 * `subject` names what completed/failed (defaults to "Query") so the shared
 * inspector family can announce "Slack failed" / "Tool completed" rather than
 * mislabelling every connector as a query.
 */
export function StatusPill({
  status,
  subject = 'Query',
}: {
  status: InspectorStatusKind;
  subject?: string;
}) {
  if (status === 'error') {
    return (
      <span
        aria-label={`${subject} failed`}
        className="inline-flex items-center gap-1 rounded-md border border-destructive/30 bg-destructive/10 px-2 py-0.5 text-[11px] font-medium text-red-400"
      >
        <AlertTriangle size={11} aria-hidden /> Error
      </span>
    );
  }
  if (status === 'running') {
    return (
      <span
        aria-label={`${subject} running`}
        className="inline-flex items-center gap-1 rounded-md border border-blue-500/25 bg-blue-500/10 px-2 py-0.5 text-[11px] font-medium text-blue-400"
      >
        <Loader2 size={11} aria-hidden className="motion-safe:animate-spin" /> Running
      </span>
    );
  }
  if (status === 'cancelling') {
    return (
      <span
        aria-label={`${subject} cancellation pending`}
        className="inline-flex items-center gap-1 rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-400"
      >
        <Loader2 size={11} aria-hidden className="motion-safe:animate-spin" /> Cancelling
      </span>
    );
  }
  if (status === 'warning') {
    return (
      <span
        aria-label={`${subject} status needs reconciliation`}
        className="inline-flex items-center gap-1 rounded-md border border-amber-500/25 bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-400"
      >
        <AlertTriangle size={11} aria-hidden /> Reconciling
      </span>
    );
  }
  if (status === 'cancelled') {
    return (
      <span
        aria-label={`${subject} cancelled`}
        className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/60 px-2 py-0.5 text-[11px] font-medium text-muted-foreground"
      >
        <XCircle size={11} aria-hidden /> Cancelled
      </span>
    );
  }
  return (
    <span
      aria-label={`${subject} completed`}
      className="inline-flex items-center gap-1 rounded-md border border-emerald-500/25 bg-emerald-500/10 px-2 py-0.5 text-[11px] font-medium text-emerald-400"
    >
      <Check size={11} aria-hidden /> Success
    </span>
  );
}

export function Stat({
  icon,
  label,
  highlight,
  mono,
}: {
  icon?: React.ReactNode;
  label: string;
  highlight?: boolean;
  mono?: boolean;
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 text-[11px]',
        highlight ? 'text-emerald-600' : 'text-muted-foreground',
        mono && 'font-mono',
      )}
    >
      {icon}
      {label}
    </span>
  );
}

export function shortId(id: string): string {
  if (!id) return '';
  if (id.length <= 14) return id;
  return `${id.slice(0, 8)}…${id.slice(-4)}`;
}

export function formatDurationMs(ms: number | undefined | null): string | null {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) return null;
  if (ms < 1) return '<1ms';
  if (ms < 1000) return `${Math.round(ms)}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  const m = Math.floor(ms / 60_000);
  const s = Math.round((ms - m * 60_000) / 1000);
  return `${m}m ${s}s`;
}

export function formatClock(ms: number | undefined | null): string | null {
  if (typeof ms !== 'number' || !Number.isFinite(ms)) return null;
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return null;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

export function formatElapsedColon(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${seconds.toString().padStart(2, '0')}`;
}
