import React, { useEffect, useMemo, useState } from 'react';
import { Ban, ChevronRight, Info, RefreshCw, TriangleAlert } from 'lucide-react';
import {
  Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import type { ProviderConfig } from '@/types';
import type {
  PlanChoiceIssue,
  PlanChoiceScope,
  PlanChoiceSelection,
  PlanIssue,
  PreflightResult,
  ThreadTarget,
} from '@/lib/capability/types';
import TargetPicker from './TargetPicker';

export type PreflightGateDecision =
  | { action: 'cancel' }
  | { action: 'reflight' }
  | { action: 'remove_attachment'; attachmentId: string }
  | { action: 'send'; selections: Record<string, PlanChoiceSelection> };

const SCOPE_LABELS: Record<PlanChoiceScope, string> = {
  run: 'This run',
  thread: 'This conversation',
  connection: 'This connection',
};

interface PreflightSheetProps {
  open: boolean;
  result: PreflightResult | null;
  connections: ProviderConfig[];
  currentTarget: ThreadTarget | null;
  onResolve: (decision: PreflightGateDecision) => void;
  onOpenPermissions?: () => void;
  /** Persist a target switch; on success the sheet re-preflights. */
  onSwitchTarget?: (target: ThreadTarget) => Promise<boolean>;
}

function FixActions({
  issue, onResolve, onOpenPermissions, onOpenSwitcher,
}: {
  issue: PlanIssue;
  onResolve: (d: PreflightGateDecision) => void;
  onOpenPermissions?: () => void;
  onOpenSwitcher?: () => void;
}) {
  const kind = issue.fix?.kind;
  if (!kind) return null;
  if (kind === 'open_permissions') {
    return onOpenPermissions ? (
      <Button variant="outline" size="sm" className="h-6 px-2 text-[10px]" onClick={onOpenPermissions}>
        Open Permissions
      </Button>
    ) : null;
  }
  if (kind === 'remove_attachment' && issue.attachmentId) {
    return (
      <Button
        variant="outline"
        size="sm"
        className="h-6 px-2 text-[10px]"
        onClick={() => onResolve({ action: 'remove_attachment', attachmentId: issue.attachmentId! })}
      >
        Remove attachment
      </Button>
    );
  }
  if (kind === 'switch_model') {
    return onOpenSwitcher ? (
      <Button variant="outline" size="sm" className="h-6 px-2 text-[10px]" onClick={onOpenSwitcher}>
        Switch model
      </Button>
    ) : null;
  }
  if (kind === 'recheck') {
    return (
      <Button variant="outline" size="sm" className="h-6 gap-1 px-2 text-[10px]" onClick={() => onResolve({ action: 'reflight' })}>
        <RefreshCw size={10} aria-hidden /> Re-check
      </Button>
    );
  }
  return null;
}

/**
 * Consolidated pre-send sheet (plan §4.4). One dialog per send attempt, ordered
 * Blockers → Choices → Notices. Cancel persists nothing; "Send with choices"
 * submits selections keyed by issueId. Nothing is silently persisted: scope
 * defaults to "This run" and wider scopes are an explicit user selection.
 */
const PreflightSheet: React.FC<PreflightSheetProps> = ({
  open, result, connections, currentTarget, onResolve, onOpenPermissions, onSwitchTarget,
}) => {
  const [selections, setSelections] = useState<Record<string, PlanChoiceSelection>>({});
  const [switcherOpen, setSwitcherOpen] = useState(false);

  // Reset per plan: default every choice to its first option at run scope.
  useEffect(() => {
    if (!result) return;
    const next: Record<string, PlanChoiceSelection> = {};
    for (const choice of result.choices) {
      const firstOption = choice.options[0];
      if (!firstOption) continue;
      const scope: PlanChoiceScope = choice.allowedScopes.includes('run') ? 'run' : choice.allowedScopes[0];
      next[choice.issueId] = { optionId: firstOption.id, scope };
    }
    setSelections(next);
    setSwitcherOpen(false);
  }, [result?.planId]);

  const blocked = (result?.blockers.length ?? 0) > 0;
  const allChosen = useMemo(
    () => (result ? result.choices.every((c) => Boolean(selections[c.issueId])) : false),
    [result, selections],
  );

  if (!result) return null;

  const setChoice = (choice: PlanChoiceIssue, patch: Partial<PlanChoiceSelection>) => {
    setSelections((prev) => {
      const current = prev[choice.issueId] || {
        optionId: choice.options[0]?.id || '',
        scope: (choice.allowedScopes.includes('run') ? 'run' : choice.allowedScopes[0]) as PlanChoiceScope,
      };
      return { ...prev, [choice.issueId]: { ...current, ...patch } };
    });
  };

  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onResolve({ action: 'cancel' }); }}>
      <DialogContent
        className="max-w-lg"
        role="alertdialog"
        aria-describedby="preflight-sheet-desc"
        onCloseAutoFocus={(event) => {
          // The sheet opens programmatically (no trigger element), so Radix
          // has nothing to restore focus to. Return keyboard users to the
          // composer their send started from — the draft is still there.
          event.preventDefault();
          document.getElementById('chat-composer-input')?.focus();
        }}
      >
        <DialogHeader>
          <DialogTitle>Before sending</DialogTitle>
          <DialogDescription id="preflight-sheet-desc">
            {blocked
              ? 'This message can’t be sent as-is. Resolve the items below or cancel — your draft is kept.'
              : 'A few decisions are needed before this message is sent. Your draft is kept until you choose.'}
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3 max-h-[55vh] overflow-y-auto pr-1">
          {result.blockers.length > 0 && (
            <div role="group" aria-label="Blockers" className="space-y-3">
              {result.blockers.map((issue) => (
                <div
                  key={issue.issueId}
                  data-preflight-blocker={issue.issueId}
                  className="rounded-lg border border-destructive/30 bg-destructive/5 p-2.5 space-y-1.5"
                >
                  <div className="flex items-start gap-1.5 text-xs">
                    <Ban size={13} className="mt-0.5 shrink-0 text-destructive" aria-hidden />
                    <span className="min-w-0">
                      <span className="font-medium text-destructive">Blocker — </span>
                      {issue.text}
                    </span>
                  </div>
                  <div className="flex items-center gap-1.5 pl-5 flex-wrap">
                    <FixActions
                      issue={issue}
                      onResolve={onResolve}
                      onOpenPermissions={onOpenPermissions}
                      onOpenSwitcher={() => setSwitcherOpen((v) => !v)}
                    />
                  </div>
                </div>
              ))}
            </div>
          )}

          {switcherOpen && onSwitchTarget && (
            <div className="rounded-lg border border-border bg-card/50">
              <TargetPicker
                connections={connections}
                currentTarget={currentTarget}
                onSelect={(target) => {
                  void onSwitchTarget(target).then((ok) => {
                    if (ok) {
                      setSwitcherOpen(false);
                      onResolve({ action: 'reflight' });
                    }
                  });
                }}
              />
            </div>
          )}

          {result.choices.length > 0 && (
          <div role="group" aria-label="Choices" className="space-y-3">
          {result.choices.map((choice) => {
            const selection = selections[choice.issueId];
            return (
              <div
                key={choice.issueId}
                data-preflight-choice={choice.issueId}
                className="rounded-lg border border-amber-500/30 bg-amber-500/5 p-2.5 space-y-2"
              >
                <div className="flex items-start gap-1.5 text-xs">
                  <TriangleAlert size={13} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
                  <span className="min-w-0">{choice.text}</span>
                </div>
                <div role="radiogroup" aria-label={`Options: ${choice.text}`} className="space-y-1 pl-5">
                  {choice.options.map((option) => {
                    const selected = selection?.optionId === option.id;
                    return (
                      <label key={option.id} className="flex items-start gap-2 cursor-pointer text-[11px] min-h-6">
                        <input
                          type="radio"
                          name={`choice-${choice.issueId}`}
                          className="mt-0.5 accent-current"
                          checked={selected}
                          onChange={() => setChoice(choice, { optionId: option.id })}
                        />
                        <span className="min-w-0">
                          <span className="text-foreground/90">{option.label}</span>
                          {option.consequence && (
                            <span className="block text-muted-foreground">{option.consequence}</span>
                          )}
                        </span>
                      </label>
                    );
                  })}
                </div>
                {choice.allowedScopes.length > 1 && (
                  <div className="flex items-center gap-2 pl-5 flex-wrap">
                    <span className="text-[10px] text-muted-foreground">Apply to:</span>
                    <div role="radiogroup" aria-label={`Scope: ${choice.text}`} className="flex items-center gap-1 flex-wrap">
                      {choice.allowedScopes.map((scope) => {
                        const active = selection?.scope === scope;
                        return (
                          <button
                            key={scope}
                            type="button"
                            role="radio"
                            aria-checked={active}
                            data-preflight-scope={`${choice.issueId}:${scope}`}
                            className={`rounded-md border px-2 py-0.5 min-h-6 text-[10px] transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring ${
                              active
                                ? 'border-foreground/40 text-foreground bg-card'
                                : 'border-border text-muted-foreground hover:text-foreground'
                            }`}
                            onClick={() => setChoice(choice, { scope })}
                          >
                            {SCOPE_LABELS[scope]}
                          </button>
                        );
                      })}
                    </div>
                  </div>
                )}
              </div>
            );
          })}
          </div>
          )}

          {result.notices.length > 0 && (
          <div role="group" aria-label="Notices" className="space-y-3">
          {result.notices.map((issue) => (
            <div
              key={issue.issueId}
              data-preflight-notice={issue.issueId}
              className="flex items-start gap-1.5 rounded-lg border border-border bg-card/40 p-2.5 text-[11px] text-muted-foreground"
            >
              <Info size={12} className="mt-0.5 shrink-0" aria-hidden />
              <span className="min-w-0">{issue.text}</span>
            </div>
          ))}
          </div>
          )}

          {result.staleEvidence && (
            <div className="flex items-center justify-between gap-2 border-t border-border pt-2 text-[11px] text-amber-500">
              <span>Capability evidence for this connection is stale.</span>
              <Button
                variant="outline"
                size="sm"
                className="h-6 gap-1 px-2 text-[10px]"
                onClick={() => onResolve({ action: 'reflight' })}
              >
                <RefreshCw size={10} aria-hidden /> Re-check
              </Button>
            </div>
          )}
        </div>

        <DialogFooter className="gap-2 sm:gap-2">
          <Button variant="outline" size="sm" onClick={() => onResolve({ action: 'cancel' })}>
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={blocked || !allChosen}
            aria-describedby={blocked ? 'preflight-send-blocked-reason' : undefined}
            onClick={() => onResolve({ action: 'send', selections })}
            className="gap-1"
          >
            Send with choices <ChevronRight size={13} aria-hidden />
          </Button>
          {blocked && (
            <span id="preflight-send-blocked-reason" className="sr-only">
              Sending is disabled while blockers remain.
            </span>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

export default PreflightSheet;
