package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/config"
)

// mustGitOutput runs `git <args>` in repoRoot and returns trimmed stdout,
// failing the test on non-zero exit. Used by the auto-commit tests below
// (act-2c7d).
func mustGitOutput(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// fakeNow returns a deterministic time func suitable for RunInit.
func fakeNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// makeRepo creates a tempdir with a `.git/` directory and returns its path.
func makeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	return root
}

func TestRunInit_HappyPath(t *testing.T) {
	root := makeRepo(t)
	out, code := RunInit(root, false, false, "machine-abc", "alice@example.com",
		fakeNow(time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}

	succ, ok := out.(successOutput)
	if !ok {
		t.Fatalf("output type = %T, want successOutput", out)
	}
	if !succ.OK {
		t.Errorf("ok = false")
	}
	if succ.ActDir != filepath.Join(root, ".act") {
		t.Errorf("act_dir = %q, want %q", succ.ActDir, filepath.Join(root, ".act"))
	}
	if len(succ.NodeID) != 8 {
		t.Errorf("node_id = %q, want 8 hex", succ.NodeID)
	}

	paths := config.Layout(root)
	for _, dir := range []string{paths.Root, paths.Ops, paths.Snapshots, paths.Hooks, paths.Imports} {
		fi, err := os.Stat(dir)
		if err != nil {
			t.Errorf("missing dir %s: %v", dir, err)
			continue
		}
		if !fi.IsDir() {
			t.Errorf("%s is not a dir", dir)
		}
	}

	cfg, err := config.ReadConfig(paths)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.NodeID != succ.NodeID {
		t.Errorf("config node_id = %q, want %q", cfg.NodeID, succ.NodeID)
	}
	if cfg.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", cfg.Version)
	}
	if cfg.CreatedAt != "2026-04-29T12:00:00.000Z" {
		t.Errorf("created_at = %q", cfg.CreatedAt)
	}
	if cfg.LastHLC != (config.HLCState{}) {
		t.Errorf("last_hlc = %+v, want zero", cfg.LastHLC)
	}
}

func TestRunInit_NoGit(t *testing.T) {
	// Use a deeply nested tempdir so no ancestor up to / has a .git/.
	// t.TempDir is guaranteed under the OS temp dir which has no .git.
	root := t.TempDir()
	// Defensive: avoid false positives if the test host has .git in /.
	if hasGitDir(root) {
		t.Skip("test host has .git/ on an ancestor of the temp dir")
	}
	out, code := RunInit(root, false, false, "m", "e", nil)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(errorOutput)
	if !ok {
		t.Fatalf("output type = %T, want errorOutput", out)
	}
	if e.Error != "not_in_git" {
		t.Errorf("error = %q, want not_in_git", e.Error)
	}
}

func TestRunInit_RejectsReinitWithoutForce(t *testing.T) {
	root := makeRepo(t)
	if _, code := RunInit(root, false, false, "m", "e", nil); code != 0 {
		t.Fatalf("first init code = %d", code)
	}
	out, code := RunInit(root, false, false, "m", "e", nil)
	if code != 1 {
		t.Fatalf("second init code = %d, want 1", code)
	}
	e, ok := out.(errorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "act_already_initialized" {
		t.Errorf("error = %q", e.Error)
	}
}

func TestRunInit_ForceReinitOverwrites(t *testing.T) {
	root := makeRepo(t)
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, code := RunInit(root, false, false, "m", "e", fakeNow(t1)); code != 0 {
		t.Fatalf("first init code = %d", code)
	}

	t2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	_, code := RunInit(root, true, false, "m2", "e2", fakeNow(t2))
	if code != 0 {
		t.Fatalf("force re-init code = %d, want 0", code)
	}

	cfg, err := config.ReadConfig(config.Layout(root))
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if cfg.CreatedAt != "2026-06-01T00:00:00.000Z" {
		t.Errorf("created_at = %q, want overwritten value", cfg.CreatedAt)
	}
	if cfg.NodeID != config.ComputeNodeID("m2", "e2") {
		t.Errorf("node_id was not overwritten: %q", cfg.NodeID)
	}
}

func TestRunInit_GitignoreAppendIdempotent(t *testing.T) {
	root := makeRepo(t)
	if _, code := RunInit(root, false, false, "m", "e", nil); code != 0 {
		t.Fatalf("first init code = %d", code)
	}
	gi := filepath.Join(root, ".gitignore")
	first, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if !strings.Contains(string(first), ".act/index.db") {
		t.Errorf("gitignore missing .act/index.db: %q", string(first))
	}

	// Second init with --force should not duplicate the entry.
	if _, code := RunInit(root, true, false, "m", "e", nil); code != 0 {
		t.Fatalf("second init code = %d", code)
	}
	second, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if got := strings.Count(string(second), ".act/index.db"); got != 1 {
		t.Errorf(".act/index.db appears %d times, want 1; content=%q", got, string(second))
	}
}

func TestRunInit_GitignorePreservesExisting(t *testing.T) {
	root := makeRepo(t)
	gi := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("seed gitignore: %v", err)
	}
	if _, code := RunInit(root, false, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}
	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "node_modules/") {
		t.Errorf("existing entry lost: %q", string(got))
	}
	if !strings.Contains(string(got), ".act/index.db") {
		t.Errorf("new entry missing: %q", string(got))
	}
}

func TestRunInit_OutputJSONShape(t *testing.T) {
	root := makeRepo(t)
	out, code := RunInit(root, false, false, "m", "alice@example.com", nil)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"ok", "act_dir", "node_id"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing key %q in %s", key, data)
		}
	}
	if decoded["ok"] != true {
		t.Errorf("ok = %v", decoded["ok"])
	}
	if s, ok := decoded["node_id"].(string); !ok || len(s) != 8 {
		t.Errorf("node_id shape: %v", decoded["node_id"])
	}
}

// makeRealGitRepo creates a real git repo with one initial commit and NO
// act state. Distinct from makeRepo (fake .git/ only) and makeCreateRepo
// (real git + pre-initialized .act/). Used by the auto-commit tests below.
func makeRealGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.email", "u@example.com")
	mustGit(t, dir, "config", "user.name", "U")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(t, dir, "add", "README")
	mustGit(t, dir, "commit", "-q", "--no-verify", "-m", "init")
	return dir
}

// TestRunInit_AutoCommit asserts that RunInit(commit=true) creates exactly
// one new git commit on the current branch with the canonical subject and
// stages only .act + .gitignore (never -A) — pre-existing dirty files
// stay out of the commit. Regression coverage for act-2c7d.
func TestRunInit_AutoCommit(t *testing.T) {
	root := makeRealGitRepo(t)

	// Drop a deliberately dirty file at root so we can verify it stays
	// unstaged after init's auto-commit. If the implementation ever
	// regresses to `git add -A`, this file lands in the commit and the
	// assertion below fires.
	dirty := filepath.Join(root, "DIRTY.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted; should NOT be in act init's commit"), 0o644); err != nil {
		t.Fatalf("seed dirty file: %v", err)
	}

	beforeCount := mustGitCommitCount(t, root)

	out, code := RunInit(root, false, true, "m", "alice@example.com", nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	succ, ok := out.(successOutput)
	if !ok {
		t.Fatalf("output type = %T, want successOutput", out)
	}
	if !succ.Committed {
		t.Errorf("Committed=false; commit_error=%q", succ.CommitError)
	}

	afterCount := mustGitCommitCount(t, root)
	if afterCount != beforeCount+1 {
		t.Fatalf("commit count went %d -> %d, want exactly 1 new commit", beforeCount, afterCount)
	}

	subject := mustGitOutput(t, root, "log", "-1", "--format=%s")
	if subject != "act init: tracker initialized" {
		t.Errorf("commit subject = %q, want %q", subject, "act init: tracker initialized")
	}

	// DIRTY.txt must still be untracked.
	status := mustGitOutput(t, root, "status", "--porcelain", "DIRTY.txt")
	if !strings.HasPrefix(status, "?? ") {
		t.Errorf("DIRTY.txt status = %q, want untracked (starts with '?? '). act init must stage only .act and .gitignore, never -A", status)
	}
}

// TestRunInit_NoCommitFlag asserts that RunInit(commit=false) leaves the
// working tree state matching the pre-v0.2 behavior — files written, no
// new commit made.
func TestRunInit_NoCommitFlag(t *testing.T) {
	root := makeRealGitRepo(t)
	beforeCount := mustGitCommitCount(t, root)

	out, code := RunInit(root, false, false, "m", "e", nil)
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	succ := out.(successOutput)
	if succ.Committed {
		t.Errorf("Committed=true with commit=false; should be false")
	}
	if succ.CommitError != "" {
		t.Errorf("CommitError=%q; should be empty", succ.CommitError)
	}

	afterCount := mustGitCommitCount(t, root)
	if afterCount != beforeCount {
		t.Errorf("commit count changed %d -> %d; expected no new commits", beforeCount, afterCount)
	}
}

// mustGitCommitCount returns the number of commits reachable from HEAD,
// failing the test on any error.
func mustGitCommitCount(t *testing.T, repoRoot string) int {
	t.Helper()
	out := mustGitOutput(t, repoRoot, "rev-list", "--count", "HEAD")
	n := 0
	if _, err := fmt.Sscanf(out, "%d", &n); err != nil {
		t.Fatalf("parse rev-list count %q: %v", out, err)
	}
	return n
}

func TestRunInit_ErrorJSONShape(t *testing.T) {
	root := t.TempDir()
	if hasGitDir(root) {
		t.Skip("temp dir has ancestor .git")
	}
	out, code := RunInit(root, false, false, "m", "e", nil)
	if code != 3 {
		t.Fatalf("code = %d", code)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["error"] != "not_in_git" {
		t.Errorf("error = %v", decoded["error"])
	}
	if _, ok := decoded["message"].(string); !ok {
		t.Errorf("message missing/non-string in %s", data)
	}
}
