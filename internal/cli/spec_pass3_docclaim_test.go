package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// Doc-claim cover for the third stale pass over docs/spec.md (act-9c9a85).
// The spec described a writer_version "reader gate", a squash-and-push
// refusal, a closed_by_tree reverse index and a doctor check on
// writer_version, none of which the code has. Each test asserts the reworded
// sentence at the boundary a reader of the spec relies on: exit codes, the
// op files on disk, the commit log of the nested .act/ repo.

// seedCreateWithOpVersion writes a create op for id whose op_version is the
// given value, straight into the op log. The envelope is marshalled at the
// current op_version and the field is rewritten in the bytes, because the
// envelope validator refuses to marshal any other value.
func seedCreateWithOpVersion(t *testing.T, root, id string, wallMs int64, opVersion int, writer string) string {
	t.Helper()
	env := makeCreateEnv(t, id, wallMs, 0, "op-version probe", "task", 2)
	env.WriterVersion = writer
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if opVersion != op.CurrentOpVersion {
		re := regexp.MustCompile(`"op_version":\s*\d+`)
		if !re.Match(body) {
			t.Fatalf("no op_version field in marshalled envelope: %s", body)
		}
		body = re.ReplaceAll(body, []byte(`"op_version":`+itoaDocClaim(opVersion)))
	}
	dir := filepath.Join(root, ".act", "ops", id, "2026-04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, id+"-create.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func itoaDocClaim(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// actRepoSubjects returns the commit subjects of the nested .act/ repo,
// newest first.
func actRepoSubjects(t *testing.T, root string) []string {
	t.Helper()
	out := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "--format=%s"))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// TestDocClaim_Doctor_UnknownOpVersionChecksOpVersion asserts the doctor
// table row and exit-code line for `unknown-op-version`: the check tests
// op_version (not writer_version), emits one error finding per offending op
// file naming it, exits 1 (the spec used to say 4), and --fix leaves the op
// file alone.
func TestDocClaim_Doctor_UnknownOpVersionChecksOpVersion(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")

	// A newer writer_version on a known op_version is not a finding.
	seedCreateWithOpVersion(t, root, "act-dddd", 1700000030000, op.CurrentOpVersion, "99.0.0")
	out, code := RunDoctor(root, DoctorOptions{Check: "unknown-op-version"})
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("doctor output type = %T, want DoctorResult", out)
	}
	if code != 0 || len(res.Findings) != 0 {
		t.Fatalf("newer writer_version, known op_version: exit = %d, findings = %+v; want exit 0 and none "+
			"(the check must not look at writer_version)", code, res.Findings)
	}

	// A newer op_version from this binary's own writer_version is.
	bogus := seedCreateWithOpVersion(t, root, "act-eeee", 1700000040000, op.CurrentOpVersion+1, op.WriterVersion)
	want, err := os.ReadFile(bogus)
	if err != nil {
		t.Fatalf("read %s: %v", bogus, err)
	}
	for _, fix := range []bool{false, true} {
		out, code := RunDoctor(root, DoctorOptions{Check: "unknown-op-version", Fix: fix})
		res, ok := out.(DoctorResult)
		if !ok {
			t.Fatalf("fix=%v: doctor output type = %T, want DoctorResult", fix, out)
		}
		if code != 1 {
			t.Errorf("fix=%v: exit = %d, want 1 (an ordinary error finding, not 4)", fix, code)
		}
		if len(res.Findings) != 1 {
			t.Fatalf("fix=%v: %d findings, want exactly 1 (one per offending op file): %+v", fix, len(res.Findings), res.Findings)
		}
		f := res.Findings[0]
		if f.Check != "unknown-op-version" || f.Severity != "error" {
			t.Errorf("fix=%v: finding = %+v, want check unknown-op-version at severity error", fix, f)
		}
		if !strings.Contains(f.Message, filepath.Base(bogus)) || !strings.Contains(f.Message, "op_version") {
			t.Errorf("fix=%v: finding message %q does not name the op file %s and op_version", fix, f.Message, filepath.Base(bogus))
		}
		got, err := os.ReadFile(bogus)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("fix=%v: the op file was changed or removed (err=%v); the check is read-only", fix, err)
		}
	}
}

// TestDocClaim_NewerOpVersionAbortsFoldWithoutVersionSkew asserts the
// migration paragraph: a binary that meets an op with a newer op_version does
// not answer version_skew — the envelope check fails and the fold aborts,
// naming the op file and op_version, with `act list` exiting 1.
func TestDocClaim_NewerOpVersionAbortsFoldWithoutVersionSkew(t *testing.T) {
	root := listTestRepo(t)
	path := seedCreateWithOpVersion(t, root, "act-dddd", 1700000030000, op.CurrentOpVersion+1, op.WriterVersion)

	out, code := RunList(root, ListOptions{Limit: 200})
	if code != 1 {
		t.Fatalf("list with a newer-op_version op: exit = %d, want 1; out=%+v", code, out)
	}
	le, ok := out.(ListErrorOutput)
	if !ok {
		t.Fatalf("list output type = %T, want ListErrorOutput", out)
	}
	if le.Error == ErrVersionSkew {
		t.Errorf("list error code = %q; the spec says an older binary does not answer version_skew here", le.Error)
	}
	if !strings.Contains(le.Message, "op_version") || !strings.Contains(le.Message, filepath.Base(path)) {
		t.Errorf("list error %q does not name both op_version and the op file %s", le.Message, filepath.Base(path))
	}
}

// TestDocClaim_NewerWriterOpDoesNotBlockWritesAndNothingSquashes asserts the
// "no reader gate" paragraph's write half and the edge-case bullet that
// replaced "Squash-and-push refused on version_skew": with an op from a newer
// writer in the tracker, `act version --check-repo` reports the skew, yet
// `act update` and `act close` still commit — each as its own act-op commit,
// with the earlier commits left as they were (nothing is squashed).
func TestDocClaim_NewerWriterOpDoesNotBlockWritesAndNothingSquashes(t *testing.T) {
	root := makeCreateRepo(t)
	a := mustCreate(t, root, "A")
	b := mustCreate(t, root, "B")
	seedCreateWithOpVersion(t, root, "act-dddd", 1700000030000, op.CurrentOpVersion, "99.0.0")

	vout, vcode := RunVersion(true, root)
	if m, _ := vout.(map[string]any); vcode != 4 || m["error"] != ErrVersionSkew {
		t.Fatalf("version --check-repo with a newer-writer op: exit = %d, out=%+v; want 4 / version_skew "+
			"(otherwise this test is not exercising skew)", vcode, vout)
	}

	before := actRepoSubjects(t, root)
	if len(before) < 2 {
		t.Fatalf("expected the two create commits in .act/, got %v", before)
	}

	prio := 0
	if out, code := RunUpdate(root, UpdateOptions{ID: a, Priority: &prio}); code != 0 {
		t.Fatalf("update with a newer-writer op in the tracker: exit = %d, out=%+v; want 0 (no version_skew refusal)", code, out)
	}
	if out, code := RunClose(root, CloseOptions{ID: b, Reason: "done"}); code != 0 {
		t.Fatalf("close with a newer-writer op in the tracker: exit = %d, out=%+v; want 0 (no version_skew refusal)", code, out)
	}

	after := actRepoSubjects(t, root)
	if len(after) != len(before)+2 {
		t.Fatalf(".act/ has %d commits after two writes, want %d (one commit per op write, none squashed)\nbefore: %v\nafter:  %v",
			len(after), len(before)+2, before, after)
	}
	if want := "act-op: (" + ShortIssueID(b) + ") close"; after[0] != want {
		t.Errorf("newest subject = %q, want %q", after[0], want)
	}
	if want := "act-op: (" + ShortIssueID(a) + ") update_field"; after[1] != want {
		t.Errorf("second-newest subject = %q, want %q", after[1], want)
	}
	if strings.Join(after[2:], "\n") != strings.Join(before, "\n") {
		t.Errorf("earlier commits were rewritten:\nbefore: %v\nafter:  %v", before, after[2:])
	}
	for _, s := range after {
		if strings.HasPrefix(s, "act-squash") {
			t.Errorf("found a squash commit %q; no command squashes act-op commits", s)
		}
	}
}

// TestDocClaim_Close_CommitSubjectAndNoClosedByTree asserts the `act close`
// Behavior line: the close commits one op file with subject
// `act-op: (<short-id>) close` — the id cut to `act-` plus six hex — and
// nothing computes or stores a closed_by_tree value (the spec used to say the
// close stored one, and listed the column in the index schema).
func TestDocClaim_Close_CommitSubjectAndNoClosedByTree(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	if !regexp.MustCompile(`^act-[0-9a-f]{6,}$`).MatchString(id) {
		t.Fatalf("issue id %q is not act- plus at least six hex", id)
	}
	before := map[string]bool{}
	for _, f := range opFilesUnder(t, root) {
		before[f] = true
	}

	if out, code := RunClose(root, CloseOptions{ID: id, Reason: "done"}); code != 0 {
		t.Fatalf("close: exit = %d, out=%+v", code, out)
	}

	// Subject, spelled out from the id rather than through the builder.
	want := "act-op: (" + id[:len("act-")+6] + ") close"
	if got := actRepoSubjects(t, root)[0]; got != want {
		t.Errorf("close commit subject = %q, want %q", got, want)
	}
	// A longer id is cut to six hex in the subject.
	long := op.Envelope{IssueID: "act-0d9d11112222aaaa", OpType: "close"}
	if got := BuildOpCommitMessage(long); got != "act-op: (act-0d9d11) close" {
		t.Errorf("subject for a 16-hex id = %q, want %q", got, "act-op: (act-0d9d11) close")
	}

	// One op file, and it is the only thing in the commit.
	var added []string
	for _, f := range opFilesUnder(t, root) {
		if !before[f] {
			added = append(added, f)
		}
	}
	if len(added) != 1 {
		t.Fatalf("close wrote %d op files, want 1: %v", len(added), added)
	}
	committed := strings.Fields(runOut(t, filepath.Join(root, ".act"), "git", "show", "--name-only", "--format=", "HEAD"))
	if len(committed) != 1 || !strings.HasSuffix(added[0], filepath.FromSlash(committed[0])) {
		t.Errorf("close commit touches %v, want exactly the close op file %s", committed, added[0])
	}
	body, err := os.ReadFile(added[0])
	if err != nil {
		t.Fatalf("read %s: %v", added[0], err)
	}
	if bytes.Contains(body, []byte("closed_by_tree")) {
		t.Errorf("the close op carries closed_by_tree: %s", body)
	}

	// The index: closed_at is there (so the probe works), closed_by_tree is not.
	idx, err := index.Open(config.Layout(root).IndexDB)
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	defer idx.Close()
	rows, err := idx.DB().Query(`PRAGMA table_info(issues)`)
	if err != nil {
		t.Fatalf("table_info(issues): %v", err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		cols[name] = true
	}
	if !cols["closed_at"] {
		t.Fatalf("issues table has no closed_at column (%v); the column probe is not reading the index", cols)
	}
	if cols["closed_by_tree"] {
		t.Errorf("issues table has a closed_by_tree column; the spec says the index has none")
	}
}

// TestDocClaim_Spec_PropertyAndFuzzTestsExist asserts the spec's property and
// fuzz tiers (§7.1, §7.3) name only tests that exist in internal/fold. The
// sections used to name `prop_fold_permutation_invariance` and
// `fuzz_fold_determinism`, which were never written. Both sides are printed
// on a miss.
func TestDocClaim_Spec_PropertyAndFuzzTestsExist(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	spec := string(body)

	declared := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(root, "internal", "fold", "*_test.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no test files under internal/fold (err=%v)", err)
	}
	funcRE := regexp.MustCompile(`(?m)^func\s+((?:Test|Fuzz)[A-Za-z0-9_]+)\s*\(`)
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, m := range funcRE.FindAllStringSubmatch(string(src), -1) {
			declared[m[1]] = true
		}
	}

	nameRE := regexp.MustCompile("\\*\\*`([A-Za-z0-9_]+)`\\*\\*")
	for _, sec := range []struct{ start, end, prefix string }{
		{"#### 7.1 Property tests", "#### 7.2 Golden tests", "TestProperty"},
		{"#### 7.3 Fuzz", "#### 7.4 Concurrency tests", "Fuzz"},
	} {
		_, rest, ok := strings.Cut(spec, sec.start)
		if !ok {
			t.Fatalf("docs/spec.md has no %q heading", sec.start)
		}
		text, _, ok := strings.Cut(rest, sec.end)
		if !ok {
			t.Fatalf("docs/spec.md has no %q heading after %q", sec.end, sec.start)
		}
		var named []string
		for _, m := range nameRE.FindAllStringSubmatch(text, -1) {
			named = append(named, m[1])
		}
		if len(named) == 0 {
			t.Errorf("%s names no tests in bold code; say plainly if the tier does not exist", sec.start)
		}
		for _, n := range named {
			if !strings.HasPrefix(n, sec.prefix) {
				t.Errorf("%s names %q, which is not a %s* function", sec.start, n, sec.prefix)
			}
			if !declared[n] {
				t.Errorf("%s names %q, but internal/fold declares no such test\n  spec names: %v", sec.start, n, named)
			}
		}
	}
}
