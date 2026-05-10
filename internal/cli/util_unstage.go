package cli

import "os/exec"

// runUnstage invokes `git restore --staged <path>` with cwd=repoRoot.
// Indirected through a package-level variable (`runUnstageFn`) so tests
// can swap in a recording stub to assert which paths the rollback actually
// touches without inspecting the live working tree.
var runUnstageFn = runUnstageReal

func runUnstage(repoRoot, path string) error {
	return runUnstageFn(repoRoot, path)
}

func runUnstageReal(repoRoot, path string) error {
	cmd := exec.Command("git", "restore", "--staged", "--", path)
	cmd.Dir = repoRoot
	return cmd.Run()
}
