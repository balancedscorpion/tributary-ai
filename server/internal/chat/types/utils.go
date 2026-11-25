package types

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

const MaxCharLimit = 100000

func SafeTruncate(s string) string {
	if len(s) <= MaxCharLimit {
		return s
	}
	return fmt.Sprintf("%s... [Truncated by server: %d chars]", s[:MaxCharLimit], len(s))
}

func NewUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// fallback: non-crypto, but should be extremely rare.
		return fmt.Sprintf("%x", b)
	}
	// Set version (4) and variant bits.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}
