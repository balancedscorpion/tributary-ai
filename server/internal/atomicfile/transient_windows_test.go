//go:build windows

package atomicfile

import (
	"errors"
	"io/fs"
	"testing"
)

func TestWindowsTransientReplaceErrors(t *testing.T) {
	t.Parallel()

	cases := []error{
		fs.ErrPermission,
		errorSharingViolation,
		errorLockViolation,
	}
	for _, err := range cases {
		if !isTransientReplaceErr(err) {
			t.Fatalf("isTransientReplaceErr(%v)=false, want true", err)
		}
	}
	if isTransientReplaceErr(errors.New("ordinary failure")) {
		t.Fatalf("ordinary errors should not be treated as transient")
	}
}
