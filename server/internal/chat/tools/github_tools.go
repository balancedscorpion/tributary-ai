package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"ai-analyst-local/internal/chat/types"
)

const (
	GitHubToolStatus             = "github_status"
	GitHubToolCheckConnection    = "github_check_connection"
	GitHubToolListRepositories   = "github_list_repositories"
	GitHubToolListPullRequests   = "github_list_pull_requests"
	GitHubToolGetPullRequest     = "github_get_pull_request"
	GitHubToolListIssues         = "github_list_issues"
	GitHubToolGetIssue           = "github_get_issue"
	GitHubToolListWorkflowRuns   = "github_list_workflow_runs"
	GitHubToolGetWorkflowRunJobs = "github_get_workflow_run_jobs"
	GitHubToolCloneRepository    = "github_clone_repository"
	GitHubToolPullRepository     = "github_pull_repository"
	GitHubToolRefreshRepository  = "github_refresh_repository"
	GitHubToolRunGHCommand       = "github_run_gh_command"
)

var githubToolNames = []string{
	GitHubToolStatus,
	GitHubToolCheckConnection,
	GitHubToolListRepositories,
	GitHubToolListPullRequests,
	GitHubToolGetPullRequest,
	GitHubToolListIssues,
	GitHubToolGetIssue,
	GitHubToolListWorkflowRuns,
	GitHubToolGetWorkflowRunJobs,
	GitHubToolCloneRepository,
	GitHubToolPullRepository,
	GitHubToolRefreshRepository,
	GitHubToolRunGHCommand,
}

const (
	githubOperationCloneRepository   = "github_clone_repository_execute"
	githubOperationPullRepository    = "github_pull_repository_execute"
	githubOperationRefreshRepository = "github_refresh_repository_execute"
	githubOperationRawGHCommand      = "github_raw_gh_command_execute"
)

func GitHubToolNames() []string {
	return append([]string(nil), githubToolNames...)
}

func IsGitHubToolName(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	for _, candidate := range githubToolNames {
		if toolName == candidate {
			return true
		}
	}
	return false
}

func newGitHubStatusTool(manager any) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(
			GitHubToolStatus,
			"Return the local GitHub CLI integration status. Does not require a project.",
			map[string]any{},
		),
		Exec: func(ctx context.Context, _ ExecContext, _ json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			status, err := githubCallNoArgs(ctx, manager, "Status")
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return githubOutput(GitHubToolStatus, map[string]any{
				"status": status,
			}, "status"), nil
		},
	}
}

func newGitHubCheckConnectionTool(manager any) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(
			GitHubToolCheckConnection,
			"Read-only check of the local GitHub CLI connection. Does not require a project.",
			map[string]any{},
		),
		Exec: func(ctx context.Context, _ ExecContext, _ json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			status, err := githubCallNoArgs(ctx, manager, "Check")
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return githubOutput(GitHubToolCheckConnection, map[string]any{
				"status": status,
			}, "connection_status"), nil
		},
	}
}

func newGitHubListRepositoriesTool(manager any) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(
			GitHubToolListRepositories,
			"List GitHub repositories cloned or available to the app. Does not require a project when app-managed repository sources exist.",
			objectSchema(map[string]any{}, nil),
		),
		Exec: func(ctx context.Context, exec ExecContext, _ json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			projectID := strings.TrimSpace(exec.ProjectID)
			repos, err := githubCallProject(ctx, manager, "ListRepositories", projectID)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			result := map[string]any{
				"repositories": repos,
			}
			if projectID != "" {
				result["project_id"] = projectID
			}
			return githubOutput(GitHubToolListRepositories, result, "repository_list"), nil
		},
	}
}

func newGitHubListPullRequestsTool(manager any) Tool {
	return githubRepositoryListTool(
		manager,
		GitHubToolListPullRequests,
		"List pull requests for an app-managed or project GitHub repository.",
		"pull_requests",
		"pull_request_list",
	)
}

func newGitHubGetPullRequestTool(manager any) Tool {
	return githubNumberedRepositoryTool(
		manager,
		GitHubToolGetPullRequest,
		"Read a pull request by number for an app-managed or project GitHub repository.",
		"pull_request",
		"pull_request",
	)
}

func newGitHubListIssuesTool(manager any) Tool {
	return githubRepositoryListTool(
		manager,
		GitHubToolListIssues,
		"List issues for an app-managed or project GitHub repository.",
		"issues",
		"issue_list",
	)
}

func newGitHubGetIssueTool(manager any) Tool {
	return githubNumberedRepositoryTool(
		manager,
		GitHubToolGetIssue,
		"Read an issue by number for an app-managed or project GitHub repository.",
		"issue",
		"issue",
	)
}

func newGitHubListWorkflowRunsTool(manager any) Tool {
	return githubRepositoryListTool(
		manager,
		GitHubToolListWorkflowRuns,
		"List GitHub Actions workflow runs for an app-managed or project GitHub repository.",
		"workflow_runs",
		"workflow_run_list",
	)
}

func newGitHubGetWorkflowRunJobsTool(manager any) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(
			GitHubToolGetWorkflowRunJobs,
			"Read jobs for a GitHub Actions workflow run in an app-managed or project GitHub repository.",
			objectSchema(githubRepositoryProperties(map[string]any{
				"run_id": stringSchema("GitHub Actions workflow run ID."),
			}), []string{"run_id"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			projectID := strings.TrimSpace(exec.ProjectID)
			var in struct {
				Repository string `json:"repository"`
				RunID      string `json:"run_id"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			runID := strings.TrimSpace(in.RunID)
			if runID == "" {
				return JSONError("run_id is required"), nil
			}
			repository := strings.TrimSpace(in.Repository)
			result, err := githubCallRequest(ctx, manager, GitHubToolGetWorkflowRunJobs, "GetWorkflowRunJobs", githubRequestFields{
				ProjectID:  projectID,
				Repository: repository,
				RunID:      runID,
			})
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return githubOutput(GitHubToolGetWorkflowRunJobs, map[string]any{
				"project_id": projectID,
				"repository": repository,
				"run_id":     runID,
				"jobs":       result,
			}, "workflow_run_jobs"), nil
		},
	}
}

func newGitHubCloneRepositoryTool(manager any) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(
			GitHubToolCloneRepository,
			"Clone a GitHub repository after persistent permission approval into the app-managed repository source store. Supports shallow, partial, and sparse clones for large repositories. After cloning, use repository/source tools and local git tools for repo-scoped code search instead of gh search code.",
			objectSchema(map[string]any{
				"repository":      stringSchema("GitHub repository owner/name or github.com URL to clone."),
				"name":            stringSchema("Optional display/destination name. App-managed source clones use a stable repository source ID."),
				"branch":          stringSchema("Optional branch or tag to check out."),
				"partial_clone":   boolSchema("Use git partial clone with blob filtering. Recommended for large repositories."),
				"sparse_paths":    stringArraySchema("Optional sparse-checkout paths to materialize after cloning."),
				"depth":           intSchema("Optional shallow clone depth. Use 1 for only the latest commit."),
				"timeout_seconds": intSchema("Optional approved command timeout in seconds, capped by the server."),
				"dry_run":         boolSchema("Validate and preview the clone operation without cloning or requesting permission."),
			}, []string{"repository"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			projectID := strings.TrimSpace(exec.ProjectID)
			var in struct {
				Repository     string   `json:"repository"`
				Name           string   `json:"name"`
				Branch         string   `json:"branch"`
				PartialClone   bool     `json:"partial_clone"`
				SparsePaths    []string `json:"sparse_paths"`
				Depth          int      `json:"depth"`
				TimeoutSeconds int      `json:"timeout_seconds"`
				DryRun         bool     `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			repository := strings.TrimSpace(in.Repository)
			if repository == "" {
				return JSONError("repository is required"), nil
			}
			argsMap := map[string]any{
				"project_id":      projectID,
				"repository":      repository,
				"name":            strings.TrimSpace(in.Name),
				"branch":          strings.TrimSpace(in.Branch),
				"partial_clone":   in.PartialClone,
				"sparse_paths":    append([]string(nil), in.SparsePaths...),
				"depth":           in.Depth,
				"timeout_seconds": in.TimeoutSeconds,
			}
			if in.DryRun {
				preview := map[string]any{
					"repository": repository,
					"command":    githubCloneDetail(repository, in.Branch, in.PartialClone, in.SparsePaths, in.Depth),
				}
				if projectID != "" {
					preview["project_id"] = projectID
				} else {
					preview["destination"] = "app-managed repository sources"
				}
				return githubDryRunOutput(GitHubToolCloneRepository, githubOperationCloneRepository, argsMap, preview), nil
			}
			if exec.PermissionApproved {
				emitGitHubProgress(exec, GitHubToolCloneRepository, "running", "Cloning approved GitHub repository", 25, map[string]any{
					"provider":   "github",
					"product":    "github",
					"repository": repository,
					"project_id": projectID,
				})
				result, err := githubRunApprovedOperation(ctx, manager, githubOperationCloneRepository, argsMap)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				emitGitHubProgress(exec, GitHubToolCloneRepository, "completed", "Cloned GitHub repository", 100, map[string]any{
					"provider":   "github",
					"product":    "github",
					"repository": repository,
					"project_id": resultString(result, "project_id"),
				})
				return githubOperationOutput(GitHubToolCloneRepository, result, "repository_write"), nil
			}
			emitGitHubProgress(exec, GitHubToolCloneRepository, "permission_required", "Persistent approval is required before cloning this GitHub repository", 100, map[string]any{
				"provider":   "github",
				"product":    "github",
				"repository": repository,
				"project_id": projectID,
			})
			target := map[string]any{
				"type":       "repository",
				"repository": repository,
			}
			if projectID != "" {
				target["project_id"] = projectID
			} else {
				target["destination"] = "app-managed repository sources"
			}
			return githubPersistentPermissionRequiredOutput(GitHubToolCloneRepository, githubOperationCloneRepository, target), nil
		},
	}
}

func newGitHubPullRepositoryTool(manager any) Tool {
	return githubQueueRefreshTool(
		manager,
		GitHubToolPullRepository,
		"Run git fetch plus git pull --ff-only after persistent permission approval for an app-managed or project GitHub repository. After pulling, use repository/source tools and local git tools for repo-scoped code search instead of gh search code.",
		"Pull GitHub repository",
		"pull_repository",
		githubOperationPullRepository,
	)
}

func newGitHubRefreshRepositoryTool(manager any) Tool {
	return githubQueueRefreshTool(
		manager,
		GitHubToolRefreshRepository,
		"Refresh one or all app-managed or project GitHub repositories after persistent permission approval with git fetch plus git pull --ff-only. Use this before local repository search when contents may be stale.",
		"Refresh GitHub repositories",
		"refresh_repository",
		githubOperationRefreshRepository,
	)
}

func newGitHubRunGHCommandTool(manager any) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(
			GitHubToolRunGHCommand,
			"Run raw GitHub CLI commands using app-managed authentication when typed GitHub/local file tools do not cover the request. Safe read-only commands run directly; mutating commands require persistent permission approval. Prefer list_git_repositories plus git_grep/git_show_file_at_ref/git_log for repo-scoped code search in app-managed local clones; use list_files/search_files/read_file only for explicit imported project snapshots. Use gh search code/search_code only as a remote broad-discovery fallback because GitHub's code_search API has tight rate limits. Pass arguments without shell syntax; credentials/admin commands and extension/codespace escape hatches are blocked.",
			objectSchema(map[string]any{
				"args": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"minItems":    1,
					"description": "GitHub CLI arguments, optionally including a leading gh token. Do not include shell syntax. Avoid gh search code/search_code for repo-scoped searches when the repository can be cloned or is already attached locally.",
				},
				"timeout_seconds": intSchema("Optional command timeout in seconds, capped by the server."),
				"dry_run":         boolSchema("Validate and preview the gh command without running it or requesting permission."),
			}, []string{"args"}),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			var in struct {
				Args           []string `json:"args"`
				TimeoutSeconds int      `json:"timeout_seconds"`
				DryRun         bool     `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			normalizedArgs, err := githubNormalizeRawGHArgs(in.Args)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			command := "gh " + strings.Join(normalizedArgs, " ")
			if in.DryRun {
				return githubDryRunOutput(GitHubToolRunGHCommand, githubOperationRawGHCommand, map[string]any{
					"project_id":      strings.TrimSpace(exec.ProjectID),
					"args":            append([]string(nil), normalizedArgs...),
					"timeout_seconds": in.TimeoutSeconds,
				}, map[string]any{
					"command":   command,
					"read_only": githubRawGHCommandIsReadOnly(normalizedArgs),
				}), nil
			}
			if githubRawGHCommandIsReadOnly(normalizedArgs) {
				emitGitHubProgress(exec, GitHubToolRunGHCommand, "running", "Running read-only gh command", 25, map[string]any{
					"provider": "github",
					"product":  "github",
					"command":  command,
				})
				result, err := githubRunReadGHCommand(ctx, manager, strings.TrimSpace(exec.ProjectID), normalizedArgs, in.TimeoutSeconds)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				emitGitHubProgress(exec, GitHubToolRunGHCommand, "completed", "Completed read-only gh command", 100, map[string]any{
					"provider": "github",
					"product":  "github",
					"command":  command,
				})
				return githubOutput(GitHubToolRunGHCommand, map[string]any{
					"command": command,
					"result":  result,
				}, "gh_command"), nil
			}
			projectID := strings.TrimSpace(exec.ProjectID)
			operationArgs := map[string]any{
				"project_id":      projectID,
				"args":            append([]string(nil), normalizedArgs...),
				"timeout_seconds": in.TimeoutSeconds,
			}
			if exec.PermissionApproved {
				emitGitHubProgress(exec, GitHubToolRunGHCommand, "running", "Running approved gh command", 25, map[string]any{
					"provider": "github",
					"product":  "github",
					"command":  command,
				})
				result, err := githubRunApprovedOperation(ctx, manager, githubOperationRawGHCommand, operationArgs)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				result["command"] = command
				emitGitHubProgress(exec, GitHubToolRunGHCommand, "completed", "Completed gh command", 100, map[string]any{
					"provider": "github",
					"product":  "github",
					"command":  command,
				})
				return githubOperationOutput(GitHubToolRunGHCommand, result, "gh_command"), nil
			}
			emitGitHubProgress(exec, GitHubToolRunGHCommand, "permission_required", "Persistent approval is required before running this gh command", 100, map[string]any{
				"provider": "github",
				"product":  "github",
				"command":  command,
			})
			return githubPersistentPermissionRequiredOutput(GitHubToolRunGHCommand, githubOperationRawGHCommand, map[string]any{
				"type":       "command",
				"project_id": projectID,
				"command":    command,
			}), nil
		},
	}
}

func githubQueueRefreshTool(manager any, toolName, description, title, action, operation string) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(
			toolName,
			description,
			objectSchema(githubRepositoryProperties(map[string]any{
				"best_effort":     boolSchema("Continue refreshing remaining repositories if one repository fails."),
				"timeout_seconds": intSchema("Optional timeout per git fetch or pull command in seconds, capped by the server."),
				"dry_run":         boolSchema("Validate and preview the repository refresh operation without running git or requesting permission."),
			}), nil),
		),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			projectID := strings.TrimSpace(exec.ProjectID)
			var in struct {
				Repository     string `json:"repository"`
				BestEffort     bool   `json:"best_effort"`
				TimeoutSeconds int    `json:"timeout_seconds"`
				DryRun         bool   `json:"dry_run"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			repository := strings.TrimSpace(in.Repository)
			operationArgs := map[string]any{
				"project_id":      projectID,
				"repository":      repository,
				"best_effort":     in.BestEffort,
				"timeout_seconds": in.TimeoutSeconds,
			}
			targetLabel := repository
			if targetLabel == "" {
				targetLabel = "all visible GitHub repositories"
			}
			if in.DryRun {
				preview := map[string]any{
					"repository": targetLabel,
					"command":    "git fetch --prune && git pull --ff-only",
				}
				if projectID != "" {
					preview["project_id"] = projectID
				} else {
					preview["source_kind"] = "github_clone"
				}
				return githubDryRunOutput(toolName, operation, operationArgs, preview), nil
			}
			if exec.PermissionApproved {
				emitGitHubProgress(exec, toolName, "running", "Refreshing approved GitHub repository", 25, map[string]any{
					"provider":   "github",
					"product":    "github",
					"repository": targetLabel,
					"project_id": projectID,
				})
				result, err := githubRunApprovedOperation(ctx, manager, operation, operationArgs)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				emitGitHubProgress(exec, toolName, "completed", "Refreshed GitHub repository", 100, map[string]any{
					"provider":   "github",
					"product":    "github",
					"repository": targetLabel,
					"project_id": projectID,
				})
				return githubOperationOutput(toolName, result, "repository_write"), nil
			}
			emitGitHubProgress(exec, toolName, "permission_required", "Persistent approval is required before refreshing this GitHub repository", 100, map[string]any{
				"provider":   "github",
				"product":    "github",
				"repository": targetLabel,
			})
			target := map[string]any{
				"type":       "repository",
				"repository": targetLabel,
			}
			if projectID != "" {
				target["project_id"] = projectID
			} else {
				target["source_kind"] = "github_clone"
			}
			return githubPersistentPermissionRequiredOutput(toolName, operation, target), nil
		},
	}
}

func githubRepositoryListTool(manager any, name, description, resultKey, widget string) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(name, description, githubRepositoryListSchema()),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			projectID := strings.TrimSpace(exec.ProjectID)
			var in struct {
				Repository string `json:"repository"`
				State      string `json:"state"`
				Limit      int    `json:"limit"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			req := githubRequestFields{
				ProjectID:  projectID,
				Repository: strings.TrimSpace(in.Repository),
				State:      strings.TrimSpace(in.State),
				Limit:      in.Limit,
			}
			result, err := githubCallRequest(ctx, manager, name, githubMethodName(name), req)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return githubOutput(name, map[string]any{
				"project_id": projectID,
				"repository": req.Repository,
				"state":      req.State,
				"limit":      req.Limit,
				resultKey:    result,
			}, widget), nil
		},
	}
}

func githubNumberedRepositoryTool(manager any, name, description, resultKey, widget string) Tool {
	return ExecutableTool{
		Def: githubToolDefinition(name, description, objectSchema(githubRepositoryProperties(map[string]any{
			"number": map[string]any{
				"type":        "integer",
				"minimum":     1,
				"description": "Pull request or issue number.",
			},
		}), []string{"number"})),
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if githubManagerIsNil(manager) {
				return JSONError("GitHub integration is not configured"), nil
			}
			projectID := strings.TrimSpace(exec.ProjectID)
			var in struct {
				Repository string `json:"repository"`
				Number     int    `json:"number"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError(err.Error()), nil
			}
			if in.Number <= 0 {
				return JSONError("number is required"), nil
			}
			req := githubRequestFields{
				ProjectID:  projectID,
				Repository: strings.TrimSpace(in.Repository),
				Number:     in.Number,
			}
			result, err := githubCallRequest(ctx, manager, name, githubMethodName(name), req)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			return githubOutput(name, map[string]any{
				"project_id": projectID,
				"repository": req.Repository,
				"number":     req.Number,
				resultKey:    result,
			}, widget), nil
		},
	}
}

func githubToolDefinition(name, description string, parameters map[string]any) types.ToolDefinition {
	parameters = normalizeObjectSchema(parameters)
	parameters["additionalProperties"] = false
	return types.ToolDefinition{
		Type: "function",
		Function: types.ToolFunctionDefinition{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

func githubRepositoryListSchema() map[string]any {
	return objectSchema(githubRepositoryProperties(map[string]any{
		"state": map[string]any{
			"type":        "string",
			"description": "Optional state filter. Common values are open, closed, all, success, failure, cancelled, or in_progress depending on the GitHub resource.",
		},
		"limit": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     100,
			"description": "Maximum number of results to return.",
		},
	}), nil)
}

func githubRepositoryProperties(extra map[string]any) map[string]any {
	props := map[string]any{
		"repository": stringSchema("Optional repository selector, usually owner/name. If omitted, the app-managed or project GitHub configuration may provide a default when only one repository is available."),
	}
	for key, value := range extra {
		props[key] = value
	}
	return props
}

func githubProjectID(exec ExecContext) (string, Output, bool) {
	projectID := strings.TrimSpace(exec.ProjectID)
	if projectID == "" {
		return "", JSONError("project context is required for GitHub repository operations"), false
	}
	return projectID, Output{}, true
}

func githubResultMetadata(toolName, widget string) map[string]any {
	toolName = strings.TrimSpace(toolName)
	widget = strings.TrimSpace(widget)
	if widget == "" {
		widget = "github_result"
	}
	meta := toolCatalogResultMetadata(toolName, widget, map[string]any{
		"integration": "github",
		"provider":    "github",
		"product":     "github",
		"widget":      widget,
	})
	meta["integration"] = "github"
	meta["provider"] = "github"
	meta["product"] = "github"
	meta["widget"] = widget
	if family, _ := meta["family"].(string); strings.TrimSpace(family) == "" || strings.TrimSpace(family) == "tool" {
		meta["family"] = "github"
	}
	if toolSetID, _ := meta["tool_set_id"].(string); strings.TrimSpace(toolSetID) != "" {
		meta["tool_set"] = strings.TrimSpace(toolSetID)
	}
	return meta
}

func githubOutput(toolName string, result map[string]any, widget string) Output {
	return JSONOKWithMetadata(result, githubResultMetadata(toolName, widget))
}

func githubDryRunOutput(toolName, operation string, args map[string]any, preview map[string]any) Output {
	out := map[string]any{
		"ok":            true,
		"dry_run":       true,
		"validated":     true,
		"tool_name":     toolName,
		"operation":     operation,
		"would_execute": operation,
		"args":          cloneStringAnyMap(args),
		"preview":       cloneStringAnyMap(preview),
		"message":       "Dry run only. GitHub state and local repositories were not changed.",
	}
	return githubOutput(toolName, out, "dry_run")
}

func githubPersistentPermissionRequiredOutput(toolName, operation string, target map[string]any) Output {
	return Output{
		OK:           false,
		ResultFormat: ResultFormatJSON,
		Result: map[string]any{
			"ok":                  false,
			"status":              "permission_required",
			"permission_required": true,
			"tool_name":           toolName,
			"operation":           operation,
			"target":              cloneStringAnyMap(target),
			"message":             "Persistent permission approval is required before this GitHub operation can run.",
		},
		ResultMetadata: githubResultMetadata(toolName, "permission_required"),
	}
}

func boolSchema(description string) map[string]any {
	return map[string]any{
		"type":        "boolean",
		"description": description,
	}
}

func intSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"minimum":     1,
		"description": description,
	}
}

func stringArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"items":       map[string]any{"type": "string"},
		"description": description,
	}
}

func githubRunApprovedOperation(ctx context.Context, manager any, operation string, args map[string]any) (map[string]any, error) {
	method := reflect.ValueOf(manager).MethodByName("RunApprovedOperation")
	if !method.IsValid() {
		return nil, fmt.Errorf("GitHub integration does not implement approved operation execution")
	}
	methodType := method.Type()
	if methodType.NumIn() != 3 || methodType.NumOut() != 2 {
		return nil, fmt.Errorf("GitHub integration method RunApprovedOperation has unexpected signature")
	}
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	if !contextType.AssignableTo(methodType.In(0)) {
		return nil, fmt.Errorf("GitHub RunApprovedOperation context argument must be context.Context")
	}
	if !reflect.TypeOf("").AssignableTo(methodType.In(1)) {
		return nil, fmt.Errorf("GitHub RunApprovedOperation operation argument must be a string")
	}
	mapType := reflect.TypeOf(map[string]any{})
	if !mapType.AssignableTo(methodType.In(2)) {
		return nil, fmt.Errorf("GitHub RunApprovedOperation args argument must be map[string]any")
	}
	results := method.Call([]reflect.Value{
		reflect.ValueOf(ctx),
		reflect.ValueOf(strings.TrimSpace(operation)),
		reflect.ValueOf(cloneStringAnyMap(args)),
	})
	if !results[1].IsNil() {
		err, _ := results[1].Interface().(error)
		if err == nil {
			err = fmt.Errorf("GitHub RunApprovedOperation failed")
		}
		return nil, err
	}
	if results[0].IsNil() {
		return map[string]any{}, nil
	}
	result, ok := results[0].Interface().(map[string]any)
	if !ok {
		return nil, fmt.Errorf("GitHub RunApprovedOperation result must be map[string]any")
	}
	return cloneStringAnyMap(result), nil
}

func githubOperationOutput(toolName string, result map[string]any, widget string) Output {
	out := cloneStringAnyMap(result)
	return githubOutput(toolName, out, widget)
}

func resultString(result map[string]any, key string) string {
	if result == nil {
		return ""
	}
	if value, ok := result[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func githubRunReadGHCommand(ctx context.Context, manager any, projectID string, args []string, timeoutSeconds int) (any, error) {
	method := reflect.ValueOf(manager).MethodByName("RunReadGHCommand")
	if !method.IsValid() {
		return nil, fmt.Errorf("GitHub integration does not implement RunReadGHCommand")
	}
	methodType := method.Type()
	if methodType.NumIn() != 2 || methodType.NumOut() != 2 {
		return nil, fmt.Errorf("GitHub integration method RunReadGHCommand has unexpected signature")
	}
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	if !contextType.AssignableTo(methodType.In(0)) {
		return nil, fmt.Errorf("GitHub RunReadGHCommand context argument must be context.Context")
	}
	reqType := methodType.In(1)
	asPointer := reqType.Kind() == reflect.Pointer
	if asPointer {
		reqType = reqType.Elem()
	}
	if reqType.Kind() != reflect.Struct {
		return nil, fmt.Errorf("GitHub RunReadGHCommand request type must be a struct")
	}
	req := reflect.New(reqType).Elem()
	githubSetStringField(req, "ProjectID", projectID)
	githubSetStringSliceField(req, "Args", append([]string(nil), args...))
	githubSetIntField(req, "TimeoutSeconds", int64(timeoutSeconds))
	var reqArg reflect.Value
	if asPointer {
		ptr := reflect.New(reqType)
		ptr.Elem().Set(req)
		reqArg = ptr
	} else {
		reqArg = req
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(ctx), reqArg})
	if errValue := results[1]; !errValue.IsNil() {
		err, _ := errValue.Interface().(error)
		if err != nil {
			return nil, err
		}
	}
	return results[0].Interface(), nil
}

func githubSetMapField(req reflect.Value, name string, value map[string]any) bool {
	field := req.FieldByName(name)
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Map {
		return false
	}
	mapValue := reflect.ValueOf(value)
	if !mapValue.Type().AssignableTo(field.Type()) {
		return false
	}
	field.Set(mapValue)
	return true
}

func githubSetStringSliceField(req reflect.Value, name string, value []string) bool {
	field := req.FieldByName(name)
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Slice || field.Type().Elem().Kind() != reflect.String {
		return false
	}
	field.Set(reflect.ValueOf(value))
	return true
}

func githubCloneDetail(repository string, branch string, partialClone bool, sparsePaths []string, depth int) string {
	parts := []string{"git clone"}
	if partialClone || len(sparsePaths) > 0 {
		parts = append(parts, "--filter=blob:none")
	}
	if len(sparsePaths) > 0 {
		parts = append(parts, "--sparse")
	}
	if depth > 0 {
		parts = append(parts, "--depth", strconv.Itoa(depth))
	}
	if strings.TrimSpace(branch) != "" {
		parts = append(parts, "--branch", strings.TrimSpace(branch))
	}
	parts = append(parts, repository)
	if len(sparsePaths) > 0 {
		parts = append(parts, "(sparse paths: "+strings.Join(sparsePaths, ", ")+")")
	}
	return strings.Join(parts, " ")
}

var blockedGitHubRawCommands = map[string]string{
	"alias":      "gh alias can hide or rewrite future commands",
	"auth":       "gh auth commands can expose or alter credentials",
	"completion": "gh completion is local shell configuration",
	"config":     "gh config can expose or alter local client configuration",
	"codespace":  "gh codespace can open remote shells and port forwards",
	"extension":  "gh extension can install and execute third-party code",
	"gpg-key":    "gh gpg-key manages account credentials",
	"ssh-key":    "gh ssh-key manages account credentials",
}

func githubNormalizeRawGHArgs(input []string) ([]string, error) {
	args := make([]string, 0, len(input))
	for _, raw := range input {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if len(value) > 4000 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("gh arguments must be finite strings without control characters")
		}
		args = append(args, value)
	}
	if len(args) > 0 && strings.EqualFold(args[0], "gh") {
		args = args[1:]
	}
	args = githubCanonicalRawGHArgs(args)
	if len(args) == 0 {
		return nil, fmt.Errorf("gh command arguments are required")
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	if strings.HasPrefix(command, "-") {
		return nil, fmt.Errorf("the first gh argument must be a command, not a flag")
	}
	if reason, blocked := blockedGitHubRawCommands[command]; blocked {
		return nil, fmt.Errorf("blocked gh %s: %s", command, reason)
	}
	for _, arg := range args {
		if strings.EqualFold(arg, "--show-token") {
			return nil, fmt.Errorf("blocked gh flag: --show-token")
		}
	}
	return args, nil
}

func NormalizeGitHubRawGHArgs(input []string) ([]string, error) {
	return githubNormalizeRawGHArgs(input)
}

func githubRawGHCommandIsReadOnly(args []string) bool {
	args = githubCanonicalRawGHArgs(args)
	if len(args) == 0 {
		return false
	}
	if githubRawGHReadCommandHasBlockedFlag(args[1:]) {
		return false
	}
	command := strings.ToLower(strings.TrimSpace(args[0]))
	if command == "status" {
		return true
	}
	if command == "api" {
		return githubRawGHAPICommandIsReadOnly(args[1:])
	}
	if len(args) < 2 {
		return false
	}
	subcommand := strings.ToLower(strings.TrimSpace(args[1]))
	if command == "search" {
		return githubStringSetContains(githubReadOnlySearchSubcommands, subcommand)
	}
	if allowed, ok := githubReadOnlySubcommands[command]; ok {
		return githubStringSetContains(allowed, subcommand)
	}
	return false
}

func GitHubRawGHCommandIsReadOnly(args []string) bool {
	return githubRawGHCommandIsReadOnly(args)
}

func githubRawGHAPICommandIsReadOnly(args []string) bool {
	method := ""
	hasFields := false
	endpoint := ""
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		lower := strings.ToLower(arg)
		switch {
		case lower == "--method" || lower == "-x":
			if i+1 >= len(args) {
				return false
			}
			i++
			method = strings.ToUpper(strings.TrimSpace(args[i]))
		case strings.HasPrefix(lower, "--method="):
			method = strings.ToUpper(strings.TrimSpace(arg[len("--method="):]))
		case strings.HasPrefix(lower, "-x") && len(arg) > 2:
			method = strings.ToUpper(strings.TrimSpace(arg[2:]))
		case lower == "--field" || lower == "--raw-field" || lower == "-f":
			if i+1 >= len(args) {
				return false
			}
			hasFields = true
			i++
		case strings.HasPrefix(lower, "--field=") || strings.HasPrefix(lower, "--raw-field="):
			hasFields = true
		case strings.HasPrefix(lower, "-f") && len(arg) > 2:
			hasFields = true
		case lower == "--input":
			return false
		case strings.HasPrefix(lower, "--input="):
			return false
		case githubRawGHAPIFlagConsumesValue(lower):
			if i+1 >= len(args) {
				return false
			}
			i++
		case strings.HasPrefix(arg, "-"):
			continue
		case endpoint == "":
			endpoint = strings.TrimSpace(arg)
		default:
			return false
		}
	}
	if endpoint == "" || strings.EqualFold(endpoint, "graphql") {
		return false
	}
	if method == "" && hasFields {
		return false
	}
	if method == "" {
		method = "GET"
	}
	return method == "GET" || method == "HEAD"
}

func githubRawGHAPIFlagConsumesValue(flag string) bool {
	switch {
	case flag == "--header" || flag == "-h":
		return true
	case strings.HasPrefix(flag, "--header="):
		return false
	case strings.HasPrefix(flag, "-h") && len(flag) > 2:
		return false
	case flag == "--hostname":
		return true
	case strings.HasPrefix(flag, "--hostname="):
		return false
	case flag == "--jq" || flag == "-q":
		return true
	case strings.HasPrefix(flag, "--jq="):
		return false
	case strings.HasPrefix(flag, "-q") && len(flag) > 2:
		return false
	case flag == "--preview" || flag == "-p":
		return true
	case strings.HasPrefix(flag, "--preview="):
		return false
	case strings.HasPrefix(flag, "-p") && len(flag) > 2:
		return false
	case flag == "--template" || flag == "-t":
		return true
	case strings.HasPrefix(flag, "--template="):
		return false
	case strings.HasPrefix(flag, "-t") && len(flag) > 2:
		return false
	case flag == "--cache":
		return true
	case strings.HasPrefix(flag, "--cache="):
		return false
	default:
		return false
	}
}

func githubCanonicalRawGHArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "search_code":
		return append([]string{"search", "code"}, args[1:]...)
	case "search_commits":
		return append([]string{"search", "commits"}, args[1:]...)
	case "search_issues":
		return append([]string{"search", "issues"}, args[1:]...)
	case "search_prs":
		return append([]string{"search", "prs"}, args[1:]...)
	case "search_repos":
		return append([]string{"search", "repos"}, args[1:]...)
	case "rs":
		return append([]string{"ruleset"}, args[1:]...)
	default:
		return args
	}
}

var githubReadOnlySearchSubcommands = map[string]struct{}{
	"code":    {},
	"commits": {},
	"issues":  {},
	"prs":     {},
	"repos":   {},
}

var githubReadOnlySubcommands = map[string]map[string]struct{}{
	"cache": {
		"list": {},
	},
	"gist": {
		"list": {},
		"view": {},
	},
	"issue": {
		"list":   {},
		"status": {},
		"view":   {},
	},
	"label": {
		"list": {},
	},
	"org": {
		"list": {},
	},
	"pr": {
		"checks": {},
		"diff":   {},
		"list":   {},
		"status": {},
		"view":   {},
	},
	"project": {
		"field-list": {},
		"item-list":  {},
		"list":       {},
		"view":       {},
	},
	"release": {
		"list": {},
		"view": {},
	},
	"repo": {
		"gitignore": {},
		"license":   {},
		"list":      {},
		"view":      {},
	},
	"ruleset": {
		"check": {},
		"list":  {},
		"view":  {},
	},
	"run": {
		"list":  {},
		"view":  {},
		"watch": {},
	},
	"workflow": {
		"list": {},
		"view": {},
	},
}

func githubStringSetContains(values map[string]struct{}, value string) bool {
	_, ok := values[strings.ToLower(strings.TrimSpace(value))]
	return ok
}

func githubRawGHReadCommandHasBlockedFlag(args []string) bool {
	for _, arg := range args {
		flag := strings.ToLower(strings.TrimSpace(arg))
		if flag == "--web" || flag == "-w" || strings.HasPrefix(flag, "--web=") {
			return true
		}
	}
	return false
}

func emitGitHubProgress(exec ExecContext, toolName, phase, label string, percent int, fields map[string]any) {
	if exec.OnProgress == nil {
		return
	}
	progress := map[string]any{
		"provider":  "github",
		"product":   "github",
		"tool_name": toolName,
		"phase":     phase,
		"label":     label,
		"percent":   percent,
	}
	for key, value := range fields {
		progress[key] = value
	}
	exec.OnProgress(progress)
}

type githubRequestFields struct {
	ProjectID  string
	Repository string
	State      string
	Limit      int
	Number     int
	RunID      string
}

func githubMethodName(toolName string) string {
	switch toolName {
	case GitHubToolListPullRequests:
		return "ListPullRequests"
	case GitHubToolGetPullRequest:
		return "GetPullRequest"
	case GitHubToolListIssues:
		return "ListIssues"
	case GitHubToolGetIssue:
		return "GetIssue"
	case GitHubToolListWorkflowRuns:
		return "ListWorkflowRuns"
	case GitHubToolGetWorkflowRunJobs:
		return "GetWorkflowRunJobs"
	default:
		return ""
	}
}

func githubManagerIsNil(manager any) bool {
	if manager == nil {
		return true
	}
	v := reflect.ValueOf(manager)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func githubCallNoArgs(ctx context.Context, manager any, methodName string) (any, error) {
	method := reflect.ValueOf(manager).MethodByName(methodName)
	if !method.IsValid() {
		return nil, fmt.Errorf("GitHub integration does not implement %s", methodName)
	}
	return githubCall(method, []reflect.Value{reflect.ValueOf(ctx)})
}

func githubCallProject(ctx context.Context, manager any, methodName string, projectID string) (any, error) {
	method := reflect.ValueOf(manager).MethodByName(methodName)
	if !method.IsValid() {
		return nil, fmt.Errorf("GitHub integration does not implement %s", methodName)
	}
	return githubCall(method, []reflect.Value{reflect.ValueOf(ctx), reflect.ValueOf(projectID)})
}

func githubCallRequest(ctx context.Context, manager any, toolName string, methodName string, fields githubRequestFields) (any, error) {
	if strings.TrimSpace(methodName) == "" {
		return nil, fmt.Errorf("GitHub tool %s is not mapped to a manager method", toolName)
	}
	method := reflect.ValueOf(manager).MethodByName(methodName)
	if !method.IsValid() {
		return nil, fmt.Errorf("GitHub integration does not implement %s", methodName)
	}
	methodType := method.Type()
	if methodType.NumIn() != 2 {
		return nil, fmt.Errorf("GitHub integration method %s has unexpected argument count", methodName)
	}
	reqType := methodType.In(1)
	req, err := githubBuildRequest(reqType, fields)
	if err != nil {
		return nil, err
	}
	return githubCall(method, []reflect.Value{reflect.ValueOf(ctx), req})
}

func githubCall(method reflect.Value, args []reflect.Value) (any, error) {
	methodType := method.Type()
	if methodType.NumIn() < 1 || !reflect.TypeOf((*context.Context)(nil)).Elem().AssignableTo(methodType.In(0)) {
		return nil, errorsNewGitHubMethodSignature(method)
	}
	if len(args) != methodType.NumIn() {
		return nil, errorsNewGitHubMethodSignature(method)
	}
	results := method.Call(args)
	if len(results) != 2 {
		return nil, errorsNewGitHubMethodSignature(method)
	}
	if !results[1].IsNil() {
		err, ok := results[1].Interface().(error)
		if !ok {
			return nil, errorsNewGitHubMethodSignature(method)
		}
		return nil, err
	}
	return results[0].Interface(), nil
}

func errorsNewGitHubMethodSignature(method reflect.Value) error {
	return fmt.Errorf("GitHub integration method %s has unexpected signature", method.Type().String())
}

func githubBuildRequest(reqType reflect.Type, fields githubRequestFields) (reflect.Value, error) {
	asPointer := reqType.Kind() == reflect.Pointer
	if asPointer {
		reqType = reqType.Elem()
	}
	if reqType.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("GitHub integration request type must be a struct")
	}
	req := reflect.New(reqType).Elem()
	githubSetStringField(req, "ProjectID", fields.ProjectID)
	githubSetStringField(req, "Repository", fields.Repository)
	githubSetStringField(req, "RepoPath", fields.Repository)
	githubSetStringField(req, "State", fields.State)
	githubSetStringField(req, "Status", fields.State)
	githubSetIntField(req, "Limit", int64(fields.Limit))
	githubSetIntField(req, "Number", int64(fields.Number))
	if strings.TrimSpace(fields.RunID) != "" && !githubSetStringField(req, "RunID", fields.RunID) {
		runID, err := strconv.ParseInt(strings.TrimSpace(fields.RunID), 10, 64)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("run_id must be numeric for this GitHub integration")
		}
		githubSetIntField(req, "RunID", runID)
	}
	if asPointer {
		ptr := reflect.New(reqType)
		ptr.Elem().Set(req)
		return ptr, nil
	}
	return req, nil
}

func githubSetStringField(req reflect.Value, name string, value string) bool {
	field := req.FieldByName(name)
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.String {
		return false
	}
	field.SetString(value)
	return true
}

func githubSetIntField(req reflect.Value, name string, value int64) bool {
	field := req.FieldByName(name)
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	switch field.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		field.SetInt(value)
		return true
	default:
		return false
	}
}
