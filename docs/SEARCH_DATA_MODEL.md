# Searchable Threads/Artifacts — Data Model (v1)

This doc defines a **canonical, versioned JSON data model** for storing chat threads, turns, and artifacts in a way that is:

- **Easy to search** (lexical now; embeddings later)
- **Easy to export** (a “JSON DB layer” as the system of record)
- **Easy for an AI retriever to consume** (clear IDs, links, and derived “documents”)

It is intentionally scoped to **data quality + structure first**. Query tools and the “Search AI” layer come later.

---

## Goals

1. **Canonical JSON as the source of truth**
   - Thread history, tool interactions, and artifacts are stored as JSON records with stable IDs.
2. **Clear turn structure**
   - A “turn” is the unit a search system usually wants (user intent + assistant work + tool outputs).
3. **Incremental, append-friendly persistence**
   - Streaming updates should not require rewriting giant monolithic files.
4. **Future-proof for indexing**
   - The model should support later adding derived search documents, embeddings, tags, and summaries without breaking old data.

## Non-goals (for v1)

- Building the query/index engine.
- Building the Search AI agent.
- Choosing a particular vector DB / embedding model.

---

## Core concepts

### IDs

Use UUIDs (string) for all primary IDs:

- `thread_id`, `run_id`, `message_id`, `artifact_id`, `project_id`, `job_id`

IDs are immutable once created.

### Timestamps

Store timestamps in **UTC** and serialize as RFC3339 (Go `time.Time`) in the JSON DB layer.

API DTOs can still use `UnixMilli()` if desired, but the persisted JSON should prefer RFC3339 for readability and diff-friendliness.

### Turns

Define a **turn** as:

> The user message that starts a model execution + the resulting assistant/tool messages and artifacts produced during that execution.

In this codebase, that maps naturally to **`run_id` as the turn key**:

- One run is created per user message.
- Messages appended during that execution share the same `run_id`.

So: **Turn = Run + Messages (by `run_id`) + Artifacts (by `tool_call_id` / `message_id`)**.

---

## Canonical record types (storage layer)

All stored JSON records should include:

```json
{
  "kind": "ai_analyst.<type>",
  "schema_version": 1
}
```

### Thread (`ai_analyst.thread`)

Minimum fields:

```json
{
  "kind": "ai_analyst.thread",
  "schema_version": 1,
  "id": "uuid",
  "title": "string",
  "summary": "string|null",
  "project_id": "uuid|null",
  "status": "active|archived",
  "created_at": "2026-02-11T12:34:56.789Z",
  "updated_at": "2026-02-11T12:35:56.789Z",
  "last_message_at": "2026-02-11T12:35:56.789Z|null",
  "last_run_id": "uuid|null",
  "counters": {
    "next_seq": 1,
    "message_count": 0,
    "artifact_count": 0,
    "run_count": 0,
    "summary_run_count": 0
  },
  "tags": ["optional", "strings"]
}
```

Notes:

- `counters.next_seq` supports deterministic, gap-free per-thread sequencing without a relational lock.
- `tags` is optional but provides a low-effort lever for later search/discovery.

### Run / Turn (`ai_analyst.run`)

Runs are the natural “turn envelope”.

```json
{
  "kind": "ai_analyst.run",
  "schema_version": 1,
  "id": "uuid",
  "thread_id": "uuid",
  "status": "queued|running|completed|failed|cancelled",
  "purpose": "analysis|search",
  "provider_id": "string",
  "model": "string",
  "profile_id": "string|null",
  "system_prompt": "string",
  "max_iterations": 200,
  "user_message_id": "uuid|null",
  "created_at": "RFC3339",
  "started_at": "RFC3339|null",
  "ended_at": "RFC3339|null",
  "updated_at": "RFC3339",
  "usage": {
    "request_id": "string|null",
    "input_tokens": 0,
    "output_tokens": 0,
    "total_tokens": 0
  },
  "error": {
    "code": "string",
    "message": "string"
  }
}
```

Notes:

- `purpose` is forward-looking: the Search AI can reuse provider settings but run under different instructions/tools.
- `user_message_id` makes turn reconstruction fast without scanning messages.

### Message (`ai_analyst.message`)

Messages are the canonical, ordered event stream inside a thread.

```json
{
  "kind": "ai_analyst.message",
  "schema_version": 1,
  "id": "uuid",
  "thread_id": "uuid",
  "run_id": "uuid|null",
  "seq": 42,
  "role": "system|user|assistant|tool",
  "content": "string|null",
  "thinking": "string|null",
  "thinking_complete": true,
  "tool_calls": [{ "id": "string", "name": "string", "args": {} }],
  "tool_call_id": "string|null",
  "name": "string|null",
  "ui": {
    "hidden": false,
    "referenced_artifact_ids": ["uuid"],
    "hidden_context": "string|null"
  },
  "created_at": "RFC3339",
  "updated_at": "RFC3339"
}
```

Notes:

- `ui.*` fields prevent UI-only behavior from being implicit/unstored (important for later “Search AI” reproduction and debugging).
- Tool-call structure should remain **typed** (avoid storing tool calls as raw strings).

### Artifact (`ai_analyst.artifact`)

Artifacts are first-class retrievable objects and should be independently addressable.

```json
{
  "kind": "ai_analyst.artifact",
  "schema_version": 1,
  "id": "uuid",
  "thread_id": "uuid",
  "message_id": "uuid|null",
  "type": "data|code|image",
  "title": "string",
  "language": "string|null",
  "tool_name": "string|null",
  "tool_call_id": "string|null",
  "ok": true,
  "content": {
    "storage": "inline|file",
    "text": "string|null",
    "path": "relative/path|null",
    "byte_size": 1234,
    "sha256": "hex|null"
  },
  "metadata": {},
  "created_at": "RFC3339"
}
```

Notes:

- Prefer storing a `sha256` hash when content is file-backed; it improves integrity checks and later indexing.
- Store `path` as a **clean, relative path** under the app config directory.

---

## On-disk JSON DB layout

Recommended on-disk layout under the app config directory (example: `~/.config/aianalyst/`):

```
chatdb/
  meta.json
  threads/
    <thread_id>/
      thread.json
      runs/
        <run_id>.json
      messages/
        0000000000000042_<message_id>.json
      artifacts/
        <artifact_id>.json
  indexes/
    threads.jsonl
    documents.jsonl
```

Notes:

- **Per-message files** allow cheap streaming updates (rewrite one file, not an append-only log that needs patching).
- Filenames include `seq` for fast ordered listing without opening every JSON file.
- Global `indexes/*.jsonl` files are **derived** and can be rebuilt by scanning `threads/*`.

---

## Derived “search documents” (for query tools later)

Introduce a derived record type to flatten thread content for retrieval:

```json
{
  "kind": "ai_analyst.search_doc",
  "schema_version": 1,
  "id": "uuid",
  "thread_id": "uuid",
  "run_id": "uuid|null",
  "message_id": "uuid|null",
  "artifact_id": "uuid|null",
  "title": "string",
  "text": "string",
  "created_at": "RFC3339",
  "updated_at": "RFC3339",
  "tags": ["optional"]
}
```

Typical docs:

- One per **turn** (user + assistant + tool results merged as text)
- One per **artifact** (title + a safe-size excerpt + metadata)

This keeps retrieval tooling decoupled from the core schema and makes it easy to:

- implement grep-style tools first
- add embeddings later without rewriting canonical history

---

## RLM-style retrieval compatibility

The RLM paper’s core idea (“prompt as environment”) maps cleanly onto this data model:

1. Give the Search AI a small “thread index” context (titles, timestamps, tags).
2. Provide tools like:
   - `list_threads(filters…)`
   - `get_thread(thread_id)`
   - `list_messages(thread_id, page_token?, limit?)` (copy the returned `next_page_token`; raw sequence bounds are for explicit windows only)
   - `get_run(run_id)`
   - `get_artifact(artifact_id)` (plus safe excerpting)
3. Let the Search AI decide which slices to load and summarize.

Practical cautions from the paper:

- Code-execution loops are powerful but need sandboxing and can be slow/costly without async batching.
- Models vary a lot in their ability to manage external context efficiently.

Recommendation: treat RLM as an **optional advanced retrieval mode**, not the only retrieval path. Start with derived `search_doc` + simple query tools, then consider RLM-style recursive summarization as a higher-cost fallback when “easy search” fails.

---

## Versioning and migrations

- `chatdb/meta.json` stores the active schema version.
- Each record stores its own `schema_version`.
- Migrations are:
  1) Load record
  2) Transform to latest schema
  3) Rewrite atomically

Keep migrations small and additive when possible (e.g., adding `purpose` to runs, adding `sha256` to artifacts).
