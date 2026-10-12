package mcp

import (
	"strings"
	"testing"
)

// TestDocClaim_EdgMCP_ComposedToolDescriptionsNameTheirExtraKeys asserts,
// at the wire (tools/list), that the descriptions of act_next, act_finish
// and act_block name the extra result keys those tools return (act-11d2e2
// item 7). An MCP client reads the description and nothing else; the keys
// arrived on 2026-10-12 (act-7b65b0) and the descriptions did not say so.
//
// What each tool really returns is asserted against tools/call by the
// TestDocClaim_FnwMCP_* tests in fnw_docclaim_test.go; this test pins the
// sentence an agent reads to the same key names.
func TestDocClaim_EdgMCP_ComposedToolDescriptionsNameTheirExtraKeys(t *testing.T) {
	root := makeRepo(t)
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      "edg-tools",
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	tools, _ := m["tools"].([]any)
	desc := map[string]string{}
	for _, raw := range tools {
		td, _ := raw.(map[string]any)
		name, _ := td["name"].(string)
		desc[name], _ = td["description"].(string)
	}

	for tool, wants := range map[string][]string{
		"act_next":   {"`warnings`", "`push_deferred`", "`push_deferred_reason`", "do not claim again"},
		"act_finish": {"`warnings`", "`push_deferred`", "`push_deferred_reason`", "do not retry"},
		"act_block":  {"`warnings`", "`push_no_remote`"},
	} {
		d, ok := desc[tool]
		if !ok {
			t.Errorf("%s not found in tools/list", tool)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(d, want) {
				t.Errorf("%s description does not mention %s: %q", tool, want, d)
			}
		}
	}

	// The one key the act_next sentence names that no tools/call test
	// provoked: a claim whose push does not reach origin.
	repo := makeRealRepo(t)
	id := seedIssue(t, repo, "edg mcp next, unreachable origin")
	fnwAddMissingOrigin(t, repo)
	out, text := fnwCall(t, repo, "act_next", map[string]any{})
	if !out.Claimed || !strings.Contains(text, id) || !out.PushDeferred || out.PushDeferredReason == "" {
		t.Errorf("act_next with an unreachable origin = %s; want claimed:true for %s, push_deferred:true and a push_deferred_reason", text, id)
	}
}
