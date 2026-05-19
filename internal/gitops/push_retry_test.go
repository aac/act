package gitops

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aac/act/internal/testfixtures"
)

// noSleep is a Sleep replacement that records calls but never blocks.
// Used by every test in this file so the retry loop runs at full speed.
func noSleep(d time.Duration) {}

// TestPushWithRetry_HappyPath: clean push with no contention succeeds
// on the first attempt.
func TestPushWithRetry_HappyPath(t *testing.T) {
	ResetPushAttemptCounter()
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	writeFile(t, filepath.Join(work, "happy.txt"), "x\n")
	runGit(t, work, "add", "happy.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "happy")

	g := NewGitOps(work)
	if err := g.PushWithRetry("main", PushOpts{Sleep: noSleep}); err != nil {
		t.Fatalf("PushWithRetry happy: %v", err)
	}
	// Verify commit landed on the remote.
	out := runGit(t, remote.Path, "log", "-1", "--format=%s", "main")
	if !strings.Contains(out, "happy") {
		t.Fatalf("expected happy commit on remote, got %q", out)
	}
}

// TestPushWithRetry_NonFastForward_Recovers: a peer advances the
// remote between our commit and our push; PushWithRetry's inner
// fetch-rebase loop recovers and the push lands.
func TestPushWithRetry_NonFastForward_Recovers(t *testing.T) {
	ResetPushAttemptCounter()
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	// Local commit.
	writeFile(t, filepath.Join(work, "ours.txt"), "ours\n")
	runGit(t, work, "add", "ours.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "ours")

	// Peer races ahead.
	remote.AdvanceCommits(1)

	g := NewGitOps(work)
	if err := g.PushWithRetry("main", PushOpts{Sleep: noSleep}); err != nil {
		t.Fatalf("PushWithRetry non-ff: %v", err)
	}
	// Both commits should be visible on the remote.
	out := runGit(t, remote.Path, "log", "--format=%s", "main")
	if !strings.Contains(out, "ours") {
		t.Fatalf("expected ours commit on remote, got\n%s", out)
	}
}

// TestPushWithRetry_TwoParallelProcesses_BothSucceed: the headline AC.
// Two writers pushing to the same bare remote both eventually land,
// neither loses commits. Each writer creates its own op file under
// .act/ops/ and we verify the union after the dust settles.
func TestPushWithRetry_TwoParallelProcesses_BothSucceed(t *testing.T) {
	ResetPushAttemptCounter()
	remote := testfixtures.NewBareRemote(t)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			work := t.TempDir()
			runGit(t, "", "clone", "-q", remote.URL, work)
			runGit(t, work, "config", "user.email", fmt.Sprintf("w%d@example.com", i))
			runGit(t, work, "config", "user.name", fmt.Sprintf("w%d", i))
			runGit(t, work, "config", "commit.gpgsign", "false")

			// Each writer creates a unique op file under .act/ops/.
			opPath := filepath.Join(work, ".act", "ops", fmt.Sprintf("op-%d.json", i))
			if err := os.MkdirAll(filepath.Dir(opPath), 0o755); err != nil {
				errs[i] = err
				return
			}
			if err := os.WriteFile(opPath, []byte(fmt.Sprintf("{\"writer\":%d}\n", i)), 0o644); err != nil {
				errs[i] = err
				return
			}
			runGit(t, work, "add", ".act/ops/")
			runGit(t, work, "commit", "-q", "--no-verify", "-m", fmt.Sprintf("op-%d", i))

			g := NewGitOps(work)
			errs[i] = g.PushWithRetry("main", PushOpts{Sleep: noSleep})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("writer %d: %v", i, err)
		}
	}
	// Inspect the remote's HEAD tree: both op files must be present.
	for i := 0; i < 2; i++ {
		expected := fmt.Sprintf(".act/ops/op-%d.json", i)
		out := runGit(t, remote.Path, "ls-tree", "-r", "--name-only", "main")
		if !strings.Contains(out, expected) {
			t.Errorf("expected %s in remote tree, got:\n%s", expected, out)
		}
	}
}

// TestPushWithRetry_SilentRejection_RecoversWithinCap: the
// ACT_TEST_FAIL_PUSH_AFTER fault-injection hook makes the FIRST push
// look like a silent rejection. The reachability check catches it, the
// helper fetch-rebases and retries; the second attempt succeeds.
func TestPushWithRetry_SilentRejection_RecoversWithinCap(t *testing.T) {
	ResetPushAttemptCounter()
	t.Setenv(envFailPushAfter, "1")
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	writeFile(t, filepath.Join(work, "silent.txt"), "x\n")
	runGit(t, work, "add", "silent.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "silent")

	g := NewGitOps(work)
	if err := g.PushWithRetry("main", PushOpts{MaxRetries: 3, Sleep: noSleep}); err != nil {
		t.Fatalf("PushWithRetry silent rejection: %v", err)
	}
	out := runGit(t, remote.Path, "log", "-1", "--format=%s", "main")
	if !strings.Contains(out, "silent") {
		t.Fatalf("expected silent commit landed, got %q", out)
	}
}

// TestPushWithRetry_ExhaustionReturnsStructured: repeated contention
// exhausts the retry cap and returns *PushExhaustedError carrying
// RetryCount and an embedded error chain unwrap-able to
// ErrPushExhausted.
//
// We force exhaustion by setting ACT_TEST_FAIL_PUSH_AFTER to a value
// LESS than MaxRetries (e.g. 1) so every retry sees the silent-reject
// AND ALSO racing the remote so it stays ahead. Simpler approach: use
// a contending writer that advances the remote between every attempt.
//
// Actually simplest: advance the remote concurrently in a goroutine
// that keeps writing. We use the Sleep hook to drive the contention
// deterministically — each "sleep" (between retries) lets the goroutine
// land one more commit.
func TestPushWithRetry_ExhaustionReturnsStructured(t *testing.T) {
	ResetPushAttemptCounter()
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	// Local commit.
	writeFile(t, filepath.Join(work, "exhaust.txt"), "x\n")
	runGit(t, work, "add", "exhaust.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "exhaust")

	// Inject a Sleep hook that advances the remote between each retry,
	// guaranteeing the local push is always non-fast-forward.
	contendingSleep := func(d time.Duration) {
		remote.AdvanceCommits(1)
	}

	// Pre-advance remote so the first attempt is also non-ff.
	remote.AdvanceCommits(1)

	g := NewGitOps(work)
	err := g.PushWithRetry("main", PushOpts{MaxRetries: 5, Sleep: contendingSleep})
	if err == nil {
		t.Fatalf("expected exhaustion, got nil")
	}
	if !errors.Is(err, ErrPushExhausted) {
		t.Fatalf("expected ErrPushExhausted, got %v", err)
	}
	var pe *PushExhaustedError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PushExhaustedError via errors.As, got %T", err)
	}
	if pe.RetryCount != 5 {
		t.Fatalf("expected RetryCount=5, got %d", pe.RetryCount)
	}
}

// TestPushWithRetry_Shallow_PlusContention_PlusExhaustion: the synthesis
// S5 case. Shallow clone + force-pushed divergent history that defeats
// the shallow clone's common-ancestor lookup, combined with continued
// contention that prevents the unshallow recovery from settling. Helper
// exhausts cap; returns *PushExhaustedError with
// ShallowUnshallowAttempted=true and RetryCount=5.
//
// Strategy:
//  1. Seed the bare remote with several commits (gives the shallow
//     clone a small depth=1 view).
//  2. Shallow clone at depth=1.
//  3. Force-push a DIVERGENT history to the bare remote (a fresh
//     commit chain rooted at a different commit than the shallow
//     clone has). This makes rebase fail with "unable to find common
//     ancestor" — the helper's shallow-recovery branch triggers.
//  4. After --unshallow, advance the remote again on every Sleep so
//     the recovery never finds a stable target. Helper exhausts cap.
func TestPushWithRetry_Shallow_PlusContention_PlusExhaustion(t *testing.T) {
	ResetPushAttemptCounter()
	ResetShallowFaultCounter()
	remote := testfixtures.NewBareRemote(t)
	// Build a depth>1 remote so the shallow clone has a non-trivial gap.
	remote.AdvanceCommits(2)
	work := remote.InitShallow(1)

	// Local commit on the shallow clone — gives us something to push.
	writeFile(t, filepath.Join(work, "shallow-exhaust.txt"), "x\n")
	runGit(t, work, "add", "shallow-exhaust.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "shallow-exhaust")

	// Pre-advance remote so first push is non-fast-forward.
	remote.AdvanceCommits(1)

	// Force the first rebase attempt to look like a shallow failure
	// via the test-only env hook. The helper's recovery branch
	// triggers --unshallow once and sets ShallowUnshallowAttempted on
	// the eventual exhaustion error.
	t.Setenv(envForceShallowRebase, "1")

	// Each retry sleep advances the remote — guarantees every push
	// after the first is also non-fast-forward, eating through the
	// retry cap.
	contendingSleep := func(d time.Duration) {
		remote.AdvanceCommits(1)
	}

	g := NewGitOps(work)
	err := g.PushWithRetry("main", PushOpts{MaxRetries: 5, Sleep: contendingSleep})
	if err == nil {
		t.Fatalf("expected exhaustion, got nil")
	}
	if !errors.Is(err, ErrPushExhausted) {
		t.Fatalf("expected ErrPushExhausted, got %v", err)
	}
	var pe *PushExhaustedError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PushExhaustedError, got %T", err)
	}
	if pe.RetryCount != 5 {
		t.Fatalf("RetryCount: want 5, got %d", pe.RetryCount)
	}
	// ShallowUnshallowAttempted MUST be true: the helper must have
	// hit the shallow boundary in at least one round and invoked the
	// --unshallow recovery branch.
	if !pe.ShallowUnshallowAttempted {
		t.Errorf("ShallowUnshallowAttempted: want true, got false")
	}
}

// TestPushWithRetry_EmptyBranch verifies the input-guard.
func TestPushWithRetry_EmptyBranch(t *testing.T) {
	ResetPushAttemptCounter()
	g := NewGitOps(t.TempDir())
	if err := g.PushWithRetry("", PushOpts{Sleep: noSleep}); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

// TestBackoffFor verifies the backoff math: starts at base, doubles
// each retry, caps at cap.
func TestBackoffFor(t *testing.T) {
	base := 100 * time.Millisecond
	cap := time.Second
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1000 * time.Millisecond,
		1000 * time.Millisecond,
	}
	for i, w := range want {
		got := backoffFor(base, cap, i)
		if got != w {
			t.Errorf("backoffFor(%d): want %v, got %v", i, w, got)
		}
	}
}
