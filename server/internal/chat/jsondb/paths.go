package jsondb

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const DirName = "chatdb"

func RootDir(cfgDir string) string {
	return filepath.Join(cfgDir, DirName)
}

func MetaPath(cfgDir string) string {
	return filepath.Join(RootDir(cfgDir), "meta.json")
}

func QueryJobsDir(cfgDir string) string {
	return filepath.Join(RootDir(cfgDir), "query_jobs")
}

func QueryJobPath(cfgDir string, jobID string) (string, error) {
	id, err := cleanID(jobID)
	if err != nil {
		return "", fmt.Errorf("job_id: %w", err)
	}
	return filepath.Join(QueryJobsDir(cfgDir), id+".json"), nil
}

func QueryJobEventsDir(cfgDir string, jobID string) (string, error) {
	id, err := cleanID(jobID)
	if err != nil {
		return "", fmt.Errorf("job_id: %w", err)
	}
	return filepath.Join(RootDir(cfgDir), "query_job_events", id), nil
}

func QueryResultChunksDir(cfgDir string, jobID string) (string, error) {
	id, err := cleanID(jobID)
	if err != nil {
		return "", fmt.Errorf("job_id: %w", err)
	}
	return filepath.Join(RootDir(cfgDir), "query_result_chunks", id), nil
}

func RunQueryDependenciesDir(cfgDir string, runID string) (string, error) {
	id, err := cleanID(runID)
	if err != nil {
		return "", fmt.Errorf("run_id: %w", err)
	}
	return filepath.Join(RootDir(cfgDir), "run_query_dependencies", id), nil
}

func RunContinuationCheckpointsDir(cfgDir string, runID string) (string, error) {
	id, err := cleanID(runID)
	if err != nil {
		return "", fmt.Errorf("run_id: %w", err)
	}
	return filepath.Join(RootDir(cfgDir), "run_continuation_checkpoints", id), nil
}

func QueryCapacityReservationsDir(cfgDir string) string {
	return filepath.Join(RootDir(cfgDir), "query_capacity_reservations")
}

func QueryTransactionsDir(cfgDir string) string {
	return filepath.Join(RootDir(cfgDir), "query_transactions")
}

func IndexesDir(cfgDir string) string {
	return filepath.Join(RootDir(cfgDir), "indexes")
}

func ThreadsDir(cfgDir string) string {
	return filepath.Join(RootDir(cfgDir), "threads")
}

func threadDir(cfgDir string, threadID string) (string, error) {
	id, err := cleanID(threadID)
	if err != nil {
		return "", fmt.Errorf("thread_id: %w", err)
	}
	return filepath.Join(ThreadsDir(cfgDir), id), nil
}

func ThreadPath(cfgDir string, threadID string) (string, error) {
	dir, err := threadDir(cfgDir, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "thread.json"), nil
}

func RunsDir(cfgDir string, threadID string) (string, error) {
	dir, err := threadDir(cfgDir, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runs"), nil
}

func RunPath(cfgDir string, threadID string, runID string) (string, error) {
	rid, err := cleanID(runID)
	if err != nil {
		return "", fmt.Errorf("run_id: %w", err)
	}
	dir, err := RunsDir(cfgDir, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, rid+".json"), nil
}

func MessagesDir(cfgDir string, threadID string) (string, error) {
	dir, err := threadDir(cfgDir, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "messages"), nil
}

func MessagePath(cfgDir string, threadID string, seq int64, messageID string) (string, error) {
	mid, err := cleanID(messageID)
	if err != nil {
		return "", fmt.Errorf("message_id: %w", err)
	}
	dir, err := MessagesDir(cfgDir, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%s_%s.json", formatSeq(seq), mid)), nil
}

func ArtifactsDir(cfgDir string, threadID string) (string, error) {
	dir, err := threadDir(cfgDir, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "artifacts"), nil
}

func ArtifactPath(cfgDir string, threadID string, artifactID string) (string, error) {
	aid, err := cleanID(artifactID)
	if err != nil {
		return "", fmt.Errorf("artifact_id: %w", err)
	}
	dir, err := ArtifactsDir(cfgDir, threadID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, aid+".json"), nil
}

func cleanID(id string) (string, error) {
	v := strings.TrimSpace(id)
	if v == "" {
		return "", errors.New("empty")
	}
	if strings.Contains(v, "/") || strings.Contains(v, "\\") {
		return "", errors.New("contains path separator")
	}
	if v == "." || v == ".." {
		return "", errors.New("invalid")
	}
	return v, nil
}

func formatSeq(seq int64) string {
	// 20 digits safely lexicographically sorts int64 sequence numbers.
	// int64 max is 19 digits (9,223,372,036,854,775,807).
	return fmt.Sprintf("%020d", seq)
}
