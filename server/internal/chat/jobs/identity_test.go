package jobs

import (
	"strings"
	"testing"
)

func TestNormalizeManagedQueryJobID(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"qj-dbx-0123456789abcdef",
		"qj-attempt_2",
		"  qj-bq-a100  ",
	} {
		normalized, err := NormalizeManagedQueryJobID(value)
		if err != nil || normalized != strings.TrimSpace(value) {
			t.Fatalf("NormalizeManagedQueryJobID(%q)=%q err=%v", value, normalized, err)
		}
	}

	for _, value := range []string{
		"", "nil", "null", "<nil>", "00000-00000",
		"qj-00000-00000", "qj-nil", "qj-", "qj-_attempt",
		"qj-bad/value", "QJ-dbx-abc", "provider-statement-id",
		"qj-" + strings.Repeat("a", MaxManagedQueryJobIDLength),
	} {
		if normalized, err := NormalizeManagedQueryJobID(value); err == nil {
			t.Fatalf("NormalizeManagedQueryJobID(%q)=%q, want error", value, normalized)
		}
	}
}

func TestIsQueryJobIDSentinel(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"nil", " NULL ", "<nil>", "none", "undefined",
		"00000-00000", "00000000-0000-0000-0000-000000000000",
		"qj-0000_0000", "qj-null",
	} {
		if !IsQueryJobIDSentinel(value) {
			t.Fatalf("IsQueryJobIDSentinel(%q)=false", value)
		}
	}
	for _, value := range []string{"", "qj-dbx-abc123", "0a00-0000", "provider-000"} {
		if IsQueryJobIDSentinel(value) {
			t.Fatalf("IsQueryJobIDSentinel(%q)=true", value)
		}
	}
}

func TestValidateDurableQueryJobIdentity(t *testing.T) {
	t.Parallel()
	managed := JobRecord{
		ID: "qj-dbx-current", ReplacesJobID: "qj-dbx-previous",
		Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
	}
	if err := ValidateDurableQueryJobIdentity(managed); err != nil {
		t.Fatalf("valid managed identity: %v", err)
	}
	for _, value := range []string{"nil", "00000-00000", "qj-00000-00000"} {
		bad := managed
		bad.ReplacesJobID = value
		if err := ValidateDurableQueryJobIdentity(bad); err == nil {
			t.Fatalf("replacement %q accepted", value)
		}
	}
	for _, value := range []string{"QJ-dbx-current", "Qj-dbx-current"} {
		bad := managed
		bad.ID = value
		if err := ValidateDurableQueryJobIdentity(bad); err == nil {
			t.Fatalf("managed-looking primary %q accepted", value)
		}
		bad = managed
		bad.ReplacesJobID = value
		if err := ValidateDurableQueryJobIdentity(bad); err == nil {
			t.Fatalf("managed-looking replacement %q accepted", value)
		}
	}
	legacy := JobRecord{
		ID: "job-generic", ReplacesJobID: "job-parent",
		Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
	}
	if err := ValidateDurableQueryJobIdentity(legacy); err != nil {
		t.Fatalf("legacy generic identity: %v", err)
	}
}
