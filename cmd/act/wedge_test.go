package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_StaleLock_WedgesWrites pins the README "If a write is
// interrupted" claim that a stale git lock file in .act/.git/ makes every
// act write fail until the lock is removed (act-8fe6eb / act-aef518).
// Exercised at the subprocess boundary for both lock files git leaves in
// practice: index.lock (blocks the stage step) and HEAD.lock (blocks the
// ref lock at commit).
func TestDocClaim_StaleLock_WedgesWrites(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "pre-wedge issue") // sanity: writes work

	for _, lock := range []string{"index.lock", "HEAD.lock"} {
		lockPath := filepath.Join(dir, ".act", ".git", lock)
		if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
			t.Fatalf("plant %s: %v", lock, err)
		}
		out, stderr, code := runActIn(t, dir, "create", "wedged by "+lock)
		if code == 0 {
			t.Fatalf("create with stale %s: want non-zero exit, got 0; out=%s", lock, out)
		}
		// The failure must name the lock file — that's the only thread a
		// wedged agent can pull (git's stderr carries the remedy).
		if !strings.Contains(out+stderr, lock) {
			t.Errorf("create failure with stale %s does not name the lock; out=%s stderr=%s", lock, out, stderr)
		}
		if err := os.Remove(lockPath); err != nil {
			t.Fatalf("remove %s: %v", lock, err)
		}
	}

	// Locks removed: writes work again.
	if out, stderr, code := runActIn(t, dir, "create", "post-wedge issue"); code != 0 {
		t.Fatalf("create after lock removal: exit %d; out=%s stderr=%s", code, out, stderr)
	}
}

// TestDocClaim_StaleLock_OpSurvivesAndRecovers pins the README "If a write
// is interrupted" claims (act-a3160b): a write that fails on a stale
// index.lock (the STAGE step) reads back as not having happened — its issue
// is absent from `act list` and `act show` — yet nothing is lost, because the
// op file is moved aside to .act/.failed-ops/<timestamp>/ops/ and the error
// names that path. It then runs the README recovery block literally, through
// a shell in the host repo, with only <timestamp> filled in, and asserts the
// wedged write's issue is back.
func TestDocClaim_StaleLock_OpSurvivesAndRecovers(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "healthy issue")
	opsBefore := stagefailCountFiles(t, filepath.Join(dir, ".act", "ops"))

	lockPath := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant index.lock: %v", err)
	}
	out, _, code := runActIn(t, dir, "create", "wedged issue", "--json")
	if code == 0 {
		t.Fatalf("create under stale lock: want non-zero exit, got 0; out=%s", out)
	}
	var envl struct {
		Error   string         `json:"error"`
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &envl); err != nil {
		t.Fatalf("parse error envelope %q: %v", out, err)
	}
	if envl.Error != "stale_git_lock" {
		t.Errorf("error = %q; want stale_git_lock; out=%s", envl.Error, out)
	}

	// Exit status and reads agree: the write did not happen.
	if listOut, _, listCode := runActIn(t, dir, "list"); listCode != 0 || strings.Contains(listOut, "wedged issue") {
		t.Fatalf("act create exited %d but act list shows the issue (exit=%d): %s", code, listCode, listOut)
	}
	if n := stagefailCountFiles(t, filepath.Join(dir, ".act", "ops")); n != opsBefore {
		t.Errorf("ops/ holds %d op files after the failed write; want %d (unchanged)", n, opsBefore)
	}

	// Nothing is lost: exactly one <timestamp> dir, holding the op, and the
	// error names a file inside it.
	failedRoot := filepath.Join(dir, ".act", ".failed-ops")
	stamps, err := os.ReadDir(failedRoot)
	if err != nil || len(stamps) != 1 {
		t.Fatalf("want exactly one .act/.failed-ops/<timestamp> dir; entries=%v err=%v", stamps, err)
	}
	stamp := stamps[0].Name()
	q, _ := envl.Details["quarantined_op"].(string)
	if !strings.HasPrefix(q, filepath.Join(failedRoot, stamp, "ops")+string(filepath.Separator)) {
		t.Errorf("details.quarantined_op = %q; want a file under .act/.failed-ops/%s/ops/", q, stamp)
	}
	if _, err := os.Stat(q); err != nil {
		t.Errorf("quarantined op %q not on disk: %v", q, err)
	}
	// The remedy in the envelope names the same copy step.
	if remedy, _ := envl.Details["remedy"].(string); !strings.Contains(remedy, "cp -R .act/.failed-ops/"+stamp+"/ops/. .act/ops/") {
		t.Errorf("remedy does not copy the op back from .act/.failed-ops/%s: %q", stamp, remedy)
	}

	// The README recovery block, extracted from the README itself so the
	// test cannot drift from it, with <timestamp> substituted.
	block := stagefailReadmeRecoveryBlock(t)
	block = strings.ReplaceAll(block, "<timestamp>", stamp)
	for _, line := range strings.Split(block, "\n") {
		cmdline := strings.TrimPrefix(line, "$ ")
		if strings.HasPrefix(cmdline, "act ") {
			if _, stderr, code := runActIn(t, dir, strings.Fields(cmdline)[1:]...); code != 0 {
				t.Fatalf("%s: exit %d; stderr=%s", cmdline, code, stderr)
			}
			continue
		}
		sh := exec.Command("sh", "-c", cmdline)
		sh.Dir = dir
		// Identity for the recovery commit only; the command text is the
		// README's, unmodified.
		sh.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := sh.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", cmdline, err, out)
		}
	}

	// The wedged write's issue is back, and new writes work.
	listOut, _, code := runActIn(t, dir, "list")
	if code != 0 || !strings.Contains(listOut, "wedged issue") {
		t.Errorf("recovered tracker is missing the wedged issue; exit=%d out=%s", code, listOut)
	}
	createBlocksIssue(t, dir, "post-recovery issue")
}

// TestDocClaim_StaleLock_CloseStageFailureStaysOpen covers the same rule on
// the close path, which stages outside the shared write helper: a close that
// fails on a stale index.lock leaves `act show` reporting the issue open.
func TestDocClaim_StaleLock_CloseStageFailureStaysOpen(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "close probe")

	lockPath := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant index.lock: %v", err)
	}
	out, _, code := runActIn(t, dir, "close", id, "--json")
	if code == 0 {
		t.Fatalf("close under stale lock: want non-zero exit, got 0; out=%s", out)
	}
	if !strings.Contains(out, "quarantined_op") {
		t.Errorf("close stage failure does not report details.quarantined_op: %s", out)
	}
	if got := showStatus(t, dir, id); got != "open" {
		t.Errorf("act close exited %d but act show reports status %q; want open", code, got)
	}
}

// stagefailReadmeRecoveryBlock returns the `$ `-prefixed lines of the
// console block under README "If a write is interrupted", joined by "\n".
func stagefailReadmeRecoveryBlock(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	s := string(raw)
	i := strings.Index(s, "## If a write is interrupted")
	if i < 0 {
		t.Fatal("README has no 'If a write is interrupted' section")
	}
	s = s[i:]
	start := strings.Index(s, "```console\n")
	if start < 0 {
		t.Fatal("recovery section has no console block")
	}
	s = s[start+len("```console\n"):]
	s = s[:strings.Index(s, "```")]
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.HasPrefix(l, "$ ") {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		t.Fatal("recovery console block has no commands")
	}
	return strings.Join(lines, "\n")
}

// stagefailCountFiles counts regular files under root (0 if absent).
func stagefailCountFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk %s: %v", root, err)
	}
	return n
}
