package queryadapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/trino"
)

type trinoClient interface {
	SubmitAsync(context.Context, trino.AsyncQueryRequest) (trino.AsyncQueryPage, error)
	PollAsync(context.Context, trino.AsyncPollRequest) (trino.AsyncQueryPage, error)
	CancelAsync(context.Context, trino.AsyncPollRequest) (trino.CancelOutcome, error)
	ExplainQuery(context.Context, trino.ExplainRequest) (trino.ExplainProfile, error)
}

// Trino implements the forward-only statement protocol. Every POST/GET page
// is returned as a single drained observation with its post-page nextUri and
// protocol state, allowing jobs.Registry to atomically commit chunk+cursor.
type Trino struct {
	client trinoClient
}

func NewTrino(handler *trino.Handler) *Trino {
	if handler == nil {
		return nil
	}
	return &Trino{client: handler}
}

func newTrinoForClient(client trinoClient) *Trino {
	return &Trino{client: client}
}

func (a *Trino) Provider() string { return "trino" }

func (a *Trino) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{
		IdempotentSubmit:        false,
		DetachedRepoll:          false,
		RequiresContinuousDrain: true,
		NativePagination:        false,
		QueuedStateVisible:      true,
		// Confirmation requires consuming the next result page, which Poll owns
		// atomically. ConfirmCancel intentionally cannot confirm independently.
		CancelConfirmable:   false,
		MaxSafePollInterval: 30 * time.Second,
		ProfileBound:        45 * time.Second,
		ProgressSignals: []jobs.ProgressSignal{
			jobs.SignalBytesProcessed,
			jobs.SignalRowsProcessed,
			jobs.SignalSplits,
			jobs.SignalNativePercent,
		},
	}
}

func (a *Trino) Profile(ctx context.Context, req jobs.SubmitRequest) (jobs.QueryProfile, error) {
	return a.ProfileWithObserver(ctx, req, nil)
}

func (a *Trino) ProfileWithObserver(ctx context.Context, req jobs.SubmitRequest, obs jobs.ProfileObserver) (jobs.QueryProfile, error) {
	if a == nil || a.client == nil {
		return jobs.QueryProfile{}, errors.New("Trino query adapter is not configured")
	}
	scope := trinoScopeFromSubmit(req)
	profileTimeout := trinoProfileTimeout(ctx)
	applyTrinoSafetyCaps(&scope, jobs.Budgets{
		HardExecution: profileTimeout,
		MaxScanBytes:  req.Budgets.MaxScanBytes,
	})
	var onObservation func(trino.ExplainObservation) error
	if obs != nil {
		onObservation = func(native trino.ExplainObservation) error {
			handle, err := trinoHandleFromPage(native.Page, scope)
			if err != nil {
				return err
			}
			if err := obs.PublishProfileHandle(handle); err != nil {
				return err
			}
			return obs.PublishProfilePhase(native.Phase)
		}
	}
	profile, err := a.client.ExplainQuery(ctx, trino.ExplainRequest{
		SQL:               req.SQL,
		Host:              scope.host,
		Catalog:           scope.catalog,
		Schema:            scope.schema,
		Source:            scope.source,
		TraceToken:        scope.traceToken,
		ClientTags:        scope.clientTags,
		SessionProperties: scope.sessionProperties,
		ProtocolState:     scope.protocolState,
		OnObservation:     onObservation,
	})
	if err != nil {
		err = withTrinoRetryAfter(err)
		return jobs.QueryProfile{Valid: false, Error: err.Error()}, err
	}
	plan := strings.TrimSpace(profile.Distributed.Text)
	if len(plan) > 16<<10 {
		plan = plan[:16<<10] + "…"
	}
	return jobs.QueryProfile{
		Valid:           profile.Validated,
		EstimateIsExact: false,
		PlanSummary:     plan,
		Warnings:        append([]string(nil), profile.Warnings...),
		ProviderDetails: map[string]any{
			"estimate_quality": profile.EstimateQuality,
			"validate":         profile.Validate,
			"io":               profile.IO,
			"distributed":      profile.Distributed,
			"protocol_state":   profile.ProtocolState,
		},
	}, nil
}

func trinoProfileTimeout(ctx context.Context) time.Duration {
	const maximum = 45 * time.Second
	bound := maximum
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < bound {
			bound = remaining
		}
	}
	return bound
}

func (a *Trino) CancelProfile(ctx context.Context, handle jobs.NativeHandle) error {
	return a.Cancel(ctx, handle)
}

func (a *Trino) Submit(ctx context.Context, req jobs.SubmitRequest) (jobs.SubmitResult, error) {
	if a == nil || a.client == nil {
		return jobs.SubmitResult{}, jobs.SubmitNotDelivered(errors.New("Trino query adapter is not configured"))
	}
	scope := trinoScopeFromSubmit(req)
	page, err := a.client.SubmitAsync(ctx, trino.AsyncQueryRequest{
		SQL:               req.SQL,
		Host:              scope.host,
		Catalog:           scope.catalog,
		Schema:            scope.schema,
		Source:            scope.source,
		TraceToken:        scope.traceToken,
		ClientTags:        scope.clientTags,
		SessionProperties: scope.sessionProperties,
		ProtocolState:     scope.protocolState,
	})
	if err != nil {
		// Trino has no idempotency token. Any POST transport failure is
		// conservatively ambiguous; the registry will never blindly retry it.
		return jobs.SubmitResult{}, withTrinoRetryAfter(err)
	}
	if strings.TrimSpace(page.QueryID) == "" {
		return jobs.SubmitResult{}, errors.New("Trino accepted the query but returned no query id")
	}
	handle, handleErr := trinoHandleFromPage(page, trinoHandleScope{
		host:              scope.host,
		catalog:           scope.catalog,
		schema:            scope.schema,
		source:            scope.source,
		traceToken:        scope.traceToken,
		clientTags:        scope.clientTags,
		sessionProperties: scope.sessionProperties,
	})
	if handleErr != nil {
		return jobs.SubmitResult{}, handleErr
	}
	initial := trinoStatus(page, handle)
	return jobs.SubmitResult{Handle: handle, Initial: &initial}, nil
}

func (a *Trino) Poll(ctx context.Context, handle jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	request, err := parseTrinoHandle(handle)
	if err != nil {
		return jobs.StatusSnapshot{}, err
	}
	page, err := a.client.PollAsync(ctx, request)
	if err != nil {
		var upstream trino.UpstreamError
		if errors.As(err, &upstream) && (upstream.StatusCode == http.StatusNotFound || upstream.StatusCode == http.StatusGone) {
			return jobs.StatusSnapshot{}, jobs.PermanentObservationLoss(fmt.Errorf("Trino result cursor is no longer available (HTTP %d): %w", upstream.StatusCode, err))
		}
		return jobs.StatusSnapshot{}, withTrinoRetryAfter(err)
	}
	next, err := trinoHandleFromPage(page, trinoHandleScope{
		host:              request.Host,
		catalog:           request.Catalog,
		schema:            request.Schema,
		source:            request.Source,
		traceToken:        request.TraceToken,
		clientTags:        request.ClientTags,
		sessionProperties: request.SessionProperties,
	})
	if err != nil {
		return jobs.StatusSnapshot{}, err
	}
	if next.ID == "" {
		next.ID = handle.ID
	}
	return trinoStatus(page, next), nil
}

func (a *Trino) Fetch(context.Context, jobs.NativeHandle, jobs.FetchCursor) (jobs.ResultPage, error) {
	return jobs.ResultPage{}, jobs.ErrUseMaterializedResults
}

func (a *Trino) Cancel(ctx context.Context, handle jobs.NativeHandle) error {
	request, err := parseTrinoHandle(handle)
	if err != nil {
		return err
	}
	outcome, err := a.client.CancelAsync(ctx, request)
	if err != nil {
		return withTrinoRetryAfter(err)
	}
	if !outcome.Accepted {
		return errors.New("Trino did not accept the full-query cancellation request")
	}
	return nil
}

func (a *Trino) ConfirmCancel(context.Context, jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	// Confirming via nextUri GET consumes the next result page. The monitor
	// immediately calls Poll after this hook and must own that observation so
	// its rows and advancing cursor are committed atomically. Returning false
	// here is intentional, not an optimistic cancellation verdict.
	return false, jobs.StatusSnapshot{}, nil
}

func (a *Trino) Adopt(_ context.Context, handle jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	// A GET is a destructive/advancing read in Trino's protocol, so adoption
	// performs structural and same-origin validation only. The monitor owns the
	// first post-restart GET and its atomic drain commit.
	request, err := parseTrinoHandle(handle)
	if err != nil {
		return jobs.AdoptOutcomeOrphaned, jobs.StatusSnapshot{Error: err.Error()}, nil
	}
	if err := validateTrinoCursorOrigin(request.Host, request.NextURI); err != nil {
		return jobs.AdoptOutcomeOrphaned, jobs.StatusSnapshot{Error: err.Error()}, nil
	}
	return jobs.AdoptOutcomeAdopted, jobs.StatusSnapshot{
		State:         jobs.ProviderStateUnknown,
		UpdatedHandle: &handle,
	}, nil
}

type trinoHandleScope struct {
	host              string
	catalog           string
	schema            string
	source            string
	traceToken        string
	clientTags        []string
	sessionProperties map[string]string
	protocolState     trino.ProtocolState
}

func trinoScopeFromSubmit(req jobs.SubmitRequest) trinoHandleScope {
	scope := trinoHandleScope{
		host:              firstNonBlank(metadataString(req.Metadata, "host"), req.ConnectionID),
		catalog:           sessionString(req.Session, req.Metadata, "catalog"),
		schema:            sessionString(req.Session, req.Metadata, "schema"),
		source:            firstNonBlank(metadataString(req.Metadata, "source"), "ai-analyst-long-horizon"),
		traceToken:        metadataString(req.Metadata, "trace_token"),
		clientTags:        metadataStrings(req.Metadata, "client_tags"),
		sessionProperties: metadataStringMap(req.Metadata, "session_properties"),
	}
	_ = decodeMetadataJSON(req.Metadata, "protocol_state", &scope.protocolState)
	for key, value := range req.Session {
		switch key {
		case "catalog", "schema", "source", "trace_token":
			continue
		default:
			if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
				if scope.sessionProperties == nil {
					scope.sessionProperties = map[string]string{}
				}
				scope.sessionProperties[key] = value
			}
		}
	}
	applyTrinoSafetyCaps(&scope, req.Budgets)
	return scope
}

func applyTrinoSafetyCaps(scope *trinoHandleScope, budgets jobs.Budgets) {
	if scope == nil {
		return
	}
	if scope.sessionProperties == nil {
		scope.sessionProperties = map[string]string{}
	}
	for _, property := range []string{"query_max_run_time", "query_max_execution_time"} {
		applyTrinoDurationCap(scope.sessionProperties, scope.protocolState.SessionProperties, property, budgets.HardExecution)
	}
	applyTrinoDataSizeCap(
		scope.sessionProperties,
		scope.protocolState.SessionProperties,
		"query_max_scan_physical_bytes",
		budgets.MaxScanBytes,
	)
}

func applyTrinoDurationCap(controls, protocol map[string]string, property string, maximum time.Duration) {
	if maximum <= 0 {
		return
	}
	controlRaw := strings.TrimSpace(controls[property])
	protocolRaw := strings.TrimSpace(protocol[property])
	control, hasControl := parseTrinoDuration(controlRaw)
	provider, hasProtocol := parseTrinoDuration(protocolRaw)
	if controlRaw != "" && !hasControl {
		// Preserve an explicit unparseable value. The coordinator will reject
		// it rather than silently running without the intended restriction.
		return
	}
	if protocolRaw != "" && !hasProtocol {
		// Let the invalid protocol state reach Trino instead of masking it with
		// a new control and accidentally accepting unintended session state.
		delete(controls, property)
		return
	}
	if hasProtocol && provider <= maximum && (!hasControl || provider <= control) {
		delete(controls, property)
		return
	}
	if hasControl && control <= maximum && (!hasProtocol || control < provider) {
		return
	}
	controls[property] = formatTrinoDurationCap(maximum)
}

func applyTrinoDataSizeCap(controls, protocol map[string]string, property string, maximumBytes int64) {
	if maximumBytes <= 0 {
		return
	}
	controlRaw := strings.TrimSpace(controls[property])
	protocolRaw := strings.TrimSpace(protocol[property])
	control, hasControl := parseTrinoDataSize(controlRaw)
	provider, hasProtocol := parseTrinoDataSize(protocolRaw)
	if controlRaw != "" && !hasControl {
		return
	}
	if protocolRaw != "" && !hasProtocol {
		delete(controls, property)
		return
	}
	if hasProtocol && provider <= maximumBytes && (!hasControl || provider <= control) {
		delete(controls, property)
		return
	}
	if hasControl && control <= maximumBytes && (!hasProtocol || control < provider) {
		return
	}
	controls[property] = strconv.FormatInt(maximumBytes, 10) + "B"
}

func formatTrinoDurationCap(maximum time.Duration) string {
	milliseconds := maximum.Milliseconds()
	if milliseconds < 1 {
		milliseconds = 1
	}
	return strconv.FormatInt(milliseconds, 10) + "ms"
}

func parseTrinoDuration(raw string) (time.Duration, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return 0, false
	}
	if strings.HasSuffix(value, "d") {
		days, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(value, "d")), 64)
		if err != nil || days < 0 {
			return 0, false
		}
		nanoseconds := days * float64(24*time.Hour)
		if math.IsInf(nanoseconds, 0) || nanoseconds > float64(math.MaxInt64) {
			return 0, false
		}
		return time.Duration(nanoseconds), true
	}
	// Trino accepts us while time.ParseDuration spells that unit µs.
	if strings.HasSuffix(value, "us") {
		value = strings.TrimSuffix(value, "us") + "µs"
	}
	parsed, err := time.ParseDuration(value)
	return parsed, err == nil && parsed >= 0
}

func parseTrinoDataSize(raw string) (int64, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return 0, false
	}
	units := []struct {
		suffix string
		bytes  float64
	}{
		{suffix: "pb", bytes: 1 << 50},
		{suffix: "tb", bytes: 1 << 40},
		{suffix: "gb", bytes: 1 << 30},
		{suffix: "mb", bytes: 1 << 20},
		{suffix: "kb", bytes: 1 << 10},
		{suffix: "b", bytes: 1},
	}
	for _, unit := range units {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSpace(strings.TrimSuffix(value, unit.suffix))
		amount, err := strconv.ParseFloat(number, 64)
		if err != nil || amount < 0 {
			return 0, false
		}
		bytes := amount * unit.bytes
		if math.IsInf(bytes, 0) || bytes > float64(math.MaxInt64) {
			return 0, false
		}
		return int64(math.Ceil(bytes)), true
	}
	return 0, false
}

func trinoHandleFromPage(page trino.AsyncQueryPage, scope trinoHandleScope) (jobs.NativeHandle, error) {
	host := strings.TrimSpace(scope.host)
	if host == "" {
		host = trinoOrigin(firstNonBlank(page.RequestURI, page.NextURI, page.InfoURI))
	}
	preserveStricterTrinoProtocolCaps(scope.sessionProperties, page.ProtocolState.SessionProperties)
	extra := map[string]string{
		"next_uri":    strings.TrimSpace(page.NextURI),
		"info_uri":    strings.TrimSpace(page.InfoURI),
		"request_uri": strings.TrimSpace(page.RequestURI),
		"host":        host,
		"catalog":     strings.TrimSpace(scope.catalog),
		"schema":      strings.TrimSpace(scope.schema),
		"source":      strings.TrimSpace(scope.source),
		"trace_token": strings.TrimSpace(scope.traceToken),
	}
	if err := putJSON(extra, "client_tags", scope.clientTags); err != nil {
		return jobs.NativeHandle{}, err
	}
	if err := putJSON(extra, "session_properties", scope.sessionProperties); err != nil {
		return jobs.NativeHandle{}, err
	}
	if err := putJSON(extra, "protocol_state", page.ProtocolState); err != nil {
		return jobs.NativeHandle{}, err
	}
	if err := putJSON(extra, "columns", page.Columns); err != nil {
		return jobs.NativeHandle{}, err
	}
	return jobs.NativeHandle{
		Provider: "trino",
		ID:       strings.TrimSpace(page.QueryID),
		Location: host,
		Extra:    extra,
	}, nil
}

func preserveStricterTrinoProtocolCaps(controls, protocol map[string]string) {
	for _, property := range []string{"query_max_run_time", "query_max_execution_time"} {
		control, controlOK := parseTrinoDuration(controls[property])
		provider, providerOK := parseTrinoDuration(protocol[property])
		if controlOK && providerOK && provider <= control {
			delete(controls, property)
		}
	}
	control, controlOK := parseTrinoDataSize(controls["query_max_scan_physical_bytes"])
	provider, providerOK := parseTrinoDataSize(protocol["query_max_scan_physical_bytes"])
	if controlOK && providerOK && provider <= control {
		delete(controls, "query_max_scan_physical_bytes")
	}
}

func trinoOrigin(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

func parseTrinoHandle(handle jobs.NativeHandle) (trino.AsyncPollRequest, error) {
	request := trino.AsyncPollRequest{
		QueryID:    strings.TrimSpace(handle.ID),
		NextURI:    strings.TrimSpace(handle.Extra["next_uri"]),
		Host:       firstNonBlank(handle.Extra["host"], handle.Location),
		Catalog:    handle.Extra["catalog"],
		Schema:     handle.Extra["schema"],
		Source:     handle.Extra["source"],
		TraceToken: handle.Extra["trace_token"],
	}
	if request.QueryID == "" || request.NextURI == "" {
		return trino.AsyncPollRequest{}, errors.New("Trino native handle is missing query_id or next_uri")
	}
	if err := getJSON(handle.Extra, "client_tags", &request.ClientTags); err != nil {
		return trino.AsyncPollRequest{}, err
	}
	if err := getJSON(handle.Extra, "session_properties", &request.SessionProperties); err != nil {
		return trino.AsyncPollRequest{}, err
	}
	if err := getJSON(handle.Extra, "protocol_state", &request.ProtocolState); err != nil {
		return trino.AsyncPollRequest{}, err
	}
	if err := getJSON(handle.Extra, "columns", &request.Columns); err != nil {
		return trino.AsyncPollRequest{}, err
	}
	return request, nil
}

func trinoStatus(page trino.AsyncQueryPage, postHandle jobs.NativeHandle) jobs.StatusSnapshot {
	state := jobs.ProviderStateRunning
	errorText := ""
	if page.Error != nil {
		errorText = strings.TrimSpace(page.Error.Error())
		if trinoCancelledError(page.Error) {
			state = jobs.ProviderStateCancelled
		} else {
			state = jobs.ProviderStateFailed
		}
	} else if page.Completed {
		state = jobs.ProviderStateSucceeded
	} else if page.Stats.Queued || strings.Contains(strings.ToUpper(page.Stats.State), "QUEUE") {
		state = jobs.ProviderStateQueued
	} else if !page.Stats.Scheduled {
		state = jobs.ProviderStatePending
	}
	bytesProcessed := page.Stats.ProcessedBytes
	rowsProcessed := page.Stats.ProcessedRows
	splitsDone := int64(page.Stats.CompletedSplits)
	splitsTotal := int64(page.Stats.TotalSplits)
	progress := page.Stats.ProgressPercentage
	if progress == nil {
		progress = page.Stats.RunningPercentage
	}
	resultPage := boundedResultPage(page.Rows, trinoSchema(page.Columns), 0, 0)
	resultPage.CursorAfter = &postHandle
	resultPage.Metadata = map[string]any{
		"query_id":             page.QueryID,
		"request_uri":          page.RequestURI,
		"next_uri":             page.NextURI,
		"info_uri":             page.InfoURI,
		"warnings":             page.Warnings,
		"protocol_state_delta": page.ProtocolStateDelta,
	}
	return jobs.StatusSnapshot{
		State:                 state,
		RawState:              page.Stats.State,
		Error:                 errorText,
		BytesProcessed:        &bytesProcessed,
		RowsProcessed:         &rowsProcessed,
		SplitsDone:            &splitsDone,
		SplitsTotal:           &splitsTotal,
		NativeProgressPercent: progress,
		UpdatedHandle:         &postHandle,
		DrainedPages:          []jobs.ResultPage{resultPage},
		Telemetry: map[string]any{
			"stats":                page.Stats,
			"warnings":             page.Warnings,
			"info_uri":             page.InfoURI,
			"request_uri":          page.RequestURI,
			"next_uri":             page.NextURI,
			"partial_cancel_uri":   page.PartialCancelURI,
			"scheduled":            page.Stats.Scheduled,
			"queued":               page.Stats.Queued,
			"running_splits":       page.Stats.RunningSplits,
			"queued_splits":        page.Stats.QueuedSplits,
			"peak_memory_bytes":    page.Stats.PeakMemoryBytes,
			"spilled_bytes":        page.Stats.SpilledBytes,
			"protocol_state":       page.ProtocolState,
			"protocol_state_delta": page.ProtocolStateDelta,
		},
	}
}

func withTrinoRetryAfter(err error) error {
	var upstream trino.UpstreamError
	if errors.As(err, &upstream) {
		return jobs.WithRetryAfter(err, upstream.RetryAfter)
	}
	return err
}

func trinoSchema(columns []trino.StatementColumn) []map[string]any {
	schema := make([]map[string]any, 0, len(columns))
	for _, column := range columns {
		item := map[string]any{"name": column.Name, "type": column.Type}
		if len(column.TypeSignature) > 0 {
			var signature any
			if json.Unmarshal(column.TypeSignature, &signature) == nil {
				item["type_signature"] = signature
			}
		}
		schema = append(schema, item)
	}
	return schema
}

func trinoCancelledError(queryErr *trino.QueryError) bool {
	if queryErr == nil {
		return false
	}
	text := strings.ToUpper(strings.Join([]string{queryErr.ErrorName, queryErr.ErrorType, queryErr.Message}, " "))
	return strings.Contains(text, "CANCEL") || strings.Contains(text, "USER_ABORT")
}

func validateTrinoCursorOrigin(host, nextURI string) error {
	base, err := url.Parse(strings.TrimSpace(host))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return errors.New("Trino native handle has an invalid host")
	}
	next, err := url.Parse(strings.TrimSpace(nextURI))
	if err != nil || next.Scheme == "" || next.Host == "" {
		return errors.New("Trino native handle has an invalid next_uri")
	}
	if !strings.EqualFold(base.Scheme, next.Scheme) || !strings.EqualFold(base.Host, next.Host) {
		return errors.New("Trino next_uri origin does not match its configured host")
	}
	return nil
}

func metadataStrings(metadata map[string]any, key string) []string {
	if metadata == nil {
		return nil
	}
	switch values := metadata[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text := strings.TrimSpace(fmt.Sprint(value)); text != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		parts := strings.Split(values, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
		return out
	default:
		return nil
	}
}

func metadataStringMap(metadata map[string]any, key string) map[string]string {
	if metadata == nil {
		return nil
	}
	switch values := metadata[key].(type) {
	case map[string]string:
		out := make(map[string]string, len(values))
		for name, value := range values {
			out[name] = value
		}
		return out
	case map[string]any:
		out := make(map[string]string, len(values))
		for name, value := range values {
			out[name] = strings.TrimSpace(fmt.Sprint(value))
		}
		return out
	default:
		return nil
	}
}

func decodeMetadataJSON(metadata map[string]any, key string, destination any) error {
	if metadata == nil || metadata[key] == nil {
		return nil
	}
	encoded, err := json.Marshal(metadata[key])
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, destination)
}
