import { expect, test, type Page } from "@playwright/test";

function jsonResponse(body: any, status = 200) {
  return {
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  };
}

const mockSettingsPayload = {
  provider: {
    providerId: "compatible",
    model: "test-model",
    profileId: "custom",
  },
  featureFlags: {
    activation: true,
    providers: {
      openai: true,
      anthropic: true,
      gemini: true,
      bedrock: true,
      compatible: true,
    },
    providerTemplates: true,
    chartDimensionFilters: true,
    cloudCredentialsAws: false,
    cloudCredentialsTrino: false,
    cloudCredentialsSnowflake: false,
    chartEngines: {
      echarts: true,
      highcharts: false,
    },
  },
  compatibleProfiles: [
    {
      id: "custom",
      displayName: "Custom Provider",
      apiType: "chat_completions",
      baseUrl: "https://mock.local/v1",
      auth: { type: "none", header: "Authorization", scheme: "Bearer" },
      apiKey: "",
      extraHeaders: [],
      queryParams: null,
      payloadOverrides: null,
      timeoutMs: null,
      reasoningEffort: null,
      modelInPath: false,
      modelInBody: true,
      endpointSuffix: null,
    },
  ],
  providerConfigs: [
    {
      id: "cfg-test",
      displayName: "Mock Compatible",
      providerId: "compatible",
      model: "test-model",
      apiType: "chat_completions",
      baseUrl: "https://mock.local/v1",
      authType: "none",
      authHeader: "Authorization",
      authScheme: "Bearer",
      extraHeaders: [],
      modelInPath: false,
      modelInBody: true,
      endpointSuffix: null,
    },
  ],
  activeProviderConfigId: "cfg-test",
};

type MockInstallOptions = {
  threadId: string;
  messages: any[];
  onRows?: (artifactId: string, url: URL) => any | null;
  onFilterChart?: (artifactId: string, body: any, url: URL) => any | null;
};

async function installMockApi(
  page: Page,
  { threadId, messages, onRows, onFilterChart }: MockInstallOptions,
): Promise<void> {
  const now = Date.now();
  const thread = {
    id: threadId,
    title: `Thread ${threadId}`,
    created_at: now - 1000,
    updated_at: now - 1000,
  };

  await page.route("**/api/**", async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const pathname = url.pathname;
    const method = req.method().toUpperCase();

    if (pathname === "/api/threads" && method === "GET") {
      await route.fulfill(jsonResponse({ threads: [thread] }));
      return;
    }

    if (pathname === `/api/threads/${threadId}` && method === "GET") {
      await route.fulfill(jsonResponse({ thread, messages }));
      return;
    }

    if (
      pathname.startsWith("/api/artifacts/") &&
      pathname.endsWith("/rows") &&
      method === "GET"
    ) {
      const artifactId = pathname.split("/")[3] || "";
      const payload = onRows?.(artifactId, url);
      if (payload) {
        await route.fulfill(jsonResponse(payload));
      } else {
        await route.fulfill(jsonResponse({ error: "artifact rows not found" }, 404));
      }
      return;
    }

    if (
      pathname.startsWith("/api/artifacts/") &&
      pathname.endsWith("/filter-chart") &&
      method === "POST"
    ) {
      const artifactId = pathname.split("/")[3] || "";
      let parsedBody: any = {};
      try {
        parsedBody = JSON.parse(req.postData() || "{}");
      } catch {
        parsedBody = {};
      }
      const payload = onFilterChart?.(artifactId, parsedBody, url);
      if (payload) {
        await route.fulfill(jsonResponse(payload));
      } else {
        await route.fulfill(jsonResponse({ error: "filter-chart not found" }, 404));
      }
      return;
    }

    if (pathname.startsWith("/api/settings") && method === "GET") {
      await route.fulfill(jsonResponse({ settings: mockSettingsPayload }));
      return;
    }

    if (
      pathname.startsWith("/api/settings") &&
      (method === "PUT" || method === "POST")
    ) {
      await route.fulfill(jsonResponse({ settings: mockSettingsPayload }));
      return;
    }

    if (pathname === "/api/secrets/status" && method === "GET") {
      await route.fulfill(
        jsonResponse({
          initialized: true,
          locked: false,
          configured: {
            openai: true,
            anthropic: true,
            gemini: true,
            compatible: {
              default: { apiKey: true },
            },
          },
        }),
      );
      return;
    }

    if (pathname.startsWith("/api/")) {
      await route.fulfill(jsonResponse({}));
      return;
    }

    await route.continue();
  });
}

async function selectDimensionValue(
  page: Page,
  columnName: string,
  value: string,
): Promise<void> {
  await page.locator("button", { hasText: `All ${columnName}` }).first().click();
  await page.getByRole("option", { name: value, exact: true }).click();
}

async function readEchartsCanvasSignature(page: Page): Promise<string> {
  return page.evaluate(() => {
    const host = document.querySelector('[data-testid="echarts-visualization"]');
    if (!host) return "";
    const canvas = host.querySelector("canvas") as HTMLCanvasElement | null;
    if (!canvas) return "";
    const dataUri = canvas.toDataURL("image/png");
    if (!dataUri) return "";

    let hash = 0x811c9dc5;
    for (let i = 0; i < dataUri.length; i += 1) {
      hash ^= dataUri.charCodeAt(i);
      hash = Math.imul(hash, 0x01000193);
    }
    return `${dataUri.length}:${(hash >>> 0).toString(16)}`;
  });
}

function buildEchartsSpec(series: Array<{ name: string; data: number[] }>) {
  return {
    schema_version: "echarts-plot-spec.v1",
    module: "core",
    options: {
      legend: { data: series.map((s) => s.name) },
      xAxis: { type: "category", data: ["2026-01", "2026-02"] },
      yAxis: { type: "value" },
      tooltip: { trigger: "axis" },
      series: series.map((s) => ({
        type: "line",
        name: s.name,
        data: s.data,
      })),
    },
  };
}

function buildSingleSeriesEchartsSpec(name: string, data: number[]) {
  return {
    schema_version: "echarts-plot-spec.v1",
    module: "core",
    options: {
      xAxis: { type: "category", data: ["2026-01", "2026-02"] },
      yAxis: { type: "value" },
      tooltip: { trigger: "axis" },
      series: [
        {
          type: "line",
          name,
          data,
        },
      ],
    },
  };
}

test("echarts artifact updates chart when a dimension filter is applied", async ({
  page,
}) => {
  const threadId = "thread-dimension-filter-echarts";
  const title = "Revenue by Region";
  const rows = [
    { month: "2026-01", region: "US", revenue: 100 },
    { month: "2026-02", region: "US", revenue: 130 },
    { month: "2026-01", region: "EU", revenue: 90 },
    { month: "2026-02", region: "EU", revenue: 95 },
  ];

  const toolCall = {
    id: "tc-dimension-filter-echarts",
    name: "display_table",
    args: {
      title,
      data_json: JSON.stringify(rows),
      echarts_spec_version: "ec-cap-v1-test",
      echarts_spec: buildSingleSeriesEchartsSpec("Revenue", [190, 225]),
    },
  };

  await installMockApi(page, {
    threadId,
    messages: [
      {
        id: "msg-assistant-1",
        seq: 1,
        role: "assistant",
        content: "Rendering revenue chart.",
        tool_calls: [toolCall],
      },
    ],
  });

  await page.goto("/");

  await expect(page.getByText(title).first()).toBeVisible();
  await page.getByText(title).first().click();

  const chart = page.getByTestId("echarts-visualization");
  await expect(chart).toBeVisible();
  await expect.poll(() => readEchartsCanvasSignature(page)).not.toBe("");
  const beforeFilterSignature = await readEchartsCanvasSignature(page);
  await expect(page.getByText("4 records")).toBeVisible();

  await selectDimensionValue(page, "region", "US");

  await expect(page.getByText("2 records")).toBeVisible();
  await expect(page.locator("tbody")).toContainText("US");
  await expect(page.locator("tbody")).not.toContainText("EU");
  await expect.poll(() => readEchartsCanvasSignature(page)).not.toBe(
    beforeFilterSignature,
  );

  const filteredSignature = await readEchartsCanvasSignature(page);

  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(page.getByText("4 records")).toBeVisible();
  await expect(page.locator("tbody")).toContainText("EU");
  await expect.poll(() => readEchartsCanvasSignature(page)).not.toBe(
    filteredSignature,
  );
});

test("partial dataset filter calls /filter-chart and updates echarts output", async ({
  page,
}) => {
  const threadId = "thread-dimension-filter-partial";
  const title = "Partial Revenue by Region";
  const artifactId = "art-filter-preview";
  const baseSpec = buildSingleSeriesEchartsSpec("Revenue", [190, 225]);
  const usOnlySpec = buildSingleSeriesEchartsSpec("Revenue", [100, 130]);
  const previewRows = [
    { month: "2026-01", region: "US", revenue: 100 },
    { month: "2026-01", region: "EU", revenue: 90 },
  ];
  const fullRows = [
    { month: "2026-01", region: "US", revenue: 100 },
    { month: "2026-02", region: "US", revenue: 130 },
    { month: "2026-01", region: "EU", revenue: 90 },
    { month: "2026-02", region: "EU", revenue: 95 },
  ];

  let rowsRequests = 0;
  let filterChartRequests = 0;
  let lastFilterBody: any = null;

  const envelope = {
    artifact_id: artifactId,
    tool_name: "display_table",
    tool_call_id: "tc-filter-preview",
    ok: true,
    result_format: "json",
    result: {
      rows: previewRows,
      preview_only: true,
      full_artifact_id: artifactId,
      row_count_total: fullRows.length,
      chart_engine: "echarts",
      chart_spec_version: "ec-cap-v1-test",
      plot_spec: baseSpec,
    },
    preview_only: true,
    full_artifact_id: artifactId,
    row_count_total: fullRows.length,
  };

  await installMockApi(page, {
    threadId,
    messages: [
      {
        id: "msg-assistant-partial-1",
        seq: 1,
        role: "assistant",
        content: "Rendering preview from source artifact.",
        tool_calls: [
          {
            id: "tc-filter-preview",
            name: "display_table",
            args: {
              title,
              source: { artifact_id: "source-artifact-id" },
              echarts_spec_version: "ec-cap-v1-test",
              echarts_spec: baseSpec,
            },
          },
        ],
      },
      {
        id: "msg-tool-partial-1",
        seq: 2,
        role: "tool",
        name: "display_table",
        tool_call_id: "tc-filter-preview",
        content: JSON.stringify(envelope),
      },
    ],
    onRows: (id) => {
      if (id !== artifactId) return null;
      rowsRequests += 1;
      return {
        artifact_id: artifactId,
        rows: fullRows,
        offset: 0,
        limit: 10,
        rows_returned: fullRows.length,
        row_count_total: fullRows.length,
        has_more: false,
      };
    },
    onFilterChart: (id, body) => {
      if (id !== artifactId) return null;
      filterChartRequests += 1;
      lastFilterBody = body;

      const filtered = fullRows.filter((row) => row.region === "US");
      return {
        artifact_id: artifactId,
        rows: filtered,
        offset: Number(body?.offset || 0),
        limit: Number(body?.limit || 10),
        rows_returned: filtered.length,
        row_count_total: filtered.length,
        has_more: false,
        filters_applied: body?.filters || [],
        plot_spec: usOnlySpec,
      };
    },
  });

  await page.goto("/");

  await expect(page.getByText(title).first()).toBeVisible();
  await page.getByText(title).first().click();

  const chart = page.getByTestId("echarts-visualization");
  await expect(chart).toBeVisible();
  await expect.poll(() => readEchartsCanvasSignature(page)).not.toBe("");
  const beforeFilterSignature = await readEchartsCanvasSignature(page);
  await expect(
    page.getByText("Table rows are loaded in pages from the full artifact."),
  ).toBeVisible();
  await expect.poll(() => rowsRequests).toBeGreaterThan(0);

  await selectDimensionValue(page, "region", "US");

  await expect.poll(() => filterChartRequests).toBeGreaterThan(0);
  await expect(page.getByText("2 records")).toBeVisible();
  await expect(page.locator("tbody")).toContainText("US");
  await expect(page.locator("tbody")).not.toContainText("EU");
  await expect.poll(() => readEchartsCanvasSignature(page)).not.toBe(
    beforeFilterSignature,
  );

  expect(Array.isArray(lastFilterBody?.filters)).toBe(true);
  expect(lastFilterBody.filters).toEqual(
    expect.arrayContaining([
      expect.objectContaining({
        column: "region",
        operator: "eq",
        value: "US",
      }),
    ]),
  );
  expect(lastFilterBody?.chart).toEqual(
    expect.objectContaining({
      kind: "line",
      engine: "echarts",
      x: "month",
      y: "revenue",
    }),
  );
});
