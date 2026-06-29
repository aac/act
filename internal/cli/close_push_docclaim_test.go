package cli

// Doc-claim tests for the corrected SKILL.md / `act help workflow` close-push
// claims (act-46b704, act-e9ce41):
//
//   - "Close writes the close op AND pushes it itself — there is no separate
//     `git push` step for the close op." Asserted by
//     TestDocClaim_Close_PushesOnRemoteNoSeparateGitPush.
//   - "With no origin remote, close (and `act finish`) commit locally and skip
//     the push with no network — the close still succeeds (exit 0) and nothing
//     reaches the wire." Asserted by
//     TestDocClaim_Close_NoOriginSkipsPushNoNetwork.
//
// Both assert at the close boundary the docs name: the gitops push counter
// (the established "did it push" probe, shared with push_integration_test.go)
// plus the visible close-op publication / status. The matching registry
// tuples live in docs_sweep_test.go.

import (
	"strings"
	"testing"

	"github.com/aac/act/internal/gitops"
)

// TestDocClaim_Close_PushesOnRemoteNoSeparateGitPush pins the SKILL.md and
// `act help workflow` claim that close pushes the close op ITSELF — there is
// no separate `git push` step for the close op. On a remote-configured nested
// .act/ repo, a single RunClose must invoke PushWithRetry exactly once, and
// the close op must land on the bare remote — so an agent that does NOT run a
// follow-up `git push` still publishes the close.
func TestDocClaim_Close_PushesOnRemoteNoSeparateGitPush(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root, remote := makeRepoWithRemoteOrigin(t)

	createOut, code := RunCreate(root, CreateOptions{Title: "close-pushes-itself", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	before := gitops.TestPushInvocationCount.Load()
	if _, code := RunClose(root, CloseOptions{ID: id}); code != 0 {
		t.Fatalf("RunClose: code=%d", code)
	}
	after := gitops.TestPushInvocationCount.Load()
	if after-before != 1 {
		t.Errorf("close push count delta = %d, want 1 (close must push the close op itself, no separate git push)", after-before)
	}

	// The close op must be reachable on the bare remote after the command
	// returns — proof the push the close performed actually published it,
	// with no separate `git push` from the agent.
	tree := runOut(t, remote.Path, "git", "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "ops/"+id) || !strings.Contains(tree, "-close.json") {
		t.Errorf("bare remote missing the pushed close op for %s\n%s", id, tree)
	}
}

// TestDocClaim_Close_NoOriginSkipsPushNoNetwork pins the SKILL.md / `act help
// workflow` claim that with no origin remote, close commits locally and skips
// the push with no network: the close still succeeds (exit 0) and nothing
// reaches the wire. Asserted at the close boundary: a no-origin RunClose exits
// 0, the issue reads closed, and the push counter does not advance.
func TestDocClaim_Close_NoOriginSkipsPushNoNetwork(t *testing.T) {
	gitops.ResetPushAttemptCounter()
	root := makeCreateRepo(t) // no remote wired on the nested .act/ repo

	createOut, code := RunCreate(root, CreateOptions{Title: "no-origin-close", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code=%d", code)
	}
	id := createOut.(CreateResult).ID

	before := gitops.TestPushInvocationCount.Load()
	out, code := RunClose(root, CloseOptions{ID: id})
	if code != 0 {
		t.Fatalf("RunClose on no-origin repo: code=%d, out=%+v; close must still succeed", code, out)
	}
	after := gitops.TestPushInvocationCount.Load()
	if after != before {
		t.Errorf("push counter advanced %d -> %d on a no-origin close; want unchanged (no network)", before, after)
	}

	// Confirm the close actually landed locally despite the skipped push.
	showOut, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("RunShow: code=%d", code)
	}
	if sr, ok := showOut.(ShowResult); ok {
		if st := sr.ShowJSON()["status"]; st != "closed" {
			t.Errorf("after no-origin close, status = %v, want closed", st)
		}
	} else {
		t.Fatalf("RunShow output type = %T, want ShowResult", showOut)
	}
}
