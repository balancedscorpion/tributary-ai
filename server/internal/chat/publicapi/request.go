package publicapi

import (
	"strings"

	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

const (
	maxInlineImageCount        = 4
	maxInlineImageDataURIBytes = 14 << 20
	maxInlineImageTotalBytes   = 15 << 20
)

func normalizeCreateRunRequest(body createRunRequest) (optionc.StartRunRequest, error) {
	if body.Stream {
		return optionc.StartRunRequest{}, invalidRequest("stream=true is not supported for POST /runs; use the events_url SSE endpoint")
	}
	content := strings.TrimSpace(body.Input.Content)
	images, err := normalizeImageAttachments(body.Input.ImageAttachments)
	if err != nil {
		return optionc.StartRunRequest{}, err
	}
	files := types.CloneFileAttachments(body.Input.FileAttachments)
	if content == "" && len(images) == 0 && len(files) == 0 {
		return optionc.StartRunRequest{}, invalidRequest("input.content is required unless attachments are provided")
	}

	referencedThreads := dedupeStringSlice(body.Input.ReferencedThreadIDs)
	if len(referencedThreads) > 5 {
		return optionc.StartRunRequest{}, invalidRequest("at most 5 referenced threads are supported")
	}

	purpose := strings.ToLower(strings.TrimSpace(body.Purpose))
	if purpose == "" {
		purpose = "analysis"
	}
	if purpose != "analysis" && purpose != "search" {
		return optionc.StartRunRequest{}, invalidRequest("purpose must be analysis or search")
	}
	searchDepth := strings.ToLower(strings.TrimSpace(body.SearchDepth))
	switch searchDepth {
	case "":
		searchDepth = "shallow"
	case "off", "shallow", "deep":
	default:
		return optionc.StartRunRequest{}, invalidRequest("search_depth must be off, shallow, or deep")
	}

	var projectID *string
	if p := strings.TrimSpace(body.ProjectID); p != "" {
		projectID = &p
	}

	dataSources := parseRunDataSources(body.DataSources)
	dataSourceOverrides := parseRunDataSourceOverrides(body.DataSources)

	req := optionc.StartRunRequest{
		ThreadID:               strings.TrimSpace(body.ThreadID),
		Content:                content,
		HiddenContext:          strings.TrimSpace(body.Input.HiddenContext),
		ReferencedMessageIDs:   dedupeStringSlice(body.Input.ReferencedMessageIDs),
		ReferencedThreadIDs:    referencedThreads,
		ReferencedArtifactIDs:  dedupeStringSlice(body.Input.ReferencedArtifactIDs),
		ImageAttachments:       images,
		FileAttachments:        files,
		ProjectIDOverride:      projectID,
		MaxIterationsOverride:  body.MaxIterations,
		Purpose:                purpose,
		SearchDepth:            searchDepth,
		DataSources:            dataSources,
		DataSourceOverrides:    dataSourceOverrides,
		ClientNowISO:           strings.TrimSpace(body.RuntimeContext.NowISO),
		ClientTimezone:         strings.TrimSpace(body.RuntimeContext.Timezone),
		ClientUTCOffsetMinutes: body.RuntimeContext.UTCOffsetMinutes,
	}
	return req, nil
}

func normalizeImageAttachments(images []types.ImageAttachment) ([]types.ImageAttachment, error) {
	if len(images) > maxInlineImageCount {
		return nil, invalidRequest("at most 4 inline image attachments are supported")
	}
	if len(images) == 0 {
		return nil, nil
	}
	total := int64(0)
	seen := map[string]struct{}{}
	out := make([]types.ImageAttachment, 0, len(images))
	for _, image := range images {
		dataURI := strings.TrimSpace(image.DataURI)
		if dataURI == "" {
			return nil, invalidRequest("image attachment dataUri is required")
		}
		if !strings.HasPrefix(strings.ToLower(dataURI), "data:image/") {
			return nil, invalidRequest("image attachments must be data:image/* URIs")
		}
		if len(dataURI) > maxInlineImageDataURIBytes {
			return nil, invalidRequest("inline image attachment exceeds size limit")
		}
		total += int64(len(dataURI))
		if total > maxInlineImageTotalBytes {
			return nil, invalidRequest("inline image attachments exceed total size limit")
		}
		mimeType := strings.ToLower(strings.TrimSpace(image.MimeType))
		if mimeType == "" {
			mimeType = inferImageMimeTypeFromDataURI(dataURI)
		}
		mimeType = supportedInlineImageMimeType(mimeType)
		if mimeType == "" {
			return nil, invalidRequest("image attachments must be PNG, JPEG, WEBP, or GIF data URIs")
		}
		if image.SizeBytes != nil && *image.SizeBytes > 10*1024*1024 {
			return nil, invalidRequest("image attachment exceeds 10MB limit")
		}
		if _, ok := seen[dataURI]; ok {
			continue
		}
		seen[dataURI] = struct{}{}
		next := image
		next.ArtifactID = strings.TrimSpace(next.ArtifactID)
		next.FileName = strings.TrimSpace(next.FileName)
		next.MimeType = mimeType
		next.DataURI = dataURI
		next.Source = strings.ToLower(strings.TrimSpace(next.Source))
		out = append(out, next)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func inferImageMimeTypeFromDataURI(dataURI string) string {
	raw := strings.TrimSpace(dataURI)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return ""
	}
	comma := strings.Index(raw, ",")
	if comma <= len("data:") {
		return ""
	}
	meta := strings.ToLower(strings.TrimSpace(raw[len("data:"):comma]))
	if semi := strings.Index(meta, ";"); semi >= 0 {
		meta = strings.TrimSpace(meta[:semi])
	}
	if !strings.HasPrefix(meta, "image/") {
		return ""
	}
	return meta
}

func supportedInlineImageMimeType(raw string) string {
	mediaType := strings.ToLower(strings.TrimSpace(raw))
	if semi := strings.Index(mediaType, ";"); semi >= 0 {
		mediaType = strings.TrimSpace(mediaType[:semi])
	}
	switch mediaType {
	case "image/png", "image/webp", "image/gif":
		return mediaType
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	default:
		return ""
	}
}

func parseRunDataSources(raw map[string]any) *config.DatasourceConnections {
	if len(raw) == 0 {
		return nil
	}
	out := &config.DatasourceConnections{}
	for name, value := range raw {
		enabled, ok := extractEnabled(value)
		if !ok {
			continue
		}
		state := &config.DatasourceConnectionState{Enabled: &enabled}
		switch normalizeDatasourceName(name) {
		case "bigquery":
			out.BigQuery = state
		case "databricks":
			out.Databricks = state
		case "trino":
			out.Trino = state
		case "snowflake":
			out.Snowflake = state
		}
	}
	if out.BigQuery == nil && out.Databricks == nil && out.Trino == nil && out.Snowflake == nil {
		return nil
	}
	return out
}

func parseRunDataSourceOverrides(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	for key, value := range raw {
		name := normalizeDatasourceName(key)
		if name == "" {
			continue
		}
		out[name] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractEnabled(raw any) (bool, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return false, false
	}
	value, ok := m["enabled"]
	if !ok {
		value, ok = m["is_enabled"]
	}
	if !ok {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true, true
		case "0", "false", "no", "off":
			return false, true
		}
	}
	return false, false
}

func normalizeDatasourceName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "bigquery", "big_query", "bq":
		return "bigquery"
	case "databricks", "dbx":
		return "databricks"
	case "trino":
		return "trino"
	case "snowflake":
		return "snowflake"
	default:
		return ""
	}
}

func dedupeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
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
	if len(out) == 0 {
		return nil
	}
	return out
}
