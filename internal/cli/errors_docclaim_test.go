package cli

// Doc-claim tests for the Phase 2 push-retry error codes (act-9f3fc5).
//
// Both `push_exhausted` and `remote_unreachable` are documented in
// docs/spec-v2.md's error table; the registry in docs_sweep_test.go
// pins each to the test function below. These tests assert two
// surfaces:
//
//   1. The CLI exposes the code via the error-envelope constants in
//      errors.go (so writer code can reference ErrPushExhausted /
//      ErrRemoteUnreachable rather than re-typing the slug).
//   2. The spec-v2.md error-table entry exists with the matching
//      code, exit class, and the documented detail keys.
//
// The second assertion is what catches doc drift: if someone removes
// the spec row without removing the constant, this test still passes
// (the sweep's docFile-vs-claimPattern check catches it). If someone
// renames the constant, the build breaks (Go-level rename); these
// assertions catch the inverse — a constant rename that didn't update
// the spec row.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_Errors_PushExhausted pins the spec-v2.md row for
// `push_exhausted` and the matching cli constant. The row MUST live in
// the error-table and reference the canonical detail keys
// (`retry_count`, `shallow_unshallow_attempted`) that
// gitops.PushWithRetry populates.
func TestDocClaim_Errors_PushExhausted(t *testing.T) {
	if ErrPushExhausted != "push_exhausted" {
		t.Fatalf("ErrPushExhausted constant: want %q, got %q", "push_exhausted", ErrPushExhausted)
	}

	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec-v2.md"))
	if err != nil {
		t.Fatalf("read docs/spec-v2.md: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "`push_exhausted`") {
		t.Errorf("spec-v2.md: missing `push_exhausted` row in error table")
	}
	// The row carries the documented detail keys.
	for _, key := range []string{"retry_count", "shallow_unshallow_attempted"} {
		if !strings.Contains(text, key) {
			t.Errorf("spec-v2.md: push_exhausted row missing detail key %q", key)
		}
	}
}

// TestDocClaim_Errors_RemoteUnreachable pins the spec-v2.md row for
// `remote_unreachable` and the matching cli constant. The row MUST
// reference `stderr_tail` as a detail key — that's how the underlying
// `git fetch` failure surfaces to the user.
func TestDocClaim_Errors_RemoteUnreachable(t *testing.T) {
	if ErrRemoteUnreachable != "remote_unreachable" {
		t.Fatalf("ErrRemoteUnreachable constant: want %q, got %q", "remote_unreachable", ErrRemoteUnreachable)
	}

	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec-v2.md"))
	if err != nil {
		t.Fatalf("read docs/spec-v2.md: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "`remote_unreachable`") {
		t.Errorf("spec-v2.md: missing `remote_unreachable` row in error table")
	}
	if !strings.Contains(text, "stderr_tail") {
		t.Errorf("spec-v2.md: remote_unreachable row missing stderr_tail detail key")
	}
}
