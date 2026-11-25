// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package blake2b

// This is a local-only copy of golang.org/x/crypto/blake2b with a generic
// implementation to avoid pulling in platform-specific assembly and
// golang.org/x/sys/cpu as external dependencies.
func hashBlocks(h *[8]uint64, c *[2]uint64, flag uint64, blocks []byte) {
	hashBlocksGeneric(h, c, flag, blocks)
}

