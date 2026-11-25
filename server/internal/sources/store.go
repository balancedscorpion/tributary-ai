package sources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
)

const (
	KindLocalReference  = "local_reference"
	KindProjectSnapshot = "project_snapshot"
	KindGitHubClone     = "github_clone"
	KindConnectorFile   = "connector_file"
	KindThreadUpload    = "thread_upload"

	dirPerm  os.FileMode = 0o700
	filePerm os.FileMode = 0o600
)

type Source struct {
	ID           string         `json:"id"`
	Kind         string         `json:"kind"`
	Label        string         `json:"label,omitempty"`
	Title        string         `json:"title,omitempty"`
	Description  string         `json:"description,omitempty"`
	ThreadID     string         `json:"thread_id,omitempty"`
	ProjectID    string         `json:"project_id,omitempty"`
	ArtifactID   string         `json:"artifact_id,omitempty"`
	LocalPath    string         `json:"local_path,omitempty"`
	RemoteID     string         `json:"remote_id,omitempty"`
	RemoteURL    string         `json:"remote_url,omitempty"`
	RemotePath   string         `json:"remote_path,omitempty"`
	Repository   string         `json:"repository,omitempty"`
	Owner        string         `json:"owner,omitempty"`
	Repo         string         `json:"repo,omitempty"`
	CloneURL     string         `json:"clone_url,omitempty"`
	Worktree     string         `json:"worktree,omitempty"`
	Branch       string         `json:"branch,omitempty"`
	Head         string         `json:"head,omitempty"`
	Hidden       bool           `json:"hidden,omitempty"`
	Capabilities []string       `json:"capabilities,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	LastUsedAt   *time.Time     `json:"last_used_at,omitempty"`
}

type ListOptions struct {
	Kind          string
	ThreadID      string
	ProjectID     string
	IncludeHidden bool
}

type envelope struct {
	Version int      `json:"version"`
	Sources []Source `json:"sources"`
}

type Store struct {
	cfgDir string
	path   string
	mu     sync.Mutex
}

func NewStore(cfgDir string) (*Store, error) {
	cfgDir = strings.TrimSpace(cfgDir)
	if cfgDir == "" {
		return nil, errors.New("sources: cfgDir is required")
	}
	base := filepath.Join(cfgDir, "sources")
	if err := os.MkdirAll(base, dirPerm); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(base, "github"), dirPerm); err != nil {
		return nil, err
	}
	s := &Store{cfgDir: cfgDir, path: filepath.Join(base, "sources.json")}
	if _, err := os.Stat(s.path); errors.Is(err, os.ErrNotExist) {
		if err := s.writeLocked(envelope{Version: 1, Sources: []Source{}}); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Upsert(ctx context.Context, src Source) (Source, error) {
	if err := contextErr(ctx); err != nil {
		return Source{}, err
	}
	prepared, err := prepareSource(src)
	if err != nil {
		return Source{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	env, err := s.readLocked()
	if err != nil {
		return Source{}, err
	}
	now := time.Now().UTC()
	found := false
	for i := range env.Sources {
		if env.Sources[i].ID != prepared.ID {
			continue
		}
		if prepared.CreatedAt.IsZero() {
			prepared.CreatedAt = env.Sources[i].CreatedAt
		}
		if prepared.CreatedAt.IsZero() {
			prepared.CreatedAt = now
		}
		prepared.UpdatedAt = now
		env.Sources[i] = prepared
		found = true
		break
	}
	if !found {
		if prepared.CreatedAt.IsZero() {
			prepared.CreatedAt = now
		}
		if prepared.UpdatedAt.IsZero() {
			prepared.UpdatedAt = now
		}
		env.Sources = append(env.Sources, prepared)
	}
	sortSources(env.Sources)
	if err := s.writeLocked(env); err != nil {
		return Source{}, err
	}
	return prepared, nil
}

func (s *Store) Get(ctx context.Context, id string) (Source, error) {
	if err := contextErr(ctx); err != nil {
		return Source{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Source{}, errors.New("sources: id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	env, err := s.readLocked()
	if err != nil {
		return Source{}, err
	}
	for _, src := range env.Sources {
		if src.ID == id {
			return cloneSource(src), nil
		}
	}
	return Source{}, os.ErrNotExist
}

func (s *Store) List(ctx context.Context, opts ListOptions) ([]Source, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	env, err := s.readLocked()
	if err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(opts.Kind)
	threadID := strings.TrimSpace(opts.ThreadID)
	projectID := strings.TrimSpace(opts.ProjectID)
	out := make([]Source, 0, len(env.Sources))
	for _, src := range env.Sources {
		if kind != "" && src.Kind != kind {
			continue
		}
		if !opts.IncludeHidden && src.Hidden {
			continue
		}
		if threadID != "" && src.ThreadID != threadID {
			continue
		}
		if projectID != "" && src.ProjectID != projectID {
			continue
		}
		out = append(out, cloneSource(src))
	}
	sortSources(out)
	return out, nil
}

func (s *Store) Delete(ctx context.Context, id string) (Source, error) {
	if err := contextErr(ctx); err != nil {
		return Source{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Source{}, errors.New("sources: id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	env, err := s.readLocked()
	if err != nil {
		return Source{}, err
	}
	next := env.Sources[:0]
	var deleted Source
	found := false
	for _, src := range env.Sources {
		if src.ID == id {
			deleted = cloneSource(src)
			found = true
			continue
		}
		next = append(next, src)
	}
	if !found {
		return Source{}, os.ErrNotExist
	}
	env.Sources = next
	if err := s.writeLocked(env); err != nil {
		return Source{}, err
	}
	return deleted, nil
}

func (s *Store) GitHubWorktreePath(sourceID string) (string, error) {
	root, err := s.GitHubSourceDir(sourceID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return "", err
	}
	return filepath.Join(root, "worktree"), nil
}

func (s *Store) GitHubSourceDir(sourceID string) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || strings.ContainsAny(sourceID, `/\`) || sourceID == "." || sourceID == ".." {
		return "", errors.New("sources: invalid github source id")
	}
	root := filepath.Join(s.cfgDir, "sources", "github", sourceID)
	return root, nil
}

func GitHubSourceID(repository string) string {
	repo := strings.ToLower(strings.TrimSpace(repository))
	sum := sha256.Sum256([]byte(repo))
	return "github-" + hex.EncodeToString(sum[:])[:24]
}

func GitHubCloneURL(repository string) string {
	repository = strings.Trim(strings.TrimSpace(repository), "/")
	if repository == "" {
		return ""
	}
	return "https://github.com/" + repository + ".git"
}

func (s *Store) readLocked() (envelope, error) {
	var env envelope
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return envelope{Version: 1, Sources: []Source{}}, nil
	}
	if err != nil {
		return envelope{}, err
	}
	if len(data) == 0 {
		return envelope{Version: 1, Sources: []Source{}}, nil
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return envelope{}, err
	}
	if env.Version <= 0 {
		env.Version = 1
	}
	if env.Sources == nil {
		env.Sources = []Source{}
	}
	return env, nil
}

func (s *Store) writeLocked(env envelope) error {
	if env.Version <= 0 {
		env.Version = 1
	}
	if env.Sources == nil {
		env.Sources = []Source{}
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(s.path, append(data, '\n'), filePerm)
}

func prepareSource(src Source) (Source, error) {
	src.ID = strings.TrimSpace(src.ID)
	src.Kind = strings.TrimSpace(src.Kind)
	if src.ID == "" {
		return Source{}, errors.New("sources: id is required")
	}
	if strings.ContainsAny(src.ID, `/\`) || src.ID == "." || src.ID == ".." {
		return Source{}, fmt.Errorf("sources: invalid id %q", src.ID)
	}
	switch src.Kind {
	case KindLocalReference, KindProjectSnapshot, KindGitHubClone, KindConnectorFile, KindThreadUpload:
	default:
		return Source{}, fmt.Errorf("sources: invalid kind %q", src.Kind)
	}
	src.Label = strings.TrimSpace(src.Label)
	src.Title = strings.TrimSpace(src.Title)
	src.Description = strings.TrimSpace(src.Description)
	src.ThreadID = strings.TrimSpace(src.ThreadID)
	src.ProjectID = strings.TrimSpace(src.ProjectID)
	src.ArtifactID = strings.TrimSpace(src.ArtifactID)
	src.LocalPath = strings.TrimSpace(src.LocalPath)
	src.RemoteID = strings.TrimSpace(src.RemoteID)
	src.RemoteURL = strings.TrimSpace(src.RemoteURL)
	src.RemotePath = strings.TrimSpace(src.RemotePath)
	src.Repository = strings.Trim(strings.TrimSpace(src.Repository), "/")
	src.Owner = strings.TrimSpace(src.Owner)
	src.Repo = strings.TrimSpace(src.Repo)
	src.CloneURL = strings.TrimSpace(src.CloneURL)
	src.Worktree = strings.TrimSpace(src.Worktree)
	src.Branch = strings.TrimSpace(src.Branch)
	src.Head = strings.TrimSpace(src.Head)
	src.Capabilities = cleanStringList(src.Capabilities)
	if src.Metadata == nil {
		src.Metadata = map[string]any{}
	}
	return src, nil
}

func cloneSource(src Source) Source {
	out := src
	out.Capabilities = append([]string(nil), src.Capabilities...)
	if src.Metadata != nil {
		out.Metadata = map[string]any{}
		for k, v := range src.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}

func cleanStringList(values []string) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortSources(values []Source) {
	sort.Slice(values, func(i, j int) bool {
		if !values[i].UpdatedAt.Equal(values[j].UpdatedAt) {
			return values[i].UpdatedAt.After(values[j].UpdatedAt)
		}
		if values[i].Kind != values[j].Kind {
			return values[i].Kind < values[j].Kind
		}
		return values[i].ID < values[j].ID
	})
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
