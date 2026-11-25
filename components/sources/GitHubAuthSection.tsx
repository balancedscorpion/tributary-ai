import React from 'react';
import { ExternalLink, KeyRound, Loader2, LogIn, LogOut, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { openExternalUrl } from '@/services/openExternal';

// Device-flow sign-in against the project git auth routes. Ported from the
// retired Project Files dialog so the clone credential management lives with
// the GitHub add-repository form. Busy controls stay enabled with aria-busy
// (disabling a focused control blurs focus to <body>).

type GitProfile = {
  git_user_name?: string;
  git_user_email?: string;
  github_login?: string;
  github_available?: boolean;
  github_error?: string;
};

type GitHubAuthStatus = {
  configured: boolean;
  client_id_configured: boolean;
  client_id?: string;
  secrets_initialized: boolean;
  secrets_locked: boolean;
  connected: boolean;
  login?: string;
  scope?: string;
  profile?: GitProfile;
};

type GitHubDeviceFlow = {
  state: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete?: string;
  expires_in?: number;
  interval?: number;
};

async function readJsonSafe(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try { return JSON.parse(text); } catch { return { error: text }; }
}

const GitHubAuthSection: React.FC = () => {
  const pollTimeoutRef = React.useRef<number | null>(null);
  const [status, setStatus] = React.useState<GitHubAuthStatus | null>(null);
  const [statusLoading, setStatusLoading] = React.useState(false);
  const [authProcessing, setAuthProcessing] = React.useState(false);
  const [clientSaving, setClientSaving] = React.useState(false);
  const [clientId, setClientId] = React.useState('');
  const [flow, setFlow] = React.useState<GitHubDeviceFlow | null>(null);
  const [message, setMessage] = React.useState<string | null>(null);

  const clearPoll = React.useCallback(() => {
    if (pollTimeoutRef.current !== null) {
      window.clearTimeout(pollTimeoutRef.current);
      pollTimeoutRef.current = null;
    }
  }, []);

  React.useEffect(() => clearPoll, [clearPoll]);

  const loadStatus = React.useCallback(async () => {
    setStatusLoading(true);
    try {
      const res = await fetch('/api/projects/git/auth/status', { headers: { Accept: 'application/json' } });
      const data = await readJsonSafe(res);
      if (!res.ok) throw new Error(data?.error || `GitHub status failed (HTTP ${res.status})`);
      const next = (data?.status || null) as GitHubAuthStatus | null;
      setStatus(next);
      setClientId(next?.client_id || '');
    } catch (error: any) {
      setStatus(null);
      setMessage(String(error?.message || error || 'Unable to read GitHub status.'));
    } finally {
      setStatusLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void loadStatus();
  }, [loadStatus]);

  const poll = React.useCallback((state: string, delaySeconds: number) => {
    clearPoll();
    const delay = Math.max(1, delaySeconds || 5) * 1000;
    pollTimeoutRef.current = window.setTimeout(async () => {
      try {
        const res = await fetch('/api/projects/git/auth/poll', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({ state }),
        });
        const data = await readJsonSafe(res);
        if (!res.ok) throw new Error(data?.error || `GitHub sign-in failed (HTTP ${res.status})`);
        if (data?.state === 'complete') {
          clearPoll();
          const next = (data?.status || null) as GitHubAuthStatus | null;
          setStatus(next);
          setFlow(null);
          setMessage(next?.login ? `Connected as ${next.login}.` : 'GitHub connected.');
          setAuthProcessing(false);
          toast.success(next?.login ? `Signed in to GitHub as ${next.login}.` : 'Signed in to GitHub.');
          return;
        }
        setMessage('Waiting for GitHub authorization...');
        poll(state, Number(data?.interval || delaySeconds || 5));
      } catch (error: any) {
        clearPoll();
        const detail = String(error?.message || error || 'GitHub sign-in failed.');
        setMessage(detail);
        setFlow(null);
        setAuthProcessing(false);
        toast.error(detail);
      }
    }, delay);
  }, [clearPoll]);

  const handleSignIn = React.useCallback(async () => {
    if (authProcessing) return;
    setAuthProcessing(true);
    setMessage('Starting GitHub sign-in...');
    clearPoll();
    try {
      const res = await fetch('/api/projects/git/auth/start', {
        method: 'POST',
        headers: { Accept: 'application/json' },
      });
      const data = await readJsonSafe(res);
      if (!res.ok) {
        const parts = [data?.error, data?.detail, data?.hint].filter(
          (part: unknown) => typeof part === 'string' && part,
        );
        throw new Error(parts.join('\n\n') || `GitHub sign-in failed (HTTP ${res.status})`);
      }
      if (data?.state === 'complete') {
        const next = (data?.status || null) as GitHubAuthStatus | null;
        setStatus(next);
        setFlow(null);
        setMessage(next?.login ? `Signed in as ${next.login}.` : 'Signed in to GitHub.');
        setAuthProcessing(false);
        toast.success(next?.login ? `Signed in to GitHub as ${next.login}.` : 'Signed in to GitHub.');
        return;
      }
      const nextFlow = data as GitHubDeviceFlow;
      if (!nextFlow.state || !nextFlow.user_code) {
        throw new Error('GitHub did not return a sign-in session.');
      }
      setFlow(nextFlow);
      setMessage('Waiting for GitHub authorization...');
      const authURL = nextFlow.verification_uri_complete || nextFlow.verification_uri;
      if (authURL) void openExternalUrl(authURL);
      poll(nextFlow.state, Number(nextFlow.interval || 5));
    } catch (error: any) {
      const detail = String(error?.message || error || 'GitHub sign-in failed.');
      setMessage(detail);
      setFlow(null);
      setAuthProcessing(false);
      toast.error(detail);
    }
  }, [authProcessing, clearPoll, poll]);

  const handleDisconnect = React.useCallback(async () => {
    if (authProcessing) return;
    setAuthProcessing(true);
    setMessage(null);
    clearPoll();
    try {
      const res = await fetch('/api/projects/git/auth/disconnect', {
        method: 'POST',
        headers: { Accept: 'application/json' },
      });
      const data = await readJsonSafe(res);
      if (!res.ok) throw new Error(data?.error || `Disconnect failed (HTTP ${res.status})`);
      setStatus((data?.status || null) as GitHubAuthStatus | null);
      setFlow(null);
      setMessage('GitHub disconnected.');
      toast.success('GitHub disconnected.');
    } catch (error: any) {
      const detail = String(error?.message || error || 'Failed to disconnect GitHub.');
      setMessage(detail);
      toast.error(detail);
    } finally {
      setAuthProcessing(false);
    }
  }, [authProcessing, clearPoll]);

  const handleSaveClientId = React.useCallback(async () => {
    if (clientSaving) return;
    setClientSaving(true);
    setMessage(null);
    try {
      const res = await fetch('/api/projects/git/auth/client', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ client_id: clientId.trim() }),
      });
      const data = await readJsonSafe(res);
      if (!res.ok) throw new Error(data?.error || `Save failed (HTTP ${res.status})`);
      const next = (data?.status || null) as GitHubAuthStatus | null;
      setStatus(next);
      setClientId(next?.client_id || '');
      const saved = next?.client_id_configured;
      setMessage(saved ? 'Custom GitHub sign-in client saved.' : 'Custom GitHub sign-in client cleared.');
      toast.success(saved ? 'Custom GitHub sign-in client saved.' : 'Custom GitHub sign-in client cleared.');
    } catch (error: any) {
      const detail = String(error?.message || error || 'Failed to save GitHub sign-in settings.');
      setMessage(detail);
      toast.error(detail);
    } finally {
      setClientSaving(false);
    }
  }, [clientId, clientSaving]);

  return (
    <div className="grid gap-2 rounded-md border border-border bg-muted/20 px-3 py-2" data-github-auth-section>
      <div className="flex flex-wrap items-center gap-2">
        {statusLoading ? (
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <Loader2 size={13} aria-hidden="true" className="motion-safe:animate-spin" />
            Reading GitHub status...
          </span>
        ) : (
          <Badge variant={status?.connected ? 'default' : 'secondary'} className="gap-1">
            <KeyRound size={11} aria-hidden="true" />
            {status?.connected ? `Connected: ${status.login || 'GitHub'}` : 'GitHub not connected'}
          </Badge>
        )}
        {status?.secrets_locked && <Badge variant="secondary">Secrets locked</Badge>}
        <div className="ml-auto flex items-center gap-1">
          {status?.connected ? (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="h-7 gap-1.5 px-2 text-xs"
              onClick={() => void handleDisconnect()}
              aria-busy={authProcessing}
              aria-label="Disconnect the GitHub account used for cloning"
            >
              {authProcessing ? <Loader2 size={13} aria-hidden="true" className="motion-safe:animate-spin" /> : <LogOut size={13} aria-hidden="true" />}
              Disconnect
            </Button>
          ) : (
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 gap-1.5 px-2 text-xs"
              onClick={() => void handleSignIn()}
              aria-busy={authProcessing}
              aria-label="Sign in to GitHub for private repositories"
            >
              {authProcessing ? <Loader2 size={13} aria-hidden="true" className="motion-safe:animate-spin" /> : <LogIn size={13} aria-hidden="true" />}
              Sign in
            </Button>
          )}
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-7 w-7 text-muted-foreground hover:text-foreground"
            onClick={() => void loadStatus()}
            aria-busy={statusLoading}
            aria-label="Reload GitHub connection status"
          >
            <RefreshCw size={13} aria-hidden="true" className={statusLoading ? 'motion-safe:animate-spin' : ''} />
          </Button>
        </div>
      </div>
      {flow && (
        <div className="flex items-center justify-between gap-3 rounded-md bg-muted/40 px-2.5 py-1.5 text-xs">
          <span>
            Code: <span className="font-mono text-foreground">{flow.user_code}</span>
          </span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 gap-1 px-2"
            onClick={() => { void openExternalUrl(flow.verification_uri_complete || flow.verification_uri); }}
          >
            <ExternalLink size={13} aria-hidden="true" />
            Open
          </Button>
        </div>
      )}
      {message && (
        <p role="status" className="whitespace-pre-wrap text-xs text-muted-foreground">{message}</p>
      )}
      <details className="group">
        <summary className="cursor-pointer select-none text-xs font-medium text-muted-foreground">
          Advanced sign-in settings
        </summary>
        <div className="mt-2 flex flex-col gap-2 sm:flex-row">
          <Input
            value={clientId}
            onChange={(e) => setClientId(e.target.value)}
            placeholder="Custom OAuth client ID"
            aria-label="Custom OAuth client ID"
            autoComplete="off"
            className="h-8 font-mono text-xs"
          />
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-8 gap-1.5 sm:w-24"
            onClick={() => void handleSaveClientId()}
            aria-busy={clientSaving}
          >
            {clientSaving ? <Loader2 size={14} aria-hidden="true" className="motion-safe:animate-spin" /> : <KeyRound size={14} aria-hidden="true" />}
            Save
          </Button>
        </div>
      </details>
    </div>
  );
};

export default GitHubAuthSection;
