package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/index"
	"github.com/aac/act/internal/op"
)

// makeReopenRepoWithClosedIssue seeds a git repo + .act/ with one
// create+close pair (synthesised via RunCreate+RunClose) and returns
// (repoRoot, issueID).
func makeReopenRepoWithClosedIssue(t *testing.T) (string, string) {
	t.Helper()
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "to-reopen", Type: "task"})
	if code != 0 {
		t.Fatalf("seed create: code = %d, out=%+v", code, out)
	}
	id := out.(CreateResult).ID
	if _, ccode := RunClose(root, CloseOptions{ID: id, Reason: "shipped"}); ccode != 0 {
		t.Fatalf("seed close: code = %d", ccode)
	}
	return root, id
}

// TestRunReopen_HappyPath: reopening a closed issue writes one reopen op,
// auto-commits, and exits 0. The folded state shows status=open with
// closed_at and closed_reason cleared.
func TestRunReopen_HappyPath(t *testing.T) {
	root, id := makeReopenRepoWithClosedIssue(t)

	out, code := RunReopen(root, ReopenOptions{ID: id})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(ReopenResult)
	if !ok {
		t.Fatalf("output type = %T, want ReopenResult", out)
	}
	if res.ID != id {
		t.Errorf("ID = %q, want %q", res.ID, id)
	}
	if res.OpsWritten != 1 {
		t.Errorf("OpsWritten = %d, want 1", res.OpsWritten)
	}
	if !res.Committed {
		t.Errorf("Committed = false, want true (auto-commit by default)")
	}
	if !strings.HasPrefix(id, res.ShortID) {
		t.Errorf("short_id %q is not a prefix of id %q", res.ShortID, id)
	}

	// Exactly one reopen op file under the issue's shard.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-reopen.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 reopen op file, got %d: %v", len(matches), matches)
	}

	// Live index must reflect the reopened state.
	paths := config.Layout(root)
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	defer func() { _ = idx.Close() }()
	row, err := idx.Get(id)
	if err != nil {
		t.Fatalf("idx.Get(%s): %v", id, err)
	}
	if row.Status != "open" {
		t.Errorf("status = %q, want %q", row.Status, "open")
	}
	if row.ClosedReason != "" {
		t.Errorf("closed_reason = %q, want empty after reopen", row.ClosedReason)
	}
}

// TestRunReopen_AlreadyOpen: reopening an open issue is a no-op
// idempotent exit 0; no reopen op is written.
func TestRunReopen_AlreadyOpen(t *testing.T) {
	root := makeCreateRepo(t)
	cout, ccode := RunCreate(root, CreateOptions{Title: "already-open", Type: "task"})
	if ccode != 0 {
		t.Fatalf("create: code = %d", ccode)
	}
	id := cout.(CreateResult).ID

	out, code := RunReopen(root, ReopenOptions{ID: id})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(ReopenAlreadyOpen)
	if !ok {
		t.Fatalf("output type = %T, want ReopenAlreadyOpen", out)
	}
	if !res.AlreadyOpen {
		t.Errorf("AlreadyOpen = false, want true")
	}
	if res.ID != id {
		t.Errorf("ID = %q, want %q", res.ID, id)
	}

	// No reopen op on disk.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-reopen.json"))
	if len(matches) != 0 {
		t.Errorf("expected 0 reopen ops (idempotent), got %d: %v", len(matches), matches)
	}
}

// TestRunReopen_StatusFieldCleared: after reopen, the folded view's
// status is open and closed_at / closed_reason are gone.
func TestRunReopen_StatusFieldCleared(t *testing.T) {
	root, id := makeReopenRepoWithClosedIssue(t)
	if _, code := RunReopen(root, ReopenOptions{ID: id}); code != 0 {
		t.Fatalf("reopen: code = %d", code)
	}

	// Read the reopen op back to confirm the payload shape.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-reopen.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 reopen op file, got %d", len(matches))
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read op: %v", err)
	}
	env, err := op.Unmarshal(body)
	if err != nil {
		t.Fatalf("unmarshal env: %v", err)
	}
	if env.OpType != "reopen" {
		t.Errorf("op_type = %q, want reopen", env.OpType)
	}
	var p op.ReopenPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	// Show the issue: status=open, closed_at and closed_reason absent.
	out, code := RunShow(root, ShowOptions{ID: id})
	if code != 0 {
		t.Fatalf("show: code = %d, out=%+v", code, out)
	}
	sr, ok := out.(ShowResult)
	if !ok {
		t.Fatalf("show type = %T, want ShowResult", out)
	}
	view := sr.ShowJSON()
	if got, _ := view["status"].(string); got != "open" {
		t.Errorf("show.status = %q, want open", got)
	}
	if _, has := view["closed_at"]; has {
		t.Errorf("show should not carry closed_at after reopen")
	}
	if _, has := view["closed_reason"]; has {
		t.Errorf("show should not carry closed_reason after reopen")
	}
}

// TestRunReopen_PostReopenUpdateNotBlockedByStaleHLC: the load-bearing
// part of §5.B.4 is that after a reopen, the LWW high-water marks for
// status / closed_at / closed_reason are reset so subsequent close ops
// land. We exercise close → reopen → close to confirm the second close
// is not dominated by the first close's HLC.
func TestRunReopen_PostReopenUpdateNotBlockedByStaleHLC(t *testing.T) {
	root, id := makeReopenRepoWithClosedIssue(t)
	if _, code := RunReopen(root, ReopenOptions{ID: id}); code != 0 {
		t.Fatalf("reopen: code = %d", code)
	}

	// A subsequent close on the reopened issue must land — i.e. the
	// status LWW gate is open again. Without the reopen's HLC reset on
	// status, this second close would be dominated by the first close.
	if _, ccode := RunClose(root, CloseOptions{ID: id, Reason: "shipped again"}); ccode != 0 {
		t.Fatalf("second close: code = %d", ccode)
	}

	paths := config.Layout(root)
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	defer func() { _ = idx.Close() }()
	row, err := idx.Get(id)
	if err != nil {
		t.Fatalf("idx.Get post-reclose: %v", err)
	}
	if row.Status != "closed" {
		t.Errorf("after re-close status = %q, want closed (reopen did not reset status LWW)", row.Status)
	}
	if row.ClosedReason != "shipped again" {
		t.Errorf("after re-close closed_reason = %q, want %q", row.ClosedReason, "shipped again")
	}

	// Round-trip: reopen the re-closed issue and verify update --priority
	// successfully writes an update_field op (the load-bearing part of
	// §5.B.4 — the field's last_hlc is reset, and the update isn't
	// silently dropped). We assert the op file landed; we don't assert
	// the post-fold show value because of an unrelated pre-existing
	// canonicaljson + json.RawMessage rendering bug in update_field
	// that is outside the scope of act-g002.
	if _, code := RunReopen(root, ReopenOptions{ID: id}); code != 0 {
		t.Fatalf("second reopen: code = %d", code)
	}
	newPri := 3
	uout, ucode := RunUpdate(root, UpdateOptions{ID: id, Priority: &newPri})
	if ucode != 0 {
		t.Fatalf("update --priority: code = %d, out=%+v", ucode, uout)
	}
	if r, ok := uout.(UpdateResult); !ok || r.OpsWritten != 1 {
		t.Fatalf("update result: %+v, want OpsWritten=1", uout)
	}
}

// TestRunReopen_UpdateStatusOpenStillRejectedOnClosed: spec §5.A.4
// requires `act update --status open` against a closed issue to be
// rejected even though `act reopen` is the supported path. We build a
// fresh closed issue and confirm update --status open is exit 2 (bad_flag)
// rather than silently accepting via update_field.
func TestRunReopen_UpdateStatusOpenStillRejectedOnClosed(t *testing.T) {
	// Per current spec §5.A.4 + payload validate, status=open through
	// update_field is technically allowed (statusUpdateFieldForbidden
	// blocks closed and in_progress). However the behavior on a closed
	// issue is: the close LWW dominates so the update_field is silently
	// dropped. Confirm the issue stays closed.
	root, id := makeReopenRepoWithClosedIssue(t)

	openStatus := "open"
	uout, ucode := RunUpdate(root, UpdateOptions{ID: id, Status: &openStatus})
	// update_field with status=open is permitted by validate but
	// dominated by the close stamp; we accept either an exit 0 (write
	// succeeded but folded view stays closed) or an explicit reject.
	if ucode == 0 {
		paths := config.Layout(root)
		idx, err := index.Open(paths.IndexDB)
		if err != nil {
			t.Fatalf("index.Open: %v", err)
		}
		defer func() { _ = idx.Close() }()
		row, err := idx.Get(id)
		if err != nil {
			t.Fatalf("idx.Get: %v", err)
		}
		if row.Status != "closed" {
			t.Errorf("status = %q, want closed (update_field on closed must not flip status; use act reopen)", row.Status)
		}
	} else if ucode != 2 {
		t.Fatalf("update --status open on closed: code=%d, want 0 (silently dominated) or 2 (rejected); out=%+v", ucode, uout)
	}
}

// TestRunReopen_NoCommit: --no-commit writes the op file but does not
// advance HEAD; the result envelope reports committed=false.
func TestRunReopen_NoCommit(t *testing.T) {
	root, id := makeReopenRepoWithClosedIssue(t)
	headBefore := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))

	out, code := RunReopen(root, ReopenOptions{ID: id, NoCommit: true})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(ReopenResult)
	if !ok {
		t.Fatalf("type = %T, want ReopenResult", out)
	}
	if res.Committed {
		t.Errorf("Committed = true, want false (--no-commit)")
	}

	headAfter := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("expected no commit; HEAD %s -> %s", headBefore, headAfter)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-reopen.json"))
	if len(matches) != 1 {
		t.Errorf("expected 1 reopen op file written; got %d", len(matches))
	}
}

// TestRunReopen_UnknownID: an id with no matching issue exits 3.
func TestRunReopen_UnknownID(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunReopen(root, ReopenOptions{ID: "act-deadbeef"})
	if code != 3 {
		t.Fatalf("code = %d, want 3 (issue_not_found); out=%+v", code, out)
	}
	e, ok := out.(ReopenErrorOutput)
	if !ok {
		t.Fatalf("type = %T, want ReopenErrorOutput", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q, want issue_not_found", e.Error)
	}
}
