package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type MissingToolTestInputError struct {
	ToolName               string     `json:"tool_name"`
	MissingArguments       []string   `json:"missing_arguments,omitempty"`
	MissingContext         []string   `json:"missing_context,omitempty"`
	RequiredArguments      []string   `json:"required_arguments,omitempty"`
	RequiredArgumentGroups [][]string `json:"required_argument_groups,omitempty"`
	RequiredContext        []string   `json:"required_context,omitempty"`
	InputHint              string     `json:"input_hint,omitempty"`
}

func (e *MissingToolTestInputError) Error() string {
	if e == nil {
		return "tool test input is missing"
	}
	parts := []string{}
	if len(e.MissingArguments) > 0 {
		parts = append(parts, "arguments: "+strings.Join(e.MissingArguments, ", "))
	}
	if len(e.MissingContext) > 0 {
		parts = append(parts, "context: "+strings.Join(e.MissingContext, ", "))
	}
	if len(parts) > 0 {
		return fmt.Sprintf("tool %s test requires %s", e.ToolName, strings.Join(parts, "; "))
	}
	return fmt.Sprintf("tool %s test requires additional input", e.ToolName)
}

// PrepareToolTestArgs normalizes arguments for the permissions-page tool test
// action. It never supplies business arguments for a tool; callers must provide
// those explicitly when the tool schema requires them.
func PrepareToolTestArgs(meta ToolMetadata, args json.RawMessage) (json.RawMessage, error) {
	if !meta.Test.Supported || meta.Test.Mode == ToolTestModeNone {
		return nil, fmt.Errorf("tool %s does not support testing", meta.Name)
	}
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 {
		args = json.RawMessage(`{}`)
	} else if bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("invalid test arguments: must be a JSON object")
	}

	var obj map[string]any
	if err := json.Unmarshal(args, &obj); err != nil {
		return nil, fmt.Errorf("invalid test arguments: %w", err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	missing := missingToolTestArgs(obj, meta.Test.RequiredArguments)
	if !requiredToolTestArgumentGroupsSatisfied(obj, meta.Test.RequiredArgumentGroups) {
		missing = append(missing, missingToolTestArgumentGroupLabels(meta.Test.RequiredArgumentGroups)...)
	}
	if len(missing) > 0 {
		return nil, &MissingToolTestInputError{
			ToolName:               meta.Name,
			MissingArguments:       missing,
			RequiredArguments:      append([]string(nil), meta.Test.RequiredArguments...),
			RequiredArgumentGroups: cloneStringMatrix(meta.Test.RequiredArgumentGroups),
			RequiredContext:        append([]string(nil), meta.Test.RequiredContext...),
			InputHint:              meta.Test.InputHint,
		}
	}

	if meta.Test.Mode == ToolTestModeDryRun {
		obj["dry_run"] = true
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(out), nil
}

func requiredToolTestArgumentGroupsSatisfied(args map[string]any, groups [][]string) bool {
	groups = cloneStringMatrix(groups)
	if len(groups) == 0 {
		return true
	}
	for _, group := range groups {
		satisfied := true
		for _, name := range group {
			value, ok := args[name]
			if !ok || toolTestArgMissing(value) {
				satisfied = false
				break
			}
		}
		if satisfied {
			return true
		}
	}
	return false
}

func missingToolTestArgumentGroupLabels(groups [][]string) []string {
	groups = cloneStringMatrix(groups)
	if len(groups) == 0 {
		return nil
	}
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, strings.Join(group, "|"))
	}
	return out
}

func DecodePreparedToolTestArgs(args json.RawMessage) (map[string]any, error) {
	if len(bytes.TrimSpace(args)) == 0 {
		return map[string]any{}, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(args, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("prepared test arguments must be an object")
	}
	return obj, nil
}

func missingToolTestArgs(args map[string]any, required []string) []string {
	missing := []string{}
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		value, ok := args[name]
		if !ok || toolTestArgMissing(value) {
			missing = append(missing, name)
		}
	}
	return missing
}

func toolTestArgMissing(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(typed) == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		return len(typed) == 0
	default:
		return false
	}
}
