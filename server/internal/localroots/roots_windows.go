//go:build windows

package localroots

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

func platformWholeComputerRoots() []string {
	preferred := preferredWindowsRoot()
	mask, err := windows.GetLogicalDrives()
	if err != nil || mask == 0 {
		if preferred != "" {
			return []string{preferred}
		}
		return []string{`C:\`}
	}

	roots := make([]string, 0, 4)
	appendRoot := func(root string) {
		for _, existing := range roots {
			if strings.EqualFold(existing, root) {
				return
			}
		}
		roots = append(roots, root)
	}
	for index := 0; index < 26; index++ {
		if mask&(1<<index) == 0 {
			continue
		}
		root := string(rune('A'+index)) + `:\`
		rootPtr, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		switch windows.GetDriveType(rootPtr) {
		case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_RAMDISK:
			appendRoot(root)
		}
	}
	if preferred != "" {
		for index, root := range roots {
			if !strings.EqualFold(root, preferred) || index == 0 {
				continue
			}
			copy(roots[1:index+1], roots[0:index])
			roots[0] = root
			break
		}
	}
	if len(roots) == 0 {
		return []string{`C:\`}
	}
	return roots
}

func preferredWindowsRoot() string {
	for _, candidate := range []func() (string, error){os.UserHomeDir, os.UserCacheDir} {
		value, err := candidate()
		if err != nil {
			continue
		}
		if volume := filepath.VolumeName(value); len(volume) == 2 && volume[1] == ':' {
			return strings.ToUpper(volume) + `\`
		}
	}
	if volume := filepath.VolumeName(os.TempDir()); len(volume) == 2 && volume[1] == ':' {
		return strings.ToUpper(volume) + `\`
	}
	return ""
}
