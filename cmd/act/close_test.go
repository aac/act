package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_CloseReasonCap_OverCapRejected pins the user-visible 500-byte
// cap on `act close --reason` documented in `act help workflow` ("--reason is
// capped at 500 bytes"). The cap must surface at flag-parse time with a clear
// stderr message naming the byte limit, NOT after the op file is written.
// The check runs before findRepoRoot() so it fires in any directory.
//
// This is the over-cap rejection case (1 byte over → exit 2 with cap message).
// See also TestDocClaim_CloseReasonCap_AtCapAccepted for the off-by-one guard.
func TestDocClaim_CloseReasonCap_OverCapRejected(t *testing.T) {
	// 501-byte reason: one byte over the documented 500-byte cap.
	reason := strings.Repeat("x", closeReasonMaxBytes+1)
	dir := t.TempDir()
	// No git init, no .act init — the upfront check fires before any
	// repo discovery, so an empty TempDir is sufficient. This is itself
	// the property under test: validation runs before any I/O.
	_, stderr, code := runActIn(t, dir, "close", "act-deadbeef", "--reason", reason)
	if code != 2 {
		t.Fatalf("exit = %d, want 2 (bad_flag); stderr=%q", code, stderr)
	}
	// Literal cap-naming substring requirements per act-7ecd acceptance:
	// the message must name the byte cap and the actual byte count so
	// the operator knows by how much to shorten.
	wantSubs := []string{
		"act close: --reason",
		fmt.Sprintf("%d-byte cap", closeReasonMaxBytes),
		fmt.Sprintf("got %d bytes", len(reason)),
	}
	for _, want := range wantSubs {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q; got %q", want, stderr)
		}
	}
	// Sanity: no op file could have been written because we don't even
	// have a .act/ directory. The point of the upfront check is to fail
	// before reaching the write path; verify no part of stderr suggests
	// the write attempted to start.
	for _, banned := range []string{"not initialized", "not inside a git", "ops_scan_failed"} {
		if strings.Contains(stderr, banned) {
			t.Errorf("upfront check did not fire — stderr reached deeper path: %q", stderr)
		}
	}
}

// TestDocClaim_CloseReasonCap_AtCapAccepted pins the at-boundary case for the
// 500-byte cap on `act close --reason`. A reason exactly at the cap must pass
// the upfront check and proceed into the command body (the off-by-one guard).
// The command still fails downstream (no .act/ in the temp dir), but the
// absence of the "byte cap" message in stderr confirms the cap did not reject.
func TestDocClaim_CloseReasonCap_AtCapAccepted(t *testing.T) {
	reason := strings.Repeat("x", closeReasonMaxBytes)
	dir := t.TempDir()
	// Need a git working tree so the command gets past hasGitDir() and
	// proves the reason was accepted. .act/ is still absent, so the
	// command will exit 3 with "not initialized" — that's the signal
	// the upfront check passed.
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	_, stderr, code := runActIn(t, dir, "close", "act-deadbeef", "--reason", reason)
	// We expect a different error path now: the cap should NOT have
	// fired (no "byte cap" mention). The actual exit code depends on
	// which downstream guard catches the missing .act/.
	if strings.Contains(stderr, "byte cap") {
		t.Errorf("at-cap reason rejected by upfront check (off-by-one); stderr=%q", stderr)
	}
	if code == 2 && strings.Contains(stderr, "--reason exceeds") {
		t.Errorf("at-cap reason should not trip bad_flag; got code=%d stderr=%q", code, stderr)
	}
	// Belt-and-braces: make sure something downstream did reject, so
	// this test isn't accidentally green on a totally broken close
	// path.
	if code == 0 {
		t.Errorf("expected downstream failure (no .act/), got code 0; stderr=%q", stderr)
	}
	_ = filepath.Join // keep import set stable for future expansion
}
