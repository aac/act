package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// These cover the incremental read path (act-50d2e2): after a write, the
// index refolds only the issues whose ops subtree moved. The equivalence
// property is what all of them turn on — an incremental update must leave the
// index in exactly the state a full Rebuild would have produced. Anything
// less makes `act doctor`'s index-vs-fold divergence check the thing that
// finds the bug, in a store, after the fact.

// issueTitle returns the indexed title for id, or "" when it has no row.
func issueTitle(t *testing.T, idx *Index, id string) string {
	t.Helper()
	var title string
	err := idx.DB().QueryRow(`SELECT title FROM issues WHERE id = ?`, id).Scan(&title)
	if err != nil {
		return ""
	}
	return title
}

// tamper writes a title no op supports, so a row that gets refolded is
// distinguishable from one that is left alone.
func tamper(t *testing.T, idx *Index, id, title string) {
	t.Helper()
	res, err := idx.DB().Exec(`UPDATE issues SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("tamper affected %d rows, want 1", n)
	}
}

// TestEnsureCurrent_RefoldsOnlyTheChangedIssue is the mechanism act-50d2e2
// adds: writing an op for one issue must not rewrite every other issue's
// rows. The untouched, tampered row surviving is the discriminating evidence
// that the fold was scoped rather than whole-tree.
func TestEnsureCurrent_RefoldsOnlyTheChangedIssue(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	seedCreateOp(t, ops, "act-bbbb", "bravo", 1700000010000)
	idx := openIndexAt(t, dir)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("prime: %v", err)
	}

	tamper(t, idx, "act-aaaa", "SENTINEL-NOT-IN-OPS")
	seedCreateOp(t, ops, "act-cccc", "charlie", 1700000020000)

	rebuilt, err := idx.EnsureCurrent(ops)
	if err != nil {
		t.Fatalf("EnsureCurrent: %v", err)
	}
	if !rebuilt {
		t.Fatal("no rebuild after an op was appended")
	}
	if got := issueTitle(t, idx, "act-cccc"); got != "charlie" {
		t.Fatalf("new issue title = %q, want %q", got, "charlie")
	}
	if got := issueTitle(t, idx, "act-aaaa"); got != "SENTINEL-NOT-IN-OPS" {
		t.Fatalf("untouched issue title = %q, want the tampered value — the append refolded the whole log", got)
	}
}

// TestEnsureCurrent_DropsIssueWhoseSubtreeDisappears: an issue whose whole
// ops directory goes away must lose its rows. An incremental path that only
// ever upserts what it refolds would serve it forever — the act-fec192 shape
// at directory granularity.
func TestEnsureCurrent_DropsIssueWhoseSubtreeDisappears(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	seedCreateOp(t, ops, "act-bbbb", "bravo", 1700000010000)
	idx := openIndexAt(t, dir)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("prime: %v", err)
	}

	if err := os.RemoveAll(filepath.Join(ops, "act-bbbb")); err != nil {
		t.Fatalf("remove subtree: %v", err)
	}
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("EnsureCurrent: %v", err)
	}
	if got := issueTitle(t, idx, "act-bbbb"); got != "" {
		t.Fatalf("act-bbbb still indexed (title %q) after its ops subtree was removed", got)
	}
	if n := countIssues(t, idx); n != 1 {
		t.Fatalf("issues = %d, want 1", n)
	}
	// Its per-issue signature must go too, or the next read would see a
	// deletion it has already performed.
	sigs, err := idx.readIssueSigs()
	if err != nil {
		t.Fatalf("read signatures: %v", err)
	}
	if _, ok := sigs["act-bbbb"]; ok {
		t.Fatal("signature for act-bbbb survived the deletion of its subtree")
	}
}

// TestEnsureCurrent_EmptiedSubtreeDropsRows is the same deletion one step
// short: the directory survives but its last op is gone. A full Rebuild
// writes no row for such an issue, so neither may the incremental path.
func TestEnsureCurrent_EmptiedSubtreeDropsRows(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	gone := seedCreateOp(t, ops, "act-bbbb", "bravo", 1700000010000)
	idx := openIndexAt(t, dir)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("prime: %v", err)
	}

	if err := os.Remove(gone); err != nil {
		t.Fatalf("remove op: %v", err)
	}
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("EnsureCurrent: %v", err)
	}
	if got := issueTitle(t, idx, "act-bbbb"); got != "" {
		t.Fatalf("act-bbbb still indexed (title %q) after its only op was removed", got)
	}
	if n := countIssues(t, idx); n != 1 {
		t.Fatalf("issues = %d, want 1", n)
	}
}

// TestEnsureCurrent_IncrementalMatchesFullRebuild is the equivalence check
// the whole design rests on: run a sequence of writes through the
// incremental path, then a full Rebuild over the same tree, and require the
// two to agree row for row. Any divergence here is what `act doctor` would
// otherwise report against a live store.
func TestEnsureCurrent_IncrementalMatchesFullRebuild(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	seedCreateOp(t, ops, "act-bbbb", "bravo", 1700000010000)
	idx := openIndexAt(t, dir)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// A write per read, the way a drain interleaves them.
	seedCreateOp(t, ops, "act-cccc", "charlie", 1700000020000)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("after create: %v", err)
	}
	seedCloseOpAt(t, ops, "act-aaaa", 1700000030000)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("after close: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(ops, "act-bbbb")); err != nil {
		t.Fatalf("remove subtree: %v", err)
	}
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("after delete: %v", err)
	}

	incremental := snapshotRows(t, idx)

	if err := idx.Rebuild(ops); err != nil {
		t.Fatalf("full rebuild: %v", err)
	}
	full := snapshotRows(t, idx)

	if len(incremental) != len(full) {
		t.Fatalf("incremental produced %d rows, full rebuild %d:\n%v\n%v", len(incremental), len(full), incremental, full)
	}
	for i := range full {
		if incremental[i] != full[i] {
			t.Fatalf("row %d differs:\n incremental %q\n full        %q", i, incremental[i], full[i])
		}
	}
}

// TestEnsureCurrent_ForeignOpFallsBackToFullRebuild: an op whose envelope
// names an issue other than its own directory breaks the per-issue partition
// the incremental path assumes. Nothing act writes produces it, so the
// requirement is not that it be handled cleverly — it is that the index still
// end up matching a full fold rather than quietly omitting the op.
func TestEnsureCurrent_ForeignOpFallsBackToFullRebuild(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	idx := openIndexAt(t, dir)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Land act-bbbb's create op inside act-aaaa's directory.
	body := createOpBody(t, "act-bbbb", "bravo", 1700000010000)
	foreignDir := filepath.Join(ops, "act-aaaa", "2026-04")
	if err := os.WriteFile(filepath.Join(foreignDir, "foreign-create.json"), body, 0o644); err != nil {
		t.Fatalf("write foreign op: %v", err)
	}

	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("EnsureCurrent: %v", err)
	}
	got := snapshotRows(t, idx)
	if err := idx.Rebuild(ops); err != nil {
		t.Fatalf("full rebuild: %v", err)
	}
	want := snapshotRows(t, idx)
	if len(got) != len(want) {
		t.Fatalf("after a foreign op the index held %d rows, a full fold %d:\n%v\n%v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d differs after a foreign op:\n got  %q\n want %q", i, got[i], want[i])
		}
	}
}

// TestFoldIssueStrict_RejectsForeignOp pins the fold-side half of that
// contract directly.
func TestFoldIssueStrict_RejectsForeignOp(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	body := createOpBody(t, "act-bbbb", "bravo", 1700000010000)
	if err := os.WriteFile(filepath.Join(ops, "act-aaaa", "2026-04", "foreign.json"), body, 0o644); err != nil {
		t.Fatalf("write foreign op: %v", err)
	}
	if _, _, err := fold.FoldIssueStrict(ops, "act-aaaa", fold.ApplyDispatch); err == nil {
		t.Fatal("FoldIssueStrict accepted a subtree holding another issue's op")
	}
}

// snapshotRows renders every indexed issue as a comparable string, ordered so
// two builds of the same tree produce identical slices.
func snapshotRows(t *testing.T, idx *Index) []string {
	t.Helper()
	rows, err := idx.DB().Query(`
		SELECT id, title, description, status, priority, type, parent, assignee,
		       created_at, claimed_at, closed_at, closed_reason, tombstoned
		  FROM issues ORDER BY id`)
	if err != nil {
		t.Fatalf("snapshot query: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		cols := make([]any, 13)
		vals := make([]any, 13)
		for i := range cols {
			cols[i] = &vals[i]
		}
		if err := rows.Scan(cols...); err != nil {
			t.Fatalf("snapshot scan: %v", err)
		}
		b, _ := json.Marshal(vals)
		out = append(out, string(b))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("snapshot iter: %v", err)
	}
	return out
}

// createOpBody marshals a create envelope for id without writing it, so a
// test can place it somewhere op.ShardDir never would.
func createOpBody(t *testing.T, id, title string, wallMs int64) []byte {
	t.Helper()
	pl, err := json.Marshal(map[string]any{
		"title": title, "type": "task", "priority": 1,
		"nonce": "00000000000000000000000000000000",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       id,
		Payload:       pl,
		HLC:           hlc.HLC{Wall: wallMs, Logical: 0, NodeID: "0123abcd"},
		NodeID:        "0123abcd",
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return raw
}

// seedCloseOpAt lands a close op for id, the way `act close` does.
func seedCloseOpAt(t *testing.T, opsRoot, id string, wallMs int64) string {
	t.Helper()
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       id,
		Payload:       json.RawMessage(`{"reason":"done"}`),
		HLC:           hlc.HLC{Wall: wallMs, Logical: 0, NodeID: "0123abcd"},
		NodeID:        "0123abcd",
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal close envelope: %v", err)
	}
	dir := filepath.Join(opsRoot, id, "2026-04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, id+"-close.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}
