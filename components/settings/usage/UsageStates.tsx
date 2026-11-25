import React from 'react';
import { AlertCircle, BarChart3, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { cn } from '@/lib/utils';

export function LoadingState() {
  return (
    <div className="space-y-5" aria-live="polite" aria-busy="true">
      <span className="sr-only">Loading token usage…</span>
      <Skeleton className="h-56" />
      <Skeleton className="h-72" />
      <Skeleton className="h-40" />
    </div>
  );
}

export function StateMessage({
  kind,
  title,
  message,
  onRetry,
}: {
  kind: 'error' | 'unavailable' | 'empty';
  title: string;
  message: string;
  onRetry?: () => void;
}) {
  const Icon = kind === 'error' ? AlertCircle : BarChart3;
  return (
    <div className="flex min-h-64 flex-col items-center justify-center rounded-lg border border-dashed px-6 py-12 text-center" role={kind === 'error' ? 'alert' : 'status'}>
      <div className={cn(
        'mb-3 rounded-full p-2.5',
        kind === 'error' ? 'bg-destructive/10 text-destructive' : 'bg-muted text-muted-foreground',
      )}>
        <Icon className="h-5 w-5" aria-hidden />
      </div>
      <h3 className="text-sm font-semibold">{title}</h3>
      <p className="mt-1 max-w-md text-sm text-muted-foreground">{message}</p>
      {onRetry && (
        <Button className="mt-4" variant="outline" size="sm" onClick={onRetry}>
          <RefreshCw aria-hidden /> Retry
        </Button>
      )}
    </div>
  );
}

export function TrackingNote({ trackingStartedAt }: { trackingStartedAt: string | null }) {
  return (
    <div className="flex gap-3 rounded-lg border bg-muted/20 p-3 text-xs text-muted-foreground" data-testid="usage-tracking-note">
      <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
      <p>
        {trackingStartedAt
          ? <>Detailed per-invocation tracking began <span className="font-medium text-foreground">{trackingStartedAt}</span>. Earlier activity is not included because legacy run totals can be incomplete. Usage records are retained independently and are not deleted with a thread.</>
          : <>No detailed invocations have been recorded yet. Legacy run totals are not included because they can be incomplete. Usage records are retained independently and are not deleted with a thread.</>}
      </p>
    </div>
  );
}
