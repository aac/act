package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// edgBreakOpLog drops a file that is not an op envelope into the issue's op
// directory, so folding that issue from the op log fails.
func edgBreakOpLog(t *testing.T, dir, id string) {
	t.Helper()
	shard := filepath.Join(dir, ".act", "ops", id, "edg")
	if err := os.MkdirAll(shard, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", shard, err)
	}
	if err := os.WriteFile(filepath.Join(shard, "edg-not-an-op.json"), []byte("{not an op"), 0o644); err != nil {
		t.Fatalf("write a broken op file: %v", err)
	}
}

// TestDocClaim_Spec_CreateParentCheckFailedWarns asserts the
// `parent_check_failed` sentence of the `act create` section in
// docs/spec.md (act-11d2e2 item 4): when neither the index nor the op log
// can say whether the `--parent` issue is closed, the create still lands and
// says the check was skipped. It used to proceed with no sign of it, which
// reads the same as "the parent is open".
func TestDocClaim_Spec_CreateParentCheckFailedWarns(t *testing.T) {
	t.Run("index and op log both unreadable", func(t *testing.T) {
		dir, parent := pcwClosedParentMalformedSite(t)
		edgBreakOpLog(t, dir, parent)

		res, stderr := wfxCreate(t, dir, "--parent", parent, "edg child of an unreadable parent")
		got := "," + strings.Join(res.Warnings, ",") + ","
		if !strings.Contains(got, ",parent_check_failed,") {
			t.Errorf("create --parent over a malformed index and a broken parent op log: warnings %v lack parent_check_failed\nstderr=%s", res.Warnings, stderr)
		}
		if strings.Contains(got, ",parent_closed,") {
			t.Errorf("create --parent whose closed-check could not run still reports parent_closed: %v", res.Warnings)
		}
	})

	t.Run("without --json it is a line on stderr", func(t *testing.T) {
		dir, parent := pcwClosedParentMalformedSite(t)
		edgBreakOpLog(t, dir, parent)

		_, stderr := pass5Run(t, dir, 0, "", "create", "--parent", parent, "edg human child of an unreadable parent")
		if !strings.Contains(stderr, "act create: warning: could not check whether the parent issue is closed") {
			t.Errorf("create --parent (no --json) whose closed-check could not run: stderr lacks the warning line:\n%s", stderr)
		}
	})

	// The op log alone answering is not a failed check (act-533566).
	t.Run("op log readable", func(t *testing.T) {
		dir, parent := pcwClosedParentMalformedSite(t)
		res, _ := wfxCreate(t, dir, "--parent", parent, "edg child of a closed parent")
		if got := strings.Join(res.Warnings, ","); got != "parent_closed,index_update_failed" {
			t.Errorf("create --parent <closed> over a malformed index: warnings [%s], want [parent_closed,index_update_failed]", got)
		}
	})
}
