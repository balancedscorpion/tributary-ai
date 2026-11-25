package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunQueryToolsAcceptDescriptionArgument(t *testing.T) {
	args, err := json.Marshal(map[string]any{
		"sql":         "SELECT 1",
		"description": "Smoke-test query description",
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	tests := []struct {
		name    string
		tool    Tool
		exec    ExecContext
		wantErr string
	}{
		{
			name:    "bigquery",
			tool:    newRunBigQueryQueryTool(),
			wantErr: "BigQuery is not configured.",
		},
		{
			name:    "databricks",
			tool:    newRunDatabricksQueryTool(),
			wantErr: "Databricks is not configured.",
		},
		{
			name:    "trino",
			tool:    newRunTrinoQueryTool(),
			wantErr: "Trino is not configured.",
		},
		{
			name:    "snowflake",
			tool:    newRunSnowflakeQueryTool(),
			wantErr: "Snowflake is not configured.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, execErr := tt.tool.Execute(context.Background(), tt.exec, args)
			if execErr != nil {
				t.Fatalf("execute: %v", execErr)
			}
			if out.OK {
				t.Fatalf("expected tool call to fail due to missing backend config")
			}

			result, ok := out.Result.(map[string]any)
			if !ok {
				t.Fatalf("result type = %T, want map[string]any", out.Result)
			}
			got := strings.TrimSpace(anyToString(result["error"]))
			if got == "Invalid JSON arguments." {
				t.Fatalf("description argument was rejected by strict decode: %q", got)
			}
			if got != tt.wantErr {
				t.Fatalf("error = %q, want %q", got, tt.wantErr)
			}
		})
	}
}
