package gitops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aac/act/internal/testfixtures"
)

// initRepo creates a fresh git repo in a tempdir and returns its path.
// The repo has user.name/user.email configured locally, commit signing
// turned off, and one initial commit so that HEAD is valid.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	// Initial commit so HEAD resolves.
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("init\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README")
	runGit(t, dir, "commit", "-q", "--no-verify", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// writeFile is a small helper used in multiple tests.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestStageOpFile(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)

	opPath := filepath.Join(dir, ".act", "ops", "x.json")
	writeFile(t, opPath, "{}")

	if err := g.StageOpFile(opPath); err != nil {
		t.Fatalf("StageOpFile: %v", err)
	}
	out := runGit(t, dir, "diff", "--cached", "--name-only")
	if strings.TrimSpace(out) != ".act/ops/x.json" {
		t.Fatalf("expected staged path .act/ops/x.json, got %q", out)
	}
}

func TestCommitNoVerifyByDefault(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pre-commit hooks via shebang are POSIX-specific")
	}
	dir := initRepo(t)

	// Install a failing pre-commit hook.
	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\necho 'hook fail' >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatalf("chmod hook: %v", err)
	}

	// Stage a file.
	writeFile(t, filepath.Join(dir, "f1"), "x")
	runGit(t, dir, "add", "f1")

	g := NewGitOps(dir) // Verify=false by default
	if err := g.Commit("act-op: deadbeef create"); err != nil {
		t.Fatalf("Commit (default): %v", err)
	}
	subj := strings.TrimSpace(runGit(t, dir, "log", "-1", "--format=%s"))
	if subj != "act-op: deadbeef create" {
		t.Fatalf("subject = %q", subj)
	}
}

func TestCommitVerifyTrueRunsHooks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pre-commit hooks via shebang are POSIX-specific")
	}
	dir := initRepo(t)

	hook := filepath.Join(dir, ".git", "hooks", "pre-commit")
	writeFile(t, hook, "#!/bin/sh\necho 'hook fail' >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatalf("chmod hook: %v", err)
	}

	writeFile(t, filepath.Join(dir, "f2"), "y")
	runGit(t, dir, "add", "f2")

	g := NewGitOps(dir)
	g.Verify = true
	if err := g.Commit("should fail"); err == nil {
		t.Fatalf("Commit with Verify=true should have failed")
	}
}

func TestIsClean(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)

	clean, err := g.IsClean()
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if !clean {
		t.Fatalf("expected clean tree after init")
	}

	writeFile(t, filepath.Join(dir, "dirty"), "z")
	clean, err = g.IsClean()
	if err != nil {
		t.Fatalf("IsClean: %v", err)
	}
	if clean {
		t.Fatalf("expected dirty tree after writing untracked file")
	}
}

func TestPushNoRemote(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)
	err := g.Push()
	if !errors.Is(err, ErrNoRemote) {
		t.Fatalf("Push without remote: want ErrNoRemote, got %v", err)
	}
}

func TestPullRebaseNoUpstream(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)
	err := g.PullRebase()
	if !errors.Is(err, ErrNoRemote) {
		t.Fatalf("PullRebase without upstream: want ErrNoRemote, got %v", err)
	}
}

// TestPullRebase_DirtyTreeReturnsSoftFail (act-68f08b regression): when
// `git pull --rebase` refuses because the working tree has unstaged
// changes to a tracked file (the canonical case is `.act/index.db`
// dirtied by a prior read), PullRebase must return ErrPullRebaseDirtyTree
// so callers that have already committed a durable local op can demote
// the failure to a soft no-op rather than bubbling raw `cannot pull with
// rebase` stderr that misleads the agent into thinking the write failed.
func TestPullRebase_DirtyTreeReturnsSoftFail(t *testing.T) {
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	// Track a file in the working tree, then leave an unstaged
	// modification — this mimics the `.act/index.db` situation where
	// the file is tracked but rewritten outside the commit boundary.
	tracked := filepath.Join(work, "cache.bin")
	writeFile(t, tracked, "v1\n")
	runGit(t, work, "add", "cache.bin")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "track cache")
	runGit(t, work, "push", "-q", "-u", "origin", "main")

	// Peer advances the remote so there's something to rebase against —
	// otherwise modern git short-circuits before the dirty-tree check.
	remote.AdvanceCommits(1)

	// Dirty the tracked file.
	writeFile(t, tracked, "v2-uncommitted\n")

	g := NewGitOps(work)
	err := g.PullRebase()
	if err == nil {
		t.Fatalf("PullRebase with dirty tracked file: want error, got nil")
	}
	if !errors.Is(err, ErrPullRebaseDirtyTree) {
		t.Fatalf("PullRebase: want ErrPullRebaseDirtyTree, got %v", err)
	}
}

func TestContiguousActOpRange(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)

	// Make a couple of non-act commits, then 3 contiguous act-op commits at HEAD.
	for i := 0; i < 2; i++ {
		writeFile(t, filepath.Join(dir, "pre"+string(rune('0'+i))), "p")
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-q", "--no-verify", "-m", "regular commit")
	}
	var firstExpected, lastExpected string
	for i := 0; i < 3; i++ {
		writeFile(t, filepath.Join(dir, ".act", "ops", "f"+string(rune('0'+i))+".json"), "{}")
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-q", "--no-verify", "-m", "act-op: deadbeef create")
		sha := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
		if i == 0 {
			firstExpected = sha
		}
		if i == 2 {
			lastExpected = sha
		}
	}

	first, last, count, err := g.ContiguousActOpRange()
	if err != nil {
		t.Fatalf("ContiguousActOpRange: %v", err)
	}
	if count != 3 {
		t.Fatalf("count: want 3, got %d", count)
	}
	if first != firstExpected {
		t.Fatalf("first: want %s, got %s", firstExpected, first)
	}
	if last != lastExpected {
		t.Fatalf("last: want %s, got %s", lastExpected, last)
	}
}

func TestContiguousActOpRangeHeadIsNotActOp(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)
	first, last, count, err := g.ContiguousActOpRange()
	if err != nil {
		t.Fatalf("ContiguousActOpRange: %v", err)
	}
	if count != 0 || first != "" || last != "" {
		t.Fatalf("expected empty range, got first=%q last=%q count=%d", first, last, count)
	}
}

func TestSquashActOpRange(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)

	// 3 contiguous act-op commits.
	for i := 0; i < 3; i++ {
		writeFile(t, filepath.Join(dir, ".act", "ops", "s"+string(rune('0'+i))+".json"), "{}")
		runGit(t, dir, "add", ".")
		runGit(t, dir, "commit", "-q", "--no-verify", "-m", "act-op: deadbeef create")
	}

	first, last, count, err := g.ContiguousActOpRange()
	if err != nil || count != 3 {
		t.Fatalf("range: err=%v count=%d", err, count)
	}

	if err := g.SquashActOpRange(first, last, "0.1.0"); err != nil {
		t.Fatalf("SquashActOpRange: %v", err)
	}

	subj := strings.TrimSpace(runGit(t, dir, "log", "-1", "--format=%s"))
	if subj != "act-squash: writer_version=0.1.0" {
		t.Fatalf("squash subject = %q", subj)
	}
	// HEAD~1 should be the original initial commit (init).
	prevSubj := strings.TrimSpace(runGit(t, dir, "log", "-1", "--format=%s", "HEAD~1"))
	if prevSubj != "init" {
		t.Fatalf("HEAD~1 subject = %q, want init", prevSubj)
	}
	// And only one act-op-prefixed commit should remain via ContiguousActOpRange:
	// since HEAD's subject is now act-squash, the contiguous range is empty.
	_, _, c2, err := g.ContiguousActOpRange()
	if err != nil {
		t.Fatalf("ContiguousActOpRange after squash: %v", err)
	}
	if c2 != 0 {
		t.Fatalf("post-squash range count: want 0, got %d", c2)
	}
}

func TestSquashSingleCommitNoOp(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)
	writeFile(t, filepath.Join(dir, ".act", "ops", "lone.json"), "{}")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "--no-verify", "-m", "act-op: deadbeef create")
	headBefore := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))

	first, last, count, err := g.ContiguousActOpRange()
	if err != nil || count != 1 {
		t.Fatalf("range: err=%v count=%d", err, count)
	}
	if first != last {
		t.Fatalf("expected first==last for single commit, got %s vs %s", first, last)
	}
	if err := g.SquashActOpRange(first, last, "0.1.0"); err != nil {
		t.Fatalf("SquashActOpRange single: %v", err)
	}
	headAfter := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	if headBefore != headAfter {
		t.Fatalf("single-commit squash should be a no-op; HEAD changed %s -> %s", headBefore, headAfter)
	}
}

func TestCurrentBranch(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)
	br, err := g.CurrentBranch()
	if err != nil {
		t.Fatalf("CurrentBranch: %v", err)
	}
	if br != "main" {
		t.Fatalf("branch: want main, got %q", br)
	}
}

// TestEnsureBranchCreatesIfMissing verifies the act-5d6a contract:
// EnsureBranch with a non-existent name creates the branch from HEAD
// and switches to it. Repeated calls are idempotent (re-pin to HEAD).
// An empty branch argument is a no-op.
func TestEnsureBranch(t *testing.T) {
	dir := initRepo(t)
	g := NewGitOps(dir)
	headBefore := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))

	// Empty branch: no-op.
	if err := g.EnsureBranch(""); err != nil {
		t.Fatalf("EnsureBranch(\"\"): %v", err)
	}
	branch, _ := g.CurrentBranch()
	if branch != "main" {
		t.Fatalf("empty-branch call should not switch; current=%q", branch)
	}

	// Create a new branch from HEAD.
	if err := g.EnsureBranch("agent-x"); err != nil {
		t.Fatalf("EnsureBranch(agent-x): %v", err)
	}
	branch, _ = g.CurrentBranch()
	if branch != "agent-x" {
		t.Fatalf("after EnsureBranch(agent-x): current=%q want agent-x", branch)
	}
	commit := strings.TrimSpace(runGit(t, dir, "rev-parse", "agent-x"))
	if commit != headBefore {
		t.Fatalf("agent-x points at %s, want %s (HEAD before)", commit, headBefore)
	}

	// Idempotent: calling again with the same branch is a no-op effectively.
	if err := g.EnsureBranch("agent-x"); err != nil {
		t.Fatalf("EnsureBranch(agent-x) repeat: %v", err)
	}
}

// TestAutoPushAfterCommitToBranchTargetsNamedRef verifies the act-5d6a
// push override: with an explicit branch argument, the push lands on
// `origin/<branch>` instead of whatever the current branch / tracking
// config would have resolved. Anchors the doc-claim that `--branch`
// decouples the push from tracking-config defaults.
func TestAutoPushAfterCommitToBranchTargetsNamedRef(t *testing.T) {
	ResetPushAttemptCounter()
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	// Start on main; switch to agent-x so HEAD is on agent-x. Commit
	// something on agent-x.
	runGit(t, work, "checkout", "-q", "-B", "agent-x")
	writeFile(t, filepath.Join(work, "op.txt"), "x\n")
	runGit(t, work, "add", "op.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "agent-x op")

	g := NewGitOps(work)
	// Push to the agent-x branch explicitly.
	if err := g.AutoPushAfterCommitToBranch("agent-x"); err != nil {
		t.Fatalf("AutoPushAfterCommitToBranch(agent-x): %v", err)
	}
	// The remote must now have agent-x with our op commit.
	out := runGit(t, remote.Path, "log", "-1", "--format=%s", "agent-x")
	if !strings.Contains(out, "agent-x op") {
		t.Fatalf("agent-x on remote: want %q, got %q", "agent-x op", out)
	}
	// Main must NOT have been advanced — the op must not have leaked
	// onto origin/main.
	mainOut, mainErr := exec.Command("git", "-C", remote.Path, "log", "-1", "--format=%s", "main").CombinedOutput()
	if mainErr == nil && strings.Contains(string(mainOut), "agent-x op") {
		t.Fatalf("agent-x op leaked onto origin/main: %q", string(mainOut))
	}
}

// TestAutoPushAfterCommitToBranchEmptyFallsBackToCurrentBranch verifies
// the empty-string fallback: AutoPushAfterCommitToBranch("") matches
// AutoPushAfterCommit() exactly — push targets the current branch.
func TestAutoPushAfterCommitToBranchEmptyFallsBackToCurrentBranch(t *testing.T) {
	ResetPushAttemptCounter()
	remote := testfixtures.NewBareRemote(t)
	work := cloneAndConfigure(t, remote.URL)

	writeFile(t, filepath.Join(work, "op.txt"), "x\n")
	runGit(t, work, "add", "op.txt")
	runGit(t, work, "commit", "-q", "--no-verify", "-m", "fallback op")

	g := NewGitOps(work)
	if err := g.AutoPushAfterCommitToBranch(""); err != nil {
		t.Fatalf("AutoPushAfterCommitToBranch(\"\"): %v", err)
	}
	out := runGit(t, remote.Path, "log", "-1", "--format=%s", "main")
	if !strings.Contains(out, "fallback op") {
		t.Fatalf("main on remote: want fallback op, got %q", out)
	}
}
