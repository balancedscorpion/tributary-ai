package publicapi

import (
	"net/http"
	"strings"

	"ai-analyst-local/internal/chat/store"
)

func (a AuthInfo) resourceRestricted() bool {
	return len(a.AllowedThreadIDs) > 0 || len(a.AllowedProjectIDs) > 0
}

func (a AuthInfo) canAccessThread(t store.Thread) bool {
	if !a.resourceRestricted() {
		return true
	}
	threadID := strings.TrimSpace(t.ID)
	for _, allowed := range a.AllowedThreadIDs {
		if threadID != "" && threadID == strings.TrimSpace(allowed) {
			return true
		}
	}
	if t.ProjectID != nil {
		projectID := strings.TrimSpace(*t.ProjectID)
		for _, allowed := range a.AllowedProjectIDs {
			if projectID != "" && projectID == strings.TrimSpace(allowed) {
				return true
			}
		}
	}
	return false
}

func (a AuthInfo) canCreateThread(projectID *string) bool {
	if !a.resourceRestricted() {
		return true
	}
	if projectID == nil {
		return false
	}
	pid := strings.TrimSpace(*projectID)
	if pid == "" {
		return false
	}
	for _, allowed := range a.AllowedProjectIDs {
		if pid == strings.TrimSpace(allowed) {
			return true
		}
	}
	return false
}

func permissionDenied(message string) *APIError {
	if strings.TrimSpace(message) == "" {
		message = "permission denied"
	}
	return apiError(http.StatusForbidden, "permission_denied", message)
}
