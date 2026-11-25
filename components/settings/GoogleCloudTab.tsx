import React from 'react';
import {
  CheckCircle2, Database, RefreshCcw, XCircle,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { AppSettings } from '@/types';
import {
  getBigQueryAdcStatus,
  testBigQueryConnection,
  type NormalizedAdcStatus,
  type BigQueryTestResult,
} from '@/services/bigqueryApi';

interface GoogleCloudTabProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  savedSettings: AppSettings;
  onSave: (settings: AppSettings) => void | Promise<void>;
}

const GoogleCloudTab: React.FC<GoogleCloudTabProps> = ({
  localSettings,
  setLocalSettings,
  savedSettings,
  onSave,
}) => {
  const [adcLoading, setAdcLoading] = React.useState(false);
  const [adcStatus, setAdcStatus] = React.useState<NormalizedAdcStatus | null>(null);
  const [adcError, setAdcError] = React.useState<string | null>(null);

  const [testLoading, setTestLoading] = React.useState(false);
  const [testResult, setTestResult] = React.useState<BigQueryTestResult | null>(null);
  const [testError, setTestError] = React.useState<string | null>(null);

  const [setLoading, setSetLoading] = React.useState(false);
  const [setError, setSetError] = React.useState<string | null>(null);

  const [showAdvanced, setShowAdvanced] = React.useState(false);

  const refreshAdc = React.useCallback(async () => {
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

  React.useEffect(() => {
    // Fetch once when the tab mounts so users immediately see whether ADC is available.
    void refreshAdc();
  }, [refreshAdc]);

  const projectId = (localSettings.googleProjectId || '').trim();
  const credsPath = (localSettings.googleCredentialsPath || '').trim();
  const savedCredsPath = (savedSettings.googleCredentialsPath || '').trim();

  const applyCustomPath = async () => {
    setSetLoading(true);
    setSetError(null);
    setTestError(null);
    setTestResult(null);
    try {
      const next: AppSettings = { ...savedSettings, googleCredentialsPath: credsPath || undefined };
      await Promise.resolve(onSave(next));
      await refreshAdc();
      if (projectId) {
        setTestLoading(true);
        try {
          const res = await testBigQueryConnection(projectId);
          setTestResult(res);
        } catch (e: any) {
          setTestError(e?.message || 'Connection test failed');
        } finally {
          setTestLoading(false);
        }
      }
    } catch (e: any) {
      setSetError(e?.message || 'Failed to set credentials path');
    } finally {
      setSetLoading(false);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
        <Database size={16} /> Google Cloud Tools
      </div>

      <div>
        <Label className="text-xs mb-1.5 block">Project ID</Label>
        <Input
          value={localSettings.googleProjectId || ''}
          onChange={(e) => setLocalSettings(prev => ({ ...prev, googleProjectId: e.target.value }))}
          placeholder="my-gcp-project-id"
          className="font-mono"
        />
        <p className="text-[10px] text-muted-foreground mt-1">
          BigQuery calls run through the local Go server and use Application Default Credentials (ADC) by default.
        </p>
      </div>

      <div className="bg-card/30 border border-border rounded-xl p-4 space-y-3">
        <div className="flex items-center justify-between gap-3">
          <div className="text-sm font-medium">Application Default Credentials (ADC)</div>
          <Button variant="outline" size="sm" onClick={refreshAdc} disabled={adcLoading}>
            <RefreshCcw size={14} className="mr-2" />
            {adcLoading ? 'Checking…' : 'Refresh'}
          </Button>
        </div>

        {adcLoading && (
          <div className="text-xs text-muted-foreground">Checking for ADC credentials…</div>
        )}

        {!adcLoading && (
          <div className="space-y-2">
            {adcError ? (
              <div className="text-xs text-destructive">{adcError}</div>
            ) : adcStatus?.detected === true ? (
              <div className="text-xs flex items-center gap-2 text-accent">
                <CheckCircle2 size={14} /> ADC detected{adcStatus?.credentialsType ? ` (${adcStatus.credentialsType})` : ''}
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
            {adcStatus?.error && (
              <div className="text-[10px] text-destructive font-mono break-all">{String(adcStatus.error)}</div>
            )}
          </div>
        )}

        <div className="flex items-center justify-between gap-3 pt-1">
          <button
            type="button"
            className="text-xs text-muted-foreground hover:text-foreground underline underline-offset-4"
            onClick={() => setShowAdvanced(v => !v)}
          >
            {showAdvanced ? 'Hide advanced' : 'Advanced: Custom credentials path'}
          </button>
        </div>

        {showAdvanced && (
          <div className="space-y-2 pt-1">
            <Label className="text-xs mb-1.5 block">Custom credentials JSON path (optional)</Label>
            <div className="flex items-center gap-2">
              <Input
                value={localSettings.googleCredentialsPath || ''}
                onChange={(e) => setLocalSettings(prev => ({ ...prev, googleCredentialsPath: e.target.value }))}
                placeholder="~/.config/gcloud/application_default_credentials.json"
                className="font-mono"
              />
              <Button
                type="button"
                variant="outline"
                onClick={applyCustomPath}
                disabled={setLoading || credsPath === savedCredsPath}
              >
                {setLoading ? 'Setting…' : 'Set'}
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => setLocalSettings(prev => ({ ...prev, googleCredentialsPath: '' }))}
                disabled={!credsPath}
              >
                Clear
              </Button>
            </div>
            {setError && (
              <div className="text-[10px] text-destructive">{setError}</div>
            )}
            <p className="text-[10px] text-muted-foreground">
              Click <span className="font-medium">Set</span> to apply the path immediately and run a connection test (if a Project ID is set).
              Default ADC locations: macOS/Linux <span className="font-mono">~/.config/gcloud/application_default_credentials.json</span>, Windows <span className="font-mono">%APPDATA%\\gcloud\\application_default_credentials.json</span>.
              In Docker, the path must exist inside the container.
            </p>
          </div>
        )}
      </div>

      <div className="bg-card/30 border border-border rounded-xl p-4 space-y-3">
        <div className="flex items-center justify-between gap-3">
          <div className="text-sm font-medium">Test BigQuery Connection</div>
          <Button
            onClick={async () => {
              setTestLoading(true);
              setTestError(null);
              setTestResult(null);
              try {
                const res = await testBigQueryConnection(projectId || undefined);
                setTestResult(res);
              } catch (e: any) {
                setTestError(e?.message || 'Connection test failed');
              } finally {
                setTestLoading(false);
              }
            }}
            disabled={testLoading || setLoading || !projectId}
          >
            {testLoading ? 'Testing…' : 'Test'}
          </Button>
        </div>

        {!projectId && (
          <div className="text-xs text-muted-foreground">
            Enter a Project ID to run the connection test.
          </div>
        )}

        {testError && (
          <div className="text-xs text-destructive">{testError}</div>
        )}

        {testResult?.ok === true && (
          <div className="text-xs text-accent flex items-center gap-2">
            <CheckCircle2 size={14} />
            Connected to <span className="font-mono">{testResult.projectId || projectId}</span>
            {typeof testResult.datasetsVisible === 'number' ? ` (datasets visible: ${testResult.datasetsVisible})` : ''}
          </div>
        )}
      </div>
    </div>
  );
};

export default GoogleCloudTab;
