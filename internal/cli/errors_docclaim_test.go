package cli

// Doc-claim tests for the Phase 2 push-retry error codes (act-9f3fc5,
// act-915a88).
//
// Both `push_exhausted` and `remote_unreachable` are documented in
// docs/spec-v2.md's error table; the registry in docs_sweep_test.go
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
	"testing"

	"github.com/aac/act/internal/gitops"
)

// TestDocClaim_Errors_PushExhausted pins the behavioral contract for
// `push_exhausted` exit 4 documented in docs/spec-v2.md. After 5 push
// retries are exhausted (fault-injected via ACT_TEST_FAIL_PUSH_AFTER=1),
// RunClose must return exit code 4 with a CloseErrorOutput whose Error
// field equals the canonical slug "push_exhausted".
//
// Asserted at the RunClose Go API boundary — the same surface the CLI wires
// to exit code + JSON output. Deleting this test removes the only behavioral
// guard anchored to the sweep registry; the sweep reports an orphaned
// registry entry and breaks the build.
func TestDocClaim_Errors_PushExhausted(t *testing.T) {
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
	// fail, exhausting all 5 retries → PushExhaustedError.
	t.Setenv("ACT_TEST_FAIL_PUSH_AFTER", "1")

	out, exitCode := RunClose(root, CloseOptions{ID: id})

	// Spec §error-envelope: push_exhausted exits 4.
	if exitCode != 4 {
		t.Errorf("exit code = %d, want 4 (push_exhausted); out=%+v", exitCode, out)
	}
	errOut, ok := out.(CloseErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want CloseErrorOutput", out)
	}
	// The envelope Error field must be the canonical slug.
	if errOut.Error != ErrPushExhausted {
		t.Errorf("envelope Error = %q, want %q", errOut.Error, ErrPushExhausted)
	}
	// Belt-and-braces: confirm ErrPushExhausted has the value the spec claims.
	if ErrPushExhausted != "push_exhausted" {
		t.Fatalf("ErrPushExhausted constant: want %q, got %q", "push_exhausted", ErrPushExhausted)
	}
}

// TestDocClaim_Errors_RemoteUnreachable pins the behavioral contract for
// `remote_unreachable` exit 3 documented in docs/spec-v2.md's error table.
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
