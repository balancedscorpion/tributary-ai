package tools

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	EchartsPlotSpecVersionV1 = "echarts-plot-spec.v1"
	echartsEngineName        = "echarts"
)

//go:embed generated/echarts-capabilities.json
var embeddedEchartsCapabilities []byte

var (
	fallbackEchartsModules = map[string]struct{}{
		"core":    {},
		"dataviz": {},
		"graph":   {},
		"all":     {},
	}
	fallbackEchartsSeriesByModule = map[string]map[string]struct{}{
		"core": {
			"line": {}, "bar": {}, "scatter": {}, "pie": {}, "radar": {},
			"funnel": {}, "gauge": {}, "candlestick": {}, "boxplot": {},
		},
		"dataviz": {
			"heatmap": {}, "treemap": {}, "sunburst": {}, "sankey": {},
			"parallel": {}, "themeRiver": {},
		},
		"graph": {
			"graph": {}, "tree": {},
		},
		"all": {
			"bar": {}, "boxplot": {}, "candlestick": {}, "chord": {}, "custom": {},
			"effectScatter": {}, "funnel": {}, "gauge": {}, "graph": {}, "heatmap": {},
			"line": {}, "lines": {}, "map": {}, "parallel": {}, "pictorialBar": {},
			"pie": {}, "radar": {}, "sankey": {}, "scatter": {}, "sunburst": {},
			"themeRiver": {}, "tree": {}, "treemap": {},
		},
	}
	fallbackEchartsOptionRootsAll = map[string]struct{}{
		"angleAxis": {}, "animation": {}, "aria": {}, "axisPointer": {},
		"backgroundColor": {}, "baseOption": {}, "brush": {}, "calendar": {},
		"color": {}, "darkMode": {}, "dataZoom": {}, "dataset": {}, "geo": {},
		"graphic": {}, "grid": {}, "hoverLayerThreshold": {}, "legacyViewCoordSysCenterBase": {},
		"legend": {}, "matrix": {}, "media": {}, "options": {}, "parallel": {},
		"parallelAxis": {}, "polar": {}, "radar": {}, "radiusAxis": {}, "series": {},
		"singleAxis": {}, "stateAnimation": {}, "textStyle": {}, "thumbnail": {},
		"timeline": {}, "title": {}, "toolbox": {}, "tooltip": {}, "useUTC": {},
		"visualMap": {}, "xAxis": {}, "yAxis": {},
	}
	fallbackEchartsAllowedOptionRoots = map[string]map[string]struct{}{
		"core":    cloneSet(fallbackEchartsOptionRootsAll),
		"dataviz": cloneSet(fallbackEchartsOptionRootsAll),
		"graph":   cloneSet(fallbackEchartsOptionRootsAll),
		"all":     cloneSet(fallbackEchartsOptionRootsAll),
	}
	fallbackEchartsCallbackIDsByKind = map[string]map[string]struct{}{
		"tooltip.formatter": {
			"echarts_tooltip_compact":     {},
			"echarts_tooltip_currency":    {},
			"echarts_tooltip_percent_1dp": {},
		},
		"drilldown.loader": {
			"drilldown_embedded_series": {},
		},
		"series.custom.render_item": {
			"custom_render_rect": {},
		},
	}
	fallbackEchartsExtensionModules = map[string]struct{}{
		"bmap":                {},
		"datatool":            {},
		"labellayout":         {},
		"universaltransition": {},
	}
)

type echartsCapabilitiesContext struct {
	SupportedSchemaVersions map[string]struct{}
	SupportedModules        map[string]struct{}
	SupportedSeriesByModule map[string]map[string]struct{}
	AllowedOptionRoots      map[string]map[string]struct{}
	CallbackIDsByKind       map[string]map[string]struct{}
	ExtensionModules        map[string]struct{}
}

func loadEchartsCapabilitiesSnapshot() (map[string]any, error) {
	raw := strings.TrimSpace(string(embeddedEchartsCapabilities))
	if raw == "" {
		return nil, errors.New("embedded echarts capabilities snapshot is empty")
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, fmt.Errorf("parse echarts capabilities snapshot: %w", err)
	}
	return snapshot, nil
}

func echartsCapabilityVersion(snapshot map[string]any) string {
	if snapshot == nil {
		return ""
	}
	v, _ := snapshot["capability_version"].(string)
	return strings.TrimSpace(v)
}

func buildEchartsCapabilitiesContext(snapshot map[string]any) echartsCapabilitiesContext {
	ctx := echartsCapabilitiesContext{
		SupportedSchemaVersions: map[string]struct{}{
			EchartsPlotSpecVersionV1: {},
		},
		SupportedModules:        cloneSetMap(fallbackEchartsModules),
		SupportedSeriesByModule: cloneNestedSetMap(fallbackEchartsSeriesByModule),
		AllowedOptionRoots:      cloneNestedSetMap(fallbackEchartsAllowedOptionRoots),
		CallbackIDsByKind:       cloneNestedSetMap(fallbackEchartsCallbackIDsByKind),
		ExtensionModules:        cloneSetMap(fallbackEchartsExtensionModules),
	}

	if snapshot == nil {
		return ctx
	}

	if versions := toStringSlice(snapshot["chart_spec_versions"]); len(versions) > 0 {
		ctx.SupportedSchemaVersions = map[string]struct{}{}
		for _, version := range versions {
			ctx.SupportedSchemaVersions[version] = struct{}{}
		}
	}
	if v := strings.TrimSpace(anyToString(snapshot["chart_spec_version"])); v != "" {
		ctx.SupportedSchemaVersions[v] = struct{}{}
	}

	if supportedModules := toStringSlice(snapshot["supported_modules"]); len(supportedModules) > 0 {
		ctx.SupportedModules = map[string]struct{}{}
		for _, module := range supportedModules {
			ctx.SupportedModules[module] = struct{}{}
		}
	}

	if rawMap, ok := snapshot["supported_series_types_by_profile"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for moduleRaw, value := range rawMap {
			module := strings.ToLower(strings.TrimSpace(moduleRaw))
			if module == "" {
				continue
			}
			seriesTypes := toStringSlice(value)
			if len(seriesTypes) == 0 {
				continue
			}
			parsed[module] = map[string]struct{}{}
			for _, seriesType := range seriesTypes {
				parsed[module][seriesType] = struct{}{}
			}
		}
		if len(parsed) > 0 {
			ctx.SupportedSeriesByModule = parsed
		}
	} else if rawMap, ok := snapshot["supported_series_types_by_module"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for moduleRaw, value := range rawMap {
			module := strings.ToLower(strings.TrimSpace(moduleRaw))
			if module == "" {
				continue
			}
			seriesTypes := toStringSlice(value)
			if len(seriesTypes) == 0 {
				continue
			}
			parsed[module] = map[string]struct{}{}
			for _, seriesType := range seriesTypes {
				parsed[module][seriesType] = struct{}{}
			}
		}
		if len(parsed) > 0 {
			ctx.SupportedSeriesByModule = parsed
		}
	}

	if rawMap, ok := snapshot["allowed_option_paths_by_profile"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for moduleRaw, value := range rawMap {
			module := strings.ToLower(strings.TrimSpace(moduleRaw))
			if module == "" {
				continue
			}
			roots := optionRootsFromPaths(toStringSlicePreserveCase(value))
			if len(roots) == 0 {
				continue
			}
			parsed[module] = roots
		}
		if len(parsed) > 0 {
			ctx.AllowedOptionRoots = parsed
		}
	} else if rawMap, ok := snapshot["allowed_option_paths_by_module"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for moduleRaw, value := range rawMap {
			module := strings.ToLower(strings.TrimSpace(moduleRaw))
			if module == "" {
				continue
			}
			roots := optionRootsFromPaths(toStringSlicePreserveCase(value))
			if len(roots) == 0 {
				continue
			}
			parsed[module] = roots
		}
		if len(parsed) > 0 {
			ctx.AllowedOptionRoots = parsed
		}
	} else if paths := toStringSlicePreserveCase(snapshot["allowed_option_paths"]); len(paths) > 0 {
		roots := optionRootsFromPaths(paths)
		if len(roots) > 0 {
			for module := range ctx.SupportedModules {
				ctx.AllowedOptionRoots[module] = cloneSet(roots)
			}
		}
	}

	if rawCallbacks, ok := snapshot["callback_ids_by_kind"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for kindRaw, value := range rawCallbacks {
			kind := strings.ToLower(strings.TrimSpace(kindRaw))
			if kind == "" {
				continue
			}
			ids := toStringSlice(value)
			if len(ids) == 0 {
				continue
			}
			parsed[kind] = map[string]struct{}{}
			for _, id := range ids {
				parsed[kind][id] = struct{}{}
			}
		}
		if len(parsed) > 0 {
			ctx.CallbackIDsByKind = parsed
		}
	}

	if rawExtensions := toStringSlice(snapshot["supported_extension_modules"]); len(rawExtensions) > 0 {
		ctx.ExtensionModules = map[string]struct{}{}
		for _, module := range rawExtensions {
			ctx.ExtensionModules[module] = struct{}{}
		}
	}

	for module := range ctx.SupportedModules {
		if _, ok := ctx.SupportedSeriesByModule[module]; !ok {
			if fallback, hasFallback := fallbackEchartsSeriesByModule[module]; hasFallback {
				ctx.SupportedSeriesByModule[module] = cloneSet(fallback)
			}
		}
		if _, ok := ctx.AllowedOptionRoots[module]; !ok {
			if fallback, hasFallback := fallbackEchartsAllowedOptionRoots[module]; hasFallback {
				ctx.AllowedOptionRoots[module] = cloneSet(fallback)
			}
		}
	}

	return ctx
}

func normalizeEchartsSpecInput(rawSpec any, specVersion string) (*normalizedChartSpec, error) {
	if rawSpec == nil {
		return nil, nil
	}

	snapshot, err := loadEchartsCapabilitiesSnapshot()
	if err != nil {
		return nil, err
	}
	capabilityVersion := echartsCapabilityVersion(snapshot)
	if capabilityVersion == "" {
		return nil, errors.New("echarts capability_version is missing")
	}
	if strings.TrimSpace(specVersion) != "" && strings.TrimSpace(specVersion) != capabilityVersion {
		return nil, fmt.Errorf("echarts_spec_version mismatch: expected %q", capabilityVersion)
	}

	caps := buildEchartsCapabilitiesContext(snapshot)
	normalized, module, modules, err := normalizeEchartsPlotSpec(rawSpec, caps)
	if err != nil {
		return nil, err
	}

	return &normalizedChartSpec{
		PlotSpec:          normalized,
		CapabilityVersion: capabilityVersion,
		Module:            module,
		Modules:           modules,
	}, nil
}

func normalizeEchartsPlotSpec(rawSpec any, caps echartsCapabilitiesContext) (map[string]any, string, []string, error) {
	spec, ok := rawSpec.(map[string]any)
	if !ok {
		return nil, "", nil, errors.New("echarts_spec must be an object")
	}

	schemaVersion := strings.TrimSpace(anyToString(spec["schema_version"]))
	if schemaVersion == "" {
		return nil, "", nil, errors.New("echarts_spec.schema_version is required")
	}
	if _, ok := caps.SupportedSchemaVersions[schemaVersion]; !ok {
		return nil, "", nil, fmt.Errorf("echarts_spec.schema_version %q is not supported", schemaVersion)
	}

	module := strings.ToLower(strings.TrimSpace(anyToString(spec["module"])))
	if module == "" {
		return nil, "", nil, errors.New("echarts_spec.module is required")
	}
	if _, ok := caps.SupportedModules[module]; !ok {
		allowed := make([]string, 0, len(caps.SupportedModules))
		for key := range caps.SupportedModules {
			allowed = append(allowed, key)
		}
		sort.Strings(allowed)
		return nil, "", nil, fmt.Errorf("echarts_spec.module %q is not supported (allowed: %s)", module, strings.Join(allowed, ", "))
	}

	options, ok := spec["options"].(map[string]any)
	if !ok {
		return nil, "", nil, errors.New("echarts_spec.options must be an object")
	}
	if err := rejectExecutableValues(options, "options"); err != nil {
		return nil, "", nil, err
	}

	resolvedModules, err := resolveRequestedEchartsModules(spec["modules"], caps)
	if err != nil {
		return nil, "", nil, err
	}

	normalizedOptions, err := normalizeEchartsOptions(module, options, caps)
	if err != nil {
		return nil, "", nil, err
	}

	normalized := map[string]any{
		"schema_version": schemaVersion,
		"module":         module,
		"options":        normalizedOptions,
	}
	if len(resolvedModules) > 0 {
		normalized["modules"] = toAnySlice(resolvedModules)
	}
	if extRaw, exists := spec["extensions"]; exists {
		extensions, err := normalizeEchartsExtensions(module, extRaw, caps)
		if err != nil {
			return nil, "", nil, err
		}
		if len(extensions) > 0 {
			normalized["extensions"] = extensions
		}
	}

	return normalized, module, resolvedModules, nil
}

func normalizeEchartsOptions(module string, options map[string]any, caps echartsCapabilitiesContext) (map[string]any, error) {
	allowedRoots := caps.AllowedOptionRoots[module]
	if len(allowedRoots) == 0 {
		allowedRoots = fallbackEchartsAllowedOptionRoots[module]
	}
	if len(allowedRoots) == 0 {
		allowedRoots = fallbackEchartsAllowedOptionRoots["core"]
	}

	out := map[string]any{}
	for key, value := range options {
		if _, ok := allowedRoots[key]; !ok {
			return nil, fmt.Errorf("unsupported options key %q for module %q", key, module)
		}
		switch key {
		case "series":
			series, err := normalizeEchartsSeries(module, value, caps)
			if err != nil {
				return nil, err
			}
			out["series"] = series
		case "tooltip":
			tooltip, err := normalizeEchartsTooltip(value, caps)
			if err != nil {
				return nil, err
			}
			if len(tooltip) > 0 {
				out["tooltip"] = tooltip
			}
		default:
			sanitized, ok := sanitizeJSONValue(value)
			if !ok {
				return nil, fmt.Errorf("options.%s contains unsupported value", key)
			}
			out[key] = sanitized
		}
	}

	if _, ok := out["series"]; !ok {
		return nil, errors.New("echarts_spec.options.series is required")
	}
	return out, nil
}

func normalizeEchartsTooltip(raw any, caps echartsCapabilitiesContext) (map[string]any, error) {
	tooltipRaw, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("echarts_spec.options.tooltip must be an object")
	}
	out := map[string]any{}
	if trigger := strings.TrimSpace(anyToString(tooltipRaw["trigger"])); trigger != "" {
		out["trigger"] = trigger
	}
	if show, ok := tooltipRaw["show"].(bool); ok {
		out["show"] = show
	}
	if confine, ok := tooltipRaw["confine"].(bool); ok {
		out["confine"] = confine
	} else {
		out["confine"] = true
	}
	if id := strings.TrimSpace(anyToString(tooltipRaw["formatter_id"])); id != "" {
		allowed := caps.CallbackIDsByKind["tooltip.formatter"]
		if len(allowed) == 0 {
			allowed = fallbackEchartsCallbackIDsByKind["tooltip.formatter"]
		}
		if _, ok := allowed[id]; !ok {
			return nil, fmt.Errorf("unsupported tooltip.formatter_id %q", id)
		}
		out["formatter_id"] = id
	}
	if _, exists := tooltipRaw["formatter"]; exists {
		return nil, errors.New("echarts_spec.options.tooltip.formatter is not allowed; use formatter_id")
	}
	if triggerOn := strings.TrimSpace(anyToString(tooltipRaw["triggerOn"])); triggerOn != "" {
		out["triggerOn"] = triggerOn
	}
	if renderMode := strings.TrimSpace(anyToString(tooltipRaw["renderMode"])); renderMode != "" {
		out["renderMode"] = renderMode
	}
	return out, nil
}

func normalizeEchartsSeries(module string, raw any, caps echartsCapabilitiesContext) ([]any, error) {
	seriesRaw, ok := raw.([]any)
	if !ok {
		return nil, errors.New("echarts_spec.options.series must be an array")
	}
	if len(seriesRaw) == 0 {
		return nil, errors.New("echarts_spec.options.series cannot be empty")
	}

	allowedTypes := caps.SupportedSeriesByModule[module]
	if len(allowedTypes) == 0 {
		allowedTypes = fallbackEchartsSeriesByModule[module]
	}
	if len(allowedTypes) == 0 {
		allowedTypes = fallbackEchartsSeriesByModule["core"]
	}

	out := make([]any, 0, len(seriesRaw))
	for i, entry := range seriesRaw {
		seriesMap, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("series[%d] must be an object", i)
		}
		seriesType := strings.ToLower(strings.TrimSpace(anyToString(seriesMap["type"])))
		if seriesType == "" {
			return nil, fmt.Errorf("series[%d].type is required", i)
		}
		if _, ok := allowedTypes[seriesType]; !ok {
			return nil, fmt.Errorf("series[%d].type %q is not supported for module %q", i, seriesType, module)
		}

		normalized := map[string]any{
			"type": seriesType,
		}
		hasData := false
		customRenderItemID := ""
		for key, value := range seriesMap {
			switch key {
			case "type":
				continue
			case "data":
				dataRaw, ok := value.([]any)
				if !ok {
					return nil, fmt.Errorf("series[%d].data must be an array", i)
				}
				data, err := sanitizeAndNormalizeEchartsSeriesData(dataRaw, seriesType)
				if err != nil {
					return nil, fmt.Errorf("series[%d].data: %w", i, err)
				}
				normalized["data"] = data
				hasData = true
			case "renderItem":
				return nil, fmt.Errorf("series[%d].renderItem is not allowed; use render_item_id", i)
			case "render_item_id":
				id := strings.TrimSpace(anyToString(value))
				if id == "" {
					return nil, fmt.Errorf("series[%d].render_item_id must be a non-empty string", i)
				}
				customRenderItemID = id
			default:
				sanitized, ok := sanitizeJSONValue(value)
				if !ok {
					return nil, fmt.Errorf("series[%d].%s contains unsupported value", i, key)
				}
				normalized[key] = sanitized
				if key == "nodes" || key == "links" {
					hasData = true
				}
			}
		}

		if seriesType == "custom" {
			if customRenderItemID == "" {
				return nil, fmt.Errorf("series[%d].render_item_id is required for custom series", i)
			}
			allowed := caps.CallbackIDsByKind["series.custom.render_item"]
			if len(allowed) == 0 {
				allowed = fallbackEchartsCallbackIDsByKind["series.custom.render_item"]
			}
			if _, ok := allowed[customRenderItemID]; !ok {
				return nil, fmt.Errorf("unsupported series[%d].render_item_id %q", i, customRenderItemID)
			}
			normalized["render_item_id"] = customRenderItemID
		} else if customRenderItemID != "" {
			return nil, fmt.Errorf("series[%d].render_item_id is only supported for custom series", i)
		}

		if seriesType != "custom" && !hasData {
			return nil, fmt.Errorf("series[%d].data is required", i)
		}
		out = append(out, normalized)
	}
	return out, nil
}

func sanitizeAndNormalizeEchartsSeriesData(values []any, seriesType string) ([]any, error) {
	cleaned := make([]any, 0, len(values))
	for _, value := range values {
		sanitized, ok := sanitizeSeriesPoint(value)
		if !ok {
			continue
		}
		normalized, ok := normalizeEchartsSeriesPointByType(sanitized, seriesType)
		if !ok {
			continue
		}
		cleaned = append(cleaned, normalized)
	}
	if len(cleaned) == 0 {
		return nil, errors.New("has no valid points after normalization")
	}
	return cleaned, nil
}

func normalizeEchartsSeriesPointByType(point any, seriesType string) (any, bool) {
	switch strings.ToLower(strings.TrimSpace(seriesType)) {
	case "line", "bar":
		if pointMap, ok := point.(map[string]any); ok {
			out := map[string]any{}
			for key, value := range pointMap {
				if key == "__proto__" || key == "constructor" || key == "prototype" {
					continue
				}
				out[key] = coerceEchartsNumericLike(value)
			}

			var (
				candidate    any
				hasCandidate bool
			)
			if value, exists := out["value"]; exists {
				candidate = value
				hasCandidate = true
			} else if yValue, hasY := out["y"]; hasY {
				if xValue, hasX := out["x"]; hasX {
					candidate = []any{xValue, yValue}
				} else {
					candidate = yValue
				}
				hasCandidate = true
			}
			if !hasCandidate {
				return nil, false
			}

			normalized, ok := normalizeEchartsCartesianPointValue(candidate)
			if !ok {
				return nil, false
			}
			out["value"] = normalized
			delete(out, "x")
			delete(out, "y")
			return out, true
		}
		return normalizeEchartsCartesianPointValue(point)
	case "scatter", "effectscatter":
		if pointMap, ok := point.(map[string]any); ok {
			out := map[string]any{}
			for key, value := range pointMap {
				if key == "__proto__" || key == "constructor" || key == "prototype" {
					continue
				}
				out[key] = coerceEchartsNumericLike(value)
			}

			var (
				candidate    any
				hasCandidate bool
			)
			if value, exists := out["value"]; exists {
				candidate = value
				hasCandidate = true
			} else if xValue, hasX := out["x"]; hasX {
				if yValue, hasY := out["y"]; hasY {
					candidate = []any{xValue, yValue}
					hasCandidate = true
				}
			}
			if !hasCandidate {
				return nil, false
			}

			normalized, ok := normalizeEchartsScatterPointValue(candidate)
			if !ok {
				return nil, false
			}
			out["value"] = normalized
			delete(out, "x")
			delete(out, "y")
			return out, true
		}
		return normalizeEchartsScatterPointValue(point)
	default:
		return point, true
	}
}

func normalizeEchartsCartesianPointValue(value any) (any, bool) {
	if arr, ok := value.([]any); ok {
		if len(arr) == 0 {
			return nil, false
		}
		normalized := make([]any, 0, len(arr))
		for _, entry := range arr {
			normalized = append(normalized, coerceEchartsNumericLike(entry))
		}
		last := normalized[len(normalized)-1]
		if last == nil {
			return normalized, true
		}
		if !isFiniteEchartsNumber(last) {
			return nil, false
		}
		return normalized, true
	}

	normalized := coerceEchartsNumericLike(value)
	if normalized == nil {
		return nil, true
	}
	if !isFiniteEchartsNumber(normalized) {
		return nil, false
	}
	return normalized, true
}

func normalizeEchartsScatterPointValue(value any) (any, bool) {
	arr, ok := value.([]any)
	if !ok || len(arr) < 2 {
		return nil, false
	}
	normalized := make([]any, 0, len(arr))
	for _, entry := range arr {
		normalized = append(normalized, coerceEchartsNumericLike(entry))
	}
	if !isFiniteEchartsNumber(normalized[0]) || !isFiniteEchartsNumber(normalized[1]) {
		return nil, false
	}
	return normalized, true
}

func coerceEchartsNumericLike(value any) any {
	switch typed := value.(type) {
	case string:
		if number, ok := coerceToFiniteNumber(typed); ok {
			return number
		}
		return value
	default:
		return value
	}
}

func isFiniteEchartsNumber(value any) bool {
	_, ok := coerceToFiniteNumber(value)
	return ok
}

func normalizeEchartsExtensions(module string, raw any, caps echartsCapabilitiesContext) (map[string]any, error) {
	extensionsRaw, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("echarts_spec.extensions must be an object")
	}
	if len(extensionsRaw) == 0 {
		return map[string]any{}, nil
	}
	for key := range extensionsRaw {
		if key != "drilldown_async" {
			return nil, fmt.Errorf("echarts_spec.extensions.%s is not supported", key)
		}
	}

	drilldownRaw, exists := extensionsRaw["drilldown_async"]
	if !exists {
		return map[string]any{}, nil
	}
	drilldownMap, ok := drilldownRaw.(map[string]any)
	if !ok {
		return nil, errors.New("echarts_spec.extensions.drilldown_async must be an object")
	}
	loaderID := strings.TrimSpace(anyToString(drilldownMap["loader_id"]))
	if loaderID == "" {
		return nil, errors.New("echarts_spec.extensions.drilldown_async.loader_id is required")
	}

	allowed := caps.CallbackIDsByKind["drilldown.loader"]
	if len(allowed) == 0 {
		allowed = fallbackEchartsCallbackIDsByKind["drilldown.loader"]
	}
	if len(allowed) > 0 {
		if _, ok := allowed[loaderID]; !ok {
			return nil, fmt.Errorf("unsupported drilldown_async.loader_id %q", loaderID)
		}
	}

	seriesByIDRaw, ok := drilldownMap["series_by_id"].(map[string]any)
	if !ok {
		return nil, errors.New("echarts_spec.extensions.drilldown_async.series_by_id must be an object")
	}
	if len(seriesByIDRaw) == 0 {
		return nil, errors.New("echarts_spec.extensions.drilldown_async.series_by_id cannot be empty")
	}
	if len(seriesByIDRaw) > 200 {
		return nil, errors.New("echarts_spec.extensions.drilldown_async.series_by_id exceeds max entries (200)")
	}

	normalizedSeriesByID := map[string]any{}
	for rawID, rawSeries := range seriesByIDRaw {
		seriesID := strings.TrimSpace(rawID)
		if seriesID == "" {
			return nil, errors.New("echarts_spec.extensions.drilldown_async.series_by_id contains an empty series id")
		}
		seriesMap, ok := rawSeries.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("echarts_spec.extensions.drilldown_async.series_by_id[%q] must be an object", seriesID)
		}
		if err := rejectExecutableValues(seriesMap, "extensions.drilldown_async.series_by_id."+seriesID); err != nil {
			return nil, err
		}
		normalizedSeries, err := normalizeEchartsSeries(module, []any{seriesMap}, caps)
		if err != nil {
			return nil, fmt.Errorf("echarts_spec.extensions.drilldown_async.series_by_id[%q]: %w", seriesID, err)
		}
		normalizedSeriesByID[seriesID] = normalizedSeries[0]
	}

	maxDepth := 4
	if rawDepth, exists := drilldownMap["max_depth"]; exists {
		depth, ok := anyToInt(rawDepth)
		if !ok {
			return nil, errors.New("echarts_spec.extensions.drilldown_async.max_depth must be an integer")
		}
		if depth < 1 || depth > 8 {
			return nil, errors.New("echarts_spec.extensions.drilldown_async.max_depth must be between 1 and 8")
		}
		maxDepth = depth
	}

	return map[string]any{
		"drilldown_async": map[string]any{
			"loader_id":    loaderID,
			"series_by_id": normalizedSeriesByID,
			"max_depth":    maxDepth,
		},
	}, nil
}

func resolveRequestedEchartsModules(raw any, caps echartsCapabilitiesContext) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("echarts_spec.modules must be an array")
	}
	if len(arr) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(arr))
	seen := map[string]struct{}{}
	for idx, entry := range arr {
		module := strings.ToLower(strings.TrimSpace(anyToString(entry)))
		if module == "" {
			return nil, fmt.Errorf("echarts_spec.modules[%d] must be a non-empty string", idx)
		}
		if _, ok := caps.ExtensionModules[module]; !ok {
			return nil, fmt.Errorf("echarts_spec.modules[%d] %q is not a supported extension module", idx, module)
		}
		if _, exists := seen[module]; exists {
			continue
		}
		seen[module] = struct{}{}
		out = append(out, module)
	}
	sort.Strings(out)
	return out, nil
}
