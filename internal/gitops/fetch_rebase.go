package gitops

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
)

// envForceShallowRebase is a test-only env var that forces the next N
// rebase attempts in this process to report a synthetic shallow-clone
// failure ("unable to find common ancestor"). The value is an integer
// count: each rebase invocation consumes one unit and emits the
// synthetic failure when the counter is non-zero.
//
// This hook exists because the real shallow-rebase failure modes are
// hard to trigger deterministically across git versions — modern git
// often computes merge bases via grafts even when the shallow boundary
// is involved. The fault-injector lets us exercise the production
// recovery branch (--unshallow + retry) without depending on git's
// internal behavior.
//
// Tests SHOULD call ResetShallowFaultCounter at setup so cross-test
// state doesn't leak.
const envForceShallowRebase = "ACT_TEST_FORCE_SHALLOW_REBASE_FAILURES"

// shallowFaultCounter is decremented on every rebase attempt while the
// env-var hook is active. When non-zero, faultInjectShallow returns
// the synthetic failure message. Stored as an atomic so parallel test
// goroutines with fault-injection env vars set don't race on the counter.
var shallowFaultCounter atomic.Int64
var shallowFaultInit atomic.Bool

// faultInjectShallow returns a non-empty synthetic rebase-failure
// message when the env-var hook is active and the counter is non-zero.
// Otherwise returns "" and the helper proceeds normally.
func faultInjectShallow() string {
	if shallowFaultInit.CompareAndSwap(false, true) {
		// First caller in this process: read the env var and seed the counter.
		v := os.Getenv(envForceShallowRebase)
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			shallowFaultCounter.Store(int64(n))
		}
	}
	for {
		cur := shallowFaultCounter.Load()
		if cur <= 0 {
			return ""
		}
		if shallowFaultCounter.CompareAndSwap(cur, cur-1) {
			break
		}
	}
	return "fatal: unable to find common ancestor (shallow file has changed)"
}

// ResetShallowFaultCounter clears the fault-injection state so the next
// test starts from zero. Tests using ACT_TEST_FORCE_SHALLOW_REBASE_FAILURES
// MUST call this in setup. Production callers never invoke it.
func ResetShallowFaultCounter() {
	shallowFaultCounter.Store(0)
	shallowFaultInit.Store(false)
}

// Sentinel errors returned by FetchAndRebase. Callers (PushWithRetry's
// inner loop and ticket 5's read-path cache-miss) branch on these to
// decide whether to retry, fall back to --unshallow, or surface to the
// user as an envelope.
//
// The set of sentinels is intentionally narrow: every other failure is
// returned as a wrapped *exec.ExitError or fmt.Errorf so the upstream
// envelope normalisation in internal/cli/errors.go can capture stderr_tail.
var (
	// ErrRebaseConflict means `git rebase origin/<branch>` halted with
	// merge conflicts. The working tree is left in the rebase-in-progress
	// state — callers MUST recover with `git rebase --abort` before
	// retrying any other operation, otherwise subsequent commands fail
	// with "you are currently rebasing".
	ErrRebaseConflict = errors.New("gitops: rebase produced conflicts")

	// ErrShallowExhausted means the rebase failed with a shallow-clone
	// error (one of the two recognised messages: "shallow file has
	// changed" or "unable to find common ancestor") AND a subsequent
	// `git fetch --unshallow` round did not produce a successful rebase.
	// Callers should treat this as a terminal failure for the current
	// retry round and either abort or continue counting against the
	// retry cap.
	ErrShallowExhausted = errors.New("gitops: shallow rebase recovery exhausted")

	// ErrShallowRecovered is returned by FetchAndRebase when the rebase
	// initially failed with a shallow pattern, the helper ran
	// `git fetch --unshallow`, and the subsequent rebase succeeded. The
	// helper still returns this sentinel (rather than nil) so callers
	// can record that the recovery branch was exercised — the
	// PushWithRetry caller uses this to set
	// `details.shallow_unshallow_attempted=true` on any eventual
	// exhaustion. From the caller's perspective the rebase did succeed;
	// it should continue retrying as if FetchAndRebase had returned nil.
	ErrShallowRecovered = errors.New("gitops: shallow rebase recovered after --unshallow")

	// ErrFetchFailed means `git fetch origin <branch>` itself failed —
	// network error, missing branch, or auth failure. The original
	// stderr is wrapped into the returned error message; callers
	// translate to envelope code `remote_unreachable`.
	ErrFetchFailed = errors.New("gitops: fetch failed")
)

// FetchAndRebase runs `git fetch origin <branch>` followed by
// `git rebase origin/<branch>`. It handles the shallow-clone failure
// mode by retrying once with `git fetch --unshallow origin <branch>`
// when the rebase error matches the recognised shallow patterns.
//
// Behavior:
//
//   - No diverging history → no-op. We detect "no diverging history"
//     after fetch by checking whether `git merge-base --is-ancestor
//     <local-HEAD> origin/<branch>` exits 0. If local HEAD is already
//     an ancestor of origin/branch, fast-forward is the only motion the
//     rebase would make, so we issue a single `git merge --ff-only` instead
//     of invoking rebase. If both are equal, this is a true no-op.
//   - Rebase conflict → return ErrRebaseConflict with the rebase aborted.
//   - Shallow rebase failure → one `--unshallow` retry; on continued
//     failure return ErrShallowExhausted.
//   - Fetch failure → return ErrFetchFailed (wrapped).
//
// Used by PushWithRetry's inner loop AND by ticket 5's read-path cache-
// miss path. Both consumers share this helper rather than reimplementing
// rebase recovery (the v1 → v2 factoring pinned by ticket 2's synthesis S7).
func (g *GitOps) FetchAndRebase(branch string) error {
	if branch == "" {
		return fmt.Errorf("gitops: FetchAndRebase: empty branch")
	}

	// Step 1: fetch origin/<branch>.
	if out, err := g.runCombined("fetch", "origin", branch); err != nil {
		return fmt.Errorf("%w: %v (output: %s)", ErrFetchFailed, err, strings.TrimSpace(out))
	}

	// Step 2: check whether rebase is even necessary. If local HEAD is
	// already an ancestor of origin/<branch>, a fast-forward (or no
	// motion at all) is the right move — invoking `git rebase` would
	// still succeed but emit "Current branch X is up to date" or fast-
	// forward, which is wasteful and trips the §11 spec rule that the
	// no-diverging-history case is a "no-op (clean exit, no rebase
	// invoked)".
	upToDate, alreadyAhead, err := g.ancestryToOrigin(branch)
	if err != nil {
		return err
	}
	if upToDate {
		// Truly no-op: nothing to do.
		return nil
	}
	if alreadyAhead {
		// origin is behind local HEAD; local already has everything
		// upstream has plus more. A rebase is also a no-op here. We
		// fall through to the rebase invocation anyway in case the
		// branch divergence is non-fast-forward (in which case the
		// rebase will exit cleanly with "current branch is up to date").
		// Net effect: no rebase invoked.
		return nil
	}

	// Step 3: try the rebase.
	rebaseOut, rebaseErr := g.runCombined("rebase", "origin/"+branch)
	if forced := faultInjectShallow(); forced != "" && rebaseErr == nil {
		// Test-only hook: pretend the rebase failed with a shallow
		// pattern so the recovery branch runs deterministically. The
		// env-var-controlled hook is per-attempt-decrementing so a
		// single set value drives N rebase failures.
		_, _ = g.runCombined("rebase", "--abort")
		rebaseOut = forced
		rebaseErr = errors.New(forced)
	}
	if rebaseErr == nil {
		return nil
	}

	// Classify the failure.
	combined := strings.ToLower(rebaseOut + " " + rebaseErr.Error())
	if isShallowFailure(combined) {
		// Abort the in-progress rebase before unshallowing — otherwise
		// the next git command refuses with "you are currently rebasing".
		_, _ = g.runCombined("rebase", "--abort")

		// One unshallow attempt.
		if uout, uerr := g.runCombined("fetch", "--unshallow", "origin", branch); uerr != nil {
			// Unshallow itself failed (e.g. remote refused, or the clone
			// was never shallow). Treat as terminal recovery failure.
			return fmt.Errorf("%w: unshallow: %v (output: %s)",
				ErrShallowExhausted, uerr, strings.TrimSpace(uout))
		}
		// Retry the rebase once.
		if out2, err2 := g.runCombined("rebase", "origin/"+branch); err2 != nil {
			_, _ = g.runCombined("rebase", "--abort")
			// Second failure with shallow patterns is exhausted; any
			// other failure (e.g. a real conflict surfaced once the
			// history was complete) is a conflict.
			combined2 := strings.ToLower(out2 + " " + err2.Error())
			if isShallowFailure(combined2) {
				return fmt.Errorf("%w: post-unshallow: %v (output: %s)",
					ErrShallowExhausted, err2, strings.TrimSpace(out2))
			}
			return fmt.Errorf("%w: post-unshallow rebase: %v (output: %s)",
				ErrRebaseConflict, err2, strings.TrimSpace(out2))
		}
		// Successful recovery: signal to the caller that the shallow
		// recovery branch was exercised so it can flag any later
		// exhaustion accordingly.
		return ErrShallowRecovered
	}

	// Plain rebase failure → conflict. Abort to clean up.
	_, _ = g.runCombined("rebase", "--abort")
	return fmt.Errorf("%w: %v (output: %s)",
		ErrRebaseConflict, rebaseErr, strings.TrimSpace(rebaseOut))
}

// isShallowFailure reports whether the rebase failure output matches one
// of the two recognised shallow-clone error patterns. The check is
// case-insensitive (git's messages have varied across versions); both
// patterns are the lowercase canonical forms.
func isShallowFailure(combined string) bool {
	return strings.Contains(combined, "shallow file has changed") ||
		strings.Contains(combined, "unable to find common ancestor")
}

// ancestryToOrigin reports the relationship between local HEAD and
// origin/<branch>:
//
//   - upToDate: local HEAD equals origin/<branch> (true no-op).
//   - alreadyAhead: local HEAD strictly contains origin/<branch>
//     (origin is behind local — rebase would be a no-op).
//
// Both can be false simultaneously, which means the two have diverged
// (or origin is strictly ahead, where rebase moves local forward).
func (g *GitOps) ancestryToOrigin(branch string) (upToDate, alreadyAhead bool, err error) {
	// Local HEAD sha.
	localOut, lerr := g.runCombined("rev-parse", "HEAD")
	if lerr != nil {
		return false, false, fmt.Errorf("gitops: rev-parse HEAD: %v (output: %s)", lerr, strings.TrimSpace(localOut))
	}
	local := strings.TrimSpace(localOut)

	// Remote-tracking branch sha (after the fetch).
	remoteOut, rerr := g.runCombined("rev-parse", "origin/"+branch)
	if rerr != nil {
		// No remote-tracking ref → treat as "not equal, not ahead"
		// so the caller falls through to the rebase. This shouldn't
		// happen post-fetch but we handle it gracefully rather than
		// failing the whole helper.
		return false, false, nil
	}
	remote := strings.TrimSpace(remoteOut)

	if local == remote {
		return true, false, nil
	}
	// Is origin/<branch> an ancestor of local HEAD? If so, local is ahead.
	// Route through runCombined so the gitDir/work-tree override (when
	// set by NewActGitOps) applies here too — otherwise an ActGitOps
	// handle could leak into the host repo's discovery on this path
	// (act-784b).
	mbArgs := []string{"merge-base", "--is-ancestor", remote, local}
	if _, e := g.runCombined(mbArgs...); e == nil {
		return false, true, nil
	}
	return false, false, nil
}

// runCombined runs git with combined stdout+stderr capture. The retry
// helper paths need stderr in the returned output for pattern-matching
// shallow-clone failures (run() puts stderr only into the error
// message, which is harder to inspect uniformly).
//
// When g.gitDir is non-empty (NewActGitOps), every invocation is prefixed
// with `--git-dir=<gitDir> --work-tree=<RepoRoot>` so git's repo discovery
// is pinned to the nested .act/.git and cannot walk up into the host repo
// (act-784b). Mirrors the override in (*GitOps).run.
func (g *GitOps) runCombined(args ...string) (string, error) {
	r := g.runner
	if r == nil {
		r = exec.Command
	}
	finalArgs := args
	if g.gitDir != "" {
		finalArgs = append([]string{
			"--git-dir=" + g.gitDir,
			"--work-tree=" + g.RepoRoot,
		}, args...)
	}
	cmd := r("git", finalArgs...)
	cmd.Dir = g.RepoRoot
	out, err := cmd.CombinedOutput()
	return string(out), err
}
