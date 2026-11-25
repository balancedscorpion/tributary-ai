package optionc

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/localexec"
	"ai-analyst-local/internal/permissions"
)

type fakeLocalSearchAgentProvider struct {
	mu                 sync.Mutex
	model              string
	toolName           string
	grantID            string
	query              string
	requests           []types.ChatRequest
	toolResultContents []string
}

func (p *fakeLocalSearchAgentProvider) ID() string { return "fake-local-search-provider" }

func (p *fakeLocalSearchAgentProvider) Model() string {
	if strings.TrimSpace(p.model) == "" {
		return "fake-local-search-model"
	}
	return p.model
}

func (p *fakeLocalSearchAgentProvider) StreamChat(ctx context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	for _, msg := range req.Messages {
		if msg.Role == types.RoleTool && msg.Content != nil {
			p.toolResultContents = append(p.toolResultContents, *msg.Content)
		}
	}
	hasToolResult := len(p.toolResultContents) > 0
	p.mu.Unlock()

	ch := make(chan types.StreamEvent, 1)
	go func() {
		defer close(ch)
		meta := types.ResponseMeta{ProviderID: p.ID(), Model: p.Model()}
		select {
		case <-ctx.Done():
			ch <- types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Message: ctx.Err().Error(), Retryable: false, Meta: &meta}}
			return
		default:
		}
		if !hasToolResult {
			toolName := strings.TrimSpace(p.toolName)
			if toolName == "" {
				toolName = "local_search_in_grant"
			}
			ch <- types.StreamEvent{Event: types.EventToolCalls, Data: types.ToolCallsData{
				ToolCalls: []types.ToolCall{{
					ID:   "agent_tool_1",
					Name: toolName,
					Args: map[string]any{
						"grant_id":    p.grantID,
						"query":       p.query,
						"max_results": 5,
					},
				}},
				Meta: meta,
			}}
			return
		}
		ch <- types.StreamEvent{Event: types.EventFinal, Data: types.FinalData{
			Answer: `{"status":"completed","agent_summary":"ranked alpha candidate"}`,
			Meta:   meta,
		}}
	}()
	return ch, nil
}

func (p *fakeLocalSearchAgentProvider) requestToolNames() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.requests) == 0 {
		return nil
	}
	names := make([]string, 0, len(p.requests[0].Tools))
	for _, def := range p.requests[0].Tools {
		names = append(names, def.Function.Name)
	}
	return names
}

func TestManagerExecToolRunsConfiguredFileSearchDirectly(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "fake", "fake-local-search-model", nil, "system", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "alpha.txt"), []byte("alpha revenue forecast\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	registry := tools.NewRegistry(nil)
	manager := &Manager{
		store:  st,
		tools:  registry,
		cfgDir: cfgDir,
	}

	msg, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_local_search_agent",
		Name: "file_search",
		ArgsText: mustJSONForLocalSearchAgentTest(map[string]any{
			"local_root":  root,
			"local_kind":  "folder",
			"grant_id":    "grant-test",
			"query":       "alpha",
			"max_results": 5,
		}),
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("tool message content is nil")
	}
	env := decodeToolEnvelopeForLocalSearchAgentTest(t, *msg.Content)
	if env["tool_name"] != "file_search" || env["ok"] != true {
		t.Fatalf("envelope=%#v, want successful file_search", env)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result=%#v, want object", env["result"])
	}
	if result["status"] != "completed" || result["grant_id"] != "grant-test" || result["query"] != "alpha" {
		t.Fatalf("result status/grant/query=%#v", result)
	}
	if strings.TrimSpace(result["job_id"].(string)) == "" {
		t.Fatalf("job_id missing from result=%#v", result)
	}
	jobID := strings.TrimSpace(result["job_id"].(string))
	files, ok := result["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("files=%#v, want one match", result["files"])
	}
	file, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("file=%#v, want object", files[0])
	}
	if file["file_ref"] != "reports/alpha.txt" || file["file_name"] != "alpha.txt" {
		t.Fatalf("file=%#v, want normalized local file result", file)
	}
	if _, ok := file["snippets"].([]any); !ok {
		t.Fatalf("file snippets=%#v, want snippets derived from line_matches", file["snippets"])
	}
	if result["agent_summary"] != "Searched configured Local Computer access directly." {
		t.Fatalf("agent_summary=%#v", result["agent_summary"])
	}
	job, ok := manager.GetLocalSearchJob(context.Background(), jobID)
	if !ok {
		t.Fatalf("local search job %q missing", jobID)
	}
	if job.Status != "completed" || job.ThreadID != thread.ID || job.RunID != run.ID || job.ToolCallID != "tc_local_search_agent" {
		t.Fatalf("job status=%#v, want completed job for run/tool", job)
	}
	if job.Query != "alpha" || job.GrantID != "grant-test" || job.FilesCount != 1 {
		t.Fatalf("job query/grant/files=%#v, want alpha grant-test one file", job)
	}
	if job.Result == nil || job.Result["status"] != "completed" {
		t.Fatalf("job result=%#v, want completed result snapshot", job.Result)
	}
	jobs := manager.ListLocalSearchJobs(context.Background(), LocalSearchJobFilter{ThreadID: thread.ID, Limit: 10})
	if len(jobs) != 1 || jobs[0].ID != jobID {
		t.Fatalf("jobs=%#v, want the completed local search job", jobs)
	}
}

func TestManagerExecToolRootlessLocalComputerSearchFallsBackAcrossSelectedFolders(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "fake", "fake-local-search-model", nil, "system", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootB, "reports"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "reports", "needle-file.txt"), []byte("quarterly local needle\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{rootA, rootB},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	registry := tools.NewRegistry(nil)
	providerCalls := 0
	manager := &Manager{
		store:       st,
		settings:    settingsStore,
		permissions: permissionSvc,
		tools:       registry,
		cfgDir:      cfgDir,
		localSearchAgent: &localSearchAgentService{
			registry: registry,
			providerFactory: func(context.Context) (types.Provider, error) {
				providerCalls++
				return nil, errors.New("provider unavailable")
			},
		},
	}

	msg, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:       "tc_rootless_local_search",
		Name:     "file_search",
		ArgsText: mustJSONForLocalSearchAgentTest(map[string]any{"query": "needle", "max_results": 10}),
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if providerCalls != 0 {
		t.Fatalf("providerCalls=%d, want filename preflight to avoid the search agent", providerCalls)
	}
	if msg.Content == nil {
		t.Fatal("tool message content is nil")
	}
	env := decodeToolEnvelopeForLocalSearchAgentTest(t, *msg.Content)
	if env["tool_name"] != "file_search" || env["ok"] != true {
		t.Fatalf("envelope=%#v, want successful file_search", env)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result=%#v, want object", env["result"])
	}
	files, ok := result["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("files=%#v, want one match from selected Local Computer folders", result["files"])
	}
	file, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("file=%#v, want object", files[0])
	}
	if file["file_ref"] != "reports/needle-file.txt" || file["local_root"] != canonicalManagerTestPath(rootB) {
		t.Fatalf("file=%#v, want match from rootB", file)
	}
	if _, ok := result["local_roots"].([]any); !ok {
		t.Fatalf("result=%#v, want local_roots summary for multi-folder search", result)
	}
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestLocalSearchAgentRejectsNonGrantTools(t *testing.T) {
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-test",
		Query:        "alpha",
		LocalRoot:    t.TempDir(),
		LocalKind:    "folder",
		AgentGrantID: "grant-test",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_bad",
		Name: "read_file",
		Args: map[string]any{"path": "secret.txt"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil || !strings.Contains(*msg.Content, "may only use local grant tools") {
		t.Fatalf("message content=%v, want fail-closed error", msg.Content)
	}
	if len(observations.items) != 0 {
		t.Fatalf("observations=%#v, want none for rejected tool", observations.items)
	}
}

func TestManagerExecToolDoesNotDispatchFileSearchWhenApprovalDenied(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "fake", "fake-local-search-model", nil, "system", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectDeny,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			Tool:      "file_search",
			LocalRoot: root,
			LocalKind: "folder",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	providerCalls := 0
	registry := tools.NewRegistry(nil)
	manager := &Manager{
		store:       st,
		tools:       registry,
		cfgDir:      cfgDir,
		permissions: permissionSvc,
		localSearchAgent: &localSearchAgentService{
			registry: registry,
			providerFactory: func(context.Context) (types.Provider, error) {
				providerCalls++
				return &fakeLocalSearchAgentProvider{}, nil
			},
		},
	}
	msg, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_local_search_denied",
		Name: "file_search",
		ArgsText: mustJSONForLocalSearchAgentTest(map[string]any{
			"local_root": root,
			"query":      "alpha",
		}),
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if providerCalls != 0 {
		t.Fatalf("providerCalls=%d, want no dispatch after denial", providerCalls)
	}
	if msg.Content == nil || !strings.Contains(*msg.Content, "LOCAL_COMPUTER_SCOPE_REQUIRED") {
		t.Fatalf("message content=%v, want Local Computer scope failure", msg.Content)
	}
}

func mustJSONForLocalSearchAgentTest(v map[string]any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func decodeToolEnvelopeForLocalSearchAgentTest(t *testing.T, raw string) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("decode tool envelope: %v\n%s", err, raw)
	}
	return env
}

func localSearchAgentFileRefs(t *testing.T, out tools.Output) []string {
	t.Helper()
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("output result=%#v, want object", out.Result)
	}
	var refs []string
	switch files := result["files"].(type) {
	case []map[string]any:
		for _, file := range files {
			if ref, ok := file["file_ref"].(string); ok {
				refs = append(refs, ref)
			}
		}
	case []any:
		for _, raw := range files {
			if file, ok := raw.(map[string]any); ok {
				if ref, ok := file["file_ref"].(string); ok {
					refs = append(refs, ref)
				}
			}
		}
	}
	return refs
}

// The sub-agent must not be able to point a scoped tool at a different grant.
func TestLocalSearchAgentRejectsGrantIDMismatch(t *testing.T) {
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-test",
		Query:        "alpha",
		LocalRoot:    t.TempDir(),
		LocalKind:    "folder",
		AgentGrantID: "grant-approved",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_mismatch",
		Name: "local_search_in_grant",
		Args: map[string]any{"grant_id": "grant-evil", "query": "alpha"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil || !strings.Contains(*msg.Content, "does not match a configured Local Computer grant") {
		t.Fatalf("message content=%v, want grant mismatch rejection", msg.Content)
	}
	if len(observations.items) != 0 {
		t.Fatalf("observations=%#v, want none for rejected grant", observations.items)
	}
}

func TestLocalSearchAgentUsesRequestedConfiguredGrantFromMultipleGrants(t *testing.T) {
	rootA := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "alpha-a.txt"), []byte("alpha in first grant\n"), 0o644); err != nil {
		t.Fatalf("WriteFile rootA: %v", err)
	}
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootB, "alpha-b.txt"), []byte("alpha in second grant\n"), 0o644); err != nil {
		t.Fatalf("WriteFile rootB: %v", err)
	}

	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID: "job-multi-grant",
		Query: "alpha",
		Grants: []localSearchAgentGrant{
			{GrantID: "grant-a", LocalRoot: rootA, LocalKind: "folder"},
			{GrantID: "grant-b", LocalRoot: rootB, LocalKind: "folder"},
		},
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_multi",
		Name: "local_search_in_grant",
		Args: map[string]any{
			"grant_id": "grant-b",
			"query":    "alpha",
		},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("message content is nil")
	}
	if len(observations.items) != 1 || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful search", observations.items)
	}
	result, ok := observations.items[0].Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%#v, want object", observations.items[0].Output.Result)
	}
	if result["grant_id"] != "grant-b" || result["local_root"] != canonicalManagerTestPath(rootB) {
		t.Fatalf("result=%#v, want selected configured Local Computer grant-b root", result)
	}
	refs := localSearchAgentFileRefs(t, observations.items[0].Output)
	if strings.Join(refs, ",") != "alpha-b.txt" {
		t.Fatalf("refs=%v, want only file from requested configured Local Computer grant", refs)
	}
}

func TestLocalSearchAgentRunsBoundedSearchCommandInsideRequestedGrant(t *testing.T) {
	if _, ok := localexec.ResolveCommand("rg"); !ok {
		t.Skip("ripgrep is unavailable in this test environment")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "forecast.txt"), []byte("needle forecast\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-command",
		Query:        "needle",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-command",
		MaxResults:   5,
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_command",
		Name: tools.LocalSearchCommandToolName,
		Args: map[string]any{
			"grant_id": "grant-command",
			"program":  "rg",
			"query":    "needle",
		},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("message content is nil")
	}
	if len(observations.items) != 1 || observations.items[0].ToolName != tools.LocalSearchCommandToolName || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful bounded command search", observations.items)
	}
	result, ok := observations.items[0].Output.Result.(map[string]any)
	if !ok || result["search_backend"] != "command_broker" || result["grant_id"] != "grant-command" {
		t.Fatalf("result=%#v, want scoped command broker result", observations.items[0].Output.Result)
	}
	if !containsStringForTest(localSearchAgentFileRefs(t, observations.items[0].Output), "reports/forecast.txt") {
		t.Fatalf("result=%#v, want reports/forecast.txt", result)
	}
}

func TestLocalSearchCommandPermissionDisablesOnlyCommandBackend(t *testing.T) {
	ctx := context.Background()
	for _, effect := range []string{permissions.EffectAsk, permissions.EffectDeny} {
		t.Run(effect, func(t *testing.T) {
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("NewJSON: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category: permissions.CategoryExecute,
				Effect:   effect,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind: "tool",
					Tool: tools.LocalSearchCommandToolName,
				},
			}); err != nil {
				t.Fatalf("CreateRule: %v", err)
			}
			manager := &Manager{permissions: permissionSvc}
			if manager.localSearchCommandEnabled(ctx, "thread") {
				t.Fatalf("localSearchCommandEnabled=true, want false for %s rule", effect)
			}

			registry := tools.NewRegistry(nil)
			definitions := localSearchAgentToolDefinitions(registry, true)
			for _, definition := range definitions {
				if definition.Function.Name == tools.LocalSearchCommandToolName {
					t.Fatalf("disabled definitions include %s", tools.LocalSearchCommandToolName)
				}
			}
		})
	}
}

func TestLocalSearchCommandAlwaysPermissionEnablesCommandBackend(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryExecute,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind: "tool",
			Tool: tools.LocalSearchCommandToolName,
		},
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	if !manager.localSearchCommandEnabled(ctx, "thread") {
		t.Fatal("localSearchCommandEnabled=false, want true for always-allow rule")
	}
}

func TestLocalSearchCommandPermissionUsesLocalComputerUIScope(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		subject store.PermissionSubject
		effect  string
		want    bool
	}{
		{
			name: "atomic ask",
			subject: store.PermissionSubject{
				Kind:      "tool",
				Tool:      tools.LocalSearchCommandToolName,
				Connector: "local-computer",
				ToolSetID: "local-computer",
			},
			effect: permissions.EffectAsk,
		},
		{
			name: "atomic allow",
			subject: store.PermissionSubject{
				Kind:      "tool",
				Tool:      tools.LocalSearchCommandToolName,
				Connector: "local-computer",
				ToolSetID: "local-computer",
			},
			effect: permissions.EffectAllow,
			want:   true,
		},
		{
			name: "tool set off",
			subject: store.PermissionSubject{
				Kind:      "tool_set",
				Connector: "local-computer",
				ToolSetID: "local-computer",
			},
			effect: permissions.EffectDeny,
		},
		{
			name: "connector ask",
			subject: store.PermissionSubject{
				Kind:      "connector",
				Connector: "local-computer",
			},
			effect: permissions.EffectAsk,
		},
		{
			name: "atomic allow overrides connector off",
			subject: store.PermissionSubject{
				Kind:      "connector",
				Connector: "local-computer",
			},
			effect: permissions.EffectDeny,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("NewJSON: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			if tt.name == "atomic allow overrides connector off" {
				if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
					Category: permissions.CategoryExecute,
					Effect:   permissions.EffectAllow,
					Scope:    "global",
					Subject: store.PermissionSubject{
						Kind:      "tool",
						Tool:      tools.LocalSearchCommandToolName,
						Connector: "local-computer",
						ToolSetID: "local-computer",
					},
				}); err != nil {
					t.Fatalf("CreateRule atomic allow: %v", err)
				}
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category: permissions.CategoryExecute,
				Effect:   tt.effect,
				Scope:    "global",
				Subject:  tt.subject,
			}); err != nil {
				t.Fatalf("CreateRule: %v", err)
			}

			manager := &Manager{permissions: permissionSvc}
			if got := manager.localSearchCommandEnabled(ctx, "thread"); got != tt.want {
				t.Fatalf("localSearchCommandEnabled=%v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocalSearchAgentPortableFallbackDoesNotUseCommandBroker(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forecast.txt"), []byte("needle forecast\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	_, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:                "job-portable",
		Query:                "needle",
		LocalRoot:            root,
		LocalKind:            "folder",
		AgentGrantID:         "grant-portable",
		DisableCommandSearch: true,
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_portable",
		Name: "local_search_in_grant",
		Args: map[string]any{"grant_id": "grant-portable", "query": "needle"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if len(observations.items) != 1 || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful portable search", observations.items)
	}
	result, ok := observations.items[0].Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%#v, want map", observations.items[0].Output.Result)
	}
	if result["search_backend"] == "command_broker" {
		t.Fatalf("result=%#v, disabled command permission must use portable search", result)
	}
	if !containsStringForTest(localSearchAgentFileRefs(t, observations.items[0].Output), "forecast.txt") {
		t.Fatalf("result=%#v, want forecast.txt", result)
	}
}

func TestLocalSearchAgentRequiresSuccessfulDiscoveryObservation(t *testing.T) {
	observations := &localSearchAgentObservations{items: []localSearchAgentObservation{
		{ToolName: tools.LocalSearchCommandToolName, Output: tools.JSONError("runtime unavailable")},
		{ToolName: "local_list_grant_folder", Output: tools.Output{OK: true}},
		{ToolName: "local_read_grant_file", Output: tools.Output{OK: true}},
	}}
	if localSearchAgentHasSuccessfulDiscovery(observations) {
		t.Fatal("failed search plus listing/read counted as successful search discovery")
	}
	observations.items = append(observations.items, localSearchAgentObservation{
		ToolName: "local_search_in_grant",
		Output:   tools.Output{OK: true},
	})
	if !localSearchAgentHasSuccessfulDiscovery(observations) {
		t.Fatal("successful portable search was not counted as discovery")
	}
}

func TestLocalSearchAgentRequiresGrantIDWhenMultipleGrantsAvailable(t *testing.T) {
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID: "job-multi-grant-missing-id",
		Query: "alpha",
		Grants: []localSearchAgentGrant{
			{GrantID: "grant-a", LocalRoot: t.TempDir(), LocalKind: "folder"},
			{GrantID: "grant-b", LocalRoot: t.TempDir(), LocalKind: "folder"},
		},
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_missing_grant",
		Name: "local_search_in_grant",
		Args: map[string]any{"query": "alpha"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil || !strings.Contains(*msg.Content, "grant_id is required when multiple configured Local Computer grants are available") {
		t.Fatalf("message content=%v, want missing grant_id error", msg.Content)
	}
	if len(observations.items) != 0 {
		t.Fatalf("observations=%#v, want none for rejected grant request", observations.items)
	}
}

// A model that supplies a different local_root must not widen scope: the
// executor injects the configured root server-side and the search runs there.
func TestLocalSearchAgentOverridesModelSuppliedRoot(t *testing.T) {
	configuredRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(configuredRoot, "approved.txt"), []byte("alpha inside configured Local Computer grant\n"), 0o644); err != nil {
		t.Fatalf("WriteFile approved: %v", err)
	}
	forbiddenRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(forbiddenRoot, "forbidden.txt"), []byte("alpha inside forbidden root\n"), 0o644); err != nil {
		t.Fatalf("WriteFile forbidden: %v", err)
	}

	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-root-override",
		Query:        "alpha",
		LocalRoot:    configuredRoot,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_override",
		Name: "local_search_in_grant",
		Args: map[string]any{
			"grant_id":   "grant-x",
			"query":      "alpha",
			"local_root": forbiddenRoot,
			"local_kind": "folder",
		},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("message content is nil")
	}
	if len(observations.items) != 1 || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful search", observations.items)
	}
	result, ok := observations.items[0].Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%#v, want object", observations.items[0].Output.Result)
	}
	if got := result["local_root"]; got != canonicalManagerTestPath(configuredRoot) {
		t.Fatalf("search local_root=%v, want configured root %q", got, canonicalManagerTestPath(configuredRoot))
	}
	refs := localSearchAgentFileRefs(t, observations.items[0].Output)
	if !containsStringForTest(refs, "approved.txt") {
		t.Fatalf("file refs=%v, want approved.txt from the configured Local Computer grant", refs)
	}
	if containsStringForTest(refs, "forbidden.txt") {
		t.Fatalf("file refs=%v, must not include files outside the configured Local Computer grant", refs)
	}
}

// Regression: a model that omits grant_id must not be falsely rejected; the
// executor injects the configured grant handle.
func TestLocalSearchAgentInjectsGrantWhenModelOmitsGrantID(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "match.txt"), []byte("alpha revenue\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-omit-grant",
		Query:        "alpha",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_omit",
		Name: "local_search_in_grant",
		Args: map[string]any{"query": "alpha"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content != nil && strings.Contains(*msg.Content, "does not match a configured Local Computer grant") {
		t.Fatalf("omitted grant_id was rejected: %s", *msg.Content)
	}
	if len(observations.items) != 1 || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful search when grant_id omitted", observations.items)
	}
	if !containsStringForTest(localSearchAgentFileRefs(t, observations.items[0].Output), "match.txt") {
		t.Fatalf("expected match.txt in results after grant_id injection")
	}
}

// The list tool is permitted and can return a folder inventory without using a
// text search as a proxy for directory listing.
func TestLocalSearchAgentAllowsAndAccumulatesGrantListTool(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "forecast.pdf"), []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-list",
		Query:        "what is in reports",
		Path:         "reports",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
		MaxResults:   10,
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_list",
		Name: "local_list_grant_folder",
		Args: map[string]any{"grant_id": "grant-x"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("message content is nil")
	}
	if len(observations.items) != 1 || observations.items[0].ToolName != "local_list_grant_folder" || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful list", observations.items)
	}
	refs := localSearchAgentFileRefs(t, observations.items[0].Output)
	if !containsStringForTest(refs, "reports/forecast.pdf") {
		t.Fatalf("file refs=%v, want listed PDF", refs)
	}
	files := mergeLocalSearchAgentFiles(observations)
	if len(files) != 1 || files[0]["file_ref"] != "reports/forecast.pdf" {
		t.Fatalf("merged files=%#v, want listed file result", files)
	}
	stats := localSearchAgentSearchStats(observations, len(files))
	if stats["lists"] != 1 || stats["matches_count"] != 1 {
		t.Fatalf("searched stats=%#v, want one list and one listed file", stats)
	}
}

// The read tool is permitted and its result is captured as an observation.
func TestLocalSearchAgentAllowsAndAccumulatesGrantReadTool(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("quarterly notes\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-read",
		Query:        "quarterly",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_read",
		Name: "local_read_grant_file",
		Args: map[string]any{"grant_id": "grant-x", "file_ref": "notes.txt"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("message content is nil")
	}
	if len(observations.items) != 1 || observations.items[0].ToolName != "local_read_grant_file" || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful read", observations.items)
	}
	result, ok := observations.items[0].Output.Result.(map[string]any)
	if !ok || !strings.Contains(strings.ToLower(fmtSprintForLocalSearchTest(result["content"])), "quarterly") {
		t.Fatalf("read result=%#v, want file content", observations.items[0].Output.Result)
	}
	// The read observation contributes to the searched.reads stat.
	stats := localSearchAgentSearchStats(observations, 0)
	if stats["reads"] != 1 {
		t.Fatalf("searched.reads=%v, want 1", stats["reads"])
	}
}

func TestLocalSearchAgentGrantReadToolExtractsPDFText(t *testing.T) {
	root := t.TempDir()
	writeLocalSearchAgentPDFTestFile(t, filepath.Join(root, "forecast.pdf"), "Local Search PDF Alpha")
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	msg, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-read-pdf",
		Query:        "alpha",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_read_pdf",
		Name: "local_read_grant_file",
		Args: map[string]any{"grant_id": "grant-x", "file_ref": "forecast.pdf"},
	}, nil)
	if err != nil {
		t.Fatalf("executeGrantTool: %v", err)
	}
	if msg.Content == nil {
		t.Fatal("message content is nil")
	}
	if len(observations.items) != 1 || observations.items[0].ToolName != "local_read_grant_file" || !observations.items[0].Output.OK {
		t.Fatalf("observations=%#v, want one successful PDF read", observations.items)
	}
	result, ok := observations.items[0].Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("read result=%T, want map", observations.items[0].Output.Result)
	}
	if result["file_kind"] != "pdf" || result["source"] != "pdf_extraction" {
		t.Fatalf("read result=%#v, want PDF extraction result", result)
	}
	if content := fmtSprintForLocalSearchTest(result["content"]); !strings.Contains(content, "Local Search PDF Alpha") {
		t.Fatalf("content=%q, want extracted PDF text", content)
	}
}

func TestLocalSearchAgentListsThenReadsPDFCandidate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeLocalSearchAgentPDFTestFile(t, filepath.Join(root, "reports", "forecast.pdf"), "Listed PDF Candidate Alpha")
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	_, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-list-read-pdf",
		Query:        "reports",
		Path:         "reports",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
		MaxResults:   10,
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_list_pdf",
		Name: "local_list_grant_folder",
		Args: map[string]any{"grant_id": "grant-x"},
	}, nil)
	if err != nil {
		t.Fatalf("execute list tool: %v", err)
	}
	refs := localSearchAgentFileRefs(t, observations.items[0].Output)
	if !containsStringForTest(refs, "reports/forecast.pdf") {
		t.Fatalf("listed refs=%v, want reports/forecast.pdf", refs)
	}

	_, err = service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-list-read-pdf",
		Query:        "alpha",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_read_listed_pdf",
		Name: "local_read_grant_file",
		Args: map[string]any{"grant_id": "grant-x", "file_ref": "reports/forecast.pdf"},
	}, nil)
	if err != nil {
		t.Fatalf("execute read tool: %v", err)
	}
	if len(observations.items) != 2 || observations.items[1].ToolName != "local_read_grant_file" || !observations.items[1].Output.OK {
		t.Fatalf("observations=%#v, want list then successful read", observations.items)
	}
	result, ok := observations.items[1].Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("read result=%T, want map", observations.items[1].Output.Result)
	}
	if content := fmtSprintForLocalSearchTest(result["content"]); !strings.Contains(content, "Listed PDF Candidate Alpha") {
		t.Fatalf("content=%q, want listed PDF extracted text", content)
	}
}

func TestLocalSearchAgentListsThenReadsXLSXCandidate(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "revenue.xlsx"), localSearchAgentTestXLSX(t), 0o644); err != nil {
		t.Fatalf("WriteFile xlsx: %v", err)
	}
	registry := tools.NewRegistry(nil)
	service := &localSearchAgentService{registry: registry}
	observations := &localSearchAgentObservations{}
	_, err := service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-list-read-xlsx",
		Query:        "reports",
		Path:         "reports",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
		MaxResults:   10,
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_list_xlsx",
		Name: "local_list_grant_folder",
		Args: map[string]any{"grant_id": "grant-x"},
	}, nil)
	if err != nil {
		t.Fatalf("execute list tool: %v", err)
	}
	refs := localSearchAgentFileRefs(t, observations.items[0].Output)
	if !containsStringForTest(refs, "reports/revenue.xlsx") {
		t.Fatalf("listed refs=%v, want reports/revenue.xlsx", refs)
	}

	_, err = service.executeGrantTool(context.Background(), localSearchAgentScope{
		JobID:        "job-list-read-xlsx",
		Query:        "north revenue",
		LocalRoot:    root,
		LocalKind:    "folder",
		AgentGrantID: "grant-x",
	}, tools.ExecContext{}, observations, types.ToolCall{
		ID:   "agent_tool_read_listed_xlsx",
		Name: "local_read_grant_file",
		Args: map[string]any{"grant_id": "grant-x", "file_ref": "reports/revenue.xlsx"},
	}, nil)
	if err != nil {
		t.Fatalf("execute read tool: %v", err)
	}
	if len(observations.items) != 2 || observations.items[1].ToolName != "local_read_grant_file" || !observations.items[1].Output.OK {
		t.Fatalf("observations=%#v, want list then successful read", observations.items)
	}
	result, ok := observations.items[1].Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("read result=%T, want map", observations.items[1].Output.Result)
	}
	if result["file_kind"] != "spreadsheet" || result["source"] != "file_extraction" {
		t.Fatalf("read result=%#v, want spreadsheet extraction result", result)
	}
	content := fmtSprintForLocalSearchTest(result["content"])
	if !strings.Contains(content, "North") || !strings.Contains(content, "South") {
		t.Fatalf("content=%q, want listed XLSX extracted text", content)
	}
}

func TestLocalSearchAgentHostedToolPolicyDisablesProviderTools(t *testing.T) {
	policy := localSearchAgentHostedToolPolicy()
	capabilities := tools.ProviderHostedToolCapabilities()
	if len(capabilities) == 0 {
		t.Fatal("expected provider-hosted capabilities to exist")
	}
	for _, capability := range capabilities {
		if !policy.Disables(capability.ID) {
			t.Fatalf("hosted capability %q should be disabled for the local search agent", capability.ID)
		}
	}
	for _, id := range []string{
		"openai-responses/web_search",
		"openai-responses/code_interpreter",
		"anthropic-messages/web_search",
		"anthropic-messages/code_execution",
	} {
		if !policy.Disables(id) {
			t.Fatalf("expected %q disabled for local search agent", id)
		}
	}
}

func TestParseConfiguredLocalSearchScopeEligibility(t *testing.T) {
	if _, ok := parseConfiguredLocalSearchScope(json.RawMessage(`{"query":"alpha"}`)); ok {
		t.Fatal("scope without local_root should not be dispatch-eligible")
	}
	if _, ok := parseConfiguredLocalSearchScope(json.RawMessage(`{"local_root":"/tmp/x"}`)); ok {
		t.Fatal("scope without query should not be dispatch-eligible")
	}
	scope, ok := parseConfiguredLocalSearchScope(json.RawMessage(`{"local_root":"/tmp/x","query":"alpha"}`))
	if !ok {
		t.Fatal("scope with local_root and query should be eligible")
	}
	if scope.LocalKind != "folder" {
		t.Fatalf("default local_kind=%q, want folder", scope.LocalKind)
	}
	if scope.MaxResults != 20 {
		t.Fatalf("default max_results=%d, want 20", scope.MaxResults)
	}
	fileScope, ok := parseConfiguredLocalSearchScope(json.RawMessage(`{"local_root":"/tmp/x/doc.txt","local_kind":"file","query":"alpha","max_results":500}`))
	if !ok {
		t.Fatal("file scope should be eligible")
	}
	if fileScope.LocalKind != "file" {
		t.Fatalf("local_kind=%q, want file", fileScope.LocalKind)
	}
	if fileScope.MaxResults != 200 {
		t.Fatalf("max_results=%d, want clamp to 200", fileScope.MaxResults)
	}
}

// When configured args carry no local grant/root, dispatch must decline so the
// classic direct file_search path keeps running.
func TestDispatchConfiguredLocalFileSearchSkipsWithoutLocalRoot(t *testing.T) {
	manager := &Manager{tools: tools.NewRegistry(nil)}
	out, dispatched := manager.dispatchConfiguredLocalFileSearch(
		context.Background(),
		store.Thread{ID: "thread"},
		store.Run{ID: "run", Model: "model"},
		types.ToolCall{ID: "tc", Name: "file_search"},
		json.RawMessage(`{"query":"alpha"}`),
		tools.ExecContext{},
		nil,
	)
	if dispatched {
		t.Fatalf("dispatched=%v out=%#v, want no dispatch without a configured local root", dispatched, out.Result)
	}
}

func TestLocalSearchDeadlineReturnsRetryablePartialResult(t *testing.T) {
	if localSearchNameProbeTimeout+localSearchAgentRunTimeout+localSearchFallbackTimeout > localSearchOverallTimeout {
		t.Fatalf("phase budgets exceed overall timeout: probe=%s agent=%s fallback=%s overall=%s",
			localSearchNameProbeTimeout, localSearchAgentRunTimeout, localSearchFallbackTimeout, localSearchOverallTimeout)
	}
	out := localSearchDeadlineOutput(localSearchAgentScope{
		JobID: "local_search_deadline", Query: "quarterly forecast", GrantID: "grant-deadline",
		LocalRoot: "/tmp/reports", LocalKind: "folder",
	})
	if !out.OK {
		t.Fatalf("deadline output=%+v, want successful bounded partial result", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok || result["status"] != "partial" || result["timed_out"] != true || result["truncated"] != true || result["retryable"] != true {
		t.Fatalf("deadline result=%#v", out.Result)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(strings.ToLower(string(raw)), "context deadline exceeded") ||
		!strings.Contains(strings.ToLower(fmtSprintForLocalSearchTest(result["instructions"])), "retry") {
		t.Fatalf("deadline result is not actionable: %#v", result)
	}
	if out.ResultMetadata["timed_out"] != true || out.ResultMetadata["retryable"] != true {
		t.Fatalf("deadline metadata=%#v", out.ResultMetadata)
	}
}

func TestDispatchConfiguredLocalFileSearchDoesNotExposeContextDeadline(t *testing.T) {
	manager := &Manager{tools: tools.NewRegistry(nil)}
	deadlineCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	out, dispatched := manager.dispatchConfiguredLocalFileSearch(
		deadlineCtx,
		store.Thread{ID: "thread-deadline"},
		store.Run{ID: "run-deadline", Model: "model"},
		types.ToolCall{ID: "call-deadline", Name: "file_search"},
		json.RawMessage(`{"local_root":"`+t.TempDir()+`","local_kind":"folder","grant_id":"grant-deadline","query":"forecast"}`),
		tools.ExecContext{},
		nil,
	)
	if !dispatched || !out.OK {
		t.Fatalf("deadline dispatch=%v output=%+v", dispatched, out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(strings.ToLower(string(raw)), "context deadline exceeded") {
		t.Fatalf("deadline leaked through file_search output: %s", raw)
	}
	result, _ := out.Result.(map[string]any)
	if result["status"] != "partial" || result["timed_out"] != true || result["retryable"] != true {
		t.Fatalf("deadline result=%#v", result)
	}
}

// The in-memory job registry records a completed job after a dispatched run.
func TestManagerExecToolLocalSearchRecordsCompletedJob(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "fake", "fake-local-search-model", nil, "system", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), []byte("alpha forecast\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	registry := tools.NewRegistry(nil)
	provider := &fakeLocalSearchAgentProvider{model: "fake-local-search-model", grantID: "grant-test", query: "alpha", toolName: "local_search_in_grant"}
	manager := &Manager{
		store:  st,
		tools:  registry,
		cfgDir: cfgDir,
		localSearchAgent: &localSearchAgentService{
			registry:        registry,
			providerFactory: func(context.Context) (types.Provider, error) { return provider, nil },
		},
	}
	msg, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_job_lifecycle",
		Name: "file_search",
		ArgsText: mustJSONForLocalSearchAgentTest(map[string]any{
			"local_root": root,
			"local_kind": "folder",
			"grant_id":   "grant-test",
			"query":      "alpha",
		}),
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	env := decodeToolEnvelopeForLocalSearchAgentTest(t, *msg.Content)
	result := env["result"].(map[string]any)
	jobID, _ := result["job_id"].(string)
	if strings.TrimSpace(jobID) == "" {
		t.Fatalf("job_id missing: %#v", result)
	}
	job, ok := manager.localSearchJobSnapshot(jobID)
	if !ok {
		t.Fatalf("job %q not recorded", jobID)
	}
	if job.Status != "completed" {
		t.Fatalf("job status=%q, want completed", job.Status)
	}
	if job.Query != "alpha" || job.GrantID != "grant-test" {
		t.Fatalf("job=%#v, want query/grant recorded", job)
	}
}

func fmtSprintForLocalSearchTest(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func writeLocalSearchAgentPDFTestFile(t *testing.T, path string, text string) {
	t.Helper()
	if strings.ContainsAny(text, "()\\") {
		t.Fatalf("test PDF literal must not contain characters requiring PDF escaping: %q", text)
	}
	content := `%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>
endobj
4 0 obj
<< /Length 80 >>
stream
BT
/F1 12 Tf
72 720 Td
(` + text + `) Tj
ET
endstream
endobj
trailer
<< /Root 1 0 R >>
%%EOF
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write PDF fixture: %v", err)
	}
}

func localSearchAgentTestXLSX(t *testing.T) []byte {
	t.Helper()
	entries := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Revenue" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="s"><v>0</v></c>
      <c r="B1" t="s"><v>1</v></c>
    </row>
    <row r="2">
      <c r="A2" t="s"><v>2</v></c>
      <c r="B2"><v>10</v></c>
    </row>
    <row r="3">
      <c r="A3" t="s"><v>3</v></c>
      <c r="B3"><v>8</v></c>
    </row>
  </sheetData>
</worksheet>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="4" uniqueCount="4">
  <si><t>region</t></si>
  <si><t>q1</t></si>
  <si><t>North</t></si>
  <si><t>South</t></si>
</sst>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create xlsx entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(entries[name])); err != nil {
			t.Fatalf("write xlsx entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close xlsx: %v", err)
	}
	return buf.Bytes()
}
