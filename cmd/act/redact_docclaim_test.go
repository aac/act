package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// act-404754 (docs/redaction-design.md §5, §10 U3): `act redact` at the
// binary boundary. Every test plants one string in a scratch tracker, runs
// the real binary, and reads what is on disk and what was printed.
// Helpers are prefixed `redCmd`.

const redCmdPlanted = "PLANTED-4417-9902-7731"

// redCmdResult is the `act redact --json` result object.
type redCmdResult struct {
	OK                   bool   `json:"ok"`
	Error                string `json:"error"`
	DryRun               bool   `json:"dry_run"`
	Patterns             int    `json:"patterns"`
	Issues               int    `json:"issues"`
	OpsRewritten         int    `json:"ops_rewritten"`
	Replacements         int    `json:"replacements"`
	LeftoversRemoved     int    `json:"leftovers_removed"`
	QuarantinedRewritten int    `json:"quarantined_rewritten"`
	Published            bool   `json:"published"`
	PushDeferred         bool   `json:"push_deferred"`
	PushDeferredReason   string `json:"push_deferred_reason"`
}

// redCmdGit runs git and returns its combined output, failing on error.
func redCmdGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// redCmdSite makes a tracker whose issue holds the planted string in its
// create op (title and description) and in a later description update.
// With origin, a bare repo is attached and published to first, so the
// create and the update are on it. Returns the host dir, the bare path
// ("" without origin) and the issue id.
func redCmdSite(t *testing.T, withOrigin bool) (dir, bare, id string) {
	t.Helper()
	dir = pass5Site(t)
	if withOrigin {
		bare = filepath.Join(t.TempDir(), "origin.git")
		redCmdGit(t, "init", "-q", "--bare", bare)
		redCmdGit(t, "-C", filepath.Join(dir, ".act"), "remote", "add", "origin", bare)
		redCmdGit(t, "-C", filepath.Join(dir, ".act"), "push", "-q", "-u", "origin", "main")
	}
	id = createIssue(t, dir, "Statement for "+redCmdPlanted, "--description", "Wire from account "+redCmdPlanted+".")
	if out, stderr, code := runActIn(t, dir, "update", id, "--description", "Wire from account "+redCmdPlanted+" to savings."); code != 0 {
		t.Fatalf("act update: exit %d\n%s\n%s", code, out, stderr)
	}
	return dir, bare, id
}

// redCmdPatterns writes a patterns file holding lines and returns its path.
func redCmdPatterns(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "patterns")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write patterns: %v", err)
	}
	return p
}

// redCmdRun runs act in dir with stdin and returns stdout, stderr, exit.
func redCmdRun(t *testing.T, dir, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(actBinary(t), args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
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
	t.Fatalf("act %v: failed to launch: %v", args, err)
	return "", "", -1
}

// redCmdRedact runs `act redact --json --patterns-file p` in dir and decodes
// the result object (or envelope) from stdout.
func redCmdRedact(t *testing.T, dir, p string, extra ...string) (res redCmdResult, raw map[string]any, stdout, stderr string, code int) {
	t.Helper()
	stdout, stderr, code = runActIn(t, dir, append([]string{"redact", "--json", "--patterns-file", p}, extra...)...)
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("act redact: stdout is not one JSON object (exit %d): %v\nstdout=%s\nstderr=%s", code, err, stdout, stderr)
	}
	_ = json.Unmarshal([]byte(stdout), &res)
	return res, raw, stdout, stderr, code
}

// redCmdHolders lists the regular files under root, outside any .git
// directory, whose bytes hold the planted string — `grep -rl
// --exclude-dir=.git`.
func redCmdHolders(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(b, []byte(redCmdPlanted)) {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// redCmdOriginHolds reports whether the bare repo's main tree holds the
// planted string under ops/ (`git grep` exits 1 when nothing matches).
func redCmdOriginHolds(t *testing.T, bare string) bool {
	t.Helper()
	out, err := exec.Command("git", "--git-dir", bare, "grep", "-l", redCmdPlanted, "main", "--", "ops").CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return false
		}
		t.Fatalf("git grep in %s: %v\n%s", bare, err, out)
	}
	return strings.TrimSpace(string(out)) != ""
}

// redCmdState is the nested repo's HEAD and `git status --porcelain`.
func redCmdState(t *testing.T, dir string) string {
	t.Helper()
	act := filepath.Join(dir, ".act")
	return redCmdGit(t, "-C", act, "rev-parse", "HEAD") + redCmdGit(t, "-C", act, "status", "--porcelain")
}

// redCmdNoPlanted fails when any text holds the planted string.
func redCmdNoPlanted(t *testing.T, what string, texts ...string) {
	t.Helper()
	for _, s := range texts {
		if strings.Contains(s, redCmdPlanted) {
			t.Errorf("%s prints the planted string:\n%s", what, s)
		}
	}
}

// redCmdOneLine collapses runs of whitespace, so a help claim that wraps
// across lines can be matched as one sentence.
func redCmdOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestDocClaim_Redact_RemovesStringFromEveryOpFile(t *testing.T) {
	dir, bare, id := redCmdSite(t, true)
	act := filepath.Join(dir, ".act")
	// Premise: both op files hold it, and so does the index once read.
	if out, stderr, code := runActIn(t, dir, "list", "--json"); code != 0 {
		t.Fatalf("act list: exit %d\n%s\n%s", code, out, stderr)
	}
	if h := redCmdHolders(t, filepath.Join(act, "ops")); len(h) != 2 {
		t.Fatalf("premise: %d op files hold the planted string, want 2 (create and update): %v", len(h), h)
	}
	if len(redCmdHolders(t, filepath.Join(act, "index.db"))) == 0 {
		t.Fatalf("premise: index.db does not hold the planted string")
	}
	// Hooks never run on act redact: one that fires would leave a marker.
	marker := filepath.Join(t.TempDir(), "hook-ran")
	for _, h := range []string{"create", "close", "claim"} {
		if err := os.WriteFile(filepath.Join(act, "hooks", h), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
			t.Fatalf("write hook: %v", err)
		}
	}

	res, _, stdout, stderr, code := redCmdRedact(t, dir, redCmdPatterns(t, redCmdPlanted))
	if code != 0 || !res.OK || res.OpsRewritten != 2 || res.Replacements != 3 || res.Issues != 1 || !res.Published {
		t.Fatalf("act redact: exit %d, result %+v; want exit 0, ok, ops_rewritten 2, replacements 3, issues 1, published\nstderr=%s", code, res, stderr)
	}
	for _, k := range []string{"ok", "dry_run", "patterns", "issues", "ops_rewritten", "replacements", "leftovers_removed", "quarantined_rewritten", "published"} {
		if !strings.Contains(stdout, `"`+k+`":`) {
			t.Errorf("result lacks key %q: %s", k, stdout)
		}
	}
	if h := redCmdHolders(t, filepath.Join(act, "ops")); len(h) != 0 {
		t.Errorf("op files still hold the planted string: %v", h)
	}
	if h := redCmdHolders(t, act); len(h) != 0 {
		t.Errorf("files under .act/ outside .git still hold the planted string (index.db is deleted and rebuilt): %v", h)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a hook ran during act redact")
	}
	show, _, _ := runActIn(t, dir, "show", id, "--no-fetch")
	if !strings.Contains(show, "Wire from account [redacted] to savings.") || !strings.Contains(show, "Statement for [redacted]") {
		t.Errorf("act show does not render [redacted]:\n%s", show)
	}
	logged, _, _ := runActIn(t, dir, "log", id, "--summary", "--no-fetch")
	if !strings.Contains(logged, "redact ") || !strings.Contains(logged, "re-issued 2 op file(s)") {
		t.Errorf("act log --summary does not show the redact record as re-issued 2 op file(s):\n%s", logged)
	}
	doc, dcode := sp6RunDoctor(t, dir)
	if dcode != 0 || len(doc.Findings) != 0 {
		t.Errorf("act doctor after the redaction: exit %d, %d findings, want 0 and 0: %+v", dcode, len(doc.Findings), doc.Findings)
	}
	// One commit, act-redact subject, on origin.
	subject := strings.TrimSpace(redCmdGit(t, "-C", act, "log", "-1", "--format=%s"))
	if subject != "act-redact: re-issued 2 op file(s) in 1 issue(s)" {
		t.Errorf("commit subject %q", subject)
	}
	if redCmdOriginHolds(t, bare) {
		t.Errorf("origin's current tree still holds the planted string")
	}
	// The limit: history is not rewritten.
	if hist := redCmdGit(t, "-C", act, "log", "--oneline", "-S"+redCmdPlanted, "--", "ops"); strings.TrimSpace(hist) == "" {
		t.Errorf("the nested repo's history no longer holds the string; act redact must not rewrite history")
	}
}

func TestDocClaim_Redact_PrintsCountsNeverTheString(t *testing.T) {
	p := redCmdPatterns(t, redCmdPlanted)

	// A run and a dry run, human and JSON.
	dir, _, _ := redCmdSite(t, false)
	for _, args := range [][]string{
		{"redact", "--patterns-file", p, "--dry-run"},
		{"redact", "--patterns-file", p, "--dry-run", "--json"},
		{"redact", "--patterns-file", p},
	} {
		out, stderr, code := runActIn(t, dir, args...)
		if code != 0 {
			t.Fatalf("act %v: exit %d\n%s\n%s", args, code, out, stderr)
		}
		redCmdNoPlanted(t, "act "+strings.Join(args, " "), out, stderr)
	}
	human, _, _ := runActIn(t, t.TempDir(), "help", "redact")
	if !strings.Contains(human, "prints counts only") {
		t.Errorf("act help redact lacks the counts-only claim")
	}

	// A residual refusal, human and JSON.
	dir2, _, _ := redCmdSite(t, false)
	createIssue(t, dir2, "pinned", "--machine", redCmdPlanted)
	for _, j := range []bool{false, true} {
		args := []string{"redact", "--patterns-file", p}
		if j {
			args = append(args, "--json")
		}
		out, stderr, code := runActIn(t, dir2, args...)
		if code != 1 || !strings.Contains(out+stderr, "redact_residual") && j {
			t.Fatalf("act %v: exit %d, want 1 redact_residual\n%s\n%s", args, code, out, stderr)
		}
		redCmdNoPlanted(t, "a residual refusal", out, stderr)
	}

	// An exit-6 run.
	dir3, bare, _ := redCmdSite(t, true)
	if err := os.WriteFile(filepath.Join(bare, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write pre-receive: %v", err)
	}
	out, stderr, code := runActIn(t, dir3, "redact", "--patterns-file", p)
	if code != 6 {
		t.Fatalf("act redact with origin refusing pushes: exit %d, want 6\n%s\n%s", code, out, stderr)
	}
	redCmdNoPlanted(t, "an unconfirmed run", out, stderr)
}

func TestDocClaim_Redact_PositionalArgumentRefusedUnechoed(t *testing.T) {
	dir, _, _ := redCmdSite(t, false)
	before := redCmdState(t, dir)
	const want = "act redact: takes no positional arguments; strings to remove are read from --patterns-file"
	p := redCmdPatterns(t, redCmdPlanted)
	for _, args := range [][]string{
		{"redact", redCmdPlanted},
		{"redact", "--json", redCmdPlanted},
		{"redact", "--patterns-file", p, redCmdPlanted},
		{"redact", "--patterns-file", p, "--", redCmdPlanted},
	} {
		out, stderr, code := runActIn(t, dir, args...)
		if code != 2 || !strings.Contains(out+stderr, want) {
			t.Errorf("act %d args: exit %d, want 2 with the fixed message\nstdout=%s\nstderr=%s", len(args), code, out, stderr)
		}
		if strings.Contains(strings.Join(args, " "), "--json") && !strings.Contains(out, `"error":"bad_flag"`) {
			t.Errorf("--json positional refusal is not the bad_flag envelope: %s", out)
		}
		redCmdNoPlanted(t, "a positional refusal", out, stderr)
	}
	if after := redCmdState(t, dir); after != before {
		t.Errorf("a refused run changed the nested repo:\nbefore %s\nafter  %s", before, after)
	}
	// None of the universal write flags is accepted.
	for _, f := range []string{"--no-commit", "--push", "--isolated", "--offline", "--verify", "--branch=x"} {
		pass5Run(t, dir, 2, "bad_flag", "redact", "--json", "--patterns-file", p, f)
	}
	pass5Run(t, dir, 2, "bad_flag", "redact", "--json")
}

func TestDocClaim_Redact_PatternsFromStdin(t *testing.T) {
	dir, _, id := redCmdSite(t, false)
	out, stderr, code := redCmdRun(t, dir, "  "+redCmdPlanted+"\t\r\n\n"+redCmdPlanted+"\n", "redact", "--patterns-file", "-", "--json")
	var res redCmdResult
	if err := json.Unmarshal([]byte(out), &res); err != nil || code != 0 || res.Patterns != 1 || res.OpsRewritten != 2 {
		t.Fatalf("act redact --patterns-file - : exit %d, result %s (%v); want exit 0, patterns 1 (trimmed and de-duplicated), ops_rewritten 2\n%s", code, out, err, stderr)
	}
	if h := redCmdHolders(t, filepath.Join(dir, ".act")); len(h) != 0 {
		t.Errorf("files still hold the string: %v", h)
	}
	if show, _, _ := runActIn(t, dir, "show", id, "--no-fetch"); !strings.Contains(show, "[redacted]") {
		t.Errorf("act show does not render [redacted]:\n%s", show)
	}
	usage, uerr, _ := runActIn(t, t.TempDir(), "redact", "-h")
	if !strings.Contains(usage+uerr, "use - for stdin. Strings are never taken from the command line") {
		t.Errorf("act redact -h does not describe --patterns-file -:\n%s", usage+uerr)
	}
}

func TestDocClaim_Redact_ShortPatternIsBadInput(t *testing.T) {
	dir, _, _ := redCmdSite(t, false)
	before := redCmdState(t, dir)
	for _, tc := range []struct {
		lines []string
		want  string
	}{
		{[]string{redCmdPlanted, "", "4417-99"}, "line 3: pattern is shorter than 8 bytes"},
		{[]string{"", "  "}, "holds no patterns"},
		{[]string{"redacted]"}, "line 1: pattern occurs inside the replacement text [redacted]"},
	} {
		out, stderr := pass5Run(t, dir, 2, "bad_input", "redact", "--json", "--patterns-file", redCmdPatterns(t, tc.lines...))
		if !strings.Contains(out, tc.want) {
			t.Errorf("patterns %q: message does not say %q\n%s", tc.lines, tc.want, out)
		}
		redCmdNoPlanted(t, "a bad_input refusal", out, stderr)
		if strings.Contains(out, "4417-99") {
			t.Errorf("the bad_input message echoes the short pattern: %s", out)
		}
	}
	pass5Run(t, dir, 3, "file_not_found", "redact", "--json", "--patterns-file", filepath.Join(t.TempDir(), "missing"))
	if after := redCmdState(t, dir); after != before {
		t.Errorf("a refused patterns file changed the nested repo")
	}
}

func TestDocClaim_Redact_DryRunChangesNothing(t *testing.T) {
	dir, _, _ := redCmdSite(t, true)
	before := redCmdState(t, dir)
	res, _, _, stderr, code := redCmdRedact(t, dir, redCmdPatterns(t, redCmdPlanted), "--dry-run")
	if code != 0 || !res.OK || !res.DryRun || res.OpsRewritten != 2 || res.Replacements != 3 || res.Published {
		t.Fatalf("dry run: exit %d, result %+v; want exit 0, dry_run, ops_rewritten 2, replacements 3, not published\n%s", code, res, stderr)
	}
	if after := redCmdState(t, dir); after != before {
		t.Errorf("--dry-run changed HEAD or the working tree:\nbefore %s\nafter  %s", before, after)
	}
	if h := redCmdHolders(t, filepath.Join(dir, ".act", "ops")); len(h) != 2 {
		t.Errorf("after --dry-run %d op files hold the string, want both still: %v", len(h), h)
	}
	human, _, _ := runActIn(t, dir, "redact", "--dry-run", "--patterns-file", redCmdPatterns(t, redCmdPlanted))
	if !strings.HasPrefix(human, "Would redact 3 occurrence(s) in 2 op file(s) across 1 issue(s)") || !strings.Contains(human, "Nothing was changed.") {
		t.Errorf("human dry run: %q", human)
	}
}

func TestDocClaim_Redact_ResidualRefusesBeforeChanging(t *testing.T) {
	p := redCmdPatterns(t, redCmdPlanted)
	// The string also given as a machine label: a structural field.
	dir, _, _ := redCmdSite(t, false)
	pinned := createIssue(t, dir, "pinned", "--machine", redCmdPlanted)
	before := redCmdState(t, dir)
	_, raw, _, _, code := redCmdRedact(t, dir, p)
	if code != 1 || raw["error"] != "redact_residual" {
		t.Fatalf("exit %d, %v; want 1 redact_residual", code, raw)
	}
	details, _ := raw["details"].(map[string]any)
	paths, _ := details["paths"].([]any)
	if len(paths) != 1 || !regexp.MustCompile(`^ops/`+pinned+`/.*-create\.json$`).MatchString(paths[0].(string)) || details["changed"] != false {
		t.Errorf("details %v: want paths naming the pinned issue's create op file (relative to .act/) and changed false", details)
	}
	if after := redCmdState(t, dir); after != before {
		t.Errorf("a residual refusal changed HEAD or the working tree")
	}
	if h := redCmdHolders(t, filepath.Join(dir, ".act", "ops")); len(h) != 3 {
		t.Errorf("after the refusal %d op files hold the string, want all 3 untouched: %v", len(h), h)
	}

	// A hook script and a compaction snapshot are files act does not
	// rewrite either.
	dir2, _, _ := redCmdSite(t, false)
	act := filepath.Join(dir2, ".act")
	if err := os.WriteFile(filepath.Join(act, "hooks", "close"), []byte("#!/bin/sh\n# "+redCmdPlanted+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(act, "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(act, "snapshots", "snap.json"), []byte(`{"title":"`+redCmdPlanted+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before2 := redCmdState(t, dir2)
	_, raw, _, _, code = redCmdRedact(t, dir2, p)
	details, _ = raw["details"].(map[string]any)
	if code != 1 || raw["error"] != "redact_residual" || len(details["paths"].([]any)) != 2 ||
		details["paths"].([]any)[0] != "hooks/close" || details["paths"].([]any)[1] != "snapshots/snap.json" {
		t.Errorf("hook script and snapshot: exit %d, %v; want 1 redact_residual naming hooks/close and snapshots/snap.json", code, raw)
	}
	if after := redCmdState(t, dir2); after != before2 {
		t.Errorf("a residual refusal changed HEAD or the working tree")
	}
	help, _, _ := runActIn(t, t.TempDir(), "help", "redact")
	if !strings.Contains(redCmdOneLine(help), "a hook script, a compaction snapshot under .act/snapshots/), nothing is changed and the command exits 1 (redact_residual), naming the files.") {
		t.Errorf("act help redact does not state the residual refusal")
	}
}

func TestDocClaim_Redact_SecondRunIsANoOp(t *testing.T) {
	dir, _, _ := redCmdSite(t, true)
	p := redCmdPatterns(t, redCmdPlanted)
	if _, _, _, stderr, code := redCmdRedact(t, dir, p); code != 0 {
		t.Fatalf("first run: exit %d\n%s", code, stderr)
	}
	before := redCmdState(t, dir)
	res, _, _, stderr, code := redCmdRedact(t, dir, p)
	if code != 0 || !res.OK || res.OpsRewritten != 0 || res.Replacements != 0 || res.LeftoversRemoved != 0 || res.QuarantinedRewritten != 0 || res.Issues != 0 || !res.Published {
		t.Errorf("second run: exit %d, %+v; want exit 0, zero counts, published\n%s", code, res, stderr)
	}
	if after := redCmdState(t, dir); after != before {
		t.Errorf("the second run changed HEAD or the working tree:\nbefore %s\nafter  %s", before, after)
	}
	human, _, _ := runActIn(t, dir, "redact", "--patterns-file", p)
	if human != "No stored file holds any of the 1 pattern(s). Nothing was changed.\n" {
		t.Errorf("human second run: %q", human)
	}
}

func TestDocClaim_Redact_UnreachableOriginChangesNothing(t *testing.T) {
	dir, bare, _ := redCmdSite(t, true)
	if err := os.RemoveAll(bare); err != nil {
		t.Fatal(err)
	}
	before := redCmdState(t, dir)
	_, raw, stdout, stderr, code := redCmdRedact(t, dir, redCmdPatterns(t, redCmdPlanted))
	if code != 3 || raw["error"] != "remote_unreachable" {
		t.Fatalf("exit %d, %s; want 3 remote_unreachable\n%s", code, stdout, stderr)
	}
	details, _ := raw["details"].(map[string]any)
	if _, ok := details["url"]; !ok {
		t.Errorf("details lacks url: %v", details)
	}
	if _, ok := details["stderr_tail"]; !ok {
		t.Errorf("details lacks stderr_tail: %v", details)
	}
	if after := redCmdState(t, dir); after != before {
		t.Errorf("an unreachable origin still changed HEAD or the working tree")
	}
	if h := redCmdHolders(t, filepath.Join(dir, ".act", "ops")); len(h) != 2 {
		t.Errorf("%d op files hold the string, want both untouched: %v", len(h), h)
	}
	redCmdNoPlanted(t, "remote_unreachable", stdout, stderr)
}

func TestDocClaim_Redact_RejectedPushIsUnconfirmedThenConfirms(t *testing.T) {
	dir, bare, _ := redCmdSite(t, true)
	act := filepath.Join(dir, ".act")
	hook := filepath.Join(bare, "hooks", "pre-receive")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'redcmd: pushes refused' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write pre-receive: %v", err)
	}
	p := redCmdPatterns(t, redCmdPlanted)
	res, _, stdout, stderr, code := redCmdRedact(t, dir, p)
	if code != 6 || res.OK || res.Error != "redact_unconfirmed" || res.Published || !res.PushDeferred || res.PushDeferredReason == "" || res.OpsRewritten != 2 {
		t.Fatalf("push refused: exit %d, %s; want exit 6, ok false, redact_unconfirmed, published false, push_deferred with a reason, ops_rewritten 2\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "act: WARNING:") || !strings.Contains(stderr, "re-run act redact with the same patterns file once origin is reachable; it re-issues nothing twice and publishes this commit") {
		t.Errorf("stderr lacks the WARNING block ending with the redact re-run line:\n%s", stderr)
	}
	if h := redCmdHolders(t, act); len(h) != 0 {
		t.Errorf("the local tree still holds the string: %v", h)
	}
	if !redCmdOriginHolds(t, bare) {
		t.Fatalf("premise: origin's tree should still hold the string while the push is refused")
	}
	pending, err := os.ReadFile(filepath.Join(act, ".pending-pushes"))
	if err != nil || !strings.Contains(string(pending), `"op_type":"redact"`) {
		t.Errorf(".act/.pending-pushes does not record a redact commit (%v):\n%s", err, pending)
	}
	human, herr, hcode := runActIn(t, dir, "redact", "--patterns-file", p)
	if hcode != 6 || !strings.Contains(human, "redact_unconfirmed") {
		t.Errorf("human re-run while refused: exit %d\n%s\n%s", hcode, human, herr)
	}

	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	res, _, stdout, stderr, code = redCmdRedact(t, dir, p)
	if code != 0 || !res.OK || !res.Published || res.OpsRewritten != 0 {
		t.Fatalf("re-run once origin accepts: exit %d, %s; want exit 0, ok, published, nothing re-issued twice\n%s", code, stdout, stderr)
	}
	if redCmdOriginHolds(t, bare) {
		t.Errorf("origin's tree still holds the string after the confirming run")
	}
}

func TestDocClaim_Redact_QuarantinedCopyIsRewritten(t *testing.T) {
	dir, _, id := redCmdSite(t, false)
	act := filepath.Join(dir, ".act")
	create := pass5CreateOp(t, dir, id)
	rel, _ := filepath.Rel(act, create)
	qdir := filepath.Join(act, ".failed-ops", "20261128T000000Z", filepath.Dir(rel))
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(create)
	if err != nil {
		t.Fatal(err)
	}
	qfile := filepath.Join(qdir, filepath.Base(create))
	if err := os.WriteFile(qfile, body, 0o644); err != nil {
		t.Fatal(err)
	}
	res, _, stdout, stderr, code := redCmdRedact(t, dir, redCmdPatterns(t, redCmdPlanted))
	if code != 0 || res.QuarantinedRewritten != 1 || res.OpsRewritten != 2 {
		t.Fatalf("exit %d, %s; want exit 0, quarantined_rewritten 1, ops_rewritten 2\n%s", code, stdout, stderr)
	}
	if _, err := os.Stat(qfile); err == nil {
		t.Errorf("the quarantined original %s is still there", qfile)
	}
	entries, _ := os.ReadDir(qdir)
	if len(entries) != 1 {
		t.Fatalf("%s holds %d files, want the one re-issued copy", qdir, len(entries))
	}
	got, _ := os.ReadFile(filepath.Join(qdir, entries[0].Name()))
	if !strings.Contains(string(got), "Statement for [redacted]") || !strings.Contains(string(got), `"redacted":1`) {
		t.Errorf("the re-issued quarantined copy does not carry [redacted] and generation 1: %s", got)
	}
	if h := redCmdHolders(t, act); len(h) != 0 {
		t.Errorf("files still hold the string: %v", h)
	}
}

func TestDocClaim_Redact_HelpSaysHistoryIsNotRewritten(t *testing.T) {
	for _, topic := range []string{"redact", "redaction", "REDACT"} {
		out, stderr, code := runActIn(t, t.TempDir(), "help", topic)
		if code != 0 || !strings.Contains(out, "act redact does not rewrite git history.") {
			t.Errorf("act help %s: exit %d, lacks 'does not rewrite git history'\n%s%s", topic, code, out, stderr)
		}
	}
	overview, _, _ := runActIn(t, t.TempDir(), "help")
	if !strings.Contains(overview, "act help redact           # remove a string from stored op files") {
		t.Errorf("act help does not list the redact topic in DEEPER DIVES")
	}
	if !strings.Contains(redCmdOneLine(overview), "delete, redact, update") {
		t.Errorf("act help's subcommand list lacks redact")
	}
	ops, _, _ := runActIn(t, t.TempDir(), "help", "ops-model")
	if !strings.Contains(redCmdOneLine(ops), "'act redact' is the one command that replaces files: it writes each affected op again under a new name and deletes the old one (act help redact).") {
		t.Errorf("act help ops-model lacks the redact exception to never-modified files")
	}
	_, stderr, code := runActIn(t, t.TempDir(), "help", "no-such-topic")
	if code != 2 || !strings.Contains(stderr, "redact") {
		t.Errorf("unknown-topic list lacks redact: exit %d, %s", code, stderr)
	}
	_, bare, _ := runActIn(t, t.TempDir())
	if !strings.Contains(bare, "delete, redact, update") {
		t.Errorf("bare usage lacks redact: %s", bare)
	}
}

// act-f3c274: a missing patterns file never has its path echoed, in text or
// JSON, and the exit code and error code are unchanged.
func TestDocClaim_Redact_PatternsPathNeverPrinted(t *testing.T) {
	dir := pass5Site(t)
	const planted = "patMsgSECRET-5521-0042"
	missing := filepath.Join(t.TempDir(), planted)
	for _, asJSON := range []bool{false, true} {
		args := []string{"redact", "--patterns-file", missing}
		if asJSON {
			args = append(args, "--json")
		}
		stdout, stderr, code := redCmdRun(t, dir, "", args...)
		if code != 3 {
			t.Errorf("json=%v: exit %d, want 3\n%s\n%s", asJSON, code, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, planted) {
			t.Errorf("json=%v: output echoes the path\nstdout=%s\nstderr=%s", asJSON, stdout, stderr)
		}
		if !strings.Contains(stdout+stderr, "file_not_found") {
			t.Errorf("json=%v: error code file_not_found missing\n%s\n%s", asJSON, stdout, stderr)
		}
	}
}

// act-88f2aa: an oversized patterns file is refused in terms of the
// patterns file, not the description cap.
func TestDocClaim_Redact_OversizePatternsFileMessage(t *testing.T) {
	dir := pass5Site(t)
	p := filepath.Join(t.TempDir(), "patMsgBig")
	if err := os.WriteFile(p, bytes.Repeat([]byte("a"), 1048576+1), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := redCmdRun(t, dir, "", "redact", "--patterns-file", p, "--json")
	if code != 2 {
		t.Fatalf("exit %d, want 2\n%s\n%s", code, stdout, stderr)
	}
	out := stdout + stderr
	if !strings.Contains(out, "patterns file") || strings.Contains(out, "description") || strings.Contains(out, "patMsgBig") {
		t.Errorf("message should name the patterns file only: %s", out)
	}
}
