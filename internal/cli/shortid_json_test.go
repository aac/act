package cli

import (
	"encoding/json"
	"testing"
)

// TestDocClaim_ShortID_OmittedWhenSameAsID asserts the response-shape claim
// docs/spec.md and the act skill make (act-8a6536): **`short_id` is emitted
// only when it differs from `id`; absent means `short_id == id`.**
//
// Asserted at the JSON boundary — the marshaled bytes a CLI `--json` consumer
// or an MCP client actually reads — not against the Go structs, which keep
// carrying the real short value for the human renderers. Both directions are
// pinned in one test on purpose: an omit rule that also drops the *informative*
// case would silently strip the only handle a caller has for an extended id.
func TestDocClaim_ShortID_OmittedWhenSameAsID(t *testing.T) {
	hasShortID := func(t *testing.T, v any) (string, bool) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %T: %v", v, err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("unmarshal %T: %v", v, err)
		}
		s, ok := m["short_id"].(string)
		return s, ok
	}

	// Identical (the normal case: ids are generated at the prefix floor, so
	// the shortest unique prefix IS the id) — the key must be absent.
	same := []any{
		ListedIssue{ID: "act-abc123", ShortID: "act-abc123", Title: "t"},
		ReadyIssue{ID: "act-abc123", ShortID: "act-abc123", Title: "t"},
		SearchMatch{ID: "act-abc123", ShortID: "act-abc123", Title: "t"},
		CloseResult{ID: "act-abc123", ShortID: "act-abc123"},
		DeleteResult{ID: "act-abc123", ShortID: "act-abc123"},
		ReopenResult{ID: "act-abc123", ShortID: "act-abc123"},
	}
	for _, v := range same {
		if got, ok := hasShortID(t, v); ok {
			t.Errorf("%T: short_id emitted (%q) despite being identical to id", v, got)
		}
	}

	// Different (an id extended past the floor to break a collision) — the
	// key must be present and carry the prefix, because now it is the only
	// place the short handle appears.
	differ := []any{
		ListedIssue{ID: "act-abc12345", ShortID: "act-abc123", Title: "t"},
		ReadyIssue{ID: "act-abc12345", ShortID: "act-abc123", Title: "t"},
		SearchMatch{ID: "act-abc12345", ShortID: "act-abc123", Title: "t"},
		CloseResult{ID: "act-abc12345", ShortID: "act-abc123"},
		DeleteResult{ID: "act-abc12345", ShortID: "act-abc123"},
		ReopenResult{ID: "act-abc12345", ShortID: "act-abc123"},
	}
	for _, v := range differ {
		got, ok := hasShortID(t, v)
		if !ok {
			t.Errorf("%T: short_id dropped even though it differs from id", v)
			continue
		}
		if got != "act-abc123" {
			t.Errorf("%T: short_id = %q, want act-abc123", v, got)
		}
	}
}

// TestDocClaim_ShortID_ShowEmitsPrefixForExtendedID drives the same rule
// through `act show` end to end: an id longer than the prefix floor renders
// a short_id, so the "absent means identical" contract never costs a caller
// the handle it needs.
func TestDocClaim_ShortID_ShowEmitsPrefixForExtendedID(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeShowCreateEnv(t, "act-abcd1234", 1700000000000, 0, "extended")
	writeOpFile(t, root, env, "2026-04", "create.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd1234", AsJSON: true})
	if code != 0 {
		t.Fatalf("RunShow exit = %d: %+v", code, out)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T, want ShowResult", out)
	}
	short, ok := res.ShowJSON()["short_id"].(string)
	if !ok {
		t.Fatalf("short_id missing for an extended id: %+v", res.ShowJSON())
	}
	if short == "act-abcd1234" {
		t.Errorf("short_id = %q; want a strictly shorter prefix", short)
	}
}
