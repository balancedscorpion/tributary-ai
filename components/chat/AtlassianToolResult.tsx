import React from 'react';
import {
  AlertCircle,
  CheckCircle2,
  ExternalLink,
  FileText,
  Link2,
  Loader2,
  MessageSquare,
  Paperclip,
  Plus,
  ShieldCheck,
  Ticket,
  XCircle,
} from 'lucide-react';
import { toast } from 'sonner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import type { ToolResultEnvelope } from '@/types';
import { isToolResultFailure } from './toolActivity/toolResultOutcome';
import {
  JiraIssueCard,
  extractJiraIssueFromResult,
  normalizeJiraIssue,
} from '@/components/renderers/tool/jira/IssueCard';
import MarkdownRenderer from './MarkdownRenderer';
import { decideApproval } from '@/services/permissionsApi';
import {
  extractToolErrorDiagnosticFromEnvelope,
  formatToolErrorCardText,
} from '@/lib/toolErrorDiagnostics';
import { ToolEnvelopeReauth } from './ConnectionReauthBanner';

interface AtlassianToolResultProps {
  envelope: ToolResultEnvelope;
  fallbackName?: string;
}

function isObject(value: any): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function firstIssue(result: any): Record<string, any> | null {
  if (isObject(result?.issue)) return result.issue;
  if (Array.isArray(result?.issues) && isObject(result.issues[0])) return result.issues[0];
  if (isObject(result?.issues?.issue)) return result.issues.issue;
  return null;
}

function pageList(result: any): Record<string, any>[] {
  if (Array.isArray(result?.pages)) return result.pages.filter(isObject);
  if (isObject(result?.page)) return [result.page];
  if (Array.isArray(result?.homepage?.pages)) return result.homepage.pages.filter(isObject);
  if (isObject(result?.homepage?.page)) return [result.homepage.page];
  return [];
}

function issueList(result: any): Record<string, any>[] {
  if (Array.isArray(result?.issues)) return result.issues.filter(isObject);
  const issue = firstIssue(result);
  return issue ? [issue] : [];
}

function commentList(result: any): Record<string, any>[] {
  return Array.isArray(result?.comments) ? result.comments.filter(isObject) : [];
}

function JiraIssueKey({ issue }: { issue: Record<string, any> }) {
  const key = text(issue.key);
  const url = text(issue.url);
  if (!key) return null;
  if (!url) {
    return <span className="font-mono text-xs font-semibold text-foreground">{key}</span>;
  }
  return (
    <a
      href={url}
      target="_blank"
      rel="noreferrer"
      className="font-mono text-xs font-semibold text-primary hover:underline"
    >
      {key}
    </a>
  );
}

function StatusPill({ ok, errors }: { ok: boolean; errors?: any[] }) {
  if (!ok) {
    return (
      <Badge variant="outline" className="bg-destructive/10 text-destructive border-destructive/25 gap-1">
        <AlertCircle size={10} /> Error{Array.isArray(errors) && errors.length ? ` · ${errors.length}` : ''}
      </Badge>
    );
  }
  return (
    <Badge variant="outline" className="bg-emerald-500/10 text-emerald-600 border-emerald-500/25 gap-1">
      <CheckCircle2 size={10} /> Ready
    </Badge>
  );
}

function Shell({ children, product, title, icon, ok, errors, errorDetail, envelope }: {
  children: React.ReactNode;
  product: string;
  title: string;
  icon: React.ReactNode;
  ok: boolean;
  errors?: any[];
  errorDetail?: string | null;
  /** When provided, auth-class failures render an inline reconnect action. */
  envelope?: ToolResultEnvelope;
}) {
  return (
    <div className="group/msg w-full max-w-[85%] md:max-w-[75%] pl-12 mb-4 animate-fade-in min-w-0">
      <div className="rounded-lg border border-border bg-background/70 overflow-hidden shadow-sm">
        <div className="px-4 py-3 border-b border-border bg-muted/20 flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 min-w-0">
            <div className="h-8 w-8 rounded-md border border-border bg-background flex items-center justify-center text-muted-foreground">
              {icon}
            </div>
            <div className="min-w-0">
              <div className="text-sm font-semibold text-foreground truncate">{title}</div>
              <div className="text-[11px] text-muted-foreground">{product}</div>
            </div>
          </div>
          <StatusPill ok={ok} errors={errors} />
        </div>
        {!ok && errorDetail && (
          <div className="border-b border-destructive/20 bg-destructive/5 px-4 py-2">
            <p className="chat-text-wrap text-xs leading-snug text-destructive/85">
              {errorDetail}
            </p>
          </div>
        )}
        {!ok && envelope && (
          <div className="px-4 [&>*]:mb-2">
            <ToolEnvelopeReauth envelope={envelope} />
          </div>
        )}
        {children}
      </div>
    </div>
  );
}

function envelopeErrorDetail(envelope: ToolResultEnvelope): string | null {
  if (!isToolResultFailure(envelope)) return null;
  return formatToolErrorCardText(
    extractToolErrorDiagnosticFromEnvelope(envelope, {
      fallbackTitle: envelope.tool_name || 'Tool error',
    }),
  );
}

function JiraIssueWidget({ envelope }: { envelope: ToolResultEnvelope }) {
  const result = envelope.result || {};
  const issues = issueList(result);
  // Single-issue payloads (jira_get_issue and friends) get the full issue
  // card; multi-issue payloads (jira_search_issues) keep the compact rows.
  // extractJiraIssueFromResult handles the live nested `result.issue` shape
  // and legacy flat results alike.
  const single =
    !Array.isArray(result.issues) || result.issues.length === 1
      ? extractJiraIssueFromResult(result) ||
        (issues.length === 1 ? normalizeJiraIssue(issues[0]) : null)
      : null;
  if (single) {
    // The card owns the key/summary/status identity row — keep the Shell
    // header quiet so the issue title isn't rendered twice back to back.
    return (
      <Shell
        product="Jira"
        title={single.key || 'Jira issue'}
        icon={<Ticket size={16} />}
        ok={envelope.ok}
        errors={result.errors}
        errorDetail={envelopeErrorDetail(envelope)}
        envelope={envelope}
      >
        <div className="p-4">
          <JiraIssueCard issue={single} />
        </div>
      </Shell>
    );
  }
  return (
    <Shell product="Jira" title="Jira results" icon={<Ticket size={16} />} ok={envelope.ok} errors={result.errors} errorDetail={envelopeErrorDetail(envelope)} envelope={envelope}>
      <div className="p-4 space-y-3">
        {issues.slice(0, 6).map((issue) => (
          <div key={issue.key || issue.id} className="rounded-md border border-border bg-background p-3">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <JiraIssueKey issue={issue} />
                  {issue.status && <Badge variant="secondary" className="text-[10px]">{issue.status}</Badge>}
                  {issue.issueType && <Badge variant="outline" className="text-[10px]">{issue.issueType}</Badge>}
                </div>
                <div className="text-sm text-foreground mt-1">{issue.summary || 'Untitled issue'}</div>
                <div className="text-[11px] text-muted-foreground mt-1">
                  {issue.assignee?.displayName ? `Assignee ${issue.assignee.displayName}` : 'Unassigned'}
                  {issue.updated ? ` · Updated ${new Date(issue.updated).toLocaleDateString()}` : ''}
                </div>
              </div>
              {issue.url && (
                <a href={issue.url} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground">
                  <ExternalLink size={14} />
                </a>
              )}
            </div>
          </div>
        ))}
        {typeof result.total === 'number' && (
          <div className="text-xs text-muted-foreground">{result.total} matching issues</div>
        )}
      </div>
    </Shell>
  );
}

function JiraCommentsWidget({ envelope }: { envelope: ToolResultEnvelope }) {
  const result = envelope.result || {};
  const comments = commentList(result);
  const issueKey = text(result.issueKey);
  return (
    <Shell product="Jira" title={issueKey ? `${issueKey}: comments` : 'Jira comments'} icon={<MessageSquare size={16} />} ok={envelope.ok} errors={result.errors} errorDetail={envelopeErrorDetail(envelope)} envelope={envelope}>
      <div className="p-4 space-y-3">
        {comments.slice(0, 8).map((comment) => (
          <div key={comment.id || comment.url} className="rounded-md border border-border bg-background p-3">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  {comment.id && <Badge variant="outline" className="font-mono text-[10px]">Comment {comment.id}</Badge>}
                  {comment.parentCommentId && <Badge variant="secondary" className="text-[10px]">Reply to {comment.parentCommentId}</Badge>}
                  {comment.author?.displayName && <span className="text-[11px] text-muted-foreground">{comment.author.displayName}</span>}
                </div>
                <div className="mt-2 text-xs text-muted-foreground line-clamp-4 whitespace-pre-wrap">
                  {text(comment.bodyText) || text(comment.renderedText) || 'No comment text available.'}
                </div>
                {comment.created && (
                  <div className="mt-2 text-[11px] text-muted-foreground">
                    Created {new Date(comment.created).toLocaleString()}
                  </div>
                )}
              </div>
              {comment.url && (
                <a href={comment.url} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground">
                  <ExternalLink size={14} />
                </a>
              )}
            </div>
          </div>
        ))}
        {typeof result.total === 'number' && (
          <div className="text-xs text-muted-foreground">{result.total} comments</div>
        )}
      </div>
    </Shell>
  );
}

function ConfluenceWidget({ envelope }: { envelope: ToolResultEnvelope }) {
  const result = envelope.result || {};
  const pages = pageList(result);
  const title = result.space?.name || pages[0]?.title || 'Confluence results';
  return (
    <Shell product="Confluence" title={title} icon={<FileText size={16} />} ok={envelope.ok} errors={result.errors} errorDetail={envelopeErrorDetail(envelope)} envelope={envelope}>
      <div className="p-4 space-y-3">
        {result.space && (
          <div className="rounded-md border border-border bg-background p-3">
            <div className="flex items-center gap-2">
              <Badge variant="outline" className="font-mono text-[10px]">{result.space.key}</Badge>
              <span className="text-sm font-medium">{result.space.name}</span>
            </div>
            {result.space.description && <p className="text-xs text-muted-foreground mt-1">{result.space.description}</p>}
          </div>
        )}
        {pages.slice(0, 5).map((page) => (
          <div key={page.id || page.title} className="rounded-md border border-border bg-background p-3">
            <div className="flex items-start justify-between gap-3">
              <div className="min-w-0">
                <div className="text-sm font-medium text-foreground">{page.title || 'Untitled page'}</div>
                <div className="text-[11px] text-muted-foreground mt-1">
                  {page.version ? `v${page.version}` : 'Page'}
                  {page.createdAt ? ` · Created ${new Date(page.createdAt).toLocaleDateString()}` : ''}
                </div>
                {page.text && <p className="text-xs text-muted-foreground mt-2 line-clamp-3">{page.text}</p>}
                {page.excerpt && <p className="text-xs text-muted-foreground mt-2 line-clamp-2">{page.excerpt}</p>}
              </div>
              {page.url && (
                <a href={page.url} target="_blank" rel="noreferrer" className="text-muted-foreground hover:text-foreground">
                  <ExternalLink size={14} />
                </a>
              )}
            </div>
          </div>
        ))}
        {typeof result.totalSize === 'number' && (
          <div className="text-xs text-muted-foreground">{result.totalSize} matching pages</div>
        )}
      </div>
    </Shell>
  );
}

function ApprovalWidget({ envelope }: { envelope: ToolResultEnvelope }) {
  const approval = envelope.result?.approval || {};
  const integration = text(envelope.result_metadata?.integration) || 'atlassian';
  const productLabel = integration === 'github' ? 'GitHub' : integration === 'slack' ? 'Slack' : 'Jira';
  const [state, setState] = React.useState<'pending' | 'approved' | 'rejected' | 'error'>('pending');
  const [busy, setBusy] = React.useState<'approve' | 'reject' | null>(null);
  const [detail, setDetail] = React.useState<string>('');
  const content = isObject(approval.content) ? approval.content : {};
  const target = isObject(approval.target) ? approval.target : {};
  const approvalId = text(approval.id);
  const isCreate = approval.action === 'create_issue';
  const isUpdate = approval.action === 'update_issue';
  const isRelated = approval.action === 'add_related_tickets';
  const isUpload = approval.action === 'upload_attachment' || approval.action === 'upload_file';
  const mentions = Array.isArray(content.mentions) ? content.mentions.filter(isObject) : [];
  const relatedIssues = Array.isArray(content.related_issue_keys) ? content.related_issue_keys.map(text).filter(Boolean) : [];
  const fieldNames = Array.isArray(content.field_names) ? content.field_names.map(text).filter(Boolean) : [];
  const labels = Array.isArray(content.labels) ? content.labels.map(text).filter(Boolean) : [];
  const targetLabel = text(target.issue_key)
    || text(target.issueKey)
    || text(target.project_key)
    || text(target.projectKey)
    || text(target.repository)
    || text(target.command)
    || text(target.channel_id)
    || text(target.channelId)
    || text(target.id)
    || productLabel;
  const replyToCommentId = text(content.reply_to_comment_id) || text(content.replyToCommentId) || text(target.comment_id) || text(target.commentId);
  const sourceFormat = text(content.source_format) || text(content.sourceFormat) || 'markdown';
  const targetFormat = text(content.target_format) || text(content.targetFormat) || 'jira_adf';
  const commandPreview = text(content.command) || text(target.command);
  const sendAs = text(content.send_as) || text(content.sendAs) || text(target.send_as) || text(target.sendAs);
  const sendAsLabel = sendAs === 'bot' ? 'Send as bot' : sendAs === 'user' ? 'Send as me' : '';

  const mutate = async (action: 'approve' | 'reject') => {
    if (!approvalId) {
      setState('error');
      setDetail('Missing approval ID.');
      return;
    }
    setBusy(action);
    try {
      // Unified approvals service first; falls back to the legacy
      // per-connector route while the backend migration is in flight.
      await decideApproval({
        approvalId,
        action,
        legacyIntegration: integration,
      });
      setState(action === 'approve' ? 'approved' : 'rejected');
      setDetail(action === 'approve' ? 'Approved and executed.' : 'Rejected.');
      toast.success(action === 'approve' ? `${productLabel} action approved` : `${productLabel} action rejected`);
    } catch (e: any) {
      setState('error');
      setDetail(e?.message || 'Approval action failed');
      toast.error(e?.message || 'Approval action failed');
    } finally {
      setBusy(null);
    }
  };

  return (
    <Shell product={`${productLabel} approval`} title={approval.title || 'Approval required'} icon={isCreate ? <Plus size={16} /> : isUpdate ? <FileText size={16} /> : isRelated ? <Link2 size={16} /> : isUpload ? <Paperclip size={16} /> : <ShieldCheck size={16} />} ok={envelope.ok} errors={envelope.result?.errors} errorDetail={envelopeErrorDetail(envelope)} envelope={envelope}>
      <div className="p-4 space-y-3">
        <div className="rounded-md border border-amber-500/25 bg-amber-500/5 p-3">
          <div className="flex items-center gap-2 text-sm font-medium text-foreground">
            <ShieldCheck size={15} className="text-amber-600" />
            Review before {productLabel} is changed
          </div>
          <div className="mt-3 grid grid-cols-1 sm:grid-cols-2 gap-3 text-xs">
            <div>
              <div className="text-muted-foreground mb-1">Target</div>
              <div className="font-mono text-foreground break-all">
                {replyToCommentId ? `${targetLabel} comment ${replyToCommentId}` : targetLabel}
              </div>
              {integration === 'slack' && sendAsLabel && (
                <div className="mt-1 text-muted-foreground">{sendAsLabel}</div>
              )}
            </div>
            <div>
              <div className="text-muted-foreground mb-1">Action</div>
              <div className="font-mono text-foreground">{String(approval.action || 'write')}</div>
            </div>
          </div>
          <div className="mt-3">
            <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
              <div className="text-xs text-muted-foreground">{productLabel} preview</div>
              {integration !== 'github' && (
                <Badge variant="outline" className="text-[10px]">{sourceFormat} -&gt; {targetFormat}</Badge>
              )}
            </div>
            <div className="rounded-md bg-background border border-border p-3 text-sm">
              {integration === 'github' ? (
                commandPreview ? (
                  <div className="font-mono text-xs whitespace-pre-wrap break-words">{commandPreview}</div>
                ) : Array.isArray(content.args) ? (
                  <div className="font-mono text-xs whitespace-pre-wrap break-words">gh {content.args.join(' ')}</div>
                ) : (
                  <span className="text-muted-foreground">No command preview available.</span>
                )
              ) : (
                <>
                  {replyToCommentId && (
                <div className="mb-3 rounded-md border border-amber-500/20 bg-amber-500/5 px-2.5 py-1.5 text-xs text-muted-foreground">
                  Replying to Jira comment <span className="font-mono text-foreground">{replyToCommentId}</span>
                </div>
                  )}
                  {integration === 'slack' && approval.action === 'post_message' ? (
                    <MarkdownRenderer content={text(content.text) || text(approval.detail)} />
                  ) : isCreate ? (
                    <>
                      {mentions.length > 0 && (
                        <div className="mb-2 flex flex-wrap gap-1.5">
                          {mentions.map((mention) => (
                            <Badge key={text(mention.accountId) || text(mention.displayName)} variant="secondary" className="text-[10px]">
                              @{text(mention.displayName) || text(mention.accountId)}
                            </Badge>
                          ))}
                        </div>
                      )}
                      <div className="font-medium">{text(content.summary)}</div>
                      {text(content.description) && (
                        <div className="mt-2">
                          <MarkdownRenderer content={text(content.description)} />
                        </div>
                      )}
                    </>
                  ) : isUpdate ? (
                    <div className="space-y-2">
                      {mentions.length > 0 && (
                        <div className="flex flex-wrap gap-1.5">
                          {mentions.map((mention) => (
                            <Badge key={text(mention.accountId) || text(mention.displayName)} variant="secondary" className="text-[10px]">
                              @{text(mention.displayName) || text(mention.accountId)}
                            </Badge>
                          ))}
                        </div>
                      )}
                      {fieldNames.length > 0 && (
                        <div className="flex flex-wrap gap-1.5">
                          {fieldNames.map((field) => (
                            <Badge key={field} variant="outline" className="font-mono text-[10px]">{field}</Badge>
                          ))}
                        </div>
                      )}
                      {text(content.summary) && <div className="font-medium">{text(content.summary)}</div>}
                      {text(content.description) && <MarkdownRenderer content={text(content.description)} />}
                      {labels.length > 0 && <div className="text-xs text-muted-foreground">Labels: {labels.join(', ')}</div>}
                      {text(content.assignee_account_id) && <div className="text-xs text-muted-foreground">Assignee: {text(content.assignee_account_id)}</div>}
                      {text(content.priority_name) && <div className="text-xs text-muted-foreground">Priority: {text(content.priority_name)}</div>}
                      {!text(content.summary) && !text(content.description) && labels.length === 0 && (
                        <div className="text-sm">{text(content.changes) || approval.detail}</div>
                      )}
                    </div>
                  ) : isRelated ? (
                    <div className="space-y-2">
                      <div className="text-sm">
                        Link as <span className="font-mono">{text(content.relationship) || 'Relates'}</span>
                      </div>
                      <div className="flex flex-wrap gap-1.5">
                        {relatedIssues.map((issue) => (
                          <Badge key={issue} variant="secondary" className="font-mono text-[10px]">{issue}</Badge>
                        ))}
                      </div>
                      {text(content.comment) && <MarkdownRenderer content={text(content.comment)} />}
                    </div>
                  ) : isUpload ? (
                    <div className="space-y-2 text-sm">
                      <div className="flex flex-wrap items-center gap-2">
                        <Badge variant="outline" className="font-mono text-[10px]">{text(content.file_name) || text(content.fileName) || approval.detail || 'Attachment'}</Badge>
                        {(content.size_bytes || content.sizeBytes) && <span className="text-xs text-muted-foreground">{String(content.size_bytes || content.sizeBytes)} bytes</span>}
                        {(text(content.mime_type) || text(content.mimeType)) && <span className="text-xs text-muted-foreground">{text(content.mime_type) || text(content.mimeType)}</span>}
                      </div>
                      {text(content.initial_comment) && (
                        <div className="text-xs text-muted-foreground">Initial comment: {text(content.initial_comment)}</div>
                      )}
                      {text(content.preview) && <MarkdownRenderer content={text(content.preview)} />}
                    </div>
                  ) : (
                    text(content.comment) || approval.detail ? (
                      <div className="space-y-2">
                        {mentions.length > 0 && (
                          <div className="flex flex-wrap gap-1.5">
                            {mentions.map((mention) => (
                              <Badge key={text(mention.accountId) || text(mention.displayName)} variant="secondary" className="text-[10px]">
                                @{text(mention.displayName) || text(mention.accountId)}
                              </Badge>
                            ))}
                          </div>
                        )}
                        <MarkdownRenderer content={text(content.comment) || text(approval.detail)} />
                      </div>
                    ) : (
                      <span className="text-muted-foreground">No content preview available.</span>
                    )
                  )}
                </>
              )}
            </div>
          </div>
        </div>
        <div className="flex items-center justify-between gap-3">
          <div className="text-xs text-muted-foreground">
            {state === 'pending'
              ? approvalId
                ? `Approval ID ${approvalId}`
                : 'Approval ID unavailable'
              : detail}
          </div>
          {state === 'pending' ? (
            <div className="flex items-center gap-2">
              <Button type="button" variant="outline" size="sm" disabled={!!busy || !approvalId} onClick={() => void mutate('reject')}>
                {busy === 'reject' ? <Loader2 size={14} className="animate-spin" /> : <XCircle size={14} />}
                Reject
              </Button>
              <Button type="button" size="sm" disabled={!!busy || !approvalId} onClick={() => void mutate('approve')}>
                {busy === 'approve' ? <Loader2 size={14} className="animate-spin" /> : <CheckCircle2 size={14} />}
                Approve
              </Button>
            </div>
          ) : (
            <Badge variant="outline" className={state === 'approved' ? 'bg-emerald-500/10 text-emerald-600 border-emerald-500/25' : state === 'rejected' ? 'bg-muted text-muted-foreground border-border' : 'bg-destructive/10 text-destructive border-destructive/25'}>
              {state}
            </Badge>
          )}
        </div>
      </div>
    </Shell>
  );
}

const AtlassianToolResult: React.FC<AtlassianToolResultProps> = ({ envelope, fallbackName }) => {
  const widget = String(envelope.result_metadata?.widget || '');
  const product = String(envelope.result_metadata?.product || '');
  const toolName = envelope.tool_name || fallbackName || '';

  if (widget === 'approval') return <ApprovalWidget envelope={envelope} />;
  if (product === 'confluence' || toolName.startsWith('confluence_')) return <ConfluenceWidget envelope={envelope} />;
  if (widget === 'comment_list' || toolName === 'jira_get_issue_comments') return <JiraCommentsWidget envelope={envelope} />;
  return <JiraIssueWidget envelope={envelope} />;
};

export default AtlassianToolResult;
