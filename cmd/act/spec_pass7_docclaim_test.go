package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Doc-claim cover for the seventh stale pass over docs/spec.md (act-870488),
// asserted against the real binary. It extends the sixth pass's two-command
// checks (spec_pass6_docclaim_test.go) to every command and every row.
//
// What was stale: the `act doctor` synopsis left out --strict and --no-fetch;
// its JSON example showed a checks/summary shape the binary has never
// emitted; seven rows of the error table carried the wrong exit code and one
// row named a code nothing emits; the list/ready/search synopses left out
// --no-fetch, and most write-command synopses hid their flags behind "plus
// universal flags", a set no command actually accepts whole; and `act help
// errors` listed `ambiguous_id`, which nothing emits.

// sp7DefinedFlags returns the flags a command defines, read from the usage
// text its own flag set prints for -h. sub is the command's words ("dep",
// "add"). Run outside any git repo so the no-state guard stays out of the way.
func sp7DefinedFlags(t *testing.T, sub ...string) []string {
	t.Helper()
	stdout, stderr, _ := runActIn(t, t.TempDir(), append(append([]string{}, sub...), "-h")...)
	usage := stdout + stderr
	if !strings.Contains(usage, "Usage of "+strings.Join(sub, " ")+":") {
		t.Fatalf("act %s -h did not print the flag set's usage:\n%s", strings.Join(sub, " "), usage)
	}
	var flags []string
	for _, m := range regexp.MustCompile(`(?m)^  -([a-z][a-z0-9-]*)`).FindAllStringSubmatch(usage, -1) {
		flags = append(flags, m[1])
	}
	sort.Strings(flags)
	return flags
}

// sp7Synopsis is one "**Synopsis:**" line of the spec.
type sp7Synopsis struct {
	line    string   // the text between the backticks
	sub     []string // the command's words, without "act"
	flags   []string // every -x / --xyz the line names
	section string   // the spec text from this line to the next "### " heading
}

var (
	sp7SynopsisRe     = regexp.MustCompile("(?m)^\\*\\*Synopsis:\\*\\* `(act [^`]*)`")
	sp7SynopsisFlagRe = regexp.MustCompile(`(?:^|[\s\[|])-{1,2}([a-z][a-z0-9-]*)`)
	sp7CommandWordRe  = regexp.MustCompile(`^[a-z][a-z-]*$`)
)

// sp7Synopses returns every Synopsis line in the spec, in document order.
func sp7Synopses(t *testing.T) []sp7Synopsis {
	t.Helper()
	spec := sp6Spec(t)
	var out []sp7Synopsis
	for _, loc := range sp7SynopsisRe.FindAllStringSubmatchIndex(spec, -1) {
		s := sp7Synopsis{line: spec[loc[2]:loc[3]]}
		for _, w := range strings.Fields(s.line)[1:] {
			if !sp7CommandWordRe.MatchString(w) {
				break
			}
			s.sub = append(s.sub, w)
		}
		seen := map[string]bool{}
		for _, m := range sp7SynopsisFlagRe.FindAllStringSubmatch(s.line, -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				s.flags = append(s.flags, m[1])
			}
		}
		s.section = spec[loc[0]:]
		if j := strings.Index(s.section, "\n### "); j >= 0 {
			s.section = s.section[:j]
		}
		out = append(out, s)
	}
	return out
}

// sp7HasBullet reports whether text holds a flag bullet for flag: a line
// starting "- `--flag", or "- `-p, --priority" for a flag with a shorthand.
func sp7HasBullet(text, flag string) bool {
	q := regexp.QuoteMeta(flag)
	return regexp.MustCompile("(?m)^- `(?:-[a-z], )?--?"+q+"[ `,]").MatchString(text) ||
		regexp.MustCompile("(?m)^- `-"+q+", --").MatchString(text)
}

// sp7NoBulletYet lists the flags a synopsis names that have no flag bullet
// in the spec — neither in the command's own section nor under "Universal
// flags". It is a debt list, checked in both directions: a flag missing a
// bullet that is not here fails, and so does an entry here whose bullet has
// since been written. Do not add to it; write the bullet (and its asserting
// test) instead.
//
// Empty since the eighth pass (act-0c1c31) wrote the last 22 bullets; their
// behaviour is asserted by TestDocClaim_Spec_FlagBullets* in
// spec_pass8_docclaim_test.go.
var sp7NoBulletYet = map[string][]string{}

// TestDocClaim_Spec_EverySynopsisNamesEveryFlag asserts, for every
// "**Synopsis:**" line in docs/spec.md, that it names every flag the
// command's flag set defines and no flag it lacks. The expected set is read
// from `act <cmd> -h`, so a flag added to any command without a synopsis
// entry turns this red. Each named flag must also have a flag bullet, in the
// command's section or under "Universal flags", unless sp7NoBulletYet
// records that it has none yet.
func TestDocClaim_Spec_EverySynopsisNamesEveryFlag(t *testing.T) {
	synopses := sp7Synopses(t)
	if len(synopses) < 16 {
		t.Fatalf("found %d Synopsis lines in docs/spec.md, want at least the 16 command sections", len(synopses))
	}
	universal := sp6Section(t, sp6Spec(t), "### Universal flags (write commands)")
	seenCmd := map[string]bool{}
	for _, s := range synopses {
		cmd := strings.Join(s.sub, " ")
		if cmd == "" || seenCmd[cmd] {
			t.Errorf("Synopsis line %q: no command words, or a second synopsis for %q", s.line, cmd)
			continue
		}
		seenCmd[cmd] = true
		flags := sp7DefinedFlags(t, s.sub...)
		if len(flags) < 2 {
			t.Fatalf("act %s -h: parsed flags %v; every command with a synopsis defines at least two", cmd, flags)
		}
		defined := map[string]bool{}
		for _, f := range flags {
			defined[f] = true
		}
		named := map[string]bool{}
		for _, f := range s.flags {
			named[f] = true
			if !defined[f] {
				t.Errorf("spec synopsis for act %s names --%s, which the flag set does not define (%v)\n  synopsis: %s", cmd, f, flags, s.line)
			}
		}
		noBullet := map[string]bool{}
		for _, f := range sp7NoBulletYet[cmd] {
			noBullet[f] = true
			if !defined[f] {
				t.Errorf("sp7NoBulletYet[%q] lists --%s, which act %s does not define", cmd, f, cmd)
			}
		}
		for _, f := range flags {
			if !named[f] {
				t.Errorf("spec synopsis for act %s omits --%s, which the flag set defines (%v)\n  synopsis: %s", cmd, f, flags, s.line)
			}
			has := sp7HasBullet(s.section, f) || sp7HasBullet(universal, f)
			switch {
			case !has && !noBullet[f]:
				t.Errorf("spec has no flag bullet for act %s --%s (looked in its section and under Universal flags)", cmd, f)
			case has && noBullet[f]:
				t.Errorf("act %s --%s has a flag bullet now; remove it from sp7NoBulletYet", cmd, f)
			}
		}
	}
	for cmd := range sp7NoBulletYet {
		if !seenCmd[cmd] {
			t.Errorf("sp7NoBulletYet has an entry for %q, which has no Synopsis line", cmd)
		}
	}
	// The commands the ticket named must be among those checked.
	for _, cmd := range []string{"doctor", "list", "ready", "search", "show", "log", "create", "update", "close", "dep add"} {
		if !seenCmd[cmd] {
			t.Errorf("no Synopsis line was checked for act %s", cmd)
		}
	}
}

// TestDocClaim_Spec_WriteFlagsAreNotUniversal asserts the "Universal flags"
// intro: the six op-writing commands define --no-commit, --push, --isolated,
// --offline and --branch; only update, reopen and delete define --verify
// (passing it to create, close or dep add is exit 2); init defines none of
// them. And the --branch and --wait-timeout bullets.
func TestDocClaim_Spec_WriteFlagsAreNotUniversal(t *testing.T) {
	has := func(flags []string, f string) bool {
		for _, x := range flags {
			if x == f {
				return true
			}
		}
		return false
	}
	verify := map[string]bool{"update": true, "reopen": true, "delete": true}
	for _, sub := range [][]string{{"create"}, {"update"}, {"close"}, {"dep", "add"}, {"reopen"}, {"delete"}} {
		cmd := strings.Join(sub, " ")
		flags := sp7DefinedFlags(t, sub...)
		for _, f := range []string{"no-commit", "push", "isolated", "offline", "branch"} {
			if !has(flags, f) {
				t.Errorf("act %s does not define --%s (%v)", cmd, f, flags)
			}
		}
		if has(flags, "verify") != verify[cmd] {
			t.Errorf("act %s defines --verify: %v, the spec says %v", cmd, has(flags, "verify"), verify[cmd])
		}
	}
	initFlags := sp7DefinedFlags(t, "init")
	for _, f := range []string{"no-commit", "push", "isolated", "offline", "branch", "verify"} {
		if has(initFlags, f) {
			t.Errorf("act init defines --%s; the spec says it takes none of the write flags", f)
		}
	}

	dir := sp6Site(t)
	id := createIssue(t, dir, "first")
	other := createIssue(t, dir, "second")
	pass5Run(t, dir, 2, "", "create", "--json", "--verify", "sp7")
	pass5Run(t, dir, 2, "", "close", "--json", "--verify", id)
	pass5Run(t, dir, 2, "", "dep", "add", "--json", "--verify", id, other)

	// --wait-timeout is a duration, default 30s; a bare number is exit 2.
	usage, stderr, _ := runActIn(t, t.TempDir(), "update", "-h")
	if !regexp.MustCompile(`(?s)-wait-timeout duration\n[^\n]*\(default 30s\)`).MatchString(usage + stderr) {
		t.Errorf("act update -h does not show --wait-timeout as a duration defaulting to 30s:\n%s", usage+stderr)
	}
	pass5Run(t, dir, 2, "", "update", "--json", "--claim", "--wait", "--wait-timeout", "60", id)
	pass5Run(t, dir, 0, "", "update", "--json", "--claim", "--wait", "--wait-timeout", "90s", id)

	// --branch NAME: the op is committed on that branch of the nested repo,
	// created if missing and left checked out.
	nested := filepath.Join(dir, ".act")
	head := func() string {
		out, err := sp7GitOut("-C", nested, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil {
			t.Fatalf("git rev-parse: %v", err)
		}
		return out
	}
	start := head()
	if start == "sp7-branch" {
		t.Fatalf("fixture already on sp7-branch")
	}
	for i, args := range [][]string{
		{"create", "--json", "--branch", "sp7-branch-0", "third"},
		{"update", "--json", "--branch", "sp7-branch-1", "--priority", "1", id},
		{"dep", "add", "--json", "--branch", "sp7-branch-2", id, other},
		{"close", "--json", "--branch", "sp7-branch-3", "--reason", "sp7", other},
	} {
		branch := "sp7-branch-" + strconv.Itoa(i)
		before, _ := sp7GitOut("-C", nested, "rev-list", "--count", "--all")
		if out, stderr, code := runActIn(t, dir, args...); code != 0 {
			t.Fatalf("act %s: exit %d\nstdout=%s\nstderr=%s", strings.Join(args, " "), code, out, stderr)
		}
		after, _ := sp7GitOut("-C", nested, "rev-list", "--count", "--all")
		onBranch, err := sp7GitOut("-C", nested, "log", "-1", "--format=%s", branch)
		if err != nil || head() != branch || before == after || !strings.HasPrefix(onBranch, "act-op:") {
			t.Errorf("act %s: nested HEAD is %q, commits %s -> %s, tip of %s is %q (%v); want the op committed on %s and that branch checked out",
				strings.Join(args, " "), head(), before, after, branch, onBranch, err, branch)
		}
		if tip, _ := sp7GitOut("-C", nested, "log", "-1", "--format=%s", start); tip == onBranch {
			t.Errorf("act %s: the op commit %q is also the tip of %s", strings.Join(args, " "), onBranch, start)
		}
	}
}

// sp7GitOut runs git and returns its trimmed stdout.
func sp7GitOut(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	return strings.TrimSpace(string(out)), err
}

// TestDocClaim_Spec_NoFetchOnListReadySearch asserts the --no-fetch bullet
// added to the list, ready and search sections: with the flag, or with
// ACT_NO_FETCH=1, the --json result reports refresh.reason "no_fetch"; a
// plain read does not.
func TestDocClaim_Spec_NoFetchOnListReadySearch(t *testing.T) {
	dir := sp6Site(t)
	createIssue(t, dir, "sp7 needle")
	for _, base := range [][]string{{"list"}, {"ready"}, {"search", "needle"}} {
		name := "act " + strings.Join(base, " ")
		out, stderr, code := runActIn(t, dir, append(append([]string{}, base...), "--json")...)
		if code != 0 || sp6RefreshReason(t, out) == "no_fetch" || sp6RefreshReason(t, out) == "" {
			t.Fatalf("baseline %s --json: exit %d, refresh.reason %q; want a reason other than no_fetch\nstdout=%s\nstderr=%s", name, code, sp6RefreshReason(t, out), out, stderr)
		}
		out, _, code = runActIn(t, dir, append(append([]string{}, base...), "--no-fetch", "--json")...)
		if code != 0 || sp6RefreshReason(t, out) != "no_fetch" {
			t.Errorf("%s --no-fetch --json: exit %d, refresh.reason %q, want no_fetch\n%s", name, code, sp6RefreshReason(t, out), out)
		}
		t.Setenv("ACT_NO_FETCH", "1")
		out, _, code = runActIn(t, dir, append(append([]string{}, base...), "--json")...)
		if code != 0 || sp6RefreshReason(t, out) != "no_fetch" {
			t.Errorf("ACT_NO_FETCH=1 %s --json: exit %d, refresh.reason %q, want no_fetch\n%s", name, code, sp6RefreshReason(t, out), out)
		}
		t.Setenv("ACT_NO_FETCH", "")
	}
}

// sp7DropExcludeLine removes one of act's lines from the nested repo's
// info/exclude, which makes `act doctor` report one warn-severity
// nested-exclude finding.
func sp7DropExcludeLine(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, ".act", ".git", "info", "exclude")
	body, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(body), "\n/.slow-writes\n") {
		t.Fatalf("read %s (err %v): no /.slow-writes line to drop:\n%s", path, err, body)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(body), "\n/.slow-writes\n", "\n", 1)), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestDocClaim_Spec_DoctorStrictAndNoFetchBullets asserts the two flag
// bullets added to the `act doctor` section. --strict: a run whose only
// finding is a warning exits 0, and under --strict the same finding is an
// error and the run exits 1. --no-fetch: an unreachable origin is exit 4
// without it and a warning with it, and ACT_NO_FETCH=1 is not the same
// switch.
func TestDocClaim_Spec_DoctorStrictAndNoFetchBullets(t *testing.T) {
	dir := sp6Site(t)
	createIssue(t, dir, "first")
	if res, code := sp6RunDoctor(t, dir); code != 0 || len(res.Findings) != 0 {
		t.Fatalf("seed: a fresh tracker: exit %d, findings %v; want 0 and none", code, sp6Checks(res))
	}
	sp7DropExcludeLine(t, dir)
	if res, code := sp6RunDoctor(t, dir); code != 0 || len(res.Findings) != 1 || !sp6Has(res, "nested-exclude", "warn") {
		t.Fatalf("act doctor with one warning: exit %d, findings %v; want 0 and nested-exclude/warn", code, sp6Checks(res))
	}
	if res, code := sp6RunDoctor(t, dir, "--strict"); code != 1 || len(res.Findings) != 1 || !sp6Has(res, "nested-exclude", "error") {
		t.Errorf("act doctor --strict with the same finding: exit %d, findings %v; want 1 and nested-exclude/error", code, sp6Checks(res))
	}
	if res, code := sp6RunDoctor(t, dir, "--fix"); code != 0 || len(res.Findings) != 1 {
		t.Fatalf("act doctor --fix: exit %d, findings %v; want the repair reported, exit 0", code, sp6Checks(res))
	}

	sp6Git(t, "-C", filepath.Join(dir, ".act"), "config", "remote.origin.url", "file://"+filepath.Join(t.TempDir(), "sp7-missing.git"))
	if res, code := sp6RunDoctor(t, dir); code != 4 || !sp6Has(res, "remote-reachable", "error") {
		t.Fatalf("act doctor with an unreachable origin: exit %d, findings %v; want 4 and a remote-reachable error", code, sp6Checks(res))
	}
	if res, code := sp6RunDoctor(t, dir, "--no-fetch"); code != 0 || !sp6Has(res, "remote-reachable", "warn") {
		t.Errorf("act doctor --no-fetch: exit %d, findings %v; want 0 and a remote-reachable warning", code, sp6Checks(res))
	}
	t.Setenv("ACT_NO_FETCH", "1")
	if res, code := sp6RunDoctor(t, dir); code != 4 || !sp6Has(res, "remote-reachable", "error") {
		t.Errorf("ACT_NO_FETCH=1 act doctor: exit %d, findings %v; the spec says the env var does not set doctor's --no-fetch (want 4)", code, sp6Checks(res))
	}
}

// sp7Keys returns the sorted keys of a JSON object.
func sp7Keys(m map[string]json.RawMessage) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

// sp7DoctorShape parses a doctor --json document into its top-level keys,
// the keys of its first finding ("" when there is none) and the keys of
// remote_status.
func sp7DoctorShape(t *testing.T, what, doc string) (top, finding, remote string, findingsNull bool) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &obj); err != nil {
		t.Fatalf("%s is not a JSON object: %v\n%s", what, err, doc)
	}
	top = sp7Keys(obj)
	findingsNull = string(obj["findings"]) == "null"
	var findings []map[string]json.RawMessage
	if err := json.Unmarshal(obj["findings"], &findings); err != nil {
		t.Fatalf("%s: findings is not an array or null: %v", what, err)
	}
	if len(findings) > 0 {
		finding = sp7Keys(findings[0])
	}
	var rs map[string]json.RawMessage
	if err := json.Unmarshal(obj["remote_status"], &rs); err != nil {
		t.Fatalf("%s: remote_status is not an object: %v", what, err)
	}
	var count int
	if err := json.Unmarshal(obj["count"], &count); err != nil || count != len(findings) {
		t.Errorf("%s: count is %s, findings holds %d (%v)", what, obj["count"], len(findings), err)
	}
	return top, finding, sp7Keys(rs), findingsNull
}

// TestDocClaim_Spec_DoctorJSONExampleMatchesBinary asserts the JSON example
// in the `act doctor` section and the sentence under it: the example parses,
// and its top-level keys, its finding's keys and its remote_status keys are
// exactly those of a real `act doctor --json` run with one finding; a clean
// run has the same three top-level keys and `findings: null`.
func TestDocClaim_Spec_DoctorJSONExampleMatchesBinary(t *testing.T) {
	section := sp6Section(t, sp6Spec(t), "### `act doctor`")
	i := strings.Index(section, "**JSON output:**\n```json\n")
	if i < 0 {
		t.Fatalf("the act doctor section has no JSON output example")
	}
	example := section[i+len("**JSON output:**\n```json\n"):]
	example = example[:strings.Index(example, "\n```")]
	exTop, exFinding, exRemote, _ := sp7DoctorShape(t, "the spec's example", example)
	const wantTop = "count,findings,remote_status"
	if exTop != wantTop {
		t.Errorf("the spec's example has top-level keys %q, want %q", exTop, wantTop)
	}

	dir := sp6Site(t)
	createIssue(t, dir, "first")
	out, stderr, code := runActIn(t, dir, "doctor", "--json")
	top, _, remote, null := sp7DoctorShape(t, "act doctor --json on a clean tracker", out)
	if code != 0 || top != wantTop || !null {
		t.Errorf("clean run: exit %d, top-level keys %q, findings null %v; want 0, %q, true\nstdout=%s\nstderr=%s", code, top, null, wantTop, out, stderr)
	}
	if remote != exRemote {
		t.Errorf("remote_status keys: binary %q, spec example %q", remote, exRemote)
	}

	sp7DropExcludeLine(t, dir)
	out, _, _ = runActIn(t, dir, "doctor", "--json")
	top, finding, remote, null := sp7DoctorShape(t, "act doctor --json with one finding", out)
	if top != exTop || finding != exFinding || remote != exRemote || null || finding == "" {
		t.Errorf("a run with one finding has keys top=%q finding=%q remote_status=%q; the spec's example has top=%q finding=%q remote_status=%q\nstdout=%s",
			top, finding, remote, exTop, exFinding, exRemote, out)
	}
	for _, gone := range []string{`"checks"`, `"summary"`, `"ok"`} {
		if strings.Contains(out, gone) {
			t.Errorf("act doctor --json carries %s, which the spec says it does not\n%s", gone, out)
		}
	}
}

// sp7ErrorRow is one row of the spec's error table.
type sp7ErrorRow struct {
	code string
	exit int
}

// sp7ErrorTable parses the table under "### 1. Error handling".
func sp7ErrorTable(t *testing.T) []sp7ErrorRow {
	t.Helper()
	section := sp6Section(t, sp6Spec(t), "### 1. Error handling")
	var rows []sp7ErrorRow
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z_]+)` +\\| (\\d+) +\\|").FindAllStringSubmatch(section, -1) {
		n, _ := strconv.Atoi(m[2])
		rows = append(rows, sp7ErrorRow{m[1], n})
	}
	if len(rows) < 12 {
		t.Fatalf("parsed %d rows from the spec's error table", len(rows))
	}
	return rows
}

// sp7SourceExit returns the exit code the source returns with an error: the
// integer of the first `}, N` return that follows the first line of file
// containing marker. For the two rows no test can provoke cheaply.
func sp7SourceExit(t *testing.T, file, marker string) int {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	i := strings.Index(string(body), marker)
	if i < 0 {
		t.Fatalf("%s no longer contains %q; find where the error is returned now", file, marker)
	}
	m := regexp.MustCompile(`(?m)^\s*\}, (\d+)$`).FindStringSubmatch(string(body)[i:])
	if m == nil {
		t.Fatalf("%s: no `}, N` return after %q", file, marker)
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// TestDocClaim_Spec_ErrorTableExitCodesMatchBinary asserts the exit column
// of the spec's error table, every row.
//
// Fourteen rows are checked by running the binary: each is provoked in a
// temp tracker and must exit with the row's code while reporting the row's
// error. Two rows cannot be provoked cheaply — claim_lost needs two writers
// racing through a shared remote, bootstrap_timeout a clone that outlasts
// its budget — so for those the exit code is read from the return statement
// in the source (their behaviour is asserted by TestDocClaim_ClaimLost* and
// TestDocClaim_BootstrapFromRemote_TimeoutEnvelope).
//
// A row with neither a provoker nor a source entry fails, so a new row
// cannot join the table unchecked.
func TestDocClaim_Spec_ErrorTableExitCodesMatchBinary(t *testing.T) {
	// run provokes the error and checks the exit code and the envelope.
	type provoker func(t *testing.T, code string, exit int)
	site := func(t *testing.T) (dir, id, other string) {
		dir = sp6Site(t)
		return dir, createIssue(t, dir, "first"), createIssue(t, dir, "second")
	}
	provoke := map[string]provoker{
		"not_in_git": func(t *testing.T, code string, exit int) {
			brokencoIsolateEnv(t)
			pass5Run(t, t.TempDir(), exit, code, "list", "--json")
			pass5Run(t, t.TempDir(), exit, code, "create", "--json", "sp7")
		},
		"act_not_initialized": func(t *testing.T, code string, exit int) {
			brokencoIsolateEnv(t)
			pass5Run(t, brokencoHostRepo(t), exit, code, "create", "--json", "sp7")
		},
		"issue_not_found": func(t *testing.T, code string, exit int) {
			dir, _, _ := site(t)
			pass5Run(t, dir, exit, code, "show", "--json", pass5NoSuchID)
		},
		"id_ambiguous": func(t *testing.T, code string, exit int) {
			dir, id, _ := site(t)
			pass5Run(t, dir, exit, code, "show", "--json", pass5Twin(t, dir, id))
		},
		"version_skew": func(t *testing.T, code string, exit int) {
			dir, id, _ := site(t)
			pass5Run(t, dir, 0, "", "version", "--check-repo", "--json")
			pass5Rewrite(t, pass5CreateOp(t, dir, id), pass5WriterRe, `"writer_version":"99.0.0"`)
			pass5Run(t, dir, exit, code, "version", "--check-repo", "--json")
		},
		"cycle_detected": func(t *testing.T, code string, exit int) {
			dir, id, _ := site(t)
			pass5Run(t, dir, exit, code, "update", "--json", "--parent", id, id)
		},
		"dep_not_found": func(t *testing.T, code string, exit int) {
			dir, id, other := site(t)
			pass5Run(t, dir, exit, code, "update", "--json", "--dep-rm", other, id)
		},
		"hook_failed": func(t *testing.T, code string, exit int) {
			dir, _, _ := site(t)
			pass5Hook(t, dir, "create")
			pass5Run(t, dir, exit, code, "create", "--json", "sp7")
		},
		"import_invalid_jsonl": func(t *testing.T, code string, exit int) {
			dir, _, _ := site(t)
			bad := filepath.Join(t.TempDir(), "sp7-bad.jsonl")
			if err := os.WriteFile(bad, []byte("not json\n"), 0o644); err != nil {
				t.Fatalf("write %s: %v", bad, err)
			}
			pass5Run(t, dir, exit, code, "import", "--json", bad)
		},
		"remote_unreachable": func(t *testing.T, code string, exit int) {
			dir, _, _ := site(t)
			pass5Run(t, dir, exit, code, "state", "import", "--json",
				"--from-remote", "file://"+filepath.Join(t.TempDir(), "sp7-missing.git"), t.TempDir())
		},
		"target_not_empty": func(t *testing.T, code string, exit int) {
			dir, _, _ := site(t)
			target := t.TempDir()
			if err := os.MkdirAll(filepath.Join(target, ".act"), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(target, ".act", "sp7-junk"), []byte("x"), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			pass5Run(t, dir, exit, code, "state", "import", "--json", target)
		},
		"blocked_by_external_dep": func(t *testing.T, code string, exit int) {
			dir, id, _ := site(t)
			pass5Run(t, dir, 0, "", "dep", "add", "--json", "--external", "sp7:X-1", id)
			pass5Run(t, dir, exit, code, "close", "--json", id)
		},
		"tracker_not_checked_out": func(t *testing.T, code string, exit int) {
			brokencoIsolateEnv(t)
			host := brokencoHostRepo(t)
			pass5Run(t, host, 0, "", "list", "--json") // no tracker remote yet: the no-state line
			bares := t.TempDir()
			brokencoTrackerBare(t, bares)
			t.Setenv("ACT_TRACKER_REMOTE", filepath.Join(bares, "{repo}.git"))
			out, stderr, got := runActIn(t, host, "list", "--json")
			if got != exit || !strings.Contains(out+stderr, code) {
				t.Errorf("act list with an uncloned tracker: exit %d, want %d and %s\nstdout=%s\nstderr=%s", got, exit, code, out, stderr)
			}
		},
		"blocked_requires_dep": func(t *testing.T, code string, exit int) {
			dir, id, _ := site(t)
			pass5Run(t, dir, exit, code, "update", "--json", "--status", "blocked", id)
		},
	}
	// Read from the source, not run: file, and a marker on or just above
	// the line that sets the error.
	fromSource := map[string][2]string{
		"claim_lost":        {"internal/cli/update.go", "Error:   ErrClaimLost,"},
		"bootstrap_timeout": {"internal/cli/bootstrap_worker.go", `"error":   ErrBootstrapTimeout,`},
	}

	rows := sp7ErrorTable(t)
	inTable := map[string]bool{}
	for _, row := range rows {
		if inTable[row.code] {
			t.Errorf("the spec's error table has two rows for %s", row.code)
		}
		inTable[row.code] = true
		row := row
		t.Run(row.code, func(t *testing.T) {
			if p, ok := provoke[row.code]; ok {
				p(t, row.code, row.exit)
				return
			}
			if src, ok := fromSource[row.code]; ok {
				if got := sp7SourceExit(t, src[0], src[1]); got != row.exit {
					t.Errorf("the spec's error table gives %s exit %d; %s returns %d with it", row.code, row.exit, src[0], got)
				}
				return
			}
			// The eighth pass (act-0c1c31) added a row for every emitted
			// code; TestDocClaim_Spec_ErrorTableRowsMatchBinaryAndSource
			// checks each of those rows' exit codes, by a provoked run or
			// from the emit site's return statement.
			if sp8RowChecked(row.code) {
				return
			}
			t.Errorf("the spec's error table has a row for %s (exit %d) that neither this test nor the eighth pass's row test checks; add a provoker", row.code, row.exit)
		})
	}
	for code := range provoke {
		if !inTable[code] {
			t.Errorf("this test provokes %s, but the spec's error table has no row for it", code)
		}
	}
	for code := range fromSource {
		if !inTable[code] {
			t.Errorf("this test reads %s's exit from the source, but the spec's error table has no row for it", code)
		}
	}
}

// sp7Emitters scans the non-test Go source under cmd/ and internal/ and
// returns, for each error code declared in internal/cli/errors.go, the
// places that name it: the code as a string literal, or its Err* constant
// (bare inside package cli, `cli.`-qualified elsewhere). The constant's own
// declaration, comment lines and the help text in cmd/act/help.go do not
// count. It also returns the declared code -> constant map.
func sp7Emitters(t *testing.T) (emitters map[string][]string, consts map[string]string) {
	t.Helper()
	root := sp6RepoRoot(t)
	decl, err := os.ReadFile(filepath.Join(root, "internal", "cli", "errors.go"))
	if err != nil {
		t.Fatalf("read errors.go: %v", err)
	}
	consts = map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*(Err[A-Za-z]+)\s*=\s*"([a-z_]+)"`).FindAllStringSubmatch(string(decl), -1) {
		consts[m[2]] = m[1]
	}
	if len(consts) < 30 {
		t.Fatalf("parsed only %d error constants from internal/cli/errors.go", len(consts))
	}
	emitters = map[string][]string{}
	scanned := 0
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.Walk(filepath.Join(root, top), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if rel == "cmd/act/help.go" {
				return nil
			}
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			scanned++
			inCLI := strings.HasPrefix(rel, "internal/cli/")
			for n, line := range strings.Split(string(body), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "//") {
					continue
				}
				for slug, konst := range consts {
					if rel == "internal/cli/errors.go" && regexp.MustCompile(`^\s*`+konst+`\s*=`).MatchString(line) {
						continue
					}
					named := strings.Contains(line, `"`+slug+`"`) ||
						strings.Contains(line, "cli."+konst) ||
						(inCLI && regexp.MustCompile(`(^|[^A-Za-z.])`+konst+`\b`).MatchString(line))
					if named {
						emitters[slug] = append(emitters[slug], rel+":"+strconv.Itoa(n+1))
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
	return emitters, consts
}

// TestDocClaim_HelpErrors_EveryRowHasAnEmitter asserts that every code `act
// help errors` lists, and every code in the spec's error table, is one some
// non-test source line names — and that `ambiguous_id` and
// `redact_target_not_found`, which nothing names, are in neither list. The
// sixth pass checked three codes by name
// (TestDocClaim_HelpErrors_NoRowForCodesNothingEmits); this checks every row.
func TestDocClaim_HelpErrors_EveryRowHasAnEmitter(t *testing.T) {
	emitters, consts := sp7Emitters(t)
	// The scan can tell an emitted code from a dead one.
	if len(emitters["not_in_git"]) < 10 || len(emitters["claim_lost"]) != 1 {
		t.Fatalf("the source scan looks wrong: not_in_git %d sites, claim_lost %v", len(emitters["not_in_git"]), emitters["claim_lost"])
	}
	// blocked_requires_dep is emitted as a literal and has no constant.
	literalOnly := map[string]bool{"blocked_requires_dep": true}

	help, stderr, code := runAct(t, "help", "errors")
	help += stderr
	i := strings.Index(help, "ERROR CODES")
	j := strings.Index(help, "NO ACT STATE IN THIS CHECKOUT")
	if code != 0 || i < 0 || j < i {
		t.Fatalf("act help errors: exit %d; could not find the ERROR CODES section", code)
	}
	var helpRows []string
	for _, m := range regexp.MustCompile(`(?m)^    ([a-z_]+) +\S`).FindAllStringSubmatch(help[i:j], -1) {
		helpRows = append(helpRows, m[1])
	}
	if len(helpRows) < 25 {
		t.Fatalf("parsed %d code rows from act help errors: %v", len(helpRows), helpRows)
	}
	// The eighth pass (act-0c1c31) lists every emitted code, including the
	// many emitted as bare literals and the two declared outside errors.go;
	// its scan (sp8EmittedCodes) is the stricter test of "has an emitter".
	sp8 := sp8EmittedCodes(t)
	check := func(where, slug string) {
		if len(sp8.sites[slug]) > 0 {
			return
		}
		if _, declared := consts[slug]; !declared && !literalOnly[slug] {
			t.Errorf("%s lists %s, which internal/cli/errors.go does not declare", where, slug)
		}
		if len(emitters[slug]) == 0 && !sp7LiteralEmitted(t, slug) {
			t.Errorf("%s lists %s, which no non-test source line emits; drop the row (leave the constant)", where, slug)
		}
	}
	for _, slug := range helpRows {
		check("act help errors", slug)
	}
	for _, row := range sp7ErrorTable(t) {
		check("the spec's error table", row.code)
	}

	section := sp6Section(t, sp6Spec(t), "### 1. Error handling")
	for _, slug := range []string{"ambiguous_id", "redact_target_not_found"} {
		if len(emitters[slug]) != 0 || sp7LiteralEmitted(t, slug) {
			t.Errorf("%s is emitted now (%v); restore its row", slug, emitters[slug])
		}
		if regexp.MustCompile(`(?m)^    ` + slug + `\b`).MatchString(help) {
			t.Errorf("act help errors still has a row for %s", slug)
		}
		if strings.Contains(section, "| `"+slug+"`") {
			t.Errorf("the spec's error table still has a row for %s", slug)
		}
	}
}

// sp7LiteralEmitted reports whether a non-test source file outside
// cmd/act/help.go holds slug as a string literal — for a code with no Err*
// constant, which sp7Emitters does not index.
func sp7LiteralEmitted(t *testing.T, slug string) bool {
	t.Helper()
	root := sp6RepoRoot(t)
	found := false
	for _, top := range []string{"cmd", "internal"} {
		_ = filepath.Walk(filepath.Join(root, top), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, filepath.Join("cmd", "act", "help.go")) {
				return err
			}
			body, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			for _, line := range strings.Split(string(body), "\n") {
				if !strings.HasPrefix(strings.TrimSpace(line), "//") && strings.Contains(line, `"`+slug+`"`) && !strings.Contains(line, `= "`+slug+`"`) {
					found = true
				}
			}
			return nil
		})
	}
	return found
}

// TestDocClaim_Spec_AmbiguousPrefixEnvelope asserts the "Prefix match" step
// of the id-resolution pipeline: an ambiguous prefix is exit 2 with the
// id_ambiguous code, and details carries the prefix and the candidates.
func TestDocClaim_Spec_AmbiguousPrefixEnvelope(t *testing.T) {
	dir := sp6Site(t)
	id := createIssue(t, dir, "first")
	prefix := pass5Twin(t, dir, id)
	out, _ := pass5Run(t, dir, 2, "id_ambiguous", "show", "--json", prefix)
	var env struct {
		Details struct {
			Prefix     string   `json:"prefix"`
			Candidates []string `json:"candidates"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Details.Prefix != prefix || len(env.Details.Candidates) != 2 {
		t.Errorf("act show of an ambiguous prefix: details %+v (%v); want prefix %q and two candidates\n%s", env.Details, err, prefix, out)
	}
	if strings.Contains(out, "ambiguous_id") {
		t.Errorf("the envelope carries the retired ambiguous_id code\n%s", out)
	}
}
