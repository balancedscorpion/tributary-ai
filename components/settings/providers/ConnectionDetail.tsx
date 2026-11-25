import React, { useState } from 'react';
import { ArrowLeft, FileCheck2, Pencil, ShieldQuestion, Star, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import type { ProviderConfig, ProviderWebSearchConfig } from '@/types';
import type { CapabilityControlSetting, FeatureKey } from '@/lib/capability/types';
import {
  HOSTED_CODE_EXECUTION_FEATURE,
  HOSTED_WEB_SEARCH_FEATURE,
  deriveProviderApiSurface,
  preserveCapabilityControls,
} from '@/lib/capability/providerConfig';
import { invalidateConnectionCapabilityReport } from '@/hooks/useCapabilityReport';
import { getProviderDef } from '@/providers/catalog/providerDefs';
import { describeSurface } from './surfaceIdentity';
import { useCapabilityReportSeam } from './capabilityContract';
import type { CapabilityUxFlags } from './capabilityUxFlags';
import CapabilityMatrix from './CapabilityMatrix';
import CapabilityCheckPanel from './CapabilityCheckPanel';
import { canonicalProviderWebSearch } from '@/lib/providerWebSearch';

/** Attachment-handling policies (plan §5.6 `attachmentPolicies`, typed in types.ts). */
interface AttachmentPolicyView {
  id: string;
  klass: string;
  action: string;
  apiSurface: string;
  createdAt: number | null;
}

function readAttachmentPolicies(cfg: ProviderConfig): AttachmentPolicyView[] {
  if (!Array.isArray(cfg.attachmentPolicies)) return [];
  return cfg.attachmentPolicies
    .map((p): AttachmentPolicyView | null => {
      const klass = String(p?.class || '').trim();
      const action = String(p?.action || '').trim();
      if (!klass || !action) return null;
      return {
        id: String(p.id || `${klass}:${action}`),
        klass,
        action,
        apiSurface: String(p.apiSurface || ''),
        createdAt: typeof p.createdAt === 'number' ? p.createdAt : null,
      };
    })
    .filter(Boolean) as AttachmentPolicyView[];
}

function humanizeToken(token: string): string {
  const t = token.replace(/[_-]+/g, ' ').trim();
  return t ? t.charAt(0).toUpperCase() + t.slice(1) : token;
}

function emptyWebSearch(enabled: boolean, providerId: ProviderConfig['providerId']): ProviderWebSearchConfig {
  return canonicalProviderWebSearch(providerId, enabled);
}

interface ConnectionDetailProps {
  cfg: ProviderConfig;
  isActive: boolean;
  flags: CapabilityUxFlags;
  onBack: () => void;
  onUse: () => void;
  onEdit: () => void;
  /** Persist an updated config through the normal settings flow (controls flag). */
  onUpdateConfig?: (next: ProviderConfig) => Promise<void>;
  /** Deep link to the Permissions surface, when the host can provide one. */
  onOpenPermissions?: () => void;
}

/**
 * Non-modal connection detail pane (plan §4.7): identity header, capability
 * summary with evidence (interactive controls when the `controls` flag is on),
 * checks, and attachment-handling policies with revocation.
 */
const ConnectionDetail: React.FC<ConnectionDetailProps> = ({
  cfg, isActive, flags, onBack, onUse, onEdit, onUpdateConfig, onOpenPermissions,
}) => {
  const def = getProviderDef(cfg.providerId);
  const surface = describeSurface(cfg.providerId, cfg.apiType);
  const { status, report, errorMessage, refetch } = useCapabilityReportSeam(cfg, true, { savedConnection: true });
  const name = cfg.displayName || def.name;
  const policies = readAttachmentPolicies(cfg);
  const currentApiSurface = deriveProviderApiSurface(cfg);
  const [revokingPolicyId, setRevokingPolicyId] = useState<string | null>(null);
  const [policyAnnouncement, setPolicyAnnouncement] = useState('');

  const interactive = flags.controls && Boolean(onUpdateConfig);

  const applyControlChange = async (feature: FeatureKey, setting: CapabilityControlSetting) => {
    if (!onUpdateConfig) return;
    const controls = { ...(preserveCapabilityControls(cfg.capabilityControls) || {}), [feature]: setting };
    let next: ProviderConfig = { ...cfg, capabilityControls: controls, updatedAt: Date.now() };
    // Runtime still reads the legacy hosted booleans; keep the mirror consistent.
    if (feature === HOSTED_WEB_SEARCH_FEATURE) {
      next = {
        ...next,
        webSearch: emptyWebSearch(setting === 'on', cfg.providerId),
      };
    }
    if (feature === HOSTED_CODE_EXECUTION_FEATURE) {
      next = {
        ...next,
        codeExecution: cfg.codeExecution
          ? { ...cfg.codeExecution, enabled: setting === 'on' }
          : (setting === 'on'
            ? { enabled: true, toolType: null, memoryLimit: null, containerMode: null, containerId: null, skills: null }
            : null),
      };
    }
    await onUpdateConfig(next);
    invalidateConnectionCapabilityReport(cfg.id);
    refetch();
  };

  const revokePolicy = async (policy: AttachmentPolicyView) => {
    if (!onUpdateConfig) return;
    const next: ProviderConfig = {
      ...cfg,
      attachmentPolicies: (cfg.attachmentPolicies || []).filter((p) => String(p.id || `${p.class}:${p.action}`) !== policy.id),
      updatedAt: Date.now(),
    };
    try {
      await onUpdateConfig(next);
      setPolicyAnnouncement(`Removed the ${humanizeToken(policy.klass)} policy. You'll be asked again before sending.`);
    } finally {
      setRevokingPolicyId(null);
    }
  };

  const handleConclusiveCheck = () => {
    invalidateConnectionCapabilityReport(cfg.id);
    refetch();
  };

  return (
    <div data-connection-detail={cfg.id} className="space-y-4">
      <div>
        <button
          type="button"
          className="inline-flex min-h-8 items-center gap-1 rounded-md px-1 -mx-1 text-xs text-muted-foreground hover:text-foreground focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
          onClick={onBack}
        >
          <ArrowLeft size={13} aria-hidden /> All provider connections
        </button>
      </div>

      <div className="flex items-start justify-between gap-2 flex-wrap">
        <div className="flex items-start gap-3 min-w-0">
          <div
            className="w-9 h-9 rounded-md border flex items-center justify-center text-sm font-bold shrink-0"
            aria-hidden
            style={{ color: def.color, backgroundColor: `${def.color}14`, borderColor: `${def.color}30` }}
          >
            {def.icon}
          </div>
          <div className="min-w-0">
            <h3 className="text-sm font-semibold flex items-center gap-2 flex-wrap">
              {name}
              {isActive && (
                <span className="inline-flex items-center gap-1 text-[10px] font-semibold text-primary bg-primary/10 px-1.5 py-0.5 rounded">
                  <Star size={10} aria-hidden /> {flags.composerTarget ? 'Default for new chats' : 'Active'}
                </span>
              )}
            </h3>
            <p className="text-[11px] text-muted-foreground">
              {surface.familyLabel} · {surface.surfaceLabel}
              {cfg.model?.trim() ? <span className="font-mono"> · {cfg.model.trim()}</span> : null}
              {cfg.region?.trim() ? <span> · {cfg.region.trim()}</span> : null}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-1.5 shrink-0">
          {!isActive && (
            <Button variant="outline" size="sm" className="h-7 px-2 text-[11px]" onClick={onUse}>
              {flags.composerTarget ? 'Make default' : 'Use'}
            </Button>
          )}
          <Button variant="outline" size="sm" className="h-7 gap-1 px-2 text-[11px]" onClick={onEdit}>
            <Pencil size={11} aria-hidden /> Edit
          </Button>
        </div>
      </div>

      <section className="rounded-lg border border-border bg-card/30 p-3 space-y-2" aria-label="Capability summary">
        <h4 className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
          <ShieldQuestion size={12} aria-hidden /> Capabilities
        </h4>
        <CapabilityMatrix
          status={status === 'idle' ? 'loading' : status}
          report={report}
          errorMessage={errorMessage}
          idPrefix={`detail-${cfg.id}`}
          controls={interactive
            ? {
              onChange: applyControlChange,
              onOpenPermissions,
              readOnlyFeatures: [HOSTED_WEB_SEARCH_FEATURE, HOSTED_CODE_EXECUTION_FEATURE],
            }
            : undefined}
        />
      </section>

      <section className="rounded-lg border border-border bg-card/30 p-3 space-y-2" aria-label="Checks and evidence">
        <h4 className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
          <FileCheck2 size={12} aria-hidden /> Checks
        </h4>
        {cfg.lastTestedAt ? (
          <div className="text-[11px] text-muted-foreground space-y-0.5">
            <div>
              Quick check:{' '}
              <span className={cfg.lastTestStatus === 'success' ? 'text-emerald-400' : 'text-destructive'}>
                {cfg.lastTestStatus === 'success' ? 'passed' : 'failed'}
              </span>
              {' · '}{new Date(cfg.lastTestedAt).toLocaleString()}
            </div>
            {cfg.lastTestMessage && <div className="break-words">{cfg.lastTestMessage}</div>}
            {cfg.lastTestEndpoint && <div className="font-mono break-all text-[10px]">{cfg.lastTestEndpoint}</div>}
          </div>
        ) : (
          <p className="text-[11px] text-muted-foreground">
            No quick check has been run for this connection yet.
          </p>
        )}
        {interactive ? (
          <CapabilityCheckPanel
            connectionId={cfg.id}
            model={cfg.model}
            onConclusiveResult={handleConclusiveCheck}
          />
        ) : (
          <p className="text-[11px] text-muted-foreground/80">
            Per-feature capability checks appear here once management is enabled.
          </p>
        )}
      </section>

      <section className="rounded-lg border border-border bg-card/30 p-3 space-y-2" aria-label="Attachment handling">
        <h4 className="text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
          Attachment handling
        </h4>
        <span className="sr-only" role="status" aria-live="polite">{policyAnnouncement}</span>
        {policies.length === 0 ? (
          <p data-attachment-policies-empty className="text-[11px] text-muted-foreground">
            No attachment policies saved. You’ll always be asked before a file is sent with
            reduced fidelity or the API surface changes.
          </p>
        ) : (
          <ul className="space-y-1.5">
            {policies.map((p) => {
              const inert = Boolean(p.apiSurface) && p.apiSurface !== currentApiSurface;
              return (
                <li
                  key={p.id}
                  data-attachment-policy={p.id}
                  className="flex items-start justify-between gap-2 text-[11px] text-muted-foreground"
                >
                  <span className="min-w-0">
                    <span className={inert ? 'text-muted-foreground/70' : 'text-foreground/80'}>
                      {humanizeToken(p.klass)}
                    </span>
                    {' → '}{humanizeToken(p.action)}
                    {inert && (
                      <span className="ml-1 text-amber-500">not applicable to the current surface</span>
                    )}
                    {p.createdAt && (
                      <span className="ml-1 text-[10px] text-muted-foreground/70">
                        · {new Date(p.createdAt).toLocaleDateString()}
                      </span>
                    )}
                  </span>
                  {interactive && (
                    revokingPolicyId === p.id ? (
                      <span className="flex items-center gap-1 shrink-0">
                        <Button size="sm" variant="destructive" className="h-6 px-2 text-[10px]" onClick={() => void revokePolicy(p)}>
                          Remove policy
                        </Button>
                        <Button size="sm" variant="outline" className="h-6 px-2 text-[10px]" onClick={() => setRevokingPolicyId(null)}>
                          Keep
                        </Button>
                      </span>
                    ) : (
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-6 w-6 shrink-0 text-muted-foreground"
                        aria-label={`Remove ${humanizeToken(p.klass)} policy`}
                        onClick={() => setRevokingPolicyId(p.id)}
                      >
                        <Trash2 size={12} aria-hidden />
                      </Button>
                    )
                  )}
                </li>
              );
            })}
          </ul>
        )}
      </section>
    </div>
  );
};

export default ConnectionDetail;
