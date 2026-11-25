package githubcli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

func normalizePullRequest(raw map[string]any) PullRequest {
	raw = normalizeMap(raw)
	comments := objectSlice(raw["comments"])
	return PullRequest{
		Number:            intValue(raw["number"]),
		Title:             stringValue(raw["title"]),
		State:             stringValue(raw["state"]),
		URL:               stringValue(raw["url"]),
		Body:              stringValue(raw["body"]),
		IsDraft:           boolValue(raw["isDraft"]),
		Author:            objectMap(raw["author"]),
		Labels:            objectSlice(raw["labels"]),
		Assignees:         objectSlice(raw["assignees"]),
		Comments:          comments,
		CommentsCount:     commentsCount(raw["comments"], comments),
		CreatedAt:         stringValue(raw["createdAt"]),
		UpdatedAt:         stringValue(raw["updatedAt"]),
		ClosedAt:          stringValue(raw["closedAt"]),
		MergedAt:          stringValue(raw["mergedAt"]),
		HeadRefName:       stringValue(raw["headRefName"]),
		BaseRefName:       stringValue(raw["baseRefName"]),
		ReviewDecision:    stringValue(raw["reviewDecision"]),
		StatusCheckRollup: objectSlice(raw["statusCheckRollup"]),
	}
}

func normalizeIssue(raw map[string]any) Issue {
	raw = normalizeMap(raw)
	comments := objectSlice(raw["comments"])
	return Issue{
		Number:        intValue(raw["number"]),
		Title:         stringValue(raw["title"]),
		State:         stringValue(raw["state"]),
		URL:           stringValue(raw["url"]),
		Body:          stringValue(raw["body"]),
		Author:        objectMap(raw["author"]),
		Labels:        objectSlice(raw["labels"]),
		Assignees:     objectSlice(raw["assignees"]),
		Comments:      comments,
		CommentsCount: commentsCount(raw["comments"], comments),
		CreatedAt:     stringValue(raw["createdAt"]),
		UpdatedAt:     stringValue(raw["updatedAt"]),
		ClosedAt:      stringValue(raw["closedAt"]),
	}
}

func normalizeWorkflowRun(raw map[string]any) WorkflowRun {
	raw = normalizeMap(raw)
	return WorkflowRun{
		DatabaseID:         int64Value(raw["databaseId"]),
		WorkflowDatabaseID: int64Value(raw["workflowDatabaseId"]),
		Number:             intValue(raw["number"]),
		Attempt:            intValue(raw["attempt"]),
		Name:               stringValue(raw["name"]),
		WorkflowName:       stringValue(raw["workflowName"]),
		DisplayTitle:       stringValue(raw["displayTitle"]),
		Status:             stringValue(raw["status"]),
		Conclusion:         stringValue(raw["conclusion"]),
		Event:              stringValue(raw["event"]),
		HeadBranch:         stringValue(raw["headBranch"]),
		HeadSHA:            stringValue(raw["headSha"]),
		URL:                stringValue(raw["url"]),
		CreatedAt:          stringValue(raw["createdAt"]),
		UpdatedAt:          stringValue(raw["updatedAt"]),
		StartedAt:          stringValue(raw["startedAt"]),
	}
}

func normalizeWorkflowJob(raw map[string]any) WorkflowJob {
	raw = normalizeMap(raw)
	return WorkflowJob{
		DatabaseID:  int64Value(raw["databaseId"]),
		Name:        stringValue(raw["name"]),
		Status:      stringValue(raw["status"]),
		Conclusion:  stringValue(raw["conclusion"]),
		URL:         stringValue(raw["url"]),
		StartedAt:   stringValue(raw["startedAt"]),
		CompletedAt: stringValue(raw["completedAt"]),
		Steps:       objectSlice(raw["steps"]),
	}
}

func normalizeMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = normalizeValue(value)
	}
	return out
}

func normalizeValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return normalizeMap(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, normalizeValue(item))
		}
		return out
	case json.Number:
		if i, err := v.Int64(); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(v.String(), 64); err == nil {
			return f
		}
		return v.String()
	default:
		return value
	}
}

func objectMap(value any) map[string]any {
	switch v := normalizeValue(value).(type) {
	case map[string]any:
		return v
	default:
		return nil
	}
}

func objectSlice(value any) []map[string]any {
	value = normalizeValue(value)
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func commentsCount(raw any, comments []map[string]any) int {
	if n := intValue(raw); n > 0 {
		return n
	}
	if len(comments) > 0 {
		return len(comments)
	}
	return 0
}

func stringValue(value any) string {
	value = normalizeValue(value)
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func boolValue(value any) bool {
	switch v := normalizeValue(value).(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func intValue(value any) int {
	i := int64Value(value)
	if i > int64(^uint(0)>>1) {
		return int(^uint(0) >> 1)
	}
	return int(i)
}

func int64Value(value any) int64 {
	value = normalizeValue(value)
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		i, _ := v.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return i
	default:
		return 0
	}
}

func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
