import type {
  MessageBlockReference,
  MessageBlockReferenceKind,
  UIMessage,
} from '@/types';
import { Role } from '@/types';

const MAX_MESSAGE_REFERENCE_PREVIEW = 180;

export interface MessageIdentity {
  messageId?: string;
  messageSeq?: number;
  key: string;
}

export function normalizeMessageId(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  const trimmed = value.trim();
  return trimmed || undefined;
}

export function normalizeMessageSeq(value: unknown): number | undefined {
  const seq = Number(value);
  return Number.isFinite(seq) && seq > 0 ? Math.trunc(seq) : undefined;
}

export function normalizeMessageReferenceSnapshot(
  value: string | null | undefined,
): string {
  return String(value || '')
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .trim();
}

export function toMessageReferencePreview(
  snapshot: string,
  maxChars = MAX_MESSAGE_REFERENCE_PREVIEW,
): string {
  const oneLine = snapshot.replace(/\s+/g, ' ').trim();
  if (!oneLine) return '(empty)';
  if (oneLine.length <= maxChars) return oneLine;
  return `${oneLine.slice(0, maxChars - 3).trimEnd()}...`;
}

function hashDeterministically(value: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16).padStart(8, '0');
}

function buildContentFallbackIdentitySource(message: UIMessage): string {
  const toolCallSignature = Array.isArray(message.tool_calls)
    ? message.tool_calls
        .map((toolCall) => {
          const callId =
            typeof toolCall?.id === 'string' ? toolCall.id.trim() : '';
          const name =
            typeof toolCall?.name === 'string' ? toolCall.name.trim() : '';
          let argsText = '{}';
          try {
            argsText = JSON.stringify(toolCall?.args || {});
          } catch {
            argsText = '{}';
          }
          return `${callId}|${name}|${argsText}`;
        })
        .join('\n')
    : '';

  return [
    String(message.role || ''),
    typeof message.name === 'string' ? message.name.trim() : '',
    typeof message.tool_call_id === 'string' ? message.tool_call_id.trim() : '',
    normalizeMessageReferenceSnapshot(message.content),
    toolCallSignature,
  ].join('\n');
}

function defaultReferenceKind(
  role: Role,
): MessageBlockReferenceKind | null {
  if (role === Role.User) return 'user_message';
  if (role === Role.Assistant) return 'assistant_message';
  if (role === Role.Tool) return 'tool_result';
  return null;
}

function defaultReferenceTitle(kind: MessageBlockReferenceKind): string {
  if (kind === 'user_message') return 'User message';
  if (kind === 'assistant_message') return 'Assistant message';
  if (kind === 'tool_call') return 'Tool call';
  return 'Tool result';
}

function buildToolCallSummarySnapshot(message: UIMessage): string {
  if (!Array.isArray(message.tool_calls) || message.tool_calls.length === 0) {
    return '';
  }

  const lines = message.tool_calls
    .map((toolCall) => {
      const toolName =
        typeof toolCall?.name === 'string' && toolCall.name.trim()
          ? toolCall.name.trim()
          : 'tool';
      const args = toolCall?.args || {};
      const detail =
        (typeof args.description === 'string' && args.description.trim()) ||
        (typeof args.path === 'string' && args.path.trim()) ||
        (typeof args.query === 'string' && args.query.trim()) ||
        (typeof args.sql === 'string' && args.sql.trim()) ||
        '';
      return detail ? `${toolName}: ${detail}` : toolName;
    })
    .filter(Boolean);

  if (lines.length === 0) return '';
  return ['Tools:', ...lines].join('\n');
}

function getMessageReferenceTextSnapshot(
  message: UIMessage,
  fallbackSnapshot?: string | null,
): string {
  const content = normalizeMessageReferenceSnapshot(message.content);
  if (content) return content;

  if (message.role === Role.Assistant) {
    const stablePrefix =
      typeof (message as any).streamStablePrefix === 'string'
        ? (message as any).streamStablePrefix
        : '';
    const unstableSuffix =
      typeof (message as any).streamUnstableSuffix === 'string'
        ? (message as any).streamUnstableSuffix
        : '';
    const streamText = normalizeMessageReferenceSnapshot(
      `${stablePrefix}${unstableSuffix}`,
    );
    if (streamText) return streamText;

    const thinking = normalizeMessageReferenceSnapshot(message.thinking);
    if (thinking) return thinking;

    const compactionSummary = normalizeMessageReferenceSnapshot(
      (message as any).compactionSummary,
    );
    if (compactionSummary) return compactionSummary;

    const toolCallSummary = normalizeMessageReferenceSnapshot(
      buildToolCallSummarySnapshot(message),
    );
    if (toolCallSummary) return toolCallSummary;
  }

  return normalizeMessageReferenceSnapshot(fallbackSnapshot);
}

export function getMessageBaseIdentity(message: UIMessage): MessageIdentity {
  const messageId = normalizeMessageId(message.id);
  const messageSeq = normalizeMessageSeq(message.seq);

  if (messageId) {
    return { messageId, messageSeq, key: `id:${messageId}` };
  }
  if (messageSeq !== undefined) {
    return { messageId, messageSeq, key: `seq:${messageSeq}` };
  }

  const uiStableId =
    typeof message._uiStableId === 'string' ? message._uiStableId.trim() : '';
  if (uiStableId) {
    return { messageId, messageSeq, key: `ui:${uiStableId}` };
  }

  const source = buildContentFallbackIdentitySource(message);
  const digest = hashDeterministically(source);
  return {
    messageId,
    messageSeq,
    key: `content:${digest}:${source.length}`,
  };
}

export function buildStableMessageIdentities(
  messages: UIMessage[],
): MessageIdentity[] {
  const seenByBaseKey = new Map<string, number>();

  return messages.map((message) => {
    const baseIdentity = getMessageBaseIdentity(message);
    const count = (seenByBaseKey.get(baseIdentity.key) || 0) + 1;
    seenByBaseKey.set(baseIdentity.key, count);

    if (count === 1) return baseIdentity;
    return {
      ...baseIdentity,
      key: `${baseIdentity.key}:dup:${count}`,
    };
  });
}

export function buildMessageBlockReference({
  identity,
  role,
  snapshot,
  kind,
  title,
  label,
  idSuffix,
}: {
  identity: MessageIdentity;
  role: Role;
  snapshot: string | null | undefined;
  kind?: MessageBlockReferenceKind;
  title?: string;
  label?: string;
  idSuffix?: string;
}): MessageBlockReference | null {
  const normalizedSnapshot = normalizeMessageReferenceSnapshot(snapshot);
  if (!normalizedSnapshot) return null;

  const resolvedKind = kind ?? defaultReferenceKind(role);
  if (!resolvedKind) return null;

  const resolvedTitle =
    String(title || '').trim() || defaultReferenceTitle(resolvedKind);
  const resolvedSuffix =
    String(idSuffix || resolvedKind).trim() || resolvedKind;
  const id = `message:${identity.key}:${resolvedSuffix}`;

  return {
    id,
    key: id,
    kind: resolvedKind,
    messageId: identity.messageId,
    messageSeq: identity.messageSeq,
    role,
    title: resolvedTitle,
    label:
      String(label || '').trim() ||
      (identity.messageSeq !== undefined
        ? `${resolvedTitle} #${identity.messageSeq}`
        : resolvedTitle),
    preview: toMessageReferencePreview(normalizedSnapshot),
    contentSnapshot: normalizedSnapshot,
  };
}

export function buildMessageTextReference(
  message: UIMessage,
  identity: MessageIdentity,
  fallbackSnapshot?: string | null,
): MessageBlockReference | null {
  return buildMessageBlockReference({
    identity,
    role: message.role,
    snapshot: getMessageReferenceTextSnapshot(message, fallbackSnapshot),
  });
}

export function buildToolResultReference(
  message: UIMessage,
  identity: MessageIdentity,
): MessageBlockReference {
  const toolName =
    typeof message.name === 'string' && message.name.trim()
      ? message.name.trim()
      : 'tool';
  const title = `Tool result - ${toolName}`;
  return buildMessageBlockReference({
    identity,
    role: Role.Tool,
    snapshot:
      normalizeMessageReferenceSnapshot(message.content) || '(empty)',
    kind: 'tool_result',
    title,
  }) as MessageBlockReference;
}

export function buildReferenceForMessage(
  message: UIMessage,
  identity: MessageIdentity,
): MessageBlockReference | null {
  if (message.role === Role.User || message.role === Role.Assistant) {
    return buildMessageTextReference(message, identity);
  }
  if (message.role === Role.Tool) {
    return buildToolResultReference(message, identity);
  }
  return null;
}
