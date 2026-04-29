package fold

import (
	"encoding/json"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// mkEnv constructs a minimal valid op.Envelope for apply tests. We bypass
// disk I/O entirely; apply functions only read env.HLC, env.IssueID, and the
// passed-in payload bytes.
func mkEnv(issueID, opType string, wall int64, logical uint32, nodeID string) op.Envelope {
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		HLC:           hlc.HLC{Wall: wall, Logical: logical, NodeID: nodeID},
		NodeID:        nodeID,
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func freshState(id string) *IssueState {
	return newIssueState(id)
}

func runCreate(t *testing.T, state *IssueState, env op.Envelope, p op.CreatePayload) {
	t.Helper()
	if err := applyCreate(state, env, mustJSON(t, p)); err != nil {
		t.Fatalf("applyCreate: %v", err)
	}
}

func TestApply_CreateSetsDefaults(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	e := mkEnv(id, "create", 1700000000000, 0, "11111111")
	runCreate(t, st, e, op.CreatePayload{
		Title: "hello", Type: "task", Nonce: "00000000000000000000000000000000",
	})
	if st.Fields["title"] != "hello" {
		t.Fatalf("title: %v", st.Fields["title"])
	}
	if st.Fields["type"] != "task" {
		t.Fatalf("type: %v", st.Fields["type"])
	}
	if st.Fields["priority"] != 1 {
		t.Fatalf("priority default: %v", st.Fields["priority"])
	}
	if _, ok := st.Fields["created_at"]; !ok {
		t.Fatalf("created_at missing")
	}
	if st.Fields["status"] != "open" {
		t.Fatalf("status default: %v", st.Fields["status"])
	}
}

func TestApply_CreateThenUpdateTitle(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	e1 := mkEnv(id, "create", 1, 0, "11111111")
	runCreate(t, st, e1, op.CreatePayload{Title: "old", Type: "task", Nonce: "00000000000000000000000000000000"})

	e2 := mkEnv(id, "update_field", 2, 0, "11111111")
	uf := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"new"`)}
	if err := applyUpdateField(st, e2, mustJSON(t, uf)); err != nil {
		t.Fatalf("applyUpdateField: %v", err)
	}
	if st.Fields["title"] != "new" {
		t.Fatalf("title: %v", st.Fields["title"])
	}
}

func TestApply_TwoUpdateFieldsLaterHLCWins(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "x", Type: "task", Nonce: "00000000000000000000000000000000"})

	uf1 := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"first"`)}
	uf2 := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"second"`)}

	if err := applyUpdateField(st, mkEnv(id, "update_field", 10, 0, "11111111"), mustJSON(t, uf1)); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdateField(st, mkEnv(id, "update_field", 20, 0, "11111111"), mustJSON(t, uf2)); err != nil {
		t.Fatal(err)
	}
	if st.Fields["title"] != "second" {
		t.Fatalf("title: got %v want second", st.Fields["title"])
	}
}

func TestApply_OutOfOrderUpdateFieldHLCWinnerWins(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "x", Type: "task", Nonce: "00000000000000000000000000000000"})

	// Apply the LATER op first, then the earlier op. HLC winner wins
	// regardless of fold order.
	uf2 := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"winner"`)}
	uf1 := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"loser"`)}

	if err := applyUpdateField(st, mkEnv(id, "update_field", 100, 0, "11111111"), mustJSON(t, uf2)); err != nil {
		t.Fatal(err)
	}
	if err := applyUpdateField(st, mkEnv(id, "update_field", 50, 0, "11111111"), mustJSON(t, uf1)); err != nil {
		t.Fatal(err)
	}
	if st.Fields["title"] != "winner" {
		t.Fatalf("title: got %v want winner", st.Fields["title"])
	}
}

func TestApply_UpdateFieldAfterCloseStillApplies(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "x", Type: "task", Nonce: "00000000000000000000000000000000"})

	if err := applyClose(st, mkEnv(id, "close", 5, 0, "11111111"), mustJSON(t, op.ClosePayload{Reason: "done"})); err != nil {
		t.Fatal(err)
	}
	uf := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"after-close"`)}
	if err := applyUpdateField(st, mkEnv(id, "update_field", 10, 0, "11111111"), mustJSON(t, uf)); err != nil {
		t.Fatal(err)
	}
	if st.Fields["title"] != "after-close" {
		t.Fatalf("title: %v", st.Fields["title"])
	}
	if st.Fields["status"] != "closed" {
		t.Fatalf("status: %v want closed", st.Fields["status"])
	}
}

func TestApply_AddDepIdempotent(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	p := op.AddDepPayload{Parent: "act-bbbb", EdgeType: "blocks"}
	for i := 0; i < 3; i++ {
		if err := applyAddDep(st, mkEnv(id, "add_dep", int64(i+1), 0, "11111111"), mustJSON(t, p)); err != nil {
			t.Fatal(err)
		}
	}
	deps := getDeps(st)
	if len(deps) != 1 {
		t.Fatalf("deps: %d want 1", len(deps))
	}
	// Different edge_type is a distinct edge.
	p2 := op.AddDepPayload{Parent: "act-bbbb", EdgeType: "relates"}
	if err := applyAddDep(st, mkEnv(id, "add_dep", 10, 0, "11111111"), mustJSON(t, p2)); err != nil {
		t.Fatal(err)
	}
	if got := len(getDeps(st)); got != 2 {
		t.Fatalf("deps: %d want 2", got)
	}
}

func TestApply_RemoveDepPresentAndAbsent(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	if err := applyAddDep(st, mkEnv(id, "add_dep", 1, 0, "11111111"),
		mustJSON(t, op.AddDepPayload{Parent: "act-bbbb", EdgeType: "blocks"})); err != nil {
		t.Fatal(err)
	}
	// Remove non-matching: state unchanged.
	if err := applyRemoveDep(st, mkEnv(id, "remove_dep", 2, 0, "11111111"),
		mustJSON(t, op.RemoveDepPayload{Parent: "act-cccc", EdgeType: "blocks"})); err != nil {
		t.Fatal(err)
	}
	if len(getDeps(st)) != 1 {
		t.Fatalf("absent remove changed deps: %v", getDeps(st))
	}
	// Remove present.
	if err := applyRemoveDep(st, mkEnv(id, "remove_dep", 3, 0, "11111111"),
		mustJSON(t, op.RemoveDepPayload{Parent: "act-bbbb", EdgeType: "blocks"})); err != nil {
		t.Fatal(err)
	}
	if len(getDeps(st)) != 0 {
		t.Fatalf("present remove did not remove: %v", getDeps(st))
	}
}

func TestApply_AddRemoveAcceptByIndex(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	for i, c := range []string{"a", "b", "c"} {
		_ = i
		if err := applyAddAccept(st, mkEnv(id, "add_accept", int64(i+1), 0, "11111111"),
			mustJSON(t, op.AddAcceptPayload{Criterion: c})); err != nil {
			t.Fatal(err)
		}
	}
	// Remove index 1 ("b"). Effective accept becomes [a, c].
	if err := applyRemoveAccept(st, mkEnv(id, "remove_accept", 10, 0, "11111111"),
		mustJSON(t, op.RemoveAcceptPayload{Index: 1})); err != nil {
		t.Fatal(err)
	}
	rendered := RenderState(st)
	got, _ := rendered["accept"].([]string)
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("accept after remove: %v", got)
	}
}

func TestApply_ClaimEarliestWins(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	if err := applyClaim(st, mkEnv(id, "claim", 100, 0, "11111111"),
		mustJSON(t, op.ClaimPayload{Assignee: "alice"})); err != nil {
		t.Fatal(err)
	}
	// Apply an earlier claim — should win.
	if err := applyClaim(st, mkEnv(id, "claim", 50, 0, "22222222"),
		mustJSON(t, op.ClaimPayload{Assignee: "bob"})); err != nil {
		t.Fatal(err)
	}
	if st.Fields["assignee"] != "bob" {
		t.Fatalf("assignee: %v want bob (earliest)", st.Fields["assignee"])
	}
	if st.Fields["status"] != "in_progress" {
		t.Fatalf("status: %v want in_progress", st.Fields["status"])
	}
	// Apply a still-later claim — should NOT override.
	if err := applyClaim(st, mkEnv(id, "claim", 200, 0, "33333333"),
		mustJSON(t, op.ClaimPayload{Assignee: "carol"})); err != nil {
		t.Fatal(err)
	}
	if st.Fields["assignee"] != "bob" {
		t.Fatalf("assignee after late claim: %v want bob", st.Fields["assignee"])
	}
}

func TestApply_CloseIdempotent(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	if err := applyClose(st, mkEnv(id, "close", 10, 0, "11111111"),
		mustJSON(t, op.ClosePayload{Reason: "first"})); err != nil {
		t.Fatal(err)
	}
	// A second close with later HLC overwrites reason (LWW).
	if err := applyClose(st, mkEnv(id, "close", 20, 0, "11111111"),
		mustJSON(t, op.ClosePayload{Reason: "second"})); err != nil {
		t.Fatal(err)
	}
	if st.Fields["status"] != "closed" {
		t.Fatalf("status: %v", st.Fields["status"])
	}
	if st.Fields["closed_reason"] != "second" {
		t.Fatalf("reason: %v want second", st.Fields["closed_reason"])
	}
	// Idempotent: a third close with EARLIER HLC must NOT change anything.
	if err := applyClose(st, mkEnv(id, "close", 5, 0, "11111111"),
		mustJSON(t, op.ClosePayload{Reason: "earlier"})); err != nil {
		t.Fatal(err)
	}
	if st.Fields["closed_reason"] != "second" {
		t.Fatalf("earlier close mutated state: %v", st.Fields["closed_reason"])
	}
}

func TestApply_UpdateFieldStatusIgnored(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "x", Type: "task", Nonce: "00000000000000000000000000000000"})
	if err := applyClose(st, mkEnv(id, "close", 5, 0, "11111111"),
		mustJSON(t, op.ClosePayload{Reason: "done"})); err != nil {
		t.Fatal(err)
	}
	uf := op.UpdateFieldPayload{Field: "status", Value: json.RawMessage(`"open"`)}
	if err := applyUpdateField(st, mkEnv(id, "update_field", 10, 0, "11111111"), mustJSON(t, uf)); err != nil {
		t.Fatal(err)
	}
	if st.Fields["status"] != "closed" {
		t.Fatalf("status mutated by update_field: %v", st.Fields["status"])
	}
}

func TestApply_RedactRendersPlaceholder(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "secret", Description: "private", Type: "task", Nonce: "00000000000000000000000000000000"})

	if err := applyRedact(st, mkEnv(id, "redact", 5, 0, "11111111"),
		mustJSON(t, op.RedactPayload{FieldPath: "description"})); err != nil {
		t.Fatal(err)
	}
	rendered := RenderState(st)
	if rendered["description"] != "<redacted>" {
		t.Fatalf("description: %v want <redacted>", rendered["description"])
	}
	if rendered["title"] != "secret" {
		t.Fatalf("title leaked: %v", rendered["title"])
	}
}

func TestApply_TombstoneRendersNil(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "x", Type: "task", Nonce: "00000000000000000000000000000000"})
	if err := applyTombstone(st, mkEnv(id, "tombstone", 2, 0, "11111111"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if !st.Tombstoned {
		t.Fatalf("tombstoned not set")
	}
	if RenderState(st) != nil {
		t.Fatalf("RenderState on tombstoned not nil")
	}
}

func TestApply_ImportRecordsMetadata(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	if err := applyImport(st, mkEnv(id, "import", 1, 0, "11111111"),
		mustJSON(t, op.ImportPayload{SourceRef: "github://owner/repo/issues/42"})); err != nil {
		t.Fatal(err)
	}
	if st.Fields[keyImportSource] != "github://owner/repo/issues/42" {
		t.Fatalf("import source: %v", st.Fields[keyImportSource])
	}
	// Internal key must NOT appear in render output.
	rendered := RenderState(st)
	if _, ok := rendered[keyImportSource]; ok {
		t.Fatalf("internal key leaked into render: %v", rendered)
	}
}

func TestApply_MigrateRecordsMetadata(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	if err := applyMigrate(st, mkEnv(id, "migrate", 1, 0, "11111111"),
		mustJSON(t, op.MigratePayload{FromVersion: 1, ToVersion: 2})); err != nil {
		t.Fatal(err)
	}
	m, ok := st.Fields[keyLastMigration].(map[string]any)
	if !ok {
		t.Fatalf("migration metadata missing: %T", st.Fields[keyLastMigration])
	}
	if m["from_version"] != 1 || m["to_version"] != 2 {
		t.Fatalf("migration metadata: %v", m)
	}
}

func TestApply_DispatchAllOpTypesSmoke(t *testing.T) {
	// Smoke: every valid op type must dispatch without error against a
	// freshly-created issue. We run them in a coherent sequence.
	id := "act-aaaa"
	st := freshState(id)

	steps := []struct {
		opType  string
		payload any
	}{
		{"create", op.CreatePayload{Title: "t", Type: "task", Accept: []string{"a", "b"}, Nonce: "00000000000000000000000000000000"}},
		{"update_field", op.UpdateFieldPayload{Field: "description", Value: json.RawMessage(`"d"`)}},
		{"add_dep", op.AddDepPayload{Parent: "act-bbbb", EdgeType: "blocks"}},
		{"add_accept", op.AddAcceptPayload{Criterion: "c"}},
		{"remove_accept", op.RemoveAcceptPayload{Index: 0}},
		{"remove_dep", op.RemoveDepPayload{Parent: "act-bbbb", EdgeType: "blocks"}},
		{"claim", op.ClaimPayload{Assignee: "alice"}},
		{"redact", op.RedactPayload{FieldPath: "description"}},
		{"import", op.ImportPayload{SourceRef: "src"}},
		{"migrate", op.MigratePayload{FromVersion: 1, ToVersion: 2}},
		{"close", op.ClosePayload{Reason: "done"}},
		{"tombstone", op.TombstonePayload{DeletedAt: "2026-04-29T00:00:00Z"}},
	}
	var wall int64 = 1
	for _, s := range steps {
		fn := ApplyDispatch(s.opType)
		if fn == nil {
			t.Fatalf("ApplyDispatch(%q) = nil", s.opType)
		}
		env := mkEnv(id, s.opType, wall, 0, "11111111")
		if err := fn(st, env, mustJSON(t, s.payload)); err != nil {
			t.Fatalf("apply %s: %v", s.opType, err)
		}
		wall++
	}
	if !st.Tombstoned {
		t.Fatalf("tombstoned expected after smoke run")
	}
}

func TestApply_DispatchUnknownReturnsNil(t *testing.T) {
	if ApplyDispatch("nonsense") != nil {
		t.Fatalf("ApplyDispatch(nonsense): want nil")
	}
}

func TestApply_CreateDoubleCreateIgnored(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "first", Type: "task", Nonce: "00000000000000000000000000000000"})
	runCreate(t, st, mkEnv(id, "create", 2, 0, "11111111"),
		op.CreatePayload{Title: "second", Type: "task", Nonce: "00000000000000000000000000000000"})
	if st.Fields["title"] != "first" {
		t.Fatalf("title: %v want first (double-create must be ignored)", st.Fields["title"])
	}
}

func TestApply_RenderStripsInternalKeys(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "t", Type: "task", Nonce: "00000000000000000000000000000000"})
	if err := applyImport(st, mkEnv(id, "import", 2, 0, "11111111"),
		mustJSON(t, op.ImportPayload{SourceRef: "src"})); err != nil {
		t.Fatal(err)
	}
	if err := applyRedact(st, mkEnv(id, "redact", 3, 0, "11111111"),
		mustJSON(t, op.RedactPayload{FieldPath: "title"})); err != nil {
		t.Fatal(err)
	}
	rendered := RenderState(st)
	for k := range rendered {
		if len(k) >= 2 && k[:2] == "__" {
			t.Fatalf("internal key %q leaked: %v", k, rendered)
		}
	}
	if rendered["title"] != "<redacted>" {
		t.Fatalf("redacted title not rendered: %v", rendered["title"])
	}
}
