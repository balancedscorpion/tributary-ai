import React from 'react';
import ReactMarkdown from 'react-markdown';
import rehypeKatex from 'rehype-katex';
import remarkBreaks from 'remark-breaks';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import { languageFromClassName } from '@/lib/markdownLanguage';
import { stripInternalCitationHandles } from '@/lib/markdownSanitizers';
import CodeBlock from './CodeBlock';

interface MarkdownRendererProps {
  content: string;
  stablePrefix?: string;
  unstableSuffix?: string;
  jiraBaseUrl?: string;
}

const JIRA_ISSUE_KEY_RE = /\b([A-Z][A-Z0-9]+-\d+)\b/g;
const MARKDOWN_CODE_SPAN_RE = /(```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]*`)/g;
const KATEX_OPTIONS = {
  strict: 'ignore',
  trust: false,
  throwOnError: false,
  output: 'htmlAndMathml',
  maxExpand: 256,
  maxSize: 500,
  errorColor: '#fca5a5',
} as const;

function normalizeJiraBaseUrl(value?: string): string | null {
  const raw = String(value || '').trim();
  if (!raw) return null;
  const withProtocol = /^[a-z][a-z0-9+.-]*:\/\//i.test(raw)
    ? raw
    : `https://${raw}`;
  try {
    const url = new URL(withProtocol);
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return null;
    url.search = '';
    url.hash = '';
    return url.toString();
  } catch {
    return null;
  }
}

function jiraIssueUrl(baseUrl: string, issueKey: string): string {
  return new URL(`/browse/${encodeURIComponent(issueKey)}`, baseUrl).toString();
}

function normalizeDisplayEnvironment(env: string, body: string): string {
  const base = env.replace(/\*$/, '');
  switch (base) {
    case 'equation':
      return body;
    case 'align':
    case 'multline':
      return `\\begin{aligned}${body}\\end{aligned}`;
    case 'gather':
      return `\\begin{gathered}${body}\\end{gathered}`;
    default:
      return `\\begin{${env}}${body}\\end{${env}}`;
  }
}

const DISPLAY_ENV_RE = /\\begin\{(equation\*?|align\*?|gather\*?|multline\*?)\}([\s\S]*?)\\end\{\1\}/g;
const DISPLAY_BRACKET_RE = /\\\[([\s\S]*?)\\\]/g;
const INLINE_PAREN_RE = /\\\(([\s\S]*?)\\\)/g;
const SINGLE_LINE_DOLLARS_RE = /\$\$([^\n]+?)\$\$/g;

/** Line-position context for a code-span-split part within the full message. */
interface PartLineContext {
  /** The part begins at the start of a line of the full message. */
  startsLine: boolean;
  /** The part ends at the very end of the full message. */
  endsText: boolean;
}

function isBlankOrIndent(value: string): boolean {
  return /^[ \t\r]*$/.test(value);
}

/**
 * Rewrites display-TeX matches into remark-math-friendly dollars.
 *
 * remark-math only renders display mode for math FLOW: `$$` fences on their
 * own lines (single-line `$$x$$` and anything mid-paragraph parse as inline
 * math-text). So a match that occupies a whole line becomes a fenced block,
 * re-indented with the line's leading whitespace so it stays inside list
 * items; a mid-line match degrades to inline `$$...$$`, which keeps sentence,
 * heading and table-row structure intact instead of splitting them.
 */
function rewriteDisplayMath(
  value: string,
  re: RegExp,
  toTex: (match: RegExpExecArray) => string,
  ctx: PartLineContext,
): string {
  re.lastIndex = 0;
  let out = '';
  let cursor = 0;
  let match: RegExpExecArray | null;
  while ((match = re.exec(value)) !== null) {
    const tex = toTex(match).trim();
    if (!tex) continue; // empty body: leave the original text untouched
    const start = match.index;
    const end = start + match[0].length;
    const lineStart = value.lastIndexOf('\n', start - 1) + 1;
    const indent = value.slice(lineStart, start);
    const lineEndIdx = value.indexOf('\n', end);
    const tail = value.slice(end, lineEndIdx === -1 ? value.length : lineEndIdx);
    const ownLine =
      isBlankOrIndent(indent) &&
      (lineStart > 0 || ctx.startsLine) &&
      isBlankOrIndent(tail) &&
      (lineEndIdx !== -1 || ctx.endsText);
    out += value.slice(cursor, start);
    if (ownLine) {
      const body = tex.split('\n').map((line) => indent + line.trim()).join('\n');
      out += `$$\n${body}\n${indent}$$`;
    } else {
      out += `$$${tex.replace(/\s*\n\s*/g, ' ')}$$`;
    }
    cursor = end;
  }
  if (cursor === 0) return value;
  return out + value.slice(cursor);
}

/**
 * Escapes `$` characters that do not form a strict inline-math pair, so
 * remark-math's single-dollar rule cannot turn currency ("$5 and $10") into
 * math. A pair is kept only when the opener touches a non-space character on
 * its right, the FIRST closing candidate touches a non-space on its left and
 * is not immediately followed by a digit (pandoc's rule, catches "$50-$60"),
 * and the pair sits on one line. `$$` blocks are copied through verbatim, and
 * `$` inside URL-shaped tokens is left alone so GFM autolink literals keep
 * working (escaping there would corrupt the link).
 */
function escapeLooseDollars(value: string): string {
  const n = value.length;
  let out = '';
  let i = 0;
  const isSpace = (ch: string | undefined) => ch === undefined || /\s/.test(ch);
  const inUrlToken = (idx: number): boolean => {
    let k = idx;
    while (k > 0 && !/\s/.test(value[k - 1])) k -= 1;
    const word = value.slice(k, idx);
    return word.includes('://') || word.startsWith('www.');
  };
  while (i < n) {
    const ch = value[i];
    if (ch === '\\') {
      out += value.slice(i, i + 2);
      i += 2;
      continue;
    }
    if (ch !== '$') {
      out += ch;
      i += 1;
      continue;
    }
    if (value[i + 1] === '$') {
      // $$ ... $$: display fences or explicit inline-double math; pass the
      // whole balanced span through untouched.
      let j = i + 2;
      let close = -1;
      while (j < n) {
        if (value[j] === '\\') {
          j += 2;
          continue;
        }
        if (value[j] === '$' && value[j + 1] === '$') {
          close = j;
          break;
        }
        j += 1;
      }
      if (close !== -1) {
        out += value.slice(i, close + 2);
        i = close + 2;
      } else {
        out += '$$';
        i += 2;
      }
      continue;
    }
    const next = value[i + 1];
    if (!isSpace(next)) {
      let j = i + 1;
      let close = -1;
      while (j < n) {
        const c = value[j];
        if (c === '\n' || c === '\r') break;
        if (c === '\\') {
          j += 2;
          continue;
        }
        if (c === '$') {
          close = j;
          break;
        }
        j += 1;
      }
      const before = close > 0 ? value[close - 1] : undefined;
      const after = close !== -1 ? value[close + 1] : undefined;
      if (
        close > i + 1 &&
        close - i <= 1000 &&
        !isSpace(before) &&
        !(after !== undefined && after >= '0' && after <= '9')
      ) {
        out += value.slice(i, close + 1);
        i = close + 1;
        continue;
      }
    }
    if (inUrlToken(i)) {
      out += '$';
      i += 1;
      continue;
    }
    out += '\\$';
    i += 1;
  }
  return out;
}

function normalizeTexDelimitersOutsideCode(value: string, ctx: PartLineContext): string {
  let out = rewriteDisplayMath(
    value,
    DISPLAY_ENV_RE,
    (m) => normalizeDisplayEnvironment(String(m[1]), String(m[2])),
    ctx,
  );
  out = rewriteDisplayMath(out, DISPLAY_BRACKET_RE, (m) => String(m[1]), ctx);
  // Whole-line $$...$$ written by the model is display intent: promote it to
  // fences. Mid-line occurrences are left for remark-math's inline handling.
  out = rewriteDisplayMath(out, SINGLE_LINE_DOLLARS_RE, (m) => String(m[1]), ctx);
  // \( ... \) is inline by definition; newlines collapse so the pair survives
  // the single-line rule below.
  out = out.replace(INLINE_PAREN_RE, (m, body) => {
    const tex = String(body).replace(/\s*\n\s*/g, ' ').trim();
    return tex ? `$${tex}$` : m;
  });
  return escapeLooseDollars(out);
}

function normalizeTexDelimitersForMarkdown(value: string): string {
  if (!value) return value;
  const parts = value.split(MARKDOWN_CODE_SPAN_RE);
  let atLineStart = true;
  return parts.map((part, index) => {
    if (!part) return part;
    const startsLine = atLineStart;
    atLineStart = part.endsWith('\n');
    if (part.startsWith('```') || part.startsWith('~~~') || part.startsWith('`')) return part;
    return normalizeTexDelimitersOutsideCode(part, {
      startsLine,
      endsText: index === parts.length - 1,
    });
  }).join('');
}

function createJiraIssueLinkPlugin(baseUrl: string | null) {
  return function jiraIssueLinkPlugin() {
    return (tree: any) => {
      if (!baseUrl) return;
      linkJiraIssueKeys(tree, baseUrl);
    };
  };
}

function linkJiraIssueKeys(node: any, baseUrl: string) {
  if (!node || typeof node !== 'object') return;
  const type = String(node.type || '');
  if (type === 'link' || type === 'linkReference' || type === 'code' || type === 'inlineCode') {
    return;
  }
  if (!Array.isArray(node.children)) return;

  const nextChildren: any[] = [];
  for (const child of node.children) {
    if (child?.type === 'text' && typeof child.value === 'string') {
      const parts = splitJiraIssueTextNode(child.value, baseUrl);
      nextChildren.push(...parts);
      continue;
    }
    linkJiraIssueKeys(child, baseUrl);
    nextChildren.push(child);
  }
  node.children = nextChildren;
}

function splitJiraIssueTextNode(value: string, baseUrl: string): any[] {
  const out: any[] = [];
  let lastIndex = 0;
  JIRA_ISSUE_KEY_RE.lastIndex = 0;
  let match: RegExpExecArray | null;
  while ((match = JIRA_ISSUE_KEY_RE.exec(value)) !== null) {
    const issueKey = match[1];
    if (match.index > lastIndex) {
      out.push({ type: 'text', value: value.slice(lastIndex, match.index) });
    }
    out.push({
      type: 'link',
      url: jiraIssueUrl(baseUrl, issueKey),
      title: null,
      children: [{ type: 'text', value: issueKey }],
    });
    lastIndex = match.index + issueKey.length;
  }
  if (lastIndex === 0) return [{ type: 'text', value }];
  if (lastIndex < value.length) {
    out.push({ type: 'text', value: value.slice(lastIndex) });
  }
  return out;
}

const markdownComponents: React.ComponentProps<typeof ReactMarkdown>['components'] = {
  pre({ children }) {
    // react-markdown wraps fenced code blocks in <pre><code/></pre>.
    // We render our own CodeBlock from <code>, so drop the extra wrapper.
    return <>{children}</>;
  },
  code({ inline, className, children, ...props }: any) {
    const language = languageFromClassName(className);
    const content = String(children ?? '');
    const isInline = typeof inline === 'boolean'
      ? inline
      : !language && !content.includes('\\n') && !content.includes('\n');

    if (!isInline) {
      return (
        <CodeBlock language={language}>
          {content}
        </CodeBlock>
      );
    }

    return (
      <code
        className="bg-muted px-1.5 py-0.5 rounded text-orange-300 font-mono text-sm border border-border break-all whitespace-pre-wrap"
        {...props}
      >
        {children}
      </code>
    );
  },
  table: ({ children }) => (
    <div className="my-4 max-w-full min-w-0 overflow-x-auto rounded-lg border border-border">
      <table className="w-full table-fixed divide-y divide-border">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-muted">{children}</thead>,
  th: ({ children }) => <th className="whitespace-normal px-4 py-3 text-left align-top text-xs font-semibold uppercase tracking-wider text-muted-foreground [overflow-wrap:anywhere] break-words">{children}</th>,
  tbody: ({ children }) => <tbody className="bg-card/20 divide-y divide-border">{children}</tbody>,
  tr: ({ children }) => <tr className="hover:bg-muted/50 transition-colors">{children}</tr>,
  td: ({ children }) => <td className="whitespace-normal px-4 py-2 align-top text-sm text-card-foreground [overflow-wrap:anywhere] break-words">{children}</td>,
  a: ({ ...props }) => (
    <a
      {...props}
      target="_blank"
      rel="noopener noreferrer"
      className="text-primary hover:underline"
    />
  ),
};

const MarkdownChunk = React.memo(function MarkdownChunk({
  content,
  jiraBaseUrl,
}: {
  content: string;
  jiraBaseUrl?: string;
}) {
  if (!content) return null;
  const normalizedJiraBaseUrl = normalizeJiraBaseUrl(jiraBaseUrl);
  const remarkPlugins = normalizedJiraBaseUrl
    ? [remarkGfm, remarkMath, remarkBreaks, createJiraIssueLinkPlugin(normalizedJiraBaseUrl)]
    : [remarkGfm, remarkMath, remarkBreaks];
  return (
    <div className="min-w-0 max-w-full">
      <ReactMarkdown
        skipHtml
        remarkPlugins={remarkPlugins}
        rehypePlugins={[[rehypeKatex, KATEX_OPTIONS]]}
        components={markdownComponents}
      >
        {content}
      </ReactMarkdown>
    </div>
  );
});

const MarkdownRenderer: React.FC<MarkdownRendererProps> = ({ content, stablePrefix, unstableSuffix, jiraBaseUrl }) => {
  const stable = typeof stablePrefix === 'string' ? stablePrefix : '';
  const unstable = typeof unstableSuffix === 'string' ? unstableSuffix : '';
  const hasStreamingSplit = stable.length > 0 || unstable.length > 0;
  const renderText = hasStreamingSplit
    ? `${stable}${unstable}`
    : content;
  const normalizedText = React.useMemo(
    () => normalizeTexDelimitersForMarkdown(stripInternalCitationHandles(renderText)),
    [renderText],
  );

  return (
    <div className="chat-markdown chat-text-wrap max-w-none leading-relaxed overflow-x-hidden">
      <MarkdownChunk content={normalizedText} jiraBaseUrl={jiraBaseUrl} />
    </div>
  );
};

export default MarkdownRenderer;
