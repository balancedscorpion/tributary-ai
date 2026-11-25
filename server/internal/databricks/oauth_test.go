package databricks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestExchangeAuthorizationCodeUsesDocumentedPKCEForm(t *testing.T) {
	var form url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oidc/v1/token" {
			t.Fatalf("path = %q, want /oidc/v1/token", r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/x-www-form-urlencoded") {
			t.Fatalf("content-type = %q, want form encoded", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "access-token",
			"refresh_token": "refresh-token",
			"expires_in":    3600,
		})
	}))
	defer ts.Close()

	h := NewHandler(nil, nil)
	flow := &oauthPendingFlow{
		Host:         ts.URL,
		ClientID:     "databricks-cli",
		RedirectURI:  "http://localhost:8020",
		CodeVerifier: "verifier",
	}

	got, err := h.exchangeAuthorizationCode(context.Background(), flow, "auth-code")
	if err != nil {
		t.Fatalf("exchangeAuthorizationCode error: %v", err)
	}
	if got != "refresh-token" {
		t.Fatalf("refresh token = %q, want refresh-token", got)
	}

	want := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     "databricks-cli",
		"scope":         "all-apis offline_access",
		"redirect_uri":  "http://localhost:8020",
		"code_verifier": "verifier",
		"code":          "auth-code",
	}
	for key, value := range want {
		if got := form.Get(key); got != value {
			t.Fatalf("form[%s] = %q, want %q", key, got, value)
		}
	}
}

func TestOAuthCallbackPagePostsFrontendMessageType(t *testing.T) {
	h := NewHandler(nil, nil)
	rec := httptest.NewRecorder()

	h.writeOAuthCallbackPage(rec, "state-123", oauthFlowResult{
		OK:          true,
		Message:     "done",
		CompletedAt: time.Now(),
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"databricks-oauth-code"`) {
		t.Fatalf("callback page did not include frontend message type: %s", body)
	}
	if !strings.Contains(body, `"state":"state-123"`) {
		t.Fatalf("callback page did not include state: %s", body)
	}
}
