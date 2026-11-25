package httpapi

import (
	"testing"
)

func TestParseRunDataSources(t *testing.T) {
	t.Parallel()

	t.Run("parses enabled flags", func(t *testing.T) {
		t.Parallel()

		parsed := parseRunDataSources(map[string]any{
			"bigquery": map[string]any{
				"enabled":     true,
				"lastCheckOk": false,
			},
			"databricks": map[string]any{
				"enabled": false,
			},
		})
		if parsed == nil {
			t.Fatalf("expected parsed datasource settings, got nil")
		}
		if parsed.BigQuery == nil || parsed.Databricks == nil {
			t.Fatalf("expected both datasource configs, got %#v", parsed)
		}
		if parsed.BigQuery.Enabled == nil || !*parsed.BigQuery.Enabled {
			t.Fatalf("expected bigquery enabled=true, got %#v", parsed.BigQuery)
		}
		if parsed.Databricks.Enabled == nil || *parsed.Databricks.Enabled {
			t.Fatalf("expected databricks enabled=false, got %#v", parsed.Databricks)
		}
		if parsed.BigQuery.LastCheckOk == nil {
			t.Fatalf("expected lastCheckOk to remain in parsed payload")
		}
		if parsed.BigQuery.LastCheckOk == nil || *parsed.BigQuery.LastCheckOk != false {
			t.Fatalf("expected lastCheckOk=false, got %#v", parsed.BigQuery.LastCheckOk)
		}
	})

	t.Run("returns nil for empty object", func(t *testing.T) {
		t.Parallel()

		parsed := parseRunDataSources(map[string]any{})
		if parsed != nil {
			t.Fatalf("expected nil, got %#v", parsed)
		}
	})

	t.Run("returns nil for unrelated payload", func(t *testing.T) {
		t.Parallel()

		parsed := parseRunDataSources(map[string]any{
			"provider": "openai",
			"model":    "gpt-4o",
		})
		if parsed != nil {
			t.Fatalf("expected nil, got %#v", parsed)
		}
	})

	t.Run("normalizes partial payload", func(t *testing.T) {
		t.Parallel()

		parsed := parseRunDataSources(map[string]any{
			"bigquery": map[string]any{},
		})
		if parsed != nil {
			t.Fatalf("expected nil when only missing enabled flags, got %#v", parsed)
		}

		parsed = parseRunDataSources(map[string]any{
			"bigquery": map[string]any{"enabled": true},
		})
		if parsed == nil || parsed.BigQuery == nil {
			t.Fatalf("expected parsed bigquery payload, got %#v", parsed)
		}
		if parsed.BigQuery.Enabled == nil || !*parsed.BigQuery.Enabled {
			t.Fatalf("expected bigquery enabled=true, got %#v", parsed)
		}
		if parsed.Databricks != nil {
			t.Fatalf("did not expect databricks payload, got %#v", parsed)
		}
	})

	t.Run("preserves snowflake raw overrides", func(t *testing.T) {
		t.Parallel()

		raw := parseRunDataSourceOverrides(map[string]any{
			"snowflake": map[string]any{
				"enabled":     true,
				"lastCheckOk": false,
				"warehouse":   "WH_XS",
			},
		})
		if raw == nil {
			t.Fatalf("expected raw datasource overrides, got nil")
		}
		sf, ok := raw["snowflake"].(map[string]any)
		if !ok {
			t.Fatalf("expected snowflake object, got %#v", raw["snowflake"])
		}
		if enabled, ok := sf["enabled"].(bool); !ok || !enabled {
			t.Fatalf("expected snowflake enabled=true, got %#v", sf["enabled"])
		}
		if warehouse, ok := sf["warehouse"].(string); !ok || warehouse != "WH_XS" {
			t.Fatalf("expected snowflake warehouse override preserved, got %#v", sf["warehouse"])
		}
	})
}
