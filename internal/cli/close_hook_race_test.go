package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/op"
)

// act-8ee085 regression cover.
//
// THE BUG. `act close` wrote its close op into `ops/`, staged it, then ran
// the close gate, and rolled back a refused close by deleting the file.
// Nothing makes that delete atomic against another process reading the
// working tree, and one such process runs on a timer: the fleet's act-sync
// sweep does `git add -- ops && git commit` on whatever it finds
// uncommitted. A sweep landing inside the gate's window committed a close
// the gate was in the middle of refusing, so the close sat in HEAD while
// the working tree had no such file — `act show` read the issue open, a
// clone read it closed, and nothing announced the disagreement. A gate
// running a test suite makes that window minutes wide.
//
// THE FIX. The gate runs before the op file is written, so there is no
// file for a sweep to capture and no rollback to race.
//
// These tests assert the invariant at the user-visible boundary (what is
// on disk and in HEAD after `act close`), not on internal ordering, and
// they contain no sleeps: the "concurrent" sweep is performed by the hook
// itself, which is exactly the interleaving the bug needs.

// writeCloseHook installs an executable .act/hooks/close containing body.
func writeCloseHook(t *testing.T, root, body string) {
	t.Helper()
	dir := filepath.Join(root, ".act", "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	path := filepath.Join(dir, "close")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

// TestDocClaim_HookRunsBeforeOpFileIsWritten asserts the spec's hooks
// contract step 1: the hook runs before the op file is written, so nothing
// exists under ops/ while it runs. The hook itself records what the op log
// looks like from the outside at the moment it runs — that recording is
// the assertion, because "nothing is there to be swept" is the property
// the fix actually buys.
func TestDocClaim_HookRunsBeforeOpFileIsWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook execution is POSIX-only in v0.1")
	}
	root, id := makeCloseRepoWithIssue(t)
	probe := filepath.Join(t.TempDir(), "seen.txt")

	// The hook lists every close op file visible under ops/ at the moment
	// it runs, then passes.
	writeCloseHook(t, root, "#!/bin/sh\n"+
		"find \"$ACT_STATE_PATH/ops\" -name '*-close.json' > "+probe+" 2>/dev/null\n"+
		"exit 0\n")

	if _, code := RunClose(root, CloseOptions{ID: id}); code != 0 {
		t.Fatalf("close with passing hook: code = %d, want 0", code)
	}

	seen, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("hook did not run (no probe file): %v", err)
	}
	if strings.TrimSpace(string(seen)) != "" {
		t.Errorf("close op file was visible under ops/ while the hook ran:\n%s\n"+
			"the hook must run before the op file is written (spec §Hooks contract step 1)",
			seen)
	}

	// And the close still landed: the fix must not cost the happy path.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-close.json"))
	if len(matches) != 1 {
		t.Errorf("after a passing hook: %d close op files, want 1", len(matches))
	}
}

// TestDocClaim_RefusedCloseSurvivesConcurrentSweep is the act-8ee085
// reproduction. The hook plays the act-sync sweep — it commits whatever is
// uncommitted under ops/, exactly as `act-sync` does — and then refuses the
// close. On the old ordering this left the close in HEAD with the working
// tree missing it. The assertion is the ticket's accept criterion: a close
// whose gate fails leaves no close op in the working tree AND none in HEAD.
func TestDocClaim_RefusedCloseSurvivesConcurrentSweep(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook execution is POSIX-only in v0.1")
	}
	root, id := makeCloseRepoWithIssue(t)

	// Sweep, then refuse — the interleaving the race produces by timing.
	writeCloseHook(t, root, "#!/bin/sh\n"+
		"cd \"$ACT_STATE_PATH\" || exit 1\n"+
		"if [ -n \"$(git status --porcelain -- ops)\" ]; then\n"+
		"  git add -- ops && git commit --no-verify -q -m 'act-sync: sweep uncommitted op file(s)'\n"+
		"fi\n"+
		"echo 'gate refuses this close' >&2\n"+
		"exit 1\n")

	out, code := RunClose(root, CloseOptions{ID: id})
	if code == 0 {
		t.Fatalf("close with a failing gate exited 0; out=%+v", out)
	}

	actDir := filepath.Join(root, ".act")

	// (a) nothing in the working tree.
	matches, _ := filepath.Glob(filepath.Join(actDir, "ops", id, "*", "*-close.json"))
	if len(matches) != 0 {
		t.Errorf("refused close left %d close op file(s) in the working tree: %v", len(matches), matches)
	}

	// (b) nothing in HEAD — the phantom close.
	inHead := gitOut(t, actDir, "ls-tree", "-r", "--name-only", "HEAD", "--", "ops")
	for _, line := range strings.Split(inHead, "\n") {
		if strings.Contains(line, id) && strings.HasSuffix(strings.TrimSpace(line), "-close.json") {
			t.Errorf("refused close is committed in HEAD: %s", line)
		}
	}

	// (c) and the two agree — no dirty deletion for a later sweep to
	// publish as an anonymous removal (act-cb55ee's shape).
	if dirty := strings.TrimSpace(gitOut(t, actDir, "status", "--porcelain", "--", "ops")); dirty != "" {
		t.Errorf("working tree and index disagree about ops/ after a refused close:\n%s", dirty)
	}
}

// TestDocClaim_WithdrawnOpCommittedBySweepIsRetracted covers the other side
// of the same race (act-cb55ee). Where a rollback is still possible — the op
// file is written and the commit then fails — something may already have
// committed the file. Deleting it locally would leave HEAD advertising an op
// act refused, and the dirty deletion would be published later by a blind
// sweep as an anonymous removal. act commits the removal itself instead, so
// the working tree and HEAD agree and the retraction says what it is.
func TestDocClaim_WithdrawnOpCommittedBySweepIsRetracted(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	actDir := filepath.Join(root, ".act")

	// Stand in for the op whose commit failed: write a file under ops/ and
	// let a "sweep" commit it, which is precisely the state act finds
	// itself in when a sweep beats its own commit.
	opDir := filepath.Join(actDir, "ops", id, "2026-09")
	if err := os.MkdirAll(opDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	opPath := filepath.Join(opDir, "2026-09-01T00-00-00.000Z-deadbeef-close.json")
	if err := os.WriteFile(opPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write op: %v", err)
	}
	mustGit(t, actDir, "add", "--", "ops")
	mustGit(t, actDir, "commit", "-q", "--no-verify", "-m", "act-sync: sweep 1 uncommitted op file(s)")

	gops := gitops.NewActGitOps(actDir)
	tracked, err := gops.HeadTracksOpFile(opPath)
	if err != nil || !tracked {
		t.Fatalf("precondition: HeadTracksOpFile = %v, %v; want true, nil", tracked, err)
	}

	env := op.Envelope{OpType: "close", IssueID: id}
	if q := withdrawOpFile(gops, actDir, opPath, env); q == "" {
		t.Errorf("withdrawOpFile returned no quarantine path; the envelope must be preserved")
	}

	if _, err := os.Stat(opPath); !os.IsNotExist(err) {
		t.Errorf("op file still in ops/ after withdrawal: %v", err)
	}
	tracked, err = gops.HeadTracksOpFile(opPath)
	if err != nil {
		t.Fatalf("HeadTracksOpFile: %v", err)
	}
	if tracked {
		t.Errorf("HEAD still tracks the withdrawn op file; the removal was not committed")
	}
	if dirty := strings.TrimSpace(gitOut(t, actDir, "status", "--porcelain", "--", "ops")); dirty != "" {
		t.Errorf("working tree and HEAD disagree after withdrawal:\n%s", dirty)
	}
}

// TestDocClaim_InstallRunsRoundTripSmoke asserts install.sh's claim that it
// round-trips a scratch tracker with the binary it just installed. Verifying
// that a binary *runs* never caught a broken tracker loop; this is the step
// that does, so the claim and the wiring have to stay together.
func TestDocClaim_InstallRunsRoundTripSmoke(t *testing.T) {
	root := repoRootForDocClaim(t)
	smoke := filepath.Join(root, "scripts", "smoke-roundtrip.sh")
	info, err := os.Stat(smoke)
	if err != nil {
		t.Fatalf("scripts/smoke-roundtrip.sh missing: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("scripts/smoke-roundtrip.sh is not executable (mode %v); install.sh runs it directly", info.Mode())
	}
	body, err := os.ReadFile(filepath.Join(root, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	if !strings.Contains(string(body), "scripts/smoke-roundtrip.sh") {
		t.Errorf("install.sh no longer invokes scripts/smoke-roundtrip.sh; its verify step is back to proving only that the binary runs")
	}
	// The smoke must exercise the whole loop, not just a subset.
	sbody, err := os.ReadFile(smoke)
	if err != nil {
		t.Fatalf("read smoke: %v", err)
	}
	for _, step := range []string{"act init", "create", "--claim", "close", "push"} {
		if !strings.Contains(string(sbody), step) {
			t.Errorf("smoke-roundtrip.sh no longer covers %q", step)
		}
	}
}
