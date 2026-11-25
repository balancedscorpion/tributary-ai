package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"ai-analyst-local/internal/chat/jobs"
)

func queryJobIdentityNeedsRepair(id string) bool {
	id = strings.TrimSpace(id)
	return id == "" || jobs.IsQueryJobIDSentinel(id) ||
		(strings.HasPrefix(strings.ToLower(id), jobs.ManagedQueryJobIDPrefix) && !jobs.IsValidManagedQueryJobID(id))
}

func repairedManagedQueryJobID(record jobs.JobRecord) string {
	material := strings.Join([]string{
		strings.TrimSpace(record.ID),
		strings.ToLower(strings.TrimSpace(record.Provider)),
		strings.TrimSpace(record.ConnectionID),
		strings.TrimSpace(record.Owner.ThreadID),
		strings.TrimSpace(record.Owner.RunID),
		strings.TrimSpace(record.IdempotencyKey),
		record.SubmittedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, "\x00")
	digest := sha256.Sum256([]byte(material))
	return "qj-repaired-" + hex.EncodeToString(digest[:16])
}

func queryJobUsesManagedIdentity(record jobs.JobRecord) bool {
	id := strings.TrimSpace(record.ID)
	return strings.HasPrefix(strings.ToLower(id), jobs.ManagedQueryJobIDPrefix)
}

// validateQueryJobRecordIdentity applies to new writes. Unlike the read-side
// sanitizer, writes never reinterpret a placeholder as an empty predecessor:
// callers must supply the durable identity they actually intend to persist.
func validateQueryJobRecordIdentity(record jobs.JobRecord) error {
	return jobs.ValidateDurableQueryJobIdentity(record)
}

// sanitizeLegacyQueryJobRecordIdentity keeps recoverable historical jobs
// monitorable. A sentinel predecessor conveys no usable lineage, so it is
// safely treated as absent. A sentinel/malformed primary id cannot be renamed
// without rewriting every dependent row and is instead quarantined.
func sanitizeLegacyQueryJobRecordIdentity(record *jobs.JobRecord) error {
	if record == nil {
		return fmt.Errorf("nil query job record: %w", ErrQuarantinedQueryJobIdentity)
	}
	id := strings.TrimSpace(record.ID)
	if id == "" || jobs.IsQueryJobIDSentinel(id) {
		return fmt.Errorf("query job has an unusable primary id %q: %w", record.ID, ErrQuarantinedQueryJobIdentity)
	}
	strict := queryJobUsesManagedIdentity(*record)
	if strict {
		normalized, err := jobs.NormalizeManagedQueryJobID(record.ID)
		if err != nil || normalized != record.ID {
			return fmt.Errorf("query job has a malformed managed primary id %q: %w", record.ID, ErrQuarantinedQueryJobIdentity)
		}
	}

	replaces := strings.TrimSpace(record.ReplacesJobID)
	if replaces == "" {
		record.ReplacesJobID = ""
		return nil
	}
	if jobs.IsQueryJobIDSentinel(replaces) {
		// Legacy writers used nil/zero placeholders to mean "no predecessor".
		// Clearing only that unusable edge preserves the current running job.
		record.ReplacesJobID = ""
		return nil
	}
	if replaces == id {
		return fmt.Errorf("query job %q replaces itself: %w", id, ErrQuarantinedQueryJobIdentity)
	}
	if strings.HasPrefix(strings.ToLower(replaces), jobs.ManagedQueryJobIDPrefix) {
		normalized, err := jobs.NormalizeManagedQueryJobID(replaces)
		if err != nil {
			return fmt.Errorf("query job %q has malformed replacement id %q: %w", id, record.ReplacesJobID, ErrQuarantinedQueryJobIdentity)
		}
		replaces = normalized
	}
	record.ReplacesJobID = replaces
	return nil
}

func validateQueryJobIdentityValue(value, field string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if jobs.IsQueryJobIDSentinel(value) {
		return fmt.Errorf("%s must not be a nil or all-zero sentinel", field)
	}
	if strings.HasPrefix(strings.ToLower(value), jobs.ManagedQueryJobIDPrefix) && !jobs.IsValidManagedQueryJobID(value) {
		return fmt.Errorf("%s has a malformed managed query job id", field)
	}
	return nil
}

// sanitizeLegacyRunQueryDependencyIdentity applies the same compatibility
// policy as query-job lineage. The dependency's JobID is an authoritative
// primary identity and cannot be guessed, while a sentinel successor was a
// legacy spelling of "no replacement" and is safe to clear in memory.
func sanitizeLegacyRunQueryDependencyIdentity(dependency *RunQueryDependency) error {
	if dependency == nil {
		return fmt.Errorf("nil run query dependency: %w", ErrQuarantinedQueryJobIdentity)
	}
	dependency.JobID = strings.TrimSpace(dependency.JobID)
	if err := validateQueryJobIdentityValue(dependency.JobID, "dependency job_id"); err != nil {
		return fmt.Errorf("%v: %w", err, ErrQuarantinedQueryJobIdentity)
	}
	dependency.ReplacementJobID = strings.TrimSpace(dependency.ReplacementJobID)
	if dependency.ReplacementJobID == "" {
		return nil
	}
	if jobs.IsQueryJobIDSentinel(dependency.ReplacementJobID) {
		if dependency.Detached {
			return fmt.Errorf("detached dependency has an unusable successor id %q: %w", dependency.ReplacementJobID, ErrQuarantinedQueryJobIdentity)
		}
		dependency.ReplacementJobID = ""
		return nil
	}
	if err := validateQueryJobIdentityValue(dependency.ReplacementJobID, "dependency replacement_job_id"); err != nil {
		return fmt.Errorf("%v: %w", err, ErrQuarantinedQueryJobIdentity)
	}
	if dependency.ReplacementJobID == dependency.JobID {
		return fmt.Errorf("dependency job cannot replace itself: %w", ErrQuarantinedQueryJobIdentity)
	}
	return nil
}
