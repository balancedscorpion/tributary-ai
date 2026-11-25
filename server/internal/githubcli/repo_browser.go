package githubcli

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"ai-analyst-local/internal/sources"
)

const defaultRepositoryRawBytes int64 = 1 << 20

func (m *Manager) RepositoryTree(ctx context.Context, sourceID string, ref string, repoPath string) (RepositoryTreeResult, error) {
	src, worktree, err := m.githubSource(ctx, sourceID)
	if err != nil {
		return RepositoryTreeResult{}, err
	}
	clean, err := cleanRepoRelPath(repoPath, true)
	if err != nil {
		return RepositoryTreeResult{}, err
	}
	ref = strings.TrimSpace(ref)
	if ref != "" {
		return m.repositoryTreeAtRef(ctx, src, worktree, ref, clean)
	}
	full, err := resolveWorktreePath(worktree, clean)
	if err != nil {
		return RepositoryTreeResult{}, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return RepositoryTreeResult{}, err
	}
	if !info.IsDir() {
		return RepositoryTreeResult{}, errors.New("repository path is not a directory")
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return RepositoryTreeResult{}, err
	}
	out := make([]RepositoryTreeEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		child := path.Join(clean, name)
		if clean == "" {
			child = name
		}
		item := RepositoryTreeEntry{Name: name, Path: child}
		if entry.IsDir() {
			item.Type = "directory"
		} else {
			item.Type = "file"
			if st, err := entry.Info(); err == nil {
				item.ByteSize = st.Size()
			}
		}
		out = append(out, item)
	}
	sortRepositoryTreeEntries(out)
	return RepositoryTreeResult{
		SourceID:   src.ID,
		Repository: src.Repository,
		Path:       clean,
		Entries:    out,
	}, nil
}

func (m *Manager) RepositoryFile(ctx context.Context, sourceID string, ref string, repoPath string, maxBytes int64) (RepositoryFileResult, error) {
	src, worktree, err := m.githubSource(ctx, sourceID)
	if err != nil {
		return RepositoryFileResult{}, err
	}
	clean, err := cleanRepoRelPath(repoPath, false)
	if err != nil {
		return RepositoryFileResult{}, err
	}
	ref = strings.TrimSpace(ref)
	if maxBytes <= 0 || maxBytes > defaultRepositoryRawBytes {
		maxBytes = defaultRepositoryRawBytes
	}
	if ref != "" {
		return m.repositoryFileAtRef(ctx, src, worktree, ref, clean, maxBytes)
	}
	full, err := resolveWorktreePath(worktree, clean)
	if err != nil {
		return RepositoryFileResult{}, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return RepositoryFileResult{}, err
	}
	if st.IsDir() {
		return RepositoryFileResult{}, errors.New("repository path is a directory")
	}
	f, err := os.Open(full)
	if err != nil {
		return RepositoryFileResult{}, err
	}
	defer f.Close()
	content, truncated, err := readStringLimit(f, maxBytes)
	if err != nil {
		return RepositoryFileResult{}, err
	}
	return RepositoryFileResult{
		SourceID:        src.ID,
		Repository:      src.Repository,
		Path:            clean,
		Content:         content,
		ByteSize:        st.Size(),
		Truncated:       truncated,
		ContentTypeHint: mime.TypeByExtension(filepath.Ext(clean)),
	}, nil
}

func (m *Manager) githubSource(ctx context.Context, sourceID string) (sources.Source, string, error) {
	if err := contextErr(ctx); err != nil {
		return sources.Source{}, "", err
	}
	if m == nil || m.sources == nil {
		return sources.Source{}, "", errors.New("source store is not configured")
	}
	src, err := m.sources.Get(ctx, strings.TrimSpace(sourceID))
	if err != nil {
		return sources.Source{}, "", err
	}
	if src.Kind != sources.KindGitHubClone {
		return sources.Source{}, "", errors.New("source is not a GitHub repository clone")
	}
	worktree := filepath.Clean(strings.TrimSpace(src.Worktree))
	if worktree == "" || !filepath.IsAbs(worktree) || !hasGitMetadata(worktree) {
		return sources.Source{}, "", errors.New("GitHub repository clone is missing or not initialized")
	}
	return src, worktree, nil
}

func (m *Manager) repositoryTreeAtRef(ctx context.Context, src sources.Source, worktree string, ref string, repoPath string) (RepositoryTreeResult, error) {
	if err := validateGitRefForRead(ref); err != nil {
		return RepositoryTreeResult{}, err
	}
	spec := ref
	if repoPath != "" {
		spec += ":" + repoPath
	}
	res, err := m.runGitInDir(ctx, "", boundedReadRawGHTimeout(15), worktree, "-C", worktree, "ls-tree", "-z", spec)
	if err != nil {
		return RepositoryTreeResult{}, gitCommandError(res, err, "")
	}
	entries := parseLsTreeZ(res.Stdout, repoPath)
	sortRepositoryTreeEntries(entries)
	return RepositoryTreeResult{
		SourceID:   src.ID,
		Repository: src.Repository,
		Ref:        ref,
		Path:       repoPath,
		Entries:    entries,
	}, nil
}

func (m *Manager) repositoryFileAtRef(ctx context.Context, src sources.Source, worktree string, ref string, repoPath string, maxBytes int64) (RepositoryFileResult, error) {
	if err := validateGitRefForRead(ref); err != nil {
		return RepositoryFileResult{}, err
	}
	spec := ref + ":" + repoPath
	res, err := m.runGitInDir(ctx, "", boundedReadRawGHTimeout(15), worktree, "-C", worktree, "show", "--no-ext-diff", "--no-textconv", spec)
	if err != nil {
		return RepositoryFileResult{}, gitCommandError(res, err, "")
	}
	content := res.Stdout
	truncated := res.StdoutTruncated
	if int64(len(content)) > maxBytes {
		content = content[:maxBytes]
		truncated = true
	}
	return RepositoryFileResult{
		SourceID:        src.ID,
		Repository:      src.Repository,
		Ref:             ref,
		Path:            repoPath,
		Content:         content,
		ByteSize:        int64(len(res.Stdout)),
		Truncated:       truncated,
		ContentTypeHint: mime.TypeByExtension(filepath.Ext(repoPath)),
	}, nil
}

func cleanRepoRelPath(raw string, allowEmpty bool) (string, error) {
	value := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("path is required")
	}
	clean := path.Clean(value)
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", errors.New("path is required")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.ContainsRune(clean, '\x00') {
		return "", errors.New("invalid repository path")
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") || strings.Contains(clean, "/.git/") {
		return "", fs.ErrNotExist
	}
	return clean, nil
}

func resolveWorktreePath(worktree string, rel string) (string, error) {
	full := filepath.Clean(filepath.Join(worktree, filepath.FromSlash(rel)))
	base := filepath.Clean(worktree)
	if !isProjectSubpath(base, full) {
		return "", errors.New("repository path escapes worktree")
	}
	return full, nil
}

func validateGitRefForRead(ref string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	if strings.ContainsAny(ref, "\x00\r\n\t ") || strings.HasPrefix(ref, "-") {
		return errors.New("invalid git ref")
	}
	return nil
}

func parseLsTreeZ(stdout string, basePath string) []RepositoryTreeEntry {
	entries := []RepositoryTreeEntry{}
	for _, record := range strings.Split(stdout, "\x00") {
		record = strings.TrimSpace(record)
		if record == "" {
			continue
		}
		meta, name, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 2 {
			continue
		}
		entryType := "file"
		if fields[1] == "tree" {
			entryType = "directory"
		}
		entries = append(entries, RepositoryTreeEntry{
			Name: path.Base(name),
			Path: path.Join(basePath, name),
			Type: entryType,
		})
	}
	return entries
}

func sortRepositoryTreeEntries(entries []RepositoryTreeEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type == "directory"
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}

func readStringLimit(r io.Reader, maxBytes int64) (string, bool, error) {
	if maxBytes <= 0 {
		maxBytes = defaultRepositoryRawBytes
	}
	var b strings.Builder
	lr := io.LimitReader(r, maxBytes+1)
	n, err := io.Copy(&b, lr)
	if err != nil {
		return "", false, err
	}
	text := b.String()
	if n > maxBytes {
		return text[:int(maxBytes)], true, nil
	}
	return text, false, nil
}
