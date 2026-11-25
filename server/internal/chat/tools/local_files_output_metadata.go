package tools

import "strings"

func localFilesResultMetadata(toolName, kind string, extra map[string]any) map[string]any {
	toolName = strings.TrimSpace(toolName)
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "local_file_result"
	}
	meta := map[string]any{
		"provider":  "local-files",
		"product":   "local-files",
		"family":    "local-files",
		"tool_name": toolName,
		"kind":      kind,
		"readonly":  true,
	}
	if toolMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		meta["connector_id"] = toolMeta.ConnectorID
		meta["category"] = string(toolMeta.Category)
		meta["origin"] = string(toolMeta.Origin)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := localFilesToolPresentationMetadata(toolMeta.Presentation)
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

func localFilesOutput(toolName, kind string, result map[string]any, extra map[string]any) Output {
	return JSONOKWithMetadata(result, localFilesResultMetadata(toolName, kind, extra))
}

func localFilesToolPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putLocalFilesPresentationString(out, "family", p.Family)
	putLocalFilesPresentationString(out, "result_kind", p.ResultKind)
	putLocalFilesPresentationString(out, "renderer_strategy", p.RendererStrategy)
	putLocalFilesPresentationString(out, "renderer_key", p.RendererKey)
	putLocalFilesPresentationString(out, "native_viewer", p.NativeViewer)
	putLocalFilesPresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putLocalFilesPresentationString(out, "file_capability", p.FileCapability)
	putLocalFilesPresentationStrings(out, "file_kinds", p.FileKinds)
	putLocalFilesPresentationStrings(out, "preview_modes", p.PreviewModes)
	putLocalFilesPresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putLocalFilesPresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func putLocalFilesPresentationString(out map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[key] = value
}

func putLocalFilesPresentationStrings(out map[string]any, key string, values []string) {
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
