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

// pushmaintInitActRepo makes a real nested repo with the named remotes
// configured (name → push URL), so push-destination resolution runs
// against git's own config rather than a guess.
func pushmaintInitActRepo(t *testing.T, remotes map[string]string) string {
	t.Helper()
	actDir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", actDir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for name, url := range remotes {
		if out, err := exec.Command("git", "-C", actDir, "remote", "add", name, url).CombinedOutput(); err != nil {
			t.Fatalf("git remote add %s: %v: %s", name, err, out)
		}
	}
	return actDir
}

// TestNestedPushReceivePackOnlyForLocalRemotes asserts at the argv
// boundary (act-25ba49) that a nested-repo push forces the remote
// receive-pack's maintenance into the foreground only where that is safe:
// local-path and file:// destinations carry --receive-pack; ssh, https and
// a remote with its own receivepack configured do not — a rewritten
// receive-pack command breaks GitHub-style ssh hosts.
func TestNestedPushReceivePackOnlyForLocalRemotes(t *testing.T) {
	const want = "--receive-pack=git -c maintenance.autoDetach=false receive-pack"
	bare := filepath.Join(t.TempDir(), "tracker.git")
	actDir := pushmaintInitActRepo(t, map[string]string{
		"localpath": bare,
		"fileurl":   "file://" + bare,
		"sshscp":    "git@github.com:example/tracker.git",
		"sshurl":    "ssh://git@example.com/tracker.git",
		"https":     "https://example.com/tracker.git",
		"ownrp":     bare,
	})
	if out, err := exec.Command("git", "-C", actDir, "config", "remote.ownrp.receivepack", "git-receive-pack").CombinedOutput(); err != nil {
		t.Fatalf("git config: %v: %s", err, out)
	}

	for _, tc := range []struct {
		name  string
		args  []string
		carry bool
	}{
		{"local path remote", []string{"push", "localpath", "main"}, true},
		{"file:// remote", []string{"push", "-u", "fileurl", "main"}, true},
		{"bare local path, no remote", []string{"push", bare, "main"}, true},
		{"scp-style ssh remote", []string{"push", "sshscp", "main"}, false},
		{"ssh:// remote", []string{"push", "sshurl", "main"}, false},
		{"https remote", []string{"push", "https", "main"}, false},
		{"remote with receivepack configured", []string{"push", "ownrp", "main"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &argvRecorder{}
			g := gitops.NewActGitOps(actDir)
			// Record every call, but never let the push itself reach the
			// network: the remote-resolution probes run real git, the push
			// is replaced by a no-op once its argv is captured.
			g.WithRunner(func(name string, args ...string) *exec.Cmd {
				cmd := rec.runner(name, args...)
				if hasAll(args, "push") {
					return exec.Command("true")
				}
				return cmd
			})
			_, _ = g.RunGitCombined(tc.args...)

			argv := rec.find(t, "push")
			if got := hasAll(argv, want); got != tc.carry {
				t.Errorf("push to %s: carries --receive-pack override = %v, want %v; argv: %v",
					tc.args, got, tc.carry, argv)
			}
			if tc.carry {
				pushAt, rpAt := -1, -1
				for i, a := range argv {
					if a == "push" && pushAt < 0 {
						pushAt = i
					}
					if a == want {
						rpAt = i
					}
				}
				if rpAt < pushAt {
					t.Errorf("--receive-pack must follow the push subcommand: %v", argv)
				}
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
