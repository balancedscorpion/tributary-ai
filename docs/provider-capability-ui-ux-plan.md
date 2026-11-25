# Provider Capability UI/UX — Agreed Implementation Plan

Date: 2026-07-11
Status: **agreed, ready for implementation**
Companion document: `docs/model-provider-compatibility.md` (2026-07-11 audit; the factual basis for the curated capability catalog)

This plan covers the UI/UX and UI-facing contracts for the capability-negotiated provider
architecture. Architecture directions already agreed (backend is the capability authority;
capability is keyed by the exact target tuple; every run gets a persisted preflight execution
plan; no adapter may silently change API surfaces, enable hosted tools, or degrade file
fidelity; Bedrock surfaces and Gemini Developer vs Vertex are distinct; capability states are
supported / unsupported / conditional / unknown with evidence; model discovery with manual
fallback and tiered probes; portable content paired with provider-native continuation state;
incremental migration of manager/engine/SSE/store).

---

## 1. Agreed decisions

Resolutions of the debate points, including refinements accepted during review. Refinements
are marked ▸ and justified inline.

### D-G1 Fidelity reduction — block by default

Any transformation that reduces native document/image fidelity (extracted text instead of
native file, image omission, page-image conversion) **blocks the send** with a preflight
choice unless an explicit fallback policy already covers it. Never silently reduce fidelity.

- Choice scopes: **this run** (one-time) or **this connection** (persisted policy).
- ▸ Connection-scope policies are stored **per transformation class**, not as a blanket
  "always reduce" flag: e.g. `office_docs → extracted_text`, `oversize_pdf → reject`.
  A blanket flag saved during casual use would silently degrade a later citation-critical
  document; per-class policies keep consent meaningful.
- ▸ Even when a saved policy applies, the run-plan row and the affected attachment chip still
  show the ▽ reduced-fidelity marker. Policy removes the *interruption*, never the *evidence*.
- ▸ Policies are scoped to the connection's API surface. If the connection's surface changes
  (e.g. Bedrock `messages` → `responses`), saved policies for the old surface are inert and
  shown as "not applicable" in connection detail, not deleted.
- Per-thread "remember for this conversation" remains available in the preflight sheet for
  users who don't want a connection-wide policy.

### D-G2 Billable / external / execute capabilities — never self-enabling

Hosted web search and hosted code execution (and any future capability classed *billable,
external-egress, or code-execute*) default **Off** on new connections. "Auto" never enables
them. Two independent gates must both be open before the runtime may attach them:

1. **Configuration gate** — the user explicitly set the capability to On for this connection.
2. **Permission gate** — the Permissions policy allows the hosted tool.

- ▸ Consequence for the control model: capability controls come in two classes. The class
  type is deliberately **not** named "billable": it is a neutral **explicit-consent class**
  (`standard` | `explicit_consent`), and *why* consent is required is carried by separate,
  composable **risk tags** (`billable`, `external_egress`, `code_execution`, …) so future
  features (e.g. a free but egress-bearing web fetch) classify correctly.
  - **Class S — standard** (`standard`) — native compaction, structured output, citations,
    prompt caching: tri-state `Auto / On / Off`, default `Auto` (= use when supported &
    verified).
  - **Class B — consent** (`explicit_consent`) — hosted web search, hosted code execution:
    two-state `Off / On` with a shield glyph and risk-tag text. No Auto state exists for
    Class B; offering one would imply the system may decide. When On but unsupported at the
    current target, the row renders `On (set) — ✕ not available here`; the preference
    persists across model switches.
- ▸ Migration exception (grandfathering): existing saved connections whose capability was
  **effectively enabled** — explicitly (`webSearch.enabled: true` /
  `codeExecution.enabled: true`) or by the legacy product default (web search defaults On
  for OpenAI/Anthropic when the field is absent; see §6) — migrate to `On`. Silently turning
  off a capability users rely on is worse than honoring resolved state. Migrated rows show
  an "imported" note in the evidence drawer. **New** connections follow the Off-default.
  The permission gate applies to grandfathered connections identically.
- The Anthropic file→code-execution force-enable (P0 in the audit) becomes impossible to
  express in the UI: a file requiring hosted execution with either gate closed is a preflight
  **blocker** with fix actions, never an implicit enable.

### D-G4 Composer target switching — compact pill, preflight-mediated

- A compact **active-target pill** lives in `ChatInput` (glyph + model + health dot). Click
  opens a lightweight picker listing only **configured connections and their selected/
  discovered models** — no browsing, no creation (a "Manage…" footer deep-links to Settings).
- Switching is allowed **between** runs only. While a run is streaming the pill is disabled
  with reason. An in-flight run is never mutated.
- If a switch loses native continuation or changes effective capabilities relative to the
  thread's previous run, the next send routes through preflight (usually a notice; a blocker
  only if the new target cannot handle pending attachments/requirements).
- ▸ **Material contract impact (called out per instruction):** composer switching means the
  run target can differ from the global `activeProviderConfigId`. Runs must accept an explicit
  per-run target, and the thread stores its last-used target. The global active config remains
  the default for new threads. See §5.4 (`POST /api/threads/{threadId}/runs` gains `target`),
  §5.6 (thread record gains `lastTarget`; `PUT /api/threads/{threadId}/target` persists picker
  selections immediately). Without this, switching in one thread would silently repoint
  every other thread — an unacceptable side effect.
- ▸ The header `providerId:model` badge (`components/layout/Header.tsx:76-78`) is **retired**
  when the pill ships (Phase 3): two interactive target affordances showing the same state is
  duplicate chrome and a divergence risk. The header keeps only the processing indicator.

### D-G5 Bedrock Invoke→Converse — consent when material, notice when covered

- The switch is a preflight **choice** whenever it changes compaction, state continuity,
  citations, fidelity, cost, or limits for this request. In practice: with compaction enabled
  on the connection, the first document attach is always a choice.
- The consent sheet's primary remembered option is
  **"Always use the Converse API for documents on this connection (recommended)"** — this
  writes an attachment-strategy entry (same store as D-G1 policies, class
  `bedrock_documents → converse`). Subsequent document turns show a quiet run-plan notice
  ("Converse API · compaction skipped this turn") instead of asking.
- ▸ If compaction is disabled and no other material change applies (no citations expectation,
  size within limits), the switch is a **notice**, not a choice — the "whenever it changes…"
  condition already permits this; stated explicitly to prevent over-asking.

### D-G9 Branding — family identity at group level, generic surface glyphs

- Provider-family icon/color appears at the **connection-group / connection-card level**
  (current glyph-tile visual language from `providers/catalog/providerDefs.ts` `icon`/`color`
  is preserved; no new external SVG dependencies).
- Surface cards in the editor use **protocol/surface labels** ("Claude · Messages",
  "Chat Completions", "Responses (Mantle)") with one restrained generic surface glyph, not
  repeated third-party logos.
- ▸ This supersedes the earlier "real downloaded brand SVGs" working preference for this
  surface area.

### Remaining points — recommended defaults adopted

| # | Decision | Backend-contract / ordering impact |
|---|----------|-------------------------------------|
| D-G3 | Terminology as specced in §2 ("connection", "Quick check"/"Capability check", "Not available") | None (copy only) |
| D-G6 | Staleness: probe evidence > 30 days renders amber "checked Nd ago" but keeps state; **fingerprint mismatch** flips evidence to Unverified. Curated-catalog verification date surfaced in evidence drawers; CI flags stale catalog dates | **Yes**: `stale` is computed server-side; age policy is a backend constant; catalog entries carry `last_verified_at` + source URLs |
| D-G7 | Probe suite ships 5 of 11 first: transport, tool loop, image, document, structured output. Web/code/thinking-continuation/compaction-replay/citation/cache probes deferred until their adapters are fixed | **Yes (ordering)**: probe API enum covers all 11 keys from day one; unimplemented probes return `verdict:"skipped"` with reason. Prevents a v2 API break |
| D-G8 | Any `payloadOverrides`/`queryParams` present on a connection flags all non-core features as **Unverified** with reason code `override_present` (core = text streaming, system prompt). Overrides are power-user territory; the resolver cannot vouch for their interaction | **Yes**: resolver rule; reason code in contract |
| F-6 | Probe/check evidence persists in a **server-side ledger per connection**, not in the settings JSON. `lastTest*` fields on `ProviderConfig` remain as a derived summary for backward compatibility | **Yes (ordering)**: new persistence lands in Phase 2 backend before evidence UI |
| F-10 | Run-plan rows are **thread-differential**: shown only when the plan differs from the thread's previous run or contains any non-✓ item | None (render rule) |

### Consistency review of the agreed set (traps found and resolved)

1. **G1 × G5 use one mechanism.** Both "fallback policy" (G1) and "attachment strategy" (G5)
   are the same store: `attachmentPolicies` on the connection (§5.6). One concept, one UI
   (connection detail → "Attachment handling"), one preflight integration.
2. **G2 × migration.** Off-by-default for Class B applies to *new* connections only;
   grandfathering preserves existing behavior (see D-G2). Without this, the migration would
   change live behavior — a contradiction with the "no user-visible change on load" guarantee.
3. **G2 × Auto semantics.** "Auto never enables billable" is incompatible with a tri-state
   Auto for hosted tools; resolved by the two-class control model rather than an Auto state
   with fine print nobody reads.
4. **G4 × global active config.** Composer switching with only a global active pointer would
   mutate unrelated threads; resolved with per-run target + per-thread memory (contract
   change, flagged above).
5. **G4 × G5 nag stacking.** A user who switches target *and* attaches a Bedrock document
   could hit two sequential sheets. Preflight is a single consolidated sheet per send —
   blockers, choices, and notices from all sources appear together, never sequential dialogs.
6. **G5 nag frequency.** Compaction-on Bedrock would ask on every document thread; resolved
   by making the connection-scope "always Converse for documents" the primary remembered
   option in the first sheet.
7. **G9 × discoverability.** Generic surface glyphs make the editor's step-1 grid text-heavy;
   mitigated by grouping surface cards under family headers that carry the family glyph tile,
   so scanning is by family first, protocol second.

---

## 2. Principles and terminology

### Principles

P1. **The backend capability report is the only source of capability truth in the UI.** No
    component may branch on `providerId`/`apiType` to decide what a target can do.
P2. **Unknown is never rendered as available.** Gray "Unverified" is an honest, common state.
P3. **Nothing degrades silently.** Fidelity loss, surface switches, and feature drops are
    blocked, chosen, or noticed — always evidenced in the persisted plan.
P4. **Configuration and permission are separate gates**; the UI shows their combined
    effective state and names the closed gate.
P5. **Quiet by default.** A healthy connection and unchanged plan add zero chat chrome.
    Target: >95% of sends on a healthy connection show nothing new.
P6. **Preferences are portable; evidence is not.** Controls/policies survive model switches
    and template copies; probe evidence is bound to the exact target + config fingerprint.
P7. **Everything is inspectable on demand.** Every state chip resolves to evidence (source,
    timestamp, probe/doc reference, raw resolver output) within two interactions.

### Terminology (user-facing; use exactly these words)

| Term | Meaning |
|---|---|
| **Provider connection** ("connection") | Saved authenticated route: family + API surface + endpoint + region + auth |
| **Model** | Selected per connection; connection + model = the capability **target** (internal term) |
| **Quick check** | Transport-level connectivity check (auto-runs; today's "hi" test) |
| **Capability check** | Opt-in tiered probes verifying specific features |
| **On** ✓ | Supported and enabled |
| **Off** ○ | Supported, disabled by user/config |
| **Not available** ✕ | Unsupported at this target — always with a reason |
| **Conditional** ◐ | Works with stated conditions ("PDF only, ≤4.5 MB, max 5") |
| **Unverified** ? | Unknown; no evidence |
| **Blocked** ⛔ | Would work, but the permission gate is closed |
| **Emulated** ≈ (modifier) | Delivered via a different mechanism than native |
| **Reduced fidelity** ▽ (modifier) | Content transformed with information loss |

Never the bare word "test" in UI copy. Timestamps: relative + absolute on hover/focus.

---

## 3. Information architecture and flows

### 3.1 IA map

```
Settings ▸ Providers (tab, inside existing SettingsDialog)
├─ Connections list
│    cards: family tile · name · "family · surface · model" · capability chip-strip · health line
├─ Connection editor (modal wizard, 4 steps)
│    1 Service   — family-grouped surface cards (Bedrock ×3, Compatible ×2 + presets)
│    2 Connect   — auth method, region, resolved endpoint (customizable)
│    3 Model     — discovery list + manual entry + lifecycle badges
│    4 Review    — auto quick check · capability summary · opt-in capability checks · save
├─ Connection detail (non-modal pane, per connection)
│    ├─ Capability summary (CapabilityMatrix, controls live here)
│    ├─ Checks & evidence (probe ledger, staleness, re-run)
│    ├─ Attachment handling (fidelity/surface policies from D-G1/D-G5)
│    └─ Manage (set active default, duplicate, save template, delete)
Chat
├─ ChatInput target pill → target picker (configured targets only)
├─ Preflight sheet (blockers / choices / notices / staleness footer)
├─ Inline notice row (notices-only plans; above composer, non-blocking)
├─ Run plan row (thread-differential, expandable to persisted plan)
└─ Attachment / message ▽ ≈ badges
```

### 3.2 Preflight decision flow

```
Send pressed
  └─ POST /api/threads/{threadId}/runs/preflight (target, message requirements)
       ├─ status: clean            → persist plan, send. No UI.
       ├─ status: notices          → thin amber row above composer; send proceeds.
       │                             Run-plan row shows the notice.
       ├─ status: choices_required → blocking PreflightSheet.
       │     each choice lists options + consequences
       │     scope selector: this run · this conversation · this connection (policy)
       │     [Send with choices] → selections recorded into plan → send
       └─ status: blocked          → blocking PreflightSheet, Send disabled.
             each blocker has a fix action:
             open Permissions · switch model (opens target picker) ·
             remove attachment · re-check capabilities
Saved policy present for an issue → issue downgraded to notice (▽ marker retained).
All issues from all sources render in ONE sheet (no sequential dialogs).
```

### 3.3 Capability state resolution (per feature, per target)

```
catalog/probe/discovery evidence ──► support: supported | unsupported | conditional | unknown
                                          │
config (capabilityControls, class S/B) ──►│  effective decision
permission policy (Class B only) ────────►│
                                          ▼
        ┌───────────────────────────────────────────────────────┐
        │ unsupported ······· ✕ Not available (+reason)         │
        │ unknown ··········· ? Unverified                      │
        │ supported + Off ··· ○ Off                             │
        │ supported + On + permission closed ⛔ Blocked (+link) │
        │ supported + On/Auto(S) + gates open ✓ On              │
        │ conditional + on ·· ◐ Conditional (+conditions)       │
        │ any + override_present (non-core) → forced ? Unverified│
        └───────────────────────────────────────────────────────┘
mode modifier (native | emulated ≈ | reduced ▽) annotates, never replaces, the state.
```

### 3.4 Evidence lifecycle

```
(no evidence) ──catalog entry──► catalog-verified (dated, sourced)
      │                                │
      └──probe run──► probe-verified ──┤
                                        ▼
                        config fingerprint changes (auth/endpoint/surface/region/model
                        placement/overrides) ──► evidence marked stale → rows revert to
                        Unverified with "Config changed since last check" + [Re-check]
                        age > 30d ──► amber "checked Nd ago" (state unchanged)
Policies (attachmentPolicies, capabilityControls) are preferences, NOT evidence:
they survive fingerprint changes; surface changes render inapplicable policies inert.
```

### 3.5 Golden-path flows (acceptance-level narratives)

- **F1 Simple setup (≤90s):** Anthropic · Claude API → paste key → discovery auto-fires →
  pick recommended model → quick check auto-runs → matrix shows Core ✓, Web ○ Off,
  Code ○ Off, Files ◐, several ? → Save. No probes required, nothing fake-green.
- **F2 Generic compatible endpoint:** everything beyond text streaming starts Unverified;
  card badged "Transport only — capabilities unverified"; tiered probes upgrade rows with
  timestamps. Curated presets (Groq/Together/Fireworks/OpenRouter) ship catalog-backed rows.
- **F3 Bedrock document turn:** first attach → consent choice (Converse switch + compaction
  skip); user accepts "always on this connection" → future turns show quiet notice + plan row.
- **F4 Blocked hosted execution:** DOCX needing hosted code with either gate closed →
  blocker with "Open Permissions" / "Remove attachment"; never auto-enabled.
- **F5 Target switch mid-thread:** pill → picker → pick other target → one-time inline
  continuity note; next send preflights; plan row reflects new target.
- **F6 Deprecation:** discovery marks model deprecated → amber on card + pill; shutdown date
  passes → preflight blocker with replacement suggestion.

---

## 4. Component and screen specs

Visual language: existing shadcn/tailwind idiom of `components/settings/*` (card sections,
`text-[11px]` metadata, uppercase group headers). All states below are normative.

### 4.1 `ConnectionCard` (list) — replaces rows at `ProvidersTab.tsx:1366-1448`

```
┌──────────────────────────────────────────────────────────────┐
│ [A]  Work Anthropic                              ★ Active     │
│      Anthropic · Messages · claude-opus-4-6                   │
│      ✓ Tools  ✓ Files  ○ Web  ○ Code  ? 4 unverified          │
│      Checked 2h ago                    [Use] [✎] [🗑]          │
└──────────────────────────────────────────────────────────────┘
```

- Family glyph tile (existing `def.icon`/`def.color`). Line 2 always shows
  family · surface · model — surface is never hidden, for any provider.
- Chip strip: ≤5 chips — Tools, Files, Web, Code + aggregate `? N unverified` (omit when 0).
  Chip = icon + one word; clicking the strip opens **connection detail**, not the editor.
- Health line states: `Checked 2h ago` · `Never checked` · amber `Config changed since last
  check` · amber `Checked 34d ago` · red `Model deprecated — pick a replacement` ·
  red `Last check failed`.
- Keyboard: card is a group; Use/Edit/Delete are discrete buttons with aria-labels including
  the connection name. Delete keeps the current two-step inline confirm.
- Responsive ≤640px: chip strip wraps; action buttons collapse into an overflow menu.
- Empty state: current empty card (`ProvidersTab.tsx:1352-1362`), copy "Add a provider
  connection to connect an LLM API."

### 4.2 Connection editor

Modal wizard in the existing dialog frame; stepper retained. Steps:

**Step 1 — Service.** Family-grouped surface cards:

```
ANTHROPIC [A]                      OPENAI [◎]
  ● Claude API · Messages            ● Responses API
AMAZON BEDROCK [▲]                 GOOGLE [✦]
  ● Claude · Messages                ● Gemini Developer API
  ● Chat Completions                 ⊘ Vertex AI — not yet supported
  ● Responses (Mantle)
OPENAI-COMPATIBLE [⟡]
  ● Chat Completions   ● Responses
  presets: Groq · Together AI · Fireworks AI · OpenRouter
```

- Family header carries the glyph tile (D-G9); surface cards use one generic surface glyph.
- Each surface card: protocol label + one honest positioning line ("Hosted web search and
  code execution are not available on Amazon Bedrock").
- Vertex renders disabled with "not yet supported" (a11y: `aria-disabled`, reason in
  description). Gemini "GCP ADC" auth is removed (it is currently non-functional).
- Presets are shortcuts that pre-fill a Compatible surface + base URL + curated catalog id.

**Step 2 — Connect.**

- Auth method radio cards as today (cloud-cred quick-add flow preserved verbatim).
- Bedrock: region select (curated region list, free-text escape). Endpoint is **resolved by
  the backend per surface** and shown read-only with "Customize…" revealing the base-URL
  field. The compatible composed-URL preview (`ProvidersTab.tsx:1783-1830`) is retained.
- Secret fields keep the immediate-store `SecretField` behavior and existing secrets API.

**Step 3 — Model.**

```
[Search models…                    ] [↻ Refresh]      fetched 2m ago
● claude-opus-4-6      Recommended · 200k ctx
○ claude-sonnet-5      200k ctx
○ claude-haiku-4-5     Deprecated — retires 2027-03-01
Can't find it?  [Enter model ID manually]
```

- States: **loading** (3 skeleton rows) · **discovered** (list + lifecycle badges:
  Recommended / Preview / Deprecated+date / Shut down) · **discovery unavailable** (inline
  note "This endpoint doesn't list models"; manual field promoted to primary) ·
  **discovery failed** (error note + Retry + manual field).
- Manual entries are immediately badged `? Unverified model`; upgraded silently if a later
  discovery matches. Deprecated models selectable with amber inline warning; shut-down
  models rendered but not selectable (`aria-disabled`, reason text).
- Listbox semantics (`role=listbox`, arrow-key navigation, type-ahead).

**Step 4 — Review & check.**

- Quick check **auto-runs on step entry** (abortable; re-run button). Result panel retains
  the current success/failure layout (`ProvidersTab.tsx:1994-2019`).
- CapabilityMatrix (§4.3) rendered read-only beneath it.
- "Run capability checks" expander: checkbox list of probes with annotations —
  Tool loop (2 small requests) · Image input (1 request) · Document input (1 request) ·
  Structured output (1 request) · Hosted web search ⚠ *real billed web query* ·
  Hosted code execution ⚠ *runs code in the provider sandbox, billed*.
  Class B probes are unchecked by default, disabled when either gate is closed, and require
  the explicit consent checkbox (`consentAcknowledged`) before Run.
- Save enabled after quick check passes. Save-without-pass allowed via an **inline confirm
  panel** (replacing `window.confirm` at `ProvidersTab.tsx:1001`): "Quick check hasn't
  passed. Chats may fail. [Save anyway] [Keep editing]". Template naming likewise becomes an
  inline field (replacing `window.prompt` at `:1018`).

### 4.3 `CapabilityMatrix`

Used in: editor step 4 (read-only), connection detail (interactive), target-pill popover
(compact, top rows + "View all"), preflight sheet (filtered to relevant rows).

```
CORE
  Streaming text          ✓ On                        probe · 2h ago  ▸
  Custom tools (local)    ✓ On                        probe · 2h ago  ▸
  Parallel tool calls     ◐ Conditional  runs serially    catalog     ▸
ATTACHMENTS
  Images                  ✓ On                            catalog     ▸
  PDF / documents         ◐ Conditional  ≤4.5MB · max 5 · Converse ▸
HOSTED TOOLS  🛡
  Web search              [Off ▾]   ○ Off                             ▸
  Code execution          [On ▾]    ⛔ Blocked by permissions · Manage ▸
REASONING & STATE
  Extended thinking       ? Unverified                                ▸
  Native compaction       [Auto ▾]  ✕ Not available on this surface   ▸
  Session continuation    ✕ Not available                             ▸
OUTPUT
  Structured output       [Auto ▾]  ? Unverified                      ▸
  Citations               ≈ Emulated — flattened to links             ▸
```

- **Row anatomy:** feature name · control (if controllable) · state chip (icon+word) ·
  condition/reason text · evidence source + age · drawer affordance ▸.
- **Controls:** Class S = `Auto / On / Off` select (default Auto). Class B = `Off / On`
  select with shield; disabled with "Blocked by permissions — Manage" (deep-links to the
  Permissions tab via existing `initialTab`/tab-switch plumbing) when the permission gate is
  closed. Setting On where unsupported renders `On (set) — ✕ not available here`.
- **Semantics/a11y:** native `<table>` (or `role=table`) with row headers; state conveyed by
  icon **and** text, never color alone; controls are labeled selects ("Web search: Off");
  control changes announce via a `role=status` live region; drawer affordance is a button
  ("Show evidence for Web search").
- **Evidence drawer** (three-zone inspector, matching the audit-trail inspector family):
  1. **Verdict** — restated state + conditions in a sentence.
  2. **Evidence** — ordered entries: probe (id, timestamp, duration, request id, summary) ·
     catalog (version, `last_verified_at`, official doc link) · discovery ·
     `override_present` / `imported` annotations.
  3. **Raw** — the `FeatureResolution` JSON, copyable.
- Responsive ≤640px: table collapses to stacked rows (name+chip line, control+evidence line).
- Loading: skeleton rows. Error (report fetch failed): inline error card with Retry; the UI
  never substitutes guessed capabilities.

### 4.4 `PreflightSheet`

Focus-trapped dialog. Section order fixed: **blockers → choices → notices → staleness footer**.

```
Before sending
⛔ report.docx requires hosted code execution, which is blocked by
   your permissions.                     [Open Permissions] [Remove attachment]
▽  slides.pptx can't be sent natively on Bedrock Converse.
   (•) Send as extracted text — layout and images are lost
   ( ) Remove attachment
   Scope: (•) this run  ( ) this conversation  ( ) always on this connection
ℹ  Attaching documents switches this run to the Converse API.
   Native compaction is skipped for this turn.
   ☐ Always use the Converse API for documents on this connection (recommended)
────────────────────────────────────────────────────────
Capabilities last checked 41 days ago                    [Re-check]
                                  [Cancel]   [Send with choices]
```

- Send disabled while any blocker remains. Every fix action resolves in place (removing an
  attachment re-runs preflight without closing the sheet).
- Accepted choices are written into the persisted plan; connection-scope selections write
  `attachmentPolicies` entries; affected attachment chips carry a permanent ▽ badge with a
  tooltip naming the transformation.
- A11y: `role=alertdialog`, labelled sections, Send-disabled reason via `aria-describedby`,
  radio groups per choice, focus returns to composer on close.
- Notices-only plans do **not** open the sheet: they render as one thin amber row above the
  composer (dismissible, `role=status`), and appear in the run-plan row.

### 4.5 `TargetPill` + `TargetPicker` (ChatInput)

- Pill: `[A] claude-opus-4-6 ·` health dot (green/amber/red per §4.1 health states).
  Placement: leading edge of the composer toolbar. `aria-label`:
  "Model: claude-opus-4-6 on Work Anthropic. Change model."
- Disabled while a run streams (tooltip "Wait for the current response to finish").
- Picker (popover listbox): configured connections as groups, their models as options;
  lifecycle/health badges inline; current target checked; footer "Manage providers…" opens
  Settings ▸ Providers. No inline creation or editing.
- Selecting a target updates the thread's target immediately (`PUT /api/threads/{threadId}/target`,
  §5.6; for a thread not yet created server-side, the selection is client-held and persists with
  thread creation / first run) and
  shows the one-time continuity note under the composer when native continuation is lost:
  "Continuing portably. Native session state from Anthropic · claude-opus-4-6 won't transfer."
- Compact capability popover (6 headline rows + "View all") opens from a chevron affordance
  inside the picker row, not from the pill itself (single-purpose pill click).

### 4.6 `RunPlanRow` + `plan_update` system rows

- Run-plan row: first line of a run, collapsed:
  `Plan · Bedrock Converse · claude-… · files ▽ · compaction skipped  ▸`
  Rendered only when the plan differs from the thread's previous run **or** contains any
  non-✓ item (thread-differential rule). Expanding shows the persisted plan rendered with the
  matrix component filtered to the features the run actually used, plus choice records.
- `plan_update` events render as thin system rows inside the run ("File sent as extracted
  text ▽"), claimed by a dedicated renderer in the existing envelope→renderer dispatch; never
  chat bubbles or toasts.
- Assistant messages/artifacts produced under ≈/▽ carry a subtle footer glyph; artifact
  provenance stores the plan id.

### 4.7 Connection detail pane

Non-modal second pane inside the Providers tab (list ⇄ detail navigation, back button,
breadcrumb "Providers / Work Anthropic"). Sections: CapabilityMatrix (interactive) · Checks &
evidence (probe ledger table: probe, verdict, when, duration, request id; Re-run per row and
"Run all safe checks") · Attachment handling (policy list per transformation class with
scope/surface, revoke buttons; inert policies grayed with "not applicable to current
surface") · Manage (set as default, duplicate, save as template, delete).

---

## 5. Backend/UI contracts

All endpoints JSON over the existing HTTP API. Errors follow the current
`{error|message}` shape (`hooks/useSettings.ts:26-34`).

### 5.1 Capability report

```
GET /api/providers/connections/{id}/capabilities?model={m}
POST /api/providers/capabilities/resolve        # pre-save drafts: body = {config, model}
```

```ts
interface CapabilityReport {
  target: {
    connectionId: string | null;              // null for pre-save drafts
    providerFamily: 'openai'|'anthropic'|'gemini'|'bedrock'|'compatible';
    apiSurface: 'responses'|'messages'|'generate_content'|'invoke_stream'
              |'converse_stream'|'chat_completions';
    endpointFamily: string;                   // 'bedrock-runtime' | 'bedrock-mantle' | …
    model: string;
    region: string | null;
    adapterVersion: string;
  };
  fingerprint: string;                        // config fingerprint resolved against
  catalogVersion: string;                     // curated catalog release id
  generatedAt: string;                        // ISO-8601
  features: Record<FeatureKey, FeatureResolution>;
}

// FeatureKey is an OPEN string identifier, not a closed union. The server ships a
// feature registry defining every key it may emit; each resolution echoes its registry
// metadata (`meta`), and the UI MUST render keys it doesn't recognize generically from
// that metadata. Adding a feature is a registry + catalog data change — never a schema
// migration on this contract.
type FeatureKey = string;

// v1 registry — covers the full minimum capability inventory of
// docs/model-provider-compatibility.md:444-458:
//   core:      text_streaming · text_nonstreaming · system_instructions
//   tools:     custom_tools · tool_choice · strict_tool_schemas · parallel_tools
//              · tool_state_preservation
//   inputs:    images · documents                     (carry structured `limits`)
//   hosted:    hosted_web_search · hosted_code_execution · hosted_computer_use
//              · provider_generated_files
//   reasoning: reasoning_request_control · thinking_display · thinking_state_preservation
//   output:    structured_output · citations
//   state:     native_compaction · session_continuation · prompt_caching · cache_reporting
//   ops:       token_counting · context_limits · rich_usage
//   safety:    safety_controls

interface FeatureMeta {                       // echoed from the server registry
  label: string;                              // user-facing name
  group: 'core'|'tools'|'inputs'|'hosted'|'reasoning'|'output'|'state'|'ops'|'safety'|string;
  headline: boolean;                          // true → eligible for chip-strips / compact popover
  order: number;                              // stable sort within group
}

type CapabilityClass = 'standard'|'explicit_consent';   // Class S / Class B (D-G2)
type RiskTag = 'billable'|'external_egress'|'code_execution'|string;

interface FeatureResolution {
  meta: FeatureMeta;
  class: CapabilityClass;
  riskTags: RiskTag[];                        // non-empty iff class='explicit_consent'
  state: 'supported'|'unsupported'|'conditional'|'unknown';
  mode: 'native'|'emulated'|'reduced' | null;
  effective: 'on'|'off'|'blocked'|'unavailable';
  control: { setting: 'auto'|'on'|'off'; controllable: boolean } | null;   // Class B: never 'auto'
  permission: { required: boolean; granted: boolean; policyRef: string | null } | null;
  conditions: Array<{ code: string; text: string }>;   // 'max_documents_5', 'pdf_only', …
  reasons: Array<{ code: string; text: string }>;      // 'not_on_bedrock', 'override_present',
                                                       // 'adapter_gap', 'permission_blocked',
                                                       // 'requires_converse_switch', …
  limits: {                                   // structured constraints (inputs, context/output)
    mimeTypes: string[] | null; maxBytes: number | null; maxCount: number | null;
    requestBudgetBytes: number | null;        // e.g. 20 MiB marshalled-JSON guard
    contextTokens: number | null; maxOutputTokens: number | null;
  } | null;
  detail: Record<string, unknown> | null;     // key-specific structured extras (forward-compat;
                                              // rendered in the evidence drawer's raw zone)
  evidence: Array<{
    kind: 'catalog'|'probe'|'discovery'|'docs'|'imported';
    verifiedAt: string | null;
    ref: string | null;                       // probe id, doc URL, catalog entry id
    stale: boolean;                           // fingerprint mismatch OR age > policy
    staleReason: 'fingerprint'|'age' | null;
  }>;
}
```

UI rule (P1): components consume `FeatureResolution` only. The `providerCanConfigureHostedTools`
inference (`ProvidersTab.tsx:809-843`) is removed; equivalent validation moves server-side.

### 5.2 Model discovery

```
POST /api/providers/connections/{id}/models/discover
POST /api/providers/models/discover             # pre-save: body = {config}
```

```ts
interface ModelDiscoveryResult {
  source: 'provider_api'|'curated'|'unavailable';
  fetchedAt: string;
  error: string | null;                       // set when source='unavailable' due to failure
  models: Array<{
    id: string; label: string | null;
    lifecycle: 'recommended'|'available'|'preview'|'deprecated'|'shutdown';
    deprecationDate: string | null; replacement: string | null;
    contextTokens: number | null; maxOutputTokens: number | null;
  }>;
}
```

### 5.3 Checks (extends `POST /api/providers/test`, `provider_test_endpoint.go`)

```ts
interface ProviderCheckRequest {
  config?: ProviderConfig;                    // pre-save draft (existing shape)
  connectionId?: string;                      // saved connection
  model?: string;
  tier: 'quick'|'capability';
  probes?: FeatureKey[];                      // required when tier='capability'
  consentAcknowledged?: boolean;              // must be true if probes ∩ Class B ≠ ∅
}
interface ProviderCheckResult {
  ok: boolean;                                // quick: transport ok; capability: all ran
  fingerprint: string;
  results: Array<{
    probe: FeatureKey | 'transport';
    verdict: 'verified'|'unsupported'|'failed'|'skipped';
    reason: string | null;                    // for skipped/failed; e.g. 'probe_not_implemented'
    message: string; requestId: string | null; durationMs: number;
  }>;
}
```

- The probe enum covers all 11 audit probes from day one; unimplemented ones return
  `skipped` / `probe_not_implemented` (D-G7).
- Class B (consent-class) probes are refused server-side (`400`) without
  `consentAcknowledged:true` **and** both gates open — the server enforces D-G2, not just
  the UI.
- Verified/unsupported results are persisted to the evidence ledger keyed by
  (connectionId, model, fingerprint, probe).

### 5.4 Preflight and runs

Run creation today is thread-nested: `POST /api/threads/{threadId}/runs`
(`server/internal/chat/httpapi/handler.go:130`; client at `hooks/useChat.ts:4091`).
Preflight mirrors it as an additive sibling route:

```
POST /api/threads/{threadId}/runs/preflight
  body: { target: { connectionId, model }, requirements: {
           attachments: [{ id, mime, sizeBytes }], features: FeatureKey[] } }
```

```ts
interface PreflightResult {
  planId: string;                             // persisted DRAFT plan, server-side (TTL below)
  requirementsHash: string;                   // run creation revalidates against this
  status: 'clean'|'notices'|'choices_required'|'blocked';
  blockers: PlanIssue[];
  choices: Array<PlanIssue & {
    options: Array<{ id: string; label: string; consequence: string;
                     policyClass: string | null }>;   // set → offer connection-scope save
    allowedScopes: Array<'run'|'thread'|'connection'>;
  }>;
  notices: PlanIssue[];
  staleEvidence: boolean;
  plan: CapabilityReport;                     // resolved for THIS request's requirements
}
interface PlanIssue {
  issueId: string;                            // STABLE unique id within the plan — the key for
                                              // choiceSelections. Codes repeat (two attachments
                                              // can both raise 'fidelity_reduction'); issueId
                                              // never does.
  code: string;                               // 'fidelity_reduction','surface_switch',
                                              // 'hosted_tool_blocked','model_shutdown',
                                              // 'continuation_loss','tool_loop_incompatible',…
  text: string;
  feature: FeatureKey | null; attachmentId: string | null;
  fix: { kind: 'open_permissions'|'switch_model'|'remove_attachment'|'recheck' } | null;
}
```

```
POST /api/threads/{threadId}/runs   # existing route, additive fields
  + planId: string
  + target: { connectionId, model }           // per-run target (D-G4)
  + choiceSelections: Record<string /* issueId */, { optionId: string;
                                                     scope: 'run'|'thread'|'connection' }>
```

**Finalization protocol (choices → policies → plan → run).** Draft plans expire after a
TTL (15 min, backend constant); expired/unfinalized drafts are garbage-collected. On run
creation the server executes, in order:

1. **Validate** — plan exists and is unexpired; connection `configFingerprint` unchanged;
   `requirementsHash` matches the submitted message; every `choices_required` issue has a
   selection whose `optionId`/`scope` is among that issue's options/`allowedScopes`.
   Any failure → `409 plan_stale` (client re-preflights); never a partial apply.
2. **Apply consented policies** — `connection`-scope selections write `attachmentPolicies`
   entries to the config store; `thread`-scope selections write thread policies to the chat
   store. Writes are idempotent (keyed by `class`+`action`+scope). Policies record user
   consent, so they are **not rolled back** if run creation subsequently fails; a retry with
   the same plan simply finds them already applied.
3. **Finalize atomically** — in a single chat-store transaction: mark the plan `finalized`
   with the selection records embedded, stamp the thread's `lastTarget`, and create the run
   referencing `planId`. Finalized plans are retained with the run for the run-plan row and
   `GET /api/runs/{id}/plan`.

Cross-store note: the config store (settings JSON) and the chat store cannot share one
transaction; the ordering plus idempotent policy writes make retries safe by construction.
A run whose `planId` is missing, expired, or divergent is rejected — there is no
plan-optional path.

### 5.5 Run-stream event (existing SSE envelope)

```ts
{ event: 'plan_update', data: {
    planId: string,
    change: { kind: 'surface_switch'|'fidelity_reduction'|'feature_unavailable'
                   |'continuation_lost',
              feature: FeatureKey | null, attachmentId: string | null, text: string } } }
```

Also: `GET /api/runs/{id}/plan` returns the persisted `CapabilityReport` + choice records for
the run-plan expander.

### 5.6 Settings/schema additions (TS `types.ts` + Go `config.ProviderConfig`; all additive)

```ts
interface ProviderConfig {
  // …existing fields unchanged…
  apiSurface?: string;                        // canonical surface; derived from providerId+apiType
                                              // for legacy configs (see §6)
  capabilityControls?: Partial<Record<FeatureKey, 'auto'|'on'|'off'>>;
  attachmentPolicies?: Array<{                // unified D-G1 + D-G5 store
    id: string;
    class: string;                            // 'office_docs_to_text','oversize_pdf_reject',
                                              // 'bedrock_documents_converse', …
    action: string;                           // class-specific action id
    apiSurface: string;                       // surface the policy was saved under
    createdAt: number;
  }>;
  configFingerprint?: string;                 // stamped by backend on save
}
// Thread record (chat store):
interface ThreadRecord {
  /* existing fields */
  lastTarget?: { connectionId: string; model: string };
  attachmentPolicies?: Array<{                // 'this conversation' consents (same entry
    id: string; class: string; action: string;   //  shape as connection policies)
    apiSurface: string; createdAt: number;
  }>;
}
```

Thread-scoped contracts:

```
PUT /api/threads/{threadId}/target    body: { connectionId, model }
```

- Persists a composer target switch immediately (survives reload), independent of sending.
  For a thread that does not yet exist server-side, the client holds the selection and it
  persists with thread creation / the first run.
- Thread `attachmentPolicies` are written **only** during run-creation finalization (§5.4
  step 2) — cancelling the preflight sheet persists nothing. There is no separate thread
  policy endpoint; deleting a thread policy happens via the connection-detail "Attachment
  handling" pane's thread section (a small `DELETE` on the thread record is included in the
  chat-store work, §8).

Evidence ledger is server-side per connection (F-6), **not** in the settings JSON; existing
`lastTest*` fields become a derived summary maintained for backward compatibility.

### 5.7 Feature flags (rollout)

`featureFlags.capabilityUx: { report: boolean; editorV2: boolean; controls: boolean;
preflight: boolean; composerTarget: boolean }` — additive to `FeatureFlags`
(`types.ts:238-256`), default false, flipped per phase. Flag off ⇒ exact current behavior.

---

## 6. Migration behavior for existing configs

Guarantee (every phase): a settings blob saved by the previous release loads with **zero
user-visible change** and re-saves without data loss. Enforced by a fixture-corpus round-trip
test (fixtures generated from current `ensureProviderConfigs` output per provider family,
including the synthesized `legacy-default` config, `services/settingsMigration.ts:1000`).

| Legacy state | Migration |
|---|---|
| `providerId`+`apiType` | Derive `apiSurface`: openai→`responses`; anthropic→`messages`; gemini→`generate_content`; bedrock+messages→`invoke_stream` (runtime may switch to converse per plan); bedrock+chat_completions/responses and compatible pass through. Original fields untouched (legacy mirror keeps working) |
| `webSearch.enabled` / `codeExecution.enabled` explicit | `true`→`capabilityControls[feature]='on'` + `imported` evidence note; `false`→`'off'` (Class B has no auto). Booleans preserved for the mirror |
| `webSearch` **absent** | Migrate to the *resolved legacy effective default*, not blanket Off: OpenAI/Anthropic configs default web search **On** today (`resolveWebSearchConfig`/`defaultWebSearchConfigForProvider`, `services/settingsMigration.ts:181-205`; Go `defaultWebSearchConfig("anthropic")` injection in `settings.go`) → `'on'` + `imported` note "product default (imported)". Bedrock/Gemini/compatible absent → `'off'`. D-G2's Off-by-default applies **only to connections created in the new editor**; the TS/Go default-injection paths must be gated to legacy blobs (absence of `capabilityControls`) so they can never re-enable web search on new connections |
| `codeExecution` **absent** | `'off'` everywhere (the legacy effective default is off for all providers) |
| `compactionEnabled` | `true`→`'on'`, `false`→`'off'`, absent→`'auto'` (Class S) |
| Gemini configs with `gcp_adc` auth | Load intact; editor shows a non-blocking banner "This auth method is not functional — switch to API key"; never auto-mutated |
| Dead default models (e.g. `gemini-2.0-flash`) | Not rewritten; discovery/lifecycle marks them `shutdown` ⇒ preflight blocker with replacement suggestion (F6) |
| `lastTest*` fields | Read as a legacy quick-check evidence entry (kind `imported`); continue to be written as derived summary |
| Templates (`ProviderTemplate`) | Copy `capabilityControls` + `attachmentPolicies`; never copy evidence or fingerprints |
| Legacy mirror (`applyProviderConfigToLegacySettings`, `lib/providerConfigRuntime.ts:191-325`) | Retained and extended-by-omission (new fields don't mirror); removal is out of scope for this plan |

Downgrade safety: all new fields are optional/additive; an older build ignores them. Policies
and controls saved by a newer build survive a downgrade round-trip because the migration
never strips unknown keys from `providerConfigs` entries (verify in round-trip test).

---

## 7. Implementation phases (dependency order)

Phases ship independently behind `featureFlags.capabilityUx.*`. Backend items within a phase
precede UI items.

### Phase 0 — Capability vocabulary + backend report (read-only)

**Backend**
- Curated capability catalog as versioned data (encode the §Upstream/§Repository matrices of
  `docs/model-provider-compatibility.md` with `last_verified_at` + source URLs per entry).
- Resolver + `GET /capabilities` / `POST /capabilities/resolve` (§5.1), including
  `override_present` rule (D-G8) and Class B gate composition (permission read from the
  existing permissions policy store). Expand/replace `capabilities.ModelCapabilities`
  (`server/internal/chat/providers/capabilities/capabilities.go:38`) — one matrix, not two.
- Config fingerprint computation + stamping on settings save.

**UI**
- `lib/capability/` (types mirroring §5.1, state-format helpers), `services/capabilityApi.ts`,
  `hooks/useCapabilityReport.ts`.
- `CapabilityChip`, `CapabilityMatrix` (read-only, no controls), `EvidenceInspector` drawer.
- Render matrix in wizard step 4 under the existing test result; chip-strip on connection
  cards. Remove all capability *display* inference from `ProvidersTab`.

**Acceptance criteria**
1. Report resolves for all 8 runtime surfaces; Bedrock reports hosted web/code `unsupported`
   with reason `not_on_bedrock`; Gemini-3-family models report `custom_tools` unsupported
   with reason `adapter_gap`.
2. No UI path renders green from `unknown`; overrides force non-core rows to Unverified.
3. Matrix + drawer meet a11y spec (§4.3) — axe clean, keyboard-only operable.
4. Flag off ⇒ pixel-identical current Providers tab.

**Tests**
- Go: resolver table tests per target tuple × feature (catalog fixtures); fingerprint
  stability tests; override rule tests.
- RTL: matrix row snapshot per state (7 states × modifiers); drawer zones.
- Playwright (`tests/e2e/provider-capability-matrix.spec.ts`): Bedrock wizard shows "Not
  available on Amazon Bedrock"; compatible endpoint shows Unverified sweep; evidence drawer
  opens with catalog source. Add named gate `npm run test:provider-surfaces`.

### Phase 1 — Editor restructure: surfaces, connect, model discovery

Depends on Phase 0 (matrix in step 4, backend endpoint generation).

**Backend**
- Correct per-surface endpoint generation (Bedrock runtime `/v1`, Mantle base, model-id
  guidance) exposed via a `resolveEndpoint` field in the capabilities/resolve response or a
  small dedicated endpoint; discovery API (§5.2) with per-family sources (OpenAI models API,
  Gemini models API, Bedrock model/inference-profile listing, OpenRouter models; curated
  lists elsewhere) + lifecycle data.

**UI**
- Step 1 family-grouped surface cards (Bedrock ×3, Compatible ×2 + presets; Vertex disabled;
  Gemini ADC removed from `providerDefs.ts`).
- Step 2 region select + resolved-endpoint read-only + customize.
- Step 3 discovery list + manual fallback + lifecycle badges; dead defaults removed from
  `providerDefs.ts` (model defaults come from discovery's `recommended`).
- Replace `window.confirm`/`window.prompt` with inline panels. Editing a saved config
  hydrates into the correct surface card (`apiType`→surface mapping).

**Acceptance criteria**
1. Creating each Bedrock surface produces the documented-correct endpoint; connection cards
   show family · surface · model for every provider.
2. Discovery failure path still allows manual save; deprecated model warns; shut-down model
   unselectable; manual model badged Unverified.
3. Every fixture config loads into the new editor and re-saves byte-equivalent (modulo
   `updatedAt`/`apiSurface`/fingerprint).
4. No `window.confirm`/`window.prompt` remain in ProvidersTab flows.

**Tests**
- settingsMigration round-trip corpus; surface-derivation unit tests.
- Playwright: wizard e2e per surface with mocked discovery/check; ADC-legacy banner;
  a11y pass on all four steps.

### Phase 2 — Tri-state controls, tiered checks, evidence ledger

Depends on Phase 0 (report), Phase 1 (editor step 4 is the main mount point).

**Backend**
- Evidence ledger persistence (per connection+model+fingerprint+probe).
- Check API tiering (§5.3): quick (existing "hi" path) + 5 probes (transport, tool loop,
  image, document, structured output); Class B refusal rule; results → ledger; staleness
  computation (age 30d, fingerprint).
- `capabilityControls`/`attachmentPolicies`/fingerprint fields in `config.ProviderConfig`
  + normalization (additive). **The Bedrock hosted-tool safety strips are RETAINED in this
  phase — both locations**: the legacy-mirror strip (`server/internal/config/settings.go:1868-1869`)
  and the providerConfigs strip (`:2096-2097`). The resolver reports the same fact as
  `unsupported`/`not_on_bedrock`, so nothing is hidden from the UI, but the strips remain
  the runtime safety boundary until capability-aware factory/engine gating is active
  (Phase 3). Removing either strip before gating would let a stored `on` control reach a
  Bedrock request.
- Evidence ledger persisted via `server/internal/chat/store` (new entities in `store.go`
  interface, `json.go`, `mysql.go` + SQL migration) with JSON/MySQL parity contract tests
  (pattern: `server/internal/chat/store/context_checkpoint_contract_test.go`).

**UI**
- Controls in CapabilityMatrix (Class S tri-state, Class B two-state + shield + permission
  deep-link); connection detail pane (matrix interactive, probe ledger, attachment-handling
  policy list); capability-check expander in step 4 with billable acknowledgement;
  stale/fingerprint states throughout.

**Acceptance criteria**
1. Class B rows can never reach `effective:'on'` without both gates; server refuses
   consent-class probes without `consentAcknowledged` (verified by contract test, not just
   UI); with the strips still in place, a Bedrock request payload never contains hosted-tool
   config regardless of stored controls (regression-guarded).
2. `webSearch.enabled:true` legacy config → control On + imported note; new connection →
   Off (D-G2 + grandfathering).
3. Editing base URL/auth flips all probe evidence to stale-fingerprint; matrix reverts rows
   to Unverified with re-check CTA.
4. Probe run upgrades `custom_tools` unknown→supported with timestamp on a compatible
   endpoint (mocked).

**Tests**
- Go: gate-composition matrix (support × control × permission — 12 combos); ledger CRUD +
  staleness; probe skip/refusal contract tests.
- RTL: control interactions incl. disabled/blocked reasons + live-region announcements.
- Playwright: probe flow, permission deep-link round trip, stale-after-edit scenario.

### Phase 3 — Preflight plan + runtime surfacing

Depends on Phase 2 (policies/controls/evidence feed the resolver).

**Backend**
- Chat-store schema work (§8): plan + issue/choice records with TTL/GC, thread `lastTarget`,
  thread `attachmentPolicies` — `store.go` interface, `json.go`, `mysql.go`, SQL migration,
  JSON/MySQL parity contract tests.
- Preflight endpoint + finalization protocol (§5.4); `POST /api/threads/{threadId}/runs`
  accepts `planId`/`target`/`choiceSelections`; `PUT /api/threads/{threadId}/target`;
  engine emits `plan_update` (§5.5); `GET /api/runs/{id}/plan`.
- Enforcement moves server-side: Anthropic file→code-exec bypass and Bedrock surface switch
  become plan items; engine refuses undeclared deviations. **Capability-aware factory/engine
  gating for hosted tools lands here**; only after it is verified does the providerConfigs
  strip (`settings.go:2096-2097`) come out, guarded by a regression test proving a Bedrock
  run with a stored `on` control never dispatches a hosted tool. The legacy-mirror strip
  (`:1868-1869`) stays for the life of the legacy mirror (§9).

**UI**
- `PreflightSheet`, inline notice row, `RunPlanRow` + `plan_update` renderer, attachment ▽/≈
  badges, `TargetPill`/`TargetPicker` in `components/ChatInput.tsx`, header badge retired,
  continuity note on target switch.

**Acceptance criteria**
1. F3/F4/F5 narratives (§3.5) pass end-to-end.
2. Clean sends on a healthy connection add zero new chrome (P5 measured in e2e).
3. Every run's plan retrievable from its plan row; choice records included and keyed by
   `issueId` (two attachments with the same issue code resolve independently).
4. Saved connection policy converts the Converse choice to a quiet notice; ▽ markers persist.
5. Target picker disabled mid-stream; switching never affects other threads' targets; a
   picker switch survives app reload (`PUT …/target`).
6. Run creation against an expired/divergent plan returns `409 plan_stale` and the client
   re-preflights; retrying after a mid-protocol failure never duplicates policies
   (idempotency test); JSON and MySQL stores pass the same plan/policy contract suite.

**Tests**
- Go: preflight decision table (requirements × capabilities × policies → status);
  plan/choice persistence; run-rejection on stale plan.
- RTL: sheet section ordering, scope selector, fix-action re-preflight; renderer unit tests
  for `plan_update`.
- Playwright: blocker/choice/notice/clean scenarios; thread-differential plan-row rule;
  pill switch + continuity note; screenshot tests light/dark for badges.

### Phase 4 — Lifecycle: continuation pairing, deprecation, curation

Depends on Phase 3.

- Continuation pairing UX (restore note when returning to a target with valid native state —
  requires the backend continuation-state persistence work from the architecture plan).
- Deprecation lifecycle: amber card/pill, preflight blocker at shutdown with replacement.
- Curated capability profiles for the 4 presets; preset cards show catalog-backed rows.
- Template flow copies controls/policies (§6).

**Acceptance criteria**: F6 narrative passes; OpenRouter preset shows curated rows instead of
blanket Unverified; switching away and back to a still-valid native session shows the restore
note and the plan row confirms native continuation.

**Tests**: date-based lifecycle unit tests (injected clock), preset catalog snapshots,
e2e thread switch-away/switch-back.

---

## 8. Work map (files/packages)

### Frontend — new

| Path | Contents |
|---|---|
| `lib/capability/types.ts` | §5 contract types (single source; server JSON mirrors) |
| `lib/capability/format.ts` | state→icon/word/color mapping, timestamp formatting |
| `services/capabilityApi.ts` | report + resolve + plan retrieval clients |
| `services/modelDiscoveryApi.ts` | §5.2 client |
| `services/providerCheckApi.ts` | tiered checks (supersedes `providerTestApi.ts` internals; keep module name/exports for callers, extend) |
| `hooks/useCapabilityReport.ts` | fetch/cache/stale handling per target |
| `components/settings/providers/ConnectionsList.tsx`, `ConnectionCard.tsx` | §4.1 |
| `components/settings/providers/ConnectionEditor.tsx` + `steps/ServiceStep.tsx`, `ConnectStep.tsx`, `ModelStep.tsx`, `ReviewStep.tsx` | §4.2 |
| `components/settings/providers/ConnectionDetail.tsx` | §4.7 |
| `components/settings/providers/CapabilityMatrix.tsx`, `CapabilityChip.tsx`, `EvidenceInspector.tsx` | §4.3 |
| `components/chat/PreflightSheet.tsx`, `PreflightNoticeRow.tsx` | §4.4 |
| `components/chat/TargetPill.tsx`, `TargetPicker.tsx` | §4.5 |
| `components/chat/RunPlanRow.tsx`, `PlanUpdateRow.tsx` | §4.6 |

### Frontend — modified

- `components/settings/ProvidersTab.tsx` — becomes a thin shell routing list ⇄ editor ⇄
  detail; wizard logic decomposes into the new step components.
- `providers/catalog/providerDefs.ts` — surface-card catalog; remove Gemini `gcp_adc`, dead
  model defaults, hosted-tool `def:true`; keep field metadata for Connect/Advanced groups.
- `providers/catalog/presets.ts` — preset→surface + curated-catalog id.
- `types.ts` — §5.6 additions.
- `services/settingsMigration.ts` — §6 mappings + round-trip fixtures.
- `lib/providerConfigRuntime.ts` — surface derivation; badge helper retired in Phase 3.
- `components/layout/Header.tsx` — badge removal (Phase 3).
- `components/ChatInput.tsx` — pill mount, capability-aware attach affordances, preflight
  integration.
- `components/settings/SettingsDialog.tsx` — detail-pane navigation, footer rules for the
  new editor (extend existing `providerWizardOpen` pattern).
- `featureFlags.ts` / flag plumbing — `capabilityUx.*`.

### Backend (Go) — new/modified

| Package | Work |
|---|---|
| `server/internal/chat/providers/capabilities` | Expand into resolver + curated catalog data (versioned, `last_verified_at`, source URLs); target-tuple keying; Class B gate composition |
| `server/internal/capability` (new) or subpackage | Evidence ledger store; staleness policy; fingerprint computation |
| `server/internal/server/provider_test_endpoint.go` | Tiering, probe dispatch, consent-class refusal, ledger writes |
| `server/internal/server/handler.go` | Settings-side routes: capabilities, resolve, discover |
| `server/internal/chat/httpapi/handler.go` | Thread/run-side routes: `POST /api/threads/{threadId}/runs/preflight`, additive run-creation fields (`planId`/`target`/`choiceSelections`), `GET /api/runs/{runId}/plan`, `PUT /api/threads/{threadId}/target` (existing run routes at `:130-133`) |
| `server/internal/chat/store` — `store.go`, `json.go`, `mysql.go` + SQL migrations | Evidence ledger; plan + issue/choice records (draft TTL 15 min, GC of unfinalized drafts, finalized plans retained with runs); thread `lastTarget` + thread `attachmentPolicies`; JSON/MySQL parity contract tests (pattern: `context_checkpoint_contract_test.go`) |
| `server/internal/chat/providers/discovery` (new) | Per-family model discovery + lifecycle |
| `server/internal/config/settings.go` | §5.6 fields + normalization; gate legacy web-search default injection to legacy blobs; **retain** both Bedrock hosted-tool strips (`:1868-1869`, `:2096-2097`) until Phase-3 gating; remove only `:2096-2097` after gating is regression-verified |
| `server/internal/chat/engine`, `server/internal/chat/runs` | Plan finalization protocol + enforcement, capability-aware hosted-tool gating, `plan_update` emission, per-run target |

### Tests

- `tests/e2e/provider-capability-matrix.spec.ts`, `provider-connection-editor.spec.ts`,
  `provider-capability-checks.spec.ts`, `provider-preflight.spec.ts`,
  `provider-target-switch.spec.ts` — aggregated as `npm run test:provider-surfaces`.
- Migration fixtures under `tests/regressions/fixtures/provider-settings/`.

---

## 9. Explicitly deferred scope

- Vertex AI surface (UI shows disabled card only; no auth/config work).
- Probes 6–11 (hosted web, hosted code, thinking continuation, compaction replay, citations,
  cache accounting) until their adapters are fixed; API enum reserves them.
- Legacy mirror removal (`applyProviderConfigToLegacySettings`) and legacy
  `openai/anthropic/gemini/bedrock/compatibleProfiles` field retirement — including the
  legacy-mirror Bedrock strip at `settings.go:1868-1869`, which lives and dies with the
  mirror.
- Full-page settings app; the dialog frame stays.
- Composer full model dropdown / model browsing or creation in the picker.
- Per-message target overrides (target is per-thread/per-run only).
- Capability-aware cost estimation/usage dashboards (usage-detail plumbing is a resolver
  feature key but no UI beyond the matrix row).
- Structured-output/strict-tools/prompt-caching *configuration* UI beyond the Class S control
  (their adapter work is separate).
- Brand SVG asset system (D-G9 keeps glyph tiles).

---

## 10. Risks, telemetry/diagnostics, rollout/rollback

### Risks

| Risk | Mitigation |
|---|---|
| Curated catalog drifts from provider reality | Dated, sourced entries; CI staleness flag on `last_verified_at`; probes override catalog per target; evidence drawer always shows provenance |
| Preflight adds perceived latency to Send | Resolver is local (catalog+ledger+config), no provider round-trip on the clean path; budget <50ms p95; preflight result cached per (thread, target, requirements-hash) |
| Nag fatigue (G1/G5 blocking) | Connection-scope policies as primary remembered options; single consolidated sheet; notices-only path never blocks; P5 e2e guard |
| Migration regression on saved configs | Fixture-corpus round-trip test in every phase; additive-only schema; flags default off |
| Two sources of "active" target (global vs thread) confuse users | Pill always shows the *thread's* target; Settings "Active" badge copy becomes "Default for new chats" (Phase 3 copy change) |
| Grandfathered web-search On surprises cost-sensitive users | Imported note in evidence drawer + one-time settings banner listing grandfathered capabilities on first load after Phase 2 |
| ProvidersTab decomposition destabilizes secrets flows | Secrets/cloud-cred components reused verbatim; dedicated e2e for secret set/clear in the new editor before removing old paths |

### Telemetry / diagnostics (local-first app — structured local logs, no external analytics)

- Log (under existing `debug` setting, plus always-on counters in the evidence ledger):
  preflight outcomes by status/issue code; plan_update kinds; probe verdict rates and
  durations; discovery source/failure rates; policy creations/revocations.
- Diagnostics surface: connection detail "Checks & evidence" is the user-facing ledger;
  Advanced tab gains a "Provider diagnostics" export (report + ledger + recent plans as JSON)
  for bug reports.
- Every plan and probe result carries request IDs where providers supply them.

### Rollout / rollback

- Per-phase flags (`featureFlags.capabilityUx.*`), default off; enable in dev → dogfood →
  default-on per phase. Flag off restores exact prior behavior (Phase-0 AC-4 guards this).
- Rollback = flip flag off. Schema is additive-only, so no data migration reversal is needed;
  newer fields sit inert under older UI (round-trip test verifies no stripping).
- Kill criteria per phase: any migration-fixture failure or a clean-path send showing new
  chrome blocks default-on.

---

## 11. First implementation slice (ticket-sized)

**Ticket: Capability report v0 for Anthropic + Bedrock, rendered read-only in the provider
wizard (flag: `capabilityUx.report`).**

Scope:
1. Go: catalog data file covering the `anthropic/messages` and `bedrock/invoke_stream` +
   `bedrock/converse_stream` targets — features per §5.1 enum, states/conditions/reasons
   transcribed from `docs/model-provider-compatibility.md` (§“What works in this repository”
   tables), each entry with `last_verified_at: 2026-07-11` and source URL.
2. Go: v1 **feature registry** (all §5.1 keys with meta/class/riskTags — data only; catalog
   entries exist just for the three slice targets) + resolver composing catalog + current
   config (webSearch/codeExecution/compaction booleans mapped to controls; permission gate
   read; `override_present` rule) → `CapabilityReport`;
   `POST /api/providers/capabilities/resolve` accepting a draft `ProviderConfig` (same
   request pattern as `provider_test_endpoint.go`). No ledger, no fingerprint (return
   `fingerprint:"v0"`), no persistence.
3. TS: `lib/capability/types.ts` + `format.ts`; `services/capabilityApi.ts`;
   `CapabilityChip` + read-only `CapabilityMatrix` + `EvidenceInspector` (catalog/docs
   evidence kinds only).
4. Mount: wizard step 4 below the existing test panel, only when the flag is on and
   `selProv ∈ {anthropic, bedrock}`.

Acceptance:
- Bedrock drafts show web/code "✕ Not available — not offered on Amazon Bedrock"; Anthropic
  draft with codeExecution disabled shows "○ Off".
- **Bedrock documents are surface-exact — no generic row may imply a silent switch:** the
  `bedrock/invoke_stream` target shows documents
  "◐ Conditional — requires switching to the Converse API (asked before sending)" with
  reason code `requires_converse_switch`; the `bedrock/converse_stream` target shows
  "◐ Conditional · PDF/Office · ≤4.5 MB each · max 5 (native)" via the `limits` field.
- Payload-override present → structured output/citations/etc. forced "? Unverified
  (override present)".
- Evidence drawer shows catalog date + doc link; raw zone shows resolver JSON.
- Flag off ⇒ zero DOM change (snapshot-guarded).
- Tests: Go resolver table test (3 targets × all features), RTL row states, one Playwright
  spec (`provider-capability-matrix.spec.ts`) driving the wizard with the flag on.

Non-goals for the slice: controls, probes, discovery, other families, connection cards,
persistence. Estimated as one focused PR per side (backend, frontend) or one combined PR.

---

## 12. Open items intentionally *not* blocking implementation

- Exact staleness constant (30d) and preflight-cache TTL — backend constants, tune in dogfood.
- Copy review of all state/reason strings (tracked as a Phase-1 polish task; strings are
  centralized in `lib/capability/format.ts` to make this cheap).
- Whether the Settings "Active" badge copy change ("Default for new chats") lands in Phase 3
  with the pill or earlier — cosmetic, decide at Phase-3 kickoff.
