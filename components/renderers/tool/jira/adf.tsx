import React from 'react';
import { CheckSquare, Paperclip, Square } from 'lucide-react';
import { cn } from '@/lib/utils';

/**
 * Minimal Atlassian Document Format (ADF) renderer.
 *
 * Covers the node/mark vocabulary Jira Cloud emits for issue descriptions and
 * comments (paragraphs, headings, lists, code blocks, panels, tables, task
 * lists, mentions, statuses…). Unknown nodes degrade to rendering their
 * children — and ultimately to plain text — so a new ADF node type can never
 * collapse the card into raw JSON.
 */

export interface AdfNode {
  type?: string;
  text?: string;
  attrs?: Record<string, any>;
  content?: AdfNode[];
  marks?: Array<{ type?: string; attrs?: Record<string, any> }>;
}

export function isAdfDoc(value: unknown): value is AdfNode {
  return (
    value !== null &&
    typeof value === 'object' &&
    !Array.isArray(value) &&
    (value as AdfNode).type === 'doc' &&
    Array.isArray((value as AdfNode).content)
  );
}

export function adfToText(node: AdfNode | null | undefined): string {
  if (!node || typeof node !== 'object') return '';
  if (node.type === 'text') return String(node.text || '');
  if (node.type === 'hardBreak') return '\n';
  if (node.type === 'mention' || node.type === 'emoji' || node.type === 'status') {
    return String(node.attrs?.text || node.attrs?.shortName || '');
  }
  const children = Array.isArray(node.content) ? node.content : [];
  const parts = children.map(adfToText);
  if (node.type === 'paragraph' || node.type === 'heading' || node.type === 'codeBlock') {
    return parts.join('');
  }
  if (node.type === 'tableRow') return parts.join(' | ');
  return parts.join('\n');
}

function safeUrl(value: unknown): string | null {
  const url = typeof value === 'string' ? value.trim() : '';
  return /^https?:\/\//i.test(url) ? url : null;
}

function TextLeaf({ node }: { node: AdfNode }) {
  let rendered: React.ReactNode = String(node.text || '');
  for (const mark of node.marks || []) {
    switch (mark?.type) {
      case 'strong':
        rendered = <strong className="font-semibold">{rendered}</strong>;
        break;
      case 'em':
        rendered = <em>{rendered}</em>;
        break;
      case 'code':
        rendered = (
          <code className="rounded bg-muted px-1 py-0.5 font-mono text-[0.85em]">{rendered}</code>
        );
        break;
      case 'strike':
        rendered = <s>{rendered}</s>;
        break;
      case 'underline':
        rendered = <u>{rendered}</u>;
        break;
      case 'subsup':
        rendered = mark.attrs?.type === 'sub' ? <sub>{rendered}</sub> : <sup>{rendered}</sup>;
        break;
      case 'link': {
        const href = safeUrl(mark.attrs?.href);
        if (href) {
          rendered = (
            <a
              href={href}
              target="_blank"
              rel="noreferrer"
              className="text-primary underline decoration-primary/40 underline-offset-2 hover:decoration-primary"
            >
              {rendered}
            </a>
          );
        }
        break;
      }
      default:
        // textColor, backgroundColor, alignment… — keep the content, drop the styling.
        break;
    }
  }
  return <>{rendered}</>;
}

function Children({ nodes }: { nodes?: AdfNode[] }) {
  return (
    <>
      {(Array.isArray(nodes) ? nodes : []).map((child, i) => (
        <AdfNodeView key={i} node={child} />
      ))}
    </>
  );
}

const STATUS_LOZENGE_TONES: Record<string, string> = {
  green: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400',
  blue: 'bg-blue-500/15 text-blue-700 dark:text-blue-400',
  red: 'bg-destructive/15 text-destructive',
  yellow: 'bg-amber-500/15 text-amber-700 dark:text-amber-400',
  purple: 'bg-purple-500/15 text-purple-700 dark:text-purple-400',
  neutral: 'bg-muted text-muted-foreground',
};

const PANEL_TONES: Record<string, string> = {
  info: 'border-l-blue-500/60 bg-blue-500/5',
  note: 'border-l-purple-500/60 bg-purple-500/5',
  warning: 'border-l-amber-500/60 bg-amber-500/5',
  error: 'border-l-destructive/60 bg-destructive/5',
  success: 'border-l-emerald-500/60 bg-emerald-500/5',
};

// Capped below the card's 15px summary so a description that opens with an
// ADF h1 never outranks the issue title.
const HEADING_CLASSES: Record<number, string> = {
  1: 'text-[14px] font-semibold',
  2: 'text-[13px] font-semibold',
  3: 'text-[13px] font-medium',
  4: 'text-[12px] font-medium',
  5: 'text-[12px] font-medium text-muted-foreground',
  6: 'text-[12px] font-medium uppercase tracking-wide text-muted-foreground',
};

function AdfNodeView({ node }: { node: AdfNode }) {
  if (!node || typeof node !== 'object') return null;
  switch (node.type) {
    case 'doc':
      return <Children nodes={node.content} />;
    case 'text':
      return <TextLeaf node={node} />;
    case 'hardBreak':
      return <br />;
    case 'paragraph':
      return (
        <p className="my-2 leading-relaxed first:mt-0 last:mb-0">
          <Children nodes={node.content} />
        </p>
      );
    case 'heading': {
      const level = Math.min(6, Math.max(1, Number(node.attrs?.level) || 1));
      // Card context: ADF h1 renders as an h4 so the document headings stay
      // below the issue summary in the accessibility outline.
      return React.createElement(
        `h${Math.min(6, level + 3)}`,
        { className: cn('mt-3 mb-1.5 first:mt-0', HEADING_CLASSES[level]) },
        <Children nodes={node.content} />,
      );
    }
    case 'bulletList':
      return (
        <ul className="my-2 list-disc space-y-1 pl-5 first:mt-0 last:mb-0">
          <Children nodes={node.content} />
        </ul>
      );
    case 'orderedList':
      return (
        <ol
          className="my-2 list-decimal space-y-1 pl-5 first:mt-0 last:mb-0"
          start={Number(node.attrs?.order) || undefined}
        >
          <Children nodes={node.content} />
        </ol>
      );
    case 'listItem':
      return (
        <li className="[&>p]:my-0.5">
          <Children nodes={node.content} />
        </li>
      );
    case 'codeBlock':
      return (
        <pre className="my-2 overflow-x-auto rounded-md border border-border bg-muted/40 px-3 py-2 font-mono text-[12px] leading-relaxed first:mt-0 last:mb-0">
          <code>{adfToText(node)}</code>
        </pre>
      );
    case 'blockquote':
      return (
        <blockquote className="my-2 border-l-2 border-border pl-3 text-muted-foreground first:mt-0 last:mb-0">
          <Children nodes={node.content} />
        </blockquote>
      );
    case 'rule':
      return <hr className="my-3 border-border" />;
    case 'panel': {
      const panelType = String(node.attrs?.panelType || 'info');
      const tone = PANEL_TONES[panelType] || PANEL_TONES.info;
      return (
        <div className={cn('my-2 rounded-md border border-border border-l-2 px-3 py-2 first:mt-0 last:mb-0', tone)}>
          {/* Panel type is otherwise conveyed by color alone. */}
          <span className="sr-only">{panelType} panel: </span>
          <Children nodes={node.content} />
        </div>
      );
    }
    case 'mention':
      return (
        <span className="rounded bg-primary/10 px-1 py-0.5 text-[0.9em] font-medium text-primary">
          {String(node.attrs?.text || `@${node.attrs?.id || 'user'}`)}
        </span>
      );
    case 'emoji':
      return <span>{String(node.attrs?.text || node.attrs?.shortName || '')}</span>;
    case 'status': {
      const tone = STATUS_LOZENGE_TONES[String(node.attrs?.color || '')] || STATUS_LOZENGE_TONES.neutral;
      return (
        <span className={cn('rounded px-1.5 py-0.5 text-[0.8em] font-semibold uppercase tracking-wide', tone)}>
          {String(node.attrs?.text || '')}
        </span>
      );
    }
    case 'date': {
      const ts = Number(node.attrs?.timestamp);
      if (!Number.isFinite(ts)) return null;
      return <span>{new Date(ts).toLocaleDateString()}</span>;
    }
    case 'inlineCard':
    case 'blockCard':
    case 'embedCard': {
      const href = safeUrl(node.attrs?.url);
      if (!href) return <Children nodes={node.content} />;
      return (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          className="text-primary underline decoration-primary/40 underline-offset-2 hover:decoration-primary [overflow-wrap:anywhere]"
        >
          {href}
        </a>
      );
    }
    case 'taskList':
      return (
        <ul className="my-2 space-y-1 first:mt-0 last:mb-0">
          <Children nodes={node.content} />
        </ul>
      );
    case 'taskItem': {
      const done = String(node.attrs?.state || '').toUpperCase() === 'DONE';
      return (
        <li className="flex items-start gap-2">
          {done ? (
            <CheckSquare size={14} className="mt-0.5 shrink-0 text-emerald-600" aria-hidden />
          ) : (
            <Square size={14} className="mt-0.5 shrink-0 text-muted-foreground" aria-hidden />
          )}
          <span className={cn('min-w-0', done && 'text-muted-foreground line-through')}>
            {/* State must not ride on the svg (unreliable for AT) or on color alone. */}
            <span className="sr-only">{done ? 'Done: ' : 'To do: '}</span>
            <Children nodes={node.content} />
          </span>
        </li>
      );
    }
    case 'mediaSingle':
    case 'mediaGroup':
      return (
        <div className="my-2 first:mt-0 last:mb-0">
          <Children nodes={node.content} />
        </div>
      );
    case 'media':
      return (
        <span className="inline-flex items-center gap-1.5 rounded-md border border-dashed border-border px-2 py-1 text-[11px] text-muted-foreground">
          <Paperclip size={11} aria-hidden />
          {String(node.attrs?.alt || node.attrs?.id || 'Attachment')}
        </span>
      );
    case 'expand':
    case 'nestedExpand':
      return (
        <details className="my-2 rounded-md border border-border px-3 py-2 first:mt-0 last:mb-0">
          <summary className="cursor-pointer text-[12px] font-medium text-muted-foreground">
            {String(node.attrs?.title || 'Expand')}
          </summary>
          <div className="mt-1.5">
            <Children nodes={node.content} />
          </div>
        </details>
      );
    case 'table':
      return (
        <div className="my-2 overflow-x-auto rounded-md border border-border first:mt-0 last:mb-0">
          <table className="w-full border-collapse text-[12px]">
            <tbody>
              <Children nodes={node.content} />
            </tbody>
          </table>
        </div>
      );
    case 'tableRow':
      return (
        <tr className="border-b border-border last:border-b-0 [&:has(th)]:bg-muted/40">
          <Children nodes={node.content} />
        </tr>
      );
    case 'tableHeader':
      return (
        <th
          scope="col"
          className="border-r border-border px-2 py-1.5 text-left font-medium last:border-r-0 [&>p]:my-0"
        >
          <Children nodes={node.content} />
        </th>
      );
    case 'tableCell':
      return (
        <td className="border-r border-border px-2 py-1.5 align-top last:border-r-0 [&>p]:my-0">
          <Children nodes={node.content} />
        </td>
      );
    default:
      // Unknown node: render children if any, else fall back to its text.
      if (Array.isArray(node.content) && node.content.length > 0) {
        return <Children nodes={node.content} />;
      }
      return <>{adfToText(node)}</>;
  }
}

export function AdfContent({ doc, className }: { doc: AdfNode; className?: string }) {
  return (
    <div className={cn('text-[13px] text-foreground [overflow-wrap:anywhere]', className)}>
      <Children nodes={doc.content} />
    </div>
  );
}
