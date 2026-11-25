import React from 'react';
import { Lock, Shield } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import type { NormalizedSecretsStatus } from '@/services/secretsApi';
import { getSecretsStatus, initSecrets, unlockSecrets } from '@/services/secretsApi';

type SecretsUnlockPanelProps = {
  /**
   * When true, the panel will fetch status and (if needed) block the rest of Settings UI.
   */
  active: boolean;
  onReady?: (status: NormalizedSecretsStatus) => void;
};

function statusNeedsGate(status: NormalizedSecretsStatus | null): boolean {
  if (!status) return true;
  if (status.initialized === false) return true;
  if (status.locked === true) return true;
  return false;
}

const SecretsUnlockPanel: React.FC<SecretsUnlockPanelProps> = ({ active, onReady }) => {
  const [status, setStatus] = React.useState<NormalizedSecretsStatus | null>(null);
  const [isLoading, setIsLoading] = React.useState(false);
  const [isSubmitting, setIsSubmitting] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  const [password, setPassword] = React.useState('');
  const [confirm, setConfirm] = React.useState('');

  // Avoid refetch loops when parents pass unstable callbacks in dev (StrictMode + re-renders).
  const onReadyRef = React.useRef(onReady);
  React.useEffect(() => {
    onReadyRef.current = onReady;
  }, [onReady]);

  const refresh = React.useCallback(async () => {
    if (!active) return;
    setIsLoading(true);
    setError(null);
    try {
      const next = await getSecretsStatus();
      setStatus(next);
      if (!statusNeedsGate(next)) onReadyRef.current?.(next);
    } catch (e: any) {
      setStatus(null);
      setError(e?.message || 'Failed to check secrets status');
    } finally {
      setIsLoading(false);
    }
  }, [active]);

  React.useEffect(() => {
    if (!active) return;
    void refresh();
  }, [active, refresh]);

  if (!active) return null;

  const needsInit = status?.initialized === false;
  const needsUnlock = status?.initialized !== false && status?.locked === true;
  const shouldGate = statusNeedsGate(status);

  if (!shouldGate && !isLoading) return null;

  return (
    <div className="p-5 space-y-4">
      <div className="flex items-center gap-2 text-sm font-medium text-muted-foreground">
        <Shield size={16} /> Secrets
      </div>

      {isLoading && (
        <div className="text-sm text-muted-foreground">Checking secrets status…</div>
      )}

      {!isLoading && needsInit && (
        <div className="space-y-3">
          <div className="text-sm">
            Initialize the secrets store to save API keys securely (encrypted at rest).
          </div>

          <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
            <div>
              <Label className="text-xs mb-1.5 block">New Password</Label>
              <Input
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                placeholder="Create a password"
                className="font-mono"
                autoFocus
              />
            </div>
            <div>
              <Label className="text-xs mb-1.5 block">Confirm Password</Label>
              <Input
                type="password"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                placeholder="Repeat password"
                className="font-mono"
              />
            </div>
          </div>

          {error && (
            <div className="text-sm text-destructive">{error}</div>
          )}

          <div className="flex items-center gap-2">
            <Button
              onClick={async () => {
                setError(null);
                const p = password.trim();
                if (!p) { setError('Password is required'); return; }
                if (p !== confirm) { setError('Passwords do not match'); return; }
                setIsSubmitting(true);
                try {
                  await initSecrets(p);
                  setPassword('');
                  setConfirm('');
                  await refresh();
                } catch (e: any) {
                  setError(e?.message || 'Failed to initialize secrets');
                } finally {
                  setIsSubmitting(false);
                }
              }}
              disabled={isSubmitting}
              className="gap-2"
            >
              <Lock size={16} />
              {isSubmitting ? 'Initializing…' : 'Initialize'}
            </Button>
            <Button variant="outline" onClick={refresh} disabled={isSubmitting}>
              Refresh
            </Button>
          </div>
        </div>
      )}

      {!isLoading && needsUnlock && (
        <div className="space-y-3">
          <div className="text-sm">
            Secrets are locked. Unlock to enable server-side API keys and provider proxies.
          </div>

          <div className="max-w-sm">
            <Label className="text-xs mb-1.5 block">Password</Label>
            <Input
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              placeholder="Enter password"
              className="font-mono"
              autoFocus
            />
          </div>

          {error && (
            <div className="text-sm text-destructive">{error}</div>
          )}

          <div className="flex items-center gap-2">
            <Button
              onClick={async () => {
                setError(null);
                const p = password.trim();
                if (!p) { setError('Password is required'); return; }
                setIsSubmitting(true);
                try {
                  await unlockSecrets(p);
                  setPassword('');
                  await refresh();
                } catch (e: any) {
                  setError(e?.message || 'Failed to unlock secrets');
                } finally {
                  setIsSubmitting(false);
                }
              }}
              disabled={isSubmitting}
              className="gap-2"
            >
              <Lock size={16} />
              {isSubmitting ? 'Unlocking…' : 'Unlock'}
            </Button>
            <Button variant="outline" onClick={refresh} disabled={isSubmitting}>
              Refresh
            </Button>
          </div>
        </div>
      )}

      {!isLoading && !needsInit && !needsUnlock && (
        <div className="space-y-2">
          {error ? (
            <div className="text-sm text-muted-foreground">
              Secrets status unavailable: {error}
            </div>
          ) : (
            <div className="text-sm text-muted-foreground">
              Secrets are ready.
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export default SecretsUnlockPanel;
