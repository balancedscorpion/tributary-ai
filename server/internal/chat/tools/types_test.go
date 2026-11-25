package tools

import "testing"

func TestNormalizeOutputMetadataPromotesNestedResultMetadata(t *testing.T) {
	out := NormalizeOutputMetadata(Output{
		OK:           true,
		ResultFormat: ResultFormatJSON,
		Result: map[string]any{
			"rows": []map[string]any{{"value": 1}},
			"result_metadata": map[string]any{
				"kind":        "table",
				"integration": "builtin",
			},
		},
	})
	if out.ResultMetadata["kind"] != "table" || out.ResultMetadata["integration"] != "builtin" {
		t.Fatalf("ResultMetadata=%#v, want nested metadata promoted", out.ResultMetadata)
	}
	result := out.Result.(map[string]any)
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("result_metadata should be removed from result payload: %#v", result)
	}
}

func TestNormalizeOutputMetadataKeepsExplicitResultMetadataAuthoritative(t *testing.T) {
	out := NormalizeOutputMetadata(Output{
		OK:           true,
		ResultFormat: ResultFormatJSON,
		Result: map[string]any{
			"result_metadata": map[string]any{"kind": "nested", "integration": "builtin"},
		},
		ResultMetadata: map[string]any{"kind": "explicit"},
	})
	if out.ResultMetadata["kind"] != "explicit" {
		t.Fatalf("ResultMetadata=%#v, want explicit metadata to win", out.ResultMetadata)
	}
	if out.ResultMetadata["integration"] != "builtin" {
		t.Fatalf("ResultMetadata=%#v, want nested metadata retained when explicit omits it", out.ResultMetadata)
	}
	result := out.Result.(map[string]any)
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("result_metadata should be removed from result payload: %#v", result)
	}
}
