package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// act-9dfdc1: `act list` listed issues of every status, closed included.
// A tracker accumulates closed work indefinitely — the repo that produced
// this ticket was 264 closed to 4 open — so the default listing was almost
// entirely finished work, and the 200-row cap spent its budget on closed
// rows while open work fell off the end. The default is now the working
// set, and closed rows are reachable explicitly.
//
// These tests assert at the subprocess boundary — the surface a caller and
// an agent actually use — rather than on ListResult internals.

// closeIssue closes an issue and fails the test if the close did not take.
func closeIssue(t *testing.T, dir, id string) {
	t.Helper()
	if _, stderr, code := runActIn(t, dir, "close", id, "--reason", "done"); code != 0 {
		t.Fatalf("act close %s: exit %d; stderr=%s", id, code, stderr)
	}
}

// statusesOf extracts the status column from human list output.
func statusesOf(stdout string) []string {
	var out []string
	for _, r := range linesOf(stdout) {
		fields := strings.SplitN(r, " ", 4)
		if len(fields) >= 2 {
			out = append(out, fields[1])
		}
	}
	return out
}

// TestDocClaim_List_DefaultExcludesClosed is the contract this ticket exists for:
// the default listing is the working set. Closed rows are absent — not
// merely sorted lower — so a caller counting or eyeballing `act list` sees
// work that is still live.
func TestDocClaim_List_DefaultExcludesClosed(t *testing.T) {
	dir := blocksSite(t)
	live := createBlocksIssue(t, dir, "still open")
	done := createBlocksIssue(t, dir, "already finished")
	closeIssue(t, dir, done)

	stdout, stderr, code := runActIn(t, dir, "list")
	if code != 0 {
		t.Fatalf("act list: exit %d; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "already finished") {
		t.Errorf("default listing included a closed issue:\n%s", stdout)
	}
	if !strings.Contains(stdout, "still open") {
		t.Errorf("default listing dropped the open issue:\n%s", stdout)
	}
	if got := len(linesOf(stdout)); got != 1 {
		t.Fatalf("default listing returned %d rows, want 1 (the open issue):\n%s", got, stdout)
	}
	for _, s := range statusesOf(stdout) {
		if s == "closed" {
			t.Errorf("closed status in default listing:\n%s", stdout)
		}
	}

	// in_progress and blocked are working-set statuses and must stay in
	// the default listing — "exclude closed" is not "show only open".
	if _, stderr, code := runActIn(t, dir, "update", live, "--claim"); code != 0 {
		t.Fatalf("act update --claim: exit %d; stderr=%s", code, stderr)
	}
	stdout, _, code = runActIn(t, dir, "list")
	if code != 0 {
		t.Fatalf("act list after in_progress: exit %d", code)
	}
	if !strings.Contains(stdout, "still open") {
		t.Errorf("default listing dropped an in_progress issue:\n%s", stdout)
	}

	// The `act help workflow` block is where an agent learns what a
	// listing contains before it counts one. Assert the live help text
	// carries the new contract, not just that the binary behaves.
	help, _, code := runActIn(t, dir, "help", "workflow")
	if code != 0 {
		t.Fatalf("act help workflow: exit %d", code)
	}
	for _, want := range []string{
		"'act list' lists the WORKING SET",
		"act list --status closed",
		"act list --all",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("act help workflow missing %q", want)
		}
	}
}

// TestDocClaim_List_ClosedReachableViaStatusAndAll pins the escape hatches. Making
// the default narrower is only safe if the rows it stops showing are still
// one flag away.
func TestDocClaim_List_ClosedReachableViaStatusAndAll(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "still open")
	done := createBlocksIssue(t, dir, "already finished")
	closeIssue(t, dir, done)

	stdout, stderr, code := runActIn(t, dir, "list", "--status", "closed")
	if code != 0 {
		t.Fatalf("act list --status closed: exit %d; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "already finished") {
		t.Errorf("--status closed did not reach the closed issue:\n%s", stdout)
	}
	if strings.Contains(stdout, "still open") {
		t.Errorf("--status closed leaked an open issue:\n%s", stdout)
	}

	stdout, stderr, code = runActIn(t, dir, "list", "--all")
	if code != 0 {
		t.Fatalf("act list --all: exit %d; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "already finished") || !strings.Contains(stdout, "still open") {
		t.Errorf("--all did not list every status:\n%s", stdout)
	}
	if got := len(linesOf(stdout)); got != 2 {
		t.Fatalf("--all returned %d rows, want 2:\n%s", got, stdout)
	}
}

// TestDocClaim_List_ClosedSortAfterOpenWork pins the ordering half of the contract.
// The case is chosen so the default sort ALONE would put the closed row
// first: it is p0 and the open row is p3, and the default sort is priority
// ascending. Only the closed-last grouping keeps live work on top.
func TestDocClaim_List_ClosedSortAfterOpenWork(t *testing.T) {
	dir := blocksSite(t)
	urgentDone := createIssueWithPriority(t, dir, "urgent but finished", "0")
	createIssueWithPriority(t, dir, "low but live", "3")
	closeIssue(t, dir, urgentDone)

	for _, args := range [][]string{
		{"list", "--all"},
		{"list", "--all", "--sort", "priority"},
		{"list", "--all", "--sort", "-priority"},
		{"list", "--status", "open,closed"},
	} {
		stdout, stderr, code := runActIn(t, dir, args...)
		if code != 0 {
			t.Fatalf("act %v: exit %d; stderr=%s", args, code, stderr)
		}
		got := statusesOf(stdout)
		if len(got) != 2 {
			t.Fatalf("act %v: got %d rows, want 2:\n%s", args, len(got), stdout)
		}
		if got[0] == "closed" {
			t.Errorf("act %v: closed row sorted above open work:\n%s", args, stdout)
		}
		if got[1] != "closed" {
			t.Errorf("act %v: closed row is not last:\n%s", args, stdout)
		}
	}
}

// TestList_AllWithStatusIsRejected pins the mutual exclusion. "Everything"
// and "exactly these statuses" have no single honest answer together, and
// silently letting one win is the class of defect this branch exists to
// remove.
func TestList_AllWithStatusIsRejected(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "anything")

	stdout, stderr, code := runActIn(t, dir, "list", "--all", "--status", "open")
	if code != 2 {
		t.Fatalf("act list --all --status open: exit %d, want 2; stdout=%s stderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("error does not name the conflict; stderr=%s", stderr)
	}
}

// TestList_JSONTotalCountsTheWorkingSet pins that the machine-readable
// contract from act-b50d81 agrees with the new default: `total` is the
// pre-limit count of what the caller ASKED for, so a JSON consumer reading
// the default listing gets the size of the working set, not of the tracker.
func TestList_JSONTotalCountsTheWorkingSet(t *testing.T) {
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "open one")
	createBlocksIssue(t, dir, "open two")
	for i := 0; i < 3; i++ {
		closeIssue(t, dir, createBlocksIssue(t, dir, "finished"))
	}

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

	def := decode(t, "list", "--json")
	if def.Count != 2 || def.Total != 2 || def.Truncated {
		t.Errorf("default: got count=%d total=%d truncated=%v; want 2/2/false",
			def.Count, def.Total, def.Truncated)
	}
	all := decode(t, "list", "--all", "--json")
	if all.Count != 5 || all.Total != 5 {
		t.Errorf("--all: got count=%d total=%d; want 5/5", all.Count, all.Total)
	}
	closed := decode(t, "list", "--status", "closed", "--json")
	if closed.Count != 3 || closed.Total != 3 {
		t.Errorf("--status closed: got count=%d total=%d; want 3/3", closed.Count, closed.Total)
	}
}

// createIssueWithPriority creates an issue at an explicit priority and
// returns its id.
func createIssueWithPriority(t *testing.T, dir, title, priority string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "create", title, "--priority", priority, "--json")
	if code != 0 {
		t.Fatalf("act create %q -p %s: exit %d; stderr=%s", title, priority, code, stderr)
	}
	marker := `"id":"`
	i := strings.Index(out, marker)
	if i < 0 {
		marker = `"id": "`
		i = strings.Index(out, marker)
	}
	if i < 0 {
		t.Fatalf("no id in create output: %s", out)
	}
	rest := out[i+len(marker):]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		t.Fatalf("could not parse id: %s", out)
	}
	return rest[:end]
}
