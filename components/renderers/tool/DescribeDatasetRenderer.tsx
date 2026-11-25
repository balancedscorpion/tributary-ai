import React, { useMemo } from 'react';
import { FileBarChart, FileText, Hash, Columns } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import StatsCard from '../shared/StatsCard';
import StatsRow from '../shared/StatsRow';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';

interface ColumnProfile {
  name: string;
  type?: string;
  nulls?: number | string;
  min?: number | string;
  max?: number | string;
  mean?: number | string;
  cardinality?: number | string;
}

interface DatasetProfile {
  path?: string;
  row_count?: number;
  column_count?: number;
  columns?: ColumnProfile[];
}

const TYPE_COLORS: Record<string, string> = {
  string: 'bg-blue-500/15 text-blue-600 border-blue-500/30',
  integer: 'bg-emerald-500/15 text-emerald-600 border-emerald-500/30',
  int: 'bg-emerald-500/15 text-emerald-600 border-emerald-500/30',
  float: 'bg-amber-500/15 text-amber-600 border-amber-500/30',
  double: 'bg-amber-500/15 text-amber-600 border-amber-500/30',
  boolean: 'bg-purple-500/15 text-purple-600 border-purple-500/30',
  date: 'bg-pink-500/15 text-pink-600 border-pink-500/30',
  timestamp: 'bg-pink-500/15 text-pink-600 border-pink-500/30',
  datetime: 'bg-pink-500/15 text-pink-600 border-pink-500/30',
};

function getTypeColorClass(type?: string): string {
  if (!type) return '';
  const lower = type.toLowerCase();
  for (const [key, cls] of Object.entries(TYPE_COLORS)) {
    if (lower.includes(key)) return cls;
  }
  return 'bg-muted text-muted-foreground';
}

function fmtCell(v: number | string | undefined): string {
  if (v === undefined || v === null) return '-';
  if (typeof v === 'number') {
    return Number.isInteger(v) ? v.toLocaleString() : v.toFixed(2);
  }
  return String(v);
}

const DescribeDatasetRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const profile = useMemo<DatasetProfile | null>(() => {
    const raw = artifact.metadata?.raw_result;
    if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
      return raw as DatasetProfile;
    }
    try {
      const parsed = JSON.parse(artifact.content);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
        return parsed as DatasetProfile;
      }
    } catch {
      // not parseable
    }
    return null;
  }, [artifact]);

  if (!profile || !profile.columns?.length) {
    return (
      <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
        <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border">
          <FileBarChart size={14} className="text-muted-foreground" />
          <span className="text-xs font-semibold text-foreground">Dataset Profile</span>
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
        <FileBarChart size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs font-semibold text-foreground truncate">
          {artifact.title || 'Dataset Profile'}
        </span>
        {profile.columns && (
          <Badge variant="secondary" className="text-[10px] shrink-0">
            {profile.columns.length} column{profile.columns.length !== 1 ? 's' : ''}
          </Badge>
        )}
      </div>

      {/* Stats */}
      <div className="px-3 pt-3">
        <StatsRow>
          {profile.path && (
            <StatsCard icon={<FileText size={14} />} label="Path" value={profile.path} />
          )}
          {profile.row_count != null && (
            <StatsCard icon={<Hash size={14} />} label="Rows" value={profile.row_count.toLocaleString()} />
          )}
          {profile.column_count != null && (
            <StatsCard icon={<Columns size={14} />} label="Columns" value={profile.column_count} />
          )}
        </StatsRow>
      </div>

      {/* Column profiles table */}
      <div className="flex-1 overflow-auto mt-3">
        <table className="w-full text-xs border-collapse">
          <thead className="sticky top-0 z-10">
            <tr className="border-b border-border bg-muted">
              <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-3 py-2">Name</th>
              <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-3 py-2">Type</th>
              <th className="text-right text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-3 py-2">Nulls</th>
              <th className="text-right text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-3 py-2">Min</th>
              <th className="text-right text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-3 py-2">Max</th>
              <th className="text-right text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-3 py-2">Mean</th>
              <th className="text-right text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-3 py-2">Cardinality</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-border/50">
            {profile.columns.map((col, i) => (
              <tr key={i} className="hover:bg-muted/30 transition-colors">
                <td className="px-3 py-2 font-mono font-medium text-foreground">{col.name}</td>
                <td className="px-3 py-2">
                  {col.type ? (
                    <Badge variant="outline" className={`text-[10px] px-1.5 py-0 ${getTypeColorClass(col.type)}`}>
                      {col.type}
                    </Badge>
                  ) : '-'}
                </td>
                <td className="px-3 py-2 text-right font-mono text-muted-foreground">{fmtCell(col.nulls)}</td>
                <td className="px-3 py-2 text-right font-mono text-muted-foreground">{fmtCell(col.min)}</td>
                <td className="px-3 py-2 text-right font-mono text-muted-foreground">{fmtCell(col.max)}</td>
                <td className="px-3 py-2 text-right font-mono text-muted-foreground">{fmtCell(col.mean)}</td>
                <td className="px-3 py-2 text-right font-mono text-muted-foreground">{fmtCell(col.cardinality)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
};

registerToolRenderer('describe_dataset', {
  component: DescribeDatasetRenderer,
  label: 'Dataset Profile',
  icon: FileBarChart,
});

export default DescribeDatasetRenderer;
