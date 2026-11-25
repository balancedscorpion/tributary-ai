package optionc

import (
	"encoding/json"
	"testing"

	"ai-analyst-local/internal/chat/types"
)

func TestMonotonicToolLoopGuardBlocksSameCallAfterCompaction(t *testing.T) {
	factory := NewMonotonicToolLoopGuardFactory(nil)
	guard := factory.NewRunGuard()

	call := types.ToolCall{
		ID:   "tc-1",
		Name: "read_file",
		Args: map[string]any{
			"path":      "data/huge.csv",
			"max_bytes": 262144,
		},
	}
	if _, blocked := guard.BeforeCall(call); blocked {
		t.Fatalf("first call unexpectedly blocked")
	}
	msg := types.ChatMessage{
		Role:       types.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
		Content: ptrString(mustEnvelopeJSON(map[string]any{
			"tool_name":       call.Name,
			"tool_call_id":    call.ID,
			"result":          map[string]any{"bytes_returned": 262144},
			"result_metadata": map[string]any{},
		})),
	}
	guard.RecordToolResult(call, msg)
	guard.MarkLatestDispatchCompacted([]types.ChatMessage{msg})

	repeatCall := types.ToolCall{
		ID:   "tc-2",
		Name: call.Name,
		Args: call.Args,
	}
	blockedOut, blocked := guard.BeforeCall(repeatCall)
	if !blocked {
		t.Fatalf("expected identical call repeat to be blocked after compaction")
	}
	m, ok := blockedOut.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected JSON map output, got %#v", blockedOut.Result)
	}
	if !parseBoolValue(m["loop_guard_triggered"]) {
		t.Fatalf("expected loop_guard_triggered=true, got %#v", m)
	}
}

func TestMonotonicToolLoopGuardAllowsChangedArgsAfterCompaction(t *testing.T) {
	factory := NewMonotonicToolLoopGuardFactory(nil)
	guard := factory.NewRunGuard()

	call := types.ToolCall{
		ID:   "tc-1",
		Name: "read_file",
		Args: map[string]any{
			"path":      "data/huge.csv",
			"max_bytes": 262144,
		},
	}
	msg := types.ChatMessage{
		Role:       types.RoleTool,
		Name:       call.Name,
		ToolCallID: call.ID,
		Content: ptrString(mustEnvelopeJSON(map[string]any{
			"tool_name":       call.Name,
			"tool_call_id":    call.ID,
			"result":          map[string]any{"bytes_returned": 262144},
			"result_metadata": map[string]any{},
		})),
	}
	guard.RecordToolResult(call, msg)
	guard.MarkLatestDispatchCompacted([]types.ChatMessage{msg})

	changedCall := types.ToolCall{
		ID:   "tc-2",
		Name: call.Name,
		Args: map[string]any{
			"path":      "data/huge.csv",
			"max_bytes": 32768,
		},
	}
	if _, blocked := guard.BeforeCall(changedCall); blocked {
		t.Fatalf("changed bounded call should not be blocked")
	}
}

func TestMonotonicToolLoopGuardBlocksNoProgressRepeats(t *testing.T) {
	factory := NewMonotonicToolLoopGuardFactory(&MonotonicToolLoopGuardConfig{MaxNoProgressRepeats: 1})
	guard := factory.NewRunGuard()

	call1 := types.ToolCall{
		ID:   "tc-1",
		Name: "sample_rows",
		Args: map[string]any{
			"path":  "data/huge.csv",
			"count": 1000,
		},
	}
	call2 := types.ToolCall{
		ID:   "tc-2",
		Name: call1.Name,
		Args: call1.Args,
	}
	msg1 := types.ChatMessage{
		Role:       types.RoleTool,
		Name:       call1.Name,
		ToolCallID: call1.ID,
		Content: ptrString(mustEnvelopeJSON(map[string]any{
			"tool_name":    call1.Name,
			"tool_call_id": call1.ID,
			"result": map[string]any{
				"has_more":        true,
				"next_page_token": "100",
				"offset":          0,
			},
			"result_metadata": map[string]any{},
		})),
	}
	msg2 := types.ChatMessage{
		Role:       types.RoleTool,
		Name:       call2.Name,
		ToolCallID: call2.ID,
		Content: ptrString(mustEnvelopeJSON(map[string]any{
			"tool_name":    call2.Name,
			"tool_call_id": call2.ID,
			"result": map[string]any{
				"has_more":        true,
				"next_page_token": "100",
				"offset":          0,
			},
			"result_metadata": map[string]any{},
		})),
	}
	guard.RecordToolResult(call1, msg1)
	guard.RecordToolResult(call2, msg2)

	call3 := types.ToolCall{
		ID:   "tc-3",
		Name: call1.Name,
		Args: call1.Args,
	}
	if _, blocked := guard.BeforeCall(call3); !blocked {
		t.Fatalf("expected repeated no-progress call to be blocked")
	}
}

// Async query polling repeats the exact same call (same job_id/owner_token)
// until the job completes; the guard must never block those polls while the
// job is still active, or the run can never retrieve results.
func TestMonotonicToolLoopGuardAllowsActiveAsyncQueryPolling(t *testing.T) {
	factory := NewMonotonicToolLoopGuardFactory(nil)
	guard := factory.NewRunGuard()

	args := map[string]any{
		"job_id":      "job-123",
		"owner_token": "tok-abc",
	}
	for i := 0; i < 6; i++ {
		call := types.ToolCall{ID: "tc-status", Name: "check_query_status", Args: args}
		if _, blocked := guard.BeforeCall(call); blocked {
			t.Fatalf("status poll %d unexpectedly blocked while job is running", i+1)
		}
		guard.RecordToolResult(call, types.ChatMessage{
			Role:       types.RoleTool,
			Name:       call.Name,
			ToolCallID: call.ID,
			Content: ptrString(mustEnvelopeJSON(map[string]any{
				"tool_name":    call.Name,
				"tool_call_id": call.ID,
				"result": map[string]any{
					"status":     "running",
					"job_id":     "job-123",
					"elapsed_ms": 1000 * (i + 1),
				},
				"result_metadata": map[string]any{},
			})),
		})
	}

	// Premature get_query_results retries (retryable=true) must not stall either.
	for i := 0; i < 4; i++ {
		call := types.ToolCall{ID: "tc-results", Name: "get_query_results", Args: args}
		if _, blocked := guard.BeforeCall(call); blocked {
			t.Fatalf("retryable get_query_results %d unexpectedly blocked", i+1)
		}
		guard.RecordToolResult(call, types.ChatMessage{
			Role:       types.RoleTool,
			Name:       call.Name,
			ToolCallID: call.ID,
			Content: ptrString(mustEnvelopeJSON(map[string]any{
				"tool_name":    call.Name,
				"tool_call_id": call.ID,
				"result": map[string]any{
					"status":    "running",
					"retryable": true,
					"job_id":    "job-123",
				},
				"result_metadata": map[string]any{},
			})),
		})
	}

	if _, blocked := guard.BeforeCall(types.ToolCall{ID: "tc-final", Name: "get_query_results", Args: args}); blocked {
		t.Fatalf("final get_query_results after completion unexpectedly blocked")
	}
}

// Provider-flavoured poll envelopes don't all use lowercase `status`:
// BigQuery-style payloads report uppercase job states under `state`
// (PENDING/RUNNING) and Databricks statement payloads do the same. The
// active-poll exemption must recognise those, not just the generic registry
// shape, or provider-specific status tools get blocked mid-flight.
func TestMonotonicToolLoopGuardAllowsProviderStyleStatusEnvelopes(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		result   map[string]any
	}{
		{
			name:     "bigquery uppercase state",
			toolName: "check_bigquery_query_status",
			result: map[string]any{
				"state":  "RUNNING",
				"job_id": "bq-job-1",
			},
		},
		{
			name:     "bigquery pending state",
			toolName: "check_query_status",
			result: map[string]any{
				"state":  "PENDING",
				"job_id": "bq-job-2",
			},
		},
		{
			name:     "databricks statement state",
			toolName: "get_databricks_query_status",
			result: map[string]any{
				"status":       "PENDING",
				"statement_id": "stmt-1",
			},
		},
		{
			name:     "job_state key",
			toolName: "check_query_status",
			result: map[string]any{
				"job_state": "in_progress",
				"job_id":    "job-x",
			},
		},
		{
			name:     "durable profiling state",
			toolName: "check_query_status",
			result: map[string]any{
				"status": "profiling",
				"job_id": "job-profile",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factory := NewMonotonicToolLoopGuardFactory(nil)
			guard := factory.NewRunGuard()
			args := map[string]any{"job_id": "j", "owner_token": "t"}
			for i := 0; i < 5; i++ {
				call := types.ToolCall{ID: "tc", Name: tc.toolName, Args: args}
				if _, blocked := guard.BeforeCall(call); blocked {
					t.Fatalf("%s: poll %d unexpectedly blocked", tc.name, i+1)
				}
				guard.RecordToolResult(call, types.ChatMessage{
					Role:       types.RoleTool,
					Name:       call.Name,
					ToolCallID: call.ID,
					Content: ptrString(mustEnvelopeJSON(map[string]any{
						"tool_name":       call.Name,
						"tool_call_id":    call.ID,
						"result":          tc.result,
						"result_metadata": map[string]any{},
					})),
				})
			}
		})
	}
}

// Once the job is terminal, identical repeats stop being exempt: stall
// accounting resumes so a model spinning on the same completed-results call
// is still caught by the guard.
func TestMonotonicToolLoopGuardStillBlocksTerminalQueryRepeats(t *testing.T) {
	factory := NewMonotonicToolLoopGuardFactory(nil)
	guard := factory.NewRunGuard()

	args := map[string]any{
		"job_id":      "job-456",
		"owner_token": "tok-def",
	}
	record := func(id string) {
		call := types.ToolCall{ID: id, Name: "check_query_status", Args: args}
		guard.RecordToolResult(call, types.ChatMessage{
			Role:       types.RoleTool,
			Name:       call.Name,
			ToolCallID: call.ID,
			Content: ptrString(mustEnvelopeJSON(map[string]any{
				"tool_name":    call.Name,
				"tool_call_id": call.ID,
				"result": map[string]any{
					"status": "completed",
					"job_id": "job-456",
				},
				"query_metadata":  map[string]any{"job_id": "job-456"},
				"result_metadata": map[string]any{},
			})),
		})
	}
	record("tc-1")
	record("tc-2")
	record("tc-3")

	if _, blocked := guard.BeforeCall(types.ToolCall{ID: "tc-4", Name: "check_query_status", Args: args}); !blocked {
		t.Fatalf("expected repeated identical post-completion status checks to be blocked")
	}
}

func mustEnvelopeJSON(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}
