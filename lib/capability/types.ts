import type { ProviderConfig } from '@/types';

/**
 * A documented value with an escape hatch for server additions. Capability
 * keys and registry metadata are intentionally open-ended: the UI must render
 * server-provided metadata instead of requiring a frontend release per key.
 */
export type ExtensibleString<Known extends string> = Known | (string & {});

export type FeatureKey = string;

export const V1_FEATURE_KEYS = [
  'text_streaming',
  'text_nonstreaming',
  'system_instructions',
  'custom_tools',
  'tool_choice',
  'strict_tool_schemas',
  'parallel_tools',
  'tool_state_preservation',
  'images',
  'documents',
  'hosted_web_search',
  'hosted_code_execution',
  'hosted_computer_use',
  'provider_generated_files',
  'reasoning_request_control',
  'thinking_display',
  'thinking_state_preservation',
  'structured_output',
  'citations',
  'native_compaction',
  'session_continuation',
  'prompt_caching',
  'cache_reporting',
  'token_counting',
  'context_limits',
  'rich_usage',
  'safety_controls',
] as const;

export type KnownV1FeatureKey = (typeof V1_FEATURE_KEYS)[number];

export const V1_CAPABILITY_PROBES = [
  'text_streaming',
  'system_instructions',
  'custom_tools',
  'images',
  'documents',
  'hosted_web_search',
  'hosted_code_execution',
  'thinking_state_preservation',
  'structured_output',
  'native_compaction',
  'citations',
  'cache_reporting',
] as const satisfies readonly FeatureKey[];

export type ProviderFamily = ExtensibleString<
  'openai' | 'anthropic' | 'gemini' | 'bedrock' | 'compatible'
>;

export type ApiSurface = ExtensibleString<
  | 'responses'
  | 'messages'
  | 'generate_content'
  | 'invoke_stream'
  | 'converse_stream'
  | 'chat_completions'
>;

export interface CapabilityTarget {
  connectionId: string | null;
  providerFamily: ProviderFamily;
  apiSurface: ApiSurface;
  endpointFamily: string;
  model: string;
  region: string | null;
  adapterVersion: string;
}

export type FeatureGroup = ExtensibleString<
  'core' | 'tools' | 'inputs' | 'hosted' | 'reasoning' | 'output' | 'state' | 'ops' | 'safety'
>;

export interface FeatureMeta {
  label: string;
  group: FeatureGroup;
  headline: boolean;
  order: number;
}

export type CapabilityClass = ExtensibleString<'standard' | 'explicit_consent'>;
export type RiskTag = ExtensibleString<'billable' | 'external_egress' | 'code_execution'>;
export type CapabilitySupportState = ExtensibleString<'supported' | 'unsupported' | 'conditional' | 'unknown'>;
export type CapabilityMode = ExtensibleString<'native' | 'emulated' | 'reduced'>;
export type CapabilityEffectiveState = ExtensibleString<'on' | 'off' | 'blocked' | 'unavailable'>;
export type CapabilityControlSetting = ExtensibleString<'auto' | 'on' | 'off'>;

export interface CapabilityControlResolution {
  setting: CapabilityControlSetting;
  controllable: boolean;
}

export interface CapabilityPermissionResolution {
  required: boolean;
  granted: boolean;
  policyRef: string | null;
}

export interface CapabilityCondition {
  code: string;
  text: string;
}

export interface CapabilityReason {
  code: string;
  text: string;
}

export interface CapabilityLimits {
  mimeTypes: string[] | null;
  maxBytes: number | null;
  maxCount: number | null;
  requestBudgetBytes: number | null;
  contextTokens: number | null;
  maxOutputTokens: number | null;
}

export type CapabilityEvidenceKind = ExtensibleString<'catalog' | 'probe' | 'discovery' | 'docs' | 'imported'>;
export type CapabilityEvidenceStaleReason = ExtensibleString<'fingerprint' | 'age'>;

export interface CapabilityEvidence {
  kind: CapabilityEvidenceKind;
  verifiedAt: string | null;
  ref: string | null;
  stale: boolean;
  staleReason: CapabilityEvidenceStaleReason | null;
  /** Forward-compatible evidence-specific fields supplied by the server. */
  [key: string]: unknown;
}

export interface FeatureResolution {
  meta: FeatureMeta;
  class: CapabilityClass;
  riskTags: RiskTag[];
  state: CapabilitySupportState;
  mode: CapabilityMode | null;
  effective: CapabilityEffectiveState;
  control: CapabilityControlResolution | null;
  permission: CapabilityPermissionResolution | null;
  conditions: CapabilityCondition[];
  reasons: CapabilityReason[];
  limits: CapabilityLimits | null;
  detail: Record<string, unknown> | null;
  evidence: CapabilityEvidence[];
}

export interface CapabilityReport {
  target: CapabilityTarget;
  fingerprint: string;
  catalogVersion: string;
  generatedAt: string;
  features: Record<FeatureKey, FeatureResolution>;
  /** Allows non-breaking report-level additions from a newer registry. */
  [key: string]: unknown;
}

export interface FeatureRegistryEntry extends FeatureMeta {
  key: FeatureKey;
  class: CapabilityClass;
  riskTags: RiskTag[];
}

export interface FeatureRegistry {
  version: string;
  features: Record<FeatureKey, FeatureRegistryEntry>;
}

export type ModelDiscoverySource = ExtensibleString<'provider_api' | 'curated' | 'unavailable'>;
export type ModelLifecycle = ExtensibleString<'recommended' | 'available' | 'preview' | 'deprecated' | 'shutdown'>;

export interface DiscoveredModel {
  id: string;
  label: string | null;
  lifecycle: ModelLifecycle;
  deprecationDate: string | null;
  replacement: string | null;
  contextTokens: number | null;
  maxOutputTokens: number | null;
  [key: string]: unknown;
}

export interface ModelDiscoveryResult {
  source: ModelDiscoverySource;
  fetchedAt: string;
  error: string | null;
  models: DiscoveredModel[];
  [key: string]: unknown;
}

export type ProviderCheckTier = 'quick' | 'capability';

export interface ProviderCheckRequest {
  config?: ProviderConfig;
  connectionId?: string;
  model?: string;
  tier: ProviderCheckTier;
  probes?: FeatureKey[];
  consentAcknowledged?: boolean;
  /** Kept for compatibility with the existing quick-check endpoint. */
  endpoint?: string;
}

export type ProviderCheckVerdict = ExtensibleString<'verified' | 'unsupported' | 'failed' | 'skipped'>;

export interface ProviderCheckProbeResult {
  probe: FeatureKey | 'transport';
  verdict: ProviderCheckVerdict;
  reason: string | null;
  message: string;
  requestId: string | null;
  durationMs: number;
  [key: string]: unknown;
}

export interface ProviderCheckResult {
  ok: boolean;
  fingerprint: string;
  results: ProviderCheckProbeResult[];
  [key: string]: unknown;
}

export interface ThreadTarget {
  connectionId: string;
  model: string;
}

export interface AttachmentPolicy {
  id: string;
  class: string;
  action: string;
  apiSurface: string;
  createdAt: number;
  [key: string]: unknown;
}

export type PlanIssueFixKind =
  | 'open_permissions'
  | 'switch_model'
  | 'remove_attachment'
  | 'recheck';

export interface PlanIssue {
  issueId: string;
  code: string;
  text: string;
  feature: FeatureKey | null;
  attachmentId: string | null;
  fix: { kind: PlanIssueFixKind } | null;
  [key: string]: unknown;
}

export type PlanChoiceScope = 'run' | 'thread' | 'connection';

export interface PlanChoiceOption {
  id: string;
  label: string;
  consequence: string;
  policyClass: string | null;
  /** Set when choosing this option switches the run to a different API surface. */
  apiSurface?: string;
  [key: string]: unknown;
}

export interface PlanChoiceIssue extends PlanIssue {
  options: PlanChoiceOption[];
  allowedScopes: PlanChoiceScope[];
}

export interface PreflightAttachmentRequirement {
  id: string;
  mime: string;
  sizeBytes: number;
}

export interface PreflightRequirements {
  attachments: PreflightAttachmentRequirement[];
  features: FeatureKey[];
}

export interface PreflightRequest {
  target: ThreadTarget;
  requirements: PreflightRequirements;
}

export type PreflightStatus = 'clean' | 'notices' | 'choices_required' | 'blocked';

export interface PreflightResult {
  planId: string;
  requirementsHash: string;
  status: PreflightStatus;
  blockers: PlanIssue[];
  choices: PlanChoiceIssue[];
  notices: PlanIssue[];
  staleEvidence: boolean;
  plan: CapabilityReport;
  [key: string]: unknown;
}

export interface PlanChoiceSelection {
  optionId: string;
  scope: PlanChoiceScope;
}

export type RunPlanLifecycleStatus = ExtensibleString<'draft' | 'finalized'>;

export interface RunPlanChoiceSelection {
  issueId: string;
  optionId: string;
  scope: PlanChoiceScope;
  /** RFC 3339 timestamp assigned by the server at finalization. */
  selectedAt: string;
  [key: string]: unknown;
}

/**
 * Persisted plan DTO from GET /api/runs/{runId}/plan. Mirrors the backend's
 * store.RunPlan JSON exactly: the capability report lives under `plan`,
 * selections are an array, and timestamps are epoch milliseconds.
 */
export interface RunPlanRecord {
  planId: string;
  requirementsHash: string;
  status: PreflightStatus;
  blockers: PlanIssue[];
  choices: PlanChoiceIssue[];
  notices: PlanIssue[];
  staleEvidence: boolean;
  plan: CapabilityReport;
  /** Epoch milliseconds. */
  expiresAt: number;
  lifecycleStatus: RunPlanLifecycleStatus;
  target: ThreadTarget;
  choiceSelections: RunPlanChoiceSelection[];
  /** Epoch milliseconds. */
  createdAt: number;
  /** Epoch milliseconds; present once the plan is finalized. */
  finalizedAt?: number;
  runId?: string;
  [key: string]: unknown;
}

export type PlanUpdateKind = ExtensibleString<
  // `continuation_loss` is the canonical preflight/backend code;
  // `continuation_lost` is accepted for legacy/forward compatibility.
  'surface_switch' | 'fidelity_reduction' | 'feature_unavailable' | 'continuation_loss' | 'continuation_lost'
>;

export interface PlanUpdateEventData {
  planId: string;
  change: {
    kind: PlanUpdateKind;
    feature: FeatureKey | null;
    attachmentId: string | null;
    text: string;
    [key: string]: unknown;
  };
}

export interface CapabilityApiErrorBody {
  error?: unknown;
  message?: unknown;
  code?: unknown;
  [key: string]: unknown;
}

export function isKnownV1FeatureKey(value: string): value is KnownV1FeatureKey {
  return (V1_FEATURE_KEYS as readonly string[]).includes(value);
}
