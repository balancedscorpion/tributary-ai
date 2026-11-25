package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"ai-analyst-local/internal/activation"
	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/chat/store"
	chattools "ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/databricks"
	"ai-analyst-local/internal/githubcli"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/proxy"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sharepoint"
	"ai-analyst-local/internal/slack"
	"ai-analyst-local/internal/trino"
)

type HandlerOpts struct {
	Dev bool
	// DesktopToken, when set, requires an app-owned per-launch token on /api
	// requests. This protects desktop loopback APIs from other local processes.
	DesktopToken string

	Settings    *config.Store
	Secrets     *secrets.Store
	Proxy       *proxy.Handler
	BigQuery    *bigquery.Handler
	Databricks  *databricks.Handler
	Trino       *trino.Handler
	Snowflake   http.Handler
	Atlassian   *atlassian.Manager
	SharePoint  *sharepoint.Manager
	GitHub      *githubcli.Manager
	Permissions *permissions.Service
	Slack       *slack.Manager
	Arxiv       http.Handler
	Activation  *activation.Store

	DB        *sql.DB      // optional; when set, /api/health also verifies DB connectivity
	Chat      http.Handler // optional; handles /api/threads, /api/runs, /api/projects, /api/artifacts, /api/tools/{toolName}/test
	ChatStore store.Store  // optional; persists provider capability probe evidence
	PublicAPI http.Handler // optional; handles /api/external/v1
	Usage     http.Handler // optional; handles read-only /api/usage reporting

	// UsageRepository also instruments provider connection tests, which create a
	// temporary provider factory outside the main chat runtime.
	UsageRepository usage.Repository

	Static http.Handler // optional; if nil, static is not served
}

func NewHandler(opts HandlerOpts) http.Handler {
	if opts.Settings == nil || opts.Secrets == nil || opts.Proxy == nil {
		panic("server.NewHandler: missing required dependencies")
	}
	if opts.Activation == nil {
		panic("server.NewHandler: missing activation store")
	}

	api := http.NewServeMux()

	api.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if opts.DB == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := opts.DB.PingContext(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":       false,
				"db_ok":    false,
				"db_error": err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "db_ok": true})
	})

	api.HandleFunc("POST /api/system/open-external", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		if err := decodeJSONLimit(r.Body, &body, 16<<10); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		target, err := validateExternalURL(body.URL)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := openExternalBrowser(target); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	// Activation
	api.HandleFunc("GET /api/activation/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, opts.Activation.Status())
	})
	api.HandleFunc("POST /api/activation/install", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
		}
		if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		status, err := opts.Activation.Install(body.Token)
		if err != nil {
			code := "INVALID_ACTIVATION_KEY"
			lerr := strings.ToLower(strings.TrimSpace(status.Error))
			if !status.PublicKeyConfigured {
				code = "PUBLIC_KEY_NOT_CONFIGURED"
			} else if strings.Contains(lerr, "expired") {
				code = "EXPIRED"
			} else if strings.Contains(lerr, "device id") || strings.Contains(lerr, "device_id") {
				code = "DEVICE_ID_MISMATCH"
			}
			msg := strings.TrimSpace(status.Error)
			if msg == "" {
				msg = strings.TrimSpace(err.Error())
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"ok":      false,
				"code":    code,
				"message": msg,
				"error":   err.Error(),
				"status":  status,
			})
			return
		}
		writeJSON(w, http.StatusOK, status)
	})
	api.HandleFunc("POST /api/activation/clear", func(w http.ResponseWriter, r *http.Request) {
		_ = opts.Activation.Clear()
		writeJSON(w, http.StatusOK, opts.Activation.Status())
	})

	// Settings
	api.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, opts.Settings.Get())
	})
	api.HandleFunc("POST /api/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var next config.AppSettings
		if err := decodeJSONLimit(r.Body, &next, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := opts.Settings.Save(next); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, opts.Settings.Get())
	})
	api.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var next config.AppSettings
		if err := decodeJSONLimit(r.Body, &next, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := opts.Settings.Save(next); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, opts.Settings.Get())
	})

	if opts.Permissions != nil {
		api.HandleFunc("GET /api/permissions/rules", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			rules, err := opts.Permissions.ListRules(r.Context(), permissions.RuleFilter{
				Category: r.URL.Query().Get("category"),
				Scope:    r.URL.Query().Get("scope"),
			})
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rules": rules})
		}))
		api.HandleFunc("POST /api/permissions/rules", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			var body permissions.PermissionRule
			if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			rule, err := opts.Permissions.CreateRule(r.Context(), body)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})
		}))
		api.HandleFunc("PATCH /api/permissions/rules/{ruleId}", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			var body permissions.PermissionRule
			if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			rule, err := opts.Permissions.UpdateRule(r.Context(), r.PathValue("ruleId"), body)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "rule": rule})
		}))
		api.HandleFunc("DELETE /api/permissions/rules/{ruleId}", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			if err := opts.Permissions.DeleteRule(r.Context(), r.PathValue("ruleId")); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}))
		api.HandleFunc("GET /api/permissions/audit", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			records, err := opts.Permissions.ListAudit(r.Context(), 200)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "records": records})
		}))
		api.HandleFunc("POST /api/permissions/local-locations/test", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Subject store.PermissionSubject `json:"subject"`
			}
			if err := decodeJSONLimit(r.Body, &body, 64<<10); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			result, err := opts.Permissions.TestLocalLocation(r.Context(), body.Subject)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
		}))
		api.HandleFunc("GET /api/approvals", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			approvals, err := opts.Permissions.ListApprovals(r.Context(), permissions.ApprovalFilter{
				ThreadID: firstQueryValue(r, "thread_id", "threadId"),
				RunID:    firstQueryValue(r, "run_id", "runId"),
				Status:   r.URL.Query().Get("status"),
				Limit:    200,
			})
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approvals": approvals})
		}))
		api.HandleFunc("POST /api/approvals/{approvalId}/approve", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			var body struct {
				Scope   string                   `json:"scope"`
				Subject *store.PermissionSubject `json:"subject,omitempty"`
			}
			if r.Body != nil && r.ContentLength != 0 {
				if err := decodeJSONLimit(r.Body, &body, 64<<10); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			}
			approval, err := opts.Permissions.ApproveWithOptions(r.Context(), r.PathValue("approvalId"), permissions.ApproveOptions{
				Scope:   body.Scope,
				Subject: body.Subject,
			})
			if err != nil {
				writeJSON(w, permissionHTTPStatus(err), map[string]any{"ok": false, "error": err.Error(), "approval": approval})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approval": approval})
		}))
		api.HandleFunc("POST /api/approvals/{approvalId}/reject", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			approval, err := opts.Permissions.Reject(r.Context(), r.PathValue("approvalId"))
			if err != nil {
				writeJSON(w, permissionHTTPStatus(err), map[string]any{"ok": false, "error": err.Error(), "approval": approval})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approval": approval})
		}))
	}

	api.HandleFunc("POST /api/providers/test", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		handleProviderConnectionTest(w, r, opts)
	}))
	api.HandleFunc("POST /api/providers/capabilities/resolve", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		handleProviderCapabilityResolve(w, r, opts)
	}))
	api.HandleFunc("GET /api/providers/connections/{connectionId}/capabilities", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		handleProviderConnectionCapabilities(w, r, opts)
	}))
	api.HandleFunc("POST /api/providers/models/discover", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		handleProviderModelDiscovery(w, r, opts)
	}))
	api.HandleFunc("POST /api/providers/connections/{connectionId}/models/discover", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		handleProviderModelDiscovery(w, r, opts)
	}))

	api.HandleFunc("GET /api/tools/inventory", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"tools":                 chattools.AllToolMetadata(),
			"provider_hosted_tools": chattools.ProviderHostedToolCapabilities(),
		})
	}))

	// Atlassian browser-backed Jira/Confluence integration.
	if opts.Atlassian != nil {
		api.HandleFunc("GET /api/integrations/atlassian/status", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.Atlassian.Status(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/atlassian/check", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.Atlassian.CheckConnection(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/atlassian/login/start", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.Atlassian.StartLogin(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/atlassian/login/finish", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.Atlassian.FinishLogin(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/atlassian/disconnect", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.Atlassian.Disconnect(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("GET /api/integrations/atlassian/approvals", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"approvals": opts.Atlassian.ListApprovals()})
		}))
		api.HandleFunc("POST /api/integrations/atlassian/approvals/{approvalId}/approve", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.Atlassian.Approve(r.Context(), r.PathValue("approvalId"))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result, "approvals": opts.Atlassian.ListApprovals()})
		}))
		api.HandleFunc("POST /api/integrations/atlassian/approvals/{approvalId}/reject", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			approval, err := opts.Atlassian.Reject(r.PathValue("approvalId"))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approval": approval, "approvals": opts.Atlassian.ListApprovals()})
		}))
	}

	// SharePoint browser-backed document integration.
	if opts.SharePoint != nil {
		api.HandleFunc("GET /api/integrations/sharepoint/status", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.SharePoint.Status(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/sharepoint/check", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.SharePoint.CheckConnection(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/sharepoint/login/start", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.SharePoint.StartLogin(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/sharepoint/login/finish", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.SharePoint.FinishLogin(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/sharepoint/disconnect", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.SharePoint.Disconnect(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
	}

	// GitHub CLI-backed MCP integration. Read tools use the app-managed GitHub
	// token through an isolated gh process. Mutating tools queue approvals.
	if opts.GitHub != nil {
		api.HandleFunc("GET /api/integrations/github/status", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.GitHub.Status(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("POST /api/integrations/github/check", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.GitHub.Check(r.Context())
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, result)
		}))
		api.HandleFunc("GET /api/integrations/github/approvals", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, map[string]any{"approvals": opts.GitHub.ListApprovals()})
		}))
		api.HandleFunc("POST /api/integrations/github/approvals/{approvalId}/approve", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			result, err := opts.GitHub.Approve(r.Context(), r.PathValue("approvalId"))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result, "approvals": opts.GitHub.ListApprovals()})
		}))
		api.HandleFunc("POST /api/integrations/github/approvals/{approvalId}/reject", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
			approval, err := opts.GitHub.Reject(r.PathValue("approvalId"))
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approval": approval, "approvals": opts.GitHub.ListApprovals()})
		}))
	}

	// Slack OAuth-backed read integration. OAuth tokens stay in the encrypted
	// local secrets store; status responses only expose workspace metadata.
	if opts.Slack != nil {
		api.Handle("/api/integrations/slack/", activationGate(opts.Activation, opts.Settings, opts.Slack))
	}

	// arXiv public-data integration. Papers are stored locally under the app
	// config directory; outbound calls go only to arXiv hosts.
	if opts.Arxiv != nil {
		api.Handle("/api/integrations/arxiv/", activationGate(opts.Activation, opts.Settings, opts.Arxiv))
	}

	// Secrets
	api.HandleFunc("GET /api/secrets/status", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		initialized, locked, configured := opts.Secrets.Status()
		writeJSON(w, http.StatusOK, map[string]any{
			"initialized": initialized,
			"locked":      locked,
			"configured":  configured,
		})
	}))

	api.HandleFunc("POST /api/secrets/init", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Passphrase string `json:"passphrase"`
		}
		if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := opts.Secrets.Init(body.Passphrase); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	api.HandleFunc("POST /api/secrets/unlock", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Passphrase string `json:"passphrase"`
		}
		if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := opts.Secrets.Unlock(body.Passphrase); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	api.HandleFunc("POST /api/secrets/lock", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		opts.Secrets.Lock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}))

	api.HandleFunc("POST /api/secrets/set", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProviderID string `json:"providerId"`
			AuthType   string `json:"authType,omitempty"`

			APIKey          string `json:"apiKey,omitempty"`
			AccessKeyID     string `json:"accessKeyId,omitempty"`
			SecretAccessKey string `json:"secretAccessKey,omitempty"`
			SessionToken    string `json:"sessionToken,omitempty"`

			ProfileID   string `json:"profileId,omitempty"`
			HeaderKey   string `json:"headerKey,omitempty"`
			HeaderValue string `json:"headerValue,omitempty"`

			OAuthAccessToken     string `json:"oauthAccessToken,omitempty"`
			RefreshToken         string `json:"refreshToken,omitempty"`
			ClientID             string `json:"clientId,omitempty"`
			Username             string `json:"username,omitempty"`
			Password             string `json:"password,omitempty"`
			JWT                  string `json:"jwt,omitempty"`
			PrivateKeyPEM        string `json:"privateKeyPem,omitempty"`
			PrivateKeyPassphrase string `json:"privateKeyPassphrase,omitempty"`
			ClientSecret         string `json:"clientSecret,omitempty"`
		}
		if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		pid := strings.TrimSpace(body.ProviderID)
		switch pid {
		case "openai", "anthropic", "gemini":
			if err := opts.Secrets.SetProviderKey(pid, body.APIKey); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "bedrock":
			if strings.TrimSpace(body.HeaderKey) != "" {
				if err := opts.Secrets.SetBedrockSecretHeader(body.HeaderKey, body.HeaderValue); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			authType := strings.TrimSpace(body.AuthType)
			if authType == "" {
				if strings.TrimSpace(body.APIKey) != "" {
					authType = "api_key"
				} else {
					authType = "aws_credentials"
				}
			}
			switch authType {
			case "api_key":
				if err := opts.Secrets.SetBedrockAPIKey(body.APIKey); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "aws_credentials":
				if err := opts.Secrets.SetBedrockCredentials(body.AccessKeyID, body.SecretAccessKey, body.SessionToken); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown bedrock authType"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "compatible":
			if strings.TrimSpace(body.ProfileID) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "profileId is required"})
				return
			}
			if strings.TrimSpace(body.HeaderKey) != "" {
				if err := opts.Secrets.SetCompatibleSecretHeader(body.ProfileID, body.HeaderKey, body.HeaderValue); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			if err := opts.Secrets.SetCompatibleAPIKey(body.ProfileID, body.APIKey); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "bigquery":
			if err := opts.Secrets.SetBigQueryOAuthAccessToken(body.OAuthAccessToken); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "databricks":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			if authType == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "databricks authType is required (refresh_token or pat)"})
				return
			}
			switch authType {
			case "refresh_token":
				if strings.TrimSpace(body.RefreshToken) == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "refreshToken is required when authType=refresh_token"})
					return
				}
				if err := opts.Secrets.SetDatabricksRefreshTokenWithClientID(body.RefreshToken, body.ClientID); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "pat":
				if strings.TrimSpace(body.APIKey) == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "apiKey is required when authType=pat"})
					return
				}
				if err := opts.Secrets.SetDatabricksPAT(body.APIKey); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown databricks authType"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "trino":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			switch authType {
			case "basic":
				if strings.TrimSpace(body.Username) == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "username is required when authType=basic"})
					return
				}
				if strings.TrimSpace(body.Password) == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "password is required when authType=basic"})
					return
				}
				if err := opts.Secrets.SetTrinoBasicAuth(body.Username, body.Password); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "jwt":
				token := strings.TrimSpace(body.JWT)
				if token == "" {
					token = strings.TrimSpace(body.APIKey)
				}
				if token == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "jwt is required when authType=jwt"})
					return
				}
				if err := opts.Secrets.SetTrinoJWT(token); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "none":
				if err := opts.Secrets.SetTrinoNone(); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown trino authType"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "snowflake":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			if authType == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "snowflake authType is required (oauth_token or keypair_jwt)"})
				return
			}
			switch authType {
			case "oauth_token":
				if strings.TrimSpace(body.OAuthAccessToken) == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "oauthAccessToken is required when authType=oauth_token"})
					return
				}
				if strings.TrimSpace(body.PrivateKeyPEM) != "" || strings.TrimSpace(body.PrivateKeyPassphrase) != "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "privateKeyPem/privateKeyPassphrase are not allowed when authType=oauth_token"})
					return
				}
				if err := opts.Secrets.SetSnowflakeOAuthToken(body.OAuthAccessToken); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "keypair_jwt":
				if strings.TrimSpace(body.PrivateKeyPEM) == "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "privateKeyPem is required when authType=keypair_jwt"})
					return
				}
				if strings.TrimSpace(body.OAuthAccessToken) != "" {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "oauthAccessToken is not allowed when authType=keypair_jwt"})
					return
				}
				if err := opts.Secrets.SetSnowflakeKeyPair(body.PrivateKeyPEM, body.PrivateKeyPassphrase); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown snowflake authType"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "slack":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			switch authType {
			case "client_secret":
				if err := opts.Secrets.SetSlackClientSecret(body.ClientSecret); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown slack authType"})
				return
			}
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown providerId"})
			return
		}
	}))

	api.HandleFunc("POST /api/secrets/clear", activationGateFunc(opts.Activation, opts.Settings, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ProviderID string `json:"providerId"`
			AuthType   string `json:"authType,omitempty"`
			ProfileID  string `json:"profileId,omitempty"`
			HeaderKey  string `json:"headerKey,omitempty"`
		}
		if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		pid := strings.TrimSpace(body.ProviderID)
		switch pid {
		case "openai", "anthropic", "gemini":
			if err := opts.Secrets.ClearProviderKey(pid); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "bedrock":
			if strings.TrimSpace(body.HeaderKey) != "" {
				if err := opts.Secrets.SetBedrockSecretHeader(body.HeaderKey, ""); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			authType := strings.TrimSpace(body.AuthType)
			switch authType {
			case "", "all":
				if err := opts.Secrets.ClearBedrockAuth(); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "api_key":
				if err := opts.Secrets.ClearBedrockAPIKey(); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "aws_credentials":
				if err := opts.Secrets.ClearBedrockCredentials(); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown bedrock authType"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "compatible":
			if strings.TrimSpace(body.ProfileID) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "profileId is required"})
				return
			}
			if strings.TrimSpace(body.HeaderKey) != "" {
				if err := opts.Secrets.SetCompatibleSecretHeader(body.ProfileID, body.HeaderKey, ""); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				return
			}
			if err := opts.Secrets.ClearCompatibleAPIKey(body.ProfileID); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "bigquery":
			if err := opts.Secrets.ClearBigQueryOAuthAccessToken(); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "databricks":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			if authType == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "databricks authType is required (refresh_token, pat, or all)"})
				return
			}
			if authType != "refresh_token" && authType != "pat" && authType != "all" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown databricks authType"})
				return
			}
			if err := opts.Secrets.ClearDatabricks(authType); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "trino":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			if authType == "" {
				authType = "all"
			}
			if authType != "basic" && authType != "jwt" && authType != "all" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "trino authType must be one of basic, jwt, or all"})
				return
			}
			if err := opts.Secrets.ClearTrino(authType); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "snowflake":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			if authType != "" && authType != "all" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "snowflake authType must be all when clearing"})
				return
			}
			if err := opts.Secrets.ClearSnowflake(); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		case "slack":
			authType := strings.TrimSpace(strings.ToLower(body.AuthType))
			switch authType {
			case "client_secret":
				if err := opts.Secrets.ClearSlackClientSecret(); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			case "", "oauth", "all":
				if err := opts.Secrets.ClearSlackOAuth(); err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
					return
				}
			default:
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown slack authType"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unknown providerId"})
			return
		}
	}))

	// Proxy
	api.Handle("/api/proxy", activationGate(opts.Activation, opts.Settings, opts.Proxy))

	// BigQuery (optional)
	if opts.BigQuery != nil {
		api.Handle("/api/bigquery/", activationGate(opts.Activation, opts.Settings, opts.BigQuery))
	}
	// Databricks (optional)
	if opts.Databricks != nil {
		api.Handle("/api/databricks/", activationGate(opts.Activation, opts.Settings, opts.Databricks))
	}
	// Trino (optional)
	if opts.Trino != nil {
		api.Handle("/api/trino/", activationGate(opts.Activation, opts.Settings, opts.Trino))
	}
	// Snowflake (optional)
	if opts.Snowflake != nil {
		api.Handle("/api/snowflake/", activationGate(opts.Activation, opts.Settings, cloudCredentialsSnowflakeGate(opts.Settings, opts.Snowflake)))
	}

	// DB-backed chat API (optional; mounted behind activation gate).
	chat := opts.Chat
	if chat == nil {
		chat = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"ok":    false,
				"error": "chat API not configured",
				"hint":  "This build uses the DB-backed (MySQL) threads/runs/projects API. Start the server with --db-config <mysql dsn> (or create db.json in the config dir) to enable it.",
			})
		})
	}
	api.Handle("/api/threads", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/threads/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/runs", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/runs/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/search", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/search/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/projects", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/projects/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("POST /api/tools/test", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("POST /api/tools/{toolName}/test", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/integrations/github/auth/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/artifacts", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/artifacts/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/files", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/files/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/sources", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/sources/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/local-files", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/local-files/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/github/repositories", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/github/repositories/", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/queries", activationGate(opts.Activation, opts.Settings, chat))
	api.Handle("/api/queries/", activationGate(opts.Activation, opts.Settings, chat))
	if opts.Usage != nil {
		api.Handle("/api/usage", activationGate(opts.Activation, opts.Settings, opts.Usage))
		api.Handle("/api/usage/", activationGate(opts.Activation, opts.Settings, opts.Usage))
	}
	if opts.PublicAPI != nil {
		api.Handle("/api/external/v1", activationGate(opts.Activation, opts.Settings, opts.PublicAPI))
		api.Handle("/api/external/v1/", activationGate(opts.Activation, opts.Settings, opts.PublicAPI))
	}

	root := http.NewServeMux()
	root.Handle("/api/", desktopTokenGuard(originGuard(api, opts.Dev), opts.DesktopToken))
	root.Handle("/", rootPageHandler(opts))

	return root
}

func rootPageHandler(opts HandlerOpts) http.Handler {
	static := opts.Static
	if static == nil {
		static = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "UI not built. Run `npm run build` to generate server/internal/webui/dist.", http.StatusNotFound)
		})
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.Databricks != nil && isDatabricksRootOAuthCallback(r) {
			r2 := r.Clone(r.Context())
			urlCopy := *r.URL
			urlCopy.Path = "/api/databricks/oauth/callback"
			urlCopy.RawPath = ""
			r2.URL = &urlCopy
			activationGate(opts.Activation, opts.Settings, opts.Databricks).ServeHTTP(w, r2)
			return
		}
		static.ServeHTTP(w, r)
	})
}

func isDatabricksRootOAuthCallback(r *http.Request) bool {
	if r == nil || r.Method != http.MethodGet || r.URL == nil {
		return false
	}
	if path := strings.TrimSpace(r.URL.Path); path != "" && path != "/" {
		return false
	}
	q := r.URL.Query()
	state := strings.TrimSpace(q.Get("state"))
	code := strings.TrimSpace(q.Get("code"))
	errCode := strings.TrimSpace(q.Get("error"))
	errDescription := strings.TrimSpace(q.Get("error_description"))
	return state != "" && (code != "" || errCode != "" || errDescription != "")
}

func cloudCredentialsSnowflakeEnabled(settings *config.Store) bool {
	if settings == nil {
		return false
	}
	ff := settings.Get().FeatureFlags
	if ff == nil {
		return false
	}
	return ff.CloudCredentialsSnowflake
}

func cloudCredentialsSnowflakeGate(settings *config.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cloudCredentialsSnowflakeEnabled(settings) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originGuard(next http.Handler, dev bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if originAllowed(origin, r.Host, dev) {
			w.Header().Add("Vary", "Origin")
			next.ServeHTTP(w, r)
			return
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	})
}

func originAllowed(origin string, host string, dev bool) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	// Allow same-origin with this server.
	if strings.EqualFold(u.Host, host) {
		return true
	}
	// Allow localhost/127.0.0.1 host variants matching server port.
	serverHost := strings.ToLower(host)
	if strings.HasPrefix(serverHost, "127.0.0.1:") || strings.HasPrefix(serverHost, "localhost:") {
		port := strings.Split(serverHost, ":")[1]
		if (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost") && u.Port() == port {
			return true
		}
	}
	if dev {
		if origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173" {
			return true
		}
	}
	return false
}

const desktopTokenHeader = "X-AI-Desktop-Token"

func desktopTokenGuard(next http.Handler, token string) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if desktopTokenExempt(r) {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimSpace(r.Header.Get(desktopTokenHeader))
		if got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"ok":      false,
			"code":    "DESKTOP_TOKEN_REQUIRED",
			"message": "desktop authorization token required",
		})
	})
}

func desktopTokenExempt(r *http.Request) bool {
	if r == nil {
		return false
	}
	path := strings.TrimSpace(r.URL.Path)
	if r.Method == http.MethodGet {
		switch path {
		case "/api/databricks/oauth/callback", "/api/integrations/slack/oauth/callback":
			return true
		}
	}
	if r.Method == http.MethodPost && path == "/api/databricks/oauth/complete" {
		// Databricks can redirect the external system browser to the app root.
		// That page can only complete the PKCE flow with the opaque state/code.
		return true
	}
	return false
}

func validateExternalURL(raw string) (string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", errors.New("url is required")
	}
	if len(target) > 4096 {
		return "", errors.New("url is too long")
	}
	if strings.ContainsFunc(target, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return "", errors.New("url contains invalid control characters")
	}
	u, err := url.Parse(target)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("url must be absolute")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", errors.New("only https urls can be opened externally")
	}
	if u.User != nil {
		return "", errors.New("url credentials are not allowed")
	}
	return u.String(), nil
}

func openExternalBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

func decodeJSONLimit(r io.Reader, dst any, limit int64) error {
	if limit <= 0 {
		limit = 1 << 20
	}
	dec := json.NewDecoder(io.LimitReader(r, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Prevent trailing junk.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected additional JSON content")
		}
		return err
	}
	return nil
}

func firstQueryValue(r *http.Request, keys ...string) string {
	q := r.URL.Query()
	for _, key := range keys {
		if value := strings.TrimSpace(q.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func permissionHTTPStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, store.ErrConflict):
		return http.StatusConflict
	case errors.Is(err, store.ErrInvalid):
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(payload)
}
