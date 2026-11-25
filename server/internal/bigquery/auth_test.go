package bigquery

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectedProjectFromCredentialsPrefersServiceAccountProjectID(t *testing.T) {
	got := detectedProjectFromCredentials(credentialsFile{
		ProjectID:      " data-project ",
		QuotaProjectID: " quota-project ",
	})
	if got.ProjectID != "data-project" {
		t.Fatalf("ProjectID = %q, want data-project", got.ProjectID)
	}
	if got.ProjectSource != "adc.project_id" {
		t.Fatalf("ProjectSource = %q, want adc.project_id", got.ProjectSource)
	}
	if got.QuotaProjectID != "quota-project" {
		t.Fatalf("QuotaProjectID = %q, want quota-project", got.QuotaProjectID)
	}
}

func TestDetectedProjectFromCredentialsFallsBackToQuotaProjectID(t *testing.T) {
	got := detectedProjectFromCredentials(credentialsFile{
		QuotaProjectID: " quota-project ",
	})
	if got.ProjectID != "quota-project" {
		t.Fatalf("ProjectID = %q, want quota-project", got.ProjectID)
	}
	if got.ProjectSource != "adc.quota_project_id" {
		t.Fatalf("ProjectSource = %q, want adc.quota_project_id", got.ProjectSource)
	}
}

func TestProjectIDFromCloudSDKConfigBytesReadsCoreProject(t *testing.T) {
	in := []byte(`
[component_manager]
disable_update_check = true

[core]
account = user@example.com
project = "configured-project"
`)
	if got := projectIDFromCloudSDKConfigBytes(in); got != "configured-project" {
		t.Fatalf("projectIDFromCloudSDKConfigBytes = %q, want configured-project", got)
	}
}

func TestADCTokenUnavailableMessagePromptsReauthentication(t *testing.T) {
	got := adcTokenUnavailableMessage([]error{
		errors.New("gcloud token error: Reauthentication failed. cannot prompt during non-interactive execution."),
	})
	if !strings.Contains(got, "gcloud auth application-default login") {
		t.Fatalf("message = %q, want ADC login command", got)
	}
	if !strings.Contains(got, "reauthentication") {
		t.Fatalf("message = %q, want reauthentication guidance", got)
	}
}
