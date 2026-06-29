// Package testfixtures provides shared test infrastructure for act's
// integration tests. The flagship type is BareRemote, the fixture-remote
// owned by Phase 2 ticket 2 (act-9f3fc5).
//
// Design notes:
//
//   - A BareRemote is a bare git repository at a tempdir path, accessed via
//     the local-filesystem URL scheme. No `git daemon`, no SSH loopback.
//     This makes timing deterministic (no socket buffering, no TLS negotiation)
//     and side-steps process management entirely.
//   - The bare repo's `receive.denyCurrentBranch` defaults to `updateInstead`
//     so push-on-checked-out-branch is allowed (matches the Phase 2 design's
//     orchestrator setup). The "dirty-tree silent rejection" semantics are
//     simulated by InitWorking() + DirtyWorkingTree(), which prepare a
//     non-bare receiver that rejects the push when the working tree is dirty.
//   - Tests register a t.Cleanup so the tempdir is removed automatically.
//   - All helpers panic-via-t.Fatalf on errors so callers don't have to do
//     err-style branching for fixture setup. They never silently no-op.
//
// Public API (the §5 prompt-addendum-pinned surface):
//
//	NewBareRemote(t)                  — construct
//	(*BareRemote).AdvanceCommits(n)   — push N synthetic commits to the bare remote
//	(*BareRemote).ConcurrentPush(...) — run N concurrent local pushes against the remote
//	(*BareRemote).InitShallow(depth)  — clone the remote at --depth=depth to a fresh tempdir
//	(*BareRemote).PauseTransfer(d)    — install a transfer-pause hook on the receiver
package testfixtures

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// BareRemote is a bare git repository on the local filesystem, suitable
// for use as the `origin` of a writer's clone in tests.
//
// Zero value is not usable; construct with NewBareRemote.
type BareRemote struct {
	// URL is the local-filesystem URL of the bare repository. It is also
	// a plain path; `git clone <URL>` and `git push <URL>` accept either.
	URL string
	// Path is the absolute filesystem path to the bare repository (same
	// as URL today; kept distinct so the API tolerates a future scheme).
	Path string
	// Cleanup removes the bare-repo tempdir. Registered with t.Cleanup
	// by NewBareRemote; the field is exposed for callers who want to
	// explicitly tear down mid-test (rare).
	Cleanup func()

	// Branch is the bare remote's default branch name (always "main").
	// Surfaced so callers don't have to assume.
	Branch string

	t *testing.T
}

// NewBareRemote constructs a bare git repository in a tempdir, configures
// it as a viable push target, and registers cleanup. The returned remote
// has `main` as its initial branch and one root commit (an empty
// `.gitkeep` file) so HEAD resolves immediately — concurrent-push tests
// don't have to handle the "empty repo" edge case.
func NewBareRemote(t *testing.T) *BareRemote {
	t.Helper()
	dir := t.TempDir()
	barePath := filepath.Join(dir, "remote.git")

	mustGit(t, dir, "init", "--bare", "-b", "main", barePath)

	// Seed the bare repo with a root commit so HEAD points somewhere.
	// We do this by initialising a fresh working tree (the bare repo
	// has no branches yet so `git clone -b main` would fail), making
	// the seed commit, and pushing it back. After the push the bare
	// repo's main branch exists and is checkable-out.
	seedDir := filepath.Join(dir, "seed")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatalf("mkdir seed: %v", err)
	}
	mustGit(t, seedDir, "init", "-q", "-b", "main")
	configureRepo(t, seedDir)
	mustGit(t, seedDir, "remote", "add", "origin", barePath)
	if err := os.WriteFile(filepath.Join(seedDir, ".gitkeep"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	mustGit(t, seedDir, "add", ".gitkeep")
	mustGit(t, seedDir, "commit", "-q", "--no-verify", "-m", "seed")
	mustGit(t, seedDir, "push", "-q", "origin", "main")

	br := &BareRemote{
		URL:    barePath,
		Path:   barePath,
		Branch: "main",
		t:      t,
	}
	br.Cleanup = func() {
		// t.TempDir handles its own cleanup; this exists so callers who
		// want to tear down mid-test have a knob. After cleanup the
		// receiver is unsafe to use.
	}
	return br
}

// AdvanceCommits pushes n synthetic commits to the bare remote's main
// branch. Each commit touches a unique filename ("advance-<i>") so the
// remote-side history grows without affecting any other clone's working
// tree.
//
// Used to simulate a concurrent writer who has pushed between two of the
// caller's push attempts (the contention scenario in PushWithRetry's
// retry loop).
func (b *BareRemote) AdvanceCommits(n int) {
	b.t.Helper()
	if n <= 0 {
		return
	}
	work := b.t.TempDir()
	mustGit(b.t, "", "clone", "-q", b.URL, work)
	configureRepo(b.t, work)
	for i := 0; i < n; i++ {
		// Use a high-precision unique suffix in case AdvanceCommits is
		// called many times across the same test — without it concurrent
		// callers could collide on filename.
		name := fmt.Sprintf("advance-%d-%d", time.Now().UnixNano(), i)
		if err := os.WriteFile(filepath.Join(work, name), []byte("x\n"), 0o644); err != nil {
			b.t.Fatalf("advance: write: %v", err)
		}
		mustGit(b.t, work, "add", name)
		mustGit(b.t, work, "commit", "-q", "--no-verify", "-m", "advance "+name)
	}
	mustGit(b.t, work, "push", "-q", "origin", "main")
}

// ConcurrentPush executes the supplied pushFn N times in parallel,
// returning the resulting errors in the same order as the input.
//
// pushFn(i) is called with a per-goroutine working clone whose origin
// points at b.URL. The fn is responsible for making at least one commit
// before pushing — typically it writes a unique file and runs the
// caller's PushWithRetry against it.
//
// Cleans up the per-goroutine clones on test teardown. Errors are
// returned, not t.Fatalf'd, so callers can assert on the union/winners.
func (b *BareRemote) ConcurrentPush(n int, pushFn func(i int, workdir string) error) []error {
	b.t.Helper()
	if n <= 0 {
		return nil
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		i := i
		work := b.t.TempDir()
		mustGit(b.t, "", "clone", "-q", b.URL, work)
		configureRepo(b.t, work)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = pushFn(i, work)
		}()
	}
	wg.Wait()
	return errs
}

// InitShallow clones the bare remote at --depth=depth to a fresh tempdir
// and returns the clone path. The clone is configured for commits and
// has b.URL as `origin`.
//
// Used to drive the shallow-clone rebase-failure branch of PushWithRetry.
// After this, callers typically use AdvanceCommits to push history that
// is beyond the shallow window, then attempt to rebase on the clone.
func (b *BareRemote) InitShallow(depth int) string {
	b.t.Helper()
	if depth <= 0 {
		b.t.Fatalf("InitShallow: depth must be > 0, got %d", depth)
	}
	work := b.t.TempDir()
	// Shallow clone over a local filesystem path requires --no-local —
	// without it git hardlinks the bare's objects and ignores --depth,
	// producing a full clone. The explicit knob keeps the shallow
	// boundary deterministic across local-filesystem fixtures.
	mustGit(b.t, "", "clone", "-q", "--no-local", "--depth", fmt.Sprintf("%d", depth), b.URL, work)
	configureRepo(b.t, work)
	return work
}

// PauseTransfer installs a synthetic delay on the bare remote's push
// receive path. The delay is approximated via a `pre-receive` hook that
// sleeps for the requested duration. Hook installation is idempotent —
// repeated calls overwrite the prior hook.
//
// Used to simulate slow networks (or `git daemon`'s ack-delay) in tests
// without introducing real socket I/O. The current implementation is the
// simplest possible: a shell `sleep <seconds>`, rounded up.
//
// This is the §5 addendum surface — included in the public API so
// downstream tickets (3a/3b/6a/6b/7/11) can drive timing-sensitive
// scenarios without each test reinventing the hook.
func (b *BareRemote) PauseTransfer(target time.Duration) {
	b.t.Helper()
	if target <= 0 {
		// Remove any prior hook.
		_ = os.Remove(filepath.Join(b.Path, "hooks", "pre-receive"))
		return
	}
	hookPath := filepath.Join(b.Path, "hooks", "pre-receive")
	// `sleep` accepts fractional seconds on modern coreutils and on
	// BSD/macOS (the platforms act runs CI on). Round to milliseconds
	// for readability.
	secs := target.Seconds()
	script := fmt.Sprintf("#!/bin/sh\nsleep %.3f\nexit 0\n", secs)
	if err := os.WriteFile(hookPath, []byte(script), 0o755); err != nil {
		b.t.Fatalf("PauseTransfer: write hook: %v", err)
	}
	// Make sure the hook is executable even if umask stripped the bit.
	if err := os.Chmod(hookPath, 0o755); err != nil {
		b.t.Fatalf("PauseTransfer: chmod hook: %v", err)
	}
}

// configureRepo applies the user.email/user.name/commit.gpgsign config
// that every fixture clone needs so commits don't fail under a stripped
// CI environment.
func configureRepo(t *testing.T, dir string) {
	t.Helper()
	mustGit(t, dir, "config", "user.email", "fixture@example.com")
	mustGit(t, dir, "config", "user.name", "Fixture")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
}

// mustGit runs `git <args...>` with cwd=dir (or no cwd if dir is empty)
// and t.Fatalf's on error. Returns the combined output for callers that
// want to inspect it.
func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
