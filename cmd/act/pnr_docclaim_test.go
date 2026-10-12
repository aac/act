package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pnrUpdated is the part of `act update --json` these tests read, for both
// the plain result and the --claim result.
type pnrUpdated struct {
	ID        string          `json:"id"`
	Committed bool            `json:"committed"`
	Claimed   bool            `json:"claimed"`
	Error     string          `json:"error"`
	Warnings  []string        `json:"warnings"`
	RawOps    json.RawMessage `json:"ops_written"`
}

// pnrUpdate runs `act update --json <args>`, wants exit 0, and returns the
// parsed result and stderr.
func pnrUpdate(t *testing.T, dir string, args ...string) (pnrUpdated, string) {
	t.Helper()
	out, stderr := pass5Run(t, dir, 0, "", append([]string{"update", "--json"}, args...)...)
	var res pnrUpdated
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("act update --json %v: not a JSON object (%v)\nstdout=%s\nstderr=%s", args, err, out, stderr)
	}
	return res, stderr
}

// pnrPendingPushes returns the content of the tracker's deferred-push queue,
// "" when the file does not exist.
func pnrPendingPushes(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, ".act", ".pending-pushes"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read .pending-pushes: %v", err)
	}
	return strings.TrimSpace(string(b))
}

// TestDocClaim_Spec_UpdatePushNoRemoteWarns asserts the `--push` bullet of
// the `act update` section: in a tracker with no `origin` remote the update
// lands, exits 0, and names the push that did not happen with the warning
// `push_no_remote` (act-f4fd34: it used to exit 1 `push_failed` with the
// field already changed).
func TestDocClaim_Spec_UpdatePushNoRemoteWarns(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "pnr update target")
		plain, _ := pnrUpdate(t, dir, made.ID, "--priority", "2")
		if !plain.Committed || len(plain.Warnings) != 0 {
			t.Errorf("act update --json --priority 2 with no remote: committed %v, warnings %v; want committed and none", plain.Committed, plain.Warnings)
		}
		res, _ := pnrUpdate(t, dir, made.ID, "--priority", "1", "--push")
		if res.Error != "" || !res.Committed || strings.Join(res.Warnings, ",") != "push_no_remote" {
			t.Errorf("act update --json --priority 1 --push with no remote: error %q, committed %v, warnings %v; want no error, committed, exactly [push_no_remote]", res.Error, res.Committed, res.Warnings)
		}
		if p := string(sp8Show(t, dir, made.ID)["priority"]); p != "1" {
			t.Errorf("after update --priority 1 --push with no remote, show reports priority %s, want 1", p)
		}
		// Without --json the reason goes to stderr and the update still lands.
		out, stderr := pass5Run(t, dir, 0, "", "update", made.ID, "--priority", "3", "--push")
		if !strings.Contains(stderr, "act update: warning: --push: no origin remote, nothing was pushed") {
			t.Errorf("act update --priority 3 --push with no remote: stdout %q, stderr %q; want the nothing-was-pushed warning on stderr", out, stderr)
		}
		if p := string(sp8Show(t, dir, made.ID)["priority"]); p != "3" {
			t.Errorf("after update --priority 3 --push with no remote, show reports priority %s, want 3", p)
		}
	})

	t.Run("origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		made, _ := wfxCreate(t, dir, "pnr update target, origin")
		res, _ := pnrUpdate(t, dir, made.ID, "--priority", "1", "--push")
		if !res.Committed || len(res.Warnings) != 0 || !wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act update --json --priority 1 --push with origin: committed %v, warnings %v, origin has the commit %v; want true, none, true", res.Committed, res.Warnings, wfxOriginHasHead(t, dir, bare))
		}
	})
}

// TestDocClaim_Spec_UpdateClaimPushNoRemoteWarns asserts the `--claim`
// bullet of the `act update` section: a won claim with `--push` in a tracker
// with no `origin` remote exits 0 with the issue claimed and the warning
// `push_no_remote` (act-f4fd34: it used to exit 1 `claim_failed` with the
// issue already in_progress).
func TestDocClaim_Spec_UpdateClaimPushNoRemoteWarns(t *testing.T) {
	t.Run("no remote", func(t *testing.T) {
		dir := sp6Site(t)
		made, _ := wfxCreate(t, dir, "pnr claim target")
		res, _ := pnrUpdate(t, dir, made.ID, "--claim", "--push")
		if res.Error != "" || !res.Claimed || strings.Join(res.Warnings, ",") != "push_no_remote" {
			t.Errorf("act update --json --claim --push with no remote: error %q, claimed %v, warnings %v; want no error, claimed, exactly [push_no_remote]", res.Error, res.Claimed, res.Warnings)
		}
		if st := sp8Str(sp8Show(t, dir, made.ID)["status"]); st != "in_progress" {
			t.Errorf("after update --claim --push with no remote, show reports status %q, want in_progress", st)
		}
		// A plain claim there has nothing to warn about.
		other, _ := wfxCreate(t, dir, "pnr claim target, no push")
		plain, _ := pnrUpdate(t, dir, other.ID, "--claim")
		if !plain.Claimed || len(plain.Warnings) != 0 {
			t.Errorf("act update --json --claim with no remote: claimed %v, warnings %v; want claimed and none", plain.Claimed, plain.Warnings)
		}
		// Without --json the reason goes to stderr and the claim still lands.
		third, _ := wfxCreate(t, dir, "pnr claim target, human")
		out, stderr := pass5Run(t, dir, 0, "", "update", third.ID, "--claim", "--push")
		if !strings.HasPrefix(out, "Claimed ") || !strings.Contains(stderr, "act update: warning: --push: no origin remote, nothing was pushed") {
			t.Errorf("act update --claim --push with no remote: stdout %q, stderr %q; want the Claimed line and the nothing-was-pushed warning", out, stderr)
		}
	})

	t.Run("origin", func(t *testing.T) {
		dir := sp6Site(t)
		bare := wfxAddBareOrigin(t, dir)
		made, _ := wfxCreate(t, dir, "pnr claim target, origin")
		res, _ := pnrUpdate(t, dir, made.ID, "--claim", "--push")
		if !res.Claimed || len(res.Warnings) != 0 || !wfxOriginHasHead(t, dir, bare) {
			t.Errorf("act update --json --claim --push with origin: claimed %v, warnings %v, origin has the commit %v; want true, none, true", res.Claimed, res.Warnings, wfxOriginHasHead(t, dir, bare))
		}
	})
}

// TestDocClaim_Spec_IsolatedWritesDoNotPush asserts the `--isolated` bullet
// of the universal write flags: with `origin` configured, an isolated write
// commits locally, pushes nothing, and queues nothing in
// `.act/.pending-pushes` (act-f4fd34: create and update --isolated used to
// publish to origin like any other write).
func TestDocClaim_Spec_IsolatedWritesDoNotPush(t *testing.T) {
	dir := sp6Site(t)
	bare := wfxAddBareOrigin(t, dir)
	seed, _ := wfxCreate(t, dir, "pnr isolated seed") // origin gets the branch
	if !wfxOriginHasHead(t, dir, bare) {
		t.Fatalf("setup: a plain create with origin did not publish")
	}

	made, _ := wfxCreate(t, dir, "--isolated", "pnr isolated create")
	if !made.Committed || *made.Pushed {
		t.Errorf("act create --json --isolated with origin: committed %v, pushed %v; want true, false", made.Committed, *made.Pushed)
	}
	if wfxOriginHasHead(t, dir, bare) {
		t.Errorf("act create --isolated with origin: the op commit is on origin; isolated forbids the push")
	}
	if q := pnrPendingPushes(t, dir); q != "" {
		t.Errorf("act create --isolated queued a deferred push; want none:\n%s", q)
	}

	upd, _ := pnrUpdate(t, dir, seed.ID, "--priority", "1", "--isolated")
	if !upd.Committed {
		t.Errorf("act update --json --priority 1 --isolated: committed false, want true")
	}
	if wfxOriginHasHead(t, dir, bare) {
		t.Errorf("act update --isolated with origin: the op commit is on origin; isolated forbids the push")
	}
	if q := pnrPendingPushes(t, dir); q != "" {
		t.Errorf("act update --isolated queued a deferred push; want none:\n%s", q)
	}
	if p := string(sp8Show(t, dir, seed.ID)["priority"]); p != "1" {
		t.Errorf("after update --priority 1 --isolated, show reports priority %s, want 1", p)
	}

	// The next ordinary write publishes everything isolated held back.
	wfxCreate(t, dir, "pnr after isolated")
	if !wfxOriginHasHead(t, dir, bare) {
		t.Errorf("a plain create after isolated writes did not bring origin up to HEAD")
	}
}
