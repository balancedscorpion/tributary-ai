import type { Artifact, ChartBudgetReport, ToolResultEnvelope } from '@/types';
import {
  SANDBOXED_HTML_NETWORK_POLICY,
  SANDBOXED_HTML_RENDER_MODE,
  SANDBOXED_HTML_SANDBOX_POLICY,
} from '@/types';
import {
  getQueryProviderForToolName,
  isQueryToolName,
  normalizeQueryProvider,
} from '@/lib/queryProviderRegistry';
import type { ToolPresentationSpec } from '@/lib/toolInventory';
import { isActiveQueryStatus } from '@/lib/queryProgress';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';

export interface ToolTestResultLike {
  tool_name: string;
  presentation?: ToolPresentationSpec;
  args?: Record<string, unknown>;
  output?: {
    ok: boolean;
    result_format?: string;
    result?: unknown;
    query_metadata?: Record<string, unknown>;
    result_metadata?: Record<string, unknown>;
    artifact?: unknown;
  };
}

export function buildToolTestArtifact(result: ToolTestResultLike | null | undefined): Artifact | null {
  if (!result?.output) return null;
  const toolName = String(result.tool_name || 'tool_test').trim() || 'tool_test';
  const output = result.output;
  const resultFormat = output.result_format === 'text' ? 'text' : 'json';
  const resultValue = output.result;
  const nestedMetadata = isPlainObject(resultValue) && isPlainObject((resultValue as any).result_metadata)
    ? ((resultValue as any).result_metadata as Record<string, any>)
    : undefined;
  const outputArtifact = buildToolTestOutputArtifact(result);
  if (outputArtifact) return outputArtifact;
  const envelope: ToolResultEnvelope = {
    artifact_id: `tool-test-${toolName}`,
    tool_name: toolName,
    ok: output.ok !== false,
    result_format: resultFormat,
    result: resultValue,
    query_metadata: output.query_metadata as any,
    result_metadata: output.result_metadata || nestedMetadata,
    tool_presentation: result.presentation as any,
  };
  const artifact = buildArtifactFromEnvelope(
    envelope,
    toolName,
    resultFormat === 'text' ? String(resultValue ?? '') : null,
  );
  return {
    ...artifact,
    metadata: {
      ...(artifact.metadata || {}),
      tool_test: true,
      tool_args: result.args || {},
      tool_presentation: result.presentation,
    },
  };
}

function buildToolTestOutputArtifact(result: ToolTestResultLike): Artifact | null {
  const output = result.output;
  if (!output || !isPlainObject(output.artifact)) return null;
  const rawArtifact = output.artifact as Record<string, any>;
  const toolName = String(result.tool_name || 'tool_test').trim() || 'tool_test';
  const id =
    String(
      rawArtifact.id ||
        rawArtifact.artifact_id ||
        rawArtifact.artifactId ||
        `tool-test-${toolName}-artifact`,
    ).trim() || `tool-test-${toolName}-artifact`;
  const title =
    String(rawArtifact.title || rawArtifact.name || '').trim() ||
    titleFromToolName(toolName);
  const metadata = isPlainObject(rawArtifact.metadata)
    ? { ...(rawArtifact.metadata as Record<string, any>) }
    : {};
  const nestedMetadata =
    isPlainObject(output.result) && isPlainObject((output.result as any).result_metadata)
      ? ((output.result as any).result_metadata as Record<string, any>)
      : undefined;
  const resultMetadata =
    (isPlainObject(output.result_metadata)
      ? (output.result_metadata as Record<string, any>)
      : undefined) ||
    (isPlainObject(metadata.result_metadata)
      ? (metadata.result_metadata as Record<string, any>)
      : undefined) ||
    nestedMetadata;
  const toolPresentation = normalizeToolPresentation(
    result.presentation,
    isPlainObject(metadata.tool_presentation) ? metadata.tool_presentation : undefined,
    isPlainObject(resultMetadata) ? resultMetadata.tool_presentation : undefined,
  );
  return {
    id,
    type: normalizeArtifactType(rawArtifact.type),
    title,
    content: String(rawArtifact.content ?? ''),
    language: stringOrUndefined(rawArtifact.language),
    metadata: {
      ...metadata,
      tool_test: true,
      tool_name: toolName,
      ok: output.ok !== false,
      placeholder: false,
      result_format: output.result_format,
      raw_result: output.result,
      tool_args: result.args || {},
      ...(isPlainObject(output.query_metadata)
        ? { query_metadata: output.query_metadata }
        : {}),
      ...(resultMetadata ? { result_metadata: resultMetadata } : {}),
      ...(toolPresentation ? { tool_presentation: toolPresentation } : {}),
    },
  };
}

export function buildArtifactFromEnvelope(
  envelope: ToolResultEnvelope,
  fallbackToolName: string,
  fallbackRawContent: string | null,
): Artifact {
  const toolName =
    (envelope.tool_name || fallbackToolName || 'tool_result').trim() ||
    'tool_result';
  const ok = envelope.ok !== false;
  const resultFormat = envelope.result_format;
  const result = envelope.result;
  const isQueryTool = isQueryToolName(toolName);
  const envelopePlotSpec = isPlainObject((envelope as any).plot_spec)
    ? ((envelope as any).plot_spec as Record<string, any>)
    : isPlainObject(result) && isPlainObject((result as any).plot_spec)
      ? ((result as any).plot_spec as Record<string, any>)
      : undefined;
  const chartEngine = String(
    (envelope as any).chart_engine ||
      (isPlainObject(result) ? (result as any).chart_engine : '') ||
      '',
  ).trim();
  const chartSpecVersion = String(
    (envelope as any).chart_spec_version ||
      (isPlainObject(result) ? (result as any).chart_spec_version : '') ||
      '',
  ).trim();
  const chartModule = String(
    (envelope as any).chart_module ||
      (isPlainObject(result) ? (result as any).chart_module : '') ||
      '',
  ).trim();
  const chartModules = normalizeStringArray(
    (envelope as any).chart_modules,
    isPlainObject(result) ? (result as any).chart_modules : undefined,
  );
  const previewOnly = Boolean(
    (envelope as any).preview_only ||
      (isPlainObject(result) && (result as any).preview_only === true) ||
      (isPlainObject(envelope.result_metadata) &&
        (envelope.result_metadata as any).preview_only === true),
  );
  const fullArtifactId = String(
    (envelope as any).full_artifact_id ||
      (isPlainObject(result) ? (result as any).full_artifact_id : '') ||
      (isPlainObject(envelope.result_metadata)
        ? (envelope.result_metadata as any).full_artifact_id
        : '') ||
      '',
  ).trim();
  const rowCountTotalRaw =
    (envelope as any).row_count_total ??
    (isPlainObject(result) ? (result as any).row_count_total : undefined) ??
    (isPlainObject(envelope.result_metadata)
      ? (envelope.result_metadata as any).row_count_total
      : undefined);
  const rowCountTotal = Number(rowCountTotalRaw);
  const downsampleRatioRaw =
    (envelope as any).downsample_ratio ??
    (isPlainObject(result) ? (result as any).downsample_ratio : undefined) ??
    (isPlainObject(envelope.result_metadata)
      ? (envelope.result_metadata as any).downsample_ratio
      : undefined);
  const downsampleRatio = Number(downsampleRatioRaw);
  const chartBudget = normalizeChartBudget(
    (envelope as any).chart_budget,
    isPlainObject(result) ? (result as any).chart_budget : undefined,
    isPlainObject(envelope.result_metadata)
      ? (envelope.result_metadata as any).chart_budget
      : undefined,
  );
  const toolPresentation = normalizeToolPresentation(
    (envelope as any).tool_presentation,
    isPlainObject(envelope.result_metadata)
      ? (envelope.result_metadata as any).tool_presentation
      : undefined,
  );
  const presentationNativeViewer = String(
    toolPresentation?.native_viewer ||
      (isPlainObject(envelope.result_metadata)
        ? (envelope.result_metadata as any).native_viewer
        : '') ||
      '',
  ).trim();
  const presentationResultKind = String(
    toolPresentation?.result_kind ||
      (isPlainObject(envelope.result_metadata)
        ? (envelope.result_metadata as any).kind
        : '') ||
      '',
  ).trim();
  const toolArgs = isPlainObject((envelope as any).tool_args)
    ? ((envelope as any).tool_args as Record<string, any>)
    : undefined;

  const managedQueryJobId = isPlainObject(result)
    ? normalizeManagedQueryJobId((result as any).job_id)
    : undefined;
  if (
    isQueryTool &&
    isPlainObject(result) &&
    managedQueryJobId &&
    isActiveQueryStatus((result as any).status)
  ) {
    const queryStatus = String((result as any).status || '').trim().toLowerCase();
    const ownerToken =
      String(
        (result as any).owner_token ??
          envelope.query_metadata?.owner_token ??
          '',
      ).trim() || undefined;
    const threadId =
      String(
        (result as any).thread_id ?? envelope.query_metadata?.thread_id ?? '',
      ).trim() || undefined;
    const sql =
      String(
        (result as any).sql ?? envelope.query_metadata?.sql ?? '',
      ).trim() || undefined;
    const sqlPreview =
      String(
        (result as any).sql_preview ??
          envelope.query_metadata?.sql_preview ??
          '',
      ).trim() || undefined;
    const description =
      String(
        (result as any).description ??
          envelope.query_metadata?.description ??
          '',
      ).trim() || undefined;
    const rawStartedAt = (result as any)._uiStartedAt;
    const uiStartedAt =
      typeof rawStartedAt === 'number' &&
      Number.isFinite(rawStartedAt) &&
      rawStartedAt > 0
        ? rawStartedAt
        : undefined;
    const provider = normalizeQueryProvider(
      (result as any).provider ??
        envelope.query_metadata?.provider ??
        getQueryProviderForToolName(toolName) ??
        undefined,
      'bigquery',
    );
    const mergedQueryMetadata = {
      ...(envelope.query_metadata || {}),
      ...(provider ? { provider } : {}),
      job_id: managedQueryJobId,
      status: queryStatus || 'running',
      ...(ownerToken ? { owner_token: ownerToken } : {}),
      ...(threadId ? { thread_id: threadId } : {}),
      ...(sql ? { sql } : {}),
      ...(sqlPreview ? { sql_preview: sqlPreview } : {}),
      ...(description ? { description } : {}),
    };

    return {
      id: envelope.artifact_id,
      type: 'data',
      title: queryStatus === 'profiling' ? 'Query Profiling' : 'Query Running',
      content: '',
      metadata: {
        tool_name: toolName,
        ...(envelope.tool_call_id ? { tool_call_id: envelope.tool_call_id } : {}),
        ...(toolArgs ? { tool_args: toolArgs } : {}),
        ok: true,
        placeholder: true,
        async_query: true,
        status: queryStatus || 'running',
        job_id: managedQueryJobId,
        provider,
        ...(sql ? { sql } : {}),
        ...(sqlPreview ? { sql_preview: sqlPreview } : {}),
        ...(description ? { description } : {}),
        ...(ownerToken ? { owner_token: ownerToken } : {}),
        ...(threadId ? { thread_id: threadId } : {}),
        ...(uiStartedAt ? { _uiStartedAt: uiStartedAt } : {}),
        query_metadata: mergedQueryMetadata,
      },
    };
  }

  const meta = {
    tool_name: toolName,
    ...(envelope.tool_call_id ? { tool_call_id: envelope.tool_call_id } : {}),
    ...(toolArgs ? { tool_args: toolArgs } : {}),
    ok,
    placeholder: false,
    query_metadata: envelope.query_metadata,
    result_format: resultFormat,
    raw_result: result,
    ...(toolPresentation ? { tool_presentation: toolPresentation } : {}),
    ...(envelope.result_metadata
      ? { result_metadata: envelope.result_metadata }
      : {}),
    ...(envelopePlotSpec ? { plot_spec: envelopePlotSpec } : {}),
    ...(chartEngine ? { chart_engine: chartEngine } : {}),
    ...(chartSpecVersion ? { chart_spec_version: chartSpecVersion } : {}),
    ...(chartModule ? { chart_module: chartModule } : {}),
    ...(chartModules ? { chart_modules: chartModules } : {}),
    ...(previewOnly ? { preview_only: true, partial: true } : {}),
    ...(fullArtifactId ? { full_artifact_id: fullArtifactId } : {}),
    ...(Number.isFinite(rowCountTotal) && rowCountTotal >= 0
      ? { row_count_total: rowCountTotal }
      : {}),
    ...(Number.isFinite(downsampleRatio) && downsampleRatio > 0
      ? { downsample_ratio: downsampleRatio }
      : {}),
    ...(chartBudget ? { chart_budget: chartBudget } : {}),
  };

  const promotedNativeFile = nativeFileArtifactFromPromotedResult(
    envelope.artifact_id,
    toolName,
    ok,
    result,
    meta,
  );
  if (promotedNativeFile) return promotedNativeFile;

  if (
    ok &&
    resultFormat === 'json' &&
    isPlainObject(result) &&
    isPlainObject(envelope.result_metadata) &&
    (envelope.result_metadata as any).integration === 'arxiv'
  ) {
    const kind = String((envelope.result_metadata as any).kind || '');
    const arxivTitle =
      kind === 'search_results'
        ? 'arXiv Search Results'
        : kind === 'paper_list'
          ? 'arXiv Paper Library'
          : kind === 'paper_text'
            ? 'arXiv Paper Text'
            : kind === 'downloaded_paper' || kind === 'paper'
              ? 'arXiv Paper'
              : 'arXiv Result';
    return {
      id: envelope.artifact_id,
      type: 'code',
      title: arxivTitle,
      content: safeJsonStringify(result),
      language: 'json',
      metadata: { ...meta },
    };
  }

  if (
    (toolName === 'create_interactive_artifact' ||
      toolName === 'create_interactive_data_artifact' ||
      presentationNativeViewer === 'interactive_html') &&
    resultFormat === 'json' &&
    isPlainObject(result) &&
    String((result as any).render_mode || '').trim() ===
      SANDBOXED_HTML_RENDER_MODE
  ) {
    const title =
      String((result as any).title || '').trim() || 'Interactive HTML';
    const bytes = Number((result as any).byte_size);
    const dataSourceArtifactId = String(
      (result as any).data_source_artifact_id || '',
    ).trim();
    const dataSourceColumns = normalizeStringArray(
      (result as any).data_source_columns,
    );
    const dataSourceRowCount = Number((result as any).data_source_row_count);
    const dataAccess = isPlainObject((result as any).data_access)
      ? ((result as any).data_access as Record<string, any>)
      : undefined;
    return {
      id: envelope.artifact_id,
      type: 'code',
      title,
      content: '',
      language: 'html',
      metadata: {
        ...meta,
        render_mode: SANDBOXED_HTML_RENDER_MODE,
        artifact_kind:
          String((result as any).artifact_type || '').trim() ||
          'interactive_html',
        sandbox: SANDBOXED_HTML_SANDBOX_POLICY,
        network: SANDBOXED_HTML_NETWORK_POLICY,
        inline_preview: (result as any).inline_preview !== false,
        placeholder: true,
        ...(Number.isFinite(Number((result as any).preview_height))
          ? { preview_height: Number((result as any).preview_height) }
          : {}),
        ...(Number.isFinite(bytes) && bytes >= 0 ? { byte_size: bytes } : {}),
        ...(dataSourceArtifactId
          ? { data_source_artifact_id: dataSourceArtifactId }
          : {}),
        ...(dataSourceColumns
          ? { data_source_columns: dataSourceColumns }
          : {}),
        ...(Number.isFinite(dataSourceRowCount) && dataSourceRowCount >= 0
          ? { data_source_row_count: dataSourceRowCount }
          : {}),
        ...(typeof (result as any).data_source_title === 'string'
          ? { data_source_title: (result as any).data_source_title }
          : {}),
        ...(dataAccess ? { data_access: dataAccess } : {}),
      },
    };
  }

  if (ok && presentationNativeViewer === 'code') {
    const shaped = codeArtifactFromNativeViewerResult(
      envelope.artifact_id,
      toolName,
      resultFormat,
      result,
      fallbackRawContent,
      meta,
    );
    if (shaped) return shaped;
  }

  if (toolName === 'read_file') {
    if (
      resultFormat === 'json' &&
      isPlainObject(result) &&
      typeof result.content === 'string'
    ) {
      const path = String(result.path || result.file_path || 'file');
      const ext = path.match(/\.([a-zA-Z0-9]+)$/)?.[1] || 'text';
      return {
        id: envelope.artifact_id,
        type: 'code',
        title: path,
        content: result.content,
        language: ext,
        metadata: { ...meta },
      };
    }
    const raw =
      typeof result === 'string'
        ? result
        : typeof fallbackRawContent === 'string'
          ? fallbackRawContent
          : '';
    const parsed = raw ? parseReadFileResult(raw) : null;
    if (parsed) {
      return {
        id: envelope.artifact_id,
        type: 'code',
        title: parsed.path,
        content: parsed.content,
        language: parsed.language,
        metadata: { ...meta },
      };
    }
  }

  // File-backed image tools intentionally keep the multi-megabyte raster out
  // of the SSE envelope. Shape a real image placeholder from the compact
  // result so image-specific cards can render immediately and then hydrate
  // the title/prompt/metadata from /api/artifacts/:id. Without this branch the
  // generic JSON fallback is marked complete, so ensureArtifactLoaded short-
  // circuits and the card is left with a "generate_image result" title.
  if (
    ok &&
    (presentationNativeViewer === 'image' || presentationResultKind === 'image')
  ) {
    const imageResult = isPlainObject(result) ? result : {};
    const title =
      String((imageResult as any).title || '').trim() ||
      titleFromToolName(toolName);
    const mimeType = String(
      (imageResult as any).mime_type || (imageResult as any).mimeType || '',
    ).trim();
    const width = Number((imageResult as any).width);
    const height = Number((imageResult as any).height);
    const sizeBytes = Number(
      (imageResult as any).size_bytes ?? (imageResult as any).sizeBytes,
    );
    return {
      id: envelope.artifact_id,
      type: 'image',
      title,
      content: '',
      metadata: {
        ...meta,
        placeholder: true,
        source: 'generated_image',
        ...(mimeType ? { mimeType, mime_type: mimeType } : {}),
        ...(Number.isFinite(width) && width > 0 ? { width } : {}),
        ...(Number.isFinite(height) && height > 0 ? { height } : {}),
        ...(Number.isFinite(sizeBytes) && sizeBytes > 0
          ? { sizeBytes, size_bytes: sizeBytes }
          : {}),
      },
    };
  }

  const imageDataUri = extractImageDataUriFromToolResult(
    resultFormat,
    result,
    fallbackRawContent,
  );
  if (imageDataUri) {
    const mimeType = inferImageMimeTypeFromDataUri(imageDataUri);
    return {
      id: envelope.artifact_id,
      type: 'image',
      title: `${toolName} image`,
      content: imageDataUri,
      metadata: {
        ...meta,
        ...(mimeType ? { mimeType } : {}),
      },
    };
  }

  if (!ok) {
    return {
      id: envelope.artifact_id,
      type: 'code',
      title: `${toolName} error`,
      content:
        resultFormat === 'json'
          ? safeJsonStringify(result)
          : String(result ?? fallbackRawContent ?? ''),
      language: resultFormat === 'json' ? 'json' : 'text',
      metadata: { ...meta },
    };
  }

  const maybeDataset =
    resultFormat === 'json' ? unwrapSingleArrayField(result) : null;
  const isDeclaredData =
    presentationNativeViewer === 'data' || presentationResultKind === 'data';
  const isDataset = isQueryTool || isDeclaredData
    ? isArrayOfObjectsOrEmpty(maybeDataset)
    : isArrayOfObjects(maybeDataset);
  if (isDataset) {
    const rows = Array.isArray(maybeDataset) ? maybeDataset : [];
    const title = isQueryTool ? 'Query Results' : `${toolName} results`;
    return {
      id: envelope.artifact_id,
      type: 'data',
      title,
      content: safeJsonStringify(maybeDataset),
      metadata: {
        ...meta,
        row_count: rows.length,
        ...(Number.isFinite(rowCountTotal) && rowCountTotal >= 0
          ? { row_count_total: rowCountTotal }
          : {}),
      },
    };
  }

  return {
    id: envelope.artifact_id,
    type: 'code',
    title: `${toolName} result`,
    content:
      resultFormat === 'json'
        ? safeJsonStringify(result)
        : String(result ?? fallbackRawContent ?? ''),
    language: resultFormat === 'json' ? 'json' : 'text',
    metadata: { ...meta },
  };
}

function codeArtifactFromNativeViewerResult(
  artifactId: string,
  toolName: string,
  resultFormat: ToolResultEnvelope['result_format'],
  result: any,
  fallbackRawContent: string | null,
  meta: Record<string, any>,
): Artifact | null {
  if (resultFormat === 'text') {
    const raw = String(result ?? fallbackRawContent ?? '');
    if (!raw) return null;
    return {
      id: artifactId,
      type: 'code',
      title: `${toolName} output`,
      content: raw,
      language: 'text',
      metadata: { ...meta },
    };
  }
  if (!isPlainObject(result)) return null;

  const directContent = firstStringField(result, [
    'content',
    'text',
    'stdout',
    'output',
    'markdown',
  ]);
  if (directContent !== null) {
    const title = firstStringField(result, [
      'path',
      'file_path',
      'name',
      'title',
      'command',
    ]) || `${toolName} output`;
    return {
      id: artifactId,
      type: 'code',
      title,
      content: directContent,
      language: languageFromTitleOrMeta(title, meta),
      metadata: { ...meta },
    };
  }

  const commandResult = isPlainObject(result.result) ? result.result : null;
  if (commandResult) {
    const commandOutput = joinCommandOutput(commandResult);
    if (commandOutput) {
      const title = firstStringField(result, ['command']) || `${toolName} command`;
      return {
        id: artifactId,
        type: 'code',
        title,
        content: commandOutput,
        language: 'text',
        metadata: { ...meta },
      };
    }
  }

  const textWindow = isPlainObject(result.text_window) ? result.text_window : null;
  if (textWindow && typeof textWindow.text === 'string') {
    return {
      id: artifactId,
      type: 'code',
      title: firstStringField(textWindow, ['title', 'paper_key', 'arxiv_id']) || 'Paper Text',
      content: textWindow.text,
      language: 'text',
      metadata: { ...meta },
    };
  }

  const paperPages = isPlainObject(result.paper_pages) ? result.paper_pages : null;
  if (paperPages && Array.isArray(paperPages.pages)) {
    const pageText = paperPages.pages
      .filter((page: any) => isPlainObject(page) && typeof page.text === 'string')
      .map((page: any) => {
        const pageNumber = Number(page.page_number);
        const label = Number.isFinite(pageNumber) && pageNumber > 0 ? `Page ${pageNumber}` : 'Page';
        return `${label}\n\n${page.text}`;
      })
      .join('\n\n');
    if (pageText) {
      return {
        id: artifactId,
        type: 'code',
        title: firstStringField(paperPages, ['title', 'paper_key', 'arxiv_id']) || 'Paper Pages',
        content: pageText,
        language: 'text',
        metadata: { ...meta },
      };
    }
  }

  return null;
}

function nativeFileArtifactFromPromotedResult(
  fallbackArtifactId: string,
  toolName: string,
  ok: boolean,
  result: unknown,
  meta: Record<string, any>,
): Artifact | null {
  if (!ok) return null;
  const resultMetadata = isPlainObject(meta.result_metadata)
    ? (meta.result_metadata as Record<string, any>)
    : {};
  const nativeFileInput =
    resultMetadata.native_file_input === true ||
    resultMetadata.model_file_input === true ||
    (isPlainObject(result) &&
      ((result as any).native_file_input === true ||
        (result as any).model_file_input === true));
  if (!nativeFileInput) return null;

  const attachmentIDs = normalizeStringArray(
    resultMetadata.attachment_artifact_ids,
    resultMetadata.attachmentArtifactIds,
    isPlainObject(result) ? (result as any).attachment_artifact_ids : undefined,
    isPlainObject(result) ? (result as any).attachmentArtifactIds : undefined,
  );
  const artifactId = String(
    resultMetadata.attachment_artifact_id ||
      resultMetadata.attachmentArtifactId ||
      resultMetadata.file_artifact_id ||
      (isPlainObject(result) ? (result as any).attachment_artifact_id : '') ||
      (isPlainObject(result) ? (result as any).attachmentArtifactId : '') ||
      attachmentIDs?.[0] ||
      '',
  ).trim();
  if (!artifactId) return null;

  const resultMap = isPlainObject(result) ? (result as Record<string, any>) : {};
  const nestedFile = firstPlainObjectField(resultMap, ['file', 'download', 'pdf_file']);
  const title =
    firstStringField(resultMetadata, ['file_name', 'fileName', 'name', 'title']) ||
    firstStringField(resultMap, ['file_name', 'fileName', 'name', 'title']) ||
    (nestedFile ? firstStringField(nestedFile, ['file_name', 'fileName', 'name', 'title']) : null) ||
    `${toolName} file`;
  const mimeType =
    firstStringField(resultMetadata, [
      'mimeType',
      'mime_type',
      'detectedMimeType',
      'detected_mime_type',
      'normalizedMimeType',
      'normalized_mime_type',
    ]) ||
    firstStringField(resultMap, ['mimeType', 'mime_type', 'mimetype', 'contentType', 'content_type']) ||
    (nestedFile ? firstStringField(nestedFile, ['mimeType', 'mime_type', 'mimetype', 'contentType', 'content_type']) : null);

  return {
    id: artifactId,
    type: 'file',
    title,
    content: '',
    metadata: {
      ...meta,
      source: resultMetadata.source || 'external_tool',
      external_tool_file: true,
      externalToolFile: true,
      model_file_input: true,
      native_file_input: true,
      attachment_artifact_id: artifactId,
      attachmentArtifactId: artifactId,
      ...(attachmentIDs ? { attachment_artifact_ids: attachmentIDs } : {}),
      ...(mimeType ? { mime_type: mimeType, mimeType } : {}),
      fallback_artifact_id: fallbackArtifactId,
    },
  };
}

function firstPlainObjectField(value: Record<string, any>, keys: string[]): Record<string, any> | null {
  for (const key of keys) {
    const raw = value[key];
    if (isPlainObject(raw)) return raw;
  }
  return null;
}

function firstStringField(value: Record<string, any>, keys: string[]): string | null {
  for (const key of keys) {
    const raw = value[key];
    if (typeof raw !== 'string') continue;
    const trimmed = raw.trim();
    if (trimmed) return raw;
  }
  return null;
}

function joinCommandOutput(value: Record<string, any>): string {
  const parts: string[] = [];
  const stdout = firstStringField(value, ['stdout', 'output']);
  const stderr = firstStringField(value, ['stderr', 'error']);
  if (stdout) parts.push(stdout);
  if (stderr) parts.push(parts.length > 0 ? `stderr:\n${stderr}` : stderr);
  return parts.join('\n\n').trim();
}

function languageFromTitleOrMeta(title: string, meta: Record<string, any>): string {
  const explicit = String(meta.language || meta.file_language || '').trim();
  if (explicit) return explicit;
  const ext = title.match(/\.([a-zA-Z0-9]+)(?:\?|#)?$/)?.[1];
  return ext || 'text';
}

function titleFromToolName(toolName: string): string {
  return (
    toolName
      .split('_')
      .filter(Boolean)
      .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
      .join(' ') || 'Tool Result'
  );
}

function normalizeArtifactType(value: unknown): Artifact['type'] {
  switch (String(value || '').trim()) {
    case 'data':
      return 'data';
    case 'image':
      return 'image';
    case 'file':
      return 'file';
    case 'code':
    default:
      return 'code';
  }
}

function stringOrUndefined(value: unknown): string | undefined {
  const trimmed = String(value || '').trim();
  return trimmed || undefined;
}

function safeJsonStringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    try {
      return JSON.stringify({ error: 'Failed to serialize value' }, null, 2);
    } catch {
      return '{"error":"Failed to serialize value"}';
    }
  }
}

function isPlainObject(value: any): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function normalizeToolPresentation(
  primary: unknown,
  ...fallbacks: unknown[]
): Record<string, any> | null {
  if (isPlainObject(primary)) return primary;
  for (const fallback of fallbacks) {
    if (isPlainObject(fallback)) return fallback;
  }
  return null;
}

function parseReadFileResult(
  text: string,
): { path: string; content: string; language: string } | null {
  const match = text.match(/^File: (.+)\n\n/);
  if (!match) return null;
  const path = match[1].trim();
  const content = text.replace(/^File: .+\n\n/, '');
  const ext = path.match(/\.([a-zA-Z0-9]+)$/)?.[1];
  return { path, content, language: ext || 'text' };
}

function unwrapSingleArrayField(value: any): any {
  if (!isPlainObject(value)) return value;
  const maybeRows = (value as any).rows;
  if (Array.isArray(maybeRows)) return maybeRows;
  const keys = Object.keys(value);
  if (keys.length !== 1) return value;
  const inner = (value as any)[keys[0]];
  return Array.isArray(inner) ? inner : value;
}

function isArrayOfObjects(value: any): value is Array<Record<string, any>> {
  return Array.isArray(value) && value.length > 0 && isPlainObject(value[0]);
}

function isArrayOfObjectsOrEmpty(
  value: any,
): value is Array<Record<string, any>> {
  return (
    Array.isArray(value) && (value.length === 0 || isPlainObject(value[0]))
  );
}

function parseImageDataUri(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (!/^data:image\//i.test(trimmed)) return null;
  return trimmed;
}

function extractImageDataUriFromToolResult(
  resultFormat: ToolResultEnvelope['result_format'],
  result: any,
  fallbackRawContent: string | null,
): string | null {
  if (resultFormat === 'text') {
    return parseImageDataUri(
      typeof result === 'string' ? result : fallbackRawContent,
    );
  }
  if (!isPlainObject(result)) return null;

  const direct = parseImageDataUri(
    (result as any).dataUri ??
      (result as any).data_uri ??
      (result as any).image ??
      (result as any).imageDataUri ??
      (result as any).image_data_uri ??
      (result as any).url,
  );
  if (direct) return direct;

  const imageURL = (result as any).image_url;
  if (isPlainObject(imageURL)) {
    return parseImageDataUri((imageURL as any).url);
  }

  return null;
}

function inferImageMimeTypeFromDataUri(dataUri: string): string | undefined {
  const raw = dataUri.trim();
  const comma = raw.indexOf(',');
  if (comma <= 5) return undefined;
  const meta = raw.slice(5, comma);
  const semi = meta.indexOf(';');
  const mime = (semi >= 0 ? meta.slice(0, semi) : meta).trim().toLowerCase();
  if (!mime.startsWith('image/')) return undefined;
  return mime;
}

function normalizeStringArray(...values: any[]): string[] | undefined {
  const out: string[] = [];
  const seen = new Set<string>();

  for (const value of values) {
    if (!Array.isArray(value)) continue;
    for (const item of value) {
      if (typeof item !== 'string') continue;
      const trimmed = item.trim();
      if (!trimmed || seen.has(trimmed)) continue;
      seen.add(trimmed);
      out.push(trimmed);
    }
  }

  return out.length > 0 ? out : undefined;
}

function normalizeChartBudget(...values: any[]): ChartBudgetReport | undefined {
  for (const value of values) {
    if (!isPlainObject(value)) continue;
    const out: Record<string, any> = {};
    const schemaVersion = String((value as any).schema_version || '').trim();
    if (schemaVersion) out.schema_version = schemaVersion;
    const chartKind = String((value as any).chart_kind || '').trim();
    if (chartKind) out.chart_kind = chartKind;
    const chartEngine = String((value as any).chart_engine || '').trim();
    if (chartEngine) out.chart_engine = chartEngine;
    const asFiniteNonNegativeInt = (raw: any): number | undefined => {
      const n = Number(raw);
      if (!Number.isFinite(n) || n < 0) return undefined;
      return Math.trunc(n);
    };
    const rowCountTotal = asFiniteNonNegativeInt(
      (value as any).row_count_total,
    );
    if (rowCountTotal !== undefined) out.row_count_total = rowCountTotal;
    const pointsRequested = asFiniteNonNegativeInt(
      (value as any).points_requested,
    );
    if (pointsRequested !== undefined) out.points_requested = pointsRequested;
    const pointsReturned = asFiniteNonNegativeInt(
      (value as any).points_returned,
    );
    if (pointsReturned !== undefined) out.points_returned = pointsReturned;
    const seriesCount = asFiniteNonNegativeInt((value as any).series_count);
    if (seriesCount !== undefined) out.series_count = seriesCount;
    const plotSpecBytes = asFiniteNonNegativeInt(
      (value as any).plot_spec_bytes,
    );
    if (plotSpecBytes !== undefined) out.plot_spec_bytes = plotSpecBytes;
    const rowsPreviewBytes = asFiniteNonNegativeInt(
      (value as any).rows_preview_bytes,
    );
    if (rowsPreviewBytes !== undefined)
      out.rows_preview_bytes = rowsPreviewBytes;
    const resultPayloadBytes = asFiniteNonNegativeInt(
      (value as any).result_payload_bytes,
    );
    if (resultPayloadBytes !== undefined)
      out.result_payload_bytes = resultPayloadBytes;
    const sseEventBytes = asFiniteNonNegativeInt(
      (value as any).sse_event_bytes,
    );
    if (sseEventBytes !== undefined) out.sse_event_bytes = sseEventBytes;
    const budgetActions = normalizeStringArray((value as any).budget_actions);
    if (budgetActions) out.budget_actions = budgetActions;
    const failureStage = String((value as any).failure_stage || '').trim();
    if (failureStage) out.failure_stage = failureStage;
    const failureCode = String((value as any).failure_code || '').trim();
    if (failureCode) out.failure_code = failureCode;
    const failureMessage = String((value as any).failure_message || '').trim();
    if (failureMessage) out.failure_message = failureMessage;
    if (Object.keys(out).length > 0) return out as ChartBudgetReport;
  }
  return undefined;
}
