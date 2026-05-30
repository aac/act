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
	_, stderr, _ := runAct(t, "--help")
	if !strings.Contains(stderr, "install-skill") {
		t.Errorf("usage missing install-skill; got: %s", stderr)
	}
}

// runActWithHome runs the act binary with args and overrides HOME to the given
// path so install-skill default target resolution doesn't touch the real home.
func runActWithHome(t *testing.T, home string, args ...string) (string, string, int) {
	t.Helper()
	bin := actBinary(t)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	var outB, errB strings.Builder
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return outB.String(), errB.String(), ee.ExitCode()
		}
		t.Fatalf("act %v: failed to launch: %v", args, err)
	}
	return outB.String(), errB.String(), 0
}

// TestDocClaim_InstallSkill_DefaultPreservesClaude verifies the default target
// (no --target, no --dest) installs to ~/.claude/skills/act/ — the pre-act-8550
// location. Preserving this ensures no silent breakage for existing users.
// Asserted at the subprocess boundary with HOME override so no real ~/.claude
// is touched.
func TestDocClaim_InstallSkill_DefaultPreservesClaude(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runActWithHome(t, home, "install-skill", "--json")
	if code != 0 {
		t.Fatalf("default install: exit = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		Dest string `json:"dest"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse JSON: %v; stdout=%q", err, stdout)
	}
	wantDest := filepath.Join(home, ".claude", "skills", "act")
	if payload.Dest != wantDest {
		t.Errorf("default install: Dest = %q, want %q (default target must remain claude)", payload.Dest, wantDest)
	}
	if _, err := os.Stat(filepath.Join(wantDest, "SKILL.md")); err != nil {
		t.Errorf("default install: SKILL.md missing at claude path: %v", err)
	}
}

// TestDocClaim_InstallSkill_TargetCodex verifies --target codex installs to
// ~/.codex/skills/act/ and that SKILL.md lands there byte-for-byte.
// Asserted at the subprocess boundary with HOME override.
func TestDocClaim_InstallSkill_TargetCodex(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runActWithHome(t, home, "install-skill", "--target", "codex", "--json")
	if code != 0 {
		t.Fatalf("--target codex: exit = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		Dest    string   `json:"dest"`
		Written []string `json:"written"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse JSON: %v; stdout=%q", err, stdout)
	}
	wantDest := filepath.Join(home, ".codex", "skills", "act")
	if payload.Dest != wantDest {
		t.Errorf("--target codex: Dest = %q, want %q", payload.Dest, wantDest)
	}
	if len(payload.Written) == 0 {
		t.Errorf("--target codex: expected non-empty Written; got %+v", payload)
	}
	want, err := skill.SkillMD()
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(wantDest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read codex SKILL.md: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("--target codex: SKILL.md differs from embedded copy")
	}
}

// TestDocClaim_InstallSkill_CheckMatchAfterInstall verifies --check exits 0
// when the skill has been installed and matches the embedded copy.
// Asserted at the subprocess boundary using --dest.
func TestDocClaim_InstallSkill_CheckMatchAfterInstall(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")

	if _, _, code := runAct(t, "install-skill", "--dest", dest); code != 0 {
		t.Fatalf("install: exit = %d, want 0", code)
	}
	stdout, stderr, code := runAct(t, "install-skill", "--dest", dest, "--check", "--json")
	if code != 0 {
		t.Fatalf("--check after install: exit = %d, want 0; stdout=%q stderr=%q", code, stdout, stderr)
	}
	var payload struct {
		Dest    string   `json:"dest"`
		Match   []string `json:"match"`
		Drift   []string `json:"drift"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse JSON: %v; stdout=%q", err, stdout)
	}
	if payload.Dest != dest {
		t.Errorf("--check: Dest = %q, want %q", payload.Dest, dest)
	}
	if len(payload.Match) == 0 {
		t.Errorf("--check: expected non-empty Match; got %+v", payload)
	}
	if len(payload.Drift) != 0 {
		t.Errorf("--check: unexpected Drift: %v", payload.Drift)
	}
	if len(payload.Missing) != 0 {
		t.Errorf("--check: unexpected Missing: %v", payload.Missing)
	}
}

// TestDocClaim_InstallSkill_CheckDetectsDrift verifies --check exits 1 when an
// installed file has drifted from the embedded copy, and never overwrites.
// Asserted at the subprocess boundary.
func TestDocClaim_InstallSkill_CheckDetectsDrift(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")

	// Install first, then tamper.
	if _, _, code := runAct(t, "install-skill", "--dest", dest); code != 0 {
		t.Fatalf("install: exit = %d, want 0", code)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("drifted\n"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	stdout, _, code := runAct(t, "install-skill", "--dest", dest, "--check", "--json")
	if code != 1 {
		t.Fatalf("--check with drift: exit = %d, want 1; stdout=%q", code, stdout)
	}
	var payload struct {
		Drift []string `json:"drift"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse JSON: %v; stdout=%q", err, stdout)
	}
	foundDrift := false
	for _, p := range payload.Drift {
		if strings.HasSuffix(p, "SKILL.md") {
			foundDrift = true
		}
	}
	if !foundDrift {
		t.Errorf("--check: SKILL.md should be in Drift; got %v", payload.Drift)
	}
	// --check must never write.
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read post-check SKILL.md: %v", err)
	}
	if string(got) != "drifted\n" {
		t.Error("--check overwrote the drifted file; it must be read-only")
	}
}

// TestDocClaim_InstallSkill_CheckHonorsTargetCodex verifies
// `act install-skill --check --target codex` checks the codex path.
// Asserted at the subprocess boundary with HOME override.
func TestDocClaim_InstallSkill_CheckHonorsTargetCodex(t *testing.T) {
	home := t.TempDir()

	// Install to codex first.
	if _, _, code := runActWithHome(t, home, "install-skill", "--target", "codex"); code != 0 {
		t.Fatalf("codex install: exit = %d, want 0", code)
	}

	// --check --target codex should exit 0 (matches).
	stdout, _, code := runActWithHome(t, home, "install-skill", "--target", "codex", "--check", "--json")
	if code != 0 {
		t.Fatalf("--check --target codex: exit = %d, want 0; stdout=%q", code, stdout)
	}
	var payload struct {
		Dest    string   `json:"dest"`
		Missing []string `json:"missing"`
		Drift   []string `json:"drift"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("parse JSON: %v; stdout=%q", err, stdout)
	}
	wantDest := filepath.Join(home, ".codex", "skills", "act")
	if payload.Dest != wantDest {
		t.Errorf("--check --target codex: Dest = %q, want %q", payload.Dest, wantDest)
	}
	if len(payload.Missing) > 0 || len(payload.Drift) > 0 {
		t.Errorf("--check --target codex: unexpected missing=%v drift=%v", payload.Missing, payload.Drift)
	}

	// --check --target claude (not installed): should exit 1 with missing.
	stdout2, _, code2 := runActWithHome(t, home, "install-skill", "--target", "claude", "--check", "--json")
	if code2 != 1 {
		t.Fatalf("--check --target claude (not installed): exit = %d, want 1; stdout=%q", code2, stdout2)
	}
	var payload2 struct {
		Dest    string   `json:"dest"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal([]byte(stdout2), &payload2); err != nil {
		t.Fatalf("parse JSON: %v; stdout=%q", err, stdout2)
	}
	wantClaudeDest := filepath.Join(home, ".claude", "skills", "act")
	if payload2.Dest != wantClaudeDest {
		t.Errorf("--check --target claude: Dest = %q, want %q", payload2.Dest, wantClaudeDest)
	}
	if len(payload2.Missing) == 0 {
		t.Error("--check --target claude (not installed): should report missing files")
	}
}
