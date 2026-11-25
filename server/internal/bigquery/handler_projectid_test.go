package bigquery

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"ai-analyst-local/internal/config"
)

func TestProjectIDFromSettingsUsesGoogleProjectIDFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.GoogleProjectID = strPtr("legacy-project")
	settings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{
			LastProjectID: strPtr("fallback-project"),
		},
	}
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	h := NewHandler(store, nil)
	got := h.projectIDFromSettings()
	if got != "legacy-project" {
		t.Fatalf("projectIDFromSettings() = %q, want %q", got, "legacy-project")
	}
}

func TestProjectIDFromSettingsFallsBackToDatasourceLastProjectID(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.GoogleProjectID = nil
	settings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{
			LastProjectID: strPtr("fallback-project"),
		},
	}
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	h := NewHandler(store, nil)
	got := h.projectIDFromSettings()
	if got != "fallback-project" {
		t.Fatalf("projectIDFromSettings() = %q, want %q", got, "fallback-project")
	}
}

func TestProjectIDFromSettingsReturnsEmptyWhenUnset(t *testing.T) {
	dir := t.TempDir()
	store, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.GoogleProjectID = nil
	settings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{},
	}
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	h := NewHandler(store, nil)
	got := h.projectIDFromSettings()
	if got != "" {
		t.Fatalf("projectIDFromSettings() = %q, want empty", got)
	}
}

func TestHandleStatusReturnsSafeDetectedQuotaProject(t *testing.T) {
	dir := t.TempDir()
	adcPath := dir + "/application_default_credentials.json"
	if err := os.WriteFile(adcPath, []byte(`{
		"type": "authorized_user",
		"client_id": "client-id",
		"client_secret": "client-secret",
		"refresh_token": "refresh-token",
		"quota_project_id": "quota-project"
	}`), 0o600); err != nil {
		t.Fatalf("write adc: %v", err)
	}

	store, err := config.NewStore(dir + "/settings")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.GoogleCredentialsPath = strPtr(adcPath)
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	h := NewHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/bigquery/status", nil)
	rr := httptest.NewRecorder()
	h.handleStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var body map[string]map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	adc := body["adc"]
	if adc["project_id"] != "quota-project" {
		t.Fatalf("project_id = %#v, want quota-project; adc=%#v", adc["project_id"], adc)
	}
	if adc["project_source"] != "adc.quota_project_id" {
		t.Fatalf("project_source = %#v, want adc.quota_project_id", adc["project_source"])
	}
	if adc["quota_project_id"] != "quota-project" {
		t.Fatalf("quota_project_id = %#v, want quota-project", adc["quota_project_id"])
	}
	encoded := rr.Body.String()
	for _, secret := range []string{"refresh-token", "client-secret"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("status leaked secret %q in %s", secret, encoded)
		}
	}
}

func strPtr(v string) *string {
	s := v
	return &s
}
