package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/databricks"
	"ai-analyst-local/internal/sources"
	"ai-analyst-local/internal/trino"
)

type ResultFormat string

const (
	ResultFormatJSON ResultFormat = "json"
	ResultFormatText ResultFormat = "text"
)

type Output struct {
	OK             bool             `json:"ok"`
	ResultFormat   ResultFormat     `json:"result_format"`
	Result         any              `json:"result"`
	QueryMetadata  map[string]any   `json:"query_metadata,omitempty"`
	ResultMetadata map[string]any   `json:"result_metadata,omitempty"`
	Artifact       *ArtifactPayload `json:"-"`
}

type ArtifactPayload struct {
	// Optional explicit artifact ID. When empty, manager chooses the default ID.
	ID string

	Type     string
	Title    string
	Language *string
	Content  string
	Metadata map[string]any
}

const (
	LocalOpenFileToolName      = "local_open_file"
	LocalSearchCommandToolName = "local_search_command"
)

type ChartEngineFlags struct {
	Echarts    bool
	Highcharts bool
}

const (
	defaultQueryPageSize    = 200
	maxQueryPageSize        = config.MaxDatabaseQueryRowLimit
	defaultReadFileBytes    = 32 * 1024
	maxReadFileBytes        = 256 * 1024
	defaultSearchMatches    = 20
	maxSearchMatches        = 200
	defaultCSVTransformRows = 100
	maxCSVTransformRows     = 1000
)

type DatasourceCapability struct {
	Name          string
	RunQueryTool  string
	MetadataTools []string
	AsyncQuery    bool
}

var datasourceCapabilities = []DatasourceCapability{
	{
		Name:         "bigquery",
		RunQueryTool: "run_bigquery_query",
		MetadataTools: []string{
			"list_bq_datasets",
			"list_bq_tables",
			"get_bq_table_schema",
		},
		AsyncQuery: true,
	},
	{
		Name:         "databricks",
		RunQueryTool: "run_databricks_query",
		MetadataTools: []string{
			"list_databricks_catalogs",
			"list_databricks_schemas",
			"list_databricks_tables",
			"describe_databricks_table",
		},
		AsyncQuery: true,
	},
	{
		Name:         "trino",
		RunQueryTool: "run_trino_query",
		MetadataTools: []string{
			"list_trino_catalogs",
			"list_trino_schemas",
			"list_trino_tables",
			"describe_trino_table",
		},
		AsyncQuery: true,
	},
	{
		Name:         "snowflake",
		RunQueryTool: "run_snowflake_query",
		MetadataTools: []string{
			"list_snowflake_databases",
			"list_snowflake_schemas",
			"list_snowflake_tables",
			"describe_snowflake_table",
		},
		AsyncQuery: true,
	},
}

func DatasourceCapabilities() []DatasourceCapability {
	out := make([]DatasourceCapability, 0, len(datasourceCapabilities))
	for _, cap := range datasourceCapabilities {
		copied := cap
		if len(cap.MetadataTools) > 0 {
			copied.MetadataTools = append([]string(nil), cap.MetadataTools...)
		}
		out = append(out, copied)
	}
	return out
}

func DatasourceNames() []string {
	out := make([]string, 0, len(datasourceCapabilities))
	for _, cap := range datasourceCapabilities {
		if strings.TrimSpace(cap.Name) == "" {
			continue
		}
		out = append(out, cap.Name)
	}
	return out
}

func DatasourceToolNames(datasource string) []string {
	datasource = strings.TrimSpace(strings.ToLower(datasource))
	if datasource == "" {
		return nil
	}
	for _, cap := range datasourceCapabilities {
		if strings.TrimSpace(strings.ToLower(cap.Name)) != datasource {
			continue
		}
		out := make([]string, 0, 1+len(cap.MetadataTools))
		if strings.TrimSpace(cap.RunQueryTool) != "" {
			out = append(out, strings.TrimSpace(cap.RunQueryTool))
		}
		for _, name := range cap.MetadataTools {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			out = append(out, name)
		}
		return out
	}
	return nil
}

func DatasourceForToolName(toolName string) string {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return ""
	}
	for _, cap := range datasourceCapabilities {
		if strings.EqualFold(toolName, strings.TrimSpace(cap.RunQueryTool)) {
			return cap.Name
		}
		for _, metaTool := range cap.MetadataTools {
			if strings.EqualFold(toolName, strings.TrimSpace(metaTool)) {
				return cap.Name
			}
		}
	}
	return ""
}

func AsyncQueryRunToolNames() []string {
	out := make([]string, 0, len(datasourceCapabilities))
	for _, cap := range datasourceCapabilities {
		if !cap.AsyncQuery {
			continue
		}
		name := strings.TrimSpace(cap.RunQueryTool)
		if name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func IsAsyncQueryRunTool(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return false
	}
	for _, name := range AsyncQueryRunToolNames() {
		if strings.EqualFold(toolName, name) {
			return true
		}
	}
	return false
}

func clampPositiveInt(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func resolveQueryPageSize(inputLimit, configuredLimit int) (requestedLimit int, pageSize int, isCapped bool, effectiveConfiguredLimit int) {
	if configuredLimit <= 0 {
		configuredLimit = maxQueryPageSize
	}
	if configuredLimit > maxQueryPageSize {
		configuredLimit = maxQueryPageSize
	}
	if configuredLimit <= 0 {
		configuredLimit = defaultQueryPageSize
	}

	requestedLimit = inputLimit
	if requestedLimit <= 0 {
		requestedLimit = configuredLimit
	}

	pageSize = requestedLimit
	if pageSize > configuredLimit {
		pageSize = configuredLimit
		isCapped = true
	}
	if pageSize <= 0 {
		pageSize = defaultQueryPageSize
	}
	effectiveConfiguredLimit = configuredLimit
	return
}

func clampPositiveInt64(v, def, max int64) int64 {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func parsePageOffsetToken(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	offset, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || offset < 0 {
		return 0, false
	}
	return offset, true
}

func encodePageOffsetToken(offset int64) string {
	return strconv.FormatInt(offset, 10)
}

func TextOK(s string) Output {
	return Output{OK: true, ResultFormat: ResultFormatText, Result: s}
}

func JSONOK(v any) Output {
	return NormalizeOutputMetadata(Output{OK: true, ResultFormat: ResultFormatJSON, Result: v})
}

func JSONOKWithMetadata(v any, metadata map[string]any) Output {
	return NormalizeOutputMetadata(Output{
		OK:             true,
		ResultFormat:   ResultFormatJSON,
		Result:         v,
		ResultMetadata: cloneOutputMetadataMap(metadata),
	})
}

func JSONError(msg string) Output {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "unknown error"
	}
	return Output{OK: false, ResultFormat: ResultFormatJSON, Result: map[string]any{"error": msg}}
}

func NormalizeOutputMetadata(out Output) Output {
	merged := cloneOutputMetadataMap(takeOutputNestedResultMetadata(out.Result))
	for key, value := range out.ResultMetadata {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if merged == nil {
			merged = map[string]any{}
		}
		merged[key] = value
	}
	out.ResultMetadata = merged
	return out
}

func takeOutputNestedResultMetadata(result any) map[string]any {
	m, ok := result.(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := m["result_metadata"]
	if !ok {
		return nil
	}
	nested, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	delete(m, "result_metadata")
	return nested
}

func cloneOutputMetadataMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (o Output) ContentString() (string, error) {
	switch o.ResultFormat {
	case ResultFormatText:
		if s, ok := o.Result.(string); ok {
			return s, nil
		}
		b, err := json.Marshal(o.Result)
		if err != nil {
			return "", err
		}
		return string(b), nil
	case ResultFormatJSON:
		b, err := json.Marshal(o.Result)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		return "", fmt.Errorf("unknown result format: %q", o.ResultFormat)
	}
}

type ExecContext struct {
	ProjectID             string
	Projects              *projects.Store
	BigQuery              *bigquery.Handler
	BigQueryHTTP          http.Handler // optional adapter/fake handler for tests and wrappers
	Databricks            *databricks.Handler
	DatabricksHTTP        http.Handler // optional adapter/fake handler for tests and wrappers
	Trino                 *trino.Handler
	TrinoHTTP             http.Handler // optional adapter/fake handler for tests and wrappers
	SnowflakeHTTP         http.Handler // optional snowflake handler for query + metadata tools
	Jobs                  *jobs.Registry
	LongHorizon           store.LongHorizonRepository // durable query validation/dependency lifecycle
	Store                 store.Store
	Usage                 usage.Repository
	Sources               *sources.Store
	ThreadID              string
	RunID                 string
	ToolCallID            string // durable per-run identity used to make replay-safe submissions
	CfgDir                string
	EvidenceBudget        int
	DatabaseQueryRowLimit int
	ChartEngines          ChartEngineFlags
	OnProgress            func(progress map[string]any) // emits tool_progress SSE
	RequestScanApproval   jobs.ScanApprovalFunc
	// True when the run manager has already resolved a persistent permission
	// approval for this exact tool call. Tools with legacy/internal approval
	// queues should execute directly instead of asking a second time.
	PermissionApproved bool

	SearchDepth string
}

type Tool interface {
	Definition() types.ToolDefinition
	Execute(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error)
}

type ExecutableTool struct {
	Def  types.ToolDefinition
	Exec func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error)
}

func (t ExecutableTool) Definition() types.ToolDefinition { return t.Def }
func (t ExecutableTool) Execute(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
	if t.Exec == nil {
		return JSONError("tool execute not implemented"), errors.New("tool execute not implemented")
	}
	return t.Exec(ctx, exec, args)
}

func decodeArgs(args json.RawMessage, dst any) error {
	if dst == nil {
		return errors.New("nil dst")
	}
	if len(bytes.TrimSpace(args)) == 0 {
		args = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected additional JSON values")
		}
		return err
	}
	return nil
}
