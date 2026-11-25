import { useMemo } from 'react';
import { FolderOpen } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import FileIcon from '../shared/FileIcon';
import ItemList from '../shared/ItemList';
import type { ItemListItem } from '../shared/ItemList';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';

function parseFileList(artifact: { content: string; metadata?: Record<string, any> }): {
  files: string[];
  hasMore: boolean;
} {
  const rawValue = artifact.metadata?.raw_result;
  let raw:
    | { files?: unknown; entries?: unknown; has_more?: boolean }
    | undefined;

  if (rawValue && typeof rawValue === 'object' && !Array.isArray(rawValue)) {
    raw = rawValue as { files?: unknown; entries?: unknown; has_more?: boolean };
  }
  if (typeof rawValue === 'string') {
    try {
      const parsed = JSON.parse(rawValue);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        raw = parsed as { files?: unknown; entries?: unknown; has_more?: boolean };
      }
    } catch {
      // keep string fallback
    }
  }

  // Array directly
  if (Array.isArray(raw)) {
    return { files: raw, hasMore: false };
  }

  if (raw && typeof raw === 'object') {
    const filesRaw = raw.files;
    const entriesRaw = raw.entries;
    const list =
      Array.isArray(filesRaw) ? filesRaw :
      Array.isArray(entriesRaw) ? entriesRaw :
      typeof filesRaw === 'string' ? filesRaw.split('\n') : [];
    const parsedList = list.filter((l): l is string => typeof l === 'string').map((l) => l.trim()).filter(Boolean);
    return { files: parsedList, hasMore: raw.has_more === true };
  }

  // Fall back to content: one file per line
  const text = artifact.content || '';
  if (!text.trim()) return { files: [], hasMore: false };
  return {
    files: text.split('\n').map((l) => l.trim()).filter(Boolean),
    hasMore: false,
  };
}

const ListFilesRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const { files, hasMore } = useMemo(() => parseFileList(artifact), [artifact]);

  const items: ItemListItem[] = useMemo(
    () =>
      files.map((f) => {
        const name = f.split('/').pop() || f;
        return {
          icon: <FileIcon filename={name} size={14} />,
          label: f,
        };
      }),
    [files],
  );

  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      {hasMore && (
        <div className="px-3 py-1.5 bg-yellow-500/10 border-b border-yellow-500/30">
          <Badge className="text-[10px] bg-yellow-500/15 text-yellow-600 border-yellow-500/30">
            File list truncated — more entries exist
          </Badge>
        </div>
      )}
      <div className="flex-1 overflow-auto">
        <ItemList items={items} searchable title="Files" />
      </div>
    </div>
  );
};

registerToolRenderer('list_files', {
  component: ListFilesRenderer,
  label: 'File List',
  icon: FolderOpen,
});

export default ListFilesRenderer;
