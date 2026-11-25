# Anthropic, Bedrock, and Tooling Compatibility Review

> Historical review. For the current cross-provider compatibility source of truth, see [Model Provider Compatibility Review](model-provider-compatibility.md). The provider and Bedrock document paths have changed since this snapshot.

Date: 2026-05-29

## Scope

This review covers the active chat runtime path:

`Frontend -> HTTP API -> optionc.Manager -> engine.RunAgentLoop -> Go provider adapters -> local tool registry -> storage`

The TypeScript provider adapters under `providers/adapters/*` still exist, but the current application run path uses the Go provider factory and Go provider implementations. The TypeScript adapters are relevant mostly as historical parity references and for settings expectations.

Primary code reviewed:

- `server/internal/chat/providers/factory.go`
- `server/internal/chat/providers/anthropic/provider.go`
- `server/internal/chat/providers/bedrock/provider.go`
- `server/internal/chat/providers/openai_compat/provider.go`
- `server/internal/chat/engine/engine.go`
- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/tools/registry.go`
- `server/internal/chat/tools/browser_control_tools.go`
- `constants.ts`
- `lib/providerConfigRuntime.ts`
- `providers/catalog/providerDefs.ts`

External references used:

- [AWS: Inference using Anthropic Messages API](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-messages-api.html)
- [AWS: Anthropic Claude Messages API](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html)
- [AWS: Claude Messages request and response](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-request-response.html)
- [AWS: Claude Messages tool use](https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages-tool-use.html)
- [AWS: Claude Messages compaction](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-compaction.html)
- [AWS: APIs supported by Amazon Bedrock](https://docs.aws.amazon.com/bedrock/latest/userguide/apis.html)
- [AWS: Bedrock API keys](https://docs.aws.amazon.com/bedrock/latest/userguide/api-keys.html)
- [Anthropic: Tool use with Claude](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview)
- [Anthropic: Tool reference](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-reference)
- [Anthropic: Web search tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool)
- [Anthropic: Code execution tool](https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool)
- [OpenAI: Computer use](https://developers.openai.com/api/docs/guides/tools-computer-use)
- [OpenAI: Using tools](https://developers.openai.com/api/docs/guides/tools)
- [OpenAI: Responses create reference](https://developers.openai.com/api/reference/resources/responses/methods/create)

## Executive Summary

The direct Anthropic integration is the most complete and best-aligned implementation. It supports Anthropic Messages request shape, custom client tools, hosted Anthropic web search and code execution, streaming tool-call arguments, provider-executed tool display, `pause_turn`, provider state, provider payload replay, file/image inputs, and native compaction events.

The native Bedrock integration is solid for basic Anthropic Messages over `InvokeModelWithResponseStream` with custom local function tools. It correctly uses `anthropic_version: bedrock-2023-05-31`, AWS event-stream parsing, `tool_use`/`tool_result` loops, system prompts, image blocks, Bearer API keys or SigV4, and Bedrock compaction request fields.

Follow-up implementation status:

- Fixed custom tool schema serialization so no-argument tools are sent as valid object JSON schemas instead of `{}` across Go Anthropic, Bedrock, OpenAI-compatible providers, and TypeScript provider adapters.
- Added Bedrock regression coverage for the full optional tool catalog: Jira, Confluence, GitHub including `github_run_gh_command`, and local browser CUA tools.
- Added an engine-level Bedrock loop test that executes representative Jira, Confluence, GitHub CLI, and CUA tool calls and verifies the second Bedrock request replays both assistant `tool_use` and user `tool_result` blocks.
- Verified local app live readiness: the server is healthy, Atlassian browser automation starts, external `gh` CLI works for a read-only repository call, but Bedrock/Jira/Confluence/GitHub-app/CUA chat live runs are blocked by current local settings and locked/unconfigured secrets.

Bedrock is not at parity with direct Anthropic or OpenAI Responses. The main gaps are:

1. Bedrock web search and code execution settings are exposed/defaulted in the UI, but the native Bedrock provider ignores them and sends only local custom tools.
2. Bedrock server-side compaction is requested and streamed, but the provider does not preserve the returned `compaction` block in provider payload for future Messages turns.
3. Bedrock OpenAI-compatible `responses`/`chat_completions` mode defaults to `bedrock-runtime`, while AWS documents the OpenAI-compatible APIs on the `bedrock-mantle` endpoint.
4. Bedrock native Messages handles image attachments but not document/file attachments, while direct Anthropic supports PDF and text documents.
5. The local browser CUA tools are ordinary function tools and should work with Bedrock custom tool use. They are not OpenAI native CUA and should not be represented as compatible with OpenAI `computer` / `computer_call_output` without additional provider work.

## Current Provider Routing

### Direct Anthropic

`factory.go` builds an `anthropic.Provider` for provider ID `anthropic`. It reads Anthropic settings, injects `x-api-key`, and passes through Anthropic web search and code execution config.

The provider posts to `/v1/messages`, sets `anthropic-version`, streams SSE unless disabled, and translates internal tool definitions to Anthropic tools:

```json
{
  "name": "tool_name",
  "description": "...",
  "input_schema": { "...": "..." }
}
```

It also appends Anthropic hosted tools when enabled:

- `web_search_20250305` or configured `web_search_*`
- `code_execution_20250825` or configured `code_execution_*`

The implementation handles client tool calls and hosted server tool blocks separately. Hosted server tools are surfaced as `ProviderExecuted` tool calls and tool results in the final message, while client tools are returned to the engine for local execution.

### Native Bedrock Messages

`factory.go` builds a `bedrock.Provider` for provider ID `bedrock` when the active Bedrock API type is blank or `messages`.

The factory deliberately normalizes the base URL to Bedrock Runtime and calls:

`POST {base}/model/{model}/invoke-with-response-stream`

The request body uses the Anthropic Messages shape expected by Bedrock Runtime:

```json
{
  "anthropic_version": "bedrock-2023-05-31",
  "messages": [],
  "max_tokens": 8192,
  "system": "...",
  "tools": []
}
```

When compaction is enabled, it also sends:

```json
{
  "anthropic_beta": ["compact-2026-01-12"],
  "context_management": {
    "edits": [
      {
        "type": "compact_20260112",
        "instructions": "...",
        "trigger": { "type": "input_tokens", "value": 170000 }
      }
    ]
  }
}
```

This is broadly aligned with AWS's documented Runtime Messages and compaction examples. AWS currently documents Messages API availability on both `bedrock-mantle` and `bedrock-runtime`, with `bedrock-mantle` recommended for new Anthropic-compatible applications and `bedrock-runtime` useful for existing AWS SDK-style integrations.

### Bedrock OpenAI-Compatible Mode

If active Bedrock `apiType` is `responses` or `chat_completions`, `factory.go` routes to `openai_compat.Provider` with provider ID `bedrock`.

This path is not Bedrock Anthropic Messages. It is an OpenAI-compatible API path, with OpenAI-style tool schemas and OpenAI Responses/Chat Completions streaming parsers.

Current defaulting is risky: `bedrockCompatibleProviderFromConfig` defaults the base URL to `https://bedrock-runtime.{region}.amazonaws.com`. AWS documents OpenAI-compatible Responses and Chat Completions under `bedrock-mantle.{region}.api.aws`, with the OpenAI SDK base URL typically set to `https://bedrock-mantle.{region}.api.aws/v1`.

## Tool Integration Compatibility Matrix

| Tool family | Direct Anthropic Messages | Native Bedrock Messages | Bedrock OpenAI-compatible | Notes |
| --- | --- | --- | --- | --- |
| Core file/git/search/artifact tools | Compatible | Compatible | Compatible | They are ordinary local function tools. |
| CSV/data artifact tools | Compatible | Compatible | Compatible | Output compaction and artifact persistence are provider-agnostic. |
| BigQuery/Databricks/Trino/Snowflake tools | Compatible | Compatible | Compatible | Protocol-compatible; availability is gated by settings and connection health. |
| Query lifecycle tools | Compatible | Compatible | Compatible | Ordinary local function tools. |
| Chart/interactive artifact tools | Compatible | Compatible | Compatible | Tool results are JSON envelopes and artifacts; no provider-specific requirement. |
| Atlassian tools | Compatible | Compatible | Compatible | Ordinary local function tools, gated by Jira/Confluence settings and write approvals. |
| GitHub tools | Compatible | Compatible | Compatible | Ordinary local function tools, gated by GitHub settings and approval model. |
| Local browser CUA (`cua_browser_*`) | Compatible | Compatible | Compatible | These are local Playwright bridge tools, not OpenAI native CUA. |
| Anthropic hosted web search | Implemented | Not implemented | Not applicable | Direct Anthropic sends `web_search_*`; native Bedrock provider does not. |
| Anthropic hosted code execution | Implemented | Not implemented | Not applicable | Anthropic docs state code execution is not currently available on Amazon Bedrock. |
| Anthropic client-schema computer/bash/text editor | Not implemented as native schemas | Not implemented as native schemas | Not applicable | The app has custom equivalents for browser and filesystem, but not the Anthropic-defined schemas. |
| OpenAI Responses web search | Not applicable | Not applicable | Implemented in `openai_compat` | Works only if the endpoint/model supports OpenAI Responses hosted tools. |
| OpenAI Responses code interpreter | Not applicable | Not applicable | Partially implemented | Request shape and container state are handled; parser only stores container IDs from completed output. |
| OpenAI native CUA (`computer`, `computer_call_output`) | Not applicable | Not applicable | Not implemented | Current parser does not handle `computer_call` or `computer_call_output` loop semantics. |

## Detailed Findings

### 1. Direct Anthropic is substantially complete

The direct Anthropic provider is the reference-quality implementation in this repo.

Strengths:

- Correct Messages endpoint construction and version header.
- Custom tool definition translation to `name` / `description` / `input_schema`.
- Hosted web search request support using Anthropic tool types and limits/domain filters.
- Hosted code execution request support with beta header derivation, container reuse, and Skills support.
- Streaming support for text, thinking, signature, JSON tool arguments, compaction deltas, `tool_use`, and `server_tool_use`.
- Provider-executed tool blocks are sanitized and surfaced in UI without being replayed as local tool calls.
- Provider payload replay preserves Anthropic-native assistant content blocks across turns, including tool and provider-executed blocks.
- PDF and text documents are passed as Anthropic `document` blocks; images are passed as Anthropic `image` blocks.
- Invalid/orphaned tool results are filtered before replay, which avoids Anthropic's strict `tool_use`/`tool_result` ordering failures.

Main direct-Anthropic risks:

- Hosted tool versions in frontend defaults and provider catalog are not perfectly aligned. `constants.ts` defaults Anthropic web search to `web_search_20260209`; Go provider fallback is `web_search_20250305`; provider catalog code sandbox default is `code_execution_20260120`; Go fallback is `code_execution_20250825`. This is not necessarily wrong, but it means defaults depend on which config path populates the provider.
- Direct Anthropic hosted code execution is provider-run code. Security, data retention, and cost expectations should be explicit in UI copy if enabled.

### 2. Native Bedrock custom function tools are compatible

Native Bedrock Messages should handle the app's custom local tools well.

Why:

- AWS documents Claude Messages custom tools with `name`, `description`, and `input_schema`.
- `bedrock.Provider.toolsToAnthropic` emits that exact shape.
- `bedrock.Provider` parses streamed `tool_use` blocks and `input_json_delta` into internal `ToolCall` values.
- The engine executes local tools and appends `tool_result` blocks in subsequent user messages.
- Tool result persistence is provider-agnostic. `optionc.Manager.persistToolResult` wraps all results into a stable JSON envelope with artifact IDs, result format, metadata, and compaction where needed.

This covers core app tools, data source tools, chart/artifact tools, Atlassian tools, GitHub tools, and the local browser-control CUA tools.

### 3. Native Bedrock hosted/provider tools are not implemented

The native Bedrock provider currently sends only local custom tools:

```go
func toolsToAnthropic(tools []types.ToolDefinition, mapper *toolnamemapper.Mapper) []any
```

It has no Bedrock equivalent of the direct Anthropic provider's `webSearch` or `codeExecution` options.

This is partially defensible:

- Anthropic's code execution docs currently say code execution is available on the Anthropic API and Azure AI Foundry, not Amazon Bedrock.
- AWS Bedrock tool docs include broader Anthropic tool features such as fine-grained tool streaming and computer use beta, but this app does not implement those native Anthropic-defined client schemas in Bedrock.

The product issue is that the UI and defaults still expose Bedrock `webSearch` and `codeExecution` settings:

- `constants.ts` defaults Bedrock web search to enabled.
- `providers/catalog/providerDefs.ts` exposes Bedrock web search and code sandbox controls.
- `lib/providerConfigRuntime.ts` copies Bedrock `webSearch` and `codeExecution` config into settings.
- `factory.go` never passes those settings into the native Bedrock provider.

Recommendation:

- For native Bedrock `apiType=messages`, hide or disable hosted web search/code sandbox controls unless a supported Bedrock implementation is added.
- Keep local tools visible because they are custom function tools and are compatible.
- If Bedrock native computer use is required, implement it explicitly as Anthropic-schema client tools, not as OpenAI CUA.

### 4. Bedrock compaction request shape is documented, but preservation is incomplete

AWS documents Bedrock Claude Messages compaction using:

- `anthropic_beta: ["compact-2026-01-12"]`
- `context_management.edits[].type: "compact_20260112"`
- optional trigger and instructions
- streaming `compaction` content blocks

The native Bedrock provider sends those fields and parses streaming compaction blocks into `compaction_start`, `compaction_delta`, and `compaction_end` events.

The gap is continuity. AWS's compaction guidance says subsequent requests should append the assistant response, including the `compaction` block, so the API can continue from the summary. Direct Anthropic preserves provider-native assistant blocks through `ProviderPayload`. Native Bedrock does not set `ProviderPayload` on final/tool-call events and `messagesToAnthropic` has no path to replay prior `compaction` blocks.

Impact:

- Users can see compaction progress.
- The server-side compaction summary is not reliably carried forward in future Bedrock requests.
- Long-running Bedrock conversations may fall back to local manager compaction rather than benefiting from Bedrock's native compaction continuity.

Recommendation:

- Add Bedrock provider payload preservation mirroring the direct Anthropic provider's `anthropic_messages_v1` payload, or a Bedrock-specific equivalent.
- Preserve and replay `compaction` blocks in assistant content.
- Add regression tests proving that a Bedrock response containing a `compaction` block is persisted and included in the next provider request.
- Gate compaction by model support. AWS currently lists specific Claude 4.6 models for this compaction beta; defaulting it on for every Bedrock model can cause validation failures on older or unsupported models.

### 5. Native Bedrock document/file attachment support is incomplete

Direct Anthropic supports:

- PDF and text files as `document` content blocks.
- Images as `image` content blocks.

The native Bedrock provider supports:

- User text.
- Images as standard Anthropic image blocks.

It does not include `FileAttachments` in `bedrock.Provider.messagesToAnthropic`. There is Bedrock document/image shaping code in `anthropic.Provider.userContentToBedrockMessages`, but the current factory does not use `anthropic.Provider` for native Bedrock Runtime. That code path is effectively not helping the current Bedrock provider.

Recommendation:

- Decide whether native Bedrock should use Runtime Messages shape or the `bedrock-mantle` Anthropic-compatible shape.
- Add file attachment tests for Bedrock PDFs/text/CSV before implementing.
- If sticking with Runtime Messages, use the request shape AWS supports for Claude Messages multimodal/document inputs and verify against the exact target models.

### 6. Bedrock endpoint strategy needs a product decision

The app currently has three concepts that do not fully align:

1. Native Bedrock `messages` in `factory.go` always normalizes to Runtime `InvokeModelWithResponseStream`.
2. `lib/providerConfigRuntime.ts` previews `messages` as `https://bedrock-mantle.{region}.api.aws/anthropic`.
3. Bedrock `responses` and `chat_completions` in `factory.go` use `openai_compat`, but default the base URL to `https://bedrock-runtime.{region}.amazonaws.com`.

AWS current docs say:

- `bedrock-mantle` supports Responses, Chat Completions, and Anthropic Messages.
- `bedrock-runtime` supports Invoke and Converse.
- Bedrock API keys can authenticate direct Runtime requests with `Authorization: Bearer ...`, so the app's Bearer support itself is valid.

The mismatch is not auth. The mismatch is endpoint family.

Recommendation:

- For `apiType=messages`, either:
  - Keep Runtime as the intentional default and update endpoint previews/copy to say this is the Runtime Invoke path, or
  - Move new Bedrock Messages configs to Mantle and revive/fix the `anthropic.Provider` `BedrockMessagesShape` path.
- For `apiType=responses`, default to `https://bedrock-mantle.{region}.api.aws/v1`, not Runtime.
- For `apiType=chat_completions`, prefer Mantle by default, while allowing advanced users to override to any compatible endpoint.
- Add provider endpoint preview tests covering all three Bedrock API types.

### 7. OpenAI CUA and local CUA are different systems

The local browser integration defines tools named:

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

These are ordinary local function tools backed by the app's browser-control manager. They expose DOM snapshots, indexes, Playwright-style actions, and logs.

OpenAI native CUA is different. OpenAI's current Responses computer-use flow uses a `computer` tool, receives `computer_call` items, executes model-requested actions, captures screenshots, and sends `computer_call_output` items back. The older preview path used `computer_use_preview`; the current docs show the GA `computer` flow while preserving preview examples for older integrations.

Current app status:

- Local `cua_browser_*` tools are compatible with direct Anthropic, native Bedrock Messages, and OpenAI-compatible custom function calling.
- The app does not implement OpenAI native CUA in `openai_compat.Provider`.
- The Responses parser does not handle `computer_call` events.
- The request builder does not send a `computer` tool or implement a screenshot return loop.
- Bedrock Messages cannot consume OpenAI native `computer_call_output` items because those are OpenAI Responses-specific.

Recommendation:

- Rename UI/docs where needed to distinguish "local browser-control tools" from "OpenAI native computer use."
- If native OpenAI CUA is required, implement it only in the OpenAI Responses path first:
  - Add `computer` tool configuration.
  - Parse streamed and final `computer_call` items.
  - Execute actions through the existing browser-control manager.
  - Send `computer_call_output` with screenshots.
  - Preserve safety checks and reasoning items according to OpenAI Responses requirements.
- Do not try to translate OpenAI native CUA directly into Bedrock Messages. For Bedrock, use local browser function tools or implement Anthropic computer-use schema separately.

### 8. Tool result envelopes are provider-compatible but can be heavy

All local tools return through `persistToolResult`, which wraps data as a JSON envelope:

```json
{
  "artifact_id": "...",
  "tool_name": "...",
  "tool_call_id": "...",
  "ok": true,
  "result_format": "json",
  "result": {}
}
```

This is safe for Anthropic and Bedrock because `tool_result.content` is a string. It also works for OpenAI function call outputs.

Risk:

- Some tools have large schemas and large result envelopes.
- Anthropic and Bedrock count tool definitions and tool result blocks as input tokens.
- The app estimates tool overhead and compacts tool results, but the estimates are generic and not model-specific enough for every Bedrock Claude model variant.

Recommendation:

- Keep high-volume tool outputs artifact-backed and compact by default.
- Add Bedrock-specific load tests for a realistic enabled-tool set, not only unit tests for one or two tools.
- Consider smaller Bedrock tool descriptions if users report tool-schema request-size or token-limit failures.

## Provider-Specific Recommendations

### Direct Anthropic

Priority: maintain and test.

- Keep as the reference implementation for Anthropic Messages behavior.
- Add tests for latest default tool versions to make UI/provider defaults explicit.
- Keep provider payload replay tests for `server_tool_use`, web search results, code execution results, and compaction blocks.

### Native Bedrock Messages

Priority: fix capability accuracy and compaction continuity.

- Hide/disable native Bedrock hosted web search/code execution controls until implemented or supported.
- Preserve/replay provider-native content blocks, especially `compaction`.
- Gate compaction by supported model family/version.
- Add file/document attachment support or explicitly mark it unsupported for Bedrock.
- Add a provider test asserting Bedrock does not silently drop file attachments if the UI allows them.

### Bedrock OpenAI-Compatible

Priority: fix endpoint defaults.

- Default Responses/Chat Completions to Mantle-compatible base URLs.
- Make the distinction clear in settings:
  - `messages`: Anthropic-native Bedrock Messages.
  - `responses`: OpenAI-compatible Bedrock Responses.
  - `chat_completions`: OpenAI-compatible Bedrock Chat Completions.
- Do not expose OpenAI Responses hosted tools unless the chosen Bedrock model/API supports them.

### OpenAI-Compatible Provider

Priority: do not overstate native CUA.

- Current OpenAI Responses web search handling is reasonable.
- Code interpreter request support exists, but parser coverage should be expanded if generated files or outputs need first-class display.
- Native OpenAI CUA is not implemented.
- Add tests for unknown hosted tool item types so future OpenAI tool events fail visibly rather than disappearing.

## Suggested Test Plan

### Unit tests

- `bedrock.Provider` request body includes only custom tools for native Messages when Bedrock `webSearch` and `codeExecution` are enabled in settings.
- `bedrock.Provider` preserves and replays `compaction` blocks through provider payload.
- `bedrock.Provider` gates compaction for unsupported model IDs.
- `bedrock.Provider` encodes PDF/text file attachments or rejects them before dispatch with a clear unsupported-feature error.
- `factory.go` defaults Bedrock OpenAI-compatible Responses to Mantle URL.
- `openai_compat.Provider` ignores or errors clearly on `computer_call` until native CUA is implemented.
- `browser_control_tools` definitions remain ordinary function tools across providers.

### Integration tests

- Direct Anthropic: user asks for a tool, model emits `tool_use`, local tool result is returned, final answer completes.
- Native Bedrock Runtime: same tool loop with a real or mocked AWS event-stream.
- Native Bedrock Runtime: compaction response with text after compaction persists and continues on the next turn.
- Bedrock Responses: endpoint URL and request body match Mantle OpenAI-compatible docs.
- OpenAI Responses: hosted web search and code interpreter outputs show provider-executed tool calls/results in UI.

### Manual QA

- Bedrock settings UI: toggling API type changes endpoint preview and available controls accurately.
- Bedrock Messages: local browser `cua_browser_*` tools work as custom tools.
- OpenAI Responses: local browser `cua_browser_*` tools still work as custom function tools.
- Confirm no UI copy implies local browser tools are the OpenAI hosted/native computer-use tool.

## Bottom Line

For Anthropic-direct, the integration is strong.

For Bedrock, the custom-tool loop is compatible and useful, but the product currently presents more capability than the native provider actually implements. The most important fixes are to align Bedrock endpoint defaults with AWS's current API families, remove or implement unsupported hosted-tool controls, and persist Bedrock provider-native compaction blocks so native compaction works across turns rather than only appearing as a streaming event.
