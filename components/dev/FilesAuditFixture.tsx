import React from 'react';
import { TooltipProvider } from '@/components/ui/tooltip';
import FilesAuditView from '@/components/files/FilesAuditView';
import ArtifactPanel from '@/components/ArtifactPanel';
import InspectorOverlay from '@/components/InspectorOverlay';
import CollapsedToolsSummary from '@/components/chat/CollapsedToolsSummary';
import { DEFAULT_SETTINGS } from '@/constants';
import type { Artifact, ToolCall, ToolResultEnvelope } from '@/types';
import { buildRemoteFileDownloadPrompt, fileArtifactMimeType, type FileArtifactProvenance } from '@/lib/fileProvenance';
import {
  markRemoteFileDownloadRequested,
  notifyRemoteFileDownloadRunCompleted,
} from '@/lib/remoteFileDownloadRequests';
import type { FileAuditRecord } from '@/services/fileAuditApi';
import { requestToolCallReveal } from '@/lib/toolCallReveal';

type FixtureRecord = Record<string, any>;

const THREAD_ID = 'thread-files-fixture';
const THREAD_TITLE = 'Connector file review';

function nowMinus(minutes: number): string {
  return new Date(Date.now() - minutes * 60_000).toISOString();
}

function isPlainRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value !== 'string') continue;
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return '';
}

function fixturePdfBytes(): Uint8Array {
  const stream = 'BT /F1 14 Tf 36 96 Td (arXiv fixture PDF) Tj ET';
  const objects = [
    '1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n',
    '2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n',
    '3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 144] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>\nendobj\n',
    '4 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n',
    `5 0 obj\n<< /Length ${stream.length} >>\nstream\n${stream}\nendstream\nendobj\n`,
  ];
  let body = '%PDF-1.4\n';
  const offsets: number[] = [];
  for (const object of objects) {
    offsets.push(body.length);
    body += object;
  }
  const xrefStart = body.length;
  body += `xref\n0 ${objects.length + 1}\n`;
  body += '0000000000 65535 f \n';
  for (const offset of offsets) {
    body += `${String(offset).padStart(10, '0')} 00000 n \n`;
  }
  body += `trailer\n<< /Size ${objects.length + 1} /Root 1 0 R >>\nstartxref\n${xrefStart}\n%%EOF\n`;
  return new TextEncoder().encode(body);
}

function fileArtifact(
  id: string,
  title: string,
  toolName: string,
  sourceLabel: string,
  family: string,
  mimeType: string,
  body: string,
  extraMetadata: Record<string, any> = {},
): Artifact {
  // Nested result_metadata overrides merge into the envelope defaults so a
  // fixture can add connector-native keys without losing the shared shape.
  const { result_metadata: resultMetadataOverrides, ...topLevelExtras } = extraMetadata;
  return {
    id,
    type: 'file',
    title,
    content: '',
    metadata: {
      tool_name: toolName,
      source: sourceLabel === 'Chat upload' ? 'chat_upload' : 'external_tool',
      externalToolFile: sourceLabel !== 'Chat upload',
      external_tool_file: sourceLabel !== 'Chat upload',
      mimeType,
      mime_type: mimeType,
      detectedMimeType: mimeType,
      detected_mime_type: mimeType,
      isText: true,
      is_text: true,
      previewAvailable: true,
      preview_available: true,
      sizeBytes: body.length,
      size_bytes: body.length,
      result_metadata: {
        integration: family === 'jira' ? 'atlassian' : family,
        family,
        product: family,
        source_tool_name: toolName,
        tool_name: toolName,
        kind: 'file',
        result_kind: 'file',
        native_viewer: 'file',
        renderer_strategy: 'artifact_type',
        renderer_key: 'file',
        file_capability: sourceLabel === 'Chat upload' ? 'attachment_read' : 'remote_file_download',
        external_tool_file: sourceLabel !== 'Chat upload',
        native_file_input: true,
        model_file_input: true,
        attachment_artifact_id: id,
        file_artifact_id: id,
        ...(isPlainRecord(resultMetadataOverrides) ? resultMetadataOverrides : {}),
      },
      tool_presentation: {
        family,
        result_kind: 'file',
        renderer_strategy: 'artifact_type',
        renderer_key: 'file',
        native_viewer: 'file',
        artifact_types: ['file'],
        file_capability: sourceLabel === 'Chat upload' ? 'attachment_read' : 'remote_file_download',
        produces_file_artifact: true,
        promotes_native_input: true,
      },
      ...topLevelExtras,
    },
  };
}

const fileBodies: Record<string, string> = {
  'file-chat-upload': [
    '# Local upload notes',
    '',
    'The user uploaded this markdown file directly into the chat.',
    'It should be visible in this chat and in All Files.',
  ].join('\n'),
  'file-jira-attachment': [
    '# Jira attachment',
    '',
    'Incident bundle for STREAM-4821.',
    'The source should read as Jira and retain the attachment provenance.',
  ].join('\n'),
  'file-sharepoint-plan': [
    '# SharePoint roadmap',
    '',
    'Gate 1: finish migration readiness by July 15.',
    'Gate 2: validate partner dependencies by August 5.',
  ].join('\n'),
  'file-slack-export': [
    '# Slack export',
    '',
    'Canary green. Pipeline output came from a shared Slack file.',
  ].join('\n'),
  'file-arxiv-paper': [
    '# arXiv PDF text preview',
    '',
    'Sparse Attention Models should still route through the native file viewer.',
  ].join('\n'),
};

const fixturePdfBlob = new Blob([fixturePdfBytes().buffer as ArrayBuffer], { type: 'application/pdf' });

const fixtureRawBodies: Record<string, string | Blob> = {
  'file-chat-upload': fileBodies['file-chat-upload'],
  'file-jira-attachment': fileBodies['file-jira-attachment'],
  'file-sharepoint-plan': fileBodies['file-sharepoint-plan'],
  'file-slack-export': fileBodies['file-slack-export'],
  'file-arxiv-paper': fixturePdfBlob,
  'file-slack-scan': fixturePdfBlob,
};

function createArtifacts(): Record<string, Artifact> {
  return {
    'file-chat-upload': fileArtifact(
      'file-chat-upload',
      'local-upload-notes.md',
      'thread_file_attachment',
      'Chat upload',
      'attachments',
      'text/markdown',
      fileBodies['file-chat-upload'],
      { fileKind: 'markdown', file_kind: 'markdown' },
    ),
    'file-jira-attachment': fileArtifact(
      'file-jira-attachment',
      'STREAM-4821-incident.md',
      'jira_read_attachment',
      'Jira',
      'jira',
      'text/markdown',
      fileBodies['file-jira-attachment'],
      {
        managedLocalPath: 'atlassian/downloads/jira/STREAM-4821/incident.md',
        tool_args: { issueKey: 'STREAM-4821', attachmentId: '10032' },
      },
    ),
    'file-sharepoint-plan': fileArtifact(
      'file-sharepoint-plan',
      'Q3 Roadmap.md',
      'sharepoint_read',
      'SharePoint',
      'sharepoint',
      'text/markdown',
      fileBodies['file-sharepoint-plan'],
      {
        managedLocalPath: 'sharepoint/downloads/q3-roadmap.md',
        tool_args: {
          item_url: 'https://contoso.sharepoint.com/sites/team/Shared%20Documents/Roadmaps/Q3%20Roadmap.md',
          server_relative_url: '/sites/team/Shared Documents/Roadmaps/Q3 Roadmap.md',
          drive_path: '/Shared Documents/Roadmaps/Q3 Roadmap.md',
        },
      },
    ),
    'file-slack-export': fileArtifact(
      'file-slack-export',
      'pipeline-export.md',
      'slack_download_file',
      'Slack',
      'slack',
      'text/markdown',
      fileBodies['file-slack-export'],
      {
        managedLocalPath: 'slack/downloads/F123/pipeline-export.md',
        // token + local_path exercise the provenance detail hardening:
        // secrets must be redacted and path duplicates skipped.
        tool_args: {
          channel: 'C0STREAM',
          file_id: 'F123',
          token: 'xoxb-secret-value',
          local_path: 'slack/downloads/F123/pipeline-export.md',
        },
        source_url: 'https://example.slack.com/files/F123/pipeline-export.md',
      },
    ),
    'file-arxiv-paper': fileArtifact(
      'file-arxiv-paper',
      '2301.12345.pdf',
      'arxiv_download_paper',
      'arXiv',
      'arxiv',
      'application/pdf',
      fileBodies['file-arxiv-paper'],
      {
        fileKind: 'pdf',
        file_kind: 'pdf',
        sizeBytes: fixturePdfBlob.size,
        size_bytes: fixturePdfBlob.size,
        managedLocalPath: 'arxiv/papers/2301.12345/paper.pdf',
        tool_args: { arxiv_id: '2301.12345' },
        source_url: 'https://arxiv.org/pdf/2301.12345',
      },
    ),
    // Connector-native metadata only: MIME/kind live solely in nested
    // result_metadata (Slack's `mimetype`/`filetype`) and the title has no
    // extension, so the file renderer and card presentation must route this
    // PDF from the nested keys alone.
    'file-slack-scan': fileArtifact(
      'file-slack-scan',
      'Quarterly Close Scan',
      'slack_download_file',
      'Slack',
      'slack',
      '',
      '',
      {
        isText: false,
        is_text: false,
        sizeBytes: fixturePdfBlob.size,
        size_bytes: fixturePdfBlob.size,
        managedLocalPath: 'slack/downloads/F999/quarterly-close-scan.pdf',
        tool_args: { channel: 'C0STREAM', file_id: 'F999' },
        result_metadata: {
          name: 'Quarterly Close Scan',
          mimetype: 'application/pdf',
          filetype: 'pdf',
          file_id: 'F999',
          url_private_download: 'https://files.slack.com/files-pri/T123-F999/download/quarterly-close-scan.pdf',
          permalink: 'https://example.slack.com/files/F999/quarterly-close-scan',
        },
      },
    ),
  };
}

// The backend's connector-specific composer prompt for the remote reference.
// Deliberately unlike the generic frontend rebuild so tests can prove the
// backend prompt survives normalization verbatim.
const REMOTE_DOWNLOAD_PROMPT = 'Call sharepoint_download_file with drive_path "/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx" (item SPDOC-8842) to cache "Vendor SOW.docx" as a file artifact so it opens in Files.';

// A connector search surfaced this document, but its bytes were never
// downloaded: no artifact, no preview, no managed path. The audit surface
// must present it as remote-only with a request-download action instead of
// pretending it can open bytes it does not have.
function remoteOnlyRecord(): FixtureRecord {
  return {
    id: 'file-sharepoint-remote',
    artifact_id: 'file-sharepoint-remote',
    thread_id: THREAD_ID,
    thread_title: THREAD_TITLE,
    type: 'file',
    title: 'Vendor SOW.docx',
    source: 'external_tool',
    source_label: 'SharePoint',
    source_tool_name: 'sharepoint_search',
    tool_name: 'sharepoint_search',
    tool_call_id: 'file-sharepoint-remote-call',
    file_kind: 'docx',
    mime_type: 'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    byte_size: 48213,
    // Field names mirror the backend's remote_reference emission.
    record_kind: 'remote_reference',
    availability: 'remote_only',
    cached: false,
    openable: false,
    can_request_download: true,
    removable: true,
    remote_id: 'SPDOC-8842',
    remote_url: 'https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx',
    remote_path: '/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx',
    request_tool_name: 'sharepoint_download_file',
    request_tool_args: {
      drive_path: '/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx',
      remote_id: 'SPDOC-8842',
      source_url: 'https://contoso.sharepoint.com/sites/streaming/Shared Documents/Contracts/Vendor SOW.docx',
    },
    request_download_prompt: REMOTE_DOWNLOAD_PROMPT,
    preview_available: false,
    downloadable: false,
    created_at: nowMinus(2),
    metadata: {
      tool_args: { query: 'vendor SOW contracts' },
    },
  };
}

// Same remote-only shape, but the backend forbids the ask-agent download
// (can_request_download: false — e.g. the connector cannot export the item).
// The row must show remote-only status with no download CTA at all.
// Also path-only: the connector emitted a server-relative remote_path but no
// web URL, so remote_path is the record's only locator and must be preserved,
// displayed, and searchable.
function blockedRemoteRecord(): FixtureRecord {
  return {
    id: 'file-sharepoint-remote-blocked',
    artifact_id: 'file-sharepoint-remote-blocked',
    thread_id: THREAD_ID,
    thread_title: THREAD_TITLE,
    type: 'file',
    title: 'Legal Hold.pdf',
    source: 'external_tool',
    source_label: 'SharePoint',
    source_tool_name: 'sharepoint_search',
    tool_name: 'sharepoint_search',
    tool_call_id: 'file-sharepoint-remote-blocked-call',
    file_kind: 'pdf',
    mime_type: 'application/pdf',
    byte_size: 91544,
    record_kind: 'remote_reference',
    availability: 'remote_only',
    cached: false,
    openable: false,
    can_request_download: false,
    removable: true,
    remote_id: 'SPDOC-9017',
    remote_path: '/sites/streaming/Shared Documents/Legal/Legal Hold.pdf',
    preview_available: false,
    downloadable: false,
    created_at: nowMinus(3),
    metadata: {
      tool_args: {
        query: 'legal hold notices',
        // Over the 80-char Request-row display cap: QA that the clipped
        // value stays reachable through its copy control.
        select_fields: 'Title,Path,Author,Modified,ContentType,DocumentLink,SiteName,LibraryName,RetentionLabel,ComplianceTag',
      },
    },
  };
}

function createRecords(artifacts: Record<string, Artifact>): FixtureRecord[] {
  return [
    remoteOnlyRecord(),
    blockedRemoteRecord(),
    recordFor(artifacts['file-chat-upload'], 'Chat upload', 4),
    recordFor(artifacts['file-jira-attachment'], 'Jira', 8),
    recordFor(artifacts['file-sharepoint-plan'], 'SharePoint', 12),
    recordFor(artifacts['file-slack-export'], 'Slack', 16),
    recordFor(artifacts['file-arxiv-paper'], 'arXiv', 20),
  ];
}

function recordFor(artifact: Artifact, sourceLabel: string, minutesAgo: number): FixtureRecord {
  const metadata = artifact.metadata || {};
  const resultMetadata = isPlainRecord(metadata.result_metadata) ? metadata.result_metadata : {};
  const toolArgs = isPlainRecord(metadata.tool_args) ? metadata.tool_args : {};
  const remoteUrl = firstString(
    metadata.remote_url,
    metadata.remoteUrl,
    metadata.source_url,
    metadata.sourceUrl,
    metadata.item_url,
    metadata.itemUrl,
    resultMetadata.remote_url,
    resultMetadata.remoteUrl,
    resultMetadata.source_url,
    resultMetadata.sourceUrl,
    resultMetadata.item_url,
    resultMetadata.itemUrl,
    toolArgs.remote_url,
    toolArgs.remoteUrl,
    toolArgs.source_url,
    toolArgs.sourceUrl,
    toolArgs.item_url,
    toolArgs.itemUrl,
  );
  const remotePath = firstString(
    metadata.remote_path,
    metadata.remotePath,
    metadata.server_relative_url,
    metadata.serverRelativeUrl,
    metadata.drive_path,
    metadata.drivePath,
    resultMetadata.remote_path,
    resultMetadata.remotePath,
    resultMetadata.server_relative_url,
    resultMetadata.serverRelativeUrl,
    resultMetadata.drive_path,
    resultMetadata.drivePath,
    toolArgs.remote_path,
    toolArgs.remotePath,
    toolArgs.server_relative_url,
    toolArgs.serverRelativeUrl,
    toolArgs.drive_path,
    toolArgs.drivePath,
  );
  const remoteRef = firstString(
    metadata.remote_id,
    metadata.remoteId,
    metadata.file_id,
    metadata.fileId,
    metadata.attachmentId,
    metadata.attachment_id,
    metadata.arxiv_id,
    metadata.arxivId,
    resultMetadata.remote_id,
    resultMetadata.remoteId,
    resultMetadata.file_id,
    resultMetadata.fileId,
    resultMetadata.attachmentId,
    resultMetadata.attachment_id,
    resultMetadata.arxiv_id,
    resultMetadata.arxivId,
    toolArgs.remote_id,
    toolArgs.remoteId,
    toolArgs.file_id,
    toolArgs.fileId,
    toolArgs.attachmentId,
    toolArgs.attachment_id,
    toolArgs.arxiv_id,
    toolArgs.arxivId,
  );
  return {
    id: artifact.id,
    artifact_id: artifact.id,
    thread_id: THREAD_ID,
    thread_title: THREAD_TITLE,
    type: 'file',
    title: artifact.title,
    source: metadata.source,
    source_label: sourceLabel,
    source_tool_name: metadata.tool_name,
    tool_name: metadata.tool_name,
    tool_call_id: `${artifact.id}-call`,
    file_kind: metadata.fileKind || metadata.file_kind || 'markdown',
    mime_type: metadata.mimeType || metadata.mime_type,
    byte_size: metadata.sizeBytes || metadata.size_bytes,
    content_storage: 'file',
    content_path: String(metadata.managedLocalPath || metadata.managed_local_path || ''),
    managed_local_path: String(metadata.managedLocalPath || metadata.managed_local_path || ''),
    preview_text: fileBodies[artifact.id]?.slice(0, 180) || '',
    preview_available: true,
    preview_truncated: false,
    downloadable: true,
    created_at: nowMinus(minutesAgo),
    product: metadata.result_metadata?.product,
    family: metadata.result_metadata?.family,
    integration: metadata.result_metadata?.integration,
    ...(remoteUrl ? { remote_url: remoteUrl, source_url: remoteUrl } : {}),
    ...(remotePath ? { remote_path: remotePath } : {}),
    ...(remoteRef ? { remote_ref: remoteRef } : {}),
    metadata,
    artifact,
  };
}

// ---- Origin transcript ----------------------------------------------------
// A completed file-handling turn rendered through the production tool-activity
// group. The SharePoint call owns `file-sharepoint-plan-call`, so the audit
// surface's "open origin chat" action can prove the reveal path: pill
// auto-expands, the card anchor scrolls into view and flashes. The other calls
// keep direct-open file provenance covered for every connector source.

const ORIGIN_TOOL_CALLS: ToolCall[] = [
  {
    id: 'file-chat-upload-call',
    name: 'thread_file_attachment',
    args: { file_name: 'local-upload-notes.md' },
  },
  {
    id: 'file-jira-attachment-call',
    name: 'jira_read_attachment',
    args: { issueKey: 'STREAM-4821', attachmentId: '10032' },
  },
  {
    id: 'file-sharepoint-plan-call',
    name: 'sharepoint_read',
    args: { drive_path: '/Shared Documents/Roadmaps/Q3 Roadmap.md' },
  },
  {
    id: 'file-slack-export-call',
    name: 'slack_download_file',
    args: { channel: 'C0STREAM', file_id: 'F123' },
  },
  {
    id: 'file-arxiv-paper-call',
    name: 'arxiv_download_paper',
    args: { arxiv_id: '2301.12345' },
  },
  {
    id: 'file-slack-scan-call',
    name: 'slack_download_file',
    args: { channel: 'C0STREAM', file_id: 'F999' },
  },
];

function artifactIdForOriginCall(callId: string): string {
  return callId.replace(/-call$/, '');
}

function createOriginToolResults(artifacts: Record<string, Artifact>): Map<
  string,
  { envelope?: ToolResultEnvelope; artifact?: Artifact }
> {
  const results = new Map<string, { envelope?: ToolResultEnvelope; artifact?: Artifact }>();
  for (const call of ORIGIN_TOOL_CALLS) {
    const artifactId = artifactIdForOriginCall(call.id);
    const artifact = artifacts[artifactId];
    const metadata = artifact?.metadata || {};
    const envelope: ToolResultEnvelope = {
      artifact_id: artifactId,
      tool_name: call.name,
      tool_call_id: call.id,
      ok: true,
      result_format: 'json',
      result: { status: artifactId === 'file-chat-upload' ? 'attached' : 'downloaded', title: artifact?.title },
      tool_args: call.args,
      result_metadata: metadata.result_metadata,
      tool_presentation: metadata.tool_presentation,
    };
    results.set(call.id, { envelope, artifact });
  }
  return results;
}

interface FixtureState {
  artifacts: Record<string, Artifact>;
  records: FixtureRecord[];
  deletes: string[];
  rawReads: string[];
  originOpens: Array<{ threadId: string; toolCallId?: string }>;
  remoteDownloadRequests: Array<{ artifactId: string; title: string; prompt: string }>;
}

function createFixtureState(): FixtureState {
  const artifacts = createArtifacts();
  return {
    artifacts,
    records: createRecords(artifacts),
    deletes: [],
    rawReads: [],
    originOpens: [],
    remoteDownloadRequests: [],
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function requestLimit(url: URL, fallback: number): number {
  const parsed = Number(url.searchParams.get('limit') || '');
  if (!Number.isFinite(parsed)) return fallback;
  return Math.min(Math.max(Math.trunc(parsed), 1), 500);
}

function requestOffset(url: URL): number {
  const parsed = Number(url.searchParams.get('offset') || '');
  if (!Number.isFinite(parsed)) return 0;
  return Math.max(Math.trunc(parsed), 0);
}

// Mirrors the backend's file_audit search text: top-level provenance fields
// plus string values from the metadata / result_metadata / tool_args maps.
function fixtureRecordSearchText(record: FixtureRecord): string {
  const parts: string[] = [];
  const push = (value: unknown) => {
    if (typeof value === 'string' && value.trim()) parts.push(value);
  };
  for (const key of [
    'title', 'source_label', 'source_tool_name', 'tool_name', 'thread_title',
    'file_kind', 'mime_type', 'content_path', 'managed_local_path',
    'remote_url', 'source_url', 'remote_path', 'remote_ref', 'remote_id',
    'preview_text', 'content_note', 'request_tool_name',
  ]) push(record[key]);
  const metadata = isPlainRecord(record.metadata) ? record.metadata : {};
  for (const nested of [metadata, metadata.result_metadata, metadata.tool_args]) {
    if (!isPlainRecord(nested)) continue;
    for (const value of Object.values(nested)) push(value);
  }
  return parts.join('\n').toLowerCase();
}

// Mirrors the backend: match the whole query, else require every term.
function filterFixtureRecords(records: FixtureRecord[], query: string): FixtureRecord[] {
  const q = query.trim().toLowerCase();
  if (!q) return records;
  const terms = q.split(/\s+/);
  return records.filter((record) => {
    const text = fixtureRecordSearchText(record);
    if (text.includes(q)) return true;
    return terms.length > 1 && terms.every((term) => text.includes(term));
  });
}

// Mirrors fileSourceKey on the frontend: source_label falling back to source.
function fixtureSourceKey(record: FixtureRecord): string {
  return firstString(record.source_label, record.source) || 'File artifact';
}

function fixtureIsRemoteOnly(record: FixtureRecord): boolean {
  return record.cached === false;
}

interface FixtureFilterParams {
  query: string;
  source: string;
  remoteOnly: boolean;
}

function pagedFileAuditResponse(records: FixtureRecord[], threadId: string, limit: number, offset: number, params: FixtureFilterParams) {
  // Search and filters happen before pagination, per the backend contract.
  const { query, source, remoteOnly } = params;
  const searched = filterFixtureRecords(records, query);
  const sourceMatch = (record: FixtureRecord) =>
    !source || fixtureSourceKey(record).toLowerCase() === source.toLowerCase();
  const statusMatch = (record: FixtureRecord) => !remoteOnly || fixtureIsRemoteOnly(record);
  const matches = searched.filter((record) => sourceMatch(record) && statusMatch(record));
  // Facets exclude their own dimension: source buckets over query+status,
  // status buckets over query+source — each answers "what would I get if I
  // clicked this chip". Shapes mirror the backend: source_counts is a list
  // of {key, label, count}; status_counts is {total, cached, remote_only}.
  const sourceBuckets = new Map<string, number>();
  for (const record of searched.filter(statusMatch)) {
    const key = fixtureSourceKey(record);
    sourceBuckets.set(key, (sourceBuckets.get(key) || 0) + 1);
  }
  const sourceCounts = [...sourceBuckets.entries()]
    .sort(([a], [b]) => a.toLowerCase().localeCompare(b.toLowerCase()))
    .map(([label, count]) => ({ key: label, label, count }));
  const statusRecords = searched.filter(sourceMatch);
  const statusRemoteOnly = statusRecords.filter(fixtureIsRemoteOnly).length;
  const statusCounts = {
    total: statusRecords.length,
    cached: statusRecords.length - statusRemoteOnly,
    remote_only: statusRemoteOnly,
  };
  const files = matches.slice(offset, offset + limit);
  const nextOffset = offset + files.length < matches.length ? offset + files.length : 0;
  return {
    files,
    scope: {
      thread_id: threadId,
      limit,
      offset,
      next_offset: nextOffset,
      returned: files.length,
      has_more: nextOffset > 0,
      truncated: nextOffset > 0,
      // The fixture enumerates its whole dataset, so candidate_count is exact
      // and no scan limit is hit — the backend's "response truncated by
      // pagination" state. The UI must render "N of M files" without the
      // lower-bound "+" marker.
      cached_file_limit_hit: false,
      remote_scan_limit: limit,
      remote_scan_limit_hit: false,
      candidate_count: matches.length,
      query: query.trim(),
      source: source.trim(),
      source_label: source.trim(),
      remote_only: remoteOnly,
      source_counts: sourceCounts,
      status_counts: statusCounts,
    },
  };
}

// Simulates the agent fulfilling a download request: the remote reference
// becomes a cached record (same fixture id — the frontend must not depend on
// that; it also matches by remote locators) and gains openable bytes.
function fulfillRemoteDownloadRequest(state: FixtureState, artifactId: string): void {
  const record = state.records.find((entry) => entry.artifact_id === artifactId);
  if (!record || record.cached !== false) return;
  const body = `# ${record.title}\n\nCached by the agent after a download request.`;
  record.record_kind = 'cached_file';
  record.availability = 'cached';
  record.cached = true;
  record.openable = true;
  record.downloadable = true;
  record.preview_available = true;
  record.preview_text = body.slice(0, 180);
  record.content_storage = 'file';
  record.content_path = `sharepoint/downloads/${artifactId}.docx`;
  record.managed_local_path = record.content_path;
  delete record.request_download_prompt;
  delete record.request_tool_name;
  delete record.request_tool_args;
  state.artifacts[artifactId] = fileArtifact(
    artifactId,
    String(record.title || 'Downloaded file'),
    'sharepoint_download_file',
    'SharePoint',
    'sharepoint',
    String(record.mime_type || ''),
    body,
  );
  fixtureRawBodies[artifactId] = body;
}

function exposeState(state: FixtureState): void {
  (window as any).__filesAuditFixture = state;
  (window as any).__filesAuditFixtureDeletes = state.deletes;
  (window as any).__filesAuditFixtureRawReads = state.rawReads;
  (window as any).__filesAuditFixtureOriginOpens = state.originOpens;
  (window as any).__filesAuditFixtureRemoteRequests = state.remoteDownloadRequests;
}

const FilesAuditFixture: React.FC = () => {
  const [ready, setReady] = React.useState(false);
  const [activeArtifact, setActiveArtifact] = React.useState<Artifact | null>(null);
  const [activeProvenance, setActiveProvenance] = React.useState<FileArtifactProvenance | null>(null);
  const [originArtifacts, setOriginArtifacts] = React.useState<Record<string, Artifact> | null>(null);
  const stateRef = React.useRef<FixtureState | null>(null);
  const fixturePageSize = React.useMemo(() => {
    const parsed = Number(new URLSearchParams(window.location.search).get('pageSize') || '');
    if (!Number.isFinite(parsed) || parsed <= 0) return undefined;
    return Math.min(Math.max(Math.trunc(parsed), 1), 500);
  }, []);
  // Artificial latency for /api/files so tests can observe the loading state
  // (focus retention on refresh, aria-busy) — instant responses hide it.
  const filesDelayMs = React.useMemo(() => {
    const parsed = Number(new URLSearchParams(window.location.search).get('filesDelay') || '');
    if (!Number.isFinite(parsed) || parsed <= 0) return 0;
    return Math.min(Math.trunc(parsed), 5000);
  }, []);
  const showOriginTranscript = React.useMemo(
    () => new URLSearchParams(window.location.search).get('originTranscript') === '1',
    [],
  );
  // Simulates a backend that predates server-side source/status filtering:
  // the params are ignored and never echoed, and no facet counts are sent,
  // so the UI must fall back to client-side filtering over loaded records.
  const legacyFilters = React.useMemo(
    () => new URLSearchParams(window.location.search).get('legacyFilters') === '1',
    [],
  );
  // When set, asking for a remote download immediately converts the record to
  // cached in the fixture backend — as if the user sent the drafted prompt
  // and the agent's run downloaded the file. The LIST does not refetch by
  // itself; tests drive the run-completed hint + refresh to observe the flip.
  const downloadRequestFulfills = React.useMemo(
    () => new URLSearchParams(window.location.search).get('downloadRequestFulfills') === '1',
    [],
  );
  const originToolResults = React.useMemo(
    () => (originArtifacts
      ? createOriginToolResults(originArtifacts)
      : new Map<string, { envelope?: ToolResultEnvelope; artifact?: Artifact }>()),
    [originArtifacts],
  );

  const handleOpenArtifact = React.useCallback((artifact: Artifact, provenance?: FileArtifactProvenance) => {
    setActiveArtifact(artifact);
    setActiveProvenance(provenance || null);
  }, []);

  const handleCloseArtifact = React.useCallback(() => {
    setActiveArtifact(null);
    setActiveProvenance(null);
  }, []);

  const handleOpenOriginThread = React.useCallback((threadId: string, toolCallId?: string) => {
    const state = stateRef.current;
    if (!state) return;
    state.originOpens.push({ threadId, toolCallId });
    exposeState(state);
    // Mirrors App.handleOpenOriginThread: the origin transcript group below
    // claims the request, expands, and flashes the matching tool card.
    const callId = String(toolCallId || '').trim();
    if (callId) requestToolCallReveal(callId);
  }, []);

  // Mirrors App.handleRequestRemoteFileDownload: build the same composer
  // prompt the app would insert and record it, so tests can prove the
  // backend's connector-specific prompt survives normalization verbatim.
  const handleRequestRemoteDownload = React.useCallback((record: FileAuditRecord) => {
    const state = stateRef.current;
    if (!state) return;
    state.remoteDownloadRequests.push({
      artifactId: record.artifactId,
      title: record.title,
      prompt: buildRemoteFileDownloadPrompt(record),
    });
    // Mirrors App.handleRequestRemoteFileDownload: the session ledger is what
    // renders the row's "you drafted a download request" note.
    markRemoteFileDownloadRequested(record, { threadId: THREAD_ID, threadTitle: THREAD_TITLE });
    if (downloadRequestFulfills) fulfillRemoteDownloadRequest(state, record.artifactId);
    exposeState(state);
  }, [downloadRequestFulfills]);

  React.useEffect(() => {
    const state = createFixtureState();
    stateRef.current = state;
    exposeState(state);
    setOriginArtifacts(state.artifacts);
    // Mirrors App's runningThreadIds diff effect firing when a run ends in a
    // thread with pending download requests (a no-op when none are pending).
    (window as any).__filesAuditFixtureCompleteRun = () => {
      notifyRemoteFileDownloadRunCompleted(THREAD_ID);
    };
    const originalFetch = window.fetch.bind(window);
    window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        typeof input === 'string'
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url,
        window.location.origin,
      );
      const method = String(init?.method || request?.method || 'GET').toUpperCase();

      const fileAuditResponse = async (threadId: string, defaultLimit: number) => {
        const limit = requestLimit(url, defaultLimit);
        const offset = requestOffset(url);
        const query = url.searchParams.get('q') || '';
        const source = legacyFilters ? '' : (url.searchParams.get('source') || '');
        const remoteOnlyParam = url.searchParams.get('remote_only') || '';
        const remoteOnly = !legacyFilters && (remoteOnlyParam === '1' || remoteOnlyParam === 'true');
        if (filesDelayMs > 0) await new Promise((resolve) => setTimeout(resolve, filesDelayMs));
        const response = pagedFileAuditResponse(state.records, threadId, limit, offset, { query, source, remoteOnly });
        if (legacyFilters) {
          // A legacy backend has no idea these fields exist.
          const scope = response.scope as Record<string, unknown>;
          delete scope.source;
          delete scope.source_label;
          delete scope.remote_only;
          delete scope.source_counts;
          delete scope.status_counts;
        }
        return jsonResponse(response);
      };
      if (url.pathname === '/api/files' && method === 'GET') {
        return fileAuditResponse('', 500);
      }
      if (url.pathname === `/api/threads/${THREAD_ID}/files` && method === 'GET') {
        return fileAuditResponse(THREAD_ID, 150);
      }
      const artifactMatch = url.pathname.match(/^\/api\/artifacts\/([^/]+)$/);
      if (artifactMatch && method === 'GET') {
        const artifact = state.artifacts[decodeURIComponent(artifactMatch[1])];
        return artifact
          ? jsonResponse({ artifact })
          : jsonResponse({ error: 'not found' }, 404);
      }
      if (artifactMatch && method === 'DELETE') {
        const artifactId = decodeURIComponent(artifactMatch[1]);
        state.deletes.push(artifactId);
        delete state.artifacts[artifactId];
        state.records = state.records.filter((record) => record.artifact_id !== artifactId);
        exposeState(state);
        return jsonResponse({ ok: true, artifact_id: artifactId });
      }
      const rawMatch = url.pathname.match(/^\/api\/artifacts\/([^/]+)\/raw$/);
      if (rawMatch && method === 'GET') {
        const artifactId = decodeURIComponent(rawMatch[1]);
        const artifact = state.artifacts[artifactId];
        const body = fileBodies[artifactId] || '';
        if (!artifact) return jsonResponse({ error: 'not found' }, 404);
        state.rawReads.push(artifactId);
        exposeState(state);
        return new Response(fixtureRawBodies[artifactId] || body, {
          status: 200,
          headers: {
            'Content-Type': fileArtifactMimeType(artifact) || 'text/plain',
            'Content-Disposition': `inline; filename="${artifact.title}"`,
            'X-Content-Type-Options': 'nosniff',
          },
        });
      }
      return originalFetch(input, init);
    };
    setReady(true);
    return () => {
      window.fetch = originalFetch;
      delete (window as any).__filesAuditFixtureCompleteRun;
    };
  }, [filesDelayMs]);

  return (
    <TooltipProvider delayDuration={300}>
      <div className="flex h-screen min-h-0 flex-col bg-background text-foreground" data-testid="files-audit-fixture">
        <header className="shrink-0 border-b border-border bg-card/40 px-5 py-4">
          <div className="mx-auto max-w-5xl">
            <h1 className="text-lg font-semibold">Files Audit Fixture</h1>
            <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
              Synthetic chat upload, Jira, SharePoint, Slack, and arXiv file records — plus remote-only SharePoint references — rendered through the production files surface.
            </p>
          </div>
        </header>
        <main className="min-h-0 flex-1 overflow-auto p-4">
          <div className="mx-auto h-full min-h-0 max-w-5xl">
            {ready ? (
              <FilesAuditView
                currentThreadId={THREAD_ID}
                currentThreadTitle={THREAD_TITLE}
                onOpenArtifact={handleOpenArtifact}
                onOpenOriginThread={handleOpenOriginThread}
                onRequestRemoteDownload={handleRequestRemoteDownload}
                initialScope="all"
                layout="page"
                title="Fixture files"
                description="QA source labels, filtering, native file viewing, downloading, and removal across remote connector file records."
                pageSize={fixturePageSize}
              />
            ) : (
              <div className="text-sm text-muted-foreground">Loading fixture...</div>
            )}
          </div>
        </main>
        {showOriginTranscript && (
          <section
            className="shrink-0 border-t border-border bg-card/20 px-5 py-4"
            data-testid="files-audit-origin-transcript"
          >
            <div className="mx-auto max-w-5xl">
              <h2 className="text-sm font-semibold">Origin transcript</h2>
              <p className="mt-1 text-xs text-muted-foreground">
                The chat turn that attached or downloaded the fixture files, rendered through
                the production tool-activity group so origin deep links and direct-open file
                provenance can be exercised end to end.
              </p>
              <div className="mt-2">
                {ready && originArtifacts && (
                  <CollapsedToolsSummary
                    toolCalls={ORIGIN_TOOL_CALLS}
                    isLastMessage={false}
                    isThreadRunActive={false}
                    messageIdentityKey="files-audit-fixture-origin-turn"
                    artifactsById={originArtifacts}
                    toolResultsByCallId={originToolResults}
                    onOpenArtifact={handleOpenArtifact}
                  />
                )}
              </div>
            </div>
          </section>
        )}
        {activeArtifact && (
          <InspectorOverlay onClose={handleCloseArtifact}>
            <ArtifactPanel
              artifact={activeArtifact}
              settings={DEFAULT_SETTINGS}
              onClose={handleCloseArtifact}
              fileProvenance={activeProvenance}
              onOpenOriginThread={handleOpenOriginThread}
            />
          </InspectorOverlay>
        )}
      </div>
    </TooltipProvider>
  );
};

export default FilesAuditFixture;
