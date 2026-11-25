import React from "react";
import { TooltipProvider } from "@/components/ui/tooltip";
import CollapsedToolsSummary from "@/components/chat/CollapsedToolsSummary";
import ArtifactPanel from "@/components/ArtifactPanel";
import InspectorOverlay from "@/components/InspectorOverlay";
import ImageGenerationSection from "@/components/settings/providers/ImageGenerationSection";
import type {
  AppSettings,
  Artifact,
  ToolCall,
  ToolProgressEvent,
  ToolResultEnvelope,
} from "@/types";
import { IMAGE_GENERATION_TOOL_NAME } from "@/lib/generatedImageArtifacts";

/**
 * Local UI fixture for the generated-image experience (the `generate_image`
 * tool). It mounts the REAL live-transcript path — CollapsedToolsSummary plus
 * the ArtifactPanel inspector — from synthetic tool calls / result envelopes /
 * progress events, exactly like AuditTrailFixture, so the generated-image card,
 * gallery, error states, and inspector are exercised end to end against
 * production renderers.
 *
 * Every fixture image is a real, decodable data URI drawn on an offscreen
 * canvas at module load (correct requested aspect ratio, gradient + label) — no
 * repo binaries, no external URLs. The single deliberately-invalid entry is
 * `image-broken-preview`, whose content is an undecodable data URI to drive the
 * <img> onError fallback.
 */

const now = Date.now();

// ---------------------------------------------------------------------------
// Real image bytes: gradient + label drawn on an offscreen canvas.
// ---------------------------------------------------------------------------

// A 1x1 transparent PNG, used only if a canvas is somehow unavailable at import
// (the fixture only ever renders in a browser, where canvas exists).
const FALLBACK_PNG =
  "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M8AAAMBAQDJ/pLvAAAAAElFTkSuQmCC";

function makeImageDataUri(opts: {
  width: number;
  height: number;
  label: string;
  mime?: "image/png" | "image/jpeg" | "image/webp";
  hueStart?: number;
  hueEnd?: number;
}): string {
  const { width, height, label, mime = "image/png", hueStart = 214, hueEnd = 280 } = opts;
  if (typeof document === "undefined") return FALLBACK_PNG;
  // Bound the raster so 1536px nominal dimensions don't allocate huge canvases;
  // the reported metadata still carries the real requested pixel size.
  const maxSide = 384;
  const scale = Math.min(1, maxSide / Math.max(width, height));
  const w = Math.max(2, Math.round(width * scale));
  const h = Math.max(2, Math.round(height * scale));
  const canvas = document.createElement("canvas");
  canvas.width = w;
  canvas.height = h;
  const ctx = canvas.getContext("2d");
  if (!ctx) return FALLBACK_PNG;
  const grad = ctx.createLinearGradient(0, 0, w, h);
  grad.addColorStop(0, `hsl(${hueStart} 72% 56%)`);
  grad.addColorStop(1, `hsl(${hueEnd} 64% 34%)`);
  ctx.fillStyle = grad;
  ctx.fillRect(0, 0, w, h);
  ctx.fillStyle = "rgba(255,255,255,0.94)";
  ctx.textAlign = "center";
  ctx.textBaseline = "middle";
  ctx.font = `${Math.max(11, Math.round(w / 16))}px sans-serif`;
  ctx.fillText(label, w / 2, h / 2, w - 16);
  try {
    return canvas.toDataURL(mime);
  } catch {
    return FALLBACK_PNG;
  }
}

const LANDSCAPE = { width: 1536, height: 1024 };
const PORTRAIT = { width: 1024, height: 1536 };

// ---------------------------------------------------------------------------
// Artifact / envelope builders
// ---------------------------------------------------------------------------

interface GeneratedImageArtifactInput {
  id: string;
  prompt: string;
  revisedPrompt?: string;
  width: number;
  height: number;
  mime?: "image/png" | "image/jpeg" | "image/webp";
  quality?: string;
  content?: string; // override (broken-preview)
  snakeCase?: boolean;
  hueStart?: number;
  hueEnd?: number;
  toolCallId: string;
  runId: string;
}

function generatedImageArtifact(input: GeneratedImageArtifactInput): Artifact {
  const {
    id,
    prompt,
    revisedPrompt,
    width,
    height,
    mime = "image/png",
    quality = "high",
    content,
    snakeCase = false,
    hueStart,
    hueEnd,
    toolCallId,
    runId,
  } = input;
  const sizeBytes = Math.round(width * height * 1.4);
  const dataUri =
    content ??
    makeImageDataUri({
      width,
      height,
      label: `${width}×${height}`,
      mime,
      hueStart,
      hueEnd,
    });
  // Prove the classifier/normalizer tolerates snake_case spellings on some
  // artifacts and camelCase on others.
  const metadata: Record<string, any> = snakeCase
    ? {
        source: "image_generation",
        generated: true,
        tool_name: IMAGE_GENERATION_TOOL_NAME,
        tool_call_id: toolCallId,
        run_id: runId,
        provider: "openai",
        model: "gpt-image-2",
        prompt,
        revised_prompt: revisedPrompt,
        width,
        height,
        mime_type: mime,
        size_bytes: sizeBytes,
        quality,
        created_at: new Date(now).toISOString(),
      }
    : {
        source: "generated_image",
        generated: true,
        tool_name: IMAGE_GENERATION_TOOL_NAME,
        toolCallId,
        runId,
        provider: "openai",
        model: "gpt-image-2",
        prompt,
        revisedPrompt,
        width,
        height,
        mimeType: mime,
        sizeBytes,
        quality,
        createdAt: new Date(now).toISOString(),
      };
  return {
    id,
    type: "image",
    title: prompt.length > 60 ? `${prompt.slice(0, 57)}…` : prompt,
    content: dataUri,
    metadata,
  };
}

function completedEnvelope(input: {
  artifactId: string;
  toolCallId: string;
  width: number;
  height: number;
  mime?: string;
  revisedPrompt?: string;
  artifactIds?: string[];
}): ToolResultEnvelope {
  const { artifactId, toolCallId, width, height, mime = "image/png", revisedPrompt, artifactIds } = input;
  const envelope: ToolResultEnvelope & { artifact_ids?: string[] } = {
    artifact_id: artifactId,
    tool_name: IMAGE_GENERATION_TOOL_NAME,
    tool_call_id: toolCallId,
    ok: true,
    result_format: "json",
    result: {
      status: "completed",
      mime_type: mime,
      width,
      height,
      size_bytes: Math.round(width * height * 1.4),
      revised_prompt: revisedPrompt,
      _uiStartedAt: now - 42_000,
      _uiEndedAt: now,
    },
    result_metadata: {
      provider: "openai",
      model: "gpt-image-2",
      request_id: `req_${toolCallId}`,
      latency_ms: 42_000,
      operation: "image_generation",
    },
  };
  if (artifactIds && artifactIds.length > 0) envelope.artifact_ids = artifactIds;
  return envelope;
}

function failedEnvelope(input: {
  toolCallId: string;
  errorCategory: string;
  error: string;
  status?: string;
  extraMetadata?: Record<string, unknown>;
}): ToolResultEnvelope {
  const { toolCallId, errorCategory, error, status = "failed", extraMetadata } = input;
  return {
    artifact_id: "",
    tool_name: IMAGE_GENERATION_TOOL_NAME,
    tool_call_id: toolCallId,
    ok: status === "cancelled",
    result_format: "json",
    result: {
      status,
      error: status === "cancelled" ? undefined : error,
      _uiStartedAt: now - 8_000,
      _uiEndedAt: now,
    },
    result_metadata: {
      provider: "openai",
      model: "gpt-image-2",
      ...(status === "cancelled" ? {} : { error_category: errorCategory }),
      ...extraMetadata,
    },
  };
}

function imageCall(id: string, args: Record<string, unknown>, runId?: string): ToolCall {
  const call: any = { id, name: IMAGE_GENERATION_TOOL_NAME, args };
  if (runId) {
    call._uiStartedAt = now - 3_000;
    call._uiRunId = runId;
  }
  return call as ToolCall;
}

function progress(
  callId: string,
  runId: string,
  phase: "requesting" | "generating" | "saving",
  label: string,
): ToolProgressEvent {
  return {
    tool_call_id: callId,
    tool_name: IMAGE_GENERATION_TOOL_NAME,
    provider: "openai",
    run_id: runId,
    phase,
    label,
    timestamp: now,
  };
}

// ---------------------------------------------------------------------------
// Fixture entries
// ---------------------------------------------------------------------------

interface ImageFixture {
  label: string; // data-fixture-label
  title: string;
  description: string;
  call: ToolCall;
  envelope?: ToolResultEnvelope;
  /** Result artifacts, added to artifactsById and toolResultsByCallId. */
  artifacts?: Artifact[];
  progress?: ToolProgressEvent;
  /** Artifact opened by the launch button; falls back to artifacts[0]. */
  launchArtifact?: Artifact;
}

const RUN_QUEUED = "run-img-queued";
const RUN_GENERATING = "run-img-generating";
const RUN_SAVING = "run-img-saving";

// --- Live (executing, no envelope) ---

const queuedCall = imageCall(
  "call_img_queued",
  { prompt: "An isometric city built inside a terrarium", aspect_ratio: "landscape", quality: "high", format: "png" },
  RUN_QUEUED,
);

const generatingCall = imageCall(
  "call_img_generating",
  { prompt: "A koi pond rendered as stained glass", aspect_ratio: "landscape", quality: "high", format: "png" },
  RUN_GENERATING,
);

const savingCall = imageCall(
  "call_img_saving",
  { prompt: "A lighthouse made of origami paper", aspect_ratio: "portrait", quality: "high", format: "png" },
  RUN_SAVING,
);

// --- Completed single (camelCase metadata) ---

const completedArtifact = generatedImageArtifact({
  id: "art-img-completed",
  prompt: "A neon city skyline reflected in rain-slick streets at dusk",
  revisedPrompt: "A neon-lit futuristic city skyline reflected in rain-slick streets at dusk, cinematic wide shot",
  ...LANDSCAPE,
  toolCallId: "call_img_completed",
  runId: "run-img-completed",
  hueStart: 210,
  hueEnd: 300,
});
const completedCall = imageCall("call_img_completed", {
  prompt: "A neon city skyline reflected in rain-slick streets at dusk",
  aspect_ratio: "landscape",
  quality: "high",
  format: "png",
});
const completedEnv = completedEnvelope({
  artifactId: completedArtifact.id,
  toolCallId: "call_img_completed",
  ...LANDSCAPE,
  revisedPrompt: "A neon-lit futuristic city skyline reflected in rain-slick streets at dusk, cinematic wide shot",
});

// --- Completed single (snake_case metadata: mime_type/revised_prompt/size_bytes) ---

const completedSnakeArtifact = generatedImageArtifact({
  id: "art-img-completed-snake",
  prompt: "A watercolor fox curled asleep in a snowy forest",
  revisedPrompt: "A soft watercolor illustration of a fox curled asleep in a quiet snowy forest",
  ...PORTRAIT,
  mime: "image/webp",
  snakeCase: true,
  toolCallId: "call_img_completed_snake",
  runId: "run-img-completed-snake",
  hueStart: 150,
  hueEnd: 210,
});
const completedSnakeCall = imageCall("call_img_completed_snake", {
  prompt: "A watercolor fox curled asleep in a snowy forest",
  aspect_ratio: "portrait",
  quality: "high",
  format: "webp",
});
const completedSnakeEnv = completedEnvelope({
  artifactId: completedSnakeArtifact.id,
  toolCallId: "call_img_completed_snake",
  ...PORTRAIT,
  mime: "image/webp",
  revisedPrompt: "A soft watercolor illustration of a fox curled asleep in a quiet snowy forest",
});

// --- Gallery (4 artifacts, mixed portrait / landscape) ---

const galleryArtifacts: Artifact[] = [
  generatedImageArtifact({ id: "art-img-gallery-1", prompt: "Gallery tile 1 — landscape", ...LANDSCAPE, toolCallId: "call_img_gallery", runId: "run-img-gallery", hueStart: 20, hueEnd: 60 }),
  generatedImageArtifact({ id: "art-img-gallery-2", prompt: "Gallery tile 2 — portrait", ...PORTRAIT, toolCallId: "call_img_gallery", runId: "run-img-gallery", hueStart: 260, hueEnd: 320 }),
  generatedImageArtifact({ id: "art-img-gallery-3", prompt: "Gallery tile 3 — landscape", ...LANDSCAPE, toolCallId: "call_img_gallery", runId: "run-img-gallery", hueStart: 120, hueEnd: 170 }),
  generatedImageArtifact({ id: "art-img-gallery-4", prompt: "Gallery tile 4 — portrait", ...PORTRAIT, toolCallId: "call_img_gallery", runId: "run-img-gallery", hueStart: 330, hueEnd: 20 }),
];
const galleryCall = imageCall("call_img_gallery", {
  prompt: "Four studies of a paper crane in different seasons",
  aspect_ratio: "auto",
  quality: "high",
  format: "png",
});
const galleryEnv = completedEnvelope({
  artifactId: galleryArtifacts[0].id,
  toolCallId: "call_img_gallery",
  ...LANDSCAPE,
  artifactIds: galleryArtifacts.map((a) => a.id),
});

// --- Broken preview (undecodable data URI → img onError) ---

const brokenArtifact = generatedImageArtifact({
  id: "art-img-broken",
  prompt: "A corrupted transmission from a deep-space probe",
  ...LANDSCAPE,
  content: "data:image/png;base64,broken",
  toolCallId: "call_img_broken",
  runId: "run-img-broken",
});
const brokenCall = imageCall("call_img_broken", {
  prompt: "A corrupted transmission from a deep-space probe",
  aspect_ratio: "landscape",
  quality: "high",
  format: "png",
});
const brokenEnv = completedEnvelope({
  artifactId: brokenArtifact.id,
  toolCallId: "call_img_broken",
  ...LANDSCAPE,
});

// --- Error / terminal states ---

const moderationCall = imageCall("call_img_moderation", { prompt: "A blocked prompt", aspect_ratio: "square", quality: "high", format: "png" });
const rateLimitedCall = imageCall("call_img_rate_limited", { prompt: "A busy prompt", aspect_ratio: "landscape", quality: "high", format: "png" });
const providerCall = imageCall("call_img_provider", { prompt: "A prompt during an outage", aspect_ratio: "landscape", quality: "high", format: "png" });
const authCall = imageCall("call_img_auth", { prompt: "A prompt with a bad target", aspect_ratio: "landscape", quality: "high", format: "png" });
const cancelledCall = imageCall("call_img_cancelled", { prompt: "A cancelled prompt", aspect_ratio: "portrait", quality: "high", format: "png" });

// Placeholder image an executing/error launch button can open in the inspector.
function placeholderArtifact(id: string, prompt: string, dims: { width: number; height: number }): Artifact {
  return generatedImageArtifact({
    id,
    prompt,
    ...dims,
    toolCallId: `${id}-preview`,
    runId: `${id}-preview`,
    hueStart: 200,
    hueEnd: 250,
  });
}

const fixtures: ImageFixture[] = [
  {
    label: "image-queued",
    title: "Queued",
    description: "Executing call, no progress event yet — the queued placeholder reserves a stable landscape frame.",
    call: queuedCall,
    launchArtifact: placeholderArtifact("art-img-queued-preview", "An isometric city built inside a terrarium", LANDSCAPE),
  },
  {
    label: "image-generating",
    title: "Generating",
    description: "Executing call with a `generating` progress event and elapsed shimmer.",
    call: generatingCall,
    progress: progress("call_img_generating", RUN_GENERATING, "generating", "Generating image"),
    launchArtifact: placeholderArtifact("art-img-generating-preview", "A koi pond rendered as stained glass", LANDSCAPE),
  },
  {
    label: "image-saving",
    title: "Saving",
    description: "Executing call in the `saving` stage (bytes returned, persisting artifact).",
    call: savingCall,
    progress: progress("call_img_saving", RUN_SAVING, "saving", "Saving image"),
    launchArtifact: placeholderArtifact("art-img-saving-preview", "A lighthouse made of origami paper", PORTRAIT),
  },
  {
    label: "image-completed",
    title: "Completed (single)",
    description: "Completed landscape image with camelCase metadata, revised prompt, and provider info.",
    call: completedCall,
    envelope: completedEnv,
    artifacts: [completedArtifact],
  },
  {
    label: "image-completed-snake",
    title: "Completed (snake_case metadata)",
    description: "Completed portrait WebP whose metadata uses snake_case spellings (mime_type / revised_prompt / size_bytes).",
    call: completedSnakeCall,
    envelope: completedSnakeEnv,
    artifacts: [completedSnakeArtifact],
  },
  {
    label: "image-gallery",
    title: "Gallery (4 tiles)",
    description: "One tool result whose ordered artifact_ids render as a 4-tile gallery, mixed portrait/landscape.",
    call: galleryCall,
    envelope: galleryEnv,
    artifacts: galleryArtifacts,
  },
  {
    label: "image-moderation",
    title: "Moderation blocked",
    description: "Non-retryable moderation block — offers Edit prompt, never Retry, and creates no artifact.",
    call: moderationCall,
    envelope: failedEnvelope({
      toolCallId: "call_img_moderation",
      errorCategory: "moderation_blocked",
      error: "Request was blocked by the content moderation system",
    }),
    launchArtifact: placeholderArtifact("art-img-moderation-preview", "A blocked prompt", LANDSCAPE),
  },
  {
    label: "image-rate-limited",
    title: "Rate limited",
    description: "Transient rate-limit failure with retry timing — manual retry allowed.",
    call: rateLimitedCall,
    envelope: failedEnvelope({
      toolCallId: "call_img_rate_limited",
      errorCategory: "rate_limited",
      error: "Rate limit exceeded. Please retry after 30 seconds.",
      extraMetadata: { retry_after_seconds: 30 },
    }),
    launchArtifact: placeholderArtifact("art-img-rate-limited-preview", "A busy prompt", LANDSCAPE),
  },
  {
    label: "image-provider-unavailable",
    title: "Provider unavailable",
    description: "Transient provider outage (429/5xx) after one bounded internal retry — manual retry allowed.",
    call: providerCall,
    envelope: failedEnvelope({
      toolCallId: "call_img_provider",
      errorCategory: "provider_unavailable",
      error: "The image provider is temporarily unavailable. Try again shortly.",
    }),
    launchArtifact: placeholderArtifact("art-img-provider-preview", "A prompt during an outage", LANDSCAPE),
  },
  {
    label: "image-auth",
    title: "Target invalid",
    description: "Auth/target failure — deep-links to Image generation settings.",
    call: authCall,
    envelope: failedEnvelope({
      toolCallId: "call_img_auth",
      errorCategory: "target_invalid",
      error: "The configured image target is invalid or unauthorized.",
    }),
    launchArtifact: placeholderArtifact("art-img-auth-preview", "A prompt with a bad target", LANDSCAPE),
  },
  {
    label: "image-cancelled",
    title: "Cancelled",
    description: "Quiet terminal state — no error toast, no durable artifact.",
    call: cancelledCall,
    envelope: failedEnvelope({
      toolCallId: "call_img_cancelled",
      errorCategory: "cancelled",
      error: "",
      status: "cancelled",
    }),
    launchArtifact: placeholderArtifact("art-img-cancelled-preview", "A cancelled prompt", PORTRAIT),
  },
  {
    label: "image-broken-preview",
    title: "Broken preview",
    description: "Completed envelope whose artifact content is an undecodable data URI — drives the <img> onError fallback.",
    call: brokenCall,
    envelope: brokenEnv,
    artifacts: [brokenArtifact],
  },
];

// ---------------------------------------------------------------------------
// Shared maps consumed by CollapsedToolsSummary
// ---------------------------------------------------------------------------

const artifactsById: Record<string, Artifact> = {};
for (const f of fixtures) {
  for (const a of f.artifacts || []) artifactsById[a.id] = a;
  if (f.launchArtifact) artifactsById[f.launchArtifact.id] = f.launchArtifact;
}

const toolResultsByCallId = new Map<
  string,
  { envelope?: ToolResultEnvelope; artifact?: Artifact }
>();
for (const f of fixtures) {
  if (f.envelope) {
    toolResultsByCallId.set(f.call.id, { envelope: f.envelope, artifact: f.artifacts?.[0] });
  }
}

const toolProgressByKey: Record<string, ToolProgressEvent> = {};
for (const f of fixtures) {
  if (f.progress) {
    const runId = (f.call as any)._uiRunId as string | undefined;
    if (runId) toolProgressByKey[`${runId}:${f.call.id}`] = f.progress;
  }
}

const emptySettings = {} as AppSettings;

const fixtureSettings: AppSettings = {
  provider: { providerId: "openai", model: "gpt-4o" },
  compatibleProfiles: [],
  systemPrompt: "",
  providerConfigs: [
    {
      id: "fixture-openai",
      displayName: "Production OpenAI",
      providerId: "openai",
      model: "gpt-4o",
    },
  ],
  imageGeneration: {
    enabled: false,
    providerId: "fixture-openai",
    model: "gpt-image-2",
    defaults: { quality: "high", size: "1536x1024", format: "png" },
    maxImagesPerRun: 1,
  },
} as AppSettings;

function launchArtifactFor(f: ImageFixture): Artifact | undefined {
  return f.launchArtifact || f.artifacts?.[0];
}

export default function ImageGenerationFixture() {
  const firstInspectable = fixtures.map(launchArtifactFor).find(Boolean) as Artifact;
  const [activeArtifact, setActiveArtifact] = React.useState<Artifact>(firstInspectable);
  const [isInspectorOpen, setIsInspectorOpen] = React.useState(false);
  const [pinnedArtifactIds, setPinnedArtifactIds] = React.useState<string[]>([]);
  const [settings, setSettings] = React.useState<AppSettings>(fixtureSettings);

  const openArtifact = React.useCallback((artifact: Artifact) => {
    setActiveArtifact(artifact);
    setIsInspectorOpen(true);
  }, []);
  const closeInspector = React.useCallback(() => setIsInspectorOpen(false), []);
  // Present so the card renders its "Use in chat" affordance (the card gates it
  // on onPinArtifact); toggling keeps the pinned state visible in the fixture.
  const pinArtifact = React.useCallback((artifact: Artifact) => {
    setPinnedArtifactIds((ids) =>
      ids.includes(artifact.id) ? ids.filter((id) => id !== artifact.id) : [...ids, artifact.id],
    );
  }, []);

  return (
    <TooltipProvider>
      <div className="flex h-screen min-w-0 bg-background text-foreground">
        <main className="flex min-w-0 flex-1 flex-col gap-4 overflow-auto p-6">
          <div className="max-w-3xl">
            <p className="text-xs uppercase tracking-wide text-muted-foreground">
              Local UI fixture
            </p>
            <h1 className="mt-1 text-xl font-semibold">Image Generation</h1>
            <p className="mt-2 text-sm text-muted-foreground">
              Synthetic <code>generate_image</code> tool calls across every card state — queued,
              generating, saving, completed (single + gallery), and the moderation-blocked,
              rate-limited, provider-unavailable, target-invalid, cancelled, and broken-preview
              terminal states — rendered through the real CollapsedToolsSummary and ArtifactPanel.
              Every image is a decodable canvas-drawn data URI; only “Broken preview” is
              intentionally invalid.
            </p>
          </div>

          {fixtures.map((f) => (
            <section
              key={f.label}
              data-fixture-card={f.label}
              className="max-w-3xl rounded-md border border-border bg-card/40 p-4"
            >
              <div className="mb-3">
                <p className="text-xs uppercase tracking-wide text-muted-foreground">{f.title}</p>
                <p className="mt-1 text-sm text-muted-foreground">{f.description}</p>
              </div>
              <CollapsedToolsSummary
                toolCalls={[f.call]}
                isLastMessage
                artifactsById={artifactsById}
                toolResultsByCallId={toolResultsByCallId}
                toolProgressByKey={toolProgressByKey}
                messageIdentityKey={`fixture:image:${f.label}`}
                onOpenArtifact={openArtifact}
                onPinArtifact={pinArtifact}
                pinnedArtifactIds={pinnedArtifactIds}
              />
            </section>
          ))}

          <section className="max-w-3xl">
            <p className="mb-2 text-xs uppercase tracking-wide text-muted-foreground">
              Open in inspector
            </p>
            <div className="flex flex-wrap gap-2">
              {fixtures.map((f) => {
                const artifact = launchArtifactFor(f);
                const active = !!artifact && artifact.id === activeArtifact?.id;
                return (
                  <button
                    key={f.label}
                    type="button"
                    onClick={() => artifact && openArtifact(artifact)}
                    aria-pressed={active}
                    className={[
                      "rounded-md border px-3 py-1.5 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1",
                      active
                        ? "border-foreground/40 bg-muted text-foreground"
                        : "border-border bg-background text-muted-foreground hover:bg-muted hover:text-foreground",
                    ].join(" ")}
                    data-fixture-label={f.label}
                  >
                    {f.label}
                  </button>
                );
              })}
            </div>
          </section>

          <section
            data-fixture-settings="image-generation"
            className="max-w-3xl rounded-md border border-border bg-card/40 p-4"
          >
            <p className="mb-3 text-xs uppercase tracking-wide text-muted-foreground">
              Settings integration
            </p>
            <ImageGenerationSection
              localSettings={settings}
              setLocalSettings={setSettings}
            />
          </section>
        </main>

        {isInspectorOpen && activeArtifact && (
          <InspectorOverlay onClose={closeInspector}>
            <ArtifactPanel
              artifact={activeArtifact}
              settings={emptySettings}
              onClose={closeInspector}
            />
          </InspectorOverlay>
        )}
      </div>
    </TooltipProvider>
  );
}
