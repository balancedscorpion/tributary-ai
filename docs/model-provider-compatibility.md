# Model Provider Compatibility Review

Date: 2026-07-11
Status: repository and official-documentation snapshot

## Executive summary

The application exposes five provider families, but it has more than five effective inference surfaces:

- OpenAI through the Responses API.
- Anthropic through the native Messages API.
- Gemini through the legacy `GenerateContent` API.
- Amazon Bedrock Claude through `InvokeModelWithResponseStream`, with an automatic switch to `ConverseStream` when a document is attached.
- Amazon Bedrock through OpenAI-compatible Chat Completions or Responses.
- Arbitrary OpenAI-compatible Chat Completions or Responses endpoints, including the Groq, Together AI, Fireworks AI, and OpenRouter presets.

Those surfaces must not share one provider-level capability flag. Capability is a tuple of:

`provider family + endpoint/API surface + model/version + region + repository adapter version`

The most important conclusions are:

1. **Direct OpenAI Responses and direct Anthropic Messages are the most complete integrations.** Both support streaming, the repository's local function tools, images/files, provider-hosted web search, and provider-hosted code execution. Neither exposes the complete native API surface.
2. **Anthropic hosted web search and hosted code execution are not available through Amazon Bedrock.** Local repository tools still work because they are ordinary client-side function calls. “Claude Platform on AWS” is a separate Anthropic service and must not be confused with Amazon Bedrock.
3. **Gemini 3 tool loops are currently protocol-incompatible.** The adapter discards provider function-call IDs and `thoughtSignature`, invents local IDs, and returns a `functionResponse` under role `tool` rather than `user`. Gemini 3 requires exact call IDs and thought signatures during tool continuation; affected runs can fail with a 400.
4. **The configured Gemini default, `gemini-2.0-flash`, was shut down on 2026-06-01.** The UI also advertises GCP ADC, but the runtime only implements a Gemini Developer API key and Developer API URL shape—not standard Vertex access.
5. **Bedrock is split across incompatible paths.** The normal `messages` path is Claude-specific Bedrock Runtime. Document attachments silently move that run to Converse, which changes document, citation, reasoning, and compaction behavior. The OpenAI-compatible Bedrock paths have separate endpoint, auth, and model constraints.
6. **Native Anthropic and Bedrock compaction is not durable in the normal streaming path.** The adapters emit UI events but discard the returned opaque compaction block, so it cannot be replayed on the next request. The capability metadata currently overstates continuity.
7. **A document that takes Anthropic's Files/container path can re-enable hosted code execution after policy/configuration disabled it.** The file preparation path uploads the file, marks it as a container upload, and force-enables the code-execution tool. This is a permission-boundary defect, not merely a compatibility limitation.
8. **“OpenAI-compatible” describes an HTTP shape, not feature parity.** Hosted tools, files, reasoning state, citations, structured output, and even function-call details vary by endpoint and model. The current app performs no capability negotiation.
9. **Structured outputs, strict tool schemas, explicit prompt caching, model discovery, token-count APIs, rich usage accounting, native computer use, audio/video, and first-class safety controls are not implemented across providers.** Some upstream APIs support them, but the repository does not expose them.
10. **Provider and model limits are stale.** The app uses substring heuristics and fixed 8,192-token output limits in Anthropic/Bedrock. The shared 20 MiB marshalled-JSON guard also makes the effective raw inline-file limit roughly 15 MiB after base64 expansion, despite the frontend allowing files up to 128 MiB.

## Scope and methodology

This review follows the active production path:

`Frontend -> HTTP API -> optionc.Manager -> engine.RunAgentLoop -> Go provider adapters -> tool registry -> storage/SSE`

The TypeScript adapters under `providers/adapters/*` are not the active inference runtime. The frontend imports shared streaming utilities, but chat runs are created through the Go factory and Go adapters. This map therefore treats the Go implementation as authoritative and the TypeScript adapters as legacy/parity debt.

The review distinguishes three questions:

1. **Native:** does the upstream API/model support the feature?
2. **Adapter:** does the active Go adapter serialize, parse, preserve, and expose it correctly?
3. **Product:** is it configurable, permissioned, tested, and accurately represented in the UI?

Legend used below:

- ✅ implemented and usable on a supported model.
- ◐ partial, model-dependent, endpoint-dependent, or invisible to the product.
- ⚠ implemented with a material defect or misleading capability claim.
- ❌ unavailable or not implemented.
- — not applicable.

Every upstream `✅` remains model/version dependent unless the cited provider documentation says otherwise.

## Runtime provider inventory

| Product card | Active runtime/API | Auth implemented | Product default | Important routing behavior |
| --- | --- | --- | --- | --- |
| OpenAI | Go `openai_compat`, fixed to Responses | API key | `gpt-4o` | Web search and native compaction default on; code interpreter defaults off. |
| Anthropic | Go `anthropic`, native Messages | API key | `claude-opus-4-6` | Web search and compaction default on; code execution defaults off. |
| Gemini | Go `gemini`, `v1beta ...:streamGenerateContent` | Gemini Developer API key | `gemini-2.0-flash` | GCP ADC is shown in UI but not used by the factory. |
| Bedrock `messages` | Go `bedrock`, Claude body over InvokeModel streaming | Bedrock API key or AWS credentials | `anthropic.claude-opus-4-6-v1` | Falls back to synchronous InvokeModel when streaming is unavailable; any usable document switches the request to ConverseStream. |
| Bedrock `chat_completions` | Go `openai_compat`, Chat Completions | Bedrock API key only | Same free-text model field | Generated base URL lacks required API path; model/API support differs from Messages. |
| Bedrock `responses` | Go `openai_compat`, Responses | Bedrock API key only | Same free-text model field | Responses belongs on Bedrock Mantle `/v1`; current generated base points at Bedrock Runtime root. |
| Compatible `chat_completions` | Go `openai_compat` | API key, custom header, or none | Blank model | Default for all four presets. No capability probing. |
| Compatible `responses` | Go `openai_compat` | API key, custom header, or none | Blank model | Assumes OpenAI Responses event/state semantics. |

Primary routing evidence:

- `providers/catalog/providerDefs.ts:45-225`
- `providers/catalog/presets.ts:12-45`
- `server/internal/chat/providers/factory.go:44-602`
- `server/internal/chat/providers/openai_compat/provider.go`
- `server/internal/chat/providers/anthropic/provider.go`
- `server/internal/chat/providers/gemini/provider.go`
- `server/internal/chat/providers/bedrock/provider.go`

## Repository feature surface

### Provider-independent local tools

The following families are ordinary custom function tools. They do not require a provider-hosted search or code service; they require a model/API that can reliably perform multi-turn custom function calling:

| Tool family | Examples in the repository | Provider requirement |
| --- | --- | --- |
| Files, local grants, Git, attachments | list/read/search files, Git history, attachment inspection | Custom function calling and safe multi-turn tool-result replay. |
| Artifacts, tables, charts, datasets | table display, chart/interactive artifact creation, dataset inspection/aggregation | Custom function calling; artifact persistence is provider independent. |
| Data sources | BigQuery, Databricks, Trino, Snowflake, query lifecycle | Custom function calling; datasource configuration and permissions. |
| Atlassian | Jira and Confluence read/write operations | Custom function calling; integration auth and approval policy. |
| Browser control | local browser status/navigation/snapshot/click/type | Custom function calling. These are local browser functions, not native OpenAI or Anthropic computer-use protocols. |
| SharePoint, GitHub, Slack, arXiv | connected-system read/write operations | Custom function calling; connector auth and approval policy. |
| Search and RLM | thread/document/artifact/message search and RLM execution | Custom function calling; local services available. |

The registry is defined in `server/internal/chat/tools/registry.go:29-288`. The engine accepts multiple tool calls but executes them serially at `server/internal/chat/engine/engine.go:500-531`.

### Provider-hosted tools

Only four hosted capabilities are cataloged by the product:

- OpenAI Responses web search.
- OpenAI Responses code interpreter.
- Anthropic Messages web search.
- Anthropic Messages code execution.

See `server/internal/chat/tools/provider_hosted_tool_metadata.go:28-104`. Gemini Search/code execution, Anthropic web fetch, Bedrock server-side tools, Groq built-ins, and OpenRouter server tools are absent from the hosted-tool catalog.

## Upstream API capability map

This table describes native upstream availability, not what the application currently exposes.

| API surface | Custom tools | Images/files | Hosted web | Hosted code | Thinking/reasoning | Structured output | Prompt caching | Native state/compaction | Citations |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| OpenAI Responses | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ automatic/explicit, model dependent | ✅ `previous_response_id` and compaction | ✅ tool annotations |
| Anthropic Messages, direct | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ explicit | ✅ selected models | ✅ |
| Gemini GenerateContent | ✅ | ✅ | ✅ Google Search/URL context | ✅ | ✅ | ✅ | ✅ implicit/explicit | ❌ compaction; API is stateless | ✅ grounding metadata |
| Gemini Interactions | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ◐ | ✅ stateful interactions | ✅ |
| Bedrock Claude InvokeModel | ✅ | ✅, model dependent | ❌ Anthropic hosted search | ❌ Anthropic hosted sandbox | ✅ selected models | ✅ Bedrock Runtime | ✅ cache points | ✅ compaction on selected models | ◐ model/API dependent |
| Bedrock Converse | ✅ | ✅ | ❌ Anthropic hosted search | ❌ Anthropic hosted sandbox | ✅ selected models | ✅ | ✅ cache points | ❌ compaction | ✅ document citations |
| Bedrock Mantle Responses | ✅, model dependent | ◐ model dependent | ◐ Bedrock Responses built-ins | ◐ Bedrock Responses built-ins | ◐ model dependent | ◐ model dependent | ◐ model dependent | ✅ stored Responses state | ◐ tool/model dependent |

Official references:

- OpenAI: [models](https://developers.openai.com/api/docs/models), [function calling](https://developers.openai.com/api/docs/guides/function-calling), [web search](https://developers.openai.com/api/docs/guides/tools-web-search), [code interpreter](https://developers.openai.com/api/docs/guides/tools-code-interpreter), [file inputs](https://developers.openai.com/api/docs/guides/file-inputs), [compaction](https://developers.openai.com/api/docs/guides/compaction), [structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs), and [prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching).
- Anthropic: [tool use](https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview), [web search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool), [code execution](https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool), [extended thinking](https://platform.claude.com/docs/en/build-with-claude/extended-thinking), [compaction](https://platform.claude.com/docs/en/build-with-claude/compaction), [structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs), [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), and [citations](https://platform.claude.com/docs/en/build-with-claude/citations).
- Gemini: [GenerateContent reference](https://ai.google.dev/api/generate-content), [function calling](https://ai.google.dev/gemini-api/docs/generate-content/function-calling), [thought signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures), [Google Search](https://ai.google.dev/gemini-api/docs/generate-content/google-search), [code execution](https://ai.google.dev/gemini-api/docs/generate-content/code-execution), [structured output](https://ai.google.dev/gemini-api/docs/generate-content/structured-output), [caching](https://ai.google.dev/gemini-api/docs/generate-content/caching), and [Interactions](https://ai.google.dev/gemini-api/docs/interactions-overview).
- Bedrock: [API compatibility by model](https://docs.aws.amazon.com/bedrock/latest/userguide/models-api-compatibility.html), [endpoint comparison](https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html), [tool use](https://docs.aws.amazon.com/bedrock/latest/userguide/tool-use.html), [Responses](https://docs.aws.amazon.com/bedrock/latest/userguide/bedrock-mantle.html), [Claude compaction](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-compaction.html), [thinking](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-extended-thinking.html), [structured output](https://docs.aws.amazon.com/bedrock/latest/userguide/structured-output.html), and [prompt caching](https://docs.aws.amazon.com/bedrock/latest/userguide/prompt-caching.html).

## What works in this repository

### Core interaction and local tools

| Active runtime profile | Text streaming | System prompt | First custom-tool call | Multi-step local-tool loop | Multiple calls in one turn | Thinking UI | Usage/request ID |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| OpenAI Responses | ✅ | ✅ | ✅ | ✅ | ◐ accepted, executed serially | ❌ summaries/reasoning items not parsed | ◐ aggregate tokens + request ID |
| Anthropic Messages | ✅ | ✅ | ✅ | ✅ | ◐ accepted, executed serially | ◐ parsed if provider emits it; no request control | ◐ aggregate tokens + request ID |
| Gemini GenerateContent | ✅ | ✅ | ✅ | ⚠ broken for Gemini 3 | ⚠ IDs/signatures/order not preserved | ❌ | ◐ three aggregate token counts; no request ID |
| Bedrock Claude Invoke | ✅ | ✅ | ✅ | ✅ for basic non-thinking turns | ◐ accepted, executed serially | ⚠ text parsed; signature/state discarded | ◐ aggregate tokens + AWS request ID |
| Bedrock Converse | ✅ | ✅ | ✅ | ✅ for basic non-thinking turns | ◐ accepted, executed serially | ⚠ reasoning text parsed; state discarded | ◐ aggregate tokens + AWS request ID |
| Bedrock Chat/Responses | ⚠ unusable with generated defaults | ◐ | ◐ model/API dependent | ◐ after endpoint correction | ◐ serial execution | ❌ | ◐ |
| Compatible Chat Completions | ✅ if transport is compatible | ✅ | ◐ endpoint/model dependent | ◐ vendor opaque state can be lost | ◐ serial execution | ❌ vendor reasoning fields dropped | ◐ when provider streams usage |
| Compatible Responses | ✅ if Responses-compatible | ✅ | ◐ endpoint/model dependent | ◐ OpenAI state semantics assumed | ◐ serial execution | ❌ | ◐ OpenAI-shaped totals/state only |

### Multimodal input, hosted tools, and citations

| Active runtime profile | Images | PDFs/documents/files | Provider-hosted web | Provider-hosted code | User-visible structured citations |
| --- | ---: | ---: | ---: | ---: | ---: |
| OpenAI Responses | ✅ | ✅ broad native file path with text-sidecar retry | ✅ | ✅ | ◐ URLs/titles/snippets become provider-tool results; offsets are lost |
| Anthropic Messages | ✅ standard file and legacy image paths | ◐ PDF/text inline; other files use Files API + code container | ✅ | ✅ | ❌ citation deltas/blocks are not surfaced |
| Gemini GenerateContent | ⚠ legacy image path works; standard uploaded-image path can be omitted | ◐ inline PDF/text/code-like MIME types; 20 MiB JSON guard | ❌ despite native Google Search | ❌ despite native code execution | ❌ grounding metadata ignored |
| Bedrock Claude Invoke | ◐ legacy image path; normal uploaded-image path is not native | — usable documents select Converse | ❌ | ❌ | ❌ |
| Bedrock Converse | ◐ legacy image path | ✅ constrained native documents plus extracted-text fallback | ❌ | ❌ | ⚠ PDF citations requested but citation events ignored |
| Bedrock Chat Completions | ◐ endpoint/model dependent | ⚠ nonstandard Chat `file` part | ❌ settings are normalized away | ❌ settings are normalized away | ❌ |
| Bedrock Responses | ◐ endpoint/model dependent | ◐ OpenAI-shaped input only | ❌ product settings normalized away | ❌ product settings normalized away | ◐ only OpenAI-shaped annotations if manually reached |
| Compatible Chat Completions | ◐ `image_url`, model dependent | ⚠ optimistic nonstandard `file` part; no fallback | ❌ Responses-only product integration | ❌ Responses-only product integration | ❌ annotations ignored |
| Compatible Responses | ◐ endpoint/model dependent | ◐ OpenAI shape with rejection fallback | ◐ only exact OpenAI-style support | ◐ only exact OpenAI-style support | ◐ OpenAI-shaped annotations only |

### State, controls, and operational features

| Active runtime profile | Native compaction | Opaque provider continuity | Reasoning control | Structured output/strict tools | Prompt caching | Safety/guardrails | Detailed usage/cost |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| OpenAI Responses | ✅ | ✅ stored response ID + container ID | ◐ adapter supports effort, direct profile does not configure it | ❌ | ◐ automatic upstream cache may work; app does not control/report it | ❌ first-class controls | ❌ cached/reasoning/tool detail dropped |
| Anthropic Messages | ⚠ request/events work; compaction block dropped | ✅ normal blocks/thinking/container; ⚠ not compaction | ❌ no `thinking` request | ❌ | ❌ no `cache_control` | — no repository control surface | ❌ cache/server-tool/iteration detail dropped |
| Gemini GenerateContent | ❌ | ❌ IDs/signatures/parts not preserved | ❌ | ❌ | ◐ implicit caching may occur; unobservable | ❌ request settings and feedback handling absent | ❌ cache/thought/tool/modality detail dropped |
| Bedrock Claude Invoke | ⚠ request/events, no replay | ❌ | ❌ | ❌ | ❌ no cache points | ◐ raw headers only, not a product capability | ❌ compaction iterations/cache detail dropped |
| Bedrock Converse | ❌ silently bypassed for document turns | ❌ | ❌ | ❌ | ❌ no cache points | ◐ raw headers only | ❌ |
| Compatible Chat Completions | ❌ | ❌ | ◐ static `reasoning_effort`; output state dropped | ◐ payload override only, no schema contract | ❌ product support | ◐ raw payload/header escape hatch | ❌ vendor details dropped |
| Compatible Responses | ◐ exact OpenAI semantics required | ◐ `previous_response_id` assumed | ◐ static effort; reasoning output ignored | ◐ payload override only | ❌ product support | ◐ raw payload/header escape hatch | ❌ vendor details dropped |

## Provider-specific findings

### OpenAI Responses

Implemented well:

- Streaming Responses text and custom function calls.
- Provider-hosted `web_search` and `code_interpreter` requests and progress/result cards.
- Images and broad `input_file` handling, with an extracted-text retry when native file input is rejected.
- `previous_response_id`, code-interpreter container state, and native context-management compaction.
- Extraction of web-search URL annotations into provider-hosted tool result envelopes.

Important gaps:

- The direct OpenAI card cannot configure reasoning effort even though the shared adapter supports it for compatible profiles.
- Reasoning summaries/items are not parsed or displayed. Stateless encrypted reasoning (`include: reasoning.encrypted_content`) is not implemented.
- Structured output (`text.format`/JSON schema), strict tool schemas, native computer use, file search, MCP/connectors, shell, image generation, and background mode are not implemented.
- Usage only keeps input/output/total tokens. Cached input and reasoning-token details are discarded.
- The default native compaction threshold is 200k while the configured default model `gpt-4o` has a 128k context window. Compaction therefore cannot protect that default before provider overflow.
- Web search also defaults on for `gpt-4o`, but the current Responses web-search documentation lists newer supported model families rather than `gpt-4o`. The product performs no model gate or feature probe, so this default combination must be treated as unverified and can fail at request time.
- The Responses state path relies on provider-side storage and does not expose `store:false` behavior. This matters on Bedrock Mantle, where stored Responses are retained for 30 days by default.

Key code:

- `server/internal/chat/providers/factory.go:67-119`
- `server/internal/chat/providers/openai_compat/provider.go:615-790`
- `server/internal/chat/providers/openai_compat/provider.go:1041-1177`
- `server/internal/chat/providers/openai_compat/provider.go:1635-1699`
- `server/internal/chat/providers/openai_compat/provider.go:1797-2081`

### Direct Anthropic Messages

Implemented well:

- Streaming Messages, custom tools, hosted web search, hosted code execution, `pause_turn`, code containers, Skills, images, documents, and Files API uploads.
- Thinking and signature deltas are preserved in normal assistant provider payloads.
- Provider-executed tool blocks/results are separated from client-side calls and rendered as hosted-tool results.

Compatibility and correctness gaps:

- `web_search_20260209` and later can default to programmatic calling. The integration now forwards configured `allowed_callers` and defaults standalone web search to `["direct"]`; programmatic calling still requires explicitly enabling the code-execution capability.
- The adapter permits both allowed and blocked domain arrays even though Anthropic defines those filters as mutually exclusive.
- Anthropic web fetch is not implemented.
- No `thinking`/adaptive-thinking request configuration is sent. Thinking display is passive only.
- Citation deltas and citation blocks are not mapped to a first-class output, so hosted-search/document attribution is lost.
- Native compaction is model-gated but defaults on without model validation. In streaming mode, the compaction block is not stored in `ProviderPayload`, so subsequent requests cannot continue from it.
- Compaction usage undercounts billing because `usage.iterations` is ignored.
- Output is hardcoded to 8,192 tokens.
- No prompt-cache controls, structured output, strict tool schemas, token-count endpoint, Message Batches, or rich usage accounting.

Permission defect:

- For DOCX/XLSX/PPTX and other files that require an Anthropic container, `prepareNativeFileInputs` sets `NeedsCodeExecution`; `StreamChat` then calls `ensureAnthropicCodeExecutionEnabled`. This can add the hosted code-execution tool even when configuration or the hosted-tool permission policy disabled it. The file pipeline must never broaden provider-hosted execution permission implicitly.

Key code:

- `server/internal/chat/providers/anthropic/provider.go:86-123`
- `server/internal/chat/providers/anthropic/provider.go:1431-1601`
- `server/internal/chat/providers/anthropic/provider.go:1735-1844`
- `server/internal/chat/providers/anthropic/provider.go:2047-2297`
- `server/internal/chat/providers/anthropic/provider.go:2314-2359`
- `server/internal/chat/providers/factory.go:238-303,811-869`

### Gemini Developer API

Native Gemini has a much broader surface than the adapter. Current Gemini supports Google Search grounding, URL context, code execution, structured outputs, thinking controls/summaries, rich grounding/citation metadata, explicit/implicit caching, and detailed safety/usage metadata. The app only implements streaming text, the first leg of custom function calling, system instructions, inline media/documents, and three token totals.

Blocking custom-tool defects:

1. A Gemini `functionResponse` must be returned in a `user` content turn; the adapter emits role `tool`.
2. Gemini 3 returns a unique function-call `id`, which must be echoed in the matching response. The adapter discards it and invents a UUID.
3. Gemini 3 requires exact `thoughtSignature` preservation during function-call continuation. The parser drops it.
4. The adapter reconstructs only text and function calls rather than replaying all original parts in order.

These defects make Gemini 3 unsafe for the repository's core multi-step local-tool workflow.

Other gaps:

- Google Search, URL context, code execution, code/file outputs, grounding metadata, Search suggestion UI, safety feedback, finish reasons, detailed usage, and structured output are ignored.
- The standard first-party upload path stores images as `FileAttachment`. `geminiFileParts` accepts documents/text but rejects image MIME types, while the manager suppresses fallback preview because it assumes native Gemini image support. An uploaded image can therefore be silently omitted. Existing tests cover the legacy `ImageAttachments` path, not this product path.
- The request is capped at 20 MiB after JSON/base64 encoding, leaving roughly 15 MiB for raw inline bytes before prompt overhead. This is lower than the current Gemini PDF/file service limits.
- The UI offers GCP ADC, but the factory always requires a Gemini API key, sends `x-goog-api-key`, and constructs the Developer API URL. A custom base URL cannot turn this into standard Vertex because the Vertex path and OAuth/IAM scheme differ.
- The adapter is hardcoded to legacy `GenerateContent`; Google now recommends the GA Interactions API for the latest stateful capabilities.
- The UI default `gemini-2.0-flash` is shut down. Current stable model cards and lifecycle must be discovered rather than embedded as timeless strings.
- The current stable Gemini 3.5 Flash and 3.1 Flash-Lite cards list 1,048,576 input and 65,536 output tokens; the repository reports 1,000,000 and 8,192.

Key code:

- `server/internal/chat/providers/gemini/provider.go:93-130`
- `server/internal/chat/providers/gemini/provider.go:169-260`
- `server/internal/chat/providers/gemini/provider.go:303-427`
- `server/internal/chat/providers/factory.go:305-321`
- `server/internal/chat/providers/capabilities/capabilities.go:120-129`
- `providers/catalog/providerDefs.ts:151-176`

Official lifecycle and protocol references:

- [Gemini model catalog](https://ai.google.dev/gemini-api/docs/models)
- [Gemini deprecations](https://ai.google.dev/gemini-api/docs/deprecations)
- [Function calling](https://ai.google.dev/gemini-api/docs/generate-content/function-calling)
- [Thought signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures)
- [Vertex quickstart/auth](https://cloud.google.com/vertex-ai/generative-ai/docs/start/quickstart)

### Amazon Bedrock

#### Bedrock Claude InvokeModel path

This is the default `apiType=messages` runtime when no document is present. It sends an Anthropic Messages-shaped body to:

`/model/{model}/invoke-with-response-stream`

Working features:

- Text streaming, system prompt, local custom tools, basic multi-step tool loops, legacy inline images, token totals, and AWS request IDs.
- Automatic synchronous `InvokeModel` fallback when the model does not support response streaming or the credentials only allow the non-streaming action. The synchronous response is converted into the same internal text/tool/final event contract.
- Claude compaction request/events on models that support the beta.
- Passive parsing of thinking text.

Missing or unsafe features:

- Anthropic hosted web search and hosted code execution are unavailable on Amazon Bedrock and are correctly absent from this adapter.
- Thinking signatures/redacted thinking are not preserved, so thinking-plus-tool continuation is unsafe.
- No provider payload/state is returned. Compaction blocks and other opaque Claude blocks are not replayed.
- Compaction defaults on without model validation and usage iterations are ignored.
- Prompt-cache points, structured output, strict tools, tool choice, Bedrock token counting, and guardrail configuration are not first-class.
- Output is fixed at 8,192 tokens.

#### Automatic Converse path for documents

If any supported document or extracted fallback text exists, the adapter changes the entire request to:

`/model/{model}/converse-stream`

This path supports custom tools, images, and up to five document blocks capped by the adapter at 4.5 MiB each. Native formats include PDF, TXT, Markdown, HTML, CSV, DOC/DOCX, and XLS/XLSX; other office/presentation content can fall back to extracted text.

The switch has important side effects:

- AWS documents Claude compaction as InvokeModel-only; the Converse request omits it even when settings/capability metadata say it is enabled.
- PDFs set `citations.enabled=true`, which is needed for full visual PDF understanding, but the parser ignores citation content/deltas and source locations.
- Reasoning text is parsed, but opaque signatures/state are not preserved.
- The same conversation can therefore change capability semantics simply because a document was attached.

#### Bedrock OpenAI-compatible paths

These are separate APIs, not alternate Claude Messages encodings.

- Bedrock Runtime Chat Completions uses a base such as `https://bedrock-runtime.{region}.amazonaws.com/v1` and supports only compatible models. AWS documents both API-key and SigV4 HTTP auth; the app requires an API key.
- Bedrock Responses is documented on `https://bedrock-mantle.{region}.api.aws/v1`, with model-specific availability and provider-side stored state. The app defaults to `https://bedrock-runtime.{region}.amazonaws.com` and then appends `/responses`, so its generated route is invalid.
- The generated Chat route also misses `/v1`.
- Bedrock settings normalization clears hosted web/code configuration for every Bedrock API type. Even where a Bedrock Responses model supports built-in tools, the product cannot enable them through the normal provider card.
- New Bedrock connections default to `anthropic.claude-opus-4-6-v1`, and settings migration upgrades the former direct-Anthropic-style `claude-opus-4-6` default. Explicit model IDs and inference-profile identifiers remain unchanged.
- `anthropic_version` is freely editable even though the Runtime Claude request requires `bedrock-2023-05-31`.

Key code:

- `server/internal/chat/providers/factory.go:322-486,562-602`
- `server/internal/chat/providers/bedrock/provider.go:261-318`
- `server/internal/chat/providers/bedrock/provider.go:634-908`
- `server/internal/chat/providers/bedrock/provider.go:1206-1467`
- `server/internal/config/settings.go:2072-2097`

Official Bedrock references:

- [Endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html)
- [Chat Completions endpoints](https://docs.aws.amazon.com/bedrock/latest/userguide/inference-chat-completions-mantle.html)
- [Responses/Mantle](https://docs.aws.amazon.com/bedrock/latest/userguide/bedrock-mantle.html)
- [Claude compaction](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-compaction.html)
- [Claude extended thinking](https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-extended-thinking.html)
- [Document blocks](https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_DocumentBlock.html)
- [Claude Opus 4.6 model card](https://docs.aws.amazon.com/bedrock/latest/userguide/model-card-anthropic-claude-opus-4-6.html)

#### Anthropic on AWS terminology

The following are distinct:

- **Amazon Bedrock:** AWS model hosting and inference APIs reviewed above.
- **Claude Platform on AWS:** Anthropic's first-party Claude API service hosted on AWS.

Claude Platform on AWS can have Anthropic API feature parity that Amazon Bedrock does not. Do not infer Bedrock web search/code support from the [Claude Platform on AWS feature table](https://docs.aws.amazon.com/claude-platform/latest/userguide/feature-support.html).

### OpenAI-compatible profiles and presets

The generic adapter supports useful transport customization: Chat or Responses, arbitrary base URL, query parameters, custom headers/auth, model placement, endpoint suffixes, payload overrides, timeout, reasoning effort, and file-input policy. That flexibility is not capability detection.

Risks common to compatible profiles:

- New profiles default to `native_supported` file input even when the endpoint has never been verified.
- Chat sends a nonstandard `type: "file"` content part for arbitrary files and has no rejection fallback. Responses has a fallback.
- Chat parsing keeps only string text deltas, canonical OpenAI tool calls, and basic usage. It drops vendor reasoning state, array content, annotations/citations, safety metadata, and finish details.
- Responses parsing assumes OpenAI event names, built-in tool types, `previous_response_id`, compaction, and annotation shapes.
- Payload overrides can request vendor fields, but cannot teach the parser to preserve the vendor's output/state protocol.
- No profile-level feature test exists. The current connection test sends a simple “hi”; it does not verify tools, files, hosted search, citations, reasoning, or state continuation.

#### Preset map

All four presets use Chat Completions. Consequently, the repository's Responses-only hosted web/code/compaction integrations are off for every preset.

| Preset | Native provider capabilities | What the current preset safely provides | Important missing integration |
| --- | --- | --- | --- |
| Groq | Chat streaming; tool use and structured output on supported models; Groq server-side web/code built-ins | Text and custom tools on a verified tool-capable model | Groq built-ins use provider-specific `allowed_tools`/result metadata and are not wired or parsed. |
| Together AI | Chat streaming, vision, function calling, and structured output on supported models; separate code interpreter service | Text, `image_url`, and custom tools on supported models | Together code interpreter is a separate API; structured output/reasoning state are not product features. |
| Fireworks AI | Chat streaming, model-dependent tools/vision/structured output | Text, images, and custom tools on verified models | Native PDF input is not established by the Chat preset; Fireworks documents converting PDF pages to images. |
| OpenRouter | Model/provider-dependent tools, vision, reasoning, structured output; standardized `openrouter:web_search` server tool | Text and custom tools when both selected model and routed provider support them | The current preset does not add `openrouter:web_search`; Chat annotations/reasoning are ignored. |

Official preset references:

- Groq: [OpenAI compatibility](https://console.groq.com/docs/openai), [local tool use](https://console.groq.com/docs/tool-use/local-tool-calling), [built-in tools](https://console.groq.com/docs/tool-use/built-in-tools), and [structured outputs](https://console.groq.com/docs/structured-outputs).
- Together AI: [OpenAI compatibility](https://docs.together.ai/docs/inference/openai-compatibility), [function calling](https://docs.together.ai/docs/inference/function-calling/overview), [structured outputs](https://docs.together.ai/docs/inference/chat/structured-outputs), and [code interpreter](https://docs.together.ai/docs/together-code-interpreter).
- Fireworks AI: [OpenAI compatibility](https://docs.fireworks.ai/tools-sdks/openai-compatibility), [tool calling](https://docs.fireworks.ai/guides/function-calling), [structured outputs](https://docs.fireworks.ai/structured-responses/structured-response-formatting), and [vision/PDF guidance](https://docs.fireworks.ai/guides/querying-vision-language-models).
- OpenRouter: [tool calling](https://openrouter.ai/docs/guides/features/tool-calling), [web-search server tool](https://openrouter.ai/docs/guides/features/server-tools/web-search), [parameters](https://openrouter.ai/docs/api/reference/parameters), and [model discovery](https://openrouter.ai/docs/guides/overview/models).

## Cross-cutting defects and omissions

### P0: permission and core-loop correctness

1. **Anthropic file upload can bypass hosted-code permission/configuration.** Never force-enable provider execution as a side effect of attaching a file. If a file requires code execution, preflight it and return a clear permission/capability error.
2. **Gemini 3 custom tool continuation is broken.** Preserve complete provider parts, function IDs, thought signatures, and response ordering; use role `user` for `functionResponse`.
3. **The Gemini default model is unavailable.** Replace the static default and add live model discovery/lifecycle handling.

### P1: misleading capability and state

4. **Anthropic compaction claims opaque preservation but drops the streamed block.** Persist/replay the block and add a continuity contract test.
5. **Bedrock compaction/reasoning state is not replayed.** Do not advertise safe native continuation until provider payload preservation exists.
6. **Bedrock document attachment changes API and disables compaction without surfacing the change.** Make the API choice explicit or derive a capability result per request.
7. **Bedrock OpenAI-compatible endpoints/default model are incorrect.** Generate API-specific bases, support appropriate auth, and validate model/API compatibility.
8. **Gemini GCP ADC is a UI-only option.** Either implement Vertex path/auth properly or remove it from the card.
9. **Standard image upload can be omitted on Gemini and is not consistently native on Bedrock.** Test the real `FileAttachment` product path, not only legacy `ImageAttachments` fixtures.
10. **Citations are not a cross-provider output type.** OpenAI flattens sources, while Anthropic, Gemini, and Bedrock drop structured citation/grounding data.

### P2: completeness and efficiency

11. No first-class structured output or strict tool schema support.
12. No explicit prompt caching/cache-point management or cache accounting.
13. No provider-native token-count call; local planning uses stale substring heuristics.
14. Anthropic/Bedrock output is fixed at 8,192, while current models can support much more.
15. Parallel calls are executed serially.
16. Usage is not accumulated across all provider dispatches in a tool loop and omits cached, thinking, hosted-tool, and compaction-iteration details.
17. Provider-generated files from hosted code are not promoted into managed downloadable artifacts.
18. The 128 MiB frontend upload allowance conflicts with the 20 MiB provider JSON body limit for inline paths.
19. No model capability discovery or capability-specific connection test.
20. No first-class native computer use, MCP, audio/video, image generation, logprobs, moderation, or safety-settings UI.

## Recommended compatibility architecture

### 1. Replace provider booleans with versioned capability profiles

Key each profile by:

```text
provider_id
api_surface
endpoint_family
model_id_or_pattern
region
adapter_version
last_verified_at
```

Track each feature in three dimensions:

```json
{
  "custom_function_tools": {
    "native": true,
    "adapter": "supported",
    "product": "enabled",
    "constraints": ["tool results execute serially"],
    "last_verified_at": "2026-07-11",
    "sources": ["official-documentation-url"]
  }
}
```

Minimum capability fields:

- Text streaming and non-streaming.
- System/developer instructions.
- Custom tools, tool choice, strict schemas, parallel calls, and opaque tool state.
- Images by MIME and request limit.
- Documents by MIME, native visual/layout fidelity, count, and size.
- Hosted web search, code execution, computer use, and provider-generated files.
- Thinking/reasoning request mode, visible summary, signature/state preservation.
- Structured output/JSON schema.
- Prompt caching and cache usage reporting.
- Native compaction/state chaining and retention policy.
- Citations/grounding metadata.
- Token-count API, context/output limits, rich usage, and cost fields.
- Safety/guardrails.

### 2. Expose effective capabilities from the backend

Add a backend capability endpoint based on the active provider configuration and selected model. UI controls should consume this result rather than infer support from provider ID or `apiType`.

The existing `ModelCapabilities` type only covers token limits, compaction, and state, and has no meaningful production consumer. Expand or replace it; do not keep a second disconnected matrix.

### 3. Discover models and lifecycle dynamically where possible

- OpenAI and Gemini publish model-list APIs/metadata.
- Bedrock exposes model/API/endpoint compatibility and region-specific availability.
- OpenRouter exposes supported parameters per model/provider route.
- Other compatible providers publish model catalogs, but fields differ.

Discovery will not replace maintained feature rules—most model APIs do not expose every tool detail—but it can prevent dead defaults and invalid model/API combinations.

### 4. Add capability-specific preflight tests

A successful “hi” request proves only text transport. Add opt-in probes for:

1. Text SSE and terminal usage.
2. A two-step custom function loop with multiple calls.
3. An image through the real first-party upload path.
4. A small PDF/document.
5. Hosted web search with source extraction.
6. Hosted code execution and generated-file promotion.
7. Thinking/signature continuation through a tool call.
8. Structured JSON output.
9. Compaction block replay on the following request.
10. Citation/grounding rendering.
11. Cache hit accounting.

Record `verified`, `unsupported`, or `unknown` per exact endpoint/model rather than treating a 400 as generic provider failure.

### 5. Make fallbacks explicit and permission safe

- A native-file rejection may fall back to extracted text only when the user/product accepts reduced fidelity.
- A fallback must never enable hosted code, web access, or another billable/external capability.
- Surface when Bedrock changes from Invoke to Converse.
- Reject unsupported feature combinations before dispatch with a precise message.

## Suggested implementation order

1. Close the Anthropic file/code permission bypass.
2. Fix Gemini function-response role, IDs, thought signatures, full-part replay, image upload path, and default model.
3. Correct Bedrock endpoint/model/auth generation and split Bedrock capability profiles by API.
4. Preserve Anthropic and Bedrock compaction/thinking/citation blocks; correct capability claims and tests.
5. Introduce the backend effective-capabilities contract and use it to gate provider settings.
6. Add model discovery and replace static context/output heuristics.
7. Add first-class citation/grounding output and rich usage accounting.
8. Add structured outputs, strict tools, prompt caching, token counting, and provider-generated file promotion.
9. Add vendor-specific compatible profiles only when request and response adapters exist; otherwise label them transport-only.

## Verification performed

- Traced every provider card through the Go factory and active adapter.
- Audited provider request construction, streaming parsers, file paths, state/payload preservation, tool execution, hosted-tool catalog, settings defaults, and token-limit heuristics.
- Compared current behavior with official OpenAI, Anthropic, Google/Vertex, AWS Bedrock, Groq, Together AI, Fireworks AI, and OpenRouter documentation.
- Ran `go test ./internal/chat/...`; all packages passed.

The passing suite does not cover the structural defects above. In particular:

- Anthropic compaction tests assert UI event order, not provider-payload replay.
- Gemini tool tests cover the first returned call, not a valid Gemini 3 continuation.
- Gemini image tests cover legacy `ImageAttachments`, not the standard `FileAttachment` upload path.
- Capability tests assert metadata that production does not use.

## Maintenance rule

This document is a dated snapshot. Provider/model support changes too quickly for an undated matrix. Any product capability profile should store its source URLs and `last_verified_at`, and CI should flag stale verification dates rather than silently treating old claims as current.
