package fold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aac/act/internal/op"
)

func TestComputeTreeHash_StableAcrossTimestamps(t *testing.T) {
	dir := t.TempDir()
	rootA := filepath.Join(dir, "a")
	rootB := filepath.Join(dir, "b")
	for _, r := range []string{rootA, rootB} {
		if err := os.MkdirAll(filepath.Join(r, "act-0001", "2026-04"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(r, "act-0001", "2026-04", "x.json"), []byte("hello"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	// Bump timestamps on B so the on-disk mtimes differ.
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(filepath.Join(rootB, "act-0001", "2026-04", "x.json"), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	hashA, err := ComputeTreeHash(rootA)
	if err != nil {
		t.Fatalf("hash A: %v", err)
	}
	hashB, err := ComputeTreeHash(rootB)
	if err != nil {
		t.Fatalf("hash B: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("ComputeTreeHash: different hashes for identical content: %s vs %s", hashA, hashB)
	}
	if hashA == "" {
		t.Fatalf("ComputeTreeHash: empty hash")
	}
}

func TestComputeTreeHash_ChangesOnFileEdit(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	if err := os.MkdirAll(filepath.Join(root, "act-0001", "2026-04"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(root, "act-0001", "2026-04", "x.json")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h1, err := ComputeTreeHash(root)
	if err != nil {
		t.Fatalf("h1: %v", err)
	}
	if err := os.WriteFile(p, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write2: %v", err)
	}
	h2, err := ComputeTreeHash(root)
	if err != nil {
		t.Fatalf("h2: %v", err)
	}
	if h1 == h2 {
		t.Fatalf("ComputeTreeHash: hash unchanged after edit (%s)", h1)
	}
}

func TestComputeTreeHash_MissingDirReturnsStableHash(t *testing.T) {
	dir := t.TempDir()
	h, err := ComputeTreeHash(filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h == "" {
		t.Fatalf("expected stable empty hash")
	}
	h2, err := ComputeTreeHash(filepath.Join(dir, "also-missing"))
	if err != nil {
		t.Fatalf("hash2: %v", err)
	}
	if h != h2 {
		t.Fatalf("empty-tree hash not stable: %s vs %s", h, h2)
	}
}

func TestReadCheckpoint_MissingReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	cp, err := ReadCheckpoint(filepath.Join(dir, "fold-checkpoint.json"))
	if err != nil {
		t.Fatalf("ReadCheckpoint: %v", err)
	}
	if cp != nil {
		t.Fatalf("expected nil checkpoint, got %+v", cp)
	}
}

func TestReadCheckpoint_SchemaVersionMismatchReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fold-checkpoint.json")
	body, _ := json.Marshal(map[string]any{
		"schema_version": 99,
		"tree_hash":      "abc",
		"issues":         map[string]any{},
	})
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cp, err := ReadCheckpoint(path)
	if err != nil {
		t.Fatalf("ReadCheckpoint: %v", err)
	}
	if cp != nil {
		t.Fatalf("expected nil for schema mismatch, got %+v", cp)
	}
}

func TestWriteThenReadCheckpoint_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fold-checkpoint.json")
	in := &Checkpoint{
		TreeHash: "tree-abc",
		Issues: map[string]IssueCheckpoint{
			"act-0001": {SubtreeHash: "sub-1", FoldHash: "fold-1"},
			"act-0002": {SubtreeHash: "sub-2", FoldHash: "fold-2"},
		},
	}
	if err := WriteCheckpoint(path, in); err != nil {
		t.Fatalf("WriteCheckpoint: %v", err)
	}
	out, err := ReadCheckpoint(path)
	if err != nil {
		t.Fatalf("ReadCheckpoint: %v", err)
	}
	if out == nil {
		t.Fatalf("ReadCheckpoint returned nil")
	}
	if out.TreeHash != in.TreeHash {
		t.Fatalf("TreeHash: got %q want %q", out.TreeHash, in.TreeHash)
	}
	if len(out.Issues) != len(in.Issues) {
		t.Fatalf("Issues len: got %d want %d", len(out.Issues), len(in.Issues))
	}
	for id, want := range in.Issues {
		got, ok := out.Issues[id]
		if !ok {
			t.Fatalf("missing issue %s", id)
		}
		if got != want {
			t.Fatalf("issue %s: got %+v want %+v", id, got, want)
		}
	}

	// Sanity: no .tmp file left behind.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp checkpoint file should be removed after rename, stat err = %v", err)
	}
}

func TestWriteCheckpoint_AtomicNoPartialOnRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fold-checkpoint.json")
	if err := WriteCheckpoint(path, &Checkpoint{TreeHash: "v1", Issues: map[string]IssueCheckpoint{}}); err != nil {
		t.Fatalf("write1: %v", err)
	}
	if err := WriteCheckpoint(path, &Checkpoint{TreeHash: "v2", Issues: map[string]IssueCheckpoint{}}); err != nil {
		t.Fatalf("write2: %v", err)
	}
	cp, err := ReadCheckpoint(path)
	if err != nil {
		t.Fatalf("ReadCheckpoint: %v", err)
	}
	if cp.TreeHash != "v2" {
		t.Fatalf("TreeHash after rewrite: got %q want v2", cp.TreeHash)
	}
}

// countingDispatch wraps StubDispatch with an atomic counter that increments
// every time an apply func runs. It lets tests assert that a "warm" fold
// short-circuits before re-applying any op.
func countingDispatch(counter *int64) func(string) ApplyFunc {
	return func(opType string) ApplyFunc {
		inner := StubDispatch(opType)
		if inner == nil {
			return nil
		}
		return func(state *IssueState, env op.Envelope, payload []byte) error {
			atomic.AddInt64(counter, 1)
			return inner(state, env, payload)
		}
	}
}

func TestFoldWithCheckpoint_ColdThenWarm(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	cp := filepath.Join(dir, "fold-checkpoint.json")

	e1 := env("act-0001", "create", 1000, 0, "0123abcd", `{"title":"hello"}`)
	e2 := env("act-0002", "create", 1100, 0, "0123abcd", `{"title":"world"}`)
	writeOp(t, root, e1)
	writeOp(t, root, e2)

	// Cold: no checkpoint, full fold.
	var counter int64
	res, gotCP, err := FoldWithCheckpoint(root, cp, countingDispatch(&counter))
	if err != nil {
		t.Fatalf("cold FoldWithCheckpoint: %v", err)
	}
	if res == nil {
		t.Fatalf("cold call: expected FoldResult, got nil")
	}
	if gotCP == nil {
		t.Fatalf("cold call: expected Checkpoint, got nil")
	}
	if counter != 2 {
		t.Fatalf("cold call: expected 2 applies, got %d", counter)
	}
	if _, err := os.Stat(cp); err != nil {
		t.Fatalf("checkpoint file not written: %v", err)
	}
	if len(gotCP.Issues) != 2 {
		t.Fatalf("checkpoint issues: got %d want 2", len(gotCP.Issues))
	}

	// Warm: same content; tree-hash hit, no re-fold.
	atomic.StoreInt64(&counter, 0)
	res2, cp2, err := FoldWithCheckpoint(root, cp, countingDispatch(&counter))
	if err != nil {
		t.Fatalf("warm FoldWithCheckpoint: %v", err)
	}
	if counter != 0 {
		t.Fatalf("warm call: expected 0 applies, got %d", counter)
	}
	if cp2 == nil || cp2.TreeHash != gotCP.TreeHash {
		t.Fatalf("warm call: tree hash mismatch")
	}
	// v0.1 simplification: warm hit returns nil FoldResult; the
	// in-memory cache is the caller's job. Document via assertion.
	if res2 != nil {
		t.Fatalf("warm call: v0.1 expected nil FoldResult, got %+v", res2)
	}
}

func TestFoldWithCheckpoint_NewOpInvalidatesCache(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	cp := filepath.Join(dir, "fold-checkpoint.json")

	e1 := env("act-0001", "create", 1000, 0, "0123abcd", `{"title":"hello"}`)
	writeOp(t, root, e1)

	var counter int64
	if _, _, err := FoldWithCheckpoint(root, cp, countingDispatch(&counter)); err != nil {
		t.Fatalf("first FoldWithCheckpoint: %v", err)
	}
	if counter != 1 {
		t.Fatalf("first call applies: got %d want 1", counter)
	}
	cpFirst, err := ReadCheckpoint(cp)
	if err != nil {
		t.Fatalf("read cp: %v", err)
	}

	// Add a new op for a new issue. Tree hash must change.
	e2 := env("act-0002", "create", 1100, 0, "0123abcd", `{"title":"world"}`)
	writeOp(t, root, e2)

	atomic.StoreInt64(&counter, 0)
	res, cpSecond, err := FoldWithCheckpoint(root, cp, countingDispatch(&counter))
	if err != nil {
		t.Fatalf("second FoldWithCheckpoint: %v", err)
	}
	if counter != 2 {
		t.Fatalf("second call applies: got %d want 2", counter)
	}
	if res == nil {
		t.Fatalf("second call: expected non-nil FoldResult")
	}
	if cpSecond == nil {
		t.Fatalf("second call: expected non-nil Checkpoint")
	}
	if cpSecond.TreeHash == cpFirst.TreeHash {
		t.Fatalf("tree hash unchanged after new op")
	}
	if _, ok := cpSecond.Issues["act-0002"]; !ok {
		t.Fatalf("checkpoint missing new issue act-0002")
	}
}

func TestFoldWithCheckpoint_DropsRemovedIssues(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	cp := filepath.Join(dir, "fold-checkpoint.json")

	e1 := env("act-0001", "create", 1000, 0, "0123abcd", `{"title":"hello"}`)
	e2 := env("act-0002", "create", 1100, 0, "0123abcd", `{"title":"world"}`)
	writeOp(t, root, e1)
	writeOp(t, root, e2)

	if _, _, err := FoldWithCheckpoint(root, cp, StubDispatch); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Remove act-0002 entirely.
	if err := os.RemoveAll(filepath.Join(root, "act-0002")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, cp2, err := FoldWithCheckpoint(root, cp, StubDispatch)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if _, ok := cp2.Issues["act-0002"]; ok {
		t.Fatalf("checkpoint still references removed issue act-0002")
	}
	if _, ok := cp2.Issues["act-0001"]; !ok {
		t.Fatalf("checkpoint missing surviving issue act-0001")
	}
}

func TestComputeIssueSubtreeHash_OnlyReflectsThatIssue(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	if err := os.MkdirAll(filepath.Join(root, "act-0001", "2026-04"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "act-0002", "2026-04"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "act-0001", "2026-04", "x.json"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "act-0002", "2026-04", "y.json"), []byte("b"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h1a, err := ComputeIssueSubtreeHash(root, "act-0001")
	if err != nil {
		t.Fatalf("h1a: %v", err)
	}
	// Mutate act-0002 only.
	if err := os.WriteFile(filepath.Join(root, "act-0002", "2026-04", "y.json"), []byte("b-changed"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h1b, err := ComputeIssueSubtreeHash(root, "act-0001")
	if err != nil {
		t.Fatalf("h1b: %v", err)
	}
	if h1a != h1b {
		t.Fatalf("act-0001 subtree hash changed when act-0002 was edited")
	}
	h2, err := ComputeIssueSubtreeHash(root, "act-0002")
	if err != nil {
		t.Fatalf("h2: %v", err)
	}
	if h2 == h1a {
		t.Fatalf("act-0002 hash equals act-0001 hash")
	}
}
