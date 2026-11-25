package tools

import (
	"sort"
	"strings"
)

// ProviderHostedToolCapability documents model-exposed tools that are injected
// by provider adapters instead of being registered in the local tool registry.
type ProviderHostedToolCapability struct {
	ID                    string                 `json:"id"`
	Name                  string                 `json:"name"`
	Label                 string                 `json:"label"`
	Description           string                 `json:"description"`
	Category              ToolPermissionCategory `json:"category"`
	Origin                string                 `json:"origin"`
	ToolSetID             string                 `json:"tool_set_id"`
	ToolSetName           string                 `json:"tool_set_name"`
	AdapterFamily         string                 `json:"adapter_family"`
	SettingsKey           string                 `json:"settings_key"`
	PermissionSubjectKind string                 `json:"permission_subject_kind"`
	ProviderToolNames     []string               `json:"provider_tool_names,omitempty"`
	ProviderExecuted      bool                   `json:"provider_executed"`
	Presentation          ToolPresentationSpec   `json:"presentation"`
	Test                  ToolTestSpec           `json:"test"`
}

// ProviderHostedToolCapabilities is intentionally separate from AllToolMetadata.
// These capabilities are configured through provider settings and are executed
// by the model provider, so they do not have local Tool.Definition schemas.
// Their settings-page tests are non-executing catalog/configuration checks.
func ProviderHostedToolCapabilities() []ProviderHostedToolCapability {
	presentation := providerHostedToolPresentation()
	test := providerHostedToolTestSpec()
	out := []ProviderHostedToolCapability{
		{
			ID:                    "openai-responses/web_search",
			Name:                  "web_search",
			Label:                 "Responses-compatible hosted web search",
			Description:           "Let a Responses-compatible model provider perform web search inside the model request.",
			Category:              ToolCategoryRead,
			Origin:                "provider",
			ToolSetID:             "provider-hosted",
			ToolSetName:           "Model provider capabilities",
			AdapterFamily:         "openai-responses",
			SettingsKey:           "webSearch",
			PermissionSubjectKind: "provider_hosted_tool",
			ProviderToolNames:     []string{"web_search"},
			ProviderExecuted:      true,
			Presentation:          presentation,
			Test:                  test,
		},
		{
			ID:                    "openai-responses/code_interpreter",
			Name:                  "code_interpreter",
			Label:                 "Responses-compatible hosted code interpreter",
			Description:           "Let a Responses-compatible model provider execute code in a provider-managed sandbox.",
			Category:              ToolCategoryExecute,
			Origin:                "provider",
			ToolSetID:             "provider-hosted",
			ToolSetName:           "Model provider capabilities",
			AdapterFamily:         "openai-responses",
			SettingsKey:           "codeExecution",
			PermissionSubjectKind: "provider_hosted_tool",
			ProviderToolNames:     []string{"code_interpreter"},
			ProviderExecuted:      true,
			Presentation:          presentation,
			Test:                  test,
		},
		{
			ID:                    "anthropic-messages/web_search",
			Name:                  "web_search",
			Label:                 "Anthropic hosted web search",
			Description:           "Let the model provider perform web search inside the model request.",
			Category:              ToolCategoryRead,
			Origin:                "provider",
			ToolSetID:             "provider-hosted",
			ToolSetName:           "Model provider capabilities",
			AdapterFamily:         "anthropic-messages",
			SettingsKey:           "webSearch",
			PermissionSubjectKind: "provider_hosted_tool",
			ProviderToolNames:     []string{"web_search", "web_search_20250305", "web_search_20260209"},
			ProviderExecuted:      true,
			Presentation:          presentation,
			Test:                  test,
		},
		{
			ID:                    "anthropic-messages/code_execution",
			Name:                  "code_execution",
			Label:                 "Anthropic hosted code execution",
			Description:           "Let the model provider execute code in a provider-managed sandbox.",
			Category:              ToolCategoryExecute,
			Origin:                "provider",
			ToolSetID:             "provider-hosted",
			ToolSetName:           "Model provider capabilities",
			AdapterFamily:         "anthropic-messages",
			SettingsKey:           "codeExecution",
			PermissionSubjectKind: "provider_hosted_tool",
			ProviderToolNames:     []string{"code_execution", "code_execution_20250522", "code_execution_20250825", "code_execution_20260120"},
			ProviderExecuted:      true,
			Presentation:          presentation,
			Test:                  test,
		},
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AdapterFamily != out[j].AdapterFamily {
			return out[i].AdapterFamily < out[j].AdapterFamily
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func ProviderHostedToolCapabilityByID(id string) (ProviderHostedToolCapability, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ProviderHostedToolCapability{}, false
	}
	for _, capability := range ProviderHostedToolCapabilities() {
		if capability.ID == id {
			return capability, true
		}
	}
	return ProviderHostedToolCapability{}, false
}

func providerHostedToolPresentation() ToolPresentationSpec {
	return ToolPresentationSpec{
		Family:           "provider-hosted",
		ResultKind:       "connector",
		RendererStrategy: "family",
		RendererKey:      "provider-hosted",
		NativeViewer:     "connector_inspector",
	}
}

func providerHostedToolTestSpec() ToolTestSpec {
	return ToolTestSpec{
		Supported:        true,
		Mode:             ToolTestModeConnectionCheck,
		RequiresApproval: false,
		InputRequired:    false,
		Summary:          "Checks that this provider-hosted capability is cataloged, permissionable, and renderable. It does not execute provider code.",
	}
}
