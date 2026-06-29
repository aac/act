package main

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// bootstrapLoopRepo inits a fresh git+act repo in a temp dir and returns its
// path. No issues are created — callers add them via `act create`.
func bootstrapLoopRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", dir},
		{"-C", dir, "config", "user.email", "test@example.com"},
		{"-C", dir, "config", "user.name", "Test"},
		{"-C", dir, "config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if _, stderr, code := runActIn(t, dir, "init", "--json"); code != 0 {
		t.Fatalf("act init: exit %d; stderr=%s", code, stderr)
	}
	return dir
}

// createIssue runs `act create` with the given args (after the title) and
// returns the new issue id.
func createIssue(t *testing.T, dir, title string, extra ...string) string {
	t.Helper()
	args := append([]string{"create", title, "--json"}, extra...)
	out, stderr, code := runActIn(t, dir, args...)
	if code != 0 {
		t.Fatalf("act create %q: exit %d; stderr=%s", title, code, stderr)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("parse create id from %q: %v", out, err)
	}
	return created.ID
}

// showField drives `act show <id> --json` and returns the named string field
// at the subprocess boundary.
func showField(t *testing.T, dir, id, field string) string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show %s --json: exit %d; stderr=%s", id, code, stderr)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("parse show json %q: %v", out, err)
	}
	s, _ := m[field].(string)
	return s
}

// TestDocClaim_Next_ClaimsAndShowsTopReady pins the `act next` composed-flow
// claim documented in cmd/act/help.go ("'act next' bundles steps 1-2 (ready +
// claim + show)") and the README example. `act next` must pick the TOP
// claimable ready issue (highest priority), claim it atomically, and show it.
//
// Asserted at the user-visible boundary: `act next --json` output, plus a
// follow-up `act show --json` that confirms the claim actually landed (status
// in_progress + assignee set) — i.e. the path fired, not just the output.
func TestDocClaim_Next_ClaimsAndShowsTopReady(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	createIssue(t, dir, "low prio", "--priority", "3")
	highID := createIssue(t, dir, "high prio", "--priority", "0")

	out, stderr, code := runActIn(t, dir, "next", "--json")
	if code != 0 {
		t.Fatalf("act next --json: exit %d; stderr=%s", code, stderr)
	}
	var got struct {
		Claimed      bool           `json:"claimed"`
		CommitMarker string         `json:"commit_marker"`
		Issue        map[string]any `json:"issue"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse next json %q: %v", out, err)
	}
	if !got.Claimed {
		t.Fatalf("act next: claimed=false, want true; out=%s", out)
	}
	if id, _ := got.Issue["id"].(string); id != highID {
		t.Errorf("act next claimed %q, want the top-priority issue %q; out=%s", id, highID, out)
	}
	if st, _ := got.Issue["status"].(string); st != "in_progress" {
		t.Errorf("act next issue status = %q, want in_progress; out=%s", st, out)
	}
	if !strings.HasPrefix(got.CommitMarker, "Act-Id: ") {
		t.Errorf("act next commit_marker = %q, want an 'Act-Id: ' trailer; out=%s", got.CommitMarker, out)
	}

	// Path verification: the claim must have actually landed on disk, not
	// merely been reported. Re-read state via a fresh `act show`.
	if st := showField(t, dir, highID, "status"); st != "in_progress" {
		t.Errorf("after act next, show status = %q, want in_progress", st)
	}
	if as := showField(t, dir, highID, "assignee"); as == "" {
		t.Errorf("after act next, assignee is empty — claim did not assign the issue")
	}
}

// TestNext_HumanOutputShowsClaim covers the human (non-JSON) render of
// `act next`: it must name the claimed short id and surface the commit
// marker so an agent driving the CLI gets the same information the MCP tool
// returns.
func TestNext_HumanOutputShowsClaim(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	id := createIssue(t, dir, "only issue")

	out, stderr, code := runActIn(t, dir, "next")
	if code != 0 {
		t.Fatalf("act next: exit %d; stderr=%s", code, stderr)
	}
	short := showField(t, dir, id, "short_id")
	if !strings.Contains(out, "Claimed "+short) {
		t.Errorf("act next human output should name the claimed id %q; got:\n%s", short, out)
	}
	if !strings.Contains(out, "commit marker: Act-Id: "+short) {
		t.Errorf("act next human output should surface the commit marker; got:\n%s", out)
	}
}

// TestNext_NoReadyWork covers the empty-frontier case: `act next` exits 0 with
// {"claimed": false, "candidates": []} rather than erroring.
func TestNext_NoReadyWork(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	out, stderr, code := runActIn(t, dir, "next", "--json")
	if code != 0 {
		t.Fatalf("act next (empty): exit %d; stderr=%s", code, stderr)
	}
	var got struct {
		Claimed    bool  `json:"claimed"`
		Candidates []any `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse next json %q: %v", out, err)
	}
	if got.Claimed {
		t.Errorf("act next on empty frontier: claimed=true, want false; out=%s", out)
	}
	if len(got.Candidates) != 0 {
		t.Errorf("act next on empty frontier: candidates=%v, want []; out=%s", got.Candidates, out)
	}
}

// TestDocClaim_Finish_ClosesAndReportsClosed pins the `act finish` composed-
// flow claim documented in cmd/act/help.go ("'act finish' bundles steps 4 + 6
// (close + push)") and the README example. `act finish <id>` must close the
// issue and report it closed.
//
// Asserted at the user-visible boundary: `act finish --json` output, plus a
// follow-up `act show --json` confirming the close actually landed.
func TestDocClaim_Finish_ClosesAndReportsClosed(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	id := createIssue(t, dir, "to be finished")
	if _, stderr, code := runActIn(t, dir, "update", "--claim", id); code != 0 {
		t.Fatalf("act update --claim: exit %d; stderr=%s", code, stderr)
	}

	out, _, code := runActIn(t, dir, "finish", id, "--reason", "all done", "--json")
	if code != 0 {
		t.Fatalf("act finish --json: exit %d; out=%s", code, out)
	}
	var got struct {
		Closed  bool   `json:"closed"`
		ID      string `json:"id"`
		ShortID string `json:"short_id"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse finish json %q: %v", out, err)
	}
	if !got.Closed {
		t.Errorf("act finish: closed=false, want true; out=%s", out)
	}
	if got.ID != id {
		t.Errorf("act finish: id=%q, want %q; out=%s", got.ID, id, out)
	}
	if got.ShortID == "" {
		t.Errorf("act finish: short_id empty; out=%s", out)
	}
	if got.Reason != "all done" {
		t.Errorf("act finish: reason=%q, want %q; out=%s", got.Reason, "all done", out)
	}

	// Path verification: the close must have landed.
	if st := showField(t, dir, id, "status"); st != "closed" {
		t.Errorf("after act finish, show status = %q, want closed", st)
	}
}

// TestDocClaim_Finish_HelpSaysNoHostPush pins the act-802ef9 clarification:
// `act finish` (= `act close`) pushes the close op to the nested `.act/`
// tracker remote, NOT the host-repo work commit carrying the `Act-Id:`
// trailer. The help text must say so explicitly so a reader can't conclude
// their code was published by finish. Asserted at the user-visible boundary:
// the live `act help` output (what a cold-start agent reads), not the source
// string.
func TestDocClaim_Finish_HelpSaysNoHostPush(t *testing.T) {
	out, _, code := runAct(t, "help")
	if code != 0 {
		t.Fatalf("act help: exit %d", code)
	}
	if !strings.Contains(out, "does NOT push your host work commit") {
		t.Errorf("act help missing the no-host-push clarification for `act finish`:\n%s", out)
	}
	// The same surface must point the reader at the manual `git push` of their
	// code, so they don't assume finish published it.
	if !strings.Contains(out, "git push") {
		t.Errorf("act help should still tell the reader to `git push` their code themselves:\n%s", out)
	}
}

// TestFinish_AlreadyClosedIdempotent covers the idempotent re-finish: a second
// `act finish` on an already-closed issue exits 0 and reports already_closed.
func TestFinish_AlreadyClosedIdempotent(t *testing.T) {
	dir := bootstrapLoopRepo(t)
	id := createIssue(t, dir, "double finish")
	if _, _, code := runActIn(t, dir, "finish", id, "--json"); code != 0 {
		t.Fatalf("first act finish: exit %d", code)
	}
	out, _, code := runActIn(t, dir, "finish", id, "--json")
	if code != 0 {
		t.Fatalf("second act finish: exit %d; out=%s", code, out)
	}
	var got struct {
		Closed        bool `json:"closed"`
		AlreadyClosed bool `json:"already_closed"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse finish json %q: %v", out, err)
	}
	if !got.Closed || !got.AlreadyClosed {
		t.Errorf("second act finish: closed=%v already_closed=%v, want both true; out=%s",
			got.Closed, got.AlreadyClosed, out)
	}
}

// TestFinish_ReasonCapRejectedUpfront mirrors `act close`: a >500-byte --reason
// is rejected at flag-parse time (exit 2) before any repo discovery, with a
// message naming the byte cap.
func TestFinish_ReasonCapRejectedUpfront(t *testing.T) {
	reason := strings.Repeat("x", closeReasonMaxBytes+1)
	dir := t.TempDir() // no git init — the upfront check fires before discovery
	_, stderr, code := runActIn(t, dir, "finish", "act-deadbeef", "--reason", reason)
	if code != 2 {
		t.Fatalf("act finish over-cap: exit %d, want 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "act finish: --reason") || !strings.Contains(stderr, "byte cap") {
		t.Errorf("act finish over-cap: stderr should name the byte cap; got %q", stderr)
	}
}

// TestNextFinish_NoStateGuard locks in that `act next` and `act finish` are
// write commands: in a git repo with no .act/ state they hard-exit 3 with the
// no-state guard rather than silently no-op'ing.
func TestNextFinish_NoStateGuard(t *testing.T) {
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for _, args := range [][]string{
		{"next"},
		{"finish", "act-deadbeef"},
	} {
		_, stderr, code := runActIn(t, dir, args...)
		if code != 3 {
			t.Errorf("act %v with no .act/: exit %d, want 3; stderr=%s", args, code, stderr)
		}
		if !strings.Contains(stderr, "no act state") {
			t.Errorf("act %v: stderr should mention no-state guard; got %q", args, stderr)
		}
	}
}
