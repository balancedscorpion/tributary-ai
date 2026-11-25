import React, { useState, useMemo } from 'react';
import { Terminal, ChevronDown, ChevronRight } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import SyntaxHighlighter from '../shared/SyntaxHighlighter';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';

interface RlmResult {
  status?: string;
  stdout?: string;
  operations?: number | string;
  evidence_budget?: number;
  evidence_used?: number;
  final_answer?: string;
  error?: string;
}

function getStatusVariant(status?: string): string {
  if (!status) return '';
  const s = status.toLowerCase();
  if (s === 'ok' || s === 'done' || s === 'success' || s === 'completed') {
    return 'bg-emerald-500/15 text-emerald-600 border-emerald-500/30';
  }
  if (s === 'error' || s === 'failed' || s === 'failure') {
    return 'bg-destructive/15 text-destructive border-destructive/30';
  }
  return 'bg-muted text-muted-foreground';
}

const RlmExecRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const [stdoutOpen, setStdoutOpen] = useState(false);

  const result = useMemo<RlmResult | null>(() => {
    const raw = artifact.metadata?.raw_result;
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
      return raw as RlmResult;
    }
    try {
      const parsed = JSON.parse(artifact.content);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as RlmResult;
      }
    } catch {
      // not parseable
    }
    return null;
  }, [artifact]);

  const evidencePercent = useMemo(() => {
    if (!result?.evidence_budget || !result?.evidence_used) return null;
    return Math.min(100, Math.round((result.evidence_used / result.evidence_budget) * 100));
  }, [result]);

  if (!result) {
    return (
      <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
        <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border">
          <Terminal size={14} className="text-muted-foreground" />
          <span className="text-xs font-semibold text-foreground">Execution Result</span>
        </div>
        <pre className="p-4 text-xs font-mono text-foreground whitespace-pre-wrap flex-1 overflow-auto">
          {artifact.content}
        </pre>
      </div>
    );
  }

  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border min-w-0">
        <Terminal size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs font-semibold text-foreground truncate">
          {artifact.title || 'Execution Result'}
        </span>
        {result.status && (
          <Badge variant="outline" className={`text-[10px] shrink-0 ${getStatusVariant(result.status)}`}>
            {result.status}
          </Badge>
        )}
      </div>

      <div className="flex-1 overflow-auto space-y-3 p-3">
        {/* Error */}
        {result.error && (
          <div className="p-3 rounded border border-destructive/20 bg-destructive/5">
            <p className="text-sm text-destructive whitespace-pre-wrap break-words">{result.error}</p>
          </div>
        )}

        {/* Final answer */}
        {result.final_answer && (
          <div className="p-3 rounded border border-accent bg-accent/10">
            <p className="text-[10px] text-muted-foreground uppercase tracking-wider font-semibold mb-1">
              Result
            </p>
            <p className="text-sm text-foreground whitespace-pre-wrap break-words">
              {result.final_answer}
            </p>
          </div>
        )}

        {/* Operations */}
        {result.operations != null && (
          <p className="text-xs text-muted-foreground">
            Operations: <span className="font-mono text-foreground">{result.operations}</span>
          </p>
        )}

        {/* Evidence budget progress */}
        {evidencePercent !== null && (
          <div>
            <div className="flex items-center justify-between text-xs text-muted-foreground mb-1">
              <span>Evidence used</span>
              <span className="font-mono">{result.evidence_used} / {result.evidence_budget}</span>
            </div>
            <div className="h-2 w-full rounded-full bg-muted overflow-hidden">
              <div
                className="h-full rounded-full bg-accent transition-all"
                style={{ width: `${evidencePercent}%` }}
              />
            </div>
          </div>
        )}

        {/* Stdout collapsible */}
        {result.stdout && (
          <div className="border border-border rounded overflow-hidden">
            <button
              type="button"
              onClick={() => setStdoutOpen(!stdoutOpen)}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors w-full text-left bg-muted/30"
            >
              {stdoutOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
              Output
            </button>
            {stdoutOpen && (
              <div className="max-h-[300px] overflow-auto">
                <SyntaxHighlighter code={result.stdout} language="bash" />
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
};

registerToolRenderer('rlm_exec', {
  component: RlmExecRenderer,
  label: 'Execution Result',
  icon: Terminal,
});

export default RlmExecRenderer;
