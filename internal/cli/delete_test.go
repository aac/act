package cli

import (
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/fold"
)

// TestRunDelete_HappyPath: deleting a leaf issue (no children) writes
// one tombstone op, auto-commits, exits 0, and the post-state fold
// reports the issue as tombstoned. This exercises act-g009's primary
// acceptance criterion: write a tombstone op as defined in the op-type
// table.
func TestRunDelete_HappyPath(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)

	out, code := RunDelete(root, DeleteOptions{ID: id})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(DeleteResult)
	if !ok {
		t.Fatalf("output type = %T, want DeleteResult", out)
	}
	if res.ID != id {
		t.Errorf("ID = %q, want %q", res.ID, id)
	}
	if res.OpsWritten != 1 {
		t.Errorf("OpsWritten = %d, want 1", res.OpsWritten)
	}
	if !res.Committed {
		t.Errorf("Committed = false, want true (auto-commit by default)")
	}
	if len(res.Tombstoned) != 1 || res.Tombstoned[0] != id {
		t.Errorf("Tombstoned = %v, want [%s]", res.Tombstoned, id)
	}

	// Exactly one tombstone op file under the issue's shard.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-tombstone.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 tombstone op file, got %d: %v", len(matches), matches)
	}

	// Spec line 378: subsequent reads return only the tombstone marker.
	// Verify by re-folding and checking the issue state is Tombstoned
	// (RenderState would yield nil; we check the underlying flag).
	st, ferr := fold.FoldIssue(filepath.Join(root, ".act", "ops"), id, fold.ApplyDispatch)
	if ferr != nil {
		t.Fatalf("FoldIssue: %v", ferr)
	}
	if st == nil || !st.Tombstoned {
		t.Errorf("post-delete fold: Tombstoned = false, want true")
	}
	if rendered := fold.RenderState(st); rendered != nil {
		t.Errorf("post-delete RenderState = %v, want nil per spec line 378", rendered)
	}
}

// TestRunDelete_HasDescendants: deleting an issue with a non-tombstoned
// child fails with has_descendants and exit 1 when --cascade is not
// set. The error envelope's details.descendants lists the child id.
func TestRunDelete_HasDescendants(t *testing.T) {
	root := makeCreateRepo(t)
	parentOut, code := RunCreate(root, CreateOptions{Title: "parent", Type: "epic"})
	if code != 0 {
		t.Fatalf("seed parent: code = %d", code)
	}
	parentID := parentOut.(CreateResult).ID

	childOut, code := RunCreate(root, CreateOptions{Title: "child", Type: "task", Parent: parentID})
	if code != 0 {
		t.Fatalf("seed child: code = %d", code)
	}
	childID := childOut.(CreateResult).ID

	out, code := RunDelete(root, DeleteOptions{ID: parentID})
	if code != 1 {
		t.Fatalf("code = %d, want 1; out=%+v", code, out)
	}
	errOut, ok := out.(DeleteErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want DeleteErrorOutput", out)
	}
	if errOut.Error != "has_descendants" {
		t.Errorf("Error = %q, want has_descendants", errOut.Error)
	}
	descs, ok := errOut.Details["descendants"].([]string)
	if !ok || len(descs) != 1 || descs[0] != childID {
		t.Errorf("descendants = %v, want [%s]", errOut.Details["descendants"], childID)
	}

	// No tombstone op was written: parent is still live.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", parentID, "*", "*-tombstone.json"))
	if len(matches) != 0 {
		t.Errorf("expected 0 tombstone ops on parent (delete refused), got %d: %v", len(matches), matches)
	}
}

// TestRunDelete_Cascade: --cascade walks the parent edge and tombstones
// every descendant in a single git commit. Both parent and child end
// up tombstoned in the post-state fold.
func TestRunDelete_Cascade(t *testing.T) {
	root := makeCreateRepo(t)
	parentOut, code := RunCreate(root, CreateOptions{Title: "parent", Type: "epic"})
	if code != 0 {
		t.Fatalf("seed parent: code = %d", code)
	}
	parentID := parentOut.(CreateResult).ID

	childOut, code := RunCreate(root, CreateOptions{Title: "child", Type: "task", Parent: parentID})
	if code != 0 {
		t.Fatalf("seed child: code = %d", code)
	}
	childID := childOut.(CreateResult).ID

	// Add a grandchild to verify recursion.
	grandOut, code := RunCreate(root, CreateOptions{Title: "grand", Type: "task", Parent: childID})
	if code != 0 {
		t.Fatalf("seed grand: code = %d", code)
	}
	grandID := grandOut.(CreateResult).ID

	out, code := RunDelete(root, DeleteOptions{ID: parentID, Cascade: true})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(DeleteResult)
	if !ok {
		t.Fatalf("output type = %T, want DeleteResult", out)
	}
	if res.OpsWritten != 3 {
		t.Errorf("OpsWritten = %d, want 3 (parent + child + grand)", res.OpsWritten)
	}
	want := []string{childID, grandID, parentID}
	sort.Strings(want)
	if len(res.Tombstoned) != 3 {
		t.Fatalf("Tombstoned = %v, want %v", res.Tombstoned, want)
	}
	got := strings.Join(res.Tombstoned, ",")
	wantStr := strings.Join(want, ",")
	if got != wantStr {
		t.Errorf("Tombstoned (sorted) = %s, want %s", got, wantStr)
	}

	// Single git commit batches all three tombstones using the canonical
	// batch subject `act-op: (act-XXXX) tombstone +N` (count of *extra*
	// ops beyond the head). 3 ops => `+2`.
	subj := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "-1", "--format=%s"))
	if !strings.HasPrefix(subj, "act-op: (") {
		t.Errorf("commit subject %q missing canonical 'act-op: (act-XXXX)' prefix", subj)
	}
	if !strings.Contains(subj, " tombstone +2") {
		t.Errorf("commit subject %q missing ' tombstone +2' (3 cascaded ops)", subj)
	}

	// Verify each id is tombstoned in the post-state fold.
	for _, id := range want {
		st, ferr := fold.FoldIssue(filepath.Join(root, ".act", "ops"), id, fold.ApplyDispatch)
		if ferr != nil {
			t.Fatalf("FoldIssue %s: %v", id, ferr)
		}
		if st == nil || !st.Tombstoned {
			t.Errorf("post-cascade fold %s: Tombstoned = false, want true", id)
		}
	}
}

// TestRunDelete_AlreadyDeleted: re-deleting an already-tombstoned issue
// is an idempotent no-op (exit 0, ops_written=0, no second tombstone
// file written).
func TestRunDelete_AlreadyDeleted(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)

	if _, code := RunDelete(root, DeleteOptions{ID: id}); code != 0 {
		t.Fatalf("first delete: code = %d", code)
	}
	out, code := RunDelete(root, DeleteOptions{ID: id})
	if code != 0 {
		t.Fatalf("second delete: code = %d, out=%+v", code, out)
	}
	res, ok := out.(DeleteResult)
	if !ok {
		t.Fatalf("output type = %T, want DeleteResult", out)
	}
	if res.OpsWritten != 0 {
		t.Errorf("OpsWritten = %d, want 0 (idempotent)", res.OpsWritten)
	}
	if res.Committed {
		t.Errorf("Committed = true, want false (no op written)")
	}

	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-tombstone.json"))
	if len(matches) != 1 {
		t.Errorf("expected exactly 1 tombstone op (idempotent), got %d: %v", len(matches), matches)
	}
}

// TestRunDelete_DanglingDepDoctor: spec line 378 + act-g009 acceptance:
// doctor's dangling-deps must continue to flag deps pointing at a
// tombstoned id. Build issue A blocked-by B, then tombstone B; doctor
// should report dangling-deps on A.
func TestRunDelete_DanglingDepDoctor(t *testing.T) {
	root := makeCreateRepo(t)
	bOut, code := RunCreate(root, CreateOptions{Title: "B", Type: "task"})
	if code != 0 {
		t.Fatalf("seed B: code = %d", code)
	}
	bID := bOut.(CreateResult).ID
	aOut, code := RunCreate(root, CreateOptions{Title: "A", Type: "task"})
	if code != 0 {
		t.Fatalf("seed A: code = %d", code)
	}
	aID := aOut.(CreateResult).ID

	if _, code := RunDepAdd(root, DepAddOptions{Child: aID, Parent: bID, EdgeType: "blocks"}); code != 0 {
		t.Fatalf("seed dep A->B: code = %d", code)
	}
	if _, code := RunDelete(root, DeleteOptions{ID: bID}); code != 0 {
		t.Fatalf("delete B: code = %d", code)
	}

	out, code := RunDoctor(root, DoctorOptions{})
	// Doctor's exit code is non-zero on findings; we only care that the
	// dangling-deps finding mentions A and B.
	if code == 0 {
		t.Fatalf("expected doctor non-zero exit on dangling-deps; out=%+v", out)
	}
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}
	found := false
	for _, f := range res.Findings {
		if f.Check == "dangling-deps" && f.IssueID == aID && strings.Contains(f.Message, bID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("doctor: no dangling-deps finding for %s -> %s; findings=%+v", aID, bID, res.Findings)
	}
}
