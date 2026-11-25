# Tool Development and Registration

This document is the contract for adding or changing agent tools. It is written
for humans and coding agents working in this repository.

The short version: every tool must be registered once, described once, classified
for permissions, rendered through a declared native path, and tested through the
same path the model will use.

## Agent Contract

Treat this document as the handoff contract for any agent/model that is asked to
add, migrate, or review a tool. A tool change is not complete until a future
agent can answer these questions from code and metadata without reading the
tool's implementation body:

- What does the model call, and which JSON schema constrains the arguments?
- Which permission category applies: `read`, `write`, or `execute`?
- Which subject is permissioned: tool, tool set, connector, local location,
  remote resource, or provider-hosted capability?
- Where did the capability come from: built-in app, local files, attached file,
  connected service, or model provider?
- Which existing renderer/native viewer should show the result?
- Which settings-page test mode proves the tool can run safely?
- Which focused and contract tests fail if this tool drifts?

When those answers are not explicit, stop and make the product metadata explicit
before adding more implementation code.

## Agent Operating Model

When a model is developing a tool, it should treat the tool as a product
surface, not just a callable function. The implementation, permissions,
settings row, approval prompt, native viewer, test action, generated fallback,
and provider schema are all one contract.

The required sequence is:

1. Start from the existing nearest-domain tool and copy its registration shape.
2. Define the product contract before writing execution code: name, category,
   subject, origin, tool set, connector, renderer, native viewer, source refs,
   settings test mode, and focused tests.
3. Implement a closed provider schema and strict argument decoder.
4. Register the tool in the exact runtime surface that should expose it:
   default analysis tools, a datasource surface, a connector surface, the search
   surface, or provider-hosted adapter capabilities.
5. Add `tool_metadata.go` in the same change as the registry entry.
6. Map permissions in the run manager before the tool can mutate, execute, or
   read scoped local/remote resources.
7. Return first-class `Output.ResultMetadata` and artifact metadata that route
   through an existing native viewer.
8. Add focused tests plus the shared contract tests listed in this document.
9. Regenerate frontend metadata and verify inventory parity.

Do not leave any part as "frontend can infer it", "the tool description says
it", or "the implementation knows it". If the UI, approval system, or a future
model needs the information, it belongs in structured metadata and tests.

### Traceability Matrix

| Question | Canonical source | Contract tests |
| --- | --- | --- |
| What can the model call? | `Tool.Definition()` schema in `server/internal/chat/tools` | `TestRegisteredToolDefinitionsUseObjectSchemas`, `TestBestInClassToolDefinitionsUseClosedSchemas`, `TestBestInClassToolObjectSchemasAreClosedRecursively`, focused definition tests |
| Is the tool registered in the right runtime surface? | `NewRegistry`, helper lists, datasource/search/connector definition methods | `TestRegistryDefaultDefinitionSurfaceMatchesCanonicalToolSets`, `TestRegistryNamedDefinitionSurfacesMatchCanonicalToolSets`, `TestConnectedToolNameHelpersMatchToolMetadata`, `TestSearchDefinitionSurfacesMatchAllowedToolSets` |
| How is the tool shown to users? | `tool_metadata.go` label, description, tool set, origin, connector id | `TestToolMetadataCoversEveryRegisteredTool`, `TestBestInClassToolMetadataUsesStableTaxonomy`, `TestToolMetadataShapeIsComplete` |
| What is the risk class? | `ToolPermissionCategory` in `tool_metadata.go` | `TestToolMetadataRiskClassifications`, `TestPermissionMetaCoversWriteAndExecuteTools` |
| What permission subject is checked? | `permissionMetaForManagerTool` and local/remote subject helpers | `TestPermissionMetaCoversReadTools`, `TestPermissionMetaCoversLocalProjectReadTools`, `TestPermissionMetaScopesArtifactReadToolsToRemoteTarget`, focused permission tests |
| How does settings test the tool? | `ToolTestSpec`, `PrepareToolTestArgs`, `optionc.Manager.TestTool`, `POST /api/tools/test` with `tool_name` in the body | `TestAllToolMetadataSupportsSettingsToolTests`, `TestBestInClassToolTestInputsMapToProviderSchema`, `TestToolTestMetadataDocumentsRequiredArguments`, `tool_test_runner_test.go`, HTTP tool-test tests |
| How is output rendered natively? | `Output.ResultMetadata`, `ArtifactPayload.Metadata`, `ToolPresentationSpec` | `TestBestInClassToolPresentationIsAuditableForNativeRendering`, `TestToolPresentationNativeViewerContracts`, `TestSuccessfulOutputLiteralsCarryResultMetadata`, renderer/API tests |
| Does frontend fallback match backend? | `go generate ./internal/chat/tools` -> `lib/generatedToolMetadata.ts`, `lib/toolInventory.ts` | `TestGeneratedFrontendToolMetadataMatchesBackend`, `TestFrontendStaticToolMetadataMatchesBackend`, `npm run test:tool-contracts` |
| Is it provider-hosted instead of locally executed? | `provider_hosted_tool_metadata.go` and provider adapter injection | `TestProviderHostedToolCapabilitiesAreExplicitlyClassified`, hosted-policy tests, provider adapter tests |

## Files To Touch

Use this map for every local registered tool:

| Layer | Required file(s) | Purpose |
| --- | --- | --- |
| Backend implementation | `server/internal/chat/tools/<domain>_tools.go` | Tool definition, schema, argument validation, execution, result metadata |
| Backend registry | `server/internal/chat/tools/registry.go` and connector helper slices | Makes the tool available to model runs and connection-scoped filtering |
| Product manifest | `server/internal/chat/tools/tool_metadata.go` | Canonical label, permission category, origin, tool set, presentation, test mode |
| Permission mapping | `server/internal/chat/optionc/manager.go` | Maps model calls to durable permission subjects before execution |
| HTTP/tool test path | `server/internal/chat/httpapi/handler.go` and `optionc.Manager.TestTool` when needed | Ensures settings tests use the real registered tool path |
| Generated frontend fallback | `lib/generatedToolMetadata.ts` via `go generate` | Keeps older/dev frontend surfaces in parity with backend metadata |
| Frontend grouping fallback | `lib/toolInventory.ts` only when a tool set is new/reordered | Groups the generated metadata and provider-hosted capabilities for Settings |
| Native rendering | Existing renderer/artifact files only when introducing a new result shape | Reuse `tool_presentation` before adding a bespoke renderer |
| Tests | Tool-specific tests plus the contract suites below | Proves the tool shape, permissions, rendering metadata, and test path |

Provider-hosted capabilities use a different map:

| Layer | Required file(s) | Purpose |
| --- | --- | --- |
| Provider catalog | `server/internal/chat/tools/provider_hosted_tool_metadata.go` | Canonical id, provider adapter family, settings key, category, label |
| Provider adapter | `server/internal/chat/providers/<provider>` | Injects provider-native tools and marks streamed results as provider-executed |
| Permission policy | `server/internal/chat/optionc/manager.go` | Gates hosted capabilities before provider construction |
| Frontend inventory | `GET /api/tools/inventory`, `lib/toolInventory.ts`, `types/permissions.ts` | Renders provider-scoped rows using `provider_hosted_tool` subjects |
| Tests | Provider adapter, hosted-policy, inventory, and frontend parity tests | Proves hosted tools are not duplicated as local registered tools |

## Agent Implementation Recipe

When an agent/model is asked to add or migrate a tool, use this sequence and do
not skip ahead:

1. Inspect an existing nearby tool first.
   Match the surrounding file's conventions for constructors, argument structs,
   `decodeArgs`, manager calls, dry-run handling, result metadata, and tests.

2. Decide the user-visible contract before coding.
   Write down the tool name, permission category, origin, tool set, connector
   id, required arguments, one-of argument groups, required runtime context,
   presentation family, native viewer, file capability, source refs, and test
   mode. If any of these are unclear, the tool shape is not ready.

3. Implement one registered backend tool.
   Add the tool implementation in `server/internal/chat/tools`, return a normal
   `Tool.Definition()`, register it in `NewRegistry`, and add it to the relevant
   connector helper such as `GitHubToolNames()` or `SlackWriteToolNames()`.

4. Add product metadata immediately.
   Add the tool to `server/internal/chat/tools/tool_metadata.go` in the same
   change. The permissions UI, approval system, generated frontend fallback,
   native renderers, and future tool-test UI all depend on this manifest.

   If the capability is provider-hosted instead of locally executed, such as
   OpenAI Responses `web_search` or Anthropic `code_execution`, do not add a
   fake local tool. Add it to
   `server/internal/chat/tools/provider_hosted_tool_metadata.go` and add
   adapter tests proving the provider request and streamed result use that
   catalogued capability.

5. Make the test path real.
   Pick `connection_check`, `sample_call`, `dry_run`, or `none`. New tools
   should almost always use one of the first three. Write metadata so
   `POST /api/tools/test` with `tool_name` in the JSON body can either run the
   registered tool safely or return the shared missing-input/permission response
   before execution. The older path form remains available for simple local tool
   names, but the body form is canonical because provider-hosted ids can contain
   path separators.

6. Return first-class result metadata.
   Successful outputs must set `Output.ResultMetadata` directly, preferably via
   `toolCatalogResultMetadata(...)` or a domain helper that preserves it.
   Artifact-producing tools must attach `tool_name`, `tool_args`,
   `result_metadata`, and `tool_presentation` to live artifacts.

7. Regenerate and verify frontend inventory.
   Run `cd server && go generate ./internal/chat/tools`, then keep
   `lib/toolInventory.ts` ordering/group hints in sync only if a tool set is new
   or intentionally reordered.

8. Add tests before broad verification.
   Add focused tool tests, then run the contract suites listed below. The
   contract tests are intentionally designed to fail when a new tool is
   registered without permissions, presentation metadata, a settings test path,
   frontend fallback metadata, or first-class result metadata.

## Tool Work Packet Template

Before implementation, capture this compact spec in the issue, PR, or agent
scratchpad. The values should map one-to-one onto code and metadata.

```text
Tool name:
Provider-hosted or local registered tool:
User-facing label:
User-facing description:
Permission category: read | write | execute
Permission subject: tool | tool_set | connector | local_location | repository | remote_resource | provider_hosted_tool
Origin: builtin | connected | local | attached
Provider-hosted origin: provider
Tool set id / name:
Connector id, if any:
Operation string for approvals:
Required arguments:
Alternative argument groups:
Sample arguments:
Required runtime context:
Renderer strategy / key:
Native viewer:
Result kind:
File capability:
Source refs:
Native links:
Settings test mode: connection_check | sample_call | dry_run | none
Focused tests to add:
Contract suites to run:
```

If a field is hard to fill in, that is usually a design problem rather than an
implementation detail.

## Runtime Map

Use this map before changing any tool behavior.

Backend tool implementation and registration:

- Tool implementations live in `server/internal/chat/tools`.
- `server/internal/chat/tools/registry.go` decides which tools are available in
  the default registry and which are gated behind connections.
- Connector-specific helper slices such as `GitHubToolNames()`,
  `SlackToolNames()`, `SharePointToolNames()`, and `AtlassianJiraToolNames()`
  must include every tool exposed for that connector. These helpers are part of
  the product contract because they decide which tools appear when a connection
  is enabled.

Product metadata and permissions:

- `server/internal/chat/tools/tool_metadata.go` is the canonical product
  manifest. It powers the settings permissions UI, provenance labels, risk
  categories, and tool-test readiness.
- `server/internal/chat/optionc/manager.go` maps tool calls into persistent
  approval subjects before execution. This is where a tool becomes `off`,
  `ask`, or `always allow` aware.
- Local file reads must map to a `local_location` subject with the concrete
  path/root being listed, searched, or read. Do not collapse local file access
  into a vague global tool permission.
- Remote-resource reads must map to a precise subject whenever the arguments
  identify a single target, such as an attachment, artifact, dataset artifact,
  thread artifact, Slack channel/thread/file/user, Jira issue/attachment,
  Confluence page/path, SharePoint file, arXiv paper, or query job. Populate
  `remote_kind` and preserve the target's exact `remote_id` so a user can allow
  one resource without allowing the whole tool set.
- Group-level settings use a `tool_set` subject with `tool_set_id`, so a rule
  for a tool set applies to tools added to that set later. Exact tool rules
  override tool-set rules, and local-location subjects still gate local file
  access.

Frontend inventory and testing UI:

- `GET /api/tools/inventory` exposes backend metadata to the frontend.
- `lib/generatedToolMetadata.ts` is generated from backend metadata and feeds
  the static fallback used by older/dev surfaces.
- `lib/toolInventory.ts` owns frontend grouping, normalization, and legacy
  fallback behavior; it must consume generated metadata for the static
  inventory.
- `components/settings/PermissionsTab.tsx` renders the permission rows and the
  per-tool readiness/test UI.
- `services/toolTestApi.ts` is the frontend client for
  `POST /api/tools/test`. It sends the selected `tool_name` in the JSON body so
  tool ids such as `openai-responses/web_search` remain addressable.

Provider-hosted model capabilities:

- Some model-exposed capabilities are injected by provider adapters rather
  than registered in `tools.NewRegistry`. Current examples are OpenAI Responses
  hosted `web_search` / `code_interpreter` and Anthropic Messages hosted
  `web_search` / `code_execution`.
- These capabilities live in provider settings (`webSearch` and
  `codeExecution`), execute inside the model provider, and return events marked
  `ProviderExecuted`. They must not be replayed through the local tool executor.
- `server/internal/chat/tools/provider_hosted_tool_metadata.go` is the
  canonical catalog for these capabilities. When adding one, classify its
  permission category, adapter family, settings key, and provider tool names
  there.
- `GET /api/tools/inventory` returns these capabilities in the separate
  `provider_hosted_tools` array. The frontend renders them under the model
  provider capabilities tool set and uses `provider_hosted_tool` permission
  subjects keyed by stable ids such as `openai-responses/web_search`.
- The run manager evaluates hosted-capability permissions before creating the
  provider. Denied or unresolved hosted capabilities are stripped from provider
  settings before the adapter injects request-native tools.
- Provider-hosted capabilities are deliberately excluded from
  `AllToolMetadata()` because they do not have local `Tool.Definition()` schemas
  and must never execute through the local tool runner. They still expose a
  settings-page `connection_check` through `POST /api/tools/test`; that check is
  a non-executing catalog/config/renderability check, not a provider call. The
  exclusion from the local registry is tested so the boundary remains visible.

Rendering and artifacts:

- Tools that produce user-visible files, tables, charts, images, documents,
  PDFs, or links must return result metadata that existing renderers can
  understand.
- `tools.Output.ResultMetadata` is the canonical backend field for renderer
  metadata. If a legacy tool still nests `result_metadata` inside
  `Output.Result`, the manager normalizes it, but new tools should set
  `ResultMetadata` directly.
- Artifact-producing tools should persist artifacts through the existing
  artifact path rather than inventing a separate display channel.
- Live tool artifacts should carry `tool_args`, `result_metadata`, and
  `tool_presentation` so generic inspector/native-viewer renderers work after
  streaming, reload, and settings-page tests.
- Tool-test responses for tools that return `ArtifactPayload` should expose a
  native-renderable `output.artifact` object. Do not flatten these results to
  raw JSON in the settings test surface.

## Non-Negotiable Invariants

- The backend manifest is canonical. Frontend fallback data may duplicate it
  only while the fallback exists, and parity tests must cover the duplication.
- Every registered provider tool schema must be closed at the root with
  `additionalProperties: false`. There is no top-level open-schema allowlist.
- Nested object schemas must also be closed. The recursive closure test in
  `tool_best_shape_test.go` walks every object under `properties`, `items`,
  `oneOf`, `anyOf`, and `allOf`; `intentionalOpenProviderSchemaPaths()` is only
  for deliberately dynamic maps such as vendor chart options or Jira custom
  fields.
- The permission category must reflect the highest-impact thing the tool can do:
  `read`, `write`, or `execute`.
- The origin must tell the user where the capability came from: `builtin`,
  `connected`, `local`, or `attached`.
- Connected tools must expose a connector id. Built-in, local, and attached
  tools must not.
- A tool test must run through the registered tool implementation, not a
  parallel mock path.
- Write and execute tests must be dry runs and must require approval.
- Tool tests must obey saved permissions. `deny` blocks, `ask` returns a
  structured permission response when there is no active chat approval flow, and
  `allow` permits execution.
- If a tool can read local project files indirectly, such as via `source.path`
  or an `include_files` option, that path must still be represented as a
  `local_location` permission subject.
- If a tool reads an existing app artifact, attachment, dataset artifact, or
  thread artifact by id, the approval subject must include `remote_kind` and
  `remote_id`. This keeps per-artifact approvals narrow and auditable.
- If a tool reads an async query job as an input source, such as
  `source.job_id`, the approval subject must include
  `remote_kind = "query_job"` and the job id as `remote_id`. Do not fall back to
  a broad tool-level read approval for query-job sources.

## Canonical Tool Shape

A best-in-class tool has two pieces of metadata:

1. Provider definition: the model-facing function schema returned by
   `Tool.Definition()`.
2. Product metadata: the permission, provenance, UI, and test metadata in
   `server/internal/chat/tools/tool_metadata.go`.

The provider definition answers: "How does the model call this?"

The product metadata answers: "How should the app explain, permission, group,
audit, and eventually test this?"

Provider-hosted capabilities are the exception to this two-part shape. They
still need product classification, but their provider definition belongs to the
adapter-specific request shape, not `Tool.Definition()`. For these, update
`ProviderHostedToolCapabilities()`, expose them through
`provider_hosted_tools`, make the settings-page test a non-executing
`connection_check`, and add adapter/provider-policy tests instead of adding a
dummy local registered tool.

Required product metadata:

```go
type ToolMetadata struct {
    Name        string
    Label       string
    Description string
    Category    ToolPermissionCategory // read, write, execute
    Origin      ToolOrigin             // builtin, connected, local, attached
    ToolSetID   string
    ToolSetName string
    ConnectorID string
    Presentation ToolPresentationSpec
    Test        ToolTestSpec
}
```

Required presentation metadata:

```go
type ToolPresentationSpec struct {
    Family               string   // product family, e.g. github, jira, local-files
    ResultKind           string   // file, query, chart, connector, browser, ...
    RendererStrategy     string   // tool, family, artifact_type, or generic
    RendererKey          string   // stable key used to select the native renderer
    NativeViewer         string   // code, data, file, query, chart, connector_inspector, ...
    ArtifactTypes        []string // data, code, image, file when artifacts can be produced/read
    FileCapability       string   // optional: local_file_read, remote_file_download, ...
    FileKinds            []string // optional: pdf, image, text, document, spreadsheet, ...
    PreviewModes         []string // optional: pdf_canvas, image_raw, spreadsheet_workbook, ...
    SourceRefs           []string // artifact_id, path, local_path, content_text, content_base64, remote_id, source.*
    ProducesFileArtifact bool     // true when the tool creates/downloads a file artifact
    PromotesNativeInput  bool     // true when native file parsing/viewing is part of the flow
    NativeLinks          []string // warehouse_job, source_url, browser_url
}
```

`Presentation` is the product contract for native viewing and tool-test output.
It must be present for every tool, even if the tool renders through the generic
connector inspector. File-capable tools should declare a specific
`FileCapability` so reviewers can audit whether local files, attachments,
remote downloads, uploads, cloned repositories, and generated artifacts are
handled by native viewing paths.

The renderer fields are intentionally explicit:

- `RendererStrategy = "tool"` only when one registered tool has a dedicated
  renderer keyed by the exact tool name.
- `RendererStrategy = "artifact_type"` when the native viewer can be selected
  from the result type, such as `file`, `code`, `query`, `data`,
  `interactive_html`, or `chart`. Artifact-producing tools such as
  `display_table`, `build_chart_artifact`, `create_interactive_artifact`, and
  `export_thread` should normally use this route.
- `RendererStrategy = "family"` when output is best inspected through a
  connector/family renderer, such as GitHub, Jira, Slack, SharePoint, browser,
  or arXiv.
- `RendererStrategy = "generic"` only for true fallback inspector output.

For file-capable tools, fill the whole chain. A Slack/Jira/SharePoint/arXiv
download should declare `ResultKind = "file"`, `NativeViewer = "file"`,
`ArtifactTypes = ["file"]`, a remote file capability, broad `FileKinds`,
supported `PreviewModes`, `SourceRefs` that let the model/user refer to the
remote object or produced artifact, and `ProducesFileArtifact = true`.

For upload tools, do not hide the source model in free-form descriptions. Use
`SourceRefs` to document whether the tool can upload from an artifact, project
path, app-managed local path, generated text, or generated base64.

For query tools, use `NativeViewer = "query"`, `ArtifactTypes = ["data"]`, and
`NativeLinks = ["warehouse_job"]` when a warehouse job/result handle exists.

For source-backed artifact tools such as `display_table`,
`build_chart_artifact`, or `create_interactive_data_artifact`, do not hide the
source model in descriptions. Declare nested `SourceRefs` such as
`source.artifact_id`, `source.path`, `source.job_id`, and `owner_token` so the
permissions UI, tool-test UI, and model can explain which existing data source
the artifact is built from.

Required test metadata:

```go
type ToolTestSpec struct {
    Supported              bool
    Mode                   ToolTestMode // none, connection_check, sample_call, dry_run
    RequiresApproval       bool
    InputRequired          bool
    RequiredArguments      []string
    RequiredArgumentGroups [][]string
    RequiredContext        []string
    InputHint              string
    Summary                string
}
```

`RequiredArguments` must list every top-level schema field that the tool needs
for a useful user-supplied test.

`RequiredArgumentGroups` expresses one-of input choices. Each inner slice is a
complete acceptable group, and the test runner requires at least one group to be
satisfied. Use this when the runtime accepts equivalent alternatives such as
SharePoint `item_url` or `server_relative_url`, or when a tool needs a stable
identifier plus one of several update fields, such as Jira `issueKey` plus
`summary`, `description`, `fields`, or another update input.

The metadata test compares schema-required fields to the union of
`RequiredArguments` and single-field `RequiredArgumentGroups`, so schema and UI
contract drift fails fast. Multi-field groups should have an explicit regression
test that proves the accepted combinations and the missing-input response.

`SampleArguments` gives the Settings test UI valid examples for every required
argument and every alternative argument group. Samples are not auto-submitted;
they render as placeholders so the user still chooses when to run the test. Use
JSON-native values for arrays and objects, such as `[]any` and `map[string]any`
in Go metadata, because `/api/tools/inventory` round-trips them through JSON.
Every sample must be semantic and schema-valid: integers should be numbers,
enums should use allowed values, bounded numbers should satisfy minimums and
maximums, and nested inputs such as chart specs, Jira fields, source objects,
CLI args, and CSV metrics must use realistic structures rather than generic
`test-*` or `{"key":"value"}` placeholders.

The only exception is a tool whose settings-page test uses a backend-owned
synthetic fixture instead of user input. Query lifecycle tools create a
short-lived synthetic query job and inject `job_id`/`owner_token`. arXiv audit
tools create a temporary synthetic search/screening context so the real
registered tool can validate current-run state without writing fake messages to
the user's thread. When using this pattern, add explicit tests for the synthetic
fixture path and for frontend/backend metadata parity.

`RequiredContext` lists ambient prerequisites such as `project`, `thread`,
`artifact`, `query_job`, `browser`, `search_index`, or `connection:<id>`.
Context is shown to users before a test runs; it should not be used to hide a
real schema argument.

Settings-page tests must fail before execution with `MissingToolTestInputError`
when required context is not available. User-supplied contexts such as
`project` and `thread` are explicit inputs. Automatic contexts such as
`connection:<id>`, `browser`, `search_index`, and `artifact` should be checked
from the manager/registry/store state when that can be proven locally; bad
credentials or upstream failures remain normal tool results.

If a tool depends on transient in-run state, either create a safe synthetic
fixture in the settings-page test path or declare `ToolTestModeNone` until a
safe runtime test exists. Do not advertise a `sample_call` that can only work
after a particular model run has produced prior tool output.

## Permission Categories

Use the smallest truthful category. Do not classify a tool as `read` because it
is convenient.

`read`:
Reads data, lists data, searches data, downloads data into an app-managed cache,
or creates an in-app artifact without changing an external system.

Examples: `read_file`, `search_files`, `github_get_issue`,
`sharepoint_read`, `run_bigquery_query`, `build_chart_artifact`.

`write`:
Changes external user-visible state.

Examples: `slack_post_message`, `slack_upload_file`,
`jira_add_comment`, `jira_update_issue`, `jira_create_issue`.

`execute`:
Runs commands, controls local/browser state, clones or updates repositories,
cancels jobs, or runs a script/runtime even when the operation is app-managed.

Examples: `github_clone_repository`, `github_run_gh_command`,
`cua_browser_click`, `cua_browser_type`, `rlm_exec`, `cancel_query`.

## Origins and Tool Sets

`Origin` is the provenance badge users see in permissions.

`builtin`:
The tool operates inside the app and does not depend on a user-added connection.

`connected`:
The tool operates through a connection or integration. Set `ConnectorID`.

`local`:
The tool touches local project files, local clones, local uploads, or local
dataset artifacts.

`attached`:
The tool reads files explicitly attached to a chat.

`ToolSetID` groups tools for permissions and settings. Use stable ids such as
`github`, `sharepoint`, `local-files`, `attachments`, or `query-lifecycle`.

## Registration Flow

Follow this order when adding a tool.

1. Define a stable tool name.
   Use snake_case. Do not rename an existing tool without a migration plan for
   persisted approvals, runs, artifacts, and frontend renderers.

2. Implement the tool in `server/internal/chat/tools`.
   Prefer `ExecutableTool`. Keep argument parsing strict by using `decodeArgs`
   unless there is a specific reason to accept arbitrary maps.

3. Provide a model-facing JSON schema.
   The schema must be bounded. Include required fields, enums, minimums,
   maximums, `additionalProperties: false`, and concise descriptions. Never
   expose secrets or ambient local paths in schema descriptions. There are no
   top-level open-schema exceptions.

4. Register the tool in `NewRegistry`.
   Add it to the right default or gated section. If it belongs to a gated
   connector, update the connector's `ToolNames()` helper too. The helper must
   match the connector id and category declared in `tool_metadata.go`.

5. Add product metadata in `tool_metadata.go`.
   Include category, origin, tool set, connector id, user-facing label,
   description, presentation metadata, test strategy, required test arguments,
   and required context.

   Presentation metadata is not decorative. It is how native viewing, tool-test
   rendering, future "test this tool" actions, and audit/search behavior know
   what the result means.

6. Regenerate the frontend permissions fallback.
   Runtime inventory comes from the backend manifest. The static fallback used
   by older/dev surfaces is generated into `lib/generatedToolMetadata.ts`.
   After changing `tool_metadata.go`, run:

   ```sh
   cd server
   go generate ./internal/chat/tools
   ```

   Keep `lib/toolInventory.ts` grouping/order hints in sync only when the tool
   set itself is new or the desired ordering changes. The generated metadata
   carries labels, descriptions, categories, provenance, presentation metadata,
   and test metadata.

7. Add execution tests.
   Test valid input, invalid input, unknown fields, bounds, empty state,
   permission/approval behavior, result metadata, and the settings-page test
   path.

   If the tool reads, downloads, creates, or uploads a file or artifact, also
   test the presentation/result metadata that drives the native viewer.

8. Run the validation tests.
   At minimum:

   ```sh
   cd server
   go test ./internal/chat/tools
   ```

## Tool-Type Guidance

Local file and repository tools:

- File search and reads must use the project/local-file abstractions already in
  the app. Do not ask users to copy files when the app has been granted access
  to search a local location.
- Permission subjects should identify the concrete local root or file path. For
  project-relative arguments, normalize under the project files directory and
  reject path traversal.
- Git repository tools that inspect local clones are local read tools. GitHub
  tools that clone, pull, refresh, or run `gh` are connected execute tools.
- A local search command must not expose `bash`, a command string, executable
  paths, redirection, substitutions, or subprocess callbacks. Follow
  `local_search_command`: use a closed command enum, direct argv execution,
  grant-relative paths, canonical-path revalidation, sensitive excludes,
  bounded output/results/files/time, a clean child environment, cancellation,
  a no-process dry run, bundled runtime checks, and a deterministic non-command
  fallback.
- `local_search_command` is execute-class but is a two-key capability: its
  durable tool action can be enabled only while every invocation is separately
  constrained to a configured Local Computer grant. The global action rule does
  not grant access to a path by itself.
- Cross-platform local tools must reject Unix absolute paths, Windows
  drive-qualified paths (including drive-relative forms such as `C:foo`), and
  UNC paths anywhere a grant-relative path is required. On Windows, launch
  native binaries without a console window and discover local volumes through
  the OS rather than hard-coding `C:\\`.

Remote file tools:

- Jira, Confluence, SharePoint, Slack, GitHub, arXiv, and future connectors
  should access remote files through their connector managers and return native
  file/artifact metadata when files are downloaded or promoted into the app.
- Remote file reads are usually `read`. Uploading, posting, editing, or creating
  remote state is `write`. Running an external command or controlling local
  state is `execute`.
- When a remote-file tool promotes a downloaded file into an app artifact, the
  promoted artifact must keep the originating `tool_args`,
  `result_metadata`, and `tool_presentation` so later viewers, audits, and
  uploads can explain where the file came from.
- Tool-test arguments should include a real target field such as `item_url`,
  `issueKey`, `attachmentId`, `file_id`, or `repository` when the tool needs one.
- If a connector can identify the same remote object several ways, model that
  as `RequiredArgumentGroups` rather than forcing users through one arbitrary
  identifier. The settings UI should make the alternatives visible and the
  backend must accept any complete group.
- Upload tools that can send existing app or local files should model the file
  source as alternatives too. For example, keep the stable target argument such
  as `channel_id` or `issueKey` in `RequiredArguments`, then add source groups
  such as `artifact_id`, `path`, `local_path`, `fileName + contentText`, and
  `fileName + contentBase64`. Do not make generated-content fields globally
  required when artifact or file-path sources can infer the file name.

GitHub tools:

- Status and connection checks do not require a project.
- Repository inspection tools that operate on repositories attached to the
  current project require `project` context.
- `github_clone_repository` requires a `repository` argument but does not
  require an existing project because a successful clone can create/attach one.
- `github_run_gh_command` is classified as `execute` even for read-looking
  commands because it runs an external CLI path.

Native viewing tools:

- Prefer the existing artifact and renderer pipeline over bespoke UI.
- When a tool downloads or promotes a remote file, include enough metadata for
  the native viewer to choose the right renderer and for the model to reference
  the file in later turns.
- Result metadata should include product/family information either directly or
  by relying on the manager's canonical fallback from `tool_metadata.go`.
- Add renderer tests when a new result shape is introduced. If the result uses
  an existing shape, add at least one backend test that the metadata fields are
  present.
- Family renderers need fixture-level UI coverage for every distinct result
  shape they claim. Use `/dev/audit-trail` fixtures and Playwright assertions
  against semantic content such as tables, cards, status pills, preview blocks,
  and native links. Do not treat "the modal opened" as sufficient coverage.

## Approval Behavior

Tools should not invent their own permission model.

Use the persistent approvals service for durable permission checks. Connected
write and execute tools must not create connector-specific approval requests.
They should execute only after the manager has resolved the persistent approval
and set `ExecContext.PermissionApproved`.

Every `write` and `execute` tool must map through
`permissionMetaForManagerTool` in `server/internal/chat/optionc/manager.go`.
That metadata is what lets the approval system evaluate saved `off`, `ask`, and
`always allow` rules before the tool runs. The coverage test
`TestPermissionMetaCoversWriteAndExecuteTools` should fail if a new mutating or
executing tool ships without this mapping.

Approval requests should include:

- `tool`
- `category`
- `operation`
- `subject`
- `summary`
- a bounded preview of the action
- available scopes: `once`, `thread`, `always` where appropriate

Settings-created group rules should use `subject.kind = "tool_set"` with the
canonical `tool_set_id` from `tool_metadata.go`. Do not create a large batch of
per-tool rules when the user's intent is "all tools in this set"; that drifts as
new tools are added.

For local folder listing and file search, permission subjects should identify
the local location, not a vague global file capability. The user must be able to
see which roots will be listed or searched.

### Approval and Local-Access Guardrails

New tools must preserve these guardrails when they touch local files, execute
commands, or wait for user approval:

- Approval waits must not leave the model run in an invisible indefinite wait.
  Chat-run approvals should emit an immediate `approval_required` event, remain
  visible in the shared approval queue, and keep the run open while permission is
  unresolved. Heartbeat/progress events may continue, but no final assistant
  message should be produced until the user approves, rejects, or cancels.
- Local file tools must validate both the requested path and the resolved file
  target. Lexical path checks alone are not enough: symlinks must not let a tool
  read outside the approved project/root. Add symlink-escape tests in
  `server/internal/chat/projects` whenever local file opening behavior changes.
- Sensitive local files need narrower handling than ordinary project files.
  Tools must not silently include credential-like paths such as `.env`, private
  keys, `.ssh`, token stores, or cloud credential files in a broad local search
  or read approval. Add explicit deny/narrow-approval tests when a tool can
  traverse user-controlled local paths.
- Execute tools must avoid broad durable approvals. Do not offer or persist a
  global `always allow` rule for an execute tool unless the subject is scoped to
  a concrete resource/action that the user can audit. Broad execute subjects
  should be `once`/`thread` only or require a narrower subject.
- Cancelling a run must also cancel any pending approval requests and notify
  approval subscribers, so chat and settings queues do not show stale pending
  requests.
- Remote write tools must have mapping tests for each operation variant. For
  example, Jira comments, replies, issue updates, related-ticket links,
  attachment uploads, and issue creation should each have an approval subject
  test when their arguments differ.

## Settings Tool Testing

The permissions UI lets users test supported tools from each row. A migrated
tool is not complete until its metadata tells that UI whether the test is a
connection check, sample call, dry run, or intentionally unsupported path.

Design every new tool so it can support one of these modes:

`connection_check`:
Calls a status/check endpoint or equivalent read-only validation with no user
data mutation.

`sample_call`:
Runs a bounded read-only sample through the real tool runner.

`dry_run`:
Validates and previews a write/execute action without mutating external or local
state. Write and execute tool tests must require approval.

`none`:
Use only when no safe test exists yet. Document why in the tool's tests or
implementation comments if the reason is not obvious.

The test action should use the normal registered tool execution path, not a
separate mock-only path. The permissions UI can gate the call according to the
metadata's `requires_approval` flag; the backend manager also evaluates saved
permission rules before execution. If a rule says `deny`, or if the effective
state is `ask` and there is no active chat approval flow for the settings-page
test, the endpoint returns a structured permission response instead of running
the tool. The backend test runner still enforces `dry_run: true` for
write/execute tools before execution. Use a
`testMode`/dry-run argument or execution option when the underlying operation
needs different behavior.

The backend contract for this UI is:

```http
POST /api/tools/test
```

Request body:

```json
{
  "tool_name": "tool_name_or_provider_hosted_id",
  "thread_id": "optional thread context",
  "project_id": "optional attached project/local clone context",
  "search_depth": "optional search mode hint",
  "args": {}
}
```

The endpoint executes the registered tool implementation through
`optionc.Manager.TestTool`, using the same tool registry and `ExecContext`
dependencies as model runs. It does not persist a synthetic chat message into a
conversation. If a read/download tool promotes an app-managed file, the manager
may persist a first-class file artifact for the supplied thread context so the
settings test can use the same native viewer as chat. For `dry_run` tools, the
manager forces `dry_run: true` into the arguments before execution; callers
cannot accidentally turn a permissions-row test into a mutating operation.

The response includes the normalized arguments, test metadata, collected
progress events, the raw tool output, and `output.artifact` when the registered
tool produced a native artifact payload:

```json
{
  "ok": true,
  "test": {
    "tool_name": "list_files",
    "mode": "sample_call",
    "requires_approval": false,
    "args": {},
    "output": {
      "ok": true,
      "result_format": "json",
      "result": {},
      "artifact": {
        "id": "tool-test-export_thread-artifact",
        "type": "file",
        "title": "thread.ai-thread.json",
        "content": "{...}",
        "metadata": {
          "tool_test": true,
          "tool_name": "export_thread",
          "tool_args": {},
          "result_metadata": {},
          "tool_presentation": {}
        }
      }
    }
  }
}
```

When required arguments are omitted, the endpoint returns `400` with a
machine-readable setup response:

```json
{
  "error": "tool github_clone_repository test requires arguments: repository",
  "tool_name": "github_clone_repository",
  "missing_arguments": ["repository"],
  "required_arguments": ["repository"],
  "required_context": ["connection:github"],
  "input_hint": "Tool test for github_clone_repository requires connection:github; arguments: repository."
}
```

Do not special-case missing inputs inside individual tools for this UI. Put the
requirements in metadata and let `PrepareToolTestArgs` produce the shared
response shape.

For one-of requirements, the same response includes `required_argument_groups`.
Example for a SharePoint file target:

```json
{
  "error": "tool sharepoint_read test requires arguments: item_url, server_relative_url",
  "tool_name": "sharepoint_read",
  "missing_arguments": ["item_url", "server_relative_url"],
  "required_arguments": [],
  "required_argument_groups": [["item_url"], ["server_relative_url"]],
  "required_context": ["connection:sharepoint"],
  "input_hint": "Tool test for sharepoint_read requires connection:sharepoint; one of: item_url OR server_relative_url."
}
```

When persistent permissions block the test, the endpoint returns `409` for
`ask` and `403` for `deny`:

```json
{
  "error": "tool read_file test requires approval",
  "tool_name": "read_file",
  "decision": "ask",
  "category": "read",
  "subject": {
    "kind": "local_location",
    "tool": "read_file",
    "local_root": "/absolute/project/files/README.md"
  },
  "reason": "default"
}
```

## Required Tests for New Tools

Every new tool must have tests for:

- Metadata coverage: enforced by `TestToolMetadataCoversEveryRegisteredTool`.
- Best-in-class metadata taxonomy: enforced by
  `TestBestInClassToolMetadataUsesStableTaxonomy`, which requires stable
  snake_case tool names, stable kebab-case tool-set/family ids, connected-tool
  connector provenance, and `connection:<id>` test context for connected tools.
- Provider schema shape: enforced by
  `TestRegisteredToolDefinitionsUseObjectSchemas`, which requires every
  registered tool to expose an object parameter schema, define every required
  field in `properties`, and keep `required`/`additionalProperties` well typed.
- Closed provider schemas: enforced by
  `TestBestInClassToolDefinitionsUseClosedSchemas`, which fails when a new
  registered tool omits `additionalProperties: false` at the root. There is no
  top-level exception list.
- Recursive provider schema closure: enforced by
  `TestBestInClassToolObjectSchemasAreClosedRecursively`, which fails when a
  nested object under `properties`, `items`, `oneOf`, `anyOf`, or `allOf` is
  left open. Intentional dynamic maps must be recorded in
  `intentionalOpenProviderSchemaPaths()`.
- Metadata shape: enforced by `TestToolMetadataShapeIsComplete`.
- Settings-test schema alignment: enforced by
  `TestBestInClassToolTestInputsMapToProviderSchema`, which requires every
  `required_arguments` and `required_argument_groups` entry to correspond to a
  real provider-schema property, restricts `required_context` to the shared
  context vocabulary, verifies required sample arguments are schema-valid and
  not generic `test-*` fallbacks, and verifies dry-run tests expose a boolean
  `dry_run` argument.
- Presentation shape: enforced by `TestToolMetadataShapeIsComplete` and
  `TestToolPresentationNativeViewerContracts`.
- Auditable native rendering shape: enforced by
  `TestBestInClassToolPresentationIsAuditableForNativeRendering`, which checks
  renderer strategy/key consistency, file/download/upload source refs, remote
  source links, native file preview declarations, produced-file artifact flags,
  promoted-native-input refs, and query/data viewer metadata.
- Rich presentation fields: renderer strategy/key, artifact types, file kinds,
  preview modes, source refs, produced-file flags, promoted-native-input flags,
  and native links are enforced by `TestToolMetadataShapeIsComplete`,
  `TestToolPresentationNativeViewerContracts`,
  `TestFrontendStaticToolMetadataMatchesBackend`,
  `TestFrontendPresentationClassifierCoversNativeViewerFamilies`, and
  `TestToolInventoryRouteJSONContract`.
- Connector helper parity: enforced by
  `TestConnectedToolNameHelpersMatchToolMetadata`.
- Schema/test contract: enforced by
  `TestToolTestMetadataDocumentsRequiredArguments`.
- Settings-test sample coverage: enforced by
  `TestToolTestMetadataProvidesSampleArgumentsForAllInputs`, generated metadata
  parity, and the frontend addressability regression.
- Frontend inventory parity: enforced by
  `TestFrontendPermissionInventoryMatchesToolMetadata` and
  `TestGeneratedFrontendToolMetadataMatchesBackend`.
- Generated metadata freshness: enforced by
  `npm run tools:metadata:check`, which runs the backend generator without
  writing files and compares its output to `lib/generatedToolMetadata.ts`.
- Risk classification: add the tool to
  `TestToolMetadataRiskClassifications` when it is `write` or `execute`.
- Persistent approval metadata: enforced by
  `TestPermissionMetaCoversWriteAndExecuteTools` for every `write` or
  `execute` tool.
- Artifact/attachment/dataset read precision: enforced by
  `TestPermissionMetaScopesArtifactReadToolsToRemoteTarget` when a read tool
  targets an existing app-managed object by id.
- Dry-run test shape: if the metadata declares `dry_run`, the tool schema must
  expose a `dry_run` parameter and `PrepareToolTestArgs` must force it to true.
- Missing test input: if the tool needs arguments, assert
  `PrepareToolTestArgs` returns `MissingToolTestInputError` and the HTTP route
  exposes `missing_arguments`.
- Alternative test inputs: if the tool uses `RequiredArgumentGroups`, assert at
  least two accepted alternatives and assert the missing-input error exposes
  `required_argument_groups`.
- Upload source alternatives: if a tool supports artifact, project path,
  managed local path, generated text, or generated base64 inputs, assert the
  metadata advertises each supported source group and that `PrepareToolTestArgs`
  accepts at least one file-backed source and one generated source.
- Permission-aware tool tests: if the tool maps into persistent approvals,
  assert saved `deny` prevents execution, saved `allow` permits execution, and
  default `ask` returns the structured permission error where applicable.
- Definition schema: the registered definition must have the expected name,
  description, and parameter object.
- Argument validation: unknown fields and out-of-range values should fail.
- Permission behavior: `ask`, `allow`, and `deny` paths when the tool needs
  approval.
- Execution behavior: success, empty result, upstream error, and context
  cancellation where applicable.
- Native rendering behavior: if the tool returns files, artifacts, tables,
  images, PDFs, documents, or links, verify the result metadata drives the
  existing renderer or artifact path.
- First-class file read behavior: if a tool can read local or connector files,
  include regression coverage for supported structured formats such as PDFs,
  spreadsheets, documents, HTML, JSON, and CSV. The model-facing result should
  contain bounded readable text or explicit extraction metadata; it should not
  expose raw binary bytes as the only read path.
- Tool-test artifact behavior: if the tool returns `ArtifactPayload`, verify
  `POST /api/tools/test` includes a native-renderable
  `output.artifact` and that the Permissions tab routes it through the renderer
  registry.
- Audit-trail renderer behavior: if the tool introduces a new native/family
  viewer result shape, add a `/dev/audit-trail` fixture plus Playwright
  assertions that prove the production renderer shows structured semantic
  output instead of raw JSON. GitHub's repository, pull request, issue,
  workflow, approval, and `gh` command widgets are covered in
  `tests/e2e/audit-trail-inspectors.spec.ts`; Jira's JQL search and partial
  related-ticket write result are covered there as representative
  connector-inspector widgets.
- API inventory contract: `GET /api/tools/inventory` must expose the tool and
  its presentation/test metadata using the snake_case JSON contract consumed by
  the frontend.

This repository now includes contract tests intended to fail on incomplete tool
work:

- `TestProviderHostedToolCapabilitiesAreExplicitlyClassified` fails when a
  provider-hosted model capability lacks category, adapter family, settings key,
  provider tool name, label, or description.
- `TestProviderHostedToolCapabilitiesStayOutOfRegisteredToolInventory` fails
  when a hosted capability is accidentally duplicated into the local registered
  tool inventory.
- `TestProviderHostedToolCapabilitiesExposeSettingsTestSpec` and
  `TestHandleTestToolRecognizesProviderHostedCapability` fail when a hosted
  capability cannot be tested from Settings through the non-executing
  body-based `/api/tools/test` path.
- `TestToolInventoryRouteJSONContract` fails when `/api/tools/inventory` stops
  exposing hosted capabilities through the `provider_hosted_tools` snake_case
  contract.
- `TestHostedToolPolicyForRunEvaluatesPersistentPermissions` fails when
  persistent permission rules no longer gate hosted capabilities before provider
  construction.
- `TestToolMetadataCoversEveryRegisteredTool` fails when a registered tool has
  no product metadata.
- `TestBestInClassToolMetadataUsesStableTaxonomy` fails when product metadata
  uses unstable ids, when a metadata entry's key and name drift apart, or when a
  connected tool does not expose connector provenance and connection context.
- `TestBestInClassToolDefinitionsUseClosedSchemas` fails when a registered
  tool omits root `additionalProperties: false`; there is no top-level exception
  list.
- `TestBestInClassToolTestInputsMapToProviderSchema` fails when settings-page
  test requirements mention arguments the model schema does not actually accept,
  when sample placeholders are generic or schema-invalid, when a context label
  is outside the known vocabulary, or when a dry-run test cannot be forced
  through a boolean `dry_run` schema field.
- `TestToolTestMetadataProvidesSampleArgumentsForAllInputs` fails when a
  settings-page test input lacks a sample placeholder or declares unused sample
  data.
- `TestBestInClassToolPresentationIsAuditableForNativeRendering` fails when
  presentation metadata cannot explain how native viewers, file/source refs,
  upload/download provenance, query links, or produced artifacts should be
  audited and rendered.
- `TestToolMetadataShapeIsComplete` fails on missing labels, descriptions,
  origin, tool set, connector provenance, invalid categories, invalid test
  modes, incomplete presentation metadata, invalid presentation enum values, or
  incomplete test hints.
- `TestConnectedToolNameHelpersMatchToolMetadata` fails when a connected tool
  is present in metadata but missing from its connector helper, when a helper
  exposes a tool that metadata does not classify for that connector, or when a
  helper references a tool that is not registered.
- `TestToolTestMetadataDocumentsRequiredArguments` fails when schema-required
  arguments are not listed in test metadata, either as flat required arguments
  or as grouped alternatives.
- `TestFrontendPermissionInventoryMatchesToolMetadata` fails when frontend
  grouping/order hints are missing a tool, have a stale tool, or disagree on
  category.
- `TestGeneratedFrontendToolMetadataMatchesBackend` fails when
  `lib/generatedToolMetadata.ts` was not regenerated from the backend manifest
  or when the Permissions tab no longer consumes the generated fallback.
- `TestFrontendStaticToolTestRequirementsMatchBackend` fails when frontend
  fallback test inputs or one-of input groups drift from backend metadata.
- `TestFrontendStaticGitHubProjectContextMatchesBackend` fails when fallback
  GitHub project-context requirements drift.
- `TestFrontendStaticUnsupportedToolTestsMatchBackend` fails when unsupported
  test declarations drift.
- `TestFrontendStaticToolMetadataMatchesBackend` fails when frontend fallback
  metadata drifts from backend metadata, including presentation metadata.
- `TestFrontendPresentationClassifierCoversNativeViewerFamilies` fails when the
  fallback classifier stops documenting representative file, query, upload,
  repository, browser, and connector families.
- `TestToolInventoryRouteJSONContract` fails when the inventory API stops
  exposing the snake_case metadata contract used by the Permissions tab.
- `TestPermissionMetaCoversReadTools` fails when a read tool cannot be mapped
  into persistent permissions.
- `TestPermissionMetaCoversWriteAndExecuteTools` fails when a mutating or
  executing tool cannot be mapped into persistent permissions.
- `TestPermissionMetasForWriteAndExecuteToolsCarryProductScope` fails when a
  mutating or executing tool's enriched permission metadata loses the product
  tool-set or connector scope needed by group-level Settings rules.
- `TestPermissionMetaCoversLocalProjectReadTools` fails when known local-read
  paths are not represented as `local_location` subjects.
- `TestPermissionMetaScopesArtifactReadToolsToRemoteTarget` fails when
  attachment, artifact, dataset-artifact, or thread-artifact reads fall back to
  broad tool permissions instead of `remote_kind`/`remote_id` subjects.
- `TestManagerStartRunWaitsForApprovalBeforeFinal` fails when a chat run emits
  the terminal `final` event before a required tool approval is approved,
  rejected, canceled, or otherwise resolved.
- `TestToolTestResultMarshalJSONNormalizesArtifactWithoutPayloadMetadata`,
  `TestToolTestResultMarshalJSONMergesArtifactAndOutputResultMetadata`, and
  `TestLargeRowsArtifactPayloadCarriesDatasourceQueryPresentation` fail when
  settings-page tool-test artifacts lose `tool_args`, result metadata,
  query metadata, or presentation metadata required for native rendering.
- `TestSuccessfulOutputLiteralsCarryResultMetadata` fails when a successful
  `Output{OK: true}` literal does not set first-class `Output.ResultMetadata`.
- `TestLegacyOutputHelpersAreNotUsedByToolImplementations` fails when a tool
  implementation uses `JSONOK` or `TextOK` instead of `JSONOKWithMetadata` or an
  explicit `Output` carrying `ResultMetadata`.
- The Playwright audit-trail inspector suite fails when GitHub family widgets
  stop rendering backend-shaped repository, pull request, issue, workflow,
  approval, or `gh` command outputs as structured native UI.
- The same suite fails when representative Jira connector-inspector widgets
  stop rendering JQL search results and partial related-ticket write results as
  semantic UI.

For a new tool, add focused tests in its implementation package as well. A good
minimum set is:

```go
func TestNewToolDefinition(t *testing.T) {
    // Name, description, schema, required args, bounds, additionalProperties.
}

func TestNewToolExecuteValidInput(t *testing.T) {
    // Successful execution result and result metadata.
}

func TestNewToolExecuteRejectsInvalidInput(t *testing.T) {
    // Unknown fields, missing semantic one-of inputs, bad enum values, limits.
}

func TestNewToolPermissionMapping(t *testing.T) {
    // Subject kind, connector/local root, category, operation, summary.
}

func TestNewToolDryRunOrApproval(t *testing.T) {
    // Write/execute dry_run does not mutate and approval metadata is enforced.
}

func TestNewToolSettingsTestPath(t *testing.T) {
    // PrepareToolTestArgs and/or POST /api/tools/test returns the
    // expected output, missing-input response, or permission response.
}
```

Use this matrix when deciding whether the focused tests are complete:

| Tool behavior | Required focused coverage |
| --- | --- |
| Read-only local file/project access | local-location permission subject, path normalization, traversal rejection, native/file result metadata |
| Remote read/download | connector context, remote id/url argument handling, native file/artifact metadata, upstream error mapping |
| Upload/write | dry-run preview, saved permission denial, saved permission allow, no mutation in settings test |
| Execute/browser/CLI/repository operation | dry-run preview, approval metadata, cancellation/error behavior, bounded command/action args |
| Artifact producer | `Output.Artifact`, artifact metadata, `output.artifact` in tool-test HTTP response, renderer routing |
| Query/data producer | query metadata, warehouse job/link metadata, empty result behavior, data renderer shape |
| Source-backed artifact/data producer | declared `source.*` refs, local/artifact/query-job source permission scopes, source metadata in output |
| Alternative inputs | accepted alternatives and shared missing-input response with `required_argument_groups` |
| Provider-hosted capability | `ProviderHostedToolCapabilities` entry, adapter request-shape test, `ProviderExecuted` streamed-result test when the provider returns a result |

Run these before handing off a tool change:

```sh
npm run test:tool-migration
```

`test:tool-migration` intentionally composes the backend tool/permission/API
packages, generated metadata freshness check, TypeScript, static tool
contracts, Playwright tool UI contracts, and the production build. Use the
narrower commands while iterating, but use the composed script for handoff.

## Frontend Inventory Direction

The permissions UI fetches the canonical runtime inventory from
`GET /api/tools/inventory` and falls back to `lib/toolInventory.ts` when the
endpoint is unavailable in older builds or test fixtures. The static fallback
hydrates its rows with the same provenance and test-spec shape as the backend:
origin, tool set, connector id, description, mode, approval requirement,
required arguments, sample arguments, required context, and input hint.

Expanded tool rows in the Permissions tab surface `Tool test` readiness directly
from metadata: test mode, approval requirement, required arguments, required
context, and the input hint. Rows call `POST /api/tools/test` through
`services/toolTestApi.ts`, with the selected `tool_name` in the JSON body.
Required arguments render as per-tool inputs; values are parsed as JSON when
they look like arrays, objects, booleans, numbers, `null`, or quoted strings,
otherwise they are sent as strings. Backend-supported context currently renders
as `project_id` and `thread_id`; other contexts remain metadata chips and are
satisfied by the tool runtime or by required arguments such as `artifact_id`,
`job_id`, and `owner_token`.
`sample_arguments` render as placeholders in those inputs so complex values are
discoverable without being auto-filled or auto-run.

The architecture is:

1. Backend registry owns canonical metadata.
2. `GET /api/tools/inventory` exposes the manifest.
3. Frontend renders permissions from that endpoint.
4. `go generate ./internal/chat/tools` writes the static fallback metadata to
   `lib/generatedToolMetadata.ts`.
5. `services/toolInventoryApi.ts` is the frontend fallback boundary: unavailable
   legacy endpoints use the generated fallback, but malformed successful
   runtime inventory responses fail visibly.
6. `npm run tools:metadata:check` and parity tests prevent unclassified tools,
   stale generated metadata, and stale static grouping hints from shipping.

## Agent Checklist

Before finishing any tool change, verify:

- The tool is registered in `NewRegistry`.
- The tool has an entry in `tool_metadata.go`.
- The tool is in the correct tool-name helper slice.
- If the capability is provider-hosted rather than locally executed, it has an
  entry in `provider_hosted_tool_metadata.go`, appears in the
  `provider_hosted_tools` inventory payload, exposes a non-executing
  settings-page `connection_check`, has permission-policy tests, adapter
  request tests, and streamed `ProviderExecuted` result tests instead of local
  registry metadata.
- `go generate ./internal/chat/tools` has been run and
  `npm run tools:metadata:check` confirms `lib/generatedToolMetadata.ts`
  includes it.
- `lib/toolInventory.ts` includes grouping/order hints if the tool belongs to a
  new or reordered tool set.
- The permission category is defensible.
- The origin and connector provenance are visible to users.
- A future permissions-row test action has a declared strategy.
- Presentation metadata declares `RendererStrategy`, `RendererKey`,
  `NativeViewer`, and any artifact/file/link/source behavior the tool exposes.
- Required test arguments and context are visible in metadata.
- Required test arguments have valid `sample_arguments` placeholders.
- Required test arguments and context are visible in the Permissions tab.
- File-capable tools declare file kinds, preview modes, source refs, and
  produced/promoted file behavior where applicable.
- Query tools expose query/data presentation and warehouse job links where
  applicable.
- Query lifecycle outputs preserve provider job link metadata such as
  `provider_job_id`, `provider_job_url`, `warehouse_job_id`, and `statement_id`
  when those fields exist on the source query job.
- Source-backed artifact tools expose `source.*` refs and scope local files,
  app artifacts, and async query jobs to the exact permission subject they read.
- Upload tools expose their supported artifact/path/local/generated source
  alternatives in both test metadata and presentation metadata.
- Write/execute tools have persistent approval metadata.
- Artifact-backed read tools use scoped `remote_kind`/`remote_id` subjects
  rather than broad tool-set permission subjects.
- Tool outputs set `Output.ResultMetadata` for native renderer routing, or have
  explicit tests proving the manager fallback supplies the expected metadata.
- Live artifacts include `tool_args` and `tool_presentation` when they are
  expected to render through the generic inspector.
- New tests cover schema, execution, permission, and rendering behavior.
- `/api/tools/inventory` includes the tool with the full snake_case metadata
  contract.
- `TestGeneratedFrontendToolMetadataMatchesBackend` passes, proving the static
  fallback uses the backend-generated metadata.
- `go test ./internal/chat/tools` passes from the `server` directory.
