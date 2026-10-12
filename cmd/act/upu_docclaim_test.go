package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// upuUpdated is the part of `act update --json` these tests read, for both
// the plain result and the --claim result.
type upuUpdated struct {
	ID                 string   `json:"id"`
	Committed          bool     `json:"committed"`
	Claimed            bool     `json:"claimed"`
	Error              string   `json:"error"`
	Warnings           []string `json:"warnings"`
	PushDeferred       bool     `json:"push_deferred"`
	PushDeferredReason string   `json:"push_deferred_reason"`
}

// upuUpdate runs `act update --json <args>`, wants exit 0, and returns the
// parsed result and stderr.
func upuUpdate(t *testing.T, dir string, args ...string) (upuUpdated, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", append([]string{"update", "--json"}, args...)...)
	var res upuUpdated
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("act update --json %v: not a JSON object (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	return res, stderr
}

// upuAddMissingOrigin points the tracker's `origin` at a local path that does
// not exist, so every push fails. Local only: no network.
func upuAddMissingOrigin(t *testing.T, dir string) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "upu-no-such-origin.git")
	sp6Git(t, "--git-dir", filepath.Join(dir, ".act", ".git"), "remote", "add", "origin", missing)
}

// TestDocClaim_Spec_UpdatePushUnreachableOriginDefers asserts the
// `push_deferred` sentence under the JSON example of the `act update`
// section: with an `origin` that cannot be reached, `update --push` lands,
// exits 0, and reports the push as deferred (act-8ffc13: it used to exit 1
// `push_failed` with the field already changed).
func TestDocClaim_Spec_UpdatePushUnreachableOriginDefers(t *testing.T) {
	dir := sp6Site(t)
	made, _ := wfxCreate(t, dir, "upu update target")
	upuAddMissingOrigin(t, dir)

	res, stderr := upuUpdate(t, dir, made.ID, "--priority", "1", "--push")
	if res.Error != "" || !res.Committed || !res.PushDeferred || res.PushDeferredReason == "" {
		t.Errorf("act update --json --priority 1 --push with an unreachable origin: error %q, committed %v, push_deferred %v, push_deferred_reason %q; want no error, committed, deferred, a reason\nstderr=%s",
			res.Error, res.Committed, res.PushDeferred, res.PushDeferredReason, stderr)
	}
	if p := string(sp8Show(t, dir, made.ID)["priority"]); p != "1" {
		t.Errorf("after update --priority 1 --push with an unreachable origin, show reports priority %s, want 1", p)
	}
	if !strings.Contains(stderr, "COMMITTED LOCALLY but NOT PUSHED") {
		t.Errorf("update --push with an unreachable origin: stderr lacks the not-pushed warning block:\n%s", stderr)
	}
	if q := pnrPendingPushes(t, dir); q == "" {
		t.Errorf("update --push with an unreachable origin queued nothing in .act/.pending-pushes")
	}

	// The same deferral without --push is reported the same way.
	plain, _ := upuUpdate(t, dir, made.ID, "--priority", "2")
	if !plain.Committed || !plain.PushDeferred {
		t.Errorf("act update --json --priority 2 with an unreachable origin: committed %v, push_deferred %v; want both true", plain.Committed, plain.PushDeferred)
	}

	// A push that reaches origin reports no deferral.
	okDir := sp6Site(t)
	wfxAddBareOrigin(t, okDir)
	okMade, _ := wfxCreate(t, okDir, "upu update target, origin")
	ok, _ := upuUpdate(t, okDir, okMade.ID, "--priority", "1", "--push")
	if !ok.Committed || ok.PushDeferred || ok.PushDeferredReason != "" {
		t.Errorf("act update --json --priority 1 --push with a reachable origin: committed %v, push_deferred %v, reason %q; want committed and no deferral", ok.Committed, ok.PushDeferred, ok.PushDeferredReason)
	}
}

// TestDocClaim_Spec_UpdateClaimPushUnreachableOriginDefers asserts the same
// for a won claim: `update --claim --push` with an unreachable `origin` exits
// 0 with the issue claimed and the push reported as deferred (act-8ffc13).
func TestDocClaim_Spec_UpdateClaimPushUnreachableOriginDefers(t *testing.T) {
	dir := sp6Site(t)
	made, _ := wfxCreate(t, dir, "upu claim target")
	upuAddMissingOrigin(t, dir)

	res, stderr := upuUpdate(t, dir, made.ID, "--claim", "--push")
	if res.Error != "" || !res.Claimed || !res.PushDeferred || res.PushDeferredReason == "" {
		t.Errorf("act update --json --claim --push with an unreachable origin: error %q, claimed %v, push_deferred %v, push_deferred_reason %q; want no error, claimed, deferred, a reason\nstderr=%s",
			res.Error, res.Claimed, res.PushDeferred, res.PushDeferredReason, stderr)
	}
	if st := sp8Str(sp8Show(t, dir, made.ID)["status"]); st != "in_progress" {
		t.Errorf("after update --claim --push with an unreachable origin, show reports status %q, want in_progress", st)
	}
	if !strings.Contains(stderr, "COMMITTED LOCALLY but NOT PUSHED") {
		t.Errorf("update --claim --push with an unreachable origin: stderr lacks the not-pushed warning block:\n%s", stderr)
	}
	if q := pnrPendingPushes(t, dir); q == "" {
		t.Errorf("update --claim --push with an unreachable origin queued nothing in .act/.pending-pushes")
	}
}
