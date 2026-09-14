package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/aac/act/internal/cli"
)

// fileExists reports whether path names an existing non-directory.
func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// emitTrackerNotCheckedOut renders the no-state guard's broken-checkout
// branch (act-a025ab): the configured tracker remote exists, but this
// checkout has no usable .act/ state. The message names the remote, where
// the setting came from, and the recovery command, and deliberately does
// not call the situation normal.
func emitTrackerNotCheckedOut(asJSON bool, root, actDir string, noActDir bool, tr cli.TrackerRemote) {
	// A tracker's history may or may not carry config.json (act commits it;
	// operators syncing between machines often keep it machine-local), so
	// the clone is followed by `act init` only when it is still missing.
	clone := fmt.Sprintf("git clone %s %s (then act init if %s is missing)",
		tr.URL, actDir, filepath.Join(actDir, "config.json"))
	var recover, what string
	switch {
	case noActDir:
		what = "this checkout has no act state"
		recover = clone
	case fileExists(filepath.Join(actDir, ".git", "HEAD")):
		what = fmt.Sprintf("%s has no config.json", actDir)
		recover = "act init"
	default:
		what = fmt.Sprintf("%s has no config.json and is not a git repo", actDir)
		recover = fmt.Sprintf("move %s aside, then %s", actDir, clone)
	}
	emitEnvelope(asJSON, map[string]any{
		"error": cli.ErrTrackerNotCheckedOut,
		"message": fmt.Sprintf(
			"act: %s, but this repo's tracker exists at %s (from %s) — the queue is not checked out here. Recover with: %s",
			what, tr.URL, tr.Source, recover),
		"details": map[string]any{
			"repo_root":      root,
			"tracker_remote": tr.URL,
			"source":         tr.Source,
			"recover":        recover,
		},
	})
}
