package main

// Follow-ups to act-a025ab's tracker-remote guard: worktree {repo}
// resolution (act-15ca2b), the MCP surface (act-626391), and act init
// (act-ef5a69).

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// trkfollowGitIn runs git in dir and fails the test on error.
func trkfollowGitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v: %s", args, dir, err, out)
	}
}

// trkfollowLinkedWorktree gives host (a repo named "proj") one commit and
// adds a real linked worktree at host/.claude/worktrees/<name>.
func trkfollowLinkedWorktree(t *testing.T, host, name string) string {
	t.Helper()
	trkfollowGitIn(t, host, "-c", "user.name=t", "-c", "user.email=t@example.com",
		"commit", "-q", "--allow-empty", "-m", "seed")
	wt := filepath.Join(host, ".claude", "worktrees", name)
	trkfollowGitIn(t, host, "worktree", "add", "-q", "-b", name, wt)
	return wt
}

func TestDocClaim_TrackerRemoteWorktreeUsesMainRepoName(t *testing.T) {
	brokencoIsolateEnv(t)
	bares := t.TempDir()
	bare, id := brokencoTrackerBare(t, bares)
	host := brokencoHostRepo(t)
	wt := trkfollowLinkedWorktree(t, host, "agent-wt")
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))

	// {repo} must be "proj" (the main repo), so the probe finds proj.git
	// even though the worktree's own folder is "agent-wt".
	_, stderr, code := runActIn(t, wt, "show", id)
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (tracker_not_checked_out); stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "tracker exists at "+bare) {
		t.Errorf("stderr does not name %s: %q", bare, stderr)
	}
	if strings.Contains(stderr, "agent-wt.git") {
		t.Errorf("{repo} expanded to the worktree folder: %q", stderr)
	}
}
