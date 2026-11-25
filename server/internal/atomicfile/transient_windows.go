//go:build windows

package atomicfile

import (
	"errors"
	"io/fs"
	"syscall"
)

// Stdlib syscall does not export these Win32 error codes (x/sys/windows does,
// but it is not a direct dependency here).
const (
	errorSharingViolation = syscall.Errno(32) // ERROR_SHARING_VIOLATION
	errorLockViolation    = syscall.Errno(33) // ERROR_LOCK_VIOLATION
)

// isTransientReplaceErr reports whether err is a Windows file-locking error
// that typically clears within milliseconds: ERROR_ACCESS_DENIED (read-only
// destination, delete-pending file, or a handle held without
// FILE_SHARE_DELETE) or a sharing/lock violation from an antivirus scanner,
// search indexer, or concurrent reader.
func isTransientReplaceErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	return errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation)
}
