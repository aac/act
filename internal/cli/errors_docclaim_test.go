package cli

// Doc-claim tests for the Phase 2 push-retry error codes (act-9f3fc5,
// act-915a88).
//
// Both `push_exhausted` and `remote_unreachable` are documented in
// docs/spec.md's error table; the registry in docs_sweep_test.go
// pins each to the test function below.
//
// Act-915a88 rewrote these tests from constant-equality / spec-prose checks
// to behavioral assertions at the CLI boundary. The original tests checked
// that ErrPushExhausted == "push_exhausted" and that the spec text contained
// the substring — neither of which would catch a regression where the code is
// renamed at the *emission* site (close.go), or where the mapping from
// PushExhaustedError to exit=4 is accidentally removed.
//
// TestDocClaim_Errors_PushExhausted now fault-injects to produce push
// exhaustion and asserts the RunClose exit code + envelope code.
//
// TestDocClaim_Errors_RemoteUnreachable (act-6d9546) drives the genuine
// emitter of `remote_unreachable` at the user-visible boundary:
// `act bootstrap-worker --from-remote <bad-url>`. A non-timeout clone failure
// exits 3 with envelope `remote_unreachable` carrying details.url and
// details.stderr_tail. The close/push path canNOT emit this code — PushWithRetry
// stores a mid-loop fetch failure in lastErr and retries to exhaustion, so a
// broken remote surfaces as push_exhausted, never remote_unreachable. The
// previous version of this test called closeErrorForPushFailure directly
// against a synthetic ErrFetchFailed-wrapping error, asserting an exit-4
// mapping that no real input could ever produce; that classifier branch and
// the spec's exit-4 claim were removed when the reachability was traced.
// Deleting either test trips the sweep registry and breaks the build.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/gitops"
)

// TestDocClaim_Errors_PushExhaustedRetired pins the act-89a595 contract
// that replaced the `push_exhausted` envelope: when every push attempt
// fails (fault-injected via ACT_TEST_FAIL_PUSH_AFTER=1) on an op that
// HAS been committed, `act close` exits 0, reports push_deferred on its
// result, queues the commit to .act/.pending-pushes, and warns loudly on
// stderr — instead of the old exit-4 `push_exhausted` envelope, which
// reported failure on a write that had already landed.
//
// Asserted at the RunClose Go API boundary — the same surface the CLI
// wires to exit code + JSON output. The paired not-committed side of the
// boundary is TestPublish_CommitFailureStillExitsNonZero.
func TestDocClaim_Errors_PushExhaustedRetired(t *testing.T) {
	gitops.ResetPushAttemptCounter()

	// Build a repo + remote-configured nested .act/ using the same
	// fixture as the push-integration tests (makeRepoWithRemoteOrigin
	// is defined in push_integration_test.go, same package).
	root, _ := makeRepoWithRemoteOrigin(t)

	// Seed an open issue.
	createOut, code := RunCreate(root, CreateOptions{Title: "push-exhausted-probe", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	// Reset the fault-injection counter after the create push so the
	// exhaustion counter starts fresh at the close call.
	gitops.ResetPushAttemptCounter()
	// ACT_TEST_FAIL_PUSH_AFTER=1 causes every push attempt to silently
	// fail, exhausting all 5 retries -> PushExhaustedError.
	t.Setenv("ACT_TEST_FAIL_PUSH_AFTER", "1")

	var stderr bytes.Buffer
	out, exitCode := RunClose(root, CloseOptions{ID: id, Stderr: &stderr})

	// The op committed, so the close succeeds despite the dead remote.
	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0 (committed op must not fail on push); out=%+v", exitCode, out)
	}
	res, ok := out.(CloseResult)
	if !ok {
		t.Fatalf("output type = %T, want CloseResult", out)
	}
	if !res.Committed {
		t.Fatalf("Committed = false, want true")
	}
	if !res.PushDeferred {
		t.Errorf("PushDeferred = false, want true (the push failed and was deferred)")
	}
	if res.PushDeferredReason == "" {
		t.Errorf("PushDeferredReason is empty; want the underlying push failure")
	}

	// The warning must be unmissable and must say the op is queued, not pushed.
	warn := stderr.String()
	for _, want := range []string{"WARNING", "COMMITTED LOCALLY", "NOT PUSHED", ".act/.pending-pushes", "exit status is 0"} {
		if !strings.Contains(warn, want) {
			t.Errorf("stderr warning missing %q:\n%s", want, warn)
		}
	}

	// And the deferral must be durably queued so a later write publishes it.
	pending, err := ReadPendingPushes(filepath.Join(root, ".act"))
	if err != nil {
		t.Fatalf("ReadPendingPushes: %v", err)
	}
	if len(pending) == 0 {
		t.Errorf("no .pending-pushes entry recorded for the deferred close")
	}

	// The retired envelope must not come back: no CLI path may emit it.
	if strings.Contains(warn, "push_exhausted\"") {
		t.Errorf("stderr surfaced the retired push_exhausted envelope code")
	}
}

// TestDocClaim_Errors_RemoteUnreachable pins the behavioral contract for
// `remote_unreachable` exit 3 documented in docs/spec.md's error table.
// The genuine emitter is `act bootstrap-worker --from-remote <url>`: when the
// initial `git clone` of the remote act-state fails for a non-timeout reason
// (DNS, auth, unreachable / nonexistent URL), the staging dir is torn down
// and the command exits 3 with envelope `remote_unreachable` carrying
// details.url and details.stderr_tail.
//
// Asserted at the RunBootstrapWorker boundary — the same surface the CLI
// wires to exit code + JSON output — by pointing --from-remote at a
// nonexistent local path so `git clone` fails fast without a network round
// trip. Deleting this test trips the sweep registry and breaks the build.
func TestDocClaim_Errors_RemoteUnreachable(t *testing.T) {
	target := makeBootstrapTarget(t)

	// A path that does not exist: `git clone <path>` fails immediately
	// (non-timeout) → the remote_unreachable branch in RunBootstrapWorker.
	badURL := target + "/does-not-exist-remote.git"

	out, exitCode := RunBootstrapWorker(BootstrapWorkerOptions{
		FromRemoteURL: badURL,
		Target:        target,
	})

	// Spec §error-envelope table: remote_unreachable exits 3.
	if exitCode != 3 {
		t.Errorf("exit code = %d, want 3 (remote_unreachable); got out=%+v", exitCode, out)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("output type = %T, want map[string]any", out)
	}
	if got, _ := m["error"].(string); got != ErrRemoteUnreachable {
		t.Errorf("envelope error = %q, want %q", got, ErrRemoteUnreachable)
	}
	// Belt-and-braces: confirm ErrRemoteUnreachable has the value the spec claims.
	if ErrRemoteUnreachable != "remote_unreachable" {
		t.Fatalf("ErrRemoteUnreachable constant: want %q, got %q", "remote_unreachable", ErrRemoteUnreachable)
	}
	// The details must carry url + stderr_tail (the fields the spec row names).
	d, _ := m["details"].(map[string]any)
	if d == nil {
		t.Fatalf("envelope details is nil; want url + stderr_tail")
	}
	if got, _ := d["url"].(string); got != badURL {
		t.Errorf("details.url = %q, want %q", got, badURL)
	}
	if _, ok := d["stderr_tail"]; !ok {
		t.Errorf("details missing stderr_tail key; got %+v", d)
	}
}
