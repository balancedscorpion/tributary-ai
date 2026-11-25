# Provider & Cloud Credentials Flow Redesign Plan

## Goal

Simplify first-run and ongoing provider configuration while preserving advanced configurability:

- Merge **Provider** + **Profiles** into a single provider-card experience.
- Introduce a dedicated **Cloud Credentials** tab for reusable AWS/GCP account-level auth.
- Keep **System Prompt** and **Advanced** tabs unchanged.
- Ensure provider setup is wizard-driven, testable, and collapses into cards after save.
- Keep Bedrock/Compatible configuration fully expressive (advanced options preserved).
- Add feature flags around provider availability and provider-card actions.
- Allow provider templates to be saved and reused.

## Current State Review (Code Anchors)

- Settings tabs currently include `Provider`, `Profiles`, `System Prompt`, `Advanced`, and `Google Cloud`, with provider and profile concerns split across two tabs. (`components/settings/SettingsDialog.tsx`)
- Provider runtime settings currently support `openai`, `anthropic`, `gemini`, and `compatibleProfiles`; no first-class Bedrock provider or cloud-credential entity exists yet. (`providers/core/types.ts`, `types.ts`)
- OpenAI-compatible profiles already expose most advanced options (auth/header/scheme, timeout, reasoning effort, model-in-path/body, endpoint suffix, extra headers), which can be reused for Bedrock-compatible configuration surfaces. (`providers/core/types.ts`, `components/settings/CompatibleProfileEditor.tsx`)
- Settings persistence/migration already normalizes compatible profiles and strips persisted secret values, establishing a good foundation for API-key redaction semantics and future migration expansion. (`services/settingsMigration.ts`)

## Proposed Information Architecture

### Tabs

1. **Providers** (new merged tab)
   - Card list of saved provider setups (formerly profile-like behavior).
   - Empty state: `+ New Provider`.
   - Non-empty state: existing cards + `+ Add Provider`.
   - Create/Edit opens provider wizard.

2. **Cloud Credentials** (new tab, replaces Google Cloud)
   - Manages reusable cloud auth records independent of providers:
     - AWS credentials set(s) for Bedrock.
     - GCP credentials set(s) for Gemini/BigQuery/other cloud-linked providers.
   - Supports create/edit/delete/test semantics.

3. **System Prompt** (unchanged)
4. **Advanced** (unchanged)

## Data Model Changes

### 1) Feature Flags

Add a typed flag block to `AppSettings`:

- `featureFlags.providers.openai`
- `featureFlags.providers.anthropic`
- `featureFlags.providers.gemini`
- `featureFlags.providers.bedrock`
- `featureFlags.providers.compatible`
- `featureFlags.providerTemplates`

Behavior:
- Provider choices in the wizard are gated by flags.
- Provider card actions (`test`, `duplicate`, `save-template`) are gated by flags where needed.

### 2) Cloud Credentials Registry

Add account-level credential objects to settings:

- `cloudCredentials.aws[]`
  - `id`, `displayName`, `authMode` (keys/profile/role), region defaults, optional metadata.
  - Secrets never round-trip in plain settings payload.
- `cloudCredentials.gcp[]`
  - `id`, `displayName`, `projectId`, `adcMode` (auto/path/token), optional metadata.

### 3) Provider Cards (Merged Provider+Profile)

Introduce a `providerConfigs[]` registry replacing split semantics:

- `id`, `displayName`, `providerId`, `model`, `isDefault`, `createdAt`, `updatedAt`.
- Provider-specific config payload:
  - OpenAI/Anthropic/Gemini: API auth + base URL + model defaults, with OpenAI Responses compaction controls for long-running conversations.
  - Bedrock: full advanced fields (see section below).
  - Compatible: full existing advanced fields.
- Linkage fields:
  - `cloudCredentialRefs?: { awsCredentialId?: string; gcpCredentialId?: string }`

Compatibility bridge:
- `provider.providerId/model/profileId` can remain as runtime selector initially.
- `profileId` transitions to `activeProviderConfigId` during migration phase.

### 4) Provider Templates

Add `providerTemplates[]`:

- Derived from provider config, excluding secrets.
- Reusable as wizard starting points.
- Optional org/team presets later.

## Wizard UX Specification

## Step 1: Choose Provider Type
- Shows only feature-flag-enabled providers.
- Each option includes concise capability copy.

## Step 2: Configure Connection
- Render provider-specific form.
- For mutually exclusive auth modes, use radio-card group with single visible branch.
  - Example: “Use Cloud Credentials” **or** “Use API Key”.

## Step 3: Cloud Credentials Link
- If selected provider requires cloud credentials:
  - Detect existing matching credential records.
  - If available: require selecting one.
  - If none: inline credential mini-form appears with “Save to Cloud Credentials”.
- Inline creation writes to Cloud Credentials registry first, then links by ID.

## Step 4: Advanced Options
- Always available for Bedrock and Compatible.
- Collapsible sections for novice usability, but full parity retained.

## Step 5: Test Connection
- Explicit `Test Connection` action required before save.
- Display clear success/failure state:
  - success badge + timestamp + endpoint tested.
  - failure callout with normalized error + retry affordance.

## Step 6: Save
- On success, save provider config.
- Return to Providers tab card list; card is collapsed summary.

## Bedrock / Compatible Full Options Parity

For Bedrock and Compatible provider flows, support the complete configurable set:

- API type.
- Base URL.
- Auth type/header/scheme.
- Model placement (`modelInBody`, `modelInPath`).
- Endpoint suffix.
- Timeout.
- Reasoning effort.
- Extra headers.
- Payload overrides/query params (where applicable).

Implementation approach:
- Reuse and refactor shared advanced controls from `CompatibleProfileEditor` into reusable form sections.
- Compose those sections into both Compatible and Bedrock wizards.
- Keep provider-specific validation adapters at submit/test stage.

## API Key Redaction Policy

UI behavior for all API-key-based fields:

- When key absent: show empty entry state + “Set key”.
- When key present: show redacted preview only (e.g. `sk-ant-••••••3f`) + “Set” indicator + `Clear` button.
- Never reveal stored key after save.
- Re-entering key overwrites prior secret.

Storage behavior:
- Keep secrets in secrets store integration paths.
- Persist only non-secret metadata in settings.
- Continue migration-time sanitization of legacy secret-bearing fields.

## Migration Plan

1. Add new schema fields with migration defaults.
2. Convert legacy `compatibleProfiles` into `providerConfigs` entries (`providerId=compatible`).
3. Convert previous saved profiles to provider cards/templates where practical.
4. Preserve current active provider selection.
5. Keep temporary compatibility reads until rollout completes.
6. Add telemetry/debug counters for migration success/fallback.

## Implementation Workstreams (Parallelizable)

### Workstream A — Types, Settings, Migration
**Scope**
- Update `providers/core/types.ts`, `types.ts`, `constants.ts`, `services/settingsMigration.ts`.
- Introduce feature flags, cloud credentials, provider config/template schema.

**Deliverables**
- Fully typed data model.
- Backward-compatible migration with sanitization.
- Unit coverage for migration transforms.

### Workstream B — Settings Shell and Tabs
**Scope**
- Update `components/settings/SettingsDialog.tsx` tab layout.
- Replace `GoogleCloudTab` with `CloudCredentialsTab`.
- Merge provider+profiles entry points into new `ProvidersTab` surface.

**Deliverables**
- New tab IA rendered and wired.
- Legacy tabs removed/replaced with migration-safe UX.

### Workstream C — Providers Card List + Wizard
**Scope**
- Build provider card list view and empty state.
- Build wizard (create/edit) with stepper.
- Implement mutually exclusive auth choice UI via radio groups.

**Deliverables**
- One-time setup flow collapsing into cards.
- Add/edit/duplicate/delete/test card actions.
- “Save as template” action (flagged).

### Workstream D — Cloud Credentials Linking
**Scope**
- Build reusable cloud credential selector/inliner.
- Provider wizard linkage logic and conditional validation.

**Deliverables**
- Existing credential auto-detection.
- Inline create-and-link behavior when missing.
- Correct unlink/relink handling on edits.

### Workstream E — Bedrock + Compatible Advanced Config Parity
**Scope**
- Extract reusable advanced form blocks from current compatible editor.
- Implement Bedrock-specific config model + validations.
- Ensure all required advanced options are present.

**Deliverables**
- Full-option Bedrock form parity.
- Compatible form parity retained/regression-free.

### Workstream F — Secret Redaction + Connection Testing
**Scope**
- Shared secret input component with redaction state.
- Provider-specific test connection actions + unified result UI.

**Deliverables**
- Non-retrievable secret UX.
- Test-before-save path with clear confirmations/errors.

### Workstream G — Runtime Integration
**Scope**
- Ensure selected provider card maps into runtime provider selection used by chat.
- Update selectors in `useChat` / provider creation paths as needed.

**Deliverables**
- Active provider card drives chat behavior.
- No regression in model/provider invocation path.

### Workstream H — QA, Docs, Rollout
**Scope**
- Add test plan, fixtures, migration tests, and UI regression checks.
- Update documentation and onboarding.

**Deliverables**
- Updated docs (including this plan evolving to implementation notes).
- Rollout checklist + fallback strategy.

## Suggested Sub-Agent Task Breakdown

1. **Schema Agent**: Implement Workstream A (types/migration/flags).
2. **Shell UX Agent**: Implement Workstream B (tabs/settings shell).
3. **Provider Wizard Agent**: Implement Workstream C.
4. **Cloud Linking Agent**: Implement Workstream D.
5. **Advanced Forms Agent**: Implement Workstream E.
6. **Secrets & Test Agent**: Implement Workstream F.
7. **Runtime Agent**: Implement Workstream G.
8. **QA/Docs Agent**: Implement Workstream H.

Parallel order:
- A + B can start immediately.
- C, D, E, F depend on A; can run in parallel once schema is stable.
- G depends on C/D/E output.
- H runs continuously, finalizes after G.

## Testing Strategy

### Unit
- Migration transforms (legacy → new schema).
- Feature flag gating logic.
- Redaction formatter/parser behavior.
- Provider config validation per provider type.

### Integration
- Wizard flows for each provider type.
- Cloud credential select-vs-inline-create branch coverage.
- Test-connection success/failure states.
- Save-to-card collapse behavior.

### Regression
- Existing compatible provider behavior and advanced options.
- Existing system prompt/advanced tabs unchanged.
- Active provider loading and chat runtime behavior.

## Rollout & Risk Management

- Ship behind top-level flow flag (e.g. `providerFlowV2`) for gradual enablement.
- Keep compatibility reader path for old settings for at least one release.
- Provide one-click rollback to legacy tabs if severe regressions emerge.
- Log anonymized setup completion/test success metrics for stability tracking.

## Acceptance Criteria (Mapped to Requirements)

1. **Merged provider/profiles experience** with card collapse after first save.
2. **Cloud Credentials tab** exists and stores reusable AWS/GCP credentials.
3. **Provider wizard** allows selecting existing cloud credentials or creating inline if missing.
4. **Mutually exclusive auth choices** are explicit and branch UI accordingly.
5. **Bedrock + Compatible** expose full advanced option set.
6. **API keys redacted** after set; clearable, not revealable.
7. **Test connection** provides clear confirmation/error prior to save.
8. **Provider options and card actions feature-flagged**.
9. **Provider templates can be saved and reused**.

## Parallelized Task Plan (Execution-Ready)

This section translates the workstreams into concrete, parallelizable implementation tasks that can be assigned immediately.

### Phase 0 — Alignment (Day 0)

- **T0.1: Finalize schema contract doc**
  - Owner: Schema Agent
  - Output: short contract in this file for `providerConfigs`, `cloudCredentials`, `featureFlags`, `providerTemplates`.
  - Dependency: none.
- **T0.2: Define API/secrets boundaries**
  - Owner: Secrets & Test Agent
  - Output: list of fields that are persisted vs secret-only.
  - Dependency: T0.1.

### Phase 1 — Foundation (parallel)

- **T1.1: Type definitions + defaults**
  - Maps to Workstream A.
  - Files: `providers/core/types.ts`, `types.ts`, `constants.ts`.
  - Dependency: T0.1.
- **T1.2: Settings migration v2 path**
  - Maps to Workstream A.
  - File: `services/settingsMigration.ts`.
  - Dependency: T1.1.
- **T1.3: Feature flag plumbing**
  - Maps to Workstream A/B.
  - Files: settings state + tab/provider gating surfaces.
  - Dependency: T1.1.
- **T1.4: Settings shell/tab skeleton changes**
  - Maps to Workstream B.
  - File: `components/settings/SettingsDialog.tsx`.
  - Dependency: T1.1 (for new data references), can stub UI before full schema.

### Phase 2 — Core UI Build (high parallelism)

- **T2.1: Providers tab card list + empty state**
  - Maps to Workstream C.
  - Output: collapsed cards + `+ New Provider` / `+ Add Provider` CTA behavior.
  - Dependency: T1.1, T1.4.
- **T2.2: Provider wizard shell (stepper/state machine)**
  - Maps to Workstream C.
  - Output: step orchestration and navigation guards.
  - Dependency: T1.1.
- **T2.3: Cloud Credentials tab (AWS/GCP CRUD)**
  - Maps to Workstream B/D.
  - Output: create/edit/delete/test for cloud credentials.
  - Dependency: T1.1, T1.4.
- **T2.4: Credential selector + inline create component**
  - Maps to Workstream D.
  - Output: reusable component used by wizard and cloud tab.
  - Dependency: T2.3 (types/UI contracts), can start with mocks.
- **T2.5: Compatible advanced form extraction**
  - Maps to Workstream E.
  - Output: reusable form blocks from `CompatibleProfileEditor`.
  - Dependency: T1.1.
- **T2.6: Bedrock advanced form implementation**
  - Maps to Workstream E.
  - Output: full parity options surface.
  - Dependency: T2.5.
- **T2.7: Secret input/redaction component**
  - Maps to Workstream F.
  - Output: reusable set/clear/redacted display control.
  - Dependency: T0.2.
- **T2.8: Test connection result component + service hooks**
  - Maps to Workstream F.
  - Output: normalized success/failure UX block used in wizard and cards.
  - Dependency: T2.2.

### Phase 3 — Integration (medium parallelism)

- **T3.1: Wire wizard with provider-specific forms**
  - Combines T2.2 + T2.5 + T2.6 + T2.7 + T2.8.
  - Dependency: all listed tasks.
- **T3.2: Wire cloud-linking branch logic**
  - Combines T2.4 + T3.1.
  - Dependency: T2.4, T3.1.
- **T3.3: Provider card actions (edit/duplicate/test/save-template)**
  - Maps to Workstream C/F.
  - Dependency: T2.1, T2.8, T1.3.
- **T3.4: Runtime selection integration**
  - Maps to Workstream G.
  - Files: provider selection usage in chat/runtime hooks.
  - Dependency: T3.1.

### Phase 4 — Hardening / Rollout

- **T4.1: Migration regression test suite**
  - Maps to Workstream H.
  - Dependency: T1.2.
- **T4.2: Wizard flow integration tests**
  - Maps to Workstream H.
  - Dependency: T3.2.
- **T4.3: Feature-flag behavior tests**
  - Maps to Workstream H.
  - Dependency: T3.3.
- **T4.4: Rollout controls + fallback path**
  - Maps to Workstream H.
  - Dependency: T3.4.

### Dependency Graph (Condensed)

- `Schema`: T1.1 → T1.2/T1.3/T1.4
- `UI Shell`: T1.4 → T2.1/T2.3
- `Wizard Core`: T2.2 + T2.5 + T2.6 + T2.7 + T2.8 → T3.1
- `Cloud Link`: T2.3 + T2.4 + T3.1 → T3.2
- `Runtime`: T3.1 → T3.4
- `QA/Rollout`: T1.2 + T3.2 + T3.3 + T3.4 → T4.x

### Suggested Parallel Team Assignment

- **Team Alpha (Schema/Platform)**: T0.1, T1.1, T1.2, T1.3
- **Team Beta (Settings Shell)**: T1.4, T2.1, T2.3
- **Team Gamma (Wizard/Forms)**: T2.2, T2.5, T2.6, T3.1
- **Team Delta (Cloud Linking)**: T2.4, T3.2
- **Team Epsilon (Secrets/Test UX)**: T0.2, T2.7, T2.8, T3.3
- **Team Zeta (Runtime + QA)**: T3.4, T4.1, T4.2, T4.3, T4.4

### Definition of Done Per Task

Each task is complete only when all are true:

1. Feature-flag behavior is implemented and tested where relevant.
2. Secrets are never persisted in plain settings payloads.
3. UI copy includes explicit mutually-exclusive choice wording where applicable.
4. Test-connection UX returns clear, actionable feedback states.
5. Changes include migration coverage if schema/persistence is touched.
