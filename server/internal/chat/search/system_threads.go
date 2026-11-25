package search

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"ai-analyst-local/internal/atomicfile"
	"ai-analyst-local/internal/chat/jsondb"
	"ai-analyst-local/internal/chat/store"
)

type SystemThreadRegistry struct {
	path string
	mu   sync.Mutex
}

type systemThreadsFile struct {
	SearchThreadID string `json:"search_thread_id,omitempty"`
}

func NewSystemThreadRegistry(cfgDir string) *SystemThreadRegistry {
	return &SystemThreadRegistry{
		path: filepath.Join(jsondb.RootDir(cfgDir), "system_threads.json"),
	}
}

func (r *SystemThreadRegistry) SearchThreadID() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, err := r.loadLocked()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(current.SearchThreadID)
}

func (r *SystemThreadRegistry) EnsureSearchThread(ctx context.Context, st store.Store) (store.Thread, error) {
	if r == nil {
		return store.Thread{}, errors.New("registry is nil")
	}
	if st == nil {
		return store.Thread{}, errors.New("store is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	current, err := r.loadLocked()
	if err != nil {
		return store.Thread{}, err
	}
	if id := strings.TrimSpace(current.SearchThreadID); id != "" {
		if thread, err := st.GetThread(ctx, id); err == nil {
			return thread, nil
		}
	}

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		return store.Thread{}, err
	}
	title := "[Search]"
	_ = st.UpdateThreadTitleSummary(ctx, thread.ID, &title, nil)
	thread, _ = st.GetThread(ctx, thread.ID)
	current.SearchThreadID = thread.ID
	if err := r.saveLocked(current); err != nil {
		return store.Thread{}, err
	}
	return thread, nil
}

func (r *SystemThreadRegistry) loadLocked() (systemThreadsFile, error) {
	if _, err := os.Stat(r.path); errors.Is(err, os.ErrNotExist) {
		return systemThreadsFile{}, nil
	}
	b, err := os.ReadFile(r.path)
	if err != nil {
		return systemThreadsFile{}, err
	}
	var out systemThreadsFile
	if err := json.Unmarshal(b, &out); err != nil {
		return systemThreadsFile{}, err
	}
	return out, nil
}

func (r *SystemThreadRegistry) saveLocked(v systemThreadsFile) error {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicfile.WriteFile(r.path, b, 0o600)
}

