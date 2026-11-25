import React from 'react';
import { Check, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import type { ProviderConfig } from '@/types';
import { useModelDiscoverySeam, type DiscoveredModel } from './modelDiscoveryContract';

function lifecycleBadge(m: DiscoveredModel): { text: string; className: string } | null {
  if (m.lifecycle === 'recommended') return { text: 'Recommended', className: 'text-emerald-400 bg-emerald-400/10' };
  if (m.lifecycle === 'preview') return { text: 'Preview', className: 'text-sky-400 bg-sky-400/10' };
  if (m.lifecycle === 'deprecated') {
    return {
      text: m.deprecationDate ? `Deprecated — retires ${m.deprecationDate}` : 'Deprecated',
      className: 'text-amber-500 bg-amber-500/10',
    };
  }
  if (m.lifecycle === 'shutdown') return { text: 'Shut down', className: 'text-destructive bg-destructive/10' };
  return null;
}

interface ModelDiscoveryListProps {
  draftConfig: ProviderConfig | null;
  enabled: boolean;
  selectedModel: string;
  onSelectModel: (modelId: string) => void;
}

/**
 * Model discovery with manual fallback (plan §4.2 step 3). When the discovery
 * service is missing (infrastructure not landed) this renders nothing at all and
 * the existing manual model field stands alone — manual entry is never removed.
 * Shut-down models are visible but unselectable; deprecated models select with a
 * visible warning badge.
 */
const ModelDiscoveryList: React.FC<ModelDiscoveryListProps> = ({
  draftConfig, enabled, selectedModel, onSelectModel,
}) => {
  const { status, result, errorMessage, refresh } = useModelDiscoverySeam(draftConfig, enabled);

  if (!enabled || status === 'idle' || status === 'service_missing') return null;

  return (
    <div data-model-discovery className="rounded-md border border-border bg-muted/20 p-2.5 space-y-1.5">
      <div className="flex items-center justify-between gap-2">
        <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
          Discovered models
        </span>
        <div className="flex items-center gap-2">
          {status === 'ready' && result?.fetchedAt && (
            <span className="text-[10px] text-muted-foreground/70">fetched {new Date(result.fetchedAt).toLocaleTimeString()}</span>
          )}
          <Button
            variant="outline"
            size="sm"
            className="h-6 gap-1 px-2 text-[10px]"
            disabled={status === 'loading'}
            onClick={refresh}
          >
            <RefreshCw size={10} aria-hidden /> Refresh
          </Button>
        </div>
      </div>

      <span className="sr-only" role="status" aria-live="polite">
        {status === 'loading' ? 'Discovering models…' : status === 'ready' ? `${result?.models.length ?? 0} models discovered.` : ''}
      </span>

      {status === 'loading' && (
        <div className="space-y-1" aria-hidden>
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-3/4" />
        </div>
      )}

      {status === 'error' && (
        <p className="text-[11px] text-destructive">
          Model discovery failed{errorMessage ? `: ${errorMessage}` : '.'} Enter a model ID manually below.
        </p>
      )}

      {status === 'ready' && result && (
        result.source === 'unavailable' || result.models.length === 0 ? (
          <p className="text-[11px] text-muted-foreground">
            This endpoint doesn’t list models{result.error ? ` (${result.error})` : ''}. Enter a model ID manually below.
          </p>
        ) : (
          <div role="radiogroup" aria-label="Discovered models" className="space-y-1">
            {result.models.map((m) => {
              const badge = lifecycleBadge(m);
              const selected = selectedModel.trim() === m.id;
              const disabled = m.lifecycle === 'shutdown';
              return (
                <button
                  key={m.id}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  aria-disabled={disabled || undefined}
                  disabled={disabled}
                  className="w-full flex items-center gap-2 rounded-md border border-border bg-card/50 px-2.5 py-1.5 min-h-[36px] text-left transition-colors hover:bg-card/80 disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
                  onClick={() => { if (!disabled) onSelectModel(m.id); }}
                >
                  <span className="w-3.5 shrink-0 text-emerald-400" aria-hidden>
                    {selected ? <Check size={13} /> : null}
                  </span>
                  <span className="min-w-0 flex-1 font-mono text-[11px] truncate">{m.id}</span>
                  {m.contextTokens && (
                    <span className="shrink-0 text-[10px] text-muted-foreground">{Math.round(m.contextTokens / 1000)}k ctx</span>
                  )}
                  {badge && (
                    <span className={`shrink-0 rounded px-1.5 py-0.5 text-[10px] font-medium ${badge.className}`}>
                      {badge.text}
                    </span>
                  )}
                </button>
              );
            })}
            <p className="text-[10px] text-muted-foreground/80 pt-0.5">
              Can’t find it? Enter the model ID manually below — it stays unverified until checked.
            </p>
          </div>
        )
      )}
    </div>
  );
};

export default ModelDiscoveryList;
