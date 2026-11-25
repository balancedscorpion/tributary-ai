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
    chartDimensionFilters: false,
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

async function installMockApi(
  page: Page,
  threadId: string,
  toolCall: any,
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
      await route.fulfill(
        jsonResponse({
          thread,
          messages: [
            {
              id: `msg-${threadId}`,
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

async function installMockApiWithMessages(
  page: Page,
  threadId: string,
  messages: any[],
  onArtifactsRows?: (artifactId: string, url: URL) => any | null,
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
      const payload = onArtifactsRows?.(artifactId, url);
      if (payload) {
        await route.fulfill(jsonResponse(payload));
      } else {
        await route.fulfill(
          jsonResponse(
            { error: "artifact rows not found" },
            404,
          ),
        );
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

function buildSpecForSeriesType(seriesType: string) {
  const base = {
    schema_version: "echarts-plot-spec.v1",
    module: "all",
    options: {
      title: { text: `ECharts ${seriesType}` },
      tooltip: { trigger: "axis", formatter_id: "echarts_tooltip_compact" },
      series: [
        {
          type: seriesType,
          data: [12, 9],
        },
      ],
    } as Record<string, any>,
  };

  switch (seriesType) {
    case "line":
    case "bar":
    case "scatter":
    case "candlestick":
    case "boxplot":
    case "effectScatter":
    case "pictorialBar":
      base.options.xAxis = { type: "category", data: ["2026-01", "2026-02"] };
      base.options.yAxis = { type: "value" };
      return base;
    case "pie":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "pie",
          data: [
            { name: "A", value: 12 },
            { name: "B", value: 9 },
          ],
        },
      ];
      return base;
    case "radar":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.radar = {
        indicator: [
          { name: "Q1", max: 20 },
          { name: "Q2", max: 20 },
          { name: "Q3", max: 20 },
        ],
      };
      base.options.series = [
        {
          type: "radar",
          data: [{ value: [12, 9, 14], name: "A" }],
        },
      ];
      return base;
    case "funnel":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "funnel",
          data: [
            { name: "A", value: 12 },
            { name: "B", value: 9 },
          ],
        },
      ];
      return base;
    case "gauge":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "gauge",
          data: [{ value: 62, name: "Score" }],
        },
      ];
      return base;
    case "heatmap":
      base.options.xAxis = { type: "category", data: ["Mon", "Tue"] };
      base.options.yAxis = { type: "category", data: ["A", "B"] };
      base.options.visualMap = { min: 0, max: 20, show: false };
      base.options.series = [
        {
          type: "heatmap",
          data: [
            [0, 0, 12],
            [1, 1, 9],
          ],
        },
      ];
      return base;
    case "treemap":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "treemap",
          data: [
            { name: "A", value: 12 },
            { name: "B", value: 9 },
          ],
        },
      ];
      return base;
    case "sunburst":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "sunburst",
          data: [
            { name: "A", value: 12 },
            { name: "B", value: 9 },
          ],
        },
      ];
      return base;
    case "sankey":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "sankey",
          data: [{ name: "A" }, { name: "B" }],
          links: [{ source: "A", target: "B", value: 9 }],
        },
      ];
      return base;
    case "parallel":
      base.options.parallelAxis = [
        { dim: 0, name: "A" },
        { dim: 1, name: "B" },
      ];
      base.options.parallel = { left: 40, right: 30 };
      base.options.series = [
        {
          type: "parallel",
          data: [
            [12, 9],
            [9, 12],
          ],
        },
      ];
      return base;
    case "themeRiver":
      base.options.singleAxis = { type: "time" };
      base.options.series = [
        {
          type: "themeRiver",
          data: [
            ["2026-01-01", 12, "A"],
            ["2026-02-01", 9, "B"],
          ],
        },
      ];
      return base;
    case "graph":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "graph",
          nodes: [
            { id: "n1", name: "A" },
            { id: "n2", name: "B" },
          ],
          links: [{ source: "n1", target: "n2" }],
        },
      ];
      return base;
    case "tree":
      base.options.tooltip = {
        trigger: "item",
        formatter_id: "echarts_tooltip_compact",
      };
      base.options.series = [
        {
          type: "tree",
          data: [{ name: "root", children: [{ name: "child" }] }],
        },
      ];
      return base;
    case "lines":
      base.options.xAxis = { type: "value" };
      base.options.yAxis = { type: "value" };
      base.options.series = [
        {
          type: "lines",
          coordinateSystem: "cartesian2d",
          data: [
            {
              coords: [
                [0, 0],
                [1, 1],
              ],
            },
          ],
        },
      ];
      return base;
    case "custom":
      base.options.xAxis = { type: "value" };
      base.options.yAxis = { type: "value" };
      base.options.series = [
        {
          type: "custom",
          render_item_id: "custom_render_rect",
          data: [
            [0, 12],
            [1, 9],
          ],
        },
      ];
      return base;
    default:
      return base;
  }
}

const SERIES_FAMILIES = [
  "line",
  "bar",
  "scatter",
  "pie",
  "radar",
  "funnel",
  "gauge",
  "candlestick",
  "boxplot",
  "heatmap",
  "treemap",
  "sunburst",
  "sankey",
  "parallel",
  "themeRiver",
  "graph",
  "tree",
  "effectScatter",
  "lines",
  "pictorialBar",
  "custom",
];

for (const seriesType of SERIES_FAMILIES) {
  test(`Artifact panel renders ECharts ${seriesType} spec`, async ({
    page,
  }) => {
    const title = `ECharts ${seriesType} Render`;
    const threadID = `thread-echarts-${seriesType.toLowerCase()}`;
    const toolCall = {
      id: `tc-echarts-${seriesType.toLowerCase()}`,
      name: "display_table",
      args: {
        title,
        data_json: JSON.stringify([
          { category: "A", value: 12 },
          { category: "B", value: 9 },
        ]),
        echarts_spec_version: "ec-cap-v1-test",
        echarts_spec: buildSpecForSeriesType(seriesType),
      },
    };

    await installMockApi(page, threadID, toolCall);
    await page.goto("/");

    await expect(page.getByText(title).first()).toBeVisible();
    await page.getByText(title).first().click();
    await expect(
      page
        .getByTestId("echarts-visualization")
        .or(page.getByTestId("chart-render-fallback"))
        .or(page.getByText("Unable to render ECharts spec.")),
    ).toBeVisible();
  });
}

test("Artifact panel shows chart budget summary metadata for large ECharts artifacts", async ({
  page,
}) => {
  const title = "ECharts Large Budget Summary";
  const threadID = "thread-echarts-budget-summary";
  const toolCall = {
    id: "tc-echarts-budget-summary",
    name: "display_table",
    args: {
      title,
      data_json: JSON.stringify([
        { category: "A", value: 12 },
        { category: "B", value: 9 },
      ]),
      echarts_spec_version: "ec-cap-v1-test",
      echarts_spec: buildSpecForSeriesType("line"),
      chart_budget: {
        schema_version: "chart-budget.v1",
        chart_kind: "line",
        chart_engine: "echarts",
        row_count_total: 100000,
        points_requested: 100000,
        points_returned: 50000,
        budget_actions: [
          "downsample:line_lttb",
          "spec_budget:reduce_max_points",
        ],
      },
    },
  };

  await installMockApi(page, threadID, toolCall);
  await page.goto("/");

  await expect(page.getByText(title).first()).toBeVisible();
  await page.getByText(title).first().click();
  await expect(
    page
      .getByTestId("echarts-visualization")
      .or(page.getByTestId("chart-render-fallback")),
  ).toBeVisible();
  await expect(
    page.getByText("Showing 50,000 points from 100,000 rows."),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Policy actions: downsample:line_lttb, spec_budget:reduce_max_points.",
    ),
  ).toBeVisible();
});

test("Source-backed display_table envelope renders advanced ECharts custom series", async ({
  page,
}) => {
  const threadID = "thread-echarts-source-envelope-custom";
  const title = "Source-backed custom chart";
  const envelope = {
    artifact_id: "art-echarts-source-custom",
    tool_name: "display_table",
    tool_call_id: "tc-echarts-source-custom",
    ok: true,
    result_format: "json",
    result: {
      rows: [
        { x: 0, y: 2 },
        { x: 1, y: 4 },
      ],
      chart_engine: "echarts",
      chart_spec_version: "ec-cap-v1-test",
      plot_spec: {
        schema_version: "echarts-plot-spec.v1",
        module: "all",
        options: {
          xAxis: { type: "value" },
          yAxis: { type: "value" },
          series: [
            {
              type: "custom",
              render_item_id: "custom_render_rect",
              data: [
                [0, 2],
                [1, 4],
              ],
            },
          ],
        },
      },
    },
  };

  await installMockApiWithMessages(page, threadID, [
    {
      id: "msg-assistant-source-custom",
      seq: 1,
      role: "assistant",
      content: "Rendering source-backed custom ECharts plot.",
      tool_calls: [
        {
          id: "tc-echarts-source-custom",
          name: "display_table",
          args: {
            title,
            source: { artifact_id: "art-source-dataset-custom" },
            echarts_spec_version: "ec-cap-v1-test",
            echarts_spec: {
              schema_version: "echarts-plot-spec.v1",
              module: "all",
              options: {
                xAxis: { type: "value" },
                yAxis: { type: "value" },
                series: [
                  {
                    type: "custom",
                    render_item_id: "custom_render_rect",
                    data: [
                      [0, 2],
                      [1, 4],
                    ],
                  },
                ],
              },
            },
          },
        },
      ],
    },
    {
      id: "msg-tool-source-custom",
      seq: 2,
      role: "tool",
      name: "display_table",
      tool_call_id: "tc-echarts-source-custom",
      content: JSON.stringify(envelope),
    },
  ]);
  await page.goto("/");

  await expect(page.getByText(title).first()).toBeVisible();
  await page.getByText(title).first().click();
  await expect(
    page
      .getByTestId("echarts-visualization")
      .or(page.getByTestId("chart-render-fallback"))
      .or(page.getByText("Unable to render ECharts spec.")),
  ).toBeVisible();
});

test("Source-backed preview envelope uses full artifact paging metadata", async ({
  page,
}) => {
  const threadID = "thread-echarts-source-preview";
  const title = "Source-backed preview artifact card";
  let rowsRequests = 0;
  const envelope = {
    artifact_id: "art-echarts-source-preview",
    tool_name: "display_table",
    tool_call_id: "tc-echarts-source-preview",
    ok: true,
    result_format: "json",
    result: {
      rows: [
        { category: "c-001", value: 1 },
        { category: "c-002", value: 2 },
      ],
      preview_only: true,
      full_artifact_id: "art-echarts-source-preview",
      row_count_total: 450,
      chart_engine: "echarts",
      chart_spec_version: "ec-cap-v1-test",
      plot_spec: {
        schema_version: "echarts-plot-spec.v1",
        module: "core",
        options: {
          xAxis: { type: "category", data: ["c-001", "c-002"] },
          yAxis: { type: "value" },
          tooltip: { formatter_id: "echarts_tooltip_compact" },
          series: [
            {
              type: "bar",
              name: "Value",
              data: [1, 2],
            },
          ],
        },
      },
      chart_budget: {
        schema_version: "chart-budget.v1",
        chart_kind: "bar",
        chart_engine: "echarts",
        row_count_total: 450,
        points_requested: 450,
        points_returned: 200,
        budget_actions: ["preview:source_rows"],
      },
    },
    preview_only: true,
    full_artifact_id: "art-echarts-source-preview",
    row_count_total: 450,
  };

  await installMockApiWithMessages(
    page,
    threadID,
    [
      {
        id: "msg-assistant-source-preview",
        seq: 1,
        role: "assistant",
        content: "Rendering chart from source artifact.",
        tool_calls: [
          {
            id: "tc-echarts-source-preview",
            name: "display_table",
            args: {
              title,
              source: { artifact_id: "art-source-dataset-preview" },
              echarts_spec_version: "ec-cap-v1-test",
              echarts_spec: {
                schema_version: "echarts-plot-spec.v1",
                module: "core",
                options: {
                  xAxis: { type: "category", data: ["c-001", "c-002"] },
                  yAxis: { type: "value" },
                  tooltip: { formatter_id: "echarts_tooltip_compact" },
                  series: [
                    {
                      type: "bar",
                      name: "Value",
                      data: [1, 2],
                    },
                  ],
                },
              },
            },
          },
        ],
      },
      {
        id: "msg-tool-source-preview",
        seq: 2,
        role: "tool",
        name: "display_table",
        tool_call_id: "tc-echarts-source-preview",
        content: JSON.stringify(envelope),
      },
    ],
    (artifactId) => {
      if (artifactId !== "art-echarts-source-preview") return null;
      rowsRequests += 1;
      return {
        artifact_id: artifactId,
        rows: [
          { category: "c-001", value: 1 },
          { category: "c-002", value: 2 },
          { category: "c-003", value: 3 },
        ],
        offset: 0,
        limit: 10,
        rows_returned: 3,
        row_count_total: 450,
        has_more: true,
        response_size: 128,
      };
    },
  );
  await page.goto("/");

  await expect(page.getByText(title).first()).toBeVisible();
  await page.getByText(title).first().click();
  await expect(
    page.getByText("Table rows are loaded in pages from the full artifact."),
  ).toBeVisible();
  await expect(
    page
      .getByTestId("echarts-visualization")
      .or(page.getByTestId("chart-render-fallback")),
  ).toBeVisible();
  await expect(page.getByText("c-003").first()).toBeVisible();
  expect(rowsRequests).toBeGreaterThan(0);
});
