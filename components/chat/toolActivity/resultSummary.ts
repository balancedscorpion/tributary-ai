import type { ToolCall, ToolResultEnvelope } from '@/types';
import { extractToolErrorDiagnosticFromEnvelope } from '@/lib/toolErrorDiagnostics';
import { getJiraToolResultSummary } from '@/components/renderers/tool/jira/summary';
import {
  classifyGeneratedImageError,
  imageFormatFromMime,
  isImageGenerationToolName,
  type GeneratedImageErrorCategory,
} from '@/lib/generatedImageArtifacts';
import {
  arxivAuditSummary,
  arxivQueryOutcomeLabel,
  arxivScreeningSummary,
  buildArxivRunModelFromEnvelope,
} from './arxivAuditModel';

/**
 * One-line result summary for a completed tool call — the card subtitle in
 * the activity disclosure ("Operational", "3 repositories · acme/api", …).
 * Heuristic, best-effort: explicit message-ish fields first, then list-shaped
 * payload counts, then plain-text first line. Empty string when nothing
 * presentable exists (the card then shows the args preview instead).
 */

function compactText(value: unknown, maxLength = 90): string {
  const text = String(value ?? '').replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.length > maxLength ? `${text.slice(0, maxLength - 1).trimEnd()}…` : text;
}

function isPlainRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function capitalize(text: string): string {
  return text ? text.charAt(0).toUpperCase() + text.slice(1) : text;
}

function singularize(word: string): string {
  if (/ies$/i.test(word)) return word.replace(/ies$/i, 'y');
  if (/s$/i.test(word) && !/ss$/i.test(word)) return word.replace(/s$/i, '');
  return word;
}

function humanizeFieldName(field: string): string {
  return field.replace(/[_-]+/g, ' ').replace(/\s+/g, ' ').trim().toLowerCase();
}

function itemDisplayName(item: unknown): string {
  if (!isPlainRecord(item)) return '';
  for (const key of ['full_name', 'fullName', 'name', 'key', 'title', 'summary', 'id']) {
    const value = item[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

/** Result fields that read as a human status/summary line, in priority order. */
const MESSAGE_FIELDS = [
  'message',
  'summary',
  'status',
  'state',
  'detail',
  'description',
  'title',
] as const;

const LIST_FIELDS_PRIORITY = [
  'repositories',
  'results',
  'issues',
  'pages',
  'items',
  'rows',
  'files',
  'channels',
  'messages',
  'comments',
  'records',
  'candidates',
] as const;

/**
 * Locator-grammar one-liner for paper read tools ("Read pp. 7–9 / 24 · Title",
 * "Read 20,000 chars from 12,000 / 84,213 · Title"). Same grammar as the read
 * card's locator line so the trail reads consistently at every zoom level.
 */
function getArxivReadResultSummary(
  toolCall: ToolCall,
  envelope: ToolResultEnvelope,
): string {
  const result = isPlainRecord(envelope.result) ? envelope.result : null;
  if (!result) return '';

  if (toolCall.name === 'arxiv_read_paper' && isPlainRecord(result.paper_pages)) {
    const pages = result.paper_pages;
    const startPage = Number(pages.start_page ?? pages.startPage);
    const endPage = Number(pages.end_page ?? pages.endPage);
    const pageCount = Number(pages.page_count ?? pages.pageCount);
    const title = compactText(pages.title, 70);
    const range =
      Number.isFinite(startPage) && startPage > 0 && Number.isFinite(endPage) && endPage >= startPage
        ? `${startPage === endPage ? `p. ${startPage}` : `pp. ${startPage}–${endPage}`}${Number.isFinite(pageCount) && pageCount > 0 ? ` / ${pageCount}` : ''}`
        : 'paper pages';
    return title ? `Read ${range} · ${title}` : `Read ${range}`;
  }

  if (toolCall.name === 'arxiv_get_paper_text' && isPlainRecord(result.text_window)) {
    const win = result.text_window;
    const offset = Number(win.offset);
    const totalChars = Number(win.total_chars ?? win.totalChars);
    const title = compactText(win.title, 70);
    const where = Number.isFinite(offset) && offset > 0 ? `from ${offset.toLocaleString()}` : '';
    const of = Number.isFinite(totalChars) && totalChars > 0 ? `/ ${totalChars.toLocaleString()} chars` : '';
    const range = ['Read text', where, of].filter(Boolean).join(' ');
    return title ? `${range} · ${title}` : range;
  }

  return '';
}

const GENERATED_IMAGE_ERROR_SUMMARIES: Record<GeneratedImageErrorCategory, string> = {
  auth: 'Image target needs attention',
  rate_limited: 'Rate limited',
  moderation_blocked: 'Blocked by content policy',
  invalid_input: 'Invalid image request',
  provider_unavailable: 'Image provider unavailable',
  cancelled: 'Generation cancelled',
  unknown: 'Image generation failed',
};

/**
 * generate_image summary — owned outright so the (multi-MB) data-URI content
 * or a raw provider status string can never reach compactText. Success reads
 * "Generated 1536×1024 PNG"; failure reads a short error category.
 */
function getGenerateImageSummary(envelope: ToolResultEnvelope): string {
  if (envelope.ok === false) {
    return GENERATED_IMAGE_ERROR_SUMMARIES[classifyGeneratedImageError(envelope)];
  }
  const result = isPlainRecord(envelope.result) ? envelope.result : {};
  const width = Number(result.width ?? result.image_width);
  const height = Number(result.height ?? result.image_height);
  const format = imageFormatFromMime(
    String(result.mime_type ?? result.mimeType ?? ''),
  ).toUpperCase();
  if (Number.isFinite(width) && width > 0 && Number.isFinite(height) && height > 0) {
    return format ? `Generated ${width}×${height} ${format}` : `Generated ${width}×${height}`;
  }
  return 'Image generated';
}

export function getToolResultSummary(
  toolCall: ToolCall,
  envelope?: ToolResultEnvelope,
): string {
  if (!envelope) return '';

  // generate_image owns its summary before any generic branch so no data URI
  // or raw provider status leaks into the card subtitle.
  if (
    isImageGenerationToolName(toolCall.name) ||
    isImageGenerationToolName(envelope.tool_name)
  ) {
    return getGenerateImageSummary(envelope);
  }

  // Failed calls summarize as the structured diagnostic headline.
  if (envelope.ok === false) {
    const diagnostic = extractToolErrorDiagnosticFromEnvelope(envelope, {
      fallbackTitle: envelope.tool_name || toolCall.name,
    });
    if (diagnostic) {
      return compactText(diagnostic.message || diagnostic.title, 110);
    }
  }

  // Jira/Atlassian bridge payloads carry no top-level message-ish string, so
  // without a family summary the generic list scan below lands on the
  // always-present (often empty) `errors` array — "0 errors" tells the user
  // nothing. The Jira helper covers every bridge widget with a scannable line
  // ("STREAM-4821 · 2 sprint transitions", "GET /rest/api/3/… · 200").
  if (envelope.ok !== false) {
    // Read tools first: their locator line ("Read pp. 7–9 / 24 · Title") must
    // win before the generic arXiv funnel summary gets a chance to swallow it.
    const readSummary = getArxivReadResultSummary(toolCall, envelope);
    if (readSummary) return compactText(readSummary, 120);

    const arxivModel = buildArxivRunModelFromEnvelope(toolCall, envelope);
    if (toolCall.name === 'arxiv_screening_report') {
      const screeningSummary = arxivScreeningSummary(arxivModel);
      if (screeningSummary) return compactText(screeningSummary, 120);
    } else if (toolCall.name === 'arxiv_search') {
      const querySummary = arxivQueryOutcomeLabel(arxivModel?.queries[0]);
      if (querySummary) return compactText(querySummary, 120);
    } else {
      const arxivSummary = arxivAuditSummary(arxivModel);
      if (arxivSummary) return compactText(arxivSummary, 120);
    }

    const jiraSummary = getJiraToolResultSummary(
      {
        tool_name: envelope.tool_name || toolCall.name,
        result_metadata: envelope.result_metadata,
      },
      envelope.result,
    );
    if (jiraSummary) return compactText(jiraSummary, 110);
  }

  const result = envelope.result;

  if (typeof result === 'string') {
    const firstLine = result.split(/\r?\n/).find((line) => line.trim()) || '';
    return compactText(firstLine);
  }

  if (Array.isArray(result)) {
    const count = result.length;
    const name = itemDisplayName(result[0]);
    const noun = count === 1 ? 'item' : 'items';
    return compactText(name ? `${count} ${noun} · ${name}` : `${count} ${noun}`);
  }

  if (!isPlainRecord(result)) return '';

  if (typeof result.error === 'string' && result.error.trim()) {
    return compactText(result.error, 110);
  }

  for (const field of MESSAGE_FIELDS) {
    const value = result[field];
    if (typeof value === 'string' && value.trim()) {
      return compactText(field === 'status' || field === 'state' ? capitalize(value.trim()) : value);
    }
  }

  // List-shaped payloads: "2 repositories · acme/api". Auto-discovered fields
  // must be non-empty and never diagnostics ("0 errors"/"0 warnings" on a
  // success is noise); only the curated priority fields may summarize as a
  // meaningful zero (e.g. "0 results").
  const listFields: string[] = [...LIST_FIELDS_PRIORITY];
  for (const key of Object.keys(result)) {
    if (listFields.includes(key) || !Array.isArray(result[key])) continue;
    if (/^(errors|warnings)$/i.test(key)) continue;
    if (result[key].length === 0) continue;
    listFields.push(key);
  }
  for (const field of listFields) {
    const value = result[field];
    if (!Array.isArray(value)) continue;
    const count = value.length;
    const nounBase = humanizeFieldName(field) || 'items';
    const noun = count === 1 ? singularize(nounBase) : nounBase;
    const name = itemDisplayName(value[0]);
    return compactText(name ? `${count} ${noun} · ${name}` : `${count} ${noun}`);
  }

  // Scalar metadata of last resort: row counts stamped on the envelope.
  if (typeof envelope.row_count_total === 'number' && Number.isFinite(envelope.row_count_total)) {
    const rows = envelope.row_count_total;
    return `${rows.toLocaleString()} ${rows === 1 ? 'row' : 'rows'}`;
  }

  return '';
}
