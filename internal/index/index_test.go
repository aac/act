package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// newTempIndex opens a fresh index in a t.TempDir(). The Index is closed
// during cleanup.
func newTempIndex(t *testing.T) (*Index, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "index.db")
	idx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	if err := idx.ApplySchema(); err != nil {
		t.Fatalf("ApplySchema: %v", err)
	}
	return idx, dir
}

// writeCreateOp stages a single valid create envelope for issueID at
// rootOps/<issueID>/<yyyy-mm>/<basename>.json.
func writeCreateOp(t *testing.T, rootOps, issueID, title, basename string, wallMs int64, logical uint32) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"title": title,
		"type":  "task",
		"nonce": "0123456789abcdef0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("payload marshal: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       issueID,
		Payload:       payload,
		HLC:           hlc.HLC{Wall: wallMs, Logical: logical, NodeID: "0123abcd"},
		NodeID:        "0123abcd",
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("env.Marshal: %v", err)
	}
	dir := filepath.Join(rootOps, issueID, "2026-04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, basename)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestOpenAndApplySchema_CreatesTables(t *testing.T) {
	idx, _ := newTempIndex(t)

	// Run ApplySchema again — must be idempotent.
	if err := idx.ApplySchema(); err != nil {
		t.Fatalf("ApplySchema (second call): %v", err)
	}

	want := []string{"issues", "issue_accept", "issue_deps", "issue_external_deps", "issue_meta", "fts"}
	for _, name := range want {
		var got string
		err := idx.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE name = ?`, name,
		).Scan(&got)
		if err != nil {
			t.Fatalf("table %q missing: %v", name, err)
		}
	}

	// Also check the named indices.
	for _, name := range []string{"idx_status", "idx_priority", "idx_parent"} {
		var got string
		err := idx.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?`, name,
		).Scan(&got)
		if err != nil {
			t.Fatalf("index %q missing: %v", name, err)
		}
	}
}

func TestSchema_StringNonEmpty(t *testing.T) {
	idx, _ := newTempIndex(t)
	if idx.Schema() == "" {
		t.Fatalf("Schema() returned empty string")
	}
}

func TestRebuild_EmptyOpsDir(t *testing.T) {
	idx, dir := newTempIndex(t)
	rootOps := filepath.Join(dir, "ops")
	if err := os.MkdirAll(rootOps, 0o755); err != nil {
		t.Fatalf("mkdir ops: %v", err)
	}
	if err := idx.Rebuild(rootOps); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	rows, err := idx.ListAll(Filter{})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 issues, got %d", len(rows))
	}
}

func TestRebuild_OneCreateOp(t *testing.T) {
	idx, dir := newTempIndex(t)
	rootOps := filepath.Join(dir, "ops")
	writeCreateOp(t, rootOps, "act-abcd", "hello world", "create.json", 1700000000000, 0)

	if err := idx.Rebuild(rootOps); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	rows, err := idx.ListAll(Filter{})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(rows))
	}
	if rows[0].ID != "act-abcd" {
		t.Fatalf("ID = %q, want act-abcd", rows[0].ID)
	}
	if rows[0].Title != "hello world" {
		t.Fatalf("Title = %q, want hello world", rows[0].Title)
	}
	if rows[0].Status != "open" {
		t.Fatalf("Status = %q, want open", rows[0].Status)
	}
	if rows[0].Type != "task" {
		t.Fatalf("Type = %q, want task", rows[0].Type)
	}
}

func TestListAll_FilterByStatus(t *testing.T) {
	idx, dir := newTempIndex(t)
	rootOps := filepath.Join(dir, "ops")
	writeCreateOp(t, rootOps, "act-aaaa", "first", "c1.json", 1700000000000, 0)
	writeCreateOp(t, rootOps, "act-bbbb", "second", "c2.json", 1700000000001, 0)

	if err := idx.Rebuild(rootOps); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	open, err := idx.ListAll(Filter{Status: "open"})
	if err != nil {
		t.Fatalf("ListAll(open): %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("expected 2 open issues, got %d", len(open))
	}

	closed, err := idx.ListAll(Filter{Status: "closed"})
	if err != nil {
		t.Fatalf("ListAll(closed): %v", err)
	}
	if len(closed) != 0 {
		t.Fatalf("expected 0 closed issues, got %d", len(closed))
	}
}

func TestGet_ByID(t *testing.T) {
	idx, dir := newTempIndex(t)
	rootOps := filepath.Join(dir, "ops")
	writeCreateOp(t, rootOps, "act-abcd", "hello world", "create.json", 1700000000000, 0)

	if err := idx.Rebuild(rootOps); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	r, err := idx.Get("act-abcd")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if r.ID != "act-abcd" || r.Title != "hello world" {
		t.Fatalf("Get returned %+v", r)
	}

	if _, err := idx.Get("act-9999"); err == nil {
		t.Fatalf("Get(nonexistent): expected error, got nil")
	}
}

// TestUpsert_SnapshotRoundTrip_DepEdges is the regression test for act-8c78.
//
// The bug: upsertTx asserts rendered["deps"].([]map[string]string). That holds
// for state produced by a live fold (applyAddDep writes that typed slice into
// Fields). It silently fails when state was hydrated from a JSON snapshot
// (.act/snapshots/<id>.json) — JSON deserialises into map[string]any, with
// arrays as []any and elements as map[string]any. The type assertion's
// silent failure dropped every dep edge from the index without any error.
//
// The fix normalises "deps" and "external_deps" inside fold.RenderState so
// both live and post-snapshot state produce the canonical typed slices,
// letting upsertTx assert a single canonical type.
func TestUpsert_SnapshotRoundTrip_DepEdges(t *testing.T) {
	idx, _ := newTempIndex(t)

	const id = "act-c001"

	// Construct an IssueState whose Fields shape matches a post-snapshot
	// hydration: deps are []any with map[string]any elements, external_deps
	// are []any of strings, accept is []any of strings. This is exactly the
	// shape encoding/json produces after Unmarshalling RenderState's output
	// back into map[string]any.
	postSnapshotState := &fold.IssueState{
		ID: id,
		Fields: map[string]any{
			"title":       "round-trip target",
			"description": "dep edges must survive snapshot deser",
			"status":      "open",
			"type":        "task",
			"created_at":  "2026-05-17T00:00:00Z",
			"priority":    float64(3), // JSON numbers decode as float64
			"accept": []any{
				"first criterion",
				"second criterion",
			},
			"deps": []any{
				map[string]any{"parent": "act-aaaa", "edge_type": "blocks"},
				map[string]any{"parent": "act-bbbb", "edge_type": "relates"},
			},
			"external_deps": []any{
				"ext-ref-1",
				"ext-ref-2",
			},
		},
		LastHLC: map[string]hlc.Stamp{},
	}

	if err := idx.Upsert(postSnapshotState); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Verify the issue row landed.
	row, err := idx.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	if row.Title != "round-trip target" {
		t.Fatalf("Title = %q, want %q", row.Title, "round-trip target")
	}
	if row.Priority != 3 {
		t.Fatalf("Priority = %d, want 3", row.Priority)
	}

	// The load-bearing assertion: both dep edges land in issue_deps.
	rows, err := idx.db.Query(
		`SELECT parent_id, edge_type FROM issue_deps WHERE issue_id = ? ORDER BY parent_id`, id,
	)
	if err != nil {
		t.Fatalf("query issue_deps: %v", err)
	}
	defer rows.Close()
	type dep struct{ parent, edge string }
	var got []dep
	for rows.Next() {
		var d dep
		if err := rows.Scan(&d.parent, &d.edge); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, d)
	}
	want := []dep{
		{"act-aaaa", "blocks"},
		{"act-bbbb", "relates"},
	}
	if len(got) != len(want) {
		t.Fatalf("issue_deps rows = %d, want %d (got %+v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("issue_deps[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}

	// External deps should round-trip too.
	extRows, err := idx.db.Query(
		`SELECT ref FROM issue_external_deps WHERE issue_id = ? ORDER BY ref`, id,
	)
	if err != nil {
		t.Fatalf("query issue_external_deps: %v", err)
	}
	defer extRows.Close()
	var refs []string
	for extRows.Next() {
		var r string
		if err := extRows.Scan(&r); err != nil {
			t.Fatalf("scan ext: %v", err)
		}
		refs = append(refs, r)
	}
	wantRefs := []string{"ext-ref-1", "ext-ref-2"}
	if len(refs) != len(wantRefs) {
		t.Fatalf("external_deps rows = %d, want %d (got %v)", len(refs), len(wantRefs), refs)
	}
	for i := range wantRefs {
		if refs[i] != wantRefs[i] {
			t.Fatalf("external_deps[%d] = %q, want %q", i, refs[i], wantRefs[i])
		}
	}

	// Accept criteria should also survive (already worked pre-fix because
	// RenderState already normalised accept via getAccept; this guards
	// against regression).
	acceptRows, err := idx.db.Query(
		`SELECT criterion FROM issue_accept WHERE issue_id = ? ORDER BY idx`, id,
	)
	if err != nil {
		t.Fatalf("query issue_accept: %v", err)
	}
	defer acceptRows.Close()
	var crits []string
	for acceptRows.Next() {
		var c string
		if err := acceptRows.Scan(&c); err != nil {
			t.Fatalf("scan accept: %v", err)
		}
		crits = append(crits, c)
	}
	sort.Strings(crits)
	wantCrits := []string{"first criterion", "second criterion"}
	if len(crits) != len(wantCrits) {
		t.Fatalf("accept rows = %d, want %d (got %v)", len(crits), len(wantCrits), crits)
	}
	for i := range wantCrits {
		if crits[i] != wantCrits[i] {
			t.Fatalf("accept[%d] = %q, want %q", i, crits[i], wantCrits[i])
		}
	}
}

// TestUpsert_LiveAndSnapshotStatesEquivalent verifies that an IssueState built
// directly (live-fold form, []map[string]string) and the same state after a
// JSON round-trip (snapshot form, []any of map[string]any) produce identical
// rows in the index — the canonical-type contract of RenderState.
func TestUpsert_LiveAndSnapshotStatesEquivalent(t *testing.T) {
	idx, _ := newTempIndex(t)

	// Live shape (what applyAddDep produces).
	liveState := &fold.IssueState{
		ID: "act-d001",
		Fields: map[string]any{
			"title":      "live",
			"type":       "task",
			"status":     "open",
			"created_at": "2026-05-17T00:00:00Z",
			"accept":     []string{"crit-a"},
			"deps": []map[string]string{
				{"parent": "act-aaaa", "edge_type": "blocks"},
			},
			"external_deps": []string{"ext-1"},
		},
		LastHLC: map[string]hlc.Stamp{},
	}
	if err := idx.Upsert(liveState); err != nil {
		t.Fatalf("Upsert live: %v", err)
	}

	// Build the post-snapshot equivalent by actually round-tripping
	// RenderState through JSON, then rehydrating Fields. This guarantees
	// the shape exactly matches what compact-snapshot deserialisation
	// would produce, rather than hand-rolling the types.
	rendered := fold.RenderState(liveState)
	if rendered == nil {
		t.Fatal("RenderState returned nil for live state")
	}
	body, err := json.Marshal(rendered)
	if err != nil {
		t.Fatalf("marshal rendered: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal rendered: %v", err)
	}
	// Sanity: after JSON round-trip, deps is []any of map[string]any.
	rawDeps, _ := decoded["deps"].([]any)
	if len(rawDeps) == 0 {
		t.Fatalf("deps lost on JSON round-trip: %T %v", decoded["deps"], decoded["deps"])
	}
	if _, ok := rawDeps[0].(map[string]any); !ok {
		t.Fatalf("dep element type after round-trip = %T, want map[string]any", rawDeps[0])
	}

	snapState := &fold.IssueState{
		ID:      "act-d002",
		Fields:  decoded,
		LastHLC: map[string]hlc.Stamp{},
	}
	if err := idx.Upsert(snapState); err != nil {
		t.Fatalf("Upsert snap: %v", err)
	}

	// Both should have exactly one dep edge with the same parent/edge.
	for _, id := range []string{"act-d001", "act-d002"} {
		row := idx.db.QueryRow(
			`SELECT parent_id, edge_type FROM issue_deps WHERE issue_id = ?`, id,
		)
		var parent, edge string
		if err := row.Scan(&parent, &edge); err != nil {
			t.Fatalf("scan dep for %s: %v", id, err)
		}
		if parent != "act-aaaa" || edge != "blocks" {
			t.Fatalf("%s dep = (%s, %s), want (act-aaaa, blocks)", id, parent, edge)
		}
	}
}

func TestRebuild_Idempotent(t *testing.T) {
	idx, dir := newTempIndex(t)
	rootOps := filepath.Join(dir, "ops")
	writeCreateOp(t, rootOps, "act-abcd", "hello world", "create.json", 1700000000000, 0)

	if err := idx.Rebuild(rootOps); err != nil {
		t.Fatalf("Rebuild #1: %v", err)
	}
	first, err := idx.ListAll(Filter{})
	if err != nil {
		t.Fatalf("ListAll #1: %v", err)
	}

	if err := idx.Rebuild(rootOps); err != nil {
		t.Fatalf("Rebuild #2: %v", err)
	}
	second, err := idx.ListAll(Filter{})
	if err != nil {
		t.Fatalf("ListAll #2: %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("row count changed: %d -> %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].Title != second[i].Title ||
			first[i].Status != second[i].Status {
			t.Fatalf("row %d changed across rebuild: %+v vs %+v", i, first[i], second[i])
		}
	}
}
