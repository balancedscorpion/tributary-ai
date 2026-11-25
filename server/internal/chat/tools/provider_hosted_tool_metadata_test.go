package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestProviderHostedToolCapabilitiesAreExplicitlyClassified(t *testing.T) {
	caps := ProviderHostedToolCapabilities()
	if len(caps) == 0 {
		t.Fatal("ProviderHostedToolCapabilities returned no capabilities")
	}

	seen := map[string]bool{}
	problems := []string{}
	for _, cap := range caps {
		key := cap.AdapterFamily + "/" + cap.Name
		if cap.ID != key {
			problems = append(problems, fmt.Sprintf("%s id=%q, want adapter/name key", key, cap.ID))
		}
		if seen[key] {
			problems = append(problems, fmt.Sprintf("%s duplicated", key))
		}
		seen[key] = true
		if !isStableToolName(cap.Name) {
			problems = append(problems, fmt.Sprintf("%s hosted tool name must be lowercase snake_case", key))
		}
		if !isStableKebabID(cap.AdapterFamily) {
			problems = append(problems, fmt.Sprintf("%s adapter_family=%q must be lowercase kebab-case", key, cap.AdapterFamily))
		}
		if strings.TrimSpace(cap.Label) == "" || strings.TrimSpace(cap.Description) == "" {
			problems = append(problems, fmt.Sprintf("%s must have label and description", key))
		}
		if cap.Category != ToolCategoryRead && cap.Category != ToolCategoryExecute {
			problems = append(problems, fmt.Sprintf("%s category=%q, want read or execute", key, cap.Category))
		}
		if cap.SettingsKey != "webSearch" && cap.SettingsKey != "codeExecution" {
			problems = append(problems, fmt.Sprintf("%s settings_key=%q, want provider settings key", key, cap.SettingsKey))
		}
		if cap.Origin != "provider" {
			problems = append(problems, fmt.Sprintf("%s origin=%q, want provider", key, cap.Origin))
		}
		if cap.ToolSetID != "provider-hosted" || cap.ToolSetName != "Model provider capabilities" {
			problems = append(problems, fmt.Sprintf("%s tool set=%q/%q, want provider-hosted/Model provider capabilities", key, cap.ToolSetID, cap.ToolSetName))
		}
		if cap.PermissionSubjectKind != "provider_hosted_tool" {
			problems = append(problems, fmt.Sprintf("%s permission_subject_kind=%q, want provider_hosted_tool", key, cap.PermissionSubjectKind))
		}
		if len(cap.ProviderToolNames) == 0 {
			problems = append(problems, fmt.Sprintf("%s must list provider tool names", key))
		}
		if !cap.ProviderExecuted {
			problems = append(problems, fmt.Sprintf("%s must be provider_executed", key))
		}
	}

	for _, want := range []string{
		"anthropic-messages/code_execution",
		"anthropic-messages/web_search",
		"openai-responses/code_interpreter",
		"openai-responses/web_search",
	} {
		if !seen[want] {
			problems = append(problems, fmt.Sprintf("missing hosted capability %s", want))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("provider-hosted tool capability problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestProviderHostedToolCapabilitiesExposePresentationMetadata(t *testing.T) {
	problems := []string{}
	for _, cap := range ProviderHostedToolCapabilities() {
		p := cap.Presentation
		if p.Family != "provider-hosted" ||
			p.ResultKind != "connector" ||
			p.RendererStrategy != "family" ||
			p.RendererKey != "provider-hosted" ||
			p.NativeViewer != "connector_inspector" {
			problems = append(problems, fmt.Sprintf("%s presentation=%#v, want provider-hosted connector inspector", cap.ID, p))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("provider-hosted presentation metadata problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestProviderHostedToolCapabilitiesExposeSettingsTestSpec(t *testing.T) {
	problems := []string{}
	for _, cap := range ProviderHostedToolCapabilities() {
		if !cap.Test.Supported {
			problems = append(problems, fmt.Sprintf("%s test.supported=false, want true for non-executing settings check", cap.ID))
		}
		if cap.Test.Mode != ToolTestModeConnectionCheck {
			problems = append(problems, fmt.Sprintf("%s test.mode=%q, want connection_check", cap.ID, cap.Test.Mode))
		}
		if cap.Test.RequiresApproval {
			problems = append(problems, fmt.Sprintf("%s test.requires_approval=true, want false because settings check does not execute provider code", cap.ID))
		}
		if strings.TrimSpace(cap.Test.Summary) == "" {
			problems = append(problems, fmt.Sprintf("%s test.summary is empty", cap.ID))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("provider-hosted settings test metadata problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestProviderHostedToolAliasesCoverCanonicalProviderDefaults(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	providerDefsRaw, err := os.ReadFile(filepath.Join(root, "providers", "catalog", "providerDefs.ts"))
	if err != nil {
		t.Fatalf("read providerDefs.ts: %v", err)
	}
	webSearchRaw, err := os.ReadFile(filepath.Join(root, "lib", "providerWebSearch.ts"))
	if err != nil {
		t.Fatalf("read providerWebSearch.ts: %v", err)
	}
	anthropicWebSearch, responsesWebSearch := canonicalWebSearchDefaultsForTest(t, string(webSearchRaw))
	expected := map[string]string{
		"anthropic-messages/web_search":     anthropicWebSearch,
		"anthropic-messages/code_execution": providerCatalogDefaultForTest(t, string(providerDefsRaw), "codeExecutionToolType"),
		"openai-responses/web_search":       responsesWebSearch,
	}

	byID := map[string]ProviderHostedToolCapability{}
	for _, cap := range ProviderHostedToolCapabilities() {
		byID[cap.ID] = cap
	}
	problems := []string{}
	for id, defaultToolType := range expected {
		cap, ok := byID[id]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s provider-hosted capability missing", id))
			continue
		}
		if !containsString(cap.ProviderToolNames, defaultToolType) {
			problems = append(problems, fmt.Sprintf("%s provider_tool_names=%v missing provider catalog default %q", id, cap.ProviderToolNames, defaultToolType))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("provider-hosted aliases do not cover provider catalog defaults:\n%s", strings.Join(problems, "\n"))
	}
}

func canonicalWebSearchDefaultsForTest(t *testing.T, src string) (anthropic string, responses string) {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)providerId === 'anthropic'.*?toolType:\s*'([^']+)'.*?return\s*\{.*?toolType:\s*'([^']+)'`)
	matches := pattern.FindStringSubmatch(src)
	if len(matches) != 3 {
		t.Fatal("providerWebSearch.ts missing canonical Anthropic and Responses tool types")
	}
	return strings.TrimSpace(matches[1]), strings.TrimSpace(matches[2])
}

func TestProviderHostedToolCapabilitiesStayOutOfRegisteredToolInventory(t *testing.T) {
	problems := []string{}
	for _, cap := range ProviderHostedToolCapabilities() {
		if _, ok := ToolMetadataByName(cap.Name); ok {
			problems = append(problems, fmt.Sprintf("%s/%s is provider-hosted but appears in registered tool metadata", cap.AdapterFamily, cap.Name))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("provider-hosted tools must not be duplicated in AllToolMetadata:\n%s", strings.Join(problems, "\n"))
	}
}

func TestFrontendStaticProviderHostedCapabilitiesMatchBackend(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "generatedToolMetadata.ts"))
	if err != nil {
		t.Fatalf("read generated frontend tool metadata: %v", err)
	}
	got := parseGeneratedFrontendProviderHostedCapabilities(t, string(raw))
	want := ProviderHostedToolCapabilities()

	gotByID := map[string]ProviderHostedToolCapability{}
	for _, cap := range got {
		gotByID[cap.ID] = cap
	}
	wantByID := map[string]ProviderHostedToolCapability{}
	for _, cap := range want {
		wantByID[cap.ID] = cap
	}

	problems := []string{}
	for id, wantCap := range wantByID {
		gotCap, ok := gotByID[id]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s missing from frontend static provider-hosted capabilities", id))
			continue
		}
		if gotCap.Name != wantCap.Name {
			problems = append(problems, fmt.Sprintf("%s name=%q want %q", id, gotCap.Name, wantCap.Name))
		}
		if gotCap.Label != wantCap.Label {
			problems = append(problems, fmt.Sprintf("%s label=%q want %q", id, gotCap.Label, wantCap.Label))
		}
		if gotCap.Description != wantCap.Description {
			problems = append(problems, fmt.Sprintf("%s description=%q want %q", id, gotCap.Description, wantCap.Description))
		}
		if gotCap.Category != wantCap.Category {
			problems = append(problems, fmt.Sprintf("%s category=%q want %q", id, gotCap.Category, wantCap.Category))
		}
		if gotCap.AdapterFamily != wantCap.AdapterFamily {
			problems = append(problems, fmt.Sprintf("%s adapter_family=%q want %q", id, gotCap.AdapterFamily, wantCap.AdapterFamily))
		}
		if gotCap.SettingsKey != wantCap.SettingsKey {
			problems = append(problems, fmt.Sprintf("%s settings_key=%q want %q", id, gotCap.SettingsKey, wantCap.SettingsKey))
		}
		if gotCap.Origin != wantCap.Origin {
			problems = append(problems, fmt.Sprintf("%s origin=%q want %q", id, gotCap.Origin, wantCap.Origin))
		}
		if gotCap.ToolSetID != wantCap.ToolSetID || gotCap.ToolSetName != wantCap.ToolSetName {
			problems = append(problems, fmt.Sprintf("%s tool set=%q/%q want %q/%q", id, gotCap.ToolSetID, gotCap.ToolSetName, wantCap.ToolSetID, wantCap.ToolSetName))
		}
		if gotCap.PermissionSubjectKind != wantCap.PermissionSubjectKind {
			problems = append(problems, fmt.Sprintf("%s permission_subject_kind=%q want %q", id, gotCap.PermissionSubjectKind, wantCap.PermissionSubjectKind))
		}
		if !sameStringsUnordered(gotCap.ProviderToolNames, wantCap.ProviderToolNames) {
			problems = append(problems, fmt.Sprintf("%s provider_tool_names=%v want %v", id, gotCap.ProviderToolNames, wantCap.ProviderToolNames))
		}
		if gotCap.ProviderExecuted != wantCap.ProviderExecuted {
			problems = append(problems, fmt.Sprintf("%s provider_executed=%v want %v", id, gotCap.ProviderExecuted, wantCap.ProviderExecuted))
		}
		if !sameJSON(gotCap.Presentation, wantCap.Presentation) {
			problems = append(problems, fmt.Sprintf("%s presentation=%#v want %#v", id, gotCap.Presentation, wantCap.Presentation))
		}
		if !sameJSON(gotCap.Test, wantCap.Test) {
			problems = append(problems, fmt.Sprintf("%s test=%#v want %#v", id, gotCap.Test, wantCap.Test))
		}
	}
	for id := range gotByID {
		if _, ok := wantByID[id]; !ok {
			problems = append(problems, fmt.Sprintf("%s frontend static provider-hosted capability is stale", id))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("frontend static provider-hosted capability drift:\n%s", strings.Join(problems, "\n"))
	}
}

func providerCatalogDefaultForTest(t *testing.T, src string, key string) string {
	t.Helper()
	pattern := regexp.MustCompile(`key:\s*'` + regexp.QuoteMeta(key) + `'[\s\S]*?def:\s*'([^']+)'`)
	matches := pattern.FindStringSubmatch(src)
	if len(matches) != 2 {
		t.Fatalf("providerDefs.ts missing default for %s", key)
	}
	return strings.TrimSpace(matches[1])
}

func parseGeneratedFrontendProviderHostedCapabilities(t *testing.T, src string) []ProviderHostedToolCapability {
	t.Helper()
	start := strings.Index(src, "export const GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES = ")
	if start < 0 {
		t.Fatal("generated frontend provider-hosted capabilities missing export")
	}
	arrayOffset := strings.Index(src[start:], "[")
	if arrayOffset < 0 {
		t.Fatal("generated frontend provider-hosted capabilities missing JSON array")
	}
	start += arrayOffset
	endMarker := "\n] as const;"
	end := strings.Index(src[start:], endMarker)
	if end < 0 {
		t.Fatal("generated frontend provider-hosted capabilities missing array terminator")
	}
	block := src[start : start+end+2]
	var out []ProviderHostedToolCapability
	if err := json.Unmarshal([]byte(block), &out); err != nil {
		t.Fatalf("parse generated frontend provider-hosted capabilities: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("generated frontend provider-hosted capability parser found no entries")
	}
	return out
}

func sameJSON(a any, b any) bool {
	aj, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bj, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aj) == string(bj)
}
