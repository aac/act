package claim

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// Step 0's unpublished-hold path (act-c30acd, docs/claim-identity-design.md
// §3): a re-claim of an issue this checkout holds answers from local state
// only when origin already has the hold; otherwise it rebases onto origin
// first and decides on the ops then in the log, writing none.

// u2PlantHold plants a winning claim by alice and returns a later clock for
// the re-claim, the shape every TestRunClaim_Unpublished* test starts from.
func u2PlantHold(t *testing.T) (string, config.LayoutPaths, *hlc.Clock) {
	t.Helper()
	root := t.TempDir()
	paths := initRepo(t, root)
	prior := hlc.HLC{Wall: 1_700_000_000_500, Logical: 0, NodeID: "abcdef01"}
	writeRawClaim(t, paths.Ops, "act-1234", "alice", prior)
	return root, paths, newClock(t, "abcdef01", 1_700_000_001_000)
}

// u2ClaimOps lists the claim op files on disk for act-1234.
func u2ClaimOps(t *testing.T, paths config.LayoutPaths) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(paths.Ops, "act-1234", "*", "*-claim.json"))
	return m
}

// TestRunClaim_UnpublishedReClaimPulls: a hold whose commit origin has not
// seen is confirmed against origin before it is reported — one rebase, no
// new op — and the stranded commit is handed to the caller's publish leg
// (Republish).
func TestRunClaim_UnpublishedReClaimPulls(t *testing.T) {
	root, paths, clock := u2PlantHold(t)
	fake := &FakeGitOps{UnpublishedResult: true}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, fake)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed || !res.AlreadyHeld || res.Winner != "alice" {
		t.Fatalf("result = %+v, want a confirmed hold by alice", res)
	}
	if !res.Republish {
		t.Errorf("Republish = false, want true: the stranded claim commit still has to reach origin")
	}
	if res.PullErr != nil || res.PushErr != nil {
		t.Errorf("PullErr=%v PushErr=%v, want neither", res.PullErr, res.PushErr)
	}
	if fake.UnpublishedCalls != 1 || fake.PullRebaseCalls != 1 {
		t.Errorf("UnpublishedCalls=%d PullRebaseCalls=%d, want 1 and 1", fake.UnpublishedCalls, fake.PullRebaseCalls)
	}
	if fake.CommitCalls != 0 || fake.PushCalls != 0 {
		t.Errorf("CommitCalls=%d PushCalls=%d, want 0 and 0 (no new op; the caller publishes)", fake.CommitCalls, fake.PushCalls)
	}
	if n := len(u2ClaimOps(t, paths)); n != 1 {
		t.Errorf("claim ops on disk = %d, want 1 (no new op on this path)", n)
	}
}

// TestRunClaim_UnpublishedReClaimPushesUnderPush: with Options.Push the
// confirmed hold's commit is pushed inside the protocol instead of being
// handed back for the publish leg; a push that fails leaves the hold
// unconfirmed on PushErr.
func TestRunClaim_UnpublishedReClaimPushesUnderPush(t *testing.T) {
	root, _, clock := u2PlantHold(t)
	fake := &FakeGitOps{UnpublishedResult: true}
	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Push: true}, clock, fake)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed || !res.AlreadyHeld || res.Republish {
		t.Fatalf("result = %+v, want a confirmed hold, Republish false (pushed here)", res)
	}
	if fake.PushCalls != 1 || fake.CommitCalls != 0 {
		t.Errorf("PushCalls=%d CommitCalls=%d, want 1 and 0", fake.PushCalls, fake.CommitCalls)
	}

	root2, _, clock2 := u2PlantHold(t)
	fake2 := &FakeGitOps{UnpublishedResult: true, PushErr: errors.New("pre-receive hook declined")}
	res2, err := RunClaim(root2, "act-1234", Options{Assignee: "alice", Push: true}, clock2, fake2)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res2.Claimed || !res2.AlreadyHeld || res2.PushErr == nil {
		t.Errorf("result = %+v, want a hold with PushErr set", res2)
	}
}

// TestRunClaim_UnpublishedReClaimLosesToOriginsEarlierOp: the rebase brings
// in an earlier claim by another checkout. The hold was never ours to keep:
// the re-claim answers a loss naming the rival, and writes nothing.
func TestRunClaim_UnpublishedReClaimLosesToOriginsEarlierOp(t *testing.T) {
	root, paths, clock := u2PlantHold(t)
	fake := &FakeGitOps{UnpublishedResult: true}
	fake.OnPullRebase = func() {
		writeRawClaim(t, paths.Ops, "act-1234", "carol", hlc.HLC{Wall: 1_700_000_000_000, Logical: 0, NodeID: "11111111"})
	}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, fake)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if res.Claimed {
		t.Fatalf("Claimed = true, want false: origin held carol's earlier claim; result=%+v", res)
	}
	if res.Winner != "carol" {
		t.Errorf("Winner = %q, want carol", res.Winner)
	}
	if fake.CommitCalls != 0 || fake.PushCalls != 0 {
		t.Errorf("CommitCalls=%d PushCalls=%d, want 0 and 0 (the retry decides on the ops already in the log)", fake.CommitCalls, fake.PushCalls)
	}
	if n := len(u2ClaimOps(t, paths)); n != 2 {
		t.Errorf("claim ops on disk = %d, want 2 (ours and carol's, no third)", n)
	}
}

// TestRunClaim_UnpublishedReClaimUnreachableStaysUnconfirmed: origin cannot
// be reached, so the hold cannot be confirmed. It is reported as held with
// PullErr set (the caller answers claim_unconfirmed), nothing is pushed, and
// --wait does not loop on it.
func TestRunClaim_UnpublishedReClaimUnreachableStaysUnconfirmed(t *testing.T) {
	root, paths, clock := u2PlantHold(t)
	fake := &FakeGitOps{
		UnpublishedResult: true,
		PullRebaseErr:     fmt.Errorf("%w: fetch failed", ErrOriginUnreachable),
	}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Push: true}, clock, fake)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed || !res.AlreadyHeld {
		t.Fatalf("result = %+v, want a hold", res)
	}
	if res.PullErr == nil || !errors.Is(res.PullErr, ErrOriginUnreachable) {
		t.Errorf("PullErr = %v, want ErrOriginUnreachable", res.PullErr)
	}
	if res.Republish {
		t.Errorf("Republish = true on an unconfirmed hold; nothing can reach origin")
	}
	if fake.PushCalls != 0 || fake.CommitCalls != 0 {
		t.Errorf("PushCalls=%d CommitCalls=%d, want 0 and 0", fake.PushCalls, fake.CommitCalls)
	}
	if n := len(u2ClaimOps(t, paths)); n != 1 {
		t.Errorf("claim ops on disk = %d, want 1", n)
	}

	root2, _, clock2 := u2PlantHold(t)
	fake2 := &FakeGitOps{UnpublishedResult: true, PullRebaseErr: fmt.Errorf("%w: fetch failed", ErrOriginUnreachable)}
	var sleeps int
	res2, err := runClaimInternal(root2, "act-1234", Options{Assignee: "alice", Wait: true, WaitTimeout: time.Minute}, clock2, fake2, func(time.Duration) { sleeps++ }, noJitter)
	if err != nil {
		t.Fatalf("RunClaim --wait: %v", err)
	}
	if sleeps != 0 || fake2.PullRebaseCalls != 1 || res2.PullErr == nil {
		t.Errorf("--wait on an unconfirmed hold: sleeps=%d PullRebaseCalls=%d PullErr=%v; want 0, 1, set", sleeps, fake2.PullRebaseCalls, res2.PullErr)
	}
}

// TestRunClaim_UnpublishedReClaimRebaseConflictIsHardError: origin was
// fetched but its history cannot be rebased under ours. The re-claim never
// says "held" while that stands.
func TestRunClaim_UnpublishedReClaimRebaseConflictIsHardError(t *testing.T) {
	root, _, clock := u2PlantHold(t)
	boom := errors.New("rebase conflict on config.json")
	fake := &FakeGitOps{UnpublishedResult: true, PullRebaseErr: boom}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, fake)
	if !errors.Is(err, boom) {
		t.Fatalf("RunClaim err = %v, want a wrap of %v", err, boom)
	}
	if res.Claimed || res.AlreadyHeld {
		t.Errorf("result = %+v, want no hold reported", res)
	}
	if fake.CommitCalls != 0 {
		t.Errorf("CommitCalls = %d, want 0", fake.CommitCalls)
	}
}

// TestRunClaim_UnpublishedReClaimReleasedAtOriginIsALoss: origin released
// the issue (an unclaim after our claim), so this checkout no longer holds
// it. The conservative answer: a loss with no winner and no op written; a
// later claim starts fresh.
func TestRunClaim_UnpublishedReClaimReleasedAtOriginIsALoss(t *testing.T) {
	root, paths, clock := u2PlantHold(t)
	fake := &FakeGitOps{UnpublishedResult: true}
	fake.OnPullRebase = func() {
		u2WriteRawUnclaim(t, paths.Ops, "act-1234", hlc.HLC{Wall: 1_700_000_000_800, Logical: 0, NodeID: "11111111"})
	}
	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, fake)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if res.Claimed || res.Winner != "" {
		t.Errorf("result = %+v, want a loss with no winner", res)
	}
	if fake.CommitCalls != 0 {
		t.Errorf("CommitCalls = %d, want 0", fake.CommitCalls)
	}
}

// TestRunClaim_UnpublishedIsolatedDoesNotPull: --isolated asked for no
// confirmation, so an unpublished hold is still answered from local state.
func TestRunClaim_UnpublishedIsolatedDoesNotPull(t *testing.T) {
	root, _, clock := u2PlantHold(t)
	fake := &FakeGitOps{UnpublishedResult: true}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Isolated: true}, clock, fake)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed || !res.AlreadyHeld || res.Republish {
		t.Errorf("result = %+v, want a local hold, Republish false", res)
	}
	if fake.PullRebaseCalls != 0 || fake.UnpublishedCalls != 0 {
		t.Errorf("PullRebaseCalls=%d UnpublishedCalls=%d, want 0 and 0 under --isolated", fake.PullRebaseCalls, fake.UnpublishedCalls)
	}
}

// u2WriteRawUnclaim writes an unclaim op directly to disk.
func u2WriteRawUnclaim(t *testing.T, rootOps, issueID string, h hlc.HLC) {
	t.Helper()
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "unclaim",
		IssueID:       issueID,
		Payload:       []byte(`{}`),
		HLC:           h,
		NodeID:        h.NodeID,
	}
	if err := env.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	fsLock := func() (func(), error) { return func() {}, nil }
	if _, _, err := op.ProbeAndWrite(rootOps, env, body, fsLock); err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
}
