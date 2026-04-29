package fold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// writeOp serialises env into the on-disk shard layout under rootOps and
// returns the absolute path written. The filename is taken from op.Filename
// (the canonical 8-hex form), which fixes both the wall component and the
// hash prefix so writeOp is deterministic for a given envelope value.
func writeOp(t *testing.T, rootOps string, env op.Envelope) string {
	t.Helper()
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	shard := op.ShardDir(rootOps, env.IssueID, env.HLC.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(shard, op.Filename(env))
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// writeOpAtName writes env's canonical bytes to a chosen filename in the
// shard derived from env.HLC.Wall. Used to force misleading filenames so
// tests can show that fold orders by HLC, not filename.
func writeOpAtName(t *testing.T, rootOps string, env op.Envelope, basename string) string {
	t.Helper()
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	shard := op.ShardDir(rootOps, env.IssueID, env.HLC.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(shard, basename)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func env(issueID, opType string, wall int64, logical uint32, nodeID, payload string) op.Envelope {
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       json.RawMessage(payload),
		HLC:           hlc.HLC{Wall: wall, Logical: logical, NodeID: nodeID},
		NodeID:        nodeID,
	}
}

func TestFold_EmptyRoot(t *testing.T) {
	dir := t.TempDir()
	res, err := Fold(filepath.Join(dir, "ops"), StubDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res == nil {
		t.Fatalf("Fold: nil result")
	}
	if got := len(res.Issues); got != 0 {
		t.Fatalf("Issues: got %d, want 0", got)
	}
	if res.OpsConsumed != 0 {
		t.Fatalf("OpsConsumed: got %d, want 0", res.OpsConsumed)
	}
}

func TestFold_EmptyExistingRoot(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	res, err := Fold(root, StubDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if len(res.Issues) != 0 || res.OpsConsumed != 0 {
		t.Fatalf("non-empty result on empty dir: %+v", res)
	}
}

func TestFold_ChronologicalOrderApplied(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	id := "act-aaaa"

	a := env(id, "create", 1700000000000, 0, "0123abcd", `{"v":1}`)
	b := env(id, "update_field", 1700000001000, 0, "0123abcd", `{"v":2}`)
	c := env(id, "claim", 1700000002000, 0, "0123abcd", `{"v":3}`)
	for _, e := range []op.Envelope{a, b, c} {
		writeOp(t, root, e)
	}

	res, err := Fold(root, StubDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.OpsConsumed != 3 {
		t.Fatalf("OpsConsumed: got %d, want 3", res.OpsConsumed)
	}
	state, ok := res.Issues[id]
	if !ok {
		t.Fatalf("issue %q missing", id)
	}
	if got := state.Fields["__last_op"]; got != "claim" {
		t.Fatalf("__last_op: got %v, want %q", got, "claim")
	}
}

func TestFold_OutOfOrderFilenamesSortedByHLC(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	id := "act-bbbb"

	// Filenames carry far-future timestamps but the HLC walls inside the
	// envelopes are the true order. Fold must respect the envelope.
	first := env(id, "create", 1700000000000, 0, "11111111", `{"n":1}`)
	second := env(id, "update_field", 1700000005000, 0, "11111111", `{"n":2}`)
	last := env(id, "claim", 1700000009000, 0, "11111111", `{"n":3}`)

	// Write with deceptive filenames: "first" gets the latest filename
	// timestamp, "last" gets the earliest.
	writeOpAtName(t, root, first, "2099-01-01T00:00:00.000Z-deadbeef-create.json")
	writeOpAtName(t, root, second, "2050-01-01T00:00:00.000Z-cafef00d-update_field.json")
	writeOpAtName(t, root, last, "2000-01-01T00:00:00.000Z-baadf00d-claim.json")

	res, err := Fold(root, StubDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	state := res.Issues[id]
	if state == nil {
		t.Fatalf("issue %q missing", id)
	}
	if got := state.Fields["__last_op"]; got != "claim" {
		t.Fatalf("__last_op: got %v, want %q (HLC order should win over filename)", got, "claim")
	}
}

func TestFold_TwoIssuesInterleaved(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	idA := "act-aaaa"
	idB := "act-bbbb"

	// Interleave HLCs across issues: A1, B1, A2, B2, A3.
	writeOp(t, root, env(idA, "create", 1, 0, "11111111", `{"x":"a1"}`))
	writeOp(t, root, env(idB, "create", 2, 0, "11111111", `{"x":"b1"}`))
	writeOp(t, root, env(idA, "update_field", 3, 0, "11111111", `{"x":"a2"}`))
	writeOp(t, root, env(idB, "update_field", 4, 0, "11111111", `{"x":"b2"}`))
	writeOp(t, root, env(idA, "close", 5, 0, "11111111", `{"x":"a3"}`))

	res, err := Fold(root, StubDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.OpsConsumed != 5 {
		t.Fatalf("OpsConsumed: %d, want 5", res.OpsConsumed)
	}
	if got := res.Issues[idA].Fields["__last_op"]; got != "close" {
		t.Fatalf("A __last_op: %v want close", got)
	}
	if got := res.Issues[idB].Fields["__last_op"]; got != "update_field" {
		t.Fatalf("B __last_op: %v want update_field", got)
	}
}

func TestFold_MalformedFileReturnsErrorWithPath(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")

	// One good op so the directory tree is non-empty, plus a malformed
	// file in the same shard.
	good := env("act-cccc", "create", 1700000000000, 0, "11111111", `{"k":"v"}`)
	writeOp(t, root, good)

	shard := op.ShardDir(root, "act-cccc", good.HLC.Wall)
	bad := filepath.Join(shard, "2024-01-01T00:00:00.000Z-deadbeef-create.json")
	if err := os.WriteFile(bad, []byte("not-json{"), 0o644); err != nil {
		t.Fatalf("write bad: %v", err)
	}

	_, err := Fold(root, StubDispatch)
	if err == nil {
		t.Fatalf("Fold: nil error, want malformed-file error")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Fatalf("error %q does not reference path %q", err.Error(), bad)
	}
}

func TestFold_NonJSONFilesIgnored(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	good := env("act-dddd", "create", 1700000000000, 0, "11111111", `{"k":"v"}`)
	writeOp(t, root, good)

	shard := op.ShardDir(root, "act-dddd", good.HLC.Wall)
	for _, name := range []string{".DS_Store", "README", "junk.txt", "scratch.tmp"} {
		if err := os.WriteFile(filepath.Join(shard, name), []byte("garbage"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	res, err := Fold(root, StubDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if res.OpsConsumed != 1 {
		t.Fatalf("OpsConsumed: got %d, want 1", res.OpsConsumed)
	}
}

func TestFold_TiebreakByOpHash(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	id := "act-eeee"

	// Two ops with identical HLC (wall, logical) but different node_ids
	// produce different op_hashes. Sort must be deterministic by op_hash.
	a := env(id, "update_field", 1700000000000, 0, "11111111", `{"who":"a"}`)
	b := env(id, "update_field", 1700000000000, 0, "22222222", `{"who":"b"}`)

	// Resolve which op_hash sorts last so we know which one should win.
	hashA, err := a.FullHash()
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	hashB, err := b.FullHash()
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	expectedLastNode := a.NodeID
	if hashA > hashB {
		// hashA sorts later, so 'a' is last applied. expectedLast = a.
		expectedLastNode = a.NodeID
	} else {
		expectedLastNode = b.NodeID
	}

	writeOp(t, root, a)
	writeOp(t, root, b)

	// Run fold twice with reversed underlying file enumeration to confirm
	// determinism. We cannot directly control filepath.WalkDir order, but
	// the sort must absorb whatever order the FS returns.
	for i := 0; i < 3; i++ {
		res, err := Fold(root, StubDispatch)
		if err != nil {
			t.Fatalf("Fold: %v", err)
		}
		state := res.Issues[id]
		if state == nil {
			t.Fatalf("issue missing")
		}
		if got := state.Fields["__last_hash"]; got != expectedLastNode {
			t.Fatalf("iter %d: __last_hash: got %v, want %v", i, got, expectedLastNode)
		}
	}
}

func TestFoldIssue_FiltersToOneIssue(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	idA := "act-aaaa"
	idB := "act-bbbb"
	writeOp(t, root, env(idA, "create", 1, 0, "11111111", `{"x":"a"}`))
	writeOp(t, root, env(idB, "create", 2, 0, "11111111", `{"x":"b"}`))

	state, err := FoldIssue(root, idA, StubDispatch)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}
	if state.ID != idA {
		t.Fatalf("ID: %q want %q", state.ID, idA)
	}
	if got := state.Fields["__last_op"]; got != "create" {
		t.Fatalf("__last_op: %v want create", got)
	}
}

func TestFoldIssue_MissingReturnsEmptyState(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	state, err := FoldIssue(root, "act-zzzz", StubDispatch)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}
	if state == nil || state.ID != "act-zzzz" {
		t.Fatalf("got %+v", state)
	}
	if state.Tombstoned {
		t.Fatalf("tombstoned: want false")
	}
	if len(state.Fields) != 0 || len(state.LastHLC) != 0 {
		t.Fatalf("non-empty maps: %+v", state)
	}
}

func TestFold_TombstoneSetsFlag(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	id := "act-ffff"
	writeOp(t, root, env(id, "create", 1, 0, "11111111", `{"x":"start"}`))
	writeOp(t, root, env(id, "tombstone", 2, 0, "11111111", `{"reason":"gone"}`))

	res, err := Fold(root, StubDispatch)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	state := res.Issues[id]
	if state == nil || !state.Tombstoned {
		t.Fatalf("expected tombstoned, got %+v", state)
	}
}

func TestFold_NilDispatchErrors(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "ops")
	writeOp(t, root, env("act-gggg", "create", 1, 0, "11111111", `{"x":"y"}`))

	if _, err := Fold(root, nil); err == nil {
		t.Fatalf("Fold: nil error, want nil-dispatch error")
	}
	if _, err := Fold(root, func(string) ApplyFunc { return nil }); err == nil {
		t.Fatalf("Fold: nil error, want nil-applyfunc error")
	}
}
