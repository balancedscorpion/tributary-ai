package tools

import "strings"

func datasourceToolResultMetadata(toolName, provider, kind string, extra map[string]any) map[string]any {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "datasource"
	}
	meta := map[string]any{
		"provider":  provider,
		"product":   provider,
		"family":    provider,
		"kind":      strings.TrimSpace(kind),
		"tool_name": strings.TrimSpace(toolName),
		"readonly":  true,
	}
	if catalogMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_set_id"] = catalogMeta.ToolSetID
		meta["tool_set_name"] = catalogMeta.ToolSetName
		meta["connector_id"] = catalogMeta.ConnectorID
		meta["category"] = string(catalogMeta.Category)
		meta["origin"] = string(catalogMeta.Origin)
		meta["readonly"] = catalogMeta.Category == ToolCategoryRead
		presentation := datasourceToolPresentationMetadata(catalogMeta.Presentation)
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
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		meta[key] = value
	}
	return meta
}

func datasourceAsyncQueryResultMetadata(toolName, provider string, qm map[string]any) map[string]any {
	status := strings.TrimSpace(anyToString(qm["status"]))
	if status == "" {
		status = "running"
	}
	kind := "query_running"
	if strings.EqualFold(status, "profiling") {
		kind = "query_profiling"
	}
	extra := map[string]any{
		"kind":        kind,
		"status":      status,
		"async_query": true,
	}
	for _, key := range []string{
		"job_id",
		"artifact_id",
		"provider_job_id",
		"provider_job_url",
		"warehouse_job_id",
		"warehouse_location",
		"project_id",
		"job_location",
		"statement_id",
		"query_id",
		"next_uri",
		"owner_token",
		"thread_id",
		"adaptive_wait_ms",
		"submitted_at_ms",
		"requested_limit",
		"effective_limit",
		"configured_limit",
		"limit_capped",
		"full_data_requested",
		"execution_status",
		"remote_submitted",
		"submit_dispatched",
		"provider_submission_confirmed",
		"submission_uncertain",
		"monitor_recovery_pending",
		"monitor_recovery_detail",
		"cancellation_pending",
		"provider_max_result_bytes",
		"effective_provider_result_byte_limit",
		"profile_outcome",
		"query_profile",
		"profile",
		"profile_handle",
		"profile_phase",
		"profile_dispatched",
		"profile_pending",
		"rewrite_required",
		"replacement_required",
		"approval_required",
		"validation_state",
	} {
		if value, ok := qm[key]; ok {
			extra[key] = value
		}
	}
	return datasourceToolResultMetadata(toolName, provider, kind, extra)
}

func datasourceCompletedQueryResultMetadata(toolName, provider string, qm map[string]any) map[string]any {
	extra := map[string]any{
		"kind":   "query_result",
		"status": "completed",
	}
	for _, key := range []string{
		"job_id",
		"artifact_id",
		"full_artifact_id",
		"provider_job_id",
		"provider_job_url",
		"warehouse_job_id",
		"warehouse_location",
		"project_id",
		"job_location",
		"catalog",
		"catalog_name",
		"schema_name",
		"warehouse_id",
		"statement_id",
		"query_id",
		"owner_token",
		"thread_id",
		"requested_limit",
		"effective_limit",
		"configured_limit",
		"limit_capped",
		"full_data_requested",
		"rows_returned",
		"rows_preview_count",
		"row_count_total",
		"page_size",
		"offset",
		"has_more",
		"next_page_token",
		"truncated_to_configured_limit",
		"result_counts",
		"schema",
		"description",
	} {
		if value, ok := qm[key]; ok {
			extra[key] = value
		}
	}
	if status := strings.TrimSpace(anyToString(qm["status"])); status != "" {
		extra["status"] = status
	}
	return datasourceToolResultMetadata(toolName, provider, "query_result", extra)
}

func datasourceMetadataOutput(toolName, provider, kind string, result map[string]any, extra map[string]any) Output {
	return JSONOKWithMetadata(result, datasourceToolResultMetadata(toolName, provider, kind, extra))
}

func datasourceToolPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putDatasourcePresentationString(out, "family", p.Family)
	putDatasourcePresentationString(out, "result_kind", p.ResultKind)
	putDatasourcePresentationString(out, "renderer_strategy", p.RendererStrategy)
	putDatasourcePresentationString(out, "renderer_key", p.RendererKey)
	putDatasourcePresentationString(out, "native_viewer", p.NativeViewer)
	putDatasourcePresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putDatasourcePresentationString(out, "file_capability", p.FileCapability)
	putDatasourcePresentationStrings(out, "file_kinds", p.FileKinds)
	putDatasourcePresentationStrings(out, "preview_modes", p.PreviewModes)
	putDatasourcePresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putDatasourcePresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func putDatasourcePresentationString(out map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[key] = value
}

func putDatasourcePresentationStrings(out map[string]any, key string, values []string) {
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
