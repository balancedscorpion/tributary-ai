package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/engine"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/permissions"
)

const (
	localSearchAgentPhaseStarted   = "local_search_agent_started"
	localSearchAgentPhaseListing   = "local_search_agent_listing"
	localSearchAgentPhaseSearching = "local_search_agent_searching"
	localSearchAgentPhaseReading   = "local_search_agent_reading"
	localSearchAgentPhaseRanking   = "local_search_agent_ranking"
	localSearchAgentPhaseCompleted = "local_search_agent_completed"
	localSearchAgentPhaseFailed    = "local_search_agent_failed"

	localSearchAgentMaxIterations = 8
	localSearchNameProbeTimeout   = 8 * time.Second
	localSearchAgentRunTimeout    = 12 * time.Second
	localSearchFallbackTimeout    = 22 * time.Second
	localSearchOverallTimeout     = 45 * time.Second
)

type localSearchAgentJob struct {
	ID          string    `json:"id"`
	ThreadID    string    `json:"thread_id"`
	RunID       string    `json:"run_id"`
	ToolCallID  string    `json:"tool_call_id"`
	Status      string    `json:"status"`
	Query       string    `json:"query"`
	GrantID     string    `json:"grant_id,omitempty"`
	LocalRoot   string    `json:"local_root,omitempty"`
	LocalKind   string    `json:"local_kind,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	FilesCount  int       `json:"files_count,omitempty"`
	Error       string    `json:"error,omitempty"`
	Result      map[string]any
}

type LocalSearchJobFilter struct {
	ThreadID string
	RunID    string
	Limit    int
}

type LocalSearchJobStatus struct {
	ID          string         `json:"id"`
	ThreadID    string         `json:"thread_id"`
	RunID       string         `json:"run_id"`
	ToolCallID  string         `json:"tool_call_id,omitempty"`
	Status      string         `json:"status"`
	Query       string         `json:"query"`
	GrantID     string         `json:"grant_id,omitempty"`
	LocalRoot   string         `json:"local_root,omitempty"`
	LocalKind   string         `json:"local_kind,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	FilesCount  int            `json:"files_count,omitempty"`
	Error       string         `json:"error,omitempty"`
	Result      map[string]any `json:"result,omitempty"`
}

type localSearchAgentService struct {
	registry        *tools.Registry
	providerFactory func(context.Context) (types.Provider, error)
}

type localSearchAgentScope struct {
	JobID                string
	ThreadID             string
	RunID                string
	ToolCallID           string
	Query                string
	Model                string
	LocationHint         string
	Path                 string
	FileTypes            []string
	MaxResults           int
	LocalRoot            string
	LocalKind            string
	GrantID              string
	AgentGrantID         string
	Grants               []localSearchAgentGrant
	DisableCommandSearch bool
}

type localSearchAgentGrant struct {
	GrantID   string
	LocalRoot string
	LocalKind string
}

type localSearchAgentObservation struct {
	ToolName string
	Output   tools.Output
}

type localSearchAgentObservations struct {
	items []localSearchAgentObservation
}

func (m *Manager) dispatchConfiguredLocalFileSearch(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	args json.RawMessage,
	execCtx tools.ExecContext,
	progressFn func(map[string]any),
) (tools.Output, bool) {
	scope, ok := parseConfiguredLocalSearchScope(args)
	if !ok {
		return tools.Output{}, false
	}
	if m == nil || m.tools == nil {
		return tools.Output{}, false
	}
	scope.JobID = "local_search_" + types.NewUUID()
	scope.ThreadID = thread.ID
	scope.RunID = run.ID
	scope.ToolCallID = strings.TrimSpace(call.ID)
	scope.Model = strings.TrimSpace(run.Model)
	if len(scope.Grants) == 0 && strings.TrimSpace(scope.GrantID) == "" {
		scope.GrantID = m.resolveConfiguredLocalSearchGrantID(scope)
	}
	scope = normalizeLocalSearchAgentConfiguredGrants(scope)
	scope.DisableCommandSearch = !m.localSearchCommandEnabled(ctx, thread.ID)

	m.recordLocalSearchJob(ctx, localSearchAgentJob{
		ID:         scope.JobID,
		ThreadID:   thread.ID,
		RunID:      run.ID,
		ToolCallID: strings.TrimSpace(call.ID),
		Status:     "running",
		Query:      scope.Query,
		GrantID:    scope.GrantID,
		LocalRoot:  scope.LocalRoot,
		LocalKind:  scope.LocalKind,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	})
	if progressFn != nil {
		progressFn(map[string]any{
			"phase":    localSearchAgentPhaseStarted,
			"job_id":   scope.JobID,
			"grant_id": scope.GrantID,
			"message":  "Starting local search agent",
			"pct":      5,
		})
	}

	var out tools.Output
	var err error
	searchCtx, searchCancel := context.WithTimeout(ctx, localSearchOverallTimeout)
	defer searchCancel()
	// Most local lookups are filename requests. Give this inexpensive probe its
	// own budget so it cannot consume the agent and deterministic-fallback time.
	probeCtx, probeCancel := context.WithTimeout(searchCtx, localSearchNameProbeTimeout)
	out, err = m.runConfiguredLocalFileSearch(probeCtx, scope, execCtx, progressFn, "name")
	probeCancel()
	if err == nil && localSearchOutputHasFiles(out) {
		m.finishLocalSearchJob(ctx, scope.JobID, "completed", "", localSearchJobResultMap(out))
		return out, true
	}
	if m.localSearchAgent != nil {
		agentCtx, cancel := context.WithTimeout(searchCtx, localSearchAgentRunTimeout)
		out, err = m.localSearchAgent.run(agentCtx, scope, execCtx, progressFn)
		cancel()
		if err != nil && progressFn != nil {
			progressFn(map[string]any{
				"phase":   localSearchAgentPhaseSearching,
				"job_id":  scope.JobID,
				"message": "Search agent did not complete; running the deterministic local search fallback within the remaining deadline",
				"pct":     35,
			})
		}
	}
	if m.localSearchAgent == nil || err != nil {
		fallbackCtx, fallbackCancel := context.WithTimeout(searchCtx, localSearchFallbackTimeout)
		out, err = m.runConfiguredLocalFileSearch(fallbackCtx, scope, execCtx, progressFn, "auto")
		fallbackCancel()
	}
	if err != nil {
		if localSearchDeadlineExceeded(err) {
			out = localSearchDeadlineOutput(scope)
			m.finishLocalSearchJob(ctx, scope.JobID, "completed", "", localSearchJobResultMap(out))
			if progressFn != nil {
				progressFn(map[string]any{
					"phase":   localSearchAgentPhaseCompleted,
					"job_id":  scope.JobID,
					"message": "Local file search reached its bounded deadline; a narrower retry remains available",
					"pct":     100,
				})
			}
			return out, true
		}
		m.finishLocalSearchJob(ctx, scope.JobID, "failed", err.Error(), nil)
		if progressFn != nil {
			progressFn(map[string]any{
				"phase":   localSearchAgentPhaseFailed,
				"job_id":  scope.JobID,
				"message": err.Error(),
				"pct":     100,
			})
		}
		return tools.JSONError(err.Error()), true
	}
	m.finishLocalSearchJob(ctx, scope.JobID, "completed", "", localSearchJobResultMap(out))
	return out, true
}

func localSearchDeadlineExceeded(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	// Tool envelopes historically flattened context errors into JSON strings.
	// Recognize that stable text at this orchestration boundary so older/fallback
	// implementations still produce a bounded partial result instead of leaking
	// the transport-level error to the model.
	return strings.Contains(strings.ToLower(strings.TrimSpace(err.Error())), "context deadline exceeded")
}

func localSearchDeadlineOutput(scope localSearchAgentScope) tools.Output {
	out := buildLocalSearchAgentOutput(
		scope,
		&localSearchAgentObservations{},
		"The bounded local search completed without a full scan. Retry with a narrower location_hint, path, file type, or filename when more coverage is needed.",
		nil,
	)
	result, _ := out.Result.(map[string]any)
	if result == nil {
		result = map[string]any{}
		out.Result = result
	}
	result["status"] = "partial"
	result["timed_out"] = true
	result["truncated"] = true
	result["retryable"] = true
	result["instructions"] = "Continue the task by retrying file_search with a narrower location_hint, path, file_types filter, or more specific filename. Do not abandon the search."
	if out.ResultMetadata == nil {
		out.ResultMetadata = map[string]any{}
	}
	out.ResultMetadata["timed_out"] = true
	out.ResultMetadata["truncated"] = true
	out.ResultMetadata["retryable"] = true
	return out
}

func (m *Manager) runConfiguredLocalFileSearch(
	ctx context.Context,
	scope localSearchAgentScope,
	execCtx tools.ExecContext,
	progressFn func(map[string]any),
	searchMode string,
) (tools.Output, error) {
	if m == nil || m.tools == nil {
		return tools.Output{}, errors.New("local file search tools are not configured")
	}
	searchTool, ok := m.tools.Get("local_search_in_grant")
	if !ok {
		return tools.Output{}, errors.New("local_search_in_grant is not configured")
	}
	grants := localSearchAgentConfiguredGrants(scope)
	if len(grants) == 0 {
		return tools.JSONError("configured Local Computer grant is not available"), nil
	}
	observations := &localSearchAgentObservations{}
	execCtx.PermissionApproved = true
	failures := []string{}
	successes := 0
	for index, grant := range grants {
		args := map[string]any{
			"grant_id":        strings.TrimSpace(grant.GrantID),
			"local_root":      grant.LocalRoot,
			"local_kind":      grant.LocalKind,
			"query":           scope.Query,
			"max_results":     scope.MaxResults,
			"portable_search": scope.DisableCommandSearch,
			"search_mode":     searchMode,
		}
		if strings.TrimSpace(scope.Path) != "" {
			args["relative_path"] = scope.Path
		}
		if len(scope.FileTypes) > 0 {
			args["file_types"] = scope.FileTypes
		}
		if progressFn != nil {
			progressFn(map[string]any{
				"phase":       localSearchAgentPhaseSearching,
				"job_id":      scope.JobID,
				"tool_name":   "local_search_in_grant",
				"grant_id":    grant.GrantID,
				"message":     "Searching configured Local Computer access",
				"pct":         15 + ((index + 1) * 70 / maxInt(len(grants), 1)),
				"roots_done":  index + 1,
				"roots_total": len(grants),
			})
		}
		b, err := json.Marshal(args)
		if err != nil {
			return tools.Output{}, err
		}
		out, err := searchTool.Execute(ctx, execCtx, b)
		if err != nil {
			return tools.Output{}, err
		}
		observations.items = append(observations.items, localSearchAgentObservation{
			ToolName: "local_search_in_grant",
			Output:   out,
		})
		if !out.OK {
			failures = append(failures, localSearchAgentOutputError(out))
			continue
		}
		successes++
	}
	if successes == 0 && len(failures) > 0 {
		return tools.Output{}, errors.New(strings.Join(dedupeStringSlice(failures), "; "))
	}
	if progressFn != nil {
		progressFn(map[string]any{
			"phase":   localSearchAgentPhaseCompleted,
			"job_id":  scope.JobID,
			"message": "Local file search completed",
			"pct":     100,
		})
	}
	out := buildLocalSearchAgentOutput(scope, observations, "Searched configured Local Computer access directly.", nil)
	if len(failures) > 0 {
		if result, ok := out.Result.(map[string]any); ok {
			result["search_errors"] = dedupeStringSlice(failures)
		}
		if out.ResultMetadata != nil {
			out.ResultMetadata["partial_failure"] = true
		}
	}
	return out, nil
}

func localSearchOutputHasFiles(out tools.Output) bool {
	if !out.OK {
		return false
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		return false
	}
	files, ok := result["files"].([]map[string]any)
	return ok && len(files) > 0
}

func localSearchAgentOutputError(out tools.Output) string {
	if result, ok := out.Result.(map[string]any); ok {
		if msg := firstStringMapValue(result, "error", "message", "detail"); msg != "" {
			return msg
		}
	}
	return "local search failed"
}

func (s *localSearchAgentService) run(
	ctx context.Context,
	scope localSearchAgentScope,
	execCtx tools.ExecContext,
	progressFn func(map[string]any),
) (tools.Output, error) {
	if s == nil || s.registry == nil {
		return tools.Output{}, errors.New("local search agent is not configured")
	}
	if s.providerFactory == nil {
		return tools.Output{}, errors.New("local search agent provider is not configured")
	}
	attr, _ := usage.AttributionFromContext(ctx)
	attr.RunID = strings.TrimSpace(scope.RunID)
	attr.ThreadID = strings.TrimSpace(scope.ThreadID)
	attr.JobID = strings.TrimSpace(scope.JobID)
	attr.Operation = usage.OperationLocalSearchAgent
	ctx = usage.WithAttribution(ctx, attr)
	observations := &localSearchAgentObservations{}
	finalText := ""
	var finalMeta *types.ResponseMeta
	history, err := engine.RunAgentLoop(ctx, engine.Params{
		ProviderFactory: func() (types.Provider, error) { return s.providerFactory(ctx) },
		SystemPrompt:    localSearchAgentSystemPrompt(scope.DisableCommandSearch),
		Model:           scope.Model,
		History: []types.ChatMessage{{
			Role:    types.RoleUser,
			Content: ptr(localSearchAgentUserPrompt(scope)),
		}},
		Tools:         localSearchAgentToolDefinitions(s.registry, scope.DisableCommandSearch),
		ProviderState: nil,
		ExecuteTool: func(call types.ToolCall) (types.ChatMessage, error) {
			return s.executeGrantTool(ctx, scope, execCtx, observations, call, progressFn)
		},
		Callbacks: &engine.Callbacks{
			OnAssistantStart: func() {
				if progressFn != nil {
					progressFn(map[string]any{
						"phase":   localSearchAgentPhaseRanking,
						"job_id":  scope.JobID,
						"message": "Local search agent is reasoning over the configured Local Computer grant",
						"pct":     20,
					})
				}
			},
			OnAssistantFinal: func(message types.ChatMessage, meta *types.ResponseMeta) {
				finalMeta = meta
				if len(message.ToolCalls) == 0 && message.Content != nil {
					finalText = strings.TrimSpace(*message.Content)
				}
			},
		},
		MaxIterations: localSearchAgentMaxIterations,
	})
	if err != nil {
		return tools.Output{}, err
	}
	if strings.TrimSpace(finalText) == "" {
		finalText = lastAssistantText(history)
	}
	if !localSearchAgentHasSuccessfulDiscovery(observations) {
		if strings.HasPrefix(strings.TrimSpace(finalText), "Error:") {
			return tools.Output{}, errors.New(strings.TrimPrefix(strings.TrimSpace(finalText), "Error: "))
		}
		return tools.Output{}, errors.New("local search agent completed without a successful file search")
	}
	if progressFn != nil {
		progressFn(map[string]any{
			"phase":   localSearchAgentPhaseCompleted,
			"job_id":  scope.JobID,
			"message": "Local search agent completed",
			"pct":     100,
		})
	}
	return buildLocalSearchAgentOutput(scope, observations, finalText, finalMeta), nil
}

func localSearchAgentHasSuccessfulDiscovery(observations *localSearchAgentObservations) bool {
	if observations == nil {
		return false
	}
	for _, observation := range observations.items {
		if !observation.Output.OK {
			continue
		}
		switch observation.ToolName {
		case tools.LocalSearchCommandToolName, "local_search_in_grant":
			return true
		}
	}
	return false
}

func normalizeLocalSearchAgentConfiguredGrants(scope localSearchAgentScope) localSearchAgentScope {
	if len(scope.Grants) == 0 {
		grantID := strings.TrimSpace(scope.GrantID)
		if grantID == "" {
			grantID = strings.TrimSpace(scope.AgentGrantID)
		}
		if grantID == "" {
			grantID = strings.TrimSpace(scope.JobID)
		}
		localKind := strings.TrimSpace(strings.ToLower(scope.LocalKind))
		if localKind != "file" {
			localKind = "folder"
		}
		scope.GrantID = grantID
		scope.AgentGrantID = grantID
		if strings.TrimSpace(scope.LocalRoot) != "" {
			scope.Grants = []localSearchAgentGrant{{
				GrantID:   grantID,
				LocalRoot: filepath.Clean(strings.TrimSpace(scope.LocalRoot)),
				LocalKind: localKind,
			}}
		}
		return scope
	}
	grants := make([]localSearchAgentGrant, 0, len(scope.Grants))
	for i, grant := range scope.Grants {
		root := strings.TrimSpace(grant.LocalRoot)
		if root == "" {
			continue
		}
		localKind := strings.TrimSpace(strings.ToLower(grant.LocalKind))
		if localKind != "file" {
			localKind = "folder"
		}
		grantID := strings.TrimSpace(grant.GrantID)
		if grantID == "" {
			grantID = fmt.Sprintf("%s_grant_%d", strings.TrimSpace(scope.JobID), i+1)
		}
		grants = append(grants, localSearchAgentGrant{
			GrantID:   grantID,
			LocalRoot: filepath.Clean(root),
			LocalKind: localKind,
		})
	}
	scope.Grants = grants
	if len(grants) > 0 {
		scope.GrantID = grants[0].GrantID
		scope.AgentGrantID = grants[0].GrantID
		if strings.TrimSpace(scope.LocalRoot) == "" {
			scope.LocalRoot = grants[0].LocalRoot
		}
		if strings.TrimSpace(scope.LocalKind) == "" {
			scope.LocalKind = grants[0].LocalKind
		}
	}
	return scope
}

func localSearchAgentConfiguredGrants(scope localSearchAgentScope) []localSearchAgentGrant {
	if len(scope.Grants) > 0 {
		return scope.Grants
	}
	if strings.TrimSpace(scope.LocalRoot) == "" {
		return nil
	}
	grantID := strings.TrimSpace(scope.AgentGrantID)
	if grantID == "" {
		grantID = strings.TrimSpace(scope.GrantID)
	}
	localKind := strings.TrimSpace(strings.ToLower(scope.LocalKind))
	if localKind != "file" {
		localKind = "folder"
	}
	return []localSearchAgentGrant{{
		GrantID:   grantID,
		LocalRoot: filepath.Clean(strings.TrimSpace(scope.LocalRoot)),
		LocalKind: localKind,
	}}
}

func localSearchAgentGrantForRequest(scope localSearchAgentScope, requestedGrantID string) (localSearchAgentGrant, bool, string) {
	grants := localSearchAgentConfiguredGrants(scope)
	requestedGrantID = strings.TrimSpace(requestedGrantID)
	if len(grants) == 0 {
		return localSearchAgentGrant{}, false, "configured Local Computer grant is not available"
	}
	if requestedGrantID == "" {
		if len(grants) == 1 {
			return grants[0], true, ""
		}
		return localSearchAgentGrant{}, false, "local search agent grant_id is required when multiple configured Local Computer grants are available"
	}
	for _, grant := range grants {
		if requestedGrantID == strings.TrimSpace(grant.GrantID) {
			return grant, true, ""
		}
	}
	return localSearchAgentGrant{}, false, "local search agent grant_id does not match a configured Local Computer grant"
}

func (s *localSearchAgentService) executeGrantTool(
	ctx context.Context,
	scope localSearchAgentScope,
	execCtx tools.ExecContext,
	observations *localSearchAgentObservations,
	call types.ToolCall,
	progressFn func(map[string]any),
) (types.ChatMessage, error) {
	toolName := strings.TrimSpace(call.Name)
	if scope.DisableCommandSearch && toolName == tools.LocalSearchCommandToolName {
		out := tools.JSONError("local_search_command is disabled by its current permission; use local_search_in_grant")
		return localSearchAgentToolMessage(call, out), nil
	}
	switch toolName {
	case "local_list_grant_folder", tools.LocalSearchCommandToolName, "local_search_in_grant", "local_read_grant_file":
	default:
		out := tools.JSONError("local search agent may only use local grant tools")
		return localSearchAgentToolMessage(call, out), nil
	}
	tool, ok := s.registry.Get(toolName)
	if !ok {
		return localSearchAgentToolMessage(call, tools.JSONError("unknown local search grant tool: "+toolName)), nil
	}
	args := map[string]any{}
	if strings.TrimSpace(call.ArgsText) != "" {
		_ = json.Unmarshal([]byte(call.ArgsText), &args)
	} else if cloned := cloneStringAnyMap(call.Args); cloned != nil {
		args = cloned
	}
	// Only an explicitly-supplied, non-empty grant_id counts as a scope request.
	// A model that omits grant_id is fine: the executor injects the configured
	// handle below. Reading a missing key with fmt.Sprint would yield "<nil>"
	// and be falsely rejected as a mismatch, so probe for presence first.
	requestedGrantID := ""
	if raw, ok := args["grant_id"]; ok && raw != nil {
		requestedGrantID = strings.TrimSpace(fmt.Sprint(raw))
	}
	configuredGrant, ok, grantErr := localSearchAgentGrantForRequest(scope, requestedGrantID)
	if !ok {
		out := tools.JSONError(grantErr)
		return localSearchAgentToolMessage(call, out), nil
	}
	args["grant_id"] = configuredGrant.GrantID
	args["local_root"] = configuredGrant.LocalRoot
	args["local_kind"] = configuredGrant.LocalKind
	if toolName == "local_list_grant_folder" {
		if _, ok := args["max_entries"]; !ok && scope.MaxResults > 0 {
			args["max_entries"] = scope.MaxResults
		}
		if strings.TrimSpace(scope.Path) != "" {
			if _, ok := args["relative_path"]; !ok {
				args["relative_path"] = scope.Path
			}
		}
		if progressFn != nil {
			progressFn(map[string]any{
				"phase":     localSearchAgentPhaseListing,
				"job_id":    scope.JobID,
				"tool_name": toolName,
				"message":   "Listing the configured Local Computer grant",
				"pct":       30,
			})
		}
	} else if toolName == tools.LocalSearchCommandToolName {
		if _, ok := args["query"]; !ok || strings.TrimSpace(fmt.Sprint(args["query"])) == "" {
			args["query"] = scope.Query
		}
		if _, ok := args["program"]; !ok || strings.TrimSpace(fmt.Sprint(args["program"])) == "" {
			args["program"] = "auto"
		}
		if _, ok := args["max_results"]; !ok && scope.MaxResults > 0 {
			args["max_results"] = scope.MaxResults
		}
		if _, ok := args["file_types"]; !ok && len(scope.FileTypes) > 0 {
			args["file_types"] = scope.FileTypes
		}
		if strings.TrimSpace(scope.Path) != "" {
			if _, ok := args["relative_path"]; !ok {
				args["relative_path"] = scope.Path
			}
		}
		if progressFn != nil {
			progressFn(map[string]any{
				"phase":     localSearchAgentPhaseSearching,
				"job_id":    scope.JobID,
				"tool_name": toolName,
				"message":   "Running a bounded local search command",
				"pct":       35,
			})
		}
	} else if toolName == "local_search_in_grant" {
		if scope.DisableCommandSearch {
			args["portable_search"] = true
		}
		if _, ok := args["query"]; !ok || strings.TrimSpace(fmt.Sprint(args["query"])) == "" {
			args["query"] = scope.Query
		}
		if _, ok := args["max_results"]; !ok && scope.MaxResults > 0 {
			args["max_results"] = scope.MaxResults
		}
		if _, ok := args["file_types"]; !ok && len(scope.FileTypes) > 0 {
			args["file_types"] = scope.FileTypes
		}
		if strings.TrimSpace(scope.Path) != "" {
			if _, ok := args["relative_path"]; !ok {
				args["relative_path"] = scope.Path
			}
		}
		if progressFn != nil {
			progressFn(map[string]any{
				"phase":     localSearchAgentPhaseSearching,
				"job_id":    scope.JobID,
				"tool_name": toolName,
				"message":   "Searching the configured Local Computer grant",
				"pct":       35,
			})
		}
	} else if progressFn != nil {
		progressFn(map[string]any{
			"phase":     localSearchAgentPhaseReading,
			"job_id":    scope.JobID,
			"tool_name": toolName,
			"file_ref":  strings.TrimSpace(fmt.Sprint(args["file_ref"])),
			"message":   "Reading a candidate file from the configured Local Computer grant",
			"pct":       65,
		})
	}
	b, err := json.Marshal(args)
	if err != nil {
		return localSearchAgentToolMessage(call, tools.JSONError("failed to encode local search agent tool args")), nil
	}
	execCtx.PermissionApproved = true
	out, err := tool.Execute(ctx, execCtx, b)
	if err != nil {
		out = tools.JSONError(err.Error())
	}
	if observations != nil {
		observations.items = append(observations.items, localSearchAgentObservation{ToolName: toolName, Output: out})
	}
	return localSearchAgentToolMessage(call, out), nil
}

func parseConfiguredLocalSearchScope(args json.RawMessage) (localSearchAgentScope, bool) {
	var in struct {
		LocalRoot  string `json:"local_root"`
		LocalKind  string `json:"local_kind,omitempty"`
		GrantID    string `json:"grant_id,omitempty"`
		LocalRoots []struct {
			LocalRoot string `json:"local_root"`
			LocalKind string `json:"local_kind,omitempty"`
			GrantID   string `json:"grant_id,omitempty"`
		} `json:"local_roots,omitempty"`
		Path         string   `json:"path,omitempty"`
		Query        string   `json:"query"`
		LocationHint string   `json:"location_hint,omitempty"`
		FileTypes    []string `json:"file_types,omitempty"`
		MaxResults   int      `json:"max_results,omitempty"`
		Limit        int      `json:"limit,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return localSearchAgentScope{}, false
	}
	if strings.TrimSpace(in.LocalRoot) == "" || strings.TrimSpace(in.Query) == "" {
		if len(in.LocalRoots) == 0 || strings.TrimSpace(in.Query) == "" {
			return localSearchAgentScope{}, false
		}
	}
	localKind := strings.TrimSpace(strings.ToLower(in.LocalKind))
	if localKind != "file" {
		localKind = "folder"
	}
	maxResults := in.MaxResults
	if maxResults <= 0 {
		maxResults = in.Limit
	}
	if maxResults <= 0 {
		maxResults = 20
	}
	if maxResults > 200 {
		maxResults = 200
	}
	grants := make([]localSearchAgentGrant, 0, len(in.LocalRoots))
	for _, rawGrant := range in.LocalRoots {
		root := cleanLocalMachinePermissionPath(rawGrant.LocalRoot)
		if root == "" {
			continue
		}
		grantKind := strings.TrimSpace(strings.ToLower(rawGrant.LocalKind))
		if grantKind != "file" {
			grantKind = "folder"
		}
		grants = append(grants, localSearchAgentGrant{
			GrantID:   strings.TrimSpace(rawGrant.GrantID),
			LocalRoot: root,
			LocalKind: grantKind,
		})
	}
	if len(in.LocalRoots) > 0 && len(grants) == 0 {
		return localSearchAgentScope{}, false
	}
	localRoot := strings.TrimSpace(in.LocalRoot)
	if localRoot != "" {
		localRoot = filepath.Clean(localRoot)
	}
	return localSearchAgentScope{
		Query:        strings.TrimSpace(in.Query),
		LocationHint: strings.TrimSpace(in.LocationHint),
		Path:         strings.TrimSpace(in.Path),
		FileTypes:    cleanStringSlice(in.FileTypes),
		MaxResults:   maxResults,
		LocalRoot:    localRoot,
		LocalKind:    localKind,
		GrantID:      strings.TrimSpace(in.GrantID),
		Grants:       grants,
	}, true
}

func localSearchAgentSystemPrompt(commandSearchDisabled bool) string {
	commandGuidance := "Prefer local_search_command for flexible and fast lookup. Use program=auto for combined filename/content search, rg or grep for content patterns, and fd or find for filename patterns. Optional args are validated search flags only; never include shell syntax, paths, redirection, substitutions, or execution hooks."
	if commandSearchDisabled {
		commandGuidance = "local_search_command is disabled by the user's current tool permission. Use local_search_in_grant for portable filename and content search."
	}
	return strings.TrimSpace(fmt.Sprintf(`You are a dedicated local file search agent.

You may only use the provided local grant tools.
Do not request permissions.
Do not infer or invent filesystem access.
Do not write files, run unrestricted shell commands, browse the web, or use connector tools.
Search only the configured Local Computer grant_id values supplied by the application.
%s
Use local_list_grant_folder when the task is folder inventory or browsing.
Use local_search_in_grant as the portable fallback when the command runtime is unavailable.
Read only a few strong candidates with local_read_grant_file when reading helps rank or answer from the result.
PDF, spreadsheet, document, HTML, JSON, CSV, and other supported candidates can be read with local_read_grant_file; use the extracted text it returns when available.
Return JSON only with: status, agent_summary, files if you have ranked candidates, and uncertainty if relevant.`, commandGuidance))
}

func localSearchAgentToolDefinitions(registry *tools.Registry, commandSearchDisabled bool) []types.ToolDefinition {
	if registry == nil {
		return nil
	}
	definitions := registry.LocalGrantToolDefinitions()
	if !commandSearchDisabled {
		return definitions
	}
	filtered := make([]types.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if strings.TrimSpace(definition.Function.Name) == tools.LocalSearchCommandToolName {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func (m *Manager) localSearchCommandEnabled(ctx context.Context, threadID string) bool {
	if m == nil || m.permissions == nil {
		return true
	}
	_, _, evaluation, err := m.evaluateSingleManagerToolPermission(ctx, strings.TrimSpace(threadID), tools.LocalSearchCommandToolName, managerPermissionMeta{
		Category:    permissions.CategoryExecute,
		SubjectKind: "tool",
		ToolSetID:   "local-files",
		Operation:   tools.LocalSearchCommandToolName,
	})
	if err != nil {
		return false
	}
	if evaluation.Rule == nil {
		return true
	}
	return evaluation.Decision == permissions.DecisionAllow
}

func localSearchAgentUserPrompt(scope localSearchAgentScope) string {
	payload := map[string]any{
		"task":          "Use the configured Local Computer grant to satisfy the user's local file lookup intent. List folders for inventory or browsing tasks; search names and content for matching tasks.",
		"query":         scope.Query,
		"max_results":   scope.MaxResults,
		"local_kind":    scope.LocalKind,
		"output_schema": "status:string, agent_summary:string, files:[{file_ref, score, match_reasons}], uncertainty?:string",
	}
	grants := localSearchAgentConfiguredGrants(scope)
	if len(grants) == 1 {
		payload["grant_id"] = grants[0].GrantID
		payload["local_kind"] = grants[0].LocalKind
	} else if len(grants) > 1 {
		payload["task"] = "Use the configured Local Computer grants to satisfy the user's local file lookup intent. Search every relevant configured grant and keep grant_id on any files you return."
		configured := make([]map[string]any, 0, len(grants))
		for _, grant := range grants {
			configured = append(configured, map[string]any{
				"grant_id":   grant.GrantID,
				"local_kind": grant.LocalKind,
			})
		}
		payload["configured_grants"] = configured
	}
	if strings.TrimSpace(scope.LocationHint) != "" {
		payload["location_hint"] = scope.LocationHint
	}
	if strings.TrimSpace(scope.Path) != "" {
		payload["relative_path"] = scope.Path
	}
	if len(scope.FileTypes) > 0 {
		payload["file_types"] = scope.FileTypes
	}
	b, _ := json.MarshalIndent(payload, "", "  ")
	return string(b)
}

func localSearchAgentToolMessage(call types.ToolCall, out tools.Output) types.ChatMessage {
	content, err := out.ContentString()
	if err != nil {
		content = `{"error":"failed to encode local search agent tool result"}`
	}
	return types.ChatMessage{
		Role:       types.RoleTool,
		ToolCallID: strings.TrimSpace(call.ID),
		Name:       strings.TrimSpace(call.Name),
		Content:    ptr(content),
	}
}

func buildLocalSearchAgentOutput(scope localSearchAgentScope, observations *localSearchAgentObservations, finalText string, meta *types.ResponseMeta) tools.Output {
	files := mergeLocalSearchAgentFiles(observations)
	summary := localSearchAgentSummary(finalText)
	searched := localSearchAgentSearchStats(observations, len(files))
	timedOut, _ := searched["timed_out"].(bool)
	grants := localSearchAgentConfiguredGrants(scope)
	result := map[string]any{
		"status":        "completed",
		"job_id":        scope.JobID,
		"query":         scope.Query,
		"local_root":    scope.LocalRoot,
		"local_kind":    scope.LocalKind,
		"files":         files,
		"searched":      searched,
		"agent_summary": summary,
	}
	if timedOut {
		result["status"] = "partial"
		result["timed_out"] = true
		result["truncated"] = true
		result["retryable"] = true
		result["instructions"] = "Continue the task by retrying file_search with a narrower location_hint, path, file_types filter, or more specific filename. Do not abandon the search."
	}
	if len(grants) > 1 {
		result["local_roots"] = localSearchAgentRootSummaries(grants)
	} else if strings.TrimSpace(scope.GrantID) != "" {
		result["grant_id"] = scope.GrantID
	}
	if strings.TrimSpace(scope.Path) != "" {
		result["path"] = scope.Path
	}
	if len(scope.FileTypes) > 0 {
		result["file_types"] = scope.FileTypes
	}
	if meta != nil {
		result["search_agent"] = map[string]any{
			"provider_id": meta.ProviderID,
			"model":       meta.Model,
		}
	}
	extra := map[string]any{
		"provider":    "local-files",
		"product":     "local-files",
		"family":      "local-files",
		"tool_name":   "file_search",
		"kind":        "file_search",
		"readonly":    true,
		"job_id":      scope.JobID,
		"query":       scope.Query,
		"local_root":  scope.LocalRoot,
		"local_kind":  scope.LocalKind,
		"files_count": len(files),
	}
	if len(grants) > 1 {
		extra["local_roots_count"] = len(grants)
	} else if strings.TrimSpace(scope.GrantID) != "" {
		extra["grant_id"] = scope.GrantID
	}
	if truncated, ok := searched["truncated"].(bool); ok {
		extra["truncated"] = truncated
	}
	if timedOut {
		extra["timed_out"] = true
		extra["retryable"] = true
	}
	return tools.JSONOKWithMetadata(result, extra)
}

func localSearchAgentRootSummaries(grants []localSearchAgentGrant) []map[string]any {
	out := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		entry := map[string]any{
			"grant_id":   grant.GrantID,
			"local_kind": grant.LocalKind,
		}
		if strings.TrimSpace(grant.LocalRoot) != "" {
			entry["local_root"] = grant.LocalRoot
		}
		out = append(out, entry)
	}
	return out
}

func mergeLocalSearchAgentFiles(observations *localSearchAgentObservations) []map[string]any {
	if observations == nil {
		return nil
	}
	byRef := map[string]map[string]any{}
	for _, obs := range observations.items {
		if (obs.ToolName != tools.LocalSearchCommandToolName && obs.ToolName != "local_search_in_grant" && obs.ToolName != "local_list_grant_folder") || !obs.Output.OK {
			continue
		}
		result, ok := obs.Output.Result.(map[string]any)
		if !ok {
			continue
		}
		resultGrantID := firstStringMapValue(result, "grant_id")
		resultLocalRoot := firstStringMapValue(result, "local_root")
		rawFiles, _ := result["files"].([]map[string]any)
		if rawFiles == nil {
			if arr, ok := result["files"].([]any); ok {
				for _, raw := range arr {
					if m, ok := raw.(map[string]any); ok {
						rawFiles = append(rawFiles, m)
					}
				}
			}
		}
		for _, file := range rawFiles {
			ref := firstStringMapValue(file, "file_ref", "path")
			if ref == "" {
				continue
			}
			entry := cloneStringAnyMap(file)
			entry["file_ref"] = ref
			if resultGrantID != "" {
				if _, ok := entry["grant_id"]; !ok {
					entry["grant_id"] = resultGrantID
				}
			}
			if resultLocalRoot != "" {
				if _, ok := entry["local_root"]; !ok {
					entry["local_root"] = resultLocalRoot
				}
			}
			if _, ok := entry["file_name"]; !ok {
				entry["file_name"] = filepath.Base(ref)
			}
			if _, ok := entry["match_reasons"]; !ok {
				if matchTypes, ok := stringSliceFromAny(entry["match_types"]); ok {
					entry["match_reasons"] = matchTypes
				} else if obs.ToolName == "local_list_grant_folder" {
					entry["match_reasons"] = []string{"listed"}
				}
			}
			if _, ok := entry["snippets"]; !ok {
				if lines, ok := stringSliceFromAny(entry["line_matches"]); ok && len(lines) > 0 {
					snippets := make([]map[string]any, 0, len(lines))
					for _, line := range lines {
						snippets = append(snippets, map[string]any{"text": line})
					}
					entry["snippets"] = snippets
				} else {
					entry["snippets"] = []map[string]any{}
				}
			}
			if _, ok := entry["score"]; !ok {
				entry["score"] = localSearchAgentScore(entry)
			}
			key := ref
			if grantID := firstStringMapValue(entry, "grant_id"); grantID != "" {
				key = grantID + ":" + ref
			}
			byRef[key] = entry
		}
	}
	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	files := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		files = append(files, byRef[ref])
	}
	return files
}

func localSearchAgentSearchStats(observations *localSearchAgentObservations, fileCount int) map[string]any {
	stats := map[string]any{
		"files_matched": fileCount,
		"lists":         0,
		"searches":      0,
		"reads":         0,
		"truncated":     false,
		"timed_out":     false,
	}
	if observations == nil {
		return stats
	}
	totalMatches := 0
	for _, obs := range observations.items {
		switch obs.ToolName {
		case "local_list_grant_folder":
			stats["lists"] = stats["lists"].(int) + 1
			if result, ok := obs.Output.Result.(map[string]any); ok {
				if n, ok := intFromAny(result["files_count"]); ok {
					totalMatches += n
				}
				if v, ok := result["truncated"].(bool); ok && v {
					stats["truncated"] = true
				}
				if v, ok := result["timed_out"].(bool); ok && v {
					stats["timed_out"] = true
				}
			}
		case tools.LocalSearchCommandToolName, "local_search_in_grant":
			stats["searches"] = stats["searches"].(int) + 1
			if result, ok := obs.Output.Result.(map[string]any); ok {
				if n, ok := intFromAny(result["matches_count"]); ok {
					totalMatches += n
				}
				if v, ok := result["truncated"].(bool); ok && v {
					stats["truncated"] = true
				}
				if v, ok := result["timed_out"].(bool); ok && v {
					stats["timed_out"] = true
				}
			}
		case "local_read_grant_file":
			stats["reads"] = stats["reads"].(int) + 1
		}
	}
	stats["matches_count"] = totalMatches
	return stats
}

func localSearchAgentSummary(finalText string) string {
	finalText = strings.TrimSpace(finalText)
	if finalText == "" {
		return "Local search completed."
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(finalText), &parsed); err == nil {
		if summary := firstStringMapValue(parsed, "agent_summary", "summary"); summary != "" {
			return summary
		}
	}
	return finalText
}

func localSearchAgentScore(entry map[string]any) float64 {
	matchTypes, _ := stringSliceFromAny(entry["match_types"])
	hasName := false
	hasContent := false
	for _, matchType := range matchTypes {
		switch strings.TrimSpace(strings.ToLower(matchType)) {
		case "name":
			hasName = true
		case "content":
			hasContent = true
		}
	}
	switch {
	case hasName && hasContent:
		return 1.0
	case hasName:
		return 0.85
	case hasContent:
		return 0.75
	default:
		return 0.5
	}
}

func localSearchAgentHostedToolPolicy() providers.HostedToolPolicy {
	disabled := map[string]bool{}
	for _, capability := range tools.ProviderHostedToolCapabilities() {
		if strings.TrimSpace(capability.ID) != "" {
			disabled[capability.ID] = true
		}
	}
	return providers.HostedToolPolicy{Disabled: disabled}
}

func lastAssistantText(history []types.ChatMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role == types.RoleAssistant && msg.Content != nil && strings.TrimSpace(*msg.Content) != "" {
			return strings.TrimSpace(*msg.Content)
		}
	}
	return ""
}

func (m *Manager) recordLocalSearchJob(ctx context.Context, job localSearchAgentJob) {
	if m == nil || strings.TrimSpace(job.ID) == "" {
		return
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = now
	}
	m.localSearchMu.Lock()
	if m.localSearchJobs == nil {
		m.localSearchJobs = map[string]localSearchAgentJob{}
	}
	m.localSearchJobs[job.ID] = job
	m.localSearchMu.Unlock()
	m.persistLocalSearchJob(ctx, job)
}

func (m *Manager) finishLocalSearchJob(ctx context.Context, jobID string, status string, errMsg string, result map[string]any) {
	if m == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	m.localSearchMu.Lock()
	if m.localSearchJobs == nil {
		m.localSearchJobs = map[string]localSearchAgentJob{}
	}
	job, ok := m.localSearchJobs[jobID]
	if !ok {
		job = localSearchAgentJob{ID: strings.TrimSpace(jobID), CreatedAt: time.Now().UTC()}
	}
	job.Status = strings.TrimSpace(status)
	job.Error = strings.TrimSpace(errMsg)
	job.UpdatedAt = time.Now().UTC()
	job.CompletedAt = job.UpdatedAt
	if result != nil {
		job.Result = cloneStringAnyMap(result)
		job.FilesCount = localSearchJobFilesCount(result)
	}
	m.localSearchJobs[jobID] = job
	m.localSearchMu.Unlock()
	m.persistLocalSearchJob(ctx, job)
}

func (m *Manager) localSearchJobSnapshot(jobID string) (localSearchAgentJob, bool) {
	if m == nil {
		return localSearchAgentJob{}, false
	}
	m.localSearchMu.Lock()
	defer m.localSearchMu.Unlock()
	job, ok := m.localSearchJobs[strings.TrimSpace(jobID)]
	return job, ok
}

func (m *Manager) GetLocalSearchJob(ctx context.Context, jobID string) (LocalSearchJobStatus, bool) {
	job, ok := m.localSearchJobSnapshot(jobID)
	if ok {
		return localSearchJobStatus(job), true
	}
	if m == nil || m.store == nil {
		return LocalSearchJobStatus{}, false
	}
	stored, err := m.store.GetLocalSearchJob(ctx, strings.TrimSpace(jobID))
	if err != nil {
		return LocalSearchJobStatus{}, false
	}
	job = localSearchJobFromStore(stored)
	m.cacheLocalSearchJob(job)
	return localSearchJobStatus(job), true
}

func (m *Manager) ListLocalSearchJobs(ctx context.Context, filter LocalSearchJobFilter) []LocalSearchJobStatus {
	if m == nil {
		return nil
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	threadID := strings.TrimSpace(filter.ThreadID)
	runID := strings.TrimSpace(filter.RunID)
	byID := map[string]localSearchAgentJob{}
	if m.store != nil {
		stored, err := m.store.ListLocalSearchJobs(ctx, store.LocalSearchJobFilter{
			ThreadID: threadID,
			RunID:    runID,
			Limit:    limit,
		})
		if err == nil {
			for _, job := range stored {
				agentJob := localSearchJobFromStore(job)
				if strings.TrimSpace(agentJob.ID) != "" {
					byID[agentJob.ID] = agentJob
				}
			}
		}
	}
	m.localSearchMu.Lock()
	jobs := make([]localSearchAgentJob, 0, len(m.localSearchJobs))
	for _, job := range m.localSearchJobs {
		if threadID != "" && job.ThreadID != threadID {
			continue
		}
		if runID != "" && job.RunID != runID {
			continue
		}
		byID[job.ID] = job
	}
	m.localSearchMu.Unlock()
	for _, job := range byID {
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].UpdatedAt.After(jobs[j].UpdatedAt)
	})
	if len(jobs) > limit {
		jobs = jobs[:limit]
	}
	out := make([]LocalSearchJobStatus, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, localSearchJobStatus(job))
	}
	return out
}

func (m *Manager) cacheLocalSearchJob(job localSearchAgentJob) {
	if m == nil || strings.TrimSpace(job.ID) == "" {
		return
	}
	m.localSearchMu.Lock()
	defer m.localSearchMu.Unlock()
	if m.localSearchJobs == nil {
		m.localSearchJobs = map[string]localSearchAgentJob{}
	}
	m.localSearchJobs[job.ID] = job
}

func (m *Manager) persistLocalSearchJob(ctx context.Context, job localSearchAgentJob) {
	if m == nil || m.store == nil || strings.TrimSpace(job.ID) == "" {
		return
	}
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	_, _ = m.store.UpsertLocalSearchJob(ctx, localSearchJobToStore(job))
}

func localSearchJobToStore(job localSearchAgentJob) store.LocalSearchJob {
	var completedAt *time.Time
	if !job.CompletedAt.IsZero() {
		t := job.CompletedAt.UTC()
		completedAt = &t
	}
	return store.LocalSearchJob{
		ID:          strings.TrimSpace(job.ID),
		ThreadID:    strings.TrimSpace(job.ThreadID),
		RunID:       strings.TrimSpace(job.RunID),
		ToolCallID:  strings.TrimSpace(job.ToolCallID),
		Status:      strings.TrimSpace(strings.ToLower(job.Status)),
		Query:       strings.TrimSpace(job.Query),
		GrantID:     strings.TrimSpace(job.GrantID),
		LocalRoot:   strings.TrimSpace(job.LocalRoot),
		LocalKind:   strings.TrimSpace(strings.ToLower(job.LocalKind)),
		CreatedAt:   job.CreatedAt.UTC(),
		UpdatedAt:   job.UpdatedAt.UTC(),
		CompletedAt: completedAt,
		FilesCount:  job.FilesCount,
		Error:       strings.TrimSpace(job.Error),
		Result:      cloneStringAnyMap(job.Result),
	}
}

func localSearchJobFromStore(job store.LocalSearchJob) localSearchAgentJob {
	out := localSearchAgentJob{
		ID:         strings.TrimSpace(job.ID),
		ThreadID:   strings.TrimSpace(job.ThreadID),
		RunID:      strings.TrimSpace(job.RunID),
		ToolCallID: strings.TrimSpace(job.ToolCallID),
		Status:     strings.TrimSpace(strings.ToLower(job.Status)),
		Query:      strings.TrimSpace(job.Query),
		GrantID:    strings.TrimSpace(job.GrantID),
		LocalRoot:  strings.TrimSpace(job.LocalRoot),
		LocalKind:  strings.TrimSpace(strings.ToLower(job.LocalKind)),
		CreatedAt:  job.CreatedAt,
		UpdatedAt:  job.UpdatedAt,
		FilesCount: job.FilesCount,
		Error:      strings.TrimSpace(job.Error),
		Result:     cloneStringAnyMap(job.Result),
	}
	if job.CompletedAt != nil {
		out.CompletedAt = job.CompletedAt.UTC()
	}
	return out
}

func localSearchJobStatus(job localSearchAgentJob) LocalSearchJobStatus {
	status := LocalSearchJobStatus{
		ID:         strings.TrimSpace(job.ID),
		ThreadID:   strings.TrimSpace(job.ThreadID),
		RunID:      strings.TrimSpace(job.RunID),
		ToolCallID: strings.TrimSpace(job.ToolCallID),
		Status:     strings.TrimSpace(job.Status),
		Query:      strings.TrimSpace(job.Query),
		GrantID:    strings.TrimSpace(job.GrantID),
		LocalRoot:  strings.TrimSpace(job.LocalRoot),
		LocalKind:  strings.TrimSpace(job.LocalKind),
		CreatedAt:  job.CreatedAt,
		UpdatedAt:  job.UpdatedAt,
		FilesCount: job.FilesCount,
		Error:      strings.TrimSpace(job.Error),
		Result:     cloneStringAnyMap(job.Result),
	}
	if !job.CompletedAt.IsZero() {
		completedAt := job.CompletedAt
		status.CompletedAt = &completedAt
	}
	return status
}

func localSearchJobResultMap(out tools.Output) map[string]any {
	result, ok := out.Result.(map[string]any)
	if !ok {
		return nil
	}
	return cloneStringAnyMap(result)
}

func localSearchJobFilesCount(result map[string]any) int {
	if result == nil {
		return 0
	}
	if n, ok := intFromAny(result["files_count"]); ok {
		return n
	}
	if files, ok := result["files"].([]map[string]any); ok {
		return len(files)
	}
	if files, ok := result["files"].([]any); ok {
		return len(files)
	}
	return 0
}

func (m *Manager) resolveConfiguredLocalSearchGrantID(scope localSearchAgentScope) string {
	if m == nil || strings.TrimSpace(scope.LocalRoot) == "" {
		return ""
	}
	scopeKind := strings.TrimSpace(strings.ToLower(scope.LocalKind))
	if scopeKind != "file" {
		scopeKind = "folder"
	}
	for _, grant := range m.configuredLocalComputerReadPermissionGrants() {
		if filepath.Clean(strings.TrimSpace(grant.Root)) != filepath.Clean(strings.TrimSpace(scope.LocalRoot)) {
			continue
		}
		grantKind := strings.TrimSpace(strings.ToLower(grant.Kind))
		if grantKind != "file" {
			grantKind = "folder"
		}
		if grantKind != scopeKind {
			continue
		}
		return strings.TrimSpace(grant.ID)
	}
	return ""
}

func permissionsRuleFilterRead() permissions.RuleFilter {
	return permissions.RuleFilter{Category: permissions.CategoryRead}
}

func stringSliceFromAny(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return cleanStringSlice(values), true
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s := strings.TrimSpace(fmt.Sprint(value)); s != "" {
				out = append(out, s)
			}
		}
		return out, len(out) > 0
	default:
		return nil, false
	}
}

func cleanStringSlice(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func intFromAny(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		i, err := v.Int64()
		return int(i), err == nil
	default:
		return 0, false
	}
}
