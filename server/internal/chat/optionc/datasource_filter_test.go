package optionc

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func TestFilterToolsByDatasource(t *testing.T) {
	defs := []types.ToolDefinition{
		{Function: types.ToolFunctionDefinition{Name: "run_bigquery_query"}},
		{Function: types.ToolFunctionDefinition{Name: "list_bq_datasets"}},
		{Function: types.ToolFunctionDefinition{Name: "run_databricks_query"}},
		{Function: types.ToolFunctionDefinition{Name: "run_trino_query"}},
		{Function: types.ToolFunctionDefinition{Name: "run_snowflake_query"}},
		{Function: types.ToolFunctionDefinition{Name: "list_files"}},
		{Function: types.ToolFunctionDefinition{Name: "search_docs"}},
	}

	none := filterToolsByDatasource(defs, false, false)
	names := toolNames(none)
	if len(names) != 2 || (!contains(names, "list_files") || !contains(names, "search_docs")) {
		t.Fatalf("expected only non-datasource tools, got %#v", names)
	}

	bqOnly := filterToolsByDatasource(defs, true, false)
	names = toolNames(bqOnly)
	for _, n := range names {
		if strings.HasPrefix(n, "run_databricks") || strings.HasPrefix(n, "list_databricks") {
			t.Fatalf("unexpected databricks tool in BigQuery-only list: %s", n)
		}
	}
	if len(names) != 4 || !contains(names, "run_bigquery_query") {
		t.Fatalf("expected bigquery + non-datasource tools, got %#v", names)
	}

	bothEnabled := filterToolsByDatasource(defs, true, true, true, true)
	names = toolNames(bothEnabled)
	if len(names) != 7 {
		t.Fatalf("expected all datasource and non-datasource tools when both enabled, got %#v", names)
	}
	if !contains(names, "run_bigquery_query") || !contains(names, "list_bq_datasets") || !contains(names, "run_databricks_query") || !contains(names, "run_trino_query") || !contains(names, "run_snowflake_query") {
		t.Fatalf("expected all datasource tools when enabled, got %#v", names)
	}

	dbOnly := filterToolsByDatasource(defs, false, true)
	names = toolNames(dbOnly)
	for _, n := range names {
		if strings.HasPrefix(n, "run_bigquery") || strings.HasPrefix(n, "list_bq_") {
			t.Fatalf("unexpected bigquery tool in Databricks-only list: %s", n)
		}
	}
	if len(names) != 3 || !contains(names, "run_databricks_query") {
		t.Fatalf("expected databricks + non-datasource tools, got %#v", names)
	}

	trinoOnly := filterToolsByDatasource(defs, false, false, true, false)
	names = toolNames(trinoOnly)
	for _, n := range names {
		if strings.HasPrefix(n, "run_bigquery") || strings.HasPrefix(n, "list_bq_") || strings.HasPrefix(n, "run_databricks") || strings.HasPrefix(n, "list_databricks") || strings.HasPrefix(n, "run_snowflake") || strings.HasPrefix(n, "list_snowflake") {
			t.Fatalf("unexpected non-trino datasource tool in Trino-only list: %s", n)
		}
	}
	if len(names) != 3 || !contains(names, "run_trino_query") {
		t.Fatalf("expected trino + non-datasource tools, got %#v", names)
	}

	snowflakeOnly := filterToolsByDatasource(defs, false, false, false, true)
	names = toolNames(snowflakeOnly)
	for _, n := range names {
		if strings.HasPrefix(n, "run_bigquery") || strings.HasPrefix(n, "list_bq_") || strings.HasPrefix(n, "run_databricks") || strings.HasPrefix(n, "list_databricks") || strings.HasPrefix(n, "run_trino") || strings.HasPrefix(n, "list_trino") {
			t.Fatalf("unexpected non-snowflake datasource tool in Snowflake-only list: %s", n)
		}
	}
	if len(names) != 3 || !contains(names, "run_snowflake_query") {
		t.Fatalf("expected snowflake + non-datasource tools, got %#v", names)
	}
}

func TestDatasourceEnabledUsesEnabledFlagOnly(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }

	for _, tc := range []struct {
		name       string
		settings   config.AppSettings
		datasource string
		want       bool
	}{
		{
			name:       "bigquery missing datasource data",
			datasource: "bigquery",
			settings:   config.AppSettings{},
			want:       false,
		},
		{
			name:       "bigquery enabled but no check result",
			datasource: "bigquery",
			settings: config.AppSettings{
				DataSources: &config.DatasourceConnections{
					BigQuery: &config.DatasourceConnectionState{
						Enabled: boolPtr(true),
					},
				},
			},
			want: true,
		},
		{
			name:       "bigquery enabled but failed last check",
			datasource: "bigquery",
			settings: config.AppSettings{
				DataSources: &config.DatasourceConnections{
					BigQuery: &config.DatasourceConnectionState{
						Enabled:     boolPtr(true),
						LastCheckOk: boolPtr(false),
					},
				},
			},
			want: true,
		},
		{
			name:       "bigquery enabled and passed last check",
			datasource: "bigquery",
			settings: config.AppSettings{
				DataSources: &config.DatasourceConnections{
					BigQuery: &config.DatasourceConnectionState{
						Enabled:     boolPtr(true),
						LastCheckOk: boolPtr(true),
					},
				},
			},
			want: true,
		},
		{
			name:       "databricks enabled but failed last check",
			datasource: "databricks",
			settings: config.AppSettings{
				DataSources: &config.DatasourceConnections{
					Databricks: &config.DatasourceConnectionState{
						Enabled:     boolPtr(true),
						LastCheckOk: boolPtr(false),
					},
				},
			},
			want: false,
		},
		{
			name:       "databricks enabled but no check result",
			datasource: "databricks",
			settings: config.AppSettings{
				DataSources: &config.DatasourceConnections{
					Databricks: &config.DatasourceConnectionState{
						Enabled: boolPtr(true),
					},
				},
			},
			want: false,
		},
		{
			name:       "databricks enabled and passed last check",
			datasource: "databricks",
			settings: config.AppSettings{
				DataSources: &config.DatasourceConnections{
					Databricks: &config.DatasourceConnectionState{
						Enabled:     boolPtr(true),
						LastCheckOk: boolPtr(true),
					},
				},
			},
			want: true,
		},
		{
			name:       "snowflake defaults to disabled when not configured",
			datasource: "snowflake",
			settings:   config.AppSettings{},
			want:       false,
		},
		{
			name:       "trino enabled but failed last check",
			datasource: "trino",
			settings: config.AppSettings{
				FeatureFlags: &config.FeatureFlags{CloudCredentialsTrino: true},
				DataSources: &config.DatasourceConnections{
					Trino: &config.DatasourceConnectionState{
						Enabled:     boolPtr(true),
						LastCheckOk: boolPtr(false),
					},
				},
			},
			want: false,
		},
		{
			name:       "trino enabled and passed last check",
			datasource: "trino",
			settings: config.AppSettings{
				FeatureFlags: &config.FeatureFlags{CloudCredentialsTrino: true},
				DataSources: &config.DatasourceConnections{
					Trino: &config.DatasourceConnectionState{
						Enabled:     boolPtr(true),
						LastCheckOk: boolPtr(true),
					},
				},
			},
			want: true,
		},
		{
			name:       "trino disabled by feature flag even when enabled and healthy",
			datasource: "trino",
			settings: config.AppSettings{
				FeatureFlags: &config.FeatureFlags{CloudCredentialsTrino: false},
				DataSources: &config.DatasourceConnections{
					Trino: &config.DatasourceConnectionState{
						Enabled:     boolPtr(true),
						LastCheckOk: boolPtr(true),
					},
				},
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := datasourceEnabled(tc.settings, tc.datasource)
			if got != tc.want {
				t.Fatalf("datasourceEnabled(..., %q) = %v, want %v", tc.datasource, got, tc.want)
			}
		})
	}
}

func TestDatasourceEnabledSnowflakeValidationAndFeatureGate(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }

	settings := config.AppSettings{
		FeatureFlags: &config.FeatureFlags{},
		DataSources:  &config.DatasourceConnections{},
	}
	if !setDatasourceConnectionState(settings.DataSources, "snowflake", &config.DatasourceConnectionState{
		Enabled:     boolPtr(true),
		LastCheckOk: boolPtr(false),
	}) {
		if datasourceEnabled(settings, "snowflake") {
			t.Fatalf("snowflake should be disabled when datasource fields are unavailable")
		}
		return
	}
	if !setDatasourceFeatureFlagForTest(settings.FeatureFlags, "snowflake", true) {
		t.Fatalf("snowflake feature flag field was expected but not found")
	}
	if datasourceEnabled(settings, "snowflake") {
		t.Fatalf("snowflake should be disabled when lastCheckOk=false")
	}

	if state := datasourceConnectionState(settings, "snowflake"); state == nil {
		t.Fatalf("snowflake state missing after setup")
	} else {
		state.LastCheckOk = boolPtr(true)
	}
	if !datasourceEnabled(settings, "snowflake") {
		t.Fatalf("snowflake should be enabled when feature flag=true and lastCheckOk=true")
	}

	_ = setDatasourceFeatureFlagForTest(settings.FeatureFlags, "snowflake", false)
	if datasourceEnabled(settings, "snowflake") {
		t.Fatalf("snowflake should be disabled when feature flag=false")
	}
}

func TestApplyRunDatasourceOverrides(t *testing.T) {
	boolPtr := func(v bool) *bool { return &v }
	legacyBQProject := "legacy"

	base := config.AppSettings{
		DataSources: &config.DatasourceConnections{
			BigQuery: &config.DatasourceConnectionState{
				Enabled:       boolPtr(false),
				LastCheckOk:   boolPtr(false),
				LastProjectID: &legacyBQProject,
			},
			Databricks: &config.DatasourceConnectionState{
				Enabled:     boolPtr(false),
				LastCheckOk: boolPtr(true),
			},
			Trino: &config.DatasourceConnectionState{
				Enabled:     boolPtr(false),
				LastCheckOk: boolPtr(true),
			},
		},
	}
	overrides := &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{
			Enabled: boolPtr(true),
		},
	}

	merged := applyRunDatasourceOverrides(base, overrides)
	if merged.DataSources == nil || merged.DataSources.BigQuery == nil || merged.DataSources.Databricks == nil || merged.DataSources.Trino == nil {
		t.Fatalf("expected both datasource entries, got %#v", merged.DataSources)
	}
	if merged.DataSources.BigQuery.Enabled == nil || !*merged.DataSources.BigQuery.Enabled {
		t.Fatalf("bigquery enabled should come from override=true, got %#v", merged.DataSources.BigQuery)
	}
	if merged.DataSources.BigQuery.LastProjectID == nil || strings.TrimSpace(*merged.DataSources.BigQuery.LastProjectID) != "legacy" {
		t.Fatalf("non-overridden datasource metadata should be preserved, got %#v", merged.DataSources.BigQuery)
	}
	if merged.DataSources.Databricks.Enabled == nil || *merged.DataSources.Databricks.Enabled {
		t.Fatalf("databricks should remain disabled from base settings, got %#v", merged.DataSources.Databricks)
	}
	if merged.DataSources.Trino.Enabled == nil || *merged.DataSources.Trino.Enabled {
		t.Fatalf("trino should remain disabled from base settings, got %#v", merged.DataSources.Trino)
	}
	if base.DataSources.BigQuery.Enabled == nil || *base.DataSources.BigQuery.Enabled {
		t.Fatalf("applyRunDatasourceOverrides should not mutate base settings")
	}

	overrides2 := &config.DatasourceConnections{
		Databricks: &config.DatasourceConnectionState{
			Enabled: boolPtr(true),
		},
	}
	merged2 := applyRunDatasourceOverrides(base, overrides2)
	if merged2.DataSources == nil || merged2.DataSources.Databricks == nil || merged2.DataSources.BigQuery == nil || merged2.DataSources.Trino == nil {
		t.Fatalf("expected both datasource entries after partial override, got %#v", merged2.DataSources)
	}
	if merged2.DataSources.Databricks.Enabled == nil || !*merged2.DataSources.Databricks.Enabled {
		t.Fatalf("databricks enabled should come from override, got %#v", merged2.DataSources.Databricks)
	}

	overrides3 := &config.DatasourceConnections{
		Trino: &config.DatasourceConnectionState{
			Enabled: boolPtr(true),
		},
	}
	merged3 := applyRunDatasourceOverrides(base, overrides3)
	if merged3.DataSources == nil || merged3.DataSources.Trino == nil {
		t.Fatalf("expected trino datasource override to be present")
	}
	if merged3.DataSources.Trino.Enabled == nil || !*merged3.DataSources.Trino.Enabled {
		t.Fatalf("trino enabled should come from override, got %#v", merged3.DataSources.Trino)
	}

	merged4 := applyRunDatasourceOverrides(base, nil, map[string]any{
		"snowflake": map[string]any{"enabled": true},
	})
	if state := datasourceConnectionState(merged4, "snowflake"); state != nil {
		if state.Enabled == nil || !*state.Enabled {
			t.Fatalf("snowflake enabled should come from raw override, got %#v", state)
		}
	}
	if state := datasourceConnectionState(base, "snowflake"); state != nil && state.Enabled != nil && *state.Enabled {
		t.Fatalf("applyRunDatasourceOverrides should not mutate base snowflake settings")
	}
}

func TestExecTool_DatabaseToolBlockedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	storeSvc, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := storeSvc.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}
	manager := &Manager{
		store:  storeSvc,
		tools:  tools.NewRegistry(nil),
		cfgDir: cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc-db-disabled",
		Name: "run_bigquery_query",
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	msg, ok := res["error"].(string)
	if !ok {
		t.Fatalf("expected error message, got %#v", res["error"])
	}
	if !strings.Contains(msg, "tool is disabled") {
		t.Fatalf("error = %q, want disabled", msg)
	}
}

func toolNames(defs []types.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, d.Function.Name)
	}
	return out
}

func contains(ss []string, value string) bool {
	for _, s := range ss {
		if s == value {
			return true
		}
	}
	return false
}

func setDatasourceFeatureFlagForTest(flags *config.FeatureFlags, datasource string, enabled bool) bool {
	if flags == nil {
		return false
	}
	fieldName := datasourceFeatureFlagField(datasource)
	if fieldName == "" {
		return false
	}
	rv := reflect.ValueOf(flags)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return false
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return false
	}
	field := elem.FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	switch field.Kind() {
	case reflect.Bool:
		field.SetBool(enabled)
		return true
	case reflect.Ptr:
		if field.Type().Elem().Kind() != reflect.Bool {
			return false
		}
		v := enabled
		field.Set(reflect.ValueOf(&v))
		return true
	default:
		return false
	}
}
