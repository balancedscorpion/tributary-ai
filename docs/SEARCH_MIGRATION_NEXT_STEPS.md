# Migration + Next Steps (JSON DB → Searchable Threads)

This project’s current server-side chat persistence is **MySQL-backed** (Option C). The goal is to make threads/artifacts **first-class JSON records** so that:

- the app can persist without requiring MySQL, and
- retrieval tooling (and later a Search AI) can consume a clean JSON layer.

This doc outlines a staged path that keeps the scope aligned with:

1) data model correctness  
2) query tools  
3) Search AI integration

---

## Stage 1 — Canonical JSON DB (data quality first)

### 1. Implement a file-backed store

Add a new store implementation (server) that persists the canonical records from `server/internal/chat/jsondb/` under:

```
<config-dir>/chatdb/
```

Implementation notes:

- Use atomic writes (see `server/internal/atomicfile/atomicfile.go`).
- Keep messages as **one JSON file per message** so streaming updates rewrite a single small file.
- Treat **`run_id` as the “turn key”** and ensure all messages created during a run carry that `run_id`.
- Store artifact *metadata* in the JSON DB, but continue storing large artifact *content* in file storage (the server already supports content paths).

### 2. Wire server startup to prefer JSON DB when MySQL is absent

Today, `/api/threads` is disabled unless MySQL is configured. A simple, user-friendly behavior would be:

- If MySQL is configured: use MySQL store
- Else: use JSON DB store under `chatdb/`

This makes “works out of the box” possible while keeping MySQL as an optional upgrade.

### 3. Record-level validation

Before writing:

- Validate required fields (`id`, `thread_id`, `seq`, `role`, etc.)
- Enforce per-thread `seq` uniqueness
- Enforce artifact/thread/message referential integrity on write (best-effort, local store)

---

## Stage 1.5 — Export-friendly “turn views” (still no query engine)

Even before building query tools, add a deterministic *export* view to feed later retrieval:

- `turns.jsonl`: one JSON line per run/turn with:
  - user text
  - assistant final answer text
  - tool calls (names + args)
  - artifact ids + titles + safe excerpts

This is a **derived** artifact that can be regenerated from canonical records.

---

## Stage 2 — Query tools (no AI yet)

Start with simple, predictable tools:

1. `search_docs(query, filters…)` over derived `search_doc` records (JSONL scan)
2. `get_thread(thread_id)` / `get_run(run_id)` / `list_messages(thread_id, …)` / `get_artifact(artifact_id)`
3. Optional: lightweight inverted index built incrementally (still stored as JSON)

Keep the first version “dumb but correct” and add speed later.

---

## Stage 3 — Search AI

Introduce a distinct “Search” mode that:

- uses the same provider connection settings (same keys/base URLs)
- uses a separate system prompt and tool allowlist
- prioritizes **retrieval + citation of record IDs** (thread/run/message/artifact IDs) over long-form analysis

---

## Where RLM fits (optional advanced retrieval)

RLM’s “prompt as environment” idea maps to this architecture if the Search AI:

- starts from a small index context (thread titles + timestamps)
- uses tools to pull slices of canonical JSON records on-demand
- recursively summarizes candidates when needed

Recommended use: keep RLM-like recursion as a *fallback* for hard queries after cheap lexical narrowing, because it can be slower and more model-dependent than direct indexing.

---

## Migration strategies (MySQL → JSON DB)

There are two clean paths:

### A) New installs: no migration

If JSON DB is the default store, new data is written in the new format automatically.

### B) Existing installs: one-time export

Add a server command (or admin endpoint) that:

1. Reads threads/runs/messages/artifacts/projects from the MySQL store
2. Writes canonical JSON records under `chatdb/`
3. Copies/retains artifact content files (content paths already exist for file-backed artifacts)

Keep this exporter idempotent by using stable IDs and writing per-record files.

