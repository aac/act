// Package cli — `nested-layout` doctor check.
//
// Asserts the repo is in the Phase 1 nested-repo state (post-migration).
// This is the dogfood gate: a CI-friendly check that an act-using repo
// has finished migrating to docs/coordination-plane-design.md (v2.1)
// layout. Runs as part of the standard doctor check set.
//
// Sibling check `gitignore-effective` (act-37f7) overlaps partially with
// criterion (b) below; they are intentionally separate so an unmigrated
// repo can show both findings and the operator sees a complete picture
// before running `act migrate-to-nested`.
package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/config"
)

// checkNestedLayout asserts the four invariants of the post-migration
// nested-repo state:
//
//	(a) `.act/.git` exists (nested-repo bootstrap landed).
//	(b) `.act/` is in the host repo's `.gitignore` (line-match for the
//	    canonical `.act/` entry; functional-equivalence variants are
//	    `gitignore-effective`'s territory).
//	(c) No `.act/*` paths are tracked by the host repo (the `git rm -r
//	    --cached .act/` step succeeded and stuck).
//	(d) Host pre-commit hook is installed (we look for the act-managed
//	    region marker preCommitHookHeader from init.go).
//
// All four findings are severity=error. A repo that fails any of them is
// not in the migrated state and the canonical loop will not produce the
// right commit/log shape. Remedy is uniform: `act migrate-to-nested`.
//
// Returns nil when all four invariants hold (the standard "no findings"
// shape for a passing doctor check).
func checkNestedLayout(repoRoot string, paths config.LayoutPaths) []Finding {
	var findings []Finding

	// (a) nested .act/.git
	if _, err := os.Stat(filepath.Join(paths.Root, ".git")); err != nil {
		findings = append(findings, Finding{
			Check:    "nested-layout",
			Severity: "error",
			Message:  fmt.Sprintf("nested .act/.git not found at %s; remedy: act migrate-to-nested", filepath.Join(paths.Root, ".git")),
		})
	}

	// (b) host .gitignore contains the canonical entry (exact line match,
	// trim-space). Equivalent variants like `**/.act/` or `/.act` are
	// accepted by the `gitignore-effective` check (act-37f7); this check
	// is strict about the canonical line because that's what init/migrate
	// installs and what re-running the migration is idempotent on.
	if !gitignoreHasEntry(repoRoot, gitignoreEntry) {
		findings = append(findings, Finding{
			Check:    "nested-layout",
			Severity: "error",
			Message:  fmt.Sprintf(".act/ not in host .gitignore (canonical %q line); remedy: act migrate-to-nested", gitignoreEntry),
		})
	}

	// (c) No `.act/*` paths tracked by the host repo. We ask git's
	// ls-files which is the authoritative answer (much more reliable
	// than walking the worktree).
	if tracked, err := hostHasTrackedActPaths(repoRoot); err == nil && tracked {
		findings = append(findings, Finding{
			Check:    "nested-layout",
			Severity: "error",
			Message:  ".act/* paths are tracked by the host repo; remedy: act migrate-to-nested (runs git rm -r --cached .act/)",
		})
	}

	// (d) host pre-commit hook installed. We resolve the hooks dir the
	// same way init/migrate does (worktree-aware) and look for the
	// preCommitHookHeader marker.
	if !hostPreCommitHookInstalled(repoRoot) {
		findings = append(findings, Finding{
			Check:    "nested-layout",
			Severity: "error",
			Message:  "host pre-commit hook missing the act-managed block; remedy: act migrate-to-nested (installs hook)",
		})
	}

	return findings
}

// gitignoreHasEntry reports whether <repoRoot>/.gitignore contains an
// exact (trim-space) line matching entry.
func gitignoreHasEntry(repoRoot, entry string) bool {
	data, err := os.ReadFile(filepath.Join(repoRoot, ".gitignore"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == entry {
			return true
		}
	}
	return false
}

// hostHasTrackedActPaths asks `git ls-files -- .act/` whether any paths
// under .act/ are tracked by the host repo. Returns (true, nil) when one
// or more paths are tracked, (false, nil) when nothing is tracked, and
// (false, err) on git failure.
func hostHasTrackedActPaths(repoRoot string) (bool, error) {
	cmd := exec.Command("git", "ls-files", "--", ".act/")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(out))) > 0, nil
}

// hostPreCommitHookInstalled reports whether the host repo's pre-commit
// hook contains the act-managed region (preCommitHookHeader). Resolves
// the hooks dir via the same worktree-aware helper init/migrate use, so
// a hook installed by act under a worktree's main `.git/hooks` is seen.
func hostPreCommitHookInstalled(repoRoot string) bool {
	hooksDir, err := resolveGitHooksDir(repoRoot)
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	if err != nil {
		return false
	}
	return strings.Contains(string(data), preCommitHookHeader)
}
