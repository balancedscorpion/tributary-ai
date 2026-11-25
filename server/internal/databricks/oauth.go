package databricks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	oauthFlowTTL   = 10 * time.Minute
	oauthResultTTL = 5 * time.Minute
)

type oauthPendingFlow struct {
	State        string
	Host         string
	ClientID     string
	RedirectURI  string
	CodeVerifier string
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

func (h *Handler) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		Host           string `json:"host,omitempty"`
		DatabricksHost string `json:"databricksHost,omitempty"`
		ClientID       string `json:"client_id,omitempty"`
		ClientId       string `json:"clientId,omitempty"`
		OAuthClientID  string `json:"oauthClientId,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		writeDatabricksError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	rawHost := strings.TrimSpace(body.Host)
	if rawHost == "" {
		rawHost = strings.TrimSpace(body.DatabricksHost)
	}

	host := ""
	var err error
	if rawHost != "" {
		host, err = normalizeGCPDatabricksHost(rawHost)
		if err != nil {
			writeDatabricksError(w, http.StatusBadRequest, err.Error())
			return
		}
	} else {
		host, err = h.normalizedHostFromSettings()
		if err != nil {
			writeDatabricksError(w, httpStatusForErr(err), err.Error())
			return
		}
	}

	clientID := strings.TrimSpace(body.ClientID)
	if clientID == "" {
		clientID = strings.TrimSpace(body.ClientId)
	}
	if clientID == "" {
		clientID = strings.TrimSpace(body.OAuthClientID)
	}
	if clientID == "" {
		clientID = strings.TrimSpace(h.oauthClientIDFromSettings())
	}
	if clientID == "" {
		clientID = "databricks-cli"
	}
	if strings.ContainsAny(clientID, " \t\r\n") {
		writeDatabricksError(w, http.StatusBadRequest, "OAuth client_id must not contain whitespace")
		return
	}

	redirectURI, useOriginRedirect := oauthLocalhostRedirectFromRequest(r)
	var listeners []net.Listener
	listenersInUse := false
	if !useOriginRedirect {
		var port int
		listeners, port, err = startOAuthLoopbackListeners()
		if err != nil {
			writeDatabricksError(w, http.StatusInternalServerError, "Failed to start OAuth callback listener")
			return
		}
		redirectURI = fmt.Sprintf("http://localhost:%d", port)
		defer func() {
			if !listenersInUse {
				closeOAuthListeners(listeners)
			}
		}()
	}

	state, err := randomURLSafe(32)
	if err != nil {
		writeDatabricksError(w, http.StatusInternalServerError, "Failed to start OAuth flow")
		return
	}
	verifier, err := randomURLSafe(64)
	if err != nil {
		writeDatabricksError(w, http.StatusInternalServerError, "Failed to start OAuth flow")
		return
	}
	challenge := pkceCodeChallenge(verifier)

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", "all-apis offline_access")
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	authorizeURL := strings.TrimRight(host, "/") + "/oidc/v1/authorize?" + q.Encode()

	now := time.Now()
	h.storeOAuthFlow(&oauthPendingFlow{
		State:        state,
		Host:         host,
		ClientID:     clientID,
		RedirectURI:  redirectURI,
		CodeVerifier: verifier,
		CreatedAt:    now,
		ExpiresAt:    now.Add(oauthFlowTTL),
	})

	if len(listeners) > 0 {
		// Start ephemeral HTTP servers to receive OAuth redirects on loopback.
		for _, ln := range listeners {
			if ln == nil {
				continue
			}
			srv := &http.Server{
				Handler:           http.HandlerFunc(h.handleEphemeralOAuthCallback),
				ReadHeaderTimeout: 10 * time.Second,
			}
			h.storeOAuthServer(state, srv)
			go func(server *http.Server, listener net.Listener) {
				_ = server.Serve(listener)
			}(srv, ln)
		}
		listenersInUse = true
		go func() {
			// Safety net: shut down after the flow TTL even if no callback arrives.
			time.Sleep(oauthFlowTTL + 30*time.Second)
			h.shutdownOAuthServer(state)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"authorize_url":      authorizeURL,
		"state":              state,
		"expires_in_seconds": int(oauthFlowTTL.Seconds()),
		"client_id":          clientID,
		"host":               host,
		"redirect_uri":       redirectURI,
		"callback_mode": func() string {
			if useOriginRedirect {
				return "origin"
			}
			return "loopback"
		}(),
	})
}

func (h *Handler) handleOAuthComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		State             string `json:"state,omitempty"`
		Code              string `json:"code,omitempty"`
		AuthorizationCode string `json:"authorization_code,omitempty"`
		Error             string `json:"error,omitempty"`
		ErrorDescription  string `json:"error_description,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		writeDatabricksError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	state := strings.TrimSpace(body.State)
	if state == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing OAuth state")
		return
	}

	flow, ok := h.loadOAuthFlow(state)
	if !ok {
		writeDatabricksError(w, http.StatusNotFound, "OAuth session not found or expired")
		return
	}

	if flow.Result != nil && flow.Result.OK {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "success",
			"ok":      true,
			"message": nilIfEmpty(strings.TrimSpace(flow.Result.Message)),
			"error":   nil,
		})
		return
	}

	errMsg := strings.TrimSpace(body.ErrorDescription)
	if errMsg == "" {
		errMsg = strings.TrimSpace(body.Error)
	}
	if errMsg != "" {
		res := oauthFlowResult{
			OK:          false,
			Error:       errMsg,
			CompletedAt: time.Now(),
		}
		h.setOAuthFlowResult(state, res)
		h.shutdownOAuthServer(state)
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "error",
			"ok":      false,
			"message": nil,
			"error":   errMsg,
		})
		return
	}

	code := strings.TrimSpace(body.Code)
	if code == "" {
		code = strings.TrimSpace(body.AuthorizationCode)
	}
	if code == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing authorization code")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	res := h.completeOAuthFlowWithCode(ctx, flow, code)
	h.setOAuthFlowResult(state, res)
	if res.OK {
		h.shutdownOAuthServer(state)
	}

	status := "error"
	if res.OK {
		status = "success"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  status,
		"ok":      res.OK,
		"message": nilIfEmpty(strings.TrimSpace(res.Message)),
		"error":   nilIfEmpty(strings.TrimSpace(res.Error)),
	})
}

func (h *Handler) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		h.writeOAuthCallbackPage(w, "", oauthFlowResult{
			OK:    false,
			Error: "Missing OAuth state",
		})
		return
	}

	flow, ok := h.loadOAuthFlow(state)
	if !ok {
		h.writeOAuthCallbackPage(w, state, oauthFlowResult{
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
		h.setOAuthFlowResult(state, res)
		h.writeOAuthCallbackPage(w, state, res)
		return
	}

	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		res := oauthFlowResult{
			OK:          false,
			Error:       "Missing authorization code",
			CompletedAt: time.Now(),
		}
		h.setOAuthFlowResult(state, res)
		h.writeOAuthCallbackPage(w, state, res)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	res := h.completeOAuthFlowWithCode(ctx, flow, code)
	h.setOAuthFlowResult(state, res)
	h.writeOAuthCallbackPage(w, state, res)
}

func (h *Handler) completeOAuthFlowWithCode(ctx context.Context, flow *oauthPendingFlow, code string) oauthFlowResult {
	if flow == nil {
		return oauthFlowResult{
			OK:          false,
			Error:       "OAuth session not found or expired. Start the flow again from Settings.",
			CompletedAt: time.Now(),
		}
	}

	refreshToken, err := h.exchangeAuthorizationCode(ctx, flow, code)
	if err != nil {
		return oauthFlowResult{
			OK:          false,
			Error:       err.Error(),
			CompletedAt: time.Now(),
		}
	}

	if h.Secrets == nil {
		return oauthFlowResult{
			OK:          false,
			Error:       "Databricks token save failed: secrets store is unavailable",
			CompletedAt: time.Now(),
		}
	}
	if err := h.Secrets.SetDatabricksRefreshTokenWithClientID(refreshToken, flow.ClientID); err != nil {
		return oauthFlowResult{
			OK:          false,
			Error:       fmt.Sprintf("Databricks token save failed: %v", err),
			CompletedAt: time.Now(),
		}
	}

	h.invalidateCachedToken()
	return oauthFlowResult{
		OK:          true,
		Message:     "Databricks refresh token saved.",
		CompletedAt: time.Now(),
	}
}

func (h *Handler) handleOAuthResult(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing OAuth state")
		return
	}

	flow, ok := h.loadOAuthFlow(state)
	if !ok {
		writeDatabricksError(w, http.StatusNotFound, "OAuth session not found or expired")
		return
	}
	if flow.Result == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status": "pending",
			"ok":     false,
		})
		return
	}

	status := "error"
	if flow.Result.OK {
		status = "success"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  status,
		"ok":      flow.Result.OK,
		"message": nilIfEmpty(strings.TrimSpace(flow.Result.Message)),
		"error":   nilIfEmpty(strings.TrimSpace(flow.Result.Error)),
	})
}

func (h *Handler) exchangeAuthorizationCode(ctx context.Context, flow *oauthPendingFlow, code string) (string, error) {
	if flow == nil {
		return "", httpError{status: http.StatusBadRequest, msg: "OAuth session is missing"}
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", strings.TrimSpace(flow.ClientID))
	form.Set("scope", "all-apis offline_access")
	form.Set("redirect_uri", strings.TrimSpace(flow.RedirectURI))
	form.Set("code_verifier", strings.TrimSpace(flow.CodeVerifier))
	form.Set("code", strings.TrimSpace(code))

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(strings.TrimSpace(flow.Host), "/")+"/oidc/v1/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.httpc.Do(req)
	if err != nil {
		return "", httpError{
			status: http.StatusBadGateway,
			msg:    fmt.Sprintf("failed to reach Databricks token endpoint: %v", err),
		}
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr tokenEndpointResponse
	_ = json.Unmarshal(b, &tr)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(tr.ErrorDescription)
		if msg == "" {
			msg = strings.TrimSpace(tr.Error)
		}
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		if msg == "" {
			msg = fmt.Sprintf("Databricks token endpoint error (HTTP %d)", resp.StatusCode)
		}
		return "", httpError{status: resp.StatusCode, msg: msg}
	}

	refreshToken := strings.TrimSpace(tr.RefreshToken)
	if refreshToken == "" {
		return "", httpError{
			status: http.StatusBadGateway,
			msg:    "Databricks OAuth did not return a refresh token. Ensure offline_access scope is granted.",
		}
	}
	return refreshToken, nil
}

func oauthCallbackURI(r *http.Request) (string, error) {
	origin, err := requestOrigin(r)
	if err != nil {
		return "", err
	}
	return origin + "/api/databricks/oauth/callback", nil
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
		return "", errors.New("Invalid request scheme for OAuth callback URL")
	}

	host := strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
	if strings.Contains(host, ",") {
		host = strings.TrimSpace(strings.SplitN(host, ",", 2)[0])
	}
	if host == "" {
		host = strings.TrimSpace(r.Host)
	}
	if host == "" {
		return "", errors.New("Unable to determine callback host")
	}
	if strings.ContainsAny(host, " \t\r\n") {
		return "", errors.New("Invalid callback host")
	}

	return scheme + "://" + host, nil
}

func oauthLocalhostRedirectFromRequest(r *http.Request) (string, bool) {
	origin, err := requestOrigin(r)
	if err != nil {
		return "", false
	}
	u, err := url.Parse(origin)
	if err != nil || u == nil {
		return "", false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	switch host {
	case "localhost", "127.0.0.1", "::1":
		// Supported loopback hosts.
	default:
		return "", false
	}

	port := strings.TrimSpace(u.Port())
	if port == "" {
		switch strings.ToLower(strings.TrimSpace(u.Scheme)) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", false
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return "", false
	}
	return fmt.Sprintf("http://localhost:%d", n), true
}

func startOAuthLoopbackListeners() ([]net.Listener, int, error) {
	if l4, err := net.Listen("tcp4", "127.0.0.1:0"); err == nil && l4 != nil {
		tcp, ok := l4.Addr().(*net.TCPAddr)
		if !ok || tcp.Port <= 0 {
			_ = l4.Close()
			return nil, 0, errors.New("invalid loopback listener address")
		}
		port := tcp.Port
		listeners := []net.Listener{l4}
		if l6, err6 := net.Listen("tcp6", fmt.Sprintf("[::1]:%d", port)); err6 == nil && l6 != nil {
			listeners = append(listeners, l6)
		}
		return listeners, port, nil
	}

	if l6, err := net.Listen("tcp6", "[::1]:0"); err == nil && l6 != nil {
		tcp, ok := l6.Addr().(*net.TCPAddr)
		if !ok || tcp.Port <= 0 {
			_ = l6.Close()
			return nil, 0, errors.New("invalid loopback listener address")
		}
		port := tcp.Port
		listeners := []net.Listener{l6}
		if l4, err4 := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", port)); err4 == nil && l4 != nil {
			listeners = append(listeners, l4)
		}
		return listeners, port, nil
	}

	return nil, 0, errors.New("unable to bind localhost callback listener")
}

func closeOAuthListeners(listeners []net.Listener) {
	for _, ln := range listeners {
		if ln == nil {
			continue
		}
		_ = ln.Close()
	}
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

func (h *Handler) storeOAuthFlow(flow *oauthPendingFlow) {
	if flow == nil || strings.TrimSpace(flow.State) == "" {
		return
	}
	h.oauthMu.Lock()
	defer h.oauthMu.Unlock()
	if h.oauthFlows == nil {
		h.oauthFlows = map[string]*oauthPendingFlow{}
	}
	h.cleanupOAuthFlowsLocked(time.Now())
	h.oauthFlows[strings.TrimSpace(flow.State)] = flow
}

func (h *Handler) loadOAuthFlow(state string) (*oauthPendingFlow, bool) {
	state = strings.TrimSpace(state)
	if state == "" {
		return nil, false
	}
	h.oauthMu.Lock()
	defer h.oauthMu.Unlock()
	h.cleanupOAuthFlowsLocked(time.Now())
	flow, ok := h.oauthFlows[state]
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

func (h *Handler) setOAuthFlowResult(state string, result oauthFlowResult) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	now := time.Now()
	if result.CompletedAt.IsZero() {
		result.CompletedAt = now
	}

	h.oauthMu.Lock()
	defer h.oauthMu.Unlock()
	h.cleanupOAuthFlowsLocked(now)
	flow, ok := h.oauthFlows[state]
	if !ok || flow == nil {
		return
	}
	res := result
	flow.Result = &res
	flow.ExpiresAt = now.Add(oauthResultTTL)
}

func (h *Handler) cleanupOAuthFlowsLocked(now time.Time) {
	if h.oauthFlows == nil {
		return
	}
	for state, flow := range h.oauthFlows {
		if flow == nil {
			delete(h.oauthFlows, state)
			h.shutdownOAuthServerLocked(state)
			continue
		}
		exp := flow.ExpiresAt
		if exp.IsZero() && !flow.CreatedAt.IsZero() {
			exp = flow.CreatedAt.Add(oauthFlowTTL)
		}
		if !exp.IsZero() && now.After(exp) {
			delete(h.oauthFlows, state)
			h.shutdownOAuthServerLocked(state)
		}
	}
}

// handleEphemeralOAuthCallback is the handler for the ephemeral loopback
// listener. It delegates to the main callback logic and then schedules
// shutdown of the ephemeral server.
func (h *Handler) handleEphemeralOAuthCallback(w http.ResponseWriter, r *http.Request) {
	h.handleOAuthCallback(w, r)
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state != "" {
		go func() {
			// Small delay so the HTTP response is flushed before shutdown.
			time.Sleep(500 * time.Millisecond)
			h.shutdownOAuthServer(state)
		}()
	}
}

func (h *Handler) storeOAuthServer(state string, srv *http.Server) {
	state = strings.TrimSpace(state)
	if state == "" || srv == nil {
		return
	}
	h.oauthMu.Lock()
	defer h.oauthMu.Unlock()
	if h.oauthServers == nil {
		h.oauthServers = map[string][]*http.Server{}
	}
	h.oauthServers[state] = append(h.oauthServers[state], srv)
}

func shutdownOAuthServerList(servers []*http.Server) {
	for _, srv := range servers {
		if srv == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = srv.Shutdown(ctx)
		cancel()
	}
}

func (h *Handler) shutdownOAuthServer(state string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	h.oauthMu.Lock()
	servers, ok := h.oauthServers[state]
	if ok {
		delete(h.oauthServers, state)
	}
	h.oauthMu.Unlock()
	if ok && len(servers) > 0 {
		shutdownOAuthServerList(append([]*http.Server(nil), servers...))
	}
}

// shutdownOAuthServerLocked shuts down the ephemeral server for the given
// state. Caller must hold h.oauthMu.
func (h *Handler) shutdownOAuthServerLocked(state string) {
	state = strings.TrimSpace(state)
	if state == "" {
		return
	}
	if h.oauthServers == nil {
		return
	}
	servers, ok := h.oauthServers[state]
	if ok {
		delete(h.oauthServers, state)
		if len(servers) == 0 {
			return
		}
		copyServers := append([]*http.Server(nil), servers...)
		go func() {
			shutdownOAuthServerList(copyServers)
		}()
	}
}

func (h *Handler) writeOAuthCallbackPage(w http.ResponseWriter, state string, result oauthFlowResult) {
	payload := map[string]any{
		"type":  "databricks-oauth-code",
		"state": strings.TrimSpace(state),
		"ok":    result.OK,
	}
	if strings.TrimSpace(result.Message) != "" {
		payload["message"] = strings.TrimSpace(result.Message)
	}
	if strings.TrimSpace(result.Error) != "" {
		payload["error"] = strings.TrimSpace(result.Error)
	}
	payloadJSON, _ := json.Marshal(payload)
	payloadJS := strings.ReplaceAll(string(payloadJSON), "</", "<\\/")

	title := "Databricks Authorization Complete"
	body := "Databricks refresh token was saved. You can close this window."
	if !result.OK {
		title = "Databricks Authorization Failed"
		body = strings.TrimSpace(result.Error)
		if body == "" {
			body = "Authorization failed. Return to Settings and try again."
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(
		w,
		`<!doctype html>
<html lang="en">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width,initial-scale=1" />
    <title>%s</title>
    <style>
      body { font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, Helvetica, Arial, sans-serif; background: #0b0f17; color: #e7ecf3; margin: 0; display: grid; place-items: center; min-height: 100vh; }
      .card { max-width: 420px; margin: 24px; padding: 20px; border: 1px solid #293243; border-radius: 12px; background: #121927; }
      h1 { margin: 0 0 10px; font-size: 18px; line-height: 1.3; }
      p { margin: 0; font-size: 14px; color: #b8c2d3; line-height: 1.5; }
      button { margin-top: 14px; border: 1px solid #3f4a5f; border-radius: 8px; background: #1c2434; color: #e7ecf3; padding: 8px 12px; cursor: pointer; }
    </style>
  </head>
  <body>
    <div class="card">
      <h1>%s</h1>
      <p>%s</p>
      <button onclick="window.close()">Close</button>
    </div>
    <script>
      const payload = %s;
      try {
        if (window.opener && !window.opener.closed) {
          window.opener.postMessage(payload, '*');
        }
      } catch (_) {}
      setTimeout(() => { try { window.close(); } catch (_) {} }, 1200);
    </script>
  </body>
</html>`,
		html.EscapeString(title),
		html.EscapeString(title),
		html.EscapeString(body),
		payloadJS,
	)
}
