package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/fold"
)

// TestRunUpdate_ExtAddWritesOp: a single --ext-add writes one
// add_external_dep op file and the rendered state surfaces external_deps.
func TestRunUpdate_ExtAddWritesOp(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:     id,
		ExtAdd: []string{"linear:ENG-42"},
	})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res := out.(UpdateResult)
	if res.OpsWritten != 1 {
		t.Errorf("OpsWritten = %d, want 1", res.OpsWritten)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-add_external_dep.json"))
	if len(matches) != 1 {
		t.Fatalf("want 1 add_external_dep op, got %d: %v", len(matches), matches)
	}

	state, err := fold.FoldIssue(filepath.Join(root, ".act", "ops"), id, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	rendered := fold.RenderState(state)
	refs, _ := rendered["external_deps"].([]string)
	if len(refs) != 1 || refs[0] != "linear:ENG-42" {
		t.Errorf("external_deps = %v, want [linear:ENG-42]", rendered["external_deps"])
	}
}

// TestRunUpdate_ExtAddIdempotent: re-adding the same ref produces a second
// op file (audit trail), but the folded state still shows one entry.
func TestRunUpdate_ExtAddIdempotent(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	if _, code := RunUpdate(root, UpdateOptions{ID: id, ExtAdd: []string{"gh:owner/repo#7"}}); code != 0 {
		t.Fatalf("first add: code = %d", code)
	}
	if _, code := RunUpdate(root, UpdateOptions{ID: id, ExtAdd: []string{"gh:owner/repo#7"}}); code != 0 {
		t.Fatalf("second add: code = %d", code)
	}
	state, err := fold.FoldIssue(filepath.Join(root, ".act", "ops"), id, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	refs, _ := fold.RenderState(state)["external_deps"].([]string)
	if len(refs) != 1 {
		t.Errorf("after duplicate add, external_deps = %v, want one entry", refs)
	}
}

// TestRunUpdate_ExtRmClears: --ext-rm clears a previously-added ref.
func TestRunUpdate_ExtRmClears(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	if _, code := RunUpdate(root, UpdateOptions{ID: id, ExtAdd: []string{"jira:PROJ-1"}}); code != 0 {
		t.Fatalf("add: code = %d", code)
	}
	if _, code := RunUpdate(root, UpdateOptions{ID: id, ExtRm: []string{"jira:PROJ-1"}}); code != 0 {
		t.Fatalf("rm: code = %d", code)
	}
	state, err := fold.FoldIssue(filepath.Join(root, ".act", "ops"), id, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	refs, _ := fold.RenderState(state)["external_deps"].([]string)
	if len(refs) != 0 {
		t.Errorf("after rm, external_deps = %v, want empty", refs)
	}
}

// TestRunUpdate_ExtRmAbsentIsNoop: clearing a ref the issue doesn't have
// succeeds (idempotent absence) — no dep_not_found error.
func TestRunUpdate_ExtRmAbsentIsNoop(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{ID: id, ExtRm: []string{"ghost-ref"}})
	if code != 0 {
		t.Fatalf("rm absent: code = %d, out=%+v", code, out)
	}
	res := out.(UpdateResult)
	if res.OpsWritten != 1 {
		t.Errorf("OpsWritten = %d, want 1", res.OpsWritten)
	}
}

// TestRunUpdate_ExtAddMulti: two refs in one call → two ops.
func TestRunUpdate_ExtAddMulti(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	if _, code := RunUpdate(root, UpdateOptions{
		ID:     id,
		ExtAdd: []string{"ref-a", "ref-b"},
	}); code != 0 {
		t.Fatalf("multi add: code = %d", code)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-add_external_dep.json"))
	if len(matches) != 2 {
		t.Errorf("want 2 add_external_dep ops, got %d", len(matches))
	}
	state, err := fold.FoldIssue(filepath.Join(root, ".act", "ops"), id, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	refs, _ := fold.RenderState(state)["external_deps"].([]string)
	if len(refs) != 2 {
		t.Errorf("external_deps = %v, want 2 entries", refs)
	}
}

// TestRunUpdate_ExtClaimConflict: --ext-add with --claim → exit 2.
func TestRunUpdate_ExtClaimConflict(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:     id,
		Claim:  true,
		ExtAdd: []string{"ref"},
	})
	if code != 2 {
		t.Fatalf("code = %d, want 2; out=%+v", code, out)
	}
}

// TestRunUpdate_ExtAddBadRef: payload validation rejects empty refs at the
// CLI boundary, with exit 2 (bad flag) and no op file written.
func TestRunUpdate_ExtAddBadRef(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{
		ID:     id,
		ExtAdd: []string{""},
	})
	if code != 2 {
		t.Fatalf("expected exit 2 for empty ref, got code %d; out=%+v", code, out)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-add_external_dep.json"))
	if len(matches) != 0 {
		t.Errorf("op should not be written on validation failure; got %v", matches)
	}
}

// TestRunUpdate_ExtAddBadRefControlChar: refs with control characters are
// rejected for the same reason — protects against accidental paste of a
// multi-line "id" or a binary blob.
func TestRunUpdate_ExtAddBadRefControlChar(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	_, code := RunUpdate(root, UpdateOptions{
		ID:     id,
		ExtAdd: []string{"bad\nref"},
	})
	if code != 2 {
		t.Fatalf("expected exit 2 for control char in ref, got code %d", code)
	}
}

// TestRunReady_ExternalDepExcludes: an open issue with at least one external
// dep is excluded from `act ready`. After clearing the ref the issue
// reappears in the ready set.
func TestRunReady_ExternalDepExcludes(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	if _, code := RunUpdate(root, UpdateOptions{ID: id, ExtAdd: []string{"upstream:42"}}); code != 0 {
		t.Fatalf("ext-add: code = %d", code)
	}
	out, code := RunReady(root, ReadyOptions{})
	if code != 0 {
		t.Fatalf("ready code = %d", code)
	}
	res := out.(ReadyResult)
	for _, r := range res.Ready {
		if r.ID == id {
			t.Fatalf("issue %s should be excluded; ready=%+v", id, res.Ready)
		}
	}

	// Clear the ref — issue should re-enter the ready set.
	if _, code := RunUpdate(root, UpdateOptions{ID: id, ExtRm: []string{"upstream:42"}}); code != 0 {
		t.Fatalf("ext-rm: code = %d", code)
	}
	out, code = RunReady(root, ReadyOptions{})
	if code != 0 {
		t.Fatalf("ready code = %d", code)
	}
	res = out.(ReadyResult)
	found := false
	for _, r := range res.Ready {
		if r.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issue %s should be ready after clear; ready=%+v", id, res.Ready)
	}
}

// TestShow_RendersExternalDep: act show JSON includes external_deps and the
// human formatter prints an external_dep line per ref.
func TestShow_RendersExternalDep(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	if _, code := RunUpdate(root, UpdateOptions{ID: id, ExtAdd: []string{"src-of-truth-1"}}); code != 0 {
		t.Fatalf("ext-add: code = %d", code)
	}
	out, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show code = %d, out=%+v", code, out)
	}
	res := out.(ShowResult)
	refs, _ := res.Fields["external_deps"].([]string)
	if len(refs) != 1 || refs[0] != "src-of-truth-1" {
		t.Errorf("Fields[external_deps] = %v, want [src-of-truth-1]", res.Fields["external_deps"])
	}
	human := FormatShowHuman(res)
	if want := "external_dep: src-of-truth-1\n"; !contains(human, want) {
		t.Errorf("human output missing %q:\n%s", want, human)
	}

	// JSON round-trip via ShowJSON preserves the slice.
	body, err := json.Marshal(res.ShowJSON())
	if err != nil {
		t.Fatalf("marshal ShowJSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, _ := decoded["external_deps"].([]any)
	if len(got) != 1 || got[0] != "src-of-truth-1" {
		t.Errorf("JSON external_deps = %v, want [src-of-truth-1]", decoded["external_deps"])
	}
}

// TestUpdate_RequiresAtLeastOneMutatingFlag_IncludesExt: error message
// surfaces --ext-add / --ext-rm in the list of acceptable flags so an agent
// reading the failure knows the new surface exists.
func TestUpdate_RequiresAtLeastOneMutatingFlag_IncludesExt(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{ID: id})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	msg := out.(UpdateErrorOutput).Message
	if !contains(msg, "--ext-add") || !contains(msg, "--ext-rm") {
		t.Errorf("error message missing ext flags: %q", msg)
	}
}
