package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// The read path used to rebuild index.db from a full fold of `.act/ops/` on
// every single read, so cost scaled with the whole op log rather than with
// the rows returned (act-43d11f). These tests pin the two halves of the fix
// that have to hold together:
//
//   - an unchanged store must NOT be refolded (the win), and
//   - any change to the op log underneath an existing index MUST be
//     reflected by the next read (the safety property act-fec192 cost us).
//
// The skip is asserted by tampering with index.db directly and observing
// that the tampered value survives the next read. Nothing act writes can
// produce that state — only a rebuild-skipping read can serve it — so it is
// a discriminating probe for "the rebuild did not run", not a proxy for it.

// tamperIndexTitle writes a title into index.db that no op in `.act/ops/`
// supports. A read that refolds will overwrite it; a read that serves the
// cached index will hand it back.
func tamperIndexTitle(t *testing.T, root, id, title string) {
	t.Helper()
	paths := config.Layout(root)
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()
	res, err := idx.DB().Exec(`UPDATE issues SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		t.Fatalf("tamper title: %v", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("rows affected: %v", err)
	}
	if n != 1 {
		t.Fatalf("tamper affected %d rows, want 1 (is %s in the index?)", n, id)
	}
}

// seedCloseOp writes a close op for id straight into `.act/ops/`, the way a
// concurrent `act close` would land one, and returns the file's path so a
// test can roll it back by removing it.
func seedCloseOp(t *testing.T, root, id string, wallMs int64, monthDir string) string {
	t.Helper()
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       id,
		Payload:       json.RawMessage(`{"reason":"rolled back by the gate"}`),
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: 0,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal close envelope: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", id, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, id+"-close.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// listTitle returns the title `act list` reports for id, or "" if absent.
func listTitle(t *testing.T, root, id string) string {
	t.Helper()
	out, code := RunList(root, ListOptions{All: true, Limit: 0})
	if code != 0 {
		t.Fatalf("list exit = %d, out=%+v", code, out)
	}
	res, ok := out.(ListResult)
	if !ok {
		t.Fatalf("output type = %T, want ListResult", out)
	}
	for _, is := range res.Issues {
		if is.ID == id {
			return is.Title
		}
	}
	return ""
}

// listIDs returns every id `act list --all` reports.
func listIDs(t *testing.T, root string) map[string]string {
	t.Helper()
	out, code := RunList(root, ListOptions{All: true, Limit: 0})
	if code != 0 {
		t.Fatalf("list exit = %d, out=%+v", code, out)
	}
	res, ok := out.(ListResult)
	if !ok {
		t.Fatalf("output type = %T, want ListResult", out)
	}
	got := map[string]string{}
	for _, is := range res.Issues {
		got[is.ID] = is.Status
	}
	return got
}

// TestDocClaim_List_SkipsRefoldOnUnchangedOpTree asserts the spec's `act
// list` behaviour claim — "an unchanged op tree is answered from the index
// without a refold" — as behaviour rather than as a stopwatch: with
// `.act/ops/` untouched between two reads, a value written straight into
// index.db survives the second read.
func TestDocClaim_List_SkipsRefoldOnUnchangedOpTree(t *testing.T) {
	root := listTestRepo(t)

	// First read populates the index from the op log.
	if got := listTitle(t, root, "act-aaaa"); got != "alpha task" {
		t.Fatalf("first read title = %q, want %q", got, "alpha task")
	}

	tamperIndexTitle(t, root, "act-aaaa", "SENTINEL-NOT-IN-OPS")

	// Second read, ops untouched: a rebuild would restore "alpha task".
	if got := listTitle(t, root, "act-aaaa"); got != "SENTINEL-NOT-IN-OPS" {
		t.Fatalf("second read title = %q, want the tampered value — the read refolded the whole op log on an unchanged store", got)
	}
}

// TestRunList_RefoldsWhenAnotherProcessAppendsAnOp covers the first way the
// op log moves underneath a live index: a concurrent writer lands a new op
// file. The next read must see it, and must not serve the cached rows.
func TestRunList_RefoldsWhenAnotherProcessAppendsAnOp(t *testing.T) {
	root := listTestRepo(t)

	if ids := listIDs(t, root); len(ids) != 3 {
		t.Fatalf("first read returned %d issues, want 3: %v", len(ids), ids)
	}

	// Tamper so a served-from-cache answer is distinguishable from a refold
	// even on the rows that did not change.
	tamperIndexTitle(t, root, "act-aaaa", "SENTINEL-NOT-IN-OPS")

	// Another process appends an op: a fourth issue, written straight into
	// `.act/ops/` exactly as a concurrent act writer would.
	seedIssue(t, root, "act-dddd", "delta task", "task", 3, 1700000030000, "2026-04")

	ids := listIDs(t, root)
	if _, ok := ids["act-dddd"]; !ok {
		t.Fatalf("read after an appended op did not report act-dddd: %v", ids)
	}
	if got := listTitle(t, root, "act-aaaa"); got != "alpha task" {
		t.Fatalf("act-aaaa title = %q, want %q — the refold left tampered rows in place", got, "alpha task")
	}
}

// TestRunList_RefoldsWhenARollbackDeletesAnOp covers the other direction, and
// the exact shape act-fec192 turned on: a close op is written, read, and then
// removed by a rollback. The next read must report the issue open again
// rather than answering from an index the op log no longer supports.
func TestRunList_RefoldsWhenARollbackDeletesAnOp(t *testing.T) {
	root := listTestRepo(t)

	// Close act-bbbb by landing a close op, the way `act close` does.
	closePath := seedCloseOp(t, root, "act-bbbb", 1700000040000, "2026-04")

	if got := listIDs(t, root)["act-bbbb"]; got != "closed" {
		t.Fatalf("status after the close op = %q, want closed", got)
	}

	// Tamper so a served-from-cache answer is distinguishable from a refold.
	tamperIndexTitle(t, root, "act-aaaa", "SENTINEL-NOT-IN-OPS")

	// The gate fails and the close is rolled back: the op file is removed.
	if err := os.Remove(closePath); err != nil {
		t.Fatalf("remove close op: %v", err)
	}

	if got := listIDs(t, root)["act-bbbb"]; got != "open" {
		t.Fatalf("status after the close op was rolled back = %q, want open — the read answered from an index the op log no longer supports", got)
	}
	if got := listTitle(t, root, "act-aaaa"); got != "alpha task" {
		t.Fatalf("act-aaaa title = %q, want %q — the refold left tampered rows in place", got, "alpha task")
	}
}

// TestRunList_RefoldsWhenAnOpIsRewrittenInPlace is the paranoid case for a
// staleness key built on file metadata: the op log keeps the same file set
// but one file's content changes. Rewriting an op in place is not something
// act itself does — ops are immutable once written — but a `git checkout` of
// the nested `.act/` repo can produce exactly this shape, so the next read
// still has to reflect it.
func TestRunList_RefoldsWhenAnOpIsRewrittenInPlace(t *testing.T) {
	root := listTestRepo(t)

	if got := listTitle(t, root, "act-cccc"); got != "charlie chore" {
		t.Fatalf("first read title = %q", got)
	}

	// Overwrite act-cccc's create op with a different title. Same path,
	// different bytes.
	env := makeCreateEnv(t, "act-cccc", 1700000020000, 0, "charlie REWRITTEN", "chore", 2)
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	path := filepath.Join(root, ".act", "ops", "act-cccc", "2026-04", "act-cccc-create.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("rewrite op: %v", err)
	}

	if got := listTitle(t, root, "act-cccc"); got != "charlie REWRITTEN" {
		t.Fatalf("title after the op was rewritten in place = %q, want %q", got, "charlie REWRITTEN")
	}
}
