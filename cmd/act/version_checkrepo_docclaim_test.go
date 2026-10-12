package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Doc-claim cover for `act version --check-repo` resolving the repo root
// (act-77b411). It used the working directory as the repo root, so from
// <repo>/internal it reported no_repo (exit 3) for a repo that has a tracker
// while every other read command walked up. The helpers it borrows (pass5*)
// run the real binary.

// vcheckSubdir makes dir/<rel> and returns it.
func vcheckSubdir(t *testing.T, dir, rel string) string {
	t.Helper()
	sub := filepath.Join(dir, rel)
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	return sub
}

// TestDocClaim_VersionCheckRepo_ResolvesRepoRootFromSubdirectory asserts the
// spec's `act version` exit-codes line: --check-repo finds the tracker from
// any directory of the repo, and exit 3 is kept for a directory that has no
// tracker above it.
func TestDocClaim_VersionCheckRepo_ResolvesRepoRootFromSubdirectory(t *testing.T) {
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")
	sub := vcheckSubdir(t, dir, filepath.Join("internal", "deep"))

	// 0 from a subdirectory, in both renderings, and from inside .act/. The
	// payload names a max op version, so the walk read this repo's ops.
	out, _ := pass5Run(t, sub, 0, "", "version", "--check-repo", "--json")
	if !strings.Contains(out, `"max_op_version"`) {
		t.Errorf("version --check-repo from a subdirectory read no op: no max_op_version in %s", out)
	}
	pass5Run(t, sub, 0, "", "version", "--check-repo")
	pass5Run(t, filepath.Join(dir, ".act"), 0, "", "version", "--check-repo", "--json")

	// 4 / version_skew from the same subdirectory once the tracker holds an
	// op from a newer writer.
	restore := pass5NewerWriter(t, dir, id)
	pass5Run(t, sub, 4, "version_skew", "version", "--check-repo", "--json")
	restore()
	pass5Run(t, sub, 0, "", "version", "--check-repo", "--json")

	// 3 is kept where there is no tracker to find: a git repo with no .act/
	// (and a subdirectory of one), and a directory outside any git repo.
	host := brokencoHostRepo(t)
	pass5Run(t, host, 3, "no_repo", "version", "--check-repo", "--json")
	pass5Run(t, vcheckSubdir(t, host, "internal"), 3, "no_repo", "version", "--check-repo", "--json")
	bare := t.TempDir()
	pass5Run(t, bare, 3, "not_in_git", "version", "--check-repo", "--json")

	// Without the flag no repo is read: 0 anywhere.
	pass5Run(t, bare, 0, "", "version", "--json")
	pass5Run(t, sub, 0, "", "version", "--json")
}
