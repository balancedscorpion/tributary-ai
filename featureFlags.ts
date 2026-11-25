export const CAPABILITY_UX_DEFAULTS = {
  report: false,
  editorV2: false,
  controls: false,
  preflight: false,
  composerTarget: false,
} as const;

export const FEATURE_FLAGS = {
  providersTab: {
    /**
     * Show legacy first-party providers (OpenAI Responses / Anthropic / Gemini).
     * Default is off to keep the UI focused on a single OpenAI-compatible custom endpoint.
     */
    showLegacyProviders: true,

    /**
     * Within the OpenAI-compatible editor, show the "OpenAI Compatible" option in addition
     * to the always-available "Custom Provider" option.
     *
     * Currently off: only show the custom provider option.
     */
    showOpenAICompatibleOption: false,

    /**
     * Show third-party OpenAI-compatible presets (Groq, Together, Fireworks, OpenRouter).
     * Default is off.
     */
    showCompatiblePresets: false,

    /**
     * Allow creating/deleting multiple OpenAI-compatible profiles in the UI.
     * Default is off; the UI only exposes the supported options above.
     */
    allowMultipleCompatibleProfiles: false,
  },
  capabilityUx: CAPABILITY_UX_DEFAULTS,
} as const;
