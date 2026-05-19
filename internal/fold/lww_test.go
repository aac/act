package fold

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// renderCanonical returns the canonical JSON encoding of RenderState(state),
// suitable for byte-equality comparisons. The accept slice and deps slice are
// re-marshalled through encoding/json first to normalise concrete-vs-interface
// element types (canonicaljson is reflection-driven and treats []string and
// []any with strings as distinct shapes; round-tripping flattens both to
// []any-of-string).
func renderCanonical(t *testing.T, state *IssueState) []byte {
	t.Helper()
	rendered := RenderState(state)
	if rendered == nil {
		return []byte("null")
	}
	// Round-trip through encoding/json to normalise types ([]string vs []any
	// of strings, map[string]bool vs map[string]any{bool}).
	intermediate, err := json.Marshal(rendered)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var generic any
	if err := json.Unmarshal(intermediate, &generic); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	out, err := canonicaljson.Marshal(generic)
	if err != nil {
		t.Fatalf("canonicaljson.Marshal: %v", err)
	}
	return out
}

// generatedOp is a single op envelope plus its canonical hash, used by the
// permutation property test to drive applyAll-equivalent fold sequences.
type generatedOp struct {
	env      op.Envelope
	fullHash string
	payload  []byte
}

func mustHash(t *testing.T, e op.Envelope) string {
	t.Helper()
	h, err := e.FullHash()
	if err != nil {
		t.Fatalf("FullHash: %v", err)
	}
	return h
}

// foldSorted sorts the given ops by (wall, logical, fullHash) and applies them
// in order against a fresh state. This mirrors the production fold pipeline
// without going through disk.
func foldSorted(t *testing.T, issueID string, ops []generatedOp) *IssueState {
	t.Helper()
	cp := make([]generatedOp, len(ops))
	copy(cp, ops)
	// In-place insertion sort keyed by (wall, logical, fullHash). The op set
	// is small in tests; insertion sort is stable and obvious.
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0; j-- {
			a, b := cp[j-1], cp[j]
			if !lessGenerated(b, a) {
				break
			}
			cp[j-1], cp[j] = b, a
		}
	}
	state := newIssueState(issueID)
	for _, g := range cp {
		fn := ApplyDispatch(g.env.OpType)
		if fn == nil {
			t.Fatalf("no apply for %q", g.env.OpType)
		}
		if err := fn(state, g.env, g.payload, g.fullHash); err != nil {
			t.Fatalf("apply %s: %v", g.env.OpType, err)
		}
	}
	return state
}

func lessGenerated(a, b generatedOp) bool {
	if a.env.HLC.Wall != b.env.HLC.Wall {
		return a.env.HLC.Wall < b.env.HLC.Wall
	}
	if a.env.HLC.Logical != b.env.HLC.Logical {
		return a.env.HLC.Logical < b.env.HLC.Logical
	}
	return a.fullHash < b.fullHash
}

// TestPropertyLWWPermutationInvariance asserts that for any sequence of
// generated ops, folding in HLC-sorted order yields a RenderState that is
// byte-identical regardless of the *input* permutation. Permuting only the
// fold-input order (the sort step is part of fold) must not change output.
func TestPropertyLWWPermutationInvariance(t *testing.T) {
	const iterations = 100
	const opsPerIter = 12
	issueID := "act-aaaa"

	for iter := 0; iter < iterations; iter++ {
		seed := int64(iter * 7919)
		r := rand.New(rand.NewSource(seed))
		ops := generateRandomOps(t, r, issueID, opsPerIter)

		canonical := renderCanonical(t, foldSorted(t, issueID, ops))

		// Permute the input order 5 times and confirm output is identical.
		for k := 0; k < 5; k++ {
			perm := make([]generatedOp, len(ops))
			copy(perm, ops)
			r.Shuffle(len(perm), func(i, j int) { perm[i], perm[j] = perm[j], perm[i] })
			got := renderCanonical(t, foldSorted(t, issueID, perm))
			if !bytes.Equal(canonical, got) {
				t.Fatalf("seed=%d perm=%d: render diverged\nwant: %s\ngot:  %s", seed, k, canonical, got)
			}
		}
	}
}

// generateRandomOps builds a deterministic-by-seed mix of ops over a fixed
// HLC wall range. Walls and logicals collide with non-trivial probability so
// the (op_hash) tiebreak path is exercised. Each op carries a unique nonce
// (via the op_hash) so equal HLCs still sort deterministically.
func generateRandomOps(t *testing.T, r *rand.Rand, issueID string, n int) []generatedOp {
	t.Helper()
	out := make([]generatedOp, 0, n)
	// Always start with a create at wall=1 so subsequent ops have a state to
	// mutate.
	create := op.CreatePayload{
		Title: "init", Type: "task",
		Nonce: "00000000000000000000000000000000",
	}
	createEnv := mkEnvP(t, issueID, "create", 1, 0, "11111111", create)
	out = append(out, createEnv)

	titles := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	descs := []string{"d1", "d2", "d3"}
	deps := []struct {
		parent string
		edge   string
	}{
		{"act-bbbb", "blocks"},
		{"act-cccc", "relates"},
		{"act-bbbb", "relates"},
	}
	criteria := []string{"c1", "c2", "c3", "c4"}
	nodes := []string{"11111111", "22222222", "33333333"}

	for i := 0; i < n; i++ {
		// Use distinct (wall, logical, node) triples per op so the
		// (wall, logical, op_hash) sort is total: same payload + same HLC
		// across distinct op types would otherwise yield identical op
		// hashes (envelope hash is over hlc+node_id+payload, not op_type).
		// Walls still collide across ops with non-trivial probability so
		// the op_hash tiebreak path is exercised.
		wall := int64(2 + r.Intn(6))
		logical := uint32(i) // monotone-per-op disambiguator
		node := nodes[r.Intn(len(nodes))]
		switch r.Intn(5) {
		case 0:
			p := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"` + titles[r.Intn(len(titles))] + `"`)}
			out = append(out, mkEnvP(t, issueID, "update_field", wall, logical, node, p))
		case 1:
			p := op.UpdateFieldPayload{Field: "description", Value: json.RawMessage(`"` + descs[r.Intn(len(descs))] + `"`)}
			out = append(out, mkEnvP(t, issueID, "update_field", wall, logical, node, p))
		case 2:
			d := deps[r.Intn(len(deps))]
			p := op.AddDepPayload{Parent: d.parent, EdgeType: d.edge}
			out = append(out, mkEnvP(t, issueID, "add_dep", wall, logical, node, p))
		case 3:
			p := op.AddAcceptPayload{Criterion: criteria[r.Intn(len(criteria))]}
			out = append(out, mkEnvP(t, issueID, "add_accept", wall, logical, node, p))
		case 4:
			d := deps[r.Intn(len(deps))]
			p := op.RemoveDepPayload{Parent: d.parent, EdgeType: d.edge}
			out = append(out, mkEnvP(t, issueID, "remove_dep", wall, logical, node, p))
		}
	}
	return out
}

// mkEnvP builds a generatedOp from a payload value, computing its full hash so
// the global sort can use it as a tiebreaker.
func mkEnvP(t *testing.T, issueID, opType string, wall int64, logical uint32, nodeID string, payload any) generatedOp {
	t.Helper()
	pb, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("payload marshal: %v", err)
	}
	e := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       pb,
		HLC:           hlc.HLC{Wall: wall, Logical: logical, NodeID: nodeID},
		NodeID:        nodeID,
	}
	return generatedOp{env: e, fullHash: mustHash(t, e), payload: pb}
}

// TestStatusOnlyViaClaimOrClose confirms two layers of defence:
//  1. Write-time validation (op.UpdateFieldPayload.Validate) rejects field=status.
//  2. Apply-time defensive ignore (applyUpdateField) leaves state untouched
//     even if a malformed payload makes it past validation.
func TestStatusOnlyViaClaimOrClose(t *testing.T) {
	// Layer 1: write-time validation.
	bad := op.UpdateFieldPayload{Field: "status", Value: json.RawMessage(`"closed"`)}
	if err := bad.Validate(); err == nil {
		t.Fatal("UpdateFieldPayload.Validate: want error for field=status, got nil")
	}

	// Layer 2: apply-time defensive ignore.
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "x", Type: "task", Nonce: "00000000000000000000000000000000"})
	if st.Fields["status"] != "open" {
		t.Fatalf("precondition: status %v", st.Fields["status"])
	}
	if err := applyUpdateField(st, mkEnv(id, "update_field", 10, 0, "11111111"), mustJSON(t, bad), testHash(mkEnv(id, "update_field", 10, 0, "11111111"))); err != nil {
		t.Fatalf("applyUpdateField: %v", err)
	}
	if st.Fields["status"] != "open" {
		t.Fatalf("status mutated by update_field: %v", st.Fields["status"])
	}
}

// TestClaimEarliestWins verifies §5.B.3: among multiple claim ops, the one
// with the smallest HLC tuple wins regardless of arrival order.
func TestClaimEarliestWins(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	// Apply in non-monotone order.
	if err := applyClaim(st, mkEnv(id, "claim", 200, 0, "33333333"),
		mustJSON(t, op.ClaimPayload{Assignee: "carol"}), testHash(mkEnv(id, "claim", 200, 0, "33333333"))); err != nil {
		t.Fatal(err)
	}
	if err := applyClaim(st, mkEnv(id, "claim", 50, 0, "22222222"),
		mustJSON(t, op.ClaimPayload{Assignee: "bob"}), testHash(mkEnv(id, "claim", 50, 0, "22222222"))); err != nil {
		t.Fatal(err)
	}
	if err := applyClaim(st, mkEnv(id, "claim", 100, 0, "11111111"),
		mustJSON(t, op.ClaimPayload{Assignee: "alice"}), testHash(mkEnv(id, "claim", 100, 0, "11111111"))); err != nil {
		t.Fatal(err)
	}
	if got := st.Fields["assignee"]; got != "bob" {
		t.Fatalf("assignee: %v want bob (earliest)", got)
	}
	if got := st.Fields["status"]; got != "in_progress" {
		t.Fatalf("status: %v want in_progress", got)
	}
}

// TestAcceptGrowShrink: add 3 criteria, remove the middle by index, render
// preserves the surviving two in original insertion order.
func TestAcceptGrowShrink(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	for i, c := range []string{"a", "b", "c"} {
		if err := applyAddAccept(st, mkEnv(id, "add_accept", int64(i+1), 0, "11111111"),
			mustJSON(t, op.AddAcceptPayload{Criterion: c}), testHash(mkEnv(id, "add_accept", int64(i+1), 0, "11111111"))); err != nil {
			t.Fatal(err)
		}
	}
	// Remove the middle ("b"); index 1 in the effective list.
	if err := applyRemoveAccept(st, mkEnv(id, "remove_accept", 10, 0, "11111111"),
		mustJSON(t, op.RemoveAcceptPayload{Index: 1}), testHash(mkEnv(id, "remove_accept", 10, 0, "11111111"))); err != nil {
		t.Fatal(err)
	}
	rendered := RenderState(st)
	got, _ := rendered["accept"].([]string)
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("accept after remove: %v want [a c]", got)
	}
}

// TestDepDedupKey verifies that the dedup key is (parent, edge_type) per
// §5.C.5, not parent alone. Distinct edge_types coexist; identical
// (parent, edge_type) collapses to one entry; remove takes only the exact
// matching tuple.
func TestDepDedupKey(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	add := func(wall int64, parent, edge string) {
		if err := applyAddDep(st, mkEnv(id, "add_dep", wall, 0, "11111111"),
			mustJSON(t, op.AddDepPayload{Parent: parent, EdgeType: edge}), testHash(mkEnv(id, "add_dep", wall, 0, "11111111"))); err != nil {
			t.Fatal(err)
		}
	}
	add(1, "act-aaaa", "blocks")
	add(2, "act-aaaa", "relates")
	if got := len(getDeps(st)); got != 2 {
		t.Fatalf("after 2 adds: deps=%d want 2", got)
	}
	// Re-add (act-aaaa, blocks) — must remain a single entry.
	add(3, "act-aaaa", "blocks")
	if got := len(getDeps(st)); got != 2 {
		t.Fatalf("after re-add: deps=%d want 2 (idempotent)", got)
	}
	// Remove (act-aaaa, relates) — only blocks remains.
	if err := applyRemoveDep(st, mkEnv(id, "remove_dep", 4, 0, "11111111"),
		mustJSON(t, op.RemoveDepPayload{Parent: "act-aaaa", EdgeType: "relates"}), testHash(mkEnv(id, "remove_dep", 4, 0, "11111111"))); err != nil {
		t.Fatal(err)
	}
	deps := getDeps(st)
	if len(deps) != 1 {
		t.Fatalf("after remove: deps=%d want 1", len(deps))
	}
	if deps[0]["parent"] != "act-aaaa" || deps[0]["edge_type"] != "blocks" {
		t.Fatalf("surviving dep: %v want (act-aaaa, blocks)", deps[0])
	}
}

// TestClosedTerminal confirms close pins status, but the closed issue is not
// frozen as a whole: scalar fields remain mutable via update_field. Only
// status is terminal until reopen (which is deferred).
func TestClosedTerminal(t *testing.T) {
	id := "act-aaaa"
	st := freshState(id)
	runCreate(t, st, mkEnv(id, "create", 1, 0, "11111111"),
		op.CreatePayload{Title: "old", Type: "task", Nonce: "00000000000000000000000000000000"})
	if err := applyClose(st, mkEnv(id, "close", 5, 0, "11111111"),
		mustJSON(t, op.ClosePayload{Reason: "done"}), testHash(mkEnv(id, "close", 5, 0, "11111111"))); err != nil {
		t.Fatal(err)
	}
	if !IsClosedTerminal(st) {
		t.Fatal("IsClosedTerminal: want true after close")
	}
	// Later HLC update_field on title — title updates, status stays closed.
	uf := op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(`"new"`)}
	if err := applyUpdateField(st, mkEnv(id, "update_field", 100, 0, "11111111"), mustJSON(t, uf), testHash(mkEnv(id, "update_field", 100, 0, "11111111"))); err != nil {
		t.Fatal(err)
	}
	if st.Fields["title"] != "new" {
		t.Fatalf("title not updated post-close: %v", st.Fields["title"])
	}
	if st.Fields["status"] != "closed" {
		t.Fatalf("status not closed: %v", st.Fields["status"])
	}
	resolved, _ := ResolveStatus(st)
	if resolved != "closed" {
		t.Fatalf("ResolveStatus: %q want closed", resolved)
	}
}

// TestIsClosedTerminalNil and TestResolveStatusDefaults guard the helper
// behaviour on edge inputs.
func TestIsClosedTerminalNil(t *testing.T) {
	if IsClosedTerminal(nil) {
		t.Fatal("IsClosedTerminal(nil): want false")
	}
	st := freshState("act-aaaa")
	if IsClosedTerminal(st) {
		t.Fatal("IsClosedTerminal(empty): want false")
	}
}

func TestResolveStatusDefaults(t *testing.T) {
	if s, _ := ResolveStatus(nil); s != "" {
		t.Fatalf("ResolveStatus(nil): %q want empty", s)
	}
	st := freshState("act-aaaa")
	s, _ := ResolveStatus(st)
	if s != "open" {
		t.Fatalf("ResolveStatus(empty): %q want open", s)
	}
}
