import React, { useCallback, useEffect, useState } from 'react';
import {
  Database,
  Shield,
  CheckCircle2,
  XCircle,
  RefreshCw,
  FlaskConical,
} from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import type { AppSettings, DataSourceState, TrinoAuthMode } from '@/types';
import SecretField from './SecretField';
import { getSecretsStatus } from '@/services/secretsApi';
import {
  getTrinoStatus,
  testTrinoConnection,
  type TrinoStatus,
  type TrinoTestResult,
} from '@/services/trinoApi';

interface TrinoSectionProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  savedSettings: AppSettings;
  onSave: (settings: AppSettings) => void | Promise<void>;
}

const TRINO_COLOR = '#1F9D6A';

const AUTH_OPTIONS: Array<{ id: TrinoAuthMode; label: string; desc: string }> = [
  {
    id: 'basic',
    label: 'Basic',
    desc: 'Username/password auth. Requires a secure password secret.',
  },
  {
    id: 'jwt',
    label: 'JWT',
    desc: 'Bearer token auth with a secure JWT token.',
  },
  {
    id: 'none',
    label: 'None',
    desc: 'No authentication headers are added.',
  },
];

const trinoDataSource = (state?: AppSettings['dataSources']): DataSourceState => {
  const raw = state?.trino;
  return {
    enabled: Boolean(raw?.enabled),
    lastCheckedAt: raw?.lastCheckedAt,
    lastCheckOk: raw?.lastCheckOk,
    lastCheckMessage: raw?.lastCheckMessage,
    lastHost: raw?.lastHost,
  };
};

const upsertTrinoState = (
  current: AppSettings,
  patch: Partial<DataSourceState>,
): AppSettings => {
  const prev = trinoDataSource(current.dataSources);
  return {
    ...current,
    dataSources: {
      ...current.dataSources,
      trino: { ...prev, ...patch },
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

const TrinoSection: React.FC<TrinoSectionProps> = ({
  localSettings,
  setLocalSettings,
  savedSettings,
  onSave,
}) => {
  const [expanded, setExpanded] = useState(false);
  const [authMode, setAuthMode] = useState<TrinoAuthMode>('none');
  const [basicSecretSet, setBasicSecretSet] = useState(false);
  const [jwtSecretSet, setJwtSecretSet] = useState(false);
  const [statusLoading, setStatusLoading] = useState(false);
  const [status, setStatus] = useState<TrinoStatus | null>(null);
  const [testLoading, setTestLoading] = useState(false);
  const [testResult, setTestResult] = useState<TrinoTestResult | null>(null);
  const [testError, setTestError] = useState<string | null>(null);

  const host = (localSettings.trinoHost || '').trim();
  const catalog = (localSettings.trinoCatalog || '').trim();
  const schema = (localSettings.trinoSchema || '').trim();
  const user = (localSettings.trinoUser || '').trim();
  const source = (localSettings.trinoSource || '').trim();

  const refreshStatus = useCallback(async () => {
    setStatusLoading(true);
    let backendAuthMode: TrinoAuthMode | null = null;
    try {
      const st = await getTrinoStatus();
      setStatus(st);
      backendAuthMode = st.authMode;
      setAuthMode(st.authMode);
    } catch {
      setStatus(null);
    }

    try {
      const secrets = await getSecretsStatus();
      const hasBasic = Boolean(
        secrets.present.trino_basic_password
        || secrets.present.trino_password
        || secrets.present.trino_basic
        || secrets.present['provider:trino:basic'],
      );
      const hasJwt = Boolean(
        secrets.present.trino_jwt
        || secrets.present.trino_jwt_token
        || secrets.present['provider:trino:jwt'],
      );
      const hasGeneric = Boolean(secrets.present.trino_credentials || secrets.present['provider:trino']);

      setBasicSecretSet(hasBasic || (hasGeneric && backendAuthMode === 'basic'));
      setJwtSecretSet(hasJwt || (hasGeneric && backendAuthMode === 'jwt'));
    } catch {
      // ignore
    } finally {
      setStatusLoading(false);
    }
  }, []);

  useEffect(() => {
    if (expanded) void refreshStatus();
  }, [expanded, refreshStatus]);

  const ensureTrinoSettingsSaved = useCallback(async () => {
    const hostChanged = (localSettings.trinoHost || '') !== (savedSettings.trinoHost || '');
    const catalogChanged = (localSettings.trinoCatalog || '') !== (savedSettings.trinoCatalog || '');
    const schemaChanged = (localSettings.trinoSchema || '') !== (savedSettings.trinoSchema || '');
    const userChanged = (localSettings.trinoUser || '') !== (savedSettings.trinoUser || '');
    const sourceChanged = (localSettings.trinoSource || '') !== (savedSettings.trinoSource || '');
    if (hostChanged || catalogChanged || schemaChanged || userChanged || sourceChanged) {
      await Promise.resolve(onSave({
        ...savedSettings,
        trinoHost: localSettings.trinoHost,
        trinoCatalog: localSettings.trinoCatalog,
        trinoSchema: localSettings.trinoSchema,
        trinoUser: localSettings.trinoUser,
        trinoSource: localSettings.trinoSource,
      }));
    }
  }, [
    localSettings.trinoCatalog,
    localSettings.trinoHost,
    localSettings.trinoSchema,
    localSettings.trinoSource,
    localSettings.trinoUser,
    onSave,
    savedSettings,
  ]);

  const persistTrinoState = useCallback(async (patch: Partial<DataSourceState>) => {
    let next: AppSettings | null = null;
    setLocalSettings((prev) => {
      next = upsertTrinoState(prev, patch);
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

  const handleSetBasicSecret = useCallback(async (value: string) => {
    if (!user) {
      toast.error('Enter Trino user before saving Basic password.');
      return;
    }
    try {
      await postJson('/api/secrets/set', {
        providerId: 'trino',
        authType: 'basic',
        username: user,
        password: value,
      });
      setBasicSecretSet(true);
      toast.success('Trino basic password saved to secure storage.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Trino basic password');
    }
  }, [postJson, user]);

  const handleClearBasicSecret = useCallback(async () => {
    try {
      await postJson('/api/secrets/clear', {
        providerId: 'trino',
        authType: 'basic',
      });
      setBasicSecretSet(false);
      setTestResult(null);
      toast.success('Trino basic password cleared.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear Trino basic password');
    }
  }, [postJson]);

  const handleSetJwtSecret = useCallback(async (value: string) => {
    try {
      await postJson('/api/secrets/set', {
        providerId: 'trino',
        authType: 'jwt',
        jwt: value,
        token: value,
      });
      setJwtSecretSet(true);
      toast.success('Trino JWT token saved to secure storage.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Trino JWT token');
    }
  }, [postJson]);

  const handleClearJwtSecret = useCallback(async () => {
    try {
      await postJson('/api/secrets/clear', {
        providerId: 'trino',
        authType: 'jwt',
      });
      setJwtSecretSet(false);
      setTestResult(null);
      toast.success('Trino JWT token cleared.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear Trino JWT token');
    }
  }, [postJson]);

  const handleTestConnection = useCallback(async () => {
    setTestLoading(true);
    setTestError(null);
    setTestResult(null);
    try {
      if (authMode === 'none') {
        await postJson('/api/secrets/set', {
          providerId: 'trino',
          authType: 'none',
        });
        setBasicSecretSet(false);
        setJwtSecretSet(false);
      }
      await ensureTrinoSettingsSaved();
      const result = await testTrinoConnection({
        host,
        catalog,
        schema,
        source,
      });
      setTestResult(result);
      await persistTrinoState({
        enabled: result.ok,
        lastCheckedAt: Date.now(),
        lastCheckOk: result.ok,
        lastCheckMessage: result.message || (result.ok ? 'Trino connection verified.' : null),
        lastHost: host || null,
      });
      if (result.ok) toast.success(result.message || 'Trino connection successful.');
      else toast.error(result.message || 'Trino connection test failed.');
    } catch (e: any) {
      try {
        await persistTrinoState({
          enabled: false,
          lastCheckedAt: Date.now(),
          lastCheckOk: false,
          lastCheckMessage: e?.message || 'Trino connection test failed.',
          lastHost: host || null,
        });
      } catch (saveErr: any) {
        toast.error(saveErr?.message || 'Failed to save Trino datasource state');
      }
      setTestError(e?.message || 'Connection test failed.');
      toast.error(e?.message || 'Connection test failed.');
      return;
    } finally {
      setTestLoading(false);
    }
  }, [authMode, catalog, ensureTrinoSettingsSaved, host, persistTrinoState, postJson, schema, source]);

  const dsState = trinoDataSource(localSettings.dataSources);
  const trinoEnabled = dsState.enabled === true;
  const trinoValidated = dsState.lastCheckOk === true;
  const trinoStatusText = dsState.lastCheckOk === true
    ? 'Validation passed'
    : dsState.lastCheckOk === false
      ? 'Validation failed'
      : 'Not validated yet';

  const handleTrinoToggle = useCallback(async (enabled: boolean) => {
    if (enabled && !trinoValidated) {
      toast.error('Run a Trino connection test successfully before enabling SQL tools.');
      return;
    }
    try {
      await persistTrinoState({ enabled });
    } catch (err: any) {
      toast.error(err?.message || 'Failed to save Trino datasource state');
    }
  }, [persistTrinoState, trinoValidated]);

  const hasAnyCredential = basicSecretSet || jwtSecretSet;
  const activeCredentialSet = authMode === 'none' ? true : authMode === 'basic' ? basicSecretSet : jwtSecretSet;
  const requiredFieldsReady = Boolean(host && catalog && schema && user);

  const setAuthModeWithSettings = useCallback(async (nextAuthMode: TrinoAuthMode) => {
    if (nextAuthMode !== 'none') {
      setAuthMode(nextAuthMode);
      return;
    }

    const previousMode = authMode;
    setAuthMode('none');
    try {
      await postJson('/api/secrets/set', {
        providerId: 'trino',
        authType: 'none',
      });
      setBasicSecretSet(false);
      setJwtSecretSet(false);
      await refreshStatus();
    } catch (e: any) {
      setAuthMode(previousMode);
      toast.error(e?.message || 'Failed to set Trino auth mode to none');
    }
  }, [authMode, postJson, refreshStatus]);

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
            color: TRINO_COLOR,
            backgroundColor: `${TRINO_COLOR}14`,
            borderColor: `${TRINO_COLOR}30`,
          }}
        >
          <Database size={16} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium">Trino</div>
          <div className="text-[11px] text-muted-foreground truncate">
            {host ? host.replace(/^https?:\/\//, '') : 'Not configured'}
            {catalog ? ` · ${catalog}` : ''}
            {schema ? `.${schema}` : ''}
          </div>
          <div className={`text-[10px] mt-1 ${dsState.lastCheckOk ? 'text-emerald-400' : dsState.lastCheckOk === false ? 'text-destructive' : 'text-muted-foreground'}`}>
            {trinoStatusText}
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
              {trinoEnabled ? (
                <span className="text-emerald-400">Enabled</span>
              ) : (
                <span className="text-muted-foreground/80">Disabled</span>
              )}
            </div>
            {authMode === 'none' ? (
              <span className="text-[9px] font-medium text-muted-foreground/70 bg-muted/40 px-1.5 py-0.5 rounded">
                No auth
              </span>
            ) : hasAnyCredential ? (
              <span className="text-[9px] font-semibold text-emerald-500 bg-emerald-500/10 px-1.5 py-0.5 rounded">
                {authMode === 'basic' ? 'Basic Secret' : 'JWT Secret'}
              </span>
            ) : (
              <span className="text-[9px] font-medium text-muted-foreground/60 bg-muted/40 px-1.5 py-0.5 rounded">
                Secret missing
              </span>
            )}
          </div>
        </div>
      </button>
    );
  }

  return (
    <div className="rounded-lg border border-border bg-card/30 overflow-hidden">
      <button
        type="button"
        className="w-full flex items-center gap-3 p-3 hover:bg-card/60 transition-colors text-left border-b border-border"
        onClick={() => setExpanded(false)}
      >
        <div
          className="w-8 h-8 rounded-md border flex items-center justify-center text-sm font-bold shrink-0"
          style={{
            color: TRINO_COLOR,
            backgroundColor: `${TRINO_COLOR}14`,
            borderColor: `${TRINO_COLOR}30`,
          }}
        >
          <Database size={16} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium">Trino</div>
          <div className="text-[11px] text-muted-foreground">Coordinator connection</div>
        </div>
        {statusLoading && <RefreshCw size={13} className="text-muted-foreground animate-spin shrink-0" />}
      </button>

      <div className="p-3 space-y-4">
        <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2">
          <div className="flex items-start justify-between gap-2">
            <div>
              <div className="text-xs font-medium">Enable Trino SQL Tools</div>
              <div className="text-[10px] text-muted-foreground">
                {trinoEnabled
                  ? trinoValidated
                    ? 'Enabled and validated'
                    : 'Enabled, but validation failed. Re-run test or disable.'
                  : trinoValidated
                    ? 'Validation passed. Enable to expose Trino tools to the assistant.'
                    : 'Validation required before enabling Trino SQL tools.'}
              </div>
            </div>
            <Switch
              checked={trinoEnabled}
              onCheckedChange={handleTrinoToggle}
              disabled={!trinoValidated && !trinoEnabled}
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

        <div className="space-y-3">
          <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
            <Database size={12} /> Connection
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs">Host URL</Label>
            <Input
              placeholder="https://trino.example.com"
              value={localSettings.trinoHost || ''}
              onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoHost: e.target.value }))}
              className="font-mono"
            />
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label className="text-xs">Catalog</Label>
              <Input
                placeholder="hive"
                value={localSettings.trinoCatalog || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoCatalog: e.target.value }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">Schema</Label>
              <Input
                placeholder="default"
                value={localSettings.trinoSchema || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoSchema: e.target.value }))}
                className="font-mono"
              />
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label className="text-xs">User</Label>
              <Input
                placeholder="analyst"
                value={localSettings.trinoUser || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoUser: e.target.value }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">Source</Label>
              <Input
                placeholder="ai-analyst"
                value={localSettings.trinoSource || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoSource: e.target.value }))}
                className="font-mono"
              />
            </div>
          </div>
        </div>

        <div className="space-y-3">
          <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
            <Shield size={12} /> Authentication
          </div>

          <div className="grid grid-cols-1 md:grid-cols-3 gap-2">
            {AUTH_OPTIONS.map((opt) => {
              const selected = authMode === opt.id;
              const isSet = opt.id === 'basic' ? basicSecretSet : opt.id === 'jwt' ? jwtSecretSet : true;
              return (
                <button
                  key={opt.id}
                  type="button"
                  className="relative flex flex-col items-start p-2.5 rounded-lg border text-left transition-all hover:bg-card/60"
                  style={{
                    borderColor: selected ? TRINO_COLOR : 'hsl(var(--border))',
                    boxShadow: selected ? `0 0 0 1px ${TRINO_COLOR}40` : undefined,
                    backgroundColor: selected ? `${TRINO_COLOR}08` : undefined,
                  }}
                  onClick={() => { void setAuthModeWithSettings(opt.id); }}
                >
                  <div className="text-xs font-medium">{opt.label}</div>
                  <div className="text-[10px] text-muted-foreground leading-snug mt-1">{opt.desc}</div>
                  {isSet && opt.id !== 'none' && (
                    <div className="absolute top-2 right-2">
                      <CheckCircle2 size={12} className="text-emerald-500" />
                    </div>
                  )}
                </button>
              );
            })}
          </div>

          <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2">
            {authMode === 'basic' ? (
              <>
                <SecretField
                  label="Basic Password"
                  placeholder="Enter Trino password"
                  isSet={basicSecretSet}
                  onSet={handleSetBasicSecret}
                  onClear={handleClearBasicSecret}
                />
                <p className="text-[10px] text-muted-foreground">
                  Username comes from the <span className="font-mono">User</span> field above.
                </p>
              </>
            ) : authMode === 'jwt' ? (
              <>
                <SecretField
                  label="JWT Token"
                  placeholder="eyJ..."
                  isSet={jwtSecretSet}
                  onSet={handleSetJwtSecret}
                  onClear={handleClearJwtSecret}
                />
                <p className="text-[10px] text-muted-foreground">
                  Token is stored in secure local secrets storage.
                </p>
              </>
            ) : (
              <p className="text-[11px] text-muted-foreground">
                No auth secret is required for this mode.
              </p>
            )}
          </div>
        </div>

        <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2.5">
          <div className="flex items-center justify-between gap-3">
            <div className="text-xs font-medium">Test Connection</div>
            <Button
              variant="outline"
              size="sm"
              className="gap-1.5"
              onClick={() => void handleTestConnection()}
              disabled={testLoading || !requiredFieldsReady || !activeCredentialSet}
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

          {!requiredFieldsReady && (
            <p className="text-[10px] text-muted-foreground">
              Enter host, catalog, schema, and user before testing.
            </p>
          )}
          {(authMode === 'basic' && !basicSecretSet) && (
            <p className="text-[10px] text-muted-foreground">
              Set the Basic password secret to run the test.
            </p>
          )}
          {(authMode === 'jwt' && !jwtSecretSet) && (
            <p className="text-[10px] text-muted-foreground">
              Set the JWT token secret to run the test.
            </p>
          )}

          {status && (
            <div className="text-[10px] text-muted-foreground">
              Backend auth mode: <span className="font-mono">{status.authMode}</span>
            </div>
          )}

          {testError && (
            <div className="flex items-start gap-1.5 text-xs text-destructive">
              <XCircle size={13} className="mt-0.5 shrink-0" />
              <span>{testError}</span>
            </div>
          )}

          {testResult && !testError && (
            <div className={`flex items-start gap-1.5 text-xs ${testResult.ok ? 'text-emerald-400' : 'text-destructive'}`}>
              {testResult.ok ? (
                <CheckCircle2 size={13} className="mt-0.5 shrink-0" />
              ) : (
                <XCircle size={13} className="mt-0.5 shrink-0" />
              )}
              <div>
                <div>{testResult.ok ? 'Connected' : 'Failed'}</div>
                {testResult.message && <div className="text-[10px] mt-0.5 opacity-80">{testResult.message}</div>}
                {testResult.queryId && (
                  <div className="text-[10px] mt-0.5 text-muted-foreground">
                    Query ID: <span className="font-mono">{testResult.queryId}</span>
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};

export default TrinoSection;
