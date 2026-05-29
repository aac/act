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
