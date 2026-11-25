package publicapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

type APIError struct {
	Status  int
	Code    string
	Message string
	Param   string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Message)
}

func apiError(status int, code string, message string) *APIError {
	return &APIError{Status: status, Code: strings.TrimSpace(code), Message: strings.TrimSpace(message)}
}

func invalidRequest(message string) *APIError {
	return apiError(http.StatusBadRequest, "invalid_request", message)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(payload)
}

func writeError(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error", "internal error", ""))
		return
	}
	if apiErr, ok := err.(*APIError); ok {
		writeJSON(w, apiErr.Status, errorEnvelope(apiErr.Code, apiErr.Message, apiErr.Param))
		return
	}
	writeJSON(w, http.StatusInternalServerError, errorEnvelope("internal_error", err.Error(), ""))
}

func errorEnvelope(code string, message string, param string) map[string]any {
	errObj := map[string]any{
		"code":    strings.TrimSpace(code),
		"message": strings.TrimSpace(message),
	}
	if strings.TrimSpace(param) != "" {
		errObj["param"] = strings.TrimSpace(param)
	}
	return map[string]any{"error": errObj}
}
