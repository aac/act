package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// TestRunDoctor_NoActDir: a git repo without `.act/` returns exit 3.
func TestRunDoctor_NoActDir(t *testing.T) {
	dir := t.TempDir()
	out, code := RunDoctor(dir, DoctorOptions{})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (out=%+v)", code, out)
	}
	if _, ok := out.(DoctorErrorOutput); !ok {
		t.Fatalf("output type = %T, want DoctorErrorOutput", out)
	}
}

// TestRunDoctor_Clean: a freshly-seeded repo with one create op produces no
// error-severity findings.
func TestRunDoctor_Clean(t *testing.T) {
	root := makeCreateRepo(t)
	if _, code := RunCreate(root, CreateOptions{Title: "A", Type: "task"}); code != 0 {
		t.Fatalf("seed: code=%d", code)
	}
	out, code := RunDoctor(root, DoctorOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (out=%+v)", code, out)
	}
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}
	for _, f := range res.Findings {
		if f.Severity == "error" {
			t.Errorf("unexpected error finding: %+v", f)
		}
	}
}

// TestRunDoctor_DanglingDep: a synthetic add_dep op pointing at a nonexistent
// parent surfaces a dangling-deps finding.
func TestRunDoctor_DanglingDep(t *testing.T) {
	root := makeCreateRepo(t)
	createOut, code := RunCreate(root, CreateOptions{Title: "child", Type: "task"})
	if code != 0 {
		t.Fatalf("seed: code=%d", code)
	}
	childID := createOut.(CreateResult).ID

	// Hand-write an add_dep op for a phantom parent.
	writeRawOp(t, root, childID, "add_dep",
		map[string]string{"parent": "act-deadbeef", "edge_type": "blocks"},
		1)

	out, code := RunDoctor(root, DoctorOptions{Check: "dangling-deps"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "dangling-deps" && f.IssueID == childID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dangling-deps finding for %s, got %+v", childID, res.Findings)
	}
}

// TestRunDoctor_Cycle: A blocks B, B blocks A produces a cycle finding.
func TestRunDoctor_Cycle(t *testing.T) {
	root := makeCreateRepo(t)
	a := mustCreate(t, root, "A")
	b := mustCreate(t, root, "B")

	writeRawOp(t, root, a, "add_dep", map[string]string{"parent": b, "edge_type": "blocks"}, 1)
	writeRawOp(t, root, b, "add_dep", map[string]string{"parent": a, "edge_type": "blocks"}, 2)

	out, code := RunDoctor(root, DoctorOptions{Check: "cycle"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	if len(res.Findings) == 0 {
		t.Fatalf("expected at least one cycle finding, got 0")
	}
	for _, f := range res.Findings {
		if f.Check != "cycle" || f.Severity != "error" {
			t.Errorf("unexpected finding: %+v", f)
		}
	}
}

// TestRunDoctor_UnknownOpVersion: a synthetic op file with op_version=2
// produces an unknown-op-version finding; --fix is ignored (still error).
func TestRunDoctor_UnknownOpVersion(t *testing.T) {
	root := makeCreateRepo(t)
	a := mustCreate(t, root, "A")

	// Write a raw bogus op file with op_version=2 so we sidestep envelope
	// validation. The doctor only consults the JSON header.
	paths := config.Layout(root)
	shard := op.ShardDir(paths.Ops, a, time.Now().UnixMilli())
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	bogus := []byte(`{"op_version":2,"schema_version":1,"writer_version":"0.1.0","op_type":"create","issue_id":"` + a + `","payload":{},"hlc":{"wall":"2026-04-29T00:00:00.000Z","logical":0,"node_id":"0123abcd"},"node_id":"0123abcd"}`)
	bogusPath := filepath.Join(shard, "2026-04-29T00:00:00.000Z-deadbeef-create.json")
	if err := os.WriteFile(bogusPath, bogus, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, fix := range []bool{false, true} {
		out, code := RunDoctor(root, DoctorOptions{Check: "unknown-op-version", Fix: fix})
		if code != 1 {
			t.Fatalf("fix=%v: exit code = %d, want 1 (out=%+v)", fix, code, out)
		}
		res := out.(DoctorResult)
		found := false
		for _, f := range res.Findings {
			if f.Check == "unknown-op-version" {
				found = true
			}
		}
		if !found {
			t.Errorf("fix=%v: expected unknown-op-version finding", fix)
		}
		// Confirm the bogus op file still exists (--fix is read-only here).
		if _, err := os.Stat(bogusPath); err != nil {
			t.Errorf("fix=%v: bogus op was removed: %v", fix, err)
		}
	}
}

// TestRunDoctor_IndexDivergence_Fix: corrupt the index, run with --fix, and
// confirm a re-run reports zero index-divergence findings.
func TestRunDoctor_IndexDivergence_Fix(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	paths := config.Layout(root)

	// Open the index, drop a row to force divergence.
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if err := idx.ApplySchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := idx.Rebuild(paths.Ops); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, err := idx.DB().Exec(`DELETE FROM issues`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	idx.Close()

	// First run without --fix: must surface a divergence finding (error).
	out, code := RunDoctor(root, DoctorOptions{Check: "index-divergence"})
	if code != 1 {
		t.Fatalf("pre-fix: exit = %d, want 1 (out=%+v)", code, out)
	}

	// Run with --fix: replaces the index, exit 0 (warn-only).
	out, code = RunDoctor(root, DoctorOptions{Check: "index-divergence", Fix: true})
	if code != 0 {
		t.Fatalf("fix: exit = %d, want 0 (out=%+v)", code, out)
	}

	// Re-run: zero findings.
	out, code = RunDoctor(root, DoctorOptions{Check: "index-divergence"})
	if code != 0 {
		t.Fatalf("post-fix: exit = %d, want 0 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	if len(res.Findings) != 0 {
		t.Errorf("post-fix findings = %+v, want none", res.Findings)
	}
}

// mustCreate is a tiny helper that creates an issue with the given title,
// failing the test on error and returning the new issue id.
func mustCreate(t *testing.T, root, title string) string {
	t.Helper()
	out, code := RunCreate(root, CreateOptions{Title: title, Type: "task"})
	if code != 0 {
		t.Fatalf("create %s: code=%d", title, code)
	}
	return out.(CreateResult).ID
}

// writeRawOp writes a hand-built op file under <root>/.act/ops/<issueID>/
// for tests that need synthetic add_dep / dangling edges. logical is used as
// a tiebreak so multiple raw writes within the same millisecond don't collide.
func writeRawOp(t *testing.T, root, issueID, opType string, payload map[string]string, logical uint32) {
	t.Helper()
	paths := config.Layout(root)
	cfg, err := config.ReadConfig(paths)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	stamp := hlc.HLC{
		Wall:    time.Now().UnixMilli(),
		Logical: logical,
		NodeID:  cfg.NodeID,
	}
	pjson, err := canonicaljson.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       pjson,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	shard := op.ShardDir(paths.Ops, issueID, stamp.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	full, _ := env.FullHash()
	iso := time.UnixMilli(stamp.Wall).UTC().Format("2006-01-02T15:04:05.000Z")
	name := iso + "-" + full[:8] + "-" + opType + ".json"
	path := filepath.Join(shard, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Compile-time guard that DoctorResult is JSON-marshalable as documented.
func TestDoctorResult_JSONShape(t *testing.T) {
	res := DoctorResult{Findings: []Finding{{Check: "x", Severity: "warn", Message: "m"}}, Count: 1}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if !stringsContains(s, `"findings"`) || !stringsContains(s, `"count":1`) {
		t.Errorf("marshalled = %s", s)
	}
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
