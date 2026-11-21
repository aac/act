package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSharedCheckoutForBranch pins the act-94181c classification: a nested
// .act/ in the host repo's main working tree is shared; one in a linked
// worktree is a worker's own copy; a worktree's .act symlinked into the main
// checkout is the main checkout's (shared, named by its real path); and a
// directory git cannot place is treated as shared.
func TestSharedCheckoutForBranch(t *testing.T) {
	host := initRepo(t)
	realHost, err := filepath.EvalSymlinks(host)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(host, ".act"), 0o755); err != nil {
		t.Fatalf("mkdir .act: %v", err)
	}
	if dir, shared := SharedCheckoutForBranch(filepath.Join(host, ".act")); !shared || dir != realHost {
		t.Errorf("main working tree: (%q, %v), want (%q, true)", dir, shared, realHost)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	runGit(t, host, "worktree", "add", "-q", "-b", "wt-host", wt)
	if err := os.MkdirAll(filepath.Join(wt, ".act"), 0o755); err != nil {
		t.Fatalf("mkdir wt/.act: %v", err)
	}
	if dir, shared := SharedCheckoutForBranch(filepath.Join(wt, ".act")); shared {
		t.Errorf("linked worktree copy %s: reported shared, want a worker's own copy", dir)
	}

	wt2 := filepath.Join(t.TempDir(), "wt2")
	runGit(t, host, "worktree", "add", "-q", "-b", "wt-host-2", wt2)
	if err := os.Symlink(filepath.Join(host, ".act"), filepath.Join(wt2, ".act")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if dir, shared := SharedCheckoutForBranch(filepath.Join(wt2, ".act")); !shared || dir != realHost {
		t.Errorf("worktree .act symlinked to the main checkout: (%q, %v), want (%q, true)", dir, shared, realHost)
	}

	plain := t.TempDir()
	if _, shared := SharedCheckoutForBranch(filepath.Join(plain, ".act")); !shared {
		t.Errorf("a directory outside any git repo: reported a worker's own copy, want shared")
	}
}
