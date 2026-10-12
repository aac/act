package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/compact"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// Doc-claim cover for the fourth stale pass over docs/spec.md (act-7b9c54):
// the version_skew error row, the compaction note and commit subject, the
// fold's dispatch, the index schema block and `act migrate`. The doctor
// exit-code line and the migrate flag surface are asserted at the binary
// boundary in cmd/act/spec_pass4_docclaim_test.go.

// pass4Spec returns docs/spec.md.
func pass4Spec(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRootForDocClaim(t), "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	return string(body)
}

// pass4SeedUpdates writes n update_field ops for id straight into the op log,
// stamped after baseWall, so an issue can be pushed past the compaction
// trigger without n real writes.
func pass4SeedUpdates(t *testing.T, root, id string, n int, baseWall int64) {
	t.Helper()
	rootOps := filepath.Join(root, ".act", "ops")
	for i := 0; i < n; i++ {
		pb, err := json.Marshal(op.UpdateFieldPayload{Field: "title", Value: json.RawMessage(fmt.Sprintf(`"t-%d"`, i))})
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		h := hlc.HLC{Wall: baseWall + int64(i+1), Logical: 0, NodeID: "11111111"}
		env := op.Envelope{
			OpVersion:     op.CurrentOpVersion,
			SchemaVersion: op.CurrentSchemaVersion,
			WriterVersion: op.WriterVersion,
			OpType:        "update_field",
			IssueID:       id,
			Payload:       pb,
			HLC:           h,
			NodeID:        h.NodeID,
		}
		body, err := env.Marshal()
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		dir := op.ShardDir(rootOps, id, h.Wall)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, op.Filename(env)), body, 0o644); err != nil {
			t.Fatalf("write op: %v", err)
		}
	}
}

// pass4Committer records the commit subjects compact.Run asks for.
type pass4Committer struct{ msgs []string }

func (c *pass4Committer) Commit(msg string) error {
	c.msgs = append(c.msgs, msg)
	return nil
}

// TestDocClaim_Spec_VersionSkewRowMatchesCheckRepo asserts the error-table
// row for `version_skew`: its exit code, message template and details keys
// are the ones `act version --check-repo` really answers with. The row used
// to give a message and three details keys (op_writer_version, binary_max,
// op_path) that no code path emits.
func TestDocClaim_Spec_VersionSkewRowMatchesCheckRepo(t *testing.T) {
	var cells []string
	for _, line := range strings.Split(pass4Spec(t), "\n") {
		if strings.HasPrefix(line, "| `version_skew`") {
			for _, c := range strings.Split(strings.Trim(line, "|"), "|") {
				cells = append(cells, strings.TrimSpace(c))
			}
		}
	}
	if len(cells) != 4 {
		t.Fatalf("docs/spec.md: want exactly one 4-cell `version_skew` error-table row, got cells %q", cells)
	}
	wantExit, tmpl := cells[1], strings.Trim(cells[2], "`")
	var wantKeys []string
	for _, m := range regexp.MustCompile("`([a-z_]+)`").FindAllStringSubmatch(cells[3], -1) {
		wantKeys = append(wantKeys, m[1])
	}
	sort.Strings(wantKeys)

	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	seedCreateWithOpVersion(t, root, "act-dddd", 1700000030000, op.CurrentOpVersion, "99.0.0")
	out, code := RunVersion(true, root)
	m, ok := out.(map[string]any)
	if !ok || m["error"] != ErrVersionSkew {
		t.Fatalf("version --check-repo with a 99.0.0 op: out=%+v; want a version_skew envelope", out)
	}
	if fmt.Sprint(code) != wantExit {
		t.Errorf("exit = %d, spec row says %s", code, wantExit)
	}

	// <N> and <M> are the two versions; everything else is literal.
	pat := regexp.QuoteMeta(tmpl)
	pat = strings.Replace(pat, "<N>", `99\.0\.0`, 1)
	pat = strings.Replace(pat, "<M>", regexp.QuoteMeta(op.WriterVersion), 1)
	msg, _ := m["message"].(string)
	if !regexp.MustCompile("^" + pat + "$").MatchString(msg) {
		t.Errorf("message does not match the spec row\n  spec template: %s\n  code message:  %s", tmpl, msg)
	}

	details, _ := m["details"].(map[string]any)
	var gotKeys []string
	for k := range details {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if strings.Join(gotKeys, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("details keys differ\n  spec row: %v\n  code:     %v", wantKeys, gotKeys)
	}
}

// TestDocClaim_Compaction_NotWiredAndEngineCommitSubject asserts the note at
// the top of the Compaction section and its step 6. A write to an issue that
// is past the trigger compacts nothing — no snapshot, no compact op — and
// `compact` is not an op type; the engine, called directly on the same
// tracker, does compact it and asks for one commit with subject
// `act-compact: <N> issues` (which also proves the fixture was past the
// trigger, so the first half is not vacuous).
func TestDocClaim_Compaction_NotWiredAndEngineCommitSubject(t *testing.T) {
	root := makeCreateRepo(t)
	id := mustCreate(t, root, "A")
	pass4SeedUpdates(t, root, id, compact.OpCountTrigger+5, 4102444800000) // 2100-01-01: after the create
	snapshots := filepath.Join(root, ".act", "snapshots", "*.json")

	prio := 0
	if out, code := RunUpdate(root, UpdateOptions{ID: id, Priority: &prio}); code != 0 {
		t.Fatalf("update on an issue with %d ops: exit = %d, out=%+v", compact.OpCountTrigger+6, code, out)
	}
	if n := len(opFilesUnder(t, root)); n <= compact.OpCountTrigger {
		t.Fatalf("fixture has %d op files, want more than the trigger of %d", n, compact.OpCountTrigger)
	}
	if got, _ := filepath.Glob(snapshots); len(got) != 0 {
		t.Errorf("a write past the trigger wrote snapshots %v; the spec says a running act never compacts", got)
	}
	for _, f := range opFilesUnder(t, root) {
		if strings.HasSuffix(f, "-compact.json") {
			t.Errorf("a write past the trigger wrote a compact op %s", f)
		}
	}
	for _, s := range actRepoSubjects(t, root) {
		if strings.HasPrefix(s, "act-compact") {
			t.Errorf("a write past the trigger made a compaction commit %q", s)
		}
	}
	if op.ValidOpTypes["compact"] {
		t.Errorf("`compact` is in op.ValidOpTypes; the spec says it is not in the op_type enum")
	}

	// No non-test code outside the engine imports it.
	repo := repoRootForDocClaim(t)
	for _, dir := range []string{"cmd", "internal"} {
		_ = filepath.Walk(filepath.Join(repo, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			if filepath.Base(filepath.Dir(path)) == "compact" {
				return nil
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("read %s: %v", path, rerr)
			}
			if strings.Contains(string(src), `"github.com/aac/act/internal/compact"`) {
				t.Errorf("%s imports internal/compact; the spec says nothing calls the engine", path)
			}
			return nil
		})
	}

	// The engine itself: one commit for the run, subject act-compact: <N> issues.
	fc := &pass4Committer{}
	res, err := compact.Run(root, compact.Options{}, fc)
	if err != nil {
		t.Fatalf("compact.Run: %v", err)
	}
	if res.CompactedIssues != 1 {
		t.Fatalf("compact.Run compacted %d issues, want 1 (the fixture is past the trigger)", res.CompactedIssues)
	}
	if len(fc.msgs) != 1 || fc.msgs[0] != "act-compact: 1 issues" {
		t.Errorf("engine commit subjects = %q, want exactly [\"act-compact: 1 issues\"]", fc.msgs)
	}
	if got, _ := filepath.Glob(snapshots); len(got) != 1 {
		t.Errorf("engine wrote snapshots %v, want one", got)
	}
}

// TestDocClaim_Fold_DispatchIsByOpTypeOnly asserts the "Fold dispatch"
// paragraph: fold.ApplyDispatch has an apply function for every op type
// except the parse-only `redact` and returns nothing for an unknown type; the
// version-keyed registry exists with version 1 registered, but the fold does
// not go through it — replacing its version-1 entry with one that always
// fails changes nothing a read returns. The spec used to call the dispatch
// "a registry keyed by (op_type, op_version)".
func TestDocClaim_Fold_DispatchIsByOpTypeOnly(t *testing.T) {
	for name := range op.ValidOpTypes {
		got := fold.ApplyDispatch(name) != nil
		if want := name != "redact"; got != want {
			t.Errorf("fold.ApplyDispatch(%q) has an apply function = %v, want %v", name, got, want)
		}
	}
	if fold.ApplyDispatch("pass4_no_such_type") != nil {
		t.Errorf("fold.ApplyDispatch returned an apply function for an unknown op type")
	}

	saved, err := op.DispatchByVersion(op.CurrentOpVersion)
	if err != nil || saved == nil {
		t.Fatalf("op.DispatchByVersion(%d): %v; the spec says version %d is registered", op.CurrentOpVersion, err, op.CurrentOpVersion)
	}
	if _, err := op.DispatchByVersion(op.CurrentOpVersion + 1); err == nil {
		t.Errorf("op.DispatchByVersion(%d) succeeded; only version %d should be registered", op.CurrentOpVersion+1, op.CurrentOpVersion)
	}

	root := listTestRepo(t)
	before, code := RunList(root, ListOptions{Limit: 200})
	if code != 0 {
		t.Fatalf("list: exit = %d, out=%+v", code, before)
	}
	op.RegisterOpVersion(op.CurrentOpVersion, func(any, op.Envelope, []byte, string) error {
		return fmt.Errorf("pass4: the version registry was called")
	})
	t.Cleanup(func() { op.RegisterOpVersion(op.CurrentOpVersion, saved) })
	// A second, empty-index repo so the read has to fold rather than serve a
	// cached index.
	root2 := listTestRepo(t)
	after, code := RunList(root2, ListOptions{Limit: 200})
	if code != 0 {
		t.Fatalf("list with a failing version-registry entry: exit = %d, out=%+v; the fold must not dispatch on op_version", code, after)
	}
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Errorf("list changed when the version registry was replaced\n  before: %s\n  after:  %s", b, a)
	}
}

// pass4SchemaShape is what a SQLite database holds, read back through
// sqlite_master and the table/index pragmas rather than compared as SQL text.
type pass4SchemaShape struct {
	objects []string            // "type name on tbl_name", sorted
	columns map[string][]string // table -> "name type notnull=N default=D pk=N"
	indexed map[string][]string // index -> indexed column names
	virtual map[string]string   // virtual table -> whitespace-normalised SQL
}

func pass4ReadSchema(t *testing.T, db *sql.DB) pass4SchemaShape {
	t.Helper()
	s := pass4SchemaShape{columns: map[string][]string{}, indexed: map[string][]string{}, virtual: map[string]string{}}
	rows, err := db.Query(`SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	type obj struct{ typ, name, tbl, sql string }
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.typ, &o.name, &o.tbl, &o.sql); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		objs = append(objs, o)
	}
	rows.Close()
	ws := regexp.MustCompile(`\s+`)
	for _, o := range objs {
		s.objects = append(s.objects, o.typ+" "+o.name+" on "+o.tbl)
		switch {
		case o.typ == "table" && strings.Contains(strings.ToUpper(o.sql), "CREATE VIRTUAL TABLE"):
			norm := strings.Replace(o.sql, "IF NOT EXISTS ", "", 1)
			s.virtual[o.name] = strings.TrimSpace(ws.ReplaceAllString(norm, " "))
		case o.typ == "table":
			cr, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, o.name))
			if err != nil {
				t.Fatalf("table_info(%s): %v", o.name, err)
			}
			for cr.Next() {
				var cid, notnull, pk int
				var name, typ string
				var dflt sql.NullString
				if err := cr.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
					t.Fatalf("scan table_info(%s): %v", o.name, err)
				}
				s.columns[o.name] = append(s.columns[o.name],
					fmt.Sprintf("%s %s notnull=%d default=%s pk=%d", name, typ, notnull, dflt.String, pk))
			}
			cr.Close()
		case o.typ == "index":
			ir, err := db.Query(fmt.Sprintf(`PRAGMA index_info(%s)`, o.name))
			if err != nil {
				t.Fatalf("index_info(%s): %v", o.name, err)
			}
			for ir.Next() {
				var seqno, cid int
				var name sql.NullString
				if err := ir.Scan(&seqno, &cid, &name); err != nil {
					t.Fatalf("scan index_info(%s): %v", o.name, err)
				}
				s.indexed[o.name] = append(s.indexed[o.name], name.String)
			}
			ir.Close()
		}
	}
	return s
}

// TestDocClaim_Spec_IndexSchemaBlockMatchesIndex asserts the
// "`.act/index.db` schema" block: executed as SQL, it builds exactly the
// tables, columns (name, type, NOT NULL, default, primary key), indices and
// full-text table that the index a real `act create` leaves behind has. The
// block used to list an `accept` table with a `done` column, four indices by
// other names and an fts table keyed on `id`, and to omit four tables. Both
// sides are printed on a mismatch.
func TestDocClaim_Spec_IndexSchemaBlockMatchesIndex(t *testing.T) {
	spec := pass4Spec(t)
	const header = "#### `.act/index.db` schema"
	_, after, ok := strings.Cut(spec, header)
	if !ok {
		t.Fatalf("docs/spec.md has no %q heading", header)
	}
	parts := strings.SplitN(after, "```", 3)
	if len(parts) < 3 {
		t.Fatalf("no fenced block follows %q in docs/spec.md", header)
	}
	block := strings.TrimPrefix(parts[1], "sql")

	specDB, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "spec.db"))
	if err != nil {
		t.Fatalf("open scratch db: %v", err)
	}
	defer specDB.Close()
	if _, err := specDB.Exec(block); err != nil {
		t.Fatalf("the spec's index schema block is not runnable SQL: %v\n%s", err, block)
	}
	want := pass4ReadSchema(t, specDB)

	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	idx, err := index.Open(config.Layout(root).IndexDB)
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	defer idx.Close()
	got := pass4ReadSchema(t, idx.DB())
	if len(got.columns["issues"]) == 0 {
		t.Fatalf("the real index has no issues table; the probe is not reading it: %+v", got.objects)
	}

	if strings.Join(want.objects, "\n") != strings.Join(got.objects, "\n") {
		t.Errorf("schema objects differ\n  spec block: %v\n  index.db:   %v", want.objects, got.objects)
	}
	for name, cols := range got.columns {
		if strings.Join(want.columns[name], "\n") != strings.Join(cols, "\n") {
			t.Errorf("table %s differs\n  spec block: %q\n  index.db:   %q", name, want.columns[name], cols)
		}
	}
	for name, cols := range got.indexed {
		if strings.Join(want.indexed[name], ",") != strings.Join(cols, ",") {
			t.Errorf("index %s differs\n  spec block: %v\n  index.db:   %v", name, want.indexed[name], cols)
		}
	}
	for name, sqlText := range got.virtual {
		if want.virtual[name] != sqlText {
			t.Errorf("virtual table %s differs\n  spec block: %s\n  index.db:   %s", name, want.virtual[name], sqlText)
		}
	}

	// The sentence under the block counts what the block declares.
	if len(got.virtual) != 1 || got.virtual["fts"] == "" {
		t.Errorf("index.db virtual tables = %v, want just fts", got.virtual)
	}
	tables := 0
	for name := range got.columns {
		if name != "fts" && !strings.HasPrefix(name, "fts_") {
			tables++
		}
	}
	var indices []string
	for name := range got.indexed {
		indices = append(indices, name)
	}
	sort.Strings(indices)
	if tables != 7 || strings.Join(indices, ",") != "idx_parent,idx_priority,idx_status" {
		t.Errorf("index.db has %d tables and indices %v; the spec says seven tables and idx_status, idx_priority, idx_parent", tables, indices)
	}
}

// TestDocClaim_Migrate_PayloadOutputAndExitCodes asserts the `act migrate`
// section below the flag surface: with no migration registered a well-formed
// call exits 5 `migration_not_found` and writes nothing, bad versions exit 2
// `bad_input`, and a registered migration writes the transformed ops plus
// exactly one `migrate` op per affected issue whose payload is
// {from_version, to_version, transform_id}, reports
// {migrated_issues, wrote_ops}, and leaves what a reader sees unchanged. The
// spec used to give a six-key payload with nested from/to objects.
func TestDocClaim_Migrate_PayloadOutputAndExitCodes(t *testing.T) {
	from, to := op.CurrentOpVersion, op.CurrentOpVersion+1
	if len(op.Migrations) != 0 {
		t.Fatalf("op.Migrations has %d entries; the spec says none is registered in this version", len(op.Migrations))
	}

	root := makeCreateRepo(t)
	a := mustCreate(t, root, "A")
	b := mustCreate(t, root, "B")
	opsBefore := opFilesUnder(t, root)

	out, code := op.RunMigrate(root, from, to)
	if me, ok := out.(op.MigrateError); !ok || code != 5 || me.Error != "migration_not_found" {
		t.Fatalf("migrate with nothing registered: exit = %d, out=%+v; want 5 / migration_not_found", code, out)
	}
	for _, bad := range [][2]int{{0, to}, {from, 0}, {to, from}, {from, from}} {
		out, code := op.RunMigrate(root, bad[0], bad[1])
		if me, ok := out.(op.MigrateError); !ok || code != 2 || me.Error != "bad_input" {
			t.Errorf("migrate from=%d to=%d: exit = %d, out=%+v; want 2 / bad_input", bad[0], bad[1], code, out)
		}
	}
	if got := opFilesUnder(t, root); len(got) != len(opsBefore) {
		t.Fatalf("refused migrate calls wrote op files: %d before, %d after", len(opsBefore), len(got))
	}

	listBefore, code := RunList(root, ListOptions{Limit: 200})
	if code != 0 {
		t.Fatalf("list: exit = %d", code)
	}

	for _, tc := range []struct{ description, wantID string }{
		{"pass4-rename", "pass4-rename"},
		{"", fmt.Sprintf("v%d-to-v%d", from, to)},
	} {
		root := makeCreateRepo(t)
		a := mustCreate(t, root, "A")
		b := mustCreate(t, root, "B")
		before := map[string]bool{}
		for _, f := range opFilesUnder(t, root) {
			before[f] = true
		}

		op.Migrations = []op.Migration{{
			FromVersion: from, ToVersion: to, Description: tc.description,
			// Identity transform: one op in, the same op out.
			Transform: func(env op.Envelope) ([]op.Envelope, error) { return []op.Envelope{env}, nil },
		}}
		out, code := op.RunMigrate(root, from, to)
		op.Migrations = []op.Migration{}
		if code != 0 {
			t.Fatalf("description %q: migrate: exit = %d, out=%+v", tc.description, code, out)
		}
		raw, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("marshal migrate output: %v", err)
		}
		// Two issues, each with one create op: two transformed ops plus two migrate ops.
		if string(raw) != `{"migrated_issues":2,"wrote_ops":4}` {
			t.Errorf("description %q: migrate output = %s, want {\"migrated_issues\":2,\"wrote_ops\":4}", tc.description, raw)
		}

		migrateOps := map[string]int{}
		added := 0
		for _, f := range opFilesUnder(t, root) {
			if before[f] {
				continue
			}
			added++
			if !strings.HasSuffix(f, "-migrate.json") {
				continue
			}
			body, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			var env struct {
				OpType  string         `json:"op_type"`
				IssueID string         `json:"issue_id"`
				Payload map[string]any `json:"payload"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("parse %s: %v", f, err)
			}
			migrateOps[env.IssueID]++
			payload, _ := json.Marshal(env.Payload)
			want := fmt.Sprintf(`{"from_version":%d,"to_version":%d,"transform_id":%q}`, from, to, tc.wantID)
			if env.OpType != "migrate" || string(payload) != want {
				t.Errorf("description %q: migrate op %s has op_type %q payload %s, want payload %s",
					tc.description, filepath.Base(f), env.OpType, payload, want)
			}
		}
		if added != 4 || len(migrateOps) != 2 || migrateOps[a] != 1 || migrateOps[b] != 1 {
			t.Errorf("description %q: %d new op files, migrate ops per issue %v; want 4 new files and one migrate op each for %s and %s",
				tc.description, added, migrateOps, a, b)
		}
	}

	// What a reader sees is unchanged by a migrate op: same two issues.
	op.Migrations = []op.Migration{{
		FromVersion: from, ToVersion: to,
		Transform: func(env op.Envelope) ([]op.Envelope, error) { return []op.Envelope{env}, nil },
	}}
	t.Cleanup(func() { op.Migrations = []op.Migration{} })
	if out, code := op.RunMigrate(root, from, to); code != 0 {
		t.Fatalf("migrate: exit = %d, out=%+v", code, out)
	}
	op.Migrations = []op.Migration{}
	listAfter, code := RunList(root, ListOptions{Limit: 200})
	if code != 0 {
		t.Fatalf("list after migrate: exit = %d, out=%+v", code, listAfter)
	}
	lb, _ := json.Marshal(listBefore)
	la, _ := json.Marshal(listAfter)
	if string(la) != string(lb) || !strings.Contains(string(la), a) || !strings.Contains(string(la), b) {
		t.Errorf("list changed across a migrate\n  before: %s\n  after:  %s", lb, la)
	}
}
