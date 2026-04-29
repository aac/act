package cli

import (
	"path/filepath"
	"testing"
)

// makeDepAddRepoWithIssues seeds a fresh repo + .act/ with two `create`
// ops (synthesised via RunCreate) and returns (repoRoot, idA, idB).
//
// The two ids are guaranteed distinct because RunCreate derives them
// from a fresh nonce on each call. Both creates auto-commit so the
// tree is clean before RunDepAdd is exercised.
func makeDepAddRepoWithIssues(t *testing.T) (string, string, string) {
	t.Helper()
	root := makeCreateRepo(t)
	outA, code := RunCreate(root, CreateOptions{Title: "A", Type: "task"})
	if code != 0 {
		t.Fatalf("seed A: code = %d, out=%+v", code, outA)
	}
	outB, code := RunCreate(root, CreateOptions{Title: "B", Type: "task"})
	if code != 0 {
		t.Fatalf("seed B: code = %d, out=%+v", code, outB)
	}
	return root, outA.(CreateResult).ID, outB.(CreateResult).ID
}

// TestRunDepAdd_HappyPath: a fresh blocks edge between two unrelated
// issues writes exactly one add_dep op file and exits 0.
func TestRunDepAdd_HappyPath(t *testing.T) {
	root, a, b := makeDepAddRepoWithIssues(t)

	out, code := RunDepAdd(root, DepAddOptions{
		Child:    a,
		Parent:   b,
		EdgeType: "blocks",
	})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(DepAddResult)
	if !ok {
		t.Fatalf("output type = %T, want DepAddResult", out)
	}
	if !res.OK || res.Child != a || res.Parent != b || res.EdgeType != "blocks" {
		t.Errorf("unexpected result: %+v", res)
	}
	if !res.Committed {
		t.Errorf("Committed = false, want true (auto-commit on by default)")
	}

	// Exactly one add_dep op file under the child's shard.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", a, "*", "*-add_dep.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 add_dep op file, got %d: %v", len(matches), matches)
	}
}

// TestRunDepAdd_Idempotent: re-issuing the same call after a successful
// dep-add returns exit 0 with committed=false and writes no second op.
func TestRunDepAdd_Idempotent(t *testing.T) {
	root, a, b := makeDepAddRepoWithIssues(t)

	if _, code := RunDepAdd(root, DepAddOptions{Child: a, Parent: b, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("first call: code = %d", code)
	}
	out, code := RunDepAdd(root, DepAddOptions{Child: a, Parent: b, EdgeType: "blocks"})
	if code != 0 {
		t.Fatalf("second call: code = %d, out=%+v", code, out)
	}
	res, ok := out.(DepAddResult)
	if !ok {
		t.Fatalf("output type = %T, want DepAddResult", out)
	}
	if res.Committed {
		t.Errorf("Committed = true on idempotent call, want false")
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", a, "*", "*-add_dep.json"))
	if len(matches) != 1 {
		t.Errorf("expected exactly 1 add_dep op (idempotent), got %d: %v", len(matches), matches)
	}
}

// TestRunDepAdd_DifferentEdgeTypes: per §5.C.5, the dedup key is
// (child, parent, edge_type). A second call with a different --type
// MUST produce a new op even when the (child, parent) pair already
// has an edge of another type.
func TestRunDepAdd_DifferentEdgeTypes(t *testing.T) {
	root, a, b := makeDepAddRepoWithIssues(t)

	if _, code := RunDepAdd(root, DepAddOptions{Child: a, Parent: b, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("blocks call: code = %d", code)
	}
	if _, code := RunDepAdd(root, DepAddOptions{Child: a, Parent: b, EdgeType: "relates"}); code != 0 {
		t.Fatalf("relates call: code = %d", code)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", a, "*", "*-add_dep.json"))
	if len(matches) != 2 {
		t.Errorf("expected 2 add_dep ops (one per edge type), got %d: %v", len(matches), matches)
	}
}

// TestRunDepAdd_CycleDetected: A blocks B, B blocks C; adding C blocks A
// closes a cycle in the blocks subgraph and must exit 1.
func TestRunDepAdd_CycleDetected(t *testing.T) {
	root := makeCreateRepo(t)
	aOut, _ := RunCreate(root, CreateOptions{Title: "A", Type: "task"})
	bOut, _ := RunCreate(root, CreateOptions{Title: "B", Type: "task"})
	cOut, _ := RunCreate(root, CreateOptions{Title: "C", Type: "task"})
	a := aOut.(CreateResult).ID
	b := bOut.(CreateResult).ID
	c := cOut.(CreateResult).ID

	// A blocks B
	if _, code := RunDepAdd(root, DepAddOptions{Child: a, Parent: b, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("A blocks B: code = %d", code)
	}
	// B blocks C
	if _, code := RunDepAdd(root, DepAddOptions{Child: b, Parent: c, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("B blocks C: code = %d", code)
	}
	// C blocks A — would close A→B→C→A.
	out, code := RunDepAdd(root, DepAddOptions{Child: c, Parent: a, EdgeType: "blocks"})
	if code != 1 {
		t.Fatalf("C blocks A: code = %d, want 1; out=%+v", code, out)
	}
	cyc, ok := out.(DepAddCycleOutput)
	if !ok {
		t.Fatalf("output type = %T, want DepAddCycleOutput", out)
	}
	if cyc.Error.Kind != "cycle" {
		t.Errorf("error.kind = %q, want cycle", cyc.Error.Kind)
	}
	if len(cyc.Error.Path) < 2 {
		t.Errorf("path too short: %v", cyc.Error.Path)
	}
	// First and last nodes must coincide on the would-be edge child (c).
	if cyc.Error.Path[0] != c || cyc.Error.Path[len(cyc.Error.Path)-1] != c {
		t.Errorf("path %v: must start and end with %s", cyc.Error.Path, c)
	}
}

// TestRunDepAdd_UnknownChild: an unresolvable child id is exit 3.
func TestRunDepAdd_UnknownChild(t *testing.T) {
	root, _, b := makeDepAddRepoWithIssues(t)
	out, code := RunDepAdd(root, DepAddOptions{
		Child:    "act-deadbeef",
		Parent:   b,
		EdgeType: "blocks",
	})
	if code != 3 {
		t.Fatalf("code = %d, want 3; out=%+v", code, out)
	}
	e, ok := out.(DepAddErrorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q, want issue_not_found", e.Error)
	}
}

// TestRunDepAdd_UnknownParent: an unresolvable parent id is exit 3.
func TestRunDepAdd_UnknownParent(t *testing.T) {
	root, a, _ := makeDepAddRepoWithIssues(t)
	out, code := RunDepAdd(root, DepAddOptions{
		Child:    a,
		Parent:   "act-deadbeef",
		EdgeType: "blocks",
	})
	if code != 3 {
		t.Fatalf("code = %d, want 3; out=%+v", code, out)
	}
	e, ok := out.(DepAddErrorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q, want issue_not_found", e.Error)
	}
}

// TestRunDepAdd_BadEdgeType: an unknown --type value is exit 2.
func TestRunDepAdd_BadEdgeType(t *testing.T) {
	root, a, b := makeDepAddRepoWithIssues(t)
	out, code := RunDepAdd(root, DepAddOptions{
		Child:    a,
		Parent:   b,
		EdgeType: "duplicates",
	})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
	e, ok := out.(DepAddErrorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "bad_flag" {
		t.Errorf("error = %q, want bad_flag", e.Error)
	}
}

// TestRunDepAdd_DefaultEdgeType: empty EdgeType normalises to "blocks".
func TestRunDepAdd_DefaultEdgeType(t *testing.T) {
	root, a, b := makeDepAddRepoWithIssues(t)
	out, code := RunDepAdd(root, DepAddOptions{Child: a, Parent: b})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, _ := out.(DepAddResult)
	if res.EdgeType != "blocks" {
		t.Errorf("edge_type = %q, want blocks", res.EdgeType)
	}
}
