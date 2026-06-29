package gitops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/testfixtures"
)

// TestFetchAndRebase_NoOpOnUpToDate verifies the AC: "FetchAndRebase
// called against a remote with no diverging history is a no-op (clean
// exit, no rebase invoked)."
//
// Strategy: clone the bare remote fresh; both sides identical; rebase
// should not actually invoke (we verify by checking the ORIG_HEAD ref
// — rebase touches it but a clean no-op does not).
func TestFetchAndRebase_NoOpOnUpToDate(t *testing.T) {
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	// Clear any pre-existing ORIG_HEAD so we can detect rebase invocation.
	origHead := filepath.Join(work, ".git", "ORIG_HEAD")
	_ = os.Remove(origHead)

	g := NewGitOps(work)
	if err := g.FetchAndRebase("main"); err != nil {
		t.Fatalf("FetchAndRebase no-op: unexpected error: %v", err)
	}
	if _, err := os.Stat(origHead); err == nil {
		t.Fatalf("FetchAndRebase invoked rebase on up-to-date branch (ORIG_HEAD present)")
	}
}

// TestFetchAndRebase_HappyPath simulates a diverging history where
// origin has a new commit beyond the local clone. FetchAndRebase should
// pull origin's commit in and replay local on top.
func TestFetchAndRebase_HappyPath(t *testing.T) {
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	// Remote advances by one commit.
	remote.AdvanceCommits(1)
	// Local makes its own commit.
	writeFile(t, filepath.Join(work, "local.txt"), "local\n")
	runGit(t, work, "add", "local.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "local")

	g := NewGitOps(work)
	if err := g.FetchAndRebase("main"); err != nil {
		t.Fatalf("FetchAndRebase: %v", err)
	}
	// Local HEAD should now have the remote commit as an ancestor.
	out := runGit(t, work, "log", "--format=%s", "main", "-3")
	if !strings.Contains(out, "local") {
		t.Fatalf("expected local commit replayed, log=\n%s", out)
	}
	// Verify rev-list count >= 3 (seed + advance + local).
	count := strings.TrimSpace(runGit(t, work, "rev-list", "--count", "HEAD"))
	if count != "3" {
		t.Fatalf("expected 3 commits after rebase, got %q", count)
	}
}

// TestFetchAndRebase_ShallowSucceedsOnSimpleAdvance exercises the
// rebase-against-shallow path in the case where origin advances by a
// commit that IS reachable through the shallow boundary. The rebase
// succeeds without the --unshallow fallback, but the helper completes
// cleanly — the test asserts the helper doesn't error out spuriously
// when shallow is present but recovery isn't needed.
//
// (The full "--unshallow recovery" path is exercised in
// push_retry_test.go's TestPushWithRetry_Shallow_PlusContention_*
// case, which drives the scenario where the rebase repeatedly hits
// the shallow boundary across attempts.)
func TestFetchAndRebase_ShallowSucceedsOnSimpleAdvance(t *testing.T) {
	remote := testfixtures.NewBareRemote(t)
	// Advance the remote to give some depth.
	remote.AdvanceCommits(3)
	// Shallow clone at depth=1.
	work := remote.InitShallow(1)

	// Local commit on the shallow clone.
	writeFile(t, filepath.Join(work, "shallow-local.txt"), "x\n")
	runGit(t, work, "add", "shallow-local.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "shallow-local")

	// Advance remote further so a non-fast-forward exists.
	remote.AdvanceCommits(1)

	g := NewGitOps(work)
	if err := g.FetchAndRebase("main"); err != nil {
		t.Fatalf("FetchAndRebase shallow simple advance: %v", err)
	}
}

// TestFetchAndRebase_RebaseConflict verifies that a true merge conflict
// surfaces ErrRebaseConflict and the working tree is cleaned up
// (rebase aborted).
func TestFetchAndRebase_RebaseConflict(t *testing.T) {
	remote := testfixtures.NewBareRemote(t)

	// Two clones; both make a divergent edit to the same file.
	clone1 := cloneAndConfigure(t, remote.URL)
	clone2 := cloneAndConfigure(t, remote.URL)

	writeFile(t, filepath.Join(clone1, "conflict.txt"), "from-clone1\n")
	runGit(t, clone1, "add", "conflict.txt")
	runGit(t, clone1, "commit", "-q", "--no-verify", "-m", "c1")
	runGit(t, clone1, "push", "-q", "origin", "main")

	writeFile(t, filepath.Join(clone2, "conflict.txt"), "from-clone2\n")
	runGit(t, clone2, "add", "conflict.txt")
	runGit(t, clone2, "commit", "-q", "--no-verify", "-m", "c2")

	g := NewGitOps(clone2)
	err := g.FetchAndRebase("main")
	if err == nil {
		t.Fatalf("FetchAndRebase: expected conflict error, got nil")
	}
	if !errors.Is(err, ErrRebaseConflict) {
		t.Fatalf("FetchAndRebase: expected ErrRebaseConflict, got %v", err)
	}
	// Verify the rebase was aborted — .git/rebase-merge or rebase-apply
	// should not be present.
	if _, errStat := os.Stat(filepath.Join(clone2, ".git", "rebase-merge")); errStat == nil {
		t.Fatalf("rebase-merge dir still present after abort")
	}
	if _, errStat := os.Stat(filepath.Join(clone2, ".git", "rebase-apply")); errStat == nil {
		t.Fatalf("rebase-apply dir still present after abort")
	}
}

// TestFetchAndRebase_EmptyBranch verifies the input-guard.
func TestFetchAndRebase_EmptyBranch(t *testing.T) {
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)
	g := NewGitOps(work)
	if err := g.FetchAndRebase(""); err == nil {
		t.Fatalf("FetchAndRebase(\"\"): expected error, got nil")
	}
}

// cloneAndConfigure clones the bare URL to a fresh tempdir and applies
// the test user/commit config. Returns the working dir.
func cloneAndConfigure(t *testing.T, url string) string {
	t.Helper()
	work := t.TempDir()
	runGit(t, "", "clone", "-q", url, work)
	runGit(t, work, "config", "user.email", "test@example.com")
	runGit(t, work, "config", "user.name", "Test")
	runGit(t, work, "config", "commit.gpgsign", "false")
	return work
}
