package main

import (
	"encoding/json"
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

// TestDocClaim_StaleLock_RemedyConfirmsBeforeRemove pins the README and
// `act help errors` claim (act-94bbea) that the stale_git_lock recovery
// confirms nothing still owns the lock BEFORE removing it: with several act
// processes in one checkout, a bare "rm the lock" can delete a lock a live
// git process holds. Asserted on every surface an agent reads the remedy
// from: the write envelope's details.remedy, doctor's human finding, the
// `act help errors` text, and the README prose ahead of the runbook block.
func TestDocClaim_StaleLock_RemedyConfirmsBeforeRemove(t *testing.T) {
	const check = "pgrep -fl '(^|/)git( |$)'"
	confirmedFirst := func(surface, text string) {
		t.Helper()
		ci, ri := strings.Index(text, check), strings.Index(text, "rm -f")
		if ci < 0 || !strings.Contains(text, ".act/.write.lock") {
			t.Errorf("%s: missing the confirm step (%q naming .act/.write.lock): %s", surface, check, text)
			return
		}
		if ri >= 0 && ci > ri {
			t.Errorf("%s: the confirm step comes after rm -f: %s", surface, text)
		}
	}

	dir := blocksSite(t)
	lockPath := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant index.lock: %v", err)
	}
	out, _, code := runActIn(t, dir, "create", "wedged", "--json")
	if code == 0 {
		t.Fatalf("create with stale index.lock: want non-zero exit; out=%s", out)
	}
	var env struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Error != "stale_git_lock" {
		t.Fatalf("envelope: err=%v out=%s", err, out)
	}
	remedy, _ := env.Details["remedy"].(string)
	confirmedFirst("stale_git_lock details.remedy", remedy)

	_, doctorStderr, _ := runActIn(t, dir, "doctor", "--check", "stale-git-lock")
	confirmedFirst("doctor stale-git-lock finding", doctorStderr)

	help, _, hcode := runActIn(t, dir, "help", "errors")
	if hcode != 0 {
		t.Fatalf("act help errors: exit %d", hcode)
	}
	if !strings.Contains(help, "Confirm the lock is really stale before removing it.") {
		t.Errorf("act help errors lacks the confirm-before-remove paragraph")
	}
	confirmedFirst("act help errors", help)

	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readme := string(raw)
	i := strings.Index(readme, "## If a write is interrupted")
	if i < 0 {
		t.Fatal("README has no 'If a write is interrupted' section")
	}
	section := readme[i:]
	if j := strings.Index(section[3:], "\n## "); j >= 0 {
		section = section[:j+3]
	}
	if !strings.Contains(section, "Before you remove anything, confirm nothing still owns the lock.") {
		t.Errorf("README recovery section lacks the confirm-before-remove step")
	}
	confirmedFirst("README 'If a write is interrupted'", section)
}
