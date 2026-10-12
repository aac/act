package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/aac/act/internal/op"
)

// opFilesIn returns every op file path under <root>/.act/ops.
func opFilesIn(t *testing.T, root string) []string {
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

// TestDocClaim_MCP_BlockWritesOneAddDepOpInOneCommit asserts spec §5.D.2 and
// the `act_block` tool section as reworded in act-549a7e: act_block writes
// exactly one op — an `add_dep` with type=blocks, no status op — lands it in
// a single commit, and on a failed commit leaves no op in the op log. The
// spec used to require a `set-status` op alongside a `dep-add` op; no op has
// either name, and the status op was removed in act-018cb3.
func TestDocClaim_MCP_BlockWritesOneAddDepOpInOneCommit(t *testing.T) {
	root := makeRealRepo(t)
	victim := seedIssue(t, root, "victim")
	blocker := seedIssue(t, root, "blocker")
	actDir := filepath.Join(root, ".act")

	before := map[string]bool{}
	for _, f := range opFilesIn(t, root) {
		before[f] = true
	}
	commitsBefore := gitOutput(t, actDir, "rev-list", "--count", "HEAD")

	srv := NewServer(root, false, nil, nil)
	args := json.RawMessage(fmt.Sprintf(`{"id":%q,"blocked_by":%q}`, victim, blocker))
	out, isErr := srv.callBlock(args)
	if isErr {
		t.Fatalf("act_block: %+v", out)
	}

	var added []string
	for _, f := range opFilesIn(t, root) {
		if !before[f] {
			added = append(added, f)
		}
	}
	if len(added) != 1 {
		t.Fatalf("act_block wrote %d op files, want exactly 1: %v", len(added), added)
	}
	body, err := os.ReadFile(added[0])
	if err != nil {
		t.Fatalf("read %s: %v", added[0], err)
	}
	env, err := op.Unmarshal(body)
	if err != nil {
		t.Fatalf("op.Unmarshal %s: %v", added[0], err)
	}
	if env.OpType != "add_dep" || env.IssueID != victim {
		t.Errorf("act_block wrote op_type %q on %s, want add_dep on %s", env.OpType, env.IssueID, victim)
	}
	var pl op.AddDepPayload
	if err := json.Unmarshal(env.Payload, &pl); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if pl.Parent != blocker || pl.EdgeType != "blocks" {
		t.Errorf("payload = %+v, want parent=%s edge_type=blocks", pl, blocker)
	}

	// One commit, and the op is in it with a clean tree behind it.
	var n0, n1 int
	fmt.Sscan(commitsBefore, &n0)
	fmt.Sscan(gitOutput(t, actDir, "rev-list", "--count", "HEAD"), &n1)
	if n1-n0 != 1 {
		t.Errorf("act_block made %d commits in .act, want 1", n1-n0)
	}
	if st := gitOutput(t, actDir, "status", "--porcelain", "--", "ops"); st != "" {
		t.Errorf("ops/ is dirty after act_block; the op must be committed:\n%s", st)
	}

	// The response labels the op `dep-add` — the spec says so in as many
	// words, and says the on-disk type is add_dep (asserted above).
	m, _ := out.(map[string]any)
	if got, _ := m["ops_written"].([]string); len(got) != 1 || got[0] != "dep-add" {
		t.Errorf("ops_written = %v, want [dep-add]", m["ops_written"])
	}

	// Failure half: a commit that fails leaves no op behind, and the error
	// reaches the caller.
	victim2 := seedIssue(t, root, "victim two")
	pre := opFilesIn(t, root)
	factory := func(_ string) blockGitOps { return failingGops{repoRoot: actDir} }
	args = json.RawMessage(fmt.Sprintf(`{"id":%q,"blocked_by":%q}`, victim2, blocker))
	out, isErr = srv.callBlockWithGops(args, factory)
	if !isErr {
		t.Fatalf("act_block with a failing commit reported success: %+v", out)
	}
	if post := opFilesIn(t, root); strings.Join(post, "\n") != strings.Join(pre, "\n") {
		t.Errorf("a failed act_block left op files behind:\n before: %v\n after:  %v", pre, post)
	}
	if em, _ := out.(map[string]any); em["error"] == nil || em["error"] == "" {
		t.Errorf("failed act_block surfaced no error class: %+v", out)
	}
}
