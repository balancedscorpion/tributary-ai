import React from 'react';
import { TooltipProvider } from '@/components/ui/tooltip';
import SourcesControl from '@/components/sources/SourcesDialog';
import ArtifactPanel from '@/components/ArtifactPanel';
import { DEFAULT_SETTINGS } from '@/constants';
import type { AppSettings, Artifact } from '@/types';

// Visual QA fixture for the unified Sources dialog: /dev/sources
//
// Query params:
//   backend=unified   GET /api/sources answers with registry rows
//   backend=legacy    (default) /api/sources 404s; the dialog composes from
//                     permission rules + project sources + the repo store
//   repoStore=0       the GitHub repo-store routes 404 (pre-repo-store build)
//   failLocal=1       the permission-rules route returns 500
//   treeUnavailable=1 the repo tree route 404s (browse fallback state)
//   empty=1           no grants, repos, or project sources (empty state)
//
// Mutations are recorded on window.__sourcesFixtureState for assertions.

const PROJECT_ID = 'project-fixture';

type FixtureState = {
  rules: Array<Record<string, any>>;
  projectSources: Array<Record<string, any>>;
  repos: Array<Record<string, any>>;
  createdRules: Array<Record<string, any>>;
  deletedRuleIds: string[];
  deletedProjectSources: string[];
  hiddenPatches: Array<{ name: string; hidden: boolean }>;
  removedRepoIds: string[];
  refreshedRepoIds: string[];
  clonedUrls: string[];
  openFilesCalls: number;
  askAgentPrompts: string[];
  settingsSaves: Array<Record<string, any>>;
};

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function initialState(): FixtureState {
  return {
    rules: [
      {
        id: 'rule-reports',
        category: 'read',
        effect: 'allow',
        scope: 'global',
        subject: { kind: 'local_location', local_root: '/Users/fixture/Reports', local_kind: 'folder' },
      },
    ],
    projectSources: [
      { name: 'quarterly-uploads', path: 'quarterly-uploads', kind: 'directory', hidden: false, file_count: 42, total_bytes: 1048576 },
      { name: 'legacy-repo', path: 'legacy-repo', kind: 'git', hidden: false, file_count: 210, total_bytes: 4194304 },
    ],
    repos: [
      {
        id: 'repo-analytics',
        full_name: 'acme/analytics',
        name: 'acme/analytics',
        remote_url: 'https://github.com/acme/analytics',
        default_ref: 'main',
        last_fetched_at: new Date(Date.now() - 2 * 3600_000).toISOString(),
        byte_size: 5242880,
      },
    ],
    createdRules: [],
    deletedRuleIds: [],
    deletedProjectSources: [],
    hiddenPatches: [],
    removedRepoIds: [],
    refreshedRepoIds: [],
    clonedUrls: [],
    openFilesCalls: 0,
    askAgentPrompts: [],
    settingsSaves: [],
  };
}

const REPO_TREE: Record<string, Array<Record<string, any>>> = {
  '': [
    { name: 'src', path: 'src', type: 'dir' },
    { name: 'README.md', path: 'README.md', type: 'file', byte_size: 96 },
  ],
  src: [
    { name: 'main.go', path: 'src/main.go', type: 'file', byte_size: 220 },
  ],
};

const REPO_FILES: Record<string, { body: string; mime: string }> = {
  'README.md': { body: '# Analytics\n\nFixture readme for the repo browser.', mime: 'text/markdown' },
  'src/main.go': { body: 'package main\n\nfunc main() {}\n', mime: 'text/plain' },
};

function unifiedSourceRows(state: FixtureState): Array<Record<string, any>> {
  return [
    {
      id: 'src-local-1',
      kind: 'local_reference',
      title: 'Reports',
      local_root: '/Users/fixture/Reports',
      grant_id: 'rule-reports',
    },
    {
      id: 'src-repo-1',
      kind: 'github_clone',
      title: 'acme/analytics',
      remote_url: 'https://github.com/acme/analytics',
      branch: 'main',
    },
    {
      id: 'src-copy-1',
      kind: 'project_snapshot',
      title: 'quarterly-uploads',
      file_count: 42,
      byte_size: 1048576,
      visibility: 'visible',
      capabilities: { delete_copy: true, hide: true },
    },
    // Individual connector/chat files live in Files, not this dialog; the
    // registry still returns them, and the footer must count them honestly.
    {
      id: 'src-file-1',
      kind: 'connector_file',
      title: 'roadmap.pdf',
      remote_url: 'https://example.sharepoint.com/roadmap.pdf',
    },
  ];
}

const SourcesFixture: React.FC = () => {
  const [ready, setReady] = React.useState(false);
  const [settings, setSettings] = React.useState<AppSettings>(() => ({
    ...DEFAULT_SETTINGS,
    localComputer: { enabled: true, accessMode: 'selected-folders', selectedFolders: ['/Users/fixture/Reports'] },
  }));
  const [activeArtifact, setActiveArtifact] = React.useState<Artifact | null>(null);
  const [project, setProject] = React.useState({ id: PROJECT_ID, name: 'Fixture project', fileCount: 252 });
  const stateRef = React.useRef<FixtureState>(initialState());

  React.useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const unified = params.get('backend') === 'unified';
    const repoStore = params.get('repoStore') !== '0';
    const failLocal = params.get('failLocal') === '1';
    const treeUnavailable = params.get('treeUnavailable') === '1';
    const empty = params.get('empty') === '1';
    const state = stateRef.current;
    if (empty) {
      state.rules = [];
      state.projectSources = [];
      state.repos = [];
    }
    (window as any).__sourcesFixtureState = state;
    (window as any).aiAnalyst = {
      pickLocalDirectory: async () => '/Users/fixture/Case Files',
    };

    const originalFetch = window.fetch.bind(window);
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url,
        window.location.origin,
      );
      const method = String(init?.method || request?.method || 'GET').toUpperCase();
      const path = url.pathname;

      if (path === '/api/sources' && method === 'GET') {
        return unified ? jsonResponse({ sources: unifiedSourceRows(state) }) : jsonResponse({ error: 'not found' }, 404);
      }

      if (path === '/api/permissions/rules' && method === 'GET') {
        if (failLocal) return jsonResponse({ error: 'permissions store exploded' }, 500);
        return jsonResponse({ rules: state.rules });
      }
      if (path === '/api/permissions/rules' && method === 'POST') {
        const body = JSON.parse(String(init?.body || '{}'));
        const rule = { id: `rule-${state.createdRules.length + 1}`, ...body };
        state.createdRules.push(rule);
        state.rules.push(rule);
        return jsonResponse({ rule });
      }
      const ruleMatch = path.match(/^\/api\/permissions\/rules\/([^/]+)$/);
      if (ruleMatch && method === 'DELETE') {
        const id = decodeURIComponent(ruleMatch[1]);
        state.deletedRuleIds.push(id);
        state.rules = state.rules.filter((rule) => rule.id !== id);
        return jsonResponse({ ok: true });
      }

      if (path === `/api/projects/${PROJECT_ID}/sources` && method === 'GET') {
        return jsonResponse({ sources: state.projectSources });
      }
      const projectSourceMatch = path.match(new RegExp(`^/api/projects/${PROJECT_ID}/sources/([^/]+)$`));
      if (projectSourceMatch && method === 'DELETE') {
        const name = decodeURIComponent(projectSourceMatch[1]);
        state.deletedProjectSources.push(name);
        state.projectSources = state.projectSources.filter((source) => source.name !== name);
        return jsonResponse({
          sources: state.projectSources,
          project: { id: PROJECT_ID, name: 'Fixture project', file_count: 42 },
        });
      }
      if (projectSourceMatch && method === 'PATCH') {
        const name = decodeURIComponent(projectSourceMatch[1]);
        const body = JSON.parse(String(init?.body || '{}'));
        state.hiddenPatches.push({ name, hidden: Boolean(body.hidden) });
        state.projectSources = state.projectSources.map((source) => (
          source.name === name ? { ...source, hidden: Boolean(body.hidden) } : source
        ));
        return jsonResponse({
          sources: state.projectSources,
          project: { id: PROJECT_ID, name: 'Fixture project', file_count: 252 },
        });
      }
      if (path === `/api/projects/${PROJECT_ID}/refresh` && method === 'POST') {
        return jsonResponse({
          project: { id: PROJECT_ID, name: 'Fixture project', file_count: 252 },
          repositories: [{ path: 'legacy-repo', status: 'updated', detail: 'Fast-forwarded' }],
        });
      }

      if (path.startsWith('/api/github/repos/') || path === '/api/github/repos') {
        return jsonResponse({ error: 'not found' }, 404);
      }
      if (path === '/api/github/repositories' && method === 'GET') {
        return repoStore ? jsonResponse({ repositories: state.repos }) : jsonResponse({ error: 'not found' }, 404);
      }
      if (path === '/api/github/repositories/clone' && method === 'POST') {
        if (!repoStore) return jsonResponse({ error: 'not found' }, 404);
        const body = JSON.parse(String(init?.body || '{}'));
        const requested = String(body.repository || body.url || '');
        state.clonedUrls.push(requested);
        const fullName = (requested || 'acme/new-repo').replace(/^https?:\/\/github\.com\//, '').replace(/\.git$/, '');
        state.repos.push({
          id: `repo-new-${state.clonedUrls.length}`,
          full_name: fullName,
          default_ref: 'main',
          remote_url: requested,
        });
        return jsonResponse({
          ok: true,
          repository: {
            source_id: `repo-new-${state.clonedUrls.length}`,
            source_kind: 'github_clone',
            repository: fullName,
            url: requested,
            added_path: `github/repo-new-${state.clonedUrls.length}/worktree`,
          },
        });
      }
      const repoTreeMatch = path.match(/^\/api\/github\/repositories\/([^/]+)\/tree$/);
      if (repoTreeMatch && method === 'GET') {
        if (treeUnavailable) return jsonResponse({ error: 'not found' }, 404);
        const dir = url.searchParams.get('path') || '';
        const entries = REPO_TREE[dir];
        return entries
          ? jsonResponse({ entries, ref: 'main' })
          : jsonResponse({ error: 'unknown path' }, 400);
      }
      const repoRawMatch = path.match(/^\/api\/github\/repositories\/([^/]+)\/raw$/);
      if (repoRawMatch && method === 'GET') {
        const filePath = url.searchParams.get('path') || '';
        const file = REPO_FILES[filePath];
        if (!file) return jsonResponse({ error: 'unknown file' }, 404);
        return new Response(file.body, {
          status: 200,
          headers: { 'Content-Type': file.mime, 'Content-Length': String(file.body.length) },
        });
      }
      const repoRefreshMatch = path.match(/^\/api\/github\/repositories\/([^/]+)\/refresh$/);
      if (repoRefreshMatch && method === 'POST') {
        const id = decodeURIComponent(repoRefreshMatch[1]);
        state.refreshedRepoIds.push(id);
        const repo = state.repos.find((entry) => entry.id === id);
        return jsonResponse({ repository: { ...repo, last_fetched_at: new Date().toISOString() } });
      }
      const repoDeleteMatch = path.match(/^\/api\/github\/repositories\/([^/]+)$/);
      if (repoDeleteMatch && method === 'DELETE') {
        const id = decodeURIComponent(repoDeleteMatch[1]);
        state.removedRepoIds.push(id);
        state.repos = state.repos.filter((entry) => entry.id !== id);
        return jsonResponse({ ok: true });
      }

      if (path === '/api/projects/git/auth/status' && method === 'GET') {
        return jsonResponse({
          status: {
            configured: true,
            client_id_configured: false,
            secrets_initialized: true,
            secrets_locked: false,
            connected: false,
          },
        });
      }

      return originalFetch(input, init);
    };
    setReady(true);
    return () => {
      window.fetch = originalFetch;
      delete (window as any).__sourcesFixtureState;
      delete (window as any).aiAnalyst;
    };
  }, []);

  const handleSaveSettings = React.useCallback(async (next: AppSettings) => {
    stateRef.current.settingsSaves.push({ localComputer: next.localComputer });
    setSettings(next);
  }, []);

  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex h-screen min-h-0 flex-col bg-background text-foreground" data-testid="sources-fixture">
        <header className="flex shrink-0 items-center justify-between border-b border-border bg-card/40 px-5 py-3">
          <div>
            <h1 className="text-lg font-semibold">Sources Fixture</h1>
            <p className="mt-0.5 text-sm text-muted-foreground">
              Unified sources dialog against a mocked backend (legacy composition or unified registry).
            </p>
          </div>
          {ready && (
            <SourcesControl
              settings={settings}
              onSaveSettings={handleSaveSettings}
              currentProjectId={project.id}
              currentProjectName={project.name}
              currentFileCount={project.fileCount}
              onProjectLoaded={(next) => {
                if (next) setProject({ id: next.id, name: next.name, fileCount: next.fileCount });
              }}
              onOpenArtifact={setActiveArtifact}
              onOpenFiles={() => { stateRef.current.openFilesCalls += 1; }}
              onManageLocalComputer={() => { /* settings deep link is App-owned */ }}
              onAskAgent={(prompt) => { stateRef.current.askAgentPrompts.push(prompt); }}
            />
          )}
        </header>
        <main className="min-h-0 flex-1 overflow-hidden p-4">
          {activeArtifact && (
            <div className="mx-auto h-full max-w-4xl overflow-hidden rounded-lg border border-border">
              <ArtifactPanel
                artifact={activeArtifact}
                settings={settings}
                onClose={() => setActiveArtifact(null)}
              />
            </div>
          )}
        </main>
      </div>
    </TooltipProvider>
  );
};

export default SourcesFixture;
