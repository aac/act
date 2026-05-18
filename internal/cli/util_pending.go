package cli

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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

	cmd := exec.Command("git", "ls-files",
		"--others", "--exclude-standard", "--full-name", "--",
		issuePath)
	cmd.Dir = actDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("cli: list pending ops for %s: git ls-files: %w (stderr: %s)",
			issueID, err, strings.TrimSpace(stderr.String()))
	}

	var result []string
	for _, line := range strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n") {
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
