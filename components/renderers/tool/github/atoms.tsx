import React, { useEffect, useState } from 'react';
import { ExternalLink } from 'lucide-react';
import { cn } from '@/lib/utils';

/** A single state-pill vocabulary reused by every GitHub widget. */
export type StateKind =
  | 'open'
  | 'closed'
  | 'merged'
  | 'draft'
  | 'success'
  | 'failed'
  | 'cancelled'
  | 'skipped'
  | 'pending'
  | 'queued'
  | 'in_progress'
  | 'action_required'
  | 'mergeable'
  | 'conflicting'
  | 'not_planned'
  | 'completed'
  | 'unknown';

const PILL_BASE =
  'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px] font-medium leading-none';

const PILL_STYLES: Record<StateKind, string> = {
  open: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-600',
  success: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-600',
  completed: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-600',
  mergeable: 'border-emerald-500/25 bg-emerald-500/10 text-emerald-600',
  merged: 'border-purple-500/30 bg-purple-500/10 text-purple-500',
  failed: 'border-destructive/30 bg-destructive/10 text-destructive',
  conflicting: 'border-destructive/30 bg-destructive/10 text-destructive',
  not_planned: 'border-destructive/30 bg-destructive/10 text-destructive',
  pending: 'border-amber-500/30 bg-amber-500/10 text-amber-600',
  queued: 'border-amber-500/30 bg-amber-500/10 text-amber-600',
  action_required: 'border-amber-500/30 bg-amber-500/10 text-amber-600',
  in_progress: 'border-amber-500/30 bg-amber-500/10 text-amber-600',
  draft: 'border-border bg-muted/40 text-muted-foreground',
  closed: 'border-border bg-muted/40 text-muted-foreground',
  cancelled: 'border-border bg-muted/40 text-muted-foreground',
  skipped: 'border-border bg-muted/40 text-muted-foreground',
  unknown: 'border-border bg-muted/40 text-muted-foreground',
};

export function StatePill({
  kind,
  label,
  className,
}: {
  kind: StateKind;
  label?: string;
  className?: string;
}) {
  return (
    <span className={cn(PILL_BASE, PILL_STYLES[kind], className)}>
      {label ?? humanize(kind)}
    </span>
  );
}

export function humanize(s: string): string {
  return s.replace(/_/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());
}

/** Normalise free-form server strings into our internal vocabulary. */
export function classifyState(value: unknown, hint?: string): StateKind {
  const v = String(value || '').toLowerCase();
  if (!v) return 'unknown';
  if (v === 'open') return 'open';
  if (v === 'closed') {
    if (hint === 'pr-merged') return 'merged';
    if (hint === 'issue-not-planned') return 'not_planned';
    if (hint === 'issue-completed') return 'completed';
    return 'closed';
  }
  if (v === 'merged') return 'merged';
  if (v === 'draft') return 'draft';
  if (
    v === 'success' ||
    v === 'passed' ||
    v === 'operational' ||
    v === 'none' ||
    v === 'ok' ||
    v === 'healthy'
  )
    return 'success';
  if (v === 'failure' || v === 'failed' || v === 'error') return 'failed';
  if (v === 'cancelled' || v === 'canceled') return 'cancelled';
  if (v === 'skipped' || v === 'neutral') return 'skipped';
  if (v === 'queued' || v === 'requested' || v === 'waiting') return 'queued';
  if (v === 'in_progress' || v === 'running' || v === 'pending') return 'in_progress';
  if (v === 'action_required') return 'action_required';
  if (v === 'completed') return 'completed';
  if (v === 'mergeable') return 'mergeable';
  if (v === 'conflicting' || v === 'dirty') return 'conflicting';
  if (v === 'not_planned') return 'not_planned';
  return 'unknown';
}

/** Relative-time string with absolute ISO tooltip. */
export function RelativeTime({
  value,
  className,
  prefix,
}: {
  value: unknown;
  className?: string;
  prefix?: string;
}) {
  if (typeof value !== 'string' || !value) return null;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return null;
  const now = Date.now();
  const diff = now - d.getTime();
  const absSec = Math.abs(Math.round(diff / 1000));
  let rel: string;
  if (absSec < 60) rel = `${absSec}s`;
  else if (absSec < 3600) rel = `${Math.round(absSec / 60)}m`;
  else if (absSec < 86400) rel = `${Math.round(absSec / 3600)}h`;
  else if (absSec < 86400 * 7) rel = `${Math.round(absSec / 86400)}d`;
  else if (absSec < 86400 * 30) rel = `${Math.round(absSec / (86400 * 7))}w`;
  else if (absSec < 86400 * 365) rel = `${Math.round(absSec / (86400 * 30))}mo`;
  else rel = `${Math.round(absSec / (86400 * 365))}y`;
  const phrase = diff < 0 ? `in ${rel}` : `${rel} ago`;
  const iso = d.toISOString();
  const display = prefix ? `${prefix} ${phrase}` : phrase;
  return (
    <time dateTime={iso} title={iso} className={className}>
      {display}
    </time>
  );
}

/** Avatar with initials fallback. */
export function Avatar({
  url,
  login,
  size = 16,
  className,
}: {
  url?: string | null;
  login?: string | null;
  size?: 16 | 20 | 24 | 28 | 32;
  className?: string;
}) {
  // Reset the failure flag if `url` changes — the same Avatar instance can be
  // reused across artifact swaps in list rows.
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    setFailed(false);
  }, [url]);
  const px = `${size}px`;
  const initials = (login || '?').slice(0, 2).toUpperCase();
  const showImg = !!url && !failed;
  return (
    <span
      style={{ width: px, height: px }}
      className={cn(
        'inline-flex shrink-0 items-center justify-center overflow-hidden rounded-md border border-border bg-muted/50 text-[9px] font-medium text-muted-foreground',
        className,
      )}
      aria-hidden={!login}
      title={login || undefined}
    >
      {showImg ? (
        // eslint-disable-next-line jsx-a11y/alt-text
        <img
          src={url}
          alt={login || ''}
          loading="lazy"
          referrerPolicy="no-referrer"
          className="h-full w-full object-cover"
          onError={() => setFailed(true)}
        />
      ) : (
        initials
      )}
    </span>
  );
}

export interface AvatarRef {
  login: string;
  avatar_url?: string | null;
}

export function AvatarStack({
  items,
  max = 5,
  size = 16,
  className,
}: {
  items: AvatarRef[];
  max?: number;
  size?: 16 | 20 | 24;
  className?: string;
}) {
  if (!items.length) return null;
  const shown = items.slice(0, max);
  const extra = items.length - shown.length;
  return (
    <span className={cn('inline-flex items-center', className)}>
      {shown.map((a, i) => (
        <span key={`${a.login}-${i}`} className={i > 0 ? '-ml-1.5' : ''}>
          <Avatar url={a.avatar_url} login={a.login} size={size} />
        </span>
      ))}
      {extra > 0 && (
        <span className="ml-1 text-[11px] text-muted-foreground">
          +{extra}
        </span>
      )}
    </span>
  );
}

/** External link icon — only render when href is truthy. */
export function ExternalLinkIcon({
  href,
  label,
  className,
}: {
  href?: string | null;
  label: string;
  className?: string;
}) {
  if (!href) return null;
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      aria-label={label}
      title={label}
      className={cn(
        'inline-flex items-center justify-center rounded p-0.5 text-muted-foreground/60 hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
        className,
      )}
    >
      <ExternalLink size={12} aria-hidden />
    </a>
  );
}

/** Two-column metadata grid that stacks at narrow widths. */
export function MetaGrid({
  rows,
}: {
  rows: Array<{ label: string; value: React.ReactNode; mono?: boolean } | null>;
}) {
  const filtered = rows.filter(Boolean) as Array<{
    label: string;
    value: React.ReactNode;
    mono?: boolean;
  }>;
  if (!filtered.length) return null;
  return (
    <dl className="grid grid-cols-1 gap-x-4 gap-y-2 sm:grid-cols-[max-content_1fr]">
      {filtered.map((r) => (
        <React.Fragment key={r.label}>
          <dt className="text-[11px] uppercase tracking-wide text-muted-foreground/60 sm:py-0.5">
            {r.label}
          </dt>
          <dd
            className={cn(
              'min-w-0 text-[13px] text-foreground [overflow-wrap:anywhere]',
              r.mono && 'font-mono text-[12px]',
            )}
          >
            {r.value}
          </dd>
        </React.Fragment>
      ))}
    </dl>
  );
}

/** Small zone label used above every Response subzone. */
export function ZoneLabel({ children }: { children: React.ReactNode }) {
  return (
    <div className="mb-1.5 text-xs text-muted-foreground/60">{children}</div>
  );
}

/** A coloured dot used in dense rows (state on workflow runs etc.). */
export function StatusDot({
  kind,
  pulse,
  className,
}: {
  kind: StateKind;
  pulse?: boolean;
  className?: string;
}) {
  const color =
    kind === 'success' || kind === 'open' || kind === 'completed' || kind === 'mergeable'
      ? 'bg-emerald-500'
      : kind === 'failed' || kind === 'conflicting' || kind === 'not_planned'
        ? 'bg-destructive'
        : kind === 'in_progress' || kind === 'pending' || kind === 'queued' || kind === 'action_required'
          ? 'bg-amber-500'
          : kind === 'merged'
            ? 'bg-purple-500'
            : 'bg-muted-foreground/50';
  return (
    <span
      className={cn(
        'inline-block h-1.5 w-1.5 shrink-0 rounded-full',
        color,
        pulse && 'animate-pulse',
        className,
      )}
      aria-hidden
    />
  );
}

/** Generic chip used for branches, scopes, labels, etc. */
export function Chip({
  children,
  mono,
  outline,
  className,
}: {
  children: React.ReactNode;
  mono?: boolean;
  outline?: boolean;
  className?: string;
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-[11px] leading-none',
        outline
          ? 'border-border bg-transparent text-muted-foreground'
          : 'border-border bg-muted/40 text-foreground',
        mono && 'font-mono',
        className,
      )}
    >
      {children}
    </span>
  );
}

/** Render the actor for list-row meta lines. */
export function ActorInline({
  login,
  avatarUrl,
}: {
  login?: string | null;
  avatarUrl?: string | null;
}) {
  if (!login) return null;
  return (
    <span className="inline-flex items-center gap-1">
      <Avatar url={avatarUrl} login={login} size={16} />
      <span className="font-medium text-foreground/80">{login}</span>
    </span>
  );
}

/** Quiet section header used inside detail widgets. */
export function SectionHeading({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        'mb-2 text-[11px] uppercase tracking-wide text-muted-foreground/60',
        className,
      )}
    >
      {children}
    </div>
  );
}
