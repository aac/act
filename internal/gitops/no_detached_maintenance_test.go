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

// pushmaintTracing returns a runner that sets GIT_TRACE for every git it
// starts. The variable is inherited by the receive-pack a local push
// spawns, so the trace also records what the REMOTE side runs.
func pushmaintTracing(tracePath string) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		cmd := exec.Command(name, args...)
		cmd.Env = append(os.Environ(), "GIT_TRACE="+tracePath)
		return cmd
	}
}

// TestPush_RemoteMaintenanceRunsNoDetach is the behavioral half of
// act-25ba49. A push's detached auto-maintenance is spawned by the remote
// receive-pack, which the local `-c` overrides never reach; act therefore
// passes a foreground receive-pack for local remotes. This pushes to a
// real local bare repo with GIT_TRACE on and asserts the maintenance child
// receive-pack fires is `--no-detach` — the boundary that decides whether
// a process outlives the push.
func TestPush_RemoteMaintenanceRunsNoDetach(t *testing.T) {
	host := initHostWithIgnoredAct(t)
	actDir := filepath.Join(host, ".act")
	initNestedActRepo(t, actDir)

	bare := filepath.Join(t.TempDir(), "tracker.git")
	runGit(t, "", "init", "-q", "--bare", "-b", "main", bare)
	// Make the remote's auto-maintenance as eager as possible so its
	// receive-pack has a reason to fire it (the nudge the research used).
	runGit(t, bare, "config", "gc.auto", "1")
	runGit(t, actDir, "remote", "add", "origin", bare)

	tracePath := filepath.Join(t.TempDir(), "git-trace.log")
	g := NewActGitOps(actDir).WithRunner(pushmaintTracing(tracePath))
	if out, err := g.RunGitCombined("push", "origin", "main"); err != nil {
		t.Fatalf("push: %v: %s", err, out)
	}

	body, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("read git trace: %v", err)
	}
	trace := string(body)
	if !strings.Contains(trace, "receive-pack") {
		t.Fatalf("trace shows no receive-pack; the push did not use the local transport:\n%s", trace)
	}
	if !strings.Contains(trace, "maintenance run --auto") {
		t.Skipf("this git's receive-pack does not fire auto-maintenance; nothing to detach\n%s", trace)
	}
	for _, line := range strings.Split(trace, "\n") {
		if strings.Contains(line, "maintenance run --auto") && strings.Contains(line, "--detach") &&
			!strings.Contains(line, "--no-detach") {
			t.Errorf("remote auto-maintenance was detached; a push to a local tracker can return while a git daemon still writes into it.\ntrace:\n%s", trace)
			return
		}
	}
	if !strings.Contains(trace, "--no-detach") {
		t.Errorf("remote auto-maintenance not forced into the foreground.\ntrace:\n%s", trace)
	}
}
