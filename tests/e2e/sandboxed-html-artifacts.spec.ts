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

type MockApiOptions = {
  threadId?: string;
  messages: any[];
  threadArtifacts?: any[];
  artifactsById?: Record<string, any>;
  delayArtifactIds?: string[];
  onArtifactRows?: (artifactId: string, url: URL) => any | null;
};

type MockApiState = {
  artifactRequests: string[];
  rowRequests: URL[];
  releaseArtifact: (artifactId: string) => void;
};

async function installMockApi(
  page: Page,
  options: MockApiOptions,
): Promise<MockApiState> {
  const threadId = options.threadId || "thread-sandboxed-html";
  const artifactRequests: string[] = [];
  const rowRequests: URL[] = [];
  const delayedArtifactIds = new Set(options.delayArtifactIds || []);
  const artifactReleaseById = new Map<string, () => void>();
  const artifactGateById = new Map<string, Promise<void>>();
  const now = Date.now();
  const thread = {
    id: threadId,
    title: `Thread ${threadId}`,
    created_at: now - 1000,
    updated_at: now - 1000,
  };

  for (const artifactId of delayedArtifactIds) {
    artifactGateById.set(
      artifactId,
      new Promise<void>((resolve) => {
        artifactReleaseById.set(artifactId, resolve);
      }),
    );
  }

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
          messages: options.messages,
          artifacts: options.threadArtifacts || [],
        }),
      );
      return;
    }

    if (
      pathname.startsWith("/api/artifacts/") &&
      pathname.endsWith("/rows") &&
      method === "GET"
    ) {
      const artifactId = decodeURIComponent(pathname.split("/")[3] || "");
      rowRequests.push(url);
      const payload = options.onArtifactRows?.(artifactId, url);
      await route.fulfill(
        payload
          ? jsonResponse(payload)
          : jsonResponse({ error: "artifact rows not found" }, 404),
      );
      return;
    }

    const artifactMatch = pathname.match(/^\/api\/artifacts\/([^/]+)$/);
    if (artifactMatch && method === "GET") {
      const artifactId = decodeURIComponent(artifactMatch[1] || "");
      artifactRequests.push(artifactId);
      const gate = artifactGateById.get(artifactId);
      if (gate) await gate;
      const artifact = options.artifactsById?.[artifactId];
      await route.fulfill(
        artifact
          ? jsonResponse({ artifact })
          : jsonResponse({ error: "artifact not found" }, 404),
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

  return {
    artifactRequests,
    rowRequests,
    releaseArtifact: (artifactId: string) => {
      artifactReleaseById.get(artifactId)?.();
    },
  };
}

function toolResultEnvelope(overrides: Record<string, any>) {
  return JSON.stringify({
    artifact_id: "html-inline",
    tool_call_id: "call-inline",
    tool_name: "create_interactive_data_artifact",
    ok: true,
    result_format: "json",
    result: {
      title: "Inline Data HTML",
      artifact_type: "interactive_data_html",
      render_mode: "sandboxed_html",
      byte_size: 2048,
      data_source_artifact_id: "source-data",
      data_source_title: "Source Rows",
      data_source_columns: ["name", "value"],
      data_source_row_count: 12,
      data_access: {
        mode: "parent_bridge",
        max_page_size: 1000,
      },
      ...overrides,
    },
  });
}

function buildInteractiveThreadMessages() {
  return [
    {
      id: "msg-assistant-inline",
      seq: 1,
      role: "assistant",
      content: "Here is the interactive artifact.",
      tool_calls: [
        {
          id: "call-inline",
          name: "create_interactive_data_artifact",
          provider_executed: true,
          args: {},
        },
      ],
    },
    {
      id: "msg-tool-inline",
      seq: 2,
      role: "tool",
      name: "create_interactive_data_artifact",
      tool_call_id: "call-inline",
      content: toolResultEnvelope({}),
    },
  ];
}

test("inline sandboxed HTML artifacts hydrate into a hardened iframe and report runtime errors", async ({
  page,
}) => {
  const requestedColumns = [
    " name ",
    "",
    ...Array.from({ length: 103 }, (_, index) => `c${index + 2}`),
  ];
  const html = `
    <main>
      <div id="status">Booting</div>
      <script>
        (async function () {
          var metadata = window.sandboxData.getMetadata();
          var payload = await window.sandboxData.getRows({
            offset: -25,
            limit: 9999,
            columns: ${JSON.stringify(requestedColumns)}
          });
          document.getElementById("status").textContent =
            metadata.title + " rows=" + payload.rows.length +
            " limit=" + payload.limit +
            " columns=" + payload.columns.length;
          setTimeout(function () {
            throw new Error("inline sandbox boom");
          }, 0);
        })();
      </script>
    </main>
  `;
  const api = await installMockApi(page, {
    messages: buildInteractiveThreadMessages(),
    threadArtifacts: [
      {
        id: "html-inline",
        type: "code",
        title: "Inline Data HTML",
        content: "",
        language: "html",
        metadata: {
          tool_name: "create_interactive_data_artifact",
          render_mode: "sandboxed_html",
          artifact_kind: "interactive_data_html",
          sandbox: "allow-scripts",
          network: "blocked",
          placeholder: false,
          partial: true,
          data_source_artifact_id: "source-data",
          data_source_title: "Source Rows",
          data_source_columns: ["name", "value"],
          data_source_row_count: 12,
          data_access: {
            mode: "parent_bridge",
            max_page_size: 1000,
          },
        },
      },
    ],
    delayArtifactIds: ["html-inline"],
    artifactsById: {
      "html-inline": {
        id: "html-inline",
        type: "code",
        title: "Inline Data HTML",
        content: html,
        language: "html",
        metadata: {
          tool_name: "create_interactive_data_artifact",
          render_mode: "sandboxed_html",
          artifact_kind: "interactive_data_html",
          sandbox: "allow-scripts",
          network: "blocked",
          placeholder: false,
          data_source_artifact_id: "source-data",
          data_source_title: "Source Rows",
          data_source_columns: ["name", "value"],
          data_source_row_count: 12,
          data_access: {
            mode: "parent_bridge",
            max_page_size: 1000,
          },
        },
      },
    },
    onArtifactRows: (_artifactId, url) => {
      const limit = Number(url.searchParams.get("limit"));
      const columns = (url.searchParams.get("columns") || "")
        .split(",")
        .filter(Boolean);
      return {
        rows: [{ name: "alpha", value: 1 }],
        limit,
        columns,
      };
    },
  });

  await page.goto("/");

  await expect(page.getByTestId("inline-html-preview-loading")).toBeVisible();
  await expect
    .poll(() => api.artifactRequests)
    .toContainEqual("html-inline");
  api.releaseArtifact("html-inline");

  const iframe = page.locator('iframe[title="Inline Data HTML"]');
  await expect(iframe).toBeVisible();
  await expect(iframe).toHaveAttribute("sandbox", "allow-scripts");
  await expect(iframe).toHaveAttribute("referrerpolicy", "no-referrer");

  const srcDoc = await iframe.getAttribute("srcdoc");
  expect(srcDoc).toContain('http-equiv="Content-Security-Policy"');
  expect(srcDoc).toContain("default-src 'none'");
  expect(srcDoc).toContain("connect-src 'none'");
  expect(srcDoc).toContain("base-uri 'none'");

  await expect(
    page
      .frameLocator('iframe[title="Inline Data HTML"]')
      .locator("#status"),
  ).toHaveText("Source Rows rows=1 limit=500 columns=1");

  await expect.poll(() => api.rowRequests.length).toBe(1);
  const rowRequest = api.rowRequests[0];
  expect(rowRequest.pathname).toBe("/api/artifacts/source-data/rows");
  expect(rowRequest.searchParams.get("offset")).toBe("0");
  expect(rowRequest.searchParams.get("limit")).toBe("500");
  const forwardedColumns = (rowRequest.searchParams.get("columns") || "").split(
    ",",
  );
  expect(forwardedColumns).toHaveLength(1);
  expect(forwardedColumns[0]).toBe("name");

  await expect(page.getByText("Sandbox runtime error")).toBeVisible();
  await expect(page.getByText(/inline sandbox boom/)).toBeVisible();
});

test("inline sandboxed HTML artifacts are promoted out of compact intermediate tool steps", async ({
  page,
}) => {
  await installMockApi(page, {
    messages: [
      {
        id: "msg-assistant-tool-only",
        seq: 1,
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: "call-inline",
            name: "create_interactive_data_artifact",
            provider_executed: true,
            args: {},
          },
        ],
      },
      {
        id: "msg-tool-inline",
        seq: 2,
        role: "tool",
        name: "create_interactive_data_artifact",
        tool_call_id: "call-inline",
        content: toolResultEnvelope({}),
      },
      {
        id: "msg-final",
        seq: 3,
        role: "assistant",
        content: "Created the widget.",
      },
    ],
    threadArtifacts: [
      {
        id: "html-inline",
        type: "code",
        title: "Inline Data HTML",
        content: "<main><h1>Current Time</h1><div id=\"clock\">12:34:56</div></main>",
        language: "html",
        metadata: {
          tool_name: "create_interactive_data_artifact",
          render_mode: "sandboxed_html",
          artifact_kind: "interactive_data_html",
          sandbox: "allow-scripts",
          network: "blocked",
          inline_preview: true,
        },
      },
    ],
  });

  await page.goto("/");

  const inlineCard = page.getByTestId("sandboxed-html-artifact-card");
  await expect(inlineCard).toBeVisible();
  await expect(page.getByTestId("sandboxed-html-iframe")).toBeVisible();
  await expect(page.getByText("Created the widget.")).toBeVisible();

  await page.getByRole("button", { name: "Ran 1 tool succeeded" }).click();
  const inlinePreviewToggle = page.getByLabel("Toggle inline preview");
  await expect(inlinePreviewToggle).toHaveCount(1);
  await inlinePreviewToggle.click();
  const inlinePreview = page.locator('[id$="-preview-0"]');
  await expect(inlinePreview.getByText("Interactive data HTML")).toBeVisible();
  await expect(page.getByText("Shown as the artifact card below")).toBeVisible();
  await expect(page.locator("pre").filter({ hasText: '"artifact_type"' })).toHaveCount(0);
});

test("inline HTML previews require explicit sandboxed HTML tool result eligibility", async ({
  page,
}) => {
  await installMockApi(page, {
    messages: [
      {
        id: "msg-assistant-ineligible",
        seq: 1,
        role: "assistant",
        content: "",
        tool_calls: [
          {
            id: "call-ineligible",
            name: "create_interactive_artifact",
            provider_executed: true,
            args: {},
          },
        ],
      },
      {
        id: "msg-tool-ineligible",
        seq: 2,
        role: "tool",
        name: "create_interactive_artifact",
        tool_call_id: "call-ineligible",
        content: JSON.stringify({
          artifact_id: "html-ineligible",
          tool_call_id: "call-ineligible",
          tool_name: "create_interactive_artifact",
          ok: true,
          result_format: "json",
          result: {
            title: "Ineligible Inline HTML",
            artifact_type: "interactive_html",
            html: "<p>Should not render inline</p>",
          },
        }),
      },
    ],
  });

  await page.goto("/");

  await expect(page.getByText("Loading HTML preview")).toHaveCount(0);
  await expect(
    page.locator('iframe[title="Ineligible Inline HTML"]'),
  ).toHaveCount(0);
});
