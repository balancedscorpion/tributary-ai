package optionc

import "strings"

type runConnectionScope map[string]bool

func normalizeRunConnectionID(id string) string {
	normalized := strings.TrimSpace(strings.ToLower(id))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, " ", "-")
	switch normalized {
	case "gcp", "google", "google-cloud", "googlecloud":
		return "bigquery"
	case "big-query":
		return "bigquery"
	}
	return normalized
}

func newRunConnectionScope(ids []string) runConnectionScope {
	out := runConnectionScope{}
	for _, raw := range ids {
		switch id := normalizeRunConnectionID(raw); id {
		case "bigquery", "databricks", "trino", "snowflake", "atlassian", "browser", "sharepoint", "github", "slack", "arxiv", "thread-search", "search-threads", "image-generation", "web-search", "python-sandbox":
			if id == "search-threads" {
				id = "thread-search"
			}
			out[id] = true
		case "local", "localcomputer", "local-computer", "my-computer":
			out["local-computer"] = true
		}
	}
	return out
}

func applyRunActiveConnectionScope(
	enabledDatasources map[string]bool,
	enabledAtlassian atlassianEnablement,
	enabledBrowserControl bool,
	enabledSharePoint bool,
	enabledGitHub bool,
	enabledSlack slackToolAccess,
	enabledArxiv bool,
	enabledThreadSearch bool,
	enabledLocalComputer bool,
	activeConnectionIDs []string,
) (map[string]bool, atlassianEnablement, bool, bool, bool, slackToolAccess, bool, bool, bool) {
	scope := newRunConnectionScope(activeConnectionIDs)

	scopedDatasources := make(map[string]bool, len(enabledDatasources))
	for datasource, enabled := range enabledDatasources {
		name := normalizeDatasourceName(datasource)
		scopedDatasources[name] = enabled && scope[name]
	}

	if !scope["atlassian"] {
		enabledAtlassian = atlassianEnablement{}
	}
	if !scope["browser"] {
		enabledBrowserControl = false
	}
	if !scope["sharepoint"] {
		enabledSharePoint = false
	}
	if !scope["github"] {
		enabledGitHub = false
	}
	if !scope["slack"] {
		enabledSlack = slackToolAccess{}
	}
	if !scope["arxiv"] {
		enabledArxiv = false
	}
	if !scope["thread-search"] {
		enabledThreadSearch = false
	}
	if !scope["local-computer"] {
		enabledLocalComputer = false
	}

	return scopedDatasources, enabledAtlassian, enabledBrowserControl, enabledSharePoint, enabledGitHub, enabledSlack, enabledArxiv, enabledThreadSearch, enabledLocalComputer
}
