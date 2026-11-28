package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// act-59175a: `act list --under <id>` lists an issue's descendants at any
// depth, under list's status filters. The caller this exists for asks "does
// this epic still have ANY open descendant?" — a question `act ready
// --under` cannot answer (it sees only ready rows) and that used to cost one
// `act show` per open ticket. Asserted at the subprocess boundary.

// listUnderCreate files an issue (optionally under parent) and returns its id.
func listUnderCreate(t *testing.T, dir, title, parent string) string {
	t.Helper()
	args := []string{"create", title, "--json"}
	if parent != "" {
		args = append(args, "--parent", parent)
	}
	out, stderr, code := runActIn(t, dir, args...)
	if code != 0 {
		t.Fatalf("act %s: exit %d; stdout=%s stderr=%s", strings.Join(args, " "), code, out, stderr)
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.ID == "" {
		t.Fatalf("act create: no id in %s (%v)", out, err)
	}
	return res.ID
}

// listUnderIDs runs `act list <args> --json` and returns the listed ids,
// sorted, failing on a non-zero exit.
func listUnderIDs(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	full := append([]string{"list", "--json"}, args...)
	out, stderr, code := runActIn(t, dir, full...)
	if code != 0 {
		t.Fatalf("act %s: exit %d; stdout=%s stderr=%s", strings.Join(full, " "), code, out, stderr)
	}
	var res struct {
		Issues []struct {
			ID string `json:"id"`
		} `json:"issues"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("act %s: bad JSON %s: %v", strings.Join(full, " "), out, err)
	}
	ids := make([]string, 0, len(res.Issues))
	for _, it := range res.Issues {
		ids = append(ids, it.ID)
	}
	if res.Total != len(ids) {
		t.Errorf("act %s: total %d != %d rows (no limit hit)", strings.Join(full, " "), res.Total, len(ids))
	}
	sort.Strings(ids)
	return ids
}

func listUnderSorted(ids ...string) []string {
	out := append([]string(nil), ids...)
	sort.Strings(out)
	return out
}

// listUnderSite builds epic E, child C (closed), open grandchild G under C,
// and an unrelated open issue O.
func listUnderSite(t *testing.T) (dir, e, c, g, o string) {
	t.Helper()
	dir = pass5Site(t)
	e = listUnderCreate(t, dir, "epic E", "")
	c = listUnderCreate(t, dir, "child C", e)
	g = listUnderCreate(t, dir, "grandchild G", c)
	o = listUnderCreate(t, dir, "outside O", "")
	if _, stderr, code := runActIn(t, dir, "close", c, "--reason", "done"); code != 0 {
		t.Fatalf("act close %s: exit %d; stderr=%s", c, code, stderr)
	}
	return dir, e, c, g, o
}

// TestDocClaim_ListUnder_ClosedChildLinksOpenGrandchild is the ticket's
// accept case: with a closed child C between them, the open grandchild G is
// still a descendant of E, and C (closed) stays out of the default listing.
func TestDocClaim_ListUnder_ClosedChildLinksOpenGrandchild(t *testing.T) {
	dir, e, c, g, _ := listUnderSite(t)

	if got, want := listUnderIDs(t, dir, "--under", e), []string{g}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("list --under E: got %v, want %v (G only: not C, not E, not O)", got, want)
	}
	// Composes with --all and --status.
	if got, want := listUnderIDs(t, dir, "--under", e, "--all"), listUnderSorted(c, g); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("list --under E --all: got %v, want %v", got, want)
	}
	if got, want := listUnderIDs(t, dir, "--under", e, "--status", "closed"), []string{c}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("list --under E --status closed: got %v, want %v", got, want)
	}
	// A prefix resolves like any other id argument.
	if got, want := listUnderIDs(t, dir, "--under", e[:len("act-")+4]), []string{g}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("list --under <prefix of E>: got %v, want %v", got, want)
	}
	// A leaf has no descendants: empty, exit 0.
	if got := listUnderIDs(t, dir, "--under", g); len(got) != 0 {
		t.Errorf("list --under G (a leaf): got %v, want none", got)
	}
}

// TestDocClaim_ListUnder_ExitCodes asserts the --under clauses of the spec's
// `act list` exit-code line.
func TestDocClaim_ListUnder_ExitCodes(t *testing.T) {
	dir, e, _, _, _ := listUnderSite(t)
	pass5Run(t, dir, 3, "issue_not_found", "list", "--json", "--under", pass5NoSuchID)
	pass5Run(t, dir, 2, "id_ambiguous", "list", "--json", "--under", pass5Twin(t, dir, e))
}

// TestDocClaim_ListUnder_FlagHelp asserts `act list -h` advertises --under.
func TestDocClaim_ListUnder_FlagHelp(t *testing.T) {
	dir := pass5Site(t)
	_, stderr, _ := runActIn(t, dir, "list", "-h")
	if !strings.Contains(stderr, "restrict to descendants of the given issue id (prefix ok), at any depth") {
		t.Errorf("act list -h does not describe --under:\n%s", stderr)
	}
}

// TestDocClaim_ListUnder_HelpWorkflow asserts the `act help workflow`
// paragraph is what the command prints.
func TestDocClaim_ListUnder_HelpWorkflow(t *testing.T) {
	dir := pass5Site(t)
	out, stderr, code := runActIn(t, dir, "help", "workflow")
	if code != 0 {
		t.Fatalf("act help workflow: exit %d; stderr=%s", code, stderr)
	}
	if !strings.Contains(out+stderr, "'act list --under <id>' scopes the listing to the descendants of") {
		t.Errorf("act help workflow lacks the --under paragraph:\n%s", out)
	}
}

// TestDocClaim_ListUnder_MCP asserts act_list's `under` argument scopes the
// MCP listing the same way.
func TestDocClaim_ListUnder_MCP(t *testing.T) {
	dir, e, c, g, o := listUnderSite(t)
	call := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act_list","arguments":{"under":%q}}}`, e)
	out, stderr, _ := pass5MCP(t, dir, nil, pass5MCPInit, call)
	if !strings.Contains(out, g) {
		t.Errorf("act_list under=E omits grandchild %s:\nstdout=%s\nstderr=%s", g, out, stderr)
	}
	for _, id := range []string{c, o} {
		if strings.Contains(out, id) {
			t.Errorf("act_list under=E lists %s, want only %s:\n%s", id, g, out)
		}
	}
}
