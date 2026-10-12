package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// fnwToolResult is the part of a composed tool's result these tests read.
type fnwToolResult struct {
	OK                 bool     `json:"ok"`
	Closed             bool     `json:"closed"`
	Claimed            bool     `json:"claimed"`
	Error              string   `json:"error"`
	Warnings           []string `json:"warnings"`
	PushDeferred       bool     `json:"push_deferred"`
	PushDeferredReason string   `json:"push_deferred_reason"`
}

// fnwCall drives one tools/call through the server, wants a non-error
// result, and returns it parsed with its raw text.
func fnwCall(t *testing.T, root, tool string, args map[string]any) (fnwToolResult, string) {
	t.Helper()
	res, text := callTool(t, root, tool, args)
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("%s %v is an error result: %s", tool, args, text)
	}
	var out fnwToolResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse %s result %q: %v", tool, text, err)
	}
	return out, text
}

// fnwFreezeIndex brings .act/index.db up to date and makes it read-only, so
// reads before a write still answer and only the refresh after the commit
// fails (the shape internal/mcp/ixw_index_readonly_test.go uses).
func fnwFreezeIndex(t *testing.T, root string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions; a read-only index.db stays writable")
	}
	if out, code := cli.RunList(root, cli.ListOptions{AsJSON: true}); code != 0 {
		t.Fatalf("list before freezing the index: %v", out)
	}
	files, _ := filepath.Glob(filepath.Join(root, ".act", "index.db*"))
	if len(files) == 0 {
		t.Fatal("no .act/index.db to freeze")
	}
	for _, f := range files {
		if err := os.Chmod(f, 0o444); err != nil {
			t.Fatalf("chmod %s: %v", f, err)
		}
	}
	t.Cleanup(func() {
		files, _ := filepath.Glob(filepath.Join(root, ".act", "index.db*"))
		for _, f := range files {
			_ = os.Chmod(f, 0o644)
		}
	})
}

// fnwAddMissingOrigin points the tracker's origin at a local path that does
// not exist, so every push fails. Local only: no network.
func fnwAddMissingOrigin(t *testing.T, root string) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "fnw-no-such-origin.git")
	mustGitMCP(t, filepath.Join(root, ".act"), "remote", "add", "origin", missing)
}

// fnwShow returns id's `show --json` body.
func fnwShow(t *testing.T, root, id string) map[string]any {
	t.Helper()
	shown, code := cli.RunShow(root, cli.ShowOptions{ID: id, AsJSON: true})
	if code != 0 {
		t.Fatalf("show %s: code=%d out=%+v", id, code, shown)
	}
	sr, ok := shown.(cli.ShowResult)
	if !ok {
		t.Fatalf("show %s: unexpected type %T", id, shown)
	}
	return sr.ShowJSON()
}

// fnwStatus returns id's folded status.
func fnwStatus(t *testing.T, root, id string) string {
	t.Helper()
	s, _ := fnwShow(t, root, id)["status"].(string)
	return s
}

// TestDocClaim_FnwMCP_FinishCarriesClosesWarningsAndDeferral: act_finish
// passes on what the close under it reported besides the close itself —
// `warnings` and `push_deferred` (act-7b65b0: it rebuilt the result as
// {closed,id,reason} and dropped them).
func TestDocClaim_FnwMCP_FinishCarriesClosesWarningsAndDeferral(t *testing.T) {
	t.Run("push_no_remote", func(t *testing.T) {
		root := makeRealRepo(t)
		id := seedIssue(t, root, "fnw mcp finish push")
		out, text := fnwCall(t, root, "act_finish", map[string]any{"id": id, "push": true})
		if !out.Closed || strings.Join(out.Warnings, ",") != "push_no_remote" {
			t.Errorf("act_finish push:true with no origin = %s; want closed:true and warnings [push_no_remote]", text)
		}
		if st := fnwStatus(t, root, id); st != "closed" {
			t.Errorf("status after act_finish = %q, want closed", st)
		}
		// No push asked for: no `warnings` key.
		id2 := seedIssue(t, root, "fnw mcp finish, no push")
		if _, text2 := fnwCall(t, root, "act_finish", map[string]any{"id": id2}); strings.Contains(text2, "warnings") {
			t.Errorf("act_finish without push carries warnings: %s", text2)
		}
	})

	t.Run("index_update_failed", func(t *testing.T) {
		root := makeRealRepo(t)
		id := seedIssue(t, root, "fnw mcp finish, frozen index")
		fnwFreezeIndex(t, root)
		out, text := fnwCall(t, root, "act_finish", map[string]any{"id": id})
		if !out.Closed || strings.Join(out.Warnings, ",") != "index_update_failed" {
			t.Errorf("act_finish with a read-only index = %s; want closed:true and warnings [index_update_failed]", text)
		}
	})

	t.Run("push_deferred", func(t *testing.T) {
		root := makeRealRepo(t)
		id := seedIssue(t, root, "fnw mcp finish, unreachable origin")
		fnwAddMissingOrigin(t, root)
		out, text := fnwCall(t, root, "act_finish", map[string]any{"id": id})
		if !out.Closed || !out.PushDeferred || out.PushDeferredReason == "" {
			t.Errorf("act_finish with an unreachable origin = %s; want closed:true, push_deferred:true and a push_deferred_reason", text)
		}
	})
}

// TestDocClaim_FnwMCP_BlockPushNoRemoteWarns: act_block with push:true in a
// tracker with no origin remote is a success that names push_no_remote —
// the edge landed and the push the caller asked for did not happen
// (act-7b65b0).
func TestDocClaim_FnwMCP_BlockPushNoRemoteWarns(t *testing.T) {
	root := makeRealRepo(t)
	a := seedIssue(t, root, "fnw mcp blocked")
	b := seedIssue(t, root, "fnw mcp blocker")

	res, text := callTool(t, root, "act_block", map[string]any{"id": a, "blocked_by": b, "push": true})
	t.Logf("act_block push:true, no origin: isError=%v text=%s", res["isError"], text)
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("act_block push:true with no origin is an error result: %s", text)
	}
	var out fnwToolResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse act_block result %q: %v", text, err)
	}
	if !out.OK || strings.Join(out.Warnings, ",") != "push_no_remote" {
		t.Errorf("act_block push:true with no origin = %s; want ok:true and warnings [push_no_remote]", text)
	}
	if shown, _ := json.Marshal(fnwShow(t, root, a)["blocked_by"]); !strings.Contains(string(shown), b) {
		t.Errorf("blocked_by of the blocked issue after act_block = %s, want it to name %s", shown, b)
	}

	// No push asked for: no `warnings` key.
	c := seedIssue(t, root, "fnw mcp blocked, no push")
	if _, text2 := fnwCall(t, root, "act_block", map[string]any{"id": c, "blocked_by": b}); strings.Contains(text2, "warnings") {
		t.Errorf("act_block without push carries warnings: %s", text2)
	}
}

// TestDocClaim_FnwMCP_NextCarriesTheClaimsWarnings: act_next whose claim is
// committed and whose index refresh then fails answers claimed:true with
// the claim's index_update_failed warning (act-7b65b0).
func TestDocClaim_FnwMCP_NextCarriesTheClaimsWarnings(t *testing.T) {
	root := makeRealRepo(t)
	id := seedIssue(t, root, "fnw mcp next, frozen index")
	fnwFreezeIndex(t, root)

	res, text := callTool(t, root, "act_next", map[string]any{})
	t.Logf("act_next, read-only index: isError=%v text=%s", res["isError"], text)
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("act_next with a read-only index is an error result: %s", text)
	}
	var out fnwToolResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse act_next result %q: %v", text, err)
	}
	if !out.Claimed || !strings.Contains(text, id) || strings.Join(out.Warnings, ",") != "index_update_failed" {
		t.Errorf("act_next with a read-only index = %s; want claimed:true for %s and warnings [index_update_failed]", text, id)
	}
}
