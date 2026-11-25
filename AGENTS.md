# AGENTS.md

## Purpose

This repository is a full-stack, multi-provider AI assistant app with:
- A Go backend for run orchestration, SSE delivery, persistence, proxying and API tooling.
- A React frontend for chat UI, artifacts, settings, and local file handling.

Use this document to quickly map the core structure before editing behavior across the stack.

Primary convention for this workspace:
- **Prefer local logic and existing config over env vars** unless unavoidable.
- For architectural changes, follow execution flow in order:  
  `Frontend -> HTTP API -> Option manager/engine -> Provider adapters -> Storage`.

---

## Runtime Architecture (high level)

```
Browser
  └─ ChatInput/MessageList/useChat
      └─ POST /api/threads/{threadId}/runs
          └─ server/internal/chat/httpapi
              └─ optionc.Manager.StartRun
                  └─ engine.RunAgentLoop (providers/core/engine)
                      ├─ provider.streamChat (adapter)
                      ├─ tool execution callbacks (tool registry)
                      └─ progress/events persisted + pushed via SSE
                          └─ GET /api/runs/{runId}/events (SSE stream in frontend)
```

Artifacts and tools also integrate at the run loop level and are retrievable by API (`/api/artifacts/{id}`), plus pinned artifacts are injected into future message context.

---

## Core directories

### 1) Backend entrypoints and setup

- `server/cmd/ai-analyst/main.go`
  - Application bootstrap and dependency wiring.
  - Store selection (`mysql` vs `json` fallback), service init, routes setup, background cleanup start.
- `server/internal/server/handler.go`
  - Top-level HTTP router/mux setup.
  - Registers: threads/runs/chat routes, search/projects/secrets/settings routes, artifact routes, proxy endpoints.
  - CORS/origin checks and middleware composition.
- `server/internal/server/activation_middleware.go`
  - Activation / install gating for route access.

### 2) Chat HTTP API layer

- `server/internal/chat/httpapi/handler.go`
  - Main REST contract for threads, runs, and artifacts.
  - Important endpoints:
    - `POST /api/threads/{threadId}/runs`
    - `GET /api/runs/{runId}`
    - `GET /api/runs/{runId}/events` (SSE)
    - `POST /api/runs/{runId}/cancel`
    - `GET /api/threads/{threadId}`
    - `GET /api/artifacts/{artifactId}`
  - Converts internal events into SSE envelope format.

### 3) Chat orchestration engine

- `server/internal/chat/optionc/manager.go`
  - Run lifecycle ownership (`Manager`, active run enforcement per thread).
  - Message + run persistence boundaries.
  - SSE subscriber/event buffer + broadcast lifecycle.
  - Tool execution plumbing and post-run state transitions.
- `server/internal/chat/optionc/events.go`
  - Internal run event modeling for server-side event stream lifecycle.
- `server/internal/chat/optionc/planner.go`
  - Runtime context planning before loop starts (history truncation/selection).
- `server/internal/chat/optionc/worker.go`
- Worker routines and background tasks around async run maintenance (if enabled by build path).

### 4) Agent loop / model execution

- `server/internal/chat/engine/engine.go`
  - `RunAgentLoop` main control loop for:
    - text streaming
    - tool-calls
    - thinking deltas/events
    - max-iteration handling
    - finalization/error transitions

### 5) Provider layer (model adapters + registry)

- `providers/core/types.ts`
  - Shared interface and `StreamEvent` contract.
- `providers/core/registry.ts`
  - Provider factory and runtime provider selection.
- `providers/core/engine.ts`
  - Public engine contract used by callers.
- `providers/core/toolNameMapper.ts`
  - Tool name sanitization for provider constraints.
- Adapters:
  - `providers/adapters/openaiCompatibleHttp.ts`
  - `providers/adapters/anthropicHttp.ts`
  - `providers/adapters/geminiHttp.ts`

### 6) Storage layer

- `server/internal/chat/store/store.go`
  - `Store` interface and expected behavior.
- `server/internal/chat/store/json.go`
  - File-system fallback store.
- `server/internal/chat/store/mysql.go`
  - DB-backed store for threads/runs/messages/artifacts/search metadata.

### 7) Tooling and artifact generation

- `tools/registry.ts`
  - Aggregates available tool implementations.
- `tools/fileSystem.ts`
  - `list_files`, `read_file`, `search_files`.
- `tools/dataTools.ts`
  - `display_table` artifact production.
- `tools/bigQuery.ts`
  - BigQuery query tool.
- Tool results are wrapped as envelopes and rendered into `data/code/image` artifacts.

### 8) Frontend runtime

- `hooks/useChat.ts`
  - Core chat state machine:
    - thread/run selection
    - start run request
    - SSE connect and incremental reducer updates
    - cancel flow and error handling
- `components/ChatInput.tsx`
  - Message composition + artifact pinning context injection.
- `components/chat/MessageList.tsx`
  - Rendering messages, assistant/tool outputs, thinking traces.
- `components/chat/*`
  - `AssistantMessage`, `ToolCallBlock`, `ToolResultBlock`, `ArtifactCard`, `ThinkingTrace`, `CodeBlock`.
- `components/ArtifactPanel.tsx`
  - Artifact viewer and persistence interface.

### 9) App-level state and settings

- `hooks/useSettings.ts`
  - localStorage settings + migration-compatible defaults.
- `services/settingsMigration.ts`
  - Backward-compatible settings load path.
- `types.ts`, `constants.ts`
  - Canonical type models and default values.
- `services/sourcesApi.ts`, `services/githubReposApi.ts`
  - Unified source registry, local reference, imported copy, and app-managed GitHub repository API clients.

### 10) Streaming parser and transport

- `providers/stream/sseParser.ts`
  - Generic SSE parser utility.
- Frontend SSE parsing/integration point remains in `hooks/useChat.ts`.
- Backend SSE emission format should be read in sync in:
  - `server/internal/chat/httpapi/handler.go`
  - `hooks/useChat.ts`

---

## Canonical execution path to trace first

1. User submits input:
   - `components/ChatInput.tsx` -> `hooks/useChat.ts`.
2. Frontend creates run:
   - `POST /api/threads/{threadId}/runs` -> `server/internal/chat/httpapi/handler.go`.
3. Run orchestration:
   - `optionc.Manager.StartRun` -> `engine.RunAgentLoop`.
4. Provider/model streaming:
   - `providers/core` interface + adapter in `providers/adapters/*`.
5. Tool events:
   - Loop uses tool callbacks -> `tools/registry.ts` -> artifact/result envelopes.
6. Stream back:
   - SSE from `/api/runs/{runId}/events` consumed by `hooks/useChat.ts`.
7. Completion:
   - Run status/answer persisted in store (`server/internal/chat/store/*`) and reflected in UI.

---

## Files to inspect for likely breakages

- SSE and event consistency:
  - `server/internal/chat/httpapi/handler.go`
  - `hooks/useChat.ts`
  - `providers/stream/sseParser.ts`
  - `server/internal/chat/optionc/manager.go`
- Tool execution and context contamination:
  - `server/internal/chat/engine/engine.go`
  - `server/internal/chat/optionc/planner.go`
  - `tools/registry.ts`
  - `providers/core/toolNameMapper.ts`
- Persistence and run state races:
  - `server/internal/chat/store/store.go`
  - `server/internal/chat/store/mysql.go`
  - `server/internal/chat/store/json.go`
- Multi-provider incompatibilities:
  - `providers/adapters/openaiCompatibleHttp.ts`
  - `providers/adapters/anthropicHttp.ts`
  - `providers/adapters/geminiHttp.ts`
  - `providers/core/types.ts`

---

## Design notes

- Run/thread relationship is managed in backend state, while UI state is local/reactive.
- SSE is the source of real-time truth for assistant/tool progress.
- Artifacts are first-class outputs and can be re-used in subsequent prompts via pinned context.
- The architecture is hook-first on frontend and interface-based on backend, which makes feature-level edits easiest when done at service boundaries first (HTTP -> manager -> engine -> adapters).

---

## Common commands

- `npm install`
- `npm run dev`
- `npm run build`

Backend run commands are defined in `server/cmd/ai-analyst/main.go` and typically via local environment/service process setup for the target environment.

---

## Quick onboarding checklist for future agents

- If changing chat behavior: start from `server/internal/chat/httpapi/handler.go`.
- If changing model/protocol behavior: edit in `providers/core` + adapter.
- If changing persistence: edit `server/internal/chat/store/*` and `server/internal/chat/optionc/manager.go`.
- If changing rendering/state: edit `hooks/useChat.ts` and `components/chat/*`.
