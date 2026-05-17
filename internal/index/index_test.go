package index

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
