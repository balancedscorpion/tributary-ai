package tools

import (
	"sort"
	"strings"
)

//go:generate go run ../../../cmd/toolinventorygen -out ../../../../lib/generatedToolMetadata.ts

type ToolPermissionCategory string

const (
	ToolCategoryRead    ToolPermissionCategory = "read"
	ToolCategoryWrite   ToolPermissionCategory = "write"
	ToolCategoryExecute ToolPermissionCategory = "execute"
)

type ToolOrigin string

const (
	ToolOriginBuiltin   ToolOrigin = "builtin"
	ToolOriginConnected ToolOrigin = "connected"
	ToolOriginLocal     ToolOrigin = "local"
	ToolOriginAttached  ToolOrigin = "attached"
)

type ToolTestMode string

const (
	ToolTestModeNone            ToolTestMode = "none"
	ToolTestModeConnectionCheck ToolTestMode = "connection_check"
	ToolTestModeSampleCall      ToolTestMode = "sample_call"
	ToolTestModeDryRun          ToolTestMode = "dry_run"
)

type ToolTestSpec struct {
	Supported              bool           `json:"supported"`
	Mode                   ToolTestMode   `json:"mode"`
	RequiresApproval       bool           `json:"requires_approval"`
	InputRequired          bool           `json:"input_required"`
	RequiredArguments      []string       `json:"required_arguments,omitempty"`
	RequiredArgumentGroups [][]string     `json:"required_argument_groups,omitempty"`
	RequiredContext        []string       `json:"required_context,omitempty"`
	SampleArguments        map[string]any `json:"sample_arguments,omitempty"`
	InputHint              string         `json:"input_hint,omitempty"`
	Summary                string         `json:"summary,omitempty"`
}

type ToolPresentationSpec struct {
	Family               string   `json:"family"`
	ResultKind           string   `json:"result_kind"`
	RendererStrategy     string   `json:"renderer_strategy"`
	RendererKey          string   `json:"renderer_key"`
	NativeViewer         string   `json:"native_viewer"`
	ArtifactTypes        []string `json:"artifact_types,omitempty"`
	FileCapability       string   `json:"file_capability,omitempty"`
	FileKinds            []string `json:"file_kinds,omitempty"`
	PreviewModes         []string `json:"preview_modes,omitempty"`
	SourceRefs           []string `json:"source_refs,omitempty"`
	ProducesFileArtifact bool     `json:"produces_file_artifact,omitempty"`
	PromotesNativeInput  bool     `json:"promotes_native_input,omitempty"`
	NativeLinks          []string `json:"native_links,omitempty"`
}

type ToolMetadata struct {
	Name         string                 `json:"name"`
	Label        string                 `json:"label"`
	Description  string                 `json:"description"`
	Category     ToolPermissionCategory `json:"category"`
	Origin       ToolOrigin             `json:"origin"`
	ToolSetID    string                 `json:"tool_set_id"`
	ToolSetName  string                 `json:"tool_set_name"`
	ConnectorID  string                 `json:"connector_id,omitempty"`
	Presentation ToolPresentationSpec   `json:"presentation"`
	Test         ToolTestSpec           `json:"test"`
}

func ToolMetadataByName(name string) (ToolMetadata, bool) {
	meta, ok := toolMetadataManifest()[strings.TrimSpace(name)]
	return meta, ok
}

func AllToolMetadata() []ToolMetadata {
	manifest := toolMetadataManifest()
	names := make([]string, 0, len(manifest))
	for name := range manifest {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]ToolMetadata, 0, len(names))
	for _, name := range names {
		out = append(out, manifest[name])
	}
	return out
}

func toolMetadataManifest() map[string]ToolMetadata {
	b := newToolMetadataBuilder()

	b.read("list_files", "List imported snapshot files", "List files in an imported project snapshot copy attached to this thread.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("read_file", "Read an imported snapshot file", "Read one file from an imported project snapshot copy attached to this thread.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("search_files", "Search imported snapshot files", "Search text across files in an imported project snapshot copy attached to this thread.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("file_search", "Search files on this device", "Search file names and text under already-configured read-only Local Computer access without copying files into the app. Local access must be configured in Settings > Connections > Local Computer before this tool can search a location.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("local_list_folder", "List a folder on this device", "List files and subfolders under configured Local Computer folders without copying files into the app.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("local_read_file", "Read a file on this device", "Read a bounded text chunk from a single file under configured Local Computer access. Use this for exact text snippets or pagination; use local_open_file first for whole-document summarization, viewing, rendering, or native file input.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("local_open_file", "Open a file on this device", "Open a single file under configured Local Computer access in the chat native file viewer and provider-native file input pipeline by snapshotting only that file into app-managed thread storage.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("local_read_pdf_page", "Read one local PDF page", "Read one page from a configured Local Computer PDF by rendering only that page and OCRing it when ordinary PDF text extraction is unavailable. For scanned PDFs or exact page-number questions, use this instead of whole-document PDF extraction.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("local_list_grant_folder", "List a local file grant", "Internal local search-agent tool for listing one configured Local Computer folder grant.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.execute(LocalSearchCommandToolName, "Run a local search command", "Run validated ripgrep or fd search semantics inside one configured Local Computer grant. The broker rejects shell syntax, writes, subprocess hooks, path traversal, and commands outside the read-only search allowlist.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeDryRun)
	b.read("local_search_in_grant", "Search a local file grant", "Internal local search-agent tool for searching one configured Local Computer file grant.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("local_read_grant_file", "Read from a local file grant", "Internal local search-agent tool for reading one file from a configured Local Computer file grant.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("list_git_repositories", "List cloned repositories", "List visible local Git repositories from app-managed sources or the current project.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("git_list_refs", "List branches and tags", "List local Git branches and tags for a visible repository.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("git_log", "Read commit history in a clone", "Read commit history for a visible local Git repository.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("git_search_history", "Search commit history", "Search commits in a visible local Git repository.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("git_grep", "Search files in a clone", "Run a bounded Git grep over a visible local repository.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("git_show_commit", "Read a commit", "Read details and patches for a commit in a visible repository.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)
	b.read("git_show_file_at_ref", "Read a file at a branch or tag", "Read a repository file at a specific branch, tag, or commit.", ToolOriginLocal, "local-files", "Local files & repositories", "", ToolTestModeSampleCall)

	b.read("inspect_attachment", "Inspect an attached file", "Inspect metadata and parser capabilities for a conversation attachment.", ToolOriginAttached, "attachments", "Attachments", "", ToolTestModeSampleCall)
	b.read("read_attachment", "Read an attached file", "Read paged text or promoted native-file content from a conversation attachment.", ToolOriginAttached, "attachments", "Attachments", "", ToolTestModeSampleCall)
	b.read("search_attachment", "Search an attached file", "Search within a conversation attachment.", ToolOriginAttached, "attachments", "Attachments", "", ToolTestModeSampleCall)

	b.read("get_artifact_chunk", "Read part of a large artifact", "Read a bounded chunk from a persisted artifact.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("search_artifact_content", "Search artifact content", "Search text inside a persisted artifact.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("get_json_artifact", "Read a JSON artifact", "Read structured JSON content from a persisted artifact.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("get_echarts_capabilities", "Read chart capabilities (ECharts)", "Return the supported ECharts plot capability snapshot.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("get_highcharts_capabilities", "Read chart capabilities (Highcharts)", "Return the supported Highcharts plot capability snapshot.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("build_chart_artifact", "Create a chart", "Create a chart artifact from structured data.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("create_interactive_artifact", "Create an interactive artifact", "Create a sandboxed interactive HTML artifact.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("create_interactive_data_artifact", "Create an interactive data artifact", "Create a sandboxed interactive HTML artifact bound to a dataset.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("display_table", "Display a table", "Render tabular data as an in-app artifact.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.read("export_thread", "Export this conversation", "Export a conversation bundle as a downloadable artifact.", ToolOriginBuiltin, "artifacts", "Artifacts & charts", "", ToolTestModeSampleCall)
	b.connectedExecute("image-generation", "Image Generation", generateImageToolName, "Generate an image", "Generate a raster image with the configured image model and save it as a reusable file artifact.", ToolTestModeDryRun)

	b.read("describe_dataset", "Describe an uploaded dataset", "Profile an uploaded CSV dataset.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)
	b.read("sample_rows", "Sample dataset rows", "Read a bounded sample of rows from an uploaded CSV dataset.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)
	b.read("filter_csv", "Filter a CSV", "Filter rows in an uploaded CSV dataset.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)
	b.read("aggregate_csv", "Aggregate a CSV", "Aggregate rows in an uploaded CSV dataset.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)
	b.read("inspect_dataset_artifact", "Inspect a dataset artifact", "Inspect schema and row metadata for a dataset artifact.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)
	b.read("get_dataset_rows", "Read dataset rows", "Read rows from a persisted dataset artifact.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)
	b.read("aggregate_dataset", "Aggregate a dataset", "Aggregate rows from a persisted dataset artifact.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)
	b.read("search_dataset_rows", "Search dataset rows", "Search rows in a persisted dataset artifact.", ToolOriginLocal, "data-analysis", "Data analysis", "", ToolTestModeSampleCall)

	b.read("check_query_status", "Check a running query", "Check status for an asynchronous warehouse query.", ToolOriginBuiltin, "query-lifecycle", "Query lifecycle", "", ToolTestModeSampleCall)
	b.read("get_query_results", "Fetch query results", "Fetch results for a completed asynchronous warehouse query.", ToolOriginBuiltin, "query-lifecycle", "Query lifecycle", "", ToolTestModeSampleCall)
	b.read("validate_query_result", "Validate query results", "Explicitly validate or reject completed asynchronous query results in app-owned state; never mutates the warehouse query.", ToolOriginBuiltin, "query-lifecycle", "Query lifecycle", "", ToolTestModeSampleCall)
	b.execute("cancel_query", "Cancel a running query", "Cancel an asynchronous warehouse query for an explicit durable reason; slowness alone is not a cancellation reason.", ToolOriginBuiltin, "query-lifecycle", "Query lifecycle", "", ToolTestModeDryRun)

	b.connectedRead("thread-search", "Search past conversations", "search_threads", "Search past conversations", "Search indexed conversations and messages.", ToolTestModeSampleCall)
	b.connectedRead("thread-search", "Search past conversations", "search_docs", "Search thread documents", "Search indexed thread documents and artifacts.", ToolTestModeSampleCall)
	b.connectedRead("thread-search", "Search past conversations", "get_doc", "Read a thread document", "Read an indexed thread document.", ToolTestModeSampleCall)
	b.connectedRead("thread-search", "Search past conversations", "get_artifact", "Read a generated artifact", "Read an artifact from a conversation.", ToolTestModeSampleCall)
	b.connectedRead("thread-search", "Search past conversations", "list_messages", "List messages in a thread", "List messages from a conversation.", ToolTestModeSampleCall)
	b.connectedExecute("thread-search", "Search past conversations", "rlm_exec", "Run a retrieval script over threads", "Run a bounded Starlark retrieval script over indexed conversations.", ToolTestModeDryRun)

	b.datasource("bigquery", "BigQuery", "run_bigquery_query", "Run a SQL query", "Run a bounded BigQuery SQL query.")
	b.datasource("bigquery", "BigQuery", "list_bq_datasets", "List datasets", "List BigQuery datasets.")
	b.datasource("bigquery", "BigQuery", "list_bq_tables", "List tables", "List BigQuery tables.")
	b.datasource("bigquery", "BigQuery", "get_bq_table_schema", "Read a table schema", "Read BigQuery table schema metadata.")

	b.datasource("databricks", "Databricks", "run_databricks_query", "Run a SQL query", "Run a bounded Databricks SQL query.")
	b.datasource("databricks", "Databricks", "list_databricks_catalogs", "List catalogs", "List Databricks catalogs.")
	b.datasource("databricks", "Databricks", "list_databricks_schemas", "List schemas", "List Databricks schemas.")
	b.datasource("databricks", "Databricks", "list_databricks_tables", "List tables", "List Databricks tables.")
	b.datasource("databricks", "Databricks", "describe_databricks_table", "Read a table schema", "Read Databricks table schema metadata.")

	b.datasource("trino", "Trino", "run_trino_query", "Run a SQL query", "Run a bounded Trino SQL query.")
	b.datasource("trino", "Trino", "list_trino_catalogs", "List catalogs", "List Trino catalogs.")
	b.datasource("trino", "Trino", "list_trino_schemas", "List schemas", "List Trino schemas.")
	b.datasource("trino", "Trino", "list_trino_tables", "List tables", "List Trino tables.")
	b.datasource("trino", "Trino", "describe_trino_table", "Read a table schema", "Read Trino table schema metadata.")

	b.datasource("snowflake", "Snowflake", "run_snowflake_query", "Run a SQL query", "Run a bounded Snowflake SQL query.")
	b.datasource("snowflake", "Snowflake", "list_snowflake_databases", "List databases", "List Snowflake databases.")
	b.datasource("snowflake", "Snowflake", "list_snowflake_schemas", "List schemas", "List Snowflake schemas.")
	b.datasource("snowflake", "Snowflake", "list_snowflake_tables", "List tables", "List Snowflake tables.")
	b.datasource("snowflake", "Snowflake", "describe_snowflake_table", "Read a table schema", "Read Snowflake table schema metadata.")

	b.connectedRead("github", "GitHub", GitHubToolStatus, "Check connection status", "Return local GitHub integration status.", ToolTestModeConnectionCheck)
	b.connectedRead("github", "GitHub", GitHubToolCheckConnection, "Verify the GitHub sign-in", "Check the local GitHub CLI connection.", ToolTestModeConnectionCheck)
	b.connectedRead("github", "GitHub", GitHubToolListRepositories, "List repositories", "List app-managed or project GitHub repository clones available to the app.", ToolTestModeSampleCall)
	b.connectedRead("github", "GitHub", GitHubToolListPullRequests, "List pull requests", "List pull requests for an available repository.", ToolTestModeSampleCall)
	b.connectedRead("github", "GitHub", GitHubToolGetPullRequest, "Read a pull request", "Read a pull request by number.", ToolTestModeSampleCall)
	b.connectedRead("github", "GitHub", GitHubToolListIssues, "List issues", "List issues for an available repository.", ToolTestModeSampleCall)
	b.connectedRead("github", "GitHub", GitHubToolGetIssue, "Read an issue", "Read a GitHub issue by number.", ToolTestModeSampleCall)
	b.connectedRead("github", "GitHub", GitHubToolListWorkflowRuns, "List workflow runs", "List workflow runs for an available repository.", ToolTestModeSampleCall)
	b.connectedRead("github", "GitHub", GitHubToolGetWorkflowRunJobs, "Read workflow run jobs", "Read jobs for a GitHub workflow run.", ToolTestModeSampleCall)
	b.connectedExecute("github", "GitHub", GitHubToolCloneRepository, "Clone a repository", "Clone a GitHub repository into app-managed local source storage.", ToolTestModeDryRun)
	b.connectedExecute("github", "GitHub", GitHubToolPullRepository, "Pull repository changes", "Pull updates for an app-managed or project GitHub repository clone.", ToolTestModeDryRun)
	b.connectedExecute("github", "GitHub", GitHubToolRefreshRepository, "Refresh a cloned repository", "Refresh one or all app-managed or project GitHub repository clones.", ToolTestModeDryRun)
	b.connectedExecute("github", "GitHub", GitHubToolRunGHCommand, "Run a gh command", "Run an approved GitHub CLI command.", ToolTestModeDryRun)

	b.connectedRead("slack", "Slack", SlackToolStatus, "Check connection status", "Return local Slack OAuth integration status.", ToolTestModeConnectionCheck)
	b.connectedRead("slack", "Slack", SlackToolCheckConnection, "Verify the Slack sign-in", "Check the stored Slack OAuth connection.", ToolTestModeConnectionCheck)
	b.connectedRead("slack", "Slack", SlackToolListConversations, "List channels", "List visible Slack conversations.", ToolTestModeSampleCall)
	b.connectedRead("slack", "Slack", SlackToolGetConversation, "Read channel details", "Read Slack conversation metadata.", ToolTestModeSampleCall)
	b.connectedRead("slack", "Slack", SlackToolGetConversationMembers, "List channel members", "List members of a Slack conversation.", ToolTestModeSampleCall)
	b.connectedRead("slack", "Slack", SlackToolGetMessages, "Read channel messages", "Read Slack conversation messages.", ToolTestModeSampleCall)
	b.connectedRead("slack", "Slack", SlackToolGetThreadReplies, "Read thread replies", "Read replies in a Slack thread.", ToolTestModeSampleCall)
	b.connectedRead("slack", "Slack", SlackToolGetUser, "Look up a user", "Look up Slack user metadata.", ToolTestModeSampleCall)
	b.connectedRead("slack", "Slack", SlackToolSearchMessages, "Search messages", "Search Slack messages.", ToolTestModeSampleCall)
	b.connectedRead("slack", "Slack", SlackToolDownloadFile, "Download a shared file", "Download a Slack file into the app-managed cache.", ToolTestModeSampleCall)
	b.connectedWrite("slack", "Slack", SlackToolPostMessage, "Send a message", "Send an approved Slack message.", ToolTestModeDryRun)
	b.connectedWrite("slack", "Slack", SlackToolUploadFile, "Upload a file", "Upload an approved file to Slack.", ToolTestModeDryRun)

	b.connectedRead("atlassian", "Atlassian", AtlassianSharedToolAtlassianStatus, "Check connection status", "Return shared Jira and Confluence browser state.", ToolTestModeConnectionCheck)
	b.connectedRead("atlassian", "Atlassian", AtlassianSharedToolAtlassianCheckConnection, "Verify the Atlassian sign-in", "Check Jira and Confluence browser-authenticated connection state.", ToolTestModeConnectionCheck)
	b.connectedRead("atlassian", "Atlassian", "jira_get_issue", "Read a Jira issue", "Read a Jira issue.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_get_issue_history", "Read issue history", "Read Jira changelog and sprint history.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_get_issue_comments", "Read issue comments", "Read Jira comments.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_get_issue_attachments", "List issue attachments", "Read Jira issue attachment metadata.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_read_attachment", "Read an issue attachment", "Read or promote a Jira issue attachment.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_get_issue_link_types", "List issue link types", "Read available Jira issue link types.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_find_users", "Find Jira users", "Find Jira users by display name or email.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_search_issues", "Search Jira issues", "Search Jira issues with JQL.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_find", "Find Jira issues", "Find Jira issues, projects, or boards.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "jira_rest_get", "Read from the Jira API", "Read an allowed Jira REST path.", ToolTestModeSampleCall)
	b.connectedWrite("atlassian", "Atlassian", "jira_add_comment", "Post a Jira comment", "Post an approved Jira comment.", ToolTestModeDryRun)
	b.connectedWrite("atlassian", "Atlassian", "jira_reply_to_comment", "Reply to a Jira comment", "Post an approved Jira reply.", ToolTestModeDryRun)
	b.connectedWrite("atlassian", "Atlassian", "jira_add_related_tickets", "Link Jira issues", "Add approved related issue links in Jira.", ToolTestModeDryRun)
	b.connectedWrite("atlassian", "Atlassian", "jira_upload_attachment", "Upload a file to Jira", "Upload an approved attachment to Jira.", ToolTestModeDryRun)
	b.connectedWrite("atlassian", "Atlassian", "jira_update_issue", "Update a Jira issue", "Apply an approved Jira issue update.", ToolTestModeDryRun)
	b.connectedWrite("atlassian", "Atlassian", "jira_create_issue", "Create a Jira issue", "Create an approved Jira issue.", ToolTestModeDryRun)
	b.connectedRead("atlassian", "Atlassian", "confluence_search", "Search Confluence", "Search Confluence pages.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "confluence_open_page", "Read a Confluence page", "Read a Confluence page.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "confluence_open_space", "Browse a Confluence space", "Read Confluence space metadata.", ToolTestModeSampleCall)
	b.connectedRead("atlassian", "Atlassian", "confluence_rest_get", "Read from the Confluence API", "Read an allowed Confluence REST path.", ToolTestModeSampleCall)

	b.connectedRead("sharepoint", "SharePoint", SharePointToolStatus, "Check connection status", "Return SharePoint browser-authenticated connection state.", ToolTestModeConnectionCheck)
	b.connectedRead("sharepoint", "SharePoint", SharePointToolCheckConnection, "Verify the SharePoint sign-in", "Check the configured SharePoint site.", ToolTestModeConnectionCheck)
	b.connectedRead("sharepoint", "SharePoint", SharePointToolSearch, "Search files", "Search accessible SharePoint and OneDrive documents.", ToolTestModeSampleCall)
	b.connectedRead("sharepoint", "SharePoint", SharePointToolGetMetadata, "Read file details", "Read metadata for a SharePoint file.", ToolTestModeSampleCall)
	b.connectedRead("sharepoint", "SharePoint", SharePointToolRead, "Read a document", "Download and promote a SharePoint file for native parsing.", ToolTestModeSampleCall)
	b.connectedRead("sharepoint", "SharePoint", SharePointToolSummarize, "Summarize a document", "Prepare a SharePoint file for summarization.", ToolTestModeSampleCall)
	b.connectedRead("sharepoint", "SharePoint", SharePointToolDownload, "Download a document", "Download a SharePoint file into the app-managed cache.", ToolTestModeSampleCall)

	b.connectedRead("browser", "In-app browser", BrowserControlToolStatus, "Check browser status", "Return the current in-app browser state.", ToolTestModeConnectionCheck)
	b.connectedExecute("browser", "In-app browser", BrowserControlToolNavigate, "Open a web page", "Navigate the in-app browser.", ToolTestModeDryRun)
	b.connectedRead("browser", "In-app browser", BrowserControlToolSnapshot, "Read the current page", "Inspect visible page text and interactive elements.", ToolTestModeSampleCall)
	b.connectedExecute("browser", "In-app browser", BrowserControlToolClick, "Click on the page", "Click an element in the in-app browser.", ToolTestModeDryRun)
	b.connectedExecute("browser", "In-app browser", BrowserControlToolType, "Type into the page", "Type into an element in the in-app browser.", ToolTestModeDryRun)
	b.connectedExecute("browser", "In-app browser", BrowserControlToolPress, "Press a key", "Press a key in the in-app browser.", ToolTestModeDryRun)
	b.connectedExecute("browser", "In-app browser", BrowserControlToolWait, "Wait for the page", "Wait for the in-app browser.", ToolTestModeDryRun)
	b.connectedExecute("browser", "In-app browser", BrowserControlToolBack, "Go back a page", "Navigate back in the in-app browser.", ToolTestModeDryRun)
	b.connectedRead("browser", "In-app browser", BrowserControlToolLogs, "Read console logs", "Read recent console, page error, and network logs.", ToolTestModeSampleCall)
	b.connectedExecute("browser", "In-app browser", BrowserControlToolClearLogs, "Clear console logs", "Clear captured in-app browser logs.", ToolTestModeDryRun)

	b.connectedRead("arxiv", "arXiv", ArxivToolStatus, "Check connection status", "Return local arXiv integration status.", ToolTestModeConnectionCheck)
	b.connectedRead("arxiv", "arXiv", ArxivToolCheck, "Verify the arXiv connection", "Check the public arXiv export API.", ToolTestModeConnectionCheck)
	b.connectedRead("arxiv", "arXiv", ArxivToolSearch, "Search papers", "Search arXiv papers.", ToolTestModeSampleCall)
	b.connectedRead("arxiv", "arXiv", ArxivToolScreeningReport, "Build a screening report", "Build a screening report from arXiv search results.", ToolTestModeSampleCall)
	b.connectedRead("arxiv", "arXiv", ArxivToolRecommendations, "Get paper recommendations", "Recommend arXiv papers from a query.", ToolTestModeSampleCall)
	b.connectedRead("arxiv", "arXiv", ArxivToolDownloadPaper, "Download a paper", "Download an arXiv paper PDF into the app-managed cache.", ToolTestModeSampleCall)
	b.connectedRead("arxiv", "arXiv", ArxivToolListPapers, "List downloaded papers", "List papers stored in the local arXiv library.", ToolTestModeSampleCall)
	b.connectedRead("arxiv", "arXiv", ArxivToolGetPaper, "Read paper details", "Read metadata for a stored arXiv paper.", ToolTestModeSampleCall)
	b.connectedRead("arxiv", "arXiv", ArxivToolGetPaperText, "Read paper text", "Read extracted text for a stored arXiv paper.", ToolTestModeSampleCall)
	b.connectedRead("arxiv", "arXiv", ArxivToolReadPaper, "Read paper pages", "Read bounded pages from a stored arXiv paper.", ToolTestModeSampleCall)

	return b.manifest
}

type toolMetadataBuilder struct {
	manifest map[string]ToolMetadata
}

func newToolMetadataBuilder() *toolMetadataBuilder {
	return &toolMetadataBuilder{manifest: map[string]ToolMetadata{}}
}

func (b *toolMetadataBuilder) read(name, label, description string, origin ToolOrigin, setID, setName, connectorID string, testMode ToolTestMode) {
	b.add(name, label, description, ToolCategoryRead, origin, setID, setName, connectorID, testMode)
}

func (b *toolMetadataBuilder) write(name, label, description string, origin ToolOrigin, setID, setName, connectorID string, testMode ToolTestMode) {
	b.add(name, label, description, ToolCategoryWrite, origin, setID, setName, connectorID, testMode)
}

func (b *toolMetadataBuilder) execute(name, label, description string, origin ToolOrigin, setID, setName, connectorID string, testMode ToolTestMode) {
	b.add(name, label, description, ToolCategoryExecute, origin, setID, setName, connectorID, testMode)
}

func (b *toolMetadataBuilder) connectedRead(connectorID, setName, name, label, description string, testMode ToolTestMode) {
	b.read(name, label, description, ToolOriginConnected, connectorID, setName, connectorID, testMode)
}

func (b *toolMetadataBuilder) connectedWrite(connectorID, setName, name, label, description string, testMode ToolTestMode) {
	b.write(name, label, description, ToolOriginConnected, connectorID, setName, connectorID, testMode)
}

func (b *toolMetadataBuilder) connectedExecute(connectorID, setName, name, label, description string, testMode ToolTestMode) {
	b.execute(name, label, description, ToolOriginConnected, connectorID, setName, connectorID, testMode)
}

func (b *toolMetadataBuilder) datasource(connectorID, setName, name, label, description string) {
	b.connectedRead(connectorID, setName, name, label, description, ToolTestModeSampleCall)
}

func (b *toolMetadataBuilder) add(name, label, description string, category ToolPermissionCategory, origin ToolOrigin, setID, setName, connectorID string, testMode ToolTestMode) {
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	if testMode == "" {
		testMode = ToolTestModeNone
	}
	if _, exists := b.manifest[name]; exists {
		panic("tools: duplicate tool metadata for " + name)
	}
	supported := testMode != ToolTestModeNone
	requiresApproval := supported && (category == ToolCategoryWrite || category == ToolCategoryExecute)
	summary := ""
	if supported {
		summary = "Runs a bounded test through the normal tool execution and permission path."
	}
	testReq := toolTestRequirementFor(name, origin, setID, connectorID, testMode)
	b.manifest[name] = ToolMetadata{
		Name:         name,
		Label:        strings.TrimSpace(label),
		Description:  strings.TrimSpace(description),
		Category:     category,
		Origin:       origin,
		ToolSetID:    strings.TrimSpace(setID),
		ToolSetName:  strings.TrimSpace(setName),
		ConnectorID:  strings.TrimSpace(connectorID),
		Presentation: toolPresentationFor(name, category, origin, setID, connectorID),
		Test: ToolTestSpec{
			Supported:              supported,
			Mode:                   testMode,
			RequiresApproval:       requiresApproval,
			InputRequired:          len(testReq.RequiredArguments) > 0 || len(testReq.RequiredArgumentGroups) > 0,
			RequiredArguments:      append([]string(nil), testReq.RequiredArguments...),
			RequiredArgumentGroups: cloneStringMatrix(testReq.RequiredArgumentGroups),
			RequiredContext:        append([]string(nil), testReq.RequiredContext...),
			SampleArguments:        toolTestSampleArguments(name, testReq.RequiredArguments, testReq.RequiredArgumentGroups),
			InputHint:              testReq.InputHint,
			Summary:                summary,
		},
	}
}

type toolTestRequirement struct {
	RequiredArguments      []string
	RequiredArgumentGroups [][]string
	RequiredContext        []string
	InputHint              string
}

func toolPresentationFor(name string, category ToolPermissionCategory, origin ToolOrigin, setID string, connectorID string) ToolPresentationSpec {
	name = strings.TrimSpace(name)
	setID = strings.TrimSpace(setID)
	connectorID = strings.TrimSpace(connectorID)
	family := setID
	if family == "" {
		family = string(origin)
	}
	resultKind := "generic"
	nativeViewer := "connector_inspector"
	fileCapability := ""

	switch setID {
	case "local-files":
		resultKind = "repository"
		nativeViewer = "connector_inspector"
		fileCapability = "local_repository"
		switch name {
		case "read_file", "local_read_file", "local_read_pdf_page", "local_read_grant_file", "git_show_file_at_ref":
			resultKind = "file"
			nativeViewer = "code"
			fileCapability = "local_file_read"
		case "local_open_file":
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "local_file_read"
		case "local_list_folder", "local_list_grant_folder":
			resultKind = "folder"
			nativeViewer = "connector_inspector"
			fileCapability = "local_folder_list"
		case "search_files", "file_search", LocalSearchCommandToolName, "local_search_in_grant", "git_grep":
			resultKind = "search"
			nativeViewer = "connector_inspector"
			fileCapability = "local_file_search"
		}
	case "attachments":
		resultKind = "metadata"
		nativeViewer = "connector_inspector"
		fileCapability = "attachment_inspect"
		switch name {
		case "read_attachment":
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "attachment_read"
		case "search_attachment":
			resultKind = "search"
			fileCapability = "attachment_search"
		}
	case "artifacts":
		resultKind = "artifact"
		nativeViewer = "connector_inspector"
		fileCapability = "artifact_read"
		switch name {
		case "build_chart_artifact":
			resultKind = "chart"
			nativeViewer = "chart"
			fileCapability = "artifact_create"
		case "display_table":
			resultKind = "data"
			nativeViewer = "data"
			fileCapability = "artifact_create"
		case "create_interactive_artifact", "create_interactive_data_artifact":
			resultKind = "interactive"
			nativeViewer = "interactive_html"
			fileCapability = "artifact_create"
		case "export_thread":
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "artifact_create"
		case "get_echarts_capabilities", "get_highcharts_capabilities":
			resultKind = "status"
			nativeViewer = "connector_inspector"
			fileCapability = ""
		}
	case "image-generation":
		resultKind = "image"
		nativeViewer = "image"
		fileCapability = "artifact_create"
	case "data-analysis":
		resultKind = "data"
		nativeViewer = "data"
		fileCapability = "dataset_artifact_read"
		switch name {
		case "describe_dataset", "sample_rows", "filter_csv", "aggregate_csv":
			fileCapability = "local_file_read"
		}
	case "query-lifecycle":
		resultKind = "query"
		nativeViewer = "query"
		fileCapability = ""
	case "thread-search":
		resultKind = "search"
		nativeViewer = "connector_inspector"
		if name == "get_artifact" {
			resultKind = "artifact"
			fileCapability = "artifact_read"
		}
		if name == "rlm_exec" {
			resultKind = "code"
			nativeViewer = "code"
		}
	case "bigquery", "databricks", "trino", "snowflake":
		resultKind = "query"
		nativeViewer = "query"
		if !presentationIsQueryRunTool(name) {
			resultKind = presentationDatasourceCatalogKind(name)
			nativeViewer = "connector_inspector"
		}
	case "github":
		resultKind = "connector"
		nativeViewer = "connector_inspector"
		switch name {
		case GitHubToolCloneRepository:
			resultKind = "repository"
			fileCapability = "repository_clone"
		case GitHubToolPullRepository, GitHubToolRefreshRepository:
			resultKind = "repository"
			fileCapability = "local_repository"
		case GitHubToolRunGHCommand:
			resultKind = "command"
			nativeViewer = "code"
		}
	case "slack":
		resultKind = "connector"
		nativeViewer = "connector_inspector"
		switch name {
		case SlackToolDownloadFile:
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "remote_file_download"
		case SlackToolUploadFile:
			resultKind = "file"
			nativeViewer = "connector_inspector"
			fileCapability = "remote_file_upload"
		}
	case "atlassian":
		resultKind = "connector"
		nativeViewer = "connector_inspector"
		if strings.HasPrefix(name, "jira_") {
			family = "jira"
		}
		if strings.HasPrefix(name, "confluence_") {
			family = "confluence"
		}
		switch name {
		case "jira_read_attachment":
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "remote_file_download"
		case "jira_upload_attachment":
			resultKind = "file"
			fileCapability = "remote_file_upload"
		}
	case "sharepoint":
		resultKind = "connector"
		nativeViewer = "connector_inspector"
		switch name {
		case SharePointToolRead, SharePointToolSummarize, SharePointToolDownload:
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "remote_file_download"
		}
	case "browser":
		resultKind = "browser"
		nativeViewer = "connector_inspector"
		if category == ToolCategoryExecute {
			fileCapability = "browser_control"
		}
	case "arxiv":
		resultKind = "paper"
		nativeViewer = "connector_inspector"
		switch name {
		case ArxivToolDownloadPaper:
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "remote_file_download"
		case ArxivToolGetPaperText, ArxivToolReadPaper:
			resultKind = "file"
			nativeViewer = "code"
			fileCapability = "remote_file_read"
		case ArxivToolSearch, ArxivToolScreeningReport, ArxivToolRecommendations, ArxivToolListPapers:
			resultKind = "data"
			nativeViewer = "connector_inspector"
		}
	default:
		switch origin {
		case ToolOriginBuiltin:
			nativeViewer = "connector_inspector"
		case ToolOriginLocal, ToolOriginAttached:
			resultKind = "file"
			nativeViewer = "file"
			fileCapability = "local_file_read"
		}
	}

	rendererStrategy, rendererKey := presentationRenderer(name, family, resultKind, nativeViewer)
	return ToolPresentationSpec{
		Family:               family,
		ResultKind:           resultKind,
		RendererStrategy:     rendererStrategy,
		RendererKey:          rendererKey,
		NativeViewer:         nativeViewer,
		ArtifactTypes:        presentationArtifactTypes(resultKind, nativeViewer),
		FileCapability:       fileCapability,
		FileKinds:            presentationFileKinds(fileCapability, nativeViewer),
		PreviewModes:         presentationPreviewModes(fileCapability, nativeViewer),
		SourceRefs:           presentationSourceRefs(name, setID, resultKind, fileCapability),
		ProducesFileArtifact: presentationProducesFileArtifact(name, fileCapability),
		PromotesNativeInput:  presentationPromotesNativeInput(fileCapability),
		NativeLinks:          presentationNativeLinks(name, family, setID, resultKind),
	}
}

func presentationRenderer(name, family, resultKind, nativeViewer string) (string, string) {
	if presentationUsesToolRenderer(name) {
		return "tool", strings.TrimSpace(name)
	}
	if nativeViewer != "" && nativeViewer != "connector_inspector" {
		return "artifact_type", nativeViewer
	}
	if strings.TrimSpace(family) != "" && resultKind != "generic" {
		return "family", family
	}
	return "generic", "connector_inspector"
}

func presentationArtifactTypes(resultKind, nativeViewer string) []string {
	switch nativeViewer {
	case "image":
		return []string{"image", "file"}
	case "file":
		return []string{"file"}
	case "code", "interactive_html":
		return []string{"code"}
	case "chart", "data", "query":
		return []string{"data"}
	}
	switch resultKind {
	case "artifact":
		return []string{"data", "code", "image", "file"}
	case "catalog", "table_schema":
		return []string{"data"}
	case "file":
		return []string{"file"}
	}
	return nil
}

func presentationFileKinds(fileCapability, nativeViewer string) []string {
	if nativeViewer != "file" && !presentationHasFileSource(fileCapability) {
		return nil
	}
	return []string{"pdf", "image", "text", "html", "document", "spreadsheet", "presentation", "archive"}
}

func presentationPreviewModes(fileCapability, nativeViewer string) []string {
	if nativeViewer != "file" && !presentationHasFileSource(fileCapability) {
		return nil
	}
	return []string{"raw_download", "text_preview", "pdf_canvas", "image_raw", "html_sandbox", "spreadsheet_workbook", "presentation_slides", "download_only"}
}

func presentationHasFileSource(fileCapability string) bool {
	switch fileCapability {
	case "local_file_read", "local_file_search", "local_folder_list", "attachment_read", "dataset_artifact_read", "remote_file_download", "remote_file_read", "remote_file_upload", "artifact_read":
		return true
	default:
		return false
	}
}

func presentationSourceRefs(name, setID, resultKind, fileCapability string) []string {
	switch name {
	case "display_table", "build_chart_artifact":
		return []string{"source.artifact_id", "source.path", "source.job_id", "owner_token"}
	case "create_interactive_data_artifact":
		return []string{"source.artifact_id"}
	case "local_open_file":
		return []string{"artifact_id", "path", "local_path"}
	}
	if refs := presentationDatasourceCatalogSourceRefs(name); len(refs) > 0 {
		return refs
	}
	switch name {
	case SlackToolDownloadFile:
		return []string{"file_id", "channel_id", "remote_id", "artifact_id"}
	case "jira_read_attachment":
		return []string{"issueKey", "attachmentId", "remote_id", "artifact_id"}
	case SharePointToolRead, SharePointToolSummarize, SharePointToolDownload:
		return []string{"item_url", "server_relative_url", "site_url", "remote_id", "artifact_id"}
	case ArxivToolDownloadPaper:
		return []string{"arxiv_id", "remote_id", "artifact_id"}
	case ArxivToolGetPaperText, ArxivToolReadPaper:
		return []string{"paper", "arxiv_id", "remote_id", "artifact_id"}
	}
	switch fileCapability {
	case "remote_file_upload":
		return []string{"artifact_id", "path", "local_path", "fileName", "contentText", "contentBase64"}
	case "local_file_read", "local_file_search", "local_folder_list", "local_repository":
		return []string{"path", "local_path"}
	case "repository_clone":
		return []string{"remote_id", "local_path"}
	case "attachment_read", "attachment_inspect", "attachment_search", "artifact_read", "dataset_artifact_read":
		return []string{"artifact_id"}
	case "remote_file_download", "remote_file_read":
		return []string{"remote_id", "artifact_id"}
	}
	if resultKind == "query" && (setID == "query-lifecycle" || strings.HasPrefix(name, "run_")) {
		return []string{"remote_id"}
	}
	return nil
}

func presentationIsQueryRunTool(name string) bool {
	switch strings.TrimSpace(name) {
	case "run_bigquery_query", "run_databricks_query", "run_trino_query", "run_snowflake_query":
		return true
	default:
		return false
	}
}

func presentationUsesToolRenderer(name string) bool {
	switch strings.TrimSpace(name) {
	case "list_bq_datasets", "list_bq_tables", "get_bq_table_schema",
		"list_databricks_catalogs", "list_databricks_schemas", "list_databricks_tables", "describe_databricks_table",
		"list_trino_catalogs", "list_trino_schemas", "list_trino_tables", "describe_trino_table",
		"list_snowflake_databases", "list_snowflake_schemas", "list_snowflake_tables", "describe_snowflake_table":
		return true
	default:
		return false
	}
}

func presentationDatasourceCatalogKind(name string) string {
	switch strings.TrimSpace(name) {
	case "get_bq_table_schema", "describe_databricks_table", "describe_trino_table", "describe_snowflake_table":
		return "table_schema"
	default:
		return "catalog"
	}
}

func presentationDatasourceCatalogSourceRefs(name string) []string {
	switch strings.TrimSpace(name) {
	case "list_bq_datasets":
		return []string{"project_id"}
	case "list_bq_tables":
		return []string{"project_id", "dataset_id"}
	case "get_bq_table_schema":
		return []string{"project_id", "dataset_id", "table_id"}
	case "list_databricks_catalogs", "list_trino_catalogs", "list_snowflake_databases":
		return []string{"connection"}
	case "list_databricks_schemas", "list_trino_schemas":
		return []string{"catalog_name"}
	case "list_databricks_tables", "list_trino_tables":
		return []string{"catalog_name", "schema_name"}
	case "describe_databricks_table", "describe_trino_table", "describe_snowflake_table":
		return []string{"full_name"}
	case "list_snowflake_schemas":
		return []string{"database_name"}
	case "list_snowflake_tables":
		return []string{"database_name", "schema_name"}
	default:
		return nil
	}
}

func presentationProducesFileArtifact(name, fileCapability string) bool {
	if name == "export_thread" || name == "local_open_file" || name == generateImageToolName {
		return true
	}
	return fileCapability == "remote_file_download"
}

func presentationPromotesNativeInput(fileCapability string) bool {
	switch fileCapability {
	case "local_file_read", "attachment_read", "dataset_artifact_read", "remote_file_download", "remote_file_read":
		return true
	default:
		return false
	}
}

func presentationNativeLinks(name, family, setID, resultKind string) []string {
	links := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range links {
			if existing == value {
				return
			}
		}
		links = append(links, value)
	}
	if resultKind == "query" && (setID == "query-lifecycle" || strings.HasPrefix(name, "run_")) {
		add("warehouse_job")
	}
	if setID == "browser" {
		add("browser_url")
	}
	switch family {
	case "github", "jira", "confluence", "sharepoint", "slack", "arxiv":
		add("source_url")
	}
	if len(links) == 0 {
		return nil
	}
	return links
}

func toolTestRequirementFor(name string, origin ToolOrigin, setID string, connectorID string, mode ToolTestMode) toolTestRequirement {
	if mode == ToolTestModeNone {
		return toolTestRequirement{}
	}
	name = strings.TrimSpace(name)
	setID = strings.TrimSpace(setID)
	connectorID = strings.TrimSpace(connectorID)

	req := toolTestRequirement{
		RequiredArguments:      requiredToolTestArguments(name),
		RequiredArgumentGroups: requiredToolTestArgumentGroups(name),
		RequiredContext:        requiredToolTestContext(name, origin, setID, connectorID, mode),
	}
	if len(req.RequiredArguments) > 0 || len(req.RequiredArgumentGroups) > 0 || len(req.RequiredContext) > 0 {
		req.InputHint = toolTestInputHint(name, req.RequiredArguments, req.RequiredArgumentGroups, req.RequiredContext)
	}
	return req
}

func requiredToolTestArguments(name string) []string {
	required := map[string][]string{
		"read_file":                        {"path"},
		"search_files":                     {"query"},
		"file_search":                      {"query"},
		"local_read_file":                  {"path"},
		"local_open_file":                  {"path"},
		"local_read_pdf_page":              {"path", "page_number"},
		"local_list_grant_folder":          {"grant_id"},
		LocalSearchCommandToolName:         {"grant_id", "program", "query"},
		"local_search_in_grant":            {"grant_id", "query"},
		"local_read_grant_file":            {"grant_id", "file_ref"},
		"git_search_history":               {"query"},
		"git_grep":                         {"query"},
		"git_show_file_at_ref":             {"path"},
		"inspect_attachment":               {"artifact_id"},
		"read_attachment":                  {"artifact_id"},
		"search_attachment":                {"artifact_id", "query"},
		"get_artifact_chunk":               {"artifact_id"},
		"search_artifact_content":          {"artifact_id", "query"},
		"get_json_artifact":                {"artifact_id"},
		generateImageToolName:              {"prompt"},
		"build_chart_artifact":             {"source", "chart"},
		"create_interactive_artifact":      {"title", "html"},
		"create_interactive_data_artifact": {"title", "source", "html"},
		"describe_dataset":                 {"path"},
		"sample_rows":                      {"path"},
		"filter_csv":                       {"path", "column", "operator"},
		"aggregate_csv":                    {"path", "metrics"},
		"inspect_dataset_artifact":         {"artifact_id"},
		"get_dataset_rows":                 {"artifact_id"},
		"aggregate_dataset":                {"artifact_id"},
		"search_dataset_rows":              {"artifact_id", "query"},
		"search_threads":                   {"query"},
		"search_docs":                      {"q"},
		"get_doc":                          {"doc_id"},
		"get_artifact":                     {"artifact_id"},
		"list_messages":                    {"thread_id"},
		"rlm_exec":                         {"code"},
		"run_bigquery_query":               {"sql"},
		"list_bq_tables":                   {"dataset_id"},
		"get_bq_table_schema":              {"dataset_id", "table_id"},
		"run_databricks_query":             {"sql"},
		"list_databricks_schemas":          {"catalog_name"},
		"list_databricks_tables":           {"catalog_name", "schema_name"},
		"describe_databricks_table":        {"full_name"},
		"run_trino_query":                  {"sql"},
		"list_trino_schemas":               {"catalog_name"},
		"list_trino_tables":                {"catalog_name", "schema_name"},
		"describe_trino_table":             {"full_name"},
		"run_snowflake_query":              {"sql"},
		"list_snowflake_schemas":           {"database_name"},
		"list_snowflake_tables":            {"database_name", "schema_name"},
		"describe_snowflake_table":         {"full_name"},
		BrowserControlToolNavigate:         {"url"},
		BrowserControlToolType:             {"value"},
		BrowserControlToolPress:            {"key"},
		GitHubToolGetPullRequest:           {"number"},
		GitHubToolGetIssue:                 {"number"},
		GitHubToolGetWorkflowRunJobs:       {"run_id"},
		GitHubToolCloneRepository:          {"repository"},
		GitHubToolRunGHCommand:             {"args"},
		SlackToolGetConversation:           {"channel_id"},
		SlackToolGetConversationMembers:    {"channel_id"},
		SlackToolGetMessages:               {"channel_id"},
		SlackToolGetThreadReplies:          {"channel_id", "thread_ts"},
		SlackToolGetUser:                   {"user_id"},
		SlackToolSearchMessages:            {"query"},
		SlackToolDownloadFile:              {"file_id"},
		SlackToolPostMessage:               {"channel_id", "text"},
		SlackToolUploadFile:                {"channel_id"},
		"jira_get_issue":                   {"issueKey"},
		"jira_get_issue_history":           {"issueKey"},
		"jira_get_issue_comments":          {"issueKey"},
		"jira_get_issue_attachments":       {"issueKey"},
		"jira_read_attachment":             {"issueKey", "attachmentId"},
		"jira_find_users":                  {"query"},
		"jira_search_issues":               {"jql"},
		"jira_rest_get":                    {"path"},
		"jira_add_comment":                 {"issueKey", "comment"},
		"jira_reply_to_comment":            {"issueKey", "commentId", "comment"},
		"jira_add_related_tickets":         {"issueKey", "relatedIssueKeys"},
		"jira_upload_attachment":           {"issueKey"},
		"jira_update_issue":                {"issueKey"},
		"jira_create_issue":                {"projectKey", "summary"},
		"confluence_open_space":            {"spaceKey"},
		"confluence_search":                {"query"},
		"confluence_rest_get":              {"path"},
		SharePointToolSearch:               {"query"},
		ArxivToolDownloadPaper:             {"arxiv_id"},
		ArxivToolGetPaper:                  {"paper"},
		ArxivToolGetPaperText:              {"paper"},
		ArxivToolReadPaper:                 {"paper"},
	}
	return append([]string(nil), required[name]...)
}

func toolTestSampleArguments(toolName string, requiredArguments []string, requiredArgumentGroups [][]string) map[string]any {
	samples := map[string]any{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := samples[name]; exists {
			return
		}
		samples[name] = toolTestSampleArgumentValue(toolName, name)
	}
	for _, name := range requiredArguments {
		add(name)
	}
	for _, group := range requiredArgumentGroups {
		for _, name := range group {
			add(name)
		}
	}
	if len(samples) == 0 {
		return nil
	}
	return samples
}

func toolTestSampleArgumentValue(toolName string, name string) any {
	toolName = strings.TrimSpace(toolName)
	switch strings.TrimSpace(name) {
	case "args":
		return []any{"api", "user"}
	case "artifact_id":
		return "artifact-123"
	case "arxiv_id":
		return "2401.00001"
	case "assigneeAccountId":
		return "712020:example-account"
	case "attachmentId":
		return "10001"
	case "authors":
		return []any{"Smith"}
	case "catalog_name":
		return "main"
	case "categories":
		return []any{"cs.AI"}
	case "channel_id":
		return "C1234567890"
	case "code":
		return `result = search_threads(query="release notes", limit=3)`
	case "column":
		return "value"
	case "comment":
		return "Thanks for the update."
	case "commentId":
		return "10002"
	case "components":
		return []any{"Platform"}
	case "database_name":
		return "analytics"
	case "dataset_id":
		return "sample_dataset"
	case "description":
		return "Updated description"
	case "doc_id":
		return "doc-123"
	case "file_id":
		return "F1234567890"
	case "file_ref":
		return "reports/summary.txt"
	case "full_name":
		return "analytics.public.events"
	case "grant_id":
		return "perm_local_grant"
	case "ids":
		return []any{"2401.00001"}
	case "index":
		return 1
	case "issueType":
		return "Task"
	case "item_url":
		return "https://contoso.sharepoint.com/sites/team/Shared%20Documents/report.docx"
	case "jql":
		return "project = TEAM ORDER BY updated DESC"
	case "key":
		return "TEAM"
	case "labels":
		return []any{"triage"}
	case "local_path":
		return "downloads/report.txt"
	case "metrics":
		return []any{map[string]any{"column": "value", "function": "count"}}
	case "number":
		return 123
	case "page_number":
		return 1
	case "program":
		if toolName == LocalSearchCommandToolName {
			return "auto"
		}
		return "test-program"
	case "operator":
		return "eq"
	case "pageId":
		return "123456"
	case "paper":
		return "2401.00001"
	case "prompt":
		if toolName == generateImageToolName {
			return "A small blue circle on a clean white background"
		}
		return "Example prompt"
	case "priorityName":
		return "Medium"
	case "q":
		return "release notes"
	case "query":
		return "release notes"
	case "repository":
		return "owner/repo"
	case "relatedIssueKeys":
		return []any{"TEAM-124"}
	case "run_id":
		return "123456789"
	case "schema_name":
		return "public"
	case "selector":
		return "button.primary"
	case "server_relative_url":
		return "/sites/team/Shared Documents/report.docx"
	case "spaceKey":
		return "TEAM"
	case "summary":
		return "Example issue summary"
	case "table_id":
		return "events"
	case "text":
		return "Submit"
	case "thread_id":
		return "thread-123"
	case "thread_ts":
		return "1780000000.000001"
	case "title":
		return "Project plan"
	case "user_id":
		return "U1234567890"
	case "value":
		return "example"
	case "chart":
		return map[string]any{"kind": "bar", "x": "name", "y": "value"}
	case "source":
		return map[string]any{"artifact_id": "artifact-123"}
	case "fields":
		return map[string]any{"customfield_10000": "value"}
	case "clearDescription", "recursive":
		return true
	case "contentBase64":
		return "VGVzdCBmaWxlIGNvbnRlbnQ="
	case "contentText":
		return "Test file content"
	case "data_json":
		return `[{"name":"A","value":10}]`
	case "fileName":
		return "test.txt"
	case "html":
		return "<div>Test artifact</div>"
	case "issueKey":
		return "TEAM-123"
	case "local_root":
		return "/path/to/approved/folder"
	case "path":
		if toolName == "local_read_pdf_page" || toolName == "local_open_file" {
			return "docs/example.pdf"
		}
		if strings.Contains(toolName, "csv") || strings.Contains(toolName, "dataset") {
			return "data/sample.csv"
		}
		if strings.Contains(toolName, "git_") {
			return "README.md"
		}
		return "docs/example.txt"
	case "projectOrBoard":
		return "TEAM"
	case "projectKey":
		return "TEAM"
	case "project_id":
		return "example-project"
	case "sql":
		return "select 1 as value"
	case "url":
		return "https://example.com"
	default:
		return "test-" + strings.TrimSpace(name)
	}
}

func requiredToolTestArgumentGroups(name string) [][]string {
	groups := map[string][][]string{
		"jira_update_issue": {
			{"summary"},
			{"description"},
			{"clearDescription"},
			{"issueType"},
			{"assigneeAccountId"},
			{"priorityName"},
			{"labels"},
			{"components"},
			{"fields"},
		},
		SharePointToolGetMetadata: {
			{"item_url"},
			{"server_relative_url"},
		},
		SharePointToolRead: {
			{"item_url"},
			{"server_relative_url"},
		},
		SharePointToolSummarize: {
			{"item_url"},
			{"server_relative_url"},
		},
		SharePointToolDownload: {
			{"item_url"},
			{"server_relative_url"},
		},
		BrowserControlToolClick: {
			{"index"},
			{"selector"},
			{"text"},
		},
		BrowserControlToolType: {
			{"index"},
			{"selector"},
			{"text"},
		},
		SlackToolUploadFile: {
			{"fileName", "contentText"},
			{"fileName", "contentBase64"},
			{"artifact_id"},
			{"path"},
			{"local_path"},
		},
		"jira_upload_attachment": {
			{"fileName", "contentText"},
			{"fileName", "contentBase64"},
			{"artifact_id"},
			{"path"},
			{"local_path"},
		},
		"display_table": {
			{"data_json"},
			{"source"},
		},
		"jira_find": {
			{"query"},
			{"projectOrBoard"},
		},
		"confluence_open_page": {
			{"url"},
			{"pageId"},
			{"spaceKey", "title"},
		},
		ArxivToolSearch: {
			{"query"},
			{"categories"},
			{"authors"},
			{"ids"},
		},
	}
	return cloneStringMatrix(groups[name])
}

func requiredToolTestContext(name string, origin ToolOrigin, setID string, connectorID string, mode ToolTestMode) []string {
	context := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range context {
			if existing == value {
				return
			}
		}
		context = append(context, value)
	}
	if connectorID != "" {
		add("connection:" + connectorID)
	}
	if name == LocalSearchCommandToolName {
		add("connection:local-computer")
	}
	switch origin {
	case ToolOriginAttached:
		add("thread")
	case ToolOriginLocal:
		switch setID {
		case "local-files":
			if localFilesToolTestRequiresProject(name) {
				add("project")
			}
		case "data-analysis":
			if dataAnalysisToolTestRequiresProject(name) {
				add("project")
			}
		}
	}
	switch setID {
	case "attachments":
		add("thread")
	case "artifacts":
		if name == "export_thread" {
			add("thread")
		}
		if artifactToolTestRequiresArtifactContext(name) {
			add("artifact")
		}
	case "thread-search":
		add("search_index")
	case "github":
		if githubToolTestRequiresProject(name) {
			add("project")
		}
	case "browser":
		add("browser")
	}
	if mode == ToolTestModeConnectionCheck && len(context) == 0 && connectorID != "" {
		add("connection:" + connectorID)
	}
	return context
}

func localFilesToolTestRequiresProject(name string) bool {
	switch strings.TrimSpace(name) {
	case "list_files",
		"read_file",
		"search_files",
		"git_list_refs",
		"git_log",
		"git_search_history",
		"git_grep",
		"git_show_commit",
		"git_show_file_at_ref":
		return true
	default:
		return false
	}
}

func dataAnalysisToolTestRequiresProject(name string) bool {
	switch strings.TrimSpace(name) {
	case "describe_dataset", "sample_rows", "filter_csv", "aggregate_csv":
		return true
	default:
		return false
	}
}

func artifactToolTestRequiresArtifactContext(name string) bool {
	switch strings.TrimSpace(name) {
	case "get_artifact_chunk",
		"search_artifact_content",
		"get_json_artifact",
		"build_chart_artifact",
		"create_interactive_data_artifact":
		return true
	default:
		return false
	}
}

func githubToolTestRequiresProject(name string) bool {
	switch strings.TrimSpace(name) {
	case GitHubToolListPullRequests,
		GitHubToolGetPullRequest,
		GitHubToolListIssues,
		GitHubToolGetIssue,
		GitHubToolListWorkflowRuns,
		GitHubToolGetWorkflowRunJobs:
		return true
	default:
		return false
	}
}

func toolTestInputHint(name string, requiredArgs []string, requiredArgGroups [][]string, requiredContext []string) string {
	parts := []string{}
	if len(requiredContext) > 0 {
		parts = append(parts, "requires "+strings.Join(requiredContext, ", "))
	}
	if len(requiredArgs) > 0 {
		parts = append(parts, "arguments: "+strings.Join(requiredArgs, ", "))
	}
	if len(requiredArgGroups) > 0 {
		groupLabels := make([]string, 0, len(requiredArgGroups))
		for _, group := range requiredArgGroups {
			clean := cleanStringSlice(group)
			if len(clean) == 0 {
				continue
			}
			groupLabels = append(groupLabels, strings.Join(clean, " + "))
		}
		if len(groupLabels) > 0 {
			parts = append(parts, "one of: "+strings.Join(groupLabels, " OR "))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "Tool test for " + name + " " + strings.Join(parts, "; ") + "."
}

func cloneStringMatrix(in [][]string) [][]string {
	if len(in) == 0 {
		return nil
	}
	out := make([][]string, 0, len(in))
	for _, group := range in {
		clean := cleanStringSlice(group)
		if len(clean) == 0 {
			continue
		}
		out = append(out, clean)
	}
	return out
}

func cleanStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		value := strings.TrimSpace(raw)
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
