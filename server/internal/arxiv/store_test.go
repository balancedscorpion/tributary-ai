package arxiv

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testPaper(key string, addedAt int64) *Paper {
	id := strings.ReplaceAll(key, "_", "/")
	return &Paper{
		PaperKey:        key,
		ArxivID:         id,
		Version:         2,
		Title:           "A Title for " + id,
		Summary:         "Summary text.",
		Authors:         []string{"Jane Doe", "John Public"},
		Categories:      []string{"cs.LG"},
		PrimaryCategory: "cs.LG",
		Published:       time.Date(2023, 1, 28, 18, 0, 0, 0, time.UTC),
		Updated:         time.Date(2023, 2, 1, 10, 0, 0, 0, time.UTC),
		AddedAt:         addedAt,
		PDFState:        PDFStateStored,
		PDFSizeBytes:    1234,
		PDFSHA256:       "abc",
		ExtractState:    ExtractStateExtracted,
		ExtractQuality:  "pypdf",
		TextChars:       4321,
		AbsURL:          "https://arxiv.org/abs/" + id,
		PDFURL:          "https://arxiv.org/pdf/" + id,
	}
}

func TestStoreRoundTrip(t *testing.T) {
	store := NewStore(t.TempDir())

	first := testPaper("2301.12345", 100)
	second := testPaper("math_0309136", 200)
	if err := store.SavePaper(first); err != nil {
		t.Fatalf("SavePaper(first): %v", err)
	}
	if err := store.SavePaper(second); err != nil {
		t.Fatalf("SavePaper(second): %v", err)
	}

	got, err := store.GetPaper("2301.12345")
	if err != nil {
		t.Fatalf("GetPaper: %v", err)
	}
	if got.ArxivID != "2301.12345" || got.Title != first.Title || got.PDFSHA256 != "abc" || len(got.Authors) != 2 {
		t.Fatalf("GetPaper = %#v, want roundtripped record", got)
	}

	papers, err := store.ListPapers()
	if err != nil {
		t.Fatalf("ListPapers: %v", err)
	}
	if len(papers) != 2 || papers[0].PaperKey != "math_0309136" || papers[1].PaperKey != "2301.12345" {
		t.Fatalf("ListPapers order = %#v, want added_at desc", papers)
	}

	if store.StorageBytes() <= 0 {
		t.Fatalf("StorageBytes = %d, want > 0", store.StorageBytes())
	}

	if err := store.DeletePaper("2301.12345"); err != nil {
		t.Fatalf("DeletePaper: %v", err)
	}
	if _, err := store.GetPaper("2301.12345"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("GetPaper after delete = %v, want fs.ErrNotExist", err)
	}
	papers, err = store.ListPapers()
	if err != nil || len(papers) != 1 {
		t.Fatalf("ListPapers after delete = (%#v, %v), want single entry", papers, err)
	}
}

func TestStoreRejectsHostileKeys(t *testing.T) {
	cfgDir := t.TempDir()
	store := NewStore(cfgDir)
	hostile := []string{"", ".", "..", "...", "../escape", "a/b", `a\b`, "UPPER", "key with space", "x..y", "nul\x00l"}
	for _, key := range hostile {
		if err := store.SavePaper(testPaper("2301.12345", 1)); err != nil {
			t.Fatalf("seed SavePaper: %v", err)
		}
		if _, err := store.GetPaper(key); err == nil {
			t.Fatalf("GetPaper(%q) succeeded, want error", key)
		}
		if err := store.DeletePaper(key); err == nil {
			t.Fatalf("DeletePaper(%q) succeeded, want error", key)
		}
		if _, err := store.PaperPDFPath(key); err == nil {
			t.Fatalf("PaperPDFPath(%q) succeeded, want error", key)
		}
		if _, err := store.PaperTextPath(key); err == nil {
			t.Fatalf("PaperTextPath(%q) succeeded, want error", key)
		}
		bad := testPaper("2301.12345", 1)
		bad.PaperKey = key
		if err := store.SavePaper(bad); err == nil {
			t.Fatalf("SavePaper with key %q succeeded, want error", key)
		}
	}
	// Nothing outside the store dir should have been touched.
	if _, err := os.Stat(filepath.Join(cfgDir, "..", "escape")); err == nil {
		t.Fatalf("hostile key escaped the store directory")
	}
}

func TestStorePathsStayUnderRoot(t *testing.T) {
	cfgDir := t.TempDir()
	store := NewStore(cfgDir)
	pdfPath, err := store.PaperPDFPath("math_0309136")
	if err != nil {
		t.Fatalf("PaperPDFPath: %v", err)
	}
	textPath, err := store.PaperTextPath("math_0309136")
	if err != nil {
		t.Fatalf("PaperTextPath: %v", err)
	}
	root := filepath.Join(cfgDir, "arxiv")
	for _, p := range []string{pdfPath, textPath} {
		rel, err := filepath.Rel(root, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			t.Fatalf("path %q escapes store root %q", p, root)
		}
	}
	if filepath.Base(pdfPath) != "paper.pdf" {
		t.Fatalf("pdf path = %q, want paper.pdf leaf", pdfPath)
	}
	if filepath.Base(textPath) != "text.txt" || filepath.Base(filepath.Dir(textPath)) != "extract" {
		t.Fatalf("text path = %q, want extract/text.txt leaf", textPath)
	}
}
