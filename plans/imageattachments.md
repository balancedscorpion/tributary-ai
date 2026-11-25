# Image Attachments in Chat

## Problem

Users cannot attach images to chat messages. Visual context — screenshots of dashboards, schema diagrams, error screenshots, chart mockups — has to be described in text. This adds friction and loses fidelity. The `ArtifactType` union already includes `'image'` but nothing in the UI or backend produces or consumes image artifacts.

## Design Principles

1. **Zero new mental models.** Images are artifacts. They pin, reference, and render using the same systems as data and code artifacts. Users already understand artifacts; images should feel like a natural extension.
2. **Paste-first, instant preview.** The primary entry point is `Cmd/Ctrl+V`. No modal dialogs, no multi-step uploads. The image appears inline in the composer immediately.
3. **Consistent with existing patterns.** Image thumbnails in the pinned row follow the same chip pattern as pinned query results. Image artifacts in messages follow the same click-to-open-panel pattern as data cards.
4. **Client-side only in Phase 1.** Images are stored as base64 data URIs on the artifact and sent inline in the message payload. No new server endpoints, no file upload API, no blob storage. This keeps the first iteration small and self-contained.
5. **Graceful limits.** Cap file size, validate MIME types, show clear feedback via existing toast system. Don't silently fail.

---

## Architecture Overview

```
User Input                    Message Rendering              Artifact Panel
─────────────────────        ──────────────────────         ─────────────────

 Paste / Drop / Pick         UserMessage.tsx                ArtifactPanel.tsx
       │                       │                              │
       ▼                       ▼                              ▼
 ChatInput.tsx               ImageThumbnail                 ImageRenderer
 (onPaste/onDrop/onClick)    (inline in bubble)             (full viewer)
       │                       │                              │
       ▼                       ▼                              ▼
 useChat.ts                  Click → onOpenArtifact         Zoom / Pan / Info
 addImageArtifact()          (opens ArtifactPanel)          Pin / Reference
       │
       ▼
 sendMessage() with
 referencedArtifactIds[]
```

No new server endpoints. Images travel as artifact references on the `UIMessage`, same as pinned data/code artifacts today. The LLM receives the image context via the existing `hiddenContext` field or through provider-specific image content blocks.

---

## Detailed Design

### 1. Image Input — ChatInput.tsx

**Modified file:** `components/ChatInput.tsx`

Three input vectors, all converging to the same handler:

#### 1a. Paste (`onPaste`)

Intercept the native `paste` event on the textarea. Check `clipboardData.items` for `image/*` MIME types. If found, extract the `File` object and pass it to the shared `handleImageFile` function.

```tsx
const handlePaste = (e: React.ClipboardEvent) => {
  const items = Array.from(e.clipboardData.items);
  const imageItem = items.find((item) => item.type.startsWith('image/'));
  if (!imageItem) return; // Let normal text paste proceed
  e.preventDefault();
  const file = imageItem.getAsFile();
  if (file) handleImageFile(file);
};
```

Attach to the textarea: `<Textarea onPaste={handlePaste} ... />`

#### 1b. Drag & Drop (`onDragOver` / `onDrop`)

Add drag-and-drop handlers to the outer composer `<div>`. Show a visual drop zone overlay on `dragenter`/`dragover` (subtle border highlight, not a full-screen modal). Accept files on `drop`, filter for `image/*`.

```tsx
const [isDragging, setIsDragging] = useState(false);

const handleDragOver = (e: React.DragEvent) => {
  e.preventDefault();
  if (e.dataTransfer.types.includes('Files')) setIsDragging(true);
};

const handleDragLeave = (e: React.DragEvent) => {
  // Only clear when leaving the container (not entering a child)
  if (!e.currentTarget.contains(e.relatedTarget as Node)) setIsDragging(false);
};

const handleDrop = (e: React.DragEvent) => {
  e.preventDefault();
  setIsDragging(false);
  const files = Array.from(e.dataTransfer.files).filter((f) => f.type.startsWith('image/'));
  files.forEach(handleImageFile);
};
```

The outer composer div gains: `onDragOver={handleDragOver}` `onDragLeave={handleDragLeave}` `onDrop={handleDrop}`

When `isDragging` is true, apply a ring highlight: `ring-2 ring-primary/50 ring-dashed`

#### 1c. File Picker (attach button)

Add a small `ImagePlus` (or `Paperclip`) icon button to the left of the send button area. Clicking opens a hidden `<input type="file" accept="image/*" multiple>`. On change, pass selected files to `handleImageFile`.

```tsx
const fileInputRef = useRef<HTMLInputElement>(null);

<input
  ref={fileInputRef}
  type="file"
  accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml"
  multiple
  className="hidden"
  onChange={(e) => {
    Array.from(e.target.files || []).forEach(handleImageFile);
    e.target.value = ''; // Reset so same file can be re-selected
  }}
/>

<Tooltip>
  <TooltipTrigger asChild>
    <Button
      variant="ghost"
      size="icon"
      onClick={() => fileInputRef.current?.click()}
      className="rounded-xl text-muted-foreground hover:text-foreground"
    >
      <ImagePlus size={20} />
    </Button>
  </TooltipTrigger>
  <TooltipContent>Attach image</TooltipContent>
</Tooltip>
```

#### 1d. Shared Handler — `handleImageFile`

Validates and converts the file to a base64 data URI, then calls the parent callback.

```tsx
const MAX_IMAGE_SIZE_BYTES = 10 * 1024 * 1024; // 10 MB
const ACCEPTED_MIME_TYPES = new Set([
  'image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/svg+xml',
]);

const handleImageFile = async (file: File) => {
  if (!ACCEPTED_MIME_TYPES.has(file.type)) {
    toast.error(`Unsupported image type: ${file.type}`);
    return;
  }
  if (file.size > MAX_IMAGE_SIZE_BYTES) {
    toast.error(`Image too large (${(file.size / 1024 / 1024).toFixed(1)} MB). Max 10 MB.`);
    return;
  }

  const dataUri = await readFileAsDataUri(file);
  // Optional: read dimensions for metadata
  const dimensions = await getImageDimensions(dataUri);

  onAttachImage?.({
    fileName: file.name || 'Pasted image',
    mimeType: file.type,
    sizeBytes: file.size,
    dataUri,
    width: dimensions?.width,
    height: dimensions?.height,
  });
};
```

Helper to read dimensions (non-blocking):

```tsx
function getImageDimensions(dataUri: string): Promise<{ width: number; height: number } | null> {
  return new Promise((resolve) => {
    const img = new Image();
    img.onload = () => resolve({ width: img.naturalWidth, height: img.naturalHeight });
    img.onerror = () => resolve(null);
    img.src = dataUri;
  });
}
```

#### 1e. New ChatInput Props

```tsx
interface ImageAttachment {
  fileName: string;
  mimeType: string;
  sizeBytes: number;
  dataUri: string;
  width?: number;
  height?: number;
}

interface ChatInputProps {
  // ... existing props ...
  onAttachImage?: (image: ImageAttachment) => void;
  pendingImages?: ImageAttachment[];
  onRemoveImage?: (index: number) => void;
}
```

#### 1f. Image Preview in Composer

Pending images render in the pinned row alongside existing artifact chips:

```tsx
{pinnedArtifacts.length > 0 || pendingImages.length > 0 && (
  <div className="flex flex-wrap gap-1.5 px-3 pt-2.5 pb-1.5 border-b border-border/30">
    {/* Existing artifact chips (unchanged) */}
    {pinnedArtifacts.map((artifact) => (
      /* ... existing code ... */
    ))}

    {/* Image preview chips */}
    {pendingImages.map((img, i) => (
      <button
        key={i}
        onClick={() => onRemoveImage?.(i)}
        className="group/chip flex items-center gap-1.5 bg-muted/50 border border-border/50
                   rounded-lg px-2 py-1.5 text-xs animate-fade-in
                   hover:border-border transition-colors"
      >
        <img
          src={img.dataUri}
          alt={img.fileName}
          className="h-8 w-8 rounded object-cover shrink-0"
        />
        <span className="truncate max-w-[100px] text-muted-foreground">
          {img.fileName}
        </span>
        <X size={12} className="text-muted-foreground/60 group-hover/chip:text-foreground
                                 shrink-0 ml-0.5 transition-colors" />
      </button>
    ))}
  </div>
)}
```

The thumbnail is 32x32px (`h-8 w-8`), object-cover, matching the chip height. Clicking the chip removes it (same pattern as artifact chip removal).

---

### 2. Image Artifact Creation — useChat.ts

**Modified file:** `hooks/useChat.ts`

#### 2a. New `addImageArtifact` function

Called from the parent component when `onAttachImage` fires. Creates an `Artifact` with `type: 'image'` and stores the base64 data URI in `content`.

```typescript
const addImageArtifact = useCallback((image: ImageAttachment): Artifact => {
  const artifact: Artifact = {
    id: crypto.randomUUID(),
    type: 'image',
    title: image.fileName,
    content: image.dataUri,
    metadata: {
      mimeType: image.mimeType,
      sizeBytes: image.sizeBytes,
      width: image.width,
      height: image.height,
      source: image.fileName.startsWith('Pasted') ? 'paste' : 'upload',
    },
  };
  // Store in the artifact map (same as tool-created artifacts)
  setArtifacts((prev) => ({ ...prev, [artifact.id]: artifact }));
  return artifact;
}, [setArtifacts]);
```

#### 2b. Pending Images State

The parent page component (or useChat) manages pending image attachments:

```typescript
const [pendingImages, setPendingImages] = useState<
  Array<ImageAttachment & { artifactId: string }>
>([]);

const handleAttachImage = useCallback((image: ImageAttachment) => {
  const artifact = addImageArtifact(image);
  setPendingImages((prev) => [...prev, { ...image, artifactId: artifact.id }]);
}, [addImageArtifact]);

const handleRemoveImage = useCallback((index: number) => {
  setPendingImages((prev) => {
    const removed = prev[index];
    // Optionally remove the artifact too, or keep it (cheap, user may re-add)
    return prev.filter((_, i) => i !== index);
  });
}, []);
```

#### 2c. Integrating into `sendMessage`

When `sendMessage` is called, pending image artifact IDs are included in `referencedArtifactIds`:

```typescript
const sendMessage = useCallback(async (
  content: string,
  opts?: {
    referencedArtifactIds?: string[];
    hiddenContext?: string;
    imageArtifactIds?: string[];
  },
): Promise<boolean> => {
  // ... existing validation ...

  // Merge image artifact IDs into referenced artifacts
  const allReferencedIds = [
    ...(opts?.referencedArtifactIds || []),
    ...(opts?.imageArtifactIds || []),
  ];

  const userMessage: UIMessage = {
    role: Role.User,
    content: trimmed,
    referencedArtifactIds: allReferencedIds.length > 0 ? allReferencedIds : undefined,
    hiddenContext: opts?.hiddenContext,
  };

  // ... rest unchanged ...
}, [/* deps */]);
```

The calling code clears `pendingImages` after `sendMessage` returns `true`.

---

### 3. Message Rendering — UserMessage.tsx

**Modified file:** `components/chat/UserMessage.tsx`

When a user message references image artifacts, render inline thumbnails below the text (alongside existing artifact chips).

```tsx
const UserMessage: React.FC<UserMessageProps> = ({
  content,
  referencedArtifacts = [],
  onOpenArtifact,
}) => {
  // Separate images from other artifacts
  const imageArtifacts = referencedArtifacts.filter((a) => a.type === 'image');
  const otherArtifacts = referencedArtifacts.filter((a) => a.type !== 'image');

  return (
    <div className="flex w-full justify-end animate-fade-in min-w-0">
      <div className="flex gap-3 w-full max-w-[90%] md:max-w-[85%] flex-row-reverse min-w-0">
        <Avatar className="h-8 w-8 mt-1 flex-shrink-0">
          <AvatarFallback className="bg-primary text-primary-foreground">
            <User size={18} />
          </AvatarFallback>
        </Avatar>
        <div className="flex flex-col gap-1 min-w-0 w-full items-end">
          <div className="px-4 py-3 rounded-2xl rounded-tr-none shadow-sm w-full
                          bg-primary text-primary-foreground">
            {content && (
              <p className="text-sm leading-relaxed whitespace-pre-wrap">{content}</p>
            )}

            {/* Inline image thumbnails */}
            {imageArtifacts.length > 0 && (
              <div className={content ? 'mt-2' : ''}>
                <div className="flex flex-wrap gap-2">
                  {imageArtifacts.map((artifact) => (
                    <button
                      key={artifact.id}
                      type="button"
                      onClick={() => onOpenArtifact?.(artifact)}
                      className="group/img rounded-lg overflow-hidden border border-primary-foreground/20
                                 hover:border-primary-foreground/40 transition-all
                                 hover:ring-1 hover:ring-primary-foreground/20"
                      title={artifact.title}
                    >
                      <img
                        src={artifact.content}
                        alt={artifact.title}
                        className="max-w-[240px] max-h-[160px] object-contain rounded-lg"
                      />
                    </button>
                  ))}
                </div>
              </div>
            )}

            {/* Existing non-image artifact chips */}
            {otherArtifacts.length > 0 && (
              <div className={content || imageArtifacts.length > 0 ? 'mt-2' : ''}>
                <div className="flex flex-wrap gap-1.5">
                  {otherArtifacts.map((artifact) => (
                    /* ... existing chip rendering (unchanged) ... */
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  );
};
```

**Key rendering decisions:**
- Images render at `max-w-[240px] max-h-[160px]` with `object-contain` — large enough to be legible, small enough to not dominate the message stream.
- Click opens the full image in the ArtifactPanel (reusing `onOpenArtifact`).
- Non-image artifacts continue rendering as text chips below (no change to existing behavior).
- Multiple images stack horizontally with `flex-wrap`.

---

### 4. Artifact Panel — Image Viewer

**Modified file:** `components/ArtifactPanel.tsx`

When the active artifact has `type === 'image'`, render an image viewer instead of the data/code tabs.

```tsx
// Inside ArtifactPanel, before the return statement:
const isImage = artifact.type === 'image';

// In the content area (replacing the ternary between hasQueryMetadata and default):
{isImage ? (
  <div className="flex-1 overflow-auto p-4 flex items-center justify-center bg-muted/20">
    <img
      src={artifact.content}
      alt={artifact.title}
      className="max-w-full max-h-full object-contain rounded-lg shadow-md"
    />
  </div>
) : hasQueryMetadata ? (
  /* ... existing tabbed data/schema/info view ... */
) : (
  /* ... existing default view ... */
)}
```

**Header adjustments for images:**

```tsx
// In the header icon/label resolution:
const isImage = artifact.type === 'image';
const fallbackIcon = isImage ? ImageIcon : artifact.type === 'data' ? Database : Code2;
const fallbackLabel = isImage ? 'Image' : artifact.type === 'data' ? 'Interactive Data' : 'Source Code';
const iconBg = isImage
  ? 'bg-purple-500/10 text-purple-500'
  : artifact.type === 'data' ? 'bg-accent/10 text-accent' : 'bg-primary/10 text-primary';
```

**Image info subtitle** (below the title):

```tsx
{isImage && artifact.metadata && (
  <p className="text-[10px] text-muted-foreground">
    {artifact.metadata.width && artifact.metadata.height
      ? `${artifact.metadata.width} x ${artifact.metadata.height} · `
      : ''}
    {artifact.metadata.mimeType?.split('/')[1]?.toUpperCase()}
    {artifact.metadata.sizeBytes
      ? ` · ${(artifact.metadata.sizeBytes / 1024).toFixed(0)} KB`
      : ''}
  </p>
)}
```

The **Raw/Rendered toggle** and **Pin** button remain functional for images:
- Raw mode could show the base64 data URI (useful for debugging).
- Pin adds the image to the chat input for referencing in a follow-up message.

---

### 5. Renderer Registry — ImageRenderer

**New file:** `components/renderers/type/ImageRenderer.tsx`

Register a type renderer so the existing `resolveRenderer` system can handle image artifacts:

```tsx
import React from 'react';
import { ImageIcon } from 'lucide-react';
import { registerTypeRenderer } from '../registry';
import type { RendererProps } from '../types';

const ImageRenderer: React.FC<RendererProps> = ({ artifact }) => {
  return (
    <div className="h-full w-full flex items-center justify-center overflow-auto p-4">
      <img
        src={artifact.content}
        alt={artifact.title}
        className="max-w-full max-h-full object-contain rounded-lg"
      />
    </div>
  );
};

registerTypeRenderer('image', {
  component: ImageRenderer,
  label: 'Image',
  icon: ImageIcon,
});

export default ImageRenderer;
```

**Modified file:** `components/renderers/index.ts`

Add the import to trigger registration:

```typescript
// Type fallback renderers
import './type/DataRenderer';
import './type/CodeRenderer';
import './type/ImageRenderer';  // New
```

---

### 6. ArtifactCard — Image Type Support

**Modified file:** `components/chat/ArtifactCard.tsx`

The `ArtifactCard` component currently only handles `'data' | 'code'` types. Extend it to support `'image'`:

```tsx
interface ArtifactCardProps {
  title: string;
  type: 'data' | 'code' | 'image';  // Extended
  details?: React.ReactNode;
  thumbnailSrc?: string;              // New — for image preview
  onClick: () => void;
  onPin?: (e: React.MouseEvent) => void;
  isPinned?: boolean;
}

const ArtifactCard: React.FC<ArtifactCardProps> = ({
  title, type, details, thumbnailSrc, onClick, onPin, isPinned,
}) => {
  const iconBg = type === 'data'
    ? 'bg-accent/10 text-accent'
    : type === 'image'
      ? 'bg-purple-500/10 text-purple-500'
      : 'bg-primary/10 text-primary';

  return (
    <Card onClick={onClick} className="...">
      {thumbnailSrc ? (
        <img src={thumbnailSrc} alt="" className="h-10 w-10 rounded-lg object-cover shrink-0" />
      ) : (
        <div className={`p-2 rounded-lg ${iconBg}`}>
          {type === 'data' ? <TableIcon size={20} />
            : type === 'image' ? <ImageIcon size={20} />
            : <Code2 size={20} />}
        </div>
      )}
      {/* ... rest unchanged ... */}
    </Card>
  );
};
```

---

### 7. Provider Integration (Phase 2)

When sending image artifacts to the LLM, the content needs to be formatted according to each provider's vision/multimodal API.

**Modified file:** `providers/adapters/anthropicHttp.ts` (and equivalents for other providers)

Before sending a message that references image artifacts, convert the image data URI into the provider's expected format:

**Anthropic format** (Claude vision):
```json
{
  "role": "user",
  "content": [
    { "type": "text", "text": "What does this schema diagram mean?" },
    {
      "type": "image",
      "source": {
        "type": "base64",
        "media_type": "image/png",
        "data": "<base64-encoded-data>"
      }
    }
  ]
}
```

**OpenAI-compatible format:**
```json
{
  "role": "user",
  "content": [
    { "type": "text", "text": "What does this schema diagram mean?" },
    {
      "type": "image_url",
      "image_url": { "url": "data:image/png;base64,<data>" }
    }
  ]
}
```

**Gemini format:**
```json
{
  "parts": [
    { "text": "What does this schema diagram mean?" },
    {
      "inlineData": {
        "mimeType": "image/png",
        "data": "<base64-encoded-data>"
      }
    }
  ]
}
```

The adapter layer should detect when a message has `referencedArtifactIds` pointing to image artifacts, look them up from the artifact store, and inject the appropriate multimodal content blocks. For Phase 1, a simpler approach is to include the image description in `hiddenContext` as a text fallback.

---

### 8. Type Changes

**Modified file:** `types.ts`

No changes to existing types. `ArtifactType` already includes `'image'`. The `Artifact` interface is flexible enough (content as string, metadata as `Record<string, any>`).

One optional addition for type safety in the component layer:

```typescript
/** Shape of metadata for image artifacts. */
export interface ImageArtifactMetadata {
  mimeType: string;
  sizeBytes: number;
  width?: number;
  height?: number;
  source: 'paste' | 'drop' | 'upload';
}
```

This is advisory — image artifacts still use `metadata?: Record<string, any>` on the `Artifact` interface.

---

## User Experience Scenarios

### Scenario 1: Paste a screenshot

```
User pastes a dashboard screenshot (Cmd+V)

┌─────────────────────────────────────────────┐
│ [🖼 Pasted image  ✕]                        │  ← preview chip appears instantly
├─────────────────────────────────────────────┤
│ What's causing the spike in this chart?     │
│                                             │
├──────────────────────────────── [Send] ──────┤

User hits Enter.

User message bubble:
┌────────────────────────────────────┐
│  What's causing the spike in       │
│  this chart?                       │
│                                    │
│  ┌──────────────────┐              │
│  │   (screenshot)   │  ← clickable │
│  │                  │    thumbnail  │
│  └──────────────────┘              │
└────────────────────────────────────┘

Clicking the thumbnail opens ArtifactPanel
with the full-resolution image.
```

### Scenario 2: Paste image with pinned query results

```
User has a pinned "Revenue by Region" artifact from a previous query.
User pastes an ERD diagram.

┌────────────────────────────────────────────────┐
│ [📊 Revenue by Region ✕]  [🖼 Pasted image ✕] │
├────────────────────────────────────────────────┤
│ Does this schema match the data I queried?     │
│                                                │
├─────────────────────────────────── [Send] ──────┤

Both the data artifact and the image artifact are
referenced in the same message. The assistant can
reason about both.
```

### Scenario 3: Drag and drop multiple images

```
User drags 3 mockup screenshots from Finder onto the chat input.

Drop zone activates (dashed border highlight).
User releases.

┌──────────────────────────────────────────────────────┐
│ [🖼 mockup-v1.png ✕] [🖼 mockup-v2.png ✕] [🖼 ...]  │
├──────────────────────────────────────────────────────┤
│ Which of these dashboard layouts is clearest?        │
│                                                      │
├──────────────────────────────────────── [Send] ────────┤

All three images render as inline thumbnails in
the sent message. Each is clickable to view full-size.
```

### Scenario 4: Image too large

```
User pastes a 25 MB screenshot.

Toast notification (via Sonner):
  "Image too large (25.0 MB). Max 10 MB."

Nothing is added to the composer. The user can
resize the image externally and paste again.
```

### Scenario 5: Reference a previous image

```
User sends a schema diagram in message 1.
Later, user wants to ask a follow-up.

User opens ArtifactPanel for the image → clicks "Reference" button.
The image appears as a pinned chip in the composer.

┌─────────────────────────────────────────────┐
│ [🖼 schema-diagram.png ✕]                   │
├─────────────────────────────────────────────┤
│ Now add the "orders" table to this diagram  │
├──────────────────────────────────── [Send] ──┤
```

---

## Edge Cases

### Clipboard contains both text and image
When pasting, check for image items first. If an image is found, prevent default (don't paste the text). The user can paste text separately. This matches the behavior of Slack, Discord, and other chat apps.

### SVG files
SVGs are accepted (`image/svg+xml`) but rendered via `<img>` tag with the data URI, not as inline SVG. This prevents XSS from embedded scripts in SVG. The `object-contain` sizing handles SVGs naturally.

### Animated GIFs
GIFs play in thumbnails and in the ArtifactPanel viewer. No special handling needed — `<img>` with a GIF data URI plays natively.

### Very large dimensions (e.g. 8000x6000)
The `max-w-[240px] max-h-[160px]` constraint in message thumbnails and `max-w-full max-h-full` in the ArtifactPanel prevents layout blowout. The browser scales via `object-contain`.

### Base64 size in localStorage / thread history
Base64-encoded images are ~33% larger than the binary. A 5 MB image becomes ~6.7 MB of base64 text stored in the artifact map. If thread history is persisted to localStorage, this could hit storage limits. Mitigation: in Phase 2, move to server-side blob storage with URL references. For Phase 1, cap at 10 MB input (≈13 MB base64) and accept the tradeoff.

### Provider doesn't support vision
If the active provider/model doesn't support multimodal input, fall back to sending the image filename and dimensions in `hiddenContext` as text. The image still renders in the UI; the LLM just can't "see" it. Consider showing a subtle warning: "This model may not be able to analyze images."

---

## File Change Summary

| File | Change Type | Description |
|------|------------|-------------|
| `components/ChatInput.tsx` | Modified | Add `onPaste`, `onDragOver`/`onDrop` handlers, file picker button, image preview chips in pinned row. New props: `onAttachImage`, `pendingImages`, `onRemoveImage`. |
| `components/chat/UserMessage.tsx` | Modified | Separate image artifacts from other referenced artifacts. Render image thumbnails inline in the message bubble. |
| `components/chat/ArtifactCard.tsx` | Modified | Extend `type` prop to include `'image'`. Add optional `thumbnailSrc` prop. Add image icon/color. |
| `components/ArtifactPanel.tsx` | Modified | Add image viewer branch when `artifact.type === 'image'`. Show image metadata (dimensions, format, size). Import `ImageIcon` from lucide. |
| `components/renderers/type/ImageRenderer.tsx` | **New** | Type renderer for image artifacts. Registers with `registerTypeRenderer('image', ...)`. |
| `components/renderers/index.ts` | Modified | Import `ImageRenderer` to trigger registration. |
| `hooks/useChat.ts` | Modified | Add `addImageArtifact` function. Add `pendingImages` state management. Merge image artifact IDs into `referencedArtifactIds` on send. |
| `types.ts` | Optional | Add `ImageArtifactMetadata` interface (advisory typing). |

### Phase 2 (follow-up)

| File | Change Type | Description |
|------|------------|-------------|
| `providers/adapters/anthropicHttp.ts` | Modified | Convert image artifacts to Anthropic vision content blocks before sending. |
| `providers/adapters/*` | Modified | Same for OpenAI-compatible, Gemini, Bedrock adapters. |
| `server/internal/chat/httpapi/handler.go` | Modified | Add `POST /api/images/upload` endpoint for server-side storage (replaces base64 inline). |
| `server/internal/chat/engine/engine.go` | Modified | Inject image content blocks into provider messages when image artifacts are referenced. |

---

## Implementation Phases

### Phase 1: Client-side image attachments (UI only)

- Add paste/drop/pick handlers to `ChatInput.tsx`
- Add image preview chips in the composer pinned row
- Create `addImageArtifact` in `useChat.ts` and wire pending image state
- Render image thumbnails inline in `UserMessage.tsx`
- Create `ImageRenderer` and register in the renderer system
- Add image viewer branch to `ArtifactPanel.tsx`
- Extend `ArtifactCard.tsx` with image type support
- Images are stored as base64 data URIs in the artifact `content` field
- Images are referenced but not sent to the LLM (text-only context via `hiddenContext`)

### Phase 2: Provider multimodal integration

- Convert image artifacts to provider-specific vision content blocks (Anthropic, OpenAI, Gemini, Bedrock)
- Detect model vision capability and show warning for non-vision models
- Handle image tokens/cost in the provider adapter layer
- Add image content to the server-side message schema (`engine.go`)

### Phase 3: Server-side storage

- Add `POST /api/images/upload` endpoint that accepts multipart form data
- Store images in configurable blob storage (local disk / S3 / GCS)
- Return a URL reference instead of inline base64
- Migrate artifact `content` from data URI to URL reference
- Add image retrieval endpoint for the ArtifactPanel
- Reduces localStorage pressure and enables larger images

### Phase 4: Polish

- Image zoom/pan controls in ArtifactPanel (pinch-to-zoom, scroll-to-zoom)
- Image annotation (crop, highlight regions) before sending
- Copy image from ArtifactPanel to clipboard
- Accessibility: alt-text input field on image preview chips
- Image compression option for large screenshots (resize before encoding)
- Thumbnail generation for thread history sidebar
