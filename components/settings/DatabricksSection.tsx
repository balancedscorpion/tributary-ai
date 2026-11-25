import React, { useState, useCallback, useEffect, useRef } from 'react';
import {
  Database,
  Shield,
  CheckCircle2,
  XCircle,
  RefreshCw,
  FlaskConical,
  AlertTriangle,
} from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import type { AppSettings, DataSourceState } from '@/types';
import SecretField from './SecretField';
import { getSecretsStatus } from '@/services/secretsApi';
import {
  getDatabricksStatus,
  testDatabricksConnection,
  startDatabricksOAuth,
  completeDatabricksOAuth,
  getDatabricksOAuthResult,
  type DatabricksStatus,
  type DatabricksTestResult,
  type DatabricksAuthMode,
} from '@/services/databricksApi';
import { openExternalUrl } from '@/services/openExternal';

interface DatabricksSectionProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  savedSettings: AppSettings;
  onSave: (settings: AppSettings) => void | Promise<void>;
}

const DATABRICKS_COLOR = '#FF3621';

type AuthOption = {
  id: DatabricksAuthMode;
  label: string;
  desc: string;
};

const AUTH_OPTIONS: AuthOption[] = [
  {
    id: 'refresh_token',
    label: 'Refresh Token',
    desc: 'Auto-renews access tokens. Recommended for long-lived sessions.',
  },
  {
    id: 'pat',
    label: 'Personal Access Token',
    desc: 'Static token with configurable expiry (typically 90 days).',
  },
];

/**
 * Returns true if the host looks like a GCP Databricks workspace URL.
 * Accepts patterns like: https://1234567890.7.gcp.databricks.com
 */
function isGcpDatabricksHost(host: string): boolean {
  if (!host.trim()) return true; // Don't warn on empty
  try {
    const u = new URL(host.startsWith('http') ? host : `https://${host}`);
    return u.hostname.endsWith('.gcp.databricks.com');
  } catch {
    return false;
  }
}

function extractAuthorizationCode(inputRaw: string, expectedState: string): string {
  const input = inputRaw.trim();
  if (!input) {
    throw new Error('Paste the redirect URL or authorization code.');
  }

  const validateState = (stateValue: string | null) => {
    const state = (stateValue || '').trim();
    if (!state || !expectedState) return;
    if (state !== expectedState) {
      throw new Error('OAuth state mismatch. Start a new authorization attempt and retry.');
    }
  };

  const parseParams = (params: URLSearchParams): string => {
    validateState(params.get('state'));
    const code = (params.get('code') || '').trim();
    if (code) return code;
    const err = (params.get('error_description') || params.get('error') || '').trim();
    if (err) {
      throw new Error(`Databricks authorization returned an error: ${err}`);
    }
    throw new Error('No authorization code found. Paste the full redirect URL or just the code value.');
  };

  if (/^https?:\/\//i.test(input)) {
    let parsed: URL;
    try {
      parsed = new URL(input);
    } catch {
      throw new Error('Invalid redirect URL. Paste the full URL from the browser address bar.');
    }
    return parseParams(parsed.searchParams);
  }

  if (input.includes('code=') || input.includes('state=')) {
    const query = input.includes('?') ? input.slice(input.indexOf('?') + 1) : input;
    return parseParams(new URLSearchParams(query));
  }

  return input;
}

const databricksDataSource = (state?: AppSettings['dataSources']): DataSourceState => {
  const raw = state?.databricks;
  return {
    enabled: Boolean(raw?.enabled),
    lastCheckedAt: raw?.lastCheckedAt,
    lastCheckOk: raw?.lastCheckOk,
    lastCheckMessage: raw?.lastCheckMessage,
    lastProjectId: raw?.lastProjectId,
    lastWarehouseId: raw?.lastWarehouseId,
    lastHost: raw?.lastHost,
  };
};

const upsertDatabricksState = (
  current: AppSettings,
  patch: Partial<DataSourceState>,
): AppSettings => {
  const prev = databricksDataSource(current.dataSources);
  return {
    ...current,
    dataSources: {
      ...current.dataSources,
      databricks: { ...prev, ...patch },
    },
  };
};

const formatTimestamp = (ts: number | null | undefined): string => {
  if (!ts) return 'Never';
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return 'Never';
  }
};

const DatabricksSection: React.FC<DatabricksSectionProps> = ({
  localSettings,
  setLocalSettings,
  savedSettings,
  onSave,
}) => {
  const [expanded, setExpanded] = useState(false);
  const [authMode, setAuthMode] = useState<DatabricksAuthMode>('refresh_token');

  // Secret state -- tracks whether each credential is set in secure storage
  const [refreshTokenSet, setRefreshTokenSet] = useState(false);
  const [patSet, setPatSet] = useState(false);

  // Status
  const [statusLoading, setStatusLoading] = useState(false);
  const [status, setStatus] = useState<DatabricksStatus | null>(null);

  // Connection test
  const [testLoading, setTestLoading] = useState(false);
  const [testResult, setTestResult] = useState<DatabricksTestResult | null>(null);
  const [testError, setTestError] = useState<string | null>(null);
  const [oauthLoading, setOauthLoading] = useState(false);
  const [pendingOAuthState, setPendingOAuthState] = useState('');
  const [pendingOAuthUrl, setPendingOAuthUrl] = useState('');
  const [manualOAuthInput, setManualOAuthInput] = useState('');
  const [manualOAuthSubmitting, setManualOAuthSubmitting] = useState(false);
  const oauthCodeSubmitInFlightRef = useRef(false);
  const lastAutoCodeKeyRef = useRef('');

  const host = (localSettings.databricksHost || '').trim();
  const warehouseId = (localSettings.databricksWarehouseId || '').trim();
  const oauthClientId = (localSettings.databricksOAuthClientId || '').trim();
  const hostIsGcp = isGcpDatabricksHost(host);

  // Refresh status from backend on mount
  const refreshStatus = useCallback(async () => {
    setStatusLoading(true);
    try {
      const st = await getDatabricksStatus();
      setStatus(st);
      if (st.authMode !== 'none') {
        setAuthMode(st.authMode);
      }
    } catch {
      // Backend may not have the endpoint yet; silently ignore
      setStatus(null);
    }
    // Also check secrets store for Databricks credential flags
    try {
      const secrets = await getSecretsStatus();
      setRefreshTokenSet(Boolean(secrets.present.databricks_refresh_token));
      setPatSet(Boolean(secrets.present.databricks_pat));
    } catch {
      // Secrets store may be locked or unavailable
    }
    setStatusLoading(false);
  }, []);

  useEffect(() => {
    if (expanded) void refreshStatus();
  }, [expanded, refreshStatus]);

  // Persist Databricks settings fields to the backend if they've changed.
  // This ensures the Go handler has the correct host/warehouseId/clientId
  // before OAuth flows or connection tests, so chat tools work immediately.
  const ensureDatabricksSettingsSaved = useCallback(async () => {
    const hostChanged = (localSettings.databricksHost || '') !== (savedSettings.databricksHost || '');
    const whChanged = (localSettings.databricksWarehouseId || '') !== (savedSettings.databricksWarehouseId || '');
    const clientChanged = (localSettings.databricksOAuthClientId || '') !== (savedSettings.databricksOAuthClientId || '');
    if (hostChanged || whChanged || clientChanged) {
      await Promise.resolve(onSave({
        ...savedSettings,
        databricksHost: localSettings.databricksHost,
        databricksWarehouseId: localSettings.databricksWarehouseId,
        databricksOAuthClientId: localSettings.databricksOAuthClientId,
      }));
    }
  }, [localSettings.databricksHost, localSettings.databricksWarehouseId, localSettings.databricksOAuthClientId, savedSettings, onSave]);

  const persistDatabricksState = useCallback(async (patch: Partial<DataSourceState>) => {
    let next: AppSettings | null = null;
    setLocalSettings((prev) => {
      next = upsertDatabricksState(prev, patch);
      return next;
    });
    const saved = next || localSettings;
    await Promise.resolve(onSave(saved));
    return saved;
  }, [localSettings, onSave, setLocalSettings]);

  const postJson = useCallback(async (path: string, body: any) => {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      throw new Error(text || `HTTP ${res.status}`);
    }
  }, []);

  const handleSetRefreshToken = useCallback(async (value: string) => {
    try {
      await postJson('/api/secrets/set', {
        providerId: 'databricks',
        authType: 'refresh_token',
        refreshToken: value,
        ...(oauthClientId ? { clientId: oauthClientId } : {}),
      });
      setRefreshTokenSet(true);
      toast.success('Databricks refresh token saved to secure storage.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save refresh token');
    }
  }, [oauthClientId, postJson]);

  const handleClearRefreshToken = useCallback(async () => {
    try {
      await postJson('/api/secrets/clear', {
        providerId: 'databricks',
        authType: 'refresh_token',
      });
      setRefreshTokenSet(false);
      setTestResult(null);
      toast.success('Databricks refresh token cleared.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear refresh token');
    }
  }, [postJson]);

  const submitOAuthCode = useCallback(async (
    state: string,
    code: string,
    source: 'manual' | 'auto',
  ) => {
    const stateTrimmed = state.trim();
    const codeTrimmed = code.trim();
    if (!stateTrimmed || !codeTrimmed) {
      return false;
    }
    if (oauthCodeSubmitInFlightRef.current) {
      return false;
    }

    oauthCodeSubmitInFlightRef.current = true;
    setManualOAuthSubmitting(true);
    try {
      const result = await completeDatabricksOAuth({
        state: stateTrimmed,
        code: codeTrimmed,
      });
      if (!result.ok && result.status !== 'success') {
        throw new Error(result.error || result.message || 'Databricks OAuth authorization failed.');
      }
      setManualOAuthInput('');
      setPendingOAuthUrl('');
      if (source === 'manual') {
        toast.success('Authorization code accepted. Finalizing token setup...');
      }
      return true;
    } catch (e: any) {
      toast.error(e?.message || 'Failed to complete OAuth with authorization code');
      return false;
    } finally {
      setManualOAuthSubmitting(false);
      oauthCodeSubmitInFlightRef.current = false;
    }
  }, []);

  const handleGenerateRefreshToken = useCallback(async () => {
    if (!host) {
      toast.error('Databricks host is required before starting OAuth.');
      return;
    }
    if (!hostIsGcp) {
      toast.error('Databricks host must be a GCP workspace URL (*.gcp.databricks.com).');
      return;
    }

    setOauthLoading(true);
    setPendingOAuthState('');
    setPendingOAuthUrl('');
    setManualOAuthInput('');
    lastAutoCodeKeyRef.current = '';
    oauthCodeSubmitInFlightRef.current = false;
    try {
      await ensureDatabricksSettingsSaved();

      const started = await startDatabricksOAuth({
        host,
        clientId: oauthClientId || undefined,
      });
      setPendingOAuthState(started.state);
      setPendingOAuthUrl(started.authorizeUrl);
      const opened = await openExternalUrl(started.authorizeUrl);
      if (!opened) {
        toast.message('Open the Databricks authorization link shown below to continue.');
      }

      const timeoutMs = Math.max(started.expiresInSeconds * 1000, 2 * 60 * 1000);
      const deadline = Date.now() + timeoutMs;

      while (Date.now() < deadline) {
        await new Promise((resolve) => window.setTimeout(resolve, 1250));

        let result: Awaited<ReturnType<typeof getDatabricksOAuthResult>> | null = null;
        try {
          result = await getDatabricksOAuthResult(started.state);
        } catch (pollErr: any) {
          const status = Number(pollErr?.status ?? NaN);
          if (status === 404) {
            continue;
          }
          throw pollErr;
        }
        if (!result || result.status === 'pending') {
          continue;
        }

        if (result.ok || result.status === 'success') {
          setAuthMode('refresh_token');
          setRefreshTokenSet(true);
          setPatSet(false);
          setPendingOAuthUrl('');
          await refreshStatus();
          toast.success(result.message || 'Databricks refresh token generated and saved.');
          return;
        }
        throw new Error(result.error || result.message || 'Databricks OAuth authorization failed.');
      }

      throw new Error('Timed out waiting for Databricks OAuth authorization to complete.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to generate Databricks refresh token');
    } finally {
      setPendingOAuthState('');
      setManualOAuthInput('');
      lastAutoCodeKeyRef.current = '';
      oauthCodeSubmitInFlightRef.current = false;
      setOauthLoading(false);
    }
  }, [host, hostIsGcp, oauthClientId, refreshStatus, ensureDatabricksSettingsSaved]);

  const handleCompleteOAuthManually = useCallback(async () => {
    if (!pendingOAuthState.trim()) {
      toast.error('OAuth session not found. Start Generate first.');
      return;
    }

    let code = '';
    try {
      code = extractAuthorizationCode(manualOAuthInput, pendingOAuthState);
    } catch (err: any) {
      toast.error(err?.message || 'Invalid authorization code input.');
      return;
    }
    await submitOAuthCode(pendingOAuthState, code, 'manual');
  }, [manualOAuthInput, pendingOAuthState, submitOAuthCode]);

  useEffect(() => {
    const onOAuthPopupMessage = (event: MessageEvent) => {
      if (!pendingOAuthState.trim()) return;
      const data = event.data;
      if (!data || typeof data !== 'object') return;

      const payload = data as Record<string, unknown>;
      const msgType = typeof payload.type === 'string' ? payload.type.trim() : '';
      if (msgType !== 'databricks-oauth-code') return;

      const msgState = typeof payload.state === 'string' ? payload.state.trim() : '';
      if (!msgState || msgState !== pendingOAuthState.trim()) return;

      const msgError = typeof payload.error === 'string' ? payload.error.trim() : '';
      if (msgError) {
        toast.error(`Databricks authorization failed: ${msgError}`);
        return;
      }

      const msgCode = typeof payload.code === 'string' ? payload.code.trim() : '';
      if (!msgCode) return;

      const key = `${msgState}|${msgCode}`;
      if (lastAutoCodeKeyRef.current === key) return;
      lastAutoCodeKeyRef.current = key;

      setManualOAuthInput(msgCode);
      void submitOAuthCode(msgState, msgCode, 'auto');
    };

    window.addEventListener('message', onOAuthPopupMessage);
    return () => window.removeEventListener('message', onOAuthPopupMessage);
  }, [pendingOAuthState, submitOAuthCode]);

  const handleSetPat = useCallback(async (value: string) => {
    try {
      await postJson('/api/secrets/set', {
        providerId: 'databricks',
        authType: 'pat',
        apiKey: value,
      });
      setPatSet(true);
      toast.success('Databricks PAT saved to secure storage.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save PAT');
    }
  }, [postJson]);

  const handleClearPat = useCallback(async () => {
    try {
      await postJson('/api/secrets/clear', {
        providerId: 'databricks',
        authType: 'pat',
      });
      setPatSet(false);
      setTestResult(null);
      toast.success('Databricks PAT cleared.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear PAT');
    }
  }, [postJson]);

  const handleTestConnection = useCallback(async () => {
    setTestLoading(true);
    setTestError(null);
    setTestResult(null);
    try {
      await ensureDatabricksSettingsSaved();
      const result = await testDatabricksConnection({
        host,
        warehouseId,
      });
      setTestResult(result);
      await persistDatabricksState({
        enabled: result.ok,
        lastCheckedAt: Date.now(),
        lastCheckOk: result.ok,
        lastCheckMessage: result.message || (result.ok ? 'Databricks connection verified.' : null),
        lastHost: host || null,
        lastWarehouseId: warehouseId || null,
      });
      if (result.ok) {
        toast.success(result.message || 'Databricks connection successful.');
      } else {
        toast.error(result.message || 'Databricks connection test failed.');
      }
    } catch (e: any) {
      try {
        await persistDatabricksState({
          enabled: false,
          lastCheckedAt: Date.now(),
          lastCheckOk: false,
          lastCheckMessage: e?.message || 'Databricks connection test failed.',
          lastHost: host || null,
          lastWarehouseId: warehouseId || null,
        });
      } catch (saveErr: any) {
        toast.error(saveErr?.message || 'Failed to save Databricks datasource state');
      }
      setTestError(e?.message || 'Connection test failed.');
      toast.error(e?.message || 'Connection test failed.');
      return;
    } finally {
      setTestLoading(false);
    }
  }, [host, warehouseId, ensureDatabricksSettingsSaved, persistDatabricksState]);

  const dsState = databricksDataSource(localSettings.dataSources);
  const databricksEnabled = dsState.enabled === true;
  const databricksValidated = dsState.lastCheckOk === true;
  const databricksStatusText = dsState.lastCheckOk === true
    ? 'Validation passed'
    : dsState.lastCheckOk === false
      ? 'Validation failed'
      : 'Not validated yet';

  const handleDatabricksToggle = useCallback(async (enabled: boolean) => {
    if (enabled && !databricksValidated) {
      toast.error('Run a Databricks connection test successfully before enabling SQL tools.');
      return;
    }
    try {
      await persistDatabricksState({
        enabled,
      });
    } catch (err: any) {
      toast.error(err?.message || 'Failed to save Databricks datasource state');
    }
  }, [databricksValidated, persistDatabricksState]);

  const hasAnyCredential = refreshTokenSet || patSet;
  const activeCredentialSet = authMode === 'refresh_token' ? refreshTokenSet : patSet;

  // Collapsed state -- show a compact summary card
  if (!expanded) {
    return (
      <button
        type="button"
        className="w-full flex items-center gap-3 p-3 rounded-lg border border-border bg-card/30 hover:bg-card/60 transition-colors text-left"
        onClick={() => setExpanded(true)}
      >
        <div
          className="w-8 h-8 rounded-md border flex items-center justify-center text-sm font-bold shrink-0"
          style={{
            color: DATABRICKS_COLOR,
            backgroundColor: `${DATABRICKS_COLOR}14`,
            borderColor: `${DATABRICKS_COLOR}30`,
          }}
        >
          <Database size={16} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium">Databricks</div>
          <div className="text-[11px] text-muted-foreground truncate">
            {host
              ? host.replace(/^https?:\/\//, '')
              : 'Not configured'}
            {warehouseId ? ` - ${warehouseId}` : ''}
          </div>
          <div className={`text-[10px] mt-1 ${dsState.lastCheckOk ? 'text-emerald-400' : dsState.lastCheckOk === false ? 'text-destructive' : 'text-muted-foreground'}`}>
            {databricksStatusText}
          </div>
          {dsState.lastCheckMessage ? (
            <div className="text-[10px] text-muted-foreground/80 mt-1">{dsState.lastCheckMessage}</div>
          ) : null}
          {dsState.lastCheckedAt ? (
            <div className="text-[10px] text-muted-foreground/80 mt-1">Last checked: {formatTimestamp(dsState.lastCheckedAt)}</div>
          ) : null}
        </div>
        <div className="shrink-0">
          <div className="text-right space-y-1">
            <div className="text-[10px] font-medium">
              {databricksEnabled ? (
                <span className="text-emerald-400">Enabled</span>
              ) : (
                <span className="text-muted-foreground/80">Disabled</span>
              )}
            </div>
            {hasAnyCredential ? (
              <span className="text-[9px] font-semibold text-emerald-500 bg-emerald-500/10 px-1.5 py-0.5 rounded">
                {refreshTokenSet ? 'Refresh Token' : 'PAT'}
              </span>
            ) : (
              <span className="text-[9px] font-medium text-muted-foreground/60 bg-muted/40 px-1.5 py-0.5 rounded">
                No auth
              </span>
            )}
          </div>
        </div>
      </button>
    );
  }

  // Expanded state -- full configuration form
  return (
    <div className="rounded-lg border border-border bg-card/30 overflow-hidden">
      {/* Header */}
      <button
        type="button"
        className="w-full flex items-center gap-3 p-3 hover:bg-card/60 transition-colors text-left border-b border-border"
        onClick={() => setExpanded(false)}
      >
        <div
          className="w-8 h-8 rounded-md border flex items-center justify-center text-sm font-bold shrink-0"
          style={{
            color: DATABRICKS_COLOR,
            backgroundColor: `${DATABRICKS_COLOR}14`,
            borderColor: `${DATABRICKS_COLOR}30`,
          }}
        >
          <Database size={16} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium">Databricks</div>
          <div className="text-[11px] text-muted-foreground">GCP workspace connection</div>
        </div>
        {statusLoading && (
          <RefreshCw size={13} className="text-muted-foreground animate-spin shrink-0" />
        )}
      </button>

      <div className="p-3 space-y-4">
        {/* ── Enable Toggle ── */}
        <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2">
          <div className="flex items-start justify-between gap-2">
            <div>
              <div className="text-xs font-medium">Enable Databricks SQL Tools</div>
              <div className="text-[10px] text-muted-foreground">
                {databricksEnabled
                  ? databricksValidated
                    ? 'Enabled and validated'
                    : 'Enabled, but validation failed. Re-run test or disable.'
                  : databricksValidated
                    ? 'Validation passed. Enable to expose Databricks tools to the assistant.'
                    : 'Validation required before enabling Databricks SQL tools.'}
              </div>
            </div>
            <Switch
              checked={databricksEnabled}
              onCheckedChange={handleDatabricksToggle}
              disabled={!databricksValidated && !databricksEnabled}
            />
          </div>
          {dsState.lastCheckMessage ? (
            <div className="text-[10px] text-muted-foreground/80">{dsState.lastCheckMessage}</div>
          ) : null}
          {dsState.lastCheckedAt ? (
            <div className="text-[10px] text-muted-foreground/80">
              Last validated: {new Date(dsState.lastCheckedAt).toLocaleString()}
            </div>
          ) : null}
        </div>
        {/* ── Connection Details ── */}
        <div className="space-y-3">
          <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
            <Database size={12} /> Connection
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs">Host URL</Label>
            <Input
              placeholder="https://1234567890.7.gcp.databricks.com"
              value={localSettings.databricksHost || ''}
              onChange={(e) =>
                setLocalSettings((prev) => ({ ...prev, databricksHost: e.target.value }))
              }
              className="font-mono"
            />
            {host && !hostIsGcp && (
              <div className="flex items-start gap-1.5 text-[10px] text-amber-400">
                <AlertTriangle size={11} className="mt-0.5 shrink-0" />
                <span>
                  This doesn't look like a GCP Databricks workspace. Expected format:{' '}
                  <span className="font-mono">*.gcp.databricks.com</span>
                </span>
              </div>
            )}
            {!host && (
              <p className="text-[10px] text-muted-foreground">
                Your Databricks workspace URL on GCP.
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs">SQL Warehouse ID</Label>
            <Input
              placeholder="Enter your SQL warehouse ID"
              value={localSettings.databricksWarehouseId || ''}
              onChange={(e) =>
                setLocalSettings((prev) => ({ ...prev, databricksWarehouseId: e.target.value }))
              }
              className="font-mono"
            />
            <p className="text-[10px] text-muted-foreground">
              Found in Databricks SQL &rarr; SQL Warehouses &rarr; Connection details.
            </p>
          </div>
        </div>

        {/* ── Authentication ── */}
        <div className="space-y-3">
          <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
            <Shield size={12} /> Authentication
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs">OAuth Client ID (Optional)</Label>
            <Input
              placeholder="databricks-cli"
              value={localSettings.databricksOAuthClientId || ''}
              onChange={(e) =>
                setLocalSettings((prev) => ({ ...prev, databricksOAuthClientId: e.target.value }))
              }
              className="font-mono"
            />
            <p className="text-[10px] text-muted-foreground">
              Used for PKCE/OAuth token generation and refresh exchange. Leave empty to use{' '}
              <span className="font-mono">databricks-cli</span>.
            </p>
          </div>

          {/* Auth mode selector */}
          <div className="grid grid-cols-2 gap-2">
            {AUTH_OPTIONS.map((opt) => {
              const selected = authMode === opt.id;
              const isSet =
                opt.id === 'refresh_token' ? refreshTokenSet : patSet;
              return (
                <button
                  key={opt.id}
                  type="button"
                  className="relative flex flex-col items-start p-2.5 rounded-lg border text-left transition-all hover:bg-card/60"
                  style={{
                    borderColor: selected ? DATABRICKS_COLOR : 'hsl(var(--border))',
                    boxShadow: selected ? `0 0 0 1px ${DATABRICKS_COLOR}40` : undefined,
                    backgroundColor: selected ? `${DATABRICKS_COLOR}06` : undefined,
                  }}
                  onClick={() => setAuthMode(opt.id as DatabricksAuthMode)}
                >
                  <div className="flex items-center gap-2 mb-1">
                    <div
                      className="w-3.5 h-3.5 rounded-full border-2 flex items-center justify-center shrink-0"
                      style={{
                        borderColor: selected ? DATABRICKS_COLOR : 'hsl(var(--border))',
                      }}
                    >
                      {selected && (
                        <div
                          className="w-1.5 h-1.5 rounded-full"
                          style={{ backgroundColor: DATABRICKS_COLOR }}
                        />
                      )}
                    </div>
                    <span className="text-xs font-medium">{opt.label}</span>
                    {opt.id === 'refresh_token' && (
                      <span className="text-[8px] font-semibold text-emerald-500 bg-emerald-500/10 px-1 py-0.5 rounded leading-none">
                        Recommended
                      </span>
                    )}
                  </div>
                  <div className="text-[10px] text-muted-foreground leading-snug pl-5.5">
                    {opt.desc}
                  </div>
                  {isSet && (
                    <div className="absolute top-2 right-2">
                      <CheckCircle2 size={12} className="text-emerald-500" />
                    </div>
                  )}
                </button>
              );
            })}
          </div>

          {/* Credential field for selected auth mode */}
          <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2">
            {authMode === 'refresh_token' ? (
              <>
                <div className="flex items-center justify-between gap-2">
                  <div className="text-[11px] text-muted-foreground">
                    Generate a refresh token via Databricks OAuth.
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 px-2 text-[11px]"
                    disabled={oauthLoading || !host || !hostIsGcp}
                    onClick={() => void handleGenerateRefreshToken()}
                  >
                    {oauthLoading ? (
                      <>
                        <RefreshCw size={11} className="mr-1 animate-spin" />
                        Waiting...
                      </>
                    ) : (
                      'Generate'
                    )}
                  </Button>
                </div>
                {pendingOAuthState && (
                  <div className="rounded-md border border-border bg-card/30 p-2.5 space-y-2">
                    <p className="text-[10px] text-muted-foreground leading-relaxed">
                      Complete authorization in your browser. If it does not open automatically
                      {pendingOAuthUrl ? (
                        <>
                          ,{' '}
                          <a href={pendingOAuthUrl} target="_blank" rel="noreferrer" className="underline underline-offset-2">
                            open Databricks authorization
                          </a>
                        </>
                      ) : null}
                      . If Databricks redirects to an unavailable localhost page, copy the full
                      redirect URL (or the <span className="font-mono">code</span> value) and paste it below.
                    </p>
                    <Input
                      value={manualOAuthInput}
                      onChange={(e) => setManualOAuthInput(e.target.value)}
                      placeholder="http://localhost:12345/?code=...&state=..."
                      className="h-8 text-[11px] font-mono"
                      autoComplete="off"
                      spellCheck={false}
                    />
                    <div className="flex justify-end">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        className="h-7 px-2 text-[11px]"
                        disabled={manualOAuthSubmitting || !manualOAuthInput.trim()}
                        onClick={() => void handleCompleteOAuthManually()}
                      >
                        {manualOAuthSubmitting ? (
                          <>
                            <RefreshCw size={11} className="mr-1 animate-spin" />
                            Submitting...
                          </>
                        ) : (
                          'Complete with code'
                        )}
                      </Button>
                    </div>
                  </div>
                )}
                <SecretField
                  label="Refresh Token"
                  placeholder="doau2c95f1..."
                  isSet={refreshTokenSet}
                  onSet={handleSetRefreshToken}
                  onClear={handleClearRefreshToken}
                />
                <p className="text-[10px] text-muted-foreground leading-relaxed">
                  Obtained via the Databricks PKCE/OAuth flow. The server will automatically
                  exchange this for short-lived access tokens.
                </p>
              </>
            ) : (
              <>
                <SecretField
                  label="Personal Access Token"
                  placeholder="dapi..."
                  isSet={patSet}
                  onSet={handleSetPat}
                  onClear={handleClearPat}
                />
                <p className="text-[10px] text-muted-foreground leading-relaxed">
                  Generate in Databricks &rarr; User Settings &rarr; Developer &rarr; Access
                  tokens. Typically expires after 90 days.
                </p>
              </>
            )}
          </div>
        </div>

        {/* ── Connection Test ── */}
        <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2.5">
          <div className="flex items-center justify-between gap-3">
            <div className="text-xs font-medium">Test Connection</div>
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5"
              onClick={() => void handleTestConnection()}
              disabled={testLoading || !host || !warehouseId || !activeCredentialSet}
            >
              {testLoading ? (
                <>
                  <RefreshCw size={12} className="animate-spin" />
                  Testing...
                </>
              ) : (
                <>
                  <FlaskConical size={12} />
                  Test
                </>
              )}
            </Button>
          </div>

          {!host && !warehouseId && !activeCredentialSet && (
            <p className="text-[10px] text-muted-foreground">
              Enter a host, warehouse ID, and set credentials to test the connection.
            </p>
          )}

          {testError && (
            <div className="flex items-start gap-1.5 text-xs text-destructive">
              <XCircle size={13} className="mt-0.5 shrink-0" />
              <span>{testError}</span>
            </div>
          )}

          {testResult && !testError && (
            <div
              className={`flex items-start gap-1.5 text-xs ${testResult.ok ? 'text-emerald-400' : 'text-destructive'}`}
            >
              {testResult.ok ? (
                <CheckCircle2 size={13} className="mt-0.5 shrink-0" />
              ) : (
                <XCircle size={13} className="mt-0.5 shrink-0" />
              )}
              <div>
                <div>
                  {testResult.ok ? 'Connected' : 'Failed'}
                  {testResult.warehouseName && (
                    <span className="font-mono"> &middot; {testResult.warehouseName}</span>
                  )}
                </div>
                {testResult.warehouseState && testResult.warehouseState !== 'RUNNING' && (
                  <div className="text-amber-400 text-[10px] mt-0.5">
                    Warehouse state: {testResult.warehouseState}. Start it from the Databricks
                    console to run queries.
                  </div>
                )}
                {testResult.message && !testResult.ok && (
                  <div className="text-[10px] mt-0.5 opacity-80">{testResult.message}</div>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};

export default DatabricksSection;
