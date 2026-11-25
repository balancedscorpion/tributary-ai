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

async function installMockApi(
  page: Page,
  threadId = "thread-highcharts",
  toolCallOverride?: any,
): Promise<void> {
  const now = Date.now();
  const thread = {
    id: threadId,
    title: `Thread ${threadId}`,
    created_at: now - 1000,
    updated_at: now - 1000,
  };

  const toolCall = toolCallOverride || {
    id: "tc-highcharts-1",
    name: "display_table",
    args: {
      title: "Revenue Trend",
      data_json: JSON.stringify([
        { month: "2026-01", revenue: 100 },
        { month: "2026-02", revenue: 130 },
        { month: "2026-03", revenue: 125 },
      ]),
      chart_spec_version: "hc-cap-v1-5f4133a00aff",
      chart_spec: {
        schema_version: "highcharts-plot-spec.v1",
        module: "core",
        options: {
          chart: { type: "line" },
          title: { text: "Revenue Trend" },
          tooltip: { formatter_id: "tooltip_currency_usd" },
          xAxis: { categories: ["2026-01", "2026-02", "2026-03"] },
          series: [
            {
              type: "line",
              name: "Revenue",
              data: [100, 130, 125],
            },
          ],
        },
      },
    },
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
      await route.fulfill(
        jsonResponse({
          thread,
          messages: [
            {
              id: "msg-assistant-1",
              seq: 1,
              role: "assistant",
              content: "",
              tool_calls: [toolCall],
            },
          ],
        }),
      );
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

test("Artifact panel renders highcharts for display_table chart_spec artifacts", async ({
  page,
}) => {
  await installMockApi(page);
  await page.goto("/");

  await expect(page.getByText("Revenue Trend").first()).toBeVisible();
  await page.getByText("Revenue Trend").first().click();

  const chart = page.getByTestId("highcharts-visualization");
  await expect(chart).toBeVisible();
});

test("Artifact panel shows deterministic fallback when frontend chart render failure metadata is present", async ({
  page,
}) => {
  const threadId = "thread-highcharts-render-failure";
  const toolCall = {
    id: "tc-highcharts-render-failure",
    name: "display_table",
    args: {
      title: "Highcharts Render Failure",
      data_json: JSON.stringify([
        { month: "2026-01", revenue: 100 },
        { month: "2026-02", revenue: 130 },
      ]),
      chart_spec_version: "hc-cap-v1-5f4133a00aff",
      chart_budget: {
        schema_version: "chart-budget.v1",
        chart_kind: "line",
        chart_engine: "highcharts",
        row_count_total: 100000,
        points_requested: 100000,
        points_returned: 50000,
        failure_stage: "frontend_render",
        failure_code: "RENDERER_EXCEPTION",
        failure_message: "Renderer exceeded browser memory budget.",
      },
      chart_spec: {
        schema_version: "highcharts-plot-spec.v1",
        module: "core",
        options: {
          chart: { type: "line" },
          title: { text: "Highcharts Render Failure" },
          tooltip: { formatter_id: "tooltip_currency_usd" },
          xAxis: { categories: ["2026-01", "2026-02"] },
          series: [
            {
              type: "line",
              name: "Revenue",
              data: [100, 130],
            },
          ],
        },
      },
    },
  };

  await installMockApi(page, threadId, toolCall);
  await page.goto("/");

  await expect(
    page.getByText("Highcharts Render Failure").first(),
  ).toBeVisible();
  await page.getByText("Highcharts Render Failure").first().click();

  const fallback = page.getByTestId("chart-render-fallback");
  await expect(fallback).toBeVisible();
  await expect(fallback).toContainText("Chart rendering unavailable");
  await expect(fallback).toContainText("frontend_render / RENDERER_EXCEPTION");
});
