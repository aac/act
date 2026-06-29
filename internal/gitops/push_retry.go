package gitops

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ErrPushExhausted is returned by PushWithRetry when the retry cap has
// been exceeded. The error wraps a synthetic envelope-shaped detail map
// for the upstream cli.Envelope translation (ticket 3a wires the
// translation; this ticket just defines the surface).
//
// The error message is stable; the detail keys (`retry_count`,
// `shallow_unshallow_attempted`, `last_error`) are pinned by the spec.
var ErrPushExhausted = errors.New("gitops: push retries exhausted")

// PushExhaustedError is the structured form of ErrPushExhausted. It
// carries the detail fields that the cli envelope reads into
// `details.retry_count` and `details.shallow_unshallow_attempted`.
//
// Tests use errors.As to inspect the struct fields without doing string
// parsing on the error message.
type PushExhaustedError struct {
	RetryCount                int
	ShallowUnshallowAttempted bool
	LastError                 error
}

func (e *PushExhaustedError) Error() string {
	return fmt.Sprintf("%v: retry_count=%d shallow_unshallow_attempted=%t: %v",
		ErrPushExhausted, e.RetryCount, e.ShallowUnshallowAttempted, e.LastError)
}

func (e *PushExhaustedError) Unwrap() error { return ErrPushExhausted }

// PushOpts controls the retry loop. The zero value uses sensible
// defaults documented inline.
type PushOpts struct {
	// MaxRetries caps the number of full "push, detect rejection,
	// fetch-rebase, push" rounds. Default 5 when zero.
	MaxRetries int

	// BackoffBase is the initial backoff between retries. Defaults to
	// 100ms when zero. Doubled each retry, capped at BackoffCap.
	//
	// Implementer's note (ticket 2 explicit "implementer's call"):
	// 100ms base / 1s cap was chosen because:
	//   - Typical contention windows on the bare-repo-on-fs fixture are
	//     in the low-millisecond range, so a 100ms base gives the
	//     concurrent writer time to finish without being so long that
	//     a deterministic test stalls.
	//   - 1s cap matches the v4 brief's "capped at 1s" language verbatim.
	//   - Exponential growth (100ms → 200 → 400 → 800 → 1000) means the
	//     full 5-retry budget is ~2.5s of sleep in the worst case, fast
	//     enough that tests don't need to skip under -short.
	BackoffBase time.Duration

	// BackoffCap is the max backoff between retries. Defaults to 1s.
	BackoffCap time.Duration

	// Sleep is the sleep function used between retries. Defaults to
	// time.Sleep; tests inject a no-op or recording variant.
	Sleep func(time.Duration)
}

// pushOptsDefaults returns a PushOpts with all zero-fields replaced by
// their documented defaults. Pure function so callers can compose with
// their own partial override.
func pushOptsDefaults(o PushOpts) PushOpts {
	if o.MaxRetries == 0 {
		o.MaxRetries = 5
	}
	if o.BackoffBase == 0 {
		o.BackoffBase = 100 * time.Millisecond
	}
	if o.BackoffCap == 0 {
		o.BackoffCap = 1 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	return o
}

// ACT_TEST_FAIL_PUSH_AFTER is a test-only env var that forces push
// attempts at-or-after the Nth call to fail silently — as if
// `receive.denyCurrentBranch=updateInstead` had rejected the receive
// against a dirty working tree. The env var's value is the integer N
// (1-indexed). When set, the helper increments a per-process counter on
// each push attempt and, when the counter is >= N, suppresses the
// underlying `git push` and synthesises a "silent rejection" outcome —
// the push command exits 0 but the reachability check below detects
// local HEAD is not an ancestor of origin and triggers fetch-and-rebase.
//
// Sticky semantics: once the counter crosses the threshold every
// subsequent attempt in this process fails too. This drives exhaustion
// deterministically — setting N=1 causes all 5 default retries to be
// silently rejected and PushWithRetry returns *PushExhaustedError{
// RetryCount=5}. The previous one-shot semantics (`counter == N`) made
// the env var useful only for "one fail then succeed" scenarios; ticket
// 3a's integration tests need every attempt to fail to assert the
// exhaustion envelope, hence the switch to >=.
//
// This hook is consulted on every call into PushWithRetry's inner
// attempt loop. Setting N=999 (or any value beyond the retry cap) is a
// no-op; the hook is a fault-injector, not a forcing function.
//
// Tests that want the one-shot "first attempt fails, rest succeed"
// pattern unset the env var inside their Sleep hook so the second
// attempt sees no fault — see TestPushWithRetry_SilentRejection_
// RecoversWithinCap for the canonical idiom.
const envFailPushAfter = "ACT_TEST_FAIL_PUSH_AFTER"

// pushAttemptCounter tracks the global push-attempt count for the
// fault-injection hook. Using a package-level atomic is necessary because
// tests may run in parallel goroutines with fault-injection env vars set,
// and the race detector flags concurrent plain-int increments.
var pushAttemptCounter atomic.Int64

// PushWithRetry implements the v4 brief's "fetch, rebase, push, verify"
// loop:
//
//  1. `git push origin <branch>`.
//  2. On non-fast-forward rejection: invoke FetchAndRebase; goto 1.
//  3. On apparent success: `git fetch origin <branch>`; check that
//     local HEAD is an ancestor of origin/<branch>. If not, the receive
//     was silently rejected (e.g. `receive.denyCurrentBranch=updateInstead`
//     with a dirty working tree); treat as non-fast-forward; goto 2.
//  4. After MaxRetries full rounds with exponential backoff, return
//     *PushExhaustedError (which unwraps to ErrPushExhausted).
//
// FetchAndRebase failures bubble up: ErrShallowExhausted triggers a
// single `--unshallow` attempt (handled inside FetchAndRebase); the
// helper records `ShallowUnshallowAttempted=true` on the exhaustion
// error so callers can surface `details.shallow_unshallow_attempted`.
//
// Concurrency: safe to call from multiple goroutines with the SAME
// *GitOps so long as they operate on distinct working trees (a single
// working tree serialises on git index regardless). The bare remote
// itself is fine — git's receive-pack does the right thing under
// concurrent push.
func (g *GitOps) PushWithRetry(branch string, opts PushOpts) error {
	opts = pushOptsDefaults(opts)
	if branch == "" {
		return fmt.Errorf("gitops: PushWithRetry: empty branch")
	}

	var lastErr error
	shallowAttempted := false

	for round := 0; round < opts.MaxRetries; round++ {
		if round > 0 {
			opts.Sleep(backoffFor(opts.BackoffBase, opts.BackoffCap, round-1))
		}

		// Attempt push (or simulated silent-rejection under fault injection).
		failThisAttempt := shouldFaultInjectPush()
		var pushOut string
		var pushErr error
		if failThisAttempt {
			// Simulate the silent-rejection path: pretend git push
			// returned 0 but the remote actually has older history.
			// The reachability check below then catches it.
			pushOut = ""
			pushErr = nil
		} else {
			pushOut, pushErr = g.runCombined("push", "origin", branch)
		}

		if pushErr != nil {
			// Non-fast-forward rejection → fetch and rebase, then retry.
			if isNonFastForward(pushOut, pushErr) {
				lastErr = fmt.Errorf("non-fast-forward: %v (output: %s)",
					pushErr, strings.TrimSpace(pushOut))
				rerr := g.FetchAndRebase(branch)
				if errors.Is(rerr, ErrShallowExhausted) || errors.Is(rerr, ErrShallowRecovered) {
					shallowAttempted = true
				}
				if rerr != nil && !errors.Is(rerr, ErrShallowRecovered) {
					// Carry the rebase error forward; loop continues to
					// next retry (so the cap is respected even if the
					// rebase keeps failing — that's the exhaustion case
					// in the spec).
					lastErr = rerr
				}
				continue
			}
			// Other push failure (e.g. fetch_failed-like cases).
			// Return immediately: this isn't a contention class.
			return fmt.Errorf("gitops: push: %w (output: %s)", pushErr, strings.TrimSpace(pushOut))
		}

		// Apparent success → verify reachability to catch silent rejection.
		if reachable, verr := g.verifyPushed(branch); verr != nil {
			lastErr = verr
			continue
		} else if !reachable {
			// Silent rejection (updateInstead dirty-tree, or our
			// fault-injection hook). Treat as non-fast-forward.
			lastErr = errors.New("silent rejection: local HEAD not reachable from origin/" + branch)
			rerr := g.FetchAndRebase(branch)
			if errors.Is(rerr, ErrShallowExhausted) || errors.Is(rerr, ErrShallowRecovered) {
				shallowAttempted = true
			}
			if rerr != nil && !errors.Is(rerr, ErrShallowRecovered) {
				lastErr = rerr
			}
			continue
		}

		// Success.
		return nil
	}

	return &PushExhaustedError{
		RetryCount:                opts.MaxRetries,
		ShallowUnshallowAttempted: shallowAttempted,
		LastError:                 lastErr,
	}
}

// verifyPushed runs `git fetch origin <branch>` and checks whether
// local HEAD is an ancestor of origin/<branch>. Returns (true, nil) when
// the push truly landed; (false, nil) when the remote rejected silently;
// (false, err) on a fetch failure (which the caller treats as a retry-
// worthy condition because contention races can sometimes look like
// transient fetch failures).
func (g *GitOps) verifyPushed(branch string) (bool, error) {
	if out, err := g.runCombined("fetch", "origin", branch); err != nil {
		return false, fmt.Errorf("verify-fetch: %w (output: %s)", err, strings.TrimSpace(out))
	}
	// Local HEAD sha.
	localOut, lerr := g.runCombined("rev-parse", "HEAD")
	if lerr != nil {
		return false, fmt.Errorf("verify rev-parse HEAD: %w (output: %s)", lerr, strings.TrimSpace(localOut))
	}
	local := strings.TrimSpace(localOut)
	// Is local HEAD an ancestor of origin/<branch>? `--is-ancestor`
	// exits 0 when yes, 1 when no, 128 on error.
	remote := "origin/" + branch
	out, err := g.runCombined("merge-base", "--is-ancestor", local, remote)
	if err == nil {
		return true, nil
	}
	// Distinguish "no, but git ran" (exit 1) from any other failure.
	// Both runCombined and exec.ExitError surface non-zero through err;
	// we treat exit 1 as the "no" answer and any other as an error.
	if isExit1(err) {
		return false, nil
	}
	return false, fmt.Errorf("verify ancestor: %w (output: %s)", err, strings.TrimSpace(out))
}

// isNonFastForward classifies the push error as a non-fast-forward
// rejection (the recoverable contention case). The check is generous —
// git's stderr varies across versions — but anchored on the unique
// substrings that no other failure class shares.
func isNonFastForward(out string, err error) bool {
	if err == nil {
		return false
	}
	combined := strings.ToLower(out + " " + err.Error())
	if strings.Contains(combined, "non-fast-forward") {
		return true
	}
	if strings.Contains(combined, "rejected") && strings.Contains(combined, "fetch first") {
		return true
	}
	if strings.Contains(combined, "updates were rejected") {
		return true
	}
	// Race between two concurrent pushers can manifest as "cannot lock
	// ref" / "is at X but expected Y" / "failed to update ref" — git's
	// receive-pack saw a different value than the pusher expected at
	// the moment of compare-and-swap. Treat as a recoverable
	// contention class.
	if strings.Contains(combined, "cannot lock ref") {
		return true
	}
	if strings.Contains(combined, "failed to update ref") {
		return true
	}
	return false
}

// isExit1 reports whether err corresponds to a process exit code of 1.
// Returns false on any other failure (signal kill, exec error, etc.).
func isExit1(err error) bool {
	type exitCoder interface {
		ExitCode() int
	}
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode() == 1
	}
	return false
}

// backoffFor computes the backoff duration for the given retry index
// (0-indexed, so retry 0 uses BackoffBase). Doubles each retry, capped
// at BackoffCap.
func backoffFor(base, cap time.Duration, retry int) time.Duration {
	d := base
	for i := 0; i < retry; i++ {
		d *= 2
		if d > cap {
			return cap
		}
	}
	if d > cap {
		return cap
	}
	return d
}

// shouldFaultInjectPush consults the ACT_TEST_FAIL_PUSH_AFTER env var on
// every push attempt. Returns true when the current attempt counter is
// at-or-above the configured value, simulating a silent-reject (sticky;
// every subsequent attempt also fails).
//
// Counter increments unconditionally; setting the env var to "0" or an
// invalid value disables the hook. Tests that want one-shot behavior
// unset the env var inside their Sleep callback so the second attempt
// sees no fault.
func shouldFaultInjectPush() bool {
	count := pushAttemptCounter.Add(1)
	v := os.Getenv(envFailPushAfter)
	if v == "" {
		return false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return false
	}
	return count >= int64(n)
}

// ResetPushAttemptCounter resets the global fault-injection counter to
// 0. Tests that rely on the env-var hook MUST call this in setup so
// previously-run tests don't bias the count. Production callers never
// invoke this.
func ResetPushAttemptCounter() {
	pushAttemptCounter.Store(0)
}
