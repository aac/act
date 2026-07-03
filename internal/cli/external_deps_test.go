package cli

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/fold"
)

// addExt attaches external refs to an issue via the unified add surface
// `act dep add <id> --external <ref>` (act-ce1427), asserting success.
func addExt(t *testing.T, root, id string, refs ...string) {
	t.Helper()
	_, code := RunDepAddExternal(root, id, refs, DepAddOptions{})
	if code != 0 {
		t.Fatalf("dep add --external %v: code = %d", refs, code)
	}
}

// TestDepAddExternal_WritesOp: a single --external writes one
// add_external_dep op file and the rendered state surfaces external_deps.
func TestDepAddExternal_WritesOp(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunDepAddExternal(root, id, []string{"linear:ENG-42"}, DepAddOptions{})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res := out.(DepAddExternalResult)
	if !res.Committed || res.Issue != id || len(res.External) != 1 {
		t.Errorf("unexpected result: %+v", res)
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

// TestDepAddExternal_Idempotent: re-adding the same ref produces a second
// op file (audit trail), but the folded state still shows one entry.
func TestDepAddExternal_Idempotent(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	addExt(t, root, id, "gh:owner/repo#7")
	addExt(t, root, id, "gh:owner/repo#7")
	state, err := fold.FoldIssue(filepath.Join(root, ".act", "ops"), id, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	refs, _ := fold.RenderState(state)["external_deps"].([]string)
	if len(refs) != 1 {
		t.Errorf("after duplicate add, external_deps = %v, want one entry", refs)
	}
}

// TestRunUpdate_ExtRmClears: --ext-rm (still on `act update`) clears a
// ref added via `act dep add --external`.
func TestRunUpdate_ExtRmClears(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	addExt(t, root, id, "jira:PROJ-1")
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

// TestDepAddExternal_Multi: two refs in one call → two ops.
func TestDepAddExternal_Multi(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	addExt(t, root, id, "ref-a", "ref-b")
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

// TestDepAddExternal_UnknownSubject: an unresolvable subject id is exit 3.
func TestDepAddExternal_UnknownSubject(t *testing.T) {
	root, _ := makeUpdateRepoWithIssue(t)
	if _, code := RunDepAddExternal(root, "act-zzzzzz", []string{"ref"}, DepAddOptions{}); code != 3 {
		t.Fatalf("unknown subject: code = %d, want 3", code)
	}
}

// TestDepAddExternal_BadRef: payload validation rejects empty refs at the
// CLI boundary, with exit 2 (bad flag) and no op file written.
func TestDepAddExternal_BadRef(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunDepAddExternal(root, id, []string{""}, DepAddOptions{})
	if code != 2 {
		t.Fatalf("expected exit 2 for empty ref, got code %d; out=%+v", code, out)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-add_external_dep.json"))
	if len(matches) != 0 {
		t.Errorf("op should not be written on validation failure; got %v", matches)
	}
}

// TestDepAddExternal_BadRefControlChar: refs with control characters are
// rejected for the same reason — protects against accidental paste of a
// multi-line "id" or a binary blob.
func TestDepAddExternal_BadRefControlChar(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	if _, code := RunDepAddExternal(root, id, []string{"bad\nref"}, DepAddOptions{}); code != 2 {
		t.Fatalf("expected exit 2 for control char in ref, got code %d", code)
	}
}

// TestRunReady_ExternalDepExcludes: an open issue with at least one external
// dep is excluded from `act ready`. After clearing the ref the issue
// reappears in the ready set.
func TestRunReady_ExternalDepExcludes(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	addExt(t, root, id, "upstream:42")
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
	addExt(t, root, id, "src-of-truth-1")
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

// TestUpdate_RequiresAtLeastOneMutatingFlag_IncludesExtRm: the "no mutating
// flag" error still surfaces --ext-rm (the removal surface that stays on
// `act update`) and no longer advertises the removed --ext-add.
func TestUpdate_RequiresAtLeastOneMutatingFlag_IncludesExtRm(t *testing.T) {
	root, id := makeUpdateRepoWithIssue(t)
	out, code := RunUpdate(root, UpdateOptions{ID: id})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	msg := out.(UpdateErrorOutput).Message
	if !contains(msg, "--ext-rm") {
		t.Errorf("error message missing --ext-rm: %q", msg)
	}
	if contains(msg, "--ext-add") {
		t.Errorf("error message should no longer advertise --ext-add: %q", msg)
	}
}
