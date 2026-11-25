import React from 'react';
import { ChevronDown, KeyRound } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import { cn } from '@/lib/utils';
import type { NormalizedActivationStatus } from '@/services/activationApi';
import ActivationPanel from './ActivationPanel';

type LicensingSectionProps = {
  /**
   * Mirrors the Settings dialog "open" state so we can reset internal UI when closed.
   */
  active: boolean;
  onStatus?: (status: NormalizedActivationStatus | null) => void;
};

const LicensingSection: React.FC<LicensingSectionProps> = ({ active, onStatus }) => {
  const [open, setOpen] = React.useState(false);

  React.useEffect(() => {
    if (!active) setOpen(false);
  }, [active]);

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="rounded-xl border bg-card text-card-foreground shadow"
    >
      <div className="flex items-start justify-between gap-4 p-4">
        <div className="space-y-1">
          <div className="flex items-center gap-2 text-sm font-medium">
            <KeyRound size={16} className="text-muted-foreground" />
            Licensing
          </div>
          <div className="text-[10px] text-muted-foreground">
            Manage device activation (Device ID, activation key, remove).
          </div>
        </div>

        <CollapsibleTrigger asChild>
          <Button type="button" variant="outline" size="sm" className="gap-2">
            {open ? 'Hide' : 'Manage'}
            <ChevronDown
              size={16}
              className={cn('transition-transform duration-200', open && 'rotate-180')}
            />
          </Button>
        </CollapsibleTrigger>
      </div>

      <CollapsibleContent className="border-t border-border">
        {open ? (
          <ActivationPanel active={active} onStatus={onStatus} />
        ) : null}
      </CollapsibleContent>
    </Collapsible>
  );
};

export default LicensingSection;

