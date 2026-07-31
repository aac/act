package gitops

// Regression tests for act-5ed9f5.
//
// Mechanism: `git commit` ends by firing `git maintenance run --auto
// --quiet --detach`. The `--detach` daemonizes that child, so it outlives
// the `git commit` act waited on and keeps creating/removing lock files
// directly inside `.git`. Anything that removes the tree right after the
// commit — `t.TempDir()` cleanup in the harvest tests — races it and gets
// `unlinkat .../.act/.git: directory not empty`.
//
// The fix makes act's nested-repo invocations pass
// `-c maintenance.autoDetach=false -c gc.autoDetach=false`, so git runs
// maintenance in the FOREGROUND and `git commit` waits for it. These tests
// assert both halves: the argv act builds, and the `--no-detach` git
// actually ends up firing.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGitArgs_NestedRepoForcesForegroundMaintenance asserts the argv act
// builds for a nested-`.act/.git` handle carries the autoDetach overrides,
// and that they precede the subcommand (git rejects `-c` after it).
func TestGitArgs_NestedRepoForcesForegroundMaintenance(t *testing.T) {
	host := initHostWithIgnoredAct(t)
	actDir := filepath.Join(host, ".act")
	initNestedActRepo(t, actDir)

	got := NewActGitOps(actDir).gitArgs([]string{"commit", "-m", "x"})

	joined := strings.Join(got, " ")
	for _, want := range []string{
		"-c maintenance.autoDetach=false",
		"-c gc.autoDetach=false",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("nested-repo argv missing %q:\n  %v", want, got)
		}
	}
	// Every override has to sit ahead of the subcommand.
	subcommand := -1
	for i, a := range got {
		if a == "commit" {
			subcommand = i
			break
		}
	}
	if subcommand < 0 {
		t.Fatalf("subcommand 'commit' not present in argv: %v", got)
	}
	for i, a := range got {
		if (a == "maintenance.autoDetach=false" || a == "gc.autoDetach=false") && i > subcommand {
			t.Errorf("override %q at index %d is after the subcommand at %d: %v",
				a, i, subcommand, got)
		}
	}
}

// TestGitArgs_HostRepoHandleUnchanged pins the deliberate scope limit: a
// handle with no nested git-dir (a caller's own repo) keeps the caller's
// git configuration, background maintenance included. Widening the fix to
// host repos would make act's commits block on a foreground gc in a repo
// act does not own.
func TestGitArgs_HostRepoHandleUnchanged(t *testing.T) {
	host := initHostWithIgnoredAct(t)

	got := NewGitOps(host).gitArgs([]string{"commit", "-m", "x"})

	if len(got) != 3 || got[0] != "commit" {
		t.Errorf("host-repo argv was rewritten; want [commit -m x], got %v", got)
	}
}

// TestCommit_FiresMaintenanceWithNoDetach is the behavioral half: it runs a
// real Commit through ActGitOps with GIT_TRACE on and asserts that the
// auto-maintenance child git fires is `--no-detach`. Argv alone would not
// catch a future git that stops honoring the config key — this asserts at
// the boundary that actually decides whether a process outlives us.
func TestCommit_FiresMaintenanceWithNoDetach(t *testing.T) {
	host := initHostWithIgnoredAct(t)
	actDir := filepath.Join(host, ".act")
	initNestedActRepo(t, actDir)

	opPath := filepath.Join(actDir, "ops", "act-abcdef", "2026-05", "op.json")
	writeFile(t, opPath, "{\"op_type\":\"create\"}\n")

	tracePath := filepath.Join(t.TempDir(), "git-trace.log")
	tracing := func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "GIT_TRACE="+tracePath)
		return cmd
	}

	g := NewActGitOps(actDir).WithRunner(tracing)
	if err := g.StageOpFile(opPath); err != nil {
		t.Fatalf("StageOpFile: %v", err)
	}
	if err := g.Commit("act-op: (act-abcdef) create"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	body, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read git trace: %v", err)
	}
	trace := string(body)
	if !strings.Contains(trace, "maintenance run --auto") {
		t.Skipf("this git does not fire auto-maintenance from commit; nothing to detach\n%s", trace)
	}
	// The quiet flag varies with the commit's own verbosity; the detach
	// decision is the load-bearing token.
	if !strings.Contains(trace, "--no-detach") {
		t.Errorf("auto-maintenance was not forced into the foreground; act's commit can return while a git daemon still writes into .act/.git.\ntrace:\n%s", trace)
	}
}
