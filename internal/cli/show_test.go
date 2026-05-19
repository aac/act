package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/op"
)

// makeShowCreateEnv returns a create-op envelope for a show test. The nonce
// is fixed at zeros so the per-payload hash is deterministic.
func makeShowCreateEnv(t *testing.T, id string, wallMs int64, logical uint32, title string) op.Envelope {
	t.Helper()
	priority := 1
	pl := op.CreatePayload{
		Title:    title,
		Type:     "task",
		Priority: &priority,
		Nonce:    "00000000000000000000000000000000",
	}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal create payload: %v", err)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// makeShowUpdateEnv returns an update_field op envelope that overrides
// `title` with newTitle.
func makeShowUpdateEnv(t *testing.T, id string, wallMs int64, logical uint32, field string, value any) op.Envelope {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	pl := op.UpdateFieldPayload{
		Field: field,
		Value: raw,
	}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal update payload: %v", err)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "update_field",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// makeShowTombstoneEnv returns a tombstone op envelope.
func makeShowTombstoneEnv(t *testing.T, id string, wallMs int64, logical uint32) op.Envelope {
	t.Helper()
	pl := op.TombstonePayload{DeletedAt: "2026-04-29T00:00:00Z"}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal tombstone payload: %v", err)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "tombstone",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// makeShowRedactEnv returns a legacy redact op envelope (the op-type was
// removed in act-8d1d; the envelope still parses but fold silently skips
// the op). Used by TestRunShow_HistoricalRedactOpSkipped.
func makeShowRedactEnv(t *testing.T, id string, wallMs int64, logical uint32, fieldPath string) op.Envelope {
	t.Helper()
	// Hand-roll the legacy redact payload — op.RedactPayload was removed
	// with the rest of the command, but the on-disk shape is documented
	// here so the test fixture matches what historical ops looked like.
	body := []byte(`{"field_path":"` + fieldPath + `"}`)
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "redact",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  "0123abcd",
		},
		NodeID: "0123abcd",
	}
}

// TestFormatShowHuman_CommitMarkerAndDescription asserts the show text mode
// renders commit_marker (the canonical (act-XXXX) string) and description
// (with truncation for long values). Regression coverage for act-10f7;
// these fields were JSON-only in v0.1, forcing agents to reach for jq.
func TestFormatShowHuman_CommitMarkerAndDescription(t *testing.T) {
	res := ShowResult{Fields: map[string]any{
		"id":          "act-1234567890abcdef",
		"title":       "demo",
		"status":      "open",
		"priority":    1,
		"type":        "task",
		"description": "first line\nsecond line\nthird line",
	}}
	out := FormatShowHuman(res)

	// MinShortHexLen widened to 6 in act-f9a0, so the commit marker for a
	// long id now carries 6 hex chars. Emission form switched to the
	// `Act-Id: act-XXXXXX` trailer in act-c4c5 (pre-migration was
	// `(act-XXXX)` subject-line form).
	if !strings.Contains(out, "commit_marker: Act-Id: act-123456") {
		t.Errorf("missing commit_marker line; got:\n%s", out)
	}
	if !strings.Contains(out, "description: first line") {
		t.Errorf("missing description line; got:\n%s", out)
	}
	// Multi-line descriptions: continuation lines must be indented so the
	// block is visibly part of the description value, not sibling fields.
	if !strings.Contains(out, "  second line") {
		t.Errorf("multi-line description not indented; got:\n%s", out)
	}
}

func TestFormatShowHuman_TruncatesLongDescription(t *testing.T) {
	long := strings.Repeat("a really verbose paragraph that goes on and on and on. ", 50)
	res := ShowResult{Fields: map[string]any{
		"id":          "act-1234567890abcdef",
		"title":       "demo",
		"status":      "open",
		"priority":    1,
		"type":        "task",
		"description": long,
	}}
	out := FormatShowHuman(res)
	if !strings.Contains(out, "(truncated; see --json") {
		t.Errorf("long description should be truncated with marker; got:\n%s", out)
	}
}

func TestFormatShowHuman_ShortDescriptionPassthrough(t *testing.T) {
	res := ShowResult{Fields: map[string]any{
		"id":          "act-1234567890abcdef",
		"title":       "demo",
		"status":      "open",
		"priority":    1,
		"type":        "task",
		"description": "short",
	}}
	out := FormatShowHuman(res)
	if strings.Contains(out, "(truncated") {
		t.Errorf("short description should not be truncated; got:\n%s", out)
	}
	if !strings.Contains(out, "description: short") {
		t.Errorf("short description missing; got:\n%s", out)
	}
}

// TestFormatShowHuman_FullDisablesDescriptionTruncation asserts the
// --full flag suppresses the truncation guard on description so the
// verbatim text renders in human format (act-3c89).
func TestFormatShowHuman_FullDisablesDescriptionTruncation(t *testing.T) {
	long := strings.Repeat("a really verbose paragraph that goes on and on and on. ", 50)
	res := ShowResult{
		Fields: map[string]any{
			"id":          "act-1234567890abcdef",
			"title":       "demo",
			"status":      "open",
			"priority":    1,
			"type":        "task",
			"description": long,
		},
		Full: true,
	}
	out := FormatShowHuman(res)
	if strings.Contains(out, "(truncated") {
		t.Errorf("--full should suppress truncation marker; got:\n%s", out)
	}
	// The verbatim long text must appear in full.
	if !strings.Contains(out, long) {
		t.Errorf("--full should render full description verbatim; missing in:\n%s", out)
	}
}

// TestFormatShowHuman_FullDisablesClosedReasonTruncation asserts that
// closed_reason renders verbatim in human format when --full is set
// (act-3c89).
func TestFormatShowHuman_FullDisablesClosedReasonTruncation(t *testing.T) {
	long := strings.Repeat("close reason narrative spanning many words. ", 30)
	res := ShowResult{
		Fields: map[string]any{
			"id":            "act-1234567890abcdef",
			"title":         "demo",
			"status":        "closed",
			"priority":      1,
			"type":          "task",
			"closed_reason": long,
		},
		Full: true,
	}
	out := FormatShowHuman(res)
	if strings.Contains(out, "(truncated") {
		t.Errorf("--full should suppress truncation marker on closed_reason; got:\n%s", out)
	}
	if !strings.Contains(out, long) {
		t.Errorf("--full should render full closed_reason verbatim; missing in:\n%s", out)
	}
}

// TestFormatShowHuman_NoFullPreservesTruncation asserts the absence of
// --full keeps the existing truncation guard active on description
// (regression coverage so the default path doesn't accidentally inherit
// the verbatim render; act-3c89).
func TestFormatShowHuman_NoFullPreservesTruncation(t *testing.T) {
	long := strings.Repeat("a really verbose paragraph that goes on and on and on. ", 50)
	res := ShowResult{Fields: map[string]any{
		"id":          "act-1234567890abcdef",
		"title":       "demo",
		"status":      "open",
		"priority":    1,
		"type":        "task",
		"description": long,
	}}
	out := FormatShowHuman(res)
	if !strings.Contains(out, "(truncated; see --json") {
		t.Errorf("default render should keep truncation marker; got:\n%s", out)
	}
}

func TestRunShow_HappyPath(t *testing.T) {
	root := makeRepoWithAct(t)

	// create then update_field title.
	createEnv := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "first")
	updateEnv := makeShowUpdateEnv(t, "act-abcd", 1700000010000, 0, "title", "second")

	writeOpFile(t, root, createEnv, "2026-04", "create.json")
	writeOpFile(t, root, updateEnv, "2026-04", "update.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T, want ShowResult", out)
	}
	if got := res.Fields["title"]; got != "second" {
		t.Errorf("title = %v, want second", got)
	}
	if got := res.Fields["id"]; got != "act-abcd" {
		t.Errorf("id = %v, want act-abcd", got)
	}
	if _, ok := res.Fields["short_id"].(string); !ok {
		t.Errorf("short_id missing or wrong type: %v", res.Fields["short_id"])
	}
}

func TestRunShow_PrefixResolution(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeShowCreateEnv(t, "act-abcd1234", 1700000000000, 0, "hello")
	writeOpFile(t, root, env, "2026-04", "create.json")

	// 4-char prefix should resolve.
	out, code := RunShow(root, ShowOptions{ID: "abcd"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T, want ShowResult", out)
	}
	if got := res.Fields["id"]; got != "act-abcd1234" {
		t.Errorf("id = %v, want act-abcd1234", got)
	}
}

// TestRunShow_ShortPrefixResolution covers act-6fca: sub-4-char prefixes that
// uniquely identify one issue must resolve successfully. Every doc that says
// "prefix ok" implies that e.g. `act show act-ab` works when no other id
// shares the `ab` hex prefix.
func TestRunShow_ShortPrefixResolution(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeShowCreateEnv(t, "act-abcd1234", 1700000000000, 0, "hello")
	writeOpFile(t, root, env, "2026-04", "create.json")

	for _, prefix := range []string{"a", "ab", "abc", "act-a", "act-ab", "act-abc"} {
		out, code := RunShow(root, ShowOptions{ID: prefix})
		if code != 0 {
			t.Errorf("prefix=%q: exit code = %d, want 0; out=%+v", prefix, code, out)
			continue
		}
		res, ok := out.(ShowResult)
		if !ok {
			t.Errorf("prefix=%q: output type = %T, want ShowResult", prefix, out)
			continue
		}
		if got := res.Fields["id"]; got != "act-abcd1234" {
			t.Errorf("prefix=%q: id = %v, want act-abcd1234", prefix, got)
		}
	}
}

// TestRunShow_ShortPrefixAmbiguous verifies that a sub-4-char prefix matching
// multiple issues returns id_ambiguous (not issue_not_found) with candidates.
func TestRunShow_ShortPrefixAmbiguous(t *testing.T) {
	root := makeRepoWithAct(t)
	a := makeShowCreateEnv(t, "act-ab001234", 1700000000000, 0, "a")
	b := makeShowCreateEnv(t, "act-ab005678", 1700000000001, 0, "b")
	writeOpFile(t, root, a, "2026-04", "a.json")
	writeOpFile(t, root, b, "2026-04", "b.json")

	// "ab" prefix matches both — must surface id_ambiguous, not not_found.
	// Exit 2 (usage error) per spec-v2.md universal exit-code table — see
	// act-8dcd. The TestRunShow_ShortPrefixAmbiguous test was added by
	// act-6fca's agent off pre-8dcd main and asserted the old exit 3.
	out, code := RunShow(root, ShowOptions{ID: "ab"})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	e, ok := out.(ShowErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want ShowErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Fatalf("candidates len = %d, want 2", len(e.Candidates))
	}
}

// TestRunShow_TombstonedViaPrefix covers the acceptance criterion that
// `act show <unique prefix of tombstoned issue>` returns tombstoned=true
// (not issue_not_found).
func TestRunShow_TombstonedViaPrefix(t *testing.T) {
	root := makeRepoWithAct(t)
	createEnv := makeShowCreateEnv(t, "act-dead1234", 1700000000000, 0, "doomed")
	tombEnv := makeShowTombstoneEnv(t, "act-dead1234", 1700000010000, 0)
	writeOpFile(t, root, createEnv, "2026-04", "create.json")
	writeOpFile(t, root, tombEnv, "2026-04", "tomb.json")

	// Resolve via unique prefix — must return tombstoned shape, not not_found.
	out, code := RunShow(root, ShowOptions{ID: "dead"})
	if code != 0 {
		t.Fatalf("prefix=dead: exit code = %d, want 0; out=%+v", code, out)
	}
	tomb, ok := out.(ShowTombstoned)
	if !ok {
		t.Fatalf("output type = %T, want ShowTombstoned", out)
	}
	if !tomb.Tombstoned {
		t.Errorf("Tombstoned = false, want true")
	}
	if tomb.ID != "act-dead1234" {
		t.Errorf("ID = %q, want act-dead1234", tomb.ID)
	}
}

func TestRunShow_AmbiguousPrefix(t *testing.T) {
	root := makeRepoWithAct(t)
	a := makeShowCreateEnv(t, "act-abcd1234", 1700000000000, 0, "a")
	b := makeShowCreateEnv(t, "act-abcd5678", 1700000000001, 0, "b")
	writeOpFile(t, root, a, "2026-04", "a.json")
	writeOpFile(t, root, b, "2026-04", "b.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd"})
	// Ambiguous prefix is a usage error per spec-v2.md universal exit-code
	// table; see resolve_helpers.go and act-8dcd.
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	e, ok := out.(ShowErrorOutput)
	if !ok {
		t.Fatalf("output type = %T, want ShowErrorOutput", out)
	}
	if e.Error != "id_ambiguous" {
		t.Errorf("error = %q, want id_ambiguous", e.Error)
	}
	if len(e.Candidates) != 2 {
		t.Fatalf("candidates len = %d, want 2", len(e.Candidates))
	}
}

func TestRunShow_UnknownID(t *testing.T) {
	root := makeRepoWithAct(t)
	env := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "hello")
	writeOpFile(t, root, env, "2026-04", "create.json")

	out, code := RunShow(root, ShowOptions{ID: "act-ffff"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(ShowErrorOutput)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q, want issue_not_found", e.Error)
	}
}

func TestRunShow_NoActDir(t *testing.T) {
	root := t.TempDir()
	out, code := RunShow(root, ShowOptions{ID: "act-abcd"})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
	e, ok := out.(ShowErrorOutput)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if e.Error != "not_in_git" {
		t.Errorf("error = %q, want not_in_git", e.Error)
	}
}

func TestRunShow_IncludeOps(t *testing.T) {
	root := makeRepoWithAct(t)
	createEnv := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "first")
	updateEnv := makeShowUpdateEnv(t, "act-abcd", 1700000010000, 0, "title", "second")
	writeOpFile(t, root, createEnv, "2026-04", "create.json")
	writeOpFile(t, root, updateEnv, "2026-04", "update.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd", IncludeOps: true})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T, want ShowResult", out)
	}
	if !res.IncludeOps {
		t.Errorf("IncludeOps = false, want true")
	}
	if got := len(res.Ops); got != 2 {
		t.Fatalf("len(Ops) = %d, want 2", got)
	}
	// Ops must be HLC-sorted: create wall < update wall.
	if res.Ops[0].OpType != "create" || res.Ops[1].OpType != "update_field" {
		t.Errorf("ops order = [%s %s], want [create update_field]", res.Ops[0].OpType, res.Ops[1].OpType)
	}
	// JSON shape contains ops.
	jsm := res.ShowJSON()
	if _, ok := jsm["ops"]; !ok {
		t.Errorf("ShowJSON missing ops key")
	}
}

// TestRunShow_IncludeOpsHumanFormat asserts that the human-format renderer
// emits an "ops:" section when --include-ops is set and omits it when the
// flag isn't set. Regression for act-b891: pre-fix the text-mode output was
// identical with and without --include-ops; ops only appeared in --json.
func TestRunShow_IncludeOpsHumanFormat(t *testing.T) {
	root := makeRepoWithAct(t)
	createEnv := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "first")
	updateEnv := makeShowUpdateEnv(t, "act-abcd", 1700000010000, 0, "title", "second")
	writeOpFile(t, root, createEnv, "2026-04", "create.json")
	writeOpFile(t, root, updateEnv, "2026-04", "update.json")

	// With --include-ops: "ops:" section + one line per op (HLC-sorted).
	out, code := RunShow(root, ShowOptions{ID: "act-abcd", IncludeOps: true})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	human := FormatShowHuman(out)
	if !strings.Contains(human, "ops:\n") {
		t.Errorf("human output missing 'ops:' section header: %q", human)
	}
	// Each op_type should appear on its own indented line. HLC-sorted, so
	// create comes before update_field.
	createIdx := strings.Index(human, "create")
	updateIdx := strings.Index(human, "update_field")
	if createIdx < 0 {
		t.Errorf("human output missing create op: %q", human)
	}
	if updateIdx < 0 {
		t.Errorf("human output missing update_field op: %q", human)
	}
	if createIdx >= 0 && updateIdx >= 0 && createIdx >= updateIdx {
		t.Errorf("ops not HLC-sorted: create at %d, update_field at %d", createIdx, updateIdx)
	}
	// Lines under "ops:" are indented (two spaces) — confirms the renderer
	// placed them inside the section rather than as sibling fields.
	if !strings.Contains(human, "  ") {
		t.Errorf("human output missing indented op lines: %q", human)
	}

	// Without --include-ops: no "ops:" section.
	out2, code2 := RunShow(root, ShowOptions{ID: "act-abcd"})
	if code2 != 0 {
		t.Fatalf("exit code = %d, want 0", code2)
	}
	human2 := FormatShowHuman(out2)
	if strings.Contains(human2, "ops:") {
		t.Errorf("human output without --include-ops should not contain 'ops:' section: %q", human2)
	}
}

func TestRunShow_TombstonedJSON(t *testing.T) {
	root := makeRepoWithAct(t)
	createEnv := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "doomed")
	tombEnv := makeShowTombstoneEnv(t, "act-abcd", 1700000010000, 0)
	writeOpFile(t, root, createEnv, "2026-04", "create.json")
	writeOpFile(t, root, tombEnv, "2026-04", "tomb.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd", AsJSON: true})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	tomb, ok := out.(ShowTombstoned)
	if !ok {
		t.Fatalf("output type = %T, want ShowTombstoned", out)
	}
	if !tomb.Tombstoned {
		t.Errorf("Tombstoned = false, want true")
	}
	if tomb.ID != "act-abcd" {
		t.Errorf("ID = %q, want act-abcd", tomb.ID)
	}
	// Round-trip the JSON to lock the wire shape.
	data, err := json.Marshal(tomb)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"tombstoned":true`) {
		t.Errorf("JSON missing tombstoned:true: %s", data)
	}
	// Human form prints "[deleted]".
	human := FormatShowHuman(tomb)
	if !strings.Contains(human, "[deleted]") {
		t.Errorf("human form missing [deleted]: %q", human)
	}
}

// makeShowClaimEnv returns a claim op envelope from the given nodeID.
func makeShowClaimEnv(t *testing.T, id string, wallMs int64, logical uint32, nodeID, assignee string) op.Envelope {
	t.Helper()
	pl := op.ClaimPayload{Assignee: assignee}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal claim payload: %v", err)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "claim",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  nodeID,
		},
		NodeID: nodeID,
	}
}

// makeShowCloseEnv returns a close op envelope from the given nodeID.
func makeShowCloseEnv(t *testing.T, id string, wallMs int64, logical uint32, nodeID, reason string) op.Envelope {
	t.Helper()
	pl := op.ClosePayload{Reason: reason}
	body, err := json.Marshal(pl)
	if err != nil {
		t.Fatalf("marshal close payload: %v", err)
	}
	return op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       id,
		Payload:       body,
		HLC: hlc.HLC{
			Wall:    wallMs,
			Logical: logical,
			NodeID:  nodeID,
		},
		NodeID: nodeID,
	}
}

// TestRunShow_ClosedByNode covers act-g001: a closed issue's show output
// must surface the node_id of the writer that emitted the close op so an
// auditor can answer "who closed this?" without dropping to act log.
func TestRunShow_ClosedByNode(t *testing.T) {
	root := makeRepoWithAct(t)

	const claimer = "1111aaaa"
	const closer = "2222bbbb"

	createEnv := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "to be closed")
	claimEnv := makeShowClaimEnv(t, "act-abcd", 1700000010000, 0, claimer, "alice")
	closeEnv := makeShowCloseEnv(t, "act-abcd", 1700000020000, 0, closer, "fixed")

	writeOpFile(t, root, createEnv, "2026-04", "create.json")
	writeOpFile(t, root, claimEnv, "2026-04", "claim.json")
	writeOpFile(t, root, closeEnv, "2026-04", "close.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; out=%+v", code, out)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T, want ShowResult", out)
	}
	if got := res.Fields["status"]; got != "closed" {
		t.Errorf("status = %v, want closed", got)
	}
	if got := res.Fields["closed_by_node"]; got != closer {
		t.Errorf("closed_by_node = %v, want %s", got, closer)
	}
	if got := res.Fields["assignee"]; got != "alice" {
		t.Errorf("assignee = %v, want alice (last claim drift, but preserved)", got)
	}
	// JSON wire shape carries closed_by_node.
	jsm := res.ShowJSON()
	if got, ok := jsm["closed_by_node"].(string); !ok || got != closer {
		t.Errorf("ShowJSON closed_by_node = %v, want %s", jsm["closed_by_node"], closer)
	}
	data, err := json.Marshal(jsm)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"closed_by_node":"`+closer+`"`) {
		t.Errorf("JSON missing closed_by_node=%s: %s", closer, data)
	}
}

// TestRunShow_OpenIssueNoClosedByNode covers the negative case: open issues
// must NOT carry a closed_by_node field (absent rather than empty string).
func TestRunShow_OpenIssueNoClosedByNode(t *testing.T) {
	root := makeRepoWithAct(t)
	createEnv := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "open")
	writeOpFile(t, root, createEnv, "2026-04", "create.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if _, has := res.Fields["closed_by_node"]; has {
		t.Errorf("open issue must not carry closed_by_node; got %v", res.Fields["closed_by_node"])
	}
	if _, has := res.Fields["closed_at"]; has {
		t.Errorf("open issue must not carry closed_at; got %v", res.Fields["closed_at"])
	}
}

// TestRunShow_HistoricalRedactOpSkipped asserts that fold silently skips
// historical "redact" ops left behind by the pre-act-8d1d command. The fold
// must not error, and the previously-redacted field must render in its
// original form (since the redact apply path was removed; the field is no
// longer masked).
func TestRunShow_HistoricalRedactOpSkipped(t *testing.T) {
	root := makeRepoWithAct(t)
	createEnv := makeShowCreateEnv(t, "act-abcd", 1700000000000, 0, "secret-title")
	redactEnv := makeShowRedactEnv(t, "act-abcd", 1700000010000, 0, "title")
	writeOpFile(t, root, createEnv, "2026-04", "create.json")
	writeOpFile(t, root, redactEnv, "2026-04", "redact.json")

	out, code := RunShow(root, ShowOptions{ID: "act-abcd"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (fold must silently skip legacy redact ops)", code)
	}
	res, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if got := res.Fields["title"]; got != "secret-title" {
		t.Errorf("title = %v, want %q (original value; redact op no longer masks)", got, "secret-title")
	}
}
