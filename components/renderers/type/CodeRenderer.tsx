import { Code2 } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import SyntaxHighlighter from '../shared/SyntaxHighlighter';
import CopyButton from '../shared/CopyButton';
import { registerTypeRenderer } from '../registry';
import type { RendererProps } from '../types';

const CodeRenderer: React.FC<RendererProps> = ({ artifact }) => {
  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      <div className="flex items-center justify-between px-3 py-1.5 bg-muted/50 border-b border-border">
        <Badge variant="secondary" className="text-[10px] font-mono">
          {artifact.language || 'code'}
        </Badge>
        <CopyButton text={artifact.content} />
      </div>
      <div className="flex-1 overflow-auto">
        <SyntaxHighlighter code={artifact.content} language={artifact.language || 'text'} />
      </div>
    </div>
  );
};

registerTypeRenderer('code', {
  component: CodeRenderer,
  label: 'Source Code',
  icon: Code2,
});

export default CodeRenderer;
