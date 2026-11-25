package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"ai-analyst-local/internal/chat/threadtransfer"
)

func (h *Handler) handleExportThread(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	fileName, payload, _, err := threadtransfer.ExportThreadFile(r.Context(), h.store, h.cfgDir, threadID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", threadtransfer.BundleMimeType)
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fileName))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (h *Handler) handleImportThread(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxThreadTransferBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if int64(len(body)) > maxThreadTransferBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":     "thread bundle too large",
			"max_bytes": maxThreadTransferBytes,
		})
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing thread bundle payload"})
		return
	}

	result, err := threadtransfer.ImportThread(r.Context(), h.store, h.cfgDir, body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"thread":         threadToDTO(result.Thread),
		"message_count":  result.MessageCount,
		"artifact_count": result.ArtifactCount,
	})
}
