package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeCreateEnvWithParent builds a create envelope where the optional
// parent field is populated, so we can construct parent-chain trees in
// tests.
func makeCreateEnvWithParent(t *testing.T, id string, wallMs int64, title, typ, parent string, priority int) op.Envelope {
	t.Helper()
	pl := op.CreatePayload{
		Title:    title,
		Type:     typ,
		Priority: &priority,
		Parent:   parent,
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
			Logical: 0,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// seedIssueWithParent writes a create op for `id` whose parent is `parent`.
// Empty parent yields a top-level issue. Files are sharded by monthDir.
func seedIssueWithParent(t *testing.T, root, id, title, typ, parent string, priority int, wallMs int64, monthDir string) {
	t.Helper()
	env := makeCreateEnvWithParent(t, id, wallMs, title, typ, parent, priority)
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", id, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, id+"-create.json")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedAddDep writes an add_dep op so that `child` declares a dependency on
// `parent` of the given edge type. The HLC must be later than the create
// ops for both endpoints (callers pass a higher wallMs).
func seedAddDep(t *testing.T, root, child, parent, edge string, wallMs int64, logical uint32, monthDir string) {
	t.Helper()
	pl := op.AddDepPayload{Parent: parent, EdgeType: edge}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal add_dep payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "add_dep",
		IssueID:       child,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
	envBody, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", child, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	// Distinct filename: include logical so multiple add_dep ops don't
	// collide.
	name := child + "-add_dep.json"
	if logical > 0 {
		name = child + "-add_dep-" + string(rune('0'+logical)) + ".json"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, envBody, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedClose writes a close op so that `id` transitions to status=closed.
func seedClose(t *testing.T, root, id, reason string, wallMs int64, monthDir string) {
	t.Helper()
	pl := op.ClosePayload{Reason: reason}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal close payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: 0,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
	envBody, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	dir := filepath.Join(root, ".act", "ops", id, monthDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, id+"-close.json")
	if err := os.WriteFile(path, envBody, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRunReady_BlocksFiltersBlockedIssues seeds A blocked by B (open). Only
// B should be ready. After closing B, A becomes ready as well.
func TestRunReady_BlocksFiltersBlockedIssues(t *testing.T) {
	root := makeRepoWithAct(t)
	// A and B both open. A.deps has parent=B, edge=blocks.
	seedIssueWithParent(t, root, "act-aaaa00000000aaaa", "alpha", "task", "", 1, 1700000000000, "2026-04")
	seedIssueWithParent(t, root, "act-bbbb00000000bbbb", "beta", "task", "", 1, 1700000010000, "2026-04")
	seedAddDep(t, root, "act-aaaa00000000aaaa", "act-bbbb00000000bbbb", "blocks", 1700000020000, 0, "2026-04")

	out, code := RunReady(root, ReadyOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(ReadyResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if res.Count != 1 {
		t.Fatalf("count = %d, want 1; ready=%+v", res.Count, res.Ready)
	}
	if res.Ready[0].ID != "act-bbbb00000000bbbb" {
		t.Errorf("ready[0].ID = %q, want act-bbbb00000000bbbb", res.Ready[0].ID)
	}

	// Close B; now A becomes ready as well.
	seedClose(t, root, "act-bbbb00000000bbbb", "done", 1700000030000, "2026-04")
	out, code = RunReady(root, ReadyOptions{})
	if code != 0 {
		t.Fatalf("after close: exit code = %d", code)
	}
	res = out.(ReadyResult)
	// B is now closed (excluded from ready); A is unblocked.
	if res.Count != 1 {
		t.Fatalf("after close: count = %d, want 1; ready=%+v", res.Count, res.Ready)
	}
	if res.Ready[0].ID != "act-aaaa00000000aaaa" {
		t.Errorf("after close: ready[0].ID = %q, want act-aaaa00000000aaaa", res.Ready[0].ID)
	}
}

// TestRunReady_UnderRestrictsToDescendants seeds a parent issue P with two
// children C1, C2 and an unrelated top-level issue X; --under <P> returns
// only the C* children.
func TestRunReady_UnderRestrictsToDescendants(t *testing.T) {
	root := makeRepoWithAct(t)
	seedIssueWithParent(t, root, "act-1111000000001111", "parent", "epic", "", 1, 1700000000000, "2026-04")
	seedIssueWithParent(t, root, "act-2222000000002222", "child1", "task", "act-1111000000001111", 1, 1700000010000, "2026-04")
	seedIssueWithParent(t, root, "act-3333000000003333", "child2", "task", "act-1111000000001111", 1, 1700000020000, "2026-04")
	seedIssueWithParent(t, root, "act-4444000000004444", "stranger", "task", "", 1, 1700000030000, "2026-04")

	out, code := RunReady(root, ReadyOptions{Under: "act-1111"})
	if code != 0 {
		t.Fatalf("exit code = %d; out=%+v", code, out)
	}
	res := out.(ReadyResult)
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2; ready=%+v", res.Count, res.Ready)
	}
	got := map[string]bool{}
	for _, r := range res.Ready {
		got[r.ID] = true
	}
	if !got["act-2222000000002222"] || !got["act-3333000000003333"] {
		t.Errorf("missing children in ready: %+v", res.Ready)
	}
	if got["act-1111000000001111"] {
		t.Errorf("parent itself should not appear under --under")
	}
	if got["act-4444000000004444"] {
		t.Errorf("stranger should not appear under --under")
	}
}

// TestRunReady_UnderUnknown returns exit 3 on a no-match prefix.
func TestRunReady_UnderUnknown(t *testing.T) {
	root := makeRepoWithAct(t)
	seedIssueWithParent(t, root, "act-aaaa00000000aaaa", "alpha", "task", "", 1, 1700000000000, "2026-04")
	out, code := RunReady(root, ReadyOptions{Under: "act-ffff"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; out=%+v", code, out)
	}
	if _, ok := out.(ReadyErrorOutput); !ok {
		t.Fatalf("output type = %T, want ReadyErrorOutput", out)
	}
}

// TestRunReady_LimitTruncates ensures Limit caps the result set.
func TestRunReady_LimitTruncates(t *testing.T) {
	root := makeRepoWithAct(t)
	seedIssueWithParent(t, root, "act-aaaa00000000aaaa", "a", "task", "", 0, 1700000000000, "2026-04")
	seedIssueWithParent(t, root, "act-bbbb00000000bbbb", "b", "task", "", 1, 1700000010000, "2026-04")
	seedIssueWithParent(t, root, "act-cccc00000000cccc", "c", "task", "", 2, 1700000020000, "2026-04")

	out, code := RunReady(root, ReadyOptions{Limit: 2})
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	res := out.(ReadyResult)
	if res.Count != 2 {
		t.Fatalf("count = %d, want 2", res.Count)
	}
	// Sort is priority asc; expect a, b first.
	if res.Ready[0].Priority != 0 || res.Ready[1].Priority != 1 {
		t.Errorf("priorities = %d,%d; want 0,1", res.Ready[0].Priority, res.Ready[1].Priority)
	}
}

// TestRunReady_MissingActDir returns exit 3.
func TestRunReady_MissingActDir(t *testing.T) {
	root := t.TempDir()
	out, code := RunReady(root, ReadyOptions{})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3; out=%+v", code, out)
	}
	if _, ok := out.(ReadyErrorOutput); !ok {
		t.Fatalf("output type = %T, want ReadyErrorOutput", out)
	}
}

// TestRunReady_EmptyRepo returns an empty ready set, exit 0.
func TestRunReady_EmptyRepo(t *testing.T) {
	root := makeRepoWithAct(t)
	out, code := RunReady(root, ReadyOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(ReadyResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if res.Count != 0 {
		t.Errorf("count = %d, want 0", res.Count)
	}
	// JSON shape sanity-check: marshal must produce {"ready":[],"count":0}.
	data, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ready, ok := decoded["ready"].([]any)
	if !ok || len(ready) != 0 {
		t.Errorf("ready = %v, want []", decoded["ready"])
	}
	if c, _ := decoded["count"].(float64); c != 0 {
		t.Errorf("count = %v, want 0", decoded["count"])
	}
}
