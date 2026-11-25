package tools

import "strings"

func chartCapabilityOutput(toolName, engine string, snapshot map[string]any) Output {
	return JSONOKWithMetadata(snapshot, chartCapabilityResultMetadata(toolName, engine, snapshot))
}

func chartCapabilityResultMetadata(toolName, engine string, snapshot map[string]any) map[string]any {
	toolName = strings.TrimSpace(toolName)
	engine = strings.TrimSpace(engine)
	meta := map[string]any{
		"provider":  engine,
		"product":   engine,
		"family":    "artifacts",
		"kind":      "chart_capabilities",
		"tool_name": toolName,
		"readonly":  true,
	}
	if version := firstStringMapValue(snapshot, "capability_version"); version != "" {
		meta["capability_version"] = version
	}
	if specVersion := firstStringMapValue(snapshot, "chart_spec_version"); specVersion != "" {
		meta["chart_spec_version"] = specVersion
	}
	if schemaVersion := firstStringMapValue(snapshot, "schema_version"); schemaVersion != "" {
		meta["schema_version"] = schemaVersion
	}
	if toolMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		meta["connector_id"] = toolMeta.ConnectorID
		meta["category"] = string(toolMeta.Category)
		meta["origin"] = string(toolMeta.Origin)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := chartCapabilityPresentationMetadata(toolMeta.Presentation)
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
			if v := firstStringMapValue(presentation, "family"); v != "" {
				meta["family"] = v
			}
			if v := firstStringMapValue(presentation, "result_kind"); v != "" {
				meta["result_kind"] = v
			}
			if v := firstStringMapValue(presentation, "native_viewer"); v != "" {
				meta["native_viewer"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_strategy"); v != "" {
				meta["renderer_strategy"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_key"); v != "" {
				meta["renderer_key"] = v
			}
		}
	}
	return meta
}

func chartCapabilityPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putChartCapabilityPresentationString(out, "family", p.Family)
	putChartCapabilityPresentationString(out, "result_kind", p.ResultKind)
	putChartCapabilityPresentationString(out, "renderer_strategy", p.RendererStrategy)
	putChartCapabilityPresentationString(out, "renderer_key", p.RendererKey)
	putChartCapabilityPresentationString(out, "native_viewer", p.NativeViewer)
	putChartCapabilityPresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putChartCapabilityPresentationString(out, "file_capability", p.FileCapability)
	putChartCapabilityPresentationStrings(out, "file_kinds", p.FileKinds)
	putChartCapabilityPresentationStrings(out, "preview_modes", p.PreviewModes)
	putChartCapabilityPresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putChartCapabilityPresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func putChartCapabilityPresentationString(out map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[key] = value
}

func putChartCapabilityPresentationStrings(out map[string]any, key string, values []string) {
	if len(values) == 0 {
		return
	}
	copied := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		copied = append(copied, value)
	}
	if len(copied) == 0 {
		return
	}
	out[key] = copied
}
