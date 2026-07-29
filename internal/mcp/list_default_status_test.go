package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aac/act/internal/cli"
)

// act-9dfdc1: `act list` — and with it act_list — listed every status,
// closed included. The MCP surface is the one most agents actually call,
// so the working-set default and its escape hatch have to hold here too,
// not just on the CLI.

// TestDocClaim_MCP_ListDefaultExcludesClosed pins the act_list schema claim
// at the wire boundary (tools/list) AND exercises the behaviour through a
// real tools/call, so a schema that advertises `all` while the handler
// drops it fails here.
func TestDocClaim_MCP_ListDefaultExcludesClosed(t *testing.T) {
	root := makeRepo(t)

	// --- schema half.
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

	var listDesc string
	var listProps map[string]any
	for _, raw := range tools {
		td, _ := raw.(map[string]any)
		if n, _ := td["name"].(string); n != "act_list" {
			continue
		}
		listDesc, _ = td["description"].(string)
		schema, _ := td["inputSchema"].(map[string]any)
		listProps, _ = schema["properties"].(map[string]any)
	}
	if listProps == nil {
		t.Fatal("act_list not found in tools/list")
	}
	if !strings.Contains(listDesc, "closed issues are excluded") {
		t.Errorf("act_list description does not state the default excludes closed: %q", listDesc)
	}
	if _, ok := listProps["all"].(map[string]any); !ok {
		t.Fatal("act_list schema does not advertise `all`")
	}

	// --- behaviour half.
	mk := func(title string) string {
		t.Helper()
		out, code := cli.RunCreate(root, cli.CreateOptions{Title: title, NoCommit: true})
		if code != 0 {
			t.Fatalf("create %q: code=%d out=%+v", title, code, out)
		}
		created, _ := out.(cli.CreateResult)
		if created.ID == "" {
			t.Fatalf("no id from create: %+v", out)
		}
		return created.ID
	}
	mk("mcp still open")
	done := mk("mcp already finished")
	if out, code := cli.RunClose(root, cli.CloseOptions{ID: done, Reason: "done", NoCommit: true}); code != 0 {
		t.Fatalf("close: code=%d out=%+v", code, out)
	}

	call := func(t *testing.T, args map[string]any) string {
		t.Helper()
		resp := runOne(t, root, false, map[string]any{
			"jsonrpc": "2.0",
			"id":      "call",
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "act_list",
				"arguments": args,
			},
		})
		if resp.Error != nil {
			t.Fatalf("tools/call error: %+v", resp.Error)
		}
		body, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		return string(body)
	}

	def := call(t, map[string]any{})
	if strings.Contains(def, "mcp already finished") {
		t.Errorf("default act_list included a closed issue: %s", def)
	}
	if !strings.Contains(def, "mcp still open") {
		t.Errorf("default act_list dropped the open issue: %s", def)
	}

	all := call(t, map[string]any{"all": true})
	if !strings.Contains(all, "mcp already finished") || !strings.Contains(all, "mcp still open") {
		t.Errorf("act_list all=true did not list every status: %s", all)
	}

	closed := call(t, map[string]any{"status": "closed"})
	if !strings.Contains(closed, "mcp already finished") {
		t.Errorf("act_list status=closed did not reach the closed issue: %s", closed)
	}
	if strings.Contains(closed, "mcp still open") {
		t.Errorf("act_list status=closed leaked an open issue: %s", closed)
	}
}
