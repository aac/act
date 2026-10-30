package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// act-c30acd (docs/claim-identity-design.md §3, §4): a claim is won when
// origin has seen it. A re-claim of a hold this checkout never published
// rebases onto origin before answering "already held", and a claim whose op
// origin has not seen when the command ends is exit 6 claim_unconfirmed,
// not a win.

// u2Claim is the part of `act update --claim --json` these tests read, on
// top of clrClaim.
type u2Claim struct {
	clrClaim
	AlreadyHeld bool `json:"already_held"`
}

// u2RunClaim is clrRunClaim reading already_held as well.
func u2RunClaim(t *testing.T, dir, id string) (u2Claim, string, string, int) {
	t.Helper()
	res, stdout, stderr, code := clrRunClaim(t, dir, id)
	var full u2Claim
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &full); err != nil {
		t.Fatalf("parse the claim result: %v\n%s", err, stdout)
	}
	full.clrClaim = res
	return full, stdout, stderr, code
}

// u2ClaimOpFiles lists the claim op files for id in dir's tracker.
func u2ClaimOpFiles(t *testing.T, dir, id string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, ".act", "ops", id, "*", "*-claim.json"))
	if err != nil {
		t.Fatalf("glob claim ops: %v", err)
	}
	return m
}

// u2Assignee is the issue's assignee as `act show --json --no-fetch` reports it.
func u2Assignee(t *testing.T, dir, id string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "show", id, "--json", "--no-fetch")
	if code != 0 {
		t.Fatalf("act show %s: exit %d\n%s\n%s", id, code, out, stderr)
	}
	var res struct {
		Assignee string `json:"assignee"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &res); err != nil {
		t.Fatalf("parse act show: %v\n%s", err, out)
	}
	return res.Assignee
}

// u2PendingCount is the number of entries in .act/.pending-pushes.
func u2PendingCount(t *testing.T, dir string) int {
	t.Helper()
	s := pnrPendingPushes(t, dir)
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

// u2HeadCommitted reports whether the nested repo's HEAD commit contains
// every claim op file for id — the claim op is committed, not just written.
func u2HeadCommitted(t *testing.T, dir, id string) bool {
	t.Helper()
	files := u2ClaimOpFiles(t, dir, id)
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		rel, err := filepath.Rel(clrActDir(dir), f)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		if _, err := sp7GitOut("-C", clrActDir(dir), "cat-file", "-e", "HEAD:"+rel); err != nil {
			return false
		}
	}
	return true
}

// TestDocClaim_Clr_TwoCheckoutsNeverBothHold: two checkouts of one tracker
// have distinct identities, so the second claimant loses — and keeps losing
// on a re-claim — while the first keeps the hold.
func TestDocClaim_Clr_TwoCheckoutsNeverBothHold(t *testing.T) {
	dir, bare, id := clrSiteWithOrigin(t)
	rival := clrRival(t, bare)

	won, _, _, code := u2RunClaim(t, rival, id)
	if code != 0 || !won.Claimed || won.AlreadyHeld {
		t.Fatalf("B's claim: exit %d claimed=%v already_held=%v, want a fresh win", code, won.Claimed, won.AlreadyHeld)
	}
	lost, stdout, _, code := u2RunClaim(t, dir, id)
	if code != 5 || lost.Claimed || lost.Error != "claim_lost" || lost.Winner != won.Winner {
		t.Fatalf("A's claim: exit %d claimed=%v error=%q winner=%q; want exit 5, claim_lost, winner %q\n%s", code, lost.Claimed, lost.Error, lost.Winner, won.Winner, stdout)
	}
	again, stdout, _, code := u2RunClaim(t, dir, id)
	if code != 5 || again.Claimed || again.Winner != won.Winner {
		t.Errorf("A's re-claim: exit %d claimed=%v winner=%q; want exit 5 and winner %q (B)\n%s", code, again.Claimed, again.Winner, won.Winner, stdout)
	}
	held, stdout, _, code := u2RunClaim(t, rival, id)
	if code != 0 || !held.Claimed || !held.AlreadyHeld {
		t.Errorf("B's re-claim: exit %d claimed=%v already_held=%v; want exit 0, already_held true\n%s", code, held.Claimed, held.AlreadyHeld, stdout)
	}
	if a, b := u2Assignee(t, dir, id), u2Assignee(t, rival, id); a != won.Winner || b != won.Winner {
		t.Errorf("assignee in A = %q, in B = %q; want both %q", a, b, won.Winner)
	}
}

// u2CommitConflictingFile commits clr-conflict.txt with body in dir's
// nested repo, so the two checkouts' next rebase conflicts.
func u2CommitConflictingFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(clrActDir(dir), "clr-conflict.txt"), []byte(body), 0o644); err != nil {
		t.Fatalf("write the conflicting file: %v", err)
	}
	sp6Git(t, "-C", clrActDir(dir), "add", "clr-conflict.txt")
	sp6Git(t, "-C", clrActDir(dir), "commit", "-q", "--no-verify", "-m", "u2: conflicting file")
}

// TestDocClaim_Clr_ReclaimAfterRebaseConflictConsultsOrigin is F1 of the
// design note: a claim whose rebase onto origin conflicted leaves the claim
// op committed locally, the only claim this checkout can see. Re-running
// the claim must not answer "already held" from that: it rebases first, so
// while the conflict stands it fails the same way, and once the conflict is
// resolved it sees the rival's earlier claim and loses.
func TestDocClaim_Clr_ReclaimAfterRebaseConflictConsultsOrigin(t *testing.T) {
	dir, bare, id := clrSiteWithOrigin(t)
	rival := clrRival(t, bare)
	won, _, _, code := u2RunClaim(t, rival, id)
	if code != 0 || !won.Claimed {
		t.Fatalf("B's claim: exit %d claimed=%v, want a win", code, won.Claimed)
	}
	u2CommitConflictingFile(t, rival, "rival\n")
	u2CommitConflictingFile(t, dir, "this checkout\n")
	sp6Git(t, "-C", clrActDir(rival), "push", "-q", "origin", "HEAD")

	first, stdout, _, code := u2RunClaim(t, dir, id)
	if code != 1 || first.Claimed || first.Error != "claim_failed" {
		t.Fatalf("A's claim whose rebase conflicts: exit %d claimed=%v error=%q; want exit 1 claim_failed\n%s", code, first.Claimed, first.Error, stdout)
	}
	opsAfterFirst := len(u2ClaimOpFiles(t, dir, id))

	again, stdout, _, code := u2RunClaim(t, dir, id)
	if code != 1 || again.Claimed || again.Error != "claim_failed" {
		t.Errorf("A's re-claim while the conflict stands: exit %d claimed=%v error=%q; want exit 1 claim_failed again, never a win\n%s", code, again.Claimed, again.Error, stdout)
	}
	if n := len(u2ClaimOpFiles(t, dir, id)); n != opsAfterFirst {
		t.Errorf("A's re-claim wrote a claim op: %d claim ops, was %d", n, opsAfterFirst)
	}

	// Resolve the conflict by hand in A's nested repo: rebase onto origin,
	// keep this checkout's text, continue.
	branch := clrBranch(t, dir)
	if out, err := exec.Command("git", "-C", clrActDir(dir), "pull", "-q", "--rebase", "origin", branch).CombinedOutput(); err == nil {
		t.Fatalf("premise: the manual rebase did not conflict: %s", out)
	}
	if err := os.WriteFile(filepath.Join(clrActDir(dir), "clr-conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	sp6Git(t, "-C", clrActDir(dir), "add", "clr-conflict.txt")
	sp6Git(t, "-C", clrActDir(dir), "-c", "core.editor=true", "rebase", "--continue")

	last, stdout, _, code := u2RunClaim(t, dir, id)
	if code != 5 || last.Claimed || last.Error != "claim_lost" || last.Winner != won.Winner {
		t.Errorf("A's claim after the conflict is resolved: exit %d claimed=%v error=%q winner=%q; want exit 5 claim_lost, winner %q (B)\n%s", code, last.Claimed, last.Error, last.Winner, won.Winner, stdout)
	}
}

// TestDocClaim_Clr_UnreachableOriginClaimIsUnconfirmed: a claim whose fetch
// cannot reach origin is committed and queued but not won — exit 6
// claim_unconfirmed, reason origin_unreachable. Re-running it once origin is
// back confirms it: a loss when a rival's earlier claim was waiting there,
// a hold (already_held, now published) when none was.
func TestDocClaim_Clr_UnreachableOriginClaimIsUnconfirmed(t *testing.T) {
	for _, withRival := range []bool{true, false} {
		name := "no rival"
		if withRival {
			name = "a rival claimed first"
		}
		t.Run(name, func(t *testing.T) {
			dir, bare, id := clrSiteWithOrigin(t)
			var rivalID string
			if withRival {
				rival := clrRival(t, bare)
				won, _, _, code := u2RunClaim(t, rival, id)
				if code != 0 || !won.Claimed {
					t.Fatalf("B's claim: exit %d claimed=%v, want a win", code, won.Claimed)
				}
				rivalID = won.Winner
			}
			dplRepoint(t, dir, "origin", clrMissingOrigin(t))

			res, stdout, stderr, code := u2RunClaim(t, dir, id)
			if code != 6 || res.OK || res.Claimed || res.Error != "claim_unconfirmed" || res.Reason != "origin_unreachable" {
				t.Fatalf("claim with an unreachable origin: exit %d ok=%v claimed=%v error=%q reason=%q; want exit 6, claimed false, claim_unconfirmed, origin_unreachable\n%s",
					code, res.OK, res.Claimed, res.Error, res.Reason, stdout)
			}
			if !res.PushDeferred || res.PushDeferredReason == "" {
				t.Errorf("push_deferred=%v push_deferred_reason=%q; want true and a reason", res.PushDeferred, res.PushDeferredReason)
			}
			if res.Winner != "" {
				t.Errorf("an unconfirmed claim names winner %q; want no winner key", res.Winner)
			}
			if !strings.Contains(stderr, "re-run the claim once origin is reachable to confirm it") || strings.Contains(stderr, "Do NOT re-run") {
				t.Errorf("the deferral warning does not tell a claimant to re-run the claim:\n%s", stderr)
			}
			if !u2HeadCommitted(t, dir, id) {
				t.Errorf("the unconfirmed claim op is not committed in the nested repo")
			}
			if n := u2PendingCount(t, dir); n != 1 {
				t.Errorf(".act/.pending-pushes has %d entries, want 1", n)
			}

			dplRepoint(t, dir, "origin", bare)
			again, stdout, _, code := u2RunClaim(t, dir, id)
			if withRival {
				if code != 5 || again.Claimed || again.Error != "claim_lost" || again.Winner != rivalID {
					t.Errorf("re-claim once origin is back, rival holds: exit %d claimed=%v error=%q winner=%q; want exit 5, winner %q\n%s", code, again.Claimed, again.Error, again.Winner, rivalID, stdout)
				}
				return
			}
			if code != 0 || !again.Claimed || !again.AlreadyHeld {
				t.Errorf("re-claim once origin is back, no rival: exit %d claimed=%v already_held=%v; want exit 0, claimed, already_held\n%s", code, again.Claimed, again.AlreadyHeld, stdout)
			}
			if !wfxOriginHasHead(t, dir, bare) {
				t.Errorf("the confirmed claim's commit is not on origin")
			}
		})
	}
}

// TestDocClaim_Clr_PushFailedClaimIsUnconfirmed: origin is reached but
// refuses the push, so the claim op is committed and queued and the answer
// is exit 6 claim_unconfirmed, reason push_failed. Once origin accepts
// pushes again, re-running the claim publishes the stranded commit and
// answers the hold.
func TestDocClaim_Clr_PushFailedClaimIsUnconfirmed(t *testing.T) {
	dir, bare, id := clrSiteWithOrigin(t)
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'u2: pushes refused' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}

	res, stdout, stderr, code := u2RunClaim(t, dir, id)
	if code != 6 || res.Claimed || res.Error != "claim_unconfirmed" || res.Reason != "push_failed" {
		t.Fatalf("claim whose push is refused: exit %d claimed=%v error=%q reason=%q; want exit 6, claim_unconfirmed, push_failed\n%s", code, res.Claimed, res.Error, res.Reason, stdout)
	}
	if !res.PushDeferred || res.PushDeferredReason == "" {
		t.Errorf("push_deferred=%v push_deferred_reason=%q; want true and a reason", res.PushDeferred, res.PushDeferredReason)
	}
	if !strings.Contains(stderr, "re-run the claim once origin is reachable to confirm it") {
		t.Errorf("the deferral warning does not tell a claimant to re-run the claim:\n%s", stderr)
	}
	if !u2HeadCommitted(t, dir, id) {
		t.Errorf("the unconfirmed claim op is not committed in the nested repo")
	}
	if n := u2PendingCount(t, dir); n != 1 {
		t.Errorf(".act/.pending-pushes has %d entries, want 1", n)
	}
	if wfxOriginHasHead(t, dir, bare) {
		t.Fatalf("premise: the refused push reached origin")
	}
	ops := len(u2ClaimOpFiles(t, dir, id))

	if err := os.Remove(hook); err != nil {
		t.Fatalf("remove hook: %v", err)
	}
	again, stdout, _, code := u2RunClaim(t, dir, id)
	if code != 0 || !again.Claimed || !again.AlreadyHeld {
		t.Errorf("re-claim once origin accepts pushes: exit %d claimed=%v already_held=%v; want exit 0, already_held\n%s", code, again.Claimed, again.AlreadyHeld, stdout)
	}
	if !wfxOriginHasHead(t, dir, bare) {
		t.Errorf("the confirmed claim's commit is not on origin")
	}
	if n := len(u2ClaimOpFiles(t, dir, id)); n != ops {
		t.Errorf("the re-claim wrote a claim op: %d claim ops, was %d", n, ops)
	}
}
