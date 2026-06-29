package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runOrFatal is a thin wrapper over exec.Command that fails the test on
// non-zero exit. Shared by the pre-commit-hook tests below.
func runOrFatal(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
}

// TestDocClaim_PreCommitHook_PermitsStagedDeletions exercises the hook's
// user-visible boundary: a normal `git commit` that stages deletions of
// .act/* paths must pass through the hook unobstructed. This is the
// per-claim assertion for the fix in act-4094c6:
// docs/coordination-plane-design.md says staged deletions of `.act/*`
// are permitted; this test pins that promise at the boundary (a real
// `git commit` going through the installed hook script).
func TestDocClaim_PreCommitHook_PermitsStagedDeletions(t *testing.T) {
	root := makeRealGitRepo(t)

	// Track a .act/ subtree before installing the hook — we need
	// something the deletion-shape commit can delete.
	if err := os.MkdirAll(filepath.Join(root, ".act", "ops"), 0o755); err != nil {
		t.Fatalf("mkdir .act/ops: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".act", "config.json"),
		[]byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write .act/config.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".act", "ops", "op.json"),
		[]byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write .act/ops/op.json: %v", err)
	}
	runOrFatal(t, root, "git", "add", "-A")
	runOrFatal(t, root, "git", "commit", "-q", "--no-verify", "-m", "track legacy .act/")

	// Install the hook. From here on, every commit must pass the hook.
	if _, err := installHostPreCommitHook(root); err != nil {
		t.Fatalf("install hook: %v", err)
	}

	// Stage deletions: the untrack shape `git rm -r --cached .act/` produces.
	runOrFatal(t, root, "git", "rm", "-r", "--cached", "--ignore-unmatch", ".act/")

	// Commit through the hook (no --no-verify). Must succeed.
	commitCmd := exec.Command("git", "commit", "-m", "untrack .act/")
	commitCmd.Dir = root
	if out, err := commitCmd.CombinedOutput(); err != nil {
		t.Fatalf("commit with staged .act/* deletions blocked by hook (regression of act-4094c6): %v\n%s", err, out)
	}
}

// TestPreCommitHook_RejectsStagedAdditions is the companion to the
// "permits deletions" test: the hook must still hard-reject any staged
// addition under .act/, which is the original failure mode the hook was
// installed to prevent. Without this assertion the fix could over-rotate
// and unblock the very class of accidental re-tracking that motivated
// the hook in the first place.
func TestPreCommitHook_RejectsStagedAdditions(t *testing.T) {
	root := makeRealGitRepo(t)
	if _, err := installHostPreCommitHook(root); err != nil {
		t.Fatalf("install hook: %v", err)
	}

	// Force-add a .act/* path past gitignore (mimics an agent
	// accidentally `git add -f .act/...`).
	if err := os.MkdirAll(filepath.Join(root, ".act"), 0o755); err != nil {
		t.Fatalf("mkdir .act: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".act", "leaked.json"),
		[]byte("leaked\n"), 0o644); err != nil {
		t.Fatalf("write .act/leaked.json: %v", err)
	}
	runOrFatal(t, root, "git", "add", "-f", ".act/leaked.json")

	commitCmd := exec.Command("git", "commit", "-m", "should-be-blocked")
	commitCmd.Dir = root
	out, err := commitCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("commit with staged .act/* addition succeeded; hook should still block additions. stdout=%s", out)
	}
	if !strings.Contains(string(out), "refusing to commit .act/ paths") {
		t.Errorf("hook rejection message missing; got %q", out)
	}
}
