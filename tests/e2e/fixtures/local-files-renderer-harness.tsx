import React from 'react';
import { createRoot } from 'react-dom/client';
import ArtifactPanel from '@/components/ArtifactPanel';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { AppSettings, Artifact } from '@/types';

// Harness for the first-class local file result renderer (file_search /
// local_list_folder). Mounts real ArtifactPanel resolution so the exact
// tool-name registration is under test, and mocks the grant preview stream
// endpoint in both its available and unavailable forms.

const settings = {
  systemPrompt: '',
  featureFlags: {},
} as AppSettings;

const SEARCH_RESULT = {
  local_root: '/Users/fixture/Reports',
  local_kind: 'folder',
  path: '',
  query: 'forecast',
  grant_id: 'grant-reports',
  name_matches: ['Q3 forecast.xlsx'],
  content_matches: {
    'notes/summary.md': ['Line 4: forecast revised upward', 'Line 9: forecast assumptions'],
  },
  files: [
    {
      file_ref: 'Q3 forecast.xlsx',
      path: 'Q3 forecast.xlsx',
      display_path: 'Q3 forecast.xlsx',
      mime_type: 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
      file_kind: 'spreadsheet',
      match_types: ['name'],
      grant_id: 'grant-reports',
    },
    {
      file_ref: 'notes/summary.md',
      path: 'notes/summary.md',
      display_path: 'notes/summary.md',
      mime_type: 'text/markdown',
      file_kind: 'document',
      match_types: ['content'],
      line_matches: ['Line 4: forecast revised upward', 'Line 9: forecast assumptions'],
      grant_id: 'grant-reports',
    },
  ],
  matches_count: 3,
  limit: 20,
  truncated: true,
};

const FOLDER_RESULT = {
  local_root: '/Users/fixture/Reports',
  local_kind: 'folder',
  path: 'archive',
  recursive: false,
  entries_count: 3,
  files_count: 2,
  folders_count: 1,
  limit: 100,
  truncated: false,
  grant_id: 'grant-reports',
  folders: [
    { name: '2025', path: 'archive/2025', display_path: 'archive/2025', entry_kind: 'folder' },
  ],
  files: [
    {
      name: 'summary.csv',
      path: 'archive/summary.csv',
      display_path: 'archive/summary.csv',
      entry_kind: 'file',
      file_ref: 'archive/summary.csv',
      mime_type: 'text/csv',
      file_kind: 'spreadsheet',
      size_bytes: 2048,
      grant_id: 'grant-reports',
    },
    {
      name: 'readme.txt',
      path: 'archive/readme.txt',
      display_path: 'archive/readme.txt',
      entry_kind: 'file',
      file_ref: 'archive/readme.txt',
      mime_type: 'text/plain',
      file_kind: 'document',
      size_bytes: 512,
      grant_id: 'grant-reports',
    },
  ],
};

function localArtifact(toolName: string, result: Record<string, unknown>): Artifact {
  return {
    id: `local:${toolName}`,
    type: 'data',
    title: toolName,
    content: JSON.stringify(result),
    metadata: {
      ok: true,
      tool_name: toolName,
      tool_args: toolName === 'file_search' ? { query: 'forecast' } : { path: 'archive' },
      raw_result: result,
      result_metadata: {
        tool_name: toolName,
        provider: 'local-files',
        family: 'local-files',
        readonly: true,
        grant_id: 'grant-reports',
      },
    },
  };
}

export async function mountLocalFilesRendererHarness(
  options: { preview?: 'available' | 'unavailable' } = {},
): Promise<void> {
  const previewMode = options.preview || 'available';
  const originalFetch = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(
      typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url,
      window.location.origin,
    );
    if (url.pathname.startsWith('/api/local-files/grants/')) {
      if (previewMode === 'unavailable') {
        // A build without the stream endpoint answers with the bare mux 404
        // (text, not the endpoint's JSON error envelope).
        return new Response('404 page not found', {
          status: 404,
          headers: { 'Content-Type': 'text/plain; charset=utf-8' },
        });
      }
      const body = 'forecast revised upward\nforecast assumptions hold';
      return new Response(body, {
        status: 200,
        headers: { 'Content-Type': 'text/plain', 'Content-Length': String(body.length) },
      });
    }
    return originalFetch(input as RequestInfo, init);
  };

  const rootNode = document.createElement('div');
  rootNode.id = 'local-files-renderer-root';
  document.body.appendChild(rootNode);
  const root = createRoot(rootNode);
  root.render(
    <TooltipProvider>
      <div className="space-y-4">
        <section
          data-testid="local-files-case-search"
          style={{ height: 640, border: '1px solid #ddd', overflow: 'hidden', position: 'relative' }}
        >
          <ArtifactPanel
            artifact={localArtifact('file_search', SEARCH_RESULT)}
            settings={settings}
            onClose={() => undefined}
          />
        </section>
        <section
          data-testid="local-files-case-folder"
          style={{ height: 640, border: '1px solid #ddd', overflow: 'hidden', position: 'relative' }}
        >
          <ArtifactPanel
            artifact={localArtifact('local_list_folder', FOLDER_RESULT)}
            settings={settings}
            onClose={() => undefined}
          />
        </section>
      </div>
    </TooltipProvider>,
  );
  await new Promise((resolve) => window.setTimeout(resolve, 50));
}
