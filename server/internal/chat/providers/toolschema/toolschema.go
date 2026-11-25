package toolschema

import "strings"

// NormalizeFunctionParameters returns a valid object JSON schema for model tool
// definitions, including no-argument tools that historically used an empty map.
func NormalizeFunctionParameters(params map[string]any) map[string]any {
	out := make(map[string]any, len(params)+2)
	for key, value := range params {
		out[key] = value
	}
	if strings.TrimSpace(stringValue(out["type"])) == "" {
		out["type"] = "object"
	}
	if _, ok := out["properties"]; !ok {
		out["properties"] = map[string]any{}
	}
	return out
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
