package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// pnrWarnings marshals a tool result the way the server does and returns
// its `warnings` joined with commas ("" when the key is absent).
func pnrWarnings(t *testing.T, out any) string {
	t.Helper()
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal tool result %T: %v", out, err)
	}
	var body struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("tool result is not a JSON object: %v\n%s", err, data)
	}
	return strings.Join(body.Warnings, ",")
}

// TestPnrMCPPushNoRemoteWarningReachesTheClient: a write tool called with
// push:true in a tracker with no origin remote answers with the
// push_no_remote warning, as the CLI does — the write landed and the push
// the caller asked for did not happen (act-f4fd34).
func TestPnrMCPPushNoRemoteWarningReachesTheClient(t *testing.T) {
	root := makeRealRepo(t)
	srv := NewServer(root, false, nil, nil)

	t.Run("act_create", func(t *testing.T) {
		out, isErr := srv.callCreate(json.RawMessage(`{"title":"pnr mcp create","push":true}`))
		if isErr {
			t.Fatalf("callCreate: %+v", out)
		}
		if got := pnrWarnings(t, out); got != "push_no_remote" {
			t.Errorf("act_create push:true with no remote: warnings %q, want push_no_remote", got)
		}
	})

	t.Run("act_file_blocker", func(t *testing.T) {
		blocked := seedIssue(t, root, "pnr mcp blocked work")
		out, isErr := srv.callFileBlocker(json.RawMessage(`{"title":"pnr mcp blocker","blocked_by":["` + blocked + `"],"push":true}`))
		if isErr {
			t.Fatalf("callFileBlocker: %+v", out)
		}
		if got := pnrWarnings(t, out); got != "push_no_remote" {
			t.Errorf("act_file_blocker push:true with no remote: warnings %q, want push_no_remote", got)
		}
		// No push asked for, nothing to warn about.
		out, isErr = srv.callFileBlocker(json.RawMessage(`{"title":"pnr mcp blocker, no push","blocked_by":["` + blocked + `"]}`))
		if isErr {
			t.Fatalf("callFileBlocker: %+v", out)
		}
		if got := pnrWarnings(t, out); got != "" {
			t.Errorf("act_file_blocker without push: warnings %q, want none", got)
		}
	})

	t.Run("act_update", func(t *testing.T) {
		id := seedIssue(t, root, "pnr mcp update target")
		out, isErr := srv.callUpdate(json.RawMessage(`{"id":"` + id + `","priority":1,"push":true}`))
		if isErr {
			t.Fatalf("callUpdate push:true with no remote: %+v", out)
		}
		if got := pnrWarnings(t, out); got != "push_no_remote" {
			t.Errorf("act_update push:true with no remote: warnings %q, want push_no_remote", got)
		}
	})
}
