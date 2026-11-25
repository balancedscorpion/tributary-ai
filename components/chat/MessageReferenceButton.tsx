import React from 'react';
import { MessageSquarePlus, MessageSquareDashed } from 'lucide-react';

interface MessageReferenceButtonProps {
  isReferenced?: boolean;
  onToggle: () => void;
  className?: string;
}

const MessageReferenceButton: React.FC<MessageReferenceButtonProps> = ({ isReferenced, onToggle, className }) => {
  return (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        onToggle();
      }}
      aria-label={isReferenced ? 'Remove reference' : 'Reference in chat'}
      className={`group/pin shrink-0 inline-flex items-center gap-1 rounded-md px-1.5 py-1 transition-all touch-manipulation focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50 focus-visible:ring-offset-1 focus-visible:ring-offset-background ${
        isReferenced
          ? 'text-sky-400 opacity-100'
          : 'text-muted-foreground/30 hover:text-muted-foreground/60 opacity-100'
      } ${className || ''}`}
    >
      {isReferenced ? <MessageSquareDashed size={14} /> : <MessageSquarePlus size={14} />}
      <span className={`text-[10px] transition-all overflow-hidden ${
        isReferenced
          ? 'max-w-[60px] opacity-100'
          : 'max-w-0 opacity-0 group-hover/pin:max-w-[60px] group-hover/pin:opacity-100'
      }`}>
        {isReferenced ? 'Remove' : 'Reference'}
      </span>
    </button>
  );
};

export default MessageReferenceButton;
