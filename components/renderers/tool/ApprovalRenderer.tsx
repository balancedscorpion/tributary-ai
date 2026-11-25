import React, { useMemo } from 'react';
import { ShieldQuestion } from 'lucide-react';
import { registerCustomResolver } from '../registry';
import type { RendererProps } from '../types';
import ApprovalCard from '@/components/approvals/ApprovalCard';
import { useApprovals } from '@/hooks/useApprovals';
import { normalizeApprovalRequest } from '@/types/permissions';

function isObject(v: unknown): v is Record<string, any> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

/**
 * Transcript renderer for the unified approvals system.
 *
 * Claims approval envelopes from both the persistent approval service and the
 * legacy connector queues. The normalizer tolerates both shapes, so the visual
 * treatment stays consistent while backend integrations migrate.
 *
 * The envelope is a snapshot; the shared approvals store (fed by `approval`
 * SSE events and API decisions) always wins, so a card decided from the strip
 * or Settings updates here immediately — and vice versa.
 */
function rawResultFromArtifact(
  artifact: RendererProps['artifact'],
  toolResult?: unknown,
): Record<string, any> {
  if (isObject(toolResult)) return toolResult;
  const meta = (artifact.metadata || {}) as Record<string, any>;
  if (isObject(meta.raw_result)) return meta.raw_result;
  try {
    const parsed = JSON.parse(artifact.content || '{}');
    return isObject(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

const ApprovalEnvelopeRenderer: React.FC<RendererProps> = ({ artifact, toolResult }) => {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  const rawResult = useMemo(
    () => rawResultFromArtifact(artifact, toolResult),
    [artifact, toolResult],
  );
  const resultMetadata = isObject(meta.result_metadata) ? meta.result_metadata : {};

  const snapshot = useMemo(
    () =>
      normalizeApprovalRequest(approvalPayloadFromResult(rawResult), {
        legacyIntegration:
          typeof resultMetadata.integration === 'string'
            ? resultMetadata.integration
            : undefined,
      }),
    [rawResult.approval, resultMetadata.integration],
  );

  const { approvalsById, decide, busyIds, refresh } = useApprovals(
    snapshot?.threadId || null,
  );
  if (!snapshot) return null;
  const live = approvalsById[snapshot.id];
  const approval = live
    ? { ...snapshot, ...live, preview: Object.keys(live.preview || {}).length ? live.preview : snapshot.preview }
    : snapshot;

  return (
    <ApprovalCard
      approval={approval}
      variant="transcript"
      onDecide={(id, decision) => decide(id, decision)}
      busy={busyIds.has(approval.id)}
      onRefresh={refresh}
    />
  );
};

function approvalPayloadFromResult(raw: unknown): unknown {
  if (!isObject(raw)) return raw;
  if (!isObject(raw.approval)) return raw;
  return {
    ...raw.approval,
    status: raw.approval.status ?? raw.approval.state ?? raw.status ?? raw.approval_status,
    state: raw.approval.state ?? raw.approval.status ?? raw.status ?? raw.approval_status,
  };
}

function isApprovalEnvelope(raw: unknown): boolean {
  if (!isObject(raw)) return false;
  const approval = raw.approval;
  if (!isObject(approval)) return false;
  const hasId = typeof approval.id === 'string' && approval.id.trim().length > 0;
  return hasId;
}

registerCustomResolver((artifact) => {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  const resultMetadata = isObject(meta.result_metadata) ? meta.result_metadata : {};
  const kind = String(resultMetadata.kind || '').trim().toLowerCase();
  const widget = String(resultMetadata.widget || '').trim().toLowerCase();
  if (kind !== 'approval' && widget !== 'approval') return null;
  if (!isApprovalEnvelope(rawResultFromArtifact(artifact))) return null;
  return {
    component: ApprovalEnvelopeRenderer,
    label: 'Approval',
    icon: ShieldQuestion,
    ownsChrome: true,
  };
});

export default ApprovalEnvelopeRenderer;
