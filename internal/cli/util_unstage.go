package cli

import (
	"os"
	"os/exec"
	"path/filepath"
)

// runUnstage invokes `git restore --staged <path>` with cwd=repoRoot.
// Indirected through a package-level variable (`runUnstageFn`) so tests
// can swap in a recording stub to assert which paths the rollback actually
// touches without inspecting the live working tree.
var runUnstageFn = runUnstageReal

func runUnstage(repoRoot, path string) error {
	return runUnstageFn(repoRoot, path)
}

// runUnstageReal runs the actual git invocation. When repoRoot contains
// a `.git` subdir (the nested-act-repo shape from Phase 1), we pin git's
// repo discovery with explicit --git-dir/--work-tree flags so the rollback
// targets the nested repo rather than walking up into a host repo whose
// .gitignore refuses .act/ paths (act-784b). Mirrors the override in
// internal/gitops.GitOps.run when gitDir is set.
func runUnstageReal(repoRoot, path string) error {
	args := []string{"restore", "--staged", "--", path}
	if nestedGit := filepath.Join(repoRoot, ".git"); dirOrFileExists(nestedGit) {
		args = append([]string{
			"--git-dir=" + nestedGit,
			"--work-tree=" + repoRoot,
		}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	return cmd.Run()
}

// dirOrFileExists returns true when path resolves to a regular file or
// directory. We use os.Lstat so a symlink at .git (the worktree shape)
// also counts as present. The function ignores errors other than
// not-exist; on any other failure the caller falls through to cwd
// discovery, which preserves the pre-act-784b behavior.
func dirOrFileExists(path string) bool {
	if _, err := os.Lstat(path); err == nil {
		return true
	}
	return false
}
