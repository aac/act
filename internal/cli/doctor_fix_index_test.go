package cli

// Regression tests for act-f2f93a: `act doctor --fix-index` rebuilds a
// malformed .act/index.db from .act/ops/.
//
// Three scenarios are pinned:
//
//   1. Healthy index → index-malformed check is a no-op (no finding).
//   2. Malformed index without --fix-index → ERROR-severity finding whose
//      Message ends with the literal remediation hint
//      `rebuild with 'act doctor --fix-index'`. The literal is
//      load-bearing — TestDocClaim_DoctorFixIndex_StderrRemediationHint
//      in doctor_fix_index_docclaim_test.go pins it from the user-visible
//      boundary side.
//   3. Malformed index with --fix-index → backup created, index rebuilt
//      from ops/, exit 0, follow-up read commands work.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aac/act/internal/config"
	"github.com/aac/act/internal/index"
)

// corruptIndexFile scribbles 0xAA over page 2 of the index file (offset
// 4096..8192) while leaving the header intact. This mirrors the production
// failure shape — Open() succeeds, but the next page-tree read trips
// SQLITE_CORRUPT.
//
// To make corruption land in pages we actually populate, the helper first
// opens the index and forces a few schema applications + bulk inserts so
// the file grows past one page. Returns the absolute path so callers can
// assert the backup naming.
func corruptIndexFile(t *testing.T, root string) string {
	t.Helper()
	paths := config.Layout(root)

	// Make sure the index exists and has multi-page payload.
	idx, err := index.Open(paths.IndexDB)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	if err := idx.ApplySchema(); err != nil {
		t.Fatalf("seed apply schema: %v", err)
	}
	for i := 0; i < 64; i++ {
		_, _ = idx.DB().Exec(
			`INSERT OR REPLACE INTO issues (id, title, status) VALUES (?, 'padding', 'open')`,
			"act-pad"+string(rune('a'+i%26))+string(rune('0'+i%10)),
		)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	f, err := os.OpenFile(paths.IndexDB, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("scribble open: %v", err)
	}
	garbage := make([]byte, 4096)
	for i := range garbage {
		garbage[i] = 0xAA
	}
	if _, err := f.WriteAt(garbage, 4096); err != nil {
		t.Fatalf("scribble write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("scribble close: %v", err)
	}
	return paths.IndexDB
}

// TestRunDoctor_IndexMalformed_HealthyNoOp: a healthy index produces no
// index-malformed finding regardless of --fix-index.
func TestRunDoctor_IndexMalformed_HealthyNoOp(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")

	for _, fix := range []bool{false, true} {
		out, code := RunDoctor(root, DoctorOptions{Check: "index-malformed", FixIndex: fix})
		if code != 0 {
			t.Errorf("fix=%v: exit = %d, want 0; out=%+v", fix, code, out)
		}
		res := out.(DoctorResult)
		for _, f := range res.Findings {
			if f.Check == "index-malformed" {
				t.Errorf("fix=%v: unexpected finding on healthy index: %+v", fix, f)
			}
		}
	}
}

// TestRunDoctor_IndexMalformed_DetectsAndHintsWithoutFix: scribble over the
// index pages, run doctor without --fix-index, and confirm:
//   - exit code 1 (error finding)
//   - finding's Message contains the literal remediation hint
//   - the malformed file is left in place (no backup yet — only on --fix-index)
func TestRunDoctor_IndexMalformed_DetectsAndHintsWithoutFix(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	dbPath := corruptIndexFile(t, root)

	out, code := RunDoctor(root, DoctorOptions{Check: "index-malformed"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	var found *Finding
	for i := range res.Findings {
		if res.Findings[i].Check == "index-malformed" {
			found = &res.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no index-malformed finding; got %+v", res.Findings)
	}
	if found.Severity != "error" {
		t.Errorf("severity = %q, want error", found.Severity)
	}
	const wantHint = "rebuild with 'act doctor --fix-index'"
	if !strings.Contains(found.Message, wantHint) {
		t.Errorf("message %q missing remediation hint %q", found.Message, wantHint)
	}

	// File still on disk; no backup created yet.
	if _, err := os.Stat(dbPath); err != nil {
		t.Errorf("malformed file missing post-detect: %v", err)
	}
	matches, _ := filepath.Glob(dbPath + ".malformed-*")
	if len(matches) != 0 {
		t.Errorf("unexpected backup created on detect-only run: %v", matches)
	}
}

// TestRunDoctor_IndexMalformed_FixRebuildsFromOps: with --fix-index, the
// malformed file is moved to a .malformed-<ts> backup and the rebuilt index
// reflects the current op log. A follow-up `act ready` (the canonical
// read after recovery) returns successfully.
func TestRunDoctor_IndexMalformed_FixRebuildsFromOps(t *testing.T) {
	root := makeCreateRepo(t)
	a := mustCreate(t, root, "A")
	b := mustCreate(t, root, "B")
	dbPath := corruptIndexFile(t, root)

	out, code := RunDoctor(root, DoctorOptions{Check: "index-malformed", FixIndex: true})
	if code != 0 {
		t.Fatalf("fix exit = %d, want 0; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	var found *Finding
	for i := range res.Findings {
		if res.Findings[i].Check == "index-malformed" {
			found = &res.Findings[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no index-malformed finding after fix; got %+v", res.Findings)
	}
	if found.Severity != "warn" {
		t.Errorf("severity = %q, want warn", found.Severity)
	}
	if !strings.Contains(found.Message, "rebuilt") {
		t.Errorf("message %q missing 'rebuilt' summary", found.Message)
	}

	// Backup exists and is non-empty.
	matches, err := filepath.Glob(dbPath + ".malformed-*")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no backup file at %s.malformed-*: matches=%v err=%v", dbPath, matches, err)
	}
	info, statErr := os.Stat(matches[0])
	if statErr != nil || info.Size() == 0 {
		t.Errorf("backup file unusable: size=%d err=%v", info.Size(), statErr)
	}

	// Rebuilt file is healthy and contains both seeded issues.
	idx, oerr := index.Open(dbPath)
	if oerr != nil {
		t.Fatalf("post-fix open: %v", oerr)
	}
	defer idx.Close()
	if err := idx.IntegrityCheck(); err != nil {
		t.Errorf("post-fix integrity check: %v", err)
	}
	rows, err := idx.ListAll(index.Filter{})
	if err != nil {
		t.Fatalf("post-fix ListAll: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.ID] = true
	}
	if !seen[a] || !seen[b] {
		t.Errorf("rebuilt index missing seeded issues; got %v want %s+%s", rows, a, b)
	}

	// Subsequent doctor pass on the same check is clean.
	out, code = RunDoctor(root, DoctorOptions{Check: "index-malformed"})
	if code != 0 {
		t.Fatalf("post-fix re-run exit = %d, want 0; out=%+v", code, out)
	}
	res = out.(DoctorResult)
	for _, f := range res.Findings {
		if f.Check == "index-malformed" {
			t.Errorf("unexpected finding after rebuild: %+v", f)
		}
	}

	// Canonical read after recovery: `act ready` doesn't blow up.
	if _, rcode := RunReady(root, ReadyOptions{}); rcode != 0 {
		t.Errorf("act ready after rebuild: code=%d", rcode)
	}
}

// TestRunDoctor_IndexMalformed_FixIsNoopOnHealthy: --fix-index against a
// healthy index doesn't create a backup, doesn't rebuild, and returns 0.
func TestRunDoctor_IndexMalformed_FixIsNoopOnHealthy(t *testing.T) {
	root := makeCreateRepo(t)
	mustCreate(t, root, "A")
	paths := config.Layout(root)

	// Capture pre-state mtime as a cheap signal that we didn't rewrite.
	pre, err := os.Stat(paths.IndexDB)
	if err != nil {
		t.Fatalf("pre stat: %v", err)
	}

	out, code := RunDoctor(root, DoctorOptions{Check: "index-malformed", FixIndex: true})
	if code != 0 {
		t.Fatalf("exit = %d, want 0; out=%+v", code, out)
	}
	res := out.(DoctorResult)
	for _, f := range res.Findings {
		if f.Check == "index-malformed" {
			t.Errorf("unexpected finding on healthy --fix-index run: %+v", f)
		}
	}

	matches, _ := filepath.Glob(paths.IndexDB + ".malformed-*")
	if len(matches) != 0 {
		t.Errorf("unexpected backup on healthy --fix-index run: %v", matches)
	}
	post, err := os.Stat(paths.IndexDB)
	if err != nil {
		t.Fatalf("post stat: %v", err)
	}
	if !post.ModTime().Equal(pre.ModTime()) {
		t.Errorf("index mtime moved on healthy --fix-index run: pre=%v post=%v",
			pre.ModTime(), post.ModTime())
	}
}
