package testfixtures

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Test_API_Surface is the §5 addendum no-op compile-test. It references
// every documented public symbol of internal/testfixtures/remote.go so
// downstream tickets (1b/6a/6b/7/11) get a compile-time signal if a
// symbol name drifts.
//
// The test passes if the package builds and the references resolve;
// runtime behavior is intentionally minimal.
func Test_API_Surface(t *testing.T) {
	r := NewBareRemote(t)
	if r == nil {
		t.Fatalf("NewBareRemote returned nil")
	}
	if r.URL == "" || r.Path == "" || r.Branch != "main" || r.Cleanup == nil {
		t.Fatalf("BareRemote zero-value: URL=%q Path=%q Branch=%q Cleanup=%v",
			r.URL, r.Path, r.Branch, r.Cleanup != nil)
	}
	// Each helper is invoked with the smallest possible "do nothing"
	// argument — n=0, depth=1, target=0 — so the assertions run
	// quickly while still proving the symbols exist at link time.
	r.AdvanceCommits(0)
	_ = r.ConcurrentPush(0, func(i int, workdir string) error { return nil })
	shallowDir := r.InitShallow(1)
	if shallowDir == "" {
		t.Fatalf("InitShallow returned empty dir")
	}
	r.PauseTransfer(0)
	r.Cleanup()
}

// TestNewBareRemote_HasSeedCommit verifies the seed commit is present on
// `main` so callers can clone and rebase against a non-empty history.
func TestNewBareRemote_HasSeedCommit(t *testing.T) {
	r := NewBareRemote(t)
	// `git log` on the bare repo's main should return one commit.
	out := mustGit(t, r.Path, "log", "--format=%s", "main")
	if !strings.Contains(out, "seed") {
		t.Fatalf("expected seed commit on main, got %q", out)
	}
}

// TestAdvanceCommits_GrowsRemoteHistory pushes 3 synthetic commits and
// verifies the remote-side log has them.
func TestAdvanceCommits_GrowsRemoteHistory(t *testing.T) {
	r := NewBareRemote(t)
	r.AdvanceCommits(3)
	out := mustGit(t, r.Path, "rev-list", "--count", "main")
	count := strings.TrimSpace(out)
	if count != "4" { // seed + 3
		t.Fatalf("expected 4 commits on main (seed + 3), got %q", count)
	}
}

// TestConcurrentPush_AllSucceed drives two goroutines pushing through
// the bare remote and asserts both eventually land. Each goroutine pushes
// to a distinct branch to avoid contention — the contention test lives in
// internal/gitops/push_retry_test.go because it consumes PushWithRetry.
func TestConcurrentPush_AllSucceed(t *testing.T) {
	r := NewBareRemote(t)
	errs := r.ConcurrentPush(2, func(i int, workdir string) error {
		fname := fmt.Sprintf("concur-%d", i)
		if err := os.WriteFile(filepath.Join(workdir, fname), []byte("x\n"), 0o644); err != nil {
			return err
		}
		cmd := exec.Command("git", "add", fname)
		cmd.Dir = workdir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git add: %v\n%s", err, out)
		}
		cmd = exec.Command("git", "commit", "-q", "--no-verify", "-m", "concur "+fname)
		cmd.Dir = workdir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git commit: %v\n%s", err, out)
		}
		// Push to a per-goroutine branch — pure concurrency check, not
		// contention. The contention test lives one package over.
		cmd = exec.Command("git", "push", "-q", "origin",
			fmt.Sprintf("HEAD:concur-branch-%d", i))
		cmd.Dir = workdir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git push: %v\n%s", err, out)
		}
		return nil
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("ConcurrentPush[%d]: %v", i, err)
		}
	}
	// Verify both branches landed on the remote.
	for i := 0; i < 2; i++ {
		branch := fmt.Sprintf("concur-branch-%d", i)
		mustGit(t, r.Path, "rev-parse", branch)
	}
}

// TestInitShallow_HasDepth1 asserts the shallow clone has exactly one
// commit (depth=1) so the shallow-rebase failure path in push_retry can
// be exercised cleanly.
func TestInitShallow_HasDepth1(t *testing.T) {
	r := NewBareRemote(t)
	r.AdvanceCommits(2) // remote now has 3 commits total (seed + 2)
	clone := r.InitShallow(1)
	out := mustGit(t, clone, "rev-list", "--count", "HEAD")
	count := strings.TrimSpace(out)
	if count != "1" {
		t.Fatalf("expected shallow clone to have 1 commit, got %q", count)
	}
}

// TestPauseTransfer_DelaysPush asserts the installed pre-receive hook
// adds wall-clock delay to a push. We use a small target (200ms) so the
// test is fast and the inequality margin is large enough to absorb
// process-spawn jitter.
func TestPauseTransfer_DelaysPush(t *testing.T) {
	if testing.Short() {
		t.Skip("PauseTransfer test sleeps; skipped under -short")
	}
	r := NewBareRemote(t)
	r.PauseTransfer(200 * time.Millisecond)

	work := t.TempDir()
	mustGit(t, "", "clone", "-q", r.URL, work)
	configureRepo(t, work)
	if err := os.WriteFile(filepath.Join(work, "pause.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, work, "add", "pause.txt")
	mustGit(t, work, "commit", "-q", "--no-verify", "-m", "pause")

	start := time.Now()
	mustGit(t, work, "push", "-q", "origin", "main")
	elapsed := time.Since(start)
	if elapsed < 150*time.Millisecond {
		t.Fatalf("PauseTransfer(200ms): push completed in %v, expected ≥150ms", elapsed)
	}
}
