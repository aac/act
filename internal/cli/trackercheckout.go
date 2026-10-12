package cli

import (
	"fmt"
	"os"
	"path/filepath"
)

// TrackerNotCheckedOutPayload builds the tracker_not_checked_out envelope
// for a checkout at root whose act dir (actDir) is missing (noActDir) or
// lacks config.json, when tr — the configured tracker remote — exists. The
// message names the remote, where the setting came from, and the recovery
// command, and deliberately does not call the situation normal. Shared by
// the CLI no-state guard, `act init`, and the MCP server so every surface
// says the same thing (act-a025ab, act-626391, act-ef5a69).
//
// act-18bd72: the remote is printed with any userinfo masked
// (tr.DisplayURL) — in the message, in the recovery command and in
// `details.tracker_remote`. The recovery command is meant to be copied, so
// when something was masked it says so and names tr.Source, where the real
// URL is; it does not print a credential to save a lookup.
func TrackerNotCheckedOutPayload(root, actDir string, noActDir bool, tr TrackerRemote) map[string]any {
	// A tracker's history may or may not carry config.json (act commits it;
	// operators syncing between machines often keep it machine-local), so
	// the clone is followed by `act init` only when it is still missing.
	shown := tr.DisplayURL()
	clone := fmt.Sprintf("git clone %s %s (then act init if %s is missing)",
		shown, actDir, filepath.Join(actDir, "config.json"))
	if shown != tr.URL {
		clone += fmt.Sprintf(" — credentials in the URL are masked here; the real URL is in %s", tr.Source)
	}
	var recover, what string
	switch {
	case noActDir:
		what = "this checkout has no act state"
		recover = clone
	case isRegularFile(filepath.Join(actDir, ".git", "HEAD")):
		what = fmt.Sprintf("%s has no config.json", actDir)
		recover = "act init"
	default:
		what = fmt.Sprintf("%s has no config.json and is not a git repo", actDir)
		recover = fmt.Sprintf("move %s aside, then %s", actDir, clone)
	}
	return map[string]any{
		"error": ErrTrackerNotCheckedOut,
		"message": fmt.Sprintf(
			"act: %s, but this repo's tracker exists at %s (from %s) — the queue is not checked out here. Recover with: %s",
			what, shown, tr.Source, recover),
		"details": map[string]any{
			"repo_root":      root,
			"tracker_remote": shown,
			"source":         tr.Source,
			"recover":        recover,
		},
	}
}

// TrackerCheckoutState reports, for the host repo at root, whether this
// checkout lacks usable act state: noActDir when `.act/` is absent, missing
// when `.act/` is absent or has no config.json. It probes no remote.
func TrackerCheckoutState(root string) (actDir string, noActDir, missing bool) {
	actDir = filepath.Join(root, ".act")
	_, err := os.Stat(actDir)
	noActDir = os.IsNotExist(err)
	missing = noActDir || !isRegularFile(filepath.Join(actDir, "config.json"))
	return actDir, noActDir, missing
}

// isRegularFile reports whether path names an existing non-directory.
func isRegularFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}
