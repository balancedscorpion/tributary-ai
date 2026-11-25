package atlassian

import (
	"path/filepath"
	"strings"
	"sync"
)

var preparedBundledRuntimeRoots sync.Map

func prepareBundledRuntimeForLaunch(runtimeRoot string) error {
	runtimeRoot = strings.TrimSpace(runtimeRoot)
	if runtimeRoot == "" {
		return nil
	}
	absRoot, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return err
	}
	if _, loaded := preparedBundledRuntimeRoots.LoadOrStore(absRoot, struct{}{}); loaded {
		return nil
	}
	_ = removeQuarantineAttributes(absRoot)
	return nil
}
