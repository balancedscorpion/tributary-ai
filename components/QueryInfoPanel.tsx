import React from 'react';
import { Copy, ExternalLink } from 'lucide-react';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import type { QueryMetadata } from '@/types';
import { formatBytes, formatDuration, formatRowCount } from '@/lib/formatters';
import {
  getQueryProviderLabel,
  getQueryProviderProgressLinkLabel,
  normalizeQueryProvider,
  resolveQueryNativeJobLink,
} from '@/lib/queryProviderRegistry';
import { toast } from 'sonner';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';

interface QueryInfoPanelProps {
  metadata: QueryMetadata;
  rowCount?: number;
}

function CopyButton({ text, label }: { text: string; label?: string }) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          className="inline-flex items-center gap-1 p-1 rounded text-muted-foreground/40 hover:text-muted-foreground hover:bg-muted/50 transition-colors"
          onClick={() => {
            navigator.clipboard.writeText(text);
            toast.success('Copied to clipboard');
          }}
        >
          <Copy size={12} />
        </button>
      </TooltipTrigger>
      <TooltipContent>{label || 'Copy'}</TooltipContent>
    </Tooltip>
  );
}

function InfoField({ label, value, copyable, mono }: { label: string; value: string; copyable?: boolean; mono?: boolean }) {
  return (
    <div className="flex flex-col gap-1 py-3 border-b border-border/50 last:border-b-0">
      <span className="text-[10px] text-muted-foreground uppercase tracking-wider font-semibold">{label}</span>
      <div className="flex items-center gap-1.5">
        <span className={`text-sm text-foreground ${mono ? 'font-mono' : ''}`}>{value}</span>
        {copyable && <CopyButton text={value} label={`Copy ${label}`} />}
      </div>
    </div>
  );
}

function InfoLinkField({ label, href, value }: { label: string; href: string; value: string }) {
  return (
    <div className="flex flex-col gap-1 py-3 border-b border-border/50 last:border-b-0">
      <span className="text-[10px] text-muted-foreground uppercase tracking-wider font-semibold">{label}</span>
      <a
        href={href}
        target="_blank"
        rel="noreferrer"
        data-testid="query-info-native-link"
        className="inline-flex items-center gap-1.5 text-sm font-mono text-accent hover:text-accent/80 transition-colors break-all"
      >
        <span>{value}</span>
        <ExternalLink size={12} />
      </a>
    </div>
  );
}

function normalizeString(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed ? trimmed : null;
}

const QueryInfoPanel: React.FC<QueryInfoPanelProps> = ({ metadata, rowCount }) => {
  const qm = metadata;
  const provider = normalizeQueryProvider(qm.provider, 'bigquery');
  const providerLabel = getQueryProviderLabel(provider);
  const providerLinkLabel = getQueryProviderProgressLinkLabel(provider);
  const managedJobId = normalizeManagedQueryJobId(qm.job_id);
  const nativeJobLink = resolveQueryNativeJobLink(provider, {
    providerJobId: normalizeString(qm.provider_job_id),
    providerJobUrl: normalizeString(qm.provider_job_url),
    warehouseJobId: normalizeString(qm.warehouse_job_id),
    statementId: normalizeString(qm.statement_id),
    queryId: normalizeString(qm.query_id),
    projectId: normalizeString(qm.project_id),
    accountIdentifier: normalizeString(qm.account_identifier),
    location: normalizeString(qm.job_location) || normalizeString(qm.location),
    host: normalizeString(qm.host),
    url: normalizeString(qm.url),
    jobId: managedJobId,
  });

  return (
    <div className="h-full overflow-auto p-4">
      <div className="max-w-lg space-y-6">
        {/* Performance section */}
        <div>
          <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1">Performance</h3>
          <div className="bg-card/50 rounded-lg border border-border px-4">
            {qm.duration_ms !== undefined && (
              <InfoField label="Duration" value={formatDuration(qm.duration_ms)} />
            )}
            {qm.configured_limit !== undefined && (
              <InfoField label="Configured row cap" value={formatRowCount(qm.configured_limit)} />
            )}
            {qm.requested_limit !== undefined && (
              <InfoField label="Requested row limit" value={formatRowCount(qm.requested_limit)} />
            )}
            {qm.effective_limit !== undefined && (
              <InfoField label="Applied row limit" value={formatRowCount(qm.effective_limit)} />
            )}
            {qm.limit_capped !== undefined && (
              <InfoField label="Limit capped" value={qm.limit_capped ? 'Yes' : 'No'} />
            )}
            {rowCount !== undefined && rowCount > 0 && (
              <InfoField label="Rows returned" value={formatRowCount(rowCount)} />
            )}
            {qm.total_bytes_processed !== undefined && (
              <InfoField label="Data scanned" value={formatBytes(qm.total_bytes_processed)} />
            )}
            {qm.cache_hit !== undefined && (
              <InfoField label="Cache hit" value={qm.cache_hit ? 'Yes' : 'No'} />
            )}
          </div>
        </div>

        {/* Identity section */}
        <div>
          <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider mb-1">Identity</h3>
          <div className="bg-card/50 rounded-lg border border-border px-4">
            {qm.provider && (
              <InfoField label="Provider" value={providerLabel} />
            )}
            {managedJobId && (
              <InfoField label="Job ID" value={managedJobId} copyable mono />
            )}
            {nativeJobLink && (
              <InfoLinkField label={providerLinkLabel} href={nativeJobLink.url} value={nativeJobLink.id} />
            )}
            {qm.statement_id && (
              <InfoField label="Statement ID" value={qm.statement_id} copyable mono />
            )}
            {qm.job_location && (
              <InfoField label="Location" value={qm.job_location} />
            )}
          </div>
        </div>
      </div>
    </div>
  );
};

export default QueryInfoPanel;
