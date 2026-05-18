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
// failing the test on non-zero exit. Used by the auto-commit tests below.
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

// makeRepo creates a tempdir with a real `git init`'d repo (no commits) and
// returns its path. Phase 1 of the coordination-plane design (act-c1b4)
// makes act init invoke git init inside .act/; a real outer git repo is
// not strictly required, but a real .git/ subtree lets the host-side
// installHostPreCommitHook step land its file.
func makeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// Pin commit identity so any later commits in the test don't fail on
	// hosts without global user.{name,email} configured.
	configEmail := exec.Command("git", "config", "user.email", "u@example.com")
	configEmail.Dir = root
	if out, err := configEmail.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	configName := exec.Command("git", "config", "user.name", "U")
	configName.Dir = root
	if out, err := configName.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}
	configSign := exec.Command("git", "config", "commit.gpgsign", "false")
	configSign.Dir = root
	_, _ = configSign.CombinedOutput()
	return root
}

func TestRunInit_HappyPath(t *testing.T) {
	root := makeRepo(t)
	out, code := RunInit(root, false, "machine-abc", "alice@example.com",
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
	if !succ.NestedCommitted {
		t.Errorf("nested_committed = false; want true (initial bootstrap commit)")
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

	// Nested .act/.git exists and has at least one commit.
	nestedGit := filepath.Join(paths.Root, ".git")
	if fi, err := os.Stat(nestedGit); err != nil || !fi.IsDir() {
		t.Errorf("nested .act/.git missing or not a dir: %v", err)
	}
	if got := mustGitOutput(t, paths.Root, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("nested repo commit count = %q, want 1", got)
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
	out, code := RunInit(root, false, "m", "e", nil)
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
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("first init code = %d", code)
	}
	out, code := RunInit(root, false, "m", "e", nil)
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
	if _, code := RunInit(root, false, "m", "e", fakeNow(t1)); code != 0 {
		t.Fatalf("first init code = %d", code)
	}

	t2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	_, code := RunInit(root, true, "m2", "e2", fakeNow(t2))
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

// TestRunInit_GitignoreEntry verifies the host .gitignore receives the
// `.act/` entry and that re-init does not duplicate it. Phase 1 swapped
// the legacy `.act/index.db` entry for the broader `.act/` (the whole
// nested-repo tree is gitignored from the host).
func TestRunInit_GitignoreEntry(t *testing.T) {
	root := makeRepo(t)
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("first init code = %d", code)
	}
	gi := filepath.Join(root, ".gitignore")
	first, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	if !strings.Contains(string(first), ".act/") {
		t.Errorf("gitignore missing .act/ entry: %q", string(first))
	}

	// Second init with --force should not duplicate the entry.
	if _, code := RunInit(root, true, "m", "e", nil); code != 0 {
		t.Fatalf("second init code = %d", code)
	}
	second, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read gitignore: %v", err)
	}
	// Count exact-line matches, not substring (because the line ".act/"
	// would also substring-match ".act/something" in other places).
	count := 0
	for _, line := range strings.Split(string(second), "\n") {
		if strings.TrimSpace(line) == ".act/" {
			count++
		}
	}
	if count != 1 {
		t.Errorf(".act/ appears as a whole line %d times, want 1; content=%q", count, string(second))
	}
}

// TestRunInit_GitignorePreservesExisting verifies that prior entries in
// the host .gitignore survive across init.
func TestRunInit_GitignorePreservesExisting(t *testing.T) {
	root := makeRepo(t)
	gi := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatalf("seed gitignore: %v", err)
	}
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}
	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "node_modules/") {
		t.Errorf("existing entry lost: %q", string(got))
	}
	if !strings.Contains(string(got), ".act/") {
		t.Errorf("new entry missing: %q", string(got))
	}
}

// TestRunInit_GitignoreMissingNoTrailingNewline covers an edge case from
// the spec: when the existing .gitignore doesn't end in a newline, the
// appended entry must start on its own line.
func TestRunInit_GitignoreMissingNoTrailingNewline(t *testing.T) {
	root := makeRepo(t)
	gi := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(gi, []byte("node_modules/"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}
	got, err := os.ReadFile(gi)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// node_modules/ and .act/ should each be on their own line.
	want1 := "node_modules/\n"
	want2 := ".act/\n"
	if !strings.Contains(string(got), want1) || !strings.Contains(string(got), want2) {
		t.Errorf("gitignore entries not on own lines: %q", string(got))
	}
}

// TestRunInit_PreCommitHookInstalled asserts that act init lays down the
// host's .git/hooks/pre-commit (or augments an existing one) with the
// act-managed block.
func TestRunInit_PreCommitHookInstalled(t *testing.T) {
	root := makeRepo(t)
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}

	hook := filepath.Join(root, ".git", "hooks", "pre-commit")
	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("read pre-commit: %v", err)
	}
	if !strings.Contains(string(body), "act: reject staged .act/* paths") {
		t.Errorf("pre-commit hook missing act block: %q", string(body))
	}
	// Re-init should be idempotent: hook body must not grow.
	if _, code := RunInit(root, true, "m", "e", nil); code != 0 {
		t.Fatalf("re-init code = %d", code)
	}
	body2, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("read pre-commit: %v", err)
	}
	if string(body) != string(body2) {
		t.Errorf("pre-commit hook changed across re-init; want idempotent\nbefore:\n%s\nafter:\n%s", body, body2)
	}
	// Must be executable so git actually invokes it.
	fi, err := os.Stat(hook)
	if err != nil {
		t.Fatalf("stat hook: %v", err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("pre-commit hook not executable: mode = %v", fi.Mode())
	}
}

// TestRunInit_PreCommitHookRejectsActPaths is the user-visible boundary
// test for the hook: stage a .act path in the host repo (the realistic
// leak shape under Phase 1 is a gitlink to the nested repo, force-added
// past gitignore) and confirm `git commit` refuses it with the remedy
// hint.
func TestRunInit_PreCommitHookRejectsActPaths(t *testing.T) {
	root := makeRealGitRepo(t)
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}

	// Force-add the nested .act/ directory as a gitlink (`git add -f
	// .act`). git treats the embedded-repo path as a single entry rather
	// than enumerating files inside; the pre-commit hook must catch this
	// shape because it's the realistic accidental-leak vector under
	// Phase 1.
	addCmd := exec.Command("git", "add", "-f", ".act")
	addCmd.Dir = root
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add -f .act: %v\n%s", err, out)
	}
	commitCmd := exec.Command("git", "commit", "-m", "should-be-blocked")
	commitCmd.Dir = root
	out, err := commitCmd.CombinedOutput()
	if err == nil {
		t.Fatalf("commit succeeded but should have been blocked by pre-commit hook; stdout=%s", out)
	}
	if !strings.Contains(string(out), "refusing to commit .act/ paths") {
		t.Errorf("commit failed but with unexpected message: %q", string(out))
	}
	if !strings.Contains(string(out), "git rm -r --cached .act/") {
		t.Errorf("hook failure message missing remedy hint: %q", string(out))
	}
}

// TestRunInit_PreCommitHookAugmentsExisting verifies that an existing
// non-act pre-commit hook is preserved and the act block is appended.
func TestRunInit_PreCommitHookAugmentsExisting(t *testing.T) {
	root := makeRepo(t)
	hooksDir := filepath.Join(root, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	preExisting := "#!/usr/bin/env sh\necho 'user pre-commit'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(preExisting), 0o755); err != nil {
		t.Fatalf("seed pre-commit: %v", err)
	}

	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}
	got, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(got), "user pre-commit") {
		t.Errorf("existing hook clobbered: %q", string(got))
	}
	if !strings.Contains(string(got), "act: reject staged .act/* paths") {
		t.Errorf("act block missing: %q", string(got))
	}
}

// TestRunInit_ContributingStanzaForGithubRemote verifies the CONTRIBUTING
// stanza is emitted when origin points at github.com.
func TestRunInit_ContributingStanzaForGithubRemote(t *testing.T) {
	root := makeRepo(t)
	addRemote := exec.Command("git", "remote", "add", "origin", "https://github.com/example/repo.git")
	addRemote.Dir = root
	if out, err := addRemote.CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v\n%s", err, out)
	}
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init code = %d", code)
	}
	body, err := os.ReadFile(filepath.Join(root, "CONTRIBUTING.md"))
	if err != nil {
		t.Fatalf("read CONTRIBUTING.md: %v", err)
	}
	if !strings.Contains(string(body), "act:contributing-stanza:start") {
		t.Errorf("CONTRIBUTING.md missing start marker: %q", string(body))
	}
	if !strings.Contains(string(body), "Act-Id:") {
		t.Errorf("CONTRIBUTING.md missing Act-Id reference: %q", string(body))
	}
	// Re-init is idempotent.
	if _, code := RunInit(root, true, "m", "e", nil); code != 0 {
		t.Fatalf("re-init: %d", code)
	}
	body2, err := os.ReadFile(filepath.Join(root, "CONTRIBUTING.md"))
	if err != nil {
		t.Fatalf("read CONTRIBUTING.md: %v", err)
	}
	if got := strings.Count(string(body2), "act:contributing-stanza:start"); got != 1 {
		t.Errorf("stanza start marker count = %d, want 1; body=%q", got, string(body2))
	}
}

// TestRunInit_ContributingStanzaForSSHGithubRemote covers `git@github.com:`
// shape which is the common SSH form.
func TestRunInit_ContributingStanzaForSSHGithubRemote(t *testing.T) {
	root := makeRepo(t)
	addRemote := exec.Command("git", "remote", "add", "origin", "git@github.com:example/repo.git")
	addRemote.Dir = root
	if out, err := addRemote.CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v\n%s", err, out)
	}
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "CONTRIBUTING.md")); err != nil {
		t.Errorf("CONTRIBUTING.md missing: %v", err)
	}
}

// TestRunInit_NoContributingStanzaForPrivateRemote verifies that an
// SSH-to-private-host remote does NOT trigger the CONTRIBUTING stanza.
func TestRunInit_NoContributingStanzaForPrivateRemote(t *testing.T) {
	root := makeRepo(t)
	addRemote := exec.Command("git", "remote", "add", "origin", "git@private.host:example/repo.git")
	addRemote.Dir = root
	if out, err := addRemote.CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v\n%s", err, out)
	}
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "CONTRIBUTING.md")); !os.IsNotExist(err) {
		t.Errorf("CONTRIBUTING.md exists when remote is private: err=%v", err)
	}
}

// TestRunInit_NoContributingStanzaForNoRemote verifies that a repo with
// no remote at all also doesn't get the stanza.
func TestRunInit_NoContributingStanzaForNoRemote(t *testing.T) {
	root := makeRepo(t)
	if _, code := RunInit(root, false, "m", "e", nil); code != 0 {
		t.Fatalf("init: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "CONTRIBUTING.md")); !os.IsNotExist(err) {
		t.Errorf("CONTRIBUTING.md exists when there's no remote: err=%v", err)
	}
}

// TestRunInit_OutputJSONShape exercises the JSON envelope shape produced
// by the success path. Phase 1 added nested_committed / host_committed /
// gitignore_updated / hook_installed / contributing_emitted to the
// envelope; older shape (`committed`) is replaced.
func TestRunInit_OutputJSONShape(t *testing.T) {
	root := makeRepo(t)
	out, code := RunInit(root, false, "m", "alice@example.com", nil)
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
	for _, key := range []string{"ok", "act_dir", "node_id", "nested_committed", "gitignore_updated", "hook_installed"} {
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
	if decoded["nested_committed"] != true {
		t.Errorf("nested_committed = %v, want true", decoded["nested_committed"])
	}
}

// makeRealGitRepo creates a real git repo with one initial commit and NO
// act state. Distinct from makeRepo (real git init but no commits): this
// helper seeds an initial host commit so subsequent act-init host-side
// commits land cleanly.
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

// TestRunInit_HostCommitOnRealRepo asserts that on a host repo with a
// HEAD, RunInit ends up with a host-side commit that contains only
// .gitignore (and CONTRIBUTING.md when present) — not -A, no DIRTY work.
func TestRunInit_HostCommitOnRealRepo(t *testing.T) {
	root := makeRealGitRepo(t)

	// Pre-existing dirty file: must NOT be in the host-side commit.
	dirty := filepath.Join(root, "DIRTY.txt")
	if err := os.WriteFile(dirty, []byte("uncommitted; should NOT be in act init's commit"), 0o644); err != nil {
		t.Fatalf("seed dirty file: %v", err)
	}

	beforeCount := mustGitCommitCount(t, root)

	out, code := RunInit(root, false, "m", "alice@example.com", nil)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	succ, ok := out.(successOutput)
	if !ok {
		t.Fatalf("output type = %T, want successOutput", out)
	}
	if !succ.HostCommitted {
		t.Errorf("HostCommitted=false; PartialFailures=%v", succ.PartialFailures)
	}

	afterCount := mustGitCommitCount(t, root)
	if afterCount != beforeCount+1 {
		t.Fatalf("host commit count went %d -> %d, want exactly 1 new commit", beforeCount, afterCount)
	}

	// DIRTY.txt must still be untracked.
	status := mustGitOutput(t, root, "status", "--porcelain", "DIRTY.txt")
	if !strings.HasPrefix(status, "?? ") {
		t.Errorf("DIRTY.txt status = %q, want untracked (starts with '?? '). act init must stage only specific paths, never -A", status)
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
	out, code := RunInit(root, false, "m", "e", nil)
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

// TestPublicRemoteHeuristic exercises publicRemoteRegex against the URL
// shapes the spec calls out as required behavior.
func TestPublicRemoteHeuristic(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://github.com/foo/bar.git", true},
		{"https://github.com/foo/bar", true},
		{"git@github.com:foo/bar.git", true},
		{"https://gitlab.com/foo/bar", true},
		{"git@gitlab.com:foo/bar.git", true},
		{"https://bitbucket.org/foo/bar", true},
		{"ssh://git@github.com/foo/bar", true},
		{"git@private.host:foo/bar.git", false},
		{"https://private.example/foo/bar.git", false},
		{"file:///tmp/repo", false},
		{"", false},
		{"github.com.evil.example/foo", false},
	}
	for _, c := range cases {
		got := publicRemoteRegex.MatchString(c.url)
		if got != c.want {
			t.Errorf("publicRemoteRegex.MatchString(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}
