package model

import (
	"crypto/rand"
	"fmt"
	"time"
)

// NewID generates a stable-ish random id. Prefix should be a short noun like "thread", "msg", or "run".
//
// IDs are ASCII and URL-safe without additional encoding.
func NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely, but avoid hard-failing id generation.
		return fmt.Sprintf("%s_%d", prefix, time.Now().UTC().UnixNano())
	}
	return fmt.Sprintf("%s_%x", prefix, b)
}
