package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/gitops"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeUpdateRepoWithIssue seeds a fresh repo + .act/ + a single create
// op via RunCreate, and returns (repoRoot, issueID).
func makeUpdateRepoWithIssue(t *testing.T) (string, string) {
	t.Helper()
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "seed", Type: "task"})
	if code != 0 {
		t.Fatalf("seed: code = %d, out=%+v", code, out)
	}
	return root, out.(CreateResult).ID
}

// strPtr is a tiny test helper for *string fields.
func strPtr(s string) *string { return &s }

// intPtr is a tiny test helper for *int fields.
func intPtr(i int) *int { return &i }

// TestRunUpdate_DescriptionOnly: a single mutating flag writes one op.
func TestRunUpdate_DescriptionOnly(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:          id,
		Description: strPtr("new description"),
	})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(UpdateResult)
	if !ok {
		t.Fatalf("type %T, want UpdateResult", out)
	}
	if res.OpsWritten != 1 {
		t.Errorf("OpsWritten = %d, want 1", res.OpsWritten)
	}
	if !res.Committed {
		t.Errorf("Committed = false, want true")
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-update_field.json"))
	if len(matches) != 1 {
		t.Fatalf("want 1 update_field op, got %d: %v", len(matches), matches)
	}
}

// TestRunUpdate_DescriptionRoundTripsAsString: regression for the
// canonicaljson + json.RawMessage bug, which produced an on-disk update_field
// op whose `value` was the byte-array form of "new description text".
// After update + show the description must come back as the literal string,
// not as [34, 110, 101, ...] bytes.
func TestRunUpdate_DescriptionRoundTripsAsString(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	const desc = "new description text"
	if _, code := RunUpdate(root, UpdateOptions{
		ID:          id,
		Description: strPtr(desc),
	}); code != 0 {
		t.Fatalf("update: code = %d", code)
	}
	out, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show: code = %d, out=%+v", code, out)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("show output type = %T, want ShowResult", out)
	}
	got, ok := res.Fields["description"]
	if !ok {
		t.Fatalf("description missing from show output: %+v", res.Fields)
	}
	gotStr, ok := got.(string)
	if !ok {
		t.Fatalf("description type = %T (%v), want string", got, got)
	}
	if gotStr != desc {
		t.Errorf("description = %q, want %q", gotStr, desc)
	}
}

// TestRunUpdate_MultiFieldGenerates2Ops: two flags → two ops.
func TestRunUpdate_MultiFieldGenerates2Ops(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:          id,
		Description: strPtr("d"),
		Priority:    intPtr(2),
	})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res := out.(UpdateResult)
	if res.OpsWritten != 2 {
		t.Errorf("OpsWritten = %d, want 2", res.OpsWritten)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-update_field.json"))
	if len(matches) != 2 {
		t.Errorf("want 2 update_field ops, got %d", len(matches))
	}
}

// TestRunUpdate_StatusClosedRejected: --status closed → exit 2 always.
func TestRunUpdate_StatusClosedRejected(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:     id,
		Status: strPtr("closed"),
	})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
	e, ok := out.(UpdateErrorOutput)
	if !ok {
		t.Fatalf("type %T, want UpdateErrorOutput", out)
	}
	if e.Error != "bad_flag" {
		t.Errorf("Error = %q, want bad_flag", e.Error)
	}
}

// TestRunUpdate_StatusInProgressRejected: --status in_progress → exit 2.
// in_progress only flows through --claim per §5.B.3.
func TestRunUpdate_StatusInProgressRejected(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:     id,
		Status: strPtr("in_progress"),
	})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
	e := out.(UpdateErrorOutput)
	if e.Error != "bad_flag" {
		t.Errorf("Error = %q, want bad_flag", e.Error)
	}
}

// TestRunUpdate_NoCommitAndPushRejected: universal write-flag conflict.
func TestRunUpdate_NoCommitAndPushRejected(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:          id,
		Description: strPtr("x"),
		NoCommit:    true,
		Push:        true,
	})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
	e := out.(UpdateErrorOutput)
	if e.Error != "bad_flag" {
		t.Errorf("Error = %q, want bad_flag", e.Error)
	}
}

// TestRunUpdate_ClaimHappyPath: --claim against a fresh issue wins.
func TestRunUpdate_ClaimHappyPath(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:       id,
		Claim:    true,
		Isolated: true, // skip pull-rebase since the test repo has no remote
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(UpdateClaimResult)
	if !ok {
		t.Fatalf("type %T, want UpdateClaimResult", out)
	}
	if !res.Claimed || !res.OK {
		t.Errorf("Claimed=%v OK=%v, want true/true", res.Claimed, res.OK)
	}
	if res.Winner == "" {
		t.Errorf("Winner empty")
	}
	// Verify the claim op was written.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-claim.json"))
	if len(matches) < 1 {
		t.Errorf("expected at least 1 claim op, got %d", len(matches))
	}
}

// TestRunUpdate_ClaimLossWithPreExistingWinner: plant an earlier claim
// op on disk so our --claim invocation loses; expect exit 5 (claim_lost)
// with a structured loss envelope carrying error=claim_lost. The exit code
// and slug match spec §error-envelope's universal exit-code table
// (reconciled in act-a373bb).
func TestRunUpdate_ClaimLossWithPreExistingWinner(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)

	// Plant a competitor claim with an EARLIER wall so it always wins
	// the (wall, logical, op_hash) ordering.
	paths := config.Layout(root)
	competitorHLC := hlc.HLC{Wall: 1, Logical: 0, NodeID: "11111111"}
	writeRawClaimUpdate(t, paths.Ops, id, "competitor", competitorHLC)

	out, code := RunUpdate(root, UpdateOptions{
		ID:       id,
		Claim:    true,
		Isolated: true,
	})
	if code != 5 {
		t.Fatalf("code = %d, want 5 (claim_lost); out=%+v", code, out)
	}
	res, ok := out.(UpdateClaimResult)
	if !ok {
		t.Fatalf("type %T, want UpdateClaimResult", out)
	}
	if res.Claimed || res.OK {
		t.Errorf("Claimed=%v OK=%v, want false/false", res.Claimed, res.OK)
	}
	if res.Error != ErrClaimLost {
		t.Errorf("Error = %q, want %q", res.Error, ErrClaimLost)
	}
	if res.Winner != "competitor" {
		t.Errorf("Winner = %q, want competitor", res.Winner)
	}
	if res.Reason != "lost-race" {
		t.Errorf("Reason = %q, want lost-race", res.Reason)
	}
}

// TestRunUpdate_UnknownID: an unresolvable id is exit 3.
func TestRunUpdate_UnknownID(t *testing.T) {
	root, _ := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:          "act-deadbeef",
		Description: strPtr("x"),
	})
	if code != 3 {
		t.Fatalf("code = %d, want 3; out=%+v", code, out)
	}
	e, ok := out.(UpdateErrorOutput)
	if !ok {
		t.Fatalf("type %T, want UpdateErrorOutput", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("Error = %q, want issue_not_found", e.Error)
	}
}

// TestRunUpdate_WaitWithoutClaimRejected: --wait without --claim → exit 2.
func TestRunUpdate_WaitWithoutClaimRejected(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:   id,
		Wait: true,
	})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
}

// TestRunUpdate_ClaimMutuallyExclusiveWithFieldFlags: --claim + --priority
// is rejected as a flag conflict.
func TestRunUpdate_ClaimMutuallyExclusiveWithFieldFlags(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:       id,
		Claim:    true,
		Priority: intPtr(1),
	})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
}

// TestRunUpdate_ClaimNoUpstreamSucceeds: act-fdb2 fix #1, integration.
// `act update --claim` against a repo with NO upstream remote configured
// must succeed (exit 0) and write a claim op — without the caller having
// to pass --isolated. The fresh-repo / local-first case is the canonical
// loop in the act skill and should not require an escape-hatch flag.
func TestRunUpdate_ClaimNoUpstreamSucceeds(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	// makeCreateRepo produces a repo with no remote/upstream by default,
	// which is exactly the fdb2 reproduction case.
	out, code := RunUpdate(root, UpdateOptions{
		ID:    id,
		Claim: true,
		// NOTE: no Isolated:true — that's the whole point of the fix.
	})
	if code != 0 {
		t.Fatalf("code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(UpdateClaimResult)
	if !ok {
		t.Fatalf("type %T, want UpdateClaimResult", out)
	}
	if !res.Claimed || !res.OK {
		t.Errorf("Claimed=%v OK=%v, want true/true", res.Claimed, res.OK)
	}
	// The claim op IS written (the rebase short-circuit must not skip the
	// write — fix #1 only affects the rebase step).
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-claim.json"))
	if len(matches) != 1 {
		t.Errorf("expected 1 claim op on disk, got %d (%v)", len(matches), matches)
	}
}

// TestRunUpdate_ClaimIdempotentSameNode: act-fdb2 fix #2, integration.
// Re-running `act update --claim` against an issue this node already
// owns must return success (exit 0) with no new op written. Before the
// fix, the second invocation wrote a later-HLC claim, then lost the
// (earliest-wins) ordering against its own first op and reported
// `Lost claim race for <id> (winner=<self-node-id>)`.
func TestRunUpdate_ClaimIdempotentSameNode(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)

	// First claim: win.
	out1, code1 := RunUpdate(root, UpdateOptions{ID: id, Claim: true})
	if code1 != 0 {
		t.Fatalf("first claim code = %d, want 0; out=%+v", code1, out1)
	}
	matchesAfterFirst, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-claim.json"))
	if len(matchesAfterFirst) != 1 {
		t.Fatalf("after first claim: %d ops on disk, want 1 (%v)", len(matchesAfterFirst), matchesAfterFirst)
	}

	// Second claim: idempotent success, NO new op written.
	out2, code2 := RunUpdate(root, UpdateOptions{ID: id, Claim: true})
	if code2 != 0 {
		t.Fatalf("second claim code = %d, want 0 (idempotent); out=%+v", code2, out2)
	}
	res2, ok := out2.(UpdateClaimResult)
	if !ok {
		t.Fatalf("type %T, want UpdateClaimResult", out2)
	}
	if !res2.Claimed || !res2.OK {
		t.Errorf("second claim Claimed=%v OK=%v, want true/true", res2.Claimed, res2.OK)
	}
	if res2.Reason == "lost-race" {
		t.Errorf("Reason=%q, want empty (re-claim against self must not be a loss)", res2.Reason)
	}

	// Op count unchanged.
	matchesAfterSecond, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-claim.json"))
	if len(matchesAfterSecond) != 1 {
		t.Errorf("after second claim: %d ops on disk, want 1 (idempotent); files=%v",
			len(matchesAfterSecond), matchesAfterSecond)
	}
}

// TestClaimGitOps_WorktreeContext_StagesViaOverride asserts act-f64d6e:
// claimGitOps.Commit must stage the ops subtree through the inner
// ActGitOps' --git-dir/--work-tree override (StageOpFile), NOT through a
// bare `git add` that relies on cwd-discovery. The harm being guarded
// against: in a worktree/migration context where the nested .act/.git is
// not discoverable from the .act/ cwd, a cwd-discovered `git add` walks
// UP and stages the act op into the WRONG (ambient host) git index —
// silently polluting the host repo's tracked history with .act/ops/**.
//
// Fixture (the bug-visible shape): a host repo that does NOT gitignore
// .act/, with .act/ present as a plain directory that has NO nested
// .git of its own. NewActGitOps(actDir) pins the override git-dir to
// <actDir>/.git (absent here) — so the override path FAILS LOUDLY rather
// than silently retargeting the host.
//
// How this fails on the UNFIXED code: the old claimGitOps.runGit ran
// `git add -- ops` with only cmd.Dir=<actDir> set. With <actDir>/.git
// absent, git's cwd-discovery walks up to the host repo and (because the
// host does not ignore .act/) STAGES .act/ops/...op.json into the host
// index, and Commit returns nil. The test asserts (a) Commit returns a
// non-nil error and (b) the host index has NO .act/ path staged — both
// fail pre-fix (no error; host index polluted). Post-fix StageOpFile
// pins --git-dir=<actDir>/.git, which does not exist, so the stage
// errors with "not a git repository" before any host write — error
// returned, host index clean, both assertions pass.
func TestClaimGitOps_WorktreeContext_StagesViaOverride(t *testing.T) {
	host := t.TempDir()
	mustGit(t, host, "init", "-q", "-b", "main")
	mustGit(t, host, "config", "user.email", "u@example.com")
	mustGit(t, host, "config", "user.name", "U")
	mustGit(t, host, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(host, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	// Deliberately NO `.act/` line in .gitignore: this is what lets a
	// cwd-discovered `git add` silently stage into the host index instead
	// of being refused — the silent wrong-repo write the fix prevents.
	mustGit(t, host, "add", "README")
	mustGit(t, host, "commit", "-q", "--no-verify", "-m", "init")

	// .act/ is a plain directory with an unstaged op file and NO nested
	// .git of its own (worktree/mid-migration shape).
	actDir := filepath.Join(host, ".act")
	opPath := filepath.Join(actDir, "ops", "act-aaaaaa", "2026-05", "op-create.json")
	if err := os.MkdirAll(filepath.Dir(opPath), 0o755); err != nil {
		t.Fatalf("mkdir ops: %v", err)
	}
	if err := os.WriteFile(opPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write op: %v", err)
	}

	// Production wrapper, production constructor: the override git-dir is
	// <actDir>/.git, which does not exist in this fixture.
	wrapped := &claimGitOps{inner: gitops.NewActGitOps(actDir)}

	err := wrapped.Commit("act-op: (act-aaaaaa) claim")
	if err == nil {
		t.Fatalf("claimGitOps.Commit: want error (override git-dir absent), got nil — " +
			"a cwd-discovered `git add` silently staged into the ambient host index")
	}

	// The host index must NOT carry any .act/ path: the stage must never
	// retarget the ambient repo.
	hostStaged := mustGitOutput(t, host, "diff", "--cached", "--name-only")
	if strings.Contains(hostStaged, ".act/") {
		t.Fatalf("host index polluted with act ops: %q (claim stage leaked into ambient repo)", hostStaged)
	}
}

// writeRawClaimUpdate writes a hand-crafted claim op directly to disk
// and stages+commits it so the test repo's working tree stays clean.
// (Bypassing RunClaim lets tests simulate concurrent writers.)
func writeRawClaimUpdate(t *testing.T, rootOps, issueID, assignee string, h hlc.HLC) string {
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
	// Phase 1: rootOps is "<hostRoot>/.act/ops". The nested .act/ repo
	// is its parent, and the staging git command must run in THAT
	// working tree so the .act/ gitignore on the host doesn't filter
	// the new op file out.
	actDir := filepath.Dir(rootOps)
	mustGit(t, actDir, "add", path)
	mustGit(t, actDir, "commit", "-q", "--no-verify", "-m", "plant claim")
	return path
}
