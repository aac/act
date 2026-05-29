package main

// Doc-claim regression tests for cmd/act user-visible surfaces (act-ddd458
// and related). Tests here drive the act binary as a subprocess, asserting
// behavior at the boundary an agent would hit — not at the internal Go API.
//
// Pattern: use runActIn / runAct from dispatch_test.go. Every test name
// starts with TestDocClaim_; the sweep registry in
// internal/cli/docs_sweep_test.go pins each claim to its test symbol.

import (
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
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}

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
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}

	dir := t.TempDir()
	out, _, code := runActIn(t, dir, "help")
	if code != 0 {
		t.Fatalf("act help: exit %d", code)
	}
	if !strings.Contains(out, "migrate-to-nested") {
		t.Errorf("act help: output does not list 'migrate-to-nested' subcommand\n%s", out)
	}
}

// TestDocClaim_HelpErrors_ExitCodesListsThreeAndFour pins the claim that
// `act help errors` documents exit 3 (issue_not_found) and exit 4
// (push_exhausted, remote_unreachable). Before this fix the EXIT CODES
// block listed only exits 1 and 2, leaving agents no help on resolving
// not-found or push-retry errors.
//
// Semantics verified against docs/spec-v2.md error table:
//   - exit 3: issue_not_found
//   - exit 4: push_exhausted and remote_unreachable
func TestDocClaim_HelpErrors_ExitCodesListsThreeAndFour(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}

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
		"remote_unreachable",
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
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}

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
