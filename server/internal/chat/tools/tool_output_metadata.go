package tools

import "strings"

func toolCatalogResultMetadata(toolName, kind string, extra map[string]any) map[string]any {
	toolName = strings.TrimSpace(toolName)
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "tool_result"
	}

	provider := "tool"
	product := "tool"
	family := "tool"
	meta := map[string]any{
		"provider":  provider,
		"product":   product,
		"family":    family,
		"tool_name": toolName,
		"kind":      kind,
		"readonly":  true,
	}
	if toolMeta, ok := ToolMetadataByName(toolName); ok {
		provider = strings.TrimSpace(toolMeta.ConnectorID)
		if provider == "" {
			provider = strings.TrimSpace(toolMeta.ToolSetID)
		}
		if provider == "" {
			provider = "tool"
		}
		family = strings.TrimSpace(toolMeta.Presentation.Family)
		if family == "" {
			family = provider
		}
		product = family
		meta["provider"] = provider
		meta["product"] = product
		meta["family"] = family
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		meta["connector_id"] = toolMeta.ConnectorID
		meta["category"] = string(toolMeta.Category)
		meta["origin"] = string(toolMeta.Origin)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := toolPresentationMetadata(toolMeta.Presentation)
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
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
			if v := firstStringMapValue(presentation, "file_capability"); v != "" {
				meta["file_capability"] = v
			}
		}
	}
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		meta[key] = value
	}
	return meta
}

func toolPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putToolPresentationString(out, "family", p.Family)
	putToolPresentationString(out, "result_kind", p.ResultKind)
	putToolPresentationString(out, "renderer_strategy", p.RendererStrategy)
	putToolPresentationString(out, "renderer_key", p.RendererKey)
	putToolPresentationString(out, "native_viewer", p.NativeViewer)
	putToolPresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putToolPresentationString(out, "file_capability", p.FileCapability)
	putToolPresentationStrings(out, "file_kinds", p.FileKinds)
	putToolPresentationStrings(out, "preview_modes", p.PreviewModes)
	putToolPresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putToolPresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func putToolPresentationString(out map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[key] = value
}

func putToolPresentationStrings(out map[string]any, key string, values []string) {
	clean := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		clean = append(clean, value)
	}
	if len(clean) == 0 {
		return
	}
	out[key] = clean
}
