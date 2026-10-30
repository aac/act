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
		"act_next":   {"`warnings`", "`claim_unconfirmed`", "`push_deferred`", "`push_deferred_reason`", "not yours yet"},
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

	// The keys the act_next sentence names that no other tools/call test
	// here provokes: a claim origin has not seen is an error result,
	// claim_unconfirmed, with the deferral keys (act-c30acd).
	repo := makeRealRepo(t)
	seedIssue(t, repo, "edg mcp next, unreachable origin")
	fnwAddMissingOrigin(t, repo)
	res, text := callTool(t, repo, "act_next", map[string]any{})
	if isErr, _ := res["isError"].(bool); !isErr || !strings.Contains(text, `"claim_unconfirmed"`) || !strings.Contains(text, `"push_deferred":true`) || !strings.Contains(text, `"push_deferred_reason"`) {
		t.Errorf("act_next with an unreachable origin = isError %v, %s; want an error result, claim_unconfirmed, push_deferred:true and a push_deferred_reason", res["isError"], text)
	}
}
