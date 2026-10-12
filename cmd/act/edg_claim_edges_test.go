package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// edgPointOriginAtMissing repoints an existing `origin` of the tracker's
// nested repo at a local path that does not exist, so every later push
// fails. Local only: no network.
func edgPointOriginAtMissing(t *testing.T, dir string) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "edg-no-such-origin.git")
	sp6Git(t, "--git-dir", filepath.Join(dir, ".act", ".git"), "remote", "set-url", "origin", missing)
}

// TestEdg_ReclaimPushOfHeldIssueDoesNotPush pins that `act update --claim
// --push` on an issue this checkout already holds runs no push (act-11d2e2
// item 1): the re-claim writes no op, so there is nothing to publish, and an
// origin that has since become unreachable must not turn a claim already on
// origin into a "COMMITTED LOCALLY but NOT PUSHED" block.
func TestEdg_ReclaimPushOfHeldIssueDoesNotPush(t *testing.T) {
	dir := sp6Site(t)
	bare := wfxAddBareOrigin(t, dir)
	made, _ := wfxCreate(t, dir, "edg re-claim --push target")
	if first, stderr := upuUpdate(t, dir, made.ID, "--claim", "--push"); !first.Claimed || first.PushDeferred {
		t.Fatalf("setup: first claim --push: claimed %v, push_deferred %v\nstderr=%s", first.Claimed, first.PushDeferred, stderr)
	}
	if !wfxOriginHasHead(t, dir, bare) {
		t.Fatalf("setup: origin does not hold the claim commit")
	}
	edgPointOriginAtMissing(t, dir)

	res, stderr := upuUpdate(t, dir, made.ID, "--claim", "--push")
	if !res.Claimed || res.PushDeferred || res.PushDeferredReason != "" || len(res.Warnings) != 0 {
		t.Errorf("re-claim --push of a held issue, origin now unreachable: claimed %v, push_deferred %v, reason %q, warnings %v; want claimed and nothing else",
			res.Claimed, res.PushDeferred, res.PushDeferredReason, res.Warnings)
	}
	if strings.Contains(stderr, "NOT PUSHED") || stderr != "" {
		t.Errorf("re-claim --push of a held issue printed to stderr; want quiet (no op was written, nothing to push):\n%s", stderr)
	}
	if q := pnrPendingPushes(t, dir); q != "" {
		t.Errorf("re-claim --push of a held issue queued a push in .act/.pending-pushes:\n%s", q)
	}

	// Premise: the origin really is unreachable — a write that does commit
	// something is deferred. Without this the quiet re-claim proves nothing.
	if w, _ := upuUpdate(t, dir, made.ID, "--priority", "1"); !w.PushDeferred {
		t.Fatalf("premise: a real write against the repointed origin was not deferred")
	}
}
