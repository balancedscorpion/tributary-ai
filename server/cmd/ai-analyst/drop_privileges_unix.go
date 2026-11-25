//go:build unix
// +build unix

package main

import (
	"errors"
	"os"
	"syscall"
)

func dropToNonrootInContainer() error {
	if os.Geteuid() != 0 || !isContainer() {
		return nil
	}

	// Best-effort: set groups, then gid, then uid.
	_ = syscall.Setgroups([]int{dockerDropGID})
	if err := syscall.Setgid(dockerDropGID); err != nil {
		return err
	}
	if err := syscall.Setuid(dockerDropUID); err != nil {
		return err
	}
	if os.Geteuid() == 0 {
		return errors.New("failed to drop root privileges")
	}
	return nil
}
