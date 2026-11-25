package trino

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"ai-analyst-local/internal/secrets"
)

const defaultTrinoSource = "ai-analyst"

type authRequestOverrides struct {
	Host    string
	Catalog string
	Schema  string
	Source  string
}

type trinoSettingsSnapshot struct {
	Host    string
	Catalog string
	Schema  string
	User    string
	Source  string
}

type trinoAuthConfig struct {
	Mode          string
	BaseURL       string
	User          string
	Authorization string
	Catalog       string
	Schema        string
	Source        string
}

func (h *Handler) settingsSnapshot() trinoSettingsSnapshot {
	out := trinoSettingsSnapshot{}
	if h == nil || h.Settings == nil {
		return out
	}
	s := h.Settings.Get()
	if s.TrinoHost != nil {
		out.Host = strings.TrimSpace(*s.TrinoHost)
	}
	if s.TrinoCatalog != nil {
		out.Catalog = strings.TrimSpace(*s.TrinoCatalog)
	}
	if s.TrinoSchema != nil {
		out.Schema = strings.TrimSpace(*s.TrinoSchema)
	}
	if s.TrinoUser != nil {
		out.User = strings.TrimSpace(*s.TrinoUser)
	}
	if s.TrinoSource != nil {
		out.Source = strings.TrimSpace(*s.TrinoSource)
	}
	return out
}

func (h *Handler) statusAuthMode() (string, *secrets.TrinoSecrets, error) {
	if h == nil || h.Secrets == nil {
		mode := "none"
		return mode, &secrets.TrinoSecrets{Mode: mode}, nil
	}
	sec, err := h.Secrets.GetTrino()
	if err != nil {
		return "none", nil, err
	}
	mode := strings.TrimSpace(strings.ToLower(sec.Mode))
	switch mode {
	case "basic", "jwt", "none":
		return mode, &sec, nil
	default:
		return "", nil, httpError{status: http.StatusBadRequest, msg: "invalid Trino auth mode in secrets store"}
	}
}

func (h *Handler) authConfig(overrides authRequestOverrides) (trinoAuthConfig, error) {
	settings := h.settingsSnapshot()
	authMode, sec, err := h.statusAuthMode()
	if err != nil {
		return trinoAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: err.Error()}
	}
	host := firstNonEmpty(overrides.Host, settings.Host)
	baseURL, err := normalizeTrinoHost(host, authMode)
	if err != nil {
		return trinoAuthConfig{}, err
	}

	catalog := firstNonEmpty(overrides.Catalog, settings.Catalog)
	schema := firstNonEmpty(overrides.Schema, settings.Schema)
	source := firstNonEmpty(overrides.Source, settings.Source)
	if source == "" {
		source = defaultTrinoSource
	}

	cfg := trinoAuthConfig{
		Mode:    authMode,
		BaseURL: baseURL,
		Catalog: catalog,
		Schema:  schema,
		Source:  source,
	}

	switch authMode {
	case "basic":
		if sec == nil {
			return trinoAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: "Trino credentials are unavailable"}
		}
		username := strings.TrimSpace(sec.Username)
		password := strings.TrimSpace(sec.Password)
		if username == "" || password == "" {
			return trinoAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: "Trino basic auth credentials are not configured"}
		}
		cfg.User = username
		cfg.Authorization = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	case "jwt":
		if sec == nil {
			return trinoAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: "Trino credentials are unavailable"}
		}
		token := strings.TrimSpace(sec.JWT)
		if token == "" {
			return trinoAuthConfig{}, httpError{status: http.StatusUnauthorized, msg: "Trino JWT token is not configured"}
		}
		if settings.User == "" {
			return trinoAuthConfig{}, httpError{status: http.StatusBadRequest, msg: "Missing trinoUser in settings"}
		}
		cfg.User = settings.User
		cfg.Authorization = "Bearer " + token
	case "none":
		if settings.User == "" {
			return trinoAuthConfig{}, httpError{status: http.StatusBadRequest, msg: "Missing trinoUser in settings for authType=none"}
		}
		cfg.User = settings.User
	default:
		return trinoAuthConfig{}, httpError{status: http.StatusBadRequest, msg: "invalid Trino auth mode"}
	}

	return cfg, nil
}

func normalizeTrinoHost(rawHost, authMode string) (string, error) {
	raw := strings.TrimSpace(rawHost)
	if raw == "" {
		return "", httpError{status: http.StatusBadRequest, msg: "Missing trinoHost in settings"}
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(u.Scheme) == "" || strings.TrimSpace(u.Host) == "" {
		return "", httpError{status: http.StatusBadRequest, msg: "Invalid Trino host"}
	}
	if strings.TrimSpace(strings.Trim(u.Path, "/")) != "" {
		return "", httpError{status: http.StatusBadRequest, msg: "Trino host must not include a path"}
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	mode := strings.ToLower(strings.TrimSpace(authMode))
	if mode != "none" && scheme != "https" {
		return "", httpError{status: http.StatusBadRequest, msg: "Trino host must use https for basic/jwt auth"}
	}
	if mode == "none" && scheme != "https" && scheme != "http" {
		return "", httpError{status: http.StatusBadRequest, msg: "Trino host must use http or https"}
	}
	return scheme + "://" + u.Host, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
