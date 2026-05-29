package gitops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestActGitOps_NestedRepo_StagesIntoNested asserts the act-784b regression
// fix: when the host repo gitignores `.act/`, a write through ActGitOps must
// target the nested `.act/.git` rather than walking up to the host repo and
// being refused as gitignored. Concretely:
//
//   - Host repo at /tmp/<dir>/, .gitignore contains `.act/`.
//   - Nested act-state repo at /tmp/<dir>/.act/ with its own .git.
//   - NewActGitOps(<host>/.act).StageOpFile(absolute path under .act/ops)
//     succeeds and stages into the NESTED repo's index.
//   - The host repo's index is untouched (would otherwise carry the staged
//     op file under .act/ops/...).
//   - A subsequent CommitOp lands in the nested repo's log.
func TestActGitOps_NestedRepo_StagesIntoNested(t *testing.T) {
	host := initHostWithIgnoredAct(t)
	actDir := filepath.Join(host, ".act")
	initNestedActRepo(t, actDir)

	// Write an op file under .act/ops/.
	opPath := filepath.Join(actDir, "ops", "act-abcdef", "2026-05", "2026-05-19T00-00-00.000Z-feedface-create.json")
	writeFile(t, opPath, "{\"op_type\":\"create\"}\n")

	g := NewActGitOps(actDir)
	if err := g.StageOpFile(opPath); err != nil {
		t.Fatalf("StageOpFile: %v", err)
	}

	// Assert the staged path lives in the NESTED index.
	stagedNested := strings.TrimSpace(runGit(t, actDir, "diff", "--cached", "--name-only"))
	if stagedNested != "ops/act-abcdef/2026-05/2026-05-19T00-00-00.000Z-feedface-create.json" {
		t.Fatalf("nested index: want ops/...-create.json, got %q", stagedNested)
	}

	// Assert the HOST index is untouched (the bug surface).
	stagedHost := strings.TrimSpace(runGit(t, host, "diff", "--cached", "--name-only"))
	if stagedHost != "" {
		t.Fatalf("host index: want empty, got %q (act stage leaked into host repo)", stagedHost)
	}

	// CommitOp lands in the nested repo's log.
	swCtx := SlowWriteContext{OpType: "create", OpID: "x", StateRoot: actDir}
	if err := g.CommitOp("act-op: (act-abcdef) create", swCtx); err != nil {
		t.Fatalf("CommitOp: %v", err)
	}
	nestedSubject := strings.TrimSpace(runGit(t, actDir, "log", "-1", "--format=%s"))
	if nestedSubject != "act-op: (act-abcdef) create" {
		t.Fatalf("nested HEAD subject: want act-op: (act-abcdef) create, got %q", nestedSubject)
	}
	// Host log unchanged (still just "init").
	hostSubject := strings.TrimSpace(runGit(t, host, "log", "-1", "--format=%s"))
	if hostSubject != "init host" {
		t.Fatalf("host HEAD subject: want init host (unchanged), got %q", hostSubject)
	}
}

// TestActGitOps_NestedRepo_UnstageTargetsNested asserts the same override
// applies on the rollback path. After staging an op into the nested repo,
// UnstageOpFile must restore the staging in the NESTED repo, not in the
// host (where the .gitignore refusal would otherwise surface).
func TestActGitOps_NestedRepo_UnstageTargetsNested(t *testing.T) {
	host := initHostWithIgnoredAct(t)
	actDir := filepath.Join(host, ".act")
	initNestedActRepo(t, actDir)

	opPath := filepath.Join(actDir, "ops", "act-cafe01", "2026-05", "2026-05-19T00-00-00.000Z-deadbeef-create.json")
	writeFile(t, opPath, "{\"op_type\":\"create\"}\n")

	g := NewActGitOps(actDir)
	if err := g.StageOpFile(opPath); err != nil {
		t.Fatalf("StageOpFile: %v", err)
	}
	if err := g.UnstageOpFile(opPath); err != nil {
		t.Fatalf("UnstageOpFile: %v", err)
	}

	// After unstage the nested index should be empty for that path.
	stagedNested := strings.TrimSpace(runGit(t, actDir, "diff", "--cached", "--name-only"))
	if stagedNested != "" {
		t.Fatalf("nested index after unstage: want empty, got %q", stagedNested)
	}
}

// TestActGitOps_NestedRepo_MissingGitDirSurfaces asserts that when the
// caller hands NewActGitOps an act-state root that has NO nested .git
// (e.g. an old layout, or a partial install), the resulting git error is
// the clear "not a git repository" message — NOT a misleading "ignored
// by .gitignore" refusal from the host repo. Pre-fix this case fell
// through to host cwd discovery and surfaced the ignore refusal; the
// gitDir override makes the actual failure mode visible.
func TestActGitOps_NestedRepo_MissingGitDirSurfaces(t *testing.T) {
	host := initHostWithIgnoredAct(t)
	actDir := filepath.Join(host, ".act")
	if err := os.MkdirAll(filepath.Join(actDir, "ops"), 0o755); err != nil {
		t.Fatalf("mkdir act: %v", err)
	}
	// Deliberately do NOT initNestedActRepo — the nested .git is missing.

	opPath := filepath.Join(actDir, "ops", "x.json")
	writeFile(t, opPath, "{}")

	g := NewActGitOps(actDir)
	err := g.StageOpFile(opPath)
	if err == nil {
		t.Fatalf("StageOpFile with missing nested .git: want error, got nil")
	}
	// Pre-fix message: "ignored by one of your .gitignore files".
	// Post-fix message: "not a git repository".
	msg := err.Error()
	if strings.Contains(msg, "ignored by") {
		t.Fatalf("StageOpFile error surfaced gitignore refusal (host-repo leak): %v", err)
	}
	if !strings.Contains(msg, "not a git repository") {
		t.Fatalf("StageOpFile error: want 'not a git repository', got %q", msg)
	}
}

// TestActGitOps_CheckIgnored_RespectsGitDirOverride asserts act-acdf5d:
// CheckIgnored must route through the same runner seam + --git-dir/
// --work-tree override every other gitops method uses, so the ignore
// verdict reflects the PINNED git-dir, not whatever git's cwd-discovery
// walks up to find.
//
// Fixture (two distinct repos with CONFLICTING ignore rules for the same
// path, so cwd-discovery and the override give opposite answers):
//
//   - Repo B (the "ambient" repo): a normal git repo. Its
//     <B>/.git/info/exclude is EMPTY, so it does NOT ignore "target".
//   - A working subdir <B>/wt with NO .git of its own: cwd-discovery from
//     here walks UP to repo B.
//   - Repo A (the "pinned" repo): a separate git dir whose
//     <A>/.git/info/exclude DOES ignore "target".
//
// GitOps is constructed with RepoRoot=<B>/wt (so cwd-discovery resolves
// to B) and gitDir=<A>/.git (the override target). The exclude rule is
// placed in $GIT_DIR/info/exclude (git-dir-specific, not work-tree's
// .gitignore) precisely so the verdict differs between the discovered
// repo and the pinned one.
//
// How this fails on the UNFIXED code: pre-fix CheckIgnored shelled out
// with `exec.Command("git","check-ignore",path); cmd.Dir = RepoRoot`,
// ignoring g.gitDir entirely. cwd-discovery from <B>/wt finds repo B,
// whose info/exclude does NOT list "target" → returns (false, nil). The
// test asserts (true, nil), so it fails. Post-fix CheckIgnored runs
// through g.run, which prepends --git-dir=<A>/.git, so A's info/exclude
// applies → (true, nil), and the test passes.
func TestActGitOps_CheckIgnored_RespectsGitDirOverride(t *testing.T) {
	root := t.TempDir()

	mkdir := func(p string) string {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		return p
	}

	// Repo B (ambient / cwd-discovered): does NOT ignore "target".
	repoB := filepath.Join(root, "ambient")
	runGit(t, mkdir(repoB), "init", "-q", "-b", "main")
	// info/exclude intentionally left empty for B.

	// Working subdir inside B with no .git of its own; cwd-discovery from
	// here walks up to B.
	wt := mkdir(filepath.Join(repoB, "wt"))

	// Repo A (pinned via gitDir): DOES ignore "target" via info/exclude.
	repoA := filepath.Join(root, "pinned")
	runGit(t, mkdir(repoA), "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoA, ".git", "info", "exclude"),
		[]byte("target\n"), 0o644); err != nil {
		t.Fatalf("write A info/exclude: %v", err)
	}

	// Pre-condition: confirm the two repos genuinely disagree, so the
	// post-fix verdict is only correct if the override was honored.
	// B's check-ignore exits 1 ("not ignored") for "target".
	if b := checkIgnoreExit(t, repoB, "target"); b != 1 {
		t.Fatalf("precondition: ambient repo B check-ignore exit = %d, want 1 (not ignored)", b)
	}
	// A's check-ignore exits 0 ("ignored") for "target".
	if a := checkIgnoreExit(t, repoA, "target"); a != 0 {
		t.Fatalf("precondition: pinned repo A check-ignore exit = %d, want 0 (ignored)", a)
	}

	// Construct a GitOps whose cwd (RepoRoot) discovers B but whose
	// override pins git-dir to A. Mirrors NewActGitOps's override shape;
	// in-package test sets the unexported gitDir directly.
	g := &GitOps{
		RepoRoot: wt,
		gitDir:   filepath.Join(repoA, ".git"),
		runner:   exec.Command,
	}

	ignored, err := g.CheckIgnored("target")
	if err != nil {
		t.Fatalf("CheckIgnored: %v", err)
	}
	if !ignored {
		t.Fatalf("CheckIgnored(\"target\") = false; want true (verdict must come "+
			"from the pinned git-dir %s's info/exclude, not the cwd-discovered repo %s)",
			g.gitDir, repoB)
	}
}

// checkIgnoreExit runs `git check-ignore <path>` directly in dir and
// returns its exit code (0 = ignored, 1 = not ignored). Any other exit
// code fails the test. Used only by the precondition assertions above.
func checkIgnoreExit(t *testing.T, dir, path string) int {
	t.Helper()
	cmd := exec.Command("git", "check-ignore", path)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	t.Fatalf("check-ignore in %s: %v", dir, err)
	return -1
}

// initHostWithIgnoredAct creates a tempdir + `git init`, configures
// identity, writes a `.gitignore` that excludes `.act/`, and lands one
// initial commit (so HEAD resolves). Returns the host's working tree
// path.
func initHostWithIgnoredAct(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".act/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runGit(t, dir, "add", ".gitignore")
	runGit(t, dir, "commit", "-q", "--no-verify", "-m", "init host")
	return dir
}

// initNestedActRepo creates `<actDir>/.git` via `git init` and lands one
// initial commit in it so HEAD resolves (CommitOp / Push / log invariants
// depend on a valid HEAD). Identity is configured locally; signing is
// disabled.
func initNestedActRepo(t *testing.T, actDir string) {
	t.Helper()
	if err := os.MkdirAll(actDir, 0o755); err != nil {
		t.Fatalf("mkdir actDir: %v", err)
	}
	runGit(t, actDir, "init", "-q", "-b", "main")
	runGit(t, actDir, "config", "user.email", "test@example.com")
	runGit(t, actDir, "config", "user.name", "Test")
	runGit(t, actDir, "config", "commit.gpgsign", "false")
	// Seed commit so HEAD resolves.
	if err := os.WriteFile(filepath.Join(actDir, "config.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write nested seed: %v", err)
	}
	runGit(t, actDir, "add", "config.json")
	runGit(t, actDir, "commit", "-q", "--no-verify", "-m", "nested init")
}
