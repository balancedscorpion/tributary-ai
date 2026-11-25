package httpapi

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"ai-analyst-local/internal/chat/tools"
)

type filterChartRequest struct {
	Filters   []filterChartParam `json:"filters"`
	Chart     *chartRebuildInput `json:"chart,omitempty"`
	MaxPoints int                `json:"max_points,omitempty"`
	Offset    int                `json:"offset,omitempty"`
	Limit     int                `json:"limit,omitempty"`
}

type filterChartParam struct {
	Column   string   `json:"column"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
	Value    string   `json:"value,omitempty"`
}

type chartRebuildInput struct {
	Kind   string `json:"kind,omitempty"`   // scatter, line, bar
	Engine string `json:"engine,omitempty"` // echarts, highcharts
	X      string `json:"x,omitempty"`
	Y      string `json:"y,omitempty"`
	Color  string `json:"color,omitempty"`
	Size   string `json:"size,omitempty"`
}

func (h *Handler) handleFilterChart(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing artifactId"})
		return
	}

	var req filterChartRequest
	if err := decodeJSONLimit(w, r, &req, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if err := validateFilterChartParams(req.Filters); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	a, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	raw, err := h.readArtifactContentWithLimit(a, maxArtifactRowsReadBytes)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	rows, err := parseDatasetRows(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if len(req.Filters) > 0 {
		toolFilters := make([]tools.ChartArtifactFilterInput, 0, len(req.Filters))
		for _, f := range req.Filters {
			toolFilters = append(toolFilters, tools.ChartArtifactFilterInput{
				Column:   strings.TrimSpace(f.Column),
				Operator: strings.TrimSpace(f.Operator),
				Values:   append([]string(nil), f.Values...),
				Value:    strings.TrimSpace(f.Value),
			})
		}
		rows, err = tools.ApplyChartFilters(rows, toolFilters)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}

	totalAfterFilter := len(rows)

	rebuildInput, rebuildProvided, rebuildErr := resolveChartRebuildInput(req.Chart, a.Metadata, rows)
	if rebuildErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": rebuildErr.Error()})
		return
	}

	resolvedPlotSpec := any(nil)
	resolvedEngine := ""
	resolvedSpecVersion := ""
	resolvedModule := ""
	resolvedModules := []string(nil)

	if rebuildProvided {
		title := strings.TrimSpace(a.Title)
		if title == "" {
			title = "Filtered Chart"
		}
		rebuilt, rebuildRunErr := tools.BuildChartPlotSpec(rows, rebuildInput, title)
		if rebuildRunErr != nil {
			if req.Chart != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": rebuildRunErr.Error()})
				return
			}
		} else {
			resolvedPlotSpec = rebuilt.PlotSpec
			resolvedEngine = rebuilt.Engine
			resolvedSpecVersion = rebuilt.CapabilityVersion
			resolvedModule = rebuilt.Module
			resolvedModules = append([]string(nil), rebuilt.Modules...)
		}
	}

	if a.Metadata != nil {
		if resolvedEngine == "" {
			resolvedEngine = strings.TrimSpace(anyToString(a.Metadata["chart_engine"]))
		}
		if resolvedSpecVersion == "" {
			resolvedSpecVersion = strings.TrimSpace(anyToString(a.Metadata["chart_spec_version"]))
		}
		if resolvedModule == "" {
			resolvedModule = strings.TrimSpace(anyToString(a.Metadata["chart_module"]))
		}
		if len(resolvedModules) == 0 {
			resolvedModules = stringSliceFromAny(a.Metadata["chart_modules"])
		}
		if resolvedPlotSpec == nil {
			if original, ok := a.Metadata["plot_spec"]; ok && original != nil {
				if rebuilt, ok := rebuildPlotSpecFallback(original, rows); ok {
					resolvedPlotSpec = rebuilt
				} else {
					resolvedPlotSpec = original
				}
			}
		}
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultArtifactRowsLimit
	}
	if limit > maxArtifactRowsLimit {
		limit = maxArtifactRowsLimit
	}
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(rows) {
		offset = len(rows)
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	selected := rows[offset:end]
	selected = clampRowsByResponseBytes(selected, maxArtifactRowsReplyBytes)

	resp := map[string]any{
		"artifact_id":     artifactID,
		"rows":            selected,
		"offset":          offset,
		"limit":           limit,
		"rows_returned":   len(selected),
		"row_count_total": totalAfterFilter,
		"has_more":        end < totalAfterFilter,
		"filters_applied": len(req.Filters),
	}
	if resolvedPlotSpec != nil {
		resp["plot_spec"] = resolvedPlotSpec
	}
	if resolvedEngine != "" {
		resp["chart_engine"] = resolvedEngine
	}
	if resolvedSpecVersion != "" {
		resp["chart_spec_version"] = resolvedSpecVersion
	}
	if resolvedModule != "" {
		resp["chart_module"] = resolvedModule
	}
	if len(resolvedModules) > 0 {
		resp["chart_modules"] = resolvedModules
	}
	if a.Metadata != nil {
		if chartBudget, ok := a.Metadata["chart_budget"]; ok && chartBudget != nil {
			resp["chart_budget"] = chartBudget
		}
		if chartRebuild, ok := a.Metadata["chart_rebuild"]; ok && chartRebuild != nil {
			resp["chart_rebuild"] = chartRebuild
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func validateFilterChartParams(filters []filterChartParam) error {
	if len(filters) == 0 {
		return nil
	}
	validOps := map[string]bool{
		"eq": true, "neq": true, "gt": true, "lt": true,
		"gte": true, "lte": true, "contains": true,
		"starts_with": true, "ends_with": true,
		"is_null": true, "not_null": true,
	}
	for _, f := range filters {
		if strings.TrimSpace(f.Column) == "" {
			return errString("filter column is required")
		}
		operator := strings.TrimSpace(strings.ToLower(f.Operator))
		if !validOps[operator] {
			return errString("invalid filter operator: " + strings.TrimSpace(f.Operator))
		}
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

func resolveChartRebuildInput(
	reqChart *chartRebuildInput,
	metadata map[string]any,
	rows []map[string]any,
) (tools.ChartRebuildInput, bool, error) {
	if reqChart != nil {
		out := tools.ChartRebuildInput{
			Kind:   strings.TrimSpace(reqChart.Kind),
			Engine: strings.TrimSpace(reqChart.Engine),
			X:      strings.TrimSpace(reqChart.X),
			Y:      strings.TrimSpace(reqChart.Y),
			Color:  strings.TrimSpace(reqChart.Color),
			Size:   strings.TrimSpace(reqChart.Size),
		}
		if out.X == "" || out.Y == "" {
			return tools.ChartRebuildInput{}, false, errString("chart.x and chart.y are required when chart is provided")
		}
		if out.Kind == "" {
			out.Kind = "scatter"
		}
		if out.Engine == "" {
			out.Engine = inferEngineFromMetadataOrRows(metadata)
		}
		return out, true, nil
	}

	if metadata == nil {
		return tools.ChartRebuildInput{}, false, nil
	}
	if rawRebuild, ok := metadata["chart_rebuild"].(map[string]any); ok {
		out := tools.ChartRebuildInput{
			Kind:   strings.TrimSpace(anyToString(rawRebuild["kind"])),
			Engine: strings.TrimSpace(anyToString(rawRebuild["engine"])),
			X:      strings.TrimSpace(anyToString(rawRebuild["x"])),
			Y:      strings.TrimSpace(anyToString(rawRebuild["y"])),
			Color:  strings.TrimSpace(anyToString(rawRebuild["color"])),
			Size:   strings.TrimSpace(anyToString(rawRebuild["size"])),
		}
		if out.X != "" && out.Y != "" {
			if out.Kind == "" {
				out.Kind = "scatter"
			}
			if out.Engine == "" {
				out.Engine = inferEngineFromMetadataOrRows(metadata)
			}
			return out, true, nil
		}
	}

	if inferred, ok := inferChartInputFromPlotSpec(metadata["plot_spec"], rows, metadata); ok {
		return inferred, true, nil
	}
	return tools.ChartRebuildInput{}, false, nil
}

func inferChartInputFromPlotSpec(plotSpec any, rows []map[string]any, metadata map[string]any) (tools.ChartRebuildInput, bool) {
	if len(rows) == 0 {
		return tools.ChartRebuildInput{}, false
	}
	root, ok := plotSpec.(map[string]any)
	if !ok || len(root) == 0 {
		return tools.ChartRebuildInput{}, false
	}
	options, ok := root["options"].(map[string]any)
	if !ok || len(options) == 0 {
		return tools.ChartRebuildInput{}, false
	}
	series := objectSliceFromAny(options["series"])
	if len(series) == 0 {
		return tools.ChartRebuildInput{}, false
	}
	kind := normalizeChartKind(anyToString(series[0]["type"]))
	if kind == "" {
		return tools.ChartRebuildInput{}, false
	}

	keys := keysFromRows(rows)
	numericKeys := numericLikeKeys(rows, 120)
	xKey := ""
	yKey := ""
	colorKey := ""

	xAxisName := strings.TrimSpace(axisNameFromAny(options["xAxis"]))
	if xAxisName != "" {
		xKey = matchKeyByName(keys, xAxisName)
	}
	if xKey == "" {
		xLabels := axisCategoryLabels(options["xAxis"])
		if len(xLabels) > 0 {
			xKey = matchKeyByLabelCoverage(rows, keys, xLabels)
		}
	}
	if xKey == "" {
		if kind == "scatter" {
			xKey = firstFromSlice(numericKeys)
		} else {
			xKey = firstNonNumericKey(keys, numericKeys)
			if xKey == "" {
				xKey = firstFromSlice(keys)
			}
		}
	}

	yAxisName := strings.TrimSpace(axisNameFromAny(options["yAxis"]))
	yDerivedFromHint := false
	if yAxisName != "" {
		yKey = matchKeyByName(keys, yAxisName)
		yDerivedFromHint = yKey != ""
	}
	if yKey == "" {
		seriesName := strings.TrimSpace(anyToString(series[0]["name"]))
		if seriesName != "" {
			yKey = matchKeyByName(keys, seriesName)
			yDerivedFromHint = yKey != ""
		}
	}
	if yKey == "" {
		for _, key := range numericKeys {
			if key != xKey {
				yKey = key
				break
			}
		}
	}
	if !yDerivedFromHint && kind != "scatter" {
		return tools.ChartRebuildInput{}, false
	}

	if len(series) > 1 {
		seriesNames := make([]string, 0, len(series))
		for _, s := range series {
			name := strings.TrimSpace(anyToString(s["name"]))
			if name != "" {
				seriesNames = append(seriesNames, name)
			}
		}
		if len(seriesNames) > 0 {
			colorKey = matchKeyForSeriesNames(rows, keys, seriesNames)
		}
	}

	if xKey == "" || yKey == "" {
		return tools.ChartRebuildInput{}, false
	}
	return tools.ChartRebuildInput{
		Kind:   kind,
		Engine: inferEngineFromPlotSpec(root, metadata),
		X:      xKey,
		Y:      yKey,
		Color:  colorKey,
	}, true
}

func inferEngineFromMetadataOrRows(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v := strings.TrimSpace(anyToString(metadata["chart_engine"])); v != "" {
		return v
	}
	return ""
}

func inferEngineFromPlotSpec(plotSpec map[string]any, metadata map[string]any) string {
	if v := strings.TrimSpace(inferEngineFromMetadataOrRows(metadata)); v != "" {
		return v
	}
	schemaVersion := strings.ToLower(strings.TrimSpace(anyToString(plotSpec["schema_version"])))
	if strings.HasPrefix(schemaVersion, "echarts-") {
		return "echarts"
	}
	if strings.HasPrefix(schemaVersion, "highcharts-") {
		return "highcharts"
	}
	return ""
}

func normalizeChartKind(raw string) string {
	kind := strings.ToLower(strings.TrimSpace(raw))
	switch kind {
	case "line", "bar", "scatter":
		return kind
	case "column":
		return "bar"
	case "bubble":
		return "scatter"
	default:
		return ""
	}
}

func rebuildPlotSpecFallback(plotSpec any, rows []map[string]any) (map[string]any, bool) {
	root, ok := deepCloneJSONMap(plotSpec)
	if !ok {
		return nil, false
	}
	if changed, ok := filterSeriesByRows(root, rows); ok {
		return changed, true
	}
	if changed, ok := rebuildHistogramRanges(root, rows); ok {
		return changed, true
	}
	return nil, false
}

func filterSeriesByRows(plotSpec map[string]any, rows []map[string]any) (map[string]any, bool) {
	options, ok := plotSpec["options"].(map[string]any)
	if !ok {
		return nil, false
	}
	seriesRaw, ok := options["series"].([]any)
	if !ok || len(seriesRaw) <= 1 {
		return nil, false
	}

	seriesNames := make([]string, 0, len(seriesRaw))
	for _, item := range seriesRaw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(anyToString(obj["name"]))
		if name != "" {
			seriesNames = append(seriesNames, name)
		}
	}
	if len(seriesNames) <= 1 {
		return nil, false
	}

	keys := keysFromRows(rows)
	column := matchKeyForSeriesNames(rows, keys, seriesNames)
	if column == "" {
		return nil, false
	}

	allowed := map[string]struct{}{}
	for _, row := range rows {
		value := strings.TrimSpace(anyToString(row[column]))
		if value != "" {
			allowed[value] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil, false
	}

	filtered := make([]any, 0, len(seriesRaw))
	for _, item := range seriesRaw {
		obj, ok := item.(map[string]any)
		if !ok {
			filtered = append(filtered, item)
			continue
		}
		name := strings.TrimSpace(anyToString(obj["name"]))
		if name == "" {
			filtered = append(filtered, item)
			continue
		}
		if _, ok := allowed[name]; ok {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 || len(filtered) == len(seriesRaw) {
		return nil, false
	}

	options["series"] = filtered
	plotSpec["options"] = options
	return plotSpec, true
}

type numericRange struct {
	Min          float64
	Max          float64
	IncludeUpper bool
}

func rebuildHistogramRanges(plotSpec map[string]any, rows []map[string]any) (map[string]any, bool) {
	options, ok := plotSpec["options"].(map[string]any)
	if !ok {
		return nil, false
	}
	seriesRaw, ok := options["series"].([]any)
	if !ok || len(seriesRaw) != 1 {
		return nil, false
	}
	series0, ok := seriesRaw[0].(map[string]any)
	if !ok {
		return nil, false
	}
	dataRaw, ok := series0["data"].([]any)
	if !ok || len(dataRaw) == 0 {
		return nil, false
	}

	xLabels := axisCategoryLabels(options["xAxis"])
	if len(xLabels) == 0 || len(xLabels) != len(dataRaw) {
		return nil, false
	}
	ranges, ok := parseNumericRanges(xLabels)
	if !ok || len(ranges) != len(xLabels) {
		return nil, false
	}

	valueColumn := inferHistogramValueColumn(rows, axisNameFromAny(options["xAxis"]))
	if valueColumn == "" {
		return nil, false
	}

	counts := make([]int, len(ranges))
	for _, row := range rows {
		value, ok := coerceFiniteFloat(row[valueColumn])
		if !ok {
			continue
		}
		for idx, r := range ranges {
			if value < r.Min {
				continue
			}
			if value > r.Max {
				continue
			}
			if value == r.Max && !r.IncludeUpper {
				continue
			}
			counts[idx]++
			break
		}
	}
	updatedData := make([]any, 0, len(counts))
	for _, c := range counts {
		updatedData = append(updatedData, c)
	}
	series0["data"] = updatedData
	seriesRaw[0] = series0
	options["series"] = seriesRaw
	plotSpec["options"] = options
	return plotSpec, true
}

func parseNumericRanges(labels []string) ([]numericRange, bool) {
	if len(labels) == 0 {
		return nil, false
	}
	ranges := make([]numericRange, 0, len(labels))
	for i, label := range labels {
		clean := strings.TrimSpace(strings.ToLower(label))
		clean = strings.ReplaceAll(clean, " ", "")
		clean = strings.ReplaceAll(clean, "–", "-")
		clean = strings.ReplaceAll(clean, "—", "-")
		if clean == "" {
			return nil, false
		}
		if strings.HasSuffix(clean, "+") {
			minVal, ok := parseLooseFloat(strings.TrimSuffix(clean, "+"))
			if !ok {
				return nil, false
			}
			ranges = append(ranges, numericRange{Min: minVal, Max: math.Inf(1), IncludeUpper: true})
			continue
		}
		parts := strings.SplitN(clean, "-", 2)
		if len(parts) != 2 {
			return nil, false
		}
		minVal, okMin := parseLooseFloat(parts[0])
		maxVal, okMax := parseLooseFloat(parts[1])
		if !okMin || !okMax {
			return nil, false
		}
		if maxVal < minVal {
			minVal, maxVal = maxVal, minVal
		}
		ranges = append(ranges, numericRange{
			Min:          minVal,
			Max:          maxVal,
			IncludeUpper: i == len(labels)-1,
		})
	}
	return ranges, true
}

func inferHistogramValueColumn(rows []map[string]any, axisName string) string {
	if len(rows) == 0 {
		return ""
	}
	keys := keysFromRows(rows)
	if len(keys) == 0 {
		return ""
	}
	axisTokens := tokenizeAxisName(axisName)
	bestKey := ""
	bestScore := -1
	for _, key := range keys {
		numericCount := 0
		nonEmptyCount := 0
		sampleLimit := minIntFilterChart(len(rows), 200)
		for i := 0; i < sampleLimit; i++ {
			v := rows[i][key]
			if strings.TrimSpace(anyToString(v)) == "" {
				continue
			}
			nonEmptyCount++
			if _, ok := coerceFiniteFloat(v); ok {
				numericCount++
			}
		}
		if nonEmptyCount == 0 {
			continue
		}
		if float64(numericCount)/float64(nonEmptyCount) < 0.6 {
			continue
		}
		score := numericCount
		lowerKey := strings.ToLower(key)
		for _, token := range axisTokens {
			if strings.Contains(lowerKey, token) {
				score += 200
				break
			}
		}
		if score > bestScore {
			bestScore = score
			bestKey = key
		}
	}
	return bestKey
}

func tokenizeAxisName(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	replacer := strings.NewReplacer("_", " ", "-", " ", "(", " ", ")", " ", "/", " ")
	parts := strings.Fields(replacer.Replace(name))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) >= 3 {
			out = append(out, p)
		}
	}
	return out
}

func matchKeyForSeriesNames(rows []map[string]any, keys []string, seriesNames []string) string {
	if len(rows) == 0 || len(seriesNames) == 0 {
		return ""
	}
	target := map[string]struct{}{}
	for _, name := range seriesNames {
		clean := strings.TrimSpace(name)
		if clean != "" {
			target[clean] = struct{}{}
		}
	}
	if len(target) == 0 {
		return ""
	}

	bestKey := ""
	bestMatches := 0
	for _, key := range keys {
		distinct := map[string]struct{}{}
		sampleLimit := minIntFilterChart(len(rows), 300)
		for i := 0; i < sampleLimit; i++ {
			v := strings.TrimSpace(anyToString(rows[i][key]))
			if v != "" {
				distinct[v] = struct{}{}
			}
		}
		if len(distinct) < 2 {
			continue
		}
		matches := 0
		for name := range target {
			if _, ok := distinct[name]; ok {
				matches++
			}
		}
		if matches > bestMatches {
			bestMatches = matches
			bestKey = key
		}
	}
	if bestMatches == 0 {
		return ""
	}
	return bestKey
}

func axisNameFromAny(axis any) string {
	if axisMap, ok := axis.(map[string]any); ok {
		return anyToString(axisMap["name"])
	}
	if axisList, ok := axis.([]any); ok && len(axisList) > 0 {
		if first, ok := axisList[0].(map[string]any); ok {
			return anyToString(first["name"])
		}
	}
	return ""
}

func axisCategoryLabels(axis any) []string {
	axisMap, ok := axis.(map[string]any)
	if !ok {
		if axisList, ok := axis.([]any); ok && len(axisList) > 0 {
			axisMap, _ = axisList[0].(map[string]any)
		}
	}
	if axisMap == nil {
		return nil
	}
	data, ok := axisMap["data"].([]any)
	if !ok || len(data) == 0 {
		categories, ok := axisMap["categories"].([]any)
		if !ok || len(categories) == 0 {
			return nil
		}
		data = categories
	}
	labels := make([]string, 0, len(data))
	for _, item := range data {
		label := strings.TrimSpace(anyToString(item))
		if label != "" {
			labels = append(labels, label)
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

func matchKeyByLabelCoverage(rows []map[string]any, keys []string, labels []string) string {
	if len(rows) == 0 || len(keys) == 0 || len(labels) == 0 {
		return ""
	}
	labelSet := map[string]struct{}{}
	for _, label := range labels {
		labelSet[strings.TrimSpace(label)] = struct{}{}
	}
	bestKey := ""
	bestMatches := 0
	for _, key := range keys {
		matches := 0
		sampleLimit := minIntFilterChart(len(rows), 300)
		for i := 0; i < sampleLimit; i++ {
			value := strings.TrimSpace(anyToString(rows[i][key]))
			if value == "" {
				continue
			}
			if _, ok := labelSet[value]; ok {
				matches++
			}
		}
		if matches > bestMatches {
			bestMatches = matches
			bestKey = key
		}
	}
	if bestMatches < 2 {
		return ""
	}
	return bestKey
}

func matchKeyByName(keys []string, name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}
	for _, key := range keys {
		if strings.ToLower(strings.TrimSpace(key)) == name {
			return key
		}
	}
	for _, key := range keys {
		if strings.Contains(strings.ToLower(key), name) || strings.Contains(name, strings.ToLower(key)) {
			return key
		}
	}
	return ""
}

func firstNonNumericKey(keys []string, numericKeys []string) string {
	numericSet := map[string]struct{}{}
	for _, key := range numericKeys {
		numericSet[key] = struct{}{}
	}
	for _, key := range keys {
		if _, ok := numericSet[key]; !ok {
			return key
		}
	}
	return ""
}

func firstFromSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func keysFromRows(rows []map[string]any) []string {
	if len(rows) == 0 {
		return nil
	}
	keys := make([]string, 0, len(rows[0]))
	for key := range rows[0] {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func numericLikeKeys(rows []map[string]any, sampleLimit int) []string {
	if len(rows) == 0 {
		return nil
	}
	keys := keysFromRows(rows)
	if len(keys) == 0 {
		return nil
	}
	limit := minIntFilterChart(len(rows), sampleLimit)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		numericCount := 0
		nonEmptyCount := 0
		for i := 0; i < limit; i++ {
			value := rows[i][key]
			if strings.TrimSpace(anyToString(value)) == "" {
				continue
			}
			nonEmptyCount++
			if _, ok := coerceFiniteFloat(value); ok {
				numericCount++
			}
		}
		if nonEmptyCount == 0 {
			continue
		}
		if float64(numericCount)/float64(nonEmptyCount) >= 0.6 {
			out = append(out, key)
		}
	}
	return out
}

func objectSliceFromAny(value any) []map[string]any {
	raw, ok := value.([]any)
	if !ok || len(raw) == 0 {
		if single, ok := value.(map[string]any); ok {
			return []map[string]any{single}
		}
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func deepCloneJSONMap(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	blob, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var out map[string]any
	if err := json.Unmarshal(blob, &out); err != nil {
		return nil, false
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int8:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint:
		return strconv.FormatUint(uint64(x), 10)
	case uint8:
		return strconv.FormatUint(uint64(x), 10)
	case uint16:
		return strconv.FormatUint(uint64(x), 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case bool:
		if x {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func stringSliceFromAny(v any) []string {
	raw, ok := v.([]any)
	if !ok || len(raw) == 0 {
		if ss, ok := v.([]string); ok {
			out := make([]string, 0, len(ss))
			for _, item := range ss {
				item = strings.TrimSpace(item)
				if item != "" {
					out = append(out, item)
				}
			}
			return out
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text := strings.TrimSpace(anyToString(item))
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func parseLooseFloat(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	raw = strings.ReplaceAll(raw, ",", "")
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	return value, true
}

func coerceFiniteFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0, false
		}
		return x, true
	case float32:
		n := float64(x)
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case json.Number:
		if f, err := x.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, true
		}
	case string:
		return parseLooseFloat(x)
	}
	return 0, false
}

func minIntFilterChart(a, b int) int {
	if a < b {
		return a
	}
	return b
}
