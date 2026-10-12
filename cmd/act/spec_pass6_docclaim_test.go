package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// Doc-claim cover for the sixth stale pass over docs/spec.md (act-f7a994),
// the half asserted against the real binary. The half that compares the
// spec's doctor check lists with allChecks lives in
// internal/cli/spec_pass6_docclaim_test.go.
//
// What was stale: the universal exit-code list gave 4 one meaning it does not
// have; `--check` was called repeatable; four doctor checks had no table row;
// the `act show` / `act log` synopses left out most of their flags; the
// `act help errors` table listed three codes nothing emits; the
// `--check-repo` bullet did not say where it looks; and the remote_status
// intro said every field defaults to its zero value.

// sp6RepoRoot is the root of the source tree this test file belongs to.
func sp6RepoRoot(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(here), "..", ".."))
}

// sp6Spec reads docs/spec.md.
func sp6Spec(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	return string(body)
}

// sp6Section returns the spec text from the given "### " heading line up to
// the next "### " heading.
func sp6Section(t *testing.T, spec, heading string) string {
	t.Helper()
	i := strings.Index(spec, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("docs/spec.md has no heading %q", heading)
	}
	rest := spec[i+len(heading)+2:]
	if j := strings.Index(rest, "\n### "); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

// sp6DefinedFlags returns the flags subcommand sub defines, read from the
// usage text its own flag set prints for -h. Run outside any git repo so the
// no-state guard stays out of the way.
func sp6DefinedFlags(t *testing.T, sub string) []string {
	t.Helper()
	stdout, stderr, _ := runActIn(t, t.TempDir(), sub, "-h")
	usage := stdout + stderr
	if !strings.Contains(usage, "Usage of "+sub+":") {
		t.Fatalf("act %s -h did not print the flag set's usage:\n%s", sub, usage)
	}
	var flags []string
	for _, m := range regexp.MustCompile(`(?m)^  -([a-z][a-z0-9-]*)`).FindAllStringSubmatch(usage, -1) {
		flags = append(flags, m[1])
	}
	sort.Strings(flags)
	return flags
}

// sp6Site makes an isolated tracker and returns its directory.
func sp6Site(t *testing.T) string {
	t.Helper()
	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")
	t.Setenv("ACT_NO_FETCH", "")
	return blocksSite(t)
}

// sp6Doctor is the part of `act doctor --json` these tests read.
type sp6Doctor struct {
	Findings []struct {
		Check    string `json:"check"`
		Severity string `json:"severity"`
		Message  string `json:"message"`
	} `json:"findings"`
	RemoteStatus map[string]any `json:"remote_status"`
}

// sp6RunDoctor runs `act doctor --json <args>` and returns the parsed result
// and the exit code. It fails the test when stdout is not a doctor result.
func sp6RunDoctor(t *testing.T, dir string, args ...string) (sp6Doctor, int) {
	t.Helper()
	out, stderr, code := runActIn(t, dir, append([]string{"doctor", "--json"}, args...)...)
	var res sp6Doctor
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.RemoteStatus == nil {
		t.Fatalf("act doctor --json %v: exit %d, stdout is not a doctor result (%v)\nstdout=%s\nstderr=%s", args, code, err, out, stderr)
	}
	return res, code
}

// sp6Checks returns the check name of each finding, with its severity.
func sp6Checks(res sp6Doctor) []string {
	var out []string
	for _, f := range res.Findings {
		out = append(out, f.Check+"/"+f.Severity)
	}
	return out
}

// sp6Has reports whether res holds a finding with this check and severity.
func sp6Has(res sp6Doctor, check, severity string) bool {
	for _, f := range res.Findings {
		if f.Check == check && f.Severity == severity {
			return true
		}
	}
	return false
}

// sp6Git runs git with args and fails the test on error.
func sp6Git(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// sp6LogOps runs `act log --json <args>` and returns the op types in order.
func sp6LogOps(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	out, stderr, code := runActIn(t, dir, append([]string{"log", "--json"}, args...)...)
	if code != 0 {
		t.Fatalf("act log --json %v: exit %d\nstdout=%s\nstderr=%s", args, code, out, stderr)
	}
	var res struct {
		Ops []struct {
			OpType string `json:"op_type"`
		} `json:"ops"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("act log --json %v: %v\nstdout=%s", args, err, out)
	}
	types := []string{}
	for _, o := range res.Ops {
		types = append(types, o.OpType)
	}
	return types
}

// sp6RefreshReason returns refresh.reason from a --json read result.
func sp6RefreshReason(t *testing.T, stdout string) string {
	t.Helper()
	var doc struct {
		Refresh *struct {
			Reason string `json:"reason"`
		} `json:"refresh"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("parse %q: %v", stdout, err)
	}
	if doc.Refresh == nil {
		return ""
	}
	return doc.Refresh.Reason
}

// TestDocClaim_Spec_ShowLogSynopsesNameEveryFlag asserts that the `act show`
// and `act log` sections of the spec name every flag the binary's flag set
// defines — in the Synopsis line and as a bullet under Flags — and that the
// synopsis names no flag the binary lacks. The expected set comes from the
// flag set itself (`act <cmd> -h`), so a flag added without a doc line turns
// this red.
func TestDocClaim_Spec_ShowLogSynopsesNameEveryFlag(t *testing.T) {
	spec := sp6Spec(t)
	for _, tc := range []struct{ sub, heading string }{
		{"show", "### `act show <id>`"},
		{"log", "### `act log <id>`"},
	} {
		flags := sp6DefinedFlags(t, tc.sub)
		defined := map[string]bool{}
		for _, f := range flags {
			defined[f] = true
		}
		if !defined["json"] || len(flags) < 5 {
			t.Fatalf("act %s -h: parsed flags %v; expected at least --json and four more", tc.sub, flags)
		}
		section := sp6Section(t, spec, tc.heading)
		m := regexp.MustCompile("(?m)^\\*\\*Synopsis:\\*\\* `([^`]*)`").FindStringSubmatch(section)
		if m == nil {
			t.Fatalf("spec section %s has no Synopsis line", tc.heading)
		}
		synopsis := m[1]
		named := map[string]bool{}
		for _, fm := range regexp.MustCompile(`\[--([a-z][a-z0-9-]*)[ \]]`).FindAllStringSubmatch(synopsis, -1) {
			named[fm[1]] = true
		}
		for _, f := range flags {
			if !named[f] {
				t.Errorf("spec synopsis for act %s (%q) omits --%s, which the flag set defines (%v)", tc.sub, synopsis, f, flags)
			}
			if !regexp.MustCompile("(?m)^- `--" + regexp.QuoteMeta(f) + "[ `]").MatchString(section) {
				t.Errorf("spec section for act %s has no Flags bullet for --%s", tc.sub, f)
			}
		}
		for f := range named {
			if !defined[f] {
				t.Errorf("spec synopsis for act %s names --%s, which the flag set does not define (%v)", tc.sub, f, flags)
			}
		}
	}
}

// TestDocClaim_Show_CommitMarkerFullAndNoFetchFlags asserts the three flag
// bullets added to the `act show` section.
func TestDocClaim_Show_CommitMarkerFullAndNoFetchFlags(t *testing.T) {
	dir := sp6Site(t)
	const marker = "… (truncated; see --json for full text)"
	lines := "sp6-line-1\nsp6-line-2\nsp6-line-3\nsp6-line-4\nsp6-line-5\nsp6-line-6\nsp6-line-7"
	id := createIssue(t, dir, "seven lines", "--description", lines)
	wide := createIssue(t, dir, "one wide line", "--description", strings.Repeat("w", 600))

	// --full: without it a description is cut at 5 lines, with the marker.
	human, _, code := runActIn(t, dir, "show", id)
	if code != 0 || !strings.Contains(human, "sp6-line-5") || strings.Contains(human, "sp6-line-6") || !strings.Contains(human, marker) {
		t.Errorf("act show: exit %d, want lines 1-5, no line 6, and the truncation marker\n%s", code, human)
	}
	full, _, code := runActIn(t, dir, "show", id, "--full")
	if code != 0 || !strings.Contains(full, "sp6-line-7") || strings.Contains(full, marker) {
		t.Errorf("act show --full: exit %d, want all seven lines and no marker\n%s", code, full)
	}
	asJSON, _, code := runActIn(t, dir, "show", id, "--json")
	if code != 0 || !strings.Contains(asJSON, "sp6-line-7") || strings.Contains(asJSON, "truncated; see --json") {
		t.Errorf("act show --json: exit %d, want the whole description\n%s", code, asJSON)
	}
	// ... or at 400 characters.
	human, _, _ = runActIn(t, dir, "show", wide)
	if !strings.Contains(human, strings.Repeat("w", 400)) || strings.Contains(human, strings.Repeat("w", 401)) || !strings.Contains(human, marker) {
		t.Errorf("act show of a 600-character line: want exactly 400 characters and the marker\n%s", human)
	}
	if full, _, _ = runActIn(t, dir, "show", wide, "--full"); !strings.Contains(full, strings.Repeat("w", 600)) {
		t.Errorf("act show --full of a 600-character line: want all 600\n%s", full)
	}
	// closed_reason takes the same cut.
	if out, stderr, code := runActIn(t, dir, "close", id, "--reason", strings.ReplaceAll(lines, "line", "why"), "--json"); code != 0 {
		t.Fatalf("act close: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	human, _, _ = runActIn(t, dir, "show", id)
	full, _, _ = runActIn(t, dir, "show", id, "--full")
	if strings.Contains(human, "sp6-why-6") || !strings.Contains(human, "sp6-why-5") || !strings.Contains(full, "sp6-why-7") {
		t.Errorf("closed_reason: want it cut at 5 lines without --full and whole with it\nplain:\n%s\n--full:\n%s", human, full)
	}

	// --commit-marker: one line, the trailer, exit 0.
	out, stderr, code := runActIn(t, dir, "show", id, "--commit-marker")
	mm := regexp.MustCompile(`^Act-Id: (act-[0-9a-f]+)\n$`).FindStringSubmatch(out)
	if code != 0 || mm == nil || !strings.HasPrefix(id, mm[1]) {
		t.Errorf("act show --commit-marker: exit %d, stdout %q; want exactly `Act-Id: <a prefix of %s>`\nstderr=%s", code, out, id, stderr)
	}

	// --no-fetch, and ACT_NO_FETCH=1: the read reports it skipped the fetch.
	out, _, code = runActIn(t, dir, "show", id, "--json")
	if code != 0 || sp6RefreshReason(t, out) == "no_fetch" {
		t.Fatalf("baseline act show --json: exit %d, refresh.reason %q; a plain read must not report no_fetch", code, sp6RefreshReason(t, out))
	}
	out, _, code = runActIn(t, dir, "show", id, "--no-fetch", "--json")
	if code != 0 || sp6RefreshReason(t, out) != "no_fetch" {
		t.Errorf("act show --no-fetch --json: exit %d, refresh.reason %q, want no_fetch\n%s", code, sp6RefreshReason(t, out), out)
	}
	t.Setenv("ACT_NO_FETCH", "1")
	out, _, code = runActIn(t, dir, "show", id, "--json")
	if code != 0 || sp6RefreshReason(t, out) != "no_fetch" {
		t.Errorf("ACT_NO_FETCH=1 act show --json: exit %d, refresh.reason %q, want no_fetch\n%s", code, sp6RefreshReason(t, out), out)
	}
	t.Setenv("ACT_NO_FETCH", "")

	// A tombstoned issue has no marker: exit 3, while plain show exits 0.
	if out, stderr, code := runActIn(t, dir, "delete", wide, "--reason", "sp6", "--json"); code != 0 {
		t.Fatalf("act delete: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	if _, _, code := runActIn(t, dir, "show", wide, "--json"); code != 0 {
		t.Errorf("act show of a tombstoned issue: exit %d, want 0", code)
	}
	if out, stderr, code := runActIn(t, dir, "show", wide, "--commit-marker"); code != 3 || strings.Contains(out, "Act-Id") {
		t.Errorf("act show --commit-marker of a tombstoned issue: exit %d, want 3 and no trailer\nstdout=%s\nstderr=%s", code, out, stderr)
	}
}

// TestDocClaim_Log_ScopeAndFilterFlags asserts the Flags and Behavior text
// added to the `act log` section: the id is optional, a bare `act log` is a
// usage error, and each flag does what its bullet says.
func TestDocClaim_Log_ScopeAndFilterFlags(t *testing.T) {
	dir := sp6Site(t)
	a := createIssue(t, dir, "alpha")
	b := createIssue(t, dir, "beta")
	if out, stderr, code := runActIn(t, dir, "close", a, "--reason", "done", "--json"); code != 0 {
		t.Fatalf("act close: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	eq := func(what string, got []string, want ...string) {
		t.Helper()
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: op types %v, want %v", what, got, want)
		}
	}

	// No id and no filter: usage error.
	pass5Run(t, dir, 2, "bad_flag", "log", "--json")

	// Scope: positional id, --by-issue, or every issue.
	eq("log <a>", sp6LogOps(t, dir, a), "create", "close")
	eq("log --by-issue <a>", sp6LogOps(t, dir, "--by-issue", a), "create", "close")
	eq("log --by-issue <b>", sp6LogOps(t, dir, "--by-issue", b), "create")
	eq("log --by-issue <prefix of b>", sp6LogOps(t, dir, "--by-issue", b[:len(b)-2]), "create")
	pass5Run(t, dir, 2, "bad_flag", "log", "--json", "--by-issue", b, a)
	eq("log --since 24h (every issue)", sp6LogOps(t, dir, "--since", "24h"), "create", "create", "close")

	// --type: one or several, across issues or inside a scope; unknown is 2.
	eq("log --type create", sp6LogOps(t, dir, "--type", "create"), "create", "create")
	eq("log --type create,close", sp6LogOps(t, dir, "--type", "create,close"), "create", "create", "close")
	eq("log <a> --type close", sp6LogOps(t, dir, a, "--type", "close"), "close")
	pass5Run(t, dir, 2, "bad_flag", "log", "--json", "--type", "sp6-no-such-type")

	// --since: Go durations and whole days; a mixed form is rejected; and it
	// filters — every op here is older than a millisecond.
	eq("log --since 7d", sp6LogOps(t, dir, "--since", "7d"), "create", "create", "close")
	pass5Run(t, dir, 2, "bad_flag", "log", "--json", "--since", "1d12h")
	time.Sleep(20 * time.Millisecond)
	eq("log --since 1ms", sp6LogOps(t, dir, "--since", "1ms"))
	eq("log <a> --since 1ms", sp6LogOps(t, dir, a, "--since", "1ms"))

	// --summary: human mode is one line per op with or without it; the flag
	// adds a note of what the op did and, on a one-issue log, drops the
	// issue tag. It counts as a filter for the bare-log rule, and --json
	// is the same with or without it.
	plain, _, code := runActIn(t, dir, "log", a)
	plainLines := linesOf(plain)
	if code != 0 || len(plainLines) != 3 || !strings.Contains(plainLines[0], " create ") || !strings.Contains(plainLines[0], "[issue=") {
		t.Fatalf("act log <a>: exit %d, want one line per op carrying [issue=...] and a count line\n%s", code, plain)
	}
	if strings.Contains(plain, "alpha") || strings.Contains(plain, "done") {
		t.Errorf("act log <a> without --summary already carries the title or the close reason\n%s", plain)
	}
	summary, _, code := runActIn(t, dir, "log", a, "--summary")
	sumLines := linesOf(summary)
	if code != 0 || len(sumLines) != 3 || !strings.Contains(sumLines[0], " create ") || !strings.HasSuffix(sumLines[0], "alpha") ||
		!strings.Contains(sumLines[1], " close ") || !strings.HasSuffix(sumLines[1], "done") || strings.Contains(summary, "[issue=") {
		t.Errorf("act log <a> --summary: exit %d, want a create line ending in the title, a close line ending in the reason, no issue tag, and a count line\n%s", code, summary)
	}
	if out, _, code := runActIn(t, dir, "log", "--summary"); code != 0 || len(linesOf(out)) != 4 || !strings.Contains(out, "alpha") || !strings.Contains(out, "[issue=") {
		t.Errorf("act log --summary (every issue): exit %d, want three op lines with their notes and issue tags, and a count line\n%s", code, out)
	}
	eq("log <a> --summary --json", sp6LogOps(t, dir, a, "--summary"), "create", "close")

	// --no-fetch, and ACT_NO_FETCH=1.
	out, _, _ := runActIn(t, dir, "log", a, "--json")
	if sp6RefreshReason(t, out) == "no_fetch" {
		t.Fatalf("baseline act log --json reports no_fetch")
	}
	out, _, code = runActIn(t, dir, "log", a, "--no-fetch", "--json")
	if code != 0 || sp6RefreshReason(t, out) != "no_fetch" {
		t.Errorf("act log --no-fetch --json: exit %d, refresh.reason %q, want no_fetch\n%s", code, sp6RefreshReason(t, out), out)
	}
	t.Setenv("ACT_NO_FETCH", "1")
	out, _, code = runActIn(t, dir, "log", a, "--json")
	if code != 0 || sp6RefreshReason(t, out) != "no_fetch" {
		t.Errorf("ACT_NO_FETCH=1 act log --json: exit %d, refresh.reason %q, want no_fetch\n%s", code, sp6RefreshReason(t, out), out)
	}
}

// TestDocClaim_Spec_ExitCodeFourHasThreeUses asserts the universal exit-code
// list's line for 4 for the two uses a test can provoke cheaply — `act
// version --check-repo` on skew and `act doctor` on an unreachable origin —
// and that the same tracker makes no other command exit 4. The third use,
// the clone timeout, is asserted by
// TestDocClaim_BootstrapFromRemote_TimeoutEnvelope.
func TestDocClaim_Spec_ExitCodeFourHasThreeUses(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "first")

	// Skew: version --check-repo exits 4 (asserted inside the helper);
	// reads, a write and doctor on the same tracker do not.
	restore := pass5NewerWriter(t, dir, id)
	for _, args := range [][]string{
		{"list", "--json"}, {"show", id, "--json"}, {"log", id, "--json"}, {"ready", "--json"},
		{"update", id, "--priority", "2", "--json"}, {"doctor", "--json"}, {"version", "--json"},
	} {
		if out, stderr, code := runActIn(t, dir, args...); code != 0 {
			t.Errorf("act %s beside a newer-writer op: exit %d, want 0\nstdout=%s\nstderr=%s", strings.Join(args, " "), code, out, stderr)
		}
	}
	restore()

	// An unreachable origin: doctor exits 4 with the remote-reachable
	// finding; nothing else does, and --no-fetch skips the probe.
	nested := filepath.Join(dir, ".act")
	sp6Git(t, "-C", nested, "config", "remote.origin.url", "file://"+filepath.Join(t.TempDir(), "sp6-missing.git"))
	res, code := sp6RunDoctor(t, dir)
	if code != 4 || !sp6Has(res, "remote-reachable", "error") {
		t.Errorf("act doctor with an unreachable origin: exit %d, findings %v; want 4 and a remote-reachable error", code, sp6Checks(res))
	}
	if res, code := sp6RunDoctor(t, dir, "--no-fetch"); code != 0 || !sp6Has(res, "remote-reachable", "warn") {
		t.Errorf("act doctor --no-fetch with an unreachable origin: exit %d, findings %v; want 0 and a remote-reachable warning", code, sp6Checks(res))
	}
	pass5Run(t, dir, 0, "", "version", "--check-repo", "--json")
	pass5Run(t, dir, 0, "", "list", "--no-fetch", "--json")
}

// TestDocClaim_Doctor_CheckFlagTakesOneName asserts the `--check NAME`
// bullet: a single value (the last one wins when the flag is repeated), the
// accepted names are the rows of the spec's check table, and anything else —
// the five phase2 case names included — exits 2.
func TestDocClaim_Doctor_CheckFlagTakesOneName(t *testing.T) {
	dir := sp6Site(t)
	createIssue(t, dir, "first")
	lock := filepath.Join(dir, ".act", ".git", "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatalf("plant lock: %v", err)
	}
	// Path proof: the lock is a finding when its check is selected.
	if res, code := sp6RunDoctor(t, dir, "--check", "stale-git-lock"); code != 1 || !sp6Has(res, "stale-git-lock", "error") {
		t.Fatalf("seed: --check stale-git-lock: exit %d, findings %v; want 1 and the lock finding", code, sp6Checks(res))
	}
	if res, code := sp6RunDoctor(t, dir, "--check", "cycle", "--check", "stale-git-lock"); code != 1 || !sp6Has(res, "stale-git-lock", "error") {
		t.Errorf("--check cycle --check stale-git-lock: exit %d, findings %v; want the last name to run (1, the lock finding)", code, sp6Checks(res))
	}
	if res, code := sp6RunDoctor(t, dir, "--check", "stale-git-lock", "--check", "cycle"); code != 0 || len(res.Findings) != 0 {
		t.Errorf("--check stale-git-lock --check cycle: exit %d, findings %v; want only cycle to run (0, none)", code, sp6Checks(res))
	}
	if err := os.Remove(lock); err != nil {
		t.Fatalf("remove lock: %v", err)
	}

	// Every row of the spec's table is a name --check accepts.
	spec := sp6Spec(t)
	i := strings.Index(spec, "**Checks and `--fix` behavior:**")
	if i < 0 {
		t.Fatalf("docs/spec.md has no doctor check table")
	}
	table := spec[i:]
	table = table[:strings.Index(table, "\n#### ")]
	rows := regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\|").FindAllStringSubmatch(table, -1)
	if len(rows) < 10 {
		t.Fatalf("parsed %d rows from the spec's doctor check table", len(rows))
	}
	for _, m := range rows {
		if out, stderr, code := runActIn(t, dir, "doctor", "--json", "--no-fetch", "--check", m[1]); code == 2 {
			t.Errorf("act doctor --check %s: exit 2, but the spec's table has a row for it\nstdout=%s\nstderr=%s", m[1], out, stderr)
		}
	}
	for _, name := range []string{
		"sp6-no-such-check",
		"remote-attached-orchestrator", "worker-without-origin", "unpushed-commits", "remote-reachable", "upstream-drift",
	} {
		pass5Run(t, dir, 2, "bad_flag", "doctor", "--json", "--check", name)
	}
}

// TestDocClaim_Spec_DoctorRowsGitignorePhase2StatusLock asserts the four rows
// added to the spec's doctor check table, on what each row says a test can
// provoke: what the check reports, that --fix leaves the fault in place, and
// the exit code.
func TestDocClaim_Spec_DoctorRowsGitignorePhase2StatusLock(t *testing.T) {
	t.Run("stale-git-lock", func(t *testing.T) {
		dir := sp6Site(t)
		createIssue(t, dir, "first")
		for _, name := range []string{"index.lock", "HEAD.lock"} {
			if err := os.WriteFile(filepath.Join(dir, ".act", ".git", name), nil, 0o644); err != nil {
				t.Fatalf("plant %s: %v", name, err)
			}
		}
		res, code := sp6RunDoctor(t, dir, "--check", "stale-git-lock", "--fix")
		if code != 1 || len(res.Findings) != 2 || !sp6Has(res, "stale-git-lock", "error") {
			t.Errorf("--check stale-git-lock --fix with both locks: exit %d, findings %v; want 1 and one error per lock", code, sp6Checks(res))
		}
		for _, name := range []string{"index.lock", "HEAD.lock"} {
			if _, err := os.Stat(filepath.Join(dir, ".act", ".git", name)); err != nil {
				t.Errorf("--fix removed %s (or it is unreadable): %v", name, err)
			}
		}
		_, stderr, _ := runActIn(t, dir, "doctor", "--check", "stale-git-lock")
		if !strings.Contains(stderr, ".act/.git/index.lock") || !strings.Contains(stderr, ".act/.git/HEAD.lock") {
			t.Errorf("human mode: stderr does not carry both findings' messages\n%s", stderr)
		}
	})

	t.Run("gitignore-effective", func(t *testing.T) {
		dir := sp6Site(t)
		createIssue(t, dir, "first")
		if res, code := sp6RunDoctor(t, dir, "--check", "gitignore-effective"); code != 0 || len(res.Findings) != 0 {
			t.Fatalf("seed: a fresh tracker: exit %d, findings %v; want 0 and none", code, sp6Checks(res))
		}
		ignore := filepath.Join(dir, ".gitignore")
		if err := os.WriteFile(ignore, []byte("# sp6: nothing ignored\n"), 0o644); err != nil {
			t.Fatalf("rewrite .gitignore: %v", err)
		}
		res, code := sp6RunDoctor(t, dir, "--check", "gitignore-effective", "--fix")
		if code != 1 || len(res.Findings) != 1 || !sp6Has(res, "gitignore-effective", "error") {
			t.Fatalf("--check gitignore-effective --fix with .act/ not ignored: exit %d, findings %v; want 1 and one error", code, sp6Checks(res))
		}
		for _, want := range []string{".gitignore", "git rm -r --cached .act/"} {
			if !strings.Contains(res.Findings[0].Message, want) {
				t.Errorf("finding message lacks %q: %s", want, res.Findings[0].Message)
			}
		}
		if body, _ := os.ReadFile(ignore); string(body) != "# sp6: nothing ignored\n" {
			t.Errorf("--fix edited .gitignore: %q", body)
		}
	})

	t.Run("status-vs-oplog", func(t *testing.T) {
		dir := sp6Site(t)
		id := createIssue(t, dir, "first")
		if out, stderr, code := runActIn(t, dir, "close", id, "--reason", "done", "--json"); code != 0 {
			t.Fatalf("act close: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
		}
		if res, code := sp6RunDoctor(t, dir, "--check", "status-vs-oplog"); code != 0 || len(res.Findings) != 0 {
			t.Fatalf("seed: a committed close: exit %d, findings %v; want 0 and none", code, sp6Checks(res))
		}
		// Un-commit the close op, leaving its file on disk.
		nested := filepath.Join(dir, ".act")
		sp6Git(t, "-C", nested, "reset", "-q", "--soft", "HEAD~1")
		res, code := sp6RunDoctor(t, dir, "--check", "status-vs-oplog", "--fix")
		if code != 0 || len(res.Findings) != 1 || !sp6Has(res, "status-vs-oplog", "warn") || !strings.Contains(res.Findings[0].Message, "not durable yet") {
			t.Fatalf("an uncommitted close op, --fix: exit %d, findings %+v; want 0 and one warning saying the status is not durable yet", code, res.Findings)
		}
		if res, code := sp6RunDoctor(t, dir, "--check", "status-vs-oplog", "--strict"); code != 1 || !sp6Has(res, "status-vs-oplog", "error") {
			t.Errorf("the same under --strict: exit %d, findings %v; want 1", code, sp6Checks(res))
		}
		_, stderr, _ := runActIn(t, dir, "doctor", "--check", "status-vs-oplog")
		if !strings.Contains(stderr, "not durable yet") {
			t.Errorf("human mode: stderr does not carry the finding's message\n%s", stderr)
		}
		// Retract a committed op instead: commit the close again, then
		// delete its file. That is an error, and exit 1.
		sp6Git(t, "-C", nested, "-c", "user.email=sp6@example.com", "-c", "user.name=sp6", "commit", "-q", "--no-verify", "-m", "sp6: recommit the close")
		var closeOps []string
		_ = filepath.Walk(filepath.Join(nested, "ops", id), func(path string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.HasSuffix(path, "-close.json") {
				closeOps = append(closeOps, path)
			}
			return nil
		})
		if len(closeOps) != 1 {
			t.Fatalf("want one close op file, found %v", closeOps)
		}
		if err := os.Remove(closeOps[0]); err != nil {
			t.Fatalf("remove the close op: %v", err)
		}
		res, code = sp6RunDoctor(t, dir, "--check", "status-vs-oplog")
		if code != 1 || !sp6Has(res, "status-vs-oplog", "error") {
			t.Errorf("a committed close op missing from ops/: exit %d, findings %+v; want 1 and an error", code, res.Findings)
		}
	})

	t.Run("phase2-reconciliation", func(t *testing.T) {
		dir := sp6Site(t)
		createIssue(t, dir, "first")
		nested := filepath.Join(dir, ".act")
		// A worker with no origin: case (c'), an error, exit 1 — reported
		// under the case's own name, with and without --fix.
		sp6Git(t, "-C", nested, "config", "act.role", "worker")
		for _, extra := range [][]string{nil, {"--fix"}} {
			res, code := sp6RunDoctor(t, dir, append([]string{"--check", "phase2-reconciliation"}, extra...)...)
			if code != 1 || len(res.Findings) != 1 || !sp6Has(res, "worker-without-origin", "error") {
				t.Errorf("--check phase2-reconciliation %v on a worker with no origin: exit %d, findings %v; want 1 and worker-without-origin", extra, code, sp6Checks(res))
			}
			for _, f := range res.Findings {
				if f.Check == "phase2-reconciliation" {
					t.Errorf("a finding carries the umbrella name: %+v", f)
				}
			}
		}
		// The same fault is invisible to a run of another check.
		if res, code := sp6RunDoctor(t, dir, "--check", "cycle"); code != 0 || len(res.Findings) != 0 {
			t.Errorf("--check cycle on the same tracker: exit %d, findings %v; want 0 and none", code, sp6Checks(res))
		}
		// An unreachable origin: exit 4, and a warning under --no-fetch.
		sp6Git(t, "-C", nested, "config", "remote.origin.url", "file://"+filepath.Join(t.TempDir(), "sp6-missing.git"))
		res, code := sp6RunDoctor(t, dir, "--check", "phase2-reconciliation")
		if code != 4 || !sp6Has(res, "remote-reachable", "error") || res.RemoteStatus["remote_reachable"] != false {
			t.Errorf("unreachable origin: exit %d, findings %v, remote_status %v; want 4, a remote-reachable error, remote_reachable false", code, sp6Checks(res), res.RemoteStatus)
		}
		res, code = sp6RunDoctor(t, dir, "--check", "phase2-reconciliation", "--no-fetch")
		if code != 0 || !sp6Has(res, "remote-reachable", "warn") {
			t.Errorf("unreachable origin, --no-fetch: exit %d, findings %v; want 0 and a remote-reachable warning", code, sp6Checks(res))
		}
	})
}

// TestDocClaim_Doctor_RemoteStatusWhenPhase2DidNotRun asserts the
// remote_status intro: on a run that never reaches phase2-reconciliation,
// remote_reachable is true and the other four fields are zero — on a tracker
// whose phase2 run reports the opposite, so the values cannot be a
// coincidence of a healthy fixture.
func TestDocClaim_Doctor_RemoteStatusWhenPhase2DidNotRun(t *testing.T) {
	dir := sp6Site(t)
	createIssue(t, dir, "first")
	nested := filepath.Join(dir, ".act")
	sp6Git(t, "-C", nested, "config", "remote.origin.url", "file://"+filepath.Join(t.TempDir(), "sp6-missing.git"))
	entry := `{"timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","duration_ms":9000}` + "\n"
	if err := os.WriteFile(filepath.Join(nested, ".slow-writes"), []byte(entry), 0o644); err != nil {
		t.Fatalf("write .slow-writes: %v", err)
	}

	// Path proof: when phase2 runs here, the block is not the default one.
	res, code := sp6RunDoctor(t, dir, "--check", "phase2-reconciliation")
	if code != 4 || res.RemoteStatus["remote_reachable"] != false || res.RemoteStatus["fetch_failure_reason"] == "" {
		t.Fatalf("seed: phase2 on an unreachable origin: exit %d, remote_status %v; want 4, remote_reachable false, a failure reason", code, res.RemoteStatus)
	}
	if n, _ := res.RemoteStatus["slow_writes_last_hour"].(float64); n != 1 {
		t.Fatalf("seed: slow_writes_last_hour = %v, want 1 (remote_status %v)", res.RemoteStatus["slow_writes_last_hour"], res.RemoteStatus)
	}

	want := map[string]any{
		"remote_reachable":       true,
		"local_unpushed_count":   float64(0),
		"upstream_drift_commits": float64(0),
		"slow_writes_last_hour":  float64(0),
		"fetch_failure_reason":   "",
	}
	for _, check := range []string{"cycle", "time-travel", "fold-error"} {
		res, code := sp6RunDoctor(t, dir, "--check", check)
		if code != 0 {
			t.Errorf("--check %s: exit %d, want 0 (findings %v)", check, code, sp6Checks(res))
		}
		if len(res.RemoteStatus) != len(want) {
			t.Errorf("--check %s: remote_status has %d fields, want the five: %v", check, len(res.RemoteStatus), res.RemoteStatus)
		}
		for k, v := range want {
			if got, ok := res.RemoteStatus[k]; !ok || got != v {
				t.Errorf("--check %s: remote_status.%s = %v (present %v), want %v", check, k, got, ok, v)
			}
		}
	}
}

// TestDocClaim_HelpErrors_NoRowForCodesNothingEmits asserts that `act help
// errors` and the spec's error table have no row for op_invalid, hlc_drift
// or index_corrupt, and that this is still the truth: nothing outside the
// constant block in internal/cli/errors.go names any of the three. If a
// command starts emitting one, this fails and the rows come back with it.
func TestDocClaim_HelpErrors_NoRowForCodesNothingEmits(t *testing.T) {
	codes := map[string]string{
		"op_invalid":    "ErrOpInvalid",
		"hlc_drift":     "ErrHLCDrift",
		"index_corrupt": "ErrIndexCorrupt",
	}
	help, stderr, code := runAct(t, "help", "errors")
	help += stderr
	if code != 0 || !strings.Contains(help, "version_skew") {
		t.Fatalf("act help errors: exit %d, %d bytes", code, len(help))
	}
	table := sp6Section(t, sp6Spec(t), "### 1. Error handling")
	if !strings.Contains(table, "| `version_skew`") {
		t.Fatalf("did not find the spec's error table")
	}
	for slug := range codes {
		if regexp.MustCompile(`\b` + slug + `\b`).MatchString(help) {
			t.Errorf("act help errors still lists %s", slug)
		}
		if strings.Contains(table, "`"+slug+"`") {
			t.Errorf("the spec's error table still has a row for %s", slug)
		}
	}

	// The premise, from the source: no emitter.
	root := sp6RepoRoot(t)
	scanned := 0
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.Walk(filepath.Join(root, top), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			scanned++
			rel, _ := filepath.Rel(root, path)
			for slug, konst := range codes {
				for n, line := range strings.Split(string(body), "\n") {
					declares := filepath.ToSlash(rel) == "internal/cli/errors.go" && strings.Contains(line, konst) && strings.Contains(line, `= "`+slug+`"`)
					if declares {
						continue
					}
					if strings.Contains(line, konst) || strings.Contains(line, `"`+slug+`"`) {
						t.Errorf("%s:%d uses %s — if a command emits it now, restore its row in `act help errors` and the spec's error table: %s",
							rel, n+1, slug, strings.TrimSpace(line))
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d Go files under cmd/ and internal/", scanned)
	}
}

// TestDocClaim_HelpErrors_NotInGitAlsoFromMCPReadsWithNoActDir asserts the
// not_in_git row of `act help errors`: besides "outside a git repo", it is
// what the act_show, act_log and act_search MCP tools answer in a git repo
// with no .act/, where the CLI commands print the no-state line and exit 0.
func TestDocClaim_HelpErrors_NotInGitAlsoFromMCPReadsWithNoActDir(t *testing.T) {
	help, stderr, code := runAct(t, "help", "errors")
	flat := strings.Join(strings.Fields(help+stderr), " ")
	want := "not_in_git act run outside a git repo; also what the act_show, act_log and act_search MCP tools answer when the repo has no .act/ (the CLI commands exit 0 there)"
	if code != 0 || !strings.Contains(flat, want) {
		t.Fatalf("act help errors (exit %d) lacks the row %q", code, want)
	}

	brokencoIsolateEnv(t)
	t.Setenv("ACT_MACHINE", "")

	// Outside a git repo: the CLI commands answer not_in_git, exit 3.
	bare := t.TempDir()
	for _, args := range [][]string{
		{"show", "--json", pass5NoSuchID}, {"log", "--json", pass5NoSuchID}, {"search", "--json", "sp6"},
	} {
		pass5Run(t, bare, 3, "not_in_git", args...)
	}

	// A git repo with no .act/: the CLI commands exit 0 with the no-state line.
	host := brokencoHostRepo(t)
	for _, args := range [][]string{
		{"show", "--json", pass5NoSuchID}, {"log", "--json", pass5NoSuchID}, {"search", "--json", "sp6"},
	} {
		_, stderr, code := runActIn(t, host, args...)
		if code != 0 || !strings.Contains(stderr, "no act state in this repo") {
			t.Errorf("act %s in a git repo with no .act/: exit %d, want 0 and the no-state line\nstderr=%s", strings.Join(args, " "), code, stderr)
		}
	}

	// The same repo through MCP: the three read tools answer not_in_git.
	for _, call := range []string{
		`{"name":"act_show","arguments":{"id":"` + pass5NoSuchID + `"}}`,
		`{"name":"act_log","arguments":{"id":"` + pass5NoSuchID + `"}}`,
		`{"name":"act_search","arguments":{"query":"sp6"}}`,
	} {
		out, stderr, code := pass5MCP(t, host, nil, pass5MCPInit, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":`+call+`}`)
		if code != 0 || !strings.Contains(out, `"isError":true`) || !strings.Contains(out, "not_in_git") {
			t.Errorf("MCP %s in a git repo with no .act/: exit %d, want an error result naming not_in_git\nstdout=%s\nstderr=%s", call, code, out, stderr)
		}
	}
	// Control: a tool that does not take that path answers something else.
	out, _, _ := pass5MCP(t, host, nil, pass5MCPInit, pass5MCPList)
	if strings.Contains(out, "not_in_git") {
		t.Errorf("MCP act_list in the same repo also answers not_in_git; the row names only show, log and search\n%s", out)
	}
}

// TestDocClaim_Spec_VersionCheckRepoBulletSaysWhereItLooks asserts the
// `--check-repo` flag bullet: it reads the tracker of the repo the working
// directory is in, from a subdirectory as well as the root, and exits 3
// outside a git working tree or in a repo with no .act/. The skew is planted
// so the subdirectory run is shown to have read the ops, not skipped them.
func TestDocClaim_Spec_VersionCheckRepoBulletSaysWhereItLooks(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "first")
	sub := filepath.Join(dir, "sp6-a", "sp6-b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pass5Run(t, dir, 0, "", "version", "--check-repo", "--json")
	pass5Run(t, sub, 0, "", "version", "--check-repo", "--json")
	restore := pass5NewerWriter(t, dir, id) // asserts exit 4 from the root
	pass5Run(t, sub, 4, "version_skew", "version", "--check-repo", "--json")
	restore()
	pass5Run(t, t.TempDir(), 3, "not_in_git", "version", "--check-repo", "--json")
	pass5Run(t, brokencoHostRepo(t), 3, "no_repo", "version", "--check-repo", "--json")
}
