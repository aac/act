package cli

// act-40e336: behavior-level companion to TestNoDirectGitExec.
//
// The guard test proves no file *names* exec.Command("git"). This one
// proves the converted call sites actually get the overrides — i.e. that
// routing them through gitops was real and not cosmetic. It asserts on
// the argv git is invoked with, which is the only externally observable
// evidence that the maintenance/discovery prefix was applied: whether git
// then spawns a detached child is git's decision, not act's.

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aac/act/internal/gitops"
)

type argvRecorder struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *argvRecorder) runner(name string, args ...string) *exec.Cmd {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	r.mu.Unlock()
	return exec.Command(name, args...)
}

func (r *argvRecorder) find(t *testing.T, subcommand string) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		for _, tok := range c {
			if tok == subcommand {
				return c
			}
		}
	}
	t.Fatalf("no git invocation with subcommand %q; recorded: %v", subcommand, r.calls)
	return nil
}

func hasAll(argv []string, want ...string) bool {
	for _, w := range want {
		found := false
		for _, got := range argv {
			if got == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestNestedGitInvocationsCarryOverrides asserts that a git invocation
// built by the act handle carries BOTH groups the direct-exec sites used
// to skip: the foreground-maintenance overrides (act-5ed9f5) and the
// git-dir/work-tree discovery pinning (act-784b) — for the exact
// subcommands the four ticketed sites issue.
func TestNestedGitInvocationsCarryOverrides(t *testing.T) {
	actDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(actDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"commit (init bootstrap)", []string{"commit", "-q", "-m", "x"}},
		{"push (remote sync / add-upstream)", []string{"push", "origin-upstream", "main"}},
		{"fetch --dry-run (doctor probe)", []string{"fetch", "--dry-run", "origin"}},
		{"ls-files (pending ops)", []string{"ls-files", "--others"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &argvRecorder{}
			g := gitops.NewActGitOps(actDir)
			g.WithRunner(rec.runner)
			_, _ = g.RunGitCombined(tc.args...)

			argv := rec.find(t, tc.args[0])
			if !hasAll(argv, "-c", "maintenance.autoDetach=false", "gc.autoDetach=false") {
				t.Errorf("%s argv lacks the foreground-maintenance overrides: %v", tc.args[0], argv)
			}
			if !hasAll(argv, "--git-dir="+filepath.Join(actDir, ".git"), "--work-tree="+actDir) {
				t.Errorf("%s argv lacks the discovery pinning: %v", tc.args[0], argv)
			}
		})
	}
}

// TestHostGitInvocationsKeepHostConfig is the counterpart fence: act must
// NOT impose its maintenance preference on the caller's repo. The plain
// handle (what runHostGitIn and hostHasHEAD use) passes args through
// untouched. Without this, "route everything through the wrapper" would
// quietly become "override the host's git config too".
func TestHostGitInvocationsKeepHostConfig(t *testing.T) {
	repoRoot := t.TempDir()
	rec := &argvRecorder{}
	g := gitops.NewGitOps(repoRoot)
	g.WithRunner(rec.runner)
	_, _ = g.RunGitCombined("commit", "-q", "-m", "host")

	argv := rec.find(t, "commit")
	for _, forbidden := range []string{"maintenance.autoDetach=false", "gc.autoDetach=false"} {
		if hasAll(argv, forbidden) {
			t.Errorf("host-repo invocation carries act's %s override: %v", forbidden, argv)
		}
	}
	if hasAll(argv, "--git-dir="+filepath.Join(repoRoot, ".git")) {
		t.Errorf("host-repo invocation is git-dir pinned; it must use cwd discovery: %v", argv)
	}
}

// TestDoctorProbeSuppressesTerminalPrompt pins the behavior the doctor
// probes set by hand before they became gitops callers: a remote that
// wants credentials must fail fast, not block a non-interactive run on a
// password prompt. Losing this in the refactor would only show up as a
// hung doctor against an auth'd remote, so it gets its own assertion.
func TestDoctorProbeSuppressesTerminalPrompt(t *testing.T) {
	actDir := t.TempDir()
	gitDir := filepath.Join(actDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec := &argvRecorder{}
	g := nestedProbe(gitDir)
	g.WithRunner(rec.runner)
	if len(g.Env) == 0 {
		t.Fatal("doctor probe handle carries no extra environment")
	}
	found := false
	for _, e := range g.Env {
		if e == "GIT_TERMINAL_PROMPT=0" {
			found = true
		}
	}
	if !found {
		t.Errorf("doctor probe handle env = %v; want GIT_TERMINAL_PROMPT=0", g.Env)
	}
}
