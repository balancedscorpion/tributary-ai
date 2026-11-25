package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bounded backoff for transient Windows file-locking errors. Total worst-case
// wait is about 1s before the original error is surfaced.
const replaceAttempts = 9

func backoffDelay(attempt int) time.Duration {
	d := 5 * time.Millisecond << uint(attempt)
	if d > 400*time.Millisecond {
		d = 400 * time.Millisecond
	}
	return d
}

// WriteFile writes data to path atomically by writing a unique temp file in the
// same directory and renaming it into place.
//
// This avoids failures caused by stale root-owned ".tmp" files in Docker volumes.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)

	// Best-effort cleanup of legacy deterministic tmp files.
	_ = os.Remove(path + ".tmp")

	f, err := os.CreateTemp(dir, base+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(tmp)
		}
	}()

	// Ensure explicit permissions even if umask is unexpected.
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := replaceWithRetry(tmp, path, perm); err != nil {
		return err
	}
	keep = true
	return nil
}

// replaceWithRetry renames tmp over path, retrying transient Windows errors.
//
// On Windows, MoveFileEx(MOVEFILE_REPLACE_EXISTING) fails with
// ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION when the destination is
// briefly held open by an antivirus scanner, search indexer, or a concurrent
// reader, and when the destination carries the read-only attribute. Those
// conditions clear within milliseconds, so a short bounded backoff converts an
// intermittent "Access is denied" failure into a successful write. On other
// platforms isTransientReplaceErr always reports false and behavior is
// unchanged: a single rename attempt.
func replaceWithRetry(tmp string, path string, perm os.FileMode) error {
	var err error
	for attempt := 0; attempt < replaceAttempts; attempt++ {
		err = os.Rename(tmp, path)
		if err == nil {
			return nil
		}
		if !isTransientReplaceErr(err) {
			return err
		}
		if attempt+1 >= replaceAttempts {
			break
		}
		// A read-only destination makes the replace fail with access denied;
		// clearing the attribute is safe for files this process owns.
		_ = os.Chmod(path, perm)
		time.Sleep(backoffDelay(attempt))
	}
	return err
}

// ReadFile reads path, retrying the transient Windows locking errors described
// on replaceWithRetry. Opening a file that is mid-replace (delete-pending) or
// momentarily held by a scanner also returns ERROR_ACCESS_DENIED on Windows,
// so readers of frequently rewritten files need the same bounded patience.
// Non-transient errors (including fs.ErrNotExist) are returned immediately.
func ReadFile(path string) ([]byte, error) {
	var (
		b   []byte
		err error
	)
	for attempt := 0; attempt < replaceAttempts; attempt++ {
		b, err = os.ReadFile(path)
		if err == nil || !isTransientReplaceErr(err) {
			return b, err
		}
		if attempt+1 >= replaceAttempts {
			break
		}
		time.Sleep(backoffDelay(attempt))
	}
	return b, err
}
