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
// TestDocClaim_Errors_RemoteUnreachable calls closeErrorForPushFailure
// directly with an ErrFetchFailed-wrapping error and asserts the mapping
// produces exit=4 and envelope "remote_unreachable". The full RunClose
// trigger path for remote_unreachable requires a non-fast-forward push
// followed by a fetch failure; because PushWithRetry stores FetchAndRebase
// errors as lastErr (retrying until exhaustion), a true end-to-end RunClose
// test would produce push_exhausted rather than remote_unreachable. Testing
// closeErrorForPushFailure directly is the correct boundary for this code
// path — it is the function that maps the error to the documented envelope.
// Deleting either test trips the sweep registry and breaks the build.

import (
	"errors"
	"fmt"
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
// `remote_unreachable` exit 4 documented in docs/spec-v2.md. The claim is
// that when the underlying git fetch fails (ErrFetchFailed), the error is
// classified into envelope code "remote_unreachable" with exit 4.
//
// The test calls closeErrorForPushFailure directly with an ErrFetchFailed-
// wrapping error and asserts the classification produces exit=4 and envelope
// "remote_unreachable". This is the correct boundary for this code path
// because PushWithRetry stores FetchAndRebase errors as lastErr (retrying
// until exhaustion), so a full RunClose path with a broken remote produces
// push_exhausted rather than remote_unreachable. The mapping from ErrFetchFailed
// to the documented envelope lives in closeErrorForPushFailure; testing it
// there ensures the guard catches a rename or removed branch in that function.
//
// Deleting this test trips the sweep registry and breaks the build.
func TestDocClaim_Errors_RemoteUnreachable(t *testing.T) {
	// Construct an error that wraps ErrFetchFailed but is not a
	// *PushExhaustedError — this is the input shape that triggers the
	// remote_unreachable classification path in closeErrorForPushFailure.
	fetchErr := fmt.Errorf("%w: git fetch failed: exit status 128 (output: fatal: unable to connect)", gitops.ErrFetchFailed)

	out, exitCode := closeErrorForPushFailure(fetchErr)

	// Spec §error-envelope: remote_unreachable exits 4.
	if exitCode != 4 {
		t.Errorf("exit code = %d, want 4 (remote_unreachable); got out=%+v", exitCode, out)
	}
	errOut, ok := out.(CloseErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want CloseErrorOutput", out)
	}
	// The envelope Error field must be "remote_unreachable".
	if errOut.Error != ErrRemoteUnreachable {
		t.Errorf("envelope Error = %q, want %q", errOut.Error, ErrRemoteUnreachable)
	}
	// Belt-and-braces: confirm ErrRemoteUnreachable has the value the spec claims.
	if ErrRemoteUnreachable != "remote_unreachable" {
		t.Fatalf("ErrRemoteUnreachable constant: want %q, got %q", "remote_unreachable", ErrRemoteUnreachable)
	}
	// The details must carry stderr_tail (the field documented in the spec row).
	if errOut.Details == nil {
		t.Errorf("CloseErrorOutput.Details is nil; want stderr_tail populated")
	} else if _, ok := errOut.Details["stderr_tail"]; !ok {
		t.Errorf("CloseErrorOutput.Details missing stderr_tail key; got %+v", errOut.Details)
	}
	// Verify the classification is driven by ErrFetchFailed, not a
	// catch-all: errors.Is must confirm the wrapping was detected.
	if !errors.Is(fetchErr, gitops.ErrFetchFailed) {
		t.Errorf("test precondition failed: fetchErr does not wrap ErrFetchFailed")
	}
}
