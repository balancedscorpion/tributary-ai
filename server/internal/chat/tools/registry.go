package tools

import (
	"strings"

	"ai-analyst-local/internal/arxiv"
	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/browsercontrol"
	"ai-analyst-local/internal/chat/rlm"
	"ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sharepoint"
	"ai-analyst-local/internal/sources"
)

// Registry holds the set of server-side tools available to the agent engine.
// It provides tool definitions for provider requests and lookup by canonical name.
type Registry struct {
	byName map[string]Tool
	defs   []types.ToolDefinition

	searchDefsShallow  []types.ToolDefinition
	searchDefsDeep     []types.ToolDefinition
	searchAllowShallow map[string]struct{}
	searchAllowDeep    map[string]struct{}
}

var (
	analysisCoreToolNames = []string{
		"list_files",
		"read_file",
		"search_files",
		"file_search",
		"local_list_folder",
		"local_read_file",
		LocalOpenFileToolName,
		"local_read_pdf_page",
		"list_git_repositories",
		"git_list_refs",
		"git_log",
		"git_search_history",
		"git_grep",
		"git_show_commit",
		"git_show_file_at_ref",
		"get_artifact_chunk",
		"search_artifact_content",
		"get_json_artifact",
		"inspect_attachment",
		"read_attachment",
		"search_attachment",
		"export_thread",
		"get_echarts_capabilities",
		"get_highcharts_capabilities",
		"build_chart_artifact",
		"create_interactive_artifact",
		"create_interactive_data_artifact",
		"display_table",
		"inspect_dataset_artifact",
		"get_dataset_rows",
		"aggregate_dataset",
		"search_dataset_rows",
	}
	localGrantToolNames = []string{
		"local_list_grant_folder",
		LocalSearchCommandToolName,
		"local_search_in_grant",
		"local_read_grant_file",
	}
	csvToolNames = []string{
		"describe_dataset",
		"sample_rows",
		"filter_csv",
		"aggregate_csv",
	}
	queryLifecycleToolNames = []string{
		"check_query_status",
		"get_query_results",
		"validate_query_result",
		"cancel_query",
	}
	bigQueryToolNames   = append(append([]string(nil), DatasourceToolNames("bigquery")...), queryLifecycleToolNames...)
	databricksToolNames = append(append([]string(nil), DatasourceToolNames("databricks")...), queryLifecycleToolNames...)
	trinoToolNames      = append(append([]string(nil), DatasourceToolNames("trino")...), queryLifecycleToolNames...)
	snowflakeToolNames  = append(append([]string(nil), DatasourceToolNames("snowflake")...), queryLifecycleToolNames...)
)

type RegistryOptions struct {
	Search     *search.Service
	Store      store.Store
	CfgDir     string
	Sources    *sources.Store
	RLM        *rlm.Runtime
	Atlassian  *atlassian.Manager
	Browser    *browsercontrol.Manager
	SharePoint *sharepoint.Manager
	GitHub     any
	Slack      any
	Arxiv      *arxiv.Manager
	Settings   *config.Store
	Secrets    *secrets.Store
}

func NewRegistry(opts *RegistryOptions) *Registry {
	r := &Registry{
		byName:             map[string]Tool{},
		searchAllowShallow: map[string]struct{}{},
		searchAllowDeep:    map[string]struct{}{},
	}

	// File tools
	r.mustRegister(newListFilesTool(), true, false, false)
	r.mustRegister(newReadFileTool(), true, false, false)
	r.mustRegister(newSearchFilesTool(), true, false, false)
	r.mustRegister(newFileSearchTool(), true, false, false)
	r.mustRegister(newLocalListFolderTool(), true, false, false)
	r.mustRegister(newLocalReadFileTool(), true, false, false)
	r.mustRegister(newLocalOpenFileTool(), true, false, false)
	r.mustRegister(newLocalReadPDFPageTool(), true, false, false)
	r.mustRegister(newLocalListGrantFolderTool(), false, false, false)
	r.mustRegister(newLocalSearchCommandTool(), false, false, false)
	r.mustRegister(newLocalSearchInGrantTool(), false, false, false)
	r.mustRegister(newLocalReadGrantFileTool(), false, false, false)
	r.mustRegister(newListGitRepositoriesTool(), true, false, false)
	r.mustRegister(newGitListRefsTool(), true, false, false)
	r.mustRegister(newGitLogTool(), true, false, false)
	r.mustRegister(newGitSearchHistoryTool(), true, false, false)
	r.mustRegister(newGitGrepTool(), true, false, false)
	r.mustRegister(newGitShowCommitTool(), true, false, false)
	r.mustRegister(newGitShowFileAtRefTool(), true, false, false)
	r.mustRegister(newInspectAttachmentTool(), true, false, false)
	r.mustRegister(newReadAttachmentTool(), true, false, false)
	r.mustRegister(newSearchAttachmentTool(), true, false, false)
	r.mustRegister(newExportThreadTool(), true, false, false)

	// CSV analysis tools
	r.mustRegister(newDescribeDatasetTool(), true, false, false)
	r.mustRegister(newSampleRowsTool(), true, false, false)
	r.mustRegister(newFilterCSVTool(), true, false, false)
	r.mustRegister(newAggregateCSVTool(), true, false, false)

	// BigQuery tools
	r.mustRegister(newRunBigQueryQueryTool(), true, false, false)
	r.mustRegister(newListBQDatasetsTool(), true, false, false)
	r.mustRegister(newListBQTablesTool(), true, false, false)
	r.mustRegister(newGetBQTableSchemaTool(), true, false, false)

	// Databricks tools
	r.mustRegister(newRunDatabricksQueryTool(), true, false, false)
	r.mustRegister(newListDatabricksCatalogsTool(), true, false, false)
	r.mustRegister(newListDatabricksSchemasTool(), true, false, false)
	r.mustRegister(newListDatabricksTablesTool(), true, false, false)
	r.mustRegister(newDescribeDatabricksTableTool(), true, false, false)

	// Trino tools
	r.mustRegister(newRunTrinoQueryTool(), true, false, false)
	r.mustRegister(newListTrinoCatalogsTool(), true, false, false)
	r.mustRegister(newListTrinoSchemasTool(), true, false, false)
	r.mustRegister(newListTrinoTablesTool(), true, false, false)
	r.mustRegister(newDescribeTrinoTableTool(), true, false, false)

	// Snowflake tools
	r.mustRegister(newRunSnowflakeQueryTool(), true, false, false)
	r.mustRegister(newListSnowflakeDatabasesTool(), true, false, false)
	r.mustRegister(newListSnowflakeSchemasTool(), true, false, false)
	r.mustRegister(newListSnowflakeTablesTool(), true, false, false)
	r.mustRegister(newDescribeSnowflakeTableTool(), true, false, false)

	// Query lifecycle tools
	r.mustRegister(newCheckQueryStatusTool(), true, false, false)
	r.mustRegister(newGetQueryResultsTool(), true, false, false)
	r.mustRegister(newValidateQueryResultTool(), true, false, false)
	r.mustRegister(newCancelQueryTool(), true, false, false)

	// Atlassian browser-authenticated tools. These are included per run only when
	// enabled by the Jira/Confluence integration toggles in settings.
	if opts != nil && opts.Atlassian != nil {
		r.mustRegister(newAtlassianStatusTool(opts.Atlassian), false, false, false)
		r.mustRegister(newAtlassianCheckConnectionTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraGetIssueTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraGetIssueHistoryTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraGetIssueCommentsTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraGetIssueAttachmentsTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraReadAttachmentTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraGetIssueLinkTypesTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraFindUsersTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraSearchIssuesTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraFindTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraRestGetTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraAddCommentTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraReplyToCommentTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraAddRelatedTicketsTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraUploadAttachmentTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraUpdateIssueTool(opts.Atlassian), false, false, false)
		r.mustRegister(newJiraCreateIssueTool(opts.Atlassian), false, false, false)
		r.mustRegister(newConfluenceSearchTool(opts.Atlassian), false, false, false)
		r.mustRegister(newConfluenceOpenPageTool(opts.Atlassian), false, false, false)
		r.mustRegister(newConfluenceOpenSpaceTool(opts.Atlassian), false, false, false)
		r.mustRegister(newConfluenceRestGetTool(opts.Atlassian), false, false, false)
	}
	if opts != nil && opts.Browser != nil {
		r.mustRegister(newBrowserControlStatusTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlNavigateTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlSnapshotTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlClickTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlTypeTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlPressTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlWaitTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlBackTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlLogsTool(opts.Browser), false, false, false)
		r.mustRegister(newBrowserControlClearLogsTool(opts.Browser), false, false, false)
	}
	if opts != nil && opts.SharePoint != nil {
		r.mustRegister(newSharePointStatusTool(opts.SharePoint), false, false, false)
		r.mustRegister(newSharePointCheckConnectionTool(opts.SharePoint), false, false, false)
		r.mustRegister(newSharePointSearchTool(opts.SharePoint), false, false, false)
		r.mustRegister(newSharePointGetMetadataTool(opts.SharePoint), false, false, false)
		r.mustRegister(newSharePointReadTool(opts.SharePoint), false, false, false)
		r.mustRegister(newSharePointSummarizeTool(opts.SharePoint), false, false, false)
		r.mustRegister(newSharePointDownloadTool(opts.SharePoint), false, false, false)
	}
	if opts != nil && opts.GitHub != nil {
		r.mustRegister(newGitHubStatusTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubCheckConnectionTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubListRepositoriesTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubListPullRequestsTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubGetPullRequestTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubListIssuesTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubGetIssueTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubListWorkflowRunsTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubGetWorkflowRunJobsTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubCloneRepositoryTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubPullRepositoryTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubRefreshRepositoryTool(opts.GitHub), false, false, false)
		r.mustRegister(newGitHubRunGHCommandTool(opts.GitHub), false, false, false)
	}
	if opts != nil && opts.Slack != nil {
		r.mustRegister(newSlackStatusTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackCheckConnectionTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackListConversationsTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackGetConversationTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackGetConversationMembersTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackGetMessagesTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackGetThreadRepliesTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackGetUserTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackSearchMessagesTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackDownloadFileTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackPostMessageTool(opts.Slack), false, false, false)
		r.mustRegister(newSlackUploadFileTool(opts.Slack), false, false, false)
	}
	if opts != nil && opts.Arxiv != nil {
		r.mustRegister(newArxivStatusTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivCheckConnectionTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivSearchTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivScreeningReportTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivRecommendationsTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivDownloadPaperTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivListPapersTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivGetPaperTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivGetPaperTextTool(opts.Arxiv), false, false, false)
		r.mustRegister(newArxivReadPaperTool(opts.Arxiv), false, false, false)
	}

	// UI/data tools. Image generation is registered unconditionally so its
	// metadata and permission identity are stable, but exposed to model requests
	// only when Image Generation is enabled in settings.
	var imageSettings *config.Store
	var imageSecrets *secrets.Store
	if opts != nil {
		imageSettings = opts.Settings
		imageSecrets = opts.Secrets
	}
	r.mustRegister(newGenerateImageTool(imageSettings, imageSecrets), false, false, false)
	r.mustRegister(newGetEchartsCapabilitiesTool(), true, false, false)
	r.mustRegister(newGetHighchartsCapabilitiesTool(), true, false, false)
	r.mustRegister(newBuildChartArtifactTool(), true, false, false)
	r.mustRegister(newCreateInteractiveArtifactTool(), true, false, false)
	r.mustRegister(newCreateInteractiveDataArtifactTool(), true, false, false)
	r.mustRegister(newDisplayTableTool(), true, false, false)
	r.mustRegister(newInspectDatasetArtifactTool(), true, true, true)
	r.mustRegister(newGetDatasetRowsTool(), true, true, true)
	r.mustRegister(newAggregateDatasetTool(), true, true, true)
	r.mustRegister(newSearchDatasetRowsTool(), true, true, true)

	if opts != nil && opts.Store != nil {
		r.mustRegister(newGetArtifactChunkTool(opts.Store, opts.CfgDir), true, true, true)
		r.mustRegister(newSearchArtifactContentTool(opts.Store, opts.CfgDir), true, true, true)
		r.mustRegister(newGetJSONArtifactTool(opts.Store, opts.CfgDir), true, true, true)
	}

	if opts != nil && opts.Search != nil && opts.Store != nil {
		r.mustRegister(newSearchThreadsTool(opts.Search), false, true, true)
		r.mustRegister(newSearchDocsTool(opts.Search), false, false, false)
		r.allowSearchAlias("search_docs", true, true)
		r.mustRegister(newGetDocTool(opts.Search), false, true, true)
		r.mustRegister(newGetArtifactTool(opts.Store, opts.CfgDir), false, true, true)
		r.mustRegister(newListMessagesTool(opts.Store), false, true, true)
	}
	if opts != nil && opts.RLM != nil {
		r.mustRegister(newRLMExecTool(opts.RLM), false, true, true)
	}

	return r
}

func (r *Registry) mustRegister(t Tool, includeDefault bool, allowSearchShallow bool, allowSearchDeep bool) {
	if r == nil {
		panic("tools.Registry: nil receiver")
	}
	if t == nil {
		panic("tools.Registry: nil tool")
	}
	def := t.Definition()
	name := strings.TrimSpace(def.Function.Name)
	if name == "" {
		panic("tools.Registry: tool with empty name")
	}
	if _, ok := r.byName[name]; ok {
		panic("tools.Registry: duplicate tool name: " + name)
	}
	r.byName[name] = t
	if includeDefault {
		r.defs = append(r.defs, def)
	}
	if allowSearchShallow {
		r.searchDefsShallow = append(r.searchDefsShallow, def)
		r.searchAllowShallow[name] = struct{}{}
	}
	if allowSearchDeep {
		r.searchDefsDeep = append(r.searchDefsDeep, def)
		r.searchAllowDeep[name] = struct{}{}
	}
}

func (r *Registry) allowSearchAlias(name string, allowShallow bool, allowDeep bool) {
	if r == nil {
		panic("tools.Registry: nil receiver")
	}
	n := strings.TrimSpace(name)
	if n == "" {
		panic("tools.Registry: empty search alias")
	}
	if _, ok := r.byName[n]; !ok {
		panic("tools.Registry: unknown search alias: " + n)
	}
	if allowShallow {
		r.searchAllowShallow[n] = struct{}{}
	}
	if allowDeep {
		r.searchAllowDeep[n] = struct{}{}
	}
}

func (r *Registry) Definitions() []types.ToolDefinition {
	if r == nil {
		return nil
	}
	return append([]types.ToolDefinition(nil), r.defs...)
}

func (r *Registry) DefinitionsByName(names ...string) []types.ToolDefinition {
	if r == nil || len(names) == 0 {
		return nil
	}
	out := make([]types.ToolDefinition, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		t, ok := r.byName[n]
		if !ok {
			continue
		}
		out = append(out, t.Definition())
		seen[n] = struct{}{}
	}
	return out
}

func (r *Registry) AnalysisCoreDefinitions() []types.ToolDefinition {
	return r.DefinitionsByName(analysisCoreToolNames...)
}

// ImageGenerationToolDefinitions exposes the billable image tool only when the
// user has enabled it in Settings. The tool itself resolves the selected
// connection and defaults at execution time so settings changes do not require
// a server restart.
func (r *Registry) ImageGenerationToolDefinitions(settings *config.Store) []types.ToolDefinition {
	if r == nil || settings == nil {
		return nil
	}
	cfg := settings.Get().ImageGeneration
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		return nil
	}
	return r.DefinitionsByName(generateImageToolName)
}

func (r *Registry) CSVToolDefinitions() []types.ToolDefinition {
	return r.DefinitionsByName(csvToolNames...)
}

func (r *Registry) LocalGrantToolDefinitions() []types.ToolDefinition {
	return r.DefinitionsByName(localGrantToolNames...)
}

func (r *Registry) BigQueryToolDefinitions() []types.ToolDefinition {
	return r.DefinitionsByName(bigQueryToolNames...)
}

func (r *Registry) DatabricksToolDefinitions() []types.ToolDefinition {
	return r.DefinitionsByName(databricksToolNames...)
}

func (r *Registry) TrinoToolDefinitions() []types.ToolDefinition {
	return r.DefinitionsByName(trinoToolNames...)
}

func (r *Registry) SnowflakeToolDefinitions() []types.ToolDefinition {
	return r.DefinitionsByName(snowflakeToolNames...)
}

func (r *Registry) DatasourceToolDefinitions(datasource string) []types.ToolDefinition {
	datasource = strings.TrimSpace(strings.ToLower(datasource))
	switch datasource {
	case "bigquery":
		return r.BigQueryToolDefinitions()
	case "databricks":
		return r.DatabricksToolDefinitions()
	case "trino":
		return r.TrinoToolDefinitions()
	case "snowflake":
		return r.SnowflakeToolDefinitions()
	default:
		return nil
	}
}

func (r *Registry) AtlassianToolDefinitions(jiraEnabled bool, confluenceEnabled bool) []types.ToolDefinition {
	if r == nil || (!jiraEnabled && !confluenceEnabled) {
		return nil
	}
	names := []string{}
	names = append(names, AtlassianSharedToolNames()...)
	if jiraEnabled {
		names = append(names, AtlassianJiraToolNames()...)
	}
	if confluenceEnabled {
		names = append(names, AtlassianConfluenceToolNames()...)
	}
	return r.DefinitionsByName(names...)
}

func (r *Registry) BrowserControlToolDefinitions(enabled bool) []types.ToolDefinition {
	if r == nil || !enabled {
		return nil
	}
	return r.DefinitionsByName(BrowserControlToolNames()...)
}

func (r *Registry) SharePointToolDefinitions(enabled bool) []types.ToolDefinition {
	if r == nil || !enabled {
		return nil
	}
	return r.DefinitionsByName(SharePointToolNames()...)
}

func (r *Registry) GitHubToolDefinitions(enabled bool) []types.ToolDefinition {
	if r == nil || !enabled {
		return nil
	}
	return r.DefinitionsByName(GitHubToolNames()...)
}

func (r *Registry) SlackToolDefinitions(readEnabled, writeEnabled bool) []types.ToolDefinition {
	if r == nil || (!readEnabled && !writeEnabled) {
		return nil
	}
	return r.DefinitionsByName(SlackToolNamesForAccess(readEnabled, writeEnabled)...)
}

func (r *Registry) ArxivToolDefinitions(enabled bool) []types.ToolDefinition {
	if r == nil || !enabled {
		return nil
	}
	return r.DefinitionsByName(ArxivToolNames()...)
}

func (r *Registry) ThreadSearchToolDefinitions(enabled bool) []types.ToolDefinition {
	if r == nil || !enabled {
		return nil
	}
	return r.DefinitionsByName("search_threads")
}

func (r *Registry) DefinitionsForSearch(depth string) []types.ToolDefinition {
	if r == nil {
		return nil
	}
	switch normalizeSearchDepth(depth) {
	case "off":
		return nil
	case "deep":
		return append([]types.ToolDefinition(nil), r.searchDefsDeep...)
	default:
		return append([]types.ToolDefinition(nil), r.searchDefsShallow...)
	}
}

func (r *Registry) AllowedForSearch(name string, depth string) bool {
	if r == nil {
		return false
	}
	n := strings.TrimSpace(name)
	if n == "" {
		return false
	}
	switch normalizeSearchDepth(depth) {
	case "off":
		return false
	case "deep":
		_, ok := r.searchAllowDeep[n]
		return ok
	default:
		_, ok := r.searchAllowShallow[n]
		return ok
	}
}

func (r *Registry) Get(name string) (Tool, bool) {
	if r == nil {
		return nil, false
	}
	t, ok := r.byName[strings.TrimSpace(name)]
	return t, ok
}

func normalizeSearchDepth(depth string) string {
	switch strings.TrimSpace(strings.ToLower(depth)) {
	case "off":
		return "off"
	case "deep":
		return "deep"
	default:
		return "shallow"
	}
}
