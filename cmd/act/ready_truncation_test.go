package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// act-1b816e: `act ready` capped at 50 rows, said nothing about it, and
// ignored `--limit 0` — so `act ready --json --limit 0` returned 50 while
// `--limit 500` returned 99 on the same store, and no caller could tell a
// project with exactly 50 ready issues from one with 500. `act list` had
// documented and honoured `--limit 0` since act-b50d81, so the two
// subcommands disagreed on the same flag.
//
// These tests assert at the subprocess boundary — the surface the
// disagreement was measured on — per the repo's documentation-discipline
// rule.

// readyJSON is the machine-readable half of the ready contract.
type readyJSON struct {
	Ready []struct {
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
		ClaimedAt string `json:"claimed_at"`
	} `json:"ready"`
	Count     int  `json:"count"`
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

func decodeReady(t *testing.T, dir string, args ...string) (readyJSON, string) {
	t.Helper()
	stdout, stderr, code := runActIn(t, dir, args...)
	if code != 0 {
		t.Fatalf("act %v: exit %d; stderr=%s", args, code, stderr)
	}
	var got readyJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("act %v: unmarshal: %v\nstdout=%s", args, err, stdout)
	}
	return got, stderr
}

// TestDocClaim_Ready_LimitZeroReturnsEverything pins the `act ready
// --limit` flag-help claim that "--limit 0 means no limit (return every
// ready issue)".
//
// The store deliberately holds MORE ready issues than the default limit
// (the ticket's acceptance criterion): with 55 ready issues, the pre-fix
// binary returned 50 for `--limit 0` and 55 for `--limit 500`, which is
// the disagreement itself. Anything smaller than the default cannot tell
// "unlimited" apart from "defaulted".
func TestDocClaim_Ready_LimitZeroReturnsEverything(t *testing.T) {
	const n = cli.DefaultReadyLimit + 5
	dir := blocksSite(t)
	seedIssues(t, dir, n)

	// The default cap still bites, and says so.
	def, defErr := decodeReady(t, dir, "ready", "--json")
	if def.Count != cli.DefaultReadyLimit || def.Total != n || !def.Truncated {
		t.Errorf("default ready: got count=%d total=%d truncated=%v; want %d/%d/true",
			def.Count, def.Total, def.Truncated, cli.DefaultReadyLimit, n)
	}
	if !strings.Contains(defErr, "WARNING") {
		t.Errorf("a capped default ready set emitted no warning; stderr=%q", defErr)
	}

	// --limit 0 returns EVERY ready issue. This is the assertion the
	// pre-fix binary failed: it silently fell back to the default 50.
	full, fullErr := decodeReady(t, dir, "ready", "--json", "--limit", "0")
	if full.Count != n || full.Total != n || full.Truncated {
		t.Fatalf("--limit 0: got count=%d total=%d truncated=%v; want %d/%d/false",
			full.Count, full.Total, full.Truncated, n, n)
	}
	if len(full.Ready) != n {
		t.Fatalf("--limit 0 returned %d rows, want all %d", len(full.Ready), n)
	}
	if strings.Contains(fullErr, "WARNING") {
		t.Errorf("uncapped ready set emitted a truncation warning:\n%s", fullErr)
	}

	// And it agrees with an explicitly-high limit — the workaround
	// act-roundup had to use, which must keep working.
	high, _ := decodeReady(t, dir, "ready", "--json", "--limit", "500")
	if high.Count != full.Count {
		t.Errorf("--limit 0 (%d) disagrees with --limit 500 (%d)", full.Count, high.Count)
	}

	// The human path honours --limit 0 too: one row per ready issue.
	stdout, _, code := runActIn(t, dir, "ready", "--limit", "0")
	if code != 0 {
		t.Fatalf("act ready --limit 0: exit %d", code)
	}
	if got := len(linesOf(stdout)); got != n {
		t.Errorf("human --limit 0 printed %d rows, want %d", got, n)
	}
}

// TestDocClaim_Ready_CappedSetWarnsOnStderr pins the flag-help claim that
// "A capped ready set prints a WARNING to stderr naming how many issues
// were hidden."
//
// stderr placement is the load-bearing part, for the same reason it is on
// `act list`: the consumer that hit this bug was
// `act ready --json | jq '.count'`, where stdout belongs to the pipe. A
// notice in the row stream would also corrupt both the human line count
// and the JSON document.
func TestDocClaim_Ready_CappedSetWarnsOnStderr(t *testing.T) {
	dir := blocksSite(t)
	seedIssues(t, dir, 5)

	stdout, stderr, code := runActIn(t, dir, "ready", "--limit", "2")
	if code != 0 {
		t.Fatalf("act ready --limit 2: exit %d; stderr=%s", code, stderr)
	}
	if rows := linesOf(stdout); len(rows) != 2 {
		t.Fatalf("expected 2 rows on stdout, got %d:\n%s", len(rows), stdout)
	}
	for _, want := range []string{"WARNING", "showing 2 of 5", "3 hidden", "--limit 0"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q; got:\n%s", want, stderr)
		}
	}
	if strings.Contains(stdout, "WARNING") {
		t.Errorf("truncation notice leaked onto stdout, corrupting the row stream:\n%s", stdout)
	}

	// Same contract under --json: the warning is on stderr and the
	// document on stdout still parses.
	jsonOut, jsonErr, code := runActIn(t, dir, "ready", "--json", "--limit", "2")
	if code != 0 {
		t.Fatalf("act ready --json --limit 2: exit %d", code)
	}
	if !strings.Contains(jsonErr, "WARNING") {
		t.Errorf("a piped --json caller's human saw no warning; stderr=%q", jsonErr)
	}
	var doc readyJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonOut)), &doc); err != nil {
		t.Fatalf("the warning corrupted the JSON document: %v\nstdout=%s", err, jsonOut)
	}

	// The `act help workflow` block is the surface an agent reads BEFORE
	// making this mistake; assert the live help text carries the ready
	// half of the guidance, not just that the binary behaves.
	help, _, code := runActIn(t, dir, "help", "workflow")
	if code != 0 {
		t.Fatalf("act help workflow: exit %d", code)
	}
	for _, want := range []string{"'act ready' caps at 50 rows", "act ready --limit 0"} {
		if !strings.Contains(help, want) {
			t.Errorf("act help workflow missing %q", want)
		}
	}
}

// TestReady_JSONCarriesTotalAndTruncated covers the case that defeats the
// `count == limit` inference a caller is otherwise forced into: the limit
// is exactly the number of ready issues, so nothing was dropped.
func TestReady_JSONCarriesTotalAndTruncated(t *testing.T) {
	dir := blocksSite(t)
	seedIssues(t, dir, 5)

	capped, _ := decodeReady(t, dir, "ready", "--json", "--limit", "2")
	if capped.Count != 2 || capped.Total != 5 || !capped.Truncated {
		t.Errorf("capped: got count=%d total=%d truncated=%v; want 2/5/true",
			capped.Count, capped.Total, capped.Truncated)
	}

	exact, stderr := decodeReady(t, dir, "ready", "--json", "--limit", "5")
	if exact.Truncated {
		t.Errorf("limit == ready count must not report truncated; got count=%d total=%d",
			exact.Count, exact.Total)
	}
	if strings.Contains(stderr, "WARNING") {
		t.Errorf("limit == ready count warned about nothing; stderr=%q", stderr)
	}
}

// TestDocClaim_Listings_CarryClaimAge pins the spec claim that
// `act list --json` carries `claimed_at` and `act ready --json` carries
// `created_at` (act-d627c8).
//
// The consumer is a cross-store aggregator: "8 in progress" is a healthy
// mid-drain or a pile of stale claims depending only on age, and the only
// route to that age used to be one `act show` subprocess per in-progress
// id — unbounded fan-out keyed on exactly the worst-off projects. The
// assertion is on the listing shape, not on ListedIssue internals, since
// the subprocess JSON is what the aggregator actually parses.
func TestDocClaim_Listings_CarryClaimAge(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "claim age probe")

	// ready --json carries created_at while the issue is unclaimed.
	before, _ := decodeReady(t, dir, "ready", "--json")
	if len(before.Ready) != 1 {
		t.Fatalf("expected 1 ready row, got %d", len(before.Ready))
	}
	if before.Ready[0].CreatedAt == "" {
		t.Errorf("ready row carries no created_at: %+v", before.Ready[0])
	}

	if _, stderr, code := runActIn(t, dir, "update", id, "--claim"); code != 0 {
		t.Fatalf("act update %s --claim: exit %d; stderr=%s", id, code, stderr)
	}

	stdout, stderr, code := runActIn(t, dir, "list", "--json")
	if code != 0 {
		t.Fatalf("act list --json: exit %d; stderr=%s", code, stderr)
	}
	var listing struct {
		Issues []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			CreatedAt string `json:"created_at"`
			ClaimedAt string `json:"claimed_at"`
		} `json:"issues"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &listing); err != nil {
		t.Fatalf("unmarshal list: %v\nstdout=%s", err, stdout)
	}
	if len(listing.Issues) != 1 {
		t.Fatalf("expected 1 listed issue, got %d", len(listing.Issues))
	}
	row := listing.Issues[0]
	if row.Status != "in_progress" {
		t.Fatalf("issue is %q, not in_progress; the claim did not land", row.Status)
	}
	if row.ClaimedAt == "" {
		t.Fatalf("list --json omits claimed_at on a claimed issue: %+v", row)
	}
	if row.CreatedAt == "" {
		t.Errorf("list --json dropped created_at: %+v", row)
	}

	// The value must be the same one `act show --json` reports — the
	// aggregator is replacing that call, so a divergent field would be
	// worse than the fan-out it removes.
	showOut, _, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show --json: exit %d", code)
	}
	var shown struct {
		ClaimedAt string `json:"claimed_at"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(showOut)), &shown); err != nil {
		t.Fatalf("unmarshal show: %v", err)
	}
	if shown.ClaimedAt != row.ClaimedAt {
		t.Errorf("claimed_at disagrees: list=%q show=%q", row.ClaimedAt, shown.ClaimedAt)
	}

	// Unclaimed rows omit the field entirely rather than carrying an
	// empty string a consumer could mistake for a zero timestamp.
	other := createBlocksIssue(t, dir, "never claimed")
	raw, _, code := runActIn(t, dir, "list", "--json")
	if code != 0 {
		t.Fatalf("act list --json: exit %d", code)
	}
	var loose struct {
		Issues []map[string]any `json:"issues"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &loose); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	for _, m := range loose.Issues {
		if m["id"] == other {
			if _, present := m["claimed_at"]; present {
				t.Errorf("unclaimed issue carries a claimed_at key: %v", m)
			}
		}
	}
}
