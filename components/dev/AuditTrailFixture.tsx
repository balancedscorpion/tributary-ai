import React from "react";
import { TooltipProvider } from "@/components/ui/tooltip";
import CollapsedToolsSummary from "@/components/chat/CollapsedToolsSummary";
import ArtifactPanel from "@/components/ArtifactPanel";
import InspectorOverlay from "@/components/InspectorOverlay";
import type {
  AppSettings,
  Artifact,
  ToolCall,
  ToolProgressEvent,
  ToolResultEnvelope,
} from "@/types";
import { isQueryRunToolName } from "@/lib/queryProviderRegistry";
import { getQueryCardStateKey } from "@/lib/queryCardStateKey";

const now = Date.now();

// Build a synthetic envelope and the matching Artifact the renderer expects.
function artifactFromEnvelope(
  envelope: ToolResultEnvelope,
  args: Record<string, unknown>,
): Artifact {
  const result = envelope.result as any;
  const startedAt =
    typeof result?._uiStartedAt === "number" ? result._uiStartedAt : undefined;
  const endedAt =
    typeof result?._uiEndedAt === "number" ? result._uiEndedAt : undefined;

  // Detect async query handle (running/submitted). useChat normally turns
  // these into placeholder artifacts; the fixture mirrors that so the
  // artifact-panel running view matches what production would build.
  const isAsyncHandle =
    result &&
    typeof result === "object" &&
    typeof (result as any).job_id === "string" &&
    ((result as any).status === "running" || (result as any).status === "submitted");

  if (isAsyncHandle) {
    return {
      id: envelope.artifact_id,
      type: "data",
      title: "Query Running",
      content: "",
      metadata: {
        tool_name: envelope.tool_name,
        tool_call_id: envelope.tool_call_id,
        integration: envelope.result_metadata?.integration,
        product: envelope.result_metadata?.product,
        widget: envelope.result_metadata?.widget,
        query_metadata: envelope.query_metadata,
        provider: envelope.query_metadata?.provider,
        ok: true,
        placeholder: true,
        async_query: true,
        job_id: (result as any).job_id,
        tool_args: args,
        started_at_ms: startedAt,
        _uiStartedAt: startedAt,
        raw_result: envelope.result,
        result_metadata: envelope.result_metadata,
      },
    };
  }

  return {
    id: envelope.artifact_id,
    type: "data",
    title: envelope.tool_name || "Tool result",
    content:
      typeof envelope.result === "string"
        ? (envelope.result as string)
        : JSON.stringify(envelope.result, null, 2),
    metadata: {
      tool_name: envelope.tool_name,
      tool_call_id: envelope.tool_call_id,
      // Derive integration/product from the envelope so non-GitHub fixtures
      // can route through the generic ErrorRenderer instead of being claimed
      // by the GitHub resolver.
      integration: envelope.result_metadata?.integration,
      product: envelope.result_metadata?.product,
      widget: envelope.result_metadata?.widget,
      // Preserve structured query_metadata so the Query Tool Inspector can
      // read provider, job_id, schema, stats, etc.
      query_metadata: envelope.query_metadata,
      provider: envelope.query_metadata?.provider,
      ok: envelope.ok,
      tool_args: args,
      started_at_ms: startedAt,
      ended_at_ms: endedAt,
      duration_ms:
        typeof startedAt === "number" && typeof endedAt === "number"
          ? endedAt - startedAt
          : undefined,
      raw_result: envelope.result,
      result_metadata: envelope.result_metadata,
    },
  };
}

interface FixtureEntry {
  label: string;
  call: ToolCall;
  envelope: ToolResultEnvelope;
}

function meta(widget: string): Record<string, any> {
  return { integration: "github", product: "github", widget };
}

function timing(offsetMs: number, durationMs: number) {
  return {
    _uiStartedAt: now - offsetMs - durationMs,
    _uiEndedAt: now - offsetMs,
  };
}

// ---------- synthetic query datasets ----------

// Deterministic pseudo-random so the fixture is stable across reloads.
function seeded(seed: number): () => number {
  let s = seed | 0;
  return () => {
    s = (s * 9301 + 49297) % 233280;
    return s / 233280;
  };
}

function buildBigQueryRows(): {
  rows: Record<string, unknown>[];
  schema: Array<{ name: string; type: string; mode?: string; description?: string }>;
} {
  const channels = ['organic_search', 'paid_search', 'direct', 'referral', 'email', 'social'];
  const countries = ['US', 'GB', 'DE', 'FR', 'BR', 'JP', 'IN', 'AU'];
  const rng = seeded(1421);
  const rows: Record<string, unknown>[] = [];
  for (let i = 0; i < 64; i++) {
    const day = new Date(now - (30 - (i % 30)) * 86_400_000);
    const channel = channels[i % channels.length];
    const country = countries[Math.floor(rng() * countries.length)];
    const sessions = 1200 + Math.floor(rng() * 9_800);
    const conversions = Math.floor(sessions * (0.012 + rng() * 0.04));
    const revenue = +(conversions * (32 + rng() * 88)).toFixed(2);
    const new_visitor_share = +(0.18 + rng() * 0.6).toFixed(3);
    rows.push({
      session_day: day.toISOString().slice(0, 10),
      channel,
      country,
      sessions,
      conversions,
      revenue_usd: revenue,
      new_visitor_share,
      is_top_channel: channel === 'organic_search',
    });
  }
  return {
    rows,
    schema: [
      { name: 'session_day', type: 'DATE' },
      { name: 'channel', type: 'STRING' },
      { name: 'country', type: 'STRING' },
      { name: 'sessions', type: 'INT64' },
      { name: 'conversions', type: 'INT64' },
      { name: 'revenue_usd', type: 'FLOAT64' },
      { name: 'new_visitor_share', type: 'FLOAT64', description: 'Fraction of sessions from first-time visitors.' },
      { name: 'is_top_channel', type: 'BOOL' },
    ],
  };
}

function buildDatabricksRows(): {
  rows: Record<string, unknown>[];
  schema: Array<{ name: string; type: string; mode?: string; description?: string }>;
} {
  const regions = ['us-east-1', 'us-west-2', 'eu-west-1', 'ap-south-1'];
  const eventTypes = ['ingest', 'flush', 'compaction', 'snapshot', 'rebalance'];
  const rng = seeded(7702);
  const rows: Record<string, unknown>[] = [];
  for (let i = 0; i < 58; i++) {
    const sentAt = new Date(now - i * 17_000);
    const region = regions[i % regions.length];
    const eventType = eventTypes[Math.floor(rng() * eventTypes.length)];
    const deviceId = `dev_${(2000 + Math.floor(rng() * 6500)).toString(16)}`;
    const payloadKb = +((40 + rng() * 9600) / 10).toFixed(2);
    const latencyMs = Math.floor(8 + rng() * 240);
    const ok = rng() > 0.07;
    rows.push({
      sent_at: sentAt.toISOString(),
      region,
      event_type: eventType,
      device_id: deviceId,
      payload_kb: payloadKb,
      latency_ms: latencyMs,
      ok,
    });
  }
  return {
    rows,
    schema: [
      { name: 'sent_at', type: 'TIMESTAMP' },
      { name: 'region', type: 'STRING' },
      { name: 'event_type', type: 'STRING' },
      { name: 'device_id', type: 'STRING' },
      { name: 'payload_kb', type: 'DOUBLE' },
      { name: 'latency_ms', type: 'INT' },
      { name: 'ok', type: 'BOOLEAN' },
    ],
  };
}

// ---------- synthetic data ----------

const fixtures: FixtureEntry[] = [
  // 1. github_status
  {
    label: "Integration status",
    call: { id: "call_status", name: "github_status", args: {} },
    envelope: {
      artifact_id: "artifact_status",
      tool_name: "github_status",
      tool_call_id: "call_status",
      ok: true,
      result_format: "json",
      result: {
        status: {
          configured: true,
          client_id_configured: false,
          secrets_initialized: true,
          secrets_locked: false,
          connected: true,
          login: "example-user",
          scope: "repo read:org workflow",
          read_tools_enabled: true,
          allowed_repositories: ["example-lab/demo-benchmark"],
          lastCheckedAt: now - 2 * 60_000,
          lastCheckOk: true,
          lastCheckMessage: "GitHub CLI authentication is usable.",
          message: "GitHub CLI authentication is usable.",
        },
        ...timing(1500, 420),
      },
      result_metadata: meta("status"),
    },
  },

  // 2. github_check_connection
  {
    label: "Connection status",
    call: { id: "call_conn", name: "github_check_connection", args: {} },
    envelope: {
      artifact_id: "artifact_conn",
      tool_name: "github_check_connection",
      tool_call_id: "call_conn",
      ok: true,
      result_format: "json",
      result: {
        result_metadata: meta("connection_status"),
        status: {
          ok: true,
          status: {
            configured: true,
            client_id_configured: false,
            secrets_initialized: true,
            secrets_locked: false,
            connected: true,
            login: "example-user",
            read_tools_enabled: true,
            lastCheckedAt: now - 90_000,
            lastCheckOk: true,
            lastCheckMessage: "GitHub CLI authentication is usable.",
            message: "GitHub CLI authentication is usable.",
          },
        },
        ...timing(2200, 90),
      },
      result_metadata: meta("connection_status"),
    },
  },

  // 3. github_list_repositories
  {
    label: "Repository list",
    call: { id: "call_repos", name: "github_list_repositories", args: { owner: "example-org" } },
    envelope: {
      artifact_id: "artifact_repos",
      tool_name: "github_list_repositories",
      tool_call_id: "call_repos",
      ok: true,
      result_format: "json",
      // Live backend shape: ProjectRepository rows from
      // server/internal/githubcli/types.go (no visibility/updated_at —
      // remote/resolvable instead).
      result: {
        project_id: "dev-fixture",
        repositories: [
          {
            project_id: "dev-fixture",
            repo_path: "Demo-Benchmark",
            owner: "example-lab",
            repo: "demo-benchmark",
            full_name: "example-lab/demo-benchmark",
            remote_name: "origin",
            remote_url: "https://github.com/example-lab/demo-benchmark",
            github_remotes: [
              {
                name: "origin",
                url: "https://github.com/example-lab/demo-benchmark",
                owner: "example-lab",
                repo: "demo-benchmark",
                full_name: "example-lab/demo-benchmark",
              },
            ],
            resolvable: true,
          },
          {
            project_id: "dev-fixture",
            repo_path: "source/issue-tracker",
            owner: "example-org",
            repo: "issue-tracker",
            full_name: "example-org/issue-tracker",
            remote_name: "origin",
            remote_url: "https://github.com/example-org/issue-tracker",
            resolvable: true,
          },
          {
            project_id: "dev-fixture",
            repo_path: "source/audit-ui-fixtures",
            owner: "example-org",
            repo: "audit-ui-fixtures",
            full_name: "example-org/audit-ui-fixtures",
            remote_name: "fork",
            remote_url: "git@github.com:example-org/audit-ui-fixtures.git",
            resolvable: false,
          },
        ],
        ...timing(800, 320),
      },
      result_metadata: meta("repository_list"),
    },
  },

  // 4. github_list_pull_requests
  {
    label: "Pull request list",
    call: {
      id: "call_prs",
      name: "github_list_pull_requests",
      args: { repository: "example-org/issue-tracker", state: "open" },
    },
    envelope: {
      artifact_id: "artifact_prs",
      tool_name: "github_list_pull_requests",
      tool_call_id: "call_prs",
      ok: true,
      result_format: "json",
      result: {
        project_id: "dev-fixture",
        repository: "example-org/issue-tracker",
        state: "open",
        limit: 30,
        pull_requests: [
          {
            number: 412,
            title: "Stream rate-limit envelope to the client and reuse it in toast components",
            state: "open",
            url: "https://github.com/example-org/issue-tracker/pull/412",
            author: { login: "example-user", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4" },
            head_ref_name: "stream-rate-limit",
            base_ref_name: "main",
            review_decision: "APPROVED",
            is_draft: false,
            labels: [{ name: "frontend" }, { name: "rate-limit" }, { name: "needs-review" }],
            created_at: new Date(now - 2 * 24 * 60 * 60_000).toISOString(),
            updated_at: new Date(now - 4 * 60 * 60_000).toISOString(),
          },
          {
            number: 411,
            title: "wip: split desktop bridge into native + worker channels",
            state: "open",
            url: "https://github.com/example-org/issue-tracker/pull/411",
            author: { login: "ada", avatar_url: "https://avatars.githubusercontent.com/u/2?v=4" },
            head_ref_name: "desktop-bridge-split",
            base_ref_name: "main",
            review_decision: "REVIEW_REQUIRED",
            is_draft: true,
            labels: [{ name: "desktop" }],
            created_at: new Date(now - 7 * 60 * 60_000).toISOString(),
            updated_at: new Date(now - 50 * 60_000).toISOString(),
          },
          {
            number: 407,
            title: "Fix audit-trail tool scope leaking across chats",
            state: "closed",
            merged_at: new Date(now - 6 * 60 * 60_000).toISOString(),
            url: "https://github.com/example-org/issue-tracker/pull/407",
            author: { login: "marian", avatar_url: "https://avatars.githubusercontent.com/u/3?v=4" },
            head_ref_name: "ta-scope-fix",
            base_ref_name: "main",
            review_decision: "APPROVED",
            is_draft: false,
            labels: [{ name: "bug" }, { name: "audit-trail" }],
            created_at: new Date(now - 36 * 60 * 60_000).toISOString(),
            updated_at: new Date(now - 6 * 60 * 60_000).toISOString(),
          },
        ],
        ...timing(2400, 540),
      },
      result_metadata: meta("pull_request_list"),
    },
  },

  // 5. github_get_pull_request
  {
    label: "Pull request (single)",
    call: {
      id: "call_pr",
      name: "github_get_pull_request",
      args: { repository: "example-org/issue-tracker", number: 412 },
    },
    envelope: {
      artifact_id: "artifact_pr",
      tool_name: "github_get_pull_request",
      tool_call_id: "call_pr",
      ok: true,
      result_format: "json",
      result: {
        project_id: "dev-fixture",
        repository: "example-org/issue-tracker",
        number: 412,
        pull_request: {
          number: 412,
          title: "Stream rate-limit envelope to the client and reuse it in toast components",
          state: "open",
          url: "https://github.com/example-org/issue-tracker/pull/412",
          author: { login: "example-user", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4" },
          head_ref_name: "stream-rate-limit",
          base_ref_name: "main",
          is_draft: false,
          review_decision: "APPROVED",
          mergeable: true,
          mergeable_state: "mergeable",
          labels: [{ name: "frontend" }, { name: "rate-limit" }, { name: "needs-review" }],
          assignees: [
            { login: "example-user", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4" },
          ],
          reviewers: [
            { login: "ada", avatar_url: "https://avatars.githubusercontent.com/u/2?v=4" },
            { login: "marian", avatar_url: "https://avatars.githubusercontent.com/u/3?v=4" },
          ],
          created_at: new Date(now - 2 * 24 * 60 * 60_000).toISOString(),
          updated_at: new Date(now - 4 * 60 * 60_000).toISOString(),
          body: [
            "## Summary",
            "Bubble the SSE `rate-limit` envelope all the way to React state so the toast",
            "components can render the remaining budget without a second poll.",
            "",
            "## Notes",
            "- Stream is per-chat",
            "- Reuses existing `useChat` boundary",
            "",
            "Closes #399, partially addresses #400.",
          ].join("\n"),
        },
        ...timing(3000, 220),
      },
      result_metadata: meta("pull_request"),
    },
  },

  // 6. github_list_issues
  {
    label: "Issue list",
    call: {
      id: "call_issues",
      name: "github_list_issues",
      args: { repository: "example-org/issue-tracker", state: "open", limit: 2 },
    },
    envelope: {
      artifact_id: "artifact_issues",
      tool_name: "github_list_issues",
      tool_call_id: "call_issues",
      ok: true,
      result_format: "json",
      // Mirrors the real github_list_issues envelope: the tool wrapper
      // (server/internal/chat/tools/github_tools.go) nests the backend
      // githubcli.IssueList object under `issues`, with the repository as a
      // ProjectRepository object inside it.
      result: {
        project_id: "dev-fixture",
        repository: "example-org/issue-tracker",
        state: "open",
        limit: 2,
        issues: {
          repository: {
            project_id: "dev-fixture",
            repo_path: "issue-tracker",
            owner: "example-org",
            repo: "issue-tracker",
            full_name: "example-org/issue-tracker",
            remote_name: "origin",
            remote_url: "https://github.com/example-org/issue-tracker",
            resolvable: true,
          },
          issues: [
            {
              number: 401,
              title: "Audit-trail group rolls up errors but doesn't expose the first failing tool",
              state: "open",
              url: "https://github.com/example-org/issue-tracker/issues/401",
              author: { login: "ada", avatar_url: "https://avatars.githubusercontent.com/u/2?v=4" },
              labels: [{ name: "audit-trail" }, { name: "ux" }],
              assignees: [
                { login: "example-user", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4" },
              ],
              comments_count: 4,
              created_at: new Date(now - 5 * 24 * 60 * 60_000).toISOString(),
              updated_at: new Date(now - 90 * 60_000).toISOString(),
            },
            {
              number: 388,
              title: "Add per-chat tool scope to the connection panel",
              state: "open",
              url: "https://github.com/example-org/issue-tracker/issues/388",
              author: { login: "marian", avatar_url: "https://avatars.githubusercontent.com/u/3?v=4" },
              labels: [{ name: "connections" }],
              comments_count: 12,
              created_at: new Date(now - 9 * 24 * 60 * 60_000).toISOString(),
              updated_at: new Date(now - 12 * 60 * 60_000).toISOString(),
            },
          ],
          // Live backend semantics (githubcli.ListIssues): total_count is the
          // returned count, truncated flips when it hits the requested limit.
          total_count: 2,
          limit: 2,
          truncated: true,
        },
        ...timing(2600, 380),
      },
      result_metadata: meta("issue_list"),
    },
  },

  // 7. github_get_issue
  {
    label: "Issue (single)",
    call: {
      id: "call_issue",
      name: "github_get_issue",
      args: { repository: "example-org/issue-tracker", number: 401 },
    },
    envelope: {
      artifact_id: "artifact_issue",
      tool_name: "github_get_issue",
      tool_call_id: "call_issue",
      ok: true,
      result_format: "json",
      // Mirrors the real github_get_issue envelope: the tool wrapper nests
      // the backend githubcli.IssueResult ({repository, issue}) under
      // `issue`, so the actual Issue sits two levels deep.
      result: {
        project_id: "dev-fixture",
        repository: "example-org/issue-tracker",
        number: 401,
        issue: {
          repository: {
            project_id: "dev-fixture",
            repo_path: "issue-tracker",
            owner: "example-org",
            repo: "issue-tracker",
            full_name: "example-org/issue-tracker",
            remote_name: "origin",
            remote_url: "https://github.com/example-org/issue-tracker",
            resolvable: true,
          },
          issue: {
            number: 401,
            title: "Audit-trail group rolls up errors but doesn't expose the first failing tool",
            state: "open",
            url: "https://github.com/example-org/issue-tracker/issues/401",
            author: { login: "ada", avatar_url: "https://avatars.githubusercontent.com/u/2?v=4" },
            labels: [{ name: "audit-trail" }, { name: "ux" }],
            assignees: [
              { login: "example-user", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4" },
            ],
            comments_count: 4,
            created_at: new Date(now - 5 * 24 * 60 * 60_000).toISOString(),
            updated_at: new Date(now - 90 * 60_000).toISOString(),
            body: [
              "When a group bubble rolls up two tool calls and one fails, the bubble shows a red",
              "border but we still have to scroll the inspector to find which tool failed and what",
              "the message was. Surface the first failure inline so the user can triage without",
              "expanding everything.",
            ].join(" "),
          },
        },
        ...timing(3300, 210),
      },
      result_metadata: meta("issue"),
    },
  },

  // 8. github_list_workflow_runs
  {
    label: "Workflow runs",
    call: {
      id: "call_runs",
      name: "github_list_workflow_runs",
      args: { repository: "example-org/issue-tracker" },
    },
    envelope: {
      artifact_id: "artifact_runs",
      tool_name: "github_list_workflow_runs",
      tool_call_id: "call_runs",
      ok: true,
      result_format: "json",
      result: {
        project_id: "dev-fixture",
        repository: "example-org/issue-tracker",
        state: "completed",
        limit: 30,
        workflow_runs: [
          {
            database_id: 9001,
            number: 231,
            workflow_name: "ci",
            display_title: "Fix audit-trail tool scope leaking across chats",
            status: "completed",
            conclusion: "success",
            event: "pull_request",
            head_branch: "ta-scope-fix",
            head_sha: "1c50683f1c50683",
            url: "https://github.com/example-org/issue-tracker/actions/runs/9001",
            actor: { login: "marian", avatar_url: "https://avatars.githubusercontent.com/u/3?v=4" },
            created_at: new Date(now - 8 * 60 * 60_000).toISOString(),
            started_at: new Date(now - 8 * 60 * 60_000).toISOString(),
            updated_at: new Date(now - 8 * 60 * 60_000 + 4 * 60_000 + 12_000).toISOString(),
          },
          {
            database_id: 9002,
            number: 230,
            workflow_name: "ci",
            display_title: "wip: split desktop bridge into native + worker channels",
            status: "in_progress",
            conclusion: null,
            event: "push",
            head_branch: "desktop-bridge-split",
            head_sha: "907cc0f1c50683",
            url: "https://github.com/example-org/issue-tracker/actions/runs/9002",
            actor: { login: "ada", avatar_url: "https://avatars.githubusercontent.com/u/2?v=4" },
            created_at: new Date(now - 12 * 60_000).toISOString(),
            started_at: new Date(now - 11 * 60_000).toISOString(),
            updated_at: new Date(now - 60_000).toISOString(),
          },
          {
            database_id: 9003,
            number: 229,
            workflow_name: "ci",
            display_title: "Add per-chat tool audit UI",
            status: "completed",
            conclusion: "failure",
            event: "pull_request",
            head_branch: "audit-ui",
            head_sha: "aa38e0b1c50683",
            url: "https://github.com/example-org/issue-tracker/actions/runs/9003",
            actor: { login: "example-user", avatar_url: "https://avatars.githubusercontent.com/u/1?v=4" },
            created_at: new Date(now - 26 * 60 * 60_000).toISOString(),
            started_at: new Date(now - 26 * 60 * 60_000).toISOString(),
            updated_at: new Date(now - 26 * 60 * 60_000 + 3 * 60_000 + 4_000).toISOString(),
          },
        ],
        ...timing(900, 450),
      },
      result_metadata: meta("workflow_run_list"),
    },
  },

  // 9. github_get_workflow_run_jobs
  {
    label: "Workflow run jobs",
    call: {
      id: "call_jobs",
      name: "github_get_workflow_run_jobs",
      args: { repository: "example-org/issue-tracker", run_id: "9003" },
    },
    envelope: {
      artifact_id: "artifact_jobs",
      tool_name: "github_get_workflow_run_jobs",
      tool_call_id: "call_jobs",
      ok: true,
      result_format: "json",
      result: {
        project_id: "dev-fixture",
        repository: "example-org/issue-tracker",
        run_id: "9003",
        jobs: [
          {
            database_id: 50001,
            name: "lint",
            status: "completed",
            conclusion: "success",
            url: "https://github.com/example-org/issue-tracker/actions/runs/9003/jobs/50001",
            started_at: new Date(now - 26 * 60 * 60_000).toISOString(),
            completed_at: new Date(now - 26 * 60 * 60_000 + 70_000).toISOString(),
            steps: [
              { number: 1, name: "Setup", status: "completed", conclusion: "success", started_at: new Date(now - 26 * 60 * 60_000).toISOString(), completed_at: new Date(now - 26 * 60 * 60_000 + 12_000).toISOString() },
              { number: 2, name: "ESLint", status: "completed", conclusion: "success", started_at: new Date(now - 26 * 60 * 60_000 + 12_000).toISOString(), completed_at: new Date(now - 26 * 60 * 60_000 + 70_000).toISOString() },
            ],
          },
          {
            database_id: 50002,
            name: "test",
            status: "completed",
            conclusion: "failure",
            url: "https://github.com/example-org/issue-tracker/actions/runs/9003/jobs/50002",
            started_at: new Date(now - 26 * 60 * 60_000).toISOString(),
            completed_at: new Date(now - 26 * 60 * 60_000 + 3 * 60_000).toISOString(),
            steps: [
              { number: 1, name: "Setup", status: "completed", conclusion: "success", started_at: new Date(now - 26 * 60 * 60_000).toISOString(), completed_at: new Date(now - 26 * 60 * 60_000 + 10_000).toISOString() },
              { number: 2, name: "Install", status: "completed", conclusion: "success", started_at: new Date(now - 26 * 60 * 60_000 + 10_000).toISOString(), completed_at: new Date(now - 26 * 60 * 60_000 + 32_000).toISOString() },
              { number: 3, name: "Vitest", status: "completed", conclusion: "failure", started_at: new Date(now - 26 * 60 * 60_000 + 32_000).toISOString(), completed_at: new Date(now - 26 * 60 * 60_000 + 3 * 60_000).toISOString() },
            ],
          },
        ],
        ...timing(1400, 320),
      },
      result_metadata: meta("workflow_run_jobs"),
    },
  },

  // 10. approval (clone repository)
  {
    label: "Approval (clone)",
    call: {
      id: "call_approval",
      name: "github_clone_repository",
      args: {
        repository: "example-org/model-eval",
        branch: "main",
        depth: 1,
      },
    },
    envelope: {
      artifact_id: "artifact_approval",
      tool_name: "github_clone_repository",
      tool_call_id: "call_approval",
      ok: true,
      result_format: "json",
      result: {
        approval: {
          id: "appr_8f3c9a",
          title: "Clone example-org/model-eval into the current project",
          detail:
            "Will run a depth-1 clone of branch `main` from example-org/model-eval into the current project directory.",
          tool_name: "github_clone_repository",
          product: "github",
          action: "clone_repository",
          state: "pending",
          target: {
            repository: "example-org/model-eval",
            branch: "main",
          },
          content: {
            command: "gh repo clone example-org/model-eval -- --depth 1 --branch main",
          },
          created_at: new Date(now - 90_000).toISOString(),
        },
        ...timing(60_000, 60),
      },
      result_metadata: meta("approval"),
    },
  },

  // 11. gh_command
  {
    label: "gh command",
    call: {
      id: "call_gh",
      name: "github_run_gh_command",
      args: { args: ["pr", "list", "--repo", "example-org/issue-tracker", "--state", "open", "--limit", "3"] },
    },
    envelope: {
      artifact_id: "artifact_gh",
      tool_name: "github_run_gh_command",
      tool_call_id: "call_gh",
      ok: true,
      result_format: "json",
      result: {
        command: "gh pr list --repo example-org/issue-tracker --state open --limit 3",
        result: {
          args: ["pr", "list", "--repo", "example-org/issue-tracker", "--state", "open", "--limit", "3"],
          stdout: [
            "#412  Stream rate-limit envelope to the client and reuse it in toast components  stream-rate-limit",
            "#411  wip: split desktop bridge into native + worker channels                    desktop-bridge-split",
            "#410  Add per-chat tool scope to the connection panel                            ta-scope",
          ].join("\n"),
          stderr: "",
          exit_code: 0,
          timed_out: false,
          stdout_truncated: false,
          stderr_truncated: false,
        },
        ...timing(450, 1240),
      },
      result_metadata: meta("gh_command"),
    },
  },

  // 12. BigQuery — sessions / conversions / revenue by channel + country.
  (() => {
    const ds = buildBigQueryRows();
    const sql = [
      '-- Daily sessions, conversions and revenue by channel and country',
      '-- for the last 30 days.',
      'SELECT',
      '  DATE(session_start) AS session_day,',
      '  traffic_source.channel AS channel,',
      '  geo.country AS country,',
      '  COUNT(DISTINCT session_id) AS sessions,',
      '  SUM(IF(event_type = "purchase", 1, 0)) AS conversions,',
      '  ROUND(SUM(IF(event_type = "purchase", revenue_usd, 0)), 2) AS revenue_usd,',
      '  SAFE_DIVIDE(',
      '    COUNT(DISTINCT IF(is_new_visitor, session_id, NULL)),',
      '    COUNT(DISTINCT session_id)',
      '  ) AS new_visitor_share,',
      '  traffic_source.channel = "organic_search" AS is_top_channel',
      'FROM `analytics-prod.events.sessions_daily`',
      'WHERE session_start >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 30 DAY)',
      'GROUP BY session_day, channel, country',
      'ORDER BY session_day DESC, sessions DESC',
      'LIMIT 1000',
    ].join('\n');
    return {
      label: 'BigQuery query',
      call: {
        id: 'call_bq_query',
        name: 'run_bigquery_query',
        args: {
          sql,
          description:
            'Daily sessions, conversions and revenue by channel and country, for the last 30 days.',
        },
      },
      envelope: {
        artifact_id: 'artifact_bq_query',
        tool_name: 'run_bigquery_query',
        tool_call_id: 'call_bq_query',
        ok: true,
        result_format: 'json',
        result: {
          rows: ds.rows,
          ...timing(950, 920),
        },
        query_metadata: {
          provider: 'bigquery',
          duration_ms: 920,
          job_id: 'bquxjob_3f9c2a1e_198d31f4',
          provider_job_id: 'bquxjob_3f9c2a1e_198d31f4',
          provider_job_url:
            'https://console.cloud.google.com/bigquery?project=analytics-prod&j=bq:US:bquxjob_3f9c2a1e_198d31f4&page=queryresults',
          project_id: 'analytics-prod',
          job_location: 'US',
          schema: ds.schema,
          rows_returned: ds.rows.length,
          row_count: ds.rows.length,
          total_bytes_processed: 1_503_338_496,
          cache_hit: true,
          description:
            'Daily sessions, conversions and revenue by channel and country, for the last 30 days.',
        },
        result_metadata: { integration: 'bigquery', product: 'bigquery' },
      },
    } as FixtureEntry;
  })(),

  // 13. Databricks — streaming device telemetry slice.
  (() => {
    const ds = buildDatabricksRows();
    const sql = [
      '-- Last 1,000 streaming-engine events across regions; flag long-tail',
      '-- latencies and failed publishes so the on-call can spot bursts.',
      'SELECT',
      '  sent_at,',
      '  region,',
      '  event_type,',
      '  device_id,',
      '  payload_bytes / 1024.0 AS payload_kb,',
      '  latency_ms,',
      '  status = "ok" AS ok',
      'FROM telemetry.events_raw',
      'WHERE sent_at >= current_timestamp() - INTERVAL 1 HOUR',
      'ORDER BY sent_at DESC',
      'LIMIT 1000',
    ].join('\n');
    return {
      label: 'Databricks query',
      call: {
        id: 'call_dbx_query',
        name: 'run_databricks_query',
        args: {
          sql,
          description:
            'Streaming-engine events from the last hour, scoped by region with latency and ok flag.',
        },
      },
      envelope: {
        artifact_id: 'artifact_dbx_query',
        tool_name: 'run_databricks_query',
        tool_call_id: 'call_dbx_query',
        ok: true,
        result_format: 'json',
        result: {
          rows: ds.rows,
          ...timing(820, 1340),
        },
        query_metadata: {
          provider: 'databricks',
          duration_ms: 1340,
          statement_id: '01HZK2X9KGB4PEVQNT3K5GAM2V',
          provider_job_id: '01HZK2X9KGB4PEVQNT3K5GAM2V',
          provider_job_url:
            'https://dbc-1234abcd-5678.cloud.databricks.com/sql/history?statementId=01HZK2X9KGB4PEVQNT3K5GAM2V',
          host: 'dbc-1234abcd-5678.cloud.databricks.com',
          warehouse_id: 'a1b2c3d4e5f6g7h8',
          schema: ds.schema,
          rows_returned: ds.rows.length,
          row_count: ds.rows.length,
          description:
            'Streaming-engine events from the last hour, scoped by region with latency and ok flag.',
        },
        result_metadata: { integration: 'databricks', product: 'databricks' },
      },
    } as FixtureEntry;
  })(),

  // 14. BigQuery — SQL syntax error case. Line + column come back from the
  //     server so the Query zone can underline the offending line.
  (() => {
    const sql = [
      'SELECT',
      '  channel,',
      '  COUNT(* FROM sessions_daily',
      '  WHERE session_start >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY)',
      'GROUP BY channel',
    ].join('\n');
    return {
      label: 'BigQuery error',
      call: {
        id: 'call_bq_error',
        name: 'run_bigquery_query',
        args: { sql, description: 'Weekly session counts by channel.' },
      },
      envelope: {
        artifact_id: 'artifact_bq_error',
        tool_name: 'run_bigquery_query',
        tool_call_id: 'call_bq_error',
        ok: false,
        result_format: 'json',
        result: {
          error:
            'Syntax error: Expected ")" but got keyword FROM at [3:12]',
          ...timing(220, 180),
        },
        query_metadata: {
          provider: 'bigquery',
          job_id: 'bquxjob_a44e9b22_198d3201',
          job_location: 'US',
          error_line: 3,
          error_column: 12,
          duration_ms: 180,
        },
        result_metadata: {
          integration: 'bigquery',
          product: 'bigquery',
          diagnostic: {
            title: 'SQL syntax error',
            code: '400',
            kind: 'validation',
            why: 'COUNT(*) was opened but never closed before the FROM keyword.',
            try_this:
              'Add the missing `)` after `COUNT(*` so the projection list is complete.',
          },
        },
      },
    } as FixtureEntry;
  })(),

  // 15. Generic tool error with STRUCTURED `result_metadata.diagnostic` —
  //     proves the generic ErrorRenderer surfaces Why / Try this from the
  //     tool's diagnostic metadata, not from frontend heuristics.
  {
    label: "Generic error (read_file)",
    call: {
      id: "call_read_file_error",
      name: "read_file",
      args: { path: "/etc/secrets/api-key.conf" },
    },
    envelope: {
      artifact_id: "artifact_read_file_error",
      tool_name: "read_file",
      tool_call_id: "call_read_file_error",
      ok: false,
      result_format: "json",
      result: {
        error: "open /etc/secrets/api-key.conf: permission denied",
        ...timing(900, 80),
      },
      result_metadata: {
        // No `integration` field — this exercises the generic ErrorRenderer
        // and proves the GitHub resolver guard works.
        diagnostic: {
          title: "Permission denied",
          code: "EACCES",
          kind: "permission",
          why: "The file exists, but the agent process doesn't have read access to it.",
          try_this:
            "Re-run with credentials that can read this path, or relax the file's permissions.",
          failed_request: "read_file('/etc/secrets/api-key.conf')",
        },
      },
    },
  },

  // 13. GitHub connector error — no structured diagnostic, only a plain
  //     `error` string. Exercises the GitHub classifier fallback path.
  {
    label: "Error (404 PRs)",
    call: {
      id: "call_prs_error",
      name: "github_list_pull_requests",
      args: { repository: "openai/missing-repo" },
    },
    envelope: {
      artifact_id: "artifact_prs_error",
      tool_name: "github_list_pull_requests",
      tool_call_id: "call_prs_error",
      ok: false,
      result_format: "json",
      result: {
        error:
          "404 Not Found: repository openai/missing-repo is unavailable or you do not have permission to access it",
        ...timing(1840, 280),
      },
      result_metadata: meta("pull_request_list"),
    },
  },

  // 16. BigQuery — running query. Envelope carries an async query handle, no
  //     rows yet. Inspector should render the unified header + Query zone +
  //     RunningResponse panel (progress bar, elapsed, native job link, cancel
  //     affordance, "Results will appear here" message — never raw JSON).
  (() => {
    const startedAtMs = now - 12_400;
    const sql = [
      '-- Monthly active customers, with revenue and last-touch channel,',
      '-- for the trailing 90-day window. Long enough to land in async land.',
      'WITH last_touch AS (',
      '  SELECT',
      '    customer_id,',
      '    ARRAY_AGG(traffic_source.channel ORDER BY session_start DESC LIMIT 1)[OFFSET(0)] AS channel',
      '  FROM `analytics-prod.events.sessions_daily`',
      '  WHERE session_start >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)',
      '  GROUP BY customer_id',
      ')',
      'SELECT',
      '  c.customer_id,',
      '  c.signup_country,',
      '  l.channel,',
      '  SUM(o.revenue_usd) AS revenue_usd,',
      '  COUNT(DISTINCT o.order_id) AS orders',
      'FROM `analytics-prod.crm.customers` c',
      'JOIN last_touch l USING (customer_id)',
      'JOIN `analytics-prod.commerce.orders` o USING (customer_id)',
      'WHERE o.created_at >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 90 DAY)',
      'GROUP BY 1, 2, 3',
      'ORDER BY revenue_usd DESC',
      'LIMIT 50000',
    ].join('\n');
    return {
      label: 'BigQuery running',
      call: {
        id: 'call_bq_running',
        name: 'run_bigquery_query',
        args: {
          sql,
          description:
            'Monthly active customers with revenue and last-touch channel, 90-day window.',
        },
      },
      envelope: {
        artifact_id: 'artifact_bq_running',
        tool_name: 'run_bigquery_query',
        tool_call_id: 'call_bq_running',
        ok: true,
        result_format: 'json',
        result: {
          status: 'running',
          job_id: 'bquxjob_5b71d8af_198d3380',
          provider: 'bigquery',
          sql_preview: 'WITH last_touch AS (SELECT customer_id, …) …',
          _uiStartedAt: startedAtMs,
        },
        query_metadata: {
          provider: 'bigquery',
          duration_ms: 0,
          job_id: 'bquxjob_5b71d8af_198d3380',
          provider_job_id: 'bquxjob_5b71d8af_198d3380',
          provider_job_url:
            'https://console.cloud.google.com/bigquery?project=analytics-prod&j=bq:US:bquxjob_5b71d8af_198d3380&page=queryresults',
          project_id: 'analytics-prod',
          job_location: 'US',
          progress_percent: 47,
          description:
            'Monthly active customers with revenue and last-touch channel, 90-day window.',
          status: 'running',
        },
        result_metadata: { integration: 'bigquery', product: 'bigquery' },
      },
    } as FixtureEntry;
  })(),

  // 17. Databricks — cancelled query. Lands at the post-cancellation resting
  //     state: status = 'cancelled' stamped on query_metadata, no rows, the
  //     same brand chrome as the running fixture but the response zone shows
  //     "Results unavailable — query was cancelled."
  (() => {
    const sql = [
      '-- Top 200 high-latency events by region in the last 24h.',
      'SELECT',
      '  region,',
      '  event_type,',
      '  device_id,',
      '  latency_ms,',
      '  sent_at',
      'FROM telemetry.events_raw',
      'WHERE sent_at >= current_timestamp() - INTERVAL 24 HOUR',
      '  AND latency_ms > 500',
      'ORDER BY latency_ms DESC',
      'LIMIT 200',
    ].join('\n');
    return {
      label: 'Databricks cancelled',
      call: {
        id: 'call_dbx_cancelled',
        name: 'run_databricks_query',
        args: {
          sql,
          description:
            'Top 200 high-latency telemetry events by region in the last 24h.',
        },
      },
      envelope: {
        artifact_id: 'artifact_dbx_cancelled',
        tool_name: 'run_databricks_query',
        tool_call_id: 'call_dbx_cancelled',
        ok: true,
        result_format: 'json',
        result: {
          // Cancelled queries have no rows — empty payload reflects the
          // post-cancel resting state without claiming a result format.
          rows: [],
          ...timing(360, 7400),
        },
        query_metadata: {
          provider: 'databricks',
          duration_ms: 7400,
          statement_id: '01HZK2X9KGB4PEVQNT3K5GAM7Q',
          provider_job_id: '01HZK2X9KGB4PEVQNT3K5GAM7Q',
          provider_job_url:
            'https://dbc-1234abcd-5678.cloud.databricks.com/sql/history?statementId=01HZK2X9KGB4PEVQNT3K5GAM7Q',
          host: 'dbc-1234abcd-5678.cloud.databricks.com',
          warehouse_id: 'a1b2c3d4e5f6g7h8',
          warehouse_name: 'streaming-prod',
          rows_returned: 0,
          row_count: 0,
          description:
            'Top 200 high-latency telemetry events by region in the last 24h.',
          status: 'cancelled',
        },
        result_metadata: { integration: 'databricks', product: 'databricks' },
      },
    } as FixtureEntry;
  })(),

  // 18. Slack — message history (success). Generic connector inspector:
  //     connector logo + Request args + an auto-table of messages + Metadata.
  {
    label: 'Slack messages',
    call: {
      id: 'call_slack_msgs',
      name: 'slack_get_messages',
      args: { channel_id: 'C04APP7TEAM', limit: 8 },
    },
    envelope: {
      artifact_id: 'artifact_slack_msgs',
      tool_name: 'slack_get_messages',
      tool_call_id: 'call_slack_msgs',
      ok: true,
      result_format: 'json',
      result: {
        messages: [
          { ts: '1717081200.001200', user: 'U02DEV01', text: 'Deploying streaming-prod v2.14 to canary now.', reply_count: 3 },
          { ts: '1717081320.004500', user: 'U02OPS22', text: 'Canary green — latency p95 down 18%.', reply_count: 0 },
          { ts: '1717081500.006700', user: 'U02DEV01', text: 'Promoting to 100%.', reply_count: 1 },
          { ts: '1717081980.009100', user: 'U02PM005', text: 'Nice. Updating the incident doc.', reply_count: 0 },
        ],
        has_more: true,
        response_metadata: { next_cursor: 'bmV4dF9wYWdlOjE3MTcwODE5ODA=' },
        ...timing(180, 540),
      },
      result_metadata: { integration: 'slack', product: 'slack', kind: 'message_history', readonly: true },
    },
  },

  // 19. Slack — post message (write / approval pending). Header shows the
  //     amber "Approval required" badge; Response features the pending action.
  {
    label: 'Slack post (approval)',
    call: {
      id: 'call_slack_post',
      name: 'slack_post_message',
      args: { channel_id: 'C04RELEASE', text: 'streaming-prod v2.14 is live at 100%. Latency p95 −18%.' },
    },
    envelope: {
      artifact_id: 'artifact_slack_post',
      tool_name: 'slack_post_message',
      tool_call_id: 'call_slack_post',
      ok: true,
      result_format: 'json',
      result: {
        approval: {
          id: 'apv_slack_9f2',
          title: 'Post message to #release-eng',
          action: 'post_message',
          target: { channel_id: 'C04RELEASE', channel_name: 'release-eng' },
          content: { text: 'streaming-prod v2.14 is live at 100%. Latency p95 −18%.' },
        },
        ...timing(60, 210),
      },
      result_metadata: { integration: 'slack', product: 'slack', kind: 'approval', widget: 'approval', readonly: false },
    },
  },

  // 20. Slack — missing-scope error. Routes to the generic ErrorRenderer, which
  //     now brands the header with the Slack logo + diagnostic banner.
  {
    label: 'Slack error (scope)',
    call: {
      id: 'call_slack_err',
      name: 'slack_post_message',
      args: { channel_id: 'C04RELEASE', text: 'ping' },
    },
    envelope: {
      artifact_id: 'artifact_slack_err',
      tool_name: 'slack_post_message',
      tool_call_id: 'call_slack_err',
      ok: false,
      result_format: 'json',
      result: {
        error:
          'Slack workspace "Acme" is missing required scope(s): chat:write. Reconnect Slack from Settings to grant it.',
        ...timing(40, 120),
      },
      result_metadata: {
        integration: 'slack',
        product: 'slack',
        diagnostic: {
          title: 'Missing OAuth scope',
          code: 'missing_scope',
          kind: 'permission',
          why: 'The connected Slack token was not granted chat:write, so it cannot post.',
          try_this: 'Reconnect Slack from Settings and approve the chat:write scope.',
        },
      },
    },
  },

  // 21. Jira — get issue (success). Mirrors the live bridge envelope
  // (server/internal/atlassian/bridge.mjs jiraGetIssue): the summarized issue
  // nested under `issue` with users as {displayName}, an ADF `description`
  // (+ plain-text preview), changelog, and atlassian result_metadata. The
  // ADF doc exercises the rich description rendering (marks, lists,
  // code block, panel) in the issue card.
  {
    label: 'Jira issue',
    call: {
      id: 'call_jira_issue',
      name: 'jira_get_issue',
      args: { issueKey: 'STREAM-4821' },
    },
    envelope: {
      artifact_id: 'artifact_jira_issue',
      tool_name: 'jira_get_issue',
      tool_call_id: 'call_jira_issue',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/issue/STREAM-4821',
        errors: [],
        issue: {
          id: '10421',
          key: 'STREAM-4821',
          url: 'https://acme.atlassian.net/browse/STREAM-4821',
          summary: 'Backpressure stalls ingestion when Kafka lag > 5m',
          status: 'In Progress',
          issueType: 'Bug',
          priority: 'High',
          assignee: { displayName: 'Dana Whitfield', accountId: '5f8a-dana', active: true },
          reporter: { displayName: 'Sam Ortega', accountId: '5d2b-sam', active: true },
          labels: ['streaming', 'reliability', 'sev2'],
          components: ['ingestion'],
          commentsCount: 7,
          created: '2026-05-28T09:04:00.000Z',
          updated: '2026-06-09T14:22:00.000Z',
          description: {
            type: 'doc',
            version: 1,
            content: [
              {
                type: 'paragraph',
                content: [
                  { type: 'text', text: 'When consumer lag exceeds ' },
                  { type: 'text', text: '5m', marks: [{ type: 'code' }] },
                  { type: 'text', text: ', the ingestion worker stops draining ' },
                  { type: 'text', text: 'entirely', marks: [{ type: 'strong' }] },
                  { type: 'text', text: ' instead of applying backpressure.' },
                ],
              },
              {
                type: 'heading',
                attrs: { level: 3 },
                content: [{ type: 'text', text: 'Steps to reproduce' }],
              },
              {
                type: 'orderedList',
                content: [
                  {
                    type: 'listItem',
                    content: [
                      {
                        type: 'paragraph',
                        content: [
                          { type: 'text', text: 'Pause the consumer group for six minutes.' },
                        ],
                      },
                    ],
                  },
                  {
                    type: 'listItem',
                    content: [
                      {
                        type: 'paragraph',
                        content: [
                          { type: 'text', text: 'Resume and watch ' },
                          { type: 'text', text: 'ingestion_rows_per_s', marks: [{ type: 'code' }] },
                          { type: 'text', text: ' stay at zero.' },
                        ],
                      },
                    ],
                  },
                ],
              },
              {
                type: 'codeBlock',
                attrs: { language: 'text' },
                content: [
                  {
                    type: 'text',
                    text: 'WARN BackpressureGovernor lag=312s threshold=300s action=HALT',
                  },
                ],
              },
              {
                type: 'panel',
                attrs: { panelType: 'warning' },
                content: [
                  {
                    type: 'paragraph',
                    content: [
                      { type: 'text', text: 'Rolling back ' },
                      {
                        type: 'text',
                        text: 'v2.41',
                        marks: [
                          {
                            type: 'link',
                            attrs: { href: 'https://github.com/acme/issue-tracker/releases/v2.41' },
                          },
                        ],
                      },
                      { type: 'text', text: ' clears the stall, so this blocks the release train.' },
                    ],
                  },
                ],
              },
            ],
          },
          descriptionText:
            'When consumer lag exceeds 5m, the ingestion worker stops draining entirely instead of applying backpressure.\nSteps to reproduce\nPause the consumer group for six minutes.\nResume and watch ingestion_rows_per_s stay at zero.\nWARN BackpressureGovernor lag=312s threshold=300s action=HALT\nRolling back v2.41 clears the stall, so this blocks the release train.',
        },
        changelog: [
          {
            created: '2026-06-09T14:22:00.000Z',
            author: { displayName: 'Dana Whitfield' },
            items: [{ field: 'status', fromString: 'To Do', toString: 'In Progress' }],
          },
        ],
        rawIncluded: false,
        ...timing(300, 640),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'issue' },
    },
  },

  // 22. Jira — create issue (write / approval pending).
  {
    label: 'Jira create (approval)',
    call: {
      id: 'call_jira_create',
      name: 'jira_create_issue',
      args: { projectKey: 'STREAM', summary: 'Add backpressure metric to ingestion dashboard', issueType: 'Task' },
    },
    envelope: {
      artifact_id: 'artifact_jira_create',
      tool_name: 'jira_create_issue',
      tool_call_id: 'call_jira_create',
      ok: true,
      result_format: 'json',
      result: {
        approval: {
          id: 'apv_jira_71',
          title: 'Create issue in STREAM',
          action: 'create_issue',
          target: { projectKey: 'STREAM' },
          content: {
            summary: 'Add backpressure metric to ingestion dashboard',
            issueType: 'Task',
            description: 'Track Kafka consumer lag vs ingestion rate; alert at >5m.',
          },
        },
        ...timing(80, 300),
      },
      result_metadata: { integration: 'jira', product: 'jira', kind: 'approval', widget: 'approval', readonly: false },
    },
  },

  // 22.1 Atlassian — bridge status (widget `status`). Mirrors the bridge's
  // status payload: running/mode/autoLogin/browser plus the per-product
  // session map.
  {
    label: 'Atlassian status',
    call: { id: 'call_atl_status', name: 'atlassian_status', args: {} },
    envelope: {
      artifact_id: 'artifact_atl_status',
      tool_name: 'atlassian_status',
      tool_call_id: 'call_atl_status',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: null,
        url: null,
        errors: [],
        running: true,
        mode: 'hidden',
        hidden: true,
        title: 'Jira — Acme',
        autoLogin: { state: 'idle', message: 'Session healthy; no login required.', updatedAt: '2026-06-11T08:50:00.000Z' },
        browser: { provider: 'system', source: 'detected', name: 'Chrome', channel: 'stable' },
        products: {
          jira: {
            product: 'jira',
            connected: true,
            state: 'connected',
            status: 200,
            url: 'https://acme.atlassian.net',
            title: 'Jira — Acme',
            profile: { emailAddress: 'dana@acme.io' },
            checkedAt: '2026-06-11T08:49:30.000Z',
          },
          confluence: {
            product: 'confluence',
            connected: false,
            state: 'not_connected',
            status: 401,
            url: 'https://acme.atlassian.net/wiki',
            title: 'Confluence — Acme',
            checkedAt: '2026-06-11T08:49:32.000Z',
            error: 'Login required — session cookie expired.',
          },
        },
        ...timing(95, 240),
      },
      result_metadata: { integration: 'atlassian', product: 'atlassian', widget: 'status' },
    },
  },

  // 22.2 Atlassian — check connection (widget `connection`).
  {
    label: 'Atlassian connection',
    call: { id: 'call_atl_conn', name: 'atlassian_check_connection', args: { product: 'jira' } },
    envelope: {
      artifact_id: 'artifact_atl_conn',
      tool_name: 'atlassian_check_connection',
      tool_call_id: 'call_atl_conn',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net',
        errors: [],
        checked: true,
        message: 'Jira reachable as dana@acme.io.',
        products: {
          jira: {
            product: 'jira',
            connected: true,
            state: 'connected',
            status: 200,
            url: 'https://acme.atlassian.net',
            title: 'Jira — Acme',
            profile: { emailAddress: 'dana@acme.io' },
            checkedAt: '2026-06-11T08:52:00.000Z',
          },
        },
        ...timing(90, 410),
      },
      result_metadata: { integration: 'atlassian', product: 'atlassian', widget: 'connection' },
    },
  },

  // 22.3 Jira — issue history (widget `history`): sprint transitions timeline,
  // current/previous sprint, changelog.
  {
    label: 'Jira history',
    call: { id: 'call_jira_history', name: 'jira_get_issue_history', args: { issueKey: 'STREAM-4821' } },
    envelope: {
      artifact_id: 'artifact_jira_history',
      tool_name: 'jira_get_issue_history',
      tool_call_id: 'call_jira_history',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/issue/STREAM-4821?expand=changelog',
        errors: [],
        issue: {
          id: '10421',
          key: 'STREAM-4821',
          url: 'https://acme.atlassian.net/browse/STREAM-4821',
          summary: 'Backpressure stalls ingestion when Kafka lag > 5m',
          status: 'In Progress',
          issueType: 'Bug',
          priority: 'High',
        },
        sprintTransitions: [
          { changedAt: '2026-05-29T10:00:00.000Z', toSprintId: '41', toSprintName: 'Stream Sprint 41' },
          {
            changedAt: '2026-06-09T09:30:00.000Z',
            fromSprintId: '41',
            fromSprintName: 'Stream Sprint 41',
            toSprintId: '42',
            toSprintName: 'Stream Sprint 42',
          },
        ],
        previousSprint: { id: '41', name: 'Stream Sprint 41' },
        currentSprint: { id: '42', name: 'Stream Sprint 42' },
        changelog: [
          {
            created: '2026-06-09T14:22:00.000Z',
            author: { displayName: 'Dana Whitfield' },
            items: [{ field: 'status', fromString: 'To Do', toString: 'In Progress' }],
          },
          {
            created: '2026-06-09T09:30:00.000Z',
            author: { displayName: 'Sam Ortega' },
            items: [{ field: 'Sprint', fromString: 'Stream Sprint 41', toString: 'Stream Sprint 42' }],
          },
        ],
        ...timing(280, 720),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'history' },
    },
  },

  // 22.4 Jira — issue comments (widget `comment_list`): thread with a reply
  // (parentCommentId) and an edited comment (updated > created).
  {
    label: 'Jira comments',
    call: {
      id: 'call_jira_comments',
      name: 'jira_get_issue_comments',
      args: { issueKey: 'STREAM-4821', maxResults: 3, orderBy: '-created' },
    },
    envelope: {
      artifact_id: 'artifact_jira_comments',
      tool_name: 'jira_get_issue_comments',
      tool_call_id: 'call_jira_comments',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/issue/STREAM-4821/comment',
        errors: [],
        issueKey: 'STREAM-4821',
        comments: [
          {
            id: '54021',
            issueKey: 'STREAM-4821',
            url: 'https://acme.atlassian.net/browse/STREAM-4821?focusedCommentId=54021',
            author: { displayName: 'Dana Whitfield', emailAddress: 'dana@acme.io', accountId: '5f8a-dana', active: true },
            created: '2026-06-10T16:40:00.000Z',
            updated: '2026-06-10T16:40:00.000Z',
            bodyText:
              'Bisected to the governor refactor in v2.41 — the HALT branch never re-arms once lag drops below the threshold. Draft fix re-checks lag on every poll tick.',
            renderedText: '',
          },
          {
            id: '54018',
            issueKey: 'STREAM-4821',
            url: 'https://acme.atlassian.net/browse/STREAM-4821?focusedCommentId=54018',
            parentCommentId: '54002',
            parentUrl: 'https://acme.atlassian.net/browse/STREAM-4821?focusedCommentId=54002',
            author: { displayName: 'Sam Ortega', emailAddress: 'sam@acme.io', accountId: '5d2b-sam', active: true },
            updateAuthor: { displayName: 'Sam Ortega', emailAddress: 'sam@acme.io', accountId: '5d2b-sam', active: true },
            created: '2026-06-10T11:05:00.000Z',
            updated: '2026-06-10T11:32:00.000Z',
            bodyText: 'Confirmed on staging — replayed the six-minute pause and ingestion never resumed until restart.',
            renderedText: '',
          },
          {
            id: '54002',
            issueKey: 'STREAM-4821',
            url: 'https://acme.atlassian.net/browse/STREAM-4821?focusedCommentId=54002',
            author: { displayName: 'Priya Nair', emailAddress: 'priya@acme.io', accountId: '6a1c-priya', active: true },
            created: '2026-06-09T15:10:00.000Z',
            updated: '2026-06-09T15:10:00.000Z',
            bodyText: 'Can someone try to reproduce outside of the release-train branch? Suspect this is v2.41-only.',
            renderedText: '',
          },
        ],
        total: 7,
        startAt: 0,
        maxResults: 3,
        orderBy: '-created',
        ...timing(260, 530),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'comment_list' },
    },
  },

  // 22.5 Jira — issue attachments (widget `attachment_list`).
  {
    label: 'Jira attachments',
    call: { id: 'call_jira_attachments', name: 'jira_get_issue_attachments', args: { issueKey: 'STREAM-4821' } },
    envelope: {
      artifact_id: 'artifact_jira_attachments',
      tool_name: 'jira_get_issue_attachments',
      tool_call_id: 'call_jira_attachments',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/issue/STREAM-4821?fields=attachment',
        errors: [],
        issue: {
          id: '10421',
          key: 'STREAM-4821',
          url: 'https://acme.atlassian.net/browse/STREAM-4821',
          summary: 'Backpressure stalls ingestion when Kafka lag > 5m',
        },
        issueKey: 'STREAM-4821',
        attachments: [
          {
            id: '88101',
            fileName: 'governor.log',
            author: { displayName: 'Sam Ortega', accountId: '5d2b-sam', active: true },
            created: '2026-06-10T11:00:00.000Z',
            sizeBytes: 18230,
            mimeType: 'text/plain',
            contentUrl: 'https://acme.atlassian.net/rest/api/3/attachment/content/88101',
            thumbnailUrl: '',
          },
          {
            id: '88102',
            fileName: 'stall-graph.png',
            author: { displayName: 'Dana Whitfield', accountId: '5f8a-dana', active: true },
            created: '2026-06-10T16:20:00.000Z',
            sizeBytes: 482113,
            mimeType: 'image/png',
            contentUrl: 'https://acme.atlassian.net/rest/api/3/attachment/content/88102',
            thumbnailUrl: 'https://acme.atlassian.net/rest/api/3/attachment/thumbnail/88102',
          },
          {
            id: '88103',
            fileName: 'heap-dump.bin',
            author: { displayName: 'Sam Ortega', accountId: '5d2b-sam', active: true },
            created: '2026-06-10T11:02:00.000Z',
            sizeBytes: 73400320,
            mimeType: 'application/octet-stream',
            contentUrl: 'https://acme.atlassian.net/rest/api/3/attachment/content/88103',
            thumbnailUrl: '',
          },
        ],
        total: 3,
        ...timing(240, 480),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'attachment_list' },
    },
  },

  // 22.6 Jira — read attachment, text content (widget `attachment`), truncated.
  {
    label: 'Jira attachment (text)',
    call: {
      id: 'call_jira_read_att',
      name: 'jira_read_attachment',
      args: { issueKey: 'STREAM-4821', attachmentId: '88101' },
    },
    envelope: {
      artifact_id: 'artifact_jira_read_att',
      tool_name: 'jira_read_attachment',
      tool_call_id: 'call_jira_read_att',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/attachment/content/88101',
        errors: [],
        issueKey: 'STREAM-4821',
        attachmentId: '88101',
        fileName: 'governor.log',
        mimeType: 'text/plain',
        sizeBytes: 18230,
        bytesReturned: 4096,
        truncated: true,
        binary: false,
        content:
          'WARN BackpressureGovernor lag=312s threshold=300s action=HALT\nWARN BackpressureGovernor lag=355s threshold=300s action=HALT\nINFO ConsumerGroup stream-ingest rebalanced (3 members)\nWARN BackpressureGovernor lag=388s threshold=300s action=HALT\nERROR IngestWorker drain stalled for 360s, queue depth 12480',
        localPath: '/Users/dana/.dbstream/downloads/atlassian/STREAM-4821/governor.log',
        localRelativePath: 'atlassian/STREAM-4821/governor.log',
        downloadDirectory: '/Users/dana/.dbstream/downloads',
        sha256: '9f2c1a7e44b0d8a3c6e5f1b2a9d4c7e8f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5',
        downloadMethod: 'browser_context_request',
        ...timing(210, 950),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'attachment' },
    },
  },

  // 22.7 Jira — read attachment, binary image routed to native image input
  // (widget `attachment`): note + download provenance, no content body.
  {
    label: 'Jira attachment (image)',
    call: {
      id: 'call_jira_read_img',
      name: 'jira_read_attachment',
      args: { issueKey: 'STREAM-4821', attachmentId: '88102', includeBase64: true },
    },
    envelope: {
      artifact_id: 'artifact_jira_read_img',
      tool_name: 'jira_read_attachment',
      tool_call_id: 'call_jira_read_img',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/attachment/content/88102',
        errors: [],
        issueKey: 'STREAM-4821',
        attachmentId: '88102',
        fileName: 'stall-graph.png',
        mimeType: 'image/png',
        sizeBytes: 482113,
        bytesReturned: 482113,
        truncated: false,
        binary: true,
        native_image_input: true,
        contentBase64Omitted: true,
        note: 'Image attachment was provided to the model as a native image input.',
        download: {
          local_path: '/Users/dana/.dbstream/downloads/atlassian/STREAM-4821/stall-graph.png',
          local_relative_path: 'atlassian/STREAM-4821/stall-graph.png',
          directory: '/Users/dana/.dbstream/downloads',
          file_name: 'stall-graph.png',
          sha256: 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2',
          size_bytes: 482113,
          source: 'atlassian_managed_download',
        },
        sha256: 'a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2',
        downloadMethod: 'browser_context_request',
        ...timing(190, 1240),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'attachment' },
    },
  },

  // 22.8 Jira — issue link types (widget `issue_link_types`).
  {
    label: 'Jira link types',
    call: { id: 'call_jira_link_types', name: 'jira_get_issue_link_types', args: {} },
    envelope: {
      artifact_id: 'artifact_jira_link_types',
      tool_name: 'jira_get_issue_link_types',
      tool_call_id: 'call_jira_link_types',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/issueLinkType',
        errors: [],
        issueLinkTypes: [
          { id: '10000', name: 'Blocks', inward: 'is blocked by', outward: 'blocks', self: 'https://acme.atlassian.net/rest/api/3/issueLinkType/10000' },
          { id: '10001', name: 'Cloners', inward: 'is cloned by', outward: 'clones', self: 'https://acme.atlassian.net/rest/api/3/issueLinkType/10001' },
          { id: '10002', name: 'Duplicate', inward: 'is duplicated by', outward: 'duplicates', self: 'https://acme.atlassian.net/rest/api/3/issueLinkType/10002' },
          { id: '10003', name: 'Relates', inward: 'relates to', outward: 'relates to', self: 'https://acme.atlassian.net/rest/api/3/issueLinkType/10003' },
        ],
        ...timing(170, 220),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'issue_link_types' },
    },
  },

  // 22.9 Jira — find users (widget `user_list`): accountIds with copy
  // affordance, inactive + app-account variants.
  {
    label: 'Jira users',
    call: { id: 'call_jira_users', name: 'jira_find_users', args: { query: 'dana' } },
    envelope: {
      artifact_id: 'artifact_jira_users',
      tool_name: 'jira_find_users',
      tool_call_id: 'call_jira_users',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/user/search?query=dana',
        errors: [],
        query: 'dana',
        users: [
          { displayName: 'Dana Whitfield', emailAddress: 'dana@acme.io', accountId: '5f8a-dana-9921-aa01', active: true, accountType: 'atlassian' },
          { displayName: 'Dana Brooks', emailAddress: 'dbrooks@acme.io', accountId: '7c2e-danab-3310-bb44', active: false, accountType: 'atlassian' },
          { displayName: 'Dana Bot', emailAddress: '', accountId: '9e0f-danabot-7755-cc12', active: true, accountType: 'app' },
        ],
        total: 3,
        ...timing(150, 380),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'user_list' },
    },
  },

  // 22.10 Jira — JQL search (widget `issue_list`): paged result with totals.
  {
    label: 'Jira search',
    call: {
      id: 'call_jira_search',
      name: 'jira_search_issues',
      args: { jql: 'project = STREAM AND statusCategory != Done ORDER BY priority DESC', maxResults: 4 },
    },
    envelope: {
      artifact_id: 'artifact_jira_search',
      tool_name: 'jira_search_issues',
      tool_call_id: 'call_jira_search',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url:
          'https://acme.atlassian.net/rest/api/3/search/jql?jql=project%20%3D%20STREAM%20AND%20statusCategory%20%21%3D%20Done%20ORDER%20BY%20priority%20DESC' +
          '&maxResults=25&fields=summary%2Cstatus%2Cissuetype%2Cassignee%2Creporter%2Ccreated%2Cupdated%2Cparent%2Ccomponents%2Clabels%2Ccustomfield_10020',
        errors: [],
        jql: 'project = STREAM AND statusCategory != Done ORDER BY priority DESC',
        issueKeys: ['STREAM-4821', 'STREAM-4906', 'STREAM-4790', 'STREAM-4633'],
        issues: [
          {
            id: '10421',
            key: 'STREAM-4821',
            url: 'https://acme.atlassian.net/browse/STREAM-4821',
            summary: 'Backpressure stalls ingestion when Kafka lag > 5m',
            status: 'In Progress',
            issueType: 'Bug',
            priority: 'High',
            assignee: { displayName: 'Dana Whitfield', accountId: '5f8a-dana', active: true },
            labels: ['streaming', 'reliability', 'sev2', 'release-train'],
            components: ['ingestion'],
            updated: '2026-06-09T14:22:00.000Z',
          },
          {
            id: '10502',
            key: 'STREAM-4906',
            url: 'https://acme.atlassian.net/browse/STREAM-4906',
            summary: 'Snapshot uploads fail when bucket region differs from cluster region',
            status: 'Blocked',
            issueType: 'Bug',
            priority: 'Highest',
            assignee: { displayName: 'Priya Nair', accountId: '6a1c-priya', active: true },
            labels: ['storage'],
            components: ['snapshot'],
            updated: '2026-06-10T08:15:00.000Z',
          },
          {
            id: '10390',
            key: 'STREAM-4790',
            url: 'https://acme.atlassian.net/browse/STREAM-4790',
            summary: 'Expose per-partition lag in the metrics endpoint',
            status: 'To Do',
            issueType: 'Story',
            priority: 'Medium',
            labels: ['observability'],
            components: ['metrics'],
            updated: '2026-06-05T10:00:00.000Z',
          },
          {
            id: '10233',
            key: 'STREAM-4633',
            url: 'https://acme.atlassian.net/browse/STREAM-4633',
            summary: 'Epic: graceful degradation under sustained consumer lag',
            status: 'In Review',
            issueType: 'Epic',
            priority: 'Low',
            assignee: { displayName: 'Sam Ortega', accountId: '5d2b-sam', active: true },
            labels: [],
            components: ['ingestion', 'metrics'],
            updated: '2026-06-08T17:45:00.000Z',
          },
        ],
        total: 28,
        startAt: 0,
        maxResults: 4,
        ...timing(140, 860),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'issue_list' },
    },
  },

  // 22.11 Jira — find across projects/boards/issues (widget `find`).
  {
    label: 'Jira find',
    call: { id: 'call_jira_find', name: 'jira_find', args: { query: 'backpressure', type: 'all' } },
    envelope: {
      artifact_id: 'artifact_jira_find',
      tool_name: 'jira_find',
      tool_call_id: 'call_jira_find',
      ok: true,
      result_format: 'json',
      result: {
        type: 'all',
        query: 'backpressure',
        projectOrBoard: '',
        projects: {
          ok: true,
          status: 200,
          url: 'https://acme.atlassian.net/rest/api/3/project/search?query=backpressure',
          errors: [],
          query: 'backpressure',
          candidates: [
            { id: '10001', key: 'STREAM', name: 'Issue Tracker', projectTypeKey: 'software', simplified: false },
          ],
          total: 1,
        },
        boards: {
          ok: true,
          status: 200,
          url: 'https://acme.atlassian.net/rest/agile/1.0/board?name=backpressure',
          errors: [],
          query: 'backpressure',
          candidates: [
            {
              id: 12,
              name: 'Streaming Delivery',
              type: 'scrum',
              self: 'https://acme.atlassian.net/rest/agile/1.0/board/12',
              projectKey: 'STREAM',
              projectId: 10001,
              projectName: 'Issue Tracker',
            },
          ],
        },
        issues: {
          ok: true,
          status: 200,
          url: 'https://acme.atlassian.net/rest/api/3/search/jql',
          errors: [],
          jql: 'text ~ "backpressure" ORDER BY updated DESC',
          issueKeys: ['STREAM-4821', 'STREAM-4633'],
          issues: [
            {
              id: '10421',
              key: 'STREAM-4821',
              url: 'https://acme.atlassian.net/browse/STREAM-4821',
              summary: 'Backpressure stalls ingestion when Kafka lag > 5m',
              status: 'In Progress',
              issueType: 'Bug',
              priority: 'High',
              assignee: { displayName: 'Dana Whitfield', accountId: '5f8a-dana', active: true },
              updated: '2026-06-09T14:22:00.000Z',
            },
            {
              id: '10233',
              key: 'STREAM-4633',
              url: 'https://acme.atlassian.net/browse/STREAM-4633',
              summary: 'Epic: graceful degradation under sustained consumer lag',
              status: 'In Review',
              issueType: 'Epic',
              priority: 'Low',
              updated: '2026-06-08T17:45:00.000Z',
            },
          ],
          total: 2,
          startAt: 0,
          maxResults: 25,
        },
        ...timing(130, 1380),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'find' },
    },
  },

  // 22.12 Jira — raw REST GET (widget `rest`): method/path/status line +
  // structured JSON body through the values engine.
  {
    label: 'Jira REST',
    call: {
      id: 'call_jira_rest',
      name: 'jira_rest_get',
      args: { path: '/rest/api/3/issue/STREAM-4821/worklog' },
    },
    envelope: {
      artifact_id: 'artifact_jira_rest',
      tool_name: 'jira_rest_get',
      tool_call_id: 'call_jira_rest',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 200,
        url: 'https://acme.atlassian.net/rest/api/3/issue/STREAM-4821/worklog',
        errors: [],
        json: {
          startAt: 0,
          maxResults: 20,
          total: 2,
          worklogs: [
            { id: '301', timeSpent: '2h', started: '2026-06-09T09:00:00.000Z', author: { displayName: 'Dana Whitfield' } },
            { id: '302', timeSpent: '45m', started: '2026-06-10T15:00:00.000Z', author: { displayName: 'Sam Ortega' } },
          ],
        },
        text: '{"startAt":0,"maxResults":20,"total":2,"worklogs":[{"id":"301","timeSpent":"2h"…',
        ...timing(120, 540),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'rest' },
    },
  },

  // 22.13 Jira — update issue (write / approval pending): the live Go envelope
  // with snake_case content, markdown source body, mentions and field_names.
  {
    label: 'Jira update (approval)',
    call: {
      id: 'call_jira_update',
      name: 'jira_update_issue',
      args: { issueKey: 'STREAM-4821', priorityName: 'Highest', labels: ['streaming', 'reliability', 'sev1'] },
    },
    envelope: {
      artifact_id: 'artifact_jira_update',
      tool_name: 'jira_update_issue',
      tool_call_id: 'call_jira_update',
      ok: true,
      result_format: 'json',
      result: {
        approval: {
          id: 'apv_jira_88',
          title: 'Update Jira issue',
          detail: 'STREAM-4821: description, labels, priority',
          tool_name: 'jira_update_issue',
          product: 'jira',
          action: 'update_issue',
          target: { type: 'issue', issue_key: 'STREAM-4821' },
          content: {
            description:
              'Escalating to **sev1** — the stall now reproduces on `main` too.\n\n- Governor never re-arms after HALT\n- Restart is the only recovery\n\ncc [~accountid:5d2b-sam]',
            priority_name: 'Highest',
            labels: ['streaming', 'reliability', 'sev1'],
            field_names: ['description', 'labels', 'priority'],
            changes: 'description, labels, priority',
            mentions: [{ accountId: '5d2b-sam', displayName: 'Sam Ortega' }],
            source_format: 'markdown',
            target_format: 'jira_adf',
          },
        },
        ...timing(70, 260),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', kind: 'approval', widget: 'approval', readonly: false },
    },
  },

  // 22.14 Jira — add comment executed (widget `write_result`).
  {
    label: 'Jira comment (executed)',
    call: {
      id: 'call_jira_comment_exec',
      name: 'jira_add_comment_execute',
      args: { approvalId: 'apv_jira_84' },
    },
    envelope: {
      artifact_id: 'artifact_jira_comment_exec',
      tool_name: 'jira_add_comment_execute',
      tool_call_id: 'call_jira_comment_exec',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 201,
        url: 'https://acme.atlassian.net/rest/api/3/issue/STREAM-4821/comment',
        errors: [],
        issueKey: 'STREAM-4821',
        comment:
          'Root cause confirmed: the HALT branch never re-arms. Fix in review — see the linked PR. cc @Sam Ortega',
        commentId: '54033',
        commentUrl: 'https://acme.atlassian.net/browse/STREAM-4821?focusedCommentId=54033',
        replyToCommentId: '54021',
        replyToUrl: 'https://acme.atlassian.net/browse/STREAM-4821?focusedCommentId=54021',
        mentions: [{ accountId: '5d2b-sam', displayName: 'Sam Ortega' }],
        bodyFormat: 'jira_adf',
        ...timing(60, 740),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'write_result' },
    },
  },

  // 22.15 Jira — create issue executed (widget `write_result`).
  {
    label: 'Jira create (executed)',
    call: {
      id: 'call_jira_create_exec',
      name: 'jira_create_issue_execute',
      args: { approvalId: 'apv_jira_71' },
    },
    envelope: {
      artifact_id: 'artifact_jira_create_exec',
      tool_name: 'jira_create_issue_execute',
      tool_call_id: 'call_jira_create_exec',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        status: 201,
        url: 'https://acme.atlassian.net/rest/api/3/issue',
        errors: [],
        projectKey: 'STREAM',
        issueKey: 'STREAM-4977',
        issueId: '10688',
        issueUrl: 'https://acme.atlassian.net/browse/STREAM-4977',
        summary: 'Add backpressure metric to ingestion dashboard',
        issueType: 'Task',
        description: 'Track Kafka consumer lag vs ingestion rate; alert at >5m.',
        components: ['metrics'],
        mentions: [],
        bodyFormat: 'jira_adf',
        ...timing(50, 880),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'write_result' },
    },
  },

  // 22.16 Jira — related tickets executed with a partial failure
  // (widget `write_result`): per-link pills, failed link keeps red error text.
  {
    label: 'Jira links (partial)',
    call: {
      id: 'call_jira_related_exec',
      name: 'jira_add_related_tickets_execute',
      args: { approvalId: 'apv_jira_90' },
    },
    envelope: {
      artifact_id: 'artifact_jira_related_exec',
      tool_name: 'jira_add_related_tickets_execute',
      tool_call_id: 'call_jira_related_exec',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        issueKey: 'STREAM-4821',
        relatedIssueKeys: ['STREAM-4633', 'STREAM-9999'],
        relationship: 'blocks',
        links: [
          {
            issueKey: 'STREAM-4821',
            relatedIssueKey: 'STREAM-4633',
            relationship: 'blocks',
            ok: true,
            status: 201,
            url: 'https://acme.atlassian.net/rest/api/3/issueLink',
            errors: [],
          },
          {
            issueKey: 'STREAM-4821',
            relatedIssueKey: 'STREAM-9999',
            relationship: 'blocks',
            ok: false,
            status: 404,
            url: 'https://acme.atlassian.net/rest/api/3/issueLink',
            errors: ['Issue STREAM-9999 does not exist or you do not have permission to see it.'],
          },
        ],
        errors: ['1 of 2 links failed'],
        ...timing(40, 690),
      },
      result_metadata: { integration: 'atlassian', product: 'jira', widget: 'write_result' },
    },
  },

  // 23. SharePoint — search results through the generic connector inspector.
  {
    label: 'SharePoint search',
    call: {
      id: 'call_sharepoint_search',
      name: 'sharepoint_search',
      args: { query: 'Q3 roadmap', row_limit: 3, file_extensions: ['docx', 'md'] },
    },
    envelope: {
      artifact_id: 'artifact_sharepoint_search',
      tool_name: 'sharepoint_search',
      tool_call_id: 'call_sharepoint_search',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        query: 'Q3 roadmap',
        count: 3,
        files: [
          {
            name: 'Q3 Roadmap.md',
            title: 'Q3 Roadmap',
            web_url: 'https://acme.sharepoint.com/sites/Product/Shared%20Documents/Q3%20Roadmap.md',
            site_url: 'https://acme.sharepoint.com/sites/Product',
            server_relative_url: '/sites/Product/Shared Documents/Q3 Roadmap.md',
            size_bytes: 18432,
            last_modified: new Date(now - 3 * 60 * 60_000).toISOString(),
            snippet: 'Launch sequencing, adoption targets, and shared-risk milestones for Q3.',
          },
          {
            name: 'Q3 GTM Pack.docx',
            title: 'Q3 GTM Pack',
            web_url: 'https://acme.sharepoint.com/sites/Product/Shared%20Documents/Q3%20GTM%20Pack.docx',
            site_url: 'https://acme.sharepoint.com/sites/Product',
            server_relative_url: '/sites/Product/Shared Documents/Q3 GTM Pack.docx',
            size_bytes: 241664,
            last_modified: new Date(now - 26 * 60 * 60_000).toISOString(),
            snippet: 'GTM checklist with launch owners, partner dependencies, and enablement dates.',
          },
          {
            name: 'Roadmap risks.md',
            title: 'Roadmap risks',
            web_url: 'https://acme.sharepoint.com/sites/Product/Shared%20Documents/Roadmap%20risks.md',
            site_url: 'https://acme.sharepoint.com/sites/Product',
            server_relative_url: '/sites/Product/Shared Documents/Roadmap risks.md',
            size_bytes: 9216,
            last_modified: new Date(now - 2 * 24 * 60 * 60_000).toISOString(),
            snippet: 'Risk register for launch blockers and mitigation owners.',
          },
        ],
        ...timing(100, 760),
      },
      result_metadata: { integration: 'sharepoint', product: 'sharepoint', widget: 'search_results' },
    },
  },

  // 24. SharePoint — remote file promoted into the native file viewer.
  {
    label: 'SharePoint read',
    call: {
      id: 'call_sharepoint_read',
      name: 'sharepoint_read',
      args: {
        item_url: 'https://acme.sharepoint.com/sites/Product/Shared%20Documents/Q3%20Roadmap.md',
        max_chars: 100000,
      },
    },
    envelope: {
      artifact_id: 'artifact_sharepoint_read',
      tool_name: 'sharepoint_read',
      tool_call_id: 'call_sharepoint_read',
      ok: true,
      result_format: 'json',
      result: {
        ok: true,
        name: 'Q3 Roadmap.md',
        file_name: 'Q3 Roadmap.md',
        mime_type: 'text/markdown',
        size_bytes: 18432,
        local_path: '/app-managed/sharepoint/Q3 Roadmap.md',
        artifact_id: 'artifact_sharepoint_read',
        item_url: 'https://acme.sharepoint.com/sites/Product/Shared%20Documents/Q3%20Roadmap.md',
        site_url: 'https://acme.sharepoint.com/sites/Product',
        server_relative_url: '/sites/Product/Shared Documents/Q3 Roadmap.md',
        text_preview: [
          '# Q3 Roadmap',
          '',
          'Launch sequencing is split into three confidence gates.',
          '',
          '- Gate 1: finish migration readiness by July 15',
          '- Gate 2: validate partner dependencies by August 5',
          '- Gate 3: publish launch notes by September 12',
          '',
          'Shared risk: enterprise rollout depends on the approvals queue audit.',
        ].join('\n'),
        preview_truncated: false,
        ...timing(80, 1040),
      },
      result_metadata: {
        integration: 'sharepoint',
        product: 'sharepoint',
        widget: 'file',
        result_kind: 'file',
        native_viewer: 'file',
        renderer_strategy: 'artifact_type',
        renderer_key: 'file',
        artifact_types: ['file'],
        file_capability: 'remote_file_download',
        file_kinds: ['pdf', 'image', 'text', 'html', 'document', 'spreadsheet', 'presentation', 'archive'],
        preview_modes: ['raw_download', 'text_preview', 'pdf_canvas', 'image_raw', 'html_sandbox', 'spreadsheet_workbook', 'presentation_slides', 'download_only'],
        source_refs: ['item_url', 'server_relative_url', 'site_url', 'remote_id', 'artifact_id'],
        produces_file_artifact: true,
        promotes_native_input: true,
        native_links: ['source_url'],
        tool_presentation: {
          family: 'sharepoint',
          result_kind: 'file',
          renderer_strategy: 'artifact_type',
          renderer_key: 'file',
          native_viewer: 'file',
          artifact_types: ['file'],
          file_capability: 'remote_file_download',
          file_kinds: ['pdf', 'image', 'text', 'html', 'document', 'spreadsheet', 'presentation', 'archive'],
          preview_modes: ['raw_download', 'text_preview', 'pdf_canvas', 'image_raw', 'html_sandbox', 'spreadsheet_workbook', 'presentation_slides', 'download_only'],
          source_refs: ['item_url', 'server_relative_url', 'site_url', 'remote_id', 'artifact_id'],
          produces_file_artifact: true,
          promotes_native_input: true,
          native_links: ['source_url'],
        },
      },
    },
  },

  // 25. Browser/CUA — navigate (success). Object result → key/value grid with a
  //     clickable url + "Open in Browser" header link.
  {
    label: 'Browser navigate',
    call: {
      id: 'call_browser_nav',
      name: 'cua_browser_navigate',
      args: { url: 'https://status.example.com/incidents/2026-06', waitUntil: 'load' },
    },
    envelope: {
      artifact_id: 'artifact_browser_nav',
      tool_name: 'cua_browser_navigate',
      tool_call_id: 'call_browser_nav',
      ok: true,
      result_format: 'json',
      result: {
        url: 'https://status.example.com/incidents/2026-06',
        title: 'Incident report — June 2026',
        status: 200,
        ...timing(220, 880),
      },
      result_metadata: { integration: 'browser_control', product: 'browser', kind: 'navigate' },
    },
  },

  // 26. Browser/CUA — console + network logs (success). Auto-table of logs.
  {
    label: 'Browser logs',
    call: {
      id: 'call_browser_logs',
      name: 'cua_browser_logs',
      args: { failedOnly: false, limit: 50, order: 'asc' },
    },
    envelope: {
      artifact_id: 'artifact_browser_logs',
      tool_name: 'cua_browser_logs',
      tool_call_id: 'call_browser_logs',
      ok: true,
      result_format: 'json',
      result: {
        logs: [
          { sequence: 1, type: 'console', level: 'info', text: 'app: hydrated in 412ms', url: 'https://status.example.com/incidents/2026-06' },
          { sequence: 2, type: 'network', level: 'error', text: 'GET /api/incidents/4821 → 500', status: 500, url: 'https://status.example.com/api/incidents/4821' },
          { sequence: 3, type: 'console', level: 'warning', text: 'deprecated <marquee> used', url: 'https://status.example.com/incidents/2026-06' },
        ],
        count: 3,
        ...timing(60, 140),
      },
      result_metadata: { integration: 'browser_control', product: 'browser', kind: 'logs' },
    },
  },

  // 25. Trino — query (success). Routes to the PRESERVED QueryInspector via the
  //     SQL scoped-key map (provider:'trino').
  (() => {
    const rows = [
      { region: 'us-east-1', service: 'ingest', requests: 184204, p95_ms: 86, error_rate: 0.004 },
      { region: 'us-west-2', service: 'ingest', requests: 96011, p95_ms: 91, error_rate: 0.006 },
      { region: 'eu-west-1', service: 'flush', requests: 72213, p95_ms: 120, error_rate: 0.010 },
      { region: 'ap-south-1', service: 'compaction', requests: 41388, p95_ms: 142, error_rate: 0.013 },
      { region: 'us-east-1', service: 'snapshot', requests: 22904, p95_ms: 168, error_rate: 0.002 },
      { region: 'eu-west-1', service: 'rebalance', requests: 11820, p95_ms: 205, error_rate: 0.021 },
    ];
    const schema = [
      { name: 'region', type: 'varchar' },
      { name: 'service', type: 'varchar' },
      { name: 'requests', type: 'bigint' },
      { name: 'p95_ms', type: 'integer' },
      { name: 'error_rate', type: 'double' },
    ];
    const sql = [
      'SELECT region, service,',
      '  count(*) AS requests,',
      '  approx_percentile(latency_ms, 0.95) AS p95_ms,',
      '  avg(if(ok, 0, 1)) AS error_rate',
      'FROM hive.telemetry.events',
      'WHERE event_date = current_date',
      'GROUP BY 1, 2',
      'ORDER BY requests DESC',
    ].join('\n');
    return {
      label: 'Trino query',
      call: {
        id: 'call_trino_query',
        name: 'run_trino_query',
        args: { sql, description: 'Per-region service request volume and p95 latency for today.' },
      },
      envelope: {
        artifact_id: 'artifact_trino_query',
        tool_name: 'run_trino_query',
        tool_call_id: 'call_trino_query',
        ok: true,
        result_format: 'json',
        result: { rows, page_size: rows.length, offset: 0, has_more: false, ...timing(540, 920) },
        query_metadata: {
          provider: 'trino',
          duration_ms: 920,
          query_id: '20260610_142233_00042_ab12c',
          provider_job_id: '20260610_142233_00042_ab12c',
          provider_job_url: 'https://trino.acme.internal/ui/query.html?20260610_142233_00042_ab12c',
          query_url: 'https://trino.acme.internal/ui/query.html?20260610_142233_00042_ab12c',
          catalog: 'hive',
          schema: schema,
          rows_returned: rows.length,
          row_count: rows.length,
          status: 'completed',
          description: 'Per-region service request volume and p95 latency for today.',
        },
        result_metadata: { integration: 'trino', product: 'trino' },
      },
    } as FixtureEntry;
  })(),

  // 26. Snowflake — query (success). PRESERVED QueryInspector (provider:'snowflake').
  (() => {
    const rows = [
      { event_hour: '2026-06-10T10:00:00Z', warehouse: 'STREAMING_WH', credits_used: 3.42, queries: 1820, avg_ms: 240 },
      { event_hour: '2026-06-10T09:00:00Z', warehouse: 'STREAMING_WH', credits_used: 3.18, queries: 1714, avg_ms: 232 },
      { event_hour: '2026-06-10T08:00:00Z', warehouse: 'ETL_WH', credits_used: 5.91, queries: 402, avg_ms: 1180 },
      { event_hour: '2026-06-10T07:00:00Z', warehouse: 'STREAMING_WH', credits_used: 2.87, queries: 1559, avg_ms: 221 },
      { event_hour: '2026-06-10T06:00:00Z', warehouse: 'ETL_WH', credits_used: 6.42, queries: 388, avg_ms: 1320 },
    ];
    const schema = [
      { name: 'EVENT_HOUR', type: 'TIMESTAMP_NTZ' },
      { name: 'WAREHOUSE', type: 'TEXT' },
      { name: 'CREDITS_USED', type: 'NUMBER' },
      { name: 'QUERIES', type: 'NUMBER' },
      { name: 'AVG_MS', type: 'NUMBER' },
    ];
    const sql = [
      "SELECT DATE_TRUNC('hour', start_time) AS event_hour,",
      '  warehouse_name AS warehouse,',
      '  SUM(credits_used) AS credits_used,',
      '  COUNT(*) AS queries,',
      '  AVG(execution_time) AS avg_ms',
      'FROM snowflake.account_usage.query_history',
      "WHERE start_time >= DATEADD('day', -1, CURRENT_TIMESTAMP())",
      'GROUP BY 1, 2',
      'ORDER BY event_hour DESC',
    ].join('\n');
    return {
      label: 'Snowflake query',
      call: {
        id: 'call_snow_query',
        name: 'run_snowflake_query',
        args: { sql, description: 'Hourly warehouse credit + query volume for the last day.' },
      },
      envelope: {
        artifact_id: 'artifact_snow_query',
        tool_name: 'run_snowflake_query',
        tool_call_id: 'call_snow_query',
        ok: true,
        result_format: 'json',
        result: { rows, page_size: rows.length, offset: 0, has_more: false, ...timing(700, 1180) },
        query_metadata: {
          provider: 'snowflake',
          duration_ms: 1180,
          query_id: '01b3e7a2-0000-4f10-0000-000abc123456',
          provider_job_id: '01b3e7a2-0000-4f10-0000-000abc123456',
          provider_job_url: 'https://app.snowflake.com/acme/prod/compute/history/queries/01b3e7a2',
          query_url: 'https://app.snowflake.com/acme/prod/compute/history/queries/01b3e7a2',
          database: 'PROD',
          schema: schema,
          rows_returned: rows.length,
          row_count: rows.length,
          status: 'completed',
          description: 'Hourly warehouse credit + query volume for the last day.',
        },
        result_metadata: { integration: 'snowflake', product: 'snowflake' },
      },
    } as FixtureEntry;
  })(),

  // 27. Generic tool, no connector (web_search) — exercises the neutral
  //     ToolInspector fallback (wrench icon, auto-table of results).
  {
    label: 'Web search (generic)',
    call: {
      id: 'call_web_search',
      name: 'web_search',
      args: { query: 'kafka backpressure ingestion tuning', max_results: 5 },
    },
    envelope: {
      artifact_id: 'artifact_web_search',
      tool_name: 'web_search',
      tool_call_id: 'call_web_search',
      ok: true,
      result_format: 'json',
      result: {
        query: 'kafka backpressure ingestion tuning',
        results: [
          { title: 'Tuning Kafka consumer backpressure', url: 'https://kafka.apache.org/documentation/#design_backpressure', snippet: 'Backpressure occurs when consumers cannot keep up with producers…' },
          { title: 'Streaming ingestion patterns', url: 'https://www.confluent.io/blog/streaming-ingestion/', snippet: 'Strategies for handling lag spikes and flow control…' },
          { title: 'Credit-based flow control in distributed logs', url: 'https://example.dev/flow-control', snippet: 'A primer on credit-based flow control for log pipelines…' },
        ],
        ...timing(420, 1320),
      },
      result_metadata: {},
    },
  },
  {
    label: 'Provider hosted web search',
    call: {
      id: 'call_provider_hosted_web_search',
      name: 'web_search',
      args: { query: 'hosted retrieval audit path', max_results: 3 },
    },
    envelope: {
      artifact_id: 'artifact_provider_hosted_web_search',
      tool_name: 'web_search',
      tool_call_id: 'call_provider_hosted_web_search',
      ok: true,
      result_format: 'json',
      result: {
        query: 'hosted retrieval audit path',
        results: [
          {
            title: 'Hosted search audit sentinel',
            url: 'https://example.com/hosted-search-audit',
            snippet: 'Provider hosted web search result rendered through the audit inspector.',
          },
        ],
        ...timing(320, 780),
      },
      result_metadata: {
        provider_tool: true,
        provider_hosted_tool_id: 'anthropic-messages/web_search',
        adapter_family: 'anthropic-messages',
        settings_key: 'webSearch',
        category: 'read',
        tool_set_id: 'provider-hosted',
        tool_set_name: 'Model provider capabilities',
        origin: 'provider',
        permission_subject_kind: 'provider_hosted_tool',
        readonly: true,
        integration: 'provider-hosted',
        product: 'anthropic',
        kind: 'provider-hosted',
        tool_presentation: {
          family: 'provider-hosted',
          result_kind: 'connector',
          renderer_strategy: 'family',
          renderer_key: 'provider-hosted',
          native_viewer: 'connector_inspector',
        },
      },
    },
  },
  {
    label: 'Provider hosted code interpreter',
    call: {
      id: 'call_provider_hosted_code_interpreter',
      name: 'code_interpreter',
      args: { code: 'print("hosted code audit sentinel")' },
    },
    envelope: {
      artifact_id: 'artifact_provider_hosted_code_interpreter',
      tool_name: 'code_interpreter',
      tool_call_id: 'call_provider_hosted_code_interpreter',
      ok: true,
      result_format: 'json',
      result: {
        status: 'completed',
        stdout: 'hosted code audit sentinel',
        files: [{ name: 'hosted-output.txt', content: 'hosted code audit sentinel' }],
        container_id: 'resp-container-audit-1',
        ...timing(260, 1140),
      },
      result_metadata: {
        provider_tool: true,
        provider_hosted_tool_id: 'openai-responses/code_interpreter',
        adapter_family: 'openai-responses',
        settings_key: 'codeExecution',
        category: 'execute',
        tool_set_id: 'provider-hosted',
        tool_set_name: 'Model provider capabilities',
        origin: 'provider',
        permission_subject_kind: 'provider_hosted_tool',
        readonly: false,
        integration: 'provider-hosted',
        product: 'openai',
        kind: 'provider-hosted',
        tool_presentation: {
          family: 'provider-hosted',
          result_kind: 'connector',
          renderer_strategy: 'family',
          renderer_key: 'provider-hosted',
          native_viewer: 'connector_inspector',
        },
      },
    },
  },
];

const arxivEntries = [
  {
    arxiv_id: '2508.08077',
    version: 1,
    title: 'Agentic Search for Scientific Discovery',
    summary: 'We study agentic literature search systems that decompose research intents into iterative queries, screen retrieved papers, and cite evidence from abstracts and full text.',
    authors: ['Mira Shah', 'Leah Chen', 'Rafael Ortiz'],
    categories: ['cs.AI', 'cs.IR'],
    primary_category: 'cs.AI',
    published: '2025-08-11T00:00:00Z',
    updated: '2025-08-11T00:00:00Z',
    abs_url: 'https://arxiv.org/abs/2508.08077',
    pdf_url: 'https://arxiv.org/pdf/2508.08077',
    downloaded: false,
  },
  {
    arxiv_id: '2406.11231',
    version: 2,
    title: 'Auditing Retrieval-Augmented Language Models with Evidence Spans',
    summary: 'This paper proposes evidence-span audits for retrieval-augmented generation, separating deterministic retrieval provenance from model judgments about relevance.',
    authors: ['Noah Patel', 'Erin Walker'],
    categories: ['cs.CL', 'cs.IR'],
    primary_category: 'cs.CL',
    published: '2024-06-18T00:00:00Z',
    updated: '2024-10-02T00:00:00Z',
    abs_url: 'https://arxiv.org/abs/2406.11231',
    pdf_url: 'https://arxiv.org/pdf/2406.11231',
    downloaded: false,
  },
  {
    arxiv_id: '2401.00320',
    version: 1,
    title: 'Workflow Traces for Human-AI Research Collaboration',
    summary: 'We present a trace schema for human-AI research workflows, capturing task intent, tool parameters, retrieved identifiers, and post-hoc screening decisions.',
    authors: ['Sofia Nguyen', 'Ari Klein', 'Priya Menon'],
    categories: ['cs.HC', 'cs.AI'],
    primary_category: 'cs.HC',
    published: '2024-01-02T00:00:00Z',
    updated: '2024-01-02T00:00:00Z',
    abs_url: 'https://arxiv.org/abs/2401.00320',
    pdf_url: 'https://arxiv.org/pdf/2401.00320',
    downloaded: false,
  },
];

const arxivFixtures: FixtureEntry[] = [
  {
    label: 'arXiv search empty 1',
    call: {
      id: 'call_arxiv_empty_1',
      name: 'arxiv_search',
      args: { query: '"AI agent A/B search audit"', max_results: 3 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_empty_1',
      tool_name: 'arxiv_search',
      tool_call_id: 'call_arxiv_empty_1',
      ok: true,
      result_format: 'json',
      result: {
        query: '"AI agent A/B search audit"',
        total_results: 0,
        start: 0,
        items_per_page: 0,
        entries: [],
        ...timing(5200, 4),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'search_results' },
    },
  },
  {
    label: 'arXiv search empty 2',
    call: {
      id: 'call_arxiv_empty_2',
      name: 'arxiv_search',
      args: { query: 'ti:"human centred progressive discovery"', max_results: 3 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_empty_2',
      tool_name: 'arxiv_search',
      tool_call_id: 'call_arxiv_empty_2',
      ok: true,
      result_format: 'json',
      result: {
        query: 'ti:"human centred progressive discovery"',
        total_results: 0,
        start: 0,
        items_per_page: 0,
        entries: [],
        ...timing(5000, 3),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'search_results' },
    },
  },
  {
    label: 'arXiv search Q1',
    call: {
      id: 'call_arxiv_q1',
      name: 'arxiv_search',
      args: { query: 'agentic literature search evidence spans', max_results: 3 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_q1',
      tool_name: 'arxiv_search',
      tool_call_id: 'call_arxiv_q1',
      ok: true,
      result_format: 'json',
      result: {
        query: 'agentic literature search evidence spans',
        total_results: 34,
        start: 0,
        items_per_page: 3,
        entries: arxivEntries,
        ...timing(3600, 780),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'search_results' },
    },
  },
  {
    label: 'arXiv search Q2',
    call: {
      id: 'call_arxiv_q2',
      name: 'arxiv_search',
      args: { query: 'human AI research workflow trace schema', max_results: 3 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_q2',
      tool_name: 'arxiv_search',
      tool_call_id: 'call_arxiv_q2',
      ok: true,
      result_format: 'json',
      result: {
        query: 'human AI research workflow trace schema',
        total_results: 2,
        start: 0,
        items_per_page: 3,
        entries: [arxivEntries[2], arxivEntries[0]],
        ...timing(2500, 640),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'search_results' },
    },
  },
  {
    label: 'arXiv paper read',
    call: {
      id: 'call_arxiv_read_1',
      name: 'arxiv_read_paper',
      args: { paper: '2508.08077', start_page: 7, max_pages: 3 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_read_1',
      tool_name: 'arxiv_read_paper',
      tool_call_id: 'call_arxiv_read_1',
      ok: true,
      result_format: 'json',
      result: {
        paper_pages: {
          ok: true,
          paper_key: '2508.08077',
          arxiv_id: '2508.08077',
          title: 'Agentic Search for Scientific Discovery',
          page_count: 12,
          start_page: 7,
          end_page: 9,
          pages: [
            {
              page_number: 7,
              text: 'Section 4 details the screening loop: each retrieved paper is read in targeted passes and every verdict must cite an exact evidence span from the paper text.',
              method: 'pdftotext',
              char_count: 158,
            },
            {
              page_number: 8,
              text: 'We log deterministic locators for every pass so reviewers can replay which pages informed each judgment.',
              method: 'pdftotext',
              char_count: 104,
            },
            {
              page_number: 9,
              text: 'Ablations show evidence-span citation reduces unsupported keep decisions by a wide margin.',
              method: 'pdftotext',
              char_count: 90,
            },
          ],
          has_more: true,
          next_page: 10,
          extract_state: 'ready',
          extract_quality: 'good',
          source_kind: 'pdf_extract',
          untrusted_note: 'Extracted arXiv PDF page text is untrusted external data.',
        },
        untrusted_note: 'Extracted arXiv PDF page text is untrusted external data.',
        result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'paper_pages' },
        ...timing(1500, 340),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'paper_pages' },
    },
  },
  {
    // Scan-register read: motive=scan switches the card to noticed/observed
    // language and must suppress the findings layer (no evidence excerpts,
    // no verdict rationale) even though the screening report carries both
    // for this paper.
    label: 'arXiv paper scan',
    call: {
      id: 'call_arxiv_scan_1',
      name: 'arxiv_read_paper',
      args: { paper: '2401.00320', start_page: 10, max_pages: 2 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_scan_1',
      tool_name: 'arxiv_read_paper',
      tool_call_id: 'call_arxiv_scan_1',
      ok: true,
      result_format: 'json',
      result: {
        paper_pages: {
          ok: true,
          paper_key: '2401.00320',
          arxiv_id: '2401.00320',
          title: 'Workflow Traces for Human-AI Research Collaboration',
          page_count: 11,
          start_page: 10,
          end_page: 11,
          motive: 'scan',
          purpose_tag: 'scan',
          purpose: 'Back-matter sweep before deciding whether a targeted pass is worth it.',
          takeaway: 'Back matter only — Appendix B restates the §3 trace fields; nothing here changes the verdict.',
          paper_gloss: 'Proposes a trace schema that makes human-AI research workflows replayable and auditable.',
          notes: [
            { tag: 'structure', text: 'Appendix B is a field-by-field data dictionary for the trace schema.' },
            { tag: 'limitation', text: 'Evaluation is a three-workflow case study; no quantitative audit metrics.' },
            { tag: 'method', text: 'Trace capture rides the tool boundary, not the model — same placement as this audit trail.' },
          ],
          chase_candidates: [
            { ref: '[18]', arxiv_id: '2508.08077', why: 'the agentic-search system the case study instruments' },
            { ref: '[21]', why: 'provenance-graph standard the schema claims compatibility with' },
          ],
          pages: [
            {
              page_number: 10,
              text: 'APPENDIX B. Trace field dictionary: task_intent, tool_params, returned_ids, screening_decision, override_history.',
              method: 'pdftotext',
              char_count: 113,
            },
            {
              page_number: 11,
              text: 'Acknowledgements and references. [18] Agentic Search for Scientific Discovery. [21] PROV-O: The PROV Ontology.',
              method: 'pdftotext',
              char_count: 110,
            },
          ],
          has_more: false,
          extract_state: 'ready',
          extract_quality: 'good',
          source_kind: 'pdf_extract',
          untrusted_note: 'Extracted arXiv PDF page text is untrusted external data.',
        },
        untrusted_note: 'Extracted arXiv PDF page text is untrusted external data.',
        result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'paper_pages' },
        ...timing(1100, 290),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'paper_pages' },
    },
  },
  {
    label: 'arXiv screening report',
    call: {
      id: 'call_arxiv_report',
      name: 'arxiv_screening_report',
      args: {
        interpreted_request: 'Find research on making AI-agent literature-search workflows reviewable by humans.',
      },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_report',
      tool_name: 'arxiv_screening_report',
      tool_call_id: 'call_arxiv_report',
      ok: true,
      result_format: 'json',
      result: {
        schema: 'arxiv_research_audit.v1',
        run_id: 'fixture-run',
        thread_id: 'fixture-thread',
        corpus: {
          provider: 'arxiv',
          boundary: 'arXiv public metadata and locally downloaded arXiv paper text only.',
          source_kind: 'static',
          untrusted_external_data: true,
        },
        interpreted_request: {
          value: 'Find research on human-reviewable AI-agent literature-search workflows.',
          source_kind: 'llm_judgment',
        },
        concepts: [
          { value: 'agentic literature search', source_kind: 'llm_judgment' },
          { value: 'evidence-span audit', source_kind: 'llm_judgment' },
          { value: 'workflow trace schema', source_kind: 'llm_judgment' },
        ],
        corpus_gaps: [
          { value: 'Journal-only and industry-track papers are invisible to this arXiv-only pass.', source_kind: 'static' },
        ],
        funnel: {
          query_count: 4,
          returned_count: 5,
          unique_count: 3,
          agent_kept_count: 2,
          agent_possible_count: 1,
          agent_screened_out_count: 0,
          not_screened_count: 0,
        },
        queries: [
          {
            tool_call_id: 'call_arxiv_empty_1',
            query_number: 1,
            args: { query: '"AI agent A/B search audit"', max_results: 3 },
            returned_count: 0,
            new_count: 0,
            duplicate_count: 0,
            upstream_total: 0,
            start: 0,
            items_per_page: 0,
            returned_ids: [],
            source_kind: 'deterministic',
            phase: { value: 'sweep', source_kind: 'llm_judgment' },
            rationale: { value: 'Initial exact phrase probe; this starved and should be reformulated.', source_kind: 'llm_judgment' },
          },
          {
            tool_call_id: 'call_arxiv_empty_2',
            query_number: 2,
            args: { query: 'ti:"human centred progressive discovery"', max_results: 3 },
            returned_count: 0,
            new_count: 0,
            duplicate_count: 0,
            upstream_total: 0,
            start: 0,
            items_per_page: 0,
            returned_ids: [],
            source_kind: 'deterministic',
            phase: { value: 'sweep', source_kind: 'llm_judgment' },
            rationale: { value: 'Title-field exact phrase probe; also starved, so the search broadened.', source_kind: 'llm_judgment' },
          },
          {
            tool_call_id: 'call_arxiv_q1',
            query_number: 3,
            args: { query: 'agentic literature search evidence spans', max_results: 3 },
            returned_count: 3,
            new_count: 3,
            duplicate_count: 0,
            upstream_total: 34,
            start: 0,
            items_per_page: 3,
            returned_ids: ['2508.08077', '2406.11231', '2401.00320'],
            source_kind: 'deterministic',
            phase: { value: 'sweep', source_kind: 'llm_judgment' },
            rationale: { value: 'Start with the direct design target: agentic search plus evidence-span auditing.', source_kind: 'llm_judgment' },
          },
          {
            tool_call_id: 'call_arxiv_q2',
            query_number: 4,
            args: { query: 'human AI research workflow trace schema', max_results: 3 },
            returned_count: 2,
            new_count: 0,
            duplicate_count: 2,
            upstream_total: 2,
            start: 0,
            items_per_page: 3,
            returned_ids: ['2401.00320', '2508.08077'],
            source_kind: 'deterministic',
            phase: { value: 'refine', source_kind: 'llm_judgment' },
            rationale: { value: 'Probe for trace-schema language after the first query saturated on agent-search phrasing.', source_kind: 'llm_judgment' },
            derived_from: [{ arxiv_id: '2508.08077', source_kind: 'validated' }],
          },
        ],
        papers: [
          {
            ...arxivEntries[0],
            found_by: ['call_arxiv_q1', 'call_arxiv_q2'],
            first_seen_query: 'call_arxiv_q1',
            duplicate_in: ['call_arxiv_q2'],
            status: 'kept',
            status_source_kind: 'validated',
            read_state: 'read',
            read_coverage: {
              source_kind: 'deterministic',
              read_state: 'read',
              read_tool_call_ids: ['call_arxiv_read_1'],
              latest_tool_call_id: 'call_arxiv_read_1',
              pages_read_count: 3,
              page_count: 12,
              paper_key: '2508.08077',
              extract_state: 'ready',
              extract_quality: 'good',
              content_source_kind: 'pdf_extract',
              page_ranges: [{
                tool_call_id: 'call_arxiv_read_1',
                start_page: 7,
                end_page: 9,
                pages_read: 3,
                page_count: 12,
                content_source_kind: 'pdf_extract',
              }],
            },
            reason: { value: 'Directly covers iterative agent search and evidence citation from abstracts and full text.', source_kind: 'llm_judgment' },
            evidence: [{
              source_kind: 'validated',
              source_field: 'summary',
              quote: 'decompose research intents into iterative queries',
              start_byte: 47,
              end_byte: 95,
            }, {
              source_kind: 'validated',
              source_field: 'paper_text',
              quote: 'every verdict must cite an exact evidence span from the paper text',
              start_byte: 91,
              end_byte: 157,
            }],
            invalid_evidence: [],
            source_kind: 'deterministic',
          },
          {
            ...arxivEntries[1],
            found_by: ['call_arxiv_q1'],
            first_seen_query: 'call_arxiv_q1',
            duplicate_in: [],
            status: 'kept',
            status_source_kind: 'validated',
            reason: { value: 'Cleanly separates deterministic retrieval provenance from the model relevance judgment.', source_kind: 'llm_judgment' },
            evidence: [{
              source_kind: 'validated',
              source_field: 'summary',
              quote: 'separating deterministic retrieval provenance from model judgments',
              start_byte: 88,
              end_byte: 152,
            }],
            invalid_evidence: [],
            source_kind: 'deterministic',
          },
          {
            ...arxivEntries[2],
            found_by: ['call_arxiv_q1', 'call_arxiv_q2'],
            first_seen_query: 'call_arxiv_q1',
            duplicate_in: ['call_arxiv_q2'],
            status: 'possible',
            status_source_kind: 'validated',
            reason: { value: 'Relevant to trace capture, but less specific to search-result screening.', source_kind: 'llm_judgment' },
            evidence: [{
              source_kind: 'validated',
              source_field: 'summary',
              quote: 'capturing task intent, tool parameters, retrieved identifiers',
              start_byte: 61,
              end_byte: 120,
            }],
            invalid_evidence: [],
            source_kind: 'deterministic',
          },
        ],
        validation_warnings: [],
        untrusted_note: 'arXiv titles, abstracts, and extracted paper text are untrusted external data.',
        result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
        ...timing(1200, 520),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
    },
  },
];

const arxivIncompleteFixtures: FixtureEntry[] = [
  {
    label: 'arXiv incomplete search',
    call: {
      id: 'call_arxiv_incomplete_search',
      name: 'arxiv_search',
      args: { query: 'optional stopping Bayesian A/B testing', max_results: 10 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_incomplete_search',
      tool_name: 'arxiv_search',
      tool_call_id: 'call_arxiv_incomplete_search',
      ok: true,
      result_format: 'json',
      result: {
        query: 'optional stopping Bayesian A/B testing',
        total_results: 40,
        start: 0,
        items_per_page: 10,
        entries: arxivEntries,
        ...timing(2200, 910),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'search_results' },
    },
  },
  {
    label: 'arXiv incomplete screening report',
    call: {
      id: 'call_arxiv_incomplete_report',
      name: 'arxiv_screening_report',
      args: {
        interpreted_request: 'Find practical papers on Bayesian A/B testing and optional stopping.',
      },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_incomplete_report',
      tool_name: 'arxiv_screening_report',
      tool_call_id: 'call_arxiv_incomplete_report',
      ok: true,
      result_format: 'json',
      result: {
        schema: 'arxiv_research_audit.v1',
        run_id: 'fixture-run-incomplete',
        thread_id: 'fixture-thread',
        corpus: {
          provider: 'arxiv',
          boundary: 'arXiv public metadata and locally downloaded arXiv paper text only.',
          source_kind: 'static',
          untrusted_external_data: true,
        },
        interpreted_request: {
          value: 'Find practical papers on Bayesian A/B testing and optional stopping.',
          source_kind: 'llm_judgment',
        },
        concepts: [
          { value: 'Bayesian A/B testing', source_kind: 'llm_judgment' },
          { value: 'optional stopping', source_kind: 'llm_judgment' },
        ],
        corpus_gaps: [
          { value: 'Industry experimentation writeups and journal-only papers are outside this arXiv pass.', source_kind: 'static' },
        ],
        funnel: {
          query_count: 1,
          returned_count: 40,
          unique_count: 40,
          agent_kept_count: 0,
          agent_possible_count: 0,
          agent_screened_out_count: 0,
          not_screened_count: 40,
        },
        queries: [
          {
            tool_call_id: 'call_arxiv_incomplete_search',
            query_number: 1,
            args: { query: 'optional stopping Bayesian A/B testing', max_results: 10 },
            returned_count: 10,
            new_count: 10,
            duplicate_count: 0,
            upstream_total: 40,
            start: 0,
            items_per_page: 10,
            returned_ids: ['2508.08077', '2406.11231', '2401.00320'],
            source_kind: 'deterministic',
            phase: { value: 'sweep', source_kind: 'llm_judgment' },
            rationale: { value: 'Broad practical probe before screening verdicts were emitted.', source_kind: 'llm_judgment' },
          },
        ],
        papers: arxivEntries.map((entry) => ({
          ...entry,
          found_by: ['call_arxiv_incomplete_search'],
          first_seen_query: 'call_arxiv_incomplete_search',
          duplicate_in: [],
          status: 'not_screened',
          status_source_kind: 'deterministic',
          reason: { value: 'No agent screening verdict was emitted for this candidate.', source_kind: 'deterministic' },
          evidence: [],
          invalid_evidence: [],
          source_kind: 'deterministic',
        })),
        validation_warnings: [
          {
            code: 'screening_missing',
            message: 'Candidates were collected, but the agent did not emit per-paper screening verdicts.',
          },
        ],
        untrusted_note: 'arXiv titles, abstracts, and extracted paper text are untrusted external data.',
        result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
        ...timing(900, 18),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
    },
  },
];

function duplicateScreeningReportFixture(
  reportId: string,
  artifactId: string,
  offsetMs: number,
  warningLabel: string,
): FixtureEntry {
  return {
    label: `arXiv duplicate ${warningLabel}`,
    call: {
      id: reportId,
      name: 'arxiv_screening_report',
      args: {
        interpreted_request: 'Audit duplicate arXiv screening reports with unverified evidence.',
      },
    },
    envelope: {
      artifact_id: artifactId,
      tool_name: 'arxiv_screening_report',
      tool_call_id: reportId,
      ok: true,
      result_format: 'json',
      result: {
        schema: 'arxiv_research_audit.v1',
        run_id: 'fixture-run-duplicate-screening',
        thread_id: 'fixture-thread',
        corpus: {
          provider: 'arxiv',
          boundary: 'arXiv public metadata and locally downloaded arXiv paper text only.',
          source_kind: 'static',
          untrusted_external_data: true,
        },
        interpreted_request: {
          value: 'Audit whether duplicate screening passes can be inspected and verified.',
          source_kind: 'llm_judgment',
        },
        concepts: [
          { value: 'duplicate screening report', source_kind: 'llm_judgment' },
          { value: 'evidence validation', source_kind: 'llm_judgment' },
        ],
        corpus_gaps: [
          { value: 'Journal-only papers are outside this arXiv-only pass.', source_kind: 'static' },
        ],
        funnel: {
          query_count: 1,
          returned_count: 3,
          unique_count: 3,
          agent_kept_count: 1,
          agent_possible_count: 1,
          agent_screened_out_count: 1,
          not_screened_count: 0,
        },
        queries: [
          {
            tool_call_id: 'call_arxiv_duplicate_search',
            query_number: 1,
            args: { query: 'human reviewable arxiv audit workflow', max_results: 3 },
            returned_count: 3,
            new_count: 3,
            duplicate_count: 0,
            upstream_total: 3,
            start: 0,
            items_per_page: 3,
            returned_ids: ['2508.08077', '2406.11231', '2401.00320'],
            source_kind: 'deterministic',
            phase: { value: 'screen', source_kind: 'llm_judgment' },
            rationale: { value: 'Single candidate pool used to exercise duplicate screening presentation.', source_kind: 'llm_judgment' },
          },
        ],
        papers: [
          {
            ...arxivEntries[0],
            found_by: ['call_arxiv_duplicate_search'],
            first_seen_query: 'call_arxiv_duplicate_search',
            duplicate_in: [],
            status: 'kept',
            status_source_kind: 'llm_judgment',
            reason: { value: 'Closest match to the proposed audit workflow and search trace UX.', source_kind: 'llm_judgment' },
            evidence: [],
            invalid_evidence: [
              { code: 'evidence_quote_missing', message: 'Claimed supporting quote could not be validated in the abstract.', arxiv_id: '2508.08077' },
            ],
            source_kind: 'deterministic',
          },
          {
            ...arxivEntries[1],
            found_by: ['call_arxiv_duplicate_search'],
            first_seen_query: 'call_arxiv_duplicate_search',
            duplicate_in: [],
            status: 'possible',
            status_source_kind: 'llm_judgment',
            reason: { value: 'Potentially useful for evidence-span auditing, but the relevance evidence is not validated.', source_kind: 'llm_judgment' },
            evidence: [],
            invalid_evidence: [
              { code: 'evidence_quote_missing', message: 'The relevance quote was not found at the recorded offset.', arxiv_id: '2406.11231' },
            ],
            source_kind: 'deterministic',
          },
          {
            ...arxivEntries[2],
            found_by: ['call_arxiv_duplicate_search'],
            first_seen_query: 'call_arxiv_duplicate_search',
            duplicate_in: [],
            status: 'screened_out',
            status_source_kind: 'llm_judgment',
            reason: { value: 'Too broad for the current arXiv audit-trail interaction question.', source_kind: 'llm_judgment' },
            evidence: [],
            invalid_evidence: [
              { code: 'evidence_quote_missing', message: 'Screen-out rationale has no validated evidence span.', arxiv_id: '2401.00320' },
            ],
            source_kind: 'deterministic',
          },
        ],
        validation_warnings: [
          { code: 'evidence_unverified', message: `${warningLabel}: 0 of 3 papers have validated evidence spans.` },
        ],
        untrusted_note: 'arXiv titles, abstracts, and extracted paper text are untrusted external data.',
        result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
        ...timing(offsetMs, 12),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
    },
  };
}

const arxivDuplicateScreeningFixtures: FixtureEntry[] = [
  {
    label: 'arXiv duplicate screening search',
    call: {
      id: 'call_arxiv_duplicate_search',
      name: 'arxiv_search',
      args: { query: 'human reviewable arxiv audit workflow', max_results: 3 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_duplicate_search',
      tool_name: 'arxiv_search',
      tool_call_id: 'call_arxiv_duplicate_search',
      ok: true,
      result_format: 'json',
      result: {
        query: 'human reviewable arxiv audit workflow',
        total_results: 3,
        start: 0,
        items_per_page: 3,
        entries: arxivEntries,
        ...timing(1800, 640),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'search_results' },
    },
  },
  duplicateScreeningReportFixture('call_arxiv_duplicate_report_1', 'artifact_arxiv_duplicate_report_1', 900, 'screening report 1'),
  duplicateScreeningReportFixture('call_arxiv_duplicate_report_2', 'artifact_arxiv_duplicate_report_2', 700, 'screening report 2'),
];

const arxivRecommendationFixtures: FixtureEntry[] = [
  {
    label: 'arXiv recommendations',
    call: {
      id: 'call_arxiv_recommendations',
      name: 'arxiv_recommendations',
      args: {
        screening_report_tool_call_id: 'call_arxiv_duplicate_report_2',
      },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_recommendations',
      tool_name: 'arxiv_recommendations',
      tool_call_id: 'call_arxiv_recommendations',
      ok: true,
      result_format: 'json',
      result: {
        schema: 'arxiv_recommendations.v1',
        run_id: 'fixture-run-duplicate-screening',
        thread_id: 'fixture-thread',
        screening_report: {
          schema: 'arxiv_research_audit.v1',
          tool_call_id: 'call_arxiv_duplicate_report_2',
          artifact_id: 'artifact_arxiv_duplicate_report_2',
        },
        selection_basis: {
          value: 'Prioritize papers that are kept or possible in the screening report and useful for auditing search workflow UX.',
          source_kind: 'llm_judgment',
        },
        metadata_resolution: 'Resolve titles, abstracts, authors, and evidence text from the linked arXiv screening report.',
        recommendation_count: 2,
        recommendations: [
          {
            rank: 1,
            arxiv_id: '2508.08077',
            version: 1,
            tier: 'primary',
            screening_status: 'kept',
            screening_report_tool_call_id: 'call_arxiv_duplicate_report_2',
            screening_report_artifact_id: 'artifact_arxiv_duplicate_report_2',
            screening_report_paper_index: 0,
            source_kind: 'validated',
            reason: { value: 'Best direct fit for reviewing search-trace and screening workflow design.', source_kind: 'llm_judgment' },
            use_for: { value: 'Use as the starting point for agentic literature-search audit UI patterns.', source_kind: 'llm_judgment' },
            evidence_refs: [
              {
                evidence_index: 0,
                source_field: 'summary',
                screening_report_tool_call_id: 'call_arxiv_duplicate_report_2',
                screening_report_artifact_id: 'artifact_arxiv_duplicate_report_2',
                source_kind: 'validated',
              },
            ],
          },
          {
            rank: 2,
            arxiv_id: '2406.11231',
            version: 2,
            tier: 'secondary',
            screening_status: 'possible',
            screening_report_tool_call_id: 'call_arxiv_duplicate_report_2',
            screening_report_artifact_id: 'artifact_arxiv_duplicate_report_2',
            screening_report_paper_index: 1,
            source_kind: 'validated',
            reason: { value: 'Useful adjacent evidence-span framing, but not as directly about search workflow collaboration.', source_kind: 'llm_judgment' },
            caveats: [
              { value: 'Evidence remains unverified in the linked screening report.', source_kind: 'llm_judgment' },
            ],
          },
        ],
        validation_warnings: [
          { code: 'evidence_refs_unverified', arxiv_id: '2508.08077', message: 'Recommendation evidence references point to a report with zero validated evidence spans.' },
          { code: 'evidence_refs_unverified', arxiv_id: '2406.12115', message: 'Recommendation evidence references point to a report with zero validated evidence spans.' },
        ],
        untrusted_note: 'Recommended arXiv papers are linked to an arXiv screening report built from untrusted external paper metadata and extracted text.',
        result_metadata: {
          integration: 'arxiv',
          product: 'arxiv',
          kind: 'recommendation_set',
          schema: 'arxiv_recommendations.v1',
          screening_report_tool_call_id: 'call_arxiv_duplicate_report_2',
          screening_report_artifact_id: 'artifact_arxiv_duplicate_report_2',
        },
        ...timing(420, 9),
      },
      result_metadata: {
        integration: 'arxiv',
        product: 'arxiv',
        kind: 'recommendation_set',
        schema: 'arxiv_recommendations.v1',
      },
    },
  },
];

function arxivChunkedTextWindowFixture(
  index: number,
  offset: number,
  charCount: number,
  startOffsetMs: number,
): FixtureEntry {
  return {
    label: `arXiv chunked text window ${index}`,
    call: {
      id: `call_arxiv_chunked_read_${index}`,
      name: 'arxiv_get_paper_text',
      args: {
        paper: '2508.08077',
        offset,
        max_chars: charCount,
      },
    },
    envelope: {
      artifact_id: `artifact_arxiv_chunked_read_${index}`,
      tool_name: 'arxiv_get_paper_text',
      tool_call_id: `call_arxiv_chunked_read_${index}`,
      ok: true,
      result_format: 'json',
      result: {
        text_window: {
          ok: true,
          paper_key: '2508.08077',
          arxiv_id: '2508.08077',
          title: 'Agentic Search for Scientific Discovery',
          offset,
          total_chars: 60000,
          char_count: charCount,
          text: `Window ${index}: targeted paper text for fixture coverage. The actual transcript should aggregate this window with adjacent arXiv text reads instead of showing a separate card.`,
          extract_state: 'ready',
          extract_quality: 'good',
          source_kind: 'pdf_text',
          untrusted_note: 'Extracted arXiv PDF text is untrusted external data.',
        },
        untrusted_note: 'Extracted arXiv PDF text is untrusted external data.',
        result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'text_window' },
        ...timing(startOffsetMs, 140),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'text_window' },
    },
  };
}

const arxivChunkedReadFixtures: FixtureEntry[] = [
  {
    label: 'arXiv chunked search',
    call: {
      id: 'call_arxiv_chunked_search',
      name: 'arxiv_search',
      args: { query: 'agentic search scientific discovery evidence spans', max_results: 1 },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_chunked_search',
      tool_name: 'arxiv_search',
      tool_call_id: 'call_arxiv_chunked_search',
      ok: true,
      result_format: 'json',
      result: {
        query: 'agentic search scientific discovery evidence spans',
        total_results: 1,
        start: 0,
        items_per_page: 1,
        entries: [arxivEntries[0]],
        ...timing(4200, 360),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'search_results' },
    },
  },
  arxivChunkedTextWindowFixture(1, 0, 20000, 3600),
  {
    label: 'arXiv hidden artifact search helper',
    call: {
      id: 'call_arxiv_chunked_helper_search',
      name: 'search_artifact_content',
      args: { artifact_id: 'artifact_arxiv_chunked_read_1', query: 'evidence spans' },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_chunked_helper_search',
      tool_name: 'search_artifact_content',
      tool_call_id: 'call_arxiv_chunked_helper_search',
      ok: true,
      result_format: 'json',
      result: {
        matches: [{ path: 'text_window.text', snippet: 'evidence spans' }],
        ...timing(3400, 1),
      },
      result_metadata: { integration: 'artifact', product: 'artifact', kind: 'lookup' },
    },
  },
  arxivChunkedTextWindowFixture(2, 20000, 20000, 3000),
  {
    label: 'arXiv hidden JSON artifact helper',
    call: {
      id: 'call_arxiv_chunked_helper_json',
      name: 'get_json_artifact',
      args: { artifact_id: 'artifact_arxiv_chunked_read_2', path: 'text_window.text' },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_chunked_helper_json',
      tool_name: 'get_json_artifact',
      tool_call_id: 'call_arxiv_chunked_helper_json',
      ok: true,
      result_format: 'json',
      result: {
        path: 'text_window.text',
        value: 'Window 2 excerpt',
        ...timing(2800, 1),
      },
      result_metadata: { integration: 'artifact', product: 'artifact', kind: 'lookup' },
    },
  },
  arxivChunkedTextWindowFixture(3, 40000, 20000, 2400),
  {
    label: 'arXiv chunked screening report',
    call: {
      id: 'call_arxiv_chunked_report',
      name: 'arxiv_screening_report',
      args: {
        interpreted_request: 'Read the selected arXiv paper in text windows.',
      },
    },
    envelope: {
      artifact_id: 'artifact_arxiv_chunked_report',
      tool_name: 'arxiv_screening_report',
      tool_call_id: 'call_arxiv_chunked_report',
      ok: true,
      result_format: 'json',
      result: {
        schema: 'arxiv_research_audit.v1',
        run_id: 'fixture-run-chunked-read',
        thread_id: 'fixture-thread',
        corpus: {
          provider: 'arxiv',
          boundary: 'arXiv public metadata and locally downloaded arXiv paper text only.',
          source_kind: 'static',
          untrusted_external_data: true,
        },
        interpreted_request: {
          value: 'Read the selected arXiv paper in text windows.',
          source_kind: 'llm_judgment',
        },
        concepts: [
          { value: 'chunked paper text reading', source_kind: 'llm_judgment' },
        ],
        corpus_gaps: [],
        funnel: {
          query_count: 1,
          returned_count: 1,
          unique_count: 1,
          read_paper_count: 1,
          agent_kept_count: 1,
          agent_possible_count: 0,
          agent_screened_out_count: 0,
          not_screened_count: 0,
        },
        queries: [
          {
            tool_call_id: 'call_arxiv_chunked_search',
            query_number: 1,
            args: { query: 'agentic search scientific discovery evidence spans', max_results: 1 },
            returned_count: 1,
            new_count: 1,
            duplicate_count: 0,
            upstream_total: 1,
            start: 0,
            items_per_page: 1,
            returned_ids: ['2508.08077'],
            source_kind: 'deterministic',
            phase: { value: 'read', source_kind: 'llm_judgment' },
            rationale: { value: 'One selected paper is read through contiguous text windows.', source_kind: 'llm_judgment' },
          },
        ],
        papers: [
          {
            ...arxivEntries[0],
            found_by: ['call_arxiv_chunked_search'],
            first_seen_query: 'call_arxiv_chunked_search',
            duplicate_in: [],
            status: 'kept',
            status_source_kind: 'validated',
            read_state: 'read',
            read_coverage: {
              source_kind: 'deterministic',
              read_state: 'read',
              read_tool_call_ids: [
                'call_arxiv_chunked_read_1',
                'call_arxiv_chunked_read_2',
                'call_arxiv_chunked_read_3',
              ],
              latest_tool_call_id: 'call_arxiv_chunked_read_3',
              pages_read_count: 0,
              paper_key: '2508.08077',
              extract_state: 'ready',
              extract_quality: 'good',
              content_source_kind: 'pdf_text',
              page_ranges: [],
            },
            reason: { value: 'Selected as the single paper to read in full-text windows.', source_kind: 'llm_judgment' },
            evidence: [],
            invalid_evidence: [],
            source_kind: 'deterministic',
          },
        ],
        validation_warnings: [],
        untrusted_note: 'arXiv titles, abstracts, and extracted paper text are untrusted external data.',
        result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
        ...timing(1600, 240),
      },
      result_metadata: { integration: 'arxiv', product: 'arxiv', kind: 'screening_report', schema: 'arxiv_research_audit.v1' },
    },
  },
];

const allFixtures = [
  ...fixtures,
  ...arxivFixtures,
  ...arxivIncompleteFixtures,
  ...arxivDuplicateScreeningFixtures,
  ...arxivRecommendationFixtures,
  ...arxivChunkedReadFixtures,
];

const artifacts = allFixtures.map((f) => {
  const artifact = artifactFromEnvelope(f.envelope, f.call.args);
  if (f.call.id === "call_sharepoint_read") {
    const result = f.envelope.result as Record<string, any>;
    const resultMetadata = (f.envelope.result_metadata || {}) as Record<string, any>;
    const textPreview = typeof result.text_preview === "string" ? result.text_preview : artifact.content;
    return {
      ...artifact,
      type: "file",
      title: "Q3 Roadmap.md",
      content: textPreview,
      metadata: {
        ...(artifact.metadata || {}),
        result_metadata: resultMetadata,
        tool_presentation: resultMetadata.tool_presentation,
        mimeType: "text/markdown",
        detectedMimeType: "text/markdown",
        fileKind: "markdown",
        sizeBytes: result.size_bytes,
        local_path: result.local_path,
        source_url: result.item_url,
        server_relative_url: result.server_relative_url,
        site_url: result.site_url,
        raw_result: f.envelope.result,
      },
    } as Artifact;
  }
  if (f.call.id === "call_arxiv_read_1") {
    return {
      ...artifact,
      metadata: {
        ...(artifact.metadata || {}),
        // Guard fixture: arXiv read results may carry artifact-ish metadata
        // for the inspector, but the transcript read card remains the preview.
        // Without the arXiv suppression in CollapsedToolsSummary this would
        // render an accidental chart card under the audit trail.
        plot_spec: {
          option: {
            xAxis: { type: "category", data: ["p7", "p8", "p9"] },
            yAxis: { type: "value" },
            series: [{ type: "bar", data: [158, 104, 90] }],
          },
        },
        chart_engine: "echarts",
      },
    };
  }
  return artifact;
});

const toolResultsByCallId = new Map(
  allFixtures.map((f, i) => [
    f.call.id,
    { envelope: f.envelope, artifact: artifacts[i] },
  ]),
);

const artifactsById: Record<string, Artifact> = Object.fromEntries(
  artifacts.map((a) => [a.id, a]),
);

// Pre-submit executing query: the tool call has started (stamped _uiStartedAt)
// but no envelope/artifact exists yet. The chat card must show live progress
// and clicking it must open a derived submitting placeholder in the panel.
// Args arrive ONLY as raw streamed argsText (no parsed args) to exercise the
// useQueryInspectorState argsText fallback for SQL/description. Not a
// FixtureEntry — it deliberately has no envelope, so it only joins toolCalls.
const executingCall = {
  id: "call_bq_executing",
  name: "run_bigquery_query",
  args: {},
  argsText: JSON.stringify({
    sql: [
      "SELECT channel, COUNT(DISTINCT session_id) AS sessions",
      "FROM `analytics-prod.events.sessions_daily`",
      "WHERE session_start >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY)",
      "GROUP BY channel",
    ].join("\n"),
    description: "Weekly session counts by channel (pre-submit executing fixture).",
  }),
  _uiStartedAt: now - 3_200,
  _uiRunId: "fixture-run",
} as unknown as ToolCall;

const executingArxivReadCall = {
  id: "call_arxiv_read_executing",
  name: "arxiv_read_paper",
  args: {
    paper: "2508.08077",
    start_page: 3,
    max_pages: 4,
  },
  _uiStartedAt: now - 4_800,
  _uiRunId: "fixture-run-arxiv-live",
} as unknown as ToolCall;

const toolProgressByKey: Record<string, ToolProgressEvent> = {
  "fixture-run:call_bq_executing": {
    tool_call_id: "call_bq_executing",
    tool_name: "run_bigquery_query",
    provider: "bigquery",
    run_id: "fixture-run",
    phase: "submitted",
    label: "Submitting query",
    timestamp: now,
  },
  "fixture-run-arxiv-live:call_arxiv_read_executing": {
    tool_call_id: "call_arxiv_read_executing",
    tool_name: "arxiv_read_paper",
    provider: "arxiv",
    integration: "arxiv",
    run_id: "fixture-run-arxiv-live",
    phase: "arxiv_pdf_page_reading",
    label: "Reading paper",
    message: "Reading page 3 of 12",
    percent: 34,
    arxiv_id: "2508.08077",
    paper_key: "2508.08077",
    title: "Agentic Search for Scientific Discovery",
    motive: "screening",
    purpose_tag: "targeted",
    purpose: "Checking the pages most likely to justify the screening verdict.",
    paper_gloss: "Studies agentic literature search systems that decompose research intents into iterative queries and cite exact evidence spans.",
    page_current: 3,
    page_count: 12,
    pages_read: 1,
    extract_state: "ready",
    extract_quality: "good",
    source_kind: "pdf_extract",
    timestamp: now,
  },
};

const toolCalls: ToolCall[] = [...fixtures.map((f) => f.call), executingCall];
const arxivToolCalls: ToolCall[] = arxivFixtures.map((f) => f.call);
const arxivIncompleteToolCalls: ToolCall[] = arxivIncompleteFixtures.map((f) => f.call);
const arxivDuplicateScreeningToolCalls: ToolCall[] = arxivDuplicateScreeningFixtures.map((f) => f.call);
const arxivRecommendationToolCalls: ToolCall[] = arxivRecommendationFixtures.map((f) => f.call);
const arxivChunkedReadToolCalls: ToolCall[] = arxivChunkedReadFixtures.map((f) => f.call);
const arxivLiveReadingToolCalls: ToolCall[] = [executingArxivReadCall];

// Chat-inline rendering routes SQL tool results through a SEPARATE map
// (`sqlToolResultsByScopedKey`) keyed by `getQueryCardStateKey`, not via
// `toolResultsByCallId`. MessageList builds this map at runtime from real
// assistant messages + their identity keys. We mirror the same shape here
// so the SQL fixtures (BigQuery / Databricks completed, running, cancelled,
// errored) all flow into the unified QueryInspector instead of falling
// through to the empty "executing" default.
const FIXTURE_MESSAGE_IDENTITY_KEY = "fixture:audit-trail:assistant-1";
const FIXTURE_MESSAGE_ID = "fixture:msg:audit-trail";

const sqlToolResultsByScopedKey = new Map<
  string,
  {
    envelope: ToolResultEnvelope;
    artifact?: Artifact;
    resultMessageId?: string;
    resultMessageSeq?: number;
    resultMessageIdentityKey?: string;
  }
>();
fixtures.forEach((f, i) => {
  if (!isQueryRunToolName(f.call.name)) return;
  const scopedKey = getQueryCardStateKey({
    messageIdentityKey: FIXTURE_MESSAGE_IDENTITY_KEY,
    toolCallId: f.call.id,
  });
  if (!scopedKey) return;
  sqlToolResultsByScopedKey.set(scopedKey, {
    envelope: f.envelope,
    artifact: artifacts[i],
    resultMessageId: FIXTURE_MESSAGE_ID,
    resultMessageSeq: 1,
    resultMessageIdentityKey: FIXTURE_MESSAGE_IDENTITY_KEY,
  });
});

const emptySettings = {} as AppSettings;

export default function AuditTrailFixture() {
  // The inspector matches the real app: a large modal overlay over a dimmed
  // backdrop (InspectorOverlay) at every width. Nothing is open until a card
  // or launch button opens an artifact.
  const [activeArtifact, setActiveArtifact] = React.useState<Artifact>(artifacts[0]);
  const [isInspectorOpen, setIsInspectorOpen] = React.useState(false);

  const openArtifact = React.useCallback((artifact: Artifact) => {
    setActiveArtifact(artifact);
    setIsInspectorOpen(true);
  }, []);
  const closeInspector = React.useCallback(() => setIsInspectorOpen(false), []);

  return (
    <TooltipProvider>
      <div className="flex h-screen min-w-0 bg-background text-foreground">
        <main className="flex min-w-0 flex-1 flex-col gap-4 overflow-auto p-6">
          <div className="max-w-3xl">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">
              Local UI fixture
            </p>
            <h1 className="mt-1 text-xl font-semibold">Audit Trail Redesign</h1>
            <p className="mt-2 text-sm text-muted-foreground">
              Synthetic tool results across every connector family and state — GitHub widgets,
              SQL query inspectors (BigQuery / Databricks / Trino / Snowflake, incl. running,
              cancelled and error), the full Jira widget family (status/connection, issue,
              history, comments, attachments, link types, users, search, find, REST, approvals
              and write results), Slack and Browser/CUA through the generic connector inspector,
              and a connector-less generic fallback.
            </p>
          </div>

          <section className="max-w-3xl rounded-md border border-border bg-card/40 p-4">
            <CollapsedToolsSummary
              toolCalls={toolCalls}
              isLastMessage
              artifactsById={artifactsById}
              toolResultsByCallId={toolResultsByCallId}
              sqlToolResultsByScopedKey={sqlToolResultsByScopedKey}
              toolProgressByKey={toolProgressByKey}
              messageIdentityKey={FIXTURE_MESSAGE_IDENTITY_KEY}
              onOpenArtifact={openArtifact}
            />
          </section>

          <section className="max-w-3xl rounded-md border border-border bg-card/40 p-4">
            <div className="mb-3">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">
                arXiv research workflow
              </p>
              <p className="mt-1 text-sm text-muted-foreground">
                Synthetic arXiv search run showing the discovery funnel, query rationale,
                corpus boundary, validated evidence spans, and screening report inspector.
              </p>
            </div>
            <CollapsedToolsSummary
              toolCalls={arxivToolCalls}
              isLastMessage={false}
              artifactsById={artifactsById}
              toolResultsByCallId={toolResultsByCallId}
              messageIdentityKey={`${FIXTURE_MESSAGE_IDENTITY_KEY}:arxiv`}
              onOpenArtifact={openArtifact}
            />
          </section>

          <section className="max-w-3xl rounded-md border border-border bg-card/40 p-4">
            <div className="mb-3">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">
                arXiv incomplete screening
              </p>
              <p className="mt-1 text-sm text-muted-foreground">
                Synthetic state matching a run where papers were collected but no verdicts
                were emitted yet.
              </p>
            </div>
            <CollapsedToolsSummary
              toolCalls={arxivIncompleteToolCalls}
              isLastMessage={false}
              artifactsById={artifactsById}
              toolResultsByCallId={toolResultsByCallId}
              messageIdentityKey={`${FIXTURE_MESSAGE_IDENTITY_KEY}:arxiv-incomplete`}
              onOpenArtifact={openArtifact}
            />
          </section>

          <section className="max-w-3xl rounded-md border border-border bg-card/40 p-4">
            <div className="mb-3">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">
                arXiv duplicate screening reports
              </p>
              <p className="mt-1 text-sm text-muted-foreground">
                Synthetic state matching a run where the agent emitted two screening
                reports with the same verdicts and unverified evidence.
              </p>
            </div>
            <CollapsedToolsSummary
              toolCalls={arxivDuplicateScreeningToolCalls}
              isLastMessage={false}
              artifactsById={artifactsById}
              toolResultsByCallId={toolResultsByCallId}
              messageIdentityKey={`${FIXTURE_MESSAGE_IDENTITY_KEY}:arxiv-duplicate-screening`}
              onOpenArtifact={openArtifact}
            />
          </section>

          <section className="max-w-3xl rounded-md border border-border bg-card/40 p-4">
            <div className="mb-3">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">
                arXiv recommendations
              </p>
              <p className="mt-1 text-sm text-muted-foreground">
                Structured recommendation-set artifact linked to a screening report,
                replacing markdown tables in final answers.
              </p>
            </div>
            <CollapsedToolsSummary
              toolCalls={arxivRecommendationToolCalls}
              isLastMessage={false}
              artifactsById={artifactsById}
              toolResultsByCallId={toolResultsByCallId}
              messageIdentityKey={`${FIXTURE_MESSAGE_IDENTITY_KEY}:arxiv-recommendations`}
              onOpenArtifact={openArtifact}
            />
          </section>

          <section className="max-w-3xl rounded-md border border-border bg-card/40 p-4">
            <div className="mb-3">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">
                arXiv chunked paper reading
              </p>
              <p className="mt-1 text-sm text-muted-foreground">
                Synthetic state matching text-window reads interleaved with
                internal artifact lookup helpers.
              </p>
            </div>
            <CollapsedToolsSummary
              toolCalls={arxivChunkedReadToolCalls}
              isLastMessage={false}
              artifactsById={artifactsById}
              toolResultsByCallId={toolResultsByCallId}
              messageIdentityKey={`${FIXTURE_MESSAGE_IDENTITY_KEY}:arxiv-chunked-reading`}
              onOpenArtifact={openArtifact}
            />
          </section>

          <section className="max-w-3xl rounded-md border border-border bg-card/40 p-4">
            <div className="mb-3">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">
                arXiv live paper reading
              </p>
              <p className="mt-1 text-sm text-muted-foreground">
                Synthetic active arXiv read-paper tool call showing the user-facing
                page progress while the model reviews a paper.
              </p>
            </div>
            <CollapsedToolsSummary
              toolCalls={arxivLiveReadingToolCalls}
              isLastMessage
              artifactsById={artifactsById}
              toolResultsByCallId={toolResultsByCallId}
              toolProgressByKey={toolProgressByKey}
              messageIdentityKey={`${FIXTURE_MESSAGE_IDENTITY_KEY}:arxiv-live-reading`}
              onOpenArtifact={openArtifact}
            />
          </section>

          <section className="max-w-3xl">
            <p className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
              Open in inspector
            </p>
            <div className="flex flex-wrap gap-2">
              {allFixtures.map((f, i) => {
                const a = artifacts[i];
                const active = a.id === activeArtifact.id;
                return (
                  <button
                    key={a.id}
                    type="button"
                    onClick={() => openArtifact(a)}
                    aria-pressed={active}
                    className={[
                      "rounded-md border px-3 py-1.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1",
                      active
                        ? "border-foreground/40 bg-muted text-foreground"
                        : "border-border bg-background text-muted-foreground hover:bg-muted hover:text-foreground",
                    ].join(" ")}
                    data-fixture-label={f.label}
                  >
                    {f.label}
                  </button>
                );
              })}
            </div>
          </section>

        </main>

        {isInspectorOpen && (
          <InspectorOverlay onClose={closeInspector}>
            <ArtifactPanel
              artifact={activeArtifact}
              settings={emptySettings}
              onClose={closeInspector}
            />
          </InspectorOverlay>
        )}
      </div>
    </TooltipProvider>
  );
}
