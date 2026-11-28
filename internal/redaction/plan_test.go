package redaction

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/op"
)

// redCreate is a create op for redIssue whose title and description hold s.
func redCreate(t *testing.T, s string) op.Envelope {
	t.Helper()
	return redEnv(t, redIssue, "create", redWall, 0, redNode,
		`{"title":"Statement for `+s+`","description":"Wire from account `+s+`.","type":"task","nonce":"`+redNonce+`"}`)
}

// redDescUpdate is a later update_field description op holding s.
func redDescUpdate(t *testing.T, s string) op.Envelope {
	t.Helper()
	return redEnv(t, redIssue, "update_field", redWall+1000, 0, redNode,
		`{"field":"description","value":"Wire from account `+s+` to savings."}`)
}

// redReissued is env with patterns applied and re-issued at gen.
func redReissued(t *testing.T, env op.Envelope, patterns []string, gen int) op.Envelope {
	t.Helper()
	out, _, err := Apply(env, patterns)
	if err != nil {
		t.Fatal(err)
	}
	re, err := Reissue(out, gen)
	if err != nil {
		t.Fatal(err)
	}
	return re
}

func redPlan(t *testing.T, stateRoot string, patterns []string, own map[string]bool) *Plan {
	t.Helper()
	p, err := NewPlan(stateRoot, patterns, own)
	if err != nil {
		t.Fatalf("NewPlan: %v", err)
	}
	return p
}

func redExecute(t *testing.T, p *Plan) {
	t.Helper()
	if _, _, err := p.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestPlan_ReissuesEveryLiveSlotMemberAtNextGeneration(t *testing.T) {
	root := t.TempDir()
	const a, b = "A-SECRET-1111", "B-SECRET-2222"
	orig := redEnv(t, redIssue, "create", redWall, 0, redNode,
		`{"title":"t","description":"`+a+` and `+b+`","type":"task","nonce":"`+redNonce+`"}`)
	// Two redactions that re-issued the same op independently (§6.3):
	// two different bodies at generation 1, beside the original.
	xa := redReissued(t, orig, []string{a}, 1)
	xb := redReissued(t, orig, []string{b}, 1)
	origRel := redWriteOp(t, root, orig)
	xaRel := redWriteOp(t, root, xa)
	xbRel := redWriteOp(t, root, xb)

	// Only xa still holds b; xb does not. Both live members are re-issued
	// anyway, at max generation + 1.
	p := redPlan(t, root, []string{b}, nil)
	if len(p.Residuals) != 0 {
		t.Fatalf("Residuals = %v, want none", p.Residuals)
	}
	want := Counts{OpsRewritten: 2, Replacements: 1, LeftoversRemoved: 1}
	if p.Totals != want {
		t.Errorf("Totals = %+v, want %+v", p.Totals, want)
	}
	redExecute(t, p)

	files := redFiles(t, root, "ops")
	if len(files) != 2 {
		t.Fatalf("ops files after = %v, want 2", files)
	}
	for _, rel := range files {
		if rel == origRel || rel == xaRel || rel == xbRel {
			t.Errorf("old file %s still present", rel)
		}
		env := redRead(t, root, rel)
		if g := Generation(env); g != 2 {
			t.Errorf("%s: generation %d, want 2", rel, g)
		}
		if strings.Contains(string(env.Payload), b) {
			t.Errorf("%s still holds the pattern", rel)
		}
	}
	// xb's body (which still holds a: this pass's patterns do not know it)
	// survives as a live member rather than being hidden by the rule.
	var holdsA int
	for _, rel := range files {
		if strings.Contains(string(redRead(t, root, rel).Payload), a) {
			holdsA++
		}
	}
	if holdsA != 1 {
		t.Errorf("%d re-issued files hold the other redaction's string, want 1 (xb kept alive)", holdsA)
	}

	// A plain single-member slot goes from generation 0 to 1.
	root2 := t.TempDir()
	redWriteOp(t, root2, redCreate(t, redSecret))
	p2 := redPlan(t, root2, []string{redSecret}, nil)
	redExecute(t, p2)
	files2 := redFiles(t, root2, "ops")
	if len(files2) != 1 || Generation(redRead(t, root2, files2[0])) != 1 {
		t.Errorf("single slot: files %v, want one file at generation 1", files2)
	}
}

func TestPlan_TwoPathsOfOneOpCollapse(t *testing.T) {
	root := t.TempDir()
	env := redCreate(t, redSecret)
	body := redMarshal(t, env)
	dashRel := redWriteOp(t, root, env)
	// The same op stored again under the pre-act-2f3d colon file name.
	colonName := strings.Replace(filepath.Base(dashRel), "T19-41-00", "T19:41:00", 1)
	if colonName == filepath.Base(dashRel) {
		t.Fatalf("fixture: could not build the colon form of %s", dashRel)
	}
	colonRel := redWriteAt(t, root, filepath.Join(root, filepath.FromSlash(redDir(dashRel)), colonName), body)

	p := redPlan(t, root, []string{redSecret}, nil)
	if want := (Counts{OpsRewritten: 1, Replacements: 2}); p.Totals != want {
		t.Errorf("Totals = %+v, want %+v (two paths of one op count once)", p.Totals, want)
	}
	redExecute(t, p)
	files := redFiles(t, root, "ops")
	if len(files) != 1 || files[0] == dashRel || files[0] == colonRel {
		t.Fatalf("ops files after = %v, want exactly one new file", files)
	}

	// Two different bodies that the patterns make equal collapse as well:
	// the remedy for a redaction conflict is running both redactions.
	root2 := t.TempDir()
	const a, b = "A-SECRET-1111", "B-SECRET-2222"
	orig := redEnv(t, redIssue, "create", redWall, 0, redNode,
		`{"title":"t","description":"`+a+` and `+b+`","type":"task","nonce":"`+redNonce+`"}`)
	redWriteOp(t, root2, redReissued(t, orig, []string{a}, 1))
	redWriteOp(t, root2, redReissued(t, orig, []string{b}, 1))
	p2 := redPlan(t, root2, []string{a, b}, nil)
	redExecute(t, p2)
	if files := redFiles(t, root2, "ops"); len(files) != 1 {
		t.Errorf("conflicting bodies after both patterns: files %v, want one", files)
	}
}

func TestPlan_OwnStaleYieldsToForeign(t *testing.T) {
	const a, b = "A-SECRET-1111", "B-SECRET-2222"
	orig := redEnv(t, redIssue, "create", redWall, 0, redNode,
		`{"title":"t","description":"`+a+` and `+b+`","type":"task","nonce":"`+redNonce+`"}`)

	// run builds checkout B's tree as it stands after its first pass and a
	// rebase onto checkout A's redaction, then runs B's second pass with
	// the given own map, and returns the resulting op files' payloads.
	run := func(t *testing.T, useOwn bool) []string {
		root := t.TempDir()
		origRel := redWriteOp(t, root, orig)
		own := map[string]bool{}
		// B's first pass, patterns {b}: re-issues X as X'b.
		redExecute(t, redPlan(t, root, []string{b}, own))
		if len(own) != 1 {
			t.Fatalf("own after first pass = %v, want one path", own)
		}
		// The rebase brings A's commit: X'a added (A redacted a from the
		// same original). X itself is already gone.
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(origRel))); !os.IsNotExist(err) {
			t.Fatalf("original still present after first pass")
		}
		redWriteOp(t, root, redReissued(t, orig, []string{a}, 1))
		var ownArg map[string]bool
		if useOwn {
			ownArg = own
		}
		redExecute(t, redPlan(t, root, []string{b}, ownArg))
		var out []string
		for _, rel := range redFiles(t, root, "ops") {
			out = append(out, string(redRead(t, root, rel).Payload))
		}
		return out
	}

	got := run(t, true)
	if len(got) != 1 {
		t.Fatalf("with own: %d files, want 1: %v", len(got), got)
	}
	if strings.Contains(got[0], a) || strings.Contains(got[0], b) {
		t.Errorf("with own: surviving payload still holds a string: %s", got[0])
	}
	if !strings.Contains(got[0], `"redacted":2`) {
		t.Errorf("with own: payload %s, want generation 2", got[0])
	}

	// Without the rule (no own map) both bodies stay live and B's own
	// stale copy, which still holds a, is re-issued — the failure the rule
	// exists to prevent. This is what proves the rule fired above.
	without := run(t, false)
	var holdsA bool
	for _, p := range without {
		holdsA = holdsA || strings.Contains(p, a)
	}
	if !holdsA {
		t.Errorf("without own: no file holds a; the comparison cannot show the rule's effect: %v", without)
	}
}

func TestPlan_ResidualInStructuralField(t *testing.T) {
	root := t.TempDir()
	createRel := redWriteOp(t, root, redCreate(t, redSecret)) // text fields only: not a residual
	claimRel := redWriteOp(t, root, redEnv(t, redIssue, "claim", redWall+2000, 0, redNode,
		`{"assignee":"`+redSecret+`"}`))
	machineRel := redWriteOp(t, root, redEnv(t, "act-bbbb", "create", redWall, 0, redNode,
		`{"title":"`+redSecret+`","type":"task","machine":"`+redSecret+`","nonce":"`+redNonce+`"}`))
	hookRel := redWriteAt(t, root, filepath.Join(root, "hooks", "close"), []byte("echo "+redSecret+"\n"))
	// Derived files are deleted and rebuilt, not rewritten: no residual.
	redWriteAt(t, root, filepath.Join(root, "index.db"), []byte("sqlite "+redSecret))
	redWriteAt(t, root, filepath.Join(root, "index.db.malformed-123"), []byte(redSecret))
	// Git history is out of scope by construction.
	redWriteAt(t, root, filepath.Join(root, ".git", "objects", "ab", "cd"), []byte(redSecret))

	p := redPlan(t, root, []string{redSecret}, nil)
	sortedWant := []string{hookRel, claimRel, machineRel}
	sort.Strings(sortedWant)
	if !reflect.DeepEqual(p.Residuals, sortedWant) {
		t.Errorf("Residuals = %v, want %v", p.Residuals, sortedWant)
	}
	for _, r := range p.Residuals {
		if r == createRel {
			t.Errorf("the create op, whose only hits are text fields, is a residual")
		}
		if strings.Contains(r, redSecret) {
			t.Errorf("residual path %q holds the pattern", r)
		}
	}

	// Scan, the final verification, reads everything outside .git.
	paths, err := Scan(root, []string{redSecret})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"index.db", "index.db.malformed-123", hookRel, claimRel, createRel} {
		if !redContains(paths, want) {
			t.Errorf("Scan = %v, missing %s", paths, want)
		}
	}
	for _, p := range paths {
		if strings.HasPrefix(p, ".git/") {
			t.Errorf("Scan read inside .git: %s", p)
		}
	}
}

// TestPlan_ResidualWrittenWithJSONEscapes plants the pattern in files whose
// bytes never hold either raw form — every '-' written as -, in an op's
// structural field, in an object key, in a JSON config file and in a JSONL
// log — and asserts both the plan's residual check and Scan still find it,
// because both match decoded strings and keys, not only bytes.
func TestPlan_ResidualWrittenWithJSONEscapes(t *testing.T) {
	root := t.TempDir()
	esc := func(b []byte) []byte {
		escDash := "\\" + "u002d" // JSON's escape for '-'
		out := []byte(strings.ReplaceAll(string(b), redSecret, strings.ReplaceAll(redSecret, "-", escDash)))
		if strings.Contains(string(out), redSecret) {
			t.Fatalf("fixture: escaped bytes still hold the raw pattern")
		}
		return out
	}
	claim := redEnv(t, redIssue, "claim", redWall, 0, redNode, `{"assignee":"`+redSecret+`"}`)
	claimRel := redWriteAt(t, root,
		filepath.Join(op.ShardDir(filepath.Join(root, "ops"), redIssue, redWall), op.Filename(claim)),
		esc(redMarshal(t, claim)))
	keyed := redEnv(t, "act-bbbb", "import", redWall, 0, redNode, `{"source_ref":"x","mapping":{"`+redSecret+`":"y"}}`)
	keyedRel := redWriteAt(t, root,
		filepath.Join(op.ShardDir(filepath.Join(root, "ops"), "act-bbbb", redWall), op.Filename(keyed)),
		esc(redMarshal(t, keyed)))
	configRel := redWriteAt(t, root, filepath.Join(root, "config.json"), esc([]byte(`{"label":"`+redSecret+`"}`)))
	logRel := redWriteAt(t, root, filepath.Join(root, ".pending-pushes"),
		esc([]byte(`{"op_type":"create"}`+"\n"+`{"note":"`+redSecret+`"}`+"\n")))
	if _, err := op.UnmarshalTolerant(esc(redMarshal(t, claim))); err != nil {
		t.Fatalf("fixture: escaped op does not parse: %v", err)
	}

	want := []string{claimRel, keyedRel, configRel, logRel}
	sort.Strings(want)
	if p := redPlan(t, root, []string{redSecret}, nil); !reflect.DeepEqual(p.Residuals, want) {
		t.Errorf("Plan residuals = %v, want %v", p.Residuals, want)
	}
	if paths, err := Scan(root, []string{redSecret}); err != nil || !reflect.DeepEqual(paths, want) {
		t.Errorf("Scan = %v, %v; want %v", paths, err, want)
	}
}

// redEscSecret is redSecret with every '-' written as a JSON \u escape, so
// its bytes match neither raw form.
func redEscSecret() string {
	return strings.ReplaceAll(redSecret, "-", "\\"+"u002d")
}

// TestPlan_ResidualInDuplicateKeyAndAfterGarbage covers the two shapes a
// decode-into-a-value check reads as clean. A duplicate key whose earlier
// value holds the \u-escaped pattern (a map keeps only the last) must be
// found. A file that claims to be JSON and is valid JSON followed by
// garbage is residual whether or not the garbage holds the pattern: no
// partial reading of it is trusted. Both the plan and Scan must agree.
func TestPlan_ResidualInDuplicateKeyAndAfterGarbage(t *testing.T) {
	root := t.TempDir()
	E := redEscSecret()

	// An op whose payload repeats "assignee": the first value holds the
	// pattern, the second (the one json.Unmarshal keeps) does not.
	claim := redEnv(t, redIssue, "claim", redWall, 0, redNode, `{"assignee":"alice"}`)
	body := strings.Replace(string(redMarshal(t, claim)), `"assignee":"alice"`, `"assignee":"`+E+`","assignee":"alice"`, 1)
	if env, err := op.UnmarshalTolerant([]byte(body)); err != nil || !strings.Contains(body, E) {
		t.Fatalf("fixture: duplicate-key op does not parse or lacks the pattern: %v", err)
	} else if strings.Contains(string(env.Payload), redSecret) {
		t.Fatalf("fixture: decoded payload holds the pattern; the duplicate is not the earlier one")
	}
	dupRel := redWriteAt(t, root,
		filepath.Join(op.ShardDir(filepath.Join(root, "ops"), redIssue, redWall), op.Filename(claim)), []byte(body))
	// The same in a plain JSON file.
	dupCfgRel := redWriteAt(t, root, filepath.Join(root, "config.json"), []byte(`{"label":"`+E+`","label":"x"}`))
	// Valid JSON, then garbage: with the escaped pattern in the garbage and
	// without anything in it, in a JSON-lines file and in a quarantined
	// .json that act cannot parse.
	withHit := []byte(`{"ok":true} @@not json@@ "note: ` + E + `" }`)
	clean := []byte(`{"ok":true} @@not json@@ "nothing here" }`)
	garbageRel := redWriteAt(t, root, filepath.Join(root, ".sync-log"), withHit)
	cleanGarbageRel := redWriteAt(t, root, filepath.Join(root, ".pending-pushes"), clean)
	qRel := redWriteAt(t, root, filepath.Join(root, ".collisions", "s", "bad.json"), clean)
	// Files that do not claim to be JSON and hold nothing stay clean: a
	// hook script, an empty lock file, a well-formed JSON-lines log.
	redWriteAt(t, root, filepath.Join(root, "hooks", "close.sample"), []byte("#!/bin/sh\necho \"closed $1\"\n"))
	redWriteAt(t, root, filepath.Join(root, ".write.lock"), nil)
	redWriteAt(t, root, filepath.Join(root, ".slow-writes"), []byte(`{"a":1}`+"\n"+`{"b":"x"}`+"\n"))

	want := []string{dupRel, dupCfgRel, garbageRel, cleanGarbageRel, qRel}
	sort.Strings(want)
	if p := redPlan(t, root, []string{redSecret}, nil); !reflect.DeepEqual(p.Residuals, want) {
		t.Errorf("Plan residuals = %v, want %v", p.Residuals, want)
	}
	if paths, err := Scan(root, []string{redSecret}); err != nil || !reflect.DeepEqual(paths, want) {
		t.Errorf("Scan = %v, %v; want %v", paths, err, want)
	}
}

func TestHolds_FailClosedUnion(t *testing.T) {
	E := redEscSecret()
	pats := []string{redSecret}
	forms := rawForms(pats)
	for _, tc := range []struct {
		name string
		file string
		in   string
		want bool
	}{
		{"raw bytes", "notes.txt", "x " + redSecret + " y", true},
		{"escaped value", "a.json", `{"a":"` + E + `"}`, true},
		{"escaped key", "a.json", `{"` + E + `":1}`, true},
		{"earlier duplicate key", "a.json", `{"a":"` + E + `","a":"b"}`, true},
		{"value stream", ".sync-log", `{"a":1}` + "\n" + `["` + E + `"]`, true},
		{"clean json", "a.json", `{"a":"b","c":["d"]}`, false},
		{"clean json-lines", ".sync-log", `{"a":1}` + "\n" + `{"b":2}` + "\n", false},

		// Claims to be JSON and does not decode cleanly: residual whatever
		// it holds.
		{"garbage after json, with hit", ".sync-log", `{"a":1} }}} "` + E + `"`, true},
		{"garbage after json, clean", ".sync-log", `{"a":1} }}} "nothing here"`, true},
		{"truncated object", "x", `{"a":["x"`, true},
		{"truncated, .json name", "a.json", `{"a":`, true},
		{"not json at all, .json name", "a.json", "hello", true},

		// Does not claim to be JSON: raw and token checks only.
		{"shell script", "hooks/close", "#!/bin/sh\necho \"done\"\n", false},
		{"binary", "index.db", "SQLite format 3\x00\x01\"" + E + "\"\xff", false},
		{"empty", ".write.lock", "", false},
	} {
		if got := holds(tc.file, []byte(tc.in), pats, forms); got != tc.want {
			t.Errorf("%s: holds(%q, %q) = %v, want %v", tc.name, tc.file, tc.in, got, tc.want)
		}
	}
}

func TestPlan_QuarantinedFiles(t *testing.T) {
	root := t.TempDir()
	// A stranded op with a hit and no copy under ops/: re-issued in place.
	stranded := redEnv(t, "act-bbbb", "update_field", redWall, 0, redNode,
		`{"field":"title","value":"`+redSecret+`"}`)
	strandedRel := redWriteAt(t, root,
		filepath.Join(root, ".failed-ops", "20261128T194100Z", "ops", "act-bbbb", "2025-11", op.Filename(stranded)),
		redMarshal(t, stranded))
	// A pre-redaction copy of an op whose ops/ slot is already redacted.
	orig := redCreate(t, redSecret)
	redWriteOp(t, root, redReissued(t, orig, []string{redSecret}, 1))
	copyRel := redWriteAt(t, root,
		filepath.Join(root, ".collisions", "20261128T194100Z", "ops", redIssue, "2025-11", op.Filename(orig)),
		redMarshal(t, orig))
	// A quarantined file that is not an op act can read: scanned raw.
	junkRel := redWriteAt(t, root, filepath.Join(root, ".collisions", "x", "junk.json"), []byte(`{"`+redSecret+`"`))

	p := redPlan(t, root, []string{redSecret}, nil)
	if want := []string{junkRel}; !reflect.DeepEqual(p.Residuals, want) {
		t.Errorf("Residuals = %v, want %v", p.Residuals, want)
	}
	if p.Totals.QuarantinedRewritten != 1 || p.Totals.LeftoversRemoved != 1 {
		t.Errorf("Totals = %+v, want 1 quarantined rewritten and 1 leftover removed", p.Totals)
	}
	if got := p.TouchedIssues(); len(got) != 0 {
		t.Errorf("TouchedIssues = %v, want none: quarantine is outside ops/", got)
	}
	redExecute(t, p)
	for _, rel := range []string{strandedRel, copyRel} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s still present", rel)
		}
	}
	q := redFiles(t, root, ".failed-ops")
	if len(q) != 1 || redDir(q[0]) != redDir(strandedRel) || Generation(redRead(t, root, q[0])) != 1 {
		t.Errorf(".failed-ops after = %v, want one re-issued file beside the old one's path", q)
	}
	paths, _ := Scan(root, []string{redSecret})
	if !reflect.DeepEqual(paths, []string{junkRel}) {
		t.Errorf("Scan after = %v, want only the unreadable file", paths)
	}
}

func TestExecute_WritesBeforeDeleting(t *testing.T) {
	root := t.TempDir()
	createRel := redWriteOp(t, root, redCreate(t, redSecret))
	updateRel := redWriteOp(t, root, redDescUpdate(t, redSecret))
	p := redPlan(t, root, []string{redSecret}, nil)
	if p.Totals != (Counts{OpsRewritten: 2, Replacements: 3}) {
		t.Fatalf("Totals = %+v, want 2 ops / 3 replacements", p.Totals)
	}

	// Fail every delete, and at the first one record what is on disk.
	var atFirstDelete []string
	orig := removeFile
	removeFile = func(name string) error {
		if atFirstDelete == nil {
			atFirstDelete = redFiles(t, root, "ops")
		}
		return errors.New("injected delete failure")
	}
	t.Cleanup(func() { removeFile = orig })

	if _, _, err := p.Execute(); err == nil {
		t.Fatal("Execute with failing deletes = nil error, want error")
	}
	if len(atFirstDelete) != 4 {
		t.Fatalf("files when the first delete ran = %v, want both originals and both re-issued files", atFirstDelete)
	}
	// The tree, old and new side by side, folds redacted.
	st := redFoldLive(t, root)[redIssue]
	if st == nil {
		t.Fatal("issue missing from fold")
	}
	if title, desc := st.Fields["title"], st.Fields["description"]; title != "Statement for [redacted]" ||
		desc != "Wire from account [redacted] to savings." {
		t.Errorf("fold renders title %q, description %q; want both redacted", title, desc)
	}
	for _, rel := range []string{createRel, updateRel} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			t.Errorf("original %s: %v, want still present (the delete failed)", rel, err)
		}
	}
}

func TestExecute_InterruptedPassIsFinishedByTheNext(t *testing.T) {
	root := t.TempDir()
	redWriteOp(t, root, redCreate(t, redSecret))
	redWriteOp(t, root, redDescUpdate(t, redSecret))

	orig := removeFile
	removeFile = func(string) error { return errors.New("killed") }
	if _, _, err := redPlan(t, root, []string{redSecret}, nil).Execute(); err == nil {
		removeFile = orig
		t.Fatal("first pass: want the injected failure")
	}
	removeFile = orig

	// A fresh run (no own map: a new process) finds the originals as dead
	// slot members and removes them; it re-issues nothing.
	p := redPlan(t, root, []string{redSecret}, nil)
	if want := (Counts{LeftoversRemoved: 2}); p.Totals != want {
		t.Errorf("second pass Totals = %+v, want %+v", p.Totals, want)
	}
	if got := p.TouchedIssues(); !reflect.DeepEqual(got, []string{redIssue}) {
		t.Errorf("second pass TouchedIssues = %v, want [%s]", got, redIssue)
	}
	touched, perIssue, err := p.Execute()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(touched, []string{redIssue}) || perIssue[redIssue].LeftoversRemoved != 2 {
		t.Errorf("Execute = %v, %+v", touched, perIssue)
	}
	if paths, err := Scan(root, []string{redSecret}); err != nil || len(paths) != 0 {
		t.Errorf("Scan after second pass = %v, %v; want clean", paths, err)
	}
	if files := redFiles(t, root, "ops"); len(files) != 2 {
		t.Errorf("ops files = %v, want the two re-issued files", files)
	}
	// And a third pass is a no-op.
	if p3 := redPlan(t, root, []string{redSecret}, nil); p3.Changes() != 0 || p3.Totals != (Counts{}) {
		t.Errorf("third pass: %d changes, %+v; want none", p3.Changes(), p3.Totals)
	}
}

func TestExecute_RefusesADifferentFileAtTheTargetName(t *testing.T) {
	root := t.TempDir()
	env := redCreate(t, redSecret)
	redWriteOp(t, root, env)
	p := redPlan(t, root, []string{redSecret}, nil)
	target := filepath.Join(root, filepath.FromSlash(p.writes[0].rel))
	if err := os.WriteFile(target, []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Execute(); err == nil {
		t.Fatal("Execute over a different file at the target = nil error, want error")
	}
}

func TestNewPlan_UnparseableOpAborts(t *testing.T) {
	root := t.TempDir()
	redWriteAt(t, root, filepath.Join(root, "ops", redIssue, "2025-11", "bad.json"), []byte("{not json"))
	if _, err := NewPlan(root, []string{redSecret}, nil); err == nil {
		t.Fatal("NewPlan over an unparseable op = nil error, want error")
	}
}

// TestDocClaim_Redaction_ReissuedOpCarriesGenerationUnderNewName asserts the
// spec's "Re-issued ops" paragraph and the "Op file naming" rule for them,
// on files a plan actually writes: the envelope is unchanged, the payload
// gains "redacted": N, the file takes the name its own hash gives in the
// same directory with the same <iso8601> and <op_type>, and the old file is
// deleted. A second redaction of the same op goes to generation 2.
func TestDocClaim_Redaction_ReissuedOpCarriesGenerationUnderNewName(t *testing.T) {
	root := t.TempDir()
	const second = "SECOND-SECRET-55"
	orig := redEnv(t, redIssue, "update_field", redWall, 4, redNode,
		`{"field":"description","value":"`+redSecret+` and `+second+`"}`)
	oldRel := redWriteOp(t, root, orig)

	for _, step := range []struct {
		pattern string
		gen     int
	}{{redSecret, 1}, {second, 2}} {
		redExecute(t, redPlan(t, root, []string{step.pattern}, nil))
		files := redFiles(t, root, "ops")
		if len(files) != 1 {
			t.Fatalf("gen %d: ops files %v, want one", step.gen, files)
		}
		newRel := files[0]
		if newRel == oldRel {
			t.Fatalf("gen %d: file kept its old name %s", step.gen, oldRel)
		}
		if redDir(newRel) != redDir(oldRel) {
			t.Errorf("gen %d: written in %s, want %s", step.gen, redDir(newRel), redDir(oldRel))
		}
		env := redRead(t, root, newRel)
		if filepath.Base(newRel) != op.Filename(env) {
			t.Errorf("gen %d: name %s, want the name its own hash gives, %s", step.gen, filepath.Base(newRel), op.Filename(env))
		}
		ts, _, opType, err := op.Parse(newRel)
		if err != nil || !ts.Equal(time.UnixMilli(redWall).UTC()) || opType != "update_field" {
			t.Errorf("gen %d: Parse(%s) = %v, %q, %v; want the original time and op type", step.gen, newRel, ts, opType, err)
		}
		if Generation(env) != step.gen {
			t.Errorf("gen %d: payload %s carries generation %d", step.gen, env.Payload, Generation(env))
		}
		if env.OpVersion != orig.OpVersion || env.SchemaVersion != orig.SchemaVersion ||
			env.WriterVersion != orig.WriterVersion || env.OpType != orig.OpType ||
			env.IssueID != orig.IssueID || env.NodeID != orig.NodeID || env.HLC != orig.HLC {
			t.Errorf("gen %d: envelope changed: %+v", step.gen, env)
		}
		if err := op.ValidatePayload(env.OpType, env.Payload); err != nil {
			t.Errorf("gen %d: re-issued payload no longer validates: %v", step.gen, err)
		}
		oldRel = newRel
	}
}

func redContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
