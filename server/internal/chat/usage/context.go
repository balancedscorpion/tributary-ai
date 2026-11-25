package usage

import (
	"context"
	"errors"
	"strings"
)

// ErrInvocationWatchdogTimeout distinguishes an internal stream timeout from
// explicit user cancellation.
var ErrInvocationWatchdogTimeout = errors.New("provider stream idle timeout")

type attributionContextKey struct{}

func WithAttribution(ctx context.Context, attr Attribution) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, attributionContextKey{}, normalizeAttribution(attr))
}

func AttributionFromContext(ctx context.Context) (Attribution, bool) {
	if ctx == nil {
		return Attribution{}, false
	}
	attr, ok := ctx.Value(attributionContextKey{}).(Attribution)
	if !ok {
		return Attribution{}, false
	}
	attr = normalizeAttribution(attr)
	present := attr.RunID != "" || attr.ThreadID != "" || attr.JobID != "" || attr.PlanID != "" ||
		attr.Operation != "" || attr.ConnectionID != "" || attr.ProfileID != "" ||
		attr.ConfigFingerprint != "" || attr.PlannedAPISurface != ""
	return attr, present
}

func normalizeAttribution(attr Attribution) Attribution {
	attr.RunID = strings.TrimSpace(attr.RunID)
	attr.ThreadID = strings.TrimSpace(attr.ThreadID)
	attr.JobID = strings.TrimSpace(attr.JobID)
	attr.PlanID = strings.TrimSpace(attr.PlanID)
	attr.Operation = Operation(strings.TrimSpace(string(attr.Operation)))
	attr.ConnectionID = firstNonEmpty(attr.ConnectionID, attr.ProviderConfigID)
	attr.ProfileID = strings.TrimSpace(attr.ProfileID)
	attr.ConfigFingerprint = strings.TrimSpace(attr.ConfigFingerprint)
	attr.PlannedAPISurface = firstNonEmpty(attr.PlannedAPISurface, attr.APIType)
	attr.ProviderConfigID = attr.ConnectionID
	attr.APIType = attr.PlannedAPISurface
	return attr
}

func applyAttribution(inv *Invocation, attr Attribution) {
	if inv == nil {
		return
	}
	attr = normalizeAttribution(attr)
	if inv.RunID == nil {
		inv.RunID = optionalString(attr.RunID)
	}
	if inv.ThreadID == nil {
		inv.ThreadID = optionalString(attr.ThreadID)
	}
	if inv.JobID == nil {
		inv.JobID = optionalString(attr.JobID)
	}
	if inv.PlanID == nil {
		inv.PlanID = optionalString(attr.PlanID)
	}
	if inv.Operation == "" {
		inv.Operation = attr.Operation
	}
	hadExplicitIdentity := inv.ConnectionID != nil || inv.ProviderConfigID != nil || inv.ProfileID != nil
	if !hadExplicitIdentity {
		if attr.ConnectionID != "" {
			inv.ConnectionID = optionalString(attr.ConnectionID)
		} else {
			inv.ProfileID = optionalString(attr.ProfileID)
		}
	}
	invConnection := firstNonEmpty(stringValue(inv.ConnectionID), stringValue(inv.ProviderConfigID))
	invProfile := stringValue(inv.ProfileID)
	identityMatches := !hadExplicitIdentity ||
		(invConnection != "" && invConnection == attr.ConnectionID && attr.ProfileID == "") ||
		(invProfile != "" && invProfile == attr.ProfileID && attr.ConnectionID == "")
	if inv.ConfigFingerprint == nil && identityMatches {
		inv.ConfigFingerprint = optionalString(attr.ConfigFingerprint)
	}
	if inv.PlannedAPISurface == nil && inv.APIType == nil && identityMatches {
		inv.PlannedAPISurface = optionalString(attr.PlannedAPISurface)
	}
}
