package main

import (
	"os"

	"github.com/aac/act/internal/cli"
)

// fileExists reports whether path names an existing non-directory.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// emitTrackerNotCheckedOut renders the no-state guard's broken-checkout
// branch (act-a025ab): the configured tracker remote exists, but this
// checkout has no usable .act/ state. The envelope itself is built in the
// cli package so `act init` and the MCP server say the same thing.
func emitTrackerNotCheckedOut(asJSON bool, root, actDir string, noActDir bool, tr cli.TrackerRemote) {
	emitEnvelope(asJSON, cli.TrackerNotCheckedOutPayload(root, actDir, noActDir, tr))
}
