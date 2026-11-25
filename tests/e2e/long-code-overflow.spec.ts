import { expect, test } from '@playwright/test';

for (const width of [1280, 360, 320]) {
  test(`long chat content stays within viewport width at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await page.goto('/tests/e2e/fixtures/long-code-overflow.html', { waitUntil: 'networkidle' });

    const dims = await page.evaluate(() => {
      const fixture = document.getElementById('fixture-viewport');
      const messageBoxes = Array.from(document.querySelectorAll('[data-overflow-check] .group\\/msg'));
      const plainTextBlocks = Array.from(document.querySelectorAll('#assistant-plain .chat-markdown, #user-plain p'));
      const pre = document.querySelector('pre');
      const preContainer = pre?.parentElement;
      const table = document.querySelector('#assistant-table table');
      const tableContainer = table?.parentElement;

      return {
        viewportWidth: window.innerWidth,
        bodyScrollWidth: Math.ceil(document.body.scrollWidth),
        htmlScrollWidth: Math.ceil(document.documentElement.scrollWidth),
        fixtureWidth: Math.ceil(fixture?.clientWidth ?? 0),
        messageWidths: messageBoxes.map((el) => Math.ceil(el.clientWidth)),
        messageScrollWidths: messageBoxes.map((el) => Math.ceil(el.scrollWidth)),
        plainTextWidths: plainTextBlocks.map((el) => Math.ceil((el as HTMLElement).clientWidth)),
        plainTextScrollWidths: plainTextBlocks.map((el) => Math.ceil((el as HTMLElement).scrollWidth)),
        preClientWidth: Math.ceil(pre?.clientWidth ?? 0),
        preContainerOverflowX: preContainer ? getComputedStyle(preContainer).overflowX : '',
        tableContainerWidth: Math.ceil(tableContainer?.clientWidth ?? 0),
        tableContainerScrollWidth: Math.ceil(tableContainer?.scrollWidth ?? 0),
        tableContainerOverflowX: tableContainer ? getComputedStyle(tableContainer).overflowX : '',
      };
    });

    expect(dims.bodyScrollWidth).toBeLessThanOrEqual(dims.viewportWidth + 1);
    expect(dims.htmlScrollWidth).toBeLessThanOrEqual(dims.viewportWidth + 1);
    expect(dims.fixtureWidth).toBeLessThanOrEqual(dims.viewportWidth);
    expect(dims.messageWidths.length).toBeGreaterThanOrEqual(4);
    for (let i = 0; i < dims.messageWidths.length; i += 1) {
      expect(dims.messageWidths[i]).toBeGreaterThan(0);
      expect(dims.messageWidths[i]).toBeLessThanOrEqual(dims.fixtureWidth);
      expect(dims.messageScrollWidths[i]).toBeLessThanOrEqual(dims.messageWidths[i] + 1);
    }
    expect(dims.plainTextWidths.length).toBeGreaterThanOrEqual(2);
    for (let i = 0; i < dims.plainTextWidths.length; i += 1) {
      expect(dims.plainTextScrollWidths[i]).toBeLessThanOrEqual(dims.plainTextWidths[i] + 1);
    }
    expect(dims.preClientWidth).toBeGreaterThan(0);
    expect(dims.preClientWidth).toBeLessThanOrEqual(dims.fixtureWidth);
    expect(dims.preContainerOverflowX).toBe('auto');
    expect(dims.tableContainerWidth).toBeGreaterThan(0);
    expect(dims.tableContainerWidth).toBeLessThanOrEqual(dims.fixtureWidth);
    expect(dims.tableContainerScrollWidth).toBeLessThanOrEqual(dims.tableContainerWidth + 1);
    expect(dims.tableContainerOverflowX).toBe('auto');
  });
}
