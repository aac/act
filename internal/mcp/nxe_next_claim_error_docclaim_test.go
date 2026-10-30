package mcp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// nxeFreezeOps makes every directory under the tracker's ops/ read-only, so
// writing a claim op fails and the claim answers exit 1 claim_failed. The
// modes are restored at cleanup so the temp dir can be removed. Local only:
// no network, no lingering process.
func nxeFreezeOps(t *testing.T, root string) {
	t.Helper()
	var dirs []string
	err := filepath.WalkDir(filepath.Join(root, ".act", "ops"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk ops: %v", err)
	}
	t.Cleanup(func() {
		for _, d := range dirs {
			_ = os.Chmod(d, 0o755)
		}
	})
	for _, d := range dirs {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatalf("chmod %s: %v", d, err)
		}
	}
}

// TestDocClaim_Spec_NxeMCPNextReturnsClaimFailed asserts the `act_next`
// sentence in docs/spec.md (act-11b323): only a lost race (`act update
// --claim` exit 5, claim_lost) moves the loop to the next candidate; any
// other claim failure — here exit 1 claim_failed — is returned as an error
// result, not swallowed into the backoff loop and a `candidates` answer
// that claimed nothing.
func TestDocClaim_Spec_NxeMCPNextReturnsClaimFailed(t *testing.T) {
	root := makeRealRepo(t)
	seedIssue(t, root, "nxe next target")
	seedIssue(t, root, "nxe next second target")
	nxeFreezeOps(t, root)

	res, text := callTool(t, root, "act_next", map[string]any{})
	t.Logf("act_next: isError=%v text=%s", res["isError"], text)
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("act_next whose claim fails (exit 1) is not an error result: %s", text)
	}
	var out struct {
		Claimed    bool   `json:"claimed"`
		Error      string `json:"error"`
		Candidates []any  `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse act_next result %q: %v", text, err)
	}
	if out.Claimed || out.Error != "claim_failed" || out.Candidates != nil {
		t.Errorf("act_next whose claim fails = %s; want the claim_failed envelope (error:claim_failed, no claimed:true, no candidates)", text)
	}

	// The tool description makes the same promise to the client.
	resp := runOne(t, root, false, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	tools, _ := m["tools"].([]any)
	var desc string
	for _, raw := range tools {
		if td, _ := raw.(map[string]any); td["name"] == "act_next" {
			desc, _ = td["description"].(string)
		}
	}
	const want = "Any other claim failure (e.g. `claim_failed`) is returned as an error, not retried on the next candidate."
	if !strings.Contains(desc, want) {
		t.Errorf("act_next description = %q; want it to contain %q", desc, want)
	}
}
