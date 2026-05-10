package cli

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// runListPendingOpFilesForIssue invokes `git ls-files --others --exclude-standard`
// restricted to the .act/ops/<issueID>/ subtree and returns the absolute paths
// of all untracked (pending) op files for that specific issue.
//
// opsDir is an absolute path; the function derives the git-relative path for
// the issue subtree by trimming repoRoot from opsDir. This ensures any
// non-standard ops directory layout (e.g. in tests) is handled correctly.
//
// Isolated in its own file so tests can replace the implementation.
func runListPendingOpFilesForIssue(repoRoot, opsDir, issueID string) ([]string, error) {
	// Derive the path relative to repoRoot so git accepts it.
	relOpsDir, err := filepath.Rel(repoRoot, opsDir)
	if err != nil {
		return nil, fmt.Errorf("cli: list pending ops for %s: rel path: %w", issueID, err)
	}
	issuePath := filepath.Join(relOpsDir, issueID) + string(filepath.Separator)

	cmd := exec.Command("git", "ls-files",
		"--others", "--exclude-standard", "--full-name", "--",
		issuePath)
	cmd.Dir = repoRoot
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
		abs := filepath.Join(repoRoot, line)
		// Only keep .json files (filter out any non-op files that might
		// appear under .act/ops/ in unusual repo states).
		if strings.HasSuffix(abs, ".json") {
			result = append(result, abs)
		}
	}
	return result, nil
}
