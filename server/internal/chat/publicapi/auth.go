package publicapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"ai-analyst-local/internal/config"
)

const (
	ScopeRunsCreate       = "runs:create"
	ScopeRunsRead         = "runs:read"
	ScopeRunsCancel       = "runs:cancel"
	ScopeThreadsRead      = "threads:read"
	ScopeThreadsWrite     = "threads:write"
	ScopeArtifactsRead    = "artifacts:read"
	ScopeAttachmentsWrite = "attachments:write"
	ScopeQueriesRead      = "queries:read"
	ScopeQueriesCancel    = "queries:cancel"
)

type AuthInfo struct {
	KeyID             string
	Name              string
	Scopes            []string
	AllowedThreadIDs  []string
	AllowedProjectIDs []string
}

type authContextKey struct{}

func hashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func tokenPrefix(token string) string {
	token = strings.TrimSpace(token)
	if len(token) <= 12 {
		return token
	}
	return token[:12]
}

func authenticateAPIKey(settings *config.Store, authorization string, requiredScopes ...string) (AuthInfo, error) {
	if settings == nil {
		return AuthInfo{}, apiError(http.StatusServiceUnavailable, "public_api_not_configured", "settings store is not configured")
	}
	parts := strings.Fields(strings.TrimSpace(authorization))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return AuthInfo{}, apiError(http.StatusUnauthorized, "authentication_required", "missing bearer token")
	}
	token := strings.TrimSpace(parts[1])
	if token == "" {
		return AuthInfo{}, apiError(http.StatusUnauthorized, "authentication_required", "missing bearer token")
	}

	cfg := settings.Get()
	if cfg.ExternalAPI == nil || len(cfg.ExternalAPI.APIKeys) == 0 {
		return AuthInfo{}, apiError(http.StatusUnauthorized, "authentication_required", "no public API keys are configured")
	}
	tokenHash := hashAPIKey(token)
	prefix := tokenPrefix(token)
	for _, key := range cfg.ExternalAPI.APIKeys {
		if strings.TrimSpace(key.Prefix) != "" && strings.TrimSpace(key.Prefix) != prefix {
			continue
		}
		storedHash := strings.ToLower(strings.TrimSpace(key.Hash))
		if storedHash == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(storedHash), []byte(tokenHash)) != 1 {
			continue
		}
		if key.RevokedAt != nil && *key.RevokedAt > 0 {
			return AuthInfo{}, apiError(http.StatusUnauthorized, "authentication_required", "API key is revoked")
		}
		if !hasRequiredScopes(key.Scopes, requiredScopes) {
			return AuthInfo{}, apiError(http.StatusForbidden, "permission_denied", "API key is missing required scope")
		}
		updateAPIKeyLastUsed(settings, key.ID)
		return AuthInfo{
			KeyID:             strings.TrimSpace(key.ID),
			Name:              strings.TrimSpace(key.Name),
			Scopes:            append([]string(nil), key.Scopes...),
			AllowedThreadIDs:  append([]string(nil), key.AllowedThreadIDs...),
			AllowedProjectIDs: append([]string(nil), key.AllowedProjectIDs...),
		}, nil
	}
	return AuthInfo{}, apiError(http.StatusUnauthorized, "authentication_required", "invalid bearer token")
}

func withAuthInfo(ctx context.Context, auth AuthInfo) context.Context {
	return context.WithValue(ctx, authContextKey{}, auth)
}

func authInfoFromContext(ctx context.Context) AuthInfo {
	auth, _ := ctx.Value(authContextKey{}).(AuthInfo)
	return auth
}

func hasRequiredScopes(have []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := map[string]struct{}{}
	for _, scope := range have {
		scope = strings.ToLower(strings.TrimSpace(scope))
		if scope == "" {
			continue
		}
		set[scope] = struct{}{}
		if scope == "*" {
			return true
		}
	}
	for _, scope := range required {
		if _, ok := set[strings.ToLower(strings.TrimSpace(scope))]; !ok {
			return false
		}
	}
	return true
}

func updateAPIKeyLastUsed(settings *config.Store, keyID string) {
	if settings == nil || strings.TrimSpace(keyID) == "" {
		return
	}
	next := settings.Get()
	if next.ExternalAPI == nil {
		return
	}
	now := time.Now().UnixMilli()
	changed := false
	for i := range next.ExternalAPI.APIKeys {
		if strings.TrimSpace(next.ExternalAPI.APIKeys[i].ID) != strings.TrimSpace(keyID) {
			continue
		}
		next.ExternalAPI.APIKeys[i].LastUsedAt = &now
		changed = true
		break
	}
	if changed {
		_ = settings.Save(next)
	}
}
