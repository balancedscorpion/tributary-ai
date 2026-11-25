import React, { useState } from 'react';
import { Database } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { useQueryMonitor } from '@/hooks/useQueryMonitor';
import QueryTray from '@/components/layout/QueryTray';
import type { Artifact, TrackedQuery } from '@/types';

interface QueryBadgeProps {
  artifactsById: Record<string, Artifact>;
  onNavigateToQuery: (query: TrackedQuery) => void;
  onCancelQuery: (jobId: string, ownerToken?: string, threadId?: string) => void;
  onOpenArtifact: (artifactId: string) => void;
}

const QueryBadge: React.FC<QueryBadgeProps> = ({
  artifactsById,
  onNavigateToQuery,
  onCancelQuery,
  onOpenArtifact,
}) => {
  const [open, setOpen] = useState(false);
  const { runningQueries, recentQueries, totalRunning } = useQueryMonitor(artifactsById);

  const displayCount = totalRunning > 99 ? '99+' : String(totalRunning);

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <Tooltip>
        <TooltipTrigger asChild>
          <PopoverTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="text-muted-foreground hover:text-foreground relative"
            >
              <Database
                size={20}
                className={totalRunning > 0 ? 'text-amber-500' : ''}
              />
              {totalRunning > 0 && (
                <span className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-amber-500 text-[9px] font-bold text-white animate-pulse px-0.5">
                  {displayCount}
                </span>
              )}
            </Button>
          </PopoverTrigger>
        </TooltipTrigger>
        <TooltipContent>Query monitor</TooltipContent>
      </Tooltip>
      <PopoverContent align="end" sideOffset={8} className="w-[380px] p-0 overflow-hidden">
        <QueryTray
          runningQueries={runningQueries}
          recentQueries={recentQueries}
          onNavigateToQuery={onNavigateToQuery}
          onCancelQuery={onCancelQuery}
          onOpenArtifact={onOpenArtifact}
          onClose={() => setOpen(false)}
        />
      </PopoverContent>
    </Popover>
  );
};

export default QueryBadge;
