import React, { useState, useMemo } from 'react';
import { Search, ChevronRight, ChevronDown } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import FileIcon from '../shared/FileIcon';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';

interface MatchLine {
  path: string;
  line?: number;
  content?: string;
  snippet?: string;
}

interface GroupedMatch {
  path: string;
  lines: { line: number; content: string }[];
}

function parseMatches(artifact: { content: string; metadata?: Record<string, any> }): {
  groups: GroupedMatch[];
  totalMatches: number;
  hasMore: boolean;
} {
  const raw = artifact.metadata?.raw_result as
    | {
        matches?: MatchLine[] | Record<string, string[]>;
        total_matches?: number;
        count?: number;
        has_more?: boolean;
      }
    | undefined;

  // Structured result with matches array
  if (raw?.matches && Array.isArray(raw.matches)) {
    const byFile = new Map<string, { line: number; content: string }[]>();
    for (const m of raw.matches) {
      const key = m.path || 'unknown';
      const line = typeof m.line === 'number' && Number.isFinite(m.line) ? m.line : 0;
      const content = String(m.content ?? m.snippet ?? '');
      if (!byFile.has(key)) byFile.set(key, []);
      byFile.get(key)!.push({ line, content });
    }
    const groups: GroupedMatch[] = [];
    for (const [path, lines] of byFile) {
      groups.push({ path, lines });
    }
    return {
      groups,
      totalMatches: raw.total_matches ?? raw.count ?? raw.matches.length,
      hasMore: raw.has_more === true,
    };
  }

  // Legacy attached-file search returns a map of path -> line strings.
  if (raw?.matches && typeof raw.matches === 'object') {
    const groups: GroupedMatch[] = [];
    let totalMatches = 0;
    for (const [path, lines] of Object.entries(raw.matches)) {
      if (!Array.isArray(lines)) continue;
      const parsedLines = lines
        .filter((line): line is string => typeof line === 'string')
        .map(parseSearchLine);
      if (parsedLines.length === 0) continue;
      totalMatches += parsedLines.length;
      groups.push({ path, lines: parsedLines });
    }
    return {
      groups,
      totalMatches: raw.total_matches ?? raw.count ?? totalMatches,
      hasMore: raw.has_more === true,
    };
  }

  // Plain text fallback: try to parse file-grouped sections
  // Format: path headers followed by indented match lines like "  123: content"
  const text = artifact.content || '';
  if (!text.trim()) return { groups: [], totalMatches: 0, hasMore: false };

  const groups: GroupedMatch[] = [];
  let currentPath: string | null = null;
  let currentLines: { line: number; content: string }[] = [];
  let totalMatches = 0;

  for (const rawLine of text.split('\n')) {
    const lineMatch = rawLine.match(/^\s+(\d+):\s?(.*)$/);
    if (lineMatch) {
      const lineNum = parseInt(lineMatch[1], 10);
      currentLines.push({ line: lineNum, content: lineMatch[2] });
      totalMatches++;
    } else if (rawLine.trim() && !rawLine.startsWith(' ')) {
      // New file header
      if (currentPath && currentLines.length > 0) {
        groups.push({ path: currentPath, lines: currentLines });
      }
      currentPath = rawLine.trim().replace(/:$/, '');
      currentLines = [];
    }
  }
  if (currentPath && currentLines.length > 0) {
    groups.push({ path: currentPath, lines: currentLines });
  }

  return { groups, totalMatches, hasMore: raw?.has_more === true };
}

function parseSearchLine(rawLine: string): { line: number; content: string } {
  const line = rawLine.trim();
  const prefixed = line.match(/^Line\s+(\d+):\s?(.*)$/i);
  if (prefixed) {
    return { line: parseInt(prefixed[1], 10), content: prefixed[2] };
  }
  const compact = line.match(/^(\d+):\s?(.*)$/);
  if (compact) {
    return { line: parseInt(compact[1], 10), content: compact[2] };
  }
  return { line: 0, content: rawLine };
}

const FileGroup: React.FC<{ group: GroupedMatch }> = ({ group }) => {
  const [open, setOpen] = useState(true);
  const fileName = group.path.split('/').pop() || group.path;

  return (
    <div className="border-b border-border/50 last:border-b-0">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full px-3 py-1.5 hover:bg-muted/30 transition-colors text-left"
      >
        {open ? (
          <ChevronDown size={12} className="text-muted-foreground shrink-0" />
        ) : (
          <ChevronRight size={12} className="text-muted-foreground shrink-0" />
        )}
        <FileIcon filename={fileName} size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs font-mono text-foreground truncate">{group.path}</span>
        <Badge variant="secondary" className="text-[10px] px-1.5 py-0 ml-auto shrink-0">
          {group.lines.length}
        </Badge>
      </button>
      {open && (
        <div className="bg-muted/20 border-t border-border/30">
          {group.lines.map((line, i) => (
            <div key={i} className="flex text-xs font-mono px-3 py-0.5 hover:bg-muted/30">
              <span className="text-muted-foreground/50 select-none pr-3 text-right w-12 shrink-0">
                {line.line > 0 ? line.line : '-'}
              </span>
              <span className="text-foreground whitespace-pre-wrap break-all">{line.content}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
};

const SearchFilesRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const { groups, totalMatches, hasMore } = useMemo(() => parseMatches(artifact), [artifact]);

  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      {/* Summary bar */}
      <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border">
        <Search size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs text-muted-foreground">
          {totalMatches} {totalMatches === 1 ? 'match' : 'matches'} across{' '}
          {groups.length} {groups.length === 1 ? 'file' : 'files'}
        </span>
        {hasMore && (
          <Badge variant="outline" className="text-[10px] ml-auto shrink-0">
            More results available
          </Badge>
        )}
      </div>

      {/* Results */}
      <div className="flex-1 overflow-auto">
        {groups.length > 0 ? (
          groups.map((group, i) => <FileGroup key={i} group={group} />)
        ) : (
          <p className="text-xs text-muted-foreground text-center py-4">No search results.</p>
        )}
      </div>
    </div>
  );
};

registerToolRenderer('search_files', {
  component: SearchFilesRenderer,
  label: 'Search Results',
  icon: Search,
});

export default SearchFilesRenderer;
