package cli

import (
	"encoding/json"
	"sort"
	"testing"
)

// TestResolvePrefix_AmbiguousReportsCandidates pins the spec contract:
// when a user-supplied prefix matches >=2 known full ids, every
// id-resolving command MUST surface `id_ambiguous` (NOT `issue_not_found`)
// with `details.candidates[]` listing the matching full ids. The test
// reproduces the usability-review finding 3 scenario (multiple issues
// sharing a prefix that satisfies the MinShortHexLen=4 floor) and asserts
// both the in-process envelope and the on-the-wire JSON shape.
//
// Exit code: per the spec's universal error table (§1 "Error handling"),
// `id_ambiguous` lives at exit 3. The older per-section text at line 529
// said exit 2; we follow the universal table for both `id_ambiguous` and
// `issue_not_found` because consistency between two near-identical id
// failures lets agents handle them with a single branch.
func TestResolvePrefix_AmbiguousReportsCandidates(t *testing.T) {
	root := makeRepoWithAct(t)

	// Three issues whose hex tails all start with the 4-hex prefix
	// "8abc". With the prefix "8abc" shared by all three issues the
	// resolver MUST report ambiguous. Note: after act-6fca, shorter
	// prefixes like "8a" also report ambiguous (not not_found) when they
	// match multiple issues; MinInputHexLen=1 is the only floor.
	envA := makeShowCreateEnv(t, "act-8abc1234", 1700000000000, 0, "alpha")
	envB := makeShowCreateEnv(t, "act-8abc5678", 1700000000001, 0, "bravo")
	envC := makeShowCreateEnv(t, "act-8abcfeed", 1700000000002, 0, "charlie")
	writeOpFile(t, root, envA, "2026-04", "a.json")
	writeOpFile(t, root, envB, "2026-04", "b.json")
	writeOpFile(t, root, envC, "2026-04", "c.json")

	// `act show act-8abc` -------------------------------------------------
	out, code := RunShow(root, ShowOptions{ID: "act-8abc"})
	if code != 3 {
		t.Fatalf("show: exit code = %d, want 3", code)
	}
	e, ok := out.(ShowErrorOutput)
	if !ok {
		t.Fatalf("show: output type = %T, want ShowErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Fatalf("show: error = %q, want id_ambiguous", e.Error)
	}
	if got := len(e.Candidates); got != 3 {
		t.Fatalf("show: candidates len = %d, want 3", got)
	}
	got := append([]string(nil), e.Candidates...)
	sort.Strings(got)
	want := []string{"act-8abc1234", "act-8abc5678", "act-8abcfeed"}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("show: candidate[%d] = %q, want %q", i, got[i], id)
		}
	}

	// JSON shape: `details.candidates[]` MUST be present (spec §1).
	body, jerr := json.Marshal(e)
	if jerr != nil {
		t.Fatalf("show: marshal: %v", jerr)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("show: unmarshal: %v", err)
	}
	if decoded["error"] != "id_ambiguous" {
		t.Errorf("show JSON: error = %v, want id_ambiguous", decoded["error"])
	}
	details, ok := decoded["details"].(map[string]any)
	if !ok {
		t.Fatalf("show JSON: details missing or not an object: %v", decoded["details"])
	}
	if details["prefix"] != "act-8abc" {
		t.Errorf("show JSON: details.prefix = %v, want act-8abc", details["prefix"])
	}
	cands, ok := details["candidates"].([]any)
	if !ok {
		t.Fatalf("show JSON: details.candidates missing or not array: %v", details["candidates"])
	}
	if len(cands) != 3 {
		t.Errorf("show JSON: details.candidates len = %d, want 3", len(cands))
	}

	// `act log act-8abc` --------------------------------------------------
	lout, lcode := RunLog(root, "act-8abc", false)
	if lcode != 3 {
		t.Fatalf("log: exit code = %d, want 3", lcode)
	}
	le, ok := lout.(LogErrorOutput)
	if !ok {
		t.Fatalf("log: output type = %T, want LogErrorOutput", lout)
	}
	if le.Error != "id_ambiguous" {
		t.Errorf("log: error = %q, want id_ambiguous", le.Error)
	}
	if len(le.Candidates) != 3 {
		t.Errorf("log: candidates len = %d, want 3", len(le.Candidates))
	}

	// Negative control: a prefix that matches none MUST still report
	// `issue_not_found` (not `id_ambiguous`), with empty/no candidates.
	nout, ncode := RunShow(root, ShowOptions{ID: "act-ffff"})
	if ncode != 3 {
		t.Fatalf("show miss: exit code = %d, want 3", ncode)
	}
	ne, ok := nout.(ShowErrorOutput)
	if !ok {
		t.Fatalf("show miss: output type = %T", nout)
	}
	if ne.Error != "issue_not_found" {
		t.Errorf("show miss: error = %q, want issue_not_found", ne.Error)
	}
	if len(ne.Candidates) != 0 {
		t.Errorf("show miss: candidates len = %d, want 0", len(ne.Candidates))
	}
}
