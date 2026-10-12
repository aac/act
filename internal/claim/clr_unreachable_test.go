package claim

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aac/act/internal/hlc"
)

// clrUnreachable is what a GitOps.PullRebase returns when the fetch never
// reached origin.
func clrUnreachable() error {
	return fmt.Errorf("%w: gitops: fetch failed: exit status 128 (output: fatal: unable to access)", ErrOriginUnreachable)
}

// TestClrRunClaim_OriginUnreachableWinIsUnconfirmed (act-2bd35b): the claim
// op is committed before the rebase step, so a fetch that cannot reach
// origin does not fail the claim. With no other claim on disk the claim
// stands, RunClaim returns no error, the reason is on Result.PullErr, and
// the push — which would fail the same way — is not attempted.
func TestClrRunClaim_OriginUnreachableWinIsUnconfirmed(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{PullRebaseErr: clrUnreachable()}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Push: true}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim with an unreachable origin: %v; want a nil error", err)
	}
	if !res.Claimed || res.Winner != "alice" {
		t.Fatalf("Claimed=%v Winner=%q, want a win for alice; result=%+v", res.Claimed, res.Winner, res)
	}
	if res.PullErr == nil || !errors.Is(res.PullErr, ErrOriginUnreachable) {
		t.Errorf("PullErr=%v, want an error wrapping ErrOriginUnreachable", res.PullErr)
	}
	if git.PushCalls != 0 || res.PushErr != nil {
		t.Errorf("PushCalls=%d PushErr=%v, want no push attempted after an unreachable fetch", git.PushCalls, res.PushErr)
	}
}

// TestClrRunClaim_OriginUnreachableLossIsStillALoss (act-2bd35b): the
// unreachable-origin path never turns a lost race into a win. Another
// writer's earlier claim is already on disk; the fetch fails; the result is
// a loss naming that writer, with no PullErr and no push.
func TestClrRunClaim_OriginUnreachableLossIsStillALoss(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)
	writeRawClaim(t, paths.Ops, "act-1234", "bob", hlc.HLC{Wall: 1_699_999_999_000, Logical: 0, NodeID: "0b0b0b0b"})
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{PullRebaseErr: clrUnreachable()}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Push: true}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v; want a nil error and a loss", err)
	}
	if res.Claimed || res.Winner != "bob" {
		t.Fatalf("Claimed=%v Winner=%q, want a loss to bob; result=%+v", res.Claimed, res.Winner, res)
	}
	if res.PullErr != nil {
		t.Errorf("PullErr=%v on a lost claim, want nil", res.PullErr)
	}
	if git.PushCalls != 0 {
		t.Errorf("PushCalls=%d on a lost claim, want 0", git.PushCalls)
	}
}

// TestClrRunClaim_FetchedButNotRebasedIsNotAWin (act-2bd35b): when origin
// was fetched and the rebase then failed, origin's ops are not in the tree,
// so the winner cannot be told. That is not ErrOriginUnreachable, and it
// stays a hard error rather than a win.
func TestClrRunClaim_FetchedButNotRebasedIsNotAWin(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{PullRebaseErr: errors.New("gitops: rebase produced conflicts: exit status 1 (output: CONFLICT)")}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err == nil {
		t.Fatalf("RunClaim with a failed rebase returned no error; result=%+v", res)
	}
	if res.Claimed {
		t.Errorf("Claimed=true with a failed rebase, want no win reported")
	}
}
