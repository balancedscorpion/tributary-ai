//go:build !windows

package atomicfile

// On POSIX platforms rename-over-existing is atomic and permission errors are
// genuine, so no replace/read retry is ever warranted.
func isTransientReplaceErr(error) bool {
	return false
}
