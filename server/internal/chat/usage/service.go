package usage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	maxDailyReportDays        = 366
	maxDailyReportInvocations = 50_000
	maxDailyReportBuckets     = 5_000
)

type DailyQuery struct {
	From     time.Time
	To       time.Time
	TimeZone string

	ProviderID          string
	ConnectionID        string
	ProfileID           string
	Model               string
	Operation           Operation
	Status              Status
	PlannedAPISurface   string
	EffectiveAPISurface string

	ProviderConfigID string // Deprecated alias for ConnectionID.
}

type DailyBucket struct {
	Date                 string      `json:"date"`
	ProviderID           string      `json:"provider_id"`
	ConnectionID         string      `json:"connection_id,omitempty"`
	ProfileID            string      `json:"profile_id,omitempty"`
	PlanID               string      `json:"plan_id,omitempty"`
	ConfigFingerprint    string      `json:"config_fingerprint,omitempty"`
	Operation            Operation   `json:"operation"`
	PlannedAPISurface    string      `json:"planned_api_surface,omitempty"`
	EffectiveAPISurface  string      `json:"effective_api_surface,omitempty"`
	Model                string      `json:"model"`
	UpstreamRequestCount int64       `json:"upstream_request_count"`
	Totals               TokenTotals `json:"totals"`
}

type DailyReport struct {
	SchemaVersion     int             `json:"schema_version"`
	ReportComplete    bool            `json:"report_complete"`
	Health            ReportHealth    `json:"health"`
	Retention         RetentionPolicy `json:"retention"`
	From              time.Time       `json:"from"`
	To                time.Time       `json:"to"`
	TimeZone          string          `json:"timezone"`
	Totals            TokenTotals     `json:"totals"`
	Days              []DailyBucket   `json:"days"`
	TrackingStartedAt *time.Time      `json:"tracking_started_at,omitempty"`
	GeneratedAt       time.Time       `json:"generated_at"`
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository, now: func() time.Time { return time.Now().UTC() }}
}

func unavailableReport(now time.Time, message string) DailyReport {
	return DailyReport{SchemaVersion: ReportSchemaVersion, ReportComplete: false,
		Health:    ReportHealth{Status: HealthUnavailable, Message: messagePointer(message)},
		Retention: IndefiniteRetention, GeneratedAt: now.UTC(), Days: []DailyBucket{}}
}

// ReconcileRunning marks invocations left open by a previous process as
// interrupted while preserving any counters already journaled by Snapshot.
func (s *Service) ReconcileRunning(ctx context.Context, interruptedAt time.Time) (int, error) {
	if s == nil || s.repository == nil {
		return 0, errors.New("nil usage repository")
	}
	if interruptedAt.IsZero() {
		interruptedAt = s.now()
	}
	interruptedAt = interruptedAt.UTC()
	running, err := s.repository.List(ctx, Query{Status: StatusRunning, Limit: MaxQueryLimit})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, invocation := range running {
		endedAt := interruptedAt
		if endedAt.Before(invocation.UpdatedAt) {
			endedAt = invocation.UpdatedAt
		}
		code := "PROCESS_RESTART"
		if _, err := s.repository.Finalize(ctx, invocation.ID, Finalization{
			Sequence: invocation.SnapshotSequence + 1, Status: StatusInterrupted, EndedAt: endedAt,
			UsageQuality: invocation.UsageQuality, UpstreamRequestCount: invocation.UpstreamRequestCount, ErrorCode: &code,
		}); err != nil {
			return count, fmt.Errorf("reconcile usage invocation %s: %w", invocation.ID, err)
		}
		count++
	}
	return count, nil
}

// Daily groups terminal invocations by ended-at date and canonical provider
// identity. Safety limits produce an explicitly incomplete/degraded report,
// never a silently complete-looking partial answer.
func (s *Service) Daily(ctx context.Context, query DailyQuery) (DailyReport, error) {
	if s == nil || s.repository == nil {
		return unavailableReport(time.Now(), "usage reporting is unavailable"), errors.New("nil usage repository")
	}
	if query.From.IsZero() || query.To.IsZero() {
		return DailyReport{}, errors.New("daily usage from and to are required")
	}
	if !query.From.Before(query.To) {
		return DailyReport{}, errors.New("daily usage from must precede to")
	}
	location, err := loadLocation(query.TimeZone)
	if err != nil {
		return DailyReport{}, err
	}
	if days := reportCalendarDays(query.From.In(location), query.To.In(location)); days <= 0 || days > maxDailyReportDays {
		return DailyReport{}, fmt.Errorf("daily usage range must contain between 1 and %d days", maxDailyReportDays)
	}
	connectionID := firstNonEmpty(query.ConnectionID, query.ProviderConfigID)
	invocations, err := s.repository.List(ctx, Query{
		From: query.From, To: query.To, RangeField: RangeByEndedAt, Operation: query.Operation,
		ProviderID: query.ProviderID, ConnectionID: connectionID, ProfileID: query.ProfileID,
		PlannedAPISurface: query.PlannedAPISurface, EffectiveAPISurface: query.EffectiveAPISurface,
		ResolvedModel: query.Model, Status: query.Status, Limit: maxDailyReportInvocations + 1,
	})
	if err != nil {
		return unavailableReport(s.now(), "usage ledger could not be read"), err
	}
	generatedAt := s.now().UTC()
	health, healthErr := s.repository.Health(ctx, generatedAt)
	if healthErr != nil {
		health = ReportHealth{Status: HealthUnavailable, Message: messagePointer("usage ledger health is unavailable")}
	}
	reportComplete := health.Status == HealthHealthy
	if len(invocations) > maxDailyReportInvocations {
		invocations = invocations[:maxDailyReportInvocations]
		reportComplete = false
		health = degradeHealth(health, "usage report invocation limit reached")
	}
	earliest, earliestErr := s.repository.EarliestTrackingDate(ctx)
	if earliestErr != nil {
		reportComplete = false
		health = degradeHealth(health, "usage tracking start date is unavailable")
	}

	// Aggregate only on dimensions exposed by the report/UI. Plan IDs are
	// normally unique per run and fingerprints can change over time; including
	// either would create visually duplicate rows and exhaust the bucket limit.
	type bucketKey struct{ date, providerID, connectionID, profileID, operation, planned, effective, model string }
	buckets := make(map[bucketKey]*DailyBucket)
	var totals TokenTotals
	bucketOverflow := false
	for _, inv := range invocations {
		if inv.EndedAt == nil {
			continue
		}
		key := bucketKey{inv.EndedAt.In(location).Format("2006-01-02"), inv.ProviderID, stringValue(inv.ConnectionID), stringValue(inv.ProfileID),
			string(inv.Operation), stringValue(inv.PlannedAPISurface),
			stringValue(inv.EffectiveAPISurface), inv.DisplayModel()}
		bucket := buckets[key]
		if bucket == nil {
			if len(buckets) >= maxDailyReportBuckets {
				bucketOverflow = true
				continue
			}
			bucket = &DailyBucket{Date: key.date, ProviderID: key.providerID, ConnectionID: key.connectionID, ProfileID: key.profileID,
				Operation: Operation(key.operation), PlannedAPISurface: key.planned,
				EffectiveAPISurface: key.effective, Model: key.model}
			buckets[key] = bucket
		}
		totals.AddInvocation(inv)
		bucket.UpstreamRequestCount += inv.UpstreamRequestCount
		bucket.Totals.AddInvocation(inv)
	}
	if bucketOverflow {
		reportComplete = false
		health = degradeHealth(health, "usage report bucket limit reached")
	}
	days := make([]DailyBucket, 0, len(buckets))
	for _, bucket := range buckets {
		days = append(days, *bucket)
	}
	sort.Slice(days, func(i, j int) bool {
		a, b := days[i], days[j]
		if a.Date != b.Date {
			return a.Date < b.Date
		}
		if a.ProviderID != b.ProviderID {
			return a.ProviderID < b.ProviderID
		}
		if a.ConnectionID != b.ConnectionID {
			return a.ConnectionID < b.ConnectionID
		}
		if a.ProfileID != b.ProfileID {
			return a.ProfileID < b.ProfileID
		}
		if a.Operation != b.Operation {
			return a.Operation < b.Operation
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.PlannedAPISurface != b.PlannedAPISurface {
			return a.PlannedAPISurface < b.PlannedAPISurface
		}
		return a.EffectiveAPISurface < b.EffectiveAPISurface
	})
	return DailyReport{SchemaVersion: ReportSchemaVersion, ReportComplete: reportComplete, Health: health, Retention: IndefiniteRetention,
		From: query.From.UTC(), To: query.To.UTC(), TimeZone: location.String(), Totals: totals, Days: days,
		TrackingStartedAt: earliest, GeneratedAt: generatedAt}, nil
}

func degradeHealth(health ReportHealth, message string) ReportHealth {
	if health.Status != HealthUnavailable {
		health.Status = HealthDegraded
	}
	health.Message = messagePointer(message)
	return health
}

func reportCalendarDays(from, toExclusive time.Time) int {
	from = dateOnly(from)
	toDate := dateOnly(toExclusive)
	if !toExclusive.Equal(toDate) {
		toDate = toDate.AddDate(0, 0, 1)
	}
	toExclusive = toDate
	count := 0
	for cursor := from; cursor.Before(toExclusive); cursor = cursor.AddDate(0, 0, 1) {
		count++
		if count > maxDailyReportDays {
			break
		}
	}
	return count
}

func loadLocation(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "UTC") {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("invalid usage timezone %q: %w", name, err)
	}
	return location, nil
}
