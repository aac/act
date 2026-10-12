package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/ids"
)

// claimsubjHeadSubject returns the subject of the nested .act/ repo's HEAD
// commit — the commit a write op just made.
func claimsubjHeadSubject(t *testing.T, root string) string {
	t.Helper()
	return strings.TrimSpace(runOut(t, filepath.Join(root, ".act"), "git", "log", "-1", "--format=%s"))
}

// TestDocClaim_Spec_ClaimCommitSubjectUsesSixHexShortID asserts the spec's
// claim-protocol line `git commit --no-verify -m "act-op: (<short-id>) claim"`
// at the boundary a reader relies on: the commit log of the nested .act/
// repo after a real claim through the CLI path (`act update --claim`).
//
// The short id is `act-` plus the first ids.MinShortHexLen (6) hex chars —
// the same marker every other op commit carries. The claim path used to
// truncate to 4 hex (`act-op: (act-f95d) claim`) while the same issue's close
// read `act-op: (act-f95d6e) close`, so two issues sharing a 4-hex prefix had
// indistinguishable claim subjects (act-263350).
func TestDocClaim_Spec_ClaimCommitSubjectUsesSixHexShortID(t *testing.T) {
	root := makeCreateRepo(t)
	out, code := RunCreate(root, CreateOptions{Title: "claim subject probe", Type: "task"})
	if code != 0 {
		t.Fatalf("RunCreate: code=%d out=%+v", code, out)
	}
	id := out.(CreateResult).ID

	hex := strings.TrimPrefix(id, "act-")
	if len(hex) < ids.MinShortHexLen {
		t.Fatalf("created id %q has %d hex chars, want at least %d", id, len(hex), ids.MinShortHexLen)
	}
	short := "act-" + hex[:ids.MinShortHexLen]
	if ids.MinShortHexLen != 6 {
		t.Fatalf("ids.MinShortHexLen = %d; the spec line and this test state a 6-hex short id", ids.MinShortHexLen)
	}

	if out, code := RunUpdate(root, UpdateOptions{ID: id, Claim: true, Isolated: true}); code != 0 {
		t.Fatalf("RunUpdate --claim: code=%d out=%+v", code, out)
	}

	want := "act-op: (" + short + ") claim"
	if got := claimsubjHeadSubject(t, root); got != want {
		t.Errorf("nested .act HEAD subject after claim = %q, want %q (issue id %s)", got, want, id)
	}

	// The claim and a later op on the same issue must carry the same
	// parenthesized marker, so a reader pairing op commits by short id sees
	// one issue, not two.
	if out, code := RunClose(root, CloseOptions{ID: id, Reason: "done", NoCode: true}); code != 0 {
		t.Fatalf("RunClose: code=%d out=%+v", code, out)
	}
	if got, want := claimsubjHeadSubject(t, root), "act-op: ("+short+") close"; got != want {
		t.Errorf("nested .act HEAD subject after close = %q, want %q", got, want)
	}
}
