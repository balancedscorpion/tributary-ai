package slack

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

const (
	authorizeURL            = "https://slack.com/oauth/v2/authorize"
	oauthFlowTTL            = 10 * time.Minute
	oauthResultTTL          = 5 * time.Minute
	refreshSkew             = 5 * time.Minute
	defaultUserAgent        = "ai-analyst-local"
	maxManagedDownloadBytes = 128 << 20
)

var apiBaseURL = "https://slack.com/api"

var defaultUserScopes = []string{
	"team:read",
	"users:read",
	"channels:read",
	"channels:history",
	"groups:read",
	"groups:history",
	"im:read",
	"im:history",
	"mpim:read",
	"mpim:history",
	"files:read",
	"search:read",
	"chat:write",
	"files:write",
}

var defaultBotScopes = []string{
	"chat:write",
	"files:write",
}

const (
	IdentityUser = "user"
	IdentityBot  = "bot"
)

type Manager struct {
	Settings *config.Store
	Secrets  *secrets.Store

	httpc *http.Client

	oauthMu    sync.Mutex
	oauthFlows map[string]*oauthPendingFlow

	tokenMu sync.Mutex

	approvalMu sync.Mutex
	approvals  map[string]approvalAction
}

type oauthPendingFlow struct {
	State        string
	ClientID     string
	RedirectURI  string
	CodeVerifier string
	RequestedBot bool
	CreatedAt    time.Time
	ExpiresAt    time.Time
	Result       *oauthFlowResult
}

type oauthFlowResult struct {
	OK          bool
	Message     string
	Error       string
	CompletedAt time.Time
}

type PendingApproval struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Detail    string         `json:"detail"`
	ToolName  string         `json:"toolName"`
	Product   string         `json:"product"`
	Action    string         `json:"action"`
	Target    map[string]any `json:"target,omitempty"`
	Content   map[string]any `json:"content,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

type approvalAction struct {
	PendingApproval
	Operation string
	Args      map[string]any
}

type StatusResult struct {
	Configured         bool     `json:"configured"`
	ClientIDConfigured bool     `json:"client_id_configured"`
	ClientID           string   `json:"client_id,omitempty"`
	ClientSecretSet    bool     `json:"client_secret_set,omitempty"`
	PKCERedirectURI    string   `json:"pkce_redirect_uri,omitempty"`
	SecretsInitialized bool     `json:"secrets_initialized"`
	SecretsLocked      bool     `json:"secrets_locked"`
	Connected          bool     `json:"connected"`
	UserConnected      bool     `json:"user_connected"`
	BotConnected       bool     `json:"bot_connected"`
	Enabled            bool     `json:"enabled"`
	ReadToolsEnabled   bool     `json:"read_tools_enabled"`
	WriteToolsEnabled  bool     `json:"write_tools_enabled"`
	WriteIdentity      string   `json:"write_identity,omitempty"`
	TeamID             string   `json:"team_id,omitempty"`
	TeamName           string   `json:"team_name,omitempty"`
	EnterpriseID       string   `json:"enterprise_id,omitempty"`
	AuthedUserID       string   `json:"authed_user_id,omitempty"`
	BotUserID          string   `json:"bot_user_id,omitempty"`
	BotID              string   `json:"bot_id,omitempty"`
	Scope              string   `json:"scope,omitempty"`
	GrantedScopes      []string `json:"granted_scopes,omitempty"`
	UserScope          string   `json:"user_scope,omitempty"`
	UserGrantedScopes  []string `json:"user_granted_scopes,omitempty"`
	BotScope           string   `json:"bot_scope,omitempty"`
	BotGrantedScopes   []string `json:"bot_granted_scopes,omitempty"`
	BotOAuthAvailable  bool     `json:"bot_oauth_available"`
	BotOAuthBlocker    string   `json:"bot_oauth_blocker,omitempty"`
	ExpiresAt          int64    `json:"expires_at,omitempty"`
	BotExpiresAt       int64    `json:"bot_expires_at,omitempty"`
	LastCheckedAt      *int64   `json:"lastCheckedAt,omitempty"`
	LastCheckOk        *bool    `json:"lastCheckOk,omitempty"`
	LastCheckMessage   string   `json:"lastCheckMessage,omitempty"`
	Message            string   `json:"message,omitempty"`
}

type CheckResult struct {
	OK     bool         `json:"ok"`
	Status StatusResult `json:"status"`
	Auth   any          `json:"auth,omitempty"`
	Team   any          `json:"team,omitempty"`
}

type ConversationListRequest struct {
	Types           string `json:"types,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	ExcludeArchived bool   `json:"exclude_archived,omitempty"`
}

type ConversationRequest struct {
	ChannelID string `json:"channel_id,omitempty"`
	Channel   string `json:"channel,omitempty"`
}

type ConversationMembersRequest struct {
	ChannelID string `json:"channel_id,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
}

type MessagesRequest struct {
	ChannelID string `json:"channel_id,omitempty"`
	Channel   string `json:"channel,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Oldest    string `json:"oldest,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Inclusive bool   `json:"inclusive,omitempty"`
}

type ThreadRepliesRequest struct {
	ChannelID string `json:"channel_id,omitempty"`
	Channel   string `json:"channel,omitempty"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	Limit     int    `json:"limit,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Oldest    string `json:"oldest,omitempty"`
	Latest    string `json:"latest,omitempty"`
	Inclusive bool   `json:"inclusive,omitempty"`
}

type UserRequest struct {
	UserID string `json:"user_id,omitempty"`
	User   string `json:"user,omitempty"`
}

type SearchMessagesRequest struct {
	Query   string `json:"query,omitempty"`
	Count   int    `json:"count,omitempty"`
	Page    int    `json:"page,omitempty"`
	Sort    string `json:"sort,omitempty"`
	SortDir string `json:"sort_dir,omitempty"`
}

type PostMessageRequest struct {
	ChannelID      string `json:"channel_id,omitempty"`
	Channel        string `json:"channel,omitempty"`
	SendAs         string `json:"send_as,omitempty"`
	Text           string `json:"text,omitempty"`
	ThreadTS       string `json:"thread_ts,omitempty"`
	ReplyBroadcast bool   `json:"reply_broadcast,omitempty"`
	UnfurlLinks    *bool  `json:"unfurl_links,omitempty"`
	UnfurlMedia    *bool  `json:"unfurl_media,omitempty"`
}

type UploadFileRequest struct {
	ChannelID      string `json:"channel_id,omitempty"`
	Channel        string `json:"channel,omitempty"`
	SendAs         string `json:"send_as,omitempty"`
	FileName       string `json:"file_name,omitempty"`
	Title          string `json:"title,omitempty"`
	MimeType       string `json:"mime_type,omitempty"`
	AltText        string `json:"alt_text,omitempty"`
	InitialComment string `json:"initial_comment,omitempty"`
	ThreadTS       string `json:"thread_ts,omitempty"`
	ContentBase64  string `json:"content_base64,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Source         string `json:"source,omitempty"`
	SourceRef      string `json:"source_ref,omitempty"`
}

type DownloadFileRequest struct {
	FileID           string `json:"file_id,omitempty"`
	ChannelID        string `json:"channel_id,omitempty"`
	MaxDownloadBytes int64  `json:"max_download_bytes,omitempty"`
}

func NewManager(settings *config.Store, secretsStore *secrets.Store) *Manager {
	return &Manager{
		Settings: settings,
		Secrets:  secretsStore,
		httpc: &http.Client{
			Timeout: 0,
		},
		approvals: map[string]approvalAction{},
	}
}

func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/integrations/slack/status"):
		m.handleStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/integrations/slack/oauth/start"):
		m.handleOAuthStart(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/integrations/slack/oauth/callback"):
		m.handleOAuthCallback(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/integrations/slack/oauth/result"):
		m.handleOAuthResult(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/integrations/slack/check"):
		m.handleCheck(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/integrations/slack/disconnect"):
		m.handleDisconnect(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/integrations/slack/approvals"):
		m.handleApprovals(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (m *Manager) Status(ctx context.Context) (StatusResult, error) {
	if err := contextErr(ctx); err != nil {
		return StatusResult{}, err
	}
	out := StatusResult{
		ClientID:          strings.TrimSpace(m.slackOAuthClientID()),
		WriteIdentity:     m.ResolveWriteIdentity(""),
		BotOAuthAvailable: true,
	}
	out.ClientIDConfigured = out.ClientID != ""
	if m.Settings != nil {
		s := m.Settings.Get()
		if s.Slack != nil {
			out.Enabled = s.Slack.Enabled != nil && *s.Slack.Enabled
			out.ReadToolsEnabled = out.Enabled
			if s.Slack.ReadTools != nil {
				out.ReadToolsEnabled = out.ReadToolsEnabled && *s.Slack.ReadTools
			}
			out.WriteToolsEnabled = out.Enabled
			if s.Slack.WriteTools != nil {
				out.WriteToolsEnabled = out.WriteToolsEnabled && *s.Slack.WriteTools
			} else {
				out.WriteToolsEnabled = false
			}
			out.LastCheckedAt = cloneInt64Ptr(s.Slack.LastCheckedAt)
			out.LastCheckOk = cloneBoolPtr(s.Slack.LastCheckOk)
			if s.Slack.LastCheckMessage != nil {
				out.LastCheckMessage = strings.TrimSpace(*s.Slack.LastCheckMessage)
			}
		}
	}
	if m.Secrets == nil {
		out.SecretsLocked = true
		out.Configured = out.ClientIDConfigured
		return out, nil
	}
	initialized, locked, configured := m.Secrets.Status()
	out.SecretsInitialized = initialized
	out.SecretsLocked = locked
	out.Connected = configured.Slack
	out.UserConnected = configured.SlackUser
	out.BotConnected = configured.SlackBot
	out.ClientSecretSet = configured.SlackClientSecret
	out.TeamID = strings.TrimSpace(configured.SlackTeamID)
	out.TeamName = strings.TrimSpace(configured.SlackTeamName)
	out.AuthedUserID = strings.TrimSpace(configured.SlackAuthedUserID)
	out.BotUserID = strings.TrimSpace(configured.SlackBotUserID)
	out.Scope = strings.TrimSpace(configured.SlackScope)
	out.GrantedScopes = splitScopes(out.Scope)
	out.UserScope = out.Scope
	out.UserGrantedScopes = append([]string(nil), out.GrantedScopes...)
	out.BotScope = strings.TrimSpace(configured.SlackBotScope)
	out.BotGrantedScopes = splitScopes(out.BotScope)
	out.Configured = out.ClientIDConfigured || out.Connected
	if initialized && !locked {
		if sec, err := m.Secrets.GetSlack(); err == nil {
			out.ClientSecretSet = strings.TrimSpace(sec.ClientSecret) != ""
			out.UserConnected = strings.TrimSpace(sec.AccessToken) != ""
			out.BotConnected = strings.TrimSpace(sec.BotAccessToken) != ""
			out.Connected = out.UserConnected || out.BotConnected
			out.TeamID = strings.TrimSpace(sec.TeamID)
			out.TeamName = strings.TrimSpace(sec.TeamName)
			out.EnterpriseID = strings.TrimSpace(sec.EnterpriseID)
			out.AuthedUserID = strings.TrimSpace(sec.AuthedUserID)
			out.BotUserID = strings.TrimSpace(sec.BotUserID)
			out.BotID = strings.TrimSpace(sec.BotID)
			out.Scope = strings.TrimSpace(sec.Scope)
			out.GrantedScopes = splitScopes(out.Scope)
			out.UserScope = out.Scope
			out.UserGrantedScopes = append([]string(nil), out.GrantedScopes...)
			out.BotScope = strings.TrimSpace(sec.BotScope)
			out.BotGrantedScopes = splitScopes(out.BotScope)
			out.ExpiresAt = sec.ExpiresAt
			out.BotExpiresAt = sec.BotExpiresAt
		}
	}
	return out, nil
}

func (m *Manager) Check(ctx context.Context) (CheckResult, error) {
	status, err := m.Status(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	out := CheckResult{Status: status}
	authByIdentity := map[string]any{}
	var firstErr error
	if status.UserConnected {
		auth, err := m.apiCallWithIdentity(ctx, IdentityUser, "auth.test", nil)
		if err != nil {
			firstErr = err
		} else {
			authByIdentity[IdentityUser] = auth
		}
	}
	if status.BotConnected {
		auth, err := m.apiCallWithIdentity(ctx, IdentityBot, "auth.test", nil)
		if err != nil && firstErr == nil {
			firstErr = err
		} else if err == nil {
			authByIdentity[IdentityBot] = auth
		}
	}
	if len(authByIdentity) == 0 {
		if firstErr != nil {
			m.applySlackHealth(&out.Status, false, firstErr.Error(), false)
			return out, firstErr
		}
		err := errors.New("Slack OAuth connection is not configured")
		m.applySlackHealth(&out.Status, false, err.Error(), false)
		return out, err
	}
	team, teamErr := m.apiCall(ctx, "team.info", nil)
	if teamErr != nil {
		team = nil
	}
	out.OK = true
	out.Auth = authByIdentity
	out.Team = team
	out.Status.Connected = true
	out.Status.Configured = true
	m.applySlackHealth(&out.Status, true, "Slack OAuth connection is usable.", false)
	return out, nil
}

func (m *Manager) applySlackHealth(status *StatusResult, ok bool, message string, disable bool) {
	checkedAt, checkedOk, checkedMessage := m.persistSlackHealth(ok, message, disable)
	if status == nil {
		return
	}
	status.LastCheckedAt = &checkedAt
	status.LastCheckOk = &checkedOk
	status.LastCheckMessage = checkedMessage
	status.Message = checkedMessage
	if disable {
		status.Enabled = false
		status.ReadToolsEnabled = false
		status.WriteToolsEnabled = false
	}
}

func (m *Manager) persistSlackHealth(ok bool, message string, disable bool) (int64, bool, string) {
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		if ok {
			checkedMessage = "Slack connection verified."
		} else {
			checkedMessage = "Slack connection check failed."
		}
	}
	if m == nil || m.Settings == nil {
		return checkedAt, ok, checkedMessage
	}
	next := m.Settings.Get()
	if next.Slack == nil {
		next.Slack = config.DefaultSettings().Slack
	}
	if next.Slack == nil {
		next.Slack = &config.SlackSettings{}
	}
	if disable {
		off := false
		next.Slack.Enabled = &off
		next.Slack.ReadTools = &off
		next.Slack.WriteTools = &off
	}
	next.Slack.LastCheckedAt = &checkedAt
	next.Slack.LastCheckOk = &ok
	next.Slack.LastCheckMessage = &checkedMessage
	_ = m.Settings.Save(next)
	return checkedAt, ok, checkedMessage
}

func (m *Manager) ListConversations(ctx context.Context, req ConversationListRequest) (map[string]any, error) {
	params := url.Values{}
	types := strings.TrimSpace(req.Types)
	if types == "" {
		types = "public_channel,private_channel,mpim,im"
	}
	params.Set("types", types)
	params.Set("limit", strconv.Itoa(clampInt(req.Limit, 20, 100)))
	if strings.TrimSpace(req.Cursor) != "" {
		params.Set("cursor", strings.TrimSpace(req.Cursor))
	}
	if req.ExcludeArchived {
		params.Set("exclude_archived", "true")
	}
	out, err := m.apiCall(ctx, "conversations.list", params)
	if err != nil {
		return nil, err
	}
	return m.filterConversationList(out), nil
}

func (m *Manager) GetConversation(ctx context.Context, req ConversationRequest) (map[string]any, error) {
	channel, err := m.normalizeAllowedChannel(firstNonEmpty(req.ChannelID, req.Channel))
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("channel", channel)
	return m.apiCall(ctx, "conversations.info", params)
}

func (m *Manager) GetConversationMembers(ctx context.Context, req ConversationMembersRequest) (map[string]any, error) {
	channel, err := m.normalizeAllowedChannel(firstNonEmpty(req.ChannelID, req.Channel))
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("channel", channel)
	params.Set("limit", strconv.Itoa(clampInt(req.Limit, 50, 200)))
	if strings.TrimSpace(req.Cursor) != "" {
		params.Set("cursor", strings.TrimSpace(req.Cursor))
	}
	return m.apiCall(ctx, "conversations.members", params)
}

func (m *Manager) GetMessages(ctx context.Context, req MessagesRequest) (map[string]any, error) {
	channel, err := m.normalizeAllowedChannel(firstNonEmpty(req.ChannelID, req.Channel))
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Set("channel", channel)
	params.Set("limit", strconv.Itoa(clampInt(req.Limit, 20, 100)))
	setOptional(params, "cursor", req.Cursor)
	setOptional(params, "oldest", req.Oldest)
	setOptional(params, "latest", req.Latest)
	if req.Inclusive {
		params.Set("inclusive", "true")
	}
	return m.apiCall(ctx, "conversations.history", params)
}

func (m *Manager) GetThreadReplies(ctx context.Context, req ThreadRepliesRequest) (map[string]any, error) {
	channel, err := m.normalizeAllowedChannel(firstNonEmpty(req.ChannelID, req.Channel))
	if err != nil {
		return nil, err
	}
	threadTS := strings.TrimSpace(req.ThreadTS)
	if threadTS == "" {
		return nil, errors.New("thread_ts is required")
	}
	params := url.Values{}
	params.Set("channel", channel)
	params.Set("ts", threadTS)
	params.Set("limit", strconv.Itoa(clampInt(req.Limit, 20, 100)))
	setOptional(params, "cursor", req.Cursor)
	setOptional(params, "oldest", req.Oldest)
	setOptional(params, "latest", req.Latest)
	if req.Inclusive {
		params.Set("inclusive", "true")
	}
	return m.apiCall(ctx, "conversations.replies", params)
}

func (m *Manager) GetUser(ctx context.Context, req UserRequest) (map[string]any, error) {
	user := strings.TrimSpace(firstNonEmpty(req.UserID, req.User))
	if user == "" {
		return nil, errors.New("user_id is required")
	}
	params := url.Values{}
	params.Set("user", user)
	return m.apiCall(ctx, "users.info", params)
}

func (m *Manager) SearchMessages(ctx context.Context, req SearchMessagesRequest) (map[string]any, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, errors.New("query is required")
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("count", strconv.Itoa(clampInt(req.Count, 20, 25)))
	if req.Page > 0 {
		params.Set("page", strconv.Itoa(req.Page))
	}
	switch strings.TrimSpace(strings.ToLower(req.Sort)) {
	case "", "score", "timestamp":
		setOptional(params, "sort", strings.TrimSpace(strings.ToLower(req.Sort)))
	default:
		return nil, errors.New("sort must be score or timestamp")
	}
	switch strings.TrimSpace(strings.ToLower(req.SortDir)) {
	case "", "asc", "desc":
		setOptional(params, "sort_dir", strings.TrimSpace(strings.ToLower(req.SortDir)))
	default:
		return nil, errors.New("sort_dir must be asc or desc")
	}
	return m.apiCall(ctx, "search.messages", params)
}

func (m *Manager) DownloadFile(ctx context.Context, appConfigDir string, req DownloadFileRequest) (map[string]any, error) {
	appConfigDir = strings.TrimSpace(appConfigDir)
	if appConfigDir == "" {
		return nil, errors.New("app config directory is required for managed Slack downloads")
	}
	fileID := strings.TrimSpace(req.FileID)
	if fileID == "" {
		return nil, errors.New("file_id is required")
	}
	if err := m.requireScopesFor(ctx, IdentityUser, "files:read"); err != nil {
		return nil, err
	}
	info, err := m.apiCallWithIdentity(ctx, IdentityUser, "files.info", url.Values{"file": []string{fileID}})
	if err != nil {
		return nil, err
	}
	file := mapValue(info["file"])
	if len(file) == 0 {
		return nil, errors.New("Slack files.info did not return file metadata")
	}
	downloadURL := firstNonEmpty(
		stringValue(file["url_private_download"]),
		stringValue(file["url_private"]),
	)
	if downloadURL == "" {
		return nil, errors.New("Slack file does not expose a private download URL")
	}
	token, err := m.accessTokenFor(ctx, IdentityUser)
	if err != nil {
		return nil, err
	}
	maxBytes := clampInt64(req.MaxDownloadBytes, maxManagedDownloadBytes, maxManagedDownloadBytes)
	raw, contentType, err := m.downloadSlackPrivateURL(ctx, token, downloadURL, maxBytes)
	if err != nil {
		return nil, err
	}
	fileName := sanitizeSlackDownloadFileName(firstNonEmpty(
		stringValue(file["name"]),
		stringValue(file["title"]),
		fileID,
	))
	mimeType := firstNonEmpty(
		stringValue(file["mimetype"]),
		contentType,
		mime.TypeByExtension(strings.ToLower(path.Ext(fileName))),
		"application/octet-stream",
	)
	descriptor, err := writeSlackManagedDownload(appConfigDir, fileID, fileName, mimeType, raw)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"ok":                  true,
		"file_id":             fileID,
		"file":                file,
		"file_name":           descriptor["file_name"],
		"fileName":            descriptor["fileName"],
		"mime_type":           descriptor["mime_type"],
		"mimeType":            descriptor["mimeType"],
		"size_bytes":          descriptor["size_bytes"],
		"sizeBytes":           descriptor["sizeBytes"],
		"sha256":              descriptor["sha256"],
		"local_path":          descriptor["local_path"],
		"localPath":           descriptor["localPath"],
		"local_relative_path": descriptor["local_relative_path"],
		"localRelativePath":   descriptor["localRelativePath"],
		"download":            descriptor,
		"source":              "slack_managed_download",
	}
	if channelID := strings.TrimSpace(req.ChannelID); channelID != "" {
		out["channel_id"] = channelID
	}
	return out, nil
}

func (m *Manager) PostMessage(ctx context.Context, req PostMessageRequest) (map[string]any, error) {
	channel, err := m.normalizeAllowedChannel(firstNonEmpty(req.ChannelID, req.Channel))
	if err != nil {
		return nil, err
	}
	identity := m.ResolveWriteIdentity(req.SendAs)
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return nil, errors.New("text is required")
	}
	if err := m.requireScopesFor(ctx, identity, "chat:write"); err != nil {
		return nil, err
	}
	body := map[string]any{
		"channel": channel,
		"text":    text,
	}
	if threadTS := strings.TrimSpace(req.ThreadTS); threadTS != "" {
		body["thread_ts"] = threadTS
		if req.ReplyBroadcast {
			body["reply_broadcast"] = true
		}
	}
	if req.UnfurlLinks != nil {
		body["unfurl_links"] = *req.UnfurlLinks
	}
	if req.UnfurlMedia != nil {
		body["unfurl_media"] = *req.UnfurlMedia
	}
	out, err := m.apiPostJSONWithIdentity(ctx, identity, "chat.postMessage", body)
	if err != nil {
		return nil, err
	}
	ts := strings.TrimSpace(stringValue(out["ts"]))
	permalink := ""
	if ts != "" {
		if p, pErr := m.apiCallWithIdentity(ctx, identity, "chat.getPermalink", url.Values{"channel": []string{channel}, "message_ts": []string{ts}}); pErr == nil {
			permalink = strings.TrimSpace(stringValue(p["permalink"]))
		}
	}
	return map[string]any{
		"ok":         true,
		"send_as":    identity,
		"message":    out,
		"channel_id": channel,
		"ts":         ts,
		"permalink":  nilIfEmpty(permalink),
	}, nil
}

func (m *Manager) UploadFile(ctx context.Context, req UploadFileRequest) (map[string]any, error) {
	channel, err := m.normalizeAllowedChannel(firstNonEmpty(req.ChannelID, req.Channel))
	if err != nil {
		return nil, err
	}
	identity := m.ResolveWriteIdentity(req.SendAs)
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		return nil, errors.New("file_name is required")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ContentBase64))
	if err != nil {
		return nil, errors.New("content_base64 is not valid base64")
	}
	if len(raw) == 0 {
		return nil, errors.New("file content is empty")
	}
	if err := m.requireScopesFor(ctx, identity, "files:write"); err != nil {
		return nil, err
	}
	uploadBody := map[string]any{
		"filename": fileName,
		"length":   len(raw),
	}
	if altText := strings.TrimSpace(req.AltText); altText != "" {
		uploadBody["alt_txt"] = altText
	}
	uploadTicket, err := m.apiPostJSONWithIdentity(ctx, identity, "files.getUploadURLExternal", uploadBody)
	if err != nil {
		return nil, err
	}
	uploadURL := strings.TrimSpace(stringValue(uploadTicket["upload_url"]))
	fileID := strings.TrimSpace(stringValue(uploadTicket["file_id"]))
	if uploadURL == "" || fileID == "" {
		return nil, errors.New("Slack did not return an upload URL and file ID")
	}
	if err := m.uploadRawFile(ctx, uploadURL, strings.TrimSpace(req.MimeType), raw); err != nil {
		return nil, err
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = fileName
	}
	completeBody := map[string]any{
		"files": []map[string]any{{
			"id":    fileID,
			"title": title,
		}},
		"channel_id": channel,
	}
	if initial := strings.TrimSpace(req.InitialComment); initial != "" {
		completeBody["initial_comment"] = initial
	}
	if threadTS := strings.TrimSpace(req.ThreadTS); threadTS != "" {
		completeBody["thread_ts"] = threadTS
	}
	completed, err := m.apiPostJSONWithIdentity(ctx, identity, "files.completeUploadExternal", completeBody)
	if err != nil {
		return nil, err
	}
	info, infoErr := m.apiCallWithIdentity(ctx, identity, "files.info", url.Values{"file": []string{fileID}})
	if infoErr != nil {
		info = map[string]any{"warning": infoErr.Error()}
	}
	return map[string]any{
		"ok":          true,
		"send_as":     identity,
		"file_id":     fileID,
		"file_name":   fileName,
		"title":       title,
		"channel_id":  channel,
		"thread_ts":   nilIfEmpty(strings.TrimSpace(req.ThreadTS)),
		"source":      nilIfEmpty(strings.TrimSpace(req.Source)),
		"source_ref":  nilIfEmpty(strings.TrimSpace(req.SourceRef)),
		"sha256":      nilIfEmpty(strings.TrimSpace(req.SHA256)),
		"completed":   completed,
		"file":        info["file"],
		"file_info":   info,
		"download":    slackFileDownloadInfo(info),
		"readability": "Uploaded via Slack files API and shared into the requested conversation; Slack visibility follows the workspace, channel membership, and file policy.",
	}, nil
}

func (m *Manager) QueueApproval(input PendingApproval, operation string, args map[string]any) PendingApproval {
	m.approvalMu.Lock()
	defer m.approvalMu.Unlock()
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = fmt.Sprintf("slack-%d", time.Now().UnixNano())
	}
	createdAt := strings.TrimSpace(input.CreatedAt)
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	input.ID = id
	input.CreatedAt = createdAt
	if m.approvals == nil {
		m.approvals = map[string]approvalAction{}
	}
	m.approvals[id] = approvalAction{
		PendingApproval: input,
		Operation:       strings.TrimSpace(operation),
		Args:            cloneMap(args),
	}
	return input
}

func (m *Manager) ListApprovals() []PendingApproval {
	m.approvalMu.Lock()
	defer m.approvalMu.Unlock()
	out := make([]PendingApproval, 0, len(m.approvals))
	for _, approval := range m.approvals {
		out = append(out, approval.PendingApproval)
	}
	return out
}

func (m *Manager) RunApprovedOperation(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
	operation = strings.TrimSpace(operation)
	switch operation {
	case "slack_post_message_execute":
		var req PostMessageRequest
		if err := decodeMap(cloneMap(args), &req); err != nil {
			return nil, err
		}
		return m.PostMessage(ctx, req)
	case "slack_upload_file_execute":
		var req UploadFileRequest
		if err := decodeMap(cloneMap(args), &req); err != nil {
			return nil, err
		}
		return m.UploadFile(ctx, req)
	default:
		return nil, fmt.Errorf("unknown Slack approval operation: %s", operation)
	}
}

func (m *Manager) Approve(ctx context.Context, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	m.approvalMu.Lock()
	action, ok := m.approvals[id]
	m.approvalMu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown approval id: %s", id)
	}
	result, err := m.RunApprovedOperation(ctx, action.Operation, action.Args)
	if err != nil {
		return nil, err
	}
	m.approvalMu.Lock()
	delete(m.approvals, id)
	m.approvalMu.Unlock()
	return map[string]any{
		"approval": action.PendingApproval,
		"result":   result,
	}, nil
}

func (m *Manager) Reject(id string) (PendingApproval, error) {
	id = strings.TrimSpace(id)
	m.approvalMu.Lock()
	defer m.approvalMu.Unlock()
	action, ok := m.approvals[id]
	if !ok {
		return PendingApproval{}, fmt.Errorf("unknown approval id: %s", id)
	}
	delete(m.approvals, id)
	return action.PendingApproval, nil
}

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	status, err := m.Status(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if redirectURI, uriErr := oauthCallbackURI(r); uriErr == nil {
		status.PKCERedirectURI = redirectURI
		status.BotOAuthAvailable, status.BotOAuthBlocker = slackBotOAuthAvailability(redirectURI)
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": status})
}

func (m *Manager) handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	result, err := m.Check(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error(), "status": result.Status})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (m *Manager) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if m.Secrets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "secrets store is not configured"})
		return
	}
	if err := m.Secrets.ClearSlackOAuth(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hint":  "Open Settings and unlock the local secrets store before disconnecting Slack.",
		})
		return
	}
	status, _ := m.Status(r.Context())
	m.applySlackHealth(&status, false, "Signed out of Slack.", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": status})
}

func (m *Manager) handleApprovals(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/integrations/slack/approvals"), "/")
	if path == "" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"approvals": m.ListApprovals()})
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	id, action := parts[0], parts[1]
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch action {
	case "approve":
		result, err := m.Approve(r.Context(), id)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result, "approvals": m.ListApprovals()})
	case "reject":
		approval, err := m.Reject(id)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approval": approval, "approvals": m.ListApprovals()})
	default:
		http.NotFound(w, r)
	}
}

func (m *Manager) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := m.requireUnlockedSecrets(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":    false,
			"error": err.Error(),
			"hint":  "Open Settings and unlock the local secrets store before connecting Slack.",
		})
		return
	}
	clientID := strings.TrimSpace(m.slackOAuthClientID())
	if clientID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": "Slack OAuth client ID is not configured",
			"hint":  "Create a Slack app with PKCE enabled, then set only the public Client ID in Settings.",
		})
		return
	}
	if strings.ContainsAny(clientID, " \t\r\n") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Slack OAuth client ID must not contain whitespace"})
		return
	}
	redirectURI, err := oauthCallbackURI(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	botOAuthAvailable, botOAuthBlocker := slackBotOAuthAvailability(redirectURI)
	state, err := randomURLSafe(32)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Failed to start OAuth flow"})
		return
	}
	verifier, err := randomURLSafe(64)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "Failed to start OAuth flow"})
		return
	}
	q := url.Values{}
	q.Set("client_id", clientID)
	if botOAuthAvailable {
		q.Set("scope", strings.Join(defaultBotScopes, ","))
	}
	q.Set("user_scope", strings.Join(defaultUserScopes, ","))
	q.Set("redirect_uri", redirectURI)
	q.Set("state", state)
	q.Set("code_challenge", pkceCodeChallenge(verifier))
	q.Set("code_challenge_method", "S256")

	now := time.Now()
	m.storeOAuthFlow(&oauthPendingFlow{
		State:        state,
		ClientID:     clientID,
		RedirectURI:  redirectURI,
		CodeVerifier: verifier,
		RequestedBot: botOAuthAvailable,
		CreatedAt:    now,
		ExpiresAt:    now.Add(oauthFlowTTL),
	})

	requestedBotScopes := []string{}
	if botOAuthAvailable {
		requestedBotScopes = defaultBotScopes
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                   true,
		"authorize_url":        authorizeURL + "?" + q.Encode(),
		"state":                state,
		"redirect_uri":         redirectURI,
		"requested_scopes":     defaultUserScopes,
		"requested_bot_scopes": requestedBotScopes,
		"bot_oauth_available":  botOAuthAvailable,
		"bot_oauth_blocker":    nilIfEmpty(botOAuthBlocker),
		"expires_in_seconds":   int(oauthFlowTTL.Seconds()),
	})
}

func (m *Manager) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		m.writeOAuthCallbackPage(w, "", oauthFlowResult{OK: false, Error: "Missing OAuth state"})
		return
	}
	flow, ok := m.loadOAuthFlow(state)
	if !ok {
		m.writeOAuthCallbackPage(w, state, oauthFlowResult{
			OK:    false,
			Error: "OAuth session not found or expired. Start the flow again from Settings.",
		})
		return
	}
	if errCode := strings.TrimSpace(r.URL.Query().Get("error")); errCode != "" {
		msg := strings.TrimSpace(r.URL.Query().Get("error_description"))
		if msg == "" {
			msg = errCode
		}
		res := oauthFlowResult{OK: false, Error: msg, CompletedAt: time.Now()}
		m.setOAuthFlowResult(state, res)
		m.writeOAuthCallbackPage(w, state, res)
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		res := oauthFlowResult{OK: false, Error: "Missing authorization code", CompletedAt: time.Now()}
		m.setOAuthFlowResult(state, res)
		m.writeOAuthCallbackPage(w, state, res)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res := m.completeOAuthFlowWithCode(ctx, flow, code)
	if !res.OK {
		m.persistSlackHealth(false, res.Error, false)
	}
	m.setOAuthFlowResult(state, res)
	m.writeOAuthCallbackPage(w, state, res)
}

func (m *Manager) handleOAuthResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "Missing OAuth state"})
		return
	}
	flow, ok := m.loadOAuthFlow(state)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "OAuth session not found or expired"})
		return
	}
	if flow.Result == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending", "ok": false})
		return
	}
	status := "error"
	if flow.Result.OK {
		status = "success"
	}
	current, _ := m.Status(r.Context())
	if strings.TrimSpace(flow.RedirectURI) != "" {
		current.PKCERedirectURI = flow.RedirectURI
		current.BotOAuthAvailable, current.BotOAuthBlocker = slackBotOAuthAvailability(flow.RedirectURI)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":       status,
		"ok":           flow.Result.OK,
		"message":      nilIfEmpty(strings.TrimSpace(flow.Result.Message)),
		"error":        nilIfEmpty(strings.TrimSpace(flow.Result.Error)),
		"slack_status": current,
	})
}

func (m *Manager) completeOAuthFlowWithCode(ctx context.Context, flow *oauthPendingFlow, code string) oauthFlowResult {
	if flow == nil {
		return oauthFlowResult{OK: false, Error: "OAuth session not found or expired. Start the flow again from Settings.", CompletedAt: time.Now()}
	}
	resp, err := m.exchangeAuthorizationCode(ctx, flow, code)
	if err != nil {
		return oauthFlowResult{OK: false, Error: err.Error(), CompletedAt: time.Now()}
	}
	sec, err := slackSecretsFromOAuthResponse(resp, secrets.SlackSecrets{}, "")
	if err != nil {
		return oauthFlowResult{OK: false, Error: err.Error(), CompletedAt: time.Now()}
	}
	if strings.TrimSpace(sec.AccessToken) == "" {
		return oauthFlowResult{
			OK:          false,
			Error:       "Slack OAuth did not initialize the user profile. Reconnect after confirming the Slack app has user scopes enabled.",
			CompletedAt: time.Now(),
		}
	}
	if flow.RequestedBot && strings.TrimSpace(sec.BotAccessToken) == "" {
		return oauthFlowResult{
			OK:          false,
			Error:       "Slack OAuth did not initialize the bot profile. Reconnect after confirming the Slack app has bot scopes enabled and the redirect URI is a web redirect that Slack allows for bot scopes.",
			CompletedAt: time.Now(),
		}
	}
	if m.Secrets == nil {
		return oauthFlowResult{OK: false, Error: "Slack OAuth credential save failed: secrets store is unavailable", CompletedAt: time.Now()}
	}
	if err := m.Secrets.SetSlackOAuth(sec); err != nil {
		return oauthFlowResult{OK: false, Error: fmt.Sprintf("Slack OAuth credential save failed: %v", err), CompletedAt: time.Now()}
	}
	if !flow.RequestedBot {
		m.persistSlackHealth(true, "Slack OAuth user profile saved.", false)
		return oauthFlowResult{OK: true, Message: "Slack OAuth user profile saved.", CompletedAt: time.Now()}
	}
	m.persistSlackHealth(true, "Slack OAuth connection saved.", false)
	return oauthFlowResult{OK: true, Message: "Slack OAuth connection saved.", CompletedAt: time.Now()}
}

func (m *Manager) exchangeAuthorizationCode(ctx context.Context, flow *oauthPendingFlow, code string) (slackOAuthAccessResponse, error) {
	pkceForm := url.Values{}
	pkceForm.Set("client_id", strings.TrimSpace(flow.ClientID))
	pkceForm.Set("code", strings.TrimSpace(code))
	pkceForm.Set("redirect_uri", strings.TrimSpace(flow.RedirectURI))
	pkceForm.Set("code_verifier", strings.TrimSpace(flow.CodeVerifier))
	resp, err := m.oauthAccess(ctx, cloneValues(pkceForm), "")
	if err == nil {
		return resp, nil
	}
	if !isBadClientSecretError(err) {
		return slackOAuthAccessResponse{}, err
	}

	confidentialForm := cloneValues(pkceForm)
	clientSecret := m.slackClientSecret()
	if clientSecret != "" {
		resp, secretErr := m.oauthAccess(ctx, confidentialForm, clientSecret)
		if secretErr == nil {
			return resp, nil
		}
		if isBadClientSecretError(secretErr) {
			return slackOAuthAccessResponse{}, errors.New("bad_client_secret: Slack rejected the saved OAuth Client Secret. Prefer clearing it and enabling PKCE with the localhost redirect URI, or re-save the correct Client Secret under Advanced OAuth app credential.")
		}
		return slackOAuthAccessResponse{}, secretErr
	}

	return slackOAuthAccessResponse{}, errors.New("bad_client_secret: the local request used localhost PKCE without a client_secret, but Slack treated this Client ID as a confidential OAuth app. Enable PKCE in Slack OAuth & Permissions for this exact Slack app, register the localhost redirect URI, and start Connect again.")
}

func (m *Manager) refreshSlackToken(ctx context.Context, current secrets.SlackSecrets, identity string) (secrets.SlackSecrets, error) {
	clientID := strings.TrimSpace(m.slackOAuthClientID())
	if clientID == "" {
		return secrets.SlackSecrets{}, errors.New("Slack OAuth client ID is required to refresh OAuth credentials")
	}
	identity = normalizeIdentity(identity)
	refreshToken := strings.TrimSpace(current.RefreshToken)
	if identity == IdentityBot {
		refreshToken = strings.TrimSpace(current.BotRefreshToken)
	}
	if refreshToken == "" {
		return current, nil
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	clientSecret := strings.TrimSpace(current.ClientSecret)
	if clientSecret == "" {
		clientSecret = m.slackClientSecret()
	}
	resp, err := m.oauthAccess(ctx, cloneValues(form), "")
	if err != nil && isBadClientSecretError(err) && clientSecret != "" {
		resp, err = m.oauthAccess(ctx, cloneValues(form), clientSecret)
		if err != nil && isBadClientSecretError(err) {
			return secrets.SlackSecrets{}, errors.New("bad_client_secret: Slack rejected the saved OAuth Client Secret during token refresh. Clear it for PKCE mode, or re-save the correct Client Secret.")
		}
	}
	if err != nil {
		return secrets.SlackSecrets{}, err
	}
	next, err := slackSecretsFromOAuthResponse(resp, current, identity)
	if err != nil {
		return secrets.SlackSecrets{}, err
	}
	if m.Secrets == nil {
		return secrets.SlackSecrets{}, errors.New("secrets store is not configured")
	}
	if err := m.Secrets.SetSlackOAuth(next); err != nil {
		return secrets.SlackSecrets{}, err
	}
	return next, nil
}

func (m *Manager) oauthAccess(ctx context.Context, form url.Values, clientSecret string) (slackOAuthAccessResponse, error) {
	clientID := strings.TrimSpace(form.Get("client_id"))
	secret := strings.TrimSpace(clientSecret)
	if clientID != "" && secret != "" {
		form.Del("client_id")
		form.Del("client_secret")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBaseURL+"/oauth.v2.access", strings.NewReader(form.Encode()))
	if err != nil {
		return slackOAuthAccessResponse{}, err
	}
	if clientID != "" && secret != "" {
		req.SetBasicAuth(clientID, secret)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := m.client().Do(req)
	if err != nil {
		return slackOAuthAccessResponse{}, fmt.Errorf("failed to reach Slack OAuth endpoint: %w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out slackOAuthAccessResponse
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !out.OK {
		msg := strings.TrimSpace(out.Error)
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		if msg == "" {
			msg = fmt.Sprintf("Slack OAuth endpoint error (HTTP %d)", resp.StatusCode)
		}
		return slackOAuthAccessResponse{}, errors.New(msg)
	}
	return out, nil
}

func (m *Manager) ResolveWriteIdentity(sendAs string) string {
	raw := strings.TrimSpace(sendAs)
	if raw != "" {
		return normalizeIdentity(raw)
	}
	return m.configuredWriteIdentity()
}

func (m *Manager) configuredWriteIdentity() string {
	if m == nil || m.Settings == nil {
		return IdentityUser
	}
	s := m.Settings.Get()
	if s.Slack == nil || s.Slack.WriteIdentity == nil {
		return IdentityUser
	}
	return normalizeIdentity(*s.Slack.WriteIdentity)
}

func normalizeIdentity(raw string) string {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case IdentityBot, "app", "slack_bot":
		return IdentityBot
	default:
		return IdentityUser
	}
}

func looksLikeBotToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(token)), "xoxb-")
}

func (m *Manager) apiCall(ctx context.Context, method string, params url.Values) (map[string]any, error) {
	return m.apiCallWithIdentity(ctx, IdentityUser, method, params)
}

func (m *Manager) apiCallWithIdentity(ctx context.Context, identity string, method string, params url.Values) (map[string]any, error) {
	token, err := m.accessTokenFor(ctx, identity)
	if err != nil {
		return nil, err
	}
	u := apiBaseURL + "/" + strings.TrimLeft(strings.TrimSpace(method), "/")
	if params != nil && len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("Slack API response was not JSON (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retry := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if retry != "" {
			return nil, fmt.Errorf("Slack API rate limited; retry after %s seconds", retry)
		}
		return nil, errors.New("Slack API rate limited")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !truthy(out["ok"]) {
		msg := strings.TrimSpace(stringValue(out["error"]))
		if msg == "" {
			msg = fmt.Sprintf("Slack API error (HTTP %d)", resp.StatusCode)
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

func (m *Manager) apiPostJSON(ctx context.Context, method string, body map[string]any) (map[string]any, error) {
	return m.apiPostJSONWithIdentity(ctx, IdentityUser, method, body)
}

func (m *Manager) apiPostJSONWithIdentity(ctx context.Context, identity string, method string, body map[string]any) (map[string]any, error) {
	token, err := m.accessTokenFor(ctx, identity)
	if err != nil {
		return nil, err
	}
	u := apiBaseURL + "/" + strings.TrimLeft(strings.TrimSpace(method), "/")
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("Slack API response was not JSON (HTTP %d)", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		retry := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if retry != "" {
			return nil, fmt.Errorf("Slack API rate limited; retry after %s seconds", retry)
		}
		return nil, errors.New("Slack API rate limited")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || !truthy(out["ok"]) {
		msg := strings.TrimSpace(stringValue(out["error"]))
		if msg == "missing_scope" {
			msg = "missing_scope: reconnect Slack from Settings so OAuth grants the newly required write scopes"
		}
		if msg == "" {
			msg = fmt.Sprintf("Slack API error (HTTP %d)", resp.StatusCode)
		}
		return nil, errors.New(msg)
	}
	return out, nil
}

func (m *Manager) downloadSlackPrivateURL(ctx context.Context, token string, targetURL string, maxBytes int64) ([]byte, string, error) {
	targetURL = strings.TrimSpace(targetURL)
	if targetURL == "" {
		return nil, "", errors.New("Slack private download URL is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("User-Agent", defaultUserAgent)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		retry := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if retry != "" {
			return nil, "", fmt.Errorf("Slack file download rate limited; retry after %s seconds", retry)
		}
		return nil, "", errors.New("Slack file download rate limited")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = fmt.Sprintf("Slack file download failed (HTTP %d)", resp.StatusCode)
		}
		return nil, "", errors.New(msg)
	}
	if maxBytes <= 0 {
		maxBytes = maxManagedDownloadBytes
	}
	if resp.ContentLength > maxBytes {
		return nil, "", fmt.Errorf("Slack file exceeds managed download cap of %d bytes", maxBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(raw)) > maxBytes {
		return nil, "", fmt.Errorf("Slack file exceeds managed download cap of %d bytes", maxBytes)
	}
	return raw, strings.TrimSpace(resp.Header.Get("Content-Type")), nil
}

func writeSlackManagedDownload(appConfigDir string, fileID string, fileName string, mimeType string, raw []byte) (map[string]any, error) {
	baseAbs, err := filepath.Abs(strings.TrimSpace(appConfigDir))
	if err != nil {
		return nil, err
	}
	fileID = sanitizeSlackPathSegment(fileID)
	if fileID == "" {
		fileID = "file"
	}
	fileName = sanitizeSlackDownloadFileName(fileName)
	dir := filepath.Join(baseAbs, "slack", "downloads", fileID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	target := filepath.Join(dir, fileName)
	tmp, err := os.CreateTemp(dir, fileName+".tmp-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := tmp.Write(raw); err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return nil, err
	}
	keep = true
	_ = os.Chmod(target, 0o600)
	rel, err := filepath.Rel(baseAbs, target)
	if err != nil {
		return nil, err
	}
	relSlash := filepath.ToSlash(rel)
	sum := sha256.Sum256(raw)
	shaHex := fmt.Sprintf("%x", sum[:])
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return map[string]any{
		"local_path":          relSlash,
		"localPath":           relSlash,
		"local_relative_path": relSlash,
		"localRelativePath":   relSlash,
		"directory":           dir,
		"file_name":           fileName,
		"fileName":            fileName,
		"mime_type":           mimeType,
		"mimeType":            mimeType,
		"size_bytes":          len(raw),
		"sizeBytes":           len(raw),
		"sha256":              shaHex,
		"source":              "slack_managed_download",
		"file_id":             fileID,
	}, nil
}

func sanitizeSlackDownloadFileName(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.Map(func(r rune) rune {
		switch r {
		case 0, '/', '\\', ':':
			return -1
		default:
			return r
		}
	}, name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		name = "download"
	}
	if len(name) > 180 {
		ext := path.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		if len(ext) > 24 {
			ext = ""
		}
		limit := 180 - len(ext)
		if limit < 1 {
			limit = 180
		}
		if len(stem) > limit {
			stem = stem[:limit]
		}
		name = stem + ext
	}
	return name
}

func sanitizeSlackPathSegment(raw string) string {
	segment := sanitizeSlackDownloadFileName(raw)
	segment = strings.ReplaceAll(segment, ".", "_")
	segment = strings.Trim(segment, "_- ")
	if segment == "" {
		return "file"
	}
	return segment
}

func clampInt64(value int64, fallback int64, maxValue int64) int64 {
	if value <= 0 {
		return fallback
	}
	if maxValue > 0 && value > maxValue {
		return maxValue
	}
	return value
}

func (m *Manager) uploadRawFile(ctx context.Context, uploadURL string, mimeType string, raw []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	contentType := strings.TrimSpace(mimeType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	req.Header.Set("User-Agent", defaultUserAgent)
	req.ContentLength = int64(len(raw))
	resp, err := m.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = fmt.Sprintf("Slack file upload endpoint error (HTTP %d)", resp.StatusCode)
		}
		return errors.New(msg)
	}
	return nil
}

func (m *Manager) requireScopes(ctx context.Context, scopes ...string) error {
	return m.requireScopesFor(ctx, IdentityUser, scopes...)
}

func (m *Manager) requireScopesFor(ctx context.Context, identity string, scopes ...string) error {
	if len(scopes) == 0 {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if m.Secrets == nil {
		return errors.New("Slack secrets store is not configured")
	}
	sec, err := m.Secrets.GetSlack()
	if err != nil {
		return err
	}
	granted := map[string]struct{}{}
	scopeText := sec.Scope
	if normalizeIdentity(identity) == IdentityBot {
		scopeText = sec.BotScope
	}
	for _, scope := range splitScopes(scopeText) {
		granted[strings.ToLower(strings.TrimSpace(scope))] = struct{}{}
	}
	missing := make([]string, 0)
	for _, scope := range scopes {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "" {
			continue
		}
		if _, ok := granted[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("Slack %s OAuth profile is missing required scope(s): %s. Reconnect Slack from Settings to grant the new scopes.", normalizeIdentity(identity), strings.Join(missing, ", "))
	}
	return nil
}

func (m *Manager) accessToken(ctx context.Context) (string, error) {
	return m.accessTokenFor(ctx, IdentityUser)
}

func (m *Manager) accessTokenFor(ctx context.Context, identity string) (string, error) {
	if err := contextErr(ctx); err != nil {
		return "", err
	}
	identity = normalizeIdentity(identity)
	if m.Secrets == nil {
		return "", errors.New("Slack secrets store is not configured")
	}
	sec, err := m.Secrets.GetSlack()
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(sec.AccessToken)
	refreshToken := strings.TrimSpace(sec.RefreshToken)
	expiresAt := sec.ExpiresAt
	if identity == IdentityBot {
		token = strings.TrimSpace(sec.BotAccessToken)
		refreshToken = strings.TrimSpace(sec.BotRefreshToken)
		expiresAt = sec.BotExpiresAt
	}
	if token == "" {
		return "", fmt.Errorf("Slack %s OAuth profile is not configured", identity)
	}
	if err := m.ensureTeamAllowed(sec); err != nil {
		return "", err
	}
	if !needsRefreshAt(expiresAt, time.Now()) {
		return token, nil
	}
	if refreshToken == "" {
		return token, nil
	}
	m.tokenMu.Lock()
	defer m.tokenMu.Unlock()
	sec, err = m.Secrets.GetSlack()
	if err != nil {
		return "", err
	}
	token = strings.TrimSpace(sec.AccessToken)
	refreshToken = strings.TrimSpace(sec.RefreshToken)
	expiresAt = sec.ExpiresAt
	if identity == IdentityBot {
		token = strings.TrimSpace(sec.BotAccessToken)
		refreshToken = strings.TrimSpace(sec.BotRefreshToken)
		expiresAt = sec.BotExpiresAt
	}
	if token == "" {
		return "", fmt.Errorf("Slack %s OAuth profile is not configured", identity)
	}
	if err := m.ensureTeamAllowed(sec); err != nil {
		return "", err
	}
	if !needsRefreshAt(expiresAt, time.Now()) {
		return token, nil
	}
	if refreshToken == "" {
		return token, nil
	}
	next, err := m.refreshSlackToken(ctx, sec, identity)
	if err != nil {
		return "", err
	}
	if identity == IdentityBot {
		return strings.TrimSpace(next.BotAccessToken), nil
	}
	return strings.TrimSpace(next.AccessToken), nil
}

func (m *Manager) ensureTeamAllowed(sec secrets.SlackSecrets) error {
	allowed := m.allowedTeamIDs()
	if len(allowed) == 0 {
		return nil
	}
	teamID := strings.TrimSpace(strings.ToUpper(sec.TeamID))
	if teamID == "" {
		return errors.New("Slack workspace ID is unknown; reconnect Slack before using tools")
	}
	for _, id := range allowed {
		if strings.EqualFold(id, teamID) {
			return nil
		}
	}
	return fmt.Errorf("Slack workspace %s is not in the allowedTeamIds setting", teamID)
}

func (m *Manager) normalizeAllowedChannel(raw string) (string, error) {
	channel := strings.TrimSpace(raw)
	if channel == "" {
		return "", errors.New("channel_id is required")
	}
	allowed := m.allowedChannelIDs()
	if len(allowed) == 0 {
		return channel, nil
	}
	upper := strings.ToUpper(channel)
	for _, id := range allowed {
		if strings.EqualFold(id, upper) {
			return channel, nil
		}
	}
	return "", fmt.Errorf("Slack channel %s is not in the allowedChannelIds setting", channel)
}

func (m *Manager) filterConversationList(out map[string]any) map[string]any {
	allowed := m.allowedChannelIDs()
	if len(allowed) == 0 || out == nil {
		return out
	}
	raw, ok := out["channels"].([]any)
	if !ok {
		return out
	}
	allowedSet := map[string]struct{}{}
	for _, id := range allowed {
		allowedSet[strings.ToUpper(strings.TrimSpace(id))] = struct{}{}
	}
	filtered := make([]any, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := strings.ToUpper(strings.TrimSpace(stringValue(obj["id"])))
		if _, ok := allowedSet[id]; ok {
			filtered = append(filtered, item)
		}
	}
	out["channels"] = filtered
	out["allowed_channel_filter_applied"] = true
	return out
}

func (m *Manager) allowedTeamIDs() []string {
	if m.Settings == nil {
		return nil
	}
	s := m.Settings.Get()
	if s.Slack == nil {
		return nil
	}
	return append([]string(nil), s.Slack.AllowedTeamIDs...)
}

func (m *Manager) allowedChannelIDs() []string {
	if m.Settings == nil {
		return nil
	}
	s := m.Settings.Get()
	if s.Slack == nil {
		return nil
	}
	return append([]string(nil), s.Slack.AllowedChannelIDs...)
}

func (m *Manager) slackOAuthClientID() string {
	if m.Settings == nil {
		return ""
	}
	s := m.Settings.Get()
	if s.SlackOAuthClientID == nil {
		return ""
	}
	return strings.TrimSpace(*s.SlackOAuthClientID)
}

func (m *Manager) slackClientSecret() string {
	if m.Secrets == nil {
		return ""
	}
	sec, err := m.Secrets.GetSlack()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(sec.ClientSecret)
}

func (m *Manager) requireUnlockedSecrets() error {
	if m.Secrets == nil {
		return errors.New("secrets store is not configured")
	}
	initialized, locked, _ := m.Secrets.Status()
	if !initialized {
		return errors.New("secrets store is not initialized")
	}
	if locked {
		return errors.New("secrets store is locked")
	}
	return nil
}

func (m *Manager) client() *http.Client {
	if m != nil && m.httpc != nil {
		return m.httpc
	}
	return http.DefaultClient
}

func needsRefresh(sec secrets.SlackSecrets, now time.Time) bool {
	return needsRefreshAt(sec.ExpiresAt, now)
}

func needsRefreshAt(expiresAt int64, now time.Time) bool {
	if expiresAt <= 0 {
		return false
	}
	return now.Add(refreshSkew).Unix() >= expiresAt
}

type slackOAuthAccessResponse struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`
	BotUserID    string `json:"bot_user_id,omitempty"`
	BotID        string `json:"bot_id,omitempty"`
	Team         struct {
		Name string `json:"name,omitempty"`
		ID   string `json:"id,omitempty"`
	} `json:"team,omitempty"`
	Enterprise struct {
		Name string `json:"name,omitempty"`
		ID   string `json:"id,omitempty"`
	} `json:"enterprise,omitempty"`
	AuthedUser struct {
		ID           string `json:"id,omitempty"`
		Scope        string `json:"scope,omitempty"`
		AccessToken  string `json:"access_token,omitempty"`
		TokenType    string `json:"token_type,omitempty"`
		RefreshToken string `json:"refresh_token,omitempty"`
		ExpiresIn    int64  `json:"expires_in,omitempty"`
	} `json:"authed_user,omitempty"`
}

func slackSecretsFromOAuthResponse(resp slackOAuthAccessResponse, current secrets.SlackSecrets, forcedIdentity string) (secrets.SlackSecrets, error) {
	now := time.Now()
	out := current
	out.ClientSecret = strings.TrimSpace(out.ClientSecret)
	out.AccessToken = strings.TrimSpace(out.AccessToken)
	out.RefreshToken = strings.TrimSpace(out.RefreshToken)
	out.TokenType = strings.TrimSpace(out.TokenType)
	out.Scope = strings.TrimSpace(out.Scope)
	out.TeamID = strings.TrimSpace(out.TeamID)
	out.TeamName = strings.TrimSpace(out.TeamName)
	out.EnterpriseID = strings.TrimSpace(out.EnterpriseID)
	out.AuthedUserID = strings.TrimSpace(out.AuthedUserID)
	out.BotAccessToken = strings.TrimSpace(out.BotAccessToken)
	out.BotRefreshToken = strings.TrimSpace(out.BotRefreshToken)
	out.BotTokenType = strings.TrimSpace(out.BotTokenType)
	out.BotScope = strings.TrimSpace(out.BotScope)
	out.BotUserID = strings.TrimSpace(out.BotUserID)
	out.BotID = strings.TrimSpace(out.BotID)

	userAccess := strings.TrimSpace(resp.AuthedUser.AccessToken)
	userRefresh := strings.TrimSpace(resp.AuthedUser.RefreshToken)
	userScope := strings.TrimSpace(resp.AuthedUser.Scope)
	userTokenType := strings.TrimSpace(resp.AuthedUser.TokenType)
	userExpiresIn := resp.AuthedUser.ExpiresIn
	authedUserID := strings.TrimSpace(resp.AuthedUser.ID)

	topAccess := strings.TrimSpace(resp.AccessToken)
	topRefresh := strings.TrimSpace(resp.RefreshToken)
	topScope := strings.TrimSpace(resp.Scope)
	topTokenType := strings.TrimSpace(resp.TokenType)
	topExpiresIn := resp.ExpiresIn

	forcedIdentity = strings.TrimSpace(strings.ToLower(forcedIdentity))
	if forcedIdentity != IdentityBot && forcedIdentity != IdentityUser {
		forcedIdentity = ""
	}
	if topAccess != "" {
		switch {
		case forcedIdentity == IdentityBot:
			out.BotAccessToken = topAccess
			out.BotRefreshToken = firstNonEmpty(topRefresh, out.BotRefreshToken)
			out.BotScope = firstNonEmpty(topScope, out.BotScope)
			out.BotTokenType = firstNonEmpty(topTokenType, "bearer")
			if topExpiresIn > 0 {
				out.BotExpiresAt = now.Add(time.Duration(topExpiresIn) * time.Second).Unix()
			}
		case forcedIdentity == IdentityUser:
			userAccess = topAccess
			userRefresh = firstNonEmpty(topRefresh, userRefresh)
			userScope = firstNonEmpty(topScope, userScope)
			userTokenType = firstNonEmpty(topTokenType, userTokenType)
			userExpiresIn = topExpiresIn
		case userAccess != "" || looksLikeBotToken(topAccess):
			out.BotAccessToken = topAccess
			out.BotRefreshToken = firstNonEmpty(topRefresh, out.BotRefreshToken)
			out.BotScope = firstNonEmpty(topScope, out.BotScope)
			out.BotTokenType = firstNonEmpty(topTokenType, "bearer")
			if topExpiresIn > 0 {
				out.BotExpiresAt = now.Add(time.Duration(topExpiresIn) * time.Second).Unix()
			}
		default:
			userAccess = topAccess
			userRefresh = firstNonEmpty(topRefresh, userRefresh)
			userScope = firstNonEmpty(topScope, userScope)
			userTokenType = firstNonEmpty(topTokenType, userTokenType)
			userExpiresIn = topExpiresIn
		}
	}

	if userAccess != "" {
		out.AccessToken = userAccess
		out.RefreshToken = firstNonEmpty(userRefresh, out.RefreshToken)
		out.Scope = firstNonEmpty(userScope, out.Scope)
		out.TokenType = firstNonEmpty(userTokenType, "bearer")
		if userExpiresIn > 0 {
			out.ExpiresAt = now.Add(time.Duration(userExpiresIn) * time.Second).Unix()
		}
	}
	if authedUserID != "" {
		out.AuthedUserID = authedUserID
	}
	if botUserID := strings.TrimSpace(resp.BotUserID); botUserID != "" {
		out.BotUserID = botUserID
	}
	if botID := strings.TrimSpace(resp.BotID); botID != "" {
		out.BotID = botID
	}
	if out.AccessToken == "" && out.BotAccessToken == "" {
		return secrets.SlackSecrets{}, errors.New("Slack OAuth did not return an access token")
	}
	out.TeamID = strings.TrimSpace(firstNonEmpty(resp.Team.ID, out.TeamID))
	out.TeamName = strings.TrimSpace(firstNonEmpty(resp.Team.Name, out.TeamName))
	out.EnterpriseID = strings.TrimSpace(firstNonEmpty(resp.Enterprise.ID, out.EnterpriseID))
	out.InstalledAt = current.InstalledAt
	out.LastRefreshed = now.Unix()
	return out, nil
}

func oauthCallbackURI(r *http.Request) (string, error) {
	origin, err := requestOrigin(r)
	if err != nil {
		return "", err
	}
	origin = normalizeLoopbackOriginForSlackPKCE(origin)
	return origin + "/api/integrations/slack/oauth/callback", nil
}

func slackBotOAuthAvailability(redirectURI string) (bool, string) {
	if isSlackDesktopRedirect(redirectURI) {
		return false, "Slack does not allow bot scopes with localhost PKCE redirects. The local OAuth flow can initialize the user profile; send-as-bot requires a Slack OAuth web redirect that is not localhost."
	}
	return true, ""
}

func isSlackDesktopRedirect(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return true
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func normalizeLoopbackOriginForSlackPKCE(origin string) string {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || u == nil {
		return origin
	}
	switch strings.ToLower(strings.TrimSpace(u.Hostname())) {
	case "127.0.0.1", "::1":
		if port := strings.TrimSpace(u.Port()); port != "" {
			u.Host = "localhost:" + port
		} else {
			u.Host = "localhost"
		}
	case "localhost":
		if port := strings.TrimSpace(u.Port()); port != "" {
			u.Host = "localhost:" + port
		} else {
			u.Host = "localhost"
		}
	default:
		return origin
	}
	return strings.TrimRight(u.String(), "/")
}

func requestOrigin(r *http.Request) (string, error) {
	scheme := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))
	if strings.Contains(scheme, ",") {
		scheme = strings.TrimSpace(strings.SplitN(scheme, ",", 2)[0])
	}
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	scheme = strings.ToLower(scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("invalid request scheme for OAuth callback URL")
	}
	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if strings.Contains(host, ",") {
		host = strings.TrimSpace(strings.SplitN(host, ",", 2)[0])
	}
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return "", errors.New("unable to determine callback host")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return "", errors.New("invalid callback host")
	}
	return scheme + "://" + host, nil
}

func randomURLSafe(n int) (string, error) {
	if n <= 0 {
		n = 32
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func pkceCodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (m *Manager) storeOAuthFlow(flow *oauthPendingFlow) {
	if flow == nil || strings.TrimSpace(flow.State) == "" {
		return
	}
	m.oauthMu.Lock()
	defer m.oauthMu.Unlock()
	if m.oauthFlows == nil {
		m.oauthFlows = map[string]*oauthPendingFlow{}
	}
	m.cleanupOAuthFlowsLocked(time.Now())
	m.oauthFlows[strings.TrimSpace(flow.State)] = flow
}

func (m *Manager) loadOAuthFlow(state string) (*oauthPendingFlow, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil, false
	}
	m.oauthMu.Lock()
	defer m.oauthMu.Unlock()
	m.cleanupOAuthFlowsLocked(time.Now())
	flow, ok := m.oauthFlows[state]
	if !ok || flow == nil {
		return nil, false
	}
	copyFlow := *flow
	if flow.Result != nil {
		res := *flow.Result
		copyFlow.Result = &res
	}
	return &copyFlow, true
}

func (m *Manager) setOAuthFlowResult(state string, result oauthFlowResult) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	if result.CompletedAt.IsZero() {
		result.CompletedAt = time.Now()
	}
	m.oauthMu.Lock()
	defer m.oauthMu.Unlock()
	m.cleanupOAuthFlowsLocked(time.Now())
	flow, ok := m.oauthFlows[state]
	if !ok || flow == nil {
		return
	}
	res := result
	flow.Result = &res
}

func (m *Manager) cleanupOAuthFlowsLocked(now time.Time) {
	for state, flow := range m.oauthFlows {
		if flow == nil || now.After(flow.ExpiresAt) {
			delete(m.oauthFlows, state)
			continue
		}
		if flow.Result != nil && !flow.Result.CompletedAt.IsZero() && now.Sub(flow.Result.CompletedAt) > oauthResultTTL {
			delete(m.oauthFlows, state)
		}
	}
}

func (m *Manager) writeOAuthCallbackPage(w http.ResponseWriter, state string, result oauthFlowResult) {
	status := "Slack connection failed"
	message := strings.TrimSpace(result.Error)
	if result.OK {
		status = "Slack connected"
		message = strings.TrimSpace(result.Message)
		if message == "" {
			message = "You can close this window and return to Settings."
		}
	} else if message == "" {
		message = "Slack OAuth did not complete."
	}
	payload, _ := json.Marshal(map[string]any{
		"type":    "slack-oauth-result",
		"state":   strings.TrimSpace(state),
		"ok":      result.OK,
		"message": message,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width,initial-scale=1" />
  <title>%s</title>
  <style>
    body { font-family: system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f8fafc; color: #111827; }
    main { max-width: 520px; padding: 32px; }
    h1 { font-size: 22px; margin: 0 0 12px; }
    p { line-height: 1.5; color: #4b5563; }
    button { border: 1px solid #d1d5db; border-radius: 6px; background: white; padding: 8px 12px; cursor: pointer; }
  </style>
</head>
<body>
  <main>
    <h1>%s</h1>
    <p>%s</p>
    <button onclick="window.close()">Close</button>
  </main>
  <script>
    try {
      if (window.opener) {
        window.opener.postMessage(%s, window.location.origin);
      }
    } catch (e) {}
  </script>
</body>
</html>`, html.EscapeString(status), html.EscapeString(status), html.EscapeString(message), string(payload))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func clampInt(value int, fallback int, max int) int {
	if value <= 0 {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func setOptional(params url.Values, key string, value string) {
	if strings.TrimSpace(value) != "" {
		params.Set(key, strings.TrimSpace(value))
	}
}

func cloneValues(values url.Values) url.Values {
	out := url.Values{}
	for key, items := range values {
		for _, item := range items {
			out.Add(key, item)
		}
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		switch typed := value.(type) {
		case []string:
			out[key] = append([]string(nil), typed...)
		case []any:
			out[key] = append([]any(nil), typed...)
		case map[string]any:
			out[key] = cloneMap(typed)
		default:
			out[key] = value
		}
	}
	return out
}

func decodeMap(in map[string]any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func isBadClientSecretError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "bad_client_secret")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitScopes(scope string) []string {
	raw := strings.FieldsFunc(scope, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' })
	out := []string{}
	seen := map[string]struct{}{}
	for _, item := range raw {
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

func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

func stringValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

func mapValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

func slackFileDownloadInfo(info map[string]any) map[string]any {
	file := mapValue(info["file"])
	if len(file) == 0 {
		return nil
	}
	out := map[string]any{}
	for _, key := range []string{"id", "name", "title", "mimetype", "filetype", "url_private", "url_private_download", "permalink", "permalink_public"} {
		if value := strings.TrimSpace(stringValue(file[key])); value != "" {
			out[key] = value
		}
	}
	if size := stringValue(file["size"]); strings.TrimSpace(size) != "" {
		out["size"] = size
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneInt64Ptr(src *int64) *int64 {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}

func cloneBoolPtr(src *bool) *bool {
	if src == nil {
		return nil
	}
	v := *src
	return &v
}
