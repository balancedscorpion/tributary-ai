import type { ToolResultEnvelope } from '@/types';

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export function normalizeQueryCardToken(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed || null;
}

export function getRunScopedQueryCardKey(runId: unknown, toolCallId: unknown): string | null {
  const run = normalizeQueryCardToken(runId);
  const call = normalizeQueryCardToken(toolCallId);
  if (!run || !call) return null;
  return `run:${run}:call:${call}`;
}

export function getMessageScopedQueryCardKey(messageIdentityKey: unknown, toolCallId: unknown): string | null {
  const identity = normalizeQueryCardToken(messageIdentityKey);
  const call = normalizeQueryCardToken(toolCallId);
  if (!identity || !call) return null;
  return `message:${identity}:call:${call}`;
}

export function getQueryCardStateKey(args: {
  runId?: unknown;
  messageIdentityKey?: unknown;
  toolCallId?: unknown;
}): string | null {
  const runScoped = getRunScopedQueryCardKey(args.runId, args.toolCallId);
  if (runScoped) return runScoped;
  return getMessageScopedQueryCardKey(args.messageIdentityKey, args.toolCallId);
}

export function getEnvelopeResultRunId(envelope: ToolResultEnvelope | null | undefined): string | null {
  if (!envelope || !isPlainObject(envelope.result)) return null;
  return normalizeQueryCardToken((envelope.result as any)._uiRunId);
}
