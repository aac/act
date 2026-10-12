package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Doc-claim cover for the ninth stale pass over docs/spec.md (act-74cf82),
// asserted against the real binary. It builds on the earlier passes' helpers
// (spec_pass5..8_docclaim_test.go).
//
// What was stale: the `act init` section said init creates no commit, that
// `--force` rewrites config.json only if absent, and that init creates an
// empty index.db; the JSON examples of create, show, update, close and
// version showed keys the binary does not print; the envelope sentence named
// too few of the failures that omit `details`; and a comment in
// internal/cli/depadd.go said an ambiguous prefix "always returned 3".

// TestDocClaim_Spec_DepAddAmbiguousPrefixIsExit2 asserts the id_ambiguous
// row of the error table for `act dep add`, and the comment above
// resolveDepID in internal/cli/depadd.go: an ambiguous child or parent prefix
// is exit 2 with id_ambiguous, an id that matches nothing is exit 3 with
// issue_not_found.
func TestDocClaim_Spec_DepAddAmbiguousPrefixIsExit2(t *testing.T) {
	dir := sp6Site(t)
	a := createIssue(t, dir, "sp9 a")
	b := createIssue(t, dir, "sp9 b")
	prefix := pass5Twin(t, dir, a)
	if strings.HasPrefix(b, prefix) {
		t.Skipf("the two ids share the prefix %s; nothing unambiguous to pair it with", prefix)
	}
	for _, args := range [][]string{
		{prefix, b},
		{b, prefix},
		{b, "--blocked-by", prefix},
		{b, "--blocks", prefix},
	} {
		out, _ := pass5Run(t, dir, 2, "id_ambiguous", append([]string{"dep", "add", "--json"}, args...)...)
		if _, keys := sp8Envelope(t, "act dep add "+strings.Join(args, " "), out, "id_ambiguous"); keys != "candidates,prefix" {
			t.Errorf("act dep add %v: details keys %q, want candidates,prefix\n%s", args, keys, out)
		}
	}
	pass5Run(t, dir, 3, "issue_not_found", "dep", "add", "--json", pass5NoSuchID, b)
	pass5Run(t, dir, 3, "issue_not_found", "dep", "add", "--json", b, pass5NoSuchID)
	if out, _ := pass5Run(t, dir, 0, "", "show", "--json", b); !strings.Contains(out, `"blocked_by":[]`) || !strings.Contains(out, `"blocks":[]`) {
		t.Errorf("a refused dep add left an edge behind\n%s", out)
	}

	src, err := os.ReadFile(filepath.Join(sp6RepoRoot(t), "internal", "cli", "depadd.go"))
	if err != nil {
		t.Fatalf("read internal/cli/depadd.go: %v", err)
	}
	if strings.Contains(string(src), "always returned 3 for id_ambiguous") {
		t.Errorf("internal/cli/depadd.go still says an ambiguous prefix has always returned 3")
	}
}
