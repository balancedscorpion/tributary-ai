package store

import (
	"fmt"
	"strings"
	"time"
)

func prepareProviderCapabilityEvidence(in ProviderCapabilityEvidence) (ProviderCapabilityEvidence, error) {
	in.ConnectionID = strings.TrimSpace(in.ConnectionID)
	in.Model = strings.TrimSpace(in.Model)
	in.Fingerprint = strings.TrimSpace(in.Fingerprint)
	in.Probe = strings.TrimSpace(in.Probe)
	in.Verdict = strings.ToLower(strings.TrimSpace(in.Verdict))
	in.Reason = strings.TrimSpace(in.Reason)
	in.Message = strings.TrimSpace(in.Message)
	in.RequestID = strings.TrimSpace(in.RequestID)
	if in.ConnectionID == "" || in.Model == "" || in.Fingerprint == "" || in.Probe == "" {
		return ProviderCapabilityEvidence{}, fmt.Errorf("%w: connectionId, model, fingerprint, and probe are required", ErrInvalid)
	}
	switch in.Verdict {
	case "verified", "unsupported", "failed", "skipped":
	default:
		return ProviderCapabilityEvidence{}, fmt.Errorf("%w: invalid capability evidence verdict", ErrInvalid)
	}
	if in.DurationMS < 0 {
		return ProviderCapabilityEvidence{}, fmt.Errorf("%w: durationMs must be non-negative", ErrInvalid)
	}
	if in.VerifiedAt.IsZero() {
		in.VerifiedAt = time.Now().UTC()
	} else {
		in.VerifiedAt = in.VerifiedAt.UTC()
	}
	return in, nil
}

// validateStoredProviderCapabilityEvidence deliberately does not synthesize a
// verification timestamp. Doing so while reading a malformed JSON record would
// make old/invalid evidence appear freshly verified.
func validateStoredProviderCapabilityEvidence(in ProviderCapabilityEvidence) (ProviderCapabilityEvidence, error) {
	if in.VerifiedAt.IsZero() {
		return ProviderCapabilityEvidence{}, fmt.Errorf("%w: stored capability evidence verifiedAt is required", ErrInvalid)
	}
	return prepareProviderCapabilityEvidence(in)
}

func normalizeProviderCapabilityEvidenceFilter(in ProviderCapabilityEvidenceFilter) (ProviderCapabilityEvidenceFilter, error) {
	in.ConnectionID = strings.TrimSpace(in.ConnectionID)
	in.Model = strings.TrimSpace(in.Model)
	in.Fingerprint = strings.TrimSpace(in.Fingerprint)
	in.Probe = strings.TrimSpace(in.Probe)
	if in.ConnectionID == "" {
		return ProviderCapabilityEvidenceFilter{}, fmt.Errorf("%w: connectionId is required", ErrInvalid)
	}
	if in.Limit <= 0 {
		in.Limit = 200
	}
	if in.Limit > 1000 {
		in.Limit = 1000
	}
	return in, nil
}
