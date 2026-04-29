package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/op"
)

// makeCloseRepoWithIssue seeds a git repo + .act/ with one create op
// (synthesised via RunCreate) and returns (repoRoot, issueID).
func makeCloseRepoWithIssue(t *testing.T) (string, string) {
	t.Helper()
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "to-close", Type: "task"})
	if code != 0 {
		t.Fatalf("seed: code = %d, out=%+v", code, out)
	}
	return root, out.(CreateResult).ID
}

// TestRunClose_HappyPath: closing an open issue writes one close op,
// auto-commits with `(<short_id>)` in the subject, and exits 0.
func TestRunClose_HappyPath(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)

	out, code := RunClose(root, CloseOptions{ID: id})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(CloseResult)
	if !ok {
		t.Fatalf("output type = %T, want CloseResult", out)
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

	// Exactly one close op file under the issue's shard.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-close.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 close op file, got %d: %v", len(matches), matches)
	}

	// Commit subject must contain `(<short_id>)` so doctor's
	// orphan-close grep matches.
	subj := strings.TrimSpace(runOut(t, root, "git", "log", "-1", "--format=%s"))
	if !strings.Contains(subj, "("+res.ShortID+")") {
		t.Errorf("commit subject %q missing (%s)", subj, res.ShortID)
	}
	if !strings.HasPrefix(subj, "act-op: ") {
		t.Errorf("commit subject %q missing act-op: prefix", subj)
	}
}

// TestRunClose_AlreadyClosed: closing an already-closed issue is a
// no-op idempotent exit 0; no second close op is written.
func TestRunClose_AlreadyClosed(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)

	if _, code := RunClose(root, CloseOptions{ID: id}); code != 0 {
		t.Fatalf("first close: code = %d", code)
	}
	out, code := RunClose(root, CloseOptions{ID: id})
	if code != 0 {
		t.Fatalf("second close: code = %d, out=%+v", code, out)
	}
	res, ok := out.(CloseAlreadyClosed)
	if !ok {
		t.Fatalf("output type = %T, want CloseAlreadyClosed", out)
	}
	if !res.AlreadyClosed {
		t.Errorf("AlreadyClosed = false, want true")
	}
	if res.ID != id {
		t.Errorf("ID = %q, want %q", res.ID, id)
	}

	// Still exactly one close op on disk.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-close.json"))
	if len(matches) != 1 {
		t.Errorf("expected exactly 1 close op (idempotent), got %d: %v", len(matches), matches)
	}
}

// TestRunClose_NoCommit: --no-commit writes the op file but does not
// advance HEAD; the result envelope reports committed=false.
func TestRunClose_NoCommit(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)
	headBefore := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))

	out, code := RunClose(root, CloseOptions{ID: id, NoCommit: true})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(CloseResult)
	if !ok {
		t.Fatalf("type = %T, want CloseResult", out)
	}
	if res.Committed {
		t.Errorf("Committed = true, want false (--no-commit)")
	}

	headAfter := strings.TrimSpace(runOut(t, root, "git", "rev-parse", "HEAD"))
	if headAfter != headBefore {
		t.Errorf("expected no commit; HEAD %s -> %s", headBefore, headAfter)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-close.json"))
	if len(matches) != 1 {
		t.Errorf("expected 1 close op file written; got %d", len(matches))
	}
}

// TestRunClose_UnknownID: an id with no matching issue exits 3.
func TestRunClose_UnknownID(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunClose(root, CloseOptions{ID: "act-deadbeef"})
	if code != 3 {
		t.Fatalf("code = %d, want 3 (issue_not_found); out=%+v", code, out)
	}
	e, ok := out.(CloseErrorOutput)
	if !ok {
		t.Fatalf("type = %T, want CloseErrorOutput", out)
	}
	if e.Error != "issue_not_found" {
		t.Errorf("error = %q, want issue_not_found", e.Error)
	}
}

// TestRunClose_ReasonRecorded: --reason is persisted verbatim in the
// ClosePayload.reason field of the on-disk op envelope.
func TestRunClose_ReasonRecorded(t *testing.T) {
	root, id := makeCloseRepoWithIssue(t)

	const reason = "shipped"
	out, code := RunClose(root, CloseOptions{ID: id, Reason: reason})
	if code != 0 {
		t.Fatalf("code = %d, out=%+v", code, out)
	}
	res, ok := out.(CloseResult)
	if !ok {
		t.Fatalf("type = %T, want CloseResult", out)
	}
	if res.Reason != reason {
		t.Errorf("Reason = %q, want %q", res.Reason, reason)
	}

	// Read back the op file and inspect the payload.
	matches, _ := filepath.Glob(filepath.Join(root, ".act", "ops", id, "*", "*-close.json"))
	if len(matches) != 1 {
		t.Fatalf("expected 1 close op file, got %d", len(matches))
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read op: %v", err)
	}
	env, err := op.Unmarshal(body)
	if err != nil {
		t.Fatalf("unmarshal env: %v", err)
	}
	var p op.ClosePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if p.Reason != reason {
		t.Errorf("payload.Reason = %q, want %q", p.Reason, reason)
	}
}
