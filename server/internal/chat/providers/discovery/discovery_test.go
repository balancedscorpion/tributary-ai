package discovery

import (
	"testing"
)

func TestParseModelsOpenAIStyle(t *testing.T) {
	models, err := parseModels("openai", []byte(`{
      "data": [
        {"id":"gpt-next-preview"},
        {"id":"gpt-stable"},
        {"id":"gpt-stable"}
      ]
    }`), "gpt-stable")
	if err != nil {
		t.Fatalf("parseModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("len(models) = %d, want 2", len(models))
	}
	if models[0].ID != "gpt-stable" || models[0].Lifecycle != LifecycleRecommended {
		t.Fatalf("selected model should sort first as recommended: %#v", models[0])
	}
	if models[1].Lifecycle != LifecyclePreview {
		t.Fatalf("preview lifecycle = %q, want %q", models[1].Lifecycle, LifecyclePreview)
	}
}

func TestParseModelsGeminiFiltersUnsupportedMethodsAndPreservesLimits(t *testing.T) {
	models, err := parseModels("gemini", []byte(`{
      "models": [
        {
          "name":"models/gemini-current",
          "displayName":"Gemini Current",
          "supportedGenerationMethods":["generateContent"],
          "inputTokenLimit":1048576,
          "outputTokenLimit":65536
        },
        {
          "name":"models/embedding-only",
          "supportedGenerationMethods":["embedContent"]
        }
      ]
    }`), "gemini-current")
	if err != nil {
		t.Fatalf("parseModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1: %#v", len(models), models)
	}
	got := models[0]
	if got.ID != "gemini-current" || got.Label == nil || *got.Label != "Gemini Current" {
		t.Fatalf("unexpected model identity: %#v", got)
	}
	if got.ContextTokens == nil || *got.ContextTokens != 1048576 {
		t.Fatalf("context tokens = %#v", got.ContextTokens)
	}
	if got.MaxOutputTokens == nil || *got.MaxOutputTokens != 65536 {
		t.Fatalf("output tokens = %#v", got.MaxOutputTokens)
	}
}

func TestParseModelsRejectsMalformedJSON(t *testing.T) {
	if _, err := parseModels("anthropic", []byte(`{"data":`), ""); err == nil {
		t.Fatal("expected malformed JSON error")
	}
}

func TestInferLifecycleFailsClosedForKnownMarkers(t *testing.T) {
	tests := map[string]Lifecycle{
		"model-retired":      LifecycleShutdown,
		"model-deprecated":   LifecycleDeprecated,
		"model-experimental": LifecyclePreview,
		"model-stable":       LifecycleAvailable,
	}
	for model, want := range tests {
		if got := inferLifecycle(model, ""); got != want {
			t.Errorf("inferLifecycle(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestInferLifecycleSelectionDoesNotMaskLifecycleTruth(t *testing.T) {
	tests := map[string]Lifecycle{
		"model-retired":    LifecycleShutdown,
		"model-deprecated": LifecycleDeprecated,
		"model-preview":    LifecyclePreview,
	}
	for model, want := range tests {
		if got := inferLifecycle(model, model); got != want {
			t.Errorf("inferLifecycle(%q, selected) = %q, want %q", model, got, want)
		}
	}
}

func TestParseModelsGeminiKnownShutdownFamiliesOverrideSelected(t *testing.T) {
	tests := []struct {
		name        string
		model       string
		shutdown    string
		replacement string
	}{
		{
			name:        "Gemini 1.5 Pro version",
			model:       "gemini-1.5-pro-002",
			shutdown:    "2025-09-29",
			replacement: "gemini-3.1-pro-preview",
		},
		{
			name:        "Gemini 1.5 Flash alias",
			model:       "gemini-1.5-flash-latest",
			shutdown:    "2025-09-29",
			replacement: "gemini-3.5-flash",
		},
		{
			name:        "Gemini 1.5 Flash 8B version",
			model:       "gemini-1.5-flash-8b-001",
			shutdown:    "2025-09-29",
			replacement: "gemini-3.1-flash-lite",
		},
		{
			name:        "Gemini 2.0 Flash stable",
			model:       "gemini-2.0-flash-001",
			shutdown:    "2026-06-01",
			replacement: "gemini-3.5-flash",
		},
		{
			name:        "Gemini 2.0 Flash Lite stable",
			model:       "gemini-2.0-flash-lite-001",
			shutdown:    "2026-06-01",
			replacement: "gemini-3.1-flash-lite",
		},
		{
			name:        "Gemini 2.0 Flash Lite preview",
			model:       "gemini-2.0-flash-lite-preview-02-05",
			shutdown:    "2025-12-09",
			replacement: "gemini-3.1-flash-lite",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"models":[{"name":"models/` + tt.model + `","supportedGenerationMethods":["generateContent"]}]}`)
			models, err := parseModels("gemini", body, tt.model)
			if err != nil {
				t.Fatalf("parseModels: %v", err)
			}
			if len(models) != 1 {
				t.Fatalf("len(models) = %d, want 1: %#v", len(models), models)
			}
			got := models[0]
			if got.Lifecycle != LifecycleShutdown {
				t.Fatalf("lifecycle = %q, want %q (selected model must not be recommended)", got.Lifecycle, LifecycleShutdown)
			}
			if got.DeprecationDate == nil || *got.DeprecationDate != tt.shutdown {
				t.Errorf("deprecationDate = %#v, want %q", got.DeprecationDate, tt.shutdown)
			}
			if got.Replacement == nil || *got.Replacement != tt.replacement {
				t.Errorf("replacement = %#v, want %q", got.Replacement, tt.replacement)
			}
		})
	}
}

func TestGeminiLifecycleRulesDoNotLeakToCompatibleProviders(t *testing.T) {
	models, err := parseModels("compatible", []byte(`{"data":[{"id":"gemini-2.0-flash"}]}`), "gemini-2.0-flash")
	if err != nil {
		t.Fatalf("parseModels: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("len(models) = %d, want 1", len(models))
	}
	if models[0].Lifecycle != LifecycleRecommended {
		t.Fatalf("compatible lifecycle = %q, want %q", models[0].Lifecycle, LifecycleRecommended)
	}
	if models[0].DeprecationDate != nil || models[0].Replacement != nil {
		t.Fatalf("Gemini metadata leaked to compatible provider: %#v", models[0])
	}
}

func TestResolveLifecycleSharesProviderScopedShutdownMetadata(t *testing.T) {
	metadata := ResolveLifecycle("gemini", "gemini-1.5-pro-002")
	if metadata.Lifecycle != LifecycleShutdown {
		t.Fatalf("lifecycle = %q, want %q", metadata.Lifecycle, LifecycleShutdown)
	}
	if metadata.Replacement == nil || *metadata.Replacement != "gemini-3.1-pro-preview" {
		t.Fatalf("replacement = %#v", metadata.Replacement)
	}
	if metadata.DeprecationDate == nil || *metadata.DeprecationDate != "2025-09-29" {
		t.Fatalf("shutdown date = %#v", metadata.DeprecationDate)
	}

	compatible := ResolveLifecycle("compatible", "gemini-1.5-pro-002")
	if compatible.Lifecycle == LifecycleShutdown {
		t.Fatalf("provider-scoped rule leaked into compatible target: %#v", compatible)
	}
}
