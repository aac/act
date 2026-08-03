package mcp

import (
	"encoding/json"
	"testing"

	"github.com/aac/act/internal/cli"
)

// TestDocClaim_MCP_UpdateTitleTypeParent is act-3e21b8's third acceptance
// criterion: MCP `act_update` gains the same title / type / parent params
// the CLI does. Asserted at both layers that can drift independently — the
// advertised schema (what a client can construct a call from) and the
// handler (what the call actually does) — because a param in the schema
// that the handler drops on the floor is the exact shape of failure this
// ticket was filed about, one layer down.
func TestDocClaim_MCP_UpdateTitleTypeParent(t *testing.T) {
	root := makeRealRepo(t)
	srv := NewServer(root, false, nil, nil)

	// Layer 1: the advertised schema carries all three params.
	var props map[string]any
	for _, td := range allTools() {
		if td.Name == "act_update" {
			props, _ = td.InputSchema["properties"].(map[string]any)
		}
	}
	if props == nil {
		t.Fatal("act_update descriptor not found")
	}
	for _, param := range []string{"title", "type", "parent"} {
		if _, ok := props[param]; !ok {
			t.Errorf("act_update schema does not advertise %q: %+v", param, props)
		}
	}

	// Layer 2: the handler applies them.
	epic := seedIssue(t, root, "the mcp epic")
	id := seedIssue(t, root, "stale mcp title")

	if out, isErr := srv.callUpdate(json.RawMessage(`{"id":"` + id + `","title":"retitled through mcp","type":"bug","parent":"` + epic + `","isolated":true}`)); isErr {
		t.Fatalf("callUpdate: %+v", out)
	}
	fields := mcpShowFields(t, srv, id)
	if got, _ := fields["title"].(string); got != "retitled through mcp" {
		t.Errorf("title = %q, want %q", got, "retitled through mcp")
	}
	if got, _ := fields["type"].(string); got != "bug" {
		t.Errorf("type = %q, want %q", got, "bug")
	}
	if got, _ := fields["parent"].(string); got != epic {
		t.Errorf("parent = %q, want %q", got, epic)
	}

	// The validation the CLI applies is the same code path, so a bad
	// value is rejected here too rather than written.
	if out, isErr := srv.callUpdate(json.RawMessage(`{"id":"` + id + `","type":"feature","isolated":true}`)); !isErr {
		t.Errorf("callUpdate with type=feature should have failed; got %+v", out)
	}
	fields = mcpShowFields(t, srv, id)
	if got, _ := fields["type"].(string); got != "bug" {
		t.Errorf("a rejected type mutated the issue: type = %q", got)
	}

	// The empty string detaches, matching `act update --parent ""`.
	if out, isErr := srv.callUpdate(json.RawMessage(`{"id":"` + id + `","parent":"","isolated":true}`)); isErr {
		t.Fatalf("callUpdate (detach): %+v", out)
	}
	fields = mcpShowFields(t, srv, id)
	if got, _ := fields["parent"].(string); got == epic {
		t.Errorf("parent:\"\" did not detach; parent = %q", got)
	}
}

// mcpShowFields folds an issue and returns its rendered field map.
func mcpShowFields(t *testing.T, srv *Server, id string) map[string]any {
	t.Helper()
	out, code := cli.RunShow(srv.repoRoot, cli.ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("RunShow(%s): code=%d out=%+v", id, code, out)
	}
	res, ok := out.(cli.ShowResult)
	if !ok {
		t.Fatalf("RunShow returned %T", out)
	}
	return res.Fields
}
