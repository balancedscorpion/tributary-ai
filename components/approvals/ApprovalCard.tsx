import React, { useCallback, useMemo, useRef, useState } from 'react';
import {
  AlertTriangle,
  Check,
  ChevronDown,
  CircleSlash,
  Clock,
  FileText,
  FolderOpen,
  HardDrive,
  Loader2,
  MessageSquareText,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible';
import { cn } from '@/lib/utils';
import MarkdownRenderer from '@/components/chat/MarkdownRenderer';
import ApprovalScopeSelector, { orderedScopes } from './ApprovalScopeSelector';
import type { ApprovalRequestUI, ApprovalScope, PermissionSubject } from '@/types/permissions';
import {
  approvalPhrase,
  categoryMeaning,
  CATEGORY_META,
  decidedStatusSentence,
  displayPath,
  isComputerWideLocalSubject,
  isScanBudgetApproval,
  relativeTime,
  scanBudgetMeaning,
  STATUS_META,
  subjectProvenance,
} from '@/lib/approvalPresentation';
import { formatScanBytes } from '@/lib/scanBudget';
import {
  isApprovalConflict,
  isPermissionsUnavailable,
} from '@/services/permissionsApi';
import {
  isLocalPickerCanceled,
  pickLocalDirectory,
  pickLocalFile,
  type LocalDirectoryPick,
  type LocalFilePick,
} from '@/services/localFilePicker';

export type ApprovalCardVariant = 'live' | 'transcript';

export interface ApprovalCardProps {
  approval: ApprovalRequestUI;
  variant: ApprovalCardVariant;
  /** Resolve the decision (approve with scope, or reject). Throws on failure. */
  onDecide: (
    approvalId: string,
    decision: { action: 'approve' | 'reject'; scope?: ApprovalScope; subject?: PermissionSubject },
  ) => Promise<void>;
  /** A decision request for this approval is in flight (from the shared store). */
  busy?: boolean;
  /** Refetch approval state (used after a conflict: decided elsewhere). */
  onRefresh?: () => void;
  /**
   * Live variant: called after this card's decision resolves (or the menu
   * selection resolves it) so the parent can move focus to a surviving
   * element. Never called for SSE-driven changes.
   */
  onDecidedByUser?: (approvalId: string) => void;
  /** Live variant: scroll to / reveal the originating tool card in the transcript. */
  onShowInChat?: (approval: ApprovalRequestUI) => void;
  className?: string;
}

type CardError =
  | { kind: 'conflict'; message: string }
  | { kind: 'retryable'; message: string; retry: { action: 'approve' | 'reject'; scope?: ApprovalScope } };

/**
 * The shared approval request card. One component for the pending strip, the
 * Settings queue ("live") and the durable transcript record ("transcript").
 *
 * Design contract (see docs/persistent-approvals-implementation-plan.md §Front-End UX):
 * - Title is a plain action phrase; internal tool names only under "Technical details".
 * - Primary action = narrowest offered scope; "Don't allow" is plain, never destructive-red.
 * - Rejected/expired/canceled are neutral (a completed decision, not an error).
 * - Busy keeps focus: aria-disabled + aria-busy, never the disabled attribute.
 * - Focus moves only on the user's own action; SSE changes are announce-only.
 */
const ApprovalCard: React.FC<ApprovalCardProps> = ({
  approval,
  variant,
  onDecide,
  busy = false,
  onRefresh,
  onDecidedByUser,
  onShowInChat,
  className,
}) => {
  const phrase = useMemo(() => approvalPhrase(approval), [approval]);
  const category = CATEGORY_META[approval.category];
  const status = STATUS_META[approval.status];
  // Local cards already lead with location language ("Search this computer",
  // the picked path); a "This computer" chip would just repeat it. The chip
  // carries its weight when the subject reaches somewhere else — a connected
  // account, an attachment, a provider-hosted capability.
  const provenance = useMemo(() => {
    const label = subjectProvenance(approval.subject);
    return label === 'This computer' ? null : label;
  }, [approval.subject]);
  const isPending = approval.status === 'pending';
  const [error, setError] = useState<CardError | null>(null);
  const [busyAction, setBusyAction] = useState<'reject' | ApprovalScope | null>(null);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const statusLineRef = useRef<HTMLParagraphElement>(null);
  const PhraseIcon = phrase.icon;
  const CategoryIcon = category.icon;

  const decide = useCallback(
    async (action: 'approve' | 'reject', scope?: ApprovalScope, subject?: PermissionSubject) => {
      if (busy || busyAction) return;
      setError(null);
      setBusyAction(action === 'reject' ? 'reject' : (scope ?? 'once'));
      try {
        await onDecide(approval.id, { action, scope, subject });
        onDecidedByUser?.(approval.id);
        if (variant === 'transcript') {
          // The card stays mounted; land focus on the outcome so keyboard and
          // screen-reader users hear what happened without a live-region echo.
          requestAnimationFrame(() => statusLineRef.current?.focus());
        }
      } catch (e: any) {
        if (isApprovalConflict(e)) {
          setError({
            kind: 'conflict',
            message: 'This request was already decided somewhere else.',
          });
          onRefresh?.();
        } else if (isPermissionsUnavailable(e)) {
          setError({
            kind: 'retryable',
            message: 'The approvals service didn’t respond. Try again.',
            retry: { action, scope },
          });
        } else {
          setError({
            kind: 'retryable',
            message: e?.message
              ? `Couldn’t send your decision: ${e.message}`
              : 'Couldn’t send your decision. Check your connection and try again.',
            retry: { action, scope },
          });
        }
      } finally {
        setBusyAction(null);
      }
    },
    [approval.id, busy, busyAction, onDecide, onDecidedByUser, onRefresh, variant],
  );

  const isBusy = busy || busyAction !== null;
  const decidedCollapsed = variant === 'transcript' && !isPending;
  const isLocalLocationRequest = approval.subject.kind === 'local_location';
  const deferLocalLocationPreview =
    isPending && isLocalLocationRequest && !approval.subject.local_root;
  const titleWithDestination = phrase.destination
    ? `${phrase.title} ${phrase.destination}`
    : phrase.title;

  const groupLabel = isPending
    ? `Approval needed: ${titleWithDestination}`
    : `${status.label}: ${titleWithDestination}`;

  return (
    <div
      role="group"
      aria-label={groupLabel}
      data-approval-anchor={approval.id}
      className={cn(
        'rounded-md border border-border bg-muted/40 border-l-2 text-[13px]',
        isPending && 'border-l-amber-500/70 bg-amber-500/5',
        approval.status === 'approved' && 'border-l-emerald-500/60',
        !isPending && approval.status !== 'approved' && 'border-l-border',
        className,
      )}
    >
      <div className={cn('p-3', decidedCollapsed && 'py-2.5')}>
        {/* Header */}
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <PhraseIcon
            size={15}
            className={cn(
              'shrink-0',
              isPending ? 'text-amber-500' : 'text-muted-foreground',
            )}
            aria-hidden
          />
          <h3 className="min-w-0 flex-1 text-[13px] font-medium text-foreground [overflow-wrap:anywhere]">
            {phrase.title}
            {phrase.destination && (
              <span className="text-muted-foreground font-normal"> {phrase.destination}</span>
            )}
          </h3>
          {provenance && (
            <span
              className="inline-flex shrink-0 items-center rounded-full border border-border px-2 py-0.5 text-[11px] font-medium text-muted-foreground"
              data-approval-provenance
              title={
                provenance.startsWith('Connected:')
                  ? 'Reaches a connected account'
                  : provenance === 'Attached'
                    ? 'Reads an attachment in this chat'
                    : provenance === 'Provider-hosted'
                      ? 'Runs on your model provider'
                      : undefined
              }
            >
              {provenance}
            </span>
          )}
          <span
            className="inline-flex shrink-0 items-center gap-1 rounded-full border border-border px-2 py-0.5 text-[11px] font-medium text-muted-foreground"
            title={category.meaning}
          >
            <CategoryIcon size={11} aria-hidden />
            {category.chipLabel}
          </span>
        </div>

        {decidedCollapsed ? (
          <DecidedSummaryLine approval={approval} statusLineRef={statusLineRef} />
        ) : (
          <>
            <p className="mt-1 text-[12px] text-muted-foreground">
              {isScanBudgetApproval(approval)
                ? scanBudgetMeaning()
                : categoryMeaning(approval.category, approval.subject)}
            </p>

            {/* Subject / preview zone */}
            {!deferLocalLocationPreview && <ApprovalPreview approval={approval} />}

            {/* Technical details */}
            <TechnicalDetails approval={approval} />

            {/* Decision zone */}
            {isPending ? (
              isLocalLocationRequest ? (
                <LocalLocationDecision
                  approval={approval}
                  variant={variant}
                  isBusy={isBusy}
                  busyAction={busyAction}
                  onApprove={(scope, subject) => decide('approve', scope, subject)}
                  onReject={() => decide('reject')}
                  onShowInChat={onShowInChat}
                />
              ) : (
                <GenericDecisionZone
                  approval={approval}
                  variant={variant}
                  isBusy={isBusy}
                  busyAction={busyAction}
                  onApprove={(scope) => decide('approve', scope)}
                  onReject={() => decide('reject')}
                  onResolvedMenuClose={() => onDecidedByUser?.(approval.id)}
                  onShowInChat={onShowInChat}
                />
              )
            ) : (
              <DecidedStatusLine approval={approval} statusLineRef={statusLineRef} />
            )}

            {/* Error zone */}
            {error && (
              <div
                role="alert"
                className={cn(
                  'mt-2 flex flex-wrap items-center gap-2 rounded-md border px-2.5 py-1.5 text-[12px]',
                  error.kind === 'conflict'
                    ? 'border-border bg-muted/60 text-muted-foreground'
                    : 'border-destructive/40 bg-destructive/10 text-destructive',
                )}
              >
                <AlertTriangle size={13} className="shrink-0" aria-hidden />
                <span className="min-w-0 flex-1">{error.message}</span>
                {error.kind === 'retryable' && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 text-[12px]"
                    onClick={() => void decide(error.retry.action, error.retry.scope)}
                  >
                    Try again
                  </Button>
                )}
              </div>
            )}
          </>
        )}

        {/* Collapsed decided transcript card: request details behind a disclosure */}
        {decidedCollapsed && (
          <Collapsible open={detailsOpen} onOpenChange={setDetailsOpen}>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="mt-1.5 inline-flex min-h-6 items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm"
              >
                <ChevronDown
                  size={12}
                  className={cn('transition-transform motion-reduce:transition-none', detailsOpen && 'rotate-180')}
                  aria-hidden
                />
                {detailsOpen ? 'Hide request details' : 'Show request details'}
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <div className="mt-2">
                <ApprovalPreview approval={approval} />
                <TechnicalDetails approval={approval} />
              </div>
            </CollapsibleContent>
          </Collapsible>
        )}
      </div>
    </div>
  );
};

// ---------------------------------------------------------------------------
// Decided states
// ---------------------------------------------------------------------------

function decidedIcon(approval: ApprovalRequestUI) {
  switch (approval.status) {
    case 'approved':
      return <Check size={13} className="text-emerald-500" aria-hidden />;
    case 'rejected':
      return <CircleSlash size={13} className="text-foreground/70" aria-hidden />;
    default:
      return <Clock size={13} className="text-foreground/70" aria-hidden />;
  }
}

function decidedText(approval: ApprovalRequestUI): string {
  if (approval.status === 'canceled') {
    return 'Canceled — the assistant stopped before you decided';
  }
  const when = approval.decision?.decided_at
    ? ` · ${relativeTime(approval.decision.decided_at)}`
    : '';
  return `${decidedStatusSentence(approval)}${when}`;
}

const DecidedStatusLine: React.FC<{
  approval: ApprovalRequestUI;
  statusLineRef: React.RefObject<HTMLParagraphElement>;
}> = ({ approval, statusLineRef }) => (
  <p
    ref={statusLineRef}
    tabIndex={-1}
    className="mt-3 flex items-center gap-1.5 text-[12px] text-foreground/80 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
  >
    {decidedIcon(approval)}
    {decidedText(approval)}
  </p>
);

const DecidedSummaryLine: React.FC<{
  approval: ApprovalRequestUI;
  statusLineRef: React.RefObject<HTMLParagraphElement>;
}> = ({ approval, statusLineRef }) => (
  <p
    ref={statusLineRef}
    tabIndex={-1}
    className="mt-1 flex flex-wrap items-center gap-1.5 text-[12px] text-foreground/80 rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
  >
    {decidedIcon(approval)}
    {decidedText(approval)}
  </p>
);

// ---------------------------------------------------------------------------
// Decision zones
// ---------------------------------------------------------------------------

const GenericDecisionZone: React.FC<{
  approval: ApprovalRequestUI;
  variant: ApprovalCardVariant;
  isBusy: boolean;
  busyAction: 'reject' | ApprovalScope | null;
  onApprove: (scope: ApprovalScope) => Promise<void>;
  onReject: () => Promise<void>;
  onResolvedMenuClose?: () => void;
  onShowInChat?: (approval: ApprovalRequestUI) => void;
}> = ({
  approval,
  variant,
  isBusy,
  busyAction,
  onApprove,
  onReject,
  onResolvedMenuClose,
  onShowInChat,
}) => (
  <>
    <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center">
      <ApprovalScopeSelector
        // Scan-budget approvals authorize one query's cost; a thread/always
        // grant would misrepresent what approving does, so never offer one.
        options={isScanBudgetApproval(approval) ? ['once'] : approval.options}
        subject={approval.subject}
        busy={isBusy}
        busyScope={busyAction && busyAction !== 'reject' ? busyAction : null}
        onSelect={(scope) => void onApprove(scope)}
        onResolvedMenuClose={onResolvedMenuClose}
      />
      <RejectButton isBusy={isBusy} rejecting={busyAction === 'reject'} onReject={onReject} />
      {variant === 'live' && onShowInChat && <ShowInChatButton approval={approval} onShowInChat={onShowInChat} />}
    </div>
    <WaitingLine approval={approval} />
  </>
);

const LocalLocationDecision: React.FC<{
  approval: ApprovalRequestUI;
  variant: ApprovalCardVariant;
  isBusy: boolean;
  busyAction: 'reject' | ApprovalScope | null;
  onApprove: (scope: ApprovalScope, subject: PermissionSubject) => Promise<void>;
  onReject: () => Promise<void>;
  onShowInChat?: (approval: ApprovalRequestUI) => void;
}> = ({ approval, variant, isBusy, busyAction, onApprove, onReject, onShowInChat }) => {
  const scopes = orderedScopes(approval.options);
  const requestedKind = localLocationKind(approval.subject);
  const canChooseComputer = requestedKind === 'folder' && !approval.subject.local_root;
  const [picked, setPicked] = useState<LocalDirectoryPick | LocalFilePick | null>(
    approval.subject.local_root
      ? { path: approval.subject.local_root, displayName: displayPath(approval.subject.local_root) }
      : null,
  );
  const [scope, setScope] = useState<ApprovalScope>(scopes[0] || 'once');
  const [picking, setPicking] = useState(false);
  const [pickerError, setPickerError] = useState('');

  const pick = async () => {
    if (isBusy || picking) return;
    setPicking(true);
    setPickerError('');
    try {
      setPicked(requestedKind === 'file' ? await pickLocalFile() : await pickLocalDirectory());
    } catch (error: any) {
      if (!isLocalPickerCanceled(error)) {
        setPickerError(error?.message || `Couldn’t choose a ${requestedKind}.`);
      }
    } finally {
      setPicking(false);
    }
  };

  const subject: PermissionSubject | null = picked
    ? {
        ...approval.subject,
        kind: 'local_location',
        local_root: picked.path,
        local_kind: requestedKind,
        excludes: approval.subject.excludes,
      }
    : null;
  const selectedApproval = subject ? { ...approval, subject } : approval;
  const approveLabel = subject
    ? isComputerWideLocalSubject(subject)
      ? 'Allow computer search'
      : requestedKind === 'file'
        ? 'Allow file read'
        : 'Allow folder search'
    : 'Allow';

  if (!picked || !subject) {
    return (
      <>
        <LocalLocationRequestPreview approval={approval} />
        <div className="mt-3 flex flex-col gap-2 sm:flex-row sm:items-center">
          <Button
            type="button"
            className="h-9 w-full sm:w-auto text-[13px]"
            aria-busy={picking || undefined}
            aria-disabled={isBusy || picking || undefined}
            onClick={() => void pick()}
          >
            {picking ? (
              <Loader2 size={14} className="mr-1.5 animate-spin motion-reduce:animate-none" aria-hidden />
            ) : requestedKind === 'file' ? (
              <FileText size={14} className="mr-1.5" aria-hidden />
            ) : (
              <FolderOpen size={14} className="mr-1.5" aria-hidden />
            )}
            {requestedKind === 'file' ? 'Choose file' : 'Choose folder'}
          </Button>
          {canChooseComputer && (
            <Button
              type="button"
              variant="outline"
              className="h-9 w-full sm:w-auto text-[13px]"
              aria-disabled={isBusy || picking || undefined}
              onClick={() => {
                if (!isBusy && !picking) {
                  setPicked({ path: '/', displayName: 'This computer' });
                }
              }}
            >
              <HardDrive size={14} className="mr-1.5" aria-hidden />
              Search this computer
            </Button>
          )}
          <RejectButton isBusy={isBusy} rejecting={busyAction === 'reject'} onReject={onReject} />
          {variant === 'live' && onShowInChat && <ShowInChatButton approval={approval} onShowInChat={onShowInChat} />}
        </div>
        {pickerError && (
          <p role="alert" className="mt-2 text-[12px] text-destructive">
            {pickerError}
          </p>
        )}
        <WaitingLine approval={approval} />
      </>
    );
  }

  return (
    <>
      {!approval.subject.local_root && <LocalLocationPreview approval={selectedApproval} />}
      <div className="mt-3 flex flex-col gap-2">
        {scopes.length > 1 && (
          <div
            role="radiogroup"
            aria-label={`How long to allow this ${requestedKind}`}
            className="flex flex-wrap items-center gap-1 rounded-md border border-border bg-muted/40 p-1"
          >
            {scopes.map((option) => {
              const selected = option === scope;
              return (
                <button
                  key={option}
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  className={cn(
                    'rounded px-2.5 py-1 text-[12px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                    selected
                      ? 'bg-foreground font-medium text-background'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                  onClick={() => {
                    if (!isBusy) setScope(option);
                  }}
                >
                  {localScopeButtonLabel(option, subject)}
                </button>
              );
            })}
          </div>
        )}
        <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
          <Button
            type="button"
            className="h-9 w-full sm:w-auto text-[13px]"
            aria-disabled={isBusy || undefined}
            aria-busy={busyAction && busyAction !== 'reject' || undefined}
            onClick={() => {
              if (!isBusy) void onApprove(scope, subject);
            }}
          >
            {busyAction && busyAction !== 'reject' && (
              <Loader2 size={14} className="mr-1.5 animate-spin motion-reduce:animate-none" aria-hidden />
            )}
            {approveLabel}
          </Button>
          <Button
            type="button"
            variant="outline"
            className="h-9 w-full sm:w-auto text-[13px]"
            aria-disabled={isBusy || picking || undefined}
            aria-busy={picking || undefined}
            onClick={() => void pick()}
          >
            {requestedKind === 'file' ? 'Choose different file' : 'Choose different folder'}
          </Button>
          <RejectButton isBusy={isBusy} rejecting={busyAction === 'reject'} onReject={onReject} />
          {variant === 'live' && onShowInChat && <ShowInChatButton approval={approval} onShowInChat={onShowInChat} />}
        </div>
      </div>
      {pickerError && (
        <p role="alert" className="mt-2 text-[12px] text-destructive">
          {pickerError}
        </p>
      )}
      <WaitingLine approval={approval} />
    </>
  );
};

const RejectButton: React.FC<{
  isBusy: boolean;
  rejecting: boolean;
  onReject: () => Promise<void>;
}> = ({ isBusy, rejecting, onReject }) => (
  <Button
    type="button"
    variant="outline"
    className="h-9 w-full sm:w-auto text-[13px]"
    aria-disabled={isBusy || undefined}
    aria-busy={rejecting || undefined}
    onClick={(e) => {
      if (isBusy) {
        e.preventDefault();
        return;
      }
      void onReject();
    }}
  >
    {rejecting && (
      <Loader2 size={14} className="mr-1.5 animate-spin motion-reduce:animate-none" aria-hidden />
    )}
    Don’t allow
  </Button>
);

const ShowInChatButton: React.FC<{
  approval: ApprovalRequestUI;
  onShowInChat: (approval: ApprovalRequestUI) => void;
}> = ({ approval, onShowInChat }) => (
  <Button
    type="button"
    variant="ghost"
    className="h-9 w-full sm:w-auto text-[12px] text-muted-foreground sm:ml-auto"
    onClick={() => onShowInChat(approval)}
  >
    <MessageSquareText size={13} className="mr-1.5" aria-hidden />
    Show in chat
  </Button>
);

const WaitingLine: React.FC<{ approval: ApprovalRequestUI }> = ({ approval }) => (
  <p className="mt-2 flex items-center gap-1.5 text-[11px] text-muted-foreground">
    <Clock size={11} aria-hidden />
    Waiting for your decision
    {approval.createdAt ? ` · requested ${relativeTime(approval.createdAt)}` : ''}
  </p>
);

function localScopeButtonLabel(scope: ApprovalScope, subject?: PermissionSubject | null): string {
  switch (scope) {
    case 'thread':
      return 'This chat';
    case 'always':
      if (subject?.kind === 'local_location') {
        if (isComputerWideLocalSubject(subject)) return 'Always allow this computer';
        return localLocationKind(subject) === 'file' ? 'Always allow this file' : 'Always allow this folder';
      }
      return 'Always allow';
    default:
      return 'Once';
  }
}

// ---------------------------------------------------------------------------
// Preview zone
// ---------------------------------------------------------------------------

const ApprovalPreview: React.FC<{ approval: ApprovalRequestUI }> = ({ approval }) => {
  if (approval.subject.kind === 'local_location') {
    if (approval.subject.local_root) {
      return <LocalLocationPreview approval={approval} />;
    }
    return <LocalLocationRequestPreview approval={approval} />;
  }
  if (isScanBudgetApproval(approval)) {
    return <ScanBudgetPreview approval={approval} />;
  }

  const preview = approval.preview || {};
  const markdown =
    typeof preview.description === 'string'
      ? preview.description
      : typeof preview.text === 'string'
        ? preview.text
        : '';
  const command =
    typeof preview.command === 'string'
      ? preview.command
      : Array.isArray(preview.args)
        ? `gh ${preview.args.join(' ')}`
        : '';
  const summaryLine = typeof preview.summary === 'string' ? preview.summary : '';

  if (!markdown && !command && !summaryLine) return null;

  return (
    <div className="mt-2.5 rounded-md border border-border bg-background/60 p-2.5">
      {summaryLine && <div className="font-medium text-[13px]">{summaryLine}</div>}
      {markdown && (
        <div className={cn('text-[13px]', summaryLine && 'mt-1.5')}>
          <MarkdownRenderer content={markdown} />
        </div>
      )}
      {command && (
        <pre className="overflow-x-auto font-mono text-[12px] text-foreground whitespace-pre-wrap [overflow-wrap:anywhere]">
          $ {command}
        </pre>
      )}
    </div>
  );
};

/**
 * Cost preview for BigQuery scan-budget approvals: the estimated scan against
 * the user's two policy levels, plus the query itself. Facts only — the
 * one-time framing lives in the card's meaning line.
 */
const ScanBudgetPreview: React.FC<{ approval: ApprovalRequestUI }> = ({ approval }) => {
  const preview = approval.preview || {};
  const estimated = formatScanBytes(preview.estimated_bytes ?? preview.estimatedBytes);
  const automatic = formatScanBytes(preview.automatic_scan_bytes ?? preview.automaticScanBytes);
  const authorized = formatScanBytes(preview.authorized_scan_bytes ?? preview.authorizedScanBytes);
  const approvedCeiling = formatScanBytes(preview.approved_scan_bytes ?? preview.approvedScanBytes);
  const project = stringPreviewValue(preview, 'project_id', 'projectId');
  const sql = stringPreviewValue(preview, 'sql_preview', 'sqlPreview', 'sql');
  const summaryLine = typeof preview.summary === 'string' ? preview.summary : '';

  if (!estimated && !automatic && !authorized && !approvedCeiling && !project && !sql && !summaryLine) return null;

  return (
    <div className="mt-2.5 rounded-md border border-border bg-background/60 p-2.5">
      {summaryLine && <div className="text-[13px] font-medium">{summaryLine}</div>}
      {(estimated || automatic || authorized || approvedCeiling || project) && (
        <dl className={cn('grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-[12px]', summaryLine && 'mt-2')}>
          {estimated && (
            <>
              <dt className="text-muted-foreground">Estimated scan</dt>
              <dd className="min-w-0 font-medium text-foreground">{estimated}</dd>
            </>
          )}
          {automatic && (
            <>
              <dt className="text-muted-foreground">Runs without asking</dt>
              <dd className="min-w-0 text-foreground">up to {automatic}</dd>
            </>
          )}
          {authorized && (
            <>
              <dt className="text-muted-foreground">This approval allows</dt>
              <dd className="min-w-0 font-medium text-foreground">up to {authorized}</dd>
            </>
          )}
          {approvedCeiling && (
            <>
              <dt className="text-muted-foreground">Connection hard ceiling</dt>
              <dd className="min-w-0 text-foreground">up to {approvedCeiling}</dd>
            </>
          )}
          {project && (
            <>
              <dt className="text-muted-foreground">Project billed</dt>
              <dd className="min-w-0 font-mono text-foreground [overflow-wrap:anywhere]">{project}</dd>
            </>
          )}
        </dl>
      )}
      {sql && (
        <pre className="mt-2 max-h-44 overflow-auto rounded bg-muted/60 p-2 font-mono text-[12px] text-foreground whitespace-pre-wrap [overflow-wrap:anywhere]">
          {sql}
        </pre>
      )}
    </div>
  );
};

const DEFAULT_EXCLUDES_COPY =
  'Hidden files and credential files (like passwords and keys) are never read.';

const LocalLocationRequestPreview: React.FC<{ approval: ApprovalRequestUI }> = ({ approval }) => {
  const requestedKind = localLocationKind(approval.subject);
  const query = stringPreviewValue(approval.preview, 'query');
  const locationHint = stringPreviewValue(approval.preview, 'location_hint', 'locationHint');
  const requestedPath = stringPreviewValue(approval.preview, 'path', 'file_ref', 'fileRef');
  const fileTypes = stringArrayPreviewValue(approval.preview, 'file_types', 'fileTypes');

  return (
    <div className="mt-2.5 rounded-md border border-border bg-background/60 p-2.5">
      <div className="flex items-start gap-2">
        {requestedKind === 'file' ? (
          <FileText size={14} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
        ) : (
          <FolderOpen size={14} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
        )}
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] font-medium text-foreground">
            {requestedKind === 'file' ? 'Local file access moved to Local Computer' : 'Local folder access moved to Local Computer'}
          </div>
          <div className="mt-0.5 text-[12px] text-muted-foreground">
            Add the needed location in Settings &gt; Connections &gt; Local Computer, then retry the request.
          </div>
        </div>
      </div>
      {(query || locationHint || requestedPath || fileTypes.length > 0) && (
        <dl className="mt-2 grid grid-cols-[max-content_1fr] gap-x-3 gap-y-1 text-[12px]">
          {query && (
            <>
              <dt className="text-muted-foreground">Search</dt>
              <dd className="min-w-0 text-foreground [overflow-wrap:anywhere]">“{query}”</dd>
            </>
          )}
          {locationHint && (
            <>
              <dt className="text-muted-foreground">Hint</dt>
              <dd className="min-w-0 text-foreground [overflow-wrap:anywhere]">{locationHint}</dd>
            </>
          )}
          {requestedPath && (
            <>
              <dt className="text-muted-foreground">Target</dt>
              <dd className="min-w-0 font-mono text-foreground [overflow-wrap:anywhere]">{requestedPath}</dd>
            </>
          )}
          {fileTypes.length > 0 && (
            <>
              <dt className="text-muted-foreground">Type</dt>
              <dd className="min-w-0 text-foreground [overflow-wrap:anywhere]">{fileTypes.join(', ')}</dd>
            </>
          )}
        </dl>
      )}
      <ul className="mt-2 space-y-1 text-[12px] text-muted-foreground">
        <li>
          {requestedKind === 'file'
            ? 'Read-only access handled by the local file search agent.'
            : 'Read-only search handled by the local file search agent.'}
        </li>
        <li>
          {requestedKind === 'file'
            ? 'Only the selected file can be read.'
            : DEFAULT_EXCLUDES_COPY}
        </li>
        {requestedKind === 'folder' && (
          <li>
            Choosing this computer can search all readable folders, except protected files.
          </li>
        )}
      </ul>
    </div>
  );
};

const LocalLocationPreview: React.FC<{ approval: ApprovalRequestUI }> = ({ approval }) => {
  const [excludesOpen, setExcludesOpen] = useState(false);
  const localKind = localLocationKind(approval.subject);
  const isComputerWide = isComputerWideLocalSubject(approval.subject);
  const root = displayPath(approval.subject.local_root);
  const excludes = approval.subject.excludes || [];
  const query =
    typeof approval.preview?.query === 'string' ? approval.preview.query : '';

  return (
    <div className="mt-2.5 rounded-md border border-border bg-background/60 p-2.5">
      <div className="flex items-center gap-2">
        {localKind === 'file' ? (
          <FileText size={14} className="shrink-0 text-amber-500" aria-hidden />
        ) : isComputerWide ? (
          <HardDrive size={14} className="shrink-0 text-amber-500" aria-hidden />
        ) : (
          <FolderOpen size={14} className="shrink-0 text-amber-500" aria-hidden />
        )}
        <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-foreground" title={root}>
          {root}
        </span>
      </div>
      <ul className="mt-2 space-y-1 text-[12px] text-muted-foreground">
        <li>
          {localKind === 'file'
            ? 'Read-only — this file is not changed or deleted.'
            : isComputerWide
              ? 'Read-only — files on this computer are searched, not changed or deleted.'
              : 'Read-only — nothing in this folder is changed or deleted.'}
        </li>
        {localKind === 'folder' && (
          <li>
            {isComputerWide
              ? 'Can search all readable folders on this computer, except protected paths listed below.'
              : excludes.length > 0
                ? 'Includes subfolders, except protected paths listed below.'
                : 'Includes subfolders.'}
          </li>
        )}
        {query && (
          <li>
            Searching for: <span className="text-foreground">“{query}”</span>
          </li>
        )}
      </ul>
      {localKind === 'folder' && (
      <Collapsible open={excludesOpen} onOpenChange={setExcludesOpen}>
        <CollapsibleTrigger asChild>
          <button
            type="button"
            className="mt-2 inline-flex min-h-6 items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm"
          >
            <ChevronDown
              size={12}
              className={cn('transition-transform motion-reduce:transition-none', excludesOpen && 'rotate-180')}
              aria-hidden
            />
            What’s never read
          </button>
        </CollapsibleTrigger>
        <CollapsibleContent>
          <p className="mt-1.5 text-[12px] text-muted-foreground">{DEFAULT_EXCLUDES_COPY}</p>
          {excludes.length > 0 && (
            <ul className="mt-1 list-disc pl-5 font-mono text-[11.5px] text-muted-foreground">
              {excludes.map((pattern) => (
                <li key={pattern}>{pattern}</li>
              ))}
            </ul>
          )}
        </CollapsibleContent>
      </Collapsible>
      )}
    </div>
  );
};

function localLocationKind(subject: PermissionSubject): 'folder' | 'file' {
  return subject.local_kind === 'file' ? 'file' : 'folder';
}

function stringPreviewValue(preview: Record<string, unknown> | undefined, ...keys: string[]): string {
  if (!preview) return '';
  for (const key of keys) {
    const value = preview[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function stringArrayPreviewValue(preview: Record<string, unknown> | undefined, ...keys: string[]): string[] {
  if (!preview) return [];
  for (const key of keys) {
    const value = preview[key];
    if (Array.isArray(value)) {
      return value
        .map((item) => (typeof item === 'string' ? item.trim() : ''))
        .filter(Boolean);
    }
    if (typeof value === 'string' && value.trim()) {
      return value
        .split(',')
        .map((item) => item.trim())
        .filter(Boolean);
    }
  }
  return [];
}

// ---------------------------------------------------------------------------
// Technical details
// ---------------------------------------------------------------------------

const TechnicalDetails: React.FC<{ approval: ApprovalRequestUI }> = ({ approval }) => {
  const [open, setOpen] = useState(false);
  const rows: Array<[string, string]> = [];
  if (approval.tool) rows.push(['Tool', approval.tool]);
  if (approval.operation) rows.push(['Operation', approval.operation]);
  if (approval.subject.connector) rows.push(['Connector', approval.subject.connector]);
  if (approval.subject.tool_set_id) rows.push(['Tool set', approval.subject.tool_set_id]);
  if (approval.subject.repository) rows.push(['Repository', approval.subject.repository]);
  if (approval.subject.remote_kind) rows.push(['Remote target type', approval.subject.remote_kind]);
  if (approval.subject.remote_id) rows.push(['Remote target', approval.subject.remote_id]);
  if (approval.subject.local_root) rows.push(['Path', approval.subject.local_root]);
  rows.push(['Request ID', approval.id]);
  if (rows.length === 0) return null;

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="mt-2 inline-flex min-h-6 items-center gap-1 text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm"
        >
          <ChevronDown
            size={12}
            className={cn('transition-transform motion-reduce:transition-none', open && 'rotate-180')}
            aria-hidden
          />
          Technical details
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent>
        <dl className="mt-1.5 grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-[11.5px]">
          {rows.map(([label, value]) => (
            <React.Fragment key={label}>
              <dt className="text-muted-foreground">{label}</dt>
              <dd className="min-w-0 font-mono text-foreground [overflow-wrap:anywhere]">{value}</dd>
            </React.Fragment>
          ))}
        </dl>
      </CollapsibleContent>
    </Collapsible>
  );
};

export default ApprovalCard;
