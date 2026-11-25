import type { Artifact } from '@/types';
import type { RendererEntry } from './types';

const toolRenderers = new Map<string, RendererEntry>();
const typeRenderers = new Map<string, RendererEntry>();
const customResolvers: Array<(artifact: Artifact) => RendererEntry | null> = [];

export function registerToolRenderer(toolName: string, entry: RendererEntry): void {
  toolRenderers.set(toolName, entry);
}

export function registerTypeRenderer(type: string, entry: RendererEntry): void {
  typeRenderers.set(type, entry);
}

export function registerCustomResolver(fn: (artifact: Artifact) => RendererEntry | null): void {
  customResolvers.push(fn);
}

function isPlainObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function resolveToolPresentation(artifact: Artifact): Record<string, any> | null {
  const metadata = artifact.metadata || {};
  if (isPlainObject(metadata.tool_presentation)) return metadata.tool_presentation;
  if (
    isPlainObject(metadata.result_metadata) &&
    isPlainObject(metadata.result_metadata.tool_presentation)
  ) {
    return metadata.result_metadata.tool_presentation;
  }
  return null;
}

function presentationTypeKey(presentation: Record<string, any> | null): string | null {
  if (!presentation) return null;
  const rendererStrategy = String(presentation.renderer_strategy || '').trim();
  const rendererKey = String(presentation.renderer_key || '').trim();
  if (rendererStrategy === 'artifact_type' && rendererKey && typeRenderers.has(rendererKey)) {
    return rendererKey;
  }
  const nativeViewer = String(presentation.native_viewer || '').trim();
  switch (nativeViewer) {
    case 'file':
    case 'code':
    case 'data':
    case 'image':
      return nativeViewer;
    case 'chart':
    case 'query':
      return 'data';
    case 'interactive_html':
      return 'code';
    default:
      return null;
  }
}

function explicitArtifactTypeKey(artifact: Artifact): string | null {
  const presentation = resolveToolPresentation(artifact);
  if (!presentation) return null;
  if (String(presentation.renderer_strategy || '').trim() !== 'artifact_type') return null;
  const key = String(presentation.renderer_key || presentation.native_viewer || '').trim();
  return key || null;
}

export function resolveRenderer(artifact: Artifact): RendererEntry | null {
  // 0. Failed tool calls route through custom resolvers first so every error
  //    surfaces through the structured diagnostic UI (ErrorRenderer for
  //    generic tools; connector-specific inspectors like GitHubToolRenderer
  //    for tools that register a custom resolver covering both success and
  //    error). Without this, an exact-name tool renderer designed for the
  //    success payload would dump the error JSON into a syntax-highlighter.
  if (artifact.metadata?.ok === false) {
    for (const resolver of customResolvers) {
      const entry = resolver(artifact);
      if (entry) return entry;
    }
  }

  // 1. Exact tool_name match
  const toolName = artifact.metadata?.tool_name;
  if (typeof toolName === 'string' && toolRenderers.has(toolName)) {
    return toolRenderers.get(toolName)!;
  }

  // 2. Explicit native-viewer contract. When the producing tool declared
  //    renderer_strategy 'artifact_type' and the artifact itself carries that
  //    type, the native type renderer owns it: a downloaded paper or
  //    attachment is a file first, whatever connector fetched it. Broad
  //    custom resolvers (arXiv, GitHub, etc.) must not hijack these. Envelope
  //    artifacts whose type differs from the declared key (e.g. a JSON
  //    'data' envelope presented as 'code') keep flowing to the specialized
  //    inspectors below, as do approval envelopes, which are never typed as
  //    their tool's native key.
  if (artifact.metadata?.ok !== false) {
    const nativeKey = explicitArtifactTypeKey(artifact);
    if (nativeKey && nativeKey === artifact.type && typeRenderers.has(nativeKey)) {
      return typeRenderers.get(nativeKey)!;
    }
  }

  // 3. Custom resolver functions
  for (const resolver of customResolvers) {
    const entry = resolver(artifact);
    if (entry) return entry;
  }

  // 4. Presentation-driven native viewer fallback
  const presentationType = presentationTypeKey(resolveToolPresentation(artifact));
  if (presentationType && typeRenderers.has(presentationType)) {
    return typeRenderers.get(presentationType)!;
  }

  // 5. Type fallback
  if (typeRenderers.has(artifact.type)) {
    return typeRenderers.get(artifact.type)!;
  }

  // 6. No match
  return null;
}
