package proxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

type Handler struct {
	Settings *config.Store
	Secrets  *secrets.Store

	// Optional override for upstream HTTP client.
	Client *http.Client
}

func NewHandler(settings *config.Store, secretsStore *secrets.Store) *Handler {
	return &Handler{
		Settings: settings,
		Secrets:  secretsStore,
		Client: &http.Client{
			Timeout: 0, // streaming: rely on request context cancellation
		},
	}
}

const (
	hTargetURL  = "X-AI-Proxy-Target"
	hProviderID = "X-AI-Proxy-Provider"
	hProfileID  = "X-AI-Proxy-Profile"
)

var hopByHop = map[string]bool{
	"connection":        true,
	"proxy-connection":  true,
	"keep-alive":        true,
	"te":                true,
	"trailer":           true,
	"transfer-encoding": true,
	"upgrade":           true,
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	targetRaw := strings.TrimSpace(r.Header.Get(hTargetURL))
	providerID := strings.TrimSpace(r.Header.Get(hProviderID))
	profileID := strings.TrimSpace(r.Header.Get(hProfileID))

	if targetRaw == "" || providerID == "" {
		http.Error(w, "missing proxy headers", http.StatusBadRequest)
		return
	}

	targetURL, err := url.Parse(targetRaw)
	if err != nil || targetURL.Scheme == "" || targetURL.Host == "" {
		http.Error(w, "invalid target url", http.StatusBadRequest)
		return
	}

	// Validate target URL against configured base URLs to prevent SSRF.
	settings := h.Settings.Get()
	if err := validateTarget(settings, providerID, profileID, targetURL); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL.String(), r.Body)
	if err != nil {
		http.Error(w, "failed to build upstream request", http.StatusInternalServerError)
		return
	}

	copyRequestHeaders(upReq.Header, r.Header)
	stripProxyHeaders(upReq.Header)

	// Inject auth headers from the encrypted secrets store.
	if err := h.injectAuth(upReq, providerID, profileID, settings); err != nil {
		if isLockedErr(err) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":"LOCKED","message":"Secrets store is locked. Unlock in Settings > Security."}`)
			return
		}
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	resp, err := h.Client.Do(upReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("upstream request failed: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	// Stream the body.
	if f, ok := w.(http.Flusher); ok {
		buf := make([]byte, 32*1024)
		for {
			n, rerr := resp.Body.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				f.Flush()
			}
			if rerr != nil {
				if errors.Is(rerr, io.EOF) {
					return
				}
				return
			}
		}
	}
	_, _ = io.Copy(w, resp.Body)
}

func isLockedErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "locked")
}

func stripProxyHeaders(hdr http.Header) {
	hdr.Del(hTargetURL)
	hdr.Del(hProviderID)
	hdr.Del(hProfileID)
}

func copyRequestHeaders(dst http.Header, src http.Header) {
	for k, vals := range src {
		lk := strings.ToLower(k)
		if hopByHop[lk] {
			continue
		}
		if lk == "host" || lk == "origin" || lk == "referer" || lk == "cookie" {
			continue
		}
		// Internal proxy headers are stripped later.
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
	// Avoid upstream compression for SSE; keeps streaming predictable.
	dst.Del("Accept-Encoding")
}

func copyResponseHeaders(dst http.Header, src http.Header) {
	for k, vals := range src {
		lk := strings.ToLower(k)
		if hopByHop[lk] {
			continue
		}
		if lk == "set-cookie" {
			// Never set cookies from upstream providers.
			continue
		}
		for _, v := range vals {
			dst.Add(k, v)
		}
	}
}

func validateTarget(settings config.AppSettings, providerID string, profileID string, target *url.URL) error {
	// Tight allowlist based on configured base URLs.
	getBase := func(raw string) (*url.URL, error) {
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("invalid base url: %s", raw)
		}
		// Normalize path prefix.
		if !strings.HasPrefix(u.Path, "/") {
			u.Path = "/" + u.Path
		}
		u.Path = strings.TrimRight(u.Path, "/")
		return u, nil
	}

	matches := func(base *url.URL) bool {
		if !strings.EqualFold(base.Scheme, target.Scheme) {
			return false
		}
		if !strings.EqualFold(base.Host, target.Host) {
			return false
		}
		basePath := strings.TrimRight(base.Path, "/")
		targetPath := target.Path
		if basePath == "" || basePath == "/" {
			return true
		}
		return strings.HasPrefix(targetPath, basePath+"/") || targetPath == basePath
	}

	switch providerID {
	case "openai":
		base := "https://api.openai.com/v1"
		if settings.OpenAI != nil && settings.OpenAI.BaseURL != nil && strings.TrimSpace(*settings.OpenAI.BaseURL) != "" {
			base = *settings.OpenAI.BaseURL
		}
		u, err := getBase(base)
		if err != nil {
			return err
		}
		if !matches(u) {
			return errors.New("target url is not allowed for openai provider")
		}
		return nil
	case "anthropic":
		base := "https://api.anthropic.com"
		if settings.Anthropic != nil && settings.Anthropic.BaseURL != nil && strings.TrimSpace(*settings.Anthropic.BaseURL) != "" {
			base = *settings.Anthropic.BaseURL
		}
		u, err := getBase(base)
		if err != nil {
			return err
		}
		if !matches(u) {
			return errors.New("target url is not allowed for anthropic provider")
		}
		return nil
	case "gemini":
		base := "https://generativelanguage.googleapis.com"
		if settings.Gemini != nil && settings.Gemini.BaseURL != nil && strings.TrimSpace(*settings.Gemini.BaseURL) != "" {
			base = *settings.Gemini.BaseURL
		}
		u, err := getBase(base)
		if err != nil {
			return err
		}
		if !matches(u) {
			return errors.New("target url is not allowed for gemini provider")
		}
		return nil
	case "compatible":
		if profileID == "" {
			return errors.New("missing profileId for compatible provider")
		}
		var prof *config.CompatibleProfile
		for i := range settings.CompatibleProfiles {
			if settings.CompatibleProfiles[i].ID == profileID {
				prof = &settings.CompatibleProfiles[i]
				break
			}
		}
		if prof == nil {
			return errors.New("unknown compatible profileId")
		}
		u, err := getBase(prof.BaseURL)
		if err != nil {
			return err
		}
		if !matches(u) {
			return errors.New("target url is not allowed for compatible profile")
		}
		return nil
	default:
		return errors.New("unknown providerId")
	}
}

func (h *Handler) injectAuth(req *http.Request, providerID string, profileID string, settings config.AppSettings) error {
	switch providerID {
	case "openai":
		key, err := h.Secrets.GetProviderKey("openai")
		if err != nil {
			return err
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("openai api key is not configured")
		}
		req.Header.Del("Authorization")
		req.Header.Set("Authorization", "Bearer "+key)
		return nil
	case "anthropic":
		key, err := h.Secrets.GetProviderKey("anthropic")
		if err != nil {
			return err
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("anthropic api key is not configured")
		}
		req.Header.Del("x-api-key")
		req.Header.Set("x-api-key", key)
		// Ensure anthropic-version exists; default if missing.
		if strings.TrimSpace(req.Header.Get("anthropic-version")) == "" {
			def := "2023-06-01"
			if settings.Anthropic != nil && settings.Anthropic.AnthropicVersion != nil && strings.TrimSpace(*settings.Anthropic.AnthropicVersion) != "" {
				def = *settings.Anthropic.AnthropicVersion
			}
			req.Header.Set("anthropic-version", def)
		}
		return nil
	case "gemini":
		key, err := h.Secrets.GetProviderKey("gemini")
		if err != nil {
			return err
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return errors.New("gemini api key is not configured")
		}
		req.Header.Del("x-goog-api-key")
		req.Header.Set("x-goog-api-key", key)
		return nil
	case "compatible":
		if profileID == "" {
			return errors.New("missing profileId for compatible provider")
		}
		var prof *config.CompatibleProfile
		for i := range settings.CompatibleProfiles {
			if settings.CompatibleProfiles[i].ID == profileID {
				prof = &settings.CompatibleProfiles[i]
				break
			}
		}
		if prof == nil {
			return errors.New("unknown compatible profileId")
		}

		needsAPIKey := strings.TrimSpace(prof.Auth.Type) == "api_key"
		needsSecretHeaders := compatibleProxyHasSecretHeaders(prof.Extra)
		sec := secrets.CompatibleSecrets{}
		if needsAPIKey || needsSecretHeaders {
			var err error
			sec, err = h.Secrets.GetCompatible(profileID)
			if err != nil {
				return err
			}
		}

		// Inject auth if configured.
		if needsAPIKey {
			key := strings.TrimSpace(sec.APIKey)
			if key == "" {
				return errors.New("compatible profile api key is not configured")
			}
			header := strings.TrimSpace(prof.Auth.Header)
			if header == "" {
				header = "Authorization"
			}
			req.Header.Del(header)
			if prof.Auth.Scheme != nil && strings.TrimSpace(*prof.Auth.Scheme) != "" {
				req.Header.Set(header, strings.TrimSpace(*prof.Auth.Scheme)+" "+key)
			} else {
				req.Header.Set(header, key)
			}
		}

		// Inject secret extra headers.
		if prof.Extra != nil && len(prof.Extra) > 0 {
			for _, kv := range prof.Extra {
				if !kv.IsSecret {
					continue
				}
				k := strings.TrimSpace(kv.Key)
				if k == "" {
					continue
				}
				val := sec.SecretHeaders[k]
				if strings.TrimSpace(val) == "" {
					continue
				}
				req.Header.Set(k, val)
			}
		}
		return nil
	default:
		return errors.New("unknown providerId")
	}
}

func compatibleProxyHasSecretHeaders(headers []config.HeaderKV) bool {
	for _, kv := range headers {
		if kv.IsSecret && strings.TrimSpace(kv.Key) != "" {
			return true
		}
	}
	return false
}

// For long streaming calls, we still want a last-used timestamp to avoid auto-lock mid-stream.
func (h *Handler) TouchSecretsForStreaming() {
	// No-op placeholder; last-used is updated on key retrieval.
	_ = time.Now()
}
