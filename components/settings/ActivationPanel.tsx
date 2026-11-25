import React from 'react';
import { KeyRound } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import {
  clearActivation,
  getActivationStatus,
  installActivationKey,
  ActivationApiError,
  type NormalizedActivationStatus,
} from '@/services/activationApi';

type ActivationPanelProps = {
  active: boolean;
  onStatus?: (status: NormalizedActivationStatus | null) => void;
};

function formatStatus(status: NormalizedActivationStatus | null): {
  label: string;
  variant: 'default' | 'secondary' | 'destructive' | 'outline';
  detail: string;
} {
  if (!status || status.activated === null) {
    return { label: 'Unknown', variant: 'outline', detail: 'Status unavailable.' };
  }
  if (status.activated) {
    return {
      label: 'Activated',
      variant: 'secondary',
      detail: status.expiresAt ? `Valid until ${status.expiresAt}.` : 'Activation is active.',
    };
  }
  return { label: 'Not activated', variant: 'destructive', detail: 'This device is not activated.' };
}

const ActivationPanel: React.FC<ActivationPanelProps> = ({ active, onStatus }) => {
  const [status, setStatus] = React.useState<NormalizedActivationStatus | null>(null);
  const [isLoading, setIsLoading] = React.useState(false);
  const [isSubmitting, setIsSubmitting] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [activationKey, setActivationKey] = React.useState('');
  const [hasNotActivatedToast, setHasNotActivatedToast] = React.useState(false);

  const handleApiError = React.useCallback((err: unknown, fallback: string) => {
    if (err instanceof ActivationApiError && err.status === 402 && err.code === 'NOT_ACTIVATED') {
      if (!hasNotActivatedToast) {
        toast.error('Device is not activated.');
        setHasNotActivatedToast(true);
      }
      return;
    }
    const msg = (err as any)?.message || fallback;
    toast.error(msg);
  }, [hasNotActivatedToast]);

  const refresh = React.useCallback(async () => {
    if (!active) return;
    setIsLoading(true);
    setError(null);
    try {
      const next = await getActivationStatus();
      setStatus(next);
      onStatus?.(next);
    } catch (err: any) {
      handleApiError(err, 'Failed to fetch activation status');
      setError(err?.message || 'Failed to fetch activation status');
      onStatus?.(null);
    } finally {
      setIsLoading(false);
    }
  }, [active, handleApiError, onStatus]);

  React.useEffect(() => {
    if (!active) return;
    void refresh();
  }, [active, refresh]);

  const statusMeta = formatStatus(status);
  const backendError =
    status && status.raw && typeof (status.raw as any).error === 'string'
      ? String((status.raw as any).error).trim()
      : null;

  return (
    <div className="p-5 space-y-4">
      <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
        <KeyRound size={16} /> Activation
      </div>

      <div className="rounded-lg border bg-card p-4 space-y-4">
        <div className="flex items-start justify-between gap-3">
          <div>
            <div className="text-sm font-medium">Status</div>
            <div className="text-xs text-muted-foreground">{statusMeta.detail}</div>
            {backendError && status?.activated === false && (
              <div className="text-xs text-destructive mt-1">{backendError}</div>
            )}
          </div>
          <Badge variant={statusMeta.variant}>{statusMeta.label}</Badge>
        </div>

        <div className="space-y-2">
          <Label className="text-xs mb-1.5 block">Device ID</Label>
          <div className="flex items-center gap-2">
            <Input
              value={status?.deviceId || ''}
              placeholder={isLoading ? 'Loading…' : 'Unavailable'}
              readOnly
              className="font-mono"
            />
            <Button
              type="button"
              variant="outline"
              onClick={async () => {
                if (!status?.deviceId) return;
                try {
                  await navigator.clipboard.writeText(status.deviceId);
                  toast.success('Device ID copied');
                } catch (err: any) {
                  toast.error(err?.message || 'Failed to copy Device ID');
                }
              }}
              disabled={!status?.deviceId}
            >
              Copy
            </Button>
          </div>
          {!isLoading && !status?.deviceId && (
            <div className="space-y-2">
              <div className="text-xs text-muted-foreground">
                Device ID is unavailable. If you&apos;re running in Docker, make sure you are persisting the config directory (recommended: a Docker volume mounted at <span className="font-mono">/home/nonroot/.config/aianalyst</span>).
              </div>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={async () => {
                    const cmd =
                      'docker run --rm -p 8787:8787 -v ai_analyst_config:/home/nonroot/.config/aianalyst ai-analyst-local';
                    try {
                      await navigator.clipboard.writeText(cmd);
                      toast.success('Docker command copied');
                    } catch (err: any) {
                      toast.error(err?.message || 'Failed to copy docker command');
                    }
                  }}
                >
                  Copy docker run
                </Button>
              </div>
            </div>
          )}
          {!isLoading && status?.deviceId && (
            <div className="space-y-1 mt-1">
              {status.deviceIdSource && (
                <div className="text-[10px] text-muted-foreground">
                  Source: <span className="font-mono">{status.deviceIdSource}</span>
                </div>
              )}
              <div className="text-xs text-muted-foreground">
                Device ID is generated from a seed stored in the app config directory. In Docker, keep this directory persisted (recommended: a Docker volume mounted at <span className="font-mono">/home/nonroot/.config/aianalyst</span>) so activation stays stable across restarts.
              </div>
              <div className="flex items-center gap-2 mt-1">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={async () => {
                    const cmd =
                      'docker run --rm -p 8787:8787 -v ai_analyst_config:/home/nonroot/.config/aianalyst ai-analyst-local';
                    try {
                      await navigator.clipboard.writeText(cmd);
                      toast.success('Docker command copied');
                    } catch (err: any) {
                      toast.error(err?.message || 'Failed to copy docker command');
                    }
                  }}
                >
                  Copy docker run
                </Button>
              </div>
            </div>
          )}
        </div>

        <div className="space-y-2">
          <Label className="text-xs mb-1.5 block">Activation Key</Label>
          <div className="flex items-center gap-2">
            <Input
              value={activationKey}
              onChange={(e) => setActivationKey(e.target.value)}
              placeholder="Paste activation key"
              className="font-mono"
            />
            <Button
              type="button"
              variant="outline"
              onClick={async () => {
                try {
                  const text = await navigator.clipboard.readText();
                  if (!text.trim()) {
                    toast.error('Clipboard is empty');
                    return;
                  }
                  setActivationKey(text.trim());
                } catch (err: any) {
                  toast.error(err?.message || 'Failed to read clipboard');
                }
              }}
            >
              Paste
            </Button>
          </div>
        </div>

        {error && (
          <div className="text-sm text-destructive">{error}</div>
        )}

        <div className="flex items-center gap-2">
          <Button
            type="button"
            onClick={async () => {
              const key = activationKey.trim();
              if (!key) {
                toast.error('Activation key is required');
                return;
              }
              setIsSubmitting(true);
              try {
                await installActivationKey(key);
                toast.success('Activated');
                setActivationKey('');
                await refresh();
              } catch (err: any) {
                handleApiError(err, 'Failed to activate');
              } finally {
                setIsSubmitting(false);
              }
            }}
            disabled={isSubmitting}
          >
            {isSubmitting ? 'Activating…' : 'Activate'}
          </Button>
          <Button
            type="button"
            variant="outline"
            onClick={async () => {
              setIsSubmitting(true);
              try {
                await clearActivation();
                toast.success('Activation removed');
                await refresh();
              } catch (err: any) {
                handleApiError(err, 'Failed to remove activation');
              } finally {
                setIsSubmitting(false);
              }
            }}
            disabled={isSubmitting}
          >
            Remove
          </Button>
          <Button type="button" variant="ghost" onClick={refresh} disabled={isSubmitting}>
            Refresh
          </Button>
        </div>
      </div>
    </div>
  );
};

export default ActivationPanel;
