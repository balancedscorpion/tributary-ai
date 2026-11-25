package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDatabaseQueryPolicyDefaultsAreExplicitAndSafe(t *testing.T) {
	want := DatabaseQueryPolicy{
		MaxExecutingGlobal:               8,
		MaxExecutingPerThread:            3,
		MaxExecutingPerConnection:        2,
		InlineWaitMilliseconds:           90_000,
		MaxPollIntervalMilliseconds:      30_000,
		MonitorErrorBackoffMilliseconds:  120_000,
		SoftQueueTimeoutMilliseconds:     600_000,
		SoftExecutionTimeoutMilliseconds: 1_800_000,
		HardExecutionTimeoutMilliseconds: 21_600_000,
		MaxResultRows:                    2500,
		MaxMaterializedBytes:             32 * 1024 * 1024,
		BigQueryMaximumBytesBilled:       100 * 1024 * 1024 * 1024,
		MaxAutoContinuations:             8,
	}
	got := DefaultDatabaseQueryPolicy()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultDatabaseQueryPolicy()=%#v, want %#v", got, want)
	}

	settings := DefaultSettings()
	if settings.DatabaseQueryPolicy == nil || !reflect.DeepEqual(*settings.DatabaseQueryPolicy, want) {
		t.Fatalf("DefaultSettings().DatabaseQueryPolicy=%#v, want %#v", settings.DatabaseQueryPolicy, want)
	}
	raw, err := json.Marshal(settings.DatabaseQueryPolicy)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	for _, key := range []string{
		"inlineWaitMilliseconds", "maxPollIntervalMilliseconds", "monitorErrorBackoffMilliseconds",
		"softQueueTimeoutMilliseconds", "softExecutionTimeoutMilliseconds", "hardExecutionTimeoutMilliseconds",
		"maxMaterializedBytes", "bigQueryMaximumBytesBilled",
	} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Fatalf("serialized policy %s does not contain explicit unit key %q", raw, key)
		}
	}
}

func TestBigQueryConnectionScanBudgetsSanitizeAndClone(t *testing.T) {
	automatic := int64(200 << 30)
	approved := int64(100 << 30)
	in := DefaultSettings()
	in.DataSources.BigQuery.AutomaticScanBytes = &automatic
	in.DataSources.BigQuery.ApprovedScanBytes = &approved

	out := sanitizeSettings(in)
	state := out.DataSources.BigQuery
	if state.AutomaticScanBytes == nil || *state.AutomaticScanBytes != automatic {
		t.Fatalf("automatic scan bytes=%v want %d", state.AutomaticScanBytes, automatic)
	}
	if state.ApprovedScanBytes == nil || *state.ApprovedScanBytes != automatic {
		t.Fatalf("approved scan bytes=%v want clamped to %d", state.ApprovedScanBytes, automatic)
	}

	cloned := out.Clone()
	if cloned.DataSources.BigQuery.AutomaticScanBytes == state.AutomaticScanBytes ||
		cloned.DataSources.BigQuery.ApprovedScanBytes == state.ApprovedScanBytes {
		t.Fatal("clone aliases BigQuery scan budget pointers")
	}
	*cloned.DataSources.BigQuery.AutomaticScanBytes = 300 << 30
	if *state.AutomaticScanBytes != automatic {
		t.Fatal("mutating clone changed original BigQuery scan budget")
	}

	invalid := int64(-1)
	in.DataSources.BigQuery.AutomaticScanBytes = &invalid
	in.DataSources.BigQuery.ApprovedScanBytes = &invalid
	sanitized := sanitizeSettings(in).DataSources.BigQuery
	if sanitized.AutomaticScanBytes != nil || sanitized.ApprovedScanBytes != nil {
		t.Fatalf("invalid scan budgets were retained: %#v", sanitized)
	}
}

func TestSanitizeDatabaseQueryPolicyAndClone(t *testing.T) {
	in := DefaultSettings()
	in.DatabaseQueryPolicy = &DatabaseQueryPolicy{
		MaxExecutingGlobal:               2,
		MaxExecutingPerThread:            9,
		MaxExecutingPerConnection:        7,
		InlineWaitMilliseconds:           -1,
		MaxPollIntervalMilliseconds:      90_000,
		MonitorErrorBackoffMilliseconds:  1,
		SoftQueueTimeoutMilliseconds:     -1,
		SoftExecutionTimeoutMilliseconds: 600_000,
		HardExecutionTimeoutMilliseconds: 1,
		MaxResultRows:                    999_999,
		MaxMaterializedBytes:             0,
		BigQueryMaximumBytesBilled:       -1,
		MaxAutoContinuations:             0,
	}
	out := sanitizeSettings(in)
	p := out.DatabaseQueryPolicy
	if p == nil {
		t.Fatal("DatabaseQueryPolicy=nil")
	}
	if p.MaxExecutingGlobal != 2 || p.MaxExecutingPerThread != 2 || p.MaxExecutingPerConnection != 2 {
		t.Fatalf("sanitized concurrency=%d/%d/%d, want 2/2/2", p.MaxExecutingGlobal, p.MaxExecutingPerThread, p.MaxExecutingPerConnection)
	}
	if p.InlineWaitMilliseconds != DefaultDatabaseQueryInlineWaitMilliseconds || p.SoftQueueTimeoutMilliseconds != DefaultDatabaseQuerySoftQueueTimeoutMilliseconds {
		t.Fatalf("invalid durations did not reset: %#v", p)
	}
	if p.MonitorErrorBackoffMilliseconds != p.MaxPollIntervalMilliseconds {
		t.Fatalf("monitor backoff=%d, want at least max poll=%d", p.MonitorErrorBackoffMilliseconds, p.MaxPollIntervalMilliseconds)
	}
	if p.HardExecutionTimeoutMilliseconds != p.SoftExecutionTimeoutMilliseconds {
		t.Fatalf("hard timeout=%d, want at least soft timeout=%d", p.HardExecutionTimeoutMilliseconds, p.SoftExecutionTimeoutMilliseconds)
	}
	if p.MaxResultRows != MaxDatabaseQueryRowLimit || p.MaxMaterializedBytes != DefaultDatabaseQueryMaxMaterializedBytes || p.BigQueryMaximumBytesBilled != DefaultBigQueryMaximumBytesBilled {
		t.Fatalf("sanitized budgets=%#v", p)
	}
	if p.MaxAutoContinuations != DefaultDatabaseQueryMaxAutoContinuations {
		t.Fatalf("sanitized control fields=%#v", p)
	}

	clone := out.Clone()
	if clone.DatabaseQueryPolicy == out.DatabaseQueryPolicy {
		t.Fatal("Clone aliases DatabaseQueryPolicy")
	}
	clone.DatabaseQueryPolicy.MaxExecutingGlobal = 99
	if out.DatabaseQueryPolicy.MaxExecutingGlobal == 99 {
		t.Fatal("mutating clone changed original DatabaseQueryPolicy")
	}
}

func TestSanitizeDatabaseQueryPolicyClampsHammeringAndFanout(t *testing.T) {
	in := DefaultSettings()
	in.DatabaseQueryPolicy = &DatabaseQueryPolicy{
		MaxExecutingGlobal:               10_000,
		MaxExecutingPerThread:            10_000,
		MaxExecutingPerConnection:        10_000,
		InlineWaitMilliseconds:           1,
		MaxPollIntervalMilliseconds:      1,
		MonitorErrorBackoffMilliseconds:  1,
		SoftQueueTimeoutMilliseconds:     1,
		SoftExecutionTimeoutMilliseconds: 1,
		HardExecutionTimeoutMilliseconds: 1,
		MaxResultRows:                    1,
		MaxMaterializedBytes:             1,
		BigQueryMaximumBytesBilled:       1,
		MaxAutoContinuations:             10_000,
	}
	p := sanitizeSettings(in).DatabaseQueryPolicy
	if p.MaxExecutingGlobal != MaxDatabaseQueryExecutingGlobal ||
		p.MaxExecutingPerThread != MaxDatabaseQueryExecutingPerThread ||
		p.MaxExecutingPerConnection != MaxDatabaseQueryExecutingPerConnection {
		t.Fatalf("fanout limits not clamped: %#v", p)
	}
	if p.InlineWaitMilliseconds != MinDatabaseQueryInlineWaitMilliseconds ||
		p.MaxPollIntervalMilliseconds != MinDatabaseQueryMaxPollIntervalMilliseconds ||
		p.MonitorErrorBackoffMilliseconds != MinDatabaseQueryMonitorErrorBackoffMilliseconds {
		t.Fatalf("pacing limits not clamped: %#v", p)
	}
	if p.SoftQueueTimeoutMilliseconds != MinDatabaseQuerySoftQueueTimeoutMilliseconds ||
		p.SoftExecutionTimeoutMilliseconds != MinDatabaseQuerySoftExecutionTimeoutMilliseconds ||
		p.HardExecutionTimeoutMilliseconds != MinDatabaseQueryHardExecutionTimeoutMilliseconds {
		t.Fatalf("budget minimums not clamped: %#v", p)
	}
	if p.MaxAutoContinuations != MaxDatabaseQueryAutoContinuations {
		t.Fatalf("continuation limit=%d", p.MaxAutoContinuations)
	}
}

func TestSanitizeDatabaseQueryPolicyCarriesLegacyRowLimit(t *testing.T) {
	legacyLimit := 731
	in := DefaultSettings()
	in.DatabaseQueryPolicy = nil
	in.DatabaseQueryRowLimit = &legacyLimit

	out := sanitizeSettings(in)
	if out.DatabaseQueryPolicy == nil || out.DatabaseQueryPolicy.MaxResultRows != legacyLimit {
		t.Fatalf("legacy databaseQueryRowLimit was not carried into policy: %#v", out.DatabaseQueryPolicy)
	}
	if out.DatabaseQueryPolicy.MaxMaterializedBytes != DefaultDatabaseQueryMaxMaterializedBytes {
		t.Fatalf("other policy defaults were not retained: %#v", out.DatabaseQueryPolicy)
	}
}

func TestSanitizeDatabaseQueryPolicyIsAuthoritativeOverLegacyMirror(t *testing.T) {
	legacyLimit := 211
	in := DefaultSettings()
	in.DatabaseQueryRowLimit = &legacyLimit
	in.DatabaseQueryPolicy.MaxResultRows = 877

	out := sanitizeSettings(in)
	if out.DatabaseQueryPolicy == nil || out.DatabaseQueryPolicy.MaxResultRows != 877 {
		t.Fatalf("policy max rows changed: %#v", out.DatabaseQueryPolicy)
	}
	if out.DatabaseQueryRowLimit == nil || *out.DatabaseQueryRowLimit != 877 {
		t.Fatalf("legacy mirror=%v, want authoritative policy value 877", out.DatabaseQueryRowLimit)
	}
}

func TestStoreLoadAppliesLegacyDefaultsAndSanitization(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	legacy := map[string]any{
		"provider": map[string]any{
			"providerId": "compatible",
			"model":      "gpt-4o-mini",
			"profileId":  "default",
		},
		"compatibleProfiles": []map[string]any{
			{
				"id":          "default",
				"displayName": "Legacy Compatible",
				"apiType":     "chat_completions",
				"baseUrl":     "https://api.openai.com/v1",
				"auth": map[string]any{
					"type":   "api_key",
					"header": "Authorization",
					"scheme": "Bearer",
				},
				"apiKey": "legacy-token",
				"extraHeaders": []map[string]any{
					{
						"id":       "legacy-secret",
						"key":      "X-Legacy",
						"value":    "super-secret",
						"isSecret": true,
					},
				},
			},
		},
		"systemPrompt": "Legacy prompt",
		"openai": map[string]any{
			"apiKey":  "legacy-openai-key",
			"baseUrl": "https://api.openai.com/v1",
		},
		"databricksHost":             "   ",
		"snowflakeAccountIdentifier": "   ",
	}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, b, 0o600); err != nil {
		t.Fatalf("write legacy settings: %v", err)
	}

	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	got := st.Get()
	if got.FeatureFlags == nil {
		t.Fatalf("FeatureFlags = nil, want defaults")
	}
	if !got.FeatureFlags.Providers.Compatible {
		t.Fatalf("FeatureFlags.Providers.Compatible = false, want true")
	}
	expectedCloudCreds := defaultShowAllProvidersEnabled()
	if got.FeatureFlags.CloudCredentialsAWS != expectedCloudCreds {
		t.Fatalf("FeatureFlags.CloudCredentialsAWS = %v, want %v for current build profile", got.FeatureFlags.CloudCredentialsAWS, expectedCloudCreds)
	}
	if got.FeatureFlags.CloudCredentialsTrino != expectedCloudCreds {
		t.Fatalf("FeatureFlags.CloudCredentialsTrino = %v, want %v for current build profile", got.FeatureFlags.CloudCredentialsTrino, expectedCloudCreds)
	}
	if got.FeatureFlags.CloudCredentialsSnowflake != expectedCloudCreds {
		t.Fatalf("FeatureFlags.CloudCredentialsSnowflake = %v, want %v for current build profile", got.FeatureFlags.CloudCredentialsSnowflake, expectedCloudCreds)
	}
	if got.FeatureFlags.ChartEngines == nil {
		t.Fatalf("FeatureFlags.ChartEngines = nil, want defaults")
	}
	if !got.FeatureFlags.ChartEngines.Echarts {
		t.Fatalf("FeatureFlags.ChartEngines.Echarts = false, want true")
	}
	if got.FeatureFlags.ChartEngines.Highcharts {
		t.Fatalf("FeatureFlags.ChartEngines.Highcharts = true, want false")
	}
	if got.FeatureFlags.ChartDimensionFilters {
		t.Fatalf("FeatureFlags.ChartDimensionFilters = true, want false")
	}
	if got.CloudCredentials == nil {
		t.Fatalf("CloudCredentials = nil, want empty creds")
	}
	if len(got.CloudCredentials.AWS) != 0 || len(got.CloudCredentials.GCP) != 0 {
		t.Fatalf("CloudCredentials should be empty, got AWS=%d GCP=%d", len(got.CloudCredentials.AWS), len(got.CloudCredentials.GCP))
	}
	if len(got.ProviderConfigs) != 0 {
		t.Fatalf("ProviderConfigs = %#v, want empty slice", got.ProviderConfigs)
	}
	if len(got.ProviderTemplates) != 0 {
		t.Fatalf("ProviderTemplates = %#v, want empty slice", got.ProviderTemplates)
	}
	if got.OpenAI == nil {
		t.Fatalf("OpenAI = nil, want defaults with redacted apiKey")
	}
	if got.OpenAI.ApiKey != "" {
		t.Fatalf("OpenAI.ApiKey = %q, want empty", got.OpenAI.ApiKey)
	}
	if got.OpenAI.WebSearch == nil || got.OpenAI.WebSearch.Enabled == nil || !*got.OpenAI.WebSearch.Enabled {
		t.Fatalf("OpenAI.WebSearch should default to enabled, got %#v", got.OpenAI.WebSearch)
	}
	if got.CompatibleProfiles == nil || len(got.CompatibleProfiles) != 1 {
		t.Fatalf("CompatibleProfiles = %#v, want one entry", got.CompatibleProfiles)
	}
	profile := got.CompatibleProfiles[0]
	if profile.ApiKey == nil || *profile.ApiKey != "" {
		t.Fatalf("compatibleProfiles[0].ApiKey = %v, want empty pointer", profile.ApiKey)
	}
	if len(profile.Extra) != 1 || profile.Extra[0].Value != "" {
		t.Fatalf("compatibleProfiles[0].Extra = %#v, want one redacted secret header", profile.Extra)
	}
	if got.DatabricksHost != nil {
		t.Fatalf("DatabricksHost = %v, want nil", *got.DatabricksHost)
	}
	if got.DataSources == nil || got.DataSources.BigQuery == nil || got.DataSources.BigQuery.Enabled == nil {
		t.Fatalf("DataSources.bigquery.enabled should be set by defaults, got %#v", got.DataSources)
	}
	if got.DataSources == nil || got.DataSources.Databricks == nil || got.DataSources.Databricks.Enabled == nil {
		t.Fatalf("DataSources.databricks.enabled should be set by defaults, got %#v", got.DataSources)
	}
	if got.DataSources == nil || got.DataSources.Trino == nil || got.DataSources.Trino.Enabled == nil {
		t.Fatalf("DataSources.trino.enabled should be set by defaults, got %#v", got.DataSources)
	}
	if got.DataSources == nil || got.DataSources.Snowflake == nil || got.DataSources.Snowflake.Enabled == nil {
		t.Fatalf("DataSources.snowflake.enabled should be set by defaults, got %#v", got.DataSources)
	}
	if got.Connections == nil {
		t.Fatalf("Connections = nil, want empty defaults")
	}
	if len(got.Connections.VisibleIDs) != 1 || got.Connections.VisibleIDs[0] != ConnectionCatalogThreadSearch {
		t.Fatalf("Connections.visibleIds = %#v, want default thread search", got.Connections.VisibleIDs)
	}
	if got.ThreadSearch == nil || got.ThreadSearch.Enabled == nil || *got.ThreadSearch.Enabled != true {
		t.Fatalf("ThreadSearch.enabled = %#v, want true", got.ThreadSearch)
	}
	if got.LocalComputer == nil || got.LocalComputer.Enabled == nil || *got.LocalComputer.Enabled != false {
		t.Fatalf("LocalComputer.enabled = %#v, want false", got.LocalComputer)
	}
	if got.LocalComputer.AccessMode == nil || *got.LocalComputer.AccessMode != LocalComputerAccessModeWholeComputer {
		t.Fatalf("LocalComputer.accessMode = %#v, want whole-computer", got.LocalComputer)
	}
	if len(got.LocalComputer.SelectedFolders) != 0 {
		t.Fatalf("LocalComputer.selectedFolders = %#v, want empty", got.LocalComputer.SelectedFolders)
	}
	if got.DataSources.BigQuery == nil || got.DataSources.BigQuery.Enabled == nil || *got.DataSources.BigQuery.Enabled != false {
		t.Fatalf("DataSources.bigQuery.enabled = %v, want false", *got.DataSources.BigQuery.Enabled)
	}
	if got.DataSources.Databricks == nil || got.DataSources.Databricks.Enabled == nil || *got.DataSources.Databricks.Enabled != false {
		t.Fatalf("DataSources.databricks.enabled = %v, want false", *got.DataSources.Databricks.Enabled)
	}
	if got.DataSources.Trino == nil || got.DataSources.Trino.Enabled == nil || *got.DataSources.Trino.Enabled != false {
		t.Fatalf("DataSources.trino.enabled = %v, want false", *got.DataSources.Trino.Enabled)
	}
	if got.DataSources.Snowflake == nil || got.DataSources.Snowflake.Enabled == nil || *got.DataSources.Snowflake.Enabled != false {
		t.Fatalf("DataSources.snowflake.enabled = %v, want false", *got.DataSources.Snowflake.Enabled)
	}
	if got.SnowflakeAccountIdentifier != nil {
		t.Fatalf("SnowflakeAccountIdentifier = %v, want nil", *got.SnowflakeAccountIdentifier)
	}
}

func TestSanitizeConnectionsSettingsAllowsAllCatalogIDs(t *testing.T) {
	in := &ConnectionsSettings{
		VisibleIDs: []ConnectionCatalogID{
			"BigQuery", // case-insensitive normalization
			ConnectionCatalogTrino,
			ConnectionCatalogSnowflake,
			ConnectionCatalogTrino, // duplicate should be dropped
			"not-a-real-source",    // unknown should be dropped
			ConnectionCatalogDatabricks,
			ConnectionCatalogArxiv,
			ConnectionCatalogThreadSearch,
			ConnectionCatalogImageGeneration,
			ConnectionCatalogWebSearch,
			ConnectionCatalogPythonSandbox,
			" local-computer ",
		},
	}

	out := sanitizeConnectionsSettings(in)
	got := out.VisibleIDs
	want := []ConnectionCatalogID{
		ConnectionCatalogBigQuery,
		ConnectionCatalogTrino,
		ConnectionCatalogSnowflake,
		ConnectionCatalogDatabricks,
		ConnectionCatalogArxiv,
		ConnectionCatalogThreadSearch,
		ConnectionCatalogImageGeneration,
		ConnectionCatalogWebSearch,
		ConnectionCatalogPythonSandbox,
		ConnectionCatalogLocalComputer,
	}
	if len(got) != len(want) {
		t.Fatalf("VisibleIDs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("VisibleIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSanitizeSettingsTrimsDatasourceFields(t *testing.T) {
	s := AppSettings{
		Provider: ProviderSelection{
			ProviderID: "compatible",
			Model:      "gpt-4o-mini",
			ProfileID:  strptr("default"),
		},
		OpenAI: &OpenAIConfig{ApiKey: "", BaseURL: strptr("https://api.openai.com/v1")},
		CompatibleProfiles: []CompatibleProfile{{
			ID:          "default",
			DisplayName: "Compatible",
			ApiType:     "chat_completions",
			BaseURL:     "https://api.openai.com/v1",
			Auth: ProviderAuth{
				Type:   "api_key",
				Header: "Authorization",
				Scheme: strptr("Bearer"),
			},
		}},
		DataSources: &DatasourceConnections{
			BigQuery: &DatasourceConnectionState{
				Enabled:          nil,
				LastProjectID:    strptr("   "),
				LastHost:         strptr("  "),
				LastWarehouseID:  strptr(" "),
				LastCheckMessage: strptr("   "),
				LastCheckedAt:    nil,
				LastCheckOk:      nil,
			},
			Databricks: &DatasourceConnectionState{
				Enabled:          nil,
				LastProjectID:    strptr("  bq-123 "),
				LastHost:         strptr("  "),
				LastWarehouseID:  strptr(" wh-7 "),
				LastCheckMessage: strptr(" "),
				LastCheckedAt:    nil,
				LastCheckOk:      nil,
			},
			Trino: &DatasourceConnectionState{
				Enabled:          nil,
				LastProjectID:    strptr("  cat-1 "),
				LastHost:         strptr(" https://trino.example.com "),
				LastWarehouseID:  strptr("  schema_1 "),
				LastCheckMessage: strptr("   "),
				LastCheckedAt:    nil,
				LastCheckOk:      nil,
			},
			Snowflake: &DatasourceConnectionState{
				Enabled:          nil,
				LastProjectID:    strptr("  sf_db "),
				LastHost:         strptr("  xy12345.us-east-1 "),
				LastWarehouseID:  strptr("  etl_wh "),
				LastCheckMessage: strptr(" "),
				LastCheckedAt:    nil,
				LastCheckOk:      nil,
			},
		},
		SnowflakeAccountIdentifier: strptr("  acme-prod "),
		SnowflakeWarehouse:         strptr("  wh_xs "),
		SnowflakeDatabase:          strptr("  analytics "),
		SnowflakeSchema:            strptr("  public "),
		SnowflakeRole:              strptr("  analyst "),
		SnowflakeUser:              strptr("  svc_user "),
	}

	out := sanitizeSettings(s)
	if out.DataSources == nil {
		t.Fatalf("DataSources should exist after sanitize")
	}
	if out.DataSources.BigQuery.Enabled == nil || *out.DataSources.BigQuery.Enabled != false {
		t.Fatalf("BigQuery.Enabled = %v, want false pointer", boolPtrValue(out.DataSources.BigQuery.Enabled))
	}
	if out.DataSources.Databricks.Enabled == nil || *out.DataSources.Databricks.Enabled != false {
		t.Fatalf("Databricks.Enabled = %v, want false pointer", boolPtrValue(out.DataSources.Databricks.Enabled))
	}
	if out.DataSources.Trino.Enabled == nil || *out.DataSources.Trino.Enabled != false {
		t.Fatalf("Trino.Enabled = %v, want false pointer", boolPtrValue(out.DataSources.Trino.Enabled))
	}
	if out.DataSources.Snowflake.Enabled == nil || *out.DataSources.Snowflake.Enabled != false {
		t.Fatalf("Snowflake.Enabled = %v, want false pointer", boolPtrValue(out.DataSources.Snowflake.Enabled))
	}
	if out.DataSources.BigQuery.LastProjectID != nil {
		t.Fatalf("BigQuery.LastProjectID = %#v, want nil", out.DataSources.BigQuery.LastProjectID)
	}
	if out.DataSources.Databricks.LastProjectID == nil || *out.DataSources.Databricks.LastProjectID != "bq-123" {
		t.Fatalf("Databricks.LastProjectID = %#v, want bq-123", out.DataSources.Databricks.LastProjectID)
	}
	if out.DataSources.Databricks.LastProjectID == nil {
		t.Fatalf("Databricks.LastProjectID should persist non-empty values")
	}
	if out.DataSources.Databricks.LastHost != nil {
		t.Fatalf("Databricks.LastHost = %#v, want nil", out.DataSources.Databricks.LastHost)
	}
	if out.DataSources.Databricks.LastWarehouseID == nil || *out.DataSources.Databricks.LastWarehouseID != "wh-7" {
		t.Fatalf("Databricks.LastWarehouseID = %#v, want wh-7", out.DataSources.Databricks.LastWarehouseID)
	}
	if out.DataSources.BigQuery.LastCheckMessage != nil {
		t.Fatalf("BigQuery.LastCheckMessage = %#v, want nil", out.DataSources.BigQuery.LastCheckMessage)
	}
	if out.DataSources.Databricks.LastCheckMessage != nil {
		t.Fatalf("Databricks.LastCheckMessage = %#v, want nil", out.DataSources.Databricks.LastCheckMessage)
	}
	if out.DataSources.Trino.LastProjectID == nil || *out.DataSources.Trino.LastProjectID != "cat-1" {
		t.Fatalf("Trino.LastProjectID = %#v, want cat-1", out.DataSources.Trino.LastProjectID)
	}
	if out.DataSources.Trino.LastHost == nil || *out.DataSources.Trino.LastHost != "https://trino.example.com" {
		t.Fatalf("Trino.LastHost = %#v, want https://trino.example.com", out.DataSources.Trino.LastHost)
	}
	if out.DataSources.Trino.LastWarehouseID == nil || *out.DataSources.Trino.LastWarehouseID != "schema_1" {
		t.Fatalf("Trino.LastWarehouseID = %#v, want schema_1", out.DataSources.Trino.LastWarehouseID)
	}
	if out.DataSources.Trino.LastCheckMessage != nil {
		t.Fatalf("Trino.LastCheckMessage = %#v, want nil", out.DataSources.Trino.LastCheckMessage)
	}
	if out.DataSources.Snowflake.LastProjectID == nil || *out.DataSources.Snowflake.LastProjectID != "sf_db" {
		t.Fatalf("Snowflake.LastProjectID = %#v, want sf_db", out.DataSources.Snowflake.LastProjectID)
	}
	if out.DataSources.Snowflake.LastHost == nil || *out.DataSources.Snowflake.LastHost != "xy12345.us-east-1" {
		t.Fatalf("Snowflake.LastHost = %#v, want xy12345.us-east-1", out.DataSources.Snowflake.LastHost)
	}
	if out.DataSources.Snowflake.LastWarehouseID == nil || *out.DataSources.Snowflake.LastWarehouseID != "etl_wh" {
		t.Fatalf("Snowflake.LastWarehouseID = %#v, want etl_wh", out.DataSources.Snowflake.LastWarehouseID)
	}
	if out.DataSources.Snowflake.LastCheckMessage != nil {
		t.Fatalf("Snowflake.LastCheckMessage = %#v, want nil", out.DataSources.Snowflake.LastCheckMessage)
	}
	if out.SnowflakeAccountIdentifier == nil || *out.SnowflakeAccountIdentifier != "acme-prod" {
		t.Fatalf("SnowflakeAccountIdentifier = %#v, want acme-prod", out.SnowflakeAccountIdentifier)
	}
	if out.SnowflakeWarehouse == nil || *out.SnowflakeWarehouse != "wh_xs" {
		t.Fatalf("SnowflakeWarehouse = %#v, want wh_xs", out.SnowflakeWarehouse)
	}
	if out.SnowflakeDatabase == nil || *out.SnowflakeDatabase != "analytics" {
		t.Fatalf("SnowflakeDatabase = %#v, want analytics", out.SnowflakeDatabase)
	}
	if out.SnowflakeSchema == nil || *out.SnowflakeSchema != "public" {
		t.Fatalf("SnowflakeSchema = %#v, want public", out.SnowflakeSchema)
	}
	if out.SnowflakeRole == nil || *out.SnowflakeRole != "analyst" {
		t.Fatalf("SnowflakeRole = %#v, want analyst", out.SnowflakeRole)
	}
	if out.SnowflakeUser == nil || *out.SnowflakeUser != "svc_user" {
		t.Fatalf("SnowflakeUser = %#v, want svc_user", out.SnowflakeUser)
	}
}

func TestSanitizeSettingsPreservesExplicitWebSearchDisable(t *testing.T) {
	disabled := false
	s := AppSettings{
		Provider: ProviderSelection{
			ProviderID: "openai",
			Model:      "gpt-4.1",
		},
		OpenAI: &OpenAIConfig{
			ApiKey:    "",
			BaseURL:   strptr("https://api.openai.com/v1"),
			WebSearch: &ProviderWebSearchConfig{Enabled: &disabled},
		},
		CompatibleProfiles: []CompatibleProfile{{
			ID:          "default",
			DisplayName: "Compatible",
			ApiType:     "chat_completions",
			BaseURL:     "https://api.openai.com/v1",
			Auth: ProviderAuth{
				Type:   "api_key",
				Header: "Authorization",
				Scheme: strptr("Bearer"),
			},
		}},
	}

	out := sanitizeSettings(s)
	if out.OpenAI == nil || out.OpenAI.WebSearch == nil || out.OpenAI.WebSearch.Enabled == nil {
		t.Fatalf("expected explicit openai web search setting to survive sanitize, got %#v", out.OpenAI)
	}
	if *out.OpenAI.WebSearch.Enabled {
		t.Fatalf("expected explicit openai web search disable to be preserved, got %#v", out.OpenAI.WebSearch)
	}
}

func TestSanitizeSettingsNormalizesBareAtlassianDomains(t *testing.T) {
	enabled := true
	in := DefaultSettings()
	in.Atlassian = &AtlassianSettings{
		JiraBaseURL:       strptr(" your-company.atlassian.net/ "),
		ConfluenceBaseURL: strptr(" your-company.atlassian.net "),
		Jira:              &AtlassianProductSettings{Enabled: &enabled},
		Confluence:        &AtlassianProductSettings{Enabled: &enabled},
	}

	out := sanitizeSettings(in)
	if out.Atlassian == nil {
		t.Fatal("Atlassian settings are nil")
	}
	if got := ptrStringValue(out.Atlassian.JiraBaseURL); got != "https://your-company.atlassian.net" {
		t.Fatalf("JiraBaseURL = %q, want normalized https URL", got)
	}
	if got := ptrStringValue(out.Atlassian.ConfluenceBaseURL); got != "https://your-company.atlassian.net/wiki" {
		t.Fatalf("ConfluenceBaseURL = %q, want normalized Confluence /wiki URL", got)
	}
	if err := validateSettings(out); err != nil {
		t.Fatalf("validateSettings returned error: %v", err)
	}
}

func TestSanitizeSettingsDerivesConfluenceFromBareJiraDomain(t *testing.T) {
	enabled := true
	in := DefaultSettings()
	in.Atlassian = &AtlassianSettings{
		JiraBaseURL: strptr("your-company.atlassian.net"),
		Jira:        &AtlassianProductSettings{Enabled: &enabled},
		Confluence:  &AtlassianProductSettings{Enabled: &enabled},
	}

	out := sanitizeSettings(in)
	if got := ptrStringValue(out.Atlassian.JiraBaseURL); got != "https://your-company.atlassian.net" {
		t.Fatalf("JiraBaseURL = %q, want normalized https URL", got)
	}
	if got := ptrStringValue(out.Atlassian.ConfluenceBaseURL); got != "https://your-company.atlassian.net/wiki" {
		t.Fatalf("ConfluenceBaseURL = %q, want derived Confluence /wiki URL", got)
	}
	if err := validateSettings(out); err != nil {
		t.Fatalf("validateSettings returned error: %v", err)
	}
}

func TestDefaultSettingsExposeOnlyImplementedNativeWebSearch(t *testing.T) {
	got := DefaultSettings()
	if got.OpenAI == nil || got.OpenAI.WebSearch == nil || got.OpenAI.WebSearch.Enabled == nil || !*got.OpenAI.WebSearch.Enabled {
		t.Fatalf("OpenAI.WebSearch should default to enabled, got %#v", got.OpenAI)
	}
	if got.Anthropic == nil || got.Anthropic.WebSearch == nil || got.Anthropic.WebSearch.Enabled == nil || !*got.Anthropic.WebSearch.Enabled {
		t.Fatalf("Anthropic.WebSearch should default to enabled, got %#v", got.Anthropic)
	}
	if got.Bedrock == nil {
		t.Fatal("Bedrock settings are nil")
	}
	if got.Bedrock.WebSearch != nil {
		t.Fatalf("Bedrock.WebSearch should not default until native hosted tools are implemented, got %#v", got.Bedrock.WebSearch)
	}
	if got.Bedrock.CodeExecution != nil {
		t.Fatalf("Bedrock.CodeExecution should not default until native hosted tools are implemented, got %#v", got.Bedrock.CodeExecution)
	}
}

func TestSanitizeSettingsBrowserControlDefaultsAndTrimsProfile(t *testing.T) {
	in := DefaultSettings()
	in.BrowserControl = &BrowserControlSettings{
		BrowserProfileDir: strptr(" browser-control-profile "),
	}

	out := sanitizeSettings(in)
	if out.BrowserControl == nil {
		t.Fatal("BrowserControl settings are nil")
	}
	if out.BrowserControl.Enabled == nil || *out.BrowserControl.Enabled {
		t.Fatalf("BrowserControl.Enabled = %#v, want false", out.BrowserControl.Enabled)
	}
	if out.BrowserControl.ShowBrowser == nil || *out.BrowserControl.ShowBrowser {
		t.Fatalf("BrowserControl.ShowBrowser = %#v, want false", out.BrowserControl.ShowBrowser)
	}
	if got := ptrStringValue(out.BrowserControl.BrowserProfileDir); got != "browser-control-profile" {
		t.Fatalf("BrowserControl.BrowserProfileDir = %q, want trimmed path", got)
	}
}

func TestSanitizeSettingsLocalComputerDefaultsAndFolders(t *testing.T) {
	in := DefaultSettings()
	mode := LocalComputerAccessMode(" Selected-Folders ")
	in.LocalComputer = &LocalComputerSettings{
		AccessMode:       &mode,
		SelectedFolders:  []string{" /Users/example/Documents ", "/Users/example/Documents", "", " /tmp "},
		LastCheckMessage: strptr(" ready "),
	}

	out := sanitizeSettings(in)
	if out.LocalComputer == nil {
		t.Fatal("LocalComputer settings are nil")
	}
	if out.LocalComputer.Enabled == nil || *out.LocalComputer.Enabled {
		t.Fatalf("LocalComputer.Enabled = %#v, want false", out.LocalComputer.Enabled)
	}
	if out.LocalComputer.AccessMode == nil || *out.LocalComputer.AccessMode != LocalComputerAccessModeSelectedFolders {
		t.Fatalf("LocalComputer.AccessMode = %#v, want selected-folders", out.LocalComputer.AccessMode)
	}
	wantFolders := []string{"/Users/example/Documents", "/tmp"}
	if len(out.LocalComputer.SelectedFolders) != len(wantFolders) {
		t.Fatalf("LocalComputer.SelectedFolders = %#v, want %#v", out.LocalComputer.SelectedFolders, wantFolders)
	}
	for i := range wantFolders {
		if out.LocalComputer.SelectedFolders[i] != wantFolders[i] {
			t.Fatalf("LocalComputer.SelectedFolders = %#v, want %#v", out.LocalComputer.SelectedFolders, wantFolders)
		}
	}
	if out.LocalComputer.LastCheckMessage == nil || *out.LocalComputer.LastCheckMessage != "ready" {
		t.Fatalf("LocalComputer.LastCheckMessage = %#v, want ready", out.LocalComputer.LastCheckMessage)
	}

	in.LocalComputer = nil
	out = sanitizeSettings(in)
	if out.LocalComputer == nil || out.LocalComputer.Enabled == nil || *out.LocalComputer.Enabled {
		t.Fatalf("nil LocalComputer settings should default disabled, got %#v", out.LocalComputer)
	}
	if out.LocalComputer.AccessMode == nil || *out.LocalComputer.AccessMode != LocalComputerAccessModeWholeComputer {
		t.Fatalf("nil LocalComputer settings should default whole-computer, got %#v", out.LocalComputer)
	}
	if len(out.LocalComputer.SelectedFolders) != 0 {
		t.Fatalf("nil LocalComputer selectedFolders = %#v, want empty", out.LocalComputer.SelectedFolders)
	}
}

func TestSanitizeSettingsSlackDefaultsAndAllowlists(t *testing.T) {
	enabled := true
	writeTools := true
	writeIdentity := " bot "
	in := DefaultSettings()
	in.SlackOAuthClientID = strptr(" slack-client-id ")
	in.Slack = &SlackSettings{
		Enabled:           &enabled,
		ReadTools:         nil,
		WriteTools:        &writeTools,
		WriteIdentity:     &writeIdentity,
		AllowedTeamIDs:    []string{" t123 ", "T123", "", "e456"},
		AllowedChannelIDs: []string{" c123 ", "C123", "", "g456"},
		LastCheckMessage:  strptr(" connected "),
	}

	out := sanitizeSettings(in)
	if out.SlackOAuthClientID == nil || *out.SlackOAuthClientID != "slack-client-id" {
		t.Fatalf("SlackOAuthClientID = %#v, want trimmed client ID", out.SlackOAuthClientID)
	}
	if out.Slack == nil {
		t.Fatalf("Slack settings are nil")
	}
	if out.Slack.Enabled == nil || !*out.Slack.Enabled {
		t.Fatalf("Slack.Enabled = %#v, want true", out.Slack.Enabled)
	}
	if out.Slack.ReadTools == nil || !*out.Slack.ReadTools {
		t.Fatalf("Slack.ReadTools = %#v, want fallback to enabled=true", out.Slack.ReadTools)
	}
	if out.Slack.WriteTools == nil || !*out.Slack.WriteTools {
		t.Fatalf("Slack.WriteTools = %#v, want explicit true", out.Slack.WriteTools)
	}
	if out.Slack.WriteIdentity == nil || *out.Slack.WriteIdentity != "bot" {
		t.Fatalf("Slack.WriteIdentity = %#v, want bot", out.Slack.WriteIdentity)
	}
	if len(out.Slack.AllowedTeamIDs) != 2 || out.Slack.AllowedTeamIDs[0] != "T123" || out.Slack.AllowedTeamIDs[1] != "E456" {
		t.Fatalf("Slack.AllowedTeamIDs = %#v, want uppercase deduped IDs", out.Slack.AllowedTeamIDs)
	}
	if len(out.Slack.AllowedChannelIDs) != 2 || out.Slack.AllowedChannelIDs[0] != "C123" || out.Slack.AllowedChannelIDs[1] != "G456" {
		t.Fatalf("Slack.AllowedChannelIDs = %#v, want uppercase deduped IDs", out.Slack.AllowedChannelIDs)
	}
	if out.Slack.LastCheckMessage == nil || *out.Slack.LastCheckMessage != "connected" {
		t.Fatalf("Slack.LastCheckMessage = %#v, want trimmed message", out.Slack.LastCheckMessage)
	}

	in.SlackOAuthClientID = strptr(" ")
	in.Slack = nil
	out = sanitizeSettings(in)
	if out.SlackOAuthClientID != nil {
		t.Fatalf("blank SlackOAuthClientID = %#v, want nil", out.SlackOAuthClientID)
	}
	if out.Slack == nil || out.Slack.Enabled == nil || *out.Slack.Enabled || out.Slack.ReadTools == nil || *out.Slack.ReadTools || out.Slack.WriteTools == nil || *out.Slack.WriteTools || out.Slack.WriteIdentity == nil || *out.Slack.WriteIdentity != "user" {
		t.Fatalf("nil Slack settings should default disabled, got %#v", out.Slack)
	}
}

func TestSanitizeSettingsArxivDefaults(t *testing.T) {
	enabled := true
	in := DefaultSettings()
	in.Arxiv = &ArxivSettings{
		Enabled:          &enabled,
		ContactEmail:     strptr("  research@example.com  "),
		LastCheckMessage: strptr(" reachable "),
	}

	out := sanitizeSettings(in)
	if out.Arxiv == nil {
		t.Fatalf("Arxiv settings are nil")
	}
	if out.Arxiv.Enabled == nil || !*out.Arxiv.Enabled {
		t.Fatalf("Arxiv.Enabled = %#v, want true", out.Arxiv.Enabled)
	}
	if out.Arxiv.ContactEmail == nil || *out.Arxiv.ContactEmail != "research@example.com" {
		t.Fatalf("Arxiv.ContactEmail = %#v, want trimmed email", out.Arxiv.ContactEmail)
	}
	if out.Arxiv.LastCheckMessage == nil || *out.Arxiv.LastCheckMessage != "reachable" {
		t.Fatalf("Arxiv.LastCheckMessage = %#v, want trimmed message", out.Arxiv.LastCheckMessage)
	}

	in.Arxiv = &ArxivSettings{ContactEmail: strptr("   ")}
	out = sanitizeSettings(in)
	if out.Arxiv == nil || out.Arxiv.Enabled == nil || *out.Arxiv.Enabled {
		t.Fatalf("Arxiv.Enabled should default disabled, got %#v", out.Arxiv)
	}
	if out.Arxiv.ContactEmail != nil {
		t.Fatalf("blank Arxiv.ContactEmail = %#v, want nil", out.Arxiv.ContactEmail)
	}

	in.Arxiv = nil
	out = sanitizeSettings(in)
	if out.Arxiv == nil || out.Arxiv.Enabled == nil || *out.Arxiv.Enabled {
		t.Fatalf("nil Arxiv settings should default disabled, got %#v", out.Arxiv)
	}

	defaults := DefaultSettings()
	if defaults.Arxiv == nil || defaults.Arxiv.Enabled == nil || *defaults.Arxiv.Enabled {
		t.Fatalf("DefaultSettings().Arxiv should default disabled, got %#v", defaults.Arxiv)
	}
}

func TestSanitizeSettingsConnectionsVisibleIDs(t *testing.T) {
	in := DefaultSettings()
	in.Connections = &ConnectionsSettings{
		VisibleIDs: []ConnectionCatalogID{" Slack ", "github", "unknown", "SLACK", " ArXiv ", "bigquery", "databricks", " Thread-Search ", " local-computer "},
	}

	out := sanitizeSettings(in)
	if out.Connections == nil {
		t.Fatalf("Connections = nil, want sanitized settings")
	}
	want := []ConnectionCatalogID{
		ConnectionCatalogSlack,
		ConnectionCatalogGitHub,
		ConnectionCatalogArxiv,
		ConnectionCatalogBigQuery,
		ConnectionCatalogDatabricks,
		ConnectionCatalogThreadSearch,
		ConnectionCatalogLocalComputer,
	}
	if len(out.Connections.VisibleIDs) != len(want) {
		t.Fatalf("Connections.visibleIds = %#v, want %#v", out.Connections.VisibleIDs, want)
	}
	for i := range want {
		if out.Connections.VisibleIDs[i] != want[i] {
			t.Fatalf("Connections.visibleIds = %#v, want %#v", out.Connections.VisibleIDs, want)
		}
	}

	in.Connections = nil
	out = sanitizeSettings(in)
	if out.Connections == nil || len(out.Connections.VisibleIDs) != 1 || out.Connections.VisibleIDs[0] != ConnectionCatalogThreadSearch {
		t.Fatalf("nil Connections should default to thread search visibleId, got %#v", out.Connections)
	}

	enabled := true
	in = DefaultSettings()
	in.Connections = &ConnectionsSettings{VisibleIDs: []ConnectionCatalogID{ConnectionCatalogThreadSearch}}
	in.GitHub = &GitHubSettings{Enabled: &enabled, ReadTools: &enabled}
	in.LocalComputer = &LocalComputerSettings{Enabled: &enabled}
	out = sanitizeSettings(in)
	want = []ConnectionCatalogID{ConnectionCatalogThreadSearch, ConnectionCatalogLocalComputer, ConnectionCatalogGitHub}
	if len(out.Connections.VisibleIDs) != len(want) {
		t.Fatalf("enabled connections should be visible, got %#v want %#v", out.Connections.VisibleIDs, want)
	}
	for i := range want {
		if out.Connections.VisibleIDs[i] != want[i] {
			t.Fatalf("enabled connections should be visible, got %#v want %#v", out.Connections.VisibleIDs, want)
		}
	}
}

func TestSanitizeSettingsMakesEnabledToolConnectionsVisible(t *testing.T) {
	enabled := true
	on := "on"
	in := DefaultSettings()
	in.Connections = &ConnectionsSettings{VisibleIDs: []ConnectionCatalogID{ConnectionCatalogThreadSearch}}
	in.ImageGeneration.Enabled = &enabled
	in.ProviderConfigs = []ProviderConfig{
		{
			ID:         "provider-1",
			ProviderID: "openai",
			Model:      "gpt-5",
			CapabilityControls: map[string]string{
				"hosted_web_search":     on,
				"hosted_code_execution": on,
			},
			WebSearch:     &ProviderWebSearchConfig{Enabled: &enabled},
			CodeExecution: &ProviderCodeExecutionConfig{Enabled: &enabled},
		},
	}

	out := sanitizeSettings(in)
	want := []ConnectionCatalogID{
		ConnectionCatalogThreadSearch,
		ConnectionCatalogImageGeneration,
		ConnectionCatalogWebSearch,
		ConnectionCatalogPythonSandbox,
	}
	if len(out.Connections.VisibleIDs) != len(want) {
		t.Fatalf("enabled tool connections should be visible, got %#v want %#v", out.Connections.VisibleIDs, want)
	}
	for i := range want {
		if out.Connections.VisibleIDs[i] != want[i] {
			t.Fatalf("enabled tool connections should be visible, got %#v want %#v", out.Connections.VisibleIDs, want)
		}
	}
}

func TestSanitizeSettingsIgnoresHostedControlsOnUnsupportedProviderSurfaces(t *testing.T) {
	enabled := true
	on := "on"
	chatCompletions := "chat_completions"
	in := DefaultSettings()
	in.Connections = &ConnectionsSettings{VisibleIDs: []ConnectionCatalogID{ConnectionCatalogThreadSearch}}
	in.ProviderConfigs = []ProviderConfig{
		{
			ID:         "compatible-chat-completions",
			ProviderID: "compatible",
			ApiType:    &chatCompletions,
			CapabilityControls: map[string]string{
				"hosted_web_search":     on,
				"hosted_code_execution": on,
			},
			WebSearch:     &ProviderWebSearchConfig{Enabled: &enabled},
			CodeExecution: &ProviderCodeExecutionConfig{Enabled: &enabled},
		},
	}

	out := sanitizeSettings(in)
	if len(out.Connections.VisibleIDs) != 1 || out.Connections.VisibleIDs[0] != ConnectionCatalogThreadSearch {
		t.Fatalf("unsupported hosted-tool surface must not resurrect connections, got %#v", out.Connections.VisibleIDs)
	}
}

func TestSanitizeSettingsTreatsAutoWebSearchAsOffAndRepairsMirrors(t *testing.T) {
	enabled := true
	disabled := false
	responses := "responses"
	in := DefaultSettings()
	in.ProviderConfigs = []ProviderConfig{
		{
			ID: "openai-auto", ProviderID: "openai", Model: "gpt-5",
			CapabilityControls: map[string]string{"hosted_web_search": "auto"},
			WebSearch:          &ProviderWebSearchConfig{Enabled: &enabled},
		},
		{
			ID: "azure-off", ProviderID: "compatible", Model: "gpt-5.5", ApiType: &responses,
			CapabilityControls: map[string]string{"hosted_web_search": "off"},
			WebSearch:          &ProviderWebSearchConfig{Enabled: &disabled},
		},
	}

	out := sanitizeSettings(in)
	for _, cfg := range out.ProviderConfigs {
		if got := cfg.CapabilityControls["hosted_web_search"]; got != "off" {
			t.Errorf("%s hosted_web_search control = %q, want off", cfg.ID, got)
		}
		if cfg.WebSearch == nil || cfg.WebSearch.Enabled == nil || *cfg.WebSearch.Enabled {
			t.Errorf("%s WebSearch = %#v, want repaired disabled mirror", cfg.ID, cfg.WebSearch)
		}
	}
}

func TestSanitizeSettingsCanonicalizesStaleWebSearchSchemasAndLegacyMirrors(t *testing.T) {
	enabled := true
	disabled := false
	responses := "responses"
	chatCompletions := "chat_completions"
	staleType := "web_search_preview"
	staleContext := "legacy"
	staleMaxUses := 99
	in := DefaultSettings()
	in.OpenAI.WebSearch = &ProviderWebSearchConfig{
		Enabled:           &enabled,
		ToolType:          &staleType,
		MaxUses:           &staleMaxUses,
		SearchContextSize: &staleContext,
		AllowedDomains:    []string{"old.example"},
	}
	in.Anthropic.WebSearch = &ProviderWebSearchConfig{
		Enabled:        &disabled,
		ToolType:       strptr("web_search_20250305"),
		MaxUses:        &staleMaxUses,
		BlockedDomains: []string{"new.example"},
		AllowedCallers: []string{"code_execution_20250522"},
	}
	in.CompatibleProfiles = []CompatibleProfile{
		{
			ID: "azure", DisplayName: "Azure", ApiType: responses,
			BaseURL: "https://example.openai.azure.com/openai/v1",
			Auth:    ProviderAuth{Type: "api_key", Header: "api-key"},
			WebSearch: &ProviderWebSearchConfig{
				Enabled: &disabled, ToolType: &staleType, MaxUses: &staleMaxUses,
			},
		},
		{
			ID: "chat", DisplayName: "Chat", ApiType: chatCompletions,
			BaseURL:   "https://api.openai.com/v1",
			Auth:      ProviderAuth{Type: "api_key", Header: "Authorization"},
			WebSearch: &ProviderWebSearchConfig{Enabled: &enabled, ToolType: &staleType},
		},
	}
	in.ProviderConfigs = []ProviderConfig{
		{
			ID: "openai", ProviderID: "openai", Model: "gpt-5",
			CapabilityControls: map[string]string{"hosted_web_search": "on"},
			WebSearch: &ProviderWebSearchConfig{
				Enabled: &enabled, ToolType: &staleType, MaxUses: &staleMaxUses,
				SearchContextSize: &staleContext, AllowedDomains: []string{"old.example"},
			},
		},
		{
			ID: "azure", ProviderID: "compatible", Model: "gpt-5.5", ApiType: &responses,
			CapabilityControls: map[string]string{"hosted_web_search": "off"},
			WebSearch:          &ProviderWebSearchConfig{Enabled: &disabled, ToolType: &staleType},
		},
		{
			ID: "anthropic", ProviderID: "anthropic", Model: "claude", ApiType: strptr("messages"),
			CapabilityControls: map[string]string{"hosted_web_search": "off"},
			WebSearch:          &ProviderWebSearchConfig{Enabled: &disabled, ToolType: strptr("web_search_20250305")},
		},
		{
			ID: "compatible-chat", ProviderID: "compatible", Model: "gpt-4", ApiType: &chatCompletions,
			CapabilityControls: map[string]string{"hosted_web_search": "on"},
			WebSearch:          &ProviderWebSearchConfig{Enabled: &enabled, ToolType: &staleType},
		},
	}

	out := sanitizeSettings(in)
	assertCanonical := func(label, providerID string, got *ProviderWebSearchConfig) {
		t.Helper()
		if got == nil || got.Enabled == nil || !*got.Enabled || got.ToolType == nil {
			t.Fatalf("%s WebSearch = %#v, want enabled canonical config", label, got)
		}
		wantType := "web_search"
		if providerID == "anthropic" {
			wantType = "web_search_20260209"
		}
		if *got.ToolType != wantType {
			t.Fatalf("%s ToolType = %q, want %q", label, *got.ToolType, wantType)
		}
		if providerID == "anthropic" {
			if got.MaxUses == nil || *got.MaxUses != 5 || len(got.AllowedCallers) != 1 || got.AllowedCallers[0] != "direct" {
				t.Fatalf("%s Anthropic WebSearch = %#v, want current native schema", label, got)
			}
		} else if got.MaxUses != nil || got.SearchContextSize != nil || len(got.AllowedDomains) > 0 || len(got.BlockedDomains) > 0 || len(got.AllowedCallers) > 0 {
			t.Fatalf("%s WebSearch retained stale fields: %#v", label, got)
		}
	}

	for i := range out.ProviderConfigs[:3] {
		assertCanonical("provider "+out.ProviderConfigs[i].ID, out.ProviderConfigs[i].ProviderID, out.ProviderConfigs[i].WebSearch)
	}
	if got := out.ProviderConfigs[3]; got.WebSearch != nil || got.CapabilityControls["hosted_web_search"] != "off" {
		t.Fatalf("unsupported provider retained stale Web Search: %#v", got)
	}
	assertCanonical("legacy OpenAI", "openai", out.OpenAI.WebSearch)
	assertCanonical("legacy Anthropic", "anthropic", out.Anthropic.WebSearch)
	assertCanonical("legacy Azure profile", "compatible", out.CompatibleProfiles[0].WebSearch)
	if out.CompatibleProfiles[1].WebSearch != nil {
		t.Fatalf("chat-completions profile retained hosted Web Search: %#v", out.CompatibleProfiles[1].WebSearch)
	}
}

func TestNewStorePermanentlyRewritesStaleWebSearchSchemasOnDisk(t *testing.T) {
	enabled := true
	responses := "responses"
	staleType := "web_search_preview"
	staleContext := "legacy"
	staleMaxUses := 99
	profileID := "azure-stale"
	in := DefaultSettings()
	in.Provider = ProviderSelection{ProviderID: "compatible", Model: "gpt-5.5", ProfileID: &profileID}
	in.ActiveProviderConfigID = &profileID
	in.CompatibleProfiles = []CompatibleProfile{
		{
			ID: profileID, DisplayName: "Azure stale", ApiType: responses,
			BaseURL: "https://example.services.ai.azure.com/openai/v1",
			Auth:    ProviderAuth{Type: "api_key", Header: "api-key"},
			WebSearch: &ProviderWebSearchConfig{
				Enabled: &enabled, ToolType: &staleType, MaxUses: &staleMaxUses,
				SearchContextSize: &staleContext, AllowedDomains: []string{"old.example"},
			},
		},
	}
	in.ProviderConfigs = []ProviderConfig{
		{
			ID: profileID, DisplayName: "Azure stale", ProviderID: "compatible", Model: "gpt-5.5",
			ApiType: &responses, APISurface: &responses,
			CapabilityControls: map[string]string{"hosted_web_search": "on"},
			WebSearch: &ProviderWebSearchConfig{
				Enabled: &enabled, ToolType: &staleType, MaxUses: &staleMaxUses,
				SearchContextSize: &staleContext, BlockedDomains: []string{"old.example"},
			},
		},
	}

	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	raw, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		t.Fatalf("marshal stale settings: %v", err)
	}
	if err := os.WriteFile(settingsPath, raw, 0o600); err != nil {
		t.Fatalf("write stale settings: %v", err)
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	got := store.Get()
	if len(got.ProviderConfigs) != 1 {
		t.Fatalf("provider config count = %d, want 1", len(got.ProviderConfigs))
	}
	assertCanonical := func(label string, cfg *ProviderWebSearchConfig) {
		t.Helper()
		if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled || cfg.ToolType == nil || *cfg.ToolType != "web_search" {
			t.Fatalf("%s = %#v, want canonical enabled web_search", label, cfg)
		}
		if cfg.MaxUses != nil || cfg.SearchContextSize != nil || len(cfg.AllowedDomains) != 0 || len(cfg.BlockedDomains) != 0 || len(cfg.AllowedCallers) != 0 {
			t.Fatalf("%s retained stale fields: %#v", label, cfg)
		}
	}
	assertCanonical("provider config", got.ProviderConfigs[0].WebSearch)
	assertCanonical("compatible mirror", got.CompatibleProfiles[0].WebSearch)

	persisted, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read migrated settings: %v", err)
	}
	for _, obsolete := range []string{"web_search_preview", `"maxUses": 99`, `"searchContextSize": "legacy"`, "old.example"} {
		if strings.Contains(string(persisted), obsolete) {
			t.Fatalf("startup migration left obsolete value %q on disk:\n%s", obsolete, persisted)
		}
	}

	// A second launch must observe the durable repair rather than performing a
	// fresh in-memory-only conversion of the original stale payload.
	if _, err := NewStore(dir); err != nil {
		t.Fatalf("second NewStore: %v", err)
	}
	persistedAgain, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings after second launch: %v", err)
	}
	if string(persistedAgain) != string(persisted) {
		t.Fatalf("canonical settings changed again on second launch")
	}
}

func TestSanitizeSettingsMigratesLegacyBedrockDefaultModelID(t *testing.T) {
	messages := "messages"
	in := DefaultSettings()
	in.Provider = ProviderSelection{ProviderID: "bedrock", Model: "claude-opus-4-6"}
	in.ProviderConfigs = []ProviderConfig{
		{ID: "legacy", ProviderID: "bedrock", Model: "claude-opus-4-6", ApiType: &messages},
		{ID: "profile", ProviderID: "bedrock", Model: "global.anthropic.claude-opus-4-6-v1", ApiType: &messages},
	}

	out := sanitizeSettings(in)
	const want = "anthropic.claude-opus-4-6-v1"
	if out.Provider.Model != want {
		t.Fatalf("provider model = %q, want %q", out.Provider.Model, want)
	}
	if out.ProviderConfigs[0].Model != want {
		t.Fatalf("legacy Bedrock config model = %q, want %q", out.ProviderConfigs[0].Model, want)
	}
	if out.ProviderConfigs[1].Model != "global.anthropic.claude-opus-4-6-v1" {
		t.Fatalf("explicit inference profile was changed: %q", out.ProviderConfigs[1].Model)
	}
}

func TestDefaultFeatureFlagsCloudCredentialsIgnoreRuntimeEnv(t *testing.T) {
	t.Setenv("AIA_SHOW_ALL_PROVIDERS", "1")

	flags := defaultFeatureFlags()
	if flags == nil {
		t.Fatalf("defaultFeatureFlags returned nil")
	}
	if !flags.Providers.OpenAI || !flags.Providers.Anthropic || !flags.Providers.Gemini {
		t.Fatalf("provider flags should still respect runtime show-all override in default build, got %#v", flags.Providers)
	}
	expectedCloudCreds := defaultShowAllProvidersEnabled()
	if flags.CloudCredentialsAWS != expectedCloudCreds {
		t.Fatalf("CloudCredentialsAWS = %v, want %v for current build profile despite runtime override", flags.CloudCredentialsAWS, expectedCloudCreds)
	}
	if flags.CloudCredentialsTrino != expectedCloudCreds {
		t.Fatalf("CloudCredentialsTrino = %v, want %v for current build profile despite runtime override", flags.CloudCredentialsTrino, expectedCloudCreds)
	}
	if flags.CloudCredentialsSnowflake != expectedCloudCreds {
		t.Fatalf("CloudCredentialsSnowflake = %v, want %v for current build profile despite runtime override", flags.CloudCredentialsSnowflake, expectedCloudCreds)
	}
	if flags.ChartEngines == nil {
		t.Fatalf("ChartEngines = nil, want defaults")
	}
	if !flags.ChartEngines.Echarts || flags.ChartEngines.Highcharts {
		t.Fatalf("ChartEngines = %#v, want echarts=true highcharts=false", flags.ChartEngines)
	}
	if flags.ChartDimensionFilters {
		t.Fatalf("ChartDimensionFilters = true, want false")
	}
}

func boolPtrValue(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}
