//go:build !windows

package localroots

import "os"

func platformWholeComputerRoots() []string {
	return []string{string(os.PathSeparator)}
}
