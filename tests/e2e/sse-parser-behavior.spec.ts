import { expect, test } from '@playwright/test';

test('SSE parser preserves multiline data semantics and flushes trailing events at EOF', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const parser = await import('/providers/stream/sseParser.ts' as string);
    const encoder = new TextEncoder();

    const firstStream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('event: text_delta\ndata: line1\ndata:  line2\n\n'));
        controller.enqueue(encoder.encode('Event: ignored-case\ndata: should-discard\n'));
        controller.close();
      },
    });

    const secondStream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode('event: tool_result\ndata: complete\n\n'));
        controller.enqueue(encoder.encode('event: text_delta\ndata: incomplete'));
        controller.close();
      },
    });

    const firstEvents: Array<{ event?: string; data: string }> = [];
    for await (const ev of parser.parseSSE(firstStream)) firstEvents.push(ev);

    const secondEvents: Array<{ event?: string; data: string }> = [];
    for await (const ev of parser.parseSSE(secondStream)) secondEvents.push(ev);

    return { firstEvents, secondEvents };
  });

  expect(result.firstEvents).toHaveLength(2);
  expect(result.firstEvents[0]).toEqual({
    event: 'text_delta',
    data: 'line1\n line2',
  });
  expect(result.firstEvents[1]).toEqual({
    data: 'should-discard',
  });

  expect(result.secondEvents).toHaveLength(2);
  expect(result.secondEvents[0]).toEqual({
    event: 'tool_result',
    data: 'complete',
  });
  expect(result.secondEvents[1]).toEqual({
    event: 'text_delta',
    data: 'incomplete',
  });
});

test('SSE parser throws when event exceeds configured size', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const parser = await import('/providers/stream/sseParser.ts' as string);
    const encoder = new TextEncoder();
    const bigData = 'x'.repeat(5000);

    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode(`data: ${bigData}\n\n`));
        controller.close();
      },
    });

    try {
      for await (const _ev of parser.parseSSE(stream, { maxEventBytes: 1024 })) {
        // no-op
      }
      return { threw: false, message: '' };
    } catch (err) {
      return { threw: true, message: String((err as Error)?.message || err || '') };
    }
  });

  expect(result.threw).toBe(true);
  expect(result.message).toContain('SSE event exceeded');
});

test('SSE parser can expose heartbeat comments without changing default event output', async ({ page }) => {
  await page.goto('/tests/e2e/fixtures/module-harness.html', { waitUntil: 'domcontentloaded' });

  const result = await page.evaluate(async () => {
    const parser = await import('/providers/stream/sseParser.ts' as string);
    const encoder = new TextEncoder();

    const makeStream = () => new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(encoder.encode(': ping\n\nevent: text_delta\ndata: hello\n\n'));
        controller.close();
      },
    });

    const defaultEvents: Array<{ event?: string; data: string; comment?: string }> = [];
    for await (const ev of parser.parseSSE(makeStream())) defaultEvents.push(ev);

    const commentEvents: Array<{ event?: string; data: string; comment?: string }> = [];
    for await (const ev of parser.parseSSE(makeStream(), { includeComments: true })) {
      commentEvents.push(ev);
    }

    return { defaultEvents, commentEvents };
  });

  expect(result.defaultEvents).toEqual([
    { event: 'text_delta', data: 'hello' },
  ]);
  expect(result.commentEvents).toEqual([
    { data: '', comment: 'ping' },
    { event: 'text_delta', data: 'hello' },
  ]);
});
