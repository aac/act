package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/fold"
	"github.com/aac/act/internal/op"
)

// makeCreateRepo initializes a git repo with a nested .act/ git repo +
// valid .act/config.json (NodeID = "0123abcd") matching the Phase 1
// two-repo layout (docs/coordination-plane-design.md). It returns the
// absolute host repo root.
//
// We intentionally do NOT call RunInit here so the test fixture is
// minimal — no host pre-commit hook, no CONTRIBUTING.md emission, no
// host-side commit — just enough on-disk shape for ActGitOps to write
// op files into the nested repo and commit them.
func makeCreateRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.email", "u@example.com")
	mustGit(t, dir, "config", "user.name", "U")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	// Phase 1 contract (docs/coordination-plane-design.md): the host
	// repo gitignores `.act/`. Doctor's gitignore-effective probe will
	// error on a test fixture that doesn't satisfy this, so we set up
	// the same shape RunInit would produce.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".act/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	mustGit(t, dir, "add", "README", ".gitignore")
	mustGit(t, dir, "commit", "-q", "--no-verify", "-m", "init")

	paths := config.Layout(dir)
	if err := config.InitDirs(paths); err != nil {
		t.Fatalf("InitDirs: %v", err)
	}
	cfg := config.Config{
		NodeID:    "0123abcd",
		CreatedAt: "2026-04-29T00:00:00.000Z",
		Version:   "0.1.0",
	}
	if err := config.WriteConfig(paths, cfg); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}

	// Phase 1: nested .act/ git repo. Initialize it with the same
	// identity as the host so commits attribute consistently in tests.
	mustGit(t, paths.Root, "init", "-q", "-b", "main")
	mustGit(t, paths.Root, "config", "user.email", "u@example.com")
	mustGit(t, paths.Root, "config", "user.name", "U")
	mustGit(t, paths.Root, "config", "commit.gpgsign", "false")
	// Initial commit so HEAD exists; subsequent op commits attach to it.
	mustGit(t, paths.Root, "add", "-A")
	mustGit(t, paths.Root, "commit", "-q", "--no-verify", "-m", "act: bootstrap nested state")
	return dir
}

var idShape = regexp.MustCompile(`^act-[0-9a-f]{4,16}$`)

func TestRunCreate_HappyPath(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{
		Title:    "fix bug",
		Type:     "bug",
		Priority: intPtr(1),
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(CreateResult)
	if !ok {
		t.Fatalf("output type = %T, want CreateResult", out)
	}
	if !idShape.MatchString(res.ID) {
		t.Fatalf("id %q does not match %s", res.ID, idShape)
	}
	if !strings.HasPrefix(res.ID, "act-") {
		t.Errorf("id %q missing act- prefix", res.ID)
	}
	if res.Title != "fix bug" {
		t.Errorf("title = %q", res.Title)
	}
	if !strings.HasPrefix(res.ID, res.Prefix) {
		t.Errorf("prefix %q is not a prefix of id %q", res.Prefix, res.ID)
	}

	// One op file must be on disk.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", res.ID, "*", "*-create.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 op file, got %d: %v", len(matches), matches)
	}
}

func TestRunCreate_PersistsAllPayloadFields(t *testing.T) {
	root := makeCreateRepo(t)
	// Seed a parent issue first.
	parentOut, code := RunCreate(root, CreateOptions{Title: "parent", Type: "epic"})
	if code != 0 {
		t.Fatalf("seed parent: code = %d", code)
	}
	parent := parentOut.(CreateResult).ID

	out, code := RunCreate(root, CreateOptions{
		Title:       "child",
		Description: "child desc",
		Type:        "task",
		Parent:      parent,
		Accept:      []string{"a", "b"},
		Priority:    intPtr(2),
	})
	if code != 0 {
		t.Fatalf("create child: code = %d, out=%+v", code, out)
	}
	id := out.(CreateResult).ID

	// Read back the op file and inspect the payload.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-create.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 op file, got %d", len(matches))
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read op: %v", err)
	}
	env, err := op.Unmarshal(body)
	if err != nil {
		t.Fatalf("unmarshal env: %v", err)
	}
	var p op.CreatePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Title != "child" {
		t.Errorf("title = %q", p.Title)
	}
	if p.Description != "child desc" {
		t.Errorf("description = %q", p.Description)
	}
	if p.Parent != parent {
		t.Errorf("parent = %q, want %q", p.Parent, parent)
	}
	if len(p.Accept) != 2 || p.Accept[0] != "a" || p.Accept[1] != "b" {
		t.Errorf("accept = %v", p.Accept)
	}
	if p.Type != "task" {
		t.Errorf("type = %q", p.Type)
	}
	if p.Priority == nil || *p.Priority != 2 {
		t.Errorf("priority = %v", p.Priority)
	}
}

func TestRunCreate_AutoCommitsByDefault(t *testing.T) {
	root := makeCreateRepo(t)
	// Phase 1: op commits land in the nested .act/ repo, not the host.
	actDir := filepath.Join(root, ".act")
	headBefore := strings.TrimSpace(runOut(t, actDir, "git", "rev-parse", "HEAD"))

	_, code := RunCreate(root, CreateOptions{Title: "t1", Type: "task"})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	headAfter := strings.TrimSpace(runOut(t, actDir, "git", "rev-parse", "HEAD"))
	if headAfter == headBefore {
		t.Fatalf("expected new commit; nested HEAD unchanged %s", headAfter)
	}
	subj := strings.TrimSpace(runOut(t, actDir, "git", "log", "-1", "--format=%s"))
	if !strings.HasPrefix(subj, "act-op: ") || !strings.HasSuffix(subj, " create") {
		t.Errorf("subject = %q", subj)
	}
}

func TestRunCreate_NoCommit(t *testing.T) {
	root := makeCreateRepo(t)
	actDir := filepath.Join(root, ".act")
	headBefore := strings.TrimSpace(runOut(t, actDir, "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{Title: "t1", Type: "task", NoCommit: true})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	headAfter := strings.TrimSpace(runOut(t, actDir, "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Fatalf("expected no commit; nested HEAD %s -> %s", headBefore, headAfter)
	}
	id := out.(CreateResult).ID
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-create.json"))
	if len(matches) != 1 {
		t.Errorf("expected op file written; got %d", len(matches))
	}
}

func TestRunCreate_ClosedParentWarning(t *testing.T) {
	root := makeCreateRepo(t)
	// Seed a parent.
	parentOut, code := RunCreate(root, CreateOptions{Title: "parent", Type: "task"})
	if code != 0 {
		t.Fatalf("seed parent: %d", code)
	}
	parentID := parentOut.(CreateResult).ID

	// Hand-write a close op for the parent so the index reports
	// status=closed. We mirror the canonical envelope fields used by
	// other tests in this file.
	writeCloseOp(t, root, parentID)

	out, code := RunCreate(root, CreateOptions{
		Title:  "child",
		Type:   "task",
		Parent: parentID,
		AsJSON: true,
	})
	if code != 0 {
		t.Fatalf("create child: code = %d, out=%+v", code, out)
	}
	res, ok := out.(CreateResult)
	if !ok {
		t.Fatalf("type %T", out)
	}
	hasWarn := false
	for _, w := range res.Warnings {
		if w == "parent_closed" {
			hasWarn = true
		}
	}
	if !hasWarn {
		t.Errorf("expected parent_closed warning; got %v", res.Warnings)
	}

	// Also verify the warning round-trips through JSON (per §5.C.4).
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"parent_closed"`) {
		t.Errorf("JSON missing parent_closed: %s", data)
	}
}

func TestRunCreate_EmptyTitle(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: ""})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "bad_flag" {
		t.Errorf("error = %q", e.Error)
	}
}

func TestRunCreate_InvalidType(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "x", Type: "feature"})
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "bad_flag" {
		t.Errorf("error = %q", e.Error)
	}
}

func TestRunCreate_NotInitialized(t *testing.T) {
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

	out, code := RunCreate(dir, CreateOptions{Title: "x"})
	if code != 3 {
		t.Fatalf("code = %d, want 3", code)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "act_not_initialized" {
		t.Errorf("error = %q", e.Error)
	}
}

func TestRunCreate_ParentNotFound(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{
		Title:  "x",
		Type:   "task",
		Parent: "act-deadbeef",
	})
	if code != 3 {
		t.Fatalf("code = %d, want 3 (issue_not_found); out=%+v", code, out)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok {
		t.Fatalf("type %T", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q", e.Error)
	}
}

// TestRunCreate_PriorityValuesRoundTrip is the regression test for
// dogfood-report.md finding #1: `act create -p 0 "title"` was producing an
// issue with priority=1 because the old CLI normalized Priority==0 to the
// default. We assert that every priority in {0,1,2,3} round-trips through
// both the on-disk op payload and the rendered fold state.
func TestRunCreate_PriorityValuesRoundTrip(t *testing.T) {
	for _, want := range []int{0, 1, 2, 3} {
		want := want
		t.Run("priority="+strconvItoa(want), func(t *testing.T) {
			root := makeCreateRepo(t)
			out, code := RunCreate(root, CreateOptions{
				Title:    "p" + strconvItoa(want),
				Type:     "task",
				Priority: intPtr(want),
			})
			if code != 0 {
				t.Fatalf("code = %d; out=%+v", code, out)
			}
			id := out.(CreateResult).ID

			// 1. Op payload on disk records exactly the requested priority,
			//    including 0 (which is what the dogfood bug hid).
			matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-create.json"))
			if len(matches) != 1 {
				t.Fatalf("expected 1 op file, got %d", len(matches))
			}
			body, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatalf("read op: %v", err)
			}
			env, err := op.Unmarshal(body)
			if err != nil {
				t.Fatalf("unmarshal env: %v", err)
			}
			var p op.CreatePayload
			if err := json.Unmarshal(env.Payload, &p); err != nil {
				t.Fatalf("unmarshal payload: %v", err)
			}
			if p.Priority == nil {
				t.Fatalf("payload priority = nil; want pointer to %d", want)
			}
			if *p.Priority != want {
				t.Fatalf("payload priority = %d; want %d", *p.Priority, want)
			}

			// 2. Folded state renders the same priority. This catches a
			//    regression where the apply layer might re-apply the
			//    "0 -> 1" coercion.
			paths := config.Layout(root)
			state, err := fold.FoldIssue(paths.Ops, id, fold.ApplyDispatch)
			if err != nil {
				t.Fatalf("FoldIssue: %v", err)
			}
			got, ok := state.Fields["priority"].(int)
			if !ok {
				t.Fatalf("rendered priority field type = %T (%v); want int", state.Fields["priority"], state.Fields["priority"])
			}
			if got != want {
				t.Fatalf("rendered priority = %d; want %d", got, want)
			}
		})
	}
}

// TestRunCreate_PriorityNilDefaults verifies that omitting Priority (the
// caller passing nil) still defaults to 1 — i.e. the fix for -p 0 must not
// regress the "no flag" path that all the existing tests rely on.
func TestRunCreate_PriorityNilDefaults(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "default", Type: "task"})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	id := out.(CreateResult).ID

	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-create.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 op file, got %d", len(matches))
	}
	body, _ := os.ReadFile(matches[0])
	env, err := op.Unmarshal(body)
	if err != nil {
		t.Fatalf("unmarshal env: %v", err)
	}
	var p op.CreatePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Priority == nil || *p.Priority != 2 {
		t.Fatalf("default priority = %v; want pointer to 2 (spec default; act-d9c7)", p.Priority)
	}
}

// strconvItoa is a tiny inline wrapper so this file does not have to import
// strconv just for the priority subtest names.
func strconvItoa(i int) string {
	switch i {
	case 0:
		return "0"
	case 1:
		return "1"
	case 2:
		return "2"
	case 3:
		return "3"
	}
	return "?"
}

// writeCloseOp writes a minimal close op to the given parent's shard so
// that an index rebuild reports status=closed. The HLC wall is bumped
// past any create op the test may have written.
func writeCloseOp(t *testing.T, root, parentID string) {
	t.Helper()
	closeBody, err := json.Marshal(op.ClosePayload{Reason: "test"})
	if err != nil {
		t.Fatalf("marshal close: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       parentID,
		Payload:       closeBody,
	}
	// Build an HLC well past the create op. The exact wall does not
	// matter as long as it sorts after the create.
	env.HLC.Wall = 2000000000000
	env.HLC.Logical = 0
	env.HLC.NodeID = "0123abcd"
	env.NodeID = "0123abcd"

	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("env marshal: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", parentID, "2033-05")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "close.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write close: %v", err)
	}
	// Stage and commit the file in the NESTED .act/ repo (Phase 1)
	// so subsequent commits in the test repo do not pull this file
	// in via auto-add. The path passed to `git add` is the absolute
	// path inside the nested working tree.
	actDir := filepath.Join(root, ".act")
	cmd := exec.Command("git", "add", path)
	cmd.Dir = actDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add close: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-q", "--no-verify", "-m", "close parent")
	cmd.Dir = actDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit close: %v\n%s", err, out)
	}
}

// TestRunCreate_BlockedBy_SingleAtomicCommit verifies act-c26a's core
// promise: `act create --blocked-by X` writes the create + add_dep ops
// into ONE git commit (not two), with the canonical batch marker subject
// `act-op: (act-XXXX) create +1`.
func TestRunCreate_BlockedBy_SingleAtomicCommit(t *testing.T) {
	root := makeCreateRepo(t)

	parentOut, code := RunCreate(root, CreateOptions{Title: "blocker", Type: "task"})
	if code != 0 {
		t.Fatalf("seed parent: %d", code)
	}
	parentID := parentOut.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{
		Title:     "blocked",
		Type:      "task",
		BlockedBy: []string{parentID},
	})
	if code != 0 {
		t.Fatalf("create with --blocked-by: code = %d; out=%+v", code, out)
	}
	childID := out.(CreateResult).ID

	// One commit, not two.
	commitsAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-list", "--count", headBefore+"..HEAD"))
	if commitsAfter != "1" {
		t.Errorf("expected 1 new commit, got %s", commitsAfter)
	}

	// Subject carries the +N batch marker (the create + 1 dep op).
	subj := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "-1", "--format=%s"))
	if !strings.Contains(subj, "create +1") {
		t.Errorf("subject %q missing batch suffix `create +1`", subj)
	}
	// And the (act-XXXX) marker is the child's short id so doctor's grep
	// keys on the right issue.
	if !strings.Contains(subj, "("+ShortIssueID(childID)+")") {
		t.Errorf("subject %q does not embed child short id", subj)
	}

	// Both op files exist on disk under the child id's shard.
	createMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", childID, "*", "*-create.json"))
	depMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", childID, "*", "*-add_dep.json"))
	if len(createMatches) != 1 {
		t.Errorf("expected 1 create op file, got %d", len(createMatches))
	}
	if len(depMatches) != 1 {
		t.Errorf("expected 1 add_dep op file, got %d", len(depMatches))
	}

	// The folded state of the child has exactly one blocks-edge to the parent.
	paths := config.Layout(root)
	state, err := fold.FoldIssue(paths.Ops, childID, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("FoldIssue child: %v", err)
	}
	deps, ok := state.Fields["deps"].([]map[string]string)
	if !ok || len(deps) != 1 {
		t.Fatalf("expected 1 dep, got %v (type %T)", state.Fields["deps"], state.Fields["deps"])
	}
	if deps[0]["parent"] != parentID || deps[0]["edge_type"] != "blocks" {
		t.Errorf("dep = %v; want parent=%s edge_type=blocks", deps[0], parentID)
	}
}

// TestRunCreate_BlockedBy_MultipleDeps verifies that N --blocked-by ids
// each produce a distinct add_dep op, all bundled into one commit.
func TestRunCreate_BlockedBy_MultipleDeps(t *testing.T) {
	root := makeCreateRepo(t)

	a, _ := RunCreate(root, CreateOptions{Title: "a"})
	b, _ := RunCreate(root, CreateOptions{Title: "b"})
	c, _ := RunCreate(root, CreateOptions{Title: "c"})
	aID := a.(CreateResult).ID
	bID := b.(CreateResult).ID
	cID := c.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{
		Title:     "child of three",
		BlockedBy: []string{aID, bID, cID},
	})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	childID := out.(CreateResult).ID

	commitsAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-list", "--count", headBefore+"..HEAD"))
	if commitsAfter != "1" {
		t.Errorf("expected 1 commit, got %s", commitsAfter)
	}
	subj := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "-1", "--format=%s"))
	if !strings.Contains(subj, "create +3") {
		t.Errorf("subject %q missing `create +3` (1 create + 3 deps = 4 ops)", subj)
	}

	depMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", childID, "*", "*-add_dep.json"))
	if len(depMatches) != 3 {
		t.Errorf("expected 3 add_dep op files, got %d", len(depMatches))
	}

	paths := config.Layout(root)
	state, err := fold.FoldIssue(paths.Ops, childID, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("FoldIssue: %v", err)
	}
	deps, _ := state.Fields["deps"].([]map[string]string)
	if len(deps) != 3 {
		t.Fatalf("expected 3 deps, got %d (%v)", len(deps), deps)
	}
	gotParents := map[string]bool{}
	for _, d := range deps {
		if d["edge_type"] != "blocks" {
			t.Errorf("non-blocks edge type %q", d["edge_type"])
		}
		gotParents[d["parent"]] = true
	}
	for _, want := range []string{aID, bID, cID} {
		if !gotParents[want] {
			t.Errorf("missing dep to %s", want)
		}
	}
}

// TestRunCreate_BlockedBy_DuplicateTargetsDedup verifies that --blocked-by
// values resolving to the same full id fold to one edge (not two).
func TestRunCreate_BlockedBy_DuplicateTargetsDedup(t *testing.T) {
	root := makeCreateRepo(t)
	pOut, _ := RunCreate(root, CreateOptions{Title: "parent"})
	pID := pOut.(CreateResult).ID

	out, code := RunCreate(root, CreateOptions{
		Title:     "child",
		BlockedBy: []string{pID, pID, pID},
	})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	childID := out.(CreateResult).ID

	depMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", childID, "*", "*-add_dep.json"))
	if len(depMatches) != 1 {
		t.Errorf("expected 1 add_dep (dedup), got %d", len(depMatches))
	}
}

// TestRunCreate_BlockedBy_UnknownTarget verifies that an unknown id
// surfaces issue_not_found (exit 3) and leaves NO ops on disk — the new
// issue must not exist with a missing edge.
func TestRunCreate_BlockedBy_UnknownTarget(t *testing.T) {
	root := makeCreateRepo(t)

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{
		Title:     "would-be orphan",
		BlockedBy: []string{"act-deadbeef"},
	})
	if code != 3 {
		t.Fatalf("code = %d; want 3 (issue_not_found); out=%+v", code, out)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok || e.Error != "issue_not_found" {
		t.Fatalf("error envelope = %+v (type %T)", out, out)
	}

	// No commit should have landed.
	headAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("HEAD moved %s -> %s; should be unchanged on unknown-target failure", headBefore, headAfter)
	}

	// No op files for any new issue: the .act/ops/ tree should contain
	// only directories from prior makeCreateRepo seeds (which is none).
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", "act-*"))
	if len(matches) != 0 {
		t.Errorf("expected no issue directories, got %v", matches)
	}
}

// TestRunCreate_BlockedBy_EmptyValue verifies that --blocked-by "" is
// rejected as bad_flag (exit 2). A no-op flag with an empty string would
// silently succeed otherwise.
func TestRunCreate_BlockedBy_EmptyValue(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{
		Title:     "x",
		BlockedBy: []string{""},
	})
	if code != 2 {
		t.Fatalf("code = %d; want 2; out=%+v", code, out)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok || e.Error != "bad_flag" {
		t.Errorf("error envelope = %+v (type %T)", out, out)
	}
}

// TestRunCreate_BlockedBy_NoCommit verifies that --no-commit writes all
// op files (create + dep ops) but does not commit. This is the bootstrap/
// migration escape hatch; agents using --no-commit are responsible for
// downstream staging.
func TestRunCreate_BlockedBy_NoCommit(t *testing.T) {
	root := makeCreateRepo(t)
	pOut, _ := RunCreate(root, CreateOptions{Title: "p"})
	pID := pOut.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	out, code := RunCreate(root, CreateOptions{
		Title:     "c",
		BlockedBy: []string{pID},
		NoCommit:  true,
	})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	childID := out.(CreateResult).ID

	headAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("expected no commit; HEAD %s -> %s", headBefore, headAfter)
	}
	createMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", childID, "*", "*-create.json"))
	depMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", childID, "*", "*-add_dep.json"))
	if len(createMatches) != 1 || len(depMatches) != 1 {
		t.Errorf("expected 1 create + 1 add_dep on disk, got %d + %d", len(createMatches), len(depMatches))
	}
}

// TestRunCreate_Blocks_InverseDirectionRepro is the user-reported trap:
// when an agent files "a new follow-up that BLOCKS an existing fanout",
// reaching for the create-side --blocked-by flag records the inverse
// direction. The new issue gets blocked, the fanout stays in `ready`,
// and the gating is silently inverted.
//
// This test pins both halves of the contract:
//
//	A) --blocked-by <existing>  records "new is blocked by existing" → fanout still in ready.
//	B) --blocks <existing>      records "new blocks existing"        → fanout NOT in ready.
//
// (A) was always the behavior; (B) is what this issue ships.
func TestRunCreate_Blocks_InverseDirectionRepro(t *testing.T) {
	root := makeCreateRepo(t)

	// Seed the "fanout meta-ticket" — the existing issue that the agent
	// wants the new follow-up to gate.
	fanoutOut, code := RunCreate(root, CreateOptions{Title: "fanout meta-ticket"})
	if code != 0 {
		t.Fatalf("seed fanout: %d", code)
	}
	fanoutID := fanoutOut.(CreateResult).ID

	// ---- Scenario A: the trap. Agent reaches for --blocked-by. ----
	trapOut, code := RunCreate(root, CreateOptions{
		Title:     "critic finding (intended to gate fanout)",
		BlockedBy: []string{fanoutID},
	})
	if code != 0 {
		t.Fatalf("scenario A: %d", code)
	}
	trapNewID := trapOut.(CreateResult).ID

	readyOut, code := RunReady(root, ReadyOptions{Limit: 50})
	if code != 0 {
		t.Fatalf("ready after A: %d", code)
	}
	readyIDsA := map[string]bool{}
	for _, r := range readyOut.(ReadyResult).Ready {
		readyIDsA[r.ID] = true
	}
	if !readyIDsA[fanoutID] {
		t.Errorf("scenario A: fanout %s should still be in ready (its deps were not modified); this is the trap", fanoutID)
	}
	if readyIDsA[trapNewID] {
		t.Errorf("scenario A: new issue %s should be hidden from ready (it's blocked by fanout); this is the trap's inverted gating", trapNewID)
	}

	// ---- Scenario B: the fix. Agent reaches for --blocks. ----
	fixOut, code := RunCreate(root, CreateOptions{
		Title:  "second critic finding (gates fanout, correctly this time)",
		Blocks: []string{fanoutID},
	})
	if code != 0 {
		t.Fatalf("scenario B (the new --blocks flag): %d; out=%+v", code, fixOut)
	}
	fixNewID := fixOut.(CreateResult).ID

	readyOut, code = RunReady(root, ReadyOptions{Limit: 50})
	if code != 0 {
		t.Fatalf("ready after B: %d", code)
	}
	readyIDsB := map[string]bool{}
	for _, r := range readyOut.(ReadyResult).Ready {
		readyIDsB[r.ID] = true
	}
	if readyIDsB[fanoutID] {
		t.Errorf("scenario B: fanout %s should be hidden from ready (now blocked by the new issue); fix is not working", fanoutID)
	}
	if !readyIDsB[fixNewID] {
		t.Errorf("scenario B: new issue %s should be in ready (it has no incoming blocks); fix should leave it unblocked", fixNewID)
	}
}

// TestRunCreate_Blocks_SingleAtomicCommit mirrors the --blocked-by atomic
// commit guarantee but for the inverse direction. With --blocks, the
// add_dep op lands under the EXISTING issue's shard (its deps[] is what
// grows), not under the new issue's. The commit subject still keys the
// new issue's short id so doctor's orphan-close correlates correctly.
func TestRunCreate_Blocks_SingleAtomicCommit(t *testing.T) {
	root := makeCreateRepo(t)

	existingOut, code := RunCreate(root, CreateOptions{Title: "fanout", Type: "task"})
	if code != 0 {
		t.Fatalf("seed: %d", code)
	}
	existingID := existingOut.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{
		Title:  "follow-up blocker",
		Type:   "task",
		Blocks: []string{existingID},
	})
	if code != 0 {
		t.Fatalf("create with --blocks: %d; out=%+v", code, out)
	}
	newID := out.(CreateResult).ID

	// Exactly one new commit.
	commitsAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-list", "--count", headBefore+"..HEAD"))
	if commitsAfter != "1" {
		t.Errorf("expected 1 new commit, got %s", commitsAfter)
	}

	// Subject carries `create +1` and keys the NEW issue (not the existing).
	subj := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "-1", "--format=%s"))
	if !strings.Contains(subj, "create +1") {
		t.Errorf("subject %q missing `create +1`", subj)
	}
	if !strings.Contains(subj, "("+ShortIssueID(newID)+")") {
		t.Errorf("subject %q does not embed new issue short id; doctor's orphan-close grep will not match", subj)
	}

	// Create op lives under newID's shard; add_dep op lives under
	// existingID's shard (because the dep belongs to the existing
	// issue — its deps[] is what grew).
	createMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", newID, "*", "*-create.json"))
	if len(createMatches) != 1 {
		t.Errorf("expected 1 create op under new id, got %d", len(createMatches))
	}
	depUnderExisting, _ := filepath.Glob(filepath.Join(root, ".act", "ops", existingID, "*", "*-add_dep.json"))
	if len(depUnderExisting) != 1 {
		t.Errorf("expected 1 add_dep op under existing id (its deps grew), got %d", len(depUnderExisting))
	}
	depUnderNew, _ := filepath.Glob(filepath.Join(root, ".act", "ops", newID, "*", "*-add_dep.json"))
	if len(depUnderNew) != 0 {
		t.Errorf("expected 0 add_dep ops under new id (its deps unchanged), got %d", len(depUnderNew))
	}

	// Folded state of the EXISTING issue has the new issue in deps[].
	paths := config.Layout(root)
	existingState, err := fold.FoldIssue(paths.Ops, existingID, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("FoldIssue existing: %v", err)
	}
	existingDeps, _ := existingState.Fields["deps"].([]map[string]string)
	if len(existingDeps) != 1 {
		t.Fatalf("existing.deps len = %d; want 1; got %v", len(existingDeps), existingState.Fields["deps"])
	}
	if existingDeps[0]["parent"] != newID || existingDeps[0]["edge_type"] != "blocks" {
		t.Errorf("existing.deps[0] = %v; want parent=%s edge_type=blocks", existingDeps[0], newID)
	}

	// Folded state of the NEW issue has empty deps[] (--blocks doesn't
	// modify the new issue's deps, only the existing one's).
	newState, err := fold.FoldIssue(paths.Ops, newID, fold.ApplyDispatch)
	if err != nil {
		t.Fatalf("FoldIssue new: %v", err)
	}
	newDeps, _ := newState.Fields["deps"].([]map[string]string)
	if len(newDeps) != 0 {
		t.Errorf("new.deps should be empty; got %v", newDeps)
	}
}

// TestRunCreate_Blocks_MultipleDeps verifies that N --blocks ids each
// produce a distinct add_dep op (one per existing issue's shard), all
// bundled into one commit.
func TestRunCreate_Blocks_MultipleDeps(t *testing.T) {
	root := makeCreateRepo(t)

	a, _ := RunCreate(root, CreateOptions{Title: "a"})
	b, _ := RunCreate(root, CreateOptions{Title: "b"})
	c, _ := RunCreate(root, CreateOptions{Title: "c"})
	aID := a.(CreateResult).ID
	bID := b.(CreateResult).ID
	cID := c.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{
		Title:  "blocks three",
		Blocks: []string{aID, bID, cID},
	})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	newID := out.(CreateResult).ID

	commitsAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-list", "--count", headBefore+"..HEAD"))
	if commitsAfter != "1" {
		t.Errorf("expected 1 commit, got %s", commitsAfter)
	}
	subj := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "-1", "--format=%s"))
	if !strings.Contains(subj, "create +3") {
		t.Errorf("subject %q missing `create +3`", subj)
	}

	// One add_dep op file under each existing issue's shard.
	for _, existing := range []string{aID, bID, cID} {
		matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", existing, "*", "*-add_dep.json"))
		if len(matches) != 1 {
			t.Errorf("expected 1 add_dep under %s, got %d", existing, len(matches))
		}
		state, err := fold.FoldIssue(filepath.Join(root, ".act", "ops"), existing, fold.ApplyDispatch)
		if err != nil {
			t.Fatalf("FoldIssue %s: %v", existing, err)
		}
		deps, _ := state.Fields["deps"].([]map[string]string)
		if len(deps) != 1 || deps[0]["parent"] != newID || deps[0]["edge_type"] != "blocks" {
			t.Errorf("%s.deps = %v; want one entry pointing at %s", existing, deps, newID)
		}
	}
}

// TestRunCreate_Blocks_DuplicateTargetsDedup verifies that --blocks
// values resolving to the same full id fold to one edge.
func TestRunCreate_Blocks_DuplicateTargetsDedup(t *testing.T) {
	root := makeCreateRepo(t)
	eOut, _ := RunCreate(root, CreateOptions{Title: "existing"})
	eID := eOut.(CreateResult).ID

	out, code := RunCreate(root, CreateOptions{
		Title:  "new",
		Blocks: []string{eID, eID, eID},
	})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	_ = out.(CreateResult).ID

	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", eID, "*", "*-add_dep.json"))
	if len(matches) != 1 {
		t.Errorf("expected 1 add_dep (dedup), got %d", len(matches))
	}
}

// TestRunCreate_Blocks_UnknownTarget verifies that an unknown id
// surfaces issue_not_found (exit 3) and leaves NO ops on disk — the
// new issue must not exist with a missing edge.
func TestRunCreate_Blocks_UnknownTarget(t *testing.T) {
	root := makeCreateRepo(t)

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{
		Title:  "would-be orphan",
		Blocks: []string{"act-deadbeef"},
	})
	if code != 3 {
		t.Fatalf("code = %d; want 3; out=%+v", code, out)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok || e.Error != "issue_not_found" {
		t.Fatalf("error envelope = %+v (type %T)", out, out)
	}
	// Message must name --blocks so the operator knows which flag failed.
	if !strings.Contains(e.Message, "--blocks") {
		t.Errorf("message %q does not mention --blocks", e.Message)
	}

	headAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("HEAD moved on unknown-target failure")
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", "act-*"))
	if len(matches) != 0 {
		t.Errorf("expected no issue directories, got %v", matches)
	}
}

// TestRunCreate_Blocks_EmptyValue verifies that --blocks "" is rejected
// as bad_flag (exit 2).
func TestRunCreate_Blocks_EmptyValue(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{
		Title:  "x",
		Blocks: []string{""},
	})
	if code != 2 {
		t.Fatalf("code = %d; want 2; out=%+v", code, out)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok || e.Error != "bad_flag" {
		t.Errorf("error envelope = %+v (type %T)", out, out)
	}
}

// TestRunCreate_Blocks_NoCommit verifies that --no-commit writes the
// create op AND each add_dep op under the existing issue's shard, but
// does not commit. This matches the --blocked-by --no-commit contract.
func TestRunCreate_Blocks_NoCommit(t *testing.T) {
	root := makeCreateRepo(t)
	eOut, _ := RunCreate(root, CreateOptions{Title: "existing"})
	eID := eOut.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	out, code := RunCreate(root, CreateOptions{
		Title:    "new",
		Blocks:   []string{eID},
		NoCommit: true,
	})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	newID := out.(CreateResult).ID

	headAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("expected no commit; HEAD %s -> %s", headBefore, headAfter)
	}
	createMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", newID, "*", "*-create.json"))
	depMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", eID, "*", "*-add_dep.json"))
	if len(createMatches) != 1 || len(depMatches) != 1 {
		t.Errorf("expected 1 create (under new) + 1 add_dep (under existing) on disk, got %d + %d", len(createMatches), len(depMatches))
	}
}

// TestRunCreate_Blocks_MixedWithBlockedBy verifies that --blocks and
// --blocked-by are usable on the same create call. The new issue is
// simultaneously gated by some prereqs (--blocked-by) and gating some
// downstream work (--blocks); all 1 create + N+M add_dep ops land in
// one commit.
func TestRunCreate_Blocks_MixedWithBlockedBy(t *testing.T) {
	root := makeCreateRepo(t)
	prereqOut, _ := RunCreate(root, CreateOptions{Title: "prereq"})
	downstreamOut, _ := RunCreate(root, CreateOptions{Title: "downstream"})
	prereqID := prereqOut.(CreateResult).ID
	downstreamID := downstreamOut.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{
		Title:     "middle",
		BlockedBy: []string{prereqID},
		Blocks:    []string{downstreamID},
	})
	if code != 0 {
		t.Fatalf("code = %d; out=%+v", code, out)
	}
	newID := out.(CreateResult).ID

	commitsAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-list", "--count", headBefore+"..HEAD"))
	if commitsAfter != "1" {
		t.Errorf("expected 1 commit, got %s", commitsAfter)
	}
	subj := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "-1", "--format=%s"))
	if !strings.Contains(subj, "create +2") {
		t.Errorf("subject %q missing `create +2` (1 create + 1 blocked-by + 1 blocks = 3 ops)", subj)
	}

	// --blocked-by add_dep is under the NEW issue's shard.
	bbMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", newID, "*", "*-add_dep.json"))
	if len(bbMatches) != 1 {
		t.Errorf("expected 1 add_dep under new id (--blocked-by), got %d", len(bbMatches))
	}
	// --blocks add_dep is under the DOWNSTREAM (existing) issue's shard.
	bkMatches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", downstreamID, "*", "*-add_dep.json"))
	if len(bkMatches) != 1 {
		t.Errorf("expected 1 add_dep under downstream id (--blocks), got %d", len(bkMatches))
	}

	// Folded state: new.deps[0].parent = prereqID; downstream.deps[0].parent = newID.
	paths := config.Layout(root)
	newState, _ := fold.FoldIssue(paths.Ops, newID, fold.ApplyDispatch)
	newDeps, _ := newState.Fields["deps"].([]map[string]string)
	if len(newDeps) != 1 || newDeps[0]["parent"] != prereqID {
		t.Errorf("new.deps = %v; want one entry pointing at %s", newDeps, prereqID)
	}
	dsState, _ := fold.FoldIssue(paths.Ops, downstreamID, fold.ApplyDispatch)
	dsDeps, _ := dsState.Fields["deps"].([]map[string]string)
	if len(dsDeps) != 1 || dsDeps[0]["parent"] != newID {
		t.Errorf("downstream.deps = %v; want one entry pointing at %s", dsDeps, newID)
	}
}

// TestRunCreate_Blocks_SharedIDWithBlockedBy_Rejected verifies that the
// same existing id cannot appear in both --blocked-by and --blocks on
// one call (it would record a 2-cycle: new blocks X and X blocks new).
func TestRunCreate_Blocks_SharedIDWithBlockedBy_Rejected(t *testing.T) {
	root := makeCreateRepo(t)
	xOut, _ := RunCreate(root, CreateOptions{Title: "x"})
	xID := xOut.(CreateResult).ID

	headBefore := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	out, code := RunCreate(root, CreateOptions{
		Title:     "would-be cycle",
		BlockedBy: []string{xID},
		Blocks:    []string{xID},
	})
	if code != 2 {
		t.Fatalf("code = %d; want 2 (bad_flag for 2-cycle); out=%+v", code, out)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok || e.Error != "bad_flag" {
		t.Errorf("error envelope = %+v (type %T)", out, out)
	}
	// HEAD must not move on a rejected create.
	headAfter := strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("HEAD moved on rejected 2-cycle create")
	}
}

// TestRunCreate_Blocks_AmbiguousPrefix verifies that a prefix matching
// multiple existing issues surfaces id_ambiguous (exit 2), with --blocks
// named in the message and candidates populated.
func TestRunCreate_Blocks_AmbiguousPrefix(t *testing.T) {
	root := makeCreateRepo(t)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd1111", "alpha", 1700000000000, 0)
	seedCreateOpForAmbiguousTest(t, root, "act-abcd2222", "bravo", 1700000000001, 0)

	out, code := RunCreate(root, CreateOptions{
		Title:  "new",
		Blocks: []string{"act-abcd"},
	})
	if code != 2 {
		t.Fatalf("code = %d; want 2 (id_ambiguous); out=%+v", code, out)
	}
	e, ok := out.(CreateErrorOutput)
	if !ok || e.Error != "id_ambiguous" {
		t.Fatalf("error envelope = %+v (type %T)", out, out)
	}
	if !strings.Contains(e.Message, "--blocks") {
		t.Errorf("message %q does not mention --blocks", e.Message)
	}
	if len(e.Candidates) != 2 {
		t.Errorf("candidates = %v; want 2", e.Candidates)
	}
}

// TestRunCreate_Blocks_JSONEnvelopeRoundTrip verifies the success envelope
// for --blocks marshals through a JSON round-trip without losing fields
// (paranoid check for the act-c22b-style rollback noise; matches the
// implicit contract of --blocked-by tests).
func TestRunCreate_Blocks_JSONEnvelopeRoundTrip(t *testing.T) {
	root := makeCreateRepo(t)
	eOut, _ := RunCreate(root, CreateOptions{Title: "e"})
	eID := eOut.(CreateResult).ID

	out, code := RunCreate(root, CreateOptions{
		Title:  "n",
		Blocks: []string{eID},
	})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	body, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip CreateResult
	if err := json.Unmarshal(body, &roundTrip); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !roundTrip.Ok || roundTrip.ID == "" || roundTrip.Prefix == "" || roundTrip.Title != "n" {
		t.Errorf("round-trip = %+v", roundTrip)
	}
}
