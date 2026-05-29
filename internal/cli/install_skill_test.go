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
