package fold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// TestGenerateGoldens writes the testdata/golden/<op-type>/{before,op,after}.json
// files for every op type. It is gated on GOLDEN_GENERATE=1 so normal `go test`
// runs do not touch the fixtures (which would defeat their purpose). After
// running once with the flag, the fixtures are committed and serve as the
// canonical record.
func TestGenerateGoldens(t *testing.T) {
	if os.Getenv("GOLDEN_GENERATE") != "1" {
		t.Skip("set GOLDEN_GENERATE=1 to (re)generate fixtures")
	}
	root := filepath.Join("testdata", "golden")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const issueID = "act-aaaa"
	const nodeA = "11111111"
	const nodeB = "22222222"
	const nonce = "00000000000000000000000000000000"

	// Reusable HLCs.
	h := func(wall int64, logical uint32, node string) hlc.HLC {
		return hlc.HLC{Wall: wall, Logical: logical, NodeID: node}
	}

	// Helper to build a before-state populated by a sequence of apply calls.
	apply := func(t *testing.T, st *IssueState, opType string, hh hlc.HLC, payload any) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        opType,
			IssueID:       st.ID,
			Payload:       body,
			HLC:           hh,
			NodeID:        hh.NodeID,
		}
		fn := ApplyDispatch(opType)
		if fn == nil {
			t.Fatalf("dispatch nil for %s", opType)
		}
		fullHash, err := env.FullHash()
		if err != nil {
			t.Fatalf("full hash %s: %v", opType, err)
		}
		if err := fn(st, env, body, fullHash); err != nil {
			t.Fatalf("apply %s: %v", opType, err)
		}
	}

	// Helper to write a (before, op, after) triple.
	write := func(t *testing.T, name string, before *IssueState, opEnv op.Envelope) {
		t.Helper()
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}

		// Round-trip before through json so on-disk shape matches what
		// loadGoldenState produces (decoded as encoding/json native types).
		beforeRT := roundTripState(t, before)

		bb := formatGoldenStateJSON(t, beforeRT)
		if err := os.WriteFile(filepath.Join(dir, "before.json"), append(bb, '\n'), 0o644); err != nil {
			t.Fatalf("write before: %v", err)
		}

		envBytes, err := opEnv.Marshal()
		if err != nil {
			t.Fatalf("env.Marshal: %v", err)
		}
		// Pretty-print envelope for readability while keeping content equal.
		var generic any
		if err := json.Unmarshal(envBytes, &generic); err != nil {
			t.Fatalf("unmarshal env: %v", err)
		}
		pretty, err := json.MarshalIndent(generic, "", "  ")
		if err != nil {
			t.Fatalf("marshal indent env: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "op.json"), append(pretty, '\n'), 0o644); err != nil {
			t.Fatalf("write op: %v", err)
		}

		// Apply the op to a fresh copy of before to compute after.
		st := roundTripState(t, before)
		fn := ApplyDispatch(opEnv.OpType)
		if fn == nil {
			t.Fatalf("dispatch nil for %s", opEnv.OpType)
		}
		fullHash, err := opEnv.FullHash()
		if err != nil {
			t.Fatalf("full hash: %v", err)
		}
		if err := fn(st, opEnv, opEnv.Payload, fullHash); err != nil {
			t.Fatalf("apply for after: %v", err)
		}
		after := renderCanonicalForGolden(t, st)
		if err := os.WriteFile(filepath.Join(dir, "after.json"), append(after, '\n'), 0o644); err != nil {
			t.Fatalf("write after: %v", err)
		}
	}

	mkEnvFor := func(opType string, hh hlc.HLC, payload any) op.Envelope {
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        opType,
			IssueID:       issueID,
			Payload:       body,
			HLC:           hh,
			NodeID:        hh.NodeID,
		}
	}

	// 1) create: empty before -> after has the issue with title etc.
	{
		before := newIssueState(issueID)
		env := mkEnvFor("create", h(1700000000000, 0, nodeA), op.CreatePayload{
			Title: "hello world", Type: "task", Nonce: nonce,
		})
		write(t, "create", before, env)
	}

	// 2) update_field title: before has title=X -> after has title=Y (newer HLC).
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "old", Type: "task", Nonce: nonce})
		env := mkEnvFor("update_field", h(1700000001000, 0, nodeA),
			op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"new"`)})
		write(t, "update_field", before, env)
	}

	// 3) update_field stale: HLC older than current -> state unchanged (LWW).
	{
		before := newIssueState(issueID)
		// Create at wall=1700000010000 -> last_hlc[title] = that.
		apply(t, before, "create", h(1700000010000, 0, nodeA),
			op.CreatePayload{Title: "current", Type: "task", Nonce: nonce})
		// Op uses an EARLIER wall.
		env := mkEnvFor("update_field", h(1700000005000, 0, nodeA),
			op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"stale"`)})
		write(t, "update_field-stale", before, env)
	}

	// 4) add_dep
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		env := mkEnvFor("add_dep", h(1700000001000, 0, nodeA),
			op.AddDepPayload{Parent: "act-bbbb", EdgeType: "blocks"})
		write(t, "add_dep", before, env)
	}

	// 5) remove_dep
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		apply(t, before, "add_dep", h(1700000001000, 0, nodeA),
			op.AddDepPayload{Parent: "act-bbbb", EdgeType: "blocks"})
		env := mkEnvFor("remove_dep", h(1700000002000, 0, nodeA),
			op.RemoveDepPayload{Parent: "act-bbbb", EdgeType: "blocks"})
		write(t, "remove_dep", before, env)
	}

	// 6) add_accept
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		env := mkEnvFor("add_accept", h(1700000001000, 0, nodeA),
			op.AddAcceptPayload{Criterion: "tests pass"})
		write(t, "add_accept", before, env)
	}

	// 7) remove_accept (by index against effective list)
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Accept: []string{"a", "b", "c"}, Nonce: nonce})
		env := mkEnvFor("remove_accept", h(1700000001000, 0, nodeA),
			op.RemoveAcceptPayload{Index: 1}) // remove "b"
		write(t, "remove_accept", before, env)
	}

	// 8) claim: status=in_progress, assignee set
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		env := mkEnvFor("claim", h(1700000001000, 0, nodeB),
			op.ClaimPayload{Assignee: "alice"})
		write(t, "claim", before, env)
	}

	// 9) close: status=closed
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		env := mkEnvFor("close", h(1700000001000, 0, nodeA),
			op.ClosePayload{Reason: "done"})
		write(t, "close", before, env)
	}

	// 11) import: bookkeeping only
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		env := mkEnvFor("import", h(1700000001000, 0, nodeA),
			op.ImportPayload{SourceRef: "github://owner/repo/issues/42"})
		write(t, "import", before, env)
	}

	// 12) migrate: bookkeeping only
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		env := mkEnvFor("migrate", h(1700000001000, 0, nodeA),
			op.MigratePayload{FromVersion: 1, ToVersion: 2})
		write(t, "migrate", before, env)
	}

	// 13) tombstone: RenderState returns nil/empty
	{
		before := newIssueState(issueID)
		apply(t, before, "create", h(1700000000000, 0, nodeA),
			op.CreatePayload{Title: "t", Type: "task", Nonce: nonce})
		env := mkEnvFor("tombstone", h(1700000001000, 0, nodeA),
			op.TombstonePayload{DeletedAt: "2026-04-29T00:00:00Z"})
		write(t, "tombstone", before, env)
	}
}

// roundTripState marshals & unmarshals state.Fields/LastHLC so that the in-memory
// shapes match what loadGoldenState would produce from disk. This avoids
// surprises where apply functions emit native Go types ([]string,
// map[string]bool) that disagree with their JSON-decoded ([]any,
// map[string]any) counterparts.
func roundTripState(t *testing.T, st *IssueState) *IssueState {
	t.Helper()
	g := goldenStateJSON{
		ID:         st.ID,
		Fields:     st.Fields,
		LastHLC:    st.LastHLC,
		Tombstoned: st.Tombstoned,
	}
	body, err := json.Marshal(g)
	if err != nil {
		t.Fatalf("roundTrip marshal: %v", err)
	}
	var out goldenStateJSON
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("roundTrip unmarshal: %v", err)
	}
	st2 := newIssueState(out.ID)
	if out.Fields != nil {
		st2.Fields = out.Fields
	}
	if out.LastHLC != nil {
		st2.LastHLC = out.LastHLC
	}
	st2.Tombstoned = out.Tombstoned
	return st2
}
