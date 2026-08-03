package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// act-3e21b8 / act-3e2986: the spec's fold table lists six LWW-per-field
// updatable fields (title, description, priority, type, assignee, parent)
// and the fold implements all six, but `act update` and MCP `act_update`
// exposed only three. These tests pin the three that were missing at the
// user-visible boundary — the CLI subprocess — because the prior drift
// bugs in this repo all passed their internal tests while the command
// bailed before reaching the asserted path.

// TestDocClaim_Update_TitleSetsTitle pins the `--title` flag-help and spec
// claims: the title is replaced, the new title is what a listing shows,
// and the empty/oversized forms are rejected rather than written.
func TestDocClaim_Update_TitleSetsTitle(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "Main CI red: stale claim that outlived its cause")

	if _, stderr, code := runActIn(t, dir, "update", id, "--title", "Reconcile the stale-claim doctor check"); code != 0 {
		t.Fatalf("--title: exit %d; stderr=%s", code, stderr)
	}

	// The listing is the surface the whole ticket is about: a dispatcher
	// reads titles, not bodies. Assert there, not only on `act show`.
	list, _, code := runActIn(t, dir, "list")
	if code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(list, "Reconcile the stale-claim doctor check") {
		t.Errorf("act list does not show the new title:\n%s", list)
	}
	if strings.Contains(list, "Main CI red") {
		t.Errorf("act list still shows the stale title:\n%s", list)
	}

	shown, _, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("show: exit %d", code)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(shown), &fields); err != nil {
		t.Fatalf("show --json: %v: %s", err, shown)
	}
	if got := fields["title"]; got != "Reconcile the stale-claim doctor check" {
		t.Errorf("show --json title = %v; want the retitled text", got)
	}

	// `--title ""` is exit 2, not a clear: unlike --assignee/--description,
	// an empty title is not a state `act create` can produce either.
	if _, stderr, code := runActIn(t, dir, "update", id, "--title", ""); code != 2 {
		t.Errorf("--title \"\": exit %d, want 2; stderr=%s", code, stderr)
	}
	// ≤256 bytes, matching `act create`.
	if _, stderr, code := runActIn(t, dir, "update", id, "--title", strings.Repeat("x", 257)); code != 2 {
		t.Errorf("--title over 256 bytes: exit %d, want 2; stderr=%s", code, stderr)
	}
	// The reject must not have landed anything: the title is unchanged.
	shown, _, _ = runActIn(t, dir, "show", id, "--json")
	if !strings.Contains(shown, "Reconcile the stale-claim doctor check") {
		t.Errorf("a rejected --title mutated the issue:\n%s", shown)
	}

	// `act help workflow` is where an agent looking for "the title is
	// wrong, how do I fix it" lands.
	help, _, code := runActIn(t, dir, "help", "workflow")
	if code != 0 {
		t.Fatalf("act help workflow: exit %d", code)
	}
	if !strings.Contains(help, "act update <id> --title") {
		t.Errorf("act help workflow does not document --title:\n%s", help)
	}
}

// TestDocClaim_Update_TypeSetsType pins the `--type` claim: the same closed
// enum as `act create --type`, with anything else rejected up front rather
// than written as an op every type filter would then miss.
func TestDocClaim_Update_TypeSetsType(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "type probe")

	if _, stderr, code := runActIn(t, dir, "update", id, "--type", "bug"); code != 0 {
		t.Fatalf("--type bug: exit %d; stderr=%s", code, stderr)
	}
	// Assert through the type FILTER, not just the rendered field: the
	// point of a correct type is that `act list --type bug` finds it.
	list, _, code := runActIn(t, dir, "list", "--type", "bug")
	if code != 0 {
		t.Fatalf("list --type bug: exit %d", code)
	}
	if !strings.Contains(list, "type probe") {
		t.Errorf("retyped issue missing from `act list --type bug`:\n%s", list)
	}

	if _, stderr, code := runActIn(t, dir, "update", id, "--type", "feature"); code != 2 {
		t.Errorf("--type feature: exit %d, want 2; stderr=%s", code, stderr)
	}
	// The rejected value did not land: the issue is still a bug.
	shown, _, _ := runActIn(t, dir, "show", id, "--json")
	if !strings.Contains(shown, `"type":"bug"`) {
		t.Errorf("a rejected --type mutated the issue:\n%s", shown)
	}
}

// TestDocClaim_Update_ParentSetsAndClears pins the `--parent` claims:
// prefix-resolved, `""` detaches, and the two cycle forms are refused with
// `cycle_detected` — `act doctor`'s cycle check walks the blocks subgraph
// only, so a parent cycle would have no downstream detector.
func TestDocClaim_Update_ParentSetsAndClears(t *testing.T) {
	dir := blocksSite(t)
	epic := createBlocksIssue(t, dir, "the epic")
	child := createBlocksIssue(t, dir, "the child")

	if _, stderr, code := runActIn(t, dir, "update", child, "--parent", epic); code != 0 {
		t.Fatalf("--parent: exit %d; stderr=%s", code, stderr)
	}
	shown, _, _ := runActIn(t, dir, "show", child, "--json")
	if !strings.Contains(shown, `"parent":"`+epic+`"`) {
		t.Errorf("parent not set:\n%s", shown)
	}

	// Self-parenting is refused.
	out, stderr, code := runActIn(t, dir, "update", child, "--parent", child, "--json")
	if code != 2 {
		t.Errorf("self --parent: exit %d, want 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(out+stderr, "cycle_detected") {
		t.Errorf("self --parent envelope is not cycle_detected: %s%s", out, stderr)
	}

	// A cycle one level up is refused too: the epic already has the child
	// below it, so making the child the epic's parent closes the loop.
	out, stderr, code = runActIn(t, dir, "update", epic, "--parent", child, "--json")
	if code != 2 {
		t.Errorf("cyclic --parent: exit %d, want 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(out+stderr, "cycle_detected") {
		t.Errorf("cyclic --parent envelope is not cycle_detected: %s%s", out, stderr)
	}

	// An unknown parent resolves to exit 3, like any other unknown id.
	if _, _, code := runActIn(t, dir, "update", child, "--parent", "act-nosuchid"); code != 3 {
		t.Errorf("unknown --parent: exit %d, want 3", code)
	}

	// `--parent ""` detaches — the only way to move a child out from
	// under the wrong epic.
	if _, stderr, code := runActIn(t, dir, "update", child, "--parent", ""); code != 0 {
		t.Fatalf("--parent \"\": exit %d; stderr=%s", code, stderr)
	}
	shown, _, _ = runActIn(t, dir, "show", child, "--json")
	if strings.Contains(shown, `"parent":"`+epic+`"`) {
		t.Errorf("--parent \"\" did not detach:\n%s", shown)
	}
}

// TestDocClaim_Update_RetitleKeepsCommitCorrelation is act-3e21b8's fourth
// acceptance criterion, asserted rather than reasoned about: a retitle must
// not disturb commit-marker correlation or `act doctor`'s orphan-close
// check. Both key on the issue id (the `Act-Id:` trailer and the index's id
// column); the title is not an identity key. The test captures both
// outputs before and after the retitle and requires them to be identical.
func TestDocClaim_Update_RetitleKeepsCommitCorrelation(t *testing.T) {
	dir := blocksSite(t)
	id := createBlocksIssue(t, dir, "original title the marker was written under")
	short := id
	if len(short) > 10 {
		short = short[:10]
	}

	// A host-repo work commit carrying the Act-Id trailer — the marker
	// act correlates on.
	writeHostCommit(t, dir, "work.txt", "first\n", "feat: do the work\n\nAct-Id: "+short+"\n")

	commitsBefore := showCommits(t, dir, id)
	if len(commitsBefore) == 0 {
		t.Fatalf("no correlated commits before retitle; the marker never took: %v", commitsBefore)
	}
	doctorBefore, _, _ := runActIn(t, dir, "doctor", "--check", "orphan-close", "--json")

	if _, stderr, code := runActIn(t, dir, "update", id, "--title", "a title that no longer resembles the old one"); code != 0 {
		t.Fatalf("retitle: exit %d; stderr=%s", code, stderr)
	}

	commitsAfter := showCommits(t, dir, id)
	if len(commitsAfter) != len(commitsBefore) {
		t.Fatalf("retitle changed the correlated commit count: before=%v after=%v", commitsBefore, commitsAfter)
	}
	for i := range commitsBefore {
		if commitsBefore[i] != commitsAfter[i] {
			t.Errorf("retitle changed correlated commit %d: %q -> %q", i, commitsBefore[i], commitsAfter[i])
		}
	}
	doctorAfter, _, _ := runActIn(t, dir, "doctor", "--check", "orphan-close", "--json")
	if doctorBefore != doctorAfter {
		t.Errorf("retitle changed `act doctor --check orphan-close`:\nbefore=%s\nafter=%s", doctorBefore, doctorAfter)
	}

	// The correlation still works for a commit written AFTER the retitle:
	// the marker is the id, so a new commit lands on the same issue.
	writeHostCommit(t, dir, "work.txt", "second\n", "fix: more work\n\nAct-Id: "+short+"\n")
	if got := showCommits(t, dir, id); len(got) != len(commitsAfter)+1 {
		t.Errorf("post-retitle commit not correlated: before=%v after=%v", commitsAfter, got)
	}
}

// showCommits returns the SHAs `act show --json` correlates to an issue.
func showCommits(t *testing.T, dir, id string) []string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("show --json: exit %d; stderr=%s", code, stderr)
	}
	var parsed struct {
		Commits []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("show --json: %v: %s", err, out)
	}
	shas := make([]string, 0, len(parsed.Commits))
	for _, c := range parsed.Commits {
		shas = append(shas, c.SHA)
	}
	return shas
}

// writeHostCommit writes a file in the HOST repo and commits it with the
// given message. Named paths only — never `git commit -a` — so the commit
// carries exactly what the test wrote.
func writeHostCommit(t *testing.T, dir, name, body, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	for _, args := range [][]string{
		{"-C", dir, "add", "--", name},
		{"-C", dir, "commit", "-m", message, "--", name},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
}
