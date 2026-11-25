package atlassian

import _ "embed"

// embeddedBridgeScript is materialized at runtime for release binaries, where
// server/internal/atlassian/bridge.mjs is not present on disk.
//
//go:embed bridge.mjs
var embeddedBridgeScript []byte
