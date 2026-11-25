import { useCallback, useEffect, useRef } from 'react';
import { toast } from 'sonner';
import type { AppSettings, ConnectionCatalogId } from '@/types';
import { CONNECTION_META } from '@/lib/connectionsCatalog';
import {
  CONNECTION_HEALTH_LAUNCH_DELAY_MS,
  CONNECTION_HEALTH_REEVAL_INTERVAL_MS,
  getDueConnections,
  isConnectionEnabledForModel,
} from '@/lib/connectionHealth';
import { configFingerprint } from '@/lib/connectionConfig';
import { verifyConnectionCredentials, type ConnectionCheckOutcome } from '@/services/connectionHealthApi';

export interface UseConnectionHealthArgs {
  settings: AppSettings;
  /** Sweeps must not run against DEFAULT_SETTINGS before /api/settings loads. */
  settingsLoaded: boolean;
  /** Persist app settings without the Settings dialog success toast. */
  persistSettings: (settings: AppSettings) => Promise<void>;
  /** Re-pull /api/settings so backend-persisted check results reach the UI. */
  reloadSettings: () => Promise<void>;
  /** Open the Connections console, optionally focused on one connection. */
  onReconnect: (id?: ConnectionCatalogId) => void;
}

function productIsConnected(product: any): boolean {
  return String(product?.state || '').toLowerCase() === 'connected' || product?.connected === true;
}

function applyConnectionCheckOutcomes(
  settings: AppSettings,
  outcomes: ConnectionCheckOutcome[],
): AppSettings {
  let next = settings;
  const now = Date.now();
  for (const outcome of outcomes) {
    if (outcome.indeterminate) continue;
    const lastVerifiedFingerprint = outcome.ok ? configFingerprint(next, outcome.id) : null;
    const health = {
      lastCheckedAt: now,
      lastCheckOk: outcome.ok,
      lastCheckMessage: outcome.ok ? null : (outcome.message || null),
      lastVerifiedFingerprint,
    };
    switch (outcome.id) {
      case 'github':
        next = { ...next, github: { ...next.github, ...health } };
        break;
      case 'slack':
        next = { ...next, slack: { ...next.slack, ...health } };
        break;
      case 'sharepoint':
        next = { ...next, sharepoint: { ...next.sharepoint, ...health } };
        break;
      case 'atlassian': {
        const products = outcome.detail?.products || {};
        const atlassian = { ...(next.atlassian || {}), lastVerifiedFingerprint };
        if (products.jira || products.confluence) {
          if (products.jira) {
            atlassian.jira = {
              ...(atlassian.jira || {}),
              lastCheckedAt: now,
              lastCheckOk: productIsConnected(products.jira),
              lastCheckMessage: health.lastCheckMessage,
            };
          }
          if (products.confluence) {
            atlassian.confluence = {
              ...(atlassian.confluence || {}),
              lastCheckedAt: now,
              lastCheckOk: productIsConnected(products.confluence),
              lastCheckMessage: health.lastCheckMessage,
            };
          }
        } else {
          atlassian.jira = { ...(atlassian.jira || {}), lastCheckedAt: now, lastCheckOk: outcome.ok, lastCheckMessage: health.lastCheckMessage };
          atlassian.confluence = { ...(atlassian.confluence || {}), lastCheckedAt: now, lastCheckOk: outcome.ok, lastCheckMessage: health.lastCheckMessage };
        }
        next = { ...next, atlassian };
        break;
      }
      case 'bigquery':
      case 'databricks':
      case 'trino':
      case 'snowflake':
        next = {
          ...next,
          dataSources: {
            ...next.dataSources,
            [outcome.id]: { ...next.dataSources?.[outcome.id], ...health },
          },
        };
        break;
      default:
        break;
    }
  }
  return next;
}

export interface ConnectionHealthController {
  /** Strictly verify the given connections now (defaults to every due one). */
  verifyConnections: (ids?: ConnectionCatalogId[]) => Promise<void>;
  /**
   * A tool call failed with an auth-class error. Re-verifies that connection
   * strictly (a transient 401 must not flag a healthy connection) and, when
   * confirmed, persists the failure + surfaces a reconnect toast. Deduped by
   * `dedupeKey` (tool_call_id) so re-rendered transcripts never re-fire.
   */
  reportToolAuthFailure: (id: ConnectionCatalogId, dedupeKey: string) => void;
}

/**
 * Launch + 24h credential freshness sweeps for the connection catalogue.
 *
 * Connections mostly authenticate daily, so the only automatic network checks
 * happen (a) shortly after app launch and (b) when a connection crosses the
 * 24-hour window while the app stays open. The hourly re-evaluation is pure
 * local math over settings - no request fires unless a connection is due.
 * Strict verification and persistence live server-side in the existing
   * check/test endpoints; this hook folds verdicts into settings, persists the
   * durable health/fingerprint fields, and surfaces results.
 */
export function useConnectionHealth({
  settings,
  settingsLoaded,
  persistSettings,
  reloadSettings,
  onReconnect,
}: UseConnectionHealthArgs): ConnectionHealthController {
  const settingsRef = useRef(settings);
  settingsRef.current = settings;
  const reloadRef = useRef(reloadSettings);
  reloadRef.current = reloadSettings;
  const persistSettingsRef = useRef(persistSettings);
  persistSettingsRef.current = persistSettings;
  const onReconnectRef = useRef(onReconnect);
  onReconnectRef.current = onReconnect;

  const sweepInFlightRef = useRef(false);
  const explicitCheckInFlightRef = useRef<Set<ConnectionCatalogId>>(new Set());
  // Connections already alerted this session; cleared when a check passes so a
  // later regression alerts again.
  const alertedRef = useRef<Set<ConnectionCatalogId>>(new Set());
  const reportedKeysRef = useRef<Set<string>>(new Set());

  const surfaceOutcome = useCallback((outcome: ConnectionCheckOutcome) => {
    if (outcome.indeterminate) return;
    if (outcome.ok) {
      alertedRef.current.delete(outcome.id);
      return;
    }
    // Row health state is already persisted; only the toast is suppressed for
    // connections whose model access is off. When a tool actually fails,
    // the in-chat reconnect banner handles it regardless of this filter.
    if (!isConnectionEnabledForModel(settingsRef.current, outcome.id)) return;
    if (alertedRef.current.has(outcome.id)) return;
    alertedRef.current.add(outcome.id);
    const label = CONNECTION_META[outcome.id]?.label || outcome.id;
    toast.warning(`${label} needs re-authentication`, {
      description: outcome.message || 'The saved credentials were rejected.',
      duration: 12_000,
      action: {
        label: 'Reconnect',
        onClick: () => onReconnectRef.current(outcome.id),
      },
    });
  }, []);

  const verifyConnections = useCallback(async (ids?: ConnectionCatalogId[]) => {
    const isExplicitCheck = Array.isArray(ids);
    if (!isExplicitCheck) {
      if (sweepInFlightRef.current) return;
      sweepInFlightRef.current = true;
    }
    let targets: ConnectionCatalogId[] = [];
    try {
      const current = settingsRef.current;
      targets = isExplicitCheck
        ? Array.from(new Set(ids ?? [])).filter((id) => !explicitCheckInFlightRef.current.has(id))
        : getDueConnections(current, Date.now()).map((state) => state.id);
      if (targets.length === 0) return;
      if (isExplicitCheck) {
        for (const id of targets) explicitCheckInFlightRef.current.add(id);
      }
      const outcomes: ConnectionCheckOutcome[] = [];
      // Sequential on purpose: checks hit external providers (and the
      // SharePoint/Atlassian checks spin up a local bridge), so a burst of
      // parallel probes right after launch is exactly what we want to avoid.
      for (const id of targets) {
        outcomes.push(await verifyConnectionCredentials(id, settingsRef.current));
      }
      if (outcomes.some((o) => !o.indeterminate)) {
        const nextSettings = applyConnectionCheckOutcomes(settingsRef.current, outcomes);
        settingsRef.current = nextSettings;
        try {
          await persistSettingsRef.current(nextSettings);
        } catch {
          // If the local settings save fails, keep the old reload fallback so
          // any backend-persisted check results still get a chance to appear.
          await reloadRef.current();
        }
      }
      for (const outcome of outcomes) surfaceOutcome(outcome);
    } finally {
      if (isExplicitCheck) {
        for (const id of targets) explicitCheckInFlightRef.current.delete(id);
      } else {
        sweepInFlightRef.current = false;
      }
    }
  }, [surfaceOutcome]);

  const verifyRef = useRef(verifyConnections);
  verifyRef.current = verifyConnections;

  useEffect(() => {
    if (!settingsLoaded) return;
    const launchTimer = window.setTimeout(() => {
      void verifyRef.current();
    }, CONNECTION_HEALTH_LAUNCH_DELAY_MS);
    const interval = window.setInterval(() => {
      void verifyRef.current();
    }, CONNECTION_HEALTH_REEVAL_INTERVAL_MS);
    return () => {
      window.clearTimeout(launchTimer);
      window.clearInterval(interval);
    };
  }, [settingsLoaded]);

  const reportToolAuthFailure = useCallback((id: ConnectionCatalogId, dedupeKey: string) => {
    const key = `${id}:${dedupeKey}`;
    if (reportedKeysRef.current.has(key)) return;
    reportedKeysRef.current.add(key);
    void verifyRef.current([id]);
  }, []);

  return { verifyConnections, reportToolAuthFailure };
}
