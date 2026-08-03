package cli

import (
	"os"
	"path/filepath"

	"github.com/aac/act/internal/gitops"
)

// runUnstage invokes `git restore --staged <path>` with cwd=repoRoot.
// Indirected through a package-level variable (`runUnstageFn`) so tests
// can swap in a recording stub to assert which paths the rollback actually
// touches without inspecting the live working tree.
var runUnstageFn = runUnstageReal

func runUnstage(repoRoot, path string) error {
	return runUnstageFn(repoRoot, path)
}

// runUnstageReal runs the actual git invocation through the shared gitops
// handle. When repoRoot contains a `.git` subdir (the nested-act-repo
// shape from Phase 1) the act handle pins git's repo discovery with
// explicit --git-dir/--work-tree flags so the rollback targets the nested
// repo rather than walking up into a host repo whose .gitignore refuses
// .act/ paths (act-784b); otherwise the plain handle preserves cwd
// discovery. This used to hand-roll the same prefix locally — act-40e336
// replaced the copy with a call, so there is one place the pinning and
// the maintenance overrides are decided.
//
// The error is deliberately still swallowed by callers (`_ = runUnstage`);
// this is a best-effort rollback on a path that already has an error to
// report.
func runUnstageReal(repoRoot, path string) error {
	var g *gitops.GitOps
	if dirOrFileExists(filepath.Join(repoRoot, ".git")) {
		g = gitops.NewActGitOps(repoRoot)
	} else {
		g = gitops.NewGitOps(repoRoot)
	}
	return g.UnstageOpFile(path)
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
