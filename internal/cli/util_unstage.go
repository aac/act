package cli

import "os/exec"

// runUnstage invokes `git restore --staged <path>` with cwd=repoRoot.
// Isolated in its own file so it can be replaced (build-tag) for tests
// that need to assert no side effects on a real working tree.
func runUnstage(repoRoot, path string) error {
	cmd := exec.Command("git", "restore", "--staged", "--", path)
	cmd.Dir = repoRoot
	return cmd.Run()
}
