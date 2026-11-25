package bigquery

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"ai-analyst-local/internal/localexec"
)

const (
	adcReauthStateIdle          = "idle"
	adcReauthStateRunning       = "running"
	adcReauthStateSucceeded     = "succeeded"
	adcReauthStateFailed        = "failed"
	adcReauthStateCancelled     = "cancelled"
	adcReauthStateGcloudMissing = "gcloud_missing"

	adcReauthFallbackCommand = "gcloud auth application-default login"
	adcReauthTimeout         = 5 * time.Minute
)

var adcReauthCommand = []string{"gcloud", "auth", "application-default", "login", "--quiet"}

type adcReauthRun struct {
	state           string
	startedAt       time.Time
	finishedAt      time.Time
	message         string
	errorMessage    string
	command         []string
	fallbackCommand string
	gcloudPath      string
	cancel          context.CancelFunc
}

type adcReauthSnapshot struct {
	State           string   `json:"state"`
	Running         bool     `json:"running"`
	StartedAt       int64    `json:"started_at,omitempty"`
	FinishedAt      int64    `json:"finished_at,omitempty"`
	Message         string   `json:"message,omitempty"`
	Error           string   `json:"error,omitempty"`
	Command         []string `json:"command,omitempty"`
	FallbackCommand string   `json:"fallback_command,omitempty"`
	GcloudPath      string   `json:"gcloud_path,omitempty"`
}

func (h *Handler) handleADCReauthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, h.startADCReauth())
}

func (h *Handler) handleADCReauthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, h.adcReauthSnapshot())
}

func (h *Handler) handleADCReauthCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, h.cancelADCReauth())
}

func (h *Handler) startADCReauth() adcReauthSnapshot {
	h.reauthMu.Lock()
	if h.reauth != nil && h.reauth.state == adcReauthStateRunning {
		snap := h.reauth.snapshotLocked()
		h.reauthMu.Unlock()
		return snap
	}

	gcloudPath, ok := localexec.ResolveCommand("gcloud")
	if !ok {
		now := time.Now()
		h.reauth = &adcReauthRun{
			state:           adcReauthStateGcloudMissing,
			startedAt:       now,
			finishedAt:      now,
			message:         "Google Cloud SDK was not found on this computer.",
			errorMessage:    "Install Google Cloud SDK, then run gcloud auth application-default login.",
			command:         append([]string(nil), adcReauthCommand...),
			fallbackCommand: adcReauthFallbackCommand,
		}
		snap := h.reauth.snapshotLocked()
		h.reauthMu.Unlock()
		return snap
	}

	ctx, cancel := context.WithTimeout(context.Background(), adcReauthTimeout)
	now := time.Now()
	run := &adcReauthRun{
		state:           adcReauthStateRunning,
		startedAt:       now,
		message:         "Waiting for Google sign-in in your browser...",
		command:         append([]string(nil), adcReauthCommand...),
		fallbackCommand: adcReauthFallbackCommand,
		gcloudPath:      gcloudPath,
		cancel:          cancel,
	}
	h.reauth = run
	snap := run.snapshotLocked()
	h.reauthMu.Unlock()

	go h.runADCReauth(ctx, run, gcloudPath)
	return snap
}

func (h *Handler) runADCReauth(ctx context.Context, run *adcReauthRun, gcloudPath string) {
	args := adcReauthCommand[1:]
	cmd := exec.CommandContext(ctx, gcloudPath, args...)
	cmd.Env = adcReauthEnv(gcloudPath)
	out, err := cmd.CombinedOutput()
	output := truncateADCReauthOutput(string(out))

	state := adcReauthStateSucceeded
	message := "Google sign-in completed. Verifying BigQuery access..."
	errorMessage := ""
	if err != nil {
		switch ctx.Err() {
		case context.Canceled:
			state = adcReauthStateCancelled
			message = "Google sign-in was cancelled."
		case context.DeadlineExceeded:
			state = adcReauthStateFailed
			message = "Google sign-in timed out."
			errorMessage = "Try again, or run gcloud auth application-default login from a terminal."
		default:
			state = adcReauthStateFailed
			message = "Google sign-in did not complete."
			errorMessage = output
			if errorMessage == "" {
				errorMessage = strings.TrimSpace(err.Error())
			}
		}
	} else {
		h.invalidateCachedToken()
	}

	h.reauthMu.Lock()
	if h.reauth == run {
		run.state = state
		run.finishedAt = time.Now()
		run.message = message
		run.errorMessage = errorMessage
		run.cancel = nil
	}
	h.reauthMu.Unlock()
}

func (h *Handler) adcReauthSnapshot() adcReauthSnapshot {
	h.reauthMu.Lock()
	defer h.reauthMu.Unlock()
	if h.reauth == nil {
		return idleADCReauthSnapshot()
	}
	return h.reauth.snapshotLocked()
}

func (h *Handler) cancelADCReauth() adcReauthSnapshot {
	h.reauthMu.Lock()
	defer h.reauthMu.Unlock()
	if h.reauth == nil {
		return idleADCReauthSnapshot()
	}
	if h.reauth.state == adcReauthStateRunning {
		if h.reauth.cancel != nil {
			h.reauth.cancel()
		}
		h.reauth.state = adcReauthStateCancelled
		h.reauth.finishedAt = time.Now()
		h.reauth.message = "Google sign-in was cancelled."
		h.reauth.errorMessage = ""
		h.reauth.cancel = nil
	}
	return h.reauth.snapshotLocked()
}

func idleADCReauthSnapshot() adcReauthSnapshot {
	return adcReauthSnapshot{
		State:           adcReauthStateIdle,
		Running:         false,
		Command:         append([]string(nil), adcReauthCommand...),
		FallbackCommand: adcReauthFallbackCommand,
	}
}

func (r *adcReauthRun) snapshotLocked() adcReauthSnapshot {
	if r == nil {
		return idleADCReauthSnapshot()
	}
	snap := adcReauthSnapshot{
		State:           r.state,
		Running:         r.state == adcReauthStateRunning,
		Message:         strings.TrimSpace(r.message),
		Error:           strings.TrimSpace(r.errorMessage),
		Command:         append([]string(nil), r.command...),
		FallbackCommand: r.fallbackCommand,
		GcloudPath:      r.gcloudPath,
	}
	if !r.startedAt.IsZero() {
		snap.StartedAt = r.startedAt.UnixMilli()
	}
	if !r.finishedAt.IsZero() {
		snap.FinishedAt = r.finishedAt.UnixMilli()
	}
	if len(snap.Command) == 0 {
		snap.Command = append([]string(nil), adcReauthCommand...)
	}
	if strings.TrimSpace(snap.FallbackCommand) == "" {
		snap.FallbackCommand = adcReauthFallbackCommand
	}
	if strings.TrimSpace(snap.State) == "" {
		snap.State = adcReauthStateIdle
	}
	return snap
}

func truncateADCReauthOutput(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// gcloud output can include terminal formatting and multi-line browser URLs.
	// Keep enough for diagnosis without flooding the settings UI.
	s = strings.Join(strings.Fields(s), " ")
	const maxLen = 600
	if len(s) <= maxLen {
		return s
	}
	return strings.TrimSpace(s[:maxLen]) + "..."
}

func adcReauthEnv(gcloudPath string) []string {
	env := os.Environ()
	pathIdx := -1
	pathValue := ""
	for i, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.EqualFold(key, "PATH") {
			continue
		}
		pathIdx = i
		pathValue = value
		break
	}

	dirs := make([]string, 0, 8)
	if dir := strings.TrimSpace(filepath.Dir(gcloudPath)); dir != "" && dir != "." {
		dirs = append(dirs, dir)
	}
	dirs = append(dirs, localexec.CommandSearchDirs()...)
	if pathValue != "" {
		dirs = append(dirs, filepath.SplitList(pathValue)...)
	}

	seen := map[string]bool{}
	clean := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" || seen[dir] {
			continue
		}
		seen[dir] = true
		clean = append(clean, dir)
	}
	if len(clean) == 0 {
		return env
	}

	pathEntry := "PATH=" + strings.Join(clean, string(os.PathListSeparator))
	if pathIdx >= 0 {
		env[pathIdx] = pathEntry
		return env
	}
	return append(env, pathEntry)
}
