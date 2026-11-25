//go:build darwin

package atlassian

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRemoveQuarantineAttributesRecursively(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "chrome-headless-shell")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Setxattr(target, "com.apple.quarantine", []byte("0081;00000000;CodexTest;"), 0); err != nil {
		t.Fatal(err)
	}

	if err := removeQuarantineAttributes(root); err != nil {
		t.Fatalf("removeQuarantineAttributes returned error: %v", err)
	}
	if _, err := unix.Getxattr(target, "com.apple.quarantine", nil); !errors.Is(err, unix.ENOATTR) {
		t.Fatalf("quarantine attribute still present or unexpected error: %v", err)
	}
}
