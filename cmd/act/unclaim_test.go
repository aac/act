package main

import (
	"strings"
	"testing"
)

// TestDocClaim_Unclaim_ReleasesToOpen pins the `act help workflow` claim that
// `act update --unclaim <id>` returns an in_progress issue to open and clears
// the assignee — and that the issue can then be re-claimed (the keyClaimHLC
// release, exercised at the subprocess boundary). act-086781.
func TestDocClaim_Unclaim_ReleasesToOpen(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "unclaim probe")

	if _, stderr, code := runActIn(t, dir, "update", "--claim", id); code != 0 {
		t.Fatalf("claim: exit %d; stderr=%s", code, stderr)
	}
	if out, _, code := runActIn(t, dir, "show", id, "--json"); code != 0 || !strings.Contains(out, "in_progress") {
		t.Fatalf("expected in_progress after claim; exit=%d out=%s", code, out)
	}

	if _, stderr, code := runActIn(t, dir, "update", "--unclaim", id); code != 0 {
		t.Fatalf("unclaim: exit %d; stderr=%s", code, stderr)
	}
	out, _, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("show after unclaim: exit %d", code)
	}
	if !strings.Contains(out, `"status":"open"`) && !strings.Contains(out, `"status": "open"`) {
		t.Errorf("expected status open after unclaim; got:\n%s", out)
	}

	// Re-claim must succeed — proves the release cleared the claim high-water
	// mark at the user-visible boundary, not just in the fold unit tests.
	if _, stderr, code := runActIn(t, dir, "update", "--claim", id); code != 0 {
		t.Fatalf("re-claim after unclaim: exit %d; stderr=%s", code, stderr)
	}
	if out, _, _ := runActIn(t, dir, "show", id, "--json"); !strings.Contains(out, "in_progress") {
		t.Errorf("expected in_progress after re-claim; got:\n%s", out)
	}
}

// TestUnclaim_ClaimConflict: --claim and --unclaim together is a usage error.
func TestUnclaim_ClaimConflict(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "conflict probe")
	if _, _, code := runActIn(t, dir, "update", "--claim", "--unclaim", id); code != 2 {
		t.Errorf("--claim --unclaim: exit %d; want 2", code)
	}
}

// TestUnclaim_OnOpenIsNoop: unclaiming a never-claimed issue succeeds (exit 0)
// and leaves it open — idempotent at the CLI boundary.
func TestUnclaim_OnOpenIsNoop(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "noop probe")
	if _, stderr, code := runActIn(t, dir, "update", "--unclaim", id); code != 0 {
		t.Fatalf("unclaim on open: exit %d; stderr=%s", code, stderr)
	}
	if out, _, _ := runActIn(t, dir, "show", id, "--json"); !strings.Contains(out, `"status":"open"`) && !strings.Contains(out, `"status": "open"`) {
		t.Errorf("expected open after no-op unclaim; got:\n%s", out)
	}
}

// TestReopen_AllowsReclaim guards act-e05c3f at the subprocess boundary:
// after close+reopen, the issue must be claimable again (the winnerOnDisk
// release-window fix + the applyReopen keyClaimHLC fix, together).
func TestReopen_AllowsReclaim(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "reopen reclaim probe")
	if _, stderr, code := runActIn(t, dir, "update", "--claim", id); code != 0 {
		t.Fatalf("claim: exit %d; %s", code, stderr)
	}
	if _, stderr, code := runActIn(t, dir, "close", id, "--reason", "done"); code != 0 {
		t.Fatalf("close: exit %d; %s", code, stderr)
	}
	if _, stderr, code := runActIn(t, dir, "reopen", id); code != 0 {
		t.Fatalf("reopen: exit %d; %s", code, stderr)
	}
	if _, stderr, code := runActIn(t, dir, "update", "--claim", id); code != 0 {
		t.Fatalf("re-claim after reopen: exit %d; %s", code, stderr)
	}
	if out, _, _ := runActIn(t, dir, "show", id, "--json"); !strings.Contains(out, "in_progress") {
		t.Errorf("expected in_progress after reopen+reclaim; got:\n%s", out)
	}
}
