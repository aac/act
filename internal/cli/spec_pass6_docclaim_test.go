package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Doc-claim cover for the sixth stale pass over docs/spec.md (act-f7a994),
// the half that needs allChecks. The binary-boundary half lives in
// cmd/act/spec_pass6_docclaim_test.go.
//
// The spec's doctor check table had no row for four checks the binary runs,
// and the test plan's check list named eight of fifteen. Both lists are now
// compared with the code's own list, so a check added to allChecks without a
// row (or a row left behind after a check is removed) turns this red.

// sp6SpecBody reads docs/spec.md.
func sp6SpecBody(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRootForDocClaim(t), "docs", "spec.md"))
	if err != nil {
		t.Fatalf("read docs/spec.md: %v", err)
	}
	return string(body)
}

// sp6Between returns the text of body from the first occurrence of start up
// to the next occurrence of end after it.
func sp6Between(t *testing.T, body, start, end string) string {
	t.Helper()
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("docs/spec.md has no %q", start)
	}
	rest := body[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("docs/spec.md has no %q after %q", end, start)
	}
	return rest[:j]
}

// sp6WantChecks is every name `act doctor --check` accepts: allChecks plus
// fold-error, sorted.
func sp6WantChecks() []string {
	want := append([]string{CheckFoldError}, allChecks...)
	sort.Strings(want)
	return want
}

// sp6CompareChecks fails when got (in document order) is not exactly the
// accepted check names, each once.
func sp6CompareChecks(t *testing.T, where string, got []string) {
	t.Helper()
	seen := map[string]int{}
	for _, name := range got {
		seen[name]++
	}
	for name, n := range seen {
		if n > 1 {
			t.Errorf("%s lists %q %d times", where, name, n)
		}
		if !validChecks[name] {
			t.Errorf("%s lists %q, which `act doctor --check` does not accept", where, name)
		}
	}
	for _, name := range sp6WantChecks() {
		if seen[name] == 0 {
			t.Errorf("%s has no entry for the doctor check %q (allChecks plus fold-error: %v; listed: %v)",
				where, name, sp6WantChecks(), got)
		}
	}
}

// TestDocClaim_Spec_DoctorCheckTableListsExactlyAllChecks asserts that the
// "Checks and `--fix` behavior" table in the `act doctor` section has exactly
// one row per name in allChecks plus fold-error, and that validChecks — the
// set `--check` validates against — is that same set.
func TestDocClaim_Spec_DoctorCheckTableListsExactlyAllChecks(t *testing.T) {
	if len(validChecks) != len(allChecks)+1 {
		t.Fatalf("validChecks has %d names, want allChecks (%d) plus fold-error", len(validChecks), len(allChecks))
	}
	table := sp6Between(t, sp6SpecBody(t), "**Checks and `--fix` behavior:**", "\n#### ")
	var rows []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([a-z0-9-]+)` \\|").FindAllStringSubmatch(table, -1) {
		rows = append(rows, m[1])
	}
	if len(rows) == 0 {
		t.Fatalf("found no rows in the doctor check table")
	}
	sp6CompareChecks(t, "the spec's doctor check table", rows)
}

// TestDocClaim_Spec_TestPlanDoctorCheckListMatchesAllChecks asserts the
// "Doctor coverage" paragraph of the test plan names the same set.
func TestDocClaim_Spec_TestPlanDoctorCheckListMatchesAllChecks(t *testing.T) {
	section := sp6Between(t, sp6SpecBody(t), "#### 7.6 Doctor coverage", "\n#### ")
	line := sp6Between(t, section, "For each check (", "):")
	var names []string
	for _, m := range regexp.MustCompile("`([a-z0-9-]+)`").FindAllStringSubmatch(line, -1) {
		names = append(names, m[1])
	}
	sp6CompareChecks(t, "the spec's test-plan check list (7.6)", names)
}
