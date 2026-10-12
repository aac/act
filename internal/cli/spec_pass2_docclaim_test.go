package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/op"
)

// Doc-claim cover for the second stale pass over docs/spec.md (act-549a7e).
// Each test asserts a spec sentence that pass reworded, at the boundary a
// reader of the spec relies on: what is on disk, and what the read commands
// answer.

// opFilesUnder returns every op file path under <root>/.act/ops.
func opFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	_ = filepath.Walk(filepath.Join(root, ".act", "ops"), func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() && strings.HasSuffix(path, ".json") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// TestDocClaim_CreateHookRunsBeforeOpFileIsWritten asserts the `act create`
// Behavior line: the create hook runs before the create op file is written,
// and under --no-commit the op is written with no hook run. The hook records
// what the op log looks like from outside at the moment it runs — on a fresh
// tracker that must be nothing at all. The close-side twin is
// TestDocClaim_HookRunsBeforeOpFileIsWritten.
func TestDocClaim_CreateHookRunsBeforeOpFileIsWritten(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook execution is POSIX-only in v0.1")
	}
	root := makeCreateRepo(t)
	probe := filepath.Join(t.TempDir(), "seen.txt")

	hookDir := filepath.Join(root, ".act", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	hook := "#!/bin/sh\n" +
		"find \"$ACT_STATE_PATH/ops\" -name '*.json' > " + probe + " 2>/dev/null\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(hookDir, "create"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}

	// --no-commit first, while the tracker is still empty: the op lands and
	// the hook does not run.
	if out, code := RunCreate(root, CreateOptions{Title: "no-commit probe", Type: "task", NoCommit: true}); code != 0 {
		t.Fatalf("create --no-commit: code = %d, out=%+v", code, out)
	}
	if _, err := os.Stat(probe); err == nil {
		t.Fatalf("the create hook ran under --no-commit; the spec says no hook runs there")
	}
	seeded := opFilesUnder(t, root)
	if len(seeded) != 1 {
		t.Fatalf("create --no-commit wrote %d op files, want 1: %v", len(seeded), seeded)
	}

	// A committed create: the hook runs, and sees only the op that was
	// already there — not the one being created.
	out, code := RunCreate(root, CreateOptions{Title: "hooked probe", Type: "task"})
	if code != 0 {
		t.Fatalf("create with passing hook: code = %d, out=%+v", code, out)
	}
	id := out.(CreateResult).ID
	seen, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("create hook did not run (no probe file): %v", err)
	}
	if strings.Contains(string(seen), id) {
		t.Errorf("the create op file was visible under ops/ while the create hook ran:\n%s\n"+
			"the hook must run before the op file is written (spec `act create` Behavior)", seen)
	}
	if got := strings.Fields(string(seen)); len(got) != 1 {
		t.Errorf("hook saw %d op files, want exactly the 1 pre-existing one: %v", len(got), got)
	}

	// And the create still landed.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-create.json"))
	if len(matches) != 1 {
		t.Errorf("after a passing hook: %d create op files for %s, want 1", len(matches), id)
	}
}

// TestDocClaim_DepAdd_WritesOneAddDepOp asserts the `act dep add` Behavior
// line: the command writes one op, on the child issue, and its type is
// `add_dep` (the spec used to call it `dep-add`, a name no op carries).
func TestDocClaim_DepAdd_WritesOneAddDepOp(t *testing.T) {
	root := makeCreateRepo(t)
	mk := func(title string) string {
		out, code := RunCreate(root, CreateOptions{Title: title, Type: "task"})
		if code != 0 {
			t.Fatalf("seed %q: code = %d, out=%+v", title, code, out)
		}
		return out.(CreateResult).ID
	}
	child, parent := mk("child"), mk("parent")

	before := map[string]bool{}
	for _, f := range opFilesUnder(t, root) {
		before[f] = true
	}
	if out, code := RunDepAdd(root, DepAddOptions{Child: child, Parent: parent}); code != 0 {
		t.Fatalf("dep add: code = %d, out=%+v", code, out)
	}
	var added []string
	for _, f := range opFilesUnder(t, root) {
		if !before[f] {
			added = append(added, f)
		}
	}
	if len(added) != 1 {
		t.Fatalf("dep add wrote %d op files, want 1: %v", len(added), added)
	}
	body, err := os.ReadFile(added[0])
	if err != nil {
		t.Fatalf("read %s: %v", added[0], err)
	}
	env, err := op.Unmarshal(body)
	if err != nil {
		t.Fatalf("op.Unmarshal %s: %v", added[0], err)
	}
	if env.OpType != "add_dep" {
		t.Errorf("dep add wrote op_type %q, want add_dep", env.OpType)
	}
	if env.IssueID != child {
		t.Errorf("the add_dep op is on %s, want the child %s", env.IssueID, child)
	}
	var pl op.AddDepPayload
	if err := json.Unmarshal(env.Payload, &pl); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if pl.Parent != parent || pl.EdgeType != "blocks" {
		t.Errorf("payload = %+v, want parent=%s edge_type=blocks", pl, parent)
	}
}

// seedCreateWithVersions writes a create op for id whose schema_version and
// writer_version are the given values, straight into the op log the way
// another act binary sharing the tracker would land it.
func seedCreateWithVersions(t *testing.T, root, id, title string, wallMs int64, schema int, writer string) string {
	t.Helper()
	env := makeCreateEnv(t, id, wallMs, 0, title, "task", 2)
	env.SchemaVersion = schema
	env.WriterVersion = writer
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", id, "2026-04")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	name := id + "-create.json"
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return name
}

// TestDocClaim_Fold_VersionFieldsOnRead asserts the spec's "Version fields on
// read" paragraph, which replaced two error names the code never had
// (E_SCHEMA_TOO_NEW, E_UPGRADE_REQUIRED):
//
//   - an op whose schema_version differs from this binary's — newer OR older —
//     aborts the fold: list and ready exit 1, and show exits 3 on the issue
//     carrying the op while still answering for the others;
//   - a newer writer_version does not stop the fold and the op is applied;
//     the skew is reported by `act version --check-repo` (exit 4).
func TestDocClaim_Fold_VersionFieldsOnRead(t *testing.T) {
	for _, schema := range []int{op.CurrentSchemaVersion + 1, op.CurrentSchemaVersion - 1} {
		root := listTestRepo(t)
		name := seedCreateWithVersions(t, root, "act-dddd", "delta", 1700000030000, schema, op.WriterVersion)

		out, code := RunList(root, ListOptions{Limit: 200})
		if code != 1 {
			t.Fatalf("schema_version %d: list exit = %d, want 1; out=%+v", schema, code, out)
		}
		le, ok := out.(ListErrorOutput)
		if !ok {
			t.Fatalf("schema_version %d: list output type = %T, want ListErrorOutput", schema, out)
		}
		if !strings.Contains(le.Message, "schema_version") || !strings.Contains(le.Message, name) {
			t.Errorf("schema_version %d: list error %q does not name both the field and the op file %s", schema, le.Message, name)
		}
		// show folds one issue's ops, so it fails only on the issue that
		// carries the op and still answers for the others.
		sout, scode := RunShow(root, ShowOptions{ID: "act-dddd", AsJSON: true})
		se, _ := sout.(ShowErrorOutput)
		if scode != 3 || !strings.Contains(se.Message, "schema_version") {
			t.Errorf("schema_version %d: show of the issue carrying the op: exit = %d, out=%+v; want 3 naming schema_version", schema, scode, sout)
		}
		if out, code := RunShow(root, ShowOptions{ID: "act-aaaa", AsJSON: true}); code != 0 {
			t.Errorf("schema_version %d: show of an unaffected issue: exit = %d, want 0; out=%+v", schema, code, out)
		}
		if out, code := RunReady(root, ReadyOptions{}); code != 1 {
			t.Errorf("schema_version %d: ready exit = %d, want 1; out=%+v", schema, code, out)
		}
	}

	// A newer writer_version: the fold carries on and applies the op.
	root := listTestRepo(t)
	seedCreateWithVersions(t, root, "act-dddd", "delta from a newer writer", 1700000030000, op.CurrentSchemaVersion, "99.0.0")
	ids := listIDs(t, root)
	if len(ids) != 4 || ids["act-dddd"] != "open" {
		t.Fatalf("list with a newer-writer op = %v, want 4 issues with act-dddd open (the op is applied)", ids)
	}
	if got := listTitle(t, root, "act-dddd"); got != "delta from a newer writer" {
		t.Errorf("title of the newer-writer issue = %q; the op must be applied like any other", got)
	}
	if out, code := RunShow(root, ShowOptions{ID: "act-dddd", AsJSON: true}); code != 0 {
		t.Errorf("show of the newer-writer issue: exit = %d, out=%+v", code, out)
	}
	if out, code := RunReady(root, ReadyOptions{}); code != 0 {
		t.Errorf("ready with a newer-writer op: exit = %d, out=%+v", code, out)
	}
	out, code := RunVersion(true, root)
	m, _ := out.(map[string]any)
	if code != 4 || m["error"] != "version_skew" {
		t.Errorf("version --check-repo with a newer-writer op: exit = %d, out=%+v; want 4 / version_skew", code, out)
	}
}

// TestDocClaim_Spec_GoldenCaseListMatchesFixtures asserts the spec's "Golden
// cases:" block names exactly the case directories that exist under
// internal/fold/testdata/golden — the block used to require thirteen cases
// (`dep-add`, `compact`, `update-status`, …) under a directory that was never
// created. Both sides are printed on a mismatch.
func TestDocClaim_Spec_GoldenCaseListMatchesFixtures(t *testing.T) {
	root := repoRootForDocClaim(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	const header = "Golden cases:"
	_, after, ok := strings.Cut(string(body), header)
	if !ok {
		t.Fatalf("docs/spec.md has no %q block", header)
	}
	parts := strings.SplitN(after, "```", 3)
	if len(parts) < 3 {
		t.Fatalf("no fenced block follows %q in docs/spec.md", header)
	}
	inSpec := regexp.MustCompile(`[a-z_]+(?:-[a-z]+)?`).FindAllString(parts[1], -1)
	sort.Strings(inSpec)

	entries, err := os.ReadDir(filepath.Join(root, "internal", "fold", "testdata", "golden"))
	if err != nil {
		t.Fatalf("read golden dir: %v", err)
	}
	var onDisk []string
	for _, e := range entries {
		if e.IsDir() {
			onDisk = append(onDisk, e.Name())
		}
	}
	sort.Strings(onDisk)
	if len(onDisk) == 0 {
		t.Fatalf("no golden case directories found")
	}
	if strings.Join(inSpec, ",") != strings.Join(onDisk, ",") {
		t.Fatalf("docs/spec.md golden case list does not match internal/fold/testdata/golden\n  spec: %v\n  disk: %v", inSpec, onDisk)
	}
}
