package permissions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/store"
)

func newTestService(t *testing.T) (*Service, store.Store, string) {
	t.Helper()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	svc, err := NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return svc, st, cfgDir
}

func seedRun(t *testing.T, st store.Store) (store.Thread, store.Run) {
	t.Helper()
	ctx := context.Background()
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return thread, run
}

func TestApproveThreadCreatesThreadScopedRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID:   thread.ID,
		RunID:      run.ID,
		ToolCallID: "call_1",
		Tool:       "rlm_exec",
		Category:   CategoryExecute,
		Operation:  "execute",
		Subject:    store.PermissionSubject{Kind: "tool", Tool: "rlm_exec", Operation: "execute"},
		Summary:    "Allow execution",
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	approved, err := svc.Approve(ctx, approval.ID, "thread")
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.Status != "approved" {
		t.Fatalf("approved.Status = %q, want approved", approved.Status)
	}

	eval, err := svc.Evaluate(ctx, EvaluationInput{
		ThreadID: thread.ID,
		Tool:     "rlm_exec",
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec", Operation: "execute"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("eval.Decision = %q, want allow", eval.Decision)
	}

	otherThread, _ := seedRun(t, st)
	eval, err = svc.Evaluate(ctx, EvaluationInput{
		ThreadID: otherThread.ID,
		Tool:     "rlm_exec",
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec", Operation: "execute"},
	})
	if err != nil {
		t.Fatalf("Evaluate other thread: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("other thread eval.Decision = %q, want ask", eval.Decision)
	}
}

func TestApproveRejectsUnofferedScope(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID:  thread.ID,
		RunID:     run.ID,
		Tool:      "rlm_exec",
		Category:  CategoryExecute,
		Operation: "execute",
		Subject:   store.PermissionSubject{Kind: "tool", Tool: "rlm_exec", Operation: "execute"},
		Summary:   "Allow execution",
		Options:   []string{"once", "thread"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := svc.Approve(ctx, approval.ID, "always"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("Approve always err = %v, want ErrConflict", err)
	}
	current, err := svc.GetApproval(ctx, approval.ID)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if current.Status != "pending" {
		t.Fatalf("status after rejected scope=%q, want pending", current.Status)
	}
	approved, err := svc.Approve(ctx, approval.ID, "thread")
	if err != nil {
		t.Fatalf("Approve thread: %v", err)
	}
	if approved.Status != "approved" || approved.Decision == nil || approved.Decision.Scope != "thread" {
		t.Fatalf("approved=%#v, want approved thread decision", approved)
	}
}

func TestTestLocalLocationCanonicalizesReadableFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	root := t.TempDir()

	result, err := svc.TestLocalLocation(ctx, store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: root,
	})
	if err != nil {
		t.Fatalf("TestLocalLocation: %v", err)
	}
	if result.Kind != "folder" || !result.Readable {
		t.Fatalf("result=%#v, want readable folder", result)
	}
	if result.Subject.Kind != "local_location" || result.Subject.LocalRoot != canonicalPermissionTestPath(root) {
		t.Fatalf("subject=%#v, want canonical local_location %q", result.Subject, canonicalPermissionTestPath(root))
	}
	if len(result.Subject.Excludes) == 0 {
		t.Fatalf("subject excludes empty, want default sensitive excludes")
	}
}

func TestTestLocalLocationCanonicalizesReadableFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	root := t.TempDir()
	filePath := filepath.Join(root, "report.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	result, err := svc.TestLocalLocation(ctx, store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: filePath,
		LocalKind: "file",
	})
	if err != nil {
		t.Fatalf("TestLocalLocation: %v", err)
	}
	if result.Kind != "file" || !result.Readable {
		t.Fatalf("result=%#v, want readable file", result)
	}
	if result.Subject.Kind != "local_location" || result.Subject.LocalKind != "file" || result.Subject.LocalRoot != canonicalPermissionTestPath(filePath) {
		t.Fatalf("subject=%#v, want canonical file local_location %q", result.Subject, canonicalPermissionTestPath(filePath))
	}
}

func TestTestLocalLocationRejectsFileWhenFolderKindRequested(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	root := t.TempDir()
	filePath := filepath.Join(root, "report.txt")
	if err := os.WriteFile(filePath, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := svc.TestLocalLocation(ctx, store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: filePath,
		LocalKind: "folder",
	}); err == nil {
		t.Fatal("TestLocalLocation succeeded for file path with folder kind, want error")
	}
}

func TestApproveWithLocalSubjectOverrideCreatesGrantRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)
	root := t.TempDir()

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "file_search",
		Category: CategoryRead,
		Subject: store.PermissionSubject{
			Kind:     "local_location",
			Excludes: []string{"*.pem"},
		},
		Summary: "Allow local file search",
		Options: []string{"once", "thread", "always"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	approved, err := svc.ApproveWithOptions(ctx, approval.ID, ApproveOptions{
		Scope: "thread",
		Subject: &store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: filepath.Join(root, "."),
			LocalKind: "folder",
		},
	})
	if err != nil {
		t.Fatalf("ApproveWithOptions: %v", err)
	}
	if approved.Decision == nil || approved.Decision.Subject == nil {
		t.Fatalf("approved decision=%#v, want selected local subject", approved.Decision)
	}
	wantRoot := canonicalPermissionTestPath(root)
	if got := filepath.Clean(approved.Decision.Subject.LocalRoot); got != wantRoot {
		t.Fatalf("decision local_root=%q, want %q", got, wantRoot)
	}
	if approved.Decision.Subject.LocalKind != "folder" {
		t.Fatalf("decision local_kind=%q, want folder", approved.Decision.Subject.LocalKind)
	}
	if !containsStringForPermissionTest(approved.Decision.Subject.Excludes, "*.pem") {
		t.Fatalf("decision excludes=%v, want request excludes preserved", approved.Decision.Subject.Excludes)
	}

	eval, err := svc.Evaluate(ctx, EvaluationInput{
		ThreadID: thread.ID,
		Tool:     "file_search",
		Category: CategoryRead,
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: filepath.Join(wantRoot, "report.txt"),
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("eval.Decision=%q, want allow from selected local grant", eval.Decision)
	}

	otherThread, _ := seedRun(t, st)
	eval, err = svc.Evaluate(ctx, EvaluationInput{
		ThreadID: otherThread.ID,
		Tool:     "file_search",
		Category: CategoryRead,
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: filepath.Join(wantRoot, "report.txt"),
		},
	})
	if err != nil {
		t.Fatalf("Evaluate other thread: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("other thread eval.Decision=%q, want ask for thread-scoped local grant", eval.Decision)
	}
}

func TestApproveWithLocalSubjectOverrideAllowsComputerRootGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "file_search",
		Category: CategoryRead,
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalKind: "folder",
		},
		Summary: "Allow local file search",
		Options: []string{"once", "thread", "always"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	approved, err := svc.ApproveWithOptions(ctx, approval.ID, ApproveOptions{
		Scope: "once",
		Subject: &store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: "/",
			LocalKind: "folder",
		},
	})
	if err != nil {
		t.Fatalf("ApproveWithOptions: %v", err)
	}
	if approved.Decision == nil || approved.Decision.Subject == nil {
		t.Fatalf("approved decision=%#v, want selected computer root subject", approved.Decision)
	}
	wantRoot := canonicalPermissionTestPath("/")
	if got := filepath.Clean(approved.Decision.Subject.LocalRoot); got != wantRoot {
		t.Fatalf("decision local_root=%q, want %q", got, wantRoot)
	}
	if approved.Decision.Subject.LocalKind != "folder" {
		t.Fatalf("decision local_kind=%q, want folder", approved.Decision.Subject.LocalKind)
	}
	if !containsStringForPermissionTest(approved.Decision.Subject.Excludes, ".env") ||
		!containsStringForPermissionTest(approved.Decision.Subject.Excludes, "**/.ssh/**") {
		t.Fatalf("decision excludes=%v, want default sensitive excludes", approved.Decision.Subject.Excludes)
	}
}

func TestLocalFileGrantMatchesOnlySameFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)
	root := t.TempDir()
	filePath := filepath.Join(root, "report.txt")
	siblingPath := filepath.Join(root, "sibling.txt")

	if _, err := svc.CreateRule(ctx, PermissionRule{
		Category: CategoryRead,
		Effect:   EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: filePath,
			LocalKind: "file",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryRead,
		Subject:  store.PermissionSubject{Kind: "local_location", LocalRoot: filePath, LocalKind: "file"},
	})
	if err != nil {
		t.Fatalf("Evaluate same file: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("same file decision=%q, want allow", eval.Decision)
	}

	eval, err = svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryRead,
		Subject:  store.PermissionSubject{Kind: "local_location", LocalRoot: siblingPath, LocalKind: "file"},
	})
	if err != nil {
		t.Fatalf("Evaluate sibling: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("sibling decision=%q, want ask", eval.Decision)
	}
}

func TestApproveWithSubjectOverrideRejectsNonLocalApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID:  thread.ID,
		RunID:     run.ID,
		Tool:      "github_clone_repository",
		Category:  CategoryExecute,
		Operation: "clone_repository",
		Subject: store.PermissionSubject{
			Kind:       "repository",
			Repository: "owner/repo",
		},
		Summary: "Clone owner/repo",
		Options: []string{"once", "thread"},
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if _, err := svc.ApproveWithOptions(ctx, approval.ID, ApproveOptions{
		Scope: "once",
		Subject: &store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: t.TempDir(),
		},
	}); err == nil {
		t.Fatal("ApproveWithOptions succeeded for non-local subject override, want error")
	}
	current, err := svc.GetApproval(ctx, approval.ID)
	if err != nil {
		t.Fatalf("GetApproval: %v", err)
	}
	if current.Status != "pending" {
		t.Fatalf("status=%q, want pending after rejected override", current.Status)
	}
}

func TestLocalLocationReadDefaultsAskAndRuleAllowsChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryRead,
		Subject:  store.PermissionSubject{Kind: "local_location", LocalRoot: "/tmp/project"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("local read default = %q, want ask", eval.Decision)
	}

	rule, err := svc.CreateRule(ctx, PermissionRule{
		Category:   CategoryRead,
		Effect:     EffectAllow,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: "/tmp/project"},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if !containsStringForPermissionTest(rule.Subject.Excludes, ".env") || !containsStringForPermissionTest(rule.Subject.Excludes, ".config/gh/**") {
		t.Fatalf("default local excludes=%v, want sensitive defaults", rule.Subject.Excludes)
	}
	eval, err = svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryRead,
		Subject:  store.PermissionSubject{Kind: "local_location", LocalRoot: "/tmp/project/subdir/file.txt"},
	})
	if err != nil {
		t.Fatalf("Evaluate after rule: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("local read after rule = %q, want allow", eval.Decision)
	}
}

func TestLocalLocationRuleExcludesSensitiveChildren(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	_, err := svc.CreateRule(ctx, PermissionRule{
		Category: CategoryRead,
		Effect:   EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: "/tmp/project",
			Excludes:  []string{".env", ".env.*", "**/.ssh/**", "*.pem"},
		},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	for _, path := range []string{
		"/tmp/project/README.md",
		"/tmp/project/src/app.ts",
	} {
		eval, err := svc.Evaluate(ctx, EvaluationInput{
			Category: CategoryRead,
			Subject:  store.PermissionSubject{Kind: "local_location", LocalRoot: path},
		})
		if err != nil {
			t.Fatalf("Evaluate %s: %v", path, err)
		}
		if eval.Decision != DecisionAllow {
			t.Fatalf("local read for %s = %q, want allow", path, eval.Decision)
		}
	}

	for _, path := range []string{
		"/tmp/project/.env",
		"/tmp/project/config/.env.local",
		"/tmp/project/.ssh/id_ed25519",
		"/tmp/project/secrets/client.pem",
	} {
		eval, err := svc.Evaluate(ctx, EvaluationInput{
			Category: CategoryRead,
			Subject:  store.PermissionSubject{Kind: "local_location", LocalRoot: path},
		})
		if err != nil {
			t.Fatalf("Evaluate %s: %v", path, err)
		}
		if eval.Decision != DecisionAsk {
			t.Fatalf("local read for excluded %s = %q, want default ask", path, eval.Decision)
		}
	}

	_, err = svc.CreateRule(ctx, PermissionRule{
		Category: CategoryRead,
		Effect:   EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: "/tmp/project/.env",
			Excludes:  []string{".env", ".env.*"},
		},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("Create direct .env rule: %v", err)
	}
	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryRead,
		Subject:  store.PermissionSubject{Kind: "local_location", LocalRoot: "/tmp/project/.env"},
	})
	if err != nil {
		t.Fatalf("Evaluate direct .env: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("direct .env read = %q, want allow", eval.Decision)
	}
}

func TestLocalLocationInputExcludesProtectLegacyBroadRule(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	legacyRule := normalizeRule(PermissionRule{
		ID:         "perm_legacy_local",
		Category:   CategoryRead,
		Effect:     EffectAllow,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: "/tmp/project"},
		CreatedVia: "legacy",
		CreatedAt:  time.Now().UTC(),
	})
	legacyRule.Subject.Excludes = nil
	if err := svc.writeRules([]PermissionRule{legacyRule}); err != nil {
		t.Fatalf("write legacy rule: %v", err)
	}

	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryRead,
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: "/tmp/project/README.md",
			Excludes:  store.DefaultSensitiveLocalPermissionExcludes(),
		},
	})
	if err != nil {
		t.Fatalf("Evaluate README: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("README decision=%q, want allow", eval.Decision)
	}

	eval, err = svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryRead,
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: "/tmp/project/.docker/config.json",
			Excludes:  store.DefaultSensitiveLocalPermissionExcludes(),
		},
	})
	if err != nil {
		t.Fatalf("Evaluate docker config: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("docker config decision=%q, want ask despite legacy broad allow", eval.Decision)
	}
}

func TestToolSetRuleRequiresToolSetIDAndMatchesToolSetSubject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	_, err := svc.CreateRule(ctx, PermissionRule{
		Category:   CategoryExecute,
		Effect:     EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "tool_set"},
		CreatedVia: "test",
	})
	if err == nil {
		t.Fatal("CreateRule without tool_set_id succeeded, want validation error")
	}

	_, err = svc.CreateRule(ctx, PermissionRule{
		Category:   CategoryExecute,
		Effect:     EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "tool_set", ToolSetID: "github", Connector: "github"},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool_set", ToolSetID: "github", Connector: "github"},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval.Decision != DecisionDeny {
		t.Fatalf("tool set eval.Decision = %q, want deny", eval.Decision)
	}
	eval, err = svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool_set", ToolSetID: "slack", Connector: "slack"},
	})
	if err != nil {
		t.Fatalf("Evaluate other tool set: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("other tool set eval.Decision = %q, want default ask", eval.Decision)
	}
}

func TestRemoteResourceRuleMatchesOnlySameRemoteTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	_, err := svc.CreateRule(ctx, PermissionRule{
		Category: CategoryWrite,
		Effect:   EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "slack_post_message",
			Connector:  "slack",
			Operation:  "slack_post_message_execute",
			RemoteKind: "slack_channel",
			RemoteID:   "C123",
		},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryWrite,
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "slack_post_message",
			Connector:  "slack",
			Operation:  "slack_post_message_execute",
			RemoteKind: "slack_channel",
			RemoteID:   "C123",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate matching remote target: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("matching remote eval.Decision = %q, want allow", eval.Decision)
	}

	eval, err = svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryWrite,
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "slack_post_message",
			Connector:  "slack",
			Operation:  "slack_post_message_execute",
			RemoteKind: "slack_channel",
			RemoteID:   "C999",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate different remote target: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("different remote eval.Decision = %q, want ask", eval.Decision)
	}
}

func TestRemoteResourceRulePreservesCaseSensitiveRemoteID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)

	_, err := svc.CreateRule(ctx, PermissionRule{
		Category: CategoryWrite,
		Effect:   EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "slack_upload_file",
			Connector:  "slack",
			Operation:  "slack_upload_file_execute",
			RemoteKind: "slack_file",
			RemoteID:   "FileABC",
		},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryWrite,
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "slack_upload_file",
			Connector:  "slack",
			Operation:  "slack_upload_file_execute",
			RemoteKind: "slack_file",
			RemoteID:   "FileABC",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate matching case: %v", err)
	}
	if eval.Decision != DecisionAllow {
		t.Fatalf("matching case eval.Decision = %q, want allow", eval.Decision)
	}

	eval, err = svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryWrite,
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "slack_upload_file",
			Connector:  "slack",
			Operation:  "slack_upload_file_execute",
			RemoteKind: "slack_file",
			RemoteID:   "fileabc",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate different case: %v", err)
	}
	if eval.Decision != DecisionAsk {
		t.Fatalf("different case eval.Decision = %q, want ask", eval.Decision)
	}

	thread, run := seedRun(t, st)
	approval, err := svc.Request(ctx, RequestInput{
		ThreadID:  thread.ID,
		RunID:     run.ID,
		Tool:      "slack_download_file",
		Category:  CategoryRead,
		Operation: "slack_download_file",
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "slack_download_file",
			Connector:  "slack",
			Operation:  "slack_download_file",
			RemoteKind: "slack_file",
			RemoteID:   "FileABC",
		},
		Summary: "Download Slack file FileABC",
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if approval.Subject.RemoteID != "FileABC" {
		t.Fatalf("approval.Subject.RemoteID=%q, want FileABC", approval.Subject.RemoteID)
	}
}

func TestBroadConnectorRuleStillMatchesRemoteResourceSubject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	_, err := svc.CreateRule(ctx, PermissionRule{
		Category:   CategoryWrite,
		Effect:     EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "slack"},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	eval, err := svc.Evaluate(ctx, EvaluationInput{
		Category: CategoryWrite,
		Subject: store.PermissionSubject{
			Kind:       "connector",
			Connector:  "slack",
			RemoteKind: "slack_channel",
			RemoteID:   "C123",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if eval.Decision != DecisionDeny {
		t.Fatalf("connector eval.Decision = %q, want deny", eval.Decision)
	}
}

func TestUpdateRuleAcceptsPartialPatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := newTestService(t)

	created, err := svc.CreateRule(ctx, PermissionRule{
		Category:   CategoryExecute,
		Effect:     EffectAsk,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "tool", Tool: "rlm_exec", Operation: "execute"},
		CreatedVia: "test",
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	updated, err := svc.UpdateRule(ctx, created.ID, PermissionRule{Effect: EffectDeny})
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if updated.Effect != EffectDeny {
		t.Fatalf("updated.Effect = %q, want deny", updated.Effect)
	}
	if updated.Category != created.Category {
		t.Fatalf("updated.Category = %q, want %q", updated.Category, created.Category)
	}
	if updated.Subject.Tool != created.Subject.Tool {
		t.Fatalf("updated.Subject.Tool = %q, want %q", updated.Subject.Tool, created.Subject.Tool)
	}
}

func TestAwaitReturnsDecisionNotification(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "rlm_exec",
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:  "Allow execution",
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	done := make(chan store.ApprovalRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		updated, err := svc.Await(ctx, approval.ID, time.Second)
		if err != nil {
			errCh <- err
			return
		}
		done <- updated
	}()
	time.Sleep(10 * time.Millisecond)
	if _, err := svc.Reject(ctx, approval.ID); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	select {
	case err := <-errCh:
		t.Fatalf("Await error: %v", err)
	case updated := <-done:
		if updated.Status != "rejected" {
			t.Fatalf("Await status = %q, want rejected", updated.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Await")
	}
}

func TestAwaitTimeoutIsDeadlineExceeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "rlm_exec",
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:  "Allow execution",
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	_, err = svc.Await(ctx, approval.ID, time.Nanosecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Await err = %v, want DeadlineExceeded", err)
	}
}

func TestRequestOrReuseReturnsExistingPendingApprovalForSameKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	input := RequestInput{
		ThreadID:   thread.ID,
		RunID:      run.ID,
		ToolCallID: "tc_clone",
		Tool:       "github_clone_repository",
		Category:   CategoryExecute,
		Operation:  "clone_repository",
		Subject: store.PermissionSubject{
			Kind:       "repository",
			Connector:  "GitHub",
			Repository: "Owner/Repo",
		},
		Summary: "Clone Owner/Repo",
		Preview: map[string]any{"repository": "Owner/Repo"},
		Options: []string{"once", "thread", "always"},
	}
	first, reused, err := svc.RequestOrReuse(ctx, input)
	if err != nil {
		t.Fatalf("RequestOrReuse first: %v", err)
	}
	if reused {
		t.Fatal("first request reused=true, want false")
	}

	secondInput := input
	secondInput.Summary = "Different wording should not create a duplicate"
	secondInput.Preview = map[string]any{"repository": "owner/repo"}
	second, reused, err := svc.RequestOrReuse(ctx, secondInput)
	if err != nil {
		t.Fatalf("RequestOrReuse second: %v", err)
	}
	if !reused {
		t.Fatal("second request reused=false, want true")
	}
	if second.ID != first.ID {
		t.Fatalf("second.ID=%q, want reused ID %q", second.ID, first.ID)
	}

	pending, err := svc.ListApprovals(ctx, ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending approvals=%d, want 1", len(pending))
	}

	differentCallInput := input
	differentCallInput.ToolCallID = "tc_clone_again"
	third, reused, err := svc.RequestOrReuse(ctx, differentCallInput)
	if err != nil {
		t.Fatalf("RequestOrReuse different call: %v", err)
	}
	if reused {
		t.Fatal("different tool call reused=true, want false")
	}
	if third.ID == first.ID {
		t.Fatalf("different tool call reused first ID %q", first.ID)
	}
}

func TestRequestOrReuseDedupesConcurrentSameKeyRequests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)
	input := RequestInput{
		ThreadID:   thread.ID,
		RunID:      run.ID,
		ToolCallID: "tc_clone",
		Tool:       "github_clone_repository",
		Category:   CategoryExecute,
		Operation:  "clone_repository",
		Subject: store.PermissionSubject{
			Kind:       "repository",
			Connector:  "github",
			Repository: "owner/repo",
		},
		Summary: "Clone owner/repo",
		Options: []string{"once", "thread", "always"},
	}

	const workers = 12
	var wg sync.WaitGroup
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			approval, _, err := svc.RequestOrReuse(ctx, input)
			if err != nil {
				errs <- err
				return
			}
			ids <- approval.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatalf("RequestOrReuse concurrent: %v", err)
	}
	seen := map[string]struct{}{}
	for id := range ids {
		seen[id] = struct{}{}
	}
	if len(seen) != 1 {
		t.Fatalf("approval IDs=%#v, want exactly one reused ID", seen)
	}
	pending, err := svc.ListApprovals(ctx, ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending approvals=%d, want 1", len(pending))
	}
}

func TestAwaitWithoutTimeoutWaitsForDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "rlm_exec",
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:  "Allow execution",
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}

	done := make(chan store.ApprovalRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		updated, err := svc.Await(ctx, approval.ID, 0)
		if err != nil {
			errCh <- err
			return
		}
		done <- updated
	}()
	select {
	case err := <-errCh:
		t.Fatalf("Await error before decision: %v", err)
	case updated := <-done:
		t.Fatalf("Await returned before decision: %#v", updated)
	case <-time.After(20 * time.Millisecond):
	}

	if _, err := svc.Approve(ctx, approval.ID, "once"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	select {
	case err := <-errCh:
		t.Fatalf("Await error: %v", err)
	case updated := <-done:
		if updated.Status != "approved" {
			t.Fatalf("Await status = %q, want approved", updated.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Await")
	}
}

func TestCancelRunApprovalRequestsNotifiesAwaiters(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, st, _ := newTestService(t)
	thread, run := seedRun(t, st)

	approval, err := svc.Request(ctx, RequestInput{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "rlm_exec",
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:  "Allow execution",
	})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	otherThread, otherRun := seedRun(t, st)
	otherApproval, err := svc.Request(ctx, RequestInput{
		ThreadID: otherThread.ID,
		RunID:    otherRun.ID,
		Tool:     "rlm_exec",
		Category: CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:  "Allow execution for another run",
	})
	if err != nil {
		t.Fatalf("Request other: %v", err)
	}

	done := make(chan store.ApprovalRequest, 1)
	errCh := make(chan error, 1)
	go func() {
		updated, err := svc.Await(ctx, approval.ID, time.Second)
		if err != nil {
			errCh <- err
			return
		}
		done <- updated
	}()
	time.Sleep(10 * time.Millisecond)
	canceled, err := svc.CancelRunApprovalRequests(ctx, run.ID)
	if err != nil {
		t.Fatalf("CancelRunApprovalRequests: %v", err)
	}
	if len(canceled) != 1 || canceled[0].ID != approval.ID || canceled[0].Status != "canceled" {
		t.Fatalf("canceled approvals = %#v, want only canceled approval %s", canceled, approval.ID)
	}

	select {
	case err := <-errCh:
		t.Fatalf("Await error: %v", err)
	case updated := <-done:
		if updated.Status != "canceled" {
			t.Fatalf("Await status = %q, want canceled", updated.Status)
		}
		if updated.Decision == nil || updated.Decision.Action != "cancel" {
			t.Fatalf("Await decision = %#v, want cancel decision", updated.Decision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Await after run approval cancellation")
	}
	other, err := svc.GetApproval(ctx, otherApproval.ID)
	if err != nil {
		t.Fatalf("GetApproval other: %v", err)
	}
	if other.Status != "pending" {
		t.Fatalf("other run approval status = %q, want pending", other.Status)
	}
}

func containsStringForPermissionTest(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func canonicalPermissionTestPath(path string) string {
	clean := filepath.Clean(path)
	if evaluated, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(evaluated)
	}
	return clean
}
