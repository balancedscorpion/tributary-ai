package optionc

import (
	"reflect"
	"strings"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

var datasourceConnectionFieldMap = map[string]string{
	"bigquery":   "BigQuery",
	"databricks": "Databricks",
	"trino":      "Trino",
	"snowflake":  "Snowflake",
}

var datasourceFeatureFlagFieldMap = map[string]string{
	"trino":     "CloudCredentialsTrino",
	"snowflake": "CloudCredentialsSnowflake",
}

func normalizeDatasourceName(datasource string) string {
	return strings.TrimSpace(strings.ToLower(datasource))
}

func datasourceConnectionField(datasource string) string {
	return strings.TrimSpace(datasourceConnectionFieldMap[normalizeDatasourceName(datasource)])
}

func datasourceFeatureFlagField(datasource string) string {
	return strings.TrimSpace(datasourceFeatureFlagFieldMap[normalizeDatasourceName(datasource)])
}

func datasourceConnectionStateFromConnections(conns *config.DatasourceConnections, datasource string) *config.DatasourceConnectionState {
	if conns == nil {
		return nil
	}
	fieldName := datasourceConnectionField(datasource)
	if fieldName == "" {
		return nil
	}
	rv := reflect.ValueOf(conns)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return nil
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return nil
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || field.Kind() != reflect.Ptr || field.IsNil() {
		return nil
	}
	state, _ := field.Interface().(*config.DatasourceConnectionState)
	return state
}

func setDatasourceConnectionState(conns *config.DatasourceConnections, datasource string, state *config.DatasourceConnectionState) bool {
	if conns == nil || state == nil {
		return false
	}
	fieldName := datasourceConnectionField(datasource)
	if fieldName == "" {
		return false
	}
	rv := reflect.ValueOf(conns)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return false
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return false
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Ptr {
		return false
	}
	value := reflect.ValueOf(state)
	if !value.Type().AssignableTo(field.Type()) {
		return false
	}
	field.Set(value)
	return true
}

func ensureDatasourceConnectionState(conns *config.DatasourceConnections, datasource string) *config.DatasourceConnectionState {
	if conns == nil {
		return nil
	}
	if existing := datasourceConnectionStateFromConnections(conns, datasource); existing != nil {
		return existing
	}
	state := &config.DatasourceConnectionState{}
	if !setDatasourceConnectionState(conns, datasource, state) {
		return nil
	}
	return datasourceConnectionStateFromConnections(conns, datasource)
}

func datasourceFeatureFlagEnabled(settings config.AppSettings, datasource string) bool {
	fieldName := datasourceFeatureFlagField(datasource)
	if fieldName == "" {
		return true
	}
	if settings.FeatureFlags == nil {
		return false
	}
	rv := reflect.ValueOf(settings.FeatureFlags)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return false
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return false
	}
	field := rv.FieldByName(fieldName)
	if !field.IsValid() {
		return false
	}
	switch field.Kind() {
	case reflect.Bool:
		return field.Bool()
	case reflect.Ptr:
		if field.IsNil() {
			return false
		}
		elem := field.Elem()
		if elem.Kind() == reflect.Bool {
			return elem.Bool()
		}
	}
	return false
}

func datasourceConnectionState(settings config.AppSettings, datasource string) *config.DatasourceConnectionState {
	return datasourceConnectionStateFromConnections(settings.DataSources, datasource)
}

func datasourceEnabled(settings config.AppSettings, datasource string) bool {
	name := normalizeDatasourceName(datasource)
	if name == "" {
		return false
	}
	if !datasourceFeatureFlagEnabled(settings, name) {
		return false
	}
	state := datasourceConnectionState(settings, name)
	if state == nil || state.Enabled == nil || !*state.Enabled {
		return false
	}
	switch name {
	case "databricks", "trino", "snowflake":
		if state.LastCheckOk == nil || !*state.LastCheckOk {
			return false
		}
	}
	return true
}

func datasourceEnablement(settings config.AppSettings) map[string]bool {
	out := map[string]bool{}
	for _, capability := range tools.DatasourceCapabilities() {
		out[strings.TrimSpace(strings.ToLower(capability.Name))] = datasourceEnabled(settings, capability.Name)
	}
	return out
}

func isDatasourceToolAllowed(toolName string, isBQEnabled bool, isDBEnabled bool, extraDatasourceEnabled ...bool) bool {
	enabledByDatasource := map[string]bool{
		"bigquery":   isBQEnabled,
		"databricks": isDBEnabled,
	}
	if len(extraDatasourceEnabled) > 0 {
		enabledByDatasource["trino"] = extraDatasourceEnabled[0]
	}
	if len(extraDatasourceEnabled) > 1 {
		enabledByDatasource["snowflake"] = extraDatasourceEnabled[1]
	}
	return isDatasourceToolAllowedByMap(toolName, enabledByDatasource)
}

func isDatasourceToolAllowedByMap(toolName string, enabledByDatasource map[string]bool) bool {
	name := strings.TrimSpace(toolName)
	if name == "" {
		return true
	}
	datasource := strings.TrimSpace(strings.ToLower(tools.DatasourceForToolName(name)))
	if datasource == "" {
		return true
	}
	if enabledByDatasource == nil {
		return false
	}
	enabled, ok := enabledByDatasource[datasource]
	return ok && enabled
}

func filterToolsByDatasource(toolDefs []types.ToolDefinition, isBQEnabled bool, isDBEnabled bool, extraDatasourceEnabled ...bool) []types.ToolDefinition {
	enabledByDatasource := map[string]bool{
		"bigquery":   isBQEnabled,
		"databricks": isDBEnabled,
	}
	if len(extraDatasourceEnabled) > 0 {
		enabledByDatasource["trino"] = extraDatasourceEnabled[0]
	}
	if len(extraDatasourceEnabled) > 1 {
		enabledByDatasource["snowflake"] = extraDatasourceEnabled[1]
	}
	return filterToolsByDatasourceEnabled(toolDefs, enabledByDatasource)
}

func filterToolsByDatasourceEnabled(toolDefs []types.ToolDefinition, enabledByDatasource map[string]bool) []types.ToolDefinition {
	out := make([]types.ToolDefinition, 0, len(toolDefs))
	for _, def := range toolDefs {
		if !isDatasourceToolAllowedByMap(def.Function.Name, enabledByDatasource) {
			continue
		}
		out = append(out, def)
	}
	return out
}
