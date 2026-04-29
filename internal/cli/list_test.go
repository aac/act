package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeCreateEnv returns a create-op envelope for the given id and HLC, with
// the supplied title/type/priority. The nonce is a deterministic placeholder
// so different envelopes with the same payload produce the same hash; tests
// that need distinct hashes per file simply vary the wall.
func makeCreateEnv(t *testing.T, id string, wallMs int64, logical uint32, title, typ string, priority int) op.Envelope {
	t.Helper()
	pl := op.CreatePayload{
		Title:    title,
		Type:     typ,
		Priority: &priority,
		Nonce:    "00000000000000000000000000000000",
	}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// seedIssue writes a single create op to disk for the given parameters. The
// month-shard directory is derived from the wall.
func seedIssue(t *testing.T, root, id, title, typ string, priority int, wallMs int64, monthDir string) {
	t.Helper()
	env := makeCreateEnv(t, id, wallMs, 0, title, typ, priority)
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("envelope marshal: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", id, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// Encode the wall into the basename so different ids are unambiguous on
	// disk; the parser does not require the canonical filename here because
	// the fold reads each file via op.Unmarshal.
	name := id + "-create.json"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// listTestRepo creates a tempdir with `.git/` and `.act/ops/`, then seeds
// three issues with distinct priority/status/types so the various filters
// have something to bite on.
func listTestRepo(t *testing.T) string {
	t.Helper()
	root := makeRepoWithAct(t)
	// Three open issues with varying priorities.
	seedIssue(t, root, "act-aaaa", "alpha task", "task", 0, 1700000000000, "2026-04")
	seedIssue(t, root, "act-bbbb", "bravo bug", "bug", 1, 1700000010000, "2026-04")
	seedIssue(t, root, "act-cccc", "charlie chore", "chore", 2, 1700000020000, "2026-04")
	return root
}

func TestRunList_NoFilters(t *testing.T) {
	root := listTestRepo(t)
	out, code := RunList(root, ListOptions{Limit: 200})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(ListResult)
	if !ok {
		t.Fatalf("output type = %T, want ListResult", out)
	}
	if res.Count != 3 {
		t.Fatalf("count = %d, want 3", res.Count)
	}
}

func TestRunList_StatusFilter(t *testing.T) {
	root := listTestRepo(t)
	// All seeded issues are open; filter for closed should return nothing.
	out, code := RunList(root, ListOptions{Status: "closed", Limit: 200})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(ListResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if res.Count != 0 {
		t.Fatalf("count = %d, want 0", res.Count)
	}

	// Filter for open should return all three.
	out, code = RunList(root, ListOptions{Status: "open", Limit: 200})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res = out.(ListResult)
	if res.Count != 3 {
		t.Fatalf("count = %d, want 3 (all open)", res.Count)
	}
}

func TestRunList_DefaultSort(t *testing.T) {
	root := listTestRepo(t)
	out, code := RunList(root, ListOptions{Limit: 200})
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	res := out.(ListResult)
	// Default sort: priority asc, then created_at desc, then id asc.
	// Priorities are unique here so we just assert priority order.
	for i := 1; i < len(res.Issues); i++ {
		if res.Issues[i].Priority < res.Issues[i-1].Priority {
			t.Fatalf("priority not ascending at %d: %+v", i, res.Issues)
		}
	}
	if res.Issues[0].Priority != 0 {
		t.Errorf("first priority = %d, want 0", res.Issues[0].Priority)
	}
}

func TestRunList_LimitTruncates(t *testing.T) {
	root := listTestRepo(t)
	out, code := RunList(root, ListOptions{Limit: 2})
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	res := out.(ListResult)
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2", res.Count)
	}
	if len(res.Issues) != 2 {
		t.Fatalf("len(issues) = %d, want 2", len(res.Issues))
	}
}

func TestRunList_JSONShape(t *testing.T) {
	root := listTestRepo(t)
	out, code := RunList(root, ListOptions{Limit: 200, AsJSON: true})
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := decoded["count"]; !ok {
		t.Errorf("missing count key")
	}
	issuesAny, ok := decoded["issues"].([]any)
	if !ok {
		t.Fatalf("issues type = %T, want []any", decoded["issues"])
	}
	if len(issuesAny) == 0 {
		t.Fatalf("no issues")
	}
	first, ok := issuesAny[0].(map[string]any)
	if !ok {
		t.Fatalf("first issue type = %T", issuesAny[0])
	}
	for _, k := range []string{"id", "short_id", "title", "status"} {
		if _, ok := first[k]; !ok {
			t.Errorf("missing key %q in issue: %+v", k, first)
		}
	}
}

func TestRunList_NoActDir(t *testing.T) {
	root := t.TempDir()
	out, code := RunList(root, ListOptions{Limit: 200})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; out=%+v", code, out)
	}
	e, ok := out.(ListErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want ListErrorOutput", out)
	}
	if e.Error != "no_repo" {
		t.Errorf("error = %q, want no_repo", e.Error)
	}
}

func TestRunList_BadSortField(t *testing.T) {
	root := listTestRepo(t)
	out, code := RunList(root, ListOptions{Limit: 200, Sort: "wat"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; out=%+v", code, out)
	}
	if _, ok := out.(ListErrorOutput); !ok {
		t.Fatalf("output type = %T, want ListErrorOutput", out)
	}
}

func TestRunList_BadStatusToken(t *testing.T) {
	root := listTestRepo(t)
	out, code := RunList(root, ListOptions{Limit: 200, Status: "opn"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2; out=%+v", code, out)
	}
	if _, ok := out.(ListErrorOutput); !ok {
		t.Fatalf("output type = %T, want ListErrorOutput", out)
	}
}

func TestParseSortKeys_DescPrefix(t *testing.T) {
	keys, err := parseSortKeys("priority,-created_at")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// Expect: priority asc, created_at desc, id asc (auto-appended).
	if len(keys) != 3 {
		t.Fatalf("len = %d, want 3", len(keys))
	}
	if keys[0].Field != "priority" || keys[0].Desc {
		t.Errorf("keys[0] = %+v", keys[0])
	}
	if keys[1].Field != "created_at" || !keys[1].Desc {
		t.Errorf("keys[1] = %+v", keys[1])
	}
	if keys[2].Field != "id" || keys[2].Desc {
		t.Errorf("keys[2] = %+v", keys[2])
	}
}
