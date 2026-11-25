import React, { useState, useCallback, useEffect, useMemo } from 'react';
import {
  Plus,
  Check,
  ArrowLeft,
  Trash2,
  Cloud,
  Shield,
  RefreshCw,
  Pencil,
  FlaskConical,
  Database,
  CheckCircle2,
  XCircle,
} from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import type { AppSettings, AwsCloudCredential, GcpCloudCredential, DataSourceState } from '@/types';
import SecretField from './SecretField';
import DatabricksSection from './DatabricksSection';
import TrinoSection from './TrinoSection';
import SnowflakeSection from './SnowflakeSection';
import { getSecretsStatus } from '@/services/secretsApi';
import {
  getBigQueryAdcStatus,
  testBigQueryConnection,
  type NormalizedAdcStatus,
  type BigQueryTestResult,
} from '@/services/bigqueryApi';

interface CloudCredentialsTabProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  savedSettings: AppSettings;
  onSave: (settings: AppSettings) => void | Promise<void>;
}

type CloudPlatform = 'aws' | 'gcp';

type CredentialTestResult = {
  ok: boolean;
  message: string;
  timestamp: number;
};

type AnyCloudCredential =
  | (AwsCloudCredential & { platform: 'aws' })
  | (GcpCloudCredential & { platform: 'gcp' });

const PLATFORMS: Array<{ id: CloudPlatform; name: string; icon: string; color: string; desc: string }> = [
  { id: 'aws', name: 'Amazon Web Services', icon: '▲', color: '#FF9900', desc: 'Access key + secret key' },
  { id: 'gcp', name: 'Google Cloud Platform', icon: 'G', color: '#4285F4', desc: 'Project ID + ADC' },
];

const BIGQUERY_COLOR = '#F7B500';

const redactKey = (k: string) => {
  if (!k || k.length < 8) return '••••••••••';
  return k.slice(0, Math.min(7, k.length - 3)) + '••••••' + k.slice(-3);
};

const formatTimestamp = (ts: number | null | undefined): string => {
  if (!ts) return 'Never';
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return 'Never';
  }
};

const bigQueryDataSource = (state?: AppSettings['dataSources']): DataSourceState => {
  const raw = state?.bigquery;
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

const upsertBigQueryState = (
  current: AppSettings,
  patch: Partial<DataSourceState>,
): AppSettings => {
  const prev = bigQueryDataSource(current.dataSources);
  return {
    ...current,
    dataSources: {
      ...current.dataSources,
      bigquery: { ...prev, ...patch },
    },
  };
};

const CloudCredentialsTab: React.FC<CloudCredentialsTabProps> = ({
  localSettings,
  setLocalSettings,
  savedSettings,
  onSave,
}) => {
  const [wizardOpen, setWizardOpen] = useState(false);
  const [platform, setPlatform] = useState<CloudPlatform | null>(null);
  const [selectedPlatform, setSelectedPlatform] = useState<CloudPlatform>(
    localSettings.featureFlags?.cloudCredentialsAws === true ? 'aws' : 'gcp',
  );
  const [editingId, setEditingId] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, any>>({});
  const [secrets, setSecrets] = useState<Record<string, boolean>>({});
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [testingId, setTestingId] = useState<string | null>(null);

  const [adcLoading, setAdcLoading] = useState(false);
  const [adcStatus, setAdcStatus] = useState<NormalizedAdcStatus | null>(null);
  const [adcError, setAdcError] = useState<string | null>(null);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [setLoading, setSetLoading] = useState(false);
  const [setError, setSetError] = useState<string | null>(null);

  const [bqTesting, setBqTesting] = useState(false);
  const [bqTestResult, setBqTestResult] = useState<BigQueryTestResult | null>(null);
  const [bqTestError, setBqTestError] = useState<string | null>(null);

  const aws = localSettings.cloudCredentials?.aws || [];
  const gcp = localSettings.cloudCredentials?.gcp || [];
  const awsCredentialsEnabled = localSettings.featureFlags?.cloudCredentialsAws === true;
  const trinoEnabled = localSettings.featureFlags?.cloudCredentialsTrino === true;
  const snowflakeEnabled = localSettings.featureFlags?.cloudCredentialsSnowflake === true;
  const availablePlatforms = useMemo(
    () => PLATFORMS.filter((p) => p.id !== 'aws' || awsCredentialsEnabled),
    [awsCredentialsEnabled],
  );
  const defaultPlatform: CloudPlatform = availablePlatforms[0]?.id || 'gcp';
  const allCreds: AnyCloudCredential[] = [
    ...(awsCredentialsEnabled ? aws.map((c) => ({ ...c, platform: 'aws' as const })) : []),
    ...gcp.map((c) => ({ ...c, platform: 'gcp' as const })),
  ];

  const credsPath = (localSettings.googleCredentialsPath || '').trim();
  const savedCredsPath = (savedSettings.googleCredentialsPath || '').trim();
  const gcpProjectForTesting = String(fields.projectId || localSettings.googleProjectId || '').trim();
  const bqDataSource = bigQueryDataSource(localSettings.dataSources);
  const bqEnabled = bqDataSource.enabled === true;
  const bqValidated = bqDataSource.lastCheckOk === true;
  const bqStatusText = bqDataSource.lastCheckOk === true
    ? `Validated (${bqDataSource.lastProjectId || 'project not recorded'})`
    : bqDataSource.lastCheckOk === false
      ? `Validation failed (${bqDataSource.lastProjectId || 'project not recorded'})`
      : 'Not validated yet';
  const bqToggleDisabled = !bqValidated && !bqEnabled;

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

  const refreshAdc = useCallback(async () => {
    setAdcLoading(true);
    setAdcError(null);
    try {
      const st = await getBigQueryAdcStatus();
      setAdcStatus(st);
    } catch (e: any) {
      setAdcStatus(null);
      setAdcError(e?.message || 'Failed to check ADC status');
    } finally {
      setAdcLoading(false);
    }
  }, []);

  const persistBigQueryState = useCallback(async (patch: Partial<DataSourceState>) => {
    let next: AppSettings | null = null;
    setLocalSettings((prev) => {
      next = upsertBigQueryState(prev, patch);
      return next;
    });
    const saved = next || localSettings;
    await Promise.resolve(onSave(saved));
    return saved;
  }, [localSettings, onSave, setLocalSettings]);

  const runBigQueryTest = useCallback(async () => {
    const pid = gcpProjectForTesting;
    if (!pid) return;

    setBqTesting(true);
    setBqTestError(null);
    setBqTestResult(null);
    try {
      const res = await testBigQueryConnection(pid);
      await persistBigQueryState({
        enabled: res.ok,
        lastCheckedAt: Date.now(),
        lastCheckOk: res.ok,
        lastCheckMessage: res.ok
          ? `BigQuery connection verified for ${res.projectId || pid}.`
          : 'BigQuery connection test failed.',
        lastProjectId: pid,
      });
      setBqTestResult(res);
    } catch (e: any) {
      try {
        await persistBigQueryState({
          enabled: false,
          lastCheckedAt: Date.now(),
          lastCheckOk: false,
          lastCheckMessage: e?.message || 'BigQuery connection test failed.',
          lastProjectId: pid,
        });
      } catch (saveErr: any) {
        toast.error(saveErr?.message || 'Failed to save BigQuery datasource state');
      }
      setBqTestError(e?.message || 'BigQuery connection test failed');
      return;
    } finally {
      setBqTesting(false);
    }
  }, [gcpProjectForTesting, persistBigQueryState]);

  const handleBigQueryToggle = useCallback(async (enabled: boolean) => {
    if (enabled && !bqValidated) {
      toast.error('Run a successful BigQuery connection test before enabling SQL tools.');
      return;
    }
    try {
      await persistBigQueryState({ enabled });
    } catch (err: any) {
      toast.error(err?.message || 'Failed to save BigQuery datasource state');
    }
  }, [bqValidated, persistBigQueryState]);

  const applyCustomPath = useCallback(async () => {
    setSetLoading(true);
    setSetError(null);
    setBqTestError(null);
    setBqTestResult(null);

    try {
      const next: AppSettings = {
        ...savedSettings,
        googleCredentialsPath: credsPath || undefined,
      };
      await Promise.resolve(onSave(next));
      await refreshAdc();
    } catch (e: any) {
      setSetError(e?.message || 'Failed to set credentials path');
    } finally {
      setSetLoading(false);
    }
  }, [credsPath, onSave, refreshAdc, savedSettings]);

  useEffect(() => {
    if (!availablePlatforms.some((p) => p.id === selectedPlatform)) {
      setSelectedPlatform(defaultPlatform);
    }
  }, [availablePlatforms, defaultPlatform, selectedPlatform]);

  const resetWizard = useCallback(() => {
    setWizardOpen(false);
    setPlatform(null);
    setSelectedPlatform(defaultPlatform);
    setEditingId(null);
    setFields({});
    setSecrets({});
    setShowAdvanced(false);
    setSetError(null);
    setBqTestError(null);
    setBqTestResult(null);
  }, [defaultPlatform]);

  const openCreate = useCallback(() => {
    resetWizard();
    setWizardOpen(true);
    setPlatform(null);
    setSelectedPlatform(defaultPlatform);
    void refreshAdc();
  }, [defaultPlatform, refreshAdc, resetWizard]);

  const openEdit = useCallback((cred: AnyCloudCredential) => {
    resetWizard();
    setWizardOpen(true);
    setPlatform(cred.platform);
    setSelectedPlatform(cred.platform);
    setEditingId(cred.id);
    setFields({
      name: cred.displayName || '',
      region: cred.platform === 'aws' ? cred.region : '',
      projectId: cred.platform === 'gcp' ? cred.projectId || localSettings.googleProjectId || '' : '',
      accessKeyId: cred.platform === 'aws' ? cred.accessKeyId || '' : '',
    });
    setSecrets({
      accessKeyId: cred.platform === 'aws' ? Boolean(cred.accessKeyId) : false,
      secretAccessKey: cred.platform === 'aws' ? Boolean(cred.secretAccessKeySet) : false,
      sessionToken: cred.platform === 'aws' ? Boolean(cred.sessionTokenSet) : false,
    });
    if (cred.platform === 'gcp') {
      void refreshAdc();
    }
  }, [localSettings.googleProjectId, refreshAdc, resetWizard]);

  const runCredentialTest = useCallback(async (cred: AnyCloudCredential): Promise<CredentialTestResult> => {
    const timestamp = Date.now();

    if (cred.platform === 'aws') {
      if (!String(cred.region || '').trim()) {
        return { ok: false, message: 'AWS region is required.', timestamp };
      }

      let status: Awaited<ReturnType<typeof getSecretsStatus>>;
      try {
        status = await getSecretsStatus();
      } catch (e: any) {
        return { ok: false, message: e?.message || 'Could not verify secure credentials.', timestamp };
      }

      if (status.initialized === false || status.locked === true) {
        return { ok: false, message: 'Secrets store is locked or not initialized.', timestamp };
      }

      const hasAws = Boolean(status.present.bedrock_aws_credentials || status.present.bedrock_credentials || status.present['provider:bedrock']);
      if (!hasAws) {
        return { ok: false, message: 'AWS credentials are not set in secure storage.', timestamp };
      }

      return { ok: true, message: 'AWS credentials and secure storage checks passed.', timestamp };
    }

    try {
      const adc = await getBigQueryAdcStatus();
      const adcOk = adc.detected === true || adc.credentialsReadable === true;
      if (!adcOk) {
        return {
          ok: false,
          message: adc.error || 'ADC credentials were not detected for GCP.',
          timestamp,
        };
      }

      const pid = String(cred.projectId || localSettings.googleProjectId || '').trim();
      if (pid) {
        const test = await testBigQueryConnection(pid);
        if (test.ok) {
          return {
            ok: true,
            message: `ADC + BigQuery test passed for ${test.projectId || pid}.`,
            timestamp,
          };
        }
      }

      return {
        ok: true,
        message: `ADC detected${adc.credentialsType ? ` (${adc.credentialsType})` : ''}.`,
        timestamp,
      };
    } catch (e: any) {
      return { ok: false, message: e?.message || 'Failed to verify GCP credentials.', timestamp };
    }
  }, [localSettings.googleProjectId]);

  const handleTestCredential = useCallback(async (cred: AnyCloudCredential) => {
    setTestingId(cred.id);
    try {
      const result = await runCredentialTest(cred);
      setLocalSettings((prev) => ({
        ...prev,
        cloudCredentials: {
          aws: (prev.cloudCredentials?.aws || []).map((entry) => entry.id === cred.id
            ? {
              ...entry,
              lastTestedAt: result.timestamp,
              lastTestStatus: result.ok ? 'success' : 'failure',
              lastTestMessage: result.message,
            }
            : entry),
          gcp: (prev.cloudCredentials?.gcp || []).map((entry) => entry.id === cred.id
            ? {
              ...entry,
              lastTestedAt: result.timestamp,
              lastTestStatus: result.ok ? 'success' : 'failure',
              lastTestMessage: result.message,
            }
            : entry),
        },
      }));

      if (result.ok) toast.success(result.message);
      else toast.error(result.message);
    } finally {
      setTestingId(null);
    }
  }, [runCredentialTest, setLocalSettings]);

  const handleSave = useCallback(async () => {
    if (!platform) return;

    const id = editingId || crypto.randomUUID();

    if (platform === 'aws') {
      const existing = aws.find((c) => c.id === id);
      const accessKeyId = String(fields.accessKeyId || '').trim();
      const secretAccessKeyVal = String(fields.secretAccessKeyVal || '').trim();
      const sessionTokenVal = String(fields.sessionTokenVal || '').trim();
      const accessKeyIdVal = String(fields.accessKeyIdVal || '').trim();

      const needsSecretWrite = Boolean(accessKeyIdVal || secretAccessKeyVal || sessionTokenVal);
      if (needsSecretWrite) {
        if (!accessKeyIdVal || !secretAccessKeyVal) {
          toast.error('Both access key ID and secret access key are required when updating secure credentials.');
          return;
        }
        try {
          await postJson('/api/secrets/set', {
            providerId: 'bedrock',
            authType: 'aws_credentials',
            accessKeyId: accessKeyIdVal,
            secretAccessKey: secretAccessKeyVal,
            sessionToken: sessionTokenVal,
          });
        } catch (e: any) {
          toast.error(e?.message || 'Failed to store AWS credentials');
          return;
        }
      }

      const next: AwsCloudCredential = {
        id,
        displayName: String(fields.name || '').trim() || 'AWS Credentials',
        region: String(fields.region || '').trim() || 'us-east-1',
        authMode: 'access_key',
        accessKeyId: accessKeyId || existing?.accessKeyId || null,
        secretAccessKeySet: needsSecretWrite ? true : (existing?.secretAccessKeySet || false),
        sessionTokenSet: needsSecretWrite ? Boolean(sessionTokenVal) : (existing?.sessionTokenSet || false),
        lastTestedAt: existing?.lastTestedAt || null,
        lastTestStatus: existing?.lastTestStatus || null,
        lastTestMessage: existing?.lastTestMessage || null,
      };

      setLocalSettings((prev) => {
        const list = prev.cloudCredentials?.aws || [];
        const idx = list.findIndex((c) => c.id === id);
        const updated = [...list];
        if (idx >= 0) updated[idx] = next;
        else updated.push(next);
        return {
          ...prev,
          cloudCredentials: {
            aws: updated,
            gcp: prev.cloudCredentials?.gcp || [],
          },
        };
      });
    }

    if (platform === 'gcp') {
      const existing = gcp.find((c) => c.id === id);
      const pid = String(fields.projectId || '').trim() || null;

      setLocalSettings((prev) => ({
        ...prev,
        googleProjectId: pid || prev.googleProjectId || '',
      }));

      const next: GcpCloudCredential = {
        id,
        displayName: String(fields.name || '').trim() || 'GCP Credentials',
        projectId: pid,
        adcMode: 'auto',
        credentialsPath: null,
        lastTestedAt: existing?.lastTestedAt || null,
        lastTestStatus: existing?.lastTestStatus || null,
        lastTestMessage: existing?.lastTestMessage || null,
      };

      setLocalSettings((prev) => {
        const list = prev.cloudCredentials?.gcp || [];
        const idx = list.findIndex((c) => c.id === id);
        const updated = [...list];
        if (idx >= 0) updated[idx] = next;
        else updated.push(next);
        return {
          ...prev,
          cloudCredentials: {
            aws: prev.cloudCredentials?.aws || [],
            gcp: updated,
          },
        };
      });
    }

    toast.success(editingId ? 'Credential updated.' : 'Credential saved.');
    resetWizard();
  }, [aws, editingId, fields, gcp, platform, postJson, resetWizard, setLocalSettings]);

  const handleDelete = useCallback(async (id: string, type: CloudPlatform) => {
    if (type === 'aws') {
      const remainingAws = aws.filter((x) => x.id !== id);
      if (remainingAws.length === 0) {
        try {
          await postJson('/api/secrets/clear', { providerId: 'bedrock', authType: 'aws_credentials' });
        } catch (e: any) {
          toast.error(e?.message || 'Failed to clear AWS credentials from secure storage');
        }
      }
    }

    setLocalSettings((prev) => ({
      ...prev,
      cloudCredentials: {
        aws: type === 'aws' ? (prev.cloudCredentials?.aws || []).filter((x) => x.id !== id) : (prev.cloudCredentials?.aws || []),
        gcp: type === 'gcp' ? (prev.cloudCredentials?.gcp || []).filter((x) => x.id !== id) : (prev.cloudCredentials?.gcp || []),
      },
    }));

    setDeletingId(null);
    toast.success('Credential removed.');
  }, [aws, postJson, setLocalSettings]);

  if (!wizardOpen) {
    return (
      <div className="space-y-4">
        <div className="mb-4">
          <h3 className="text-sm font-semibold mb-1">Cloud Credentials</h3>
          <p className="text-xs text-muted-foreground">
            Manage reusable {awsCredentialsEnabled ? 'AWS and GCP' : 'GCP'} credentials used by providers
          </p>
        </div>

        {allCreds.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-12 text-center">
            <div className="w-10 h-10 rounded-lg bg-muted border border-border flex items-center justify-center text-muted-foreground mb-3">
              <Cloud size={18} />
            </div>
            <p className="text-sm font-medium text-muted-foreground mb-1">No cloud credentials configured</p>
            <p className="text-xs text-muted-foreground/70 mb-4">
              Add {awsCredentialsEnabled ? 'AWS or GCP' : 'GCP'} credentials for use with providers
            </p>
            <Button onClick={openCreate} className="gap-2">
              <Plus size={14} /> Add Cloud Credential
            </Button>
          </div>
        ) : (
          <>
            <div className="space-y-2">
              {allCreds.map((cc) => {
                const isAws = cc.platform === 'aws';
                const color = isAws ? '#FF9900' : '#4285F4';
                const icon = isAws ? '▲' : 'G';
                const meta = isAws
                  ? `${redactKey((cc as AwsCloudCredential).accessKeyId || '')}${(cc as AwsCloudCredential).region ? ` · ${(cc as AwsCloudCredential).region}` : ''}`
                  : `Project: ${(cc as GcpCloudCredential).projectId || '—'}`;
                const testing = testingId === cc.id;

                return (
                  <div key={cc.id} className="flex items-center justify-between p-3 rounded-lg border border-border bg-card/50 hover:bg-card/80 transition-colors gap-2 flex-wrap">
                    <div className="flex items-center gap-3 min-w-0 flex-1">
                      <div className="w-8 h-8 rounded-md border flex items-center justify-center text-sm font-bold shrink-0" style={{ color, backgroundColor: `${color}14`, borderColor: `${color}30` }}>
                        {icon}
                      </div>
                      <div className="min-w-0">
                        <div className="text-sm font-medium truncate">{cc.displayName}</div>
                        <div className="text-[11px] text-muted-foreground font-mono">{meta}</div>
                        <div className="text-[10px] text-muted-foreground/80">{formatTimestamp(cc.lastTestedAt)}</div>
                        {cc.lastTestStatus ? (
                          <div className={`text-[10px] ${cc.lastTestStatus === 'success' ? 'text-emerald-400' : 'text-destructive'}`}>
                            {cc.lastTestMessage || cc.lastTestStatus}
                          </div>
                        ) : null}
                      </div>
                    </div>
                    <div className="flex items-center gap-1">
                      <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground" onClick={() => openEdit(cc)}>
                        <Pencil size={13} />
                      </Button>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground"
                        onClick={() => void handleTestCredential(cc)}
                        disabled={testing}
                      >
                        {testing ? <RefreshCw size={13} className="animate-spin" /> : <FlaskConical size={13} />}
                      </Button>
                      {deletingId === cc.id ? (
                        <div className="flex items-center gap-1">
                          <Button size="sm" variant="destructive" className="h-6 px-2 text-[11px]" onClick={() => void handleDelete(cc.id, cc.platform)}>
                            Delete
                          </Button>
                          <Button size="sm" variant="outline" className="h-6 px-2 text-[11px]" onClick={() => setDeletingId(null)}>
                            Cancel
                          </Button>
                        </div>
                      ) : (
                        <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground" onClick={() => setDeletingId(cc.id)}>
                          <Trash2 size={13} />
                        </Button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>
            <button
              className="w-full flex items-center justify-center gap-2 py-2 px-4 rounded-lg border border-dashed border-border text-xs text-muted-foreground hover:text-foreground hover:border-foreground/20 transition-colors"
              onClick={openCreate}
            >
              <Plus size={14} /> Add Cloud Credential
            </button>
          </>
        )}

        {/* ── Data Sources ── */}
        <div className="pt-2 border-t border-border">
          <h3 className="text-sm font-semibold mb-1">Data Sources</h3>
          <p className="text-xs text-muted-foreground mb-3">Configure external data sources for SQL querying</p>

          <div className="rounded-lg border border-border bg-card/30 p-3 space-y-2">
            <div className="flex items-center justify-between gap-3">
              <div className="flex items-center gap-2 min-w-0">
                <div
                  className="w-7 h-7 rounded-md border flex items-center justify-center text-xs font-bold shrink-0"
                  style={{
                    color: BIGQUERY_COLOR,
                    backgroundColor: `${BIGQUERY_COLOR}14`,
                    borderColor: `${BIGQUERY_COLOR}30`,
                  }}
                >
                  BQ
                </div>
                <div className="min-w-0">
                  <div className="text-sm font-medium">BigQuery</div>
                  <div className="text-[11px] text-muted-foreground">
                    Google BigQuery SQL tool
                  </div>
                </div>
              </div>
              <Switch
                checked={bqEnabled}
                onCheckedChange={handleBigQueryToggle}
                disabled={bqToggleDisabled}
              />
            </div>
            <div className="text-xs">
              {bqDataSource.lastCheckedAt ? (
                <div className={`flex items-start gap-1.5 ${bqDataSource.lastCheckOk ? 'text-emerald-400' : 'text-destructive'}`}>
                  {bqDataSource.lastCheckOk ? <CheckCircle2 size={12} className="mt-0.5 shrink-0" /> : <XCircle size={12} className="mt-0.5 shrink-0" />}
                  <div>
                    <div>{bqDataSource.lastCheckMessage || bqStatusText}</div>
                    <div className="text-[10px] text-muted-foreground">Last checked {formatTimestamp(bqDataSource.lastCheckedAt)}</div>
                    {bqDataSource.lastProjectId && (
                      <div className="text-[10px] text-muted-foreground">Project: <span className="font-mono">{bqDataSource.lastProjectId}</span></div>
                    )}
                  </div>
                </div>
              ) : (
                <div className="text-[11px] text-muted-foreground">No validation check has been run yet.</div>
              )}
            </div>
          </div>

          <DatabricksSection
            localSettings={localSettings}
            setLocalSettings={setLocalSettings}
            savedSettings={savedSettings}
            onSave={onSave}
          />
          {trinoEnabled ? (
            <TrinoSection
              localSettings={localSettings}
              setLocalSettings={setLocalSettings}
              savedSettings={savedSettings}
              onSave={onSave}
            />
          ) : null}
          {snowflakeEnabled ? (
            <SnowflakeSection
              localSettings={localSettings}
              setLocalSettings={setLocalSettings}
              savedSettings={savedSettings}
              onSave={onSave}
            />
          ) : null}
        </div>
      </div>
    );
  }

  if (!platform) {
    return (
      <div className="space-y-4">
        <div>
          <h3 className="text-sm font-semibold mb-1">Choose cloud provider</h3>
          <p className="text-xs text-muted-foreground">Select which cloud credential type to configure</p>
        </div>

        <div className={`grid gap-2 ${availablePlatforms.length === 1 ? 'grid-cols-1' : 'grid-cols-2'}`}>
          {availablePlatforms.map((p) => {
            const selected = selectedPlatform === p.id;
            return (
              <button
                key={p.id}
                className="relative flex flex-col items-start p-3 rounded-lg border bg-card/50 text-left transition-all hover:bg-card/80"
                style={{
                  borderColor: selected ? p.color : 'hsl(var(--border))',
                  boxShadow: selected ? `0 0 0 1px ${p.color}40` : undefined,
                  backgroundColor: selected ? `${p.color}08` : undefined,
                }}
                onClick={() => setSelectedPlatform(p.id)}
              >
                <div
                  className="w-7 h-7 rounded-md border flex items-center justify-center text-xs font-bold mb-2"
                  style={{ color: p.color, backgroundColor: `${p.color}14`, borderColor: `${p.color}30` }}
                >
                  {p.icon}
                </div>
                <div className="text-xs font-medium mb-0.5">{p.name}</div>
                <div className="text-[10px] text-muted-foreground leading-snug">{p.desc}</div>
                {selected && (
                  <div
                    className="absolute top-1.5 right-1.5 w-4 h-4 rounded-full flex items-center justify-center text-white"
                    style={{ backgroundColor: p.color }}
                  >
                    <Check size={10} />
                  </div>
                )}
              </button>
            );
          })}
        </div>

        <div className="rounded-lg border border-border bg-card/40 p-3">
          {selectedPlatform === 'aws' ? (
            <div className="text-xs text-muted-foreground">Configure AWS access key credentials and region defaults.</div>
          ) : (
            <div className="text-xs text-muted-foreground">Configure GCP credentials with ADC verification and BigQuery testing.</div>
          )}
        </div>

        <div className="flex items-center justify-between pt-3 border-t border-border">
          <Button variant="outline" size="sm" onClick={resetWizard}>Cancel</Button>
          <Button size="sm" className="gap-1" onClick={() => { setPlatform(selectedPlatform); if (selectedPlatform === 'gcp') void refreshAdc(); }}>
            Continue
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div>
        <h3 className="text-sm font-semibold mb-1">{editingId ? 'Edit' : 'Configure'} {platform === 'aws' ? 'AWS' : 'GCP'}</h3>
        <p className="text-xs text-muted-foreground">These credentials will be available to providers that require them</p>
      </div>

      <div className="rounded-lg border border-border bg-card/30 p-3 space-y-3">
        <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
          <Shield size={12} /> {platform === 'aws' ? 'AWS' : 'GCP'} Authentication
        </div>

        <div className="space-y-1.5">
          <Label className="text-xs">Display Name</Label>
          <Input
            placeholder={platform === 'aws' ? 'e.g. Production AWS' : 'e.g. My GCP Project'}
            value={fields.name || ''}
            onChange={(e) => setFields((p) => ({ ...p, name: e.target.value }))}
          />
        </div>

        {platform === 'aws' ? (
          <>
            <SecretField
              label="Access Key ID"
              placeholder="AKIA..."
              isSet={secrets.accessKeyId || false}
              onSet={(v) => {
                setFields((p) => ({ ...p, accessKeyId: v, accessKeyIdVal: v }));
                setSecrets((p) => ({ ...p, accessKeyId: true }));
              }}
              onClear={() => {
                setSecrets((p) => ({ ...p, accessKeyId: false }));
                setFields((p) => ({ ...p, accessKeyId: '', accessKeyIdVal: '' }));
              }}
            />
            <SecretField
              label="Secret Access Key"
              placeholder="Enter secret key"
              isSet={secrets.secretAccessKey || false}
              onSet={(v) => {
                setFields((p) => ({ ...p, secretAccessKeyVal: v }));
                setSecrets((p) => ({ ...p, secretAccessKey: true }));
              }}
              onClear={() => {
                setSecrets((p) => ({ ...p, secretAccessKey: false }));
                setFields((p) => ({ ...p, secretAccessKeyVal: '' }));
              }}
            />
            <SecretField
              label="Session Token"
              placeholder="Optional"
              optional
              isSet={secrets.sessionToken || false}
              onSet={(v) => {
                setFields((p) => ({ ...p, sessionTokenVal: v }));
                setSecrets((p) => ({ ...p, sessionToken: true }));
              }}
              onClear={() => {
                setSecrets((p) => ({ ...p, sessionToken: false }));
                setFields((p) => ({ ...p, sessionTokenVal: '' }));
              }}
            />
            <div className="space-y-1.5">
              <Label className="text-xs">Default Region</Label>
              <Input
                placeholder="us-east-1"
                value={fields.region || ''}
                onChange={(e) => setFields((p) => ({ ...p, region: e.target.value }))}
                className="font-mono"
              />
            </div>
          </>
        ) : (
          <>
            <div className="space-y-1.5">
              <Label className="text-xs">Project ID</Label>
              <Input
                placeholder="my-gcp-project-id"
                value={fields.projectId || ''}
                onChange={(e) => {
                  const v = e.target.value;
                  setFields((p) => ({ ...p, projectId: v }));
                  setLocalSettings((prev) => ({ ...prev, googleProjectId: v }));
                }}
                className="font-mono"
              />
            </div>

            <div className="rounded-lg border border-border bg-card/20 p-3 space-y-2">
              <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
                <Database size={16} /> GCP ADC + BigQuery Verification
              </div>

              <div className="flex items-center justify-between gap-3">
                <div className="text-xs font-medium">Application Default Credentials (ADC)</div>
                <Button variant="outline" size="sm" onClick={refreshAdc} disabled={adcLoading}>
                  <RefreshCw size={12} className={`mr-1 ${adcLoading ? 'animate-spin' : ''}`} />
                  {adcLoading ? 'Checking...' : 'Refresh'}
                </Button>
              </div>

              {adcError ? (
                <div className="text-xs text-destructive">{adcError}</div>
              ) : adcStatus?.detected === true ? (
                <div className="text-xs flex items-center gap-2 text-emerald-400">
                  <CheckCircle2 size={14} /> ADC detected{adcStatus.credentialsType ? ` (${adcStatus.credentialsType})` : ''}
                </div>
              ) : (
                <div className="text-xs flex items-center gap-2 text-destructive">
                  <XCircle size={14} /> ADC not detected
                </div>
              )}

              <div className="text-[10px] text-muted-foreground">
                {adcStatus?.customPathConfigured === true ? 'Checked (custom): ' : 'Checked (auto): '}
                <span className="font-mono break-all">{adcStatus?.credentialsPathResolved || adcStatus?.credentialsPath || '—'}</span>
                {adcStatus?.credentialsPresent === false ? ' (not found)' : ''}
              </div>
              {adcStatus?.customPathConfigured === true && (
                <div className="text-[10px] text-muted-foreground">
                  Configured: <span className="font-mono break-all">{adcStatus?.credentialsPathConfigured || savedCredsPath || '—'}</span>
                </div>
              )}
              <div className="text-[10px] text-muted-foreground">
                Source: <span className="font-mono">{adcStatus?.credentialsSource || '—'}</span>
              </div>

              <button
                type="button"
                className="text-xs text-muted-foreground hover:text-foreground underline underline-offset-4"
                onClick={() => setShowAdvanced((v) => !v)}
              >
                {showAdvanced ? 'Hide advanced' : 'Advanced: Custom credentials path'}
              </button>

              {showAdvanced && (
                <div className="space-y-2 pt-1">
                  <Label className="text-xs mb-1.5 block">Custom credentials JSON path (optional)</Label>
                  <div className="flex items-center gap-2">
                    <Input
                      value={localSettings.googleCredentialsPath || ''}
                      onChange={(e) => setLocalSettings((prev) => ({ ...prev, googleCredentialsPath: e.target.value }))}
                      placeholder="~/.config/gcloud/application_default_credentials.json"
                      className="font-mono"
                    />
                    <Button type="button" variant="outline" onClick={() => void applyCustomPath()} disabled={setLoading || credsPath === savedCredsPath}>
                      {setLoading ? 'Setting...' : 'Set'}
                    </Button>
                    <Button
                      type="button"
                      variant="outline"
                      onClick={() => setLocalSettings((prev) => ({ ...prev, googleCredentialsPath: '' }))}
                      disabled={!credsPath}
                    >
                      Clear
                    </Button>
                  </div>
                  {setError && <div className="text-[10px] text-destructive">{setError}</div>}
                  <p className="text-[10px] text-muted-foreground">
                    Set applies immediately to server settings so ADC checks and BigQuery tests use this path now.
                  </p>
                </div>
              )}

              <div className="rounded-lg border border-border bg-card/10 p-3 space-y-2">
                <div className="flex items-center justify-between gap-3">
                  <div className="text-xs font-medium">Test BigQuery Connection</div>
                  <Button onClick={() => void runBigQueryTest()} disabled={bqTesting || setLoading || !gcpProjectForTesting}>
                    {bqTesting ? 'Testing...' : 'Test'}
                  </Button>
                </div>

                {!gcpProjectForTesting && (
                  <div className="text-xs text-muted-foreground">Enter a Project ID to run the connection test.</div>
                )}
                {bqTestError && <div className="text-xs text-destructive">{bqTestError}</div>}
                {bqTestResult?.ok === true && (
                  <div className="text-xs text-emerald-400 flex items-center gap-2">
                    <CheckCircle2 size={14} />
                    Connected to <span className="font-mono">{bqTestResult.projectId || gcpProjectForTesting}</span>
                    {typeof bqTestResult.datasetsVisible === 'number' ? ` (datasets visible: ${bqTestResult.datasetsVisible})` : ''}
                  </div>
                )}
              </div>
            </div>
          </>
        )}
      </div>

      <div className="flex items-center justify-between pt-3 border-t border-border">
        <Button variant="outline" size="sm" className="gap-1" onClick={() => setPlatform(null)}>
          <ArrowLeft size={14} /> Back
        </Button>
        <Button size="sm" onClick={() => void handleSave()} className="gap-1 bg-green-800 hover:bg-green-700 text-emerald-300">
          <Check size={14} /> {editingId ? 'Update Credential' : 'Save Credential'}
        </Button>
      </div>
    </div>
  );
};

export default CloudCredentialsTab;
