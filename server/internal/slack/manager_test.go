package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestOAuthCallbackURINormalizesLoopbackToLocalhostForPKCE(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "ipv4 loopback",
			url:  "http://127.0.0.1:5173/api/integrations/slack/oauth/start",
			want: "http://localhost:5173/api/integrations/slack/oauth/callback",
		},
		{
			name: "ipv6 loopback",
			url:  "http://[::1]:5173/api/integrations/slack/oauth/start",
			want: "http://localhost:5173/api/integrations/slack/oauth/callback",
		},
		{
			name: "localhost",
			url:  "http://localhost:5173/api/integrations/slack/oauth/start",
			want: "http://localhost:5173/api/integrations/slack/oauth/callback",
		},
		{
			name: "non-loopback",
			url:  "https://example.test/api/integrations/slack/oauth/start",
			want: "https://example.test/api/integrations/slack/oauth/callback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.url, nil)
			got, err := oauthCallbackURI(req)
			if err != nil {
				t.Fatalf("oauthCallbackURI returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("oauthCallbackURI()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlackBotOAuthAvailabilityClassifiesDesktopRedirects(t *testing.T) {
	tests := []struct {
		name      string
		redirect  string
		available bool
	}{
		{name: "localhost", redirect: "http://localhost:5173/api/integrations/slack/oauth/callback", available: false},
		{name: "ipv4 loopback", redirect: "http://127.0.0.1:5173/api/integrations/slack/oauth/callback", available: false},
		{name: "ipv6 loopback", redirect: "http://[::1]:5173/api/integrations/slack/oauth/callback", available: false},
		{name: "custom scheme", redirect: "myapp://oauth/callback", available: false},
		{name: "web redirect", redirect: "https://example.test/api/integrations/slack/oauth/callback", available: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			available, blocker := slackBotOAuthAvailability(tt.redirect)
			if available != tt.available {
				t.Fatalf("slackBotOAuthAvailability(%q) available=%v, want %v", tt.redirect, available, tt.available)
			}
			if !available && strings.TrimSpace(blocker) == "" {
				t.Fatalf("expected blocker message for %q", tt.redirect)
			}
			if available && strings.TrimSpace(blocker) != "" {
				t.Fatalf("expected no blocker for %q, got %q", tt.redirect, blocker)
			}
		})
	}
}

func TestOAuthStartOmitsBotScopesForLocalhostPKCE(t *testing.T) {
	m := NewManager(newSlackTestSettings(t, "123.456"), newUnlockedSlackSecretsStore(t))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5173/api/integrations/slack/oauth/start", nil)
	rec := httptest.NewRecorder()

	m.handleOAuthStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleOAuthStart status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		OK                 bool     `json:"ok"`
		AuthorizeURL       string   `json:"authorize_url"`
		RequestedBotScopes []string `json:"requested_bot_scopes"`
		BotOAuthAvailable  bool     `json:"bot_oauth_available"`
		BotOAuthBlocker    any      `json:"bot_oauth_blocker"`
		ExpiresInSeconds   int      `json:"expires_in_seconds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.OK || body.AuthorizeURL == "" || body.ExpiresInSeconds == 0 {
		t.Fatalf("unexpected OAuth start response: %#v", body)
	}
	if body.BotOAuthAvailable {
		t.Fatalf("localhost PKCE should not report bot OAuth available: %#v", body)
	}
	if len(body.RequestedBotScopes) != 0 {
		t.Fatalf("localhost PKCE requested bot scopes: %#v", body.RequestedBotScopes)
	}
	if body.BotOAuthBlocker == nil {
		t.Fatalf("expected localhost PKCE bot blocker message")
	}
	authURL, err := url.Parse(body.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize_url: %v", err)
	}
	if values, ok := authURL.Query()["scope"]; ok {
		t.Fatalf("authorize_url included bot scope parameter %#v, want no scope parameter for localhost PKCE", values)
	}
	if got := authURL.Query().Get("user_scope"); !strings.Contains(got, "channels:read") || !strings.Contains(got, "chat:write") {
		t.Fatalf("authorize_url user_scope=%q, want user read/write scopes", got)
	}
}

func TestOAuthStartRequestsBotScopesForWebRedirect(t *testing.T) {
	m := NewManager(newSlackTestSettings(t, "123.456"), newUnlockedSlackSecretsStore(t))
	req := httptest.NewRequest(http.MethodPost, "https://example.test/api/integrations/slack/oauth/start", nil)
	rec := httptest.NewRecorder()

	m.handleOAuthStart(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("handleOAuthStart status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		AuthorizeURL       string   `json:"authorize_url"`
		RequestedBotScopes []string `json:"requested_bot_scopes"`
		BotOAuthAvailable  bool     `json:"bot_oauth_available"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.BotOAuthAvailable {
		t.Fatalf("web redirect should report bot OAuth available: %#v", body)
	}
	if strings.Join(body.RequestedBotScopes, ",") != strings.Join(defaultBotScopes, ",") {
		t.Fatalf("requested_bot_scopes=%#v, want %#v", body.RequestedBotScopes, defaultBotScopes)
	}
	authURL, err := url.Parse(body.AuthorizeURL)
	if err != nil {
		t.Fatalf("parse authorize_url: %v", err)
	}
	if got := authURL.Query().Get("scope"); got != strings.Join(defaultBotScopes, ",") {
		t.Fatalf("authorize_url scope=%q, want bot scopes", got)
	}
}

func TestExchangeAuthorizationCodeUsesPublicPKCEForm(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth.v2.access" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
			t.Fatalf("PKCE exchange should not send Authorization header, got %q", auth)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = cloneValues(r.PostForm)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"team": map[string]any{
				"id":   "T123",
				"name": "Example",
			},
			"authed_user": map[string]any{
				"id":           "U123",
				"access_token": "xoxp-test",
				"token_type":   "user",
				"scope":        "team:read",
			},
		})
	}))
	t.Cleanup(srv.Close)

	oldAPIBaseURL := apiBaseURL
	apiBaseURL = srv.URL + "/api"
	t.Cleanup(func() { apiBaseURL = oldAPIBaseURL })

	m := NewManager(nil, nil)
	_, err := m.exchangeAuthorizationCode(context.Background(), &oauthPendingFlow{
		ClientID:     "client-id",
		RedirectURI:  "http://localhost:5173/api/integrations/slack/oauth/callback",
		CodeVerifier: "verifier",
	}, "code")
	if err != nil {
		t.Fatalf("exchangeAuthorizationCode: %v", err)
	}
	if gotForm.Get("client_id") != "client-id" {
		t.Fatalf("PKCE exchange sent client_id=%q, want public client ID", gotForm.Get("client_id"))
	}
	if gotForm.Get("grant_type") != "" {
		t.Fatalf("PKCE exchange sent grant_type=%q, want omitted", gotForm.Get("grant_type"))
	}
	if gotForm.Get("code") != "code" || gotForm.Get("code_verifier") != "verifier" {
		t.Fatalf("unexpected PKCE form: %#v", gotForm)
	}
}

func TestCompleteOAuthFlowAllowsUserOnlyForLocalhostPKCE(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth.v2.access" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"team": map[string]any{
				"id":   "T123",
				"name": "Example",
			},
			"authed_user": map[string]any{
				"id":           "U123",
				"access_token": "xoxp-test",
				"token_type":   "user",
				"scope":        "team:read,chat:write",
			},
		})
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	store := newUnlockedSlackSecretsStore(t)
	m := NewManager(nil, store)
	result := m.completeOAuthFlowWithCode(context.Background(), &oauthPendingFlow{
		ClientID:     "client-id",
		RedirectURI:  "http://localhost:5173/api/integrations/slack/oauth/callback",
		CodeVerifier: "verifier",
		RequestedBot: false,
	}, "code")
	if !result.OK {
		t.Fatalf("completeOAuthFlowWithCode failed: %#v", result)
	}
	if result.Message != "Slack OAuth user profile saved." {
		t.Fatalf("completeOAuthFlowWithCode message=%q, want clean user-profile success", result.Message)
	}
	sec, err := store.GetSlack()
	if err != nil {
		t.Fatalf("GetSlack: %v", err)
	}
	if sec.AccessToken != "xoxp-test" || sec.AuthedUserID != "U123" || sec.BotAccessToken != "" {
		t.Fatalf("unexpected stored Slack secrets: %#v", sec)
	}
}

func TestPostMessageUsesApprovalExecutionPayload(t *testing.T) {
	sec := newSlackTestSecrets(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "chat:write",
		TeamID:      "T123",
	})
	var gotPost map[string]any
	var gotPermalinkQuery url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat.postMessage":
			if auth := r.Header.Get("Authorization"); auth != "Bearer xoxp-test" {
				t.Fatalf("Authorization = %q, want bearer token", auth)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotPost); err != nil {
				t.Fatalf("decode post body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "C123",
				"ts":      "1780000000.000100",
			})
		case "/api/chat.getPermalink":
			gotPermalinkQuery = r.URL.Query()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":        true,
				"permalink": "https://example.slack.com/archives/C123/p1780000000000100",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	m := NewManager(nil, sec)
	result, err := m.PostMessage(context.Background(), PostMessageRequest{
		ChannelID:      "C123",
		Text:           "Hello from approval",
		ThreadTS:       "1780000000.000001",
		ReplyBroadcast: true,
	})
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if gotPost["channel"] != "C123" || gotPost["text"] != "Hello from approval" || gotPost["thread_ts"] != "1780000000.000001" || gotPost["reply_broadcast"] != true {
		t.Fatalf("unexpected chat.postMessage body: %#v", gotPost)
	}
	if gotPermalinkQuery.Get("channel") != "C123" || gotPermalinkQuery.Get("message_ts") != "1780000000.000100" {
		t.Fatalf("unexpected chat.getPermalink query: %#v", gotPermalinkQuery)
	}
	if result["permalink"] != "https://example.slack.com/archives/C123/p1780000000000100" {
		t.Fatalf("permalink = %#v", result["permalink"])
	}
}

func TestRunApprovedOperationExecutesPostMessage(t *testing.T) {
	sec := newSlackTestSecrets(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "chat:write",
		TeamID:      "T123",
	})
	var gotPost map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat.postMessage":
			if err := json.NewDecoder(r.Body).Decode(&gotPost); err != nil {
				t.Fatalf("decode post body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "C123",
				"ts":      "1780000000.000200",
			})
		case "/api/chat.getPermalink":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":        true,
				"permalink": "https://example.slack.com/archives/C123/p1780000000000200",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	m := NewManager(nil, sec)
	result, err := m.RunApprovedOperation(context.Background(), "slack_post_message_execute", map[string]any{
		"channel_id":      "C123",
		"text":            "Hello from approved operation",
		"thread_ts":       "1780000000.000001",
		"reply_broadcast": true,
	})
	if err != nil {
		t.Fatalf("RunApprovedOperation: %v", err)
	}
	if gotPost["channel"] != "C123" || gotPost["text"] != "Hello from approved operation" || gotPost["reply_broadcast"] != true {
		t.Fatalf("unexpected chat.postMessage body: %#v", gotPost)
	}
	if result["ts"] != "1780000000.000200" || result["permalink"] != "https://example.slack.com/archives/C123/p1780000000000200" {
		t.Fatalf("unexpected approved operation result: %#v", result)
	}
	if approvals := m.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("RunApprovedOperation queued approvals: %#v", approvals)
	}
}

func TestPostMessageCanUseBotIdentity(t *testing.T) {
	sec := newSlackTestSecrets(t, secrets.SlackSecrets{
		AccessToken:    "xoxp-user",
		Scope:          "chat:write",
		BotAccessToken: "xoxb-bot",
		BotScope:       "chat:write",
		BotUserID:      "B123",
		TeamID:         "T123",
	})
	var gotPostAuth string
	var gotPermalinkAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/chat.postMessage":
			gotPostAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": "C123",
				"ts":      "1780000000.000100",
			})
		case "/api/chat.getPermalink":
			gotPermalinkAuth = r.Header.Get("Authorization")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":        true,
				"permalink": "https://example.slack.com/archives/C123/p1780000000000100",
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	m := NewManager(nil, sec)
	result, err := m.PostMessage(context.Background(), PostMessageRequest{
		ChannelID: "C123",
		SendAs:    IdentityBot,
		Text:      "Hello from bot",
	})
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if gotPostAuth != "Bearer xoxb-bot" || gotPermalinkAuth != "Bearer xoxb-bot" {
		t.Fatalf("bot identity did not use bot token: post=%q permalink=%q", gotPostAuth, gotPermalinkAuth)
	}
	if result["send_as"] != IdentityBot {
		t.Fatalf("send_as result = %#v, want bot", result["send_as"])
	}
}

func TestSlackSecretsFromOAuthResponseStoresUserAndBotProfiles(t *testing.T) {
	sec, err := slackSecretsFromOAuthResponse(slackOAuthAccessResponse{
		OK:           true,
		AccessToken:  "xoxb-bot",
		TokenType:    "bot",
		Scope:        "chat:write,files:write",
		RefreshToken: "bot-refresh",
		ExpiresIn:    3600,
		BotUserID:    "B123",
		BotID:        "A123",
		Team: struct {
			Name string `json:"name,omitempty"`
			ID   string `json:"id,omitempty"`
		}{Name: "Example", ID: "T123"},
		AuthedUser: struct {
			ID           string `json:"id,omitempty"`
			Scope        string `json:"scope,omitempty"`
			AccessToken  string `json:"access_token,omitempty"`
			TokenType    string `json:"token_type,omitempty"`
			RefreshToken string `json:"refresh_token,omitempty"`
			ExpiresIn    int64  `json:"expires_in,omitempty"`
		}{
			ID:           "U123",
			Scope:        "channels:read,chat:write",
			AccessToken:  "xoxp-user",
			TokenType:    "user",
			RefreshToken: "user-refresh",
			ExpiresIn:    3600,
		},
	}, secrets.SlackSecrets{}, "")
	if err != nil {
		t.Fatalf("slackSecretsFromOAuthResponse: %v", err)
	}
	if sec.AccessToken != "xoxp-user" || sec.AuthedUserID != "U123" || sec.Scope != "channels:read,chat:write" {
		t.Fatalf("user profile not stored: %#v", sec)
	}
	if sec.BotAccessToken != "xoxb-bot" || sec.BotUserID != "B123" || sec.BotID != "A123" || sec.BotScope != "chat:write,files:write" {
		t.Fatalf("bot profile not stored: %#v", sec)
	}
	if sec.TeamID != "T123" || sec.TeamName != "Example" {
		t.Fatalf("workspace metadata not stored: %#v", sec)
	}
}

func TestUploadFileUsesExternalUploadFlowAndReturnsDownloadInfo(t *testing.T) {
	sec := newSlackTestSecrets(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "files:write,files:read",
		TeamID:      "T123",
	})
	var gotTicket map[string]any
	var gotUploaded []byte
	var gotComplete map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files.getUploadURLExternal":
			if auth := r.Header.Get("Authorization"); auth != "Bearer xoxp-test" {
				t.Fatalf("Authorization = %q, want bearer token", auth)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotTicket); err != nil {
				t.Fatalf("decode upload ticket body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"upload_url": srvURL(r) + "/upload",
				"file_id":    "F123",
			})
		case "/upload":
			if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
				t.Fatalf("raw upload should not send Slack bearer auth, got %q", auth)
			}
			gotUploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		case "/api/files.completeUploadExternal":
			if err := json.NewDecoder(r.Body).Decode(&gotComplete); err != nil {
				t.Fatalf("decode complete body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "files": []any{map[string]any{"id": "F123"}}})
		case "/api/files.info":
			if r.URL.Query().Get("file") != "F123" {
				t.Fatalf("files.info query = %#v", r.URL.Query())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id":                   "F123",
					"name":                 "hello.txt",
					"permalink":            "https://example.slack.com/files/F123/hello.txt",
					"url_private_download": "https://files.slack.com/files-pri/T123-F123/download/hello.txt",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	m := NewManager(nil, sec)
	result, err := m.UploadFile(context.Background(), UploadFileRequest{
		ChannelID:      "C123",
		FileName:       "hello.txt",
		Title:          "Hello",
		MimeType:       "text/plain",
		InitialComment: "Please review",
		ThreadTS:       "1780000000.000001",
		ContentBase64:  "aGVsbG8=",
		SHA256:         "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
		Source:         "thread_attachment",
	})
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if gotTicket["filename"] != "hello.txt" || gotTicket["length"] != float64(5) {
		t.Fatalf("unexpected files.getUploadURLExternal body: %#v", gotTicket)
	}
	if !bytes.Equal(gotUploaded, []byte("hello")) {
		t.Fatalf("uploaded bytes = %q, want hello", string(gotUploaded))
	}
	if gotComplete["channel_id"] != "C123" || gotComplete["initial_comment"] != "Please review" || gotComplete["thread_ts"] != "1780000000.000001" {
		t.Fatalf("unexpected files.completeUploadExternal body: %#v", gotComplete)
	}
	download, ok := result["download"].(map[string]any)
	if !ok || download["url_private_download"] == "" || download["permalink"] == "" {
		t.Fatalf("download metadata missing: %#v", result["download"])
	}
	if result["source"] != "thread_attachment" || result["sha256"] == nil {
		t.Fatalf("source/hash metadata missing: %#v", result)
	}
}

func TestRunApprovedOperationExecutesUploadFile(t *testing.T) {
	sec := newSlackTestSecrets(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "files:write,files:read",
		TeamID:      "T123",
	})
	var gotTicket map[string]any
	var gotUploaded []byte
	var gotComplete map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files.getUploadURLExternal":
			if err := json.NewDecoder(r.Body).Decode(&gotTicket); err != nil {
				t.Fatalf("decode upload ticket body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":         true,
				"upload_url": srvURL(r) + "/upload",
				"file_id":    "F123",
			})
		case "/upload":
			gotUploaded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		case "/api/files.completeUploadExternal":
			if err := json.NewDecoder(r.Body).Decode(&gotComplete); err != nil {
				t.Fatalf("decode complete body: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "files": []any{map[string]any{"id": "F123"}}})
		case "/api/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id":                   "F123",
					"name":                 "approved.txt",
					"permalink":            "https://example.slack.com/files/F123/approved.txt",
					"url_private_download": "https://files.slack.com/files-pri/T123-F123/download/approved.txt",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	m := NewManager(nil, sec)
	result, err := m.RunApprovedOperation(context.Background(), "slack_upload_file_execute", map[string]any{
		"channel_id":      "C123",
		"file_name":       "approved.txt",
		"title":           "Approved",
		"mime_type":       "text/plain",
		"initial_comment": "Please review",
		"content_base64":  "YXBwcm92ZWQ=",
		"source":          "inline",
		"source_ref":      "inline",
	})
	if err != nil {
		t.Fatalf("RunApprovedOperation: %v", err)
	}
	if gotTicket["filename"] != "approved.txt" || gotTicket["length"] != float64(len("approved")) {
		t.Fatalf("unexpected files.getUploadURLExternal body: %#v", gotTicket)
	}
	if !bytes.Equal(gotUploaded, []byte("approved")) {
		t.Fatalf("uploaded bytes=%q, want approved", string(gotUploaded))
	}
	if gotComplete["channel_id"] != "C123" || gotComplete["initial_comment"] != "Please review" {
		t.Fatalf("unexpected files.completeUploadExternal body: %#v", gotComplete)
	}
	if result["file_id"] != "F123" || result["source"] != "inline" {
		t.Fatalf("unexpected approved upload result: %#v", result)
	}
	if approvals := m.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("RunApprovedOperation queued approvals: %#v", approvals)
	}
}

func TestDownloadFileSavesManagedDownloadDescriptor(t *testing.T) {
	sec := newSlackTestSecrets(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "files:read",
		TeamID:      "T123",
	})
	cfgDir := t.TempDir()
	var sawDownloadAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files.info":
			if auth := r.Header.Get("Authorization"); auth != "Bearer xoxp-test" {
				t.Fatalf("files.info Authorization = %q, want bearer token", auth)
			}
			if r.URL.Query().Get("file") != "F123" {
				t.Fatalf("files.info query = %#v", r.URL.Query())
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id":                   "F123",
					"name":                 "report.csv",
					"title":                "Report",
					"mimetype":             "text/csv",
					"url_private_download": srvURL(r) + "/download/F123",
				},
			})
		case "/download/F123":
			sawDownloadAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/csv")
			_, _ = w.Write([]byte("region,revenue\nNorth,10\n"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	m := NewManager(nil, sec)
	result, err := m.DownloadFile(context.Background(), cfgDir, DownloadFileRequest{FileID: "F123"})
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if sawDownloadAuth != "Bearer xoxp-test" {
		t.Fatalf("download Authorization = %q, want bearer token", sawDownloadAuth)
	}
	if result["local_path"] != "slack/downloads/F123/report.csv" || result["localPath"] != "slack/downloads/F123/report.csv" {
		t.Fatalf("unexpected local path aliases: %#v", result)
	}
	if result["mime_type"] != "text/csv" || result["file_name"] != "report.csv" {
		t.Fatalf("unexpected file metadata: %#v", result)
	}
	download, ok := result["download"].(map[string]any)
	if !ok || download["source"] != "slack_managed_download" {
		t.Fatalf("download descriptor missing: %#v", result["download"])
	}
	raw, err := os.ReadFile(filepath.Join(cfgDir, filepath.FromSlash("slack/downloads/F123/report.csv")))
	if err != nil {
		t.Fatalf("ReadFile managed download: %v", err)
	}
	if string(raw) != "region,revenue\nNorth,10\n" {
		t.Fatalf("managed download bytes=%q", string(raw))
	}
}

func TestDownloadFileEnforcesManagedDownloadCap(t *testing.T) {
	sec := newSlackTestSecrets(t, secrets.SlackSecrets{
		AccessToken: "xoxp-test",
		Scope:       "files:read",
		TeamID:      "T123",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/files.info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"file": map[string]any{
					"id":                   "F123",
					"name":                 "large.txt",
					"mimetype":             "text/plain",
					"url_private_download": srvURL(r) + "/download/F123",
				},
			})
		case "/download/F123":
			_, _ = w.Write([]byte("too large"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	withSlackAPIBaseURL(t, srv.URL+"/api")

	m := NewManager(nil, sec)
	_, err := m.DownloadFile(context.Background(), t.TempDir(), DownloadFileRequest{FileID: "F123", MaxDownloadBytes: 4})
	if err == nil || !strings.Contains(err.Error(), "managed download cap") {
		t.Fatalf("DownloadFile error=%v, want cap error", err)
	}
}

func TestPersistSlackHealthDisablesToolsOnSignOut(t *testing.T) {
	settingsStore := newSlackTestSettings(t, "client-id")
	settings := settingsStore.Get()
	on := true
	settings.Slack.Enabled = &on
	settings.Slack.ReadTools = &on
	settings.Slack.WriteTools = &on
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	m := NewManager(settingsStore, nil)
	checkedAt, ok, message := m.persistSlackHealth(false, "Signed out of Slack.", true)
	if ok || checkedAt == 0 || !strings.Contains(message, "Signed out") {
		t.Fatalf("persistSlackHealth returned checkedAt=%d ok=%v message=%q", checkedAt, ok, message)
	}

	got := settingsStore.Get()
	if got.Slack == nil || got.Slack.Enabled == nil || *got.Slack.Enabled || got.Slack.ReadTools == nil || *got.Slack.ReadTools || got.Slack.WriteTools == nil || *got.Slack.WriteTools {
		t.Fatalf("Slack tools were not disabled: %#v", got.Slack)
	}
	if got.Slack.LastCheckOk == nil || *got.Slack.LastCheckOk || got.Slack.LastCheckedAt == nil || *got.Slack.LastCheckedAt != checkedAt {
		t.Fatalf("Slack health was not persisted: %#v", got.Slack)
	}
}

func newSlackTestSecrets(t *testing.T, sec secrets.SlackSecrets) *secrets.Store {
	t.Helper()
	store := newUnlockedSlackSecretsStore(t)
	if err := store.SetSlackOAuth(sec); err != nil {
		t.Fatalf("SetSlackOAuth: %v", err)
	}
	return store
}

func newUnlockedSlackSecretsStore(t *testing.T) *secrets.Store {
	t.Helper()
	store, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Init("passphrase"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := store.Unlock("passphrase"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	return store
}

func newSlackTestSettings(t *testing.T, clientID string) *config.Store {
	t.Helper()
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	clientID = strings.TrimSpace(clientID)
	settings.SlackOAuthClientID = &clientID
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	return store
}

func withSlackAPIBaseURL(t *testing.T, base string) {
	t.Helper()
	oldAPIBaseURL := apiBaseURL
	apiBaseURL = base
	t.Cleanup(func() { apiBaseURL = oldAPIBaseURL })
}

func srvURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}
