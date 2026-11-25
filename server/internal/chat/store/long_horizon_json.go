package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/jsondb"
)

var _ LongHorizonRepository = (*JSONStore)(nil)

const (
	queryJobRecordKind                 = "ai_analyst.query_job"
	queryJobEventRecordKind            = "ai_analyst.query_job_event"
	queryResultChunkRecordKind         = "ai_analyst.query_result_chunk"
	runQueryDependencyRecordKind       = "ai_analyst.run_query_dependency"
	continuationCheckpointRecordKind   = "ai_analyst.run_continuation_checkpoint"
	queryCapacityRecordKind            = "ai_analyst.query_capacity_reservation"
	queryTransactionRecordKind         = "ai_analyst.query_transaction"
	queryMutationTransactionKind       = "ai_analyst.query_mutation_transaction"
	runSuspendTransactionRecordKind    = "ai_analyst.run_suspend_transaction"
	runDependencyReplacementRecordKind = "ai_analyst.run_dependency_replacement_transaction"
	longHorizonSchemaVersion           = 1
)

type jsonMonitorLease struct {
	Holder    string    `json:"holder"`
	ExpiresAt time.Time `json:"expires_at"`
}

type jsonQueryJobRecord struct {
	Kind             string            `json:"kind"`
	SchemaVersion    int               `json:"schema_version"`
	ConnectionKey    string            `json:"connection_key"`
	Job              jobs.JobRecord    `json:"job"`
	MonitorLease     *jsonMonitorLease `json:"monitor_lease,omitempty"`
	ValidationState  string            `json:"validation_state,omitempty"`
	ValidationDetail string            `json:"validation_detail,omitempty"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type jsonQueryJobEventRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	QueryJobEvent
}

type jsonQueryResultChunkRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	QueryResultChunk
}

type jsonRunQueryDependencyRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	RunQueryDependency
}

type jsonContinuationCheckpointRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	ContinuationCheckpoint
}

type jsonQueryCapacityRecord struct {
	Kind          string     `json:"kind"`
	SchemaVersion int        `json:"schema_version"`
	JobID         string     `json:"job_id"`
	ThreadID      string     `json:"thread_id"`
	ConnectionKey string     `json:"connection_key"`
	State         string     `json:"state"`
	ReservedAt    time.Time  `json:"reserved_at"`
	ReleasedAt    *time.Time `json:"released_at,omitempty"`
}

type jsonQueryTransactionRecord struct {
	Kind          string           `json:"kind"`
	SchemaVersion int              `json:"schema_version"`
	ID            string           `json:"id"`
	Commit        jobs.DrainCommit `json:"commit"`
	Chunk         QueryResultChunk `json:"chunk"`
	Event         QueryJobEvent    `json:"event"`
	PreparedAt    time.Time        `json:"prepared_at"`
}

type jsonQueryMutationTransactionRecord struct {
	Kind                 string                    `json:"kind"`
	SchemaVersion        int                       `json:"schema_version"`
	ID                   string                    `json:"id"`
	Operation            string                    `json:"operation"`
	State                string                    `json:"state"`
	BeforeJob            jsonQueryJobRecord        `json:"before_job"`
	AfterJob             jsonQueryJobRecord        `json:"after_job"`
	BeforeEvents         []jsonQueryJobEventRecord `json:"before_events,omitempty"`
	AfterEvents          []jsonQueryJobEventRecord `json:"after_events,omitempty"`
	DeleteEventSequences []int64                   `json:"delete_event_sequences,omitempty"`
	BeforeCapacity       *jsonQueryCapacityRecord  `json:"before_capacity,omitempty"`
	AfterCapacity        *jsonQueryCapacityRecord  `json:"after_capacity,omitempty"`
	PreparedAt           time.Time                 `json:"prepared_at"`
}

type jsonRunSuspendTransactionRecord struct {
	Kind          string                           `json:"kind"`
	SchemaVersion int                              `json:"schema_version"`
	ID            string                           `json:"id"`
	BeforeRun     jsondb.Run                       `json:"before_run"`
	AfterRun      jsondb.Run                       `json:"after_run"`
	Checkpoint    jsonContinuationCheckpointRecord `json:"checkpoint"`
	PreparedAt    time.Time                        `json:"prepared_at"`
}

type jsonRunDependencyReplacementTransactionRecord struct {
	Kind              string                        `json:"kind"`
	SchemaVersion     int                           `json:"schema_version"`
	ID                string                        `json:"id"`
	BeforeOld         jsonRunQueryDependencyRecord  `json:"before_old"`
	BeforeReplacement *jsonRunQueryDependencyRecord `json:"before_replacement,omitempty"`
	AfterOld          jsonRunQueryDependencyRecord  `json:"after_old"`
	AfterReplacement  jsonRunQueryDependencyRecord  `json:"after_replacement"`
	PreparedAt        time.Time                     `json:"prepared_at"`
}

func queryJobEventPath(cfgDir, jobID string, sequence int64) (string, error) {
	dir, err := jsondb.QueryJobEventsDir(cfgDir, jobID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%020d.json", sequence)), nil
}

func queryResultChunkPath(cfgDir, jobID string, sequence int64) (string, error) {
	dir, err := jsondb.QueryResultChunksDir(cfgDir, jobID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%020d.json", sequence)), nil
}

func runQueryDependencyPath(cfgDir, runID, jobID string) (string, error) {
	dir, err := jsondb.RunQueryDependenciesDir(cfgDir, runID)
	if err != nil {
		return "", err
	}
	jobID = strings.TrimSpace(jobID)
	if strings.Contains(jobID, "/") || strings.Contains(jobID, "\\") || jobID == "" {
		return "", errors.New("invalid dependency job id")
	}
	return filepath.Join(dir, jobID+".json"), nil
}

func continuationCheckpointPath(cfgDir, runID string, sequence int64) (string, error) {
	dir, err := jsondb.RunContinuationCheckpointsDir(cfgDir, runID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%020d.json", sequence)), nil
}

func queryCapacityPath(cfgDir, jobID string) (string, error) {
	path, err := jsondb.QueryJobPath(cfgDir, jobID)
	if err != nil {
		return "", err
	}
	return filepath.Join(jsondb.QueryCapacityReservationsDir(cfgDir), filepath.Base(path)), nil
}

func queryTransactionPath(cfgDir, id string) (string, error) {
	path, err := jsondb.QueryJobPath(cfgDir, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(jsondb.QueryTransactionsDir(cfgDir), filepath.Base(path)), nil
}

func (s *JSONStore) readJSONQueryJobFileLocked(path string) (jsonQueryJobRecord, error) {
	var record jsonQueryJobRecord
	if err := readJSONLoose(path, &record); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if record.Kind != queryJobRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
		return jsonQueryJobRecord{}, errors.New("query job record kind or schema mismatch")
	}
	if err := sanitizeLegacyQueryJobRecordIdentity(&record.Job); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if err := validateQueryJobRecord(record.Job); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if record.Job.ValidationState == "" {
		if record.ValidationState != "" {
			record.Job.ValidationState = jobs.ValidationState(record.ValidationState)
		} else {
			record.Job.ValidationState = jobs.ValidationUnvalidated
		}
	}
	if record.Job.ValidationDetail == "" {
		record.Job.ValidationDetail = record.ValidationDetail
	}
	record.ValidationState = string(record.Job.ValidationState)
	record.ValidationDetail = record.Job.ValidationDetail
	expectedConnectionKey := queryConnectionKey(record.Job)
	if record.ConnectionKey != "" && record.ConnectionKey != expectedConnectionKey {
		return jsonQueryJobRecord{}, errors.New("query job record has non-canonical connection key")
	}
	record.ConnectionKey = expectedConnectionKey
	return record, nil
}

// repairJSONQueryJobIdentityLocked gives legacy sentinel/malformed managed
// primary ids a deterministic internal identity. The repaired job is written
// first, every durable reference is then rewritten, and the old job file is
// deleted last. A crash before that final delete simply replays the same
// deterministic repair on the next recovery scan.
func (s *JSONStore) repairJSONQueryJobIdentityLocked(path string) (jsonQueryJobRecord, error) {
	var source jsonQueryJobRecord
	if err := readJSONLoose(path, &source); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if source.Kind != queryJobRecordKind || source.SchemaVersion != longHorizonSchemaVersion {
		return jsonQueryJobRecord{}, errors.New("query job record kind or schema mismatch")
	}
	oldID := strings.TrimSpace(source.Job.ID)
	if !queryJobIdentityNeedsRepair(oldID) {
		return jsonQueryJobRecord{}, fmt.Errorf("query job %q does not require identity repair: %w", oldID, ErrConflict)
	}
	if source.MonitorLease != nil && source.MonitorLease.ExpiresAt.After(nowUTC()) {
		return jsonQueryJobRecord{}, jobs.ErrLeaseLost
	}
	source.MonitorLease = nil
	expectedOldPath, err := jsondb.QueryJobPath(s.cfgDir, oldID)
	if err != nil || filepath.Clean(expectedOldPath) != filepath.Clean(path) {
		return jsonQueryJobRecord{}, fmt.Errorf("quarantined query job identity does not match its filename: %w", ErrConflict)
	}
	newID := repairedManagedQueryJobID(source.Job)
	source.Job.ID = newID
	if strings.TrimSpace(source.Job.ReplacesJobID) == oldID || jobs.IsQueryJobIDSentinel(source.Job.ReplacesJobID) {
		source.Job.ReplacesJobID = ""
	}
	if source.Job.ValidationState == "" {
		if source.ValidationState != "" {
			source.Job.ValidationState = jobs.ValidationState(source.ValidationState)
		} else {
			source.Job.ValidationState = jobs.ValidationUnvalidated
		}
	}
	if source.Job.ValidationDetail == "" {
		source.Job.ValidationDetail = source.ValidationDetail
	}
	if err := sanitizeLegacyQueryJobRecordIdentity(&source.Job); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if err := validateQueryJobRecord(source.Job); err != nil {
		return jsonQueryJobRecord{}, err
	}
	source.ConnectionKey = queryConnectionKey(source.Job)
	source.ValidationState = string(source.Job.ValidationState)
	source.ValidationDetail = source.Job.ValidationDetail
	source.UpdatedAt = nowUTC()
	targetPath, err := jsondb.QueryJobPath(s.cfgDir, newID)
	if err != nil {
		return jsonQueryJobRecord{}, err
	}
	if existing, existingErr := s.readJSONQueryJobFileLocked(targetPath); existingErr == nil {
		if strings.ToLower(strings.TrimSpace(existing.Job.Provider)) != strings.ToLower(strings.TrimSpace(source.Job.Provider)) ||
			existing.Job.Owner.ThreadID != source.Job.Owner.ThreadID || existing.Job.IdempotencyKey != source.Job.IdempotencyKey ||
			!existing.Job.SubmittedAt.Equal(source.Job.SubmittedAt) {
			return jsonQueryJobRecord{}, fmt.Errorf("repaired query job identity collision at %s: %w", newID, ErrConflict)
		}
		if existing.Job.Revision >= source.Job.Revision {
			source = existing
		}
	} else if !errors.Is(existingErr, fs.ErrNotExist) {
		return jsonQueryJobRecord{}, existingErr
	}
	if err := jsondb.WriteJSONFile(targetPath, source); err != nil {
		return jsonQueryJobRecord{}, err
	}

	if err := s.rewriteJSONQueryJobIdentityReferencesLocked(oldID, newID); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if err := s.copyJSONQueryJobSidecarsLocked(oldID, newID); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if err := s.rewriteJSONQueryCapacityIdentityLocked(oldID, newID); err != nil {
		return jsonQueryJobRecord{}, err
	}
	oldEvents, _ := jsondb.QueryJobEventsDir(s.cfgDir, oldID)
	oldChunks, _ := jsondb.QueryResultChunksDir(s.cfgDir, oldID)
	if err := os.RemoveAll(oldEvents); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if err := os.RemoveAll(oldChunks); err != nil {
		return jsonQueryJobRecord{}, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return jsonQueryJobRecord{}, err
	}
	return s.readJSONQueryJobFileLocked(targetPath)
}

func (s *JSONStore) repairQuarantinedJSONQueryJobIdentitiesLocked() (map[string]jsonQueryJobRecord, error) {
	deferred := make(map[string]jsonQueryJobRecord)
	entries, err := os.ReadDir(jsondb.QueryJobsDir(s.cfgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return deferred, nil
	}
	if err != nil {
		return nil, err
	}
	// Classify every row against one clock sample. A lease expiring between a
	// repair pass and a second scan must not make the same row disappear from
	// both sets and expose its still-unrewritten journals to recovery.
	classificationNow := nowUTC()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryJobsDir(s.cfgDir), entry.Name())
		if _, err := s.readJSONQueryJobFileLocked(path); !errors.Is(err, ErrQuarantinedQueryJobIdentity) {
			if err != nil {
				return nil, err
			}
			continue
		}
		var raw jsonQueryJobRecord
		if err := readJSONLoose(path, &raw); err != nil {
			return nil, err
		}
		jobID := strings.TrimSpace(raw.Job.ID)
		if queryJobIdentityNeedsRepair(jobID) && raw.MonitorLease != nil && raw.MonitorLease.ExpiresAt.After(classificationNow) {
			deferred[jobID] = raw
			continue
		}
		if _, err := s.repairJSONQueryJobIdentityLocked(path); err != nil {
			return nil, err
		}
	}
	return deferred, nil
}

// liveQuarantinedJSONQueryJobsLocked returns malformed-primary records that
// are still protected by another monitor's durable lease. They cannot be
// re-keyed yet because that monitor still addresses the legacy id, but they
// must remain visible to capacity accounting and must keep their prepared
// journals deferred until the lease expires.
func (s *JSONStore) liveQuarantinedJSONQueryJobsLocked() (map[string]jsonQueryJobRecord, error) {
	entries, err := os.ReadDir(jsondb.QueryJobsDir(s.cfgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]jsonQueryJobRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	live := make(map[string]jsonQueryJobRecord)
	now := nowUTC()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryJobsDir(s.cfgDir), entry.Name())
		if _, err := s.readJSONQueryJobFileLocked(path); !errors.Is(err, ErrQuarantinedQueryJobIdentity) {
			if err != nil {
				return nil, err
			}
			continue
		}
		var record jsonQueryJobRecord
		if err := readJSONLoose(path, &record); err != nil {
			return nil, err
		}
		if record.Kind != queryJobRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
			return nil, errors.New("query job record kind or schema mismatch")
		}
		jobID := strings.TrimSpace(record.Job.ID)
		if !queryJobIdentityNeedsRepair(jobID) || record.MonitorLease == nil || !record.MonitorLease.ExpiresAt.After(now) {
			continue
		}
		expectedPath, pathErr := jsondb.QueryJobPath(s.cfgDir, jobID)
		if pathErr != nil || filepath.Clean(expectedPath) != filepath.Clean(path) {
			return nil, fmt.Errorf("quarantined query job identity does not match its filename: %w", ErrConflict)
		}
		live[jobID] = record
	}
	return live, nil
}

func queryTransactionReferencesLiveQuarantinedJob(path string, live map[string]jsonQueryJobRecord) (bool, error) {
	if len(live) == 0 {
		return false, nil
	}
	matches := func(values ...string) bool {
		for _, value := range values {
			if _, ok := live[strings.TrimSpace(value)]; ok {
				return true
			}
		}
		return false
	}
	jobReferences := func(record jsonQueryJobRecord) bool {
		return matches(record.Job.ID, record.Job.ReplacesJobID)
	}
	dependencyReferences := func(record *jsonRunQueryDependencyRecord) bool {
		return record != nil && matches(record.JobID, record.ReplacementJobID)
	}
	var header struct {
		Kind string `json:"kind"`
	}
	if err := readJSONLoose(path, &header); err != nil {
		return false, err
	}
	switch header.Kind {
	case queryTransactionRecordKind:
		var transaction jsonQueryTransactionRecord
		if err := readJSONLoose(path, &transaction); err != nil {
			return false, err
		}
		return matches(
			transaction.Commit.JobID, transaction.Commit.Record.ID, transaction.Commit.Record.ReplacesJobID,
			transaction.Chunk.JobID, transaction.Event.JobID,
		), nil
	case queryMutationTransactionKind:
		var transaction jsonQueryMutationTransactionRecord
		if err := readJSONLoose(path, &transaction); err != nil {
			return false, err
		}
		if jobReferences(transaction.BeforeJob) || jobReferences(transaction.AfterJob) {
			return true, nil
		}
		for _, event := range transaction.BeforeEvents {
			if matches(event.JobID) {
				return true, nil
			}
		}
		for _, event := range transaction.AfterEvents {
			if matches(event.JobID) {
				return true, nil
			}
		}
		if transaction.BeforeCapacity != nil && matches(transaction.BeforeCapacity.JobID) {
			return true, nil
		}
		return transaction.AfterCapacity != nil && matches(transaction.AfterCapacity.JobID), nil
	case runSuspendTransactionRecordKind:
		var transaction jsonRunSuspendTransactionRecord
		if err := readJSONLoose(path, &transaction); err != nil {
			return false, err
		}
		return matches(transaction.Checkpoint.DependencyIDs...), nil
	case runDependencyReplacementRecordKind:
		var transaction jsonRunDependencyReplacementTransactionRecord
		if err := readJSONLoose(path, &transaction); err != nil {
			return false, err
		}
		return dependencyReferences(&transaction.BeforeOld) || dependencyReferences(transaction.BeforeReplacement) ||
			dependencyReferences(&transaction.AfterOld) || dependencyReferences(&transaction.AfterReplacement), nil
	}
	return false, nil
}

func (s *JSONStore) pendingJSONRunSuspendRunsLocked() (map[string]bool, error) {
	pending := make(map[string]bool)
	entries, err := os.ReadDir(jsondb.QueryTransactionsDir(s.cfgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return pending, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryTransactionsDir(s.cfgDir), entry.Name())
		var header struct {
			Kind          string `json:"kind"`
			SchemaVersion int    `json:"schema_version"`
		}
		if err := readJSONLoose(path, &header); err != nil {
			return nil, err
		}
		if header.Kind != runSuspendTransactionRecordKind || header.SchemaVersion != longHorizonSchemaVersion {
			continue
		}
		var transaction jsonRunSuspendTransactionRecord
		if err := readJSONLoose(path, &transaction); err != nil {
			return nil, err
		}
		runID := strings.TrimSpace(transaction.AfterRun.ID)
		if runID == "" {
			runID = strings.TrimSpace(transaction.Checkpoint.RunID)
		}
		if runID == "" {
			return nil, errors.New("pending run suspend transaction is missing run id")
		}
		pending[runID] = true
	}
	return pending, nil
}

func (s *JSONStore) copyJSONQueryJobSidecarsLocked(oldID, newID string) error {
	oldEvents, err := jsondb.QueryJobEventsDir(s.cfgDir, oldID)
	if err != nil {
		return err
	}
	newEvents, err := jsondb.QueryJobEventsDir(s.cfgDir, newID)
	if err != nil {
		return err
	}
	if entries, readErr := os.ReadDir(oldEvents); readErr == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var event jsonQueryJobEventRecord
			if err := readJSONLoose(filepath.Join(oldEvents, entry.Name()), &event); err != nil {
				return err
			}
			event.JobID = newID
			if err := jsondb.WriteJSONFile(filepath.Join(newEvents, entry.Name()), event); err != nil {
				return err
			}
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}

	oldChunks, err := jsondb.QueryResultChunksDir(s.cfgDir, oldID)
	if err != nil {
		return err
	}
	newChunks, err := jsondb.QueryResultChunksDir(s.cfgDir, newID)
	if err != nil {
		return err
	}
	if entries, readErr := os.ReadDir(oldChunks); readErr == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var chunk jsonQueryResultChunkRecord
			if err := readJSONLoose(filepath.Join(oldChunks, entry.Name()), &chunk); err != nil {
				return err
			}
			chunk.JobID = newID
			if err := jsondb.WriteJSONFile(filepath.Join(newChunks, entry.Name()), chunk); err != nil {
				return err
			}
		}
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	return nil
}

func (s *JSONStore) rewriteJSONQueryJobIdentityReferencesLocked(oldID, newID string) error {
	entries, err := os.ReadDir(jsondb.QueryJobsDir(s.cfgDir))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryJobsDir(s.cfgDir), entry.Name())
		var record jsonQueryJobRecord
		if err := readJSONLoose(path, &record); err != nil {
			return err
		}
		if strings.TrimSpace(record.Job.ID) == oldID || strings.TrimSpace(record.Job.ReplacesJobID) != oldID {
			continue
		}
		record.Job.ReplacesJobID = newID
		record.UpdatedAt = nowUTC()
		if err := jsondb.WriteJSONFile(path, record); err != nil {
			return err
		}
	}

	root := filepath.Join(jsondb.RootDir(s.cfgDir), "run_query_dependencies")
	runs, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, runEntry := range runs {
		if !runEntry.IsDir() {
			continue
		}
		runDir := filepath.Join(root, runEntry.Name())
		dependencies, readErr := os.ReadDir(runDir)
		if readErr != nil {
			return readErr
		}
		for _, entry := range dependencies {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			oldPath := filepath.Join(runDir, entry.Name())
			var record jsonRunQueryDependencyRecord
			if err := readJSONLoose(oldPath, &record); err != nil {
				return err
			}
			changedPrimary := strings.TrimSpace(record.JobID) == oldID
			changedReplacement := strings.TrimSpace(record.ReplacementJobID) == oldID
			if !changedPrimary && !changedReplacement {
				continue
			}
			if changedPrimary {
				record.JobID = newID
			}
			if changedReplacement {
				record.ReplacementJobID = newID
			}
			record.UpdatedAt = nowUTC()
			newPath := oldPath
			if changedPrimary {
				newPath, err = runQueryDependencyPath(s.cfgDir, record.RunID, newID)
				if err != nil {
					return err
				}
			}
			if err := jsondb.WriteJSONFile(newPath, record); err != nil {
				return err
			}
			if newPath != oldPath {
				if err := os.Remove(oldPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}
		}
	}
	checkpointRoot := filepath.Join(jsondb.RootDir(s.cfgDir), "run_continuation_checkpoints")
	checkpointRuns, checkpointErr := os.ReadDir(checkpointRoot)
	if checkpointErr != nil && !errors.Is(checkpointErr, fs.ErrNotExist) {
		return checkpointErr
	}
	for _, runEntry := range checkpointRuns {
		if !runEntry.IsDir() {
			continue
		}
		runDir := filepath.Join(checkpointRoot, runEntry.Name())
		entries, err := os.ReadDir(runDir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			path := filepath.Join(runDir, entry.Name())
			var record jsonContinuationCheckpointRecord
			if err := readJSONLoose(path, &record); err != nil {
				return err
			}
			changed := false
			for index := range record.DependencyIDs {
				if strings.TrimSpace(record.DependencyIDs[index]) == oldID {
					record.DependencyIDs[index] = newID
					changed = true
				}
			}
			if changed {
				if err := jsondb.WriteJSONFile(path, record); err != nil {
					return err
				}
			}
		}
	}
	if err := s.rewriteJSONQueryTransactionIdentitiesLocked(oldID, newID); err != nil {
		return err
	}
	return nil
}

func (s *JSONStore) rewriteJSONQueryTransactionIdentitiesLocked(oldID, newID string) error {
	entries, err := os.ReadDir(jsondb.QueryTransactionsDir(s.cfgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	rewriteJob := func(record *jsonQueryJobRecord) {
		if record == nil {
			return
		}
		if strings.TrimSpace(record.Job.ID) == oldID {
			record.Job.ID = newID
			if strings.TrimSpace(record.Job.ReplacesJobID) == oldID || jobs.IsQueryJobIDSentinel(record.Job.ReplacesJobID) {
				record.Job.ReplacesJobID = ""
			}
		} else if strings.TrimSpace(record.Job.ReplacesJobID) == oldID {
			record.Job.ReplacesJobID = newID
		}
	}
	rewriteEvent := func(event *jsonQueryJobEventRecord) {
		if event != nil && strings.TrimSpace(event.JobID) == oldID {
			event.JobID = newID
		}
	}
	rewriteCapacity := func(capacity *jsonQueryCapacityRecord) {
		if capacity != nil && strings.TrimSpace(capacity.JobID) == oldID {
			capacity.JobID = newID
		}
	}
	rewriteDependency := func(dependency *jsonRunQueryDependencyRecord) {
		if dependency == nil {
			return
		}
		if strings.TrimSpace(dependency.JobID) == oldID {
			dependency.JobID = newID
		}
		if strings.TrimSpace(dependency.ReplacementJobID) == oldID {
			dependency.ReplacementJobID = newID
		}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryTransactionsDir(s.cfgDir), entry.Name())
		var header struct {
			Kind string `json:"kind"`
		}
		if err := readJSONLoose(path, &header); err != nil {
			return err
		}
		switch header.Kind {
		case queryTransactionRecordKind:
			var transaction jsonQueryTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return err
			}
			if strings.TrimSpace(transaction.Commit.JobID) == oldID {
				transaction.Commit.JobID = newID
			}
			if strings.TrimSpace(transaction.Commit.Record.ID) == oldID {
				transaction.Commit.Record.ID = newID
				if strings.TrimSpace(transaction.Commit.Record.ReplacesJobID) == oldID ||
					jobs.IsQueryJobIDSentinel(transaction.Commit.Record.ReplacesJobID) {
					transaction.Commit.Record.ReplacesJobID = ""
				}
			} else if strings.TrimSpace(transaction.Commit.Record.ReplacesJobID) == oldID {
				transaction.Commit.Record.ReplacesJobID = newID
			}
			if strings.TrimSpace(transaction.Chunk.JobID) == oldID {
				transaction.Chunk.JobID = newID
			}
			if strings.TrimSpace(transaction.Event.JobID) == oldID {
				transaction.Event.JobID = newID
			}
			if err := jsondb.WriteJSONFile(path, transaction); err != nil {
				return err
			}
		case queryMutationTransactionKind:
			var transaction jsonQueryMutationTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return err
			}
			rewriteJob(&transaction.BeforeJob)
			rewriteJob(&transaction.AfterJob)
			for index := range transaction.BeforeEvents {
				rewriteEvent(&transaction.BeforeEvents[index])
			}
			for index := range transaction.AfterEvents {
				rewriteEvent(&transaction.AfterEvents[index])
			}
			rewriteCapacity(transaction.BeforeCapacity)
			rewriteCapacity(transaction.AfterCapacity)
			if err := jsondb.WriteJSONFile(path, transaction); err != nil {
				return err
			}
		case runSuspendTransactionRecordKind:
			var transaction jsonRunSuspendTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return err
			}
			for index := range transaction.Checkpoint.DependencyIDs {
				if strings.TrimSpace(transaction.Checkpoint.DependencyIDs[index]) == oldID {
					transaction.Checkpoint.DependencyIDs[index] = newID
				}
			}
			if err := jsondb.WriteJSONFile(path, transaction); err != nil {
				return err
			}
		case runDependencyReplacementRecordKind:
			var transaction jsonRunDependencyReplacementTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return err
			}
			rewriteDependency(&transaction.BeforeOld)
			rewriteDependency(transaction.BeforeReplacement)
			rewriteDependency(&transaction.AfterOld)
			rewriteDependency(&transaction.AfterReplacement)
			if err := jsondb.WriteJSONFile(path, transaction); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *JSONStore) rewriteJSONQueryCapacityIdentityLocked(oldID, newID string) error {
	oldPath, err := queryCapacityPath(s.cfgDir, oldID)
	if err != nil {
		return err
	}
	var capacity jsonQueryCapacityRecord
	if err := readJSONLoose(oldPath, &capacity); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	capacity.JobID = newID
	newPath, err := queryCapacityPath(s.cfgDir, newID)
	if err != nil {
		return err
	}
	if err := jsondb.WriteJSONFile(newPath, capacity); err != nil {
		return err
	}
	if err := os.Remove(oldPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *JSONStore) readJSONQueryJobLocked(jobID string, includeWakes bool) (jsonQueryJobRecord, error) {
	path, err := jsondb.QueryJobPath(s.cfgDir, strings.TrimSpace(jobID))
	if err != nil {
		return jsonQueryJobRecord{}, err
	}
	record, err := s.readJSONQueryJobFileLocked(path)
	if errors.Is(err, fs.ErrNotExist) {
		return jsonQueryJobRecord{}, ErrNotFound
	}
	if err != nil {
		return jsonQueryJobRecord{}, err
	}
	if includeWakes {
		events, err := s.listJSONQueryJobEventsLocked(jobID, 0, 0)
		if err != nil {
			return jsonQueryJobRecord{}, err
		}
		record.Job.WakeEvents = nil
		for _, event := range events {
			if event.WakeKind == "" {
				continue
			}
			record.Job.WakeEvents = append(record.Job.WakeEvents, jobs.WakeEvent{
				At: event.CreatedAt, Kind: event.WakeKind, Detail: event.Detail, Claimed: event.ClaimedAt != nil,
			})
		}
	}
	return record, nil
}

func (s *JSONStore) writeJSONQueryJobLocked(record jsonQueryJobRecord) error {
	if record.Kind == "" {
		record.Kind = queryJobRecordKind
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = longHorizonSchemaVersion
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = nowUTC()
	}
	path, err := jsondb.QueryJobPath(s.cfgDir, record.Job.ID)
	if err != nil {
		return err
	}
	return jsondb.WriteJSONFile(path, record)
}

func (s *JSONStore) freezeJSONQueryJobIdentityLocked(current jobs.JobRecord, proposed *jobs.JobRecord) error {
	if proposed == nil {
		return freezeQueryJobIdentity(current, proposed)
	}
	requestedRunID := strings.TrimSpace(proposed.Owner.RunID)
	if err := freezeQueryJobIdentity(current, proposed); err != nil {
		return err
	}
	if strings.TrimSpace(current.Owner.RunID) != "" || requestedRunID == "" {
		return nil
	}
	if jobs.RequiresQueryObligationBinding(current) {
		// A managed job's run was authoritative at reservation time. A durable
		// blank therefore means that run was retired/deleted; attaching it to a
		// later same-thread run would resurrect a completed obligation scope.
		return nil
	}
	run, _, err := s.readRunWithPath(requestedRunID)
	if errors.Is(err, ErrNotFound) {
		// Jobs outlive runs. A monitor may still hold the deleted run id after
		// MySQL's ON DELETE SET NULL (or the equivalent JSON cleanup).
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(run.ThreadID) != strings.TrimSpace(current.Owner.ThreadID) {
		return fmt.Errorf("query job %s cannot attach to a run in another thread: %w", current.ID, ErrConflict)
	}
	proposed.Owner.RunID = requestedRunID
	return nil
}

func (s *JSONStore) upsertJSONQueryJobLocked(record jobs.JobRecord, expectedRevision *int64) (int64, error) {
	if err := validateQueryJobRecord(record); err != nil {
		return 0, err
	}
	if record.ValidationState == "" {
		record.ValidationState = jobs.ValidationUnvalidated
	}
	path, err := jsondb.QueryJobPath(s.cfgDir, record.ID)
	if err != nil {
		return 0, err
	}
	existing, readErr := s.readJSONQueryJobFileLocked(path)
	exists := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return 0, readErr
	}
	if expectedRevision != nil {
		if (!exists && *expectedRevision != 0) || (exists && existing.Job.Revision != *expectedRevision) {
			return 0, jobs.ErrRevisionConflict
		}
	}
	if exists {
		if err := s.freezeJSONQueryJobIdentityLocked(existing.Job, &record); err != nil {
			return 0, err
		}
	}
	if err := s.ensureJSONIdempotencyUniqueLocked(record); err != nil {
		return 0, err
	}
	if exists {
		record.Revision = existing.Job.Revision + 1
	} else {
		record.Revision = 1
	}
	if err := s.syncJSONWakeEventsLocked(record); err != nil {
		return 0, err
	}
	wrapper := jsonQueryJobRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ConnectionKey: queryConnectionKey(record), Job: record,
		ValidationState: string(record.ValidationState), ValidationDetail: record.ValidationDetail, UpdatedAt: nowUTC(),
	}
	if exists {
		wrapper.MonitorLease = existing.MonitorLease
	}
	if err := s.writeJSONQueryJobLocked(wrapper); err != nil {
		return 0, err
	}
	if err := s.syncJSONCapacityLocked(record); err != nil {
		return 0, err
	}
	return record.Revision, nil
}

func (s *JSONStore) ensureJSONIdempotencyUniqueLocked(record jobs.JobRecord) error {
	idempotencyKey := strings.TrimSpace(record.IdempotencyKey)
	if idempotencyKey == "" {
		return nil
	}
	entries, err := os.ReadDir(jsondb.QueryJobsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	provider := strings.ToLower(strings.TrimSpace(record.Provider))
	connectionKey := queryConnectionKey(record)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryJobsDir(s.cfgDir), entry.Name())
		existing, err := s.readJSONQueryJobFileLocked(path)
		if errors.Is(err, ErrQuarantinedQueryJobIdentity) {
			// Identity repair and journal replay are one recovery operation. If
			// this row remained quarantined after the pass above, conservatively
			// inspect its immutable idempotency tuple without re-keying it here.
			if readErr := readJSONLoose(path, &existing); readErr != nil {
				return readErr
			}
			if strings.ToLower(strings.TrimSpace(existing.Job.Provider)) == provider &&
				queryConnectionKey(existing.Job) == connectionKey &&
				strings.TrimSpace(existing.Job.IdempotencyKey) == idempotencyKey {
				return jobs.ErrLeaseLost
			}
			continue
		}
		if err != nil {
			return err
		}
		if existing.Job.ID == record.ID {
			continue
		}
		if strings.ToLower(strings.TrimSpace(existing.Job.Provider)) == provider &&
			existing.ConnectionKey == connectionKey && strings.TrimSpace(existing.Job.IdempotencyKey) == idempotencyKey {
			return jobs.ErrRevisionConflict
		}
	}
	return nil
}

func (s *JSONStore) UpsertQueryJob(ctx context.Context, record jobs.JobRecord) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return err
	}
	_, err := s.upsertJSONQueryJobLocked(record, nil)
	return err
}

func (s *JSONStore) UpsertQueryJobCAS(ctx context.Context, record jobs.JobRecord, expectedRevision int64) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return 0, err
	}
	return s.upsertJSONQueryJobLocked(record, &expectedRevision)
}

func (s *JSONStore) GetQueryJob(ctx context.Context, id string) (jobs.JobRecord, bool, error) {
	select {
	case <-ctx.Done():
		return jobs.JobRecord{}, false, ctx.Err()
	default:
	}
	if jobs.IsQueryJobIDSentinel(strings.TrimSpace(id)) {
		return jobs.JobRecord{}, false, nil
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return jobs.JobRecord{}, false, err
	}
	record, err := s.readJSONQueryJobLocked(id, true)
	if errors.Is(err, ErrNotFound) {
		return jobs.JobRecord{}, false, nil
	}
	if err != nil {
		return jobs.JobRecord{}, false, err
	}
	return record.Job, true, nil
}

func (s *JSONStore) GetQueryJobByIdempotency(ctx context.Context, provider, connectionKey, key string) (jobs.JobRecord, bool, error) {
	select {
	case <-ctx.Done():
		return jobs.JobRecord{}, false, ctx.Err()
	default:
	}
	provider, connectionKey, key, err := normalizeQueryIdempotencyIdentity(provider, connectionKey, key)
	if err != nil {
		return jobs.JobRecord{}, false, err
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return jobs.JobRecord{}, false, err
	}
	entries, err := os.ReadDir(jsondb.QueryJobsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jobs.JobRecord{}, false, nil
		}
		return jobs.JobRecord{}, false, err
	}
	matchID := ""
	var matchSubmittedAt time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryJobsDir(s.cfgDir), entry.Name())
		record, err := s.readJSONQueryJobFileLocked(path)
		if errors.Is(err, ErrQuarantinedQueryJobIdentity) {
			if readErr := readJSONLoose(path, &record); readErr != nil {
				return jobs.JobRecord{}, false, readErr
			}
			if strings.ToLower(strings.TrimSpace(record.Job.Provider)) == provider &&
				queryConnectionKey(record.Job) == connectionKey && strings.TrimSpace(record.Job.IdempotencyKey) == key {
				return jobs.JobRecord{}, false, jobs.ErrLeaseLost
			}
			continue
		}
		if err != nil {
			return jobs.JobRecord{}, false, err
		}
		if strings.ToLower(strings.TrimSpace(record.Job.Provider)) != provider || record.ConnectionKey != connectionKey ||
			strings.TrimSpace(record.Job.IdempotencyKey) != key {
			continue
		}
		if matchID == "" || record.Job.SubmittedAt.Before(matchSubmittedAt) ||
			(record.Job.SubmittedAt.Equal(matchSubmittedAt) && record.Job.ID < matchID) {
			matchID = record.Job.ID
			matchSubmittedAt = record.Job.SubmittedAt
		}
	}
	if matchID == "" {
		return jobs.JobRecord{}, false, nil
	}
	record, err := s.readJSONQueryJobLocked(matchID, true)
	if err != nil {
		return jobs.JobRecord{}, false, err
	}
	return record.Job, true, nil
}

func (s *JSONStore) listJSONQueryJobsLocked(filter func(jobs.JobRecord) bool) ([]jobs.JobRecord, error) {
	entries, err := os.ReadDir(jsondb.QueryJobsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []jobs.JobRecord
	seenIDs := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryJobsDir(s.cfgDir), entry.Name())
		record, err := s.readJSONQueryJobFileLocked(path)
		if errors.Is(err, ErrQuarantinedQueryJobIdentity) {
			// Recovery owns re-keying so every prepared journal is rewritten and
			// applied in the same critical section. A listing may omit this row for
			// one pass if its lease expires immediately after classification.
			continue
		}
		if err != nil {
			return nil, err
		}
		if seenIDs[record.Job.ID] {
			continue
		}
		seenIDs[record.Job.ID] = true
		if filter == nil || filter(record.Job) {
			events, err := s.listJSONQueryJobEventsLocked(record.Job.ID, 0, 0)
			if err != nil {
				return nil, err
			}
			record.Job.WakeEvents = nil
			for _, event := range events {
				if event.WakeKind != "" {
					record.Job.WakeEvents = append(record.Job.WakeEvents, jobs.WakeEvent{At: event.CreatedAt, Kind: event.WakeKind, Detail: event.Detail, Claimed: event.ClaimedAt != nil})
				}
			}
			out = append(out, record.Job)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SubmittedAt.Equal(out[j].SubmittedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].SubmittedAt.Before(out[j].SubmittedAt)
	})
	return out, nil
}

// listJSONQueryJobsForCapacityLocked includes live leased records whose
// primary identity is awaiting repair. Public lifecycle listings omit those
// unusable ids, but admission and reconciliation must conservatively count
// the remote work until its owner lease expires and repair can proceed.
func (s *JSONStore) listJSONQueryJobsForCapacityLocked() ([]jobs.JobRecord, error) {
	entries, err := os.ReadDir(jsondb.QueryJobsDir(s.cfgDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]jobs.JobRecord, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryJobsDir(s.cfgDir), entry.Name())
		record, readErr := s.readJSONQueryJobFileLocked(path)
		if errors.Is(readErr, ErrQuarantinedQueryJobIdentity) {
			// Recovery is the only place that may re-key a quarantined job.
			// Capacity scans use the raw lifecycle record so a lease expiring
			// mid-critical-section cannot rewrite identities without also
			// applying their prepared journals.
			if err := readJSONLoose(path, &record); err != nil {
				return nil, err
			}
			if record.Kind != queryJobRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
				return nil, errors.New("query job record kind or schema mismatch")
			}
			readErr = nil
		}
		if readErr != nil {
			return nil, readErr
		}
		identity := strings.TrimSpace(record.Job.ID)
		if identity == "" {
			identity = entry.Name()
		}
		if seen[identity] {
			continue
		}
		records = append(records, record.Job)
		seen[identity] = true
	}
	return records, nil
}

func (s *JSONStore) ListActiveQueryJobs(ctx context.Context) ([]jobs.JobRecord, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return nil, err
	}
	return s.listJSONQueryJobsLocked(func(record jobs.JobRecord) bool { return isActiveQueryExecutionState(record.Status) })
}

func (s *JSONStore) ListQueryJobsByThread(ctx context.Context, threadID string) ([]jobs.JobRecord, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, errors.New("thread id is required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return nil, err
	}
	return s.listJSONQueryJobsLocked(func(record jobs.JobRecord) bool { return record.Owner.ThreadID == threadID })
}

func (s *JSONStore) DeleteQueryJob(ctx context.Context, id string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	path, err := jsondb.QueryJobPath(s.cfgDir, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	if dir, err := jsondb.QueryJobEventsDir(s.cfgDir, id); err == nil {
		_ = os.RemoveAll(dir)
	}
	if dir, err := jsondb.QueryResultChunksDir(s.cfgDir, id); err == nil {
		_ = os.RemoveAll(dir)
	}
	if path, err := queryCapacityPath(s.cfgDir, id); err == nil {
		_ = os.Remove(path)
	}
	return nil
}

func (s *JSONStore) syncJSONWakeEventsLocked(record jobs.JobRecord) error {
	events, err := s.listJSONQueryJobEventsLocked(record.ID, 0, 0)
	if err != nil {
		return err
	}
	byKey := make(map[string]QueryJobEvent, len(events))
	var maxSequence int64
	for _, event := range events {
		byKey[event.ObservationKey] = event
		if event.Sequence > maxSequence {
			maxSequence = event.Sequence
		}
	}
	for _, wake := range record.WakeEvents {
		key := wakeObservationKey(wake)
		event, ok := byKey[key]
		if !ok {
			maxSequence++
			event = QueryJobEvent{
				JobID: record.ID, Sequence: maxSequence, Type: "wake", WakeKind: wake.Kind,
				Detail: wake.Detail, ObservationKey: key, CreatedAt: wake.At.UTC(),
			}
		}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = nowUTC()
		}
		if wake.Claimed && event.ClaimedAt == nil {
			at := event.CreatedAt
			event.ClaimedBy, event.ClaimedAt = "record", &at
		}
		if err := s.writeJSONQueryJobEventLocked(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONStore) writeJSONQueryJobEventLocked(event QueryJobEvent) error {
	event, err := normalizeQueryJobEvent(event)
	if err != nil {
		return err
	}
	if event.Sequence <= 0 {
		return errors.New("query event sequence must be positive")
	}
	path, err := queryJobEventPath(s.cfgDir, event.JobID, event.Sequence)
	if err != nil {
		return err
	}
	record := jsonQueryJobEventRecord{Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryJobEvent: event}
	return jsondb.WriteJSONFile(path, record)
}

func parseLongHorizonSequence(name string) (int64, bool) {
	if !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSuffix(name, ".json"), 10, 64)
	return value, err == nil && value > 0
}

func (s *JSONStore) listJSONQueryJobEventsLocked(jobID string, afterSequence int64, limit int) ([]QueryJobEvent, error) {
	dir, err := jsondb.QueryJobEventsDir(s.cfgDir, jobID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []QueryJobEvent
	for _, entry := range entries {
		sequence, ok := parseLongHorizonSequence(entry.Name())
		if entry.IsDir() || !ok || sequence <= afterSequence {
			continue
		}
		var record jsonQueryJobEventRecord
		if err := readJSONLoose(filepath.Join(dir, entry.Name()), &record); err != nil {
			return nil, err
		}
		if record.Kind != queryJobEventRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
			return nil, errors.New("query job event record kind or schema mismatch")
		}
		out = append(out, record.QueryJobEvent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *JSONStore) syncJSONCapacityLocked(record jobs.JobRecord) error {
	path, err := queryCapacityPath(s.cfgDir, record.ID)
	if err != nil {
		return err
	}
	capacity := jsonQueryCapacityRecord{
		Kind: queryCapacityRecordKind, SchemaVersion: longHorizonSchemaVersion,
		JobID: record.ID, ThreadID: record.Owner.ThreadID, ConnectionKey: queryConnectionKey(record),
		State: "active", ReservedAt: record.SubmittedAt.UTC(),
	}
	if !isActiveQueryExecutionState(record.Status) {
		now := nowUTC()
		capacity.State, capacity.ReleasedAt = "released", &now
		var existing jsonQueryCapacityRecord
		if err := readJSONLoose(path, &existing); err == nil && existing.ReleasedAt != nil {
			capacity.ReservedAt, capacity.ReleasedAt = existing.ReservedAt, existing.ReleasedAt
		}
	}
	return jsondb.WriteJSONFile(path, capacity)
}

func (s *JSONStore) readJSONCapacityRecordLocked(jobID string) (jsonQueryCapacityRecord, bool, error) {
	path, err := queryCapacityPath(s.cfgDir, jobID)
	if err != nil {
		return jsonQueryCapacityRecord{}, false, err
	}
	var record jsonQueryCapacityRecord
	if err := readJSONLoose(path, &record); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsonQueryCapacityRecord{}, false, nil
		}
		return jsonQueryCapacityRecord{}, false, err
	}
	if record.Kind != queryCapacityRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
		return jsonQueryCapacityRecord{}, false, errors.New("query capacity record kind or schema mismatch")
	}
	return record, true, nil
}

func (s *JSONStore) writeJSONCapacityRecordLocked(record jsonQueryCapacityRecord) error {
	path, err := queryCapacityPath(s.cfgDir, record.JobID)
	if err != nil {
		return err
	}
	return jsondb.WriteJSONFile(path, record)
}

func (s *JSONStore) readJSONCapacityRecordsLocked() ([]jsonQueryCapacityRecord, error) {
	entries, err := os.ReadDir(jsondb.QueryCapacityReservationsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []jsonQueryCapacityRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var record jsonQueryCapacityRecord
		if err := readJSONLoose(filepath.Join(jsondb.QueryCapacityReservationsDir(s.cfgDir), entry.Name()), &record); err != nil {
			return nil, err
		}
		if record.Kind != queryCapacityRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
			return nil, errors.New("query capacity record kind or schema mismatch")
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *JSONStore) ReserveQueryCapacity(ctx context.Context, record jobs.JobRecord, limits jobs.CapacityLimits) (bool, int64, error) {
	select {
	case <-ctx.Done():
		return false, 0, ctx.Err()
	default:
	}
	if err := validateQueryJobRecord(record); err != nil {
		return false, 0, err
	}
	if limits.MaxGlobal <= 0 {
		limits.MaxGlobal = 8
	}
	if limits.MaxPerThread <= 0 {
		limits.MaxPerThread = 3
	}
	if limits.MaxPerConnection <= 0 {
		limits.MaxPerConnection = 2
	}
	if limits.MaxProfilingGlobal <= 0 {
		limits.MaxProfilingGlobal = 4
	}
	if limits.MaxProfilingPerThread <= 0 {
		limits.MaxProfilingPerThread = 1
	}
	if limits.MaxProfilingPerConnection <= 0 {
		limits.MaxProfilingPerConnection = 1
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return false, 0, err
	}
	if _, err := s.readJSONQueryJobLocked(record.ID, false); err == nil {
		return false, 0, jobs.ErrRevisionConflict
	} else if !errors.Is(err, ErrNotFound) {
		return false, 0, err
	}
	// Frozen ordering: resolve a canonical idempotency winner before looking
	// at capacity, so a replay recovers the original job even when every slot
	// is currently occupied.
	if err := s.ensureJSONIdempotencyUniqueLocked(record); err != nil {
		return false, 0, err
	}
	// Job lifecycle records are the capacity authority. Reservation sidecars
	// remain compatibility/audit data only; a stale sidecar can never strand
	// capacity after a crash because the status transition itself releases it.
	records, err := s.listJSONQueryJobsForCapacityLocked()
	if err != nil {
		return false, 0, err
	}
	global, thread, connection := 0, 0, 0
	profilingGlobal, profilingThread, profilingConnection := 0, 0, 0
	connectionKey := queryConnectionKey(record)
	for _, existing := range records {
		if !isActiveQueryExecutionState(existing.Status) {
			continue
		}
		global++
		if existing.Owner.ThreadID == record.Owner.ThreadID {
			thread++
		}
		sameConnection := queryConnectionKey(existing) == connectionKey
		if sameConnection {
			connection++
		}
		if existing.Status == jobs.JobStatusProfiling {
			profilingGlobal++
			if existing.Owner.ThreadID == record.Owner.ThreadID {
				profilingThread++
			}
			if sameConnection {
				profilingConnection++
			}
		}
	}
	if global >= limits.MaxGlobal || thread >= limits.MaxPerThread || connection >= limits.MaxPerConnection {
		return false, 0, nil
	}
	if record.Status == jobs.JobStatusProfiling &&
		((limits.MaxProfilingGlobal > 0 && profilingGlobal >= limits.MaxProfilingGlobal) ||
			(limits.MaxProfilingPerThread > 0 && profilingThread >= limits.MaxProfilingPerThread) ||
			(limits.MaxProfilingPerConnection > 0 && profilingConnection >= limits.MaxProfilingPerConnection)) {
		return false, 0, nil
	}
	expected := int64(0)
	revision, err := s.upsertJSONQueryJobLocked(record, &expected)
	return err == nil, revision, err
}

func (s *JSONStore) ReleaseQueryCapacity(ctx context.Context, jobID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	now := nowUTC()
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	path, err := queryCapacityPath(s.cfgDir, jobID)
	if err != nil {
		return err
	}
	var record jsonQueryCapacityRecord
	if err := readJSONLoose(path, &record); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if record.State != "released" {
		record.State = "released"
		releasedAt := now.UTC()
		record.ReleasedAt = &releasedAt
	}
	return jsondb.WriteJSONFile(path, record)
}

func (s *JSONStore) reconcileQueryCapacityReservations() error {
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	jobRecords, err := s.listJSONQueryJobsForCapacityLocked()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, record := range jobRecords {
		seen[record.ID] = true
		if err := s.syncJSONCapacityLocked(record); err != nil {
			return err
		}
	}
	capacities, err := s.readJSONCapacityRecordsLocked()
	if err != nil {
		return err
	}
	for _, capacity := range capacities {
		if !seen[capacity.JobID] {
			if path, err := queryCapacityPath(s.cfgDir, capacity.JobID); err == nil {
				_ = os.Remove(path)
			}
		}
	}
	return nil
}

func (s *JSONStore) AcquireMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	jobID, holder = strings.TrimSpace(jobID), strings.TrimSpace(holder)
	if jobID == "" || holder == "" || ttl <= 0 {
		return false, errors.New("job id, holder, and positive ttl are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	record, err := s.readJSONQueryJobLocked(jobID, false)
	if err != nil {
		return false, err
	}
	now := nowUTC()
	if record.MonitorLease != nil && record.MonitorLease.Holder != holder && record.MonitorLease.ExpiresAt.After(now) {
		return false, nil
	}
	record.MonitorLease = &jsonMonitorLease{Holder: holder, ExpiresAt: now.Add(ttl)}
	record.UpdatedAt = now
	return true, s.writeJSONQueryJobLocked(record)
}

func (s *JSONStore) RenewMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	jobID, holder = strings.TrimSpace(jobID), strings.TrimSpace(holder)
	if jobID == "" || holder == "" || ttl <= 0 {
		return false, errors.New("job id, holder, and positive ttl are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	record, err := s.readJSONQueryJobLocked(jobID, false)
	if err != nil {
		return false, err
	}
	now := nowUTC()
	if record.MonitorLease == nil || record.MonitorLease.Holder != holder || !record.MonitorLease.ExpiresAt.After(now) {
		return false, nil
	}
	record.MonitorLease.ExpiresAt = now.Add(ttl)
	record.UpdatedAt = now
	return true, s.writeJSONQueryJobLocked(record)
}

func (s *JSONStore) ReleaseMonitorLease(ctx context.Context, jobID, holder string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	record, err := s.readJSONQueryJobLocked(jobID, false)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.MonitorLease != nil && record.MonitorLease.Holder == strings.TrimSpace(holder) {
		record.MonitorLease = nil
		record.UpdatedAt = nowUTC()
		return s.writeJSONQueryJobLocked(record)
	}
	return nil
}

func cloneStoreNativeHandle(handle jobs.NativeHandle) jobs.NativeHandle {
	cloned := handle
	if handle.Extra != nil {
		cloned.Extra = make(map[string]string, len(handle.Extra))
		for key, value := range handle.Extra {
			cloned.Extra[key] = value
		}
	}
	return cloned
}

func cloneStoreQueryProfile(profile *jobs.QueryProfile) *jobs.QueryProfile {
	if profile == nil {
		return nil
	}
	cloned := *profile
	cloned.Warnings = append([]string(nil), profile.Warnings...)
	if profile.ProviderDetails != nil {
		raw, _ := json.Marshal(profile.ProviderDetails)
		_ = json.Unmarshal(raw, &cloned.ProviderDetails)
	}
	return &cloned
}

// ApplyProfileMutation is the single durable JSON boundary for profiling.
// The process mutex is backed by the existing advisory file lock, so the
// revision, profiling status, and exact unexpired holder are checked against
// one authoritative on-disk record across all app processes.
func (s *JSONStore) ApplyProfileMutation(ctx context.Context, jobID, holder string, expectedRevision int64, mut jobs.ProfileMutation) (jobs.ProfileMutationResult, error) {
	select {
	case <-ctx.Done():
		return jobs.ProfileMutationResult{}, ctx.Err()
	default:
	}
	jobID, holder = strings.TrimSpace(jobID), strings.TrimSpace(holder)
	if jobID == "" || holder == "" || expectedRevision < 0 {
		return jobs.ProfileMutationResult{}, errors.New("job id, holder, and expected revision are required")
	}
	terminal := false
	if mut.TransitionTo != nil {
		var valid bool
		terminal, valid = queryProfileTransition(*mut.TransitionTo)
		if !valid {
			return jobs.ProfileMutationResult{}, jobs.ErrRevisionConflict
		}
	}

	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return jobs.ProfileMutationResult{}, err
	}
	record, err := s.readJSONQueryJobLocked(jobID, false)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return jobs.ProfileMutationResult{}, jobs.ErrRevisionConflict
		}
		return jobs.ProfileMutationResult{}, err
	}
	if record.Job.Revision != expectedRevision || record.Job.Status != jobs.JobStatusProfiling {
		return jobs.ProfileMutationResult{}, jobs.ErrRevisionConflict
	}
	now := nowUTC()
	if record.MonitorLease == nil || record.MonitorLease.Holder != holder || !record.MonitorLease.ExpiresAt.After(now) {
		return jobs.ProfileMutationResult{}, jobs.ErrLeaseLost
	}

	after := record
	if mut.Dispatched != nil {
		after.Job.ProfileDispatched = *mut.Dispatched
	}
	if mut.Handle != nil {
		after.Job.ProfileHandle = cloneStoreNativeHandle(*mut.Handle)
	}
	if mut.Phase != nil {
		after.Job.ProfilePhase = *mut.Phase
	}
	if mut.Profile != nil {
		after.Job.Profile = cloneStoreQueryProfile(mut.Profile)
	}
	if mut.Outcome != nil {
		after.Job.ProfileOutcome = *mut.Outcome
	}
	if mut.Error != nil {
		after.Job.Error = *mut.Error
	}
	if mut.TransitionTo != nil {
		after.Job.Status = *mut.TransitionTo
	}
	result := jobs.ProfileMutationResult{At: now, NewRevision: record.Job.Revision + 1}
	if terminal {
		completedAt := now
		after.Job.CompletedAt = &completedAt
		result.CompletedAt = &completedAt
		result.CapacityReleased = true
		if mut.CancelReason != nil {
			after.Job.CancelReason = *mut.CancelReason
		}
		if mut.CancelOutcome != nil {
			after.Job.CancelOutcome = *mut.CancelOutcome
		}
	}
	after.Job.LastAccess = now
	after.Job.Revision = result.NewRevision
	after.UpdatedAt = now

	events, err := s.listJSONQueryJobEventsLocked(jobID, 0, 0)
	if err != nil {
		return jobs.ProfileMutationResult{}, err
	}
	existingKinds := make(map[jobs.WakeKind]bool, len(events))
	var nextSequence int64 = 1
	for _, event := range events {
		if event.WakeKind != "" {
			existingKinds[event.WakeKind] = true
		}
		if event.Sequence >= nextSequence {
			nextSequence = event.Sequence + 1
		}
	}
	var afterEvents []jsonQueryJobEventRecord
	var deleteSequences []int64
	for _, spec := range mut.Wakes {
		if spec.Kind == "" || existingKinds[spec.Kind] {
			continue
		}
		existingKinds[spec.Kind] = true
		wake := jobs.WakeEvent{At: now, Kind: spec.Kind, Detail: spec.Detail}
		event := QueryJobEvent{
			JobID: jobID, Sequence: nextSequence, Type: "wake", WakeKind: spec.Kind,
			Detail: spec.Detail, ObservationKey: wakeObservationKey(wake), CreatedAt: now,
		}
		afterEvents = append(afterEvents, jsonQueryJobEventRecord{
			Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryJobEvent: event,
		})
		deleteSequences = append(deleteSequences, nextSequence)
		result.AppendedWakes = append(result.AppendedWakes, wake)
		after.Job.WakeEvents = append(after.Job.WakeEvents, wake)
		nextSequence++
	}

	transaction := jsonQueryMutationTransactionRecord{
		ID: fmt.Sprintf("%s-profile-%020d", jobID, result.NewRevision), Operation: "profile",
		BeforeJob: record, AfterJob: after, AfterEvents: afterEvents,
		DeleteEventSequences: deleteSequences, PreparedAt: now,
	}
	if terminal {
		beforeCapacity, found, err := s.readJSONCapacityRecordLocked(jobID)
		if err != nil {
			return jobs.ProfileMutationResult{}, err
		}
		if found {
			transaction.BeforeCapacity = &beforeCapacity
		}
		afterCapacity := beforeCapacity
		if !found {
			afterCapacity = jsonQueryCapacityRecord{
				Kind: queryCapacityRecordKind, SchemaVersion: longHorizonSchemaVersion,
				JobID: jobID, ThreadID: record.Job.Owner.ThreadID, ConnectionKey: queryConnectionKey(record.Job),
				ReservedAt: record.Job.SubmittedAt.UTC(),
			}
		}
		releasedAt := now
		afterCapacity.State = "released"
		afterCapacity.ReleasedAt = &releasedAt
		transaction.AfterCapacity = &afterCapacity
	}
	if err := s.executeJSONMutationLocked(transaction); err != nil {
		return jobs.ProfileMutationResult{}, err
	}
	return result, nil
}

func (s *JSONStore) executeJSONMutationLocked(transaction jsonQueryMutationTransactionRecord) error {
	transaction.Kind = queryMutationTransactionKind
	transaction.SchemaVersion = longHorizonSchemaVersion
	transaction.State = "prepared"
	if transaction.PreparedAt.IsZero() {
		transaction.PreparedAt = nowUTC()
	}
	path, err := queryTransactionPath(s.cfgDir, transaction.ID)
	if err != nil {
		return err
	}
	if err := jsondb.WriteJSONFile(path, transaction); err != nil {
		return err
	}
	if err := s.applyJSONMutationAfterLocked(transaction); err != nil {
		if rollbackErr := s.rollbackJSONMutationLocked(transaction, path); rollbackErr != nil {
			return fmt.Errorf("apply JSON %s mutation: %v (rollback pending: %w)", transaction.Operation, err, rollbackErr)
		}
		return err
	}
	transaction.State = "committed"
	if err := jsondb.WriteJSONFile(path, transaction); err != nil {
		if rollbackErr := s.rollbackJSONMutationLocked(transaction, path); rollbackErr != nil {
			return fmt.Errorf("commit JSON %s mutation: %v (rollback pending: %w)", transaction.Operation, err, rollbackErr)
		}
		return err
	}
	// Failure to unlink a committed journal is harmless: startup reapplies
	// the same after-images idempotently and removes it.
	_ = os.Remove(path)
	return nil
}

func (s *JSONStore) applyJSONMutationAfterLocked(transaction jsonQueryMutationTransactionRecord) error {
	current, err := s.readJSONQueryJobLocked(transaction.AfterJob.Job.ID, false)
	if err != nil {
		return err
	}
	if err := s.freezeJSONQueryJobIdentityLocked(current.Job, &transaction.BeforeJob.Job); err != nil {
		return err
	}
	if err := s.freezeJSONQueryJobIdentityLocked(current.Job, &transaction.AfterJob.Job); err != nil {
		return err
	}
	for _, event := range transaction.AfterEvents {
		if err := s.writeJSONQueryJobEventLocked(event.QueryJobEvent); err != nil {
			return err
		}
	}
	if current.Job.Revision < transaction.BeforeJob.Job.Revision {
		return errors.New("query mutation journal is ahead of durable job history")
	}
	if current.Job.Revision < transaction.AfterJob.Job.Revision {
		if err := s.writeJSONQueryJobLocked(transaction.AfterJob); err != nil {
			return err
		}
	}
	// A committed journal can survive unlink failure while newer lifecycle
	// writes continue. Never replay its older after-image over a newer job.
	if transaction.AfterCapacity != nil {
		if err := s.writeJSONCapacityRecordLocked(*transaction.AfterCapacity); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONStore) rollbackJSONMutationLocked(transaction jsonQueryMutationTransactionRecord, path string) error {
	current, err := s.readJSONQueryJobLocked(transaction.BeforeJob.Job.ID, false)
	if err != nil {
		return err
	}
	if err := s.freezeJSONQueryJobIdentityLocked(current.Job, &transaction.BeforeJob.Job); err != nil {
		return err
	}
	for _, event := range transaction.BeforeEvents {
		if err := s.writeJSONQueryJobEventLocked(event.QueryJobEvent); err != nil {
			return err
		}
	}
	for _, sequence := range transaction.DeleteEventSequences {
		eventPath, err := queryJobEventPath(s.cfgDir, transaction.BeforeJob.Job.ID, sequence)
		if err != nil {
			return err
		}
		if err := os.Remove(eventPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := s.writeJSONQueryJobLocked(transaction.BeforeJob); err != nil {
		return err
	}
	if transaction.AfterCapacity != nil {
		if transaction.BeforeCapacity != nil {
			if err := s.writeJSONCapacityRecordLocked(*transaction.BeforeCapacity); err != nil {
				return err
			}
		} else if capacityPath, err := queryCapacityPath(s.cfgDir, transaction.AfterCapacity.JobID); err != nil {
			return err
		} else if err := os.Remove(capacityPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func jsonValuesSemanticallyEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && jsonSemanticallyEqual(leftJSON, rightJSON)
}

func (s *JSONStore) executeJSONRunSuspendLocked(transaction jsonRunSuspendTransactionRecord) error {
	transaction.Kind = runSuspendTransactionRecordKind
	transaction.SchemaVersion = longHorizonSchemaVersion
	if transaction.ID == "" {
		transaction.ID = newStoreID("run_suspend_")
	}
	if transaction.PreparedAt.IsZero() {
		transaction.PreparedAt = nowUTC()
	}
	path, err := queryTransactionPath(s.cfgDir, transaction.ID)
	if err != nil {
		return err
	}
	if err := jsondb.WriteJSONFile(path, transaction); err != nil {
		return err
	}
	if err := s.applyJSONRunSuspendLocked(transaction); err != nil {
		// The prepared intent is deliberately retained. Recovery rolls it
		// forward so a process failure can never strand a running run after
		// its exact continuation checkpoint became durable.
		return err
	}
	_ = os.Remove(path)
	return nil
}

func (s *JSONStore) applyJSONRunSuspendLocked(transaction jsonRunSuspendTransactionRecord) error {
	checkpoint := transaction.Checkpoint
	currentRun, runPath, err := s.readRunWithPath(transaction.AfterRun.ID)
	if err != nil {
		return err
	}
	writeAfterRun := false
	switch {
	case currentRun.Revision < transaction.BeforeRun.Revision:
		return errors.New("run suspend journal is ahead of durable run history")
	case currentRun.Revision == transaction.BeforeRun.Revision:
		if !jsonValuesSemanticallyEqual(currentRun, transaction.BeforeRun) {
			return ErrConflict
		}
		writeAfterRun = true
	case currentRun.Revision == transaction.AfterRun.Revision:
		if !jsonValuesSemanticallyEqual(currentRun, transaction.AfterRun) {
			return ErrConflict
		}
	default:
		// A journal may survive a successful apply/unlink while a later
		// lifecycle transition advances the run. Never overwrite that state.
		if currentRun.LastCheckpointSequence < checkpoint.Sequence {
			return errors.New("run advanced without the suspended checkpoint")
		}
	}

	path, err := continuationCheckpointPath(s.cfgDir, checkpoint.RunID, checkpoint.Sequence)
	if err != nil {
		return err
	}
	var currentCheckpoint jsonContinuationCheckpointRecord
	if err := readJSONLoose(path, &currentCheckpoint); err == nil {
		if currentCheckpoint.Kind != continuationCheckpointRecordKind || currentCheckpoint.SchemaVersion != longHorizonSchemaVersion ||
			!continuationCheckpointsSemanticallyEqual(currentCheckpoint.ContinuationCheckpoint, checkpoint.ContinuationCheckpoint) {
			return ErrConflict
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else if err := jsondb.WriteJSONFile(path, checkpoint); err != nil {
		return err
	}
	if writeAfterRun {
		return jsondb.WriteJSONFile(runPath, transaction.AfterRun)
	}
	return nil
}

func (s *JSONStore) executeJSONDependencyReplacementLocked(transaction jsonRunDependencyReplacementTransactionRecord) error {
	transaction.Kind = runDependencyReplacementRecordKind
	transaction.SchemaVersion = longHorizonSchemaVersion
	if transaction.ID == "" {
		transaction.ID = newStoreID("dependency_replace_")
	}
	if transaction.PreparedAt.IsZero() {
		transaction.PreparedAt = nowUTC()
	}
	path, err := queryTransactionPath(s.cfgDir, transaction.ID)
	if err != nil {
		return err
	}
	if err := jsondb.WriteJSONFile(path, transaction); err != nil {
		return err
	}
	if err := s.applyJSONDependencyReplacementLocked(transaction); err != nil {
		return err
	}
	_ = os.Remove(path)
	return nil
}

func (s *JSONStore) applyJSONDependencyReplacementLocked(transaction jsonRunDependencyReplacementTransactionRecord) error {
	oldPath, err := runQueryDependencyPath(s.cfgDir, transaction.AfterOld.RunID, transaction.AfterOld.JobID)
	if err != nil {
		return err
	}
	var currentOld jsonRunQueryDependencyRecord
	if err := readJSONLoose(oldPath, &currentOld); err != nil {
		return err
	}
	writeAfterOld := false
	switch {
	case jsonValuesSemanticallyEqual(currentOld, transaction.AfterOld):
		// Already applied.
	case jsonValuesSemanticallyEqual(currentOld, transaction.BeforeOld):
		writeAfterOld = true
	default:
		return ErrConflict
	}

	replacementPath, err := runQueryDependencyPath(s.cfgDir, transaction.AfterReplacement.RunID, transaction.AfterReplacement.JobID)
	if err != nil {
		return err
	}
	var currentReplacement jsonRunQueryDependencyRecord
	replacementErr := readJSONLoose(replacementPath, &currentReplacement)
	switch {
	case errors.Is(replacementErr, fs.ErrNotExist) && transaction.BeforeReplacement == nil:
		if err := jsondb.WriteJSONFile(replacementPath, transaction.AfterReplacement); err != nil {
			return err
		}
	case replacementErr != nil:
		return replacementErr
	case jsonValuesSemanticallyEqual(currentReplacement, transaction.AfterReplacement):
		// Already applied.
	case transaction.BeforeReplacement != nil && jsonValuesSemanticallyEqual(currentReplacement, *transaction.BeforeReplacement):
		if err := jsondb.WriteJSONFile(replacementPath, transaction.AfterReplacement); err != nil {
			return err
		}
	default:
		return ErrConflict
	}
	if writeAfterOld {
		return jsondb.WriteJSONFile(oldPath, transaction.AfterOld)
	}
	return nil
}

func (s *JSONStore) ClaimWakeEvents(ctx context.Context, jobID, claimant string) ([]jobs.WakeEvent, int64, error) {
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	default:
	}
	jobID, claimant = strings.TrimSpace(jobID), strings.TrimSpace(claimant)
	if jobID == "" || claimant == "" {
		return nil, 0, errors.New("job id and claimant are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return nil, 0, err
	}
	record, err := s.readJSONQueryJobLocked(jobID, false)
	if err != nil {
		return nil, 0, err
	}
	events, err := s.listJSONQueryJobEventsLocked(jobID, 0, 0)
	if err != nil {
		return nil, 0, err
	}
	now := nowUTC()
	var claimed []jobs.WakeEvent
	var beforeEvents, afterEvents []jsonQueryJobEventRecord
	for _, event := range events {
		if event.WakeKind == "" || event.ClaimedAt != nil {
			continue
		}
		beforeEvents = append(beforeEvents, jsonQueryJobEventRecord{Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryJobEvent: event})
		after := event
		after.ClaimedBy = claimant
		after.ClaimedAt = &now
		afterEvents = append(afterEvents, jsonQueryJobEventRecord{Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryJobEvent: after})
		claimed = append(claimed, jobs.WakeEvent{At: event.CreatedAt, Kind: event.WakeKind, Detail: event.Detail, Claimed: true})
	}
	if len(claimed) == 0 {
		return nil, record.Job.Revision, nil
	}
	afterJob := record
	afterJob.Job.Revision++
	afterJob.Job.LastAccess = now
	afterJob.UpdatedAt = now
	transaction := jsonQueryMutationTransactionRecord{
		ID: fmt.Sprintf("%s-wake-%020d", jobID, afterJob.Job.Revision), Operation: "wake_claim",
		BeforeJob: record, AfterJob: afterJob, BeforeEvents: beforeEvents, AfterEvents: afterEvents, PreparedAt: now,
	}
	if err := s.executeJSONMutationLocked(transaction); err != nil {
		return nil, 0, err
	}
	return claimed, afterJob.Job.Revision, nil
}

func (s *JSONStore) UpdateQueryJobValidation(ctx context.Context, jobID string, state jobs.ValidationState, detail string, expectedRevision int64) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	jobID, detail = strings.TrimSpace(jobID), strings.TrimSpace(detail)
	if jobID == "" || expectedRevision < 0 || !validQueryValidationState(string(state)) {
		return 0, errors.New("query job id, expected revision, and valid validation state are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return 0, err
	}
	record, err := s.readJSONQueryJobLocked(jobID, false)
	if err != nil {
		return 0, err
	}
	if record.Job.Revision != expectedRevision {
		return 0, jobs.ErrRevisionConflict
	}
	afterJob := record
	afterJob.Job.Revision++
	afterJob.Job.ValidationState = state
	afterJob.Job.ValidationDetail = detail
	afterJob.ValidationState = string(state)
	afterJob.ValidationDetail = detail
	afterJob.UpdatedAt = nowUTC()
	events, err := s.listJSONQueryJobEventsLocked(jobID, 0, 0)
	if err != nil {
		return 0, err
	}
	var nextSequence int64 = 1
	for _, event := range events {
		if event.Sequence >= nextSequence {
			nextSequence = event.Sequence + 1
		}
	}
	payload, _ := json.Marshal(map[string]any{"state": state, "detail": detail, "lifecycle_revision": afterJob.Job.Revision})
	event := QueryJobEvent{
		JobID: jobID, Sequence: nextSequence, Type: "validation",
		ObservationKey: validationObservationKey(jobID, string(state), detail, afterJob.Job.Revision),
		Payload:        payload, CreatedAt: nowUTC(),
	}
	transaction := jsonQueryMutationTransactionRecord{
		ID: fmt.Sprintf("%s-validation-%020d", jobID, afterJob.Job.Revision), Operation: "validation",
		BeforeJob: record, AfterJob: afterJob,
		AfterEvents:          []jsonQueryJobEventRecord{{Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryJobEvent: event}},
		DeleteEventSequences: []int64{nextSequence}, PreparedAt: nowUTC(),
	}
	if err := s.executeJSONMutationLocked(transaction); err != nil {
		return 0, err
	}
	return afterJob.Job.Revision, nil
}

func (s *JSONStore) AppendQueryJobEvent(ctx context.Context, event QueryJobEvent) (QueryJobEvent, error) {
	select {
	case <-ctx.Done():
		return QueryJobEvent{}, ctx.Err()
	default:
	}
	event, err := normalizeQueryJobEvent(event)
	if err != nil {
		return QueryJobEvent{}, err
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if _, err := s.readJSONQueryJobLocked(event.JobID, false); err != nil {
		return QueryJobEvent{}, err
	}
	events, err := s.listJSONQueryJobEventsLocked(event.JobID, 0, 0)
	if err != nil {
		return QueryJobEvent{}, err
	}
	var maxSequence int64
	for _, existing := range events {
		if existing.ObservationKey == event.ObservationKey {
			return existing, nil
		}
		if existing.Sequence > maxSequence {
			maxSequence = existing.Sequence
		}
	}
	if event.Sequence <= 0 {
		event.Sequence = maxSequence + 1
	} else {
		for _, existing := range events {
			if existing.Sequence == event.Sequence {
				return QueryJobEvent{}, ErrConflict
			}
		}
	}
	if err := s.writeJSONQueryJobEventLocked(event); err != nil {
		return QueryJobEvent{}, err
	}
	return event, nil
}

func (s *JSONStore) ListQueryJobEvents(ctx context.Context, jobID string, afterSequence int64, limit int) ([]QueryJobEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	return s.listJSONQueryJobEventsLocked(jobID, afterSequence, limit)
}

func (s *JSONStore) readJSONQueryResultChunkLocked(path string) (QueryResultChunk, error) {
	var record jsonQueryResultChunkRecord
	if err := readJSONLoose(path, &record); err != nil {
		return QueryResultChunk{}, err
	}
	if record.Kind != queryResultChunkRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
		return QueryResultChunk{}, errors.New("query result chunk record kind or schema mismatch")
	}
	return record.QueryResultChunk, nil
}

func (s *JSONStore) appendJSONQueryResultChunkLocked(chunk QueryResultChunk) error {
	chunk, err := normalizeQueryResultChunk(chunk)
	if err != nil {
		return err
	}
	path, err := queryResultChunkPath(s.cfgDir, chunk.JobID, chunk.Sequence)
	if err != nil {
		return err
	}
	existing, err := s.readJSONQueryResultChunkLocked(path)
	if err == nil {
		// CreatedAt is record metadata, not chunk identity. Replaying the same
		// bounded page/cursor at a later wall-clock time is idempotent.
		existing.CreatedAt = chunk.CreatedAt
		left, _ := json.Marshal(existing)
		right, _ := json.Marshal(chunk)
		if !jsonSemanticallyEqual(left, right) {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	record := jsonQueryResultChunkRecord{Kind: queryResultChunkRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryResultChunk: chunk}
	return jsondb.WriteJSONFile(path, record)
}

func (s *JSONStore) AppendQueryResultChunk(ctx context.Context, chunk QueryResultChunk) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if _, err := s.readJSONQueryJobLocked(chunk.JobID, false); err != nil {
		return err
	}
	return s.appendJSONQueryResultChunkLocked(chunk)
}

func (s *JSONStore) listJSONQueryResultChunksLocked(jobID string, afterSequence int64, limit int) ([]QueryResultChunk, error) {
	dir, err := jsondb.QueryResultChunksDir(s.cfgDir, jobID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []QueryResultChunk
	for _, entry := range entries {
		sequence, ok := parseLongHorizonSequence(entry.Name())
		if entry.IsDir() || !ok || sequence <= afterSequence {
			continue
		}
		chunk, err := s.readJSONQueryResultChunkLocked(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, chunk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *JSONStore) ListQueryResultChunks(ctx context.Context, jobID string, afterSequence int64, limit int) ([]QueryResultChunk, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	return s.listJSONQueryResultChunksLocked(jobID, afterSequence, limit)
}

func (s *JSONStore) TransitionRunCAS(ctx context.Context, mutation RunStateMutation) (Run, bool, error) {
	select {
	case <-ctx.Done():
		return Run{}, false, ctx.Err()
	default:
	}
	mutation.RunID, mutation.Status = strings.TrimSpace(mutation.RunID), strings.TrimSpace(mutation.Status)
	mutation.ExpectedResumeLeaseHolder = strings.TrimSpace(mutation.ExpectedResumeLeaseHolder)
	if mutation.RunID == "" || mutation.ExpectedRevision < 0 || !validateRunStatus(mutation.Status) {
		return Run{}, false, errors.New("valid run id, revision, and status are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return Run{}, false, err
	}
	record, path, err := s.readRunWithPath(mutation.RunID)
	if err != nil {
		return Run{}, false, err
	}
	leaseMatches := mutation.ExpectedResumeLeaseHolder == "" ||
		(record.ResumeLeaseHolder == mutation.ExpectedResumeLeaseHolder && record.ResumeLeaseExpiresAt != nil && record.ResumeLeaseExpiresAt.After(nowUTC()))
	if record.Revision != mutation.ExpectedRevision || !statusAllowed(record.Status, mutation.ExpectedStatuses) || !leaseMatches {
		return runFromRecord(record), false, nil
	}
	record.Status = mutation.Status
	record.Revision++
	record.UpdatedAt = nowUTC()
	if mutation.ContinuationCount != nil {
		record.ContinuationCount = *mutation.ContinuationCount
	}
	if mutation.MaxAutoContinuations != nil {
		record.MaxAutoContinuations = *mutation.MaxAutoContinuations
	}
	if mutation.LastCheckpointSequence != nil {
		record.LastCheckpointSequence = *mutation.LastCheckpointSequence
	}
	if mutation.ClearWakeAt {
		record.WakeAt = nil
	} else if mutation.WakeAt != nil {
		value := mutation.WakeAt.UTC()
		record.WakeAt = &value
	}
	if mutation.ClearResumeLease {
		record.ResumeLeaseHolder = ""
		record.ResumeLeaseExpiresAt = nil
	}
	if err := jsondb.WriteJSONFile(path, record); err != nil {
		return Run{}, false, err
	}
	return runFromRecord(record), true, nil
}

func (s *JSONStore) ClaimRunWake(ctx context.Context, runID string, expectedRevision int64, claimant string, now time.Time, leaseUntil time.Time) (Run, bool, error) {
	select {
	case <-ctx.Done():
		return Run{}, false, ctx.Err()
	default:
	}
	runID, claimant = strings.TrimSpace(runID), strings.TrimSpace(claimant)
	if runID == "" || claimant == "" || expectedRevision < 0 || leaseUntil.IsZero() {
		return Run{}, false, errors.New("run id, revision, claimant, and lease deadline are required")
	}
	if now.IsZero() {
		now = nowUTC()
	}
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	if !leaseUntil.After(now) {
		return Run{}, false, errors.New("resume lease must expire after now")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return Run{}, false, err
	}
	record, path, err := s.readRunWithPath(runID)
	if err != nil {
		return Run{}, false, err
	}
	eligibleStatus := record.Status == RunStatusResumePending || record.Status == RunStatusWaitingExternal
	initialExecution := record.Status == RunStatusQueued && record.ResumeLeaseHolder == "" && record.ResumeLeaseExpiresAt == nil
	expiredExecution := (record.Status == RunStatusRunning || record.Status == RunStatusResuming) &&
		record.ResumeLeaseExpiresAt != nil && !record.ResumeLeaseExpiresAt.After(now)
	legacyExecution := record.Status == RunStatusRunning && record.ResumeLeaseExpiresAt == nil &&
		!record.UpdatedAt.Add(legacyRunningRecoveryAge).After(now)
	leaseAvailable := record.ResumeLeaseExpiresAt == nil || !record.ResumeLeaseExpiresAt.After(now) || record.ResumeLeaseHolder == claimant
	wakeDue := record.WakeAt != nil && !record.WakeAt.After(now)
	if record.Revision != expectedRevision || !leaseAvailable ||
		(!initialExecution && !expiredExecution && !legacyExecution && (!eligibleStatus || !wakeDue)) {
		return runFromRecord(record), false, nil
	}
	if initialExecution {
		record.Revision++
		record.Status = RunStatusRunning
		record.ResumeLeaseHolder = claimant
		record.ResumeLeaseExpiresAt = &leaseUntil
		record.UpdatedAt = now
		if err := jsondb.WriteJSONFile(path, record); err != nil {
			return Run{}, false, err
		}
		return runFromRecord(record), true, nil
	}
	if expiredExecution || legacyExecution {
		record.Revision++
		record.ResumeLeaseHolder = claimant
		record.ResumeLeaseExpiresAt = &leaseUntil
		record.UpdatedAt = now
		if err := jsondb.WriteJSONFile(path, record); err != nil {
			return Run{}, false, err
		}
		return runFromRecord(record), true, nil
	}
	maxContinuations := record.MaxAutoContinuations
	if maxContinuations <= 0 {
		maxContinuations = 8
	}
	record.Revision++
	record.WakeAt = nil
	record.UpdatedAt = now
	if record.ContinuationCount >= maxContinuations {
		record.Status = RunStatusWaitingUser
		record.ResumeLeaseHolder = ""
		record.ResumeLeaseExpiresAt = nil
		if err := jsondb.WriteJSONFile(path, record); err != nil {
			return Run{}, false, err
		}
		return runFromRecord(record), false, nil
	}
	record.Status = RunStatusResuming
	record.ContinuationCount++
	record.ResumeLeaseHolder = claimant
	record.ResumeLeaseExpiresAt = &leaseUntil
	if err := jsondb.WriteJSONFile(path, record); err != nil {
		return Run{}, false, err
	}
	return runFromRecord(record), true, nil
}

func (s *JSONStore) UpdateClaimedRunMetadata(ctx context.Context, runID, claimant string, fields map[string]any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	runID, claimant = strings.TrimSpace(runID), strings.TrimSpace(claimant)
	if runID == "" || claimant == "" {
		return errors.New("run id and claimant are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	record, path, err := s.readRunWithPath(runID)
	if err != nil {
		return err
	}
	now := nowUTC()
	if (record.Status != RunStatusRunning && record.Status != RunStatusResuming) ||
		record.ResumeLeaseHolder != claimant || record.ResumeLeaseExpiresAt == nil || !record.ResumeLeaseExpiresAt.After(now) {
		return ErrConflict
	}
	for key, value := range fields {
		switch key {
		case "started_at":
			if parsed, ok := anyTime(value); ok {
				parsed = parsed.UTC()
				record.StartedAt = &parsed
			}
		case "context_tokens_before":
			if parsed, ok := anyInt(value); ok {
				record.ContextTokensBefore = &parsed
			}
		case "context_tokens_after":
			if parsed, ok := anyInt(value); ok {
				record.ContextTokensAfter = &parsed
			}
		case "context_summary_buckets":
			if parsed, ok := anyInt(value); ok {
				record.ContextSummaryBuckets = &parsed
			}
		case "context_summary_rounds":
			if parsed, ok := anyInt(value); ok {
				record.ContextSummaryRounds = &parsed
			}
		case "context_artifact_refs_loaded":
			if parsed, ok := anyInt(value); ok {
				record.ContextArtifactRefsLoaded = &parsed
			}
		case "context_need_retrieval":
			if parsed, ok := anyBool(value); ok {
				record.ContextNeedRetrieval = &parsed
			}
		case "context_need_retrieval_tooling":
			if parsed, ok := anyBool(value); ok {
				record.ContextNeedTooling = &parsed
			}
		case "context_hard_capped":
			if parsed, ok := anyBool(value); ok {
				record.ContextHardCapped = &parsed
			}
		case "context_overflow_compacted":
			if parsed, ok := anyBool(value); ok {
				record.ContextOverflowCompacted = &parsed
			}
		case "context_plan":
			if raw, ok := value.([]byte); ok {
				record.ContextPlanJSON = cloneRawJSON(raw)
			} else if raw, ok := value.(json.RawMessage); ok {
				record.ContextPlanJSON = cloneRawJSON(raw)
			} else if encoded, marshalErr := json.Marshal(value); marshalErr == nil {
				record.ContextPlanJSON = encoded
			}
		}
	}
	record.Revision++
	record.UpdatedAt = now
	if err := validateRunRecord(record); err != nil {
		return err
	}
	return jsondb.WriteJSONFile(path, record)
}

func (s *JSONStore) RenewRunWakeLease(ctx context.Context, runID, claimant string, ttl time.Duration) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	runID, claimant = strings.TrimSpace(runID), strings.TrimSpace(claimant)
	if runID == "" || claimant == "" || ttl <= 0 {
		return false, errors.New("run id, claimant, and positive ttl are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return false, err
	}
	record, path, err := s.readRunWithPath(runID)
	if err != nil {
		return false, err
	}
	now := nowUTC()
	if (record.Status != RunStatusRunning && record.Status != RunStatusResuming) || record.ResumeLeaseHolder != claimant ||
		record.ResumeLeaseExpiresAt == nil || !record.ResumeLeaseExpiresAt.After(now) {
		return false, nil
	}
	expiresAt := now.Add(ttl)
	record.ResumeLeaseExpiresAt = &expiresAt
	record.UpdatedAt = now
	return true, jsondb.WriteJSONFile(path, record)
}

func (s *JSONStore) ReleaseRunWake(ctx context.Context, runID string, claimant string, nextStatus string, wakeAt *time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	runID, claimant, nextStatus = strings.TrimSpace(runID), strings.TrimSpace(claimant), strings.TrimSpace(nextStatus)
	if runID == "" || claimant == "" || !validateRunStatus(nextStatus) {
		return errors.New("run id, claimant, and valid next status are required")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return err
	}
	record, path, err := s.readRunWithPath(runID)
	if err != nil {
		return err
	}
	now := nowUTC()
	if (record.Status != RunStatusRunning && record.Status != RunStatusResuming) || record.ResumeLeaseHolder != claimant ||
		record.ResumeLeaseExpiresAt == nil || !record.ResumeLeaseExpiresAt.After(now) {
		return ErrConflict
	}
	record.Status = nextStatus
	record.ResumeLeaseHolder = ""
	record.ResumeLeaseExpiresAt = nil
	record.WakeAt = nil
	if wakeAt != nil {
		value := wakeAt.UTC()
		record.WakeAt = &value
	}
	record.Revision++
	record.UpdatedAt = now
	return jsondb.WriteJSONFile(path, record)
}

func (s *JSONStore) ListRecoverableRuns(ctx context.Context, now time.Time, limit int) ([]Run, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if now.IsZero() {
		now = nowUTC()
	}
	if limit <= 0 {
		limit = 100
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return nil, err
	}
	threadEntries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, threadEntry := range threadEntries {
		if !threadEntry.IsDir() {
			continue
		}
		runDir, err := jsondb.RunsDir(s.cfgDir, threadEntry.Name())
		if err != nil {
			return nil, err
		}
		runEntries, err := os.ReadDir(runDir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for _, runEntry := range runEntries {
			if runEntry.IsDir() || !strings.HasSuffix(runEntry.Name(), ".json") {
				continue
			}
			var record jsondb.Run
			if err := readJSONLoose(filepath.Join(runDir, runEntry.Name()), &record); err != nil {
				return nil, err
			}
			wakeDue := (record.Status == RunStatusWaitingExternal || record.Status == RunStatusResumePending) &&
				record.WakeAt != nil && !record.WakeAt.After(now)
			leaseExpired := (record.Status == RunStatusRunning || record.Status == RunStatusResuming) &&
				record.ResumeLeaseExpiresAt != nil && !record.ResumeLeaseExpiresAt.After(now)
			legacyRunning := record.Status == RunStatusRunning && record.ResumeLeaseExpiresAt == nil &&
				!record.UpdatedAt.Add(legacyRunningRecoveryAge).After(now)
			staleQueued := record.Status == RunStatusQueued && !record.UpdatedAt.Add(queuedRunRecoveryAge).After(now)
			if wakeDue || leaseExpired || legacyRunning || staleQueued {
				out = append(out, runFromRecord(record))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i].WakeAt, out[j].WakeAt
		if left == nil {
			left = out[i].ResumeLeaseExpiresAt
		}
		if right == nil {
			right = out[j].ResumeLeaseExpiresAt
		}
		if left == nil || right == nil || left.Equal(*right) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return left.Before(*right)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *JSONStore) findJSONContinuationCheckpointByIDLocked(id string) (*ContinuationCheckpoint, error) {
	id = strings.TrimSpace(id)
	root := filepath.Join(jsondb.RootDir(s.cfgDir), "run_continuation_checkpoints")
	runs, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var found *ContinuationCheckpoint
	for _, runEntry := range runs {
		if !runEntry.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, runEntry.Name()))
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			var record jsonContinuationCheckpointRecord
			if err := readJSONLoose(filepath.Join(root, runEntry.Name(), entry.Name()), &record); err != nil {
				return nil, err
			}
			if record.Kind != continuationCheckpointRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
				return nil, errors.New("continuation checkpoint record kind or schema mismatch")
			}
			if record.ID != id {
				continue
			}
			if found != nil {
				return nil, ErrConflict
			}
			checkpoint := cloneContinuationCheckpoint(record.ContinuationCheckpoint)
			found = &checkpoint
		}
	}
	return found, nil
}

func (s *JSONStore) SuspendRun(ctx context.Context, mutation SuspendRunMutation) (ContinuationCheckpoint, Run, bool, error) {
	select {
	case <-ctx.Done():
		return ContinuationCheckpoint{}, Run{}, false, ctx.Err()
	default:
	}
	mutation, err := normalizeSuspendRunMutation(mutation)
	if err != nil {
		return ContinuationCheckpoint{}, Run{}, false, err
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return ContinuationCheckpoint{}, Run{}, false, err
	}
	current, _, err := s.readRunWithPath(mutation.Checkpoint.RunID)
	if err != nil {
		return ContinuationCheckpoint{}, Run{}, false, err
	}
	currentRun := runFromRecord(current)
	existing, err := s.findJSONContinuationCheckpointByIDLocked(mutation.Checkpoint.ID)
	if err != nil {
		return ContinuationCheckpoint{}, currentRun, false, err
	}
	if existing != nil {
		proposed := cloneContinuationCheckpoint(mutation.Checkpoint)
		proposed.Sequence = existing.Sequence
		if !continuationCheckpointsSemanticallyEqual(*existing, proposed) || !runMatchesSuspension(currentRun, mutation, *existing) {
			return ContinuationCheckpoint{}, currentRun, false, ErrConflict
		}
		return *existing, currentRun, false, nil
	}
	if current.ThreadID != mutation.Checkpoint.ThreadID {
		return ContinuationCheckpoint{}, currentRun, false, errors.New("checkpoint thread does not match run")
	}
	if current.Revision != mutation.ExpectedRevision || !statusAllowed(current.Status, mutation.ExpectedStatuses) {
		return ContinuationCheckpoint{}, currentRun, false, nil
	}
	now := nowUTC()
	requireLease := mutation.ExpectedResumeLeaseHolder != "" || current.Status == RunStatusResuming
	if requireLease && (mutation.ExpectedResumeLeaseHolder == "" || current.ResumeLeaseHolder != mutation.ExpectedResumeLeaseHolder ||
		current.ResumeLeaseExpiresAt == nil || !current.ResumeLeaseExpiresAt.After(now)) {
		return ContinuationCheckpoint{}, currentRun, false, nil
	}

	dir, err := jsondb.RunContinuationCheckpointsDir(s.cfgDir, mutation.Checkpoint.RunID)
	if err != nil {
		return ContinuationCheckpoint{}, currentRun, false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ContinuationCheckpoint{}, currentRun, false, err
	}
	checkpoint := cloneContinuationCheckpoint(mutation.Checkpoint)
	var maxSequence int64
	for _, entry := range entries {
		if sequence, ok := parseLongHorizonSequence(entry.Name()); ok && sequence > maxSequence {
			maxSequence = sequence
		}
	}
	if checkpoint.Sequence <= 0 {
		checkpoint.Sequence = maxSequence + 1
	}
	checkpointPath, err := continuationCheckpointPath(s.cfgDir, checkpoint.RunID, checkpoint.Sequence)
	if err != nil {
		return ContinuationCheckpoint{}, currentRun, false, err
	}
	if _, err := os.Stat(checkpointPath); err == nil {
		return ContinuationCheckpoint{}, currentRun, false, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ContinuationCheckpoint{}, currentRun, false, err
	}

	after := current
	after.Status = RunStatusWaitingExternal
	after.Revision++
	after.ContinuationCount = mutation.ContinuationCount
	after.MaxAutoContinuations = mutation.MaxAutoContinuations
	after.LastCheckpointSequence = checkpoint.Sequence
	wakeAt := mutation.WakeAt
	after.WakeAt = &wakeAt
	after.ResumeLeaseHolder = ""
	after.ResumeLeaseExpiresAt = nil
	after.UpdatedAt = now
	transaction := jsonRunSuspendTransactionRecord{
		ID:        newStoreID("run_suspend_"),
		BeforeRun: current,
		AfterRun:  after,
		Checkpoint: jsonContinuationCheckpointRecord{
			Kind: continuationCheckpointRecordKind, SchemaVersion: longHorizonSchemaVersion,
			ContinuationCheckpoint: checkpoint,
		},
		PreparedAt: now,
	}
	if err := s.executeJSONRunSuspendLocked(transaction); err != nil {
		return ContinuationCheckpoint{}, currentRun, false, err
	}
	return checkpoint, runFromRecord(after), true, nil
}

func (s *JSONStore) AppendContinuationCheckpoint(ctx context.Context, checkpoint ContinuationCheckpoint) (ContinuationCheckpoint, error) {
	select {
	case <-ctx.Done():
		return ContinuationCheckpoint{}, ctx.Err()
	default:
	}
	checkpoint, err := normalizeContinuationCheckpoint(checkpoint)
	if err != nil {
		return ContinuationCheckpoint{}, err
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return ContinuationCheckpoint{}, err
	}
	run, runPath, err := s.readRunWithPath(checkpoint.RunID)
	if err != nil {
		return ContinuationCheckpoint{}, err
	}
	if run.ThreadID != checkpoint.ThreadID {
		return ContinuationCheckpoint{}, errors.New("checkpoint thread does not match run")
	}
	dir, err := jsondb.RunContinuationCheckpointsDir(s.cfgDir, checkpoint.RunID)
	if err != nil {
		return ContinuationCheckpoint{}, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ContinuationCheckpoint{}, err
	}
	var maxSequence int64
	for _, entry := range entries {
		sequence, ok := parseLongHorizonSequence(entry.Name())
		if ok && sequence > maxSequence {
			maxSequence = sequence
		}
	}
	if checkpoint.Sequence <= 0 {
		checkpoint.Sequence = maxSequence + 1
	}
	path, err := continuationCheckpointPath(s.cfgDir, checkpoint.RunID, checkpoint.Sequence)
	if err != nil {
		return ContinuationCheckpoint{}, err
	}
	if _, err := os.Stat(path); err == nil {
		return ContinuationCheckpoint{}, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ContinuationCheckpoint{}, err
	}
	record := jsonContinuationCheckpointRecord{
		Kind: continuationCheckpointRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ContinuationCheckpoint: cloneContinuationCheckpoint(checkpoint),
	}
	if err := jsondb.WriteJSONFile(path, record); err != nil {
		return ContinuationCheckpoint{}, err
	}
	if checkpoint.Sequence > run.LastCheckpointSequence {
		run.LastCheckpointSequence = checkpoint.Sequence
	}
	run.Revision++
	run.UpdatedAt = nowUTC()
	if err := jsondb.WriteJSONFile(runPath, run); err != nil {
		_ = os.Remove(path)
		return ContinuationCheckpoint{}, err
	}
	return cloneContinuationCheckpoint(checkpoint), nil
}

func (s *JSONStore) LatestContinuationCheckpoint(ctx context.Context, runID string) (*ContinuationCheckpoint, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return nil, err
	}
	dir, err := jsondb.RunContinuationCheckpointsDir(s.cfgDir, runID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var latestPath string
	var latestSequence int64
	for _, entry := range entries {
		sequence, ok := parseLongHorizonSequence(entry.Name())
		if ok && sequence > latestSequence {
			latestSequence = sequence
			latestPath = filepath.Join(dir, entry.Name())
		}
	}
	if latestPath == "" {
		return nil, nil
	}
	var record jsonContinuationCheckpointRecord
	if err := readJSONLoose(latestPath, &record); err != nil {
		return nil, err
	}
	if record.Kind != continuationCheckpointRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
		return nil, errors.New("continuation checkpoint record kind or schema mismatch")
	}
	checkpoint := cloneContinuationCheckpoint(record.ContinuationCheckpoint)
	return &checkpoint, nil
}

func (s *JSONStore) UpsertRunQueryDependency(ctx context.Context, dependency RunQueryDependency) (RunQueryDependency, error) {
	select {
	case <-ctx.Done():
		return RunQueryDependency{}, ctx.Err()
	default:
	}
	dependency, err := normalizeRunQueryDependency(dependency)
	if err != nil {
		return RunQueryDependency{}, err
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return RunQueryDependency{}, err
	}
	if _, _, err := s.readRunWithPath(dependency.RunID); err != nil {
		return RunQueryDependency{}, err
	}
	if _, err := s.readJSONQueryJobLocked(dependency.JobID, false); err != nil {
		return RunQueryDependency{}, err
	}
	path, err := runQueryDependencyPath(s.cfgDir, dependency.RunID, dependency.JobID)
	if err != nil {
		return RunQueryDependency{}, err
	}
	var existing jsonRunQueryDependencyRecord
	if err := readJSONRunQueryDependency(path, &existing); err == nil {
		if existing.Kind != runQueryDependencyRecordKind || existing.SchemaVersion != longHorizonSchemaVersion ||
			existing.RunID != dependency.RunID || existing.JobID != dependency.JobID {
			return RunQueryDependency{}, ErrConflict
		}
		if existing.ObligationID != "" && dependency.ObligationID != "" && existing.ObligationID != dependency.ObligationID {
			return RunQueryDependency{}, ErrConflict
		}
		// A generic replay may enrich validation and fill a legacy obligation,
		// but it can never reactivate or rewrite the lineage of a detached row.
		if existing.Detached {
			return existing.RunQueryDependency, nil
		}
		dependency.CreatedAt = existing.CreatedAt
		dependency.RequiredForCompletion = existing.RequiredForCompletion || dependency.RequiredForCompletion
		dependency.Detached = false
		dependency.ReplacementJobID = existing.ReplacementJobID
		if existing.ObligationID != "" {
			dependency.ObligationID = existing.ObligationID
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RunQueryDependency{}, err
	}
	if dependency.ObligationID != "" && !dependency.Detached {
		dependencies, listErr := s.listJSONRunQueryDependenciesLocked(dependency.RunID)
		if listErr != nil {
			return RunQueryDependency{}, listErr
		}
		for _, candidate := range dependencies {
			if candidate.JobID != dependency.JobID && !candidate.Detached && candidate.RequiredForCompletion &&
				candidate.ObligationID == dependency.ObligationID {
				return RunQueryDependency{}, ErrConflict
			}
		}
	}
	record := jsonRunQueryDependencyRecord{
		Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
		RunQueryDependency: dependency,
	}
	if err := jsondb.WriteJSONFile(path, record); err != nil {
		return RunQueryDependency{}, err
	}
	return dependency, nil
}

func (s *JSONStore) BindQueryObligation(ctx context.Context, jobID string) error {
	return bindQueryObligation(ctx, s, jobID)
}

func (s *JSONStore) ReplaceRunQueryDependency(ctx context.Context, runID, replacedJobID string, replacement RunQueryDependency) (RunQueryDependency, error) {
	select {
	case <-ctx.Done():
		return RunQueryDependency{}, ctx.Err()
	default:
	}
	runID, replacedJobID = strings.TrimSpace(runID), strings.TrimSpace(replacedJobID)
	replacement, err := normalizeRunQueryDependency(replacement)
	if err != nil {
		return RunQueryDependency{}, err
	}
	if runID == "" || replacedJobID == "" || replacement.RunID != runID || replacement.JobID == replacedJobID {
		return RunQueryDependency{}, errors.New("replacement requires one run and distinct old and new query jobs")
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return RunQueryDependency{}, err
	}
	run, _, err := s.readRunWithPath(runID)
	if err != nil {
		return RunQueryDependency{}, err
	}
	oldPath, err := runQueryDependencyPath(s.cfgDir, runID, replacedJobID)
	if err != nil {
		return RunQueryDependency{}, err
	}
	var beforeOld jsonRunQueryDependencyRecord
	if err := readJSONRunQueryDependency(oldPath, &beforeOld); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return RunQueryDependency{}, ErrNotFound
		}
		return RunQueryDependency{}, err
	}
	if beforeOld.Kind != runQueryDependencyRecordKind || beforeOld.SchemaVersion != longHorizonSchemaVersion ||
		beforeOld.RunID != runID || beforeOld.JobID != replacedJobID {
		return RunQueryDependency{}, ErrConflict
	}
	replacementPath, err := runQueryDependencyPath(s.cfgDir, runID, replacement.JobID)
	if err != nil {
		return RunQueryDependency{}, err
	}
	var beforeReplacement *jsonRunQueryDependencyRecord
	var existingReplacement jsonRunQueryDependencyRecord
	if err := readJSONRunQueryDependency(replacementPath, &existingReplacement); err == nil {
		if existingReplacement.Kind != runQueryDependencyRecordKind || existingReplacement.SchemaVersion != longHorizonSchemaVersion ||
			existingReplacement.RunID != runID || existingReplacement.JobID != replacement.JobID {
			return RunQueryDependency{}, ErrConflict
		}
		copyRecord := existingReplacement
		beforeReplacement = &copyRecord
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RunQueryDependency{}, err
	}
	oldJob, err := s.readJSONQueryJobLocked(replacedJobID, false)
	if err != nil {
		return RunQueryDependency{}, err
	}
	replacementJob, err := s.readJSONQueryJobLocked(replacement.JobID, false)
	if err != nil {
		return RunQueryDependency{}, err
	}
	// Either job may be owned by an earlier run in the same thread. The locked,
	// active dependency row in this run is the replacement capability; exact
	// durable lineage and same-thread ownership fence the successor without
	// rewriting its immutable owner run.
	if oldJob.Job.Owner.ThreadID != run.ThreadID || replacementJob.Job.Owner.ThreadID != run.ThreadID {
		return RunQueryDependency{}, ErrConflict
	}
	if strings.TrimSpace(replacementJob.Job.ReplacesJobID) != replacedJobID {
		return RunQueryDependency{}, fmt.Errorf("replacement job %s does not durably replace %s: %w", replacement.JobID, replacedJobID, ErrConflict)
	}

	// Reconcile all three possible identity sources. This runs even on an
	// idempotent replay so mixed-version/legacy NULL rows are hydrated instead
	// of permanently bypassing the one-active-attempt invariant.
	obligationID := ""
	acceptObligationID := func(candidate string) error {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return nil
		}
		if err := ValidateQueryObligationID(candidate); err != nil {
			return fmt.Errorf("stored query obligation is invalid: %w", ErrConflict)
		}
		if obligationID != "" && obligationID != candidate {
			return ErrConflict
		}
		obligationID = candidate
		return nil
	}
	if err := acceptObligationID(replacement.ObligationID); err != nil {
		return RunQueryDependency{}, err
	}
	if err := acceptObligationID(beforeOld.ObligationID); err != nil {
		return RunQueryDependency{}, err
	}
	if beforeReplacement != nil {
		if err := acceptObligationID(beforeReplacement.ObligationID); err != nil {
			return RunQueryDependency{}, err
		}
	}
	if obligationID == "" {
		root := replacedJobID
		ancestor := strings.TrimSpace(oldJob.Job.ReplacesJobID)
		seen := map[string]bool{root: true}
		for hops := 0; ancestor != ""; hops++ {
			if hops >= 64 || seen[ancestor] {
				return RunQueryDependency{}, fmt.Errorf("query replacement lineage is cyclic or exceeds 64 attempts: %w", ErrConflict)
			}
			seen[ancestor] = true
			root = ancestor
			ancestorRecord, ancestorErr := s.readJSONQueryJobLocked(ancestor, false)
			if errors.Is(ancestorErr, ErrNotFound) {
				return RunQueryDependency{}, fmt.Errorf("query replacement ancestor %s is missing: %w", ancestor, ErrConflict)
			}
			if ancestorErr != nil {
				return RunQueryDependency{}, ancestorErr
			}
			ancestor = strings.TrimSpace(ancestorRecord.Job.ReplacesJobID)
		}
		obligationID, err = QueryObligationIDForRoot(runID, root)
		if err != nil {
			return RunQueryDependency{}, err
		}
	}
	dependencies, listErr := s.listJSONRunQueryDependenciesLocked(runID)
	if listErr != nil {
		return RunQueryDependency{}, listErr
	}
	for _, candidate := range dependencies {
		if candidate.JobID == replacedJobID || candidate.JobID == replacement.JobID {
			continue
		}
		if candidate.RequiredForCompletion && !candidate.Detached && candidate.ObligationID == obligationID {
			return RunQueryDependency{}, fmt.Errorf("query obligation %s already has active attempt %s: %w", obligationID, candidate.JobID, ErrConflict)
		}
	}
	now := nowUTC()
	if beforeOld.Detached {
		if beforeOld.ReplacementJobID != replacement.JobID || beforeReplacement == nil ||
			!beforeReplacement.RequiredForCompletion || beforeReplacement.Detached {
			return RunQueryDependency{}, ErrConflict
		}
		if beforeOld.ObligationID == obligationID && beforeReplacement.ObligationID == obligationID {
			return beforeReplacement.RunQueryDependency, nil
		}
		afterOld := beforeOld.RunQueryDependency
		afterOld.ObligationID = obligationID
		afterOld.UpdatedAt = now
		afterReplacement := beforeReplacement.RunQueryDependency
		afterReplacement.ObligationID = obligationID
		afterReplacement.UpdatedAt = now
		transaction := jsonRunDependencyReplacementTransactionRecord{
			ID:                newStoreID("dependency_replace_"),
			BeforeOld:         beforeOld,
			BeforeReplacement: beforeReplacement,
			AfterOld: jsonRunQueryDependencyRecord{
				Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
				RunQueryDependency: afterOld,
			},
			AfterReplacement: jsonRunQueryDependencyRecord{
				Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
				RunQueryDependency: afterReplacement,
			},
			PreparedAt: now,
		}
		if err := s.executeJSONDependencyReplacementLocked(transaction); err != nil {
			return RunQueryDependency{}, err
		}
		return afterReplacement, nil
	}
	if !beforeOld.RequiredForCompletion || beforeOld.ReplacementJobID != "" {
		return RunQueryDependency{}, ErrConflict
	}
	if !dependencyReplacementEligible(beforeOld.RunQueryDependency, oldJob.Job) {
		return RunQueryDependency{}, ErrConflict
	}
	if beforeReplacement != nil && (beforeReplacement.Detached || beforeReplacement.ReplacementJobID != "") {
		return RunQueryDependency{}, ErrConflict
	}

	afterReplacement := replacement
	afterReplacement.RequiredForCompletion = true
	afterReplacement.Detached = false
	afterReplacement.ObligationID = obligationID
	afterReplacement.ReplacementJobID = ""
	afterReplacement.UpdatedAt = now
	if beforeReplacement != nil {
		afterReplacement.CreatedAt = beforeReplacement.CreatedAt
	}
	afterOld := beforeOld.RunQueryDependency
	afterOld.Detached = true
	afterOld.ObligationID = obligationID
	afterOld.ReplacementJobID = replacement.JobID
	afterOld.UpdatedAt = now
	transaction := jsonRunDependencyReplacementTransactionRecord{
		ID:                newStoreID("dependency_replace_"),
		BeforeOld:         beforeOld,
		BeforeReplacement: beforeReplacement,
		AfterOld: jsonRunQueryDependencyRecord{
			Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
			RunQueryDependency: afterOld,
		},
		AfterReplacement: jsonRunQueryDependencyRecord{
			Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
			RunQueryDependency: afterReplacement,
		},
		PreparedAt: now,
	}
	if err := s.executeJSONDependencyReplacementLocked(transaction); err != nil {
		return RunQueryDependency{}, err
	}
	return afterReplacement, nil
}

func (s *JSONStore) ListRunQueryDependencies(ctx context.Context, runID string) ([]RunQueryDependency, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return nil, err
	}
	return s.listJSONRunQueryDependenciesLocked(runID)
}

func (s *JSONStore) listJSONRunQueryDependenciesLocked(runID string) ([]RunQueryDependency, error) {
	runID = strings.TrimSpace(runID)
	dir, err := jsondb.RunQueryDependenciesDir(s.cfgDir, runID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []RunQueryDependency
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var record jsonRunQueryDependencyRecord
		if err := readJSONRunQueryDependency(filepath.Join(dir, entry.Name()), &record); err != nil {
			return nil, err
		}
		expectedJobID := strings.TrimSuffix(entry.Name(), ".json")
		if expectedJobID == "" || record.RunID != runID || record.JobID != expectedJobID {
			return nil, fmt.Errorf("run query dependency record identity does not match its directory and filename: %w", ErrConflict)
		}
		if record.ObligationID != "" &&
			(record.ObligationID != strings.TrimSpace(record.ObligationID) || ValidateQueryObligationID(record.ObligationID) != nil) {
			return nil, fmt.Errorf("run query dependency record has an invalid query_obligation_id: %w", ErrConflict)
		}
		out = append(out, record.RunQueryDependency)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].JobID < out[j].JobID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func readJSONRunQueryDependency(path string, record *jsonRunQueryDependencyRecord) error {
	if record == nil {
		return errors.New("nil run query dependency record")
	}
	if err := readJSONLoose(path, record); err != nil {
		return err
	}
	if record.Kind != runQueryDependencyRecordKind || record.SchemaVersion != longHorizonSchemaVersion {
		return errors.New("run query dependency record kind or schema mismatch")
	}
	originalReplacement := record.ReplacementJobID
	if err := sanitizeLegacyRunQueryDependencyIdentity(&record.RunQueryDependency); err != nil {
		return err
	}
	if originalReplacement != record.ReplacementJobID {
		// Active legacy rows used sentinels to spell "no successor". Heal the
		// durable value while holding the JSON store lock so later replacement
		// CAS/journal comparisons operate on the same canonical identity.
		return jsondb.WriteJSONFile(path, record)
	}
	return nil
}

func (s *JSONStore) DetachRunQueryDependencies(ctx context.Context, runID string, now time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if now.IsZero() {
		now = nowUTC()
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return err
	}
	dependencies, err := s.listJSONRunQueryDependenciesLocked(runID)
	if err != nil {
		return err
	}
	for _, dependency := range dependencies {
		dependency.Detached = true
		dependency.UpdatedAt = now.UTC()
		path, err := runQueryDependencyPath(s.cfgDir, dependency.RunID, dependency.JobID)
		if err != nil {
			return err
		}
		record := jsonRunQueryDependencyRecord{
			Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
			RunQueryDependency: dependency,
		}
		if err := jsondb.WriteJSONFile(path, record); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONStore) appendJSONQueryJobEventLocked(event QueryJobEvent) (QueryJobEvent, error) {
	event, err := normalizeQueryJobEvent(event)
	if err != nil {
		return QueryJobEvent{}, err
	}
	events, err := s.listJSONQueryJobEventsLocked(event.JobID, 0, 0)
	if err != nil {
		return QueryJobEvent{}, err
	}
	var maxSequence int64
	for _, existing := range events {
		if existing.ObservationKey == event.ObservationKey {
			return existing, nil
		}
		if existing.Sequence > maxSequence {
			maxSequence = existing.Sequence
		}
	}
	if event.Sequence <= 0 {
		event.Sequence = maxSequence + 1
	}
	for _, existing := range events {
		if existing.Sequence == event.Sequence {
			return QueryJobEvent{}, ErrConflict
		}
	}
	if err := s.writeJSONQueryJobEventLocked(event); err != nil {
		return QueryJobEvent{}, err
	}
	return event, nil
}

func drainTransactionID(jobID string, sequence int64) string {
	return fmt.Sprintf("%s-drain-%020d", strings.TrimSpace(jobID), sequence)
}

func (s *JSONStore) CommitDrainObservation(ctx context.Context, commit jobs.DrainCommit) (int64, error) {
	return s.commitDrainObservation(ctx, commit, "")
}

func (s *JSONStore) CommitDrainObservationFenced(ctx context.Context, commit jobs.DrainCommit, holder string) (int64, error) {
	holder = strings.TrimSpace(holder)
	if holder == "" {
		return 0, errors.New("monitor lease holder is required")
	}
	return s.commitDrainObservation(ctx, commit, holder)
}

func (s *JSONStore) commitDrainObservation(ctx context.Context, commit jobs.DrainCommit, holder string) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	if strings.TrimSpace(commit.JobID) == "" || commit.Seq <= 0 || commit.Record.ID != commit.JobID || commit.Record.DrainSeq != commit.Seq {
		return 0, errors.New("invalid drain commit identity or sequence")
	}
	if commit.Owner.ThreadID != commit.Record.Owner.ThreadID || commit.Owner.Handle != commit.Record.Owner.Handle {
		return 0, jobs.ErrOwnershipMismatch
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return 0, err
	}

	current, err := s.readJSONQueryJobLocked(commit.JobID, false)
	if err != nil {
		return 0, err
	}
	if current.Job.Owner.ThreadID != commit.Owner.ThreadID || current.Job.Owner.Handle != commit.Owner.Handle {
		return 0, jobs.ErrOwnershipMismatch
	}
	if err := s.freezeJSONQueryJobIdentityLocked(current.Job, &commit.Record); err != nil {
		return 0, err
	}
	if holder != "" && (current.MonitorLease == nil || current.MonitorLease.Holder != holder || !current.MonitorLease.ExpiresAt.After(nowUTC())) {
		return 0, jobs.ErrLeaseLost
	}
	if current.Job.DrainSeq >= commit.Seq {
		path, err := queryResultChunkPath(s.cfgDir, commit.JobID, commit.Seq)
		if err != nil {
			return 0, err
		}
		storedChunk, err := s.readJSONQueryResultChunkLocked(path)
		if err != nil {
			return 0, errors.New("drain sequence advanced without durable result chunk")
		}
		proposedChunk := QueryResultChunk{JobID: commit.JobID, Sequence: commit.Seq, Page: commit.Page,
			CursorAfter: commit.Handle, RowCount: len(commit.Page.Rows), ByteCount: commit.Page.Bytes, Truncated: commit.Page.Truncated}
		if !queryResultChunksSemanticallyEqual(storedChunk, proposedChunk) ||
			(current.Job.DrainSeq == commit.Seq && (!nativeHandlesSemanticallyEqual(current.Job.Handle, commit.Handle) ||
				!drainReplayAccountingMatches(commit, current.Job))) {
			return 0, ErrConflict
		}
		return current.Job.Revision, nil
	}
	if isTerminalQueryExecutionState(current.Job.Status) {
		return 0, jobs.ErrRevisionConflict
	}
	if current.Job.Revision != commit.Record.Revision || current.Job.DrainSeq+1 != commit.Seq {
		return 0, jobs.ErrRevisionConflict
	}
	if err := validateDrainCommitAccounting(commit, current.Job); err != nil {
		return 0, err
	}

	chunk := QueryResultChunk{
		JobID: commit.JobID, Sequence: commit.Seq, Page: commit.Page, CursorAfter: commit.Handle,
		RowCount: len(commit.Page.Rows), ByteCount: commit.Page.Bytes, Truncated: commit.Page.Truncated, CreatedAt: nowUTC(),
	}
	payload, _ := json.Marshal(map[string]any{
		"drain_sequence": commit.Seq,
		"rows":           len(commit.Page.Rows),
		"bytes":          commit.Page.Bytes,
		"truncated":      commit.Page.Truncated,
		"cursor_after":   commit.Handle,
	})
	event := QueryJobEvent{
		JobID: commit.JobID, Type: "drain_observation", ObservationKey: fmt.Sprintf("drain:%d", commit.Seq),
		Payload: payload, CreatedAt: nowUTC(),
	}
	transaction := jsonQueryTransactionRecord{
		Kind: queryTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: drainTransactionID(commit.JobID, commit.Seq), Commit: commit, Chunk: chunk, Event: event, PreparedAt: nowUTC(),
	}
	transactionPath, err := queryTransactionPath(s.cfgDir, transaction.ID)
	if err != nil {
		return 0, err
	}
	// The journal rename is the JSON transaction's durable commit point.
	// Recheck expiry immediately before it while still holding the shared
	// advisory lock; recovery may then safely roll the authorized intent
	// forward even if the originating process stops mid-apply.
	if holder != "" && (current.MonitorLease == nil || current.MonitorLease.Holder != holder || !current.MonitorLease.ExpiresAt.After(nowUTC())) {
		return 0, jobs.ErrLeaseLost
	}
	if err := jsondb.WriteJSONFile(transactionPath, transaction); err != nil {
		return 0, err
	}
	return s.applyJSONDrainTransactionLocked(transaction, transactionPath)
}

func (s *JSONStore) applyJSONDrainTransactionLocked(transaction jsonQueryTransactionRecord, transactionPath string) (int64, error) {
	commit := transaction.Commit
	current, err := s.readJSONQueryJobLocked(commit.JobID, false)
	if err != nil {
		return 0, err
	}
	if err := s.freezeJSONQueryJobIdentityLocked(current.Job, &commit.Record); err != nil {
		return 0, err
	}
	if current.Job.DrainSeq >= commit.Seq {
		chunkPath, pathErr := queryResultChunkPath(s.cfgDir, commit.JobID, commit.Seq)
		if pathErr != nil {
			return 0, pathErr
		}
		storedChunk, readErr := s.readJSONQueryResultChunkLocked(chunkPath)
		if readErr != nil {
			return 0, errors.New("drain journal found advanced cursor without durable chunk")
		}
		if !queryResultChunksSemanticallyEqual(storedChunk, transaction.Chunk) ||
			(current.Job.DrainSeq == commit.Seq && (!nativeHandlesSemanticallyEqual(current.Job.Handle, commit.Handle) ||
				!drainReplayAccountingMatches(commit, current.Job))) {
			return 0, ErrConflict
		}
		if err := os.Remove(transactionPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		return current.Job.Revision, nil
	}
	// Every normal lifecycle mutator recovers the journal before it reads or
	// writes the job, so the ordinary case below still rolls this durable
	// intent forward. This branch is a compatibility escape hatch for state
	// produced before that invariant was enforced: a later lifecycle revision
	// (or a terminal record) makes an entirely unapplied next-page intent
	// obsolete. Only discard it after proving that no chunk or observation was
	// partially applied; partial effects are inconsistent and must stay loud.
	if current.Job.Revision > commit.Record.Revision || isTerminalQueryExecutionState(current.Job.Status) {
		if current.Job.Revision < commit.Record.Revision || current.Job.DrainSeq != commit.Seq-1 ||
			current.Job.Owner.ThreadID != commit.Owner.ThreadID || current.Job.Owner.Handle != commit.Owner.Handle {
			return 0, jobs.ErrRevisionConflict
		}
		if err := validateDrainCommitAccounting(commit, current.Job); err != nil {
			return 0, err
		}
		chunkPath, pathErr := queryResultChunkPath(s.cfgDir, commit.JobID, commit.Seq)
		if pathErr != nil {
			return 0, pathErr
		}
		if _, readErr := s.readJSONQueryResultChunkLocked(chunkPath); readErr == nil {
			return 0, fmt.Errorf("obsolete drain journal has a partially applied result chunk: %w", ErrConflict)
		} else if !errors.Is(readErr, fs.ErrNotExist) {
			return 0, readErr
		}
		events, listErr := s.listJSONQueryJobEventsLocked(commit.JobID, 0, 0)
		if listErr != nil {
			return 0, listErr
		}
		for _, event := range events {
			if event.ObservationKey == transaction.Event.ObservationKey {
				return 0, fmt.Errorf("obsolete drain journal has a partially applied observation: %w", ErrConflict)
			}
		}
		if err := os.Remove(transactionPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return 0, err
		}
		return current.Job.Revision, nil
	}
	if current.Job.Revision != commit.Record.Revision || current.Job.DrainSeq+1 != commit.Seq {
		return 0, jobs.ErrRevisionConflict
	}
	if err := validateDrainCommitAccounting(commit, current.Job); err != nil {
		return 0, err
	}
	if err := s.appendJSONQueryResultChunkLocked(transaction.Chunk); err != nil {
		return 0, err
	}
	if _, err := s.appendJSONQueryJobEventLocked(transaction.Event); err != nil {
		return 0, err
	}
	if err := s.syncJSONWakeEventsLocked(commit.Record); err != nil {
		return 0, err
	}
	commit.Record.Handle = commit.Handle
	commit.Record.DrainSeq = commit.Seq
	commit.Record.Revision = current.Job.Revision + 1
	current.Job = commit.Record
	current.UpdatedAt = nowUTC()
	if err := s.writeJSONQueryJobLocked(current); err != nil {
		return 0, err
	}
	if err := s.syncJSONCapacityLocked(current.Job); err != nil {
		return 0, err
	}
	if err := os.Remove(transactionPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	return current.Job.Revision, nil
}

func (s *JSONStore) recoverQueryTransactions() error {
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	return s.recoverQueryTransactionsLocked()
}

func (s *JSONStore) recoverQueryTransactionsLocked() error {
	liveQuarantined, err := s.repairQuarantinedJSONQueryJobIdentitiesLocked()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(jsondb.QueryTransactionsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(jsondb.QueryTransactionsDir(s.cfgDir), entry.Name())
		var header struct {
			Kind          string `json:"kind"`
			SchemaVersion int    `json:"schema_version"`
		}
		if err := readJSONLoose(path, &header); err != nil {
			return fmt.Errorf("read query transaction %s: %w", entry.Name(), err)
		}
		if header.SchemaVersion != longHorizonSchemaVersion {
			return fmt.Errorf("query transaction %s kind or schema mismatch", entry.Name())
		}
		deferRecovery, err := queryTransactionReferencesLiveQuarantinedJob(path, liveQuarantined)
		if err != nil {
			return fmt.Errorf("inspect query transaction %s: %w", entry.Name(), err)
		}
		if deferRecovery {
			continue
		}
		switch header.Kind {
		case queryTransactionRecordKind:
			var transaction jsonQueryTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return fmt.Errorf("read drain transaction %s: %w", entry.Name(), err)
			}
			if _, err := s.applyJSONDrainTransactionLocked(transaction, path); err != nil {
				return fmt.Errorf("recover drain transaction %s: %w", entry.Name(), err)
			}
		case queryMutationTransactionKind:
			var transaction jsonQueryMutationTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return fmt.Errorf("read mutation transaction %s: %w", entry.Name(), err)
			}
			if transaction.State == "committed" {
				if err := s.applyJSONMutationAfterLocked(transaction); err != nil {
					return fmt.Errorf("complete committed query mutation %s: %w", entry.Name(), err)
				}
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			} else if err := s.rollbackJSONMutationLocked(transaction, path); err != nil {
				return fmt.Errorf("rollback prepared query mutation %s: %w", entry.Name(), err)
			}
		case runSuspendTransactionRecordKind:
			var transaction jsonRunSuspendTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return fmt.Errorf("read run suspend transaction %s: %w", entry.Name(), err)
			}
			if err := s.applyJSONRunSuspendLocked(transaction); err != nil {
				return fmt.Errorf("recover run suspend transaction %s: %w", entry.Name(), err)
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		case runDependencyReplacementRecordKind:
			var transaction jsonRunDependencyReplacementTransactionRecord
			if err := readJSONLoose(path, &transaction); err != nil {
				return fmt.Errorf("read dependency replacement transaction %s: %w", entry.Name(), err)
			}
			if err := s.applyJSONDependencyReplacementLocked(transaction); err != nil {
				return fmt.Errorf("recover dependency replacement transaction %s: %w", entry.Name(), err)
			}
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		default:
			return fmt.Errorf("query transaction %s kind or schema mismatch", entry.Name())
		}
	}
	return nil
}

// reconcileContinuationCheckpoints closes the only multi-file JSON window in
// checkpoint creation: if the process stopped after the immutable checkpoint
// rename but before the run pointer rename, reopening advances the pointer and
// revision before any continuation can be claimed.
func (s *JSONStore) reconcileContinuationCheckpoints() error {
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	// Close the time-of-check gap between startup recovery and pointer
	// reconciliation. If a legacy owner lease expired after the first pass,
	// repair and apply its journal now while holding the same advisory lock.
	if err := s.recoverQueryTransactionsLocked(); err != nil {
		return err
	}
	pendingSuspendRuns, err := s.pendingJSONRunSuspendRunsLocked()
	if err != nil {
		return err
	}
	root := filepath.Join(jsondb.RootDir(s.cfgDir), "run_continuation_checkpoints")
	runEntries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, runEntry := range runEntries {
		if !runEntry.IsDir() {
			continue
		}
		if pendingSuspendRuns[runEntry.Name()] {
			// The checkpoint may be the first half of that journal's atomic
			// suspend. Advancing the run pointer independently would make the
			// deferred after-image permanently conflict when ownership expires.
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, runEntry.Name()))
		if err != nil {
			return err
		}
		var latest int64
		for _, entry := range entries {
			if sequence, ok := parseLongHorizonSequence(entry.Name()); ok && sequence > latest {
				latest = sequence
			}
		}
		if latest == 0 {
			continue
		}
		run, path, err := s.readRunWithPath(runEntry.Name())
		if err != nil {
			return err
		}
		if run.LastCheckpointSequence >= latest {
			continue
		}
		run.LastCheckpointSequence = latest
		run.Revision++
		run.UpdatedAt = nowUTC()
		if err := jsondb.WriteJSONFile(path, run); err != nil {
			return err
		}
	}
	return nil
}
