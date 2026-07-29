package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// act-a79d66: agents lost annotations because act had no note-append path
// and `act log <id> "message"` swallowed the message. The CLI gained
// --description-append; act_update gains the matching `description_append`
// so an MCP agent isn't pushed back to the read-modify-write.

// TestDocClaim_MCP_UpdateDescriptionAppend pins the act_update schema claim
// at the wire boundary (tools/list) AND exercises the append through a real
// tools/call, so a schema that advertises the parameter while the handler
// drops it — the exact silent-swallow shape this ticket is about — fails
// here.
func TestDocClaim_MCP_UpdateDescriptionAppend(t *testing.T) {
	root := makeRepo(t)

	// --- schema half: the parameter is advertised, and `description` says
	// it replaces so an agent can tell the two apart.
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      "schema",
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	tools, _ := m["tools"].([]any)

	var updateProps map[string]any
	for _, raw := range tools {
		td, _ := raw.(map[string]any)
		if n, _ := td["name"].(string); n != "act_update" {
			continue
		}
		schema, _ := td["inputSchema"].(map[string]any)
		updateProps, _ = schema["properties"].(map[string]any)
	}
	if updateProps == nil {
		t.Fatal("act_update not found in tools/list")
	}

	appendProp, ok := updateProps["description_append"].(map[string]any)
	if !ok {
		t.Fatal("act_update schema does not advertise description_append")
	}
	appendDesc, _ := appendProp["description"].(string)
	if !strings.Contains(appendDesc, "Append this text to the existing description") {
		t.Errorf("description_append schema text does not say it appends: %q", appendDesc)
	}
	replaceProp, _ := updateProps["description"].(map[string]any)
	replaceDesc, _ := replaceProp["description"].(string)
	if !strings.Contains(replaceDesc, "REPLACES") {
		t.Errorf("`description` must say it REPLACES so the pair is distinguishable; got %q", replaceDesc)
	}

	// --- behaviour half: the handler actually honours it.
	out, code := cli.RunCreate(root, cli.CreateOptions{
		Title:       "mcp append probe",
		Description: "original body",
		NoCommit:    true,
	})
	if code != 0 {
		t.Fatalf("create: code=%d out=%+v", code, out)
	}
	created, _ := out.(cli.CreateResult)
	id := created.ID
	if id == "" {
		t.Fatalf("no id from create: %+v", out)
	}

	resp = runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      "append",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "act_update",
			"arguments": map[string]any{
				"id":                 id,
				"description_append": "appended via mcp",
				"no_commit":          true,
			},
		},
	})
	if resp.Error != nil {
		t.Fatalf("tools/call error: %+v", resp.Error)
	}

	shown, code := cli.RunShow(root, cli.ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show: code=%d", code)
	}
	body, _ := json.Marshal(shown)
	if !strings.Contains(string(body), "original body") {
		t.Errorf("append destroyed the existing description: %s", body)
	}
	if !strings.Contains(string(body), "appended via mcp") {
		t.Errorf("appended text missing — the parameter was advertised but dropped: %s", body)
	}
}
