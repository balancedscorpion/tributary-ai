package bigquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sqlutil"
)

// Handler implements the small BigQuery surface area the UI needs.
//
// We intentionally use the BigQuery REST API + ADC token discovery to avoid
// pulling large external dependencies into this local-only binary.
type Handler struct {
	Settings *config.Store
	Secrets  *secrets.Store

	httpc *http.Client

	tokenMu     sync.Mutex
	cachedToken string
	cachedUntil time.Time
	cachedKey   string
	refreshing  bool
	refreshWait chan struct{}

	reauthMu sync.Mutex
	reauth   *adcReauthRun
}

func NewHandler(settings *config.Store, secretsStore *secrets.Store) *Handler {
	return &Handler{
		Settings: settings,
		Secrets:  secretsStore,
		httpc: &http.Client{
			Timeout: 0, // rely on request context timeouts; needed for streamed/polled responses
		},
	}
}

func (h *Handler) credentialsPathOverrideFromSettings() string {
	if h.Settings == nil {
		return ""
	}
	s := h.Settings.Get()
	if s.GoogleCredentialsPath == nil {
		return ""
	}
	return strings.TrimSpace(*s.GoogleCredentialsPath)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/auth/application-default/start"):
		h.handleADCReauthStart(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/auth/application-default/status"):
		h.handleADCReauthStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/auth/application-default/cancel"):
		h.handleADCReauthCancel(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/status"):
		h.handleStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/test"):
		h.handleTest(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/preflight"):
		h.handlePreflight(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/query"):
		h.handleQuery(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/datasets"):
		h.handleDatasets(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/tables"):
		h.handleTables(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/bigquery/schema"):
		h.handleSchema(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	override := h.credentialsPathOverrideFromSettings()
	info := credentialsFilePathInfo()
	if override != "" {
		info = credentialsPathInfo{Path: override, Source: "custom"}
	}
	resolved := resolveFilePath(info.Path)
	configured := ""
	if override != "" {
		// Preserve the exact configured string (users often enter "~/" paths).
		configured = strings.TrimSpace(override)
	}
	adc := map[string]any{
		"detected": false,
		// credentials_path is the path we actually check on disk (post "~" expansion).
		"credentials_path":            resolved,
		"credentials_path_resolved":   resolved,
		"credentials_path_configured": configured,
		"credentials_source":          info.Source,
		"credentials_present":         false,
		"credentials_readable":        false,
		"custom_path_configured":      override != "",
	}

	if strings.TrimSpace(resolved) == "" {
		adc["error"] = "no ADC credentials path resolved"
		writeJSON(w, http.StatusOK, map[string]any{"adc": adc})
		return
	}

	if st, err := os.Stat(resolved); err == nil && st != nil && !st.IsDir() {
		adc["credentials_present"] = true
	} else {
		// Don't treat "missing" as an error; it's a normal state.
		writeJSON(w, http.StatusOK, map[string]any{"adc": adc})
		return
	}

	b, err := os.ReadFile(resolved)
	if err != nil {
		adc["error"] = err.Error()
		writeJSON(w, http.StatusOK, map[string]any{"adc": adc})
		return
	}
	adc["credentials_readable"] = true

	var cf credentialsFile
	if err := json.Unmarshal(b, &cf); err != nil {
		adc["error"] = "invalid ADC credentials JSON: " + err.Error()
		writeJSON(w, http.StatusOK, map[string]any{"adc": adc})
		return
	}
	typ := strings.TrimSpace(cf.Type)
	if typ != "" {
		adc["credentials_type"] = typ
	}
	if pid := strings.TrimSpace(cf.ProjectID); pid != "" {
		adc["credentials_project_id"] = pid
	}
	if pid := strings.TrimSpace(cf.QuotaProjectID); pid != "" {
		adc["quota_project_id"] = pid
	}
	if email := strings.TrimSpace(cf.ClientEmail); email != "" {
		adc["client_email"] = email
	}

	project := detectedProjectFromCredentials(cf)
	if project.ProjectID == "" {
		project = detectedProjectFromCloudSDKConfig()
		if project.ProjectID != "" {
			adc["cloud_sdk_project_id"] = project.ProjectID
		}
		if project.ConfigName != "" {
			adc["cloud_sdk_config_name"] = project.ConfigName
		}
	}
	if project.ProjectID != "" {
		adc["project_id"] = project.ProjectID
		adc["detected_project_id"] = project.ProjectID
		adc["project_source"] = project.ProjectSource
	}

	adc["detected"] = true
	writeJSON(w, http.StatusOK, map[string]any{"adc": adc})
}

func (h *Handler) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	statusMsg := "Missing project_id"
	statusOk := false
	pid := ""
	defer func() {
		h.persistTestStatus(context.Background(), pid, statusMsg, statusOk)
	}()
	var body struct {
		ProjectID string `json:"project_id,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		statusMsg = "Invalid JSON body"
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Invalid JSON body",
			},
		})
		return
	}
	pid = strings.TrimSpace(body.ProjectID)
	if pid == "" {
		pid = strings.TrimSpace(h.projectIDFromSettings())
	}
	if pid == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": statusMsg,
			},
		})
		return
	}

	// Minimal connectivity check: list datasets with maxResults=1.
	upURL := fmt.Sprintf(
		"https://bigquery.googleapis.com/bigquery/v2/projects/%s/datasets?maxResults=1",
		url.PathEscape(pid),
	)
	var out bqDatasetList
	if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &out); err != nil {
		statusMsg = err.Error()
		statusOk = false
		writeJSON(w, httpStatusForErr(err), map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": err.Error(),
			},
		})
		return
	}

	visible := 0
	if out.Datasets != nil {
		visible = len(out.Datasets)
	}
	statusMsg = "BigQuery connection verified."
	statusOk = true
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"project_id":       pid,
		"datasets_visible": visible,
	})
}

func (h *Handler) persistTestStatus(ctx context.Context, projectID, message string, ok bool) {
	if h.Settings == nil {
		return
	}
	pid := strings.TrimSpace(projectID)
	msg := strings.TrimSpace(message)
	s := h.Settings.Get()
	if s.DataSources == nil {
		off := false
		s.DataSources = &config.DatasourceConnections{
			BigQuery: &config.DatasourceConnectionState{
				Enabled: &off,
			},
			Databricks: &config.DatasourceConnectionState{
				Enabled: &off,
			},
		}
	}
	if s.DataSources.BigQuery == nil {
		off := false
		s.DataSources.BigQuery = &config.DatasourceConnectionState{
			Enabled: &off,
		}
	} else if s.DataSources.BigQuery.Enabled == nil {
		off := false
		s.DataSources.BigQuery.Enabled = &off
	}

	ds := s.DataSources.BigQuery
	now := time.Now().UnixMilli()
	ds.LastCheckedAt = &now
	ds.LastCheckOk = &ok
	if pid == "" {
		ds.LastProjectID = nil
	} else {
		ds.LastProjectID = &pid
	}
	if msg == "" {
		ds.LastCheckMessage = nil
	} else {
		ds.LastCheckMessage = &msg
	}

	_ = h.Settings.Save(s)
}

func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	var body struct {
		ProjectID string `json:"project_id,omitempty"`
		SQL       string `json:"sql"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Invalid JSON body",
			},
		})
		return
	}

	pid := strings.TrimSpace(body.ProjectID)
	if pid == "" {
		pid = strings.TrimSpace(h.projectIDFromSettings())
	}
	sql := strings.TrimSpace(body.SQL)
	if pid == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Missing project_id",
			},
		})
		return
	}
	if sql == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Missing sql",
			},
		})
		return
	}
	if !sqlutil.IsReadOnlySQL(sql) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Only read-only SELECT/WITH queries are allowed",
			},
		})
		return
	}

	bytesProcessed, err := h.preflightQuery(ctx, pid, sql)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": err.Error(),
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"project_id":      pid,
		"bytes_processed": bytesProcessed,
	})
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	var body struct {
		ProjectID string `json:"project_id,omitempty"`
		SQL       string `json:"sql"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		writeBQError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	pid := strings.TrimSpace(body.ProjectID)
	if pid == "" {
		pid = strings.TrimSpace(h.projectIDFromSettings())
	}
	body.SQL = strings.TrimSpace(body.SQL)
	if pid == "" {
		writeBQError(w, http.StatusBadRequest, "Missing project_id")
		return
	}
	if body.SQL == "" {
		writeBQError(w, http.StatusBadRequest, "Missing sql")
		return
	}
	if !sqlutil.IsReadOnlySQL(body.SQL) {
		writeBQError(w, http.StatusBadRequest, "Only read-only SELECT/WITH queries are allowed")
		return
	}

	result, err := h.runQuery(ctx, pid, body.SQL)
	if err != nil {
		writeBQError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": result.Rows,
		"query_metadata": map[string]any{
			"provider":              "bigquery",
			"duration_ms":           result.DurationMs,
			"total_bytes_processed": result.TotalBytesProcessed,
			"job_id":                result.JobID,
			"job_location":          result.JobLocation,
			"schema":                result.Schema,
			"cache_hit":             result.CacheHit,
		},
	})
}

func (h *Handler) handleDatasets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	pid := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if pid == "" {
		pid = strings.TrimSpace(h.projectIDFromSettings())
	}
	if pid == "" {
		writeBQError(w, http.StatusBadRequest, "Missing project_id")
		return
	}

	datasets, err := h.listDatasets(ctx, pid)
	if err != nil {
		writeBQError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"datasets": datasets})
}

func (h *Handler) handleTables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	q := r.URL.Query()
	pid := strings.TrimSpace(q.Get("project_id"))
	if pid == "" {
		pid = strings.TrimSpace(h.projectIDFromSettings())
	}
	did := strings.TrimSpace(q.Get("dataset_id"))
	if pid == "" {
		writeBQError(w, http.StatusBadRequest, "Missing project_id")
		return
	}
	if did == "" {
		writeBQError(w, http.StatusBadRequest, "Missing dataset_id")
		return
	}

	tables, err := h.listTables(ctx, pid, did)
	if err != nil {
		writeBQError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

func (h *Handler) handleSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	q := r.URL.Query()
	pid := strings.TrimSpace(q.Get("project_id"))
	if pid == "" {
		pid = strings.TrimSpace(h.projectIDFromSettings())
	}
	did := strings.TrimSpace(q.Get("dataset_id"))
	tid := strings.TrimSpace(q.Get("table_id"))
	if pid == "" {
		writeBQError(w, http.StatusBadRequest, "Missing project_id")
		return
	}
	if did == "" || tid == "" {
		writeBQError(w, http.StatusBadRequest, "Missing dataset_id or table_id")
		return
	}

	schema, err := h.getTableSchema(ctx, pid, did, tid)
	if err != nil {
		writeBQError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schema": schema})
}

func (h *Handler) projectIDFromSettings() string {
	if h.Settings == nil {
		return ""
	}
	s := h.Settings.Get()
	if s.GoogleProjectID != nil {
		pid := strings.TrimSpace(*s.GoogleProjectID)
		if pid != "" {
			return pid
		}
	}
	if s.DataSources == nil || s.DataSources.BigQuery == nil || s.DataSources.BigQuery.LastProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*s.DataSources.BigQuery.LastProjectID)
}

type bqDatasetList struct {
	Datasets []struct {
		DatasetReference struct {
			DatasetID string `json:"datasetId"`
		} `json:"datasetReference"`
		Location string `json:"location,omitempty"`
	} `json:"datasets"`
}

func (h *Handler) listDatasets(ctx context.Context, projectID string) ([]map[string]any, error) {
	upURL := fmt.Sprintf(
		"https://bigquery.googleapis.com/bigquery/v2/projects/%s/datasets",
		url.PathEscape(projectID),
	)
	var out bqDatasetList
	if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &out); err != nil {
		return nil, err
	}
	datasets := make([]map[string]any, 0, len(out.Datasets))
	for _, d := range out.Datasets {
		id := strings.TrimSpace(d.DatasetReference.DatasetID)
		if id == "" {
			continue
		}
		datasets = append(datasets, map[string]any{
			"id":       id,
			"location": strings.TrimSpace(d.Location),
		})
	}
	return datasets, nil
}

type bqTableList struct {
	Tables []struct {
		TableReference struct {
			TableID string `json:"tableId"`
		} `json:"tableReference"`
	} `json:"tables"`
}

func (h *Handler) listTables(ctx context.Context, projectID, datasetID string) ([]string, error) {
	upURL := fmt.Sprintf(
		"https://bigquery.googleapis.com/bigquery/v2/projects/%s/datasets/%s/tables",
		url.PathEscape(projectID),
		url.PathEscape(datasetID),
	)
	var out bqTableList
	if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &out); err != nil {
		return nil, err
	}
	tables := make([]string, 0, len(out.Tables))
	for _, t := range out.Tables {
		id := strings.TrimSpace(t.TableReference.TableID)
		if id == "" {
			continue
		}
		tables = append(tables, id)
	}
	return tables, nil
}

type bqTableField struct {
	Name        string         `json:"name,omitempty"`
	Type        string         `json:"type,omitempty"`
	Mode        string         `json:"mode,omitempty"`
	Description string         `json:"description,omitempty"`
	Fields      []bqTableField `json:"fields,omitempty"`
}

type bqTableGet struct {
	Schema struct {
		Fields []bqTableField `json:"fields"`
	} `json:"schema"`
}

func (h *Handler) getTableSchema(ctx context.Context, projectID, datasetID, tableID string) ([]bqTableField, error) {
	upURL := fmt.Sprintf(
		"https://bigquery.googleapis.com/bigquery/v2/projects/%s/datasets/%s/tables/%s",
		url.PathEscape(projectID),
		url.PathEscape(datasetID),
		url.PathEscape(tableID),
	)
	var out bqTableGet
	if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &out); err != nil {
		return nil, err
	}
	return out.Schema.Fields, nil
}

type bqQueryRequest struct {
	Query        string `json:"query"`
	UseLegacySQL bool   `json:"useLegacySql"`
	TimeoutMs    int    `json:"timeoutMs,omitempty"`
	DryRun       bool   `json:"dryRun,omitempty"`
	Location     string `json:"location,omitempty"`
}

type bqErrorProto struct {
	Message  string `json:"message,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Location string `json:"location,omitempty"`
}

type bqQueryResponse struct {
	JobComplete         bool   `json:"jobComplete,omitempty"`
	PageToken           string `json:"pageToken,omitempty"`
	TotalRows           string `json:"totalRows,omitempty"`
	TotalBytesProcessed string `json:"totalBytesProcessed,omitempty"`
	CacheHit            bool   `json:"cacheHit,omitempty"`

	Errors []bqErrorProto `json:"errors,omitempty"`

	JobReference struct {
		JobID     string `json:"jobId,omitempty"`
		Location  string `json:"location,omitempty"`
		ProjectID string `json:"projectId,omitempty"`
	} `json:"jobReference,omitempty"`

	Schema struct {
		Fields []bqTableField `json:"fields"`
	} `json:"schema,omitempty"`

	Rows []struct {
		F []struct {
			V any `json:"v"`
		} `json:"f"`
	} `json:"rows,omitempty"`
}

func (h *Handler) preflightQuery(ctx context.Context, projectID, sql string) (string, error) {
	profile, err := h.PreflightQuery(ctx, QueryPreflightRequest{ProjectID: projectID, SQL: sql})
	if err != nil {
		return "", err
	}
	return profile.EstimatedBytesText, nil
}

type queryResult struct {
	Rows                []map[string]any
	DurationMs          int64
	TotalBytesProcessed string
	JobID               string
	JobLocation         string
	Schema              []map[string]any
	CacheHit            bool
}

type QueryResult struct {
	Rows     []map[string]any `json:"rows,omitempty"`
	Schema   []map[string]any `json:"schema,omitempty"`
	Metadata map[string]any   `json:"metadata,omitempty"`
}

type SubmitResult struct {
	Completed bool             `json:"completed"`
	Rows      []map[string]any `json:"rows,omitempty"`
	Schema    []map[string]any `json:"schema,omitempty"`
	Metadata  map[string]any   `json:"metadata,omitempty"`

	JobID       string `json:"job_id,omitempty"`
	JobLocation string `json:"job_location,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
}

const (
	defaultAdaptiveWaitFirst  = 300 * time.Millisecond
	defaultAdaptiveWaitSecond = 800 * time.Millisecond
)

func (h *Handler) SubmitQuery(ctx context.Context, projectID, sql string, initialWait time.Duration) (*SubmitResult, error) {
	projectID = strings.TrimSpace(projectID)
	sql = strings.TrimSpace(sql)
	if projectID == "" {
		return nil, errors.New("missing project_id")
	}
	if sql == "" {
		return nil, errors.New("missing sql")
	}
	firstWait := initialWait
	if firstWait <= 0 {
		firstWait = defaultAdaptiveWaitFirst
	}
	if firstWait < 50*time.Millisecond {
		firstWait = 50 * time.Millisecond
	}

	jobID := DeterministicQueryJobID(fmt.Sprintf("legacy:%d:%s", time.Now().UnixNano(), sql), 0)
	handle := QueryJobHandle{ProjectID: projectID, JobID: jobID}
	useCache := true
	snapshot, err := h.InsertQueryJob(ctx, InsertQueryJobRequest{
		Handle:        handle,
		SQL:           sql,
		UseQueryCache: &useCache,
	})
	if err != nil {
		return nil, err
	}
	handle = snapshot.Handle
	jobID = handle.JobID
	location := handle.Location
	if snapshot.Terminal {
		if !snapshot.Succeeded {
			message := "BigQuery query failed"
			if snapshot.Error != nil && strings.TrimSpace(snapshot.Error.Message) != "" {
				message = strings.TrimSpace(snapshot.Error.Message)
			}
			return nil, errors.New(message)
		}
		materialized, materializeErr := h.MaterializeQueryResult(ctx, MaterializeQueryRequest{
			Handle:   handle,
			MaxRows:  2501,
			MaxBytes: 32 << 20,
		})
		if materializeErr != nil {
			return nil, materializeErr
		}
		return legacyBQSubmitResult(snapshot, materialized), nil
	}

	done, quickResult, err := h.waitForBigQueryJob(ctx, projectID, jobID, location, firstWait)
	if err != nil {
		return nil, err
	}
	if !done {
		done, quickResult, err = h.waitForBigQueryJob(ctx, projectID, jobID, location, defaultAdaptiveWaitSecond)
		if err != nil {
			return nil, err
		}
	}
	if done && quickResult != nil {
		md := cloneStringAnyMap(quickResult.Metadata)
		if md == nil {
			md = map[string]any{}
		}
		md["duration_ms"] = 0
		return &SubmitResult{
			Completed:   true,
			Rows:        quickResult.Rows,
			Schema:      quickResult.Schema,
			Metadata:    md,
			JobID:       jobID,
			JobLocation: location,
			ProjectID:   projectID,
		}, nil
	}

	return &SubmitResult{
		Completed: false,
		Metadata: map[string]any{
			"provider":        "bigquery",
			"job_id":          jobID,
			"job_location":    location,
			"project_id":      projectID,
			"state":           "RUNNING",
			"terminal_status": "",
		},
		JobID:       jobID,
		JobLocation: location,
		ProjectID:   projectID,
	}, nil
}

func (h *Handler) waitForBigQueryJob(ctx context.Context, projectID, jobID, location string, wait time.Duration) (bool, *QueryResult, error) {
	if wait <= 0 {
		return false, nil, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	for {
		if waitCtx.Err() != nil {
			if ctx.Err() != nil {
				return false, nil, ctx.Err()
			}
			return false, nil, nil
		}
		done, result, err := h.PollJob(waitCtx, projectID, jobID, location)
		if err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return false, nil, nil
			}
			return false, nil, err
		}
		if done {
			return true, result, nil
		}
		if err := sleepWithContext(waitCtx, 120*time.Millisecond); err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return false, nil, nil
			}
			return false, nil, err
		}
	}
}

func (h *Handler) PollJob(ctx context.Context, projectID, jobID, location string) (bool, *QueryResult, error) {
	projectID = strings.TrimSpace(projectID)
	jobID = strings.TrimSpace(jobID)
	location = strings.TrimSpace(location)
	if projectID == "" || jobID == "" {
		return false, nil, errors.New("project_id and job_id are required")
	}

	handle := QueryJobHandle{ProjectID: projectID, JobID: jobID, Location: location}
	snapshot, err := h.GetQueryJob(ctx, handle)
	if err != nil {
		return false, nil, err
	}
	if !snapshot.Terminal {
		return false, nil, nil
	}

	state := snapshot.State
	terminal := "completed"
	message := ""
	if !snapshot.Succeeded {
		if snapshot.Error != nil {
			message = strings.TrimSpace(snapshot.Error.Message)
		}
		if message == "" {
			message = "BigQuery job failed"
		}
		state = "FAILED"
		terminal = "failed"
		if snapshot.Cancelled {
			state = "CANCELLED"
			terminal = "cancelled"
		}
	}

	var rows []map[string]any
	var schema []map[string]any
	if snapshot.Succeeded {
		materialized, materializeErr := h.MaterializeQueryResult(ctx, MaterializeQueryRequest{
			Handle:   snapshot.Handle,
			MaxRows:  2501,
			MaxBytes: 32 << 20,
		})
		if materializeErr != nil {
			return true, nil, materializeErr
		}
		rows = materialized.Rows
		schema = materialized.Schema
	}

	out := &QueryResult{
		Rows:   rows,
		Schema: schema,
		Metadata: map[string]any{
			"provider":              "bigquery",
			"job_id":                jobID,
			"job_location":          location,
			"project_id":            projectID,
			"total_bytes_processed": strconv.FormatInt(snapshot.Statistics.TotalBytesProcessed, 10),
			"total_bytes_billed":    strconv.FormatInt(snapshot.Statistics.TotalBytesBilled, 10),
			"total_slot_ms":         snapshot.Statistics.TotalSlotMs,
			"cache_hit":             snapshot.Statistics.CacheHit,
			"state":                 state,
			"terminal_status":       terminal,
		},
	}
	if message != "" {
		out.Metadata["error"] = message
	}
	return true, out, nil
}

func (h *Handler) CancelJob(ctx context.Context, projectID, jobID, location string) error {
	projectID = strings.TrimSpace(projectID)
	jobID = strings.TrimSpace(jobID)
	location = strings.TrimSpace(location)
	if projectID == "" || jobID == "" {
		return errors.New("project_id and job_id are required")
	}

	_, err := h.RequestCancelQueryJob(ctx, QueryJobHandle{ProjectID: projectID, JobID: jobID, Location: location})
	return err
}

func (h *Handler) runQuery(ctx context.Context, projectID, sql string) (queryResult, error) {
	return h.runQueryLifecycle(ctx, projectID, sql, nil)
}

func (h *Handler) pollQueryResults(ctx context.Context, projectID, jobID, location string) (bqQueryResponse, error) {
	// Poll until ctx deadline; backoff modestly.
	deadline, hasDeadline := ctx.Deadline()
	_ = deadline
	for attempt := 0; ; attempt++ {
		if hasDeadline && time.Until(deadline) <= 2*time.Second {
			return bqQueryResponse{}, errors.New("query did not complete before timeout")
		}

		upURL := fmt.Sprintf(
			"https://bigquery.googleapis.com/bigquery/v2/projects/%s/queries/%s",
			url.PathEscape(projectID),
			url.PathEscape(jobID),
		)
		if location != "" {
			upURL += "?location=" + url.QueryEscape(location)
		}

		var resp bqQueryResponse
		if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &resp); err != nil {
			return bqQueryResponse{}, err
		}
		if resp.JobComplete || attempt >= 20 {
			return resp, nil
		}
		time.Sleep(time.Duration(150+attempt*50) * time.Millisecond)
	}
}

// QueryProgressFn is called during query execution to report progress.
type QueryProgressPhase string

const (
	queryProgressPhaseSubmitted QueryProgressPhase = "submitted"
	queryProgressPhasePolling   QueryProgressPhase = "polling"
	queryProgressPhaseComplete  QueryProgressPhase = "complete"
)

type QueryProgressFn func(phase string, meta map[string]any)

// RunQueryWithProgress mirrors runQuery but accepts an optional progress callback
// that emits metadata as the query executes.
func (h *Handler) RunQueryWithProgress(ctx context.Context, projectID, sql string, onProgress QueryProgressFn) (queryResult, error) {
	return h.runQueryLifecycle(ctx, projectID, sql, onProgress)
}

func (h *Handler) pollQueryResultsWithProgress(ctx context.Context, projectID, jobID, location string, onProgress QueryProgressFn) (bqQueryResponse, error) {
	deadline, hasDeadline := ctx.Deadline()
	_ = deadline
	for attempt := 0; ; attempt++ {
		if hasDeadline && time.Until(deadline) <= 2*time.Second {
			return bqQueryResponse{}, errors.New("query did not complete before timeout")
		}

		upURL := fmt.Sprintf(
			"https://bigquery.googleapis.com/bigquery/v2/projects/%s/queries/%s",
			url.PathEscape(projectID),
			url.PathEscape(jobID),
		)
		if location != "" {
			upURL += "?location=" + url.QueryEscape(location)
		}

		var resp bqQueryResponse
		if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &resp); err != nil {
			return bqQueryResponse{}, err
		}

		state := "RUNNING"
		if resp.JobComplete {
			state = "DONE"
		}
		if onProgress != nil {
			onProgress(string(queryProgressPhasePolling), map[string]any{
				"provider":        "bigquery",
				"state":           state,
				"job_id":          jobID,
				"location":        location,
				"project_id":      projectID,
				"url":             bigqueryConsoleURL(projectID, location, jobID),
				"bytes_processed": resp.TotalBytesProcessed,
			})
		}

		if resp.JobComplete || attempt >= 20 {
			return resp, nil
		}
		time.Sleep(time.Duration(150+attempt*50) * time.Millisecond)
	}
}

func bigqueryConsoleURL(projectID, location, jobID string) string {
	project := url.QueryEscape(strings.TrimSpace(projectID))
	job := url.QueryEscape(strings.TrimSpace(jobID))
	loc := strings.TrimSpace(location)

	if loc == "" {
		return fmt.Sprintf(
			"https://console.cloud.google.com/bigquery?project=%s&j=%s&page=queryresults",
			project, job,
		)
	}

	return fmt.Sprintf(
		"https://console.cloud.google.com/bigquery?project=%s&j=bq:%s:%s&page=queryresults",
		project, url.QueryEscape(loc), job,
	)
}

func queryResponseToRows(resp bqQueryResponse) []map[string]any {
	if len(resp.Rows) == 0 || len(resp.Schema.Fields) == 0 {
		return []map[string]any{}
	}

	fields := resp.Schema.Fields
	out := make([]map[string]any, 0, len(resp.Rows))
	for _, row := range resp.Rows {
		if len(row.F) == 0 {
			continue
		}
		obj := map[string]any{}
		for i, cell := range row.F {
			if i >= len(fields) {
				continue
			}
			f := fields[i]
			name := strings.TrimSpace(f.Name)
			if name == "" {
				name = fmt.Sprintf("col_%d", i)
			}
			obj[name] = coerceBQString(cell.V, f.Type)
		}
		out = append(out, obj)
	}
	return out
}

func bqSchemaToMaps(fields []bqTableField) []map[string]any {
	out := make([]map[string]any, 0, len(fields))
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		if name == "" {
			continue
		}
		col := map[string]any{
			"name": name,
		}
		if value := strings.TrimSpace(field.Type); value != "" {
			col["type"] = value
		}
		if value := strings.TrimSpace(field.Mode); value != "" {
			col["mode"] = value
		}
		if value := strings.TrimSpace(field.Description); value != "" {
			col["description"] = value
		}
		if len(field.Fields) > 0 {
			col["fields"] = bqSchemaToMaps(field.Fields)
		}
		out = append(out, col)
	}
	return out
}

func cloneStringAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func coerceBQString(v any, typ string) any {
	if v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return v
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	switch strings.ToUpper(strings.TrimSpace(typ)) {
	case "INTEGER", "INT64", "FLOAT", "FLOAT64", "NUMERIC", "BIGNUMERIC":
		var n json.Number = json.Number(s)
		if strings.ContainsAny(s, ".eE") {
			if f, err := n.Float64(); err == nil {
				return f
			}
			return s
		}
		if i, err := n.Int64(); err == nil {
			return i
		}
		if f, err := n.Float64(); err == nil {
			return f
		}
		return s
	case "BOOLEAN", "BOOL":
		if strings.EqualFold(s, "true") {
			return true
		}
		if strings.EqualFold(s, "false") {
			return false
		}
		return s
	default:
		return s
	}
}

func (h *Handler) doBQ(ctx context.Context, method, upstreamURL string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyBytes = b
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		token, err := h.accessToken(ctx)
		if err != nil {
			return err
		}

		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, upstreamURL, bodyReader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := h.httpc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to reach BigQuery API: %w", err)
			if attempt < 2 && shouldRetryTransportError(ctx, err) {
				if sleepErr := sleepWithContext(ctx, bqRetryDelay(attempt)); sleepErr == nil {
					continue
				}
			}
			return lastErr
		}

		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// Try to surface BigQuery error.message if present.
			msg := strings.TrimSpace(string(b))
			var parsed struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(b, &parsed); err == nil && strings.TrimSpace(parsed.Error.Message) != "" {
				msg = strings.TrimSpace(parsed.Error.Message)
			}
			if msg == "" {
				msg = fmt.Sprintf("BigQuery API error (HTTP %d)", resp.StatusCode)
			}
			he := httpError{
				status:     resp.StatusCode,
				msg:        msg,
				retryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			}
			lastErr = he

			if resp.StatusCode == http.StatusUnauthorized {
				h.invalidateCachedToken()
			}
			if attempt < 2 && isRetryableBQStatus(resp.StatusCode) {
				delay := bqRetryDelay(attempt)
				if he.retryAfter > delay {
					delay = he.retryAfter
				}
				if sleepErr := sleepWithContext(ctx, delay); sleepErr == nil {
					continue
				}
			}
			return he
		}

		if out == nil {
			return nil
		}
		if len(bytes.TrimSpace(b)) == 0 {
			return nil
		}
		if err := json.Unmarshal(b, out); err != nil {
			return err
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("BigQuery request failed")
}

func isRetryableBQStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusRequestTimeout || (status >= 500 && status <= 599)
}

func bqRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 200 * time.Millisecond
	case 1:
		return 600 * time.Millisecond
	default:
		return 1200 * time.Millisecond
	}
}

func shouldRetryTransportError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	return true
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (h *Handler) accessToken(ctx context.Context) (string, error) {
	// Optional OAuth token stored in the encrypted secrets store.
	if h.Secrets != nil {
		// Ignore locked secrets store errors; BigQuery can still use ADC.
		if mode, tok, err := h.Secrets.GetBigQueryToken(); err == nil {
			if strings.TrimSpace(mode) == "oauth_token" && strings.TrimSpace(tok) != "" {
				return strings.TrimSpace(tok), nil
			}
		}
	}

	cacheKey := h.tokenCacheKey()
	for {
		if tok := h.cachedTokenForKey(cacheKey); tok != "" {
			return tok, nil
		}

		waitCh, refresh := h.beginTokenRefresh()
		if !refresh {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-waitCh:
				// Another goroutine finished refresh; loop and re-check cache.
				continue
			}
		}

		tok, exp, err := h.resolveAccessToken(ctx)
		h.endTokenRefresh()
		if err != nil {
			return "", err
		}
		h.cacheToken(cacheKey, tok, exp)
		return tok, nil
	}
}

func (h *Handler) resolveAccessToken(ctx context.Context) (string, time.Time, error) {
	var attemptErrs []error
	recordErr := func(err error) {
		if err != nil {
			attemptErrs = append(attemptErrs, err)
		}
	}

	// 1) Custom ADC credentials path (if configured in settings).
	if p := h.credentialsPathOverrideFromSettings(); p != "" {
		if tok, exp, err := accessTokenFromCredentialsFileAtPath(ctx, h.httpc, bqScopes, p); err == nil {
			return tok, exp, nil
		} else {
			recordErr(err)
		}
	}

	// 2) ADC credentials file or GOOGLE_APPLICATION_CREDENTIALS.
	if tok, exp, err := accessTokenFromCredentialsFile(ctx, h.httpc, bqScopes); err == nil {
		return tok, exp, nil
	} else {
		recordErr(err)
	}

	// 3) GCP metadata server (when running on GCE / GKE).
	if tok, exp, err := accessTokenFromMetadata(ctx, h.httpc); err == nil {
		return tok, exp, nil
	} else {
		recordErr(err)
	}

	// 4) gcloud (best-effort fallback; not required for Docker).
	if tok, exp, err := accessTokenFromGcloud(ctx); err == nil {
		return tok, exp, nil
	} else {
		recordErr(err)
	}

	return "", time.Time{}, httpError{
		status: http.StatusUnauthorized,
		msg:    adcTokenUnavailableMessage(attemptErrs),
	}
}

func adcTokenUnavailableMessage(errs []error) string {
	base := "Google ADC token unavailable."
	for _, err := range errs {
		msg := strings.TrimSpace(err.Error())
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "reauthentication failed") ||
			strings.Contains(lower, "cannot prompt during non-interactive execution") ||
			strings.Contains(lower, "invalid_grant") ||
			strings.Contains(lower, "expired") ||
			strings.Contains(lower, "revoked") {
			return base + " The saved Google login has expired or requires reauthentication. Run `gcloud auth application-default login`, then reconnect BigQuery."
		}
	}
	for _, err := range errs {
		msg := strings.TrimSpace(err.Error())
		if msg != "" {
			return base + " " + msg
		}
	}
	return base + " Provide ADC credentials, for example by running `gcloud auth application-default login` or setting GOOGLE_APPLICATION_CREDENTIALS."
}

func (h *Handler) cachedTokenForKey(cacheKey string) string {
	now := time.Now()
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	if h.cachedToken == "" || strings.TrimSpace(h.cachedKey) == "" || h.cachedKey != cacheKey {
		return ""
	}
	if !now.Before(h.cachedUntil) {
		return ""
	}
	return h.cachedToken
}

func (h *Handler) beginTokenRefresh() (<-chan struct{}, bool) {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	if h.refreshing {
		return h.refreshWait, false
	}
	h.refreshing = true
	h.refreshWait = make(chan struct{})
	return h.refreshWait, true
}

func (h *Handler) endTokenRefresh() {
	h.tokenMu.Lock()
	defer h.tokenMu.Unlock()
	if h.refreshing {
		h.refreshing = false
		if h.refreshWait != nil {
			close(h.refreshWait)
			h.refreshWait = nil
		}
	}
}

func (h *Handler) tokenCacheKey() string {
	// Cache invalidation is keyed on the currently configured ADC source,
	// so "Set custom path" immediately takes effect without waiting for token expiry.
	if p := strings.TrimSpace(h.credentialsPathOverrideFromSettings()); p != "" {
		return "custom:" + resolveFilePath(p)
	}
	info := credentialsFilePathInfo()
	return info.Source + ":" + resolveFilePath(info.Path)
}

func (h *Handler) cacheToken(key string, tok string, exp time.Time) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	until := exp
	if until.IsZero() {
		until = time.Now().Add(50 * time.Minute)
	}
	// Refresh a little before expiration.
	until = until.Add(-1 * time.Minute)
	if until.Before(time.Now().Add(10 * time.Second)) {
		until = time.Now().Add(10 * time.Second)
	}

	h.tokenMu.Lock()
	h.cachedToken = tok
	h.cachedUntil = until
	h.cachedKey = key
	h.tokenMu.Unlock()
}

func (h *Handler) invalidateCachedToken() {
	h.tokenMu.Lock()
	h.cachedToken = ""
	h.cachedUntil = time.Time{}
	h.cachedKey = ""
	h.tokenMu.Unlock()
}

type httpError struct {
	status     int
	msg        string
	retryAfter time.Duration
}

func (e httpError) Error() string { return e.msg }

func (e httpError) RetryAfter() time.Duration {
	if e.retryAfter < 0 {
		return 0
	}
	return e.retryAfter
}

func httpStatusForErr(err error) int {
	var he httpError
	if errors.As(err, &he) && he.status != 0 {
		return he.status
	}
	return http.StatusBadRequest
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeBQError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
		},
	})
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	if limit <= 0 {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
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
