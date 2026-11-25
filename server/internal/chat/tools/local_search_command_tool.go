package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	chatstore "ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/localexec"
)

const (
	defaultLocalSearchCommandTimeout = 15 * time.Second
	maxLocalSearchCommandTimeout     = 30 * time.Second
	defaultLocalSearchCommandBytes   = 512 << 10
	maxLocalSearchCommandBytes       = 2 << 20
	maxLocalSearchCommandArgs        = 32
	maxLocalSearchCommandArgBytes    = 4096
	maxLocalSearchCommandArgsBytes   = 16 << 10
	maxLocalSearchCommandQueryBytes  = 4096
	maxLocalSearchCommandGrantBytes  = 512
	maxLocalSearchCommandFileTypes   = 64
	maxLocalSearchCommandFileTypeLen = 64
)

type localSearchCommandInput struct {
	LocalRoot     string   `json:"local_root"`
	LocalKind     string   `json:"local_kind,omitempty"`
	GrantID       string   `json:"grant_id"`
	Program       string   `json:"program"`
	Query         string   `json:"query"`
	RelativePath  string   `json:"relative_path,omitempty"`
	Args          []string `json:"args,omitempty"`
	FileTypes     []string `json:"file_types,omitempty"`
	Regex         bool     `json:"regex,omitempty"`
	CaseSensitive bool     `json:"case_sensitive,omitempty"`
	MaxResults    int      `json:"max_results,omitempty"`
	TimeoutMS     int      `json:"timeout_ms,omitempty"`
	MaxFileBytes  int64    `json:"max_file_bytes,omitempty"`
	DryRun        bool     `json:"dry_run,omitempty"`
}

type localSearchBrokerRequest struct {
	Root          string
	Base          string
	RelPrefix     string
	SingleFile    bool
	GrantID       string
	Program       string
	Query         string
	Args          []string
	FileTypes     map[string]struct{}
	Regex         bool
	CaseSensitive bool
	Limit         int
	Timeout       time.Duration
	MaxOutput     int
	MaxFileBytes  int64
}

type localSearchBrokerCommand struct {
	Program         string `json:"program"`
	Backend         string `json:"backend"`
	DurationMS      int64  `json:"duration_ms"`
	ExitCode        int    `json:"exit_code"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	Error           string `json:"error,omitempty"`
}

type localSearchBrokerResult struct {
	NameMatches    []string
	ContentMatches map[string][]string
	Commands       []localSearchBrokerCommand
	Truncated      bool
	TimedOut       bool
	Available      bool
	Succeeded      bool
}

type localSearchProcessResult struct {
	Stdout          []byte
	Stderr          string
	OutputTruncated bool
	Duration        time.Duration
	ExitCode        int
	Err             error
}

func newLocalSearchCommandTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        LocalSearchCommandToolName,
				Description: "Run a bounded, read-only local search command inside one configured Local Computer grant. This tool supports ripgrep/fd search semantics without a shell: use auto for filename plus content search, rg or grep for content, and fd or find for filenames. Paths are grant-relative, dangerous command flags are rejected, and writes, shell expansion, subprocess hooks, and network commands are unavailable.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"grant_id": map[string]any{
							"type":        "string",
							"maxLength":   maxLocalSearchCommandGrantBytes,
							"description": "Configured Local Computer grant ID supplied by the application.",
						},
						"program": map[string]any{
							"type":        "string",
							"enum":        []string{"auto", "rg", "grep", "fd", "find"},
							"description": "Search command family. auto runs filename and content strategies; grep aliases rg and find aliases fd.",
						},
						"query": map[string]any{
							"type":        "string",
							"maxLength":   maxLocalSearchCommandQueryBytes,
							"description": "Filename or content pattern. Literal by default; set regex for regular-expression matching.",
						},
						"relative_path": map[string]any{
							"type":        "string",
							"maxLength":   maxLocalSearchCommandArgBytes,
							"description": "Optional grant-relative folder to search. Absolute paths and traversal are rejected.",
						},
						"args": map[string]any{
							"type":        "array",
							"description": "Optional validated search flags. Positional paths, shell syntax, redirection, execution hooks, and unknown flags are rejected.",
							"items":       map[string]any{"type": "string", "maxLength": maxLocalSearchCommandArgBytes},
							"maxItems":    maxLocalSearchCommandArgs,
						},
						"file_types": map[string]any{
							"type":        "array",
							"description": "Optional extensions or broad types such as pdf, spreadsheet, image, or code.",
							"items":       map[string]any{"type": "string", "maxLength": maxLocalSearchCommandFileTypeLen},
							"maxItems":    maxLocalSearchCommandFileTypes,
						},
						"regex": map[string]any{
							"type":        "boolean",
							"description": "Interpret query as a regular expression instead of a literal string.",
						},
						"case_sensitive": map[string]any{
							"type":        "boolean",
							"description": "Use case-sensitive matching. Default false.",
						},
						"max_results": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"maximum":     maxSearchMatches,
							"description": "Maximum distinct files to return. Default 20, max 200.",
						},
						"timeout_ms": map[string]any{
							"type":        "integer",
							"minimum":     1000,
							"maximum":     int(maxLocalSearchCommandTimeout / time.Millisecond),
							"description": "Overall command deadline in milliseconds. Default 15000, max 30000.",
						},
						"max_file_bytes": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"maximum":     maxReadFileBytes,
							"description": "Skip content search in files larger than this many bytes.",
						},
						"dry_run": map[string]any{
							"type":        "boolean",
							"description": "Validate the command plan without starting a process.",
						},
					},
					"required":             []string{"grant_id", "program", "query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: executeLocalSearchCommandTool,
	}
}

func executeLocalSearchCommandTool(ctx context.Context, execCtx ExecContext, args json.RawMessage) (Output, error) {
	var in localSearchCommandInput
	if err := decodeArgs(args, &in); err != nil {
		return JSONError("Invalid JSON arguments."), nil
	}
	grantID := strings.TrimSpace(in.GrantID)
	if grantID == "" {
		return JSONError("grant_id is required"), nil
	}
	if len(grantID) > maxLocalSearchCommandGrantBytes || strings.ContainsAny(grantID, "\x00\r\n") {
		return JSONError("grant_id is invalid"), nil
	}
	program, err := normalizeLocalSearchCommandProgram(in.Program)
	if err != nil {
		return JSONError(err.Error()), nil
	}
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return JSONError("query is required"), nil
	}
	if len(query) > maxLocalSearchCommandQueryBytes || strings.ContainsRune(query, '\x00') {
		return JSONError("query is too large or contains invalid characters"), nil
	}
	if len(in.RelativePath) > maxLocalSearchCommandArgBytes {
		return JSONError("relative_path is too large"), nil
	}
	if len(in.FileTypes) > maxLocalSearchCommandFileTypes {
		return JSONError("too many file_types"), nil
	}
	if err := validateLocalSearchCommandFileTypes(in.FileTypes); err != nil {
		return JSONError(err.Error()), nil
	}
	validatedArgs, err := validateLocalSearchCommandArgs(program, in.Args)
	if err != nil {
		return JSONError(err.Error()), nil
	}
	root, base, relPrefix, singleFile, err := resolveLocalMachineSearchScope(in.LocalRoot, in.RelativePath, in.LocalKind)
	if err != nil {
		return JSONError(err.Error()), nil
	}
	limit := clampPositiveInt(in.MaxResults, defaultSearchMatches, maxSearchMatches)
	timeout := clampLocalSearchCommandTimeout(in.TimeoutMS)
	maxFileBytes := clampPositiveInt64(in.MaxFileBytes, int64(defaultReadFileBytes), int64(maxReadFileBytes))

	if in.DryRun {
		_, ripgrepAvailable := resolveLocalSearchExecutable("rg")
		_, fdAvailable := resolveLocalSearchExecutable("fd")
		return localFilesOutput(LocalSearchCommandToolName, "dry_run", map[string]any{
			"ok":            true,
			"dry_run":       true,
			"validated":     true,
			"grant_id":      grantID,
			"program":       program,
			"relative_path": relPrefix,
			"args":          validatedArgs,
			"runtime": map[string]bool{
				"ripgrep": ripgrepAvailable,
				"fd":      fdAvailable,
			},
		}, map[string]any{
			"grant_id":        grantID,
			"program":         program,
			"path":            relPrefix,
			"dry_run":         true,
			"filesystem_only": true,
		}), nil
	}

	result, err := executeLocalSearchCommandBroker(ctx, localSearchBrokerRequest{
		Root:          root,
		Base:          base,
		RelPrefix:     relPrefix,
		SingleFile:    singleFile,
		GrantID:       grantID,
		Program:       program,
		Query:         query,
		Args:          validatedArgs,
		FileTypes:     normalizeLocalMachineFileTypes(in.FileTypes),
		Regex:         in.Regex,
		CaseSensitive: in.CaseSensitive,
		Limit:         limit,
		Timeout:       timeout,
		MaxOutput:     defaultLocalSearchCommandBytes,
		MaxFileBytes:  maxFileBytes,
	})
	if err != nil {
		return JSONError(err.Error()), nil
	}
	if !result.Available {
		return JSONError("local search runtime is unavailable; bundle ripgrep or fd with the desktop app"), nil
	}
	return localSearchBrokerOutput(LocalSearchCommandToolName, root, relPrefix, singleFile, grantID, program, query, limit, maxFileBytes, result), nil
}

func commandBackedFileSearch(
	ctx context.Context,
	root string,
	base string,
	relPrefix string,
	singleFile bool,
	grantID string,
	query string,
	fileTypes map[string]struct{},
	caseSensitive bool,
	limit int,
	maxFileBytes int64,
	searchMode string,
) (Output, bool) {
	program := "auto"
	switch normalizeLocalFileSearchMode(searchMode) {
	case "name":
		program = "fd"
	case "content":
		program = "rg"
	}
	result, err := executeLocalSearchCommandBroker(ctx, localSearchBrokerRequest{
		Root:          root,
		Base:          base,
		RelPrefix:     relPrefix,
		SingleFile:    singleFile,
		GrantID:       strings.TrimSpace(grantID),
		Program:       program,
		Query:         normalizeLocalFileSearchQuery(query, mapKeys(fileTypes)),
		FileTypes:     fileTypes,
		CaseSensitive: caseSensitive,
		Limit:         limit,
		Timeout:       defaultLocalFileSearchTimeout,
		MaxOutput:     defaultLocalSearchCommandBytes,
		MaxFileBytes:  maxFileBytes,
	})
	if err != nil || !result.Available || (!result.Succeeded && !result.TimedOut) {
		return Output{}, false
	}
	// A command search that reached its own deadline is already the best bounded
	// partial observation. Do not immediately start a slower portable full-tree
	// walk with the nearly exhausted parent context; that path used to surface a
	// raw context deadline exceeded error and discard the useful timeout state.
	return localSearchBrokerOutput("file_search", root, relPrefix, singleFile, strings.TrimSpace(grantID), program, query, limit, maxFileBytes, result), true
}

func localSearchBrokerOutput(toolName, root, relPrefix string, singleFile bool, grantID, program, query string, limit int, maxFileBytes int64, result localSearchBrokerResult) Output {
	files := localMachineSearchFileEntries(grantID, relPrefix, result.NameMatches, result.ContentMatches)
	payload := map[string]any{
		"local_root":      root,
		"local_kind":      localMachineGrantKindForOutput(singleFile),
		"path":            relPrefix,
		"query":           query,
		"program":         program,
		"search_backend":  "command_broker",
		"name_matches":    result.NameMatches,
		"content_matches": result.ContentMatches,
		"files":           files,
		"matches_count":   len(files),
		"limit":           limit,
		"truncated":       result.Truncated,
		"timed_out":       result.TimedOut,
		"max_file_bytes":  maxFileBytes,
		"commands":        result.Commands,
	}
	if grantID != "" {
		payload["grant_id"] = grantID
	}
	extra := map[string]any{
		"local_root":          root,
		"local_kind":          localMachineGrantKindForOutput(singleFile),
		"path":                relPrefix,
		"query":               query,
		"program":             program,
		"search_backend":      "command_broker",
		"matches":             len(files),
		"files_count":         len(files),
		"truncated":           result.Truncated,
		"timed_out":           result.TimedOut,
		"max_bytes":           maxFileBytes,
		"filesystem_readonly": true,
	}
	if grantID != "" {
		extra["grant_id"] = grantID
	}
	return localFilesOutput(toolName, "file_search", payload, extra)
}

func executeLocalSearchCommandBroker(ctx context.Context, req localSearchBrokerRequest) (localSearchBrokerResult, error) {
	result := localSearchBrokerResult{ContentMatches: map[string][]string{}}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if req.Timeout <= 0 || req.Timeout > maxLocalSearchCommandTimeout {
		req.Timeout = defaultLocalSearchCommandTimeout
	}
	if req.MaxOutput <= 0 || req.MaxOutput > maxLocalSearchCommandBytes {
		req.MaxOutput = defaultLocalSearchCommandBytes
	}
	if req.Limit <= 0 || req.Limit > maxSearchMatches {
		req.Limit = defaultSearchMatches
	}
	searchCtx, cancel := context.WithTimeout(ctx, req.Timeout)
	defer cancel()

	rgPath, haveRG := resolveLocalSearchExecutable("rg")
	fdPath, haveFD := resolveLocalSearchExecutable("fd")
	program, err := normalizeLocalSearchCommandProgram(req.Program)
	if err != nil {
		return result, err
	}
	needsContent := program == "auto" || program == "rg"
	needsNames := program == "auto" || program == "fd"
	result.Available = (needsContent && haveRG) || (needsNames && (haveFD || haveRG))
	if !result.Available {
		return result, nil
	}

	bases := []string{req.Base}
	if !req.SingleFile {
		bases = localMachineSearchBases(req.Base)
	}
	seenNames := map[string]struct{}{}
	addName := func(rel string) bool {
		rel = strings.TrimSpace(filepath.ToSlash(rel))
		if rel == "" {
			return true
		}
		if _, ok := seenNames[rel]; ok {
			return true
		}
		if localSearchUniqueFileCount(seenNames, result.ContentMatches) >= req.Limit {
			result.Truncated = true
			return false
		}
		seenNames[rel] = struct{}{}
		result.NameMatches = append(result.NameMatches, rel)
		return true
	}
	addContent := func(rel, line string) bool {
		rel = strings.TrimSpace(filepath.ToSlash(rel))
		if rel == "" {
			return true
		}
		if _, ok := seenNames[rel]; !ok {
			if _, ok := result.ContentMatches[rel]; !ok && localSearchUniqueFileCount(seenNames, result.ContentMatches) >= req.Limit {
				result.Truncated = true
				return false
			}
		}
		if line != "" && len(result.ContentMatches[rel]) < 3 {
			result.ContentMatches[rel] = append(result.ContentMatches[rel], line)
		} else if _, ok := result.ContentMatches[rel]; !ok {
			result.ContentMatches[rel] = []string{}
		}
		return true
	}

	for _, searchBase := range bases {
		if searchCtx.Err() != nil || result.Truncated {
			break
		}
		searchBase = filepath.Clean(searchBase)
		if req.SingleFile {
			searchBase = filepath.Dir(req.Base)
		}
		if needsNames {
			var process localSearchProcessResult
			backend := "fd"
			if haveFD {
				process = runLocalSearchProcess(searchCtx, searchBase, fdPath, buildLocalFDArgs(req), req.MaxOutput)
			} else {
				backend = "rg-files"
				process = runLocalSearchProcess(searchCtx, searchBase, rgPath, buildLocalRGFileArgs(req), req.MaxOutput)
			}
			result.Commands = append(result.Commands, localSearchCommandSummary(program, backend, process))
			result.Truncated = result.Truncated || process.OutputTruncated
			if processSucceededWithSearchOutput(process) {
				result.Succeeded = true
				for _, rawPath := range splitLocalSearchPaths(process.Stdout) {
					rel, ok := localSearchCommandResultPath(req.Root, searchBase, rawPath, req.SingleFile)
					if !ok || !localSearchNameMatches(rel, req.Query, req.Regex, req.CaseSensitive) ||
						(len(req.FileTypes) > 0 && !localMachineFileTypeAllowed(rel, req.FileTypes)) {
						continue
					}
					if !addName(rel) {
						break
					}
				}
			}
			// Whole-computer roots are ordered by likely user locations. A filename
			// hit in one of those locations is enough; continuing into content or the
			// filesystem root turns a sub-second lookup into a full-disk scan.
			if program == "fd" && len(result.NameMatches) > 0 {
				break
			}
		}
		if needsContent && haveRG && searchCtx.Err() == nil && !result.Truncated {
			process := runLocalSearchProcess(searchCtx, searchBase, rgPath, buildLocalRGContentArgs(req), req.MaxOutput)
			result.Commands = append(result.Commands, localSearchCommandSummary(program, "rg", process))
			result.Truncated = result.Truncated || process.OutputTruncated
			if processSucceededWithSearchOutput(process) {
				result.Succeeded = true
				parseLocalRGJSONMatches(process.Stdout, req.Root, searchBase, req.SingleFile, addContent)
			}
		}
	}
	if errors.Is(searchCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
		result.Truncated = true
	} else if err := searchCtx.Err(); err != nil {
		return result, err
	}
	if !result.Succeeded {
		messages := []string{}
		for _, command := range result.Commands {
			if command.Error != "" {
				messages = append(messages, command.Error)
			}
		}
		if len(messages) > 0 && !result.TimedOut {
			return result, errors.New(strings.Join(dedupeStringSlice(messages), "; "))
		}
	}
	sort.Strings(result.NameMatches)
	return result, nil
}

func normalizeLocalFileSearchMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "name", "content":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "auto"
	}
}

func normalizeLocalFileSearchQuery(raw string, fileTypes []string) string {
	query := strings.TrimSpace(raw)
	for len(query) >= 2 {
		first, last := query[0], query[len(query)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			query = strings.TrimSpace(query[1 : len(query)-1])
			continue
		}
		break
	}
	extensions := normalizeLocalMachineFileTypes(fileTypes)
	parts := strings.Fields(query)
	if len(parts) > 1 && len(extensions) > 0 {
		last := strings.Trim(strings.ToLower(parts[len(parts)-1]), ".,;:()[]{}")
		if _, ok := extensions[last]; ok {
			parts = parts[:len(parts)-1]
		}
	}
	if normalized := strings.TrimSpace(strings.Join(parts, " ")); normalized != "" {
		return normalized
	}
	return query
}

func mapKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	return out
}

func normalizeLocalSearchCommandProgram(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto":
		return "auto", nil
	case "rg", "grep":
		return "rg", nil
	case "fd", "find":
		return "fd", nil
	default:
		return "", fmt.Errorf("unsupported local search program %q", strings.TrimSpace(raw))
	}
}

func validateLocalSearchCommandArgs(program string, args []string) ([]string, error) {
	if len(args) > maxLocalSearchCommandArgs {
		return nil, fmt.Errorf("too many local search command arguments")
	}
	program, err := normalizeLocalSearchCommandProgram(program)
	if err != nil {
		return nil, err
	}
	if program == "auto" && len(args) > 0 {
		return nil, errors.New("args require an explicit rg/grep or fd/find program")
	}
	boolFlags := map[string]struct{}{}
	valueFlags := map[string]string{}
	if program == "rg" {
		for _, flag := range []string{"-i", "--ignore-case", "-s", "--case-sensitive", "-S", "--smart-case", "-F", "--fixed-strings", "-w", "--word-regexp", "-x", "--line-regexp", "-U", "--multiline", "--pcre2", "-a", "--text", "--crlf", "--no-ignore", "--no-ignore-vcs"} {
			boolFlags[flag] = struct{}{}
		}
		for flag, kind := range map[string]string{"-g": "text", "--glob": "text", "-t": "text", "--type": "text", "-T": "text", "--type-not": "text", "--max-depth": "nonnegative"} {
			valueFlags[flag] = kind
		}
	} else if program == "fd" {
		for _, flag := range []string{"--hidden", "--no-ignore", "--no-ignore-vcs", "--case-sensitive", "--ignore-case", "--fixed-strings", "--glob"} {
			boolFlags[flag] = struct{}{}
		}
		for flag, kind := range map[string]string{"-e": "extension", "--extension": "extension", "-d": "nonnegative", "--max-depth": "nonnegative", "--min-depth": "nonnegative", "--size": "size", "--changed-before": "text", "--changed-within": "text", "-E": "text", "--exclude": "text"} {
			valueFlags[flag] = kind
		}
	}
	out := make([]string, 0, len(args))
	totalBytes := 0
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		totalBytes += len(arg)
		if totalBytes > maxLocalSearchCommandArgsBytes {
			return nil, errors.New("local search command arguments are too large")
		}
		if arg == "" || len(arg) > maxLocalSearchCommandArgBytes || strings.ContainsAny(arg, "\x00\r\n") {
			return nil, errors.New("local search command arguments must be finite non-empty strings without control characters")
		}
		if strings.ContainsAny(arg, "|;&`$<>") {
			return nil, fmt.Errorf("shell syntax is not allowed in local search command argument %q", arg)
		}
		if localSearchCommandExecutionFlag(program, arg) {
			return nil, fmt.Errorf("local search execution flag %q is not allowed", arg)
		}
		if _, ok := boolFlags[arg]; ok {
			out = append(out, arg)
			continue
		}
		kind, ok := valueFlags[arg]
		if !ok {
			return nil, fmt.Errorf("local search flag %q is not allowed for %s", arg, program)
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("local search flag %q requires a value", arg)
		}
		i++
		value := strings.TrimSpace(args[i])
		totalBytes += len(value)
		if totalBytes > maxLocalSearchCommandArgsBytes {
			return nil, errors.New("local search command arguments are too large")
		}
		if err := validateLocalSearchCommandFlagValue(kind, value); err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", arg, err)
		}
		out = append(out, arg, value)
	}
	return out, nil
}

func localSearchCommandExecutionFlag(program, arg string) bool {
	switch program {
	case "rg":
		return arg == "--pre" || arg == "--pre-glob"
	case "fd":
		switch arg {
		case "-x", "--exec", "-X", "--exec-batch", "--batch-size":
			return true
		}
	}
	return false
}

func validateLocalSearchCommandFlagValue(kind, value string) error {
	if value == "" || len(value) > maxLocalSearchCommandArgBytes || strings.ContainsAny(value, "\x00\r\n|;&`$<>") {
		return errors.New("value is empty, too large, or contains shell syntax")
	}
	switch kind {
	case "positive", "nonnegative":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || (kind == "positive" && n == 0) || n > 1000000 {
			return errors.New("value must be a bounded integer")
		}
	case "extension":
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,31}$`).MatchString(value) {
			return errors.New("extension is invalid")
		}
	case "size":
		if !regexp.MustCompile(`^[0-9]+([KMGT]i?[Bb]?|[Bb])?$`).MatchString(value) {
			return errors.New("size is invalid")
		}
	}
	return nil
}

func validateLocalSearchCommandFileTypes(values []string) error {
	valid := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	for _, raw := range values {
		value := strings.TrimPrefix(strings.TrimSpace(raw), ".")
		if value == "" || len(value) > maxLocalSearchCommandFileTypeLen || !valid.MatchString(value) {
			return fmt.Errorf("invalid file_type %q", strings.TrimSpace(raw))
		}
	}
	return nil
}

func clampLocalSearchCommandTimeout(timeoutMS int) time.Duration {
	if timeoutMS <= 0 {
		return defaultLocalSearchCommandTimeout
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout < time.Second {
		return time.Second
	}
	if timeout > maxLocalSearchCommandTimeout {
		return maxLocalSearchCommandTimeout
	}
	return timeout
}

func resolveLocalSearchExecutable(name string) (string, bool) {
	if path, ok := localexec.ResolveCommand(name); ok {
		if localSearchExecutableIsNative(path) {
			return path, true
		}
	}
	if name == "fd" {
		if path, ok := localexec.ResolveCommand("fdfind"); ok && localSearchExecutableIsNative(path) {
			return path, true
		}
	}
	return "", false
}

func localSearchExecutableIsNative(path string) bool {
	if runtime.GOOS != "windows" {
		return true
	}
	return strings.EqualFold(filepath.Ext(strings.TrimSpace(path)), ".exe")
}

func buildLocalRGContentArgs(req localSearchBrokerRequest) []string {
	args := []string{"--json", "--color", "never", "--no-messages", "--hidden", "--no-ignore-vcs", "--max-count", "3"}
	if req.CaseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}
	if !req.Regex {
		args = append(args, "--fixed-strings")
	}
	if req.MaxFileBytes > 0 {
		args = append(args, "--max-filesize", strconv.FormatInt(req.MaxFileBytes, 10))
	}
	args = append(args, localSearchRGTypeArgs(req.FileTypes)...)
	args = append(args, req.Args...)
	args = append(args, localSearchRGExcludeArgs()...)
	args = append(args, "-e", req.Query, "--", localSearchCommandTarget(req))
	return args
}

func buildLocalRGFileArgs(req localSearchBrokerRequest) []string {
	args := []string{"--files", "--null", "--color", "never", "--no-messages", "--hidden", "--no-ignore-vcs"}
	args = append(args, localSearchRGNameQueryArgs(req)...)
	args = append(args, localSearchRGExcludeArgs()...)
	args = append(args, "--", localSearchCommandTarget(req))
	return args
}

func localSearchRGNameQueryArgs(req localSearchBrokerRequest) []string {
	if req.Regex || strings.TrimSpace(req.Query) == "" || strings.ContainsAny(req.Query, "/\\") {
		return nil
	}
	flag := "--iglob"
	if req.CaseSensitive {
		flag = "--glob"
	}
	query := escapeLocalSearchGlob(req.Query)
	return []string{
		flag, "*" + query + "*",
		flag, "**/*" + query + "*/**",
	}
}

func escapeLocalSearchGlob(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`*`, `\*`,
		`?`, `\?`,
		`[`, `\[`,
		`]`, `\]`,
		`{`, `\{`,
		`}`, `\}`,
	)
	return replacer.Replace(value)
}

func buildLocalFDArgs(req localSearchBrokerRequest) []string {
	args := []string{"--type", "f", "--hidden", "--no-ignore-vcs", "--color", "never", "--print0"}
	if req.CaseSensitive {
		args = append(args, "--case-sensitive")
	} else {
		args = append(args, "--ignore-case")
	}
	if !req.Regex {
		args = append(args, "--fixed-strings")
	}
	for _, ext := range sortedLocalSearchExtensions(req.FileTypes) {
		args = append(args, "--extension", ext)
	}
	args = append(args, req.Args...)
	for _, pattern := range localSearchSensitiveExcludePatterns() {
		args = append(args, "--exclude", pattern)
	}
	args = append(args, "--", req.Query, localSearchCommandTarget(req))
	return args
}

func localSearchCommandTarget(req localSearchBrokerRequest) string {
	if req.SingleFile {
		return filepath.Base(req.Base)
	}
	return "."
}

func localSearchRGTypeArgs(fileTypes map[string]struct{}) []string {
	args := []string{}
	for _, ext := range sortedLocalSearchExtensions(fileTypes) {
		args = append(args, "--glob", "*."+ext)
	}
	return args
}

func sortedLocalSearchExtensions(fileTypes map[string]struct{}) []string {
	out := make([]string, 0, len(fileTypes))
	for ext := range fileTypes {
		ext = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(ext), "."))
		if ext != "" {
			out = append(out, ext)
		}
	}
	sort.Strings(out)
	return out
}

func localSearchSensitiveExcludePatterns() []string {
	patterns := append([]string{".git", ".git/**"}, chatstore.DefaultSensitiveLocalPermissionExcludes()...)
	return dedupeStringSlice(patterns)
}

func localSearchRGExcludeArgs() []string {
	args := []string{}
	for _, pattern := range localSearchSensitiveExcludePatterns() {
		args = append(args, "--glob", "!"+pattern)
	}
	return args
}

func runLocalSearchProcess(ctx context.Context, dir, executable string, args []string, maxOutput int) localSearchProcessResult {
	started := time.Now()
	cmd := localexec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("")
	tmpDir, err := os.MkdirTemp("", "ai-analyst-local-search-*")
	if err != nil {
		return localSearchProcessResult{Err: err, ExitCode: -1}
	}
	defer os.RemoveAll(tmpDir)
	cmd.Env = localSearchCommandEnvironment(tmpDir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return localSearchProcessResult{Err: err, ExitCode: -1}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return localSearchProcessResult{Err: err, ExitCode: -1}
	}
	if err := cmd.Start(); err != nil {
		return localSearchProcessResult{Err: err, ExitCode: -1}
	}
	type streamResult struct {
		bytes     []byte
		truncated bool
		err       error
	}
	stdoutCh := make(chan streamResult, 1)
	stderrCh := make(chan streamResult, 1)
	go func() {
		b, truncated, err := readLimitedSearchBytes(stdout, maxOutput)
		stdoutCh <- streamResult{bytes: b, truncated: truncated, err: err}
	}()
	go func() {
		b, truncated, err := readLimitedSearchBytes(stderr, 64<<10)
		stderrCh <- streamResult{bytes: b, truncated: truncated, err: err}
	}()
	out := <-stdoutCh
	errOut := <-stderrCh
	waitErr := cmd.Wait()
	if out.err != nil && waitErr == nil {
		waitErr = out.err
	}
	if errOut.err != nil && waitErr == nil {
		waitErr = errOut.err
	}
	exitCode := 0
	if waitErr != nil {
		exitCode = -1
		var exitErr *osexec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return localSearchProcessResult{
		Stdout:          out.bytes,
		Stderr:          strings.TrimSpace(string(bytes.ToValidUTF8(errOut.bytes, []byte("?")))),
		OutputTruncated: out.truncated || errOut.truncated,
		Duration:        time.Since(started),
		ExitCode:        exitCode,
		Err:             waitErr,
	}
}

func readLimitedSearchBytes(r io.Reader, limit int) ([]byte, bool, error) {
	if limit <= 0 {
		limit = defaultLocalSearchCommandBytes
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	truncated := n > int64(limit)
	if truncated {
		_, _ = io.Copy(io.Discard, r)
	}
	b := buf.Bytes()
	if len(b) > limit {
		b = b[:limit]
	}
	return append([]byte(nil), b...), truncated, nil
}

func localSearchCommandEnvironment(tmpDir string) []string {
	pathValue := strings.Join(localexec.CommandSearchDirs(), string(os.PathListSeparator))
	env := []string{
		"PATH=" + pathValue,
		"HOME=" + tmpDir,
		"TMPDIR=" + tmpDir,
		"TMP=" + tmpDir,
		"TEMP=" + tmpDir,
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	if runtime.GOOS == "windows" {
		env = append(env,
			"USERPROFILE="+tmpDir,
			"APPDATA="+tmpDir,
			"LOCALAPPDATA="+tmpDir,
		)
		for _, key := range []string{"SystemRoot", "WINDIR"} {
			if value := strings.TrimSpace(os.Getenv(key)); value != "" {
				env = append(env, key+"="+value)
			}
		}
	}
	return env
}

func processSucceededWithSearchOutput(result localSearchProcessResult) bool {
	if result.Err == nil || (result.ExitCode == 1 && len(result.Stdout) == 0) {
		return true
	}
	return len(result.Stdout) > 0
}

func localSearchCommandSummary(program, backend string, result localSearchProcessResult) localSearchBrokerCommand {
	summary := localSearchBrokerCommand{
		Program:         program,
		Backend:         backend,
		DurationMS:      result.Duration.Milliseconds(),
		ExitCode:        result.ExitCode,
		OutputTruncated: result.OutputTruncated,
	}
	if result.Err != nil && !(result.ExitCode == 1 && strings.TrimSpace(result.Stderr) == "") {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = result.Err.Error()
		}
		summary.Error = truncateLocalSearchText(detail, 500)
	}
	return summary
}

func splitLocalSearchPaths(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	separator := byte('\n')
	if bytes.IndexByte(raw, 0) >= 0 {
		separator = 0
	}
	parts := bytes.Split(raw, []byte{separator})
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(string(bytes.ToValidUTF8(part, []byte("?"))))
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func parseLocalRGJSONMatches(raw []byte, root, commandDir string, singleFile bool, add func(rel, line string) bool) {
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				LineNumber int `json:"line_number"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Type != "match" {
			continue
		}
		rel, ok := localSearchCommandResultPath(root, commandDir, event.Data.Path.Text, singleFile)
		if !ok {
			continue
		}
		text := strings.TrimSpace(strings.ReplaceAll(event.Data.Lines.Text, "\x00", ""))
		text = truncateLocalSearchText(text, 500)
		if event.Data.LineNumber > 0 && text != "" {
			text = fmt.Sprintf("Line %d: %s", event.Data.LineNumber, text)
		}
		if !add(rel, text) {
			return
		}
	}
}

func localSearchCommandResultPath(root, commandDir, raw string, singleFile bool) (string, bool) {
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "./"))
	if raw == "" {
		return "", false
	}
	full := raw
	if !filepath.IsAbs(full) {
		full = filepath.Join(commandDir, filepath.FromSlash(raw))
	}
	full = filepath.Clean(full)
	if evaluated, err := filepath.EvalSymlinks(full); err == nil {
		full = filepath.Clean(evaluated)
	}
	if singleFile {
		rootInfo, rootErr := os.Stat(filepath.Clean(root))
		info, err := os.Stat(full)
		if rootErr != nil || err != nil || rootInfo == nil || info == nil || !os.SameFile(rootInfo, info) || !info.Mode().IsRegular() {
			return "", false
		}
		if chatstore.LocalPathExcludedByPermissionPatterns(filepath.Base(full), chatstore.DefaultSensitiveLocalPermissionExcludes()) {
			return "", false
		}
		return filepath.Base(full), true
	}
	if !localMachineIsSubpath(root, full) {
		return "", false
	}
	rel, ok := localMachineRel(root, full)
	if !ok || rel == "" || chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes()) {
		return "", false
	}
	info, err := os.Stat(full)
	if err != nil || info == nil || !info.Mode().IsRegular() {
		return "", false
	}
	return rel, true
}

func localSearchNameMatches(rel, query string, regexMode, caseSensitive bool) bool {
	haystack := filepath.ToSlash(rel)
	needle := query
	if !caseSensitive {
		haystack = strings.ToLower(haystack)
		needle = strings.ToLower(needle)
	}
	if !regexMode {
		return strings.Contains(haystack, needle)
	}
	if !caseSensitive {
		needle = "(?i)" + needle
	}
	re, err := regexp.Compile(needle)
	return err == nil && re.MatchString(filepath.ToSlash(rel))
}

func localSearchUniqueFileCount(names map[string]struct{}, content map[string][]string) int {
	count := len(names)
	for rel := range content {
		if _, ok := names[rel]; !ok {
			count++
		}
	}
	return count
}

func truncateLocalSearchText(value string, max int) string {
	runes := []rune(strings.TrimSpace(value))
	if max <= 0 || len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max]) + "..."
}
