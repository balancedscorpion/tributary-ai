import React, { useRef } from 'react';
import { ChevronDown, Loader2 } from 'lucide-react';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { cn } from '@/lib/utils';
import type { ApprovalScope, PermissionSubject } from '@/types/permissions';
import { isComputerWideLocalSubject, SCOPE_META, scopeLabel } from '@/lib/approvalPresentation';

/** Narrowest-first ordering; the primary action is always the narrowest offered. */
const SCOPE_ORDER: ApprovalScope[] = ['once', 'thread', 'always'];

export function orderedScopes(options: ApprovalScope[]): ApprovalScope[] {
  return SCOPE_ORDER.filter((scope) => options.includes(scope));
}

interface ApprovalScopeSelectorProps {
  /** Scopes the backend offered for this request; may be any subset. */
  options: ApprovalScope[];
  subject?: PermissionSubject;
  /** A decision request is in flight (buttons stay focusable — aria-disabled). */
  busy: boolean;
  /** The scope whose request is in flight, for spinner placement. */
  busyScope?: ApprovalScope | null;
  onSelect: (scope: ApprovalScope) => void;
  /**
   * Called instead of Radix's default return-focus when a menu selection is
   * about to resolve (and unmount) this card — the caller routes focus to a
   * surviving element.
   */
  onResolvedMenuClose?: () => void;
  className?: string;
}

/**
 * Split button: primary = narrowest offered scope, one click. Wider scopes
 * live behind a separately-focusable chevron ("More allow options") so an
 * accidental broad grant takes two deliberate steps.
 */
const ApprovalScopeSelector: React.FC<ApprovalScopeSelectorProps> = ({
  options,
  subject,
  busy,
  busyScope,
  onSelect,
  onResolvedMenuClose,
  className,
}) => {
  const scopes = orderedScopes(options);
  const menuSelectionRef = useRef(false);
  if (scopes.length === 0) return null;
  const [primary, ...wider] = scopes;
  const visibleAlways = wider.includes('always') ? 'always' : null;
  const menuScopes = visibleAlways
    ? wider.filter((scope) => scope !== visibleAlways)
    : wider;

  const guardedSelect = (scope: ApprovalScope) => {
    if (busy) return;
    onSelect(scope);
  };

  const primaryLabel = scopeLabel(primary, subject);
  const primaryBusy = busy && busyScope === primary;

  const segmentBase =
    'inline-flex items-center justify-center gap-1.5 text-[13px] font-medium ' +
    'bg-primary text-primary-foreground transition-colors ' +
    'hover:bg-primary/90 aria-disabled:opacity-70 aria-disabled:hover:bg-primary ' +
    'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:z-10';
  const secondaryBase =
    'inline-flex h-9 min-w-0 items-center justify-center gap-1.5 rounded-md border border-border bg-background px-3.5 ' +
    'text-[13px] font-medium text-foreground transition-colors hover:bg-muted ' +
    'aria-disabled:opacity-70 aria-disabled:hover:bg-background focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring';

  return (
    <div className={cn('flex w-full flex-col gap-2 sm:w-auto sm:flex-row', className)}>
      <div className="flex w-full sm:w-auto">
        <button
          type="button"
          onClick={() => guardedSelect(primary)}
          aria-disabled={busy || undefined}
          aria-busy={primaryBusy || undefined}
          className={cn(
            segmentBase,
            'h-9 min-w-0 flex-1 sm:flex-none px-3.5',
            menuScopes.length > 0 ? 'rounded-l-md' : 'rounded-md',
          )}
        >
          {primaryBusy && (
            <Loader2 size={14} className="animate-spin motion-reduce:animate-none" aria-hidden />
          )}
          <span className="truncate">{primaryLabel}</span>
        </button>
        {menuScopes.length > 0 && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <button
                type="button"
                aria-label="More allow options"
                aria-disabled={busy || undefined}
                className={cn(
                  segmentBase,
                  'h-9 w-9 shrink-0 rounded-r-md border-l border-primary-foreground/25',
                )}
                onClick={(e) => {
                  if (busy) {
                    e.preventDefault();
                    e.stopPropagation();
                  }
                }}
              >
                {busy && busyScope && busyScope !== primary && busyScope !== visibleAlways ? (
                  <Loader2 size={14} className="animate-spin motion-reduce:animate-none" aria-hidden />
                ) : (
                  <ChevronDown size={15} aria-hidden />
                )}
              </button>
            </DropdownMenuTrigger>
            <DropdownMenuContent
              align="end"
              className="w-72 max-w-[calc(100vw-2rem)]"
              onCloseAutoFocus={(event) => {
                if (menuSelectionRef.current) {
                  // The selection resolves the card; the trigger is about to
                  // unmount. Let the caller move focus somewhere that survives.
                  menuSelectionRef.current = false;
                  event.preventDefault();
                  onResolvedMenuClose?.();
                }
              }}
            >
              {menuScopes.map((scope) => (
                <DropdownMenuItem
                  key={scope}
                  className="flex flex-col items-start gap-0.5 py-2"
                  onSelect={() => {
                    menuSelectionRef.current = true;
                    guardedSelect(scope);
                  }}
                >
                  <span className="text-[13px] font-medium">{scopeLabel(scope, subject)}</span>
                  <span className="text-[12px] text-muted-foreground">
                    {scopeConsequence(scope, subject)}
                  </span>
                </DropdownMenuItem>
              ))}
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </div>
      {visibleAlways && (
        <button
          type="button"
          onClick={() => guardedSelect(visibleAlways)}
          aria-disabled={busy || undefined}
          aria-busy={busy && busyScope === visibleAlways || undefined}
          className={secondaryBase}
        >
          {busy && busyScope === visibleAlways && (
            <Loader2 size={14} className="animate-spin motion-reduce:animate-none" aria-hidden />
          )}
          <span className="truncate">{scopeLabel(visibleAlways, subject)}</span>
        </button>
      )}
    </div>
  );
};

function scopeConsequence(scope: ApprovalScope, subject?: PermissionSubject): string {
  if (subject?.kind === 'local_location' && isComputerWideLocalSubject(subject)) {
    if (scope === 'thread') {
      return 'Won’t ask before searching this computer again in this conversation';
    }
    if (scope === 'always') {
      return 'Won’t ask before searching this computer again — change anytime in Settings → Permissions';
    }
  }
  if (scope === 'thread' && subject?.kind === 'local_location') {
    return subject.local_kind === 'file'
      ? 'Won’t ask before reading this file again in this conversation'
      : 'Won’t ask before reading this folder again in this conversation';
  }
  if (scope === 'always' && subject?.kind === 'local_location') {
    return subject.local_kind === 'file'
      ? 'Won’t ask before reading this file again — change anytime in Settings → Permissions'
      : 'Won’t ask before reading this folder again — change anytime in Settings → Permissions';
  }
  return SCOPE_META[scope].consequence;
}

export default ApprovalScopeSelector;
