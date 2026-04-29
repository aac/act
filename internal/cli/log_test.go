package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeRepoWithAct creates a tempdir with `.git/` and `.act/ops/` and returns
// repoRoot.
func makeRepoWithAct(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".act", "ops"), 0o755); err != nil {
		t.Fatalf("mkdir .act/ops: %v", err)
	}
	return root
}

// writeOpFile writes env to <root>/.act/ops/<issueID>/<yyyy-mm>/<basename>.json
// using the canonical envelope marshaller.
func writeOpFile(t *testing.T, root string, env op.Envelope, monthDir, basename string) {
	t.Helper()
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", env.IssueID, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, basename)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// makeEnv builds a minimal valid create-op envelope for the given issue id and
// HLC. The payload is intentionally tiny; tests only care that the envelope
// validates and round-trips.
func makeEnv(issueID string, wallMs int64, logical uint32) op.Envelope {
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       issueID,
		Payload:       json.RawMessage(`{"title":"hello"}`),
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

func TestRunLog_HappyPath(t *testing.T) {
	root := makeRepoWithAct(t)

	// Two ops in HLC order: same wall, logical 0 then 1.
	first := makeEnv("act-abcd", 1700000000000, 0)
	second := makeEnv("act-abcd", 1700000000000, 1)
	// Vary payload to avoid filename hash collision.
	second.Payload = json.RawMessage(`{"title":"second"}`)

	// Write in reverse on-disk order to prove the sort is by HLC, not file
	// listing order.
	writeOpFile(t, root, second, "2026-04", "z-second.json")
	writeOpFile(t, root, first, "2026-04", "a-first.json")

	out, code := RunLog(root, "act-abcd", false)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(LogResult)
	if !ok {
		t.Fatalf("output type = %T, want LogResult", out)
	}
	if res.ID != "act-abcd" {
		t.Errorf("id = %q, want act-abcd", res.ID)
	}
	if got := len(res.Ops); got != 2 {
		t.Fatalf("len(ops) = %d, want 2", got)
	}
	if res.Ops[0].HLC.Logical != 0 {
		t.Errorf("ops[0].logical = %d, want 0", res.Ops[0].HLC.Logical)
	}
	if res.Ops[1].HLC.Logical != 1 {
		t.Errorf("ops[1].logical = %d, want 1", res.Ops[1].HLC.Logical)
	}
}

func TestRunLog_PrefixResolution(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeEnv("act-abcd", 1700000000000, 0)
	writeOpFile(t, root, env, "2026-04", "op.json")

	// Short prefix without `act-` should resolve.
	out, code := RunLog(root, "abcd", false)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(LogResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if res.ID != "act-abcd" {
		t.Errorf("id = %q, want act-abcd", res.ID)
	}
}

func TestRunLog_NoActDir(t *testing.T) {
	root := t.TempDir()
	out, code := RunLog(root, "act-abcd", false)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(LogErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want LogErrorOutput", out)
	}
	if e.Error != "not_in_git" {
		t.Errorf("error = %q, want not_in_git", e.Error)
	}
}

func TestRunLog_UnknownID(t *testing.T) {
	root := makeRepoWithAct(t)
	// Seed one issue so allIDs is non-empty.
	env := makeEnv("act-abcd", 1700000000000, 0)
	writeOpFile(t, root, env, "2026-04", "op.json")

	out, code := RunLog(root, "act-ffff", false)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(LogErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want LogErrorOutput", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q, want issue_not_found", e.Error)
	}
}

func TestRunLog_AmbiguousPrefix(t *testing.T) {
	root := makeRepoWithAct(t)
	a := makeEnv("act-abcd1234", 1700000000000, 0)
	b := makeEnv("act-abcd5678", 1700000000001, 0)
	writeOpFile(t, root, a, "2026-04", "a.json")
	writeOpFile(t, root, b, "2026-04", "b.json")

	out, code := RunLog(root, "act-abcd", false)
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(LogErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want LogErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Fatalf("len(candidates) = %d, want 2; candidates=%v", len(e.Candidates), e.Candidates)
	}
	// Candidates must be lexicographically sorted.
	if e.Candidates[0] != "act-abcd1234" || e.Candidates[1] != "act-abcd5678" {
		t.Errorf("candidates = %v, want [act-abcd1234 act-abcd5678]", e.Candidates)
	}
}

func TestRunLog_JSONShape(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeEnv("act-abcd", 1700000000000, 0)
	writeOpFile(t, root, env, "2026-04", "op.json")

	out, code := RunLog(root, "act-abcd", true)
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["id"] != "act-abcd" {
		t.Errorf("id = %v, want act-abcd", decoded["id"])
	}
	ops, ok := decoded["ops"].([]any)
	if !ok {
		t.Fatalf("ops type = %T, want []any", decoded["ops"])
	}
	if len(ops) != 1 {
		t.Errorf("len(ops) = %d, want 1", len(ops))
	}
}

func TestFormatLogHuman_Smoke(t *testing.T) {
	res := LogResult{
		ID: "act-abcd",
		Ops: []op.Envelope{
			makeEnv("act-abcd", 1700000000000, 0),
		},
	}
	got := FormatLogHuman(res)
	if got == "" {
		t.Fatalf("FormatLogHuman returned empty string")
	}
	// Must include op_type and the issue short id and a count line.
	for _, want := range []string{"create", "issue=act-abcd", "1 ops"} {
		if !contains(got, want) {
			t.Errorf("output missing %q in %q", want, got)
		}
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
