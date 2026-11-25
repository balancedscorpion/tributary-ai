//go:build windows

package localroots

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestWholeComputerRootsContainOnlyLocalSearchableDriveTypes(t *testing.T) {
	for _, root := range WholeComputerRoots() {
		rootPtr, err := windows.UTF16PtrFromString(root)
		if err != nil {
			t.Fatalf("UTF16PtrFromString(%q): %v", root, err)
		}
		switch driveType := windows.GetDriveType(rootPtr); driveType {
		case windows.DRIVE_FIXED, windows.DRIVE_REMOVABLE, windows.DRIVE_RAMDISK:
		default:
			t.Fatalf("root %q has excluded Windows drive type %d", root, driveType)
		}
	}
}
