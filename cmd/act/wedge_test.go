package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_StaleLock_WedgesWrites pins the README "If a write is
// interrupted" claim that a stale git lock file in .act/.git/ makes every
// act write fail until the lock is removed (act-8fe6eb / act-aef518).
// Exercised at the subprocess boundary for both lock files git leaves in
// practice: index.lock (blocks the stage step) and HEAD.lock (blocks the
// ref lock at commit).
func TestDocClaim_StaleLock_WedgesWrites(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "pre-wedge issue") // sanity: writes work

	for _, lock := range []string{"index.lock", "HEAD.lock"} {
		lockPath := filepath.Join(dir, ".act", ".git", lock)
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatalf("plant %s: %v", lock, err)
		}
		out, stderr, code := runActIn(t, dir, "create", "wedged by "+lock)
		if code == 0 {
			t.Fatalf("create with stale %s: want non-zero exit, got 0; out=%s", lock, out)
		}
		// The failure must name the lock file — that's the only thread a
		// wedged agent can pull (git's stderr carries the remedy).
		if !strings.Contains(out+stderr, lock) {
			t.Errorf("create failure with stale %s does not name the lock; out=%s stderr=%s", lock, out, stderr)
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

// TestDocClaim_StaleLock_OpSurvivesAndRecovers pins the README claims that
// nothing is lost under a stale lock — the failed write's op file is already
// on disk — and that the documented recovery sequence (remove the lock,
// commit the stranded ops, `act doctor --fix`) restores the tracker,
// including the wedged write's issue, at the subprocess boundary.
func TestDocClaim_StaleLock_OpSurvivesAndRecovers(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "healthy issue")

	opsDir := filepath.Join(dir, ".act", "ops")
	before := countOpsIssueDirs(t, opsDir)

	lockPath := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant index.lock: %v", err)
	}
	if _, _, code := runActIn(t, dir, "create", "wedged issue"); code == 0 {
		t.Fatal("create under stale lock: want non-zero exit, got 0")
	}
	if after := countOpsIssueDirs(t, opsDir); after != before+1 {
		t.Fatalf("failed write's op file not on disk: %d -> %d issue dirs under ops/", before, after)
	}

	// The documented recovery sequence, verbatim from the README.
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove index.lock: %v", err)
	}
	actRepo := filepath.Join(dir, ".act")
	for _, args := range [][]string{
		{"-C", actRepo, "add", "ops"},
		{"-C", actRepo, "-c", "user.email=test@example.com", "-c", "user.name=Test",
			"commit", "-m", "recover ops stranded by a stale lock"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if _, stderr, code := runActIn(t, dir, "doctor", "--fix"); code != 0 {
		t.Fatalf("doctor --fix after recovery: exit %d; stderr=%s", code, stderr)
	}

	// The wedged write's issue is present, and new writes work.
	out, _, code := runActIn(t, dir, "list")
	if code != 0 || !strings.Contains(out, "wedged issue") {
		t.Errorf("recovered tracker is missing the wedged issue; exit=%d out=%s", code, out)
	}
	createBlocksIssue(t, dir, "post-recovery issue")
}

// countOpsIssueDirs counts per-issue directories under .act/ops/. A missing
// ops/ dir counts as zero (nothing written yet).
func countOpsIssueDirs(t *testing.T, opsDir string) int {
	t.Helper()
	entries, err := os.ReadDir(opsDir)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s: %v", opsDir, err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			n++
		}
	}
	return n
}
