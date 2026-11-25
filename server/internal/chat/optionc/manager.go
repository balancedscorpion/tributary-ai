package optionc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/chat/engine"
	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/providers/anthropic"
	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/runs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/databricks"
	"ai-analyst-local/internal/localroots"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/sources"
	"ai-analyst-local/internal/trino"
)

const (
	DefaultMaxConcurrentRuns  = 2
	DefaultMaxHistoryMessages = 20

	defaultMaxInputTokens     = 180000
	defaultEvidenceBudget     = 16
	defaultTokenCharsPerToken = 4
	defaultCompactionGuardCap = 170000
	minCompactionGuardCap     = 50000
	nativeImageEstimateTokens = 1024
	nativeFileEstimateTokens  = 2048

	artifactInlineMaxBytes = 512 << 10 // 512 KiB
	// Mirror inbound API image guardrails so model-side multimodal payload stays bounded.
	maxModelImageAttachmentCount = 4
	maxModelImageDataURIBytes    = 14 << 20
	maxModelImageTotalBytes      = 15 << 20
	toolResultPreviewBytes       = 8 << 10
	toolResultTextLimit          = 2048
	toolResultArrayLimit         = 6
	toolResultMapKeyLimit        = 24
	toolResultEnvelopeArrayLimit = 50
	toolResultEnvelopeByteLimit  = 64 << 10

	defaultAnthropicToolMessageLimit       = 4
	defaultAnthropicCompactionSummaryLimit = 6
	defaultApprovalWaitTimeout             = 10 * time.Minute
	finalizedQueuedRunReplayGrace          = 2 * time.Minute
	highchartsEngineName                   = "highcharts"
	echartsEngineName                      = "echarts"

	contextCheckpointEnvelopeKind    = "active_context_checkpoint"
	contextCheckpointEnvelopeVersion = 1
	localPhaseContextCheckpoint      = "context_checkpoint_written"
	localPhaseContextRetry           = "context_retrying_with_checkpoint"
)

var (
	ErrQueryJobsUnavailable   = errors.New("query jobs registry is not configured")
	ErrQueryJobUnauthorized   = errors.New("query job owner token validation failed")
	ErrQueryJobThreadScope    = errors.New("query job thread validation failed")
	ErrQueryJobRunScope       = errors.New("query job run validation failed")
	queryObligationIDTextRE   = regexp.MustCompile(`qob-[0-9a-f]{32}`)
	queryArgumentKeyTextRE    = regexp.MustCompile(`"([A-Za-z0-9_.-]+)"\s*:`)
	queryRetryTargetTextRE    = regexp.MustCompile(`(?i)\b(?:replaces[_ -]?job[_ -]?id|replacement[_ -]?(?:target[_ -]?)?job[_ -]?id|attempted[_ -]?(?:replaces|replacement|retry)[_ -]?(?:target|job[_ -]?id)?|retry[_ -]?target|target[_ -]?job[_ -]?id)\b\s*[:=]\s*(?:"(?:qj-[A-Za-z0-9][A-Za-z0-9_-]{0,127}|nil|null|<nil>|0{4,}(?:-0{4,})*)"|'(?:qj-[A-Za-z0-9][A-Za-z0-9_-]{0,127}|nil|null|<nil>|0{4,}(?:-0{4,})*)'|(?:qj-[A-Za-z0-9][A-Za-z0-9_-]{0,127}|nil|null|<nil>|0{4,}(?:-0{4,})*))`)
	queryRetryOwnershipTextRE = regexp.MustCompile(`(?i)\breplacement[_ -]?target\s+(?:\[[^\]\r\n]{1,256}\]|qj-[A-Za-z0-9][A-Za-z0-9_-]{0,127}|nil|null|<nil>|0{4,}(?:-0{4,})*)\s+is\s+not\s+owned\s+by\s+(?:the\s+)?current\s+run\b`)
)

type anthropicToolProfile struct {
	includeCSV        bool
	includeSearch     bool
	datasourceEnabled map[string]bool
}

type Options struct {
	MaxConcurrentRuns  int
	MaxHistoryMessages int

	// DeltaEmitInterval coalesces streaming deltas to reduce UI churn/bandwidth.
	// Default: 50ms.
	DeltaEmitInterval time.Duration

	// BufferSize controls how many recent SSE events are kept in-memory per run.
	// This lets clients connect slightly late and still receive the final events.
	// Default: 2048.
	BufferSize int

	// QueryJobs optionally provides async query job lookup/cancel support.
	QueryJobs any

	// LongHorizon is the durable continuation/query dependency repository.
	// It must be obtained from the concrete base chat store, not an indexing
	// wrapper which intentionally does not forward this lifecycle contract.
	LongHorizon store.LongHorizonRepository

	// SnowflakeHTTP optionally provides Snowflake handler-backed query execution.
	SnowflakeHTTP http.Handler

	// ToolResultCompactionStrategy optionally controls how tool results are compacted
	// for persistence and in-flight dispatch.
	ToolResultCompactionStrategy ToolResultCompactionStrategy

	// ToolLoopGuardFactory optionally controls loop-prevention behavior for repeated
	// tool calls that do not make progress.
	ToolLoopGuardFactory ToolLoopGuardFactory

	// Permissions optionally evaluates and queues persistent approval requests
	// before tools execute.
	Permissions *permissions.Service

	// Sources indexes app-managed file and repository sources outside the
	// legacy copied project-files store.
	Sources *sources.Store

	// Usage is the provider-invocation ledger used to roll exact call-level
	// token totals back onto legacy run fields.
	Usage usage.Repository

	// ApprovalWaitTimeout is retained for compatibility with older callers.
	// Chat-run tool approvals now wait until approval, rejection, cancellation,
	// or run context cancellation instead of timing out into a final tool result.
	ApprovalWaitTimeout time.Duration
}

type Manager struct {
	store      store.Store
	settings   *config.Store
	providers  *providers.Factory
	tools      *tools.Registry
	projects   *projects.Store
	bigquery   *bigquery.Handler
	databricks *databricks.Handler
	trino      *trino.Handler
	snowflake  http.Handler

	cfgDir string

	maxConcurrent             int
	maxHistory                int
	deltaInterval             time.Duration
	bufferSize                int
	contextPlanner            *ContextPlanner
	contextPlannerForSnapshot func(providers.DispatchSnapshot) *ContextPlanner
	queryJobs                 any
	longHorizon               store.LongHorizonRepository
	resumeClaimant            string
	toolCompaction            ToolResultCompactionStrategy
	toolLoopGuard             ToolLoopGuardFactory
	permissions               *permissions.Service
	sources                   *sources.Store
	usage                     usage.Repository
	approvalWait              time.Duration
	localSearchAgent          *localSearchAgentService
	localSearchMu             sync.Mutex
	localSearchJobs           map[string]localSearchAgentJob
	queryDependencyFaults     map[string]map[string]string
	queryProtocolFaultChecked map[string]bool
	longHorizonRecoveryOnce   sync.Once

	sem chan struct{}

	mu             sync.Mutex
	runs           map[string]*runState
	activeByThread map[string]string
}

type runState struct {
	threadID string

	mu      sync.Mutex
	subs    map[int]chan SSEEvent
	nextSub int

	buffer        []SSEEvent
	nextSeq       int64
	durableEvents bool

	done   bool
	doneCh chan struct{}

	cancel context.CancelFunc
}

type StartRunRequest struct {
	ThreadID string

	// Optional capability preflight contract. Legacy callers omit these fields.
	// Once PlanID is supplied the target/hash/fingerprint are mandatory and the
	// store rejects any stale or divergent plan.
	PlanID            string
	Target            *store.RunTarget
	RequirementsHash  string
	ConfigFingerprint string
	ChoiceSelections  []store.PlanChoiceSelection

	// Visible user content (what should be stored and shown).
	Content string

	// Optional context appended to the user content for the model only.
	HiddenContext string

	ReferencedMessageIDs  []string
	ReferencedThreadIDs   []string
	ReferencedArtifactIDs []string
	ImageAttachments      []types.ImageAttachment
	FileAttachments       []types.FileAttachment

	ProjectIDOverride     *string
	MaxIterationsOverride *int

	// Optional run purpose. Supported values: "analysis", "search".
	Purpose string
	// Optional search depth hint. Supported values: "off", "shallow", "deep".
	SearchDepth string

	// Optional datasource state overrides requested for this run.
	// We intentionally accept only enabled flags, and ignore the remainder.
	DataSources *config.DatasourceConnections
	// Raw datasource override objects keyed by datasource name.
	// Preserves forward-compatible objects that may not be present as typed fields yet.
	DataSourceOverrides map[string]any

	// Optional run-local connection scope. When provided, this further restricts
	// the globally enabled/authenticated tool families for this specific chat run.
	ActiveConnectionIDs         []string
	ActiveConnectionIDsProvided bool

	// Optional client runtime clock context captured on the user's device.
	// When provided, this is used to anchor temporal guidance in the system prompt.
	ClientNowISO           string
	ClientTimezone         string
	ClientUTCOffsetMinutes *int
}

type StartRunResult struct {
	Thread store.Thread
	Run    store.Run
}

type ToolTestRequest struct {
	ToolName    string
	ThreadID    string
	ProjectID   string
	Args        json.RawMessage
	SearchDepth string
}

type ToolTestResult struct {
	ToolName         string                       `json:"tool_name"`
	Mode             tools.ToolTestMode           `json:"mode"`
	RequiresApproval bool                         `json:"requires_approval"`
	Category         tools.ToolPermissionCategory `json:"category"`
	Origin           tools.ToolOrigin             `json:"origin"`
	ToolSetID        string                       `json:"tool_set_id"`
	ConnectorID      string                       `json:"connector_id,omitempty"`
	Presentation     tools.ToolPresentationSpec   `json:"presentation"`
	Args             map[string]any               `json:"args"`
	Output           tools.Output                 `json:"output"`
	Progress         []map[string]any             `json:"progress,omitempty"`
}

type toolTestArtifactJSON struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Title    string         `json:"title"`
	Content  string         `json:"content"`
	Language *string        `json:"language,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

type toolTestOutputJSON struct {
	OK             bool                  `json:"ok"`
	ResultFormat   tools.ResultFormat    `json:"result_format"`
	Result         any                   `json:"result"`
	QueryMetadata  map[string]any        `json:"query_metadata,omitempty"`
	ResultMetadata map[string]any        `json:"result_metadata,omitempty"`
	Artifact       *toolTestArtifactJSON `json:"artifact,omitempty"`
}

func (r ToolTestResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ToolName         string                       `json:"tool_name"`
		Mode             tools.ToolTestMode           `json:"mode"`
		RequiresApproval bool                         `json:"requires_approval"`
		Category         tools.ToolPermissionCategory `json:"category"`
		Origin           tools.ToolOrigin             `json:"origin"`
		ToolSetID        string                       `json:"tool_set_id"`
		ConnectorID      string                       `json:"connector_id,omitempty"`
		Presentation     tools.ToolPresentationSpec   `json:"presentation"`
		Args             map[string]any               `json:"args"`
		Output           toolTestOutputJSON           `json:"output"`
		Progress         []map[string]any             `json:"progress,omitempty"`
	}{
		ToolName:         r.ToolName,
		Mode:             r.Mode,
		RequiresApproval: r.RequiresApproval,
		Category:         r.Category,
		Origin:           r.Origin,
		ToolSetID:        r.ToolSetID,
		ConnectorID:      r.ConnectorID,
		Presentation:     r.Presentation,
		Args:             r.Args,
		Output: toolTestOutputJSON{
			OK:             r.Output.OK,
			ResultFormat:   r.Output.ResultFormat,
			Result:         r.Output.Result,
			QueryMetadata:  r.Output.QueryMetadata,
			ResultMetadata: r.Output.ResultMetadata,
			Artifact:       toolTestArtifactFromResult(r),
		},
		Progress: r.Progress,
	})
}

func toolTestArtifactFromResult(result ToolTestResult) *toolTestArtifactJSON {
	payload := result.Output.Artifact
	if payload == nil {
		return nil
	}
	artifactID := strings.TrimSpace(payload.ID)
	if artifactID == "" {
		artifactID = firstStringMapValue(payload.Metadata, "artifact_id", "artifactId", "full_artifact_id", "fullArtifactId")
	}
	if artifactID == "" {
		toolName := strings.TrimSpace(result.ToolName)
		if toolName == "" {
			toolName = "tool_test"
		}
		artifactID = "tool-test-" + strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
				return r
			}
			return '-'
		}, toolName) + "-artifact"
	}
	metadata := cloneStringAnyMap(payload.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["tool_test"] = true
	metadata["tool_name"] = strings.TrimSpace(result.ToolName)
	toolArgs := cloneStringAnyMap(result.Args)
	if toolArgs == nil {
		toolArgs = map[string]any{}
	}
	metadata["tool_args"] = toolArgs
	metadata["ok"] = result.Output.OK
	metadata["placeholder"] = false
	metadata["result_format"] = result.Output.ResultFormat
	metadata["raw_result"] = result.Output.Result
	if len(result.Output.QueryMetadata) > 0 {
		metadata["query_metadata"] = cloneStringAnyMap(result.Output.QueryMetadata)
	}
	if len(result.Output.ResultMetadata) > 0 {
		resultMetadata := map[string]any{}
		if existing, ok := firstMapMapValue(metadata, "result_metadata"); ok {
			for key, value := range existing {
				resultMetadata[key] = value
			}
		}
		for key, value := range cloneStringAnyMap(result.Output.ResultMetadata) {
			resultMetadata[key] = value
		}
		metadata["result_metadata"] = resultMetadata
	}
	metadata["tool_presentation"] = result.Presentation

	typ := strings.TrimSpace(payload.Type)
	switch typ {
	case "data", "code", "image", "file":
	default:
		typ = "code"
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = strings.TrimSpace(result.ToolName)
	}
	if title == "" {
		title = "Tool Test Artifact"
	}
	return &toolTestArtifactJSON{
		ID:       artifactID,
		Type:     typ,
		Title:    title,
		Content:  payload.Content,
		Language: payload.Language,
		Metadata: metadata,
	}
}

type ToolTestPermissionError struct {
	ToolName string                  `json:"tool_name"`
	Decision string                  `json:"decision"`
	Category string                  `json:"category"`
	Subject  store.PermissionSubject `json:"subject"`
	Reason   string                  `json:"reason,omitempty"`
}

func (e *ToolTestPermissionError) Error() string {
	if e == nil {
		return "tool test permission error"
	}
	switch e.Decision {
	case permissions.DecisionDeny:
		return fmt.Sprintf("tool %s test is denied by permissions", e.ToolName)
	default:
		return fmt.Sprintf("tool %s test requires approval", e.ToolName)
	}
}

type managerContextCheckpoint struct {
	Kind                  string              `json:"kind"`
	Version               int                 `json:"version"`
	ID                    string              `json:"id"`
	ThreadID              string              `json:"thread_id"`
	RunID                 *string             `json:"run_id,omitempty"`
	SourceMessageSeq      int64               `json:"source_message_seq"`
	SourceMessageID       string              `json:"source_message_id,omitempty"`
	ReplacementHistory    []types.ChatMessage `json:"replacement_history"`
	Summary               string              `json:"summary,omitempty"`
	SummaryMessageIDs     []string            `json:"summary_message_ids,omitempty"`
	ReferencedArtifactIDs []string            `json:"referenced_artifact_ids,omitempty"`
	TokenEstimate         int                 `json:"token_estimate"`
	CharsPerToken         int                 `json:"chars_per_token"`
	Model                 string              `json:"model,omitempty"`
	ProviderID            string              `json:"provider_id,omitempty"`
	Plan                  RunContextPlan      `json:"plan"`
	CreatedAt             time.Time           `json:"created_at"`
}

func applyRunDatasourceOverrides(base config.AppSettings, overrides *config.DatasourceConnections, overrideMaps ...map[string]any) config.AppSettings {
	out := base.Clone()

	mergedOverrides := datasourceEnabledOverridesFromConnections(overrides)
	if mergedOverrides == nil {
		mergedOverrides = map[string]any{}
	}
	for _, raw := range overrideMaps {
		for key, value := range raw {
			name := normalizeDatasourceName(key)
			if name == "" {
				continue
			}
			mergedOverrides[name] = value
		}
	}
	if len(mergedOverrides) == 0 {
		return out
	}

	if out.DataSources == nil {
		out.DataSources = &config.DatasourceConnections{}
	}

	for datasource, rawOverride := range mergedOverrides {
		override, _ := rawOverride.(map[string]any)
		if len(override) == 0 {
			continue
		}
		enabled, ok := extractDatasourceEnabledOverride(override)
		if !ok {
			continue
		}
		state := ensureDatasourceConnectionState(out.DataSources, datasource)
		if state == nil {
			continue
		}
		value := enabled
		state.Enabled = &value
	}

	return out
}

func providerIDForRunTarget(settings config.AppSettings, connectionID string) (string, error) {
	connectionID = strings.TrimSpace(connectionID)
	for _, cfg := range settings.ProviderConfigs {
		if strings.TrimSpace(cfg.ID) == connectionID {
			providerID := strings.TrimSpace(cfg.ProviderID)
			if providerID == "" {
				return "", fmt.Errorf("%w: provider connection has no providerId", store.ErrInvalid)
			}
			return providerID, nil
		}
	}
	activeID := ""
	if settings.ActiveProviderConfigID != nil {
		activeID = strings.TrimSpace(*settings.ActiveProviderConfigID)
	}
	if len(settings.ProviderConfigs) == 0 && (connectionID == "legacy-default" || connectionID == activeID) {
		providerID := strings.TrimSpace(settings.Provider.ProviderID)
		if providerID != "" {
			return providerID, nil
		}
	}
	return "", store.ErrNotFound
}

func (m *Manager) runtimePlanPolicy(ctx context.Context, run store.Run) (*types.RuntimePlanPolicy, error) {
	planID := strings.TrimSpace(run.PlanID)
	if planID == "" {
		// Legacy runs retain their existing adapter behavior. Capability-UX runs
		// always carry a finalized plan and therefore use strict authorization.
		return nil, nil
	}
	plan, err := m.store.GetRunPlanForRun(ctx, run.ID)
	if err != nil {
		return nil, err
	}
	policy := &types.RuntimePlanPolicy{PlanID: planID}
	for _, notice := range plan.Notices {
		switch strings.TrimSpace(notice.Code) {
		case "surface_switch":
			policy.AllowSurfaceSwitch = true
		case "fidelity_reduction":
			policy.AllowReducedFileFallback = true
		}
	}
	selected := make(map[string]store.PlanChoiceSelection, len(plan.Selections))
	for _, selection := range plan.Selections {
		selected[strings.TrimSpace(selection.IssueID)] = selection
	}
	for _, choice := range plan.Choices {
		selection, ok := selected[strings.TrimSpace(choice.IssueID)]
		if !ok || strings.TrimSpace(selection.OptionID) == "" {
			continue
		}
		switch strings.TrimSpace(choice.Code) {
		case "surface_switch":
			policy.AllowSurfaceSwitch = true
		case "fidelity_reduction":
			policy.AllowReducedFileFallback = true
		}
	}
	return policy, nil
}

func providerFingerprintForRunTarget(settings config.AppSettings, connectionID, model string) (string, bool) {
	connectionID = strings.TrimSpace(connectionID)
	model = strings.TrimSpace(model)
	for _, cfg := range settings.ProviderConfigs {
		if strings.TrimSpace(cfg.ID) != connectionID {
			continue
		}
		// A target may select a discovered model other than the connection's
		// saved default. Match preflight's exact-target snapshot by substituting
		// only that requested model while keeping every routing field live.
		if model != "" && model != strings.TrimSpace(cfg.Model) {
			cfg.Model = model
			return config.ProviderConfigFingerprint(cfg), true
		}
		if cfg.ConfigFingerprint != nil && strings.TrimSpace(*cfg.ConfigFingerprint) != "" {
			return strings.TrimSpace(*cfg.ConfigFingerprint), true
		}
		return config.ProviderConfigFingerprint(cfg), true
	}
	return "", false
}

func providerConnectionPolicyFingerprintForRunTarget(settings config.AppSettings, connectionID, model string) (string, bool) {
	connectionID = strings.TrimSpace(connectionID)
	model = strings.TrimSpace(model)
	for _, cfg := range settings.ProviderConfigs {
		if strings.TrimSpace(cfg.ID) != connectionID {
			continue
		}
		if model != "" {
			cfg.Model = model
		}
		return runs.ProviderConnectionPolicyFingerprint(cfg), true
	}
	return "", false
}

func datasourceEnabledOverridesFromConnections(overrides *config.DatasourceConnections) map[string]any {
	if overrides == nil {
		return nil
	}
	out := map[string]any{}
	for _, datasource := range tools.DatasourceNames() {
		name := normalizeDatasourceName(datasource)
		if name == "" {
			continue
		}
		state := datasourceConnectionStateFromConnections(overrides, name)
		if state == nil || state.Enabled == nil {
			continue
		}
		out[name] = map[string]any{"enabled": *state.Enabled}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func extractDatasourceEnabledOverride(raw map[string]any) (bool, bool) {
	for _, key := range []string{"enabled", "is_enabled"} {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			switch strings.TrimSpace(strings.ToLower(typed)) {
			case "true", "1", "yes", "on":
				return true, true
			case "false", "0", "no", "off":
				return false, true
			}
		}
	}
	return false, false
}

func runtimeTimeFromClientContext(req StartRunRequest) time.Time {
	fallback := time.Now().In(time.Local)
	rawNow := strings.TrimSpace(req.ClientNowISO)
	if rawNow == "" {
		return fallback
	}
	parsed, err := time.Parse(time.RFC3339Nano, rawNow)
	if err != nil {
		return fallback
	}
	if tz := strings.TrimSpace(req.ClientTimezone); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return parsed.In(loc)
		}
	}
	if req.ClientUTCOffsetMinutes != nil {
		offsetMinutes := *req.ClientUTCOffsetMinutes
		if offsetMinutes >= -24*60 && offsetMinutes <= 24*60 {
			offsetSeconds := offsetMinutes * 60
			return parsed.In(time.FixedZone(formatUTCOffset(offsetSeconds), offsetSeconds))
		}
	}
	return parsed
}

func NewManager(st store.Store, settings *config.Store, pf *providers.Factory, tr *tools.Registry, ps *projects.Store, bq *bigquery.Handler, dbk *databricks.Handler, tri *trino.Handler, cfgDir string, opts *Options) *Manager {
	if st == nil {
		panic("optionc.NewManager: store is required")
	}
	if settings == nil {
		panic("optionc.NewManager: settings is required")
	}
	if pf == nil {
		panic("optionc.NewManager: providers factory is required")
	}
	if tr == nil {
		panic("optionc.NewManager: tools registry is required")
	}
	if ps == nil {
		panic("optionc.NewManager: projects store is required")
	}
	if strings.TrimSpace(cfgDir) == "" {
		panic("optionc.NewManager: cfgDir is required")
	}
	o := Options{}
	if opts != nil {
		o = *opts
	}
	if o.MaxConcurrentRuns <= 0 {
		o.MaxConcurrentRuns = DefaultMaxConcurrentRuns
	}
	if o.MaxHistoryMessages <= 0 {
		o.MaxHistoryMessages = DefaultMaxHistoryMessages
	}
	if o.DeltaEmitInterval <= 0 {
		o.DeltaEmitInterval = 50 * time.Millisecond
	}
	if o.BufferSize <= 0 {
		o.BufferSize = 2048
	}
	if o.ApprovalWaitTimeout <= 0 {
		o.ApprovalWaitTimeout = defaultApprovalWaitTimeout
	}
	toolCompaction := o.ToolResultCompactionStrategy
	if toolCompaction == nil {
		toolCompaction = NewBudgetAwareToolResultCompactionStrategy(nil)
	}
	toolLoopGuard := o.ToolLoopGuardFactory
	if toolLoopGuard == nil {
		toolLoopGuard = NewMonotonicToolLoopGuardFactory(nil)
	}

	m := &Manager{
		store:      st,
		settings:   settings,
		providers:  pf,
		tools:      tr,
		projects:   ps,
		bigquery:   bq,
		databricks: dbk,
		trino:      tri,
		snowflake:  o.SnowflakeHTTP,
		cfgDir:     cfgDir,

		maxConcurrent: o.MaxConcurrentRuns,
		maxHistory:    o.MaxHistoryMessages,
		deltaInterval: o.DeltaEmitInterval,
		bufferSize:    o.BufferSize,
		contextPlannerForSnapshot: func(snapshot providers.DispatchSnapshot) *ContextPlanner {
			return NewContextPlanner(newContextSummarizerForSnapshot(pf, snapshot))
		},
		queryJobs:      o.QueryJobs,
		longHorizon:    o.LongHorizon,
		resumeClaimant: "runmgr_" + strings.ReplaceAll(types.NewUUID(), "-", ""),
		toolCompaction: toolCompaction,
		toolLoopGuard:  toolLoopGuard,
		permissions:    o.Permissions,
		sources:        o.Sources,
		usage:          o.Usage,
		approvalWait:   o.ApprovalWaitTimeout,
		localSearchAgent: &localSearchAgentService{
			registry: tr,
			providerFactory: func(ctx context.Context) (types.Provider, error) {
				if pf == nil {
					return nil, errors.New("local search agent provider factory is unavailable")
				}
				return pf.CreateProviderForContext(ctx, "", localSearchAgentHostedToolPolicy())
			},
		},
		queryDependencyFaults:     make(map[string]map[string]string),
		queryProtocolFaultChecked: make(map[string]bool),

		sem: make(chan struct{}, o.MaxConcurrentRuns),

		runs:           make(map[string]*runState),
		activeByThread: make(map[string]string),
	}
	return m
}

func (m *Manager) Subscribe(runID string) (<-chan SSEEvent, func(), error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil, errors.New("runId is required")
	}

	m.mu.Lock()
	st := m.runs[runID]
	m.mu.Unlock()
	if st == nil {
		return nil, nil, store.ErrNotFound
	}

	st.mu.Lock()
	backlog := append([]SSEEvent(nil), st.buffer...)
	if st.subs == nil {
		st.subs = make(map[int]chan SSEEvent)
	}
	st.durableEvents = true
	if st.done {
		st.mu.Unlock()
		if m.store != nil {
			if durable, durableErr := m.store.GetRun(context.Background(), runID); durableErr == nil {
				status := strings.ToLower(strings.TrimSpace(durable.Status))
				if status != store.RunStatusCompleted && status != store.RunStatusFailed && status != store.RunStatusCancelled {
					// This stale local state belongs to an earlier suspension/owner;
					// force the HTTP layer onto the shared durable-event tail.
					return nil, nil, store.ErrNotFound
				}
			}
		}
		// One-shot channel for already-completed runs (includes backlog).
		ch := make(chan SSEEvent, len(backlog)+1)
		for _, ev := range backlog {
			ch <- ev
		}
		close(ch)
		return ch, func() {}, nil
	}
	id := st.nextSub
	st.nextSub++
	ch := make(chan SSEEvent, len(backlog)+256)
	// Maintain per-subscriber FIFO by registering and replaying while st.mu is held.
	// This guarantees backlog delivery is fully enqueued before live broadcasts can send.
	st.subs[id] = ch
	for _, ev := range backlog {
		ch <- ev
	}
	st.mu.Unlock()

	cancel := func() {
		st.mu.Lock()
		c := st.subs[id]
		delete(st.subs, id)
		st.mu.Unlock()
		if c != nil {
			close(c)
		}
	}
	return ch, cancel, nil
}

func (m *Manager) Cancel(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return errors.New("runId is required")
	}
	m.mu.Lock()
	st := m.runs[runID]
	m.mu.Unlock()
	if m.longHorizon != nil {
		if _, err := m.cancelRunDurably(context.Background(), runID); err != nil {
			return err
		}
	} else if st == nil {
		return store.ErrNotFound
	}

	if st != nil {
		st.mu.Lock()
		cancel := st.cancel
		st.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	if m.permissions != nil {
		if canceled, err := m.permissions.CancelRunApprovalRequests(context.Background(), runID); err == nil {
			for _, approval := range canceled {
				m.broadcastApproval(runID, approval)
			}
		}
	} else if m.store != nil {
		_ = m.store.CancelRunApprovalRequests(context.Background(), runID)
	}
	return nil
}

func (m *Manager) runIsTerminal(ctx context.Context, runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return false
	}
	run, err := m.store.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(run.Status)) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func (m *Manager) GetQueryJobStatus(ctx context.Context, jobID, threadID, ownerToken string) (map[string]any, error) {
	return m.queryJobOp(ctx, "status", jobID, threadID, ownerToken, "", "")
}

// GetQueryJobStatusForRun returns the concrete job status with its identity
// resolved through the requesting run's dependency ledger. One durable query
// job can be observed by more than one run, and therefore can have a different
// query_obligation_id in each run. The job record's owning run is not an
// authoritative substitute for this lookup.
func (m *Manager) GetQueryJobStatusForRun(
	ctx context.Context,
	jobID, threadID, ownerToken, runID, queryObligationID string,
) (map[string]any, error) {
	return m.queryJobOp(ctx, "status", jobID, threadID, ownerToken, runID, queryObligationID)
}

func (m *Manager) CancelQueryJob(ctx context.Context, jobID, threadID, ownerToken string) (map[string]any, error) {
	return m.queryJobOp(ctx, "cancel", jobID, threadID, ownerToken, "", "")
}

func (m *Manager) queryJobOp(
	ctx context.Context,
	op, jobID, threadID, ownerToken, runID, requestedObligationID string,
) (map[string]any, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, errors.New("jobId is required")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, errors.New("threadId is required")
	}
	ownerToken = strings.TrimSpace(ownerToken)
	if ownerToken == "" {
		return nil, errors.New("owner token is required")
	}
	if m.queryJobs == nil {
		return nil, ErrQueryJobsUnavailable
	}
	if _, err := m.store.GetThread(ctx, threadID); err != nil {
		return nil, err
	}

	payload, err := invokeQueryJobsRegistry(ctx, m.queryJobs, op, jobID, ownerToken, threadID)
	if err != nil {
		return nil, err
	}
	if len(payload) == 0 {
		return nil, store.ErrNotFound
	}
	if !queryJobMatchesThread(payload, threadID) {
		return nil, ErrQueryJobThreadScope
	}
	if !queryJobMatchesOwnerToken(payload, ownerToken) {
		return nil, ErrQueryJobUnauthorized
	}
	// Registry metadata describes the concrete job and its original owning
	// run. Recursively remove its run-scoped retry identity before resolving
	// the requesting run's canonical identity below; arbitrary nested metadata
	// must not smuggle an old qob/run or internal lifecycle marker across runs.
	if clean, ok := stripQueryRegistryRunScopedFields(payload).(map[string]any); ok {
		payload = clean
	}

	if strings.EqualFold(strings.TrimSpace(op), "status") {
		resolvedRunID, resolvedObligationID, err := m.resolveQueryStatusIdentity(
			ctx, threadID, runID, jobID, requestedObligationID,
		)
		if err != nil {
			return nil, err
		}
		if resolvedRunID != "" {
			payload["run_id"] = resolvedRunID
			if resolvedObligationID != "" {
				payload["query_obligation_id"] = resolvedObligationID
			}
		}
	}
	if _, ok := payload["job_id"]; !ok {
		payload["job_id"] = jobID
	}
	if _, ok := payload["thread_id"]; !ok {
		payload["thread_id"] = threadID
	}
	normalizeProviderJobMetadata(payload)
	if strings.TrimSpace(strings.ToLower(op)) == "status" {
		if artifact, materialized, err := m.materializeCompletedQueryResultArtifact(ctx, jobID, threadID, ownerToken); err != nil {
			log.Printf("query job %s: failed to materialize completed result artifact: %v", jobID, err)
			payload["results_ready"] = false
			payload["results_error"] = strings.TrimSpace(err.Error())
		} else if materialized {
			payload["artifact_id"] = artifact.ID
			payload["full_artifact_id"] = artifact.ID
			payload["result_artifact_id"] = artifact.ID
			payload["results_ready"] = true
			if rowCount, ok := firstIntMapValue(artifact.Metadata, "row_count_total"); ok {
				payload["row_count_total"] = rowCount
			}
		}
	}
	return payload, nil
}

func (m *Manager) resolveQueryStatusIdentity(
	ctx context.Context,
	threadID, runID, jobID, requestedObligationID string,
) (string, string, error) {
	runID = strings.TrimSpace(runID)
	requestedObligationID = strings.TrimSpace(requestedObligationID)
	if runID == "" {
		if requestedObligationID != "" {
			return "", "", ErrQueryJobRunScope
		}
		// Compatibility for callers that have not yet adopted run-scoped
		// polling. No obligation is returned without a ledger context.
		return "", "", nil
	}
	if requestedObligationID != "" {
		if err := store.ValidateQueryObligationID(requestedObligationID); err != nil {
			return "", "", fmt.Errorf("invalid query obligation identity: %w", store.ErrConflict)
		}
	}
	if m.longHorizon == nil {
		return "", "", ErrQueryJobsUnavailable
	}
	run, err := m.store.GetRun(ctx, runID)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(run.ThreadID) != strings.TrimSpace(threadID) {
		return "", "", ErrQueryJobRunScope
	}
	dependencies, err := m.longHorizon.ListRunQueryDependencies(ctx, runID)
	if err != nil {
		return "", "", err
	}
	jobID = strings.TrimSpace(jobID)
	for _, dependency := range dependencies {
		if strings.TrimSpace(dependency.JobID) != jobID {
			continue
		}
		obligationID := strings.TrimSpace(dependency.ObligationID)
		if obligationID == "" {
			// Mixed-version repositories can contain a dependency created before
			// obligation ids existed. Resolve it from durable lineage; never
			// invent an identity from request input.
			obligationID, err = store.DeriveQueryObligationID(ctx, m.longHorizon, runID, jobID)
			if err != nil {
				return "", "", err
			}
			if !dependency.Detached {
				// Active legacy rows can be hydrated monotonically. Generic upsert
				// intentionally leaves detached audit rows immutable, so their
				// derived identity is used only for lineage checks and is not
				// written back to the detached row.
				dependency.ObligationID = obligationID
				dependency.UpdatedAt = time.Now().UTC()
				repaired, repairErr := m.longHorizon.UpsertRunQueryDependency(ctx, dependency)
				if repairErr != nil {
					return "", "", repairErr
				}
				obligationID = strings.TrimSpace(repaired.ObligationID)
			}
		}
		if err := store.ValidateQueryObligationID(obligationID); err != nil {
			return "", "", fmt.Errorf("query dependency has invalid obligation identity: %w", store.ErrConflict)
		}
		if requestedObligationID != "" && requestedObligationID != obligationID {
			return "", "", ErrQueryJobRunScope
		}
		for _, candidate := range dependencies {
			if !candidate.RequiredForCompletion || candidate.Detached ||
				strings.EqualFold(strings.TrimSpace(candidate.ValidationState), store.QueryValidationValidated) {
				continue
			}
			candidateObligationID := strings.TrimSpace(candidate.ObligationID)
			if candidateObligationID == "" {
				candidateObligationID, err = store.DeriveQueryObligationID(ctx, m.longHorizon, runID, candidate.JobID)
				if err != nil {
					return "", "", err
				}
			}
			if candidateObligationID == obligationID {
				return runID, obligationID, nil
			}
		}
		// The job remains observable, but its validated obligation is settled
		// and no longer grants retry authority.
		return runID, "", nil
	}
	return "", "", ErrQueryJobRunScope
}

func (m *Manager) cancelActiveQueryJobsForThread(threadID string) []string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" || m.queryJobs == nil {
		return nil
	}
	reg, ok := m.queryJobs.(*jobs.Registry)
	if !ok || reg == nil {
		return nil
	}
	return reg.CancelActiveByThread(threadID)
}

func usageAttributionForRun(run store.Run, threadID string, snapshot providers.DispatchSnapshot) usage.Attribution {
	connectionID := strings.TrimSpace(run.ConnectionID)
	if connectionID == "" {
		connectionID = snapshot.ConnectionID
	}
	attribution := usage.Attribution{
		RunID: run.ID, ThreadID: strings.TrimSpace(threadID), PlanID: strings.TrimSpace(run.PlanID),
		Operation: usage.OperationChat, ConnectionID: connectionID,
		ConfigFingerprint: snapshot.ConfigFingerprint, PlannedAPISurface: snapshot.PlannedAPISurface,
	}
	if run.ProfileID != nil {
		attribution.ProfileID = strings.TrimSpace(*run.ProfileID)
		if attribution.ProfileID != "" {
			attribution.ConnectionID = ""
		}
	}
	return attribution
}

func (m *Manager) StartRun(ctx context.Context, req StartRunRequest) (StartRunResult, error) {
	threadID := strings.TrimSpace(req.ThreadID)
	if threadID == "" {
		return StartRunResult{}, errors.New("threadId is required")
	}
	content := strings.TrimSpace(req.Content)
	req.ImageAttachments = types.CloneImageAttachments(req.ImageAttachments)
	req.FileAttachments = types.CloneFileAttachments(req.FileAttachments)
	if content == "" && len(req.ImageAttachments) == 0 && len(req.FileAttachments) == 0 {
		return StartRunResult{}, errors.New("content is required")
	}

	// Enforce a single active/queued run per thread.
	// We reserve the thread up-front to avoid a race where two StartRun() calls arrive
	// before executeRun() has a chance to mark the thread as active.
	resv := "starting:" + types.NewUUID()
	for {
		m.mu.Lock()
		if rid := strings.TrimSpace(m.activeByThread[threadID]); rid != "" {
			m.mu.Unlock()
			if strings.HasPrefix(rid, "starting:") {
				rid = ""
			}
			if rid != "" && m.runIsTerminal(ctx, rid) {
				m.mu.Lock()
				if cur := m.activeByThread[threadID]; cur == rid {
					delete(m.activeByThread, threadID)
				}
				m.mu.Unlock()
				continue
			}
			return StartRunResult{}, &ActiveRunConflictError{RunID: rid}
		}
		m.activeByThread[threadID] = resv
		m.mu.Unlock()
		break
	}
	releaseReservation := func() {
		m.mu.Lock()
		if cur := m.activeByThread[threadID]; cur == resv {
			delete(m.activeByThread, threadID)
		}
		m.mu.Unlock()
	}
	defer releaseReservation()

	thread, err := m.store.GetThread(ctx, threadID)
	if err != nil {
		return StartRunResult{}, err
	}
	purpose := strings.TrimSpace(strings.ToLower(req.Purpose))
	if purpose == "" {
		purpose = "analysis"
	}
	if purpose != "analysis" && purpose != "search" {
		return StartRunResult{}, errors.New("purpose must be analysis or search")
	}
	searchDepth := normalizeSearchDepth(req.SearchDepth)
	var requestedPlan *store.RunPlan
	if planID := strings.TrimSpace(req.PlanID); planID != "" {
		if req.Target == nil || strings.TrimSpace(req.Target.ConnectionID) == "" || strings.TrimSpace(req.Target.Model) == "" {
			return StartRunResult{}, fmt.Errorf("%w: planId requires target", store.ErrInvalid)
		}
		persistedPlan, getPlanErr := m.store.GetRunPlan(ctx, planID)
		if getPlanErr != nil {
			if errors.Is(getPlanErr, store.ErrNotFound) {
				return StartRunResult{}, store.ErrPlanStale
			}
			return StartRunResult{}, getPlanErr
		}
		requestedPlan = &persistedPlan
		if persistedPlan.Status == store.RunPlanStatusFinalized {
			// A finalized plan is the idempotency boundary for run creation. Ask
			// the store to validate the complete replay identity, then return the
			// original run before reading mutable provider policy, appending the
			// message, or launching execution again.
			replayed, _, replayErr := m.store.FinalizeRunPlanAndCreateRun(ctx, store.FinalizeRunPlanRequest{
				PlanID: planID, ExpectedThreadID: thread.ID, ExpectedTarget: *req.Target,
				ExpectedRequirementsHash:  strings.TrimSpace(req.RequirementsHash),
				ExpectedConfigFingerprint: strings.TrimSpace(req.ConfigFingerprint),
				Selections:                req.ChoiceSelections,
				Run: store.CreateRunParams{
					ThreadID: thread.ID, ConnectionID: strings.TrimSpace(req.Target.ConnectionID), Model: strings.TrimSpace(req.Target.Model),
				},
				Now: time.Now().UTC(),
			})
			if replayErr != nil {
				return StartRunResult{}, replayErr
			}
			if replayed.Status == store.RunStatusQueued && m.longHorizon != nil &&
				!replayed.UpdatedAt.Add(finalizedQueuedRunReplayGrace).After(time.Now().UTC()) {
				// A finalized queued run has no model checkpoint and no worker lease.
				// Claim that boundary with the same durable fence used by initial
				// execution, then park it for an explicit retry/alternative plan. A
				// concurrent worker claim wins the CAS and is returned untouched.
				now := time.Now().UTC()
				claimToken := m.resumeClaimant + "_replay_" + strings.ReplaceAll(types.NewUUID(), "-", "")
				claimed, acquired, claimErr := m.longHorizon.ClaimRunWake(
					ctx, replayed.ID, replayed.Revision, claimToken, now, now.Add(runWakeLeaseTTL),
				)
				if claimErr != nil {
					return StartRunResult{}, claimErr
				}
				if acquired {
					if err := m.parkClaimedInitialRun(ctx, claimed.ID, claimToken); err != nil && !errors.Is(err, store.ErrConflict) {
						return StartRunResult{}, err
					}
				}
				if refreshed, getErr := m.store.GetRun(ctx, replayed.ID); getErr == nil {
					replayed = refreshed
				} else {
					return StartRunResult{}, getErr
				}
			}
			// Running replays are owned by the durable execution lease, not this
			// process's in-memory run map. A request routed to another instance
			// must never fail a genuinely live worker. Lease-expiry recovery parks
			// unsafe pre-checkpoint boundaries for a deliberate alternative plan
			// without abandoning the task.
			return StartRunResult{Thread: thread, Run: replayed}, nil
		}
	}
	settingsAtRequest := m.settings.Get()
	settingsAtRequest = applyRunDatasourceOverrides(settingsAtRequest, req.DataSources, req.DataSourceOverrides)
	req.DataSources = settingsAtRequest.DataSources

	// Resolve the canonical target. Its immutable dispatch snapshot is captured
	// only after live fingerprint validation below and before atomic finalization.
	st := settingsAtRequest
	providerID := strings.TrimSpace(st.Provider.ProviderID)
	model := strings.TrimSpace(st.Provider.Model)
	connectionID := ""
	if req.Target != nil {
		target := store.RunTarget{ConnectionID: strings.TrimSpace(req.Target.ConnectionID), Model: strings.TrimSpace(req.Target.Model)}
		if strings.TrimSpace(req.PlanID) == "" || target.ConnectionID == "" || target.Model == "" {
			return StartRunResult{}, fmt.Errorf("%w: a run target requires a preflight plan", store.ErrInvalid)
		}
		resolvedProviderID, err := providerIDForRunTarget(st, target.ConnectionID)
		if err != nil {
			return StartRunResult{}, err
		}
		providerID = resolvedProviderID
		model = target.Model
		connectionID = target.ConnectionID
		selectedID := target.ConnectionID
		st.ActiveProviderConfigID = &selectedID
		settingsAtRequest = st
	}
	if providerID == "" {
		return StartRunResult{}, errors.New("no provider is configured")
	}
	if model == "" {
		return StartRunResult{}, errors.New("no provider model is configured")
	}
	var profileID *string
	if providerID == "compatible" && (connectionID == "" || connectionID == "legacy-default") && st.Provider.ProfileID != nil && strings.TrimSpace(*st.Provider.ProfileID) != "" {
		p := strings.TrimSpace(*st.Provider.ProfileID)
		profileID = &p
	}
	systemPrompt := strings.TrimSpace(st.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = "You are a helpful AI assistant."
	}
	if purpose == "search" {
		systemPrompt = searchSystemPrompt(searchDepth)
	}
	maxIters := 200
	if st.MaxToolIterations != nil && *st.MaxToolIterations > 0 {
		maxIters = *st.MaxToolIterations
	}
	if req.MaxIterationsOverride != nil && *req.MaxIterationsOverride > 0 {
		maxIters = *req.MaxIterationsOverride
	}

	var run store.Run
	var dispatchSnapshot providers.DispatchSnapshot
	if strings.TrimSpace(req.PlanID) != "" {
		if requestedPlan == nil {
			return StartRunResult{}, store.ErrPlanStale
		}
		persistedPlan := *requestedPlan
		connectionPolicies, validateErr := runs.ValidateSubmission(
			persistedPlan, *req.Target, req.RequirementsHash, req.ConfigFingerprint, req.ChoiceSelections, time.Now().UTC(),
		)
		if validateErr != nil {
			return StartRunResult{}, validateErr
		}
		plannedConnectionPolicyFingerprint, plannedThreadPolicyFingerprint := runs.PlanPolicyFingerprints(persistedPlan)
		if plannedConnectionPolicyFingerprint == "" || plannedThreadPolicyFingerprint == "" ||
			store.AttachmentPoliciesFingerprint(thread.AttachmentPolicies) != plannedThreadPolicyFingerprint {
			return StartRunResult{}, store.ErrPlanStale
		}
		policyBatch := make([]config.ProviderAttachmentPolicy, 0, len(connectionPolicies))
		for _, policy := range connectionPolicies {
			policyBatch = append(policyBatch, config.ProviderAttachmentPolicy{
				Class: policy.Class, Action: policy.Action, APISurface: policy.APISurface,
			})
		}
		validatedSettings, createdConnectionPolicies, policyErr := m.settings.ApplyProviderAttachmentPoliciesCAS(
			connectionID, req.Target.Model, strings.TrimSpace(persistedPlan.ConfigFingerprint),
			plannedConnectionPolicyFingerprint, policyBatch,
		)
		if policyErr != nil {
			if errors.Is(policyErr, config.ErrProviderConfigChanged) {
				return StartRunResult{}, store.ErrPlanStale
			}
			return StartRunResult{}, policyErr
		}
		// Provider settings and run plans live in separate stores. This is
		// deliberately narrow cross-store compensation, not a distributed
		// transaction: remember only policies this submission actually created,
		// then remove them if the run-plan store rejects finalization.
		rollbackConnectionPolicies := func() {
			for index := len(createdConnectionPolicies) - 1; index >= 0; index-- {
				policy := createdConnectionPolicies[index]
				if _, cleanupErr := m.settings.DeleteProviderAttachmentPolicy(connectionID, policy.ID); cleanupErr != nil {
					log.Printf("run plan %s: failed to compensate provider attachment policy %s after run creation failure: %v", persistedPlan.ID, policy.ID, cleanupErr)
				}
			}
		}
		dispatchSnapshot, err = m.providers.SnapshotConnection(
			validatedSettings, connectionID, req.Target.Model, strings.TrimSpace(persistedPlan.ConfigFingerprint), "",
		)
		if err != nil {
			rollbackConnectionPolicies()
			return StartRunResult{}, store.ErrPlanStale
		}
		run, _, err = m.store.FinalizeRunPlanAndCreateRun(ctx, store.FinalizeRunPlanRequest{
			PlanID: strings.TrimSpace(req.PlanID), ExpectedThreadID: thread.ID,
			ExpectedTarget: *req.Target, ExpectedRequirementsHash: strings.TrimSpace(req.RequirementsHash),
			ExpectedConfigFingerprint:       strings.TrimSpace(req.ConfigFingerprint),
			ExpectedThreadPolicyFingerprint: plannedThreadPolicyFingerprint,
			Selections:                      req.ChoiceSelections,
			Run: store.CreateRunParams{
				ThreadID: thread.ID, ProviderID: providerID, ConnectionID: connectionID, Model: model,
				ProfileID: profileID, SystemPrompt: systemPrompt, MaxIterations: maxIters,
			},
			Now: time.Now().UTC(),
		})
		if err != nil {
			rollbackConnectionPolicies()
		}
	} else {
		dispatchSnapshot, err = m.providers.SnapshotConnection(settingsAtRequest, connectionID, model, "", "")
		if err != nil {
			return StartRunResult{}, err
		}
		run, err = m.store.CreateRun(ctx, thread.ID, providerID, model, profileID, systemPrompt, maxIters)
	}
	if err != nil {
		return StartRunResult{}, err
	}
	if strings.TrimSpace(req.PlanID) != "" {
		if refreshed, getErr := m.store.GetThread(ctx, thread.ID); getErr == nil {
			thread = refreshed
		}
	}
	run.Purpose = purpose
	if purpose != "analysis" {
		if err := m.store.UpdateRunStatus(ctx, run.ID, run.Status, map[string]any{"purpose": purpose}); err != nil {
			log.Printf("run %s: failed to persist initial purpose update: %v", run.ID, err)
		}
	}

	imageArtifactIDs := m.persistImageAttachmentsAsArtifacts(ctx, thread.ID, run.ID, req.ImageAttachments)
	if len(imageArtifactIDs) > 0 {
		req.ReferencedArtifactIDs = append(req.ReferencedArtifactIDs, imageArtifactIDs...)
	}
	if fileArtifactIDs := referencedFileAttachmentArtifactIDs(req.FileAttachments); len(fileArtifactIDs) > 0 {
		req.ReferencedArtifactIDs = append(req.ReferencedArtifactIDs, fileArtifactIDs...)
	}

	cleanArtifactRefs, cleanMessageRefs, droppedArtifactRefs, droppedMessageRefs := m.sanitizeReferencedIDs(
		ctx,
		thread.ID,
		req.ReferencedArtifactIDs,
		req.ReferencedMessageIDs,
	)
	req.ReferencedArtifactIDs = cleanArtifactRefs
	req.ReferencedMessageIDs = cleanMessageRefs
	req.ReferencedThreadIDs = dedupeStringSlice(req.ReferencedThreadIDs)
	req.ImageAttachments = m.hydrateReferencedImageAttachments(ctx, thread.ID, req.ReferencedArtifactIDs, req.ImageAttachments)
	req.FileAttachments = m.hydrateReferencedFileAttachments(ctx, thread.ID, req.FileAttachments)
	req.FileAttachments = m.hydrateFileAttachmentInlineData(ctx, thread.ID, req.FileAttachments)
	nativeFileInputByArtifactID := fileAttachmentNativeInputAvailability(settingsAtRequest, providerID, profileID, req.FileAttachments)
	nativeImageInputByArtifactID := fileAttachmentNativeImageInputAvailability(settingsAtRequest, providerID, profileID, req.FileAttachments)
	for _, attachment := range req.ImageAttachments {
		artifactID := strings.TrimSpace(attachment.ArtifactID)
		if artifactID != "" && strings.TrimSpace(attachment.DataURI) != "" {
			nativeImageInputByArtifactID[artifactID] = true
		}
	}
	req.HiddenContext = appendHiddenContext(req.HiddenContext, buildFileAttachmentTurnContext(req.FileAttachments, fileAttachmentTurnContextOptions{
		NativeInputAvailable: func(attachment types.FileAttachment) bool {
			artifactID := strings.TrimSpace(attachment.ArtifactID)
			return nativeFileInputByArtifactID[artifactID] && !nativeImageInputByArtifactID[artifactID]
		},
		NativeImageInputAvailable: func(attachment types.FileAttachment) bool {
			return nativeImageInputByArtifactID[strings.TrimSpace(attachment.ArtifactID)]
		},
	}))
	// Hidden context can contain a client-rendered snapshot of a pinned message
	// from an older run. Treat it as historical prose, never as current-run
	// query authority: otherwise a stale obligation or concrete replacement
	// target is persisted on the new user message and bypasses history
	// sanitization (which intentionally trusts messages owned by this run).
	req.HiddenContext = redactStaleQueryRetryText(req.HiddenContext)
	if len(droppedArtifactRefs) > 0 || len(droppedMessageRefs) > 0 {
		log.Printf(
			"run %s thread %s: dropped out-of-scope refs artifacts=%v messages=%v",
			run.ID,
			thread.ID,
			droppedArtifactRefs,
			droppedMessageRefs,
		)
	}

	// Persist the user message now that we have a run ID.
	referencedArtifacts := append([]string(nil), req.ReferencedArtifactIDs...)
	referencedMessages := append([]string(nil), req.ReferencedMessageIDs...)
	referencedThreads := append([]string(nil), req.ReferencedThreadIDs...)
	var hiddenContext *string
	if c := strings.TrimSpace(req.HiddenContext); c != "" {
		hiddenContext = &c
	}
	userMsg, err := m.store.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		ID:                    types.NewUUID(),
		Role:                  types.RoleUser,
		Content:               &content,
		ReferencedMessageIDs:  referencedMessages,
		ReferencedThreadIDs:   referencedThreads,
		ReferencedArtifactIDs: referencedArtifacts,
		HiddenContext:         hiddenContext,
	})
	if err != nil {
		ended := time.Now().UTC()
		if failErr := m.updateRunStatusWithFallback(context.Background(), run.ID, "failed", map[string]any{
			"error_code": "PERSISTENCE_ERROR", "error_message": "failed to persist the initial user message", "ended_at": ended,
		}); failErr != nil {
			log.Printf("run %s: failed to terminalize after initial message persistence error: %v", run.ID, failErr)
		}
		return StartRunResult{}, err
	}
	// The committed user-message sequence is the durable generation boundary
	// for this thread. Revoke only older model-loop continuations before this
	// run can launch; their warehouse jobs remain running and monitored.
	if err := m.disarmPriorContinuationsForNewRun(ctx, run, userMsg.Seq); err != nil {
		ended := time.Now().UTC()
		if failErr := m.updateRunStatusWithFallback(context.Background(), run.ID, store.RunStatusFailed, map[string]any{
			"error_code": "SUPERSESSION_ERROR", "error_message": "failed to fence an older run continuation", "ended_at": ended,
		}); failErr != nil {
			log.Printf("run %s: failed to terminalize after continuation fencing error: %v", run.ID, failErr)
		}
		return StartRunResult{}, fmt.Errorf("fence older run continuation: %w", err)
	}
	initialExecutionClaimToken := ""
	initialExecutionPreclaimed := false
	if m.longHorizon != nil {
		// Own the execution before returning or launching asynchronous planning.
		// This closes the finalized-plan replay window where another process could
		// otherwise mistake a healthy but not-yet-scheduled queued run for a crash.
		now := time.Now().UTC()
		initialExecutionClaimToken = m.resumeClaimant + "_run_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		claimed, acquired, claimErr := m.longHorizon.ClaimRunWake(
			ctx, run.ID, run.Revision, initialExecutionClaimToken, now, now.Add(runWakeLeaseTTL),
		)
		if claimErr != nil {
			return StartRunResult{}, fmt.Errorf("claim initial run execution: %w", claimErr)
		}
		if !acquired {
			// A concurrent replay/recovery won the durable fence. Return its
			// authoritative state and never launch a second model execution.
			current, getErr := m.store.GetRun(ctx, run.ID)
			if getErr != nil {
				return StartRunResult{}, getErr
			}
			return StartRunResult{Thread: thread, Run: current}, nil
		}
		run = claimed
		initialExecutionPreclaimed = true
	}

	// Create run state now so the SSE endpoint can attach immediately.
	m.mu.Lock()
	stRun := &runState{threadID: thread.ID, subs: make(map[int]chan SSEEvent), doneCh: make(chan struct{}), durableEvents: true}
	m.runs[run.ID] = stRun
	// Replace the temporary reservation with the real run ID.
	// If this doesn't match, we don't override it; the execution path will fail the run.
	if cur := m.activeByThread[threadID]; cur == resv {
		m.activeByThread[threadID] = run.ID
	}
	m.mu.Unlock()

	usageAttribution := usageAttributionForRun(run, thread.ID, dispatchSnapshot)
	runCtx := providers.WithDispatchSnapshot(usage.WithAttribution(context.Background(), usageAttribution), dispatchSnapshot)
	runCtx, cancel := context.WithCancel(runCtx)
	stRun.mu.Lock()
	stRun.cancel = cancel
	stRun.mu.Unlock()

	req.SearchDepth = searchDepth
	go m.executeRun(
		runCtx, thread, run, userMsg.ID, req, dispatchSnapshot, nil,
		initialExecutionClaimToken, initialExecutionPreclaimed,
	)

	return StartRunResult{Thread: thread, Run: run}, nil
}

func (m *Manager) executeRun(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	userMsgID string,
	req StartRunRequest,
	dispatchSnapshot providers.DispatchSnapshot,
	resumeCheckpoint *store.ContinuationCheckpoint,
	continuationClaimToken string,
	initialExecutionPreclaimed bool,
) {
	executionCtx, cancelExecution := context.WithCancel(ctx)
	ctx = executionCtx
	leaseGuard := newRunWakeLeaseGuard(m.longHorizon, run.ID, runWakeLeaseTTL, cancelExecution)
	if continuationClaimToken != "" {
		leaseGuard.SetSupersessionValidator(func(validationCtx context.Context) (bool, error) {
			return m.validateRunContinuationGeneration(validationCtx, run.ID)
		})
		leaseGuard.Hold(continuationClaimToken, run.ResumeLeaseExpiresAt)
	}
	defer leaseGuard.Stop()
	defer cancelExecution()
	// Queueing and concurrency control.
	select {
	case m.sem <- struct{}{}:
		// acquired
	case <-ctx.Done():
		if leaseGuard.Lost() {
			m.mu.Lock()
			if cur := m.activeByThread[thread.ID]; cur == run.ID {
				delete(m.activeByThread, thread.ID)
			}
			m.mu.Unlock()
			m.finish(run.ID)
			return
		}
		// Cancelled while queued; mark and release the per-thread reservation.
		ended := time.Now().UTC()
		if _, err := m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusCancelled, continuationClaimToken, map[string]any{"ended_at": ended}); err != nil && !errors.Is(err, store.ErrConflict) {
			log.Printf("run %s: failed to update status to cancelled: %v", run.ID, err)
		}
		m.mu.Lock()
		if cur := m.activeByThread[thread.ID]; cur == run.ID {
			delete(m.activeByThread, thread.ID)
		}
		m.mu.Unlock()
		if b, err := json.Marshal(RunTerminalPayload{RunID: run.ID, Status: "cancelled", Timestamp: time.Now().UnixMilli()}); err == nil {
			m.broadcast(run.ID, SSEEvent{Event: EventRunTerminal, Data: b})
		}
		m.broadcast(run.ID, SSEEvent{Event: "final", Data: []byte(`{"answer":""}`)})
		m.finish(run.ID)
		return
	}
	modelSlotHeld := true
	releaseModelSlot := func() {
		if modelSlotHeld {
			<-m.sem
			modelSlotHeld = false
		}
	}
	acquireModelSlot := func() error {
		if modelSlotHeld {
			return nil
		}
		select {
		case m.sem <- struct{}{}:
			modelSlotHeld = true
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer releaseModelSlot()

	m.mu.Lock()
	// Enforce per-thread exclusive run execution.
	if existing := m.activeByThread[thread.ID]; strings.TrimSpace(existing) != "" && existing != run.ID {
		m.mu.Unlock()
		_, _ = m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusFailed, continuationClaimToken, map[string]any{
			"ended_at": time.Now().UTC(), "error_code": "CONFLICT", "error_message": "another run is active for this thread",
		})
		if b, err := json.Marshal(RunTerminalPayload{RunID: run.ID, Status: "failed", Code: "CONFLICT", Message: "another run is active for this thread", Timestamp: time.Now().UnixMilli()}); err == nil {
			m.broadcast(run.ID, SSEEvent{Event: EventRunTerminal, Data: b})
		}
		m.broadcast(run.ID, SSEEvent{Event: "error", Data: []byte(`{"message":"another run is active for this thread"}`)})
		m.broadcast(run.ID, SSEEvent{Event: "final", Data: []byte(`{"answer":""}`)})
		m.finish(run.ID)
		return
	}
	m.activeByThread[thread.ID] = run.ID
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if cur := m.activeByThread[thread.ID]; cur == run.ID {
			delete(m.activeByThread, thread.ID)
		}
		m.mu.Unlock()
	}()

	started := time.Now().UTC()
	if ctx.Err() != nil {
		if leaseGuard.Lost() {
			m.finish(run.ID)
			return
		}
		ended := time.Now().UTC()
		if _, err := m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusCancelled, continuationClaimToken, map[string]any{"ended_at": ended}); err != nil && !errors.Is(err, store.ErrConflict) {
			log.Printf("run %s: failed to update status to cancelled: %v", run.ID, err)
		}
		if b, err := json.Marshal(RunTerminalPayload{RunID: run.ID, Status: "cancelled", Timestamp: time.Now().UnixMilli()}); err == nil {
			m.broadcast(run.ID, SSEEvent{Event: EventRunTerminal, Data: b})
		}
		m.broadcast(run.ID, SSEEvent{Event: "final", Data: []byte(`{"answer":""}`)})
		m.finish(run.ID)
		return
	}

	emit := func(event string, payload any) {
		var b []byte
		if payload != nil {
			if jb, err := json.Marshal(payload); err == nil {
				b = jb
			}
		}
		m.broadcast(run.ID, SSEEvent{Event: event, Data: b})
	}
	emitDiagnostic := func(code string, message string) {
		message = strings.TrimSpace(message)
		if message == "" {
			return
		}
		emit(EventRunDiagnostic, RunDiagnosticPayload{
			Code:       strings.TrimSpace(code),
			Message:    message,
			Visibility: "debug",
			Timestamp:  time.Now().UnixMilli(),
		})
	}
	var flushThinkingDeltas func()
	thinkingOpen := false
	ensureThinkingEnd := func(summary *string) {
		if !thinkingOpen {
			return
		}
		emitThinkingEnd(flushThinkingDeltas, emit, summary)
		thinkingOpen = false
	}
	terminalErrorEmitted := false
	emitTerminalError := func(message string) {
		ensureThinkingEnd(nil)
		message = strings.TrimSpace(message)
		if message == "" {
			message = "The run could not be completed."
		}
		if terminalErrorEmitted {
			return
		}
		terminalErrorEmitted = true
		emit(EventRunTerminal, RunTerminalPayload{
			RunID:     run.ID,
			Status:    "failed",
			Code:      "FAILED",
			Message:   message,
			Timestamp: time.Now().UnixMilli(),
		})
		emit("error", map[string]any{"message": message})
	}
	emitTerminalFinal := func() {
		ensureThinkingEnd(nil)
		emit("final", map[string]any{"answer": ""})
	}
	emitTerminalCompleted := func() {
		ensureThinkingEnd(nil)
		emit(EventRunTerminal, RunTerminalPayload{
			RunID:     run.ID,
			Status:    "completed",
			Timestamp: time.Now().UnixMilli(),
		})
		emit("final", map[string]any{"answer": ""})
	}
	emitTerminalCancelled := func() {
		ensureThinkingEnd(nil)
		emit(EventRunTerminal, RunTerminalPayload{
			RunID:     run.ID,
			Status:    "cancelled",
			Timestamp: time.Now().UnixMilli(),
		})
		emit("final", map[string]any{"answer": ""})
	}

	var recoverySnapshot *continuationExecutionSnapshot
	if resumeCheckpoint != nil {
		decoded, snapshotErr := unmarshalContinuationExecutionSnapshot(resumeCheckpoint.ExecutionSnapshotJSON, run)
		if snapshotErr == nil {
			snapshotErr = validateContinuationCheckpointDigests(decoded, resumeCheckpoint)
		}
		if snapshotErr != nil {
			_ = m.transitionRunToWaitingUser(context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, continuationClaimToken)
			emit("run_waiting_user", map[string]any{
				"run_id": run.ID,
				"reason": "continuation execution snapshot is incompatible or corrupt: " + snapshotErr.Error(),
			})
			m.finish(run.ID)
			return
		}
		if strings.TrimSpace(decoded.Dispatch.ConnectionID) != strings.TrimSpace(dispatchSnapshot.ConnectionID) ||
			strings.TrimSpace(decoded.Dispatch.LegacyProfileID) != strings.TrimSpace(dispatchSnapshot.LegacyProfileID) ||
			strings.TrimSpace(decoded.Dispatch.ProviderID) != strings.TrimSpace(dispatchSnapshot.ProviderID) ||
			strings.TrimSpace(decoded.Dispatch.Model) != strings.TrimSpace(dispatchSnapshot.Model) ||
			strings.TrimSpace(decoded.Dispatch.ConfigFingerprint) != strings.TrimSpace(dispatchSnapshot.ConfigFingerprint) ||
			strings.TrimSpace(decoded.Dispatch.PlannedAPISurface) != strings.TrimSpace(dispatchSnapshot.PlannedAPISurface) {
			_ = m.transitionRunToWaitingUser(context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, continuationClaimToken)
			emit("run_waiting_user", map[string]any{
				"run_id": run.ID,
				"reason": "continuation dispatch identity no longer matches its durable checkpoint",
			})
			m.finish(run.ID)
			return
		}
		recoverySnapshot = decoded
	}

	// Determine project context.
	projectID := ""
	if recoverySnapshot != nil {
		projectID = strings.TrimSpace(recoverySnapshot.ProjectID)
	} else if req.ProjectIDOverride != nil && strings.TrimSpace(*req.ProjectIDOverride) != "" {
		projectID = strings.TrimSpace(*req.ProjectIDOverride)
	} else if thread.ProjectID != nil && strings.TrimSpace(*thread.ProjectID) != "" {
		projectID = strings.TrimSpace(*thread.ProjectID)
	}

	// Load history (excluding ui-hidden messages) and apply context planning.
	contextPlan := RunContextPlan{PlanReason: "fixed_window_fallback"}
	emit(EventLocalProgress, LocalProgressPayload{
		Phase:     LocalPhaseContextPlanning,
		Status:    LocalProgressStatusStarted,
		Timestamp: time.Now().UnixMilli(),
	})
	var msgs []store.Message
	var err error
	if recoverySnapshot == nil {
		msgs, err = m.store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID: thread.ID,
			Limit:    500,
		})
	}
	if err != nil {
		emit(EventLocalProgress, LocalProgressPayload{
			Phase:     LocalPhaseContextPlanning,
			Status:    LocalProgressStatusFailed,
			Timestamp: time.Now().UnixMilli(),
		})
		_, _ = m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusFailed, continuationClaimToken, map[string]any{
			"ended_at": time.Now().UTC(), "error_code": "STORE_ERROR", "error_message": err.Error(),
		})
		emitTerminalError(err.Error())
		emitTerminalFinal()
		m.finish(run.ID)
		return
	}
	filtered := make([]store.Message, 0, len(msgs))
	for _, mm := range msgs {
		if mm.UIHidden {
			continue
		}
		filtered = append(filtered, mm)
	}
	filtered = sanitizeStoredQueryRetryHistoryForRun(filtered, run.ID)
	var activeCheckpoint *managerContextCheckpoint
	if recoverySnapshot == nil {
		var checkpointErr error
		activeCheckpoint, checkpointErr = m.loadActiveContextCheckpoint(ctx, thread.ID)
		if checkpointErr != nil {
			log.Printf("run %s: failed to load active context checkpoint: %v", run.ID, checkpointErr)
		}
		activeCheckpoint = sanitizeQueryRetryCheckpointForRun(activeCheckpoint, run.ID)
	}
	currentSettings := m.settings.Get()
	searchMode := strings.EqualFold(strings.TrimSpace(run.Purpose), "search")
	searchDepth := normalizeSearchDepth(req.SearchDepth)
	maxContextInputTokens := defaultMaxInputTokens
	maxContextHardTokens := defaultHardContextTokenCap
	tokenCharsPerToken := defaultTokenCharsPerToken
	if recoverySnapshot != nil {
		maxContextInputTokens = recoverySnapshot.MaxContextInputTokens
		maxContextHardTokens = recoverySnapshot.MaxContextHardTokens
		tokenCharsPerToken = recoverySnapshot.TokenCharsPerToken
	}
	isAnthropicProvider := isAnthropicProviderContext(run.ProviderID, run.Model)
	if recoverySnapshot == nil && isAnthropicProvider {
		if soft, hard, chars, ok := anthropic.ModelContextProfile(run.Model); ok {
			if soft > 0 {
				maxContextInputTokens = soft
			}
			if hard > 0 {
				maxContextHardTokens = hard
			}
			if chars > 0 {
				tokenCharsPerToken = chars
			}
		} else if strings.HasPrefix(strings.ToLower(strings.TrimSpace(run.Model)), "claude-") {
			tokenCharsPerToken = 3
		}
	}
	if guardCap, ok := compactionPlanningTokenCap(currentSettings, run.ProviderID); recoverySnapshot == nil && ok {
		if guardCap > 0 && guardCap < maxContextInputTokens {
			maxContextInputTokens = guardCap
		}
		if guardCap > 0 && guardCap < maxContextHardTokens {
			maxContextHardTokens = guardCap
		}
	}
	checkpointHistory, checkpointDeltaMessages, checkpointUsed := m.buildHistoryFromActiveCheckpoint(ctx, thread.ID, activeCheckpoint, filtered, userMsgID, req.HiddenContext)
	prePlanTokenEstimate := estimateMessageTokensForStoreMessages(filtered, tokenCharsPerToken)
	activePrePlanTokenEstimate := prePlanTokenEstimate
	if checkpointUsed {
		activePrePlanTokenEstimate = estimateChatTokens(checkpointHistory, tokenCharsPerToken)
	}
	initialHardOverflow := prePlanTokenEstimate > maxContextHardTokens
	activeInitialHardOverflow := activePrePlanTokenEstimate > maxContextHardTokens
	if activeInitialHardOverflow {
		contextPlan.HardCapped = true
	}

	plannerResult := ContextBuildResult{}
	history := m.buildLegacyHistory(ctx, thread.ID, filtered, userMsgID, req.HiddenContext)
	contextUsed := false
	if recoverySnapshot != nil {
		var checkpointHistory []types.ChatMessage
		if err := json.Unmarshal(resumeCheckpoint.HistoryJSON, &checkpointHistory); err != nil {
			_ = m.transitionRunToWaitingUser(context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, continuationClaimToken)
			emit("run_waiting_user", map[string]any{"run_id": run.ID, "reason": "continuation history is unreadable"})
			m.finish(run.ID)
			return
		}
		history = checkpointHistory
		contextPlan = recoverySnapshot.ContextPlan
		contextUsed = true
		checkpointUsed = true
	}
	if checkpointUsed && recoverySnapshot == nil {
		history = checkpointHistory
		contextPlan = activeCheckpoint.Plan
		contextPlan.PlanReason = strings.TrimSpace(contextPlan.PlanReason)
		if contextPlan.PlanReason == "" {
			contextPlan.PlanReason = "active_context_checkpoint"
		}
		contextPlan.OverflowCompacted = true
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		contextPlan.TokensBefore = activePrePlanTokenEstimate
		contextPlan.TokensAfter = activePrePlanTokenEstimate
		contextUsed = true
		log.Printf(
			"run %s: loaded active context checkpoint id=%s thread=%s source_seq=%d delta_messages=%d tokens=%d",
			run.ID,
			activeCheckpoint.ID,
			thread.ID,
			activeCheckpoint.SourceMessageSeq,
			len(checkpointDeltaMessages),
			activePrePlanTokenEstimate,
		)
	}
	runContextPlanner := m.contextPlanner
	if runContextPlanner == nil && m.contextPlannerForSnapshot != nil {
		// Context summarization is itself a provider dispatch. Bind it to the
		// finalized run snapshot so queueing or settings edits cannot redirect a
		// pre-dispatch handover call to a different connection/model.
		runContextPlanner = m.contextPlannerForSnapshot(dispatchSnapshot)
	}
	if runContextPlanner != nil && recoverySnapshot == nil {
		planningPolicy := ContextPolicy{
			MaxInputTokens:     maxContextInputTokens,
			HardContextCap:     maxContextHardTokens,
			BaseWindowTurns:    defaultBaseWindowTurns,
			SummaryChunkTokens: defaultSummaryChunkTokens,
			MaxSummaryDepth:    defaultMaxSummaryDepth,
			NeedRetrievalLimit: defaultSummaryNeedTokenLimit,
			EvidenceBudget:     defaultEvidenceBudget,
			CharsPerToken:      tokenCharsPerToken,
		}
		planningPolicy.NeedRetrievalByDefault = searchMode && strings.EqualFold(searchDepth, "deep")
		if activePrePlanTokenEstimate > defaultSummaryNeedTokenLimit {
			planningPolicy.NeedRetrievalByDefault = true
		}
		if checkpointUsed {
			if p, err := runContextPlanner.BuildFromInput(ctx, ContextBuildInput{
				CheckpointDelta: &ContextCheckpointDeltaInput{
					Checkpoint:    contextCheckpointPayloadFromManager(activeCheckpoint),
					DeltaMessages: checkpointDeltaMessages,
				},
				UserMessageID: userMsgID,
				HiddenContext: req.HiddenContext,
				Policy:        planningPolicy,
			}); err == nil {
				plannerResult = p
				history = m.truncateHistory(p.History)
				history = m.hydrateHistoryFileAttachments(ctx, thread.ID, history)
				contextPlan = p.Plan
				contextUsed = true
			} else {
				emitDiagnostic("ACTIVE_CONTEXT_CHECKPOINT_FALLBACK", "active context checkpoint planning failed: "+err.Error())
			}
		} else {
			if p, err := runContextPlanner.Build(ctx, filtered, userMsgID, req.HiddenContext, planningPolicy); err == nil {
				plannerResult = p
				history = m.truncateHistory(p.History)
				history = m.hydrateHistoryFileAttachments(ctx, thread.ID, history)
				contextPlan = p.Plan
				if initialHardOverflow {
					contextPlan.HardCapped = true
				}
				contextUsed = true
			} else {
				emitDiagnostic("CONTEXT_PLANNER_FALLBACK", "context planner failed: "+err.Error())
			}
		}
	}
	shouldFallbackWindow := recoverySnapshot == nil && !contextUsed &&
		m.maxHistory > 0 &&
		activePrePlanTokenEstimate > maxContextInputTokens &&
		len(filtered) > m.maxHistory
	if shouldFallbackWindow {
		emitDiagnostic("CONTEXT_FIXED_WINDOW_FALLBACK", "context planner unavailable and token estimate exceeds model window; applying fixed-window history fallback")
		filtered = filtered[len(filtered)-m.maxHistory:]
		history = m.buildLegacyHistory(ctx, thread.ID, filtered, userMsgID, req.HiddenContext)
	}
	history = m.truncateHistory(history)
	if contextPlan.TokensAfter == 0 && len(history) > 0 {
		contextPlan.TokensAfter = estimateChatTokens(history, tokenCharsPerToken)
	}
	if contextPlan.TokensBefore == 0 && len(filtered) > 0 {
		contextPlan.TokensBefore = estimateMessageTokensForStoreMessages(filtered, tokenCharsPerToken)
	}

	contextPlan.PlanReason = strings.TrimSpace(contextPlan.PlanReason)
	if contextPlan.PlanReason == "" && contextUsed {
		contextPlan.PlanReason = "planner_applied"
	}

	if recoverySnapshot == nil {
		history = attachImageAttachmentsToLatestUser(history, req.ImageAttachments)
		history = attachFileAttachmentsToLatestUser(history, req.FileAttachments)
	}
	// A finalized plan binds provider-native replay/state to one exact saved
	// connection and model. Portable message content remains available across
	// target switches, but legacy or differently-bound opaque payloads do not.
	providerState := map[string]any(nil)
	if recoverySnapshot == nil {
		history = providerHistoryForRun(history, run, false)
		providerState = providerStateFromHistoryForRun(history, run)
	}
	if resumeCheckpoint != nil {
		if strings.TrimSpace(resumeCheckpoint.RunID) != run.ID || strings.TrimSpace(resumeCheckpoint.ThreadID) != thread.ID {
			_ = m.transitionRunToWaitingUser(context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, continuationClaimToken)
			emit("run_waiting_user", map[string]any{
				"run_id": run.ID,
				"reason": "continuation checkpoint ownership mismatch",
			})
			m.finish(run.ID)
			return
		}
		var checkpointHistory []types.ChatMessage
		var checkpointProviderState map[string]any
		if err := json.Unmarshal(resumeCheckpoint.HistoryJSON, &checkpointHistory); err != nil {
			_ = m.transitionRunToWaitingUser(context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, continuationClaimToken)
			emit("run_waiting_user", map[string]any{"run_id": run.ID, "reason": "continuation history is unreadable"})
			m.finish(run.ID)
			return
		}
		if len(resumeCheckpoint.ProviderStateJSON) > 0 && string(resumeCheckpoint.ProviderStateJSON) != "null" {
			if err := json.Unmarshal(resumeCheckpoint.ProviderStateJSON, &checkpointProviderState); err != nil {
				_ = m.transitionRunToWaitingUser(context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, continuationClaimToken)
				emit("run_waiting_user", map[string]any{"run_id": run.ID, "reason": "continuation provider state is unreadable"})
				m.finish(run.ID)
				return
			}
		}
		validatedHistory := providerHistoryForRun(checkpointHistory, run, true)
		checkpointHistoryJSON, _ := json.Marshal(checkpointHistory)
		validatedHistoryJSON, _ := json.Marshal(validatedHistory)
		if !bytes.Equal(checkpointHistoryJSON, validatedHistoryJSON) {
			_ = m.transitionRunToWaitingUser(context.Background(), run.ID, run.ContinuationCount, run.MaxAutoContinuations, continuationClaimToken)
			emit("run_waiting_user", map[string]any{"run_id": run.ID, "reason": "continuation history target binding is incompatible"})
			m.finish(run.ID)
			return
		}
		history = checkpointHistory
		providerState = cloneStringAnyMap(checkpointProviderState)
		contextPlan = recoverySnapshot.ContextPlan
		contextUsed = true
	}
	if resumeCheckpoint != nil {
		if notice := queryContinuationIdentityNotice(m.queryDependenciesForRun(thread.ID, run.ID)); notice != "" {
			history = append(history, types.ChatMessage{Role: types.RoleSystem, Content: &notice})
		}
	}

	runFields := m.runContextUpdateFields(contextPlan)
	runFields["started_at"] = started
	resumedExecution := strings.TrimSpace(continuationClaimToken) != "" && !initialExecutionPreclaimed
	initialExecutionLease := m.longHorizon != nil && !resumedExecution
	if initialExecutionLease {
		if strings.TrimSpace(continuationClaimToken) == "" {
			continuationClaimToken = m.resumeClaimant + "_run_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		}
	}
	if err := m.transitionRunExecutionStarted(ctx, run.ID, continuationClaimToken, runFields); err != nil {
		log.Printf("run %s: failed to update status to running: %v", run.ID, err)
		if leaseGuard.Lost() || errors.Is(err, store.ErrConflict) {
			m.finish(run.ID)
			return
		}
		if resumedExecution {
			// A claimed continuation already has durable query/checkpoint work.
			// A transient start verification error must yield the lease back to
			// recovery (or let it expire), never terminalize that unfinished run.
			releaseModelSlot()
			if requeueErr := m.requeueClaimedRun(context.Background(), run.ID, continuationClaimToken); requeueErr != nil && !errors.Is(requeueErr, store.ErrConflict) {
				log.Printf("run %s: failed to requeue continuation after start verification error; lease expiry will recover it: %v", run.ID, requeueErr)
			}
			leaseGuard.Clear(continuationClaimToken)
			m.finish(run.ID)
			return
		}
		// If we can't persist the running transition, terminate the run to avoid queue stalls.
		fallbackClaimToken := continuationClaimToken
		if initialExecutionLease {
			// ClaimRunWake may have failed before acquiring the queued run. Read the
			// durable state once to distinguish a committed lease from an untouched
			// queue entry; never bypass a lease held by another worker.
			if current, currentErr := m.store.GetRun(context.Background(), run.ID); currentErr == nil {
				switch {
				case current.Status == store.RunStatusRunning && current.ResumeLeaseHolder == continuationClaimToken:
					// Keep the fenced token.
				case current.Status == store.RunStatusQueued && current.ResumeLeaseHolder == "":
					fallbackClaimToken = ""
				default:
					m.finish(run.ID)
					return
				}
			} else {
				m.finish(run.ID)
				return
			}
		}
		if _, failErr := m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusFailed, fallbackClaimToken, map[string]any{
			"ended_at": time.Now().UTC(), "error_code": "STORE_ERROR", "error_message": "failed to mark run as running: " + err.Error(),
		}); failErr != nil {
			log.Printf("run %s: failed to persist fallback failed status: %v", run.ID, failErr)
		}
		emit(EventLocalProgress, LocalProgressPayload{
			Phase:     LocalPhaseContextPlanning,
			Status:    LocalProgressStatusFailed,
			Timestamp: time.Now().UnixMilli(),
		})
		emitTerminalError("failed to persist run start: " + err.Error())
		emitTerminalFinal()
		m.finish(run.ID)
		return
	}
	if initialExecutionLease {
		// Initial model executions now use the same durable ownership lease as
		// resumed continuations. If this process dies after launching warehouse
		// work but before its first checkpoint, recovery can safely distinguish
		// the dead worker from a genuinely live one.
		leaseGuard.SetSupersessionValidator(func(validationCtx context.Context) (bool, error) {
			return m.validateRunContinuationGeneration(validationCtx, run.ID)
		})
		leaseGuard.Hold(continuationClaimToken, nil)
	}
	emit(EventLocalProgress, LocalProgressPayload{
		Phase:        LocalPhaseContextPlanning,
		Status:       LocalProgressStatusCompleted,
		Timestamp:    time.Now().UnixMilli(),
		TokenBudget:  maxContextInputTokens,
		TokensBefore: contextPlan.TokensBefore,
		TokensAfter:  contextPlan.TokensAfter,
		Compacted:    contextPlan.OverflowCompacted,
	})
	emit("run_started", map[string]any{})

	referencedArtifacts := append([]string(nil), req.ReferencedArtifactIDs...)
	referencedMessages := append([]string(nil), req.ReferencedMessageIDs...)
	referencedThreads := append([]string(nil), req.ReferencedThreadIDs...)
	persistContextForUserMsg := strings.TrimSpace(userMsgID) != "" && (contextUsed || contextPlan.TokensBefore > 0 || contextPlan.TokensAfter > 0 || strings.TrimSpace(plannerResult.SummaryForUser) != "")
	if persistContextForUserMsg || req.HiddenContext != "" || len(referencedArtifacts) > 0 || len(referencedMessages) > 0 || len(referencedThreads) > 0 {
		updates := map[string]any{}
		if strings.TrimSpace(req.HiddenContext) != "" {
			v := strings.TrimSpace(req.HiddenContext)
			updates["hidden_context"] = v
		}
		if len(referencedArtifacts) > 0 {
			updates["referenced_artifact_ids"] = referencedArtifacts
		}
		if len(referencedMessages) > 0 {
			updates["referenced_message_ids"] = referencedMessages
		}
		if len(referencedThreads) > 0 {
			updates["referenced_thread_ids"] = referencedThreads
		}
		if summary := strings.TrimSpace(plannerResult.SummaryForUser); summary != "" {
			updates["context_summary"] = summary
		}
		if contextPlan.TokensBefore > 0 || contextPlan.TokensAfter > 0 || contextUsed {
			if planBlob, err := json.Marshal(contextPlan); err == nil && len(planBlob) > 0 {
				updates["context_plan"] = json.RawMessage(planBlob)
			}
		}
		if len(updates) > 0 {
			_ = m.store.UpdateMessage(ctx, userMsgID, updates)
		}
	}

	currentSettings = applyRunDatasourceOverrides(currentSettings, req.DataSources, req.DataSourceOverrides)
	queryRowLimit := config.DefaultDatabaseQueryRowLimit
	if currentSettings.DatabaseQueryRowLimit != nil && *currentSettings.DatabaseQueryRowLimit > 0 {
		queryRowLimit = *currentSettings.DatabaseQueryRowLimit
	}
	enabledChartEngines := chartEngineEnablementFromSettings(currentSettings)
	enabledAtlassian := atlassianEnablementFromSettings(currentSettings)
	enabledBrowserControl := browserControlEnabledFromSettings(currentSettings)
	enabledSharePoint := sharePointEnabledFromSettings(&currentSettings)
	enabledGitHub := githubEnabledFromSettings(&currentSettings)
	enabledSlack := slackToolAccessFromSettings(&currentSettings)
	enabledArxiv := arxivEnabledFromSettings(&currentSettings)
	enabledThreadSearch := threadSearchEnabledFromSettings(&currentSettings)
	enabledLocalComputer := localComputerEnabledFromSettings(currentSettings)
	enabledImageGeneration := currentSettings.ImageGeneration != nil &&
		currentSettings.ImageGeneration.Enabled != nil && *currentSettings.ImageGeneration.Enabled
	enabledDatasources := datasourceEnablement(currentSettings)
	var activeRunConnectionScope runConnectionScope
	if req.ActiveConnectionIDsProvided {
		activeRunConnectionScope = newRunConnectionScope(req.ActiveConnectionIDs)
		enabledImageGeneration = enabledImageGeneration && activeRunConnectionScope["image-generation"]
		enabledDatasources, enabledAtlassian, enabledBrowserControl, enabledSharePoint, enabledGitHub, enabledSlack, enabledArxiv, enabledThreadSearch, enabledLocalComputer = applyRunActiveConnectionScope(
			enabledDatasources,
			enabledAtlassian,
			enabledBrowserControl,
			enabledSharePoint,
			enabledGitHub,
			enabledSlack,
			enabledArxiv,
			enabledThreadSearch,
			enabledLocalComputer,
			req.ActiveConnectionIDs,
		)
	}
	runtimeReferenceTime := runtimeTimeFromClientContext(req)
	localComputerGrants := []localComputerPermissionGrant(nil)
	if enabledLocalComputer {
		localComputerGrants = compactLocalPermissionGrants(localComputerReadPermissionGrantsFromSettings(currentSettings))
	}
	if recoverySnapshot != nil {
		queryRowLimit = recoverySnapshot.QueryRowLimit
		searchMode = recoverySnapshot.SearchMode
		searchDepth = recoverySnapshot.SearchDepth
		runtimeReferenceTime = recoverySnapshot.RuntimeReference
		enabledDatasources = cloneDatasourceEnabledMap(recoverySnapshot.ToolScope.Datasources)
		enabledChartEngines = chartEngineEnablement{
			Echarts: recoverySnapshot.ToolScope.ChartEcharts, Highcharts: recoverySnapshot.ToolScope.ChartHighcharts,
			ChartProgramPipeline: recoverySnapshot.ToolScope.ChartPipeline, ChartDimensionFilters: recoverySnapshot.ToolScope.ChartFilters,
		}
		enabledAtlassian = atlassianEnablement{Jira: recoverySnapshot.ToolScope.AtlassianJira, Confluence: recoverySnapshot.ToolScope.AtlassianWiki}
		enabledBrowserControl = recoverySnapshot.ToolScope.BrowserControl
		enabledSharePoint = recoverySnapshot.ToolScope.SharePoint
		enabledGitHub = recoverySnapshot.ToolScope.GitHub
		enabledSlack = slackToolAccess{Read: recoverySnapshot.ToolScope.SlackRead, Write: recoverySnapshot.ToolScope.SlackWrite}
		enabledArxiv = recoverySnapshot.ToolScope.Arxiv
		enabledThreadSearch = recoverySnapshot.ToolScope.ThreadSearch
		enabledLocalComputer = recoverySnapshot.ToolScope.LocalComputer
		localComputerGrants = append([]localComputerPermissionGrant(nil), recoverySnapshot.ToolScope.LocalGrants...)
	}
	systemPromptBase := strings.TrimSpace(run.SystemPrompt)
	if recoverySnapshot != nil {
		// Older checkpoints may have embedded a prior thread summary before the
		// summary boundary was sanitized. Never replay a run-scoped qob from it.
		systemPromptBase = redactStaleQueryRetryText(recoverySnapshot.SystemPromptBase)
	} else {
		systemPromptBase = appendSanitizedThreadSummary(systemPromptBase, thread.Summary)
		systemPromptBase = embedRemoteQueryLimitGuidance(systemPromptBase, queryRowLimit)
		systemPromptBase = embedAsyncQueryGuidance(systemPromptBase)
		systemPromptBase = embedToolErrorCommentaryGuidance(systemPromptBase)
		systemPromptBase = embedChartingGuidance(systemPromptBase, enabledChartEngines)
		systemPromptBase = embedThreadTransferGuidance(systemPromptBase)
		systemPromptBase = embedAtlassianGuidance(systemPromptBase, enabledAtlassian)
		systemPromptBase = embedBrowserControlGuidance(systemPromptBase, enabledBrowserControl)
		systemPromptBase = embedSharePointGuidance(systemPromptBase, enabledSharePoint)
		systemPromptBase = embedGitHubGuidance(systemPromptBase, enabledGitHub)
		systemPromptBase = embedSlackGuidance(systemPromptBase, enabledSlack)
		systemPromptBase = embedArxivGuidance(systemPromptBase, enabledArxiv)
		systemPromptBase = embedThreadSearchAvailabilityGuidance(systemPromptBase, enabledThreadSearch)
		systemPromptBase = embedLocalComputerAccessGuidance(systemPromptBase, localComputerGrants)
	}

	systemPrompt := ""
	rebuildSystemPrompt := func() {
		systemPromptWithRuntime := embedRuntimeTemporalGuidance(systemPromptBase, runtimeReferenceTime)
		systemPrompt = embedContextPlanningGuidance(systemPromptWithRuntime, thread.ID, contextPlan, enabledThreadSearch)
		if isAnthropicProvider {
			profile := m.inferAnthropicToolProfile(
				req,
				(contextPlan.NeedRetrieval || contextPlan.NeedRetrievalTooling) && enabledThreadSearch,
				enabledDatasources,
			)
			if searchMode && enabledThreadSearch {
				profile.includeSearch = true
			}
			systemPrompt = embedAnthropicToolingGuidance(systemPrompt, profile)
		}
	}
	rebuildSystemPrompt()
	if recoverySnapshot != nil {
		systemPrompt = redactStaleQueryRetryText(recoverySnapshot.SystemPrompt)
	}

	// Tool execution wrapper.
	if contextPlan.EvidenceBudget <= 0 {
		contextPlan.EvidenceBudget = defaultEvidenceBudget
	}
	evidenceBudget := maxInt(contextPlan.EvidenceBudget, defaultEvidenceBudget)
	evidenceUsed := 0
	if recoverySnapshot != nil {
		evidenceBudget = recoverySnapshot.EvidenceBudget
		evidenceUsed = recoverySnapshot.EvidenceUsed
		contextPlan.EvidenceUsed = evidenceUsed
	}
	toolBudgetEnabled := evidenceBudget > 0
	consumeEvidenceTool := func(toolName string) bool {
		if !toolBudgetEnabled || !isRetrievalTool(toolName) {
			return true
		}
		evidenceUsed++
		contextPlan.EvidenceUsed = evidenceUsed
		if evidenceUsed > evidenceBudget {
			contextPlan.EvidenceExhausted = true
			return false
		}
		return true
	}
	var toolLoopGuard ToolLoopGuard
	if m.toolLoopGuard != nil {
		toolLoopGuard = m.toolLoopGuard.NewRunGuard()
	}

	persistMidRunContextCheckpoint := func(reason string, compactedHistory []types.ChatMessage, tokenEstimate int) {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			reason = "mid_run_context_handoff"
		}
		sourceMessages := filtered
		if latest, err := m.store.ListMessages(context.Background(), store.ListMessagesParams{
			ThreadID: thread.ID,
			Limit:    500,
		}); err == nil {
			visible := make([]store.Message, 0, len(latest))
			for _, msg := range latest {
				if msg.UIHidden {
					continue
				}
				visible = append(visible, msg)
			}
			if len(visible) > 0 {
				sourceMessages = visible
			}
		} else {
			log.Printf("run %s: failed to refresh messages for mid-run context checkpoint: %v", run.ID, err)
		}
		checkpointPlan := contextPlan
		checkpointPlan.OverflowCompacted = true
		checkpointPlan.NeedRetrieval = true
		checkpointPlan.NeedRetrievalTooling = true
		checkpointPlan.CompactionMode = handoverCompactionMode
		checkpointPlan.HandoverSummaryUsed = true
		checkpointPlan.TokensAfter = tokenEstimate
		if strings.TrimSpace(checkpointPlan.PlanReason) == "" || checkpointPlan.PlanReason == "within_budget" {
			checkpointPlan.PlanReason = reason
		}
		summary := m.summarizeHistoryForContextCheckpoint(compactedHistory, reason)
		compactedHistory = providerHistoryForRun(compactedHistory, run, true)
		checkpoint := managerContextCheckpoint{
			ThreadID:              thread.ID,
			RunID:                 &run.ID,
			SourceMessageSeq:      maxMessageSeq(sourceMessages),
			SourceMessageID:       latestMessageID(sourceMessages),
			ReplacementHistory:    m.checkpointSafeHistory(compactedHistory),
			Summary:               summary,
			SummaryMessageIDs:     idsFromMessages(sourceMessages),
			ReferencedArtifactIDs: checkpointReferencedArtifactIDs(sourceMessages),
			TokenEstimate:         tokenEstimate,
			CharsPerToken:         tokenCharsPerToken,
			Model:                 run.Model,
			ProviderID:            run.ProviderID,
			Plan:                  checkpointPlan,
		}
		persisted, err := m.persistActiveContextCheckpoint(context.Background(), thread, run, checkpoint)
		if err != nil {
			log.Printf("run %s: failed to persist mid-run context checkpoint: %v", run.ID, err)
			return
		}
		fields := m.runContextUpdateFields(checkpointPlan)
		if m.longHorizon == nil {
			if err := m.updateRunStatusWithFallback(context.Background(), run.ID, "running", fields); err != nil {
				log.Printf("run %s: failed to persist mid-run context metadata: %v", run.ID, err)
			}
		}
		log.Printf(
			"run %s: wrote mid-run context checkpoint id=%s thread=%s source_seq=%d tokens_after=%d reason=%s",
			run.ID,
			persisted.ID,
			thread.ID,
			persisted.SourceMessageSeq,
			tokenEstimate,
			reason,
		)
	}

	midRunCompact := func(history []types.ChatMessage, tokenBudget int, systemPrompt string, toolDefs []types.ToolDefinition) ([]types.ChatMessage, bool) {
		if tokenBudget <= 0 {
			return history, false
		}
		tokenBudget = maxInt(1, tokenBudget)
		effectiveCompaction := func(before, after []types.ChatMessage, hinted bool) (bool, int) {
			if after == nil {
				after = before
			}
			beforeTokens := estimateRequestTokensWithTools(systemPrompt, before, toolDefs, tokenCharsPerToken)
			afterTokens := estimateRequestTokensWithTools(systemPrompt, after, toolDefs, tokenCharsPerToken)
			didCompact := hinted || len(after) < len(before) || afterTokens < beforeTokens
			return didCompact, afterTokens
		}
		if m.toolCompaction != nil {
			result := m.toolCompaction.CompactHistory(ToolResultHistoryInput{
				History:       history,
				TokenBudget:   tokenBudget,
				SystemPrompt:  systemPrompt,
				ToolDefs:      toolDefs,
				CharsPerToken: tokenCharsPerToken,
			})
			compactedHistory := result.History
			if compactedHistory == nil {
				compactedHistory = history
			}
			if didCompact, compactedTokens := effectiveCompaction(history, compactedHistory, result.Compacted); didCompact {
				history = compactedHistory
				if toolLoopGuard != nil {
					toolLoopGuard.MarkLatestDispatchCompacted(history)
				}
				contextPlan.OverflowCompacted = true
				contextPlan.NeedRetrieval = true
				contextPlan.NeedRetrievalTooling = true
				contextPlan.PlanReason = strings.TrimSpace(contextPlan.PlanReason)
				if contextPlan.PlanReason == "" {
					contextPlan.PlanReason = "tool_result_budget_compaction"
				}
				contextPlan.TokensAfter = compactedTokens
				if contextPlan.TokensAfter <= tokenBudget {
					return history, true
				}
			}
		}
		currentTokens := estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		proactiveBudget := handoverTriggerTokenLimit(ContextPolicy{
			MaxInputTokens:     tokenBudget,
			NeedRetrievalLimit: defaultSummaryNeedTokenLimit,
		})
		if tokenBudget >= minProactiveHandoverTokens && currentTokens > proactiveBudget {
			reason := "mid_run_proactive_handoff"
			summary := m.summarizeHistoryForContextCheckpoint(history, reason)
			compacted := m.buildEmergencyContextHistory(history, summary, systemPrompt, toolDefs, proactiveBudget, tokenCharsPerToken)
			compactedTokens := estimateRequestTokensWithTools(systemPrompt, compacted, toolDefs, tokenCharsPerToken)
			if compactedTokens < currentTokens {
				contextPlan.OverflowCompacted = true
				contextPlan.NeedRetrieval = true
				contextPlan.NeedRetrievalTooling = true
				contextPlan.CompactionMode = handoverCompactionMode
				contextPlan.HandoverSummaryUsed = true
				contextPlan.TokensAfter = compactedTokens
				if strings.TrimSpace(contextPlan.PlanReason) == "" || contextPlan.PlanReason == "within_budget" {
					contextPlan.PlanReason = reason
				}
				persistMidRunContextCheckpoint(reason, compacted, compactedTokens)
				return compacted, true
			}
		}
		if currentTokens <= tokenBudget {
			return history, false
		}

		if !isAnthropicProvider {
			compacted := m.trimHistoryToTokenBudget(history, systemPrompt, toolDefs, tokenBudget, tokenCharsPerToken)
			didCompact, compactedTokens := effectiveCompaction(history, compacted, false)
			if !didCompact {
				return history, false
			}
			contextPlan.OverflowCompacted = true
			contextPlan.NeedRetrieval = true
			contextPlan.NeedRetrievalTooling = true
			contextPlan.PlanReason = strings.TrimSpace(contextPlan.PlanReason)
			if contextPlan.PlanReason == "" {
				contextPlan.PlanReason = "iterative_handoff"
			}
			contextPlan.TokensAfter = compactedTokens
			return compacted, true
		}

		compacted, dropped := m.compactAnthropicToolHistory(history, systemPrompt, toolDefs, tokenBudget, defaultAnthropicToolMessageLimit, tokenCharsPerToken)
		didCompact, compactedTokens := effectiveCompaction(history, compacted, dropped > 0)
		if !didCompact {
			return history, false
		}
		contextPlan.OverflowCompacted = true
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		contextPlan.PlanReason = strings.TrimSpace(contextPlan.PlanReason)
		if contextPlan.PlanReason == "" {
			contextPlan.PlanReason = "iterative_handoff"
		}
		contextPlan.TokensAfter = compactedTokens
		return compacted, true
	}

	execTool := func(call types.ToolCall) (types.ChatMessage, error) {
		if toolLoopGuard != nil {
			if blockedOut, blocked := toolLoopGuard.BeforeCall(call); blocked {
				return m.persistToolResult(ctx, thread, run, call, blockedOut, contextPlan), nil
			}
		}
		if !consumeEvidenceTool(strings.TrimSpace(call.Name)) {
			return m.persistToolResult(ctx, thread, run, call, tools.JSONError("evidence budget exhausted"), contextPlan), nil
		}
		msg, err := m.execTool(
			ctx,
			thread,
			run,
			&projectID,
			call,
			searchMode,
			searchDepth,
			queryRowLimit,
			&contextPlan,
			enabledDatasources,
			enabledChartEngines,
			enabledSharePoint,
			enabledArxiv,
			enabledThreadSearch,
			enabledLocalComputer,
			enabledImageGeneration,
			enabledAtlassian,
			enabledBrowserControl,
			enabledGitHub,
			enabledSlack,
		)
		if toolLoopGuard != nil {
			toolLoopGuard.RecordToolResult(call, msg)
		}
		return msg, err
	}

	var lastMeta *types.ResponseMeta
	var callbackErr error
	thinkingDeltaPush, flushThinkingDeltas := m.coalesceDeltas(run.ID, "thinking_delta", emit)
	compactionDeltaPush, flushCompactionDeltas := m.coalesceDeltas(run.ID, "compaction_delta", emit)
	textDeltaPush, flushTextDeltas := m.coalesceDeltas(run.ID, "text_delta", emit)
	assistantTurnStreamedText := false
	cb := &engine.Callbacks{
		OnAssistantStart: func() {
			assistantTurnStreamedText = false
			emit("assistant_start", map[string]any{})
		},
		OnThinkingStart: func(traceID string) {
			thinkingOpen = true
			emit("thinking_start", map[string]any{"traceId": traceID})
		},
		OnThinkingDelta: thinkingDeltaPush,
		OnThinkingEnd: func(summary *string) {
			ensureThinkingEnd(summary)
		},
		OnCompactionStart: func() {
			emit("compaction_start", map[string]any{})
		},
		OnCompactionDelta: compactionDeltaPush,
		OnCompactionEnd: func() {
			emitCompactionEnd(flushCompactionDeltas, emit)
		},
		OnLocalProgress: func(progress engine.LocalProgressEvent) {
			phase := strings.TrimSpace(progress.Phase)
			switch phase {
			case engine.LocalPhaseContextCompacting:
				phase = LocalPhaseContextCompacting
			case engine.LocalPhaseToolResultCompacting:
				phase = LocalPhaseToolResultCompacting
			}
			status := strings.TrimSpace(progress.Status)
			switch status {
			case engine.LocalProgressStatusStarted:
				status = LocalProgressStatusStarted
			case engine.LocalProgressStatusCompleted:
				status = LocalProgressStatusCompleted
			}
			if phase == "" || status == "" {
				return
			}
			emit(EventLocalProgress, LocalProgressPayload{
				Phase:         phase,
				Status:        status,
				Timestamp:     time.Now().UnixMilli(),
				Iteration:     progress.Iteration,
				ToolCallCount: progress.ToolCallCount,
				TokenBudget:   progress.TokenBudget,
				TokensBefore:  progress.TokensBefore,
				TokensAfter:   progress.TokensAfter,
				Compacted:     progress.Compacted,
			})
		},
		OnProviderToolProgress: func(progress types.ProviderToolProgressData) {
			if strings.TrimSpace(progress.ToolName) == "" {
				return
			}
			if strings.TrimSpace(progress.ToolCallID) == "" {
				progress.ToolCallID = "provider_tool_" + strings.ReplaceAll(types.NewUUID(), "-", "")
			}
			if progress.Timestamp == 0 {
				progress.Timestamp = time.Now().UnixMilli()
			}
			emit(types.EventProviderToolProgress, progress)
		},
		OnPlanUpdate: func(update types.PlanUpdateData) {
			if strings.TrimSpace(run.PlanID) == "" {
				return
			}
			update.PlanID = strings.TrimSpace(run.PlanID)
			if strings.TrimSpace(update.Change.Kind) == "" || strings.TrimSpace(update.Change.Text) == "" {
				return
			}
			emit(types.EventPlanUpdate, update)
		},
		OnTextDelta: func(delta string) {
			if delta == "" {
				return
			}
			assistantTurnStreamedText = true
			textDeltaPush(delta)
		},
		OnAssistantFinal: func(message types.ChatMessage, meta *types.ResponseMeta) {
			ensureThinkingEnd(nil)
			// Ensure pending coalesced deltas are delivered before final snapshot events.
			flushTextDeltas()
			if meta != nil {
				c := *meta
				lastMeta = &c
			}

			// Persist assistant message.
			var toolCalls []types.ToolCall
			if len(message.ToolCalls) > 0 {
				toolCalls = message.ToolCalls
			}
			contextPlanBlob := []byte(nil)
			if blob, err := json.Marshal(contextPlan); err == nil && len(blob) > 0 {
				contextPlanBlob = blob
			}

			msgContent := message.Content
			providerPayload := cloneRawJSON(message.ProviderPayload)
			if strings.TrimSpace(run.PlanID) != "" {
				providerPayload = bindProviderPayloadTarget(providerPayload, run.ConnectionID, run.Model)
			}
			assistantMsg := store.Message{
				ID:                    types.NewUUID(),
				Role:                  types.RoleAssistant,
				Content:               msgContent,
				ProviderPayload:       providerPayload,
				ToolCalls:             toolCalls,
				ContextSummary:        summarizeContextPlan(contextPlan),
				ContextPlan:           contextPlanBlob,
				ReferencedArtifactIDs: nil,
			}
			assistantForUI := message
			assistantForUI.ProviderPayload = cloneRawJSON(providerPayload)
			if persistedAssistant, err := m.store.AppendMessage(ctx, thread.ID, &run.ID, assistantMsg); err != nil {
				// Best-effort: surface to stream but continue.
				emitDiagnostic("ASSISTANT_PERSIST_WARNING", "failed to persist assistant message: "+err.Error())
			} else {
				if id := strings.TrimSpace(persistedAssistant.ID); id != "" {
					assistantForUI.ID = id
				}
				if persistedAssistant.Seq > 0 {
					seq := persistedAssistant.Seq
					assistantForUI.Seq = &seq
				}
			}

			// Emit tool call snapshot if present.
			if len(toolCalls) > 0 {
				emit("tool_calls", map[string]any{"toolCalls": toolCalls})
			}

			// Emit assistant completion.
			uiMsg := toUIMessage(assistantForUI)
			if assistantTurnStreamedText {
				// Avoid repeating already-streamed text in parsers that append both text_delta and assistant_final.
				uiMsg["content"] = nil
			}
			payload := map[string]any{"message": uiMsg}
			if meta != nil {
				payload["meta"] = meta
			}
			emit("assistant_final", payload)
		},
		OnToolResult: func(message types.ChatMessage) {
			if message.Role == types.RoleTool && strings.TrimSpace(message.ID) == "" {
				content := ""
				if message.Content != nil {
					content = *message.Content
				}
				env, ok := types.ParseToolResultEnvelope(content)
				if !ok || env.ResultMetadata["provider_tool"] != true {
					uiMsg := toUIMessage(message)
					emit("tool_result", map[string]any{"message": uiMsg})
					return
				}
				toolCallID := strings.TrimSpace(message.ToolCallID)
				toolName := strings.TrimSpace(message.Name)
				referencedArtifacts := []string(nil)
				if strings.TrimSpace(env.ArtifactID) != "" {
					referencedArtifacts = []string{strings.TrimSpace(env.ArtifactID)}
				}
				contextPlanBlob := []byte(nil)
				if blob, err := json.Marshal(contextPlan); err == nil && len(blob) > 0 {
					contextPlanBlob = blob
				}
				persisted, err := m.store.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
					ID:                    types.NewUUID(),
					Role:                  types.RoleTool,
					Content:               &content,
					ToolCallID:            &toolCallID,
					Name:                  &toolName,
					ReferencedArtifactIDs: referencedArtifacts,
					ContextSummary:        summarizeContextPlan(contextPlan),
					ContextPlan:           contextPlanBlob,
				})
				if err != nil {
					emitDiagnostic("PROVIDER_TOOL_RESULT_PERSIST_WARNING", "failed to persist provider tool result: "+err.Error())
				} else {
					if id := strings.TrimSpace(persisted.ID); id != "" {
						message.ID = id
					}
					if persisted.Seq > 0 {
						seq := persisted.Seq
						message.Seq = &seq
					}
				}
			}
			uiMsg := toUIMessage(message)
			emit("tool_result", map[string]any{"message": uiMsg})
		},
		OnError: func(message string) {
			message = strings.TrimSpace(message)
			if message == "" {
				message = "provider stream failed"
			}
			callbackErr = errors.New(message)
			emitTerminalError(message)
		},
	}

	toolDefs := []types.ToolDefinition{}
	buildToolDefinitions := func() {
		if recoverySnapshot != nil {
			encoded, err := json.Marshal(recoverySnapshot.ToolDefinitions)
			if err == nil {
				_ = json.Unmarshal(encoded, &toolDefs)
			}
			return
		}
		includeContextTools := (contextPlan.NeedRetrieval || contextPlan.NeedRetrievalTooling) && enabledThreadSearch
		anthropicProfile := m.inferAnthropicToolProfile(req, includeContextTools, enabledDatasources)
		if searchMode && enabledThreadSearch {
			anthropicProfile.includeSearch = true
		}
		if isAnthropicProvider {
			toolDefs = m.selectAnthropicToolDefs(searchMode, searchDepth, includeContextTools, enabledDatasources, enabledThreadSearch, anthropicProfile)
		} else {
			toolDefs = m.tools.Definitions()
			toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.ThreadSearchToolDefinitions(enabledThreadSearch))
			if searchMode {
				toolDefs = m.tools.DefinitionsForSearch(searchDepth)
			} else if includeContextTools {
				toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.DefinitionsForSearch("deep"))
			}
		}
		if !searchMode {
			if enabledImageGeneration {
				toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.ImageGenerationToolDefinitions(m.settings))
			}
			toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.AtlassianToolDefinitions(enabledAtlassian.Jira, enabledAtlassian.Confluence))
			toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.BrowserControlToolDefinitions(enabledBrowserControl))
			toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.SharePointToolDefinitions(enabledSharePoint))
			toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.GitHubToolDefinitions(enabledGitHub))
			toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.SlackToolDefinitions(enabledSlack.Read, enabledSlack.Write))
			toolDefs = m.mergeToolDefinitions(toolDefs, m.tools.ArxivToolDefinitions(enabledArxiv))
		}
		toolDefs = filterToolsByDatasourceEnabled(toolDefs, enabledDatasources)
		toolDefs = filterToolsByAtlassianEnabled(toolDefs, enabledAtlassian)
		toolDefs = filterToolsByBrowserControlEnabled(toolDefs, enabledBrowserControl)
		toolDefs = filterToolsBySharePointEnabled(toolDefs, enabledSharePoint)
		toolDefs = filterToolsByGitHubEnabled(toolDefs, enabledGitHub)
		toolDefs = filterToolsBySlackEnabled(toolDefs, enabledSlack)
		toolDefs = filterToolsByArxivEnabled(toolDefs, enabledArxiv)
		toolDefs = filterToolsByThreadSearchEnabled(toolDefs, enabledThreadSearch)
		toolDefs = filterToolDefinitionsByChartEngines(toolDefs, enabledChartEngines)
		if strings.TrimSpace(projectID) == "" {
			toolDefs = filterProjectFileToolDefinitionsWithoutProject(toolDefs)
		}
		if len(req.FileAttachments) > 0 {
			toolDefs = filterLocalAttachmentToolDefinitions(toolDefs)
		}
		toolDefs = compactAnthropicToolDefinitions(toolDefs, isAnthropicProvider)
	}
	buildToolDefinitions()

	toolingTokenReserve := estimateToolOverheadTokens(toolDefs, tokenCharsPerToken)
	requestTokenEstimate := estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
	engineInputCap := maxInt(1, maxContextInputTokens-toolingTokenReserve)
	hardInputCap := maxInt(1, maxContextHardTokens-toolingTokenReserve)
	if hardInputCap < engineInputCap {
		hardInputCap = engineInputCap
	}
	runInputCap := engineInputCap
	if recoverySnapshot != nil {
		engineInputCap = recoverySnapshot.EngineInputCap
		hardInputCap = recoverySnapshot.HardInputCap
		runInputCap = recoverySnapshot.RunInputCap
	}

	refreshEngineCaps := func() {
		if recoverySnapshot != nil {
			requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
			return
		}
		toolingTokenReserve = estimateToolOverheadTokens(toolDefs, tokenCharsPerToken)
		engineInputCap = maxInt(1, maxContextInputTokens-toolingTokenReserve)
		requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		hardInputCap = maxInt(1, maxContextHardTokens-toolingTokenReserve)
		if hardInputCap < engineInputCap {
			hardInputCap = engineInputCap
		}
	}
	retryLimit := 3
	emitLocalProgress := func(phase string, status string, tokenBudget int, tokensBefore int, tokensAfter int, compacted bool) {
		phase = strings.TrimSpace(phase)
		status = strings.TrimSpace(status)
		if phase == "" || status == "" {
			return
		}
		emit(EventLocalProgress, LocalProgressPayload{
			Phase:        phase,
			Status:       status,
			Timestamp:    time.Now().UnixMilli(),
			TokenBudget:  tokenBudget,
			TokensBefore: tokensBefore,
			TokensAfter:  tokensAfter,
			Compacted:    compacted,
		})
	}
	var deferRunFailureForQueryDependencies func(string) bool
	failRunAfterContextRecovery := func(reason string, message string) {
		if deferRunFailureForQueryDependencies != nil && deferRunFailureForQueryDependencies(reason) {
			return
		}
		if strings.TrimSpace(contextPlan.PlanReason) == "" {
			contextPlan.PlanReason = reason
		}
		contextPlan.HardCapped = true
		contextPlan.OverflowCompacted = true
		failFields := m.runContextUpdateFields(contextPlan)
		failFields["context_overflow_compacted"] = true
		failFields["context_hard_capped"] = true
		failFields["error_code"] = "CONTEXT_RECOVERY_FAILED"
		failFields["error_message"] = strings.TrimSpace(message)
		failFields["ended_at"] = time.Now().UTC()
		changed, err := m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusFailed, continuationClaimToken, failFields)
		if err != nil {
			log.Printf("run %s: failed to update status to failed: %v", run.ID, err)
		}
		if !changed {
			m.finish(run.ID)
			return
		}
		emitTerminalError(strings.TrimSpace(message))
		emitTerminalFinal()
		m.finish(run.ID)
	}
	nearCapFallbackApplied := recoverySnapshot != nil && recoverySnapshot.NearCapFallbackApplied
	applyNearCapacityFallback := func(reason string) bool {
		if !isAnthropicProvider || nearCapFallbackApplied {
			return false
		}
		nearCapFallbackApplied = true
		searchMode = true
		searchDepth = "shallow"
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		contextPlan.HardCapped = true
		contextPlan.OverflowCompacted = true
		if strings.TrimSpace(contextPlan.PlanReason) == "" {
			contextPlan.PlanReason = reason
		}
		buildToolDefinitions()
		refreshEngineCaps()
		rebuildSystemPrompt()
		aggressiveBudget := maxInt(1, hardInputCap/2)
		history = m.trimHistoryToTokenBudget(history, systemPrompt, toolDefs, aggressiveBudget, tokenCharsPerToken)
		requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		refreshEngineCaps()
		return requestTokenEstimate <= hardInputCap
	}

	attemptCapRecovery := func(reason string, target int) bool {
		if runContextPlanner == nil {
			if isAnthropicProvider && target >= hardInputCap && applyNearCapacityFallback(reason) {
				return requestTokenEstimate <= hardInputCap
			}
			return false
		}
		targetInput := target
		if targetInput <= 0 {
			targetInput = maxContextInputTokens
		}
		policyMaxInput := maxInt(1, targetInput)
		policyHardCap := maxInt(1, targetInput)
		policy := ContextPolicy{
			MaxInputTokens:         policyMaxInput,
			HardContextCap:         policyHardCap,
			BaseWindowTurns:        maxInt(2, defaultBaseWindowTurns/2),
			SummaryChunkTokens:     defaultSummaryChunkTokens,
			MaxSummaryDepth:        defaultMaxSummaryDepth,
			NeedRetrievalLimit:     defaultSummaryNeedTokenLimit,
			NeedRetrievalByDefault: true,
			EvidenceBudget:         maxInt(contextPlan.EvidenceBudget, defaultEvidenceBudget),
			CharsPerToken:          tokenCharsPerToken,
		}
		fallback, err := runContextPlanner.BuildLocal(ctx, filtered, userMsgID, req.HiddenContext, policy)
		if err != nil {
			emitDiagnostic("CONTEXT_COMPACT_RECOVERY_FAILED", "context planner compact recovery failed: "+err.Error())
			return false
		}
		history = m.truncateHistory(fallback.History)
		history = m.hydrateHistoryFileAttachments(ctx, thread.ID, history)
		contextPlan = fallback.Plan
		contextPlan.HardCapped = contextPlan.HardCapped || initialHardOverflow || target > maxContextInputTokens
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		contextPlan.OverflowCompacted = true
		if strings.TrimSpace(contextPlan.PlanReason) == "" {
			contextPlan.PlanReason = reason
		}
		buildToolDefinitions()
		refreshEngineCaps()
		rebuildSystemPrompt()
		requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		return requestTokenEstimate <= targetInput
	}

	recoverContextOverflow := func(reason string, target int) bool {
		target = maxInt(1, target)
		before := estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		emitLocalProgress(LocalPhaseContextCompacting, LocalProgressStatusStarted, target, before, 0, false)
		contextPlan.HardCapped = true
		contextPlan.OverflowCompacted = true
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		if strings.TrimSpace(contextPlan.PlanReason) == "" || contextPlan.PlanReason == "within_budget" {
			contextPlan.PlanReason = strings.TrimSpace(reason)
		}
		searchDepth = "shallow"
		buildToolDefinitions()
		rebuildSystemPrompt()
		refreshEngineCaps()

		emergencyTarget := maxInt(1, (target*6)/10)
		if emergencyTarget < 1 {
			emergencyTarget = target
		}
		recovered := false
		if !contextPlan.HandoverSummaryUsed && attemptCapRecovery(reason, emergencyTarget) {
			recovered = true
		} else {
			summary := m.summarizeHistoryForContextCheckpoint(history, reason)
			history = m.buildEmergencyContextHistory(history, summary, systemPrompt, toolDefs, emergencyTarget, tokenCharsPerToken)
			rebuildSystemPrompt()
			refreshEngineCaps()
			requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
			if requestTokenEstimate > target {
				history = m.buildEmergencyContextHistory(history, summary, systemPrompt, toolDefs, maxInt(1, emergencyTarget/2), tokenCharsPerToken)
				rebuildSystemPrompt()
				refreshEngineCaps()
				requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
			}
			recovered = requestTokenEstimate <= target
		}

		if !recovered {
			history = m.shrinkLatestUserForTokenBudget(history, systemPrompt, toolDefs, target, tokenCharsPerToken)
			rebuildSystemPrompt()
			refreshEngineCaps()
			requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
			recovered = requestTokenEstimate <= target
		}

		after := estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		contextPlan.TokensAfter = after
		emitLocalProgress(LocalPhaseContextCompacting, LocalProgressStatusCompleted, target, before, after, after < before)
		if !recovered {
			log.Printf(
				"run %s: context recovery did not fit target reason=%s before=%d after=%d target=%d",
				run.ID,
				reason,
				before,
				after,
				target,
			)
			return false
		}

		// Recovery may rebuild from the pre-run store snapshot. Re-apply exact
		// target filtering, but never bind those historical unbound payloads to
		// the current target.
		history = providerHistoryForRun(history, run, false)
		providerState = providerStateFromHistoryForRun(history, run)
		checkpoint := managerContextCheckpoint{
			ThreadID:              thread.ID,
			RunID:                 &run.ID,
			SourceMessageSeq:      maxMessageSeq(filtered),
			SourceMessageID:       latestMessageID(filtered),
			ReplacementHistory:    m.checkpointSafeHistory(history),
			Summary:               m.summarizeHistoryForContextCheckpoint(history, reason),
			SummaryMessageIDs:     idsFromMessages(filtered),
			ReferencedArtifactIDs: checkpointReferencedArtifactIDs(filtered),
			TokenEstimate:         after,
			CharsPerToken:         tokenCharsPerToken,
			Model:                 run.Model,
			ProviderID:            run.ProviderID,
			Plan:                  contextPlan,
		}
		emitLocalProgress(localPhaseContextCheckpoint, LocalProgressStatusStarted, target, before, after, true)
		if persisted, err := m.persistActiveContextCheckpoint(context.Background(), thread, run, checkpoint); err != nil {
			log.Printf("run %s: failed to persist active context checkpoint: %v", run.ID, err)
		} else {
			emitLocalProgress(localPhaseContextCheckpoint, LocalProgressStatusCompleted, target, before, after, true)
			log.Printf(
				"run %s: wrote active context checkpoint id=%s thread=%s source_seq=%d tokens_before=%d tokens_after=%d reason=%s",
				run.ID,
				persisted.ID,
				thread.ID,
				persisted.SourceMessageSeq,
				before,
				after,
				reason,
			)
		}
		fields := m.runContextUpdateFields(contextPlan)
		if m.longHorizon == nil {
			if err := m.updateRunStatusWithFallback(context.Background(), run.ID, "running", fields); err != nil {
				log.Printf("run %s: failed to persist context recovery metadata: %v", run.ID, err)
			}
		}
		emitLocalProgress(localPhaseContextRetry, LocalProgressStatusCompleted, target, before, after, true)
		runInputCap = target
		return true
	}

	ensureCap := func(target int, reason string) bool {
		trimBudget := maxInt(1, target)
		if target <= 0 {
			return true
		}
		if requestTokenEstimate <= target {
			return true
		}
		contextPlan.OverflowCompacted = true
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		if strings.TrimSpace(contextPlan.PlanReason) == "" || contextPlan.PlanReason == "within_budget" {
			contextPlan.PlanReason = reason
		}

		singleMessageTrimStalled := false
		for i := 0; i < retryLimit; i++ {
			if !contextPlan.HandoverSummaryUsed && attemptCapRecovery(reason, target) {
				return true
			}
			prevTokenEstimate := requestTokenEstimate
			prevLen := len(history)
			history = m.trimHistoryToTokenBudget(history, systemPrompt, toolDefs, trimBudget, tokenCharsPerToken)
			rebuildSystemPrompt()
			requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
			if requestTokenEstimate <= target {
				return true
			}
			if prevLen == 1 && len(history) == 1 && requestTokenEstimate >= prevTokenEstimate {
				// Single-message prompts that exceed the budget cannot be reduced by drop-only trimming.
				// Stop retry churn and let caller decide hard-cap handling.
				singleMessageTrimStalled = true
				break
			}
		}
		if singleMessageTrimStalled {
			return false
		}

		history = m.trimHistoryToTokenBudget(history, systemPrompt, toolDefs, trimBudget, tokenCharsPerToken)
		rebuildSystemPrompt()
		requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		return requestTokenEstimate <= target
	}

	if runContextPlanner == nil && activeInitialHardOverflow {
		contextPlan.HardCapped = true
		contextPlan.OverflowCompacted = true
		if strings.TrimSpace(contextPlan.PlanReason) == "" || strings.TrimSpace(contextPlan.PlanReason) == "fixed_window_fallback" {
			contextPlan.PlanReason = "hard_cap_exceeded"
		}
		if !recoverContextOverflow("hard_cap_exceeded", hardInputCap) {
			failRunAfterContextRecovery("hard_cap_exceeded", "This run could not be safely continued. Please retry.")
			return
		}
	}
	softCapPassed := ensureCap(engineInputCap, "input_cap_recovery")
	if !softCapPassed {
		if !ensureCap(hardInputCap, "hard_cap_recovery") && !applyNearCapacityFallback("hard_cap_recovery") {
			if !recoverContextOverflow("hard_cap_recovery", hardInputCap) {
				failRunAfterContextRecovery("hard_cap_recovery", "This run could not be safely continued. Please retry.")
				return
			}
		}
		runInputCap = hardInputCap
		contextPlan.HardCapped = true
	}
	reconcileHardCap := func(reason string) bool {
		requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		if requestTokenEstimate <= hardInputCap {
			return true
		}
		if !ensureCap(hardInputCap, reason) && !applyNearCapacityFallback(reason) {
			return false
		}
		runInputCap = hardInputCap
		requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		return requestTokenEstimate <= hardInputCap
	}
	if !reconcileHardCap("hard_cap_pre_dispatch") {
		if !recoverContextOverflow("hard_cap_pre_dispatch", hardInputCap) {
			failRunAfterContextRecovery("hard_cap_pre_dispatch", "This run could not be safely continued. Please retry.")
			return
		}
	}
	proactiveDispatchCap := handoverTriggerTokenLimit(ContextPolicy{
		MaxInputTokens:     engineInputCap,
		NeedRetrievalLimit: defaultSummaryNeedTokenLimit,
	})
	if engineInputCap >= minProactiveHandoverTokens && requestTokenEstimate > proactiveDispatchCap {
		if !recoverContextOverflow("proactive_context_handoff", proactiveDispatchCap) {
			failRunAfterContextRecovery("proactive_context_handoff", "This run could not be safely continued. Please retry.")
			return
		}
	}

	contextPlan.TokensAfter = requestTokenEstimate
	if !checkpointUsed && contextPlan.HandoverSummaryUsed && requestTokenEstimate <= hardInputCap {
		checkpointSourceSeq := maxMessageSeq(filtered)
		checkpointSourceID := latestMessageID(filtered)
		checkpointHistory := m.checkpointSafeHistory(history)
		checkpointSummary := strings.TrimSpace(plannerResult.SummaryForUser)
		checkpointMessageIDs := idsFromMessages(filtered)
		checkpointArtifactIDs := checkpointReferencedArtifactIDs(filtered)
		checkpointTokenEstimate := requestTokenEstimate
		if plannerResult.Checkpoint != nil {
			checkpointSourceSeq = plannerResult.Checkpoint.SourceMessageSeq
			checkpointSourceID = plannerResult.Checkpoint.SourceMessageID
			checkpointHistory = m.checkpointSafeHistory(plannerResult.Checkpoint.ReplacementHistory)
			checkpointSummary = strings.TrimSpace(plannerResult.Checkpoint.Summary)
			checkpointMessageIDs = dedupeStringSlice(plannerResult.Checkpoint.ReferencedMessageIDs)
			checkpointArtifactIDs = dedupeStringSlice(plannerResult.Checkpoint.ReferencedArtifactIDs)
			if plannerResult.Checkpoint.TokenEstimate > 0 {
				checkpointTokenEstimate = plannerResult.Checkpoint.TokenEstimate
			}
		}
		checkpoint := managerContextCheckpoint{
			ThreadID:              thread.ID,
			RunID:                 &run.ID,
			SourceMessageSeq:      checkpointSourceSeq,
			SourceMessageID:       checkpointSourceID,
			ReplacementHistory:    checkpointHistory,
			Summary:               checkpointSummary,
			SummaryMessageIDs:     checkpointMessageIDs,
			ReferencedArtifactIDs: checkpointArtifactIDs,
			TokenEstimate:         checkpointTokenEstimate,
			CharsPerToken:         tokenCharsPerToken,
			Model:                 run.Model,
			ProviderID:            run.ProviderID,
			Plan:                  contextPlan,
		}
		if persisted, err := m.persistActiveContextCheckpoint(context.Background(), thread, run, checkpoint); err != nil {
			log.Printf("run %s: failed to persist planner context checkpoint: %v", run.ID, err)
		} else {
			log.Printf(
				"run %s: wrote planner context checkpoint id=%s thread=%s source_seq=%d tokens=%d",
				run.ID,
				persisted.ID,
				thread.ID,
				persisted.SourceMessageSeq,
				requestTokenEstimate,
			)
		}
	}

	if requestTokenEstimate > hardInputCap {
		contextPlan.HardCapped = true
	}
	if requestTokenEstimate > maxContextInputTokens {
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		if contextPlan.PlanReason == "" {
			contextPlan.PlanReason = "input_cap_exceeded"
		}
	}
	log.Printf(
		"run %s: dispatch context tokens=%d input_cap=%d hard_cap=%d plan_reason=%s compacted=%t hard_capped=%t checkpoint_used=%t tools=%d",
		run.ID,
		requestTokenEstimate,
		engineInputCap,
		hardInputCap,
		strings.TrimSpace(contextPlan.PlanReason),
		contextPlan.OverflowCompacted,
		contextPlan.HardCapped,
		checkpointUsed,
		len(toolDefs),
	)

	hostedScopes := []runConnectionScope(nil)
	if req.ActiveConnectionIDsProvided {
		hostedScopes = append(hostedScopes, activeRunConnectionScope)
	}
	hostedToolPolicy, hostedToolPolicyErr := m.hostedToolPolicyForRun(ctx, thread.ID, hostedScopes...)
	if hostedToolPolicyErr != nil {
		log.Printf("run %s: failed to evaluate provider-hosted tool permissions: %v", run.ID, hostedToolPolicyErr)
		emitDiagnostic("HOSTED_TOOL_PERMISSION_WARNING", "failed to evaluate provider-hosted tool permissions; provider-hosted tools requiring approval will be omitted")
		hostedToolPolicy = providers.HostedToolPolicy{Disabled: map[string]bool{}}
		for _, capability := range tools.ProviderHostedToolCapabilities() {
			if capability.Category != tools.ToolCategoryRead {
				hostedToolPolicy.Disabled[capability.ID] = true
			}
		}
	}
	if recoverySnapshot != nil {
		if hostedToolPolicy.Disabled == nil {
			hostedToolPolicy.Disabled = map[string]bool{}
		}
		for _, capabilityID := range recoverySnapshot.ToolScope.HostedToolDenied {
			if capabilityID = strings.TrimSpace(capabilityID); capabilityID != "" {
				hostedToolPolicy.Disabled[capabilityID] = true
			}
		}
	}
	if recoverySnapshot == nil {
		hostedToolPolicy, hostedToolPolicyErr = m.applyHostedToolPlanCeiling(ctx, run, hostedToolPolicy)
		if hostedToolPolicyErr != nil {
			_, _ = m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusFailed, continuationClaimToken, map[string]any{
				"ended_at": time.Now().UTC(), "error_code": "PLAN_ERROR", "error_message": hostedToolPolicyErr.Error(),
			})
			emitTerminalError("The finalized run plan could not be applied safely.")
			emitTerminalFinal()
			m.finish(run.ID)
			return
		}
	}

	var runtimePlanPolicy *types.RuntimePlanPolicy
	if recoverySnapshot != nil {
		if recoverySnapshot.RuntimePlan == nil {
			runtimePlanPolicy = nil
		} else {
			copyPolicy := *recoverySnapshot.RuntimePlan
			runtimePlanPolicy = &copyPolicy
		}
	} else {
		var runtimePlanErr error
		runtimePlanPolicy, runtimePlanErr = m.runtimePlanPolicy(ctx, run)
		if runtimePlanErr != nil {
			_, _ = m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusFailed, continuationClaimToken, map[string]any{
				"ended_at": time.Now().UTC(), "error_code": "PLAN_ERROR", "error_message": runtimePlanErr.Error(),
			})
			emitTerminalError("The finalized run plan could not be loaded safely.")
			emitTerminalFinal()
			m.finish(run.ID)
			return
		}
	}

	// Run the agent loop.
	var loopErr error
	dispatchOrdinal := 0
	if recoverySnapshot != nil {
		dispatchOrdinal = recoverySnapshot.NextDispatchOrdinal
	}
	currentExecutionSnapshot := func() continuationExecutionSnapshot {
		deniedHostedTools := make([]string, 0, len(hostedToolPolicy.Disabled))
		for capabilityID, disabled := range hostedToolPolicy.Disabled {
			if disabled && strings.TrimSpace(capabilityID) != "" {
				deniedHostedTools = append(deniedHostedTools, strings.TrimSpace(capabilityID))
			}
		}
		sort.Strings(deniedHostedTools)
		definitions := []types.ToolDefinition(nil)
		if encoded, err := json.Marshal(toolDefs); err == nil {
			_ = json.Unmarshal(encoded, &definitions)
		}
		var runtimePlanCopy *types.RuntimePlanPolicy
		if runtimePlanPolicy != nil {
			value := *runtimePlanPolicy
			runtimePlanCopy = &value
		}
		return continuationExecutionSnapshot{
			RunID:    run.ID,
			ThreadID: thread.ID,
			Dispatch: continuationDispatchIdentity{
				ConnectionID: dispatchSnapshot.ConnectionID, LegacyProfileID: dispatchSnapshot.LegacyProfileID,
				ProviderID: dispatchSnapshot.ProviderID, Model: dispatchSnapshot.Model,
				ConfigFingerprint: dispatchSnapshot.ConfigFingerprint, PlannedAPISurface: dispatchSnapshot.PlannedAPISurface,
			},
			NextDispatchOrdinal: dispatchOrdinal,
			SystemPromptBase:    systemPromptBase,
			SystemPrompt:        systemPrompt,
			ToolDefinitions:     definitions,
			ToolScope: continuationToolScope{
				Datasources:  cloneDatasourceEnabledMap(enabledDatasources),
				ChartEcharts: enabledChartEngines.Echarts, ChartHighcharts: enabledChartEngines.Highcharts,
				ChartPipeline: enabledChartEngines.ChartProgramPipeline, ChartFilters: enabledChartEngines.ChartDimensionFilters,
				AtlassianJira: enabledAtlassian.Jira, AtlassianWiki: enabledAtlassian.Confluence,
				BrowserControl: enabledBrowserControl, SharePoint: enabledSharePoint, GitHub: enabledGitHub,
				SlackRead: enabledSlack.Read, SlackWrite: enabledSlack.Write, Arxiv: enabledArxiv,
				ThreadSearch: enabledThreadSearch, LocalComputer: enabledLocalComputer,
				LocalGrants: append([]localComputerPermissionGrant(nil), localComputerGrants...), HostedToolDenied: deniedHostedTools,
			},
			ProjectID: projectID, SearchMode: searchMode, SearchDepth: searchDepth, QueryRowLimit: queryRowLimit,
			ContextPlan: contextPlan, MaxContextInputTokens: maxContextInputTokens, MaxContextHardTokens: maxContextHardTokens,
			TokenCharsPerToken: tokenCharsPerToken, EngineInputCap: engineInputCap, HardInputCap: hardInputCap,
			RunInputCap: runInputCap, EvidenceBudget: evidenceBudget, EvidenceUsed: evidenceUsed,
			RuntimeReference: runtimeReferenceTime, RuntimePlan: runtimePlanCopy, NearCapFallbackApplied: nearCapFallbackApplied,
		}
	}
	automaticContinuations := run.ContinuationCount
	maxQueryContinuations := m.maxAutomaticQueryContinuations()
	if run.MaxAutoContinuations > 0 {
		maxQueryContinuations = run.MaxAutoContinuations
	}
	var suspensionRefs []queryDependencyRef
	allowActiveQueryReview := resumeCheckpoint != nil
	queryCompletionBarrier := func(_ engine.LoopControlPoint) *engine.LoopSuspension {
		refs := m.queryDependenciesForRun(thread.ID, run.ID)
		if len(refs) == 0 {
			return nil
		}
		suspensionRefs = append([]queryDependencyRef(nil), refs...)
		return &engine.LoopSuspension{
			Reason:        "required_query_running",
			DependencyIDs: queryDependencyIDs(refs),
		}
	}
	queryExecutionBarrier := func(_ engine.LoopControlPoint) *engine.LoopSuspension {
		refs := m.queryDependenciesForRun(thread.ID, run.ID)
		active := make([]queryDependencyRef, 0, len(refs))
		for _, ref := range refs {
			if strings.HasPrefix(strings.TrimSpace(ref.ID), "dependency-fault-") {
				// A protocol/persistence fault must block final completion, but it
				// must not suspend immediately after the failed tool call: the model
				// needs a reasoning turn to execute its corrective alternative plan.
				continue
			}
			if strings.TrimSpace(ref.Owner.Handle) == "" || !queryJobTerminal(ref.Status) {
				active = append(active, ref)
			}
		}
		if len(active) == 0 {
			return nil
		}
		if allowActiveQueryReview {
			// A monitor/policy wake exists to let the model inspect evidence and
			// choose a diagnostic, rewrite, replacement, or continued wait. Give
			// it one complete post-tool reasoning turn before suspending again.
			allowActiveQueryReview = false
			return nil
		}
		suspensionRefs = append([]queryDependencyRef(nil), active...)
		return &engine.LoopSuspension{
			Reason:        "required_query_running",
			DependencyIDs: queryDependencyIDs(active),
		}
	}
	moveRunToWaitingUser := func(reason string, dependencyIDs []string) {
		// Parking a run is a persistence boundary, not a best-effort UI update.
		// Release model capacity, then retry transient store failures until the
		// run is durably waiting_user or another authoritative state owns it.
		releaseModelSlot()
		retryDelay := 25 * time.Millisecond
		for attempt := 1; ; attempt++ {
			var transitionErr error
			if m.longHorizon != nil {
				transitionErr = m.transitionRunToWaitingUser(context.Background(), run.ID, automaticContinuations, maxQueryContinuations, continuationClaimToken)
			} else {
				fields := m.runContextUpdateFields(contextPlan)
				transitionErr = m.updateRunStatusWithFallback(context.Background(), run.ID, "waiting_user", fields)
			}
			if transitionErr == nil {
				break
			}
			current, currentErr := m.store.GetRun(context.Background(), run.ID)
			if currentErr == nil {
				switch current.Status {
				case store.RunStatusWaitingUser:
					transitionErr = nil
				case store.RunStatusCompleted, store.RunStatusFailed, store.RunStatusCancelled,
					store.RunStatusWaitingExternal, store.RunStatusResumePending:
					log.Printf("run %s: waiting_user transition yielded to authoritative status %s", run.ID, current.Status)
					m.finish(run.ID)
					return
				case store.RunStatusResuming:
					leaseExpired := current.ResumeLeaseExpiresAt == nil || !current.ResumeLeaseExpiresAt.After(time.Now().UTC())
					if strings.TrimSpace(current.ResumeLeaseHolder) != strings.TrimSpace(continuationClaimToken) || leaseExpired {
						log.Printf("run %s: waiting_user transition yielded after continuation ownership expired or changed", run.ID)
						m.finish(run.ID)
						return
					}
				case store.RunStatusRunning:
					if token := strings.TrimSpace(continuationClaimToken); token != "" {
						leaseExpired := current.ResumeLeaseExpiresAt == nil || !current.ResumeLeaseExpiresAt.After(time.Now().UTC())
						if strings.TrimSpace(current.ResumeLeaseHolder) != token || leaseExpired {
							log.Printf("run %s: waiting_user transition yielded after initial execution ownership expired or changed", run.ID)
							m.finish(run.ID)
							return
						}
					}
				}
				if transitionErr == nil {
					break
				}
			} else if errors.Is(currentErr, store.ErrNotFound) {
				m.finish(run.ID)
				return
			}
			if attempt == 1 || attempt%10 == 0 {
				log.Printf("run %s: retrying durable waiting_user transition after error: %v", run.ID, transitionErr)
			}
			timer := time.NewTimer(retryDelay)
			<-timer.C
			if retryDelay < 2*time.Second {
				retryDelay *= 2
				if retryDelay > 2*time.Second {
					retryDelay = 2 * time.Second
				}
			}
		}
		emit("run_waiting_user", map[string]any{
			"run_id":             run.ID,
			"reason":             strings.TrimSpace(reason),
			"dependency_ids":     dependencyIDs,
			"continuation_count": automaticContinuations,
		})
		m.finish(run.ID)
	}
	deferRunFailureForQueryDependencies = func(reason string) bool {
		refs := m.queryDependenciesForRun(thread.ID, run.ID)
		if len(refs) == 0 {
			return false
		}
		moveRunToWaitingUser("execution paused while required query work remains: "+strings.TrimSpace(reason), queryDependencyIDs(refs))
		return true
	}
	const maxProviderDispatchAttempts = 4
	contextRecoveryAttempts := 0
	initialExecutionLeaseActive := initialExecutionLease
	for {
		if continuationClaimToken != "" {
			current, currentErr := m.store.GetRun(context.Background(), run.ID)
			if currentErr != nil {
				if initialExecutionLeaseActive {
					releaseModelSlot()
					if parkErr := m.parkClaimedInitialRun(context.Background(), run.ID, continuationClaimToken); parkErr != nil && !errors.Is(parkErr, store.ErrConflict) {
						log.Printf("run %s: failed to park initial execution after ownership verification error; lease expiry will recover it: %v", run.ID, parkErr)
					}
					leaseGuard.Clear(continuationClaimToken)
					m.finish(run.ID)
					return
				}
				_ = m.requeueClaimedRun(context.Background(), run.ID, continuationClaimToken)
				leaseGuard.Clear(continuationClaimToken)
				m.finish(run.ID)
				return
			}
			_, supersedingRunID, supersessionErr := m.disarmIfSuperseded(context.Background(), current, nil)
			if supersessionErr != nil {
				if initialExecutionLeaseActive {
					releaseModelSlot()
					if parkErr := m.parkClaimedInitialRun(context.Background(), run.ID, continuationClaimToken); parkErr != nil && !errors.Is(parkErr, store.ErrConflict) {
						log.Printf("run %s: failed to park initial execution after generation verification error; lease expiry will recover it: %v", run.ID, parkErr)
					}
					leaseGuard.Clear(continuationClaimToken)
					m.finish(run.ID)
					return
				}
				_ = m.requeueClaimedRun(context.Background(), run.ID, continuationClaimToken)
				leaseGuard.Clear(continuationClaimToken)
				m.finish(run.ID)
				return
			}
			if supersedingRunID != "" {
				leaseGuard.Clear(continuationClaimToken)
				m.finish(run.ID)
				return
			}
		}
		if !reconcileHardCap("runtime_dispatch_recovery") {
			if !recoverContextOverflow("runtime_dispatch_recovery", hardInputCap) {
				failRunAfterContextRecovery("runtime_dispatch_recovery", "This run could not be safely continued. Please retry.")
				return
			}
		}
		loopHistory, err := engine.RunAgentLoop(ctx, engine.Params{
			ProviderFactory: func() (types.Provider, error) {
				return m.providers.CreateProviderFromSnapshot(dispatchSnapshot, hostedToolPolicy)
			},
			DispatchOrdinal:      &dispatchOrdinal,
			SystemPrompt:         systemPrompt,
			Model:                run.Model,
			History:              history,
			Tools:                toolDefs,
			ProviderState:        providerState,
			RuntimePlan:          runtimePlanPolicy,
			ExecuteTool:          execTool,
			AfterToolBatch:       queryExecutionBarrier,
			BeforeAssistantFinal: queryCompletionBarrier,
			Callbacks:            cb,
			CompactHistory:       midRunCompact,
			MaxIterations:        run.MaxIterations,
			MaxInputTokens:       runInputCap,
			TokenEstimator: func(systemPrompt string, history []types.ChatMessage, toolDefs []types.ToolDefinition) int {
				return estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
			},
		})
		if loopHistory != nil {
			// The loop starts from already-filtered history, so any newly unbound
			// payload here was produced by this exact target during the current
			// run and can safely receive the target binding before recovery.
			history = providerHistoryForRun(loopHistory, run, true)
		}
		loopErr = err
		if loopErr == nil && callbackErr != nil {
			loopErr = callbackErr
		}
		requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
		contextPlan.TokensAfter = requestTokenEstimate
		if loopErr == nil {
			break
		}

		var suspended *engine.LoopSuspendedError
		if errors.As(loopErr, &suspended) {
			refs := append([]queryDependencyRef(nil), suspensionRefs...)
			// A query may have completed between the barrier observation and
			// this branch. In that case, resume immediately from the exact
			// checkpoint without consuming a continuation wake.
			if len(refs) == 0 {
				refs = m.queryDependenciesForRun(thread.ID, run.ID)
			}
			dependencyIDs := queryDependencyIDs(refs)
			if len(dependencyIDs) == 0 {
				loopErr = nil
				continue
			}
			if m.longHorizon != nil {
				suspendingClaimToken := continuationClaimToken
				leaseGuard.Clear(suspendingClaimToken)
				_, waitingRun, suspendErr := m.suspendRunAtCheckpoint(
					context.Background(), run, suspended.Point, dependencyIDs, currentExecutionSnapshot(),
					automaticContinuations, maxQueryContinuations, suspendingClaimToken,
				)
				if suspendErr != nil {
					if leaseGuard.Lost() || errors.Is(suspendErr, store.ErrConflict) {
						m.finish(run.ID)
						return
					}
					moveRunToWaitingUser("durable atomic suspension failed: "+suspendErr.Error(), dependencyIDs)
					return
				}
				continuationClaimToken = ""
				initialExecutionLeaseActive = false
				run = waitingRun
			} else {
				fields := m.runContextUpdateFields(contextPlan)
				if err := m.updateRunStatusWithFallback(context.Background(), run.ID, "waiting_external", fields); err != nil {
					log.Printf("run %s: failed to update status to waiting_external: %v", run.ID, err)
				}
			}
			emit("run_waiting_external", map[string]any{
				"run_id":             run.ID,
				"reason":             suspended.Suspension.Reason,
				"dependency_ids":     dependencyIDs,
				"continuation_count": automaticContinuations,
			})

			// Waiting on the warehouse must not occupy scarce model capacity.
			releaseModelSlot()
			wakeKind, waitErr := m.waitForQueryDependencyWake(ctx, refs)
			if waitErr != nil {
				if m.deferSuspendedQueryWaitError(ctx, run.ID, waitErr) {
					return
				}
				loopErr = waitErr
				break
			}
			if m.longHorizon != nil {
				claimedRun, claimOutcome, claimToken, claimErr := m.claimRunContinuation(context.Background(), run.ID)
				if claimErr != nil {
					log.Printf("run %s: continuation claim deferred for recovery retry: %v", run.ID, claimErr)
					m.finish(run.ID)
					return
				}
				switch claimOutcome {
				case continuationClaimCapReached:
					automaticContinuations = claimedRun.ContinuationCount
					moveRunToWaitingUser("automatic query continuation limit reached", dependencyIDs)
					return
				case continuationClaimHeldElsewhere:
					// Another process owns the continuation. This local execution
					// yields without changing the durable run.
					m.finish(run.ID)
					return
				}
				run = claimedRun
				continuationClaimToken = claimToken
				leaseGuard.Hold(claimToken, claimedRun.ResumeLeaseExpiresAt)
				automaticContinuations = claimedRun.ContinuationCount
			} else {
				automaticContinuations++
				if automaticContinuations > maxQueryContinuations {
					moveRunToWaitingUser("automatic query continuation limit reached", dependencyIDs)
					return
				}
			}
			if err := acquireModelSlot(); err != nil {
				loopErr = err
				break
			}
			allowActiveQueryReview = true
			wakeNotice := fmt.Sprintf(
				"[Orchestrator query wake: %s] Inspect the durable query status and evidence. If it is healthy, continue waiting without cancelling it. If it is failed, unrecoverable, or demonstrably wrong, pursue a diagnostic/rewrite/replacement plan; do not abandon the task.",
				strings.TrimSpace(string(wakeKind)),
			)
			history = append(history, types.ChatMessage{Role: types.RoleSystem, Content: &wakeNotice})
			if notice := queryContinuationIdentityNotice(m.queryDependenciesForRun(thread.ID, run.ID)); notice != "" {
				history = append(history, types.ChatMessage{Role: types.RoleSystem, Content: &notice})
			}
			if m.longHorizon == nil {
				if err := m.updateRunStatusWithFallback(context.Background(), run.ID, "resuming", m.runContextUpdateFields(contextPlan)); err != nil {
					log.Printf("run %s: failed to update status to resuming: %v", run.ID, err)
				}
			}
			emit("run_resuming", map[string]any{
				"run_id":             run.ID,
				"wake_kind":          string(wakeKind),
				"dependency_ids":     dependencyIDs,
				"continuation_count": automaticContinuations,
			})
			if m.longHorizon == nil {
				if err := m.updateRunStatusWithFallback(context.Background(), run.ID, "running", m.runContextUpdateFields(contextPlan)); err != nil {
					log.Printf("run %s: failed to restore running status: %v", run.ID, err)
				}
			}
			providerState = cloneStringAnyMap(suspended.Point.ProviderState)
			suspensionRefs = nil
			loopErr = nil
			continue
		}

		var exhausted *engine.MaxIterationsError
		if errors.As(loopErr, &exhausted) {
			providerState = cloneStringAnyMap(exhausted.Point.ProviderState)
			if m.longHorizon != nil {
				suspendingClaimToken := continuationClaimToken
				leaseGuard.Clear(suspendingClaimToken)
				_, waitingRun, suspendErr := m.suspendRunAtCheckpoint(
					context.Background(), run, exhausted.Point, nil, currentExecutionSnapshot(),
					automaticContinuations, maxQueryContinuations, suspendingClaimToken,
				)
				if suspendErr != nil {
					if leaseGuard.Lost() || errors.Is(suspendErr, store.ErrConflict) {
						m.finish(run.ID)
						return
					}
					moveRunToWaitingUser("durable atomic iteration suspension failed: "+suspendErr.Error(), nil)
					return
				}
				continuationClaimToken = ""
				initialExecutionLeaseActive = false
				run = waitingRun
				claimedRun, claimOutcome, claimToken, claimErr := m.claimRunContinuation(context.Background(), run.ID)
				if claimErr != nil {
					log.Printf("run %s: iteration continuation claim deferred for recovery retry: %v", run.ID, claimErr)
					m.finish(run.ID)
					return
				}
				automaticContinuations = claimedRun.ContinuationCount
				switch claimOutcome {
				case continuationClaimCapReached:
					moveRunToWaitingUser("model tool-iteration continuation limit reached", nil)
					return
				case continuationClaimHeldElsewhere:
					m.finish(run.ID)
					return
				}
				run = claimedRun
				continuationClaimToken = claimToken
				leaseGuard.Hold(claimToken, claimedRun.ResumeLeaseExpiresAt)
			} else {
				automaticContinuations++
				if automaticContinuations > maxQueryContinuations {
					moveRunToWaitingUser("model tool-iteration continuation limit reached", nil)
					return
				}
			}
			emitDiagnostic("TOOL_ITERATION_CONTINUATION", fmt.Sprintf(
				"Continuing from the exact tool checkpoint after iteration segment %d of %d.",
				automaticContinuations,
				maxQueryContinuations,
			))
			loopErr = nil
			continue
		}
		if !isRecoverableContextOverflowError(loopErr) {
			break
		}
		contextRecoveryAttempts++
		contextPlan.HardCapped = true
		contextPlan.OverflowCompacted = true
		contextPlan.NeedRetrieval = true
		contextPlan.NeedRetrievalTooling = true
		if strings.TrimSpace(contextPlan.PlanReason) == "" {
			contextPlan.PlanReason = "runtime_context_overflow"
		}
		if isAnthropicProvider && applyNearCapacityFallback("runtime_context_overflow") {
			history = m.trimHistoryToTokenBudget(history, systemPrompt, toolDefs, hardInputCap, tokenCharsPerToken)
			requestTokenEstimate = estimateRequestTokensWithTools(systemPrompt, history, toolDefs, tokenCharsPerToken)
			runInputCap = hardInputCap
			continue
		}

		if !ensureCap(hardInputCap, "runtime_context_overflow_recovery") && !recoverContextOverflow("runtime_context_overflow_recovery", hardInputCap) {
			break
		}
		runInputCap = hardInputCap
		contextPlan.HardCapped = true
		if contextRecoveryAttempts >= maxProviderDispatchAttempts {
			break
		}
		loopErr = nil
		continue
	}

	ended := time.Now().UTC()

	switch {
	case errors.Is(loopErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
		if leaseGuard.Lost() {
			m.finish(run.ID)
			return
		}
		changed, err := m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusCancelled, continuationClaimToken, map[string]any{"ended_at": ended})
		if err != nil && !errors.Is(err, store.ErrConflict) {
			log.Printf("run %s: failed to update status to cancelled: %v", run.ID, err)
		}
		if !changed && m.longHorizon != nil {
			m.finish(run.ID)
			return
		}
		// Keep any partial assistant text already streamed/persisted; do not overwrite it with a cancellation stub.
		emitTerminalCancelled()
		m.finish(run.ID)
		return
	case loopErr != nil:
		if isRecoverableContextOverflowError(loopErr) {
			if strings.TrimSpace(contextPlan.PlanReason) == "" {
				contextPlan.PlanReason = "engine_context_overflow"
			}
			contextPlan.HardCapped = true
			contextPlan.OverflowCompacted = true
			contextPlan.NeedRetrieval = true
			contextPlan.NeedRetrievalTooling = true
			failRunAfterContextRecovery("engine_context_overflow", "This run could not be safely continued. Please retry.")
			return
		}
		if deferRunFailureForQueryDependencies("execution_error") {
			return
		}
		fields := m.runContextUpdateFields(contextPlan)
		fields["error_code"] = "FAILED"
		fields["error_message"] = loopErr.Error()
		fields["ended_at"] = ended
		changed, err := m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusFailed, continuationClaimToken, fields)
		if err != nil {
			log.Printf("run %s: failed to update status to failed: %v", run.ID, err)
		}
		if !changed {
			m.finish(run.ID)
			return
		}
		emitTerminalError(loopErr.Error())
		emitTerminalFinal()
		m.finish(run.ID)
		return
	default:
		fields := m.runContextUpdateFields(contextPlan)
		fields["ended_at"] = ended
		if lastMeta != nil {
			if lastMeta.RequestID != nil {
				fields["request_id"] = *lastMeta.RequestID
			}
			if lastMeta.InputTokens != nil {
				fields["input_tokens"] = *lastMeta.InputTokens
			}
			if lastMeta.OutputTokens != nil {
				fields["output_tokens"] = *lastMeta.OutputTokens
			}
			if lastMeta.TotalTokens != nil {
				fields["total_tokens"] = *lastMeta.TotalTokens
			}
		}
		changed, err := m.transitionRunTerminal(context.Background(), run.ID, store.RunStatusCompleted, continuationClaimToken, fields)
		if err != nil {
			log.Printf("run %s: failed to update status to completed: %v", run.ID, err)
		}
		if !changed {
			m.finish(run.ID)
			return
		}

		// Track how many completed turns happened in the thread (used for summary cadence).
		_ = m.store.IncrementThreadRunCount(context.Background(), thread.ID)
		if t2, err := m.store.GetThread(context.Background(), thread.ID); err == nil {
			thread = t2
		}

		// Best-effort: enqueue title/summary jobs after completion.
		m.enqueuePostRunJobs(context.Background(), thread, run)

		emitTerminalCompleted()
		m.finish(run.ID)
		return
	}
}

func (m *Manager) enqueuePostRunJobs(ctx context.Context, thread store.Thread, run store.Run) {
	// Title only if still default.
	if strings.TrimSpace(thread.Title) == "" || strings.EqualFold(strings.TrimSpace(thread.Title), "New Thread") {
		_, _ = m.store.EnqueueJob(ctx, "thread_title", thread.ID, &run.ID)
	}

	// Summary: avoid calling the model every turn. Refresh only when:
	// 1) the thread has grown past a baseline size, AND
	// 2) either there's no summary yet or it's older than N completed runs.
	const minMessagesForSummary = 30
	const summarizeEveryRuns = 3

	msgs, err := m.store.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    minMessagesForSummary + 1,
	})
	if err != nil || len(msgs) < minMessagesForSummary {
		return
	}

	summary := ""
	if thread.Summary != nil {
		summary = strings.TrimSpace(*thread.Summary)
	}
	if summary == "" {
		_, _ = m.store.EnqueueJob(ctx, "thread_summarize", thread.ID, &run.ID)
		return
	}

	if summarizeEveryRuns > 0 && (thread.RunCount-thread.SummaryRunCount) >= int64(summarizeEveryRuns) {
		_, _ = m.store.EnqueueJob(ctx, "thread_summarize", thread.ID, &run.ID)
	}
}

func (m *Manager) runContextUpdateFields(plan RunContextPlan) map[string]any {
	out := map[string]any{}
	if plan.TokensBefore > 0 {
		out["context_tokens_before"] = plan.TokensBefore
	}
	if plan.TokensAfter > 0 {
		out["context_tokens_after"] = plan.TokensAfter
	}
	out["context_summary_buckets"] = len(plan.SummarizedBuckets)
	if plan.SummaryRounds > 0 {
		out["context_summary_rounds"] = plan.SummaryRounds
	}
	if plan.ArtifactRefsLoaded > 0 {
		out["context_artifact_refs_loaded"] = plan.ArtifactRefsLoaded
	}
	if plan.EvidenceBudget > 0 {
		out["context_evidence_budget"] = plan.EvidenceBudget
	}
	if plan.EvidenceUsed > 0 {
		out["context_evidence_used"] = plan.EvidenceUsed
	}
	out["context_evidence_exhausted"] = plan.EvidenceExhausted
	out["context_need_retrieval"] = plan.NeedRetrieval
	out["context_need_retrieval_tooling"] = plan.NeedRetrievalTooling
	out["context_hard_capped"] = plan.HardCapped
	out["context_overflow_compacted"] = plan.OverflowCompacted
	if planBlob, err := json.Marshal(plan); err == nil && len(planBlob) > 0 {
		out["context_plan"] = json.RawMessage(planBlob)
	}
	return out
}

func (m *Manager) loadActiveContextCheckpoint(ctx context.Context, threadID string) (*managerContextCheckpoint, error) {
	cp, err := m.store.GetActiveContextCheckpoint(ctx, threadID)
	if err != nil {
		return nil, err
	}
	if cp == nil {
		return nil, nil
	}
	return managerCheckpointFromStore(*cp)
}

func (m *Manager) persistActiveContextCheckpoint(ctx context.Context, thread store.Thread, run store.Run, checkpoint managerContextCheckpoint) (*managerContextCheckpoint, error) {
	checkpoint.Kind = contextCheckpointEnvelopeKind
	checkpoint.Version = contextCheckpointEnvelopeVersion
	if strings.TrimSpace(checkpoint.ID) == "" {
		checkpoint.ID = types.NewUUID()
	}
	checkpoint.ThreadID = strings.TrimSpace(thread.ID)
	if checkpoint.RunID == nil && strings.TrimSpace(run.ID) != "" {
		checkpoint.RunID = &run.ID
	}
	checkpoint.ReplacementHistory = m.checkpointSafeHistory(checkpoint.ReplacementHistory)
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}
	storeCheckpoint, err := storeCheckpointFromManager(checkpoint)
	if err != nil {
		return nil, err
	}
	persisted, err := m.store.UpsertActiveContextCheckpoint(ctx, storeCheckpoint)
	if err != nil {
		return nil, err
	}
	return managerCheckpointFromStore(persisted)
}

func managerCheckpointFromStore(cp store.ContextCheckpoint) (*managerContextCheckpoint, error) {
	var plan RunContextPlan
	if len(cp.PlanJSON) > 0 {
		if err := json.Unmarshal(cp.PlanJSON, &plan); err != nil {
			return nil, fmt.Errorf("decode context checkpoint plan: %w", err)
		}
	}
	out := &managerContextCheckpoint{
		Kind:                  contextCheckpointEnvelopeKind,
		Version:               contextCheckpointEnvelopeVersion,
		ID:                    strings.TrimSpace(cp.ID),
		ThreadID:              strings.TrimSpace(cp.ThreadID),
		RunID:                 cloneOptionalStringPtr(cp.RunID),
		SourceMessageSeq:      cp.SourceMessageSeq,
		SourceMessageID:       strings.TrimSpace(cp.SourceMessageID),
		ReplacementHistory:    cloneChatHistory(cp.ReplacementHistory),
		Summary:               strings.TrimSpace(cp.Summary),
		SummaryMessageIDs:     dedupeStringSlice(cp.SummaryMessageIDs),
		ReferencedArtifactIDs: dedupeStringSlice(cp.ReferencedArtifactIDs),
		TokenEstimate:         cp.TokenEstimate,
		CharsPerToken:         cp.CharsPerToken,
		Model:                 strings.TrimSpace(cp.Model),
		ProviderID:            strings.TrimSpace(cp.ProviderID),
		Plan:                  plan,
		CreatedAt:             cp.CreatedAt,
	}
	return out, nil
}

func storeCheckpointFromManager(cp managerContextCheckpoint) (store.ContextCheckpoint, error) {
	planBlob, err := json.Marshal(cp.Plan)
	if err != nil {
		return store.ContextCheckpoint{}, err
	}
	return store.ContextCheckpoint{
		ID:                    strings.TrimSpace(cp.ID),
		ThreadID:              strings.TrimSpace(cp.ThreadID),
		RunID:                 cloneOptionalStringPtr(cp.RunID),
		SourceMessageSeq:      cp.SourceMessageSeq,
		SourceMessageID:       strings.TrimSpace(cp.SourceMessageID),
		ReplacementHistory:    cloneChatHistory(cp.ReplacementHistory),
		Summary:               strings.TrimSpace(cp.Summary),
		SummaryMessageIDs:     dedupeStringSlice(cp.SummaryMessageIDs),
		ReferencedArtifactIDs: dedupeStringSlice(cp.ReferencedArtifactIDs),
		TokenEstimate:         cp.TokenEstimate,
		CharsPerToken:         cp.CharsPerToken,
		Model:                 strings.TrimSpace(cp.Model),
		ProviderID:            strings.TrimSpace(cp.ProviderID),
		PlanJSON:              json.RawMessage(planBlob),
		CreatedAt:             cp.CreatedAt,
	}, nil
}

func contextCheckpointPayloadFromManager(cp *managerContextCheckpoint) *ContextCheckpointPayload {
	if cp == nil {
		return nil
	}
	summarySource := strings.TrimSpace(cp.Plan.SummarySource)
	if summarySource == "" {
		summarySource = ContextSummarySourceActiveCheckpoint
	}
	tokenEstimate := cp.TokenEstimate
	if tokenEstimate <= 0 {
		tokenEstimate = estimateChatTokens(cp.ReplacementHistory, cp.CharsPerToken)
	}
	return &ContextCheckpointPayload{
		ReplacementHistory:    cloneChatHistory(cp.ReplacementHistory),
		SourceMessageSeq:      cp.SourceMessageSeq,
		SourceMessageID:       strings.TrimSpace(cp.SourceMessageID),
		Summary:               strings.TrimSpace(cp.Summary),
		SummarySource:         summarySource,
		ReferencedMessageIDs:  dedupeStringSlice(cp.SummaryMessageIDs),
		ReferencedArtifactIDs: dedupeStringSlice(cp.ReferencedArtifactIDs),
		TokenEstimate:         tokenEstimate,
		CharsPerToken:         cp.CharsPerToken,
	}
}

func cloneOptionalStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	if trimmed == "" {
		return nil
	}
	out := trimmed
	return &out
}

func (m *Manager) buildHistoryFromActiveCheckpoint(ctx context.Context, threadID string, checkpoint *managerContextCheckpoint, msgs []store.Message, userMsgID string, hiddenContext string) ([]types.ChatMessage, []store.Message, bool) {
	if checkpoint == nil || checkpoint.SourceMessageSeq <= 0 || len(checkpoint.ReplacementHistory) == 0 {
		return nil, nil, false
	}
	delta := make([]store.Message, 0)
	for _, msg := range msgs {
		if msg.Seq > checkpoint.SourceMessageSeq {
			delta = append(delta, msg)
		}
	}
	history := m.checkpointSafeHistory(checkpoint.ReplacementHistory)
	history = m.hydrateHistoryFileAttachments(ctx, threadID, history)
	history = append(history, m.buildLegacyHistory(ctx, threadID, delta, userMsgID, hiddenContext)...)
	return m.truncateHistory(history), delta, true
}

func (m *Manager) checkpointSafeHistory(history []types.ChatMessage) []types.ChatMessage {
	out := make([]types.ChatMessage, 0, len(history))
	for _, msg := range history {
		if msg.Content != nil {
			content := types.SafeTruncate(*msg.Content)
			msg.Content = &content
		}
		msg.ImageAttachments = nil
		msg.FileAttachments = fileAttachmentRefsForAttachments(msg.FileAttachments)
		msg.ProviderPayload = cloneRawJSON(msg.ProviderPayload)
		out = append(out, msg)
	}
	return out
}

func (m *Manager) summarizeHistoryForContextCheckpoint(history []types.ChatMessage, reason string) string {
	const maxSummaryChars = 12000
	lines := []string{"Context checkpoint summary."}
	if reason = strings.TrimSpace(reason); reason != "" {
		lines = append(lines, "Recovery reason: "+reason+".")
	}
	if len(history) == 0 {
		return strings.Join(lines, "\n")
	}
	remaining := maxSummaryChars
	for _, msg := range history {
		if remaining <= 0 {
			break
		}
		role := strings.TrimSpace(string(msg.Role))
		if role == "" {
			role = "message"
		}
		content := strings.TrimSpace(safeContent(msg.Content))
		if content == "" && len(msg.ToolCalls) > 0 {
			content = summarizeToolCallsForCheckpoint(msg.ToolCalls)
		}
		if content == "" {
			continue
		}
		content = strings.Join(strings.Fields(content), " ")
		if len(content) > 700 {
			content = content[:700] + "..."
		}
		line := role + ": " + content
		if len(line) > remaining {
			line = line[:remaining]
		}
		lines = append(lines, line)
		remaining -= len(line)
	}
	lines = append(lines, "Older raw transcript remains available through retrieval tools and stored thread messages.")
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func summarizeToolCalls(calls []types.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	names := make([]string, 0, minInt(len(calls), 8))
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			name = "tool"
		}
		names = append(names, name)
		if len(names) >= 8 {
			break
		}
	}
	if len(names) == 0 {
		return ""
	}
	suffix := ""
	if len(calls) > len(names) {
		suffix = " and " + strconv.Itoa(len(calls)-len(names)) + " more"
	}
	return "Assistant requested tool calls: " + strings.Join(names, ", ") + suffix + "."
}

func (m *Manager) buildEmergencyContextHistory(history []types.ChatMessage, summary string, systemPrompt string, toolDefs []types.ToolDefinition, target int, charsPerToken int) []types.ChatMessage {
	target = maxInt(1, target)
	latestUserIdx := latestUserMessageIndex(history)
	if latestUserIdx < 0 {
		return m.trimHistoryToTokenBudget(history, systemPrompt, toolDefs, target, charsPerToken)
	}
	start := maxInt(0, latestUserIdx-4)
	if len(history)-start > 8 {
		start = maxInt(0, len(history)-8)
	}
	recent := append([]types.ChatMessage(nil), history[start:]...)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		summary = "Context checkpoint summary: older messages were compacted. Use retrieval tools for omitted raw details."
	}
	replacement := make([]types.ChatMessage, 0, len(recent)+1)
	replacement = append(replacement, types.ChatMessage{Role: types.RoleAssistant, Content: &summary})
	replacement = append(replacement, recent...)
	replacement = m.checkpointSafeHistory(replacement)
	if estimateRequestTokensWithTools(systemPrompt, replacement, toolDefs, charsPerToken) <= target {
		return replacement
	}
	replacement = m.trimHistoryToTokenBudget(replacement, systemPrompt, toolDefs, target, charsPerToken)
	replacement = m.shrinkLatestUserForTokenBudget(replacement, systemPrompt, toolDefs, target, charsPerToken)
	return m.checkpointSafeHistory(replacement)
}

func (m *Manager) shrinkLatestUserForTokenBudget(history []types.ChatMessage, systemPrompt string, toolDefs []types.ToolDefinition, target int, charsPerToken int) []types.ChatMessage {
	out := m.checkpointSafeHistory(history)
	if len(out) == 0 || target <= 0 {
		return out
	}
	if estimateRequestTokensWithTools(systemPrompt, out, toolDefs, charsPerToken) <= target {
		return out
	}
	idx := latestUserMessageIndex(out)
	if idx < 0 || out[idx].Content == nil {
		return out
	}
	original := strings.TrimSpace(*out[idx].Content)
	if original == "" {
		return out
	}
	overheadHistory := append([]types.ChatMessage(nil), out...)
	empty := ""
	overheadHistory[idx].Content = &empty
	overhead := estimateRequestTokensWithTools(systemPrompt, overheadHistory, toolDefs, charsPerToken)
	allowedTokens := maxInt(16, target-overhead)
	allowedChars := maxInt(256, allowedTokens*maxInt(1, charsPerToken))
	if allowedChars >= len(original) {
		return out
	}
	preview := checkpointPreview(original, allowedChars)
	out[idx].Content = &preview
	if estimateRequestTokensWithTools(systemPrompt, out, toolDefs, charsPerToken) <= target {
		return out
	}
	return []types.ChatMessage{out[idx]}
}

func checkpointPreview(content string, maxChars int) string {
	content = strings.TrimSpace(content)
	if maxChars <= 0 || len(content) <= maxChars {
		return content
	}
	header := "The latest user message was larger than the active context budget. A bounded preview is included; retrieve the full stored message if exact omitted details are needed.\n\n"
	if maxChars <= len(header)+128 {
		return header + content[:maxInt(0, maxChars-len(header))]
	}
	remaining := maxChars - len(header) - len("\n\n[...omitted middle...]\n\n")
	head := maxInt(64, remaining/2)
	tail := maxInt(64, remaining-head)
	if head+tail > len(content) {
		return header + content[:maxInt(0, maxChars-len(header))]
	}
	return header + content[:head] + "\n\n[...omitted middle...]\n\n" + content[len(content)-tail:]
}

func summarizeToolCallsForCheckpoint(calls []types.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			name = "tool_call"
		}
		parts = append(parts, name)
	}
	return "tool calls: " + strings.Join(parts, ", ")
}

func latestUserMessageIndex(history []types.ChatMessage) int {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == types.RoleUser {
			return i
		}
	}
	return -1
}

func maxMessageSeq(msgs []store.Message) int64 {
	var out int64
	for _, msg := range msgs {
		if msg.Seq > out {
			out = msg.Seq
		}
	}
	return out
}

func latestMessageID(msgs []store.Message) string {
	var (
		seq int64
		id  string
	)
	for _, msg := range msgs {
		if msg.Seq >= seq {
			seq = msg.Seq
			id = strings.TrimSpace(msg.ID)
		}
	}
	return id
}

func isRecoverableContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, engine.ErrPromptContextTooLarge) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	needles := []string{
		"prompt context exceeds",
		"context length exceeded",
		"context_length_exceeded",
		"context window",
		"maximum context length",
		"input is too long",
		"too many tokens",
	}
	for _, needle := range needles {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func (m *Manager) updateRunStatusWithFallback(ctx context.Context, runID string, status string, fields map[string]any) error {
	fields = m.withRunUsageRollup(ctx, runID, status, fields)
	baseErr := m.store.UpdateRunStatus(ctx, runID, status, fields)
	if baseErr == nil {
		return nil
	}

	if !isUnknownColumnError(baseErr) {
		return baseErr
	}

	retry := func(next map[string]any) error {
		return m.store.UpdateRunStatus(ctx, runID, status, next)
	}

	filtered := make(map[string]any, len(fields))
	removedContextField := false
	for k, v := range fields {
		if strings.HasPrefix(k, "context_") {
			removedContextField = true
			continue
		}
		filtered[k] = v
	}
	if !removedContextField {
		// If no context-like fields were involved, keep the original DB error.
		return baseErr
	}

	if retry(filtered) == nil {
		log.Printf("run %s: retried status update without context_* fields after DB column error", runID)
		return nil
	}

	return baseErr
}

func (m *Manager) withRunUsageRollup(ctx context.Context, runID string, status string, fields map[string]any) map[string]any {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
	default:
		return fields
	}
	if m.usage == nil {
		return fields
	}
	totals, err := m.usage.SumByRun(ctx, runID)
	if err != nil {
		log.Printf("run %s: failed to aggregate invocation usage: %v", runID, err)
		return fields
	}
	if totals.InvocationCount == 0 {
		return fields
	}
	if fields == nil {
		fields = map[string]any{}
	}
	setCompleteCounter := func(key string, value int64, reported int64) {
		if reported == totals.InvocationCount {
			fields[key] = value
			return
		}
		// A legacy run column has no way to express partial coverage. Remove a
		// last-response fallback rather than persisting an incomplete sum as exact.
		delete(fields, key)
	}
	setCompleteCounter("input_tokens", totals.InputTokens, totals.CallsWithInputTokens)
	setCompleteCounter("output_tokens", totals.OutputTokens, totals.CallsWithOutputTokens)
	setCompleteCounter("total_tokens", totals.TotalTokens, totals.CallsWithTotalTokens)
	return fields
}

func isUnknownColumnError(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1054
	}
	return strings.Contains(strings.ToLower(err.Error()), "unknown column")
}

func (m *Manager) failRun(ctx context.Context, run store.Run, code string, msg string) error {
	ended := time.Now().UTC()
	fields := map[string]any{
		"ended_at":      ended,
		"error_code":    strings.TrimSpace(code),
		"error_message": strings.TrimSpace(msg),
	}
	return m.updateRunStatusWithFallback(ctx, run.ID, "failed", fields)
}

// deferSuspendedQueryWaitError preserves the durable suspension boundary. Once
// the checkpoint and waiting_external state are committed, a transient local
// polling/claim error must yield to the recovery worker instead of converting
// the run into a terminal failure. Explicit user cancellation still flows
// through the normal cancelled transition.
func (m *Manager) deferSuspendedQueryWaitError(ctx context.Context, runID string, waitErr error) bool {
	if m == nil || m.longHorizon == nil || waitErr == nil || errors.Is(waitErr, context.Canceled) ||
		(ctx != nil && errors.Is(ctx.Err(), context.Canceled)) {
		return false
	}
	log.Printf("run %s: durable query wait deferred for recovery retry: %v", strings.TrimSpace(runID), waitErr)
	m.finish(runID)
	return true
}

func (m *Manager) finish(runID string) {
	m.mu.Lock()
	st := m.runs[runID]
	m.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.done {
		st.mu.Unlock()
		return
	}
	st.done = true
	for id, ch := range st.subs {
		close(ch)
		delete(st.subs, id)
	}
	close(st.doneCh)
	st.mu.Unlock()

	// Keep state around briefly to allow late subscribers to get a clean close.
	go func(finished *runState) {
		time.Sleep(5 * time.Minute)
		m.removeRunStateIfCurrent(runID, finished)
	}(st)
}

// removeRunStateIfCurrent prevents delayed cleanup for an earlier execution
// from deleting a live state installed by same-process continuation recovery.
func (m *Manager) removeRunStateIfCurrent(runID string, expected *runState) {
	if m == nil || expected == nil {
		return
	}
	m.mu.Lock()
	if m.runs[runID] == expected {
		delete(m.runs, runID)
	}
	m.mu.Unlock()
}

func (m *Manager) broadcast(runID string, ev SSEEvent) {
	m.mu.Lock()
	st := m.runs[runID]
	m.mu.Unlock()
	if st == nil {
		return
	}
	st.mu.Lock()
	// All sends to subscriber channels are serialized by st.mu.
	// Subscribe replay also enqueues under st.mu so backlog cannot be interleaved by live events.
	// Append to in-memory buffer for late subscribers.
	st.nextSeq++
	ev.Seq = st.nextSeq
	if st.durableEvents && strings.TrimSpace(ev.Event) != "" && m.store != nil {
		data := ev.Data
		if len(strings.TrimSpace(string(data))) == 0 {
			data = []byte("{}")
		}
		persisted, err := m.store.AppendRunEvent(context.Background(), store.RunEvent{
			RunID:    strings.TrimSpace(runID),
			ThreadID: st.threadID,
			// Allocate after the durable maximum so recovered/cross-process
			// executions do not collide with an earlier owner's local sequence.
			Seq:  0,
			Type: strings.TrimSpace(ev.Event),
			Data: json.RawMessage(append([]byte(nil), data...)),
		})
		if err != nil {
			log.Printf("run %s: failed to persist SSE event type=%s seq=%d: %v", runID, ev.Event, ev.Seq, err)
			// An event that never reached the durable log must not advance a
			// client's Last-Event-ID. The store may allocate this same sequence
			// to the next successful append; advertising it here would make a
			// reconnect skip that durable event.
			ev.Seq = 0
		} else if persisted.Seq > 0 {
			ev.Seq = persisted.Seq
			if persisted.Seq > st.nextSeq {
				st.nextSeq = persisted.Seq
			}
		}
	}
	st.buffer = append(st.buffer, ev)
	if m.bufferSize > 0 && len(st.buffer) > m.bufferSize {
		st.buffer = st.buffer[len(st.buffer)-m.bufferSize:]
	}
	for id, ch := range st.subs {
		select {
		case ch <- ev:
		default:
			close(ch)
			delete(st.subs, id)
		}
	}
	st.mu.Unlock()
}

func (m *Manager) coalesceDeltas(runID string, event string, emit func(event string, payload any)) (func(delta string), func()) {
	var mu sync.Mutex
	var buf strings.Builder
	last := time.Now()
	timer := (*time.Timer)(nil)

	flush := func() {
		mu.Lock()
		s := buf.String()
		buf.Reset()
		mu.Unlock()
		if s == "" {
			return
		}
		emit(event, map[string]any{"content": s})
	}

	push := func(delta string) {
		if delta == "" {
			return
		}
		mu.Lock()
		buf.WriteString(delta)
		shouldFlush := time.Since(last) >= m.deltaInterval
		if shouldFlush {
			last = time.Now()
		}
		if timer == nil {
			timer = time.AfterFunc(m.deltaInterval, flush)
		} else {
			_ = timer.Reset(m.deltaInterval)
		}
		mu.Unlock()
		if shouldFlush {
			flush()
		}
	}
	return push, flush
}

func emitThinkingEnd(flushThinkingDeltas func(), emit func(event string, payload any), summary *string) {
	if flushThinkingDeltas != nil {
		flushThinkingDeltas()
	}
	if summary != nil && strings.TrimSpace(*summary) != "" {
		emit("thinking_end", map[string]any{"summary": *summary})
		return
	}
	emit("thinking_end", map[string]any{})
}

func emitCompactionEnd(flushCompactionDeltas func(), emit func(event string, payload any)) {
	if flushCompactionDeltas != nil {
		flushCompactionDeltas()
	}
	emit("compaction_end", map[string]any{})
}

func redactStaleQueryObligationIDs(text string) string {
	return queryObligationIDTextRE.ReplaceAllString(text, "[stale query obligation]")
}

func redactStaleQueryRetryText(text string) string {
	text = redactStaleQueryObligationIDs(text)
	text = queryRetryOwnershipTextRE.ReplaceAllString(text, "[stale query retry target]")
	return queryRetryTargetTextRE.ReplaceAllString(text, "[stale query retry target]")
}

func appendSanitizedThreadSummary(systemPrompt string, summary *string) string {
	if summary == nil || strings.TrimSpace(*summary) == "" {
		return strings.TrimSpace(systemPrompt)
	}
	return strings.TrimSpace(systemPrompt) + "\n\n---\nThread summary:\n" + redactStaleQueryRetryText(strings.TrimSpace(*summary))
}

func redactStaleQueryObligationStrings(value any) any {
	return redactStaleQueryStrings(value, true)
}

func redactStaleQueryObligationTokens(value any) any {
	return redactStaleQueryStrings(value, false)
}

func redactStaleQueryStrings(value any, includeRetryTargets bool) any {
	redact := redactStaleQueryObligationIDs
	if includeRetryTargets {
		redact = redactStaleQueryRetryText
	}
	switch typed := value.(type) {
	case string:
		return redact(typed)
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			cleanKey := redact(key)
			if cleanKey != key {
				digest := sha256.Sum256([]byte(key))
				cleanKey = fmt.Sprintf("%s [redacted-key-%x]", cleanKey, digest[:4])
			}
			clean[cleanKey] = redactStaleQueryStrings(nested, includeRetryTargets)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, nested := range typed {
			clean[i] = redactStaleQueryStrings(nested, includeRetryTargets)
		}
		return clean
	case []map[string]any:
		clean := make([]map[string]any, len(typed))
		for i, nested := range typed {
			if item, ok := redactStaleQueryStrings(nested, includeRetryTargets).(map[string]any); ok {
				clean[i] = item
			}
		}
		return clean
	default:
		return value
	}
}

func sanitizeStaleQueryRetryJSON(raw []byte, stripLifecycleControlFields bool) []byte {
	if len(raw) == 0 {
		return nil
	}
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		if stripLifecycleControlFields {
			return []byte(redactStaleQueryRetryText(string(raw)))
		}
		return []byte(redactStaleQueryObligationIDs(string(raw)))
	}
	if stripLifecycleControlFields {
		decoded = stripStaleQueryLifecycleControlFields(decoded, false)
		decoded = redactStaleQueryObligationStrings(decoded)
	} else {
		decoded = redactStaleQueryObligationTokens(decoded)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		if stripLifecycleControlFields {
			return []byte(redactStaleQueryRetryText(string(raw)))
		}
		return []byte(redactStaleQueryObligationIDs(string(raw)))
	}
	return encoded
}

func stripStaleQueryLifecycleControlFields(value any, preserveDatasetColumns bool) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized := normalizedQueryRetryIdentityKey(key)
			if !preserveDatasetColumns && (shouldStripQueryRetryIdentityKey(key, false) || normalized == "runid" || normalized == "queryobligationmanaged") {
				continue
			}
			nextPreserve := preserveDatasetColumns
			switch normalized {
			case "rows", "data", "results", "records", "schema", "columns":
				nextPreserve = true
			}
			clean[key] = stripStaleQueryLifecycleControlFields(nested, nextPreserve)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, nested := range typed {
			clean[i] = stripStaleQueryLifecycleControlFields(nested, preserveDatasetColumns)
		}
		return clean
	case []map[string]any:
		clean := make([]map[string]any, len(typed))
		for i, nested := range typed {
			if item, ok := stripStaleQueryLifecycleControlFields(nested, preserveDatasetColumns).(map[string]any); ok {
				clean[i] = item
			}
		}
		return clean
	default:
		return value
	}
}

func staleQueryLifecycleEnvelope(raw, toolName string) bool {
	if isAsyncQueryTool(toolName) {
		return true
	}
	var envelope map[string]any
	if json.Unmarshal([]byte(raw), &envelope) != nil {
		return false
	}
	return isAsyncQueryTool(firstStringMapValue(envelope, "tool_name"))
}

func sanitizeStaleQueryRetryContent(content *string, structured bool, toolName string) *string {
	if content == nil {
		return nil
	}
	raw := *content
	if structured {
		raw = string(sanitizeStaleQueryRetryJSON([]byte(raw), staleQueryLifecycleEnvelope(raw, toolName)))
	} else {
		raw = redactStaleQueryRetryText(raw)
	}
	return &raw
}

func sanitizeStaleQueryRetryToolCall(call types.ToolCall) types.ToolCall {
	stripControlFields := tools.IsAsyncQueryRunTool(call.Name)
	if call.Args != nil {
		var sanitized any = cloneStringAnyMap(call.Args)
		if stripControlFields {
			sanitized = stripQueryRetryIdentityFields(sanitized, true)
			sanitized = redactStaleQueryObligationStrings(sanitized)
		} else {
			sanitized = redactStaleQueryObligationTokens(sanitized)
		}
		if clean, ok := sanitized.(map[string]any); ok {
			call.Args = clean
		}
	}
	if strings.TrimSpace(call.ArgsText) != "" {
		var decoded any
		if json.Unmarshal([]byte(call.ArgsText), &decoded) == nil {
			if stripControlFields {
				decoded = stripQueryRetryIdentityFields(decoded, true)
				decoded = redactStaleQueryObligationStrings(decoded)
			} else {
				decoded = redactStaleQueryObligationTokens(decoded)
			}
			if encoded, err := json.Marshal(decoded); err == nil {
				call.ArgsText = string(encoded)
			}
		} else {
			call.ArgsText = redactStaleQueryRetryText(call.ArgsText)
		}
	} else if call.Args != nil {
		if encoded, err := json.Marshal(call.Args); err == nil {
			call.ArgsText = string(encoded)
		}
	}
	if !stripControlFields && isAsyncQueryTool(call.Name) {
		if _, _, valid := executedQueryLifecycleIdentity(call); !valid {
			clean := cloneStringAnyMap(call.Args)
			if clean == nil {
				clean = map[string]any{}
			}
			for _, key := range []string{"job_id", "jobId", "query_job_id", "queryJobID", "owner_token", "ownerToken"} {
				delete(clean, key)
			}
			call.Args = clean
			if encoded, err := json.Marshal(clean); err == nil {
				call.ArgsText = string(encoded)
			}
		}
	}
	return call
}

func sanitizeStaleQueryRetryChatMessage(msg types.ChatMessage) types.ChatMessage {
	msg.Content = sanitizeStaleQueryRetryContent(msg.Content, msg.Role == types.RoleTool, msg.Name)
	if len(msg.ToolCalls) > 0 {
		calls := make([]types.ToolCall, len(msg.ToolCalls))
		for i, call := range msg.ToolCalls {
			calls[i] = sanitizeStaleQueryRetryToolCall(call)
		}
		msg.ToolCalls = calls
	}
	// Provider payloads are opaque and may contain signed thinking/tool blocks.
	// Rewriting even one byte while retaining the provider signature can make
	// replay fail. Portable Content/ToolCalls above are sufficient for history;
	// drop a stale checkpoint's provider-native representation altogether.
	msg.ProviderPayload = nil
	return msg
}

func sanitizeStoredQueryRetryHistoryForRun(messages []store.Message, currentRunID string) []store.Message {
	currentRunID = strings.TrimSpace(currentRunID)
	out := make([]store.Message, len(messages))
	for i, message := range messages {
		out[i] = message
		messageRunID := ""
		if message.RunID != nil {
			messageRunID = strings.TrimSpace(*message.RunID)
		}
		if currentRunID != "" && messageRunID == currentRunID {
			continue
		}
		toolName := ""
		if message.Name != nil {
			toolName = strings.TrimSpace(*message.Name)
		}
		out[i].Content = sanitizeStaleQueryRetryContent(message.Content, message.Role == types.RoleTool, toolName)
		if len(message.ToolCalls) > 0 {
			calls := make([]types.ToolCall, len(message.ToolCalls))
			for callIndex, call := range message.ToolCalls {
				calls[callIndex] = sanitizeStaleQueryRetryToolCall(call)
			}
			out[i].ToolCalls = calls
		}
		// Never rewrite opaque provider-native payloads from another run. They
		// can carry signatures that no longer validate after redaction.
		out[i].ProviderPayload = nil
	}
	return out
}

func sanitizeQueryRetryCheckpointForRun(checkpoint *managerContextCheckpoint, currentRunID string) *managerContextCheckpoint {
	if checkpoint == nil {
		return nil
	}
	checkpointRunID := ""
	if checkpoint.RunID != nil {
		checkpointRunID = strings.TrimSpace(*checkpoint.RunID)
	}
	if strings.TrimSpace(currentRunID) != "" && checkpointRunID == strings.TrimSpace(currentRunID) {
		return checkpoint
	}
	clean := *checkpoint
	clean.ReplacementHistory = make([]types.ChatMessage, len(checkpoint.ReplacementHistory))
	for i, message := range checkpoint.ReplacementHistory {
		clean.ReplacementHistory[i] = sanitizeStaleQueryRetryChatMessage(message)
	}
	clean.Summary = redactStaleQueryRetryText(checkpoint.Summary)
	clean.Plan = checkpoint.Plan
	clean.Plan.SummarizedBuckets = append([]ContextSummaryBucket(nil), checkpoint.Plan.SummarizedBuckets...)
	for i := range clean.Plan.SummarizedBuckets {
		clean.Plan.SummarizedBuckets[i].Summary = redactStaleQueryRetryText(clean.Plan.SummarizedBuckets[i].Summary)
	}
	return &clean
}

func (m *Manager) buildLegacyHistory(ctx context.Context, threadID string, msgs []store.Message, userMsgID string, hiddenContext string) []types.ChatMessage {
	out := make([]types.ChatMessage, 0, len(msgs))
	hidden := strings.TrimSpace(hiddenContext)
	targetUser := strings.TrimSpace(userMsgID)
	for _, mm := range msgs {
		content := mm.Content
		if strings.TrimSpace(mm.ID) == targetUser {
			if content != nil {
				merged := strings.TrimSpace(*content)
				if hidden != "" {
					merged = strings.TrimSpace(strings.TrimSpace(*content) + "\n\n" + hidden)
				}
				content = &merged
			} else if hidden != "" {
				content = &hidden
			}
		}
		if mm.Role == types.RoleTool {
			content = compactToolResultForModel(content)
		}

		msg := types.ChatMessage{
			Role:            mm.Role,
			Content:         content,
			ProviderPayload: cloneRawJSON(mm.ProviderPayload),
			FileAttachments: fileAttachmentRefsForMessage(mm.Role, mm.ReferencedArtifactIDs),
		}
		if len(mm.ToolCalls) > 0 {
			msg.ToolCalls = mm.ToolCalls
		}
		if mm.Role == types.RoleTool {
			if mm.ToolCallID != nil {
				msg.ToolCallID = strings.TrimSpace(*mm.ToolCallID)
			}
			if mm.Name != nil {
				msg.Name = strings.TrimSpace(*mm.Name)
			}
		}
		out = append(out, msg)
	}
	return m.hydrateHistoryFileAttachments(ctx, threadID, out)
}

func attachImageAttachmentsToLatestUser(history []types.ChatMessage, imageAttachments []types.ImageAttachment) []types.ChatMessage {
	normalized := types.CloneImageAttachments(imageAttachments)
	if len(normalized) == 0 || len(history) == 0 {
		return history
	}

	out := append([]types.ChatMessage(nil), history...)
	for idx := len(out) - 1; idx >= 0; idx-- {
		if out[idx].Role != types.RoleUser {
			continue
		}
		out[idx].ImageAttachments = types.CloneImageAttachments(normalized)
		return out
	}
	return out
}

func attachFileAttachmentsToLatestUser(history []types.ChatMessage, fileAttachments []types.FileAttachment) []types.ChatMessage {
	normalized := types.CloneFileAttachments(fileAttachments)
	if len(normalized) == 0 || len(history) == 0 {
		return history
	}

	out := append([]types.ChatMessage(nil), history...)
	for idx := len(out) - 1; idx >= 0; idx-- {
		if out[idx].Role != types.RoleUser {
			continue
		}
		out[idx].FileAttachments = types.CloneFileAttachments(normalized)
		return out
	}
	return out
}

func (m *Manager) persistImageAttachmentsAsArtifacts(ctx context.Context, threadID string, runID string, imageAttachments []types.ImageAttachment) []string {
	normalized := types.CloneImageAttachments(imageAttachments)
	if len(normalized) == 0 {
		return nil
	}
	trimmedRunID := strings.TrimSpace(runID)

	referencedArtifactIDs := make([]string, 0, len(normalized))
	for _, attachment := range normalized {
		dataURI := strings.TrimSpace(attachment.DataURI)
		if dataURI == "" {
			continue
		}

		artifactID := strings.TrimSpace(attachment.ArtifactID)
		if artifactID == "" {
			artifactID = types.NewUUID()
		}

		title := strings.TrimSpace(attachment.FileName)
		if title == "" {
			title = "Image attachment"
		}

		payload := []byte(dataURI)
		storage := "mysql"
		contentText := dataURI
		var contentTextPtr *string = &contentText
		var contentPathPtr *string
		if len(payload) > artifactInlineMaxBytes {
			storage = "file"
			rel, err := m.writeArtifactFile(artifactID, "image", payload)
			if err == nil {
				contentPathPtr = &rel
				contentTextPtr = nil
			} else {
				log.Printf("run %s: failed to write image artifact %s to file storage: %v", runID, artifactID, err)
				storage = "mysql"
				truncated := types.SafeTruncate(dataURI)
				contentTextPtr = &truncated
				contentPathPtr = nil
			}
		}

		toolName := "image_attachment"
		okVal := true

		var metadata map[string]any
		addMeta := func(key string, value any) {
			if metadata == nil {
				metadata = map[string]any{}
			}
			metadata[key] = value
		}
		if trimmedRunID != "" {
			addMeta("run_id", trimmedRunID)
		}
		if mimeType := strings.TrimSpace(strings.ToLower(attachment.MimeType)); mimeType != "" {
			addMeta("mimeType", mimeType)
		}
		if attachment.SizeBytes != nil && *attachment.SizeBytes > 0 {
			addMeta("sizeBytes", *attachment.SizeBytes)
		}
		if attachment.Width != nil && *attachment.Width > 0 {
			addMeta("width", *attachment.Width)
		}
		if attachment.Height != nil && *attachment.Height > 0 {
			addMeta("height", *attachment.Height)
		}
		if source := strings.TrimSpace(strings.ToLower(attachment.Source)); source != "" {
			addMeta("source", source)
		}
		if fileName := strings.TrimSpace(attachment.FileName); fileName != "" {
			addMeta("fileName", fileName)
		}

		artifact := store.Artifact{
			ID:          artifactID,
			ThreadID:    threadID,
			Type:        "image",
			Title:       title,
			ToolName:    &toolName,
			OK:          &okVal,
			Storage:     storage,
			ContentText: contentTextPtr,
			ContentPath: contentPathPtr,
			ByteSize:    int64(len(payload)),
			Metadata:    metadata,
		}

		insertArtifact := func(a store.Artifact) error {
			if _, err := m.store.InsertArtifact(ctx, a); err != nil {
				return err
			}
			return nil
		}

		if err := insertArtifact(artifact); err != nil {
			if errors.Is(err, store.ErrConflict) {
				existing, getErr := m.store.GetArtifact(ctx, artifactID)
				if getErr == nil && strings.TrimSpace(existing.ThreadID) == threadID {
					referencedArtifactIDs = append(referencedArtifactIDs, artifactID)
					continue
				}

				fallbackID := types.NewUUID()
				artifact.ID = fallbackID
				if artifact.Storage == "file" {
					rel, werr := m.writeArtifactFile(fallbackID, "image", payload)
					if werr == nil {
						artifact.ContentPath = &rel
						artifact.ContentText = nil
					} else {
						log.Printf("run %s: failed to write fallback image artifact %s: %v", runID, fallbackID, werr)
						truncated := types.SafeTruncate(dataURI)
						artifact.Storage = "mysql"
						artifact.ContentText = &truncated
						artifact.ContentPath = nil
					}
				}
				if fallbackErr := insertArtifact(artifact); fallbackErr != nil {
					log.Printf("run %s: failed to persist fallback image artifact %s: %v", runID, fallbackID, fallbackErr)
					continue
				}
				referencedArtifactIDs = append(referencedArtifactIDs, fallbackID)
				continue
			}
			log.Printf("run %s: failed to persist image artifact %s: %v", runID, artifactID, err)
			continue
		}
		referencedArtifactIDs = append(referencedArtifactIDs, artifactID)
	}

	return dedupeStringSlice(referencedArtifactIDs)
}

func (m *Manager) hydrateReferencedImageAttachments(
	ctx context.Context,
	threadID string,
	artifactIDs []string,
	current []types.ImageAttachment,
) []types.ImageAttachment {
	base := types.CloneImageAttachments(current)
	if len(base) == 0 && len(artifactIDs) == 0 {
		return nil
	}

	totalBytes := 0
	seenDataURI := map[string]struct{}{}
	out := make([]types.ImageAttachment, 0, maxInt(len(base), 1))

	addAttachment := func(attachment types.ImageAttachment) bool {
		dataURI := strings.TrimSpace(attachment.DataURI)
		if dataURI == "" || !strings.HasPrefix(strings.ToLower(dataURI), "data:image/") {
			return false
		}
		if len(out) >= maxModelImageAttachmentCount {
			return false
		}
		if len(dataURI) > maxModelImageDataURIBytes {
			return false
		}
		if _, exists := seenDataURI[dataURI]; exists {
			return true
		}
		if totalBytes+len(dataURI) > maxModelImageTotalBytes {
			return false
		}
		totalBytes += len(dataURI)
		seenDataURI[dataURI] = struct{}{}

		next := attachment
		next.DataURI = dataURI
		next.ArtifactID = strings.TrimSpace(next.ArtifactID)
		next.FileName = strings.TrimSpace(next.FileName)
		mimeType := supportedModelImageMimeType(next.MimeType)
		if mimeType == "" {
			mimeType = supportedModelImageMimeType(inferImageMimeTypeFromDataURI(dataURI))
		}
		if mimeType == "" {
			return false
		}
		next.MimeType = mimeType
		next.Source = strings.TrimSpace(strings.ToLower(next.Source))
		out = append(out, next)
		return true
	}

	for _, attachment := range base {
		if len(out) >= maxModelImageAttachmentCount {
			break
		}
		_ = addAttachment(attachment)
	}
	if len(out) >= maxModelImageAttachmentCount {
		return out
	}

	for _, artifactID := range dedupeStringSlice(artifactIDs) {
		if len(out) >= maxModelImageAttachmentCount {
			break
		}
		artifact, err := m.store.GetArtifact(ctx, artifactID)
		if err != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(artifact.ThreadID), strings.TrimSpace(threadID)) {
			continue
		}
		attachment, ok := m.imageAttachmentFromReferencedArtifact(artifact)
		if !ok {
			continue
		}
		_ = addAttachment(attachment)
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) imageAttachmentFromReferencedArtifact(artifact store.Artifact) (types.ImageAttachment, bool) {
	artifactType := strings.TrimSpace(strings.ToLower(artifact.Type))
	if artifactType != "image" && artifactType != "file" {
		return types.ImageAttachment{}, false
	}

	fileName := strings.TrimSpace(firstNonEmptyString(
		artifact.Title,
		stringMapValue(artifact.Metadata, "fileName", "file_name"),
	))
	if fileName == "" {
		fileName = "Image attachment"
	}
	mimeType := firstSupportedModelImageMimeType(
		stringMapValue(artifact.Metadata, "mimeType", "mime_type"),
		stringMapValue(artifact.Metadata, "detectedMimeType", "detected_mime_type"),
		mime.TypeByExtension(strings.ToLower(filepath.Ext(fileName))),
	)
	source := strings.TrimSpace(strings.ToLower(stringMapValue(artifact.Metadata, "source")))

	var dataURI string
	var sizeBytes int64
	switch artifactType {
	case "image":
		dataURI = strings.TrimSpace(m.readArtifactContent(artifact))
		if dataURI == "" || !strings.HasPrefix(strings.ToLower(dataURI), "data:image/") {
			return types.ImageAttachment{}, false
		}
		if mimeType == "" {
			mimeType = supportedModelImageMimeType(inferImageMimeTypeFromDataURI(dataURI))
		}
		if size, ok := int64MapValue(artifact.Metadata, "sizeBytes", "size_bytes"); ok && size > 0 {
			sizeBytes = size
		}
	case "file":
		if artifact.ByteSize > 10*1024*1024 {
			return types.ImageAttachment{}, false
		}
		raw := m.readArtifactBytes(artifact)
		if len(raw) == 0 || len(raw) > 10*1024*1024 {
			return types.ImageAttachment{}, false
		}
		if mimeType == "" {
			mimeType = supportedModelImageMimeType(http.DetectContentType(raw))
		}
		if mimeType == "" {
			return types.ImageAttachment{}, false
		}
		dataURI = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(raw)
		sizeBytes = int64(len(raw))
	}
	if mimeType == "" {
		return types.ImageAttachment{}, false
	}

	attachment := types.ImageAttachment{
		ArtifactID: strings.TrimSpace(artifact.ID),
		FileName:   fileName,
		MimeType:   mimeType,
		DataURI:    dataURI,
		Source:     source,
	}
	if sizeBytes > 0 {
		v := sizeBytes
		attachment.SizeBytes = &v
	}
	if width, ok := intMapValue(artifact.Metadata, "width"); ok && width > 0 {
		v := width
		attachment.Width = &v
	}
	if height, ok := intMapValue(artifact.Metadata, "height"); ok && height > 0 {
		v := height
		attachment.Height = &v
	}
	return attachment, true
}

func imageAttachmentsForModelArtifact(artifactID, title, artifactType, content string, meta map[string]any) []types.ImageAttachment {
	if !strings.EqualFold(strings.TrimSpace(artifactType), "image") {
		return nil
	}
	dataURI := strings.TrimSpace(content)
	if dataURI == "" || !strings.HasPrefix(strings.ToLower(dataURI), "data:image/") {
		return nil
	}
	mimeType := firstSupportedModelImageMimeType(
		stringMapValue(meta, "mimeType", "mime_type"),
		inferImageMimeTypeFromDataURI(dataURI),
	)
	if mimeType == "" {
		return nil
	}

	fileName := strings.TrimSpace(title)
	if fileName == "" {
		fileName = "Image attachment"
	}
	attachment := types.ImageAttachment{
		ArtifactID: strings.TrimSpace(artifactID),
		FileName:   fileName,
		MimeType:   mimeType,
		DataURI:    dataURI,
		Source:     strings.TrimSpace(strings.ToLower(stringMapValue(meta, "source"))),
	}
	if sizeBytes, ok := int64MapValue(meta, "sizeBytes", "size_bytes"); ok && sizeBytes > 0 {
		v := sizeBytes
		attachment.SizeBytes = &v
	}
	if width, ok := intMapValue(meta, "width"); ok && width > 0 {
		v := width
		attachment.Width = &v
	}
	if height, ok := intMapValue(meta, "height"); ok && height > 0 {
		v := height
		attachment.Height = &v
	}
	return []types.ImageAttachment{attachment}
}

func supportedModelImageMimeType(raw string) string {
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

func firstSupportedModelImageMimeType(values ...string) string {
	for _, value := range values {
		if mimeType := supportedModelImageMimeType(value); mimeType != "" {
			return mimeType
		}
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func (m *Manager) readArtifactContent(artifact store.Artifact) string {
	raw := m.readArtifactBytes(artifact)
	if len(raw) == 0 {
		return ""
	}
	return string(raw)
}

func (m *Manager) readArtifactBytes(artifact store.Artifact) []byte {
	switch strings.TrimSpace(strings.ToLower(artifact.Storage)) {
	case "file":
		if artifact.ContentPath == nil {
			return nil
		}
		relPath := strings.TrimSpace(*artifact.ContentPath)
		if relPath == "" {
			return nil
		}
		clean := filepath.Clean(filepath.FromSlash(relPath))
		if clean == "" || clean == "." || clean == ".." || filepath.IsAbs(clean) {
			return nil
		}
		if strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return nil
		}
		fullPath := filepath.Join(m.cfgDir, clean)
		b, err := os.ReadFile(fullPath)
		if err != nil {
			return nil
		}
		return b
	default:
		if artifact.ContentText == nil {
			return nil
		}
		return []byte(*artifact.ContentText)
	}
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
	semi := strings.Index(meta, ";")
	if semi >= 0 {
		meta = strings.TrimSpace(meta[:semi])
	}
	if !strings.HasPrefix(meta, "image/") {
		return ""
	}
	return meta
}

func stringMapValue(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func int64MapValue(m map[string]any, keys ...string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case int:
			return int64(t), true
		case int64:
			return t, true
		case float64:
			return int64(t), true
		case json.Number:
			if n, err := t.Int64(); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func intMapValue(m map[string]any, keys ...string) (int, bool) {
	v, ok := int64MapValue(m, keys...)
	if !ok {
		return 0, false
	}
	return int(v), true
}

func (m *Manager) truncateHistory(msgs []types.ChatMessage) []types.ChatMessage {
	out := make([]types.ChatMessage, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Content != nil {
			t := types.SafeTruncate(*msg.Content)
			msg.Content = &t
		}
		out = append(out, msg)
	}
	return out
}

func (m *Manager) compactAnthropicToolHistory(
	msgs []types.ChatMessage,
	systemPrompt string,
	toolDefs []types.ToolDefinition,
	tokenBudget int,
	keepToolMessages int,
	charsPerToken int,
) ([]types.ChatMessage, int) {
	if len(msgs) == 0 || tokenBudget <= 0 {
		return msgs, 0
	}
	keepToolMessages = maxInt(1, keepToolMessages)
	if estimateRequestTokensWithTools(systemPrompt, msgs, toolDefs, charsPerToken) <= maxInt(1, tokenBudget) {
		return msgs, 0
	}

	toolCount := 0
	cutoff := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != types.RoleTool {
			continue
		}
		toolCount++
		if toolCount > keepToolMessages {
			cutoff = i + 1
		}
	}

	out := append([]types.ChatMessage(nil), msgs...)
	if cutoff < len(msgs) {
		summaryText := summarizeAnthropicToolHistory(msgs[:cutoff], defaultAnthropicCompactionSummaryLimit)
		compacted := make([]types.ChatMessage, 0, len(msgs)-cutoff+1)
		compacted = append(compacted, types.ChatMessage{
			Role:    types.RoleAssistant,
			Content: &summaryText,
		})
		compacted = append(compacted, msgs[cutoff:]...)
		out = compacted
	}

	if estimateRequestTokensWithTools(systemPrompt, out, toolDefs, charsPerToken) > maxInt(1, tokenBudget) {
		out = m.trimHistoryToTokenBudget(out, systemPrompt, toolDefs, tokenBudget, charsPerToken)
	}
	out = m.truncateHistory(out)
	return out, maxInt(0, len(msgs)-len(out))
}

func (m *Manager) inferAnthropicToolProfile(
	req StartRunRequest,
	includeContextTools bool,
	enabledDatasources map[string]bool,
) anthropicToolProfile {
	haystack := strings.ToLower(strings.TrimSpace(req.Content + "\n" + req.HiddenContext))
	profile := anthropicToolProfile{
		includeSearch:     includeContextTools,
		datasourceEnabled: cloneDatasourceEnabledMap(enabledDatasources),
	}
	if containsAnyToken(haystack,
		"csv",
		"spreadsheet",
		"column",
		"row",
		"aggregate",
		"filter",
		"group by",
		"groupby",
		"pivot",
	) {
		profile.includeCSV = true
	}
	if containsAnyToken(haystack,
		"search",
		"lookup",
		"look up",
		"find",
		"documentation",
		"docs",
		"evidence",
		"reference",
	) {
		profile.includeSearch = true
	}
	return profile
}

func (m *Manager) selectAnthropicToolDefs(
	searchMode bool,
	searchDepth string,
	includeContextTools bool,
	enabledDatasources map[string]bool,
	enabledThreadSearch bool,
	profile anthropicToolProfile,
) []types.ToolDefinition {
	if searchMode {
		if !enabledThreadSearch {
			return nil
		}
		depth := strings.TrimSpace(strings.ToLower(searchDepth))
		if depth == "off" {
			return nil
		}
		return m.tools.DefinitionsForSearch(depth)
	}

	profile.includeSearch = enabledThreadSearch && (profile.includeSearch || includeContextTools)
	defs := m.tools.AnalysisCoreDefinitions()
	defs = m.mergeToolDefinitions(defs, m.tools.ThreadSearchToolDefinitions(enabledThreadSearch))
	if profile.includeSearch {
		depth := normalizeSearchDepth(searchDepth)
		defs = m.mergeToolDefinitions(defs, m.tools.DefinitionsForSearch(depth))
	}
	if profile.includeCSV {
		defs = m.mergeToolDefinitions(defs, m.tools.CSVToolDefinitions())
	}
	for _, capability := range tools.DatasourceCapabilities() {
		name := strings.TrimSpace(strings.ToLower(capability.Name))
		if name == "" {
			continue
		}
		if !enabledDatasources[name] || !profile.datasourceEnabled[name] {
			continue
		}
		defs = m.mergeToolDefinitions(defs, m.tools.DatasourceToolDefinitions(name))
	}
	return defs
}

func cloneDatasourceEnabledMap(src map[string]bool) map[string]bool {
	out := map[string]bool{}
	for key, value := range src {
		k := strings.TrimSpace(strings.ToLower(key))
		if k == "" {
			continue
		}
		out[k] = value
	}
	return out
}

func compactAnthropicToolDefinitions(defs []types.ToolDefinition, enabled bool) []types.ToolDefinition {
	if !enabled || len(defs) == 0 {
		return defs
	}
	out := make([]types.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		copied := def
		fn := copied.Function
		fn.Description = compactToolDescription(fn.Description)
		if compacted := compactToolSchema(fn.Parameters); compacted != nil {
			if compactedParams, ok := compacted.(map[string]any); ok {
				fn.Parameters = compactedParams
			}
		}
		copied.Function = fn
		out = append(out, copied)
	}
	return out
}

func compactToolDescription(description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}
	if len(description) <= 120 {
		return description
	}
	return description[:120] + "..."
}

func compactToolSchema(v any) any {
	return compactToolSchemaNode(v, false)
}

func compactToolSchemaNode(v any, inProperties bool) any {
	switch typed := v.(type) {
	case map[string]any:
		if len(typed) == 0 {
			return typed
		}
		out := make(map[string]any, len(typed))
		for k, value := range typed {
			key := strings.ToLower(strings.TrimSpace(k))
			// Preserve a parameter literally named "description" under JSON Schema
			// "properties", while still dropping verbose schema metadata elsewhere.
			if !inProperties && (key == "description" || key == "title" || key == "examples") {
				continue
			}
			out[k] = compactToolSchemaNode(value, key == "properties")
		}
		return out
	case []any:
		clean := make([]any, 0, len(typed))
		for _, item := range typed {
			clean = append(clean, compactToolSchemaNode(item, false))
		}
		return clean
	default:
		return v
	}
}

func containsAnyToken(haystack string, tokens ...string) bool {
	text := strings.ToLower(strings.TrimSpace(haystack))
	if text == "" {
		return false
	}
	for _, token := range tokens {
		t := strings.ToLower(strings.TrimSpace(token))
		if t == "" {
			continue
		}
		if strings.Contains(text, t) {
			return true
		}
	}
	return false
}

func summarizeAnthropicToolHistory(msgs []types.ChatMessage, maxEntries int) string {
	maxEntries = maxInt(1, maxEntries)
	toolSummaries := make([]string, 0, maxEntries)

	for _, msg := range msgs {
		if msg.Role != types.RoleTool {
			continue
		}
		name := strings.TrimSpace(msg.Name)
		if name == "" {
			name = "tool"
		}
		callID := strings.TrimSpace(msg.ToolCallID)
		label := name
		if callID != "" {
			label = label + "#" + callID
		}
		preview := ""
		if msg.Content != nil {
			preview = strings.TrimSpace(*msg.Content)
			if len(preview) > 220 {
				preview = preview[:220] + "..."
			}
		}
		line := label
		if preview != "" {
			line = line + ": " + preview
		}
		toolSummaries = append(toolSummaries, line)
		if len(toolSummaries) >= maxEntries {
			break
		}
	}

	if len(toolSummaries) == 0 {
		return "Anthropic context was compacted to keep recent tool outputs only."
	}
	return "Anthropic context was compacted; older tool outputs were summarized. " + strings.Join(toolSummaries, "; ")
}

func isAnthropicProviderContext(providerID string, model string) bool {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "anthropic" || providerID == "bedrock" {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "claude-")
}

func compactionPlanningTokenCap(settings config.AppSettings, providerID string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "anthropic":
		if settings.Anthropic == nil {
			return 0, false
		}
		enabled := true
		if settings.Anthropic.CompactionEnabled != nil {
			enabled = *settings.Anthropic.CompactionEnabled
		}
		if !enabled {
			return 0, false
		}
		return normalizeCompactionGuardCap(settings.Anthropic.CompactionTrigger), true
	case "bedrock":
		if settings.Bedrock == nil {
			return 0, false
		}
		enabled := true
		if settings.Bedrock.CompactionEnabled != nil {
			enabled = *settings.Bedrock.CompactionEnabled
		}
		if !enabled {
			return 0, false
		}
		return normalizeCompactionGuardCap(settings.Bedrock.CompactionTrigger), true
	default:
		return 0, false
	}
}

func normalizeCompactionGuardCap(trigger *int) int {
	v := defaultCompactionGuardCap
	if trigger != nil {
		v = *trigger
	}
	if v < minCompactionGuardCap {
		v = minCompactionGuardCap
	}
	return v
}

func (m *Manager) mergeToolDefinitions(base []types.ToolDefinition, extra []types.ToolDefinition) []types.ToolDefinition {
	out := make([]types.ToolDefinition, 0, len(base)+len(extra))
	seen := map[string]struct{}{}
	add := func(in []types.ToolDefinition) {
		for _, d := range in {
			name := strings.TrimSpace(d.Function.Name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, d)
		}
	}
	add(base)
	add(extra)
	return out
}

func (m *Manager) trimHistoryToTokenBudget(msgs []types.ChatMessage, systemPrompt string, toolDefs []types.ToolDefinition, budget int, charsPerToken int) []types.ChatMessage {
	if budget <= 0 || len(msgs) == 0 {
		return nil
	}
	if budget == 1 {
		return append([]types.ChatMessage(nil), msgs[len(msgs)-1])
	}
	overhead := 0
	overhead += estimateStringTokenCount(strings.TrimSpace(systemPrompt), charsPerToken)
	overhead += estimateToolOverheadTokens(toolDefs, charsPerToken)
	if overhead >= budget {
		return append([]types.ChatMessage(nil), msgs[len(msgs)-1])
	}
	outRev := make([]types.ChatMessage, 0, len(msgs))
	used := overhead
	startIdx := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		msgTokens := estimateMessageTokenCost(msg, charsPerToken)
		if used+msgTokens <= budget {
			outRev = append(outRev, msg)
			used += msgTokens
			startIdx = i
			continue
		}
		if len(outRev) == 0 {
			outRev = append(outRev, msg)
			startIdx = i
		}
		break
	}
	startIdx = expandTrimStartForToolChain(msgs, startIdx)
	if startIdx < len(msgs) {
		return append([]types.ChatMessage(nil), msgs[startIdx:]...)
	}
	for i, j := 0, len(outRev)-1; i < j; i, j = i+1, j-1 {
		outRev[i], outRev[j] = outRev[j], outRev[i]
	}
	return outRev
}

func expandTrimStartForToolChain(msgs []types.ChatMessage, startIdx int) int {
	if startIdx <= 0 || startIdx >= len(msgs) {
		return maxInt(0, startIdx)
	}
	start := startIdx
	for start > 0 && msgs[start].Role == types.RoleTool && msgs[start-1].Role == types.RoleTool {
		start--
	}
	if start > 0 && msgs[start].Role == types.RoleTool && msgs[start-1].Role == types.RoleAssistant && len(msgs[start-1].ToolCalls) > 0 {
		start--
	}
	return maxInt(0, start)
}

func estimateChatTokens(msgs []types.ChatMessage, charsPerToken ...int) int {
	cpt := defaultTokenCharsPerToken
	if len(charsPerToken) > 0 && charsPerToken[0] > 0 {
		cpt = charsPerToken[0]
	}
	total := 0
	for _, msg := range msgs {
		if msg.Content == nil {
			continue
		}
		total += estimateStringTokenCount(*msg.Content, cpt)
	}
	return total
}

func estimateMessageTokensForStoreMessages(msgs []store.Message, charsPerToken ...int) int {
	cpt := defaultTokenCharsPerToken
	if len(charsPerToken) > 0 && charsPerToken[0] > 0 {
		cpt = charsPerToken[0]
	}
	total := 0
	for _, msg := range msgs {
		if msg.Content == nil {
			continue
		}
		total += estimateStringTokenCount(*msg.Content, cpt)
	}
	return total
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *Manager) sanitizeReferencedIDs(
	ctx context.Context,
	threadID string,
	artifactIDs []string,
	messageIDs []string,
) (
	cleanArtifacts []string,
	cleanMessages []string,
	droppedArtifacts []string,
	droppedMessages []string,
) {
	scopedThreadID := strings.TrimSpace(threadID)
	candidateArtifacts := dedupeStringSlice(artifactIDs)
	candidateMessages := dedupeStringSlice(messageIDs)
	if scopedThreadID == "" {
		return nil, nil, candidateArtifacts, candidateMessages
	}

	for _, artifactID := range candidateArtifacts {
		artifact, err := m.store.GetArtifact(ctx, artifactID)
		if err != nil {
			droppedArtifacts = append(droppedArtifacts, artifactID)
			continue
		}
		if strings.TrimSpace(artifact.ThreadID) != scopedThreadID {
			droppedArtifacts = append(droppedArtifacts, artifactID)
			continue
		}
		cleanArtifacts = append(cleanArtifacts, artifactID)
	}

	if len(candidateMessages) > 0 {
		msgs, err := m.store.ListMessages(ctx, store.ListMessagesParams{
			ThreadID:      scopedThreadID,
			Limit:         len(candidateMessages),
			MessageIDs:    candidateMessages,
			IncludeHidden: true,
		})
		if err != nil {
			droppedMessages = append(droppedMessages, candidateMessages...)
		} else {
			found := make(map[string]struct{}, len(msgs))
			for _, msg := range msgs {
				id := strings.TrimSpace(msg.ID)
				if id == "" {
					continue
				}
				found[id] = struct{}{}
			}
			for _, messageID := range candidateMessages {
				if _, ok := found[messageID]; ok {
					cleanMessages = append(cleanMessages, messageID)
					continue
				}
				droppedMessages = append(droppedMessages, messageID)
			}
		}
	}

	return cleanArtifacts, cleanMessages, droppedArtifacts, droppedMessages
}

func dedupeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func estimateStringTokenCount(s string, charsPerToken ...int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	cpt := defaultTokenCharsPerToken
	if len(charsPerToken) > 0 && charsPerToken[0] > 0 {
		cpt = charsPerToken[0]
	}
	return max(1, (len(s)+(cpt-1))/cpt)
}

func isRetrievalTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "search_threads", "search_docs", "get_doc", "get_artifact", "list_messages", "rlm_exec",
		"inspect_attachment", "read_attachment", "search_attachment":
		return true
	default:
		return false
	}
}

func filterLocalAttachmentToolDefinitions(defs []types.ToolDefinition) []types.ToolDefinition {
	if len(defs) == 0 {
		return nil
	}
	out := make([]types.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		name := strings.TrimSpace(def.Function.Name)
		switch name {
		case "get_artifact", "get_artifact_chunk", "search_artifact_content", "get_json_artifact",
			"inspect_attachment", "read_attachment", "search_attachment":
			continue
		default:
			out = append(out, def)
		}
	}
	return out
}

func filterProjectFileToolDefinitionsWithoutProject(defs []types.ToolDefinition) []types.ToolDefinition {
	if len(defs) == 0 {
		return nil
	}
	out := make([]types.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		switch strings.TrimSpace(def.Function.Name) {
		case "list_files", "read_file", "search_files":
			continue
		default:
			out = append(out, def)
		}
	}
	return out
}

func summarizeContextPlan(plan RunContextPlan) string {
	lines := make([]string, 0, 8)
	lines = append(lines, "Need retrieval: "+strconv.FormatBool(plan.NeedRetrieval || plan.NeedRetrievalTooling))
	lines = append(lines, "Hard capped: "+strconv.FormatBool(plan.HardCapped))
	if plan.TokensBefore > 0 || plan.TokensAfter > 0 {
		lines = append(lines, "Tokens before/after: "+strconv.Itoa(plan.TokensBefore)+"/"+strconv.Itoa(plan.TokensAfter))
	}
	if len(plan.SelectedMessageIDs) > 0 {
		lines = append(lines, "Selected messages: "+strconv.Itoa(len(plan.SelectedMessageIDs)))
	}
	if len(plan.SummarizedBuckets) > 0 {
		lines = append(lines, "Summary buckets: "+strconv.Itoa(len(plan.SummarizedBuckets)))
	}
	if plan.EvidenceBudget > 0 {
		lines = append(lines, "Evidence budget: "+strconv.Itoa(plan.EvidenceBudget))
	}
	if strings.TrimSpace(plan.CompactionMode) != "" {
		lines = append(lines, "Compaction mode: "+strings.TrimSpace(plan.CompactionMode))
	}
	if plan.HandoverSummaryUsed {
		lines = append(lines, "Handover summary chars: "+strconv.Itoa(plan.HandoverSummaryChars))
	}
	if strings.TrimSpace(plan.PlanReason) != "" {
		lines = append(lines, "Plan reason: "+strings.TrimSpace(plan.PlanReason))
	}
	if len(lines) == 0 {
		return "Context planning unavailable."
	}
	return strings.Join(lines, "; ")
}

func estimateToolOverheadTokens(toolDefs []types.ToolDefinition, charsPerToken ...int) int {
	if len(toolDefs) == 0 {
		return 0
	}
	cpt := defaultTokenCharsPerToken
	if len(charsPerToken) > 0 && charsPerToken[0] > 0 {
		cpt = charsPerToken[0]
	}
	const perToolBaseOverheadChars = 64
	totalChars := 0
	for _, def := range toolDefs {
		totalChars += perToolBaseOverheadChars
		if strings.TrimSpace(def.Type) != "" {
			totalChars += len(def.Type)
		}
		fn := strings.TrimSpace(def.Function.Name)
		if fn != "" {
			totalChars += len(fn)
		}
		desc := strings.TrimSpace(def.Function.Description)
		if desc != "" {
			totalChars += len(desc)
		}
		if params, err := json.Marshal(def.Function.Parameters); err == nil {
			totalChars += len(params)
		}
	}
	return max(1, (totalChars+(cpt-1))/cpt)
}

func estimateRequestTokensWithTools(systemPrompt string, history []types.ChatMessage, toolDefs []types.ToolDefinition, charsPerToken ...int) int {
	cpt := defaultTokenCharsPerToken
	if len(charsPerToken) > 0 && charsPerToken[0] > 0 {
		cpt = charsPerToken[0]
	}
	total := 0
	total += estimateStringTokenCount(systemPrompt, cpt)
	for _, msg := range history {
		total += estimateMessageTokenCost(msg, cpt)
	}
	total += estimateToolOverheadTokens(toolDefs, cpt)
	return max(1, total)
}

func estimateMessageTokenCost(msg types.ChatMessage, charsPerToken ...int) int {
	cpt := defaultTokenCharsPerToken
	if len(charsPerToken) > 0 && charsPerToken[0] > 0 {
		cpt = charsPerToken[0]
	}
	cost := 0
	if msg.Content != nil {
		cost += estimateStringTokenCount(*msg.Content, cpt)
	}
	for _, attachment := range msg.ImageAttachments {
		if strings.TrimSpace(attachment.DataURI) != "" {
			cost += nativeImageEstimateTokens
		}
	}
	for _, attachment := range msg.FileAttachments {
		if strings.TrimSpace(attachment.InlineDataBase64) != "" {
			cost += nativeFileEstimateTokens
		}
	}
	if strings.TrimSpace(msg.ToolCallID) != "" {
		cost += estimateStringTokenCount(msg.ToolCallID, cpt)
	}
	if strings.TrimSpace(msg.Name) != "" {
		cost += estimateStringTokenCount(msg.Name, cpt)
	}
	if len(msg.ToolCalls) > 0 {
		if encoded, err := json.Marshal(msg.ToolCalls); err == nil {
			cost += estimateStringTokenCount(string(encoded), cpt)
		}
	}
	return cost
}

func extractToolReferencedArtifactIDs(toolName string, args map[string]any) []string {
	out := make([]string, 0, 8)
	var add func(raw any)
	add = func(raw any) {
		switch v := raw.(type) {
		case string:
			if t := strings.TrimSpace(v); t != "" {
				out = append(out, t)
			}
		case []string:
			for _, item := range v {
				add(item)
			}
		case []any:
			for _, item := range v {
				add(item)
			}
		}
	}

	switch strings.TrimSpace(toolName) {
	case "get_artifact", "inspect_attachment", "read_attachment", "search_attachment":
		add(args["artifact_id"])
		add(args["artifact_ids"])
		add(args["artifactIds"])
	case "display_table", "build_chart_artifact":
		if source, ok := args["source"].(map[string]any); ok {
			add(source["artifact_id"])
			add(source["artifactId"])
		}
	case "get_doc":
		add(args["doc_id"])
		add(args["docId"])
	case "search_threads", "search_docs":
		add(args["artifact_ids"])
		add(args["artifactIds"])
		add(args["message_ids"])
		add(args["messageIds"])
	case "list_messages":
		add(args["artifact_ids"])
		add(args["artifactIds"])
		add(args["message_ids"])
		add(args["messageIds"])
		add(args["thread_ids"])
		add(args["threadIds"])
		add(args["thread_id"])
	}
	if len(out) == 0 {
		add(args["project_id"])
	}
	return dedupeStringSlice(out)
}

func extractToolResultReferencedArtifactIDs(toolName string, out tools.Output) []string {
	raw := make([]string, 0, 8)
	var add func(rawVal any)
	add = func(rawVal any) {
		switch v := rawVal.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				raw = append(raw, s)
			}
		case []string:
			for _, item := range v {
				add(item)
			}
		case []any:
			for _, item := range v {
				add(item)
			}
		}
	}
	addMap := func(m map[string]any) {
		if len(m) == 0 {
			return
		}
		add(m["artifact_id"])
		add(m["artifactId"])
		add(m["id"])
	}
	addFileArtifactMap := func(m map[string]any) {
		if len(m) == 0 {
			return
		}
		add(m["attachment_artifact_id"])
		add(m["attachmentArtifactId"])
		add(m["attachment_artifact_ids"])
		add(m["attachmentArtifactIds"])
		add(m["file_artifact_id"])
		add(m["fileArtifactId"])
		add(m["file_artifact_ids"])
		add(m["fileArtifactIds"])
	}
	walkList := func(results any, includeMessageID bool) {
		items, ok := results.([]any)
		if !ok {
			return
		}
		for _, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if includeMessageID {
				add(row["artifact_id"])
				add(row["artifactId"])
				add(row["doc_id"])
				if _, hasMessageID := row["message_id"]; hasMessageID {
					add(row["message_id"])
				} else {
					add(row["id"])
				}
				continue
			}
			addMap(row)
			add(row["message_id"])
			add(row["doc_id"])
			add(row["artifact_id"])
		}
	}
	walkEvidence := func(results any) {
		items, ok := results.([]any)
		if !ok {
			return
		}
		for _, item := range items {
			row, ok := item.(map[string]any)
			if !ok {
				continue
			}
			artifactID := ""
			if v, ok := row["artifact_id"].(string); ok {
				artifactID = strings.TrimSpace(v)
			}
			if artifactID != "" {
				add(artifactID)
				add(row["message_id"])
				add(row["doc_id"])
				continue
			}
			add(row["id"])
		}
	}
	addFileArtifactMap(out.ResultMetadata)

	m, ok := out.Result.(map[string]any)
	if !ok {
		return dedupeStringSlice(raw)
	}
	addFileArtifactMap(m)
	switch toolName {
	case "rlm_exec":
		walkEvidence(m["evidence"])
	case "search_threads", "search_docs":
		walkList(m["results"], false)
	case "get_doc":
		addMap(m)
	case "list_messages":
		walkList(m["messages"], true)
		artifactList, ok := m["artifact_ids"].([]any)
		if ok {
			add(artifactList)
		}
	case "get_artifact", "inspect_attachment", "read_attachment", "search_attachment":
		addMap(m)
	}
	return dedupeStringSlice(raw)
}

func newContextSummarizer(pf *providers.Factory) SubLLMSummarizer {
	return newContextSummarizerWithProvider(func() (types.Provider, error) {
		if pf == nil {
			return nil, errors.New("provider factory is required")
		}
		return pf.CreateProvider("")
	})
}

func newContextSummarizerForSnapshot(pf *providers.Factory, snapshot providers.DispatchSnapshot) SubLLMSummarizer {
	return newContextSummarizerWithProvider(func() (types.Provider, error) {
		if pf == nil {
			return nil, errors.New("provider factory is required")
		}
		return pf.CreateProviderFromSnapshot(snapshot, localSearchAgentHostedToolPolicy())
	})
}

func newContextSummarizerWithProvider(createProvider func() (types.Provider, error)) SubLLMSummarizer {
	return func(ctx context.Context, text string, instruction string) (string, error) {
		if createProvider == nil {
			return "", errors.New("provider factory is required")
		}
		attr, _ := usage.AttributionFromContext(ctx)
		attr.Operation = usage.OperationContextSummary
		ctx = usage.WithAttribution(ctx, attr)
		prov, err := createProvider()
		if err != nil {
			return "", err
		}
		model := strings.TrimSpace(prov.Model())
		if model == "" {
			return "", errors.New("model is required")
		}
		payload := strings.Join([]string{
			handoverSummaryInstruction,
			"Instruction:",
			strings.TrimSpace(instruction),
			"",
			"Thread history to summarize:",
			strings.TrimSpace(text),
		}, "\n")
		msg := types.ChatMessage{
			Role:    types.RoleUser,
			Content: &payload,
		}
		stream, err := prov.StreamChat(ctx, types.ChatRequest{
			Model:        model,
			SystemPrompt: "You are a handover summarizer. Preserve key findings, queries, and artifact references without inventing details.",
			Messages:     []types.ChatMessage{msg},
			Tools:        nil,
		})
		if err != nil {
			return "", err
		}
		var out strings.Builder
		for ev := range stream {
			switch ev.Event {
			case types.EventTextDelta:
				if d, ok := ev.Data.(types.TextDeltaData); ok {
					out.WriteString(d.Content)
				}
			case types.EventFinal:
				if d, ok := ev.Data.(types.FinalData); ok && strings.TrimSpace(d.Answer) != "" {
					out.Reset()
					out.WriteString(d.Answer)
				}
			case types.EventError:
				if d, ok := ev.Data.(types.ErrorData); ok {
					return "", errors.New(d.Message)
				}
			}
		}
		summary := strings.TrimSpace(out.String())
		if summary == "" {
			return "", errors.New("empty sub llm output")
		}
		if len([]rune(summary)) > handoverSummaryMaxChars {
			summary = truncateRunes(summary, handoverSummaryMaxChars)
		}
		return summary, nil
	}
}

func toUIMessage(m types.ChatMessage) map[string]any {
	out := map[string]any{
		"role":    string(m.Role),
		"content": nil,
	}
	if id := strings.TrimSpace(m.ID); id != "" {
		out["id"] = id
	}
	if m.Seq != nil && *m.Seq > 0 {
		out["seq"] = *m.Seq
	}
	if m.Content != nil {
		out["content"] = *m.Content
	}
	if len(m.ToolCalls) > 0 {
		out["tool_calls"] = m.ToolCalls
	}
	if strings.TrimSpace(m.ToolCallID) != "" {
		out["tool_call_id"] = strings.TrimSpace(m.ToolCallID)
	}
	if strings.TrimSpace(m.Name) != "" {
		out["name"] = strings.TrimSpace(m.Name)
	}
	if len(m.FileAttachments) > 0 {
		attachments := types.CloneFileAttachments(m.FileAttachments)
		for i := range attachments {
			attachments[i].InlineDataBase64 = ""
		}
		out["file_attachments"] = attachments
	}
	return out
}

func cloneRawJSON(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

func cloneProviderStateMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func providerPayloadState(raw json.RawMessage, providerID string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		ProviderID string         `json:"provider_id"`
		State      map[string]any `json:"state"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	if providerID != "" && strings.TrimSpace(payload.ProviderID) != "" && strings.TrimSpace(payload.ProviderID) != strings.TrimSpace(providerID) {
		return nil
	}
	return cloneProviderStateMap(payload.State)
}

const providerPayloadTargetBindingField = "target_binding"

type providerPayloadTargetBinding struct {
	ConnectionID string `json:"connection_id"`
	Model        string `json:"model"`
}

// bindProviderPayloadTarget adds app-owned target metadata alongside the
// provider's native payload fields. Provider adapter decoders intentionally
// ignore unknown top-level fields, so existing replay formats stay compatible.
func bindProviderPayloadTarget(raw json.RawMessage, connectionID, model string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	connectionID = strings.TrimSpace(connectionID)
	model = strings.TrimSpace(model)
	if connectionID == "" || model == "" {
		return nil
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		// An opaque payload that cannot carry an exact binding must not be
		// persisted as reusable native state for a plan-based run.
		return nil
	}
	bindingJSON, err := json.Marshal(providerPayloadTargetBinding{
		ConnectionID: connectionID,
		Model:        model,
	})
	if err != nil {
		return nil
	}
	payload[providerPayloadTargetBindingField] = bindingJSON
	bound, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return bound
}

// providerPayloadBinding reports whether the app-owned field is present as
// well as its decoded value. Presence is distinct from validity so malformed
// or partially-bound historical payloads can never be adopted by a target.
func providerPayloadBinding(raw json.RawMessage) (providerPayloadTargetBinding, bool) {
	if len(raw) == 0 {
		return providerPayloadTargetBinding{}, false
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil || payload == nil {
		return providerPayloadTargetBinding{}, false
	}
	bindingJSON, present := payload[providerPayloadTargetBindingField]
	if !present {
		return providerPayloadTargetBinding{}, false
	}
	var binding providerPayloadTargetBinding
	if err := json.Unmarshal(bindingJSON, &binding); err != nil {
		return providerPayloadTargetBinding{}, true
	}
	binding.ConnectionID = strings.TrimSpace(binding.ConnectionID)
	binding.Model = strings.TrimSpace(binding.Model)
	return binding, true
}

func providerPayloadMatchesTarget(raw json.RawMessage, connectionID, model string) bool {
	binding, present := providerPayloadBinding(raw)
	if !present {
		return false
	}
	return binding.ConnectionID != "" && binding.Model != "" &&
		binding.ConnectionID == strings.TrimSpace(connectionID) &&
		binding.Model == strings.TrimSpace(model)
}

// providerHistoryForRun keeps portable message fields intact while controlling
// only provider-native payload replay. allowUnbound is used solely after an
// engine loop that began with filtered history; at that point an unbound
// payload can only have been generated by the current exact target.
func providerHistoryForRun(history []types.ChatMessage, run store.Run, allowUnbound bool) []types.ChatMessage {
	if strings.TrimSpace(run.PlanID) == "" || len(history) == 0 {
		return history
	}
	out := append([]types.ChatMessage(nil), history...)
	for idx := range out {
		if len(out[idx].ProviderPayload) == 0 {
			continue
		}
		binding, present := providerPayloadBinding(out[idx].ProviderPayload)
		if present {
			if binding.ConnectionID == strings.TrimSpace(run.ConnectionID) &&
				binding.Model == strings.TrimSpace(run.Model) &&
				binding.ConnectionID != "" && binding.Model != "" {
				out[idx].ProviderPayload = cloneRawJSON(out[idx].ProviderPayload)
			} else {
				out[idx].ProviderPayload = nil
			}
			continue
		}
		if allowUnbound {
			out[idx].ProviderPayload = bindProviderPayloadTarget(out[idx].ProviderPayload, run.ConnectionID, run.Model)
		} else {
			out[idx].ProviderPayload = nil
		}
	}
	return out
}

func providerStateFromHistory(history []types.ChatMessage, providerID string) map[string]any {
	for idx := len(history) - 1; idx >= 0; idx-- {
		msg := history[idx]
		if msg.Role != types.RoleAssistant {
			continue
		}
		if state := providerPayloadState(msg.ProviderPayload, providerID); len(state) > 0 {
			return state
		}
	}
	return nil
}

func providerStateFromHistoryForRun(history []types.ChatMessage, run store.Run) map[string]any {
	if strings.TrimSpace(run.PlanID) == "" {
		return providerStateFromHistory(history, run.ProviderID)
	}
	for idx := len(history) - 1; idx >= 0; idx-- {
		msg := history[idx]
		if msg.Role != types.RoleAssistant || !providerPayloadMatchesTarget(msg.ProviderPayload, run.ConnectionID, run.Model) {
			continue
		}
		if state := providerPayloadState(msg.ProviderPayload, run.ProviderID); len(state) > 0 {
			return state
		}
	}
	return nil
}

func (m *Manager) TestTool(ctx context.Context, req ToolTestRequest) (ToolTestResult, error) {
	toolName := strings.TrimSpace(req.ToolName)
	if toolName == "" {
		return ToolTestResult{}, errors.New("tool_name is required")
	}
	meta, ok := tools.ToolMetadataByName(toolName)
	if !ok {
		if capability, ok := tools.ProviderHostedToolCapabilityByID(toolName); ok {
			if !capability.Test.Supported || capability.Test.Mode == tools.ToolTestModeNone {
				return ToolTestResult{}, fmt.Errorf("tool %s does not support testing", capability.ID)
			}
			return providerHostedToolTestResult(capability), nil
		}
		return ToolTestResult{}, fmt.Errorf("unknown tool: %s", toolName)
	}
	if !meta.Test.Supported || meta.Test.Mode == tools.ToolTestModeNone {
		return ToolTestResult{}, fmt.Errorf("tool %s does not support testing", meta.Name)
	}
	if m == nil || m.tools == nil {
		return ToolTestResult{}, errors.New("tool registry is not configured")
	}
	testArgs := req.Args
	testQueryJobs := m.queryJobs
	testStore := m.store
	testLongHorizon := m.longHorizon
	testThreadID := strings.TrimSpace(req.ThreadID)
	testRunID := "tool_test"
	cleanupSyntheticStore := func() {}
	if meta.Name == "validate_query_result" && toolTestArgsAreEmpty(req.Args) {
		seededArgs, seededJobs, seededStore, seededLongHorizon, seededThreadID, seededRunID, cleanup, err := prepareSyntheticQueryValidationToolTest(ctx)
		if err != nil {
			return ToolTestResult{}, err
		}
		testArgs = seededArgs
		testQueryJobs = seededJobs
		testStore = seededStore
		testLongHorizon = seededLongHorizon
		testThreadID = seededThreadID
		testRunID = seededRunID
		cleanupSyntheticStore = cleanup
	} else if seededArgs, seededJobs, seededThreadID, ok, err := prepareSyntheticQueryLifecycleToolTest(ctx, meta.Name, testThreadID, req.Args); err != nil {
		return ToolTestResult{}, err
	} else if ok {
		testArgs = seededArgs
		testQueryJobs = seededJobs
		testThreadID = seededThreadID
	}
	if seededArgs, seededStore, seededThreadID, seededRunID, cleanup, ok, err := prepareSyntheticArxivToolTest(ctx, meta.Name, req.Args); err != nil {
		return ToolTestResult{}, err
	} else if ok {
		testArgs = seededArgs
		testStore = seededStore
		testThreadID = seededThreadID
		testRunID = seededRunID
		cleanupSyntheticStore = cleanup
	}
	defer cleanupSyntheticStore()

	args, err := tools.PrepareToolTestArgs(meta, testArgs)
	if err != nil {
		return ToolTestResult{}, err
	}
	decodedArgs, err := tools.DecodePreparedToolTestArgs(args)
	if err != nil {
		return ToolTestResult{}, err
	}
	if missingContext := missingUserSuppliedToolTestContext(meta, req, testThreadID); len(missingContext) > 0 {
		return ToolTestResult{}, &tools.MissingToolTestInputError{
			ToolName:        meta.Name,
			MissingContext:  missingContext,
			RequiredContext: append([]string(nil), meta.Test.RequiredContext...),
			InputHint:       meta.Test.InputHint,
		}
	}
	if missingContext := m.missingAutomaticToolTestContext(meta); len(missingContext) > 0 {
		return ToolTestResult{}, &tools.MissingToolTestInputError{
			ToolName:        meta.Name,
			MissingContext:  missingContext,
			RequiredContext: append([]string(nil), meta.Test.RequiredContext...),
			InputHint:       meta.Test.InputHint,
		}
	}
	tool, ok := m.tools.Get(meta.Name)
	if !ok {
		return ToolTestResult{}, fmt.Errorf("tool %s is not available in the current registry", meta.Name)
	}
	permissionReq := req
	permissionReq.ThreadID = testThreadID
	approvedArgs, err := m.requireToolTestPermission(ctx, permissionReq, meta, args)
	if err != nil {
		return ToolTestResult{}, err
	}
	args = approvedArgs
	decodedArgs, err = tools.DecodePreparedToolTestArgs(args)
	if err != nil {
		return ToolTestResult{}, err
	}

	progress := []map[string]any{}
	progressFn := func(update map[string]any) {
		copied := make(map[string]any, len(update)+2)
		for key, value := range update {
			copied[key] = value
		}
		copied["tool_name"] = meta.Name
		copied["timestamp"] = time.Now().UnixMilli()
		progress = append(progress, copied)
	}
	execCtx := tools.ExecContext{
		ProjectID:             strings.TrimSpace(req.ProjectID),
		Projects:              m.projects,
		BigQuery:              m.bigquery,
		Databricks:            m.databricks,
		Trino:                 m.trino,
		SnowflakeHTTP:         m.snowflake,
		Store:                 testStore,
		Usage:                 m.usage,
		LongHorizon:           testLongHorizon,
		ThreadID:              testThreadID,
		RunID:                 testRunID,
		ToolCallID:            "tool_test_" + meta.Name,
		CfgDir:                m.cfgDir,
		SearchDepth:           strings.TrimSpace(req.SearchDepth),
		EvidenceBudget:        defaultEvidenceBudget,
		DatabaseQueryRowLimit: config.MaxDatabaseQueryRowLimit,
		ChartEngines:          defaultChartEngineEnablement().toToolFlags(),
		OnProgress:            progressFn,
	}
	setExecContextQueryJobs(&execCtx, testQueryJobs)

	out, err := tool.Execute(ctx, execCtx, args)
	if err != nil {
		out = tools.JSONError(err.Error())
	}
	m.promoteToolTestExternalFileArtifacts(ctx, testStore, testThreadID, testRunID, meta.Name, decodedArgs, &out)
	out = enrichToolOutputResultMetadata(meta.Name, out)
	return ToolTestResult{
		ToolName:         meta.Name,
		Mode:             meta.Test.Mode,
		RequiresApproval: meta.Test.RequiresApproval,
		Category:         meta.Category,
		Origin:           meta.Origin,
		ToolSetID:        meta.ToolSetID,
		ConnectorID:      meta.ConnectorID,
		Presentation:     meta.Presentation,
		Args:             decodedArgs,
		Output:           out,
		Progress:         progress,
	}, nil
}

func providerHostedToolTestResult(capability tools.ProviderHostedToolCapability) ToolTestResult {
	presentation := capability.Presentation
	presentationMap := toolPresentationSpecMap(presentation)
	resultMetadata := map[string]any{
		"tool_name":               capability.Name,
		"provider_tool":           true,
		"provider_hosted_tool_id": capability.ID,
		"adapter_family":          capability.AdapterFamily,
		"settings_key":            capability.SettingsKey,
		"tool_set_id":             capability.ToolSetID,
		"tool_set_name":           capability.ToolSetName,
		"origin":                  capability.Origin,
		"category":                string(capability.Category),
		"permission_subject_kind": capability.PermissionSubjectKind,
		"provider_executed":       capability.ProviderExecuted,
		"integration":             "provider-hosted",
		"kind":                    presentation.ResultKind,
		"renderer_strategy":       presentation.RendererStrategy,
		"renderer_key":            presentation.RendererKey,
		"native_viewer":           presentation.NativeViewer,
		"tool_presentation":       presentationMap,
		"readonly":                capability.Category == tools.ToolCategoryRead,
	}
	result := map[string]any{
		"ok":                      true,
		"status":                  "catalog_check_passed",
		"not_executed":            true,
		"tool_name":               capability.Name,
		"provider_hosted_tool_id": capability.ID,
		"adapter_family":          capability.AdapterFamily,
		"settings_key":            capability.SettingsKey,
		"provider_tool_names":     append([]string(nil), capability.ProviderToolNames...),
		"message":                 "Provider-hosted capability is cataloged and permissionable. This settings check does not execute provider code.",
	}
	return ToolTestResult{
		ToolName:         capability.ID,
		Mode:             capability.Test.Mode,
		RequiresApproval: capability.Test.RequiresApproval,
		Category:         capability.Category,
		Origin:           tools.ToolOrigin(capability.Origin),
		ToolSetID:        capability.ToolSetID,
		ConnectorID:      capability.AdapterFamily,
		Presentation:     presentation,
		Args:             map[string]any{},
		Output:           tools.JSONOKWithMetadata(result, resultMetadata),
		Progress: []map[string]any{
			{
				"tool_name": capability.ID,
				"phase":     "catalog_check",
				"message":   "Provider-hosted capability catalog check completed.",
				"timestamp": time.Now().UnixMilli(),
			},
		},
	}
}

func toolPresentationSpecMap(p tools.ToolPresentationSpec) map[string]any {
	out := map[string]any{
		"family":            p.Family,
		"result_kind":       p.ResultKind,
		"renderer_strategy": p.RendererStrategy,
		"renderer_key":      p.RendererKey,
		"native_viewer":     p.NativeViewer,
	}
	if len(p.ArtifactTypes) > 0 {
		out["artifact_types"] = append([]string(nil), p.ArtifactTypes...)
	}
	if p.FileCapability != "" {
		out["file_capability"] = p.FileCapability
	}
	if len(p.FileKinds) > 0 {
		out["file_kinds"] = append([]string(nil), p.FileKinds...)
	}
	if len(p.PreviewModes) > 0 {
		out["preview_modes"] = append([]string(nil), p.PreviewModes...)
	}
	if len(p.SourceRefs) > 0 {
		out["source_refs"] = append([]string(nil), p.SourceRefs...)
	}
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	if len(p.NativeLinks) > 0 {
		out["native_links"] = append([]string(nil), p.NativeLinks...)
	}
	return out
}

func (m *Manager) promoteToolTestExternalFileArtifacts(
	ctx context.Context,
	st store.Store,
	threadID string,
	runID string,
	toolName string,
	decodedArgs map[string]any,
	out *tools.Output,
) {
	if m == nil || st == nil || out == nil || strings.TrimSpace(threadID) == "" {
		return
	}
	thread, err := st.GetThread(ctx, threadID)
	if err != nil {
		log.Printf("tool test %s: skipped native file promotion: %v", toolName, err)
		return
	}
	callID := "tool_test_" + strings.TrimSpace(toolName)
	run := store.Run{
		ID:       firstNonEmptyString(strings.TrimSpace(runID), "tool_test"),
		ThreadID: thread.ID,
	}
	call := types.ToolCall{
		ID:   callID,
		Name: strings.TrimSpace(toolName),
		Args: cloneStringAnyMap(decodedArgs),
	}
	pending := m.prepareExternalToolFileArtifacts(ctx, thread, run, call, "", callID, out)
	for _, item := range pending {
		if _, err := st.InsertArtifact(ctx, item.Artifact); err != nil {
			if errors.Is(err, store.ErrConflict) {
				continue
			}
			log.Printf("tool test %s: failed to persist native file artifact %s: %v", toolName, item.Artifact.ID, err)
		}
	}
}

func prepareSyntheticQueryLifecycleToolTest(ctx context.Context, toolName string, requestedThreadID string, args json.RawMessage) (json.RawMessage, *jobs.Registry, string, bool, error) {
	if !isQueryLifecycleToolTest(toolName) || !toolTestArgsAreEmpty(args) {
		return nil, nil, "", false, nil
	}
	threadID := strings.TrimSpace(requestedThreadID)
	if threadID == "" {
		threadID = "tool_test_thread"
	}
	owner := jobs.JobOwner{
		ThreadID: threadID,
		RunID:    "tool_test",
		Handle:   "tool_test_owner",
	}
	registry := jobs.NewRegistry(jobs.RegistryOptions{})

	var poll jobs.PollFunc
	var cancel jobs.CancelFunc
	if toolName == "get_query_results" {
		poll = func(ctx context.Context) (bool, *jobs.QueryResult, error) {
			return true, &jobs.QueryResult{
				Rows: []map[string]any{
					{"metric": "synthetic", "value": 1},
				},
				Schema: []map[string]any{
					{"name": "metric", "type": "STRING"},
					{"name": "value", "type": "INTEGER"},
				},
				Metadata: map[string]any{
					"tool_test": true,
				},
			}, nil
		}
	}
	if toolName == "cancel_query" {
		cancel = func(ctx context.Context) error { return nil }
	}

	job, err := registry.Register(owner, "tool_test", "select 1 as value", "select 1", map[string]any{"tool_test": true}, poll, cancel)
	if err != nil {
		return nil, nil, "", false, err
	}
	if toolName == "get_query_results" {
		if err := waitForSyntheticQueryJobCompletion(ctx, registry, job.ID, owner); err != nil {
			return nil, nil, "", false, err
		}
	}
	prepared := map[string]any{
		"job_id":      job.ID,
		"owner_token": owner.Handle,
	}
	if toolName == "cancel_query" {
		prepared["reason"] = string(jobs.CancelReasonNoLongerNeeded)
	}
	b, err := json.Marshal(prepared)
	if err != nil {
		return nil, nil, "", false, err
	}
	return json.RawMessage(b), registry, threadID, true, nil
}

// syntheticQueryValidationAdapter provides one deterministic, fully managed
// completed result for the settings-page sample call. It never reaches a
// warehouse and exercises the same durable lifecycle as a real query.
type syntheticQueryValidationAdapter struct{}

func (syntheticQueryValidationAdapter) Provider() string { return "tool_test" }
func (syntheticQueryValidationAdapter) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{DetachedRepoll: true, NativePagination: true, CancelConfirmable: true}
}
func (syntheticQueryValidationAdapter) Submit(context.Context, jobs.SubmitRequest) (jobs.SubmitResult, error) {
	return jobs.SubmitResult{Handle: jobs.NativeHandle{Provider: "tool_test", ID: "synthetic-validation-result"}}, nil
}
func (syntheticQueryValidationAdapter) Poll(context.Context, jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	return jobs.StatusSnapshot{State: jobs.ProviderStateSucceeded, RawState: "SUCCEEDED"}, nil
}
func (syntheticQueryValidationAdapter) Fetch(context.Context, jobs.NativeHandle, jobs.FetchCursor) (jobs.ResultPage, error) {
	return jobs.ResultPage{
		Rows:     []map[string]any{{"metric": "synthetic", "value": 1}},
		Schema:   []map[string]any{{"name": "metric", "type": "STRING"}, {"name": "value", "type": "INTEGER"}},
		Metadata: map[string]any{"tool_test": true},
	}, nil
}
func (syntheticQueryValidationAdapter) Cancel(context.Context, jobs.NativeHandle) error { return nil }
func (syntheticQueryValidationAdapter) ConfirmCancel(context.Context, jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	return true, jobs.StatusSnapshot{State: jobs.ProviderStateCancelled}, nil
}
func (syntheticQueryValidationAdapter) Adopt(context.Context, jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	return jobs.AdoptOutcomeTerminal, jobs.StatusSnapshot{State: jobs.ProviderStateSucceeded}, nil
}

func prepareSyntheticQueryValidationToolTest(ctx context.Context) (
	json.RawMessage,
	*jobs.Registry,
	store.Store,
	store.LongHorizonRepository,
	string,
	string,
	func(),
	error,
) {
	tmpDir, err := os.MkdirTemp("", "ai-analyst-query-validation-tool-test-*")
	if err != nil {
		return nil, nil, nil, nil, "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	st, err := store.NewJSON(tmpDir)
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, "", "", nil, err
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, "", "", nil, err
	}
	run, err := st.CreateRun(ctx, thread.ID, "tool-test", "synthetic", nil, "Synthetic durable query validation fixture.", 1)
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, "", "", nil, err
	}
	adapter := syntheticQueryValidationAdapter{}
	registry := jobs.NewRegistry(jobs.RegistryOptions{
		Store:        st,
		Materializer: store.NewQueryMaterializer(st),
		Scheduler: &jobs.SchedulerConfig{
			BaseIntervals: []time.Duration{5 * time.Millisecond},
			MaxInterval:   5 * time.Millisecond,
		},
	})
	registry.RegisterAdapter(adapter)
	owner := jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tool_test_owner"}
	job, err := registry.Launch(ctx, jobs.LaunchRequest{
		Owner:      owner,
		Provider:   adapter.Provider(),
		Adapter:    adapter,
		SQL:        "select 'synthetic' as metric, 1 as value",
		SQLPreview: "select synthetic validation row",
		Metadata:   map[string]any{"tool_test": true},
		Budgets:    jobs.Budgets{MaxResultRows: 10, MaxResultBytes: 64 << 10},
	})
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, "", "", nil, err
	}
	if err := waitForSyntheticQueryJobCompletion(ctx, registry, job.ID, owner); err != nil {
		cleanup()
		return nil, nil, nil, nil, "", "", nil, err
	}
	prepared, err := json.Marshal(map[string]any{
		"job_id":      job.ID,
		"owner_token": owner.Handle,
		"verdict":     string(jobs.ValidationValidated),
		"detail":      "Synthetic schema, row count, and returned value were checked.",
		"checks": []map[string]any{
			{"category": "result_availability", "name": "result is retrievable", "passed": true, "detail": "the managed completed result is available"},
			{"category": "schema_shape", "name": "schema matches", "passed": true, "detail": "metric and value columns are present"},
			{"category": "row_count_materialization", "name": "row count is materialized", "passed": true, "detail": "one deterministic row returned"},
		},
		"evidence": []map[string]any{
			{"category": "result_availability", "name": "managed result", "observed": "completed result is retrievable", "source": "get_query_results", "result_available": true},
			{"category": "schema_shape", "name": "retrieved schema", "observed": "metric, value", "source": "get_query_results", "column_count": 2, "columns": []string{"metric", "value"}},
			{"category": "row_count_materialization", "name": "materialized rows", "observed": "1 row", "source": "get_query_results", "row_count": 1, "materialization_state": "complete"},
		},
	})
	if err != nil {
		cleanup()
		return nil, nil, nil, nil, "", "", nil, err
	}
	return json.RawMessage(prepared), registry, st, st, thread.ID, run.ID, cleanup, nil
}

func isQueryLifecycleToolTest(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "check_query_status", "get_query_results", "validate_query_result", "cancel_query":
		return true
	default:
		return false
	}
}

func prepareSyntheticArxivToolTest(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, store.Store, string, string, func(), bool, error) {
	if !isArxivSyntheticToolTest(toolName) || !toolTestArgsAreEmpty(args) {
		return nil, nil, "", "", nil, false, nil
	}

	tmpDir, err := os.MkdirTemp("", "ai-analyst-arxiv-tool-test-*")
	if err != nil {
		return nil, nil, "", "", nil, false, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	st, err := store.NewJSON(tmpDir)
	if err != nil {
		cleanup()
		return nil, nil, "", "", nil, false, err
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		cleanup()
		return nil, nil, "", "", nil, false, err
	}
	run, err := st.CreateRun(ctx, thread.ID, "tool-test", "synthetic", nil, "Synthetic arXiv tool test fixture.", 1)
	if err != nil {
		cleanup()
		return nil, nil, "", "", nil, false, err
	}
	if err := appendSyntheticArxivSearch(ctx, st, thread.ID, run.ID); err != nil {
		cleanup()
		return nil, nil, "", "", nil, false, err
	}

	var prepared map[string]any
	switch strings.TrimSpace(toolName) {
	case tools.ArxivToolScreeningReport:
		prepared = map[string]any{
			"interpreted_request": "Find sparse attention papers for a synthetic tool test.",
			"query_rationales": []map[string]any{
				{
					"tool_call_id": "tool-test-arxiv-search",
					"phase":        "synthetic_fixture",
					"rationale":    "Seed a deterministic arXiv search trace for the settings tool test.",
				},
			},
			"verdicts": []map[string]any{
				{
					"arxiv_id": "2301.12345",
					"status":   "kept",
					"reason":   "Synthetic paper matches sparse attention.",
					"evidence": []map[string]any{
						{"source_field": "summary", "quote": "sparse attention"},
					},
				},
			},
		}
	case tools.ArxivToolRecommendations:
		if err := appendSyntheticArxivScreeningReport(ctx, st, thread.ID, run.ID); err != nil {
			cleanup()
			return nil, nil, "", "", nil, false, err
		}
		prepared = map[string]any{
			"screening_report_tool_call_id": "tool-test-arxiv-report",
			"selection_basis":               "Synthetic recommendation from a kept screening result.",
			"recommendations": []map[string]any{
				{
					"arxiv_id": "2301.12345",
					"rank":     1,
					"tier":     "primary",
					"reason":   "Synthetic recommendation from the seeded arXiv screening report.",
				},
			},
		}
	default:
		cleanup()
		return nil, nil, "", "", nil, false, nil
	}
	raw, err := json.Marshal(prepared)
	if err != nil {
		cleanup()
		return nil, nil, "", "", nil, false, err
	}
	return json.RawMessage(raw), st, thread.ID, run.ID, cleanup, true, nil
}

func isArxivSyntheticToolTest(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case tools.ArxivToolScreeningReport, tools.ArxivToolRecommendations:
		return true
	default:
		return false
	}
}

func missingUserSuppliedToolTestContext(meta tools.ToolMetadata, req ToolTestRequest, threadID string) []string {
	missing := []string{}
	for _, raw := range meta.Test.RequiredContext {
		switch strings.TrimSpace(raw) {
		case "project":
			if strings.TrimSpace(req.ProjectID) == "" {
				missing = append(missing, "project")
			}
		case "thread":
			if strings.TrimSpace(threadID) == "" {
				missing = append(missing, "thread")
			}
		}
	}
	return missing
}

func (m *Manager) missingAutomaticToolTestContext(meta tools.ToolMetadata) []string {
	missing := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range missing {
			if existing == value {
				return
			}
		}
		missing = append(missing, value)
	}
	toolAvailable := false
	if m != nil && m.tools != nil {
		_, toolAvailable = m.tools.Get(meta.Name)
	}
	for _, raw := range meta.Test.RequiredContext {
		value := strings.TrimSpace(raw)
		switch {
		case value == "artifact":
			if m == nil || m.store == nil {
				add(value)
			}
		case value == "search_index":
			if !toolAvailable {
				add(value)
			}
		case value == "browser":
			if !toolAvailable {
				add(value)
			}
		case strings.HasPrefix(value, "connection:"):
			connectorID := strings.TrimSpace(strings.TrimPrefix(value, "connection:"))
			if !m.toolTestConnectorAvailable(connectorID, toolAvailable) {
				add(value)
			}
		}
	}
	return missing
}

func (m *Manager) toolTestConnectorAvailable(connectorID string, toolAvailable bool) bool {
	switch strings.TrimSpace(connectorID) {
	case "bigquery":
		return m != nil && m.bigquery != nil
	case "databricks":
		return m != nil && m.databricks != nil
	case "trino":
		return m != nil && m.trino != nil
	case "snowflake":
		return m != nil && m.snowflake != nil
	default:
		return toolAvailable
	}
}

func appendSyntheticArxivSearch(ctx context.Context, st store.Store, threadID string, runID string) error {
	_, err := st.AppendMessage(ctx, threadID, &runID, store.Message{
		Role: types.RoleAssistant,
		ToolCalls: []types.ToolCall{{
			ID:   "tool-test-arxiv-search",
			Name: tools.ArxivToolSearch,
			Args: map[string]any{"query": "sparse attention"},
		}},
	})
	if err != nil {
		return err
	}
	result := map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":         "2301.12345",
				"version":          1,
				"title":            "Synthetic Sparse Attention Paper",
				"summary":          "We study sparse attention for efficient transformers.",
				"authors":          []any{"Synthetic Author"},
				"categories":       []any{"cs.LG"},
				"primary_category": "cs.LG",
				"downloaded":       false,
			},
		},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	}
	env := map[string]any{
		"artifact_id":     "tool-test-arxiv-search-artifact",
		"tool_name":       tools.ArxivToolSearch,
		"tool_call_id":    "tool-test-arxiv-search",
		"ok":              true,
		"result_format":   "json",
		"result":          result,
		"result_metadata": map[string]any{"provider": "arxiv", "integration": "arxiv", "kind": "search_results"},
	}
	return appendSyntheticToolResult(ctx, st, threadID, runID, tools.ArxivToolSearch, "tool-test-arxiv-search", env)
}

func appendSyntheticArxivScreeningReport(ctx context.Context, st store.Store, threadID string, runID string) error {
	report := map[string]any{
		"schema":    "arxiv_research_audit.v1",
		"run_id":    runID,
		"thread_id": threadID,
		"papers": []map[string]any{
			{
				"arxiv_id": "2301.12345",
				"version":  1,
				"title":    "Synthetic Sparse Attention Paper",
				"summary":  "We study sparse attention for efficient transformers.",
				"status":   "kept",
				"evidence": []map[string]any{
					{
						"source_field": "summary",
						"source_kind":  "validated",
						"quote":        "sparse attention",
					},
				},
			},
		},
	}
	env := map[string]any{
		"artifact_id":     "tool-test-arxiv-report-artifact",
		"tool_name":       tools.ArxivToolScreeningReport,
		"tool_call_id":    "tool-test-arxiv-report",
		"ok":              true,
		"result_format":   "json",
		"result":          report,
		"result_metadata": map[string]any{"provider": "arxiv", "integration": "arxiv", "kind": "screening_report", "schema": "arxiv_research_audit.v1"},
	}
	return appendSyntheticToolResult(ctx, st, threadID, runID, tools.ArxivToolScreeningReport, "tool-test-arxiv-report", env)
}

func appendSyntheticToolResult(ctx context.Context, st store.Store, threadID string, runID string, toolName string, toolCallID string, envelope map[string]any) error {
	contentBytes, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	content := string(contentBytes)
	name := toolName
	callID := toolCallID
	_, err = st.AppendMessage(ctx, threadID, &runID, store.Message{
		Role:       types.RoleTool,
		Content:    &content,
		ToolCallID: &callID,
		Name:       &name,
	})
	return err
}

func toolTestArgsAreEmpty(args json.RawMessage) bool {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte("{}")) {
		return true
	}
	return false
}

func waitForSyntheticQueryJobCompletion(ctx context.Context, registry *jobs.Registry, jobID string, owner jobs.JobOwner) error {
	deadline := time.Now().Add(2 * time.Second)
	lastStatus := jobs.JobStatusUnknown
	lastMaterialization := jobs.MaterializationNone
	for {
		job, ok := registry.Snapshot(jobID, owner)
		if ok {
			lastStatus = job.Status
			lastMaterialization = job.Materialization
			switch job.Status {
			case jobs.JobStatusCompleted:
				switch job.Materialization {
				case jobs.MaterializationComplete, jobs.MaterializationTruncated:
					return nil
				case "", jobs.MaterializationNone:
					// Legacy in-memory synthetic lifecycle fixtures have no
					// durable materializer. A non-nil completed result is their
					// honest result-ready boundary.
					if job.Result != nil {
						return nil
					}
				case jobs.MaterializationPartial:
					if detail := strings.TrimSpace(fmt.Sprint(job.Metadata["materialization_error"])); detail != "" && detail != "<nil>" {
						return fmt.Errorf("synthetic query materialization failed: %s", detail)
					}
				}
			case jobs.JobStatusFailed:
				if strings.TrimSpace(job.Error) != "" {
					return errors.New(strings.TrimSpace(job.Error))
				}
				return errors.New("synthetic query job failed")
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("synthetic query job did not become result-ready (status=%s materialization=%s)", lastStatus, lastMaterialization)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (m *Manager) requireToolTestPermission(ctx context.Context, req ToolTestRequest, meta tools.ToolMetadata, args json.RawMessage) (json.RawMessage, error) {
	if m == nil || m.permissions == nil {
		return args, nil
	}
	metas := m.permissionMetasForTool(ctx, strings.TrimSpace(req.ThreadID), meta.Name, args, strings.TrimSpace(req.ProjectID))
	if len(metas) == 0 {
		return args, nil
	}
	for _, permissionMeta := range metas {
		localComputerTool := isLocalComputerMachineTool(meta.Name)
		permissionMeta = m.resolveLocalComputerPathPermissionMeta(ctx, strings.TrimSpace(req.ThreadID), meta.Name, args, permissionMeta)
		if approvedArgs, ok, err := m.rootlessLocalComputerFileSearchArgs(ctx, strings.TrimSpace(req.ThreadID), meta.Name, args, permissionMeta); err != nil {
			return args, err
		} else if ok {
			args = approvedArgs
			continue
		}
		if approvedArgs, ok, err := m.rootlessLocalComputerFileReadArgs(ctx, strings.TrimSpace(req.ThreadID), meta.Name, args, permissionMeta); err != nil {
			return args, err
		} else if ok {
			args = approvedArgs
			continue
		}
		if localComputerTool {
			permissionMeta = m.resolveConfiguredLocalComputerPermissionMeta(meta.Name, args, permissionMeta)
		} else {
			permissionMeta = m.resolveSavedLocalLocationPermissionMeta(ctx, strings.TrimSpace(req.ThreadID), meta.Name, permissionMeta)
		}
		if permissionMeta.GrantID != "" && strings.TrimSpace(permissionMeta.LocalRoot) == "" {
			return args, &ToolTestPermissionError{
				ToolName: meta.Name,
				Decision: permissions.DecisionDeny,
				Category: permissionMeta.Category,
				Subject:  managerPermissionSubject(permissionMeta, meta.Name),
				Reason:   "Local file grant is not available.",
			}
		}
		evaluationMeta, subject, evaluation, err := m.evaluateSingleManagerToolPermission(ctx, strings.TrimSpace(req.ThreadID), meta.Name, permissionMeta)
		if err != nil {
			return args, err
		}
		if evaluation.Decision == permissions.DecisionAllow {
			args = injectLocalApprovalArgs(args, meta.Name, subject)
			continue
		}
		if localComputerTool && strings.TrimSpace(strings.ToLower(evaluationMeta.SubjectKind)) == "local_location" {
			return args, &ToolTestPermissionError{
				ToolName: meta.Name,
				Decision: permissions.DecisionDeny,
				Category: evaluationMeta.Category,
				Subject:  subject,
				Reason:   localComputerScopeRequiredMessage(meta.Name),
			}
		}
		reason := evaluation.Reason
		if reason == "" && evaluation.Decision == permissions.DecisionAsk {
			reason = "approval required"
		}
		return args, &ToolTestPermissionError{
			ToolName: meta.Name,
			Decision: evaluation.Decision,
			Category: evaluationMeta.Category,
			Subject:  subject,
			Reason:   reason,
		}
	}
	return args, nil
}

func (m *Manager) evaluateManagerToolPermission(ctx context.Context, threadID string, toolName string, args json.RawMessage, activeProjectID string) (managerPermissionMeta, store.PermissionSubject, permissions.EvaluationResult, bool, error) {
	if m == nil || m.permissions == nil {
		return managerPermissionMeta{}, store.PermissionSubject{}, permissions.EvaluationResult{}, false, nil
	}
	metas := m.permissionMetasForTool(ctx, threadID, toolName, args, activeProjectID)
	if len(metas) == 0 {
		return managerPermissionMeta{}, store.PermissionSubject{}, permissions.EvaluationResult{}, false, nil
	}
	toolName = strings.TrimSpace(toolName)
	var allowMeta managerPermissionMeta
	var allowSubject store.PermissionSubject
	var allowEvaluation permissions.EvaluationResult
	haveAllow := false
	for _, meta := range metas {
		meta = m.resolveLocalComputerPathPermissionMeta(ctx, strings.TrimSpace(threadID), toolName, args, meta)
		if isLocalComputerMachineTool(toolName) {
			meta = m.resolveConfiguredLocalComputerPermissionMeta(toolName, args, meta)
		} else {
			meta = m.resolveSavedLocalLocationPermissionMeta(ctx, strings.TrimSpace(threadID), toolName, meta)
		}
		evaluationMeta, subject, evaluation, err := m.evaluateSingleManagerToolPermission(ctx, strings.TrimSpace(threadID), toolName, meta)
		if err != nil {
			return managerPermissionMeta{}, store.PermissionSubject{}, permissions.EvaluationResult{}, false, err
		}
		if evaluation.Decision != permissions.DecisionAllow {
			return evaluationMeta, subject, evaluation, true, nil
		}
		allowMeta = evaluationMeta
		allowSubject = subject
		allowEvaluation = evaluation
		haveAllow = true
	}
	if haveAllow {
		return allowMeta, allowSubject, allowEvaluation, true, nil
	}
	return managerPermissionMeta{}, store.PermissionSubject{}, permissions.EvaluationResult{}, false, nil
}

func (m *Manager) hostedToolPolicyForRun(ctx context.Context, threadID string, scopes ...runConnectionScope) (providers.HostedToolPolicy, error) {
	policy := providers.HostedToolPolicy{Disabled: map[string]bool{}}
	if m == nil || m.permissions == nil {
		return applyHostedToolConnectionScope(policy, scopes...), nil
	}
	seen := map[string]bool{}
	for _, capability := range tools.ProviderHostedToolCapabilities() {
		// The Web Search connection toggle is the user's consent boundary.
		// Older builds could persist a separate provider-hosted permission deny;
		// do not let that stale policy silently override an enabled connection.
		if capability.SettingsKey == "webSearch" {
			continue
		}
		id := strings.TrimSpace(capability.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		meta := managerPermissionMeta{
			Category:    string(capability.Category),
			SubjectKind: "provider_hosted_tool",
			ToolSetID:   "provider-hosted",
			Connector:   strings.TrimSpace(capability.AdapterFamily),
			Operation:   strings.TrimSpace(capability.SettingsKey),
			Summary:     strings.TrimSpace(capability.Description),
		}
		_, _, evaluation, err := m.evaluateSingleManagerToolPermission(ctx, strings.TrimSpace(threadID), id, meta)
		if err != nil {
			return policy, err
		}
		if evaluation.Decision != permissions.DecisionAllow {
			policy.Disabled[id] = true
		}
	}
	return applyHostedToolConnectionScope(policy, scopes...), nil
}

func applyHostedToolConnectionScope(policy providers.HostedToolPolicy, scopes ...runConnectionScope) providers.HostedToolPolicy {
	if len(scopes) == 0 {
		return policy
	}
	if policy.Disabled == nil {
		policy.Disabled = map[string]bool{}
	}
	for _, capability := range tools.ProviderHostedToolCapabilities() {
		// activeConnectionIds was a client-derived legacy scope. New runs use
		// server settings as the source of truth, so an older cached frontend
		// must not be able to remove Web Search from an enabled connection.
		if capability.SettingsKey == "webSearch" {
			continue
		}
		connectionID := ""
		switch capability.SettingsKey {
		case "codeExecution":
			connectionID = "python-sandbox"
		}
		if connectionID != "" && !scopes[0][connectionID] {
			policy.Disabled[capability.ID] = true
		}
	}
	return policy
}

// applyHostedToolPlanCeiling intersects live permissions with the finalized
// plan. Revocations remain effective, while grants made after preflight cannot
// silently add a provider-executed capability the user never preflighted.
func (m *Manager) applyHostedToolPlanCeiling(ctx context.Context, run store.Run, policy providers.HostedToolPolicy) (providers.HostedToolPolicy, error) {
	if strings.TrimSpace(run.PlanID) == "" {
		return policy, nil
	}
	plan, err := m.store.GetRunPlanForRun(ctx, run.ID)
	if err != nil {
		return policy, err
	}
	var report capabilities.CapabilityReport
	if err := json.Unmarshal(plan.CapabilityReport, &report); err != nil {
		return policy, err
	}
	if policy.Disabled == nil {
		policy.Disabled = map[string]bool{}
	}
	for _, capability := range tools.ProviderHostedToolCapabilities() {
		var featureKey capabilities.FeatureKey
		switch capability.SettingsKey {
		case "webSearch":
			featureKey = capabilities.FeatureHostedWebSearch
		case "codeExecution":
			featureKey = capabilities.FeatureHostedCodeExecution
		default:
			policy.Disabled[capability.ID] = true
			continue
		}
		feature, ok := report.Features[featureKey]
		if !ok || feature.Effective != capabilities.EffectiveOn {
			policy.Disabled[capability.ID] = true
		}
	}
	return policy, nil
}

func (m *Manager) evaluateSingleManagerToolPermission(ctx context.Context, threadID string, toolName string, meta managerPermissionMeta) (managerPermissionMeta, store.PermissionSubject, permissions.EvaluationResult, error) {
	permissionScopeMeta := meta
	if isLocalComputerMachineTool(toolName) {
		permissionScopeMeta.Connector = "local-computer"
		permissionScopeMeta.ToolSetID = "local-computer"
	}
	toolSubject := store.PermissionSubject{
		Kind:      "tool",
		Tool:      toolName,
		Connector: permissionScopeMeta.Connector,
		ToolSetID: permissionScopeMeta.ToolSetID,
		Operation: permissionScopeMeta.Operation,
	}
	operationSubject := managerPermissionSubject(permissionScopeMeta, toolName)
	toolSetSubject, hasToolSetSubject := managerToolSetPermissionSubject(permissionScopeMeta)
	connectorSubject, hasConnectorSubject := managerConnectorPermissionSubject(permissionScopeMeta)

	type permissionCandidate struct {
		subject     store.PermissionSubject
		isOperation bool
	}
	candidates := []permissionCandidate{}
	if strings.EqualFold(strings.TrimSpace(operationSubject.Kind), "provider_hosted_tool") {
		candidates = append(candidates, permissionCandidate{subject: operationSubject, isOperation: true})
	} else {
		candidates = append(candidates, permissionCandidate{subject: toolSubject})
		if !samePermissionSubject(toolSubject, operationSubject) {
			candidates = append(candidates, permissionCandidate{subject: operationSubject, isOperation: true})
		} else {
			candidates[0].isOperation = true
		}
	}
	if hasToolSetSubject {
		already := false
		for _, candidate := range candidates {
			if samePermissionSubject(candidate.subject, toolSetSubject) {
				already = true
				break
			}
		}
		if !already {
			candidates = append(candidates, permissionCandidate{subject: toolSetSubject})
		}
	}
	if hasConnectorSubject {
		candidates = append(candidates, permissionCandidate{subject: connectorSubject})
	}

	var operationDefault permissions.EvaluationResult
	haveOperationDefault := false
	var matchedSubject store.PermissionSubject
	var matchedEvaluation permissions.EvaluationResult
	haveMatchedRule := false
	for _, candidate := range candidates {
		evaluation, err := m.permissions.Evaluate(ctx, permissions.EvaluationInput{
			ThreadID: threadID,
			Tool:     toolName,
			Category: meta.Category,
			Subject:  candidate.subject,
		})
		if err != nil {
			return managerPermissionMeta{}, store.PermissionSubject{}, permissions.EvaluationResult{}, err
		}
		if candidate.isOperation {
			operationDefault = evaluation
			haveOperationDefault = true
		}
		if evaluation.Rule != nil {
			if candidate.isOperation && operationSubject.Kind == "local_location" && evaluation.Decision == permissions.DecisionDeny {
				return meta, candidate.subject, evaluation, nil
			}
			if !haveMatchedRule {
				matchedSubject = candidate.subject
				matchedEvaluation = evaluation
				haveMatchedRule = true
			}
		}
	}
	if operationSubject.Kind == "local_location" && haveMatchedRule && matchedEvaluation.Decision == permissions.DecisionDeny {
		return meta, matchedSubject, matchedEvaluation, nil
	}
	if operationSubject.Kind == "local_location" &&
		meta.Category == permissions.CategoryExecute &&
		strings.TrimSpace(meta.Operation) == tools.LocalSearchCommandToolName &&
		haveMatchedRule && matchedEvaluation.Decision == permissions.DecisionAsk {
		return meta, matchedSubject, matchedEvaluation, nil
	}
	if operationSubject.Kind == "local_location" && haveOperationDefault {
		if operationDefault.Decision == permissions.DecisionAllow {
			return meta, operationSubject, operationDefault, nil
		}
		if operationDefault.Decision == permissions.DecisionAsk &&
			m.localComputerAllowsSubject(meta, operationSubject) {
			return meta, operationSubject, permissions.EvaluationResult{
				Decision: permissions.DecisionAllow,
				Reason:   "allowed by Local Computer connection",
			}, nil
		}
		return meta, operationSubject, operationDefault, nil
	}
	if haveMatchedRule {
		return meta, matchedSubject, matchedEvaluation, nil
	}
	if haveOperationDefault {
		return meta, operationSubject, operationDefault, nil
	}
	evaluation, err := m.permissions.Evaluate(ctx, permissions.EvaluationInput{
		ThreadID: threadID,
		Tool:     toolName,
		Category: meta.Category,
		Subject:  operationSubject,
	})
	if err != nil {
		return managerPermissionMeta{}, store.PermissionSubject{}, permissions.EvaluationResult{}, err
	}
	return meta, operationSubject, evaluation, nil
}

func (m *Manager) execTool(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	projectID *string,
	call types.ToolCall,
	searchMode bool,
	searchDepth string,
	queryRowLimit int,
	contextPlan *RunContextPlan,
	enabledDatasources map[string]bool,
	enabledChartEngines chartEngineEnablement,
	enabledSharePoint bool,
	enabledArxiv bool,
	enabledThreadSearch bool,
	enabledLocalComputer bool,
	enabledImageGeneration bool,
	enabledAtlassian atlassianEnablement,
	enabledBrowserControl bool,
	enabledGitHub bool,
	enabledSlack slackToolAccess,
) (types.ChatMessage, error) {
	toolName := strings.TrimSpace(call.Name)
	activeProjectID := ""
	if projectID != nil {
		activeProjectID = strings.TrimSpace(*projectID)
	}
	execPlan := RunContextPlan{}
	if contextPlan != nil {
		execPlan = *contextPlan
	}
	if searchMode && !m.tools.AllowedForSearch(toolName, searchDepth) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is not allowed in search mode: "+toolName), execPlan), nil
	}
	if !isThreadSearchToolAllowedForRun(toolName, enabledThreadSearch) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	if !isLocalComputerToolAllowedForRun(toolName, enabledLocalComputer) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	if !isImageGenerationToolAllowedForRun(toolName, enabledImageGeneration) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	if !isChartToolAllowedForRun(toolName, enabledChartEngines) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	enabledByDatasource := cloneDatasourceEnabledMap(enabledDatasources)
	if enabledByDatasource == nil {
		enabledByDatasource = map[string]bool{}
	}
	for _, datasource := range tools.DatasourceNames() {
		name := strings.TrimSpace(strings.ToLower(datasource))
		if name == "" {
			continue
		}
		if _, ok := enabledByDatasource[name]; !ok {
			enabledByDatasource[name] = false
		}
	}
	if !isDatasourceToolAllowedByMap(toolName, enabledByDatasource) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	// enabledAtlassian/enabledBrowserControl/enabledGitHub/enabledSlack are the
	// run-scoped values computed at run start (global settings narrowed by the
	// run's connection scope), passed in like every other connector. They must
	// NOT be re-derived from m.settings.Get() here: a live re-read would let a
	// stray or replayed call execute against a connection the run's scope
	// excludes, and would leak mid-run settings edits into an in-flight run.
	if !isAtlassianToolAllowedForRun(toolName, enabledAtlassian) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	if !isBrowserControlToolAllowedForRun(toolName, enabledBrowserControl) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	if !isSharePointToolAllowedForRun(toolName, enabledSharePoint) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	if !isGitHubToolAllowedForRun(toolName, enabledGitHub) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	if !isSlackToolAllowedForRun(toolName, enabledSlack) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	// enabledArxiv is the run-scoped value computed near executeRun (global
	// settings narrowed by the run's ActiveConnectionIDs). Re-deriving it from
	// global settings here would let a stray arXiv tool call execute on a run
	// whose connection scope excludes arXiv.
	if !isArxivToolAllowedForRun(toolName, enabledArxiv) {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("tool is disabled for this run"), execPlan), nil
	}
	tool, ok := m.tools.Get(toolName)
	if !ok {
		return m.persistToolResult(ctx, thread, run, call, tools.JSONError("unknown tool: "+toolName), execPlan), nil
	}

	var args json.RawMessage
	if strings.TrimSpace(call.ArgsText) != "" {
		args = json.RawMessage(call.ArgsText)
	} else {
		b, _ := json.Marshal(call.Args)
		args = json.RawMessage(b)
	}

	progressFn := func(progress map[string]any) {
		progressSafe := make(map[string]any, len(progress)+3)
		for key, value := range progress {
			progressSafe[key] = value
		}
		progressSafe["tool_call_id"] = strings.TrimSpace(call.ID)
		progressSafe["tool_name"] = toolName
		progressSafe["timestamp"] = time.Now().UnixMilli()
		b, err := json.Marshal(progressSafe)
		if err != nil {
			return
		}
		m.broadcast(run.ID, SSEEvent{Event: "tool_progress", Data: b})
	}

	execCtx := tools.ExecContext{
		ProjectID:             activeProjectID,
		Projects:              m.projects,
		BigQuery:              m.bigquery,
		Databricks:            m.databricks,
		Trino:                 m.trino,
		SnowflakeHTTP:         m.snowflake,
		Store:                 m.store,
		Usage:                 m.usage,
		LongHorizon:           m.longHorizon,
		Sources:               m.sources,
		ThreadID:              thread.ID,
		RunID:                 run.ID,
		ToolCallID:            strings.TrimSpace(call.ID),
		CfgDir:                m.cfgDir,
		SearchDepth:           searchDepth,
		EvidenceBudget:        execPlan.EvidenceBudget,
		DatabaseQueryRowLimit: queryRowLimit,
		ChartEngines:          enabledChartEngines.toToolFlags(),
		OnProgress:            progressFn,
		RequestScanApproval: func(approvalCtx context.Context, request jobs.ScanApprovalRequest) (jobs.ScanApprovalDecision, error) {
			return m.requestQueryScanApproval(approvalCtx, thread, run, call, toolName, request, progressFn)
		},
	}
	setExecContextQueryJobs(&execCtx, m.queryJobs)
	requestedCall := toolCallWithExecutedArgs(call, args)
	preApprovalCancellationGuard, preApprovalGuardedCancellation, preApprovalGuardErr := m.adoptGuardedQueryCancellation(
		context.Background(), thread, run, requestedCall,
	)
	if preApprovalGuardedCancellation && preApprovalGuardErr != nil {
		out := guardedQueryCancellationPreflightFailure(requestedCall, preApprovalGuardErr)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(requestedCall), preApprovalGuardErr)
		if m.longHorizon != nil {
			m.rearmQueryProtocolFaultRecovery(run.ID)
		}
		return m.persistToolResult(ctx, thread, run, requestedCall, out, execPlan), nil
	}

	approvalApplied, allowed, approvedArgs, approvalOut := m.requireToolApproval(ctx, thread, run, call, toolName, args, activeProjectID, progressFn)
	if !allowed {
		if preApprovalGuardedCancellation {
			preApprovalCancellationGuard.ApprovalDenied = true
			stampGuardedQueryCancellationOutput(&approvalOut, preApprovalCancellationGuard)
		}
		return m.persistToolResult(ctx, thread, run, requestedCall, approvalOut, execPlan), nil
	}
	args = approvedArgs
	if approvalApplied {
		execCtx.PermissionApproved = true
	}
	// From this point onward, lifecycle validation and persisted diagnostics
	// must use the exact bytes sent to the tool (including approval rewrites),
	// not a possibly divergent provider-side Args copy.
	call = toolCallWithExecutedArgs(call, args)

	expectedQueryObligationID := ""
	if tools.IsAsyncQueryRunTool(toolName) {
		// Validate retry identity from the exact JSON bytes that will be sent to
		// the tool. Provider adapters normally keep Args and ArgsText in sync,
		// but execution deliberately prefers ArgsText; trusting the other copy
		// would let a divergent representation bypass the durable-ledger check.
		request := queryRetryTargetFromCallArgs(call)
		if request.Obligation.Present || request.LegacyJob.Present || request.UnsupportedIdentityPresent {
			preflightOut := tools.JSONError("query retry target preflight failed")
			disposition := m.classifyExplicitQueryRetryTarget(context.Background(), run.ID, call, &preflightOut)
			switch disposition.State {
			case explicitQueryRetryTargetUnverified:
				m.setQueryDependencyFaultFor(
					run.ID,
					queryDependencyProtocolFaultKey(call),
					errors.New("query retry target was not a verified active obligation for this run"),
				)
				return m.persistToolResult(ctx, thread, run, call, preflightOut, execPlan), nil
			case explicitQueryRetryTargetVerified:
				expectedQueryObligationID = disposition.ObligationID
				var canonicalErr error
				call, args, canonicalErr = canonicalizeRunQueryRetryCall(call, disposition.ObligationID)
				if canonicalErr != nil {
					preflightOut = tools.JSONError("query retry target could not be canonicalized: " + canonicalErr.Error())
					m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), canonicalErr)
					return m.persistToolResult(ctx, thread, run, call, preflightOut, execPlan), nil
				}
				m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
				m.rearmQueryProtocolFaultRecovery(run.ID)
			case explicitQueryRetryTargetFresh:
				// A stale handle with no unfinished obligation in this run is not
				// authority to replace old work. Remove it and execute a fresh,
				// durably tracked query rather than trapping the model in a retry loop.
				var canonicalErr error
				call, args, canonicalErr = canonicalizeRunQueryRetryCall(call, "")
				if canonicalErr != nil {
					preflightOut = tools.JSONError("stale query retry target could not be removed: " + canonicalErr.Error())
					m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), canonicalErr)
					return m.persistToolResult(ctx, thread, run, call, preflightOut, execPlan), nil
				}
				m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
				m.rearmQueryProtocolFaultRecovery(run.ID)
			}
		}
	}

	cancellationGuard, guardedCancellation, cancellationGuardErr := m.preflightGuardedQueryCancellation(
		context.Background(), thread, run, call,
	)
	if guardedCancellation && cancellationGuardErr != nil {
		out := guardedQueryCancellationPreflightFailure(call, cancellationGuardErr)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), cancellationGuardErr)
		if m.longHorizon != nil {
			m.rearmQueryProtocolFaultRecovery(run.ID)
		}
		return m.persistToolResult(ctx, thread, run, call, out, execPlan), nil
	}
	if guardedCancellation {
		m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
		m.rearmQueryProtocolFaultRecovery(run.ID)
	}

	if toolName == "file_search" {
		if out, dispatched := m.dispatchConfiguredLocalFileSearch(ctx, thread, run, call, args, execCtx, progressFn); dispatched {
			return m.persistToolResult(ctx, thread, run, call, out, execPlan), nil
		}
	}

	out, err := tool.Execute(ctx, execCtx, args)
	if toolName == "rlm_exec" {
		m.updatePlanFromToolResult(&execPlan, out, toolName)
		if contextPlan != nil {
			*contextPlan = execPlan
		}
	}
	if err != nil {
		// Tool implementations should not return non-nil errors for user-facing failures,
		// but treat this defensively.
		out = tools.JSONError(err.Error())
	}
	if tools.IsAsyncQueryRunTool(toolName) {
		m.processAsyncQueryRunOutput(context.Background(), thread, run, call, toolName, expectedQueryObligationID, &out)
	} else if isAsyncQueryStatusTool(toolName) || isAsyncQueryResultsTool(toolName) || isAsyncQueryValidationTool(toolName) {
		m.processAsyncQueryObservationOutput(context.Background(), thread, run, call, &out)
	} else if guardedCancellation {
		stampGuardedQueryCancellationOutput(&out, cancellationGuard)
	}
	if attachedProjectID := m.attachThreadProjectFromToolResult(ctx, thread, run, toolName, activeProjectID, out); attachedProjectID != "" && projectID != nil {
		*projectID = attachedProjectID
	}
	return m.persistToolResult(ctx, thread, run, call, out, execPlan), nil
}

type guardedQueryCancellation struct {
	JobID          string
	OwnerToken     string
	Provider       string
	ThreadID       string
	RunID          string
	ObligationID   string
	Reason         jobs.CancelReason
	NeedsBarrier   bool
	NeedsAlternate bool
	ApprovalDenied bool
}

// preflightGuardedQueryCancellation closes the crash window between a model
// deciding that a query is wrong/superseded and dispatching provider
// cancellation. The current run first adopts the exact authenticated job (or
// recognizes its already-active successor) and marks the observed active
// attempt rejected. A failed ledger write prevents RequestCancel entirely.
func (m *Manager) preflightGuardedQueryCancellation(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
) (guardedQueryCancellation, bool, error) {
	guard, guarded, err := m.adoptGuardedQueryCancellation(ctx, thread, run, call)
	if !guarded || err != nil || !guard.NeedsBarrier {
		return guard, guarded, err
	}
	rejectedObservedAttempt, err := m.rejectObservedQueryAttempt(ctx, run.ID, guard.JobID, guard.ObligationID)
	if err != nil {
		return guard, true, fmt.Errorf("mark cancellation target replacement-required: %w", err)
	}
	guard.NeedsAlternate = rejectedObservedAttempt
	return guard, true, nil
}

// adoptGuardedQueryCancellation is intentionally safe to run before execute
// permission is resolved: it only authenticates durable local state and adds
// a wait barrier. Thus a denied/abandoned approval cannot let a newer run
// forget the older warehouse work. Rejection and provider cancellation remain
// after approval in preflightGuardedQueryCancellation.
func (m *Manager) adoptGuardedQueryCancellation(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
) (guardedQueryCancellation, bool, error) {
	reason, guarded := guardedQueryCancellationReason(call)
	if !guarded {
		return guardedQueryCancellation{}, false, nil
	}
	guard := guardedQueryCancellation{
		Reason: reason, ThreadID: strings.TrimSpace(thread.ID), RunID: strings.TrimSpace(run.ID),
	}
	jobID, ownerToken, identityOK := executedQueryLifecycleIdentity(call)
	if !identityOK {
		return guard, true, errors.New("guarded query cancellation requires an exact managed job identity and owner capability")
	}
	guard.JobID, guard.OwnerToken = jobID, ownerToken
	if m == nil || m.longHorizon == nil {
		return guard, true, errors.New("durable query cancellation ledger is unavailable")
	}
	record, found, err := m.longHorizon.GetQueryJob(ctx, jobID)
	if err != nil {
		return guard, true, fmt.Errorf("read cancellation target: %w", err)
	}
	if !found || strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(thread.ID) ||
		strings.TrimSpace(record.Owner.Handle) == "" || strings.TrimSpace(record.Owner.Handle) != ownerToken {
		return guard, true, errors.New("query cancellation target did not verify in the current thread scope")
	}
	guard.Provider = strings.TrimSpace(record.Provider)
	obligationID, needsBarrier, err := m.ensureObservedQueryDependency(ctx, thread.ID, run.ID, record)
	if err != nil {
		return guard, true, fmt.Errorf("adopt cancellation target: %w", err)
	}
	if !needsBarrier {
		// A completed+validated job (or an already validated same-lineage
		// successor) cannot leave unresolved warehouse work for this run.
		return guard, true, nil
	}
	if err := store.ValidateQueryObligationID(obligationID); err != nil {
		return guard, true, fmt.Errorf("adopted cancellation target has invalid obligation identity: %w", err)
	}
	guard.ObligationID = obligationID
	guard.NeedsBarrier = true
	return guard, true, nil
}

func guardedQueryCancellationReason(call types.ToolCall) (jobs.CancelReason, bool) {
	if strings.TrimSpace(call.Name) != "cancel_query" {
		return "", false
	}
	var raw []byte
	if strings.TrimSpace(call.ArgsText) != "" {
		raw = []byte(call.ArgsText)
	} else if call.Args != nil {
		raw, _ = json.Marshal(call.Args)
	}
	var in struct {
		Reason string `json:"reason"`
		DryRun bool   `json:"dry_run"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &in) != nil || in.DryRun {
		return "", false
	}
	reason := jobs.CancelReason(strings.TrimSpace(in.Reason))
	switch reason {
	case jobs.CancelReasonWrongQuery, jobs.CancelReasonSuperseded:
		return reason, true
	default:
		return "", false
	}
}

// rejectObservedQueryAttempt makes an active wrong/superseded attempt
// replacement-eligible without ever transferring that rejection to an active
// successor in the same obligation lineage.
func (m *Manager) rejectObservedQueryAttempt(ctx context.Context, runID, observedJobID, obligationID string) (bool, error) {
	dependencies, err := m.longHorizon.ListRunQueryDependencies(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, dependency := range dependencies {
		if !dependency.RequiredForCompletion || dependency.Detached || strings.TrimSpace(dependency.ObligationID) != obligationID {
			continue
		}
		if strings.TrimSpace(dependency.JobID) != strings.TrimSpace(observedJobID) {
			// The rejected observation was a detached predecessor. Its active
			// successor remains authoritative and retains its own validation.
			return false, nil
		}
		if strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationRejected) {
			return true, nil
		}
		candidate := dependency
		candidate.ValidationState = store.QueryValidationRejected
		candidate.UpdatedAt = time.Now().UTC()
		_, err = m.longHorizon.UpsertRunQueryDependency(ctx, candidate)
		return err == nil, err
	}
	return false, errors.New("adopted cancellation obligation has no active attempt")
}

func guardedQueryCancellationPreflightFailure(call types.ToolCall, cause error) tools.Output {
	identityMismatch := false
	if _, _, valid := executedQueryLifecycleIdentity(call); !valid {
		identityMismatch = true
	}
	result := map[string]any{
		"error":                               "The query cancellation was not sent because its replacement obligation could not be committed durably.",
		"query_cancellation_preflight_failed": true,
		"replacement_wait_required":           true,
		"alternative_plan_required":           true,
		"instructions":                        "Keep the task unfinished. Re-read the exact managed query status, then retry cancellation or run a corrected replacement using the active query_obligation_id. Do not abandon the query.",
	}
	if identityMismatch {
		result["query_observation_identity_mismatch"] = true
	}
	_ = cause // Never expose internal persistence/provider details to the model.
	return tools.Output{
		OK: false, ResultFormat: tools.ResultFormatJSON, Result: result,
		QueryMetadata: map[string]any{
			"query_cancellation_preflight_failed": true,
			"replacement_wait_required":           true,
			"query_observation_identity_mismatch": identityMismatch,
		},
	}
}

func stampGuardedQueryCancellationOutput(out *tools.Output, guard guardedQueryCancellation) {
	if out == nil || !guard.NeedsBarrier || store.ValidateQueryObligationID(guard.ObligationID) != nil {
		return
	}
	stampObservedQueryRunScope(out, guard.ThreadID, guard.RunID, guard.JobID, guard.ObligationID, guard.OwnerToken, guard.Provider)
	result := anyMap(out.Result)
	if result == nil {
		result = map[string]any{}
	}
	result["replacement_wait_required"] = true
	if guard.NeedsAlternate {
		result["replacement_required"] = true
		result["alternative_plan_required"] = true
		result["instructions"] = "Cancellation does not complete the task. Run and validate a corrected replacement for this query_obligation_id; if cancellation remains pending, continue monitoring it rather than abandoning the query."
	} else if guard.ApprovalDenied {
		result["instructions"] = "Cancellation was not approved and was not sent. Continue monitoring this active query obligation; do not treat the denied cancellation as completed work or abandon the task."
	} else {
		result["instructions"] = "An active attempt still represents this query obligation. Continue monitoring and validate that attempt; do not launch a duplicate or abandon the task merely because cancellation was denied, deferred, or targeted a superseded predecessor."
	}
	out.Result = result
	if out.QueryMetadata == nil {
		out.QueryMetadata = map[string]any{}
	}
	out.QueryMetadata["replacement_wait_required"] = true
	if guard.NeedsAlternate {
		out.QueryMetadata["replacement_required"] = true
		out.QueryMetadata["alternative_plan_required"] = true
	}
}

// processAsyncQueryObservationOutput adopts an explicitly observed active (or
// completed-but-unvalidated) same-thread job as a completion dependency of the
// current run. This is the handoff path after a newer user message supersedes
// and fences the older waiting continuation while the warehouse job continues.
func (m *Manager) processAsyncQueryObservationOutput(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	out *tools.Output,
) {
	if m == nil || m.longHorizon == nil || out == nil {
		return
	}
	requestedJobID, requestedOwnerToken, requestedOK := executedQueryLifecycleIdentity(call)
	if !requestedOK {
		normalizeQueryObservationIdentityFailure(out)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), errors.New("query observation did not include an exact managed job identity and owner capability"))
		return
	}
	jobID := requestedJobID
	if out.OK {
		returnedJobID, conflictingJobIDs := selectAsyncQueryJobID(*out)
		if conflictingJobIDs || returnedJobID != requestedJobID {
			normalizeQueryObservationIdentityFailure(out)
			m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), errors.New("query observation response did not match the exact executed job identity"))
			return
		}
		jobID = returnedJobID
	}
	record, found, recordErr := m.longHorizon.GetQueryJob(ctx, jobID)
	if recordErr != nil {
		markQueryObservationAdoptionPending(out, thread.ID, run.ID, jobID, recordErr)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), fmt.Errorf("read observed query job: %w", recordErr))
		m.rearmQueryProtocolFaultRecovery(run.ID)
		return
	}
	if !found || strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(thread.ID) ||
		strings.TrimSpace(record.Owner.Handle) == "" || strings.TrimSpace(record.Owner.Handle) != requestedOwnerToken {
		normalizeQueryObservationIdentityFailure(out)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), errors.New("query observation owner capability or durable job identity did not verify"))
		return
	}
	var attachErr error
	obligationID := ""
	needsBarrier := false
	for attempt := 0; attempt < 3; attempt++ {
		obligationID, needsBarrier, attachErr = m.ensureObservedQueryDependency(ctx, thread.ID, run.ID, record)
		if attachErr == nil {
			break
		}
	}
	if attachErr != nil {
		markQueryObservationAdoptionPending(out, thread.ID, run.ID, jobID, attachErr)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), attachErr)
		m.rearmQueryProtocolFaultRecovery(run.ID)
		log.Printf("run %s: failed to adopt observed query dependency %s: %v", run.ID, jobID, attachErr)
		return
	}
	if !needsBarrier {
		m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
		m.rearmQueryProtocolFaultRecovery(run.ID)
		return
	}
	if err := store.ValidateQueryObligationID(obligationID); err != nil {
		markQueryObservationAdoptionPending(out, thread.ID, run.ID, jobID, err)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), err)
		m.rearmQueryProtocolFaultRecovery(run.ID)
		return
	}
	// Preserve the exact observed status identity even when it is a detached
	// predecessor; its owner capability authenticated this call. The current
	// qob may be carried by a newer active successor in the same lineage.
	stampObservedQueryRunScope(out, thread.ID, run.ID, jobID, obligationID, record.Owner.Handle, record.Provider)
	m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
	m.rearmQueryProtocolFaultRecovery(run.ID)
}

func (m *Manager) ensureObservedQueryDependency(
	ctx context.Context,
	threadID, runID string,
	record jobs.JobRecord,
) (string, bool, error) {
	if m == nil || m.longHorizon == nil {
		return "", false, errors.New("long-horizon repository is not configured")
	}
	threadID = strings.TrimSpace(threadID)
	runID = strings.TrimSpace(runID)
	jobID := strings.TrimSpace(record.ID)
	if threadID == "" || runID == "" || !validManagedQueryJobID(jobID) || strings.TrimSpace(record.Owner.ThreadID) != threadID {
		return "", false, errors.New("observed query identity is not in the current thread scope")
	}
	dependencies, err := m.longHorizon.ListRunQueryDependencies(ctx, runID)
	if err != nil {
		return "", false, err
	}
	observedObligationID, observedObligationErr := store.DeriveQueryObligationID(ctx, m.longHorizon, runID, jobID)
	if observedObligationErr == nil && store.ValidateQueryObligationID(observedObligationID) == nil {
		for _, dependency := range dependencies {
			if !dependency.RequiredForCompletion || dependency.Detached {
				continue
			}
			candidateObligationID := strings.TrimSpace(dependency.ObligationID)
			if candidateObligationID == "" {
				candidateObligationID, err = store.DeriveQueryObligationID(ctx, m.longHorizon, runID, dependency.JobID)
				if err != nil {
					return "", false, err
				}
			}
			if candidateObligationID != observedObligationID {
				continue
			}
			if strings.TrimSpace(dependency.JobID) != jobID {
				if strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) {
					return "", false, nil
				}
				successorPath, observedIsSuccessor, relationErr := m.querySuccessorPath(ctx, dependency.JobID, record)
				if relationErr != nil {
					return "", false, relationErr
				}
				if observedIsSuccessor {
					advanced, advanceErr := m.advanceObservedQueryDependency(
						ctx, runID, observedObligationID, dependency, successorPath,
					)
					if advanceErr != nil {
						return "", false, advanceErr
					}
					if strings.EqualFold(strings.TrimSpace(advanced.ValidationState), store.QueryValidationValidated) {
						return "", false, nil
					}
					return observedObligationID, true, nil
				}
				activeRecord, activeFound, activeErr := m.longHorizon.GetQueryJob(ctx, strings.TrimSpace(dependency.JobID))
				if activeErr != nil {
					return "", false, activeErr
				}
				if !activeFound {
					return "", false, errors.New("active query obligation attempt is missing from durable registry")
				}
				_, activeIsSuccessor, reverseErr := m.querySuccessorPath(ctx, jobID, activeRecord)
				if reverseErr != nil {
					return "", false, reverseErr
				}
				if !activeIsSuccessor {
					return "", false, errors.New("same-obligation query jobs have ambiguous replacement direction")
				}
				// The observed job is a detached predecessor of the active
				// successor. Its late validation can never validate the successor;
				// honor only the successor dependency's own state.
				return observedObligationID, true, nil
			}
			recordValidated := record.Status == jobs.JobStatusCompleted && strings.EqualFold(string(record.ValidationState), store.QueryValidationValidated)
			if recordValidated && !strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) {
				candidate := dependency
				candidate.ValidationState = store.QueryValidationValidated
				candidate.UpdatedAt = time.Now().UTC()
				if _, repairErr := m.longHorizon.UpsertRunQueryDependency(ctx, candidate); repairErr != nil {
					return "", false, repairErr
				}
			}
			if strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) || recordValidated {
				return "", false, nil
			}
			return observedObligationID, true, nil
		}
	}
	for _, dependency := range dependencies {
		if strings.TrimSpace(dependency.JobID) != jobID || !dependency.RequiredForCompletion || dependency.Detached {
			continue
		}
		recordValidated := record.Status == jobs.JobStatusCompleted && strings.EqualFold(string(record.ValidationState), store.QueryValidationValidated)
		if recordValidated && !strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) {
			candidate := dependency
			candidate.ValidationState = store.QueryValidationValidated
			candidate.UpdatedAt = time.Now().UTC()
			if _, repairErr := m.longHorizon.UpsertRunQueryDependency(ctx, candidate); repairErr != nil {
				return "", false, repairErr
			}
		}
		if strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) || recordValidated {
			return "", false, nil
		}
		obligationID := strings.TrimSpace(dependency.ObligationID)
		if obligationID == "" {
			obligationID, err = store.DeriveQueryObligationID(ctx, m.longHorizon, runID, jobID)
		}
		if err != nil || store.ValidateQueryObligationID(obligationID) != nil {
			return "", false, firstNonNilError(err, errors.New("observed query dependency has invalid obligation identity"))
		}
		return obligationID, true, nil
	}
	if record.Status == jobs.JobStatusCompleted && strings.EqualFold(string(record.ValidationState), store.QueryValidationValidated) {
		return "", false, nil
	}

	observable := record.Status == jobs.JobStatusCompleted && record.ValidationState != jobs.ValidationValidated
	switch record.Status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted,
		jobs.JobStatusSubmissionUnknown, jobs.JobStatusRunning,
		jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		observable = true
	}
	if observable {
		if err := m.persistRunQueryWaitDependency(ctx, threadID, runID, jobID); err != nil {
			return "", false, err
		}
	} else {
		switch record.Status {
		case jobs.JobStatusFailed, jobs.JobStatusCancelled, jobs.JobStatusExpired, jobs.JobStatusOrphaned:
			obligationID, deriveErr := store.DeriveQueryObligationID(ctx, m.longHorizon, runID, jobID)
			if deriveErr != nil {
				return "", false, deriveErr
			}
			validationState := strings.TrimSpace(string(record.ValidationState))
			if validationState == "" || strings.EqualFold(validationState, store.QueryValidationUnvalidated) {
				validationState = store.QueryValidationRejected
			}
			now := time.Now().UTC()
			if _, upsertErr := m.longHorizon.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
				RunID: runID, JobID: jobID, RequiredForCompletion: true,
				ValidationState: validationState, ObligationID: obligationID,
				CreatedAt: now, UpdatedAt: now,
			}); upsertErr != nil {
				return "", false, upsertErr
			}
		default:
			return "", false, fmt.Errorf("observed query %s is not in an actionable durable state", jobID)
		}
	}
	dependency, found, err := m.attachedRequiredQueryDependency(ctx, runID, jobID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, errors.New("observed query dependency was not readable after persistence")
	}
	obligationID := strings.TrimSpace(dependency.ObligationID)
	if obligationID == "" {
		obligationID, err = store.DeriveQueryObligationID(ctx, m.longHorizon, runID, jobID)
	}
	if err != nil || store.ValidateQueryObligationID(obligationID) != nil {
		return "", false, firstNonNilError(err, errors.New("observed query dependency has invalid obligation identity"))
	}
	return obligationID, true, nil
}

// querySuccessorPath returns the exact durable replacement path from
// ancestorJobID's child through descendant. IDs and creation order are never
// used to infer direction; every edge must be an explicit ReplacesJobID link.
func (m *Manager) querySuccessorPath(
	ctx context.Context,
	ancestorJobID string,
	descendant jobs.JobRecord,
) ([]jobs.JobRecord, bool, error) {
	ancestorJobID = strings.TrimSpace(ancestorJobID)
	if m == nil || m.longHorizon == nil || ancestorJobID == "" || strings.TrimSpace(descendant.ID) == "" {
		return nil, false, nil
	}
	reversePath := []jobs.JobRecord{descendant}
	seen := map[string]bool{strings.TrimSpace(descendant.ID): true}
	parentID := strings.TrimSpace(descendant.ReplacesJobID)
	for hops := 0; parentID != "" && hops < 64; hops++ {
		if parentID == ancestorJobID {
			path := make([]jobs.JobRecord, len(reversePath))
			for index := range reversePath {
				path[index] = reversePath[len(reversePath)-1-index]
			}
			return path, true, nil
		}
		if seen[parentID] {
			return nil, false, errors.New("query replacement lineage contains a cycle")
		}
		seen[parentID] = true
		parent, found, err := m.longHorizon.GetQueryJob(ctx, parentID)
		if err != nil {
			return nil, false, err
		}
		if !found {
			return nil, false, fmt.Errorf("query replacement ancestor %s is missing", parentID)
		}
		reversePath = append(reversePath, parent)
		parentID = strings.TrimSpace(parent.ReplacesJobID)
	}
	if parentID != "" {
		return nil, false, errors.New("query replacement lineage exceeds 64 attempts")
	}
	return nil, false, nil
}

func observedQueryDependencyValidationState(record jobs.JobRecord) string {
	if record.Status == jobs.JobStatusCompleted && record.ValidationState == jobs.ValidationValidated {
		return store.QueryValidationValidated
	}
	if record.ValidationState == jobs.ValidationRejected ||
		(record.ProfileOutcome != jobs.ProfileOutcomeNone && record.ProfileOutcome != jobs.ProfileOutcomeAccepted) {
		return store.QueryValidationRejected
	}
	if record.ValidationState == jobs.ValidationValidating {
		return store.QueryValidationValidating
	}
	switch record.Status {
	case jobs.JobStatusFailed, jobs.JobStatusCancelled, jobs.JobStatusExpired, jobs.JobStatusOrphaned:
		return store.QueryValidationRejected
	default:
		return store.QueryValidationUnvalidated
	}
}

func (m *Manager) advanceObservedQueryDependency(
	ctx context.Context,
	runID, obligationID string,
	active store.RunQueryDependency,
	successorPath []jobs.JobRecord,
) (store.RunQueryDependency, error) {
	if len(successorPath) == 0 {
		return active, errors.New("query replacement path is empty")
	}
	current := active
	for index, successor := range successorPath {
		now := time.Now().UTC()
		validationState := observedQueryDependencyValidationState(successor)
		if index < len(successorPath)-1 {
			// Every intermediate node has an explicit durable successor, so it
			// is necessarily superseded for this observer run. Marking only the
			// intermediate dependency rejected makes the next atomic edge
			// replacement-eligible without transferring that verdict to the
			// final observed attempt.
			validationState = store.QueryValidationRejected
		}
		request := store.RunQueryDependency{
			RunID: runID, JobID: strings.TrimSpace(successor.ID), RequiredForCompletion: true,
			ValidationState: validationState, ObligationID: obligationID,
			CreatedAt: now, UpdatedAt: now,
		}
		replaced, err := m.longHorizon.ReplaceRunQueryDependency(ctx, runID, current.JobID, request)
		if err != nil {
			// A racing observer may already have completed this exact edge. Accept
			// only the durable active successor for the same qob; every other
			// conflict remains fail-closed.
			dependencies, listErr := m.longHorizon.ListRunQueryDependencies(ctx, runID)
			if listErr != nil {
				return store.RunQueryDependency{}, err
			}
			found := false
			for _, candidate := range dependencies {
				if candidate.RequiredForCompletion && !candidate.Detached &&
					strings.TrimSpace(candidate.JobID) == strings.TrimSpace(successor.ID) &&
					strings.TrimSpace(candidate.ObligationID) == obligationID {
					replaced = candidate
					found = true
					break
				}
			}
			if !found {
				return store.RunQueryDependency{}, err
			}
		}
		current = replaced
	}
	return current, nil
}

func executedQueryLifecycleIdentity(call types.ToolCall) (string, string, bool) {
	args := call.Args
	if strings.TrimSpace(call.ArgsText) != "" {
		var decoded map[string]any
		if json.Unmarshal([]byte(call.ArgsText), &decoded) != nil {
			return "", "", false
		}
		args = decoded
	}
	if args == nil {
		return "", "", false
	}
	raw, present := args["job_id"]
	if !present {
		return "", "", false
	}
	jobID, ok := raw.(string)
	jobID = strings.TrimSpace(jobID)
	ownerToken, ownerOK := args["owner_token"].(string)
	ownerToken = strings.TrimSpace(ownerToken)
	return jobID, ownerToken, ok && ownerOK && validManagedQueryJobID(jobID) && ownerToken != ""
}

func normalizeQueryObservationIdentityFailure(out *tools.Output) {
	if out == nil {
		return
	}
	const failure = "The query lifecycle response did not match the exact managed job requested by this tool call."
	const instructions = "Preserve the task and retry check_query_status or get_query_results with the exact app-managed job_id and owner_token. Do not infer a replacement identity or abandon the query."
	out.OK = false
	out.ResultFormat = tools.ResultFormatJSON
	out.Result = map[string]any{
		"error":                               failure,
		"message":                             failure,
		"instructions":                        instructions,
		"alternative_plan":                    instructions,
		"query_observation_identity_mismatch": true,
		"replacement_wait_required":           true,
		"retryable":                           true,
	}
	out.QueryMetadata = map[string]any{
		"query_observation_identity_mismatch": true,
		"replacement_wait_required":           true,
		"retryable":                           true,
	}
	out.ResultMetadata = nil
	out.Artifact = nil
}

func markQueryObservationAdoptionPending(out *tools.Output, threadID, runID, jobID string, cause error) {
	if out == nil {
		return
	}
	out.OK = false
	if out.ResultFormat == "" {
		out.ResultFormat = tools.ResultFormatJSON
	}
	result := anyMap(out.Result)
	if result == nil {
		result = map[string]any{}
	}
	result["job_id"] = jobID
	result["thread_id"] = threadID
	result["run_id"] = runID
	result["query_observation_adoption_pending"] = true
	result["replacement_wait_required"] = true
	result["retryable"] = true
	result["instructions"] = "The observed query is still unfinished, but its current-run completion barrier could not yet be persisted. Retry this exact lifecycle check; do not launch duplicate work or abandon the task."
	_ = cause // detailed store errors are logged server-side, never model-facing
	out.Result = result
	if out.QueryMetadata == nil {
		out.QueryMetadata = map[string]any{}
	}
	out.QueryMetadata["job_id"] = jobID
	out.QueryMetadata["thread_id"] = threadID
	out.QueryMetadata["run_id"] = runID
	out.QueryMetadata["query_observation_adoption_pending"] = true
	out.QueryMetadata["replacement_wait_required"] = true
	out.QueryMetadata["retryable"] = true
}

func (m *Manager) stampAttachedQueryDependencyScope(ctx context.Context, threadID, runID, jobID string, out *tools.Output) error {
	if m == nil || m.longHorizon == nil {
		return nil
	}
	dependency, found, err := m.attachedRequiredQueryDependency(ctx, runID, jobID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("query dependency was not readable after persistence")
	}
	obligationID := strings.TrimSpace(dependency.ObligationID)
	if obligationID == "" {
		obligationID, err = store.DeriveQueryObligationID(ctx, m.longHorizon, runID, jobID)
		if err != nil {
			return err
		}
	}
	if err := store.ValidateQueryObligationID(obligationID); err != nil {
		return fmt.Errorf("query dependency has an invalid obligation identity: %w", err)
	}
	record, found, err := m.longHorizon.GetQueryJob(ctx, jobID)
	if err != nil {
		return err
	}
	if !found || strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(threadID) || strings.TrimSpace(record.Owner.Handle) == "" {
		return errors.New("query dependency owner capability is not durable")
	}
	stampObservedQueryRunScope(out, threadID, runID, jobID, obligationID, record.Owner.Handle, record.Provider)
	return nil
}

func stampObservedQueryRunScope(out *tools.Output, threadID, runID, jobID, obligationID, ownerToken, provider string) {
	if out == nil {
		return
	}
	stamp := func(payload map[string]any) {
		if payload == nil {
			return
		}
		for _, key := range []string{"query_obligation_id", "queryObligationId", "queryObligationID", "run_id", "runId", "runID", "thread_id", "threadId", "threadID"} {
			delete(payload, key)
		}
		payload["job_id"] = jobID
		payload["thread_id"] = threadID
		payload["run_id"] = runID
		payload["query_obligation_id"] = obligationID
		payload["owner_token"] = strings.TrimSpace(ownerToken)
		if strings.TrimSpace(provider) != "" {
			payload["provider"] = strings.TrimSpace(provider)
		}
	}
	canonicalizeManagedQueryJobIdentity(out, jobID)
	if out.QueryMetadata == nil {
		out.QueryMetadata = map[string]any{}
	}
	stamp(out.QueryMetadata)
	if result := anyMap(out.Result); result != nil {
		stamp(result)
	}
}

func (m *Manager) processAsyncQueryRunOutput(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	toolName string,
	expectedObligationID string,
	out *tools.Output,
) {
	if out == nil {
		return
	}
	jobID, conflictingJobIDs := selectAsyncQueryJobID(*out)
	if conflictingJobIDs {
		normalizeMissingDurableQueryIdentityOutput(out)
		m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), errors.New("query tool returned conflicting durable job identities"))
		return
	}
	if out.OK && queryOutputCarriesMaterializedDataset(*out) && !validManagedQueryJobID(jobID) {
		// Legacy synchronous tools (notably BigQuery) may return completed rows
		// with a provider-native job_id. The dataset is already materialized, so
		// preserve success and relocate that diagnostic id out of the reserved
		// app-managed namespace instead of converting good rows into an error.
		relocateSynchronousProviderJobIdentity(out)
		return
	}
	if jobID != "" && !validManagedQueryJobID(jobID) {
		// A provider/native id or an all-zero sentinel is not an app-managed
		// durable identity. Never turn it into a permanent job:<bogus> fault.
		normalizeMissingDurableQueryIdentityOutput(out)
		jobID = ""
	}
	if jobID != "" {
		canonicalizeManagedQueryJobIdentity(out, jobID)
	}
	if jobID == "" {
		// Preserve the legacy synchronous compatibility path only when the
		// successful output already contains the materialized dataset. Every
		// other run-query output needs a durable identity or an alternative
		// recovery obligation; OK=true with nil/zero job_id is a contract
		// failure, not permission for the model to finish and abandon work.
		if !out.OK || !queryOutputCarriesMaterializedDataset(*out) {
			if out.OK {
				normalizeMissingDurableQueryIdentityOutput(out)
			}
			m.handleAsyncQueryMissingDurableIdentity(ctx, thread, run, call, toolName, out)
		}
		return
	}
	if queryOutputRequiresReplacementWait(*out) {
		// Preserve any existing fail-closed fault. The named dependency is
		// attached to this run when possible, including an older same-thread
		// live job, so the model waits instead of duplicating warehouse work.
		dependencyErr := m.persistRunQueryWaitDependency(ctx, thread.ID, run.ID, jobID)
		if dependencyErr == nil {
			if scopeErr := m.stampAttachedQueryDependencyScope(ctx, thread.ID, run.ID, jobID, out); scopeErr != nil {
				m.setQueryDependencyFaultFor(run.ID, "job:"+jobID, scopeErr)
				return
			}
			if !queryRunOutputMatchesExpectedObligation(out, expectedObligationID) {
				markQueryRunLineageMismatch(out)
				m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), errors.New("query output did not match the obligation authorized before execution"))
				return
			}
			m.setQueryDependencyFaultFor(run.ID, "job:"+jobID, nil)
			m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
			m.rearmQueryProtocolFaultRecovery(run.ID)
			return
		}
		represented, verifyErr := m.queryJobDurablyObservableForThread(ctx, thread.ID, jobID)
		if represented {
			m.setQueryDependencyFaultFor(run.ID, "job:"+jobID, dependencyErr)
			log.Printf("run %s: failed to persist replacement-wait dependency %s: %v", run.ID, jobID, dependencyErr)
			return
		}
		normalizeMissingDurableQueryIdentityOutput(out)
		m.handleAsyncQueryMissingDurableIdentity(ctx, thread, run, call, toolName, out)
		log.Printf("run %s: rejected replacement-wait identity %s without durable same-thread evidence: dependency=%v verification=%v", run.ID, jobID, dependencyErr, verifyErr)
		return
	}

	var dependencyErr error
	for attempt := 0; attempt < 3; attempt++ {
		dependencyErr = m.persistRunQueryDependency(ctx, run.ID, jobID, extractAsyncQueryReplacesJobID(*out))
		if dependencyErr == nil {
			break
		}
	}
	if dependencyErr == nil {
		if scopeErr := m.stampAttachedQueryDependencyScope(ctx, thread.ID, run.ID, jobID, out); scopeErr != nil {
			m.setQueryDependencyFaultFor(run.ID, "job:"+jobID, scopeErr)
			return
		}
		if !queryRunOutputMatchesExpectedObligation(out, expectedObligationID) {
			markQueryRunLineageMismatch(out)
			m.setQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call), errors.New("query output did not match the obligation authorized before execution"))
			return
		}
		m.setQueryDependencyFaultFor(run.ID, "job:"+jobID, nil)
		m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
		m.rearmQueryProtocolFaultRecovery(run.ID)
		return
	}
	if represented, verifyErr := m.queryJobDurablyOwnedByRun(ctx, run.ID, jobID); represented {
		// A real current-run job exists but its completion barrier could not be
		// committed. Keep an exact job fault: clearing this on unrelated
		// protocol success could let live warehouse work be abandoned.
		m.setQueryDependencyFaultFor(run.ID, "job:"+jobID, dependencyErr)
		log.Printf("run %s: failed to persist required query dependency %s: %v", run.ID, jobID, dependencyErr)
	} else {
		// The claimed id is nonexistent, foreign, or could not be verified.
		// Downgrade it to a healable protocol/attempt path rather than poisoning
		// the run forever with job:<unresolvable>.
		normalizeMissingDurableQueryIdentityOutput(out)
		m.handleAsyncQueryMissingDurableIdentity(ctx, thread, run, call, toolName, out)
		log.Printf("run %s: rejected query identity %s without durable current-run evidence: dependency=%v verification=%v", run.ID, jobID, dependencyErr, verifyErr)
	}
}

func queryRunOutputMatchesExpectedObligation(out *tools.Output, expected string) bool {
	expected = strings.TrimSpace(expected)
	if expected == "" || out == nil {
		return true
	}
	actual := firstStringMapValue(out.QueryMetadata, "query_obligation_id")
	if actual == "" {
		actual = firstStringMapValue(anyMap(out.Result), "query_obligation_id")
	}
	return actual == expected
}

func markQueryRunLineageMismatch(out *tools.Output) {
	if out == nil {
		return
	}
	out.OK = false
	result := anyMap(out.Result)
	if result == nil {
		result = map[string]any{}
	}
	result["error"] = "The durable query job returned by the tool belongs to a different obligation than the retry authorized before execution."
	result["query_obligation_lineage_mismatch"] = true
	result["replacement_wait_required"] = true
	result["instructions"] = "Keep all existing query obligations unfinished, inspect their current status, and retry only with the exact active query_obligation_id. Do not abandon either durable job."
	out.Result = result
	if out.QueryMetadata == nil {
		out.QueryMetadata = map[string]any{}
	}
	out.QueryMetadata["query_obligation_lineage_mismatch"] = true
	out.QueryMetadata["replacement_wait_required"] = true
}

func canonicalizeManagedQueryJobIdentity(out *tools.Output, jobID string) {
	if out == nil || !validManagedQueryJobID(jobID) {
		return
	}
	clean := func(payload map[string]any, stamp bool) {
		if payload == nil {
			return
		}
		for _, key := range []string{"query_job_id", "queryJobID", "job_id", "jobId", "async_job_id", "asyncJobID"} {
			delete(payload, key)
		}
		if stamp {
			payload["job_id"] = jobID
		}
	}
	if out.QueryMetadata == nil {
		out.QueryMetadata = map[string]any{}
	}
	clean(out.QueryMetadata, true)
	clean(out.ResultMetadata, false)
	if result := anyMap(out.Result); result != nil {
		clean(result, true)
		if nested := anyMap(result["job"]); nested != nil {
			for _, key := range []string{"job_id", "jobId", "id"} {
				delete(nested, key)
			}
			nested["job_id"] = jobID
		}
	}
}

func relocateSynchronousProviderJobIdentity(out *tools.Output) {
	if out == nil {
		return
	}
	relocate := func(payload map[string]any) {
		if payload == nil {
			return
		}
		for _, key := range []string{"query_job_id", "queryJobID", "job_id", "jobId", "async_job_id", "asyncJobID"} {
			value := strings.TrimSpace(fmt.Sprint(payload[key]))
			if value == "" || value == "<nil>" || validManagedQueryJobID(value) {
				continue
			}
			if strings.TrimSpace(firstStringMapValue(payload, "provider_job_id", "providerJobID")) == "" {
				payload["provider_job_id"] = value
			}
			delete(payload, key)
		}
	}
	relocate(out.QueryMetadata)
	relocate(out.ResultMetadata)
	if result := anyMap(out.Result); result != nil {
		relocate(result)
		if nested := anyMap(result["job"]); nested != nil {
			for _, key := range []string{"job_id", "jobId", "id"} {
				value := strings.TrimSpace(fmt.Sprint(nested[key]))
				if value == "" || value == "<nil>" || validManagedQueryJobID(value) {
					continue
				}
				if strings.TrimSpace(firstStringMapValue(result, "provider_job_id", "providerJobID")) == "" {
					result["provider_job_id"] = value
				}
				delete(nested, key)
			}
		}
	}
}

func (m *Manager) attachThreadProjectFromToolResult(ctx context.Context, thread store.Thread, run store.Run, toolName string, currentProjectID string, out tools.Output) string {
	if m == nil || m.store == nil || !out.OK {
		return ""
	}
	if strings.TrimSpace(currentProjectID) != "" || strings.TrimSpace(toolName) != tools.GitHubToolCloneRepository {
		return ""
	}
	if thread.ProjectID != nil && strings.TrimSpace(*thread.ProjectID) != "" {
		return ""
	}
	projectID := projectIDFromToolResult(out.Result)
	if projectID == "" {
		return ""
	}
	if err := m.store.UpdateThreadProject(ctx, thread.ID, projectID); err != nil {
		log.Printf("optionc: attach cloned project %s to thread %s: %v", projectID, thread.ID, err)
		return ""
	}
	payload := map[string]any{
		"thread_id":  thread.ID,
		"run_id":     run.ID,
		"project_id": projectID,
	}
	if b, err := json.Marshal(payload); err == nil {
		m.broadcast(run.ID, SSEEvent{Event: "thread_updated", Data: b})
	}
	return projectID
}

func projectIDFromToolResult(result any) string {
	if result == nil {
		return ""
	}
	if value, ok := result.(map[string]any); ok {
		if projectID := stringFromAny(value["project_id"]); projectID != "" {
			return projectID
		}
		if nested, ok := value["result"].(map[string]any); ok {
			return stringFromAny(nested["project_id"])
		}
	}
	return ""
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

type managerPermissionMeta struct {
	Category    string
	SubjectKind string
	Connector   string
	ToolSetID   string
	GrantID     string
	LocalRoot   string
	LocalKind   string
	Operation   string
	Repository  string
	RemoteKind  string
	RemoteID    string
	Excludes    []string
	Summary     string
}

type localComputerPermissionGrant struct {
	ID       string
	Root     string
	Kind     string
	Excludes []string
}

func isLocalComputerMachineTool(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page", "local_list_grant_folder", tools.LocalSearchCommandToolName, "local_search_in_grant", "local_read_grant_file":
		return true
	default:
		return false
	}
}

func (m *Manager) requireToolApproval(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	toolName string,
	args json.RawMessage,
	activeProjectID string,
	progressFn func(map[string]any),
) (bool, bool, json.RawMessage, tools.Output) {
	if m == nil || m.permissions == nil {
		return false, true, args, tools.Output{}
	}
	metas := m.permissionMetasForTool(ctx, thread.ID, toolName, args, activeProjectID)
	if len(metas) == 0 {
		return false, true, args, tools.Output{}
	}
	approvalApplied := false
	for _, permissionMeta := range metas {
		localComputerTool := isLocalComputerMachineTool(toolName)
		permissionMeta = m.resolveLocalComputerPathPermissionMeta(ctx, thread.ID, toolName, args, permissionMeta)
		if approvedArgs, ok, err := m.rootlessLocalComputerFileSearchArgs(ctx, thread.ID, toolName, args, permissionMeta); err != nil {
			return false, false, args, approvalToolResult("approval_error", "", permissionMeta.Category, toolName, err.Error(), nil)
		} else if ok {
			approvalApplied = true
			args = approvedArgs
			continue
		}
		if approvedArgs, ok, err := m.rootlessLocalComputerFileReadArgs(ctx, thread.ID, toolName, args, permissionMeta); err != nil {
			return false, false, args, approvalToolResult("approval_error", "", permissionMeta.Category, toolName, err.Error(), nil)
		} else if ok {
			approvalApplied = true
			args = approvedArgs
			continue
		}
		if localComputerTool {
			permissionMeta = m.resolveConfiguredLocalComputerPermissionMeta(toolName, args, permissionMeta)
		} else {
			permissionMeta = m.resolveSavedLocalLocationPermissionMeta(ctx, thread.ID, toolName, permissionMeta)
		}
		if permissionMeta.GrantID != "" && strings.TrimSpace(permissionMeta.LocalRoot) == "" {
			if localComputerTool {
				return false, false, args, localComputerScopeRequiredToolResult(toolName, args, permissionMeta, "The requested Local Computer grant is not configured.")
			}
			return false, false, args, approvalToolResult("approval_denied", "", permissionMeta.Category, toolName, "Local file grant is not available.", nil)
		}
		meta, subject, evaluation, err := m.evaluateSingleManagerToolPermission(ctx, thread.ID, toolName, permissionMeta)
		if err != nil {
			return false, false, args, approvalToolResult("approval_error", "", "", toolName, err.Error(), nil)
		}
		switch evaluation.Decision {
		case permissions.DecisionAllow:
			approvalApplied = true
			args = injectLocalApprovalArgs(args, toolName, subject)
			continue
		case permissions.DecisionDeny:
			if localComputerTool && strings.TrimSpace(strings.ToLower(meta.SubjectKind)) == "local_location" {
				return false, false, args, localComputerScopeRequiredToolResult(toolName, args, meta, "Local Computer access to this location is denied by the configured permissions.")
			}
			return false, false, args, approvalToolResult("approval_denied", "", meta.Category, toolName, "Permission denied by saved rule.", nil)
		}
		if localComputerTool && strings.TrimSpace(strings.ToLower(meta.SubjectKind)) == "local_location" {
			return false, false, args, localComputerScopeRequiredToolResult(toolName, args, meta, "The requested local file location is not covered by the configured Local Computer connection.")
		}

		summary := approvalPromptSummary(toolName, meta.Category)
		if strings.TrimSpace(meta.Summary) != "" {
			summary = strings.TrimSpace(meta.Summary)
		}
		preview := map[string]any{}
		if len(bytes.TrimSpace(args)) > 0 {
			var parsed any
			if err := json.Unmarshal(args, &parsed); err == nil {
				preview["args"] = parsed
			}
		}
		for key, value := range localLocationApprovalPreview(args, subject) {
			preview[key] = value
		}
		approval, reusedApproval, err := m.permissions.RequestOrReuse(ctx, permissions.RequestInput{
			ThreadID:   thread.ID,
			RunID:      run.ID,
			ToolCallID: strings.TrimSpace(call.ID),
			Tool:       toolName,
			Category:   meta.Category,
			Operation:  meta.Operation,
			Subject:    subject,
			Summary:    summary,
			Preview:    preview,
			Options:    approvalOptionsForPermissionSubject(meta.Category, subject),
		})
		if err != nil {
			return false, false, args, approvalToolResult("approval_error", "", meta.Category, toolName, err.Error(), nil)
		}
		if !reusedApproval {
			m.broadcastApproval(run.ID, approval)
		}
		if progressFn != nil {
			progressFn(map[string]any{
				"phase":       "approval_required",
				"approval_id": approval.ID,
				"message":     "Waiting for approval",
				"pct":         100,
			})
		}
		decided, err := m.permissions.Await(ctx, approval.ID, 0)
		if err != nil {
			return false, false, args, approvalToolResult("approval_error", approval.ID, meta.Category, toolName, err.Error(), &approval)
		}
		m.broadcastApproval(run.ID, decided)
		if decided.Status == "approved" {
			approvalApplied = true
			args = injectLocalApprovalArgs(args, toolName, approvalSubjectForExecution(decided))
			if progressFn != nil {
				progressFn(map[string]any{
					"phase":       "approval_approved",
					"approval_id": decided.ID,
					"message":     "Approval received",
					"pct":         100,
				})
			}
			continue
		}
		status := "approval_denied"
		if decided.Status == "expired" || decided.Status == "canceled" {
			status = "approval_pending"
		}
		return false, false, args, approvalToolResult(status, decided.ID, meta.Category, toolName, "Approval was not granted.", &decided)
	}
	return approvalApplied, true, args, tools.Output{}
}

// scanApprovalPreview builds the approval card's preview payload. A zero
// ApprovedScanBytes means no ceiling is configured: the key is omitted (the
// card renders no hard-ceiling row). AuthorizedScanBytes is the transparent,
// provider-side cap for this one query, including bounded estimate headroom.
func scanApprovalPreview(providerName string, request jobs.ScanApprovalRequest) map[string]any {
	authorizedBytes := request.AuthorizedScanBytes
	if authorizedBytes < request.EstimatedBytes {
		// Defensive compatibility for older/internal callers: never present an
		// execution cap below the estimate being approved.
		authorizedBytes = request.EstimatedBytes
	}
	headroomBytes := authorizedBytes - request.EstimatedBytes
	description := fmt.Sprintf(
		"%s estimates this query will scan %.2f GiB. The automatic limit is %.2f GiB. Approving authorizes up to %.2f GiB for this one query, including up to 5%% allowance for estimate changes before execution.",
		providerName,
		float64(request.EstimatedBytes)/(1<<30),
		float64(request.AutomaticScanBytes)/(1<<30),
		float64(authorizedBytes)/(1<<30),
	)
	if request.ApprovedScanBytes > 0 {
		description = fmt.Sprintf(
			"%s estimates this query will scan %.2f GiB. The automatic limit is %.2f GiB. Approving authorizes up to %.2f GiB for this one query, including up to 5%% estimate allowance and bounded by the configured %.2f GiB hard ceiling.",
			providerName,
			float64(request.EstimatedBytes)/(1<<30),
			float64(request.AutomaticScanBytes)/(1<<30),
			float64(authorizedBytes)/(1<<30),
			float64(request.ApprovedScanBytes)/(1<<30),
		)
	}
	preview := map[string]any{
		"job_id":                strings.TrimSpace(request.JobID),
		"estimated_bytes":       request.EstimatedBytes,
		"automatic_scan_bytes":  request.AutomaticScanBytes,
		"authorized_scan_bytes": authorizedBytes,
		"scan_headroom_bytes":   headroomBytes,
		"sql_preview":           strings.TrimSpace(request.SQLPreview),
		"project_id":            strings.TrimSpace(request.ConnectionID),
		"summary": fmt.Sprintf(
			"Estimated %.2f GiB scan exceeds the %.2f GiB automatic limit",
			float64(request.EstimatedBytes)/(1<<30),
			float64(request.AutomaticScanBytes)/(1<<30),
		),
		"description": description,
	}
	if request.ApprovedScanBytes > 0 {
		preview["approved_scan_bytes"] = request.ApprovedScanBytes
	}
	return preview
}

func (m *Manager) requestQueryScanApproval(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	toolName string,
	request jobs.ScanApprovalRequest,
	progressFn func(map[string]any),
) (jobs.ScanApprovalDecision, error) {
	if m == nil || m.permissions == nil {
		return jobs.ScanApprovalDecision{}, errors.New("query scan approval service is unavailable")
	}
	provider := strings.TrimSpace(strings.ToLower(request.Provider))
	if provider == "" {
		provider = "bigquery"
	}
	providerName := "BigQuery"
	if provider != "bigquery" {
		providerName = provider
	}
	preview := scanApprovalPreview(providerName, request)
	approval, reused, err := m.permissions.RequestOrReuse(ctx, permissions.RequestInput{
		ThreadID:   thread.ID,
		RunID:      run.ID,
		ToolCallID: strings.TrimSpace(call.ID),
		Tool:       strings.TrimSpace(toolName),
		Category:   permissions.CategoryExecute,
		Operation:  "query_scan_budget",
		Subject: store.PermissionSubject{
			Kind:      "connector",
			Tool:      strings.TrimSpace(toolName),
			Connector: provider,
			Operation: "query_scan_budget",
		},
		Summary: "Approve a BigQuery scan above the automatic limit",
		Preview: preview,
		Options: []string{"once"},
	})
	if err != nil {
		return jobs.ScanApprovalDecision{}, err
	}
	if !reused {
		m.broadcastApproval(run.ID, approval)
	}
	if progressFn != nil {
		progressFn(map[string]any{
			"phase":                 "approval_required",
			"approval_id":           approval.ID,
			"message":               "Waiting for scan budget approval",
			"estimated_bytes":       request.EstimatedBytes,
			"automatic_scan_bytes":  request.AutomaticScanBytes,
			"approved_scan_bytes":   request.ApprovedScanBytes,
			"authorized_scan_bytes": request.AuthorizedScanBytes,
			"pct":                   100,
		})
	}
	waitFor := time.Until(approval.ExpiresAt)
	if waitFor <= 0 {
		waitFor = time.Nanosecond
	}
	decided, err := m.permissions.Await(ctx, approval.ID, waitFor)
	if err != nil {
		return jobs.ScanApprovalDecision{ApprovalID: approval.ID}, err
	}
	m.broadcastApproval(run.ID, decided)
	approved := strings.EqualFold(strings.TrimSpace(decided.Status), "approved")
	if progressFn != nil {
		phase := "approval_denied"
		message := "Scan budget approval was not granted"
		if approved {
			phase = "approval_approved"
			message = "Scan budget approval received"
		}
		progressFn(map[string]any{
			"phase":       phase,
			"approval_id": decided.ID,
			"message":     message,
			"pct":         100,
		})
	}
	return jobs.ScanApprovalDecision{
		Approved:   approved,
		ApprovalID: decided.ID,
	}, nil
}

func (m *Manager) rootlessLocalComputerFileSearchArgs(ctx context.Context, threadID string, toolName string, args json.RawMessage, meta managerPermissionMeta) (json.RawMessage, bool, error) {
	if strings.TrimSpace(toolName) != "file_search" {
		return nil, false, nil
	}
	if strings.TrimSpace(strings.ToLower(meta.SubjectKind)) != "local_location" || strings.TrimSpace(meta.LocalRoot) != "" {
		return nil, false, nil
	}
	grants := m.configuredLocalComputerReadPermissionGrants()
	if len(grants) < 2 {
		return nil, false, nil
	}
	allowed := make([]localComputerPermissionGrant, 0, len(grants))
	for _, grant := range grants {
		grantMeta := meta
		grantMeta.LocalRoot = grant.Root
		grantMeta.LocalKind = grant.Kind
		grantMeta.Excludes = grant.Excludes
		_, _, evaluation, err := m.evaluateSingleManagerToolPermission(ctx, strings.TrimSpace(threadID), toolName, grantMeta)
		if err != nil {
			return nil, false, err
		}
		switch evaluation.Decision {
		case permissions.DecisionAllow:
			allowed = append(allowed, grant)
		case permissions.DecisionDeny:
			continue
		default:
			return nil, false, nil
		}
	}
	if len(allowed) == 0 {
		return nil, false, nil
	}
	if whole, ok := wholeComputerGrantFromLocalPermissionGrants(allowed); ok {
		return injectLocalApprovalArgs(args, toolName, store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: whole.Root,
			LocalKind: whole.Kind,
			Excludes:  whole.Excludes,
		}), true, nil
	}
	return injectLocalComputerSearchRootsArgs(args, allowed), true, nil
}

func (m *Manager) rootlessLocalComputerFileReadArgs(ctx context.Context, threadID string, toolName string, args json.RawMessage, meta managerPermissionMeta) (json.RawMessage, bool, error) {
	switch strings.TrimSpace(toolName) {
	case "local_read_file", "local_open_file", "local_read_pdf_page":
	default:
		return nil, false, nil
	}
	if strings.TrimSpace(strings.ToLower(meta.SubjectKind)) != "local_location" || strings.TrimSpace(meta.LocalRoot) != "" {
		return nil, false, nil
	}
	raw := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return nil, false, nil
		}
	}
	relPath, ok := cleanRootlessLocalComputerRelativePath(stringMapValue(raw, "path"))
	if !ok {
		return nil, false, nil
	}
	grants := m.configuredLocalComputerReadPermissionGrants()
	if len(grants) < 2 {
		return nil, false, nil
	}
	type match struct {
		grant localComputerPermissionGrant
		rel   string
	}
	matches := []match(nil)
	for _, grant := range grants {
		grantKind := strings.TrimSpace(strings.ToLower(grant.Kind))
		matchedRel := relPath
		if grantKind == "file" {
			if filepath.Base(grant.Root) != filepath.Base(relPath) {
				continue
			}
			if info, err := os.Stat(grant.Root); err != nil || info.IsDir() {
				continue
			}
			matchedRel = filepath.Base(grant.Root)
		} else {
			candidate := filepath.Join(grant.Root, relPath)
			if !localComputerGrantCoversPath(grant, candidate) {
				continue
			}
			if info, err := os.Stat(candidate); err != nil || info.IsDir() {
				continue
			}
		}
		grantMeta := meta
		grantMeta.LocalRoot = grant.Root
		grantMeta.LocalKind = grant.Kind
		grantMeta.Excludes = grant.Excludes
		_, _, evaluation, err := m.evaluateSingleManagerToolPermission(ctx, strings.TrimSpace(threadID), toolName, grantMeta)
		if err != nil {
			return nil, false, err
		}
		switch evaluation.Decision {
		case permissions.DecisionAllow:
			matches = append(matches, match{grant: grant, rel: matchedRel})
		case permissions.DecisionDeny:
			continue
		default:
			return nil, false, nil
		}
	}
	if len(matches) != 1 {
		return nil, false, nil
	}
	return injectLocalComputerGrantArgs(args, toolName, matches[0].grant, matches[0].rel), true, nil
}

func cleanRootlessLocalComputerRelativePath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || localComputerPathLooksAbsolute(raw) {
		return "", false
	}
	normalized := strings.ReplaceAll(raw, "\\", string(filepath.Separator))
	clean := filepath.Clean(normalized)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		return "", false
	}
	return clean, true
}

func (m *Manager) resolveLocalComputerPathPermissionMeta(ctx context.Context, threadID string, toolName string, args json.RawMessage, meta managerPermissionMeta) managerPermissionMeta {
	if strings.TrimSpace(strings.ToLower(meta.SubjectKind)) != "local_location" || strings.TrimSpace(meta.LocalRoot) != "" {
		return meta
	}
	switch strings.TrimSpace(toolName) {
	case "file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page":
	default:
		return meta
	}
	raw := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return meta
		}
	}
	path := stringMapValue(raw, "path")
	if !localComputerPathLooksAbsolute(path) {
		return meta
	}
	grant, ok := m.configuredLocalComputerGrantForPath(path)
	if !ok {
		return meta
	}
	meta.LocalRoot = grant.Root
	meta.LocalKind = grant.Kind
	meta.Excludes = grant.Excludes
	action := "read"
	if strings.TrimSpace(toolName) == "file_search" {
		action = "search"
	} else if strings.TrimSpace(toolName) == "local_list_folder" {
		action = "list"
	}
	meta.Summary = "Allow " + strings.ReplaceAll(strings.TrimSpace(toolName), "_", " ") + " to " + action + " " + grant.Root
	return meta
}

func (m *Manager) resolveConfiguredLocalComputerPermissionMeta(toolName string, args json.RawMessage, meta managerPermissionMeta) managerPermissionMeta {
	if !isLocalComputerMachineTool(toolName) {
		return meta
	}
	if strings.TrimSpace(strings.ToLower(meta.SubjectKind)) != "local_location" || strings.TrimSpace(meta.LocalRoot) != "" {
		return meta
	}
	grants := m.configuredLocalComputerReadPermissionGrants()
	if grantID := strings.TrimSpace(meta.GrantID); grantID != "" {
		for _, grant := range grants {
			if strings.TrimSpace(grant.ID) != grantID {
				continue
			}
			meta.LocalRoot = grant.Root
			meta.LocalKind = grant.Kind
			meta.Excludes = grant.Excludes
			return meta
		}
		return meta
	}
	if localComputerToolHasAbsolutePathArg(toolName, args) {
		return meta
	}
	for _, grant := range grants {
		if grant.Kind == "folder" && localPermissionRootIsFilesystemRoot(grant.Root) {
			meta.LocalRoot = grant.Root
			meta.LocalKind = grant.Kind
			meta.Excludes = grant.Excludes
			return meta
		}
	}
	if len(grants) == 1 {
		meta.LocalRoot = grants[0].Root
		meta.LocalKind = grants[0].Kind
		meta.Excludes = grants[0].Excludes
	}
	return meta
}

func localComputerToolHasAbsolutePathArg(toolName string, args json.RawMessage) bool {
	switch strings.TrimSpace(toolName) {
	case "file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page":
	default:
		return false
	}
	raw := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return false
		}
	}
	return localComputerPathLooksAbsolute(stringMapValue(raw, "path"))
}

func approvalSubjectForExecution(approval store.ApprovalRequest) store.PermissionSubject {
	if approval.Decision != nil && approval.Decision.Subject != nil {
		return *approval.Decision.Subject
	}
	return approval.Subject
}

func (m *Manager) resolveSavedLocalLocationPermissionMeta(ctx context.Context, threadID string, toolName string, meta managerPermissionMeta) managerPermissionMeta {
	if m == nil || m.permissions == nil {
		return meta
	}
	if strings.TrimSpace(strings.ToLower(meta.SubjectKind)) != "local_location" || strings.TrimSpace(meta.LocalRoot) != "" {
		return meta
	}
	rules, err := m.permissions.ListRules(ctx, permissions.RuleFilter{Category: permissions.CategoryRead})
	if err != nil {
		return meta
	}
	threadScope := "thread:" + strings.TrimSpace(threadID)
	if grantID := strings.TrimSpace(meta.GrantID); grantID != "" {
		for _, rule := range rules {
			if rule.ID != grantID {
				continue
			}
			if strings.TrimSpace(strings.ToLower(rule.Effect)) != permissions.EffectAllow {
				return meta
			}
			scope := strings.TrimSpace(rule.Scope)
			if scope != "global" && scope != threadScope {
				return meta
			}
			subject := rule.Subject
			if strings.TrimSpace(strings.ToLower(subject.Kind)) != "local_location" || strings.TrimSpace(subject.LocalRoot) == "" {
				return meta
			}
			if subject.Tool != "" && subject.Tool != toolName {
				return meta
			}
			meta.LocalRoot = filepath.Clean(strings.TrimSpace(subject.LocalRoot))
			meta.LocalKind = localKindForManagerSubject(subject)
			if len(subject.Excludes) > 0 {
				meta.Excludes = dedupeStringSlice(subject.Excludes)
			}
			return meta
		}
		for _, grant := range m.localReadPermissionGrantsForRun(ctx, threadID, toolName, m.settingsSnapshot()) {
			if strings.TrimSpace(grant.ID) != grantID {
				continue
			}
			meta.LocalRoot = grant.Root
			meta.LocalKind = grant.Kind
			if len(grant.Excludes) > 0 {
				meta.Excludes = grant.Excludes
			}
			return meta
		}
		return meta
	}
	grants := m.localReadPermissionGrantsForRun(ctx, threadID, toolName, m.settingsSnapshot())
	for _, grant := range grants {
		if grant.Kind == "folder" && localPermissionRootIsFilesystemRoot(grant.Root) {
			meta.LocalRoot = grant.Root
			meta.LocalKind = grant.Kind
			if len(grant.Excludes) > 0 {
				meta.Excludes = grant.Excludes
			}
			return meta
		}
	}
	if len(grants) != 1 {
		return meta
	}
	for _, grant := range grants {
		meta.LocalRoot = grant.Root
		meta.LocalKind = grant.Kind
		if len(grant.Excludes) > 0 {
			meta.Excludes = grant.Excludes
		}
		return meta
	}
	return meta
}

func localPermissionRootIsFilesystemRoot(root string) bool {
	clean := filepath.Clean(strings.TrimSpace(root))
	return clean != "" && clean != "." && filepath.Dir(clean) == clean
}

func injectLocalApprovalArgs(args json.RawMessage, toolName string, subject store.PermissionSubject) json.RawMessage {
	if strings.TrimSpace(strings.ToLower(subject.Kind)) != "local_location" || strings.TrimSpace(subject.LocalRoot) == "" {
		return args
	}
	switch strings.TrimSpace(toolName) {
	case "file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page", "local_list_grant_folder", tools.LocalSearchCommandToolName, "local_search_in_grant", "local_read_grant_file":
	default:
		return args
	}
	next := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &next); err != nil {
			return args
		}
	}
	next["local_root"] = subject.LocalRoot
	if localKind := localKindForManagerSubject(subject); localKind != "" {
		next["local_kind"] = localKind
	}
	rewriteLocalPathArgForApproval(next, toolName, subject)
	b, err := json.Marshal(next)
	if err != nil {
		return args
	}
	return b
}

func injectLocalComputerGrantArgs(args json.RawMessage, toolName string, grant localComputerPermissionGrant, relPath string) json.RawMessage {
	subject := store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: grant.Root,
		LocalKind: grant.Kind,
		Excludes:  grant.Excludes,
	}
	next := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &next); err != nil {
			return injectLocalApprovalArgs(args, toolName, subject)
		}
	}
	next["local_root"] = grant.Root
	if kind := strings.TrimSpace(grant.Kind); kind != "" {
		next["local_kind"] = kind
	} else {
		next["local_kind"] = "folder"
	}
	if grantID := strings.TrimSpace(grant.ID); grantID != "" {
		next["grant_id"] = grantID
	}
	if relPath = strings.TrimSpace(relPath); relPath != "" {
		next["path"] = filepath.ToSlash(filepath.Clean(relPath))
	}
	b, err := json.Marshal(next)
	if err != nil {
		return injectLocalApprovalArgs(args, toolName, subject)
	}
	return b
}

func rewriteLocalPathArgForApproval(args map[string]any, toolName string, subject store.PermissionSubject) {
	if args == nil || strings.TrimSpace(subject.LocalRoot) == "" {
		return
	}
	switch strings.TrimSpace(toolName) {
	case "file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page":
	default:
		return
	}
	rawPath := stringMapValue(args, "path")
	if !localComputerPathLooksAbsolute(rawPath) {
		return
	}
	rel, ok := localPathRelativeToPermissionSubject(rawPath, subject)
	if !ok {
		return
	}
	if rel == "" && localKindForManagerSubject(subject) == "file" {
		switch strings.TrimSpace(toolName) {
		case "local_read_file", "local_open_file", "local_read_pdf_page":
			rel = filepath.Base(subject.LocalRoot)
		}
	}
	args["path"] = rel
}

func localPathRelativeToPermissionSubject(rawPath string, subject store.PermissionSubject) (string, bool) {
	root := cleanLocalMachinePermissionPath(subject.LocalRoot)
	target := cleanLocalMachinePermissionPath(rawPath)
	if root == "" || target == "" {
		return "", false
	}
	if target != root && !localPermissionRootContains(root, target) {
		return "", false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." {
		rel = ""
	}
	if rel != "" && store.LocalPathExcludedByPermissionPatterns(rel, subject.Excludes) {
		return "", false
	}
	return rel, true
}

func injectLocalComputerSearchRootsArgs(args json.RawMessage, grants []localComputerPermissionGrant) json.RawMessage {
	next := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &next); err != nil {
			return args
		}
	}
	roots := make([]map[string]any, 0, len(grants))
	for _, grant := range grants {
		root := strings.TrimSpace(grant.Root)
		if root == "" {
			continue
		}
		kind := strings.TrimSpace(grant.Kind)
		if kind == "" {
			kind = "folder"
		}
		entry := map[string]any{
			"local_root": root,
			"local_kind": kind,
		}
		if id := strings.TrimSpace(grant.ID); id != "" {
			entry["grant_id"] = id
		}
		roots = append(roots, entry)
	}
	if len(roots) == 0 {
		return args
	}
	delete(next, "local_root")
	delete(next, "localRoot")
	next["local_roots"] = roots
	b, err := json.Marshal(next)
	if err != nil {
		return args
	}
	return b
}

func localLocationApprovalPreview(args json.RawMessage, subject store.PermissionSubject) map[string]any {
	if strings.TrimSpace(strings.ToLower(subject.Kind)) != "local_location" {
		return nil
	}
	raw := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		_ = json.Unmarshal(args, &raw)
	}
	preview := map[string]any{}
	if query := stringMapValue(raw, "query"); query != "" {
		preview["query"] = query
	}
	if hint := stringMapValue(raw, "location_hint", "locationHint"); hint != "" {
		preview["location_hint"] = hint
	}
	if path := stringMapValue(raw, "path", "relative_path", "relativePath", "file_ref", "fileRef"); path != "" {
		preview["path"] = path
	}
	if len(preview) == 0 {
		return nil
	}
	return preview
}

func localComputerScopeRequiredMessage(toolName string) string {
	action := "use local files"
	switch strings.TrimSpace(toolName) {
	case "file_search", tools.LocalSearchCommandToolName, "local_search_in_grant":
		action = "search local files"
	case "local_list_folder", "local_list_grant_folder":
		action = "list local folders"
	case "local_read_file", "local_open_file", "local_read_pdf_page", "local_read_grant_file":
		action = "read local files"
	}
	return "Local Computer access is configured only in Settings > Connections > Local Computer. Ask the user to add the needed folder there or enable whole-computer read-only access, then retry " + action + "."
}

func localComputerScopeRequiredToolResult(toolName string, args json.RawMessage, meta managerPermissionMeta, detail string) tools.Output {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = "The requested local file location is not covered by the configured Local Computer connection."
	}
	preview := map[string]any{}
	if len(bytes.TrimSpace(args)) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(args, &raw); err == nil {
			for _, key := range []string{"query", "location_hint", "path", "relative_path", "file_ref", "grant_id"} {
				if value, ok := raw[key]; ok {
					preview[key] = value
				}
			}
		}
	}
	return tools.Output{
		OK:           false,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"code":        "LOCAL_COMPUTER_SCOPE_REQUIRED",
			"status":      "local_computer_scope_required",
			"tool":        strings.TrimSpace(toolName),
			"category":    firstNonEmptyString(strings.TrimSpace(meta.Category), permissions.CategoryRead),
			"detail":      detail,
			"message":     localComputerScopeRequiredMessage(toolName),
			"remediation": "Open Settings > Connections > Local Computer and choose Whole computer or add the folder that contains the requested file.",
			"preview":     preview,
		},
		ResultMetadata: map[string]any{
			"kind":        "local_computer_scope_required",
			"widget":      "local_files",
			"integration": "local-computer",
			"readonly":    true,
		},
	}
}

func (m *Manager) broadcastApproval(runID string, approval store.ApprovalRequest) {
	payload := map[string]any{
		"approval": approvalEventPayload(approval),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	m.broadcast(runID, SSEEvent{Event: "approval", Data: b})
}

func approvalOptionsForPermissionSubject(category string, subject store.PermissionSubject) []string {
	if strings.TrimSpace(strings.ToLower(category)) == permissions.CategoryExecute && !executeApprovalSubjectIsScoped(subject) {
		return []string{"once", "thread"}
	}
	return []string{"once", "thread", "always"}
}

func executeApprovalSubjectIsScoped(subject store.PermissionSubject) bool {
	kind := strings.TrimSpace(strings.ToLower(subject.Kind))
	if kind == "repository" && strings.TrimSpace(subject.Repository) != "" {
		return true
	}
	if strings.TrimSpace(subject.RemoteKind) != "" && strings.TrimSpace(subject.RemoteID) != "" {
		return true
	}
	if strings.TrimSpace(subject.LocalRoot) != "" {
		return true
	}
	return false
}

func managerPermissionSubject(meta managerPermissionMeta, toolName string) store.PermissionSubject {
	subject := store.PermissionSubject{
		Kind:       meta.SubjectKind,
		Tool:       strings.TrimSpace(toolName),
		Connector:  meta.Connector,
		ToolSetID:  meta.ToolSetID,
		LocalRoot:  meta.LocalRoot,
		LocalKind:  localKindForManagerMeta(meta),
		Operation:  meta.Operation,
		Repository: meta.Repository,
		RemoteKind: meta.RemoteKind,
		RemoteID:   meta.RemoteID,
		Excludes:   dedupeStringSlice(meta.Excludes),
	}
	if subject.Kind == "" {
		subject.Kind = "tool"
	}
	if subject.Kind == "local_location" {
		subject.Operation = ""
	}
	return subject
}

func managerToolSetPermissionSubject(meta managerPermissionMeta) (store.PermissionSubject, bool) {
	toolSetID := strings.TrimSpace(strings.ToLower(meta.ToolSetID))
	if toolSetID == "" {
		return store.PermissionSubject{}, false
	}
	return store.PermissionSubject{
		Kind:      "tool_set",
		ToolSetID: toolSetID,
		Connector: strings.TrimSpace(strings.ToLower(meta.Connector)),
	}, true
}

func managerConnectorPermissionSubject(meta managerPermissionMeta) (store.PermissionSubject, bool) {
	connector := strings.TrimSpace(strings.ToLower(meta.Connector))
	if connector == "" {
		return store.PermissionSubject{}, false
	}
	return store.PermissionSubject{Kind: "connector", Connector: connector}, true
}

func samePermissionSubject(a store.PermissionSubject, b store.PermissionSubject) bool {
	return strings.TrimSpace(strings.ToLower(a.Kind)) == strings.TrimSpace(strings.ToLower(b.Kind)) &&
		strings.TrimSpace(a.Tool) == strings.TrimSpace(b.Tool) &&
		strings.TrimSpace(strings.ToLower(a.Connector)) == strings.TrimSpace(strings.ToLower(b.Connector)) &&
		strings.TrimSpace(strings.ToLower(a.ToolSetID)) == strings.TrimSpace(strings.ToLower(b.ToolSetID)) &&
		strings.TrimSpace(a.LocalRoot) == strings.TrimSpace(b.LocalRoot) &&
		localKindForManagerSubject(a) == localKindForManagerSubject(b) &&
		strings.TrimSpace(strings.ToLower(a.Repository)) == strings.TrimSpace(strings.ToLower(b.Repository)) &&
		strings.TrimSpace(strings.ToLower(a.RemoteKind)) == strings.TrimSpace(strings.ToLower(b.RemoteKind)) &&
		strings.TrimSpace(strings.ToLower(a.RemoteID)) == strings.TrimSpace(strings.ToLower(b.RemoteID)) &&
		strings.TrimSpace(strings.ToLower(a.Operation)) == strings.TrimSpace(strings.ToLower(b.Operation))
}

func localKindForManagerSubject(subject store.PermissionSubject) string {
	if strings.TrimSpace(strings.ToLower(subject.Kind)) != "local_location" {
		return ""
	}
	switch strings.TrimSpace(strings.ToLower(subject.LocalKind)) {
	case "file":
		return "file"
	default:
		return "folder"
	}
}

func localKindForManagerMeta(meta managerPermissionMeta) string {
	if strings.TrimSpace(strings.ToLower(meta.SubjectKind)) != "local_location" {
		return ""
	}
	switch strings.TrimSpace(strings.ToLower(meta.LocalKind)) {
	case "file":
		return "file"
	default:
		return "folder"
	}
}

func (m *Manager) localComputerAllowsSubject(meta managerPermissionMeta, subject store.PermissionSubject) bool {
	if m == nil {
		return false
	}
	category := strings.TrimSpace(strings.ToLower(meta.Category))
	if category != permissions.CategoryRead && !(category == permissions.CategoryExecute && strings.TrimSpace(meta.Operation) == tools.LocalSearchCommandToolName) {
		return false
	}
	if strings.TrimSpace(strings.ToLower(subject.Kind)) != "local_location" || strings.TrimSpace(subject.LocalRoot) == "" {
		return false
	}
	for _, grant := range m.localComputerReadPermissionGrants() {
		if localComputerGrantCoversSubject(grant, subject) {
			return true
		}
	}
	return false
}

func (m *Manager) localComputerReadPermissionGrants() []localComputerPermissionGrant {
	if m == nil || m.settings == nil {
		return nil
	}
	return localComputerReadPermissionGrantsFromSettings(m.settings.Get())
}

func (m *Manager) configuredLocalComputerReadPermissionGrants() []localComputerPermissionGrant {
	if m == nil {
		return nil
	}
	return compactLocalPermissionGrants(localComputerReadPermissionGrantsFromSettings(m.settingsSnapshot()))
}

func (m *Manager) configuredLocalComputerGrantForPath(rawPath string) (localComputerPermissionGrant, bool) {
	target := cleanLocalMachinePermissionPath(rawPath)
	if target == "" {
		return localComputerPermissionGrant{}, false
	}
	var best localComputerPermissionGrant
	bestLen := -1
	for _, grant := range m.configuredLocalComputerReadPermissionGrants() {
		if !localComputerGrantCoversPath(grant, target) {
			continue
		}
		rootLen := len(filepath.Clean(grant.Root))
		if rootLen > bestLen {
			best = grant
			bestLen = rootLen
		}
	}
	if bestLen < 0 {
		return localComputerPermissionGrant{}, false
	}
	return best, true
}

func (m *Manager) settingsSnapshot() config.AppSettings {
	if m == nil || m.settings == nil {
		return config.AppSettings{}
	}
	return m.settings.Get()
}

func (m *Manager) localReadPermissionGrantsForRun(ctx context.Context, threadID string, toolName string, settings config.AppSettings) []localComputerPermissionGrant {
	return compactLocalPermissionGrants(localComputerReadPermissionGrantsFromSettings(settings))
}

func compactLocalPermissionGrants(grants []localComputerPermissionGrant) []localComputerPermissionGrant {
	if len(grants) == 0 {
		return nil
	}
	out := make([]localComputerPermissionGrant, 0, len(grants))
	seen := map[string]int{}
	for _, grant := range grants {
		root := cleanLocalMachinePermissionPath(grant.Root)
		if root == "" {
			continue
		}
		kind := strings.TrimSpace(strings.ToLower(grant.Kind))
		if kind != "file" {
			kind = "folder"
		}
		root = filepath.Clean(root)
		key := root + "\x00" + kind
		normalized := localComputerPermissionGrant{
			ID:       strings.TrimSpace(grant.ID),
			Root:     root,
			Kind:     kind,
			Excludes: dedupeStringSlice(grant.Excludes),
		}
		if idx, ok := seen[key]; ok {
			if out[idx].ID == "" {
				out[idx].ID = normalized.ID
			}
			out[idx].Excludes = dedupeStringSlice(append(out[idx].Excludes, normalized.Excludes...))
			continue
		}
		seen[key] = len(out)
		out = append(out, normalized)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Root == out[j].Root {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Root < out[j].Root
	})
	return out
}

func localComputerReadPermissionGrantsFromSettings(settings config.AppSettings) []localComputerPermissionGrant {
	localComputer := settings.LocalComputer
	if !localComputerEnabledFromSettings(settings) {
		return nil
	}
	mode := config.LocalComputerAccessModeWholeComputer
	if localComputer.AccessMode != nil {
		mode = *localComputer.AccessMode
	}
	roots := []string{}
	if mode == config.LocalComputerAccessModeSelectedFolders {
		roots = append(roots, localComputer.SelectedFolders...)
	} else {
		roots = append(roots, localroots.WholeComputerRoots()...)
	}
	out := make([]localComputerPermissionGrant, 0, len(roots))
	seen := map[string]struct{}{}
	for _, raw := range roots {
		root := cleanLocalMachinePermissionPath(raw)
		if root == "" {
			continue
		}
		key := filepath.Clean(root)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, localComputerPermissionGrant{
			ID:       localComputerPermissionGrantID(key),
			Root:     key,
			Kind:     "folder",
			Excludes: sensitiveLocalPermissionExcludes(),
		})
	}
	return out
}

func localComputerEnabledFromSettings(settings config.AppSettings) bool {
	localComputer := settings.LocalComputer
	return localComputer != nil && localComputer.Enabled != nil && *localComputer.Enabled
}

func (m *Manager) localPermissionGrantForPath(ctx context.Context, threadID string, toolName string, rawPath string) (localComputerPermissionGrant, bool) {
	target := cleanLocalMachinePermissionPath(rawPath)
	if target == "" {
		return localComputerPermissionGrant{}, false
	}
	var best localComputerPermissionGrant
	bestLen := -1
	for _, grant := range m.localReadPermissionGrantsForRun(ctx, threadID, toolName, m.settingsSnapshot()) {
		if !localComputerGrantCoversPath(grant, target) {
			continue
		}
		rootLen := len(filepath.Clean(grant.Root))
		if rootLen > bestLen {
			best = grant
			bestLen = rootLen
		}
	}
	if bestLen < 0 {
		return localComputerPermissionGrant{}, false
	}
	return best, true
}

func localComputerWholeComputerRoot() string {
	root := localroots.PrimaryRoot()
	if clean := cleanLocalMachinePermissionPath(root); clean != "" {
		return clean
	}
	return root
}

func localMachinePermissionPathIsFilesystemRoot(raw string) bool {
	clean := filepath.Clean(strings.TrimSpace(raw))
	return clean != "" && clean != "." && filepath.Dir(clean) == clean
}

func localComputerPermissionGrantID(root string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(strings.TrimSpace(root))))
	return "local_computer_" + fmt.Sprintf("%x", sum[:8])
}

func wholeComputerGrantFromLocalPermissionGrants(grants []localComputerPermissionGrant) (localComputerPermissionGrant, bool) {
	for _, candidate := range grants {
		if strings.TrimSpace(strings.ToLower(candidate.Kind)) != "folder" || !localMachinePermissionPathIsFilesystemRoot(candidate.Root) {
			continue
		}
		coversAll := true
		for _, grant := range grants {
			if !localComputerGrantCoversPath(candidate, grant.Root) {
				coversAll = false
				break
			}
		}
		if coversAll {
			return candidate, true
		}
	}
	return localComputerPermissionGrant{}, false
}

func localComputerGrantCoversSubject(grant localComputerPermissionGrant, subject store.PermissionSubject) bool {
	return localComputerGrantCoversPath(grant, subject.LocalRoot)
}

func localComputerGrantCoversPath(grant localComputerPermissionGrant, rawPath string) bool {
	grantRoot := cleanLocalMachinePermissionPath(grant.Root)
	target := cleanLocalMachinePermissionPath(rawPath)
	if grantRoot == "" || target == "" {
		return false
	}
	if target == grantRoot {
		return true
	}
	if strings.TrimSpace(strings.ToLower(grant.Kind)) == "file" {
		return false
	}
	if !localPermissionRootContains(grantRoot, target) {
		return false
	}
	rel, err := filepath.Rel(grantRoot, target)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == "" {
		return true
	}
	return !store.LocalPathExcludedByPermissionPatterns(rel, grant.Excludes)
}

func localComputerPathLooksAbsolute(path string) bool {
	value := strings.TrimSpace(path)
	if value == "" {
		return false
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		return true
	}
	if filepath.IsAbs(value) {
		return true
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if len(normalized) >= 3 && normalized[1] == ':' && normalized[2] == '/' {
		ch := normalized[0]
		return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')
	}
	return false
}

func embedLocalComputerAccessGuidance(systemPrompt string, grants []localComputerPermissionGrant) string {
	if len(grants) == 0 {
		return systemPrompt
	}
	base := strings.TrimSpace(systemPrompt)
	lines := []string{
		"Local file access:",
		"- Read-only local file search and read access is configured for the locations below. Do not ask the user for permission again for these locations.",
		"- Use file_search directly for local file lookup. If the user names a folder path under a configured location, use local_list_folder with that path.",
		"- When the user asks to summarize, inspect, view, open, render, preview, download, or otherwise work with a whole found/named local file, call local_open_file for that exact file first. That promotes the file into the same native file pipeline as uploads.",
		"- Use local_read_file only for bounded text snippets, exact excerpts, or paginated fallback reads after the file has been identified.",
		"- For scanned PDFs and exact PDF page questions, avoid whole-document extraction. Locate the PDF through configured local access, then call local_read_pdf_page with the user's 1-based page number. It renders just that page, OCRs it, and returns bounded text plus page_count and method metadata.",
		"- If the requested path is outside these locations, explain that the user must add the needed folder in Settings > Connections > Local Computer; do not request local file access from chat.",
	}
	if localComputerGrantsIncludeWholeComputer(grants) {
		lines = append(lines, "- Configured scope: whole computer ("+strings.Join(localroots.WholeComputerRoots(), ", ")+"), with default sensitive credential exclusions.")
	} else {
		lines = append(lines, "- Configured folders:")
		limit := minInt(len(grants), 12)
		for i := 0; i < limit; i++ {
			lines = append(lines, "  - "+grants[i].Root)
		}
		if remaining := len(grants) - limit; remaining > 0 {
			lines = append(lines, fmt.Sprintf("  - ...and %d more configured folders.", remaining))
		}
	}
	if base == "" {
		return strings.Join(lines, "\n")
	}
	return strings.Join(append([]string{base, ""}, lines...), "\n")
}

func localComputerGrantsIncludeWholeComputer(grants []localComputerPermissionGrant) bool {
	roots := localroots.WholeComputerRoots()
	if len(roots) == 0 {
		return false
	}
	for _, root := range roots {
		covered := false
		for _, grant := range grants {
			if strings.TrimSpace(strings.ToLower(grant.Kind)) == "folder" && localComputerGrantCoversPath(grant, root) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func (m *Manager) localPermissionProjectRoot(projectID string) string {
	if m == nil || m.projects == nil {
		return ""
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return ""
	}
	root, err := m.projects.FilesDir(pid)
	if err != nil {
		return ""
	}
	return filepath.Clean(root)
}

func localReadPermissionMetaForManagerTool(toolName string, args json.RawMessage, localProjectRoot string) (managerPermissionMeta, bool) {
	projectRoot := strings.TrimSpace(localProjectRoot)
	if projectRoot == "" {
		return managerPermissionMeta{}, false
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return managerPermissionMeta{}, false
		}
	}
	root := ""
	name := strings.TrimSpace(toolName)
	switch name {
	case "list_files":
		root = localPermissionRoot(projectRoot, stringMapValue(raw, "path"))
	case "read_file":
		root = localPermissionRoot(projectRoot, stringMapValue(raw, "path"))
	case "search_files", "list_git_repositories":
		root = localPermissionRoot(projectRoot)
	case "search_threads":
		if includeFiles, ok := boolMapValue(raw, "include_files"); ok && !includeFiles {
			return managerPermissionMeta{}, false
		}
		root = localPermissionRoot(projectRoot)
	case "git_list_refs", "git_search_history", "git_show_commit":
		root = localPermissionRoot(projectRoot, stringMapValue(raw, "repo_path"))
	case "git_log", "git_grep":
		root = localPermissionRoot(projectRoot, stringMapValue(raw, "repo_path"), stringMapValue(raw, "path"))
	case "git_show_file_at_ref":
		root = localPermissionRoot(projectRoot, stringMapValue(raw, "repo_path"), stringMapValue(raw, "path"))
	case "describe_dataset", "sample_rows", "filter_csv", "aggregate_csv":
		root = localPermissionRoot(projectRoot, stringMapValue(raw, "path"))
	case "display_table", "build_chart_artifact":
		sourcePath := localPermissionSourcePath(raw)
		if sourcePath == "" {
			return managerPermissionMeta{}, false
		}
		root = localPermissionRoot(projectRoot, sourcePath)
	default:
		return managerPermissionMeta{}, false
	}
	if root == "" {
		return managerPermissionMeta{}, false
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "local_location",
		LocalRoot:   root,
		Operation:   name,
		Excludes:    sensitiveLocalPermissionExcludes(),
		Summary:     localReadPermissionSummary(name, root),
	}, true
}

func localMachineReadPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	name := strings.TrimSpace(toolName)
	if name != "file_search" && name != "local_list_folder" && name != "local_read_file" && name != "local_open_file" && name != "local_read_pdf_page" && name != "local_list_grant_folder" && name != tools.LocalSearchCommandToolName && name != "local_search_in_grant" && name != "local_read_grant_file" {
		return managerPermissionMeta{}, false
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return managerPermissionMeta{}, false
		}
	}
	if name == "local_list_grant_folder" || name == tools.LocalSearchCommandToolName || name == "local_search_in_grant" || name == "local_read_grant_file" {
		grantID := stringMapValue(raw, "grant_id", "grantId")
		if grantID == "" {
			return managerPermissionMeta{}, false
		}
		summary := "Use configured Local Computer grant " + grantID
		if name == "local_list_grant_folder" {
			if path := stringMapValue(raw, "relative_path", "relativePath", "path"); path != "" {
				summary = fmt.Sprintf("List %q in configured Local Computer grant %s", path, grantID)
			} else {
				summary = "List configured Local Computer grant " + grantID
			}
		} else if name == tools.LocalSearchCommandToolName || name == "local_search_in_grant" {
			if query := stringMapValue(raw, "query"); query != "" {
				summary = fmt.Sprintf("Search configured Local Computer grant %s for %q", grantID, query)
			}
		} else if fileRef := stringMapValue(raw, "file_ref", "fileRef"); fileRef != "" {
			summary = fmt.Sprintf("Read %q from configured Local Computer grant %s", fileRef, grantID)
		}
		category := permissions.CategoryRead
		if name == tools.LocalSearchCommandToolName {
			category = permissions.CategoryExecute
		}
		return managerPermissionMeta{
			Category:    category,
			SubjectKind: "local_location",
			GrantID:     grantID,
			Operation:   name,
			Excludes:    sensitiveLocalPermissionExcludes(),
			Summary:     summary,
		}, true
	}
	root := localMachinePermissionRoot(stringMapValue(raw, "local_root", "localRoot"), stringMapValue(raw, "path"))
	action := "read"
	if name == "file_search" {
		action = "search"
	} else if name == "local_list_folder" {
		action = "list"
	}
	if root == "" {
		summary := "Use configured Local Computer " + action
		if name == "file_search" {
			if query := stringMapValue(raw, "query"); query != "" {
				summary = fmt.Sprintf("Search configured Local Computer scope for %q", query)
			}
		} else if name == "local_list_folder" {
			summary = "List configured Local Computer folders"
			if path := stringMapValue(raw, "path"); path != "" {
				summary = fmt.Sprintf("List %q in configured Local Computer scope", path)
			}
		} else if path := stringMapValue(raw, "path"); path != "" {
			summary = fmt.Sprintf("Read %q from configured Local Computer scope", path)
		}
		return managerPermissionMeta{
			Category:    permissions.CategoryRead,
			SubjectKind: "local_location",
			LocalKind:   localMachineRootlessPermissionKind(name),
			Operation:   name,
			Excludes:    sensitiveLocalPermissionExcludes(),
			Summary:     summary,
		}, true
	}
	localKind := localMachinePermissionKind(root)
	switch name {
	case "local_read_file", "local_open_file", "local_read_pdf_page":
		localKind = "file"
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "local_location",
		LocalRoot:   root,
		LocalKind:   localKind,
		Operation:   name,
		Excludes:    sensitiveLocalPermissionExcludes(),
		Summary:     "Use " + strings.ReplaceAll(name, "_", " ") + " to " + action + " " + root,
	}, true
}

func localMachineRootlessPermissionKind(toolName string) string {
	switch strings.TrimSpace(toolName) {
	case "local_read_file", "local_open_file", "local_read_pdf_page":
		return "file"
	}
	return "folder"
}

func localPermissionSourcePath(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	source, ok := raw["source"].(map[string]any)
	if !ok {
		return ""
	}
	return stringMapValue(source, "path")
}

func localPermissionRoot(projectRoot string, relParts ...string) string {
	root := filepath.Clean(strings.TrimSpace(projectRoot))
	if root == "" || root == "." {
		return ""
	}
	out := root
	for _, raw := range relParts {
		rel := cleanLocalPermissionRel(raw)
		if rel == "" {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return ""
		}
		out = filepath.Clean(filepath.Join(out, rel))
	}
	if !localPermissionRootContains(root, out) {
		return ""
	}
	return out
}

func localMachinePermissionRoot(rawRoot string, relParts ...string) string {
	root := cleanLocalMachinePermissionPath(rawRoot)
	if root == "" {
		return ""
	}
	out := root
	for _, raw := range relParts {
		rel := cleanLocalPermissionRel(raw)
		if rel == "" {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return ""
		}
		out = filepath.Clean(filepath.Join(out, rel))
	}
	if !localPermissionRootContains(root, out) {
		return ""
	}
	if evaluated, err := filepath.EvalSymlinks(out); err == nil {
		out = filepath.Clean(evaluated)
		if !localPermissionRootContains(root, out) {
			return ""
		}
	}
	return out
}

func localMachinePermissionKind(root string) string {
	clean := cleanLocalMachinePermissionPath(root)
	if clean == "" {
		return ""
	}
	if info, err := os.Stat(clean); err == nil && info != nil && !info.IsDir() {
		return "file"
	}
	return "folder"
}

func cleanLocalMachinePermissionPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			if value == "~" {
				value = home
			} else {
				value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
			}
		}
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return ""
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		abs = evaluated
	}
	return filepath.Clean(abs)
}

func cleanLocalPermissionRel(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if localPermissionRelativePathIsAbsolute(value) {
		return ""
	}
	value = strings.TrimPrefix(value, "./")
	clean := filepath.Clean(filepath.FromSlash(value))
	if clean == "." {
		return ""
	}
	return clean
}

func localPermissionRelativePathIsAbsolute(value string) bool {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) {
		return true
	}
	if len(value) >= 2 && value[1] == ':' {
		letter := value[0]
		return (letter >= 'A' && letter <= 'Z') || (letter >= 'a' && letter <= 'z')
	}
	return false
}

func localPermissionRootContains(root string, candidate string) bool {
	root = filepath.Clean(root)
	candidate = filepath.Clean(candidate)
	if root == candidate {
		return true
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func localReadPermissionSummary(toolName string, localRoot string) string {
	label := strings.ReplaceAll(strings.TrimSpace(toolName), "_", " ")
	if label == "" {
		label = "local read"
	}
	return "Allow " + label + " to read " + localRoot
}

func (m *Manager) gitSourceReadPermissionMetaForManagerTool(ctx context.Context, toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	name := strings.TrimSpace(toolName)
	if name != "list_git_repositories" && !gitPermissionToolAllowsOmittedRepoPath(name) {
		return managerPermissionMeta{}, false
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return managerPermissionMeta{}, false
		}
	}
	selector := stringMapValue(raw, "repo_path", "repository", "source_id", "sourceId")
	src, ok := m.githubSourceForPermission(ctx, selector)
	if !ok {
		return managerPermissionMeta{}, false
	}
	worktree := filepath.Clean(strings.TrimSpace(src.Worktree))
	if worktree == "" || !filepath.IsAbs(worktree) {
		return managerPermissionMeta{}, false
	}
	root := worktree
	switch name {
	case "git_log", "git_grep", "git_show_file_at_ref":
		root = localPermissionRoot(worktree, stringMapValue(raw, "path"))
	}
	if root == "" {
		return managerPermissionMeta{}, false
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "local_location",
		LocalRoot:   root,
		Operation:   name,
		Excludes:    sensitiveLocalPermissionExcludes(),
		Summary:     localReadPermissionSummary(name, root),
	}, true
}

func sensitiveLocalPermissionExcludes() []string {
	return store.DefaultSensitiveLocalPermissionExcludes()
}

func permissionMetasForManagerTool(toolName string, args json.RawMessage, localProjectRoot string) []managerPermissionMeta {
	metas := []managerPermissionMeta{}
	if meta, ok := uploadArtifactSourceReadPermissionMetaForManagerTool(toolName, args); ok {
		metas = append(metas, meta)
	}
	if meta, ok := localUploadSourceReadPermissionMetaForManagerTool(toolName, args, localProjectRoot); ok {
		metas = append(metas, meta)
	}
	if meta, ok := permissionMetaForManagerTool(toolName, args, localProjectRoot); ok {
		metas = append(metas, meta)
	}
	return enrichManagerPermissionMetas(toolName, metas)
}

func (m *Manager) permissionMetasForTool(ctx context.Context, threadID string, toolName string, args json.RawMessage, activeProjectID string) []managerPermissionMeta {
	localProjectRoot := m.localPermissionProjectRoot(activeProjectID)
	permissionArgs, includeDirectMeta := m.resolveOmittedGitRepoPathForPermission(ctx, toolName, args, activeProjectID)
	permissionArgs = m.resolveOmittedGitHubRepositoryForPermission(ctx, toolName, permissionArgs, activeProjectID)
	metas := []managerPermissionMeta{}
	if includeDirectMeta {
		if meta, ok := m.gitSourceReadPermissionMetaForManagerTool(ctx, toolName, permissionArgs); ok {
			metas = append(metas, meta)
		} else if meta, ok := m.queryLifecyclePermissionMetaForManagerTool(threadID, toolName, permissionArgs); ok {
			metas = append(metas, meta)
		} else {
			metas = permissionMetasForManagerTool(toolName, permissionArgs, localProjectRoot)
		}
	}
	if meta, ok := m.localManagedUploadSourceReadPermissionMetaForManagerTool(toolName, args); ok {
		metas = append([]managerPermissionMeta{meta}, metas...)
	}
	if meta, ok := m.queryJobSourceReadPermissionMetaForManagerTool(threadID, toolName, args); ok {
		metas = append([]managerPermissionMeta{meta}, metas...)
		return enrichManagerPermissionMetas(toolName, m.filterImplicitChatUploadArtifactPermissionMetas(ctx, threadID, metas))
	}
	if meta, ok := m.localArtifactSourceReadPermissionMetaForManagerTool(ctx, toolName, args, localProjectRoot); ok {
		metas = append([]managerPermissionMeta{meta}, metas...)
		return enrichManagerPermissionMetas(toolName, m.filterImplicitChatUploadArtifactPermissionMetas(ctx, threadID, metas))
	}
	return enrichManagerPermissionMetas(toolName, m.filterImplicitChatUploadArtifactPermissionMetas(ctx, threadID, metas))
}

func (m *Manager) filterImplicitChatUploadArtifactPermissionMetas(ctx context.Context, threadID string, metas []managerPermissionMeta) []managerPermissionMeta {
	if len(metas) == 0 || m == nil || m.store == nil {
		return metas
	}
	out := make([]managerPermissionMeta, 0, len(metas))
	for _, meta := range metas {
		if m.isImplicitlyApprovedChatUploadArtifactMeta(ctx, threadID, meta) {
			continue
		}
		out = append(out, meta)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) isImplicitlyApprovedChatUploadArtifactMeta(ctx context.Context, threadID string, meta managerPermissionMeta) bool {
	if m == nil || m.store == nil || strings.TrimSpace(threadID) == "" {
		return false
	}
	if meta.Category != permissions.CategoryRead || meta.SubjectKind != "tool" {
		return false
	}
	remoteKind := strings.TrimSpace(strings.ToLower(meta.RemoteKind))
	if remoteKind != "artifact" && remoteKind != "attachment" {
		return false
	}
	artifactID := strings.TrimSpace(meta.RemoteID)
	if artifactID == "" {
		return false
	}
	artifact, err := m.store.GetArtifact(ctx, artifactID)
	if err != nil {
		return false
	}
	if !fileartifact.IsThreadFileArtifact(threadID, artifact) {
		return false
	}
	source := strings.TrimSpace(stringMapValue(artifact.Metadata, "source"))
	if source != "" {
		return strings.EqualFold(source, types.ChatUploadAttachmentSource)
	}
	return artifact.ToolName != nil && strings.EqualFold(strings.TrimSpace(*artifact.ToolName), types.ThreadFileAttachmentToolName)
}

func enrichManagerPermissionMetas(toolName string, metas []managerPermissionMeta) []managerPermissionMeta {
	if len(metas) == 0 {
		return nil
	}
	productMeta, ok := tools.ToolMetadataByName(toolName)
	if !ok {
		return metas
	}
	out := append([]managerPermissionMeta(nil), metas...)
	for i := range out {
		if strings.TrimSpace(out[i].ToolSetID) == "" {
			out[i].ToolSetID = productMeta.ToolSetID
		}
		if strings.TrimSpace(out[i].Connector) == "" {
			out[i].Connector = productMeta.ConnectorID
		}
	}
	return out
}

func (m *Manager) queryLifecyclePermissionMetaForManagerTool(threadID string, toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	if m == nil {
		return managerPermissionMeta{}, false
	}
	name := strings.TrimSpace(toolName)
	switch name {
	case "check_query_status", "get_query_results", "validate_query_result", "cancel_query":
	default:
		return managerPermissionMeta{}, false
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return managerPermissionMeta{}, false
		}
	}
	jobID := stringMapValue(raw, "job_id", "jobId")
	ownerToken := stringMapValue(raw, "owner_token", "ownerToken")
	connector := queryLifecycleConnectorForProvider(stringMapValue(raw, "provider", "connector", "integration"))
	if reg, ok := m.queryJobs.(*jobs.Registry); ok && reg != nil {
		owner := jobs.JobOwner{
			ThreadID: strings.TrimSpace(threadID),
			Handle:   strings.TrimSpace(ownerToken),
		}
		if snap, ok := reg.Snapshot(jobID, owner); ok {
			if snapConnector := queryLifecycleConnectorForProvider(snap.Provider); snapConnector != "" {
				connector = snapConnector
			}
			if strings.TrimSpace(snap.ID) != "" {
				jobID = strings.TrimSpace(snap.ID)
			}
		}
	}
	category := permissions.CategoryRead
	verb := "Read"
	switch name {
	case "check_query_status":
		verb = "Check"
	case "get_query_results":
		verb = "Read"
	case "validate_query_result":
		verb = "Validate"
	case "cancel_query":
		category = permissions.CategoryExecute
		verb = "Cancel"
	}
	summaryTarget := strings.TrimSpace(jobID)
	if summaryTarget == "" {
		summaryTarget = "query job"
	} else if connector != "" {
		summaryTarget = queryLifecycleProviderLabel(connector) + " query " + summaryTarget
	} else {
		summaryTarget = "query " + summaryTarget
	}
	return managerPermissionMeta{
		Category:    category,
		SubjectKind: "tool",
		Connector:   connector,
		Operation:   name,
		RemoteKind:  "query_job",
		RemoteID:    strings.TrimSpace(jobID),
		Summary:     verb + " " + summaryTarget,
	}, true
}

func queryLifecycleConnectorForProvider(provider string) string {
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case "bigquery", "databricks", "trino", "snowflake":
		return strings.TrimSpace(strings.ToLower(provider))
	default:
		return ""
	}
}

func queryLifecycleProviderLabel(connector string) string {
	switch strings.TrimSpace(strings.ToLower(connector)) {
	case "bigquery":
		return "BigQuery"
	case "databricks":
		return "Databricks"
	case "trino":
		return "Trino"
	case "snowflake":
		return "Snowflake"
	default:
		return strings.TrimSpace(connector)
	}
}

func (m *Manager) resolveOmittedGitRepoPathForPermission(ctx context.Context, toolName string, args json.RawMessage, activeProjectID string) (json.RawMessage, bool) {
	if !gitPermissionToolAllowsOmittedRepoPath(toolName) || m == nil {
		return args, true
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return args, true
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if strings.TrimSpace(stringMapValue(raw, "repo_path")) != "" {
		return args, true
	}
	repoPath := m.singleVisibleGitRepoPathForPermission(ctx, activeProjectID)
	if repoPath == "" {
		return args, false
	}
	if repoPath == "." {
		return args, true
	}
	raw["repo_path"] = repoPath
	encoded, err := json.Marshal(raw)
	if err != nil {
		return args, true
	}
	return json.RawMessage(encoded), true
}

func gitPermissionToolAllowsOmittedRepoPath(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "git_list_refs", "git_log", "git_search_history", "git_grep", "git_show_commit", "git_show_file_at_ref":
		return true
	default:
		return false
	}
}

func (m *Manager) resolveOmittedGitHubRepositoryForPermission(ctx context.Context, toolName string, args json.RawMessage, activeProjectID string) json.RawMessage {
	if !githubPermissionToolAllowsOmittedRepository(toolName) || m == nil {
		return args
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return args
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}
	if strings.TrimSpace(stringMapValue(raw, "repository")) != "" {
		return args
	}
	repository := m.singleVisibleGitHubRepositoryForPermission(ctx, activeProjectID)
	if repository == "" {
		return args
	}
	raw["repository"] = repository
	encoded, err := json.Marshal(raw)
	if err != nil {
		return args
	}
	return json.RawMessage(encoded)
}

func githubPermissionToolAllowsOmittedRepository(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case tools.GitHubToolListPullRequests,
		tools.GitHubToolGetPullRequest,
		tools.GitHubToolListIssues,
		tools.GitHubToolGetIssue,
		tools.GitHubToolListWorkflowRuns,
		tools.GitHubToolGetWorkflowRunJobs,
		tools.GitHubToolPullRepository,
		tools.GitHubToolRefreshRepository:
		return true
	default:
		return false
	}
}

func (m *Manager) visibleSourceGitHubRepositoriesForPermission(ctx context.Context) []string {
	if m == nil || m.sources == nil {
		return nil
	}
	rows, err := m.sources.List(ctx, sources.ListOptions{Kind: sources.KindGitHubClone})
	if err != nil {
		return nil
	}
	out := []string{}
	seen := map[string]struct{}{}
	for _, row := range rows {
		repository := sourceGitHubRepositoryForPermission(row)
		if repository == "" {
			continue
		}
		key := strings.ToLower(repository)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, repository)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) visibleSourceGitRepoPathsForPermission(ctx context.Context) []string {
	if m == nil || m.sources == nil {
		return nil
	}
	rows, err := m.sources.List(ctx, sources.ListOptions{Kind: sources.KindGitHubClone})
	if err != nil {
		return nil
	}
	out := []string{}
	seen := map[string]struct{}{}
	for _, row := range rows {
		repoPath := sourceGitRepoPathForPermission(row)
		if repoPath == "" {
			continue
		}
		key := strings.ToLower(repoPath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, repoPath)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) githubSourceForPermission(ctx context.Context, selector string) (sources.Source, bool) {
	if m == nil || m.sources == nil {
		return sources.Source{}, false
	}
	rows, err := m.sources.List(ctx, sources.ListOptions{Kind: sources.KindGitHubClone})
	if err != nil || len(rows) == 0 {
		return sources.Source{}, false
	}
	selector = strings.Trim(strings.TrimSpace(selector), "/")
	if selector == "" {
		if len(rows) == 1 {
			return rows[0], true
		}
		return sources.Source{}, false
	}
	normalizedSelector := strings.ToLower(selector)
	for _, row := range rows {
		if strings.EqualFold(strings.TrimSpace(row.ID), selector) ||
			strings.EqualFold(sourceGitHubRepositoryForPermission(row), normalizedSelector) ||
			strings.EqualFold(sourceGitRepoPathForPermission(row), selector) {
			return row, true
		}
	}
	return sources.Source{}, false
}

func sourceGitHubRepositoryForPermission(src sources.Source) string {
	repository := strings.Trim(strings.TrimSpace(src.Repository), "/")
	if repository == "" && strings.TrimSpace(src.Owner) != "" && strings.TrimSpace(src.Repo) != "" {
		repository = strings.TrimSpace(src.Owner) + "/" + strings.TrimSpace(src.Repo)
	}
	if !isCanonicalGitHubPermissionRepository(repository) {
		return ""
	}
	return repository
}

func sourceGitRepoPathForPermission(src sources.Source) string {
	if repository := sourceGitHubRepositoryForPermission(src); repository != "" {
		return repository
	}
	return strings.TrimSpace(src.ID)
}

func (m *Manager) singleVisibleGitHubRepositoryForPermission(ctx context.Context, projectID string) string {
	if m == nil {
		return ""
	}
	if repositories := m.visibleSourceGitHubRepositoriesForPermission(ctx); len(repositories) == 1 {
		return repositories[0]
	} else if len(repositories) > 1 {
		return ""
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" || m.projects == nil {
		return ""
	}
	root, err := m.projects.FilesDir(pid)
	if err != nil {
		return ""
	}
	sources, err := m.projects.ListSources(ctx, pid)
	if err != nil {
		return ""
	}
	visibleSources := map[string]struct{}{}
	for _, source := range sources {
		name := strings.TrimSpace(source.Name)
		if name == "" || source.Hidden || strings.Contains(strings.ReplaceAll(name, "\\", "/"), "/") {
			continue
		}
		visibleSources[name] = struct{}{}
	}
	repositories := visibleGitHubRepositoriesForPermission(ctx, filepath.Clean(root), visibleSources)
	if len(repositories) != 1 {
		return ""
	}
	return repositories[0]
}

func visibleGitHubRepositoriesForPermission(ctx context.Context, root string, visibleSources map[string]struct{}) []string {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil
	}
	repositories := []string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return fs.SkipDir
		}
		if p != root {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
			if _, ok := visibleSources[first]; !ok {
				return fs.SkipDir
			}
		}
		configPath := gitConfigPathForPermission(root, p)
		if configPath == "" {
			return nil
		}
		if repository := preferredGitHubRepositoryFromConfigForPermission(configPath); repository != "" {
			repositories = append(repositories, repository)
		}
		return fs.SkipDir
	})
	sort.Strings(repositories)
	return repositories
}

func gitConfigPathForPermission(projectRoot string, worktree string) string {
	gitPath := filepath.Join(worktree, ".git")
	st, err := os.Lstat(gitPath)
	if err != nil || st == nil {
		return ""
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return ""
	}
	if st.IsDir() {
		return filepath.Join(gitPath, "config")
	}
	if !st.Mode().IsRegular() {
		return ""
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(strings.ToLower(line), "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(line[len("gitdir:"):])
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(worktree, gitDir)
	}
	gitDir = filepath.Clean(gitDir)
	if !permissionPathIsSubpath(filepath.Clean(projectRoot), gitDir) {
		return ""
	}
	return filepath.Join(gitDir, "config")
}

func permissionPathIsSubpath(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	rel = filepath.Clean(rel)
	if rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func preferredGitHubRepositoryFromConfigForPermission(configPath string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	currentRemote := ""
	type remoteCandidate struct {
		name       string
		repository string
	}
	candidates := []remoteCandidate{}
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentRemote = gitRemoteSectionNameForPermission(line)
			continue
		}
		if currentRemote == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "url") {
			continue
		}
		repository := normalizeGitHubPermissionRepository(strings.TrimSpace(value))
		if !isCanonicalGitHubPermissionRepository(repository) {
			continue
		}
		candidates = append(candidates, remoteCandidate{name: currentRemote, repository: repository})
	}
	if len(candidates) == 0 {
		return ""
	}
	for _, candidate := range candidates {
		if candidate.name == "origin" {
			return candidate.repository
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].name != candidates[j].name {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].repository < candidates[j].repository
	})
	return candidates[0].repository
}

func gitRemoteSectionNameForPermission(line string) string {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
	if !strings.HasPrefix(body, `remote `) {
		return ""
	}
	return strings.Trim(strings.TrimSpace(strings.TrimPrefix(body, `remote `)), `"`)
}

func isCanonicalGitHubPermissionRepository(repository string) bool {
	owner, repo, ok := strings.Cut(strings.TrimSpace(repository), "/")
	return ok &&
		owner != "" &&
		repo != "" &&
		!strings.Contains(owner, " ") &&
		!strings.Contains(repo, " ") &&
		!strings.Contains(owner, ":") &&
		!strings.Contains(repo, ":")
}

func (m *Manager) singleVisibleGitRepoPathForPermission(ctx context.Context, projectID string) string {
	if m == nil {
		return ""
	}
	if repos := m.visibleSourceGitRepoPathsForPermission(ctx); len(repos) == 1 {
		return repos[0]
	} else if len(repos) > 1 {
		return ""
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" || m.projects == nil {
		return ""
	}
	root, err := m.projects.FilesDir(pid)
	if err != nil {
		return ""
	}
	sources, err := m.projects.ListSources(ctx, pid)
	if err != nil {
		return ""
	}
	visibleSources := map[string]struct{}{}
	for _, source := range sources {
		name := strings.TrimSpace(source.Name)
		if name == "" || source.Hidden || strings.Contains(strings.ReplaceAll(name, "\\", "/"), "/") {
			continue
		}
		visibleSources[name] = struct{}{}
	}
	repos := visibleGitRepoPathsForPermission(ctx, filepath.Clean(root), visibleSources)
	if len(repos) != 1 {
		return ""
	}
	return repos[0]
}

func visibleGitRepoPathsForPermission(ctx context.Context, root string, visibleSources map[string]struct{}) []string {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil
	}
	seen := map[string]struct{}{}
	repos := []string{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return fs.SkipDir
		}
		if p != root {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
			if _, ok := visibleSources[first]; !ok {
				return fs.SkipDir
			}
		}
		if st, err := os.Stat(filepath.Join(p, ".git")); err != nil || st == nil {
			return nil
		}
		clean := filepath.Clean(p)
		if _, ok := seen[clean]; ok {
			return nil
		}
		seen[clean] = struct{}{}
		rel := "."
		if p != root {
			if r, err := filepath.Rel(root, p); err == nil && r != "." {
				rel = filepath.ToSlash(r)
			}
		}
		repos = append(repos, rel)
		return nil
	})
	sort.Strings(repos)
	return repos
}

func (m *Manager) localManagedUploadSourceReadPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	if m == nil {
		return managerPermissionMeta{}, false
	}
	name := strings.TrimSpace(toolName)
	if name != tools.SlackToolUploadFile && name != "jira_upload_attachment" {
		return managerPermissionMeta{}, false
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return managerPermissionMeta{}, false
		}
	}
	localPath := stringMapValue(raw, "local_path", "localPath")
	if localPath == "" {
		return managerPermissionMeta{}, false
	}
	root := managedLocalPermissionRoot(m.cfgDir, localPath)
	if root == "" {
		return managerPermissionMeta{}, false
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "local_location",
		LocalRoot:   root,
		Operation:   name + "_managed_local_source_read",
		Excludes:    sensitiveLocalPermissionExcludes(),
		Summary:     "Allow " + strings.ReplaceAll(name, "_", " ") + " to read " + root,
	}, true
}

func managedLocalPermissionRoot(cfgDir string, localPath string) string {
	cfgDir = strings.TrimSpace(cfgDir)
	localPath = strings.TrimSpace(localPath)
	if cfgDir == "" || localPath == "" {
		return ""
	}
	baseAbs, err := filepath.Abs(cfgDir)
	if err != nil {
		return ""
	}
	target := localPath
	if !filepath.IsAbs(target) {
		clean := cleanLocalPermissionRel(target)
		if clean == "" {
			return ""
		}
		target = filepath.Join(baseAbs, clean)
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(baseAbs, targetAbs)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if !fileartifact.IsManagedDownloadRelPath(filepath.ToSlash(rel)) {
		return ""
	}
	return filepath.Clean(targetAbs)
}

func uploadArtifactSourceReadPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	name := strings.TrimSpace(toolName)
	if name != tools.SlackToolUploadFile && name != "jira_upload_attachment" {
		return managerPermissionMeta{}, false
	}
	artifactID := artifactIDForReadPermission(args)
	if artifactID == "" {
		return managerPermissionMeta{}, false
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "tool",
		Operation:   name + "_artifact_source_read",
		RemoteKind:  "artifact",
		RemoteID:    artifactID,
		Summary:     "Allow " + strings.ReplaceAll(name, "_", " ") + " to read artifact " + artifactID,
	}, true
}

func (m *Manager) localArtifactSourceReadPermissionMetaForManagerTool(ctx context.Context, toolName string, args json.RawMessage, localProjectRoot string) (managerPermissionMeta, bool) {
	projectRoot := strings.TrimSpace(localProjectRoot)
	if projectRoot == "" || m == nil || m.store == nil {
		return managerPermissionMeta{}, false
	}
	name := strings.TrimSpace(toolName)
	if name != "display_table" && name != "build_chart_artifact" {
		return managerPermissionMeta{}, false
	}
	artifactID := artifactSourceIDForPermission(args)
	if artifactID == "" {
		return managerPermissionMeta{}, false
	}
	artifact, err := m.store.GetArtifact(ctx, artifactID)
	if err != nil || !isReadFileArtifactForPermission(artifact) {
		return managerPermissionMeta{}, false
	}
	sourcePath := readFileArtifactPathForPermission(m.readArtifactContent(artifact))
	if sourcePath == "" {
		return managerPermissionMeta{}, false
	}
	root := localPermissionRoot(projectRoot, sourcePath)
	if root == "" {
		return managerPermissionMeta{}, false
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "local_location",
		LocalRoot:   root,
		Operation:   name + "_source_artifact_read",
		Excludes:    sensitiveLocalPermissionExcludes(),
		Summary:     "Allow " + strings.ReplaceAll(name, "_", " ") + " to read " + root,
	}, true
}

func (m *Manager) queryJobSourceReadPermissionMetaForManagerTool(threadID string, toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	if m == nil {
		return managerPermissionMeta{}, false
	}
	name := strings.TrimSpace(toolName)
	if name != "display_table" && name != "build_chart_artifact" {
		return managerPermissionMeta{}, false
	}
	jobID, ownerToken := queryJobSourceForPermission(args)
	if jobID == "" {
		return managerPermissionMeta{}, false
	}
	connector := ""
	if reg, ok := m.queryJobs.(*jobs.Registry); ok && reg != nil {
		owner := jobs.JobOwner{
			ThreadID: strings.TrimSpace(threadID),
			Handle:   strings.TrimSpace(ownerToken),
		}
		if snap, ok := reg.Snapshot(jobID, owner); ok {
			if snapConnector := queryLifecycleConnectorForProvider(snap.Provider); snapConnector != "" {
				connector = snapConnector
			}
			if strings.TrimSpace(snap.ID) != "" {
				jobID = strings.TrimSpace(snap.ID)
			}
		}
	}
	summaryTarget := strings.TrimSpace(jobID)
	if summaryTarget == "" {
		summaryTarget = "query job"
	} else if connector != "" {
		summaryTarget = queryLifecycleProviderLabel(connector) + " query " + summaryTarget
	} else {
		summaryTarget = "query " + summaryTarget
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "tool",
		Connector:   connector,
		Operation:   name + "_source_query_job_read",
		RemoteKind:  "query_job",
		RemoteID:    strings.TrimSpace(jobID),
		Summary:     "Read " + summaryTarget + " as " + strings.ReplaceAll(name, "_", " ") + " source",
	}, true
}

func queryJobSourceForPermission(args json.RawMessage) (string, string) {
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return "", ""
		}
	}
	source, ok := raw["source"].(map[string]any)
	if !ok {
		return "", ""
	}
	return stringMapValue(source, "job_id", "jobId"), stringMapValue(source, "owner_token", "ownerToken")
}

func artifactSourceIDForPermission(args json.RawMessage) string {
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return ""
		}
	}
	source, ok := raw["source"].(map[string]any)
	if !ok {
		return ""
	}
	return stringMapValue(source, "artifact_id", "artifactId")
}

func isReadFileArtifactForPermission(artifact store.Artifact) bool {
	if artifact.ToolName != nil && strings.EqualFold(strings.TrimSpace(*artifact.ToolName), "read_file") {
		return true
	}
	if artifact.Metadata == nil {
		return false
	}
	toolName := stringMapValue(artifact.Metadata, "tool_name", "toolName")
	return strings.EqualFold(toolName, "read_file")
}

func readFileArtifactPathForPermission(raw string) string {
	var envelope struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &envelope); err != nil {
		return ""
	}
	if strings.TrimSpace(envelope.Content) == "" {
		return ""
	}
	return strings.TrimSpace(envelope.Path)
}

func localUploadSourceReadPermissionMetaForManagerTool(toolName string, args json.RawMessage, localProjectRoot string) (managerPermissionMeta, bool) {
	projectRoot := strings.TrimSpace(localProjectRoot)
	if projectRoot == "" {
		return managerPermissionMeta{}, false
	}
	name := strings.TrimSpace(toolName)
	if name != tools.SlackToolUploadFile && name != "jira_upload_attachment" {
		return managerPermissionMeta{}, false
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return managerPermissionMeta{}, false
		}
	}
	sourcePath := stringMapValue(raw, "path")
	if sourcePath == "" {
		return managerPermissionMeta{}, false
	}
	root := localPermissionRoot(projectRoot, sourcePath)
	if root == "" {
		return managerPermissionMeta{}, false
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "local_location",
		LocalRoot:   root,
		Operation:   name + "_source_read",
		Excludes:    sensitiveLocalPermissionExcludes(),
		Summary:     "Allow " + strings.ReplaceAll(name, "_", " ") + " to read " + root,
	}, true
}

func browserPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	name := strings.TrimSpace(toolName)
	meta := managerPermissionMeta{
		Category:    permissions.CategoryExecute,
		SubjectKind: "tool",
		Connector:   "browser",
		Operation:   strings.TrimPrefix(name, "cua_browser_"),
	}
	if name != tools.BrowserControlToolNavigate {
		return meta, true
	}
	var in struct {
		URL string `json:"url"`
	}
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return meta, true
		}
	}
	origin := browserPermissionOrigin(in.URL)
	if origin == "" {
		return meta, true
	}
	meta.RemoteKind = "browser_origin"
	meta.RemoteID = origin
	meta.Summary = "Allow browser navigation to " + origin
	return meta, true
}

func browserPermissionOrigin(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed == nil {
		return ""
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	host := strings.ToLower(strings.TrimSpace(parsed.Host))
	if scheme == "" || host == "" {
		return ""
	}
	if scheme != "http" && scheme != "https" {
		return ""
	}
	return scheme + "://" + host
}

func permissionMetaForManagerTool(toolName string, args json.RawMessage, localProjectRoot string) (managerPermissionMeta, bool) {
	name := strings.TrimSpace(toolName)
	if meta, ok := localMachineReadPermissionMetaForManagerTool(name, args); ok {
		return meta, true
	}
	if meta, ok := localReadPermissionMetaForManagerTool(name, args, localProjectRoot); ok {
		return meta, true
	}
	if meta, ok := artifactTargetReadPermissionMetaForManagerTool(name, args); ok {
		return meta, true
	}
	if meta, ok := connectedReadRemoteTargetPermissionMetaForManagerTool(name, args); ok {
		return meta, true
	}
	switch name {
	case "list_files", "read_file", "search_files", "file_search", "local_list_folder", "local_read_file", "local_open_file", "local_read_pdf_page", "list_git_repositories", "git_list_refs", "git_log", "git_search_history", "git_grep", "git_show_commit", "git_show_file_at_ref", "describe_dataset", "sample_rows", "filter_csv", "aggregate_csv":
		return managerPermissionMeta{}, false
	case "rlm_exec":
		return managerPermissionMeta{Category: permissions.CategoryExecute, SubjectKind: "tool", Connector: "thread-search", Operation: "execute"}, true
	case "cancel_query":
		return managerPermissionMeta{Category: permissions.CategoryExecute, SubjectKind: "tool", Operation: "cancel_query", Summary: "Cancel a running query"}, true
	case "generate_image":
		return managerPermissionMeta{Category: permissions.CategoryExecute, SubjectKind: "tool", Operation: "generate_image", Summary: "Generate an image with the configured provider"}, true
	case tools.BrowserControlToolNavigate, tools.BrowserControlToolClick, tools.BrowserControlToolType, tools.BrowserControlToolPress, tools.BrowserControlToolWait, tools.BrowserControlToolBack, tools.BrowserControlToolClearLogs:
		return browserPermissionMetaForManagerTool(toolName, args)
	case tools.SlackToolPostMessage, tools.SlackToolUploadFile:
		meta, ok := slackPermissionMetaForManagerTool(toolName, args)
		return meta, ok
	case "jira_add_comment", "jira_reply_to_comment", "jira_add_related_tickets", "jira_upload_attachment", "jira_update_issue", "jira_create_issue":
		meta, ok := jiraPermissionMetaForManagerTool(toolName, args)
		return meta, ok
	case tools.GitHubToolCloneRepository:
		var in struct {
			Repository string `json:"repository"`
		}
		if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Repository) == "" {
			return managerPermissionMeta{}, false
		}
		repository := normalizeGitHubPermissionRepository(in.Repository)
		return managerPermissionMeta{
			Category:    permissions.CategoryExecute,
			SubjectKind: "repository",
			Connector:   "github",
			Operation:   "clone_repository",
			Repository:  repository,
			RemoteKind:  "github_repository",
			RemoteID:    repository,
			Summary:     "Clone GitHub repository " + displayGitHubPermissionRepository(repository),
		}, true
	case tools.GitHubToolPullRepository, tools.GitHubToolRefreshRepository:
		var in struct {
			Repository string `json:"repository"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return managerPermissionMeta{}, false
		}
		repository := normalizeGitHubPermissionRepository(in.Repository)
		operation := "refresh_repository"
		verb := "Refresh"
		if strings.TrimSpace(toolName) == tools.GitHubToolPullRepository {
			operation = "pull_repository"
			verb = "Pull"
		}
		subjectKind := "repository"
		summaryTarget := displayGitHubPermissionRepository(repository)
		if repository == "" {
			subjectKind = "tool"
			summaryTarget = "visible GitHub repositories"
		}
		return managerPermissionMeta{
			Category:    permissions.CategoryExecute,
			SubjectKind: subjectKind,
			Connector:   "github",
			Operation:   operation,
			Repository:  repository,
			RemoteKind:  gitHubRepositoryRemoteKind(repository),
			RemoteID:    repository,
			Summary:     verb + " " + summaryTarget,
		}, true
	case tools.GitHubToolRunGHCommand:
		var in struct {
			Args []string `json:"args"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return managerPermissionMeta{}, false
		}
		normalizedArgs, err := tools.NormalizeGitHubRawGHArgs(in.Args)
		if err != nil {
			return managerPermissionMeta{}, false
		}
		command := "gh " + strings.Join(normalizedArgs, " ")
		repository := normalizeGitHubPermissionRepository(gitHubRepositoryFromRawGHArgs(normalizedArgs))
		subjectKind := "tool"
		if repository != "" {
			subjectKind = "repository"
		}
		return managerPermissionMeta{
			Category:    permissions.CategoryExecute,
			SubjectKind: subjectKind,
			Connector:   "github",
			Operation:   "run_gh_command",
			Repository:  repository,
			RemoteKind:  gitHubRepositoryRemoteKind(repository),
			RemoteID:    repository,
			Summary:     "Run GitHub CLI command: " + command,
		}, true
	default:
		if meta, ok := tools.ToolMetadataByName(toolName); ok && meta.Category == tools.ToolCategoryRead {
			connector := strings.TrimSpace(meta.ConnectorID)
			return managerPermissionMeta{
				Category:    permissions.CategoryRead,
				SubjectKind: "tool",
				Connector:   connector,
				Operation:   strings.TrimSpace(toolName),
				Summary:     genericReadPermissionSummary(meta),
			}, true
		}
		return managerPermissionMeta{}, false
	}
}

func connectedReadRemoteTargetPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	name := strings.TrimSpace(toolName)
	productMeta, ok := tools.ToolMetadataByName(name)
	if !ok || productMeta.Category != tools.ToolCategoryRead || strings.TrimSpace(productMeta.ConnectorID) == "" {
		return managerPermissionMeta{}, false
	}
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return managerPermissionMeta{}, false
		}
	}
	remoteKind, remoteID := connectedReadRemoteTargetForManagerTool(name, raw)
	if remoteKind == "" || remoteID == "" {
		return managerPermissionMeta{}, false
	}
	label := strings.TrimSpace(productMeta.Label)
	if label == "" {
		label = strings.ReplaceAll(name, "_", " ")
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "tool",
		Connector:   productMeta.ConnectorID,
		ToolSetID:   productMeta.ToolSetID,
		Operation:   name,
		RemoteKind:  remoteKind,
		RemoteID:    remoteID,
		Summary:     "Allow " + label + " for " + strings.ReplaceAll(remoteKind, "_", " ") + " " + remoteID,
	}, true
}

func connectedReadRemoteTargetForManagerTool(toolName string, raw map[string]any) (string, string) {
	switch strings.TrimSpace(toolName) {
	case tools.SlackToolGetConversation, tools.SlackToolGetConversationMembers, tools.SlackToolGetMessages:
		if channel := stringMapValue(raw, "channel_id", "channel"); channel != "" {
			return "slack_channel", channel
		}
	case tools.SlackToolGetThreadReplies:
		channel := stringMapValue(raw, "channel_id", "channel")
		threadTS := stringMapValue(raw, "thread_ts", "threadTs")
		if channel != "" && threadTS != "" {
			return "slack_thread", channel + "/" + threadTS
		}
		if channel != "" {
			return "slack_channel", channel
		}
	case tools.SlackToolGetUser:
		if userID := stringMapValue(raw, "user_id", "user"); userID != "" {
			return "slack_user", userID
		}
	case tools.SlackToolDownloadFile:
		if fileID := stringMapValue(raw, "file_id", "fileId"); fileID != "" {
			return "slack_file", fileID
		}
	case tools.GitHubToolListPullRequests, tools.GitHubToolListIssues, tools.GitHubToolListWorkflowRuns:
		if repository := normalizeGitHubPermissionRepository(stringMapValue(raw, "repository")); repository != "" {
			return "github_repository", repository
		}
	case tools.GitHubToolGetPullRequest:
		repository := normalizeGitHubPermissionRepository(stringMapValue(raw, "repository"))
		number, ok := intMapValue(raw, "number")
		if repository != "" && ok && number > 0 {
			return "github_pull_request", fmt.Sprintf("%s#%d", repository, number)
		}
	case tools.GitHubToolGetIssue:
		repository := normalizeGitHubPermissionRepository(stringMapValue(raw, "repository"))
		number, ok := intMapValue(raw, "number")
		if repository != "" && ok && number > 0 {
			return "github_issue", fmt.Sprintf("%s#%d", repository, number)
		}
	case tools.GitHubToolGetWorkflowRunJobs:
		repository := normalizeGitHubPermissionRepository(stringMapValue(raw, "repository"))
		runID := strings.TrimSpace(stringMapValue(raw, "run_id", "runId"))
		if repository != "" && runID != "" {
			return "github_workflow_run", repository + "/" + runID
		}
	case "list_bq_datasets":
		if projectID := stringMapValue(raw, "project_id", "projectId"); projectID != "" {
			return "bigquery_project", projectID
		}
	case "run_bigquery_query":
		if projectID := stringMapValue(raw, "project_id", "projectId"); projectID != "" {
			return "bigquery_project", projectID
		}
	case "list_bq_tables":
		projectID := stringMapValue(raw, "project_id", "projectId")
		datasetID := stringMapValue(raw, "dataset_id", "datasetId")
		if remoteID := datasourcePermissionID(projectID, datasetID); remoteID != "" {
			return "bigquery_dataset", remoteID
		}
	case "get_bq_table_schema":
		projectID := stringMapValue(raw, "project_id", "projectId")
		datasetID := stringMapValue(raw, "dataset_id", "datasetId")
		tableID := stringMapValue(raw, "table_id", "tableId")
		if remoteID := datasourcePermissionID(projectID, datasetID, tableID); remoteID != "" {
			return "bigquery_table", remoteID
		}
	case "list_databricks_schemas":
		if catalogName := stringMapValue(raw, "catalog_name", "catalogName", "catalog"); catalogName != "" {
			return "databricks_catalog", catalogName
		}
	case "run_databricks_query":
		catalogName := stringMapValue(raw, "catalog_name", "catalogName", "catalog")
		schemaName := stringMapValue(raw, "schema_name", "schemaName", "schema")
		if remoteID := datasourcePermissionID(catalogName, schemaName); remoteID != "" {
			return "databricks_schema", remoteID
		}
		if catalogName != "" {
			return "databricks_catalog", catalogName
		}
	case "list_databricks_tables":
		catalogName := stringMapValue(raw, "catalog_name", "catalogName", "catalog")
		schemaName := stringMapValue(raw, "schema_name", "schemaName", "schema")
		if remoteID := datasourcePermissionID(catalogName, schemaName); remoteID != "" {
			return "databricks_schema", remoteID
		}
	case "describe_databricks_table":
		if fullName := stringMapValue(raw, "full_name", "fullName", "table"); fullName != "" {
			return "databricks_table", fullName
		}
	case "list_trino_schemas":
		if catalogName := stringMapValue(raw, "catalog_name", "catalogName", "catalog"); catalogName != "" {
			return "trino_catalog", catalogName
		}
	case "run_trino_query":
		catalogName := stringMapValue(raw, "catalog_name", "catalogName", "catalog")
		schemaName := stringMapValue(raw, "schema_name", "schemaName", "schema")
		if remoteID := datasourcePermissionID(catalogName, schemaName); remoteID != "" {
			return "trino_schema", remoteID
		}
		if catalogName != "" {
			return "trino_catalog", catalogName
		}
	case "list_trino_tables":
		catalogName := stringMapValue(raw, "catalog_name", "catalogName", "catalog")
		schemaName := stringMapValue(raw, "schema_name", "schemaName", "schema")
		if remoteID := datasourcePermissionID(catalogName, schemaName); remoteID != "" {
			return "trino_schema", remoteID
		}
	case "describe_trino_table":
		if fullName := stringMapValue(raw, "full_name", "fullName", "table"); fullName != "" {
			return "trino_table", fullName
		}
	case "list_snowflake_schemas":
		if databaseName := stringMapValue(raw, "database_name", "databaseName", "database"); databaseName != "" {
			return "snowflake_database", databaseName
		}
	case "run_snowflake_query":
		databaseName := stringMapValue(raw, "database_name", "databaseName", "database")
		schemaName := stringMapValue(raw, "schema_name", "schemaName", "schema")
		if remoteID := datasourcePermissionID(databaseName, schemaName); remoteID != "" {
			return "snowflake_schema", remoteID
		}
		if databaseName != "" {
			return "snowflake_database", databaseName
		}
	case "list_snowflake_tables":
		databaseName := stringMapValue(raw, "database_name", "databaseName", "database")
		schemaName := stringMapValue(raw, "schema_name", "schemaName", "schema")
		if remoteID := datasourcePermissionID(databaseName, schemaName); remoteID != "" {
			return "snowflake_schema", remoteID
		}
	case "describe_snowflake_table":
		if fullName := stringMapValue(raw, "full_name", "fullName", "table"); fullName != "" {
			return "snowflake_table", fullName
		}
	case "jira_get_issue", "jira_get_issue_history", "jira_get_issue_comments", "jira_get_issue_attachments":
		if issueKey := upperStringMapValue(raw, "issueKey", "issue_key"); issueKey != "" {
			return "jira_issue", issueKey
		}
	case "jira_read_attachment":
		if attachmentID := stringMapValue(raw, "attachmentId", "attachment_id"); attachmentID != "" {
			return "jira_attachment", attachmentID
		}
	case "jira_rest_get":
		if path := stringMapValue(raw, "path"); path != "" {
			return "jira_rest_path", path
		}
	case "confluence_search":
		if spaceKey := stringMapValue(raw, "spaceKey", "space_key"); spaceKey != "" {
			return "confluence_space", spaceKey
		}
	case "confluence_open_page":
		if pageID := stringMapValue(raw, "pageId", "page_id"); pageID != "" {
			return "confluence_page", pageID
		}
		if url := stringMapValue(raw, "url"); url != "" {
			return "confluence_page_url", url
		}
		spaceKey := stringMapValue(raw, "spaceKey", "space_key")
		title := stringMapValue(raw, "title")
		if spaceKey != "" && title != "" {
			return "confluence_page", spaceKey + "/" + title
		}
	case "confluence_open_space":
		if spaceKey := stringMapValue(raw, "spaceKey", "space_key"); spaceKey != "" {
			return "confluence_space", spaceKey
		}
	case "confluence_rest_get":
		if path := stringMapValue(raw, "path"); path != "" {
			return "confluence_rest_path", path
		}
	case tools.SharePointToolGetMetadata, tools.SharePointToolRead, tools.SharePointToolSummarize, tools.SharePointToolDownload:
		if serverRelativeURL := stringMapValue(raw, "server_relative_url", "serverRelativeUrl"); serverRelativeURL != "" {
			return "sharepoint_file", serverRelativeURL
		}
		if itemURL := stringMapValue(raw, "item_url", "itemUrl", "url"); itemURL != "" {
			return "sharepoint_file", itemURL
		}
	case tools.ArxivToolSearch:
		ids := firstStringSliceMapValue(raw, "ids")
		if len(ids) == 1 {
			return "arxiv_paper", ids[0]
		}
		if len(ids) > 1 {
			sortedIDs := append([]string(nil), ids...)
			sort.Strings(sortedIDs)
			return "arxiv_paper_set", strings.Join(sortedIDs, ",")
		}
	case tools.ArxivToolDownloadPaper:
		if arxivID := stringMapValue(raw, "arxiv_id", "arxivId"); arxivID != "" {
			return "arxiv_paper", arxivID
		}
	case tools.ArxivToolGetPaper, tools.ArxivToolGetPaperText, tools.ArxivToolReadPaper:
		if paper := stringMapValue(raw, "paper", "paper_key", "paperKey", "arxiv_id", "arxivId"); paper != "" {
			return "arxiv_paper", paper
		}
	case "get_doc":
		if docID := stringMapValue(raw, "doc_id", "docId"); docID != "" {
			return "search_doc", docID
		}
	case "list_messages":
		if threadID := stringMapValue(raw, "thread_id", "threadId"); threadID != "" {
			return "thread", threadID
		}
	}
	return "", ""
}

func datasourcePermissionID(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return ""
		}
		clean = append(clean, part)
	}
	return strings.Join(clean, ".")
}

func upperStringMapValue(m map[string]any, keys ...string) string {
	return strings.ToUpper(strings.TrimSpace(stringMapValue(m, keys...)))
}

func artifactTargetReadPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	name := strings.TrimSpace(toolName)
	productMeta, ok := tools.ToolMetadataByName(name)
	if !ok || productMeta.Category != tools.ToolCategoryRead {
		return managerPermissionMeta{}, false
	}
	artifactID := artifactIDForReadPermission(args)
	if artifactID == "" {
		return managerPermissionMeta{}, false
	}
	remoteKind := ""
	switch productMeta.ToolSetID {
	case "attachments":
		remoteKind = "attachment"
	case "artifacts":
		if name == "create_interactive_data_artifact" {
			remoteKind = "dataset_artifact"
		} else {
			remoteKind = "artifact"
		}
	case "data-analysis":
		switch name {
		case "inspect_dataset_artifact", "get_dataset_rows", "aggregate_dataset", "search_dataset_rows":
			remoteKind = "dataset_artifact"
		}
	case "thread-search":
		if name == "get_artifact" {
			remoteKind = "artifact"
		}
	}
	if remoteKind == "" {
		return managerPermissionMeta{}, false
	}
	summary := strings.TrimSpace(productMeta.Label)
	if summary == "" {
		summary = strings.ReplaceAll(name, "_", " ")
	}
	return managerPermissionMeta{
		Category:    permissions.CategoryRead,
		SubjectKind: "tool",
		Connector:   productMeta.ConnectorID,
		ToolSetID:   productMeta.ToolSetID,
		Operation:   name,
		RemoteKind:  remoteKind,
		RemoteID:    artifactID,
		Summary:     "Allow " + summary + " for " + remoteKind + " " + artifactID,
	}, true
}

func artifactIDForReadPermission(args json.RawMessage) string {
	var raw map[string]any
	if len(bytes.TrimSpace(args)) > 0 {
		if err := json.Unmarshal(args, &raw); err != nil {
			return ""
		}
	}
	if id := strings.TrimSpace(stringMapValue(raw, "artifact_id", "artifactId")); id != "" {
		return id
	}
	source, ok := raw["source"].(map[string]any)
	if !ok {
		return ""
	}
	return strings.TrimSpace(stringMapValue(source, "artifact_id", "artifactId"))
}

func genericReadPermissionSummary(meta tools.ToolMetadata) string {
	label := strings.TrimSpace(meta.Label)
	if label == "" {
		label = strings.ReplaceAll(strings.TrimSpace(meta.Name), "_", " ")
	}
	setName := strings.TrimSpace(meta.ToolSetName)
	if setName == "" {
		return "Allow " + label
	}
	return "Allow " + label + " in " + setName
}

func slackPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	var raw map[string]any
	if err := json.Unmarshal(args, &raw); err != nil {
		return managerPermissionMeta{}, false
	}
	channel := strings.TrimSpace(stringMapValue(raw, "channel_id", "channel"))
	if channel == "" {
		return managerPermissionMeta{}, false
	}
	switch toolName {
	case tools.SlackToolPostMessage:
		summary := "Send Slack message to " + channel
		remoteKind := "slack_channel"
		remoteID := channel
		if threadTS := strings.TrimSpace(stringMapValue(raw, "thread_ts")); threadTS != "" {
			summary += " in thread " + threadTS
			remoteKind = "slack_thread"
			remoteID = channel + "/" + threadTS
		}
		return managerPermissionMeta{
			Category:    permissions.CategoryWrite,
			SubjectKind: "tool",
			Connector:   "slack",
			Operation:   "slack_post_message_execute",
			RemoteKind:  remoteKind,
			RemoteID:    remoteID,
			Summary:     summary,
		}, true
	case tools.SlackToolUploadFile:
		fileName := firstNonEmptyString(
			stringMapValue(raw, "fileName", "file_name"),
			stringMapValue(raw, "artifact_id", "artifactId"),
			stringMapValue(raw, "path"),
			stringMapValue(raw, "local_path", "localPath"),
			"file",
		)
		return managerPermissionMeta{
			Category:    permissions.CategoryWrite,
			SubjectKind: "tool",
			Connector:   "slack",
			Operation:   "slack_upload_file_execute",
			RemoteKind:  "slack_channel",
			RemoteID:    channel,
			Summary:     "Upload Slack file " + fileName + " to " + channel,
		}, true
	default:
		return managerPermissionMeta{}, false
	}
}

func jiraPermissionMetaForManagerTool(toolName string, args json.RawMessage) (managerPermissionMeta, bool) {
	var raw map[string]any
	if err := json.Unmarshal(args, &raw); err != nil {
		return managerPermissionMeta{}, false
	}
	issueKey := strings.ToUpper(strings.TrimSpace(stringMapValue(raw, "issueKey", "issue_key")))
	base := managerPermissionMeta{
		Category:    permissions.CategoryWrite,
		SubjectKind: "tool",
		Connector:   "atlassian",
	}
	switch toolName {
	case "jira_add_comment":
		if issueKey == "" {
			return managerPermissionMeta{}, false
		}
		base.Operation = "jira_add_comment_execute"
		base.RemoteKind = "jira_issue"
		base.RemoteID = issueKey
		base.Summary = "Post Jira comment on " + issueKey
		return base, true
	case "jira_reply_to_comment":
		commentID := strings.TrimSpace(stringMapValue(raw, "commentId", "comment_id"))
		if issueKey == "" || commentID == "" {
			return managerPermissionMeta{}, false
		}
		base.Operation = "jira_reply_to_comment_execute"
		base.RemoteKind = "jira_comment"
		base.RemoteID = issueKey + "/" + commentID
		base.Summary = "Reply to Jira comment " + commentID + " on " + issueKey
		return base, true
	case "jira_add_related_tickets":
		if issueKey == "" {
			return managerPermissionMeta{}, false
		}
		base.Operation = "jira_add_related_tickets_execute"
		base.RemoteKind = "jira_issue"
		base.RemoteID = issueKey
		base.Summary = "Link related Jira tickets to " + issueKey
		return base, true
	case "jira_upload_attachment":
		if issueKey == "" {
			return managerPermissionMeta{}, false
		}
		fileName := firstNonEmptyString(
			stringMapValue(raw, "fileName", "file_name"),
			stringMapValue(raw, "artifact_id", "artifactId"),
			stringMapValue(raw, "path"),
			stringMapValue(raw, "local_path", "localPath"),
			"file",
		)
		base.Operation = "jira_upload_attachment_execute"
		base.RemoteKind = "jira_issue"
		base.RemoteID = issueKey
		base.Summary = "Upload Jira attachment " + fileName + " to " + issueKey
		return base, true
	case "jira_update_issue":
		if issueKey == "" {
			return managerPermissionMeta{}, false
		}
		base.Operation = "jira_update_issue_execute"
		base.RemoteKind = "jira_issue"
		base.RemoteID = issueKey
		base.Summary = "Update Jira issue " + issueKey
		return base, true
	case "jira_create_issue":
		projectKey := strings.ToUpper(strings.TrimSpace(stringMapValue(raw, "projectKey", "project_key")))
		if projectKey == "" {
			return managerPermissionMeta{}, false
		}
		summary := strings.TrimSpace(stringMapValue(raw, "summary"))
		base.Operation = "jira_create_issue_execute"
		base.RemoteKind = "jira_project"
		base.RemoteID = projectKey
		base.Summary = "Create Jira issue in " + projectKey
		if summary != "" {
			base.Summary += ": " + summary
		}
		return base, true
	default:
		return managerPermissionMeta{}, false
	}
}

func normalizeGitHubPermissionRepository(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{
		"https://github.com/",
		"http://github.com/",
		"git@github.com:",
		"ssh://git@github.com/",
	} {
		if strings.HasPrefix(lower, prefix) {
			value = value[len(prefix):]
			break
		}
	}
	value = strings.TrimSuffix(value, ".git")
	value = strings.Trim(value, "/")
	if idx := strings.IndexAny(value, "?#"); idx >= 0 {
		value = value[:idx]
	}
	parts := strings.Split(value, "/")
	if len(parts) >= 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
		return strings.ToLower(strings.TrimSpace(parts[0]) + "/" + strings.TrimSpace(parts[1]))
	}
	return strings.ToLower(strings.TrimSpace(raw))
}

func displayGitHubPermissionRepository(repository string) string {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return "GitHub repository"
	}
	return repository
}

func gitHubRepositoryRemoteKind(repository string) string {
	if strings.TrimSpace(repository) == "" {
		return ""
	}
	return "github_repository"
}

func gitHubRepositoryFromRawGHArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		lower := strings.ToLower(arg)
		switch {
		case lower == "--repo" || lower == "-r":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(lower, "--repo="):
			return strings.TrimSpace(arg[len("--repo="):])
		}
	}
	if len(args) >= 3 && strings.EqualFold(args[0], "repo") {
		for _, arg := range args[2:] {
			if arg == "--" {
				break
			}
			if strings.HasPrefix(arg, "-") {
				continue
			}
			if strings.Contains(arg, "/") {
				return arg
			}
			break
		}
	}
	return ""
}

func approvalPromptSummary(toolName string, category string) string {
	switch strings.TrimSpace(toolName) {
	case "rlm_exec":
		return "Allow the agent to run an executable planning tool."
	case tools.BrowserControlToolNavigate:
		return "Allow the agent to navigate the local browser."
	case tools.BrowserControlToolClick:
		return "Allow the agent to click in the local browser."
	case tools.BrowserControlToolType:
		return "Allow the agent to type in the local browser."
	case tools.BrowserControlToolPress:
		return "Allow the agent to press keys in the local browser."
	case tools.BrowserControlToolBack:
		return "Allow the agent to go back in the local browser."
	case tools.BrowserControlToolClearLogs:
		return "Allow the agent to clear browser logs."
	default:
		return fmt.Sprintf("Allow %s access for %s.", category, toolName)
	}
}

func approvalEventPayload(approval store.ApprovalRequest) map[string]any {
	return map[string]any{
		"id":           approval.ID,
		"state":        approval.Status,
		"status":       approval.Status,
		"category":     approval.Category,
		"tool":         approval.Tool,
		"tool_call_id": approval.ToolCallID,
		"summary":      approval.Summary,
		"options":      approval.Options,
		"subject":      approval.Subject,
		"preview":      approval.Preview,
		"decision":     approval.Decision,
		"created_at":   approval.CreatedAt,
		"expires_at":   approval.ExpiresAt,
		"updated_at":   approval.UpdatedAt,
	}
}

func approvalToolResult(status string, approvalID string, category string, toolName string, detail string, approval *store.ApprovalRequest) tools.Output {
	result := map[string]any{
		"status":      strings.TrimSpace(status),
		"approval_id": strings.TrimSpace(approvalID),
		"category":    strings.TrimSpace(category),
		"tool":        strings.TrimSpace(toolName),
		"detail":      strings.TrimSpace(detail),
		"result_metadata": map[string]any{
			"kind":     "approval",
			"widget":   "approval",
			"readonly": false,
		},
	}
	if approval != nil {
		result["approval"] = approvalEventPayload(*approval)
	}
	return tools.JSONOK(result)
}

func (m *Manager) updatePlanFromToolResult(plan *RunContextPlan, out tools.Output, toolName string) {
	if plan == nil {
		return
	}
	if strings.TrimSpace(toolName) != "rlm_exec" {
		return
	}
	mm := out.QueryMetadata
	if len(mm) == 0 {
		return
	}
	if v, ok := mm["evidence_budget"]; ok {
		plan.EvidenceBudget = toInt(v)
	}
	if v, ok := mm["evidence_used"]; ok {
		plan.EvidenceUsed = toInt(v)
	}
	if v, ok := mm["evidence_budget_exhausted"]; ok {
		if b, ok := v.(bool); ok {
			plan.EvidenceExhausted = b
		}
	}
	if plan.EvidenceBudget > 0 && plan.EvidenceUsed >= plan.EvidenceBudget {
		plan.EvidenceExhausted = true
	}
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int8:
		return int(x)
	case int16:
		return int(x)
	case int32:
		return int(x)
	case int64:
		return int(x)
	case uint:
		return int(x)
	case uint8:
		return int(x)
	case uint16:
		return int(x)
	case uint32:
		return int(x)
	case uint64:
		return int(x)
	case float32:
		return int(x)
	case float64:
		return int(x)
	}
	return 0
}

func parseBoolValue(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case int:
		return b != 0
	case int8:
		return b != 0
	case int16:
		return b != 0
	case int32:
		return b != 0
	case int64:
		return b != 0
	case uint:
		return b != 0
	case uint8:
		return b != 0
	case uint16:
		return b != 0
	case uint32:
		return b != 0
	case uint64:
		return b != 0
	case float32:
		return b != 0
	case float64:
		return b != 0
	case string:
		return strings.EqualFold(strings.TrimSpace(b), "true")
	}
	return false
}

func (m *Manager) persistToolResult(ctx context.Context, thread store.Thread, run store.Run, call types.ToolCall, out tools.Output, contextPlan RunContextPlan) types.ChatMessage {
	persistCtx, cancelPersist := toolResultPersistenceContext(ctx)
	defer cancelPersist()

	toolName := strings.TrimSpace(call.Name)
	out.Result = sanitizeInteractiveHTMLToolResult(toolName, out.Result)
	modelFacingCall := sanitizeAsyncQueryToolCallForModel(toolName, call, out)
	out = enrichToolErrorOutput(toolName, modelFacingCall, out)

	msgID := types.NewUUID()
	toolCallID := strings.TrimSpace(call.ID)
	externalFileArtifacts := m.prepareExternalToolFileArtifacts(persistCtx, thread, run, call, msgID, toolCallID, &out)

	artifactID := ""
	if out.Artifact != nil {
		artifactID = strings.TrimSpace(out.Artifact.ID)
	}
	if artifactID == "" {
		artifactID = resolveToolArtifactIDForThread(call, out, thread.ID)
	}
	if strings.TrimSpace(artifactID) == "" {
		artifactID = types.NewUUID()
	}

	toolRefs := extractToolReferencedArtifactIDs(toolName, call.Args)
	toolRefs = append(toolRefs, extractToolResultReferencedArtifactIDs(toolName, out)...)
	for _, external := range externalFileArtifacts {
		toolRefs = append(toolRefs, external.Artifact.ID)
	}
	toolRefs = append(toolRefs, artifactID)
	toolRefs = dedupeStringSlice(toolRefs)

	contentText, err := out.ContentString()
	if err != nil {
		contentText = "{\"error\":\"failed to encode tool result\"}"
		out = tools.JSONError("failed to encode tool result")
	}
	out = enrichToolOutputResultMetadata(toolName, out)

	includeFullResult := parseBoolValueFromMap(out.QueryMetadata)
	resultPayload, envelopeCompaction := compactToolResultEnvelope(toolName, out, includeFullResult)
	if envelopeCompaction != nil && envelopeCompaction.Compacted {
		logToolResultEnvelopeCompaction(run.ID, artifactID, toolName, envelopeCompaction)
	}
	var compactionMeta map[string]any
	if m.toolCompaction != nil {
		hardBudget, charsPerToken := m.resolveRunToolResultBudget(run)
		compacted := m.toolCompaction.CompactForPersistence(ToolResultPersistenceInput{
			ToolName:          toolName,
			Result:            resultPayload,
			QueryMetadata:     out.QueryMetadata,
			IncludeFullResult: includeFullResult,
			HardTokenBudget:   hardBudget,
			CharsPerToken:     charsPerToken,
		})
		resultPayload = compacted.Result
		if compacted.Compacted {
			compactionMeta = compacted.Metadata
		}
	}

	env := map[string]any{
		"artifact_id":   artifactID,
		"tool_name":     toolName,
		"tool_call_id":  toolCallID,
		"ok":            out.OK,
		"result_format": string(out.ResultFormat),
		"result":        resultPayload,
	}
	if len(modelFacingCall.Args) > 0 {
		env["tool_args"] = cloneStringAnyMap(modelFacingCall.Args)
	}
	if meta, ok := tools.ToolMetadataByName(toolName); ok {
		env["tool_presentation"] = meta.Presentation
	}
	if out.ResultFormat == tools.ResultFormatJSON {
		if resultMap, ok := out.Result.(map[string]any); ok {
			if queryOutputFlag(resultMap, "ui_suppress", "suppress_ui", "hide_from_ui") {
				env["ui_suppress"] = true
			}
		}
	}
	if plotSpec, chartEngine, chartSpecVersion, chartModule, chartModules := extractPlotSpecMetadata(out.Result); plotSpec != nil {
		env["plot_spec"] = plotSpec
		if chartEngine != "" {
			env["chart_engine"] = chartEngine
		}
		if chartSpecVersion != "" {
			env["chart_spec_version"] = chartSpecVersion
		}
		if chartModule != "" {
			env["chart_module"] = chartModule
		}
		if len(chartModules) > 0 {
			env["chart_modules"] = chartModules
		}
	}
	if previewOnly, ok := firstBoolMapValue(anyMap(out.Result), "preview_only"); ok {
		env["preview_only"] = previewOnly
	}
	if fullArtifactID := firstStringMapValue(anyMap(out.Result), "full_artifact_id"); fullArtifactID != "" {
		env["full_artifact_id"] = fullArtifactID
	}
	if rowCountTotal, ok := firstIntMapValue(anyMap(out.Result), "row_count_total"); ok {
		env["row_count_total"] = rowCountTotal
	}
	if downsampleRatio, ok := firstFloatMapValue(anyMap(out.Result), "downsample_ratio"); ok {
		env["downsample_ratio"] = downsampleRatio
	}
	if chartBudget, ok := extractChartBudgetMetadata(out.Result); ok {
		env["chart_budget"] = chartBudget
	}
	if inlinePreview, ok := toolOutputInlinePreview(out); ok {
		env["inline_preview"] = inlinePreview
	}
	env["result_metadata"] = map[string]any{}
	resultMetadata := env["result_metadata"].(map[string]any)
	if b, err := json.Marshal(env["result"]); err == nil {
		resultMetadata["result_size_bytes"] = len(b)
	}
	out = tools.NormalizeOutputMetadata(out)
	if len(out.ResultMetadata) > 0 {
		for key, value := range out.ResultMetadata {
			resultMetadata[key] = value
		}
	}
	if previewOnly, ok := firstBoolMapValue(anyMap(out.Result), "preview_only"); ok {
		resultMetadata["preview_only"] = previewOnly
	}
	if fullArtifactID := firstStringMapValue(anyMap(out.Result), "full_artifact_id"); fullArtifactID != "" {
		resultMetadata["full_artifact_id"] = fullArtifactID
	}
	if rowCountTotal, ok := firstIntMapValue(anyMap(out.Result), "row_count_total"); ok {
		resultMetadata["row_count_total"] = rowCountTotal
	}
	if downsampleRatio, ok := firstFloatMapValue(anyMap(out.Result), "downsample_ratio"); ok {
		resultMetadata["downsample_ratio"] = downsampleRatio
	}
	if chartBudget, ok := firstMapMapValue(anyMap(env), "chart_budget"); ok {
		resultMetadata["chart_budget"] = cloneStringAnyMap(chartBudget)
	}
	if inlinePreview, ok := toolOutputInlinePreview(out); ok {
		resultMetadata["inline_preview"] = inlinePreview
	}
	if envelopeCompaction != nil && envelopeCompaction.Compacted {
		resultMetadata["model_context_compacted"] = true
		resultMetadata["model_context_compaction_stage"] = "envelope"
		resultMetadata["model_context_compaction_reason"] = envelopeCompaction.Reason
		resultMetadata["result_size_bytes_before_compaction"] = envelopeCompaction.ResultSizeBefore
		resultMetadata["result_size_bytes_after_compaction"] = envelopeCompaction.ResultSizeAfter
	}
	for key, value := range compactionMeta {
		resultMetadata[key] = value
	}
	if out.QueryMetadata != nil {
		env["query_metadata"] = out.QueryMetadata
	}
	if len(resultMetadata) > 0 {
		env["result_metadata"] = resultMetadata
	} else {
		delete(env, "result_metadata")
	}
	sanitized := SanitizeModelToolResultEnvelope(env, nil)
	env = sanitized.Envelope
	if sanitized.Compacted {
		log.Printf(
			"run %s: sanitized model-facing tool result artifact_id=%s tool_name=%s bytes_before=%d envelope_bytes=%d sha256=%s",
			run.ID,
			artifactID,
			toolName,
			sanitized.ResultBytes,
			sanitized.EnvelopeBytes,
			sanitized.SHA256,
		)
	}
	resultMetadata = map[string]any{}
	if meta, ok := firstMapMapValue(anyMap(env), "result_metadata"); ok {
		resultMetadata = meta
	}
	envBytes, _ := json.Marshal(env)
	if len(resultMetadata) > 0 {
		resultMetadata["sse_event_bytes"] = len(envBytes)
		if chartBudget, ok := firstMapMapValue(resultMetadata, "chart_budget"); ok {
			chartBudget["sse_event_bytes"] = len(envBytes)
			resultMetadata["chart_budget"] = chartBudget
			env["chart_budget"] = chartBudget
		}
		env["result_metadata"] = resultMetadata
		envBytes, _ = json.Marshal(env)
	}
	if isChartBudgetTool(toolName) {
		logChartBudgetSummary(run.ID, artifactID, toolName, env, resultMetadata)
	}

	uiHidden := shouldSuppressToolUI(toolName, out)

	msgContent := string(envBytes)
	toolMsg := store.Message{
		ID:                    msgID,
		Role:                  types.RoleTool,
		Content:               &msgContent,
		ReferencedArtifactIDs: toolRefs,
		ContextSummary:        summarizeContextPlan(contextPlan),
		ToolCallID:            &toolCallID,
		Name:                  &toolName,
		UIHidden:              uiHidden,
		ContextPlan: func() json.RawMessage {
			if blob, err := json.Marshal(contextPlan); err == nil {
				return blob
			}
			return nil
		}(),
	}
	persistedToolMsg, err := m.store.AppendMessage(persistCtx, thread.ID, &run.ID, toolMsg)
	if err != nil {
		log.Printf("run %s: failed to persist tool message: %v", run.ID, err)
		return types.ChatMessage{
			Role:       types.RoleTool,
			Content:    &msgContent,
			ToolCallID: toolCallID,
			Name:       toolName,
		}
	}
	persistedMsgID := strings.TrimSpace(persistedToolMsg.ID)
	if persistedMsgID == "" {
		persistedMsgID = msgID
	}
	var persistedMsgSeq *int64
	if persistedToolMsg.Seq > 0 {
		seq := persistedToolMsg.Seq
		persistedMsgSeq = &seq
	}

	// Create artifact payload for UI.
	artifactType, title, language, artifactContent, meta := buildArtifact(toolName, out, contentText)
	if meta == nil {
		meta = map[string]any{}
	}
	if _, exists := meta["tool_args"]; !exists && len(modelFacingCall.Args) > 0 {
		meta["tool_args"] = cloneStringAnyMap(modelFacingCall.Args)
	}
	if productMeta, ok := tools.ToolMetadataByName(toolName); ok {
		if _, exists := meta["tool_presentation"]; !exists {
			meta["tool_presentation"] = productMeta.Presentation
		}
	}
	if payload := out.Artifact; payload != nil {
		if t := strings.TrimSpace(payload.Type); t != "" {
			artifactType = t
		}
		if t := strings.TrimSpace(payload.Title); t != "" {
			title = t
		}
		if payload.Language != nil {
			l := strings.TrimSpace(*payload.Language)
			if l != "" {
				language = &l
			}
		}
		artifactContent = payload.Content
		if payload.Metadata != nil {
			merged := map[string]any{}
			for key, value := range meta {
				merged[key] = value
			}
			for key, value := range payload.Metadata {
				if key == "result_metadata" {
					resultMetadata := map[string]any{}
					if existing, ok := firstMapMapValue(merged, "result_metadata"); ok {
						for existingKey, existingValue := range existing {
							resultMetadata[existingKey] = existingValue
						}
					}
					if incoming, ok := value.(map[string]any); ok {
						for incomingKey, incomingValue := range incoming {
							resultMetadata[incomingKey] = incomingValue
						}
						merged[key] = resultMetadata
						continue
					}
				}
				merged[key] = value
			}
			meta = merged
		}
	}
	if tools.IsAsyncQueryRunTool(toolName) {
		// An explicit artifact payload must not be able to restore the legacy
		// concrete replacement id that was removed from the model envelope.
		if len(modelFacingCall.Args) > 0 {
			meta["tool_args"] = cloneStringAnyMap(modelFacingCall.Args)
		} else {
			delete(meta, "tool_args")
		}
	}
	if isAsyncQueryTool(toolName) {
		// Every query-lifecycle artifact can occupy the deterministic durable-job
		// slot that is eventually upgraded by get_query_results. Keep that shared
		// record run-neutral, including nested tool_args and registry metadata.
		meta = stripSharedQueryArtifactRetryFields(meta)
		if out.Artifact == nil && !parseBoolValue(meta["placeholder"]) && !queryOutputCarriesMaterializedDataset(out) {
			// Lifecycle/preflight artifacts carry status rather than datasets. The
			// model receives the run-scoped qob in its tool envelope; the persisted
			// shared artifact receives a recursively sanitized status snapshot.
			cleanResult := stripQueryRegistryRunScopedFields(out.Result)
			if encoded, err := json.Marshal(cleanResult); err == nil {
				artifactContent = string(encoded)
			}
		}
	}
	if inlinePreview, ok := toolOutputInlinePreview(out); ok {
		if meta == nil {
			meta = map[string]any{}
		}
		if _, exists := meta["inline_preview"]; !exists {
			meta["inline_preview"] = inlinePreview
		}
	}
	if planID := strings.TrimSpace(run.PlanID); planID != "" {
		meta["plan_id"] = planID
	}

	storage := "mysql"
	var contentTextPtr *string
	var contentPathPtr *string

	b := []byte(artifactContent)
	if len(b) > artifactInlineMaxBytes {
		storage = "file"
		rel, werr := m.writeArtifactFile(artifactID, artifactType, b)
		if werr == nil {
			contentPathPtr = &rel
		} else {
			// Fallback to truncation in MySQL.
			storage = "mysql"
			tr := types.SafeTruncate(artifactContent)
			contentTextPtr = &tr
		}
	} else {
		contentTextPtr = &artifactContent
	}

	okVal := out.OK
	a := store.Artifact{
		ID:          artifactID,
		ThreadID:    thread.ID,
		MessageID:   &persistedMsgID,
		Type:        artifactType,
		Title:       title,
		Language:    language,
		ToolName:    &toolName,
		ToolCallID:  &toolCallID,
		OK:          &okVal,
		Storage:     storage,
		ContentText: contentTextPtr,
		ContentPath: contentPathPtr,
		ByteSize:    int64(len(b)),
		Metadata:    meta,
	}
	if _, err := m.store.InsertArtifact(persistCtx, a); err != nil {
		if errors.Is(err, store.ErrConflict) {
			// Async queries persist a placeholder under the deterministic
			// artifact id when the job is submitted; the completed
			// get_query_results dataset arrives later under the SAME id and
			// must replace it, otherwise /api/artifacts/:id serves the stale
			// placeholder forever and the explorer can never hydrate from
			// storage (missed SSE, reload, polling-only completion).
			// Conflicts between two results artifacts (e.g. paginated
			// get_query_results calls) still keep the first page.
			if m.upgradeQueryPlaceholderArtifact(persistCtx, run.ID, a) {
				log.Printf("run %s: upgraded query placeholder artifact %s with completed results", run.ID, artifactID)
			} else {
				log.Printf("run %s: artifact %s already exists; reusing existing artifact", run.ID, artifactID)
			}
		} else {
			log.Printf("run %s: failed to persist artifact %s: %v", run.ID, artifactID, err)
		}
	}

	fileAttachments := m.persistExternalToolFileArtifacts(persistCtx, thread, run, toolName, externalFileArtifacts)
	fileAttachments = m.appendReferencedFileAttachments(persistCtx, thread.ID, fileAttachments, extractToolResultReferencedArtifactIDs(toolName, out))
	imageAttachments := imageAttachmentsForModelArtifact(artifactID, title, artifactType, artifactContent, meta)
	return types.ChatMessage{
		ID:               persistedMsgID,
		Seq:              persistedMsgSeq,
		Role:             types.RoleTool,
		Content:          &msgContent,
		ToolCallID:       toolCallID,
		Name:             toolName,
		ImageAttachments: imageAttachments,
		FileAttachments:  fileAttachments,
	}
}

// Tool execution uses the run context so Stop/steer can cancel provider work
// immediately. The terminal result still has to be persisted after that
// context is cancelled; otherwise a reload loses the explicit cancellation
// envelope and the UI is forced to guess. Keep request-scoped values while
// removing cancellation, and bound the cleanup write so shutdown cannot hang.
func toolResultPersistenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		return context.WithTimeout(context.Background(), 5*time.Second)
	}
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

func parseBoolValueFromMap(mm map[string]any) bool {
	if mm == nil {
		return false
	}
	return parseBoolValue(mm["full_data"]) || parseBoolValue(mm["full_data_requested"])
}

// upgradeQueryPlaceholderArtifact replaces an async-query placeholder artifact
// with the completed results artifact that conflicted with it on insert.
// Returns true when a replacement happened. Only upgrades when the incoming
// artifact is a successful query-results dataset and the stored record is
// still a placeholder (or a legacy async-run record without placeholder
// metadata), so repeated results inserts never clobber an existing dataset.
func (m *Manager) upgradeQueryPlaceholderArtifact(ctx context.Context, runID string, incoming store.Artifact) bool {
	toolName := ""
	if incoming.ToolName != nil {
		toolName = strings.TrimSpace(strings.ToLower(*incoming.ToolName))
	}
	if !isAsyncQueryResultsTool(toolName) || incoming.OK == nil || !*incoming.OK {
		return false
	}
	if parseBoolValue(incoming.Metadata["placeholder"]) {
		return false
	}
	existing, err := m.store.GetArtifact(ctx, incoming.ID)
	if err != nil {
		log.Printf("run %s: failed to load conflicting artifact %s for upgrade: %v", runID, incoming.ID, err)
		return false
	}
	if strings.TrimSpace(existing.ThreadID) != strings.TrimSpace(incoming.ThreadID) {
		return false
	}
	existingTool := ""
	if existing.ToolName != nil {
		existingTool = strings.TrimSpace(strings.ToLower(*existing.ToolName))
	}
	isPlaceholder := parseBoolValue(existing.Metadata["placeholder"]) ||
		parseBoolValue(existing.Metadata["async_query"]) ||
		tools.IsAsyncQueryRunTool(existingTool)
	if !isPlaceholder || isAsyncQueryResultsTool(existingTool) {
		return false
	}
	if _, err := m.store.ReplaceArtifact(ctx, incoming); err != nil {
		log.Printf("run %s: failed to upgrade placeholder artifact %s: %v", runID, incoming.ID, err)
		return false
	}
	return true
}

func (m *Manager) resolveRunToolResultBudget(run store.Run) (hardTokenBudget int, charsPerToken int) {
	hardTokenBudget = defaultHardContextTokenCap
	charsPerToken = defaultTokenCharsPerToken
	if isAnthropicProviderContext(run.ProviderID, run.Model) {
		if _, hard, chars, ok := anthropic.ModelContextProfile(run.Model); ok {
			if hard > 0 {
				hardTokenBudget = hard
			}
			if chars > 0 {
				charsPerToken = chars
			}
		} else if strings.HasPrefix(strings.ToLower(strings.TrimSpace(run.Model)), "claude-") {
			charsPerToken = 3
		}
	}
	if m != nil && m.settings != nil {
		if guardCap, ok := compactionPlanningTokenCap(m.settings.Get(), run.ProviderID); ok {
			if guardCap > 0 && guardCap < hardTokenBudget {
				hardTokenBudget = guardCap
			}
		}
	}
	hardTokenBudget = maxInt(1, hardTokenBudget)
	charsPerToken = maxInt(1, charsPerToken)
	return hardTokenBudget, charsPerToken
}

func sanitizeInteractiveHTMLToolResult(toolName string, result any) any {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "create_interactive_artifact", "create_interactive_data_artifact":
	default:
		return result
	}

	resultMap, ok := result.(map[string]any)
	if !ok {
		return result
	}

	out := make(map[string]any, len(resultMap)+1)
	for key, value := range resultMap {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "html", "content", "srcdoc", "src_doc":
			continue
		default:
			out[key] = value
		}
	}
	if _, exists := out["inline_preview"]; !exists {
		out["inline_preview"] = true
	}
	return out
}

func toolOutputInlinePreview(out tools.Output) (bool, bool) {
	if inlinePreview, ok := firstBoolMapValue(anyMap(out.Result), "inline_preview", "inlinePreview"); ok {
		return inlinePreview, true
	}
	if out.Artifact != nil {
		if inlinePreview, ok := firstBoolMapValue(out.Artifact.Metadata, "inline_preview", "inlinePreview"); ok {
			return inlinePreview, true
		}
	}
	return false, false
}

type toolResultEnvelopeCompactionInfo struct {
	Compacted        bool
	Reason           string
	ResultSizeBefore int
	ResultSizeAfter  int
	RowsTotal        int
}

func compactToolResultEnvelope(toolName string, out tools.Output, includeFullResult bool) (any, *toolResultEnvelopeCompactionInfo) {
	_ = toolName
	_ = includeFullResult
	if out.ResultFormat != tools.ResultFormatJSON {
		return out.Result, nil
	}

	var (
		reason    string
		rowsTotal int
	)
	if mm, ok := out.Result.(map[string]any); ok {
		if _, total, exceeded := firstOversizedToolResultArray(mm); exceeded {
			reason = "row_count_threshold"
			rowsTotal = total
		}
	}
	resultSizeBefore := marshalJSONSize(out.Result)
	if reason == "" && resultSizeBefore > toolResultEnvelopeByteLimit {
		reason = "result_bytes_threshold"
	}
	if reason == "" {
		return out.Result, nil
	}

	compacted := compactToolResultPayload(out.Result)
	resultSizeAfter := marshalJSONSize(compacted)
	info := &toolResultEnvelopeCompactionInfo{
		Compacted:        true,
		Reason:           reason,
		ResultSizeBefore: resultSizeBefore,
		ResultSizeAfter:  resultSizeAfter,
		RowsTotal:        rowsTotal,
	}
	return compacted, info
}

func firstOversizedToolResultArray(mm map[string]any) (field string, total int, exceeded bool) {
	if mm == nil {
		return "", 0, false
	}
	for _, key := range []string{"rows", "results", "messages", "evidence"} {
		if count := countAnySlice(mm[key]); count > toolResultEnvelopeArrayLimit {
			return key, count, true
		}
	}
	return "", 0, false
}

func marshalJSONSize(value any) int {
	if value == nil {
		return 0
	}
	b, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(b)
}

func logToolResultEnvelopeCompaction(runID, artifactID, toolName string, info *toolResultEnvelopeCompactionInfo) {
	if info == nil || !info.Compacted {
		return
	}
	log.Printf(
		"run %s: model_context_compaction artifact_id=%s tool_name=%s reason=%s bytes_before=%d bytes_after=%d rows_total=%d",
		strings.TrimSpace(runID),
		strings.TrimSpace(artifactID),
		strings.TrimSpace(toolName),
		strings.TrimSpace(info.Reason),
		info.ResultSizeBefore,
		info.ResultSizeAfter,
		info.RowsTotal,
	)
}

func compactToolResultPayload(value any) any {
	switch v := value.(type) {
	case []string:
		arr := make([]any, 0, len(v))
		for _, item := range v {
			arr = append(arr, item)
		}
		return compactToolResultArray(arr)
	case map[string]any:
		out := make(map[string]any, min(toolResultMapKeyLimit, len(v)))
		keysLeft := toolResultMapKeyLimit

		addArrayPreview := func(key string, raw any) {
			if keysLeft <= 0 {
				return
			}
			out[key] = compactToolResultArray(raw)
			out[key+"_total"] = countAnySlice(raw)
			out[key+"_returned"] = min(countAnySlice(raw), toolResultArrayLimit)
			keysLeft--
		}

		if keysLeft > 0 {
			if _, ok := v["rows"]; ok {
				addArrayPreview("rows", v["rows"])
			}
		}
		if keysLeft > 0 {
			if _, ok := v["results"]; ok {
				addArrayPreview("results", v["results"])
			}
		}
		if keysLeft > 0 {
			if _, ok := v["evidence"]; ok {
				addArrayPreview("evidence", v["evidence"])
			}
		}
		if keysLeft > 0 {
			if _, ok := v["messages"]; ok {
				addArrayPreview("messages", v["messages"])
			}
		}

		for key, raw := range v {
			switch key {
			case "rows", "results", "evidence", "messages":
				continue
			}
			if keysLeft <= 0 {
				out["...truncated"] = true
				break
			}
			out[key] = compactToolResultPayload(raw)
			keysLeft--
		}
		return out
	case []any:
		return compactToolResultArray(v)
	case string:
		return compactToolResultText(v)
	default:
		if v == nil {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return compactToolResultText(string(b))
	}
}

func compactToolResultArray(value any) any {
	arr, ok := value.([]any)
	if !ok {
		return compactToolResultPayload(value)
	}
	limit := min(len(arr), toolResultArrayLimit)
	out := make([]any, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, compactToolResultPayload(arr[i]))
	}
	if len(arr) > limit {
		out = append(out, map[string]any{
			"...truncated": true,
			"total":        len(arr),
			"returned":     limit,
		})
	}
	return out
}

func compactToolResultText(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return truncateForToolPreview(v, toolResultTextLimit)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return truncateForToolPreview(fmt.Sprintf("%v", v), toolResultTextLimit)
		}
		return truncateForToolPreview(string(b), toolResultTextLimit)
	}
}

func countAnySlice(raw any) int {
	if arr, ok := raw.([]string); ok {
		return len(arr)
	}
	if arr, ok := raw.([]any); ok {
		return len(arr)
	}
	if raw == nil {
		return 0
	}
	rv := reflect.ValueOf(raw)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return rv.Len()
	}
	return 0
}

func truncateForToolPreview(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "... [truncated]"
}

func shouldSuppressToolUI(toolName string, out tools.Output) bool {
	toolName = strings.TrimSpace(strings.ToLower(toolName))

	if queryOutputFlag(out.QueryMetadata, "ui_hidden", "hide_from_ui", "suppress_ui", "suppress_from_ui", "ui_suppress", "status_check_ui_hidden") {
		return true
	}
	if out.ResultFormat == tools.ResultFormatJSON {
		if m, ok := out.Result.(map[string]any); ok {
			if queryOutputFlag(m, "ui_hidden", "hide_from_ui", "suppress_ui", "suppress_from_ui", "ui_suppress") {
				return true
			}
			if strings.EqualFold(toolName, "get_query_results") {
				if queryOutputFlag(m, "retryable") {
					return true
				}
				if status := strings.TrimSpace(strings.ToLower(firstStringMapValue(m, "status", "state", "phase"))); status != "" {
					switch status {
					case "profiling", "running", "submitted", "pending", "queued", "unknown", "in_progress", "processing":
						return true
					}
				}
			}
			if strings.EqualFold(toolName, "check_query_status") {
				return true
			}
		}
	}

	if tools.IsAsyncQueryRunTool(toolName) {
		if out.ResultFormat != tools.ResultFormatJSON {
			return false
		}
		m, ok := out.Result.(map[string]any)
		if !ok {
			return false
		}
		v, ok := m["preflight_failed"].(bool)
		return ok && v && extractAsyncQueryJobID(out) == ""
	}

	if !isAsyncQueryStatusTool(toolName) {
		return false
	}
	return isPendingQueryJobState(out)
}

func setExecContextQueryJobs(execCtx *tools.ExecContext, queryJobs any) {
	if execCtx == nil || queryJobs == nil {
		return
	}
	rv := reflect.ValueOf(execCtx)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	structVal := rv.Elem()
	if !structVal.IsValid() || structVal.Kind() != reflect.Struct {
		return
	}
	for _, name := range []string{"Jobs", "QueryJobs", "QueryJobRegistry", "JobsRegistry", "AsyncQueryJobs"} {
		if setStructFieldByName(structVal, name, queryJobs) {
			return
		}
	}
}

func setStructFieldByName(structVal reflect.Value, fieldName string, value any) bool {
	field := structVal.FieldByName(fieldName)
	if !field.IsValid() || !field.CanSet() {
		return false
	}
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return false
	}
	if v.Type().AssignableTo(field.Type()) {
		field.Set(v)
		return true
	}
	if field.Kind() == reflect.Interface && v.Type().Implements(field.Type()) {
		field.Set(v)
		return true
	}
	if v.Type().ConvertibleTo(field.Type()) {
		field.Set(v.Convert(field.Type()))
		return true
	}
	return false
}

func resolveToolArtifactID(call types.ToolCall, out tools.Output) string {
	return resolveToolArtifactIDForThread(call, out, "")
}

func resolveToolArtifactIDForThread(call types.ToolCall, out tools.Output, fallbackThreadID string) string {
	if id := firstStringMapValue(out.QueryMetadata, "result_artifact_id", "resultArtifactID", "async_artifact_id", "asyncArtifactID"); id != "" {
		return id
	}
	if id := firstStringMapValue(out.ResultMetadata, "result_artifact_id", "resultArtifactID", "async_artifact_id", "asyncArtifactID"); id != "" {
		return id
	}
	if m, ok := out.Result.(map[string]any); ok {
		if id := firstStringMapValue(m, "result_artifact_id", "resultArtifactID"); id != "" {
			return id
		}
		if nested, ok := m["artifact"].(map[string]any); ok {
			if id := firstStringMapValue(nested, "result_artifact_id", "resultArtifactID"); id != "" {
				return id
			}
		}
	}
	if !isAsyncQueryTool(call.Name) {
		return ""
	}
	if id := firstStringMapValue(out.QueryMetadata, "artifact_id", "artifactId"); id != "" {
		return id
	}
	if id := firstStringMapValue(out.ResultMetadata, "artifact_id", "artifactId"); id != "" {
		return id
	}
	jobID := extractAsyncQueryJobID(out)
	if jobID == "" {
		return ""
	}
	threadID := firstStringMapValue(out.QueryMetadata, "thread_id", "threadId")
	if threadID == "" {
		threadID = firstStringMapValue(anyMap(out.Result), "thread_id", "threadId")
	}
	if threadID == "" {
		threadID = firstStringMapValue(out.ResultMetadata, "thread_id", "threadId")
	}
	if threadID == "" {
		threadID = strings.TrimSpace(fallbackThreadID)
	}
	if canonicalID := tools.DeterministicAsyncArtifactID(threadID, jobID); canonicalID != "" {
		return canonicalID
	}
	// Compatibility for incomplete legacy status payloads. This remains a
	// separate run-neutral placeholder and cannot occupy another thread's
	// canonical dataset slot.
	return deterministicAsyncArtifactID(call.Name, jobID)
}

// selectAsyncQueryJobID reads only app-managed job identity keys. Native
// provider ids (statement_id, query_id, provider_job_id, ...) are deliberately
// excluded. A broken zero/native alias cannot mask a valid qj-* in a later
// approved location, while conflicting valid qj-* claims fail closed.
func selectAsyncQueryJobID(out tools.Output) (string, bool) {
	keys := []string{"query_job_id", "queryJobID", "job_id", "jobId", "async_job_id", "asyncJobID"}
	candidates := make([]string, 0, 8)
	appendCandidates := func(payload map[string]any, candidateKeys ...string) {
		for _, key := range candidateKeys {
			if value := strings.TrimSpace(fmt.Sprint(payload[key])); value != "" && value != "<nil>" {
				candidates = append(candidates, value)
			}
		}
	}
	appendCandidates(out.QueryMetadata, keys...)
	appendCandidates(out.ResultMetadata, keys...)
	if result := anyMap(out.Result); result != nil {
		appendCandidates(result, keys...)
		if nested := anyMap(result["job"]); nested != nil {
			appendCandidates(nested, "job_id", "jobId", "id")
		}
	}
	valid := make(map[string]struct{}, 2)
	for _, candidate := range candidates {
		if validManagedQueryJobID(candidate) {
			valid[candidate] = struct{}{}
		}
	}
	if len(valid) > 1 {
		return "", true
	}
	for candidate := range valid {
		return candidate, false
	}
	if len(candidates) > 0 {
		return candidates[0], false
	}
	return "", false
}

func extractAsyncQueryJobID(out tools.Output) string {
	id, conflict := selectAsyncQueryJobID(out)
	if conflict {
		return ""
	}
	return id
}

// validManagedQueryJobID accepts only app-owned durable query identities. In
// particular, blank/native provider ids and the all-zero sentinels emitted by
// a few broken adapter paths must never become completion-fault keys.
func validManagedQueryJobID(id string) bool {
	return jobs.IsValidManagedQueryJobID(id)
}

// normalizeMissingDurableQueryIdentityOutput removes an unusable identity and
// gives the model an actionable, provider-neutral recovery contract. The
// retry-target classifier below may then attach either the already-verified
// obligation or a deterministic local failed-attempt obligation.
func normalizeMissingDurableQueryIdentityOutput(out *tools.Output) {
	if out == nil {
		return
	}
	const failure = "The query tool did not return a usable durable job identity."
	const instructions = "Preserve the task and retry through the durable managed query launch/status flow. If the query cannot be launched as written, profile or rewrite it and use the active query_obligation_id returned for this run. Do not abandon the task."
	out.OK = false
	out.ResultFormat = tools.ResultFormatJSON
	out.Result = map[string]any{
		"error":                            failure,
		"message":                          failure,
		"instructions":                     instructions,
		"alternative_plan":                 instructions,
		"durable_identity_contract_failed": true,
		"rewrite_required":                 true,
		"retryable":                        true,
	}
	out.QueryMetadata = map[string]any{
		"durable_identity_contract_failed": true,
		"rewrite_required":                 true,
		"retryable":                        true,
	}
	out.ResultMetadata = nil
	out.Artifact = nil
}

// handleAsyncQueryMissingDurableIdentity preserves the distinction between a
// verified retry, an explicit-but-unverified target, and a genuinely
// targetless attempt. Only the last case creates a deterministic local
// recovery obligation.
func (m *Manager) handleAsyncQueryMissingDurableIdentity(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	toolName string,
	out *tools.Output,
) {
	disposition := m.classifyExplicitQueryRetryTarget(ctx, run.ID, call, out)
	switch disposition.State {
	case explicitQueryRetryTargetVerified:
		// A ledger-verified target heals protocol/preflight faults only. It
		// must not erase unrelated failures for real durable jobs.
		m.clearQueryDependencyFaultFor(run.ID, queryDependencyProtocolFaultKey(call))
		m.rearmQueryProtocolFaultRecovery(run.ID)
		log.Printf("run %s: %s returned no usable durable identity; preserved existing query obligation %s", run.ID, toolName, disposition.ObligationID)
	case explicitQueryRetryTargetUnverified:
		m.setQueryDependencyFaultFor(
			run.ID,
			queryDependencyProtocolFaultKey(call),
			errors.New("query retry target was not a verified active obligation for this run"),
		)
		log.Printf("run %s: %s rejected an explicit query retry target that was not a verified active obligation", run.ID, toolName)
	default:
		attemptJobID, obligationErr := m.persistFailedQueryAttemptObligation(ctx, thread, run, call, toolName, out)
		faultKey := queryDependencyProtocolFaultKey(call)
		if obligationErr != nil && strings.TrimSpace(attemptJobID) != "" {
			// Use a job-scoped fault only when the synthetic record itself is
			// durably present and current-run owned. A merely computed id is not
			// evidence and must remain a healable protocol fault.
			if represented, verifyErr := m.queryJobDurablyOwnedByRun(ctx, run.ID, attemptJobID); represented {
				faultKey = "job:" + strings.TrimSpace(attemptJobID)
			} else if verifyErr != nil {
				obligationErr = fmt.Errorf("%w (verify failed-attempt identity: %v)", obligationErr, verifyErr)
			}
		}
		if obligationErr == nil {
			m.clearQueryDependencyFaultFor(run.ID, faultKey)
			m.rearmQueryProtocolFaultRecovery(run.ID)
		} else {
			m.setQueryDependencyFaultFor(run.ID, faultKey, obligationErr)
			log.Printf("run %s: failed to persist query-attempt obligation for %s: %v", run.ID, toolName, obligationErr)
		}
	}
}

func extractAsyncQueryReplacesJobID(out tools.Output) string {
	if id := firstStringMapValue(out.QueryMetadata, "replaces_job_id", "replacesJobId", "replacesJobID"); id != "" {
		return id
	}
	m, ok := out.Result.(map[string]any)
	if !ok {
		return ""
	}
	return firstStringMapValue(m, "replaces_job_id", "replacesJobId", "replacesJobID")
}

func queryOutputRequiresReplacementWait(out tools.Output) bool {
	if value, ok := out.QueryMetadata["replacement_wait_required"].(bool); ok && value {
		return true
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		return false
	}
	value, _ := result["replacement_wait_required"].(bool)
	return value
}

func (m *Manager) queryJobDurablyOwnedByRun(ctx context.Context, runID, jobID string) (bool, error) {
	if m == nil || m.longHorizon == nil || strings.TrimSpace(runID) == "" || !validManagedQueryJobID(jobID) {
		return false, nil
	}
	record, found, err := m.longHorizon.GetQueryJob(ctx, strings.TrimSpace(jobID))
	if err != nil {
		return false, err
	}
	if !found || strings.TrimSpace(record.Owner.RunID) != strings.TrimSpace(runID) {
		return false, nil
	}
	// A CAS-losing replacement reservation is intentionally excluded from the
	// dependency ledger: no provider submission or native handle exists, and a
	// winner owns the obligation. Treating the loser as evidence would create a
	// job:<loser> fault that reconciliation can never clear.
	concurrentLoser := record.CancelOutcome == jobs.CancelOutcomeBeforeSubmit && !record.SubmitDispatched &&
		record.Handle.IsZero() && strings.TrimSpace(record.ReplacesJobID) != ""
	return !concurrentLoser, nil
}

func (m *Manager) queryJobDurablyObservableForThread(ctx context.Context, threadID, jobID string) (bool, error) {
	if m == nil || m.longHorizon == nil || strings.TrimSpace(threadID) == "" || !validManagedQueryJobID(jobID) {
		return false, nil
	}
	record, found, err := m.longHorizon.GetQueryJob(ctx, strings.TrimSpace(jobID))
	if err != nil || !found || strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(threadID) {
		return false, err
	}
	if record.Status == jobs.JobStatusCompleted && record.ValidationState != jobs.ValidationValidated {
		return true, nil
	}
	switch record.Status {
	case jobs.JobStatusReserved, jobs.JobStatusProfiling, jobs.JobStatusSubmitted,
		jobs.JobStatusSubmissionUnknown, jobs.JobStatusRunning,
		jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return true, nil
	default:
		return false, nil
	}
}

func (m *Manager) handleAsyncQueryReplacementWait(ctx context.Context, threadID, runID string, out tools.Output) {
	jobID := extractAsyncQueryJobID(out)
	if !validManagedQueryJobID(jobID) {
		m.setQueryDependencyFaultFor(runID, "protocol:replacement-wait-missing-job", errors.New("query replacement wait did not include a durable job identity"))
		return
	}
	if err := m.persistRunQueryWaitDependency(ctx, threadID, runID, jobID); err != nil {
		if represented, verifyErr := m.queryJobDurablyObservableForThread(ctx, threadID, jobID); represented {
			m.setQueryDependencyFaultFor(runID, "job:"+jobID, err)
		} else {
			m.setQueryDependencyFaultFor(runID, "protocol:replacement-wait-identity", fmt.Errorf("replacement-wait identity has no durable same-thread evidence: %w (verification: %v)", err, verifyErr))
		}
		log.Printf("run %s: failed to persist replacement-wait dependency %s: %v", runID, jobID, err)
		return
	}
	m.setQueryDependencyFaultFor(runID, "job:"+jobID, nil)
	m.rearmQueryProtocolFaultRecovery(runID)
}

func queryDependencyProtocolFaultKey(call types.ToolCall) string {
	material := strings.TrimSpace(call.ID)
	if material == "" {
		material = strings.TrimSpace(call.ArgsText)
	}
	if material == "" && call.Args != nil {
		if encoded, err := json.Marshal(call.Args); err == nil {
			material = string(encoded)
		}
	}
	digest := sha256.Sum256([]byte(material))
	return fmt.Sprintf("protocol:%x", digest[:12])
}

func toolCallWithExecutedArgs(call types.ToolCall, args json.RawMessage) types.ToolCall {
	call.ArgsText = strings.TrimSpace(string(args))
	var decoded map[string]any
	if json.Unmarshal(args, &decoded) == nil {
		call.Args = decoded
	} else {
		call.Args = nil
	}
	return call
}

// canonicalizeRunQueryRetryCall rewrites a model-supplied retry request to the
// exact ledger-verified obligation. With an empty obligation it removes stale
// retry identity so the request becomes a fresh durable launch. Provider and
// app job ids never survive this boundary.
func canonicalizeRunQueryRetryCall(call types.ToolCall, obligationID string) (types.ToolCall, json.RawMessage, error) {
	args := cloneStringAnyMap(call.Args)
	if args == nil && strings.TrimSpace(call.ArgsText) != "" {
		if err := json.Unmarshal([]byte(call.ArgsText), &args); err != nil {
			return call, nil, err
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	clean, ok := stripQueryRetryIdentityFields(args, true).(map[string]any)
	if !ok {
		return call, nil, errors.New("query arguments are not a JSON object")
	}
	obligationID = strings.TrimSpace(obligationID)
	if obligationID != "" {
		if err := store.ValidateQueryObligationID(obligationID); err != nil {
			return call, nil, err
		}
		clean["query_obligation_id"] = obligationID
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return call, nil, err
	}
	raw := json.RawMessage(encoded)
	return toolCallWithExecutedArgs(call, raw), raw, nil
}

type queryRetryTargetField struct {
	Present bool
	Invalid bool
	Value   string
}

type queryRetryTargetRequest struct {
	Obligation                 queryRetryTargetField
	LegacyJob                  queryRetryTargetField
	UnsupportedIdentityPresent bool
}

type explicitQueryRetryTargetState uint8

const (
	explicitQueryRetryTargetNone explicitQueryRetryTargetState = iota
	explicitQueryRetryTargetVerified
	explicitQueryRetryTargetFresh
	explicitQueryRetryTargetUnverified
)

type explicitQueryRetryTargetDisposition struct {
	State        explicitQueryRetryTargetState
	ObligationID string
}

// queryRetryTargetFromCallArgs preserves key presence as well as value. An
// explicit null, blank, non-string, conflicting alias, or malformed qob is an
// unverified retry request; it is not equivalent to a targetless first attempt.
func queryRetryTargetFromCallArgs(call types.ToolCall) queryRetryTargetRequest {
	var request queryRetryTargetRequest
	merge := func(field *queryRetryTargetField, raw any) {
		field.Present = true
		value, ok := raw.(string)
		value = strings.TrimSpace(value)
		if !ok || value == "" {
			field.Invalid = true
			return
		}
		if field.Value != "" && field.Value != value {
			field.Invalid = true
			return
		}
		field.Value = value
	}
	var read func(map[string]any, bool)
	var readNested func(any)
	readNested = func(value any) {
		switch nested := value.(type) {
		case map[string]any:
			read(nested, false)
		case []any:
			for _, item := range nested {
				readNested(item)
			}
		case []map[string]any:
			for _, item := range nested {
				read(item, false)
			}
		}
	}
	read = func(args map[string]any, topLevel bool) {
		for key, raw := range args {
			normalized := normalizedQueryRetryIdentityKey(key)
			switch {
			case topLevel && key == "query_obligation_id":
				merge(&request.Obligation, raw)
			case topLevel && key == "replaces_job_id":
				merge(&request.LegacyJob, raw)
			case isReservedRunQueryArgumentIdentityKey(normalized):
				// A retry identity hidden under an object or supplied through a
				// decode-only/internal alias is still an explicit retry attempt,
				// but it can never be accepted as an authoritative target.
				request.UnsupportedIdentityPresent = true
			}
			readNested(raw)
		}
	}
	if call.Args != nil {
		read(call.Args, true)
	}
	if strings.TrimSpace(call.ArgsText) != "" {
		var decoded map[string]any
		if json.Unmarshal([]byte(call.ArgsText), &decoded) == nil {
			read(decoded, true)
		} else {
			// A streamed tool call can end with truncated JSON. If the raw bytes
			// already contain a completed reserved identity key, it was still an
			// explicit retry attempt; never downgrade it to a targetless launch
			// and synthesize unrelated work merely because decoding failed.
			for _, match := range queryArgumentKeyTextRE.FindAllStringSubmatch(call.ArgsText, -1) {
				if len(match) > 1 && isReservedRunQueryArgumentIdentityKey(normalizedQueryRetryIdentityKey(match[1])) {
					request.UnsupportedIdentityPresent = true
					break
				}
			}
		}
	}
	return request
}

func isReservedQueryRetryArgumentKey(normalized string) bool {
	switch normalized {
	case "queryobligationid", "replacesjobid", "replacementjobid",
		"replacementtargetjobid", "attemptedretrytarget", "attemptedreplacesjobid",
		"attemptedreplacementjobid", "candidatejobids", "replacementcandidatejobids",
		"resolvedreplacementjobid", "replacementtarget", "retrytarget", "targetjobid":
		return true
	default:
		return false
	}
}

func isReservedRunQueryArgumentIdentityKey(normalized string) bool {
	if isReservedQueryRetryArgumentKey(normalized) {
		return true
	}
	switch normalized {
	case "jobid", "providerjobid", "warehousejobid", "statementid", "queryid",
		"ownertoken", "runid", "threadid", "profilehandle", "handle":
		return true
	default:
		return false
	}
}

func normalizedQueryRetryIdentityKey(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func shouldStripQueryRetryIdentityKey(key string, unverified bool) bool {
	normalized := normalizedQueryRetryIdentityKey(key)
	if isReservedQueryRetryArgumentKey(normalized) {
		return true
	}
	switch normalized {
	case "failedrequest":
		return true
	case "jobid", "ownertoken", "runid", "threadid", "providerjobid",
		"statementid", "queryid", "handle":
		return unverified
	default:
		return false
	}
}

func stripQueryRetryIdentityFields(value any, unverified bool) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			if shouldStripQueryRetryIdentityKey(key, unverified) {
				continue
			}
			clean[key] = stripQueryRetryIdentityFields(nested, unverified)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, nested := range typed {
			clean[i] = stripQueryRetryIdentityFields(nested, unverified)
		}
		return clean
	case []map[string]any:
		clean := make([]map[string]any, len(typed))
		for i, nested := range typed {
			if item, ok := stripQueryRetryIdentityFields(nested, unverified).(map[string]any); ok {
				clean[i] = item
			}
		}
		return clean
	default:
		return value
	}
}

func stripQueryRegistryRunScopedFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			normalized := normalizedQueryRetryIdentityKey(key)
			if shouldStripQueryRetryIdentityKey(key, false) || normalized == "runid" || normalized == "queryobligationmanaged" {
				continue
			}
			clean[key] = stripQueryRegistryRunScopedFields(nested)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for i, nested := range typed {
			clean[i] = stripQueryRegistryRunScopedFields(nested)
		}
		return clean
	case []map[string]any:
		clean := make([]map[string]any, len(typed))
		for i, nested := range typed {
			if item, ok := stripQueryRegistryRunScopedFields(nested).(map[string]any); ok {
				clean[i] = item
			}
		}
		return clean
	default:
		return value
	}
}

func sanitizeUnverifiedQueryRetryOutput(out *tools.Output) {
	if out == nil {
		return
	}
	const failure = "The requested query retry target could not be verified as an active obligation for this run."
	const instructions = "Refresh this run's query status, then retry only with the exact query_obligation_id returned for an active obligation. If no active obligation exists, correct the original query and submit a new first attempt. Do not abandon the task."
	flags := map[string]any{
		"replacement_preflight_failed":         true,
		"replacement_wait_required":            true,
		"replacement_preflight_retry_required": true,
		"rewrite_required":                     true,
	}
	out.OK = false
	out.ResultFormat = tools.ResultFormatJSON
	out.Result = map[string]any{
		"error":                                failure,
		"message":                              failure,
		"instructions":                         instructions,
		"alternative_plan":                     instructions,
		"replacement_preflight_failed":         true,
		"replacement_wait_required":            true,
		"replacement_preflight_retry_required": true,
		"rewrite_required":                     true,
	}
	out.QueryMetadata = flags
	out.ResultMetadata = nil
	out.Artifact = nil
}

func preserveVerifiedQueryRetryOutput(out *tools.Output, obligationID string) {
	if out == nil {
		return
	}
	if clean, ok := stripQueryRetryIdentityFields(out.QueryMetadata, false).(map[string]any); ok {
		out.QueryMetadata = clean
	} else {
		out.QueryMetadata = map[string]any{}
	}
	out.QueryMetadata["query_obligation_id"] = obligationID
	out.QueryMetadata["replacement_wait_required"] = true
	result, ok := stripQueryRetryIdentityFields(out.Result, false).(map[string]any)
	if !ok {
		result = map[string]any{"error": toolErrorMessageFromOutput(*out)}
	}
	result["query_obligation_id"] = obligationID
	result["replacement_wait_required"] = true
	if strings.TrimSpace(stringMapValue(result, "instructions")) == "" {
		result["instructions"] = "This retry failed before any durable or provider work; its target obligation is still active. Fix the reported condition, then retry with this exact query_obligation_id. Do not start unrelated query work for this task."
	}
	out.Result = result
	if clean, ok := stripQueryRetryIdentityFields(out.ResultMetadata, false).(map[string]any); ok {
		out.ResultMetadata = clean
	}
	if out.Artifact != nil {
		if clean, ok := stripQueryRetryIdentityFields(out.Artifact.Metadata, false).(map[string]any); ok {
			out.Artifact.Metadata = clean
		}
	}
}

// classifyExplicitQueryRetryTarget validates every supplied identity against
// the current run's durable ledger. A stale single handle is canonicalized to
// the run's sole active obligation; if the run has no unfinished obligation it
// is classified as a fresh launch. Ambiguous multi-obligation requests and
// conflicting paired identities still fail closed.
func (m *Manager) classifyExplicitQueryRetryTarget(ctx context.Context, runID string, call types.ToolCall, out *tools.Output) explicitQueryRetryTargetDisposition {
	request := queryRetryTargetFromCallArgs(call)
	if !request.Obligation.Present && !request.LegacyJob.Present && !request.UnsupportedIdentityPresent {
		return explicitQueryRetryTargetDisposition{State: explicitQueryRetryTargetNone}
	}
	unverified := func() explicitQueryRetryTargetDisposition {
		sanitizeUnverifiedQueryRetryOutput(out)
		return explicitQueryRetryTargetDisposition{State: explicitQueryRetryTargetUnverified}
	}
	if out == nil || m == nil || m.longHorizon == nil || strings.TrimSpace(runID) == "" || request.UnsupportedIdentityPresent {
		return unverified()
	}
	dependencies, err := m.longHorizon.ListRunQueryDependencies(ctx, strings.TrimSpace(runID))
	if err != nil {
		return unverified()
	}
	rowObligations := make([]string, len(dependencies))
	for i, dependency := range dependencies {
		obligationID := strings.TrimSpace(dependency.ObligationID)
		if obligationID == "" {
			obligationID, err = store.DeriveQueryObligationID(ctx, m.longHorizon, runID, dependency.JobID)
			if err != nil {
				return unverified()
			}
		}
		if store.ValidateQueryObligationID(obligationID) != nil {
			return unverified()
		}
		rowObligations[i] = obligationID
	}
	activeObligations := map[string]struct{}{}
	for i, dependency := range dependencies {
		if !dependency.RequiredForCompletion || dependency.Detached ||
			strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) {
			continue
		}
		activeObligations[rowObligations[i]] = struct{}{}
	}
	canonicalFallback := func() explicitQueryRetryTargetDisposition {
		switch len(activeObligations) {
		case 0:
			return explicitQueryRetryTargetDisposition{State: explicitQueryRetryTargetFresh}
		case 1:
			for obligationID := range activeObligations {
				preserveVerifiedQueryRetryOutput(out, obligationID)
				return explicitQueryRetryTargetDisposition{State: explicitQueryRetryTargetVerified, ObligationID: obligationID}
			}
		}
		return unverified()
	}
	// Two independently supplied target fields are an assertion that both are
	// authoritative. Never guess through a malformed or conflicting pair.
	if request.Obligation.Present && request.LegacyJob.Present &&
		(request.Obligation.Invalid || request.LegacyJob.Invalid ||
			store.ValidateQueryObligationID(request.Obligation.Value) != nil) {
		return unverified()
	}
	if request.Obligation.Invalid || request.LegacyJob.Invalid ||
		(request.Obligation.Present && store.ValidateQueryObligationID(request.Obligation.Value) != nil) {
		return canonicalFallback()
	}
	resolveObligation := ""
	if request.Obligation.Present {
		for _, obligationID := range rowObligations {
			if obligationID == request.Obligation.Value {
				resolveObligation = obligationID
				break
			}
		}
	}
	resolveLegacy := ""
	if request.LegacyJob.Present {
		for i, dependency := range dependencies {
			if strings.TrimSpace(dependency.JobID) == request.LegacyJob.Value {
				resolveLegacy = rowObligations[i]
				break
			}
		}
	}
	verified := resolveObligation
	if verified == "" {
		verified = resolveLegacy
	}
	if request.Obligation.Present && request.LegacyJob.Present &&
		(resolveObligation == "" || resolveLegacy == "" || resolveObligation != resolveLegacy) {
		return unverified()
	}
	if verified == "" {
		return canonicalFallback()
	}
	active := false
	for i, dependency := range dependencies {
		if !dependency.RequiredForCompletion || dependency.Detached ||
			strings.EqualFold(strings.TrimSpace(dependency.ValidationState), store.QueryValidationValidated) {
			continue
		}
		if rowObligations[i] == verified {
			active = true
			break
		}
	}
	if !active {
		return canonicalFallback()
	}
	preserveVerifiedQueryRetryOutput(out, verified)
	return explicitQueryRetryTargetDisposition{State: explicitQueryRetryTargetVerified, ObligationID: verified}
}

// preservedExplicitQueryRetryTarget remains as the narrow compatibility
// predicate used by focused tests and older callers. New orchestration must
// consume the full classification so explicit-unverified is never
// confused with targetless.
func (m *Manager) preservedExplicitQueryRetryTarget(ctx context.Context, runID string, call types.ToolCall, out *tools.Output) (string, bool) {
	disposition := m.classifyExplicitQueryRetryTarget(ctx, runID, call, out)
	return disposition.ObligationID, disposition.State == explicitQueryRetryTargetVerified
}

// persistFailedQueryAttemptObligation turns a pre-submit/profile/adapter
// failure into durable unfinished work. That prevents a model from treating
// one failed attempt as permission to finalize and abandon the user's task.
// The synthetic job is app-local (SubmitDispatched=false), deterministic per
// tool call, and explicitly replaceable; it never represents remote execution.
func (m *Manager) persistFailedQueryAttemptObligation(
	ctx context.Context,
	thread store.Thread,
	run store.Run,
	call types.ToolCall,
	toolName string,
	out *tools.Output,
) (string, error) {
	if m == nil || out == nil {
		return "", errors.New("durable query dependency repository is not configured")
	}
	if m.longHorizon == nil {
		// Long-horizon orchestration is an optional Manager capability. The
		// production wiring always supplies it and therefore retains the
		// fail-safe replacement obligation; lightweight harnesses without the
		// capability preserve the historical non-durable tool behavior.
		return "", nil
	}
	attemptedReplacementJobID := extractAsyncQueryReplacesJobID(*out)
	if attemptedReplacementJobID == "" {
		if result, ok := out.Result.(map[string]any); ok {
			attemptedReplacementJobID = firstStringMapValue(result, "attempted_replaces_job_id", "attemptedReplacesJobId", "attemptedReplacesJobID")
		}
	}
	callIdentity := strings.TrimSpace(call.ID)
	if callIdentity == "" {
		callIdentity = strings.TrimSpace(call.ArgsText)
		if callIdentity == "" {
			if encoded, err := json.Marshal(call.Args); err == nil {
				callIdentity = string(encoded)
			}
		}
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(run.ID) + "\x00" + callIdentity + "\x00" + strings.TrimSpace(toolName)))
	jobID := fmt.Sprintf("qj-attempt-%x", digest[:12])
	obligationID, obligationErr := store.QueryObligationIDForRoot(run.ID, jobID)
	if obligationErr != nil {
		return "", obligationErr
	}

	record, found, err := m.longHorizon.GetQueryJob(ctx, jobID)
	if err != nil {
		return jobID, err
	}
	if !found {
		ownerToken, tokenErr := jobs.GenerateOwnerToken()
		if tokenErr != nil {
			return jobID, tokenErr
		}
		now := time.Now().UTC()
		completedAt := now
		provider := strings.TrimSpace(strings.ToLower(tools.DatasourceForToolName(toolName)))
		errorMessage := "query attempt requires a corrected or alternative plan"
		if result, ok := out.Result.(map[string]any); ok {
			if detail := firstStringMapValue(result, "error", "message", "detail"); detail != "" {
				errorMessage = detail
			}
		}
		sql := strings.TrimSpace(stringMapValue(call.Args, "sql", "query"))
		if sql == "" && strings.TrimSpace(call.ArgsText) != "" {
			var decoded map[string]any
			if json.Unmarshal([]byte(call.ArgsText), &decoded) == nil {
				sql = strings.TrimSpace(stringMapValue(decoded, "sql", "query"))
			}
		}
		record = jobs.JobRecord{
			ID:                   jobID,
			Provider:             provider,
			SQL:                  sql,
			SQLPreview:           truncateForToolPreview(sql, 500),
			Status:               jobs.JobStatusFailed,
			MonitorHealth:        jobs.MonitorHealthOK,
			PolicyHealth:         jobs.PolicyHealthOK,
			MaterializationState: jobs.MaterializationNone,
			Owner:                jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: ownerToken},
			IdempotencyKey:       "attempt_" + fmt.Sprintf("%x", digest[:]),
			KeepOnRunCompletion:  true,
			SubmittedAt:          now,
			CompletedAt:          &completedAt,
			LastAccess:           now,
			Error:                errorMessage,
			ValidationState:      jobs.ValidationRejected,
			ValidationDetail:     errorMessage,
			Metadata: map[string]any{
				"local_query_attempt": true,
				"remote_submitted":    false,
				"tool_name":           toolName,
				"tool_call_id":        strings.TrimSpace(call.ID),
				"rewrite_required":    true,
				"query_obligation_id": obligationID,
			},
		}
		if attemptedReplacementJobID != "" {
			record.Metadata["attempted_replaces_job_id"] = attemptedReplacementJobID
		}
		if _, err = m.longHorizon.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			if !errors.Is(err, jobs.ErrRevisionConflict) {
				return jobID, err
			}
			record, found, err = m.longHorizon.GetQueryJob(ctx, jobID)
			if err != nil || !found {
				return jobID, firstNonNilError(err, jobs.ErrRevisionConflict)
			}
		}
	}
	if strings.TrimSpace(record.Owner.ThreadID) != strings.TrimSpace(thread.ID) || strings.TrimSpace(record.Owner.RunID) != strings.TrimSpace(run.ID) {
		return jobID, jobs.ErrOwnershipMismatch
	}
	// Stamp the deterministic repair handle as soon as the root record itself
	// is durable. If dependency insertion fails next, the ledger backstop can
	// auto-bind this root and the model still has the exact qob needed to retry.
	stampFailedQueryAttemptIdentity(out, thread, record, obligationID)
	now := time.Now().UTC()
	if _, err := m.longHorizon.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID:                 run.ID,
		JobID:                 jobID,
		RequiredForCompletion: true,
		ValidationState:       store.QueryValidationRejected,
		ObligationID:          obligationID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}); err != nil {
		return jobID, err
	}
	return jobID, nil
}

func stampFailedQueryAttemptIdentity(out *tools.Output, thread store.Thread, record jobs.JobRecord, obligationID string) {
	if out == nil {
		return
	}
	jobID := strings.TrimSpace(record.ID)
	if out.QueryMetadata == nil {
		out.QueryMetadata = map[string]any{}
	}
	delete(out.QueryMetadata, "replaces_job_id")
	delete(out.QueryMetadata, "replacesJobId")
	delete(out.QueryMetadata, "replacesJobID")
	delete(out.QueryMetadata, "attempted_replaces_job_id")
	delete(out.QueryMetadata, "attemptedReplacesJobId")
	delete(out.QueryMetadata, "attemptedReplacesJobID")
	out.QueryMetadata["job_id"] = jobID
	out.QueryMetadata["owner_token"] = record.Owner.Handle
	out.QueryMetadata["thread_id"] = thread.ID
	out.QueryMetadata["provider"] = record.Provider
	out.QueryMetadata["status"] = string(record.Status)
	out.QueryMetadata["validation_state"] = string(record.ValidationState)
	out.QueryMetadata["query_obligation_id"] = obligationID
	if result, ok := out.Result.(map[string]any); ok {
		delete(result, "replaces_job_id")
		delete(result, "replacesJobId")
		delete(result, "replacesJobID")
		delete(result, "attempted_replaces_job_id")
		delete(result, "attemptedReplacesJobId")
		delete(result, "attemptedReplacesJobID")
		result["job_id"] = jobID
		result["owner_token"] = record.Owner.Handle
		result["status"] = string(record.Status)
		result["validation_state"] = string(record.ValidationState)
		result["replacement_required"] = true
		result["query_obligation_id"] = obligationID
		result["instructions"] = fmt.Sprintf("Correct or redesign the query, then call the run-query tool with query_obligation_id=%s. Do not abandon the task.", obligationID)
	}
}

func firstNonNilError(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func normalizeProviderJobMetadata(payload map[string]any) {
	if payload == nil {
		return
	}

	if strings.TrimSpace(firstStringMapValue(payload, "provider_job_id", "providerJobID")) == "" {
		if id := firstStringMapValue(
			payload,
			"warehouse_job_id",
			"warehouseJobID",
			"statement_id",
			"statementId",
			"query_id",
			"queryId",
			"native_job_id",
			"nativeJobID",
		); id != "" {
			payload["provider_job_id"] = id
		}
	}

	if strings.TrimSpace(firstStringMapValue(payload, "provider_job_url", "providerJobURL")) == "" {
		if u := firstStringMapValue(
			payload,
			"url",
			"job_url",
			"jobUrl",
			"query_url",
			"queryUrl",
			"query_history_url",
			"queryHistoryURL",
			"console_url",
			"consoleUrl",
			"warehouse_job_url",
			"warehouseJobURL",
			"query_url",
			"queryUrl",
			"next_uri",
			"nextUri",
			"next_uri_url",
			"nextUriURL",
		); u != "" {
			payload["provider_job_url"] = u
		}
	}
}

func deterministicAsyncArtifactID(toolName string, jobID string) string {
	t := strings.TrimSpace(strings.ToLower(toolName))
	j := strings.TrimSpace(jobID)
	if t == "" || j == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(t + ":" + j))
	return fmt.Sprintf("artifact_async_%x", sum[:12])
}

func isAsyncQueryTool(toolName string) bool {
	name := strings.TrimSpace(strings.ToLower(toolName))
	if isAsyncQueryStatusTool(name) {
		return true
	}
	if isAsyncQueryResultsTool(name) {
		return true
	}
	if tools.IsAsyncQueryRunTool(name) {
		return true
	}
	switch name {
	case "validate_query_result", "cancel_query", "cancel_query_job":
		return true
	default:
		return strings.Contains(name, "query") && strings.Contains(name, "async")
	}
}

// isAsyncQueryResultsTool reports whether the tool delivers the completed
// dataset of an async query job. Results tools must resolve to the
// query_metadata artifact_id (the deterministic placeholder id stamped at
// submit time) so the completed dataset conflicts with — and upgrades — the
// stored "Query Running" placeholder instead of landing under a fresh UUID.
func isAsyncQueryResultsTool(toolName string) bool {
	switch strings.TrimSpace(strings.ToLower(toolName)) {
	case "get_query_results", "fetch_query_results", "get_query_result", "query_results":
		return true
	default:
		return false
	}
}

func isAsyncQueryStatusTool(toolName string) bool {
	name := strings.TrimSpace(strings.ToLower(toolName))
	switch name {
	case "get_query_status", "query_status", "check_query_status", "poll_query_status", "get_query_job_status":
		return true
	default:
		return strings.Contains(name, "status") && strings.Contains(name, "query")
	}
}

func isAsyncQueryValidationTool(toolName string) bool {
	return strings.TrimSpace(strings.ToLower(toolName)) == "validate_query_result"
}

func isPendingQueryJobState(out tools.Output) bool {
	state := strings.ToLower(strings.TrimSpace(firstStringMapValue(out.QueryMetadata, "state", "status", "phase", "job_state", "job_status")))
	if state == "" {
		if m, ok := out.Result.(map[string]any); ok {
			state = strings.ToLower(strings.TrimSpace(firstStringMapValue(m, "state", "status", "phase", "job_state", "job_status")))
			if state == "" {
				if parseBoolValue(m["done"]) || parseBoolValue(m["completed"]) || parseBoolValue(m["is_complete"]) {
					return false
				}
				if parseBoolValue(m["pending"]) || parseBoolValue(m["running"]) {
					return true
				}
			}
		}
	}
	switch state {
	case "profiling", "queued", "queueing", "pending", "running", "in_progress", "submitted", "polling", "processing", "started":
		return true
	default:
		return false
	}
}

func queryOutputFlag(mm map[string]any, keys ...string) bool {
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || mm == nil {
			continue
		}
		if v, ok := mm[key]; ok && parseBoolValue(v) {
			return true
		}
	}
	return false
}

func invokeQueryJobsRegistry(ctx context.Context, registry any, op, jobID, ownerToken, threadID string) (map[string]any, error) {
	if registry == nil {
		return nil, ErrQueryJobsUnavailable
	}
	if reg, ok := registry.(*jobs.Registry); ok {
		return invokeJobsRegistry(ctx, reg, op, jobID, ownerToken, threadID)
	}
	rv := reflect.ValueOf(registry)
	if !rv.IsValid() {
		return nil, ErrQueryJobsUnavailable
	}
	for _, methodName := range queryJobsMethodNames(op) {
		method := rv.MethodByName(methodName)
		if !method.IsValid() {
			continue
		}
		out, handled, err := callQueryJobsMethod(method, ctx, jobID, ownerToken, threadID)
		if !handled {
			continue
		}
		return out, err
	}
	return nil, fmt.Errorf("query jobs registry does not support %s", strings.TrimSpace(op))
}

func invokeJobsRegistry(ctx context.Context, reg *jobs.Registry, op, jobID, ownerToken, threadID string) (map[string]any, error) {
	if reg == nil {
		return nil, ErrQueryJobsUnavailable
	}
	owner := jobs.JobOwner{
		ThreadID: strings.TrimSpace(threadID),
		Handle:   strings.TrimSpace(ownerToken),
	}
	switch strings.TrimSpace(strings.ToLower(op)) {
	case "cancel":
		if err := reg.Cancel(jobID, owner); err != nil {
			switch {
			case errors.Is(err, jobs.ErrNotFound):
				return nil, store.ErrNotFound
			case errors.Is(err, jobs.ErrOwnershipMismatch):
				return nil, ErrQueryJobUnauthorized
			default:
				return nil, err
			}
		}
	default:
		if err := reg.AttachOwner(jobID, owner); err != nil {
			switch {
			case errors.Is(err, jobs.ErrNotFound):
				return nil, store.ErrNotFound
			case errors.Is(err, jobs.ErrOwnershipMismatch):
				return nil, ErrQueryJobUnauthorized
			default:
				return nil, err
			}
		}
	}

	snap, ok := reg.Snapshot(jobID, owner)
	if !ok {
		return nil, store.ErrNotFound
	}
	return queryJobPayloadFromRegistry(snap), nil
}

func queryJobPayloadFromRegistry(job *jobs.Job) map[string]any {
	if job == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"job_id":                        strings.TrimSpace(job.ID),
		"status":                        strings.TrimSpace(string(job.Status)),
		"execution_status":              strings.TrimSpace(string(job.Status)),
		"provider":                      strings.TrimSpace(job.Provider),
		"thread_id":                     strings.TrimSpace(job.Owner.ThreadID),
		"owner_token":                   strings.TrimSpace(job.Owner.Handle),
		"monitor_health":                string(job.MonitorHealth),
		"policy_health":                 string(job.PolicyHealth),
		"materialization_state":         string(job.Materialization),
		"validation_state":              string(job.Validation),
		"validation_detail":             strings.TrimSpace(job.ValidationDetail),
		"max_result_rows":               job.Budgets.MaxResultRows,
		"max_result_bytes":              job.Budgets.MaxResultBytes,
		"max_scan_bytes":                job.Budgets.MaxScanBytes,
		"soft_queue_budget_ms":          job.Budgets.SoftQueue.Milliseconds(),
		"soft_execution_budget_ms":      job.Budgets.SoftExecution.Milliseconds(),
		"hard_execution_budget_ms":      job.Budgets.HardExecution.Milliseconds(),
		"submitted_at":                  job.SubmittedAt.UTC().Format(time.RFC3339Nano),
		"remote_submitted":              !job.Handle.IsZero(),
		"submit_dispatched":             job.SubmitDispatched,
		"provider_submission_confirmed": !job.Handle.IsZero(),
		"submission_uncertain":          queryJobSubmissionUncertain(job),
		"profile_outcome":               string(job.ProfileOutcome),
		"profile_phase":                 strings.TrimSpace(job.ProfilePhase),
		"profile_dispatched":            job.ProfileDispatched,
	}
	obligationID := firstStringMapValue(job.Metadata, "query_obligation_id")
	if store.ValidateQueryObligationID(obligationID) != nil {
		obligationID = ""
	}
	// Self-root minting is restricted to managed obligation roots whose
	// in-memory run scope is durable-truthful (AttachOwner never fills a
	// managed job's blank RunID, and a tombstoned blank fails derivation), so
	// a deleted-run job can never surface a fabricated current-run handle.
	if obligationID == "" && queryJobParticipatesInObligations(job) &&
		strings.TrimSpace(job.ReplacesJobID) == "" && strings.TrimSpace(job.ID) != "" {
		obligationID, _ = store.QueryObligationIDForRoot(job.Owner.RunID, job.ID)
	}
	if obligationID != "" {
		out["query_obligation_id"] = obligationID
	}
	if job.Profile != nil {
		out["query_profile"] = job.Profile
		out["profile"] = job.Profile
	}
	if !job.ProfileHandle.IsZero() {
		out["profile_handle"] = job.ProfileHandle
	}
	observedEnd := time.Now()
	if job.CompletedAt != nil {
		observedEnd = *job.CompletedAt
	}
	elapsed := observedEnd.Sub(job.SubmittedAt).Milliseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	out["elapsed_ms"] = elapsed
	out["telemetry_age_ms"] = nonNegativeMilliseconds(time.Since(job.LastAccess))
	if job.StartedAt != nil {
		out["started_at"] = job.StartedAt.UTC().Format(time.RFC3339Nano)
		out["queued_elapsed_ms"] = nonNegativeMilliseconds(job.StartedAt.Sub(job.SubmittedAt))
		out["execution_elapsed_ms"] = nonNegativeMilliseconds(observedEnd.Sub(*job.StartedAt))
	} else {
		out["queued_elapsed_ms"] = nonNegativeMilliseconds(observedEnd.Sub(job.SubmittedAt))
	}
	if job.CompletedAt != nil {
		out["completed_at"] = job.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if job.CancelReason != "" {
		out["cancel_reason"] = string(job.CancelReason)
	}
	if job.CancelOutcome != "" {
		out["cancel_outcome"] = string(job.CancelOutcome)
	}
	if strings.TrimSpace(job.Error) != "" {
		out["error"] = strings.TrimSpace(job.Error)
	}
	if job.Metadata != nil {
		for key, value := range job.Metadata {
			switch key {
			case jobs.QueryObligationManagedMetadataKey,
				"replaces_job_id", "replacesJobId", "replacesJobID",
				"attempted_replaces_job_id", "attemptedReplacesJobId", "attemptedReplacesJobID":
				continue
			}
			if _, exists := out[key]; exists {
				continue
			}
			out[key] = value
		}
	}
	if firstStringMapValue(out, "artifact_id", "full_artifact_id") == "" {
		if artifactID := tools.DeterministicAsyncArtifactID(job.Owner.ThreadID, job.ID); artifactID != "" {
			out["artifact_id"] = artifactID
			out["full_artifact_id"] = artifactID
		}
	}
	if job.Status == jobs.JobStatusCompleted && job.Result != nil {
		out["row_count_total"] = len(job.Result.Rows)
		out["results_ready"] = false
	}
	return out
}

// queryJobParticipatesInObligations mirrors jobs.RequiresQueryObligationBinding
// for in-memory jobs: only marked managed launches belong to the run-query
// obligation protocol, so only they may ever surface an obligation handle.
func queryJobParticipatesInObligations(job *jobs.Job) bool {
	if job == nil {
		return false
	}
	managed, _ := job.Metadata[jobs.QueryObligationManagedMetadataKey].(bool)
	return managed
}

func queryJobSubmissionUncertain(job *jobs.Job) bool {
	if job == nil || !job.Handle.IsZero() {
		return false
	}
	switch job.Status {
	case jobs.JobStatusSubmissionUnknown, jobs.JobStatusOrphaned:
		return true
	case jobs.JobStatusReserved, jobs.JobStatusSubmitted, jobs.JobStatusRunning,
		jobs.JobStatusCancelRequested, jobs.JobStatusCancelling:
		return job.SubmitDispatched
	default:
		return false
	}
}

func nonNegativeMilliseconds(duration time.Duration) int64 {
	if duration < 0 {
		return 0
	}
	return duration.Milliseconds()
}

func (m *Manager) materializeCompletedQueryResultArtifact(ctx context.Context, jobID, threadID, ownerToken string) (store.Artifact, bool, error) {
	reg, ok := m.queryJobs.(*jobs.Registry)
	if !ok || reg == nil {
		return store.Artifact{}, false, nil
	}
	owner := jobs.JobOwner{
		ThreadID: strings.TrimSpace(threadID),
		Handle:   strings.TrimSpace(ownerToken),
	}
	snap, ok := reg.Snapshot(jobID, owner)
	if !ok || snap.Status != jobs.JobStatusCompleted {
		return store.Artifact{}, false, nil
	}
	artifactID := firstStringMapValue(snap.Metadata, "artifact_id", "full_artifact_id")
	if artifactID == "" {
		artifactID = tools.DeterministicAsyncArtifactID(threadID, jobID)
	}
	if artifactID == "" {
		return store.Artifact{}, false, nil
	}

	var existing *store.Artifact
	if current, err := m.store.GetArtifact(ctx, artifactID); err == nil {
		if strings.TrimSpace(current.ThreadID) != strings.TrimSpace(threadID) {
			return store.Artifact{}, false, ErrQueryJobThreadScope
		}
		if !isAsyncQueryPlaceholderArtifact(current) {
			return current, true, nil
		}
		existing = &current
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Artifact{}, false, err
	}

	result, err := reg.Results(jobID, owner)
	if err != nil {
		return store.Artifact{}, false, err
	}
	if result == nil {
		result = &jobs.QueryResult{}
	}
	metadata := completedQueryResultArtifactMetadata(snap, result, ownerToken, artifactID)
	payload, err := tools.NewLargeRowsArtifactPayload(artifactID, "Query Results", result.Rows, "get_query_results", metadata)
	if err != nil {
		return store.Artifact{}, false, err
	}
	artifact, err := m.persistMaterializedQueryArtifact(ctx, threadID, existing, payload)
	if err != nil {
		return store.Artifact{}, false, err
	}
	return artifact, true, nil
}

func completedQueryResultArtifactMetadata(job *jobs.Job, result *jobs.QueryResult, ownerToken, artifactID string) map[string]any {
	metadata := cloneStringAnyMap(job.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	for _, key := range []string{
		jobs.QueryObligationManagedMetadataKey,
		"replaces_job_id", "replacesJobId", "replacesJobID",
		"attempted_replaces_job_id", "attemptedReplacesJobId", "attemptedReplacesJobID",
	} {
		delete(metadata, key)
	}
	// The materialized artifact is deterministic per job and is served across
	// runs, so it must not advertise ANY run-scoped obligation handle: a later
	// run reading the artifact would otherwise see another run's retry
	// identity. Obligation handles surface only through run-scoped status and
	// result payloads whose ledger verified them.
	delete(metadata, "query_obligation_id")
	if result != nil {
		for key, value := range result.Metadata {
			switch key {
			case jobs.QueryObligationManagedMetadataKey,
				"replaces_job_id", "replacesJobId", "replacesJobID",
				"attempted_replaces_job_id", "attemptedReplacesJobId", "attemptedReplacesJobID",
				"query_obligation_id":
				continue
			}
			metadata[key] = value
		}
	}
	if sql := strings.TrimSpace(job.SQL); sql != "" {
		metadata["sql"] = sql
	}
	if sqlPreview := strings.TrimSpace(job.SQLPreview); sqlPreview != "" {
		metadata["sql_preview"] = sqlPreview
	}
	rowCount := 0
	if result != nil {
		rowCount = len(result.Rows)
		if len(result.Schema) > 0 {
			metadata["schema"] = result.Schema
		}
	}
	metadata["provider"] = strings.TrimSpace(job.Provider)
	metadata["job_id"] = strings.TrimSpace(job.ID)
	metadata["status"] = string(jobs.JobStatusCompleted)
	metadata["owner_token"] = strings.TrimSpace(ownerToken)
	metadata["thread_id"] = strings.TrimSpace(job.Owner.ThreadID)
	metadata["artifact_id"] = artifactID
	metadata["full_artifact_id"] = artifactID
	metadata["rows_returned"] = rowCount
	metadata["rows_preview_count"] = rowCount
	metadata["row_count_total"] = rowCount
	metadata["result_counts"] = map[string]any{"rows": rowCount}
	metadata["page_size"] = rowCount
	metadata["offset"] = 0
	metadata["has_more"] = false
	enriched := enrichToolOutputResultMetadata("get_query_results", tools.Output{
		OK:            true,
		ResultFormat:  tools.ResultFormatJSON,
		Result:        map[string]any{"rows": resultRowsForMetadata(result)},
		QueryMetadata: cloneStringAnyMap(metadata),
	})
	if len(enriched.QueryMetadata) > 0 {
		metadata["query_metadata"] = cloneStringAnyMap(enriched.QueryMetadata)
	}
	if len(enriched.ResultMetadata) > 0 {
		resultMetadata := cloneStringAnyMap(enriched.ResultMetadata)
		metadata["result_metadata"] = resultMetadata
		if presentation, ok := resultMetadata["tool_presentation"]; ok {
			metadata["tool_presentation"] = presentation
		}
	}
	return stripSharedQueryArtifactRetryFields(metadata)
}

func resultRowsForMetadata(result *jobs.QueryResult) []map[string]any {
	if result == nil || result.Rows == nil {
		return []map[string]any{}
	}
	return result.Rows
}

func (m *Manager) persistMaterializedQueryArtifact(ctx context.Context, threadID string, existing *store.Artifact, payload *tools.ArtifactPayload) (store.Artifact, error) {
	if payload == nil {
		return store.Artifact{}, errors.New("query result artifact payload is missing")
	}
	storage := "mysql"
	var contentTextPtr *string
	var contentPathPtr *string
	b := []byte(payload.Content)
	if len(b) > artifactInlineMaxBytes {
		rel, err := m.writeArtifactFile(payload.ID, payload.Type, b)
		if err != nil {
			return store.Artifact{}, err
		}
		storage = "file"
		contentPathPtr = &rel
	} else {
		content := payload.Content
		contentTextPtr = &content
	}

	toolName := "get_query_results"
	okVal := true
	a := store.Artifact{
		ID:          strings.TrimSpace(payload.ID),
		ThreadID:    strings.TrimSpace(threadID),
		Type:        payload.Type,
		Title:       payload.Title,
		Language:    payload.Language,
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     storage,
		ContentText: contentTextPtr,
		ContentPath: contentPathPtr,
		ByteSize:    int64(len(b)),
		Metadata:    cloneStringAnyMap(payload.Metadata),
	}
	if existing != nil {
		a.MessageID = existing.MessageID
		a.ToolCallID = existing.ToolCallID
		return m.store.ReplaceArtifact(ctx, a)
	}
	created, err := m.store.InsertArtifact(ctx, a)
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, store.ErrConflict) {
		return store.Artifact{}, err
	}
	current, getErr := m.store.GetArtifact(ctx, a.ID)
	if getErr != nil {
		return store.Artifact{}, getErr
	}
	if strings.TrimSpace(current.ThreadID) != strings.TrimSpace(a.ThreadID) {
		return store.Artifact{}, ErrQueryJobThreadScope
	}
	if !isAsyncQueryPlaceholderArtifact(current) {
		return current, nil
	}
	a.MessageID = current.MessageID
	a.ToolCallID = current.ToolCallID
	return m.store.ReplaceArtifact(ctx, a)
}

func isAsyncQueryPlaceholderArtifact(artifact store.Artifact) bool {
	toolName := ""
	if artifact.ToolName != nil {
		toolName = strings.TrimSpace(strings.ToLower(*artifact.ToolName))
	}
	return parseBoolValue(artifact.Metadata["placeholder"]) ||
		parseBoolValue(artifact.Metadata["async_query"]) ||
		tools.IsAsyncQueryRunTool(toolName)
}

func queryJobsMethodNames(op string) []string {
	switch strings.TrimSpace(strings.ToLower(op)) {
	case "cancel":
		return []string{"Cancel", "CancelJob", "CancelQuery", "Stop"}
	default:
		return []string{"Status", "GetStatus", "GetJobStatus", "QueryStatus", "Lookup", "Get"}
	}
}

func callQueryJobsMethod(method reflect.Value, ctx context.Context, jobID, ownerToken, threadID string) (map[string]any, bool, error) {
	candidates := [][]any{
		{ctx, jobID, ownerToken, threadID},
		{ctx, jobID, threadID, ownerToken},
		{ctx, jobID, ownerToken},
		{ctx, jobID, threadID},
		{ctx, jobID},
		{jobID, ownerToken, threadID},
		{jobID, ownerToken},
		{jobID},
	}
	for _, rawArgs := range candidates {
		args, ok := reflectArgsForCall(method.Type(), rawArgs)
		if !ok {
			continue
		}
		returnVals := method.Call(args)
		out, err := decodeQueryJobsCallOutput(returnVals)
		return out, true, err
	}
	return nil, false, nil
}

func reflectArgsForCall(methodType reflect.Type, rawArgs []any) ([]reflect.Value, bool) {
	if methodType.NumIn() != len(rawArgs) {
		return nil, false
	}
	args := make([]reflect.Value, 0, len(rawArgs))
	for idx, raw := range rawArgs {
		arg, ok := coerceReflectArg(raw, methodType.In(idx))
		if !ok {
			return nil, false
		}
		args = append(args, arg)
	}
	return args, true
}

func coerceReflectArg(raw any, target reflect.Type) (reflect.Value, bool) {
	if raw == nil {
		switch target.Kind() {
		case reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.Func:
			return reflect.Zero(target), true
		default:
			return reflect.Value{}, false
		}
	}
	v := reflect.ValueOf(raw)
	if v.Type().AssignableTo(target) {
		return v, true
	}
	if target.Kind() == reflect.Interface && v.Type().Implements(target) {
		return v, true
	}
	if v.Type().ConvertibleTo(target) {
		return v.Convert(target), true
	}
	return reflect.Value{}, false
}

func decodeQueryJobsCallOutput(vals []reflect.Value) (map[string]any, error) {
	if len(vals) == 0 {
		return map[string]any{}, nil
	}
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	trimmed := vals
	last := vals[len(vals)-1]
	if last.IsValid() && last.Type().Implements(errorType) {
		if !last.IsNil() {
			return nil, last.Interface().(error)
		}
		trimmed = vals[:len(vals)-1]
	}
	if len(trimmed) == 0 {
		return map[string]any{}, nil
	}
	if len(trimmed) == 2 {
		if ok, isBool := trimmed[1].Interface().(bool); isBool {
			if !ok {
				return nil, store.ErrNotFound
			}
			trimmed = trimmed[:1]
		}
	}
	return normalizeQueryJobPayload(trimmed[0].Interface())
}

func normalizeQueryJobPayload(raw any) (map[string]any, error) {
	if raw == nil {
		return map[string]any{}, nil
	}
	if m, ok := raw.(map[string]any); ok {
		out := make(map[string]any, len(m))
		for key, value := range m {
			out[key] = value
		}
		return out, nil
	}
	if m, ok := raw.(map[string]string); ok {
		out := make(map[string]any, len(m))
		for key, value := range m {
			out[key] = value
		}
		return out, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func queryJobMatchesThread(payload map[string]any, threadID string) bool {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return false
	}
	candidate := firstStringMapValue(payload, "thread_id", "threadId")
	if candidate == "" {
		if m, ok := payload["context"].(map[string]any); ok {
			candidate = firstStringMapValue(m, "thread_id", "threadId")
		}
	}
	if candidate == "" {
		if m, ok := payload["owner"].(map[string]any); ok {
			candidate = firstStringMapValue(m, "thread_id", "threadId", "ThreadID")
		}
	}
	if candidate == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(candidate), threadID)
}

func queryJobMatchesOwnerToken(payload map[string]any, ownerToken string) bool {
	ownerToken = strings.TrimSpace(ownerToken)
	if ownerToken == "" {
		return false
	}
	candidate := firstStringMapValue(payload, "owner_token", "ownerToken", "query_owner_token", "queryOwnerToken", "x-query-owner-token")
	if candidate == "" {
		if m, ok := payload["context"].(map[string]any); ok {
			candidate = firstStringMapValue(m, "owner_token", "ownerToken", "query_owner_token", "queryOwnerToken", "x-query-owner-token")
		}
	}
	if candidate == "" {
		if m, ok := payload["owner"].(map[string]any); ok {
			candidate = firstStringMapValue(m, "owner_token", "ownerToken", "handle", "Handle")
		}
	}
	if candidate == "" {
		return false
	}
	return strings.TrimSpace(candidate) == ownerToken
}

func firstStringMapValue(mm map[string]any, keys ...string) string {
	if mm == nil {
		return ""
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		raw, ok := mm[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case fmt.Stringer:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		}
	}
	return ""
}

func anyMap(v any) map[string]any {
	if mm, ok := v.(map[string]any); ok {
		return mm
	}
	return nil
}

func firstBoolMapValue(mm map[string]any, keys ...string) (bool, bool) {
	if mm == nil {
		return false, false
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		raw, ok := mm[key]
		if !ok {
			continue
		}
		if value, ok := parseBoolAny(raw); ok {
			return value, true
		}
	}
	return false, false
}

func parseBoolAny(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		switch s {
		case "true", "1", "yes", "y", "on":
			return true, true
		case "false", "0", "no", "n", "off":
			return false, true
		}
	case int:
		return x != 0, true
	case int8:
		return x != 0, true
	case int16:
		return x != 0, true
	case int32:
		return x != 0, true
	case int64:
		return x != 0, true
	case uint:
		return x != 0, true
	case uint8:
		return x != 0, true
	case uint16:
		return x != 0, true
	case uint32:
		return x != 0, true
	case uint64:
		return x != 0, true
	case float32:
		return x != 0, true
	case float64:
		return x != 0, true
	}
	return false, false
}

func firstIntMapValue(mm map[string]any, keys ...string) (int, bool) {
	if mm == nil {
		return 0, false
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		raw, ok := mm[key]
		if !ok {
			continue
		}
		if value, ok := parseIntAny(raw); ok {
			return value, true
		}
	}
	return 0, false
}

func parseIntAny(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int8:
		return int(x), true
	case int16:
		return int(x), true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case uint:
		return int(x), true
	case uint8:
		return int(x), true
	case uint16:
		return int(x), true
	case uint32:
		return int(x), true
	case uint64:
		return int(x), true
	case float32:
		return int(x), true
	case float64:
		return int(x), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err == nil {
			return n, true
		}
	case json.Number:
		if n, err := x.Int64(); err == nil {
			return int(n), true
		}
	}
	return 0, false
}

func firstFloatMapValue(mm map[string]any, keys ...string) (float64, bool) {
	if mm == nil {
		return 0, false
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		raw, ok := mm[key]
		if !ok {
			continue
		}
		if value, ok := parseFloatAny(raw); ok {
			return value, true
		}
	}
	return 0, false
}

func parseFloatAny(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case json.Number:
		if f, err := x.Float64(); err == nil {
			return f, true
		}
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err == nil {
			return f, true
		}
	}
	return 0, false
}

func firstMapMapValue(mm map[string]any, keys ...string) (map[string]any, bool) {
	if mm == nil {
		return nil, false
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		raw, ok := mm[key]
		if !ok {
			continue
		}
		if m, ok := raw.(map[string]any); ok {
			return m, true
		}
	}
	return nil, false
}

func cloneStringAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		out = make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
	}
	return out
}

func stripSharedQueryArtifactRetryFields(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	clean, ok := redactStaleQueryObligationStrings(stripQueryRegistryRunScopedFields(value)).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return clean
}

func sanitizeAsyncQueryToolCallForModel(toolName string, call types.ToolCall, out tools.Output) types.ToolCall {
	if !tools.IsAsyncQueryRunTool(toolName) {
		if (isAsyncQueryStatusTool(toolName) || isAsyncQueryResultsTool(toolName) || isAsyncQueryValidationTool(toolName) || strings.TrimSpace(toolName) == "cancel_query") &&
			(queryOutputFlag(anyMap(out.Result), "query_observation_identity_mismatch") || queryOutputFlag(out.QueryMetadata, "query_observation_identity_mismatch")) {
			// A valid executed capability must remain available to restart
			// recovery even when the provider response was cross-wired. Only
			// remove lifecycle authority when the executed request itself was
			// malformed (nil/zero/non-managed id or missing owner token).
			if _, _, valid := executedQueryLifecycleIdentity(call); valid {
				return call
			}
			clean := cloneStringAnyMap(call.Args)
			if clean == nil && strings.TrimSpace(call.ArgsText) != "" {
				_ = json.Unmarshal([]byte(call.ArgsText), &clean)
			}
			delete(clean, "job_id")
			delete(clean, "owner_token")
			call.Args = clean
			if encoded, err := json.Marshal(clean); err == nil {
				call.ArgsText = string(encoded)
			}
		}
		return call
	}
	args := call.Args
	if args == nil && strings.TrimSpace(call.ArgsText) != "" {
		var decoded map[string]any
		if json.Unmarshal([]byte(call.ArgsText), &decoded) == nil {
			args = decoded
		}
	}
	clean := cloneStringAnyMap(args)
	if clean == nil {
		clean = map[string]any{}
	}
	if recursivelyClean, ok := stripQueryRetryIdentityFields(clean, true).(map[string]any); ok {
		clean = recursivelyClean
	}
	canonicalObligationID := firstStringMapValue(out.QueryMetadata, "query_obligation_id")
	if canonicalObligationID == "" {
		canonicalObligationID = firstStringMapValue(anyMap(out.Result), "query_obligation_id")
	}
	if store.ValidateQueryObligationID(canonicalObligationID) == nil {
		clean["query_obligation_id"] = canonicalObligationID
	}
	call.Args = clean
	if encoded, err := json.Marshal(clean); err == nil {
		call.ArgsText = string(encoded)
	} else {
		call.ArgsText = "{}"
	}
	return call
}

func extractChartBudgetMetadata(result any) (map[string]any, bool) {
	resultMap := anyMap(result)
	if resultMap == nil {
		return nil, false
	}
	if direct, ok := firstMapMapValue(resultMap, "chart_budget"); ok {
		clone := cloneStringAnyMap(direct)
		if strings.TrimSpace(firstStringMapValue(clone, "schema_version")) == "" {
			clone["schema_version"] = "chart-budget.v1"
		}
		return clone, true
	}
	if resultMetadata, ok := firstMapMapValue(resultMap, "result_metadata"); ok {
		if nested, ok := firstMapMapValue(resultMetadata, "chart_budget"); ok {
			clone := cloneStringAnyMap(nested)
			if strings.TrimSpace(firstStringMapValue(clone, "schema_version")) == "" {
				clone["schema_version"] = "chart-budget.v1"
			}
			return clone, true
		}
	}
	return nil, false
}

func isChartBudgetTool(toolName string) bool {
	switch strings.TrimSpace(strings.ToLower(toolName)) {
	case "display_table", "build_chart_artifact":
		return true
	default:
		return false
	}
}

func logChartBudgetSummary(runID string, artifactID string, toolName string, env map[string]any, resultMetadata map[string]any) {
	var chartBudget map[string]any
	if rm := anyMap(resultMetadata); rm != nil {
		if nested, ok := firstMapMapValue(rm, "chart_budget"); ok {
			chartBudget = nested
		}
	}
	if chartBudget == nil {
		if nested, ok := firstMapMapValue(env, "chart_budget"); ok {
			chartBudget = nested
		}
	}

	chartEngine := firstStringMapValue(anyMap(env), "chart_engine")
	chartKind := firstStringMapValue(chartBudget, "chart_kind")
	rowCountTotal, _ := firstIntMapValue(chartBudget, "row_count_total")
	if rowCountTotal == 0 {
		rowCountTotal, _ = firstIntMapValue(anyMap(env), "row_count_total")
	}
	pointsReturned, _ := firstIntMapValue(chartBudget, "points_returned")
	plotSpecBytes, _ := firstIntMapValue(chartBudget, "plot_spec_bytes")
	sseEventBytes, _ := firstIntMapValue(anyMap(resultMetadata), "sse_event_bytes")
	if sseEventBytes == 0 {
		sseEventBytes, _ = firstIntMapValue(chartBudget, "sse_event_bytes")
	}
	budgetActions := strings.Join(firstStringSliceMapValue(chartBudget, "budget_actions"), ",")
	failureStage := firstStringMapValue(chartBudget, "failure_stage")

	log.Printf(
		"chart_budget run_id=%s artifact_id=%s tool_name=%s chart_engine=%s chart_kind=%s row_count_total=%d points_returned=%d plot_spec_bytes=%d sse_event_bytes=%d budget_actions=%q failure_stage=%s",
		strings.TrimSpace(runID),
		strings.TrimSpace(artifactID),
		strings.TrimSpace(toolName),
		chartEngine,
		chartKind,
		rowCountTotal,
		pointsReturned,
		plotSpecBytes,
		sseEventBytes,
		budgetActions,
		failureStage,
	)
}

func extractPlotSpecMetadata(result any) (plotSpec map[string]any, chartEngine string, chartSpecVersion string, chartModule string, chartModules []string) {
	resultMap, ok := result.(map[string]any)
	if !ok || resultMap == nil {
		return nil, "", "", "", nil
	}
	raw := resultMap["plot_spec"]
	if raw == nil {
		raw = resultMap["chart_spec"]
	}
	if spec, ok := raw.(map[string]any); ok && len(spec) > 0 {
		plotSpec = spec
	}
	if plotSpec == nil {
		return nil, "", "", "", nil
	}
	chartEngine = firstStringMapValue(resultMap, "chart_engine")
	if chartEngine == "" {
		schemaVersion := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", plotSpec["schema_version"])))
		switch {
		case strings.HasPrefix(schemaVersion, "highcharts-"):
			chartEngine = highchartsEngineName
		case strings.HasPrefix(schemaVersion, "echarts-"):
			chartEngine = echartsEngineName
		default:
			chartEngine = highchartsEngineName
		}
	}
	chartSpecVersion = firstStringMapValue(resultMap, "chart_spec_version", "capability_version")
	chartModule = firstStringMapValue(resultMap, "chart_module")
	chartModules = firstStringSliceMapValue(resultMap, "chart_modules")
	if chartModule == "" {
		chartModule = firstStringMapValue(plotSpec, "module")
	}
	if len(chartModules) == 0 {
		chartModules = firstStringSliceMapValue(plotSpec, "modules")
	}
	if len(chartModules) == 0 && chartModule != "" {
		chartModules = []string{chartModule}
	}
	return plotSpec, chartEngine, chartSpecVersion, chartModule, chartModules
}

func firstStringSliceMapValue(mm map[string]any, key string) []string {
	if mm == nil {
		return nil
	}
	raw, ok := mm[strings.TrimSpace(key)]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	seen := map[string]struct{}{}
	for _, entry := range arr {
		var s string
		switch v := entry.(type) {
		case string:
			s = strings.TrimSpace(v)
		case fmt.Stringer:
			s = strings.TrimSpace(v.String())
		default:
			s = strings.TrimSpace(fmt.Sprintf("%v", entry))
		}
		if s == "" {
			continue
		}
		if _, exists := seen[s]; exists {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func queryOutputCarriesMaterializedDataset(out tools.Output) bool {
	if out.Artifact != nil {
		return true
	}
	result := anyMap(out.Result)
	if result == nil {
		return false
	}
	for _, key := range []string{"rows", "data", "results"} {
		if _, exists := result[key]; exists {
			return true
		}
	}
	return false
}

func buildArtifact(toolName string, out tools.Output, rawText string) (typ string, title string, language *string, content string, metadata map[string]any) {
	out = enrichToolOutputResultMetadata(toolName, out)
	metadata = map[string]any{
		"tool_name": toolName,
		"ok":        out.OK,
	}
	if out.QueryMetadata != nil {
		queryMetadata := out.QueryMetadata
		if isAsyncQueryTool(toolName) {
			queryMetadata = stripSharedQueryArtifactRetryFields(queryMetadata)
		}
		metadata["query_metadata"] = queryMetadata
	}
	if len(out.ResultMetadata) > 0 {
		resultMetadata := cloneStringAnyMap(out.ResultMetadata)
		if isAsyncQueryTool(toolName) {
			resultMetadata = stripSharedQueryArtifactRetryFields(resultMetadata)
		}
		metadata["result_metadata"] = resultMetadata
	}
	if resultMap := anyMap(out.Result); resultMap != nil {
		if resultMetadata, ok := firstMapMapValue(resultMap, "result_metadata"); ok {
			kind := strings.TrimSpace(strings.ToLower(firstStringMapValue(resultMetadata, "kind")))
			widget := strings.TrimSpace(strings.ToLower(firstStringMapValue(resultMetadata, "widget")))
			if kind == "approval" || widget == "approval" {
				typ = "data"
				title = "Approval Required"
				switch strings.TrimSpace(strings.ToLower(firstStringMapValue(resultMap, "status"))) {
				case "approval_denied":
					title = "Approval Denied"
				case "approval_error":
					title = "Approval Error"
				case "approval_pending":
					title = "Approval Pending"
				}
				metadata["raw_result"] = cloneStringAnyMap(resultMap)
				content = rawText
				return
			}
		}
	}
	if plotSpec, chartEngine, chartSpecVersion, chartModule, chartModules := extractPlotSpecMetadata(out.Result); plotSpec != nil {
		metadata["plot_spec"] = plotSpec
		if chartEngine != "" {
			metadata["chart_engine"] = chartEngine
		}
		if chartSpecVersion != "" {
			metadata["chart_spec_version"] = chartSpecVersion
		}
		if chartModule != "" {
			metadata["chart_module"] = chartModule
		}
		if len(chartModules) > 0 {
			metadata["chart_modules"] = chartModules
		}
	}
	if chartBudget, ok := extractChartBudgetMetadata(out.Result); ok {
		metadata["chart_budget"] = chartBudget
	}

	// A lifecycle response without a dataset is always an upgradeable
	// placeholder. With thread/job identity it occupies the canonical result
	// artifact slot; incomplete legacy payloads use a separate deterministic
	// fallback. This includes status, validation, cancel,
	// completed-before-fetch, and pending get-results responses.
	if isAsyncQueryTool(toolName) && !queryOutputCarriesMaterializedDataset(out) && extractAsyncQueryJobID(out) != "" {
		typ = "data"
		title = "Query Running"
		status := firstStringMapValue(out.QueryMetadata, "status", "execution_status")
		if status == "" {
			status = firstStringMapValue(anyMap(out.Result), "status", "execution_status")
		}
		status = strings.ToLower(strings.TrimSpace(status))
		switch status {
		case string(jobs.JobStatusProfiling):
			title = "Query Profiling"
		case string(jobs.JobStatusCompleted):
			title = "Query Results Ready"
		case string(jobs.JobStatusFailed), string(jobs.JobStatusOrphaned), string(jobs.JobStatusExpired):
			title = "Query Failed"
		case string(jobs.JobStatusCancelled):
			title = "Query Cancelled"
		}
		content = ""
		metadata["placeholder"] = true
		metadata["async_query"] = true
		if jobID := extractAsyncQueryJobID(out); jobID != "" {
			metadata["job_id"] = jobID
		}
		for _, key := range []string{"thread_id", "owner_token", "provider", "status", "description", "sql_preview", "artifact_id"} {
			for _, source := range []map[string]any{out.QueryMetadata, anyMap(out.Result), out.ResultMetadata} {
				if v := firstStringMapValue(source, key); v != "" {
					metadata[key] = v
					break
				}
			}
		}
		return
	}

	if (toolName == "search_threads" || toolName == "search_docs" || toolName == "rlm_exec") && out.ResultFormat == tools.ResultFormatJSON {
		typ = "data"
		if toolName == "search_threads" {
			title = "Thread Search Results"
		} else {
			title = "Search Results"
		}
		content = rawText
		return
	}

	// Special: display_table should persist the dataset itself for UI rendering.
	if toolName == "display_table" && out.OK {
		typ = "data"
		title = "Table"
		if resultMap, ok := out.Result.(map[string]any); ok {
			if tableTitle := firstStringMapValue(resultMap, "title"); tableTitle != "" {
				title = tableTitle
			}
			if previewOnly, ok := firstBoolMapValue(resultMap, "preview_only"); ok {
				metadata["preview_only"] = previewOnly
				metadata["partial"] = previewOnly
			}
			if fullArtifactID := firstStringMapValue(resultMap, "full_artifact_id"); fullArtifactID != "" {
				metadata["full_artifact_id"] = fullArtifactID
			}
			if rowCountTotal, ok := firstIntMapValue(resultMap, "row_count_total"); ok {
				metadata["row_count_total"] = rowCountTotal
			}
			if downsampleRatio, ok := firstFloatMapValue(resultMap, "downsample_ratio"); ok {
				metadata["downsample_ratio"] = downsampleRatio
			}
		}
		if arr, ok := unwrapSingleArrayField(out.Result); ok && isArrayOfObjects(arr) {
			b, _ := json.Marshal(arr)
			content = string(b)
			metadata["row_count"] = len(arr)
			return
		}
		content = rawText
		return
	}

	// Special: build_chart_artifact returns preview rows in the tool result and
	// stores the full transformed dataset via out.Artifact.
	if toolName == "build_chart_artifact" && out.OK {
		typ = "data"
		title = "Chart Preview"
		if resultMap, ok := out.Result.(map[string]any); ok {
			if tableTitle := firstStringMapValue(resultMap, "title"); tableTitle != "" {
				title = tableTitle
			}
			if previewOnly, ok := firstBoolMapValue(resultMap, "preview_only"); ok {
				metadata["preview_only"] = previewOnly
				metadata["partial"] = previewOnly
			}
			if fullArtifactID := firstStringMapValue(resultMap, "full_artifact_id"); fullArtifactID != "" {
				metadata["full_artifact_id"] = fullArtifactID
			}
			if rowCountTotal, ok := firstIntMapValue(resultMap, "row_count_total"); ok {
				metadata["row_count_total"] = rowCountTotal
			}
			if downsampleRatio, ok := firstFloatMapValue(resultMap, "downsample_ratio"); ok {
				metadata["downsample_ratio"] = downsampleRatio
			}
		}
		if arr, ok := unwrapSingleArrayField(out.Result); ok && isArrayOfObjects(arr) {
			b, _ := json.Marshal(arr)
			content = string(b)
			metadata["row_count"] = len(arr)
			return
		}
		content = rawText
		return
	}

	// read_file: prefer code artifact with extracted file content.
	if toolName == "read_file" && out.ResultFormat == tools.ResultFormatText {
		lines := strings.SplitN(rawText, "\n\n", 2)
		if len(lines) == 2 && strings.HasPrefix(lines[0], "File: ") {
			p := strings.TrimSpace(strings.TrimPrefix(lines[0], "File: "))
			if p != "" {
				title = p
			}
			body := lines[1]
			content = body
			typ = "code"
			lang := fileExtLang(p)
			if lang != "" {
				language = &lang
			}
			return
		}
	}

	// JSON dataset heuristic (array-of-objects, or single-field wrapper).
	if out.ResultFormat == tools.ResultFormatJSON {
		if arr, ok := unwrapSingleArrayField(out.Result); ok && isArrayOfObjects(arr) {
			typ = "data"
			if toolName == "run_bigquery_query" || toolName == "run_databricks_query" || toolName == "run_trino_query" || toolName == "run_snowflake_query" {
				title = "Query Results"
			} else {
				title = toolName + " results"
			}
			b, _ := json.Marshal(arr)
			content = string(b)
			metadata["row_count"] = len(arr)
			return
		}
	}

	// Fallback: store raw output as code/text.
	if tools.IsAsyncQueryRunTool(toolName) && !out.OK && extractAsyncQueryJobID(out) != "" {
		metadata["async_query"] = true
		metadata["job_id"] = extractAsyncQueryJobID(out)
		if resultMap := anyMap(out.Result); resultMap != nil {
			metadata["raw_result"] = cloneStringAnyMap(resultMap)
		}
		for _, key := range []string{"thread_id", "owner_token", "provider", "status", "profile_outcome", "profile_phase"} {
			if value := firstStringMapValue(out.QueryMetadata, key); value != "" {
				metadata[key] = value
			}
		}
	}
	typ = "code"
	title = toolName + " result"
	if out.OK == false {
		title = toolName + " error"
	}
	content = rawText
	if out.ResultFormat == tools.ResultFormatJSON {
		lang := "json"
		language = &lang
	}
	return
}

func enrichToolOutputResultMetadata(toolName string, out tools.Output) tools.Output {
	out = tools.NormalizeOutputMetadata(out)
	productMeta, ok := tools.ToolMetadataByName(toolName)
	if !ok {
		return out
	}
	resultMetadata := cloneStringAnyMap(out.ResultMetadata)
	if resultMetadata == nil {
		resultMetadata = map[string]any{}
	}
	setStringDefault := func(key string, value string) {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			return
		}
		if _, exists := resultMetadata[key]; exists {
			return
		}
		resultMetadata[key] = value
	}
	setAnyDefault := func(key string, value any) {
		key = strings.TrimSpace(key)
		if key == "" || value == nil {
			return
		}
		if _, exists := resultMetadata[key]; exists {
			return
		}
		resultMetadata[key] = value
	}

	presentation := productMeta.Presentation
	integration := strings.TrimSpace(productMeta.ConnectorID)
	if integration == "" {
		integration = strings.TrimSpace(presentation.Family)
	}
	if integration == "" {
		integration = string(productMeta.Origin)
	}

	setStringDefault("tool_name", productMeta.Name)
	setStringDefault("tool_set_id", productMeta.ToolSetID)
	setStringDefault("tool_set_name", productMeta.ToolSetName)
	setStringDefault("connector_id", productMeta.ConnectorID)
	setStringDefault("integration", integration)
	setStringDefault("product", presentation.Family)
	setStringDefault("family", presentation.Family)
	setStringDefault("kind", presentation.ResultKind)
	setStringDefault("native_viewer", presentation.NativeViewer)
	setStringDefault("renderer_strategy", presentation.RendererStrategy)
	setStringDefault("renderer_key", presentation.RendererKey)
	setStringDefault("file_capability", presentation.FileCapability)
	setAnyDefault("tool_presentation", presentation)
	out.ResultMetadata = resultMetadata
	return out
}

func unwrapSingleArrayField(v any) ([]map[string]any, bool) {
	switch t := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(t))
		for _, it := range t {
			m, ok := it.(map[string]any)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	case map[string]any:
		// Common: BigQuery tool results wrap the dataset under a "rows" field, sometimes with extra fields (e.g. notes).
		// Prefer unwrapping "rows" when present so the artifact is recognized as a dataset.
		if inner, ok := t["rows"]; ok {
			return unwrapSingleArrayField(inner)
		}
		if len(t) != 1 {
			return nil, false
		}
		for _, inner := range t {
			return unwrapSingleArrayField(inner)
		}
	}
	return nil, false
}

func isArrayOfObjects(arr []map[string]any) bool {
	return arr != nil
}

func fileExtLang(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	ext := filepath.Ext(p)
	if ext == "" {
		return ""
	}
	ext = strings.TrimPrefix(ext, ".")
	return ext
}

func (m *Manager) writeArtifactFile(artifactID string, typ string, b []byte) (string, error) {
	dir := filepath.Join(m.cfgDir, "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	ext := ".txt"
	if typ == "data" {
		ext = ".json"
	}
	name := artifactID + ext
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, b, 0o600); err != nil {
		return "", err
	}
	// Store relative to cfgDir for portability.
	return filepath.ToSlash(filepath.Join("artifacts", name)), nil
}

func sha256Bytes(b []byte) []byte {
	sum := sha256.Sum256(b)
	out := make([]byte, 32)
	copy(out, sum[:])
	return out
}
