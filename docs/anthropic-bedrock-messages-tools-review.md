# Anthropic, Bedrock, Messages API, and Tool Integration Review

> Historical review. For the current cross-provider compatibility source of truth, see [Model Provider Compatibility Review](model-provider-compatibility.md). The provider and Bedrock document paths have changed since this snapshot.

Date: 2026-05-29

## Scope

This review covers the current Anthropic and Bedrock provider implementation, the tool registry that feeds those providers, OpenAI Responses hosted tools, and the local browser-control tools currently named as CUA browser tools.

Primary code paths reviewed:

- `server/internal/chat/providers/anthropic/provider.go`
- `server/internal/chat/providers/bedrock/provider.go`
- `server/internal/chat/providers/openai_compat/provider.go`
- `server/internal/chat/providers/factory.go`
- `server/internal/chat/engine/engine.go`
- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/tools/registry.go`
- `server/internal/chat/tools/browser_control_tools.go`
- `constants.ts`
- `types.ts`

Official references checked:

- Anthropic Messages API: https://docs.anthropic.com/en/api/messages
- Anthropic tool use overview: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/overview
- Anthropic web search tool: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/web-search-tool
- Anthropic code execution tool: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/code-execution-tool
- Anthropic computer use tool: https://docs.anthropic.com/en/docs/agents-and-tools/tool-use/computer-use-tool
- Anthropic compaction: https://platform.claude.com/docs/en/build-with-claude/compaction
- AWS Bedrock Anthropic Claude Messages request/response: https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-request-response.html
- AWS Bedrock `InvokeModelWithResponseStream`: https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_InvokeModelWithResponseStream.html
- AWS Bedrock API compatibility table: https://docs.aws.amazon.com/bedrock/latest/userguide/models-api-compatibility.html
- OpenAI computer use: https://platform.openai.com/docs/guides/tools-computer-use
- OpenAI web search: https://platform.openai.com/docs/guides/tools-web-search
- OpenAI code interpreter: https://platform.openai.com/docs/guides/tools-code-interpreter

## Executive Summary

The first-class Bedrock adapter is broadly compatible with Anthropic Messages-style custom function tool use. It sends an AWS Bedrock Runtime request to `/model/{modelId}/invoke-with-response-stream`, emits an Anthropic Messages body with `anthropic_version`, `messages`, `tools`, and `max_tokens`, signs the request for Bedrock Runtime or applies a Bedrock API key, and parses the AWS event-stream envelope back into Anthropic stream events.

The largest compatibility gap is not custom tools. It is hosted/provider-executed tools and beta feature drift. Direct Anthropic supports hosted `web_search` and `code_execution` tools in this codebase, including persisted provider-executed tool results. Bedrock does not. Current Anthropic docs explicitly state web search is not available on Amazon Bedrock, and current AWS Bedrock Claude Messages docs list custom/computer/bash/text-editor tools rather than Anthropic hosted `web_search` or `code_execution` tools. The Bedrock adapter correctly omits those hosted tools. However, the application defaults still expose Bedrock web search as enabled, which creates a misleading settings surface.

The second major risk is Bedrock context management. Direct Anthropic compaction uses `compact-2026-01-12` with `context_management.edits[].type = compact_20260112`. The first-class Bedrock provider currently sends that same beta name in `anthropic_beta`. AWS currently documents a different Bedrock context-management beta header, `context-management-2025-06-27`, for specific Claude models. This makes default Bedrock compaction a likely validation-risk area unless the deployed Bedrock endpoint has explicitly adopted the newer Anthropic compaction contract.

The third major gap is computer use naming. The repository's `cua_browser_*` tools are local function tools backed by the in-app/browser-control bridge. They are compatible with Bedrock as ordinary custom function tools, but they are not OpenAI native CUA and they are not Anthropic native computer use. Native OpenAI CUA uses the Responses API `computer_use_preview` tool and `computer_call`/`computer_call_output` loop. Native Anthropic computer use uses `computer_*`, `bash_*`, and `text_editor_*` tools plus beta headers. The app currently implements neither native provider computer-use protocol.

## Provider Paths

### Direct Anthropic Messages

The direct Anthropic provider builds `/v1/messages` requests in `server/internal/chat/providers/anthropic/provider.go`. It maps local tool definitions to Anthropic `tools` entries, includes optional hosted `web_search` and `code_execution` tools, includes context management when enabled, and sends `anthropic-beta` headers for compaction, code execution, and skills.

Evidence:

- `StreamChat` posts to `p.baseURL + "/v1/messages"` and builds the Messages body at `server/internal/chat/providers/anthropic/provider.go:1486-1537`.
- It disables hosted web search and code execution only when `BedrockMessagesShape` is set at `server/internal/chat/providers/anthropic/provider.go:1488-1494`.
- It sets Anthropic beta headers for compaction, code execution, and skills at `server/internal/chat/providers/anthropic/provider.go:1557-1599`.
- `toolsToAnthropic` emits custom function tools and optionally adds `web_search` and `code_execution` at `server/internal/chat/providers/anthropic/provider.go:2038-2084`.
- Direct Anthropic user content supports text, images, and inline PDF/text document blocks at `server/internal/chat/providers/anthropic/provider.go:2185-2249`.

### First-Class Bedrock Messages

The normal Bedrock `messages` route is the first-class Bedrock Runtime adapter, not the Anthropic/Mantle-shaped provider. `factory.go` explicitly normalizes away Mantle-style URLs and constructs `bedrock.New`.

Evidence:

- `factory.go` selects Bedrock Runtime for `apiType == "messages"` and routes non-`messages` Bedrock configs through the OpenAI-compatible provider at `server/internal/chat/providers/factory.go:349-355`.
- It comments that Bedrock messages use Runtime `InvokeModelWithResponseStream` with an Anthropic Messages body and strips Mantle URLs at `server/internal/chat/providers/factory.go:438-442`.
- `bedrockRuntimeBaseURL` removes `/anthropic`, `/v1/messages`, and Mantle-specific base URL suffixes at `server/internal/chat/providers/factory.go:448-467`.
- `bedrock.Provider.StreamChat` posts to `/model/{model}/invoke-with-response-stream`, sends `anthropic_version`, `messages`, `max_tokens`, optional `tools`, and optional `context_management` at `server/internal/chat/providers/bedrock/provider.go:238-267`.
- Bedrock auth supports either bearer API key or AWS SigV4 at `server/internal/chat/providers/bedrock/provider.go:708-716`.
- The adapter parses AWS event-stream frames and unpacks Bedrock `chunk.bytes` envelopes at `server/internal/chat/providers/bedrock/provider.go:590-677`.

### Anthropic Provider `BedrockMessagesShape`

The Anthropic provider still has a Bedrock-shaped mode. It signs against `bedrock-mantle`, posts to `/anthropic/v1/messages`, converts attachments to Converse-like `document`/`image` blocks, and disables hosted Anthropic tools. The current factory does not select this path for normal Bedrock `messages` configs.

Evidence:

- `BedrockMessagesShape` controls base URL normalization and headers at `server/internal/chat/providers/anthropic/provider.go:116-176`.
- Hosted web search and code execution are explicitly nulled in this mode at `server/internal/chat/providers/anthropic/provider.go:1488-1494`.
- It serializes content as `document`, `text`, and `image` Bedrock-style blocks at `server/internal/chat/providers/anthropic/provider.go:2251-2304`.

This path should be treated as experimental or legacy unless the product intentionally supports Claude Platform on AWS/Mantle separately from Amazon Bedrock Runtime.

## Tool Compatibility Matrix

| Tool family | Direct Anthropic Messages | Bedrock Runtime Messages | OpenAI Responses | Notes |
| --- | --- | --- | --- | --- |
| Local function tools | Supported | Supported | Supported | Core agent loop handles `tool_use`/function calls and appends local tool results. |
| Search/file/git/artifact/chart/data tools | Supported as local function tools | Supported as local function tools | Supported as function tools | Tool selection is reduced for Anthropic/Bedrock to control schema size. |
| Datasource SQL tools | Supported as local function tools | Supported as local function tools | Supported as function tools | Enabled per datasource and profile. |
| Atlassian tools | Supported as local function tools | Supported as local function tools | Supported as function tools | Added after the Anthropic tool-profile reduction when enabled. |
| GitHub tools | Supported as local function tools | Supported as local function tools | Supported as function tools | Added after provider-specific filtering when enabled. |
| Local browser-control `cua_browser_*` tools | Supported as local function tools | Supported as local function tools | Supported as function tools | These are not native OpenAI CUA or native Anthropic computer-use tools. |
| Anthropic hosted web search | Supported in direct Anthropic provider | Not supported | Not applicable | Anthropic docs say web search is not available on Amazon Bedrock. |
| Anthropic hosted code execution | Supported in direct Anthropic provider | Not supported | Not applicable | AWS Bedrock Claude Messages docs do not list the hosted `code_execution_*` server tool. |
| Anthropic skills via code execution container | Supported in direct Anthropic provider | Not supported | Not applicable | Requires Anthropic code execution container support. |
| Anthropic native computer use | Not implemented | Not implemented | Not applicable | AWS documents Bedrock computer tool shapes, but the repo does not emit those tool definitions. |
| OpenAI hosted web search | Not applicable | Only in non-`messages` OpenAI-compatible profile if endpoint supports it | Supported | Repo emits Responses `web_search`; do not assume Bedrock OpenAI-compatible endpoints implement hosted OpenAI tools. |
| OpenAI code interpreter | Not applicable | Only in non-`messages` OpenAI-compatible profile if endpoint supports it | Supported | Repo emits Responses `code_interpreter` with containers. |
| OpenAI native CUA | Not implemented | Not compatible | Not implemented | Native CUA requires `computer_use_preview` plus a `computer_call` output loop. |
| Images | Supported | Supported | Supported | Bedrock Runtime path supports images but not other file attachments. |
| PDF/text documents | Supported direct Anthropic | Not supported in first-class Bedrock Runtime path | PDF only in OpenAI Responses input_file path | Anthropic Bedrock-shaped mode has document serialization, but the Runtime adapter does not. |
| Native/server compaction | Supported direct Anthropic | Risky/default-on mismatch | Supported for OpenAI context management | Bedrock uses direct Anthropic compaction beta in repo, while AWS current docs list a different beta header. |

## Detailed Findings

### 1. Bedrock custom tool use is structurally sound

Bedrock Runtime supports Anthropic Messages `tools` and returns `tool_use` content blocks. The repository follows that model for custom tools:

- Request tool definitions use `name`, `description`, and `input_schema` at `server/internal/chat/providers/bedrock/provider.go:873-882`.
- Assistant `tool_use` and user `tool_result` content are preserved across turns at `server/internal/chat/providers/bedrock/provider.go:910-975`.
- The engine handles tool call turns provider-independently, executing local tools and appending tool results at `server/internal/chat/engine/engine.go:493-525`.

This is the compatibility baseline that works for the repository's core local tools: file, git, search, artifacts, charting, dataset, datasource, Atlassian, GitHub, and browser-control tools.

### 2. Bedrock hosted Anthropic tools are correctly absent in code but incorrectly suggested by defaults

The Bedrock provider does not have `webSearch` or `codeExecution` options and does not add Anthropic hosted tools. That is correct for Amazon Bedrock based on the currently documented APIs: Anthropic documents web search as unavailable on Amazon Bedrock, and AWS Bedrock's Claude Messages tool list documents custom/computer/bash/text-editor tools rather than Anthropic hosted `web_search` or `code_execution`.

The mismatch is in product settings:

- `constants.ts` defaults Bedrock `webSearch.enabled` to `true` with `web_search_20260209` at `constants.ts:101-117`.
- `types.ts` allows `webSearch` and `codeExecution` on all `ProviderConfig` values at `types.ts:218-251`.
- `factory.go` never passes Bedrock web search or code execution into `bedrock.New`, so the setting has no runtime effect for the first-class Bedrock Messages adapter.

Impact:

- Users may believe Bedrock web search is active when it is silently ignored.
- If future refactoring accidentally wires these settings into Bedrock Runtime, requests will likely fail validation.

Recommendation:

- Split provider capabilities in settings and UI. For Bedrock `messages`, hide or disable Anthropic hosted web search and code execution unless a verified non-Bedrock Claude Platform on AWS path is selected.
- Add a request-shape unit test asserting Bedrock Runtime requests never contain `web_search_*` or `code_execution_*` hosted tools.

### 3. Bedrock compaction is the highest-risk request-shape issue

Direct Anthropic compaction uses:

- beta header `compact-2026-01-12`
- `context_management.edits[].type = compact_20260112`

The repo applies that same beta and edit type to Bedrock:

- `server/internal/chat/providers/bedrock/provider.go:247-260`
- default enabled in `server/internal/chat/providers/factory.go:302-347`
- default enabled in `constants.ts:101-108`

AWS current Bedrock docs list `context-management-2025-06-27` as the documented Bedrock context-management beta header for Claude Sonnet 4.5 and Claude Haiku 4.5. That does not match the repo's `compact-2026-01-12` value.

Impact:

- Bedrock runs may fail with validation errors as soon as compaction is included, especially once input crosses trigger thresholds or on models/regions that do not recognize the direct Anthropic beta name.
- Since Bedrock compaction defaults to enabled, the failure mode can appear late in long conversations rather than at initial provider configuration.

Recommendation:

- Disable Bedrock compaction by default until the exact Bedrock-supported beta and edit shape are verified for the target models and regions.
- If Bedrock context management is required, add a Bedrock-specific compaction mode with documented `anthropic_beta` values and model gating, separate from direct Anthropic `compact-2026-01-12`.
- Add snapshot tests for Bedrock request bodies with compaction on/off.

### 4. The Bedrock Runtime adapter lacks document/file attachment support

Direct Anthropic supports inline PDF and text document blocks. The first-class Bedrock Runtime adapter only serializes user text and image attachments:

- Image-only Bedrock content handling is at `server/internal/chat/providers/bedrock/provider.go:924-948`.
- No `FileAttachments` path exists in the first-class Bedrock provider.
- The unused Anthropic `BedrockMessagesShape` path does have `document` block serialization at `server/internal/chat/providers/anthropic/provider.go:2251-2304`.

Impact:

- PDF/text attachments that work on direct Anthropic will be omitted when using Bedrock Runtime.
- The app may present a provider-independent file workflow, but Bedrock Runtime currently receives only the text prompt and images.

Recommendation:

- Either document Bedrock's current no-document behavior in the UI, or add Bedrock Runtime document support using the exact shape supported by the selected Bedrock API.
- Do not reuse the Converse-style block shape in the Runtime adapter without a compatibility test against Bedrock Runtime Anthropic Messages.

### 5. Local browser-control tools are Bedrock-compatible but are not native CUA

The local browser-control integration defines function tools named:

- `cua_browser_status`
- `cua_browser_navigate`
- `cua_browser_snapshot`
- `cua_browser_click`
- `cua_browser_type`
- `cua_browser_press`
- `cua_browser_wait`
- `cua_browser_back`
- `cua_browser_logs`
- `cua_browser_clear_logs`

Evidence:

- Tool names are declared at `server/internal/chat/tools/browser_control_tools.go:13-24`.
- Tools are registered as ordinary `Type: "function"` definitions at `server/internal/chat/tools/browser_control_tools.go:165-174`.
- They are exposed only when browser control is enabled at `server/internal/chat/tools/registry.go:171-181` and filtered by settings at `server/internal/chat/optionc/browser_control_filter.go:11-30`.
- The system prompt guidance calls them computer-use browser tools and instructs snapshot-before-action behavior at `server/internal/chat/optionc/browser_control_filter.go:33-47`.

Compatibility:

- Bedrock: compatible as normal custom function tools.
- Direct Anthropic: compatible as normal custom function tools.
- OpenAI Responses: compatible as normal function tools.
- Native OpenAI CUA: not implemented.
- Native Anthropic computer use: not implemented.

OpenAI native CUA current docs use a `computer_use_preview` tool shape with display size and environment fields, and the model returns computer actions that must be executed and returned through computer-call output items. The repository does not emit `computer_use_preview`, does not parse `computer_call`, and does not return `computer_call_output`.

Anthropic native computer use current docs use `computer_*`, `bash_*`, and `text_editor_*` tool definitions plus beta headers. AWS Bedrock documents computer, bash, and text-editor tool shapes for Claude Messages, but the repository does not emit those native tool types either.

Recommendation:

- Rename the product-facing label away from "OpenAI CUA" if it refers to these local function tools. A more accurate term is "local browser-control function tools".
- If native OpenAI CUA is desired, add a distinct OpenAI Responses feature path that emits `computer_use_preview`, parses `computer_call`, executes browser actions, handles pending safety checks, captures screenshots, and sends `computer_call_output`.
- If native Anthropic/Bedrock computer use is desired, add a distinct Anthropic-schema computer-use provider feature with beta gating and native `computer_*` tool output translation.

### 6. Direct Anthropic hosted tool result capture is solid

The direct Anthropic provider explicitly captures hosted/server tool calls and results:

- It converts `server_tool_use` blocks to provider-executed `ToolCall` records at `server/internal/chat/providers/anthropic/provider.go:552-593`.
- It converts `*_tool_result` blocks to persisted provider tool-result envelopes at `server/internal/chat/providers/anthropic/provider.go:595-663`.
- It handles streamed `server_tool_use` blocks alongside normal `tool_use` blocks at `server/internal/chat/providers/anthropic/provider.go:1787-1813`.
- It attaches provider tool calls and results to the final event at `server/internal/chat/providers/anthropic/provider.go:1977-2021`.
- The engine persists final-event provider tool results at `server/internal/chat/engine/engine.go:455-492`.

This is the right pattern for direct Anthropic web search and code execution. The Bedrock provider does not have equivalent hosted-tool parsing, which is acceptable today because the currently documented Bedrock Claude Messages contract does not include Anthropic hosted `web_search` or `code_execution` server tools. If AWS adds server-executed tool blocks later, the Bedrock adapter would need similar parsing.

### 7. OpenAI Responses hosted tools are implemented, but are not portable to Bedrock by assumption

The OpenAI-compatible provider emits Responses hosted tool definitions:

- `web_search` default tool type at `server/internal/chat/providers/openai_compat/provider.go:471-495`.
- `code_interpreter` with `container` config at `server/internal/chat/providers/openai_compat/provider.go:496-518`.
- Web-search provider tool events are parsed at `server/internal/chat/providers/openai_compat/provider.go:1200-1208` and finalized later in the stream.

The Bedrock factory can route non-`messages` Bedrock configs into `openai_compat` for `responses` or `chat_completions` API types. This should not be interpreted as automatic support for OpenAI hosted tools on Bedrock. AWS has API-compatibility surfaces for selected models, but hosted OpenAI tools are provider-side services, not just JSON syntax. A Bedrock endpoint accepting Responses-compatible JSON may still reject or ignore OpenAI hosted tools.

Recommendation:

- For Bedrock non-`messages` profiles, make hosted OpenAI tools opt-in per verified endpoint/model instead of inheriting OpenAI defaults.
- Add provider capability flags so `web_search`, `code_interpreter`, and future CUA cannot be enabled just because the transport is Responses-shaped.

### 8. Tool schema control is useful but may reduce model accuracy

Anthropic and Bedrock use a reduced tool profile:

- `isAnthropicProviderContext` treats both `anthropic` and `bedrock` as Anthropic-style providers at `server/internal/chat/optionc/manager.go:3598-3605`.
- `buildToolDefinitions` selects a narrower Anthropic set before merging optional Atlassian/browser/GitHub tools at `server/internal/chat/optionc/manager.go:1561-1589`.
- `selectAnthropicToolDefs` starts with analysis core tools and conditionally adds search, CSV, and datasource tools at `server/internal/chat/optionc/manager.go:3429-3464`.
- Schema compaction truncates descriptions and removes JSON Schema metadata at `server/internal/chat/optionc/manager.go:3478-3539`.

This is a pragmatic response to Claude's sensitivity to large tool schemas and Bedrock request size limits. The tradeoff is that complex chart, dataset, and datasource tools may be harder for the model to call correctly after parameter descriptions are removed.

Recommendation:

- Keep schema compaction, but add targeted tests for the highest-value tools where property descriptions matter.
- Consider provider-specific compact schema profiles instead of a single aggressive stripping rule.

### 9. Tool name mapping should enforce provider limits

Tool names are sanitized to `[a-zA-Z0-9_-]`, but both Anthropic and Bedrock instantiate the mapper with no length limit:

- `toolnamemapper.New(0)` in `server/internal/chat/providers/anthropic/provider.go:1481`.
- `toolnamemapper.New(0)` in `server/internal/chat/providers/bedrock/provider.go:233`.
- `applyMaxLength` is disabled when `maxLength <= 0` at `server/internal/chat/providers/toolnamemapper/mapper.go:72-76`.

Today the repository's tool names are short enough. The risk is future integrations with long names. Anthropic tool-name constraints are stricter than arbitrary strings, and Bedrock validation tends to be immediate.

Recommendation:

- Set a provider max length in Anthropic and Bedrock tool mappers and add collision tests.
- Keep canonical-name recovery so existing persisted tool calls still map back correctly.

### 10. `BedrockMessagesShape` and Bedrock Runtime should not be mixed implicitly

There are currently two Bedrock-like shapes:

- First-class Bedrock Runtime: Anthropic Messages body inside AWS Runtime `/model/{model}/invoke-with-response-stream`.
- Anthropic provider `BedrockMessagesShape`: `/v1/messages` style transport with Bedrock-shaped content blocks and `bedrock-mantle` signing support.

The factory now selects Runtime and strips Mantle base URLs. That is good for clarity. The risk is that future work may reuse helpers from one shape in the other without integration tests. The content block shapes differ meaningfully, especially documents and images.

Recommendation:

- Name the experimental/Mantle path explicitly in code and tests, for example `ClaudePlatformAWSMessagesShape`, if it remains supported.
- Keep the Bedrock Runtime adapter self-contained and tested against AWS Bedrock request/response docs.

## Priority Recommendations

1. Disable or hide Bedrock hosted web search and code execution settings for `apiType: "messages"`.
2. Disable Bedrock compaction by default, or implement a Bedrock-specific context-management mode with AWS-documented beta headers and model gating.
3. Add Bedrock request-shape tests:
   - no hosted `web_search_*` or `code_execution_*`
   - no direct Anthropic compaction beta unless explicitly opted in
   - images are included
   - PDF/text attachments are either rejected with a clear message or serialized through a verified Bedrock-supported shape
4. Rename local `cua_browser_*` product language to avoid implying native OpenAI CUA.
5. Add a separate native OpenAI CUA implementation only if the product needs OpenAI `computer_use_preview` semantics.
6. Add a separate native Anthropic/Bedrock computer-use implementation only if the product needs Claude `computer_*` semantics.
7. Introduce explicit provider capability flags instead of deriving hosted-tool behavior from provider ID or API shape.
8. Enforce Anthropic/Bedrock tool-name length limits in `toolnamemapper`.

## Practical Compatibility Guidance

For Bedrock today, rely on custom local function tools. These include file/git/search/artifact/chart/dataset/datasource tools, optional Atlassian/GitHub tools, and the local browser-control function tools. Do not rely on Anthropic hosted web search, Anthropic hosted code execution, Anthropic skills, or OpenAI native CUA on Bedrock.

For direct Anthropic, hosted web search and code execution are implemented and have provider-executed result capture. Keep them separate from local tools in product language because they run in Anthropic infrastructure and produce different result block types.

For OpenAI Responses, hosted web search and code interpreter are implemented. Native OpenAI CUA is not implemented. The local browser-control tools can still be exposed as normal functions, but that is a separate protocol from OpenAI's computer-use tool.
