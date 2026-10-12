package mcp

import (
	"strings"
	"testing"
)

// TestDocClaim_DplMCP_BlockSchemaAdvertisesPush asserts, at the wire
// (tools/list), that act_block's input schema advertises the `push` argument
// the spec's act_block section documents, and that its description names
// the `push_deferred` / `push_deferred_reason` keys the tool has returned
// since e36dec8 (act-18bd72 item 2: the schema listed id, blocked_by and
// reason only, and the description named `warnings` alone).
//
// That `push: true` is honoured, and that the deferral keys are returned,
// is asserted against tools/call by TestDocClaim_FnwMCP_BlockPushNoRemoteWarns
// and TestDocClaim_Spec_PdfMCPBlockReportsPushDeferred.
func TestDocClaim_DplMCP_BlockSchemaAdvertisesPush(t *testing.T) {
	root := makeRepo(t)
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      "dpl-tools",
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	tools, _ := m["tools"].([]any)
	var block map[string]any
	for _, raw := range tools {
		td, _ := raw.(map[string]any)
		if name, _ := td["name"].(string); name == "act_block" {
			block = td
		}
	}
	if block == nil {
		t.Fatalf("act_block not found in tools/list")
	}

	schema, _ := block["inputSchema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Fatalf("act_block has no inputSchema.properties in tools/list: %v", block)
	}
	push, _ := props["push"].(map[string]any)
	if push == nil || push["type"] != "boolean" {
		var names []string
		for k := range props {
			names = append(names, k)
		}
		t.Errorf("act_block inputSchema.properties.push = %v, want a boolean property; advertised properties: %v", props["push"], names)
	}

	desc, _ := block["description"].(string)
	for _, want := range []string{"`push_deferred`", "`push_deferred_reason`", "do not retry"} {
		if !strings.Contains(desc, want) {
			t.Errorf("act_block description does not mention %s: %q", want, desc)
		}
	}
}
