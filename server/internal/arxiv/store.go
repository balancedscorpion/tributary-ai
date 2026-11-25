package arxiv

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/atomicfile"
)

// PDF states.
const (
	PDFStateNone   = "none"
	PDFStateStored = "stored"
	PDFStateFailed = "failed"
)

// Extract states.
const (
	ExtractStateNone         = "none"
	ExtractStateExtracted    = "extracted"
	ExtractStateMetadataOnly = "metadata_only"
	ExtractStateFailed       = "failed"
)

// Paper is the stored record for one downloaded arXiv paper.
type Paper struct {
	PaperKey        string    `json:"paper_key"`
	ArxivID         string    `json:"arxiv_id"`
	Version         int       `json:"version"`
	Title           string    `json:"title"`
	Summary         string    `json:"summary"`
	Authors         []string  `json:"authors"`
	Categories      []string  `json:"categories"`
	PrimaryCategory string    `json:"primary_category"`
	Published       time.Time `json:"published"`
	Updated         time.Time `json:"updated"`
	AddedAt         int64     `json:"added_at"`
	PDFState        string    `json:"pdf_state"`
	PDFSizeBytes    int64     `json:"pdf_size_bytes"`
	PDFSHA256       string    `json:"pdf_sha256"`
	ExtractState    string    `json:"extract_state"`
	ExtractQuality  string    `json:"extract_quality"`
	TextChars       int       `json:"text_chars"`
	AbsURL          string    `json:"abs_url"`
	PDFURL          string    `json:"pdf_url"`
	LastError       string    `json:"last_error"`
}

// Store persists papers under cfgDir/arxiv/papers/<paperKey>/.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at cfgDir/arxiv.
func NewStore(cfgDir string) *Store {
	return &Store{dir: filepath.Join(cfgDir, "arxiv")}
}

// PaperPDFRelPath returns the app-config-relative path for a stored paper PDF.
func PaperPDFRelPath(key string) (string, error) {
	if !ValidPaperKey(key) {
		return "", fmt.Errorf("invalid paper key: %q", key)
	}
	return filepath.ToSlash(filepath.Join("arxiv", "papers", key, "paper.pdf")), nil
}

// Dir returns the store root directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) papersDir() string { return filepath.Join(s.dir, "papers") }

// paperDir validates key and returns the contained paper directory.
func (s *Store) paperDir(key string) (string, error) {
	if !ValidPaperKey(key) {
		return "", fmt.Errorf("invalid paper key: %q", key)
	}
	dir := filepath.Join(s.papersDir(), key)
	rel, err := filepath.Rel(s.papersDir(), dir)
	if err != nil || rel != key || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid paper key: %q", key)
	}
	return dir, nil
}

// PaperPDFPath returns the stored PDF path for key.
func (s *Store) PaperPDFPath(key string) (string, error) {
	dir, err := s.paperDir(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "paper.pdf"), nil
}

// PaperExtractDir returns the pdfextract output directory for key.
func (s *Store) PaperExtractDir(key string) (string, error) {
	dir, err := s.paperDir(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "extract"), nil
}

// PaperTextPath returns the extracted text path for key.
func (s *Store) PaperTextPath(key string) (string, error) {
	dir, err := s.PaperExtractDir(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "text.txt"), nil
}

// PaperPagesPath returns the extracted per-page JSONL path for key.
func (s *Store) PaperPagesPath(key string) (string, error) {
	dir, err := s.PaperExtractDir(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "pages.jsonl"), nil
}

// PaperManifestPath returns the pdfextract manifest path for key.
func (s *Store) PaperManifestPath(key string) (string, error) {
	dir, err := s.PaperExtractDir(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "manifest.json"), nil
}

// SavePaper writes the paper record atomically.
func (s *Store) SavePaper(paper *Paper) error {
	if paper == nil {
		return fmt.Errorf("paper is required")
	}
	dir, err := s.paperDir(paper.PaperKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(paper, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(dir, "paper.json"), b, 0o600)
}

// GetPaper loads the record for key. Missing papers return an error matching
// errors.Is(err, fs.ErrNotExist).
func (s *Store) GetPaper(key string) (*Paper, error) {
	dir, err := s.paperDir(key)
	if err != nil {
		return nil, err
	}
	b, err := atomicfile.ReadFile(filepath.Join(dir, "paper.json"))
	if err != nil {
		return nil, err
	}
	var paper Paper
	if err := json.Unmarshal(b, &paper); err != nil {
		return nil, fmt.Errorf("paper record %q is corrupt: %v", key, err)
	}
	return &paper, nil
}

// HasPaper reports whether a record exists for key.
func (s *Store) HasPaper(key string) bool {
	paper, err := s.GetPaper(key)
	return err == nil && paper != nil
}

// ListPapers returns all stored papers sorted by added_at descending.
func (s *Store) ListPapers() ([]*Paper, error) {
	entries, err := os.ReadDir(s.papersDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []*Paper{}, nil
		}
		return nil, err
	}
	out := []*Paper{}
	for _, entry := range entries {
		if !entry.IsDir() || !ValidPaperKey(entry.Name()) {
			continue
		}
		paper, err := s.GetPaper(entry.Name())
		if err != nil {
			continue
		}
		out = append(out, paper)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AddedAt != out[j].AddedAt {
			return out[i].AddedAt > out[j].AddedAt
		}
		return out[i].PaperKey < out[j].PaperKey
	})
	return out, nil
}

// DeletePaper removes the paper directory for key.
func (s *Store) DeletePaper(key string) error {
	dir, err := s.paperDir(key)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// StorageBytes walks the store directory and sums file sizes (best effort).
func (s *Store) StorageBytes() int64 {
	var total int64
	_ = filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
