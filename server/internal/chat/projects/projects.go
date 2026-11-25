package projects

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ai-analyst-local/internal/atomicfile"
	chatstore "ai-analyst-local/internal/chat/store"
)

const (
	// DefaultMaxReadBytes is a safety cap for single-file reads used by read-style APIs.
	// Callers may override per request.
	DefaultMaxReadBytes int64 = 1 << 20 // 1 MiB

	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600

	sourceVisibilityFile = "source_visibility.json"
)

var projectIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var ErrFileTooLarge = errors.New("projects: file too large")

// Store manages disk-backed storage for uploaded project files under:
//
//	cfgDir/projects/<id>/files
type Store struct {
	baseDir string
}

type Source struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Hidden     bool   `json:"hidden"`
	FileCount  int    `json:"file_count"`
	TotalBytes int64  `json:"total_bytes"`
}

type sourceVisibilityEnvelope struct {
	Hidden []string `json:"hidden"`
}

func NewStore(cfgDir string) (*Store, error) {
	cfgDir = strings.TrimSpace(cfgDir)
	if cfgDir == "" {
		return nil, errors.New("projects: cfgDir is required")
	}
	base := filepath.Join(cfgDir, "projects")
	if err := os.MkdirAll(base, dirPerm); err != nil {
		return nil, err
	}
	return &Store{baseDir: base}, nil
}

func (s *Store) BaseDir() string { return s.baseDir }

func ValidateProjectID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("projects: empty project id")
	}
	if !projectIDRe.MatchString(id) {
		return fmt.Errorf("projects: invalid project id %q", id)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("projects: invalid project id %q", id)
	}
	return nil
}

func cleanRelPath(p string, allowEmpty bool) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("projects: empty path")
	}
	// Be liberal in what we accept from clients.
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimPrefix(raw, "/")

	clean := path.Clean(raw)
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("projects: invalid path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("projects: invalid path traversal")
	}
	if strings.ContainsRune(clean, '\x00') {
		return "", errors.New("projects: invalid path")
	}
	return clean, nil
}

func isSubpath(base string, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	rel = filepath.Clean(rel)
	if rel == ".." {
		return false
	}
	sep := string(filepath.Separator)
	return !strings.HasPrefix(rel, ".."+sep)
}

func isGitInternalRelPath(rel string) bool {
	clean := strings.Trim(strings.ReplaceAll(rel, "\\", "/"), "/")
	return clean == ".git" || strings.HasPrefix(clean, ".git/")
}

func isSensitiveLocalProjectRelPath(rel string) bool {
	return chatstore.LocalPathExcludedByPermissionPatterns(rel, chatstore.DefaultSensitiveLocalPermissionExcludes())
}

func (s *Store) projectDir(projectID string) (string, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return "", err
	}
	return filepath.Join(s.baseDir, projectID), nil
}

func (s *Store) FilesDir(projectID string) (string, error) {
	pdir, err := s.projectDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(pdir, "files"), nil
}

func (s *Store) EnsureFilesDir(projectID string) (string, error) {
	root, err := s.FilesDir(projectID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return "", err
	}
	return root, nil
}

func (s *Store) PutFile(projectID string, relPath string, data []byte) error {
	root, err := s.EnsureFilesDir(projectID)
	if err != nil {
		return err
	}
	clean, err := cleanRelPath(relPath, false)
	if err != nil {
		return err
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	full = filepath.Clean(full)
	if !isSubpath(root, full) {
		return errors.New("projects: path escapes project root")
	}

	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return err
	}
	return atomicfile.WriteFile(full, data, filePerm)
}

func (s *Store) PutFileStream(projectID string, relPath string, r io.Reader, maxBytes int64) (int64, []byte, error) {
	root, err := s.EnsureFilesDir(projectID)
	if err != nil {
		return 0, nil, err
	}
	clean, err := cleanRelPath(relPath, false)
	if err != nil {
		return 0, nil, err
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	full = filepath.Clean(full)
	if !isSubpath(root, full) {
		return 0, nil, errors.New("projects: path escapes project root")
	}

	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return 0, nil, err
	}

	tmp, err := os.CreateTemp(parent, filepath.Base(full)+".tmp-*")
	if err != nil {
		return 0, nil, err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(filePerm); err != nil {
		return 0, nil, err
	}

	hasher := sha256.New()
	writer := io.MultiWriter(tmp, hasher)

	var n int64
	if maxBytes > 0 {
		n, err = io.Copy(writer, io.LimitReader(r, maxBytes+1))
		if err != nil {
			return 0, nil, err
		}
		if n > maxBytes {
			return 0, nil, fmt.Errorf("%w (max %d bytes)", ErrFileTooLarge, maxBytes)
		}
	} else {
		n, err = io.Copy(writer, r)
		if err != nil {
			return 0, nil, err
		}
	}

	if err := tmp.Sync(); err != nil {
		return 0, nil, err
	}
	if err := tmp.Close(); err != nil {
		return 0, nil, err
	}
	if err := os.Rename(tmpPath, full); err != nil {
		return 0, nil, err
	}
	keep = true
	return n, hasher.Sum(nil), nil
}

func (s *Store) ListSources(ctx context.Context, projectID string) ([]Source, error) {
	root, err := s.FilesDir(projectID)
	if err != nil {
		return nil, err
	}
	hidden, err := s.loadSourceVisibility(projectID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return []Source{}, nil
		}
		return nil, err
	}

	out := make([]Source, 0, len(entries))
	for _, entry := range entries {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
		}
		if !entry.IsDir() || entry.Name() == ".git" {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		sourceDir := filepath.Join(root, name)
		count, total, err := scanSourceStats(ctx, sourceDir)
		if err != nil {
			return nil, err
		}
		kind := "directory"
		if hasSourceGitMetadata(sourceDir) {
			kind = "git"
		}
		out = append(out, Source{
			Name:       name,
			Path:       filepath.ToSlash(name),
			Kind:       kind,
			Hidden:     hidden[name],
			FileCount:  count,
			TotalBytes: total,
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}

func (s *Store) SetSourceHidden(ctx context.Context, projectID string, sourceName string, hidden bool) error {
	sourceName, sourceDir, err := s.cleanExistingSourceDir(projectID, sourceName)
	if err != nil {
		return err
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if st, err := os.Stat(sourceDir); err != nil {
		return err
	} else if st == nil || !st.IsDir() {
		return errors.New("projects: source is not a directory")
	}

	visibility, err := s.loadSourceVisibility(projectID)
	if err != nil {
		return err
	}
	if hidden {
		visibility[sourceName] = true
	} else {
		delete(visibility, sourceName)
	}
	return s.saveSourceVisibility(projectID, visibility)
}

func (s *Store) DeleteSource(ctx context.Context, projectID string, sourceName string) error {
	sourceName, sourceDir, err := s.cleanExistingSourceDir(projectID, sourceName)
	if err != nil {
		return err
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	if st, err := os.Stat(sourceDir); err != nil {
		return err
	} else if st == nil || !st.IsDir() {
		return errors.New("projects: source is not a directory")
	}
	if err := os.RemoveAll(sourceDir); err != nil {
		return err
	}
	visibility, err := s.loadSourceVisibility(projectID)
	if err != nil {
		return err
	}
	delete(visibility, sourceName)
	return s.saveSourceVisibility(projectID, visibility)
}

// OpenFile returns an open *os.File for streaming reads. The caller is
// responsible for closing the file. The same security checks as ReadFile
// apply (path normalization, traversal prevention, subpath validation).
func (s *Store) OpenFile(projectID string, relPath string) (*os.File, error) {
	root, err := s.FilesDir(projectID)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(root); err != nil || st == nil || !st.IsDir() {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("projects: files directory missing")
	}

	clean, err := cleanRelPath(relPath, false)
	if err != nil {
		return nil, err
	}
	if isGitInternalRelPath(clean) {
		return nil, fs.ErrNotExist
	}
	if hidden, err := s.pathHidden(projectID, clean); err != nil {
		return nil, err
	} else if hidden {
		return nil, fs.ErrNotExist
	}
	full, err := s.resolveReadableFilePath(projectID, root, clean)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}

	st, err := f.Stat()
	if err == nil && st != nil && st.IsDir() {
		f.Close()
		return nil, errors.New("projects: path is a directory")
	}
	return f, nil
}

func (s *Store) ReadFile(projectID string, relPath string, maxBytes int64) (data []byte, truncated bool, _ error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxReadBytes
	}
	root, err := s.FilesDir(projectID)
	if err != nil {
		return nil, false, err
	}
	if st, err := os.Stat(root); err != nil || st == nil || !st.IsDir() {
		if err != nil {
			return nil, false, err
		}
		return nil, false, errors.New("projects: files directory missing")
	}

	clean, err := cleanRelPath(relPath, false)
	if err != nil {
		return nil, false, err
	}
	if isGitInternalRelPath(clean) {
		return nil, false, fs.ErrNotExist
	}
	if hidden, err := s.pathHidden(projectID, clean); err != nil {
		return nil, false, err
	} else if hidden {
		return nil, false, fs.ErrNotExist
	}
	full, err := s.resolveReadableFilePath(projectID, root, clean)
	if err != nil {
		return nil, false, err
	}

	f, err := os.Open(full)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	st, err := f.Stat()
	if err == nil && st != nil && st.IsDir() {
		return nil, false, errors.New("projects: path is a directory")
	}

	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(b)) > maxBytes {
		return b[:maxBytes], true, nil
	}
	return b, false, nil
}

func (s *Store) resolveReadableFilePath(projectID string, root string, clean string) (string, error) {
	root = filepath.Clean(root)
	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(clean)))
	if !isSubpath(root, full) {
		return "", errors.New("projects: path escapes project root")
	}

	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedFull, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", err
	}
	resolvedRoot = filepath.Clean(resolvedRoot)
	resolvedFull = filepath.Clean(resolvedFull)
	if !isSubpath(resolvedRoot, resolvedFull) {
		return "", errors.New("projects: path escapes project root")
	}
	resolvedRel, err := filepath.Rel(resolvedRoot, resolvedFull)
	if err != nil {
		return "", err
	}
	resolvedRel = filepath.ToSlash(filepath.Clean(resolvedRel))
	if resolvedRel == "." || isGitInternalRelPath(resolvedRel) {
		return "", fs.ErrNotExist
	}
	if hidden, err := s.pathHidden(projectID, resolvedRel); err != nil {
		return "", err
	} else if hidden {
		return "", fs.ErrNotExist
	}
	return resolvedFull, nil
}

type ListOptions struct {
	// Prefix is a relative prefix (slash-separated). Empty means "all files".
	Prefix string
	// Limit is the max number of file paths to return. 0 means "no limit".
	Limit int
}

type ListResult struct {
	Files     []string
	Total     int
	Truncated bool
}

type FileStat struct {
	Path     string
	ByteSize int64
}

func (s *Store) ListFiles(ctx context.Context, projectID string, opts ListOptions) (ListResult, error) {
	root, err := s.FilesDir(projectID)
	if err != nil {
		return ListResult{}, err
	}
	root = filepath.Clean(root)
	hidden, err := s.loadSourceVisibility(projectID)
	if err != nil {
		return ListResult{}, err
	}

	prefix, err := cleanRelPath(opts.Prefix, true)
	if err != nil {
		return ListResult{}, err
	}
	// For prefix matching, treat it as a directory prefix.
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	limit := opts.Limit
	if limit < 0 {
		limit = 0
	}

	out := ListResult{Files: []string{}}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			if p != root {
				rel, err := filepath.Rel(root, p)
				if err == nil && isHiddenSourceRel(filepath.ToSlash(rel), hidden) {
					return fs.SkipDir
				}
			}
			return nil
		}
		// Skip symlinks.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		rel = strings.TrimPrefix(rel, "./")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
			return nil
		}
		if prefix != "" && !strings.HasPrefix(rel, prefix) {
			return nil
		}
		out.Total++
		if limit == 0 || len(out.Files) < limit {
			out.Files = append(out.Files, rel)
		} else {
			out.Truncated = true
		}
		return nil
	})
	if walkErr != nil {
		// If the project doesn't exist yet, surface a consistent not-found style error.
		if errors.Is(walkErr, fs.ErrNotExist) || os.IsNotExist(walkErr) {
			return ListResult{}, fs.ErrNotExist
		}
		return ListResult{}, walkErr
	}
	if out.Total > len(out.Files) {
		out.Truncated = true
	}
	return out, nil
}

type SearchOptions struct {
	CaseSensitive bool
	// MaxMatches is a global cap across all files. 0 means "no limit".
	MaxMatches int
	// MaxFileBytes skips files larger than this size (0 means "no limit").
	MaxFileBytes int64
}

type SearchResult struct {
	Matches   map[string][]string
	Truncated bool
}

func (s *Store) SearchFiles(ctx context.Context, projectID string, query string, opts SearchOptions) (SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return SearchResult{}, errors.New("projects: search query is required")
	}
	root, err := s.FilesDir(projectID)
	if err != nil {
		return SearchResult{}, err
	}
	root = filepath.Clean(root)
	hidden, err := s.loadSourceVisibility(projectID)
	if err != nil {
		return SearchResult{}, err
	}

	max := opts.MaxMatches
	if max <= 0 {
		max = int(^uint(0) >> 1)
	}

	normalize := func(in string) string {
		if opts.CaseSensitive {
			return in
		}
		return strings.ToLower(in)
	}
	nq := normalize(query)

	res := SearchResult{Matches: map[string][]string{}}
	matchCount := 0

	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		if matchCount >= max {
			res.Truncated = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			if p != root {
				rel, err := filepath.Rel(root, p)
				if err == nil && isHiddenSourceRel(filepath.ToSlash(rel), hidden) {
					return fs.SkipDir
				}
				if err == nil && isSensitiveLocalProjectRelPath(filepath.ToSlash(rel)) {
					return fs.SkipDir
				}
			}
			return nil
		}
		// Skip symlinks.
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		rel = strings.TrimPrefix(rel, "./")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || isSensitiveLocalProjectRelPath(rel) {
			return nil
		}
		if opts.MaxFileBytes > 0 {
			if info, err := d.Info(); err == nil && info != nil && info.Size() > opts.MaxFileBytes {
				return nil
			}
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if len(b) == 0 {
			return nil
		}
		// Fast skip likely-binary files.
		if bytesHasNUL(b, 8192) {
			return nil
		}

		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if matchCount >= max {
				res.Truncated = true
				return fs.SkipAll
			}
			if strings.Contains(normalize(line), nq) {
				res.Matches[rel] = append(res.Matches[rel], fmt.Sprintf("Line %d: %s", i+1, strings.TrimSpace(line)))
				matchCount++
			}
		}
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) || os.IsNotExist(walkErr) {
			return SearchResult{}, fs.ErrNotExist
		}
		return SearchResult{}, walkErr
	}
	return res, nil
}

func (s *Store) ScanFiles(ctx context.Context, projectID string) ([]FileStat, error) {
	root, err := s.FilesDir(projectID)
	if err != nil {
		return nil, err
	}
	root = filepath.Clean(root)
	hidden, err := s.loadSourceVisibility(projectID)
	if err != nil {
		return nil, err
	}

	out := []FileStat{}
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			if p != root {
				rel, err := filepath.Rel(root, p)
				if err == nil && isHiddenSourceRel(filepath.ToSlash(rel), hidden) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || info == nil {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		rel = strings.TrimPrefix(rel, "./")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") || isGitInternalRelPath(rel) {
			return nil
		}
		out = append(out, FileStat{Path: rel, ByteSize: info.Size()})
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) || os.IsNotExist(walkErr) {
			return nil, fs.ErrNotExist
		}
		return nil, walkErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (s *Store) sourceVisibilityPath(projectID string) (string, error) {
	pdir, err := s.projectDir(projectID)
	if err != nil {
		return "", err
	}
	return filepath.Join(pdir, sourceVisibilityFile), nil
}

func (s *Store) loadSourceVisibility(projectID string) (map[string]bool, error) {
	p, err := s.sourceVisibilityPath(projectID)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, err
	}
	var env sourceVisibilityEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, name := range env.Hidden {
		clean, err := cleanSourceName(name)
		if err == nil && clean != "" {
			out[clean] = true
		}
	}
	return out, nil
}

func (s *Store) saveSourceVisibility(projectID string, hidden map[string]bool) error {
	p, err := s.sourceVisibilityPath(projectID)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(hidden))
	for name, isHidden := range hidden {
		if !isHidden {
			continue
		}
		clean, err := cleanSourceName(name)
		if err == nil && clean != "" {
			names = append(names, clean)
		}
	}
	sort.Strings(names)
	b, err := json.MarshalIndent(sourceVisibilityEnvelope{Hidden: names}, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(p), dirPerm); err != nil {
		return err
	}
	return atomicfile.WriteFile(p, b, filePerm)
}

func (s *Store) cleanExistingSourceDir(projectID string, sourceName string) (string, string, error) {
	root, err := s.FilesDir(projectID)
	if err != nil {
		return "", "", err
	}
	sourceName, err = cleanSourceName(sourceName)
	if err != nil {
		return "", "", err
	}
	sourceDir := filepath.Clean(filepath.Join(root, filepath.FromSlash(sourceName)))
	if !isSubpath(root, sourceDir) || sourceDir == filepath.Clean(root) {
		return "", "", errors.New("projects: invalid source")
	}
	return sourceName, sourceDir, nil
}

func cleanSourceName(sourceName string) (string, error) {
	clean, err := cleanRelPath(sourceName, false)
	if err != nil {
		return "", err
	}
	if clean == "." || clean == ".git" || strings.HasPrefix(clean, ".git/") || strings.Contains(clean, "/") {
		return "", errors.New("projects: source must be a top-level directory")
	}
	return clean, nil
}

func (s *Store) pathHidden(projectID string, relPath string) (bool, error) {
	hidden, err := s.loadSourceVisibility(projectID)
	if err != nil {
		return false, err
	}
	return isHiddenSourceRel(relPath, hidden), nil
}

func isHiddenSourceRel(relPath string, hidden map[string]bool) bool {
	source, ok := sourceNameFromRel(relPath)
	return ok && hidden[source]
}

func sourceNameFromRel(relPath string) (string, bool) {
	clean, err := cleanRelPath(relPath, false)
	if err != nil {
		return "", false
	}
	first, _, _ := strings.Cut(clean, "/")
	if first == "" || first == "." || first == ".git" {
		return "", false
	}
	return first, true
}

func scanSourceStats(ctx context.Context, sourceDir string) (int, int64, error) {
	count := 0
	total := int64(0)
	walkErr := filepath.WalkDir(sourceDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx != nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() == ".git" {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil || info == nil {
			return nil
		}
		count++
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		return 0, 0, walkErr
	}
	return count, total, nil
}

func hasSourceGitMetadata(sourceDir string) bool {
	st, err := os.Stat(filepath.Join(sourceDir, ".git"))
	return err == nil && st != nil
}

func bytesHasNUL(b []byte, max int) bool {
	n := len(b)
	if n > max && max > 0 {
		n = max
	}
	for i := 0; i < n; i++ {
		if b[i] == 0 {
			return true
		}
	}
	return false
}
