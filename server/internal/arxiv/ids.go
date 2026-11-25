// Package arxiv implements a polite, allowlisted client for the public arXiv
// export API plus local storage for downloaded papers.
package arxiv

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	// New-style identifiers: YYMM.NNNNN (4 or 5 digit sequence), e.g. 2301.12345.
	newStyleIDRE = regexp.MustCompile(`^\d{4}\.\d{4,5}$`)
	// Old-style identifiers after lowering: archive(.SC)?/YYMMNNN, e.g.
	// hep-th/9901001 or math.gt/0309136.
	oldStyleIDRE = regexp.MustCompile(`^[a-z-]+(\.[a-z]{2})?/\d{7}$`)
	// Optional trailing version marker, e.g. ...v3.
	versionSuffixRE = regexp.MustCompile(`^(.+)v([0-9]+)$`)
	// Filesystem-safe paper keys produced by PaperKey.
	paperKeyRE = regexp.MustCompile(`^[a-z0-9._-]+$`)
)

// idURLHosts are the hosts accepted when an arXiv ID is supplied as an
// abs/pdf URL.
var idURLHosts = map[string]bool{
	"arxiv.org":        true,
	"www.arxiv.org":    true,
	"export.arxiv.org": true,
}

// CanonicalID validates raw as an arXiv identifier and returns the lowercase
// canonical versionless ID plus the version number (0 when no version was
// given). It accepts new-style (2301.12345, 2301.12345v3), old-style
// (math/0309136, hep-th/9901001v2, math.GT/0309136), an optional
// case-insensitive "arXiv:" prefix, and abs/pdf URL forms on arxiv.org hosts.
func CanonicalID(raw string) (string, int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", 0, errors.New("arxiv id is empty")
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return "", 0, errors.New("arxiv id contains control characters")
		}
	}
	id := strings.ToLower(trimmed)
	id = strings.TrimPrefix(id, "arxiv:")

	if strings.Contains(id, "://") {
		extracted, err := idFromURL(id)
		if err != nil {
			return "", 0, err
		}
		id = extracted
	} else if strings.HasPrefix(id, "/abs/") || strings.HasPrefix(id, "/pdf/") {
		extracted, err := idFromPath(id)
		if err != nil {
			return "", 0, err
		}
		id = extracted
	}

	version := 0
	if m := versionSuffixRE.FindStringSubmatch(id); m != nil {
		v, err := strconv.Atoi(m[2])
		if err == nil && v >= 1 && v <= 9999 {
			id = m[1]
			version = v
		}
	}

	if newStyleIDRE.MatchString(id) || oldStyleIDRE.MatchString(id) {
		return id, version, nil
	}
	return "", 0, fmt.Errorf("invalid arxiv id: %q", strings.TrimSpace(raw))
}

func idFromURL(lowered string) (string, error) {
	u, err := url.Parse(lowered)
	if err != nil {
		return "", fmt.Errorf("invalid arxiv url: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errors.New("arxiv url must use http or https")
	}
	if !idURLHosts[u.Host] {
		return "", fmt.Errorf("arxiv url host %q is not arxiv.org", u.Host)
	}
	return idFromPath(u.Path)
}

func idFromPath(path string) (string, error) {
	rest := ""
	switch {
	case strings.HasPrefix(path, "/abs/"):
		rest = strings.TrimPrefix(path, "/abs/")
	case strings.HasPrefix(path, "/pdf/"):
		rest = strings.TrimPrefix(path, "/pdf/")
		rest = strings.TrimSuffix(rest, ".pdf")
	default:
		return "", errors.New("arxiv url path must start with /abs/ or /pdf/")
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return "", errors.New("arxiv url is missing an id")
	}
	return rest, nil
}

// PaperKey converts a canonical arXiv ID into a filesystem-safe key by
// replacing "/" with "_". Valid arXiv IDs never contain "_", so the mapping
// is injective.
func PaperKey(id string) string {
	return strings.ReplaceAll(id, "/", "_")
}

// KeyToID is the inverse of PaperKey. It rejects keys that do not map back to
// a valid canonical arXiv ID.
func KeyToID(key string) (string, error) {
	if !ValidPaperKey(key) {
		return "", fmt.Errorf("invalid paper key: %q", key)
	}
	id, _, err := CanonicalID(strings.ReplaceAll(key, "_", "/"))
	if err != nil {
		return "", fmt.Errorf("invalid paper key: %q", key)
	}
	return id, nil
}

// ValidPaperKey reports whether key has the shape produced by PaperKey:
// lowercase alphanumerics plus "._-", without path separators, "..", or
// dot-only names such as "." (which would resolve to the papers root).
func ValidPaperKey(key string) bool {
	if key == "" || strings.Contains(key, "..") {
		return false
	}
	if strings.Trim(key, ".") == "" {
		return false
	}
	return paperKeyRE.MatchString(key)
}
