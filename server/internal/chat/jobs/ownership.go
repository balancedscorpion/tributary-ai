package jobs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
)

const (
	ownerTokenPrefix = "tok_"
	ownerTokenBytes  = 16 // 128-bit entropy
)

func GenerateOwnerToken() (string, error) {
	b := make([]byte, ownerTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return ownerTokenPrefix + hex.EncodeToString(b), nil
}

func normalizeOwner(owner JobOwner) JobOwner {
	owner.ThreadID = strings.TrimSpace(owner.ThreadID)
	owner.RunID = strings.TrimSpace(owner.RunID)
	owner.UserToken = strings.TrimSpace(owner.UserToken)
	owner.Handle = strings.TrimSpace(owner.Handle)
	return owner
}

func validateOwnerForRegister(owner JobOwner) error {
	if strings.TrimSpace(owner.ThreadID) == "" {
		return errors.New("thread_id is required")
	}
	return nil
}

func ownerMatches(registered JobOwner, input JobOwner) bool {
	return strings.TrimSpace(registered.ThreadID) != "" &&
		strings.TrimSpace(registered.ThreadID) == strings.TrimSpace(input.ThreadID) &&
		strings.TrimSpace(registered.Handle) != "" &&
		strings.TrimSpace(registered.Handle) == strings.TrimSpace(input.Handle)
}
