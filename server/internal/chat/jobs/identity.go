package jobs

import (
	"errors"
	"strings"
)

const (
	// ManagedQueryJobIDPrefix distinguishes application-owned durable query
	// identities from provider-native statement, query, and job ids.
	ManagedQueryJobIDPrefix = "qj-"
	// MaxManagedQueryJobIDLength bounds identities accepted from persisted
	// records and tool output. Generated ids are substantially shorter.
	MaxManagedQueryJobIDLength = 96
)

var errInvalidManagedQueryJobID = errors.New("managed query job id must be a non-sentinel qj-* id containing only ASCII letters, digits, '-' or '_'")

// IsQueryJobIDSentinel identifies legacy placeholder values that were
// occasionally persisted in place of a real job identity. The all-zero form
// intentionally ignores the separators used by UUID- and warehouse-shaped
// placeholders. It does not classify an ordinary provider-native id.
func IsQueryJobIDSentinel(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, ManagedQueryJobIDPrefix) {
		lower = strings.TrimPrefix(lower, ManagedQueryJobIDPrefix)
	}
	switch lower {
	case "nil", "null", "<nil>", "none", "undefined":
		return true
	}
	foundZero := false
	for _, char := range lower {
		switch char {
		case '0':
			foundZero = true
		case '-', '_':
		default:
			return false
		}
	}
	return foundZero
}

// NormalizeManagedQueryJobID validates and returns the canonical trimmed form
// of an application-owned durable query identity. Provider-native ids and
// legacy nil/all-zero placeholders are deliberately rejected.
func NormalizeManagedQueryJobID(value string) (string, error) {
	id := strings.TrimSpace(value)
	if len(id) <= len(ManagedQueryJobIDPrefix) || len(id) > MaxManagedQueryJobIDLength ||
		!strings.HasPrefix(id, ManagedQueryJobIDPrefix) || IsQueryJobIDSentinel(id) {
		return "", errInvalidManagedQueryJobID
	}
	payload := strings.TrimPrefix(id, ManagedQueryJobIDPrefix)
	hasNonZeroAlphaNumeric := false
	for index := 0; index < len(payload); index++ {
		char := payload[index]
		alphaNumeric := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
		if index == 0 && !alphaNumeric {
			return "", errInvalidManagedQueryJobID
		}
		if !alphaNumeric && char != '-' && char != '_' {
			return "", errInvalidManagedQueryJobID
		}
		if alphaNumeric && char != '0' {
			hasNonZeroAlphaNumeric = true
		}
	}
	if !hasNonZeroAlphaNumeric {
		return "", errInvalidManagedQueryJobID
	}
	return id, nil
}

// IsValidManagedQueryJobID reports whether value is an application-owned
// durable query identity.
func IsValidManagedQueryJobID(value string) bool {
	_, err := NormalizeManagedQueryJobID(value)
	return err == nil
}

// ValidateDurableQueryJobIdentity validates the immutable identity fields of
// a record before a store writes it. Legacy generic job-* clients remain
// supported even when later marked for obligation recovery, while any actual
// qj-* identity is held to the application-managed format.
func ValidateDurableQueryJobIdentity(record JobRecord) error {
	id := strings.TrimSpace(record.ID)
	if id == "" {
		return errors.New("query job id is required")
	}
	if IsQueryJobIDSentinel(id) {
		return errors.New("query job id must not be a nil or all-zero sentinel")
	}
	if strings.HasPrefix(strings.ToLower(id), ManagedQueryJobIDPrefix) {
		normalized, err := NormalizeManagedQueryJobID(record.ID)
		if err != nil || normalized != record.ID {
			return errors.New("managed query job id is invalid or non-canonical")
		}
	}

	replaces := strings.TrimSpace(record.ReplacesJobID)
	if replaces == "" {
		return nil
	}
	if IsQueryJobIDSentinel(replaces) {
		return errors.New("replaces_job_id must not be a nil or all-zero sentinel")
	}
	if replaces == id {
		return errors.New("query job cannot replace itself")
	}
	if strings.HasPrefix(strings.ToLower(replaces), ManagedQueryJobIDPrefix) {
		normalized, err := NormalizeManagedQueryJobID(record.ReplacesJobID)
		if err != nil || normalized != record.ReplacesJobID {
			return errors.New("managed replaces_job_id is invalid or non-canonical")
		}
	}
	return nil
}
