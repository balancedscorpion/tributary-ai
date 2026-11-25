package server

import (
	"context"
	"net/http"
	"strings"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/capabilityruntime"
	chatstore "ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/permissions"
)

type providerCapabilityResolveRequest struct {
	Config         *config.ProviderConfig `json:"config,omitempty"`
	ProviderConfig *config.ProviderConfig `json:"providerConfig,omitempty"`
	Model          string                 `json:"model,omitempty"`
}

func handleProviderCapabilityResolve(w http.ResponseWriter, r *http.Request, opts HandlerOpts) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body providerCapabilityResolveRequest
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "message": err.Error()})
		return
	}
	cfg := body.Config
	if cfg == nil {
		cfg = body.ProviderConfig
	}
	if cfg == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider config is required", "message": "provider config is required"})
		return
	}
	if model := strings.TrimSpace(body.Model); model != "" {
		copy := *cfg
		copy.Model = model
		cfg = &copy
	}
	report, err := resolveProviderCapabilityReport(r.Context(), *cfg, nil, opts.Permissions, opts.ChatStore)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func handleProviderConnectionCapabilities(w http.ResponseWriter, r *http.Request, opts HandlerOpts) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if opts.Settings == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "settings store is unavailable"})
		return
	}
	connectionID := strings.TrimSpace(r.PathValue("connectionId"))
	var cfg *config.ProviderConfig
	settings := opts.Settings.Get()
	for i := range settings.ProviderConfigs {
		if strings.TrimSpace(settings.ProviderConfigs[i].ID) == connectionID {
			copy := settings.ProviderConfigs[i]
			cfg = &copy
			break
		}
	}
	if cfg == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider connection not found", "message": "provider connection not found"})
		return
	}
	if model := strings.TrimSpace(r.URL.Query().Get("model")); model != "" {
		cfg.Model = model
	}
	report, err := resolveProviderCapabilityReport(r.Context(), *cfg, &connectionID, opts.Permissions, opts.ChatStore)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func resolveProviderCapabilityReport(
	ctx context.Context,
	cfg config.ProviderConfig,
	connectionID *string,
	permissionService *permissions.Service,
	evidenceStore chatstore.Store,
) (capabilities.CapabilityReport, error) {
	return capabilityruntime.NewService(evidenceStore, permissionService).Resolve(ctx, capabilityruntime.ResolveInput{
		Config: cfg, ConnectionID: connectionID,
	})
}
