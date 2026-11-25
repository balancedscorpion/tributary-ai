package sharepoint

import _ "embed"

// embeddedBridgeScript is materialized at runtime for release binaries, where
// server/internal/sharepoint/bridge.mjs is not present on disk.
//
//go:embed bridge.mjs
var embeddedBridgeScript []byte
