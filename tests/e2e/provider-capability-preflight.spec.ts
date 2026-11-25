import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * Chat-side capability UX (plan §4.4–§4.6): preflight send protocol, the
 * consolidated sheet, plan rows, plan_update SSE, and the composer target pill.
 * All §5 endpoints are mocked; the UI must faithfully submit the chosen plan.
 */

const THREAD_ID = "thread-preflight";

function jsonResponse(body: unknown, status = 200) {
  return {
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  };
}

const settingsPayload = {
  settings: {
    provider: { providerId: "bedrock", model: "anthropic.claude-opus-4-6-v1" },
    compatibleProfiles: [],
    featureFlags: {
      providers: {
        openai: false,
        anthropic: true,
        gemini: false,
        bedrock: true,
        compatible: true,
      },
    },
    providerConfigs: [
      {
        id: "cfg-bedrock",
        displayName: "Bedrock Claude",
        providerId: "bedrock",
        model: "anthropic.claude-opus-4-6-v1",
        apiType: "messages",
        region: "us-east-1",
        authType: "api_key",
        isDefault: true,
      },
      {
        id: "cfg-anthropic",
        displayName: "Work Anthropic",
        providerId: "anthropic",
        model: "claude-opus-4-6",
        authType: "api_key",
      },
    ],
    activeProviderConfigId: "cfg-bedrock",
  },
};

function planIssue(
  issueId: string,
  code: string,
  text: string,
  extra: Record<string, unknown> = {},
) {
  return {
    issueId,
    code,
    text,
    feature: null,
    attachmentId: null,
    fix: null,
    ...extra,
  };
}

function preflightResult(
  status: string,
  overrides: Record<string, unknown> = {},
) {
  return {
    planId: `plan-${Math.random().toString(36).slice(2, 8)}`,
    requirementsHash: "hash-1",
    status,
    blockers: [],
    choices: [],
    notices: [],
    staleEvidence: false,
    plan: {
      target: {},
      fingerprint: "fp",
      catalogVersion: "v",
      generatedAt: "",
      features: {},
    },
    ...overrides,
  };
}

interface Captured {
  preflightBodies: any[];
  runBodies: any[];
  targetPuts: any[];
  settingsPuts: any[];
  /** GET /api/secrets/status count — in chat, only preflight auth fetches it. */
  secretsGets: number;
  /** GET /api/runs/{id}/plan count. */
  planGets: number;
}

async function setupApp(
  page: Page,
  opts: {
    preflightQueue?: Array<Record<string, unknown>>;
    runResponses?: Array<{ status: number; body: unknown }>;
    sseBody?: string | null;
    /** Optional settings endpoint payload. */
    settingsResponse?: Record<string, unknown>;
    /** Secrets-store contents; dynamic compatible keys may be absent. */
    secretsPresent?: Record<string, boolean>;
    /** Per-request secrets responses; the last entry repeats once drained. */
    secretsPresentQueue?: Array<Record<string, boolean>>;
    /** Latency added to each preflight response (double-send race window). */
    preflightDelayMs?: number;
    /** Persisted plan served (wrapped) from GET /api/runs/{runId}/plan. */
    runPlan?: Record<string, unknown>;
    /** Per-run persisted plans for historical transcript hydration. */
    runPlans?: Record<string, Record<string, unknown>>;
    /** thread.last_run_id in the thread DTO (persisted-plan hydration). */
    threadLastRunId?: string;
    /** Persisted thread messages. */
    threadMessages?: Array<Record<string, unknown>>;
    /** Persisted per-thread target returned by GET /api/threads/{id}. */
    threadLastTarget?: { connectionId: string; model: string };
  },
): Promise<Captured> {
  const captured: Captured = {
    preflightBodies: [],
    runBodies: [],
    targetPuts: [],
    settingsPuts: [],
    secretsGets: 0,
    planGets: 0,
  };
  const preflightQueue = [...(opts.preflightQueue || [])];
  const runResponses = [...(opts.runResponses || [])];
  const secretsQueue = [...(opts.secretsPresentQueue || [])];

  await page.addInitScript(
    ([threadId]) => {
      window.localStorage.setItem("ai_analyst_active_thread_id_v1", threadId);
    },
    [THREAD_ID],
  );

  await page.route("**/api/**", async (route: Route) => {
    const request = route.request();
    const url = new URL(request.url());
    const pathname = url.pathname;
    const method = request.method();
    const postJson = () => {
      try {
        return JSON.parse(request.postData() || "{}");
      } catch {
        return {};
      }
    };

    if (pathname === "/api/settings" && method === "GET") {
      await route.fulfill(
        jsonResponse(opts.settingsResponse || settingsPayload),
      );
      return;
    }
    if (
      pathname === "/api/settings" &&
      (method === "PUT" || method === "POST")
    ) {
      const body = postJson();
      captured.settingsPuts.push(body);
      // Echo the stored settings like the real backend so in-test edits
      // (e.g. deleting a connection) actually take effect client-side.
      await route.fulfill(jsonResponse({ settings: body }));
      return;
    }
    if (pathname === "/api/activation/status") {
      await route.fulfill(jsonResponse({ activated: true }));
      return;
    }
    if (pathname === "/api/secrets/status") {
      captured.secretsGets += 1;
      const present =
        secretsQueue.length > 0
          ? secretsQueue.length > 1
            ? secretsQueue.shift()!
            : secretsQueue[0]
          : opts.secretsPresent || {};
      await route.fulfill(
        jsonResponse({
          initialized: true,
          locked: false,
          configured: {},
          present,
        }),
      );
      return;
    }
    if (/^\/api\/runs\/[^/]+\/plan$/.test(pathname) && method === "GET") {
      captured.planGets += 1;
      const runId = pathname.split("/")[3] || "";
      const plan = opts.runPlans?.[runId] || opts.runPlan;
      if (plan) {
        await route.fulfill(jsonResponse({ plan }));
      } else {
        await route.fulfill(jsonResponse({ error: "not found" }, 404));
      }
      return;
    }
    if (pathname === "/api/threads" && method === "GET") {
      await route.fulfill(
        jsonResponse({
          threads: [
            { id: THREAD_ID, title: "Preflight chat", updated_at: Date.now() },
          ],
        }),
      );
      return;
    }
    if (pathname === `/api/threads/${THREAD_ID}` && method === "GET") {
      await route.fulfill(
        jsonResponse({
          thread: {
            id: THREAD_ID,
            title: "Preflight chat",
            ...(opts.threadLastRunId
              ? { last_run_id: opts.threadLastRunId }
              : {}),
            ...(opts.threadLastTarget
              ? { lastTarget: opts.threadLastTarget }
              : {}),
          },
          messages: opts.threadMessages || [],
          artifacts: [],
        }),
      );
      return;
    }
    if (
      pathname === `/api/threads/${THREAD_ID}/runs/preflight` &&
      method === "POST"
    ) {
      captured.preflightBodies.push(postJson());
      if (opts.preflightDelayMs) {
        await new Promise((r) => setTimeout(r, opts.preflightDelayMs));
      }
      const next =
        preflightQueue.length > 1 ? preflightQueue.shift() : preflightQueue[0];
      if (!next) {
        await route.fulfill(jsonResponse({ error: "not found" }, 404));
        return;
      }
      await route.fulfill(jsonResponse(next));
      return;
    }
    if (pathname === `/api/threads/${THREAD_ID}/runs` && method === "POST") {
      captured.runBodies.push(postJson());
      const next =
        runResponses.length > 1 ? runResponses.shift() : runResponses[0];
      if (!next) {
        await route.fulfill(
          jsonResponse({
            run: { id: "run-1", thread_id: THREAD_ID, status: "running" },
          }),
        );
        return;
      }
      await route.fulfill(jsonResponse(next.body, next.status));
      return;
    }
    if (pathname === `/api/threads/${THREAD_ID}/target` && method === "PUT") {
      captured.targetPuts.push(postJson());
      await route.fulfill(jsonResponse({}));
      return;
    }
    if (/^\/api\/runs\/[^/]+\/events$/.test(pathname)) {
      if (opts.sseBody === null) {
        // Hang: keeps the run active for in-flight assertions.
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body:
          opts.sseBody ??
          'event: assistant_final\ndata: {"content":"Done."}\n\nevent: run_end\ndata: {}\n\n',
      });
      return;
    }
    if (/^\/api\/runs\/[^/]+$/.test(pathname) && method === "GET") {
      await route.fulfill(
        jsonResponse({
          run: { id: "run-1", thread_id: THREAD_ID, status: "running" },
        }),
      );
      return;
    }
    if (
      pathname === "/api/providers/capabilities/resolve" ||
      /^\/api\/providers\/connections\/[^/]+\/capabilities$/.test(pathname)
    ) {
      await route.fulfill(jsonResponse({ error: "not found" }, 404));
      return;
    }
    await route.fulfill(jsonResponse({}));
  });

  return captured;
}

async function typeAndSend(page: Page, text: string) {
  const composer = page.locator("#chat-composer-input");
  await composer.fill(text);
  await page.getByRole("button", { name: /Send message/ }).click();
}

test("flag off: legacy send path — no preflight requests, header badge retained, no pill", async ({
  page,
}) => {
  const captured = await setupApp(page, {});
  await page.goto("/");
  await expect(page.locator("#chat-composer-input")).toBeVisible();

  await expect(
    page.getByText("bedrock:anthropic.claude-opus-4-6-v1"),
  ).toBeVisible();
  await expect(page.locator("[data-target-pill]")).toHaveCount(0);

  await typeAndSend(page, "hello legacy");
  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.preflightBodies).toHaveLength(0);
  expect(captured.runBodies[0].planId).toBeUndefined();
});

test("clean preflight: zero new chrome, plan submitted with the run", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-clean" })],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "clean send");

  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.preflightBodies).toHaveLength(1);
  expect(captured.runBodies[0].planId).toBe("plan-clean");
  await expect(page.locator("[data-preflight-notice-row]")).toHaveCount(0);
  await expect(
    page.getByRole("alertdialog", { name: "Before sending" }),
  ).toHaveCount(0);
  await expect(page.locator("[data-run-plan-row]")).toHaveCount(0);
});

test("notices: auto-send without another click, quiet notice + plan row", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("notices", {
        planId: "plan-notice",
        notices: [
          planIssue(
            "n1",
            "surface_switch",
            "Documents switch this run to the Converse API; native compaction is skipped.",
          ),
        ],
      }),
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "notice send");

  await expect.poll(() => captured.runBodies.length).toBe(1);
  await expect(
    page.getByRole("alertdialog", { name: "Before sending" }),
  ).toHaveCount(0);
  await expect(page.locator("[data-preflight-notice-row]")).toContainText(
    "Converse API",
  );
  const planRow = page.locator("[data-run-plan-row]");
  await expect(planRow).toHaveCount(1);
  await planRow.getByRole("button").click();
  await expect(planRow).toContainText("native compaction is skipped");
});

test("two same-code choices stay independently keyed by issueId", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("choices_required", {
        planId: "plan-choices",
        choices: [
          {
            ...planIssue(
              "issue-a",
              "fidelity_reduction",
              "report.docx can’t be sent natively.",
              { attachmentId: "file-a" },
            ),
            options: [
              {
                id: "extract",
                label: "Send as extracted text",
                consequence: "Layout is lost",
                policyClass: "office_docs_to_text",
              },
              {
                id: "skip",
                label: "Leave it out of this message",
                consequence: "",
                policyClass: null,
              },
            ],
            allowedScopes: ["run", "thread", "connection"],
          },
          {
            ...planIssue(
              "issue-b",
              "fidelity_reduction",
              "slides.pptx can’t be sent natively.",
              { attachmentId: "file-b" },
            ),
            options: [
              {
                id: "extract",
                label: "Send as extracted text",
                consequence: "Layout is lost",
                policyClass: "office_docs_to_text",
              },
              {
                id: "skip",
                label: "Leave it out of this message",
                consequence: "",
                policyClass: null,
              },
            ],
            allowedScopes: ["run", "thread"],
          },
        ],
      }),
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "two docs");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();
  await expect(sheet.locator("[data-preflight-choice]")).toHaveCount(2);

  // Change only the second issue's option; scopes stay "This run" by default.
  await sheet
    .locator('[data-preflight-choice="issue-b"]')
    .getByRole("radio", { name: /Leave it out/ })
    .check();
  await expect(
    sheet.locator('[data-preflight-scope="issue-a:run"]'),
  ).toHaveAttribute("aria-checked", "true");
  await sheet.getByRole("button", { name: "Send with choices" }).click();

  await expect.poll(() => captured.runBodies.length).toBe(1);
  const body = captured.runBodies[0];
  expect(body.planId).toBe("plan-choices");
  expect(body.choiceSelections["issue-a"]).toEqual({
    optionId: "extract",
    scope: "run",
  });
  expect(body.choiceSelections["issue-b"]).toEqual({
    optionId: "skip",
    scope: "run",
  });
});

test("blocked: send disabled; remove-attachment fix re-preflights and sends", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("blocked", {
        planId: "plan-blocked",
        blockers: [
          planIssue(
            "b1",
            "hosted_tool_blocked",
            "report.docx requires hosted code execution, which is blocked.",
            {
              attachmentId: "file-a",
              fix: { kind: "remove_attachment" },
            },
          ),
        ],
      }),
      preflightResult("clean", { planId: "plan-after-fix" }),
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "blocked send");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();
  await expect(
    sheet.getByRole("button", { name: "Send with choices" }),
  ).toBeDisabled();
  await sheet.getByRole("button", { name: "Remove attachment" }).click();

  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.preflightBodies).toHaveLength(2);
  expect(captured.runBodies[0].planId).toBe("plan-after-fix");
});

test("cancel keeps the draft and sends nothing", async ({ page }) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("choices_required", {
        planId: "plan-cancel",
        choices: [
          {
            ...planIssue(
              "c1",
              "fidelity_reduction",
              "doc.docx can’t be sent natively.",
              { attachmentId: "file-a" },
            ),
            options: [
              {
                id: "extract",
                label: "Send as extracted text",
                consequence: "",
                policyClass: null,
              },
            ],
            allowedScopes: ["run"],
          },
        ],
      }),
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "keep my draft");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();
  await sheet.getByRole("button", { name: "Cancel" }).click();
  await expect(sheet).toHaveCount(0);

  await expect(page.locator("#chat-composer-input")).toHaveValue(
    "keep my draft",
  );
  expect(captured.runBodies).toHaveLength(0);
});

test("plan_stale: exactly one silent re-preflight, then the run proceeds", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("clean", { planId: "plan-v1" }),
      preflightResult("clean", { planId: "plan-v2" }),
    ],
    runResponses: [
      { status: 409, body: { error: "plan_stale" } },
      {
        status: 200,
        body: { run: { id: "run-2", thread_id: THREAD_ID, status: "running" } },
      },
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "stale plan");

  await expect.poll(() => captured.runBodies.length).toBe(2);
  expect(captured.preflightBodies).toHaveLength(2);
  expect(captured.runBodies[0].planId).toBe("plan-v1");
  expect(captured.runBodies[1].planId).toBe("plan-v2");
  await expect(page.getByText(/Error:/)).toHaveCount(0);
});

test("plan_stale recovery rebuilds the audit row from recovered notices", async ({
  page,
}) => {
  const recoveredNotice = "Recovered plan switches this run to Converse.";
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("clean", { planId: "plan-clean-stale" }),
      preflightResult("notices", {
        planId: "plan-recovered-notice",
        notices: [
          planIssue("notice-recovered", "surface_switch", recoveredNotice),
        ],
      }),
    ],
    runResponses: [
      { status: 409, body: { error: "plan_stale" } },
      {
        status: 200,
        body: {
          run: { id: "run-recovered", thread_id: THREAD_ID, status: "running" },
        },
      },
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "recover with a notice");

  await expect.poll(() => captured.runBodies.length).toBe(2);
  expect(captured.runBodies[1].planId).toBe("plan-recovered-notice");
  const row = page.locator('[data-run-plan-row="run-recovered"]');
  await expect(row).toHaveCount(1);
  await row.getByRole("button").first().click();
  await expect(row).toContainText(recoveredNotice);
});

test("plan_stale recovery applies remove-attachment fixes and re-preflights", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("clean", { planId: "plan-before-stale-fix" }),
      preflightResult("blocked", {
        planId: "plan-stale-blocked",
        blockers: [
          planIssue(
            "stale-blocker",
            "hosted_tool_blocked",
            "This attachment must be removed before sending.",
            {
              attachmentId: "file-a",
              fix: { kind: "remove_attachment" },
            },
          ),
        ],
      }),
      preflightResult("clean", { planId: "plan-after-stale-fix" }),
    ],
    runResponses: [
      { status: 409, body: { error: "plan_stale" } },
      {
        status: 200,
        body: {
          run: { id: "run-after-fix", thread_id: THREAD_ID, status: "running" },
        },
      },
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "recover by removing attachment");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();
  await sheet.getByRole("button", { name: "Remove attachment" }).click();

  await expect.poll(() => captured.runBodies.length).toBe(2);
  expect(captured.preflightBodies).toHaveLength(3);
  expect(captured.runBodies[1].planId).toBe("plan-after-stale-fix");
  await expect(page.getByText(/send plan changed/i)).toHaveCount(0);
});

test("plan_update SSE renders a first-class plan row, not assistant prose", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-live" })],
    sseBody: [
      "event: assistant_start",
      "data: {}",
      "",
      "event: plan_update",
      'data: {"planId":"plan-live","change":{"kind":"surface_switch","feature":null,"attachmentId":null,"text":"Documents switched this run to the Converse API."}}',
      "",
      "event: assistant_final",
      'data: {"content":"All done."}',
      "",
      "event: run_end",
      "data: {}",
      "",
    ].join("\n"),
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "live update");

  await expect.poll(() => captured.runBodies.length).toBe(1);
  const planRow = page.locator("[data-run-plan-row]");
  await expect(planRow).toHaveCount(1);
  await planRow.getByRole("button").first().click();
  await expect(
    page.locator('[data-plan-update-row="surface_switch"]'),
  ).toContainText("Converse API");
  // Server text lives in the plan row, not in an assistant bubble.
  await expect(page.getByText("All done.")).toBeVisible();
});

test("composer pill switches the thread target without touching the global default", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-x" })],
  });
  await page.goto("/?capabilityUx=preflight,composerTarget");

  // Header badge is retired when the pill ships.
  await expect(
    page.getByText("bedrock:anthropic.claude-opus-4-6-v1"),
  ).toHaveCount(0);
  const pill = page.locator("[data-target-pill]");
  await expect(pill).toBeVisible();
  await expect(pill).toContainText("anthropic.claude-opus-4-6-v1");

  await pill.click();
  await page.locator('[data-target-option="cfg-anthropic"]').click();

  await expect.poll(() => captured.targetPuts.length).toBe(1);
  expect(captured.targetPuts[0]).toEqual({
    connectionId: "cfg-anthropic",
    model: "claude-opus-4-6",
  });
  await expect(pill).toContainText("claude-opus-4-6");
  await expect(page.locator("[data-target-continuity-note]")).toContainText(
    "Continuing portably",
  );
  expect(captured.settingsPuts).toHaveLength(0);

  // The switched target rides on the next run.
  await typeAndSend(page, "after switch");
  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.runBodies[0].target).toEqual({
    connectionId: "cfg-anthropic",
    model: "claude-opus-4-6",
  });
});

test("target pill is disabled with a reason while the thread has a run", async ({
  page,
}) => {
  await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-run" })],
    sseBody: null, // stream hangs → run stays active
  });
  await page.goto("/?capabilityUx=preflight,composerTarget");
  await typeAndSend(page, "long running");

  const pill = page.locator("[data-target-pill]");
  await expect(pill).toBeDisabled();
  await pill.locator("..").focus();
  await expect(
    page.getByText(
      "Wait for the current response to finish before switching models.",
    ),
  ).toBeVisible();
});

test("switch-model fix mid-gate: reflight and run submit the switched target, never the captured one", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("blocked", {
        planId: "plan-blocked-target",
        blockers: [
          planIssue(
            "b1",
            "model_unavailable",
            "This model can’t run this message.",
            {
              fix: { kind: "switch_model" },
            },
          ),
        ],
      }),
      preflightResult("clean", { planId: "plan-switched" }),
    ],
    secretsPresent: { bedrock_api_key: true, anthropic_api_key: true },
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "switch target mid-gate");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();
  await sheet.getByRole("button", { name: "Switch model" }).click();
  await sheet.locator('[data-target-option="cfg-anthropic"]').click();

  // PUT target B → reflight → second preflight and the run both carry B.
  await expect.poll(() => captured.targetPuts.length).toBe(1);
  expect(captured.targetPuts[0]).toEqual({
    connectionId: "cfg-anthropic",
    model: "claude-opus-4-6",
  });
  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.preflightBodies).toHaveLength(2);
  expect(captured.preflightBodies[0].target).toEqual({
    connectionId: "cfg-bedrock",
    model: "anthropic.claude-opus-4-6-v1",
  });
  expect(captured.preflightBodies[1].target).toEqual({
    connectionId: "cfg-anthropic",
    model: "claude-opus-4-6",
  });
  expect(captured.runBodies[0].planId).toBe("plan-switched");
  expect(captured.runBodies[0].target).toEqual({
    connectionId: "cfg-anthropic",
    model: "claude-opus-4-6",
  });
  // Exact-target auth: A once, B once after the switch — nothing else, no dupes.
  expect(captured.secretsGets).toBe(2);
});

test("preflight auth validates the selected thread target: default A missing its key does not block target B", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-auth-b" })],
    // Global default (cfg-bedrock) has NO credentials; thread target B does.
    secretsPresent: { bedrock_api_key: false, anthropic_api_key: true },
  });
  await page.goto("/?capabilityUx=preflight,composerTarget");

  await page.locator("[data-target-pill]").click();
  await page.locator('[data-target-option="cfg-anthropic"]').click();
  await expect.poll(() => captured.targetPuts.length).toBe(1);

  await typeAndSend(page, "auth follows the target");
  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.runBodies[0].target).toEqual({
    connectionId: "cfg-anthropic",
    model: "claude-opus-4-6",
  });
  await expect(page.getByText(/Missing API key/)).toHaveCount(0);
});

test("preflight auth blocks on the selected target’s missing key even when the global default has one", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-auth-missing" })],
    secretsPresent: { bedrock_api_key: true, anthropic_api_key: false },
  });
  await page.goto("/?capabilityUx=preflight,composerTarget");

  await page.locator("[data-target-pill]").click();
  await page.locator('[data-target-option="cfg-anthropic"]').click();
  await expect.poll(() => captured.targetPuts.length).toBe(1);

  await typeAndSend(page, "blocked by target auth");
  // The failure names target B — the connection the thread actually runs with.
  await expect(
    page.getByText(/Missing API key for Work Anthropic/),
  ).toBeVisible();
  expect(captured.preflightBodies).toHaveLength(0);
  expect(captured.runBodies).toHaveLength(0);
});

test("saved compatible auth fails closed when its exact secret-status entry is absent", async ({
  page,
}) => {
  const compatibleSettings = {
    settings: {
      ...settingsPayload.settings,
      provider: {
        providerId: "compatible",
        model: "model-new",
        profileId: "cfg-compatible-new",
      },
      providerConfigs: [
        {
          id: "cfg-compatible-new",
          displayName: "New compatible endpoint",
          providerId: "compatible",
          model: "model-new",
          apiType: "chat_completions",
          apiSurface: "openai_chat_completions",
          baseUrl: "https://compatible.invalid/v1",
          authType: "api_key",
          isDefault: true,
        },
      ],
      activeProviderConfigId: "cfg-compatible-new",
      compatibleProfiles: [],
    },
  };
  const captured = await setupApp(page, {
    settingsResponse: compatibleSettings,
    preflightQueue: [
      preflightResult("clean", { planId: "plan-should-not-run" }),
    ],
    // The real secrets endpoint omits never-created dynamic compatible keys.
    secretsPresent: {},
  });
  await page.goto("/?capabilityUx=preflight");

  await typeAndSend(page, "missing exact compatible key");
  await expect(
    page.getByText(/Missing API key for New compatible endpoint/),
  ).toBeVisible();
  expect(captured.preflightBodies).toHaveLength(0);
  expect(captured.runBodies).toHaveLength(0);
  await expect(page.locator("#chat-composer-input")).toHaveValue(
    "missing exact compatible key",
  );
});

test("expanding a run-backed plan row renders the persisted plan DTO (plan report + selection array)", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("notices", {
        planId: "plan-finalized",
        notices: [
          planIssue(
            "n1",
            "surface_switch",
            "Documents switch this run to the Converse API.",
          ),
        ],
      }),
    ],
    runPlan: {
      planId: "plan-finalized",
      requirementsHash: "hash-1",
      status: "notices",
      blockers: [],
      choices: [
        {
          ...planIssue(
            "c1",
            "fidelity_reduction",
            "report.docx can’t be sent natively.",
            { attachmentId: "file-1" },
          ),
          options: [
            {
              id: "extract",
              label: "Send as extracted text",
              consequence: "Layout is lost",
              policyClass: "office_docs_to_text",
            },
          ],
          allowedScopes: ["run", "thread"],
        },
      ],
      notices: [],
      staleEvidence: false,
      plan: {
        target: {
          connectionId: "cfg-bedrock",
          providerFamily: "bedrock",
          apiSurface: "converse_stream",
          endpointFamily: "bedrock-runtime",
          model: "anthropic.claude-opus-4-6-v1",
          region: "us-east-1",
          adapterVersion: "v1",
        },
        fingerprint: "fp",
        catalogVersion: "v1",
        generatedAt: "2026-07-11T00:00:00Z",
        features: {},
      },
      expiresAt: 1893456000000,
      lifecycleStatus: "finalized",
      target: {
        connectionId: "cfg-bedrock",
        model: "anthropic.claude-opus-4-6-v1",
      },
      choiceSelections: [
        {
          issueId: "c1",
          optionId: "extract",
          scope: "thread",
          selectedAt: "2026-07-11T00:00:00Z",
        },
      ],
      createdAt: 1752192000000,
      finalizedAt: 1752192001000,
      runId: "run-1",
    },
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "expand the finalized plan");

  await expect.poll(() => captured.runBodies.length).toBe(1);
  const planRow = page.locator("[data-run-plan-row]");
  await expect(planRow).toHaveCount(1);
  await planRow.getByRole("button").first().click();

  await expect(planRow).toContainText("Finalized plan");
  await expect(planRow).toContainText("plan plan-finalized");
  await expect(planRow).toContainText("converse_stream");
  // Selections resolve through the persisted choices to human labels.
  await expect(planRow).toContainText(
    "report.docx can’t be sent natively. → Send as extracted text (thread)",
  );
});

test("Open Permissions closes the preflight sheet first, keeps the draft, and routes into Settings", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("blocked", {
        planId: "plan-perms",
        blockers: [
          planIssue(
            "b1",
            "permission_required",
            "Hosted web search needs permission before this can be sent.",
            {
              fix: { kind: "open_permissions" },
            },
          ),
        ],
      }),
    ],
  });
  await page.goto("/?capabilityUx=preflight,report");
  await typeAndSend(page, "needs permissions");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();
  await sheet.getByRole("button", { name: "Open Permissions" }).click();

  // No stacked dialogs: the sheet resolves (cancel semantics) before Settings opens.
  await expect(sheet).toHaveCount(0);
  const settings = page.getByRole("dialog", { name: "Settings" });
  await expect(settings).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await expect(page.getByRole("alertdialog")).toHaveCount(0);
  // With the report surface on, it lands on the target connection's controls.
  await expect(
    settings.locator('[data-connection-detail="cfg-bedrock"]'),
  ).toBeVisible();

  // Cancel semantics: nothing was sent and the draft survives.
  expect(captured.runBodies).toHaveLength(0);
  await settings
    .getByRole("button", { name: "Close" })
    .click()
    .catch(() => {});
  await page.keyboard.press("Escape");
  await expect(page.locator("#chat-composer-input")).toHaveValue(
    "needs permissions",
  );
});

test("sheet switch to an unauthenticated target stops before its preflight — no B preflight, no run", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("blocked", {
        planId: "plan-blocked-auth",
        blockers: [
          planIssue(
            "b1",
            "model_unavailable",
            "This model can’t run this message.",
            {
              fix: { kind: "switch_model" },
            },
          ),
        ],
      }),
      preflightResult("clean", { planId: "plan-should-never-happen" }),
    ],
    // A (global default bedrock) authenticates; B (anthropic) has no key.
    secretsPresent: { bedrock_api_key: true, anthropic_api_key: false },
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "switch to missing key");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await sheet.getByRole("button", { name: "Switch model" }).click();
  await sheet.locator('[data-target-option="cfg-anthropic"]').click();
  await expect.poll(() => captured.targetPuts.length).toBe(1);

  // The reflight authenticates B and stops: B is never preflighted or run.
  await expect(
    page.getByText(/Missing API key for Work Anthropic/),
  ).toBeVisible();
  expect(captured.preflightBodies).toHaveLength(1);
  expect(captured.preflightBodies[0].target.connectionId).toBe("cfg-bedrock");
  expect(captured.runBodies).toHaveLength(0);
  await expect(sheet).toHaveCount(0);
  await expect(page.locator("#chat-composer-input")).toHaveValue(
    "switch to missing key",
  );
});

test("a second send during an open negotiation fails closed — the gate resolver is never clobbered", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("choices_required", {
        planId: "plan-locked",
        choices: [
          {
            ...planIssue(
              "c1",
              "fidelity_reduction",
              "doc.docx can’t be sent natively.",
              { attachmentId: "file-a" },
            ),
            options: [
              {
                id: "extract",
                label: "Send as extracted text",
                consequence: "",
                policyClass: null,
              },
            ],
            allowedScopes: ["run"],
          },
        ],
      }),
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "first send");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();

  // Second submit while the first negotiation holds the gate open.
  await page.locator("#chat-composer-input").press("Enter");
  await page.waitForTimeout(300);
  expect(captured.preflightBodies).toHaveLength(1);
  await expect(page.getByRole("alertdialog")).toHaveCount(1);

  // The FIRST negotiation is still live: cancel resolves it and keeps the draft.
  await sheet.getByRole("button", { name: "Cancel" }).click();
  await expect(sheet).toHaveCount(0);
  await expect(page.locator("#chat-composer-input")).toHaveValue("first send");
  expect(captured.runBodies).toHaveLength(0);
});

test("steering a running run authenticates the exact target; failure never cancels the run", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-steer" })],
    sseBody: null, // stream hangs → run stays active
    // First auth (the original send) passes; the steer's auth check fails.
    secretsPresentQueue: [
      { bedrock_api_key: true },
      { bedrock_api_key: false },
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "long running");
  await expect.poll(() => captured.runBodies.length).toBe(1);

  const composer = page.locator("#chat-composer-input");
  await expect(composer).toHaveAttribute(
    "placeholder",
    "Steer the response...",
  );
  await composer.fill("steer harder");
  await composer.press("Enter");

  // Auth for the steer's exact target fails BEFORE any preflight or restart.
  await expect(
    page.getByText(/Missing credentials for Bedrock Claude/),
  ).toBeVisible();
  expect(captured.preflightBodies).toHaveLength(1);
  expect(captured.runBodies).toHaveLength(1);
  expect(captured.secretsGets).toBe(2);
  // The in-flight run was not cancelled and the steer draft is kept.
  await expect(composer).toHaveAttribute(
    "placeholder",
    "Steer the response...",
  );
  await expect(composer).toHaveValue("steer harder");
});

test("a stale steering plan is re-preflighted once against the authenticated target", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [
      preflightResult("clean", { planId: "plan-initial" }),
      preflightResult("clean", { planId: "plan-steer-v1" }),
      preflightResult("clean", { planId: "plan-steer-v2" }),
    ],
    runResponses: [
      {
        status: 200,
        body: {
          run: { id: "run-initial", thread_id: THREAD_ID, status: "running" },
        },
      },
      { status: 409, body: { error: "plan_stale" } },
      {
        status: 200,
        body: {
          run: { id: "run-steer", thread_id: THREAD_ID, status: "running" },
        },
      },
    ],
    sseBody: null,
    secretsPresent: { bedrock_api_key: true },
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "long running");
  await expect.poll(() => captured.runBodies.length).toBe(1);

  const composer = page.locator("#chat-composer-input");
  await expect(composer).toHaveAttribute(
    "placeholder",
    "Steer the response...",
  );
  await composer.fill("steer with a fresh plan");
  await composer.press("Enter");

  await expect.poll(() => captured.runBodies.length).toBe(3);
  expect(captured.preflightBodies).toHaveLength(3);
  expect(captured.runBodies[1].planId).toBe("plan-steer-v1");
  expect(captured.runBodies[2].planId).toBe("plan-steer-v2");
  // Initial send + steer negotiation. Recovery reuses the same exact-target
  // authentication result instead of validating an unrelated default.
  expect(captured.secretsGets).toBe(2);
  await expect(page.getByText(/send plan changed/i)).toHaveCount(0);
});

test("composerTarget without preflight is inert: no pill, legacy send path (fail-closed dependency)", async ({
  page,
}) => {
  const captured = await setupApp(page, {});
  await page.goto("/?capabilityUx=composerTarget");

  await expect(page.locator("[data-target-pill]")).toHaveCount(0);
  await expect(
    page.getByText("bedrock:anthropic.claude-opus-4-6-v1"),
  ).toBeVisible();

  await typeAndSend(page, "dependency enforced");
  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.preflightBodies).toHaveLength(0);
  expect(captured.runBodies[0].planId).toBeUndefined();
});

test("flag-off legacy send authenticates the global dispatch target, not a persisted per-thread target it cannot transport", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    threadLastTarget: {
      connectionId: "cfg-anthropic",
      model: "claude-opus-4-6",
    },
    // Global Bedrock is valid; persisted Anthropic is intentionally not.
    secretsPresent: { bedrock_api_key: true, anthropic_api_key: false },
  });
  await page.goto("/");

  await typeAndSend(page, "legacy target is global");
  await expect.poll(() => captured.runBodies.length).toBe(1);
  expect(captured.preflightBodies).toHaveLength(0);
  expect(captured.secretsGets).toBe(1);
  expect(captured.runBodies[0].planId).toBeUndefined();
});

test("preflight endpoint missing after a target switch fails closed instead of dispatching the default", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    // Empty queue → the preflight endpoint 404s (older backend).
    preflightQueue: [],
  });
  await page.goto("/?capabilityUx=preflight,composerTarget");

  await page.locator("[data-target-pill]").click();
  await page.locator('[data-target-option="cfg-anthropic"]').click();
  await expect.poll(() => captured.targetPuts.length).toBe(1);

  await typeAndSend(page, "switched but no transport");
  await expect(
    page.getByText(
      "This backend can’t run per-chat model switches. The message wasn’t sent — your draft is kept.",
    ),
  ).toBeVisible();
  expect(captured.runBodies).toHaveLength(0);
  await expect(page.locator("#chat-composer-input")).toHaveValue(
    "switched but no transport",
  );
});

test("preflight endpoint missing after reload of a non-default persisted target also fails closed", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [],
    threadLastTarget: {
      connectionId: "cfg-anthropic",
      model: "claude-opus-4-6",
    },
    secretsPresent: { anthropic_api_key: true },
  });
  await page.goto("/?capabilityUx=preflight");

  await typeAndSend(page, "persisted switch but no transport");
  await expect(
    page.getByText(
      "This backend can’t run per-chat model switches. The message wasn’t sent — your draft is kept.",
    ),
  ).toBeVisible();
  expect(captured.preflightBodies).toHaveLength(1);
  expect(captured.preflightBodies[0].target.connectionId).toBe("cfg-anthropic");
  expect(captured.runBodies).toHaveLength(0);
  await expect(page.locator("#chat-composer-input")).toHaveValue(
    "persisted switch but no transport",
  );
});

test("persisted run plan hydrates into the thread on reload, deduplicated, and only with the flag on", async ({
  page,
}) => {
  const hydrationPlan = {
    planId: "plan-persisted",
    requirementsHash: "hash-h",
    status: "notices",
    blockers: [],
    choices: [
      {
        ...planIssue(
          "c1",
          "fidelity_reduction",
          "report.docx can’t be sent natively.",
          { attachmentId: "file-1" },
        ),
        options: [
          {
            id: "extract",
            label: "Send as extracted text",
            consequence: "",
            policyClass: null,
          },
        ],
        allowedScopes: ["run"],
      },
    ],
    notices: [
      planIssue(
        "n1",
        "surface_switch",
        "Documents switched this run to the Converse API.",
      ),
    ],
    staleEvidence: false,
    plan: {
      target: {
        connectionId: "cfg-bedrock",
        providerFamily: "bedrock",
        apiSurface: "converse_stream",
        endpointFamily: "bedrock-runtime",
        model: "anthropic.claude-opus-4-6-v1",
        region: "us-east-1",
        adapterVersion: "v1",
      },
      fingerprint: "fp",
      catalogVersion: "v1",
      generatedAt: "2026-07-11T00:00:00Z",
      features: {},
    },
    expiresAt: 1893456000000,
    lifecycleStatus: "finalized",
    target: {
      connectionId: "cfg-bedrock",
      model: "anthropic.claude-opus-4-6-v1",
    },
    choiceSelections: [
      {
        issueId: "c1",
        optionId: "extract",
        scope: "run",
        selectedAt: "2026-07-11T00:00:00Z",
      },
    ],
    createdAt: 1752192000000,
    finalizedAt: 1752192001000,
    runId: "run-past",
  };
  const captured = await setupApp(page, {
    threadLastRunId: "run-past",
    threadMessages: [
      {
        id: "msg-old-user",
        seq: 1,
        role: "user",
        content: "Earlier persisted question",
        run_id: "run-old",
      },
      {
        id: "msg-old-assistant",
        seq: 2,
        role: "assistant",
        content: "Earlier persisted answer",
        run_id: "run-old",
      },
      {
        id: "msg-latest-user",
        seq: 3,
        role: "user",
        content: "Latest persisted question",
        run_id: "run-past",
      },
      {
        id: "msg-latest-assistant",
        seq: 4,
        role: "assistant",
        content: "Latest persisted answer",
        run_id: "run-past",
      },
    ],
    runPlans: {
      "run-old": {
        ...hydrationPlan,
        planId: "plan-old",
        runId: "run-old",
        notices: [
          planIssue(
            "old-n1",
            "surface_switch",
            "Earlier run switched to the Converse API.",
          ),
        ],
      },
      "run-past": hydrationPlan,
    },
  });
  await page.goto("/?capabilityUx=preflight");

  const planRows = page.locator("[data-run-plan-row]");
  await expect(planRows).toHaveCount(2);
  const latestPlanRow = page.locator('[data-run-plan-row="run-past"]');
  await latestPlanRow.getByRole("button").first().click();
  await expect(latestPlanRow).toContainText(
    "report.docx can’t be sent natively. → Send as extracted text",
  );
  await expect(latestPlanRow).toContainText(
    "Documents switched this run to the Converse API.",
  );

  // Every historical row is restored at its persisted run boundary, never
  // appended below the answer (or below a newer run).
  const earlierUser = await page
    .getByText("Earlier persisted question", { exact: true })
    .boundingBox();
  const earlierPlan = await page
    .locator('[data-run-plan-row="run-old"]')
    .boundingBox();
  const earlierAnswer = await page
    .getByText("Earlier persisted answer", { exact: true })
    .boundingBox();
  const latestUser = await page
    .getByText("Latest persisted question", { exact: true })
    .boundingBox();
  const latestPlan = await latestPlanRow.boundingBox();
  const latestAnswer = await page
    .getByText("Latest persisted answer", { exact: true })
    .boundingBox();
  expect(earlierUser).not.toBeNull();
  expect(earlierPlan).not.toBeNull();
  expect(earlierAnswer).not.toBeNull();
  expect(latestUser).not.toBeNull();
  expect(latestPlan).not.toBeNull();
  expect(latestAnswer).not.toBeNull();
  expect(earlierUser!.y).toBeLessThan(earlierPlan!.y);
  expect(earlierPlan!.y).toBeLessThan(earlierAnswer!.y);
  expect(latestUser!.y).toBeLessThan(latestPlan!.y);
  expect(latestPlan!.y).toBeLessThan(latestAnswer!.y);
  // At least two hydration reads plus the explicit expander read. React may
  // overlap thread hydration, but plan rows remain transcript-deduplicated.
  expect(captured.planGets).toBeGreaterThanOrEqual(3);
});

test("persisted-plan hydration never fires with the preflight flag off (legacy DOM untouched)", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    threadLastRunId: "run-past",
    runPlan: { planId: "plan-persisted" },
  });
  await page.goto("/");
  await expect(page.locator("#chat-composer-input")).toBeVisible();
  await page.waitForTimeout(300);
  await expect(page.locator("[data-run-plan-row]")).toHaveCount(0);
  expect(captured.planGets).toBe(0);
});

test("sheet a11y: labeled sections, Escape cancels, and focus returns to the composer", async ({
  page,
}) => {
  await setupApp(page, {
    preflightQueue: [
      preflightResult("blocked", {
        planId: "plan-a11y",
        blockers: [
          planIssue(
            "b1",
            "permission_required",
            "Hosted web search needs permission.",
            {
              fix: { kind: "open_permissions" },
            },
          ),
        ],
        choices: [
          {
            ...planIssue(
              "c1",
              "fidelity_reduction",
              "doc.docx can’t be sent natively.",
              { attachmentId: "file-a" },
            ),
            options: [
              {
                id: "extract",
                label: "Send as extracted text",
                consequence: "",
                policyClass: null,
              },
            ],
            allowedScopes: ["run"],
          },
        ],
        notices: [
          planIssue(
            "n1",
            "surface_switch",
            "Documents switch this run to the Converse API.",
          ),
        ],
      }),
    ],
  });
  await page.goto("/?capabilityUx=preflight");
  await typeAndSend(page, "a11y pass");

  const sheet = page.getByRole("alertdialog", { name: "Before sending" });
  await expect(sheet).toBeVisible();
  await expect(sheet.getByRole("group", { name: "Blockers" })).toBeVisible();
  await expect(sheet.getByRole("group", { name: "Choices" })).toBeVisible();
  await expect(sheet.getByRole("group", { name: "Notices" })).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(sheet).toHaveCount(0);
  // Cancel keeps the draft and hands focus back to the composer.
  await expect(page.locator("#chat-composer-input")).toBeFocused();
  await expect(page.locator("#chat-composer-input")).toHaveValue("a11y pass");
});

test("target picker listbox: Arrow/Home/End roving focus and aria-selected", async ({
  page,
}) => {
  await setupApp(page, {});
  await page.goto("/?capabilityUx=preflight,composerTarget");

  await page.locator("[data-target-pill]").click();
  const listbox = page.getByRole("listbox", {
    name: "Provider connection and model",
  });
  await expect(listbox).toBeVisible();
  await expect(
    page.locator('[data-target-option="cfg-bedrock"]'),
  ).toHaveAttribute("aria-selected", "true");
  // Initial focus lands on the roving (selected) option.
  await expect(
    page.locator('[data-target-option="cfg-bedrock"]'),
  ).toBeFocused();

  await page.keyboard.press("ArrowDown");
  await expect(
    page.locator('[data-target-option="cfg-anthropic"]'),
  ).toBeFocused();
  await page.keyboard.press("ArrowUp");
  await expect(
    page.locator('[data-target-option="cfg-bedrock"]'),
  ).toBeFocused();
  await page.keyboard.press("End");
  await expect(
    page.locator('[data-target-option="cfg-anthropic"]'),
  ).toBeFocused();
  await page.keyboard.press("Home");
  await expect(
    page.locator('[data-target-option="cfg-bedrock"]'),
  ).toBeFocused();
  // Escape closes the picker without switching anything.
  await page.keyboard.press("Escape");
  await expect(listbox).toHaveCount(0);
  await expect(page.locator("[data-target-pill]")).toContainText(
    "anthropic.claude-opus-4-6-v1",
  );
});

test("deleting the selected target’s connection fails the next send closed — never another connection", async ({
  page,
}) => {
  const captured = await setupApp(page, {
    preflightQueue: [preflightResult("clean", { planId: "plan-stale-target" })],
  });
  await page.goto("/?capabilityUx=preflight,composerTarget,report");

  // Explicitly select B for this thread…
  await page.locator("[data-target-pill]").click();
  await page.locator('[data-target-option="cfg-anthropic"]').click();
  await expect.poll(() => captured.targetPuts.length).toBe(1);

  // …then delete B's connection in Settings and persist.
  await page.getByRole("button", { name: "Open settings" }).click();
  const settings = page.getByRole("dialog", { name: "Settings" });
  await settings.getByRole("tab", { name: "Providers" }).click();
  await settings.getByRole("button", { name: "Delete Work Anthropic" }).click();
  await settings.getByRole("button", { name: "Delete", exact: true }).click();
  await settings.getByRole("button", { name: "Save", exact: true }).click();
  await expect(settings).toHaveCount(0);
  await expect.poll(() => captured.settingsPuts.length).toBeGreaterThan(0);

  // The stale explicit target must be rejected, not silently replaced.
  await typeAndSend(page, "stale target send");
  await expect(
    page.getByText(/The selected model's connection no longer exists/),
  ).toBeVisible();
  expect(captured.preflightBodies).toHaveLength(0);
  expect(captured.runBodies).toHaveLength(0);
  await expect(page.locator("#chat-composer-input")).toHaveValue(
    "stale target send",
  );
});
