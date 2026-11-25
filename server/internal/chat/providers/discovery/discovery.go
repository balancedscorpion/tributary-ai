package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

const maxDiscoveryResponseBytes = 4 << 20

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Lifecycle string

const (
	LifecycleRecommended Lifecycle = "recommended"
	LifecycleAvailable   Lifecycle = "available"
	LifecyclePreview     Lifecycle = "preview"
	LifecycleDeprecated  Lifecycle = "deprecated"
	LifecycleShutdown    Lifecycle = "shutdown"
)

type Model struct {
	ID              string    `json:"id"`
	Label           *string   `json:"label"`
	Lifecycle       Lifecycle `json:"lifecycle"`
	DeprecationDate *string   `json:"deprecationDate"`
	Replacement     *string   `json:"replacement"`
	ContextTokens   *int      `json:"contextTokens"`
	MaxOutputTokens *int      `json:"maxOutputTokens"`
}

type Result struct {
	Source    string    `json:"source"`
	FetchedAt time.Time `json:"fetchedAt"`
	Error     *string   `json:"error"`
	Models    []Model   `json:"models"`
}

// LifecycleMetadata is the provider-scoped lifecycle authority shared by
// discovery and run admission. It is deliberately transport-free so callers
// can evaluate an exact provider/model pair without performing discovery.
type LifecycleMetadata struct {
	Lifecycle       Lifecycle
	DeprecationDate *string
	Replacement     *string
}

type lifecycleRule struct {
	Prefix          string
	Lifecycle       Lifecycle
	DeprecationDate string
	Replacement     string
}

// knownGeminiLifecycleRules captures provider-announced shutdowns that cannot
// be inferred from model names. Keep more-specific prefixes before their
// parent families. Dates are the provider's shutdown dates, which are the
// actionable date exposed by the discovery contract's deprecationDate field.
//
// Source: https://ai.google.dev/gemini-api/docs/deprecations
var knownGeminiLifecycleRules = []lifecycleRule{
	{
		Prefix:          "gemini-2.0-flash-lite-preview",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-12-09",
		Replacement:     "gemini-3.1-flash-lite",
	},
	{
		Prefix:          "gemini-2.0-flash-lite",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2026-06-01",
		Replacement:     "gemini-3.1-flash-lite",
	},
	{
		Prefix:          "gemini-2.0-flash-preview-image-generation",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-11-14",
		Replacement:     "gemini-3.1-flash-image",
	},
	{
		Prefix:          "gemini-2.0-flash-exp-image-generation",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-11-14",
		Replacement:     "gemini-3.1-flash-image",
	},
	{
		Prefix:          "gemini-2.0-flash-thinking-exp",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-12-02",
		Replacement:     "gemini-3.5-flash",
	},
	{
		Prefix:          "gemini-2.0-flash-live",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-12-09",
		Replacement:     "gemini-3.1-flash-live-preview",
	},
	{
		Prefix:          "gemini-2.0-flash-exp",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-12-09",
		Replacement:     "gemini-3.5-flash",
	},
	{
		Prefix:          "gemini-2.0-pro-exp",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-12-09",
		Replacement:     "gemini-3.1-pro-preview",
	},
	{
		Prefix:          "gemini-2.0-flash",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2026-06-01",
		Replacement:     "gemini-3.5-flash",
	},
	{
		Prefix:          "gemini-1.5-flash-8b",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-09-29",
		Replacement:     "gemini-3.1-flash-lite",
	},
	{
		Prefix:          "gemini-1.5-flash",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-09-29",
		Replacement:     "gemini-3.5-flash",
	},
	{
		Prefix:          "gemini-1.5-pro",
		Lifecycle:       LifecycleShutdown,
		DeprecationDate: "2025-09-29",
		Replacement:     "gemini-3.1-pro-preview",
	},
}

type Service struct {
	Secrets *secrets.Store
	Client  HTTPClient
	Now     func() time.Time
}

func NewService(secretStore *secrets.Store) *Service {
	return &Service{
		Secrets: secretStore,
		Client:  &http.Client{Timeout: 20 * time.Second},
		Now:     func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Discover(ctx context.Context, cfg config.ProviderConfig) Result {
	now := time.Now().UTC()
	if s != nil && s.Now != nil {
		now = s.Now().UTC()
	}
	result := Result{Source: "unavailable", FetchedAt: now, Models: []Model{}}
	if s == nil || s.Secrets == nil {
		return result.withError(errors.New("provider secrets store is unavailable"))
	}
	if strings.TrimSpace(cfg.ProviderID) == "bedrock" {
		// Runtime credentials do not expose the Bedrock control-plane model list.
		// Preserve an honest curated/manual fallback instead of probing an invalid
		// endpoint or claiming that every Bedrock model supports every surface.
		result.Source = "curated"
		if model := strings.TrimSpace(cfg.Model); model != "" {
			label := model
			result.Models = []Model{{ID: model, Label: &label, Lifecycle: LifecycleRecommended}}
		}
		return result
	}

	req, err := s.request(ctx, cfg)
	if err != nil {
		return result.withError(err)
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return result.withError(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDiscoveryResponseBytes+1))
	if err != nil {
		return result.withError(err)
	}
	if len(body) > maxDiscoveryResponseBytes {
		return result.withError(errors.New("model discovery response is too large"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result.withError(fmt.Errorf("model discovery failed with HTTP %d", resp.StatusCode))
	}
	models, err := parseModels(strings.TrimSpace(cfg.ProviderID), body, strings.TrimSpace(cfg.Model))
	if err != nil {
		return result.withError(err)
	}
	result.Source = "provider_api"
	result.Models = models
	return result
}

func (r Result) withError(err error) Result {
	if err == nil {
		return r
	}
	message := strings.TrimSpace(err.Error())
	if message == "" {
		message = "model discovery failed"
	}
	r.Error = &message
	return r
}

func (s *Service) request(ctx context.Context, cfg config.ProviderConfig) (*http.Request, error) {
	providerID := strings.TrimSpace(cfg.ProviderID)
	base := strings.TrimRight(strings.TrimSpace(stringValue(cfg.BaseURL)), "/")
	endpoint := ""
	switch providerID {
	case "openai":
		if base == "" {
			base = "https://api.openai.com/v1"
		}
		endpoint = base + "/models"
	case "anthropic":
		if base == "" {
			base = "https://api.anthropic.com"
		}
		endpoint = base + "/v1/models"
	case "gemini":
		if base == "" {
			base = "https://generativelanguage.googleapis.com"
		}
		endpoint = base + "/v1beta/models?pageSize=1000"
	case "compatible":
		if base == "" {
			return nil, errors.New("base URL is required for model discovery")
		}
		endpoint = base + "/models"
	default:
		return nil, fmt.Errorf("model discovery is unavailable for provider %q", providerID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	switch providerID {
	case "openai":
		key, err := s.Secrets.GetProviderKey("openai")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("OpenAI API key is not configured")
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	case "anthropic":
		key, err := s.Secrets.GetProviderKey("anthropic")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("Anthropic API key is not configured")
		}
		req.Header.Set("x-api-key", strings.TrimSpace(key))
		version := strings.TrimSpace(stringValue(cfg.AnthropicVersion))
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
	case "gemini":
		key, err := s.Secrets.GetProviderKey("gemini")
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(key) == "" {
			return nil, errors.New("Gemini API key is not configured")
		}
		req.Header.Set("x-goog-api-key", strings.TrimSpace(key))
	case "compatible":
		sec, err := s.Secrets.GetCompatible(strings.TrimSpace(cfg.ID))
		if err != nil {
			return nil, err
		}
		for _, header := range cfg.ExtraHeaders {
			key := strings.TrimSpace(header.Key)
			if key == "" || header.IsSecret {
				continue
			}
			req.Header.Set(key, header.Value)
		}
		for key, value := range sec.SecretHeaders {
			if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
				req.Header.Set(strings.TrimSpace(key), value)
			}
		}
		if strings.TrimSpace(sec.APIKey) != "" && strings.TrimSpace(stringValue(cfg.AuthType)) != "none" {
			header := strings.TrimSpace(stringValue(cfg.AuthHeader))
			if header == "" {
				header = "Authorization"
			}
			scheme := strings.TrimSpace(stringValue(cfg.AuthScheme))
			value := strings.TrimSpace(sec.APIKey)
			if scheme != "" {
				value = scheme + " " + value
			}
			req.Header.Set(header, value)
		}
	}
	return req, nil
}

func parseModels(providerID string, body []byte, selected string) ([]Model, error) {
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("model discovery response was not valid JSON")
	}
	rawModels, _ := payload["data"].([]any)
	if providerID == "gemini" {
		rawModels, _ = payload["models"].([]any)
	}
	models := make([]Model, 0, len(rawModels))
	seen := map[string]struct{}{}
	for _, raw := range rawModels {
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := stringField(obj, "id")
		if providerID == "gemini" {
			id = strings.TrimPrefix(stringField(obj, "name"), "models/")
			if !supportsGenerateContent(obj["supportedGenerationMethods"]) {
				continue
			}
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		labelValue := stringField(obj, "display_name")
		if labelValue == "" {
			labelValue = stringField(obj, "displayName")
		}
		var label *string
		if strings.TrimSpace(labelValue) != "" {
			value := strings.TrimSpace(labelValue)
			label = &value
		}
		lifecycle := inferLifecycleMetadata(providerID, id, selected)
		model := Model{
			ID:              id,
			Label:           label,
			Lifecycle:       lifecycle.Lifecycle,
			DeprecationDate: lifecycle.DeprecationDate,
			Replacement:     lifecycle.Replacement,
			ContextTokens:   intField(obj, "inputTokenLimit"),
			MaxOutputTokens: intField(obj, "outputTokenLimit"),
		}
		models = append(models, model)
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].Lifecycle == LifecycleRecommended && models[j].Lifecycle != LifecycleRecommended {
			return true
		}
		if models[j].Lifecycle == LifecycleRecommended && models[i].Lifecycle != LifecycleRecommended {
			return false
		}
		return strings.ToLower(models[i].ID) < strings.ToLower(models[j].ID)
	})
	return models, nil
}

func inferLifecycle(id, selected string) Lifecycle {
	return inferLifecycleMetadata("", id, selected).Lifecycle
}

// ResolveLifecycle returns provider-scoped lifecycle truth for a model. A
// configured selection is intentionally not accepted here: shutdown and
// replacement semantics must be identical wherever run safety is evaluated.
func ResolveLifecycle(providerID, id string) LifecycleMetadata {
	return inferLifecycleMetadata(providerID, id, "")
}

func inferLifecycleMetadata(providerID, id, selected string) LifecycleMetadata {
	lower := strings.ToLower(id)
	if strings.EqualFold(strings.TrimSpace(providerID), "gemini") {
		for _, rule := range knownGeminiLifecycleRules {
			if strings.HasPrefix(lower, rule.Prefix) {
				return LifecycleMetadata{
					Lifecycle:       rule.Lifecycle,
					DeprecationDate: optionalString(rule.DeprecationDate),
					Replacement:     optionalString(rule.Replacement),
				}
			}
		}
	}

	// Lifecycle truth has precedence over the configured selection. Selection
	// is only a recommendation signal for otherwise-available models.
	if strings.Contains(lower, "shutdown") || strings.Contains(lower, "retired") {
		return LifecycleMetadata{Lifecycle: LifecycleShutdown}
	}
	if strings.Contains(lower, "deprecated") || strings.Contains(lower, "legacy") {
		return LifecycleMetadata{Lifecycle: LifecycleDeprecated}
	}
	if strings.Contains(lower, "preview") || strings.Contains(lower, "experimental") || strings.Contains(lower, "beta") {
		return LifecycleMetadata{Lifecycle: LifecyclePreview}
	}
	if strings.EqualFold(strings.TrimSpace(id), strings.TrimSpace(selected)) && strings.TrimSpace(selected) != "" {
		return LifecycleMetadata{Lifecycle: LifecycleRecommended}
	}
	return LifecycleMetadata{Lifecycle: LifecycleAvailable}
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func supportsGenerateContent(raw any) bool {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 {
		return true
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(value)), "generateContent") {
			return true
		}
	}
	return false
}

func stringField(obj map[string]any, key string) string {
	value, _ := obj[key].(string)
	return strings.TrimSpace(value)
}

func intField(obj map[string]any, key string) *int {
	switch value := obj[key].(type) {
	case json.Number:
		n, err := value.Int64()
		if err == nil && n >= 0 {
			out := int(n)
			return &out
		}
	case float64:
		if value >= 0 {
			out := int(value)
			return &out
		}
	case int:
		if value >= 0 {
			out := value
			return &out
		}
	}
	return nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
