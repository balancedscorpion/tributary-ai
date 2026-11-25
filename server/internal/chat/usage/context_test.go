package usage

import (
	"context"
	"testing"
)

func TestAttributionContextAndNewInvocation(t *testing.T) {
	ctx := WithAttribution(context.Background(), Attribution{
		RunID:            " run-1 ",
		ThreadID:         " thread-1 ",
		JobID:            " job-1 ",
		Operation:        OperationContextSummary,
		ProviderConfigID: " profile-1 ",
		APIType:          " responses ",
	})
	attr, ok := AttributionFromContext(ctx)
	if !ok {
		t.Fatal("AttributionFromContext returned no attribution")
	}
	if attr.RunID != "run-1" || attr.ThreadID != "thread-1" || attr.JobID != "job-1" {
		t.Fatalf("unexpected normalized attribution: %#v", attr)
	}
	if attr.ProviderConfigID != "profile-1" || attr.APIType != "responses" {
		t.Fatalf("unexpected provider attribution: %#v", attr)
	}

	inv := NewInvocation(ctx, ProviderIdentity{
		ProviderID:     "openai",
		RequestedModel: "gpt-5",
	})
	if stringValue(inv.RunID) != "run-1" || stringValue(inv.ThreadID) != "thread-1" || stringValue(inv.JobID) != "job-1" {
		t.Fatalf("NewInvocation attribution = %#v", inv)
	}
	if inv.Operation != OperationContextSummary || stringValue(inv.ProviderConfigID) != "profile-1" || stringValue(inv.APIType) != "responses" {
		t.Fatalf("NewInvocation operation/provider attribution = %#v", inv)
	}
}

func TestLegacyProfileAttributionDoesNotBecomeProviderConfig(t *testing.T) {
	ctx := WithAttribution(context.Background(), Attribution{ProfileID: "legacy-profile"})
	inv := NewInvocation(ctx, ProviderIdentity{ProviderID: "compatible", RequestedModel: "legacy-model"})
	if stringValue(inv.ProfileID) != "legacy-profile" || inv.ProviderConfigID != nil {
		t.Fatalf("legacy attribution = %#v", inv)
	}
}

func TestExplicitInvocationIdentityWinsOverContextFallback(t *testing.T) {
	ctx := WithAttribution(context.Background(), Attribution{
		ProviderConfigID:  "context-profile",
		APIType:           "context-api",
		ConfigFingerprint: "context-fingerprint",
	})
	inv := NewInvocation(ctx, ProviderIdentity{
		ProviderID:       "openai",
		ProviderConfigID: "explicit-profile",
		APIType:          "explicit-api",
		RequestedModel:   "gpt-5",
	})
	if got := stringValue(inv.ProviderConfigID); got != "explicit-profile" {
		t.Fatalf("ProviderConfigID = %q, want explicit-profile", got)
	}
	if got := stringValue(inv.APIType); got != "explicit-api" {
		t.Fatalf("APIType = %q, want explicit-api", got)
	}
	if inv.ConfigFingerprint != nil {
		t.Fatalf("cross-identity fingerprint leaked from context: %#v", inv.ConfigFingerprint)
	}
}
