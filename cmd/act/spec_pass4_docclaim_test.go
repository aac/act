package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
)

// Doc-claim cover for the fourth stale pass over docs/spec.md (act-7b9c54),
// for the two claims that only exist at the binary boundary: every exit code
// `act doctor` returns, and the flag surface of `act migrate`. These live
// here rather than in internal/cli because the no-state guard, the flag
// parser and the not-in-git branch are all in package main.

// pass4OpFileCount counts op files under dir/.act/ops.
func pass4OpFileCount(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(filepath.Join(dir, ".act", "ops"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".json") {
			n++
		}
		return nil
	})
	return n
}

// pass4ErrorCode pulls the error code out of a --json error envelope, which
// is either {"error":"code",...} or {"error":{"code":"...",...}}.
func pass4ErrorCode(t *testing.T, stdout string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &m); err != nil {
		t.Fatalf("stdout is not one JSON object: %v: %q", err, stdout)
	}
	switch e := m["error"].(type) {
	case string:
		return e
	case map[string]any:
		for _, k := range []string{"code", "kind"} {
			if s, ok := e[k].(string); ok {
				return s
			}
		}
	}
	t.Fatalf("no error code in %q", stdout)
	return ""
}

// TestDocClaim_Doctor_ExitCodes asserts the doctor "Exit codes" line, one
// code at a time, against the real binary. The line used to stop at 0, 1, 2.
func TestDocClaim_Doctor_ExitCodes(t *testing.T) {
	brokencoIsolateEnv(t)
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "healthy issue")

	// 0: no error-severity finding.
	if out, stderr, code := runActIn(t, dir, "doctor", "--json"); code != 0 {
		t.Fatalf("doctor on a healthy tracker: exit %d, want 0; out=%s stderr=%s", code, out, stderr)
	}

	// 2: an unparseable flag, and a --check name that is not a check.
	if _, _, code := runActIn(t, dir, "doctor", "--pass4-no-such-flag"); code != 2 {
		t.Errorf("doctor with an unknown flag: exit %d, want 2", code)
	}
	if out, _, code := runActIn(t, dir, "doctor", "--check", "pass4-no-such-check", "--json"); code != 2 || pass4ErrorCode(t, out) != "bad_flag" {
		t.Errorf("doctor --check <unknown>: exit %d out=%s; want 2 / bad_flag", code, out)
	}

	// An origin that is configured but cannot be reached. With --no-fetch the
	// probe is skipped and the finding is a warning: exit 0, and --strict
	// promotes that warning to exit 1.
	bogus := filepath.Join(t.TempDir(), "missing-origin.git")
	brokencoGit(t, "config", "-f", config.ActGitConfigPath(filepath.Join(dir, ".act")), "remote.origin.url", "file://"+bogus)
	out, _, code := runActIn(t, dir, "doctor", "--check", "phase2-reconciliation", "--no-fetch", "--json")
	if code != 0 || !strings.Contains(out, `"severity":"warn"`) || strings.Contains(out, `"severity":"error"`) {
		t.Fatalf("doctor --no-fetch with an unreachable origin: exit %d, want 0 with warnings only; out=%s", code, out)
	}
	if out, _, code := runActIn(t, dir, "doctor", "--check", "phase2-reconciliation", "--no-fetch", "--strict", "--json"); code != 1 {
		t.Errorf("the same run under --strict: exit %d, want 1 (warnings promoted); out=%s", code, out)
	}

	// 4: the probe runs and origin is unreachable.
	out, _, code = runActIn(t, dir, "doctor", "--check", "phase2-reconciliation", "--json")
	if code != 4 || !strings.Contains(out, `"check":"remote-reachable"`) {
		t.Fatalf("doctor with an unreachable origin: exit %d, want 4 with a remote-reachable finding; out=%s", code, out)
	}

	// 1: an error finding, here a stale lock in the nested repo ...
	lock := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("plant index.lock: %v", err)
	}
	if out, _, code := runActIn(t, dir, "doctor", "--check", "stale-git-lock", "--json"); code != 1 {
		t.Fatalf("doctor --check stale-git-lock with a planted lock: exit %d, want 1; out=%s", code, out)
	}
	// ... and 4 takes precedence over it when both are present in one run.
	out, _, code = runActIn(t, dir, "doctor", "--json")
	if !strings.Contains(out, `"check":"stale-git-lock"`) || !strings.Contains(out, `"check":"remote-reachable"`) {
		t.Fatalf("full doctor run is missing one of the two error findings; out=%s", out)
	}
	if code != 4 {
		t.Errorf("doctor with a stale lock AND an unreachable origin: exit %d, want 4 (precedence over 1)", code)
	}

	// 3: not inside a git working tree.
	if out, _, code := runActIn(t, t.TempDir(), "doctor", "--json"); code != 3 || pass4ErrorCode(t, out) != "not_in_git" {
		t.Errorf("doctor outside a git working tree: exit %d out=%s; want 3 / not_in_git", code, out)
	}

	// A git repo with no .act/ and no tracker remote: a read command, exit 0.
	host := brokencoHostRepo(t)
	if _, stderr, code := runActIn(t, host, "doctor"); code != 0 || !strings.Contains(stderr, "no act state in this repo") {
		t.Errorf("doctor in a git repo with no .act/: exit %d stderr=%q; want 0 and the no-state line", code, stderr)
	}

	// 3 again: the same checkout once a tracker remote holds its tracker.
	bares := t.TempDir()
	brokencoTrackerBare(t, bares)
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))
	if out, stderr, code := runActIn(t, host, "doctor", "--json"); code != 3 || !strings.Contains(out+stderr, "tracker_not_checked_out") {
		t.Errorf("doctor with an uncloned tracker: exit %d out=%s stderr=%s; want 3 / tracker_not_checked_out", code, out, stderr)
	}
}

// TestDocClaim_Migrate_FlagsAndExitCodes asserts the `act migrate` synopsis,
// flags and exit codes against the real binary: --from and --to are the only
// flags (the spec used to document a --dry-run that was never there), the
// command is absent from `act help`, output is one JSON object, and a
// well-formed call exits 5 `migration_not_found` and writes nothing because
// no migration is registered.
func TestDocClaim_Migrate_FlagsAndExitCodes(t *testing.T) {
	brokencoIsolateEnv(t)
	dir := blocksSite(t)
	createBlocksIssue(t, dir, "an issue to migrate")
	before := pass4OpFileCount(t, dir)
	if before == 0 {
		t.Fatalf("fixture has no op files")
	}

	for _, flag := range []string{"--dry-run", "--json"} {
		if out, _, code := runActIn(t, dir, "migrate", "--from", "1", "--to", "2", flag); code != 2 {
			t.Errorf("migrate %s: exit %d, want 2 (not a flag); out=%s", flag, code, out)
		}
	}

	out, _, code := runActIn(t, dir, "migrate", "--from", "1", "--to", "2")
	if code != 5 || pass4ErrorCode(t, out) != "migration_not_found" {
		t.Errorf("migrate --from 1 --to 2: exit %d out=%s; want 5 / migration_not_found", code, out)
	}
	for _, args := range [][]string{
		{"migrate"},
		{"migrate", "--from", "1"},
		{"migrate", "--to", "2"},
		{"migrate", "--from", "2", "--to", "1"},
		{"migrate", "--from", "1", "--to", "1"},
		{"migrate", "--from", "-1", "--to", "2"},
	} {
		out, _, code := runActIn(t, dir, args...)
		if code != 2 || pass4ErrorCode(t, out) != "bad_input" {
			t.Errorf("act %s: exit %d out=%s; want 2 / bad_input", strings.Join(args, " "), code, out)
		}
	}
	if after := pass4OpFileCount(t, dir); after != before {
		t.Errorf("refused migrate calls changed the op log: %d op files before, %d after", before, after)
	}

	if out, _, code := runActIn(t, t.TempDir(), "migrate", "--from", "1", "--to", "2"); code != 3 || pass4ErrorCode(t, out) != "not_in_git" {
		t.Errorf("migrate outside a git working tree: exit %d out=%s; want 3 / not_in_git", code, out)
	}

	help, helpErr, _ := runAct(t, "help")
	if len(help)+len(helpErr) < 200 {
		t.Fatalf("`act help` printed almost nothing; the absence check below would be vacuous: %q %q", help, helpErr)
	}
	if strings.Contains(help+helpErr, "migrate") {
		t.Errorf("`act help` mentions migrate; the spec says the command is not listed there")
	}
}
