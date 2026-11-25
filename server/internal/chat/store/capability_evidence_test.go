package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJSONProviderCapabilityEvidenceUpsertAndFingerprintFilter(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := NewJSON(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := ProviderCapabilityEvidence{
		ConnectionID: "bedrock-prod", Model: "anthropic.claude", Fingerprint: "sha256:a",
		Probe: "hosted_web_search", Verdict: "unsupported", Reason: "not_on_bedrock",
		Message: "Hosted web search is unavailable", DurationMS: 20, VerifiedAt: now,
	}
	if _, err := st.UpsertProviderCapabilityEvidence(ctx, base); err != nil {
		t.Fatal(err)
	}
	updated := base
	updated.Message = "Confirmed unavailable"
	updated.DurationMS = 12
	updated.VerifiedAt = now.Add(time.Minute)
	if _, err := st.UpsertProviderCapabilityEvidence(ctx, updated); err != nil {
		t.Fatal(err)
	}
	older := base
	older.Message = "late stale completion"
	older.VerifiedAt = now.Add(30 * time.Second)
	kept, err := st.UpsertProviderCapabilityEvidence(ctx, older)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Message != updated.Message || !kept.VerifiedAt.Equal(updated.VerifiedAt) {
		t.Fatalf("older evidence replaced newer evidence: kept=%#v newer=%#v", kept, updated)
	}
	otherFingerprint := base
	otherFingerprint.Fingerprint = "sha256:b"
	otherFingerprint.Verdict = "failed"
	otherFingerprint.VerifiedAt = now.Add(2 * time.Minute)
	if _, err := st.UpsertProviderCapabilityEvidence(ctx, otherFingerprint); err != nil {
		t.Fatal(err)
	}
	st, err = NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen NewJSON: %v", err)
	}

	current, err := st.ListProviderCapabilityEvidence(ctx, ProviderCapabilityEvidenceFilter{
		ConnectionID: "bedrock-prod", Model: "anthropic.claude", Fingerprint: "sha256:a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 1 || current[0].Message != updated.Message || current[0].DurationMS != 12 {
		t.Fatalf("current evidence=%#v, want updated fingerprint-a record", current)
	}
	all, err := st.ListProviderCapabilityEvidence(ctx, ProviderCapabilityEvidenceFilter{ConnectionID: "bedrock-prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Fingerprint != "sha256:b" {
		t.Fatalf("all evidence=%#v, want two records newest first", all)
	}
}

func TestStoredProviderCapabilityEvidenceRequiresVerificationTime(t *testing.T) {
	_, err := validateStoredProviderCapabilityEvidence(ProviderCapabilityEvidence{
		ConnectionID: "conn", Model: "model", Fingerprint: "fingerprint", Probe: "probe",
		Verdict: "verified", Message: "ok",
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("validateStoredProviderCapabilityEvidence err=%v, want ErrInvalid", err)
	}
}
