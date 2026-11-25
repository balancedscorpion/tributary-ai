import type { AppSettings, ConnectionCatalogId, ProviderConfig } from '@/types';
import { disableHostedToolConnectionAcrossProviders } from '@/lib/connectionConfig';
import { supportsProviderHostedTools } from '@/lib/capability/providerConfig';
import { applyProviderConfigToLegacySettings } from '@/lib/providerConfigRuntime';
import { deriveVisibleConnections } from '@/lib/connectionsCatalog';
import { canonicalProviderWebSearch } from '@/lib/providerWebSearch';

/**
 * Canonical enabled-for-run connection state.
 *
 * There is exactly ONE such state: the per-connection enabled flags persisted
 * in AppSettings. The Connections settings tab and the chat header drawer are
 * both editors of it, and runs consume it server-side from the same persisted
 * settings. `withConnectionEnabled` is the shared mutation both surfaces use
 * to flip a connection on or off for runs without touching its stored
 * configuration or credentials.
 *
 * The mutation is deliberately conservative: when a connection cannot become
 * usable by flipping enablement alone (no hosted-tool provider configured, no
 * Atlassian base URL, a dangling image provider), it returns the input
 * settings object unchanged so callers can route the user to full setup
 * instead of persisting a flag that would not produce working tools.
 */

type HostedKind = 'web-search' | 'python-sandbox';

const hostedControlKey = (kind: HostedKind): 'hosted_web_search' | 'hosted_code_execution' => (
  kind === 'web-search' ? 'hosted_web_search' : 'hosted_code_execution'
);

const hostedConfigKey = (kind: HostedKind): 'webSearch' | 'codeExecution' => (
  kind === 'web-search' ? 'webSearch' : 'codeExecution'
);

const hostedToolOn = (config: ProviderConfig, kind: HostedKind): boolean => {
  const control = config.capabilityControls?.[hostedControlKey(kind)];
  if (control != null) return control === 'on';
  return config[hostedConfigKey(kind)]?.enabled === true;
};

function withHostedToolEnabled(settings: AppSettings, kind: HostedKind, on: boolean): AppSettings {
  if (!on) {
    return applyProviderConfigToLegacySettings(
      disableHostedToolConnectionAcrossProviders(settings, kind),
    );
  }

  // Web search is one model capability toggle, not a second provider target.
  // Enable it on every supported model connection so whichever connection a
  // chat selects receives the provider-native search tool on that same model
  // request. Provider-specific filters remain stored on each connection.
  if (kind === 'web-search') {
    const configs = (settings.providerConfigs || []).filter(supportsProviderHostedTools);
    if (configs.length === 0) return settings;
    return applyProviderConfigToLegacySettings({
      ...settings,
      providerConfigs: (settings.providerConfigs || []).map((config) => (
        supportsProviderHostedTools(config)
          ? {
              ...config,
              capabilityControls: {
                ...(config.capabilityControls || {}),
                hosted_web_search: 'on',
              },
              webSearch: canonicalProviderWebSearch(config.providerId, true),
            }
          : config
      )),
    });
  }

  const key = hostedConfigKey(kind);
  const configs = (settings.providerConfigs || []).filter(supportsProviderHostedTools);
  const target = configs.find((config) => hostedToolOn(config, kind))
    || configs.find((config) => config.id === settings.activeProviderConfigId)
    || configs.find((config) => !!config[key])
    || null;
  if (!target) return settings;
  return applyProviderConfigToLegacySettings({
    ...settings,
    providerConfigs: (settings.providerConfigs || []).map((config) => (
      config.id === target.id
        ? {
            ...config,
            capabilityControls: {
              ...(config.capabilityControls || {}),
              [hostedControlKey(kind)]: 'on',
            },
            [key]: { ...(config[key] || {}), enabled: true },
          }
        : config
    )),
  });
}

/**
 * `settings` with connection `id` enabled/disabled for runs. Pure; never
 * mutates configuration, credentials, or the visible catalogue. Returns the
 * same object (identity-equal) when the request cannot be honored by flipping
 * enablement alone — see module docs.
 */
export function withConnectionEnabled(
  settings: AppSettings,
  id: ConnectionCatalogId,
  on: boolean,
): AppSettings {
  switch (id) {
    case 'thread-search':
      return { ...settings, threadSearch: { ...(settings.threadSearch || {}), enabled: on } };
    case 'image-generation': {
      if (on) {
        const target = (settings.providerConfigs || []).find(
          (config) => config.id === settings.imageGeneration?.providerId
            && (config.providerId === 'openai' || config.providerId === 'compatible'),
        );
        if (!target) return settings;
      }
      return { ...settings, imageGeneration: { ...(settings.imageGeneration || {}), enabled: on } };
    }
    case 'web-search':
    case 'python-sandbox':
      return withHostedToolEnabled(settings, id, on);
    case 'local-computer':
      return { ...settings, localComputer: { ...(settings.localComputer || {}), enabled: on } };
    case 'browser':
      return { ...settings, browserControl: { ...(settings.browserControl || {}), enabled: on } };
    case 'github':
      return {
        ...settings,
        github: {
          ...(settings.github || {}),
          enabled: on,
          // Older Settings-tab toggles cleared readTools when disabling. Make
          // re-enable repair that legacy state so the backend actually exposes
          // GitHub tools instead of only making the UI look enabled.
          ...(on ? { readTools: true } : {}),
        },
      };
    case 'slack': {
      const slack = settings.slack || {};
      const hasSelectedTools = slack.readTools === true || slack.writeTools === true;
      return {
        ...settings,
        slack: {
          ...slack,
          enabled: on,
          // Preserve the user's read/write selection while disabled. If this
          // is a legacy state where disabling cleared both, restore the safe
          // read capability so turning Slack on has real run-time effect.
          ...(on && !hasSelectedTools ? { readTools: true } : {}),
        },
      };
    }
    case 'sharepoint':
      return { ...settings, sharepoint: { ...(settings.sharepoint || {}), enabled: on } };
    case 'arxiv':
      return { ...settings, arxiv: { ...(settings.arxiv || {}), enabled: on } };
    case 'atlassian': {
      const atlassian = settings.atlassian || {};
      if (!on) {
        return {
          ...settings,
          atlassian: {
            ...atlassian,
            jira: { ...(atlassian.jira || {}), enabled: false },
            confluence: { ...(atlassian.confluence || {}), enabled: false },
          },
        };
      }
      const hasJira = Boolean(String(atlassian.jiraBaseUrl || '').trim());
      const hasConfluence = Boolean(String(atlassian.confluenceBaseUrl || '').trim());
      if (!hasJira && !hasConfluence) return settings;
      return {
        ...settings,
        atlassian: {
          ...atlassian,
          jira: { ...(atlassian.jira || {}), enabled: hasJira ? true : atlassian.jira?.enabled },
          confluence: {
            ...(atlassian.confluence || {}),
            enabled: hasConfluence ? true : atlassian.confluence?.enabled,
          },
        },
      };
    }
    case 'bigquery':
    case 'databricks':
    case 'trino':
    case 'snowflake':
      return {
        ...settings,
        dataSources: {
          ...settings.dataSources,
          [id]: { ...(settings.dataSources?.[id] || {}), enabled: on },
        },
      };
    default:
      return settings;
  }
}

/** Is the connection enabled for runs right now (ready in the catalogue)? */
export function isConnectionEnabledForRuns(
  settings: AppSettings,
  id: ConnectionCatalogId,
): boolean {
  return deriveVisibleConnections(settings).some(
    (conn) => conn.id === id && conn.readiness === 'ready',
  );
}

function hasReusableSetup(settings: AppSettings, id: ConnectionCatalogId): boolean {
  const usableText = (value: unknown): boolean => Boolean(String(value || '').trim());
  switch (id) {
    case 'thread-search':
    case 'browser':
    case 'arxiv':
      // These connections do not have a separate credential/setup state.
      return true;
    case 'local-computer':
      return settings.localComputer?.lastCheckOk === true;
    case 'github':
      return settings.github?.lastCheckOk === true;
    case 'slack':
      return settings.slack?.lastCheckOk === true;
    case 'atlassian': {
      const atlassian = settings.atlassian;
      const jiraReady = usableText(atlassian?.jiraBaseUrl) && atlassian?.jira?.lastCheckOk !== false;
      const confluenceReady = usableText(atlassian?.confluenceBaseUrl)
        && atlassian?.confluence?.lastCheckOk !== false;
      return jiraReady || confluenceReady;
    }
    case 'sharepoint':
      return usableText(settings.sharepoint?.siteUrl) && settings.sharepoint?.lastCheckOk !== false;
    case 'bigquery':
      return usableText(settings.googleProjectId || settings.dataSources?.bigquery?.lastProjectId)
        && settings.dataSources?.bigquery?.lastCheckOk !== false;
    case 'databricks':
      return usableText(settings.databricksHost || settings.dataSources?.databricks?.lastHost)
        && usableText(settings.databricksWarehouseId || settings.dataSources?.databricks?.lastWarehouseId)
        && settings.dataSources?.databricks?.lastCheckOk !== false;
    case 'trino':
      return usableText(settings.trinoHost || settings.dataSources?.trino?.lastHost)
        && usableText(settings.trinoCatalog)
        && usableText(settings.trinoSchema)
        && usableText(settings.trinoUser)
        && settings.dataSources?.trino?.lastCheckOk !== false;
    case 'snowflake':
      return usableText(settings.snowflakeAccountIdentifier || settings.dataSources?.snowflake?.lastHost)
        && usableText(settings.snowflakeWarehouse)
        && usableText(settings.snowflakeDatabase)
        && usableText(settings.snowflakeSchema)
        && usableText(settings.snowflakeUser)
        && settings.dataSources?.snowflake?.lastCheckOk !== false;
    case 'image-generation':
    case 'web-search':
    case 'python-sandbox':
      // These are validated by their provider-target logic in the mutation.
      return true;
    default:
      return false;
  }
}

/**
 * Can the connection be switched on for runs by flipping enablement alone
 * (configuration/credentials already in place)? False means it needs the full
 * setup flow in the Connections tab.
 */
export function canEnableConnection(settings: AppSettings, id: ConnectionCatalogId): boolean {
  if (!hasReusableSetup(settings, id)) return false;
  const next = withConnectionEnabled(settings, id, true);
  if (next === settings && !isConnectionEnabledForRuns(settings, id)) return false;
  return deriveVisibleConnections(next).some(
    (conn) => conn.id === id && conn.readiness === 'ready',
  );
}
