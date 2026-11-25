Executive summary
Streaming LLM output over Server‑Sent Events (SSE) is deceptively hard to render well when the content mixes Markdown, code blocks (e.g., SQL), and ad‑hoc colour/formatting hints—especially when chunk boundaries and language labels are unreliable. The best-performing architecture in practice is a layered, streaming-first pipeline: (1) a spec-aware SSE reader that enforces size limits and yields clean “content deltas”, (2) a small streaming lexer/state machine that detects fenced code blocks robustly across chunk boundaries and provides a conservative fallback path when fences are missing, and (3) a rendering/highlighting stage that can operate incrementally (ANSI or HTML) with configurable reparse windows and strict sanitisation. This separates correctness concerns (SSE parsing, Markdown fence semantics) from UX concerns (latency, incremental highlight quality) and security concerns (XSS, terminal escape injection, denial-of-service limits). 

For code-block detection, a “fence-first” strategy is the most robust baseline: implement CommonMark-aligned recognition of fenced code blocks (backticks or tildes, ≥3, closing fence length ≥ opening, close at end-of-document if missing) and keep parser state across SSE events. CommonMark explicitly allows unclosed fences to run to end-of-document, and fenced blocks can interrupt paragraphs, which means you should not require blank lines to transition into/out of code mode. 

When fences are absent or malformed (common in LLM output), add a second-stage heuristic classifier that operates on “line-ish” units and uses conservative confidence thresholds: treat content as Markdown by default, and only flip to “implicit code mode” when evidence strongly suggests code (high punctuation density, consistent indentation, keyword patterns, SQL-like structure). For language identification, prioritise explicit info strings after fences, then fall back to content-based detectors such as go-enry (ported from GitHub Linguist) or highlighter “auto-detect” analysis (e.g., Chroma’s best-effort selection in its quick API). 

For incremental highlighting, you have two practical tiers: (A) “highlight on closure” using a fast pure-Go highlighter like Chroma, which is simplest and works well when you can tolerate code blocks being unhighlighted until the closing fence; and (B) “true incremental” highlighting using incremental parsers like tree-sitter with query-based highlighting rules, which is more complex but can keep stable highlighting as text is appended. Tree-sitter is designed to efficiently update syntax trees as source is edited, and its highlighting system is query-based (highlights.scm, etc.). 

Security must be treated as a first-class requirement: CommonMark permits raw HTML that is rendered without escaping, so any HTML output must be sanitised (e.g., with bluemonday allowlists). Terminal/ANSI output must actively neutralise control sequences (CWE-150) to prevent terminal escape injection (e.g., content that clears screen, rewrites titles, spoofs prompts). You should also enforce strict payload and buffer limits at the SSE layer to mitigate memory pressure and denial-of-service. 

Problem framing and stream realities
SSE is a text-based streaming protocol where events are separated by blank lines; fields are data:, event:, id:, retry:, and lines beginning with : are comments. More importantly for implementers, the WHATWG processing model specifies that events are dispatched on blank lines, data: lines are concatenated with \n, field names are compared literally (case-sensitive), and if the stream ends mid-event (no final blank line) pending data is discarded (i.e., the final incomplete event is not dispatched). 

In LLM streaming APIs, you typically receive small “content delta” strings inside SSE events (often JSON within data:), but the semantic boundaries you care about (Markdown blocks, fences, or language labels) seldom align with SSE boundaries. This creates four recurring pathologies:

Fence fragmentation: ``` can be split across SSE events (e.g., "`" then "``sql").
Malformed Markdown: missing closing fences, mismatched fence characters, or extra backticks inside code. CommonMark explicitly defines unclosed fenced blocks as extending to the end of the (containing) document. 
Mixed or interleaved languages: narrative Markdown plus embedded SQL plus pseudo-code, sometimes without fences.
Inline “hints” as hostile input: colour/style codes, raw HTML tags, or terminal escape sequences are both formatting mechanisms and potential injection vectors (HTML/XSS, terminal spoofing). 
A useful mental model is that you are not “parsing Markdown” in the classic offline sense; you are running a streaming classifier that produces rendering segments under uncertainty, continuously, while maintaining low latency and bounded memory.

Streaming-safe segmentation and buffering architecture in Go
A robust Go design separates concerns into three layers:

SSE reader: yields a sequence of logical SSE events (or error/EOF) with enforced size limits.
Content segmenter: consumes a bytestream of deltas and emits a stream of typed segments (MarkdownText, CodeBlockBegin, CodeBlockText, CodeBlockEnd, HintToken, etc.), preserving state across chunk boundaries.
Renderer/highlighter: maps segments into output form (ANSI/HTML/plain), with sanitisation and error recovery.
This is consistent with the CommonMark reference parsing strategy’s first-phase emphasis on consuming input line-by-line to build block structure and discarding lines once incorporated—i.e., streaming parsing is feasible if you design state properly. 

mermaid
Copy
flowchart LR
  A[HTTP Response Body\ntext/event-stream] --> B[SSE Parser\n(line-based, size-limited)]
  B -->|event.data deltas| C[Delta Extractor\n(JSON decode, join)]
  C --> D[Streaming Segmenter\n(Markdown/code/hints state machine)]
  D --> E1[ANSI Renderer\n(escape + style mapping)]
  D --> E2[HTML Renderer\n(markdown->html + sanitise)]
  D --> E3[Plaintext Sink\n(logging/storage)]
  E1 --> F[Output]
  E2 --> F
  E3 --> F
SSE ingestion choices
You can implement SSE parsing directly with net/http + bufio.Reader, but many teams prefer using a library to avoid protocol edge cases and to get built-in limits.

net/http requires you to close the response body and (for connection reuse) read it to EOF; streaming consumers must still ensure proper teardown (context cancellation and Close). 
bufio.Scanner is convenient for line-based protocols but stops unrecoverably on tokens too large for its buffer; the docs recommend bufio.Reader for large tokens or finer control. 
github.com/tmaxmax/go-sse provides a spec-compliant SSE reader (sse.Read) and explicitly supports LLM streaming use cases; it includes a ReadConfig with MaxEventSize (default 64KB) to bound per-event memory. 
github.com/r3labs/sse provides client/server utilities for SSE, widely used, but you should evaluate its compliance and buffering behaviour for your threat/volume model. 
Handling partial/incomplete fences across SSE events
Because SSE events may split arbitrary byte positions, the content segmenter should operate on a continuous stream abstraction, not “per SSE event”. Practically, this means you keep:

a small look-behind buffer (e.g., last 8–16 bytes) to detect fence runs that cross boundaries,
a line accumulation buffer to detect “start-of-line” semantics (CommonMark fences are line-based constructs, allowing up to 3 spaces indentation before the fence), and
mode state: inCodeBlock, fenceChar (or ~),fenceLen, infoString`, current “block start column”, etc. 
A minimal timeline view:

mermaid
Copy
sequenceDiagram
  participant SSE as SSE events
  participant Seg as Segmenter state
  SSE->>Seg: chunk="Here is SQL:\n`"
  Note over Seg: stores trailing "`" in look-behind
  SSE->>Seg: chunk="``sql\nSELECT *\n"
  Note over Seg: recognises "```" + infoString="sql"\nenter CodeBlock mode
  SSE->>Seg: chunk="FROM t;\n``"
  Note over Seg: hold partial closing fence in look-behind
  SSE->>Seg: chunk="`\nDone."
  Note over Seg: completes closing fence\nexit CodeBlock mode; emit segments
CommonMark rules matter operationally: closing fences may be preceded by up to three spaces and must be at least as long as the opening fence; if no closing fence exists, the block runs to end-of-document. These precise conditions are what make a deterministic streaming lexer possible. 

Buffering policies and latency vs memory trade-offs
Buffering is not optional; it is where you trade latency for certainty. The main buffering knobs are:

Tail window size: how much “recent text” you keep to allow reclassification when new evidence appears (e.g., the opening fence appears late).
Line buffer limit: maximum characters you will hold waiting for a newline (important because CommonMark rules are line-oriented). Avoid Scanner defaults for untrusted streams; enforce explicit caps. 
Max open-structure budget: maximum bytes you allow inside an “unclosed” structure (e.g., inside code mode without seeing a close fence) before you forcibly degrade behaviour (e.g., stop highlighting, treat as plaintext, or auto-close on timeout).
If you use an SSE library with a per-event limit (like go-sse’s MaxEventSize), you should still enforce a cross-event limit for your own buffers because a long code block may span many SSE events. 

Comparison of parsing approaches
The table below compares four common strategies (plus a hybrid) for mixed Markdown/code streaming.

Approach	Latency	Memory	Accuracy on fenced Markdown	Accuracy when fences absent/malformed	Complexity	Robustness to weird LLM output
Fence-first streaming lexer (recommended baseline)	Low	Low–Medium (tail + line buffers)	High (spec-aligned) 
Medium (needs extra heuristics)	Medium	High
Heuristic-first classifier (code vs prose per line/chunk)	Very low	Low	Medium (may misclassify real fences)	Medium–High (if tuned)	Medium–High	Medium
Language-detection-on-chunk (detect language continuously)	Low–Medium	Medium	Medium	Medium	High	Medium (language flips can thrash)
Full-buffer-then-parse (wait for completion)	High (worst)	High (entire text)	High	High	Low	Medium (but poor UX)
Hybrid: fence-first + “stable prefix” re-render with tail window	Low–Medium	Medium	High	High (best overall in practice)	High	Very high

The hybrid (“stable prefix + tail window re-render”) is often the most user-pleasing approach: you render a confirmed prefix, keep an uncertain suffix, and re-run parsing/highlighting on only the suffix when new text arrives. This echoes CommonMark’s two-phase model where block structure decisions are line-driven and can be finalised progressively. 

Detection of code blocks and language
Explicit fences and info strings
CommonMark defines a fenced code block as starting with a line containing at least three backticks or tildes (not mixed), optionally preceded by up to three spaces, and ending with a closing fence of the same character and length ≥ the opening fence. The opening fence can be followed by an “info string”; the first word is typically used as the language label (commonly rendered as a language-... class in HTML), but the spec does not mandate how to interpret it. 

Operational implications for a stream parser:

A correct fence detector is line-based. You need to know whether backticks are at line start (modulo indentation) before treating them as a fence. 
You should capture and normalise the first token of the info string (trim, lowercase, map aliases like postgresql→sql), but keep the raw info string for debugging and fallback.
Treat unclosed fences as “code until end” (or until you decide “end of stream” is the end of document in your UI). CommonMark allows this and avoids expensive backtracking. 
Also remember indented code blocks exist (four spaces or a tab as indentation) and have no info string. They are more likely to appear accidentally in LLM output, so you may want to de-emphasise or disable indent-code recognition unless you truly need CommonMark fidelity. 

Implicit code blocks when fences are absent
When fences are missing or unreliable, the core challenge is not “detecting Markdown” but deciding when to treat content as code (and which highlighter to use). A pragmatic, streaming-safe heuristic model is:

Maintain a small rolling window of recent lines (e.g., 20–50).
Compute features per line: indentation depth, ratio of punctuation/symbols, presence of SQL keywords, balanced delimiters (()[]{}), average token length, presence of semicolons, etc.
Trigger “implicit code mode” only when the confidence crosses a threshold and remains consistent for N lines (hysteresis prevents thrashing).
Exit implicit code mode on strong prose signals (multiple sentences with typical punctuation patterns, Markdown list markers, headings).
For SQL specifically, evidence can include SELECT, FROM, WHERE, JOIN, GROUP BY, ORDER BY, INSERT, UPDATE, DELETE, CREATE TABLE, and a high density of commas/parentheses/aliases; however, you should expect false positives in English explanations that mention these keywords.

Because this is heuristic, a key “knob” is conservatism: most UIs look better if ambiguous text stays as Markdown than if prose is incorrectly monospaced and highlighted.

Language identification strategies
Use a layered decision stack:

Use explicit info string when present (highest confidence). 
Use repository-grade language detection on content when fences are absent: go-enry provides content-based identification (ported from GitHub Linguist) via GetLanguage(filename, content). For streaming, you can pass a synthetic filename (e.g., snippet.sql) when you have a guess; otherwise pass an empty name and rely on content signals. 
Use highlighter best-effort detection: Chroma’s quick.Highlight explicitly supports leaving lexer/formatter/style empty for best-effort selection, which can be used as a fallback for small snippets (though you should benchmark and bound it for large inputs). 
In practice, you will also want an alias table and “safe default” lexers (e.g., default to plaintext when uncertain) because language misclassification can produce visually noisy output.

Incremental tokenisation, parsing, and highlighting strategies in Go
Why “incremental Markdown parsing” is different from “incremental rendering”
Most Go Markdown parsers (e.g., Goldmark, Blackfriday, golang-commonmark) are designed for complete documents. Goldmark is CommonMark-compliant and extensible, which makes it a strong choice for final rendering, but it is not designed as a push-based incremental parser. 

As a result, streaming systems usually pick one of two rendering patterns:

Segment-first rendering (recommended): implement a small streaming lexer that only detects block-level transitions you care about (fences, “implicit code mode”), and treat everything else as plain text/Markdown that you can periodically re-render (or render as plaintext until stable).
Reparse-on-update: keep a tail window and re-run a full Markdown renderer over the tail after each chunk. This can be surprisingly effective when coupled with a stable-prefix strategy and careful throttling.
A practical hybrid is: parse fences and code blocks with your own streaming lexer (high confidence), but use a full Markdown renderer only for the accumulating non-code segments (lower confidence) when you have a newline boundary or an idle timeout.

Incremental syntax highlighting options
Chroma (pure Go, simple integration)
Chroma converts source code into highlighted HTML or ANSI-coloured text and is commonly used in Go tooling. It is pure Go and supports multiple output formats. 

Pros:

Simple: highlight a complete code string to ANSI or HTML.
Works well for “highlight-on-closure” (once you have a closed fenced block).
Pairs well with Goldmark via extensions like goldmark-highlighting, which adds highlighting for fenced code blocks using Chroma. 
Cons:

Not inherently incremental: lexers generally assume full input; streaming highlighting often means re-lexing at least a window.
Context sensitivity: partial code can mis-highlight until more text arrives.
Tree-sitter (incremental parsing, query-based highlighting)
Tree-sitter is an incremental parsing system designed to efficiently update syntax trees as source changes. It supports syntax highlighting via query files (e.g., queries/highlights.scm) that pattern-match syntax trees and emit capture names that you map to styles. 

In Go, you can use bindings such as github.com/tree-sitter/go-tree-sitter and apply highlighting using a library like go-tree-sitter-highlight, which interprets highlighting queries and produces highlight names. 

Pros:

True incremental updates: append text, update tree, update highlights (best UX for streaming).
More robust under syntax errors than many regex lexers (still not perfect, but often better for live editing scenarios). 
Cons:

Higher integration cost: per-language grammars, queries, style mapping, and update bookkeeping.
Injection languages (Markdown containing embedded code) require extra plumbing (tree-sitter supports “injections” queries conceptually, but your Go integration must implement the behaviour). 
Colour/formatting mapping to ANSI and HTML
For ANSI terminals, a useful option is glamour, which renders Markdown to ANSI and exposes configuration for colour profiles and Chroma formatters (useful for code blocks). 

For HTML output, avoid emitting raw HTML directly unless you sanitise; CommonMark explicitly states that raw HTML tags are rendered without escaping. 

Concise code sketch examples
SSE consume → streaming segmenter → highlight-on-closure (Chroma)
go
Copy
// Sketch: consume SSE with go-sse, segment deltas, highlight closed code blocks.
// This is intentionally concise; production code should add better error paths,
// metrics, and stricter bounds on all buffers.

type SegmentKind int

const (
	SegMarkdown SegmentKind = iota
	SegCodeBlock // a complete, closed code block
)

type Segment struct {
	Kind     SegmentKind
	Language string // may be empty
	Text     string
}

func StreamSegments(ctx context.Context, body io.Reader, out chan<- Segment) error {
	defer close(out)

	seg := NewFenceSegmenter(/* knobs */)

	// go-sse provides an iterator-style reader and MaxEventSize config.
	cfg := &sse.ReadConfig{MaxEventSize: 64 * 1024} // tune for your API
	for ev, err := range sse.Read(body, cfg) {
		if err != nil {
			return err
		}

		// ev is your SSE event. Most LLM APIs pack JSON in data; extract delta text here.
		delta := ExtractDeltaString(ev) // application-specific

		for _, s := range seg.Push(delta) { // Push returns any completed segments
			out <- s
		}
	}

	// Flush remaining buffered text (may include an unclosed fence).
	for _, s := range seg.Flush() {
		out <- s
	}
	return nil
}

func RenderCodeANSI(w io.Writer, lang, code string) error {
	lexer := lang
	if lexer == "" {
		lexer = "auto" // or "" for best-effort selection, see Chroma docs
	}
	return quick.Highlight(w, code, lexer, "terminal16m", "monokai")
}
Key points behind this sketch:

go-sse is explicitly positioned for reading LLM SSE responses and provides a per-event size cap via MaxEventSize. 
Chroma’s quick.Highlight supports selecting lexer/formatter/style and makes a best-effort when fields are empty. 
Streaming fence segmenter skeleton (handles cross-chunk fences)
go
Copy
// Sketch: a minimal fence-first segmenter.
// Strategy: keep a line buffer + small look-behind; recognise CommonMark fences;
// emit closed code blocks as a single segment; emit everything else as markdown.

type FenceSegmenter struct {
	inCode bool

	fenceChar byte // '`' or '~'
	fenceLen  int
	lang      string

	lineBuf   bytes.Buffer
	codeBuf   bytes.Buffer
	textBuf   bytes.Buffer
	lookbehind []byte // small ring; simplest as slice with cap

	maxLine   int
	maxHold   int // max buffered bytes to avoid unbounded growth
}

func NewFenceSegmenter() *FenceSegmenter {
	return &FenceSegmenter{
		maxLine: 32 * 1024,
		maxHold: 2 * 1024 * 1024, // example: 2MB across all buffers
	}
}

func (s *FenceSegmenter) Push(delta string) []Segment {
	var out []Segment
	for i := 0; i < len(delta); i++ {
		ch := delta[i]
		s.lineBuf.WriteByte(ch)

		if s.lineBuf.Len() > s.maxLine {
			// degrade: force-flush as markdown to avoid memory blowup
			s.textBuf.Write(s.lineBuf.Bytes())
			s.lineBuf.Reset()
		}

		if ch != '\n' {
			continue
		}

		line := s.lineBuf.String()
		s.lineBuf.Reset()

		if !s.inCode {
			if ok, fenceChar, fenceLen, lang := parseOpeningFenceLine(line); ok {
				// flush pending prose before entering code mode
				if s.textBuf.Len() > 0 {
					out = append(out, Segment{Kind: SegMarkdown, Text: s.textBuf.String()})
					s.textBuf.Reset()
				}
				s.inCode, s.fenceChar, s.fenceLen, s.lang = true, fenceChar, fenceLen, lang
				continue
			}
			s.textBuf.WriteString(line)
		} else {
			if ok := parseClosingFenceLine(line, s.fenceChar, s.fenceLen); ok {
				out = append(out, Segment{
					Kind:     SegCodeBlock,
					Language: s.lang,
					Text:     s.codeBuf.String(),
				})
				s.codeBuf.Reset()
				s.inCode, s.fenceChar, s.fenceLen, s.lang = false, 0, 0, ""
				continue
			}
			s.codeBuf.WriteString(line)
		}

		// enforce a total buffered cap across text/code (simplified)
		if s.textBuf.Len()+s.codeBuf.Len() > s.maxHold {
			out = append(out, Segment{Kind: SegMarkdown, Text: s.textBuf.String()})
			s.textBuf.Reset()
			// If codeBuf is huge and unclosed, you may stop highlighting or hard-close.
		}
	}
	return out
}
This aligns with the CommonMark fence rules that make a streaming, line-based recogniser possible (including “unclosed fences run to end”). 

Error recovery, security, and operational safety
Error recovery when LLM emits malformed Markdown or interleaves languages
A streaming renderer should treat “Markdown correctness” as probabilistic and adopt explicit recovery behaviours:

Unclosed fenced blocks: CommonMark treats them as extending to end-of-document; for a “live stream view”, consider implementing a soft close policy (e.g., if no closing fence appears within N KB or T seconds, stop highlighting and render as preformatted text, or auto-close in UI). 
Mismatched fences: if you see a closing fence with the wrong character, CommonMark treats it as literal content within the block; you can follow that or add a “LLM recovery mode” that allows closing with either char when confidence is high (but gate it behind a knob—this changes semantics). 
Language interleaving: when a block seems to switch languages mid-stream, prefer not to retag the whole block; instead, allow “sub-block” detection only if you see a new valid fence. Otherwise, you risk oscillating lexers and visual noise.
A practical recovery mechanism is to record “parser events” (entered code mode, exited code mode, implicit code activated, etc.) with offsets, making misparses diagnosable and testable.

Sanitisation and injection risks
HTML/XSS
CommonMark allows raw HTML blocks and inline raw HTML, and the spec states such tags will be rendered without escaping. This means that rendering LLM output as HTML is unsafe unless you sanitise. 

bluemonday is a Go HTML sanitizer that applies allowlist-based policies for untrusted, user-generated content, explicitly targeting XSS risk. It is designed for the “string in, sanitised HTML out” workflow and provides convenience policies. 

Terminal escape injection (ANSI)
If you render to terminals, treat any escape/control sequences from the model output as untrusted. CWE-150 highlights the risk of improper neutralisation of control sequences. This applies both to direct printing and to logs that are later viewed in terminals. 

A recommended practice is:

Strip or escape all ASCII control characters except \n and \t (and maybe \r if you need it).
Never pass through raw ANSI sequences from the model; instead, parse your own hint syntax and emit ANSI codes only from trusted code paths.
Payload and resource limits
At the SSE layer, enforce maximum event sizes. For example, go-sse’s ReadConfig.MaxEventSize defaults to 64KB, and exceeding it yields an error. 

At the application layer, enforce:

maximum total bytes per response,
maximum bytes buffered in uncertain tail,
maximum bytes inside an unclosed code block,
maximum line length (to avoid pathological no-newline segments).
Also be cautious with bufio.Scanner; its docs emphasise that scanning stops unrecoverably if a token is too large, and recommend bufio.Reader for more control. 

Configuration knobs and fallback behaviours
Useful knobs to expose (with safe defaults):

MaxEventBytes (SSE-level): per-event cap. (If using go-sse, map to MaxEventSize.) 
MaxTotalBytes: cap per streamed completion.
MaxLineBytes: cap on line buffer.
MaxUnclosedCodeBytes / UnclosedFenceTimeout: degrade behaviour if fence never closes.
FenceDetection: {commonmark_strict, llm_recovery}.
IndentCodeBlocks: {off, on} (CommonMark supports indented blocks; often safer off for LLM streams). 
LanguageDetection: {fence_only, fence_then_enry, fence_then_chroma_auto}. 
RenderMode: {ansi, html, plaintext} plus ColourProfile for terminals (TrueColor/ANSI256/ANSI) when using terminal renderers like Glamour. 
TailReparseBytes: size of suffix re-render window.
UpdateCoalescing: minimum time between re-renders to avoid thrash.
Fallback behaviours that keep UX acceptable when things go wrong:

If you cannot confidently classify, render as plain Markdown text without highlighting.
If sanitisation fails or output is too large, fall back to plaintext escaping.
If language detection is unstable, pin the language once chosen for a given code block.
Performance, concurrency, testing, and observability strategies
Performance and concurrency considerations in Go SSE consumers
Concurrency goals for a streaming renderer are: low latency per chunk, bounded allocations, and controlled backpressure. A common pattern is:

one goroutine owns reading the HTTP response body (SSE parsing),
one goroutine owns segmentation state (avoid locks on parser state),
one goroutine owns rendering/output (can be slower; apply backpressure).
Always ensure response bodies are closed, and understand that not reading to EOF and closing can prevent HTTP connection reuse in net/http transports. 

For IO parsing, prefer bufio.Reader-based routines (or a proven SSE library) when lines/events may exceed typical small sizes. The bufio docs explicitly recommend Reader over Scanner when you need more control over large tokens or error handling. 

Testing strategy
A strong testing approach blends:

Golden tests: recorded SSE transcripts (including pathological fence splits) and expected segment streams.
Property-based / fuzz testing: apply fuzzing to your segmenter and hint parser, asserting invariants such as “never panic”, “buffers never exceed caps”, “never emit un-sanitised HTML”, “never emit raw ESC from input”. Go’s tooling supports fuzz tests via testing.F, and the official fuzzing documentation highlights its value for finding crash-causing inputs and injection-like vulnerabilities. 
Differential tests: for fully buffered text, compare your fence detection against a reference CommonMark renderer’s interpretation for fenced block boundaries (where you aim to be compatible). 
Observability
Instrument the pipeline so you can diagnose “why did it render like that?”:

Metrics: bytes/sec, events/sec, average event size, segment counts, time spent in segmentation vs rendering, reparse frequency, buffer usage high-water marks.
Logs/traces: “entered code mode”, “closed code mode”, “implicit code activated”, including offsets and confidence scores.
OpenTelemetry-Go provides APIs to measure performance/behaviour and export telemetry. The OpenTelemetry documentation describes manual instrumentation and the need to initialise the SDK for your language. 

Recommended Go ecosystem tools and libraries
SSE clients/parsers
net/http (standard library): fully capable for streaming bodies; must close bodies and manage cancellation via contexts; best when you need total control. 
github.com/tmaxmax/go-sse: spec-focused SSE implementation with explicit LLM streaming guidance and MaxEventSize configuration; good default choice when you want correctness + limits without reinventing parsing. 
github.com/r3labs/sse/v2: popular client/server SSE utilities; evaluate its parsing and limit controls against your workload and adversarial assumptions. 
Markdown parsing/rendering
github.com/yuin/goldmark: CommonMark-compliant, extensible Markdown parser; strong for final rendering (HTML) or non-streaming phases. 
github.com/russross/blackfriday/v2: widely used Markdown processor with a focus on being “paranoid about its input” and fast; not CommonMark-compliant per Goldmark project notes, so be careful if spec fidelity matters. 
gitlab.com/golang-commonmark/markdown: CommonMark-compliant parser/renderer option; evaluate based on feature needs and maintenance expectations. 
Terminal rendering: github.com/charmbracelet/glamour renders Markdown to ANSI terminals and exposes knobs for colour profiles and Chroma formatter selection. 
Syntax highlighting and language detection
Chroma (github.com/alecthomas/chroma): pure-Go syntax highlighting to HTML/ANSI; simple integration for highlight-on-closure, and a common building block in Go Markdown ecosystems. 
Goldmark highlighting glue: github.com/yuin/goldmark-highlighting/v2 (Chroma-backed) for fenced code blocks when rendering full Markdown. 
Language detection: github.com/go-enry/go-enry/v2 provides content-based language identification derived from GitHub Linguist. 
Incremental highlighting: tree-sitter bindings (github.com/tree-sitter/go-tree-sitter) + go-tree-sitter-highlight, leveraging tree-sitter’s query-based highlighting system. 
Sanitisation/security
HTML sanitisation: github.com/microcosm-cc/bluemonday allowlist-based HTML sanitiser for untrusted content. 
Control-sequence hygiene: treat terminal escape injection as CWE-150 class risk and neutralise control sequences from untrusted input. 
CommonMark-specific safety note: U+0000 must be replaced with U+FFFD for security reasons (important when normalising streams). 
Entities referenced
The CommonMark spec is authored by John MacFarlane under a Creative Commons licence. 

SSE behaviour is specified by WHATWG and has historical drafts under W3C; practitioner-facing SSE documentation is hosted by Mozilla (MDN). 

Control-sequence injection risks are catalogued by MITRE (CWE). 

Observability recommendations align with OpenTelemetry guidance. 
