package cli

// Regression tests for act-cc65: `checkIndexSchema` must enumerate FTS5
// virtual tables alongside regular tables. SQLite stores FTS5 virtual
// tables in sqlite_master with type='table', so a plain `type='table'`
// filter is sufficient. The prior query
//   SELECT name FROM sqlite_master WHERE type IN ('table','virtual') OR type='table'
// masked that the IN list was wrong: dropping the redundant OR would have
// broken FTS detection. These tests pin both directions:
//
//   1. With a healthy, fully-applied schema (FTS table present),
//      checkIndexSchema reports no index-schema finding — proving the
//      query observes the FTS5 virtual table.
//   2. If the FTS table is dropped, checkIndexSchema surfaces a missing
//      "fts" entry — proving the check still distinguishes present from
//      absent for the virtual table.

import (
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/index"
)

// TestRunDoctor_IndexSchema_FTSDetected: a freshly seeded repo with a real
// index.db (built via the canonical schema, including the FTS5 virtual
// table) produces no index-schema finding.
func TestRunDoctor_IndexSchema_FTSDetected(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")

	out, code := RunDoctor(root, DoctorOptions{Check: "index-schema"})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	for _, f := range res.Findings {
		if f.Check == "index-schema" {
			t.Errorf("unexpected index-schema finding on healthy index: %+v", f)
		}
	}
}

// TestRunDoctor_IndexSchema_MissingFTSReported: drop the FTS5 virtual
// table out from under the index, then confirm checkIndexSchema reports
// "fts" as missing. This pins the negative direction: the query is
// genuinely observing the FTS table, not silently passing because the
// loop never reached it.
func TestRunDoctor_IndexSchema_MissingFTSReported(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")

	paths := config.Layout(root)
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := idx.DB().Exec(`DROP TABLE fts`); err != nil {
		_ = idx.Close()
		t.Fatalf("drop fts: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	out, code := RunDoctor(root, DoctorOptions{Check: "index-schema"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	var found *Finding
	for i := range res.Findings {
		if res.Findings[i].Check == "index-schema" {
			found = &res.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no index-schema finding; got %+v", res.Findings)
	}
	if found.Severity != "error" {
		t.Errorf("severity = %q, want error", found.Severity)
	}
	if !strings.Contains(found.Message, "fts") {
		t.Errorf("message %q missing 'fts' in missing-tables list", found.Message)
	}
}
