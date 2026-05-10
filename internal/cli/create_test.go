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

// makeCreateRepo initializes a git repo with `.act/` and a valid
// `.act/config.json` (NodeID = "0123abcd"). It returns the absolute
// repo root.
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
	mustGit(t, dir, "add", "README")
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
	if !strings.HasPrefix(res.ID, res.ShortID) {
		t.Errorf("short_id %q is not a prefix of id %q", res.ShortID, res.ID)
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
	headBefore := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))

	_, code := RunCreate(root, CreateOptions{Title: "t1", Type: "task"})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	headAfter := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))
	if headAfter == headBefore {
		t.Fatalf("expected new commit; HEAD unchanged %s", headAfter)
	}
	subj := strings.TrimSpace(runOut(t, root, "git", "log", "-1", "--format=%s"))
	if !strings.HasPrefix(subj, "act-op: ") || !strings.HasSuffix(subj, " create") {
		t.Errorf("subject = %q", subj)
	}
}

func TestRunCreate_NoCommit(t *testing.T) {
	root := makeCreateRepo(t)
	headBefore := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))

	out, code := RunCreate(root, CreateOptions{Title: "t1", Type: "task", NoCommit: true})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	headAfter := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Fatalf("expected no commit; HEAD %s -> %s", headBefore, headAfter)
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
	// Stage and commit the file (so subsequent commits in the test
	// repo do not pull this file in via auto-add).
	cmd := exec.Command("git", "add", path)
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatalf("git add close: %v", err)
	}
	cmd = exec.Command("git", "commit", "-q", "--no-verify", "-m", "close parent")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatalf("git commit close: %v", err)
	}
}
