package server

import (
	"net/http"
	"strings"

	"ai-analyst-local/internal/chat/providers/discovery"
	"ai-analyst-local/internal/config"
)

type providerModelDiscoveryRequest struct {
	Config         *config.ProviderConfig `json:"config,omitempty"`
	ProviderConfig *config.ProviderConfig `json:"providerConfig,omitempty"`
}

func handleProviderModelDiscovery(w http.ResponseWriter, r *http.Request, opts HandlerOpts) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if opts.Settings == nil || opts.Secrets == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "settings or secrets store is unavailable"})
		return
	}

	var cfg *config.ProviderConfig
	connectionID := strings.TrimSpace(r.PathValue("connectionId"))
	if connectionID != "" {
		settings := opts.Settings.Get()
		for i := range settings.ProviderConfigs {
			if strings.TrimSpace(settings.ProviderConfigs[i].ID) == connectionID {
				candidate := settings.ProviderConfigs[i]
				cfg = &candidate
				break
			}
		}
		if cfg == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "provider connection not found"})
			return
		}
	} else {
		var body providerModelDiscoveryRequest
		if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		cfg = body.Config
		if cfg == nil {
			cfg = body.ProviderConfig
		}
	}

	if cfg == nil || strings.TrimSpace(cfg.ProviderID) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "provider config is required"})
		return
	}
	service := discovery.NewService(opts.Secrets)
	result := service.Discover(r.Context(), *cfg)
	writeJSON(w, http.StatusOK, map[string]any{"result": result})
}
