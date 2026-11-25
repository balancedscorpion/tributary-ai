//go:build darwin

package atlassian

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func removeQuarantineAttributes(root string) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		err := unix.Removexattr(path, "com.apple.quarantine")
		if err == nil || errors.Is(err, unix.ENOATTR) || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("%s: %w", path, err)
	})
}
