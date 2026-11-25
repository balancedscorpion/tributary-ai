package toolnamemapper

import (
	"regexp"
	"strings"
)

var sanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

type Mapper struct {
	providerToCanonical map[string]string
	canonicalToProvider map[string]string
	maxLength           int
}

func New(maxLength int) *Mapper {
	return &Mapper{
		providerToCanonical: map[string]string{},
		canonicalToProvider: map[string]string{},
		maxLength:           maxLength,
	}
}

func (m *Mapper) ProviderName(canonical string) string {
	c := strings.TrimSpace(canonical)
	if c == "" {
		c = "tool"
	}
	if existing, ok := m.canonicalToProvider[c]; ok {
		return existing
	}

	base := sanitize(c)
	suffix := ""
	candidate := m.applyMaxLength(base, suffix)
	attempt := 1
	for {
		cur, ok := m.providerToCanonical[candidate]
		if !ok || cur == c {
			break
		}
		suffix = "_" + itoa(attempt)
		candidate = m.applyMaxLength(base, suffix)
		attempt++
	}

	m.providerToCanonical[candidate] = c
	m.canonicalToProvider[c] = candidate
	return candidate
}

func (m *Mapper) CanonicalName(providerName string) string {
	p := strings.TrimSpace(providerName)
	if p == "" {
		return p
	}
	if c, ok := m.providerToCanonical[p]; ok {
		return c
	}
	return p
}

func sanitize(name string) string {
	s := sanitizer.ReplaceAllString(name, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "tool"
	}
	return s
}

func (m *Mapper) applyMaxLength(base, suffix string) string {
	max := m.maxLength
	if max <= 0 {
		return base + suffix
	}
	if len(base)+len(suffix) <= max {
		return base + suffix
	}
	if len(suffix) >= max {
		return suffix[len(suffix)-max:]
	}
	avail := max - len(suffix)
	trimmed := base
	if len(trimmed) > avail {
		trimmed = trimmed[:avail]
	}
	if trimmed == "" {
		trimmed = "tool"[:avail]
	}
	return trimmed + suffix
}

func itoa(i int) string {
	// tiny local int->string (avoids fmt in hot path)
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [32]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + (i % 10))
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
