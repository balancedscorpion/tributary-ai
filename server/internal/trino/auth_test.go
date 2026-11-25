package trino

import (
	"strings"
	"testing"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestNormalizeTrinoHostRules(t *testing.T) {
	if _, err := normalizeTrinoHost("http://trino.local", "basic"); err == nil {
		t.Fatalf("expected http host to be rejected for basic auth")
	}
	if got, err := normalizeTrinoHost("http://trino.local", "none"); err != nil || got != "http://trino.local" {
		t.Fatalf("normalizeTrinoHost(http, none) = %q, %v; want http://trino.local, nil", got, err)
	}
	if got, err := normalizeTrinoHost("trino.example.com", "jwt"); err != nil || got != "https://trino.example.com" {
		t.Fatalf("normalizeTrinoHost(no scheme, jwt) = %q, %v; want https://trino.example.com, nil", got, err)
	}
}

func TestAuthConfigBasicUsesSecretUserAndDefaultSource(t *testing.T) {
	h, settings, sec := newAuthTestHandler(t)
	cfg := settings.Get()
	cfg.TrinoHost = strPtr("https://trino.example.com")
	if err := settings.Save(cfg); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	if err := sec.SetTrinoBasicAuth("svc-user", "svc-pass"); err != nil {
		t.Fatalf("SetTrinoBasicAuth: %v", err)
	}

	auth, err := h.authConfig(authRequestOverrides{})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	if auth.Mode != "basic" {
		t.Fatalf("Mode = %q, want basic", auth.Mode)
	}
	if auth.User != "svc-user" {
		t.Fatalf("User = %q, want svc-user", auth.User)
	}
	if !strings.HasPrefix(auth.Authorization, "Basic ") {
		t.Fatalf("Authorization = %q, want Basic prefix", auth.Authorization)
	}
	if auth.Source != defaultTrinoSource {
		t.Fatalf("Source = %q, want %q", auth.Source, defaultTrinoSource)
	}
}

func TestAuthConfigJWTRequiresSettingsUser(t *testing.T) {
	h, settings, sec := newAuthTestHandler(t)
	cfg := settings.Get()
	cfg.TrinoHost = strPtr("https://trino.example.com")
	cfg.TrinoUser = nil
	if err := settings.Save(cfg); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	if err := sec.SetTrinoJWT("token-1"); err != nil {
		t.Fatalf("SetTrinoJWT: %v", err)
	}

	_, err := h.authConfig(authRequestOverrides{})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "trinouser") {
		t.Fatalf("authConfig error = %v, want missing trinoUser message", err)
	}
}

func TestAuthConfigNoneAllowsHTTP(t *testing.T) {
	h, settings, sec := newAuthTestHandler(t)
	cfg := settings.Get()
	cfg.TrinoHost = strPtr("http://127.0.0.1:8080")
	cfg.TrinoUser = strPtr("alice")
	if err := settings.Save(cfg); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	if err := sec.SetTrinoNone(); err != nil {
		t.Fatalf("SetTrinoNone: %v", err)
	}

	auth, err := h.authConfig(authRequestOverrides{})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	if auth.Mode != "none" {
		t.Fatalf("Mode = %q, want none", auth.Mode)
	}
	if auth.BaseURL != "http://127.0.0.1:8080" {
		t.Fatalf("BaseURL = %q, want http://127.0.0.1:8080", auth.BaseURL)
	}
	if auth.Authorization != "" {
		t.Fatalf("Authorization = %q, want empty", auth.Authorization)
	}
	if auth.User != "alice" {
		t.Fatalf("User = %q, want alice", auth.User)
	}
}

func newAuthTestHandler(t *testing.T) (*Handler, *config.Store, *secrets.Store) {
	t.Helper()
	dir := t.TempDir()
	settings, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	sec, err := secrets.NewStore(dir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("passphrase"); err != nil {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("passphrase"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	return NewHandler(settings, sec), settings, sec
}

func strPtr(v string) *string {
	s := v
	return &s
}
