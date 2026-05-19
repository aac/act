package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeLegacyActRepo builds a tempdir-shaped fixture that mirrors the
// pre-Phase-1 single-repo layout: an outer host git repo with `.act/` and
// some op files tracked as host commits, no nested `.act/.git`, no
// gitignore entry, no pre-commit hook. This is what existing act-using
// repos look like before they run `act migrate-to-nested`.
//
// The fixture is the minimum that exercises the migration end-to-end:
// `.act/config.json` (so migration's "already initialized" sentinel
// passes), one op file under `.act/ops/act-deadbeef/`, an initial host
// commit that tracks both, and an explicitly-clean working tree.
func makeLegacyActRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	// `git init -b main` with local identity so commits don't fail on
	// hosts without global user.{name,email}.
	runOrFatal(t, root, "git", "init", "-q", "-b", "main")
	runOrFatal(t, root, "git", "config", "user.email", "u@example.com")
	runOrFatal(t, root, "git", "config", "user.name", "U")
	runOrFatal(t, root, "git", "config", "commit.gpgsign", "false")

	// Lay down a minimal `.act/` tree: config.json + one op file. Match
	// the structure act actually writes — `.act/ops/<id>/<yyyy-mm>/<file>.json`.
	if err := os.MkdirAll(filepath.Join(root, ".act", "ops", "act-deadbeef", "2026-05"), 0o755); err != nil {
		t.Fatalf("mkdir ops: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".act", "config.json"),
		[]byte(`{"node_id":"00000000-0000-0000-0000-000000000000","writer_version":"0.1.0","created_at":"2026-05-01T00:00:00.000Z","last_hlc":{"wall":"","logical":0,"node_id":""}}`+"\n"),
		0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	// Op envelope must parse: hlc.node_id is exactly 8 chars; payload
	// matches the create op shape. Keeps the legacy fixture replayable
	// by fold/doctor so default-doctor tests don't bail on a fold error
	// before reaching nested-layout.
	if err := os.WriteFile(filepath.Join(root, ".act", "ops", "act-deadbeef", "2026-05",
		"2026-05-01T00:00:00.000Z-deadbeef-create.json"),
		[]byte(`{"op_version":1,"schema_version":1,"op_type":"create","issue_id":"act-deadbeef","node_id":"deadbeef","writer_version":"0.1.0","hlc":{"wall":"2026-05-01T00:00:00.000Z","logical":0,"node_id":"deadbeef"},"payload":{"title":"legacy","type":"task","priority":3}}`+"\n"),
		0o644); err != nil {
		t.Fatalf("write op: %v", err)
	}

	// Track the `.act/` tree from the host repo — this is the legacy shape.
	runOrFatal(t, root, "git", "add", "-A")
	runOrFatal(t, root, "git", "commit", "-q", "--no-verify", "-m", "initial: tracked .act/ (legacy)")
	return root
}

// runOrFatal is a thin wrapper over exec.Command that fails the test on
// non-zero exit. Used by the fixture builders below.
func runOrFatal(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
	}
}

func TestRunMigrateToNested_HappyPath(t *testing.T) {
	root := makeLegacyActRepo(t)

	out, code := RunMigrateToNested(root, "machine-x", "alice@example.com", MigrateToNestedOptions{})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(MigrateToNestedResult)
	if !ok {
		t.Fatalf("output = %T, want MigrateToNestedResult", out)
	}
	if !res.OK {
		t.Errorf("ok = false")
	}
	if res.AlreadyMigrated {
		t.Errorf("already_migrated = true on fresh migrate")
	}
	if !res.NestedCommitted {
		t.Errorf("nested_committed = false; want true")
	}
	if !res.HostUntracked {
		t.Errorf("host_untracked = false; want true (legacy tree had .act/ tracked)")
	}
	if !res.GitignoreUpdated {
		t.Errorf("gitignore_updated = false; want true (legacy tree had no entry)")
	}
	if !res.HookInstalled {
		t.Errorf("hook_installed = false; want true")
	}
	if !res.HostCommitted {
		t.Errorf("host_committed = false; want true (.gitignore + untrack staged)")
	}
	if len(res.PartialFailures) > 0 {
		t.Errorf("partial_failures = %v; want empty", res.PartialFailures)
	}

	// Verify post-state on disk.
	if _, err := os.Stat(filepath.Join(root, ".act", ".git")); err != nil {
		t.Errorf("nested .act/.git not present: %v", err)
	}
	gi, _ := os.ReadFile(filepath.Join(root, ".gitignore"))
	if !strings.Contains(string(gi), ".act/") {
		t.Errorf("host .gitignore missing .act/ entry; got %q", gi)
	}
	hook, _ := os.ReadFile(filepath.Join(root, ".git", "hooks", "pre-commit"))
	if !strings.Contains(string(hook), preCommitHookHeader) {
		t.Errorf("pre-commit hook missing act-managed block; got %q", hook)
	}
	// Host repo should no longer track .act/.
	tracked, err := exec.Command("git", "-C", root, "ls-files", "--", ".act/").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	if strings.TrimSpace(string(tracked)) != "" {
		t.Errorf(".act/ paths still tracked by host repo: %q", tracked)
	}

	// Nested repo should have at least one commit, with the migration
	// message as the initial commit.
	commitMsg, err := exec.Command("git", "-C", filepath.Join(root, ".act"), "log", "--reverse", "--format=%s", "-1").Output()
	if err != nil {
		t.Fatalf("nested git log: %v\n%s", err, commitMsg)
	}
	if !strings.Contains(string(commitMsg), "migrated from host-tracked") {
		t.Errorf("initial nested commit subject = %q; want it to mention 'migrated'", strings.TrimSpace(string(commitMsg)))
	}
}

func TestRunMigrateToNested_Idempotent(t *testing.T) {
	root := makeLegacyActRepo(t)

	// First migration: should succeed normally.
	_, code := RunMigrateToNested(root, "m", "u@e", MigrateToNestedOptions{})
	if code != 0 {
		t.Fatalf("first migrate exit = %d", code)
	}

	// Second migration: should detect already-migrated and exit 0 with
	// no other side effects.
	out2, code2 := RunMigrateToNested(root, "m", "u@e", MigrateToNestedOptions{})
	if code2 != 0 {
		t.Fatalf("second migrate exit = %d; want 0 (idempotent)", code2)
	}
	res, ok := out2.(MigrateToNestedResult)
	if !ok {
		t.Fatalf("output = %T, want MigrateToNestedResult", out2)
	}
	if !res.AlreadyMigrated {
		t.Errorf("already_migrated = false on re-run; want true (nested .git already present)")
	}
	// Nested bootstrap is skipped on re-run; host-side steps re-run
	// idempotently and should report no state changes either since the
	// first migration already produced the final state.
	if res.NestedCommitted {
		t.Errorf("nested_committed = true on re-run; want false")
	}
	if res.HostUntracked {
		t.Errorf("host_untracked = true on re-run; want false (already untracked)")
	}
	if res.GitignoreUpdated {
		t.Errorf("gitignore_updated = true on re-run; want false (entry already present)")
	}
	if res.HookInstalled {
		t.Errorf("hook_installed = true on re-run; want false (hook already installed)")
	}
}

// TestRunMigrateToNested_FinishesPartialMigration covers the case where
// an earlier migration landed the nested .git but the host-side steps
// (untrack, gitignore, hook) didn't complete — exactly the state we
// observed on the dogfooded act repo when nested-layout caught
// leftover tracked .act/* paths. Re-running the migrate command should
// finish the job: untrack stays tracked paths, install hook, etc.
func TestRunMigrateToNested_FinishesPartialMigration(t *testing.T) {
	root := makeLegacyActRepo(t)

	// Manually do step 1 (nested bootstrap) but skip the host-side
	// steps, mimicking the partial state.
	if _, err := bootstrapMigratedNestedRepo(filepath.Join(root, ".act"), "m", "u@e"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Now run the full migrate. Should detect the nested already exists
	// and complete the host-side untrack / gitignore / hook installs.
	out, code := RunMigrateToNested(root, "m", "u@e", MigrateToNestedOptions{})
	if code != 0 {
		t.Fatalf("exit = %d; out=%+v", code, out)
	}
	res := out.(MigrateToNestedResult)
	if !res.AlreadyMigrated {
		t.Errorf("already_migrated = false; want true (nested .git already present)")
	}
	if !res.HostUntracked {
		t.Errorf("host_untracked = false; want true (.act/ was tracked before partial migration)")
	}
	if !res.GitignoreUpdated {
		t.Errorf("gitignore_updated = false; want true")
	}
	if !res.HookInstalled {
		t.Errorf("hook_installed = false; want true")
	}
}

func TestRunMigrateToNested_NoActDir(t *testing.T) {
	root := t.TempDir()
	runOrFatal(t, root, "git", "init", "-q", "-b", "main")

	out, code := RunMigrateToNested(root, "m", "u@e", MigrateToNestedOptions{})
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (missing .act/); out=%+v", code, out)
	}
	envErr, ok := out.(errorOutput)
	if !ok {
		t.Fatalf("output = %T, want errorOutput", out)
	}
	if envErr.Error != "act_not_initialized" {
		t.Errorf("error = %q, want act_not_initialized", envErr.Error)
	}
}

func TestRunMigrateToNested_NotInGit(t *testing.T) {
	root := t.TempDir()
	// No git init — bare tempdir.

	out, code := RunMigrateToNested(root, "m", "u@e", MigrateToNestedOptions{})
	if code != 3 {
		t.Fatalf("exit = %d, want 3 (not in git); out=%+v", code, out)
	}
	envErr, ok := out.(errorOutput)
	if !ok {
		t.Fatalf("output = %T, want errorOutput", out)
	}
	if envErr.Error != "not_in_git" {
		t.Errorf("error = %q, want not_in_git", envErr.Error)
	}
}

// TestRunMigrateToNested_RestagedAfterAlreadyHasGitignore exercises the
// case where the host already has `.act/` in its .gitignore (perhaps from
// a partially-completed earlier migration) but `.act/` is still tracked
// (the `git rm --cached` step never ran). The migration should still
// untrack and produce a complete final state.
func TestRunMigrateToNested_PartialPrior(t *testing.T) {
	root := makeLegacyActRepo(t)
	// Pre-set .gitignore so ensureGitignoreEntry reports "no change".
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".act/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	runOrFatal(t, root, "git", "add", ".gitignore")
	runOrFatal(t, root, "git", "commit", "-q", "--no-verify", "-m", "pre-state: .gitignore only")

	out, code := RunMigrateToNested(root, "m", "u@e", MigrateToNestedOptions{})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%+v", code, out)
	}
	res := out.(MigrateToNestedResult)
	if res.GitignoreUpdated {
		t.Errorf("gitignore_updated = true; want false (already had entry)")
	}
	if !res.HostUntracked {
		t.Errorf("host_untracked = false; want true (.act/ was tracked)")
	}
	if !res.NestedCommitted {
		t.Errorf("nested_committed = false; want true")
	}
}

// silence unused-import warning when the test file is the only consumer
// of helpers we want compiled even on cross-checks.
var _ = time.Now
