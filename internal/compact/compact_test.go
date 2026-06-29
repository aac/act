package compact

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// fakeCommitter is a no-op gitOpsCommitter used in tests; it records every
// Commit message it receives so assertions can inspect the result.
type fakeCommitter struct {
	mu    sync.Mutex
	msgs  []string
	calls int32
}

func (f *fakeCommitter) Commit(msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
	atomic.AddInt32(&f.calls, 1)
	return nil
}

// makeOpFile mirrors the helper in fold/ — duplicated here so the compact
// package is self-contained at test time.
func makeOpFile(t *testing.T, dir, issueID, opType string, h hlc.HLC, payload any) string {
	t.Helper()
	pb, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("makeOpFile: marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       pb,
		HLC:           h,
		NodeID:        h.NodeID,
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("makeOpFile: marshal env: %v", err)
	}
	shard := op.ShardDir(dir, issueID, h.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("makeOpFile: mkdir %s: %v", shard, err)
	}
	path := filepath.Join(shard, op.Filename(env))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("makeOpFile: write %s: %v", path, err)
	}
	return path
}

// seedIssue writes a create op + n update_field ops for issueID under
// rootOps. Returns the list of every file path written, in write order.
func seedIssue(t *testing.T, rootOps, issueID string, n int, baseWall int64, node string) []string {
	t.Helper()
	var paths []string
	create := op.CreatePayload{
		Title:    "seeded",
		Type:     "task",
		Nonce:    "00000000000000000000000000000001",
		Priority: ptrInt(1),
	}
	paths = append(paths, makeOpFile(t, rootOps, issueID, "create",
		hlc.HLC{Wall: baseWall, Logical: 0, NodeID: node}, create))

	for i := 0; i < n; i++ {
		val := json.RawMessage(fmt.Sprintf(`"t-%d"`, i))
		paths = append(paths, makeOpFile(t, rootOps, issueID, "update_field",
			hlc.HLC{Wall: baseWall + int64(i+1), Logical: uint32(i), NodeID: node},
			op.UpdateFieldPayload{Field: "title", Value: val}))
	}
	return paths
}

func ptrInt(v int) *int { return &v }

// TestCompact_TriggersOnCount verifies that an issue with > 50 ops triggers
// compaction and produces a snapshot at the expected location with the
// folded title in state.
func TestCompact_TriggersOnCount(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	issue := "act-aaaa"
	seedIssue(t, rootOps, issue, 60, 1700000000000, "11111111")

	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{}, fc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CompactedIssues != 1 {
		t.Fatalf("CompactedIssues = %d, want 1", res.CompactedIssues)
	}
	snapPath := filepath.Join(tmp, ".act", "snapshots", issue+".json")
	body, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(body, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.ID != issue {
		t.Errorf("snap.ID = %q, want %q", snap.ID, issue)
	}
	if got, _ := snap.State["title"].(string); got == "" {
		t.Errorf("snap.State.title is empty; want some folded title")
	}
	// One commit, message shape act-compact: 1 issues.
	if len(fc.msgs) != 1 {
		t.Fatalf("commit count = %d, want 1", len(fc.msgs))
	}
	if !strings.HasPrefix(fc.msgs[0], "act-compact:") {
		t.Errorf("commit message %q: missing act-compact prefix", fc.msgs[0])
	}
	// A compact op envelope should have been written under .act/ops/<issue>/.
	found := false
	_ = filepath.Walk(filepath.Join(rootOps, issue), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasSuffix(info.Name(), "-compact.json") {
			found = true
		}
		return nil
	})
	if !found {
		t.Errorf("expected a -compact.json envelope under %s", filepath.Join(rootOps, issue))
	}
}

// TestCompact_BelowThreshold confirms that an issue with <= 50 ops is left
// alone (no snapshot, no commit, no compact op).
func TestCompact_BelowThreshold(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	issue := "act-bbbb"
	seedIssue(t, rootOps, issue, 40, 1700000000000, "22222222")

	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{}, fc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CompactedIssues != 0 {
		t.Errorf("CompactedIssues = %d, want 0", res.CompactedIssues)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".act", "snapshots", issue+".json")); !os.IsNotExist(err) {
		t.Errorf("snapshot written for under-threshold issue")
	}
	if len(fc.msgs) != 0 {
		t.Errorf("commit fired with no eligible issues: %v", fc.msgs)
	}
}

// TestCompact_DryRun ensures DryRun does not write any file or call Commit.
func TestCompact_DryRun(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	issue := "act-cccc"
	seedIssue(t, rootOps, issue, 60, 1700000000000, "33333333")

	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{DryRun: true}, fc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CompactedIssues != 1 {
		t.Errorf("CompactedIssues = %d, want 1 (Result is populated)", res.CompactedIssues)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".act", "snapshots", issue+".json")); !os.IsNotExist(err) {
		t.Errorf("DryRun wrote a snapshot file")
	}
	if len(fc.msgs) != 0 {
		t.Errorf("DryRun called Commit: %v", fc.msgs)
	}
	// Confirm no compact op was written.
	_ = filepath.Walk(filepath.Join(rootOps, issue), func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasSuffix(info.Name(), "-compact.json") {
			t.Errorf("DryRun wrote a compact op: %s", path)
		}
		return nil
	})
}

// TestCompact_AggressivePruneClosed deletes subsumed op files when an issue
// is closed and closed_at is older than 30 days. A still-open issue keeps
// its op files even when the flag is set.
func TestCompact_AggressivePruneClosed(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	openIssue := "act-dddd"
	closedIssue := "act-eeee"

	// Open issue with 60 ops.
	seedIssue(t, rootOps, openIssue, 60, 1700000000000, "44444444")

	// Closed issue: create + 60 updates + claim + close. Wall in the past
	// so closed_at parses as long-ago.
	pastWall := time.Now().Add(-60 * 24 * time.Hour).UnixMilli()
	create := op.CreatePayload{
		Title:    "closed-issue",
		Type:     "task",
		Nonce:    "00000000000000000000000000000002",
		Priority: ptrInt(1),
	}
	makeOpFile(t, rootOps, closedIssue, "create",
		hlc.HLC{Wall: pastWall, Logical: 0, NodeID: "55555555"}, create)
	for i := 0; i < 60; i++ {
		makeOpFile(t, rootOps, closedIssue, "update_field",
			hlc.HLC{Wall: pastWall + int64(i+1), Logical: uint32(i), NodeID: "55555555"},
			op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(fmt.Sprintf(`"t-%d"`, i))})
	}
	makeOpFile(t, rootOps, closedIssue, "claim",
		hlc.HLC{Wall: pastWall + 100, Logical: 0, NodeID: "55555555"},
		op.ClaimPayload{Assignee: "alice"})
	makeOpFile(t, rootOps, closedIssue, "close",
		hlc.HLC{Wall: pastWall + 200, Logical: 0, NodeID: "55555555"},
		op.ClosePayload{Reason: "done"})

	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{AggressivePrune: true}, fc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CompactedIssues < 2 {
		t.Errorf("CompactedIssues = %d, want >= 2", res.CompactedIssues)
	}
	if res.PrunedOps == 0 {
		t.Errorf("PrunedOps = 0; expected closed-issue ops to be deleted")
	}

	// Open issue: subsumed op files still on disk.
	openWalkCount := 0
	_ = filepath.Walk(filepath.Join(rootOps, openIssue), func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".json") {
			openWalkCount++
		}
		return nil
	})
	if openWalkCount < 60 {
		t.Errorf("open issue lost ops to prune: count=%d", openWalkCount)
	}

	// Closed issue: ops directory should be largely empty (only the new
	// compact op remains, since prune removes the subsumed files).
	closedNonCompact := 0
	_ = filepath.Walk(filepath.Join(rootOps, closedIssue), func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), ".json") &&
			!strings.HasSuffix(info.Name(), "-compact.json") {
			closedNonCompact++
		}
		return nil
	})
	if closedNonCompact != 0 {
		t.Errorf("closed issue still has %d non-compact ops; want 0 after prune", closedNonCompact)
	}
}

// TestCompact_AggressivePruneNotEligible: AggressivePrune set, but issue
// remains open — op files MUST be preserved.
func TestCompact_AggressivePruneNotEligible(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	issue := "act-ffff"
	seedIssue(t, rootOps, issue, 60, 1700000000000, "66666666")

	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{AggressivePrune: true}, fc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.PrunedOps != 0 {
		t.Errorf("PrunedOps = %d; expected 0 for open issue", res.PrunedOps)
	}
}

// TestCompact_LockContention: another process holding the lock makes the
// compactor record `compaction_locked` and exit cleanly.
func TestCompact_LockContention(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, ".act"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lockPath := filepath.Join(tmp, ".act", ".compact.lock")
	release, locked, err := acquireLock(lockPath)
	if err != nil {
		t.Fatalf("acquireLock holder: %v", err)
	}
	if !locked {
		t.Fatalf("acquireLock could not initially lock; holder failed to take the lock")
	}
	defer release()

	// Seed an eligible issue so the compactor would otherwise do work.
	rootOps := filepath.Join(tmp, ".act", "ops")
	seedIssue(t, rootOps, "act-dade", 60, 1700000000000, "77777777")

	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{}, fc)
	if err != nil {
		t.Fatalf("Run under contention returned err: %v", err)
	}
	if res.CompactedIssues != 0 {
		t.Errorf("CompactedIssues = %d under contention; want 0", res.CompactedIssues)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != SkipCompactionLocked {
		t.Errorf("Skipped = %v; want [%q]", res.Skipped, SkipCompactionLocked)
	}
	if len(fc.msgs) != 0 {
		t.Errorf("commit fired under contention: %v", fc.msgs)
	}
}

// TestCompact_CompactOpRecordsTreeHash: the compact op envelope on disk
// carries the same tree-hash as the snapshot file.
func TestCompact_CompactOpRecordsTreeHash(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	issue := "act-dada"
	seedIssue(t, rootOps, issue, 60, 1700000000000, "88888888")

	fc := &fakeCommitter{}
	if _, err := Run(tmp, Options{}, fc); err != nil {
		t.Fatalf("Run: %v", err)
	}
	snapBody, err := os.ReadFile(filepath.Join(tmp, ".act", "snapshots", issue+".json"))
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(snapBody, &snap); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if snap.TreeHash == "" {
		t.Fatal("snap.TreeHash empty")
	}

	// Find compact op and verify its snapshot_tree_hash matches.
	var compactPath string
	_ = filepath.Walk(filepath.Join(rootOps, issue), func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), "-compact.json") {
			compactPath = path
		}
		return nil
	})
	if compactPath == "" {
		t.Fatal("no compact op found")
	}
	body, err := os.ReadFile(compactPath)
	if err != nil {
		t.Fatalf("read compact op: %v", err)
	}
	var compactOp map[string]any
	if err := json.Unmarshal(body, &compactOp); err != nil {
		t.Fatalf("unmarshal compact op: %v", err)
	}
	if got := compactOp["snapshot_tree_hash"]; got != snap.TreeHash {
		t.Errorf("compact op snapshot_tree_hash = %v; want %q", got, snap.TreeHash)
	}
	if got := compactOp["op_type"]; got != "compact" {
		t.Errorf("compact op op_type = %v; want \"compact\"", got)
	}
	if got := compactOp["subsumed_count"]; got == nil {
		t.Error("compact op missing subsumed_count")
	}
}

// TestCompactFilename_NoColon asserts that compact tombstone filenames use
// the NTFS-safe dash-form ISO layout (per act-d5d1ff). Colons in path
// components break `git checkout` on Windows hosts before any Go code runs,
// so the time component of compact filenames must use '-' separators in
// the HH-MM-SS portion. The filename must still parse with op.IsoLayout so
// the canonical contract stays shared with op writers (act-2f3d).
func TestCompactFilename_NoColon(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	issue := "act-d5d1ff"
	seedIssue(t, rootOps, issue, 60, 1700000000000, "d5d1ffd5")

	fc := &fakeCommitter{}
	if _, err := Run(tmp, Options{}, fc); err != nil {
		t.Fatalf("Run: %v", err)
	}

	var compactName string
	_ = filepath.Walk(filepath.Join(rootOps, issue), func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(info.Name(), "-compact.json") {
			compactName = info.Name()
		}
		return nil
	})
	if compactName == "" {
		t.Fatal("no compact tombstone file found")
	}
	if strings.Contains(compactName, ":") {
		t.Fatalf("compact tombstone filename contains ':' (NTFS-unsafe): %q", compactName)
	}

	// The time component must parse with op.IsoLayout (the canonical
	// dash-form layout). The filename shape is `<iso>-<hash>-compact.json`;
	// the leading 24-char prefix is the time component.
	if len(compactName) < len(op.IsoLayout) {
		t.Fatalf("compact filename %q shorter than expected layout", compactName)
	}
	isoPart := compactName[:len(op.IsoLayout)]
	if _, err := time.ParseInLocation(op.IsoLayout, isoPart, time.UTC); err != nil {
		t.Errorf("compact filename time component %q does not parse with op.IsoLayout: %v", isoPart, err)
	}
}

// TestCompact_NoActDir: a tempdir without .act/ should be a no-op.
func TestCompact_NoActDir(t *testing.T) {
	tmp := t.TempDir()
	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{}, fc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CompactedIssues != 0 || res.PrunedOps != 0 {
		t.Errorf("Run on bare tempdir produced non-zero result: %+v", res)
	}
}

// TestCompact_IssueIDFilter limits compaction to a single issue.
func TestCompact_IssueIDFilter(t *testing.T) {
	tmp := t.TempDir()
	rootOps := filepath.Join(tmp, ".act", "ops")
	seedIssue(t, rootOps, "act-cccd", 60, 1700000000000, "99999999")
	seedIssue(t, rootOps, "act-cdcd", 60, 1700001000000, "aaaaaaaa")

	fc := &fakeCommitter{}
	res, err := Run(tmp, Options{IssueID: "act-cccd"}, fc)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.CompactedIssues != 1 {
		t.Errorf("CompactedIssues = %d, want 1", res.CompactedIssues)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".act", "snapshots", "act-cccd.json")); err != nil {
		t.Errorf("issue iiii snapshot missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, ".act", "snapshots", "act-cdcd.json")); !os.IsNotExist(err) {
		t.Errorf("issue jjjj should not have been compacted under IssueID filter")
	}
}
