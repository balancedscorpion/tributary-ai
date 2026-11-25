import { FileText } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import SyntaxHighlighter from '../shared/SyntaxHighlighter';
import CopyButton from '../shared/CopyButton';
import FileIcon from '../shared/FileIcon';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';

const ReadFileRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const raw = artifact.metadata?.raw_result as
    | { size?: number; has_more?: boolean }
    | undefined;
  const filePath = artifact.title || 'unknown';
  const fileName = filePath.split('/').pop() || filePath;

  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border min-w-0">
        <FileIcon filename={fileName} size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs font-mono text-foreground truncate" title={filePath}>
          {filePath}
        </span>
        {artifact.language && (
          <Badge variant="secondary" className="text-[10px] font-mono shrink-0">
            {artifact.language}
          </Badge>
        )}
        {raw?.size != null && (
          <Badge variant="outline" className="text-[10px] shrink-0">
            {raw.size >= 1024
              ? `${(raw.size / 1024).toFixed(1)} KB`
              : `${raw.size} B`}
          </Badge>
        )}
        {raw?.has_more === true && (
          <Badge className="text-[10px] shrink-0 bg-yellow-500/15 text-yellow-600 border-yellow-500/30">
            Truncated
          </Badge>
        )}
        <div className="ml-auto shrink-0">
          <CopyButton text={artifact.content} />
        </div>
      </div>

      {/* Body */}
      <div className="flex-1 overflow-auto">
        <SyntaxHighlighter
          code={artifact.content}
          language={artifact.language || 'text'}
          showLineNumbers
        />
      </div>
    </div>
  );
};

registerToolRenderer('read_file', {
  component: ReadFileRenderer,
  label: 'File Contents',
  icon: FileText,
});

export default ReadFileRenderer;
