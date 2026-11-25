package tools

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestBestInClassToolMetadataUsesStableTaxonomy(t *testing.T) {
	manifest := toolMetadataManifest()
	problems := []string{}

	for key, meta := range manifest {
		if key != meta.Name {
			problems = append(problems, fmt.Sprintf("%s metadata name=%q, want map key", key, meta.Name))
		}
		if !isStableToolName(meta.Name) {
			problems = append(problems, fmt.Sprintf("%s tool name must be lowercase snake_case", meta.Name))
		}
		if !isStableKebabID(meta.ToolSetID) {
			problems = append(problems, fmt.Sprintf("%s tool_set_id=%q must be lowercase kebab-case", meta.Name, meta.ToolSetID))
		}
		if !isStableKebabID(meta.Presentation.Family) {
			problems = append(problems, fmt.Sprintf("%s presentation.family=%q must be lowercase kebab-case", meta.Name, meta.Presentation.Family))
		}
		if meta.Origin == ToolOriginConnected {
			if meta.ConnectorID != meta.ToolSetID {
				problems = append(problems, fmt.Sprintf("%s connected connector_id=%q, want tool_set_id %q", meta.Name, meta.ConnectorID, meta.ToolSetID))
			}
			if !containsString(meta.Test.RequiredContext, "connection:"+meta.ConnectorID) {
				problems = append(problems, fmt.Sprintf("%s connected tool test context=%v, want connection:%s", meta.Name, meta.Test.RequiredContext, meta.ConnectorID))
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("best-in-class metadata taxonomy problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestBestInClassToolDefinitionsAreUserFacing(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}

	for _, meta := range AllToolMetadata() {
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		def := tool.Definition().Function
		if strings.TrimSpace(def.Name) != meta.Name {
			problems = append(problems, fmt.Sprintf("%s provider definition name=%q, want metadata name", meta.Name, def.Name))
		}
		if !isUserFacingToolCopy(meta.Label, false) {
			problems = append(problems, fmt.Sprintf("%s label=%q must be user-facing copy", meta.Name, meta.Label))
		}
		if !isUserFacingToolCopy(meta.Description, true) {
			problems = append(problems, fmt.Sprintf("%s metadata description=%q must be user-facing copy", meta.Name, meta.Description))
		}
		if !isUserFacingToolCopy(def.Description, true) {
			problems = append(problems, fmt.Sprintf("%s provider description=%q must be user-facing copy", meta.Name, def.Description))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("best-in-class tool copy problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestBestInClassToolDefinitionsUseClosedSchemas(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}

	for _, meta := range AllToolMetadata() {
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		params := tool.Definition().Function.Parameters
		if got, ok := params["additionalProperties"].(bool); !ok || got {
			problems = append(problems, fmt.Sprintf("%s parameters.additionalProperties=%#v, want false", meta.Name, params["additionalProperties"]))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("best-in-class closed schema problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestBestInClassToolObjectSchemasAreClosedRecursively(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}
	intentionalOpenSchemaPaths := intentionalOpenProviderSchemaPaths()

	for _, meta := range AllToolMetadata() {
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		walkSchemaObjectClosures(meta.Name, "$", tool.Definition().Function.Parameters, intentionalOpenSchemaPaths, &problems)
		delete(intentionalOpenSchemaPaths, meta.Name)
	}
	for toolName, paths := range intentionalOpenSchemaPaths {
		for path := range paths {
			problems = append(problems, fmt.Sprintf("%s schema path %s is listed in intentionalOpenProviderSchemaPaths but is not a registered metadata schema path", toolName, path))
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("best-in-class recursive schema closure problems:\n%s", strings.Join(problems, "\n"))
	}
}

func TestBestInClassToolTestInputsMapToProviderSchema(t *testing.T) {
	registry := newAllEnabledRegistry(t)
	problems := []string{}

	for _, meta := range AllToolMetadata() {
		if !meta.Test.Supported {
			continue
		}
		tool, ok := registry.byName[meta.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s metadata exists but registered tool is missing", meta.Name))
			continue
		}
		properties, ok := tool.Definition().Function.Parameters["properties"].(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("%s schema properties are not an object", meta.Name))
			continue
		}

		for _, arg := range meta.Test.RequiredArguments {
			if _, ok := properties[arg]; !ok {
				problems = append(problems, fmt.Sprintf("%s test.required_arguments includes %q but schema has no such property", meta.Name, arg))
				continue
			}
			validateToolTestSampleArgument(meta, properties, arg, &problems)
		}
		for idx, group := range meta.Test.RequiredArgumentGroups {
			for _, arg := range group {
				if _, ok := properties[arg]; !ok {
					problems = append(problems, fmt.Sprintf("%s test.required_argument_groups[%d] includes %q but schema has no such property", meta.Name, idx, arg))
					continue
				}
				validateToolTestSampleArgument(meta, properties, arg, &problems)
			}
		}
		for _, ctx := range meta.Test.RequiredContext {
			if !isKnownToolTestContext(ctx) {
				problems = append(problems, fmt.Sprintf("%s test.required_context includes unknown context %q", meta.Name, ctx))
			}
		}
		if meta.Test.Mode == ToolTestModeDryRun {
			dryRunSchema, ok := properties["dry_run"]
			if !ok {
				problems = append(problems, fmt.Sprintf("%s dry_run test mode but schema has no dry_run property", meta.Name))
			} else if !schemaAllowsJSONType(dryRunSchema, "boolean") {
				problems = append(problems, fmt.Sprintf("%s dry_run schema does not advertise boolean type: %#v", meta.Name, dryRunSchema))
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("best-in-class tool-test schema problems:\n%s", strings.Join(problems, "\n"))
	}
}

func validateToolTestSampleArgument(meta ToolMetadata, properties map[string]any, arg string, problems *[]string) {
	sample, ok := meta.Test.SampleArguments[arg]
	if !ok {
		return
	}
	if value, ok := sample.(string); ok && strings.HasPrefix(value, "test-") {
		*problems = append(*problems, fmt.Sprintf("%s sample_arguments[%s]=%q is a generic fallback; use a semantic schema-valid sample", meta.Name, arg, value))
	}
	if problem := toolTestSampleSchemaProblem(properties[arg], sample); problem != "" {
		*problems = append(*problems, fmt.Sprintf("%s sample_arguments[%s]=%#v does not match provider schema: %s", meta.Name, arg, sample, problem))
	}
}

func TestBestInClassToolPresentationIsAuditableForNativeRendering(t *testing.T) {
	problems := []string{}

	for _, meta := range AllToolMetadata() {
		p := meta.Presentation
		if p.RendererStrategy == "generic" {
			problems = append(problems, fmt.Sprintf("%s renderer_strategy=generic, want explicit tool/family/artifact_type renderer path", meta.Name))
		}
		if p.RendererStrategy == "tool" && p.RendererKey != meta.Name {
			problems = append(problems, fmt.Sprintf("%s tool renderer_key=%q, want tool name", meta.Name, p.RendererKey))
		}
		if p.RendererStrategy == "family" && p.RendererKey != p.Family {
			problems = append(problems, fmt.Sprintf("%s family renderer_key=%q, want family %q", meta.Name, p.RendererKey, p.Family))
		}
		if p.RendererStrategy == "artifact_type" && p.RendererKey != p.NativeViewer {
			problems = append(problems, fmt.Sprintf("%s artifact_type renderer_key=%q, want native_viewer %q", meta.Name, p.RendererKey, p.NativeViewer))
		}

		if presentationFileCapabilityNeedsSourceRefs(p.FileCapability) && len(p.SourceRefs) == 0 {
			problems = append(problems, fmt.Sprintf("%s file_capability=%q needs source_refs", meta.Name, p.FileCapability))
		}
		if p.FileCapability == "remote_file_download" || p.FileCapability == "remote_file_read" {
			if !containsString(p.SourceRefs, "remote_id") || !containsString(p.SourceRefs, "artifact_id") {
				problems = append(problems, fmt.Sprintf("%s %s source_refs=%v, want remote_id and artifact_id", meta.Name, p.FileCapability, p.SourceRefs))
			}
			for _, ref := range remoteFileSpecificSourceRefs(meta.Name) {
				if !containsString(p.SourceRefs, ref) {
					problems = append(problems, fmt.Sprintf("%s %s source_refs=%v, missing connector-specific %s", meta.Name, p.FileCapability, p.SourceRefs, ref))
				}
			}
			if !containsString(p.NativeLinks, "source_url") {
				problems = append(problems, fmt.Sprintf("%s %s native_links=%v, want source_url", meta.Name, p.FileCapability, p.NativeLinks))
			}
		}
		if p.FileCapability == "remote_file_upload" {
			for _, ref := range []string{"artifact_id", "path", "local_path", "fileName", "contentText", "contentBase64"} {
				if !containsString(p.SourceRefs, ref) {
					problems = append(problems, fmt.Sprintf("%s remote upload source_refs=%v, missing %s", meta.Name, p.SourceRefs, ref))
				}
			}
		}
		if meta.Name == "display_table" || meta.Name == "build_chart_artifact" {
			for _, ref := range []string{"source.artifact_id", "source.path", "source.job_id", "owner_token"} {
				if !containsString(p.SourceRefs, ref) {
					problems = append(problems, fmt.Sprintf("%s source-backed artifact source_refs=%v, missing %s", meta.Name, p.SourceRefs, ref))
				}
			}
		}
		if meta.Name == "create_interactive_data_artifact" && !containsString(p.SourceRefs, "source.artifact_id") {
			problems = append(problems, fmt.Sprintf("%s source_refs=%v, want source.artifact_id", meta.Name, p.SourceRefs))
		}
		if p.NativeViewer == "file" {
			if !containsString(p.ArtifactTypes, "file") {
				problems = append(problems, fmt.Sprintf("%s native file viewer artifact_types=%v, want file", meta.Name, p.ArtifactTypes))
			}
			if len(p.FileKinds) == 0 || len(p.PreviewModes) == 0 {
				problems = append(problems, fmt.Sprintf("%s native file viewer must declare file_kinds and preview_modes", meta.Name))
			}
		}
		if p.ProducesFileArtifact && !containsString(p.ArtifactTypes, "file") {
			problems = append(problems, fmt.Sprintf("%s produces_file_artifact=true but artifact_types=%v", meta.Name, p.ArtifactTypes))
		}
		if p.PromotesNativeInput && len(p.SourceRefs) == 0 {
			problems = append(problems, fmt.Sprintf("%s promotes_native_input=true but source_refs are empty", meta.Name))
		}
		if p.ResultKind == "query" {
			if p.NativeViewer != "query" || !containsString(p.ArtifactTypes, "data") {
				problems = append(problems, fmt.Sprintf("%s query presentation native_viewer=%q artifact_types=%v, want query/data", meta.Name, p.NativeViewer, p.ArtifactTypes))
			}
			if (meta.ToolSetID == "query-lifecycle" || strings.HasPrefix(meta.Name, "run_")) && !containsString(p.NativeLinks, "warehouse_job") {
				problems = append(problems, fmt.Sprintf("%s query native_links=%v, want warehouse_job", meta.Name, p.NativeLinks))
			}
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		t.Fatalf("best-in-class presentation problems:\n%s", strings.Join(problems, "\n"))
	}
}

func remoteFileSpecificSourceRefs(toolName string) []string {
	switch toolName {
	case SlackToolDownloadFile:
		return []string{"file_id"}
	case "jira_read_attachment":
		return []string{"issueKey", "attachmentId"}
	case SharePointToolRead, SharePointToolSummarize, SharePointToolDownload:
		return []string{"item_url", "server_relative_url"}
	case ArxivToolDownloadPaper:
		return []string{"arxiv_id"}
	case ArxivToolGetPaperText, ArxivToolReadPaper:
		return []string{"paper"}
	default:
		return nil
	}
}

func isStableToolName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return !strings.Contains(value, "__") && value[0] != '_' && value[len(value)-1] != '_'
}

func isStableKebabID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return !strings.Contains(value, "--") && value[0] != '-' && value[len(value)-1] != '-'
}

func isUserFacingToolCopy(value string, requireSentencePunctuation bool) bool {
	value = strings.TrimSpace(value)
	if len(value) < 4 {
		return false
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "todo") || strings.Contains(lower, "tbd") || strings.Contains(lower, "fixme") {
		return false
	}
	if requireSentencePunctuation && !strings.ContainsAny(value[len(value)-1:], ".!?") {
		return false
	}
	return true
}

func isKnownToolTestContext(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "connection:") {
		return strings.TrimSpace(strings.TrimPrefix(value, "connection:")) != ""
	}
	switch value {
	case "artifact", "browser", "project", "query_job", "search_index", "thread":
		return true
	default:
		return false
	}
}

func toolTestSampleSchemaProblem(schema any, sample any) string {
	obj, ok := schema.(map[string]any)
	if !ok {
		return ""
	}
	if enumValues, ok := obj["enum"].([]any); ok && len(enumValues) > 0 && !sampleInEnum(sample, enumValues) {
		return fmt.Sprintf("value is not in enum %v", enumValues)
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if list, ok := obj[key].([]any); ok && len(list) > 0 {
			for _, entry := range list {
				if toolTestSampleSchemaProblem(entry, sample) == "" {
					return ""
				}
			}
			return fmt.Sprintf("value does not match any %s schema", key)
		}
	}
	if list, ok := obj["allOf"].([]any); ok && len(list) > 0 {
		for _, entry := range list {
			if problem := toolTestSampleSchemaProblem(entry, sample); problem != "" {
				return problem
			}
		}
	}

	types := schemaTypeList(obj["type"])
	if len(types) == 0 {
		if _, ok := obj["properties"]; ok {
			types = []string{"object"}
		} else if _, ok := obj["items"]; ok {
			types = []string{"array"}
		}
	}
	if len(types) > 0 {
		matches := false
		for _, typ := range types {
			if sampleMatchesJSONType(sample, typ) {
				matches = true
				break
			}
		}
		if !matches {
			return fmt.Sprintf("sample JSON type %q is not one of %v", sampleJSONType(sample), types)
		}
	}

	if num, ok := sampleNumber(sample); ok {
		if min, ok := schemaNumber(obj["minimum"]); ok && num < min {
			return fmt.Sprintf("number is below minimum %v", min)
		}
		if max, ok := schemaNumber(obj["maximum"]); ok && num > max {
			return fmt.Sprintf("number is above maximum %v", max)
		}
	}
	if str, ok := sample.(string); ok {
		if min, ok := schemaInt(obj["minLength"]); ok && len(str) < min {
			return fmt.Sprintf("string is shorter than minLength %d", min)
		}
		if max, ok := schemaInt(obj["maxLength"]); ok && len(str) > max {
			return fmt.Sprintf("string is longer than maxLength %d", max)
		}
	}
	if items, ok := sampleArray(sample); ok {
		if min, ok := schemaInt(obj["minItems"]); ok && len(items) < min {
			return fmt.Sprintf("array has fewer than minItems %d", min)
		}
		if max, ok := schemaInt(obj["maxItems"]); ok && len(items) > max {
			return fmt.Sprintf("array has more than maxItems %d", max)
		}
		if itemSchema, ok := obj["items"].(map[string]any); ok {
			for idx, item := range items {
				if problem := toolTestSampleSchemaProblem(itemSchema, item); problem != "" {
					return fmt.Sprintf("items[%d]: %s", idx, problem)
				}
			}
		}
	}
	if fields, ok := sampleObject(sample); ok {
		if required, ok := stringList(obj["required"]); ok {
			for _, key := range required {
				if _, exists := fields[key]; !exists {
					return fmt.Sprintf("object missing required property %q", key)
				}
			}
		}
		if properties, ok := obj["properties"].(map[string]any); ok {
			for key, value := range fields {
				if propertySchema, ok := properties[key]; ok {
					if problem := toolTestSampleSchemaProblem(propertySchema, value); problem != "" {
						return fmt.Sprintf("properties.%s: %s", key, problem)
					}
				}
			}
		}
	}
	return ""
}

func sampleInEnum(sample any, enumValues []any) bool {
	for _, value := range enumValues {
		if sampleScalarEqual(sample, value) {
			return true
		}
	}
	return false
}

func sampleScalarEqual(a any, b any) bool {
	if af, aok := sampleNumber(a); aok {
		if bf, bok := sampleNumber(b); bok {
			return af == bf
		}
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func schemaTypeList(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := []string{}
		for _, entry := range typed {
			if text, ok := entry.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func sampleMatchesJSONType(sample any, want string) bool {
	switch want {
	case "array":
		_, ok := sampleArray(sample)
		return ok
	case "boolean":
		_, ok := sample.(bool)
		return ok
	case "integer":
		return sampleIsInteger(sample)
	case "number":
		_, ok := sampleNumber(sample)
		return ok
	case "object":
		_, ok := sampleObject(sample)
		return ok
	case "string":
		_, ok := sample.(string)
		return ok
	case "null":
		return sample == nil
	default:
		return false
	}
}

func sampleJSONType(sample any) string {
	switch {
	case sample == nil:
		return "null"
	case sampleIsInteger(sample):
		return "integer"
	}
	if _, ok := sampleNumber(sample); ok {
		return "number"
	}
	if _, ok := sampleArray(sample); ok {
		return "array"
	}
	if _, ok := sampleObject(sample); ok {
		return "object"
	}
	switch sample.(type) {
	case bool:
		return "boolean"
	case string:
		return "string"
	default:
		return fmt.Sprintf("%T", sample)
	}
}

func sampleNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func sampleIsInteger(value any) bool {
	num, ok := sampleNumber(value)
	if !ok {
		return false
	}
	return num == float64(int64(num))
}

func schemaNumber(value any) (float64, bool) {
	return sampleNumber(value)
}

func schemaInt(value any) (int, bool) {
	num, ok := sampleNumber(value)
	if !ok || num != float64(int64(num)) {
		return 0, false
	}
	return int(num), true
}

func sampleArray(value any) ([]any, bool) {
	switch typed := value.(type) {
	case []any:
		return typed, true
	case []string:
		out := make([]any, 0, len(typed))
		for _, entry := range typed {
			out = append(out, entry)
		}
		return out, true
	default:
		return nil, false
	}
}

func sampleObject(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]string:
		out := make(map[string]any, len(typed))
		for key, entry := range typed {
			out[key] = entry
		}
		return out, true
	default:
		return nil, false
	}
}

func stringList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}

func schemaAllowsJSONType(schema any, want string) bool {
	obj, ok := schema.(map[string]any)
	if !ok {
		return false
	}
	if schemaTypeAllows(obj["type"], want) {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if schemaListAllowsJSONType(obj[key], want) {
			return true
		}
	}
	return false
}

func schemaListAllowsJSONType(value any, want string) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}
	for _, entry := range list {
		if schemaAllowsJSONType(entry, want) {
			return true
		}
	}
	return false
}

func schemaTypeAllows(value any, want string) bool {
	switch typed := value.(type) {
	case string:
		return typed == want
	case []string:
		for _, entry := range typed {
			if entry == want {
				return true
			}
		}
	case []any:
		for _, entry := range typed {
			if s, ok := entry.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}

func presentationFileCapabilityNeedsSourceRefs(value string) bool {
	switch strings.TrimSpace(value) {
	case "artifact_read",
		"attachment_inspect",
		"attachment_read",
		"attachment_search",
		"dataset_artifact_read",
		"local_folder_list",
		"local_file_read",
		"local_file_search",
		"local_repository",
		"remote_file_download",
		"remote_file_read",
		"remote_file_upload",
		"repository_clone":
		return true
	default:
		return false
	}
}

func intentionalOpenProviderSchemaPaths() map[string]map[string]bool {
	return map[string]map[string]bool{
		"display_table": {
			// Chart option objects intentionally accept upstream Highcharts/ECharts
			// extension maps. They should be converted to stricter typed schemas
			// when the chart contract is narrowed.
			"$.properties.chart_spec.properties.extensions.properties.drilldown_async.properties.series_by_id":   true,
			"$.properties.chart_spec.properties.options":                                                         true,
			"$.properties.echarts_spec.properties.extensions.properties.drilldown_async.properties.series_by_id": true,
			"$.properties.echarts_spec.properties.options":                                                       true,
		},
		"jira_update_issue": {
			// Jira custom fields are project-specific and keyed by field id.
			"$.properties.fields": true,
		},
	}
}

func walkSchemaObjectClosures(toolName, path string, schema any, intentionalOpenPaths map[string]map[string]bool, problems *[]string) {
	obj, ok := schema.(map[string]any)
	if !ok {
		return
	}

	if schemaLooksLikeObject(obj) {
		isIntentional := intentionalSchemaPathAllowed(intentionalOpenPaths, toolName, path)
		if got, ok := obj["additionalProperties"].(bool); !ok || got {
			if !isIntentional {
				*problems = append(*problems, fmt.Sprintf("%s schema %s additionalProperties=%#v, want false", toolName, path, obj["additionalProperties"]))
			}
		} else if isIntentional {
			*problems = append(*problems, fmt.Sprintf("%s schema %s is listed in intentionalOpenProviderSchemaPaths but is now closed; remove it from the intentional dynamic map list", toolName, path))
		}
	}

	if props, ok := obj["properties"].(map[string]any); ok {
		for key, value := range props {
			walkSchemaObjectClosures(toolName, path+".properties."+key, value, intentionalOpenPaths, problems)
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		walkSchemaObjectClosures(toolName, path+"."+key, obj[key], intentionalOpenPaths, problems)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		list, ok := obj[key].([]any)
		if !ok {
			continue
		}
		for idx, value := range list {
			walkSchemaObjectClosures(toolName, fmt.Sprintf("%s.%s[%d]", path, key, idx), value, intentionalOpenPaths, problems)
		}
	}
}

func schemaLooksLikeObject(obj map[string]any) bool {
	if schemaTypeAllows(obj["type"], "object") {
		return true
	}
	_, hasProperties := obj["properties"]
	return hasProperties
}

func intentionalSchemaPathAllowed(intentional map[string]map[string]bool, toolName, path string) bool {
	paths, ok := intentional[toolName]
	if !ok {
		return false
	}
	if !paths[path] {
		return false
	}
	delete(paths, path)
	if len(paths) == 0 {
		delete(intentional, toolName)
	}
	return true
}
