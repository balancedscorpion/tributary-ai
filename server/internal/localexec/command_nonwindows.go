//go:build !windows

package localexec

import "os/exec"

func configureCommand(_ *exec.Cmd) {}
