import React from 'react';
import { MessageSquare } from 'lucide-react';
import type { MessageBlockReference } from '@/types';

interface ReferencedMessageChipsProps {
  references?: MessageBlockReference[];
  pinnedMessageReferenceIdSet?: Set<string>;
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
  className?: string;
}

const ReferencedMessageChips: React.FC<ReferencedMessageChipsProps> = ({
  references = [],
  pinnedMessageReferenceIdSet,
  onToggleMessageReference,
  className,
}) => {
  if (references.length === 0) return null;

  const canToggleReferences = typeof onToggleMessageReference === 'function';

  return (
    <div className={className}>
      <div className="flex min-w-0 max-w-full flex-wrap gap-1.5">
        {references.map((reference) => {
          const isReferenced =
            pinnedMessageReferenceIdSet?.has(reference.id) || false;
          const title =
            reference.preview || reference.contentSnapshot || reference.title;
          return (
            <button
              key={reference.key || reference.id}
              type="button"
              disabled={!canToggleReferences}
              onClick={() => onToggleMessageReference?.(reference)}
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
              <MessageSquare
                size={14}
                className="text-muted-foreground shrink-0 opacity-90"
              />
              <span className="min-w-0 truncate max-w-[220px]">
                {reference.label || reference.title || reference.id}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
};

export default ReferencedMessageChips;
