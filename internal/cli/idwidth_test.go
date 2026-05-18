// Tests in this file are the acceptance suite for act-f9a0: widening the
// short-id generation floor from 4 to 6 hex chars ahead of the Phase 1
// nested-repo migration. They cover the three contract surfaces the ticket
// names:
//
//  1. Fresh-repo generation: newly minted ids are exactly MinShortHexLen
//     (6 since act-f9a0) hex chars wide.
//  2. Mixed-shape resolution: historical 4-hex ids in `.act/ops/` still
//     resolve via `act show`, while new ids generated alongside them are
//     6-hex.
//  3. Doctor marker grep: the orphan-close check matches both the
//     historical `(act-XXXX)` form (4 hex) AND the new `(act-XXXXXX)`
//     form (6 hex) on commits in the host git log.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/ids"
	"github.com/aac/act/internal/op"
)

// TestIDWidth_FreshRepoMintsAtFloor exercises acceptance criterion #1.
// `act create` on a clean repo must mint an id whose hex tail is exactly
// MinShortHexLen chars wide. Driven through the CLI surface — same path
// agents hit — so any future regression in generation lands here.
func TestIDWidth_FreshRepoMintsAtFloor(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "fresh", Type: "task"})
	if code != 0 {
		t.Fatalf("create: code=%d, out=%+v", code, out)
	}
	id := out.(CreateResult).ID
	hex := strings.TrimPrefix(id, "act-")
	if len(hex) != ids.MinShortHexLen {
		t.Errorf("new id %q has hex tail length %d, want %d (MinShortHexLen)",
			id, len(hex), ids.MinShortHexLen)
	}
	// Sanity: each hex char is lowercase 0-9 a-f.
	for _, r := range hex {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char %q in id %q", r, id)
		}
	}
	if !ids.IsValidID(id) {
		t.Errorf("IsValidID(%q) = false; generated id failed on-disk syntax", id)
	}
}

// TestIDWidth_FullCloseShowLoopAtFloor covers the create→close→show→doctor
// flow named in the ticket: every step downstream of the bumped floor must
// keep working. RunDoctor on a freshly closed issue should NOT surface an
// orphan-close finding because the close commit carries the 6-hex marker
// (`(act-XXXXXX)`) that the new doctor grep matches.
func TestIDWidth_FullCloseShowLoopAtFloor(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	// Marker hex on the issue should be exactly MinShortHexLen wide.
	if hl := len(strings.TrimPrefix(id, "act-")); hl != ids.MinShortHexLen {
		t.Fatalf("seeded id %q has hex length %d, want %d", id, hl, ids.MinShortHexLen)
	}

	// Close. The close path writes an op-commit `act-op: (act-XXXXXX) close`
	// which is what doctor's marker grep will look for.
	if _, code := RunClose(root, CloseOptions{ID: id, Reason: "test"}); code != 0 {
		t.Fatalf("close: code=%d", code)
	}

	// Show must still produce the issue with status=closed.
	showOut, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show: code=%d, out=%+v", code, showOut)
	}
	res := showOut.(ShowResult)
	if got, _ := res.Fields["status"].(string); got != "closed" {
		t.Errorf("show status = %q, want closed", got)
	}

	// Doctor must NOT report an orphan-close finding for this issue —
	// the close commit's `(act-XXXXXX)` marker should be found by the
	// grep. (Acceptance criterion #3, happy path for the new shape.)
	docOut, code := RunDoctor(root, DoctorOptions{Check: "orphan-close"})
	if code != 0 {
		t.Fatalf("doctor: code=%d, out=%+v", code, docOut)
	}
	docRes := docOut.(DoctorResult)
	for _, f := range docRes.Findings {
		if f.Check == "orphan-close" && f.IssueID == id {
			t.Errorf("unexpected orphan-close finding for new 6-hex id: %+v", f)
		}
	}
}

// TestIDWidth_HistoricalFourHexResolves exercises acceptance criterion #2:
// a hand-seeded 4-hex create op (representing an id minted before
// MinShortHexLen widened to 6) must still resolve through `act show`,
// alongside newer 6-hex ids generated in the same repo. This is the
// backwards-compat path that protects existing `.act/ops/` directories
// from the floor bump.
func TestIDWidth_HistoricalFourHexResolves(t *testing.T) {
	root := makeCreateRepo(t)

	// Seed a 4-hex id by hand-writing a synthetic create op. The on-disk
	// syntax (idPattern) still admits 4-hex ids per the act-f9a0 design.
	const historicalID = "act-aaaa"
	seedHistoricalCreateOp(t, root, historicalID)

	// New issues generated alongside must mint at the current floor.
	newOut, code := RunCreate(root, CreateOptions{Title: "new", Type: "task"})
	if code != 0 {
		t.Fatalf("create new: code=%d", code)
	}
	newID := newOut.(CreateResult).ID
	if hl := len(strings.TrimPrefix(newID, "act-")); hl != ids.MinShortHexLen {
		t.Errorf("new id %q has hex length %d, want %d", newID, hl, ids.MinShortHexLen)
	}

	// Show must find the historical 4-hex id by exact match.
	showOut, code := RunShow(root, ShowOptions{ID: historicalID})
	if code != 0 {
		t.Fatalf("show historical: code=%d, out=%+v", code, showOut)
	}
	res := showOut.(ShowResult)
	if gotID, _ := res.Fields["id"].(string); gotID != historicalID {
		t.Errorf("show historical id = %q, want %q", gotID, historicalID)
	}

	// Sub-prefix resolution of the historical id (4 → 3-char prefix)
	// must also work. This is the act-6fca-style backwards compat — any
	// unique prefix >= MinInputHexLen resolves.
	showOut2, code := RunShow(root, ShowOptions{ID: "act-aaa"})
	if code != 0 {
		t.Fatalf("show by prefix: code=%d, out=%+v", code, showOut2)
	}
	res2 := showOut2.(ShowResult)
	if gotID, _ := res2.Fields["id"].(string); gotID != historicalID {
		t.Errorf("show by prefix resolved to %q, want %q", gotID, historicalID)
	}

	// And the new id must resolve normally.
	showOut3, code := RunShow(root, ShowOptions{ID: newID})
	if code != 0 {
		t.Fatalf("show new: code=%d", code)
	}
	if gotID, _ := showOut3.(ShowResult).Fields["id"].(string); gotID != newID {
		t.Errorf("show new id = %q, want %q", gotID, newID)
	}
}

// TestIDWidth_DoctorMarkerScanMatchesBothForms exercises acceptance
// criterion #3: doctor's orphan-close marker grep must match both
// 4-hex (historical) markers AND 6+-hex (new) markers in the host git
// log. The test stages two closed issues — one historical 4-hex, one
// new 6-hex — synthesises real commits carrying each marker form, and
// asserts that doctor surfaces NO orphan-close finding for either.
func TestIDWidth_DoctorMarkerScanMatchesBothForms(t *testing.T) {
	root := makeCreateRepo(t)

	// --- Historical 4-hex issue ---
	const historicalID = "act-aaaa"
	const historicalShort = "act-aaaa" // exact marker form for sub-floor ids
	seedHistoricalCreateOp(t, root, historicalID)
	seedHistoricalCloseOp(t, root, historicalID)
	// Synthesise a real work commit carrying the 4-hex marker form.
	writeWorkCommit(t, root, "hist-work", "implement hist ("+historicalShort+")")

	// --- New 6-hex issue ---
	newOut, code := RunCreate(root, CreateOptions{Title: "newwork", Type: "task"})
	if code != 0 {
		t.Fatalf("create new: code=%d", code)
	}
	newID := newOut.(CreateResult).ID
	newShort := ShortIssueID(newID)
	if len(strings.TrimPrefix(newShort, "act-")) != ids.MinShortHexLen {
		t.Fatalf("new short %q is not %d hex wide", newShort, ids.MinShortHexLen)
	}
	if _, code := RunClose(root, CloseOptions{ID: newID, Reason: "test"}); code != 0 {
		t.Fatalf("close new: code=%d", code)
	}
	// Synthesise an additional real work commit so the test isn't
	// dependent on the auto-commits surfacing both forms.
	writeWorkCommit(t, root, "new-work", "implement new ("+newShort+")")

	// --- Doctor ---
	docOut, code := RunDoctor(root, DoctorOptions{Check: "orphan-close"})
	if code != 0 {
		t.Fatalf("doctor: code=%d, out=%+v", code, docOut)
	}
	docRes := docOut.(DoctorResult)
	for _, f := range docRes.Findings {
		if f.Check != "orphan-close" {
			continue
		}
		if f.IssueID == historicalID {
			t.Errorf("orphan-close fired on historical 4-hex id %q despite matching marker (%s); finding=%+v",
				historicalID, historicalShort, f)
		}
		if f.IssueID == newID {
			t.Errorf("orphan-close fired on new 6-hex id %q despite matching marker (%s); finding=%+v",
				newID, newShort, f)
		}
	}
}

// seedHistoricalCreateOp hand-writes a create op for a sub-floor (e.g.
// 4-hex) issue id. Used to simulate a `.act/ops/` directory carrying ids
// minted before MinShortHexLen widened. Bypasses the regular create path
// (which would generate at the current floor).
func seedHistoricalCreateOp(t *testing.T, root, issueID string) {
	t.Helper()
	paths := config.Layout(root)
	cfg, err := config.ReadConfig(paths)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	stamp := hlc.HLC{
		Wall:    time.Now().UnixMilli(),
		Logical: 0,
		NodeID:  cfg.NodeID,
	}
	payload := map[string]any{
		"title":       "historical",
		"description": "",
		"priority":    1,
		"type":        "task",
		"parent":      "",
		"accept":      []string{},
		"nonce":       "00112233445566778899aabbccddeeff",
	}
	pjson, err := canonicaljson.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "create",
		IssueID:       issueID,
		Payload:       pjson,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	writeRawEnvelopeAndCommit(t, root, env, "create", stamp)
}

// seedHistoricalCloseOp hand-writes a close op for a sub-floor issue id,
// taking a fresh HLC stamp so it sorts after the create. Pairs with
// seedHistoricalCreateOp; together they represent a fully-closed issue
// in a state where the ids are below the current generation floor.
func seedHistoricalCloseOp(t *testing.T, root, issueID string) {
	t.Helper()
	paths := config.Layout(root)
	cfg, err := config.ReadConfig(paths)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	stamp := hlc.HLC{
		Wall:    time.Now().UnixMilli() + 1, // strictly after create
		Logical: 0,
		NodeID:  cfg.NodeID,
	}
	payload := map[string]any{
		"reason": "test-historical-close",
	}
	pjson, err := canonicaljson.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal close payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        "close",
		IssueID:       issueID,
		Payload:       pjson,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	writeRawEnvelopeAndCommit(t, root, env, "close", stamp)
}

// writeRawEnvelopeAndCommit serialises env to its sharded op file, stages
// it, and commits with the canonical `act-op: (<short>) <opType>` subject
// — the same subject the regular write helpers produce. This makes the
// seeded ops indistinguishable from real ones at the doctor-grep layer.
func writeRawEnvelopeAndCommit(t *testing.T, root string, env op.Envelope, opType string, stamp hlc.HLC) {
	t.Helper()
	paths := config.Layout(root)
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	shard := op.ShardDir(paths.Ops, env.IssueID, stamp.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir shard: %v", err)
	}
	full, _ := env.FullHash()
	iso := time.UnixMilli(stamp.Wall).UTC().Format("2006-01-02T15:04:05.000Z")
	name := iso + "-" + full[:8] + "-" + opType + ".json"
	path := filepath.Join(shard, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write op: %v", err)
	}
	// Stage and commit with the canonical subject so the marker lands in
	// the commit log where doctor's grep will find it.
	mustGit(t, root, "add", path)
	subj := BuildOpCommitMessage(env)
	mustGit(t, root, "commit", "-q", "--no-verify", "-m", subj)
}

// writeWorkCommit synthesises a "real" code-touching commit (not an
// `act-op:` auto-commit) carrying the supplied subject. The subject must
// embed the issue's commit marker; doctor's grep keys on it. Used by the
// mixed-shape doctor test so each issue has at least one non-act-op commit
// to match against.
func writeWorkCommit(t *testing.T, root, filename, subject string) {
	t.Helper()
	path := filepath.Join(root, filename)
	if err := os.WriteFile(path, []byte("work\n"), 0o644); err != nil {
		t.Fatalf("write work file: %v", err)
	}
	mustGit(t, root, "add", filename)
	mustGit(t, root, "commit", "-q", "--no-verify", "-m", subject)
}
