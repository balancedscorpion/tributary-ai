package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/config"
)

const (
	githubDeviceCodeURL  = "https://github.com/login/device/code"
	githubAccessTokenURL = "https://github.com/login/oauth/access_token"
	githubUserAPIURL     = "https://api.github.com/user"
	githubOAuthScope     = "repo read:org read:user"
	// GitHub OAuth client IDs are public identifiers. Product builds can set
	// this to hide OAuth setup from end users while still allowing local
	// settings to override it.
	defaultGitHubOAuthClientID = ""
)

type githubDeviceSession struct {
	DeviceCode string
	ExpiresAt  time.Time
	Interval   time.Duration
	LastPoll   time.Time
}

type githubDeviceSessionStore struct {
	mu       sync.Mutex
	sessions map[string]githubDeviceSession
}

func newGitHubDeviceSessionStore() *githubDeviceSessionStore {
	return &githubDeviceSessionStore{sessions: map[string]githubDeviceSession{}}
}

func (s *githubDeviceSessionStore) set(state string, session githubDeviceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[state] = session
}

func (s *githubDeviceSessionStore) get(state string) (githubDeviceSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[state]
	if ok && time.Now().After(session.ExpiresAt) {
		delete(s.sessions, state)
		return githubDeviceSession{}, false
	}
	return session, ok
}

func (s *githubDeviceSessionStore) update(state string, fn func(githubDeviceSession) githubDeviceSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[state]
	if !ok {
		return
	}
	if time.Now().After(session.ExpiresAt) {
		delete(s.sessions, state)
		return
	}
	s.sessions[state] = fn(session)
}

func (s *githubDeviceSessionStore) delete(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, state)
}

type githubAuthStatus struct {
	Configured         bool           `json:"configured"`
	ClientIDConfigured bool           `json:"client_id_configured"`
	ClientID           string         `json:"client_id,omitempty"`
	SecretsInitialized bool           `json:"secrets_initialized"`
	SecretsLocked      bool           `json:"secrets_locked"`
	Connected          bool           `json:"connected"`
	Login              string         `json:"login,omitempty"`
	Scope              string         `json:"scope,omitempty"`
	Profile            gitProfileInfo `json:"profile"`
	LastCheckedAt      *int64         `json:"lastCheckedAt,omitempty"`
	LastCheckOk        *bool          `json:"lastCheckOk,omitempty"`
	LastCheckMessage   string         `json:"lastCheckMessage,omitempty"`
}

func (h *Handler) handleGitHubAuthStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": h.githubAuthStatus(r.Context())})
}

func (h *Handler) handleSetGitHubOAuthClientID(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "settings store is not configured"})
		return
	}
	var body struct {
		ClientID string `json:"client_id"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	next := h.settings.Get()
	if trimmed := strings.TrimSpace(body.ClientID); trimmed != "" {
		next.GitHubOAuthClientID = &trimmed
	} else {
		next.GitHubOAuthClientID = nil
	}
	if err := h.settings.Save(next); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": h.githubAuthStatus(r.Context())})
}

func (h *Handler) handleStartGitHubAuth(w http.ResponseWriter, r *http.Request) {
	if err := h.requireUnlockedSecretsForGitHub(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"hint":  "Open Settings and unlock the local secrets store before connecting GitHub.",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	clientID := h.githubOAuthClientID()
	if clientID == "" {
		connected, err := h.connectGitHubFromCLI(ctx)
		if connected {
			h.persistGitHubAuthHealth(true, "Signed in to GitHub.", false)
			writeJSON(w, http.StatusOK, map[string]any{
				"state":  "complete",
				"status": h.githubAuthStatus(r.Context()),
			})
			return
		}
		detail := ""
		if err != nil {
			detail = err.Error()
		}
		h.persistGitHubAuthHealth(false, detail, false)
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":  "GitHub sign-in is not configured for this build",
			"detail": detail,
			"hint":   "Sign in with GitHub CLI on this machine, or configure a custom OAuth client ID in advanced sign-in settings.",
		})
		return
	}
	device, err := requestGitHubDeviceCode(ctx, clientID)
	if err != nil {
		h.persistGitHubAuthHealth(false, err.Error(), false)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	state, err := randomGitHubAuthState()
	if err != nil {
		h.writeErr(w, err)
		return
	}
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	h.githubAuth.set(state, githubDeviceSession{
		DeviceCode: device.DeviceCode,
		ExpiresAt:  time.Now().Add(time.Duration(device.ExpiresIn) * time.Second),
		Interval:   interval,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"state":                     state,
		"user_code":                 device.UserCode,
		"verification_uri":          device.VerificationURI,
		"verification_uri_complete": device.VerificationURIComplete,
		"expires_in":                device.ExpiresIn,
		"interval":                  int(interval / time.Second),
	})
}

func (h *Handler) handlePollGitHubAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		State string `json:"state"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	state := strings.TrimSpace(body.State)
	if state == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "state is required"})
		return
	}
	session, ok := h.githubAuth.get(state)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "GitHub authorization session expired"})
		return
	}
	clientID := h.githubOAuthClientID()
	if clientID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "GitHub OAuth client ID is not configured"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	token, err := requestGitHubAccessToken(ctx, clientID, session.DeviceCode)
	if err != nil {
		var pending githubAuthPendingError
		if errors.As(err, &pending) {
			if pending.SlowDown {
				session.Interval += 5 * time.Second
			}
			session.LastPoll = time.Now()
			interval := session.Interval
			h.githubAuth.update(state, func(githubDeviceSession) githubDeviceSession { return session })
			writeJSON(w, http.StatusOK, map[string]any{
				"state":    "pending",
				"interval": int(interval / time.Second),
			})
			return
		}
		h.githubAuth.delete(state)
		h.persistGitHubAuthHealth(false, err.Error(), false)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	login, err := fetchGitHubLogin(ctx, token.AccessToken)
	if err != nil {
		h.githubAuth.delete(state)
		h.persistGitHubAuthHealth(false, err.Error(), false)
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if h.secrets == nil {
		h.githubAuth.delete(state)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "secrets store is not configured"})
		return
	}
	if err := h.secrets.SetGitHubOAuthAccessToken(token.AccessToken, login, token.Scope, token.TokenType); err != nil {
		h.githubAuth.delete(state)
		h.persistGitHubAuthHealth(false, err.Error(), false)
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
		return
	}
	h.githubAuth.delete(state)
	h.persistGitHubAuthHealth(true, "Signed in to GitHub.", false)
	writeJSON(w, http.StatusOK, map[string]any{
		"state":  "complete",
		"status": h.githubAuthStatus(r.Context()),
	})
}

func (h *Handler) handleDisconnectGitHubAuth(w http.ResponseWriter, r *http.Request) {
	if h.secrets == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "secrets store is not configured"})
		return
	}
	if err := h.secrets.ClearGitHubOAuthAccessToken(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": err.Error(),
			"hint":  "Open Settings and unlock the local secrets store before disconnecting GitHub.",
		})
		return
	}
	h.persistGitHubAuthHealth(false, "Signed out of GitHub.", true)
	writeJSON(w, http.StatusOK, map[string]any{"status": h.githubAuthStatus(r.Context())})
}

func (h *Handler) githubAuthStatus(ctx context.Context) githubAuthStatus {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	status := githubAuthStatus{
		ClientID: h.githubOAuthClientID(),
		Profile:  loadGitProfile(ctx),
	}
	status.ClientIDConfigured = status.ClientID != ""
	status.Configured = status.ClientIDConfigured
	if h.secrets == nil {
		status.SecretsLocked = true
		return status
	}
	initialized, locked, configured := h.secrets.Status()
	status.SecretsInitialized = initialized
	status.SecretsLocked = locked
	status.Connected = configured.GitHub
	status.Login = strings.TrimSpace(configured.GitHubLogin)
	if initialized && !locked {
		if gh, err := h.secrets.GetGitHub(); err == nil {
			status.Connected = strings.TrimSpace(gh.OAuthAccessToken) != ""
			status.Login = strings.TrimSpace(gh.Login)
			status.Scope = strings.TrimSpace(gh.Scope)
		}
	}
	if !status.Connected {
		if token, login, err := githubAuthFromCLI(ctx); err == nil && strings.TrimSpace(token) != "" {
			status.Connected = true
			status.Login = strings.TrimSpace(login)
		}
	}
	if h.settings != nil {
		settings := h.settings.Get()
		if settings.GitHub != nil {
			status.LastCheckedAt = cloneInt64Ptr(settings.GitHub.LastCheckedAt)
			status.LastCheckOk = cloneBoolPtr(settings.GitHub.LastCheckOk)
			if settings.GitHub.LastCheckMessage != nil {
				status.LastCheckMessage = strings.TrimSpace(*settings.GitHub.LastCheckMessage)
			}
		}
	}
	if status.Connected {
		status.Configured = true
	}
	return status
}

func (h *Handler) persistGitHubAuthHealth(ok bool, message string, disable bool) {
	if h == nil || h.settings == nil {
		return
	}
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		if ok {
			checkedMessage = "GitHub connection verified."
		} else {
			checkedMessage = "GitHub connection check failed."
		}
	}
	next := h.settings.Get()
	if next.GitHub == nil {
		next.GitHub = config.DefaultSettings().GitHub
	}
	if next.GitHub == nil {
		next.GitHub = &config.GitHubSettings{}
	}
	if disable {
		off := false
		next.GitHub.Enabled = &off
		next.GitHub.ReadTools = &off
	}
	next.GitHub.LastCheckedAt = &checkedAt
	next.GitHub.LastCheckOk = &ok
	next.GitHub.LastCheckMessage = &checkedMessage
	_ = h.settings.Save(next)
}

func (h *Handler) githubOAuthClientID() string {
	if h.settings == nil {
		return defaultGitHubOAuthClientID
	}
	settings := h.settings.Get()
	if settings.GitHubOAuthClientID == nil {
		return defaultGitHubOAuthClientID
	}
	if trimmed := strings.TrimSpace(*settings.GitHubOAuthClientID); trimmed != "" {
		return trimmed
	}
	return defaultGitHubOAuthClientID
}

func (h *Handler) requireUnlockedSecretsForGitHub() error {
	if h.secrets == nil {
		return errors.New("secrets store is not configured")
	}
	initialized, locked, _ := h.secrets.Status()
	if !initialized {
		return errors.New("secrets store is not initialized")
	}
	if locked {
		return errors.New("secrets store is locked")
	}
	return nil
}

func (h *Handler) connectGitHubFromCLI(ctx context.Context) (bool, error) {
	if h.secrets == nil {
		return false, errors.New("secrets store is not configured")
	}
	token, login, err := githubAuthFromCLI(ctx)
	if err != nil {
		return false, err
	}
	if err := h.secrets.SetGitHubOAuthAccessToken(token, login, "", "bearer"); err != nil {
		return false, err
	}
	return true, nil
}

func githubCLIToken(ctx context.Context) (string, error) {
	out, err := runCommandOutput(ctx, "gh", "auth", "token", "--hostname", "github.com")
	token := strings.TrimSpace(out)
	if err != nil {
		return "", errors.New("GitHub CLI authentication is not available")
	}
	if token == "" {
		return "", errors.New("GitHub CLI did not return a token")
	}
	return token, nil
}

func githubAuthFromCLI(ctx context.Context) (string, string, error) {
	token, err := githubCLIToken(ctx)
	if err != nil {
		return "", "", err
	}
	login, err := fetchGitHubLogin(ctx, token)
	if err != nil {
		return "", "", err
	}
	return token, login, nil
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

type githubDeviceCodeResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDescription        string `json:"error_description"`
}

func requestGitHubDeviceCode(ctx context.Context, clientID string) (githubDeviceCodeResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("scope", githubOAuthScope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return githubDeviceCodeResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "ai-analyst-local")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubDeviceCodeResponse{}, err
	}
	defer res.Body.Close()
	var out githubDeviceCodeResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return githubDeviceCodeResponse{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 || strings.TrimSpace(out.Error) != "" {
		return githubDeviceCodeResponse{}, githubOAuthError(out.Error, out.ErrorDescription, res.StatusCode)
	}
	if strings.TrimSpace(out.DeviceCode) == "" || strings.TrimSpace(out.UserCode) == "" {
		return githubDeviceCodeResponse{}, errors.New("GitHub did not return a device authorization code")
	}
	return out, nil
}

type githubAccessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

type githubAuthPendingError struct {
	SlowDown bool
}

func (e githubAuthPendingError) Error() string {
	if e.SlowDown {
		return "GitHub requested slower polling"
	}
	return "GitHub authorization is pending"
}

func requestGitHubAccessToken(ctx context.Context, clientID string, deviceCode string) (githubAccessTokenResponse, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, githubAccessTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return githubAccessTokenResponse{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "ai-analyst-local")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return githubAccessTokenResponse{}, err
	}
	defer res.Body.Close()
	var out githubAccessTokenResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return githubAccessTokenResponse{}, err
	}
	switch strings.TrimSpace(out.Error) {
	case "":
	case "authorization_pending":
		return githubAccessTokenResponse{}, githubAuthPendingError{}
	case "slow_down":
		return githubAccessTokenResponse{}, githubAuthPendingError{SlowDown: true}
	case "expired_token":
		return githubAccessTokenResponse{}, errors.New("GitHub authorization expired")
	case "access_denied":
		return githubAccessTokenResponse{}, errors.New("GitHub authorization was denied")
	default:
		return githubAccessTokenResponse{}, githubOAuthError(out.Error, out.ErrorDescription, res.StatusCode)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return githubAccessTokenResponse{}, githubOAuthError(out.Error, out.ErrorDescription, res.StatusCode)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return githubAccessTokenResponse{}, errors.New("GitHub did not return an access token")
	}
	return out, nil
}

func fetchGitHubLogin(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubUserAPIURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("User-Agent", "ai-analyst-local")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(res.Body); err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("GitHub user lookup failed (HTTP %d)", res.StatusCode)
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body.Bytes(), &out); err != nil {
		return "", err
	}
	if strings.TrimSpace(out.Login) == "" {
		return "", errors.New("GitHub user lookup did not return a login")
	}
	return strings.TrimSpace(out.Login), nil
}

func githubOAuthError(code string, description string, status int) error {
	msg := strings.TrimSpace(description)
	if msg == "" {
		msg = strings.TrimSpace(code)
	}
	if msg == "" {
		msg = fmt.Sprintf("GitHub OAuth request failed (HTTP %d)", status)
	}
	return errors.New(msg)
}

func randomGitHubAuthState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
