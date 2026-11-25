# Main File Type Extraction Pipelines

## Scope

All uploaded and imported files go through one first step:

```text
fileingest.Ingest or fileingest.EvaluatePath
  -> filepipeline.ExtractFile
  -> type-specific local extractor
  -> artifact metadata or project file_evaluations.json
```

This document covers the additional main file families implemented after the PowerPoint and spreadsheet pipelines:

- PDF
- DOCX, RTF, and legacy DOC handling
- raster images
- text, Markdown, JSON/JSONL, HTML, XML/SVG
- ZIP/TAR/TGZ/GZ archive inventories

The common design principle is provider agnosticism. Provider adapters should not parse raw files. They can optionally receive native PDFs/images when supported, but the application still writes local sidecars that `inspect_attachment`, `read_attachment`, and `search_attachment` can use consistently.

## Common Output Contract

Every extractor writes:

- `manifest.json`
- a readable Markdown sidecar exposed as `markdownPath`
- a machine-readable JSON sidecar exposed as `dataPath`
- compact `metadata["extraction"]` on thread/public artifacts or project sidecars

Common metadata keys:

```json
{
  "kind": "pdf|document|image|text|archive",
  "pipeline": "pdf|docx|rtf|image|json|html|markdown|text|xml|zip|tar|tgz|gz",
  "version": 1,
  "status": "complete|partial|failed",
  "rootPath": "...",
  "manifestPath": "...",
  "dataPath": "...",
  "markdownPath": "...",
  "capabilities": {},
  "warnings": []
}
```

## PDF

### Research Summary

OpenAI, Anthropic, and Gemini all have native PDF paths, but the behavior differs by provider. OpenAI file inputs can provide extracted PDF text and page images to vision-capable models. Anthropic document blocks support PDFs, but limits and Bedrock behavior differ. Gemini treats PDFs as the key document-vision format and counts rendered pages as image input.

For the app, native provider PDF input is an enhancement, not the source of truth. The provider-neutral baseline is a local sidecar. A future high-fidelity version should use a real PDF engine such as PDFium, PyMuPDF/PyMuPDF4LLM, Docling, Poppler, or OCR tooling behind `EnableExternalTools`.

### Current Local Pipeline

Implemented in:

- `server/internal/chat/pdfextract`
- `server/internal/chat/filepipeline/pdf.go`

The current dependency-free local extractor:

1. Validates the `%PDF-` header.
2. Counts page objects.
3. Detects `/Encrypt`.
4. Detects linearization.
5. Extracts simple title/author metadata when present.
6. Performs best-effort literal-string text extraction.
7. Writes sidecars and labels text quality as `basic_literal_strings`.

It does not claim layout, table, image, compressed-stream, or OCR fidelity.

### Outputs

```text
manifest.json
document.json
document.md
text.txt
pages.jsonl
```

Key metadata:

- `pageCount`
- `encrypted`
- `linearized`
- `textExtractionQuality`
- `nativePdfEligible`
- `pagesPath`
- `textPath`

## Documents

### Research Summary

DOCX is an Office Open XML ZIP package. Microsoft documents WordprocessingML as a structured document/body/paragraph/run/text model with related parts for headers, footers, comments, footnotes, endnotes, media, and relationships. LLM-facing extraction should preserve semantic order and structured tables rather than attempting pixel-perfect rendering.

Legacy `.doc` should not be parsed by hand. The recommended future path is optional LibreOffice/Tika-style conversion behind `EnableExternalTools`. RTF can be reduced to text locally.

### Current Pipeline

Implemented in:

- `server/internal/chat/documentextract`
- `server/internal/chat/filepipeline/document.go`

DOCX stages:

1. Validate ZIP package safety.
2. Require `[Content_Types].xml` and `word/document.xml`.
3. Parse `word/document.xml` plus headers, footers, footnotes, endnotes, and comments when present.
4. Extract paragraphs, table rows/cells, relationships, and media inventory.
5. Flag external relationship targets and macro/OLE-like parts.
6. Write Markdown, plain text, paragraph JSONL, tables JSON, relationship JSON, and media manifest.

RTF stages:

1. Strip basic RTF control words.
2. Emit text, Markdown, and paragraph JSONL.

Legacy `.doc`:

Returns failed metadata with an explicit unsupported message.

### Outputs

```text
manifest.json
document.json
document.md
text.txt
paragraphs.jsonl
tables.json
relationships.json
media_manifest.json
raw/ooxml/...        optional
```

Key metadata:

- `paragraphCount`
- `tableCount`
- `wordCount`
- `characterCount`
- `headerFooterCount`
- `commentCount`
- `relationshipCount`
- `mediaCount`
- `hasMacrosOrOle`
- `hasExternalTargets`

## Images

### Research Summary

Images are best handled with a dual path:

1. Native image input for supported providers and formats when the task needs visual reasoning.
2. Local metadata/OCR sidecars for deterministic inspection, searchability, routing, and safety.

OCR is not enabled by default. A future stage can run Tesseract or another OCR engine behind `EnableExternalTools`, reporting `ocrStatus` rather than failing when unavailable.

### Current Pipeline

Implemented in:

- `server/internal/chat/imageextract`
- `server/internal/chat/filepipeline/image.go`

The extractor:

1. Decodes PNG/JPEG/GIF dimensions with Go image decoders.
2. Parses WebP dimensions directly from RIFF headers.
3. Detects animated GIF frame count.
4. Records alpha for PNG.
5. Computes native vision eligibility.
6. Emits `ocrStatus: "not_run"`.

### Outputs

```text
manifest.json
image.json
image.md
```

Key metadata:

- `width`
- `height`
- `pixelCount`
- `animated`
- `frameCount`
- `hasAlpha`
- `nativeVisionEligible`
- `nativeVisionBlockedReason`
- `ocrStatus`

## Text, Markdown, JSON, HTML, XML

### Research Summary

Text-like files should be chunked locally with metadata. Provider file-search systems perform similar parse/chunk/index steps, but their behavior and limits differ. The local pipeline gives the app stable chunks and validation metadata before provider dispatch.

HTML/XML should ideally use parsers. The current implementation keeps dependencies low and uses conservative stripping for HTML; this is acceptable for preview/search sidecars but can be upgraded to `golang.org/x/net/html` if richer element provenance is needed.

### Current Pipeline

Implemented in:

- `server/internal/chat/textextract`
- `server/internal/chat/filepipeline/text.go`

The extractor:

1. Reads text as UTF-8 or ASCII-preserved text.
2. Strips UTF-8 BOM.
3. Routes by pipeline:
   - `text`
   - `markdown`
   - `json`
   - `jsonl`
   - `html`
   - `xml`
4. Validates JSON and JSONL.
5. Extracts HTML title and visible text.
6. Emits fixed-size chunks.

### Outputs

```text
manifest.json
content.json
content.md
text.txt
chunks.jsonl
structure.json
```

Key metadata:

- `lineCount`
- `wordCount`
- `characterCount`
- `chunkCount`
- `validJson`
- `jsonRootType`
- `htmlTitle`
- `chunksPath`
- `structurePath`

## Archives

### Research Summary

Archives should be inventoried before any extraction. The security baseline is to detect traversal paths, absolute paths, null bytes, unsafe symlinks, suspicious compression ratios, and aggregate uncompressed size. This follows common ZIP-slip and archive upload safety guidance.

The current pipeline inventories archives but does not recursively extract child files. Recursive child extraction should be a later controlled stage with budget limits.

### Current Pipeline

Implemented in:

- `server/internal/chat/archiveextract`
- `server/internal/chat/filepipeline/archive.go`

Supported formats:

- ZIP
- TAR
- TGZ
- GZ single-member inventory

The extractor:

1. Reads archive entries without writing child files.
2. Normalizes entry paths.
3. Flags unsafe paths and symlink targets.
4. Counts files, directories, symlinks, sizes, and max depth.
5. Writes a Markdown index and JSONL inventory.

### Outputs

```text
manifest.json
archive.json
entries.jsonl
index.md
```

Key metadata:

- `entryCount`
- `fileCount`
- `directoryCount`
- `symlinkCount`
- `unsafeEntryCount`
- `totalUncompressedBytes`
- `totalCompressedBytes`
- `maxDepth`

## Attachment Tools

`inspect_attachment` surfaces compact extraction metadata for all implemented families.

For non-text attachments, `read_attachment` uses:

1. `markdownPath`
2. `dataPath`
3. `manifestPath`

`search_attachment` walks extraction roots and searches Markdown, JSON, JSONL, and text outputs, skipping raw OOXML folders. This makes binary files inspectable without provider-specific parsing.

## Verification

Focused tests live in:

- `server/internal/chat/filepipeline/pdf_test.go`
- `server/internal/chat/filepipeline/document_test.go`
- `server/internal/chat/filepipeline/image_test.go`
- `server/internal/chat/filepipeline/text_test.go`
- `server/internal/chat/filepipeline/archive_test.go`
- `server/internal/chat/tools/thread_attachment_tools_test.go`

These tests verify dispatch, metadata, and sidecar path existence for the main file families.
