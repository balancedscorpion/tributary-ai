package localroots

import (
	"path/filepath"
	"runtime"
	"strings"
)

// WholeComputerRoots returns every local filesystem root covered by the
// Local Computer connection. Windows can have multiple independent volumes.
func WholeComputerRoots() []string {
	raw := platformWholeComputerRoots()
	out := make([]string, 0, len(raw))
	seen := map[string]struct{}{}
	for _, value := range raw {
		root := filepath.Clean(strings.TrimSpace(value))
		if root == "" || root == "." || filepath.Dir(root) != root {
			continue
		}
		key := root
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, root)
	}
	return out
}

// PrimaryRoot is the default root for legacy single-root source links.
func PrimaryRoot() string {
	if roots := WholeComputerRoots(); len(roots) > 0 {
		return roots[0]
	}
	return string(filepath.Separator)
}
