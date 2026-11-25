import React, { useCallback, useMemo } from 'react';
import { ImageIcon, AlertTriangle, Info } from 'lucide-react';
import { Label } from '@/components/ui/label';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type {
  AppSettings,
  ImageGenerationDefaults,
  ImageGenerationSettings,
  ProviderConfig,
} from '@/types';

interface ImageGenerationSectionProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
}

const QUALITY_OPTIONS = ['auto', 'low', 'medium', 'high'];
const FORMAT_OPTIONS = ['png', 'jpeg', 'webp'];

const isOpenAiFamily = (cfg: ProviderConfig): boolean =>
  cfg.providerId === 'openai' || cfg.providerId === 'compatible';

const imageTargetLabel = (cfg: ProviderConfig): string =>
  cfg.providerId === 'openai' ? 'OpenAI credentials' : 'Compatible endpoint';

const DEFAULTS: Required<Pick<ImageGenerationSettings, 'model' | 'maxImagesPerRun'>> & {
  defaults: Required<ImageGenerationDefaults>;
} = {
  model: 'gpt-image-2',
  maxImagesPerRun: 1,
  defaults: { quality: 'auto', size: 'auto', format: 'png' },
};

/**
 * App-level image-generation target. It reuses an existing OpenAI-family
 * provider's stored credentials rather than adding its own connection or key.
 * There is no image-generation test endpoint, so readiness is reported as
 * derived config state only — never a fabricated "verified" result.
 */
const ImageGenerationSection: React.FC<ImageGenerationSectionProps> = ({
  localSettings,
  setLocalSettings,
}) => {
  const settings = localSettings.imageGeneration || {};
  const enabled = settings.enabled ?? false;
  const providerRef = typeof settings.providerId === 'string' ? settings.providerId : null;
  const model = settings.model ?? DEFAULTS.model;
  const defaults = settings.defaults || {};
  const quality = defaults.quality ?? DEFAULTS.defaults.quality;
  const size = defaults.size ?? DEFAULTS.defaults.size;
  const format = defaults.format ?? DEFAULTS.defaults.format;
  const maxImagesPerRun = settings.maxImagesPerRun ?? DEFAULTS.maxImagesPerRun;

  const configs = localSettings.providerConfigs || [];
  const familyConfigs = useMemo(() => configs.filter(isOpenAiFamily), [configs]);
  const hasFamilyConfigs = familyConfigs.length > 0;

  // The reference may dangle if the target provider was deleted; resolve it
  // explicitly and surface an invalid state rather than silently falling back.
  const resolvedTarget = useMemo(
    () => (providerRef ? familyConfigs.find((c) => c.id === providerRef) || null : null),
    [providerRef, familyConfigs],
  );
  const targetMissing = Boolean(providerRef) && !resolvedTarget;

  const update = useCallback(
    (patch: Partial<ImageGenerationSettings>) => {
      setLocalSettings((prev) => ({
        ...prev,
        imageGeneration: { ...(prev.imageGeneration || {}), ...patch },
      }));
    },
    [setLocalSettings],
  );

  const updateDefaults = useCallback(
    (patch: Partial<ImageGenerationDefaults>) => {
      setLocalSettings((prev) => {
        const current = prev.imageGeneration || {};
        return {
          ...prev,
          imageGeneration: {
            ...current,
            defaults: { ...(current.defaults || {}), ...patch },
          },
        };
      });
    },
    [setLocalSettings],
  );

  const handleMaxImages = useCallback(
    (raw: string) => {
      const parsed = Number.parseInt(raw, 10);
      const value = Number.isFinite(parsed) ? Math.min(4, Math.max(1, parsed)) : 1;
      update({ maxImagesPerRun: value });
    },
    [update],
  );

  const detailDisabled = !enabled;

  return (
    <div className="rounded-lg border border-border bg-card/30 p-3 space-y-3">
      <div className="flex items-start justify-between gap-3">
        <div className="flex items-start gap-2 min-w-0">
          <div className="w-7 h-7 rounded-md bg-muted border border-border flex items-center justify-center text-muted-foreground shrink-0">
            <ImageIcon size={14} />
          </div>
          <div className="min-w-0">
            <div className="text-sm font-medium">Image generation</div>
            <p className="text-[11px] text-muted-foreground mt-0.5">
              Let any chat model call <span className="font-mono">generate_image</span>. Images
              are generated with the selected provider&apos;s credentials.
            </p>
          </div>
        </div>
        <Switch
          checked={enabled}
          onCheckedChange={(checked) => update({ enabled: checked })}
          aria-label="Enable image generation"
        />
      </div>

      {!hasFamilyConfigs && (
        <div className="flex items-start gap-1.5 rounded-md border border-border bg-muted/30 p-2 text-[11px] text-muted-foreground">
          <Info size={12} className="mt-0.5 shrink-0" />
          <span>Add an OpenAI or compatible provider first to use as the image target.</span>
        </div>
      )}

      <div className={detailDisabled ? 'space-y-3 opacity-50 pointer-events-none' : 'space-y-3'}>
        <div className="space-y-1.5">
          <Label className="text-[11px] font-medium text-muted-foreground">Image target</Label>
          <Select
            value={resolvedTarget?.id ?? ''}
            onValueChange={(value) => update({ providerId: value })}
            disabled={detailDisabled || !hasFamilyConfigs}
          >
            <SelectTrigger className="h-8 text-xs">
              <SelectValue placeholder="Select an OpenAI-family provider" />
            </SelectTrigger>
            <SelectContent>
              {familyConfigs.map((cfg) => (
                <SelectItem key={cfg.id} value={cfg.id} className="text-xs">
                  {cfg.displayName}
                  <span className="text-muted-foreground"> · {imageTargetLabel(cfg)}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          {targetMissing && (
            <div className="flex items-start gap-1.5 rounded-md border border-amber-500/40 bg-amber-500/10 p-2 text-[11px] text-amber-500">
              <AlertTriangle size={12} className="mt-0.5 shrink-0" />
              <span>
                The selected provider is no longer available. Pick another OpenAI-family target.
              </span>
            </div>
          )}

          {!targetMissing && hasFamilyConfigs && (
            <p className="text-[11px] text-muted-foreground">
              {resolvedTarget
                ? 'Target configured — not verified. Generation is validated at first use.'
                : 'Select a provider whose credentials will be reused for image generation.'}
            </p>
          )}
        </div>

        <div className="space-y-1.5">
          <Label className="text-[11px] font-medium text-muted-foreground" htmlFor="image-gen-model">
            Image model
          </Label>
          <Input
            id="image-gen-model"
            value={model}
            onChange={(e) => update({ model: e.target.value })}
            placeholder="gpt-image-2"
            className="h-8 text-xs font-mono"
            disabled={detailDisabled}
          />
        </div>

        <div className="grid grid-cols-3 gap-2">
          <div className="space-y-1.5">
            <Label className="text-[11px] font-medium text-muted-foreground">Quality</Label>
            <Select
              value={quality}
              onValueChange={(value) => updateDefaults({ quality: value })}
              disabled={detailDisabled}
            >
              <SelectTrigger className="h-8 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {QUALITY_OPTIONS.map((opt) => (
                  <SelectItem key={opt} value={opt} className="text-xs">
                    {opt}
                  </SelectItem>
                ))}
                {!QUALITY_OPTIONS.includes(quality) && (
                  <SelectItem value={quality} className="text-xs">
                    {quality}
                  </SelectItem>
                )}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1.5">
            <Label className="text-[11px] font-medium text-muted-foreground" htmlFor="image-gen-size">
              Size
            </Label>
            <Input
              id="image-gen-size"
              value={size}
              onChange={(e) => updateDefaults({ size: e.target.value })}
              placeholder="auto"
              className="h-8 text-xs font-mono"
              disabled={detailDisabled}
            />
          </div>

          <div className="space-y-1.5">
            <Label className="text-[11px] font-medium text-muted-foreground">Format</Label>
            <Select
              value={format}
              onValueChange={(value) => updateDefaults({ format: value })}
              disabled={detailDisabled}
            >
              <SelectTrigger className="h-8 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {FORMAT_OPTIONS.map((opt) => (
                  <SelectItem key={opt} value={opt} className="text-xs">
                    {opt}
                  </SelectItem>
                ))}
                {!FORMAT_OPTIONS.includes(format) && (
                  <SelectItem value={format} className="text-xs">
                    {format}
                  </SelectItem>
                )}
              </SelectContent>
            </Select>
          </div>
        </div>

        <div className="space-y-1.5">
          <Label
            className="text-[11px] font-medium text-muted-foreground"
            htmlFor="image-gen-max-per-run"
          >
            Max images per run
          </Label>
          <Input
            id="image-gen-max-per-run"
            type="number"
            min={1}
            max={4}
            step={1}
            value={maxImagesPerRun}
            onChange={(e) => handleMaxImages(e.target.value)}
            className="h-8 w-20 text-xs"
            disabled={detailDisabled}
          />
          <p className="text-[11px] text-muted-foreground/70">
            MVP produces one image per call; up to 4 is reserved for variant support.
          </p>
        </div>
      </div>
    </div>
  );
};

export default ImageGenerationSection;
