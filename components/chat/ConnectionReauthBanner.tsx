import React from 'react';
import { AlertTriangle } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import type { ConnectionCatalogId, ToolResultEnvelope } from '@/types';
import { CONNECTION_META, getConnectionForToolName } from '@/lib/connectionsCatalog';
import { extractToolErrorDiagnosticFromEnvelope } from '@/lib/toolErrorDiagnostics';
import ConnectionLogo from './ConnectionLogo';
import { useChatActions } from './ChatActionsContext';

/**
 * Inline remediation for auth-class tool failures: when a tool result reports
 * that a connection's credentials were rejected, this banner names the
 * connection and offers a one-click path into its re-auth flow - no hunting
 * through settings. It also reports the failure to the app so the connection's
 * health is strictly re-verified and the Connections console reflects it.
 */

/** Map a tool's integration marker to a catalogue connection. */
export function resolveConnectionId(
  toolName?: string | null,
  integration?: string | null,
): ConnectionCatalogId | null {
  const byTool = getConnectionForToolName(String(toolName || ''));
  if (byTool) return byTool;
  const s = String(integration || '').trim().toLowerCase();
  if (!s) return null;
  if (s === 'jira' || s === 'confluence' || s === 'atlassian') return 'atlassian';
  if (s === 'browser_control' || s === 'browser') return 'browser';
  if (s in CONNECTION_META) return s as ConnectionCatalogId;
  return null;
}

interface ConnectionReauthBannerProps {
  connectionId: ConnectionCatalogId | null;
  /** tool_call_id - dedupes health reporting across transcript re-renders. */
  toolCallId?: string | null;
  message?: string | null;
  className?: string;
}

export const ConnectionReauthBanner: React.FC<ConnectionReauthBannerProps> = ({
  connectionId,
  toolCallId,
  message,
  className,
}) => {
  const { openConnections, reportConnectionAuthFailure } = useChatActions();
  const meta = connectionId ? CONNECTION_META[connectionId] : null;
  const label = meta?.label || 'This connection';

  React.useEffect(() => {
    if (!connectionId || !toolCallId || !reportConnectionAuthFailure) return;
    reportConnectionAuthFailure(connectionId, toolCallId);
  }, [connectionId, toolCallId, reportConnectionAuthFailure]);

  if (!openConnections) return null;

  return (
    <div
      role="status"
      className={cn(
        'mt-2 flex items-center gap-2.5 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs',
        className,
      )}
    >
      <span className="flex h-7 w-7 shrink-0 items-center justify-center overflow-hidden rounded-md border border-amber-500/20 bg-background/60">
        {meta ? (
          <ConnectionLogo
            meta={meta}
            className={cn('h-4 w-4 object-contain', meta.mono && '[filter:brightness(0)_invert(1)]')}
          />
        ) : (
          <AlertTriangle size={14} className="text-amber-500" aria-hidden />
        )}
      </span>
      <div className="min-w-0 flex-1">
        <div className="font-medium text-amber-500">{label} needs re-authentication</div>
        <div className="truncate text-muted-foreground">
          {message || 'The saved credentials were rejected. Reconnect to keep using these tools.'}
        </div>
      </div>
      <Button
        size="sm"
        variant="outline"
        className="h-7 shrink-0 border-amber-500/40 text-amber-500 hover:bg-amber-500/15 hover:text-amber-400"
        onClick={() => openConnections(connectionId ?? undefined)}
      >
        Reconnect
      </Button>
    </div>
  );
};

/**
 * Envelope-driven variant: renders the banner only when the tool result's
 * diagnostic classifies as an auth failure. Safe to drop into any tool-result
 * renderer's error path.
 */
export const ToolEnvelopeReauth: React.FC<{ envelope: ToolResultEnvelope }> = ({ envelope }) => {
  if (envelope.ok !== false) return null;
  const diagnostic = extractToolErrorDiagnosticFromEnvelope(envelope, {
    fallbackTitle: envelope.tool_name || 'Tool error',
  });
  if (diagnostic?.kind !== 'auth') return null;
  return (
    <ConnectionReauthBanner
      connectionId={resolveConnectionId(envelope.tool_name, envelope.result_metadata?.integration)}
      toolCallId={envelope.tool_call_id}
      message={diagnostic.message || diagnostic.title}
    />
  );
};

export default ConnectionReauthBanner;
