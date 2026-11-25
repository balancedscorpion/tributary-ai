package toolschema

import "testing"

func TestNormalizeFunctionParametersDefaultsNoArgumentToolSchema(t *testing.T) {
	got := NormalizeFunctionParameters(map[string]any{})
	if typ, _ := got["type"].(string); typ != "object" {
		t.Fatalf("type=%q, want object", typ)
	}
	if _, ok := got["properties"].(map[string]any); !ok {
		t.Fatalf("properties=%#v, want empty object", got["properties"])
	}
}

func TestNormalizeFunctionParametersDoesNotMutateInput(t *testing.T) {
	in := map[string]any{"description": "schema"}
	got := NormalizeFunctionParameters(in)
	if _, ok := in["type"]; ok {
		t.Fatalf("input was mutated: %#v", in)
	}
	if typ, _ := got["type"].(string); typ != "object" {
		t.Fatalf("type=%q, want object", typ)
	}
	if desc, _ := got["description"].(string); desc != "schema" {
		t.Fatalf("description=%q, want preserved value", desc)
	}
}
