package tools

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	HighchartsPlotSpecVersionV1 = "highcharts-plot-spec.v1"
	HighchartsPlotSpecVersionV2 = "highcharts-plot-spec.v2"
	// Backward-compatible alias used in tests and legacy references.
	HighchartsPlotSpecVersion = HighchartsPlotSpecVersionV1
	highchartsEngineName      = "highcharts"
)

//go:embed generated/highcharts-capabilities.json
var embeddedHighchartsCapabilities []byte

var (
	fallbackHighchartsModules = map[string]struct{}{
		"core":  {},
		"stock": {},
		"maps":  {},
		"gantt": {},
	}
	allowedHighchartsSeriesByModule = map[string]map[string]struct{}{
		"core": {
			"line": {}, "spline": {}, "area": {}, "areaspline": {},
			"column": {}, "bar": {}, "scatter": {}, "pie": {}, "bubble": {},
			"treemap": {}, "sunburst": {}, "sankey": {}, "organization": {},
			"dependencywheel": {}, "networkgraph": {}, "wordcloud": {}, "venn": {},
			"timeline": {}, "streamgraph": {}, "item": {}, "pareto": {}, "dumbbell": {},
			"lollipop": {}, "funnel": {}, "histogram": {}, "bellcurve": {}, "heatmap": {},
			"tilemap": {}, "xrange": {},
		},
		"stock": {
			"line": {}, "area": {}, "column": {}, "candlestick": {}, "ohlc": {},
			"scatter": {}, "areaspline": {}, "spline": {},
		},
		"maps": {
			"map": {}, "mapline": {}, "mappoint": {}, "mapbubble": {},
			"bubble": {}, "heatmap": {}, "tilemap": {}, "geoheatmap": {}, "flowmap": {},
		},
		"gantt": {
			"gantt": {}, "xrange": {},
		},
	}
	fallbackAllowedOptionRoots = map[string]map[string]struct{}{
		"core": {
			"chart": {}, "title": {}, "subtitle": {}, "caption": {},
			"xAxis": {}, "yAxis": {}, "zAxis": {}, "colorAxis": {},
			"legend": {}, "tooltip": {}, "plotOptions": {}, "series": {},
			"drilldown": {}, "rangeSelector": {}, "navigator": {}, "scrollbar": {},
			"mapView": {}, "mapNavigation": {}, "navigation": {}, "pane": {},
			"responsive": {}, "credits": {}, "time": {}, "annotations": {},
			"data": {}, "accessibility": {}, "boost": {},
		},
		"stock": {
			"chart": {}, "title": {}, "subtitle": {}, "caption": {},
			"xAxis": {}, "yAxis": {}, "zAxis": {}, "colorAxis": {},
			"legend": {}, "tooltip": {}, "plotOptions": {}, "series": {},
			"drilldown": {}, "rangeSelector": {}, "navigator": {}, "scrollbar": {},
			"navigation": {}, "pane": {}, "responsive": {}, "credits": {},
			"time": {}, "annotations": {}, "data": {}, "accessibility": {}, "boost": {},
		},
		"maps": {
			"chart": {}, "title": {}, "subtitle": {}, "caption": {},
			"xAxis": {}, "yAxis": {}, "zAxis": {}, "colorAxis": {},
			"legend": {}, "tooltip": {}, "plotOptions": {}, "series": {},
			"drilldown": {}, "mapView": {}, "mapNavigation": {},
			"navigation": {}, "responsive": {}, "credits": {}, "time": {},
			"annotations": {}, "data": {}, "accessibility": {}, "boost": {},
		},
		"gantt": {
			"chart": {}, "title": {}, "subtitle": {}, "caption": {},
			"xAxis": {}, "yAxis": {}, "zAxis": {}, "colorAxis": {},
			"legend": {}, "tooltip": {}, "plotOptions": {}, "series": {},
			"drilldown": {}, "navigation": {}, "responsive": {}, "credits": {},
			"time": {}, "annotations": {}, "data": {}, "accessibility": {}, "boost": {},
		},
	}
	fallbackCallbackIDsByKind = map[string]map[string]struct{}{
		"tooltip.formatter": {
			"tooltip_number_compact": {},
			"tooltip_currency_usd":   {},
			"tooltip_percent_1dp":    {},
			"tooltip_datetime_short": {},
		},
		"drilldown.loader": {
			"drilldown_embedded_series": {},
		},
	}
	fallbackExtensionModules = map[string][]string{
		"highcharts-more":     {},
		"drilldown":           {},
		"heatmap":             {},
		"tilemap":             {"heatmap"},
		"treemap":             {},
		"sunburst":            {"treemap"},
		"sankey":              {},
		"organization":        {"sankey"},
		"dependency-wheel":    {"sankey"},
		"networkgraph":        {},
		"wordcloud":           {},
		"venn":                {},
		"timeline":            {},
		"xrange":              {},
		"streamgraph":         {"highcharts-more"},
		"item-series":         {},
		"pareto":              {"histogram-bellcurve"},
		"dumbbell":            {"highcharts-more"},
		"lollipop":            {"dumbbell"},
		"funnel":              {},
		"histogram-bellcurve": {},
		"geoheatmap":          {"heatmap"},
		"flowmap":             {},
		"tiledwebmap":         {},
	}
)

type normalizedChartSpec struct {
	PlotSpec          map[string]any
	CapabilityVersion string
	Module            string
	Modules           []string
}

type highchartsCapabilitiesContext struct {
	SupportedSchemaVersions map[string]struct{}
	SupportedModules        map[string]struct{}
	SupportedSeriesByModule map[string]map[string]struct{}
	AllowedOptionRoots      map[string]map[string]struct{}
	CallbackIDsByKind       map[string]map[string]struct{}
	ExtensionModuleDeps     map[string][]string
}

func loadHighchartsCapabilitiesSnapshot() (map[string]any, error) {
	raw := strings.TrimSpace(string(embeddedHighchartsCapabilities))
	if raw == "" {
		return nil, errors.New("embedded highcharts capabilities snapshot is empty")
	}
	var snapshot map[string]any
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return nil, fmt.Errorf("parse highcharts capabilities snapshot: %w", err)
	}
	return snapshot, nil
}

func highchartsCapabilityVersion(snapshot map[string]any) string {
	if snapshot == nil {
		return ""
	}
	v, _ := snapshot["capability_version"].(string)
	return strings.TrimSpace(v)
}

func buildHighchartsCapabilitiesContext(snapshot map[string]any) highchartsCapabilitiesContext {
	ctx := highchartsCapabilitiesContext{
		SupportedSchemaVersions: map[string]struct{}{
			HighchartsPlotSpecVersionV1: {},
			HighchartsPlotSpecVersionV2: {},
		},
		SupportedModules:        cloneSetMap(fallbackHighchartsModules),
		SupportedSeriesByModule: cloneNestedSetMap(allowedHighchartsSeriesByModule),
		AllowedOptionRoots:      cloneNestedSetMap(fallbackAllowedOptionRoots),
		CallbackIDsByKind:       cloneNestedSetMap(fallbackCallbackIDsByKind),
		ExtensionModuleDeps:     cloneStringSliceMap(fallbackExtensionModules),
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

	if constructors, ok := snapshot["constructors"].(map[string]any); ok && len(constructors) > 0 {
		ctx.SupportedModules = map[string]struct{}{}
		for key := range constructors {
			module := strings.ToLower(strings.TrimSpace(key))
			if module == "" {
				continue
			}
			ctx.SupportedModules[module] = struct{}{}
		}
	}
	if supportedModules := toStringSlice(snapshot["supported_modules"]); len(supportedModules) > 0 {
		for _, module := range supportedModules {
			ctx.SupportedModules[module] = struct{}{}
		}
	}

	if rawMap, ok := snapshot["supported_series_types_by_constructor"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for key, value := range rawMap {
			module := strings.ToLower(strings.TrimSpace(key))
			if module == "" {
				continue
			}
			types := toStringSlice(value)
			if len(types) == 0 {
				continue
			}
			parsed[module] = map[string]struct{}{}
			for _, seriesType := range types {
				parsed[module][seriesType] = struct{}{}
			}
		}
		if len(parsed) > 0 {
			ctx.SupportedSeriesByModule = parsed
		}
	} else if rawMap, ok := snapshot["supported_series_types"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for key, value := range rawMap {
			module := strings.ToLower(strings.TrimSpace(key))
			if module == "" {
				continue
			}
			types := toStringSlice(value)
			if len(types) == 0 {
				continue
			}
			parsed[module] = map[string]struct{}{}
			for _, seriesType := range types {
				parsed[module][seriesType] = struct{}{}
			}
		}
		if len(parsed) > 0 {
			ctx.SupportedSeriesByModule = parsed
		}
	}

	if rawMap, ok := snapshot["allowed_option_paths_by_constructor"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for key, value := range rawMap {
			module := strings.ToLower(strings.TrimSpace(key))
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
	} else {
		if paths := toStringSlicePreserveCase(snapshot["allowed_option_paths"]); len(paths) > 0 {
			roots := optionRootsFromPaths(paths)
			if len(roots) > 0 {
				for module := range ctx.SupportedModules {
					ctx.AllowedOptionRoots[module] = cloneSet(roots)
				}
			}
		}
	}

	if rawMap, ok := snapshot["callback_ids_by_kind"].(map[string]any); ok {
		parsed := map[string]map[string]struct{}{}
		for key, value := range rawMap {
			kind := strings.TrimSpace(key)
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
	} else {
		if callbacks := toStringSlice(snapshot["callback_ids"]); len(callbacks) > 0 {
			ids := map[string]struct{}{}
			for _, id := range callbacks {
				ids[id] = struct{}{}
			}
			ctx.CallbackIDsByKind["tooltip.formatter"] = ids
		}
	}

	if rawCatalog, ok := snapshot["module_catalog"].(map[string]any); ok {
		catalog := map[string][]string{}
		for moduleName, raw := range rawCatalog {
			module := strings.ToLower(strings.TrimSpace(moduleName))
			if module == "" {
				continue
			}
			entry, _ := raw.(map[string]any)
			catalog[module] = toStringSlice(entry["depends_on"])
		}
		if len(catalog) > 0 {
			ctx.ExtensionModuleDeps = catalog
		}
	}

	for module := range ctx.SupportedModules {
		if _, ok := ctx.SupportedSeriesByModule[module]; !ok {
			if fallback, hasFallback := allowedHighchartsSeriesByModule[module]; hasFallback {
				ctx.SupportedSeriesByModule[module] = cloneSet(fallback)
			}
		}
		if _, ok := ctx.AllowedOptionRoots[module]; !ok {
			if fallback, hasFallback := fallbackAllowedOptionRoots[module]; hasFallback {
				ctx.AllowedOptionRoots[module] = cloneSet(fallback)
			}
		}
	}

	return ctx
}

func normalizeChartSpecInput(rawSpec any, chartSpecVersion string) (*normalizedChartSpec, error) {
	if rawSpec == nil {
		return nil, nil
	}
	snapshot, err := loadHighchartsCapabilitiesSnapshot()
	if err != nil {
		return nil, err
	}
	capabilityVersion := highchartsCapabilityVersion(snapshot)
	if capabilityVersion == "" {
		return nil, errors.New("highcharts capability_version is missing")
	}
	if strings.TrimSpace(chartSpecVersion) != "" && strings.TrimSpace(chartSpecVersion) != capabilityVersion {
		return nil, fmt.Errorf("chart_spec_version mismatch: expected %q", capabilityVersion)
	}

	caps := buildHighchartsCapabilitiesContext(snapshot)
	normalized, module, modules, err := normalizeHighchartsPlotSpec(rawSpec, caps)
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

func allowedHighchartsCallbackIDs() []string {
	ids := make([]string, 0, len(fallbackCallbackIDsByKind["tooltip.formatter"]))
	for id := range fallbackCallbackIDsByKind["tooltip.formatter"] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func normalizeHighchartsPlotSpec(rawSpec any, caps highchartsCapabilitiesContext) (map[string]any, string, []string, error) {
	spec, ok := rawSpec.(map[string]any)
	if !ok {
		return nil, "", nil, errors.New("chart_spec must be an object")
	}

	schemaVersion := strings.TrimSpace(anyToString(spec["schema_version"]))
	if schemaVersion == "" {
		return nil, "", nil, errors.New("chart_spec.schema_version is required")
	}
	if _, ok := caps.SupportedSchemaVersions[schemaVersion]; !ok {
		return nil, "", nil, fmt.Errorf("chart_spec.schema_version %q is not supported", schemaVersion)
	}

	module := strings.ToLower(strings.TrimSpace(anyToString(spec["module"])))
	if _, ok := caps.SupportedModules[module]; !ok {
		allowed := make([]string, 0, len(caps.SupportedModules))
		for key := range caps.SupportedModules {
			allowed = append(allowed, key)
		}
		sort.Strings(allowed)
		return nil, "", nil, fmt.Errorf("chart_spec.module %q is not supported (allowed: %s)", module, strings.Join(allowed, ", "))
	}

	options, ok := spec["options"].(map[string]any)
	if !ok {
		return nil, "", nil, errors.New("chart_spec.options must be an object")
	}
	if err := rejectExecutableValues(options, "options"); err != nil {
		return nil, "", nil, err
	}

	resolvedModules, err := resolveRequestedModules(spec["modules"], caps)
	if err != nil {
		return nil, "", nil, err
	}

	normalizedOptions, err := normalizeHighchartsOptions(module, options, caps)
	if err != nil {
		return nil, "", nil, err
	}
	if module == "maps" {
		if err := validateInlineMapDataOnly(normalizedOptions); err != nil {
			return nil, "", nil, err
		}
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
		normalizedExtensions, err := normalizeHighchartsExtensions(module, extRaw, caps)
		if err != nil {
			return nil, "", nil, err
		}
		if len(normalizedExtensions) > 0 {
			normalized["extensions"] = normalizedExtensions
		}
	}

	return normalized, module, resolvedModules, nil
}

func normalizeHighchartsOptions(module string, options map[string]any, caps highchartsCapabilitiesContext) (map[string]any, error) {
	allowedRoots := caps.AllowedOptionRoots[module]
	if len(allowedRoots) == 0 {
		allowedRoots = fallbackAllowedOptionRoots[module]
	}
	if len(allowedRoots) == 0 {
		allowedRoots = fallbackAllowedOptionRoots["core"]
	}

	out := map[string]any{}
	for key, value := range options {
		if _, ok := allowedRoots[key]; !ok {
			return nil, fmt.Errorf("unsupported options key %q for module %q", key, module)
		}
		switch key {
		case "series":
			series, err := normalizeHighchartsSeries(module, value, caps)
			if err != nil {
				return nil, err
			}
			out["series"] = series
		case "tooltip":
			tooltip, err := normalizeHighchartsTooltip(value, caps)
			if err != nil {
				return nil, err
			}
			if len(tooltip) > 0 {
				out["tooltip"] = tooltip
			}
		case "xAxis", "yAxis":
			axis, err := normalizeHighchartsAxis(value)
			if err != nil {
				return nil, err
			}
			if axis != nil {
				out[key] = axis
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
		return nil, errors.New("chart_spec.options.series is required")
	}
	return out, nil
}

func normalizeHighchartsTooltip(raw any, caps highchartsCapabilitiesContext) (map[string]any, error) {
	tooltipRaw, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("chart_spec.options.tooltip must be an object")
	}
	out := map[string]any{}
	if shared, ok := tooltipRaw["shared"].(bool); ok {
		out["shared"] = shared
	}
	if outside, ok := tooltipRaw["outside"].(bool); ok {
		out["outside"] = outside
	}
	// Enforce HTML safety by default.
	out["useHTML"] = false
	if id := strings.TrimSpace(anyToString(tooltipRaw["formatter_id"])); id != "" {
		allowed := caps.CallbackIDsByKind["tooltip.formatter"]
		if len(allowed) == 0 {
			allowed = fallbackCallbackIDsByKind["tooltip.formatter"]
		}
		if _, ok := allowed[id]; !ok {
			return nil, fmt.Errorf("unsupported tooltip.formatter_id %q", id)
		}
		out["formatter_id"] = id
	}
	if pointFormat := strings.TrimSpace(anyToString(tooltipRaw["pointFormat"])); pointFormat != "" {
		out["pointFormat"] = pointFormat
	}
	if suffix := strings.TrimSpace(anyToString(tooltipRaw["valueSuffix"])); suffix != "" {
		out["valueSuffix"] = suffix
	}
	if _, exists := tooltipRaw["formatter"]; exists {
		return nil, errors.New("chart_spec.options.tooltip.formatter is not allowed; use formatter_id")
	}
	return out, nil
}

func normalizeHighchartsAxis(raw any) (any, error) {
	switch typed := raw.(type) {
	case map[string]any:
		out := map[string]any{}
		if axisType := strings.TrimSpace(anyToString(typed["type"])); axisType != "" {
			switch axisType {
			case "linear", "logarithmic", "datetime", "category", "treegrid":
				out["type"] = axisType
			default:
				return nil, fmt.Errorf("unsupported axis.type %q", axisType)
			}
		}
		if categories, ok := sanitizeArrayOfScalars(typed["categories"]); ok {
			out["categories"] = categories
		}
		if min, ok := anyToFloat64(typed["min"]); ok {
			out["min"] = min
		}
		if max, ok := anyToFloat64(typed["max"]); ok {
			out["max"] = max
		}
		if opposite, ok := typed["opposite"].(bool); ok {
			out["opposite"] = opposite
		}
		if titleRaw, ok := typed["title"].(map[string]any); ok {
			if text := strings.TrimSpace(anyToString(titleRaw["text"])); text != "" {
				out["title"] = map[string]any{"text": text}
			}
		}
		return out, nil
	case []any:
		arr := make([]any, 0, len(typed))
		for _, entry := range typed {
			normalized, err := normalizeHighchartsAxis(entry)
			if err != nil {
				return nil, err
			}
			if normalized != nil {
				arr = append(arr, normalized)
			}
		}
		return arr, nil
	default:
		return nil, errors.New("axis must be an object or array")
	}
}

func normalizeHighchartsSeries(module string, raw any, caps highchartsCapabilitiesContext) ([]any, error) {
	seriesRaw, ok := raw.([]any)
	if !ok {
		return nil, errors.New("chart_spec.options.series must be an array")
	}
	if len(seriesRaw) == 0 {
		return nil, errors.New("chart_spec.options.series cannot be empty")
	}
	allowedTypes := caps.SupportedSeriesByModule[module]
	if len(allowedTypes) == 0 {
		allowedTypes = allowedHighchartsSeriesByModule[module]
	}
	if len(allowedTypes) == 0 {
		allowedTypes = allowedHighchartsSeriesByModule["core"]
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
		for key, value := range seriesMap {
			switch key {
			case "type":
				continue
			case "data":
				dataRaw, ok := value.([]any)
				if !ok {
					return nil, fmt.Errorf("series[%d].data must be an array", i)
				}
				data, err := sanitizeSeriesData(dataRaw)
				if err != nil {
					return nil, fmt.Errorf("series[%d].data: %w", i, err)
				}
				normalized["data"] = data
				hasData = true
			case "mapData":
				mapData, err := sanitizeMapData(value)
				if err != nil {
					return nil, fmt.Errorf("series[%d].mapData: %w", i, err)
				}
				normalized["mapData"] = mapData
			default:
				sanitized, ok := sanitizeJSONValue(value)
				if !ok {
					return nil, fmt.Errorf("series[%d].%s contains unsupported value", i, key)
				}
				normalized[key] = sanitized
			}
		}
		if !hasData {
			if _, ok := normalized["mapData"]; !ok {
				return nil, fmt.Errorf("series[%d].data is required", i)
			}
		}
		out = append(out, normalized)
	}
	return out, nil
}

func sanitizeMapData(value any) (any, error) {
	switch value.(type) {
	case string:
		return nil, errors.New("external map URLs or map key strings are not allowed; embed GeoJSON/TopoJSON inline")
	}
	sanitized, ok := sanitizeJSONValue(value)
	if !ok {
		return nil, errors.New("unsupported mapData payload")
	}
	if containsExternalURL(sanitized) {
		return nil, errors.New("external map URLs are not allowed")
	}
	return sanitized, nil
}

func validateInlineMapDataOnly(options map[string]any) error {
	if containsExternalURL(options) {
		return errors.New("maps module requires inline map data; external URLs are not allowed")
	}
	if chartRaw, ok := options["chart"].(map[string]any); ok {
		if _, hasMap := chartRaw["map"]; hasMap {
			return errors.New("options.chart.map string references are not allowed; embed map data in series.mapData")
		}
	}
	return nil
}

func normalizeHighchartsExtensions(module string, raw any, caps highchartsCapabilitiesContext) (map[string]any, error) {
	extensionsRaw, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("chart_spec.extensions must be an object")
	}
	if len(extensionsRaw) == 0 {
		return map[string]any{}, nil
	}

	out := map[string]any{}
	for key := range extensionsRaw {
		if key != "drilldown_async" {
			return nil, fmt.Errorf("chart_spec.extensions.%s is not supported", key)
		}
	}

	drilldownRaw, exists := extensionsRaw["drilldown_async"]
	if !exists {
		return out, nil
	}

	drilldownMap, ok := drilldownRaw.(map[string]any)
	if !ok {
		return nil, errors.New("chart_spec.extensions.drilldown_async must be an object")
	}
	loaderID := strings.TrimSpace(anyToString(drilldownMap["loader_id"]))
	if loaderID == "" {
		return nil, errors.New("chart_spec.extensions.drilldown_async.loader_id is required")
	}

	allowed := caps.CallbackIDsByKind["drilldown.loader"]
	if len(allowed) > 0 {
		if _, ok := allowed[loaderID]; !ok {
			return nil, fmt.Errorf("unsupported drilldown_async.loader_id %q", loaderID)
		}
	}

	seriesByIDRaw, ok := drilldownMap["series_by_id"].(map[string]any)
	if !ok {
		return nil, errors.New("chart_spec.extensions.drilldown_async.series_by_id must be an object")
	}
	if len(seriesByIDRaw) == 0 {
		return nil, errors.New("chart_spec.extensions.drilldown_async.series_by_id cannot be empty")
	}
	if len(seriesByIDRaw) > 200 {
		return nil, errors.New("chart_spec.extensions.drilldown_async.series_by_id exceeds max entries (200)")
	}

	normalizedSeriesByID := map[string]any{}
	validationModule := module
	if module == "maps" || module == "gantt" {
		validationModule = "core"
	}

	for rawID, rawSeries := range seriesByIDRaw {
		seriesID := strings.TrimSpace(rawID)
		if seriesID == "" {
			return nil, errors.New("chart_spec.extensions.drilldown_async.series_by_id contains an empty series id")
		}
		seriesMap, ok := rawSeries.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("chart_spec.extensions.drilldown_async.series_by_id[%q] must be an object", seriesID)
		}
		if err := rejectExecutableValues(seriesMap, "extensions.drilldown_async.series_by_id."+seriesID); err != nil {
			return nil, err
		}
		normalizedSeries, err := normalizeHighchartsSeries(validationModule, []any{seriesMap}, caps)
		if err != nil {
			return nil, fmt.Errorf("chart_spec.extensions.drilldown_async.series_by_id[%q]: %w", seriesID, err)
		}
		normalizedSeriesByID[seriesID] = normalizedSeries[0]
	}

	maxDepth := 4
	if rawDepth, exists := drilldownMap["max_depth"]; exists {
		depth, ok := anyToInt(rawDepth)
		if !ok {
			return nil, errors.New("chart_spec.extensions.drilldown_async.max_depth must be an integer")
		}
		if depth < 1 || depth > 8 {
			return nil, errors.New("chart_spec.extensions.drilldown_async.max_depth must be between 1 and 8")
		}
		maxDepth = depth
	}

	out["drilldown_async"] = map[string]any{
		"loader_id":    loaderID,
		"series_by_id": normalizedSeriesByID,
		"max_depth":    maxDepth,
	}
	return out, nil
}

func resolveRequestedModules(raw any, caps highchartsCapabilitiesContext) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil, errors.New("chart_spec.modules must be an array")
	}
	if len(arr) == 0 {
		return nil, nil
	}

	requested := make([]string, 0, len(arr))
	seenRequested := map[string]struct{}{}
	for idx, entry := range arr {
		module := strings.ToLower(strings.TrimSpace(anyToString(entry)))
		if module == "" {
			return nil, fmt.Errorf("chart_spec.modules[%d] must be a non-empty string", idx)
		}
		if _, ok := caps.ExtensionModuleDeps[module]; !ok {
			return nil, fmt.Errorf("chart_spec.modules[%d] %q is not a supported extension module", idx, module)
		}
		if _, exists := seenRequested[module]; exists {
			continue
		}
		seenRequested[module] = struct{}{}
		requested = append(requested, module)
	}

	resolved := make([]string, 0, len(requested))
	seenResolved := map[string]struct{}{}
	visiting := map[string]struct{}{}
	var visit func(module string) error
	visit = func(module string) error {
		if _, done := seenResolved[module]; done {
			return nil
		}
		if _, cycle := visiting[module]; cycle {
			return fmt.Errorf("cyclic extension module dependency detected at %q", module)
		}
		visiting[module] = struct{}{}
		for _, dep := range caps.ExtensionModuleDeps[module] {
			dep = strings.ToLower(strings.TrimSpace(dep))
			if dep == "" {
				continue
			}
			if _, ok := caps.ExtensionModuleDeps[dep]; !ok {
				return fmt.Errorf("extension module %q depends on unsupported module %q", module, dep)
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		delete(visiting, module)
		seenResolved[module] = struct{}{}
		resolved = append(resolved, module)
		return nil
	}

	for _, module := range requested {
		if err := visit(module); err != nil {
			return nil, err
		}
	}

	return resolved, nil
}

func sanitizeSeriesData(values []any) ([]any, error) {
	out := make([]any, 0, len(values))
	for _, value := range values {
		sanitized, ok := sanitizeSeriesPoint(value)
		if !ok {
			return nil, errors.New("unsupported data point value")
		}
		out = append(out, sanitized)
	}
	return out, nil
}

func sanitizeSeriesPoint(value any) (any, bool) {
	switch typed := value.(type) {
	case nil, bool, string:
		if containsExternalURL(typed) {
			return nil, false
		}
		return typed, true
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
	case []any:
		if len(typed) == 0 || len(typed) > 16 {
			return nil, false
		}
		arr := make([]any, 0, len(typed))
		for _, entry := range typed {
			sanitized, ok := sanitizeSeriesPoint(entry)
			if !ok {
				return nil, false
			}
			arr = append(arr, sanitized)
		}
		return arr, true
	case map[string]any:
		point := map[string]any{}
		for key, raw := range typed {
			lower := strings.ToLower(strings.TrimSpace(key))
			if lower == "__proto__" || lower == "constructor" || lower == "prototype" {
				return nil, false
			}
			if lower == "url" || lower == "href" {
				if containsExternalURL(raw) {
					return nil, false
				}
			}
			sanitized, ok := sanitizeSeriesPoint(raw)
			if !ok {
				return nil, false
			}
			point[key] = sanitized
		}
		return point, true
	default:
		return nil, false
	}
}

func rejectExecutableValues(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(strings.TrimSpace(key))
			if lower == "__proto__" || lower == "constructor" || lower == "prototype" {
				return fmt.Errorf("unsupported key at %s.%s", path, key)
			}
			if lower == "formatter" || lower == "events" {
				return fmt.Errorf("executable option at %s.%s is not allowed", path, key)
			}
			if err := rejectExecutableValues(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for idx, child := range typed {
			if err := rejectExecutableValues(child, fmt.Sprintf("%s[%d]", path, idx)); err != nil {
				return err
			}
		}
	case string:
		if looksLikeExecutableString(typed) {
			return fmt.Errorf("executable string value is not allowed at %s", path)
		}
	}
	return nil
}

func looksLikeExecutableString(value string) bool {
	text := strings.ToLower(strings.TrimSpace(value))
	if text == "" {
		return false
	}
	if strings.Contains(text, "<script") {
		return true
	}
	if strings.Contains(text, "function(") || strings.Contains(text, "=>") {
		return true
	}
	return false
}

func containsExternalURL(value any) bool {
	switch typed := value.(type) {
	case string:
		text := strings.ToLower(strings.TrimSpace(typed))
		if text == "" {
			return false
		}
		if strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") || strings.HasPrefix(text, "//") {
			return true
		}
		return false
	case []any:
		for _, child := range typed {
			if containsExternalURL(child) {
				return true
			}
		}
		return false
	case map[string]any:
		for _, child := range typed {
			if containsExternalURL(child) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func sanitizeJSONValue(value any) (any, bool) {
	switch typed := value.(type) {
	case nil, bool, string, float64:
		return typed, true
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
	case []any:
		arr := make([]any, 0, len(typed))
		for _, entry := range typed {
			sanitized, ok := sanitizeJSONValue(entry)
			if !ok {
				return nil, false
			}
			arr = append(arr, sanitized)
		}
		return arr, true
	case map[string]any:
		obj := map[string]any{}
		for key, raw := range typed {
			sanitized, ok := sanitizeJSONValue(raw)
			if !ok {
				continue
			}
			obj[key] = sanitized
		}
		return obj, true
	default:
		return nil, false
	}
}

func sanitizeArrayOfScalars(raw any) ([]any, bool) {
	arr, ok := raw.([]any)
	if !ok {
		return nil, false
	}
	out := make([]any, 0, len(arr))
	for _, entry := range arr {
		switch typed := entry.(type) {
		case string, bool, nil:
			out = append(out, typed)
		case int:
			out = append(out, float64(typed))
		case int8:
			out = append(out, float64(typed))
		case int16:
			out = append(out, float64(typed))
		case int32:
			out = append(out, float64(typed))
		case int64:
			out = append(out, float64(typed))
		case uint:
			out = append(out, float64(typed))
		case uint8:
			out = append(out, float64(typed))
		case uint16:
			out = append(out, float64(typed))
		case uint32:
			out = append(out, float64(typed))
		case uint64:
			out = append(out, float64(typed))
		case float32:
			out = append(out, float64(typed))
		case float64:
			out = append(out, typed)
		default:
			return nil, false
		}
	}
	return out, true
}

func toAnySlice(values []string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func toStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if s := strings.ToLower(strings.TrimSpace(entry)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		seen := map[string]struct{}{}
		for _, entry := range typed {
			s := strings.ToLower(strings.TrimSpace(anyToString(entry)))
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

func toStringSlicePreserveCase(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		seen := map[string]struct{}{}
		for _, entry := range typed {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			if _, ok := seen[entry]; ok {
				continue
			}
			seen[entry] = struct{}{}
			out = append(out, entry)
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		seen := map[string]struct{}{}
		for _, entry := range typed {
			s := strings.TrimSpace(anyToString(entry))
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

func optionRootsFromPaths(paths []string) map[string]struct{} {
	roots := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		root := path
		if dot := strings.Index(root, "."); dot >= 0 {
			root = root[:dot]
		}
		if bracket := strings.Index(root, "["); bracket >= 0 {
			root = root[:bracket]
		}
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		roots[root] = struct{}{}
	}
	return roots
}

func cloneSetMap(in map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func cloneSet(in map[string]struct{}) map[string]struct{} {
	if in == nil {
		return nil
	}
	out := map[string]struct{}{}
	for key := range in {
		out[key] = struct{}{}
	}
	return out
}

func cloneNestedSetMap(in map[string]map[string]struct{}) map[string]map[string]struct{} {
	out := map[string]map[string]struct{}{}
	for key, entry := range in {
		out[key] = cloneSet(entry)
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for key, entry := range in {
		if len(entry) == 0 {
			out[key] = nil
			continue
		}
		copied := make([]string, 0, len(entry))
		for _, dep := range entry {
			dep = strings.ToLower(strings.TrimSpace(dep))
			if dep == "" {
				continue
			}
			copied = append(copied, dep)
		}
		out[key] = copied
	}
	return out
}

func anyToString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func anyToFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
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
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

func anyToInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
