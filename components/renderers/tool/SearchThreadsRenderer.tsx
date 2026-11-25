import React, { useMemo } from 'react';
import { FileText, MessageSquare, Search } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';

interface ThreadSearchResult {
  doc_id?: string;
  doc_kind?: string;
  thread_id?: string;
  title?: string;
  snippet?: string;
  score?: number;
  updated_at?: number;
  run_id?: string;
  message_id?: string;
  artifact_id?: string;
}

interface FileMatch {
  path?: string;
  line?: number;
  snippet?: string;
}

interface FileSearchResult {
  available?: boolean;
  reason?: string;
  error?: string;
  count?: number;
  truncated?: boolean;
  matches?: FileMatch[];
}

interface SearchThreadsPayload {
  results?: ThreadSearchResult[];
  count?: number;
  file_search?: FileSearchResult;
}

function parsePayload(content: string): SearchThreadsPayload {
  try {
    const parsed = JSON.parse(content);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as SearchThreadsPayload;
    }
  } catch {
    // Fall through to empty payload.
  }
  return {};
}

function formatScore(score: unknown): string | null {
  if (typeof score !== 'number' || !Number.isFinite(score)) return null;
  return score.toFixed(3);
}

const SearchThreadsRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const payload = useMemo(() => parsePayload(artifact.content || ''), [artifact.content]);
  const threadResults = Array.isArray(payload.results) ? payload.results : [];
  const fileSearch = payload.file_search;
  const fileMatches = Array.isArray(fileSearch?.matches) ? fileSearch.matches : [];

  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border">
        <Search size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs text-muted-foreground">
          {threadResults.length} thread/artifact {threadResults.length === 1 ? 'match' : 'matches'}
        </span>
        {fileSearch?.available && (
          <Badge variant="outline" className="text-[10px] ml-auto shrink-0">
            {fileMatches.length} file {fileMatches.length === 1 ? 'match' : 'matches'}
          </Badge>
        )}
      </div>

      <div className="flex-1 overflow-auto divide-y divide-border/50">
        {threadResults.length === 0 && fileMatches.length === 0 && (
          <p className="text-xs text-muted-foreground text-center py-4">
            No thread, artifact, or file results.
          </p>
        )}

        {threadResults.map((result, i) => {
          const score = formatScore(result.score);
          return (
            <div key={`${result.doc_id || 'result'}-${i}`} className="p-3 hover:bg-muted/20">
              <div className="flex items-start gap-2 min-w-0">
                <MessageSquare size={14} className="text-muted-foreground shrink-0 mt-0.5" />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 min-w-0">
                    <span className="text-xs font-medium text-foreground truncate">
                      {result.title || result.doc_id || 'Untitled result'}
                    </span>
                    {result.doc_kind && (
                      <Badge variant="secondary" className="text-[10px] px-1.5 py-0 shrink-0">
                        {result.doc_kind}
                      </Badge>
                    )}
                    {score && (
                      <span className="text-[10px] text-muted-foreground font-mono shrink-0">
                        {score}
                      </span>
                    )}
                  </div>
                  {result.snippet && (
                    <p className="text-xs text-muted-foreground mt-1 whitespace-pre-wrap break-words">
                      {result.snippet}
                    </p>
                  )}
                  <div className="flex flex-wrap gap-x-3 gap-y-1 mt-2 text-[10px] text-muted-foreground font-mono">
                    {result.doc_id && <span>doc_id={result.doc_id}</span>}
                    {result.thread_id && <span>thread_id={result.thread_id}</span>}
                    {result.artifact_id && <span>artifact_id={result.artifact_id}</span>}
                    {result.message_id && <span>message_id={result.message_id}</span>}
                  </div>
                </div>
              </div>
            </div>
          );
        })}

        {fileMatches.length > 0 && (
          <div className="bg-muted/10">
            <div className="flex items-center gap-2 px-3 py-2 border-b border-border/40">
              <FileText size={14} className="text-muted-foreground" />
              <span className="text-xs font-medium text-foreground">Current project files</span>
              {fileSearch?.truncated && (
                <Badge variant="outline" className="text-[10px] ml-auto">
                  More available
                </Badge>
              )}
            </div>
            {fileMatches.map((match, i) => (
              <div key={`${match.path || 'file'}-${match.line || i}-${i}`} className="px-3 py-2 border-b border-border/30 last:border-b-0">
                <div className="flex items-center gap-2 text-xs font-mono text-foreground">
                  <span className="truncate">{match.path || 'unknown file'}</span>
                  {typeof match.line === 'number' && (
                    <span className="text-muted-foreground shrink-0">:{match.line}</span>
                  )}
                </div>
                {match.snippet && (
                  <p className="text-xs text-muted-foreground mt-1 whitespace-pre-wrap break-words">
                    {match.snippet}
                  </p>
                )}
              </div>
            ))}
          </div>
        )}

        {fileSearch && fileSearch.available === false && (fileSearch.reason || fileSearch.error) && (
          <div className="p-3 text-xs text-muted-foreground">
            File search skipped: {fileSearch.error || fileSearch.reason}
          </div>
        )}
      </div>
    </div>
  );
};

registerToolRenderer('search_threads', {
  component: SearchThreadsRenderer,
  label: 'Thread Search Results',
  icon: Search,
});

registerToolRenderer('search_docs', {
  component: SearchThreadsRenderer,
  label: 'Search Results',
  icon: Search,
});

export default SearchThreadsRenderer;
