import React from 'react';
import { Link2 } from 'lucide-react';
import type { ThreadReference } from '@/types';

interface ReferencedThreadChipsProps {
  references?: ThreadReference[];
  pinnedThreadReferenceIdSet?: Set<string>;
  onToggleThreadReference?: (reference: ThreadReference) => void;
  className?: string;
}

const ReferencedThreadChips: React.FC<ReferencedThreadChipsProps> = ({
  references = [],
  pinnedThreadReferenceIdSet,
  onToggleThreadReference,
  className,
}) => {
  if (references.length === 0) return null;

  const canToggleReferences = typeof onToggleThreadReference === 'function';

  return (
    <div className={className}>
      <div className="flex min-w-0 max-w-full flex-wrap gap-1.5">
        {references.map((reference) => {
          const id = String(reference?.id || '').trim();
          if (!id) return null;
          const isReferenced = pinnedThreadReferenceIdSet?.has(id) || false;
          const title = reference.summary || reference.title || id;
          return (
            <button
              key={id}
              type="button"
              disabled={!canToggleReferences}
              onClick={() => onToggleThreadReference?.(reference)}
              className={[
                'group/chip flex max-w-full min-w-0 items-center gap-1.5 rounded-lg px-2.5 py-1.5 text-xs border transition-colors',
                isReferenced
                  ? 'bg-background/40 border-border/70 text-foreground'
                  : 'bg-background/30 border-border/50 text-foreground',
                canToggleReferences
                  ? 'hover:bg-background/40 hover:border-border/70'
                  : 'cursor-default',
              ].join(' ')}
              title={title}
            >
              <Link2
                size={14}
                className="text-muted-foreground shrink-0 opacity-90"
              />
              <span className="min-w-0 truncate max-w-[220px]">
                {reference.title || id}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
};

export default ReferencedThreadChips;
