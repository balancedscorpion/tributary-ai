package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/filepipeline"
	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/runs"
	searchsvc "ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/githubcli"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sources"
)

type Deps struct {
	Store                store.Store
	Manager              *optionc.Manager
	Projects             *projects.Store
	Search               *searchsvc.Service
	SystemThreads        *searchsvc.SystemThreadRegistry
	Settings             *config.Store
	Secrets              *secrets.Store
	Permissions          *permissions.Service
	Sources              *sources.Store
	GitHub               *githubcli.Manager
	CfgDir               string
	RunPreflightResolver runs.Resolver

	// MaxBodyBytes caps JSON request bodies. Default: 1MiB.
	MaxBodyBytes int64
}

type Handler struct {
	store         store.Store
	manager       *optionc.Manager
	projects      *projects.Store
	search        *searchsvc.Service
	systemThreads *searchsvc.SystemThreadRegistry
	settings      *config.Store
	secrets       *secrets.Store
	permissions   *permissions.Service
	sources       *sources.Store
	github        *githubcli.Manager
	githubAuth    *githubDeviceSessionStore
	cfgDir        string
	runPreflight  *runs.Service

	maxBodyBytes int64
}

const (
	maxProjectFileBytes       int64 = 10 << 30
	maxArtifactRowsLimit            = 5000
	defaultArtifactRowsLimit        = 200
	maxArtifactRowsReadBytes        = 256 << 20
	maxArtifactRowsReplyBytes       = 2 << 20
	maxThreadTransferBytes    int64 = 256 << 20

	// Allow larger run payloads for inline image attachments.
	minCreateRunBodyBytes      int64 = 20 << 20
	maxInlineImageCount              = 4
	maxInlineImageDataURIBytes       = 14 << 20
	maxInlineImageTotalBytes         = 15 << 20
)

func New(deps Deps) http.Handler {
	if deps.Store == nil {
		panic("chat/httpapi.New: Store is required")
	}
	if deps.Manager == nil {
		panic("chat/httpapi.New: Manager is required")
	}
	if deps.Projects == nil {
		panic("chat/httpapi.New: Projects is required")
	}
	if strings.TrimSpace(deps.CfgDir) == "" {
		panic("chat/httpapi.New: CfgDir is required")
	}
	mb := deps.MaxBodyBytes
	if mb <= 0 {
		mb = 1 << 20
	}
	h := &Handler{
		store:         deps.Store,
		manager:       deps.Manager,
		projects:      deps.Projects,
		search:        deps.Search,
		systemThreads: deps.SystemThreads,
		settings:      deps.Settings,
		secrets:       deps.Secrets,
		permissions:   deps.Permissions,
		sources:       deps.Sources,
		github:        deps.GitHub,
		githubAuth:    newGitHubDeviceSessionStore(),
		cfgDir:        deps.CfgDir,
		runPreflight:  runs.NewService(deps.Store, deps.RunPreflightResolver),
		maxBodyBytes:  mb,
	}

	mux := http.NewServeMux()

	// Threads
	mux.HandleFunc("GET /api/threads", h.handleListThreads)
	mux.HandleFunc("POST /api/threads", h.handleCreateThread)
	mux.HandleFunc("GET /api/threads/{threadId}", h.handleGetThread)
	mux.HandleFunc("DELETE /api/threads/{threadId}", h.handleDeleteThread)
	mux.HandleFunc("POST /api/threads/{threadId}/archive", h.handleArchiveThread)
	mux.HandleFunc("PUT /api/threads/{threadId}/target", h.handlePutThreadTarget)
	mux.HandleFunc("DELETE /api/threads/{threadId}/attachment-policies/{policyId}", h.handleDeleteThreadAttachmentPolicy)
	mux.HandleFunc("POST /api/threads/{threadId}/attachments", h.handlePostThreadAttachment)
	mux.HandleFunc("GET /api/threads/{threadId}/files", h.handleListThreadFiles)
	mux.HandleFunc("GET /api/threads/{threadId}/export", h.handleExportThread)
	mux.HandleFunc("POST /api/threads/import", h.handleImportThread)

	// Runs
	mux.HandleFunc("POST /api/threads/{threadId}/runs/preflight", h.handleRunPreflight)
	mux.HandleFunc("POST /api/threads/{threadId}/runs", h.handleCreateRun)
	mux.HandleFunc("GET /api/runs/{runId}", h.handleGetRun)
	mux.HandleFunc("GET /api/runs/{runId}/plan", h.handleGetRunPlan)
	mux.HandleFunc("POST /api/runs/{runId}/cancel", h.handleCancelRun)
	mux.HandleFunc("GET /api/runs/{runId}/events", h.handleRunEvents)
	mux.HandleFunc("POST /api/tools/test", h.handleTestToolByBody)
	mux.HandleFunc("POST /api/tools/{toolName}/test", h.handleTestTool)
	mux.HandleFunc("GET /api/queries/{jobId}/status", h.handleGetQueryStatus)
	mux.HandleFunc("POST /api/queries/{jobId}/cancel", h.handleCancelQuery)

	// Artifacts
	mux.HandleFunc("GET /api/files", h.handleListFiles)
	mux.HandleFunc("GET /api/sources", h.handleListSources)
	mux.HandleFunc("GET /api/sources/{sourceId}", h.handleGetSource)
	mux.HandleFunc("DELETE /api/sources/{sourceId}", h.handleDeleteSource)
	mux.HandleFunc("GET /api/sources/{sourceId}/preview", h.handleSourcePreview)
	mux.HandleFunc("GET /api/local-files/roots", h.handleLocalComputerRoots)
	mux.HandleFunc("GET /api/local-files/search-jobs", h.handleListLocalSearchJobs)
	mux.HandleFunc("GET /api/local-files/search-jobs/{jobId}", h.handleGetLocalSearchJob)
	mux.HandleFunc("GET /api/local-files/grants/{grantId}/file", h.handleLocalGrantFileRaw)
	mux.HandleFunc("GET /api/artifacts/{artifactId}", h.handleGetArtifact)
	mux.HandleFunc("DELETE /api/artifacts/{artifactId}", h.handleDeleteArtifact)
	mux.HandleFunc("GET /api/artifacts/{artifactId}/raw", h.handleGetArtifactRaw)
	mux.HandleFunc("GET /api/artifacts/{artifactId}/extracted", h.handleGetArtifactExtracted)
	mux.HandleFunc("GET /api/artifacts/{artifactId}/spreadsheet/cells", h.handleGetSpreadsheetCells)
	mux.HandleFunc("GET /api/artifacts/{artifactId}/rows", h.handleGetArtifactRows)
	mux.HandleFunc("POST /api/artifacts/{artifactId}/filter-chart", h.handleFilterChart)

	// Search
	mux.HandleFunc("GET /api/search", h.handleSearch)
	mux.HandleFunc("GET /api/search/suggest", h.handleSearchSuggest)
	mux.HandleFunc("POST /api/search/answer", h.handleSearchAnswer)

	// Projects
	mux.HandleFunc("POST /api/projects", h.handleCreateProject)
	mux.HandleFunc("POST /api/projects/git", h.handleCreateGitProject)
	mux.HandleFunc("GET /api/projects/git/profile", h.handleGitProfile)
	mux.HandleFunc("GET /api/projects/git/auth/status", h.handleGitHubAuthStatus)
	mux.HandleFunc("PUT /api/projects/git/auth/client", h.handleSetGitHubOAuthClientID)
	mux.HandleFunc("POST /api/projects/git/auth/start", h.handleStartGitHubAuth)
	mux.HandleFunc("POST /api/projects/git/auth/poll", h.handlePollGitHubAuth)
	mux.HandleFunc("POST /api/projects/git/auth/disconnect", h.handleDisconnectGitHubAuth)
	mux.HandleFunc("GET /api/projects/git/auth/prerequisites", h.handleGitHubAuthPrerequisites)
	mux.HandleFunc("PUT /api/integrations/github/auth/client", h.handleSetGitHubOAuthClientID)
	mux.HandleFunc("POST /api/integrations/github/auth/start", h.handleStartGitHubAuth)
	mux.HandleFunc("POST /api/integrations/github/auth/poll", h.handlePollGitHubAuth)
	mux.HandleFunc("POST /api/integrations/github/auth/disconnect", h.handleDisconnectGitHubAuth)
	mux.HandleFunc("GET /api/integrations/github/auth/prerequisites", h.handleGitHubAuthPrerequisites)
	mux.HandleFunc("POST /api/projects/local", h.handleAddLocalProjectPath)
	mux.HandleFunc("GET /api/projects", h.handleListProjects)
	mux.HandleFunc("GET /api/projects/{projectId}", h.handleGetProject)
	mux.HandleFunc("GET /api/projects/{projectId}/sources", h.handleListProjectSources)
	mux.HandleFunc("PATCH /api/projects/{projectId}/sources/{sourceName}", h.handleUpdateProjectSource)
	mux.HandleFunc("DELETE /api/projects/{projectId}/sources/{sourceName}", h.handleDeleteProjectSource)
	mux.HandleFunc("PUT /api/projects/{projectId}/files", h.handlePutProjectFile)
	mux.HandleFunc("POST /api/projects/{projectId}/finalize", h.handleFinalizeProject)
	mux.HandleFunc("POST /api/projects/{projectId}/refresh", h.handleRefreshGitProject)
	mux.HandleFunc("GET /api/github/repositories", h.handleGitHubRepositories)
	mux.HandleFunc("POST /api/github/repositories/clone", h.handleCloneGitHubRepository)
	mux.HandleFunc("DELETE /api/github/repositories/{sourceId}", h.handleDeleteGitHubRepository)
	mux.HandleFunc("POST /api/github/repositories/{sourceId}/refresh", h.handleRefreshGitHubRepository)
	mux.HandleFunc("GET /api/github/repositories/{sourceId}/tree", h.handleGitHubRepositoryTree)
	mux.HandleFunc("GET /api/github/repositories/{sourceId}/raw", h.handleGitHubRepositoryRaw)

	return mux
}

func (h *Handler) handleListThreads(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := parseInt(r.URL.Query().Get("limit"), 50, 1, 200)
	threads, err := h.store.ListThreads(r.Context(), status, limit)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	includeSystem := parseBool(r.URL.Query().Get("include_system"))
	if !includeSystem && h.systemThreads != nil {
		searchThreadID := strings.TrimSpace(h.systemThreads.SearchThreadID())
		if searchThreadID != "" {
			filtered := make([]store.Thread, 0, len(threads))
			for _, t := range threads {
				if t.ID == searchThreadID {
					continue
				}
				filtered = append(filtered, t)
			}
			threads = filtered
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"threads": threadsToDTO(threads)})
}

func (h *Handler) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title     string  `json:"title,omitempty"`
		ProjectID *string `json:"project_id,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	var pid *string
	if body.ProjectID != nil && strings.TrimSpace(*body.ProjectID) != "" {
		p := strings.TrimSpace(*body.ProjectID)
		pid = &p
	}
	t, err := h.store.CreateThread(r.Context(), pid)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if strings.TrimSpace(body.Title) != "" {
		title := strings.TrimSpace(body.Title)
		_ = h.store.UpdateThreadTitleSummary(r.Context(), t.ID, &title, nil)
		// Refresh.
		t, _ = h.store.GetThread(r.Context(), t.ID)
	}
	if pid != nil {
		h.refreshGitRepositoriesBestEffortAsync(*pid)
	}

	writeJSON(w, http.StatusOK, map[string]any{"thread": threadToDTO(t)})
}

func (h *Handler) handleGetThread(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	t, err := h.store.GetThread(r.Context(), threadID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	msgs, err := h.store.ListMessages(r.Context(), store.ListMessagesParams{ThreadID: threadID, Limit: 1000})
	if err != nil {
		h.writeErr(w, err)
		return
	}
	referencedThreadsByID, err := h.loadReferencedThreadsForMessages(r.Context(), threadID, msgs)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	fileArtifacts, err := h.loadReferencedFileArtifactsForMessages(r.Context(), threadID, msgs)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	uiMsgs := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		if m.UIHidden {
			continue
		}
		uiMsgs = append(uiMsgs, messageToUI(m, referencedThreadsByID))
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": threadToDTO(t), "messages": uiMsgs, "artifacts": fileArtifacts})
}

func (h *Handler) loadReferencedFileArtifactsForMessages(ctx context.Context, threadID string, msgs []store.Message) ([]map[string]any, error) {
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	for _, msg := range msgs {
		if msg.UIHidden {
			continue
		}
		for _, id := range msg.ReferencedArtifactIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		artifact, err := h.store.GetArtifact(ctx, id)
		if err != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(artifact.ThreadID), strings.TrimSpace(threadID)) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(artifact.Type), "file") {
			continue
		}
		if !fileartifact.IsThreadFileArtifact(threadID, artifact) {
			continue
		}
		out = append(out, fileAttachmentArtifactDTO(artifact))
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (h *Handler) handleArchiveThread(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	if err := h.store.ArchiveThread(r.Context(), threadID); err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	if err := h.store.DeleteThread(r.Context(), threadID); err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handlePutThreadTarget(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	var target store.RunTarget
	if err := decodeJSONLimit(w, r, &target, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	target = runs.NormalizeTarget(target)
	if _, _, err := h.providerConnectionSnapshot(target); err != nil {
		h.writeErr(w, err)
		return
	}
	thread, err := h.store.SetThreadTarget(r.Context(), threadID, target)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": threadToDTO(thread), "target": target})
}

func (h *Handler) handleDeleteThreadAttachmentPolicy(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	policyID := strings.TrimSpace(r.PathValue("policyId"))
	if threadID == "" || policyID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId or policyId"})
		return
	}
	thread, err := h.store.DeleteThreadAttachmentPolicy(r.Context(), threadID, policyID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"thread": threadToDTO(thread)})
}

func (h *Handler) handleRunPreflight(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	var body struct {
		Target       store.RunTarget   `json:"target"`
		Requirements runs.Requirements `json:"requirements"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	body.Target = runs.NormalizeTarget(body.Target)
	_, _ = h.store.DeleteExpiredRunPlans(r.Context(), time.Now().UTC())
	plan, err := h.runPreflight.Preflight(r.Context(), runs.PreflightRequest{
		ThreadID: threadID, Target: body.Target, Requirements: body.Requirements,
	})
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, preflightPlanDTO(plan))
}

func (h *Handler) handleGetRunPlan(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing runId"})
		return
	}
	plan, err := h.store.GetRunPlanForRun(r.Context(), runID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"plan": persistedPlanDTO(plan)})
}

func (h *Handler) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}

	var body map[string]any
	maxBodyBytes := h.maxBodyBytes
	if maxBodyBytes < minCreateRunBodyBytes {
		maxBodyBytes = minCreateRunBodyBytes
	}
	if err := decodeJSONLimit(w, r, &body, maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	// Accept either {content:"..."} or
	// {message:{content:"...", hiddenContext:"...", referencedMessageIds:[...], referencedThreadIds:[...], referencedArtifactIds:[...]}}
	content := ""
	hidden := ""
	var projectOverride *string
	var maxItersOverride *int
	var referencedMessages []string
	var referencedThreads []string
	var referencedArtifacts []string
	var imageAttachments []types.ImageAttachment
	var fileAttachments []types.FileAttachment
	var purpose string
	var searchDepth string
	var dataSources *config.DatasourceConnections
	var dataSourceOverrides map[string]any
	var activeConnectionIDs []string
	activeConnectionIDsProvided := false
	var clientNowISO string
	var clientTimezone string
	var clientUTCOffsetMinutes *int
	planID, target, choiceSelections, requirementFeatures, planFieldsErr := parseRunPlanFields(body)
	if planFieldsErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": planFieldsErr.Error()})
		return
	}
	assignRuntimeContext := func(raw map[string]any) {
		nowISO, timezone, utcOffset := parseRuntimeContext(raw)
		if clientNowISO == "" && nowISO != "" {
			clientNowISO = nowISO
		}
		if clientTimezone == "" && timezone != "" {
			clientTimezone = timezone
		}
		if clientUTCOffsetMinutes == nil && utcOffset != nil {
			clientUTCOffsetMinutes = utcOffset
		}
	}

	if v, ok := body["content"].(string); ok {
		content = strings.TrimSpace(v)
	}
	if v, ok := body["hiddenContext"].(string); ok {
		hidden = strings.TrimSpace(v)
	}
	if v, ok := body["project_id"].(string); ok && strings.TrimSpace(v) != "" {
		p := strings.TrimSpace(v)
		projectOverride = &p
	}
	if v, ok := body["purpose"].(string); ok {
		purpose = strings.TrimSpace(v)
	}
	if v, ok := body["searchDepth"].(string); ok {
		searchDepth = strings.TrimSpace(v)
	}
	if v, ok := body["search_depth"].(string); ok && searchDepth == "" {
		searchDepth = strings.TrimSpace(v)
	}
	if v, ok := body["referencedArtifactIds"].([]any); ok {
		referencedArtifacts = append(referencedArtifacts, extractStringSlice(v)...)
	}
	if v, ok := body["referenced_artifact_ids"].([]any); ok {
		referencedArtifacts = append(referencedArtifacts, extractStringSlice(v)...)
	}
	if v, ok := body["referencedMessageIds"].([]any); ok {
		referencedMessages = append(referencedMessages, extractStringSlice(v)...)
	}
	if v, ok := body["referenced_message_ids"].([]any); ok {
		referencedMessages = append(referencedMessages, extractStringSlice(v)...)
	}
	if v, ok := body["referencedThreadIds"].([]any); ok {
		referencedThreads = append(referencedThreads, extractStringSlice(v)...)
	}
	if v, ok := body["referenced_thread_ids"].([]any); ok {
		referencedThreads = append(referencedThreads, extractStringSlice(v)...)
	}
	if v, ok := body["imageAttachments"]; ok {
		images, err := extractImageAttachments(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		imageAttachments = append(imageAttachments, images...)
	}
	if v, ok := body["image_attachments"]; ok {
		images, err := extractImageAttachments(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		imageAttachments = append(imageAttachments, images...)
	}
	if v, ok := body["fileAttachments"]; ok {
		files, err := extractFileAttachments(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		fileAttachments = append(fileAttachments, files...)
	}
	if v, ok := body["file_attachments"]; ok {
		files, err := extractFileAttachments(v)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		fileAttachments = append(fileAttachments, files...)
	}
	if v, ok := body["max_iterations"].(float64); ok && v > 0 {
		i := int(v)
		maxItersOverride = &i
	}
	if v, ok := body["dataSources"].(map[string]any); ok {
		dataSources = parseRunDataSources(v)
		dataSourceOverrides = parseRunDataSourceOverrides(v)
	}
	if dataSources == nil && dataSourceOverrides == nil {
		if v, ok := body["data_sources"].(map[string]any); ok {
			dataSources = parseRunDataSources(v)
			dataSourceOverrides = parseRunDataSourceOverrides(v)
		}
	}
	if v, ok := body["activeConnectionIds"]; ok {
		ids, parsed := parseRunActiveConnectionIDs(v)
		if !parsed {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "activeConnectionIds must be an array of strings"})
			return
		}
		activeConnectionIDs = ids
		activeConnectionIDsProvided = true
	}
	if !activeConnectionIDsProvided {
		if v, ok := body["active_connection_ids"]; ok {
			ids, parsed := parseRunActiveConnectionIDs(v)
			if !parsed {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "active_connection_ids must be an array of strings"})
				return
			}
			activeConnectionIDs = ids
			activeConnectionIDsProvided = true
		}
	}
	if v, ok := body["runtimeContext"].(map[string]any); ok {
		assignRuntimeContext(v)
	}
	if v, ok := body["runtime_context"].(map[string]any); ok {
		assignRuntimeContext(v)
	}
	if clientNowISO == "" {
		clientNowISO = firstStringMapValue(body, "clientNowISO", "client_now_iso", "clientTimeISO", "client_time_iso")
	}
	if clientTimezone == "" {
		clientTimezone = firstStringMapValue(body, "clientTimezone", "client_timezone", "timeZone", "time_zone")
	}
	if clientUTCOffsetMinutes == nil {
		if offset, ok := extractOptionalSignedIntMapValue(body, "clientUtcOffsetMinutes", "client_utc_offset_minutes"); ok {
			clientUTCOffsetMinutes = &offset
		}
	}

	if msg, ok := body["message"].(map[string]any); ok {
		if v, ok := msg["content"].(string); ok && strings.TrimSpace(content) == "" {
			content = v
		}
		if v, ok := msg["hiddenContext"].(string); ok && strings.TrimSpace(hidden) == "" {
			hidden = strings.TrimSpace(v)
		}
		if v, ok := msg["hidden_context"].(string); ok && strings.TrimSpace(hidden) == "" {
			hidden = strings.TrimSpace(v)
		}
		if v, ok := msg["purpose"].(string); ok && strings.TrimSpace(purpose) == "" {
			purpose = strings.TrimSpace(v)
		}
		if v, ok := msg["searchDepth"].(string); ok && strings.TrimSpace(searchDepth) == "" {
			searchDepth = strings.TrimSpace(v)
		}
		if v, ok := msg["search_depth"].(string); ok && strings.TrimSpace(searchDepth) == "" {
			searchDepth = strings.TrimSpace(v)
		}
		if v, ok := msg["referencedArtifactIds"].([]any); ok {
			referencedArtifacts = append(referencedArtifacts, extractStringSlice(v)...)
		}
		if v, ok := msg["referenced_artifact_ids"].([]any); ok {
			referencedArtifacts = append(referencedArtifacts, extractStringSlice(v)...)
		}
		if v, ok := msg["referencedMessageIds"].([]any); ok {
			referencedMessages = append(referencedMessages, extractStringSlice(v)...)
		}
		if v, ok := msg["referenced_message_ids"].([]any); ok {
			referencedMessages = append(referencedMessages, extractStringSlice(v)...)
		}
		if v, ok := msg["referencedThreadIds"].([]any); ok {
			referencedThreads = append(referencedThreads, extractStringSlice(v)...)
		}
		if v, ok := msg["referenced_thread_ids"].([]any); ok {
			referencedThreads = append(referencedThreads, extractStringSlice(v)...)
		}
		if v, ok := msg["imageAttachments"]; ok {
			images, err := extractImageAttachments(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			imageAttachments = append(imageAttachments, images...)
		}
		if v, ok := msg["image_attachments"]; ok {
			images, err := extractImageAttachments(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			imageAttachments = append(imageAttachments, images...)
		}
		if v, ok := msg["fileAttachments"]; ok {
			files, err := extractFileAttachments(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			fileAttachments = append(fileAttachments, files...)
		}
		if v, ok := msg["file_attachments"]; ok {
			files, err := extractFileAttachments(v)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			fileAttachments = append(fileAttachments, files...)
		}
		if v, ok := msg["dataSources"].(map[string]any); ok {
			dataSources = parseRunDataSources(v)
			dataSourceOverrides = parseRunDataSourceOverrides(v)
		}
		if dataSources == nil && dataSourceOverrides == nil {
			if v, ok := msg["data_sources"].(map[string]any); ok {
				dataSources = parseRunDataSources(v)
				dataSourceOverrides = parseRunDataSourceOverrides(v)
			}
		}
		if !activeConnectionIDsProvided {
			if v, ok := msg["activeConnectionIds"]; ok {
				ids, parsed := parseRunActiveConnectionIDs(v)
				if !parsed {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message.activeConnectionIds must be an array of strings"})
					return
				}
				activeConnectionIDs = ids
				activeConnectionIDsProvided = true
			}
		}
		if !activeConnectionIDsProvided {
			if v, ok := msg["active_connection_ids"]; ok {
				ids, parsed := parseRunActiveConnectionIDs(v)
				if !parsed {
					writeJSON(w, http.StatusBadRequest, map[string]any{"error": "message.active_connection_ids must be an array of strings"})
					return
				}
				activeConnectionIDs = ids
				activeConnectionIDsProvided = true
			}
		}
		if v, ok := msg["runtimeContext"].(map[string]any); ok {
			assignRuntimeContext(v)
		}
		if v, ok := msg["runtime_context"].(map[string]any); ok {
			assignRuntimeContext(v)
		}
	}

	if p := strings.TrimSpace(purpose); p != "" {
		purpose = strings.ToLower(p)
		if purpose != "analysis" && purpose != "search" {
			purpose = ""
		}
	}
	if searchDepth == "" {
		searchDepth = "shallow"
	}

	content = strings.TrimSpace(content)
	if content == "" && len(imageAttachments) == 0 && len(fileAttachments) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing content"})
		return
	}
	referencedArtifacts = dedupeStringSlice(referencedArtifacts)
	referencedMessages = dedupeStringSlice(referencedMessages)
	referencedThreads = dedupeStringSlice(referencedThreads)
	if len(referencedThreads) > 5 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "at most 5 referenced threads are supported"})
		return
	}
	imageAttachments = dedupeImageAttachments(imageAttachments)
	fileAttachments = types.CloneFileAttachments(fileAttachments)
	if planID == "" && target != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "target requires planId; preflight the run first"})
		return
	}
	var requirementsHash, configFingerprint string
	if planID != "" {
		if target == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "planId requires target"})
			return
		}
		_, resolvedFingerprint, snapshotErr := h.providerConnectionSnapshot(*target)
		if snapshotErr != nil {
			if errors.Is(snapshotErr, store.ErrNotFound) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "plan_stale", "message": "The selected provider connection no longer exists. Preflight again."})
				return
			}
			h.writeErr(w, snapshotErr)
			return
		}
		configFingerprint = resolvedFingerprint
		requirementsHash = runs.RequirementsHash(runs.RequirementsFromAttachments(imageAttachments, fileAttachments, requirementFeatures))
	}

	if len(referencedThreads) > 0 {
		threadContext, sanitizedThreadIDs, err := h.buildReferencedThreadsHiddenContext(
			r.Context(),
			threadID,
			content,
			referencedThreads,
		)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		referencedThreads = sanitizedThreadIDs
		hidden = appendHiddenContextSections(hidden, threadContext)
	}

	res, err := h.manager.StartRun(r.Context(), optionc.StartRunRequest{
		ThreadID:                    threadID,
		Content:                     content,
		HiddenContext:               hidden,
		ProjectIDOverride:           projectOverride,
		MaxIterationsOverride:       maxItersOverride,
		ReferencedMessageIDs:        referencedMessages,
		ReferencedThreadIDs:         referencedThreads,
		ReferencedArtifactIDs:       referencedArtifacts,
		ImageAttachments:            imageAttachments,
		FileAttachments:             fileAttachments,
		Purpose:                     purpose,
		SearchDepth:                 searchDepth,
		DataSources:                 dataSources,
		DataSourceOverrides:         dataSourceOverrides,
		ActiveConnectionIDs:         activeConnectionIDs,
		ActiveConnectionIDsProvided: activeConnectionIDsProvided,
		ClientNowISO:                clientNowISO,
		ClientTimezone:              clientTimezone,
		ClientUTCOffsetMinutes:      clientUTCOffsetMinutes,
		PlanID:                      planID,
		Target:                      target,
		RequirementsHash:            requirementsHash,
		ConfigFingerprint:           configFingerprint,
		ChoiceSelections:            choiceSelections,
	})
	if err != nil {
		var conflictErr *optionc.ActiveRunConflictError
		if errors.As(err, &conflictErr) {
			payload := map[string]any{"error": conflictErr.Error()}
			if runID := conflictErr.ActiveRunID(); runID != "" {
				payload["active_run_id"] = runID
			}
			writeJSON(w, http.StatusConflict, payload)
			return
		}
		if errors.Is(err, store.ErrPlanStale) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "plan_stale", "message": "The run plan expired or no longer matches this request. Preflight again."})
			return
		}
		h.writeErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"run":   runToDTO(res.Run),
		"runId": res.Run.ID,
	})
}

func (h *Handler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing runId"})
		return
	}
	run, err := h.store.GetRun(r.Context(), runID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": runToDTO(run)})
}

func (h *Handler) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing runId"})
		return
	}
	if err := h.manager.Cancel(runID); err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleTestTool(w http.ResponseWriter, r *http.Request) {
	h.handleTestToolNamed(w, r, r.PathValue("toolName"))
}

func (h *Handler) handleTestToolByBody(w http.ResponseWriter, r *http.Request) {
	h.handleTestToolNamed(w, r, "")
}

func (h *Handler) handleTestToolNamed(w http.ResponseWriter, r *http.Request, pathToolName string) {
	var body struct {
		ToolName    string          `json:"tool_name,omitempty"`
		ThreadID    string          `json:"thread_id,omitempty"`
		ProjectID   string          `json:"project_id,omitempty"`
		SearchDepth string          `json:"search_depth,omitempty"`
		Args        json.RawMessage `json:"args,omitempty"`
	}
	if r.Body != nil {
		if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
	}

	toolName := strings.TrimSpace(pathToolName)
	if toolName == "" {
		toolName = strings.TrimSpace(body.ToolName)
	}
	if toolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing toolName"})
		return
	}

	result, err := h.manager.TestTool(r.Context(), optionc.ToolTestRequest{
		ToolName:    toolName,
		ThreadID:    body.ThreadID,
		ProjectID:   body.ProjectID,
		SearchDepth: body.SearchDepth,
		Args:        body.Args,
	})
	if err != nil {
		var inputErr *tools.MissingToolTestInputError
		if errors.As(err, &inputErr) {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":                    inputErr.Error(),
				"tool_name":                inputErr.ToolName,
				"missing_arguments":        inputErr.MissingArguments,
				"missing_context":          inputErr.MissingContext,
				"required_arguments":       inputErr.RequiredArguments,
				"required_argument_groups": inputErr.RequiredArgumentGroups,
				"required_context":         inputErr.RequiredContext,
				"input_hint":               inputErr.InputHint,
			})
			return
		}
		var permissionErr *optionc.ToolTestPermissionError
		if errors.As(err, &permissionErr) {
			status := http.StatusForbidden
			if permissionErr.Decision == "ask" {
				status = http.StatusConflict
			}
			writeJSON(w, status, map[string]any{
				"error":     permissionErr.Error(),
				"tool_name": permissionErr.ToolName,
				"decision":  permissionErr.Decision,
				"category":  permissionErr.Category,
				"subject":   permissionErr.Subject,
				"reason":    permissionErr.Reason,
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   result.Output.OK,
		"test": result,
	})
}

func (h *Handler) handleGetQueryStatus(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing jobId"})
		return
	}
	ownerToken := strings.TrimSpace(r.Header.Get("x-query-owner-token"))
	if ownerToken == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing x-query-owner-token"})
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	if threadID == "" {
		threadID = strings.TrimSpace(r.URL.Query().Get("threadId"))
	}
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing thread_id"})
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" {
		runID = strings.TrimSpace(r.URL.Query().Get("runId"))
	}
	queryObligationID := strings.TrimSpace(r.URL.Query().Get("query_obligation_id"))
	if queryObligationID == "" {
		queryObligationID = strings.TrimSpace(r.URL.Query().Get("queryObligationId"))
	}

	status, err := h.manager.GetQueryJobStatusForRun(
		r.Context(), jobID, threadID, ownerToken, runID, queryObligationID,
	)
	if err != nil {
		h.writeQueryJobErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) handleCancelQuery(w http.ResponseWriter, r *http.Request) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	if jobID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing jobId"})
		return
	}
	ownerToken := strings.TrimSpace(r.Header.Get("x-query-owner-token"))
	if ownerToken == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "missing x-query-owner-token"})
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	if threadID == "" {
		threadID = strings.TrimSpace(r.URL.Query().Get("threadId"))
	}
	if threadID == "" {
		var body struct {
			ThreadID      string `json:"thread_id"`
			ThreadIDCamel string `json:"threadId"`
		}
		if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		threadID = strings.TrimSpace(body.ThreadID)
		if threadID == "" {
			threadID = strings.TrimSpace(body.ThreadIDCamel)
		}
	}
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing thread_id"})
		return
	}

	status, err := h.manager.CancelQueryJob(r.Context(), jobID, threadID, ownerToken)
	if err != nil {
		h.writeQueryJobErr(w, err)
		return
	}
	resp := map[string]any{
		"ok": true,
	}
	for key, value := range status {
		resp[key] = value
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing runId"})
		return
	}

	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	f.Flush()

	afterSeq := parseRunEventAfterSeq(r)
	ch, cancel, err := h.manager.Subscribe(runID)
	if err != nil {
		if h.writeStoredRunTerminalSSE(r.Context(), w, f, runID, afterSeq) {
			return
		}
		h.writeSSEError(w, f, err)
		return
	}
	defer cancel()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	durableCursor := afterSeq

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			f.Flush()
		case ev, ok := <-ch:
			if !ok {
				if run, getErr := h.store.GetRun(r.Context(), runID); getErr == nil {
					status := strings.ToLower(strings.TrimSpace(run.Status))
					if status != store.RunStatusCompleted && status != store.RunStatusFailed && status != store.RunStatusCancelled {
						h.writeStoredRunTerminalSSE(r.Context(), w, f, runID, durableCursor)
					}
				}
				return
			}
			if err := ev.WriteSSETo(w); err != nil {
				return
			}
			if ev.Seq > durableCursor {
				durableCursor = ev.Seq
			}
			f.Flush()
		}
	}
}

func parseRunEventAfterSeq(r *http.Request) int64 {
	if r == nil {
		return 0
	}
	candidates := []string{
		strings.TrimSpace(r.URL.Query().Get("after_seq")),
		strings.TrimSpace(r.URL.Query().Get("afterSeq")),
		strings.TrimSpace(r.Header.Get("Last-Event-ID")),
	}
	for _, raw := range candidates {
		if raw == "" {
			continue
		}
		if seq, err := strconv.ParseInt(raw, 10, 64); err == nil && seq > 0 {
			return seq
		}
	}
	return 0
}

func (h *Handler) writeStoredRunTerminalSSE(ctx context.Context, w io.Writer, f http.Flusher, runID string, afterSeq int64) bool {
	runID = strings.TrimSpace(runID)
	run, err := h.store.GetRun(ctx, runID)
	if err != nil {
		return false
	}
	cursor := afterSeq
	sawTerminal := false
	sawFinal := false
	sawWaitingUser := false
	writeAvailable := func() {
		events, listErr := h.store.ListRunEvents(ctx, store.ListRunEventsParams{RunID: runID, AfterSeq: cursor, Limit: 4096})
		if listErr != nil {
			return
		}
		for _, stored := range events {
			ev := optionc.SSEEvent{Event: stored.Type, Data: stored.Data, Seq: stored.Seq}
			switch strings.TrimSpace(ev.Event) {
			case optionc.EventRunTerminal:
				sawTerminal = true
			case "final":
				sawFinal = true
			case "run_waiting_user":
				sawWaitingUser = true
			}
			if stored.Seq > cursor {
				cursor = stored.Seq
			}
			_ = ev.WriteSSETo(w)
		}
	}
	writeActualTerminal := func(run store.Run) bool {
		status := strings.ToLower(strings.TrimSpace(run.Status))
		if status != store.RunStatusCompleted && status != store.RunStatusFailed && status != store.RunStatusCancelled {
			return false
		}
		payload := optionc.RunTerminalPayload{RunID: run.ID, Status: status, Timestamp: time.Now().UnixMilli()}
		if status == "failed" {
			if run.ErrorCode != nil {
				payload.Code = strings.TrimSpace(*run.ErrorCode)
			}
			if run.ErrorMessage != nil {
				payload.Message = strings.TrimSpace(*run.ErrorMessage)
			}
		}
		if !sawTerminal {
			b, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				return false
			}
			_ = (optionc.SSEEvent{Event: optionc.EventRunTerminal, Data: b}).WriteSSETo(w)
		}
		if !sawFinal {
			_ = (optionc.SSEEvent{Event: "final", Data: []byte(`{"answer":""}`)}).WriteSSETo(w)
		}
		f.Flush()
		return true
	}
	writeWaitingUser := func(run store.Run) bool {
		if strings.ToLower(strings.TrimSpace(run.Status)) != store.RunStatusWaitingUser {
			return false
		}
		if !sawWaitingUser {
			payload, marshalErr := json.Marshal(map[string]any{
				"run_id":             run.ID,
				"reason":             "Automatic execution paused for review; durable query work was not abandoned.",
				"continuation_count": run.ContinuationCount,
			})
			if marshalErr != nil {
				return false
			}
			_ = (optionc.SSEEvent{Event: "run_waiting_user", Data: payload}).WriteSSETo(w)
		}
		f.Flush()
		return true
	}

	// A finalized-plan replay may be routed to a process that does not own the
	// live in-memory run. Tail its durable event log and status instead of
	// manufacturing RUN_INTERRUPTED for a healthy leased worker.
	writeAvailable()
	f.Flush()
	if sawWaitingUser || writeWaitingUser(run) || writeActualTerminal(run) || sawFinal {
		return true
	}
	poll := time.NewTicker(250 * time.Millisecond)
	defer poll.Stop()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-ping.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			f.Flush()
		case <-poll.C:
			writeAvailable()
			f.Flush()
			if sawWaitingUser || sawFinal {
				return true
			}
			current, getErr := h.store.GetRun(ctx, runID)
			if getErr != nil {
				if ctx.Err() != nil {
					return true
				}
				continue
			}
			if writeWaitingUser(current) || writeActualTerminal(current) {
				return true
			}
		}
	}
}

func (h *Handler) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing artifactId"})
		return
	}
	a, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if fileartifact.IsFileAttachmentArtifact(a) {
		writeJSON(w, http.StatusOK, map[string]any{"artifact": fileAttachmentArtifactDTO(a)})
		return
	}

	content := ""
	// Generated-image bytes have a dedicated authenticated /raw endpoint. Keep
	// them out of the JSON hydration response so large data URIs do not inflate
	// the Files view or inspector request (and do not hit the 8 MiB text limit).
	if !isGeneratedImageArtifact(a) {
		switch strings.TrimSpace(a.Storage) {
		case "file":
			if a.ContentPath == nil || strings.TrimSpace(*a.ContentPath) == "" {
				break
			}
			rel, err := cleanRelPath(strings.TrimSpace(*a.ContentPath))
			if err != nil {
				h.writeErr(w, err)
				return
			}
			full := filepath.Join(h.cfgDir, filepath.FromSlash(rel))
			b, err := osReadFileLimit(full, 8<<20)
			if err != nil {
				h.writeErr(w, err)
				return
			}
			content = string(b)
		default:
			if a.ContentText != nil {
				content = *a.ContentText
			}
		}
	}

	metadata := artifactHydrationMetadata(a)
	dto := map[string]any{
		"id":              a.ID,
		"type":            a.Type,
		"title":           a.Title,
		"content":         content,
		"content_storage": strings.TrimSpace(a.Storage),
		"byte_size":       a.ByteSize,
		"metadata":        metadata,
		"created_at":      a.CreatedAt,
	}
	if a.Language != nil && strings.TrimSpace(*a.Language) != "" {
		dto["language"] = strings.TrimSpace(*a.Language)
	}
	if a.ToolName != nil && strings.TrimSpace(*a.ToolName) != "" {
		dto["tool_name"] = strings.TrimSpace(*a.ToolName)
	}
	if a.ToolCallID != nil && strings.TrimSpace(*a.ToolCallID) != "" {
		dto["tool_call_id"] = strings.TrimSpace(*a.ToolCallID)
	}
	if a.OK != nil {
		dto["ok"] = *a.OK
	}
	if artifactInlinePreview(metadata) {
		dto["inline_preview"] = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifact": dto})
}

func artifactHydrationMetadata(a store.Artifact) map[string]any {
	metadata := cloneArtifactMetadata(a.Metadata)
	if inlinePreview, ok := artifactInlinePreviewValue(metadata); ok {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["inline_preview"] = inlinePreview
		return metadata
	}
	if strings.EqualFold(firstStringMapValue(metadata, "render_mode", "renderMode"), "sandboxed_html") {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["inline_preview"] = true
	}
	return metadata
}

func artifactInlinePreview(metadata map[string]any) bool {
	inlinePreview, ok := artifactInlinePreviewValue(metadata)
	return ok && inlinePreview
}

func artifactInlinePreviewValue(metadata map[string]any) (bool, bool) {
	if metadata == nil {
		return false, false
	}
	for _, key := range []string{"inline_preview", "inlinePreview"} {
		raw, ok := metadata[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case bool:
			return v, true
		case string:
			return strings.EqualFold(strings.TrimSpace(v), "true"), true
		}
	}
	return false, false
}

func cloneArtifactMetadata(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (h *Handler) handleGetArtifactRows(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing artifactId"})
		return
	}
	offset, ok := parseNonNegativeInt64(r.URL.Query().Get("offset"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "offset must be a non-negative integer"})
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), defaultArtifactRowsLimit, 1, maxArtifactRowsLimit)
	if limit <= 0 {
		limit = defaultArtifactRowsLimit
	}
	columns := parseColumnsCSV(r.URL.Query().Get("columns"))

	a, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	raw, err := h.readArtifactContentWithLimit(a, maxArtifactRowsReadBytes)
	if err != nil {
		h.writeErr(w, err)
		return
	}

	rows, err := parseDatasetRows(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	total := len(rows)
	if offset > int64(total) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "offset out of range"})
		return
	}
	start := int(offset)
	end := start + limit
	if end > total {
		end = total
	}
	selected := rows[start:end]
	if len(columns) > 0 {
		selected = projectRowsColumns(selected, columns)
	}
	requestedRows := end - start
	selected = clampRowsByResponseBytes(selected, maxArtifactRowsReplyBytes)
	hasMore := end < total || len(selected) < requestedRows
	nextOffset := start + len(selected)
	encodedRows, err := json.Marshal(selected)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if len(encodedRows) > maxArtifactRowsReplyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": "requested rows exceed response size limit (2MB); reduce limit or select fewer columns",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"artifact_id":       a.ID,
		"rows":              selected,
		"offset":            start,
		"next_offset":       nextOffset,
		"limit":             limit,
		"rows_returned":     len(selected),
		"row_count_total":   total,
		"has_more":          hasMore,
		"response_size":     len(encodedRows),
		"requested_columns": columns,
	})
}

func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	if h.search == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "search is not configured"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing q"})
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 20, 1, 100)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	intent := strings.TrimSpace(r.URL.Query().Get("intent"))
	depth := strings.TrimSpace(r.URL.Query().Get("depth"))
	threadIDs := append([]string(nil), r.URL.Query()["thread_id"]...)
	intentOut, results, err := h.search.Search(r.Context(), searchsvc.Query{
		Text:      q,
		Limit:     limit,
		Status:    status,
		ProjectID: projectID,
		ThreadIDs: threadIDs,
		Intent:    intent,
		Depth:     depth,
	})
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"intent":  intentOut,
		"results": results,
	})
}

func (h *Handler) handleSearchSuggest(w http.ResponseWriter, r *http.Request) {
	if h.search == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "search is not configured"})
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing q"})
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 8, 1, 20)
	docKind := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("doc_kind")))
	docs := h.search.Suggest(q, limit)
	out := make([]map[string]any, 0, len(docs))
	for _, d := range docs {
		if docKind != "" && strings.TrimSpace(strings.ToLower(d.DocKind)) != docKind {
			continue
		}
		out = append(out, map[string]any{
			"doc_id":     d.ID,
			"doc_kind":   d.DocKind,
			"title":      d.Title,
			"thread_id":  d.ThreadID,
			"updated_at": d.UpdatedAt.UnixMilli(),
		})
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

func (h *Handler) handleSearchAnswer(w http.ResponseWriter, r *http.Request) {
	if h.search == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "search is not configured"})
		return
	}
	if h.systemThreads == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "system thread registry is not configured"})
		return
	}
	var body struct {
		Query     string   `json:"query"`
		Depth     string   `json:"depth,omitempty"`
		Intent    string   `json:"intent,omitempty"`
		Status    string   `json:"status,omitempty"`
		ProjectID string   `json:"project_id,omitempty"`
		ThreadIDs []string `json:"thread_id,omitempty"`
		Limit     int      `json:"limit,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	query := strings.TrimSpace(body.Query)
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing query"})
		return
	}
	limit := body.Limit
	if limit < 8 {
		limit = 8
	}
	if limit > 100 {
		limit = 100
	}
	depth := strings.TrimSpace(strings.ToLower(body.Depth))
	if depth != "off" && depth != "deep" {
		depth = "shallow"
	}

	intentOut, results, err := h.search.Search(r.Context(), searchsvc.Query{
		Text:      query,
		Limit:     limit,
		Status:    body.Status,
		ProjectID: body.ProjectID,
		ThreadIDs: body.ThreadIDs,
		Intent:    body.Intent,
		Depth:     depth,
	})
	if err != nil {
		h.writeErr(w, err)
		return
	}

	thread, err := h.systemThreads.EnsureSearchThread(r.Context(), h.store)
	if err != nil {
		h.writeErr(w, err)
		return
	}

	hiddenContext := buildSearchHiddenContext(intentOut, depth, results)
	maxIters := mapDepthToMaxIterations(depth)
	res, err := h.manager.StartRun(r.Context(), optionc.StartRunRequest{
		ThreadID:              thread.ID,
		Content:               query,
		HiddenContext:         hiddenContext,
		MaxIterationsOverride: &maxIters,
		Purpose:               "search",
		SearchDepth:           depth,
	})
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run":     runToDTO(res.Run),
		"runId":   res.Run.ID,
		"thread":  threadToDTO(thread),
		"intent":  intentOut,
		"depth":   depth,
		"results": results,
	})
}

func (h *Handler) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name,omitempty"`
	}
	_ = decodeJSONLimit(w, r, &body, h.maxBodyBytes) // allow empty body

	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Project"
	}

	// root_rel_path is currently informational; files live under cfgDir/projects/<id>/files.
	p, err := h.store.CreateProject(r.Context(), name, "projects")
	if err != nil {
		h.writeErr(w, err)
		return
	}
	_, _ = h.projects.EnsureFilesDir(p.ID)

	writeJSON(w, http.StatusOK, map[string]any{"project": projectToDTO(p)})
}

func (h *Handler) handleListProjects(w http.ResponseWriter, r *http.Request) {
	limit := parseInt(r.URL.Query().Get("limit"), 50, 1, 200)
	ps, err := h.store.ListProjects(r.Context(), limit)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projectsToDTO(ps)})
}

func (h *Handler) handleGetProject(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing projectId"})
		return
	}
	p, err := h.store.GetProject(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": projectToDTO(p)})
}

func (h *Handler) handleListProjectSources(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing projectId"})
		return
	}
	if _, err := h.store.GetProject(r.Context(), projectID); err != nil {
		h.writeErr(w, err)
		return
	}
	sources, err := h.projects.ListSources(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": projectSourcesToDTO(sources)})
}

func (h *Handler) handleUpdateProjectSource(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	sourceName := strings.TrimSpace(r.PathValue("sourceName"))
	if projectID == "" || sourceName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing projectId or sourceName"})
		return
	}
	var body struct {
		Hidden bool `json:"hidden"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if _, err := h.store.GetProject(r.Context(), projectID); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := h.projects.SetSourceHidden(r.Context(), projectID, sourceName, body.Hidden); err != nil {
		h.writeErr(w, err)
		return
	}
	p, err := h.refreshProjectFileIndex(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	sources, err := h.projects.ListSources(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": projectToDTO(p),
		"sources": projectSourcesToDTO(sources),
	})
}

func (h *Handler) handleDeleteProjectSource(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	sourceName := strings.TrimSpace(r.PathValue("sourceName"))
	if projectID == "" || sourceName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing projectId or sourceName"})
		return
	}
	if _, err := h.store.GetProject(r.Context(), projectID); err != nil {
		h.writeErr(w, err)
		return
	}
	if err := h.projects.DeleteSource(r.Context(), projectID, sourceName); err != nil {
		h.writeErr(w, err)
		return
	}
	p, err := h.refreshProjectFileIndex(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	sources, err := h.projects.ListSources(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": projectToDTO(p),
		"sources": projectSourcesToDTO(sources),
	})
}

func (h *Handler) handlePutProjectFile(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing projectId"})
		return
	}
	rel := strings.TrimSpace(r.URL.Query().Get("path"))
	clean, err := cleanRelPath(rel)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "git metadata paths are not allowed"})
		return
	}

	if r.ContentLength > maxProjectFileBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":     "file too large",
			"max_bytes": maxProjectFileBytes,
		})
		return
	}

	root, err := h.projects.EnsureFilesDir(projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	targetPath := filepath.Join(root, filepath.FromSlash(clean))
	ingested, err := fileingest.Ingest(fileingest.IngestRequest{
		Scope:        fileingest.ScopeProject,
		Source:       fileingest.SourceUpload,
		FileName:     path.Base(clean),
		DeclaredMIME: r.Header.Get("Content-Type"),
		Reader:       r.Body,
		TargetPath:   targetPath,
		MaxBytes:     maxProjectFileBytes,
		PreviewBytes: fileingest.DefaultPreviewBytes,
	})
	if err != nil {
		if errors.Is(err, fileingest.ErrTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
				"error":     "file too large",
				"max_bytes": maxProjectFileBytes,
			})
			return
		}
		h.writeErr(w, err)
		return
	}

	if err := h.store.UpsertProjectFile(r.Context(), projectID, clean, ingested.ByteSize, ingested.SHA256); err != nil {
		h.writeErr(w, err)
		return
	}
	evaluationMetadata := ingested.Evaluation.Metadata(fileingest.SourceUpload)
	extractionKey := filepipeline.ProjectExtractionKey(clean)
	extractionRelPath := filepath.ToSlash(filepath.Join("projects", projectID, "file_extractions", extractionKey))
	if extraction := filepipeline.ExtractFile(r.Context(), filepipeline.ExtractionRequest{
		SourcePath:          targetPath,
		OutputDir:           filepath.Join(filepath.Dir(root), "file_extractions", extractionKey),
		OutputRelPath:       extractionRelPath,
		FileName:            path.Base(clean),
		Evaluation:          ingested.Evaluation,
		IncludeRawXML:       false,
		EnableExternalTools: false,
	}); extraction != nil {
		evaluationMetadata["extraction"] = extraction
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"evaluation": evaluationMetadata,
	})
}

func (h *Handler) handleFinalizeProject(w http.ResponseWriter, r *http.Request) {
	projectID := strings.TrimSpace(r.PathValue("projectId"))
	if projectID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing projectId"})
		return
	}
	p, err := h.refreshProjectFileIndex(r.Context(), projectID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": projectToDTO(p)})
}

// --- helpers ---

func threadToDTO(t store.Thread) map[string]any {
	out := map[string]any{
		"id":             t.ID,
		"title":          t.Title,
		"status":         t.Status,
		"created_at":     t.CreatedAt.UnixMilli(),
		"updated_at":     t.UpdatedAt.UnixMilli(),
		"artifact_count": t.ArtifactCount,
	}
	if t.Summary != nil {
		out["summary"] = *t.Summary
	}
	if t.ProjectID != nil {
		out["project_id"] = *t.ProjectID
	}
	if t.LastMessageAt != nil {
		out["last_message_at"] = t.LastMessageAt.UnixMilli()
	}
	if t.LastRunID != nil {
		out["last_run_id"] = *t.LastRunID
	}
	if t.LastTarget != nil {
		out["lastTarget"] = t.LastTarget
	}
	if len(t.AttachmentPolicies) > 0 {
		out["attachmentPolicies"] = t.AttachmentPolicies
	}
	return out
}

func threadsToDTO(ts []store.Thread) []map[string]any {
	out := make([]map[string]any, 0, len(ts))
	for _, t := range ts {
		out = append(out, threadToDTO(t))
	}
	return out
}

func runToDTO(r store.Run) map[string]any {
	out := map[string]any{
		"id":             r.ID,
		"thread_id":      r.ThreadID,
		"status":         r.Status,
		"provider_id":    r.ProviderID,
		"model":          r.Model,
		"system_prompt":  r.SystemPrompt,
		"max_iterations": r.MaxIterations,
		"created_at":     r.CreatedAt.UnixMilli(),
		"updated_at":     r.UpdatedAt.UnixMilli(),
	}
	if strings.TrimSpace(r.ConnectionID) != "" {
		out["connection_id"] = r.ConnectionID
	}
	if strings.TrimSpace(r.PlanID) != "" {
		out["plan_id"] = r.PlanID
	}
	if strings.TrimSpace(r.Purpose) != "" {
		out["purpose"] = r.Purpose
	}
	if r.ProfileID != nil {
		out["profile_id"] = *r.ProfileID
	}
	if r.RequestID != nil {
		out["request_id"] = *r.RequestID
	}
	if r.InputTokens != nil {
		out["input_tokens"] = *r.InputTokens
	}
	if r.OutputTokens != nil {
		out["output_tokens"] = *r.OutputTokens
	}
	if r.TotalTokens != nil {
		out["total_tokens"] = *r.TotalTokens
	}
	if r.ErrorCode != nil {
		out["error_code"] = *r.ErrorCode
	}
	if r.ErrorMessage != nil {
		out["error_message"] = *r.ErrorMessage
	}
	if r.ContextTokensBefore != nil {
		out["context_tokens_before"] = *r.ContextTokensBefore
	}
	if r.ContextTokensAfter != nil {
		out["context_tokens_after"] = *r.ContextTokensAfter
	}
	if r.ContextSummaryBuckets != nil {
		out["context_summary_buckets"] = *r.ContextSummaryBuckets
	}
	if r.ContextSummaryRounds != nil {
		out["context_summary_rounds"] = *r.ContextSummaryRounds
	}
	if r.ContextArtifactRefsLoaded != nil {
		out["context_artifact_refs_loaded"] = *r.ContextArtifactRefsLoaded
	}
	if r.ContextNeedRetrieval != nil {
		out["context_need_retrieval"] = *r.ContextNeedRetrieval
	}
	if r.ContextNeedTooling != nil {
		out["context_need_retrieval_tooling"] = *r.ContextNeedTooling
	}
	if r.ContextHardCapped != nil {
		out["context_hard_capped"] = *r.ContextHardCapped
	}
	if r.ContextOverflowCompacted != nil {
		out["context_overflow_compacted"] = *r.ContextOverflowCompacted
	}
	if len(r.ContextPlanJSON) > 0 {
		var plan any
		if err := json.Unmarshal(r.ContextPlanJSON, &plan); err == nil {
			out["context_plan"] = plan
			if pm, ok := plan.(map[string]any); ok {
				if reason, ok := pm["plan_reason"].(string); ok && strings.TrimSpace(reason) != "" {
					out["context_plan_reason"] = strings.TrimSpace(reason)
				}
			}
		} else {
			out["context_plan"] = string(r.ContextPlanJSON)
		}
	}
	if r.StartedAt != nil {
		out["started_at"] = r.StartedAt.UnixMilli()
	}
	if r.EndedAt != nil {
		out["ended_at"] = r.EndedAt.UnixMilli()
	}
	return out
}

func preflightPlanDTO(plan store.RunPlan) map[string]any {
	return map[string]any{
		"planId":           plan.ID,
		"requirementsHash": plan.RequirementsHash,
		"status":           plan.ResultStatus,
		"blockers":         plan.Blockers,
		"choices":          plan.Choices,
		"notices":          plan.Notices,
		"staleEvidence":    plan.StaleEvidence,
		"plan":             json.RawMessage(plan.CapabilityReport),
		"expiresAt":        plan.ExpiresAt.UnixMilli(),
	}
}

func persistedPlanDTO(plan store.RunPlan) map[string]any {
	out := preflightPlanDTO(plan)
	out["lifecycleStatus"] = plan.Status
	out["target"] = plan.Target
	out["choiceSelections"] = plan.Selections
	out["createdAt"] = plan.CreatedAt.UnixMilli()
	if plan.FinalizedAt != nil {
		out["finalizedAt"] = plan.FinalizedAt.UnixMilli()
	}
	if plan.RunID != nil {
		out["runId"] = *plan.RunID
	}
	return out
}

func parseRunPlanFields(body map[string]any) (string, *store.RunTarget, []store.PlanChoiceSelection, []string, error) {
	planID := firstStringMapValue(body, "planId", "plan_id")
	var target *store.RunTarget
	if rawTarget, ok := body["target"].(map[string]any); ok {
		parsed := runs.NormalizeTarget(store.RunTarget{
			ConnectionID: firstStringMapValue(rawTarget, "connectionId", "connection_id"),
			Model:        firstStringMapValue(rawTarget, "model"),
		})
		if parsed.ConnectionID == "" || parsed.Model == "" {
			return "", nil, nil, nil, errors.New("target.connectionId and target.model are required")
		}
		target = &parsed
	} else if _, present := body["target"]; present {
		return "", nil, nil, nil, errors.New("target must be an object")
	}

	var selections []store.PlanChoiceSelection
	selectionValue, hasSelections := body["choiceSelections"]
	if !hasSelections {
		selectionValue, hasSelections = body["choice_selections"]
	}
	if hasSelections {
		rawSelections, ok := selectionValue.(map[string]any)
		if !ok {
			return "", nil, nil, nil, errors.New("choiceSelections must be an object keyed by issueId")
		}
		for issueID, raw := range rawSelections {
			selection, ok := raw.(map[string]any)
			if !ok {
				return "", nil, nil, nil, fmt.Errorf("choiceSelections.%s must be an object", issueID)
			}
			optionID := firstStringMapValue(selection, "optionId", "option_id")
			scope := firstStringMapValue(selection, "scope")
			if strings.TrimSpace(issueID) == "" || optionID == "" || scope == "" {
				return "", nil, nil, nil, fmt.Errorf("choiceSelections.%s requires optionId and scope", issueID)
			}
			selections = append(selections, store.PlanChoiceSelection{IssueID: strings.TrimSpace(issueID), OptionID: optionID, Scope: scope})
		}
	}

	features := []string{}
	if requirements, ok := body["requirements"].(map[string]any); ok {
		if raw, ok := requirements["features"].([]any); ok {
			features = extractStringSlice(raw)
		} else if _, present := requirements["features"]; present {
			return "", nil, nil, nil, errors.New("requirements.features must be an array of strings")
		}
	}
	return strings.TrimSpace(planID), target, selections, dedupeStringSlice(features), nil
}

func (h *Handler) providerConnectionSnapshot(target store.RunTarget) (string, string, error) {
	target = runs.NormalizeTarget(target)
	if target.ConnectionID == "" || target.Model == "" {
		return "", "", fmt.Errorf("%w: target.connectionId and target.model are required", store.ErrInvalid)
	}
	if h.settings == nil {
		return "", "", errors.New("settings store is unavailable")
	}
	settings := h.settings.Get()
	for i := range settings.ProviderConfigs {
		cfg := settings.ProviderConfigs[i]
		if strings.TrimSpace(cfg.ID) != target.ConnectionID {
			continue
		}
		providerID := strings.TrimSpace(cfg.ProviderID)
		if providerID == "" {
			return "", "", fmt.Errorf("%w: provider connection has no providerId", store.ErrInvalid)
		}
		// A thread may select another discovered model on the same saved
		// connection. Fingerprints are exact-target evidence keys, so hash the
		// requested model rather than reusing the connection's stamped default.
		cfg.Model = target.Model
		return providerID, config.ProviderConfigFingerprint(cfg), nil
	}
	activeID := ""
	if settings.ActiveProviderConfigID != nil {
		activeID = strings.TrimSpace(*settings.ActiveProviderConfigID)
	}
	if len(settings.ProviderConfigs) == 0 && (target.ConnectionID == "legacy-default" || target.ConnectionID == activeID) {
		providerID := strings.TrimSpace(settings.Provider.ProviderID)
		if providerID == "" {
			return "", "", fmt.Errorf("%w: no provider is configured", store.ErrInvalid)
		}
		payload, _ := json.Marshal(struct {
			Provider config.ProviderSelection `json:"provider"`
		}{Provider: settings.Provider})
		sum := sha256.Sum256(payload)
		return providerID, "sha256:" + hex.EncodeToString(sum[:]), nil
	}
	return "", "", store.ErrNotFound
}

func messageToUI(m store.Message, referencedThreadsByID map[string]store.Thread) map[string]any {
	out := map[string]any{
		"id":      m.ID,
		"seq":     m.Seq,
		"role":    string(m.Role),
		"content": nil,
	}
	if m.RunID != nil && strings.TrimSpace(*m.RunID) != "" {
		out["run_id"] = strings.TrimSpace(*m.RunID)
	}
	if m.Content != nil {
		out["content"] = *m.Content
	}
	if m.Thinking != nil {
		out["thinking"] = *m.Thinking
		out["thinkingComplete"] = m.ThinkingComplete
	}
	if len(m.ToolCalls) > 0 {
		out["tool_calls"] = m.ToolCalls
	}
	if m.ToolCallID != nil {
		out["tool_call_id"] = *m.ToolCallID
	}
	if m.Name != nil {
		out["name"] = *m.Name
	}
	if len(m.ReferencedMessageIDs) > 0 {
		out["referenced_message_ids"] = append([]string(nil), m.ReferencedMessageIDs...)
	}
	if len(m.ReferencedThreadIDs) > 0 {
		out["referenced_thread_ids"] = append([]string(nil), m.ReferencedThreadIDs...)
		referencedThreads := make([]map[string]any, 0, len(m.ReferencedThreadIDs))
		for _, threadID := range m.ReferencedThreadIDs {
			if thread, ok := referencedThreadsByID[threadID]; ok {
				referencedThreads = append(referencedThreads, threadToDTO(thread))
				continue
			}
			referencedThreads = append(referencedThreads, map[string]any{
				"id":    threadID,
				"title": "Referenced thread",
			})
		}
		if len(referencedThreads) > 0 {
			out["referenced_threads"] = referencedThreads
		}
	}
	if len(m.ReferencedArtifactIDs) > 0 {
		out["referenced_artifact_ids"] = append([]string(nil), m.ReferencedArtifactIDs...)
	}
	if strings.TrimSpace(m.ContextSummary) != "" {
		out["context_summary"] = m.ContextSummary
	}
	if len(m.ContextPlan) > 0 {
		var plan any
		if err := json.Unmarshal(m.ContextPlan, &plan); err == nil {
			out["context_plan"] = plan
		} else {
			out["context_plan"] = string(m.ContextPlan)
		}
	}
	if m.HiddenContext != nil && strings.TrimSpace(*m.HiddenContext) != "" {
		out["hidden_context"] = strings.TrimSpace(*m.HiddenContext)
	}
	return out
}

type badRequestError struct {
	message string
}

func (e badRequestError) Error() string {
	return strings.TrimSpace(e.message)
}

func appendHiddenContextSections(sections ...string) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		trimmed := strings.TrimSpace(section)
		if trimmed == "" {
			continue
		}
		parts = append(parts, trimmed)
	}
	return strings.Join(parts, "\n\n")
}

func (h *Handler) loadReferencedThreadsForMessages(ctx context.Context, currentThreadID string, msgs []store.Message) (map[string]store.Thread, error) {
	out := make(map[string]store.Thread)
	seen := make(map[string]struct{})
	for _, msg := range msgs {
		for _, threadID := range msg.ReferencedThreadIDs {
			id := strings.TrimSpace(threadID)
			if id == "" || id == currentThreadID {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			thread, err := h.store.GetThread(ctx, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return nil, err
			}
			out[id] = thread
		}
	}
	return out, nil
}

func (h *Handler) buildReferencedThreadsHiddenContext(ctx context.Context, currentThreadID string, query string, referencedThreadIDs []string) (string, []string, error) {
	if len(referencedThreadIDs) == 0 {
		return "", nil, nil
	}

	searchThreadID := ""
	if h.systemThreads != nil {
		searchThreadID = strings.TrimSpace(h.systemThreads.SearchThreadID())
	}

	resolvedThreads := make([]store.Thread, 0, len(referencedThreadIDs))
	sanitizedIDs := make([]string, 0, len(referencedThreadIDs))
	seen := make(map[string]struct{})
	for _, rawID := range referencedThreadIDs {
		id := strings.TrimSpace(rawID)
		if id == "" || id == currentThreadID || id == searchThreadID {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		thread, err := h.store.GetThread(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return "", nil, err
		}
		resolvedThreads = append(resolvedThreads, thread)
		sanitizedIDs = append(sanitizedIDs, id)
	}

	if len(sanitizedIDs) == 0 {
		return "", nil, badRequestError{message: "no valid referenced threads remain after filtering"}
	}

	var results []searchsvc.Result
	trimmedQuery := strings.TrimSpace(query)
	if h.search != nil && trimmedQuery != "" {
		_, searchResults, err := h.search.Search(ctx, searchsvc.Query{
			Text:      trimmedQuery,
			Limit:     24,
			ThreadIDs: sanitizedIDs,
			MaxChars:  280,
			Intent:    "analysis",
			Depth:     "shallow",
		})
		if err != nil {
			return "", nil, err
		}
		results = searchResults
	}

	return buildReferencedThreadsContext(trimmedQuery, resolvedThreads, results), sanitizedIDs, nil
}

func buildReferencedThreadsContext(query string, threads []store.Thread, results []searchsvc.Result) string {
	const (
		maxContextChars  = 8192
		maxSnippetChars  = 280
		maxHitsTotal     = 12
		maxHitsPerThread = 3
		maxSummaryChars  = 420
	)

	grouped := make(map[string][]searchsvc.Result)
	hitsPerThread := make(map[string]int)
	totalHits := 0
	for _, result := range results {
		threadID := strings.TrimSpace(result.ThreadID)
		if threadID == "" {
			continue
		}
		if hitsPerThread[threadID] >= maxHitsPerThread || totalHits >= maxHitsTotal {
			continue
		}
		grouped[threadID] = append(grouped[threadID], result)
		hitsPerThread[threadID]++
		totalHits++
	}

	var b strings.Builder
	b.WriteString("Referenced threads are attached as read-only background context.\n")
	if strings.TrimSpace(query) != "" {
		b.WriteString(fmt.Sprintf("Current request: %s\n", strings.TrimSpace(query)))
	}
	b.WriteString("Use the evidence below before exploring further with tools, and cite doc_id values when relying on prior-thread evidence.\n")

	for idx, thread := range threads {
		threadTitle := strings.TrimSpace(thread.Title)
		if threadTitle == "" {
			threadTitle = "Untitled thread"
		}
		if idx > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(fmt.Sprintf("Thread %d: %s (thread_id=%s)\n", idx+1, threadTitle, thread.ID))
		if thread.Summary != nil {
			summary := strings.TrimSpace(*thread.Summary)
			if summary != "" {
				if len(summary) > maxSummaryChars {
					summary = summary[:maxSummaryChars]
				}
				b.WriteString("Summary: ")
				b.WriteString(summary)
				b.WriteByte('\n')
			}
		}

		threadResults := grouped[thread.ID]
		if len(threadResults) == 0 {
			b.WriteString("Relevant hits: none retrieved from the search index for this request.\n")
			continue
		}
		b.WriteString("Relevant hits:\n")
		for _, result := range threadResults {
			b.WriteString(fmt.Sprintf("- [%s] %s (doc_id=%s, score=%.4f)\n", result.DocKind, result.Title, result.DocID, result.Score))
			snippet := strings.TrimSpace(result.Snippet)
			if snippet == "" {
				continue
			}
			if len(snippet) > maxSnippetChars {
				snippet = snippet[:maxSnippetChars]
			}
			b.WriteString("  Snippet: ")
			b.WriteString(snippet)
			b.WriteByte('\n')
		}
	}

	context := b.String()
	if len(context) > maxContextChars {
		context = context[:maxContextChars]
	}
	return context
}

func extractStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			continue
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

func parseRunActiveConnectionIDs(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	return dedupeStringSlice(extractStringSlice(arr)), true
}

func firstStringMapValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		s, ok := raw.(string)
		if !ok {
			continue
		}
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func extractImageAttachments(v any) ([]types.ImageAttachment, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	if len(arr) > maxInlineImageCount {
		return nil, fmt.Errorf("too many image attachments (max %d)", maxInlineImageCount)
	}

	totalBytes := int64(0)
	out := make([]types.ImageAttachment, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}

		dataURI := strings.TrimSpace(firstStringMapValue(obj, "dataUri", "data_uri"))
		if dataURI == "" {
			continue
		}
		if !strings.HasPrefix(strings.ToLower(dataURI), "data:image/") {
			return nil, errors.New("image attachments must be data:image/* URIs")
		}
		if len(dataURI) > maxInlineImageDataURIBytes {
			return nil, fmt.Errorf("image attachment exceeds maximum size (%d bytes)", maxInlineImageDataURIBytes)
		}
		totalBytes += int64(len(dataURI))
		if totalBytes > maxInlineImageTotalBytes {
			return nil, fmt.Errorf("total image attachment payload exceeds maximum size (%d bytes)", maxInlineImageTotalBytes)
		}

		fileName := strings.TrimSpace(firstStringMapValue(obj, "fileName", "file_name"))
		mimeType := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "mimeType", "mime_type")))
		if mimeType == "" {
			if inferred, ok := inferMimeTypeFromDataURI(dataURI); ok {
				mimeType = inferred
			}
		}
		mimeType = supportedInlineImageMimeType(mimeType)
		if mimeType == "" {
			return nil, errors.New("image attachments must be PNG, JPEG, WEBP, or GIF data URIs")
		}

		sizeBytes, sizeOK := extractOptionalInt64MapValue(obj, "sizeBytes", "size_bytes")
		if sizeOK && sizeBytes > 10*1024*1024 {
			return nil, errors.New("image attachment exceeds 10MB limit")
		}
		width, _ := extractOptionalIntMapValue(obj, "width")
		height, _ := extractOptionalIntMapValue(obj, "height")
		source := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "source")))
		artifactID := strings.TrimSpace(firstStringMapValue(obj, "artifactId", "artifact_id"))

		attachment := types.ImageAttachment{
			ArtifactID: artifactID,
			FileName:   fileName,
			MimeType:   mimeType,
			DataURI:    dataURI,
			Source:     source,
		}
		if sizeOK {
			v := sizeBytes
			attachment.SizeBytes = &v
		}
		if width > 0 {
			v := width
			attachment.Width = &v
		}
		if height > 0 {
			v := height
			attachment.Height = &v
		}
		out = append(out, attachment)
	}
	return out, nil
}

func dedupeImageAttachments(in []types.ImageAttachment) []types.ImageAttachment {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]types.ImageAttachment, 0, len(in))
	for _, item := range in {
		key := strings.TrimSpace(item.DataURI)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func inferMimeTypeFromDataURI(dataURI string) (string, bool) {
	raw := strings.TrimSpace(dataURI)
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return "", false
	}
	comma := strings.Index(raw, ",")
	if comma <= len("data:") {
		return "", false
	}
	meta := strings.ToLower(strings.TrimSpace(raw[len("data:"):comma]))
	semi := strings.Index(meta, ";")
	if semi >= 0 {
		meta = strings.TrimSpace(meta[:semi])
	}
	if !strings.HasPrefix(meta, "image/") {
		return "", false
	}
	return meta, true
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

func extractOptionalIntMapValue(m map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			if t > 0 {
				return int(t), true
			}
		case int:
			if t > 0 {
				return t, true
			}
		case int64:
			if t > 0 {
				return int(t), true
			}
		case json.Number:
			if n, err := t.Int64(); err == nil && n > 0 {
				return int(n), true
			}
		}
	}
	return 0, false
}

func extractOptionalSignedIntMapValue(m map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			return int(t), true
		case int:
			return t, true
		case int64:
			return int(t), true
		case json.Number:
			if n, err := t.Int64(); err == nil {
				return int(n), true
			}
		}
	}
	return 0, false
}

func extractOptionalInt64MapValue(m map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		v, ok := m[key]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case float64:
			if t > 0 {
				return int64(t), true
			}
		case int:
			if t > 0 {
				return int64(t), true
			}
		case int64:
			if t > 0 {
				return t, true
			}
		case json.Number:
			if n, err := t.Int64(); err == nil && n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

func parseRuntimeContext(raw map[string]any) (string, string, *int) {
	if len(raw) == 0 {
		return "", "", nil
	}
	nowISO := firstStringMapValue(raw, "nowIso", "now_iso", "nowISO", "now", "timestamp")
	timezone := firstStringMapValue(raw, "timezone", "timeZone", "time_zone", "tz")
	offsetMinutes, ok := extractOptionalSignedIntMapValue(raw, "utcOffsetMinutes", "utc_offset_minutes")
	if ok {
		if offsetMinutes < -24*60 || offsetMinutes > 24*60 {
			ok = false
		}
	}
	var offsetPtr *int
	if ok {
		v := offsetMinutes
		offsetPtr = &v
	}
	return nowISO, timezone, offsetPtr
}

func parseRunDataSources(raw map[string]any) *config.DatasourceConnections {
	if len(raw) == 0 {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var parsed config.DatasourceConnections
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil
	}
	if runDatasourceConnectionsEmpty(&parsed) {
		return nil
	}
	return &parsed
}

func parseRunDataSourceOverrides(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil
	}
	out := map[string]any{}
	for key, value := range parsed {
		name := strings.TrimSpace(strings.ToLower(key))
		if name == "" {
			continue
		}
		state, ok := value.(map[string]any)
		if !ok || len(state) == 0 {
			continue
		}
		if _, ok := state["enabled"]; !ok {
			if _, ok := state["is_enabled"]; !ok {
				continue
			}
		}
		out[name] = state
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func runDatasourceConnectionsEmpty(conns *config.DatasourceConnections) bool {
	if conns == nil {
		return true
	}
	rv := reflect.ValueOf(conns)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return true
	}
	elem := rv.Elem()
	if elem.Kind() != reflect.Struct {
		return true
	}
	for i := 0; i < elem.NumField(); i++ {
		field := elem.Field(i)
		if field.Kind() != reflect.Ptr || field.IsNil() {
			continue
		}
		if state, ok := field.Interface().(*config.DatasourceConnectionState); ok {
			if state != nil && state.Enabled != nil {
				return false
			}
			continue
		}
		target := field.Elem()
		if target.Kind() != reflect.Struct {
			continue
		}
		enabledField := target.FieldByName("Enabled")
		if enabledField.IsValid() && enabledField.Kind() == reflect.Ptr && !enabledField.IsNil() {
			return false
		}
	}
	return true
}

func dedupeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, item := range in {
		v := strings.TrimSpace(item)
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

func projectToDTO(p store.Project) map[string]any {
	out := map[string]any{
		"id":            p.ID,
		"name":          p.Name,
		"root_rel_path": p.RootRelPath,
		"file_count":    p.FileCount,
		"total_bytes":   p.TotalBytes,
		"created_at":    p.CreatedAt.UnixMilli(),
		"updated_at":    p.UpdatedAt.UnixMilli(),
	}
	if gitURL := strings.TrimPrefix(strings.TrimSpace(p.RootRelPath), gitProjectRootPrefix); gitURL != strings.TrimSpace(p.RootRelPath) {
		out["source"] = "git"
		out["git_url"] = gitURL
	} else if localPath := strings.TrimPrefix(strings.TrimSpace(p.RootRelPath), localProjectRootPrefix); localPath != strings.TrimSpace(p.RootRelPath) {
		out["source"] = "local"
		out["local_path"] = localPath
	} else {
		out["source"] = "upload"
	}
	return out
}

func projectsToDTO(ps []store.Project) []map[string]any {
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, projectToDTO(p))
	}
	return out
}

func projectSourceToDTO(source projects.Source) map[string]any {
	return map[string]any{
		"name":        source.Name,
		"path":        source.Path,
		"kind":        source.Kind,
		"hidden":      source.Hidden,
		"file_count":  source.FileCount,
		"total_bytes": source.TotalBytes,
	}
}

func projectSourcesToDTO(sources []projects.Source) []map[string]any {
	out := make([]map[string]any, 0, len(sources))
	for _, source := range sources {
		out = append(out, projectSourceToDTO(source))
	}
	return out
}

func parseNonNegativeInt64(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	if n < 0 {
		return 0, false
	}
	return n, true
}

func parseColumnsCSV(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	for _, part := range parts {
		key := strings.TrimSpace(part)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func (h *Handler) readArtifactContentWithLimit(a store.Artifact, maxBytes int64) (string, error) {
	if maxBytes > 0 && a.ByteSize > 0 && a.ByteSize > maxBytes {
		return "", errors.New("artifact content is too large for row paging")
	}
	switch strings.TrimSpace(a.Storage) {
	case "file":
		if a.ContentPath == nil || strings.TrimSpace(*a.ContentPath) == "" {
			return "", nil
		}
		rel, err := cleanRelPath(strings.TrimSpace(*a.ContentPath))
		if err != nil {
			return "", err
		}
		full := filepath.Join(h.cfgDir, filepath.FromSlash(rel))
		b, err := osReadFileLimit(full, maxBytes)
		if err != nil {
			return "", err
		}
		return string(b), nil
	default:
		if a.ContentText == nil {
			return "", nil
		}
		content := *a.ContentText
		if maxBytes > 0 && int64(len(content)) > maxBytes {
			return "", errors.New("artifact content is too large for row paging")
		}
		return content, nil
	}
}

func parseDatasetRows(raw string) ([]map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("artifact content is empty")
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, errors.New("artifact content is not valid JSON")
	}
	switch v := parsed.(type) {
	case []any:
		rows := make([]map[string]any, 0, len(v))
		for _, row := range v {
			m, ok := row.(map[string]any)
			if !ok {
				return nil, errors.New("artifact dataset must be an array of objects")
			}
			rows = append(rows, m)
		}
		return rows, nil
	case map[string]any:
		if rows, ok := v["rows"]; ok {
			arr, ok := rows.([]any)
			if !ok {
				return nil, errors.New("artifact rows field must be an array")
			}
			out := make([]map[string]any, 0, len(arr))
			for _, row := range arr {
				m, ok := row.(map[string]any)
				if !ok {
					return nil, errors.New("artifact dataset must be an array of objects")
				}
				out = append(out, m)
			}
			return out, nil
		}
		return nil, errors.New("artifact dataset is missing rows array")
	default:
		return nil, errors.New("artifact dataset must be JSON array")
	}
}

func projectRowsColumns(rows []map[string]any, columns []string) []map[string]any {
	if len(columns) == 0 || len(rows) == 0 {
		return rows
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		next := map[string]any{}
		for _, column := range columns {
			if value, ok := row[column]; ok {
				next[column] = value
			}
		}
		out = append(out, next)
	}
	return out
}

func clampRowsByResponseBytes(rows []map[string]any, maxBytes int) []map[string]any {
	if len(rows) == 0 || maxBytes <= 0 {
		return rows
	}
	out := rows
	for len(out) > 0 {
		b, err := json.Marshal(out)
		if err != nil {
			return rows[:0]
		}
		if len(b) <= maxBytes {
			return out
		}
		out = out[:len(out)-1]
	}
	return out
}

func parseInt(raw string, def int, min int, max int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	var n int
	_, err := fmt.Sscanf(raw, "%d", &n)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func parseBool(raw string) bool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func buildSearchHiddenContext(intent searchsvc.Intent, depth string, results []searchsvc.Result) string {
	const maxSnippetChars = 260
	const maxContextChars = 4096

	var b strings.Builder
	b.WriteString("Search mode is active. Prioritize retrieval-grounded answers.\n")
	b.WriteString(fmt.Sprintf("Intent: %s (confidence=%.2f)\n", intent.Kind, intent.Confidence))
	if strings.TrimSpace(depth) != "" {
		b.WriteString(fmt.Sprintf("Depth: %s\n", strings.TrimSpace(depth)))
	}
	if len(results) == 0 {
		b.WriteString("No search results were found.\n")
		return b.String()
	}
	b.WriteString("Top scoped results:\n")
	maxRows := len(results)
	if maxRows > 12 {
		maxRows = 12
	}
	for i := 0; i < maxRows; i++ {
		r := results[i]
		b.WriteString(fmt.Sprintf("%d) [%s] %s (doc_id=%s, thread_id=%s, score=%.4f)\n", i+1, r.DocKind, r.Title, r.DocID, r.ThreadID, r.Score))
		if strings.TrimSpace(r.Snippet) != "" {
			b.WriteString("   Snippet: ")
			snippet := strings.TrimSpace(r.Snippet)
			if len(snippet) > maxSnippetChars {
				snippet = snippet[:maxSnippetChars]
			}
			b.WriteString(snippet)
			b.WriteByte('\n')
		}
	}
	b.WriteString("Cite doc_id values in the answer when referencing evidence.\n")
	context := b.String()
	if len(context) > maxContextChars {
		context = context[:maxContextChars]
	}
	return context
}

func mapDepthToMaxIterations(depth string) int {
	switch strings.TrimSpace(strings.ToLower(depth)) {
	case "off":
		return 4
	case "deep":
		return 24
	default:
		return 12
	}
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	if dst == nil {
		return errors.New("nil dst")
	}
	if limit <= 0 {
		limit = 1 << 20
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected additional JSON content")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(payload)
}

func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	var badReq badRequestError
	switch {
	case errors.As(err, &badReq):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": badReq.Error()})
	case errors.Is(err, store.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
	case errors.Is(err, store.ErrPlanStale):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "plan_stale", "message": "The run plan expired or no longer matches this request. Preflight again."})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	case errors.Is(err, os.ErrNotExist):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	case errors.Is(err, os.ErrPermission):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	case errors.Is(err, store.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func (h *Handler) writeSSEError(w http.ResponseWriter, f http.Flusher, err error) {
	payload := []byte(`{"message":"` + escapeJSONString(err.Error()) + `"}`)
	_ = (optionc.SSEEvent{Event: "error", Data: payload}).WriteSSETo(w)
	f.Flush()
}

func (h *Handler) writeQueryJobErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, optionc.ErrQueryJobsUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": err.Error()})
	case errors.Is(err, optionc.ErrQueryJobUnauthorized), errors.Is(err, optionc.ErrQueryJobThreadScope), errors.Is(err, optionc.ErrQueryJobRunScope):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden"})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	case errors.Is(err, store.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

func escapeJSONString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

func cleanRelPath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimPrefix(raw, "/")
	clean := path.Clean(raw)
	if clean == "." || clean == "" {
		return "", errors.New("invalid path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path traversal is not allowed")
	}
	if strings.ContainsRune(clean, '\x00') {
		return "", errors.New("invalid path")
	}
	return clean, nil
}

func osReadFileLimit(filename string, limit int64) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return b[:limit], errors.New("file too large")
	}
	return b, nil
}
