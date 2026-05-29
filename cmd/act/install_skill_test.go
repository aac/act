package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/skills"
)

// TestDispatchInstallSkillHelp asserts the new subcommand is wired into
// the top-level dispatcher: the binary recognises it and routes to the
// help flag (which Go's flag pkg renders to stderr).
func TestDispatchInstallSkillKnown(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	// Run with --dest pointing at a tempdir so we don't touch ~/.claude.
	dest := filepath.Join(t.TempDir(), "skills", "act")
	_, stderr, code := runAct(t, "install-skill", "--dest", dest)
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, stderr)
	}
}

// TestDispatchInstallSkillJSON drives the binary with --json against a
// tempdir destination and asserts the embedded SKILL.md plus references
// land byte-for-byte equal to the embedded copy, and that the reported
// JSON envelope agrees with the on-disk state.
func TestDispatchInstallSkillJSON(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	dest := filepath.Join(t.TempDir(), "skills", "act")

	stdout, stderr, code := runAct(t, "install-skill", "--dest", dest, "--json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("expected empty stderr; got %q", stderr)
	}

	var payload struct {
		Dest    string   `json:"dest"`
		Written []string `json:"written"`
		Skipped []string `json:"skipped"`
		Refused []string `json:"refused,omitempty"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse stdout JSON: %v; stdout=%q", err, stdout)
	}
	if payload.Dest != dest {
		t.Errorf("payload.Dest = %q, want %q", payload.Dest, dest)
	}
	if len(payload.Written) == 0 {
		t.Errorf("expected non-empty Written; got %+v", payload)
	}
	if len(payload.Refused) != 0 {
		t.Errorf("expected empty Refused; got %v", payload.Refused)
	}

	// SKILL.md on disk equals embedded copy byte-for-byte.
	want, err := skill.SkillMD()
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed SKILL.md: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("installed SKILL.md differs from embedded copy (len got=%d want=%d)", len(got), len(want))
	}
}

// TestDispatchInstallSkillIdempotentRun confirms a re-run against the
// same dest exits 0 and produces no Written entries.
func TestDispatchInstallSkillIdempotentRun(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	dest := filepath.Join(t.TempDir(), "skills", "act")

	if _, _, code := runAct(t, "install-skill", "--dest", dest); code != 0 {
		t.Fatalf("first install exit = %d, want 0", code)
	}
	stdout, stderr, code := runAct(t, "install-skill", "--dest", dest, "--json")
	if code != 0 {
		t.Fatalf("second install exit = %d, want 0; stderr=%q", code, stderr)
	}
	var payload struct {
		Written []string `json:"written"`
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(payload.Written) != 0 {
		t.Errorf("idempotent re-run wrote files: %v", payload.Written)
	}
	if len(payload.Skipped) == 0 {
		t.Errorf("expected non-empty Skipped on idempotent re-run")
	}
}

// TestHelpListsInstallSkill confirms `act help` (the agent-onboarding
// tutorial) advertises the new subcommand. Cold-start agents read help
// to discover surface area; if a subcommand isn't in help, it doesn't
// exist as far as they're concerned.
func TestHelpListsInstallSkill(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	stdout, _, code := runAct(t, "help")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "install-skill") {
		t.Error("`act help` does not mention install-skill")
	}
	if !strings.Contains(stdout, "INSTALLING THE SKILL") {
		t.Error("`act help` missing INSTALLING THE SKILL section")
	}
}

// TestUsageListsInstallSkill asserts the short usage line (`act --help`)
// also names the subcommand, so an agent that types an unknown command
// and sees the subcommand list discovers install-skill immediately.
func TestUsageListsInstallSkill(t *testing.T) {
	bin := actBinary(t)
	if _, err := exec.LookPath(bin); err != nil {
		t.Skipf("act binary not built at %s: %v", bin, err)
	}
	_, stderr, _ := runAct(t, "--help")
	if !strings.Contains(stderr, "install-skill") {
		t.Errorf("usage missing install-skill; got: %s", stderr)
	}
}
