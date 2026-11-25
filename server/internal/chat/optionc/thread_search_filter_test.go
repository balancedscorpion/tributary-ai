package optionc

import (
	"testing"

	"ai-analyst-local/internal/chat/types"
)

func TestFilterToolsByThreadSearchEnabled(t *testing.T) {
	defs := []types.ToolDefinition{
		{Function: types.ToolFunctionDefinition{Name: "list_files"}},
		{Function: types.ToolFunctionDefinition{Name: "search_threads"}},
		{Function: types.ToolFunctionDefinition{Name: "list_messages"}},
		{Function: types.ToolFunctionDefinition{Name: "rlm_exec"}},
	}

	disabled := filterToolsByThreadSearchEnabled(defs, false)
	if len(disabled) != 1 || disabled[0].Function.Name != "list_files" {
		t.Fatalf("disabled filter = %#v, want only list_files", disabled)
	}

	enabled := filterToolsByThreadSearchEnabled(defs, true)
	if len(enabled) != len(defs) {
		t.Fatalf("enabled filter returned %d defs, want %d", len(enabled), len(defs))
	}
	if isThreadSearchToolAllowedForRun("search_threads", false) {
		t.Fatal("search_threads should be blocked when Search Threads is disabled")
	}
	if !isThreadSearchToolAllowedForRun("list_files", false) {
		t.Fatal("non-thread-search tools should be allowed")
	}
}
