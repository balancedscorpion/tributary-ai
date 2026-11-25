import React from 'react';
import { Brain } from 'lucide-react';
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion';

interface ThinkingTraceProps {
  thinking: string;
  isComplete: boolean;
  stateKey?: string;
}

const thinkingTraceOpenStateByKey = new Map<string, boolean>();

function normalizeStateKey(value?: string): string {
  return typeof value === 'string' ? value.trim() : '';
}

function getInitialOpenState(stateKey: string): boolean {
  if (!stateKey) return true;
  const existing = thinkingTraceOpenStateByKey.get(stateKey);
  if (typeof existing === 'boolean') return existing;
  thinkingTraceOpenStateByKey.set(stateKey, true);
  return true;
}

const ThinkingTrace: React.FC<ThinkingTraceProps> = ({ thinking, isComplete, stateKey }) => {
  const normalizedStateKey = normalizeStateKey(stateKey);
  const [isOpen, setIsOpen] = React.useState<boolean>(() =>
    getInitialOpenState(normalizedStateKey),
  );

  React.useEffect(() => {
    setIsOpen(getInitialOpenState(normalizedStateKey));
  }, [normalizedStateKey]);

  const handleValueChange = React.useCallback(
    (value: string) => {
      const nextOpen = value === "thinking";
      setIsOpen(nextOpen);
      if (normalizedStateKey) {
        thinkingTraceOpenStateByKey.set(normalizedStateKey, nextOpen);
      }
    },
    [normalizedStateKey],
  );

  if (!thinking) return null;

  return (
    <Accordion
      type="single"
      collapsible
      value={isOpen ? "thinking" : undefined}
      onValueChange={handleValueChange}
      className="my-2 min-w-0 max-w-full"
    >
      <AccordionItem
        value="thinking"
        className={`border rounded-lg overflow-hidden min-w-0 max-w-full ${
          isComplete ? 'border-border' : 'border-border/60 animate-thinking-pulse'
        }`}
      >
        <AccordionTrigger className="px-3 py-2 text-xs hover:no-underline hover:bg-muted/50">
          <div className="flex min-w-0 items-center gap-2">
            <Brain size={14} className={`text-white ${!isComplete ? 'animate-pulse' : ''}`} />
            <span className="text-white font-medium">
              {isComplete ? 'Thought process' : 'Thinking...'}
            </span>
          </div>
        </AccordionTrigger>
        <AccordionContent className="px-3 pb-3">
          <p className="chat-text-wrap text-xs text-white italic whitespace-pre-wrap leading-relaxed">
            {thinking}
          </p>
        </AccordionContent>
      </AccordionItem>
    </Accordion>
  );
};

export default ThinkingTrace;
