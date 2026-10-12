package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDocClaim_Help_ClaimFailedRowDoesNotNameThePush asserts the
// `claim_failed` row of `act help errors` (act-11d2e2 item 5). The row said
// "claim could not write or push"; since act-8ffc13 a push that does not
// reach origin defers a won claim instead of failing it, so the row names
// what still does fail one — and the behavior half checks that a claim
// whose push fails is not a claim_failed.
func TestDocClaim_Help_ClaimFailedRowDoesNotNameThePush(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runHelpTo(&stdout, &stderr, []string{"errors"}); code != 0 {
		t.Fatalf("act help errors: exit %d\nstderr=%s", code, stderr.String())
	}
	var row string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "claim_failed") {
			row = line
		}
	}
	if !strings.Contains(row, "claim could not be written, committed or rebased") {
		t.Errorf("act help errors: claim_failed row = %q; want it to say the claim could not be written, committed or rebased", row)
	}
	if strings.Contains(row, "push") {
		t.Errorf("act help errors: claim_failed row still names the push, which no longer fails a claim: %q", row)
	}

	dir := sp6Site(t)
	made, _ := wfxCreate(t, dir, "edg claim_failed row target")
	upuAddMissingOrigin(t, dir)
	res, _ := upuUpdate(t, dir, made.ID, "--claim", "--push")
	if !res.Claimed || res.Error != "" || !res.PushDeferred {
		t.Errorf("update --claim --push with an unreachable origin: claimed %v, error %q, push_deferred %v; want a won claim with the push deferred, not claim_failed",
			res.Claimed, res.Error, res.PushDeferred)
	}
}

// TestDocClaim_Spec_CreateParentTriesTheIndexButNeverFailsOnIt asserts the
// reworded sentence of the "Recovering from a malformed index" paragraph in
// docs/spec.md (act-11d2e2 item 6). It said `act create` does "not read the
// index first"; with `--parent` it does try the index before it writes, to
// see whether the parent is closed. What holds is that it never fails on
// that read: the create lands over a malformed index with or without
// `--parent`.
func TestDocClaim_Spec_CreateParentTriesTheIndexButNeverFailsOnIt(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	if strings.Contains(string(spec), "do not read the index first") {
		t.Errorf("docs/spec.md still says create does \"not read the index first\"; create --parent tries the index before writing")
	}

	dir, parent := pcwClosedParentMalformedSite(t)
	before := spwOpFiles(t, dir)
	plain, _ := wfxCreate(t, dir, "edg create over a malformed index")
	child, _ := wfxCreate(t, dir, "--parent", parent, "edg create --parent over a malformed index")
	if got := spwOpFiles(t, dir) - before; got != 2 {
		t.Errorf("two creates over a malformed index wrote %d op files, want 2", got)
	}
	for what, res := range map[string]wfxCreated{"create": plain, "create --parent": child} {
		if !strings.Contains(","+strings.Join(res.Warnings, ",")+",", ",index_update_failed,") {
			t.Errorf("%s over a malformed index: warnings %v lack index_update_failed", what, res.Warnings)
		}
	}
	// The --parent read happened before the write and was answered (from
	// the op log, the index being unreadable): the closed parent is named.
	if !strings.Contains(","+strings.Join(child.Warnings, ",")+",", ",parent_closed,") {
		t.Errorf("create --parent <closed> over a malformed index: warnings %v lack parent_closed", child.Warnings)
	}
}
