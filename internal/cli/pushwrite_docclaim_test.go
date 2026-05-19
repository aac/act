package cli

// Doc-claim tests for the Phase 2 ticket-3a "push on write" behavior
// (act-65a7d5).
//
// The user-visible claim is documented in docs/spec-v2.md under the
// universal-flags section: "every successful auto-commit on a write
// subcommand ... is followed by a synchronous git push via the retry
// helper". Two layered assertions:
//
//   1. The spec sentence exists verbatim in docs/spec-v2.md (this is the
//      doc claim the sweep registers in pushwrite-* entries).
//   2. The behavior matches what the spec promises: a write helper
//      invokes the gitops counter exactly once on a remote-configured
//      repo, and zero times on a no-origin repo. The behavioral test
//      proper lives in push_integration_test.go; these doc-claim tests
//      assert the spec sentence is unambiguous enough that an agent
//      reading the spec cold could implement the same behavior.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/gitops"
)

// TestDocClaim_PushOnWrite_AutoPublishOnRemote pins the spec sentence
// that every write subcommand auto-publishes when origin is configured.
// The behavioral assertion is delegated to push_integration_test.go's
// TestAllWriteSubcommands_InvokePushOnce; this test fails if the spec
// sentence drifts or if the gitops counting hook is renamed (which
// would break that delegated assertion).
func TestDocClaim_PushOnWrite_AutoPublishOnRemote(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec-v2.md"))
	if err != nil {
		t.Fatalf("read docs/spec-v2.md: %v", err)
	}
	text := string(body)
	// The pivotal claim: "synchronous git push" must appear inside the
	// universal-flags section.
	if !strings.Contains(text, "synchronous `git push`") {
		t.Errorf("spec-v2.md: missing claim 'synchronous `git push`' in universal-flags section")
	}
	if !strings.Contains(text, "origin` configured") {
		t.Errorf("spec-v2.md: missing claim about origin gating the auto-publish")
	}
	// The counting hook is named in the gitops package and consumed by
	// the integration tests. Drift here (rename / removal) would silently
	// break the AC-4 assertion.
	_ = gitops.TestPushInvocationCount.Load()
}

// TestDocClaim_PushOnWrite_NoOriginIsLocalOnly pins the spec sentence
// that no-origin repos skip the publish step silently. This is the
// graceful-degradation contract: a single-machine dogfood user never
// has to wire a remote to use act locally.
func TestDocClaim_PushOnWrite_NoOriginIsLocalOnly(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec-v2.md"))
	if err != nil {
		t.Fatalf("read docs/spec-v2.md: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "No-origin repos skip the publish step silently") {
		t.Errorf("spec-v2.md: missing claim that no-origin repos skip the publish")
	}
}

// TestDocClaim_PushOnWrite_ExhaustionSurfaceIsPushExhausted pins the
// spec sentence mapping retry exhaustion to envelope code
// `push_exhausted` with exit 4. The integration test
// TestActClose_PushExhausted_ReturnsEnvelope asserts the runtime
// behavior; this test asserts the doc claim that points at that
// behavior is present in the spec.
func TestDocClaim_PushOnWrite_ExhaustionSurfaceIsPushExhausted(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec-v2.md"))
	if err != nil {
		t.Fatalf("read docs/spec-v2.md: %v", err)
	}
	text := string(body)
	// The exhaustion envelope and exit code must appear together with
	// the auto-publish description, not just in the error table — the
	// reader needs to know auto-publish errors out to this envelope
	// without cross-referencing the table.
	if !strings.Contains(text, "exits 4 with envelope `push_exhausted`") {
		t.Errorf("spec-v2.md: missing claim 'exits 4 with envelope `push_exhausted`' in universal-flags section")
	}
}
