import { useEffect, useRef, useState } from 'react';
import type { ConnectionCatalogId } from '@/types';

/**
 * Best-effort live identity enrichment for the tool-audit popover. Settings
 * already carry identity for the SQL/Atlassian connections (host, project,
 * account), but GitHub login and the Slack workspace name only exist on the
 * backend status endpoints. These are fetched lazily — only when the popover is
 * opened — and never block rendering: on any failure the popover falls back to
 * the settings-derived identity.
 */

export interface LiveConnectionStatus {
  identity?: string;
  connected?: boolean;
}

export type LiveStatusMap = Partial<Record<ConnectionCatalogId, LiveConnectionStatus>>;

async function fetchJson(url: string, signal: AbortSignal): Promise<any | null> {
  try {
    const res = await fetch(url, { headers: { Accept: 'application/json' }, signal });
    if (!res.ok) return null;
    return await res.json();
  } catch {
    return null;
  }
}

function str(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

export function useConnectionStatuses(enabled: boolean): LiveStatusMap {
  const [statuses, setStatuses] = useState<LiveStatusMap>({});
  const fetchedRef = useRef(false);

  useEffect(() => {
    if (!enabled || fetchedRef.current) return;
    fetchedRef.current = true;
    const controller = new AbortController();

    const run = async () => {
      const [github, slack, bigquery, sharepoint] = await Promise.all([
        fetchJson('/api/integrations/github/status', controller.signal),
        fetchJson('/api/integrations/slack/status', controller.signal),
        fetchJson('/api/bigquery/status', controller.signal),
        fetchJson('/api/integrations/sharepoint/status', controller.signal),
      ]);
      if (controller.signal.aborted) return;

      const next: LiveStatusMap = {};

      if (github) {
        const login = str(github.login) || str(github.profile?.github_login);
        next.github = {
          connected: github.connected === true,
          identity: login ? `@${login}` : undefined,
        };
      }
      if (slack) {
        const team = str(slack.team_name) || str(slack.team) || str(slack.workspace);
        next.slack = {
          connected: slack.connected === true,
          identity: team || undefined,
        };
      }
      if (bigquery) {
        const project = str(bigquery.detectedProject) || str(bigquery.detected_project);
        const credType = str(bigquery.credentialsType) || str(bigquery.credentials_type) || 'authorized_user';
        next.bigquery = {
          connected: bigquery.detected === true || !!project,
          identity: project ? `${project} · ${credType}` : undefined,
        };
      }
      if (sharepoint) {
        const site = sharepoint.site || {};
        const host = str(site.url) || str(sharepoint.url);
        let identity: string | undefined;
        if (host) {
          try {
            identity = new URL(host).host;
          } catch {
            identity = host.replace(/^https?:\/\//, '').replace(/\/.*$/, '');
          }
        }
        next.sharepoint = {
          connected: site.connected === true || String(site.state || '').toLowerCase() === 'connected',
          identity,
        };
      }

      setStatuses(next);
    };

    void run();
    return () => controller.abort();
  }, [enabled]);

  return statuses;
}
