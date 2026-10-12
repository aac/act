package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// Reproductions for act-e5d6b4 items 2 and 3, run against the real binary.

// dpolFoldFailedSite makes a tracker whose fold fails: its one issue's create
// op carries an op_version this binary does not know.
func dpolFoldFailedSite(t *testing.T) string {
	t.Helper()
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")
	pass5UnknownOpVersion(t, dir, id)
	return dir
}

// dpolHasLine reports whether text has a line equal to want.
func dpolHasLine(text, want string) bool {
	for _, line := range strings.Split(text, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// TestDoctor_FoldErrorEchoedBareToStderr: human-mode `act doctor` on a store
// whose fold fails writes the fold-error message bare to stderr, as it does
// for the other findings an operator must not miss (stale-git-lock,
// index-malformed, status-vs-oplog, the remote cases).
func TestDoctor_FoldErrorEchoedBareToStderr(t *testing.T) {
	dir := dpolFoldFailedSite(t)

	// The message doctor reports, read from --json so the test does not
	// restate fold's wording.
	out, _, code := runActIn(t, dir, "doctor", "--no-fetch", "--json")
	if code != 1 {
		t.Fatalf("doctor --json on a fold-failed store: exit %d, want 1; out=%s", code, out)
	}
	var parsed struct {
		Findings []struct {
			Check   string `json:"check"`
			Message string `json:"message"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("parse doctor --json output %q: %v", out, err)
	}
	msg := ""
	for _, f := range parsed.Findings {
		if f.Check == "fold-error" {
			msg = f.Message
		}
	}
	if !strings.HasPrefix(msg, "fold error: ") {
		t.Fatalf("no fold-error finding with a \"fold error: \" message in %s", out)
	}

	stdout, stderr, code := runActIn(t, dir, "doctor", "--no-fetch")
	if code != 1 {
		t.Fatalf("doctor on a fold-failed store: exit %d, want 1; stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, msg) {
		t.Errorf("stdout lacks the fold-error finding %q: %s", msg, stdout)
	}
	if !dpolHasLine(stderr, msg) {
		t.Errorf("stderr has no bare line %q; stderr=%q", msg, stderr)
	}
}

// TestVersionCheckRepo_UnclonedTracker: on a checkout whose configured
// tracker remote holds a tracker that is not cloned here, `act version
// --check-repo` answers tracker_not_checked_out like the other read commands,
// not no_repo. Plain `act version` reads no repo and still exits 0.
func TestVersionCheckRepo_UnclonedTracker(t *testing.T) {
	brokencoIsolateEnv(t)
	bares := t.TempDir()
	bare, _ := brokencoTrackerBare(t, bares)
	host := brokencoHostRepo(t)
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))

	// Path proof: the guard fires for a guarded read command here.
	pass5Run(t, host, 3, "tracker_not_checked_out", "list", "--json")

	out, _ := pass5Run(t, host, 3, "tracker_not_checked_out", "version", "--check-repo", "--json")
	if !strings.Contains(out, bare) {
		t.Errorf("version --check-repo --json does not name the tracker remote %s: %s", bare, out)
	}
	_, stderr, code := runActIn(t, host, "version", "--check-repo")
	if code != 3 || !strings.Contains(stderr, "tracker exists at "+bare) {
		t.Errorf("version --check-repo: exit %d, stderr %q; want 3 naming the tracker at %s", code, stderr, bare)
	}

	// Without the flag no repo is consulted.
	pass5Run(t, host, 0, "", "version", "--json")
	pass5Run(t, host, 0, "", "version")
}
