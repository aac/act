package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aac/act/internal/gitops"
)

// runListPendingOpFilesForIssue invokes `git ls-files --others --exclude-standard`
// restricted to the ops/<issueID>/ subtree of the nested .act/ repo and
// returns the absolute paths of all untracked (pending) op files for
// that specific issue.
//
// Under Phase 1 (docs/coordination-plane-design.md delta item 2), op files
// live in the nested .act/ git repo, not the host repo. repoRoot here is
// the HOST repo root; the nested repo sits at repoRoot/.act. opsDir is
// repoRoot/.act/ops. We run git from the nested repo's working tree so
// the host's .gitignore (which gitignores .act/) doesn't filter the
// untracked .json files out.
func runListPendingOpFilesForIssue(repoRoot, opsDir, issueID string) ([]string, error) {
	actDir := filepath.Join(repoRoot, ".act")
	// Derive the path relative to the NESTED act repo so git accepts it.
	relOpsDir, err := filepath.Rel(actDir, opsDir)
	if err != nil {
		return nil, fmt.Errorf("cli: list pending ops for %s: rel path: %w", issueID, err)
	}
	issuePath := filepath.Join(relOpsDir, issueID) + string(filepath.Separator)

	// act-40e336: routed through the shared act handle instead of exec'ing
	// git with cwd=actDir. `ls-files` does not fire auto-maintenance, but
	// the point of the refactor is that no call site decides that for
	// itself — the handle is the only constructor of nested-repo git
	// invocations, which is what makes TestNoDirectGitExec meaningful.
	stdout, err := gitops.NewActGitOps(actDir).RunGit("ls-files",
		"--others", "--exclude-standard", "--full-name", "--",
		issuePath)
	if err != nil {
		return nil, fmt.Errorf("cli: list pending ops for %s: git ls-files: %w", issueID, err)
	}

	var result []string
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		abs := filepath.Join(actDir, line)
		// Only keep .json files (filter out any non-op files that might
		// appear under ops/ in unusual repo states).
		if strings.HasSuffix(abs, ".json") {
			result = append(result, abs)
		}
	}
	return result, nil
}
