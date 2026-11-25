package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalSearchCommandDefinitionDeclaresBoundedSchema(t *testing.T) {
	def := newLocalSearchCommandTool().Definition()
	if def.Type != "function" || def.Function.Name != LocalSearchCommandToolName {
		t.Fatalf("definition=%#v, want %s function", def, LocalSearchCommandToolName)
	}
	params := def.Function.Parameters
	if got, _ := params["type"].(string); got != "object" {
		t.Fatalf("parameters.type=%#v, want object", params["type"])
	}
	if additional, ok := params["additionalProperties"].(bool); !ok || additional {
		t.Fatalf("additionalProperties=%#v, want false", params["additionalProperties"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T, want map", params["properties"])
	}
	for _, name := range []string{"grant_id", "program", "query", "relative_path", "args", "file_types", "regex", "case_sensitive", "max_results", "timeout_ms", "max_file_bytes", "dry_run"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("properties missing %q: %#v", name, properties)
		}
	}
	if _, ok := properties["local_root"]; ok {
		t.Fatalf("properties expose application-injected local_root: %#v", properties)
	}
	for _, name := range []string{"grant_id", "program", "query"} {
		if !containsString(toolMetadataRequiredFields(params), name) {
			t.Fatalf("required=%v, want %s", toolMetadataRequiredFields(params), name)
		}
	}
}

func TestLocalSearchCommandDryRunDoesNotStartProcess(t *testing.T) {
	root := t.TempDir()
	out, err := newLocalSearchCommandTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"grant_id":   "local-grant",
		"local_root": root,
		"program":    "rg",
		"query":      "needle",
		"args":       []string{"--glob", "*.txt"},
		"dry_run":    true,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok || result["dry_run"] != true || result["validated"] != true {
		t.Fatalf("result=%#v, want validated dry run", out.Result)
	}
	if out.ResultMetadata["tool_name"] != LocalSearchCommandToolName || out.ResultMetadata["filesystem_only"] != true {
		t.Fatalf("metadata=%#v, want bounded local command metadata", out.ResultMetadata)
	}
}

func TestLocalSearchCommandRejectsShellSyntaxAndExecutionHooks(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		program string
		args    []string
		want    string
	}{
		{name: "shell separator", program: "rg", args: []string{"--glob", "*.txt; touch escaped"}, want: "shell syntax"},
		{name: "ripgrep preprocessor", program: "rg", args: []string{"--pre", "cat"}, want: "not allowed"},
		{name: "fd exec", program: "fd", args: []string{"--exec", "cat"}, want: "not allowed"},
		{name: "unknown program", program: "bash", want: "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := newLocalSearchCommandTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
				"grant_id":   "local-grant",
				"local_root": root,
				"program":    tc.program,
				"query":      "needle",
				"args":       tc.args,
			}))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if out.OK || !strings.Contains(strings.ToLower(outputResultStringForLocalMachineTest(out)), tc.want) {
				t.Fatalf("output=%#v, want rejection containing %q", out, tc.want)
			}
		})
	}
}

func TestLocalSearchCommandSensitiveExcludesCannotBeOverriddenByModelGlobs(t *testing.T) {
	req := localSearchBrokerRequest{
		Query: "needle",
		Args:  []string{"--glob", ".env"},
	}
	args := buildLocalRGContentArgs(req)
	includeIndex := localSearchCommandArgPairIndex(args, "--glob", ".env")
	excludeIndex := localSearchCommandArgPairIndex(args, "--glob", "!.env")
	if includeIndex < 0 || excludeIndex <= includeIndex {
		t.Fatalf("args=%v, want sensitive exclusion after model-supplied include glob", args)
	}
}

func TestLocalSearchCommandRipgrepNameFallbackPreFiltersLiteralQuery(t *testing.T) {
	args := buildLocalRGFileArgs(localSearchBrokerRequest{Query: "Quarter[1]"})
	if localSearchCommandArgPairIndex(args, "--iglob", `*Quarter\[1\]*`) < 0 {
		t.Fatalf("args=%v, want escaped case-insensitive filename glob", args)
	}
	if localSearchCommandArgPairIndex(args, "--iglob", `**/*Quarter\[1\]*/**`) < 0 {
		t.Fatalf("args=%v, want matching-directory filename glob", args)
	}
	regexArgs := buildLocalRGFileArgs(localSearchBrokerRequest{Query: "Quarter.*", Regex: true})
	if containsString(regexArgs, "--iglob") {
		t.Fatalf("regex args=%v, regex filename matching must not be rewritten as a glob", regexArgs)
	}
}

func TestNormalizeLocalFileSearchQueryRemovesQuotesAndFileExtension(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		fileTypes []string
		want      string
	}{
		{name: "quoted", query: `"Linear Orbit"`, want: "Linear Orbit"},
		{name: "extension token", query: "linear orbit invoice pdf", fileTypes: []string{"pdf"}, want: "linear orbit invoice"},
		{name: "dotted extension token", query: "linear orbit invoice .PDF", fileTypes: []string{"pdf"}, want: "linear orbit invoice"},
		{name: "preserve meaningful suffix", query: "annual pdf report", fileTypes: []string{"pdf"}, want: "annual pdf report"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeLocalFileSearchQuery(tc.query, tc.fileTypes); got != tc.want {
				t.Fatalf("normalizeLocalFileSearchQuery(%q)=%q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

func TestLocalSearchCommandFilenameModeReturnsDocumentWithoutContentScan(t *testing.T) {
	if _, fdOK := resolveLocalSearchExecutable("fd"); !fdOK {
		if _, rgOK := resolveLocalSearchExecutable("rg"); !rgOK {
			t.Skip("fd and ripgrep are unavailable in this test environment")
		}
	}
	root := t.TempDir()
	writeLocalSearchCommandTestFile(t, filepath.Join(root, "Linear Orbit Invoice.pdf"), "%PDF binary-ish data")

	out, err := newFileSearchTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root":  root,
		"grant_id":    "local-grant",
		"query":       "linear orbit invoice pdf",
		"file_types":  []string{"pdf"},
		"search_mode": "name",
		"max_results": 10,
	}))
	if err != nil || !out.OK {
		t.Fatalf("search err=%v output=%#v", err, out)
	}
	result := out.Result.(map[string]any)
	files, _ := result["files"].([]map[string]any)
	if got := localSearchCommandFileRefs(files); len(got) != 1 || got[0] != "Linear Orbit Invoice.pdf" {
		t.Fatalf("files=%#v, want normalized PDF filename match", files)
	}
	commands, _ := result["commands"].([]localSearchBrokerCommand)
	for _, command := range commands {
		if command.Backend == "rg" {
			t.Fatalf("commands=%#v, name mode must not scan file contents", commands)
		}
	}
}

func TestLocalSearchCommandRejectsOversizedArguments(t *testing.T) {
	_, err := validateLocalSearchCommandArgs("rg", []string{"--glob", strings.Repeat("a", maxLocalSearchCommandArgBytes+1)})
	if err == nil {
		t.Fatal("oversized argument accepted")
	}
}

func TestLocalSearchCommandRejectsFileTypeGlobs(t *testing.T) {
	if err := validateLocalSearchCommandFileTypes([]string{"pdf", "../*.env"}); err == nil {
		t.Fatal("file_type glob accepted")
	}
}

func TestLocalSearchCommandFindsNamesAndContentInsideGrant(t *testing.T) {
	if _, ok := resolveLocalSearchExecutable("rg"); !ok {
		t.Skip("ripgrep is unavailable in this test environment")
	}
	root := t.TempDir()
	writeLocalSearchCommandTestFile(t, filepath.Join(root, "reports", "summary.txt"), "quarterly needle in content\n")
	writeLocalSearchCommandTestFile(t, filepath.Join(root, "reports", "needle-plan.md"), "no content match\n")
	writeLocalSearchCommandTestFile(t, filepath.Join(root, ".env"), "needle=secret\n")
	writeLocalSearchCommandTestFile(t, filepath.Join(root, ".ssh", "id_ed25519"), "needle secret key\n")

	out, err := newLocalSearchCommandTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"grant_id":    "local-grant",
		"local_root":  root,
		"program":     "auto",
		"query":       "needle",
		"max_results": 10,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok || result["search_backend"] != "command_broker" {
		t.Fatalf("result=%#v, want command broker output", out.Result)
	}
	files, _ := result["files"].([]map[string]any)
	paths := localSearchCommandFileRefs(files)
	for _, want := range []string{"reports/needle-plan.md", "reports/summary.txt"} {
		if !containsString(paths, want) {
			t.Fatalf("files=%#v, missing %q", files, want)
		}
	}
	for _, forbidden := range []string{".env", ".ssh/id_ed25519"} {
		if containsString(paths, forbidden) {
			t.Fatalf("files=%#v, sensitive path %q must be excluded", files, forbidden)
		}
	}
}

func TestLocalSearchCommandSearchesExactFileGrant(t *testing.T) {
	if _, ok := resolveLocalSearchExecutable("rg"); !ok {
		t.Skip("ripgrep is unavailable in this test environment")
	}
	root := t.TempDir()
	filePath := filepath.Join(root, "forecast.txt")
	writeLocalSearchCommandTestFile(t, filePath, "needle forecast\n")
	writeLocalSearchCommandTestFile(t, filepath.Join(root, "sibling.txt"), "needle sibling\n")
	out, err := newLocalSearchCommandTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"grant_id":   "file-grant",
		"local_root": filePath,
		"local_kind": "file",
		"program":    "rg",
		"query":      "needle",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result := out.Result.(map[string]any)
	files, _ := result["files"].([]map[string]any)
	paths := localSearchCommandFileRefs(files)
	if len(paths) != 1 || paths[0] != "forecast.txt" {
		t.Fatalf("files=%#v, want only exact file grant", files)
	}
}

func TestLocalSearchCommandRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeLocalSearchCommandTestFile(t, filepath.Join(outside, "secret.txt"), "needle\n")
	cases := []struct {
		name string
		path string
	}{
		{name: "traversal", path: "../"},
	}
	if runtime.GOOS != "windows" {
		link := filepath.Join(root, "outside-link")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		cases = append(cases, struct {
			name string
			path string
		}{name: "symlink", path: "outside-link"})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := newLocalSearchCommandTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
				"grant_id":      "local-grant",
				"local_root":    root,
				"program":       "auto",
				"query":         "needle",
				"relative_path": tc.path,
			}))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if out.OK {
				t.Fatalf("output=%#v, want scope escape rejection", out)
			}
		})
	}
}

func TestLocalSearchCommandHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := executeLocalSearchCommandBroker(ctx, localSearchBrokerRequest{
		Root:         t.TempDir(),
		Base:         t.TempDir(),
		Program:      "auto",
		Query:        "needle",
		Limit:        5,
		MaxOutput:    1024,
		MaxFileBytes: 1024,
	})
	if err != context.Canceled {
		t.Fatalf("result=%#v err=%v, want context.Canceled", result, err)
	}
}

func writeLocalSearchCommandTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}

func localSearchCommandFileRefs(files []map[string]any) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		if ref, _ := file["file_ref"].(string); strings.TrimSpace(ref) != "" {
			out = append(out, ref)
		}
	}
	return out
}

func localSearchCommandArgPairIndex(args []string, flag, value string) int {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == flag && args[index+1] == value {
			return index
		}
	}
	return -1
}
