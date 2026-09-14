package main

// act-b45379: `act update --claim` writes its op through internal/claim,
// outside the shared write helper, and never got the act-94272e / act-a3160b
// withdrawal. A claim whose stage or commit failed left its op in ops/, so
// the command exited non-zero while the next `act show` said in_progress.
// These tests drive the binary as a subprocess, the only boundary where the
// exit status and a later read are both visible.

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type claimqEnvelope struct {
	Error   string         `json:"error"`
	Details map[string]any `json:"details"`
}

func claimqParse(t *testing.T, out string) claimqEnvelope {
	t.Helper()
	var e claimqEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &e); err != nil {
		t.Fatalf("parse error envelope %q: %v", out, err)
	}
	return e
}

// claimqClaimOps lists claim op files for id still in the op log.
func claimqClaimOps(t *testing.T, dir, id string) []string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(dir, ".act", "ops", id, "*", "*-claim.json"))
	return m
}

// claimqOnlyStamp returns the single .act/.failed-ops/<stamp> dir name and
// asserts quarantined_op names a file under it that exists.
func claimqOnlyStamp(t *testing.T, dir string, e claimqEnvelope, opSuffix string) string {
	t.Helper()
	failedRoot := filepath.Join(dir, ".act", ".failed-ops")
	stamps, err := os.ReadDir(failedRoot)
	if err != nil || len(stamps) != 1 {
		t.Fatalf("want exactly one .act/.failed-ops/<timestamp> dir; entries=%v err=%v", stamps, err)
	}
	stamp := stamps[0].Name()
	q, _ := e.Details["quarantined_op"].(string)
	if !strings.HasPrefix(q, filepath.Join(failedRoot, stamp, "ops")+string(filepath.Separator)) {
		t.Errorf("details.quarantined_op = %q; want a file under .act/.failed-ops/%s/ops/", q, stamp)
	}
	if !strings.HasSuffix(q, opSuffix) {
		t.Errorf("details.quarantined_op = %q; want a *%s op", q, opSuffix)
	}
	if _, err := os.Stat(q); err != nil {
		t.Errorf("quarantined op %q not on disk: %v", q, err)
	}
	return stamp
}

// TestDocClaim_StaleLock_ClaimStageFailureStaysUnclaimed: a claim that fails
// on a stale index.lock reports stale_git_lock, reads back as not claimed,
// keeps its op under .act/.failed-ops/, and the README runbook recovers it.
func TestDocClaim_StaleLock_ClaimStageFailureStaysUnclaimed(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "claim stage probe")

	lockPath := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant index.lock: %v", err)
	}
	out, _, code := runActIn(t, dir, "update", "--claim", id, "--json")
	if code == 0 {
		t.Fatalf("claim under stale lock: want non-zero exit, got 0; out=%s", out)
	}
	e := claimqParse(t, out)
	if e.Error != "stale_git_lock" {
		t.Errorf("error = %q; want stale_git_lock; out=%s", e.Error, out)
	}
	if got := showStatus(t, dir, id); got != "open" {
		t.Fatalf("claim exited %d but act show reports status %q; want open", code, got)
	}
	if ops := claimqClaimOps(t, dir, id); len(ops) != 0 {
		t.Errorf("claim op(s) left in .act/ops after the failed stage: %v", ops)
	}
	stamp := claimqOnlyStamp(t, dir, e, "-claim.json")
	if remedy, _ := e.Details["remedy"].(string); !strings.Contains(remedy, "cp -R .act/.failed-ops/"+stamp+"/ops/. .act/ops/") {
		t.Errorf("remedy does not copy the op back from .act/.failed-ops/%s: %q", stamp, remedy)
	}

	runbookReplayRecovery(t, dir, stamp)
	if got := showStatus(t, dir, id); got != "in_progress" {
		t.Errorf("after README recovery status = %q; want in_progress", got)
	}
}

// TestDocClaim_FailedCommitClaimStaysUnclaimed: a claim whose `git commit`
// fails (after staging succeeded) exits non-zero, reads back as not claimed,
// and preserves its op under .act/.failed-ops/. A retry once commits work
// wins the claim — the quarantined op does not race it.
func TestDocClaim_FailedCommitClaimStaysUnclaimed(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "claim commit probe")

	breakCommits(t, dir)
	out, _, code := runActIn(t, dir, "update", "--claim", id, "--json")
	if code == 0 {
		t.Fatalf("claim with commits broken: want non-zero exit, got 0; out=%s", out)
	}
	e := claimqParse(t, out)
	if e.Error != "claim_failed" {
		t.Errorf("error = %q; want claim_failed; out=%s", e.Error, out)
	}
	if got := showStatus(t, dir, id); got != "open" {
		t.Fatalf("claim exited %d but act show reports status %q; want open", code, got)
	}
	if ops := claimqClaimOps(t, dir, id); len(ops) != 0 {
		t.Errorf("claim op(s) left in .act/ops after the failed commit: %v", ops)
	}
	claimqOnlyStamp(t, dir, e, "-claim.json")
	if st, _ := exec.Command("git", "-C", filepath.Join(dir, ".act"), "diff", "--cached", "--name-only").Output(); strings.Contains(string(st), "claim") {
		t.Errorf("claim op still staged after the failed commit: %s", st)
	}

	unbreakCommits(t, dir)
	out, stderr, code := runActIn(t, dir, "update", "--claim", id, "--json")
	if code != 0 || !strings.Contains(out, `"claimed":true`) {
		t.Fatalf("retry claim: exit %d; out=%s stderr=%s", code, out, stderr)
	}
	if got := showStatus(t, dir, id); got != "in_progress" {
		t.Errorf("after retry status = %q; want in_progress", got)
	}
}

// TestDocClaim_StaleLock_CloseHeadLockCommitFailure: close's COMMIT step
// failing on a stale HEAD.lock reports stale_git_lock (not commit_failed)
// with the lock file and the quarantine path, and the issue stays open.
func TestDocClaim_StaleLock_CloseHeadLockCommitFailure(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "close head lock probe")

	lockPath := filepath.Join(dir, ".act", ".git", "HEAD.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant HEAD.lock: %v", err)
	}
	out, _, code := runActIn(t, dir, "close", id, "--json")
	if code == 0 {
		t.Fatalf("close under stale HEAD.lock: want non-zero exit, got 0; out=%s", out)
	}
	e := claimqParse(t, out)
	if e.Error != "stale_git_lock" {
		t.Errorf("error = %q; want stale_git_lock; out=%s", e.Error, out)
	}
	if lf, _ := e.Details["lock_file"].(string); lf != ".act/.git/HEAD.lock" {
		t.Errorf("details.lock_file = %q; want .act/.git/HEAD.lock", lf)
	}
	claimqOnlyStamp(t, dir, e, "-close.json")
	if got := showStatus(t, dir, id); got != "open" {
		t.Errorf("close exited %d but act show reports status %q; want open", code, got)
	}
}
