package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Doc-claim cover for the fifth stale pass over docs/spec.md (act-675b59).
//
// Nine per-command "Exit codes" lines promised exit 4 on version skew. Only
// `act version --check-repo` reports version_skew; every other command applies
// an op from a newer writer like any other op. Each line now lists what its
// command returns, and each test below provokes those codes against the real
// binary. Every "not 4" assertion is made on a tracker where
// `act version --check-repo` DOES exit 4, so a pass cannot come from a fixture
// that holds no skew.
//
// These live in package main because the no-state guard, the flag parsers and
// the not-in-git branch are all here.

const pass5NoSuchID = "act-ffffffffffffffff" // 16 hex: cannot prefix-match a 6-hex id

// pass5Run runs act in dir and checks the exit code and, when wantSlug is
// non-empty, the error code in the --json envelope on stdout. It returns
// stdout and stderr. A failure names the command, both streams and the dir.
func pass5Run(t *testing.T, dir string, wantCode int, wantSlug string, args ...string) (string, string) {
	t.Helper()
	out, stderr, code := runActIn(t, dir, args...)
	if code != wantCode {
		t.Errorf("act %s (in %s): exit %d, want %d\nstdout=%s\nstderr=%s",
			strings.Join(args, " "), dir, code, wantCode, out, stderr)
		return out, stderr
	}
	if wantSlug != "" {
		if strings.TrimSpace(out) == "" {
			t.Errorf("act %s: exit %d but no envelope on stdout, want error %q (stderr=%s)",
				strings.Join(args, " "), code, wantSlug, stderr)
		} else if got := pass4ErrorCode(t, out); got != wantSlug {
			t.Errorf("act %s: error %q, want %q\nstdout=%s", strings.Join(args, " "), got, wantSlug, out)
		}
	}
	return out, stderr
}

// pass5Site makes an isolated tracker and returns its directory.
func pass5Site(t *testing.T) string {
	t.Helper()
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	return blocksSite(t)
}

// pass5CreateOp returns the path of the one create op file of issue id.
func pass5CreateOp(t *testing.T, dir, id string) string {
	t.Helper()
	var found []string
	_ = filepath.Walk(filepath.Join(dir, ".act", "ops", id), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, "-create.json") {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 1 {
		t.Fatalf("issue %s has %d create op files, want 1: %v", id, len(found), found)
	}
	return found[0]
}

// pass5Rewrite replaces the single match of re in the op file at path and
// returns a func that restores the original bytes.
func pass5Rewrite(t *testing.T, path string, re *regexp.Regexp, repl string) func() {
	t.Helper()
	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if n := len(re.FindAll(orig, -1)); n != 1 {
		t.Fatalf("%s matches %s %d times, want 1: %s", path, re, n, orig)
	}
	if err := os.WriteFile(path, re.ReplaceAll(orig, []byte(repl)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return func() {
		if err := os.WriteFile(path, orig, 0o644); err != nil {
			t.Fatalf("restore %s: %v", path, err)
		}
	}
}

var (
	pass5WriterRe    = regexp.MustCompile(`"writer_version":\s*"[^"]*"`)
	pass5OpVersionRe = regexp.MustCompile(`"op_version":\s*\d+`)
)

// pass5NewerWriter stamps issue id's create op as written by a newer act and
// proves the tracker now holds skew: `act version --check-repo` exits 4 with
// version_skew. Returns the restore func.
func pass5NewerWriter(t *testing.T, dir, id string) func() {
	t.Helper()
	restore := pass5Rewrite(t, pass5CreateOp(t, dir, id), pass5WriterRe, `"writer_version":"99.0.0"`)
	out, stderr, code := runActIn(t, dir, "version", "--check-repo", "--json")
	if code != 4 || pass4ErrorCode(t, out) != "version_skew" {
		t.Fatalf("fixture holds no skew: version --check-repo exit %d, want 4 / version_skew\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	return restore
}

// pass5UnknownOpVersion gives issue id's create op an op_version this binary
// does not know, so any fold over that issue aborts. Returns the restore func.
func pass5UnknownOpVersion(t *testing.T, dir, id string) func() {
	t.Helper()
	return pass5Rewrite(t, pass5CreateOp(t, dir, id), pass5OpVersionRe, `"op_version":99`)
}

// pass5Twin plants a second issue whose id shares id's first four hex
// characters and returns that shared prefix, which is now ambiguous.
func pass5Twin(t *testing.T, dir, id string) string {
	t.Helper()
	if len(id) < len("act-")+6 {
		t.Fatalf("id %q is shorter than act- plus six hex", id)
	}
	prefix := id[:len("act-")+4]
	twin := prefix + "00"
	if twin == id[:len(twin)] {
		twin = prefix + "11"
	}
	src := pass5CreateOp(t, dir, id)
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	rel, err := filepath.Rel(filepath.Join(dir, ".act", "ops", id), src)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	dst := filepath.Join(dir, ".act", "ops", twin, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dst, []byte(strings.ReplaceAll(string(body), id, twin)), 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return prefix
}

// pass5Hook installs a .act/hooks/<event> script that refuses, and returns a
// func that removes it.
func pass5Hook(t *testing.T, dir, event string) func() {
	t.Helper()
	path := filepath.Join(dir, ".act", "hooks", event)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	return func() { _ = os.Remove(path) }
}

// pass5NoState checks the three environments every command meets before any
// tracker exists, in the order that keeps them independent: a directory
// outside git (always 3 / not_in_git), a git repo with no .act/ and no
// tracker remote (wantNoAct, with wantNoActSlug or the no-state line), and
// the same repo once a configured tracker remote holds its tracker (always
// 3 / tracker_not_checked_out). args must put --json before any positional.
func pass5NoState(t *testing.T, wantNoAct int, wantNoActSlug string, args ...string) {
	t.Helper()
	pass5Run(t, t.TempDir(), 3, "not_in_git", args...)

	host := brokencoHostRepo(t)
	_, stderr := pass5Run(t, host, wantNoAct, wantNoActSlug, args...)
	if wantNoAct == 0 && !strings.Contains(stderr, "no act state in this repo") {
		t.Errorf("act %s in a git repo with no .act/: stderr %q lacks the no-state line", strings.Join(args, " "), stderr)
	}

	bares := t.TempDir()
	brokencoTrackerBare(t, bares)
	t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))
	out, stderr, code := runActIn(t, host, args...)
	if code != 3 || !strings.Contains(out+stderr, "tracker_not_checked_out") {
		t.Errorf("act %s with an uncloned tracker: exit %d, want 3 / tracker_not_checked_out\nstdout=%s\nstderr=%s",
			strings.Join(args, " "), code, out, stderr)
	}
	t.Setenv("ACT_TRACKER_REMOTE", "")
}

// pass5MCP runs `act mcp` in dir with the given JSON-RPC lines on stdin, then
// closes stdin. Returns stdout, stderr and the exit code.
func pass5MCP(t *testing.T, dir string, flags []string, lines ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(actBinary(t), append([]string{"mcp"}, flags...)...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n") + "\n")
	var outB, errB strings.Builder
	cmd.Stdout = &outB
	cmd.Stderr = &errB
	err := cmd.Run()
	if err == nil {
		return outB.String(), errB.String(), 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return outB.String(), errB.String(), ee.ExitCode()
	}
	t.Fatalf("act mcp: failed to launch: %v", err)
	return "", "", -1
}

const (
	pass5MCPInit = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	pass5MCPList = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"act_list","arguments":{}}}`
)

// TestDocClaim_ExitCodes_Create asserts the `act create` "Exit codes" line.
func TestDocClaim_ExitCodes_Create(t *testing.T) {
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")

	// 2: usage errors.
	pass5Run(t, dir, 2, "", "create", "--pass5-no-such-flag", "t")
	pass5Run(t, dir, 2, "bad_flag", "create", "--json")
	pass5Run(t, dir, 2, "bad_flag", "create", "--json", "")
	pass5Run(t, dir, 2, "bad_flag", "create", "--json", "--type", "pass5", "t")
	pass5Run(t, dir, 2, "bad_flag", "create", "--json", "-p", "9", "t")
	pass5Run(t, dir, 2, "bad_flag", "create", "--json", "--no-commit", "--push", "t")

	// 3: an id that matches nothing, and a description file that is not there.
	pass5Run(t, dir, 3, "issue_not_found", "create", "--json", "--parent", pass5NoSuchID, "t")
	pass5Run(t, dir, 3, "issue_not_found", "create", "--json", "--blocked-by", pass5NoSuchID, "t")
	pass5Run(t, dir, 3, "issue_not_found", "create", "--json", "--blocks", pass5NoSuchID, "t")
	pass5Run(t, dir, 3, "file_not_found", "create", "--json", "--description-file", filepath.Join(t.TempDir(), "absent"), "t")

	// 1: the create hook refuses.
	unhook := pass5Hook(t, dir, "create")
	pass5Run(t, dir, 1, "hook_failed", "create", "--json", "refused by the hook")
	unhook()

	// Not 4: an op from a newer writer does not stop the write.
	restore := pass5NewerWriter(t, dir, id)
	pass5Run(t, dir, 0, "", "create", "--json", "written beside a newer-writer op")
	restore()

	// 2: an ambiguous --parent prefix.
	prefix := pass5Twin(t, dir, id)
	pass5Run(t, dir, 2, "id_ambiguous", "create", "--json", "--parent", prefix, "t")

	// 3: outside git; no .act/ (a write, so not the read commands' exit 0);
	// an uncloned tracker.
	pass5NoState(t, 3, "act_not_initialized", "create", "--json", "t")
}

// TestDocClaim_ExitCodes_List asserts the `act list` "Exit codes" line.
func TestDocClaim_ExitCodes_List(t *testing.T) {
	dir := pass5Site(t)

	// 0, including an empty result.
	if out, _ := pass5Run(t, dir, 0, "", "list", "--json"); !strings.Contains(out, `"count":0`) {
		t.Errorf("list on an empty tracker: want count 0, got %s", out)
	}
	id := createBlocksIssue(t, dir, "first")

	// 2: usage errors.
	pass5Run(t, dir, 2, "", "list", "--pass5-no-such-flag")
	pass5Run(t, dir, 2, "bad_flag", "list", "--json", "stray")
	pass5Run(t, dir, 2, "bad_flag", "list", "--json", "--all", "--status", "open")
	pass5Run(t, dir, 2, "bad_flag", "list", "--json", "--status", "pass5")
	pass5Run(t, dir, 2, "bad_flag", "list", "--json", "--sort", "pass5")

	// Not 4: an op with a newer writer_version is applied like any other.
	restore := pass5NewerWriter(t, dir, id)
	if out, _ := pass5Run(t, dir, 0, "", "list", "--json"); !strings.Contains(out, id) {
		t.Errorf("list beside a newer-writer op does not list %s: %s", id, out)
	}
	restore()

	// 1: an op the fold rejects.
	restore = pass5UnknownOpVersion(t, dir, id)
	pass5Run(t, dir, 1, "index_rebuild_failed", "list", "--json")
	restore()

	pass5NoState(t, 0, "", "list", "--json")
}

// TestDocClaim_ExitCodes_Show asserts the `act show` "Exit codes" line.
func TestDocClaim_ExitCodes_Show(t *testing.T) {
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")
	gone := createBlocksIssue(t, dir, "to be deleted")

	pass5Run(t, dir, 0, "", "show", "--json", id)

	// 0 for a tombstoned issue; 3 when --commit-marker is asked of one.
	pass5Run(t, dir, 0, "", "delete", "--json", gone)
	if out, _ := pass5Run(t, dir, 0, "", "show", "--json", gone); !strings.Contains(out, `"tombstoned":true`) {
		t.Errorf("show of a deleted issue: want tombstoned:true, got %s", out)
	}
	pass5Run(t, dir, 3, "tombstoned", "show", "--json", "--commit-marker", gone)

	// 2: usage errors.
	pass5Run(t, dir, 2, "", "show", "--pass5-no-such-flag", id)
	pass5Run(t, dir, 2, "bad_flag", "show", "--json")
	pass5Run(t, dir, 2, "bad_flag", "show", "--json", id, "stray")

	// 3: an id that matches nothing.
	pass5Run(t, dir, 3, "issue_not_found", "show", "--json", pass5NoSuchID)

	// Not 4: an op with a newer writer_version is applied like any other.
	restore := pass5NewerWriter(t, dir, id)
	if out, _ := pass5Run(t, dir, 0, "", "show", "--json", id); !strings.Contains(out, `"title":"first"`) {
		t.Errorf("show of an issue created by a newer writer lost its title: %s", out)
	}
	restore()

	// 3: the issue's op stream cannot be folded.
	restore = pass5UnknownOpVersion(t, dir, id)
	pass5Run(t, dir, 3, "fold_failed", "show", "--json", id)
	restore()

	// 2: an ambiguous prefix.
	pass5Run(t, dir, 2, "id_ambiguous", "show", "--json", pass5Twin(t, dir, id))

	pass5NoState(t, 0, "", "show", "--json", pass5NoSuchID)
}

// TestDocClaim_ExitCodes_Close asserts the `act close` "Exit codes" line.
func TestDocClaim_ExitCodes_Close(t *testing.T) {
	dir := pass5Site(t)
	a := createBlocksIssue(t, dir, "closed twice")
	b := createBlocksIssue(t, dir, "closed beside a newer-writer op")
	c := createBlocksIssue(t, dir, "hooked, gated, then unreadable")

	// 0, and 0 again for an issue that is already closed.
	pass5Run(t, dir, 0, "", "close", "--json", "--reason", "done", a)
	if out, _ := pass5Run(t, dir, 0, "", "close", "--json", a); !strings.Contains(out, `"already_closed":true`) {
		t.Errorf("second close: want already_closed:true, got %s", out)
	}

	// 2: usage errors.
	pass5Run(t, dir, 2, "", "close", "--pass5-no-such-flag", c)
	pass5Run(t, dir, 2, "bad_flag", "close", "--json")
	pass5Run(t, dir, 2, "bad_flag", "close", "--json", "--reason", strings.Repeat("x", 501), c)
	pass5Run(t, dir, 2, "bad_flag", "close", "--json", "--no-commit", "--push", c)

	// 3: an id that matches nothing.
	pass5Run(t, dir, 3, "issue_not_found", "close", "--json", pass5NoSuchID)

	// 1: the close hook refuses.
	unhook := pass5Hook(t, dir, "close")
	pass5Run(t, dir, 1, "hook_failed", "close", "--json", c)
	unhook()

	// 2: an open external dep without --force.
	pass5Run(t, dir, 0, "", "dep", "add", c, "--external", "pass5:ref-1", "--json")
	pass5Run(t, dir, 2, "blocked_by_external_dep", "close", "--json", c)

	// Not 4: an op from a newer writer does not stop the close.
	restore := pass5NewerWriter(t, dir, c)
	pass5Run(t, dir, 0, "", "close", "--json", "--reason", "done", b)
	restore()

	// 1: the index cannot be rebuilt from the op log.
	restore = pass5UnknownOpVersion(t, dir, a)
	pass5Run(t, dir, 1, "index_rebuild_failed", "close", "--json", "--force", c)
	restore()

	// 2: an ambiguous prefix.
	pass5Run(t, dir, 2, "id_ambiguous", "close", "--json", pass5Twin(t, dir, c))

	pass5NoState(t, 3, "act_not_initialized", "close", "--json", pass5NoSuchID)
}

// TestDocClaim_ExitCodes_Ready asserts the `act ready` "Exit codes" line.
func TestDocClaim_ExitCodes_Ready(t *testing.T) {
	dir := pass5Site(t)

	// 0, including an empty ready set.
	if out, _ := pass5Run(t, dir, 0, "", "ready", "--json"); !strings.Contains(out, `"count":0`) {
		t.Errorf("ready on an empty tracker: want count 0, got %s", out)
	}
	id := createBlocksIssue(t, dir, "first")

	// 2: usage errors.
	pass5Run(t, dir, 2, "", "ready", "--pass5-no-such-flag")
	pass5Run(t, dir, 2, "bad_flag", "ready", "--json", "stray")
	pass5Run(t, dir, 2, "bad_flag", "ready", "--json", "--as", "someone")
	pass5Run(t, dir, 2, "bad_flag", "ready", "--json", "--no-fetch", "--fresh")
	pass5Run(t, dir, 2, "bad_flag", "ready", "--json", "--no-fetch", "--no-cache")

	// 3: --under matches nothing.
	pass5Run(t, dir, 3, "issue_not_found", "ready", "--json", "--under", pass5NoSuchID)

	// Not 4: an op with a newer writer_version is applied like any other.
	restore := pass5NewerWriter(t, dir, id)
	if out, _ := pass5Run(t, dir, 0, "", "ready", "--json"); !strings.Contains(out, id) {
		t.Errorf("ready beside a newer-writer op does not list %s: %s", id, out)
	}
	restore()

	// 1: an op the fold rejects.
	restore = pass5UnknownOpVersion(t, dir, id)
	pass5Run(t, dir, 1, "index_rebuild_failed", "ready", "--json")
	restore()

	// 2: an ambiguous --under prefix.
	pass5Run(t, dir, 2, "id_ambiguous", "ready", "--json", "--under", pass5Twin(t, dir, id))

	// 3: --mine cannot read .act/config.json.
	cfg := filepath.Join(dir, ".act", "config.json")
	if err := os.Rename(cfg, cfg+".pass5"); err != nil {
		t.Fatalf("move config.json aside: %v", err)
	}
	pass5Run(t, dir, 3, "no_repo", "ready", "--json", "--mine")

	pass5NoState(t, 0, "", "ready", "--json")
}

// TestDocClaim_ExitCodes_Search asserts the `act search` "Exit codes" line.
func TestDocClaim_ExitCodes_Search(t *testing.T) {
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "pass5needle in a haystack")

	// 0 with a match, and 0 with none.
	if out, _ := pass5Run(t, dir, 0, "", "search", "--json", "pass5needle"); !strings.Contains(out, id) {
		t.Errorf("search did not find %s: %s", id, out)
	}
	if out, _ := pass5Run(t, dir, 0, "", "search", "--json", "pass5absent"); !strings.Contains(out, `"count":0`) {
		t.Errorf("search for an absent word: want count 0, got %s", out)
	}

	// 0: FTS5 syntax is searched for literally, not rejected.
	for _, q := range []string{`"pass5unbalanced`, "pass5needle AND", "NEAR(", "title:pass5needle"} {
		pass5Run(t, dir, 0, "", "search", "--json", q)
	}

	// 2: usage errors.
	pass5Run(t, dir, 2, "", "search", "--pass5-no-such-flag", "x")
	pass5Run(t, dir, 2, "bad_flag", "search", "--json")
	pass5Run(t, dir, 2, "invalid_flag", "search", "--json", "")
	pass5Run(t, dir, 2, "bad_flag", "search", "--json", "two", "words")
	pass5Run(t, dir, 2, "invalid_flag", "search", "--json", "--in", "pass5", "x")
	pass5Run(t, dir, 2, "invalid_flag", "search", "--json", "--limit", "-1", "x")

	// Not 4: an op with a newer writer_version is indexed like any other.
	restore := pass5NewerWriter(t, dir, id)
	if out, _ := pass5Run(t, dir, 0, "", "search", "--json", "pass5needle"); !strings.Contains(out, id) {
		t.Errorf("search beside a newer-writer op did not find %s: %s", id, out)
	}
	restore()

	// 3 (where list and ready answer 1): the index cannot be brought up to date.
	restore = pass5UnknownOpVersion(t, dir, id)
	pass5Run(t, dir, 3, "index_rebuild_failed", "search", "--json", "pass5needle")
	pass5Run(t, dir, 1, "index_rebuild_failed", "list", "--json")
	restore()

	pass5NoState(t, 0, "", "search", "--json", "x")
}

// TestDocClaim_ExitCodes_Log asserts the `act log` "Exit codes" line.
func TestDocClaim_ExitCodes_Log(t *testing.T) {
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")
	other := createBlocksIssue(t, dir, "second")

	if out, _ := pass5Run(t, dir, 0, "", "log", "--json", id); !strings.Contains(out, `"op_type":"create"`) {
		t.Errorf("log does not show the create op: %s", out)
	}

	// 2: usage errors.
	pass5Run(t, dir, 2, "", "log", "--pass5-no-such-flag", id)
	pass5Run(t, dir, 2, "bad_flag", "log", "--json")
	pass5Run(t, dir, 2, "bad_flag", "log", "--json", id, "stray")
	pass5Run(t, dir, 2, "bad_flag", "log", "--json", "--since", "pass5", id)
	pass5Run(t, dir, 2, "bad_flag", "log", "--json", "--type", "pass5", id)
	pass5Run(t, dir, 2, "bad_flag", "log", "--json", "--by-issue", other, id)

	// 3: an id that matches nothing.
	pass5Run(t, dir, 3, "issue_not_found", "log", "--json", pass5NoSuchID)

	// Not 4: an op with a newer writer_version is printed like any other.
	restore := pass5NewerWriter(t, dir, id)
	if out, _ := pass5Run(t, dir, 0, "", "log", "--json", id); !strings.Contains(out, `"writer_version":"99.0.0"`) {
		t.Errorf("log does not print the newer-writer op: %s", out)
	}
	restore()

	// 3: an op file in scope cannot be parsed.
	restore = pass5UnknownOpVersion(t, dir, id)
	pass5Run(t, dir, 3, "ops_read_failed", "log", "--json", id)
	restore()

	// 2: an ambiguous prefix.
	pass5Run(t, dir, 2, "id_ambiguous", "log", "--json", pass5Twin(t, dir, id))

	pass5NoState(t, 0, "", "log", "--json", pass5NoSuchID)
}

// TestDocClaim_ExitCodes_MCP asserts the `act mcp` "Exit codes" line. The
// transport-error exit 1 is documented from code reading only.
func TestDocClaim_ExitCodes_MCP(t *testing.T) {
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")

	// 0: the client closes stdin.
	if out, stderr, code := pass5MCP(t, dir, nil, pass5MCPInit); code != 0 || !strings.Contains(out, `"serverInfo"`) {
		t.Errorf("act mcp, initialize then EOF: exit %d, want 0 with a serverInfo result\nstdout=%s\nstderr=%s", code, out, stderr)
	}

	// 2: an unknown flag, and a --workdir that is not there.
	if _, _, code := pass5MCP(t, dir, []string{"--pass5-no-such-flag"}); code != 2 {
		t.Errorf("act mcp with an unknown flag: exit %d, want 2", code)
	}
	if _, _, code := pass5MCP(t, dir, []string{"--workdir", filepath.Join(t.TempDir(), "absent")}); code != 2 {
		t.Errorf("act mcp --workdir <absent>: exit %d, want 2", code)
	}

	// Never 3: with no git repo the server starts, a tool call that needs
	// the tracker answers no_repo, and the server goes on to answer the
	// next request before exiting 0.
	const ping = `{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`
	out, stderr, code := pass5MCP(t, t.TempDir(), nil, pass5MCPInit, pass5MCPList, ping)
	if code != 0 {
		t.Errorf("act mcp outside git: exit %d, want 0\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	if !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "no_repo") {
		t.Errorf("act_list outside git did not answer a no_repo error result: %s", out)
	}
	if !strings.Contains(out, `"id":3`) || !strings.Contains(out, `"act_list"`) {
		t.Errorf("the server did not answer the request after the failed tool call: %s", out)
	}
	// The same in a git repo with no .act/.
	out, stderr, code = pass5MCP(t, brokencoHostRepo(t), nil, pass5MCPInit, pass5MCPList, ping)
	if code != 0 || !strings.Contains(out, `"isError":true`) || !strings.Contains(out, `"id":3`) {
		t.Errorf("act mcp in a git repo with no .act/: exit %d, want 0 with an error result and a later answer\nstdout=%s\nstderr=%s", code, out, stderr)
	}

	// Never 4: with a newer-writer op in the tracker a tool call still answers.
	restore := pass5NewerWriter(t, dir, id)
	out, stderr, code = pass5MCP(t, dir, nil, pass5MCPInit, pass5MCPList)
	if code != 0 || strings.Contains(out, `"isError":true`) || !strings.Contains(out, id) {
		t.Errorf("act mcp beside a newer-writer op: exit %d, want 0 and act_list naming %s\nstdout=%s\nstderr=%s", code, id, out, stderr)
	}
	restore()
}

// TestDocClaim_ExitCodes_Version asserts the `act version` "Exit codes" line:
// the one command that reports version_skew, and only under --check-repo.
func TestDocClaim_ExitCodes_Version(t *testing.T) {
	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")
	bare := t.TempDir()

	// Plain `act version` reads no repo: 0 anywhere.
	pass5Run(t, bare, 0, "", "version", "--json")
	pass5Run(t, dir, 0, "", "version", "--json")

	// 2: an unknown flag.
	pass5Run(t, dir, 2, "", "version", "--pass5-no-such-flag")

	// --check-repo: 0 on a tracker this binary wrote.
	pass5Run(t, dir, 0, "", "version", "--check-repo", "--json")

	// --check-repo: 3 outside a git working tree and in a repo with no
	// .act/. It resolves the repo root like the other read commands, so a
	// subdirectory of a repo that has a tracker is 0 (act-77b411).
	pass5Run(t, bare, 3, "not_in_git", "version", "--check-repo", "--json")
	pass5Run(t, brokencoHostRepo(t), 3, "no_repo", "version", "--check-repo", "--json")
	sub := filepath.Join(dir, "pass5-sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pass5Run(t, sub, 0, "", "version", "--check-repo", "--json")
	pass5Run(t, sub, 0, "", "list", "--json")

	// An unknown op_version is not skew: --check-repo compares writer_version.
	restore := pass5UnknownOpVersion(t, dir, id)
	pass5Run(t, dir, 0, "", "version", "--check-repo", "--json")
	restore()

	// 4 / version_skew on a newer writer_version, under --check-repo only.
	restore = pass5NewerWriter(t, dir, id)
	pass5Run(t, dir, 4, "version_skew", "version", "--check-repo", "--json")
	pass5Run(t, dir, 0, "", "version", "--json")
	restore()
	pass5Run(t, dir, 0, "", "version", "--check-repo", "--json")
}

// TestDocClaim_HelpErrors_VersionSkewHookFailedNoRepoRows asserts three rows
// of the `act help errors` code table against what emits each code: the
// table used to call version_skew an on-disk schema check, hook_failed a git
// pre-commit / pre-push hook, and no_repo a `git rev-parse` result. The
// "timed out" half of the hook_failed row is from code reading only.
func TestDocClaim_HelpErrors_VersionSkewHookFailedNoRepoRows(t *testing.T) {
	stdout, stderr, code := runAct(t, "help", "errors")
	help := stdout + stderr
	if code != 0 || len(help) < 500 {
		t.Fatalf("act help errors: exit %d, %d bytes", code, len(help))
	}
	flat := strings.Join(strings.Fields(help), " ")
	for _, want := range []string{
		"version_skew an op's writer_version is newer than this binary's; reported only by 'act version --check-repo' (exit 4)",
		"hook_failed a .act/hooks/<event> script exited non-zero or timed out",
		"no_repo no .act/ where the command looked (or, from an MCP tool call, no host git repo)",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("act help errors lacks the row %q", want)
		}
	}
	for _, stale := range []string{"on-disk schema newer", "pre-commit / pre-push hook", "git rev-parse showed no repo"} {
		if strings.Contains(flat, stale) {
			t.Errorf("act help errors still says %q", stale)
		}
	}

	dir := pass5Site(t)
	id := createBlocksIssue(t, dir, "first")

	// version_skew: a writer_version comparison, from version --check-repo,
	// exit 4 — and not from an op_version (schema) difference.
	restore := pass5UnknownOpVersion(t, dir, id)
	pass5Run(t, dir, 0, "", "version", "--check-repo", "--json")
	restore()
	restore = pass5NewerWriter(t, dir, id) // asserts exit 4 / version_skew itself
	pass5Run(t, dir, 0, "", "list", "--json")
	restore()

	// hook_failed: a .act/hooks/<event> script, not a git hook. A failing
	// git pre-commit hook in the nested repo does not produce it.
	gitHook := filepath.Join(dir, ".act", ".git", "hooks", "pre-commit")
	if err := os.MkdirAll(filepath.Dir(gitHook), 0o755); err != nil {
		t.Fatalf("mkdir git hooks: %v", err)
	}
	if err := os.WriteFile(gitHook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write git hook: %v", err)
	}
	pass5Run(t, dir, 0, "", "create", "--json", "past a failing git pre-commit hook")
	if err := os.Remove(gitHook); err != nil {
		t.Fatalf("remove git hook: %v", err)
	}
	unhook := pass5Hook(t, dir, "create")
	pass5Run(t, dir, 1, "hook_failed", "create", "--json", "refused by .act/hooks/create")
	unhook()

	// no_repo: no .act/ where the command looked — the root of a git repo
	// that has no tracker (act-77b411: it looks there, not in the cwd) ...
	pass5Run(t, brokencoHostRepo(t), 3, "no_repo", "version", "--check-repo", "--json")
	// ... and, from an MCP tool call, no host git repo.
	if out, _, code := pass5MCP(t, t.TempDir(), nil, pass5MCPInit, pass5MCPList); code != 0 || !strings.Contains(out, "no_repo") {
		t.Errorf("act_list outside git: exit %d, want 0 with a no_repo error result: %s", code, out)
	}
}
