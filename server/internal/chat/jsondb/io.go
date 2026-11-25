package jsondb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/atomicfile"
)

func EnsureBaseDirs(cfgDir string) error {
	root := RootDir(cfgDir)
	for _, dir := range []string{
		root,
		ThreadsDir(cfgDir),
		IndexesDir(cfgDir),
		QueryJobsDir(cfgDir),
		filepath.Join(root, "query_job_events"),
		filepath.Join(root, "query_result_chunks"),
		filepath.Join(root, "run_query_dependencies"),
		filepath.Join(root, "run_continuation_checkpoints"),
		QueryCapacityReservationsDir(cfgDir),
		QueryTransactionsDir(cfgDir),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func ReadJSONFileStrict(path string, dst any) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return errors.New("empty path")
	}
	// atomicfile.ReadFile retries the transient access-denied errors Windows
	// returns while the same file is being atomically replaced.
	b, err := atomicfile.ReadFile(p)
	if err != nil {
		return err
	}
	return decodeJSONStrict(bytes.NewReader(b), dst)
}

func WriteJSONFile(path string, v any) error {
	p := strings.TrimSpace(path)
	if p == "" {
		return errors.New("empty path")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicfile.WriteFile(p, b, 0o600)
}

func decodeJSONStrict(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	// Prevent trailing junk.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected additional JSON content")
		}
		return err
	}
	return nil
}
