package main

// Doc-claim regression tests for cmd/act user-visible surfaces (act-ddd458
// and related). Tests here drive the act binary as a subprocess, asserting
// behavior at the boundary an agent would hit — not at the internal Go API.
//
// Pattern: use runActIn / runAct from dispatch_test.go. Every test name
// starts with TestDocClaim_; the sweep registry in
// internal/cli/docs_sweep_test.go pins each claim to its test symbol.

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestDocClaim_IncludeOps_SubprocessShowsOpStream pins the --include-ops
// flag-help claim in cmd/act/main.go: "inline the HLC-sorted op stream
// alongside the snapshot". The existing TestRunShow_IncludeOpsHumanFormat
// calls RunShow() internally (not the CLI), so deleting it would not trip
// the doc-claim sweep. This test drives the full subprocess boundary:
//
//	act show --include-ops <id>
//
// and asserts that the op stream appears in the human-format output.
// Without the flag, the ops section must be absent.
func TestDocClaim_IncludeOps_SubprocessShowsOpStream(t *testing.T) {
	dir := t.TempDir()

	// Bootstrap a git repo and act state.
	if out, err := exec.Command("git", "init", "-q", "-b", "main", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "user.email", "test@example.com").CombinedOutput(); err != nil {
		t.Fatalf("git config email: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatalf("git config name: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "commit.gpgsign", "false").CombinedOutput(); err != nil {
		t.Fatalf("git config gpgsign: %v: %s", err, out)
	}

	_, stderr, code := runActIn(t, dir, "init", "--json")
	if code != 0 {
		t.Fatalf("act init: exit %d; stderr=%s", code, stderr)
	}

	// Create an issue so there's at least one op file on disk.
	createOut, _, code := runActIn(t, dir, "create", "include-ops probe", "--json")
	if code != 0 {
		t.Fatalf("act create: exit %d; stdout=%s", code, createOut)
	}
	// Extract the issue id from the JSON output.
	idPat := strings.Index(createOut, `"id":"`)
	if idPat < 0 {
		idPat = strings.Index(createOut, `"id": "`)
	}
	if idPat < 0 {
		t.Fatalf("could not find id in create output: %s", createOut)
	}
	idStart := idPat + len(`"id":"`)
	if createOut[idPat+5] == ' ' {
		idStart = idPat + len(`"id": "`)
	}
	idEnd := strings.Index(createOut[idStart:], `"`)
	if idEnd < 0 {
		t.Fatalf("could not parse id from create output: %s", createOut)
	}
	id := createOut[idStart : idStart+idEnd]
	if id == "" {
		t.Fatalf("empty id from create output: %s", createOut)
	}

	// With --include-ops: the "ops:" section header must appear in stdout.
	withOpsOut, _, code := runActIn(t, dir, "show", id, "--include-ops")
	if code != 0 {
		t.Fatalf("act show --include-ops: exit %d; stdout=%s", code, withOpsOut)
	}
	if !strings.Contains(withOpsOut, "ops:") {
		t.Errorf("act show --include-ops: stdout missing 'ops:' section header\n%s", withOpsOut)
	}
	// The create op must appear in the op stream.
	if !strings.Contains(withOpsOut, "create") {
		t.Errorf("act show --include-ops: stdout missing 'create' op entry\n%s", withOpsOut)
	}

	// Without --include-ops: no "ops:" section in the output.
	withoutOpsOut, _, code := runActIn(t, dir, "show", id)
	if code != 0 {
		t.Fatalf("act show (no flag): exit %d; stdout=%s", code, withoutOpsOut)
	}
	if strings.Contains(withoutOpsOut, "ops:") {
		t.Errorf("act show without --include-ops should not emit 'ops:' section\n%s", withoutOpsOut)
	}
}

// TestDocClaim_Help_ListsMigrateToNested pins the claim that `act help`
// lists `migrate-to-nested` in the subcommand listing. The subcommand is
// real (cmd/act/migrate_to_nested.go) but was absent from the help overview
// listing, making it undiscoverable for help-first readers.
//
// Asserted at the subprocess stdout boundary so a future split of the
// helpOverview constant still has to keep the listing text.
func TestDocClaim_Help_ListsMigrateToNested(t *testing.T) {
	dir := t.TempDir()
	out, _, code := runActIn(t, dir, "help")
	if code != 0 {
		t.Fatalf("act help: exit %d", code)
	}
	if !strings.Contains(out, "migrate-to-nested") {
		t.Errorf("act help: output does not list 'migrate-to-nested' subcommand\n%s", out)
	}
}

// TestDocClaim_HelpErrors_ExitCodesListsThreeFourFive pins the claim that
// `act help errors` documents exit 3 (issue_not_found), exit 4
// (push_exhausted), and exit 5 (claim_lost). Before the exit-3/4 fix the
// EXIT CODES block listed only exits 1 and 2; act-a373bb added exit 5 for
// the reconciled claim_lost code.
//
// remote_unreachable is intentionally NOT asserted here: it is not a
// close/push-path (exit-4) outcome. PushWithRetry collapses fetch failures
// into push_exhausted, so the only emitter is `act state import`
// (clone failure, exit 3). See act-6d9546; the EXIT CODES block no longer
// lists it under exit 4.
//
// Semantics verified against docs/spec-v2.md error table:
//   - exit 3: issue_not_found
//   - exit 4: push_exhausted
//   - exit 5: claim_lost
func TestDocClaim_HelpErrors_ExitCodesListsThreeFourFive(t *testing.T) {
	dir := t.TempDir()
	out, _, code := runActIn(t, dir, "help", "errors")
	if code != 0 {
		t.Fatalf("act help errors: exit %d", code)
	}

	for _, want := range []string{
		"exit 3",
		"issue_not_found",
		"exit 4",
		"push_exhausted",
		"exit 5",
		"claim_lost",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("act help errors: missing %q in EXIT CODES section\n%s", want, out)
		}
	}
}

// TestDocClaim_Help_NoBareTrackerIDs asserts that the user-visible `act help`
// output (overview, workflow, ops-model, errors) contains no bare internal
// tracker IDs of the form act-[0-9a-f]{4,}. Internal IDs are agent-side
// bookkeeping; a stranger cannot resolve them and they read as noise.
//
// Exception carved out: IDs in an example session context (surrounded by
// "$ act" shell prompt lines) are illustrative and harmless; the regex
// match is preceded by a non-"$" guard so those are not flagged.
//
// This is a freestanding TestDocClaim_* test (no registry entry) because
// it is an ABSENCE property: there is no "claimPattern must appear in
// docFile" framing that makes sense for it. The sweep's
// TestDocSweep_NoOrphanedDocClaimTests would flag a registry-less test as
// an orphan, so this test is listed in the crossRepoDocClaimTests opt-out
// map instead.
//
// Note for maintainers: if you need to add an example ID (like act-c26a01)
// to help text, put it inside an "EXAMPLE SESSION" block; the test only
// scans lines that are NOT within an example session block.
func TestDocClaim_Help_NoBareTrackerIDs(t *testing.T) {
	// Regex that matches a bare tracker ID anywhere on a line, except
	// within example-session shell prompt lines (those start with "  $").
	bareID := regexp.MustCompile(`act-[0-9a-f]{4,}`)

	dir := t.TempDir()
	topics := []string{"", "workflow", "ops-model", "errors"}
	for _, topic := range topics {
		var args []string
		if topic == "" {
			args = []string{"help"}
		} else {
			args = []string{"help", topic}
		}
		out, _, code := runActIn(t, dir, args...)
		if code != 0 {
			t.Fatalf("act help %s: exit %d", topic, code)
		}

		inExample := false
		for _, line := range strings.Split(out, "\n") {
			// Track entry/exit of EXAMPLE SESSION blocks.
			if strings.Contains(line, "EXAMPLE SESSION") {
				inExample = true
			}
			// Any all-caps section header (≥3 caps, no lowercase) ends the
			// example block.
			if inExample && !strings.Contains(line, "EXAMPLE SESSION") {
				// A new section header is all-uppercase words; use a simple
				// heuristic: line starts with an uppercase word of 4+ chars
				// and is not indented.
				trimmed := strings.TrimSpace(line)
				if len(trimmed) > 3 && trimmed == strings.ToUpper(trimmed) && trimmed[0] >= 'A' && trimmed[0] <= 'Z' {
					inExample = false
				}
			}
			if inExample {
				continue
			}
			if bareID.FindString(line) != "" {
				t.Errorf("act help %s: bare tracker ID found outside example block: %q", topic, line)
			}
		}
	}
}

// bootstrapAcceptRepo inits a fresh git+act repo in a temp dir and creates one
// issue carrying the supplied initial acceptance criteria (via repeated
// --accept on create, the additive create flow). Returns (dir, issueID).
func bootstrapAcceptRepo(t *testing.T, initialAccept ...string) (string, string) {
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
	createArgs := []string{"create", "accept-semantics probe", "--json"}
	for _, c := range initialAccept {
		createArgs = append(createArgs, "--accept", c)
	}
	createOut, _, code := runActIn(t, dir, createArgs...)
	if code != 0 {
		t.Fatalf("act create: exit %d; stdout=%s", code, createOut)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(createOut), &created); err != nil || created.ID == "" {
		t.Fatalf("parse create id from %q: %v", createOut, err)
	}
	return dir, created.ID
}

// showAccept drives `act show <id> --json` and returns the materialized
// acceptance_criteria list (the rendered `accept` field) at the subprocess
// boundary — NOT via the fold helper.
func showAccept(t *testing.T, dir, id string) []string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 {
		t.Fatalf("act show --json: exit %d; stderr=%s", code, stderr)
	}
	var got struct {
		Accept []string `json:"accept"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("parse show json %q: %v", out, err)
	}
	return got.Accept
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDocClaim_AcceptReplace_SetReplacesNotUnion pins the user-visible claim
// (cmd/act/update.go --accept flag-help, MCP act_update.accept schema, spec
// §"act update") that `act update --accept [...]` REPLACES the acceptance list
// rather than unioning with prior criteria.
//
// The boundary test drives `act update --accept` twice with DIFFERENT sets and
// asserts the materialized accept list (read via `act show --json`) equals the
// LAST set — not the union. This is the exact drift the doc-discipline rule
// guards: before the fix, --accept emitted one add_accept per criterion and the
// list accumulated across edits.
func TestDocClaim_AcceptReplace_SetReplacesNotUnion(t *testing.T) {
	dir, id := bootstrapAcceptRepo(t, "original A", "original B")

	// First update: replace with set 1.
	if out, stderr, code := runActIn(t, dir, "update", id, "--accept", "first-1", "--accept", "first-2"); code != 0 {
		t.Fatalf("act update --accept (set 1): exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	if got := showAccept(t, dir, id); !equalStrings(got, []string{"first-1", "first-2"}) {
		t.Fatalf("after first --accept: got %v, want [first-1 first-2] (create's original criteria must be replaced, not unioned)", got)
	}

	// Second update: replace with set 2 (entirely different).
	if out, stderr, code := runActIn(t, dir, "update", id, "--accept", "second-1"); code != 0 {
		t.Fatalf("act update --accept (set 2): exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	got := showAccept(t, dir, id)
	if !equalStrings(got, []string{"second-1"}) {
		t.Errorf("after second --accept: got %v, want [second-1] (the LAST set, not the union of all sets ever passed)", got)
	}
	// Defensive: none of the prior criteria may survive.
	for _, stale := range []string{"original A", "original B", "first-1", "first-2"} {
		for _, c := range got {
			if c == stale {
				t.Errorf("stale criterion %q survived a --accept replace: %v", stale, got)
			}
		}
	}
}

// TestDocClaim_AcceptRm_RemovesIndividualCriterion pins the claim (cmd/act/
// update.go --accept-rm flag-help, MCP act_update.accept_rm schema, spec
// §"act update") that there is a non-destructive way to remove an individual
// acceptance criterion: `act update --accept-rm <index>` drops exactly the
// criterion at that zero-based index, leaving the others intact.
func TestDocClaim_AcceptRm_RemovesIndividualCriterion(t *testing.T) {
	dir, id := bootstrapAcceptRepo(t, "keep-0", "drop-1", "keep-2")

	if out, stderr, code := runActIn(t, dir, "update", id, "--accept-rm", "1"); code != 0 {
		t.Fatalf("act update --accept-rm: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	got := showAccept(t, dir, id)
	if !equalStrings(got, []string{"keep-0", "keep-2"}) {
		t.Errorf("after --accept-rm 1: got %v, want [keep-0 keep-2] (only the indexed criterion removed)", got)
	}

	// Replace-an-individual-criterion: --accept-rm + --accept-add in one call.
	if out, stderr, code := runActIn(t, dir, "update", id, "--accept-rm", "0", "--accept-add", "replacement"); code != 0 {
		t.Fatalf("act update --accept-rm+--accept-add: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	got = showAccept(t, dir, id)
	// remove_accept resolves index against the effective list BEFORE the add
	// is folded; both ops carry independent HLCs. The net effective list is
	// {keep-2, replacement}.
	if !equalStrings(got, []string{"keep-2", "replacement"}) {
		t.Errorf("after replace (rm 0 + add): got %v, want [keep-2 replacement]", got)
	}
}

// TestDocClaim_AcceptAdd_AppendsToList pins the claim (cmd/act/update.go
// --accept-add flag-help, MCP act_update.accept_add schema) that --accept-add
// preserves the additive flow: it appends to the existing list rather than
// replacing it.
func TestDocClaim_AcceptAdd_AppendsToList(t *testing.T) {
	dir, id := bootstrapAcceptRepo(t, "base-0")

	if out, stderr, code := runActIn(t, dir, "update", id, "--accept-add", "added-1", "--accept-add", "added-2"); code != 0 {
		t.Fatalf("act update --accept-add: exit %d; stdout=%s stderr=%s", code, out, stderr)
	}
	got := showAccept(t, dir, id)
	if !equalStrings(got, []string{"base-0", "added-1", "added-2"}) {
		t.Errorf("after --accept-add: got %v, want [base-0 added-1 added-2] (additive, not replace)", got)
	}
}
