import React from 'react';
import { ChevronDown, Plug, ExternalLink, Cog } from 'lucide-react';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import type { AppSettings, ConnectionCatalogId } from '@/types';
import {
  deriveVisibleConnections,
  type ConnectionAuditInfo,
} from '@/lib/connectionsCatalog';
import { canEnableConnection } from '@/lib/connectionEnablement';
import { useConnectionStatuses } from '@/hooks/useConnectionStatuses';
import ConnectionLogo from './ConnectionLogo';
import { useChatActions } from './ChatActionsContext';
import './toolAudit.css';

/**
 * Header pill + popover over the ONE enabled-for-run connection state (the
 * per-connection enabled flags in AppSettings). This surface and the
 * Connections settings tab are two editors of the same state: a switch here
 * persists the same settings slice the tab's toggles persist, so both update
 * together and the next run consumes exactly what either surface shows.
 */

interface ToolAuditPillProps {
  settings: AppSettings;
  /** Persist the shared enabled-for-run state (serialized settings mutation). */
  onSetConnectionEnabled: (id: ConnectionCatalogId, enabled: boolean) => void | Promise<unknown>;
  onOpenSettings: () => void;
}

interface EnrichedConnection extends ConnectionAuditInfo {
  resolvedIdentity: string;
}

function ConnectionRow({
  conn,
  affordance,
  onToggle,
  onSetup,
}: {
  conn: EnrichedConnection;
  affordance: 'switch-on' | 'switch-off' | 'setup';
  onToggle?: () => void;
  onSetup?: () => void;
}) {
  const { meta } = conn;
  const dim = affordance !== 'switch-on';
  return (
    <div className={`ta-row${dim ? ' ta-off' : ''}`}>
      <div className={`ta-cic${meta.mono ? ' ta-mono' : ''}`}>
        <ConnectionLogo meta={meta} />
      </div>
      <div className="ta-cbody">
        <div className="ta-cname">
          {affordance === 'switch-on' && <span className="ta-gd" aria-hidden="true" />}
          {affordance === 'setup' && <span className="ta-ad" aria-hidden="true" />}
          {meta.label}
        </div>
        {conn.resolvedIdentity && <div className="ta-cmeta">{conn.resolvedIdentity}</div>}
        {affordance === 'switch-on' && conn.capabilities.length > 0 && (
          <div className="ta-ctools">{conn.capabilities.join(' · ')}</div>
        )}
      </div>
      {affordance !== 'setup' && (
        <label
          className="ta-switch"
          title={affordance === 'switch-on' ? `Disable ${meta.label}` : `Enable ${meta.label}`}
        >
          <input
            type="checkbox"
            checked={affordance === 'switch-on'}
            onChange={onToggle}
            aria-label={affordance === 'switch-on' ? `Disable ${meta.label}` : `Enable ${meta.label}`}
          />
          <span className="ta-track" />
        </label>
      )}
      {affordance === 'setup' && (
        <button type="button" className="ta-mini" onClick={onSetup}>
          Set up
        </button>
      )}
    </div>
  );
}

const ToolAuditPill: React.FC<ToolAuditPillProps> = ({
  settings,
  onSetConnectionEnabled,
  onOpenSettings,
}) => {
  const [open, setOpen] = React.useState(false);
  const live = useConnectionStatuses(open);

  const connections = React.useMemo<EnrichedConnection[]>(() => {
    return deriveVisibleConnections(settings).map((conn) => ({
      ...conn,
      resolvedIdentity: live[conn.id]?.identity || conn.identity,
    }));
  }, [settings, live]);

  const { enabled, disabled, needsSetup } = React.useMemo(() => {
    const enabled: EnrichedConnection[] = [];
    const disabled: EnrichedConnection[] = [];
    const needsSetup: EnrichedConnection[] = [];
    for (const conn of connections) {
      if (conn.readiness === 'ready') enabled.push(conn);
      // Configured but switched off: flipping enablement alone restores it,
      // so it gets a switch rather than the full setup affordance.
      else if (live[conn.id]?.connected === true || canEnableConnection(settings, conn.id)) disabled.push(conn);
      else needsSetup.push(conn);
    }
    return { enabled, disabled, needsSetup };
  }, [connections, live, settings]);

  const activeCount = enabled.length;
  const hasActive = activeCount > 0;
  const stackLogos = enabled.slice(0, 3);

  const pillLabel = hasActive
    ? `${activeCount} tool${activeCount === 1 ? '' : 's'} active`
    : 'No tools active';

  // Prefer the Connections deep-link so "Manage connections" / "Set up" land on
  // the Connections tab (and the specific connection) instead of the default
  // Providers tab; fall back to the plain settings opener when unavailable.
  const { openConnections } = useChatActions();
  const handleSetup = React.useCallback((connectionId?: ConnectionCatalogId) => {
    setOpen(false);
    if (openConnections) openConnections(connectionId);
    else onOpenSettings();
  }, [onOpenSettings, openConnections]);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className={`ta-scope ta-pill${hasActive ? '' : ' ta-pill-zero'}`}
          aria-label={`Connections enabled for runs: ${activeCount}`}
        >
          {hasActive && (
            <span className="ta-stack" aria-hidden="true">
              {stackLogos.map((conn) => (
                <span key={conn.id} className={`ta-lg${conn.meta.mono ? ' ta-mono' : ''}`}>
                  <ConnectionLogo meta={conn.meta} />
                </span>
              ))}
            </span>
          )}
          {!hasActive && <Plug size={14} className="shrink-0" aria-hidden="true" />}
          <span className="ta-pill-txt">{pillLabel}</span>
          {hasActive && <span className="ta-hd" aria-hidden="true" />}
          <span className="ta-cv" aria-hidden="true">
            <ChevronDown size={14} />
          </span>
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        sideOffset={8}
        className="ta-scope w-auto border-0 bg-transparent p-0 shadow-none"
      >
        <div className="ta-pop" role="dialog" aria-label="Connections enabled for runs">
          <div className="ta-pop-head">
            <div className="ta-eyebrow">
              {hasActive && <span className="ta-gd" aria-hidden="true" />}
              Enabled for runs
            </div>
            <div className="ta-pop-title">
              {`${activeCount} tool${activeCount === 1 ? '' : 's'} enabled`}
            </div>
            <div className="ta-scopeline">
              <span>Shared across chats · changes apply to new runs</span>
            </div>
          </div>

          {connections.length === 0 ? (
            <div className="ta-pop-body">
              <div className="ta-pop-empty">
                <div className="ta-pop-empty-title">No connections added</div>
                <div className="ta-pop-empty-sub">
                  Add data sources and integrations to make tools available to chats.
                </div>
              </div>
            </div>
          ) : (
            <div className="ta-pop-body">
              {enabled.length > 0 && (
                <>
                  <div className="ta-grp">Enabled</div>
                  {enabled.map((conn) => (
                    <ConnectionRow
                      key={conn.id}
                      conn={conn}
                      affordance="switch-on"
                      onToggle={() => void onSetConnectionEnabled(conn.id, false)}
                    />
                  ))}
                </>
              )}
              {disabled.length > 0 && (
                <>
                  <div className="ta-grp">Off</div>
                  {disabled.map((conn) => (
                    <ConnectionRow
                      key={conn.id}
                      conn={conn}
                      affordance="switch-off"
                      onToggle={() => void onSetConnectionEnabled(conn.id, true)}
                    />
                  ))}
                </>
              )}
              {needsSetup.length > 0 && (
                <>
                  <div className="ta-grp">Needs setup</div>
                  {needsSetup.map((conn) => (
                    <ConnectionRow
                      key={conn.id}
                      conn={conn}
                      affordance="setup"
                      onSetup={() => handleSetup(conn.id)}
                    />
                  ))}
                </>
              )}
            </div>
          )}

          <div className="ta-pop-foot">
            <button type="button" className="ta-foot-btn" onClick={() => handleSetup()}>
              <Cog aria-hidden="true" />
              Manage connections
              <ExternalLink aria-hidden="true" />
            </button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  );
};

export default ToolAuditPill;
