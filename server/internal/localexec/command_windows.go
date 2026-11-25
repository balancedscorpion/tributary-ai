//go:build windows

package localexec

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureCommand(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
