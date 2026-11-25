//go:build windows

package localexec

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

func TestCommandContextHidesWindowsConsole(t *testing.T) {
	cmd := CommandContext(context.Background(), "cmd.exe", "/c", "exit", "0")
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("SysProcAttr=%#v, want HideWindow", cmd.SysProcAttr)
	}
	if cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatalf("CreationFlags=%#x, want CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
}
