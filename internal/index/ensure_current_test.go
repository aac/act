package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// seedCreateOp writes one create op into an ops tree, the shape fold walks.
func seedCreateOp(t *testing.T, opsRoot, id, title string, wallMs int64) string {
	t.Helper()
	pl := map[string]any{
		"title":    title,
		"type":     "task",
		"priority": 1,
		"nonce":    "00000000000000000000000000000000",
	}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       id,
		Payload:       body,
		HLC:           hlc.HLC{Wall: wallMs, Logical: 0, NodeID: "0123abcd"},
		NodeID:        "0123abcd",
	}
	raw, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	dir := filepath.Join(opsRoot, id, "2026-04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, id+"-create.json")
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func openIndexAt(t *testing.T, dir string) *Index {
	t.Helper()
	idx, err := Open(filepath.Join(dir, "index.db"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	return idx
}

// TestEnsureCurrent_SkipsSecondBuildOnUnchangedOps is the mechanism behind
// the read-path fix: the first call folds, the second does not.
func TestEnsureCurrent_SkipsSecondBuildOnUnchangedOps(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)

	idx := openIndexAt(t, dir)

	rebuilt, err := idx.EnsureCurrent(ops)
	if err != nil {
		t.Fatalf("first EnsureCurrent: %v", err)
	}
	if !rebuilt {
		t.Fatal("first EnsureCurrent reported no rebuild on a cold index")
	}

	rebuilt, err = idx.EnsureCurrent(ops)
	if err != nil {
		t.Fatalf("second EnsureCurrent: %v", err)
	}
	if rebuilt {
		t.Fatal("second EnsureCurrent rebuilt an index whose op tree had not changed")
	}
}

// TestEnsureCurrent_RebuildsWhenOpsChange walks the three ways the op log
// moves under a live index.
func TestEnsureCurrent_RebuildsWhenOpsChange(t *testing.T) {
	t.Run("op appended", func(t *testing.T) {
		dir := t.TempDir()
		ops := filepath.Join(dir, "ops")
		seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
		idx := openIndexAt(t, dir)
		if _, err := idx.EnsureCurrent(ops); err != nil {
			t.Fatalf("prime: %v", err)
		}

		seedCreateOp(t, ops, "act-bbbb", "bravo", 1700000010000)
		rebuilt, err := idx.EnsureCurrent(ops)
		if err != nil {
			t.Fatalf("EnsureCurrent: %v", err)
		}
		if !rebuilt {
			t.Fatal("no rebuild after an op was appended")
		}
		if n := countIssues(t, idx); n != 2 {
			t.Fatalf("issues = %d, want 2", n)
		}
	})

	t.Run("op removed", func(t *testing.T) {
		dir := t.TempDir()
		ops := filepath.Join(dir, "ops")
		seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
		gone := seedCreateOp(t, ops, "act-bbbb", "bravo", 1700000010000)
		idx := openIndexAt(t, dir)
		if _, err := idx.EnsureCurrent(ops); err != nil {
			t.Fatalf("prime: %v", err)
		}

		if err := os.Remove(gone); err != nil {
			t.Fatalf("remove: %v", err)
		}
		rebuilt, err := idx.EnsureCurrent(ops)
		if err != nil {
			t.Fatalf("EnsureCurrent: %v", err)
		}
		if !rebuilt {
			t.Fatal("no rebuild after an op was removed")
		}
		if n := countIssues(t, idx); n != 1 {
			t.Fatalf("issues = %d, want 1", n)
		}
	})

	t.Run("build key cleared", func(t *testing.T) {
		// An index carrying no build key — written by a build that predates
		// the key, or one whose bookkeeping was cleared — must rebuild
		// rather than assume its rows are current.
		dir := t.TempDir()
		ops := filepath.Join(dir, "ops")
		seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
		idx := openIndexAt(t, dir)
		if _, err := idx.EnsureCurrent(ops); err != nil {
			t.Fatalf("prime: %v", err)
		}
		if _, err := idx.DB().Exec(`DELETE FROM index_state`); err != nil {
			t.Fatalf("clear index_state: %v", err)
		}
		rebuilt, err := idx.EnsureCurrent(ops)
		if err != nil {
			t.Fatalf("EnsureCurrent: %v", err)
		}
		if !rebuilt {
			t.Fatal("no rebuild on an index with no build key")
		}
	})
}

// TestEnsureCurrent_StaleBuildKeyFromAnotherBinaryRebuilds pins the version
// half of the build key: rows written by a binary whose fold could render
// differently are not reused.
func TestEnsureCurrent_StaleBuildKeyFromAnotherBinaryRebuilds(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	idx := openIndexAt(t, dir)
	if _, err := idx.EnsureCurrent(ops); err != nil {
		t.Fatalf("prime: %v", err)
	}

	// Rewrite the stored key as some other build's, leaving the ops tree
	// alone. The signature still matches; the key as a whole must not.
	if _, err := idx.DB().Exec(
		`UPDATE index_state SET value = 'some-other-build|99|' || value WHERE key = ?`,
		opsBuildKeyRow,
	); err != nil {
		t.Fatalf("rewrite build key: %v", err)
	}
	rebuilt, err := idx.EnsureCurrent(ops)
	if err != nil {
		t.Fatalf("EnsureCurrent: %v", err)
	}
	if !rebuilt {
		t.Fatal("no rebuild on an index built by a different binary")
	}
}

// TestRebuild_FailedRebuildLeavesNoBuildKey: the key and the rows it
// describes have to commit or roll back together, or a failed rebuild leaves
// a key vouching for rows that were never written.
func TestRebuild_RecordsKeyOnlyOnCommit(t *testing.T) {
	dir := t.TempDir()
	ops := filepath.Join(dir, "ops")
	seedCreateOp(t, ops, "act-aaaa", "alpha", 1700000000000)
	idx := openIndexAt(t, dir)
	if err := idx.Rebuild(ops); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if got := idx.readBuildKey(); got == "" {
		t.Fatal("a successful rebuild recorded no build key")
	}

	// A corrupt op aborts the fold; the prior key must not survive into a
	// state where it describes rows the rebuild never wrote. Rebuild's
	// transaction rolls back, so the index keeps the rows AND the key it
	// already had — the pair stays consistent.
	before := idx.readBuildKey()
	bad := filepath.Join(ops, "act-cccc", "2026-04")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, "broken.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := idx.Rebuild(ops); err == nil {
		t.Fatal("rebuild over a corrupt op file returned nil error")
	}
	if got := idx.readBuildKey(); got != before {
		t.Fatalf("failed rebuild changed the build key: %q -> %q", before, got)
	}
	if n := countIssues(t, idx); n != 1 {
		t.Fatalf("failed rebuild changed the rows: issues = %d, want 1", n)
	}
	// And the next read must not trust that key against the changed tree.
	rebuilt, err := idx.EnsureCurrent(ops)
	if err == nil && !rebuilt {
		t.Fatal("EnsureCurrent skipped the rebuild after the op tree changed under a failed rebuild")
	}
}

func countIssues(t *testing.T, idx *Index) int {
	t.Helper()
	var n int
	if err := idx.DB().QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n); err != nil {
		t.Fatalf("count issues: %v", err)
	}
	return n
}
