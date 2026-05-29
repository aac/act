package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/skills"
)

// TestRunInstallSkillFreshDest covers the canonical case: a clean
// destination directory ends up with the full embedded tree, byte-for-byte.
func TestRunInstallSkillFreshDest(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")

	out, code := RunInstallSkill(InstallSkillOptions{Dest: dest})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %+v", code, out)
	}

	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if res.Dest != dest {
		t.Errorf("Dest = %q, want %q", res.Dest, dest)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("expected no skipped on fresh dest; got %v", res.Skipped)
	}
	if len(res.Refused) != 0 {
		t.Errorf("expected no refused on fresh dest; got %v", res.Refused)
	}

	// Walk the embedded tree and assert every regular file landed on disk
	// with bytes equal to the embedded copy. This is the byte-for-byte
	// invariant the spec requires.
	root := skill.FS()
	if walkErr := fs.WalkDir(root, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." || d.IsDir() {
			return nil
		}
		want, rerr := fs.ReadFile(root, p)
		if rerr != nil {
			t.Fatalf("read embedded %s: %v", p, rerr)
		}
		got, gerr := os.ReadFile(filepath.Join(dest, filepath.FromSlash(p)))
		if gerr != nil {
			t.Fatalf("read installed %s: %v", p, gerr)
		}
		if string(got) != string(want) {
			t.Errorf("installed %s differs from embedded copy", p)
		}
		return nil
	}); walkErr != nil {
		t.Fatalf("walk embedded: %v", walkErr)
	}

	// SKILL.md specifically is the load-bearing file every agent reads.
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Fatalf("expected SKILL.md at install root: %v", err)
	}
	// At least one reference file should have landed (currently setup.md,
	// worktree-subagents.md); assert the directory exists and contains
	// matching content.
	refs, err := os.ReadDir(filepath.Join(dest, "references"))
	if err != nil {
		t.Fatalf("read references dir: %v", err)
	}
	if len(refs) == 0 {
		t.Error("references directory is empty after install")
	}
}

// TestRunInstallSkillIdempotent re-runs install against a populated dest
// and confirms every file ends up under Skipped (none under Written or
// Refused).
func TestRunInstallSkillIdempotent(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")

	if _, code := RunInstallSkill(InstallSkillOptions{Dest: dest}); code != 0 {
		t.Fatalf("first install exit = %d, want 0", code)
	}
	out, code := RunInstallSkill(InstallSkillOptions{Dest: dest})
	if code != 0 {
		t.Fatalf("second install exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if len(res.Written) != 0 {
		t.Errorf("second install wrote files: %v (expected all skipped)", res.Written)
	}
	if len(res.Skipped) == 0 {
		t.Error("expected at least one skipped file on idempotent re-run")
	}
}

// TestRunInstallSkillRefusesOnDiff verifies the safety policy: a
// destination file with different contents is left alone and listed
// under Refused; exit code becomes 1 so callers detect the partial state.
func TestRunInstallSkillRefusesOnDiff(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tampered := []byte("# this file was edited locally\n")
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), tampered, 0o644); err != nil {
		t.Fatalf("seed tampered SKILL.md: %v", err)
	}

	out, code := RunInstallSkill(InstallSkillOptions{Dest: dest})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out = %+v", code, out)
	}
	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	foundRefused := false
	for _, p := range res.Refused {
		if strings.HasSuffix(p, "SKILL.md") {
			foundRefused = true
		}
	}
	if !foundRefused {
		t.Errorf("expected SKILL.md under Refused; got refused=%v written=%v skipped=%v", res.Refused, res.Written, res.Skipped)
	}
	// Tampered content must remain on disk untouched.
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read post-install SKILL.md: %v", err)
	}
	if string(got) != string(tampered) {
		t.Error("tampered SKILL.md was overwritten without --force")
	}
}

// TestRunInstallSkillForceOverwrites verifies --force replaces a
// diverged file with the embedded copy and reports it under Written.
func TestRunInstallSkillForceOverwrites(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, code := RunInstallSkill(InstallSkillOptions{Dest: dest, Force: true})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if len(res.Refused) != 0 {
		t.Errorf("--force should leave no refused; got %v", res.Refused)
	}
	// SKILL.md should now equal the embedded copy.
	want, err := skill.SkillMD()
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed SKILL.md: %v", err)
	}
	if string(got) != string(want) {
		t.Error("--force did not restore SKILL.md to embedded copy")
	}
}

// TestRunInstallSkillCreatesParentDir confirms a destination several
// levels deep is created on the fly.
func TestRunInstallSkillCreatesParentDir(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a", "b", "c", "skills", "act")
	if _, code := RunInstallSkill(InstallSkillOptions{Dest: dest}); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing under nested dest: %v", err)
	}
}

// TestRunInstallSkillTargetClaude verifies --target claude (explicit) resolves
// to ~/.claude/skills/act/ — same destination as the default (no --target flag).
// Uses HOME override via t.TempDir to avoid touching the real home directory.
func TestRunInstallSkillTargetClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out, code := RunInstallSkill(InstallSkillOptions{Target: "claude"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	wantDest := filepath.Join(home, ".claude", "skills", "act")
	if res.Dest != wantDest {
		t.Errorf("Dest = %q, want %q", res.Dest, wantDest)
	}
	if _, err := os.Stat(filepath.Join(res.Dest, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing at claude target: %v", err)
	}
}

// TestRunInstallSkillTargetCodex verifies --target codex resolves to
// ~/.codex/skills/act/ and installs the embedded tree there.
// Uses HOME override via t.TempDir to avoid touching the real home directory.
func TestRunInstallSkillTargetCodex(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out, code := RunInstallSkill(InstallSkillOptions{Target: "codex"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	wantDest := filepath.Join(home, ".codex", "skills", "act")
	if res.Dest != wantDest {
		t.Errorf("Dest = %q, want %q", res.Dest, wantDest)
	}
	if _, err := os.Stat(filepath.Join(res.Dest, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md missing at codex target: %v", err)
	}
	// Verify embedded bytes landed correctly.
	want, err := skill.SkillMD()
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(res.Dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read installed SKILL.md: %v", err)
	}
	if string(got) != string(want) {
		t.Error("codex target: installed SKILL.md differs from embedded copy")
	}
}

// TestRunInstallSkillDefaultPreservesClaude verifies the default (no Target,
// no Dest) still resolves to ~/.claude/skills/act — preserving the
// pre-act-8550 byte-for-byte install location.
func TestRunInstallSkillDefaultPreservesClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	out, code := RunInstallSkill(InstallSkillOptions{})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	wantDest := filepath.Join(home, ".claude", "skills", "act")
	if res.Dest != wantDest {
		t.Errorf("default Dest = %q, want %q (default target must remain claude)", res.Dest, wantDest)
	}
}

// TestRunInstallSkillDestOverridesTarget verifies --dest takes precedence
// over --target: when both are supplied the explicit dest wins.
func TestRunInstallSkillDestOverridesTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	explicit := filepath.Join(t.TempDir(), "my", "custom", "skills")

	out, code := RunInstallSkill(InstallSkillOptions{
		Dest:   explicit,
		Target: "codex",
	})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(InstallSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if res.Dest != explicit {
		t.Errorf("Dest = %q, want explicit %q (--dest should override --target)", res.Dest, explicit)
	}
	// The codex path must NOT have been written (HOME is the temp dir so
	// nothing would be there anyway, but confirm the returned dest is right).
	codexPath := filepath.Join(home, ".codex", "skills", "act")
	if _, err := os.Stat(codexPath); !os.IsNotExist(err) {
		t.Errorf("unexpected write to codex path %s when --dest was supplied", codexPath)
	}
}

// TestRunInstallSkillUnknownTarget verifies an unknown --target value returns
// exit 2 with a bad_flag error envelope.
func TestRunInstallSkillUnknownTarget(t *testing.T) {
	out, code := RunInstallSkill(InstallSkillOptions{Target: "unknown-host"})
	if code != 2 {
		t.Fatalf("exit = %d, want 2; out = %+v", code, out)
	}
	env, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if env["error"] != "bad_flag" {
		t.Errorf("error = %q, want bad_flag; env=%v", env["error"], env)
	}
	msg, _ := env["message"].(string)
	if !strings.Contains(msg, "unknown --target") {
		t.Errorf("message should mention 'unknown --target'; got %q", msg)
	}
}

// TestRunInstallSkillCheckMatchesAfterInstall verifies --check exits 0 when
// the installed skill matches the embedded copy.
func TestRunInstallSkillCheckMatchesAfterInstall(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")
	if _, code := RunInstallSkill(InstallSkillOptions{Dest: dest}); code != 0 {
		t.Fatalf("install failed: exit = %d", code)
	}

	out, code := RunInstallSkill(InstallSkillOptions{Dest: dest, Check: true})
	if code != 0 {
		t.Fatalf("--check after install: exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(CheckSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if len(res.Drift) != 0 {
		t.Errorf("--check: unexpected drift: %v", res.Drift)
	}
	if len(res.Missing) != 0 {
		t.Errorf("--check: unexpected missing: %v", res.Missing)
	}
	if len(res.Match) == 0 {
		t.Error("--check: expected at least one match entry")
	}
	if res.Dest != dest {
		t.Errorf("--check: Dest = %q, want %q", res.Dest, dest)
	}
}

// TestRunInstallSkillCheckDetectsDrift verifies --check exits 1 and reports
// drift when an installed file differs from the embedded copy.
func TestRunInstallSkillCheckDetectsDrift(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")
	if _, code := RunInstallSkill(InstallSkillOptions{Dest: dest}); code != 0 {
		t.Fatalf("install failed: exit = %d", code)
	}
	// Tamper one file.
	if err := os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte("drift\n"), 0o644); err != nil {
		t.Fatalf("tamper SKILL.md: %v", err)
	}

	out, code := RunInstallSkill(InstallSkillOptions{Dest: dest, Check: true})
	if code != 1 {
		t.Fatalf("--check with drift: exit = %d, want 1; out = %+v", code, out)
	}
	res, ok := out.(CheckSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	foundDrift := false
	for _, p := range res.Drift {
		if strings.HasSuffix(p, "SKILL.md") {
			foundDrift = true
		}
	}
	if !foundDrift {
		t.Errorf("--check: SKILL.md should be in Drift; got %v", res.Drift)
	}
	// --check must never write.
	got, err := os.ReadFile(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		t.Fatalf("read post-check SKILL.md: %v", err)
	}
	if string(got) != "drift\n" {
		t.Error("--check overwrote the tampered file; it must be read-only")
	}
}

// TestRunInstallSkillCheckDetectsMissing verifies --check exits 1 and reports
// missing when an expected file is absent from the destination.
func TestRunInstallSkillCheckDetectsMissing(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "skills", "act")
	// Do NOT install first — all files are missing.

	out, code := RunInstallSkill(InstallSkillOptions{Dest: dest, Check: true})
	if code != 1 {
		t.Fatalf("--check on empty dest: exit = %d, want 1; out = %+v", code, out)
	}
	res, ok := out.(CheckSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	if len(res.Missing) == 0 {
		t.Errorf("--check on empty dest should report missing files; got %v", res.Missing)
	}
	foundMissing := false
	for _, p := range res.Missing {
		if strings.HasSuffix(p, "SKILL.md") {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Errorf("--check: SKILL.md should be in Missing; got %v", res.Missing)
	}
}

// TestRunInstallSkillCheckHonorsTarget verifies --check + --target codex
// checks the codex path, not the claude path.
func TestRunInstallSkillCheckHonorsTarget(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Install to codex target first.
	if _, code := RunInstallSkill(InstallSkillOptions{Target: "codex"}); code != 0 {
		t.Fatalf("codex install failed: exit = %d", code)
	}

	// Check codex target: should match.
	out, code := RunInstallSkill(InstallSkillOptions{Target: "codex", Check: true})
	if code != 0 {
		t.Fatalf("--check --target codex: exit = %d, want 0; out = %+v", code, out)
	}
	res, ok := out.(CheckSkillResult)
	if !ok {
		t.Fatalf("unexpected output type %T", out)
	}
	wantDest := filepath.Join(home, ".codex", "skills", "act")
	if res.Dest != wantDest {
		t.Errorf("--check --target codex: Dest = %q, want %q", res.Dest, wantDest)
	}
	if len(res.Missing) > 0 || len(res.Drift) > 0 {
		t.Errorf("--check --target codex: unexpected missing=%v drift=%v after install", res.Missing, res.Drift)
	}

	// Check claude target (not installed): should report missing.
	out2, code2 := RunInstallSkill(InstallSkillOptions{Target: "claude", Check: true})
	if code2 != 1 {
		t.Fatalf("--check --target claude (not installed): exit = %d, want 1; out = %+v", code2, out2)
	}
	res2, ok2 := out2.(CheckSkillResult)
	if !ok2 {
		t.Fatalf("unexpected output type %T", out2)
	}
	claudeDest := filepath.Join(home, ".claude", "skills", "act")
	if res2.Dest != claudeDest {
		t.Errorf("--check --target claude: Dest = %q, want %q", res2.Dest, claudeDest)
	}
	if len(res2.Missing) == 0 {
		t.Error("--check --target claude (not installed): should report missing files")
	}
}
