export interface SSEMessage {
  event?: string;
  data: string;
  comment?: string;
}

interface SSEParseOptions {
  signal?: AbortSignal;
  maxBufferBytes?: number;
  maxEventBytes?: number;
  includeComments?: boolean;
}

export async function* parseSSE(
  stream: ReadableStream<Uint8Array>,
  opts?: SSEParseOptions,
): AsyncGenerator<SSEMessage> {
  const reader = stream.getReader();
  const decoder = new TextDecoder('utf-8');
  const maxBufferBytes = Math.max(1024, opts?.maxBufferBytes ?? 8 * 1024 * 1024);
  const maxEventBytes = Math.max(1024, opts?.maxEventBytes ?? 4 * 1024 * 1024);

  let buffer = '';
  let eventName: string | undefined;
  let dataLines: string[] = [];
  let eventBytes = 0;

  const resetEvent = () => {
    eventName = undefined;
    dataLines = [];
    eventBytes = 0;
  };

  const flush = async () => {
    if (dataLines.length === 0) {
      eventName = undefined;
      return;
    }

    const data = dataLines.join('\n');
    const out: SSEMessage = { data };
    if (eventName) out.event = eventName;
    resetEvent();
    return out;
  };

  const abort = async () => {
    try {
      await reader.cancel();
    } catch {
      // ignore
    }
  };

  const signal = opts?.signal;
  if (signal?.aborted) {
    await abort();
    return;
  }

  const processLine = (line: string): SSEMessage | null => {
    if (line === '') return null;
    if (line.startsWith(':')) {
      if (!opts?.includeComments) return null;
      let comment = line.slice(1);
      if (comment.startsWith(' ')) comment = comment.slice(1);
      return { data: '', comment };
    }

    const separatorIndex = line.indexOf(':');
    let fieldName = line;
    let fieldValue = '';
    if (separatorIndex >= 0) {
      fieldName = line.slice(0, separatorIndex);
      fieldValue = line.slice(separatorIndex + 1);
      if (fieldValue.startsWith(' ')) fieldValue = fieldValue.slice(1);
    }

    if (fieldName === 'event') {
      eventName = fieldValue;
      return null;
    }
    if (fieldName !== 'data') return null;

    const projectedSize = eventBytes + fieldValue.length + (dataLines.length > 0 ? 1 : 0);
    if (projectedSize > maxEventBytes) {
      throw new Error(`SSE event exceeded ${maxEventBytes} bytes`);
    }
    eventBytes = projectedSize;
    dataLines.push(fieldValue);
    return null;
  };

  let done = false;
  while (!done) {
    if (signal?.aborted) {
      await abort();
      return;
    }

    const chunk = await reader.read();
    done = chunk.done;
    if (chunk.value) buffer += decoder.decode(chunk.value, { stream: true });

    if (buffer.length > maxBufferBytes) {
      throw new Error(`SSE buffer exceeded ${maxBufferBytes} bytes`);
    }

    let lineEnd = buffer.indexOf('\n');
    while (lineEnd >= 0) {
      if (signal?.aborted) {
        await abort();
        return;
      }

      let line = buffer.slice(0, lineEnd);
      if (line.endsWith('\r')) line = line.slice(0, -1);
      buffer = buffer.slice(lineEnd + 1);

      if (line === '') {
        const out = await flush();
        if (out) yield out;
      } else {
        const out = processLine(line);
        if (out) yield out;
      }

      lineEnd = buffer.indexOf('\n');
    }
  }

  // Pragmatic EOF flush: some streams close without a trailing blank line.
  if (buffer.length > 0) {
    let line = buffer;
    if (line.endsWith('\r')) line = line.slice(0, -1);
    if (line.length > maxBufferBytes) {
      throw new Error(`SSE trailing buffer exceeded ${maxBufferBytes} bytes`);
    }
    if (line !== '') {
      const comment = processLine(line);
      if (comment) yield comment;
    }
  }

  const out = await flush();
  if (out) yield out;
}
