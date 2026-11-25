package databricks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type tokenEndpointResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int64  `json:"expires_in,omitempty"`

	Error            string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
}

func (h *Handler) accessToken(ctx context.Context, host string) (string, error) {
	for {
		mode, pat, refreshToken, clientID, err := h.databricksCredentials()
		if err != nil {
			return "", err
		}

		if mode == "pat" {
			return pat, nil
		}

		if mode != "refresh_token" || strings.TrimSpace(refreshToken) == "" {
			return "", httpError{
				status: http.StatusUnauthorized,
				msg:    "Databricks credentials are not configured. Set a refresh token or PAT in Settings.",
			}
		}

		cacheKey := h.tokenCacheKey(host, mode, clientID)
		if tok := h.cachedTokenForKey(cacheKey); tok != "" {
			return tok, nil
		}

		waitCh, refresh := h.beginTokenRefresh()
		if !refresh {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-waitCh:
				// Another goroutine finished the refresh path; loop and re-check.
				continue
			}
		}

		// We own refresh. Re-check cache and credentials to avoid stale reads.
		if tok := h.cachedTokenForKey(cacheKey); tok != "" {
			h.endTokenRefresh()
			return tok, nil
		}

		mode, pat, refreshToken, clientID, err = h.databricksCredentials()
		if err != nil {
			h.endTokenRefresh()
			return "", err
		}
		if mode == "pat" {
			h.endTokenRefresh()
			return pat, nil
		}
		if mode != "refresh_token" || strings.TrimSpace(refreshToken) == "" {
			h.endTokenRefresh()
			return "", httpError{
				status: http.StatusUnauthorized,
				msg:    "Databricks refresh token is not configured.",
			}
		}

		tok, exp, rotatedRefreshToken, err := h.refreshAccessToken(ctx, host, refreshToken, clientID)
		if err != nil {
			h.endTokenRefresh()
			return "", err
		}
		if strings.TrimSpace(rotatedRefreshToken) != "" && strings.TrimSpace(rotatedRefreshToken) != strings.TrimSpace(refreshToken) {
			if h.Secrets == nil {
				h.endTokenRefresh()
				return "", httpError{
					status: http.StatusUnauthorized,
					msg:    "Databricks token rotation failed: secrets store is unavailable",
				}
			}
			if err := h.Secrets.SetDatabricksRefreshTokenWithClientID(rotatedRefreshToken, clientID); err != nil {
				h.endTokenRefresh()
				return "", httpError{
					status: http.StatusUnauthorized,
					msg:    fmt.Sprintf("Databricks token rotation failed: %v", err),
				}
			}
		}

		h.cacheToken(cacheKey, tok, exp)
		h.endTokenRefresh()
		return tok, nil
	}
}

func (h *Handler) databricksCredentials() (mode string, pat string, refreshToken string, clientID string, err error) {
	if h.Secrets == nil {
		return "", "", "", "", httpError{
			status: http.StatusUnauthorized,
			msg:    "Databricks credentials are unavailable: secrets store is not configured",
		}
	}
	secrets, err := h.Secrets.GetDatabricks()
	if err != nil {
		return "", "", "", "", httpError{
			status: http.StatusUnauthorized,
			msg:    err.Error(),
		}
	}

	mode = strings.TrimSpace(strings.ToLower(secrets.Mode))
	pat = strings.TrimSpace(secrets.PAT)
	refreshToken = strings.TrimSpace(secrets.RefreshToken)
	clientID = strings.TrimSpace(secrets.ClientID)

	if mode == "" {
		switch {
		case refreshToken != "":
			mode = "refresh_token"
		case pat != "":
			mode = "pat"
		default:
			mode = "none"
		}
	}

	switch mode {
	case "pat", "refresh_token", "none":
		if mode == "refresh_token" && clientID == "" {
			clientID = "databricks-cli"
		}
		return mode, pat, refreshToken, clientID, nil
	default:
		return "", "", "", "", httpError{
			status: http.StatusBadRequest,
			msg:    "invalid Databricks auth mode in secrets store",
		}
	}
}

func (h *Handler) refreshAccessToken(ctx context.Context, host string, refreshToken string, clientID string) (string, time.Time, string, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		clientID = "databricks-cli"
	}
	form.Set("client_id", clientID)
	form.Set("refresh_token", strings.TrimSpace(refreshToken))

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(host, "/")+"/oidc/v1/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", time.Time{}, "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := h.httpc.Do(req)
	if err != nil {
		return "", time.Time{}, "", httpError{
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
		return "", time.Time{}, "", httpError{status: resp.StatusCode, msg: msg}
	}

	tok := strings.TrimSpace(tr.AccessToken)
	if tok == "" {
		return "", time.Time{}, "", httpError{
			status: http.StatusBadGateway,
			msg:    "empty access_token from Databricks token endpoint",
		}
	}

	expiresIn := tr.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	exp := time.Now().Add(time.Duration(expiresIn) * time.Second)
	return tok, exp, strings.TrimSpace(tr.RefreshToken), nil
}

func (h *Handler) tokenCacheKey(host string, mode string, clientID string) string {
	mode = strings.TrimSpace(mode)
	host = strings.TrimSpace(host)
	clientID = strings.TrimSpace(clientID)
	if mode == "refresh_token" {
		if clientID == "" {
			clientID = "databricks-cli"
		}
		return mode + ":" + host + ":" + clientID
	}
	return mode + ":" + host
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

func (h *Handler) cacheToken(cacheKey string, tok string, exp time.Time) {
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return
	}
	cacheKey = strings.TrimSpace(cacheKey)
	if cacheKey == "" {
		return
	}
	until := exp
	if until.IsZero() {
		until = time.Now().Add(50 * time.Minute)
	}
	// Refresh slightly before expiration.
	until = until.Add(-1 * time.Minute)
	if until.Before(time.Now().Add(10 * time.Second)) {
		until = time.Now().Add(10 * time.Second)
	}

	h.tokenMu.Lock()
	h.cachedToken = tok
	h.cachedUntil = until
	h.cachedKey = cacheKey
	h.tokenMu.Unlock()
}

func (h *Handler) invalidateCachedToken() {
	h.tokenMu.Lock()
	h.cachedToken = ""
	h.cachedUntil = time.Time{}
	h.cachedKey = ""
	h.tokenMu.Unlock()
}
