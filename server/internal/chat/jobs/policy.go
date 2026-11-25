package jobs

import (
	"fmt"
	"time"
)

// PolicyConfig holds budget defaults. Values come from local AppSettings
// (wired by the orchestration slice); zero values take the defaults below.
// Soft breaches are diagnostic/wake signals only. Only the hard execution
// budget triggers automatic cancellation, terminalizing as expired.
type PolicyConfig struct {
	SoftQueue     time.Duration // default 10m
	SoftExecution time.Duration // default 30m
	HardExecution time.Duration // default 6h
	StallWindows  int           // default 3 consecutive windows
	StallWindow   time.Duration // default 2m
	// DefaultBudgets seed per-job budgets: display rows 2500, result bytes
	// 32MiB; MaxScanBytes has no global default (provider slices wire the
	// BigQuery 100GiB default from settings).
	DefaultBudgets Budgets
}

func (c PolicyConfig) withDefaults() PolicyConfig {
	if c.SoftQueue <= 0 {
		c.SoftQueue = 10 * time.Minute
	}
	if c.SoftExecution <= 0 {
		c.SoftExecution = 30 * time.Minute
	}
	if c.HardExecution <= 0 {
		c.HardExecution = 6 * time.Hour
	}
	if c.StallWindows <= 0 {
		c.StallWindows = 3
	}
	if c.StallWindow <= 0 {
		c.StallWindow = 2 * time.Minute
	}
	if c.DefaultBudgets.MaxResultRows <= 0 {
		c.DefaultBudgets.MaxResultRows = 2500
	}
	if c.DefaultBudgets.MaxResultBytes <= 0 {
		c.DefaultBudgets.MaxResultBytes = 32 << 20 // 32 MiB
	}
	if c.DefaultBudgets.SoftQueue <= 0 {
		c.DefaultBudgets.SoftQueue = c.SoftQueue
	}
	if c.DefaultBudgets.SoftExecution <= 0 {
		c.DefaultBudgets.SoftExecution = c.SoftExecution
	}
	if c.DefaultBudgets.HardExecution <= 0 {
		c.DefaultBudgets.HardExecution = c.HardExecution
	}
	return c
}

// ProfilePolicy bounds the pre-submit profiling phase. This is the entire
// profile policy contract: one independent total deadline. A profile NEVER
// inherits the query's hard execution budget; an adapter may only tighten
// the bound further via ProviderCapabilities.ProfileBound.
type ProfilePolicy struct {
	Timeout time.Duration // total bound across the whole Profile call; default 60s
}

func (p ProfilePolicy) withDefaults() ProfilePolicy {
	if p.Timeout <= 0 {
		p.Timeout = 60 * time.Second
	}
	return p
}

// effectiveProfileTimeout applies the adapter's optional tightening.
func (p ProfilePolicy) effectiveProfileTimeout(caps ProviderCapabilities) time.Duration {
	d := p.Timeout
	if caps.ProfileBound > 0 && caps.ProfileBound < d {
		d = caps.ProfileBound
	}
	return d
}

// policyInput is the locked snapshot of the fields policy evaluation needs.
type policyInput struct {
	budgets      Budgets
	submittedAt  time.Time
	startedAt    *time.Time // first observed executing (not queued)
	queued       bool
	lastProgress time.Time // last defensible telemetry advancement
	// hasSignals is true only once a real comparable baseline observation
	// exists (declared-but-never-extracted signals do not count).
	hasSignals bool
	// observations counts comparable snapshots since lastProgress in which
	// no declared signal advanced.
	observations  int
	providerState ProviderState
}

// policyVerdict is the outcome of one evaluation.
type policyVerdict struct {
	health     PolicyHealth
	wake       WakeKind // "" when no new wake signal warranted
	wakeDetail string
	hardCancel bool // request automatic cancellation (reason=budget)
}

// evaluatePolicy is pure: given the input and now, produce a verdict.
// Precedence: hard > suspected_stalled > soft execution > soft queue > ok.
func evaluatePolicy(cfg PolicyConfig, in policyInput, now time.Time) policyVerdict {
	b := in.budgets

	// Hard execution budget: measured from actual remote dispatch, because a queued
	// provider job is still consuming its lifetime (and possibly cost).
	if b.HardExecution > 0 && now.Sub(in.submittedAt) >= b.HardExecution {
		return policyVerdict{
			health:     PolicyHealthHardExceeded,
			wake:       WakeTerminal, // terminal wake is emitted by the expiry transition
			wakeDetail: fmt.Sprintf("hard execution budget %s exceeded", b.HardExecution),
			hardCancel: true,
		}
	}

	// Suspected stall: only with defensible telemetry — a real comparable
	// baseline must exist, the job must be executing (not queued), at
	// least StallWindows comparable observations showed no advancement,
	// AND the configured wall-clock windows elapsed. Absence of signals is
	// never evidence.
	if in.hasSignals && !in.queued && in.providerState == ProviderStateRunning && !in.lastProgress.IsZero() {
		stallFor := time.Duration(cfg.StallWindows) * cfg.StallWindow
		if in.observations >= cfg.StallWindows && now.Sub(in.lastProgress) >= stallFor {
			return policyVerdict{
				health:     PolicyHealthSuspectedStalled,
				wake:       WakeSuspectedStalled,
				wakeDetail: fmt.Sprintf("no telemetry progress across %d observations over %s", in.observations, now.Sub(in.lastProgress).Round(time.Second)),
			}
		}
	}

	// Soft execution: measured from first observed executing.
	if in.startedAt != nil && b.SoftExecution > 0 && now.Sub(*in.startedAt) >= b.SoftExecution {
		return policyVerdict{
			health:     PolicyHealthSoftExecution,
			wake:       WakeSoftExecution,
			wakeDetail: fmt.Sprintf("executing for %s (soft budget %s)", now.Sub(*in.startedAt).Round(time.Second), b.SoftExecution),
		}
	}

	// Soft queue: still queued (or never observed executing) past budget.
	if in.startedAt == nil && b.SoftQueue > 0 && now.Sub(in.submittedAt) >= b.SoftQueue {
		return policyVerdict{
			health:     PolicyHealthSoftQueue,
			wake:       WakeSoftQueue,
			wakeDetail: fmt.Sprintf("queued for %s (soft budget %s)", now.Sub(in.submittedAt).Round(time.Second), b.SoftQueue),
		}
	}

	return policyVerdict{health: PolicyHealthOK}
}

// progressAdvanced compares two telemetry observations and reports whether
// any declared signal moved forward. Wall-clock alone is never progress.
func progressAdvanced(prev, cur map[ProgressSignal]int64) bool {
	for sig, val := range cur {
		if pv, ok := prev[sig]; !ok || val > pv {
			return true
		}
	}
	return false
}

// signalsFromSnapshot extracts declared, provider-reported counters.
func signalsFromSnapshot(caps ProviderCapabilities, snap StatusSnapshot) map[ProgressSignal]int64 {
	out := map[ProgressSignal]int64{}
	if caps.hasSignal(SignalBytesProcessed) && snap.BytesProcessed != nil {
		out[SignalBytesProcessed] = *snap.BytesProcessed
	}
	if caps.hasSignal(SignalRowsProcessed) && snap.RowsProcessed != nil {
		out[SignalRowsProcessed] = *snap.RowsProcessed
	}
	if caps.hasSignal(SignalSplits) && snap.SplitsDone != nil {
		out[SignalSplits] = *snap.SplitsDone
	}
	if caps.hasSignal(SignalNativePercent) && snap.NativeProgressPercent != nil {
		out[SignalNativePercent] = int64(*snap.NativeProgressPercent * 100)
	}
	return out
}
