package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// spwScribbleIndex overwrites the second 4096-byte page of .act/index.db with
// garbage. The file still opens as a SQLite database, and the first statement
// that walks it answers "database disk image is malformed".
func spwScribbleIndex(t *testing.T, dir string) {
	t.Helper()
	db := filepath.Join(dir, ".act", "index.db")
	info, err := os.Stat(db)
	if err != nil || info.Size() < 8192 {
		t.Fatalf("premise: want an index.db of at least two pages, got %v (err=%v)", info, err)
	}
	f, err := os.OpenFile(db, os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", db, err)
	}
	garbage := []byte(strings.Repeat("\xaa", 4096))
	if _, err := f.WriteAt(garbage, 4096); err != nil {
		t.Fatalf("scribble %s: %v", db, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", db, err)
	}
}

// spwOpFiles counts the op files in the tracker.
func spwOpFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	_ = filepath.Walk(filepath.Join(dir, ".act", "ops"), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// spwTrackerHead is the nested tracker repo's HEAD commit.
func spwTrackerHead(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", filepath.Join(dir, ".act"), "rev-parse", "HEAD").CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse HEAD in .act: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// spwOpsClean reports whether .act/ops has nothing uncommitted.
func spwOpsClean(t *testing.T, dir string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", filepath.Join(dir, ".act"), "status", "--porcelain", "--", "ops").CombinedOutput()
	if err != nil {
		t.Fatalf("git status in .act: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out)) == ""
}

// spwMalformedSite is a tracker with two open issues whose index was
// scribbled on before the command under test starts.
func spwMalformedSite(t *testing.T) (dir, a, b string) {
	t.Helper()
	dir = sp6Site(t)
	a = createIssue(t, dir, "spw seed a")
	b = createIssue(t, dir, "spw seed b")
	spwScribbleIndex(t, dir)
	return dir, a, b
}

// TestDocClaim_Spec_IndexMalformedBeforeTheCommandStarts pins the first half
// of the `index-malformed` recovery paragraph (act-4d7596): with the index
// already malformed, close, dep add, ready and update --claim exit 1 naming
// SQLite's error, and leave the op log and the tracker's history untouched.
func TestDocClaim_Spec_IndexMalformedBeforeTheCommandStarts(t *testing.T) {
	const malformed = "database disk image is malformed"
	cases := []struct {
		name string
		args func(a, b string) []string
	}{
		{"close", func(a, b string) []string { return []string{"close", a} }},
		{"dep add", func(a, b string) []string { return []string{"dep", "add", a, b} }},
		{"ready", func(a, b string) []string { return []string{"ready"} }},
		{"update --claim", func(a, b string) []string { return []string{"update", "--claim", a} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, a, b := spwMalformedSite(t)
			ops, head := spwOpFiles(t, dir), spwTrackerHead(t, dir)

			out, stderr, code := runActIn(t, dir, tc.args(a, b)...)
			if code != 1 {
				t.Errorf("exit %d, want 1\nstdout=%s\nstderr=%s", code, out, stderr)
			}
			if !strings.Contains(stderr, malformed) {
				t.Errorf("stderr does not carry %q:\n%s", malformed, stderr)
			}
			if got := spwOpFiles(t, dir); got != ops {
				t.Errorf("op files went %d -> %d, want no op written", ops, got)
			}
			if got := spwTrackerHead(t, dir); got != head {
				t.Errorf("the tracker repo gained a commit (%s -> %s), want none", head, got)
			}

			// Under --json the same failure is the error envelope on stdout.
			jsonArgs := append(tc.args(a, b), "--json")
			out, stderr, code = runActIn(t, dir, jsonArgs...)
			if code != 1 || !strings.Contains(out, `"error"`) || !strings.Contains(out, malformed) {
				t.Errorf("--json: exit %d, want 1 and an error envelope carrying %q\nstdout=%s\nstderr=%s", code, malformed, out, stderr)
			}
			if got := spwOpFiles(t, dir); got != ops {
				t.Errorf("--json: op files went %d -> %d, want no op written", ops, got)
			}
		})
	}
}

// TestDocClaim_Spec_IndexMalformedCreateAndFieldUpdateWriteFirst pins the
// second half: create, and an update that edits a field of an issue named by
// its full id, write and commit their op with the index already malformed.
// create exits 0 with the index_update_failed warning. What update exits with
// is deliberately not asserted here; the spec sentence does not say.
func TestDocClaim_Spec_IndexMalformedCreateAndFieldUpdateWriteFirst(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		dir, _, _ := spwMalformedSite(t)
		ops, head := spwOpFiles(t, dir), spwTrackerHead(t, dir)

		res, _ := wfxCreate(t, dir, "spw created over a malformed index") // wants exit 0
		if !res.Committed || strings.Join(res.Warnings, ",") != "index_update_failed" {
			t.Errorf("create: committed %v, warnings %v; want committed and exactly [index_update_failed]", res.Committed, res.Warnings)
		}
		if got := spwOpFiles(t, dir); got != ops+1 {
			t.Errorf("create: op files went %d -> %d, want one op written", ops, got)
		}
		if spwTrackerHead(t, dir) == head || !spwOpsClean(t, dir) {
			t.Errorf("create: want the op committed in the tracker repo (head moved, ops clean)")
		}
	})

	t.Run("update a field by full id", func(t *testing.T) {
		dir, a, _ := spwMalformedSite(t)
		ops, head := spwOpFiles(t, dir), spwTrackerHead(t, dir)

		out, stderr, _ := runActIn(t, dir, "update", "--json", "--title", "spw renamed", a)
		if got := spwOpFiles(t, dir); got != ops+1 {
			t.Errorf("update --title: op files went %d -> %d, want one op written\nstdout=%s\nstderr=%s", ops, got, out, stderr)
		}
		if spwTrackerHead(t, dir) == head || !spwOpsClean(t, dir) {
			t.Errorf("update --title: want the op committed in the tracker repo (head moved, ops clean)\nstdout=%s\nstderr=%s", out, stderr)
		}
	})
}

// TestDocClaim_Spec_CreateHasThreeWarnings pins the count in the `act create`
// warnings paragraph by producing each of the three: parent_closed,
// push_no_remote and index_update_failed.
func TestDocClaim_Spec_CreateHasThreeWarnings(t *testing.T) {
	dir := sp6Site(t)
	parent := createIssue(t, dir, "spw parent")
	if out, stderr, code := runActIn(t, dir, "close", "--json", parent); code != 0 {
		t.Fatalf("close the parent: exit %d\nstdout=%s\nstderr=%s", code, out, stderr)
	}

	closedParent, _ := wfxCreate(t, dir, "--parent", parent, "spw child of a closed parent")
	if got := strings.Join(closedParent.Warnings, ","); got != "parent_closed" {
		t.Errorf("create --parent <closed>: warnings [%s], want [parent_closed]", got)
	}
	noRemote, _ := wfxCreate(t, dir, "--push", "spw pushed nowhere")
	if got := strings.Join(noRemote.Warnings, ","); got != "push_no_remote" {
		t.Errorf("create --push with no remote: warnings [%s], want [push_no_remote]", got)
	}
	spwScribbleIndex(t, dir)
	brokenIndex, _ := wfxCreate(t, dir, "spw over a malformed index")
	if got := strings.Join(brokenIndex.Warnings, ","); got != "index_update_failed" {
		t.Errorf("create over a malformed index: warnings [%s], want [index_update_failed]", got)
	}
}
