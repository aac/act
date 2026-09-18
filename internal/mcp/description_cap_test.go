package mcp

import (
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// callTool runs one tools/call against root and returns the tool result
// map plus its text content joined.
func callTool(t *testing.T, root, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	resp := runOne(t, root, false, map[string]any{
		"jsonrpc": "2.0",
		"id":      name,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": args},
	})
	if resp.Error != nil {
		t.Fatalf("%s: jsonrpc error: %+v", name, resp.Error)
	}
	res, _ := resp.Result.(map[string]any)
	var text strings.Builder
	content, _ := res["content"].([]any)
	for _, c := range content {
		if m, ok := c.(map[string]any); ok {
			s, _ := m["text"].(string)
			text.WriteString(s)
		}
	}
	return res, text.String()
}

// shownDescriptionLen returns the byte length of id's folded description.
func shownDescriptionLen(t *testing.T, root, id string) int {
	t.Helper()
	shown, code := cli.RunShow(root, cli.ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show %s: code=%d", id, code)
	}
	res, ok := shown.(cli.ShowResult)
	if !ok {
		t.Fatalf("show %s: output type %T, want cli.ShowResult", id, shown)
	}
	d, _ := res.Fields["description"].(string)
	return len(d)
}

// TestDocClaim_MCP_DescriptionCap pins docs/spec.md "Length caps" for
// description on the MCP write paths (act-993498): act_create and
// act_update (description, and the merged result of description_append)
// accept exactly 1048576 bytes and reject 1048577 as a tool error naming
// the cap. Before the cap lived in the op validators, the MCP paths had
// no description cap at all.
func TestDocClaim_MCP_DescriptionCap(t *testing.T) {
	root := makeRepo(t)
	atCap := strings.Repeat("m", 1048576)
	overCap := atCap + "m"
	const wantMsg = "> 1048576 bytes"

	// act_create
	res, text := callTool(t, root, "act_create", map[string]any{"title": "at cap", "description": atCap, "no_commit": true})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("act_create with a 1048576-byte description rejected: %s", text)
	}
	res, text = callTool(t, root, "act_create", map[string]any{"title": "over cap", "description": overCap, "no_commit": true})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("act_create with a 1048577-byte description accepted: %s", text)
	}
	if !strings.Contains(text, wantMsg) {
		t.Errorf("act_create over-cap error does not name the cap (%q): %s", wantMsg, text)
	}

	// act_update description
	out, code := cli.RunCreate(root, cli.CreateOptions{Title: "update target", NoCommit: true})
	if code != 0 {
		t.Fatalf("seed create: code=%d out=%+v", code, out)
	}
	id := out.(cli.CreateResult).ID
	res, text = callTool(t, root, "act_update", map[string]any{"id": id, "description": overCap, "no_commit": true})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("act_update with a 1048577-byte description accepted: %s", text)
	}
	if !strings.Contains(text, wantMsg) {
		t.Errorf("act_update over-cap error does not name the cap (%q): %s", wantMsg, text)
	}
	// description_append: 1048572 existing bytes + "\n\n" + "xy" is
	// exactly the cap; one more append goes over.
	res, text = callTool(t, root, "act_update", map[string]any{"id": id, "description": atCap[:1048572], "no_commit": true})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("act_update seeding description: %s", text)
	}
	res, text = callTool(t, root, "act_update", map[string]any{"id": id, "description_append": "xy", "no_commit": true})
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("description_append landing exactly on 1048576 bytes rejected: %s", text)
	}
	if n := shownDescriptionLen(t, root, id); n != 1048576 {
		t.Fatalf("merged description is %d bytes, want 1048576", n)
	}
	res, text = callTool(t, root, "act_update", map[string]any{"id": id, "description_append": "q", "no_commit": true})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Fatalf("description_append past 1048576 bytes accepted: %s", text)
	}
	if !strings.Contains(text, wantMsg) {
		t.Errorf("description_append over-cap error does not name the cap (%q): %s", wantMsg, text)
	}
	if n := shownDescriptionLen(t, root, id); n != 1048576 {
		t.Fatalf("rejected append changed the description to %d bytes", n)
	}
}
