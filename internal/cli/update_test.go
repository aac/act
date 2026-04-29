package cli

import (
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/config"
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
// op on disk so our --claim invocation loses; expect exit 1 with a
// structured loss envelope.
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
	if code != 1 {
		t.Fatalf("code = %d, want 1; out=%+v", code, out)
	}
	res, ok := out.(UpdateClaimResult)
	if !ok {
		t.Fatalf("type %T, want UpdateClaimResult", out)
	}
	if res.Claimed || res.OK {
		t.Errorf("Claimed=%v OK=%v, want false/false", res.Claimed, res.OK)
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
	// rootOps == "<repoRoot>/.act/ops"; back out twice to get repoRoot
	// so the staging git command runs in the right working tree.
	repoRoot := filepath.Dir(filepath.Dir(rootOps))
	mustGit(t, repoRoot, "add", path)
	mustGit(t, repoRoot, "commit", "-q", "--no-verify", "-m", "plant claim")
	return path
}
