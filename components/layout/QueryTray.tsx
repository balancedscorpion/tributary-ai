import React, { useMemo } from 'react';
import { Database } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { ScrollArea } from '@/components/ui/scroll-area';
import { Separator } from '@/components/ui/separator';
import QueryTrayItem from '@/components/layout/QueryTrayItem';
import { getSharedQueryStatusKey, useSharedQueryStatuses } from '@/hooks/useSharedQueryStatus';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';
import type { TrackedQuery } from '@/types';

interface QueryTrayProps {
  runningQueries: TrackedQuery[];
  recentQueries: TrackedQuery[];
  onNavigateToQuery: (query: TrackedQuery) => void;
  onCancelQuery: (jobId: string, ownerToken?: string, threadId?: string) => void;
  onOpenArtifact: (artifactId: string) => void;
  onClose: () => void;
}

const QueryTray: React.FC<QueryTrayProps> = ({
  runningQueries,
  recentQueries,
  onNavigateToQuery,
  onCancelQuery,
  onOpenArtifact,
  onClose,
}) => {
  const totalRunning = runningQueries.length;
  const isEmpty = runningQueries.length === 0 && recentQueries.length === 0;
  const pollTargets = useMemo(() => {
    const byKey = new Map<string, {
      jobId: string;
      threadId?: string;
      runId?: string;
      ownerToken?: string;
      queryObligationId?: string;
      startedAtMs: number;
    }>();
    for (const query of runningQueries) {
      const jobId = normalizeManagedQueryJobId(query.jobId);
      const threadId = String(query.threadId || '').trim();
      if (!jobId || !threadId) continue;
      const key = getSharedQueryStatusKey(
        jobId,
        threadId,
        query.runId,
        query.queryObligationId,
      );
      if (!key || byKey.has(key)) continue;
      const ownerToken = String(query.ownerToken || '').trim();
      byKey.set(key, {
        jobId,
        threadId,
        runId: query.runId,
        ownerToken: ownerToken || undefined,
        queryObligationId: query.queryObligationId,
        startedAtMs: query.startedAtMs,
      });
    }
    return Array.from(byKey.values());
  }, [runningQueries]);
  const liveStatusByKey = useSharedQueryStatuses(pollTargets);

  const handleNavigate = (query: TrackedQuery) => {
    onNavigateToQuery(query);
    if (query.status === 'completed') {
      onOpenArtifact(query.artifactId);
    }
    onClose();
  };

  const cancelQuery = (query: TrackedQuery) => {
    const jobId = normalizeManagedQueryJobId(query.jobId);
    if (!jobId) return;
    onCancelQuery(jobId, query.ownerToken, query.threadId);
  };

  const liveStatusFor = (query: TrackedQuery) => {
    const key = getSharedQueryStatusKey(
      query.jobId,
      query.threadId || '',
      query.runId,
      query.queryObligationId,
    );
    return key ? liveStatusByKey[key] : undefined;
  };

  return (
    <div>
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <span className="text-sm font-semibold text-foreground">Queries</span>
        {totalRunning > 0 && (
          <Badge
            variant="secondary"
            className="text-[10px] bg-amber-500/10 text-amber-500 border-amber-500/20"
          >
            {totalRunning} running
          </Badge>
        )}
      </div>

      {/* Content */}
      {isEmpty ? (
        <div className="flex flex-col items-center justify-center py-8 px-4 text-muted-foreground">
          <Database size={24} className="mb-2 text-muted-foreground/40" />
          <p className="text-sm">No queries yet</p>
          <p className="text-xs text-muted-foreground/60 mt-1">
            Query status will appear here
          </p>
        </div>
      ) : (
        <ScrollArea className="max-h-[420px]">
          {/* Running section */}
          {runningQueries.length > 0 && (
            <>
              <div className="px-4 py-2 text-[10px] font-semibold text-muted-foreground uppercase tracking-wider bg-muted/30">
                Running
              </div>
              <div className="space-y-2 px-2 py-2">
                {runningQueries.map((q) => (
                  <QueryTrayItem
                    key={q.artifactId}
                    query={q}
                    liveStatus={liveStatusFor(q)}
                    onNavigate={() => handleNavigate(q)}
                    onCancel={normalizeManagedQueryJobId(q.jobId) ? () => cancelQuery(q) : undefined}
                  />
                ))}
              </div>
            </>
          )}

          {/* Separator between sections */}
          {runningQueries.length > 0 && recentQueries.length > 0 && (
            <Separator />
          )}

          {/* Recent section */}
          {recentQueries.length > 0 && (
            <>
              <div className="px-4 py-2 text-[10px] font-semibold text-muted-foreground uppercase tracking-wider bg-muted/30">
                Recent
              </div>
              <div className="space-y-2 px-2 py-2">
                {recentQueries.map((q) => (
                  <QueryTrayItem
                    key={q.artifactId}
                    query={q}
                    liveStatus={liveStatusFor(q)}
                    onNavigate={() => handleNavigate(q)}
                  />
                ))}
              </div>
            </>
          )}
        </ScrollArea>
      )}
    </div>
  );
};

export default QueryTray;
