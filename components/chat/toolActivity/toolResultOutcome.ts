import type { ToolResultEnvelope } from '@/types';

/**
 * The envelope `ok` flag is the canonical outcome of the tool invocation.
 * Payload error fields may describe a nested operation,
 * warning, or observed remote job and must not silently override it.
 */
export function isToolResultFailure(
  envelope: Pick<ToolResultEnvelope, 'ok'> | null | undefined,
): boolean {
  return envelope?.ok === false;
}
