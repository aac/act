package gitops

// act-40e336: the guard that keeps nested-repo git execution funnelled
// through one place.
//
// THE PROBLEM THIS EXISTS FOR. PR #4 taught (*GitOps).newCmd to disable
// git's detached auto-maintenance (see noDetachedMaintenance) and to pin
// repo discovery to `.act/.git` (act-784b). It fixed the dominant writer
// and the repo then CLAIMED detached auto-maintenance was handled. It was
// not: five other call sites in internal/cli exec'd git directly against
// the nested repo — a bootstrap `git commit`, two `git push`es, a `git
// fetch --dry-run`, and the rollback `git restore` — and every one of
// them opted out of the fix simply by not going through this package.
//
// Patching those five would have been whack-a-mole; the sixth would land
// the next time someone needed a git call the typed surface didn't cover.
// So the deliverable is structural (Andrew's reframe on the ticket: "do
// they need to be 4 separate code paths? that smells") — all nested-repo
// git execution routes through (*GitOps).newCmd, the four sites became
// callers, and this test fails the build if a new direct exec appears.
//
// WHAT IT CANNOT CATCH: a call site that reaches git some other way
// (a shell wrapper, os/exec aliased under another name, a helper in a
// package this test doesn't scan). It is a tripwire on the shape the bug
// actually took, not a proof.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// directGitExecRE matches `exec.Command("git"` / `exec.CommandContext(ctx,
// "git"` with any whitespace between the tokens.
var directGitExecRE = regexp.MustCompile(`exec\.Command(Context)?\(\s*(\w+,\s*)?"git"`)

// gitExecAllowlist names the non-test files permitted to exec git
// directly, each with the reason it cannot route through a repo handle.
// Adding an entry is a deliberate act: state why the handle does not
// apply, or the entry is a regression wearing a comment.
var gitExecAllowlist = map[string]string{
	// NOTE: gitops.go is deliberately NOT allowlisted. newCmd calls its
	// injectable `runner` (default exec.Command) rather than naming
	// exec.Command("git", ...) inline, so the wrapper itself does not
	// match — and if someone ever adds a literal direct exec there, the
	// guard should fire on it too.

	// `git config -f <file>` operates on a config FILE by path. It does
	// no repo discovery and runs no maintenance, so there is no handle
	// for it to belong to — .act/.git/config is often read before any
	// repo handle exists (or when .git is only a config file on disk).
	"internal/config/remote.go": "file-scoped `git config -f`, not a repo invocation",

	// `git clone <url> <staging>` CREATES the nested repo. There is no
	// git-dir to pin and no existing repo to configure until it finishes,
	// and clone does not fire auto-maintenance (measured on git 2.50.1).
	"internal/cli/bootstrap_worker.go": "clone that creates the nested repo; no handle exists yet",

	// Reads the operator's `user.email` from global git config to derive
	// a node id. Not scoped to any repo act owns.
	"cmd/act/main.go": "global `git config user.email` read for node-id derivation",

	// Test-support fixtures build throwaway repos; they are not act's
	// production write path.
	"internal/testfixtures/remote.go": "test fixture repo builder",
}

// TestNoDirectGitExec walks the tracked Go sources and fails when a
// non-test file outside the allowlist execs git directly.
func TestNoDirectGitExec(t *testing.T) {
	root := repoRootForGuard(t)

	var offenders []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		base := filepath.Base(path)
		if info.IsDir() {
			switch base {
			case ".git", ".act", ".claude", "bin", "testdata", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(base, ".go") || strings.HasSuffix(base, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if _, ok := gitExecAllowlist[rel]; ok {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if directGitExecRE.Match(body) {
			offenders = append(offenders, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	if len(offenders) > 0 {
		t.Errorf("files exec git directly instead of routing through a gitops handle: %v\n"+
			"  Nested-repo git invocations must go through (*GitOps).newCmd — via a typed method,\n"+
			"  or RunGit / RunGitCombined / RunGitCombinedTimeout for anything the typed surface\n"+
			"  doesn't cover. That is what applies the --git-dir/--work-tree pinning (act-784b)\n"+
			"  and the foreground-maintenance overrides (act-5ed9f5); a direct exec silently\n"+
			"  opts out of both, which is exactly the act-40e336 defect.\n"+
			"  If the call genuinely cannot use a handle, add it to gitExecAllowlist WITH the reason.",
			offenders)
	}
}

// TestGitExecAllowlistHasNoStaleEntries fails when an allowlisted file no
// longer execs git (or no longer exists). Without this, the allowlist
// silently accumulates permissions nobody needs — and a stale entry would
// pre-authorize a future direct exec in that file.
func TestGitExecAllowlistHasNoStaleEntries(t *testing.T) {
	root := repoRootForGuard(t)
	for rel, reason := range gitExecAllowlist {
		if reason == "" {
			t.Errorf("allowlist entry %q has no reason; every exception must state why", rel)
		}
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("allowlisted file %q is unreadable (%v); drop the entry", rel, err)
			continue
		}
		if !directGitExecRE.Match(body) {
			t.Errorf("allowlisted file %q no longer execs git directly; drop the entry so it "+
				"can't pre-authorize a future one", rel)
		}
	}
}

// repoRootForGuard walks up from the package dir to the module root.
func repoRootForGuard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
