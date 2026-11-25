import React from 'react';
import { Badge } from '@/components/ui/badge';
import type { QueryMetadata } from '@/types';

interface SchemaViewerProps {
  schema: NonNullable<QueryMetadata['schema']>;
}

const SchemaViewer: React.FC<SchemaViewerProps> = ({ schema }) => {
  if (!schema || schema.length === 0) {
    return (
      <div className="flex items-center justify-center h-full text-sm text-muted-foreground">
        No schema information available.
      </div>
    );
  }

  const hasMode = schema.some(c => c.mode);
  const hasDescription = schema.some(c => c.description);

  return (
    <div className="h-full overflow-auto">
      <table className="w-full text-sm">
        <thead className="sticky top-0 bg-card z-10">
          <tr className="border-b border-border">
            <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-4 py-2">#</th>
            <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-4 py-2">Column Name</th>
            <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-4 py-2">Type</th>
            {hasMode && (
              <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-4 py-2">Mode</th>
            )}
            {hasDescription && (
              <th className="text-left text-[10px] text-muted-foreground uppercase tracking-wider font-semibold px-4 py-2">Description</th>
            )}
          </tr>
        </thead>
        <tbody>
          {schema.map((col, i) => (
            <tr key={i} className="border-b border-border/50 hover:bg-muted/30 transition-colors">
              <td className="px-4 py-2 text-xs text-muted-foreground/60 font-mono">{i + 1}</td>
              <td className="px-4 py-2 text-xs font-mono text-foreground font-medium">{col.name}</td>
              <td className="px-4 py-2 text-xs font-mono text-muted-foreground">{col.type || '-'}</td>
              {hasMode && (
                <td className="px-4 py-2">
                  {col.mode ? (
                    <Badge variant="outline" className="text-[10px] px-1.5 py-0">{col.mode}</Badge>
                  ) : '-'}
                </td>
              )}
              {hasDescription && (
                <td className="px-4 py-2 text-xs text-muted-foreground">{col.description || '-'}</td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
};

export default SchemaViewer;
