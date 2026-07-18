package main

import (
	"strings"
	"testing"
)

// TestDocClaim_StatusOpen_ReleasesClaim pins the `act update` --status flag
// help claim that `--status open` returns an in_progress issue to open and
// releases the claim — the fix for act-bf9e9d, where `--status open` wrote a
// no-op update_field{status:open} op that reported success while the
// projection stayed in_progress, leaving a stale claim unreleasable (it never
// reappeared in `act ready`). Exercised at the subprocess boundary, including
// the re-claim that proves the claim high-water mark was cleared.
func TestDocClaim_StatusOpen_ReleasesClaim(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "status-open release probe")

	if _, stderr, code := runActIn(t, dir, "update", "--claim", id); code != 0 {
		t.Fatalf("claim: exit %d; stderr=%s", code, stderr)
	}
	if out, _, code := runActIn(t, dir, "show", id, "--json"); code != 0 || !strings.Contains(out, "in_progress") {
		t.Fatalf("expected in_progress after claim; exit=%d out=%s", code, out)
	}

	// The reported-success-but-no-effect path: `act update --status open`.
	if _, stderr, code := runActIn(t, dir, "update", "--status", "open", id); code != 0 {
		t.Fatalf("update --status open: exit %d; stderr=%s", code, stderr)
	}
	out, _, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("show after --status open: exit %d", code)
	}
	if !strings.Contains(out, `"status":"open"`) && !strings.Contains(out, `"status": "open"`) {
		t.Errorf("expected status open after --status open; got:\n%s", out)
	}

	// Re-claim must succeed — proves the release cleared the claim high-water
	// mark at the user-visible boundary (the stale-claim-unreleasable bug).
	if _, stderr, code := runActIn(t, dir, "update", "--claim", id); code != 0 {
		t.Fatalf("re-claim after --status open: exit %d; stderr=%s", code, stderr)
	}
	if out, _, _ := runActIn(t, dir, "show", id, "--json"); !strings.Contains(out, "in_progress") {
		t.Errorf("expected in_progress after re-claim; got:\n%s", out)
	}
}

// TestStatusOpen_OnOpenIsNoop: `--status open` on a never-claimed (already
// open) issue succeeds and leaves it open — idempotent at the CLI boundary,
// mirroring --unclaim's no-op semantics.
func TestStatusOpen_OnOpenIsNoop(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "status-open noop probe")
	if _, stderr, code := runActIn(t, dir, "update", "--status", "open", id); code != 0 {
		t.Fatalf("--status open on open: exit %d; stderr=%s", code, stderr)
	}
	if out, _, _ := runActIn(t, dir, "show", id, "--json"); !strings.Contains(out, `"status":"open"`) && !strings.Contains(out, `"status": "open"`) {
		t.Errorf("expected open after no-op --status open; got:\n%s", out)
	}
}

// TestStatusOpen_OnClosedErrors: `--status open` on a closed issue must not
// silently no-op (the sibling of the bf9e9d bug). It exits 2 and points the
// caller to `act reopen`, the real exit from closed.
func TestStatusOpen_OnClosedErrors(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "status-open closed probe")
	if _, stderr, code := runActIn(t, dir, "close", id, "--reason", "done"); code != 0 {
		t.Fatalf("close: exit %d; stderr=%s", code, stderr)
	}
	out, stderr, code := runActIn(t, dir, "update", "--status", "open", id)
	if code != 2 {
		t.Fatalf("--status open on closed: exit %d; want 2; out=%s stderr=%s", code, out, stderr)
	}
	if !strings.Contains(out+stderr, "reopen") {
		t.Errorf("closed-issue error should point to `act reopen`; got out=%s stderr=%s", out, stderr)
	}
	// The issue stays closed — the failed transition changed nothing.
	if o, _, _ := runActIn(t, dir, "show", id, "--json"); !strings.Contains(o, "closed") {
		t.Errorf("expected still closed after rejected --status open; got:\n%s", o)
	}
}
