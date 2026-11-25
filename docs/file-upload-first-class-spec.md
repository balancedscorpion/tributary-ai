# First-Class Uploaded-File Experience — Review & Implementation Spec

Status: reviewed 2026-07-02. This document is both the UI/purpose review of the
current uploaded-file experience and the implementation specification for the
next iteration.

Reference note: the repo-local UI reference used for visual direction is
`images/DatasetUI.png`: a dark, quiet AI Analyst workspace with restrained
amber actions, compact composer-first layout, and minimal chrome. The review
also uses the current app UI plus the repo's own design documents:
`plans/imageattachments.md` (the phase-1 client-side image plan this spec
supersedes), `docs/main-file-type-pipelines.md`,
`docs/spreadsheet-file-pipeline.md`, and `docs/powerpoint-file-pipeline.md`.

---

## 1. Goals

1. Every user-uploaded file goes through the first-party upload API
   (`POST /api/threads/{threadId}/attachments`) — nothing smuggled into prompt
   text or kept in local-only state.
2. The upload card reliably shows preview/status in the composer while pending
   and later in the chat transcript, including after reload.
3. All file families are covered: PDF, text/markdown/code, CSV/TSV/JSON/HTML,
   spreadsheets, documents, presentations, images, archives, and
   unsupported/binary files.
4. Anthropic and OpenAI/OpenAI-compatible (plus Bedrock/Gemini) get native file
   inputs where their APIs support them.
5. Context limits are protected: no full-file dumps into prompt context.
   Bounded previews + native file inputs + attachment tools for retrieval.
6. Drag & drop into the composer is a first-class upload entry point.

## 2. Current Architecture (as-is review)

### 2.1 Upload flow — already first-party for files

```
ChatInput (picker / drop / paste)
  └─ App.handleAttachFile (App.tsx:670)
      └─ POST /api/threads/{id}/attachments?name=...   (raw body stream)
          └─ httpapi.handlePostThreadAttachment (thread_attachments.go:24)
              ├─ fileingest.Ingest    → cfgDir/thread_uploads/{threadId}/{artifactId}.{ext}
              │    (MIME sniff + declared/extension MIME, SHA-256, 32 KiB text preview,
              │     128 MiB cap, safe file name)
              ├─ filepipeline.ExtractFile → {artifactId}.extract/ sidecars
              │    (manifest.json, markdown/data/text sidecars, per-family metadata)
              └─ store.InsertArtifact (type "file", tool_name thread_file_attachment)
      └─ response {artifact, attachment} → pendingFiles[] → FileAttachmentCard
```

On send, the composer passes `fileAttachments` (metadata + `artifactId` refs
only — no bytes) in the run payload; the backend re-hydrates everything from
the stored artifact (`optionc/file_attachments.go`). This is the correct
first-party shape.

### 2.2 Model context — already bounded

- `buildFileAttachmentTurnContext` (file_attachments.go:288) injects a compact
  manifest per attachment: name, MIME(s), size, `native_file_input=true|false`,
  plus an extracted-text preview **only when native input is unavailable**,
  capped at 24,000 chars per file and 72,000 chars per turn.
- Native file input hydration (`hydrateFileAttachmentInlineData`) inlines
  original bytes capped at 50 MiB per file / 50 MiB per turn; oversized files
  fall back to the bounded preview + tools.
- Retrieval-style access exists via thread attachment tools:
  `inspect_attachment`, `read_attachment` (windowed reads over
  markdown/data/manifest sidecars), `search_attachment`.
- Images are capped at 4 per turn with per-image and total byte limits
  (`maxModelImageAttachmentCount`, manager.go).

Verdict: context-limit protection is genuinely implemented; the spec work is
to preserve it, not to rebuild it.

### 2.3 Provider matrix (as implemented)

| Provider / endpoint | Native file path | Images | Fallback |
|---|---|---|---|
| Anthropic Messages | `document` content blocks (PDF & text docs base64); Files API upload + `file_id` when required; `image` blocks for image-kind attachments | native `image` blocks (base64) | bounded extracted preview in turn context |
| OpenAI Responses (`apiType: responses`) | `input_file` (`file_data` base64) for eligible files — default mode `native_supported` | `input_image`/`image_url` data URIs | preview text sidecar |
| OpenAI Chat Completions / compatible | `file` content parts, default mode `native_pdf_only` (PDF only); per-profile `fileInputMode`: `native_supported` \| `native_pdf_only` \| `sidecar_only` | `image_url` data URIs | `requestWithFileAttachmentFallbackText` retry path when endpoint rejects file parts |
| Bedrock (messages) | native document types (pdf, txt, md, html, csv, doc(x), xls(x)) capped at 4.5 MB | image blocks | bounded preview |
| Gemini | inline data for pdf/text-like | inline image data | bounded preview |

Mode resolution follows existing config (`ProviderConfig.FileInputMode`,
`CompatibleProfile.FileInputMode`) — local config, no env vars, per AGENTS.md.

### 2.4 File-family pipeline coverage (as implemented)

Dispatch order (`filepipeline/pipeline.go`): presentation → spreadsheet → PDF →
document (DOCX/RTF/DOC) → image → archive → text (text/markdown/json/jsonl/
html/xml). Every extractor writes `manifest.json` + markdown + data sidecars;
unsupported/binary files fail closed with honest metadata
(`status: failed`, explicit unsupported message) and remain inspectable via
tools. Legacy `.doc` is explicitly unsupported. Archives are inventoried, never
extracted.

## 3. Review findings (gaps)

FUF-1 **No uploading/progress/error state in the composer.** The pending card
only appears after the upload finishes (`handleAttachFile` awaits `fetch`
before mutating `pendingFiles`). With a 128 MiB cap, a slow upload gives the
user zero feedback; failure is a transient toast with no retry affordance and
nothing left in the composer.

FUF-2 **Transcript cards are not reload-reliable.** `MessageList` resolves user
attachments only through `referencedArtifactIds → artifactsById`. On thread
reload each referenced artifact is seeded as a generic placeholder
(`makePlaceholderArtifact`, type `code`, title "Artifact") until an async
`GET /api/artifacts/{id}` resolves; if that fetch fails the transcript shows a
meaningless "Artifact" chip forever. The persisted `file_attachments` snapshot
on the message (types.go:163) is never used by the UI (`normalizeMessage`
doesn't map `file_attachments`/`image_attachments`).

FUF-3 **Uploaded images have no visual preview.** All images now route through
the file upload API (ChatInput's legacy dataURI branch is dead because
`onAttachFile` is always provided), which is correct — but a `file` artifact's
GET returns preview text only. There is no raw-bytes endpoint, so neither
composer nor transcript can show an image thumbnail. Vision still works
(backend converts referenced image files into native image blocks), but the UI
shows a generic text card for a screenshot.

FUF-4 **The card itself is undifferentiated.** One `FileText` icon for every
kind; no status states; no thumbnails; CSV/code previews render as an
unstyled wall of text; "Preview unavailable" and "unsupported binary" look
identical to a happy-path card.

FUF-5 **Vestigial dual image path.** `onAttachImage`/`pendingImages`/
`handleImageFile` (ChatInput.tsx:129) is dead code in the composer, but its
error copy ("Max 10 MB") and `ACCEPTED_MIME_TYPES` gate still mislead readers.
The `imageAttachments` run-payload path (base64 in JSON) remains for backend
compatibility/steer surfaces but must not be a UI upload path.

FUF-6 **Drag & drop is composer-only and leak-prone.** Dropping a file a few
pixels outside the composer navigates the browser away from the app (default
drop behavior is not prevented at window level). The overlay doesn't indicate
multi-file drops. Paste only handles images, not files.

FUF-7 **Send/upload race (introduced by fixing FUF-1).** Once cards are
optimistic, send must exclude in-flight/error attachments (the existing
`getFileAttachmentArtifactId` filter drops empty artifactIds — keep that
behavior — and the send button must communicate "waiting for upload").

## 4. Specification

### 4.1 Attachment lifecycle & state model

Extend `ChatFileAttachment` (types.ts) with UI-lifecycle fields:

```ts
status?: 'uploading' | 'uploaded' | 'error';  // absent ⇒ uploaded (back-compat)
localId?: string;        // stable key before artifactId exists
progress?: number;       // 0..1, from XHR upload progress
errorMessage?: string;   // human-readable failure
thumbnailUrl?: string;   // object URL (pending) or /api/artifacts/{id}/raw
```

`App.handleAttachFile`:
1. Validate size (>128 MiB → toast, stop).
2. Immediately append `{localId, status:'uploading', fileName, mimeType,
   sizeBytes, progress:0, thumbnailUrl?}` to `pendingFiles` (object URL for
   `image/*`).
3. Upload via `XMLHttpRequest` (same endpoint/headers as today) to get
   `upload.onprogress` → update `progress`.
4. Success → replace entry in place (same index/localId) with the server
   attachment, `status:'uploaded'`; register artifact. Image thumbnails switch
   to `/api/artifacts/{id}/raw` and the object URL is revoked.
5. Failure → `status:'error'`, `errorMessage`; keep the original `File` in a
   ref keyed by `localId` so Retry can re-upload in place.

Send behavior: attachments with `status !== 'uploaded'` are excluded from the
run payload (empty `artifactId` already guarantees this); if any attachment is
still `uploading`, the send button is disabled with tooltip "Waiting for
upload…". Error cards do not block send; sending clears them with an
informational toast.

### 4.2 FileAttachmentCard states & anatomy

One component serves the composer (removable) and transcript (clickable).

```
┌──────────────────────────────────────────────┐
│ [kind icon/thumb]  filename.ext          ✕/👁 │
│                    PDF · 2.4 MB · 12 pages   │
│  ┌────────────────────────────────────────┐  │
│  │ preview zone (per-state, see below)    │  │
│  └────────────────────────────────────────┘  │
│  status note                    [View]/[Retry]│
└──────────────────────────────────────────────┘
```

- **Kind system** (glyph-only; the theme is monochromatic amber, so every
  kind keeps the existing warm `bg-artifact-file/10 text-artifact-file` tile
  and color is reserved for state): pdf/document/text `FileText` ·
  spreadsheet/csv `FileSpreadsheet` · presentation `Presentation` · code
  `FileCode` · json/data `FileJson` · image `FileImage` (or thumbnail) ·
  archive `FileArchive` · unknown/binary `FileQuestion`. Resolution order:
  `fileKind` → MIME → filename extension → unknown.
- **State lives in the icon tile**: uploading → `Loader2` spinner; error →
  tile flips to `bg-destructive/10 text-destructive` with `AlertTriangle` and
  the card border becomes `border-destructive/40`.
- **uploading**: meta line gains "Uploading… 42%" (not a live region);
  progress is a full-bleed `h-0.5` bar on the card's bottom edge
  (`role="progressbar"` with `aria-valuenow`, omitted when indeterminate);
  the Remove button doubles as Cancel and aborts the in-flight XHR.
- **uploaded**: metadata line (`MIME label · size`); preview zone (markdown
  for pdf/markdown/math, `<pre>` otherwise, `<img>` thumbnail for images).
- **error**: destructive strip (`border-destructive/20 bg-destructive/5`,
  mirroring the compacting-banner idiom) with the message text and a Retry
  action; `role="alert"`; Retry moves focus to the still-mounted Remove
  button.
- **variant="compact" | "full"** on one component: compact (composer grid)
  clamps text previews to `max-h-16` and collapses spreadsheet/data previews
  to meta-only; full (transcript) keeps the complete preview zone.
- **preview unavailable / unsupported binary**: honest copy — "No inline
  preview — the assistant can still inspect this file with attachment
  tools." — in a dashed inner border, distinct from a truncated preview.
- **Announcements**: one persistent composer-level `sr-only` polite live
  region announces attach/uploaded/failed transitions ("x.pdf uploaded,
  ready to send."); per-card live regions are intentionally absent (progress
  ticks would flood screen readers, and unmounting nodes swallow the
  completion message).
- Long names: `truncate` + `title` attr (existing).
- Transcript variant: same card minus Remove, plus click-to-open artifact
  panel; image attachments render an `<img>` thumbnail sourced from
  `/api/artifacts/{id}/raw`.

### 4.3 Transcript reliability (fixes FUF-2)

1. `normalizeMessage` maps `file_attachments`/`fileAttachments` and
   `image_attachments`/`imageAttachments` onto the UIMessage.
2. `UserMessage`/`SteerMessage` accept `fileAttachments` snapshots. Rendering
   rule per attachment: if `artifactsById[artifactId]` is hydrated (not
   placeholder), render the interactive artifact card; otherwise render the
   snapshot card (non-interactive but fully labeled — name, kind, size,
   preview text from the snapshot). No generic "Artifact" chips for file
   attachments at any point.
3. Placeholder artifacts for file attachments keep `type: 'file'` (not
   `code`) so the fallback path renders the right card shape.

### 4.4 Raw-bytes endpoint (enables FUF-3) — implemented in `httpapi/artifact_raw.go`

`GET /api/artifacts/{artifactId}/raw` (httpapi):
- Only for thread file-attachment artifacts (`fileartifact.IsFileAttachmentArtifact`)
  and image-attachment artifacts; 404 otherwise.
- Serves the stored bytes from `cfgDir`-relative `ContentPath` with
  `Content-Type` = stored normalized/sniffed MIME (fallback
  `application/octet-stream`), `Content-Disposition: inline; filename=...`,
  `X-Content-Type-Options: nosniff`, and `Cache-Control: private, max-age=3600`
  (content is immutable per artifact id).
- Path safety via the existing `safeConfigRelativePath`.

The UI uses it for image thumbnails; it also enables a future download action.

### 4.5 Drag & drop (fixes FUF-6)

- Keep the composer counter-based overlay; overlay copy becomes
  "Drop to attach" / "Drop N files to attach" using `dataTransfer.items`.
- Add window-level `dragover`/`drop` preventDefault (App-level effect) so a
  missed drop never navigates away. Drops outside the composer are ignored,
  not uploaded.
- Drop works while a run is streaming (attachments join the steer composer
  state) and with mixed image + file batches — all through the same
  `handleAttachFile`.
- Paste: retain image paste; also accept non-image `File` items on the
  clipboard (e.g. Finder copy) through the same path.

### 4.6 First-party enforcement (fixes FUF-5)

- In the main app, `ChatInput` is mounted with `onAttachFile` and not the
  legacy inline-image `onAttachImage` path, so picker, paste, and drop all
  route through `POST /api/threads/{threadId}/attachments`.
- Keep the `ImagePlus` button as an image-filtered picker over the same file
  path (render when `onAttachFile` is present).
- The fallback `onAttachImage`/`pendingImages` props remain for embedding
  surfaces that intentionally mount `ChatInput` without first-party upload,
  but the primary app composer does not use them. Backend `imageAttachments`
  support is unchanged for compatibility with external API callers.

### 4.7 Context-limit rules (unchanged, restated as contract)

- Never inline full file bytes/text into prompt text from the frontend; the
  run payload carries `artifactId` references + display metadata only.
- Backend turn context: ≤24k chars preview per file, ≤72k per turn, previews
  omitted whenever native file input is attached.
- Native input caps: 50 MiB per file/turn (provider-specific tightenings:
  Bedrock 4.5 MB, image caps 4/turn) — oversized files degrade to preview +
  `inspect/read/search_attachment` tools, and the turn context says so
  honestly (`native_file_input=false`).

### 4.8 Tests

Frontend (build gate): `npx tsc --noEmit` and `npm run build`.

Backend (go test):
- `httpapi/thread_attachments_test.go` — existing upload coverage; add a test
  for `GET /api/artifacts/{id}/raw` (file attachment 200 + content-type,
  non-attachment 404, traversal-safe).
- `optionc` — existing `file_attachments`/`manager` tests still green.
- `filepipeline` — existing per-family tests still green.

Manual QA checklist:
1. Pick/drop/paste each family (pdf, md, py, csv, xlsx, docx, pptx, png, zip,
   random `.bin`) → card shows uploading → uploaded with correct icon,
   metadata, preview or honest no-preview note.
2. Kill the backend mid-upload → error card with Retry; Retry succeeds.
3. Send with a mix of uploaded + uploading (send disabled) and uploaded +
   error (error skipped, toast).
4. Reload the thread → transcript cards render immediately from snapshots,
   upgrade to interactive cards after hydration; image thumbs load from /raw.
5. Drop a file outside the composer → nothing navigates.
6. Anthropic + OpenAI-compatible runs: verify `native_file_input=true` files
   are not double-injected as preview text (turn context) and the model
   answers from the file.

## 5. Out of scope (documented for future phases)

- OCR and external-tool extraction tiers (`EnableExternalTools`) — designed in
  `docs/main-file-type-pipelines.md`.
- Recursive archive child extraction.
- Provider file-storage reuse across runs beyond the existing Anthropic Files
  API cache.
- Upload cancellation mid-flight (Retry/Remove cover the practical cases; XHR
  abort is a small follow-up).
