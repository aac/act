package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCloseReasonCapValidatedUpfront is the regression test for act-7ecd:
// the 500-byte cap on `act close --reason` must surface at flag-parse
// time with a clear cap-naming stderr message, NOT after the op file is
// written or staged. The check runs before findRepoRoot() so it fires
// in any directory.
func TestCloseReasonCapValidatedUpfront(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
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

// TestCloseReasonAtCapAccepted: a reason exactly at the cap passes the
// upfront check and proceeds into the command body. The command will
// still fail downstream (no .act/ in this temp dir), but we're asserting
// the gate didn't reject — i.e. the off-by-one is correct.
func TestCloseReasonAtCapAccepted(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
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
