package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestFbdMCPFileBlockerReportsPushDeferred: when the push of the new issue
// does not reach `origin`, `act_file_blocker` still succeeds and its result
// carries `push_deferred: true` and `push_deferred_reason`, as `act_create`
// and `act_block` do (act-a85bc9: the hand-built body copied only `pushed`
// and `warnings` from the create result, so the deferral was invisible).
func TestFbdMCPFileBlockerReportsPushDeferred(t *testing.T) {
	root := makeRealRepo(t)
	blocker := seedIssue(t, root, "fbd blocker")

	// No origin: nothing to defer, no key.
	if _, text := pdfCall(t, root, "act_file_blocker", map[string]any{
		"title": "fbd filed, no origin", "blocked_by": []string{blocker},
	}); strings.Contains(text, "push_deferred") {
		t.Errorf("act_file_blocker with no origin carries push_deferred: %s", text)
	}

	pdfAddMissingOrigin(t, root)
	for _, push := range []bool{false, true} {
		args := map[string]any{"title": "fbd filed, origin unreachable", "blocked_by": []string{blocker}}
		if push {
			args["push"] = true
		}
		out, text := pdfCall(t, root, "act_file_blocker", args)
		if !out.OK || !out.PushDeferred || out.PushDeferredReason == "" {
			t.Errorf("act_file_blocker (push=%v) with an unreachable origin = %s; want ok:true, push_deferred:true and a push_deferred_reason", push, text)
		}
		var body struct {
			ID     string `json:"id"`
			Pushed *bool  `json:"pushed"`
		}
		if err := json.Unmarshal([]byte(text), &body); err != nil {
			t.Fatalf("parse act_file_blocker result %q: %v", text, err)
		}
		if body.Pushed == nil || *body.Pushed {
			t.Errorf("act_file_blocker (push=%v) with an unreachable origin: pushed = %v, want the key present and false", push, body.Pushed)
		}
		// The issue and its edge were written despite the deferral.
		if shown, _ := json.Marshal(fnwShow(t, root, body.ID)["blocked_by"]); !strings.Contains(string(shown), blocker) {
			t.Errorf("blocked_by of %s after act_file_blocker = %s, want it to name %s", body.ID, shown, blocker)
		}
	}
}
