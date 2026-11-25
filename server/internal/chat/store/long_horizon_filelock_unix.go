//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package store

import (
	"os"

	"golang.org/x/sys/unix"
)

func lockLongHorizonFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX)
		if err != unix.EINTR {
			return err
		}
	}
}

func unlockLongHorizonFile(file *os.File) error {
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_UN)
		if err != unix.EINTR {
			return err
		}
	}
}
