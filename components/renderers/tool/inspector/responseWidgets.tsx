import React from 'react';

/**
 * Response-widget registry for the generic ToolInspector.
 *
 * The generic inspector renders unknown payloads through the auto table /
 * key-value fallback (values.tsx). Tools that ship a first-class rendered
 * view (the Jira issue card today) register a matcher here so the inspector's
 * Response zone shows the real widget while everything else about the
 * three-zone shell — Request zone, identity chips, the Raw disclosure —
 * stays shared. This mirrors the chat-side inline preview registry
 * (components/chat/toolActivity/inlinePreviewRegistry.tsx) at inspector scale.
 *
 * Matchers must return false when they cannot produce a meaningful render
 * (e.g. the payload shape is unrecognisable) so the generic fallback keeps
 * the inspector complete.
 */

export interface InspectorResponseWidgetProps {
  /** The tool result (artifact raw_result / parsed content), UI keys stripped. */
  result: unknown;
  /** The artifact metadata (tool_name, tool_args, result_metadata, …). */
  meta: Record<string, any>;
}

interface InspectorResponseWidgetEntry {
  match: (meta: Record<string, any>, result: unknown) => boolean;
  component: React.ComponentType<InspectorResponseWidgetProps>;
}

const entries: InspectorResponseWidgetEntry[] = [];

export function registerInspectorResponseWidget(entry: InspectorResponseWidgetEntry): void {
  entries.push(entry);
}

export function resolveInspectorResponseWidget(
  meta: Record<string, any>,
  result: unknown,
): React.ComponentType<InspectorResponseWidgetProps> | null {
  for (const entry of entries) {
    try {
      if (entry.match(meta, result)) return entry.component;
    } catch {
      // A broken matcher must never take down the inspector.
    }
  }
  return null;
}
