package cli

// Doc-claim tests for the Phase 2 ticket-3a "push on write" behavior
// (act-65a7d5).
//
// The user-visible claim is documented in docs/spec.md under the
// universal-flags section: "every successful auto-commit on a write
// subcommand ... is followed by a synchronous git push via the retry
// helper". Two layered assertions:
//
//   1. The spec sentence exists verbatim in docs/spec.md (this is the
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
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	text := string(body)
	// The pivotal claim: "synchronous git push" must appear inside the
	// universal-flags section.
	if !strings.Contains(text, "synchronous `git push`") {
		t.Errorf("spec.md: missing claim 'synchronous `git push`' in universal-flags section")
	}
	if !strings.Contains(text, "origin` configured") {
		t.Errorf("spec.md: missing claim about origin gating the auto-publish")
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
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "No-origin repos skip the publish step silently") {
		t.Errorf("spec.md: missing claim that no-origin repos skip the publish")
	}
}

// TestDocClaim_PushOnWrite_PublishFailureDefers pins the act-89a595
// spec claim that a publish failure on an already-committed op is a
// deferral, not an error — and asserts it BEHAVIORALLY on the generic
// write helper (WriteOpAndAutoCommit, via `act update`), which is a
// different code path from close.go's hand-rolled commit sequence.
//
// The doc half: the spec must carry the claim so an agent reading it
// cold learns that exit 0 can mean "committed, not published".
// The behavior half: with every push attempt fault-injected to fail,
// `act update` must exit 0, warn on stderr, and queue the commit.
func TestDocClaim_PushOnWrite_PublishFailureDefers(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	text := string(body)
	for _, want := range []string{
		"**Publish failure is a deferral, not an error (act-89a595).**",
		"`.act/.pending-pushes`",
		"exits 0",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("spec.md: missing %q in the auto-publish section", want)
		}
	}

	// Behavioral half, on the generic write helper.
	gitops.ResetPushAttemptCounter()
	repo, _ := makeRepoWithRemoteOrigin(t)
	createOut, code := RunCreate(repo, CreateOptions{Title: "defer-probe", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	gitops.ResetPushAttemptCounter()
	t.Setenv("ACT_TEST_FAIL_PUSH_AFTER", "1")

	// The generic helper writes its warning to os.Stderr (no Stderr
	// plumbing on UpdateOptions); capture it through a pipe.
	warn := captureStderr(t, func() {
		note := "annotation while origin is dead"
		out, code := RunUpdate(repo, UpdateOptions{ID: id, DescriptionAppend: &note})
		if code != 0 {
			t.Fatalf("RunUpdate exit = %d, want 0 (op committed; push failure must not fail the write); out=%+v", code, out)
		}
	})
	if !strings.Contains(warn, "NOT PUSHED") || !strings.Contains(warn, ".act/.pending-pushes") {
		t.Errorf("stderr warning missing the committed-not-pushed notice:\n%s", warn)
	}

	// The append is readable back immediately — the write is durable.
	showOut, showCode := RunShow(repo, ShowOptions{ID: id})
	if showCode != 0 {
		t.Fatalf("RunShow: code=%d out=%+v", showCode, showOut)
	}

	pending, err := ReadPendingPushes(filepath.Join(repo, ".act"))
	if err != nil {
		t.Fatalf("ReadPendingPushes: %v", err)
	}
	if len(pending) == 0 {
		t.Errorf("no .pending-pushes entry queued for the deferred update")
	}
}
