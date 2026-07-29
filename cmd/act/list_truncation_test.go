package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// act-b50d81: `act list` capped its output at 200 rows and said nothing.
// Because the cap lands after the sort, the dropped rows are systematically
// the low-priority ones, so a caller piping `act list` into a filter gets a
// confident under-count of open work. It closed a p1 ticket on a wrong
// number the night it was found.
//
// These tests assert at the subprocess boundary — the surface the incident
// actually happened on — rather than on ListResult internals, per the repo's
// documentation-discipline rule.

// seedIssues creates n issues in dir and returns their ids.
func seedIssues(t *testing.T, dir string, n int) []string {
	t.Helper()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, createBlocksIssue(t, dir, fmt.Sprintf("truncation probe %02d", i)))
	}
	return ids
}

// TestDocClaim_List_CappedListingWarnsOnStderr pins the `--limit` flag-help
// claim that "A capped listing prints a WARNING to stderr naming how many
// issues were hidden."
//
// The stderr placement is the load-bearing part: the incident was
// `act list | <filter>`, where a stdout trailer is swallowed by the pipe.
// This test asserts the notice is on stderr AND absent from stdout, so a
// future "tidy up by moving it into the row stream" would break here.
func TestDocClaim_List_CappedListingWarnsOnStderr(t *testing.T) {
	dir := blocksSite(t)
	seedIssues(t, dir, 5)

	stdout, stderr, code := runActIn(t, dir, "list", "--limit", "2")
	if code != 0 {
		t.Fatalf("act list --limit 2: exit %d; stderr=%s", code, stderr)
	}

	rows := linesOf(stdout)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows on stdout, got %d:\n%s", len(rows), stdout)
	}

	// The human-unmissable part: a WARNING naming both numbers and the
	// count that was hidden.
	for _, want := range []string{"WARNING", "showing 2 of 5", "3 hidden"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q; got:\n%s", want, stderr)
		}
	}
	// And an escape hatch the reader can act on immediately.
	if !strings.Contains(stderr, "--limit 0") {
		t.Errorf("stderr should point at `--limit 0`; got:\n%s", stderr)
	}
	// The notice must NOT be in the row stream: a pipe consumer counting
	// lines would then count the warning as an issue.
	if strings.Contains(stdout, "WARNING") {
		t.Errorf("truncation notice leaked onto stdout, corrupting the row stream:\n%s", stdout)
	}

	// The `act help workflow` COUNTING FROM A LISTING block is the surface
	// an agent reads BEFORE it makes this mistake. Assert the live help
	// text carries the guidance, not just that the binary behaves.
	help, _, code := runActIn(t, dir, "help", "workflow")
	if code != 0 {
		t.Fatalf("act help workflow: exit %d", code)
	}
	for _, want := range []string{
		"COUNTING FROM A LISTING",
		"Never derive a count by piping a default 'act list' into a",
		"--limit 0",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("act help workflow missing %q", want)
		}
	}
}

// TestDocClaim_List_LimitZeroReturnsEverything pins the flag-help claim that
// "--limit 0 means no limit (return every match)". Before act-b50d81 the CLI
// rejected `--limit 0` with exit 2, so a caller who wanted the whole list had
// no way to ask for it.
func TestDocClaim_List_LimitZeroReturnsEverything(t *testing.T) {
	dir := blocksSite(t)
	seedIssues(t, dir, 5)

	stdout, stderr, code := runActIn(t, dir, "list", "--limit", "0")
	if code != 0 {
		t.Fatalf("act list --limit 0: exit %d; stderr=%s", code, stderr)
	}
	if got := len(linesOf(stdout)); got != 5 {
		t.Fatalf("--limit 0 returned %d rows, want all 5:\n%s", got, stdout)
	}
	// Nothing was dropped, so nothing should be warned about.
	if strings.Contains(stderr, "WARNING") {
		t.Errorf("uncapped listing emitted a truncation warning:\n%s", stderr)
	}
}

// TestList_UncappedListingRowShapeUnchanged pins the row rendering of a
// listing that fits under the limit: `<short> <status> <prio> <title>`, no
// trailer, no count line, clean stderr.
//
// It used to also pin the row SET — "a non-capped default listing prints
// exactly what it printed before" — which was pinning the defect. The
// default listing is now the working set (act-9dfdc1), so the row set is
// asserted by TestDocClaim_List_DefaultExcludesClosed below and this guard covers
// only the rendering, which the default change deliberately does not touch.
func TestList_UncappedListingRowShapeUnchanged(t *testing.T) {
	dir := blocksSite(t)
	seedIssues(t, dir, 3)

	stdout, stderr, code := runActIn(t, dir, "list")
	if code != 0 {
		t.Fatalf("act list: exit %d; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("uncapped listing wrote to stderr: %q", stderr)
	}
	rows := linesOf(stdout)
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d:\n%s", len(rows), stdout)
	}
	// Row shape is still `<short> <status> <prio> <title>` — no trailer
	// line, no count line, no decoration.
	for _, r := range rows {
		fields := strings.SplitN(r, " ", 4)
		if len(fields) != 4 {
			t.Fatalf("row %q is not `<short> <status> <prio> <title>`", r)
		}
		if !strings.HasPrefix(fields[0], "act-") {
			t.Errorf("row %q does not start with an act id", r)
		}
		if fields[1] != "open" {
			t.Errorf("row %q: want status open, got %q", r, fields[1])
		}
	}
}

// TestList_JSONCarriesTotalAndTruncated pins the machine-readable half of
// the contract. A JSON consumer must be able to test one boolean rather
// than infer truncation from `count == limit` — which is wrong exactly when
// the match count equals the limit, the case asserted last here.
func TestList_JSONCarriesTotalAndTruncated(t *testing.T) {
	dir := blocksSite(t)
	seedIssues(t, dir, 5)

	type listJSON struct {
		Count     int  `json:"count"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
	}
	decode := func(t *testing.T, args ...string) listJSON {
		t.Helper()
		stdout, stderr, code := runActIn(t, dir, args...)
		if code != 0 {
			t.Fatalf("act %v: exit %d; stderr=%s", args, code, stderr)
		}
		var got listJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
			t.Fatalf("act %v: unmarshal: %v\nstdout=%s", args, err, stdout)
		}
		return got
	}

	capped := decode(t, "list", "--limit", "2", "--json")
	if capped.Count != 2 || capped.Total != 5 || !capped.Truncated {
		t.Errorf("capped: got count=%d total=%d truncated=%v; want 2/5/true",
			capped.Count, capped.Total, capped.Truncated)
	}

	full := decode(t, "list", "--limit", "0", "--json")
	if full.Count != 5 || full.Total != 5 || full.Truncated {
		t.Errorf("uncapped: got count=%d total=%d truncated=%v; want 5/5/false",
			full.Count, full.Total, full.Truncated)
	}

	// The case that defeats `count == limit` inference: the limit is
	// exactly the number of matches, so nothing was dropped.
	exact := decode(t, "list", "--limit", "5", "--json")
	if exact.Truncated {
		t.Errorf("limit == match count must not report truncated; got count=%d total=%d",
			exact.Count, exact.Total)
	}
}

// TestList_TruncationNoticeSurvivesAPipe is the incident reproduction: the
// caller pipes list into a filter, so stdout is consumed by the pipe. The
// warning must still reach the human. Asserting that stderr is non-empty
// while stdout is being consumed elsewhere is what distinguishes this from
// the capped-listing test above.
func TestList_TruncationNoticeSurvivesAPipe(t *testing.T) {
	dir := blocksSite(t)
	seedIssues(t, dir, 5)

	stdout, stderr, code := runActIn(t, dir, "list", "--limit", "2")
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr)
	}
	// Simulate the filter a caller would apply to stdout. Whatever it
	// yields, the warning is on the other stream and survives.
	filtered := 0
	for _, r := range linesOf(stdout) {
		if strings.Contains(r, " open ") {
			filtered++
		}
	}
	if filtered != 2 {
		t.Fatalf("filter over capped stdout saw %d rows, want 2", filtered)
	}
	if !strings.Contains(stderr, "WARNING") {
		t.Fatalf("a piped caller's human saw no warning; stderr=%q", stderr)
	}
}
