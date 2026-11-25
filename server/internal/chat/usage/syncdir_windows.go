//go:build windows

package usage

import (
	"errors"
	"io/fs"
	"os"
)

// Windows does not provide a portable directory-handle equivalent of POSIX
// fsync for an os.File opened by path. The journal still fsyncs the temporary
// file before its same-directory atomic rename; here we verify the destination
// directory remains available instead of failing every provider dispatch on an
// unsupported directory FlushFileBuffers call.
func syncDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return &fs.PathError{Op: "sync", Path: path, Err: errors.New("not a directory")}
	}
	return nil
}
