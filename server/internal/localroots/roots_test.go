package localroots

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWholeComputerRootsAreAbsoluteFilesystemRoots(t *testing.T) {
	roots := WholeComputerRoots()
	if len(roots) == 0 {
		t.Fatal("WholeComputerRoots returned no roots")
	}
	seen := map[string]bool{}
	for _, root := range roots {
		if !filepath.IsAbs(root) || filepath.Dir(root) != root {
			t.Fatalf("root %q is not an absolute filesystem root", root)
		}
		key := root
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if seen[key] {
			t.Fatalf("duplicate root %q in %#v", root, roots)
		}
		seen[key] = true
	}
	if PrimaryRoot() != roots[0] {
		t.Fatalf("PrimaryRoot=%q, want first discovered root %q", PrimaryRoot(), roots[0])
	}
}

func TestWindowsWholeComputerRootsExcludeNetworkAndOpticalDrives(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows drive types are only available on Windows")
	}
	for _, root := range WholeComputerRoots() {
		if !strings.HasSuffix(root, `:\`) || len(root) != 3 {
			t.Fatalf("Windows root=%q, want drive-letter root", root)
		}
	}
}
