package mcp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// pdfToolResult is the part of a composed tool's result these tests read.
type pdfToolResult struct {
	OK                 bool   `json:"ok"`
	Claimed            bool   `json:"claimed"`
	PushDeferred       bool   `json:"push_deferred"`
	PushDeferredReason string `json:"push_deferred_reason"`
}

// pdfAddMissingOrigin points the tracker's origin at a local path that does
// not exist, so every push fails. Local only: no network.
func pdfAddMissingOrigin(t *testing.T, root string) {
	t.Helper()
	missing := filepath.Join(t.TempDir(), "pdf-no-such-origin.git")
	mustGitMCP(t, filepath.Join(root, ".act"), "remote", "add", "origin", missing)
}

// pdfCall drives one tools/call, wants a non-error result, and returns it
// parsed with its raw text.
func pdfCall(t *testing.T, root, tool string, args map[string]any) (pdfToolResult, string) {
	t.Helper()
	res, text := callTool(t, root, tool, args)
	t.Logf("%s %v: isError=%v text=%s", tool, args, res["isError"], text)
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("%s %v is an error result: %s", tool, args, text)
	}
	var out pdfToolResult
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse %s result %q: %v", tool, text, err)
	}
	return out, text
}

// TestDocClaim_Spec_PdfMCPBlockReportsPushDeferred asserts the `act_block`
// sentence in docs/spec.md: when the push of the edge does not reach
// `origin`, the call still succeeds and its result carries `push_deferred:
// true` and `push_deferred_reason` (act-087b2b: it reported only the
// no-remote case).
func TestDocClaim_Spec_PdfMCPBlockReportsPushDeferred(t *testing.T) {
	root := makeRealRepo(t)
	a := seedIssue(t, root, "pdf mcp blocked")
	b := seedIssue(t, root, "pdf mcp blocker")
	c := seedIssue(t, root, "pdf mcp blocked, no origin")

	// No origin: nothing to defer, no key.
	if _, text := pdfCall(t, root, "act_block", map[string]any{"id": c, "blocked_by": b}); strings.Contains(text, "push_deferred") {
		t.Errorf("act_block with no origin carries push_deferred: %s", text)
	}

	pdfAddMissingOrigin(t, root)
	out, text := pdfCall(t, root, "act_block", map[string]any{"id": a, "blocked_by": b})
	if !out.OK || !out.PushDeferred || out.PushDeferredReason == "" {
		t.Errorf("act_block with an unreachable origin = %s; want ok:true, push_deferred:true and a push_deferred_reason", text)
	}
	if shown, _ := json.Marshal(fnwShow(t, root, a)["blocked_by"]); !strings.Contains(string(shown), b) {
		t.Errorf("blocked_by of the blocked issue after act_block = %s, want it to name %s", shown, b)
	}

	// push:true is the same deferral, not an error.
	d := seedIssue(t, root, "pdf mcp blocked, push asked for")
	pushed, pushedText := pdfCall(t, root, "act_block", map[string]any{"id": d, "blocked_by": b, "push": true})
	if !pushed.OK || !pushed.PushDeferred || pushed.PushDeferredReason == "" {
		t.Errorf("act_block push:true with an unreachable origin = %s; want ok:true, push_deferred:true and a push_deferred_reason", pushedText)
	}
}

// TestDocClaim_Spec_PdfMCPNextCarriesTheClaimsPushDeferred asserts the
// `act_next` sentence on a claim `origin` has not seen (act-c30acd; act-087b2b
// had it answer claimed:true): the tool returns the claim_unconfirmed result,
// push_deferred and push_deferred_reason included, as an error result, and
// tries no other candidate.
func TestDocClaim_Spec_PdfMCPNextCarriesTheClaimsPushDeferred(t *testing.T) {
	root := makeRealRepo(t)
	id := seedIssue(t, root, "pdf mcp next target")
	seedIssue(t, root, "pdf mcp next second target")
	pdfAddMissingOrigin(t, root)

	res, text := callTool(t, root, "act_next", map[string]any{})
	t.Logf("act_next: isError=%v text=%s", res["isError"], text)
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("act_next with an unreachable origin is not an error result: %s", text)
	}
	var out struct {
		pdfToolResult
		Error string `json:"error"`
		ID    string `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("parse act_next result %q: %v", text, err)
	}
	if out.Claimed || out.Error != "claim_unconfirmed" || out.ID == "" || !out.PushDeferred || out.PushDeferredReason == "" {
		t.Errorf("act_next with an unreachable origin = %s; want claimed:false, claim_unconfirmed, an id, push_deferred:true and a push_deferred_reason", text)
	}
	claims, _ := filepath.Glob(filepath.Join(root, ".act", "ops", "*", "*", "*-claim.json"))
	if len(claims) != 1 {
		t.Errorf("act_next wrote %d claim ops, want 1: it must stop on an unconfirmed claim, not try the next candidate (first seeded %s)", len(claims), id)
	}
}
