package server

import (
	"net/http"
	"strings"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/activation"
)

func activationEnforced(settings *config.Store) bool {
	if settings == nil {
		return true
	}
	flags := settings.Get().FeatureFlags
	if flags == nil {
		return true
	}
	if flags.Activation == nil {
		return true
	}
	return *flags.Activation
}

func activationGate(store *activation.Store, settings *config.Store, next http.Handler) http.Handler {
	if store == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "activation store not configured", http.StatusServiceUnavailable)
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !activationEnforced(settings) {
			next.ServeHTTP(w, r)
			return
		}

		st := store.Status()
		if st.Activated {
			next.ServeHTTP(w, r)
			return
		}
		code := "NOT_ACTIVATED"
		if st.KeyInstalled && strings.Contains(strings.ToLower(st.Error), "expired") {
			code = "EXPIRED"
		} else if st.KeyInstalled && strings.Contains(strings.ToLower(st.Error), "device id") {
			code = "DEVICE_ID_MISMATCH"
		}
		msg := strings.TrimSpace(st.Error)
		if msg == "" {
			msg = "activation required"
		}
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"ok":      false,
			"code":    code,
			"message": msg,
			"status":  st,
		})
	})
}

func activationGateFunc(store *activation.Store, settings *config.Store, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			http.Error(w, "activation store not configured", http.StatusServiceUnavailable)
			return
		}
		if !activationEnforced(settings) {
			fn(w, r)
			return
		}

		st := store.Status()
		if st.Activated {
			fn(w, r)
			return
		}
		code := "NOT_ACTIVATED"
		if st.KeyInstalled && strings.Contains(strings.ToLower(st.Error), "expired") {
			code = "EXPIRED"
		} else if st.KeyInstalled && strings.Contains(strings.ToLower(st.Error), "device id") {
			code = "DEVICE_ID_MISMATCH"
		}
		msg := strings.TrimSpace(st.Error)
		if msg == "" {
			msg = "activation required"
		}
		writeJSON(w, http.StatusPaymentRequired, map[string]any{
			"ok":      false,
			"code":    code,
			"message": msg,
			"status":  st,
		})
	}
}
