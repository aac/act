package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// wfxCreated is the part of `act create --json` these tests read.
type wfxCreated struct {
	ID        string   `json:"id"`
	Committed bool     `json:"committed"`
	Pushed    *bool    `json:"pushed"`
	Warnings  []string `json:"warnings"`
}

// wfxCreate runs `act create --json <args>`, wants exit 0, and returns the
// parsed result and stderr.
func wfxCreate(t *testing.T, dir string, args ...string) (wfxCreated, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", append([]string{"create", "--json"}, args...)...)
	var res wfxCreated
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil || res.ID == "" || res.Pushed == nil {
		t.Fatalf("act create --json %v: not a create result with id and pushed (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	return res, stderr
}

// wfxAddBareOrigin makes an empty bare repo under a temp dir, adds it as
// `origin` of the tracker's nested repo, and returns its path. Local only: no
// network.
func wfxAddBareOrigin(t *testing.T, dir string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "wfx-origin.git")
	sp6Git(t, "init", "-q", "--bare", bare)
	sp6Git(t, "--git-dir", filepath.Join(dir, ".act", ".git"), "remote", "add", "origin", bare)
	return bare
}

// wfxOriginHasHead reports whether the bare repo holds the nested repo's HEAD
// commit — whether the op commit really was pushed.
func wfxOriginHasHead(t *testing.T, dir, bare string) bool {
	t.Helper()
	head, err := sp7GitOut("--git-dir", filepath.Join(dir, ".act", ".git"), "rev-parse", "HEAD")
	if err != nil || head == "" {
		t.Fatalf("rev-parse HEAD of the nested repo: %v", err)
	}
	_, err = sp7GitOut("--git-dir", bare, "cat-file", "-e", head+"^{commit}")
	return err == nil
}

// TestDocClaim_Spec_CreatePushedReportsWhatHappened asserts the `pushed`
// sentences under the JSON example of the `act create` section: `pushed` is
// true exactly when this call put the op commit on `origin`, whatever
// `--push` said (act-8c1c05: it used to echo the flag, so `--push` in a
// tracker with no remote answered pushed true).
func TestDocClaim_Spec_CreatePushedReportsWhatHappened(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		dir := sp6Site(t)
		plain, _ := wfxCreate(t, dir, "wfx plain")
		if *plain.Pushed || len(plain.Warnings) != 0 {
			t.Errorf("act create --json with no remote: pushed %v, warnings %v; want false and none", *plain.Pushed, plain.Warnings)
		}
		pushed, _ := wfxCreate(t, dir, "--push", "wfx pushed nowhere")
		if *pushed.Pushed {
			t.Errorf("act create --json --push with no remote: pushed true, and there is nowhere it could have gone")
		}
		if !pushed.Committed || strings.Join(pushed.Warnings, ",") != "push_no_remote" {
			t.Errorf("act create --json --push with no remote: committed %v, warnings %v; want committed and exactly [push_no_remote]", pushed.Committed, pushed.Warnings)
		}
		if st := sp8Str(sp8Show(t, dir, pushed.ID)["status"]); st != "open" {
			t.Errorf("the issue created by --push with no remote shows status %q, want open", st)
		}
		// Without --json the reason goes to stderr and the issue is still created.
		out, stderr := pass5Run(t, dir, 0, "", "create", "--push", "wfx pushed nowhere, human")
		if !strings.HasPrefix(out, "Created ") || !strings.Contains(stderr, "act create: warning: --push: no origin remote, nothing was pushed") {
			t.Errorf("act create --push with no remote: stdout %q, stderr %q; want the Created line and the nothing-was-pushed warning", out, stderr)
		}
	})

	t.Run("origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		// No --push: a tracker with origin publishes every write.
		auto, _ := wfxCreate(t, dir, "wfx auto-published")
		if !*auto.Pushed || !wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act create --json with origin: pushed %v, origin has the commit %v; want both true", *auto.Pushed, wfxOriginHasHead(t, dir, bare))
		}
		flagged, _ := wfxCreate(t, dir, "--push", "wfx pushed")
		if !*flagged.Pushed || len(flagged.Warnings) != 0 || !wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act create --json --push with origin: pushed %v, warnings %v, origin has the commit %v; want true, none, true", *flagged.Pushed, flagged.Warnings, wfxOriginHasHead(t, dir, bare))
		}
		// --offline commits and holds the push back.
		held, _ := wfxCreate(t, dir, "--offline", "wfx offline")
		if *held.Pushed || !held.Committed || wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act create --json --offline: pushed %v, committed %v, origin has the commit %v; want false, true, false", *held.Pushed, held.Committed, wfxOriginHasHead(t, dir, bare))
		}
	})

	t.Run("origin, no commit", func(t *testing.T) {
		dir := sp6Site(t)
		wfxAddBareOrigin(t, dir)
		nc, _ := wfxCreate(t, dir, "--no-commit", "wfx uncommitted")
		if *nc.Pushed || nc.Committed {
			t.Errorf("act create --json --no-commit: pushed %v, committed %v; want both false", *nc.Pushed, nc.Committed)
		}
	})

	t.Run("origin, push fails", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		wfxCreate(t, dir, "wfx seed") // origin gets the branch
		t.Setenv("ACT_TEST_FAIL_PUSH_AFTER", "1")
		failed, stderr := wfxCreate(t, dir, "--push", "wfx push fails")
		if *failed.Pushed || !failed.Committed || wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act create --json --push with every push failing: pushed %v, committed %v, origin has the commit %v; want false, true, false", *failed.Pushed, failed.Committed, wfxOriginHasHead(t, dir, bare))
		}
		if !strings.Contains(stderr, "NOT PUSHED") {
			t.Errorf("act create with every push failing: stderr has no NOT PUSHED warning:\n%s", stderr)
		}
	})
}
