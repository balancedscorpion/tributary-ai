package projects

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type Storage struct {
	BaseDir string
}

func NewStorage(cfgDir string) *Storage {
	base := filepath.Join(strings.TrimSpace(cfgDir), "projects")
	return &Storage{BaseDir: base}
}

func (s *Storage) ProjectDir(projectID string) string {
	return filepath.Join(s.BaseDir, projectID)
}

func (s *Storage) FilesDir(projectID string) string {
	return filepath.Join(s.ProjectDir(projectID), "files")
}

func CleanRelPath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", errors.New("path is required")
	}
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimPrefix(raw, "/")
	clean := path.Clean(raw)
	clean = strings.TrimPrefix(clean, "/")
	if clean == "." || clean == "" {
		return "", errors.New("invalid path")
	}
	if strings.Contains(clean, "..") {
		// path.Clean collapses, but be extra safe.
		parts := strings.Split(clean, "/")
		for _, part := range parts {
			if part == ".." {
				return "", errors.New("path traversal is not allowed")
			}
		}
	}
	return clean, nil
}

func (s *Storage) WriteFile(projectID string, relPath string, r io.Reader, maxBytes int64) (byteSize int64, sha []byte, err error) {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return 0, nil, errors.New("projectId is required")
	}
	clean, err := CleanRelPath(relPath)
	if err != nil {
		return 0, nil, err
	}
	root := s.FilesDir(pid)
	abs := filepath.Join(root, filepath.FromSlash(clean))
	if !strings.HasPrefix(abs, root) {
		return 0, nil, errors.New("invalid path")
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return 0, nil, err
	}

	f, err := os.Create(abs)
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	h := sha256.New()
	w := io.MultiWriter(f, h)

	var n int64
	if maxBytes > 0 {
		n, err = io.Copy(w, io.LimitReader(r, maxBytes+1))
		if err == nil && n > maxBytes {
			return 0, nil, fmt.Errorf("file too large (>%d bytes)", maxBytes)
		}
	} else {
		n, err = io.Copy(w, r)
	}
	if err != nil {
		return 0, nil, err
	}

	sum := h.Sum(nil)
	return n, sum, nil
}

func (s *Storage) ReadFile(projectID string, relPath string, maxBytes int64) ([]byte, string, error) {
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return nil, "", errors.New("projectId is required")
	}
	clean, err := CleanRelPath(relPath)
	if err != nil {
		return nil, "", err
	}
	root := s.FilesDir(pid)
	abs := filepath.Join(root, filepath.FromSlash(clean))
	if !strings.HasPrefix(abs, root) {
		return nil, "", errors.New("invalid path")
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	var b []byte
	if maxBytes > 0 {
		b, err = io.ReadAll(io.LimitReader(f, maxBytes+1))
		if err == nil && int64(len(b)) > maxBytes {
			b = b[:maxBytes]
			return b, clean, fmt.Errorf("file truncated to %d bytes", maxBytes)
		}
		return b, clean, err
	}
	b, err = io.ReadAll(f)
	return b, clean, err
}
