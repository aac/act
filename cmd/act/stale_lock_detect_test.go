package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_StaleLock_StructuredWriteError pins the README claim that a
// write failing on a stale git lock reports a structured `stale_git_lock`
// error naming the lock file and the recovery sequence — not git's stderr
// buried in a generic write_failed (act-8fe6eb). Exercised at the subprocess
// boundary for both locks act's auto-commit can strand: index.lock (the stage
// step) and HEAD.lock (the ref update at commit).
func TestDocClaim_StaleLock_StructuredWriteError(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "pre-wedge issue") // sanity: writes work

	for _, lock := range []string{"index.lock", "HEAD.lock"} {
		lockPath := filepath.Join(dir, ".act", ".git", lock)
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatalf("plant %s: %v", lock, err)
		}
		out, _, code := runActIn(t, dir, "create", "wedged by "+lock, "--json")
		if code == 0 {
			t.Fatalf("create with stale %s: want non-zero exit, got 0; out=%s", lock, out)
		}
		if !strings.Contains(out, `"stale_git_lock"`) {
			t.Errorf("create failure with stale %s not classified as stale_git_lock; out=%s", lock, out)
		}
		// The structured envelope must name the lock file and carry the remedy.
		for _, want := range []string{".act/.git/" + lock, "act doctor --fix"} {
			if !strings.Contains(out, want) {
				t.Errorf("stale_git_lock envelope for %s missing %q; out=%s", lock, want, out)
			}
		}
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("remove %s: %v", lock, err)
		}
	}

	// Locks removed: writes work again.
	if out, stderr, code := runActIn(t, dir, "create", "post-wedge issue"); code != 0 {
		t.Fatalf("create after lock removal: exit %d; out=%s stderr=%s", code, out, stderr)
	}
}

// TestDocClaim_StaleLock_DoctorDetects pins the README claim that `act doctor`
// detects a lingering stale lock as an error finding (act-8fe6eb). doctor is
// read-only (fold + index), so it runs while the tracker is wedged — exactly
// when an agent reaches for it. Exercised at the subprocess boundary for both
// lock files via the targeted `--check stale-git-lock`, and confirms a clean
// tracker produces no such finding.
func TestDocClaim_StaleLock_DoctorDetects(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "healthy issue")

	// Clean tracker: no stale-git-lock finding (the check name only appears
	// inside a Finding object, so its absence proves the clean run).
	if out, _, code := runActIn(t, dir, "doctor", "--check", "stale-git-lock", "--json"); code != 0 || strings.Contains(out, "stale-git-lock") {
		t.Fatalf("clean doctor should be exit 0 with no stale-git-lock finding; exit=%d out=%s", code, out)
	}

	for _, lock := range []string{"index.lock", "HEAD.lock"} {
		lockPath := filepath.Join(dir, ".act", ".git", lock)
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatalf("plant %s: %v", lock, err)
		}
		out, _, code := runActIn(t, dir, "doctor", "--check", "stale-git-lock", "--json")
		if code != 1 {
			t.Fatalf("doctor --check stale-git-lock with %s planted: exit %d; want 1; out=%s", lock, code, out)
		}
		for _, want := range []string{`"check":"stale-git-lock"`, `"severity":"error"`, ".act/.git/" + lock} {
			if !strings.Contains(out, want) {
				t.Errorf("doctor finding for %s missing %q; out=%s", lock, want, out)
			}
		}
		// The remedy is also emitted to stderr (human message) for a tailing
		// agent that isn't parsing the bracketed stdout.
		if _, humanStderr, _ := runActIn(t, dir, "doctor", "--check", "stale-git-lock"); !strings.Contains(humanStderr, "act doctor --fix") {
			t.Errorf("doctor stderr for %s should carry the recovery remedy; stderr=%s", lock, humanStderr)
		}
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("remove %s: %v", lock, err)
		}
	}
}
