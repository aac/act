package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// edgBlockPendingPushes makes .act/.pending-pushes a directory, so every
// append to the deferred-push queue fails. No permission bits involved, so
// it behaves the same as root.
func edgBlockPendingPushes(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(filepath.Join(dir, ".act", ".pending-pushes"), 0o755); err != nil {
		t.Fatalf("make .act/.pending-pushes a directory: %v", err)
	}
}

// edgWantQueueWarning checks stderr for the one WARNING block an --offline
// write prints when it could not queue its commit.
func edgWantQueueWarning(t *testing.T, what, stderr string) {
	t.Helper()
	for _, want := range []string{
		"act: WARNING: the op was COMMITTED LOCALLY but could NOT be queued in .act/.pending-pushes.",
		"exit status is 0 on purpose",
	} {
		if n := strings.Count(stderr, want); n != 1 {
			t.Errorf("%s with .act/.pending-pushes unwritable: stderr has %d of %q, want 1:\n%s", what, n, want, stderr)
		}
	}
}

// TestDocClaim_Spec_OfflineQueueFailureIsAWarning asserts the last paragraph
// of "Offline mode" in docs/spec.md (act-11d2e2 item 2): an `--offline`
// write whose commit landed but whose `.act/.pending-pushes` append failed
// exits 0 and says so in a WARNING block on stderr. The same failure used to
// be three things: silent on `act update --claim --offline`, exit 1
// `write_failed` on create / update / dep add / reopen / delete, and exit 1
// `record_pending_push_failed` on close — the last two for an op that was
// already committed.
func TestDocClaim_Spec_OfflineQueueFailureIsAWarning(t *testing.T) {
	t.Run("update --claim --offline", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "edg offline claim target")
		edgBlockPendingPushes(t, dir)

		res, stderr := upuUpdate(t, dir, made.ID, "--claim", "--offline")
		if !res.Claimed || res.Error != "" {
			t.Errorf("update --claim --offline: claimed %v, error %q; want claimed", res.Claimed, res.Error)
		}
		edgWantQueueWarning(t, "update --claim --offline", stderr)
	})

	t.Run("update --offline", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "edg offline update target")
		edgBlockPendingPushes(t, dir)

		res, stderr := upuUpdate(t, dir, made.ID, "--priority", "1", "--offline")
		if !res.Committed || res.Error != "" {
			t.Errorf("update --offline: committed %v, error %q; want committed and no error", res.Committed, res.Error)
		}
		if p := string(sp8Show(t, dir, made.ID)["priority"]); p != "1" {
			t.Errorf("update --priority 1 --offline: show reports priority %s, want 1", p)
		}
		edgWantQueueWarning(t, "update --offline", stderr)
	})

	t.Run("create --offline", func(t *testing.T) {
		dir := sp6Site(t)
		edgBlockPendingPushes(t, dir)

		res, stderr := wfxCreate(t, dir, "--offline", "edg offline create")
		if st := sp8Str(sp8Show(t, dir, res.ID)["status"]); st != "open" {
			t.Errorf("create --offline: show reports status %q, want open", st)
		}
		edgWantQueueWarning(t, "create --offline", stderr)
	})

	t.Run("close --offline", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "edg offline close target")
		edgBlockPendingPushes(t, dir)

		out, stderr := pass5Run(t, dir, 0, "", "close", "--json", "--no-doctor", "--offline", made.ID)
		if strings.Contains(out, `"error"`) {
			t.Errorf("close --offline: stdout carries an error envelope:\n%s", out)
		}
		if st := sp8Str(sp8Show(t, dir, made.ID)["status"]); st != "closed" {
			t.Errorf("close --offline: show reports status %q, want closed", st)
		}
		edgWantQueueWarning(t, "close --offline", stderr)
	})

	// A queue that can be written stays quiet.
	t.Run("writable queue is quiet", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "edg quiet offline target")
		if _, stderr := upuUpdate(t, dir, made.ID, "--priority", "1", "--offline"); stderr != "" {
			t.Errorf("update --offline with a writable queue printed to stderr:\n%s", stderr)
		}
		if q := pnrPendingPushes(t, dir); q == "" {
			t.Errorf("update --offline with a writable queue queued nothing")
		}
	})
}
