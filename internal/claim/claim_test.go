package claim

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// FakeGitOps records calls and can be configured to return errors. It is
// the test-side stand-in for the real git wrapper.
type FakeGitOps struct {
	CommitCalls     int
	PullRebaseCalls int
	PushCalls       int

	CommitErr     error
	PullRebaseErr error
	PushErr       error

	LastCommitMessage string
}

func (f *FakeGitOps) Commit(message string) error {
	f.CommitCalls++
	f.LastCommitMessage = message
	return f.CommitErr
}

func (f *FakeGitOps) PullRebase() error {
	f.PullRebaseCalls++
	return f.PullRebaseErr
}

func (f *FakeGitOps) Push() error {
	f.PushCalls++
	return f.PushErr
}

// fakeNow returns a closure producing a fixed wall-time in unix-ms.
func fakeNow(ms int64) func() int64 {
	return func() int64 { return ms }
}

// newClock constructs an HLC clock with the given node id and a fake now.
func newClock(t *testing.T, nodeID string, ms int64) *hlc.Clock {
	t.Helper()
	return hlc.NewClock(nodeID, fakeNow(ms))
}

// initRepo creates a minimal .act/ layout under root and returns the layout.
func initRepo(t *testing.T, root string) config.LayoutPaths {
	t.Helper()
	paths := config.Layout(root)
	if err := config.InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	return paths
}

// writeRawClaim writes a hand-crafted claim op directly to disk (bypassing
// RunClaim) to simulate a concurrent writer's op file.
func writeRawClaim(t *testing.T, rootOps, issueID, assignee string, h hlc.HLC) string {
	t.Helper()
	payloadBytes := []byte(`{"assignee":"` + assignee + `"}`)
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "claim",
		IssueID:       issueID,
		Payload:       payloadBytes,
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
	path, _, err := op.ProbeAndWrite(rootOps, env, body, fsLock)
	if err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
	return path
}

// TestRunClaim_HappyPath: single writer, no other writers; result.Claimed=true.
func TestRunClaim_HappyPath(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true; result=%+v", res)
	}
	if res.Winner != "alice" {
		t.Errorf("Winner=%q, want alice", res.Winner)
	}
	if res.YourOpHash == "" {
		t.Errorf("YourOpHash empty")
	}
	if git.CommitCalls != 1 {
		t.Errorf("CommitCalls=%d, want 1", git.CommitCalls)
	}
	if git.PullRebaseCalls != 1 {
		t.Errorf("PullRebaseCalls=%d, want 1", git.PullRebaseCalls)
	}
	if git.PushCalls != 0 {
		t.Errorf("PushCalls=%d, want 0 (Push not requested)", git.PushCalls)
	}
}

// TestRunClaim_TwoWriterRace: an earlier-HLC competing claim already exists
// on disk; our claim has a later HLC and loses with the structured Winner.
func TestRunClaim_TwoWriterRace_WeLose(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Pre-existing competitor claim with EARLIER wall — wins.
	competitorHLC := hlc.HLC{Wall: 1_700_000_000_000, Logical: 0, NodeID: "11111111"}
	writeRawClaim(t, paths.Ops, "act-1234", "carol", competitorHLC)

	// Our clock returns a later wall.
	clock := newClock(t, "abcdef01", 1_700_000_000_500)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if res.Claimed {
		t.Fatalf("Claimed=true, want false (competitor was earlier); result=%+v", res)
	}
	if res.Winner != "carol" {
		t.Errorf("Winner=%q, want carol", res.Winner)
	}
	if res.YourOpHash == "" {
		t.Errorf("YourOpHash empty")
	}
}

// TestRunClaim_TwoWriterRace_WeWin: a competitor claim with LATER HLC; ours
// is earlier and wins.
func TestRunClaim_TwoWriterRace_WeWin(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Competitor claim with LATER wall.
	competitorHLC := hlc.HLC{Wall: 1_700_000_001_000, Logical: 0, NodeID: "22222222"}
	writeRawClaim(t, paths.Ops, "act-1234", "carol", competitorHLC)

	// Our clock returns the earlier wall.
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true (we were earlier); result=%+v", res)
	}
	if res.Winner != "alice" {
		t.Errorf("Winner=%q, want alice", res.Winner)
	}
}

// TestRunClaim_HLCDriftFailFast: a clock far in the future should fail the
// plausibility check before any gitOps method is called.
func TestRunClaim_HLCDriftFailFast(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Plant a config whose repo reference HLC is FAR IN THE FUTURE relative
	// to the local clock's now(). Per hlc.Plausible the reference is
	// max(now, repoRef.Wall). Send() returns now (since prev is zero),
	// then |wall - ref| = |now - repoRef| > 5min => drift error.
	now := int64(1_700_000_000_000)
	farFutureRef := now + hlc.PlausibilityBudgetMs + 60_000
	cfg := config.Config{
		NodeID:    "abcdef01",
		CreatedAt: "2026-04-29T00:00:00Z",
		Version:   "0.1.0",
		LastHLC:   config.HLCState{Wall: farFutureRef, Logical: 0},
	}
	if err := config.WriteConfig(paths, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	clock := newClock(t, "abcdef01", now)
	git := &FakeGitOps{}

	_, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err == nil {
		t.Fatalf("RunClaim: want error, got nil")
	}
	if !errors.Is(err, hlc.ErrHLCImplausible) {
		t.Fatalf("RunClaim error=%v, want ErrHLCImplausible", err)
	}
	if git.CommitCalls != 0 || git.PullRebaseCalls != 0 || git.PushCalls != 0 {
		t.Errorf("git ops invoked despite drift fail-fast: %+v", git)
	}

	// Confirm we did NOT write any op file under .act/ops.
	matches, _ := filepath.Glob(filepath.Join(paths.Ops, "*", "*", "*.json"))
	if len(matches) != 0 {
		t.Errorf("op files written despite drift fail-fast: %v", matches)
	}
}

// TestRunClaim_IsolatedSkipsPullRebase: --isolated must NOT call PullRebase.
func TestRunClaim_IsolatedSkipsPullRebase(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Isolated: true}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true; result=%+v", res)
	}
	if git.PullRebaseCalls != 0 {
		t.Errorf("PullRebaseCalls=%d, want 0 under --isolated", git.PullRebaseCalls)
	}
	if git.CommitCalls != 1 {
		t.Errorf("CommitCalls=%d, want 1", git.CommitCalls)
	}
}

// TestRunClaim_IsolatedAndPushIsRejected: spec §4 universal flags make
// --isolated --push exit 2.
func TestRunClaim_IsolatedAndPushIsRejected(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{}

	_, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Isolated: true, Push: true}, clock, git)
	if !errors.Is(err, ErrInvalidFlags) {
		t.Fatalf("RunClaim err=%v, want ErrInvalidFlags", err)
	}
}

// TestRunClaim_PushOnWin: --push triggers Push() only when we win.
func TestRunClaim_PushOnWin(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Push: true}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true")
	}
	if git.PushCalls != 1 {
		t.Errorf("PushCalls=%d, want 1", git.PushCalls)
	}
}

// TestRunClaim_PushOnlyOnWin: --push must NOT push when we lose.
func TestRunClaim_PushOnlyOnWin_LossSkipsPush(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Plant earlier competing claim.
	earlyHLC := hlc.HLC{Wall: 1_700_000_000_000, Logical: 0, NodeID: "33333333"}
	writeRawClaim(t, paths.Ops, "act-1234", "carol", earlyHLC)

	clock := newClock(t, "abcdef01", 1_700_000_001_000)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice", Push: true}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if res.Claimed {
		t.Fatalf("Claimed=true, want false")
	}
	if git.PushCalls != 0 {
		t.Errorf("PushCalls=%d, want 0 when we lose", git.PushCalls)
	}
}

// TestRunClaim_WaitRetry: on first-attempt loss, --wait sleeps and retries
// up to the timeout. Uses an injected sleeper and a controllable clock.
func TestRunClaim_WaitRetry(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Plant a competing claim that ALWAYS beats us (very early wall).
	earlyHLC := hlc.HLC{Wall: 1_000_000_000_000, Logical: 0, NodeID: "44444444"}
	writeRawClaim(t, paths.Ops, "act-1234", "carol", earlyHLC)

	// Clock advances every Send so HLCs differ across attempts. We use a
	// counter-based now so the per-attempt op file paths are distinct.
	var nowCounter int64 = 1_000_000_000_500
	clock := hlc.NewClock("abcdef01", func() int64 {
		v := atomic.AddInt64(&nowCounter, 1)
		return v
	})

	// Sleep counter; we expect at most 3 sleeps before WaitTimeout aborts.
	var sleeps []time.Duration
	sleep := func(d time.Duration) { sleeps = append(sleeps, d) }

	git := &FakeGitOps{}

	// WaitTimeout = 100ms; with backoff 1s the first sleep already saturates
	// the budget so we should observe exactly 1 sleep and 2 commits.
	res, err := runClaimInternal(root, "act-1234", Options{
		Assignee:    "alice",
		Wait:        true,
		WaitTimeout: 100 * time.Millisecond,
	}, clock, git, sleep, noJitter)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if res.Claimed {
		t.Fatalf("Claimed=true, want false (carol always wins)")
	}
	if res.Winner != "carol" {
		t.Errorf("Winner=%q, want carol", res.Winner)
	}
	if len(sleeps) == 0 {
		t.Fatalf("no sleeps recorded; want at least 1")
	}
	if sleeps[0] > 100*time.Millisecond {
		t.Errorf("first sleep=%v, want <= 100ms (capped by WaitTimeout)", sleeps[0])
	}
	// Each attempt commits once; we want >=2 commits (initial + 1 retry).
	if git.CommitCalls < 2 {
		t.Errorf("CommitCalls=%d, want >= 2", git.CommitCalls)
	}
}

// TestRunClaim_WaitRetryThreeAttempts: with a generous WaitTimeout and a
// controllable sleeper, we can observe 3+ retries.
func TestRunClaim_WaitRetryThreeAttempts(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Plant a perpetually-winning competitor.
	earlyHLC := hlc.HLC{Wall: 1_000_000_000_000, Logical: 0, NodeID: "55555555"}
	writeRawClaim(t, paths.Ops, "act-1234", "carol", earlyHLC)

	var nowCounter int64 = 1_000_000_000_500
	clock := hlc.NewClock("abcdef01", func() int64 {
		v := atomic.AddInt64(&nowCounter, 1)
		return v
	})

	var sleeps []time.Duration
	sleep := func(d time.Duration) { sleeps = append(sleeps, d) }

	git := &FakeGitOps{}
	// WaitTimeout = 7s: allows sleeps 1s + 2s + 4s = 7s exactly. Even though
	// the protocol caps the third sleep to fit the remaining budget, we
	// should observe exactly 3 sleeps.
	res, err := runClaimInternal(root, "act-1234", Options{
		Assignee:    "alice",
		Wait:        true,
		WaitTimeout: 7 * time.Second,
	}, clock, git, sleep, noJitter)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if res.Claimed {
		t.Fatalf("Claimed=true, want false")
	}
	if len(sleeps) != 3 {
		t.Errorf("len(sleeps)=%d, want 3; sleeps=%v", len(sleeps), sleeps)
	}
	if len(sleeps) >= 1 && sleeps[0] != 1*time.Second {
		t.Errorf("sleeps[0]=%v, want 1s", sleeps[0])
	}
	if len(sleeps) >= 2 && sleeps[1] != 2*time.Second {
		t.Errorf("sleeps[1]=%v, want 2s", sleeps[1])
	}
	if len(sleeps) >= 3 && sleeps[2] != 4*time.Second {
		t.Errorf("sleeps[2]=%v, want 4s", sleeps[2])
	}
	// 4 attempts total: initial + 3 retries.
	if git.CommitCalls != 4 {
		t.Errorf("CommitCalls=%d, want 4", git.CommitCalls)
	}
}

// TestRunClaim_EmptyAssigneeRejected: required-field check.
func TestRunClaim_EmptyAssigneeRejected(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{}
	_, err := RunClaim(root, "act-1234", Options{Assignee: ""}, clock, git)
	if !errors.Is(err, ErrEmptyAssignee) {
		t.Fatalf("RunClaim err=%v, want ErrEmptyAssignee", err)
	}
}

// TestRunClaim_ClaimAfterCloseExcluded: a close op invalidates earlier
// claims; if we are the only post-close claimer, we should still win and
// our op should be the chosen winner.
func TestRunClaim_ClaimAfterCloseExcluded(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// 1) Earliest claim by carol.
	carol := hlc.HLC{Wall: 1_700_000_000_000, Logical: 0, NodeID: "66666666"}
	writeRawClaim(t, paths.Ops, "act-1234", "carol", carol)

	// 2) A close op AFTER carol's claim, BEFORE our attempt.
	closeHLC := hlc.HLC{Wall: 1_700_000_000_500, Logical: 0, NodeID: "66666666"}
	writeRawClose(t, paths.Ops, "act-1234", closeHLC, "done")

	// 3) Our claim runs after the close. It should be the only active claim
	// and thus win (assuming the protocol does not reject claims on closed
	// issues — the apply layer does that, but the disk-level winner
	// computation here only filters by close window).
	clock := newClock(t, "abcdef01", 1_700_000_001_000)
	git := &FakeGitOps{}
	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true (carol's claim is pre-close); result=%+v", res)
	}
	if res.Winner != "alice" {
		t.Errorf("Winner=%q, want alice", res.Winner)
	}
}

// TestRunClaim_PullRebaseNoUpstreamIsNoopSuccess: act-fdb2 fix #1.
// When the GitOps.PullRebase implementation reports ErrNoUpstream the
// claim protocol must short-circuit the rebase step (no remote, nothing
// to rebase against) and complete as a normal win — without --isolated
// being required by the caller. This is the local-first / fresh-repo
// case CLAUDE.md's canonical loop now relies on.
func TestRunClaim_PullRebaseNoUpstreamIsNoopSuccess(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	git := &FakeGitOps{PullRebaseErr: ErrNoUpstream}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true; result=%+v", res)
	}
	if res.Winner != "alice" {
		t.Errorf("Winner=%q, want alice", res.Winner)
	}
	// PullRebase WAS called (we don't skip the call, only its error).
	if git.PullRebaseCalls != 1 {
		t.Errorf("PullRebaseCalls=%d, want 1 (called, error swallowed)", git.PullRebaseCalls)
	}
	if git.CommitCalls != 1 {
		t.Errorf("CommitCalls=%d, want 1", git.CommitCalls)
	}
}

// TestRunClaim_PullRebaseOtherErrorStillFails: any non-ErrNoUpstream
// PullRebase error remains a hard failure (rebase conflict, network).
func TestRunClaim_PullRebaseOtherErrorStillFails(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	boom := errors.New("rebase conflict on .act/ops/foo.json")
	git := &FakeGitOps{PullRebaseErr: boom}

	_, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err == nil {
		t.Fatalf("RunClaim: want error, got nil")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("RunClaim err=%v, want wrap of %v", err, boom)
	}
}

// TestRunClaim_PullRebaseSoftFailIsNoopSuccess (act-68f08b): when the
// PullRebase implementation reports ErrPullRebaseSoftFail (the canonical
// case is a dirty working tree from a prior read mutating
// `.act/index.db`), RunClaim must swallow the error and complete the
// claim as a win. By the time PullRebase fires the new claim op is
// already on disk and committed locally, so the local state is durable;
// the op log is convergent and the next read/write will reconcile.
func TestRunClaim_PullRebaseSoftFailIsNoopSuccess(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	clock := newClock(t, "abcdef01", 1_700_000_000_000)
	// Wrap the sentinel like the gitops layer does, to exercise the
	// errors.Is unwrap path the dispatch uses.
	wrapped := fmt.Errorf("%w: git pull --rebase: exit status 128 (stderr: error: cannot pull with rebase: You have unstaged changes.)", ErrPullRebaseSoftFail)
	git := &FakeGitOps{PullRebaseErr: wrapped}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true; result=%+v", res)
	}
	if res.Winner != "alice" {
		t.Errorf("Winner=%q, want alice", res.Winner)
	}
	if git.PullRebaseCalls != 1 {
		t.Errorf("PullRebaseCalls=%d, want 1 (called, soft-fail swallowed)", git.PullRebaseCalls)
	}
	if git.CommitCalls != 1 {
		t.Errorf("CommitCalls=%d, want 1 (commit landed before PullRebase)", git.CommitCalls)
	}
}

// TestRunClaim_IdempotentReClaimSameAssignee: act-fdb2 fix #2. Re-running
// claim against an issue already won by the same assignee returns
// success WITHOUT writing a new claim op (otherwise the second op loses
// the (earliest-wins) ordering against the first and the agent reports
// "lost race against itself").
func TestRunClaim_IdempotentReClaimSameAssignee(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Plant a pre-existing winning claim by alice (us).
	priorHLC := hlc.HLC{Wall: 1_700_000_000_000, Logical: 0, NodeID: "abcdef01"}
	priorPath := writeRawClaim(t, paths.Ops, "act-1234", "alice", priorHLC)

	// Our re-claim attempt with a LATER clock: without idempotence we'd
	// write a second op and lose to the earlier one.
	clock := newClock(t, "abcdef01", 1_700_000_001_000)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if !res.Claimed {
		t.Fatalf("Claimed=false, want true (idempotent re-claim); result=%+v", res)
	}
	if res.Winner != "alice" {
		t.Errorf("Winner=%q, want alice", res.Winner)
	}

	// Critically: NO new op was written, NO commit/pull happened.
	if git.CommitCalls != 0 {
		t.Errorf("CommitCalls=%d, want 0 (idempotent path must not write)", git.CommitCalls)
	}
	if git.PullRebaseCalls != 0 {
		t.Errorf("PullRebaseCalls=%d, want 0 (idempotent path must not pull)", git.PullRebaseCalls)
	}
	matches, _ := filepath.Glob(filepath.Join(paths.Ops, "act-1234", "*", "*-claim.json"))
	if len(matches) != 1 {
		t.Errorf("claim ops on disk = %d, want 1 (only the prior op); files=%v", len(matches), matches)
	}
	// The single op on disk is the one we planted, untouched.
	if len(matches) == 1 && matches[0] != priorPath {
		t.Errorf("claim op path = %q, want %q (prior op untouched)", matches[0], priorPath)
	}
}

// TestRunClaim_IdempotenceDoesNotMaskRealLoss: if the prior winner is a
// DIFFERENT assignee, re-claim must NOT short-circuit — we'd silently
// claim an issue we don't own. This is the regression guard for the
// idempotence check's predicate.
func TestRunClaim_IdempotenceDoesNotMaskRealLoss(t *testing.T) {
	root := t.TempDir()
	paths := initRepo(t, root)

	// Pre-existing winning claim by carol (NOT us).
	carolHLC := hlc.HLC{Wall: 1_700_000_000_000, Logical: 0, NodeID: "11111111"}
	writeRawClaim(t, paths.Ops, "act-1234", "carol", carolHLC)

	clock := newClock(t, "abcdef01", 1_700_000_001_000)
	git := &FakeGitOps{}

	res, err := RunClaim(root, "act-1234", Options{Assignee: "alice"}, clock, git)
	if err != nil {
		t.Fatalf("RunClaim: %v", err)
	}
	if res.Claimed {
		t.Fatalf("Claimed=true, want false (carol won); result=%+v", res)
	}
	if res.Winner != "carol" {
		t.Errorf("Winner=%q, want carol", res.Winner)
	}
	// We DID write our op (and commit) — only same-assignee short-circuits.
	if git.CommitCalls != 1 {
		t.Errorf("CommitCalls=%d, want 1 (different-assignee path must write)", git.CommitCalls)
	}
}

// writeRawClose writes a close op directly to disk for fixture setup.
func writeRawClose(t *testing.T, rootOps, issueID string, h hlc.HLC, reason string) string {
	t.Helper()
	payloadBytes := []byte(`{"reason":"` + reason + `"}`)
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       issueID,
		Payload:       payloadBytes,
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
	path, _, err := op.ProbeAndWrite(rootOps, env, body, fsLock)
	if err != nil {
		t.Fatalf("ProbeAndWrite: %v", err)
	}
	return path
}
