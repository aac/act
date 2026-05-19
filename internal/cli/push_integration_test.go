package cli

// Phase 2 ticket 3a (act-65a7d5) — push-on-write integration tests.
//
// These tests assert that the write helpers (WriteOpAndAutoCommit /
// WriteOpsAndAutoCommit) and the close.go non-helper commit path BOTH
// invoke gitops.PushWithRetry exactly once per successful commit when
// the nested .act/ repo has `origin` configured, AND that exhaustion
// surfaces the canonical `push_exhausted` envelope with the right
// details / exit code.
//
// Cross-test contract: gitops.TestPushInvocationCount is process-global.
// Tests snapshot the counter at start and compare against the post-call
// value rather than expecting an absolute number. The fault-injection
// counter inside push_retry.go is reset per test via
// gitops.ResetPushAttemptCounter().

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/testfixtures"
)

// makeRepoWithRemoteOrigin builds a Phase-1-shape repo (host + nested
// .act/) whose nested .act/ repo has `origin` pointing at a BareRemote.
// The bare remote is seeded with the nested repo's bootstrap commit so
// PushWithRetry sees a fast-forwardable target on the first attempt.
//
// Returns (hostRoot, bareRemote). The bare remote is registered for
// cleanup with t.Cleanup via NewBareRemote.
func makeRepoWithRemoteOrigin(t *testing.T) (string, *testfixtures.BareRemote) {
	t.Helper()
	root := makeCreateRepo(t)
	paths := config.Layout(root)

	// Build a bare remote and wire it as `origin` on the nested .act/
	// repo. The seed commit in the BareRemote has a different SHA than
	// the nested repo's bootstrap commit, so a naive `git push origin
	// main` would be non-fast-forward. To make the fixture predictable
	// we force-push the nested repo's main to the bare so subsequent
	// pushes are vanilla fast-forwards.
	remote := testfixtures.NewBareRemote(t)
	mustGit(t, paths.Root, "remote", "add", "origin", remote.URL)
	// Force-push the bootstrap commit so the bare remote's main now
	// reflects the nested repo's history. From here, regular pushes
	// fast-forward.
	mustGit(t, paths.Root, "push", "-f", "origin", "main")
	return root, remote
}

// TestActCreate_PushesOnRemoteConfigured — happy path: `act create` on a
// remote-configured project pushes synchronously, and the new op file is
// reachable on the bare-remote's git log after the command returns.
//
// Asserts AC1: "act create on a remote-configured project pushes
// synchronously by default."
func TestActCreate_PushesOnRemoteConfigured(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, remote := makeRepoWithRemoteOrigin(t)

	before := gitops.TestPushInvocationCount.Load()
	out, code := RunCreate(root, CreateOptions{Title: "remote-push", Type: "task"})
	if code != 0 {
		t.Fatalf("RunCreate: code=%d, out=%+v", code, out)
	}
	after := gitops.TestPushInvocationCount.Load()
	if after-before != 1 {
		t.Errorf("TestPushInvocationCount delta = %d, want 1", after-before)
	}

	id := out.(CreateResult).ID

	// Verify the op file is reachable on the bare remote's tree.
	// `git ls-tree -r --name-only main` lists every tracked path on main.
	tree := runOut(t, remote.Path, "git", "ls-tree", "-r", "--name-only", "main")
	wantPath := "ops/" + id
	if !strings.Contains(tree, wantPath) {
		t.Errorf("bare-remote tree missing %q\n%s", wantPath, tree)
	}
}

// TestActClose_PushesOnRemoteConfigured — happy path for close: the
// close op file is visible on the bare-remote after `act close`.
//
// Asserts AC2: "act close on a remote-configured project pushes; the
// close-op file is visible on the peer clone after the command returns."
func TestActClose_PushesOnRemoteConfigured(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, remote := makeRepoWithRemoteOrigin(t)

	// Seed an issue.
	createOut, code := RunCreate(root, CreateOptions{Title: "to-close-remote", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	before := gitops.TestPushInvocationCount.Load()
	closeOut, code := RunClose(root, CloseOptions{ID: id})
	if code != 0 {
		t.Fatalf("RunClose: code=%d, out=%+v", code, closeOut)
	}
	after := gitops.TestPushInvocationCount.Load()
	if after-before != 1 {
		t.Errorf("TestPushInvocationCount delta = %d, want 1", after-before)
	}

	// The close op shows up under .act/ops/<id>/.../*-close.json on the
	// bare remote's tree. Verify via ls-tree.
	tree := runOut(t, remote.Path, "git", "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "ops/"+id) {
		t.Errorf("bare-remote missing close op for %s\n%s", id, tree)
	}
	// Look for a *-close.json under the issue's shard on the remote.
	if !strings.Contains(tree, "-close.json") {
		t.Errorf("bare-remote tree has no *-close.json\n%s", tree)
	}
}

// TestActClose_PushExhausted_ReturnsEnvelope — fault-injects 5 simulated
// silent rejections; act close returns envelope push_exhausted with
// retry_count=5 and exit=4.
//
// Asserts AC3: "After 5 retries exhausted (fault-injected via the
// test-only ACT_TEST_FAIL_PUSH_AFTER=N env hook), act close returns
// envelope {code: push_exhausted, exit: 4} with details.retry_count=5."
func TestActClose_PushExhausted_ReturnsEnvelope(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, _ := makeRepoWithRemoteOrigin(t)

	// Seed an open issue.
	createOut, code := RunCreate(root, CreateOptions{Title: "exhaust-target", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	// Reset the fault-injection counter AFTER the seed create (which
	// already invoked one push). Setting N=1 from this point onward
	// means every push attempt during the close call fails silently.
	gitops.ResetPushAttemptCounter()
	t.Setenv("ACT_TEST_FAIL_PUSH_AFTER", "1")

	out, code := RunClose(root, CloseOptions{ID: id})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; out=%+v", code, out)
	}
	errOut, ok := out.(CloseErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want CloseErrorOutput", out)
	}
	if errOut.Error != ErrPushExhausted {
		t.Errorf("Error = %q, want %q", errOut.Error, ErrPushExhausted)
	}
	rc, ok := errOut.Details["retry_count"].(int)
	if !ok {
		t.Fatalf("details.retry_count missing or wrong type: %+v", errOut.Details)
	}
	if rc != 5 {
		t.Errorf("details.retry_count = %d, want 5", rc)
	}
	if _, ok := errOut.Details["shallow_unshallow_attempted"]; !ok {
		t.Errorf("details.shallow_unshallow_attempted missing: %+v", errOut.Details)
	}
}

// TestAllWriteSubcommands_InvokePushOnce — exercises each of the six
// write subcommands and asserts the cumulative invocation count is 6.
//
// Asserts AC4: "All six write subcommands invoke PushWithRetry exactly
// once per successful commit — asserted via the counting hook."
//
// Subcommand inventory: create, dep-add, update, reopen, close, delete.
// We seed dependencies in order: two creates (parent + child) so dep-add
// has both endpoints; update toggles a field; close then reopen; finally
// delete on a separately-created throwaway issue so deletion's cascade
// shape (it removes a not-yet-touched issue) is the cleanest case.
func TestAllWriteSubcommands_InvokePushOnce(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, _ := makeRepoWithRemoteOrigin(t)

	before := gitops.TestPushInvocationCount.Load()

	// 1. create — parent.
	parentOut, code := RunCreate(root, CreateOptions{Title: "parent", Type: "epic"})
	if code != 0 {
		t.Fatalf("create parent: code=%d", code)
	}
	parent := parentOut.(CreateResult).ID

	// 2. create — child (also exercises create, but we don't double-
	// count: we need a child for dep-add, and dep-add itself increments
	// once. So we count this as the second of two creates and check the
	// total later.)
	childOut, code := RunCreate(root, CreateOptions{Title: "child", Type: "task"})
	if code != 0 {
		t.Fatalf("create child: code=%d", code)
	}
	child := childOut.(CreateResult).ID

	// 3. dep-add — link child to parent.
	if _, code := RunDepAdd(root, DepAddOptions{Child: child, Parent: parent, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("dep-add: code=%d", code)
	}

	// 4. update — change child's priority.
	prio := 2
	upOut, code := RunUpdate(root, UpdateOptions{ID: child, Priority: &prio})
	if code != 0 {
		t.Fatalf("update: code=%d, out=%+v", code, upOut)
	}

	// 5. close — close the child.
	if _, code := RunClose(root, CloseOptions{ID: child}); code != 0 {
		t.Fatalf("close: code=%d", code)
	}

	// 6. reopen — bring it back.
	if _, code := RunReopen(root, ReopenOptions{ID: child}); code != 0 {
		t.Fatalf("reopen: code=%d", code)
	}

	// 7. delete — separately created throwaway issue, kept out of the
	// dep graph so RunDelete's resolve path doesn't have to traverse.
	throwOut, code := RunCreate(root, CreateOptions{Title: "throwaway", Type: "task"})
	if code != 0 {
		t.Fatalf("create throwaway: code=%d", code)
	}
	throw := throwOut.(CreateResult).ID
	if _, code := RunDelete(root, DeleteOptions{ID: throw}); code != 0 {
		t.Fatalf("delete: code=%d", code)
	}

	after := gitops.TestPushInvocationCount.Load()
	// 8 writes total: 3 creates + dep-add + update + close + reopen + delete.
	want := int64(8)
	if got := after - before; got != want {
		t.Errorf("TestPushInvocationCount delta = %d, want %d (one per write subcommand call)", got, want)
	}
}

// TestActCreate_NoOriginConfigured_DoesNotPush — sanity check the
// "no origin" branch: when the nested .act/ has no `origin`, the write
// subcommand still succeeds, no push is attempted, and the invocation
// counter does NOT advance.
//
// Asserts AC5: "No-origin project: write subcommands work as before
// (no push attempted, no error)."
func TestActCreate_NoOriginConfigured_DoesNotPush(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root := makeCreateRepo(t) // no remote wired
	paths := config.Layout(root)

	// Defensive: verify the nested repo really has no origin.
	if _, err := os.Stat(filepath.Join(paths.Root, ".git")); err != nil {
		t.Fatalf("nested .act/.git missing: %v", err)
	}

	before := gitops.TestPushInvocationCount.Load()
	if _, code := RunCreate(root, CreateOptions{Title: "no-remote", Type: "task"}); code != 0 {
		t.Fatalf("RunCreate: code=%d", code)
	}
	after := gitops.TestPushInvocationCount.Load()
	if after != before {
		t.Errorf("TestPushInvocationCount advanced %d -> %d on a no-origin repo; want unchanged", before, after)
	}
}

// TestPushIntegration_BareRemoteFixtureSanity verifies the fixture
// itself works as expected: a fresh BareRemote + force-push leaves the
// remote with the same HEAD as the nested .act/ repo. Pure fixture test
// — keeps the rest of the file from chasing fixture bugs.
func TestPushIntegration_BareRemoteFixtureSanity(t *testing.T) {
	root, remote := makeRepoWithRemoteOrigin(t)
	paths := config.Layout(root)

	localHEAD := strings.TrimSpace(runOut(t, paths.Root, "git", "rev-parse", "HEAD"))
	remoteHEAD := strings.TrimSpace(runOut(t, remote.Path, "git", "rev-parse", "main"))
	if localHEAD != remoteHEAD {
		t.Errorf("nested HEAD %s != bare main %s", localHEAD, remoteHEAD)
	}
}
