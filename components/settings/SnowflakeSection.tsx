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
import type { AppSettings, DataSourceState, SnowflakeAuthMode } from '@/types';
import SecretField from './SecretField';
import {
  clearSnowflakeSecrets,
  getSecretsStatus,
  setSnowflakeKeyPair,
  setSnowflakeOAuthToken,
} from '@/services/secretsApi';
import {
  getSnowflakeStatus,
  testSnowflakeConnection,
  type SnowflakeStatus,
  type SnowflakeTestResult,
} from '@/services/snowflakeApi';

interface SnowflakeSectionProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  savedSettings: AppSettings;
  onSave: (settings: AppSettings) => void | Promise<void>;
}

const SNOWFLAKE_COLOR = '#29B5E8';

const AUTH_OPTIONS: Array<{ id: SnowflakeAuthMode; label: string; desc: string }> = [
  {
    id: 'oauth_token',
    label: 'OAuth Token',
    desc: 'Use a Snowflake OAuth access token from secure local secrets storage.',
  },
  {
    id: 'keypair_jwt',
    label: 'Keypair JWT',
    desc: 'Use a Snowflake private key to sign JWT requests (optional passphrase).',
  },
];

const snowflakeDataSource = (state?: AppSettings['dataSources']): DataSourceState => {
  const raw = state?.snowflake;
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

const upsertSnowflakeState = (
  current: AppSettings,
  patch: Partial<DataSourceState>,
): AppSettings => {
  const prev = snowflakeDataSource(current.dataSources);
  return {
    ...current,
    dataSources: {
      ...current.dataSources,
      snowflake: { ...prev, ...patch },
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

const SnowflakeSection: React.FC<SnowflakeSectionProps> = ({
  localSettings,
  setLocalSettings,
  savedSettings,
  onSave,
}) => {
  const [expanded, setExpanded] = useState(false);
  const [authMode, setAuthMode] = useState<SnowflakeAuthMode>('oauth_token');
  const [oauthTokenSet, setOauthTokenSet] = useState(false);
  const [keyPairSet, setKeyPairSet] = useState(false);
  const [keyPairPassphrase, setKeyPairPassphrase] = useState('');
  const [statusLoading, setStatusLoading] = useState(false);
  const [status, setStatus] = useState<SnowflakeStatus | null>(null);
  const [testLoading, setTestLoading] = useState(false);
  const [testResult, setTestResult] = useState<SnowflakeTestResult | null>(null);
  const [testError, setTestError] = useState<string | null>(null);

  const accountIdentifier = (localSettings.snowflakeAccountIdentifier || '').trim();
  const warehouse = (localSettings.snowflakeWarehouse || '').trim();
  const database = (localSettings.snowflakeDatabase || '').trim();
  const schema = (localSettings.snowflakeSchema || '').trim();
  const role = (localSettings.snowflakeRole || '').trim();
  const user = (localSettings.snowflakeUser || '').trim();

  const refreshStatus = useCallback(async () => {
    setStatusLoading(true);
    let backendAuthMode: SnowflakeAuthMode | null = null;
    try {
      const st = await getSnowflakeStatus();
      setStatus(st);
      backendAuthMode = st.authMode;
      if (st.authMode !== 'none') {
        setAuthMode(st.authMode);
      }
    } catch {
      setStatus(null);
    }

    try {
      const secrets = await getSecretsStatus();
      const hasOAuth = Boolean(
        secrets.present.snowflake_oauth_token
        || secrets.present.snowflakeOAuthToken
        || secrets.present['provider:snowflake:oauth_token'],
      );
      const hasKeyPair = Boolean(
        secrets.present.snowflake_keypair
        || secrets.present.snowflake_key_pair
        || secrets.present.snowflakeKeyPair
        || secrets.present['provider:snowflake:keypair_jwt'],
      );
      const hasGeneric = Boolean(
        secrets.present.snowflake_credentials
        || secrets.present['provider:snowflake'],
      );
      setOauthTokenSet(hasOAuth || (hasGeneric && backendAuthMode === 'oauth_token'));
      setKeyPairSet(hasKeyPair || (hasGeneric && backendAuthMode === 'keypair_jwt'));
    } catch {
      // ignore
    } finally {
      setStatusLoading(false);
    }
  }, []);

  useEffect(() => {
    if (expanded) void refreshStatus();
  }, [expanded, refreshStatus]);

  const ensureSnowflakeSettingsSaved = useCallback(async () => {
    const accountChanged = (localSettings.snowflakeAccountIdentifier || '') !== (savedSettings.snowflakeAccountIdentifier || '');
    const warehouseChanged = (localSettings.snowflakeWarehouse || '') !== (savedSettings.snowflakeWarehouse || '');
    const databaseChanged = (localSettings.snowflakeDatabase || '') !== (savedSettings.snowflakeDatabase || '');
    const schemaChanged = (localSettings.snowflakeSchema || '') !== (savedSettings.snowflakeSchema || '');
    const roleChanged = (localSettings.snowflakeRole || '') !== (savedSettings.snowflakeRole || '');
    const userChanged = (localSettings.snowflakeUser || '') !== (savedSettings.snowflakeUser || '');
    if (accountChanged || warehouseChanged || databaseChanged || schemaChanged || roleChanged || userChanged) {
      await Promise.resolve(onSave({
        ...savedSettings,
        snowflakeAccountIdentifier: localSettings.snowflakeAccountIdentifier,
        snowflakeWarehouse: localSettings.snowflakeWarehouse,
        snowflakeDatabase: localSettings.snowflakeDatabase,
        snowflakeSchema: localSettings.snowflakeSchema,
        snowflakeRole: localSettings.snowflakeRole,
        snowflakeUser: localSettings.snowflakeUser,
      }));
    }
  }, [
    localSettings.snowflakeAccountIdentifier,
    localSettings.snowflakeDatabase,
    localSettings.snowflakeRole,
    localSettings.snowflakeSchema,
    localSettings.snowflakeUser,
    localSettings.snowflakeWarehouse,
    onSave,
    savedSettings,
  ]);

  const persistSnowflakeState = useCallback(async (patch: Partial<DataSourceState>) => {
    let next: AppSettings | null = null;
    setLocalSettings((prev) => {
      next = upsertSnowflakeState(prev, patch);
      return next;
    });
    const saved = next || localSettings;
    await Promise.resolve(onSave(saved));
    return saved;
  }, [localSettings, onSave, setLocalSettings]);

  const handleSetOauthToken = useCallback(async (value: string) => {
    try {
      await setSnowflakeOAuthToken(value);
      setOauthTokenSet(true);
      setTestResult(null);
      toast.success('Snowflake OAuth token saved to secure storage.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Snowflake OAuth token');
    }
  }, []);

  const handleClearOauthToken = useCallback(async () => {
    try {
      await clearSnowflakeSecrets();
      setOauthTokenSet(false);
      setKeyPairSet(false);
      setTestResult(null);
      toast.success('Snowflake credentials cleared.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear Snowflake OAuth token');
    }
  }, []);

  const handleSetKeyPair = useCallback(async (value: string) => {
    try {
      await setSnowflakeKeyPair(value, keyPairPassphrase);
      setKeyPairSet(true);
      setTestResult(null);
      toast.success('Snowflake keypair secret saved to secure storage.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Snowflake keypair secret');
    }
  }, [keyPairPassphrase]);

  const handleClearKeyPair = useCallback(async () => {
    try {
      await clearSnowflakeSecrets();
      setKeyPairSet(false);
      setOauthTokenSet(false);
      setKeyPairPassphrase('');
      setTestResult(null);
      toast.success('Snowflake credentials cleared.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear Snowflake keypair secret');
    }
  }, []);

  const handleTestConnection = useCallback(async () => {
    setTestLoading(true);
    setTestError(null);
    setTestResult(null);
    try {
      await ensureSnowflakeSettingsSaved();
      const result = await testSnowflakeConnection({
        accountIdentifier,
        warehouse,
        database,
        schema,
        role,
        user,
        authMode,
      });
      setTestResult(result);
      await persistSnowflakeState({
        enabled: result.ok,
        lastCheckedAt: Date.now(),
        lastCheckOk: result.ok,
        lastCheckMessage: result.message || (result.ok ? 'Snowflake connection verified.' : null),
        lastHost: accountIdentifier || null,
        lastWarehouseId: warehouse || null,
        lastProjectId: database || null,
      });
      if (result.ok) toast.success(result.message || 'Snowflake connection successful.');
      else toast.error(result.message || 'Snowflake connection test failed.');
    } catch (e: any) {
      try {
        await persistSnowflakeState({
          enabled: false,
          lastCheckedAt: Date.now(),
          lastCheckOk: false,
          lastCheckMessage: e?.message || 'Snowflake connection test failed.',
          lastHost: accountIdentifier || null,
          lastWarehouseId: warehouse || null,
          lastProjectId: database || null,
        });
      } catch (saveErr: any) {
        toast.error(saveErr?.message || 'Failed to save Snowflake datasource state');
      }
      setTestError(e?.message || 'Connection test failed.');
      toast.error(e?.message || 'Connection test failed.');
      return;
    } finally {
      setTestLoading(false);
    }
  }, [accountIdentifier, authMode, database, ensureSnowflakeSettingsSaved, persistSnowflakeState, role, schema, user, warehouse]);

  const dsState = snowflakeDataSource(localSettings.dataSources);
  const snowflakeEnabled = dsState.enabled === true;
  const snowflakeValidated = dsState.lastCheckOk === true;
  const snowflakeStatusText = dsState.lastCheckOk === true
    ? 'Validation passed'
    : dsState.lastCheckOk === false
      ? 'Validation failed'
      : 'Not validated yet';

  const handleSnowflakeToggle = useCallback(async (enabled: boolean) => {
    if (enabled && !snowflakeValidated) {
      toast.error('Run a Snowflake connection test successfully before enabling SQL tools.');
      return;
    }
    try {
      await persistSnowflakeState({ enabled });
    } catch (err: any) {
      toast.error(err?.message || 'Failed to save Snowflake datasource state');
    }
  }, [persistSnowflakeState, snowflakeValidated]);

  const hasAnyCredential = oauthTokenSet || keyPairSet;
  const activeCredentialSet = authMode === 'oauth_token' ? oauthTokenSet : keyPairSet;
  const requiredFieldsReady = Boolean(accountIdentifier && warehouse && database && schema && user);

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
            color: SNOWFLAKE_COLOR,
            backgroundColor: `${SNOWFLAKE_COLOR}14`,
            borderColor: `${SNOWFLAKE_COLOR}30`,
          }}
        >
          <Database size={16} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium">Snowflake</div>
          <div className="text-[11px] text-muted-foreground truncate">
            {accountIdentifier || 'Not configured'}
            {database ? ` · ${database}` : ''}
            {schema ? `.${schema}` : ''}
          </div>
          <div className={`text-[10px] mt-1 ${dsState.lastCheckOk ? 'text-emerald-400' : dsState.lastCheckOk === false ? 'text-destructive' : 'text-muted-foreground'}`}>
            {snowflakeStatusText}
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
              {snowflakeEnabled ? (
                <span className="text-emerald-400">Enabled</span>
              ) : (
                <span className="text-muted-foreground/80">Disabled</span>
              )}
            </div>
            {hasAnyCredential ? (
              <span className="text-[9px] font-semibold text-emerald-500 bg-emerald-500/10 px-1.5 py-0.5 rounded">
                {authMode === 'oauth_token' ? 'OAuth Token' : 'Keypair JWT'}
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
            color: SNOWFLAKE_COLOR,
            backgroundColor: `${SNOWFLAKE_COLOR}14`,
            borderColor: `${SNOWFLAKE_COLOR}30`,
          }}
        >
          <Database size={16} />
        </div>
        <div className="flex-1 min-w-0">
          <div className="text-sm font-medium">Snowflake</div>
          <div className="text-[11px] text-muted-foreground">SQL API connection</div>
        </div>
        {statusLoading && <RefreshCw size={13} className="text-muted-foreground animate-spin shrink-0" />}
      </button>

      <div className="p-3 space-y-4">
        <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2">
          <div className="flex items-start justify-between gap-2">
            <div>
              <div className="text-xs font-medium">Enable Snowflake SQL Tools</div>
              <div className="text-[10px] text-muted-foreground">
                {snowflakeEnabled
                  ? snowflakeValidated
                    ? 'Enabled and validated'
                    : 'Enabled, but validation failed. Re-run test or disable.'
                  : snowflakeValidated
                    ? 'Validation passed. Enable to expose Snowflake tools to the assistant.'
                    : 'Validation required before enabling Snowflake SQL tools.'}
              </div>
            </div>
            <Switch
              checked={snowflakeEnabled}
              onCheckedChange={handleSnowflakeToggle}
              disabled={!snowflakeValidated && !snowflakeEnabled}
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

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label className="text-xs">Account</Label>
              <Input
                placeholder="xy12345.us-east-1"
                value={localSettings.snowflakeAccountIdentifier || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeAccountIdentifier: e.target.value }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">Warehouse</Label>
              <Input
                placeholder="ANALYTICS_WH"
                value={localSettings.snowflakeWarehouse || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeWarehouse: e.target.value }))}
                className="font-mono"
              />
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label className="text-xs">Database</Label>
              <Input
                placeholder="ANALYTICS"
                value={localSettings.snowflakeDatabase || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeDatabase: e.target.value }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">Schema</Label>
              <Input
                placeholder="PUBLIC"
                value={localSettings.snowflakeSchema || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeSchema: e.target.value }))}
                className="font-mono"
              />
            </div>
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label className="text-xs">Role (optional)</Label>
              <Input
                placeholder="ANALYST_ROLE"
                value={localSettings.snowflakeRole || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeRole: e.target.value }))}
                className="font-mono"
              />
            </div>
            <div className="space-y-1.5">
              <Label className="text-xs">User</Label>
              <Input
                placeholder="analyst_user"
                value={localSettings.snowflakeUser || ''}
                onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeUser: e.target.value }))}
                className="font-mono"
              />
            </div>
          </div>
        </div>

        <div className="space-y-3">
          <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
            <Shield size={12} /> Authentication
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-2">
            {AUTH_OPTIONS.map((opt) => {
              const selected = authMode === opt.id;
              const isSet = opt.id === 'oauth_token' ? oauthTokenSet : keyPairSet;
              return (
                <button
                  key={opt.id}
                  type="button"
                  className="relative flex flex-col items-start p-2.5 rounded-lg border text-left transition-all hover:bg-card/60"
                  style={{
                    borderColor: selected ? SNOWFLAKE_COLOR : 'hsl(var(--border))',
                    boxShadow: selected ? `0 0 0 1px ${SNOWFLAKE_COLOR}40` : undefined,
                    backgroundColor: selected ? `${SNOWFLAKE_COLOR}08` : undefined,
                  }}
                  onClick={() => {
                    setAuthMode(opt.id);
                    setTestResult(null);
                    setTestError(null);
                  }}
                >
                  <div className="text-xs font-medium">{opt.label}</div>
                  <div className="text-[10px] text-muted-foreground leading-snug mt-1">{opt.desc}</div>
                  {isSet && (
                    <div className="absolute top-2 right-2">
                      <CheckCircle2 size={12} className="text-emerald-500" />
                    </div>
                  )}
                </button>
              );
            })}
          </div>

          <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2">
            {authMode === 'oauth_token' ? (
              <>
                <SecretField
                  label="OAuth Token"
                  placeholder="eyJ..."
                  isSet={oauthTokenSet}
                  onSet={handleSetOauthToken}
                  onClear={handleClearOauthToken}
                />
                <p className="text-[10px] text-muted-foreground">
                  Token is stored in secure local secrets storage.
                </p>
              </>
            ) : (
              <>
                <SecretField
                  label="Private Key PEM"
                  placeholder="-----BEGIN PRIVATE KEY-----"
                  isSet={keyPairSet}
                  onSet={handleSetKeyPair}
                  onClear={handleClearKeyPair}
                />
                <div className="space-y-1.5">
                  <Label className="text-xs">Passphrase (optional)</Label>
                  <Input
                    type="password"
                    placeholder="Key passphrase"
                    value={keyPairPassphrase}
                    onChange={(e) => setKeyPairPassphrase(e.target.value)}
                    className="font-mono"
                  />
                </div>
                <p className="text-[10px] text-muted-foreground">
                  Save the private key after updating passphrase so both are persisted together.
                </p>
              </>
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
              Enter account, warehouse, database, schema, and user before testing.
            </p>
          )}
          {(authMode === 'oauth_token' && !oauthTokenSet) && (
            <p className="text-[10px] text-muted-foreground">
              Set the OAuth token secret to run the test.
            </p>
          )}
          {(authMode === 'keypair_jwt' && !keyPairSet) && (
            <p className="text-[10px] text-muted-foreground">
              Set the private key secret to run the test.
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

export default SnowflakeSection;
