package capabilities

const (
	CatalogVersion        = "2026-07-11.v1"
	DefaultAdapterVersion = "go-provider-adapters/2026-07-11"
)

type SupportState string

const (
	StateSupported   SupportState = "supported"
	StateUnsupported SupportState = "unsupported"
	StateConditional SupportState = "conditional"
	StateUnknown     SupportState = "unknown"
)

type CapabilityMode string

const (
	ModeNative   CapabilityMode = "native"
	ModeEmulated CapabilityMode = "emulated"
	ModeReduced  CapabilityMode = "reduced"
)

type EffectiveState string

const (
	EffectiveOn          EffectiveState = "on"
	EffectiveOff         EffectiveState = "off"
	EffectiveBlocked     EffectiveState = "blocked"
	EffectiveUnavailable EffectiveState = "unavailable"
)

type ControlSetting string

const (
	ControlAuto ControlSetting = "auto"
	ControlOn   ControlSetting = "on"
	ControlOff  ControlSetting = "off"
)

type TargetDescriptor struct {
	ConnectionID   *string `json:"connectionId"`
	ProviderFamily string  `json:"providerFamily"`
	APISurface     string  `json:"apiSurface"`
	EndpointFamily string  `json:"endpointFamily"`
	Model          string  `json:"model"`
	ModelPattern   string  `json:"modelPattern,omitempty"`
	Region         *string `json:"region"`
	AdapterVersion string  `json:"adapterVersion"`
}

type CapabilityReport struct {
	Target         TargetDescriptor                 `json:"target"`
	Fingerprint    string                           `json:"fingerprint"`
	CatalogVersion string                           `json:"catalogVersion"`
	GeneratedAt    string                           `json:"generatedAt"`
	Features       map[FeatureKey]FeatureResolution `json:"features"`
}

type FeatureResolution struct {
	Meta       FeatureMeta             `json:"meta"`
	Class      CapabilityClass         `json:"class"`
	RiskTags   []RiskTag               `json:"riskTags"`
	State      SupportState            `json:"state"`
	Mode       *CapabilityMode         `json:"mode"`
	Effective  EffectiveState          `json:"effective"`
	Control    *CapabilityControl      `json:"control"`
	Permission *CapabilityPermission   `json:"permission"`
	Conditions []CapabilityExplanation `json:"conditions"`
	Reasons    []CapabilityExplanation `json:"reasons"`
	Limits     *CapabilityLimits       `json:"limits"`
	Detail     map[string]any          `json:"detail"`
	Evidence   []CapabilityEvidence    `json:"evidence"`
}

type CapabilityControl struct {
	Setting      ControlSetting `json:"setting"`
	Controllable bool           `json:"controllable"`
}

type CapabilityPermission struct {
	Required  bool    `json:"required"`
	Granted   bool    `json:"granted"`
	PolicyRef *string `json:"policyRef"`
}

type CapabilityExplanation struct {
	Code string `json:"code"`
	Text string `json:"text"`
}

type CapabilityLimits struct {
	MIMETypes          []string `json:"mimeTypes"`
	MaxBytes           *int64   `json:"maxBytes"`
	MaxCount           *int     `json:"maxCount"`
	RequestBudgetBytes *int64   `json:"requestBudgetBytes"`
	ContextTokens      *int     `json:"contextTokens"`
	MaxOutputTokens    *int     `json:"maxOutputTokens"`
}

type CapabilityEvidence struct {
	Kind        string  `json:"kind"`
	VerifiedAt  *string `json:"verifiedAt"`
	Ref         *string `json:"ref"`
	Stale       bool    `json:"stale"`
	StaleReason *string `json:"staleReason"`
}

type PermissionDecision struct {
	Granted   bool
	PolicyRef string
}

type ResolveRequest struct {
	Target          TargetDescriptor
	Fingerprint     string
	Controls        map[FeatureKey]ControlSetting
	Permissions     map[FeatureKey]PermissionDecision
	OverridePresent bool
}
