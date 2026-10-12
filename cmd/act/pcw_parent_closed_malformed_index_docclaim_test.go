package main

import (
	"strings"
	"testing"
)

// pcwClosedParentMalformedSite is a tracker holding one closed issue, whose
// index was scribbled on after the close. It returns the closed issue's id.
func pcwClosedParentMalformedSite(t *testing.T) (dir, parent string) {
	t.Helper()
	dir = sp6Site(t)
	parent = createIssue(t, dir, "pcw parent")
	createIssue(t, dir, "pcw filler") // keeps index.db at two pages or more
	if out, stderr, code := runActIn(t, dir, "close", "--json", parent); code != 0 {
		t.Fatalf("close the parent: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
	}
	spwScribbleIndex(t, dir)
	return dir, parent
}

// TestDocClaim_Spec_CreateParentClosedOverMalformedIndex pins that a
// malformed index does not cost `act create --parent <closed id>` its
// parent_closed warning (act-533566): whether the parent is closed is read
// from the op log when the index cannot answer.
func TestDocClaim_Spec_CreateParentClosedOverMalformedIndex(t *testing.T) {
	t.Run("--json", func(t *testing.T) {
		dir, parent := pcwClosedParentMalformedSite(t)
		res, _ := wfxCreate(t, dir, "--parent", parent, "pcw child of a closed parent")
		if got := strings.Join(res.Warnings, ","); got != "parent_closed,index_update_failed" {
			t.Errorf("create --parent <closed> over a malformed index: warnings [%s], want [parent_closed,index_update_failed]", got)
		}
	})

	// The shape the ticket was observed in: --push in a tracker with no origin.
	t.Run("--json --push with no remote", func(t *testing.T) {
		dir, parent := pcwClosedParentMalformedSite(t)
		res, _ := wfxCreate(t, dir, "--parent", parent, "--push", "pcw pushed child of a closed parent")
		got := "," + strings.Join(res.Warnings, ",") + ","
		for _, want := range []string{"parent_closed", "index_update_failed", "push_no_remote"} {
			if !strings.Contains(got, ","+want+",") {
				t.Errorf("create --parent <closed> --push over a malformed index: warnings %v lack %q", res.Warnings, want)
			}
		}
	})

	// An open parent over the same broken index must not gain the warning.
	t.Run("open parent", func(t *testing.T) {
		dir, a, _ := spwMalformedSite(t)
		res, _ := wfxCreate(t, dir, "--parent", a, "pcw child of an open parent")
		if got := strings.Join(res.Warnings, ","); got != "index_update_failed" {
			t.Errorf("create --parent <open> over a malformed index: warnings [%s], want [index_update_failed]", got)
		}
	})
}
