package capabilities

import "strings"

const (
	DefaultMaxInputTokens            = 128000
	DefaultMaxOutputTokens           = 8192
	DefaultInputSafetyReserveTokens  = 8192
	DefaultOutputSafetyReserveTokens = 1024
)

type NativeCompactionMode string

const (
	NativeCompactionNone                            NativeCompactionMode = "none"
	NativeCompactionOpenAIContextManagement         NativeCompactionMode = "openai_context_management"
	NativeCompactionAnthropicContextManagementEdits NativeCompactionMode = "anthropic_context_management_edits"
)

type TokenLimits struct {
	MaxInputTokens  int
	MaxOutputTokens int
}

type NativeCompactionSupport struct {
	Mode          NativeCompactionMode
	Enabled       bool
	TriggerTokens int
	Beta          string
	EditType      string
}

type OpaqueCompactionPreservation struct {
	ProviderPayload bool
	ProviderState   bool
}

type ModelCapabilities struct {
	ProviderID string
	Model      string

	MaxInputTokens            int
	MaxOutputTokens           int
	InputSafetyReserveTokens  int
	OutputSafetyReserveTokens int

	NativeCompaction             NativeCompactionSupport
	SupportsPreviousResponseID   bool
	OpaqueCompactionPreservation OpaqueCompactionPreservation
}

type Reporter interface {
	Capabilities() ModelCapabilities
}

func Normalize(c ModelCapabilities) ModelCapabilities {
	c.ProviderID = strings.TrimSpace(c.ProviderID)
	c.Model = strings.TrimSpace(c.Model)

	if c.MaxInputTokens <= 0 {
		c.MaxInputTokens = DefaultMaxInputTokens
	}
	if c.MaxOutputTokens <= 0 {
		c.MaxOutputTokens = DefaultMaxOutputTokens
	}
	if c.InputSafetyReserveTokens < 0 {
		c.InputSafetyReserveTokens = 0
	} else if c.InputSafetyReserveTokens == 0 {
		c.InputSafetyReserveTokens = DefaultInputSafetyReserveTokens
	}
	if c.OutputSafetyReserveTokens < 0 {
		c.OutputSafetyReserveTokens = 0
	} else if c.OutputSafetyReserveTokens == 0 {
		c.OutputSafetyReserveTokens = DefaultOutputSafetyReserveTokens
	}

	if c.NativeCompaction.Mode == "" {
		c.NativeCompaction.Mode = NativeCompactionNone
	}
	if c.NativeCompaction.Mode == NativeCompactionNone {
		c.NativeCompaction.Enabled = false
		c.NativeCompaction.TriggerTokens = 0
		c.NativeCompaction.Beta = ""
		c.NativeCompaction.EditType = ""
	}
	return c
}

func DefaultTokenLimits() TokenLimits {
	return TokenLimits{MaxInputTokens: DefaultMaxInputTokens, MaxOutputTokens: DefaultMaxOutputTokens}
}

func OpenAIModelTokenLimits(model string) TokenLimits {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "gpt-4.1"):
		return TokenLimits{MaxInputTokens: 1000000, MaxOutputTokens: 32768}
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "gpt-5"), strings.Contains(m, "o1"), strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return TokenLimits{MaxInputTokens: 128000, MaxOutputTokens: 32768}
	default:
		return DefaultTokenLimits()
	}
}

func AnthropicModelTokenLimits(model string) TokenLimits {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" || !strings.Contains(m, "claude-") {
		return TokenLimits{MaxInputTokens: 180000, MaxOutputTokens: DefaultMaxOutputTokens}
	}
	switch {
	case strings.Contains(m, "claude-2"):
		return TokenLimits{MaxInputTokens: 100000, MaxOutputTokens: DefaultMaxOutputTokens}
	case strings.Contains(m, "claude-3"), strings.Contains(m, "claude-opus-4"), strings.Contains(m, "claude-sonnet-4"):
		return TokenLimits{MaxInputTokens: 200000, MaxOutputTokens: DefaultMaxOutputTokens}
	default:
		return TokenLimits{MaxInputTokens: 180000, MaxOutputTokens: DefaultMaxOutputTokens}
	}
}

func GeminiModelTokenLimits(model string) TokenLimits {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "1.5-pro"):
		return TokenLimits{MaxInputTokens: 2000000, MaxOutputTokens: DefaultMaxOutputTokens}
	case strings.Contains(m, "gemini"):
		return TokenLimits{MaxInputTokens: 1000000, MaxOutputTokens: DefaultMaxOutputTokens}
	default:
		return DefaultTokenLimits()
	}
}
