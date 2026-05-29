package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aac/act/internal/canonicaljson"
	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/hlc"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// TestRunDoctor_NoActDir: a git repo without `.act/` returns exit 3.
func TestRunDoctor_NoActDir(t *testing.T) {
	dir := t.TempDir()
	out, code := RunDoctor(dir, DoctorOptions{})
	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (out=%+v)", code, out)
	}
	if _, ok := out.(DoctorErrorOutput); !ok {
		t.Fatalf("output type = %T, want DoctorErrorOutput", out)
	}
}

// TestRunDoctor_Clean: a freshly-seeded repo with one create op produces no
// error-severity findings.
func TestRunDoctor_Clean(t *testing.T) {
	root := makeCreateRepo(t)
	if _, code := RunCreate(root, CreateOptions{Title: "A", Type: "task"}); code != 0 {
		t.Fatalf("seed: code=%d", code)
	}
	out, code := RunDoctor(root, DoctorOptions{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (out=%+v)", code, out)
	}
	res, ok := out.(DoctorResult)
	if !ok {
		t.Fatalf("output type = %T, want DoctorResult", out)
	}
	for _, f := range res.Findings {
		if f.Severity == "error" {
			t.Errorf("unexpected error finding: %+v", f)
		}
	}
}

// TestRunDoctor_DanglingDep: a synthetic add_dep op pointing at a nonexistent
// parent surfaces a dangling-deps finding.
func TestRunDoctor_DanglingDep(t *testing.T) {
	root := makeCreateRepo(t)
	createOut, code := RunCreate(root, CreateOptions{Title: "child", Type: "task"})
	if code != 0 {
		t.Fatalf("seed: code=%d", code)
	}
	childID := createOut.(CreateResult).ID

	// Hand-write an add_dep op for a phantom parent.
	writeRawOp(t, root, childID, "add_dep",
		map[string]string{"parent": "act-deadbeef", "edge_type": "blocks"},
		1)

	out, code := RunDoctor(root, DoctorOptions{Check: "dangling-deps"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "dangling-deps" && f.IssueID == childID {
			found = true
		}
	}
	if !found {
		t.Errorf("expected dangling-deps finding for %s, got %+v", childID, res.Findings)
	}
}

// TestRunDoctor_Cycle: A blocks B, B blocks A produces a cycle finding.
func TestRunDoctor_Cycle(t *testing.T) {
	root := makeCreateRepo(t)
	a := mustCreate(t, root, "A")
	b := mustCreate(t, root, "B")

	writeRawOp(t, root, a, "add_dep", map[string]string{"parent": b, "edge_type": "blocks"}, 1)
	writeRawOp(t, root, b, "add_dep", map[string]string{"parent": a, "edge_type": "blocks"}, 2)

	out, code := RunDoctor(root, DoctorOptions{Check: "cycle"})
	if code != 1 {
		t.Fatalf("exit code = %d, want 1 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	if len(res.Findings) == 0 {
		t.Fatalf("expected at least one cycle finding, got 0")
	}
	for _, f := range res.Findings {
		if f.Check != "cycle" || f.Severity != "error" {
			t.Errorf("unexpected finding: %+v", f)
		}
	}
}

// TestRunDoctor_UnknownOpVersion: a synthetic op file with op_version=2
// produces an unknown-op-version finding; --fix is ignored (still error).
func TestRunDoctor_UnknownOpVersion(t *testing.T) {
	root := makeCreateRepo(t)
	a := mustCreate(t, root, "A")

	// Write a raw bogus op file with op_version=2 so we sidestep envelope
	// validation. The doctor only consults the JSON header.
	paths := config.Layout(root)
	shard := op.ShardDir(paths.Ops, a, time.Now().UnixMilli())
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	bogus := []byte(`{"op_version":2,"schema_version":1,"writer_version":"0.1.0","op_type":"create","issue_id":"` + a + `","payload":{},"hlc":{"wall":"2026-04-29T00:00:00.000Z","logical":0,"node_id":"0123abcd"},"node_id":"0123abcd"}`)
	bogusPath := filepath.Join(shard, "2026-04-29T00:00:00.000Z-deadbeef-create.json")
	if err := os.WriteFile(bogusPath, bogus, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, fix := range []bool{false, true} {
		out, code := RunDoctor(root, DoctorOptions{Check: "unknown-op-version", Fix: fix})
		if code != 1 {
			t.Fatalf("fix=%v: exit code = %d, want 1 (out=%+v)", fix, code, out)
		}
		res := out.(DoctorResult)
		found := false
		for _, f := range res.Findings {
			if f.Check == "unknown-op-version" {
				found = true
			}
		}
		if !found {
			t.Errorf("fix=%v: expected unknown-op-version finding", fix)
		}
		// Confirm the bogus op file still exists (--fix is read-only here).
		if _, err := os.Stat(bogusPath); err != nil {
			t.Errorf("fix=%v: bogus op was removed: %v", fix, err)
		}
	}
}

// TestRunDoctor_IndexDivergence_Fix: corrupt the index, run with --fix, and
// confirm a re-run reports zero index-divergence findings.
func TestRunDoctor_IndexDivergence_Fix(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	paths := config.Layout(root)

	// Open the index, drop a row to force divergence.
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	if err := idx.ApplySchema(); err != nil {
		t.Fatalf("schema: %v", err)
	}
	if err := idx.Rebuild(paths.Ops); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if _, err := idx.DB().Exec(`DELETE FROM issues`); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	idx.Close()

	// First run without --fix: must surface a divergence finding (error).
	out, code := RunDoctor(root, DoctorOptions{Check: "index-divergence"})
	if code != 1 {
		t.Fatalf("pre-fix: exit = %d, want 1 (out=%+v)", code, out)
	}

	// Run with --fix: replaces the index, exit 0 (warn-only).
	out, code = RunDoctor(root, DoctorOptions{Check: "index-divergence", Fix: true})
	if code != 0 {
		t.Fatalf("fix: exit = %d, want 0 (out=%+v)", code, out)
	}

	// Re-run: zero findings.
	out, code = RunDoctor(root, DoctorOptions{Check: "index-divergence"})
	if code != 0 {
		t.Fatalf("post-fix: exit = %d, want 0 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	if len(res.Findings) != 0 {
		t.Errorf("post-fix findings = %+v, want none", res.Findings)
	}
}

// mustCreate is a tiny helper that creates an issue with the given title,
// failing the test on error and returning the new issue id.
func mustCreate(t *testing.T, root, title string) string {
	t.Helper()
	out, code := RunCreate(root, CreateOptions{Title: title, Type: "task"})
	if code != 0 {
		t.Fatalf("create %s: code=%d", title, code)
	}
	return out.(CreateResult).ID
}

// writeRawOp writes a hand-built op file under <root>/.act/ops/<issueID>/
// for tests that need synthetic add_dep / dangling edges. logical is used as
// a tiebreak so multiple raw writes within the same millisecond don't collide.
func writeRawOp(t *testing.T, root, issueID, opType string, payload map[string]string, logical uint32) {
	t.Helper()
	paths := config.Layout(root)
	cfg, err := config.ReadConfig(paths)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	stamp := hlc.HLC{
		Wall:    time.Now().UnixMilli(),
		Logical: logical,
		NodeID:  cfg.NodeID,
	}
	pjson, err := canonicaljson.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env := op.Envelope{
		OpVersion:     op.CurrentOpVersion,
		SchemaVersion: op.CurrentSchemaVersion,
		WriterVersion: op.WriterVersion,
		OpType:        opType,
		IssueID:       issueID,
		Payload:       pjson,
		HLC:           stamp,
		NodeID:        cfg.NodeID,
	}
	body, err := env.Marshal()
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	shard := op.ShardDir(paths.Ops, issueID, stamp.Wall)
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatal(err)
	}
	full, _ := env.FullHash()
	iso := time.UnixMilli(stamp.Wall).UTC().Format("2006-01-02T15:04:05.000Z")
	name := iso + "-" + full[:8] + "-" + opType + ".json"
	path := filepath.Join(shard, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Compile-time guard that DoctorResult is JSON-marshalable as documented.
func TestDoctorResult_JSONShape(t *testing.T) {
	res := DoctorResult{Findings: []Finding{{Check: "x", Severity: "warn", Message: "m"}}, Count: 1}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	if !stringsContains(s, `"findings"`) || !stringsContains(s, `"count":1`) {
		t.Errorf("marshalled = %s", s)
	}
}

func stringsContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// TestRunDoctor_NoCodeCloseSuppressed exercises case (b) of the
// reconcile-lite table (docs/coordination-plane-design.md v2.1): a
// closed issue with `no_code=true` on the close op should NOT surface
// an orphan-close warning even when there's no closing marker in the
// host log. The close op commits in the nested act repo (visible to
// the nested-log half of doctor's scan), but the *suppression* is
// specifically driven by the no_code flag — independent of whether
// the nested marker happens to be present.
func TestRunDoctor_NoCodeCloseSuppressed(t *testing.T) {
	root := makeCreateRepo(t)
	id := mustCreate(t, root, "tracking-issue")

	// Close with --no-code. The nested act repo will carry an op-commit
	// with `(act-XXXX) close`, so we also need to make sure that's the
	// only place the marker appears; the host log has no work commit
	// referencing this id.
	_, code := RunClose(root, CloseOptions{ID: id, NoCode: true})
	if code != 0 {
		t.Fatalf("close: code=%d", code)
	}

	out, code := RunDoctor(root, DoctorOptions{Check: "orphan-close"})
	if code != 0 {
		t.Fatalf("doctor exit = %d, want 0; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	for _, f := range res.Findings {
		if f.Check == "orphan-close" && f.IssueID == id {
			t.Errorf("expected no orphan-close for no-code close, got %+v", f)
		}
	}
}

// TestRunDoctor_StrictPromotesWarnings: when --strict is set, any warn
// finding is promoted to error and exit becomes 1.
//
// To trigger a real case-(b) warning we need an issue that's closed
// in act state but whose marker doesn't appear in EITHER the host log
// or the nested act log. Under Phase 1, every `act create` and
// `act close` writes to the nested log, so any "real" close already
// resolves. The synthetic path: hand-write create + close op files
// directly to disk (writeRawOp), which puts them in the fold but
// outside both logs.
func TestRunDoctor_StrictPromotesWarnings(t *testing.T) {
	root := makeCreateRepo(t)

	// Use a fixed synthetic id so the test doesn't depend on id
	// generation. The id needs to be a valid id-shape but not produced
	// via RunCreate so neither log carries the marker.
	id := "act-deadbeef"
	writeRawOp(t, root, id, "create",
		map[string]string{"title": "synth", "type": "task", "nonce": "00000000000000000000000000000000"},
		1)
	writeRawOp(t, root, id, "close", map[string]string{"reason": "test"}, 99)

	out, code := RunDoctor(root, DoctorOptions{Check: "orphan-close"})
	if code != 0 {
		t.Fatalf("non-strict exit = %d, want 0 (warn doesn't fail); out=%+v", code, out)
	}
	res := out.(DoctorResult)
	// Confirm there's at least one warn finding for this id.
	sawWarn := false
	for _, f := range res.Findings {
		if f.IssueID == id && f.Severity == "warn" {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Fatalf("expected warn for raw-close issue %s, got findings %+v", id, res.Findings)
	}

	// Now with --strict: same finding, but error severity, exit 1.
	out, code = RunDoctor(root, DoctorOptions{Check: "orphan-close", Strict: true})
	if code != 1 {
		t.Fatalf("strict exit = %d, want 1; out=%+v", code, out)
	}
	res = out.(DoctorResult)
	sawError := false
	for _, f := range res.Findings {
		if f.IssueID == id && f.Severity == "error" {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("expected error-severity finding under --strict, got %+v", res.Findings)
	}
}

// TestRunDoctor_GitignoreEffective: when .act/ is NOT gitignored from
// the host repo, the gitignore-effective probe errors.
func TestRunDoctor_GitignoreEffective_NotIgnored(t *testing.T) {
	root := makeCreateRepo(t)
	// makeCreateRepo writes a .gitignore that lists .act/. Truncate it
	// to drop the rule.
	gi := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(gi, []byte("# empty\n"), 0o644); err != nil {
		t.Fatalf("rewrite .gitignore: %v", err)
	}
	mustGit(t, root, "add", ".gitignore")
	mustGit(t, root, "commit", "-q", "--no-verify", "-m", "drop ignore")

	out, code := RunDoctor(root, DoctorOptions{Check: "gitignore-effective"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "gitignore-effective" && f.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected gitignore-effective error finding, got %+v", res.Findings)
	}
}

// TestRunDoctor_GitignoreEffective_Ignored: when .act/ IS gitignored,
// the probe returns no findings.
func TestRunDoctor_GitignoreEffective_Ignored(t *testing.T) {
	root := makeCreateRepo(t)
	// makeCreateRepo's .gitignore already lists `.act/`.
	out, code := RunDoctor(root, DoctorOptions{Check: "gitignore-effective"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (out=%+v)", code, out)
	}
	res := out.(DoctorResult)
	for _, f := range res.Findings {
		if f.Check == "gitignore-effective" {
			t.Errorf("expected no gitignore-effective finding, got %+v", f)
		}
	}
}

// TestRunDoctor_UnknownMarker_InternalAuthor: a marker referencing an
// unknown id authored by an internal contributor surfaces a case (d)
// warning.
func TestRunDoctor_UnknownMarker_InternalAuthor(t *testing.T) {
	root := makeCreateRepo(t)
	// Synthesize a work commit on the host repo carrying a marker for
	// an id that doesn't exist. The author is the same as the rest of
	// the history (internal).
	wfPath := filepath.Join(root, "junk.txt")
	if err := os.WriteFile(wfPath, []byte("noop\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, root, "add", "junk.txt")
	mustGit(t, root, "commit", "-q", "--no-verify", "-m", "noop",
		"-m", "Act-Id: act-deadbeef")

	out, code := RunDoctor(root, DoctorOptions{Check: "orphan-close"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (warn shouldn't fail without --strict); out=%+v",
			code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "orphan-close" && f.IssueID == "act-deadbeef" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected case-(d) finding for act-deadbeef, got %+v", res.Findings)
	}
}

// TestRunDoctor_UnknownMarker_ExternalAuthor: a marker referencing an
// unknown id authored by an external contributor is suppressed (fork-PR
// heuristic).
func TestRunDoctor_UnknownMarker_ExternalAuthor(t *testing.T) {
	root := makeCreateRepo(t)
	// Create a baseline of internal commits so InternalContributors has
	// a non-trivial set. makeCreateRepo already wrote one commit with
	// u@example.com.
	for i := 0; i < 3; i++ {
		f := filepath.Join(root, "internal.txt")
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		mustGit(t, root, "add", "internal.txt")
		mustGit(t, root, "commit", "-q", "--no-verify",
			"-m", "internal work", "--allow-empty")
	}

	// Now author a commit under a different email (external).
	mustGit(t, root, "config", "user.email", "fork@external.test")
	wfPath := filepath.Join(root, "external.txt")
	if err := os.WriteFile(wfPath, []byte("ext\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, root, "add", "external.txt")
	mustGit(t, root, "commit", "-q", "--no-verify",
		"-m", "external PR", "-m", "Act-Id: act-cafe1234")
	// Restore the internal email for any subsequent commits.
	mustGit(t, root, "config", "user.email", "u@example.com")

	out, code := RunDoctor(root, DoctorOptions{Check: "orphan-close"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	for _, f := range res.Findings {
		if f.IssueID == "act-cafe1234" {
			t.Errorf("expected external-PR marker to be suppressed, got %+v", f)
		}
	}
}

// TestRunDoctor_DanglingDeps_ClosedIssue_IsWarn asserts that a dangling dep
// edge on a fully-closed issue produces a warn-severity finding (not error),
// so `act doctor` exits 0. (act-48d57f)
//
// Scenario: create child, close it, then hand-write an add_dep op pointing
// at a phantom parent. The dangling edge is on a closed issue, so doctor
// should tolerate it as a cosmetic leftover.
func TestRunDoctor_DanglingDeps_ClosedIssue_IsWarn(t *testing.T) {
	root := makeCreateRepo(t)
	childOut, code := RunCreate(root, CreateOptions{Title: "closed-child", Type: "task"})
	if code != 0 {
		t.Fatalf("create: code=%d", code)
	}
	childID := childOut.(CreateResult).ID

	// Close the issue.
	_, code = RunClose(root, CloseOptions{ID: childID, NoCode: true})
	if code != 0 {
		t.Fatalf("close: code=%d", code)
	}

	// Hand-write a dangling dep pointing at an unknown parent.
	writeRawOp(t, root, childID, "add_dep",
		map[string]string{"parent": "act-deadbeef", "edge_type": "blocks"},
		99)

	out, code := RunDoctor(root, DoctorOptions{Check: "dangling-deps"})
	// Benign case: closed issue → exit 0 (warn, not error).
	if code != 0 {
		t.Fatalf("exit = %d, want 0 for dangling dep on closed issue; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "dangling-deps" && f.IssueID == childID {
			found = true
			if f.Severity != "warn" {
				t.Errorf("expected warn severity for closed-issue dangling dep, got %q", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected a dangling-deps finding for %s, got %+v", childID, res.Findings)
	}
}

// TestRunDoctor_DanglingDeps_OpenIssue_IsError asserts that a dangling dep
// edge on an open (non-closed) issue remains an error-severity finding and
// causes exit 1. (act-48d57f — still-error case)
func TestRunDoctor_DanglingDeps_OpenIssue_IsError(t *testing.T) {
	root := makeCreateRepo(t)
	childOut, code := RunCreate(root, CreateOptions{Title: "open-child", Type: "task"})
	if code != 0 {
		t.Fatalf("create: code=%d", code)
	}
	childID := childOut.(CreateResult).ID

	// Hand-write a dangling dep on the OPEN issue.
	writeRawOp(t, root, childID, "add_dep",
		map[string]string{"parent": "act-deadbeef", "edge_type": "blocks"},
		1)

	out, code := RunDoctor(root, DoctorOptions{Check: "dangling-deps"})
	// Real-problem case: open issue → exit 1 (error).
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for dangling dep on open issue; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "dangling-deps" && f.IssueID == childID {
			found = true
			if f.Severity != "error" {
				t.Errorf("expected error severity for open-issue dangling dep, got %q", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected error dangling-deps finding for %s, got %+v", childID, res.Findings)
	}
}

// TestRunDoctor_OrphanOps_TombstonedIssue_IsWarn asserts that orphan ops for a
// tombstoned/deleted issue produce a warn-severity finding (not error), so
// `act doctor` exits 0. (act-11986a)
//
// Scenario: hand-write a tombstone op for a synthetic id that has NO create op.
// This reproduces the state left after an issue is deleted in a repo that
// doesn't GC its op log (the financial-repo case).
func TestRunDoctor_OrphanOps_TombstonedIssue_IsWarn(t *testing.T) {
	root := makeCreateRepo(t)

	// Use a fixed synthetic id: only a tombstone op, no create op.
	tombID := "act-deaddead"
	writeRawOp(t, root, tombID, "tombstone", map[string]string{}, 1)

	out, code := RunDoctor(root, DoctorOptions{Check: "orphan-ops"})
	// Benign case: tombstoned issue → exit 0 (warn, not error).
	if code != 0 {
		t.Fatalf("exit = %d, want 0 for orphan-ops on tombstoned issue; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "orphan-ops" && f.IssueID == tombID {
			found = true
			if f.Severity != "warn" {
				t.Errorf("expected warn severity for tombstoned-issue orphan-ops, got %q", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected an orphan-ops finding for %s, got %+v", tombID, res.Findings)
	}
}

// TestRunDoctor_OrphanOps_LiveIssue_IsError asserts that orphan ops for a
// live (non-tombstoned) issue remain an error-severity finding and cause
// exit 1. (act-11986a — still-error case)
//
// Scenario: hand-write a non-create op for a synthetic id that has NO create
// op and NO tombstone op, so it appears as a genuinely corrupt orphan.
func TestRunDoctor_OrphanOps_LiveIssue_IsError(t *testing.T) {
	root := makeCreateRepo(t)

	// Write a random non-create op with no tombstone — genuinely orphaned.
	liveOrphanID := "act-cafecafe"
	writeRawOp(t, root, liveOrphanID, "update_field",
		map[string]string{"field": "title", "value": "ghost"}, 1)

	out, code := RunDoctor(root, DoctorOptions{Check: "orphan-ops"})
	// Real-problem case: live orphan → exit 1 (error).
	if code != 1 {
		t.Fatalf("exit = %d, want 1 for orphan-ops on live issue; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	found := false
	for _, f := range res.Findings {
		if f.Check == "orphan-ops" && f.IssueID == liveOrphanID {
			found = true
			if f.Severity != "error" {
				t.Errorf("expected error severity for live-issue orphan-ops, got %q", f.Severity)
			}
		}
	}
	if !found {
		t.Errorf("expected error orphan-ops finding for %s, got %+v", liveOrphanID, res.Findings)
	}
}
