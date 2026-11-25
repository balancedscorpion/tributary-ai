package arxiv

import "testing"

func TestCanonicalID(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantID      string
		wantVersion int
		wantErr     bool
	}{
		{name: "new style", in: "2301.12345", wantID: "2301.12345"},
		{name: "new style four digit sequence", in: "0704.0001", wantID: "0704.0001"},
		{name: "new style with version", in: "2301.12345v3", wantID: "2301.12345", wantVersion: 3},
		{name: "new style uppercase version marker", in: "2301.12345V3", wantID: "2301.12345", wantVersion: 3},
		{name: "old style", in: "math/0309136", wantID: "math/0309136"},
		{name: "old style with version", in: "hep-th/9901001v2", wantID: "hep-th/9901001", wantVersion: 2},
		{name: "old style with subject class", in: "math.GT/0309136", wantID: "math.gt/0309136"},
		{name: "arxiv prefix", in: "arXiv:2301.12345", wantID: "2301.12345"},
		{name: "arxiv prefix uppercase", in: "ARXIV:2301.12345v1", wantID: "2301.12345", wantVersion: 1},
		{name: "abs url", in: "https://arxiv.org/abs/2301.12345v2", wantID: "2301.12345", wantVersion: 2},
		{name: "pdf url", in: "https://arxiv.org/pdf/2301.12345v2", wantID: "2301.12345", wantVersion: 2},
		{name: "pdf url with extension", in: "https://arxiv.org/pdf/2301.12345v2.pdf", wantID: "2301.12345", wantVersion: 2},
		{name: "export host url", in: "https://export.arxiv.org/abs/hep-th/9901001", wantID: "hep-th/9901001"},
		{name: "www host url", in: "http://www.arxiv.org/abs/math/0309136v1", wantID: "math/0309136", wantVersion: 1},
		{name: "uppercase url lowers to canonical", in: "HTTPS://ARXIV.ORG/ABS/2301.12345V2", wantID: "2301.12345", wantVersion: 2},
		{name: "bare pdf path", in: "/pdf/2301.12345", wantID: "2301.12345"},
		{name: "surrounding whitespace", in: "  2301.12345v1  ", wantID: "2301.12345", wantVersion: 1},
		{name: "old style url with version in pdf", in: "https://arxiv.org/pdf/math.GT/0309136v2", wantID: "math.gt/0309136", wantVersion: 2},

		{name: "empty", in: "", wantErr: true},
		{name: "whitespace only", in: "   ", wantErr: true},
		{name: "underscore in archive", in: "a_b/1234567", wantErr: true},
		{name: "path traversal", in: "../etc", wantErr: true},
		{name: "traversal in old style", in: "math/../1234567", wantErr: true},
		{name: "null byte", in: "2301.12345\x00", wantErr: true},
		{name: "control char", in: "2301.\x0712345", wantErr: true},
		{name: "wrong digit count", in: "2301.123", wantErr: true},
		{name: "six digit sequence", in: "2301.123456", wantErr: true},
		{name: "three digit year month", in: "231.12345", wantErr: true},
		{name: "old style short number", in: "math/030913", wantErr: true},
		{name: "old style long number", in: "math/03091366", wantErr: true},
		{name: "old style digits in archive", in: "ma7h/0309136", wantErr: true},
		{name: "old style long subject class", in: "math.gtx/0309136", wantErr: true},
		{name: "version zero rejected", in: "2301.12345v0", wantErr: true},
		{name: "double slash", in: "math//0309136", wantErr: true},
		{name: "wrong host url", in: "https://evil.example.com/abs/2301.12345", wantErr: true},
		{name: "wrong scheme url", in: "ftp://arxiv.org/abs/2301.12345", wantErr: true},
		{name: "wrong path url", in: "https://arxiv.org/list/2301.12345", wantErr: true},
		{name: "url without id", in: "https://arxiv.org/abs/", wantErr: true},
		{name: "random text", in: "not an id", wantErr: true},
		{name: "embedded space", in: "2301. 12345", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, version, err := CanonicalID(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CanonicalID(%q) = (%q, %d, nil), want error", tc.in, id, version)
				}
				return
			}
			if err != nil {
				t.Fatalf("CanonicalID(%q) error: %v", tc.in, err)
			}
			if id != tc.wantID || version != tc.wantVersion {
				t.Fatalf("CanonicalID(%q) = (%q, %d), want (%q, %d)", tc.in, id, version, tc.wantID, tc.wantVersion)
			}
		})
	}
}

func TestPaperKeyRoundTrip(t *testing.T) {
	cases := []struct {
		id      string
		wantKey string
	}{
		{id: "2301.12345", wantKey: "2301.12345"},
		{id: "math/0309136", wantKey: "math_0309136"},
		{id: "hep-th/9901001", wantKey: "hep-th_9901001"},
		{id: "math.gt/0309136", wantKey: "math.gt_0309136"},
	}
	for _, tc := range cases {
		key := PaperKey(tc.id)
		if key != tc.wantKey {
			t.Fatalf("PaperKey(%q) = %q, want %q", tc.id, key, tc.wantKey)
		}
		back, err := KeyToID(key)
		if err != nil {
			t.Fatalf("KeyToID(%q) error: %v", key, err)
		}
		if back != tc.id {
			t.Fatalf("KeyToID(%q) = %q, want %q", key, back, tc.id)
		}
	}
}

func TestKeyToIDRejectsHostileKeys(t *testing.T) {
	hostile := []string{
		"",
		".",
		"..",
		"...",
		"a..b",
		"../etc",
		"math/0309136",
		"math_0309136/../../etc",
		"key with space",
		"UPPER_0309136",
		"null\x00byte",
		"not_an_id",
	}
	for _, key := range hostile {
		if _, err := KeyToID(key); err == nil {
			t.Fatalf("KeyToID(%q) succeeded, want error", key)
		}
	}
}

func TestValidPaperKey(t *testing.T) {
	if !ValidPaperKey("math_0309136") || !ValidPaperKey("2301.12345") {
		t.Fatalf("expected canonical keys to validate")
	}
	for _, key := range []string{"", ".", "..", "...", "a/b", "a\\b", "A1234", "a b", "x..y"} {
		if ValidPaperKey(key) {
			t.Fatalf("ValidPaperKey(%q) = true, want false", key)
		}
	}
}
