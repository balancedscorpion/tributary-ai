package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	journalRecordKind          = "ai_analyst.usage_journal"
	journalRecordSchemaVersion = 1
	journalFileLimit           = 1 << 20
	maxJournalReplayBatch      = 256
)

type journalRecord struct {
	Kind          string     `json:"kind"`
	SchemaVersion int        `json:"schema_version"`
	Invocation    Invocation `json:"invocation"`
	RetryCount    int        `json:"retry_count"`
	NextRetryAt   *time.Time `json:"next_retry_at,omitempty"`
}

type journalState struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	Dropped       int64  `json:"dropped_invocation_count"`
}

// Journal is the local source of truth between provider dispatch and the
// configured primary usage repository. Its payload is deliberately limited to
// non-secret attribution and counters: no prompts, bodies, headers, URLs, or
// raw provider errors have a field in this schema.
type Journal struct {
	root        string
	now         func() time.Time
	mu          sync.Mutex
	dropped     atomic.Int64
	quarantined atomic.Int64
}

func NewJournal(cfgDir string) (*Journal, error) {
	if strings.TrimSpace(cfgDir) == "" {
		return nil, errors.New("cfgDir is required")
	}
	j := &Journal{root: filepath.Join(cfgDir, "chatdb", "usage-journal"), now: func() time.Time { return time.Now().UTC() }}
	for _, dir := range []string{j.root, j.pendingDir(), j.quarantineDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if state, err := j.readState(); err == nil {
		j.dropped.Store(state.Dropped)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return j, nil
}

func (j *Journal) pendingDir() string    { return filepath.Join(j.root, "pending") }
func (j *Journal) quarantineDir() string { return filepath.Join(j.root, "quarantine") }
func (j *Journal) path(id string) string { return filepath.Join(j.pendingDir(), id+".json") }
func (j *Journal) statePath() string     { return filepath.Join(j.root, "state.json") }

func (j *Journal) Begin(ctx context.Context, invocation Invocation) (Invocation, error) {
	if err := contextErr(ctx); err != nil {
		return Invocation{}, err
	}
	prepared, err := prepareStart(ctx, invocation, j.now())
	if err != nil {
		return Invocation{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if existing, err := j.readLocked(prepared.ID); err == nil {
		if startEquivalent(existing.Invocation, prepared) {
			return cloneInvocation(existing.Invocation), nil
		}
		return Invocation{}, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		j.dropped.Add(1)
		_ = j.persistStateLocked()
		return Invocation{}, err
	}
	record := journalRecord{Kind: journalRecordKind, SchemaVersion: journalRecordSchemaVersion, Invocation: prepared}
	if err := writeJournalJSON(j.path(prepared.ID), record); err != nil {
		j.dropped.Add(1)
		_ = j.persistStateLocked()
		return Invocation{}, err
	}
	return cloneInvocation(prepared), nil
}

func (j *Journal) Snapshot(ctx context.Context, id string, snapshot UsageSnapshot) (Invocation, error) {
	if err := contextErr(ctx); err != nil {
		return Invocation{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.readLocked(id)
	if err != nil {
		return Invocation{}, err
	}
	updated, err := applySnapshot(record.Invocation, snapshot, j.now())
	if err != nil {
		return Invocation{}, err
	}
	record.Invocation = updated
	record.RetryCount = 0
	record.NextRetryAt = nil
	if err := writeJournalJSON(j.path(id), record); err != nil {
		return Invocation{}, err
	}
	return cloneInvocation(updated), nil
}

func (j *Journal) Finalize(ctx context.Context, id string, final Finalization) (Invocation, error) {
	if err := contextErr(ctx); err != nil {
		return Invocation{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.readLocked(id)
	if err != nil {
		return Invocation{}, err
	}
	updated, err := applyFinalization(record.Invocation, final, j.now())
	if err != nil {
		return Invocation{}, err
	}
	record.Invocation = updated
	record.RetryCount = 0
	record.NextRetryAt = nil
	if err := writeJournalJSON(j.path(id), record); err != nil {
		return Invocation{}, err
	}
	return cloneInvocation(updated), nil
}

func (j *Journal) Remove(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.Remove(j.path(strings.TrimSpace(id))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDirectory(j.pendingDir())
}

func (j *Journal) markRetry(id string, now time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.readLocked(id)
	if err != nil {
		return err
	}
	record.RetryCount++
	delay := time.Second << min(record.RetryCount-1, 8)
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	next := now.UTC().Add(delay)
	record.NextRetryAt = &next
	return writeJournalJSON(j.path(id), record)
}

func (j *Journal) clearRetry(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, err := j.readLocked(id)
	if err != nil {
		return err
	}
	if record.RetryCount == 0 && record.NextRetryAt == nil {
		return nil
	}
	record.RetryCount = 0
	record.NextRetryAt = nil
	return writeJournalJSON(j.path(id), record)
}

func (j *Journal) pending(now time.Time, limit int) ([]journalRecord, error) {
	return j.pendingRecords(now, limit, true)
}

// pendingRecords can bypass retry deferral during startup recovery. A process
// restart is a terminal boundary for every locally running invocation even if
// its previous primary-storage retry had not yet become eligible.
func (j *Journal) pendingRecords(now time.Time, limit int, respectRetry bool) ([]journalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	entries, err := os.ReadDir(j.pendingDir())
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, k int) bool { return entries[i].Name() < entries[k].Name() })
	if limit == 0 || limit > maxJournalReplayBatch {
		limit = maxJournalReplayBatch
	} else if limit < 0 {
		limit = len(entries)
	}
	out := make([]journalRecord, 0, min(limit, len(entries)))
	for _, entry := range entries {
		if len(out) >= limit || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(j.pendingDir(), entry.Name())
		record, readErr := readJournalRecord(path)
		if readErr != nil {
			j.quarantineLocked(path)
			continue
		}
		if respectRetry && record.NextRetryAt != nil && now.UTC().Before(record.NextRetryAt.UTC()) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (j *Journal) health(now time.Time) ReportHealth {
	j.mu.Lock()
	defer j.mu.Unlock()
	quarantined := countJournalJSONFiles(j.quarantineDir())
	health := ReportHealth{
		Status: HealthHealthy, DroppedInvocationCount: j.dropped.Load() + quarantined,
		QuarantinedRecordCount: quarantined,
	}
	entries, err := os.ReadDir(j.pendingDir())
	if err != nil {
		health.Status = HealthUnavailable
		health.Message = optionalString("usage journal unavailable")
		return health
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, readErr := readJournalRecord(filepath.Join(j.pendingDir(), entry.Name()))
		if readErr != nil {
			health.Status = HealthDegraded
			health.DroppedInvocationCount++
			continue
		}
		health.PendingInvocationCount++
		startedAt := record.Invocation.StartedAt.UTC()
		if health.OldestPendingAt == nil || startedAt.Before(*health.OldestPendingAt) {
			copy := startedAt
			health.OldestPendingAt = &copy
		}
		if record.Invocation.Status.Terminal() {
			health.TerminalPendingCount++
		}
		if !record.Invocation.Status.Terminal() {
			health.RunningInvocationCount++
			if now.UTC().Sub(record.Invocation.UpdatedAt.UTC()) >= StaleInvocationAge {
				health.StaleInvocationCount++
			}
		}
	}
	if health.Status == HealthHealthy && (health.DroppedInvocationCount > 0 || health.PendingInvocationCount > 0 || health.StaleInvocationCount > 0) {
		health.Status = HealthDegraded
		health.Message = optionalString("usage accounting is durable but primary reporting is catching up")
	}
	return health
}

func (j *Journal) recoverInterrupted(now time.Time) (int, error) {
	records, err := j.pendingRecords(now, -1, false)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, record := range records {
		if record.Invocation.Status.Terminal() {
			// Startup reconciliation must consider durable terminal records before
			// legacy primary cleanup. A backoff deadline from the previous process
			// must not let a poorer running primary row win first.
			if err := j.clearRetry(record.Invocation.ID); err != nil {
				return recovered, err
			}
			continue
		}
		final := finalizationFromInvocation(record.Invocation, StatusInterrupted, now.UTC(), "PROCESS_RESTART")
		if _, err := j.Finalize(context.Background(), record.Invocation.ID, final); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

func (j *Journal) readLocked(id string) (journalRecord, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return journalRecord{}, err
	}
	return readJournalRecord(j.path(id))
}

func (j *Journal) quarantineLocked(path string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	destination := filepath.Join(j.quarantineDir(), fmt.Sprintf("%s-%d.json", base, j.now().UnixNano()))
	if err := os.Rename(path, destination); err == nil {
		_ = os.Chmod(destination, 0o600)
		_ = syncDirectory(j.quarantineDir())
		_ = syncDirectory(j.pendingDir())
	}
	j.quarantined.Add(1)
	_ = j.persistStateLocked()
}

func (j *Journal) readState() (journalState, error) {
	file, err := os.Open(j.statePath())
	if err != nil {
		return journalState{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, journalFileLimit))
	decoder.DisallowUnknownFields()
	var state journalState
	if err := decoder.Decode(&state); err != nil {
		return journalState{}, err
	}
	if state.Kind != journalRecordKind+".state" || state.SchemaVersion != journalRecordSchemaVersion || state.Dropped < 0 {
		return journalState{}, errors.New("unsupported usage journal state")
	}
	return state, nil
}

func (j *Journal) persistStateLocked() error {
	return writeJournalJSON(j.statePath(), journalState{
		Kind: journalRecordKind + ".state", SchemaVersion: journalRecordSchemaVersion, Dropped: j.dropped.Load(),
	})
}

func countJournalJSONFiles(dir string) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var count int64
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

func readJournalRecord(path string) (journalRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return journalRecord{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, journalFileLimit))
	decoder.DisallowUnknownFields()
	var record journalRecord
	if err := decoder.Decode(&record); err != nil {
		return journalRecord{}, err
	}
	if record.Kind != journalRecordKind || record.SchemaVersion != journalRecordSchemaVersion {
		return journalRecord{}, errors.New("unsupported usage journal record")
	}
	if err := validateID(record.Invocation.ID); err != nil {
		return journalRecord{}, err
	}
	if filepath.Base(path) != record.Invocation.ID+".json" {
		return journalRecord{}, errors.New("usage journal identity mismatch")
	}
	if record.RetryCount < 0 {
		return journalRecord{}, errors.New("invalid usage journal retry count")
	}
	if err := normalizeLoadedInvocation(&record.Invocation); err != nil {
		return journalRecord{}, fmt.Errorf("invalid usage journal invocation: %w", err)
	}
	if record.NextRetryAt != nil {
		next := canonicalUsageTime(*record.NextRetryAt)
		record.NextRetryAt = &next
	}
	return record, nil
}

func writeJournalJSON(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".usage-journal-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tmp)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncDirectory(dir)
}
