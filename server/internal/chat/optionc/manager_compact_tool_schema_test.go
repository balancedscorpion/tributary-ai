package optionc

import "testing"

func TestCompactToolSchemaPreservesDescriptionPropertyName(t *testing.T) {
	compactedAny := compactToolSchema(map[string]any{
		"type":        "object",
		"description": "top-level tool schema description",
		"properties": map[string]any{
			"sql": map[string]any{
				"type":        "string",
				"description": "SQL query text.",
			},
			"description": map[string]any{
				"type":        "string",
				"description": "Natural language query summary.",
			},
		},
		"required": []any{"sql"},
	})

	compacted, ok := compactedAny.(map[string]any)
	if !ok {
		t.Fatalf("compacted schema type = %T, want map[string]any", compactedAny)
	}
	if _, exists := compacted["description"]; exists {
		t.Fatalf("top-level schema description should be compacted out")
	}

	propsAny, ok := compacted["properties"]
	if !ok {
		t.Fatalf("compacted schema missing properties")
	}
	props, ok := propsAny.(map[string]any)
	if !ok {
		t.Fatalf("properties type = %T, want map[string]any", propsAny)
	}

	descPropAny, ok := props["description"]
	if !ok {
		t.Fatalf("parameter named description should be preserved")
	}
	descProp, ok := descPropAny.(map[string]any)
	if !ok {
		t.Fatalf("description property schema type = %T, want map[string]any", descPropAny)
	}
	if _, exists := descProp["description"]; exists {
		t.Fatalf("nested schema metadata description should be compacted out")
	}
}
