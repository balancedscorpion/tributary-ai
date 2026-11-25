package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"ai-analyst-local/internal/chat/jsondb"
)

// jsonLongHorizonMutex combines the in-process mutex with an advisory OS
// lock. Every JSON long-horizon transaction already uses the Locker shape;
// adding the file lock makes the same CAS/claim/reservation critical sections
// mutually exclusive across processes sharing a chatdb directory.
type jsonLongHorizonMutex struct {
	local sync.Mutex
	file  *os.File
}

func newJSONLongHorizonMutex(cfgDir string) (*jsonLongHorizonMutex, error) {
	path := filepath.Join(jsondb.RootDir(cfgDir), ".long_horizon.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open JSON long-horizon lock: %w", err)
	}
	return &jsonLongHorizonMutex{file: file}, nil
}

func (m *jsonLongHorizonMutex) Lock() {
	if m == nil || m.file == nil {
		panic("JSON long-horizon lock is not initialized")
	}
	m.local.Lock()
	if err := lockLongHorizonFile(m.file); err != nil {
		m.local.Unlock()
		panic(fmt.Sprintf("lock JSON long-horizon store: %v", err))
	}
}

func (m *jsonLongHorizonMutex) Unlock() {
	if m == nil || m.file == nil {
		panic("JSON long-horizon lock is not initialized")
	}
	err := unlockLongHorizonFile(m.file)
	m.local.Unlock()
	if err != nil {
		panic(fmt.Sprintf("unlock JSON long-horizon store: %v", err))
	}
}
