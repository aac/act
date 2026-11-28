package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDocClaim_OpFile_SingleLineNoFullID pins act-3d919a and the docs/spec.md
// claims that an op file is one line of minified canonical JSON (no
// indentation, no newline, top-level keys sorted) and that the create payload
// carries no full_id field.
func TestDocClaim_OpFile_SingleLineNoFullID(t *testing.T) {
	site := t.TempDir()
	runGit(t, site, "init", "-q", "-b", "main")
	configureSite(t, site, "doc@example.com", "doc")
	mustRunAct(t, site, 0, "init", "--json")
	mustRunAct(t, site, 0, "create", "op file format probe", "--json")

	found := 0
	for _, raw := range capauditReadOpFiles(t, site) {
		if strings.ContainsAny(raw, "\n\r") || strings.Contains(raw, `": `) || strings.Contains(raw, `, "`) {
			t.Errorf("op file is not single-line minified JSON: %q", raw)
		}
		var env map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatalf("parse op file: %v\n%s", err, raw)
		}
		if !strings.HasPrefix(raw, `{"hlc":`) {
			t.Errorf("top-level keys not sorted lexicographically (want hlc first): %q", raw)
		}
		if string(env["op_type"]) != `"create"` {
			continue
		}
		found++
		if strings.Contains(raw, "full_id") {
			t.Errorf("create op file contains full_id: %s", raw)
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(env["payload"], &payload); err != nil {
			t.Fatalf("parse payload: %v", err)
		}
		if _, ok := payload["full_id"]; ok {
			t.Errorf("create payload has a full_id key: %s", env["payload"])
		}
	}
	if found != 1 {
		t.Fatalf("found %d create op files, want 1", found)
	}
}
