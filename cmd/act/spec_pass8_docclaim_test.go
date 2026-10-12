package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Doc-claim cover for the eighth stale pass over docs/spec.md (act-0c1c31),
// asserted against the real binary. It builds on the sixth and seventh
// passes' helpers (spec_pass6_docclaim_test.go, spec_pass7_docclaim_test.go).
//
// What was stale: 22 flags a Synopsis named had no flag bullet; `act init`
// was said to always emit JSON and to exit 2 on an existing tracker; three
// passages showed a `"kind"` key the binary never writes; the init, list and
// search JSON examples showed keys the binary does not emit; the error
// table's message column quoted wording the binary does not print; the
// `--sort` bullet named only the `:asc`/`:desc` suffix; and `act help errors`
// and the error table listed fewer codes than the binary emits.

// sp8ListIDs runs `act list --json <args>` and returns the ids in order.
func sp8ListIDs(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, append([]string{"list", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("act list --json %v: exit %d\nstdout=%s\nstderr=%s", args, code, out, stderr)
	}
	var res struct {
		Issues []struct {
			ID string `json:"id"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("act list --json %v: %v\n%s", args, err, out)
	}
	ids := []string{}
	for _, i := range res.Issues {
		ids = append(ids, i.ID)
	}
	return ids
}

// TestDocClaim_Spec_ListSortSyntax asserts the `--sort` bullet of the `act
// list` section: keys are comma-separated; a bare key is ascending; `-key`
// and `key:desc` are descending; `+key` and `key:asc` are the bare key; the
// default is `priority,-created_at`; `id` ascending is the last key unless
// the keys name `id`; an unknown field, an unknown direction or an empty key
// is exit 2.
func TestDocClaim_Spec_ListSortSyntax(t *testing.T) {
	dir := sp6Site(t)
	// Three issues: priorities 2, 1, 1, created in that order.
	p2 := createIssue(t, dir, "sp8 p2", "--priority", "2")
	p1old := createIssue(t, dir, "sp8 p1 old", "--priority", "1")
	p1new := createIssue(t, dir, "sp8 p1 new", "--priority", "1")
	lo, hi := p1old, p1new // the two p1 ids in id order
	if lo > hi {
		lo, hi = hi, lo
	}
	eq := func(what string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: order %v, want %v", what, got, want)
		}
	}

	// Default is priority,-created_at: p1 before p2, newest first inside p1.
	eq("no --sort", sp8ListIDs(t, dir), p1new, p1old, p2)
	eq("--sort priority,-created_at", sp8ListIDs(t, dir, "--sort", "priority,-created_at"), p1new, p1old, p2)

	// A bare key is ascending; `id` is the added last key.
	eq("--sort priority", sp8ListIDs(t, dir, "--sort", "priority"), lo, hi, p2)
	eq("--sort created_at", sp8ListIDs(t, dir, "--sort", "created_at"), p2, p1old, p1new)

	// `-key` and `key:desc` are the same descending sort.
	eq("--sort -created_at", sp8ListIDs(t, dir, "--sort", "-created_at"), p1new, p1old, p2)
	eq("--sort created_at:desc", sp8ListIDs(t, dir, "--sort", "created_at:desc"), p1new, p1old, p2)
	eq("--sort -priority", sp8ListIDs(t, dir, "--sort", "-priority"), p2, lo, hi)
	eq("--sort priority:desc", sp8ListIDs(t, dir, "--sort", "priority:desc"), p2, lo, hi)

	// `+key` and `key:asc` are the bare key.
	eq("--sort +created_at", sp8ListIDs(t, dir, "--sort", "+created_at"), p2, p1old, p1new)
	eq("--sort created_at:asc", sp8ListIDs(t, dir, "--sort", "created_at:asc"), p2, p1old, p1new)

	// Several keys, and naming `id` replaces the added tie-breaker.
	eq("--sort priority,created_at", sp8ListIDs(t, dir, "--sort", "priority,created_at"), p1old, p1new, p2)
	eq("--sort priority,-id", sp8ListIDs(t, dir, "--sort", "priority,-id"), hi, lo, p2)
	eq("--sort priority:asc,id:desc", sp8ListIDs(t, dir, "--sort", "priority:asc,id:desc"), hi, lo, p2)

	// The four fields, and nothing else.
	for _, f := range []string{"priority", "created_at", "closed_at", "id"} {
		pass5Run(t, dir, 0, "", "list", "--json", "--sort", f)
	}
	for _, bad := range []string{"sp8-no-such-field", "title", "priority:sideways", "priority,,id", "priority,"} {
		pass5Run(t, dir, 2, "bad_flag", "list", "--json", "--sort", bad)
	}
}
