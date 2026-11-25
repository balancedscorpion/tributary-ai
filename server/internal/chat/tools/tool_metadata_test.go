package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ai-analyst-local/internal/arxiv"
	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/browsercontrol"
	"ai-analyst-local/internal/chat/rlm"
	"ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/sharepoint"
	slackapi "ai-analyst-local/internal/slack"
)

func TestToolMetadataCoversEveryRegisteredTool(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	manifest := toolMetadataManifest()

	registered := map[string]struct{}{}
	missingMetadata := []string{}
	for name, tool := range registry.byName {
		registered[name] = struct{}{}
		if _, ok := manifest[name]; !ok {
			missingMetadata = append(missingMetadata, name)
		}
		def := tool.Definition()
		if def.Type != "function" {
			t.Errorf("%s definition type = %q, want function", name, def.Type)
		}
		if strings.TrimSpace(def.Function.Name) != name {
			t.Errorf("%s definition name = %q, want %q", name, def.Function.Name, name)
		}
		if strings.TrimSpace(def.Function.Description) == "" {
			t.Errorf("%s definition description is empty", name)
		}
		if def.Function.Parameters == nil {
			t.Errorf("%s definition parameters are nil", name)
		}
	}
	if len(missingMetadata) > 0 {
		sort.Strings(missingMetadata)
		t.Fatalf("registered tools missing metadata: %s", strings.Join(missingMetadata, ", "))
	}

	staleMetadata := []string{}
	for name := range manifest {
		if _, ok := registered[name]; !ok {
			staleMetadata = append(staleMetadata, name)
		}
	}
	if len(staleMetadata) > 0 {
		sort.Strings(staleMetadata)
		t.Fatalf("metadata exists for tools that are not registered: %s", strings.Join(staleMetadata, ", "))
	}
}

func TestToolMetadataBuilderRejectsDuplicateNames(t *testing.T) {
	b := newToolMetadataBuilder()
	b.read("duplicate_tool", "Duplicate tool", "First declaration.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("duplicate tool metadata declaration did not panic")
		}
	}()

	b.read("duplicate_tool", "Duplicate tool", "Second declaration.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
}

func TestRegisteredToolDefinitionsUseObjectSchemas(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}
	for name, tool := range registry.byName {
		params := tool.Definition().Function.Parameters
		if got, _ := params["type"].(string); got != "object" {
			problems = append(problems, fmt.Sprintf("%s parameters.type=%#v, want object", name, params["type"]))
		}
		properties, ok := params["properties"].(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s parameters.properties=%T, want object", name, params["properties"]))
			continue
		}
		for propName, propSchema := range properties {
			if strings.TrimSpace(propName) == "" {
				problems = append(problems, fmt.Sprintf("%s has an empty property name", name))
			}
			if _, ok := propSchema.(map[string]any); !ok {
				problems = append(problems, fmt.Sprintf("%s property %q schema=%T, want object", name, propName, propSchema))
			}
		}
		for _, required := range toolMetadataRequiredFields(params) {
			if _, ok := properties[required]; !ok {
				problems = append(problems, fmt.Sprintf("%s requires %q but does not define it in properties", name, required))
			}
		}
		if rawRequired, ok := params["required"]; ok {
			switch typed := rawRequired.(type) {
			case []string:
				for _, value := range typed {
					if strings.TrimSpace(value) == "" {
						problems = append(problems, fmt.Sprintf("%s has an empty required field", name))
					}
				}
			case []any:
				for _, value := range typed {
					if s, ok := value.(string); !ok || strings.TrimSpace(s) == "" {
						problems = append(problems, fmt.Sprintf("%s has non-string required field %#v", name, value))
					}
				}
			default:
				problems = append(problems, fmt.Sprintf("%s required=%T, want []string/[]any", name, rawRequired))
			}
		}
		if rawAdditional, ok := params["additionalProperties"]; ok {
			if _, ok := rawAdditional.(bool); !ok {
				problems = append(problems, fmt.Sprintf("%s additionalProperties=%T, want bool", name, rawAdditional))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("registered tool schema contract problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestLocalFilesAndRepositoriesExposeBoundedSchemas(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}
	for _, meta := range AllToolMetadata() {
		if meta.ToolSetID != "local-files" {
			continue
		}
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		params := tool.Definition().Function.Parameters
		if got, ok := params["additionalProperties"].(bool); !ok || got {
			problems = append(problems, fmt.Sprintf("%s additionalProperties=%#v, want false", meta.Name, params["additionalProperties"]))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("local file/repository tools must expose bounded schemas:\n%s", strings.Join(problems, "\n"))
	}
}

func TestAttachmentAndThreadExportToolsExposeBoundedSchemas(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}
	for _, meta := range AllToolMetadata() {
		if meta.ToolSetID != "attachments" && meta.Name != "export_thread" {
			continue
		}
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		params := tool.Definition().Function.Parameters
		if got, ok := params["additionalProperties"].(bool); !ok || got {
			problems = append(problems, fmt.Sprintf("%s additionalProperties=%#v, want false", meta.Name, params["additionalProperties"]))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("attachment/thread-export tools must expose bounded schemas:\n%s", strings.Join(problems, "\n"))
	}
}

func TestThreadSearchToolsExposeBoundedSchemas(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}
	for _, meta := range AllToolMetadata() {
		if meta.ToolSetID != "thread-search" {
			continue
		}
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		params := tool.Definition().Function.Parameters
		if got, ok := params["additionalProperties"].(bool); !ok || got {
			problems = append(problems, fmt.Sprintf("%s additionalProperties=%#v, want false", meta.Name, params["additionalProperties"]))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("thread-search tools must expose bounded schemas:\n%s", strings.Join(problems, "\n"))
	}
}

func TestQueryLifecycleToolsExposeBoundedSchemas(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}
	for _, meta := range AllToolMetadata() {
		if meta.ToolSetID != "query-lifecycle" {
			continue
		}
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		params := tool.Definition().Function.Parameters
		if got, ok := params["additionalProperties"].(bool); !ok || got {
			problems = append(problems, fmt.Sprintf("%s additionalProperties=%#v, want false", meta.Name, params["additionalProperties"]))
		}
		properties, _ := params["properties"].(map[string]any)
		if meta.Name == "get_query_results" {
			limit, _ := properties["limit"].(map[string]any)
			if limit["type"] != "integer" || limit["minimum"] != 1 || limit["maximum"] != maxQueryPageSize {
				problems = append(problems, fmt.Sprintf("%s limit schema=%#v, want integer range 1..%d", meta.Name, limit, maxQueryPageSize))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("query-lifecycle tools must expose bounded schemas:\n%s", strings.Join(problems, "\n"))
	}
}

func TestToolMetadataShapeIsComplete(t *testing.T) {
	seen := map[string]struct{}{}
	for _, meta := range AllToolMetadata() {
		if _, ok := seen[meta.Name]; ok {
			t.Fatalf("duplicate metadata name %q", meta.Name)
		}
		seen[meta.Name] = struct{}{}

		if strings.TrimSpace(meta.Name) == "" {
			t.Fatal("metadata contains empty name")
		}
		if strings.TrimSpace(meta.Label) == "" {
			t.Fatalf("%s label is empty", meta.Name)
		}
		if strings.Contains(meta.Label, "_") {
			t.Fatalf("%s label %q exposes internal naming", meta.Name, meta.Label)
		}
		if strings.TrimSpace(meta.Description) == "" {
			t.Fatalf("%s description is empty", meta.Name)
		}
		if strings.TrimSpace(meta.ToolSetID) == "" || strings.TrimSpace(meta.ToolSetName) == "" {
			t.Fatalf("%s tool set metadata is incomplete: %#v", meta.Name, meta)
		}
		switch meta.Category {
		case ToolCategoryRead, ToolCategoryWrite, ToolCategoryExecute:
		default:
			t.Fatalf("%s category = %q, want read/write/execute", meta.Name, meta.Category)
		}
		switch meta.Origin {
		case ToolOriginBuiltin, ToolOriginConnected, ToolOriginLocal, ToolOriginAttached:
		default:
			t.Fatalf("%s origin = %q, want builtin/connected/local/attached", meta.Name, meta.Origin)
		}
		if meta.Origin == ToolOriginConnected && strings.TrimSpace(meta.ConnectorID) == "" {
			t.Fatalf("%s connected tool has no connector id", meta.Name)
		}
		if meta.Origin != ToolOriginConnected && strings.TrimSpace(meta.ConnectorID) != "" {
			t.Fatalf("%s non-connected tool unexpectedly has connector id %q", meta.Name, meta.ConnectorID)
		}
		if strings.TrimSpace(meta.Presentation.Family) == "" ||
			strings.TrimSpace(meta.Presentation.ResultKind) == "" ||
			strings.TrimSpace(meta.Presentation.RendererStrategy) == "" ||
			strings.TrimSpace(meta.Presentation.RendererKey) == "" ||
			strings.TrimSpace(meta.Presentation.NativeViewer) == "" {
			t.Fatalf("%s presentation metadata is incomplete: %#v", meta.Name, meta.Presentation)
		}
		requireKnownPresentationValue(t, meta.Name, "result_kind", meta.Presentation.ResultKind, knownResultKinds())
		requireKnownPresentationValue(t, meta.Name, "renderer_strategy", meta.Presentation.RendererStrategy, knownRendererStrategies())
		requireKnownPresentationValue(t, meta.Name, "native_viewer", meta.Presentation.NativeViewer, knownNativeViewers())
		if strings.TrimSpace(meta.Presentation.FileCapability) != "" {
			requireKnownPresentationValue(t, meta.Name, "file_capability", meta.Presentation.FileCapability, knownFileCapabilities())
		}
		requireNoEmptyOrDuplicateStrings(t, meta.Name, "presentation.artifact_types", meta.Presentation.ArtifactTypes)
		for _, value := range meta.Presentation.ArtifactTypes {
			requireKnownPresentationValue(t, meta.Name, "artifact_types", value, knownArtifactTypes())
		}
		requireNoEmptyOrDuplicateStrings(t, meta.Name, "presentation.file_kinds", meta.Presentation.FileKinds)
		for _, value := range meta.Presentation.FileKinds {
			requireKnownPresentationValue(t, meta.Name, "file_kinds", value, knownFileKinds())
		}
		requireNoEmptyOrDuplicateStrings(t, meta.Name, "presentation.preview_modes", meta.Presentation.PreviewModes)
		for _, value := range meta.Presentation.PreviewModes {
			requireKnownPresentationValue(t, meta.Name, "preview_modes", value, knownPreviewModes())
		}
		requireNoEmptyOrDuplicateStrings(t, meta.Name, "presentation.source_refs", meta.Presentation.SourceRefs)
		for _, value := range meta.Presentation.SourceRefs {
			requireKnownPresentationValue(t, meta.Name, "source_refs", value, knownSourceRefs())
		}
		requireNoEmptyOrDuplicateStrings(t, meta.Name, "presentation.native_links", meta.Presentation.NativeLinks)
		for _, value := range meta.Presentation.NativeLinks {
			requireKnownPresentationValue(t, meta.Name, "native_links", value, knownNativeLinks())
		}

		switch meta.Test.Mode {
		case ToolTestModeNone:
			if meta.Test.Supported {
				t.Fatalf("%s test mode is none but supported=true", meta.Name)
			}
			if meta.Test.RequiresApproval {
				t.Fatalf("%s test mode is none but requires approval", meta.Name)
			}
		case ToolTestModeConnectionCheck, ToolTestModeSampleCall, ToolTestModeDryRun:
			if !meta.Test.Supported {
				t.Fatalf("%s test mode %q requires supported=true", meta.Name, meta.Test.Mode)
			}
			if strings.TrimSpace(meta.Test.Summary) == "" {
				t.Fatalf("%s supported test has no summary", meta.Name)
			}
			if (len(meta.Test.RequiredArguments) > 0 || len(meta.Test.RequiredArgumentGroups) > 0) && !meta.Test.InputRequired {
				t.Fatalf("%s required arguments/groups must set input_required=true", meta.Name)
			}
			if len(meta.Test.RequiredArguments) == 0 && len(meta.Test.RequiredArgumentGroups) == 0 && meta.Test.InputRequired {
				t.Fatalf("%s input_required=true but no required arguments/groups are listed", meta.Name)
			}
			if len(meta.Test.RequiredArguments) > 0 || len(meta.Test.RequiredArgumentGroups) > 0 || len(meta.Test.RequiredContext) > 0 {
				if strings.TrimSpace(meta.Test.InputHint) == "" {
					t.Fatalf("%s test metadata has requirements but no input hint", meta.Name)
				}
			}
			requireNoEmptyOrDuplicateStrings(t, meta.Name, "required_arguments", meta.Test.RequiredArguments)
			for idx, group := range meta.Test.RequiredArgumentGroups {
				requireNoEmptyOrDuplicateStrings(t, meta.Name, fmt.Sprintf("required_argument_groups[%d]", idx), group)
			}
			requireNoEmptyOrDuplicateStrings(t, meta.Name, "required_context", meta.Test.RequiredContext)
			if meta.Category != ToolCategoryRead && !meta.Test.RequiresApproval {
				t.Fatalf("%s write/execute test must require approval", meta.Name)
			}
			if meta.Category != ToolCategoryRead && meta.Test.Mode != ToolTestModeDryRun {
				t.Fatalf("%s write/execute test mode = %q, want dry_run", meta.Name, meta.Test.Mode)
			}
		default:
			t.Fatalf("%s test mode = %q, want none/connection_check/sample_call/dry_run", meta.Name, meta.Test.Mode)
		}
	}
}

func TestAllToolMetadataSupportsSettingsToolTests(t *testing.T) {
	unsupported := []string{}
	for _, meta := range AllToolMetadata() {
		if !meta.Test.Supported || meta.Test.Mode == ToolTestModeNone {
			unsupported = append(unsupported, fmt.Sprintf("%s(mode=%s supported=%v)", meta.Name, meta.Test.Mode, meta.Test.Supported))
		}
	}
	if len(unsupported) > 0 {
		sort.Strings(unsupported)
		t.Fatalf("all registered tools must expose a settings-page test path; unsupported tools: %s", strings.Join(unsupported, ", "))
	}
}

func TestAllToolMetadataIsDeterministicAndLookupTrimsName(t *testing.T) {
	all := AllToolMetadata()
	if len(all) == 0 {
		t.Fatal("AllToolMetadata returned no tools")
	}
	names := make([]string, 0, len(all))
	for _, meta := range all {
		names = append(names, meta.Name)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("AllToolMetadata is not sorted by name: %v", names)
	}

	got, ok := ToolMetadataByName("  " + GitHubToolCloneRepository + "  ")
	if !ok {
		t.Fatalf("ToolMetadataByName did not trim %q", GitHubToolCloneRepository)
	}
	if got.Name != GitHubToolCloneRepository || got.Category != ToolCategoryExecute || got.Test.Mode != ToolTestModeDryRun {
		t.Fatalf("GitHub clone metadata = %#v, want execute dry_run metadata", got)
	}
}

func TestConnectedToolNameHelpersMatchToolMetadata(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	cases := []struct {
		label      string
		names      []string
		connector  string
		categories []ToolPermissionCategory
	}{
		{label: "GitHub", names: GitHubToolNames(), connector: "github", categories: []ToolPermissionCategory{ToolCategoryRead, ToolCategoryExecute}},
		{label: "Slack read", names: SlackReadToolNames(), connector: "slack", categories: []ToolPermissionCategory{ToolCategoryRead}},
		{label: "Slack write", names: SlackWriteToolNames(), connector: "slack", categories: []ToolPermissionCategory{ToolCategoryWrite}},
		{label: "Atlassian", names: append(append(AtlassianSharedToolNames(), AtlassianJiraToolNames()...), AtlassianConfluenceToolNames()...), connector: "atlassian", categories: []ToolPermissionCategory{ToolCategoryRead, ToolCategoryWrite}},
		{label: "SharePoint", names: SharePointToolNames(), connector: "sharepoint", categories: []ToolPermissionCategory{ToolCategoryRead}},
		{label: "Browser", names: BrowserControlToolNames(), connector: "browser", categories: []ToolPermissionCategory{ToolCategoryRead, ToolCategoryExecute}},
		{label: "arXiv", names: ArxivToolNames(), connector: "arxiv", categories: []ToolPermissionCategory{ToolCategoryRead}},
		{label: "BigQuery", names: DatasourceToolNames("bigquery"), connector: "bigquery", categories: []ToolPermissionCategory{ToolCategoryRead}},
		{label: "Databricks", names: DatasourceToolNames("databricks"), connector: "databricks", categories: []ToolPermissionCategory{ToolCategoryRead}},
		{label: "Trino", names: DatasourceToolNames("trino"), connector: "trino", categories: []ToolPermissionCategory{ToolCategoryRead}},
		{label: "Snowflake", names: DatasourceToolNames("snowflake"), connector: "snowflake", categories: []ToolPermissionCategory{ToolCategoryRead}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			requireNoEmptyOrDuplicateStrings(t, tc.label, "tool names", tc.names)
			want := connectedMetadataNames(tc.connector, tc.categories...)
			if !sameStringsUnordered(tc.names, want) {
				t.Fatalf("%s helper names=%v, want metadata names=%v", tc.label, tc.names, want)
			}
			gotDefs := toolDefinitionNames(registry.DefinitionsByName(tc.names...))
			if !sameStringsUnordered(gotDefs, tc.names) {
				t.Fatalf("%s registry definitions=%v, want helper names=%v", tc.label, gotDefs, tc.names)
			}
		})
	}
}

func TestRegistryDefaultDefinitionSurfaceMatchesCanonicalToolSets(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	want := canonicalDefaultDefinitionToolNames()
	got := toolDefinitionNames(registry.Definitions())
	if !sameStringsUnordered(got, want) {
		t.Fatalf("default registry definitions=%v, want canonical default tool names=%v", got, want)
	}

	for _, name := range got {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("default registry definition %s has no product metadata", name)
		}
		if meta.ToolSetID == "thread-search" {
			t.Fatalf("default registry definition %s is thread-search scoped; it must be exposed by thread/search surfaces", name)
		}
		if meta.Origin == ToolOriginConnected && !containsString(DatasourceNames(), meta.ToolSetID) {
			t.Fatalf("default registry definition %s is connected to %s; connected tools must be exposed by connector surfaces", name, meta.ToolSetID)
		}
	}
}

func TestRegistryNamedDefinitionSurfacesMatchCanonicalToolSets(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	cases := []struct {
		label string
		got   []string
		want  []string
	}{
		{label: "analysis core", got: toolDefinitionNames(registry.AnalysisCoreDefinitions()), want: analysisCoreToolNames},
		{label: "local grant", got: toolDefinitionNames(registry.LocalGrantToolDefinitions()), want: localGrantToolNames},
		{label: "csv", got: toolDefinitionNames(registry.CSVToolDefinitions()), want: csvToolNames},
		{label: "bigquery", got: toolDefinitionNames(registry.BigQueryToolDefinitions()), want: bigQueryToolNames},
		{label: "databricks", got: toolDefinitionNames(registry.DatabricksToolDefinitions()), want: databricksToolNames},
		{label: "trino", got: toolDefinitionNames(registry.TrinoToolDefinitions()), want: trinoToolNames},
		{label: "snowflake", got: toolDefinitionNames(registry.SnowflakeToolDefinitions()), want: snowflakeToolNames},
		{label: "datasource bigquery", got: toolDefinitionNames(registry.DatasourceToolDefinitions("bigquery")), want: bigQueryToolNames},
		{label: "datasource databricks", got: toolDefinitionNames(registry.DatasourceToolDefinitions("databricks")), want: databricksToolNames},
		{label: "datasource trino", got: toolDefinitionNames(registry.DatasourceToolDefinitions("trino")), want: trinoToolNames},
		{label: "datasource snowflake", got: toolDefinitionNames(registry.DatasourceToolDefinitions("snowflake")), want: snowflakeToolNames},
		{label: "thread-search row", got: toolDefinitionNames(registry.ThreadSearchToolDefinitions(true)), want: []string{"search_threads"}},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			want := uniqueSortedStrings(tc.want...)
			if !sameStringsUnordered(tc.got, want) {
				t.Fatalf("%s definitions=%v, want %v", tc.label, tc.got, want)
			}
			for _, name := range want {
				if _, ok := ToolMetadataByName(name); !ok {
					t.Fatalf("%s surface references %s without product metadata", tc.label, name)
				}
			}
		})
	}

	if got := registry.DatasourceToolDefinitions("unknown"); len(got) != 0 {
		t.Fatalf("unknown datasource definitions=%v, want none", toolDefinitionNames(got))
	}
	if got := registry.ThreadSearchToolDefinitions(false); len(got) != 0 {
		t.Fatalf("disabled thread-search definitions=%v, want none", toolDefinitionNames(got))
	}
}

func TestLocalGrantDefinitionSurfaceIsScopedToInternalGrantTools(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	defaultNames := stringSet(toolDefinitionNames(registry.Definitions())...)
	analysisCoreNames := stringSet(toolDefinitionNames(registry.AnalysisCoreDefinitions())...)
	shallowSearchNames := stringSet(toolDefinitionNames(registry.DefinitionsForSearch("shallow"))...)
	deepSearchNames := stringSet(toolDefinitionNames(registry.DefinitionsForSearch("deep"))...)

	got := toolDefinitionNames(registry.LocalGrantToolDefinitions())
	want := []string{"local_list_grant_folder", LocalSearchCommandToolName, "local_search_in_grant", "local_read_grant_file"}
	if !sameStringsUnordered(got, want) {
		t.Fatalf("local grant definitions=%v, want %v", got, want)
	}
	for _, name := range want {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("local grant tool %s has no metadata", name)
		}
		wantCategory := ToolCategoryRead
		if name == LocalSearchCommandToolName {
			wantCategory = ToolCategoryExecute
		}
		if meta.ToolSetID != "local-files" || meta.Category != wantCategory || meta.Origin != ToolOriginLocal {
			t.Fatalf("metadata for %s=%#v, want local-files local %s metadata", name, meta, wantCategory)
		}
		if _, ok := defaultNames[name]; ok {
			t.Fatalf("internal local grant tool %s must not be in default model definitions", name)
		}
		if _, ok := analysisCoreNames[name]; ok {
			t.Fatalf("internal local grant tool %s must not be in analysis core definitions", name)
		}
		if _, ok := shallowSearchNames[name]; ok {
			t.Fatalf("internal local grant tool %s must not be advertised in shallow search definitions", name)
		}
		if _, ok := deepSearchNames[name]; ok {
			t.Fatalf("internal local grant tool %s must not be advertised in deep search definitions", name)
		}
	}
}

func TestSearchDefinitionSurfacesMatchAllowedToolSets(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	cases := []struct {
		depth   string
		defs    []types.ToolDefinition
		allowed map[string]struct{}
	}{
		{depth: "shallow", defs: registry.DefinitionsForSearch("shallow"), allowed: registry.searchAllowShallow},
		{depth: "deep", defs: registry.DefinitionsForSearch("deep"), allowed: registry.searchAllowDeep},
	}
	aliases := searchDefinitionAliases()
	for _, tc := range cases {
		t.Run(tc.depth, func(t *testing.T) {
			defNames := toolDefinitionNames(tc.defs)
			defSet := stringSet(defNames...)

			for _, name := range defNames {
				if _, ok := tc.allowed[name]; !ok {
					t.Fatalf("%s search definition %s is not allowed for search execution", tc.depth, name)
				}
				if _, ok := ToolMetadataByName(name); !ok {
					t.Fatalf("%s search definition %s has no product metadata", tc.depth, name)
				}
			}

			missingDefinitions := []string{}
			for name := range tc.allowed {
				if _, ok := registry.byName[name]; !ok {
					t.Fatalf("%s search allowlist references unregistered tool %s", tc.depth, name)
				}
				if _, ok := ToolMetadataByName(name); !ok {
					t.Fatalf("%s search allowlist references tool %s without product metadata", tc.depth, name)
				}
				if _, alias := aliases[name]; alias {
					if _, shown := defSet[name]; shown {
						t.Fatalf("%s search alias %s should be allowed for execution but not advertised as a separate definition", tc.depth, name)
					}
					continue
				}
				if _, ok := defSet[name]; !ok {
					missingDefinitions = append(missingDefinitions, name)
				}
			}
			if len(missingDefinitions) > 0 {
				sort.Strings(missingDefinitions)
				t.Fatalf("%s search allowlist tools missing from search definitions: %s", tc.depth, strings.Join(missingDefinitions, ", "))
			}
		})
	}

	if defs := registry.DefinitionsForSearch("off"); len(defs) != 0 {
		t.Fatalf("off search definitions=%v, want none", toolDefinitionNames(defs))
	}
}

func TestDryRunToolMetadataRequiresDryRunParameter(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	for name, meta := range toolMetadataManifest() {
		if meta.Test.Mode != ToolTestModeDryRun {
			continue
		}
		tool, ok := registry.byName[name]
		if !ok {
			t.Fatalf("%s metadata exists but tool is not registered", name)
		}
		def := tool.Definition()
		properties, _ := def.Function.Parameters["properties"].(map[string]any)
		if _, ok := properties["dry_run"]; !ok {
			t.Fatalf("%s metadata declares dry_run test mode but schema does not expose dry_run", name)
		}
	}
}

func TestDryRunToolBehaviorCoverageCatalog(t *testing.T) {
	behaviorCovered := map[string][]string{
		LocalSearchCommandToolName:  {"TestLocalSearchCommandDryRunDoesNotStartProcess"},
		generateImageToolName:       {"TestGenerateImageDryRunDoesNotCallProvider"},
		"cancel_query":              {"TestCancelQueryToolDryRunDoesNotCancelRunningJob"},
		"rlm_exec":                  {"TestRLMExecToolDryRunDoesNotRequireRuntimeOrRunContext", "TestRLMExecToolDryRunDoesNotMutateWorkspace", "TestRLMExecToolDryRunValidatesSyntax"},
		GitHubToolCloneRepository:   {"TestGitHubCloneDryRunDoesNotQueueOrEmitApproval"},
		GitHubToolPullRepository:    {"TestGitHubRepositoryWriteDryRunsDoNotQueueOrExecute"},
		GitHubToolRefreshRepository: {"TestGitHubRepositoryWriteDryRunsDoNotQueueOrExecute"},
		GitHubToolRunGHCommand:      {"TestGitHubRawGHDryRunDoesNotRunCommandOrQueueApproval"},
		SlackToolPostMessage:        {"TestSlackPostMessageDryRunDoesNotQueueApprovalOrCallSlack"},
		SlackToolUploadFile:         {"TestSlackUploadFileDryRunDoesNotQueueApprovalOrCallSlack"},
		"jira_add_comment":          {"TestJiraWriteDryRunsDoNotQueueLegacyApproval"},
		"jira_reply_to_comment":     {"TestJiraWriteDryRunsDoNotQueueLegacyApproval"},
		"jira_add_related_tickets":  {"TestJiraWriteDryRunsDoNotQueueLegacyApproval", "TestJiraAddRelatedTicketsRequiresPersistentPermission"},
		"jira_upload_attachment":    {"TestJiraUploadAttachmentDryRunIncludesTextPreview"},
		"jira_update_issue":         {"TestJiraUpdateIssueDryRunNormalizesFields"},
		"jira_create_issue":         {"TestJiraCreateIssueDryRunNormalizesComponents"},
		BrowserControlToolNavigate:  {"TestBrowserControlDryRunsUseFirstClassResultMetadata"},
		BrowserControlToolClick:     {"TestBrowserControlDryRunValidatesTargetArguments"},
		BrowserControlToolType:      {"TestBrowserControlDryRunValidatesRequiredArguments", "TestBrowserControlDryRunValidatesTargetArguments"},
		BrowserControlToolPress:     {"TestBrowserControlDryRunsUseFirstClassResultMetadata"},
		BrowserControlToolWait:      {"TestBrowserControlDryRunsUseFirstClassResultMetadata"},
		BrowserControlToolBack:      {"TestBrowserControlDryRunsUseFirstClassResultMetadata"},
		BrowserControlToolClearLogs: {"TestBrowserControlDryRunsUseFirstClassResultMetadata"},
	}
	testSource := readToolPackageTestSource(t)
	missing := []string{}
	for name, meta := range toolMetadataManifest() {
		if meta.Test.Mode != ToolTestModeDryRun {
			continue
		}
		markers, ok := behaviorCovered[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		for _, marker := range markers {
			if !strings.Contains(testSource, marker) {
				missing = append(missing, fmt.Sprintf("%s missing focused test %s", name, marker))
			}
		}
	}
	for name := range behaviorCovered {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			missing = append(missing, fmt.Sprintf("%s is listed for dry-run behavior coverage but has no metadata", name))
			continue
		}
		if meta.Test.Mode != ToolTestModeDryRun {
			missing = append(missing, fmt.Sprintf("%s is listed for dry-run behavior coverage but mode=%s", name, meta.Test.Mode))
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("dry-run tools need named focused no-mutation/approval tests: %s", strings.Join(missing, ", "))
	}
}

func TestToolTestMetadataDocumentsRequiredArguments(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}
	for name, meta := range toolMetadataManifest() {
		if !meta.Test.Supported {
			continue
		}
		tool, ok := registry.byName[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but tool is not registered", name))
			continue
		}
		required := toolMetadataRequiredFields(tool.Definition().Function.Parameters)
		if toolTestUsesSyntheticArguments(name) {
			continue
		}
		for _, arg := range required {
			if !containsString(meta.Test.RequiredArguments, arg) && !stringMatrixContainsGroup(meta.Test.RequiredArgumentGroups, []string{arg}) {
				problems = append(problems, fmt.Sprintf("%s schema requires %q but test metadata required_arguments=%v required_argument_groups=%v", name, arg, meta.Test.RequiredArguments, meta.Test.RequiredArgumentGroups))
			}
		}
		if len(required) > 0 && !meta.Test.InputRequired {
			problems = append(problems, fmt.Sprintf("%s schema has required arguments %v but input_required=false", name, required))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("tool test metadata required argument problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestQueryLifecycleToolTestsUseSyntheticJobs(t *testing.T) {
	for _, name := range []string{"check_query_status", "get_query_results", "validate_query_result", "cancel_query"} {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if !meta.Test.Supported {
			t.Fatalf("%s test unsupported, want synthetic tool test", name)
		}
		if len(meta.Test.RequiredArguments) > 0 {
			t.Fatalf("%s required_arguments=%v, want synthetic test with no user job handle", name, meta.Test.RequiredArguments)
		}
		if len(meta.Test.RequiredContext) > 0 {
			t.Fatalf("%s required_context=%v, want synthetic test with no query job context", name, meta.Test.RequiredContext)
		}
		if meta.Test.InputRequired {
			t.Fatalf("%s input_required=true, want false for synthetic test", name)
		}
	}
}

func TestToolTestMetadataDocumentsAlternativeArguments(t *testing.T) {
	cases := map[string][][]string{
		"jira_update_issue":       {{"summary"}, {"description"}, {"fields"}},
		SharePointToolGetMetadata: {{"item_url"}, {"server_relative_url"}},
		SharePointToolRead:        {{"item_url"}, {"server_relative_url"}},
		SharePointToolSummarize:   {{"item_url"}, {"server_relative_url"}},
		SharePointToolDownload:    {{"item_url"}, {"server_relative_url"}},
		BrowserControlToolClick:   {{"index"}, {"selector"}, {"text"}},
		BrowserControlToolType:    {{"index"}, {"selector"}, {"text"}},
		SlackToolUploadFile:       {{"fileName", "contentText"}, {"fileName", "contentBase64"}, {"artifact_id"}, {"path"}, {"local_path"}},
		"jira_upload_attachment":  {{"fileName", "contentText"}, {"fileName", "contentBase64"}, {"artifact_id"}, {"path"}, {"local_path"}},
		"display_table":           {{"data_json"}, {"source"}},
		"jira_find":               {{"query"}, {"projectOrBoard"}},
		"confluence_open_page":    {{"url"}, {"pageId"}, {"spaceKey", "title"}},
		ArxivToolSearch:           {{"query"}, {"categories"}, {"authors"}, {"ids"}},
	}
	for name, wantGroups := range cases {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		for _, want := range wantGroups {
			if !stringMatrixContainsGroup(meta.Test.RequiredArgumentGroups, want) {
				t.Fatalf("%s required_argument_groups=%v, want group %v", name, meta.Test.RequiredArgumentGroups, want)
			}
		}
		if !meta.Test.InputRequired {
			t.Fatalf("%s input_required=false, want true for grouped arguments", name)
		}
	}
}

func TestUploadToolTestMetadataDefaultsToSelfContainedInput(t *testing.T) {
	for _, name := range []string{SlackToolUploadFile, "jira_upload_attachment"} {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if len(meta.Test.RequiredArgumentGroups) == 0 {
			t.Fatalf("%s required_argument_groups empty, want inline upload source first", name)
		}
		if strings.Join(meta.Test.RequiredArgumentGroups[0], "|") != "fileName|contentText" {
			t.Fatalf("%s first required_argument_group=%v, want [fileName contentText]", name, meta.Test.RequiredArgumentGroups[0])
		}
	}
}

func TestToolTestMetadataProvidesSampleArgumentsForAllInputs(t *testing.T) {
	problems := []string{}
	for _, meta := range AllToolMetadata() {
		if !meta.Test.Supported {
			continue
		}
		inputs := append([]string(nil), meta.Test.RequiredArguments...)
		for _, group := range meta.Test.RequiredArgumentGroups {
			inputs = append(inputs, group...)
		}
		if len(inputs) == 0 {
			if len(meta.Test.SampleArguments) > 0 {
				problems = append(problems, fmt.Sprintf("%s declares sample_arguments=%v without required inputs", meta.Name, sortedMapKeys(meta.Test.SampleArguments)))
			}
			continue
		}
		if len(meta.Test.SampleArguments) == 0 {
			problems = append(problems, fmt.Sprintf("%s has required tool-test inputs but no sample_arguments", meta.Name))
			continue
		}
		for _, input := range inputs {
			input = strings.TrimSpace(input)
			if input == "" {
				continue
			}
			value, ok := meta.Test.SampleArguments[input]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s sample_arguments missing %q", meta.Name, input))
				continue
			}
			if _, err := json.Marshal(value); err != nil {
				problems = append(problems, fmt.Sprintf("%s sample_arguments[%s] is not JSON-serializable: %v", meta.Name, input, err))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("tool-test sample argument problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestToolTestContextIsScopedToActualRuntimeNeeds(t *testing.T) {
	noArtifactContext := []string{
		"create_interactive_artifact",
		"display_table",
	}
	for _, name := range noArtifactContext {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if containsString(meta.Test.RequiredContext, "artifact") {
			t.Fatalf("%s required_context=%v, want no artifact context for inline test inputs", name, meta.Test.RequiredContext)
		}
	}

	noProjectContext := []string{
		"inspect_dataset_artifact",
		"get_dataset_rows",
		"aggregate_dataset",
		"search_dataset_rows",
	}
	for _, name := range noProjectContext {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if containsString(meta.Test.RequiredContext, "project") {
			t.Fatalf("%s required_context=%v, want no project context for artifact-backed dataset tool", name, meta.Test.RequiredContext)
		}
	}

	for _, name := range []string{"describe_dataset", "sample_rows", "filter_csv", "aggregate_csv"} {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if !containsString(meta.Test.RequiredContext, "project") {
			t.Fatalf("%s required_context=%v, want project context for CSV project-file tool", name, meta.Test.RequiredContext)
		}
	}
}

func TestToolTestMetadataDocumentsSemanticRuntimeInputs(t *testing.T) {
	cases := map[string][]string{
		"display_table":          {"data_json"},
		"run_bigquery_query":     {"sql"},
		"list_bq_tables":         {"dataset_id"},
		"get_bq_table_schema":    {"dataset_id", "table_id"},
		SlackToolUploadFile:      {"channel_id"},
		"jira_upload_attachment": {"issueKey"},
		"jira_update_issue":      {"issueKey"},
		ArxivToolSearch:          {"query"},
		BrowserControlToolType:   {"value"},
	}
	for name, wantArgs := range cases {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if !meta.Test.Supported {
			t.Fatalf("%s test unsupported, want supported semantic test input metadata", name)
		}
		for _, arg := range wantArgs {
			if !containsString(meta.Test.RequiredArguments, arg) && !stringMatrixContainsGroup(meta.Test.RequiredArgumentGroups, []string{arg}) {
				t.Fatalf("%s required_arguments=%v required_argument_groups=%v, want semantic runtime input %q", name, meta.Test.RequiredArguments, meta.Test.RequiredArgumentGroups, arg)
			}
		}
		if !meta.Test.InputRequired {
			t.Fatalf("%s input_required=false, want true", name)
		}
	}
}

func TestBigQueryToolTestsUseConfiguredDefaultProjectWhenPossible(t *testing.T) {
	cases := map[string][]string{
		"run_bigquery_query":  {"sql"},
		"list_bq_datasets":    nil,
		"list_bq_tables":      {"dataset_id"},
		"get_bq_table_schema": {"dataset_id", "table_id"},
	}
	for name, want := range cases {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if containsString(meta.Test.RequiredArguments, "project_id") {
			t.Fatalf("%s required_arguments=%v, want project_id optional so configured default projects can be tested without manual input", name, meta.Test.RequiredArguments)
		}
		if !sameStringsUnordered(meta.Test.RequiredArguments, want) {
			t.Fatalf("%s required_arguments=%v, want %v", name, meta.Test.RequiredArguments, want)
		}
	}
}

func TestToolMetadataRiskClassifications(t *testing.T) {
	reviewedWriteTools := stringSet(
		SlackToolPostMessage,
		SlackToolUploadFile,
		"jira_add_comment",
		"jira_reply_to_comment",
		"jira_add_related_tickets",
		"jira_upload_attachment",
		"jira_update_issue",
		"jira_create_issue",
	)

	reviewedExecuteTools := stringSet(
		"cancel_query",
		generateImageToolName,
		"rlm_exec",
		LocalSearchCommandToolName,
		GitHubToolCloneRepository,
		GitHubToolPullRepository,
		GitHubToolRefreshRepository,
		GitHubToolRunGHCommand,
		BrowserControlToolNavigate,
		BrowserControlToolClick,
		BrowserControlToolType,
		BrowserControlToolPress,
		BrowserControlToolWait,
		BrowserControlToolBack,
		BrowserControlToolClearLogs,
	)
	problems := []string{}
	for name, meta := range toolMetadataManifest() {
		switch meta.Category {
		case ToolCategoryWrite:
			if _, ok := reviewedWriteTools[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s is write but missing from reviewed write risk list", name))
			}
			delete(reviewedWriteTools, name)
		case ToolCategoryExecute:
			if _, ok := reviewedExecuteTools[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s is execute but missing from reviewed execute risk list", name))
			}
			delete(reviewedExecuteTools, name)
		}
	}
	for name := range reviewedWriteTools {
		problems = append(problems, fmt.Sprintf("%s is in reviewed write risk list but is not a registered write tool", name))
	}
	for name := range reviewedExecuteTools {
		problems = append(problems, fmt.Sprintf("%s is in reviewed execute risk list but is not a registered execute tool", name))
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("tool risk classification review problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestToolPresentationNativeViewerContracts(t *testing.T) {
	type boolExpectation struct {
		check bool
		want  bool
	}
	cases := []struct {
		name                 string
		resultKind           string
		nativeViewer         string
		fileCapability       string
		rendererStrategy     string
		rendererKey          string
		artifactTypes        []string
		fileKinds            []string
		previewModes         []string
		sourceRefs           []string
		nativeLinks          []string
		producesFileArtifact boolExpectation
		promotesNativeInput  boolExpectation
	}{
		{name: "read_file", resultKind: "file", nativeViewer: "code", fileCapability: "local_file_read", rendererStrategy: "artifact_type", rendererKey: "code", artifactTypes: []string{"code"}, sourceRefs: []string{"path", "local_path"}, promotesNativeInput: boolExpectation{check: true, want: true}},
		{name: "local_read_file", resultKind: "file", nativeViewer: "code", fileCapability: "local_file_read", rendererStrategy: "artifact_type", rendererKey: "code", artifactTypes: []string{"code"}, sourceRefs: []string{"path", "local_path"}, promotesNativeInput: boolExpectation{check: true, want: true}},
		{name: "local_open_file", resultKind: "file", nativeViewer: "file", fileCapability: "local_file_read", rendererStrategy: "artifact_type", rendererKey: "file", artifactTypes: []string{"file"}, fileKinds: []string{"pdf", "image", "text", "html", "document", "spreadsheet", "presentation", "archive"}, previewModes: []string{"raw_download", "text_preview", "pdf_canvas", "image_raw", "html_sandbox", "spreadsheet_workbook", "presentation_slides", "download_only"}, sourceRefs: []string{"artifact_id", "path", "local_path"}, producesFileArtifact: boolExpectation{check: true, want: true}, promotesNativeInput: boolExpectation{check: true, want: true}},
		{name: "local_list_folder", resultKind: "folder", nativeViewer: "connector_inspector", fileCapability: "local_folder_list", rendererStrategy: "family", rendererKey: "local-files", fileKinds: []string{"pdf", "image", "text", "html", "document", "spreadsheet", "presentation", "archive"}, previewModes: []string{"raw_download", "text_preview", "pdf_canvas", "image_raw", "html_sandbox", "spreadsheet_workbook", "presentation_slides", "download_only"}, sourceRefs: []string{"path", "local_path"}},
		{name: "git_show_file_at_ref", resultKind: "file", nativeViewer: "code", fileCapability: "local_file_read", rendererStrategy: "artifact_type", rendererKey: "code", artifactTypes: []string{"code"}, sourceRefs: []string{"path", "local_path"}},
		{name: "search_files", resultKind: "search", nativeViewer: "connector_inspector", fileCapability: "local_file_search", rendererStrategy: "family", rendererKey: "local-files", sourceRefs: []string{"path", "local_path"}},
		{name: "inspect_attachment", resultKind: "metadata", nativeViewer: "connector_inspector", fileCapability: "attachment_inspect", rendererStrategy: "family", rendererKey: "attachments", sourceRefs: []string{"artifact_id"}},
		{name: "read_attachment", resultKind: "file", nativeViewer: "file", fileCapability: "attachment_read", rendererStrategy: "artifact_type", rendererKey: "file", artifactTypes: []string{"file"}, fileKinds: []string{"pdf", "image", "text", "html", "document", "spreadsheet", "presentation", "archive"}, previewModes: []string{"raw_download", "text_preview", "pdf_canvas", "image_raw", "html_sandbox", "spreadsheet_workbook", "presentation_slides", "download_only"}, sourceRefs: []string{"artifact_id"}, promotesNativeInput: boolExpectation{check: true, want: true}},
		{name: "search_attachment", resultKind: "search", nativeViewer: "connector_inspector", fileCapability: "attachment_search", rendererStrategy: "family", rendererKey: "attachments", sourceRefs: []string{"artifact_id"}},
		{name: "build_chart_artifact", resultKind: "chart", nativeViewer: "chart", fileCapability: "artifact_create", rendererStrategy: "artifact_type", rendererKey: "chart", artifactTypes: []string{"data"}, sourceRefs: []string{"source.artifact_id", "source.path", "source.job_id", "owner_token"}},
		{name: "display_table", resultKind: "data", nativeViewer: "data", fileCapability: "artifact_create", rendererStrategy: "artifact_type", rendererKey: "data", artifactTypes: []string{"data"}, sourceRefs: []string{"source.artifact_id", "source.path", "source.job_id", "owner_token"}},
		{name: "create_interactive_artifact", resultKind: "interactive", nativeViewer: "interactive_html", fileCapability: "artifact_create", rendererStrategy: "artifact_type", rendererKey: "interactive_html", artifactTypes: []string{"code"}},
		{name: "export_thread", resultKind: "file", nativeViewer: "file", fileCapability: "artifact_create", rendererStrategy: "artifact_type", rendererKey: "file", artifactTypes: []string{"file"}, producesFileArtifact: boolExpectation{check: true, want: true}},
		{name: "run_bigquery_query", resultKind: "query", nativeViewer: "query", rendererStrategy: "artifact_type", rendererKey: "query", artifactTypes: []string{"data"}, sourceRefs: []string{"remote_id"}, nativeLinks: []string{"warehouse_job"}},
		{name: "list_bq_datasets", resultKind: "catalog", nativeViewer: "connector_inspector", rendererStrategy: "tool", rendererKey: "list_bq_datasets", artifactTypes: []string{"data"}, sourceRefs: []string{"project_id"}},
		{name: "get_bq_table_schema", resultKind: "table_schema", nativeViewer: "connector_inspector", rendererStrategy: "tool", rendererKey: "get_bq_table_schema", artifactTypes: []string{"data"}, sourceRefs: []string{"project_id", "dataset_id", "table_id"}},
		{name: "list_databricks_catalogs", resultKind: "catalog", nativeViewer: "connector_inspector", rendererStrategy: "tool", rendererKey: "list_databricks_catalogs", artifactTypes: []string{"data"}, sourceRefs: []string{"connection"}},
		{name: "describe_databricks_table", resultKind: "table_schema", nativeViewer: "connector_inspector", rendererStrategy: "tool", rendererKey: "describe_databricks_table", artifactTypes: []string{"data"}, sourceRefs: []string{"full_name"}},
		{name: "get_query_results", resultKind: "query", nativeViewer: "query", rendererStrategy: "artifact_type", rendererKey: "query", artifactTypes: []string{"data"}, sourceRefs: []string{"remote_id"}, nativeLinks: []string{"warehouse_job"}},
		{name: GitHubToolCloneRepository, resultKind: "repository", nativeViewer: "connector_inspector", fileCapability: "repository_clone", rendererStrategy: "family", rendererKey: "github", sourceRefs: []string{"remote_id", "local_path"}, nativeLinks: []string{"source_url"}},
		{name: SlackToolDownloadFile, resultKind: "file", nativeViewer: "file", fileCapability: "remote_file_download", rendererStrategy: "artifact_type", rendererKey: "file", artifactTypes: []string{"file"}, sourceRefs: []string{"file_id", "channel_id", "remote_id", "artifact_id"}, nativeLinks: []string{"source_url"}, producesFileArtifact: boolExpectation{check: true, want: true}, promotesNativeInput: boolExpectation{check: true, want: true}},
		{name: SlackToolUploadFile, resultKind: "file", nativeViewer: "connector_inspector", fileCapability: "remote_file_upload", rendererStrategy: "family", rendererKey: "slack", sourceRefs: []string{"artifact_id", "path", "local_path", "fileName", "contentText", "contentBase64"}, nativeLinks: []string{"source_url"}},
		{name: "jira_read_attachment", resultKind: "file", nativeViewer: "file", fileCapability: "remote_file_download", rendererStrategy: "artifact_type", rendererKey: "file", artifactTypes: []string{"file"}, sourceRefs: []string{"issueKey", "attachmentId", "remote_id", "artifact_id"}, nativeLinks: []string{"source_url"}, producesFileArtifact: boolExpectation{check: true, want: true}},
		{name: "jira_upload_attachment", resultKind: "file", nativeViewer: "connector_inspector", fileCapability: "remote_file_upload", rendererStrategy: "family", rendererKey: "jira", sourceRefs: []string{"artifact_id", "path", "local_path", "fileName", "contentText", "contentBase64"}, nativeLinks: []string{"source_url"}},
		{name: SharePointToolRead, resultKind: "file", nativeViewer: "file", fileCapability: "remote_file_download", rendererStrategy: "artifact_type", rendererKey: "file", artifactTypes: []string{"file"}, sourceRefs: []string{"item_url", "server_relative_url", "site_url", "remote_id", "artifact_id"}, nativeLinks: []string{"source_url"}, producesFileArtifact: boolExpectation{check: true, want: true}, promotesNativeInput: boolExpectation{check: true, want: true}},
		{name: BrowserControlToolClick, resultKind: "browser", nativeViewer: "connector_inspector", fileCapability: "browser_control", rendererStrategy: "family", rendererKey: "browser", nativeLinks: []string{"browser_url"}},
		{name: ArxivToolDownloadPaper, resultKind: "file", nativeViewer: "file", fileCapability: "remote_file_download", rendererStrategy: "artifact_type", rendererKey: "file", artifactTypes: []string{"file"}, sourceRefs: []string{"arxiv_id", "remote_id", "artifact_id"}, nativeLinks: []string{"source_url"}, producesFileArtifact: boolExpectation{check: true, want: true}, promotesNativeInput: boolExpectation{check: true, want: true}},
		{name: ArxivToolReadPaper, resultKind: "file", nativeViewer: "code", fileCapability: "remote_file_read", rendererStrategy: "artifact_type", rendererKey: "code", artifactTypes: []string{"code"}, sourceRefs: []string{"paper", "arxiv_id", "remote_id", "artifact_id"}, nativeLinks: []string{"source_url"}, promotesNativeInput: boolExpectation{check: true, want: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, ok := ToolMetadataByName(tc.name)
			if !ok {
				t.Fatalf("%s metadata missing", tc.name)
			}
			if meta.Presentation.ResultKind != tc.resultKind ||
				meta.Presentation.NativeViewer != tc.nativeViewer ||
				meta.Presentation.FileCapability != tc.fileCapability {
				t.Fatalf("%s presentation=%#v, want result=%q viewer=%q file=%q", tc.name, meta.Presentation, tc.resultKind, tc.nativeViewer, tc.fileCapability)
			}
			if tc.rendererStrategy != "" && meta.Presentation.RendererStrategy != tc.rendererStrategy {
				t.Fatalf("%s renderer_strategy=%q, want %q", tc.name, meta.Presentation.RendererStrategy, tc.rendererStrategy)
			}
			if tc.rendererKey != "" && meta.Presentation.RendererKey != tc.rendererKey {
				t.Fatalf("%s renderer_key=%q, want %q", tc.name, meta.Presentation.RendererKey, tc.rendererKey)
			}
			if tc.artifactTypes != nil && !sameStringsUnordered(meta.Presentation.ArtifactTypes, tc.artifactTypes) {
				t.Fatalf("%s artifact_types=%v, want %v", tc.name, meta.Presentation.ArtifactTypes, tc.artifactTypes)
			}
			if tc.fileKinds != nil && !sameStringsUnordered(meta.Presentation.FileKinds, tc.fileKinds) {
				t.Fatalf("%s file_kinds=%v, want %v", tc.name, meta.Presentation.FileKinds, tc.fileKinds)
			}
			if tc.previewModes != nil && !sameStringsUnordered(meta.Presentation.PreviewModes, tc.previewModes) {
				t.Fatalf("%s preview_modes=%v, want %v", tc.name, meta.Presentation.PreviewModes, tc.previewModes)
			}
			if tc.sourceRefs != nil && !sameStringsUnordered(meta.Presentation.SourceRefs, tc.sourceRefs) {
				t.Fatalf("%s source_refs=%v, want %v", tc.name, meta.Presentation.SourceRefs, tc.sourceRefs)
			}
			if tc.nativeLinks != nil && !sameStringsUnordered(meta.Presentation.NativeLinks, tc.nativeLinks) {
				t.Fatalf("%s native_links=%v, want %v", tc.name, meta.Presentation.NativeLinks, tc.nativeLinks)
			}
			if tc.producesFileArtifact.check && meta.Presentation.ProducesFileArtifact != tc.producesFileArtifact.want {
				t.Fatalf("%s produces_file_artifact=%v, want %v", tc.name, meta.Presentation.ProducesFileArtifact, tc.producesFileArtifact.want)
			}
			if tc.promotesNativeInput.check && meta.Presentation.PromotesNativeInput != tc.promotesNativeInput.want {
				t.Fatalf("%s promotes_native_input=%v, want %v", tc.name, meta.Presentation.PromotesNativeInput, tc.promotesNativeInput.want)
			}
		})
	}
}

func TestFrontendPermissionInventoryMatchesToolMetadata(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	re := regexp.MustCompile(`\b(?:read|write|execute)\('([^']+)'`)
	matches := re.FindAllStringSubmatch(string(raw), -1)
	if len(matches) == 0 {
		t.Fatal("frontend tool inventory contains no tool entries")
	}
	frontend := map[string]struct{}{}
	frontendCategory := map[string]ToolPermissionCategory{}
	for _, match := range matches {
		category := ToolPermissionCategory(match[0][:strings.IndexByte(match[0], '(')])
		name := match[1]
		frontend[name] = struct{}{}
		frontendCategory[name] = category
	}

	manifest := toolMetadataManifest()
	missingFrontend := []string{}
	for name := range manifest {
		if _, ok := frontend[name]; !ok {
			missingFrontend = append(missingFrontend, name)
		}
	}
	if len(missingFrontend) > 0 {
		sort.Strings(missingFrontend)
		t.Fatalf("backend tool metadata missing from frontend inventory: %s", strings.Join(missingFrontend, ", "))
	}
	categoryMismatches := []string{}
	for name, meta := range manifest {
		if got, ok := frontendCategory[name]; ok && got != meta.Category {
			categoryMismatches = append(categoryMismatches, name+" frontend="+string(got)+" backend="+string(meta.Category))
		}
	}
	if len(categoryMismatches) > 0 {
		sort.Strings(categoryMismatches)
		t.Fatalf("frontend inventory category mismatches: %s", strings.Join(categoryMismatches, ", "))
	}

	staleFrontend := []string{}
	for name := range frontend {
		if _, ok := manifest[name]; !ok {
			staleFrontend = append(staleFrontend, name)
		}
	}
	if len(staleFrontend) > 0 {
		sort.Strings(staleFrontend)
		t.Fatalf("frontend inventory contains tools missing backend metadata: %s", strings.Join(staleFrontend, ", "))
	}
}

func TestFrontendStaticToolTestRequirementsMatchBackend(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	frontendArgs := parseFrontendStringArrayMap(t, string(raw), "STATIC_REQUIRED_TEST_ARGUMENTS")
	frontendArgGroups := parseFrontendStringMatrixMap(t, string(raw), "STATIC_REQUIRED_TEST_ARGUMENT_GROUPS")
	manifest := toolMetadataManifest()
	problems := []string{}
	for name := range manifest {
		want := requiredToolTestArguments(name)
		got, ok := frontendArgs[name]
		if len(want) == 0 {
			if ok && len(got) > 0 {
				problems = append(problems, fmt.Sprintf("%s frontend declares unexpected required args %v", name, got))
			}
			continue
		}
		if !ok {
			problems = append(problems, fmt.Sprintf("%s missing from frontend static required args; want %v", name, want))
			continue
		}
		if !sameStringsUnordered(got, want) {
			problems = append(problems, fmt.Sprintf("%s frontend required args=%v want %v", name, got, want))
		}
	}
	for name := range manifest {
		want := requiredToolTestArgumentGroups(name)
		got, ok := frontendArgGroups[name]
		if len(want) == 0 {
			if ok && len(got) > 0 {
				problems = append(problems, fmt.Sprintf("%s frontend declares unexpected required arg groups %v", name, got))
			}
			continue
		}
		if !ok {
			problems = append(problems, fmt.Sprintf("%s missing from frontend static required arg groups; want %v", name, want))
			continue
		}
		if !sameStringMatrix(got, want) {
			problems = append(problems, fmt.Sprintf("%s frontend required arg groups=%v want %v", name, got, want))
		}
	}
	for name, got := range frontendArgs {
		if _, ok := manifest[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s frontend static required args are stale: %v", name, got))
		}
	}
	for name, got := range frontendArgGroups {
		if _, ok := manifest[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s frontend static required arg groups are stale: %v", name, got))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("frontend static tool-test requirement drift:\n%s", strings.Join(problems, "\n"))
	}
}

func TestFrontendStaticUploadToolTestGroupOrderMatchesBackend(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	frontendArgGroups := parseFrontendStringMatrixMap(t, string(raw), "STATIC_REQUIRED_TEST_ARGUMENT_GROUPS")
	for _, name := range []string{SlackToolUploadFile, "jira_upload_attachment"} {
		want := requiredToolTestArgumentGroups(name)
		got := frontendArgGroups[name]
		if len(got) != len(want) {
			t.Fatalf("%s frontend groups=%v, want %v", name, got, want)
		}
		for idx := range want {
			if !sameStringsUnordered(got[idx], want[idx]) {
				t.Fatalf("%s frontend group %d=%v, want %v", name, idx, got[idx], want[idx])
			}
		}
	}
}

func TestGitHubToolTestContextOnlyRequiresProjectForProjectBoundTools(t *testing.T) {
	for _, name := range GitHubToolNames() {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		got := containsString(meta.Test.RequiredContext, "project")
		want := githubToolTestRequiresProject(name)
		if got != want {
			t.Fatalf("%s required_context=%v, project context=%v want %v", name, meta.Test.RequiredContext, got, want)
		}
	}
}

func TestFrontendStaticGitHubProjectContextMatchesBackend(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	frontendProjectContext := parseFrontendStringSet(t, string(raw), "STATIC_GITHUB_PROJECT_CONTEXT_TOOLS")
	problems := []string{}
	for _, name := range GitHubToolNames() {
		_, got := frontendProjectContext[name]
		want := githubToolTestRequiresProject(name)
		if got != want {
			problems = append(problems, fmt.Sprintf("%s frontend project context=%v want %v", name, got, want))
		}
	}
	for name := range frontendProjectContext {
		if !IsGitHubToolName(name) {
			problems = append(problems, fmt.Sprintf("%s frontend GitHub project-context entry is stale", name))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("frontend static GitHub project context drift:\n%s", strings.Join(problems, "\n"))
	}
}

func TestFrontendStaticUnsupportedToolTestsMatchBackend(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	frontendUnsupported := parseFrontendStringSet(t, string(raw), "STATIC_UNSUPPORTED_TEST_TOOLS")
	problems := []string{}
	manifest := toolMetadataManifest()
	for name, meta := range manifest {
		_, gotUnsupported := frontendUnsupported[name]
		wantUnsupported := !meta.Test.Supported || meta.Test.Mode == ToolTestModeNone
		if gotUnsupported != wantUnsupported {
			problems = append(problems, fmt.Sprintf("%s frontend unsupported=%v want %v", name, gotUnsupported, wantUnsupported))
		}
	}
	for name := range frontendUnsupported {
		if _, ok := manifest[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s frontend unsupported test entry is stale", name))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("frontend static unsupported test drift:\n%s", strings.Join(problems, "\n"))
	}
}

func TestFrontendStaticToolMetadataMatchesBackend(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	inventoryRaw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	catalogRaw, err := os.ReadFile(filepath.Join(root, "lib", "connectionsCatalog.ts"))
	if err != nil {
		t.Fatalf("read frontend connection catalog: %v", err)
	}
	frontend := parseFrontendStaticToolMetadata(t, string(inventoryRaw), string(catalogRaw))
	manifest := toolMetadataManifest()
	problems := []string{}
	for name, want := range manifest {
		got, ok := frontend[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s missing from frontend static metadata", name))
			continue
		}
		if got.Category != want.Category {
			problems = append(problems, fmt.Sprintf("%s category=%q want %q", name, got.Category, want.Category))
		}
		if got.Origin != want.Origin {
			problems = append(problems, fmt.Sprintf("%s origin=%q want %q", name, got.Origin, want.Origin))
		}
		if got.ToolSetID != want.ToolSetID {
			problems = append(problems, fmt.Sprintf("%s tool_set_id=%q want %q", name, got.ToolSetID, want.ToolSetID))
		}
		if got.ToolSetName != want.ToolSetName {
			problems = append(problems, fmt.Sprintf("%s tool_set_name=%q want %q", name, got.ToolSetName, want.ToolSetName))
		}
		if got.ConnectorID != want.ConnectorID {
			problems = append(problems, fmt.Sprintf("%s connector_id=%q want %q", name, got.ConnectorID, want.ConnectorID))
		}
		if got.Presentation.Family != want.Presentation.Family {
			problems = append(problems, fmt.Sprintf("%s presentation.family=%q want %q", name, got.Presentation.Family, want.Presentation.Family))
		}
		if got.Presentation.ResultKind != want.Presentation.ResultKind {
			problems = append(problems, fmt.Sprintf("%s presentation.result_kind=%q want %q", name, got.Presentation.ResultKind, want.Presentation.ResultKind))
		}
		if got.Presentation.RendererStrategy != want.Presentation.RendererStrategy {
			problems = append(problems, fmt.Sprintf("%s presentation.renderer_strategy=%q want %q", name, got.Presentation.RendererStrategy, want.Presentation.RendererStrategy))
		}
		if got.Presentation.RendererKey != want.Presentation.RendererKey {
			problems = append(problems, fmt.Sprintf("%s presentation.renderer_key=%q want %q", name, got.Presentation.RendererKey, want.Presentation.RendererKey))
		}
		if got.Presentation.NativeViewer != want.Presentation.NativeViewer {
			problems = append(problems, fmt.Sprintf("%s presentation.native_viewer=%q want %q", name, got.Presentation.NativeViewer, want.Presentation.NativeViewer))
		}
		if !sameStringsUnordered(got.Presentation.ArtifactTypes, want.Presentation.ArtifactTypes) {
			problems = append(problems, fmt.Sprintf("%s presentation.artifact_types=%v want %v", name, got.Presentation.ArtifactTypes, want.Presentation.ArtifactTypes))
		}
		if got.Presentation.FileCapability != want.Presentation.FileCapability {
			problems = append(problems, fmt.Sprintf("%s presentation.file_capability=%q want %q", name, got.Presentation.FileCapability, want.Presentation.FileCapability))
		}
		if !sameStringsUnordered(got.Presentation.FileKinds, want.Presentation.FileKinds) {
			problems = append(problems, fmt.Sprintf("%s presentation.file_kinds=%v want %v", name, got.Presentation.FileKinds, want.Presentation.FileKinds))
		}
		if !sameStringsUnordered(got.Presentation.PreviewModes, want.Presentation.PreviewModes) {
			problems = append(problems, fmt.Sprintf("%s presentation.preview_modes=%v want %v", name, got.Presentation.PreviewModes, want.Presentation.PreviewModes))
		}
		if !sameStringsUnordered(got.Presentation.SourceRefs, want.Presentation.SourceRefs) {
			problems = append(problems, fmt.Sprintf("%s presentation.source_refs=%v want %v", name, got.Presentation.SourceRefs, want.Presentation.SourceRefs))
		}
		if got.Presentation.ProducesFileArtifact != want.Presentation.ProducesFileArtifact {
			problems = append(problems, fmt.Sprintf("%s presentation.produces_file_artifact=%v want %v", name, got.Presentation.ProducesFileArtifact, want.Presentation.ProducesFileArtifact))
		}
		if got.Presentation.PromotesNativeInput != want.Presentation.PromotesNativeInput {
			problems = append(problems, fmt.Sprintf("%s presentation.promotes_native_input=%v want %v", name, got.Presentation.PromotesNativeInput, want.Presentation.PromotesNativeInput))
		}
		if !sameStringsUnordered(got.Presentation.NativeLinks, want.Presentation.NativeLinks) {
			problems = append(problems, fmt.Sprintf("%s presentation.native_links=%v want %v", name, got.Presentation.NativeLinks, want.Presentation.NativeLinks))
		}
		if got.Test.Supported != want.Test.Supported {
			problems = append(problems, fmt.Sprintf("%s test.supported=%v want %v", name, got.Test.Supported, want.Test.Supported))
		}
		if got.Test.Mode != want.Test.Mode {
			problems = append(problems, fmt.Sprintf("%s test.mode=%q want %q", name, got.Test.Mode, want.Test.Mode))
		}
		if got.Test.RequiresApproval != want.Test.RequiresApproval {
			problems = append(problems, fmt.Sprintf("%s test.requires_approval=%v want %v", name, got.Test.RequiresApproval, want.Test.RequiresApproval))
		}
		if !sameStringsUnordered(got.Test.RequiredArguments, want.Test.RequiredArguments) {
			problems = append(problems, fmt.Sprintf("%s test.required_arguments=%v want %v", name, got.Test.RequiredArguments, want.Test.RequiredArguments))
		}
		if !sameStringMatrix(got.Test.RequiredArgumentGroups, want.Test.RequiredArgumentGroups) {
			problems = append(problems, fmt.Sprintf("%s test.required_argument_groups=%v want %v", name, got.Test.RequiredArgumentGroups, want.Test.RequiredArgumentGroups))
		}
		if !sameStringsUnordered(got.Test.RequiredContext, want.Test.RequiredContext) {
			problems = append(problems, fmt.Sprintf("%s test.required_context=%v want %v", name, got.Test.RequiredContext, want.Test.RequiredContext))
		}
	}
	for name := range frontend {
		if _, ok := manifest[name]; !ok {
			problems = append(problems, fmt.Sprintf("%s frontend static metadata is stale", name))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("frontend static metadata drift:\n%s", strings.Join(problems, "\n"))
	}
	for _, needle := range []string{
		"presentation?: ToolPresentationSpec",
		"presentation: ToolPresentationSpec",
		"normalizePresentationSpec",
		"presentationForTool",
		"result_kind",
		"renderer_strategy",
		"renderer_key",
		"native_viewer",
		"artifact_types",
		"file_capability",
		"file_kinds",
		"preview_modes",
		"source_refs",
		"produces_file_artifact",
		"promotes_native_input",
		"native_links",
	} {
		if !strings.Contains(string(inventoryRaw), needle) {
			t.Fatalf("frontend inventory does not preserve presentation metadata %q", needle)
		}
	}
}

func TestGeneratedFrontendToolMetadataMatchesBackend(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	generatedRaw, err := os.ReadFile(filepath.Join(root, "lib", "generatedToolMetadata.ts"))
	if err != nil {
		t.Fatalf("read generated frontend tool metadata: %v", err)
	}
	inventoryRaw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	src := string(generatedRaw)
	for _, needle := range []string{
		"Code generated by server/cmd/toolinventorygen; DO NOT EDIT.",
		"Regenerate with: (cd server && go generate ./internal/chat/tools)",
		"export const GENERATED_STATIC_TOOL_METADATA",
		"export const GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("generated frontend tool metadata missing %q", needle)
		}
	}
	for _, needle := range []string{
		"GENERATED_STATIC_TOOL_METADATA",
		"GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES",
		"buildGeneratedStaticToolInventory",
		"buildInventoryFromRuntimeTools(",
	} {
		if !strings.Contains(string(inventoryRaw), needle) {
			t.Fatalf("frontend static inventory does not consume generated metadata %q", needle)
		}
	}

	got := parseGeneratedFrontendToolMetadata(t, src)
	want := AllToolMetadata()
	gotContract, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal generated frontend tool metadata: %v", err)
	}
	wantContract, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal backend tool metadata: %v", err)
	}
	if !bytes.Equal(gotContract, wantContract) {
		gotJSON, _ := json.MarshalIndent(got, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("generated frontend tool metadata drifted from backend manifest.\nGot:\n%s\nWant:\n%s", gotJSON, wantJSON)
	}
}

func TestFrontendPresentationClassifierCoversNativeViewerFamilies(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	src := string(raw)
	for _, needle := range []string{
		"function presentationForTool",
		"function presentationRenderer",
		"function presentationArtifactTypes",
		"function presentationFileKinds",
		"function presentationPreviewModes",
		"function presentationSourceRefs",
		"function presentationProducesFileArtifact",
		"function presentationPromotesNativeInput",
		"function presentationNativeLinks",
		"read_file",
		"git_show_file_at_ref",
		"search_files",
		"read_attachment",
		"build_chart_artifact",
		"display_table",
		"create_interactive_artifact",
		"export_thread",
		"run_bigquery_query",
		"get_query_results",
		"github_clone_repository",
		"slack_download_file",
		"slack_upload_file",
		"jira_read_attachment",
		"jira_upload_attachment",
		"sharepoint_read",
		"cua_browser_click",
		"arxiv_download_paper",
		"arxiv_read_paper",
		"remote_file_download",
		"remote_file_upload",
		"repository_clone",
		"local_folder_list",
		"local_file_search",
		"warehouse_job",
		"source_url",
		"browser_url",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("frontend presentation classifier does not cover %q", needle)
		}
	}
	hasFileSourceBody := frontendFunctionBodyForTest(t, src, "presentationHasFileSource")
	for _, forbidden := range []string{"attachment_inspect", "attachment_search"} {
		if strings.Contains(hasFileSourceBody, forbidden) {
			t.Fatalf("frontend presentationHasFileSource should match backend and not treat %s as a file source", forbidden)
		}
	}
}

func frontendFunctionBodyForTest(t *testing.T, src string, name string) string {
	t.Helper()
	start := strings.Index(src, "function "+name)
	if start < 0 {
		t.Fatalf("frontend function %s missing", name)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("frontend function %s missing body", name)
	}
	open += start
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : i]
			}
		}
	}
	t.Fatalf("frontend function %s body did not close", name)
	return ""
}

func TestToolTestMetadataDocumentsSpecialRuntimeContext(t *testing.T) {
	exportThread, ok := ToolMetadataByName("export_thread")
	if !ok {
		t.Fatal("export_thread metadata missing")
	}
	if !containsString(exportThread.Test.RequiredContext, "thread") {
		t.Fatalf("export_thread required_context=%v, want thread", exportThread.Test.RequiredContext)
	}
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	if !strings.Contains(string(raw), "name === 'export_thread'") {
		t.Fatal("frontend static tool-test context must preserve export_thread thread requirement")
	}
}

func TestArxivAuditToolTestsUseSyntheticFixtures(t *testing.T) {
	for _, name := range []string{ArxivToolScreeningReport, ArxivToolRecommendations} {
		meta, ok := ToolMetadataByName(name)
		if !ok {
			t.Fatalf("%s metadata missing", name)
		}
		if !meta.Test.Supported || meta.Test.Mode != ToolTestModeSampleCall {
			t.Fatalf("%s test metadata=%#v, want supported sample call", name, meta.Test)
		}
		if meta.Test.InputRequired {
			t.Fatalf("%s input_required=true, want synthetic test with no user input", name)
		}
		if len(meta.Test.RequiredArguments) > 0 || len(meta.Test.RequiredArgumentGroups) > 0 {
			t.Fatalf("%s required test args=%v groups=%v, want synthetic fixture", name, meta.Test.RequiredArguments, meta.Test.RequiredArgumentGroups)
		}
		if !containsString(meta.Test.RequiredContext, "connection:arxiv") {
			t.Fatalf("%s required_context=%v, want connection:arxiv", name, meta.Test.RequiredContext)
		}
	}
}

func TestPermissionsTabSurfacesToolTestMetadata(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "components", "settings", "PermissionsTab.tsx"))
	if err != nil {
		t.Fatalf("read permissions tab: %v", err)
	}
	src := string(raw)
	for _, needle := range []string{
		"Tool test",
		"toolTestReadiness",
		"runToolTest",
		"buildToolTestArgs",
		"toolTestContextInputs",
		"parseToolTestArgumentValue",
		"required_arguments",
		"required_argument_groups",
		"required_context",
		"sample_arguments",
		"input_hint",
		"unstructuredInputRequired",
		"test.input_required === true",
		"test input",
		"toolTestGroupRequirementLabel",
		"lockedNote",
		"inventoryLoad",
		"Runtime tool inventory could not be loaded",
		"ToolTestDetails",
		"Normalized args",
		"Progress events",
		"Failure details",
		"ToolTestRenderedOutput",
		"buildToolTestArtifact",
		"resolveRenderer",
		"Raw output",
		"Output JSON",
		"Input mode",
		"toolTestGroupOptionLabel",
		"mode:{name}",
		"toolTestContext",
		"toolTestCurrentContextDescription",
		"Current project ID",
		"Current thread ID",
		"current project",
		"current thread",
		"toolTestDefaultArgumentValues",
		"toolTestSampleArgumentPlaceholder",
		"thread_id",
		"runtimeConnectedToolGroups",
		"Connected tool sets",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("PermissionsTab.tsx does not surface tool test metadata %q", needle)
		}
	}
	if strings.Contains(src, "one argument group") {
		t.Fatal("PermissionsTab.tsx should name grouped tool-test alternatives instead of showing the vague 'one argument group' copy")
	}
	if strings.Contains(src, "LockedToolRow") {
		t.Fatal("PermissionsTab.tsx should reuse ToolRow for locked tools so details and tool-test metadata remain visible")
	}
	if strings.Contains(src, "uniqueToolTestGroupArguments") {
		t.Fatal("PermissionsTab.tsx should render one selected tool-test input mode instead of flattening every argument group")
	}
}

func TestToolDevelopmentDocDocumentsSampleArgumentContract(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "docs", "tool-development-and-registration.md"))
	if err != nil {
		t.Fatalf("read tool development guide: %v", err)
	}
	src := string(raw)
	for _, needle := range []string{
		"Sample arguments:",
		"`SampleArguments`",
		"`sample_arguments`",
		"placeholders",
		"JSON-native values",
		"schema-valid",
		"`test-*`",
		"TestToolTestMetadataProvidesSampleArgumentsForAllInputs",
		"Required test arguments have valid `sample_arguments` placeholders.",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("tool development guide does not document sample argument contract %q", needle)
		}
	}
}

func TestToolDevelopmentDocForbidsTopLevelOpenSchemaAllowlist(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	docRaw, err := os.ReadFile(filepath.Join(root, "docs", "tool-development-and-registration.md"))
	if err != nil {
		t.Fatalf("read tool development guide: %v", err)
	}
	if !strings.Contains(string(docRaw), "There is no top-level open-schema allowlist.") {
		t.Fatal("tool development guide must make root schema closure non-negotiable")
	}

	testRaw, err := os.ReadFile(filepath.Join(root, "server", "internal", "chat", "tools", "tool_best_shape_test.go"))
	if err != nil {
		t.Fatalf("read best-shape tests: %v", err)
	}
	banned := "legacy" + "OpenProviderSchemaTools"
	if strings.Contains(string(testRaw), banned) {
		t.Fatalf("best-shape tests must not reintroduce %s", banned)
	}
}

func TestFrontendToolTestClientUsesBackendContract(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, "services", "toolTestApi.ts"))
	if err != nil {
		t.Fatalf("read tool test API client: %v", err)
	}
	artifactRaw, err := os.ReadFile(filepath.Join(root, "lib", "toolResultArtifact.ts"))
	if err != nil {
		t.Fatalf("read tool result artifact helper: %v", err)
	}
	typesRaw, err := os.ReadFile(filepath.Join(root, "types.ts"))
	if err != nil {
		t.Fatalf("read frontend types: %v", err)
	}
	src := string(raw)
	for _, needle := range []string{
		"tool_name: toolName",
		"fetch('/api/tools/test'",
		"ToolTestMissingInputError",
		"ToolTestPermissionError",
		"ToolPresentationSpec",
		"presentation: ToolPresentationSpec",
		"missing_arguments",
		"missing_context",
		"decision",
		"subject",
		"query_metadata",
		"result_metadata",
		"required_arguments",
		"required_argument_groups",
		"required_context",
		"input_hint",
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("toolTestApi.ts does not preserve backend tool-test contract %q", needle)
		}
	}
	for _, needle := range []string{
		"presentation?: ToolPresentationSpec",
		"tool_presentation",
	} {
		if !strings.Contains(string(artifactRaw), needle) {
			t.Fatalf("toolResultArtifact.ts does not preserve tool-test presentation metadata %q", needle)
		}
	}
	if !strings.Contains(string(typesRaw), "tool_presentation?: Record<string, any>") {
		t.Fatal("ToolResultEnvelope does not preserve tool_presentation metadata")
	}
}

func TestFrontendPermissionsSupportsToolSetRules(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	inventoryRaw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	permissionsRaw, err := os.ReadFile(filepath.Join(root, "components", "settings", "PermissionsTab.tsx"))
	if err != nil {
		t.Fatalf("read permissions tab: %v", err)
	}
	typesRaw, err := os.ReadFile(filepath.Join(root, "types", "permissions.ts"))
	if err != nil {
		t.Fatalf("read permission types: %v", err)
	}
	presentationRaw, err := os.ReadFile(filepath.Join(root, "lib", "approvalPresentation.ts"))
	if err != nil {
		t.Fatalf("read approval presentation: %v", err)
	}
	for fileName, src := range map[string]string{
		"lib/toolInventory.ts":                   string(inventoryRaw),
		"components/settings/PermissionsTab.tsx": string(permissionsRaw),
		"types/permissions.ts":                   string(typesRaw),
		"lib/approvalPresentation.ts":            string(presentationRaw),
	} {
		for _, needle := range []string{"tool_set", "tool_set_id"} {
			if !strings.Contains(src, needle) {
				t.Fatalf("%s does not preserve tool-set permission support %q", fileName, needle)
			}
		}
	}
	for _, needle := range []string{
		"tool-set-rule",
		"toolSetRuleMatchesTool",
		"connectionIdForRule",
	} {
		if !strings.Contains(string(inventoryRaw), needle) {
			t.Fatalf("lib/toolInventory.ts does not preserve tool-set resolution %q", needle)
		}
	}
	for _, needle := range []string{
		"durableToolSet",
		"ToolSetRuleRow",
		"rulesByToolSet",
		"Tool-set rule",
	} {
		if !strings.Contains(string(permissionsRaw), needle) {
			t.Fatalf("PermissionsTab.tsx does not preserve tool-set UI support %q", needle)
		}
	}
}

func TestFrontendLocalPermissionSubjectsUseExcludesContract(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	typesRaw, err := os.ReadFile(filepath.Join(root, "types", "permissions.ts"))
	if err != nil {
		t.Fatalf("read permission types: %v", err)
	}
	presentationRaw, err := os.ReadFile(filepath.Join(root, "lib", "approvalPresentation.ts"))
	if err != nil {
		t.Fatalf("read approval presentation: %v", err)
	}
	cardRaw, err := os.ReadFile(filepath.Join(root, "components", "approvals", "ApprovalCard.tsx"))
	if err != nil {
		t.Fatalf("read approval card: %v", err)
	}
	typesSrc := string(typesRaw)
	presentationSrc := string(presentationRaw)
	cardSrc := string(cardRaw)
	for _, needle := range []string{
		"excludes?: string[]",
		"subject.excludes = excludes.map",
	} {
		if !strings.Contains(typesSrc, needle) {
			t.Fatalf("types/permissions.ts does not preserve local exclude subject field %q", needle)
		}
	}
	for fileName, src := range map[string]string{
		"types/permissions.ts":                  typesSrc,
		"lib/approvalPresentation.ts":           presentationSrc,
		"components/approvals/ApprovalCard.tsx": cardSrc,
	} {
		if strings.Contains(src, "recursive") {
			t.Fatalf("%s should not expose frontend-only recursive local permission semantics", fileName)
		}
	}
	for _, needle := range []string{
		"localSubjectDetail",
		"excludeCount",
		"protected",
		"patterns",
	} {
		if !strings.Contains(presentationSrc, needle) {
			t.Fatalf("approvalPresentation.ts does not summarize local excludes for settings rows: missing %q", needle)
		}
	}
	for _, needle := range []string{
		"approval.subject.excludes",
		"Includes subfolders, except protected paths listed below.",
		"What’s never read",
		"DEFAULT_EXCLUDES_COPY",
	} {
		if !strings.Contains(cardSrc, needle) {
			t.Fatalf("ApprovalCard.tsx does not make local excludes visible in approval flow: missing %q", needle)
		}
	}
}

func TestPermissionsTabUsesInventoryServiceAsFallbackBoundary(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	tabRaw, err := os.ReadFile(filepath.Join(root, "components", "settings", "PermissionsTab.tsx"))
	if err != nil {
		t.Fatalf("read permissions tab: %v", err)
	}
	serviceRaw, err := os.ReadFile(filepath.Join(root, "services", "toolInventoryApi.ts"))
	if err != nil {
		t.Fatalf("read tool inventory API client: %v", err)
	}
	inventoryRaw, err := os.ReadFile(filepath.Join(root, "lib", "toolInventory.ts"))
	if err != nil {
		t.Fatalf("read frontend tool inventory: %v", err)
	}
	tabSrc := string(tabRaw)
	serviceSrc := string(serviceRaw)
	inventorySrc := string(inventoryRaw)
	for _, needle := range []string{
		"fetchToolInventory",
		"useState<PermissionToolInventory | null>(null)",
		"inventoryLoad === 'loading'",
		"inventoryLoad === 'error'",
	} {
		if !strings.Contains(tabSrc, needle) {
			t.Fatalf("PermissionsTab.tsx no longer waits on inventory service boundary %q", needle)
		}
	}
	if strings.Contains(tabSrc, "STATIC_TOOL_INVENTORY") {
		t.Fatal("PermissionsTab.tsx should not import or render from STATIC_TOOL_INVENTORY directly")
	}
	if !strings.Contains(serviceSrc, "STATIC_TOOL_INVENTORY") {
		t.Fatal("toolInventoryApi.ts should remain the single frontend static-fallback boundary")
	}
	for _, needle := range []string{
		"tryNormalizeRuntimeToolInventory",
		"if (!res.ok)",
		"throw new Error('Tool inventory response was malformed.')",
		"throw new Error('Tool inventory response did not contain any valid tools.')",
	} {
		if !strings.Contains(serviceSrc, needle) {
			t.Fatalf("toolInventoryApi.ts should fall back only when runtime inventory is unavailable and fail visibly when malformed: missing %q", needle)
		}
	}
	if !strings.Contains(inventorySrc, "export function tryNormalizeRuntimeToolInventory") {
		t.Fatal("lib/toolInventory.ts should expose a no-fallback runtime normalizer for API contract checks")
	}
}

func requireToolCategory(t *testing.T, name string, want ToolPermissionCategory) {
	t.Helper()
	meta, ok := ToolMetadataByName(name)
	if !ok {
		t.Fatalf("%s metadata missing", name)
	}
	if meta.Category != want {
		t.Fatalf("%s category = %q, want %q", name, meta.Category, want)
	}
}

func requireKnownPresentationValue(t *testing.T, toolName string, field string, value string, known map[string]struct{}) {
	t.Helper()
	if _, ok := known[strings.TrimSpace(value)]; !ok {
		t.Fatalf("%s presentation %s=%q is not in known values", toolName, field, value)
	}
}

func knownResultKinds() map[string]struct{} {
	return stringSet(
		"artifact",
		"browser",
		"catalog",
		"chart",
		"code",
		"command",
		"connector",
		"data",
		"file",
		"folder",
		"generic",
		"interactive",
		"image",
		"metadata",
		"paper",
		"query",
		"repository",
		"search",
		"status",
		"table_schema",
	)
}

func knownRendererStrategies() map[string]struct{} {
	return stringSet(
		"artifact_type",
		"family",
		"generic",
		"tool",
	)
}

func knownNativeViewers() map[string]struct{} {
	return stringSet(
		"chart",
		"code",
		"connector_inspector",
		"data",
		"file",
		"interactive_html",
		"image",
		"query",
	)
}

func knownArtifactTypes() map[string]struct{} {
	return stringSet(
		"code",
		"data",
		"file",
		"image",
	)
}

func knownFileCapabilities() map[string]struct{} {
	return stringSet(
		"artifact_create",
		"artifact_read",
		"attachment_inspect",
		"attachment_read",
		"attachment_search",
		"browser_control",
		"dataset_artifact_read",
		"local_folder_list",
		"local_file_read",
		"local_file_search",
		"local_repository",
		"remote_file_download",
		"remote_file_read",
		"remote_file_upload",
		"repository_clone",
	)
}

func knownFileKinds() map[string]struct{} {
	return stringSet(
		"archive",
		"document",
		"html",
		"image",
		"pdf",
		"presentation",
		"spreadsheet",
		"text",
	)
}

func knownPreviewModes() map[string]struct{} {
	return stringSet(
		"download_only",
		"html_sandbox",
		"image_raw",
		"pdf_canvas",
		"presentation_slides",
		"raw_download",
		"spreadsheet_workbook",
		"text_preview",
	)
}

func knownSourceRefs() map[string]struct{} {
	return stringSet(
		"artifact_id",
		"arxiv_id",
		"attachmentId",
		"catalog_name",
		"channel_id",
		"connection",
		"contentBase64",
		"contentText",
		"database_name",
		"dataset_id",
		"file_id",
		"fileName",
		"full_name",
		"issueKey",
		"item_url",
		"local_path",
		"owner_token",
		"paper",
		"path",
		"project_id",
		"remote_id",
		"schema_name",
		"server_relative_url",
		"site_url",
		"source.artifact_id",
		"source.job_id",
		"source.path",
		"table_id",
	)
}

func knownNativeLinks() map[string]struct{} {
	return stringSet(
		"browser_url",
		"source_url",
		"warehouse_job",
	)
}

func stringSet(values ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func readToolPackageTestSource(t *testing.T) string {
	t.Helper()
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob tool package tests: %v", err)
	}
	var b strings.Builder
	for _, path := range paths {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		b.Write(src)
		b.WriteByte('\n')
	}
	return b.String()
}

func uniqueSortedStrings(values ...string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func canonicalDefaultDefinitionToolNames() []string {
	names := []string{}
	names = append(names, analysisCoreToolNames...)
	names = append(names, csvToolNames...)
	for _, datasource := range DatasourceNames() {
		names = append(names, DatasourceToolNames(datasource)...)
	}
	names = append(names, queryLifecycleToolNames...)
	return uniqueSortedStrings(names...)
}

func searchDefinitionAliases() map[string]struct{} {
	return stringSet("search_docs")
}

func toolMetadataRequiredFields(schema map[string]any) []string {
	raw, ok := schema["required"]
	if !ok {
		return nil
	}
	out := []string{}
	switch typed := raw.(type) {
	case []string:
		out = append(out, typed...)
	case []any:
		for _, value := range typed {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func toolTestUsesSyntheticArguments(name string) bool {
	switch strings.TrimSpace(name) {
	case "check_query_status", "get_query_results", "validate_query_result", "cancel_query", ArxivToolScreeningReport, ArxivToolRecommendations:
		return true
	default:
		return false
	}
}

func parseFrontendStringArrayMap(t *testing.T, src string, constName string) map[string][]string {
	t.Helper()
	start := strings.Index(src, "const "+constName)
	if start < 0 {
		t.Fatalf("frontend source missing %s", constName)
	}
	open := strings.Index(src[start:], "{")
	if open < 0 {
		t.Fatalf("frontend source %s has no object literal", constName)
	}
	open += start
	close := strings.Index(src[open:], "};")
	if close < 0 {
		t.Fatalf("frontend source %s object literal is not closed", constName)
	}
	block := src[open : open+close]
	entryRe := regexp.MustCompile(`(?m)^\s*['"]([^'"]+)['"]:\s*\[([^\]]*)\]`)
	itemRe := regexp.MustCompile(`['"]([^'"]+)['"]`)
	out := map[string][]string{}
	for _, match := range entryRe.FindAllStringSubmatch(block, -1) {
		key := strings.TrimSpace(match[1])
		values := []string{}
		for _, item := range itemRe.FindAllStringSubmatch(match[2], -1) {
			value := strings.TrimSpace(item[1])
			if value != "" {
				values = append(values, value)
			}
		}
		out[key] = values
	}
	if len(out) == 0 {
		t.Fatalf("frontend source %s contained no string-array entries", constName)
	}
	return out
}

func parseFrontendStringMatrixMap(t *testing.T, src string, constName string) map[string][][]string {
	t.Helper()
	block := frontendConstBlock(t, src, constName, "{", "};")
	keyRe := regexp.MustCompile(`['"]([^'"]+)['"]\s*:\s*\[`)
	itemRe := regexp.MustCompile(`['"]([^'"]+)['"]`)
	groupRe := regexp.MustCompile(`(?s)\[([^\[\]]*)\]`)
	out := map[string][][]string{}
	offset := 0
	for {
		match := keyRe.FindStringSubmatchIndex(block[offset:])
		if match == nil {
			break
		}
		key := strings.TrimSpace(block[offset+match[2] : offset+match[3]])
		arrayStart := offset + match[1] - 1
		arrayEnd := frontendMatchingBracket(t, block, arrayStart)
		arrayContent := block[arrayStart+1 : arrayEnd]
		groups := [][]string{}
		for _, groupMatch := range groupRe.FindAllStringSubmatch(arrayContent, -1) {
			values := []string{}
			for _, item := range itemRe.FindAllStringSubmatch(groupMatch[1], -1) {
				value := strings.TrimSpace(item[1])
				if value != "" {
					values = append(values, value)
				}
			}
			if len(values) > 0 {
				groups = append(groups, values)
			}
		}
		if len(groups) > 0 {
			out[key] = groups
		}
		offset = arrayEnd + 1
	}
	if len(out) == 0 {
		t.Fatalf("frontend source %s contained no string-matrix entries", constName)
	}
	return out
}

func parseFrontendStringSet(t *testing.T, src string, constName string) map[string]struct{} {
	t.Helper()
	start := strings.Index(src, "const "+constName)
	if start < 0 {
		t.Fatalf("frontend source missing %s", constName)
	}
	open := strings.Index(src[start:], "[")
	if open < 0 {
		t.Fatalf("frontend source %s has no array literal", constName)
	}
	open += start
	close := strings.Index(src[open:], "]);")
	if close < 0 {
		t.Fatalf("frontend source %s array literal is not closed", constName)
	}
	block := src[open : open+close]
	itemRe := regexp.MustCompile(`['"]([^'"]+)['"]`)
	out := map[string]struct{}{}
	for _, match := range itemRe.FindAllStringSubmatch(block, -1) {
		value := strings.TrimSpace(match[1])
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

type frontendStaticToolMeta struct {
	Category     ToolPermissionCategory
	Origin       ToolOrigin
	ToolSetID    string
	ToolSetName  string
	ConnectorID  string
	Presentation ToolPresentationSpec
	Test         frontendStaticToolTest
}

type frontendStaticToolTest struct {
	Supported              bool
	Mode                   ToolTestMode
	RequiresApproval       bool
	RequiredArguments      []string
	RequiredArgumentGroups [][]string
	RequiredContext        []string
}

func parseFrontendStaticToolMetadata(t *testing.T, inventorySrc string, catalogSrc string) map[string]frontendStaticToolMeta {
	t.Helper()
	connectionLabels := parseFrontendConnectionLabels(t, catalogSrc)
	connectionSetNames := parseFrontendStringMap(t, inventorySrc, "STATIC_CONNECTION_TOOL_SET_NAMES")
	connectionCheckTools := parseFrontendStringSet(t, inventorySrc, "STATIC_CONNECTION_CHECK_TOOLS")
	unsupportedTools := parseFrontendStringSet(t, inventorySrc, "STATIC_UNSUPPORTED_TEST_TOOLS")
	githubProjectContextTools := parseFrontendStringSet(t, inventorySrc, "STATIC_GITHUB_PROJECT_CONTEXT_TOOLS")
	requiredArgs := parseFrontendStringArrayMap(t, inventorySrc, "STATIC_REQUIRED_TEST_ARGUMENTS")
	requiredArgGroups := parseFrontendStringMatrixMap(t, inventorySrc, "STATIC_REQUIRED_TEST_ARGUMENT_GROUPS")

	out := map[string]frontendStaticToolMeta{}
	for name, meta := range parseFrontendConnectionToolMetadata(t, inventorySrc, connectionLabels, connectionSetNames) {
		out[name] = meta
	}
	for name, meta := range parseFrontendAppToolMetadata(t, inventorySrc) {
		if _, exists := out[name]; exists {
			t.Fatalf("%s appears in both connection and app frontend static metadata", name)
		}
		out[name] = meta
	}
	for name, meta := range out {
		meta.Presentation = toolPresentationFor(name, meta.Category, meta.Origin, meta.ToolSetID, meta.ConnectorID)
		if _, unsupported := unsupportedTools[name]; unsupported {
			meta.Test = frontendStaticToolTest{
				Supported:        false,
				Mode:             ToolTestModeNone,
				RequiresApproval: false,
			}
		} else {
			mode := ToolTestModeSampleCall
			if _, ok := connectionCheckTools[name]; ok {
				mode = ToolTestModeConnectionCheck
			} else if meta.Category != ToolCategoryRead {
				mode = ToolTestModeDryRun
			}
			meta.Test = frontendStaticToolTest{
				Supported:         true,
				Mode:              mode,
				RequiresApproval:  meta.Category != ToolCategoryRead,
				RequiredArguments: append([]string(nil), requiredArgs[name]...),
				RequiredArgumentGroups: cloneStringMatrix(
					requiredArgGroups[name],
				),
				RequiredContext: frontendStaticRequiredContext(name, meta.Origin, meta.ToolSetID, meta.ConnectorID, mode, githubProjectContextTools),
			}
		}
		out[name] = meta
	}
	return out
}

func parseGeneratedFrontendToolMetadata(t *testing.T, src string) []ToolMetadata {
	t.Helper()
	start := strings.Index(src, "export const GENERATED_STATIC_TOOL_METADATA = ")
	if start < 0 {
		t.Fatal("generated frontend metadata missing export")
	}
	arrayOffset := strings.Index(src[start:], "[")
	if arrayOffset < 0 {
		t.Fatal("generated frontend metadata missing JSON array")
	}
	start += arrayOffset
	endMarker := "\n] as const;"
	end := strings.Index(src[start:], endMarker)
	if end < 0 {
		t.Fatal("generated frontend metadata missing array terminator")
	}
	raw := src[start : start+end+2]
	var out []ToolMetadata
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("parse generated frontend tool metadata: %v", err)
	}
	return out
}

func parseFrontendConnectionToolMetadata(t *testing.T, src string, labels map[string]string, setNames map[string]string) map[string]frontendStaticToolMeta {
	t.Helper()
	block := frontendConstBlock(t, src, "CONNECTION_TOOLS", "{", "};")
	headerRe := regexp.MustCompile(`^\s*'?([A-Za-z0-9_-]+)'?:\s*\[`)
	toolRe := regexp.MustCompile(`\b(read|write|execute)\('([^']+)'`)
	out := map[string]frontendStaticToolMeta{}
	currentID := ""
	for _, line := range strings.Split(block, "\n") {
		if currentID == "" {
			if match := headerRe.FindStringSubmatch(line); match != nil {
				currentID = strings.TrimSpace(match[1])
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "],") {
			currentID = ""
			continue
		}
		match := toolRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		setName := setNames[currentID]
		if setName == "" {
			setName = labels[currentID]
		}
		if setName == "" {
			t.Fatalf("frontend connection %s has no parsed label or static tool-set name", currentID)
		}
		name := strings.TrimSpace(match[2])
		if _, exists := out[name]; exists {
			t.Fatalf("duplicate frontend connection tool %s", name)
		}
		out[name] = frontendStaticToolMeta{
			Category:    frontendToolCategory(t, match[1]),
			Origin:      ToolOriginConnected,
			ToolSetID:   currentID,
			ToolSetName: setName,
			ConnectorID: currentID,
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no frontend connection tool metadata")
	}
	return out
}

func parseFrontendAppToolMetadata(t *testing.T, src string) map[string]frontendStaticToolMeta {
	t.Helper()
	block := frontendConstBlock(t, src, "APP_TOOL_GROUPS", "[", "];")
	idRe := regexp.MustCompile(`^\s*id:\s*'([^']+)'`)
	originRe := regexp.MustCompile(`^\s*origin:\s*'([^']+)'`)
	titleRe := regexp.MustCompile(`^\s*title:\s*'([^']+)'`)
	toolRe := regexp.MustCompile(`\b(read|write|execute)\('([^']+)'`)
	out := map[string]frontendStaticToolMeta{}
	groupID := ""
	groupOrigin := ""
	groupTitle := ""
	inTools := false
	for _, line := range strings.Split(block, "\n") {
		if match := idRe.FindStringSubmatch(line); match != nil {
			groupID = strings.TrimSpace(match[1])
			groupOrigin = ""
			groupTitle = ""
			inTools = false
			continue
		}
		if match := originRe.FindStringSubmatch(line); match != nil {
			groupOrigin = strings.TrimSpace(match[1])
			continue
		}
		if match := titleRe.FindStringSubmatch(line); match != nil {
			groupTitle = strings.TrimSpace(match[1])
			continue
		}
		if strings.Contains(line, "tools: [") {
			inTools = true
			continue
		}
		if inTools && strings.HasPrefix(strings.TrimSpace(line), "],") {
			inTools = false
			continue
		}
		if !inTools {
			continue
		}
		match := toolRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		if groupID == "" || groupOrigin == "" || groupTitle == "" {
			t.Fatalf("frontend app tool %s parsed before complete group metadata id=%q origin=%q title=%q", match[2], groupID, groupOrigin, groupTitle)
		}
		name := strings.TrimSpace(match[2])
		if _, exists := out[name]; exists {
			t.Fatalf("duplicate frontend app tool %s", name)
		}
		origin := frontendToolOrigin(t, groupOrigin)
		if groupID == "attachments" {
			origin = ToolOriginAttached
		}
		out[name] = frontendStaticToolMeta{
			Category:    frontendToolCategory(t, match[1]),
			Origin:      origin,
			ToolSetID:   groupID,
			ToolSetName: groupTitle,
		}
	}
	if len(out) == 0 {
		t.Fatal("parsed no frontend app tool metadata")
	}
	return out
}

func frontendStaticRequiredContext(name string, origin ToolOrigin, setID string, connectorID string, mode ToolTestMode, githubProjectContextTools map[string]struct{}) []string {
	context := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || containsString(context, value) {
			return
		}
		context = append(context, value)
	}
	if connectorID != "" {
		add("connection:" + connectorID)
	}
	if name == LocalSearchCommandToolName {
		add("connection:local-computer")
	}
	if origin == ToolOriginAttached {
		add("thread")
	}
	if origin == ToolOriginLocal && ((setID == "local-files" && localFilesToolTestRequiresProject(name)) || (setID == "data-analysis" && dataAnalysisToolTestRequiresProject(name))) {
		add("project")
	}
	switch setID {
	case "attachments":
		add("thread")
	case "artifacts":
		if name == "export_thread" {
			add("thread")
		}
		if artifactToolTestRequiresArtifactContext(name) {
			add("artifact")
		}
	case "thread-search":
		add("search_index")
	case "github":
		if _, ok := githubProjectContextTools[name]; ok {
			add("project")
		}
	case "browser":
		add("browser")
	}
	if mode == ToolTestModeConnectionCheck && len(context) == 0 && connectorID != "" {
		add("connection:" + connectorID)
	}
	return context
}

func parseFrontendConnectionLabels(t *testing.T, src string) map[string]string {
	t.Helper()
	block := frontendConstBlock(t, src, "CONNECTION_META", "{", "};")
	re := regexp.MustCompile(`(?m)^\s*'?([A-Za-z0-9_-]+)'?:\s*\{[^}\n]*label:\s*'([^']+)'`)
	out := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		out[strings.TrimSpace(match[1])] = strings.TrimSpace(match[2])
	}
	if len(out) == 0 {
		t.Fatal("parsed no frontend connection labels")
	}
	return out
}

func parseFrontendStringMap(t *testing.T, src string, constName string) map[string]string {
	t.Helper()
	block := frontendConstBlock(t, src, constName, "{", "};")
	re := regexp.MustCompile(`(?m)^\s*'?([A-Za-z0-9_-]+)'?:\s*'([^']+)'`)
	out := map[string]string{}
	for _, match := range re.FindAllStringSubmatch(block, -1) {
		out[strings.TrimSpace(match[1])] = strings.TrimSpace(match[2])
	}
	return out
}

func frontendConstBlock(t *testing.T, src string, constName string, openToken string, closeToken string) string {
	t.Helper()
	start := strings.Index(src, "const "+constName)
	if start < 0 {
		start = strings.Index(src, "export const "+constName)
	}
	if start < 0 {
		t.Fatalf("frontend source missing %s", constName)
	}
	open := strings.Index(src[start:], openToken)
	if open < 0 {
		t.Fatalf("frontend source %s missing %s", constName, openToken)
	}
	open += start
	close := strings.Index(src[open:], closeToken)
	if close < 0 {
		t.Fatalf("frontend source %s missing close token %s", constName, closeToken)
	}
	return src[open : open+close]
}

func frontendMatchingBracket(t *testing.T, src string, open int) int {
	t.Helper()
	if open < 0 || open >= len(src) || src[open] != '[' {
		t.Fatalf("frontend parser expected '[' at byte %d", open)
	}
	depth := 0
	var quote byte
	escaped := false
	for i := open; i < len(src); i++ {
		ch := src[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			continue
		}
		switch ch {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	t.Fatalf("frontend parser could not find matching ']' for byte %d", open)
	return -1
}

func frontendToolCategory(t *testing.T, raw string) ToolPermissionCategory {
	t.Helper()
	switch strings.TrimSpace(raw) {
	case "read":
		return ToolCategoryRead
	case "write":
		return ToolCategoryWrite
	case "execute":
		return ToolCategoryExecute
	default:
		t.Fatalf("unknown frontend tool category %q", raw)
		return ""
	}
}

func frontendToolOrigin(t *testing.T, raw string) ToolOrigin {
	t.Helper()
	switch strings.TrimSpace(raw) {
	case "builtin":
		return ToolOriginBuiltin
	case "connected":
		return ToolOriginConnected
	case "local":
		return ToolOriginLocal
	case "attached":
		return ToolOriginAttached
	default:
		t.Fatalf("unknown frontend tool origin %q", raw)
		return ""
	}
}

func sameStringsUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

func stringMatrixContainsGroup(matrix [][]string, want []string) bool {
	for _, group := range matrix {
		if sameStringsUnordered(cleanStringSlice(group), cleanStringSlice(want)) {
			return true
		}
	}
	return false
}

func sameStringMatrix(a, b [][]string) bool {
	return sameStringsUnordered(normalizedStringMatrixLabels(a), normalizedStringMatrixLabels(b))
}

func normalizedStringMatrixLabels(in [][]string) []string {
	out := []string{}
	for _, group := range in {
		clean := cleanStringSlice(group)
		if len(clean) == 0 {
			continue
		}
		sort.Strings(clean)
		out = append(out, strings.Join(clean, "\x00"))
	}
	sort.Strings(out)
	return out
}

func sortedMapKeys(values map[string]any) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if key != "" {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func requireNoEmptyOrDuplicateStrings(t *testing.T, toolName string, field string, values []string) {
	t.Helper()
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			t.Fatalf("%s test metadata %s contains an empty value", toolName, field)
		}
		if _, ok := seen[value]; ok {
			t.Fatalf("%s test metadata %s contains duplicate value %q", toolName, field, value)
		}
		seen[value] = struct{}{}
	}
}

func connectedMetadataNames(connector string, categories ...ToolPermissionCategory) []string {
	categorySet := map[ToolPermissionCategory]struct{}{}
	for _, category := range categories {
		categorySet[category] = struct{}{}
	}
	out := []string{}
	for _, meta := range AllToolMetadata() {
		if meta.Origin != ToolOriginConnected || meta.ConnectorID != connector {
			continue
		}
		if len(categorySet) > 0 {
			if _, ok := categorySet[meta.Category]; !ok {
				continue
			}
		}
		out = append(out, meta.Name)
	}
	sort.Strings(out)
	return out
}

func toolDefinitionNames(defs []types.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		name := strings.TrimSpace(def.Function.Name)
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func newAllEnabledRegistry(t *testing.T) *Registry {
	t.Helper()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	searchSvc, err := search.NewService(t.TempDir(), st)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	rlmRuntime := rlm.NewRuntime(rlm.Options{Search: searchSvc, Store: st, CfgDir: cfgDir})

	return NewRegistry(&RegistryOptions{
		Search:     searchSvc,
		Store:      st,
		CfgDir:     cfgDir,
		RLM:        rlmRuntime,
		Atlassian:  atlassian.NewManager(t.TempDir(), nil),
		Browser:    browsercontrol.NewManager(t.TempDir(), nil),
		SharePoint: sharepoint.NewManager(t.TempDir(), nil),
		GitHub:     &struct{}{},
		Slack:      slackapi.NewManager(nil, nil),
		Arxiv:      arxiv.NewManager(arxiv.Options{CfgDir: t.TempDir()}),
	})
}
