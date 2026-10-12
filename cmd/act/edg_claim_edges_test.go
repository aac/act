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

// TestEdg_MultiFlagUpdateDefersOnce pins that an `act update` writing
// several ops against an origin that cannot be reached reports the deferred
// push once (act-11d2e2 item 3): one WARNING block on stderr and one entry
// in .act/.pending-pushes, not one of each per op. A push covers every
// commit reachable from HEAD, so one entry is all a later flush needs.
func TestEdg_MultiFlagUpdateDefersOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no --push", []string{"--priority", "1", "--title", "edg retitled", "--description", "edg body"}},
		{"--push", []string{"--priority", "1", "--title", "edg retitled", "--description", "edg body", "--push"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sp6Site(t)
			made, _ := wfxCreate(t, dir, "edg multi-flag target")
			upuAddMissingOrigin(t, dir)

			res, stderr := upuUpdate(t, dir, append([]string{made.ID}, tc.args...)...)
			if !res.Committed || !res.PushDeferred || res.PushDeferredReason == "" {
				t.Errorf("multi-flag update, unreachable origin: committed %v, push_deferred %v, reason %q; want committed and deferred with a reason",
					res.Committed, res.PushDeferred, res.PushDeferredReason)
			}
			if n := strings.Count(stderr, "COMMITTED LOCALLY but NOT PUSHED"); n != 1 {
				t.Errorf("multi-flag update, unreachable origin: %d not-pushed WARNING blocks on stderr, want 1", n)
			}
			q := pnrPendingPushes(t, dir)
			if n := len(strings.Split(q, "\n")); q == "" || n != 1 {
				t.Errorf("multi-flag update, unreachable origin: .act/.pending-pushes holds %d entries, want 1:\n%s", n, q)
			}
			show := sp8Show(t, dir, made.ID)
			if p, title := string(show["priority"]), sp8Str(show["title"]); p != "1" || title != "edg retitled" {
				t.Errorf("multi-flag update, unreachable origin: show reports priority %s, title %q; want 1 and the new title", p, title)
			}
		})
	}

	// --offline queues once too, and stays quiet.
	t.Run("--offline", func(t *testing.T) {
		dir := sp6Site(t)
		wfxAddBareOrigin(t, dir)
		made, _ := wfxCreate(t, dir, "edg offline multi-flag target")
		_, stderr := upuUpdate(t, dir, made.ID, "--priority", "1", "--title", "edg retitled", "--offline")
		q := pnrPendingPushes(t, dir)
		if n := len(strings.Split(q, "\n")); q == "" || n != 1 || stderr != "" {
			t.Errorf("multi-flag update --offline: .act/.pending-pushes holds %d entries, stderr %q; want 1 entry and quiet:\n%s", n, stderr, q)
		}
	})
}
